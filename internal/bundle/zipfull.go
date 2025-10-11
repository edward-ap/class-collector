// Package bundle contains writers for full and delta bundles.
//
// This file implements the FULL bundle ZIP writer. It creates a reproducible
// archive with the following layout:
//
//	manifest.json
//	symbols.json
//	graph.json # placeholder or actual graph
//	slices.jsonl # optional, line-delimited JSON
//	pointers.jsonl # optional, line-delimited JSON
//	README.md # stable (no wall-clock timestamps)
//	src/<project files> # optional, if emitSrc=true
//
// Design goals:
//   - Deterministic output (fixed timestamps, sorted entries)
//   - Safe ZIP paths (no absolute paths, no traversal, Windows-safe)
//   - Minimal, clear helpers (JSON, JSONL, file streaming)
package bundle

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"class-collector/internal/graph"
	"class-collector/internal/index"
	"class-collector/internal/textutil"
	"class-collector/internal/ziputil"
)

// WriteFull writes the full bundle zip.
func WriteFull(
	zipPath, root string,
	files []struct{ RelPath, AbsPath string },
	man index.Manifest,
	syms index.Symbols,
	slices []index.Slice,
	pointers []index.Pointer,
	g graph.Graph,
	emitSrc bool,
	benchPath string,
	diffContext int,
	diffNoPrefix bool,
) error {
	_ = root
	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	art := index.Artifacts{
		Manifest: man,
		Symbols:  syms,
		Slices:   slices,
		Pointers: pointers,
		Graph:    g,
	}

	if err := writeCoreJson(zw, art); err != nil {
		return err
	}

	fullLangs := supportedLangs()
	presentLangs := presentLangsFromManifest(man)

	readmeOpts := ReadmeOptions{
		ModuleName:       man.Module,
		SupportedLangs:   fullLangs,
		PresentLangs:     presentLangs,
		DiffNoPrefix:     diffNoPrefix,
		ContextLines:     diffContext,
		IncludeBenchNote: strings.TrimSpace(benchPath) != "",
		IncludeFullNotes: true,
	}

	if err := writeReadmeFull(zw, readmeOpts); err != nil {
		return err
	}
	if err := writeToc(zw, man); err != nil {
		return err
	}
	if err := writeSourcesIfEnabled(zw, files, emitSrc); err != nil {
		return err
	}
	if err := writeBenchIfPresent(zw, benchPath); err != nil {
		return err
	}
	return nil
}

func writeCoreJson(zw *zip.Writer, art index.Artifacts) error {
	if err := ziputil.WriteJSON(zw, "manifest.json", art.Manifest); err != nil {
		return err
	}
	if err := ziputil.WriteJSON(zw, "symbols.json", art.Symbols); err != nil {
		return err
	}
	if art.Manifest.BundleID != "" {
		id := textutil.EnsureTrailingLF(textutil.NormalizeUTF8LF([]byte(art.Manifest.BundleID)))
		if err := ziputil.WriteText(zw, "BUNDLE.ID", id); err != nil {
			return err
		}
	}
	if err := ziputil.WriteJSON(zw, "graph.json", art.Graph); err != nil {
		return err
	}

	if len(art.Slices) > 0 {
		sorted := make([]index.Slice, len(art.Slices))
		copy(sorted, art.Slices)
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].Path == sorted[j].Path {
				if sorted[i].Start == sorted[j].Start {
					return sorted[i].End < sorted[j].End
				}
				return sorted[i].Start < sorted[j].Start
			}
			return sorted[i].Path < sorted[j].Path
		})
		if err := writeJSONLEntry(zw, "slices.jsonl", sorted, func(it any) ([]byte, error) {
			return json.Marshal(it)
		}); err != nil {
			return err
		}
	}

	if len(art.Pointers) > 0 {
		sorted := make([]index.Pointer, len(art.Pointers))
		copy(sorted, art.Pointers)
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].ID == sorted[j].ID {
				if sorted[i].Path == sorted[j].Path {
					return sorted[i].Start < sorted[j].Start
				}
				return sorted[i].Path < sorted[j].Path
			}
			return sorted[i].ID < sorted[j].ID
		})
		if err := writeJSONLEntry(zw, "pointers.jsonl", sorted, func(it any) ([]byte, error) {
			return json.Marshal(it)
		}); err != nil {
			return err
		}
	}
	return nil
}

func writeReadmeFull(zw *zip.Writer, opts ReadmeOptions) error {
	readme := GenerateFullReadme(opts)
	readme = textutil.EnsureTrailingLF(textutil.NormalizeUTF8LF(readme))
	return ziputil.WriteText(zw, "README.md", readme)
}

func writeToc(zw *zip.Writer, man index.Manifest) error {
	var b strings.Builder
	b.WriteString("# TOC\n\n| # | Path | Lines |\n|---:|:-----|-----:|\n")
	for i, f := range man.Files {
		b.WriteString("| ")
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(" | ")
		b.WriteString(f.Path)
		b.WriteString(" | ")
		b.WriteString(strconv.Itoa(f.Lines))
		b.WriteString(" |\n")
	}
	text := textutil.EnsureTrailingLF(textutil.NormalizeUTF8LF([]byte(b.String())))
	return ziputil.WriteText(zw, "TOC.md", text)
}

func writeSourcesIfEnabled(zw *zip.Writer, files []struct{ RelPath, AbsPath string }, emit bool) error {
	if !emit || len(files) == 0 {
		return nil
	}
	sorted := make([]struct{ RelPath, AbsPath string }, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].RelPath < sorted[j].RelPath })
	for _, fi := range sorted {
		zname := filepath.ToSlash(filepath.Join("src", fi.RelPath))
		zname = ziputil.SanitizePath(zname)
		data, err := os.ReadFile(fi.AbsPath)
		if err != nil {
			return err
		}
		if err := ziputil.WriteFile(zw, zname, data); err != nil {
			return err
		}
	}
	return nil
}

func writeBenchIfPresent(zw *zip.Writer, benchPath string) error {
	if strings.TrimSpace(benchPath) == "" {
		return nil
	}
	data, err := os.ReadFile(benchPath)
	if err != nil {
		return err
	}
	return ziputil.WriteFile(zw, "bench.txt", data)
}

func writeJSONLEntry(zw *zip.Writer, name string, items any, marshalEach func(it any) ([]byte, error)) error {
	h := &zip.FileHeader{Name: ziputil.SanitizePath(name), Method: zip.Deflate}
	h.SetMode(0o644)
	h.Modified = ziputil.FixedZipTime

	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	rv := reflect.ValueOf(items)
	for i := 0; i < rv.Len(); i++ {
		b, err := marshalEach(rv.Index(i).Interface())
		if err != nil {
			return err
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			return err
		}
	}
	return nil
}

func presentLangsFromManifest(man index.Manifest) []string {
	seen := map[string]struct{}{}
	add := func(p string) {
		ext := strings.ToLower(filepath.Ext(p))
		switch ext {
		case ".go":
			seen["go"] = struct{}{}
		case ".java":
			seen["java"] = struct{}{}
		case ".kt":
			seen["kt"] = struct{}{}
		case ".cs":
			seen["cs"] = struct{}{}
		case ".ts":
			seen["ts"] = struct{}{}
		case ".tsx":
			seen["tsx"] = struct{}{}
		case ".py":
			seen["py"] = struct{}{}
		case ".cpp", ".cc", ".cxx", ".hpp", ".hh", ".h":
			seen["cpp"] = struct{}{}
		}
	}
	for _, f := range man.Files {
		add(f.Path)
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
