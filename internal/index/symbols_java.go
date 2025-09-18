// Package index — Java symbol extractor.
//
// This file extracts package, primary top-level type (class/interface/enum),
// and method/constructor symbols from Java sources using lightweight regular
// expressions. It is intentionally shallow (not a full parser) but good
// enough for bundle indexing and navigation.
//
// Features:
//   - Detects top-level type kind/name (first public class/interface/enum).
//   - Extracts methods and constructors.
//   - Emits qualified symbol names using joinSym(pkg, type, member).
//   - Start line is 1-based; End is finalized by the caller (next symbol or EOF).
//
// Limitations:
//   - Only the first declared top-level type is used as the "primary" type.
//   - Nested/inner types are not explicitly modeled.
//   - The method regex is heuristic and may miss exotic signatures.
package index

import (
	"bytes"
	"fmt"
	"regexp"
)

// Examples matched by these regexes:
//
//   package com.acme.foo;
//
//   public class Server<T> implements Runnable {
//       public void start() { ... }      // method
//       protected Server() { ... }       // constructor
//   }
//
//   interface Loader {
//       String load(String key);         // method in interface
//   }
//
//   enum Mode {
//       A, B;
//       public boolean isA() { return this == A; }  // method in enum
//   }

var (
	// package com.acme.foo;
	reJavaPkg = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z0-9_.]+)\s*;`)

	// public class|interface|enum Name ...
	// Groups:
	//   2: kind ("class"|"interface"|"enum")
	//   3: type name
	reJavaType = regexp.MustCompile(`(?m)^\s*(?:public\s+)?(class|interface|enum)\s+([A-Za-z0-9_]+)`)

	// Method signature (heuristic):
	// - Optional modifiers (public/protected/private/static/final/etc)
	// - Return type token(s) including generics/arrays (e.g., List<Foo>[][])
	// - Method name
	// - Opening parenthesis follows immediately or with spaces
	//
	// NOTE: Constructors have no return type and are handled separately.
	reJavaMeth = regexp.MustCompile(
		`(?m)^\s*(?:public|protected|private|static|final|synchronized|native|abstract|default|\s)+` +
			`\s*[A-Za-z0-9_<>\[\].?]+` + // return type (very permissive)
			`\s+([A-Za-z0-9_]+)\s*\(`, // method name
	)
)

// extractJava returns:
//
//	pkg     — package name
//	kind    — "class" | "interface" | "enum" | "file"
//	typ     — primary top-level type name (empty when kind=="file")
//	exports — method/ctor names with "()" suffix for quick overview
//	syms    — collected symbols with 1-based Start (End finalized by caller)
func extractJava(relPath string, data []byte) (pkg, kind, typ string, exports []string, syms []Symbol) {
	lineOf := func(off int) int { return 1 + bytes.Count(data[:off], []byte("\n")) }

	// Package
	if m := reJavaPkg.FindSubmatch(data); m != nil {
		pkg = string(m[1])
	}

	// Primary top-level type (first match)
	if m := reJavaType.FindSubmatch(data); m != nil {
		kind = string(m[1])
		typ = string(m[2])
	} else {
		kind = "file"
	}

	// Methods
	// idx layout for FindAllSubmatchIndex:
	// [ full0 full1  ...  (only one capture group for name) grp1_0 grp1_1 ]
	if ms := reJavaMeth.FindAllSubmatchIndex(data, -1); len(ms) > 0 {
		for _, idx := range ms {
			name := string(data[idx[len(idx)-2]:idx[len(idx)-1]])
			start := lineOf(idx[0])
			syms = append(syms, Symbol{
				Symbol: joinSym(pkg, typ, name),
				Kind:   "method",
				Path:   relPath,
				Start:  start,
				End:    start, // finalized by caller
			})
			exports = append(exports, name+"()")
		}
	}

	// Constructors: same name as the primary type, no return type.
	// We build a dynamic regex only when 'typ' is known.
	if typ != "" {
		reCtor := regexp.MustCompile(fmt.Sprintf(`(?m)^\s*(?:public|protected|private|\s)+\s*%s\s*\(`, regexp.QuoteMeta(typ)))
		if cs := reCtor.FindAllSubmatchIndex(data, -1); len(cs) > 0 {
			for _, ci := range cs {
				start := lineOf(ci[0])
				// use type name as member (e.g., "Server.Server")
				syms = append(syms, Symbol{
					Symbol: joinSym(pkg, typ, typ),
					Kind:   "ctor",
					Path:   relPath,
					Start:  start,
					End:    start,
				})
				exports = append(exports, typ+"()")
			}
		}
	}

	return
}
