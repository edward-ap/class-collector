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
	"strings"

	"class-collector/internal/graph"
	"class-collector/internal/index"
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
) error {
	// Ensure output directory exists.
	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		return err
	}

	// Create output file and ZIP writer.
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	// 1) Core JSON artifacts (deterministic order & timestamps)
	if err := writeJSONEntry(zw, "manifest.json", man); err != nil {
		return err
	}
	if err := writeJSONEntry(zw, "symbols.json", syms); err != nil {
		return err
	}
	// OPTIONAL: write plain-text bundle id for quick checks
	if man.BundleID != "" {
		if err := writeTextEntry(zw, "BUNDLE.ID", []byte(man.BundleID+"\n")); err != nil {
			return err
		}
	}

	// graph.json — write real graph
	if err := writeJSONEntry(zw, "graph.json", g); err != nil {
		return err
	}

	// Sort and write slices.jsonl (if any)
	if len(slices) > 0 {
		sorted := make([]index.Slice, len(slices))
		copy(sorted, slices)
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

	// Sort and write pointers.jsonl (if any)
	if len(pointers) > 0 {
		sorted := make([]index.Pointer, len(pointers))
		copy(sorted, pointers)
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

	// README.md — stable content, no wall-clock timestamp to keep ZIP reproducible
	{
		const readme = "# Bundle\n\n" +
			"Module: %s\n\n" +
			"Artifacts: manifest.json, symbols.json, slices.jsonl, pointers.jsonl, graph.json\n"
		body := []byte(fmtPrintf(readme, man.Module))
		if err := writeTextEntry(zw, "README.md", body); err != nil {
			return err
		}
	}

	// TOC.md — quick table of contents from manifest (path + line count)
	{
		var b strings.Builder
		b.WriteString("# TOC\n\n| # | Path | Lines |\n|---:|:-----|-----:|\n")
		for i, f := range man.Files {
			b.WriteString("| ")
			b.WriteString(itoa(i + 1))
			b.WriteString(" | ")
			b.WriteString(f.Path)
			b.WriteString(" | ")
			b.WriteString(itoa(f.Lines))
			b.WriteString(" |\n")
		}
		if err := writeTextEntry(zw, "TOC.md", []byte(b.String())); err != nil {
			return err
		}
	}

	// src/ — optional emission of source files (sorted for determinism)
	if emitSrc && len(files) > 0 {
		sorted := make([]struct{ RelPath, AbsPath string }, len(files))
		copy(sorted, files)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].RelPath < sorted[j].RelPath })

		for _, fi := range sorted {
			zname := filepath.ToSlash(filepath.Join("src", fi.RelPath))
			zname = sanitizeZipPath(zname)
			if err := writeFileEntry(zw, zname, fi.AbsPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// writeJSONLEntry writes line-delimited JSON (one JSON object per line).
func writeJSONLEntry(zw *zip.Writer, name string, items any, marshalEach func(it any) ([]byte, error)) error {
	h := &zip.FileHeader{Name: sanitizeZipPath(name), Method: zip.Deflate}
	h.SetMode(0o644)
	h.Modified = fixedZipTime

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

// fmtPrintf avoids importing fmt globally for a single formatted write.
func fmtPrintf(format, module string) string {
	// minimal formatter for "%s"
	return strings.Replace(format, "%s", module, 1)
}
