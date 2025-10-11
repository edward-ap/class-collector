// Package cache contains snapshot/delta logic for incremental bundles.
package cache

import (
	"sort"
	"strings"
)

type deltaChange = struct {
	Path       string `json:"path"`
	HashBefore string `json:"hashBefore"`
	HashAfter  string `json:"hashAfter"`
	DiffPath   string `json:"diff"`
	Oversize   bool   `json:"oversize"`
}

type deltaRename = struct {
	From string `json:"from"`
	To   string `json:"to"`
	Hash string `json:"hash"`
}

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

// BuildDelta computes the change set between two snapshots.
func BuildDelta(prev *Snapshot, curr *Snapshot) Delta {
	if delta, ok := handleTrivialDelta(prev, curr); ok {
		return delta
	}

	prevMap := indexByPath(prev.Files)
	currMap := indexByPath(curr.Files)

	removed, changed := classifyRemovedAndChanged(prevMap, currMap)
	added := classifyAdded(prevMap, currMap)

	delta := Delta{
		Removed: removed,
		Added:   added,
		Changed: changed,
	}

	renamed, keepRemoved, keepAdded := matchExactRenames(delta.Removed, delta.Added)
	delta.Renamed = append(delta.Renamed, renamed...)
	delta.Removed = keepRemoved
	delta.Added = keepAdded

	if enableSimRename {
		applySimilarityRenames(&delta)
	}

	sortDelta(&delta)
	return delta
}

func handleTrivialDelta(prev, curr *Snapshot) (Delta, bool) {
	var d Delta
	switch {
	case curr == nil || len(curr.Files) == 0:
		if prev != nil {
			d.Removed = append(d.Removed, prev.Files...)
			sort.Slice(d.Removed, func(i, j int) bool { return d.Removed[i].Path < d.Removed[j].Path })
		}
		return d, true
	case prev == nil || len(prev.Files) == 0:
		if curr != nil {
			d.Added = append(d.Added, curr.Files...)
			sort.Slice(d.Added, func(i, j int) bool { return d.Added[i].Path < d.Added[j].Path })
		}
		return d, true
	default:
		return Delta{}, false
	}
}

func indexByPath(files []SnapFile) map[string]SnapFile {
	m := make(map[string]SnapFile, len(files))
	for _, f := range files {
		m[f.Path] = f
	}
	return m
}

func classifyRemovedAndChanged(prev, curr map[string]SnapFile) ([]SnapFile, []deltaChange) {
	removed := make([]SnapFile, 0)
	changed := make([]deltaChange, 0)
	for path, pf := range prev {
		if cf, ok := curr[path]; ok {
			if pf.Hash != cf.Hash {
				changed = append(changed, deltaChange{
					Path:       path,
					HashBefore: pf.Hash,
					HashAfter:  cf.Hash,
				})
			}
			continue
		}
		removed = append(removed, pf)
	}
	return removed, changed
}

func classifyAdded(prev, curr map[string]SnapFile) []SnapFile {
	added := make([]SnapFile, 0)
	for path, cf := range curr {
		if _, ok := prev[path]; !ok {
			added = append(added, cf)
		}
	}
	return added
}

func matchExactRenames(removed, added []SnapFile) ([]deltaRename, []SnapFile, []SnapFile) {
	if len(removed) == 0 || len(added) == 0 {
		return nil, removed, added
	}
	type remInfo struct {
		idx  int
		path string
	}
	byHash := make(map[string][]remInfo, len(removed))
	for idx, rf := range removed {
		byHash[rf.Hash] = append(byHash[rf.Hash], remInfo{idx: idx, path: rf.Path})
	}
	for h := range byHash {
		list := byHash[h]
		sort.Slice(list, func(i, j int) bool { return list[i].path < list[j].path })
		byHash[h] = list
	}
	type addInfo struct {
		idx  int
		path string
	}
	addInfos := make([]addInfo, len(added))
	for i, af := range added {
		addInfos[i] = addInfo{idx: i, path: af.Path}
	}
	sort.Slice(addInfos, func(i, j int) bool { return addInfos[i].path < addInfos[j].path })

	usedRemoved := make(map[int]bool)
	usedAdded := make(map[int]bool)
	renamed := make([]deltaRename, 0)
	for _, info := range addInfos {
		af := added[info.idx]
		cands := byHash[af.Hash]
		if len(cands) == 0 {
			continue
		}
		cand := cands[0]
		if len(cands) == 1 {
			delete(byHash, af.Hash)
		} else {
			byHash[af.Hash] = cands[1:]
		}
		usedRemoved[cand.idx] = true
		usedAdded[info.idx] = true
		renamed = append(renamed, deltaRename{
			From: removed[cand.idx].Path,
			To:   af.Path,
			Hash: af.Hash,
		})
	}
	return renamed, filterSnapFiles(removed, usedRemoved), filterSnapFiles(added, usedAdded)
}

type renameCandidate struct {
	removedIdx int
	addedIdx   int
}

type scoredRename struct {
	removedIdx int
	addedIdx   int
	score      int
	toPath     string
}

type hashEntry struct {
	hash uint64
	ok   bool
}

func applySimilarityRenames(d *Delta) {
	if len(d.Removed) == 0 || len(d.Added) == 0 {
		return
	}
	prov := getProvider()
	if prov == nil {
		return
	}
	pairs := collectRenameCandidates(d)
	if len(pairs) == 0 {
		return
	}
	scored := scoreRenameCandidates(d, pairs, prov)
	if len(scored) == 0 {
		return
	}
	renames, usedRemoved, usedAdded := pickScoredRenames(d, scored)
	if len(renames) == 0 {
		return
	}
	d.Renamed = append(d.Renamed, renames...)
	d.Removed = filterSnapFiles(d.Removed, usedRemoved)
	d.Added = filterSnapFiles(d.Added, usedAdded)
}

func collectRenameCandidates(d *Delta) []renameCandidate {
	pairs := make([]renameCandidate, 0)
	for i, rf := range d.Removed {
		for j, af := range d.Added {
			if rf.Hash == af.Hash {
				continue
			}
			if rf.Lines == 0 || af.Lines == 0 {
				pairs = append(pairs, renameCandidate{removedIdx: i, addedIdx: j})
				continue
			}
			a, b := rf.Lines, af.Lines
			if a < b {
				a, b = b, a
			}
			if b == 0 || a <= 2*b {
				pairs = append(pairs, renameCandidate{removedIdx: i, addedIdx: j})
			}
		}
	}
	return pairs
}

func scoreRenameCandidates(d *Delta, pairs []renameCandidate, prov ContentProvider) []scoredRename {
	remCache := make(map[int]hashEntry)
	addCache := make(map[int]hashEntry)
	scored := make([]scoredRename, 0, len(pairs))
	for _, pair := range pairs {
		ha, oka := loadSimHash(pair.removedIdx, d.Removed, true, prov, remCache)
		hb, okb := loadSimHash(pair.addedIdx, d.Added, false, prov, addCache)
		if !oka || !okb {
			continue
		}
		dist := hamming64(ha, hb)
		if dist <= simThresh {
			scored = append(scored, scoredRename{
				removedIdx: pair.removedIdx,
				addedIdx:   pair.addedIdx,
				score:      dist,
				toPath:     d.Added[pair.addedIdx].Path,
			})
		}
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].toPath != scored[j].toPath {
			return scored[i].toPath < scored[j].toPath
		}
		if scored[i].score != scored[j].score {
			return scored[i].score < scored[j].score
		}
		return d.Removed[scored[i].removedIdx].Path < d.Removed[scored[j].removedIdx].Path
	})
	return scored
}

func loadSimHash(idx int, files []SnapFile, old bool, prov ContentProvider, cache map[int]hashEntry) (uint64, bool) {
	if entry, ok := cache[idx]; ok {
		return entry.hash, entry.ok
	}
	data, err := prov.Read(files[idx].Path, old)
	if err != nil {
		cache[idx] = hashEntry{ok: false}
		return 0, false
	}
	hash := simHash64(normalizeForSim(string(data)))
	cache[idx] = hashEntry{hash: hash, ok: true}
	return hash, true
}

func pickScoredRenames(d *Delta, scored []scoredRename) ([]deltaRename, map[int]bool, map[int]bool) {
	usedRemoved := make(map[int]bool)
	usedAdded := make(map[int]bool)
	renames := make([]deltaRename, 0, len(scored))
	for _, s := range scored {
		if usedRemoved[s.removedIdx] || usedAdded[s.addedIdx] {
			continue
		}
		usedRemoved[s.removedIdx] = true
		usedAdded[s.addedIdx] = true
		renames = append(renames, deltaRename{
			From: d.Removed[s.removedIdx].Path,
			To:   d.Added[s.addedIdx].Path,
			Hash: d.Added[s.addedIdx].Hash,
		})
	}
	return renames, usedRemoved, usedAdded
}

func filterSnapFiles(files []SnapFile, used map[int]bool) []SnapFile {
	if len(used) == 0 {
		return files
	}
	out := make([]SnapFile, 0, len(files)-len(used))
	for idx, f := range files {
		if !used[idx] {
			out = append(out, f)
		}
	}
	return out
}

func sortDelta(d *Delta) {
	sort.Slice(d.Removed, func(i, j int) bool { return d.Removed[i].Path < d.Removed[j].Path })
	sort.Slice(d.Added, func(i, j int) bool { return d.Added[i].Path < d.Added[j].Path })
	sort.Slice(d.Changed, func(i, j int) bool { return d.Changed[i].Path < d.Changed[j].Path })
	sort.Slice(d.Renamed, func(i, j int) bool {
		if d.Renamed[i].From == d.Renamed[j].From {
			return d.Renamed[i].To < d.Renamed[j].To
		}
		return d.Renamed[i].From < d.Renamed[j].From
	})
}

func normalizeForSim(s string) []string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
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
			if r >= 'a' && r <= 'z' {
				return false
			}
			if r >= 'A' && r <= 'Z' {
				return false
			}
			if r >= '0' && r <= '9' {
				return false
			}
			return true
		})
		for _, t := range toks {
			h := fnv64(t)
			for b := 0; b < 64; b++ {
				if (h>>uint(b))&1 == 1 {
					vec[b] += 1
				} else {
					vec[b] -= 1
				}
			}
		}
	}
	var out uint64
	for b := 0; b < 64; b++ {
		if vec[b] >= 0 {
			out |= 1 << uint(b)
		}
	}
	return out
}

func fnv64(s string) uint64 {
	const off uint64 = 1469598103934665603
	const prm uint64 = 1099511628211
	h := off
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prm
	}
	return h
}
