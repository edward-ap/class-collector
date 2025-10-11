// Package index — symbol-based jump pointers.
//
// This file converts extracted Symbol entries into jump Pointers that downstream
// tools (or UIs) can use to navigate by fully-qualified symbol names.
//
// Design choices:
//   - Deterministic: output is sorted for reproducible bundles.
//   - Stable IDs: base ID is the symbol with '.' replaced by '-'.
//   - Uniqueness: duplicate IDs (e.g., overloaded methods without signature
//     disambiguation) are made unique via numeric suffixes -2, -3, …
//   - Minimal fields: we populate {ID, Path, Sym, Start, End}.
package index

import (
	"sort"
	"strconv"
	"strings"
)

// BuildSymbolPointers creates jump pointers from a flat list of symbols.
// Base ID = symbol with dots replaced by dashes. If multiple symbols collapse
// to the same ID (e.g., overloaded methods), we add a numeric suffix "-N".
func BuildSymbolPointers(symbols []Symbol) []Pointer {
	if len(symbols) == 0 {
		return nil
	}

	index := buildPointerIndex(symbols)
	if len(index) == 0 {
		return nil
	}
	unique := dedupPointers(index)
	sort.Slice(unique, func(i, j int) bool {
		if unique[i].ID != unique[j].ID {
			return unique[i].ID < unique[j].ID
		}
		if unique[i].Path != unique[j].Path {
			return unique[i].Path < unique[j].Path
		}
		if unique[i].Start != unique[j].Start {
			return unique[i].Start < unique[j].Start
		}
		return unique[i].End < unique[j].End
	})
	return unique
}

func buildPointerIndex(symbols []Symbol) []Pointer {
	sorted := make([]Symbol, 0, len(symbols))
	sorted = append(sorted, symbols...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Symbol != sorted[j].Symbol {
			return sorted[i].Symbol < sorted[j].Symbol
		}
		if sorted[i].Path != sorted[j].Path {
			return sorted[i].Path < sorted[j].Path
		}
		if sorted[i].Start != sorted[j].Start {
			return sorted[i].Start < sorted[j].Start
		}
		return sorted[i].End < sorted[j].End
	})

	seen := make(map[string]int, len(sorted))
	out := make([]Pointer, 0, len(sorted))
	for _, s := range sorted {
		if s.Symbol == "" {
			continue
		}
		baseID := strings.ReplaceAll(s.Symbol, ".", "-")
		id := baseID
		if c := seen[baseID]; c > 0 {
			id = baseID + "-" + strconv.Itoa(c+1)
			seen[baseID] = c + 1
		} else {
			seen[baseID] = 1
		}

		start, end := s.Start, s.End
		if start <= 0 {
			start = 1
		}
		if end < start {
			end = start
		}

		out = append(out, Pointer{
			ID:    id,
			Path:  s.Path,
			Sym:   s.Symbol,
			Start: start,
			End:   end,
		})
	}
	return out
}

func dedupPointers(in []Pointer) []Pointer {
	if len(in) <= 1 {
		return in
	}
	type key struct {
		id         string
		path       string
		start, end int
	}
	seen := make(map[key]struct{}, len(in))
	out := make([]Pointer, 0, len(in))
	for _, p := range in {
		k := key{p.ID, p.Path, p.Start, p.End}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, p)
	}
	return out
}
