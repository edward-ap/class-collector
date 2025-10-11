package bundle

import (
	"bytes"
	"strings"
	"testing"
)

func TestGenerateFullReadmeDeterminism(t *testing.T) {
	opts := ReadmeOptions{ModuleName: "MyModule", SupportedLangs: []string{"go", "java", "ts"}, DiffNoPrefix: true, ContextLines: 4, IncludeBenchNote: true}
	a := GenerateFullReadme(opts)
	b := GenerateFullReadme(opts)
	if !bytes.Equal(a, b) {
		t.Fatalf("full readme not deterministic")
	}
	if !strings.HasSuffix(string(a), "\n") {
		t.Fatalf("full readme must end with newline")
	}
	if strings.Contains(string(a), "\r") {
		t.Fatalf("full readme must not contain \r")
	}
	// Must contain key sections
	want := []string{"Bundle layout", "Anchors, slices, pointers", "Diff policy", "Conventions", "FAQ"}
	for _, w := range want {
		if !strings.Contains(string(a), w) {
			t.Fatalf("missing marker %q in full readme", w)
		}
	}
	// Languages must be rendered sorted
	opts2 := ReadmeOptions{SupportedLangs: []string{"z", "a"}}
	c := string(GenerateFullReadme(opts2))
	if !strings.Contains(c, "Supported languages: a, z") {
		t.Fatalf("languages not sorted: %s", c)
	}
}

func TestGenerateDeltaReadmeDeterminism(t *testing.T) {
	opts := ReadmeOptions{ModuleName: "MyModule", SupportedLangs: []string{"cpp", "go"}, DiffNoPrefix: true, ContextLines: 4, IncludeBenchNote: false}
	a := GenerateDeltaReadme(opts)
	b := GenerateDeltaReadme(opts)
	if !bytes.Equal(a, b) {
		t.Fatalf("delta readme not deterministic")
	}
	if !strings.HasSuffix(string(a), "\n") {
		t.Fatalf("delta readme must end with newline")
	}
	if strings.Contains(string(a), "\r") {
		t.Fatalf("delta readme must not contain \r")
	}
	// Key sections
	want := []string{"Layout", "Conventions", "Oversize diffs", "How to consume"}
	for _, w := range want {
		if !strings.Contains(string(a), w) {
			t.Fatalf("missing marker %q in delta readme", w)
		}
	}
	// Sorted languages
	opts2 := ReadmeOptions{SupportedLangs: []string{"z", "a"}}
	c := string(GenerateDeltaReadme(opts2))
	if !strings.Contains(c, "Supported languages: a, z") {
		t.Fatalf("languages not sorted: %s", c)
	}
}

func TestDeltaReadmeBenchAndLangs(t *testing.T) {
	opts := ReadmeOptions{SupportedLangs: []string{"ts", "cpp", "go"}, DiffNoPrefix: true, ContextLines: 4, IncludeBenchNote: true}
	out := string(GenerateDeltaReadme(opts))
	if !strings.Contains(out, "Benchmarks") || !strings.Contains(out, "bench.txt") {
		t.Fatalf("Benchmarks section with bench.txt mention not present when IncludeBenchNote=true. Output:\n%s", out)
	}
	// Languages must be sorted lexicographically and include cpp
	if !strings.Contains(out, "Supported languages: cpp, go, ts") {
		t.Fatalf("supported languages not sorted or missing cpp: %s", out)
	}
}
