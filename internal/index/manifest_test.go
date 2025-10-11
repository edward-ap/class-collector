package index

import (
	"testing"

	"class-collector/internal/graph"
)

func TestAssembleArtifactsSortingAndPointers(t *testing.T) {
	idx := symbolsIndex{
		manifest: []ManFile{
			{Path: "b.go", Hash: "bb", Lines: 2},
			{Path: "a.go", Hash: "aa", Lines: 1},
		},
		symbols: []Symbol{
			{Symbol: "pkg.Type.B", Path: "b.go", Start: 10, End: 12},
			{Symbol: "pkg.Type.A", Path: "a.go", Start: 5, End: 6},
		},
		slices: []Slice{
			{Path: "b.go", Start: 3, End: 4},
			{Path: "a.go", Start: 1, End: 2},
		},
		pointers: []Pointer{
			{ID: "ptr-2", Path: "b.go", Start: 8, End: 9},
			{ID: "ptr-1", Path: "a.go", Start: 1, End: 1},
		},
	}
	g := graph.Graph{Nodes: []string{"n2", "n1"}}

	art, err := assembleArtifacts("module", idx, g)
	if err != nil {
		t.Fatalf("assembleArtifacts error: %v", err)
	}

	if len(art.Manifest.Files) != 2 {
		t.Fatalf("manifest files size mismatch: %d", len(art.Manifest.Files))
	}
	if art.Manifest.Files[0].Path != "a.go" || art.Manifest.Files[1].Path != "b.go" {
		t.Fatalf("manifest not sorted: %#v", art.Manifest.Files)
	}
	if art.Manifest.BundleID == "" {
		t.Fatalf("bundle id should be computed")
	}

	if len(art.Symbols.Symbols) != 2 {
		t.Fatalf("symbols size mismatch: %d", len(art.Symbols.Symbols))
	}
	if art.Symbols.Symbols[0].Symbol != "pkg.Type.A" {
		t.Fatalf("symbols not sorted: %#v", art.Symbols.Symbols)
	}

	if len(art.Pointers) < len(idx.pointers) {
		t.Fatalf("expected symbol pointers appended, got %d", len(art.Pointers))
	}
	for i := 1; i < len(art.Pointers); i++ {
		if art.Pointers[i-1].ID > art.Pointers[i].ID {
			t.Fatalf("pointers not sorted: %#v", art.Pointers)
		}
	}

	if len(art.Slices) != 2 || art.Slices[0].Path != "a.go" {
		t.Fatalf("slices not sorted: %#v", art.Slices)
	}

	if len(art.Graph.Nodes) != len(g.Nodes) {
		t.Fatalf("graph not propagated")
	}
}
