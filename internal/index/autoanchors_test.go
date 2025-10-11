package index

import "testing"

func TestRankAndFilterAnchorsOrdersByStart(t *testing.T) {
	cands := []anchorCandidate{
		{anchor: Anchor{Name: "b", Start: 5, End: 6}, order: 1},
		{anchor: Anchor{Name: "a", Start: 2, End: 4}, order: 0},
		{anchor: Anchor{Name: "c", Start: 5, End: 5}, order: 2},
	}
	out, err := rankAndFilterAnchors(cands, AutoAnchorConfig{MaxPerFile: 0})
	if err != nil {
		t.Fatalf("rankAndFilterAnchors error: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 anchors, got %d", len(out))
	}
	if out[0].Name != "a" || out[1].Name != "c" || out[2].Name != "b" {
		t.Fatalf("unexpected ordering: %#v", out)
	}
}

func TestRankAndFilterAnchorsRespectsCap(t *testing.T) {
	cands := []anchorCandidate{
		{anchor: Anchor{Name: "a", Start: 1, End: 1}},
		{anchor: Anchor{Name: "b", Start: 2, End: 2}},
		{anchor: Anchor{Name: "c", Start: 3, End: 3}},
	}
	out, err := rankAndFilterAnchors(cands, AutoAnchorConfig{MaxPerFile: 2})
	if err != nil {
		t.Fatalf("rankAndFilterAnchors error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 anchors after cap, got %d", len(out))
	}
	if out[0].Name != "a" || out[1].Name != "b" {
		t.Fatalf("cap should keep first anchors, got %#v", out)
	}
}
