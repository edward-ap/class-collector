package ziputil

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// FixedZipTime ensures byte-for-byte reproducible archives (1980-01-01 UTC).
var FixedZipTime = time.Unix(315532800, 0).UTC()

// SanitizePath normalizes ZIP entry paths (forward slashes, no drive, no leading '/'),
// and removes '.' and '..' segments without escaping the root.
func SanitizePath(p string) string {
	s := filepath.ToSlash(p)
	if len(s) > 1 && s[1] == ':' {
		s = s[2:]
	}
	s = strings.TrimLeft(s, "/")
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

// EnsureUniqueName returns a unique name by appending -1, -2, ... when needed.
func EnsureUniqueName(name string, used map[string]struct{}) string {
	if _, ok := used[name]; !ok {
		used[name] = struct{}{}
		return name
	}
	base, ext := name, ""
	if i := strings.LastIndex(name, "."); i > 0 {
		base, ext = name[:i], name[i:]
	}
	for n := 1; ; n++ {
		alt := fmt.Sprintf("%s-%d%s", base, n, ext)
		if _, ok := used[alt]; !ok {
			used[alt] = struct{}{}
			return alt
		}
	}
}

// WriteJSON writes a JSON-encoded value with fixed timestamp and mode.
func WriteJSON(zw *zip.Writer, name string, v any) error {
	h := &zip.FileHeader{Name: SanitizePath(name), Method: zip.Deflate}
	h.SetMode(0o644)
	h.Modified = FixedZipTime
	w, err := zw.CreateHeader(h)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// WriteText writes raw text (bytes) entry with fixed timestamp.
func WriteText(zw *zip.Writer, name string, data []byte) error {
	h := &zip.FileHeader{Name: SanitizePath(name), Method: zip.Deflate}
	h.SetMode(0o644)
	h.Modified = FixedZipTime
	w, err := zw.CreateHeader(h)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// WriteFile streams data bytes as a file entry with fixed timestamp.
func WriteFile(zw *zip.Writer, name string, data []byte) error {
	return WriteText(zw, name, data)
}

// CopyFromReader writes an entry from an io.Reader to avoid buffering whole files when needed.
func CopyFromReader(zw *zip.Writer, name string, r io.Reader) error {
	h := &zip.FileHeader{Name: SanitizePath(name), Method: zip.Deflate}
	h.SetMode(0o644)
	h.Modified = FixedZipTime
	w, err := zw.CreateHeader(h)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	if _, err := io.Copy(w, r); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}
