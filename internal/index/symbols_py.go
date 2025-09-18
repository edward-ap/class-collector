package index

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
)

// Python minimal extractor (.py)
// - Package inferred from directory path (dots), __init__.py marks package root
// - Extract class and def names at top-level
func extractPy(relPath string, data []byte) (pkg, kind, typ string, exports []string, syms []Symbol) {
	lineOf := func(off int) int { return 1 + bytes.Count(data[:off], []byte("\n")) }

	// Package from directory
	clean := filepath.ToSlash(relPath)
	dir := clean
	if i := strings.LastIndex(clean, "/"); i >= 0 {
		dir = clean[:i]
	} else {
		dir = ""
	}
	if dir != "" {
		parts := strings.Split(dir, "/")
		// Drop leading src-like components heuristically? Keep all for determinism.
		pkg = strings.Join(parts, ".")
	}
	base := filepath.Base(clean)
	if base == "__init__.py" {
		// keep pkg as-is; module is package
	} else if strings.HasSuffix(base, ".py") {
		mod := strings.TrimSuffix(base, ".py")
		if pkg != "" {
			pkg = pkg + "." + mod
		} else {
			pkg = mod
		}
	}

	reClass := regexp.MustCompile(`(?m)^\s*class\s+([A-Za-z_][\w_]*)\s*\(`)
	reDef := regexp.MustCompile(`(?m)^\s*def\s+([A-Za-z_][\w_]*)\s*\(`)

	// Primary type: first class name
	if m := reClass.FindSubmatch(data); m != nil {
		kind = "class"
		typ = string(m[1])
	} else {
		kind = "file"
	}

	if ms := reDef.FindAllSubmatchIndex(data, -1); len(ms) > 0 {
		for _, idx := range ms {
			name := string(data[idx[len(idx)-2]:idx[len(idx)-1]])
			start := lineOf(idx[0])
			syms = append(syms, Symbol{
				Symbol: joinSym(pkg, typ, name),
				Kind:   "method",
				Path:   relPath,
				Start:  start,
				End:    start,
			})
			exports = append(exports, name+"()")
		}
	}
	return
}
