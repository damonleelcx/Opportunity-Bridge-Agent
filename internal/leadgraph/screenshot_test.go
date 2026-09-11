package leadgraph_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

// The screenshot fixtures are a synthetic mind map with invented names, drawn to
// carry every quirk of the owner's real screenshot, and readings of it recorded
// from the live vendor. See testdata/screenshot/README.md.
const shotDir = "testdata/screenshot"

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(shotDir, name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return string(b)
}

// The recorded readings are what the model says to THIS prompt. If the prompt
// changes and they are not re-recorded, every planning test below keeps passing
// against answers the model no longer gives.
func TestTheRecordedReadingsCameFromThisPrompt(t *testing.T) {
	if got := readFixture(t, "prompt.txt"); got != leadgraph.TranscriptPrompt {
		t.Fatal("TranscriptPrompt no longer matches the prompt the recorded readings were produced with. " +
			"Re-record them (make vision-record MODEL=<id>) and update prompt.txt in the same change")
	}
}

// The picture is rendered from mindmap.html; the answer key is truth.json. A text
// in one and not the other means the picture and the key describe different maps.
func TestThePictureAndItsAnswerKeyDescribeTheSameMap(t *testing.T) {
	page := html.UnescapeString(readFixture(t, "mindmap.html"))
	var truth map[string]any
	if err := json.Unmarshal([]byte(readFixture(t, "truth.json")), &truth); err != nil {
		t.Fatal(err)
	}
	var walk func(n map[string]any) int
	walk = func(n map[string]any) int {
		text, _ := n["text"].(string)
		if segs, ok := n["segments"].([]any); ok {
			for _, s := range segs {
				text += s.(map[string]any)["text"].(string)
			}
			// Segments are drawn as separate spans, so check each one.
			for _, s := range segs {
				if seg := s.(map[string]any)["text"].(string); !strings.Contains(page, seg) {
					t.Errorf("truth.json has %q, which the picture does not draw", seg)
				}
			}
		} else if !strings.Contains(page, text) {
			t.Errorf("truth.json has %q, which the picture does not draw", text)
		}
		count := 1
		for _, c := range asList(n["children"]) {
			count += walk(c.(map[string]any))
		}
		return count
	}
	if n := walk(truth); n != 31 {
		t.Errorf("the answer key has %d topics, want the 31 the recorded readings were scored against", n)
	}
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func TestARecordedReadingParses(t *testing.T) {
	for file, topics := range map[string]int{
		"reading-qwen3.7-plus.json":        31,
		"reading-qwen3.8-flash-split.json": 32, // the highlighted stretch read as its own topic
	} {
		tr, err := leadgraph.ParseTranscript(readFixture(t, file))
		if err != nil {
			t.Errorf("%s: a real reading was refused: %v", file, err)
			continue
		}
		if len(tr.Nodes) != topics {
			t.Errorf("%s: %d topics, want %d", file, len(tr.Nodes), topics)
		}
	}
}

func TestAFencedReadingStillParses(t *testing.T) {
	body := "```json\n{\"nodes\":[{\"id\":1,\"parent\":null,\"text\":\"星河互联\",\"kind\":\"org\"}]}\n```"
	if _, err := leadgraph.ParseTranscript(body); err != nil {
		t.Errorf("a reading wrapped in a code fence was refused: %v", err)
	}
}

// In the owner's screenshot the root is scrolled off the left edge, so the
// companies are the top of what can be seen.
func TestSeveralTopLevelTopicsAreAllowed(t *testing.T) {
	body := `{"nodes":[
		{"id":1,"parent":null,"text":"星河互联","kind":"org"},
		{"id":2,"parent":null,"text":"远航科技","kind":"org"},
		{"id":3,"parent":2,"text":"","placeholder":true,"kind":"placeholder"}]}`
	if _, err := leadgraph.ParseTranscript(body); err != nil {
		t.Errorf("a reading whose root is off-screen was refused: %v", err)
	}
}

func TestAReadingThatDoesNotHangTogetherIsRefused(t *testing.T) {
	many := make([]string, leadgraph.MaxTranscriptNodes+1)
	for i := range many {
		many[i] = fmt.Sprintf(`{"id":%d,"parent":null,"text":"t","kind":"org"}`, i+1)
	}
	cases := map[string]struct{ body, says string }{
		"not json":        {`the picture shows three companies`, "not the JSON"},
		"no topics":       {`{"nodes":[]}`, "no topics"},
		"duplicate id":    {`{"nodes":[{"id":1,"parent":null,"text":"a"},{"id":1,"parent":null,"text":"b"}]}`, "appears twice"},
		"missing parent":  {`{"nodes":[{"id":1,"parent":null,"text":"a"},{"id":2,"parent":9,"text":"b"}]}`, "does not contain"},
		"own parent":      {`{"nodes":[{"id":1,"parent":null,"text":"a"},{"id":2,"parent":2,"text":"b"}]}`, "its own parent"},
		"loop":            {`{"nodes":[{"id":1,"parent":null,"text":"a"},{"id":2,"parent":3,"text":"b"},{"id":3,"parent":2,"text":"c"}]}`, "loop"},
		"all have parent": {`{"nodes":[{"id":1,"parent":2,"text":"a"},{"id":2,"parent":1,"text":"b"}]}`, "no top"},
		"blank text":      {`{"nodes":[{"id":1,"parent":null,"text":"  "}]}`, "no text"},
		"too many":        {`{"nodes":[` + strings.Join(many, ",") + `]}`, "at most"},
		"overlong topic":  {`{"nodes":[{"id":1,"parent":null,"text":"` + strings.Repeat("长", leadgraph.MaxTopicRunes+1) + `"}]}`, "characters long"},
	}
	for name, c := range cases {
		_, err := leadgraph.ParseTranscript(c.body)
		if !errors.Is(err, leadgraph.ErrTranscriptInvalid) {
			t.Errorf("%s: err = %v, want TRANSCRIPT_INVALID", name, err)
			continue
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: the refusal does not say why (%q): %v", name, c.says, err)
		}
		if !strings.Contains(err.Error(), "crop it") {
			t.Errorf("%s: the refusal does not say what to do: %v", name, err)
		}
	}
}
