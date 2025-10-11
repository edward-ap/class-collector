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
	Enabled        bool
	MinLines       int
	MaxPerFile     int
	IncludeImports bool
	IncludeTests   bool
	Prefix         string
}

// DefaultAutoAnchorConfig returns the default heuristic configuration.
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

var autoCfg = DefaultAutoAnchorConfig()

// SetAutoAnchorsConfig overrides the global auto-anchor configuration.
func SetAutoAnchorsConfig(c AutoAnchorConfig) { autoCfg = c }

type anchorCandidate struct {
	anchor Anchor
	order  int
}

type anchorContext struct {
	relPath    string
	data       []byte
	lang       string
	symbols    []Symbol
	totalLines int
}

// BuildAutoAnchors derives virtual anchors from symbols + heuristics.
func BuildAutoAnchors(relPath string, data []byte, lang string, syms []Symbol, existing []Anchor, totalLines int) []Anchor {
	cfg, err := parseAutoAnchorConfig(data)
	if err != nil || !cfg.Enabled || totalLines < 1 {
		return nil
	}
	ctx := anchorContext{
		relPath:    relPath,
		data:       data,
		lang:       lang,
		symbols:    syms,
		totalLines: totalLines,
	}
	cands, err := collectAnchorCandidates(ctx, cfg)
	if err != nil || len(cands) == 0 {
		return nil
	}
	ranked, err := rankAndFilterAnchors(cands, cfg)
	if err != nil || len(ranked) == 0 {
		return nil
	}
	out, err := writeAnchors(existing, ranked, totalLines)
	if err != nil {
		return nil
	}
	return out
}

func parseAutoAnchorConfig(_ []byte) (AutoAnchorConfig, error) {
	cfg := autoCfg
	if cfg.MinLines < 1 {
		cfg.MinLines = 1
	}
	if cfg.MaxPerFile < 0 {
		cfg.MaxPerFile = 0
	}
	return cfg, nil
}

func collectAnchorCandidates(ctx anchorContext, cfg AutoAnchorConfig) ([]anchorCandidate, error) {
	minLines := cfg.MinLines
	var cands []anchorCandidate
	order := 0

	for _, s := range ctx.symbols {
		a, ok := symbolCandidate(s, ctx.lang, cfg.Prefix, minLines)
		if !ok {
			continue
		}
		cands = append(cands, anchorCandidate{anchor: a, order: order})
		order++
	}

	if cfg.IncludeImports {
		if imp, ok := importAnchor(ctx.data, ctx.lang); ok && linespan(imp) >= minLines {
			imp.Name = cfg.Prefix + imp.Name
			cands = append(cands, anchorCandidate{anchor: imp, order: order})
			order++
		}
	}

	if cfg.IncludeTests {
		tests := testAnchors(ctx.relPath, ctx.data, ctx.lang)
		for i := range tests {
			if linespan(tests[i]) < minLines {
				continue
			}
			tests[i].Name = cfg.Prefix + tests[i].Name
			cands = append(cands, anchorCandidate{anchor: tests[i], order: order})
			order++
		}
	}

	for _, coarse := range coarseAnchors(ctx.data, ctx.lang, cfg.Prefix) {
		if linespan(coarse) < minLines {
			continue
		}
		cands = append(cands, anchorCandidate{anchor: coarse, order: order})
		order++
	}

	return cands, nil
}

func rankAndFilterAnchors(cands []anchorCandidate, cfg AutoAnchorConfig) ([]Anchor, error) {
	if len(cands) == 0 {
		return nil, nil
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i].anchor, cands[j].anchor
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		if a.End != b.End {
			return a.End < b.End
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return cands[i].order < cands[j].order
	})
	anchors := make([]Anchor, 0, len(cands))
	for _, c := range cands {
		anchors = append(anchors, c.anchor)
	}
	if cfg.MaxPerFile > 0 && len(anchors) > cfg.MaxPerFile {
		anchors = anchors[:cfg.MaxPerFile]
	}
	return anchors, nil
}

func writeAnchors(existing []Anchor, autoAnchors []Anchor, total int) ([]Anchor, error) {
	out := normalizeAutoAnchors(autoAnchors, existing, total)
	return out, nil
}

func symbolCandidate(s Symbol, lang, prefix string, minLines int) (Anchor, bool) {
	start := s.Start
	end := s.End
	if start < 1 {
		start = 1
	}
	if end < start {
		end = start
	}
	if (end - start + 1) < minLines {
		return Anchor{}, false
	}
	name := prefix + symbolAnchorName(s, lang)
	return Anchor{Name: name, Start: start, End: end}, true
}

func linespan(a Anchor) int {
	return a.End - a.Start + 1
}

func coarseAnchors(data []byte, lang, prefix string) []Anchor {
	var out []Anchor
	switch lang {
	case "go":
		if a, ok := coarseRegion(data, `(?ms)^\s*const\s*\([^)]*\)`, "CONSTS"); ok {
			out = append(out, prefixedWith(a, prefix))
		}
		if a, ok := coarseRange(data, `(?m)^\s*const\s+\w`, "CONSTS"); ok {
			out = append(out, prefixedWith(a, prefix))
		}
		if a, ok := coarseRange(data, `(?m)^\s*type\s+[A-Za-z_]\w*\b`, "TYPES"); ok {
			out = append(out, prefixedWith(a, prefix))
		}
		if a, ok := coarseRange(data, `(?m)^\s*func\s+(?:\([^)]*\)\s*)?[A-Za-z_]\w*\s*\(`, "FUNCS"); ok {
			out = append(out, prefixedWith(a, prefix))
		}
	case "ts":
		if a, ok := coarseRange(data, `(?m)^\s*export\s+(?:const|let|var)\s+`, "CONSTS"); ok {
			out = append(out, prefixedWith(a, prefix))
		}
		if a, ok := coarseRange(data, `(?m)^\s*export\s+(?:interface|type|class)\b`, "TYPES"); ok {
			out = append(out, prefixedWith(a, prefix))
		}
		if a, ok := coarseRange(data, `(?m)^\s*export\s+(?:async\s+)?function\b|^\s*export\s+const\s+[A-Za-z_$][\w$]*\s*=\s*(?:async\s*)?(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*=>`, "FUNCS"); ok {
			out = append(out, prefixedWith(a, prefix))
		}
	case "java":
		if a, ok := coarseRange(data, `(?m)^\s*(?:public|protected|private|static|final|synchronized|native|abstract|default|strictfp|)\s*[\w<>\[\]]+\s+[A-Za-z_]\w*\s*\(`, "METHODS"); ok {
			out = append(out, prefixedWith(a, prefix))
		}
		if a, ok := coarseRange(data, `(?m)^\s*(?:public|protected|private)\s+[A-Z][A-Za-z0-9_]*\s*\(`, "CTORS"); ok {
			out = append(out, prefixedWith(a, prefix))
		}
		if a, ok := coarseRange(data, `(?m)^\s*(?:public|protected|private|static|final)\s+[\w<>\[\],\s]+;\s*$`, "FIELDS"); ok {
			out = append(out, prefixedWith(a, prefix))
		}
	case "cs":
		if a, ok := coarseRange(data, `(?m)^\s*(?:public|internal|protected|private|static|virtual|override|sealed|async|extern|unsafe|new)\s+.*\(`, "METHODS"); ok {
			out = append(out, prefixedWith(a, prefix))
		}
		if a, ok := coarseRange(data, `(?m)^\s*(?:public|internal|protected|private)\s+[A-Z][A-Za-z0-9_]*\s*\(`, "CTORS"); ok {
			out = append(out, prefixedWith(a, prefix))
		}
		if a, ok := coarseRange(data, `(?m)^\s*(?:public|internal|protected|private|static|readonly|const|volatile)\s+[^;]+;\s*$`, "FIELDS"); ok {
			out = append(out, prefixedWith(a, prefix))
		}
	}
	return out
}

func prefixedWith(a Anchor, prefix string) Anchor {
	a.Name = prefix + a.Name
	return a
}

func symbolAnchorName(s Symbol, lang string) string {
	parts := strings.Split(s.Symbol, ".")
	if len(parts) >= 2 {
		return "SYM:" + parts[len(parts)-2] + "." + parts[len(parts)-1]
	}
	return "SYM:" + s.Symbol
}

func importAnchor(data []byte, lang string) (Anchor, bool) {
	switch lang {
	case "java":
		lines := bytes.Split(data, []byte("\n"))
		first, last := 0, 0
		found := false
		for i := 0; i < len(lines) && i < 400; i++ {
			ln := strings.TrimSpace(string(lines[i]))
			if strings.HasPrefix(ln, "import ") {
				if !found {
					first = i + 1
					found = true
				}
				last = i + 1
				continue
			}
			if found && ln != "" && !strings.HasPrefix(ln, "//") {
				break
			}
		}
		if found && last >= first {
			return Anchor{Name: "IMPORTS", Start: first, End: last}, true
		}
	case "go":
		reTopImport := regexp.MustCompile(`(?ms)^\s*import\s+(?:\([^\)]*\)|"[^"]+")`)
		if loc := reTopImport.FindIndex(data); loc != nil {
			start := 1 + bytes.Count(data[:loc[0]], []byte("\n"))
			end := 1 + bytes.Count(data[:loc[1]], []byte("\n"))
			return Anchor{Name: "IMPORTS", Start: start, End: end}, true
		}
	case "ts":
		reImp := regexp.MustCompile(`(?m)^\s*import\s+[^;]+;?\s*$`)
		m := reImp.FindAllIndex(data, -1)
		if len(m) == 0 || m[0][0] >= 600 {
			return Anchor{}, false
		}
		first := 1 + bytes.Count(data[:m[0][0]], []byte("\n"))
		last := 1 + bytes.Count(data[:m[len(m)-1][1]], []byte("\n"))
		return Anchor{Name: "IMPORTS", Start: first, End: last}, true
	}
	return Anchor{}, false
}

func testAnchors(relPath string, data []byte, lang string) []Anchor {
	switch lang {
	case "go":
		if !strings.HasSuffix(relPath, "_test.go") {
			return nil
		}
		re := regexp.MustCompile(`(?m)^\s*func\s+(Test|Benchmark|Example)[A-Za-z0-9_]*\s*\(`)
		locs := re.FindAllIndex(data, -1)
		var out []Anchor
		for _, loc := range locs {
			start := 1 + bytes.Count(data[:loc[0]], []byte("\n"))
			out = append(out, Anchor{Name: "TEST", Start: start, End: start})
		}
		return out
	case "ts":
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

func coarseRange(data []byte, pattern, name string) (Anchor, bool) {
	re := regexp.MustCompile(pattern)
	locs := re.FindAllIndex(data, -1)
	if len(locs) == 0 {
		return Anchor{}, false
	}
	first := 1 + bytes.Count(data[:locs[0][0]], []byte("\n"))
	last := 1 + bytes.Count(data[:locs[len(locs)-1][1]], []byte("\n"))
	return Anchor{Name: name, Start: first, End: last}, true
}

func coarseRegion(data []byte, pattern, name string) (Anchor, bool) {
	re := regexp.MustCompile(pattern)
	loc := re.FindIndex(data)
	if loc == nil {
		return Anchor{}, false
	}
	first := 1 + bytes.Count(data[:loc[0]], []byte("\n"))
	last := 1 + bytes.Count(data[:loc[1]], []byte("\n"))
	return Anchor{Name: name, Start: first, End: last}, true
}

func normalizeAutoAnchors(in []Anchor, explicit []Anchor, total int) []Anchor {
	if len(in) == 0 {
		return nil
	}
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
	uniq := make([]Anchor, 0, len(in))
	for i, a := range in {
		if i > 0 {
			prev := in[i-1]
			if prev.Name == a.Name && prev.Start == a.Start && prev.End == a.End {
				continue
			}
		}
		uniq = append(uniq, a)
	}
	if len(explicit) == 0 {
		return uniq
	}
	type key struct {
		name       string
		start, end int
	}
	exp := make(map[key]struct{}, len(explicit))
	for _, a := range explicit {
		exp[key{a.Name, a.Start, a.End}] = struct{}{}
	}
	out := uniq[:0]
	for _, a := range uniq {
		if _, ok := exp[key{a.Name, a.Start, a.End}]; ok {
			continue
		}
		out = append(out, a)
	}
	return out
}

func fileLangByExt(relPath string) string {
	ext := strings.ToLower(filepath.Ext(relPath))
	switch ext {
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
