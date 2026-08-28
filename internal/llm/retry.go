package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Retrying wraps a Client with bounded retries on transient failures.
//
// Only rate limits, upstream 5xx and connection failures are retried. A 400 or a
// 401 is a bug or a misconfiguration, and retrying it just makes the user wait
// longer for the same error - so those are returned immediately.
type Retrying struct {
	Inner   Client
	Max     int
	Backoff time.Duration
	// OnRetry is called before each retry so the trace records the attempt.
	OnRetry func(attempt int, err error)
}

func (r Retrying) Name() string { return r.Inner.Name() }

func (r Retrying) Stream(ctx context.Context, req Request, sink func(Event)) (Response, error) {
	backoff := r.Backoff
	if backoff <= 0 {
		backoff = 500 * time.Millisecond
	}
	var lastErr error
	for attempt := 0; attempt <= r.Max; attempt++ {
		if attempt > 0 {
			if r.OnRetry != nil {
				r.OnRetry(attempt, lastErr)
			}
			select {
			case <-ctx.Done():
				return Response{}, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}
		// Deltas go STRAIGHT THROUGH, and a failed attempt is taken back
		// afterwards with EventReset.
		//
		// This used to collect every event into a slice and replay it only once
		// the attempt had succeeded. The rule it was enforcing is right — deltas
		// from a failed attempt must not reach the interface, or the reader sees
		// half an answer followed by a different whole one — but the cure removed
		// streaming from the product entirely. Every call is wrapped in Retrying,
		// so nothing the model wrote ever reached the reader until the model had
		// stopped writing: measured on a real turn, all 451 text deltas arrived in
		// the same instant, 57 seconds in, together with the final event. What
		// looked like a slow model was a 57-second blank screen followed by a wall
		// of text.
		//
		// The withholding is now paid for only when it is actually needed. An
		// attempt that fails having already written something emits EventReset,
		// and the interface drops what it has before the next attempt starts —
		// which is the same guarantee, charged to the rare path instead of every
		// path. See docs/bugfix/2026-08-28-answers-never-streamed.md
		emitted := false
		resp, err := r.Inner.Stream(ctx, req, func(e Event) {
			if e.Kind == EventTextDelta || e.Kind == EventThinkingDelta {
				emitted = true
			}
			if sink != nil {
				sink(e)
			}
		})
		if err == nil {
			return resp, nil
		}
		// Whether or not another attempt follows: what this one wrote is not what
		// the reader should be left with. A non-retryable failure ends the turn
		// with an error, and half an answer above that error is worse than none.
		if emitted && sink != nil {
			sink(Event{Kind: EventReset})
		}
		lastErr = err
		if !retryable(err) {
			return Response{}, err
		}
	}
	return Response{}, fmt.Errorf("MODEL_RETRIES_EXHAUSTED: gave up after %d attempts: %w", r.Max+1, lastErr)
}

func retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := err.Error()
	for _, code := range []string{"MODEL_RATE_LIMITED", "MODEL_UNAVAILABLE", "MODEL_CONNECTION_FAILED", "MODEL_STREAM_CORRUPT"} {
		if strings.Contains(msg, code) {
			return true
		}
	}
	return false
}
