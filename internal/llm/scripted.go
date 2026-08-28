package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// ScriptedClient replays a fixed sequence of model turns.
//
// It exists so the agent loop, the guardrails, the approval gate and the
// verifiers can be tested and evaluated against exact inputs, including the
// cases that are expensive or impossible to provoke from a live model: a tool
// call with a malformed argument, an answer that states an eligibility verdict,
// an answer that invents a program id.
//
// It is a test double, not a product mode: the server refuses to start on this
// backend unless a script path is given, and the trace records the backend name
// on every run so a scripted run can never be mistaken for a real one.
type ScriptedClient struct {
	mu     sync.Mutex
	script Script
	idx    int
}

type Script struct {
	Turns []ScriptedTurn `json:"turns"`
}

type ScriptedTurn struct {
	// WhenContains, if set, asserts that the most recent user or tool-result
	// content contains this string. A mismatch fails loudly rather than
	// silently replaying the wrong turn.
	WhenContains string `json:"when_contains,omitempty"`
	Thinking     string `json:"thinking,omitempty"`
	Text         string `json:"text,omitempty"`
	ToolCalls    []struct {
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"tool_calls,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
}

func NewScripted(s Script) *ScriptedClient { return &ScriptedClient{script: s} }

func LoadScript(path string) (*ScriptedClient, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("SCRIPT_READ_FAILED: cannot read %s: %w", path, err)
	}
	var s Script
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("SCRIPT_PARSE_FAILED: %s is not a valid script file: %w", path, err)
	}
	if len(s.Turns) == 0 {
		return nil, fmt.Errorf("SCRIPT_EMPTY: %s declares no turns", path)
	}
	return NewScripted(s), nil
}

func (c *ScriptedClient) Name() string { return "scripted" }

// Reset rewinds the script, so one script can drive several independent runs.
func (c *ScriptedClient) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.idx = 0
}

func (c *ScriptedClient) Stream(ctx context.Context, req Request, sink func(Event)) (Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idx >= len(c.script.Turns) {
		return Response{}, fmt.Errorf("SCRIPT_EXHAUSTED: the agent asked for model turn %d but the script has %d. "+
			"Either the loop took an unexpected branch, or the script is short", c.idx+1, len(c.script.Turns))
	}
	t := c.script.Turns[c.idx]
	c.idx++

	if t.WhenContains != "" {
		last := lastInboundContent(req.Messages)
		if !strings.Contains(last, t.WhenContains) {
			return Response{}, fmt.Errorf("SCRIPT_MISMATCH: turn %d expected the latest input to contain %q, but it was: %s",
				c.idx, t.WhenContains, truncate(last, 300))
		}
	}

	resp := Response{StopReason: t.StopReason}
	if t.Thinking != "" {
		resp.Blocks = append(resp.Blocks, Block{Kind: KindThinking, Thinking: t.Thinking, ThinkingSignature: "scripted"})
		if sink != nil {
			sink(Event{Kind: EventThinkingDelta, Text: t.Thinking})
		}
	}
	if t.Text != "" {
		resp.Blocks = append(resp.Blocks, Text(t.Text))
		if sink != nil {
			sink(Event{Kind: EventTextDelta, Text: t.Text})
		}
	}
	for i, tc := range t.ToolCalls {
		id := fmt.Sprintf("scripted_%d_%d", c.idx, i)
		input := tc.Input
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		resp.Blocks = append(resp.Blocks, Block{
			Kind: KindToolUse, ToolUseID: id, ToolName: tc.Name, ToolInput: input,
		})
		if sink != nil {
			sink(Event{Kind: EventToolUse, ToolName: tc.Name})
		}
	}
	if resp.StopReason == "" {
		if len(t.ToolCalls) > 0 {
			resp.StopReason = "tool_use"
		} else {
			resp.StopReason = "end_turn"
		}
	}
	resp.Usage = Usage{InputTokens: 0, OutputTokens: int64(len(t.Text) / 4)}
	if sink != nil {
		sink(Event{Kind: EventDone})
	}
	return resp, nil
}

func lastInboundContent(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != RoleUser {
			continue
		}
		var b strings.Builder
		for _, blk := range msgs[i].Blocks {
			switch blk.Kind {
			case KindText:
				b.WriteString(blk.Text)
			case KindToolResult:
				b.WriteString(blk.Result)
			}
			b.WriteByte('\n')
		}
		return b.String()
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
