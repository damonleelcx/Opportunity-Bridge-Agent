package vision_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/vision"
)

// TestLiveRecordScreenshotReading reads the synthetic mind map through the path
// production uses - vision.Reader with the transcript prompt, JSON mode and the
// image-token check - parses the result, and writes it as a fixture.
//
// Run through `make vision-record MODEL=<id>`; skipped otherwise, because it
// calls the vendor. OBA_VISION_RECORD_OUT redirects the file, so a reading can be
// compared against the committed one without overwriting it.
func TestLiveRecordScreenshotReading(t *testing.T) {
	model := os.Getenv("OBA_VISION_RECORD_MODEL")
	if model == "" {
		t.Skip("set OBA_VISION_RECORD_MODEL (make vision-record MODEL=<id>)")
	}
	key := os.Getenv("QWEN_API_KEY")
	if key == "" {
		t.Fatal("QWEN_API_KEY is empty, so nothing can be recorded")
	}
	dir := filepath.Join("..", "leadgraph", "testdata", "screenshot")
	img, err := os.ReadFile(filepath.Join(dir, "mindmap.png"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := vision.Reader{LLM: llm.NewQwen(key, os.Getenv("OBA_QWEN_BASE_URL")), Model: model}.Read(
		context.Background(), "image/png", img, vision.Question{
			Text: leadgraph.TranscriptPrompt, JSON: true, MaxTokens: leadgraph.TranscriptMaxTokens,
		})
	if err != nil {
		t.Fatalf("%s: %v", model, err)
	}
	tr, err := leadgraph.ParseTranscript(got.Text)
	if err != nil {
		t.Fatalf("%s returned a reading the import cannot plan from: %v", model, err)
	}

	out := os.Getenv("OBA_VISION_RECORD_OUT")
	if out == "" {
		out = filepath.Join(dir, "reading-"+model+".json")
	}
	if err := os.WriteFile(out, []byte(got.Text), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %d topics; the picture added %d input tokens; %d output tokens; wrote %s",
		model, len(tr.Nodes), got.ImageTokens, got.Usage.OutputTokens, out)
}
