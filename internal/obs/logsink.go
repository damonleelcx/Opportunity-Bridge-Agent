package obs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"regexp"
	"sync"
)

// ── Where a run's events reach the process log ──────────────────────────────
//
// WHY THIS FILE EXISTS
//
//	Until 2026-09-11 a run's events had exactly one consumer in production: the
//	SSE trace stream to the browser tab that ran the turn. MirrorTo was never
//	called, and the Result's Events were dropped by the HTTP handler, so
//	`kubectl logs` held one "http request" line per turn and nothing about which
//	tools had run. A turn that ran a tool and kept no card was undiagnosable
//	from the server - and the recruiter audit events, which the comments above
//	CandidateSearched say an operator must be able to audit on their own,
//	reached nothing but a browser tab that is gone on reload.
//	See docs/bugfix/2026-09-11-agent-events-never-reached-the-logs.md
//
// WHAT GOES IN, AND WHAT NEVER DOES (拍板 2026-09-11)
//
//	Only the events in loggedEvents, and on each only the fields named there.
//	Event.Message is never logged: on several events it carries text written by
//	the person, the classifier or the model - a route rationale, a verifier's
//	quoted evidence, an upstream error body. Tool arguments are never logged;
//	args_hash is. candidate_ref is never logged; outreach_id joins to it in the
//	store. A table rather than branches, so adding an event is one row and a
//	review sees the whole logged surface at once.

// loggedEvents is the whole logged surface: event name -> the only field keys
// that may appear on its log line.
var loggedEvents = map[Name][]string{
	RunStarted:            {"role", "backend", "message_chars"},
	RunFinished:           {"stop_reason", "iterations", "tool_calls", "redrafted", "output_tokens", "elapsed_ms", "cards_kept", "category"},
	RunFailed:             {"stop_reason", "elapsed_ms", "cards_kept"},
	RouteRejected:         {"intent"},
	ToolRequested:         {"tool", "args_hash"},
	ToolRejected:          {"tool"},
	ToolSucceeded:         {"tool", "result_bytes"},
	ToolFailed:            {"tool"},
	BudgetExceeded:        {"iterations", "tool_calls", "tool"},
	ModelRetried:          {"attempt"},
	ApprovalRequired:      {"tool", "approval_id"},
	ApprovalGranted:       {"tool", "approval_id"},
	ApprovalDenied:        {"tool", "approval_id"},
	CandidateSearched:     {"pool_size", "matched", "returned"},
	OutreachRequested:     {"outreach_id"},
	OutreachDecided:       {"outreach_id", "status"},
	ExternalTalentScanned: {"at_least", "at_most", "leads", "truncated", "by_vendor"},
}

// codeShape is what a code must look like to be logged. Codes are UPPER_SNAKE by
// convention, but some are derived from an error string; anything that is not
// shaped like a code is text, and text does not go into the log.
var codeShape = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// LogSink returns a Recorder sink that writes the allowlisted events to log.
// ctx is the request's context, so a ContextHandler can add request_id to each
// line. A nil log yields a sink that does nothing.
func LogSink(ctx context.Context, log *slog.Logger) func(Event) {
	return func(ev Event) {
		keys, ok := loggedEvents[ev.Name]
		if !ok || log == nil {
			return
		}
		attrs := make([]slog.Attr, 0, 6+len(keys))
		attrs = append(attrs,
			slog.String("event.name", string(ev.Name)),
			slog.String("run_id", ev.RunID),
		)
		if ev.Session != "" {
			attrs = append(attrs, slog.String("session_id", ev.Session))
		}
		if ev.Intent != "" {
			attrs = append(attrs, slog.String("intent", ev.Intent))
		}
		if ev.Step > 0 {
			attrs = append(attrs, slog.Int("step", ev.Step))
		}
		if ev.Code != "" && codeShape.MatchString(ev.Code) {
			attrs = append(attrs, slog.String("error.code", ev.Code))
		}
		for _, k := range keys {
			if v, ok := ev.Fields[k]; ok {
				attrs = append(attrs, slog.Any(k, v))
			}
		}
		log.LogAttrs(ctx, slogLevel(ev.Level), string(ev.Name), attrs...)
	}
}

func slogLevel(l Level) slog.Level {
	switch l {
	case Warn:
		return slog.LevelWarn
	case Err:
		return slog.LevelError
	}
	return slog.LevelInfo
}

// ── Request correlation ─────────────────────────────────────────────────────

type requestKey struct{}

// Request is what one HTTP request carries for correlation.
//
// RunID is filled in later, by Agent.Run: the run id is minted inside the run,
// while the HTTP middleware writes its line after the handler has returned, so
// the id has to travel back up through something both of them hold.
type Request struct {
	ID    string
	mu    sync.Mutex
	runID string
}

// SetRunID records the run this request drove.
func (q *Request) SetRunID(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.runID = id
}

// RunID is the run this request drove, or "" if it drove none.
func (q *Request) RunID() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.runID
}

// WithRequest mints a request id and attaches a Request to ctx.
//
// Minted, never read from the client: an inbound X-Request-Id is a value anyone
// can choose, and it would be written into every log line of that request - a
// way to forge correlation or to smuggle text into the log. crypto/rand.Read is
// documented never to return an error, so there is no fallback id to fall back to.
func WithRequest(ctx context.Context) (context.Context, *Request) {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	q := &Request{ID: "req_" + hex.EncodeToString(b)}
	return context.WithValue(ctx, requestKey{}, q), q
}

// RequestFrom returns the Request ctx carries, or nil.
func RequestFrom(ctx context.Context) *Request {
	if ctx == nil {
		return nil
	}
	q, _ := ctx.Value(requestKey{}).(*Request)
	return q
}

// SetRunID records id on the Request ctx carries, if it carries one.
func SetRunID(ctx context.Context, id string) {
	if q := RequestFrom(ctx); q != nil {
		q.SetRunID(id)
	}
}

// ContextHandler adds request_id and run_id from ctx to every record logged
// with a context (InfoContext, WarnContext, LogAttrs(ctx, ...)). A key the
// record already has is not added twice.
type ContextHandler struct{ slog.Handler }

// NewContextHandler wraps h.
func NewContextHandler(h slog.Handler) ContextHandler { return ContextHandler{h} }

func (h ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if q := RequestFrom(ctx); q != nil {
		have := map[string]bool{}
		r.Attrs(func(a slog.Attr) bool { have[a.Key] = true; return true })
		if !have["request_id"] {
			r.AddAttrs(slog.String("request_id", q.ID))
		}
		if id := q.RunID(); id != "" && !have["run_id"] {
			r.AddAttrs(slog.String("run_id", id))
		}
	}
	return h.Handler.Handle(ctx, r)
}

func (h ContextHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return ContextHandler{h.Handler.WithAttrs(as)}
}

func (h ContextHandler) WithGroup(name string) slog.Handler {
	return ContextHandler{h.Handler.WithGroup(name)}
}
