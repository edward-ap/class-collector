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

	// Work on a sorted view for deterministic output.
	sorted := make([]Symbol, 0, len(symbols))
	sorted = append(sorted, symbols...)
	sort.Slice(sorted, func(i, j int) bool {
		// Primary: symbol string (so ID base becomes deterministic)
		if sorted[i].Symbol != sorted[j].Symbol {
			return sorted[i].Symbol < sorted[j].Symbol
		}
		// Secondary: path
		if sorted[i].Path != sorted[j].Path {
			return sorted[i].Path < sorted[j].Path
		}
		// Tertiary: start line
		if sorted[i].Start != sorted[j].Start {
			return sorted[i].Start < sorted[j].Start
		}
		// Finally: end line
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

	// Final deterministic order: by ID, then Path, then Start/End.
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].End < out[j].End
	})

	return out
}
