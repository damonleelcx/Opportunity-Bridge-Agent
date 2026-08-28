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

// DeepSeekClient talks to DeepSeek's OpenAI-compatible chat completions API.
//
// Why a second implementation rather than pointing the Anthropic client at
// DeepSeek's Anthropic-compatible endpoint (https://api.deepseek.com/anthropic,
// which does exist and does work): that endpoint is a translation layer, and its
// documented gaps are exactly the things this application relies on - cache
// control is not supported there at all, and only part of output_config is. This
// implementation instead uses DeepSeek's own first-class parameters, which map
// onto what we need without guessing:
//
//	our Request.Thinking + Effort  ->  thinking: {type, reasoning_effort}
//	streamed reasoning_content     ->  EventThinkingDelta
//	usage.prompt_cache_hit_tokens  ->  Usage.CacheReadTokens
//
// The Anthropic-compatible route is still a legitimate option for someone who
// wants it - see docs/12-deepseek.md - it just is not what this file does.
//
// Raw HTTP rather than an OpenAI SDK: the surface used here is one endpoint and
// one streaming format, and adding a client library for it would put a
// dependency between us and a wire format we have written down and tested
// against anyway.
type DeepSeekClient struct {
	http    *http.Client
	baseURL string
	apiKey  string
}

// DefaultDeepSeekBaseURL is DeepSeek's documented base. "/v1" also works and
// means nothing about the model version; either may be passed.
const DefaultDeepSeekBaseURL = "https://api.deepseek.com"

func NewDeepSeek(apiKey, baseURL string) *DeepSeekClient {
	if baseURL == "" {
		baseURL = DefaultDeepSeekBaseURL
	}
	return &DeepSeekClient{
		// No overall client timeout: a turn streams for as long as the agent's
		// own wall-clock budget allows, and that budget is the authority.
		http:    &http.Client{},
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
	}
}

func (c *DeepSeekClient) Name() string { return "deepseek" }

// ---------------------------------------------------------------- wire types

type dsMessage struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
	// ToolCalls is set on assistant turns being replayed.
	ToolCalls []dsToolCall `json:"tool_calls,omitempty"`
	// ToolCallID is set on role:"tool" messages.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type dsToolCall struct {
	Index    int    `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type dsTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type dsThinking struct {
	Type            string `json:"type"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type dsRequest struct {
	Model         string      `json:"model"`
	Messages      []dsMessage `json:"messages"`
	MaxTokens     int64       `json:"max_tokens,omitempty"`
	Stream        bool        `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	Thinking *dsThinking `json:"thinking,omitempty"`
	Tools    []dsTool    `json:"tools,omitempty"`
}

type dsChunk struct {
	Choices []struct {
		Delta struct {
			Content          string       `json:"content"`
			ReasoningContent string       `json:"reasoning_content"`
			ToolCalls        []dsToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens         int64 `json:"prompt_tokens"`
		CompletionTokens     int64 `json:"completion_tokens"`
		PromptCacheHitTokens int64 `json:"prompt_cache_hit_tokens"`
	} `json:"usage"`
	Error *dsError `json:"error"`
}

type dsError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// effortMap translates our five-level scale onto DeepSeek's three.
//
// A table rather than a heuristic: silently collapsing "max" to "high" would
// change what the deployment paid for without anybody being able to grep for
// where it happened.
var effortMap = map[string]string{
	"low":    "low",
	"medium": "low",
	"high":   "high",
	"xhigh":  "high",
	"max":    "max",
}

// ------------------------------------------------------------------- request

func (c *DeepSeekClient) buildRequest(req Request) (dsRequest, error) {
	if req.Model == "" {
		return dsRequest{}, fmt.Errorf("MODEL_UNSET: no model was selected for this request")
	}
	out := dsRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		Stream:    true,
	}
	out.StreamOptions = &struct {
		IncludeUsage bool `json:"include_usage"`
	}{IncludeUsage: true}

	// The three prompt layers become one system message, in order. DeepSeek has
	// no cache_control directive - its context caching is automatic and keyed on
	// the prefix - so the Cache flags are not sent. The LAYER ORDER still earns
	// its keep: stable text first, volatile context last, is exactly what an
	// automatic prefix cache rewards.
	var sys strings.Builder
	for i, sb := range req.System {
		if i > 0 {
			sys.WriteString("\n\n")
		}
		sys.WriteString(sb.Text)
	}
	if sys.Len() > 0 {
		out.Messages = append(out.Messages, dsMessage{Role: "system", Content: sys.String()})
	}

	if req.Thinking {
		t := &dsThinking{Type: "enabled"}
		if e, ok := effortMap[req.Effort]; ok {
			t.ReasoningEffort = e
		}
		out.Thinking = t
	} else {
		out.Thinking = &dsThinking{Type: "disabled"}
	}

	for _, t := range req.Tools {
		var dt dsTool
		dt.Type = "function"
		dt.Function.Name = t.Name
		dt.Function.Description = t.Description
		dt.Function.Parameters = t.InputSchema
		out.Tools = append(out.Tools, dt)
	}

	msgs, err := toDSMessages(req.Messages)
	if err != nil {
		return dsRequest{}, err
	}
	out.Messages = append(out.Messages, msgs...)
	if len(out.Messages) == 0 {
		return dsRequest{}, fmt.Errorf("MESSAGES_EMPTY: a request needs at least one message")
	}
	return out, nil
}

// toDSMessages flattens our block model onto the chat-completions message model.
//
// The shape difference that matters: we carry tool results as blocks inside one
// user message (which is how the Anthropic API wants them), while this API wants
// each result as its own message with role "tool". Getting this wrong does not
// error - the model simply stops seeing tool output - so it has its own test.
func toDSMessages(in []Message) ([]dsMessage, error) {
	var out []dsMessage
	for _, m := range in {
		switch m.Role {
		case RoleUser:
			var text strings.Builder
			var results []dsMessage
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
					results = append(results, dsMessage{
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
				out = append(out, dsMessage{Role: "user", Content: text.String()})
			}
		case RoleAssistant:
			msg := dsMessage{Role: "assistant"}
			var text strings.Builder
			for _, b := range m.Blocks {
				switch b.Kind {
				case KindText:
					text.WriteString(b.Text)
				case KindThinking:
					// Deliberately dropped. reasoning_content is an output field
					// here; replaying it as input is only defined for the beta
					// prefix-completion mode, which this application does not use.
				case KindToolUse:
					var tc dsToolCall
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

func (c *DeepSeekClient) Stream(ctx context.Context, req Request, sink func(Event)) (Response, error) {
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
		return Response{}, translateDeepSeekError(res)
	}
	return c.readStream(res.Body, sink)
}

func (c *DeepSeekClient) readStream(r io.Reader, sink func(Event)) (Response, error) {
	var (
		resp     Response
		text     strings.Builder
		thinking strings.Builder
		// Tool call arguments arrive in fragments across chunks, keyed by index.
		calls  = map[int]*dsToolCall{}
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
		var chunk dsChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Response{}, fmt.Errorf("MODEL_STREAM_CORRUPT: could not parse a stream chunk: %w", err)
		}
		if chunk.Error != nil {
			// An error can arrive mid-stream after a 200, which is why this is
			// checked here as well as on the status code.
			return Response{}, fmt.Errorf("MODEL_ERROR: %s (%s)", chunk.Error.Message, chunk.Error.Code)
		}
		if chunk.Usage != nil {
			resp.Usage.InputTokens = chunk.Usage.PromptTokens
			resp.Usage.OutputTokens = chunk.Usage.CompletionTokens
			resp.Usage.CacheReadTokens = chunk.Usage.PromptCacheHitTokens
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
					cur = &dsToolCall{Index: tc.Index}
					calls[tc.Index] = cur
					order = append(order, tc.Index)
				}
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
	resp.StopReason = mapFinishReason(finish)
	if resp.StopReason == "refusal" {
		resp.RefusalCategory = "content_filter"
	}
	if sink != nil {
		sink(Event{Kind: EventDone})
	}
	return resp, nil
}

// mapFinishReason translates onto the vocabulary the agent loop already speaks,
// so neither the loop nor the interface has to know which provider answered.
func mapFinishReason(in string) string {
	switch in {
	case "tool_calls":
		return "tool_use"
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "content_filter":
		return "refusal"
	case "insufficient_system_resource":
		return "overloaded"
	case "":
		return "end_turn"
	}
	return in
}

func translateDeepSeekError(res *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))
	var e struct {
		Error dsError `json:"error"`
	}
	_ = json.Unmarshal(b, &e)
	detail := strings.TrimSpace(e.Error.Message)
	if detail == "" {
		detail = strings.TrimSpace(string(b))
	}
	switch res.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("MODEL_AUTH_FAILED: DeepSeek rejected the credential. "+
			"Set DEEPSEEK_API_KEY to a key from platform.deepseek.com and restart: %s", detail)
	case http.StatusPaymentRequired:
		// Worth its own branch: this is the failure that otherwise reads as a
		// generic outage and costs somebody an hour.
		return fmt.Errorf("MODEL_BILLING: the DeepSeek account has insufficient balance. "+
			"Top it up at platform.deepseek.com; no retry will help: %s", detail)
	case http.StatusNotFound:
		return fmt.Errorf("MODEL_NOT_FOUND: DeepSeek does not recognise this model id. "+
			"Set OBA_AGENT_MODEL to deepseek-v4-pro or deepseek-v4-flash: %s", detail)
	case http.StatusUnprocessableEntity, http.StatusBadRequest:
		return fmt.Errorf("MODEL_REQUEST_INVALID: DeepSeek rejected the request as malformed. "+
			"This is a bug in request assembly, not something the user did: %s", detail)
	case http.StatusTooManyRequests:
		return fmt.Errorf("MODEL_RATE_LIMITED: DeepSeek rate limited the request. "+
			"It will be retried; if this persists the answer will be delayed rather than wrong: %s", detail)
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return fmt.Errorf("MODEL_UNAVAILABLE: DeepSeek returned %d. Retrying: %s", res.StatusCode, detail)
	}
	if res.StatusCode >= 500 {
		return fmt.Errorf("MODEL_UNAVAILABLE: DeepSeek returned %d. Retrying: %s", res.StatusCode, detail)
	}
	return fmt.Errorf("MODEL_ERROR: DeepSeek returned %d: %s", res.StatusCode, detail)
}
