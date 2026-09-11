package llm_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
)

// sequenceClient answers each call with the next error in its list.
type sequenceClient struct {
	mu    sync.Mutex
	errs  []error
	calls int
	reqs  []llm.Request
}

func (s *sequenceClient) Name() string { return "sequence" }

func (s *sequenceClient) Stream(ctx context.Context, req llm.Request, sink func(llm.Event)) (llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.calls < len(s.errs) {
		err = s.errs[s.calls]
	}
	s.calls++
	s.reqs = append(s.reqs, req)
	return llm.Response{}, err
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The state follows real calls: a success is ok, one blip is not an outage,
// three in a row are, and a failure that waiting cannot cure is at once.
func TestHealthFollowsRealCalls(t *testing.T) {
	rate := errors.New("MODEL_RATE_LIMITED: slow down")
	quota := errors.New("MODEL_QUOTA_EXHAUSTED: used up")
	seq := &sequenceClient{errs: []error{nil, rate, rate, rate, nil, quota, nil}}
	h := llm.NewHealth(seq, llm.HealthOptions{})

	if got := h.Snapshot().State; got != llm.HealthUnknown {
		t.Fatalf("before any call: %s, want unknown - nothing has answered, so it is not ok", got)
	}
	steps := []struct {
		want llm.HealthState
		why  string
	}{
		{llm.HealthOK, "a success"},
		{llm.HealthOK, "one rate limit after a success is a blip"},
		{llm.HealthOK, "two in a row are still a blip"},
		{llm.HealthDegraded, "three in a row"},
		{llm.HealthOK, "a success clears it"},
		{llm.HealthDegraded, "a used-up quota does not cure itself, so once is enough"},
		{llm.HealthOK, "a success clears that too"},
	}
	for i, step := range steps {
		_, _ = h.Stream(context.Background(), llm.Request{}, nil)
		s := h.Snapshot()
		if s.State != step.want {
			t.Errorf("after call %d (%s): %s, want %s", i+1, step.why, s.State, step.want)
		}
	}
	// And the codes, not the messages, are what it reports.
	_, _ = h.Stream(context.Background(), llm.Request{}, nil) // beyond the list: success
	if s := h.Snapshot(); s.LastErrorCode != "" || s.LastFailureAt == nil || s.LastOKAt == nil {
		t.Errorf("after recovery: %+v, want no current error code and both times kept", s)
	}
}

func TestHealthReportsTheFailureCode(t *testing.T) {
	seq := &sequenceClient{errs: []error{fmt.Errorf("MODEL_QUOTA_EXHAUSTED: the vendor said something personal")}}
	h := llm.NewHealth(seq, llm.HealthOptions{})
	_, _ = h.Stream(context.Background(), llm.Request{}, nil)
	s := h.Snapshot()
	if s.LastErrorCode != "MODEL_QUOTA_EXHAUSTED" || s.ConsecutiveFailures != 1 || s.LastSource != "call" {
		t.Errorf("snapshot = %+v, want code MODEL_QUOTA_EXHAUSTED, 1 failure, from a call", s)
	}
}

// A call ended by its own context says nothing about the model.
func TestHealthIgnoresCallsEndedByTheirOwnContext(t *testing.T) {
	seq := &sequenceClient{errs: []error{
		fmt.Errorf("MODEL_CONNECTION_FAILED: gone: %w", context.Canceled),
		fmt.Errorf("MODEL_CONNECTION_FAILED: slow: %w", context.DeadlineExceeded),
	}}
	h := llm.NewHealth(seq, llm.HealthOptions{})
	_, _ = h.Stream(context.Background(), llm.Request{}, nil)
	_, _ = h.Stream(context.Background(), llm.Request{}, nil)
	if s := h.Snapshot(); s.State != llm.HealthUnknown || s.ConsecutiveFailures != 0 {
		t.Errorf("snapshot = %+v, want unknown with no failures counted", s)
	}
}

// With no traffic the record goes stale, which is exactly when an outage is
// invisible. A stale record starts one check - and only one per window, however
// often health is read.
func TestHealthChecksOnlyWhenStaleAndAtMostOncePerWindow(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
	var checks int32
	release := make(chan struct{})
	h := llm.NewHealth(&sequenceClient{}, llm.HealthOptions{
		StaleAfter: 10 * time.Minute, Now: clock.Now,
		Check: func(ctx context.Context, c llm.Client) error {
			atomic.AddInt32(&checks, 1)
			<-release
			return nil
		},
	})

	// Nothing recorded yet: the first read starts a check, and does not wait for it.
	done := make(chan llm.HealthSnapshot)
	go func() { done <- h.Snapshot() }()
	select {
	case s := <-done:
		if s.State != llm.HealthUnknown || !s.CheckEnabled {
			t.Errorf("first read = %+v, want unknown with the check enabled", s)
		}
	case <-time.After(time.Second):
		t.Fatal("Snapshot waited for the check; a slow model would hang the health endpoint")
	}
	waitFor(t, "the first check to start", func() bool { return atomic.LoadInt32(&checks) == 1 })

	// A second read while the check is still out starts nothing.
	h.Snapshot()
	close(release)
	waitFor(t, "the check to be recorded", func() bool { return h.Snapshot().State == llm.HealthOK })
	if n := atomic.LoadInt32(&checks); n != 1 {
		t.Fatalf("%d checks, want 1", n)
	}
	if s := h.Snapshot(); s.LastSource != "check" {
		t.Errorf("last source = %q, want check", s.LastSource)
	}

	// Five minutes on, the record is fresh: no check.
	clock.Advance(5 * time.Minute)
	for i := 0; i < 20; i++ {
		h.Snapshot()
	}
	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt32(&checks); n != 1 {
		t.Errorf("a fresh record started a check (%d checks)", n)
	}

	// Past the window: one more, however many reads.
	clock.Advance(6 * time.Minute)
	for i := 0; i < 20; i++ {
		h.Snapshot()
	}
	waitFor(t, "the second check", func() bool { return atomic.LoadInt32(&checks) == 2 })
	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt32(&checks); n != 2 {
		t.Errorf("twenty reads past the window started %d checks, want exactly 1 more", n-1)
	}
}

func TestHealthWithoutACheckNeverCallsTheModel(t *testing.T) {
	seq := &sequenceClient{}
	h := llm.NewHealth(seq, llm.HealthOptions{})
	for i := 0; i < 5; i++ {
		if s := h.Snapshot(); s.State != llm.HealthUnknown || s.CheckEnabled {
			t.Errorf("snapshot = %+v, want unknown with no check", s)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if seq.calls != 0 {
		t.Errorf("health called the model %d times with no check configured", seq.calls)
	}
}

// A model that hangs is not healthy. A check that runs out of time is a failure,
// even though a real call ended by its own deadline is not counted.
func TestHealthCountsACheckThatTimesOut(t *testing.T) {
	h := llm.NewHealth(&sequenceClient{}, llm.HealthOptions{
		CheckTimeout: 20 * time.Millisecond,
		Check: func(ctx context.Context, c llm.Client) error {
			<-ctx.Done()
			return fmt.Errorf("MODEL_CONNECTION_FAILED: waited: %w", ctx.Err())
		},
	})
	h.Snapshot()
	waitFor(t, "the timed-out check to be recorded", func() bool { return h.Snapshot().ConsecutiveFailures == 1 })
	if s := h.Snapshot(); s.LastErrorCode != "MODEL_CHECK_TIMED_OUT" {
		t.Errorf("code = %q, want MODEL_CHECK_TIMED_OUT", s.LastErrorCode)
	}
}

// The check is as cheap as a request can be, and asks the model the agent uses.
func TestModelCheckIsOneTokenAgainstTheAgentModel(t *testing.T) {
	seq := &sequenceClient{}
	if err := llm.ModelCheck("qwen3.8-max")(context.Background(), seq); err != nil {
		t.Fatal(err)
	}
	if len(seq.reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(seq.reqs))
	}
	r := seq.reqs[0]
	if r.Model != "qwen3.8-max" || r.MaxTokens != 1 || r.Thinking || len(r.Tools) != 0 {
		t.Errorf("check request = model %q max_tokens %d thinking %v tools %d; want the agent model, 1 token, no thinking, no tools",
			r.Model, r.MaxTokens, r.Thinking, len(r.Tools))
	}
}

// The window holds even when a check leaves nothing on the record - one ended
// by its own cancellation, say, which is not counted. Without the window every
// read of a public endpoint would start another request.
func TestHealthDoesNotRepeatAnUnrecordedCheckWithinTheWindow(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
	var checks int32
	h := llm.NewHealth(&sequenceClient{}, llm.HealthOptions{
		StaleAfter: 10 * time.Minute, Now: clock.Now,
		Check: func(ctx context.Context, c llm.Client) error {
			atomic.AddInt32(&checks, 1)
			return context.Canceled
		},
	})
	for i := 0; i < 50; i++ {
		h.Snapshot()
		time.Sleep(time.Millisecond)
	}
	if n := atomic.LoadInt32(&checks); n != 1 {
		t.Errorf("50 reads inside one window started %d checks, want 1", n)
	}
}
