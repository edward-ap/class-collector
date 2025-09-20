// Package index assembles high-level code artifacts for the bundle:
// manifest (files metadata), symbols (API surface), slices (file regions),
// and pointers (jump targets for anchors and symbols).
package index

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"

	"class-collector/internal/walkwalk"
)

// BuildArtifacts walks the provided FileInfo set and produces:
//   - Manifest  : per-file metadata (package/class/kind/exports/hash/lines/anchors)
//   - Symbols   : flattened list of extracted symbols with 1-based line ranges
//   - Slices    : regions for large files (prefer anchors; fallback to chunking)
//   - Pointers  : jump targets for anchors and symbols
//
// Notes:
//   - langHints, when non-empty, is a set of language tags to keep (e.g. {"java","go","ts"}).
//     Files with language not in the set are skipped.
//   - maxFileLines controls the threshold for generating fallback slices in files
//     without anchors (see BuildSlices).
//
// ComputeBundleID computes a canonical hash over manifest entries.
// It concatenates lines "<normalized-path>:<lowercase-hash>\n" sorted by path,
// then returns SHA-256 hex(lowercase) of the UTF-8 bytes.
func ComputeBundleID(man Manifest) string {
	if len(man.Files) == 0 {
		sum := sha256.Sum256(nil)
		return hex.EncodeToString(sum[:])
	}
	lines := make([]string, 0, len(man.Files))
	for _, f := range man.Files {
		p := normalizePath(f.Path)
		h := toLowerHex(f.Hash)
		lines = append(lines, p+":"+h)
	}
	sort.Strings(lines)
	var buf bytes.Buffer
	for _, ln := range lines {
		buf.WriteString(ln)
		buf.WriteByte('\n')
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

func normalizePath(p string) string {
	// unify slashes, drop leading "./", collapse duplicate '/'
	b := make([]rune, 0, len(p))
	skipDotSlash := false
	for i, r := range p {
		if i == 0 && r == '.' && len(p) > 1 && p[1] == '/' {
			skipDotSlash = true
			continue
		}
		if skipDotSlash && r == '/' {
			skipDotSlash = false
			continue
		}
		if r == '\\' {
			r = '/'
		}
		if r == '/' && len(b) > 0 && b[len(b)-1] == '/' {
			continue
		}
		b = append(b, r)
	}
	return string(b)
}

func toLowerHex(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'F' {
			c = c + ('a' - 'A')
		}
		out[i] = c
	}
	return string(out)
}

func BuildArtifacts(root string, files []walkwalk.FileInfo, maxFileLines int, langHints map[string]struct{}) (Manifest, Symbols, []Slice, []Pointer) {
	var (
		allManFiles []ManFile
		allSyms     []Symbol
		allSlices   []Slice
		allPointers []Pointer
	)

	for _, f := range files {
		data, err := os.ReadFile(f.AbsPath)
		if err != nil {
			continue
		}

		// Anchors (regions) are used both for manifest metadata and for building slices/pointers.
		anchors := ExtractAnchors(f.RelPath, data)

		// Language & symbol extraction
		lang := InferLangByExt(f.Ext)
		var pkg, kind, typ string
		var exports []string
		var syms []Symbol

		switch lang {
		case "java":
			pkg, kind, typ, exports, syms = extractJava(f.RelPath, data)
		case "go":
			pkg, kind, typ, exports, syms = extractGo(f.RelPath, data)
		case "ts":
			pkg, kind, typ, exports, syms = extractTS(f.RelPath, data)
		case "kt":
			pkg, kind, typ, exports, syms = extractKotlin(f.RelPath, data)
		case "cs":
			pkg, kind, typ, exports, syms = extractCS(f.RelPath, data)
		case "py":
			pkg, kind, typ, exports, syms = extractPy(f.RelPath, data)
		default:
			kind = "file"
		}

		// Language hints filtering:
		// If hints are provided, keep only files whose inferred language is in the set.
		if len(langHints) > 0 {
			if _, ok := langHints[lang]; !ok {
				// Unknown/empty lang or not requested → skip this file entirely.
				continue
			}
		}

		// Total line count (1-based, '\n'-counting).
		totalLines := 1 + bytes.Count(data, []byte("\n"))

		// Finalize symbol End lines within the file, based on next symbol start (or file end).
		sort.Slice(syms, func(i, j int) bool { return syms[i].Start < syms[j].Start })
		for i := range syms {
			if i+1 < len(syms) {
				syms[i].End = syms[i+1].Start - 1
				if syms[i].End < syms[i].Start {
					syms[i].End = syms[i].Start
				}
			} else {
				syms[i].End = totalLines
			}
		}

		// Manifest entry for this file
		mf := ManFile{
			Path:    f.RelPath,
			Package: pkg,
			Class:   typ,
			Kind:    kind,
			Summary: "",
			Exports: exports,
			Hash:    f.SHA256Hex,
			Lines:   totalLines,
			Anchors: anchors,
		}

		// Accumulate symbols; pointers are built later via BuildSymbolPointers(allSyms).
		for i := range syms {
			allSyms = append(allSyms, syms[i])
		}

		// Synthesize virtual anchors (symbols/imports/tests)
		if aa := BuildAutoAnchors(f.RelPath, data, lang, syms, anchors, totalLines); len(aa) > 0 {
			anchors = append(anchors, aa...)
		}
		// Emit manifest entry with combined anchors
		mf = ManFile{
			Path: f.RelPath, Package: pkg, Class: typ, Kind: kind,
			Summary: "", Exports: exports, Hash: f.SHA256Hex, Lines: totalLines,
			Anchors: anchors,
		}
		allManFiles = append(allManFiles, mf)

		// Slices (prefer anchors; fallback to chunking for large files without anchors).
		if sl := BuildSlices(f.RelPath, anchors, totalLines, maxFileLines); len(sl) > 0 {
			allSlices = append(allSlices, sl...)
		}

		// Anchor-backed pointers (already carry Start/End).
		allPointers = append(allPointers, BuildAnchorPointers(f.RelPath, anchors)...)
	}

	// Build symbol-backed pointers after collecting all symbols.
	symPtrs := BuildSymbolPointers(allSyms)
	if len(symPtrs) > 0 {
		allPointers = append(allPointers, symPtrs...)
	}

	// Deterministic ordering for reproducible bundles.
	sort.Slice(allManFiles, func(i, j int) bool { return allManFiles[i].Path < allManFiles[j].Path })

	sort.Slice(allSyms, func(i, j int) bool {
		if allSyms[i].Path == allSyms[j].Path {
			if allSyms[i].Start == allSyms[j].Start {
				return allSyms[i].End < allSyms[j].End
			}
			return allSyms[i].Start < allSyms[j].Start
		}
		return allSyms[i].Path < allSyms[j].Path
	})

	sort.Slice(allSlices, func(i, j int) bool {
		if allSlices[i].Path == allSlices[j].Path {
			if allSlices[i].Start == allSlices[j].Start {
				return allSlices[i].End < allSlices[j].End
			}
			return allSlices[i].Start < allSlices[j].Start
		}
		return allSlices[i].Path < allSlices[j].Path
	})

	sort.Slice(allPointers, func(i, j int) bool {
		if allPointers[i].ID == allPointers[j].ID {
			if allPointers[i].Path == allPointers[j].Path {
				if allPointers[i].Start == allPointers[j].Start {
					return allPointers[i].End < allPointers[j].End
				}
				return allPointers[i].Start < allPointers[j].Start
			}
			return allPointers[i].Path < allPointers[j].Path
		}
		return allPointers[i].ID < allPointers[j].ID
	})

	man := Manifest{Module: filepath.Base(root), Files: allManFiles}
	// compute canonical bundle id
	man.BundleID = ComputeBundleID(man)
	symsOut := Symbols{Version: 1, Symbols: allSyms}
	return man, symsOut, allSlices, allPointers
}
