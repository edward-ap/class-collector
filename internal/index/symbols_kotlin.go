package index

import (
	"bytes"
	"regexp"
)

// Kotlin symbol extractor (.kt)
// - Extract package: `package foo.bar`
// - Primary top-level type: first of `class|interface|object Name`
// - Functions: `fun name(` including extension functions `fun Receiver.name(`
// Exports list: function names with ()
// Kind: "class" | "interface" | "object" | "file"
func extractKotlin(relPath string, data []byte) (pkg, kind, typ string, exports []string, syms []Symbol) {
	lineOf := func(off int) int { return 1 + bytes.Count(data[:off], []byte("\n")) }

	rePkg := regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z_][\w\.]*)`)
	reType := regexp.MustCompile(`(?m)^\s*(?:public\s+|internal\s+|private\s+)?(class|interface|object)\s+([A-Za-z_][\w_]*)`)
	// fun name(   | fun Receiver.name(
	reFun := regexp.MustCompile(`(?m)^\s*(?:suspend\s+)?fun\s+(?:[A-Za-z_][\w_]*\.)?([A-Za-z_][\w_]*)\s*\(`)

	if m := rePkg.FindSubmatch(data); m != nil {
		pkg = string(m[1])
	}
	if m := reType.FindSubmatchIndex(data); m != nil {
		k := string(data[m[2]:m[3]])
		switch k {
		case "class":
			kind = "class"
		case "interface":
			kind = "interface"
		case "object":
			kind = "object"
		}
		typ = string(data[m[4]:m[5]])
	}
	if kind == "" {
		kind = "file"
	}

	if ms := reFun.FindAllSubmatchIndex(data, -1); len(ms) > 0 {
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
