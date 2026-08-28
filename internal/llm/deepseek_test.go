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

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
)

// These tests run the real client against a server speaking DeepSeek's
// documented chat-completions wire format. The shape differences from the
// Anthropic path - one flattened system message, tool results as their own
// `role: "tool"` messages, tool arguments arriving in fragments - are exactly
// the things that fail silently rather than loudly, so each has its own
// assertion.

func dsEvent(v any) string {
	b, _ := json.Marshal(v)
	return fmt.Sprintf("data: %s\n\n", b)
}

func dsChunkText(content, reasoning string) map[string]any {
	delta := map[string]any{}
	if content != "" {
		delta["content"] = content
	}
	if reasoning != "" {
		delta["reasoning_content"] = reasoning
	}
	return map[string]any{
		"object":  "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": delta}},
	}
}

func dsServer(t *testing.T, events []string, capture *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("posted to %q, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer ds-test-key" {
			t.Errorf("Authorization header %q", got)
		}
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

func TestDeepSeekStreamsTextThinkingAndUsage(t *testing.T) {
	events := []string{
		dsEvent(dsChunkText("", "The factory closed, so retraining is the useful angle. ")),
		dsEvent(dsChunkText("", "Search training first.")),
		dsEvent(dsChunkText("trn-002 fits. ", "")),
		dsEvent(dsChunkText("Call 028-5551-0022.", "")),
		dsEvent(map[string]any{
			"object":  "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
			"usage": map[string]any{
				"prompt_tokens": 1200, "completion_tokens": 44,
				"prompt_cache_hit_tokens": 960, "prompt_cache_miss_tokens": 240,
			},
		}),
		"data: [DONE]\n\n",
	}
	srv := dsServer(t, events, nil)
	defer srv.Close()

	c := llm.NewDeepSeek("ds-test-key", srv.URL)
	var text, thinking strings.Builder
	resp, err := c.Stream(context.Background(), llm.Request{
		Model: "deepseek-v4-pro", MaxTokens: 2048, Thinking: true, Effort: "xhigh",
		Messages: []llm.Message{llm.UserText("what can I do?")},
	}, func(e llm.Event) {
		switch e.Kind {
		case llm.EventTextDelta:
			text.WriteString(e.Text)
		case llm.EventThinkingDelta:
			thinking.WriteString(e.Text)
		}
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	const wantText = "trn-002 fits. Call 028-5551-0022."
	if resp.TextContent() != wantText {
		t.Errorf("assembled %q, want %q", resp.TextContent(), wantText)
	}
	if text.String() != wantText {
		t.Errorf("streamed %q, want %q", text.String(), wantText)
	}
	if !strings.HasPrefix(thinking.String(), "The factory closed") {
		t.Errorf("reasoning_content did not reach the thinking channel: %q", thinking.String())
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop reason %q, want end_turn (mapped from finish_reason \"stop\")", resp.StopReason)
	}
	// prompt_cache_hit_tokens is the only visibility into whether the layered
	// prompt is earning anything on this provider, so it must survive.
	if resp.Usage.CacheReadTokens != 960 || resp.Usage.OutputTokens != 44 {
		t.Errorf("usage not carried through: %+v", resp.Usage)
	}
}

func TestDeepSeekRequestShape(t *testing.T) {
	var got map[string]any
	srv := dsServer(t, []string{
		dsEvent(dsChunkText("ok", "")),
		dsEvent(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}),
		"data: [DONE]\n\n",
	}, &got)
	defer srv.Close()

	c := llm.NewDeepSeek("ds-test-key", srv.URL)
	_, err := c.Stream(context.Background(), llm.Request{
		Model: "deepseek-v4-pro", MaxTokens: 4096, Thinking: true, Effort: "max",
		System: []llm.SystemBlock{
			{Text: "CHARTER", Cache: true},
			{Text: "INTENT", Cache: true},
			{Text: "CONTEXT"},
		},
		Tools: []llm.ToolDef{{
			Name: "opportunity_search", Description: "search listings",
			InputSchema: map[string]any{
				"type": "object", "required": []string{"query"},
				"properties":           map[string]any{"query": map[string]any{"type": "string"}},
				"additionalProperties": false,
			},
		}},
		Messages: []llm.Message{llm.UserText("hello")},
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	if got["stream"] != true {
		t.Error("every request must stream; a turn that thinks and calls tools outruns an HTTP timeout")
	}
	if so, _ := got["stream_options"].(map[string]any); so == nil || so["include_usage"] != true {
		t.Error("include_usage was not requested, so usage never arrives")
	}

	// The three prompt layers become ONE system message, in order. DeepSeek has
	// no cache_control, but its automatic prefix cache rewards the same ordering.
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages on the wire: %d, want system + user", len(msgs))
	}
	sys, _ := msgs[0].(map[string]any)
	if sys["role"] != "system" {
		t.Fatalf("first message role %v", sys["role"])
	}
	content, _ := sys["content"].(string)
	if !strings.HasPrefix(content, "CHARTER") || !strings.HasSuffix(content, "CONTEXT") {
		t.Errorf("system layers lost their order: %q", content)
	}
	if strings.Contains(fmt.Sprint(got), "cache_control") {
		t.Error("cache_control was sent to a provider that does not accept it")
	}

	th, _ := got["thinking"].(map[string]any)
	if th["type"] != "enabled" || th["reasoning_effort"] != "max" {
		t.Errorf("thinking not mapped: %v", got["thinking"])
	}

	tools, _ := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools on the wire: %d", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Errorf("tool is not in the nested function form: %v", tool)
	}
	fn, _ := tool["function"].(map[string]any)
	params, _ := fn["parameters"].(map[string]any)
	if fn["name"] != "opportunity_search" || params["additionalProperties"] != false {
		t.Errorf("tool schema mangled: %v", fn)
	}
}

func TestDeepSeekEffortMapping(t *testing.T) {
	// Five levels onto three. The mapping is a table so a deployment can see
	// what it actually bought.
	for _, tc := range []struct{ ours, theirs string }{
		{"low", "low"}, {"medium", "low"}, {"high", "high"}, {"xhigh", "high"}, {"max", "max"},
	} {
		var got map[string]any
		srv := dsServer(t, []string{
			dsEvent(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}),
			"data: [DONE]\n\n",
		}, &got)
		c := llm.NewDeepSeek("ds-test-key", srv.URL)
		_, err := c.Stream(context.Background(), llm.Request{
			Model: "deepseek-v4-pro", Thinking: true, Effort: tc.ours,
			Messages: []llm.Message{llm.UserText("hi")},
		}, nil)
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", tc.ours, err)
		}
		th, _ := got["thinking"].(map[string]any)
		if th["reasoning_effort"] != tc.theirs {
			t.Errorf("effort %q mapped to %v, want %q", tc.ours, th["reasoning_effort"], tc.theirs)
		}
	}
}

// Tool results live inside a user message in our block model and must become
// their own `role: "tool"` messages here. Getting this wrong does not error -
// the model simply stops seeing tool output - so it is asserted directly.
func TestDeepSeekToolRoundTripShape(t *testing.T) {
	var got map[string]any
	srv := dsServer(t, []string{
		dsEvent(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}),
		"data: [DONE]\n\n",
	}, &got)
	defer srv.Close()

	c := llm.NewDeepSeek("ds-test-key", srv.URL)
	_, err := c.Stream(context.Background(), llm.Request{
		Model: "deepseek-v4-pro",
		Messages: []llm.Message{
			llm.UserText("care work in Chengdu"),
			{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Kind: llm.KindThinking, Thinking: "must not be replayed"},
				llm.Text("Looking."),
				{Kind: llm.KindToolUse, ToolUseID: "call_1", ToolName: "opportunity_search",
					ToolInput: json.RawMessage(`{"query":"elderly care"}`)},
			}},
			{Role: llm.RoleUser, Blocks: []llm.Block{
				llm.ToolResult("call_1", `{"count":2}`, false),
				llm.ToolResult("call_2", "OPPORTUNITY_NOT_FOUND", true),
			}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	msgs, _ := got["messages"].([]any)
	var roles []string
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		roles = append(roles, fmt.Sprint(mm["role"]))
	}
	want := []string{"user", "assistant", "tool", "tool"}
	if strings.Join(roles, ",") != strings.Join(want, ",") {
		t.Fatalf("message roles %v, want %v", roles, want)
	}

	assistant, _ := msgs[1].(map[string]any)
	if _, leaked := assistant["reasoning_content"]; leaked {
		t.Error("reasoning_content was replayed as input; it is an output-only field here")
	}
	tcs, _ := assistant["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("assistant tool_calls: %d", len(tcs))
	}
	tc, _ := tcs[0].(map[string]any)
	if tc["id"] != "call_1" || tc["type"] != "function" {
		t.Errorf("tool call shape: %v", tc)
	}

	errResult, _ := msgs[3].(map[string]any)
	body, _ := errResult["content"].(string)
	// There is no is_error flag in this format, so a failure has to be legible
	// in the content or the model cannot tell it from a result.
	if !strings.HasPrefix(body, "ERROR:") {
		t.Errorf("a failed tool result did not announce itself: %q", body)
	}
}

func TestDeepSeekReassemblesFragmentedToolArguments(t *testing.T) {
	frag := func(idx int, id, name, args string) map[string]any {
		call := map[string]any{"index": idx, "function": map[string]any{}}
		if id != "" {
			call["id"] = id
		}
		fn := call["function"].(map[string]any)
		if name != "" {
			fn["name"] = name
		}
		if args != "" {
			fn["arguments"] = args
		}
		return map[string]any{"choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{"tool_calls": []any{call}},
		}}}
	}
	srv := dsServer(t, []string{
		dsEvent(frag(0, "call_a", "opportunity_search", `{"que`)),
		dsEvent(frag(0, "", "", `ry":"cnc","ci`)),
		dsEvent(frag(0, "", "", `ty":"Chengdu"}`)),
		dsEvent(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}}),
		"data: [DONE]\n\n",
	}, nil)
	defer srv.Close()

	resp, err := llm.NewDeepSeek("ds-test-key", srv.URL).Stream(context.Background(), llm.Request{
		Model: "deepseek-v4-pro", Messages: []llm.Message{llm.UserText("hi")},
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	uses := resp.ToolUses()
	if len(uses) != 1 {
		t.Fatalf("tool uses: %d", len(uses))
	}
	var args map[string]string
	if err := json.Unmarshal(uses[0].ToolInput, &args); err != nil {
		t.Fatalf("arguments did not reassemble into valid JSON: %v (%s)", err, uses[0].ToolInput)
	}
	if args["query"] != "cnc" || args["city"] != "Chengdu" {
		t.Errorf("reassembled arguments wrong: %v", args)
	}
	if uses[0].ToolUseID != "call_a" || resp.StopReason != "tool_use" {
		t.Errorf("id %q stop %q", uses[0].ToolUseID, resp.StopReason)
	}
}

func TestDeepSeekErrorsAreActionable(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{401, "MODEL_AUTH_FAILED"},
		{402, "MODEL_BILLING"},
		{404, "MODEL_NOT_FOUND"},
		{422, "MODEL_REQUEST_INVALID"},
		{429, "MODEL_RATE_LIMITED"},
		{503, "MODEL_UNAVAILABLE"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			fmt.Fprint(w, `{"error":{"message":"detail from provider","code":"x"}}`)
		}))
		_, err := llm.NewDeepSeek("k", srv.URL).Stream(context.Background(), llm.Request{
			Model: "deepseek-v4-pro", Messages: []llm.Message{llm.UserText("hi")},
		}, nil)
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("status %d produced %v, want %s", tc.status, err, tc.want)
		}
		if err != nil && !strings.Contains(err.Error(), "detail from provider") {
			t.Errorf("status %d dropped the provider's own message: %v", tc.status, err)
		}
	}
}

// A 402 is permanent. Retrying it just makes the person wait for the same
// answer, so the retry wrapper must not treat it as transient.
func TestDeepSeekBillingErrorIsNotRetried(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(402)
		fmt.Fprint(w, `{"error":{"message":"Insufficient Balance"}}`)
	}))
	defer srv.Close()
	_, err := llm.Retrying{Inner: llm.NewDeepSeek("k", srv.URL), Max: 3}.Stream(
		context.Background(), llm.Request{Model: "deepseek-v4-pro",
			Messages: []llm.Message{llm.UserText("hi")}}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if attempts != 1 {
		t.Errorf("a billing failure was retried %d times", attempts)
	}
}
