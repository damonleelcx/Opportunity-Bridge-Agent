package llm_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
)

// A picture travels as an image_url part holding a data URL of its exact bytes,
// before the text that asks about it. internal/vision is the only sender.
func TestQwenImageBlockBecomesAnImageURLPart(t *testing.T) {
	pic := []byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3, 254, 255}
	req := basicReq()
	req.Messages = []llm.Message{{Role: llm.RoleUser, Blocks: []llm.Block{
		llm.Image("image/png", pic), llm.Text("what is in this picture"),
	}}}
	got := capture(t, req)

	msgs := got["messages"].([]any)
	user := msgs[len(msgs)-1].(map[string]any)
	parts, ok := user["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("user content = %#v, want a two-part array (picture, then text)", user["content"])
	}
	img := parts[0].(map[string]any)
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pic)
	if img["type"] != "image_url" {
		t.Fatalf("first part type = %v, want image_url", img["type"])
	}
	if url := img["image_url"].(map[string]any)["url"]; url != want {
		t.Errorf("picture sent as %v, want the data URL of its exact bytes", url)
	}
	if txt := parts[1].(map[string]any); txt["type"] != "text" || txt["text"] != "what is in this picture" {
		t.Errorf("second part = %v, want the question as text", txt)
	}
}

// Content is a plain string on every message that carries no picture. The
// parts array exists for pictures only; changing the shape of every other
// message would be a wire change nothing asked for.
func TestQwenMessagesWithoutAPictureKeepStringContent(t *testing.T) {
	got := capture(t, basicReq())
	for i, m := range got["messages"].([]any) {
		if _, isString := m.(map[string]any)["content"].(string); !isString {
			t.Errorf("message %d content = %#v, want a string", i, m.(map[string]any)["content"])
		}
	}
}

func TestQwenJSONModeAsksForAJSONObject(t *testing.T) {
	req := basicReq()
	req.JSON = true
	rf, _ := capture(t, req)["response_format"].(map[string]any)
	if rf == nil || rf["type"] != "json_object" {
		t.Errorf("response_format = %v, want {type: json_object}", rf)
	}
	if _, sent := capture(t, basicReq())["response_format"]; sent {
		t.Error("response_format was sent on a request that did not ask for JSON")
	}
}

func TestQwenAnImageBlockWithNoBytesIsRefused(t *testing.T) {
	req := basicReq()
	req.Messages = []llm.Message{{Role: llm.RoleUser, Blocks: []llm.Block{llm.Image("image/png", nil)}}}
	c := newQwenStub(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("an empty picture reached the vendor")
	})
	_, err := c.Stream(context.Background(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "IMAGE_BLOCK_EMPTY") {
		t.Errorf("err = %v, want IMAGE_BLOCK_EMPTY", err)
	}
}
