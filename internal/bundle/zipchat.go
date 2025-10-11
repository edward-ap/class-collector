package bundle

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"class-collector/internal/graph"
	"class-collector/internal/index"
	"class-collector/internal/textutil"
	"class-collector/internal/ziputil"
)

type chatMessageMeta struct {
	Name  string
	Files []string
}

// WriteChat creates a deterministic ZIP archive with Markdown chat messages under chat/msg-XXXX.md.
func WriteChat(
	zipPath string,
	man index.Manifest,
	files []struct{ RelPath, AbsPath string },
	syms index.Symbols,
	g graph.Graph,
	maxClasses int,
	maxChars int,
	benchPath string,
) error {
	maxClasses, maxChars = normalizeChatLimits(maxClasses, maxChars)

	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		return fmt.Errorf("mkdir output: %w", err)
	}
	f, err := os.Create(zipPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	order := rankChatOrder(man, g)
	absOf := buildAbsIndex(files)

	metas, err := writeChatMessages(zw, order, absOf, maxClasses, maxChars)
	if err != nil {
		return err
	}
	if err := writeChatToc(zw, metas); err != nil {
		return err
	}
	if err := writeChatReadme(zw, man, syms, metas, maxClasses, maxChars); err != nil {
		return err
	}
	if err := writeChatBench(zw, benchPath); err != nil {
		return err
	}
	return nil
}

func normalizeChatLimits(maxClasses, maxChars int) (int, int) {
	if maxClasses <= 0 {
		maxClasses = 10
	}
	if maxChars <= 0 {
		maxChars = 80_000
	}
	return maxClasses, maxChars
}

func rankChatOrder(man index.Manifest, g graph.Graph) []index.ManFile {
	order := make([]index.ManFile, len(man.Files))
	copy(order, man.Files)

	deg := make(map[string]int, len(order))
	for i := range order {
		p := order[i].Path
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".ts" || ext == ".tsx" || ext == ".js" || ext == ".jsx" || ext == ".mjs" || ext == ".cjs" {
			noext := strings.TrimSuffix(filepath.ToSlash(p), filepath.Ext(p))
			node := "js:" + noext
			count := 0
			for _, e := range g.Edges {
				if e[0] == node || e[1] == node {
					count++
				}
			}
			deg[p] = count
		}
	}

	sort.Slice(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if da, db := deg[a.Path], deg[b.Path]; da != db {
			return da > db
		}
		hasExportsA, hasExportsB := len(a.Exports) > 0, len(b.Exports) > 0
		if hasExportsA != hasExportsB {
			return hasExportsA && !hasExportsB
		}
		ta, tb := isTestPath(a.Path), isTestPath(b.Path)
		if ta != tb {
			return !ta && tb
		}
		return a.Path < b.Path
	})
	return order
}

func buildAbsIndex(files []struct{ RelPath, AbsPath string }) map[string]string {
	out := make(map[string]string, len(files))
	for _, fi := range files {
		out[filepath.ToSlash(fi.RelPath)] = fi.AbsPath
	}
	return out
}

func writeChatMessages(
	zw *zip.Writer,
	order []index.ManFile,
	absOf map[string]string,
	maxClasses, maxChars int,
) ([]chatMessageMeta, error) {
	metas := make([]chatMessageMeta, 0, (len(order)+maxClasses-1)/maxClasses)
	msgIdx := 0
	i := 0
	for i < len(order) {
		msgIdx++
		name := filepath.ToSlash(filepath.Join("chat", pad4(msgIdx)+".md"))
		h := &zip.FileHeader{Name: ziputil.SanitizePath(name), Method: zip.Deflate}
		h.SetMode(0o644)
		h.Modified = ziputil.FixedZipTime
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, fmt.Errorf("create %s: %w", name, err)
		}

		written := 0
		classes := 0
		meta := chatMessageMeta{Name: name}

		for classes < maxClasses && i < len(order) {
			mf := order[i]
			i++
			classes++
			meta.Files = append(meta.Files, mf.Path)

			var truncated bool
			written, truncated, err = writeChatEntry(w, mf, absOf, maxChars, written)
			if err != nil {
				return nil, err
			}
			if truncated {
				break
			}
		}

		metas = append(metas, meta)
	}
	return metas, nil
}

func writeChatEntry(
	w io.Writer,
	mf index.ManFile,
	absOf map[string]string,
	maxChars int,
	written int,
) (int, bool, error) {
	sec := buildHeader(mf)
	n, err := writeBounded(w, []byte(sec), maxChars-written)
	written += n
	if err != nil {
		return written, true, err
	}
	if written >= maxChars {
		return written, true, nil
	}

	lang := langFromExt(filepath.Ext(mf.Path))
	startFence := "```" + lang + "\n"
	n, err = writeBounded(w, []byte(startFence), maxChars-written)
	written += n
	if err != nil {
		return written, true, err
	}
	if written >= maxChars {
		return written, true, nil
	}

	if abs := absOf[mf.Path]; abs != "" {
		if err := writeFileBounded(w, abs, maxChars-written); err != nil {
			return written, true, err
		}
		if written < maxChars {
			written = maxChars - 1
		}
	}

	if written < maxChars {
		n, err = writeBounded(w, []byte("\n```\n\n"), maxChars-written)
		written += n
		if err != nil {
			return written, written >= maxChars, err
		}
	} else {
		_, _ = w.Write([]byte("\n```\n"))
	}
	return written, written >= maxChars, nil
}

func writeChatToc(zw *zip.Writer, metas []chatMessageMeta) error {
	var b strings.Builder
	b.WriteString("# CHAT TOC\n\n")
	b.WriteString("| Message | Files |\n|:--------|:------|\n")
	for _, meta := range metas {
		files := strings.Join(meta.Files, ", ")
		b.WriteString("| ")
		b.WriteString(meta.Name)
		b.WriteString(" | ")
		if files == "" {
			b.WriteString("-")
		} else {
			b.WriteString(files)
		}
		b.WriteString(" |\n")
	}
	text := textutil.EnsureTrailingLF(textutil.NormalizeUTF8LF([]byte(b.String())))
	if err := ziputil.WriteText(zw, "TOC.md", text); err != nil {
		return fmt.Errorf("write TOC.md: %w", err)
	}
	return nil
}

func writeChatReadme(
	zw *zip.Writer,
	man index.Manifest,
	syms index.Symbols,
	metas []chatMessageMeta,
	maxClasses, maxChars int,
) error {
	var b strings.Builder
	b.WriteString("# Chat Bundle\n\n")
	fmt.Fprintf(&b, "- Module: %s\n", strings.TrimSpace(man.Module))
	fmt.Fprintf(&b, "- Files indexed: %d\n", len(man.Files))
	fmt.Fprintf(&b, "- Symbols extracted: %d\n", len(syms.Symbols))
	fmt.Fprintf(&b, "- Messages: %d (up to %d files per message, %d chars each)\n\n", len(metas), maxClasses, maxChars)
	b.WriteString("Messages are sorted by heuristics (graph degree, exports, tests, path).\n")
	b.WriteString("Each message contains one or more files rendered inside fenced code blocks.\n")
	text := textutil.EnsureTrailingLF(textutil.NormalizeUTF8LF([]byte(b.String())))
	if err := ziputil.WriteText(zw, "README.md", text); err != nil {
		return fmt.Errorf("write README.md: %w", err)
	}
	return nil
}

func writeChatBench(zw *zip.Writer, benchPath string) error {
	if strings.TrimSpace(benchPath) == "" {
		return nil
	}
	data, err := os.ReadFile(benchPath)
	if err != nil {
		return fmt.Errorf("read bench.txt: %w", err)
	}
	if err := ziputil.WriteFile(zw, "bench.txt", data); err != nil {
		return fmt.Errorf("write bench.txt: %w", err)
	}
	return nil
}

// isTestPath reports whether a path belongs to a tests folder or ends with _test.go.
func isTestPath(p string) bool {
	pp := strings.ReplaceAll(p, "\\", "/")
	return strings.Contains(pp, "/test/") || strings.HasSuffix(pp, "_test.go")
}

func buildHeader(mf index.ManFile) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(mf.Path)
	b.WriteString("\n")
	if mf.Package != "" || mf.Class != "" {
		b.WriteString("- Package: ")
		if mf.Package != "" {
			b.WriteString(mf.Package)
		} else {
			b.WriteString("-")
		}
		b.WriteString("\n- Class: ")
		if mf.Class != "" {
			b.WriteString(mf.Class)
		} else {
			b.WriteString("-")
		}
		b.WriteString("\n")
	}
	if len(mf.Exports) > 0 {
		b.WriteString("- Exports: ")
		for i, e := range mf.Exports {
			if i > 0 {
				b.WriteString(", ")
			}
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
		return nil
	}
	defer f.Close()
	buf := make([]byte, 32*1024)
	left := remain
	for left > 0 {
		n := left
		if n > len(buf) {
			n = len(buf)
		}
		k, er := f.Read(buf[:n])
		if k > 0 {
			if _, ew := w.Write(buf[:k]); ew != nil {
				return ew
			}
			left -= k
		}
		if er != nil {
			break
		}
	}
	return nil
}

func pad4(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

func langFromExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".go":
		return "go"
	case ".java":
		return "java"
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return "ts"
	case ".kt":
		return "kotlin"
	case ".cs":
		return "csharp"
	case ".py":
		return "python"
	case ".md":
		return "markdown"
	default:
		return ""
	}
}
