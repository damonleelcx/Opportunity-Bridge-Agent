package leadgraph_test

import (
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

// The agent reaches a screenshot's corrections through import_remap, so the
// include argument has to go all the way through the tool, not only through
// ImportOverrides. Arguments arrive as JSON numbers, which is what these pass.

func TestIncludingAHeldPersonThroughTheTool(t *testing.T) {
	s := newStore(t)
	stageShot(t, s, goodReading)
	chen := rowOf(t, goodReading, "陈望舒")
	out, err := call(t, s, amy, "import_remap", map[string]any{"include": []any{float64(chen)}})
	if err != nil {
		t.Fatalf("import_remap include: %v", err)
	}
	sum, ok := out.(leadgraph.ImportSummary)
	if !ok {
		t.Fatalf("import_remap returned %T, want ImportSummary", out)
	}
	found := false
	for _, p := range sum.People {
		found = found || p.Label == "陈望舒"
	}
	if !found || len(sum.Held) != 1 || sum.Links != 11 {
		t.Errorf("after include through the tool: 陈望舒 planned=%v held=%d links=%d, want true, 1, 11",
			found, len(sum.Held), sum.Links)
	}
}

// A screenshot's topics are numbered from 1. A schema that still started at a
// file's line 2 would refuse a correction to the first topic.
func TestTheFirstTopicCanBeNamedInACorrection(t *testing.T) {
	s := newStore(t)
	stageShot(t, s, goodReading)
	for _, args := range []map[string]any{
		{"rows": []any{map[string]any{"row": float64(1), "field": "label", "value": "设计人才地图"}}},
		{"ignore": []any{float64(1)}},
		{"include": []any{float64(1)}},
	} {
		if _, err := call(t, s, amy, "import_remap", args); err != nil {
			t.Errorf("import_remap %v refused topic 1: %v", args, err)
		}
	}
}
