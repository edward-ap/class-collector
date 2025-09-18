// Package graph provides a minimal import/call graph builder for heterogeneous
// codebases. It uses fast, regex-driven scanners for Java, Go, and TS/JS
// to produce a coarse graph suitable for bundle navigation.
//
// Design goals:
//   - Zero external dependencies
//   - Deterministic output (sorted nodes/edges, deduped)
//   - Safe defaults; tolerant to partial/mixed codebases
//
// Notes:
//   - Nodes are language-prefixed labels to avoid collisions:
//     java:<package>, go:<package>, js:<relpath-without-ext>, npm:<package>
//   - For TS/JS, relative imports are resolved to a normalized project-relative
//     path (without extension); bare specifiers are labeled as npm:<name>.
//   - For Java, edges are from "java:<package-of-file>" to the imported FQN
//     (normalized to package or wildcard as seen). For simplicity we retain
//     the imported name as-is; you can post-process if you need package-only.
package graph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Graph is a simple directed graph (no weights).
type Graph struct {
	Nodes []string    `json:"nodes"`
	Edges [][2]string `json:"edges"`
}

// File is the minimal file descriptor expected by BuildFrom.
type File struct {
	RelPath string // project-relative, Unix-style or OS-style both accepted
	AbsPath string // absolute path for reading
	Ext     string // lowercase extension including dot (e.g. ".java")
}

// Build keeps backward compatibility with earlier code paths and returns
// an empty graph. Prefer BuildFrom in new code.
func Build() Graph { return Graph{} }

// BuildFrom scans the given files and returns a minimal import graph.
// It tolerates unreadable files and simply skips them.
func BuildFrom(files []File) Graph {
	nodeSet := make(map[string]struct{}, 256)
	edgeSet := make(map[[2]string]struct{}, 512)

	// Determine probable project root (common directory) and parse tsconfig.json if present.
	rootAbs := commonDir(files)
	var tsr *tsResolver
	if rootAbs != "" {
		if r, err := loadTsResolver(rootAbs); err == nil {
			tsr = r
		}
	}

	for _, f := range files {
		ext := strings.ToLower(f.Ext)
		data, err := os.ReadFile(f.AbsPath)
		if err != nil {
			continue
		}

		switch ext {
		case ".java":
			pkg, imports := scanJava(data)
			if pkg == "" {
				// fallback to directory-based node if package missing
				pkg = dirAsJavaPackage(f.RelPath)
			}
			from := "java:" + pkg
			addNode(nodeSet, from)
			for _, imp := range imports {
				to := "java:" + imp
				addNode(nodeSet, to)
				addEdge(edgeSet, from, to)
			}

		case ".go":
			pkg, imports := scanGo(data)
			if pkg == "" {
				pkg = dirAsGoPackage(f.RelPath)
			}
			from := "go:" + pkg
			addNode(nodeSet, from)
			for _, imp := range imports {
				to := "go:" + imp
				addNode(nodeSet, to)
				addEdge(edgeSet, from, to)
			}

 	case ".ts", ".tsx", ".js":
			node, imports := scanTSJSWithResolver(f.RelPath, data, tsr)
			from := node
			addNode(nodeSet, from)
			for _, imp := range imports {
				addNode(nodeSet, imp)
				addEdge(edgeSet, from, imp)
			}
		default:
			// ignore other extensions
		}
	}

	// Materialize deterministic, sorted slices.
	nodes := make([]string, 0, len(nodeSet))
	for n := range nodeSet {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	edges := make([][2]string, 0, len(edgeSet))
	for e := range edgeSet {
		edges = append(edges, e)
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i][0] == edges[j][0] {
			return edges[i][1] < edges[j][1]
		}
		return edges[i][0] < edges[j][0]
	})

	return Graph{Nodes: nodes, Edges: edges}
}

// --- Java scanning -----------------------------------------------------------

var (
	reJavaPkg    = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z0-9_.]+)\s*;`)
	reJavaImport = regexp.MustCompile(`(?m)^\s*import\s+(?:static\s+)?([A-Za-z0-9_.*]+)\s*;`)
)

func scanJava(data []byte) (pkg string, imports []string) {
	if m := reJavaPkg.FindSubmatch(data); m != nil {
		pkg = string(m[1])
	}
	matches := reJavaImport.FindAllSubmatch(data, -1)
	set := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		imp := string(m[1])
		if imp != "" {
			set[imp] = struct{}{}
		}
	}
	imports = setToSortedSlice(set)
	return
}

func dirAsJavaPackage(rel string) string {
	rel = filepath.ToSlash(rel)
	dir := filepath.Dir(rel)
	dir = strings.Trim(dir, "/.")
	if dir == "" || dir == "." {
		return "default"
	}
	return strings.ReplaceAll(dir, "/", ".")
}

// --- Go scanning -------------------------------------------------------------

var (
	reGoPkg          = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z0-9_]+)\s*$`)
	reGoImportSingle = regexp.MustCompile(`(?m)^\s*import\s+(?:[A-Za-z_]\w*\s+)?\"([^\"]+)\"`)
	reGoImportBlock  = regexp.MustCompile(`(?s)import\s*\(\s*(.*?)\s*\)`)
	reGoImportLine   = regexp.MustCompile(`(?m)^\s*(?:[A-Za-z_]\w*\s+)?\"([^\"]+)\"`)
)

func scanGo(data []byte) (pkg string, imports []string) {
	if m := reGoPkg.FindSubmatch(data); m != nil {
		pkg = string(m[1])
	}
	set := make(map[string]struct{}, 8)

	// Single-line imports
	for _, m := range reGoImportSingle.FindAllSubmatch(data, -1) {
		set[string(m[1])] = struct{}{}
	}
	// Block imports
	for _, blk := range reGoImportBlock.FindAllSubmatch(data, -1) {
		body := blk[1]
		for _, m := range reGoImportLine.FindAllSubmatch(body, -1) {
			set[string(m[1])] = struct{}{}
		}
	}
	imports = setToSortedSlice(set)
	return
}

func dirAsGoPackage(rel string) string {
	rel = filepath.ToSlash(rel)
	dir := filepath.Dir(rel)
	dir = strings.Trim(dir, "/.")
	if dir == "" || dir == "." {
		return "main"
	}
	parts := strings.Split(dir, "/")
	return parts[len(parts)-1]
}

// --- TS/JS scanning ----------------------------------------------------------

var (
	reImportFrom   = regexp.MustCompile(`(?m)^\s*import\s+[^;]*?\s+from\s+['"]([^'"]+)['"]`)
	reImportOnly   = regexp.MustCompile(`(?m)^\s*import\s+['"]([^'"]+)['"]`)
	reRequireCall  = regexp.MustCompile(`(?m)require\(\s*['"]([^'"]+)['"]\s*\)`)
	reExportFrom   = regexp.MustCompile(`(?m)^\s*export\s*\{[^}]*\}\s*from\s*['"]([^'"]+)['"]`)
)

func scanTSJSWithResolver(rel string, data []byte, r *tsResolver) (node string, imports []string) {
	rel = filepath.ToSlash(rel)
	// From-node: js:<relpath-without-ext>
	base := strings.TrimSuffix(rel, filepath.Ext(rel))
	node = "js:" + base

	set := make(map[string]struct{}, 8)

	// ES6: import ... from 'spec'
	for _, m := range reImportFrom.FindAllSubmatch(data, -1) {
		spec := string(m[1])
		set[normalizeTSSpec(base, spec, r)] = struct{}{}
	}
	// ES6: import 'spec'
	for _, m := range reImportOnly.FindAllSubmatch(data, -1) {
		spec := string(m[1])
		set[normalizeTSSpec(base, spec, r)] = struct{}{}
	}
	// CJS: require('spec')
	for _, m := range reRequireCall.FindAllSubmatch(data, -1) {
		spec := string(m[1])
		set[normalizeTSSpec(base, spec, r)] = struct{}{}
	}
	// Re-exports: export { X } from 'spec'
	for _, m := range reExportFrom.FindAllSubmatch(data, -1) {
		spec := string(m[1])
		set[normalizeTSSpec(base, spec, r)] = struct{}{}
	}

	imports = setToSortedSlice(set)
	return
}

// normalizeTSSpec resolves a TS/JS specifier into a node:
//   - relative (./ or ../) → js:<normalized/project-relpath-without-ext>
//   - bare (e.g. "react")  → attempts tsconfig paths/baseUrl -> js:<rel-no-ext>; else npm:<name>
func normalizeTSSpec(baseNoExt, spec string, r *tsResolver) string {
	if spec == "" {
		return ""
	}
	if strings.HasPrefix(spec, ".") {
		// Resolve against the base file directory.
		dir := filepath.Dir(baseNoExt)
		joined := filepath.ToSlash(filepath.Clean(filepath.Join(dir, spec)))
		joined = strings.TrimSuffix(joined, filepath.Ext(joined))
		return "js:" + strings.TrimPrefix(joined, "./")
	}
	// Bare specifier (npm-style). Try tsconfig resolution if available.
	if r != nil {
		if target := r.ResolveBare(spec); target != "" {
			return "js:" + strings.TrimSuffix(filepath.ToSlash(target), filepath.Ext(target))
		}
	}
	return "npm:" + spec
}

// --- helpers -----------------------------------------------------------------

// tsResolver provides minimal tsconfig.json-based resolution for bare specifiers.
// Only compilerOptions.baseUrl and compilerOptions.paths are considered.
// For paths, only the first target pattern is used.
// Resolution returns repo-relative forward-slash paths (with extension if found).

type tsResolver struct {
	root    string // absolute project root
	baseURL string // e.g., "src"
	// patterns: key -> first target (may contain *)
	patterns [][2]string
}

func loadTsResolver(rootAbs string) (*tsResolver, error) {
	b, err := os.ReadFile(filepath.Join(rootAbs, "tsconfig.json"))
	if err != nil {
		return nil, err
	}
	var raw struct {
		CompilerOptions struct {
			BaseURL string              `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	r := &tsResolver{root: rootAbs}
	if raw.CompilerOptions.BaseURL != "" {
		r.baseURL = raw.CompilerOptions.BaseURL
	}
	// Deterministic ordering of patterns
	if len(raw.CompilerOptions.Paths) > 0 {
		keys := make([]string, 0, len(raw.CompilerOptions.Paths))
		for k := range raw.CompilerOptions.Paths {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := raw.CompilerOptions.Paths[k]
			if len(v) == 0 || v[0] == "" {
				continue
			}
			r.patterns = append(r.patterns, [2]string{k, v[0]})
		}
	}
	return r, nil
}

// ResolveBare tries to map a bare specifier using paths and baseUrl.
// Returns repo-relative path if a file exists; else empty string.
func (r *tsResolver) ResolveBare(spec string) string {
	if r == nil || spec == "" {
		return ""
	}
	// 1) Try paths mappings (first match wins; last in list? We keep deterministic by sorted keys)
	for _, kv := range r.patterns {
		key, target := kv[0], kv[1]
		if !strings.Contains(key, "*") {
			if key == spec {
				p := r.joinPath(target)
				if rel := r.findExisting(p); rel != "" {
					return rel
				}
			}
			continue
		}
		// wildcard pattern prefix/suffix
		parts := strings.SplitN(key, "*", 2)
		pre, suf := parts[0], parts[1]
		if strings.HasPrefix(spec, pre) && strings.HasSuffix(spec, suf) {
			mid := spec[len(pre) : len(spec)-len(suf)]
			candidate := strings.ReplaceAll(target, "*", mid)
			p := r.joinPath(candidate)
			if rel := r.findExisting(p); rel != "" {
				return rel
			}
		}
	}
	// 2) baseUrl fallback
	if r.baseURL != "" {
		p := r.joinPath(filepath.ToSlash(filepath.Join(r.baseURL, spec)))
		if rel := r.findExisting(p); rel != "" {
			return rel
		}
	}
	return ""
}

func (r *tsResolver) joinPath(p string) string {
	p = filepath.ToSlash(p)
	if strings.HasPrefix(p, "/") {
		p = p[1:]
	}
	return p
}

// findExisting tries common TS/JS file variants for a repo-relative path.
// Returns repo-relative path with extension if found; tries index.* for directories.
func (r *tsResolver) findExisting(rel string) string {
	if rel == "" {
		return ""
	}
	abs := filepath.Join(r.root, filepath.FromSlash(rel))
	// If rel already has an extension, test it directly.
	ext := filepath.Ext(rel)
	if ext != "" {
		if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
			return filepath.ToSlash(rel)
		}
	}
	// Try file with known extensions
	extsToTry := []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}
	for _, e := range extsToTry {
		if fi, err := os.Stat(abs + e); err == nil && !fi.IsDir() {
			return filepath.ToSlash(rel + e)
		}
	}
	// Try index.* inside directory
	if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
		for _, e := range extsToTry {
			p := filepath.Join(abs, "index"+e)
			if fi2, err2 := os.Stat(p); err2 == nil && !fi2.IsDir() {
				rel2, _ := filepath.Rel(r.root, p)
				return filepath.ToSlash(rel2)
			}
		}
	}
	return ""
}

// commonDir computes the common parent directory of all files; returns empty if none.
func commonDir(files []File) string {
	if len(files) == 0 {
		return ""
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		if f.AbsPath != "" {
			paths = append(paths, filepath.Dir(f.AbsPath))
		}
	}
	if len(paths) == 0 {
		return ""
	}
	pref := filepath.Clean(paths[0])
	for _, p := range paths[1:] {
		for !strings.HasPrefix(filepath.ToSlash(p)+"/", filepath.ToSlash(pref)+"/") {
			pref = filepath.Dir(pref)
			if pref == "." || pref == "/" || pref == "" {
				return ""
			}
		}
	}
	return pref
}

func addNode(set map[string]struct{}, n string) {
	if n == "" {
		return
	}
	set[n] = struct{}{}
}

func addEdge(set map[[2]string]struct{}, from, to string) {
	if from == "" || to == "" || from == to {
		return
	}
	set[[2]string{from, to}] = struct{}{}
}

func setToSortedSlice(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
