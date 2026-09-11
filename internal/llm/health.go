package llm

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
)

// Health watches every call made through one client and says whether the model
// can actually be reached.
//
// WHY THIS EXISTS
//
//	/api/health used to print status "ok" unconditionally. On 2026-09-11 the
//	token plan's quota ran out and every model call was refused with 429, while
//	health went on saying ok - and nobody was using the site at the time, so
//	there was not even a failed conversation to notice. A health signal that
//	cannot turn red is not a signal.
//	See docs/bugfix/2026-09-11-quota-429-retried-and-health-always-ok.md
//
// WHY REAL CALLS FIRST, AND A CHECK ONLY WHEN THE RECORD IS STALE
//
//	Recording real calls costs nothing. With no traffic that record goes stale,
//	and stale is exactly when an outage is invisible, so a Snapshot that finds
//	the newest result older than StaleAfter starts ONE small request in the
//	background. The k8s probes read health every few seconds, which keeps the
//	answer fresh without a timer of its own; and a check is started at most once
//	per StaleAfter however often health is read, so a public endpoint cannot be
//	used to spend tokens.
type Health struct {
	inner Client
	opts  HealthOptions

	mu          sync.Mutex
	lastOK      time.Time
	lastFailure time.Time
	lastCode    string
	failures    int  // consecutive, since the last success
	persistent  bool // the latest failure is one that waiting does not cure
	lastSource  string
	checking    bool
	lastCheck   time.Time
}

// HealthOptions configures a Health.
type HealthOptions struct {
	// Check is the small request sent when the record is stale. Nil means never
	// send one: only real calls are recorded.
	Check func(ctx context.Context, c Client) error
	// StaleAfter is how old the newest result may be before a check is sent, and
	// the least time between two checks. Default 10 minutes.
	StaleAfter time.Duration
	// CheckTimeout bounds one check. A check that runs out of time is a failure:
	// a model that hangs is not a healthy one. Default 30 seconds.
	CheckTimeout time.Duration
	// Now is the clock, for tests. Default time.Now.
	Now func() time.Time
}

// HealthState is what the model looks like from here.
type HealthState string

const (
	HealthOK       HealthState = "ok"
	HealthDegraded HealthState = "degraded"
	// HealthUnknown means nothing has answered yet: just started, or never
	// called with no check configured. Unknown is not reported as ok.
	HealthUnknown HealthState = "unknown"
)

// HealthSnapshot is the model's state as /api/health reports it. It carries
// codes and times only: vendor messages can name account details, and this is
// served to anybody.
type HealthSnapshot struct {
	State               HealthState `json:"state"`
	LastOKAt            *time.Time  `json:"last_ok_at,omitempty"`
	LastFailureAt       *time.Time  `json:"last_failure_at,omitempty"`
	LastErrorCode       string      `json:"last_error_code,omitempty"`
	ConsecutiveFailures int         `json:"consecutive_failures"`
	// LastSource is what produced the newest result: "call" or "check".
	LastSource   string `json:"last_source,omitempty"`
	CheckEnabled bool   `json:"check_enabled"`
}

// persistentFailures do not cure themselves: retrying, or waiting a minute,
// meets the same refusal. One of them is enough to call the model degraded.
// Everything else (a rate limit, a 5xx, a dropped connection) is degraded only
// after degradeAfter in a row, so one blip does not paint the service red.
var persistentFailures = map[string]bool{
	"MODEL_AUTH_FAILED":     true,
	"MODEL_BILLING":         true,
	"MODEL_QUOTA_EXHAUSTED": true,
	"MODEL_NOT_FOUND":       true,
}

const degradeAfter = 3

// NewHealth wraps inner. Use the returned value as the client, so that every
// call is recorded.
func NewHealth(inner Client, o HealthOptions) *Health {
	if o.StaleAfter <= 0 {
		o.StaleAfter = 10 * time.Minute
	}
	if o.CheckTimeout <= 0 {
		o.CheckTimeout = 30 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Health{inner: inner, opts: o}
}

func (h *Health) Name() string { return h.inner.Name() }

func (h *Health) Stream(ctx context.Context, req Request, sink func(Event)) (Response, error) {
	resp, err := h.inner.Stream(ctx, req, sink)
	h.record(err, "call")
	return resp, err
}

// record notes one outcome. A call ended by its own context (the person went
// away, the turn's wall clock ran out) says nothing about the model and is not
// counted.
func (h *Health) record(err error, source string) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.opts.Now()
	h.lastSource = source
	if err == nil {
		h.lastOK, h.failures, h.lastCode, h.persistent = now, 0, "", false
		return
	}
	code := errorCode(err)
	h.lastFailure, h.lastCode = now, code
	h.failures++
	h.persistent = persistentFailures[code]
}

// Snapshot reports the current state, and starts a check in the background when
// the record is stale. It never waits for that check.
func (h *Health) Snapshot() HealthSnapshot {
	h.mu.Lock()
	now := h.opts.Now()
	s := HealthSnapshot{
		State: h.stateLocked(), LastErrorCode: h.lastCode, ConsecutiveFailures: h.failures,
		LastSource: h.lastSource, CheckEnabled: h.opts.Check != nil,
	}
	if !h.lastOK.IsZero() {
		t := h.lastOK
		s.LastOKAt = &t
	}
	if !h.lastFailure.IsZero() {
		t := h.lastFailure
		s.LastFailureAt = &t
	}
	newest := h.lastOK
	if h.lastFailure.After(newest) {
		newest = h.lastFailure
	}
	start := h.opts.Check != nil && !h.checking &&
		now.Sub(newest) >= h.opts.StaleAfter && now.Sub(h.lastCheck) >= h.opts.StaleAfter
	if start {
		h.checking, h.lastCheck = true, now
	}
	h.mu.Unlock()

	if start {
		go h.runCheck()
	}
	return s
}

func (h *Health) runCheck() {
	defer func() {
		h.mu.Lock()
		h.checking = false
		h.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), h.opts.CheckTimeout)
	defer cancel()
	err := h.opts.Check(ctx, h.inner)
	if errors.Is(err, context.DeadlineExceeded) {
		// Not wrapped with %w: record() ignores a deadline, and for a check the
		// deadline IS the finding.
		err = fmt.Errorf("MODEL_CHECK_TIMED_OUT: the model did not answer a one-token check within %s: %v",
			h.opts.CheckTimeout, err)
	}
	h.record(err, "check")
}

func (h *Health) stateLocked() HealthState {
	switch {
	case h.failures > 0 && (h.persistent || h.failures >= degradeAfter):
		return HealthDegraded
	case !h.lastOK.IsZero():
		return HealthOK
	default:
		return HealthUnknown
	}
}

var errorCodePrefix = regexp.MustCompile(`^([A-Z][A-Z0-9_]+):`)

// errorCode reads the leading CODE: of an error. A retry wrapper's code is the
// outer one, but Health sits below the retry layer and sees each attempt's own.
func errorCode(err error) string {
	if m := errorCodePrefix.FindStringSubmatch(err.Error()); m != nil {
		return m[1]
	}
	return "MODEL_ERROR"
}

// ModelCheck is the one small request Health sends when its record is stale:
// one token, thinking off, no tools, against the model the agent itself uses -
// so a quota or a credential that would fail the agent fails this too.
func ModelCheck(model string) func(context.Context, Client) error {
	return func(ctx context.Context, c Client) error {
		_, err := c.Stream(ctx, Request{Model: model, Messages: []Message{UserText("ping")}, MaxTokens: 1}, nil)
		return err
	}
}
