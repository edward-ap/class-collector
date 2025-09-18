// Package index — Go symbol extractor.
//
// This file extracts package name and top-level function/method symbols from Go
// source using lightweight regular expressions. It is intentionally shallow
// (not a full parser) but good enough for navigation and bundle indexing.
//
// Features:
//   - Detects functions and methods (methods have a receiver).
//   - Emits qualified symbol names using joinSym(pkg, recvType, name).
//   - Start line is 1-based; End is finalized by the caller (next symbol or EOF).
//   - Robust receiver parsing: strips pointers (*), package qualifiers (pkg.Type),
//     and generic brackets (T constraints) to get a clean base type.
//
// Limitations:
//   - Does not parse nested function literals; only top-level funcs.
//   - Complex receivers (e.g., multi-level pointers or generics) are simplified.
package index

import (
	"bytes"
	"regexp"
	"strings"
)

var (
	// package mypkg
	reGoPkg = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z0-9_]+)\s*$`)

	// func <Name>(...) or func (<recv>) <Name>(...)
	// Groups:
	//   1: receiver block (optional), including parentheses: "(r *T) "
	//   2: function/method name
	reGoFunc = regexp.MustCompile(`(?m)^\s*func\s+(\([^)]+\)\s*)?([A-Za-z0-9_]+)\s*\(`)
)

// extractGo returns:
//
//	pkg   — detected package name
//	kind  — "file" (Go has no single primary "type" per file)
//	typ   — empty (reserved for languages with file-scoped primary types)
//	exports — function names with "()" suffix for quick overview
//	syms  — collected symbols with 1-based Start (End finalized by caller)
func extractGo(relPath string, data []byte) (pkg, kind, typ string, exports []string, syms []Symbol) {
	lineOf := func(off int) int { return 1 + bytes.Count(data[:off], []byte("\n")) }

	if m := reGoPkg.FindSubmatch(data); m != nil {
		pkg = string(m[1])
	}
	kind = "file" // Go files do not have a single primary class/type.

	idxs := reGoFunc.FindAllSubmatchIndex(data, -1)
	for _, idx := range idxs {
		// idx layout: [ full0 full1  grp1_0 grp1_1  grp2_0 grp2_1 ]
		start := lineOf(idx[0])
		name := string(data[idx[4]:idx[5]])

		recvType := ""
		if idx[2] != -1 && idx[3] != -1 {
			recvBlock := string(data[idx[2]:idx[3]]) // e.g. "(r *pkg.T) "
			recvType = receiverBaseType(recvBlock)
		}

		kindSym := "func"
		if recvType != "" {
			kindSym = "method"
		}

		syms = append(syms, Symbol{
			Symbol: joinSym(pkg, recvType, name),
			Kind:   kindSym,
			Path:   relPath,
			Start:  start,
			End:    start, // finalized later by caller
		})
		exports = append(exports, name+"()")
	}
	return
}

// receiverBaseType extracts a clean base type from a receiver block.
// Input examples:
//
//	"(s *Server)"        -> "Server"
//	"(c db.Conn)"        -> "Conn"
//	"(p *pkg.Type[T])"   -> "Type"
//	"(x some.Pkg.Type)"  -> "Type"
func receiverBaseType(recvBlock string) string {
	s := strings.TrimSpace(recvBlock)
	// Strip surrounding parentheses if present.
	if strings.HasPrefix(s, "(") && strings.Contains(s, ")") {
		if i := strings.IndexByte(s, ')'); i >= 0 {
			s = s[1:i]
		}
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	// Receiver form is typically: "<ident> <type>"
	// Take the last token as the type.
	tokens := strings.Fields(s)
	if len(tokens) == 0 {
		return ""
	}
	typ := tokens[len(tokens)-1]

	// Remove pointer/reference sigils.
	typ = strings.TrimLeft(typ, "*&")

	// Drop generic brackets if any (Type[T] -> Type).
	if i := strings.IndexByte(typ, '['); i >= 0 {
		typ = typ[:i]
	}

	// Keep the final identifier after the last '.' (pkg.Type -> Type).
	if i := strings.LastIndexByte(typ, '.'); i >= 0 {
		typ = typ[i+1:]
	}
	return strings.TrimSpace(typ)
}
