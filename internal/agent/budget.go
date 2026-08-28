// Package agent is the loop: observe -> reason -> act -> observe -> verify ->
// finish (step 9), under explicit stopping conditions (step 14).
package agent

import "time"

// Budget is the set of ceilings a single turn runs under.
//
// Every one of these has been the cause of a runaway agent somewhere: a loop
// that keeps calling the same tool, a model that keeps thinking, a chain that
// runs for minutes while somebody waits at a service window. They are checked
// before each step rather than after, so the turn stops with a truthful message
// instead of being cut off mid-sentence.
type Budget struct {
	MaxIterations   int
	MaxToolCalls    int
	MaxOutputTokens int64
	Deadline        time.Time

	iterations   int
	toolCalls    int
	outputTokens int64
	// perTool counts repeats of the same tool with the same arguments, which is
	// the specific shape a stuck loop takes.
	perTool map[string]int
	started time.Time
}

func NewBudget(maxIter, maxTools int, maxOutput int64, wall time.Duration) *Budget {
	now := time.Now()
	return &Budget{
		MaxIterations: maxIter, MaxToolCalls: maxTools, MaxOutputTokens: maxOutput,
		Deadline: now.Add(wall), perTool: map[string]int{}, started: now,
	}
}

// StopReason is why a turn ended. Reported to the user in plain words, and to
// the trace as a code.
type StopReason string

const (
	StopAnswered         StopReason = "answered"
	StopMaxIterations    StopReason = "max_iterations"
	StopMaxToolCalls     StopReason = "max_tool_calls"
	StopMaxOutputTokens  StopReason = "max_output_tokens"
	StopDeadline         StopReason = "deadline"
	StopRepeatedToolCall StopReason = "repeated_tool_call"
	StopAwaitingApproval StopReason = "awaiting_approval"
	StopRefused          StopReason = "model_refusal"
	StopFailed           StopReason = "failed"
)

// CheckStep is called before each model request.
func (b *Budget) CheckStep() (StopReason, bool) {
	if b.iterations >= b.MaxIterations {
		return StopMaxIterations, true
	}
	if b.outputTokens >= b.MaxOutputTokens {
		return StopMaxOutputTokens, true
	}
	if time.Now().After(b.Deadline) {
		return StopDeadline, true
	}
	return "", false
}

func (b *Budget) StepTaken()             { b.iterations++ }
func (b *Budget) AddOutput(n int64)      { b.outputTokens += n }
func (b *Budget) Iterations() int        { return b.iterations }
func (b *Budget) ToolCalls() int         { return b.toolCalls }
func (b *Budget) OutputTokens() int64    { return b.outputTokens }
func (b *Budget) Elapsed() time.Duration { return time.Since(b.started) }

// Allowance is the wall-clock budget this turn was given, for the stop message.
func (b *Budget) Allowance() time.Duration { return b.Deadline.Sub(b.started) }

// CheckTool is called before each tool call, with a hash of its arguments.
func (b *Budget) CheckTool(name, argsHash string) (StopReason, bool) {
	if b.toolCalls >= b.MaxToolCalls {
		return StopMaxToolCalls, true
	}
	key := name + ":" + argsHash
	if b.perTool[key] >= 2 {
		return StopRepeatedToolCall, true
	}
	return "", false
}

func (b *Budget) ToolTaken(name, argsHash string) {
	b.toolCalls++
	b.perTool[name+":"+argsHash]++
}
