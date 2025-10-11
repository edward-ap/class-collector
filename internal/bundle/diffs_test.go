package bundle

import (
	"testing"

	"class-collector/internal/diff"
)

func TestDiffFileProducesUnifiedDiff(t *testing.T) {
	old := []byte("line1\nline2\n")
	new := []byte("line1\nline3\n")
	body, oversize := diffFile("sample.txt", diff.Options{Context: 3}, old, new)
	if oversize {
		t.Fatalf("unexpected oversize")
	}
	if len(body) == 0 || body[0] != '@' && body[0] != 'd' {
		t.Fatalf("unexpected diff body: %q", body)
	}
}

func TestSortAndPackageOrdersByName(t *testing.T) {
	patches := []generatedPatch{
		{name: "b.patch", body: "b"},
		{name: "a.patch", body: "a"},
	}
	out := sortAndPackage(patches)
	if out[0].name != "a.patch" || out[1].name != "b.patch" {
		t.Fatalf("patches not sorted: %#v", out)
	}
}
