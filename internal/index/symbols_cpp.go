package index

import (
	"bytes"
	"regexp"
	"strings"
)

// extractCPP performs shallow regex-based extraction for C++-like files.
// It attempts to infer:
//   - package/namespace (dot-joined)
//   - primary type and kind (class/struct/enum)
//   - method/function symbols
//
// Exports contain method/function names with trailing "()".
func extractCPP(relPath string, data []byte) (pkg, kind, typ string, exports []string, syms []Symbol) {
	s := string(data)

	// Namespace: first namespace occurrence; use '::' as separator, but store dot-joined
	nsRe := regexp.MustCompile(`(?m)^\s*namespace\s+([A-Za-z_][\w:]*)\s*{`)
	if m := nsRe.FindStringSubmatchIndex(s); m != nil {
		name := s[m[2]:m[3]]
		name = strings.ReplaceAll(name, "::", ".")
		pkg = strings.Trim(name, ":.")
	}

	// Primary type and kind
	// Capture keyword to set kind precisely
	primaryRe := regexp.MustCompile(`(?m)^\s*(class|struct|enum)\s+([A-Za-z_]\w*)\b`)
	if m := primaryRe.FindStringSubmatchIndex(s); m != nil {
		kw := s[m[2]:m[3]]
		typ = s[m[4]:m[5]]
		kind = strings.ToLower(kw)
		start := 1 + bytes.Count([]byte(s[:m[0]]), []byte("\n"))
		fq := joinSym(pkg, typ, "")
		if fq != "" {
			syms = append(syms, Symbol{Symbol: fq, Kind: kind, Path: relPath, Start: start, End: start})
		}
	}
	if kind == "" {
		kind = "file"
	}

	// Qualified method definitions: Type::method(
	qualMethRe := regexp.MustCompile(`(?m)^\s*(?:[A-Za-z_][\w:<>\*\&\s]+)?\b([A-Za-z_]\w*)::([A-Za-z_]\w*)\s*\(`)
	for _, m := range qualMethRe.FindAllStringSubmatchIndex(s, -1) {
		recv := s[m[2]:m[3]]
		name := s[m[4]:m[5]]
		line := 1 + bytes.Count([]byte(s[:m[0]]), []byte("\n"))
		fq := joinSym(pkg, recv, name)
		if fq == "" {
			continue
		}
		syms = append(syms, Symbol{Symbol: fq, Kind: "method", Path: relPath, Start: line, End: line})
		exports = append(exports, name+"()")
	}

	// Inside-class declarations (approximate) — only if we have a known type
	if typ != "" {
		declMethRe := regexp.MustCompile(`(?m)^\s*(?:virtual\s+)?[A-Za-z_][\w:<>\*\&\s]+\s+([A-Za-z_]\w*)\s*\(`)
		for _, m := range declMethRe.FindAllStringSubmatchIndex(s, -1) {
			name := s[m[2]:m[3]]
			line := 1 + bytes.Count([]byte(s[:m[0]]), []byte("\n"))
			fq := joinSym(pkg, typ, name)
			syms = append(syms, Symbol{Symbol: fq, Kind: "method", Path: relPath, Start: line, End: line})
			exports = append(exports, name+"()")
		}
	}

	// Free functions: avoid those with '::' qualifier
	freeFnRe := regexp.MustCompile(`(?m)^\s*(?:inline\s+)?[A-Za-z_][\w:<>\*\&\s]+\s+([A-Za-z_]\w*)\s*\(`)
	for _, m := range freeFnRe.FindAllStringSubmatchIndex(s, -1) {
		lineTextStart := strings.LastIndex(s[:m[0]], "\n") + 1
		lineText := s[lineTextStart:m[1]]
		if strings.Contains(lineText, "::") {
			continue // skip qualified methods already handled
		}
		name := s[m[2]:m[3]]
		line := 1 + bytes.Count([]byte(s[:m[0]]), []byte("\n"))
		fq := joinSym(pkg, "", name)
		syms = append(syms, Symbol{Symbol: fq, Kind: "func", Path: relPath, Start: line, End: line})
		exports = append(exports, name+"()")
	}

	// Deduplicate exports (stable)
	if len(exports) > 1 {
		seen := make(map[string]struct{}, len(exports))
		uniq := make([]string, 0, len(exports))
		for _, e := range exports {
			if _, ok := seen[e]; ok {
				continue
			}
			seen[e] = struct{}{}
			uniq = append(uniq, e)
		}
		exports = uniq
	}
	return
}
