package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicClient talks to the Claude Messages API.
//
// Two deliberate choices worth recording:
//
//   - Adaptive thinking with display "summarized". The default is "omitted",
//     which in a chat interface reads as a long unexplained pause; a person
//     waiting on a benefits question deserves to see that something is happening.
//   - Strict tool schemas are NOT enabled. Our tools have genuinely optional
//     fields, and local validation (tools.Validate) returns a remediation message
//     the model can act on in one round trip - which strict mode's 400 would not.
type AnthropicClient struct {
	client anthropic.Client
	model  string
}

func NewAnthropic(apiKey string, opts ...option.RequestOption) *AnthropicClient {
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	return &AnthropicClient{client: anthropic.NewClient(opts...)}
}

func (c *AnthropicClient) Name() string { return "anthropic" }

func (c *AnthropicClient) Stream(ctx context.Context, req Request, sink func(Event)) (Response, error) {
	params, err := toParams(req)
	if err != nil {
		return Response{}, err
	}
	stream := c.client.Messages.NewStreaming(ctx, params)
	acc := anthropic.Message{}
	for stream.Next() {
		ev := stream.Current()
		if err := acc.Accumulate(ev); err != nil {
			return Response{}, fmt.Errorf("MODEL_STREAM_CORRUPT: could not assemble the response: %w", err)
		}
		if sink == nil {
			continue
		}
		switch v := ev.AsAny().(type) {
		case anthropic.ContentBlockDeltaEvent:
			switch d := v.Delta.AsAny().(type) {
			case anthropic.TextDelta:
				sink(Event{Kind: EventTextDelta, Text: d.Text})
			case anthropic.ThinkingDelta:
				sink(Event{Kind: EventThinkingDelta, Text: d.Thinking})
			}
		case anthropic.ContentBlockStartEvent:
			if tb, ok := v.ContentBlock.AsAny().(anthropic.ToolUseBlock); ok {
				sink(Event{Kind: EventToolUse, ToolName: tb.Name})
			}
		}
	}
	if err := stream.Err(); err != nil {
		return Response{}, translateError(err)
	}
	if sink != nil {
		sink(Event{Kind: EventDone})
	}
	return fromMessage(acc), nil
}

func toParams(req Request) (anthropic.MessageNewParams, error) {
	if req.Model == "" {
		return anthropic.MessageNewParams{}, errors.New("MODEL_UNSET: no model was selected for this request")
	}
	p := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: req.MaxTokens,
	}
	for _, sb := range req.System {
		blk := anthropic.TextBlockParam{Text: sb.Text}
		if sb.Cache {
			// A breakpoint after each stable layer. Layer 3 (this turn's
			// context) is never marked, so it never enters the cached prefix.
			blk.CacheControl = anthropic.NewCacheControlEphemeralParam()
		}
		p.System = append(p.System, blk)
	}
	if req.Effort != "" {
		p.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(req.Effort)}
	}
	if req.Thinking {
		adaptive := anthropic.ThinkingConfigAdaptiveParam{Display: anthropic.ThinkingConfigAdaptiveDisplaySummarized}
		p.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &adaptive}
	}
	for _, t := range req.Tools {
		props, _ := t.InputSchema["properties"]
		var required []string
		if r, ok := t.InputSchema["required"].([]string); ok {
			required = r
		} else if r, ok := t.InputSchema["required"].([]any); ok {
			for _, v := range r {
				if s, ok := v.(string); ok {
					required = append(required, s)
				}
			}
		}
		tool := anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties:  props,
				Required:    required,
				ExtraFields: map[string]any{"additionalProperties": false},
			},
		}
		p.Tools = append(p.Tools, anthropic.ToolUnionParam{OfTool: &tool})
	}
	for _, m := range req.Messages {
		blocks, err := toBlocks(m.Blocks)
		if err != nil {
			return anthropic.MessageNewParams{}, err
		}
		if len(blocks) == 0 {
			continue
		}
		switch m.Role {
		case RoleUser:
			p.Messages = append(p.Messages, anthropic.NewUserMessage(blocks...))
		case RoleAssistant:
			p.Messages = append(p.Messages, anthropic.NewAssistantMessage(blocks...))
		default:
			return anthropic.MessageNewParams{}, fmt.Errorf("MESSAGE_ROLE_INVALID: %q is not a valid role", m.Role)
		}
	}
	if len(p.Messages) == 0 {
		return anthropic.MessageNewParams{}, errors.New("MESSAGES_EMPTY: a request needs at least one message")
	}
	return p, nil
}

func toBlocks(in []Block) ([]anthropic.ContentBlockParamUnion, error) {
	var out []anthropic.ContentBlockParamUnion
	for _, b := range in {
		switch b.Kind {
		case KindText:
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			out = append(out, anthropic.NewTextBlock(b.Text))
		case KindThinking:
			// Echoed back unchanged, as the API requires when continuing on the
			// same model. Dropping them silently degrades multi-step reasoning.
			if b.Thinking == "" || b.ThinkingSignature == "" {
				continue
			}
			out = append(out, anthropic.NewThinkingBlock(b.ThinkingSignature, b.Thinking))
		case KindToolUse:
			out = append(out, anthropic.NewToolUseBlock(b.ToolUseID, json.RawMessage(b.ToolInput), b.ToolName))
		case KindToolResult:
			out = append(out, anthropic.NewToolResultBlock(b.ResultFor, b.Result, b.IsError))
		default:
			return nil, fmt.Errorf("BLOCK_KIND_UNKNOWN: %q", b.Kind)
		}
	}
	return out, nil
}

func fromMessage(m anthropic.Message) Response {
	resp := Response{
		StopReason: string(m.StopReason),
		Usage: Usage{
			InputTokens:      m.Usage.InputTokens,
			OutputTokens:     m.Usage.OutputTokens,
			CacheReadTokens:  m.Usage.CacheReadInputTokens,
			CacheWriteTokens: m.Usage.CacheCreationInputTokens,
		},
	}
	if m.StopReason == anthropic.StopReasonRefusal {
		resp.RefusalCategory = string(m.StopDetails.Category)
	}
	for _, block := range m.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			resp.Blocks = append(resp.Blocks, Block{Kind: KindText, Text: v.Text})
		case anthropic.ThinkingBlock:
			resp.Blocks = append(resp.Blocks, Block{
				Kind: KindThinking, Thinking: v.Thinking, ThinkingSignature: v.Signature,
			})
		case anthropic.ToolUseBlock:
			resp.Blocks = append(resp.Blocks, Block{
				Kind: KindToolUse, ToolUseID: v.ID, ToolName: v.Name,
				ToolInput: json.RawMessage(v.JSON.Input.Raw()),
			})
		}
	}
	return resp
}

// translateError turns transport failures into messages that say what to do.
// A person waiting on a benefits question should not be shown "429".
func translateError(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 401:
			return fmt.Errorf("MODEL_AUTH_FAILED: the API credential was rejected. "+
				"Set ANTHROPIC_API_KEY, or run `ant auth login`, then restart: %w", err)
		case 404:
			return fmt.Errorf("MODEL_NOT_FOUND: the configured model id was not recognised. "+
				"Check OBA_AGENT_MODEL against the current model list: %w", err)
		case 429:
			return fmt.Errorf("MODEL_RATE_LIMITED: the request was rate limited. "+
				"It will be retried; if this persists the answer will be delayed rather than wrong: %w", err)
		case 400:
			return fmt.Errorf("MODEL_REQUEST_INVALID: the request was rejected as malformed. "+
				"This is a bug in request assembly, not something the user did: %w", err)
		default:
			if apiErr.StatusCode >= 500 {
				return fmt.Errorf("MODEL_UNAVAILABLE: the model service returned %d. Retrying: %w", apiErr.StatusCode, err)
			}
			return fmt.Errorf("MODEL_ERROR: unexpected status %d: %w", apiErr.StatusCode, err)
		}
	}
	return fmt.Errorf("MODEL_CONNECTION_FAILED: could not reach the model service; check network access: %w", err)
}
