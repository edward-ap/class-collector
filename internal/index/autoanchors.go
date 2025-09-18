package index

import (
	"bytes"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// AutoAnchorConfig controls virtual anchor generation.
type AutoAnchorConfig struct {
	Enabled        bool   // master switch
	MinLines       int    // skip regions shorter than this (lines = End-Start+1)
	MaxPerFile     int    // hard cap per file (0 = unlimited)
	IncludeImports bool   // add a single IMPORTS anchor if an import block is present
	IncludeTests   bool   // add anchors for test functions (Go: Test*/Benchmark*/Example*; TS: it/describe)
	Prefix         string // prefix for auto anchor names, e.g. "auto:"; empty = no prefix
}

// Default config (balanced and language-agnostic).
func DefaultAutoAnchorConfig() AutoAnchorConfig {
	return AutoAnchorConfig{
		Enabled:        true,
		MinLines:       8,
		MaxPerFile:     64,
		IncludeImports: true,
		IncludeTests:   true,
		Prefix:         "auto:",
	}
}

// Package-level knob set from main() once; keeps BuildArtifacts signature stable.
var autoCfg = DefaultAutoAnchorConfig()

// SetAutoAnchorsConfig is called from the CLI to adjust defaults.
func SetAutoAnchorsConfig(c AutoAnchorConfig) { autoCfg = c }

// BuildAutoAnchors derives virtual anchors from symbols + simple file heuristics.
// It never edits source files: anchors are synthesized and appended to the list.
func BuildAutoAnchors(relPath string, data []byte, lang string, syms []Symbol, existing []Anchor, totalLines int) []Anchor {
	if !autoCfg.Enabled || totalLines < 1 {
		return nil
	}
	minLines := autoCfg.MinLines
	if minLines < 1 {
		minLines = 1
	}

	// 1) Symbol-backed anchors: one per symbol (method/func/ctor).
	var out []Anchor
	for _, s := range syms {
		start, end := s.Start, s.End
		if start < 1 {
			start = 1
		}
		if end < start {
			end = start
		}
		if (end - start + 1) < minLines {
			continue
		}
		name := autoCfg.Prefix + symbolAnchorName(s, lang)
		out = append(out, Anchor{Name: name, Start: start, End: end})
	}

	// 2) Imports block: cheap, useful split near file top.
	if autoCfg.IncludeImports {
		if a, ok := importAnchor(relPath, data, lang); ok {
			if (a.End - a.Start + 1) >= minLines {
				a.Name = autoCfg.Prefix + a.Name
				out = append(out, a)
			}
		}
	}

	// 3) Test-specific anchors (language-aware, optional).
	if autoCfg.IncludeTests {
		if ta := testAnchors(relPath, data, lang); len(ta) > 0 {
			for i := range ta {
				if (ta[i].End - ta[i].Start + 1) >= minLines {
					ta[i].Name = autoCfg.Prefix + ta[i].Name
					out = append(out, ta[i])
				}
			}
		}
	}

	// 4) Coarse region anchors by language (gated by MinLines)
	switch lang {
	case "go":
		// const block
		if a, ok := coarseRegion(data, `(?ms)^\s*const\s*\([^)]*\)`, "CONSTS"); ok && (a.End-a.Start+1) >= minLines {
			out = append(out, prefixed(a))
		}
		// const single-line declarations (may be scattered)
		if a, ok := coarseRange(data, `(?m)^\s*const\s+\w`, "CONSTS"); ok && (a.End-a.Start+1) >= minLines {
			out = append(out, prefixed(a))
		}
		if a, ok := coarseRange(data, `(?m)^\s*type\s+[A-Za-z_]\w*\b`, "TYPES"); ok && (a.End-a.Start+1) >= minLines {
			out = append(out, prefixed(a))
		}
		if a, ok := coarseRange(data, `(?m)^\s*func\s+(?:\([^)]*\)\s*)?[A-Za-z_]\w*\s*\(`, "FUNCS"); ok && (a.End-a.Start+1) >= minLines {
			out = append(out, prefixed(a))
		}
	case "ts":
		if a, ok := coarseRange(data, `(?m)^\s*export\s+(?:const|let|var)\s+`, "CONSTS"); ok && (a.End-a.Start+1) >= minLines {
			out = append(out, prefixed(a))
		}
		if a, ok := coarseRange(data, `(?m)^\s*export\s+(?:interface|type|class)\b`, "TYPES"); ok && (a.End-a.Start+1) >= minLines {
			out = append(out, prefixed(a))
		}
		if a, ok := coarseRange(data, `(?m)^\s*export\s+(?:async\s+)?function\b|^\s*export\s+const\s+[A-Za-z_$][\w$]*\s*=\s*(?:async\s*)?(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*=>`, "FUNCS"); ok && (a.End-a.Start+1) >= minLines {
			out = append(out, prefixed(a))
		}
	case "java":
		if a, ok := coarseRange(data, `(?m)^\s*(?:public|protected|private|static|final|synchronized|native|abstract|default|strictfp|)\s*[\w<>\[\]]+\s+[A-Za-z_]\w*\s*\(`, "METHODS"); ok && (a.End-a.Start+1) >= minLines {
			out = append(out, prefixed(a))
		}
		if a, ok := coarseRange(data, `(?m)^\s*(?:public|protected|private)\s+[A-Z][A-Za-z0-9_]*\s*\(`, "CTORS"); ok && (a.End-a.Start+1) >= minLines {
			out = append(out, prefixed(a))
		}
		if a, ok := coarseRange(data, `(?m)^\s*(?:public|protected|private|static|final)\s+[\w<>\[\],\s]+;\s*$`, "FIELDS"); ok && (a.End-a.Start+1) >= minLines {
			out = append(out, prefixed(a))
		}
	case "cs":
		if a, ok := coarseRange(data, `(?m)^\s*(?:public|internal|protected|private|static|virtual|override|sealed|async|extern|unsafe|new)\s+.*\(`, "METHODS"); ok && (a.End-a.Start+1) >= minLines {
			out = append(out, prefixed(a))
		}
		if a, ok := coarseRange(data, `(?m)^\s*(?:public|internal|protected|private)\s+[A-Z][A-Za-z0-9_]*\s*\(`, "CTORS"); ok && (a.End-a.Start+1) >= minLines {
			out = append(out, prefixed(a))
		}
		if a, ok := coarseRange(data, `(?m)^\s*(?:public|internal|protected|private|static|readonly|const|volatile)\s+[^;]+;\s*$`, "FIELDS"); ok && (a.End-a.Start+1) >= minLines {
			out = append(out, prefixed(a))
		}
	}

	// Normalize: clamp, sort, dedup exact matches and drop exact overlaps with explicit anchors.
	out = normalizeAutoAnchors(out, existing, totalLines)

	// Cap per-file if requested.
	if autoCfg.MaxPerFile > 0 && len(out) > autoCfg.MaxPerFile {
		out = out[:autoCfg.MaxPerFile]
	}
	return out
}

// ----------------- helpers -----------------

func symbolAnchorName(s Symbol, lang string) string {
	// Prefer a compact readable name:
	//  Java:  org.acme.Server.start -> "SYM:Server.start"
	//  Go:    mypkg.Conn.Open       -> "SYM:Conn.Open" or "SYM:mypkg.main"
	//  TS:    (typ?) . name         -> "SYM:<Type>.name" or "SYM:name"
	base := s.Symbol
	// Keep the last two segments if possible ("Type.member"); else last one.
	parts := strings.Split(base, ".")
	if len(parts) >= 2 {
		return "SYM:" + parts[len(parts)-2] + "." + parts[len(parts)-1]
	}
	return "SYM:" + base
}

func importAnchor(relPath string, data []byte, lang string) (Anchor, bool) {
	switch lang {
	case "java":
		// Consecutive lines starting with "import " near top.
		lines := bytes.Split(data, []byte("\n"))
		first, last := 0, 0
		seen := false
		for i := 0; i < len(lines) && i < 400; i++ { // scan only the top chunk
			ln := strings.TrimSpace(string(lines[i]))
			if strings.HasPrefix(ln, "import ") {
				if !seen {
					first = i + 1
					seen = true
				}
				last = i + 1
			} else if seen && ln != "" && !strings.HasPrefix(ln, "//") {
				break
			}
		}
		if seen && last >= first {
			return Anchor{Name: "IMPORTS", Start: first, End: last}, true
		}
	case "go":
		// import "x" OR import (...) block
		reTopImport := regexp.MustCompile(`(?ms)^\s*import\s+(?:\([^\)]*\)|"[^"]+")`)
		if loc := reTopImport.FindIndex(data); loc != nil {
			start := 1 + bytes.Count(data[:loc[0]], []byte("\n"))
			end := 1 + bytes.Count(data[:loc[1]], []byte("\n"))
			return Anchor{Name: "IMPORTS", Start: start, End: end}, true
		}
	case "ts":
		// one or more consecutive "import ..." lines at very top
		reImp := regexp.MustCompile(`(?m)^\s*import\s+[^;]+;?\s*$`)
		m := reImp.FindAllIndex(data, -1)
		if len(m) > 0 && m[0][0] < 600 { // only if at file start-ish
			first := 1 + bytes.Count(data[:m[0][0]], []byte("\n"))
			last := 1 + bytes.Count(data[:m[len(m)-1][1]], []byte("\n"))
			return Anchor{Name: "IMPORTS", Start: first, End: last}, true
		}
	}
	return Anchor{}, false
}

func testAnchors(relPath string, data []byte, lang string) []Anchor {
	switch lang {
	case "go":
		// Go tests are in *_test.go with funcs: TestXxx/BenchmarkXxx/ExampleXxx
		if !strings.HasSuffix(relPath, "_test.go") {
			return nil
		}
		re := regexp.MustCompile(`(?m)^\s*func\s+(Test|Benchmark|Example)[A-Za-z0-9_]*\s*\(`)
		locs := re.FindAllIndex(data, -1)
		var out []Anchor
		for _, loc := range locs {
			start := 1 + bytes.Count(data[:loc[0]], []byte("\n"))
			// End will be refined later; here we give a minimal 1-line region
			out = append(out, Anchor{Name: "TEST", Start: start, End: start})
		}
		return out
	case "ts":
		// jest/mocha style: describe(...), it(...), test(...)
		re := regexp.MustCompile(`(?m)^\s*(describe|it|test)\s*\(`)
		locs := re.FindAllIndex(data, -1)
		var out []Anchor
		for _, loc := range locs {
			start := 1 + bytes.Count(data[:loc[0]], []byte("\n"))
			out = append(out, Anchor{Name: "TEST", Start: start, End: start})
		}
		return out
	default:
		return nil
	}
}

func prefixed(a Anchor) Anchor {
	a.Name = autoCfg.Prefix + a.Name
	return a
}

// coarseRange returns a single anchor covering first..last occurrence of pattern lines.
func coarseRange(data []byte, pattern string, name string) (Anchor, bool) {
	re := regexp.MustCompile(pattern)
	locs := re.FindAllIndex(data, -1)
	if len(locs) == 0 {
		return Anchor{}, false
	}
	first := 1 + bytes.Count(data[:locs[0][0]], []byte("\n"))
	last := 1 + bytes.Count(data[:locs[len(locs)-1][1]], []byte("\n"))
	return Anchor{Name: name, Start: first, End: last}, true
}

// coarseRegion returns the exact span matched by a single region pattern (first match only).
func coarseRegion(data []byte, pattern string, name string) (Anchor, bool) {
	re := regexp.MustCompile(pattern)
	if loc := re.FindIndex(data); loc != nil {
		first := 1 + bytes.Count(data[:loc[0]], []byte("\n"))
		last := 1 + bytes.Count(data[:loc[1]], []byte("\n"))
		return Anchor{Name: name, Start: first, End: last}, true
	}
	return Anchor{}, false
}

func normalizeAutoAnchors(in []Anchor, explicit []Anchor, total int) []Anchor {
	if len(in) == 0 {
		return nil
	}
	// Clamp & sort (Start, End, Name)
	for i := range in {
		if in[i].Start < 1 {
			in[i].Start = 1
		}
		if in[i].End < in[i].Start {
			in[i].End = in[i].Start
		}
		if in[i].End > total {
			in[i].End = total
		}
	}
	sort.Slice(in, func(i, j int) bool {
		if in[i].Start != in[j].Start {
			return in[i].Start < in[j].Start
		}
		if in[i].End != in[j].End {
			return in[i].End < in[j].End
		}
		return in[i].Name < in[j].Name
	})
	// Dedup exact duplicates.
	uniq := make([]Anchor, 0, len(in))
	for i, a := range in {
		if i > 0 {
			p := in[i-1]
			if a.Name == p.Name && a.Start == p.Start && a.End == p.End {
				continue
			}
		}
		uniq = append(uniq, a)
	}
	in = uniq

	// Drop anchors that exactly duplicate explicit ones.
	if len(explicit) > 0 {
		type key struct {
			n    string
			s, e int
		}
		exp := make(map[key]struct{}, len(explicit))
		for _, a := range explicit {
			exp[key{a.Name, a.Start, a.End}] = struct{}{}
		}
		out := in[:0]
		for _, a := range in {
			if _, dup := exp[key{a.Name, a.Start, a.End}]; !dup {
				out = append(out, a)
			}
		}
		in = out
	}
	return in
}

// Language inference helper mirrored from symbols_common.go (to avoid cyclic deps if needed).
func fileLangByExt(relPath string) string {
	e := strings.ToLower(filepath.Ext(relPath))
	switch e {
	case ".java":
		return "java"
	case ".go":
		return "go"
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return "ts"
	default:
		return ""
	}
}
