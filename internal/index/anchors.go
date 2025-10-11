// Package index provides source indexing utilities (anchors, symbols, slices).
//
// This file implements region anchors extraction. Anchors are declared with
// paired markers in the source and are mapped to 1-based line ranges:
//
//	// region:DOC_LINE_MARKER_EXAMPLE
//	... code ...
//	// endregion:DOC_LINE_MARKER_EXAMPLE
//
// Supported forms (case-insensitive):
//   - Line comments:  "// region NAME"  |  "// region: NAME"
//   - Preprocessor:  "#region NAME"     |  "#endregion NAME"   (C#/TS style)
//   - Block markers: "/* region: DOC_BLOCK_MARKER_EXAMPLE */" | "/* endregion: DOC_BLOCK_MARKER_EXAMPLE */"
//
// Features:
//   - Nested regions are supported, even with identical names (a stack per name).
//   - Overlapping detection is not enforced; we trust author intent.
//   - Duplicates from multiple syntaxes (e.g., both line and block) are de-duped.
//   - Deterministic output sorted by (Start, End).
package index

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
)

// Anchor is expected to be defined in this package:
//
// type Anchor struct {
//     Name  string `json:"name"`
//     Start int    `json:"start"` // 1-based inclusive
//     End   int    `json:"end"`   // 1-based inclusive
// }

// Regexes for line-style region markers:
//
//	// region NAME        // endregion NAME
//	// region: NAME       // endregion: NAME
//	#region NAME          #endregion NAME
var (
	reLineC = regexp.MustCompile(`(?i)^\s*//\s*(region|endregion)\s*:?\s*([A-Za-z0-9_.\-]+)\s*$`)
	reHash  = regexp.MustCompile(`(?i)^\s*#\s*(region|endregion)\s*:?\s*([A-Za-z0-9_.\-]+)\s*$`)
	// Block comment markers (C/Java/TS):
	reBlock = regexp.MustCompile(`(?is)/\*\s*(region|endregion)\s*:?\s*([A-Za-z0-9_.\-]+)\s*\*/`)
)

// ExtractAnchors orchestrates parsing, normalization, and deduplication.
func ExtractAnchors(path string, data []byte) []Anchor {
	raw, _ := parseAnchorsFromFile(path, data)
	if len(raw) == 0 {
		return nil
	}
	for i := range raw {
		raw[i] = normalizeAnchor(raw[i])
	}
	merged := mergeAnchors(nil, raw)
	if len(merged) <= 1 {
		return merged
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Start != merged[j].Start {
			return merged[i].Start < merged[j].Start
		}
		if merged[i].End != merged[j].End {
			return merged[i].End < merged[j].End
		}
		return merged[i].Name < merged[j].Name
	})
	return merged
}

func parseAnchorsFromFile(_ string, data []byte) ([]Anchor, error) {
	var anchors []Anchor

	startsByName := make(map[string][]int)
	lines := bytes.Split(data, []byte("\n"))
	for i, b := range lines {
		ln := i + 1
		if kind, name, ok := matchLineMarker(b); ok {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			switch strings.ToLower(kind) {
			case "region":
				startsByName[name] = append(startsByName[name], ln)
			case "endregion":
				stack := startsByName[name]
				if n := len(stack); n > 0 {
					start := stack[n-1]
					startsByName[name] = stack[:n-1]
					if start <= ln {
						anchors = append(anchors, Anchor{Name: name, Start: start, End: ln})
					}
				}
			}
		}
	}

	type open struct {
		name string
		off  int
	}
	var opens []open
	matches := reBlock.FindAllSubmatchIndex(data, -1)
	for _, m := range matches {
		kind := strings.ToLower(string(data[m[2]:m[3]]))
		name := strings.TrimSpace(string(data[m[4]:m[5]]))
		if name == "" {
			continue
		}
		switch kind {
		case "region":
			opens = append(opens, open{name: name, off: m[0]})
		case "endregion":
			for j := len(opens) - 1; j >= 0; j-- {
				if opens[j].name == name {
					startLine := 1 + bytes.Count(data[:opens[j].off], []byte("\n"))
					endLine := 1 + bytes.Count(data[:m[1]], []byte("\n"))
					if startLine <= endLine {
						anchors = append(anchors, Anchor{Name: name, Start: startLine, End: endLine})
					}
					opens = append(opens[:j], opens[j+1:]...)
					break
				}
			}
		}
	}
	return anchors, nil
}

// matchLineMarker tries both //-style and #-style line markers.
func matchLineMarker(b []byte) (kind, name string, ok bool) {
	if m := reLineC.FindSubmatch(b); m != nil {
		return string(m[1]), string(m[2]), true
	}
	if m := reHash.FindSubmatch(b); m != nil {
		return string(m[1]), string(m[2]), true
	}
	return "", "", false
}

func normalizeAnchor(a Anchor) Anchor {
	if a.Start < 1 {
		a.Start = 1
	}
	if a.End < a.Start {
		a.End = a.Start
	}
	a.Name = strings.TrimSpace(a.Name)
	return a
}

func mergeAnchors(dst []Anchor, src []Anchor) []Anchor {
	if len(src) == 0 {
		return dst
	}
	dst = append(dst, src...)
	if len(dst) > 1 {
		dst = dedupAnchors(dst)
	}
	return dst
}

// dedupAnchors removes exact duplicates (same Name/Start/End), preserving order.
func dedupAnchors(in []Anchor) []Anchor {
	type key struct {
		name       string
		start, end int
	}
	seen := make(map[key]struct{}, len(in))
	out := make([]Anchor, 0, len(in))
	for _, a := range in {
		k := key{a.Name, a.Start, a.End}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, a)
	}
	return out
}
