// Package obs is the observability spine (step 17 of the build flow).
//
// Every decision the agent makes writes one event here: the routing call, the
// prompt that went out, each tool call with its arguments and outcome, every
// guardrail and verifier result, each retry, and the stop reason. A run's events
// are the record you read when somebody asks "why did it say that?".
//
// Events are also what the UI's trace panel renders, so the operator and the
// developer are looking at exactly the same thing.
package obs

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Name is the closed set of event names. Codes are central on purpose: a
// literal string at a call site is a name nobody can grep for later.
type Name string

const (
	RunStarted       Name = "agent.run.started"
	RunFinished      Name = "agent.run.finished"
	RunFailed        Name = "agent.run.failed"
	RouteResolved    Name = "agent.route.resolved"
	RouteRejected    Name = "agent.route.rejected"
	ModelRequested   Name = "agent.model.requested"
	ModelResponded   Name = "agent.model.responded"
	ModelRetried     Name = "agent.model.retried"
	ToolRequested    Name = "agent.tool.requested"
	ToolRejected     Name = "agent.tool.rejected"
	ToolSucceeded    Name = "agent.tool.succeeded"
	ToolFailed       Name = "agent.tool.failed"
	ApprovalRequired Name = "agent.approval.required"
	ApprovalGranted  Name = "agent.approval.granted"
	ApprovalDenied   Name = "agent.approval.denied"
	GuardrailTripped Name = "agent.guardrail.tripped"
	GuardrailPassed  Name = "agent.guardrail.passed"
	VerifierFailed   Name = "agent.verify.failed"
	VerifierPassed   Name = "agent.verify.passed"
	RetrievalQueried Name = "agent.retrieval.queried"
	BudgetExceeded   Name = "agent.budget.exceeded"
	StateWritten     Name = "agent.state.written"
	ConsentChecked   Name = "agent.consent.checked"
	EscalationRaised Name = "agent.escalation.raised"
)

// Level is coarse on purpose - the event name carries the detail.
type Level string

const (
	Info Level = "info"
	Warn Level = "warn"
	Err  Level = "error"
)

type Event struct {
	At       time.Time      `json:"at"`
	Level    Level          `json:"level"`
	Name     Name           `json:"name"`
	RunID    string         `json:"run_id"`
	Session  string         `json:"session_id,omitempty"`
	Intent   string         `json:"intent,omitempty"`
	Step     int            `json:"step,omitempty"`
	Duration time.Duration  `json:"duration_ms,omitempty"`
	Code     string         `json:"code,omitempty"` // UPPER_SNAKE error/reason code
	Message  string         `json:"message,omitempty"`
	Fields   map[string]any `json:"fields,omitempty"`
}

// Recorder collects a run's events and fans them out to a sink as they happen,
// so the UI can show the trace while the run is still going.
type Recorder struct {
	mu      sync.Mutex
	runID   string
	session string
	intent  string
	step    int
	events  []Event
	sinks   []func(Event)
	writer  io.Writer
}

func NewRecorder(runID, sessionID string) *Recorder {
	return &Recorder{runID: runID, session: sessionID}
}

// Subscribe adds a live sink. Sinks are called synchronously while the
// recorder's lock is held, so they must not block.
func (r *Recorder) Subscribe(fn func(Event)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sinks = append(r.sinks, fn)
}

// MirrorTo mirrors every event as one JSON object per line.
// (Named MirrorTo rather than WriteTo so it is not mistaken for io.WriterTo.)
func (r *Recorder) MirrorTo(w io.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writer = w
}

func (r *Recorder) SetIntent(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.intent = id
}

func (r *Recorder) SetStep(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.step = n
}

func (r *Recorder) Emit(level Level, name Name, code, msg string, fields map[string]any) Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev := Event{
		At:      time.Now().UTC(),
		Level:   level,
		Name:    name,
		RunID:   r.runID,
		Session: r.session,
		Intent:  r.intent,
		Step:    r.step,
		Code:    code,
		Message: msg,
		Fields:  fields,
	}
	r.events = append(r.events, ev)
	if r.writer != nil {
		if b, err := json.Marshal(ev); err == nil {
			fmt.Fprintf(r.writer, "%s\n", b)
		}
	}
	for _, s := range r.sinks {
		s(ev)
	}
	return ev
}

func (r *Recorder) Info(name Name, msg string, f map[string]any) Event {
	return r.Emit(Info, name, "", msg, f)
}

func (r *Recorder) Warn(name Name, code, msg string, f map[string]any) Event {
	return r.Emit(Warn, name, code, msg, f)
}

func (r *Recorder) Error(name Name, code, msg string, f map[string]any) Event {
	return r.Emit(Err, name, code, msg, f)
}

// Events returns a copy of everything recorded so far.
func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func (r *Recorder) RunID() string { return r.runID }

// Timer measures a span and emits the duration on the event it closes.
func (r *Recorder) Timer() func(Level, Name, string, string, map[string]any) Event {
	start := time.Now()
	return func(level Level, name Name, code, msg string, f map[string]any) Event {
		ev := r.Emit(level, name, code, msg, f)
		r.mu.Lock()
		defer r.mu.Unlock()
		d := time.Since(start)
		for i := range r.events {
			if r.events[i].At.Equal(ev.At) && r.events[i].Name == ev.Name {
				r.events[i].Duration = d
			}
		}
		ev.Duration = d
		return ev
	}
}
