package vision_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/vision"
)

// These run the real Qwen client against a server speaking the vendor's wire
// format. The input-token counts it replays were measured on the token-plan host
// on 2026-09-11: a model that saw the probe image counted 33 without it and 99
// with it; a model that dropped it counted 27 both times.

type seenRequest struct {
	body map[string]any
	raw  string
}

// vendor answers like the live service: usage depends on whether the request
// carried an image, which is exactly the difference being checked.
func vendor(t *testing.T, withoutImage, withImage int, finish, answer string) (*llm.QwenClient, *[]seenRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []seenRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Errorf("request is not JSON: %v", err)
		}
		mu.Lock()
		seen = append(seen, seenRequest{body: body, raw: string(b)})
		mu.Unlock()
		tokens := withoutImage
		if strings.Contains(string(b), `"image_url"`) {
			tokens = withImage
		}
		w.Header().Set("Content-Type", "text/event-stream")
		content, _ := json.Marshal(answer)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s},\"finish_reason\":%q}]}\n\n", content, finish)
		fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":7}}\n\n", tokens)
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return llm.NewQwen("qw-test-key", srv.URL), &seen
}

var picture = []byte("\x89PNG\r\n\x1a\nnot really a png, but the bytes are what travel")

func question() vision.Question {
	return vision.Question{Text: "Transcribe this as json.", JSON: true, MaxTokens: 8000}
}

func TestAReadingFromAModelThatSawThePictureIsUsed(t *testing.T) {
	client, seen := vendor(t, 33, 99, "stop", `{"nodes":[]}`)
	got, err := vision.Reader{LLM: client, Model: "qwen3.7-plus"}.Read(context.Background(), "image/png", picture, question())
	if err != nil {
		t.Fatalf("a sighted reading was refused: %v", err)
	}
	if got.Text != `{"nodes":[]}` || got.ImageTokens != 66 {
		t.Errorf("reading = %q with %d image tokens, want the answer and 66", got.Text, got.ImageTokens)
	}
	if len(*seen) != 2 {
		t.Fatalf("made %d calls, want 2: the question alone, then with the picture", len(*seen))
	}

	// The baseline must be the same question with nothing that could change its
	// input count except the missing picture.
	base, withPic := (*seen)[0], (*seen)[1]
	if strings.Contains(base.raw, "image_url") {
		t.Error("the baseline call carried the picture, so the difference measures nothing")
	}
	if base.body["max_tokens"].(float64) != 1 {
		t.Errorf("baseline max_tokens = %v, want 1: it exists only to count input", base.body["max_tokens"])
	}
	for _, field := range []string{"response_format", "enable_thinking", "model"} {
		if fmt.Sprint(base.body[field]) != fmt.Sprint(withPic.body[field]) {
			t.Errorf("%s differs between the two calls (%v vs %v); only the picture may differ",
				field, base.body[field], withPic.body[field])
		}
	}

	// The picture travels as a data URL of exactly these bytes, before the text.
	msgs := withPic.body["messages"].([]any)
	parts := msgs[len(msgs)-1].(map[string]any)["content"].([]any)
	first := parts[0].(map[string]any)
	wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(picture)
	if first["type"] != "image_url" || first["image_url"].(map[string]any)["url"] != wantURL {
		t.Errorf("first part = %v, want the picture as a data URL", first)
	}
	if last := parts[len(parts)-1].(map[string]any); last["type"] != "text" || last["text"] != question().Text {
		t.Errorf("last part = %v, want the question", last)
	}
}

func TestAReadingFromAModelThatCouldNotSeeIsRefused(t *testing.T) {
	client, _ := vendor(t, 27, 27, "stop", `{"nodes":[{"id":1,"text":"invented"}]}`)
	_, err := vision.Reader{LLM: client, Model: "glm-5.2"}.Read(context.Background(), "image/png", picture, question())
	if !errors.Is(err, vision.ErrModelBlind) {
		t.Fatalf("a model that counted nothing for the picture was believed: %v", err)
	}
	for _, want := range []string{"glm-5.2", "0 input tokens", "OBA_VISION_MODEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q, so nobody can act on it: %v", want, err)
		}
	}
}

func TestAReadingCutOffByTheOutputCeilingIsRefused(t *testing.T) {
	client, _ := vendor(t, 33, 99, "length", `{"nodes":[{"id":1,"text":"half`)
	_, err := vision.Reader{LLM: client, Model: "qwen3.7-plus"}.Read(context.Background(), "image/png", picture, question())
	if !errors.Is(err, vision.ErrTruncated) {
		t.Fatalf("a truncated reading was used: %v", err)
	}
	if !strings.Contains(err.Error(), "Crop the screenshot") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
}

func TestAnUnsupportedPictureIsRefusedBeforeAnyCall(t *testing.T) {
	client, seen := vendor(t, 33, 99, "stop", "{}")
	_, err := vision.Reader{LLM: client, Model: "qwen3.7-plus"}.Read(context.Background(), "image/svg+xml", picture, question())
	if !errors.Is(err, vision.ErrUnsupportedImage) {
		t.Fatalf("an SVG was sent for reading: %v", err)
	}
	if len(*seen) != 0 {
		t.Errorf("made %d calls for a picture it refuses; the vendor must not see it", len(*seen))
	}
}

// TestLiveVisionProbe proves a model id reads images before it is added to
// llm.QwenVisionModels. Run through `make vision-probe MODEL=<id>`; skipped
// otherwise, because it calls the vendor and costs tokens.
//
// The colours are chosen so that a guess does not pass: "red, green, blue" is
// what a model that saw nothing says, and one blind model on the 2026-09-11
// probe said exactly that and was right by accident.
func TestLiveVisionProbe(t *testing.T) {
	model := os.Getenv("OBA_VISION_PROBE_MODEL")
	if model == "" {
		t.Skip("set OBA_VISION_PROBE_MODEL (make vision-probe MODEL=<id>)")
	}
	// Asked for, but unable to call the vendor: that is a failure, not a skip.
	// A skipped probe prints PASS, and a PASS is how an unproven id gets added.
	key := os.Getenv("QWEN_API_KEY")
	if key == "" {
		t.Fatal("QWEN_API_KEY is empty, so the probe cannot call the vendor and proves nothing")
	}
	swatches := []color.RGBA{{245, 140, 20, 255}, {10, 10, 10, 255}, {20, 210, 220, 255}, {220, 20, 200, 255}}
	img := image.NewRGBA(image.Rect(0, 0, 320, 80))
	for x := 0; x < 320; x++ {
		for y := 0; y < 80; y++ {
			img.Set(x, y, swatches[x/80])
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	t.Logf("probe image: 320x80 PNG, %d bytes, crc %08x", buf.Len(), crc32.ChecksumIEEE(buf.Bytes()))

	client := llm.NewQwen(key, os.Getenv("OBA_QWEN_BASE_URL"))
	got, err := vision.Reader{LLM: client, Model: model}.Read(context.Background(), "image/png", buf.Bytes(), vision.Question{
		Text:      "List the four colours in this image from left to right, in English, comma-separated. Nothing else.",
		MaxTokens: 40,
	})
	if err != nil {
		t.Fatalf("%s did not pass: %v", model, err)
	}
	t.Logf("%s answered %q; the picture added %d input tokens", model, got.Text, got.ImageTokens)
	answer := strings.ToLower(got.Text)
	last := -1
	for _, c := range []string{"orange", "black", "cyan", "magenta"} {
		i := strings.Index(answer, c)
		if i <= last {
			t.Fatalf("%s named the colours wrongly or out of order (%q); it is not proven to read images", model, got.Text)
		}
		last = i
	}
}
