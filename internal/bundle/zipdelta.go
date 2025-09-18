// Package bundle contains writers for full and delta bundles.
//
// This file implements the delta ZIP writer. It creates a reproducible
// archive with the following layout:
//
//	delta.index.json            # JSON index describing the delta
//	diffs/<name>.patch          # text patches (sorted by name)
//	added/<original/relpath>    # newly added files (sorted by relpath)
//
// Design goals:
//   - Deterministic output (fixed timestamps, sorted entries, stable names)
//   - Safe ZIP paths (no absolute paths, no "..", Windows-safe characters)
//   - Simple API: pass an arbitrary deltaIndex value, a map of diffs, and a list
//     of added files with their relative and absolute paths.
package bundle

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DeltaIndex is a minimal placeholder for writers that want a typed index.
// Callers may pass any JSON-serializable value to WriteDelta via the deltaIndex
// parameter; this type is provided only as an example/anchor.
type DeltaIndex struct {
	BaseModule   string
	BaseSnapshot string
	HeadSnapshot string
}

// fixedZipTime ensures byte-for-byte reproducible archives.
// (ZIP epoch start: 1980-01-01)
var fixedZipTime = time.Unix(315532800, 0).UTC()

// WriteDelta writes a delta ZIP archive:
//
//	zipPath     - output .zip path
//	deltaIndex  - any JSON-serializable value written to delta.index.json
//	diffs       - map[name]body  (text patches). Names are sanitized and sorted.
//	addedFiles  - new files to include under added/<relpath>
//
// The function guarantees deterministic ordering and fixed timestamps.
// Callers should already have generated a deterministic set of `diffs` names;
// this writer additionally sorts them and sanitizes ZIP paths.
//
// Note: duplicate names after sanitization are de-duplicated with a numeric
// suffix (-1, -2, …) to avoid ZIP entry conflicts in rare edge cases.
func WriteDelta(
	zipPath string,
	deltaIndex any,
	diffs map[string]string,
	addedFiles []struct{ RelPath, AbsPath string },
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

	// 1) delta.index.json
	if err := writeJSONEntry(zw, "delta.index.json", deltaIndex); err != nil {
		return err
	}

	// 2) diffs/ (sorted by name)
	if len(diffs) > 0 {
		names := make([]string, 0, len(diffs))
		for n := range diffs {
			names = append(names, n)
		}
		sort.Strings(names)

		used := make(map[string]struct{}, len(names))
		for _, n := range names {
			raw := "diffs/" + n
			zname := ensureUniqueName(sanitizeZipPath(raw), used)
			if err := writeTextEntry(zw, zname, []byte(diffs[n])); err != nil {
				return err
			}
		}
	}

	// 3) added/ (sorted by RelPath)
	if len(addedFiles) > 0 {
		sort.Slice(addedFiles, func(i, j int) bool {
			return addedFiles[i].RelPath < addedFiles[j].RelPath
		})

		used := make(map[string]struct{}, len(addedFiles))
		for _, fi := range addedFiles {
			raw := filepath.ToSlash(filepath.Join("added", fi.RelPath))
			zname := ensureUniqueName(sanitizeZipPath(raw), used)
			if err := writeFileEntry(zw, zname, fi.AbsPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// writeJSONEntry writes a JSON-encoded value with fixed timestamp/mode.
func writeJSONEntry(zw *zip.Writer, name string, v any) error {
	h := &zip.FileHeader{Name: sanitizeZipPath(name), Method: zip.Deflate}
	h.SetMode(0o644)
	h.Modified = fixedZipTime

	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writeTextEntry writes a raw text blob as a ZIP entry.
func writeTextEntry(zw *zip.Writer, name string, data []byte) error {
	h := &zip.FileHeader{Name: sanitizeZipPath(name), Method: zip.Deflate}
	h.SetMode(0o644)
	h.Modified = fixedZipTime

	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// writeFileEntry streams a local file into the ZIP entry.
func writeFileEntry(zw *zip.Writer, name, absPath string) error {
	h := &zip.FileHeader{Name: sanitizeZipPath(name), Method: zip.Deflate}
	h.SetMode(0o644)
	h.Modified = fixedZipTime

	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	r, err := os.Open(absPath)
	if err != nil {
		return err
	}
	defer r.Close()
	_, err = io.Copy(w, r)
	return err
}

// sanitizeZipPath:
//   - normalizes separators to '/'
//   - strips drive letters and leading slashes
//   - prevents path traversal by resolving "." and ".." without escaping root
func sanitizeZipPath(p string) string {
	s := filepath.ToSlash(p)
	// Drop Windows drive letters like "C:".
	if len(s) > 1 && s[1] == ':' {
		s = s[2:]
	}
	// Remove leading slashes to avoid absolute paths.
	s = strings.TrimLeft(s, "/")

	// Resolve "." and ".." without allowing escape above root.
	parts := strings.Split(s, "/")
	stack := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if n := len(stack); n > 0 {
				stack = stack[:n-1]
			}
			continue
		}
		stack = append(stack, part)
	}
	s = strings.Join(stack, "/")
	if s == "" {
		return "entry"
	}
	return s
}

// ensureUniqueName returns a unique ZIP entry name by appending -1, -2, ...
// if the given name already exists in the `used` set. It mutates `used`.
func ensureUniqueName(name string, used map[string]struct{}) string {
	if _, ok := used[name]; !ok {
		used[name] = struct{}{}
		return name
	}
	base := name
	ext := ""
	if i := strings.LastIndex(name, "."); i > 0 {
		base, ext = name[:i], name[i:]
	}
	for n := 1; ; n++ {
		alt := base + "-" + itoa(n) + ext
		if _, ok := used[alt]; !ok {
			used[alt] = struct{}{}
			return alt
		}
	}
}

// tiny, allocation-free integer -> string for small n (1..9999).
func itoa(n int) string {
	if n < 10 {
		return string('0' + byte(n))
	}
	// fallback to std for larger values; this path is extremely rare.
	return intToString(n)
}

func intToString(n int) string {
	// simple std conversion without importing strconv separately
	// (keeps imports minimal for this file).
	// In practice, n will be tiny because name collisions are rare.
	s := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		d := n % 10
		s = append([]byte{byte('0' + d)}, s...)
		n /= 10
	}
	return string(s)
}
