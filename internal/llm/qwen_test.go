package llm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
)

// These tests run the real client against a server speaking Model Studio's
// OpenAI-compatible wire format. Only the destination is substituted; the code
// under test is what the binaries call.

// sse writes the given chunks as a text/event-stream, the way the live service
// does, terminated by [DONE].
func sse(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		io.WriteString(w, "data: "+c+"\n\n")
	}
	io.WriteString(w, "data: [DONE]\n\n")
}

func newQwenStub(t *testing.T, h http.HandlerFunc) *llm.QwenClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return llm.NewQwen("qw-test-key", srv.URL)
}

func basicReq() llm.Request {
	return llm.Request{
		Model:     llm.QwenAgentModel,
		System:    []llm.SystemBlock{{Text: "layer one", Cache: true}, {Text: "layer two"}},
		Messages:  []llm.Message{llm.UserText("hello")},
		MaxTokens: 2048,
	}
}

// capture runs one Stream and returns the decoded request body actually sent.
func capture(t *testing.T, req llm.Request) map[string]any {
	t.Helper()
	var got map[string]any
	c := newQwenStub(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &got); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		sse(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	})
	if _, err := c.Stream(context.Background(), req, nil); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	return got
}

func TestQwenStreamsTextThinkingAndUsage(t *testing.T) {
	c := newQwenStub(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"reasoning_content":"weighing "}}]}`,
			`{"choices":[{"delta":{"reasoning_content":"options"}}]}`,
			`{"choices":[{"delta":{"content":"Hello "}}]}`,
			`{"choices":[{"delta":{"content":"there"},"finish_reason":"stop"}]}`,
			// Usage arrives in a FINAL chunk with an EMPTY choices list.
			`{"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":8,`+
				`"prompt_tokens_details":{"cached_tokens":64}}}`)
	})

	var events []llm.Event
	resp, err := c.Stream(context.Background(), basicReq(), func(e llm.Event) {
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := resp.TextContent(); got != "Hello there" {
		t.Errorf("text = %q, want %q", got, "Hello there")
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", resp.StopReason)
	}
	if resp.Usage.InputTokens != 120 || resp.Usage.OutputTokens != 8 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	// The fence over a field that MOVED between providers: DeepSeek reported the
	// prefix cache as a flat prompt_cache_hit_tokens, Qwen nests it under
	// prompt_tokens_details.cached_tokens. Reading the old path decodes cleanly
	// to zero, so the mistake shows up as "caching never works here".
	if resp.Usage.CacheReadTokens != 64 {
		t.Errorf("CacheReadTokens = %d, want 64 (from usage.prompt_tokens_details.cached_tokens)",
			resp.Usage.CacheReadTokens)
	}
	var thinking string
	for _, b := range resp.Blocks {
		if b.Kind == llm.KindThinking {
			thinking = b.Thinking
		}
	}
	if thinking != "weighing options" {
		t.Errorf("thinking = %q", thinking)
	}
	var sawThinkingDelta, sawDone bool
	for _, e := range events {
		switch e.Kind {
		case llm.EventThinkingDelta:
			sawThinkingDelta = true
		case llm.EventDone:
			sawDone = true
		}
	}
	if !sawThinkingDelta || !sawDone {
		t.Errorf("stream events incomplete: thinking=%v done=%v", sawThinkingDelta, sawDone)
	}
}

// TestQwenThinkingIsAlwaysStatedExplicitly. The qwen3.8 line thinks BY DEFAULT
// and older ids do not, so an omitted enable_thinking means "whatever this model
// prefers" — a per-model spend and latency difference nobody can grep for.
func TestQwenThinkingIsAlwaysStatedExplicitly(t *testing.T) {
	req := basicReq()
	req.Thinking = false
	got := capture(t, req)
	v, present := got["enable_thinking"]
	if !present {
		t.Fatal("enable_thinking was OMITTED with Thinking=false; the qwen3.8 line thinks by " +
			"default, so omitting it silently buys reasoning tokens that were not asked for")
	}
	if v != false {
		t.Fatalf("enable_thinking = %v, want false", v)
	}
	if _, ok := got["thinking_budget"]; ok {
		t.Error("thinking_budget sent with thinking disabled")
	}
}

// TestQwenEffortBecomesAClampedTokenBudget. Qwen has no reasoning_effort enum;
// the only dial is a budget in tokens, and it is drawn from the SAME max_tokens
// allowance as the answer. An unclamped 16k budget under a 4k ceiling is a
// guaranteed truncation that still bills for the thinking.
func TestQwenEffortBecomesAClampedTokenBudget(t *testing.T) {
	req := basicReq()
	req.Thinking = true
	req.Effort = "xhigh" // table value 16384
	req.MaxTokens = 4000

	got := capture(t, req)
	if got["enable_thinking"] != true {
		t.Fatalf("enable_thinking = %v, want true", got["enable_thinking"])
	}
	budget, ok := got["thinking_budget"].(float64)
	if !ok {
		t.Fatalf("thinking_budget missing or not a number: %v", got["thinking_budget"])
	}
	if int(budget) != 2000 {
		t.Errorf("thinking_budget = %d, want 2000 (half of MaxTokens=4000); an unclamped 16384 "+
			"would leave nothing for the answer", int(budget))
	}
}

// TestQwenRequestShape covers the layering decisions that are otherwise invisible.
func TestQwenRequestShape(t *testing.T) {
	got := capture(t, basicReq())

	if got["stream"] != true {
		t.Error("stream must be true; this client is streaming-only")
	}
	opts, _ := got["stream_options"].(map[string]any)
	if opts == nil || opts["include_usage"] != true {
		t.Error("stream_options.include_usage must be set, or no usage is ever reported")
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("no messages sent")
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("first message role = %v, want system", first["role"])
	}
	// The three prompt layers become ONE system message, in order: Qwen has no
	// cache_control directive, and its cache is keyed on the prefix, so stable
	// text must come first.
	content, _ := first["content"].(string)
	if !strings.HasPrefix(content, "layer one") || !strings.Contains(content, "layer two") {
		t.Errorf("system layers not concatenated in order: %q", content)
	}
	if strings.Index(content, "layer one") > strings.Index(content, "layer two") {
		t.Error("layer order inverted; an automatic prefix cache rewards stable text first")
	}
}

// TestQwenToolResultsBecomeToolRoleMessages is the mapping that fails SILENTLY.
//
// We carry tool results as blocks inside one user message (the Anthropic shape);
// this API wants each result as its own message with role "tool". Getting it
// wrong does not error — the model simply stops seeing tool output.
func TestQwenToolResultsBecomeToolRoleMessages(t *testing.T) {
	req := basicReq()
	req.Messages = []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Kind: llm.KindToolUse, ToolUseID: "call_1", ToolName: "lookup",
			ToolInput: json.RawMessage(`{"q":"x"}`),
		}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{
			llm.ToolResult("call_1", "found it", false),
			llm.Text("and now answer"),
		}},
	}
	got := capture(t, req)
	msgs, _ := got["messages"].([]any)

	var roles []string
	var toolMsg map[string]any
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		roles = append(roles, mm["role"].(string))
		if mm["role"] == "tool" {
			toolMsg = mm
		}
	}
	if toolMsg == nil {
		t.Fatalf("no role:\"tool\" message was produced; roles were %v — the model would never "+
			"see the tool output", roles)
	}
	if toolMsg["tool_call_id"] != "call_1" {
		t.Errorf("tool_call_id = %v, want call_1", toolMsg["tool_call_id"])
	}
	// The result must sit immediately after the assistant turn that asked for it.
	if roles[0] != "system" || roles[1] != "assistant" || roles[2] != "tool" || roles[3] != "user" {
		t.Errorf("message order = %v, want system, assistant, tool, user", roles)
	}
}

// TestQwenToolCallIDSurvivesFragmentation is a live-observed hazard: the id
// arrives ONLY on the first fragment and every later fragment carries "". A
// tool_result whose tool_call_id is empty is silently dropped by the next
// request, so the loop would look like a model that ignores its own tool calls.
func TestQwenToolCallIDSurvivesFragmentation(t *testing.T) {
	c := newQwenStub(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function",`+
				`"function":{"name":"get_weather","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"","function":{"arguments":"{\"city\": "}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"","function":{"arguments":"\"Paris\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	})
	resp, err := c.Stream(context.Background(), basicReq(), nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	uses := resp.ToolUses()
	if len(uses) != 1 {
		t.Fatalf("got %d tool uses, want 1", len(uses))
	}
	if uses[0].ToolUseID != "call_abc" {
		t.Errorf("ToolUseID = %q, want call_abc — later fragments carry an empty id and must not "+
			"blank the one already seen", uses[0].ToolUseID)
	}
	if uses[0].ToolName != "get_weather" {
		t.Errorf("ToolName = %q", uses[0].ToolName)
	}
	if string(uses[0].ToolInput) != `{"city": "Paris"}` {
		t.Errorf("ToolInput = %s, want the reassembled fragments", uses[0].ToolInput)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want tool_use", resp.StopReason)
	}
}

// TestQwenMalformedToolArgsAreReportedNotDropped: a vanished tool call looks to
// the agent loop like the model choosing not to act.
func TestQwenMalformedToolArgsAreReportedNotDropped(t *testing.T) {
	c := newQwenStub(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":`+
				`{"name":"f","arguments":"{not json"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	})
	_, err := c.Stream(context.Background(), basicReq(), nil)
	if err == nil || !strings.Contains(err.Error(), "MODEL_STREAM_CORRUPT") {
		t.Fatalf("err = %v, want MODEL_STREAM_CORRUPT", err)
	}
}

// TestQwenMidStreamErrorAfter200 — the service can return 200 and then fail.
func TestQwenMidStreamErrorAfter200(t *testing.T) {
	c := newQwenStub(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"error":{"message":"quota exhausted","code":"insufficient_quota"}}`)
	})
	_, err := c.Stream(context.Background(), basicReq(), nil)
	if err == nil || !strings.Contains(err.Error(), "quota exhausted") {
		t.Fatalf("a mid-stream error after HTTP 200 was not surfaced: %v", err)
	}
}

// TestQwenErrorTranslation pins the codes the interface and retry layer read.
func TestQwenErrorTranslation(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "MODEL_AUTH_FAILED"},
		{http.StatusPaymentRequired, "MODEL_BILLING"},
		{http.StatusNotFound, "MODEL_NOT_FOUND"},
		{http.StatusBadRequest, "MODEL_REQUEST_INVALID"},
		{http.StatusTooManyRequests, "MODEL_RATE_LIMITED"},
		{http.StatusServiceUnavailable, "MODEL_UNAVAILABLE"},
	} {
		c := newQwenStub(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			io.WriteString(w, `{"error":{"message":"detail here"}}`)
		})
		_, err := c.Stream(context.Background(), basicReq(), nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("HTTP %d gave %v, want %s", tc.status, err, tc.want)
		}
	}
}

// TestQwenAuthErrorNamesTheRegionalTrap. A Beijing key on the Singapore host
// 401s exactly like a revoked one; without the hint the obvious next move is to
// reissue a key that already works.
func TestQwenAuthErrorNamesTheRegionalTrap(t *testing.T) {
	c := newQwenStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"invalid_api_key"}}`)
	})
	_, err := c.Stream(context.Background(), basicReq(), nil)
	if err == nil || !strings.Contains(err.Error(), "region") {
		t.Fatalf("auth error does not mention the regional trap: %v", err)
	}
}

// TestQwenFinishReasonMapping — the loop speaks one vocabulary regardless of who
// answered.
func TestQwenFinishReasonMapping(t *testing.T) {
	for wire, want := range map[string]string{
		"stop": "end_turn", "length": "max_tokens",
		"tool_calls": "tool_use", "content_filter": "refusal",
	} {
		c := newQwenStub(t, func(w http.ResponseWriter, r *http.Request) {
			sse(w, `{"choices":[{"delta":{"content":"x"},"finish_reason":"`+wire+`"}]}`)
		})
		resp, err := c.Stream(context.Background(), basicReq(), nil)
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		if resp.StopReason != want {
			t.Errorf("finish_reason %q mapped to %q, want %q", wire, resp.StopReason, want)
		}
		if want == "refusal" && resp.RefusalCategory == "" {
			t.Error("a refusal carried no category")
		}
	}
}

// TestQwenThinkingBlocksAreNotReplayed: reasoning_content is an OUTPUT field.
// Sending it back is undefined at best and rejected at worst.
func TestQwenThinkingBlocksAreNotReplayed(t *testing.T) {
	req := basicReq()
	req.Messages = []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Kind: llm.KindThinking, Thinking: "secret deliberation"},
			llm.Text("visible answer"),
		}},
		llm.UserText("go on"),
	}
	raw, _ := json.Marshal(capture(t, req))
	if strings.Contains(string(raw), "secret deliberation") {
		t.Error("a thinking block was replayed as input")
	}
	if !strings.Contains(string(raw), "visible answer") {
		t.Error("the assistant's text was dropped along with its thinking")
	}
}

func TestQwenDefaultBaseURLIsUsedWhenEmpty(t *testing.T) {
	if c := llm.NewQwen("k", ""); c.Name() != "qwen" {
		t.Errorf("Name() = %q", c.Name())
	}
	if llm.DefaultQwenBaseURL != "https://dashscope.aliyuncs.com/compatible-mode/v1" {
		t.Errorf("default base URL changed to %q; region is part of the credential",
			llm.DefaultQwenBaseURL)
	}
}
