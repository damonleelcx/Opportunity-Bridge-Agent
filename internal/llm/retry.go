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
		// Deltas from a failed attempt must not reach the interface, or the user
		// sees half an answer followed by a different whole one.
		var buffered []Event
		resp, err := r.Inner.Stream(ctx, req, func(e Event) { buffered = append(buffered, e) })
		if err == nil {
			if sink != nil {
				for _, e := range buffered {
					sink(e)
				}
			}
			return resp, nil
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
