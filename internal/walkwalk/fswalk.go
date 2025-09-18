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

// CollectFiles walks src and returns files matching the provided filters.
//
// Filters:
//   - exts    — set of allowed extensions (including dot, lowercase). If empty,
//     all extensions are accepted unless -include is used upstream.
//   - exclude — set of base-name prefixes (dir/file) to skip (case-sensitive).
//   - includes— substrings (case-insensitive) that force-include a path even
//     if its extension is not in exts.
//   - maxBytes— soft limit on the total size of collected files. When the limit
//     is approached, the walker skips further descent into subtrees
//     and refrains from adding files that would push total over the cap.
//   - followSymlinks — whether to traverse symlinked directories/files.
//
// Determinism:
//   - Output is sorted by RelPath.
//   - RelPath uses forward slashes on all platforms.
//
// Safety:
//   - When following symlinks, any target that would resolve outside src is
//     rejected (RelPath would contain "..").
func CollectFiles(
	src string,
	exts, exclude map[string]struct{},
	includes []string,
	maxBytes int64,
	maxFileBytes int64,
	useGitignore bool,
	followSymlinks bool,
) ([]FileInfo, int64, error) {
	var (
		list  []FileInfo
		total int64
	)

	// Normalize src to an absolute path once; WalkDir will hand us absolute paths.
	srcAbs, _ := filepath.Abs(src)

	// Parse root .gitignore if requested
	var gipats []gitPattern
	if useGitignore {
		gipats, _ = parseGitignore(filepath.Join(srcAbs, ".gitignore"))
	}

	walkFn := func(path string, d fs.DirEntry, err error) error {
		// Ignore unreadable entries, keep walking sibling paths.
		if err != nil {
			return nil
		}

		// If we already hit the size budget, prune subtrees aggressively.
		if maxBytes > 0 && total >= maxBytes {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		base := filepath.Base(path)

		// Compute project-relative path early for .gitignore evaluation.
		rel, rerr := filepath.Rel(srcAbs, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "../") || rel == ".." {
			// Outside of src root — skip defensively.
			return nil
		}

		// Exclude by exact base name or by prefix.
		if _, bad := exclude[base]; bad || hasExcludedPrefix(base, exclude) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// .gitignore filtering
		if useGitignore && matchGitignore(gipats, rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Directory handling (including symlinked dirs).
		if d.IsDir() {
			if !followSymlinks && isSymlink(d) {
				return filepath.SkipDir
			}
			return nil
		}

		// File handling: skip symlinked files if not allowed.
		if !followSymlinks && isSymlink(d) {
			return nil
		}

		info, ierr := d.Info()
		if ierr != nil || !info.Mode().IsRegular() {
			return nil
		}

		// Per-file size guardrail
		if maxFileBytes > 0 && info.Size() > maxFileBytes {
			return nil
		}

		// Extension filter (lowercased).
		ext := strings.ToLower(filepath.Ext(path))
		if len(exts) > 0 {
			if _, ok := exts[ext]; !ok {
				// allow forced include by substring if specified
				if !matchesInclude(path, includes) {
					return nil
				}
			}
		}

		// Hash & size
		sumHex, herr := sha256File(path)
		if herr != nil {
			return nil
		}
		size := info.Size()

		// Honor size budget: do not add a file that would exceed maxBytes.
		if maxBytes > 0 && total+size > maxBytes {
			return nil
		}

		list = append(list, FileInfo{
			RelPath:   rel,
			AbsPath:   path,
			Size:      size,
			SHA256Hex: sumHex,
			Ext:       ext,
		})
		total += size
		return nil
	}

	// WalkDir provides efficient directory traversal with d.IsDir() available
	// without extra stat calls for each entry.
	if err := filepath.WalkDir(srcAbs, walkFn); err != nil {
		return nil, 0, err
	}

	// Deterministic order by relative path.
	sort.Slice(list, func(i, j int) bool { return list[i].RelPath < list[j].RelPath })
	return list, total, nil
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
	neg      bool       // pattern starts with '!'
	dirOnly  bool       // pattern ends with '/'
	anchored bool       // pattern starts with '/'
	rx       *regexp.Regexp // compiled matcher
}

// parseGitignore reads a .gitignore file and compiles patterns. Minimal support:
//  - '#' comments, blank lines ignored
//  - '!' negation
//  - leading '/' anchors to repo root
//  - trailing '/' restricts to directories
//  - '**' matches across directories
//  - '*' and '?' behave like shell globs (not crossing '/')
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
