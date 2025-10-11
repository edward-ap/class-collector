package bundle

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"class-collector/internal/graph"
	"class-collector/internal/index"
)

func TestWriteChatCreatesArtifacts(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "foo.ts")
	if err := os.WriteFile(src, []byte("export function bar() {}\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	out := filepath.Join(dir, "chat.zip")
	man := index.Manifest{
		Files: []index.ManFile{
			{Path: "foo.ts", Package: "pkg", Class: "Foo"},
		},
	}
	files := []struct{ RelPath, AbsPath string }{
		{RelPath: "foo.ts", AbsPath: src},
	}
	syms := index.Symbols{Symbols: []index.Symbol{{Symbol: "Foo.bar"}}}
	if err := WriteChat(out, man, files, syms, graph.Graph{}, 2, 1024, ""); err != nil {
		t.Fatalf("WriteChat error: %v", err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	seen := map[string]bool{}
	for _, f := range zr.File {
		seen[f.Name] = true
		if f.Name == "README.md" {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open README: %v", err)
			}
			body, _ := io.ReadAll(rc)
			_ = rc.Close()
			if !strings.HasSuffix(string(body), "\n") {
				t.Fatalf("README should end with newline")
			}
		}
	}
	want := []string{"chat/msg-0001.md", "TOC.md", "README.md"}
	for _, name := range want {
		if !seen[name] {
			t.Fatalf("missing zip entry %s", name)
		}
	}
}
