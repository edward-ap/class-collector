package index

import (
	"bytes"
	"regexp"
)

var (
	reTsClass            = regexp.MustCompile(`(?m)^\s*export\s+(?:default\s+)?class\s+([A-Za-z_$][\w$]*)?`)
	reTsInterface        = regexp.MustCompile(`(?m)^\s*export\s+interface\s+([A-Za-z_$][\w$]*)`)
	reTsFunc             = regexp.MustCompile(`(?m)^\s*export\s+(?:async\s+)?function\*?\s+([A-Za-z_$][\w$]*)\s*\(`)
	reTsDefaultNamedFunc = regexp.MustCompile(`(?m)^\s*export\s+default\s+function\s+([A-Za-z_$][\w$]*)\s*\(`)
	reTsDefaultAnonFunc  = regexp.MustCompile(`(?m)^\s*export\s+default\s+function\s*\(`)
	reTsReExportList     = regexp.MustCompile(`(?m)^\s*export\s*\{([^}]*)\}\s*from\s*['\"][^'\"]+['\"]`)
	reTsLetVar           = regexp.MustCompile(`(?m)^\s*export\s+(?:let|var)\s+([A-Za-z_$][\w$]*)\s*=`)
	reTsConstArrow       = regexp.MustCompile(`(?m)^\s*export\s+const\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s*)?(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*=>`)
	reTsConstObject      = regexp.MustCompile(`(?m)^\s*export\s+const\s+([A-Za-z_$][\w$]*)\s*=\s*\{`)
	reTsObjMethod        = regexp.MustCompile(`(?m)^[\t ]*([A-Za-z_$][\w$]*)\s*\(`)
)

type tsSymbol struct {
	name string
	line int
}

type tsScanResult struct {
	kind    string
	typ     string
	exports []string
	symbols []tsSymbol
}

func extractTS(relPath string, data []byte) (pkg string, kind string, typ string, exports []string, syms []Symbol) {
	res := scanTS(relPath, data)
	syms = toSymbolsTS(relPath, res)
	return "", res.kind, res.typ, res.exports, syms
}

func scanTS(relPath string, data []byte) tsScanResult {
	res := tsScanResult{kind: "file"}
	lineOf := func(off int) int { return 1 + bytes.Count(data[:off], []byte("\n")) }

	if m := reTsClass.FindSubmatchIndex(data); m != nil {
		res.kind = "class"
		if m[2] >= 0 && m[3] >= 0 {
			res.typ = string(data[m[2]:m[3]])
		} else {
			res.typ = "default"
		}
	} else if m := reTsInterface.FindSubmatch(data); m != nil {
		res.kind = "interface"
		res.typ = string(m[1])
	}

	for _, idx := range reTsFunc.FindAllSubmatchIndex(data, -1) {
		name := string(data[idx[len(idx)-2]:idx[len(idx)-1]])
		res.symbols = append(res.symbols, tsSymbol{
			name: joinSym("", res.typ, name),
			line: lineOf(idx[0]),
		})
		res.exports = append(res.exports, name+"()")
	}

	for _, idx := range reTsDefaultNamedFunc.FindAllSubmatchIndex(data, -1) {
		name := string(data[idx[len(idx)-2]:idx[len(idx)-1]])
		res.symbols = append(res.symbols, tsSymbol{
			name: joinSym("", "default", name),
			line: lineOf(idx[0]),
		})
		res.exports = append(res.exports, name+"()")
	}

	for _, idx := range reTsDefaultAnonFunc.FindAllIndex(data, -1) {
		res.symbols = append(res.symbols, tsSymbol{
			name: "default",
			line: lineOf(idx[0]),
		})
		res.exports = append(res.exports, "default()")
	}

	for _, m := range reTsReExportList.FindAllSubmatch(data, -1) {
		items := bytes.TrimSpace(m[1])
		for _, part := range bytes.Split(items, []byte(",")) {
			name := string(bytes.TrimSpace(bytes.Split(part, []byte(" as "))[0]))
			if name != "" {
				res.exports = append(res.exports, name+"()")
			}
		}
	}

	for _, idx := range reTsLetVar.FindAllSubmatchIndex(data, -1) {
		name := string(data[idx[len(idx)-2]:idx[len(idx)-1]])
		res.exports = append(res.exports, name)
	}

	for _, idx := range reTsConstArrow.FindAllSubmatchIndex(data, -1) {
		name := string(data[idx[len(idx)-2]:idx[len(idx)-1]])
		res.symbols = append(res.symbols, tsSymbol{
			name: joinSym("", res.typ, name),
			line: lineOf(idx[0]),
		})
		res.exports = append(res.exports, name+"()")
	}

	for _, idx := range reTsConstObject.FindAllSubmatchIndex(data, -1) {
		objName := string(data[idx[len(idx)-2]:idx[len(idx)-1]])
		start := idx[1]
		end := bytes.IndexByte(data[start:], '}')
		if end < 0 {
			continue
		}
		block := data[start : start+end]
		for _, mi := range reTsObjMethod.FindAllSubmatchIndex(block, -1) {
			method := string(block[mi[len(mi)-2]:mi[len(mi)-1]])
			res.symbols = append(res.symbols, tsSymbol{
				name: joinSym("", objName, method),
				line: lineOf(start + mi[0]),
			})
		}
	}

	return res
}

func toSymbolsTS(relPath string, res tsScanResult) []Symbol {
	if len(res.symbols) == 0 {
		return nil
	}
	out := make([]Symbol, 0, len(res.symbols))
	for _, sym := range res.symbols {
		if sym.name == "" {
			continue
		}
		out = append(out, Symbol{
			Symbol: sym.name,
			Kind:   "method",
			Path:   relPath,
			Start:  sym.line,
			End:    sym.line,
		})
	}
	return out
}
