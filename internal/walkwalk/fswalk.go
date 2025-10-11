// Package walkwalk provides a deterministic, filterable filesystem walker
// used by the bundle collector to gather candidate source files.
package walkwalk

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// FileInfo is a minimal, deterministic descriptor of a collected file.
type FileInfo struct {
	RelPath   string // project-relative path with forward slashes
	AbsPath   string // absolute filesystem path
	Size      int64  // size in bytes
	SHA256Hex string // lowercase hex sha256 of the file contents
	Ext       string // lowercase extension including dot (e.g., ".java")
}

type walkerConfig struct {
	src            string
	exts           map[string]struct{}
	exclude        map[string]struct{}
	includes       []string
	maxBytes       int64
	maxFileBytes   int64
	useGitignore   bool
	followSymlinks bool
}

type walkState struct {
	cfg      walkerConfig
	root     string
	patterns []gitPattern
	total    int64
	files    []FileInfo
}

// CollectFiles walks src and returns files matching the provided filters.
func CollectFiles(
	src string,
	exts, exclude map[string]struct{},
	includes []string,
	maxBytes int64,
	maxFileBytes int64,
	useGitignore bool,
	followSymlinks bool,
) ([]FileInfo, int64, error) {
	cfg := walkerConfig{
		src:            src,
		exts:           exts,
		exclude:        exclude,
		includes:       includes,
		maxBytes:       maxBytes,
		maxFileBytes:   maxFileBytes,
		useGitignore:   useGitignore,
		followSymlinks: followSymlinks,
	}
	root, patterns, err := resolveRootsAndIgnores(cfg)
	if err != nil {
		return nil, 0, err
	}
	files, total, err := scanDir(root, cfg, patterns)
	if err != nil {
		return nil, 0, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelPath < files[j].RelPath })
	return files, total, nil
}

func resolveRootsAndIgnores(cfg walkerConfig) (string, []gitPattern, error) {
	srcAbs, err := filepath.Abs(cfg.src)
	if err != nil {
		return "", nil, err
	}
	if !cfg.useGitignore {
		return srcAbs, nil, nil
	}
	pats, err := parseGitignore(filepath.Join(srcAbs, ".gitignore"))
	if err != nil {
		return srcAbs, nil, nil
	}
	return srcAbs, pats, nil
}

func scanDir(root string, cfg walkerConfig, patterns []gitPattern) ([]FileInfo, int64, error) {
	state := &walkState{cfg: cfg, root: root, patterns: patterns}
	if err := filepath.WalkDir(root, state.visit); err != nil {
		return nil, 0, err
	}
	return state.files, state.total, nil
}

func (ws *walkState) visit(path string, d fs.DirEntry, err error) error {
	if err != nil {
		return nil
	}
	if ws.cfg.maxBytes > 0 && ws.total >= ws.cfg.maxBytes {
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	rel, ok := ws.relative(path)
	if !ok {
		return nil
	}
	if ws.shouldSkip(rel, d) {
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	if d.IsDir() {
		return ws.handleDir(d)
	}
	return ws.handleFile(path, rel, d)
}

func (ws *walkState) relative(path string) (string, bool) {
	rel, err := filepath.Rel(ws.root, path)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "../") || rel == ".." {
		return "", false
	}
	return rel, true
}

func (ws *walkState) shouldSkip(rel string, d fs.DirEntry) bool {
	base := filepath.Base(rel)
	if _, bad := ws.cfg.exclude[base]; bad || hasExcludedPrefix(base, ws.cfg.exclude) {
		return true
	}
	if ws.cfg.useGitignore && matchGitignore(ws.patterns, rel, d.IsDir()) {
		return true
	}
	return false
}

func (ws *walkState) handleDir(d fs.DirEntry) error {
	if !ws.cfg.followSymlinks && isSymlink(d) {
		return filepath.SkipDir
	}
	return nil
}

func (ws *walkState) handleFile(path, rel string, d fs.DirEntry) error {
	if !ws.cfg.followSymlinks && isSymlink(d) {
		return nil
	}
	info, err := d.Info()
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	if ws.cfg.maxFileBytes > 0 && info.Size() > ws.cfg.maxFileBytes {
		return nil
	}
	if !shouldInclude(path, ws.cfg) {
		return nil
	}
	sumHex, err := sha256File(path)
	if err != nil {
		return nil
	}
	if ws.cfg.maxBytes > 0 && ws.total+info.Size() > ws.cfg.maxBytes {
		return nil
	}
	ws.files = append(ws.files, FileInfo{
		RelPath:   rel,
		AbsPath:   path,
		Size:      info.Size(),
		SHA256Hex: sumHex,
		Ext:       strings.ToLower(filepath.Ext(path)),
	})
	ws.total += info.Size()
	return nil
}

func shouldInclude(path string, cfg walkerConfig) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if len(cfg.exts) == 0 {
		return true
	}
	if _, ok := cfg.exts[ext]; ok {
		return true
	}
	return matchesInclude(path, cfg.includes)
}

// isSymlink reports whether the DirEntry is a symlink (file or directory).
func isSymlink(d fs.DirEntry) bool {
	return d.Type()&fs.ModeSymlink != 0
}

// matchesInclude reports whether path contains any of the provided substrings
// (case-insensitive). Empty include list returns false.
func matchesInclude(path string, includes []string) bool {
	if len(includes) == 0 {
		return false
	}
	lc := strings.ToLower(path)
	for _, inc := range includes {
		if inc == "" {
			continue
		}
		if strings.Contains(lc, strings.ToLower(inc)) {
			return true
		}
	}
	return false
}

// hasExcludedPrefix reports whether base begins with any of the exclude keys.
// This allows skipping "build*", "dist*", etc., while still permitting exact-match
// excludes via the map membership check.
func hasExcludedPrefix(base string, exclude map[string]struct{}) bool {
	for k := range exclude {
		if strings.HasPrefix(base, k) {
			return true
		}
	}
	return false
}

// sha256File computes a hex-encoded sha256 for the file at path.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---------------- .gitignore support ----------------

type gitPattern struct {
	neg      bool           // pattern starts with '!'
	dirOnly  bool           // pattern ends with '/'
	anchored bool           // pattern starts with '/'
	rx       *regexp.Regexp // compiled matcher
}

// parseGitignore reads a .gitignore file and compiles patterns. Minimal support:
//   - '#' comments, blank lines ignored
//   - '!' negation
//   - leading '/' anchors to repo root
//   - trailing '/' restricts to directories
//   - '**' matches across directories
//   - '*' and '?' behave like shell globs (not crossing '/')
func parseGitignore(path string) ([]gitPattern, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var res []gitPattern
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		neg := false
		if strings.HasPrefix(line, "!") {
			neg = true
			line = strings.TrimSpace(line[1:])
			if line == "" {
				continue
			}
		}
		dirOnly := strings.HasSuffix(line, "/")
		if dirOnly {
			line = strings.TrimSuffix(line, "/")
		}
		anchored := strings.HasPrefix(line, "/")
		if anchored {
			line = strings.TrimPrefix(line, "/")
		}
		rx := compileGitGlob(line, anchored, dirOnly)
		res = append(res, gitPattern{neg: neg, dirOnly: dirOnly, anchored: anchored, rx: rx})
	}
	return res, nil
}

func compileGitGlob(glob string, anchored, dirOnly bool) *regexp.Regexp {
	// Escape regex meta, then translate gitignore globs
	esc := regexp.QuoteMeta(glob)
	// Undo escapes for glob syntax
	esc = strings.ReplaceAll(esc, "\\*\\*", "__DOUBLESTAR__")
	esc = strings.ReplaceAll(esc, "\\*", "[^/]*")
	esc = strings.ReplaceAll(esc, "\\?", "[^/]")
	esc = strings.ReplaceAll(esc, "__DOUBLESTAR__", ".*")
	var pattern string
	if anchored {
		pattern = "^" + esc + "$"
	} else {
		// Unanchored: match anywhere in the path
		pattern = "(^|.*/)" + esc + "$"
	}
	if dirOnly {
		// We'll ensure dirOnly logic in matcher using isDir flag; keep pattern as-is.
	}
	rx := regexp.MustCompile(pattern)
	return rx
}

func matchGitignore(pats []gitPattern, rel string, isDir bool) bool {
	if len(pats) == 0 {
		return false
	}
	ignored := false
	for _, p := range pats {
		if p.rx.MatchString(rel) {
			if p.dirOnly && !isDir {
				continue
			}
			ignored = !p.neg
		}
	}
	return ignored
}
