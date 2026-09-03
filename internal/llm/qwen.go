package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// QwenClient talks to Alibaba Cloud Model Studio's (DashScope) OpenAI-compatible
// chat completions API, which serves the Qwen family. It is the only model
// backend this application ships with; `scripted` replays a fixture and is not a
// provider.
//
// Why the OpenAI-compatible route rather than DashScope's own native API: the
// native one takes a different envelope (`input`/`parameters` rather than
// `messages`), and its streaming mode defaults to sending cumulative snapshots
// rather than deltas, so every chunk would have to be diffed against the last to
// avoid rendering the answer several times over. The compatible endpoint is a
// first-class surface here, not a translation shim - it carries tool calling,
// `reasoning_content`, prefix cache accounting and the thinking controls this
// application needs. What it does NOT carry is an explicit cache_control
// directive, which is why the System layers are concatenated rather than marked
// (see buildRequest).
//
// Raw HTTP rather than an OpenAI SDK: the surface used here is one endpoint and
// one streaming format, and adding a client library for it would put a
// dependency between us and a wire format we have written down and tested
// against anyway.
type QwenClient struct {
	http    *http.Client
	baseURL string
	apiKey  string
}

// DefaultQwenBaseURL is Model Studio's OpenAI-compatible endpoint in the Beijing
// region.
//
// ‼️ There are TWO regional hosts and they do NOT share an account namespace:
// this one, and https://dashscope-intl.aliyuncs.com/compatible-mode/v1
// (Singapore). A key issued in one region is rejected by the other with a 401
// that is indistinguishable from a revoked key - verified against the live
// service on 2026-09-03, where a working Beijing key returned `invalid_api_key`
// on the -intl host. So the region is part of the credential, not a latency
// preference: OBA_QWEN_BASE_URL and QWEN_API_KEY have to move together.
const DefaultQwenBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"

// Model ids, and the single source of truth for them: config's backend table
// references these constants rather than repeating the strings, so a model id
// cannot drift between the table that validates it and the error that suggests
// it.
//
// ‼️ Transcribed from the live `GET /models` listing on 2026-09-03, not from
// memory. Model Studio serves ~250 ids and retires them on its own schedule; an
// id that is merely plausible ("qwen-max-latest", "qwen3-turbo") is not
// necessarily one that exists, and the 400 it earns does not say so.
const (
	// QwenAgentModel drives the agent loop: the strongest model, because this is
	// the one that reads a person's situation and decides which tools to call.
	QwenAgentModel = "qwen3.8-max"
	// QwenClassifierModel does routing and classification - short, structured,
	// high-volume calls where the strong model's price buys nothing. Roughly a
	// fourteenth of the agent model's rate on both input and output.
	QwenClassifierModel = "qwen3.8-flash"
)

// QwenKnownModels are the ids this build has been written against.
var QwenKnownModels = []string{QwenAgentModel, QwenClassifierModel, "qwen3.8-27b", "qwen3.7-max", "qwen3.7-plus", "qwen3.7-flash"}

func NewQwen(apiKey, baseURL string) *QwenClient {
	if baseURL == "" {
		baseURL = DefaultQwenBaseURL
	}
	return &QwenClient{
		// No overall client timeout: a turn streams for as long as the agent's
		// own wall-clock budget allows, and that budget is the authority.
		http:    &http.Client{},
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
	}
}

func (c *QwenClient) Name() string { return "qwen" }

// ---------------------------------------------------------------- wire types

type qwMessage struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
	// ToolCalls is set on assistant turns being replayed.
	ToolCalls []qwToolCall `json:"tool_calls,omitempty"`
	// ToolCallID is set on role:"tool" messages.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type qwToolCall struct {
	Index    int    `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type qwTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type qwRequest struct {
	Model         string      `json:"model"`
	Messages      []qwMessage `json:"messages"`
	MaxTokens     int64       `json:"max_tokens,omitempty"`
	Stream        bool        `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	// EnableThinking is Qwen's chain-of-thought toggle: a plain bool, not
	// DeepSeek's nested {"thinking":{"type":…}} object. A pointer so that "off"
	// is sent as an explicit false rather than omitted - see buildRequest.
	EnableThinking *bool `json:"enable_thinking,omitempty"`
	// ThinkingBudget caps chain-of-thought in TOKENS. Qwen has no
	// `reasoning_effort` enum, so the effort scale is translated into a token
	// count by thinkingBudget.
	ThinkingBudget *int64   `json:"thinking_budget,omitempty"`
	Tools          []qwTool `json:"tools,omitempty"`
}

type qwChunk struct {
	Choices []struct {
		Delta struct {
			Content          string       `json:"content"`
			ReasoningContent string       `json:"reasoning_content"`
			ToolCalls        []qwToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		// PromptTokensDetails carries the prefix-cache split.
		//
		// ‼️ Qwen reports cache hits HERE, nested, as `cached_tokens` - not as
		// DeepSeek's flat `prompt_cache_hit_tokens`. Reading the wrong path
		// decodes cleanly to zero rather than failing, so the mistake surfaces
		// as "caching never works on this deployment" rather than as an error.
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *qwError `json:"error"`
}

type qwError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// thinkingBudget translates our five-level effort scale onto Qwen's only dial:
// a chain-of-thought ceiling measured in tokens.
//
// A table rather than a formula, for the same reason the DeepSeek backend used
// one: when a bill or a truncation looks wrong, the number that caused it is on
// one line here instead of spread through an expression.
//
// The values are deliberately modest. Reasoning tokens are billed as OUTPUT and
// are drawn from the SAME max_tokens allowance as the answer, so a generous
// budget does not buy a better answer - it buys a truncated one at a higher
// price.
var qwenThinkingBudget = map[string]int64{
	"low":    1024,
	"medium": 2048,
	"high":   8192,
	"xhigh":  16384,
	"max":    32768,
}

// clampThinkingBudget keeps the chain of thought from consuming the answer it is
// meant to serve.
//
// Why this is not left to the caller: reasoning tokens come out of max_tokens,
// so a 16k budget under a 4k ceiling is not "high effort" - it is a guaranteed
// truncation that still bills for the thinking, and the transcript shows an
// answer cut off mid-sentence with no indication that thinking is what ate it.
// Half the ceiling, so at least half of what was paid for reaches the reader.
func clampThinkingBudget(budget, maxTokens int64) int64 {
	if maxTokens <= 0 {
		return budget
	}
	if half := maxTokens / 2; budget > half {
		return half
	}
	return budget
}

// ------------------------------------------------------------------- request

func (c *QwenClient) buildRequest(req Request) (qwRequest, error) {
	if req.Model == "" {
		return qwRequest{}, fmt.Errorf("MODEL_UNSET: no model was selected for this request")
	}
	out := qwRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		Stream:    true,
	}
	out.StreamOptions = &struct {
		IncludeUsage bool `json:"include_usage"`
	}{IncludeUsage: true}

	// The three prompt layers become one system message, in order. Qwen has no
	// cache_control directive - its context cache is automatic and keyed on the
	// prefix - so the Cache flags are not sent. The LAYER ORDER still earns its
	// keep: stable text first, volatile context last, is exactly what an
	// automatic prefix cache rewards.
	var sys strings.Builder
	for i, sb := range req.System {
		if i > 0 {
			sys.WriteString("\n\n")
		}
		sys.WriteString(sb.Text)
	}
	if sys.Len() > 0 {
		out.Messages = append(out.Messages, qwMessage{Role: "system", Content: sys.String()})
	}

	// ‼️ enable_thinking is sent EXPLICITLY in both directions rather than
	// omitted when off. The qwen3.8 line thinks BY DEFAULT and older ids do not,
	// so an omitted field means "whatever this particular model prefers" - which
	// is a per-model spend and latency difference that nobody can grep for.
	thinking := req.Thinking
	out.EnableThinking = &thinking
	if thinking {
		if b, ok := qwenThinkingBudget[req.Effort]; ok {
			if clamped := clampThinkingBudget(b, req.MaxTokens); clamped > 0 {
				out.ThinkingBudget = &clamped
			}
		}
	}

	for _, t := range req.Tools {
		var qt qwTool
		qt.Type = "function"
		qt.Function.Name = t.Name
		qt.Function.Description = t.Description
		qt.Function.Parameters = t.InputSchema
		out.Tools = append(out.Tools, qt)
	}

	msgs, err := toQwenMessages(req.Messages)
	if err != nil {
		return qwRequest{}, err
	}
	out.Messages = append(out.Messages, msgs...)
	if len(out.Messages) == 0 {
		return qwRequest{}, fmt.Errorf("MESSAGES_EMPTY: a request needs at least one message")
	}
	return out, nil
}

// toQwenMessages flattens our block model onto the chat-completions message model.
//
// The shape difference that matters: we carry tool results as blocks inside one
// user message (which is how the Anthropic API wants them), while this API wants
// each result as its own message with role "tool". Getting this wrong does not
// error - the model simply stops seeing tool output - so it has its own test.
func toQwenMessages(in []Message) ([]qwMessage, error) {
	var out []qwMessage
	for _, m := range in {
		switch m.Role {
		case RoleUser:
			var text strings.Builder
			var results []qwMessage
			for _, b := range m.Blocks {
				switch b.Kind {
				case KindText:
					if text.Len() > 0 {
						text.WriteString("\n")
					}
					text.WriteString(b.Text)
				case KindToolResult:
					content := b.Result
					if b.IsError {
						// There is no is_error flag here, so the error has to be
						// legible in the content or the model cannot tell a
						// failure from a result.
						content = "ERROR: " + content
					}
					results = append(results, qwMessage{
						Role: "tool", ToolCallID: b.ResultFor, Content: content,
					})
				default:
					return nil, fmt.Errorf("BLOCK_KIND_UNSUPPORTED: %q cannot appear in a user message", b.Kind)
				}
			}
			// Tool results first, then any accompanying text, so the results sit
			// immediately after the assistant turn that asked for them.
			out = append(out, results...)
			if text.Len() > 0 {
				out = append(out, qwMessage{Role: "user", Content: text.String()})
			}
		case RoleAssistant:
			msg := qwMessage{Role: "assistant"}
			var text strings.Builder
			for _, b := range m.Blocks {
				switch b.Kind {
				case KindText:
					text.WriteString(b.Text)
				case KindThinking:
					// Deliberately dropped. reasoning_content is an OUTPUT field
					// here; Qwen rejects a replayed assistant turn that carries
					// it, and the chain of thought has no defined meaning as
					// input on a later turn anyway.
				case KindToolUse:
					var tc qwToolCall
					tc.ID = b.ToolUseID
					tc.Type = "function"
					tc.Function.Name = b.ToolName
					tc.Function.Arguments = string(b.ToolInput)
					if tc.Function.Arguments == "" {
						tc.Function.Arguments = "{}"
					}
					msg.ToolCalls = append(msg.ToolCalls, tc)
				default:
					return nil, fmt.Errorf("BLOCK_KIND_UNSUPPORTED: %q cannot appear in an assistant message", b.Kind)
				}
			}
			msg.Content = text.String()
			if msg.Content == "" && len(msg.ToolCalls) == 0 {
				continue
			}
			out = append(out, msg)
		default:
			return nil, fmt.Errorf("MESSAGE_ROLE_INVALID: %q is not a valid role", m.Role)
		}
	}
	return out, nil
}

// -------------------------------------------------------------------- stream

func (c *QwenClient) Stream(ctx context.Context, req Request, sink func(Event)) (Response, error) {
	body, err := c.buildRequest(req)
	if err != nil {
		return Response{}, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("MODEL_REQUEST_INVALID: could not serialise the request: %w", err)
	}
	url := c.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Response{}, fmt.Errorf("MODEL_REQUEST_INVALID: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	res, err := c.http.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("MODEL_CONNECTION_FAILED: could not reach %s; check network access: %w", c.baseURL, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Response{}, translateQwenError(res)
	}
	return c.readStream(res.Body, sink)
}

func (c *QwenClient) readStream(r io.Reader, sink func(Event)) (Response, error) {
	var (
		resp     Response
		text     strings.Builder
		thinking strings.Builder
		// Tool call arguments arrive in fragments across chunks, keyed by index.
		calls  = map[int]*qwToolCall{}
		order  []int
		finish string
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk qwChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Response{}, fmt.Errorf("MODEL_STREAM_CORRUPT: could not parse a stream chunk: %w", err)
		}
		if chunk.Error != nil {
			// An error can arrive mid-stream after a 200, which is why this is
			// checked here as well as on the status code.
			return Response{}, fmt.Errorf("MODEL_ERROR: %s (%s)", chunk.Error.Message, chunk.Error.Code)
		}
		if chunk.Usage != nil {
			// Usage arrives in a FINAL chunk that carries an empty choices list,
			// which is why usage is read outside the choices loop below.
			resp.Usage.InputTokens = chunk.Usage.PromptTokens
			resp.Usage.OutputTokens = chunk.Usage.CompletionTokens
			resp.Usage.CacheReadTokens = chunk.Usage.PromptTokensDetails.CachedTokens
		}
		for _, ch := range chunk.Choices {
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
			if d := ch.Delta.ReasoningContent; d != "" {
				thinking.WriteString(d)
				if sink != nil {
					sink(Event{Kind: EventThinkingDelta, Text: d})
				}
			}
			if d := ch.Delta.Content; d != "" {
				text.WriteString(d)
				if sink != nil {
					sink(Event{Kind: EventTextDelta, Text: d})
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				cur, ok := calls[tc.Index]
				if !ok {
					cur = &qwToolCall{Index: tc.Index}
					calls[tc.Index] = cur
					order = append(order, tc.Index)
				}
				// ‼️ The id arrives ONLY on the first fragment of a call; every
				// later fragment carries an empty string. Assigning
				// unconditionally would blank it, and a tool_result whose
				// tool_call_id is "" is silently dropped by the next request.
				if tc.ID != "" {
					cur.ID = tc.ID
				}
				if tc.Function.Name != "" {
					cur.Function.Name += tc.Function.Name
					if sink != nil {
						sink(Event{Kind: EventToolUse, ToolName: cur.Function.Name})
					}
				}
				cur.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Response{}, fmt.Errorf("MODEL_STREAM_CORRUPT: the stream ended badly: %w", err)
	}

	if thinking.Len() > 0 {
		resp.Blocks = append(resp.Blocks, Block{Kind: KindThinking, Thinking: thinking.String()})
	}
	if text.Len() > 0 {
		resp.Blocks = append(resp.Blocks, Text(text.String()))
	}
	for _, idx := range order {
		tc := calls[idx]
		args := strings.TrimSpace(tc.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			// Reported as a tool call with unparseable input rather than dropped:
			// a silently vanished tool call looks to the loop like the model
			// choosing not to act.
			return Response{}, fmt.Errorf("MODEL_STREAM_CORRUPT: tool call %q arrived with arguments that are not valid JSON: %s",
				tc.Function.Name, truncate(args, 200))
		}
		resp.Blocks = append(resp.Blocks, Block{
			Kind: KindToolUse, ToolUseID: tc.ID, ToolName: tc.Function.Name,
			ToolInput: json.RawMessage(args),
		})
	}
	resp.StopReason = mapQwenFinishReason(finish)
	if resp.StopReason == "refusal" {
		resp.RefusalCategory = "content_filter"
	}
	if sink != nil {
		sink(Event{Kind: EventDone})
	}
	return resp, nil
}

// mapQwenFinishReason translates onto the vocabulary the agent loop already
// speaks, so neither the loop nor the interface has to know which provider
// answered.
func mapQwenFinishReason(in string) string {
	switch in {
	case "tool_calls":
		return "tool_use"
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "content_filter":
		return "refusal"
	case "":
		return "end_turn"
	}
	return in
}

func translateQwenError(res *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))
	var e struct {
		Error qwError `json:"error"`
	}
	_ = json.Unmarshal(b, &e)
	detail := strings.TrimSpace(e.Error.Message)
	if detail == "" {
		detail = strings.TrimSpace(string(b))
	}
	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		// ‼️ The region is named here rather than only in a comment, because
		// this is where somebody actually reads it. A Beijing key pointed at the
		// Singapore host fails EXACTLY like a revoked one, and without the hint
		// the obvious next move is to reissue a key that already works.
		return fmt.Errorf("MODEL_AUTH_FAILED: Qwen rejected the credential. Set QWEN_API_KEY to a key "+
			"from bailian.console.aliyun.com and restart. If the key is known-good, check that "+
			"OBA_QWEN_BASE_URL points at the SAME region the key was issued in - a Beijing key is "+
			"rejected by the Singapore host (dashscope-intl) with this exact error: %s", detail)
	case http.StatusPaymentRequired:
		// Worth its own branch: this is the failure that otherwise reads as a
		// generic outage and costs somebody an hour.
		return fmt.Errorf("MODEL_BILLING: the Alibaba Cloud account has no remaining balance or free "+
			"quota for this model. Top it up at bailian.console.aliyun.com; no retry will help: %s", detail)
	case http.StatusNotFound:
		return fmt.Errorf("MODEL_NOT_FOUND: Qwen does not recognise this model id. "+
			"Set OBA_AGENT_MODEL to %s or %s: %s", QwenAgentModel, QwenClassifierModel, detail)
	case http.StatusUnprocessableEntity, http.StatusBadRequest:
		return fmt.Errorf("MODEL_REQUEST_INVALID: Qwen rejected the request as malformed. "+
			"This is a bug in request assembly, not something the user did: %s", detail)
	case http.StatusTooManyRequests:
		return fmt.Errorf("MODEL_RATE_LIMITED: Qwen rate limited the request. "+
			"It will be retried; if this persists the answer will be delayed rather than wrong: %s", detail)
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return fmt.Errorf("MODEL_UNAVAILABLE: Qwen returned %d. Retrying: %s", res.StatusCode, detail)
	}
	if res.StatusCode >= 500 {
		return fmt.Errorf("MODEL_UNAVAILABLE: Qwen returned %d. Retrying: %s", res.StatusCode, detail)
	}
	return fmt.Errorf("MODEL_ERROR: Qwen returned %d: %s", res.StatusCode, detail)
}
