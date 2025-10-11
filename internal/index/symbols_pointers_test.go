package index

import "testing"

func TestBuildPointerIndexAssignsSuffixes(t *testing.T) {
	symbols := []Symbol{
		{Symbol: "pkg.Type.Func", Path: "a.go", Start: 10, End: 12},
		{Symbol: "pkg.Type.Func", Path: "b.go", Start: 20, End: 22},
	}
	ptrs := buildPointerIndex(symbols)
	if len(ptrs) != 2 {
		t.Fatalf("expected 2 pointers, got %d", len(ptrs))
	}
	if ptrs[0].ID != "pkg-Type-Func" {
		t.Fatalf("unexpected first id: %s", ptrs[0].ID)
	}
	if ptrs[1].ID != "pkg-Type-Func-2" {
		t.Fatalf("expected suffixed id, got %s", ptrs[1].ID)
	}
}

func TestDedupPointersRemovesDuplicates(t *testing.T) {
	input := []Pointer{
		{ID: "x", Path: "a", Start: 1, End: 2},
		{ID: "x", Path: "a", Start: 1, End: 2},
		{ID: "y", Path: "b", Start: 3, End: 4},
	}
	out := dedupPointers(input)
	if len(out) != 2 {
		t.Fatalf("expected deduped pointers, got %d", len(out))
	}
}
