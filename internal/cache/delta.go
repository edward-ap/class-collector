// Package cache contains snapshot/delta logic for incremental bundles.
package cache

import (
	"sort"
	"strings"
)

// BuildDelta computes the change set between two snapshots.
//
// Behavior:
//   - If prev is nil or has no files, all files in curr are reported as Added.
//   - If curr is nil or has no files, all files in prev are reported as Removed.
//   - "Changed" is detected when a file path exists in both snapshots but its hash differs.
//   - "Renamed" is detected as a move of identical content (same hash) from a path
//     that no longer exists in curr to a new path in curr (one-to-one, deterministic).
//
// Determinism:
//   - Output slices are sorted to keep delta archives reproducible.
var (
	enableSimRename bool
	simThresh       = 8
)

// SetRenameSimilarity configures the optional similarity-based rename pass.
func SetRenameSimilarity(enable bool, thresh int) {
	enableSimRename = enable
	if thresh > 0 {
		simThresh = thresh
	}
}

func BuildDelta(prev *Snapshot, curr *Snapshot) Delta {
	var d Delta

	// Handle degenerate cases early.
	switch {
	case curr == nil || len(curr.Files) == 0:
		// Everything from prev is removed.
		if prev != nil {
			d.Removed = append(d.Removed, prev.Files...)
			sort.Slice(d.Removed, func(i, j int) bool { return d.Removed[i].Path < d.Removed[j].Path })
		}
		return d
	case prev == nil || len(prev.Files) == 0:
		// Everything in curr is added.
		d.Added = append(d.Added, curr.Files...)
		sort.Slice(d.Added, func(i, j int) bool { return d.Added[i].Path < d.Added[j].Path })
		return d
	}

	// Index by path for quick membership/hash checks.
	byPathPrev := make(map[string]SnapFile, len(prev.Files))
	for _, f := range prev.Files {
		byPathPrev[f.Path] = f
	}
	byPathCurr := make(map[string]SnapFile, len(curr.Files))
	for _, f := range curr.Files {
		byPathCurr[f.Path] = f
	}

	// 1) Classify Removed / Changed
	for p, pf := range byPathPrev {
		cf, ok := byPathCurr[p]
		if !ok {
			d.Removed = append(d.Removed, pf)
			continue
		}
		if pf.Hash != cf.Hash {
			// IMPORTANT: match the exact anonymous type (with JSON tags) used by Delta.Changed.
			d.Changed = append(d.Changed, struct {
				Path       string `json:"path"`
				HashBefore string `json:"hashBefore"`
				HashAfter  string `json:"hashAfter"`
				DiffPath   string `json:"diff"`
				Oversize   bool   `json:"oversize"`
			}{
				Path:       p,
				HashBefore: pf.Hash,
				HashAfter:  cf.Hash,
			})
		}
	}

	// 2) Classify Added
	for p, cf := range byPathCurr {
		if _, ok := byPathPrev[p]; !ok {
			d.Added = append(d.Added, cf)
		}
	}

	// 3) Rename detection (same hash, from removed → to added).
	// Build map hash → list of candidate "from" paths (removed only), sorted.
	byHashPrevRemoved := make(map[string][]string, len(d.Removed))
	for _, rf := range d.Removed {
		byHashPrevRemoved[rf.Hash] = append(byHashPrevRemoved[rf.Hash], rf.Path)
	}
	for h := range byHashPrevRemoved {
		paths := byHashPrevRemoved[h]
		sort.Strings(paths)
		byHashPrevRemoved[h] = paths
	}

	// Match each "added" file (new path) to one removed path with the same hash.
	// We consume candidates to ensure one-to-one mapping.
	if len(d.Added) > 0 {
		// Work on a sorted view for deterministic pairing.
		sort.Slice(d.Added, func(i, j int) bool { return d.Added[i].Path < d.Added[j].Path })
		for _, af := range d.Added {
			cands := byHashPrevRemoved[af.Hash]
			if len(cands) == 0 {
				continue
			}
			from := cands[0]
			// Consume the candidate.
			if len(cands) == 1 {
				delete(byHashPrevRemoved, af.Hash)
			} else {
				byHashPrevRemoved[af.Hash] = cands[1:]
			}
			// IMPORTANT: match the exact anonymous type (with JSON tags) used by Delta.Renamed.
			d.Renamed = append(d.Renamed, struct {
				From string `json:"from"`
				To   string `json:"to"`
				Hash string `json:"hash"`
			}{
				From: from,
				To:   af.Path,
				Hash: af.Hash,
			})
		}
	}

	// 4) De-duplicate Added/Removed for items we classified as Renamed.
	if len(d.Renamed) > 0 {
		remFrom := make(map[string]struct{}, len(d.Renamed))
		addTo := make(map[string]struct{}, len(d.Renamed))
		for _, r := range d.Renamed {
			remFrom[r.From] = struct{}{}
			addTo[r.To] = struct{}{}
		}
		// Filter Added
		filteredAdded := d.Added[:0]
		for _, a := range d.Added {
			if _, isRename := addTo[a.Path]; !isRename {
				filteredAdded = append(filteredAdded, a)
			}
		}
		d.Added = filteredAdded
		// Filter Removed
		filteredRemoved := d.Removed[:0]
		for _, r := range d.Removed {
			if _, isRename := remFrom[r.Path]; !isRename {
				filteredRemoved = append(filteredRemoved, r)
			}
		}
		d.Removed = filteredRemoved
	}

	// Optional similarity-based rename detection (after exact-hash pass)
	if enableSimRename {
		applySimilarityRenames(&d)
	}

	// 5) Final deterministic ordering for reproducible archives.
	sort.Slice(d.Added, func(i, j int) bool { return d.Added[i].Path < d.Added[j].Path })
	sort.Slice(d.Removed, func(i, j int) bool { return d.Removed[i].Path < d.Removed[j].Path })
	sort.Slice(d.Changed, func(i, j int) bool { return d.Changed[i].Path < d.Changed[j].Path })
 sort.Slice(d.Renamed, func(i, j int) bool { return d.Renamed[i].To < d.Renamed[j].To })

	return d
}

// ---------------- Similarity-based rename pass ----------------

// applySimilarityRenames matches Removed×Added by SimHash similarity (64-bit) with
// Hamming distance <= simThresh, consuming pairs deterministically.
func applySimilarityRenames(d *Delta) {
	if d == nil || len(d.Removed) == 0 || len(d.Added) == 0 {
		return
	}
	prov := getProvider()
	if prov == nil {
		return
	}
	// size ratio prefilter 0.5..2.0
	type cand struct{ i, j int }
	pairs := make([]cand, 0, len(d.Removed)*len(d.Added))
	for i := range d.Removed {
		for j := range d.Added {
			sza, szb := d.Removed[i].Lines, d.Added[j].Lines
			// If Lines is not populated, fallback to Size via SnapFile fields
			if sza == 0 || szb == 0 {
				// We don't have Sizes on SnapFile; skip ratio check in that case
				pairs = append(pairs, cand{i, j})
				continue
			}
			var a, b int
			if sza > szb { a, b = sza, szb } else { a, b = szb, sza }
			if a <= 2*b { pairs = append(pairs, cand{i, j}) }
		}
	}
	if len(pairs) == 0 { return }

	// Cache hashes
	type hentry struct{ h uint64; ok bool }
	remH := make(map[int]hentry)
	addH := make(map[int]hentry)
	readHash := func(idx int, old bool) (uint64, bool) {
		if old {
			if v, ok := remH[idx]; ok { return v.h, v.ok }
		} else {
			if v, ok := addH[idx]; ok { return v.h, v.ok }
		}
		var path string
		if old { path = d.Removed[idx].Path } else { path = d.Added[idx].Path }
		data, err := prov.Read(path, old)
		if err != nil {
			if old { remH[idx] = hentry{0,false} } else { addH[idx] = hentry{0,false} }
			return 0, false
		}
		h := simHash64(normalizeForSim(string(data)))
		if old { remH[idx] = hentry{h,true} } else { addH[idx] = hentry{h,true} }
		return h, true
	}

	type scored struct { i, j int; score int; toPath string }
	scoredPairs := make([]scored, 0, len(pairs))
	for _, p := range pairs {
		ha, oka := readHash(p.i, true)
		hb, okb := readHash(p.j, false)
		if !oka || !okb { continue }
 	dist := hamming64(ha, hb)
		if dist <= simThresh {
			scoredPairs = append(scoredPairs, scored{i:p.i, j:p.j, score:dist, toPath:d.Added[p.j].Path})
		}
	}
	if len(scoredPairs) == 0 { return }
	// Deterministic: sort by toPath asc, score asc, fromPath asc
	sort.Slice(scoredPairs, func(a, b int) bool {
		if scoredPairs[a].toPath != scoredPairs[b].toPath { return scoredPairs[a].toPath < scoredPairs[b].toPath }
		if scoredPairs[a].score != scoredPairs[b].score { return scoredPairs[a].score < scoredPairs[b].score }
		return d.Removed[scoredPairs[a].i].Path < d.Removed[scoredPairs[b].i].Path
	})
	usedRem := make(map[int]bool)
	usedAdd := make(map[int]bool)
	ren := make([]struct{From,To,Hash string}, 0, len(scoredPairs))
	for _, s := range scoredPairs {
		if usedRem[s.i] || usedAdd[s.j] { continue }
		usedRem[s.i] = true
		usedAdd[s.j] = true
		ren = append(ren, struct{From,To,Hash string}{From:d.Removed[s.i].Path, To:d.Added[s.j].Path, Hash:d.Added[s.j].Hash})
	}
	if len(ren) == 0 { return }
	// Filter Removed/Added and append Renamed
	filter := func(xs []SnapFile, used map[int]bool) []SnapFile {
		out := make([]SnapFile, 0, len(xs))
		for i := range xs { if !used[i] { out = append(out, xs[i]) } }
		return out
	}
	d.Removed = filter(d.Removed, usedRem)
	d.Added = filter(d.Added, usedAdd)
	for _, r := range ren {
		d.Renamed = append(d.Renamed, struct{From string `json:"from"`; To string `json:"to"`; Hash string `json:"hash"`}{From:r.From, To:r.To, Hash:r.Hash})
	}
}

func normalizeForSim(s string) []string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" { continue }
		ln = strings.Join(strings.Fields(ln), " ")
		out = append(out, ln)
	}
	return out
}

func hamming64(a, b uint64) int {
	x := a ^ b
	return bitsOnesCount64(x)
}

func bitsOnesCount64(x uint64) int {
	x = x - ((x >> 1) & 0x5555555555555555)
	x = (x & 0x3333333333333333) + ((x >> 2) & 0x3333333333333333)
	return int((((x + (x >> 4)) & 0x0F0F0F0F0F0F0F0F) * 0x0101010101010101) >> 56)
}

// simHash64 computes a 64-bit SimHash over normalized tokens.
func simHash64(lines []string) uint64 {
	vec := [64]int64{}
	for _, ln := range lines {
		toks := strings.FieldsFunc(ln, func(r rune) bool {
			if r >= 'a' && r <= 'z' { return false }
			if r >= 'A' && r <= 'Z' { return false }
			if r >= '0' && r <= '9' { return false }
			return true
		})
		for _, t := range toks {
			h := fnv64(t)
			for b := 0; b < 64; b++ {
				if (h>>uint(b))&1 == 1 { vec[b] += 1 } else { vec[b] -= 1 }
			}
		}
	}
	var out uint64
	for b := 0; b < 64; b++ { if vec[b] >= 0 { out |= 1 << uint(b) } }
	return out
}

func fnv64(s string) uint64 {
	const off uint64 = 1469598103934665603
	const prm uint64 = 1099511628211
	h := off
	for i := 0; i < len(s); i++ { h ^= uint64(s[i]); h *= prm }
	return h
}
