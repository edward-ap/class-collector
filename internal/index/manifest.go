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

	"class-collector/internal/graph"
	"class-collector/internal/walkwalk"
)

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

// Artifacts bundles the primary indexing outputs alongside the graph.
type Artifacts struct {
	Manifest Manifest
	Symbols  Symbols
	Slices   []Slice
	Pointers []Pointer
	Graph    graph.Graph
}

type symbolsIndex struct {
	manifest []ManFile
	symbols  []Symbol
	slices   []Slice
	pointers []Pointer
}

type fileArtifacts struct {
	manifest ManFile
	symbols  []Symbol
	slices   []Slice
	pointers []Pointer
}

// BuildArtifacts remains the primary entry point for callers that expect the
// original tuple signature. Internally it delegates to buildArtifactsSet.
func BuildArtifacts(root string, files []walkwalk.FileInfo, maxFileLines int, langHints map[string]struct{}) (Manifest, Symbols, []Slice, []Pointer) {
	art, err := buildArtifactsSet(root, files, maxFileLines, langHints)
	if err != nil {
		return Manifest{Module: filepath.Base(root)}, Symbols{}, nil, nil
	}
	return art.Manifest, art.Symbols, art.Slices, art.Pointers
}

func buildArtifactsSet(root string, files []walkwalk.FileInfo, maxFileLines int, langHints map[string]struct{}) (Artifacts, error) {
	idx, err := gatherSymbolsIndex(files, maxFileLines, langHints)
	if err != nil {
		return Artifacts{}, err
	}
	g, err := computeGraph(files)
	if err != nil {
		return Artifacts{}, err
	}
	return assembleArtifacts(root, idx, g)
}

func gatherSymbolsIndex(files []walkwalk.FileInfo, maxFileLines int, langHints map[string]struct{}) (symbolsIndex, error) {
	var idx symbolsIndex
	for _, f := range files {
		data, err := os.ReadFile(f.AbsPath)
		if err != nil {
			continue
		}
		fa, err := processFile(f, data, maxFileLines, langHints)
		if err != nil || fa == nil {
			continue
		}
		idx.manifest = append(idx.manifest, fa.manifest)
		idx.symbols = append(idx.symbols, fa.symbols...)
		idx.slices = append(idx.slices, fa.slices...)
		idx.pointers = append(idx.pointers, fa.pointers...)
	}
	return idx, nil
}

func processFile(f walkwalk.FileInfo, data []byte, maxFileLines int, langHints map[string]struct{}) (*fileArtifacts, error) {
	anchors := ExtractAnchors(f.RelPath, data)
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
	case "cpp":
		pkg, kind, typ, exports, syms = extractCPP(f.RelPath, data)
	default:
		kind = "file"
	}

	if len(langHints) > 0 {
		if _, ok := langHints[lang]; !ok {
			return nil, nil
		}
	}

	totalLines := 1 + bytes.Count(data, []byte("\n"))

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

	if aa := BuildAutoAnchors(f.RelPath, data, lang, syms, anchors, totalLines); len(aa) > 0 {
		anchors = append(anchors, aa...)
	}

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

	var slices []Slice
	if sl := BuildSlices(f.RelPath, anchors, totalLines, maxFileLines); len(sl) > 0 {
		slices = append(slices, sl...)
	}
	pointers := BuildAnchorPointers(f.RelPath, anchors)

	return &fileArtifacts{
		manifest: mf,
		symbols:  syms,
		slices:   slices,
		pointers: pointers,
	}, nil
}

func computeGraph(files []walkwalk.FileInfo) (graph.Graph, error) {
	if len(files) == 0 {
		return graph.Graph{}, nil
	}
	gfiles := make([]graph.File, 0, len(files))
	for _, f := range files {
		gfiles = append(gfiles, graph.File{
			RelPath: f.RelPath,
			AbsPath: f.AbsPath,
			Ext:     f.Ext,
		})
	}
	return graph.BuildFrom(gfiles), nil
}

func assembleArtifacts(root string, idx symbolsIndex, g graph.Graph) (Artifacts, error) {
	manFiles := make([]ManFile, len(idx.manifest))
	copy(manFiles, idx.manifest)
	sort.Slice(manFiles, func(i, j int) bool { return manFiles[i].Path < manFiles[j].Path })

	symbols := make([]Symbol, len(idx.symbols))
	copy(symbols, idx.symbols)

	slices := make([]Slice, len(idx.slices))
	copy(slices, idx.slices)
	sort.Slice(slices, func(i, j int) bool {
		if slices[i].Path == slices[j].Path {
			if slices[i].Start == slices[j].Start {
				return slices[i].End < slices[j].End
			}
			return slices[i].Start < slices[j].Start
		}
		return slices[i].Path < slices[j].Path
	})

	pointers := make([]Pointer, len(idx.pointers))
	copy(pointers, idx.pointers)
	if len(symbols) > 0 {
		if symPtrs := BuildSymbolPointers(symbols); len(symPtrs) > 0 {
			pointers = append(pointers, symPtrs...)
		}
	}

	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].Path == symbols[j].Path {
			if symbols[i].Start == symbols[j].Start {
				return symbols[i].End < symbols[j].End
			}
			return symbols[i].Start < symbols[j].Start
		}
		return symbols[i].Path < symbols[j].Path
	})

	sort.Slice(pointers, func(i, j int) bool {
		if pointers[i].ID == pointers[j].ID {
			if pointers[i].Path == pointers[j].Path {
				if pointers[i].Start == pointers[j].Start {
					return pointers[i].End < pointers[j].End
				}
				return pointers[i].Start < pointers[j].Start
			}
			return pointers[i].Path < pointers[j].Path
		}
		return pointers[i].ID < pointers[j].ID
	})

	man := Manifest{Module: filepath.Base(root), Files: manFiles}
	man.BundleID = ComputeBundleID(man)
	symOut := Symbols{Version: 1, Symbols: symbols}

	return Artifacts{
		Manifest: man,
		Symbols:  symOut,
		Slices:   slices,
		Pointers: pointers,
		Graph:    g,
	}, nil
}
