package bundle

import (
	"bytes"
	"sort"
	"strings"
	"text/template"
)

// ReadmeOptions configures README generation for FULL and DELTA bundles.
// All fields are rendered deterministically; no timestamps or environment data.
type ReadmeOptions struct {
	ModuleName        string
	SupportedLangs    []string
	PresentLangs      []string
	DiffNoPrefix      bool
	ContextLines      int
	IncludeBenchNote  bool
	IncludeDeltaNotes bool
	IncludeFullNotes  bool
}

type rdCtx struct {
	ModuleName        string
	SupportedLangsCSV string
	PresentLangsCSV   string
	DiffNoPrefix      bool
	ContextLines      int
	IncludeBenchNote  bool
}

const fullReadmeTemplate = `
# {{.ModuleName}}

This archive is a **FULL bundle** produced by *class-collector*. It contains a project snapshot plus indexing metadata for better model comprehension.

## Bundle layout
- **manifest.json** — basic bundle manifest (version, tool info).
- **symbols.json** — per-file, per-language symbol index (packages/namespaces, types, members).
- **slices.jsonl** — code/content slices with 1-based line anchors.
- **pointers.jsonl** — logical cross-links (slice ↔ symbol ↔ file).
- **graph.json** — lightweight dependency/call graph (if available).
- **TOC.md** — optional table of contents for human reading.
- **src/** — optional source tree (when emitted).

## Anchors, slices, pointers (quick guide)
- Line numbers are **1-based**.
- A *slice* is a stable textual region in a file: { "file": "path", "start": <line>, "end": <line> }.
- A *pointer* connects slices and symbols for navigation; consumers should not assume file ordering.
- Consumers should tolerate missing optional fields — formats are forward compatible.

## Diff policy (for DELTA bundles)
- DELTA bundles place a single, root-level ` + "`delta.patch`" + ` (unified diff). Per-file patches live under ` + "`diffs/`" + `; newly added files are copied under ` + "`added/`" + `.
- Oversized diffs DO NOT use textual ellipses. Instead they include a placeholder hunk comment:
# diff omitted (oversize)
- Headers omit Git-style prefixes when configured (see "Conventions").

## Conventions
- Encoding: **UTF-8**; newlines: **\n** only.
- Unified diff context: **{{.ContextLines}}** lines.
- Git-style prefixes **a/** and **b/** are {{if .DiffNoPrefix}}**omitted**{{else}}**present**{{end}}.
- Supported languages: {{.SupportedLangsCSV}}.
- Present in this bundle: {{.PresentLangsCSV}}.

{{if .IncludeBenchNote -}}
## Benchmarks
If provided via ` + "`-bench <path>`" + `, a plain-text **bench.txt** is included at the bundle root.
{{- end}}

## FAQ
- **Why no "..." inside diffs?** Because many consumers treat literal ellipses as syntax, not truncation. Oversize content uses a dedicated placeholder hunk (see above).
- **Are JSON schemas stable?** Yes; consumers should ignore unknown fields for forward compatibility.

`

const deltaReadmeTemplate = `
# {{.ModuleName}} — DELTA bundle

This archive is a **DELTA bundle** produced by *class-collector*. It contains a compact view of changes since a prior snapshot.

## Layout
- **delta.patch** — single-file unified diff aggregating **all** changes (including added files via ` + "`/dev/null → <path>`" + `).
- **diffs/** — per-file unified diffs (same content as in ` + "`delta.patch`" + `, split by file).
- **added/** — full contents of newly added files (text).
- **SUMMARY.md** — human summary of Added/Removed/Changed/Renamed/Oversize.
- **delta.index.json** — machine-readable delta index.

## Conventions
- Encoding: **UTF-8**; newlines: **\n** only.
- Unified diff context: **{{.ContextLines}}** lines.
- Git-style prefixes **a/** and **b/** are {{if .DiffNoPrefix}}**omitted**{{else}}**present**{{end}}.
- Supported languages: {{.SupportedLangsCSV}}.
- Present in this bundle: {{.PresentLangsCSV}}.

## Oversize diffs
For files exceeding internal thresholds, we include a minimal placeholder hunk:
--- <old>
+++ <new>
@@
# diff omitted (oversize)

No textual ellipses are used.

{{if .IncludeBenchNote -}}
## Benchmarks
If provided via ` + "`-bench <path>`" + `, a plain-text **bench.txt** is included at the bundle root.
{{- end}}

## How to consume
- Prefer **delta.patch** for one-pass ingestion; use **diffs/** when you need per-file routing.
- For added files, **delta.patch** contains ` + "`/dev/null → <path>`" + ` hunks; **added/** mirrors the full file body.
- Line anchors in diffs are **1-based**; consumers must not rely on file ordering.

`

func GenerateFullReadme(opts ReadmeOptions) []byte {
	return renderReadme(fullReadmeTemplate, opts)
}

func GenerateDeltaReadme(opts ReadmeOptions) []byte {
	return renderReadme(deltaReadmeTemplate, opts)
}

func renderReadme(tpl string, opts ReadmeOptions) []byte {
	name := strings.TrimSpace(opts.ModuleName)

	if name == "" {
		name = "class-collector bundle"
	}

	langs := make([]string, 0, len(opts.SupportedLangs))
	for _, l := range opts.SupportedLangs {
		if l = strings.TrimSpace(l); l != "" {
			langs = append(langs, l)
		}
	}
	sort.Strings(langs)

	plangs := make([]string, 0, len(opts.PresentLangs))
	for _, l := range opts.PresentLangs {
		if l = strings.TrimSpace(l); l != "" {
			plangs = append(plangs, l)
		}
	}
	sort.Strings(plangs)

	ctx := rdCtx{
		ModuleName:        name,
		SupportedLangsCSV: strings.Join(langs, ", "),
		PresentLangsCSV:   strings.Join(plangs, ", "),
		DiffNoPrefix:      opts.DiffNoPrefix,
		ContextLines:      opts.ContextLines,
		IncludeBenchNote:  opts.IncludeBenchNote,
	}

	t, _ := template.New("readme").Parse(tpl)
	var buf bytes.Buffer
	_ = t.Execute(&buf, ctx)
	// Normalize lines: strip trailing spaces and ensure only \n newlines; templates use \n already.
	lines := strings.Split(buf.String(), "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t")
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out)
}
