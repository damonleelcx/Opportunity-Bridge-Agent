package llm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
)

// These tests exercise the real SDK against a server that speaks the real wire
// format, rather than a hand-rolled fake of our own client. That is the
// difference between testing our request assembly and testing our idea of it:
// a wrong field name would pass a fake and fail here.

func sseServer(t *testing.T, events []string, capture *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			body, _ := io.ReadAll(r.Body)
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil {
				t.Errorf("request body was not JSON: %v", err)
			}
			*capture = m
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		for _, e := range events {
			fmt.Fprint(w, e)
			w.(http.Flusher).Flush()
		}
	}))
}

func ev(name string, payload any) string {
	b, _ := json.Marshal(payload)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", name, b)
}

func textStream() []string {
	return []string{
		ev("message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5",
			"content": []any{}, "stop_reason": nil,
			"usage": map[string]any{"input_tokens": 120, "output_tokens": 0, "cache_read_input_tokens": 100},
		}}),
		ev("content_block_start", map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""}}),
		ev("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "Call 12333"}}),
		ev("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": " and ask for job-002."}}),
		ev("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}),
		ev("message_delta", map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn"},
			"usage": map[string]any{"output_tokens": 24}}),
		ev("message_stop", map[string]any{"type": "message_stop"}),
	}
}

func TestStreamAssemblesTextAndUsage(t *testing.T) {
	srv := sseServer(t, textStream(), nil)
	defer srv.Close()
	c := llm.NewAnthropic("test-key", option.WithBaseURL(srv.URL))

	var streamed strings.Builder
	resp, err := c.Stream(context.Background(), llm.Request{
		Model: "claude-opus-5", MaxTokens: 1024,
		Messages: []llm.Message{llm.UserText("care work in Chengdu?")},
	}, func(e llm.Event) {
		if e.Kind == llm.EventTextDelta {
			streamed.WriteString(e.Text)
		}
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	const want = "Call 12333 and ask for job-002."
	if resp.TextContent() != want {
		t.Errorf("assembled %q, want %q", resp.TextContent(), want)
	}
	if streamed.String() != want {
		t.Errorf("streamed %q, want %q", streamed.String(), want)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop reason %q", resp.StopReason)
	}
	if resp.Usage.OutputTokens != 24 || resp.Usage.CacheReadTokens != 100 {
		t.Errorf("usage not carried through: %+v", resp.Usage)
	}
}

func TestRequestCarriesCacheBreakpointsEffortAndTools(t *testing.T) {
	// The layered system prompt is only worth its complexity if the breakpoints
	// actually reach the wire. This asserts they do.
	var got map[string]any
	srv := sseServer(t, textStream(), &got)
	defer srv.Close()
	c := llm.NewAnthropic("test-key", option.WithBaseURL(srv.URL))

	_, err := c.Stream(context.Background(), llm.Request{
		Model:     "claude-opus-5",
		MaxTokens: 2048,
		Effort:    "high",
		Thinking:  true,
		System: []llm.SystemBlock{
			{Text: "charter", Cache: true},
			{Text: "intent", Cache: true},
			{Text: "context (volatile)"},
		},
		Tools: []llm.ToolDef{{
			Name: "opportunity_search", Description: "search",
			InputSchema: map[string]any{
				"type": "object", "required": []string{"query"},
				"properties": map[string]any{"query": map[string]any{"type": "string"}},
			},
		}},
		Messages: []llm.Message{llm.UserText("hello")},
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	sys, _ := got["system"].([]any)
	if len(sys) != 3 {
		t.Fatalf("system layers on the wire: %d, want 3", len(sys))
	}
	cached := 0
	for i, raw := range sys {
		blk, _ := raw.(map[string]any)
		if _, ok := blk["cache_control"]; ok {
			cached++
			if i == 2 {
				t.Error("the volatile context layer must not be inside the cached prefix")
			}
		}
	}
	if cached != 2 {
		t.Errorf("%d cache breakpoints on the wire, want 2", cached)
	}

	oc, _ := got["output_config"].(map[string]any)
	if oc["effort"] != "high" {
		t.Errorf("effort not sent: %v", got["output_config"])
	}
	th, _ := got["thinking"].(map[string]any)
	if th["type"] != "adaptive" {
		t.Errorf("adaptive thinking not sent: %v", got["thinking"])
	}
	if th["display"] != "summarized" {
		t.Errorf("thinking display %v, want summarized so the interface can show progress", th["display"])
	}

	tools, _ := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools on the wire: %d", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	schema, _ := tool["input_schema"].(map[string]any)
	if schema["additionalProperties"] != false {
		t.Error("additionalProperties:false was not sent; the model would be free to invent fields")
	}
}

func TestErrorsAreTranslatedIntoSomethingActionable(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{401, "MODEL_AUTH_FAILED"},
		{404, "MODEL_NOT_FOUND"},
		{429, "MODEL_RATE_LIMITED"},
		{503, "MODEL_UNAVAILABLE"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			fmt.Fprint(w, `{"type":"error","error":{"type":"x","message":"y"}}`)
		}))
		c := llm.NewAnthropic("k", option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
		_, err := c.Stream(context.Background(), llm.Request{
			Model: "claude-opus-5", MaxTokens: 16,
			Messages: []llm.Message{llm.UserText("hi")},
		}, nil)
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("status %d produced %v, want a %s code", tc.status, err, tc.want)
		}
	}
}

// A failed attempt must not leave half an answer on screen followed by a
// different whole one.
//
// Written as the READER experiences it — accumulate text, clear on reset —
// rather than as "the sink never sees the failed attempt", because the mechanism
// changed and the guarantee did not. Deltas now stream through as they arrive
// and a failed attempt is taken back with EventReset. Withholding every delta
// until the attempt had succeeded is what removed streaming from the product:
// nothing the model wrote reached the reader until it had stopped writing.
// See docs/bugfix/2026-08-28-answers-never-streamed.md
func TestRetryingLeavesTheReaderOnlyTheSuccessfulAttempt(t *testing.T) {
	attempts := 0
	inner := &flaky{fail: 1, onAttempt: func() { attempts++ }}
	var onScreen string
	r := llm.Retrying{Inner: inner, Max: 2}
	resp, err := r.Stream(context.Background(), llm.Request{}, func(e llm.Event) {
		switch e.Kind {
		case llm.EventReset:
			onScreen = ""
		case llm.EventTextDelta:
			onScreen += e.Text
		}
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if onScreen != "good" {
		t.Errorf("the reader was left with %q, want only the successful attempt", onScreen)
	}
	if resp.TextContent() != "good" {
		t.Errorf("final %q", resp.TextContent())
	}
}

// Deltas must reach the sink WHILE the attempt is running.
//
// This is the fence for the defect itself. Measured on a real turn before the
// fix: all 451 text deltas arrived in the same instant, 57 seconds in, together
// with the final event — a blank screen followed by a wall of text, which reads
// as a broken product rather than a thinking one.
func TestRetryingStreamsDeltasAsTheyArrive(t *testing.T) {
	var seen []string
	sawFirstDuringAttempt := false
	inner := &scripted{run: func(sink func(llm.Event)) (llm.Response, error) {
		sink(llm.Event{Kind: llm.EventTextDelta, Text: "half "})
		// If the wrapper is buffering, the sink below has not run yet.
		sawFirstDuringAttempt = len(seen) == 1
		sink(llm.Event{Kind: llm.EventTextDelta, Text: "done"})
		return llm.Response{Blocks: []llm.Block{llm.Text("half done")}, StopReason: "end_turn"}, nil
	}}
	_, err := llm.Retrying{Inner: inner, Max: 2}.
		Stream(context.Background(), llm.Request{}, func(e llm.Event) {
			if e.Kind == llm.EventTextDelta {
				seen = append(seen, e.Text)
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if !sawFirstDuringAttempt {
		t.Fatal("no delta reached the sink until the attempt had finished; " +
			"the answer arrives as one block and the product does not stream")
	}
}

// A non-retryable failure ends the turn with an error, and half an answer above
// that error is worse than none. The take-back is not only for retries.
func TestPartialOutputIsTakenBackEvenWhenNoRetryFollows(t *testing.T) {
	inner := &flaky{fail: 5, err: "MODEL_REQUEST_INVALID: bad shape"}
	var onScreen string
	reset := false
	_, err := llm.Retrying{Inner: inner, Max: 3}.
		Stream(context.Background(), llm.Request{}, func(e llm.Event) {
			switch e.Kind {
			case llm.EventReset:
				reset = true
				onScreen = ""
			case llm.EventTextDelta:
				onScreen += e.Text
			}
		})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !reset {
		t.Error("no reset was sent, so the failed attempt's text stays on screen under the error")
	}
	if onScreen != "" {
		t.Errorf("the reader was left with %q above an error message", onScreen)
	}
}

// scripted is a client whose whole behaviour is one function, for tests that
// care about WHEN the sink is called rather than about failure handling.
type scripted struct {
	run func(sink func(llm.Event)) (llm.Response, error)
}

func (s *scripted) Name() string { return "scripted" }
func (s *scripted) Stream(_ context.Context, _ llm.Request, sink func(llm.Event)) (llm.Response, error) {
	return s.run(sink)
}

func TestNonRetryableErrorsFailFast(t *testing.T) {
	inner := &flaky{fail: 5, err: "MODEL_REQUEST_INVALID: bad shape"}
	attempts := 0
	inner.onAttempt = func() { attempts++ }
	_, err := llm.Retrying{Inner: inner, Max: 3}.Stream(context.Background(), llm.Request{}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if attempts != 1 {
		t.Errorf("a malformed request was retried %d times; retrying makes the user wait for the same error", attempts)
	}
}

type flaky struct {
	fail      int
	err       string
	onAttempt func()
}

func (f *flaky) Name() string { return "flaky" }

func (f *flaky) Stream(ctx context.Context, req llm.Request, sink func(llm.Event)) (llm.Response, error) {
	if f.onAttempt != nil {
		f.onAttempt()
	}
	if f.fail > 0 {
		f.fail--
		if sink != nil {
			sink(llm.Event{Kind: llm.EventTextDelta, Text: "partial-"})
		}
		msg := f.err
		if msg == "" {
			msg = "MODEL_UNAVAILABLE: upstream 503"
		}
		return llm.Response{}, fmt.Errorf("%s", msg)
	}
	if sink != nil {
		sink(llm.Event{Kind: llm.EventTextDelta, Text: "good"})
	}
	return llm.Response{Blocks: []llm.Block{llm.Text("good")}, StopReason: "end_turn"}, nil
}
