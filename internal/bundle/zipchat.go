package bundle

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"class-collector/internal/graph"
	"class-collector/internal/index"
)

// isTestPath reports whether a path looks like a test file or under a test directory.
func isTestPath(p string) bool {
	pp := strings.ReplaceAll(p, "\\", "/")
	if strings.Contains(pp, "/test/") || strings.HasSuffix(pp, "_test.go") {
		return true
	}
	return false
}

// WriteChat creates a deterministic ZIP archive containing Markdown chat messages
// under chat/msg-XXXX.md. Each message aggregates up to maxClasses files and
// up to maxChars characters. Partitioning is deterministic by path ordering.
//
// Ranking (minimal, deterministic):
//  1) If prev snapshot info is not available here, we fall back to manifest order
//     (which is already sorted by path). This keeps output deterministic.
//  2) Future enhancements can incorporate change status and graph degree.
func WriteChat(
	zipPath string,
	man index.Manifest,
	files []struct{ RelPath, AbsPath string },
	syms index.Symbols,
	g graph.Graph,
	maxClasses int,
	maxChars int,
) error {
	if maxClasses <= 0 {
		maxClasses = 10
	}
	if maxChars <= 0 {
		maxChars = 80_000
	}

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

 // Build deterministic ordering of files with ranking (changed → degree → exports → tests → path)
 order := make([]index.ManFile, len(man.Files))
 copy(order, man.Files)

 // Precompute per-file degree using the graph for TS/JS files (best-effort).
 deg := make(map[string]int, len(order))
 // For TS/JS, compute node label and count degree.
 for i := range order {
 	p := order[i].Path
 	ext := strings.ToLower(filepath.Ext(p))
 	if ext == ".ts" || ext == ".tsx" || ext == ".js" || ext == ".jsx" || ext == ".mjs" || ext == ".cjs" {
 		noext := strings.TrimSuffix(filepath.ToSlash(p), filepath.Ext(p))
 		node := "js:" + noext
 		// count edges touching node
 		count := 0
 		for _, e := range g.Edges {
 			if e[0] == node || e[1] == node { count++ }
 		}
 		deg[p] = count
 	}
 }

 sort.Slice(order, func(i, j int) bool {
 	a, b := order[i], order[j]
 	// 1) Changed (not available here) — treat equal.
 	// 2) Degree desc (higher first)
 	da, db := deg[a.Path], deg[b.Path]
 	if da != db { return da > db }
 	// 3) Has exports desc
 	he, hf := len(a.Exports) > 0, len(b.Exports) > 0
 	if he != hf { return he && !hf }
 	// 4) Non-tests before tests
 	ti, tj := isTestPath(a.Path), isTestPath(b.Path)
 	if ti != tj { return !ti && tj }
 	// 5) Path asc
 	return a.Path < b.Path
 })

	// Build quick index from rel path to absolute path for code reading.
	absOf := make(map[string]string, len(files))
	for _, fi := range files {
		absOf[filepath.ToSlash(fi.RelPath)] = fi.AbsPath
	}

	msgIdx := 0
	i := 0
	for i < len(order) {
		msgIdx++
		// Compose a message with ≤maxClasses items and ≤maxChars characters.
		name := filepath.ToSlash(filepath.Join("chat", pad4(msgIdx)+".md"))
		h := &zip.FileHeader{Name: sanitizeZipPath(name), Method: zip.Deflate}
		h.SetMode(0o644)
		h.Modified = fixedZipTime
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		written := 0
		classes := 0

		for classes < maxClasses && i < len(order) {
			mf := order[i]
			i++
			classes++

			// Header for the section
			sec := buildHeader(mf)
			n, err := writeBounded(w, []byte(sec), maxChars-written)
			if err != nil {
				return err
			}
			written += n
			if written >= maxChars {
				break
			}

			// Emit code fenced block for the entire file (bounded by remaining chars).
			lang := langFromExt(filepath.Ext(mf.Path))
			codeStart := "```" + lang + "\n"
			n, err = writeBounded(w, []byte(codeStart), maxChars-written)
			if err != nil { return err }
			written += n
			if written < maxChars {
				if abs := absOf[mf.Path]; abs != "" {
					if err := writeFileBounded(w, abs, maxChars-written); err != nil { return err }
					// We can't know exactly how many bytes were written; to keep deterministic bounds,
					// we conservatively set written = maxChars-1 and close the block with one more char below.
					written = maxChars - 1
				}
			}
			if written < maxChars {
				n, err = writeBounded(w, []byte("\n"+"```\n\n"), maxChars-written)
				if err != nil { return err }
				written += n
			} else {
				// Ensure the fence is closed deterministically even if truncated earlier.
				_, _ = w.Write([]byte("\n```\n"))
			}
		}
	}

	return nil
}

func buildHeader(mf index.ManFile) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(mf.Path)
	b.WriteString("\n")
	if mf.Package != "" || mf.Class != "" {
		b.WriteString("- Package: ")
		if mf.Package != "" { b.WriteString(mf.Package) } else { b.WriteString("-") }
		b.WriteString("\n- Class: ")
		if mf.Class != "" { b.WriteString(mf.Class) } else { b.WriteString("-") }
		b.WriteString("\n")
	}
	if len(mf.Exports) > 0 {
		b.WriteString("- Exports: ")
		for i, e := range mf.Exports {
			if i > 0 { b.WriteString(", ") }
			b.WriteString(e)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

func writeBounded(w io.Writer, data []byte, remain int) (int, error) {
	if remain <= 0 {
		return 0, nil
	}
	if len(data) > remain {
		data = data[:remain]
	}
	n, err := w.Write(data)
	return n, err
}

func writeFileBounded(w io.Writer, absPath string, remain int) error {
	if remain <= 0 {
		return nil
	}
	f, err := os.Open(absPath)
	if err != nil {
		return nil // skip unreadable files deterministically
	}
	defer f.Close()
	buf := make([]byte, 32*1024)
	left := remain
	for left > 0 {
		n := left
		if n > len(buf) { n = len(buf) }
		k, er := f.Read(buf[:n])
		if k > 0 {
			if _, ew := w.Write(buf[:k]); ew != nil { return ew }
			left -= k
		}
		if er != nil {
			break
		}
	}
	return nil
}

func pad4(n int) string {
	s := itoa(n)
	for len(s) < 4 { s = "0" + s }
	return s
}

func langFromExt(ext string) string {
	s := strings.ToLower(ext)
	switch s {
	case ".go": return "go"
	case ".java": return "java"
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs": return "ts"
	case ".kt": return "kotlin"
	case ".cs": return "csharp"
	case ".py": return "python"
	case ".md": return "markdown"
	default: return ""
	}
}
