package index

import (
	"bytes"
	"regexp"
)

// TS/JS symbol extractor (shared for .ts, .tsx, .js, .jsx, .mjs, .cjs).
//
// We keep this intentionally simple (regex-based) for navigation purposes:
//  • Primary top-level type: first `export (default )?class Name` or
//    `export interface Name` (used as `typ` in joinSym).
//  • Exported functions: `export function Name(...)` (async/generator allowed).
//  • Exported arrow funcs: `export const Name = (...) =>` or `= param =>`.
//
// Notes:
//  • We do NOT descend into class bodies to extract methods (out of scope here).
//  • When a primary type is present, we follow the historical behavior and
//    qualify exported free functions as if methods of that type by emitting
//    joinSym("", typ, name). If no type is present, we emit just "name".
//
// Start is 1-based; End is finalized by the caller (next symbol or EOF).

var (
	// export default class Name  |  export class Name
	// The name can be missing in `export default class` → typ="default".
	reTsClass = regexp.MustCompile(`(?m)^\s*export\s+(?:default\s+)?class\s+([A-Za-z_$][\w$]*)?`)

	// export interface Name
	reTsInterface = regexp.MustCompile(`(?m)^\s*export\s+interface\s+([A-Za-z_$][\w$]*)`)

	// export (async )?function[*]? Name(
	reTsFunc = regexp.MustCompile(`(?m)^\s*export\s+(?:async\s+)?function\*?\s+([A-Za-z_$][\w$]*)\s*\(`)
	// export default function Name(
	reTsDefaultNamedFunc = regexp.MustCompile(`(?m)^\s*export\s+default\s+function\s+([A-Za-z_$][\w$]*)\s*\(`)
	// export default function(
	reTsDefaultAnonFunc = regexp.MustCompile(`(?m)^\s*export\s+default\s+function\s*\(`)

	// export { Foo, Bar } from '...'
	reTsReExportList = regexp.MustCompile(`(?m)^\s*export\s*\{([^}]*)\}\s*from\s*['\"][^'\"]+['\"]`)

	// export let/var Name = ...
	reTsLetVar = regexp.MustCompile(`(?m)^\s*export\s+(?:let|var)\s+([A-Za-z_$][\w$]*)\s*=`)

	// export const Name = (args) =>   |   export const Name = arg =>   (async optional)
	reTsConstArrow = regexp.MustCompile(`(?m)^\s*export\s+const\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s*)?(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*=>`)
	// export const X = { foo() { ... } }
	reTsConstObject = regexp.MustCompile(`(?m)^\s*export\s+const\s+([A-Za-z_$][\w$]*)\s*=\s*\{`)
	reTsObjMethod = regexp.MustCompile(`(?m)^[\t ]*([A-Za-z_$][\w$]*)\s*\(`)
)

// extractTS returns:
//
//	pkg     — unused for TS/JS (empty string)
//	kind    — "class" | "interface" | "file"
//	typ     — primary top-level type name (class/interface), or "default" for anonymous default class
//	exports — exported function-like names with "()" suffix
//	syms    — collected symbols with 1-based Start (End finalized by caller)
func extractTS(relPath string, data []byte) (pkg, kind, typ string, exports []string, syms []Symbol) {
	lineOf := func(off int) int { return 1 + bytes.Count(data[:off], []byte("\n")) }

	// Primary type: prefer class, then interface.
	if m := reTsClass.FindSubmatchIndex(data); m != nil {
		kind = "class"
		if m[2] >= 0 && m[3] >= 0 { // named class
			typ = string(data[m[2]:m[3]])
		} else {
			typ = "default" // anonymous default export class
		}
	} else if m := reTsInterface.FindSubmatch(data); m != nil {
		kind = "interface"
		typ = string(m[1])
	} else {
		kind = "file"
	}

	// Exported functions
	if ms := reTsFunc.FindAllSubmatchIndex(data, -1); len(ms) > 0 {
		for _, idx := range ms {
			name := string(data[idx[len(idx)-2]:idx[len(idx)-1]])
			start := lineOf(idx[0])
			syms = append(syms, Symbol{
				Symbol: joinSym("", typ, name),
				Kind:   "method", // keep historical label for compatibility
				Path:   relPath,
				Start:  start,
				End:    start,
			})
			exports = append(exports, name+"()")
		}
	}
	// export default function Name(
	if ms := reTsDefaultNamedFunc.FindAllSubmatchIndex(data, -1); len(ms) > 0 {
		for _, idx := range ms {
			name := string(data[idx[len(idx)-2]:idx[len(idx)-1]])
			start := lineOf(idx[0])
			// Default export function behaves like a method on typ="default"
			syms = append(syms, Symbol{
				Symbol: joinSym("", "default", name),
				Kind:   "method",
				Path:   relPath,
				Start:  start,
				End:    start,
			})
			exports = append(exports, name+"()")
		}
	}
	// export default function(  — anonymous
	if loc := reTsDefaultAnonFunc.FindAllIndex(data, -1); len(loc) > 0 {
		for _, idx := range loc {
			start := lineOf(idx[0])
			// Anonymous default — typ="default", symbol just "default"
			syms = append(syms, Symbol{
				Symbol: "default",
				Kind:   "method",
				Path:   relPath,
				Start:  start,
				End:    start,
			})
			exports = append(exports, "default()")
		}
	}

	// export { Foo, Bar } from '...'
	if ms := reTsReExportList.FindAllSubmatch(data, -1); len(ms) > 0 {
		for _, m := range ms {
			items := string(bytes.TrimSpace(m[1]))
			for _, part := range bytes.Split([]byte(items), []byte(",")) {
				name := string(bytes.TrimSpace(bytes.Split(part, []byte(" as "))[0]))
				if name != "" {
					exports = append(exports, name+"()")
				}
			}
		}
	}

	// Exported let/var
	if ms := reTsLetVar.FindAllSubmatchIndex(data, -1); len(ms) > 0 {
		for _, idx := range ms {
			name := string(data[idx[len(idx)-2]:idx[len(idx)-1]])
			exports = append(exports, name)
		}
	}

	// Exported const arrow functions
	if ms := reTsConstArrow.FindAllSubmatchIndex(data, -1); len(ms) > 0 {
		for _, idx := range ms {
			name := string(data[idx[len(idx)-2]:idx[len(idx)-1]])
			start := lineOf(idx[0])
			syms = append(syms, Symbol{
				Symbol: joinSym("", typ, name),
				Kind:   "method",
				Path:   relPath,
				Start:  start,
				End:    start,
			})
			exports = append(exports, name+"()")
		}
	}

	// export const X = { foo() { ... } }
	if ms := reTsConstObject.FindAllSubmatchIndex(data, -1); len(ms) > 0 {
		for _, idx := range ms {
			obj := string(data[idx[len(idx)-2]:idx[len(idx)-1]])
			startObj := idx[1]
			endObj := bytes.IndexByte(data[startObj:], '}')
			if endObj >= 0 {
				block := data[startObj : startObj+endObj]
				if ims := reTsObjMethod.FindAllSubmatchIndex(block, -1); len(ims) > 0 {
					for _, mi := range ims {
						mname := string(block[mi[len(mi)-2]:mi[len(mi)-1]])
						start := lineOf(startObj + mi[0])
						syms = append(syms, Symbol{
							Symbol: joinSym("", obj, mname),
							Kind:   "method",
							Path:   relPath,
							Start:  start,
							End:    start,
						})
					}
				}
			}
		}
	}

	return
}
