package llm_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
)

// tokenPlanQuotaBody is the refusal production received on 2026-09-11, message
// verbatim. It carries no error code the vendor documents, which is why the
// quota wording is recognised as well as the documented codes.
const tokenPlanQuotaBody = `{"error":{"message":"Your token-plan 1-week quota has been exhausted. ` +
	`The quota will reset at 09-12 11:40:00 UTC."}}`

// An account that cannot be served is not a rate limit, not a bad key and not a
// bug in request assembly. Each of those misreadings sends somebody to the wrong
// fix: waiting for a retry that cannot succeed, reissuing a key that works, or
// debugging code that is fine.
// Codes from https://help.aliyun.com/zh/model-studio/error-code
// See docs/bugfix/2026-09-11-quota-429-retried-and-health-always-ok.md
func TestQwenAccountFailuresAreNotRateLimitsOrBadKeys(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"allocation quota", http.StatusTooManyRequests,
			`{"error":{"code":"Throttling.AllocationQuota","message":"Allocated quota exceeded, please increase your quota limit"}}`,
			"MODEL_QUOTA_EXHAUSTED"},
		{"insufficient quota", http.StatusTooManyRequests,
			`{"error":{"type":"insufficient_quota","message":"You exceeded your current quota"}}`,
			"MODEL_QUOTA_EXHAUSTED"},
		{"token plan week", http.StatusTooManyRequests, tokenPlanQuotaBody, "MODEL_QUOTA_EXHAUSTED"},
		{"free tier used up", http.StatusForbidden,
			`{"error":{"code":"AllocationQuota.FreeTierOnly","message":"Free tier of the model has been exhausted"}}`,
			"MODEL_QUOTA_EXHAUSTED"},
		{"arrearage", http.StatusBadRequest,
			`{"error":{"code":"Arrearage","message":"Access denied, please make sure your account is in good standing"}}`,
			"MODEL_BILLING"},
		// A real rate limit stays a rate limit, and is still retried.
		{"rate quota", http.StatusTooManyRequests,
			`{"error":{"code":"Throttling.RateQuota","message":"Requests rate limit exceeded, please try again later"}}`,
			"MODEL_RATE_LIMITED"},
		{"burst", http.StatusTooManyRequests,
			`{"error":{"code":"limit_burst_rate","message":"Request rate increased too quickly. To ensure system stability, please adjust your client logic to scale requests more smoothly"}}`,
			"MODEL_RATE_LIMITED"},
		{"concurrency", http.StatusTooManyRequests,
			`{"error":{"code":"Throttling.Concurrency","message":"Too many concurrent requests"}}`,
			"MODEL_RATE_LIMITED"},
		// A genuinely bad key and a genuinely bad request keep their meaning.
		{"bad key", http.StatusUnauthorized, `{"error":{"code":"InvalidApiKey","message":"invalid_api_key"}}`, "MODEL_AUTH_FAILED"},
		{"bad request", http.StatusBadRequest, `{"error":{"code":"InvalidParameter","message":"bad field"}}`, "MODEL_REQUEST_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newQwenStub(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			_, err := c.Stream(context.Background(), basicReq(), nil)
			if err == nil || !strings.Contains(err.Error(), tc.want+":") {
				t.Fatalf("HTTP %d %s gave %v, want %s", tc.status, tc.name, err, tc.want)
			}
		})
	}
}

// Retrying a used-up quota only makes the person wait three times for the same
// refusal, and the message must not promise a retry that cannot help.
func TestQuotaExhaustionIsTriedOnceAndSaysSo(t *testing.T) {
	var calls int32
	c := newQwenStub(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, tokenPlanQuotaBody)
	})
	_, err := llm.Retrying{Inner: c, Max: 2, Backoff: time.Millisecond}.Stream(context.Background(), basicReq(), nil)
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("a used-up quota was requested %d times, want 1", n)
	}
	if err == nil {
		t.Fatal("no error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "MODEL_QUOTA_EXHAUSTED:") {
		t.Errorf("error = %v, want MODEL_QUOTA_EXHAUSTED", err)
	}
	if strings.Contains(msg, "will be retried") || strings.Contains(msg, "delayed rather than wrong") {
		t.Errorf("the quota error promises a retry: %v", err)
	}
	if !strings.Contains(msg, "will not help") || !strings.Contains(msg, "09-12 11:40:00 UTC") {
		t.Errorf("the quota error does not say retrying will not help, or drops the vendor's reset time: %v", err)
	}
}

// A real rate limit is still retried - and once the retries are spent, the error
// the person sees must not still say it will be retried.
func TestRateLimitIsRetriedAndTheFinalErrorDoesNotPromiseMore(t *testing.T) {
	var calls int32
	c := newQwenStub(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"code":"Throttling.RateQuota","message":"Requests rate limit exceeded, please try again later"}}`)
	})
	_, err := llm.Retrying{Inner: c, Max: 2, Backoff: time.Millisecond}.Stream(context.Background(), basicReq(), nil)
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Errorf("a rate limit was requested %d times, want 3 (1 + 2 retries)", n)
	}
	if err == nil || !strings.Contains(err.Error(), "MODEL_RETRIES_EXHAUSTED:") {
		t.Fatalf("error = %v, want MODEL_RETRIES_EXHAUSTED", err)
	}
	if strings.Contains(err.Error(), "will be retried") {
		t.Errorf("the final error, after the retries are spent, still says it will be retried: %v", err)
	}
}
