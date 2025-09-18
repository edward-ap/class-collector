package index

import (
	"bytes"
	"regexp"
)

// C# symbol extractor (.cs)
// - Extract namespace: `namespace Foo.Bar` (single-line form)
// - Primary type: first of class|struct|interface|enum Name
// - Methods: visibility optional, return type optional (constructor = type name)
// Note: #region anchors are handled by anchor extractor elsewhere; we just extract symbols.
func extractCS(relPath string, data []byte) (pkg, kind, typ string, exports []string, syms []Symbol) {
	lineOf := func(off int) int { return 1 + bytes.Count(data[:off], []byte("\n")) }

	reNs := regexp.MustCompile(`(?m)^\s*namespace\s+([A-Za-z_][\w\.]*)`)
	reType := regexp.MustCompile(`(?m)^\s*(?:[A-Za-z]+\s+)*(class|struct|interface|enum)\s+([A-Za-z_][\w_]*)`)
	// Method: visibility etc., return type (optional ctor), name(
	reMethod := regexp.MustCompile(`(?m)^\s*(?:public|internal|protected|private|static|virtual|override|sealed|async|extern|unsafe|new)\s+.*?([A-Za-z_][\w_]*)\s*\(`)

	if m := reNs.FindSubmatch(data); m != nil {
		pkg = string(m[1])
	}
	if m := reType.FindSubmatchIndex(data); m != nil {
		k := string(data[m[2]:m[3]])
		switch k {
		case "class":
			kind = "class"
		case "struct":
			kind = "struct"
		case "interface":
			kind = "interface"
		case "enum":
			kind = "enum"
		}
		typ = string(data[m[4]:m[5]])
	}
	if kind == "" {
		kind = "file"
	}

	if ms := reMethod.FindAllSubmatchIndex(data, -1); len(ms) > 0 {
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
