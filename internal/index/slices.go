// Package index — slice generation helpers.
//
// This file converts anchors (regions) and/or file length into coarse-grained
// "slices" used by downstream tooling for focused navigation or preview.
//
// Rules:
//   - If anchors exist, we emit one slice per (normalized) anchor.
//   - Otherwise, for large files we chunk the file into consecutive ranges
//     of at most maxFileLines lines (1-based, inclusive).
//   - Output is deterministic: anchors are normalized (clamped, sorted, deduped)
//     and chunk slices are emitted in ascending order.
package index

import (
	"fmt"
	"sort"
)

// BuildSlices creates per-file slices based on anchors or by chunking.
//
//	relPath     — project-relative path (stored into Slice.Path)
//	anchors     — extracted region anchors (may be empty or overlapping)
//	totalLines  — total number of lines in the file (1-based)
//	maxFileLines— maximum lines per chunk for non-anchored files; if <=0,
//	              the entire file becomes a single chunk.
//
// Behavior:
//   - When anchors are present, they take precedence: one slice per anchor.
//     Anchors are clamped to [1..totalLines], sorted, and exact duplicates removed.
//   - When no anchors are present:
//   - if totalLines <= maxFileLines → no slices (file small enough);
//   - else → consecutive "chunk_<start>" slices covering [1..totalLines].
func BuildSlices(relPath string, anchors []Anchor, totalLines, maxFileLines int) []Slice {
	// Normalize totalLines; ensure at least 1 to avoid negative/zero ranges.
	if totalLines < 1 {
		totalLines = 1
	}

	// 1) Anchor-backed slices
	if len(anchors) > 0 {
		na := normalizeAnchorsForSlices(anchors, totalLines)
		if len(na) == 0 {
			return nil
		}
		out := make([]Slice, 0, len(na))
		for _, a := range na {
			out = append(out, Slice{
				Path:  relPath,
				Slice: a.Name,
				Start: a.Start,
				End:   a.End,
			})
		}
		return out
	}

	// 2) Chunking for large files without anchors
	if maxFileLines <= 0 {
		// Non-positive threshold means "one chunk for the whole file".
		return []Slice{{
			Path:  relPath,
			Slice: "chunk_1",
			Start: 1,
			End:   totalLines,
		}}
	}
	if totalLines <= maxFileLines {
		// Small file: no need to emit slices.
		return nil
	}

	var slices []Slice
	for s := 1; s <= totalLines; s += maxFileLines {
		e := s + maxFileLines - 1
		if e > totalLines {
			e = totalLines
		}
		slices = append(slices, Slice{
			Path:  relPath,
			Slice: fmt.Sprintf("chunk_%d", s),
			Start: s,
			End:   e,
		})
	}
	return slices
}

// normalizeAnchorsForSlices clamps anchors to [1..total] range,
// sorts them by (Start, End, Name), and removes exact duplicates.
func normalizeAnchorsForSlices(in []Anchor, total int) []Anchor {
	if len(in) == 0 {
		return nil
	}
	out := make([]Anchor, 0, len(in))
	for _, a := range in {
		start := a.Start
		end := a.End
		if start < 1 {
			start = 1
		}
		if end < start {
			end = start
		}
		if end > total {
			end = total
		}
		out = append(out, Anchor{
			Name:  a.Name,
			Start: start,
			End:   end,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		if out[i].End != out[j].End {
			return out[i].End < out[j].End
		}
		return out[i].Name < out[j].Name
	})
	// Deduplicate exact (Name, Start, End) matches
	if len(out) <= 1 {
		return out
	}
	uniq := out[:1]
	for k := 1; k < len(out); k++ {
		prev := uniq[len(uniq)-1]
		cur := out[k]
		if !(cur.Name == prev.Name && cur.Start == prev.Start && cur.End == prev.End) {
			uniq = append(uniq, cur)
		}
	}
	return uniq
}
