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

// ExtractAnchors scans the file content and returns a sorted list of anchors.
// The 'path' parameter is unused here but kept for potential diagnostics or
// future heuristics.
func ExtractAnchors(path string, data []byte) []Anchor {
	var anchors []Anchor

	// --- 1) Line-based markers, with per-name stacks to support nesting ------
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
				// push start line for this name
				startsByName[name] = append(startsByName[name], ln)
			case "endregion":
				// pop last start for this name (if any)
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

	// --- 2) Block-based markers "/* region: DOC_BLOCK_MARKER_EXAMPLE */ ... /* endregion: DOC_BLOCK_MARKER_EXAMPLE */"
	// This pass supports interleaving/nesting by keeping an ordered stack.
	type open struct {
		name string
		off  int // byte offset of the opening marker (for line calc)
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
			// find matching last opener with the same name
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

	// --- 3) Deduplicate (same Name/Start/End can appear from different passes)
	if len(anchors) > 1 {
		anchors = dedupAnchors(anchors)
	}

	// --- 4) Deterministic ordering: by Start asc, then End asc, then Name asc
	sort.Slice(anchors, func(i, j int) bool {
		if anchors[i].Start != anchors[j].Start {
			return anchors[i].Start < anchors[j].Start
		}
		if anchors[i].End != anchors[j].End {
			return anchors[i].End < anchors[j].End
		}
		return anchors[i].Name < anchors[j].Name
	})
	return anchors
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
