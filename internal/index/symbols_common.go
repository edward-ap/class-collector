// Package index — common helpers shared by symbol extractors.
//
// This file provides:
//   - joinSym: builds a fully-qualified symbol name "pkg.Type.member"
//   - InferLangByExt: maps a file extension to a coarse language tag
package index

import "strings"

// joinSym concatenates package, type and member into a qualified symbol name.
// Empty segments are skipped; dots are inserted only between non-empty parts.
//
// Examples:
//
//	joinSym("org.acme", "Server", "start") => "org.acme.Server.start"
//	joinSym("org.acme", "", "main")        => "org.acme.main"
//	joinSym("", "Server", "start")         => "Server.start"
//	joinSym("", "", "main")                => "main"
func joinSym(pkg, typ, name string) string {
	pkg = strings.TrimSpace(pkg)
	typ = strings.TrimSpace(typ)
	name = strings.TrimSpace(name)

	var b strings.Builder
	// Append in order, inserting '.' only between non-empty parts.
	if pkg != "" {
		b.WriteString(pkg)
	}
	if typ != "" {
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(typ)
	}
	if name != "" {
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(name)
	}
	// If everything was empty, return empty string (callers typically guard this).
	return b.String()
}

// InferLangByExt returns a coarse language tag for a given file extension.
// The result is used to decide which symbol extractor to run.
//
// Normalization:
//   - Case-insensitive
//   - Accepts with or without leading '.' (".java" or "java")
//
// Mapping:
//   - ".java" → "java"
//   - ".go"   → "go"
//   - TS/JS family (".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs") → "ts"
//   - unknown/other → "" (caller may skip symbol extraction)
func InferLangByExt(ext string) string {
	e := strings.TrimSpace(strings.ToLower(ext))
	if e == "" {
		return ""
	}
	if e[0] != '.' {
		e = "." + e
	}

	switch e {
	case ".java":
		return "java"
	case ".go":
		return "go"
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		// We deliberately coalesce TS/JS into "ts" since the extractor is shared.
		return "ts"
	case ".kt":
		return "kt"
	case ".cs":
		return "cs"
	case ".py":
		return "py"
	case ".cpp", ".cc", ".cxx", ".hpp", ".hh", ".h":
		return "cpp"
	default:
		return ""
	}
}
