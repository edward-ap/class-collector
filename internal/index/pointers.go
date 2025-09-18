// Package index - anchor-based jump pointers.
//
// This module converts extracted region anchors to stable, deterministic
// jump pointers that downstream tools (or UIs) can consume.
//
// Design goals:
//   - Deterministic: pointers are emitted in a stable order.
//   - Safe IDs: anchor names are slugified (ASCII-only, path-safe).
//   - Unique: duplicate anchor names within the same file get numeric suffixes.
//   - Minimal: we only populate {ID, Path, Start, End}; Sym is intentionally empty.
package index

import (
	"sort"
	"strconv"
	"strings"
)

// BuildAnchorPointers creates jump pointers for anchors within a file.
//
// ID format:
//
//	<relPath-with-slashes-replaced-by-dashes>#<slugified-anchor-name>[-N]
//
// where N is an added numeric suffix (2, 3, …) for duplicate anchor names
// that normalize to the same slug within the same file.
//
// Example:
//
//	relPath = "src/main/java/org/acme/Server.java"
//	anchor  = "SERVER_START"
//	ID      = "src-main-java-org-acme-Server.java#SERVER_START"
//
// Note: Sym is left empty for anchor-based pointers by design.
func BuildAnchorPointers(relPath string, anchors []Anchor) []Pointer {
	if len(anchors) == 0 {
		return nil
	}

	// Deterministic order: (Start, End, Name)
	sorted := make([]Anchor, len(anchors))
	copy(sorted, anchors)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Start != sorted[j].Start {
			return sorted[i].Start < sorted[j].Start
		}
		if sorted[i].End != sorted[j].End {
			return sorted[i].End < sorted[j].End
		}
		return sorted[i].Name < sorted[j].Name
	})

	base := strings.ReplaceAll(relPath, "/", "-")
	seen := make(map[string]int, len(sorted))

	out := make([]Pointer, 0, len(sorted))
	for _, a := range sorted {
		// Clamp invalid ranges defensively.
		start, end := a.Start, a.End
		if start <= 0 {
			start = 1
		}
		if end < start {
			end = start
		}

		slug := slugifyAnchor(a.Name)
		id := base + "#" + slug

		// Ensure uniqueness across anchors that normalize to the same slug.
		if c := seen[id]; c > 0 {
			id = id + "-" + strconv.Itoa(c+1)
			seen[base+"#"+slug] = c + 1
		} else {
			seen[base+"#"+slug] = 1
		}

		out = append(out, Pointer{
			ID:    id,
			Path:  relPath,
			Start: start,
			End:   end,
		})
	}
	return out
}

// slugifyAnchor normalizes an anchor name for use in pointer IDs.
// Rules (ASCII-oriented for stability across platforms/tools):
//   - Keep [A–Z a–z 0–9 . _ -] as-is.
//   - Convert spaces and other characters to '-'.
//   - Collapse multiple '-' into one and trim leading/trailing '-'.
//   - Preserve case (IDs stay readable and stable).
func slugifyAnchor(s string) string {
	if s == "" {
		return "anchor"
	}
	var b strings.Builder
	b.Grow(len(s))
	lastDash := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		// Allowed ASCII set
		if (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '.' || c == '_' || c == '-' {
			b.WriteByte(c)
			lastDash = false
			continue
		}
		// Map everything else to '-'
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	res := b.String()
	res = strings.Trim(res, "-")
	if res == "" {
		return "anchor"
	}
	return res
}
