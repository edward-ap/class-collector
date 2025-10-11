package index

import "testing"

func TestParseAnchorsFromFileFindsLineAndBlock(t *testing.T) {
	data := []byte(`// region FOO
line1
// endregion FOO
/* region: BAR */
code
/* endregion: BAR */`)
	anchors, err := parseAnchorsFromFile("test", data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(anchors) != 2 {
		t.Fatalf("expected 2 anchors, got %d", len(anchors))
	}
}

func TestMergeAnchorsDedupsExactMatches(t *testing.T) {
	src := []Anchor{
		{Name: "A", Start: 1, End: 2},
		{Name: "A", Start: 1, End: 2},
		{Name: "B", Start: 3, End: 4},
	}
	out := mergeAnchors(nil, src)
	if len(out) != 2 {
		t.Fatalf("expected deduped anchors, got %d", len(out))
	}
	if out[0].Name != "A" || out[1].Name != "B" {
		t.Fatalf("unexpected anchors: %#v", out)
	}
}
