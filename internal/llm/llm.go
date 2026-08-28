// Package llm is the model boundary (step 4 of the build flow).
//
// The agent loop talks to this interface, not to a vendor SDK. That buys two
// things worth the indirection: the loop can be exercised deterministically by a
// scripted client in tests and evals, and the request shape the loop builds -
// layered system prompt with cache breakpoints, tool definitions, tool results -
// is written down in one place instead of being spread through the loop.
package llm

import (
	"context"
	"encoding/json"
)

// Role is the author of a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// BlockKind enumerates the content block types this application uses.
type BlockKind string

const (
	KindText       BlockKind = "text"
	KindThinking   BlockKind = "thinking"
	KindToolUse    BlockKind = "tool_use"
	KindToolResult BlockKind = "tool_result"
)

// Block is one content block. Keeping a single struct rather than an interface
// keeps the round trip - response block back into the next request - a copy
// rather than a conversion, which is where replay bugs usually come from.
type Block struct {
	Kind BlockKind `json:"kind"`

	Text string `json:"text,omitempty"`

	// Thinking blocks must be echoed back unchanged on the same model.
	Thinking          string `json:"thinking,omitempty"`
	ThinkingSignature string `json:"thinking_signature,omitempty"`

	// tool_use
	ToolUseID string          `json:"tool_use_id,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`

	// tool_result
	ResultFor string `json:"result_for,omitempty"`
	Result    string `json:"result,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

func Text(s string) Block { return Block{Kind: KindText, Text: s} }

func ToolResult(forID, content string, isError bool) Block {
	return Block{Kind: KindToolResult, ResultFor: forID, Result: content, IsError: isError}
}

type Message struct {
	Role   Role    `json:"role"`
	Blocks []Block `json:"blocks"`
}

func UserText(s string) Message {
	return Message{Role: RoleUser, Blocks: []Block{Text(s)}}
}

// SystemBlock is one layer of the system prompt. Cache marks a cache breakpoint
// after this block; see package prompt for why the layers are ordered as they are.
type SystemBlock struct {
	Text  string `json:"text"`
	Cache bool   `json:"cache,omitempty"`
}

// ToolDef is a tool as the model sees it.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type Request struct {
	Model     string        `json:"model"`
	System    []SystemBlock `json:"system"`
	Messages  []Message     `json:"messages"`
	Tools     []ToolDef     `json:"tools,omitempty"`
	MaxTokens int64         `json:"max_tokens"`
	// Effort maps to output_config.effort: low | medium | high | xhigh | max.
	Effort string `json:"effort,omitempty"`
	// Thinking, when true, sends adaptive thinking with summarised display, so
	// the interface can show the model working rather than a long silence.
	Thinking bool `json:"thinking,omitempty"`
}

type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

type Response struct {
	Blocks     []Block `json:"blocks"`
	StopReason string  `json:"stop_reason"`
	// RefusalCategory is set only when StopReason is "refusal".
	RefusalCategory string `json:"refusal_category,omitempty"`
	Usage           Usage  `json:"usage"`
}

// Text concatenates every text block, which is what the user finally reads.
func (r Response) TextContent() string {
	var out string
	for _, b := range r.Blocks {
		if b.Kind == KindText {
			out += b.Text
		}
	}
	return out
}

// ToolUses returns the tool calls in this response, in order.
func (r Response) ToolUses() []Block {
	var out []Block
	for _, b := range r.Blocks {
		if b.Kind == KindToolUse {
			out = append(out, b)
		}
	}
	return out
}

// EventKind is what the interface can render while a turn is in flight.
type EventKind string

const (
	EventTextDelta     EventKind = "text_delta"
	EventThinkingDelta EventKind = "thinking_delta"
	EventToolUse       EventKind = "tool_use"
	EventDone          EventKind = "done"
)

type Event struct {
	Kind     EventKind `json:"kind"`
	Text     string    `json:"text,omitempty"`
	ToolName string    `json:"tool_name,omitempty"`
}

// Client is the model boundary. Stream is the only method: every call in this
// application streams, because a turn that thinks and calls tools can easily
// outrun a non-streaming HTTP timeout.
type Client interface {
	Stream(ctx context.Context, req Request, sink func(Event)) (Response, error)
	// Name identifies the backend in traces.
	Name() string
}
