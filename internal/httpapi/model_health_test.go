package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/httpapi"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

// readHealth reads /api/health as a stranger would: no sign-in.
func readHealth(t *testing.T, ts *httptest.Server) (int, string, map[string]any) {
	t.Helper()
	res, err := ts.Client().Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("health is not JSON: %v: %s", err, b)
	}
	return res.StatusCode, string(b), m
}

// On 2026-09-11 every model call was refused for a used-up quota while
// /api/health said "ok", and nobody was using the site to notice. Health must
// turn red when the model cannot answer - and stay HTTP 200, because the k8s
// probes read only the code and a model outage must not get the pod killed.
// See docs/bugfix/2026-09-11-quota-429-retried-and-health-always-ok.md
func TestHealthSaysDegradedWhenTheModelQuotaIsUsedUp(t *testing.T) {
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"Your token-plan 1-week quota has been exhausted. `+
			`The quota will reset at 09-12 11:40:00 UTC."}}`)
	}))
	t.Cleanup(vendor.Close)
	// No check: this test is about what a real conversation leaves behind.
	health := llm.NewHealth(llm.NewQwen("qw-test-key", vendor.URL), llm.HealthOptions{})
	ts := newServerTweaking(t, llm.Script{}, nil, func(s *httpapi.Server) {
		s.Agent.LLM = health
		s.Model = health
	})

	code, _, m := readHealth(t, ts)
	if code != http.StatusOK || m["status"] != "unknown" {
		t.Fatalf("before any call: HTTP %d status %v, want 200 and unknown - nothing has answered yet, so it is not ok",
			code, m["status"])
	}

	c := signedIn(t, ts, "quota-watcher")
	res := postAs(t, c, ts.URL+"/api/sessions", map[string]string{"role": "resident"})
	var ses store.Session
	_ = json.NewDecoder(res.Body).Decode(&ses)
	res.Body.Close()
	res = postAs(t, c, ts.URL+"/api/sessions/"+ses.ID+"/messages", map[string]string{"message": "我在成都，想找工作"})
	stream, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(stream), "MODEL_QUOTA_EXHAUSTED") {
		t.Fatalf("the turn did not end with MODEL_QUOTA_EXHAUSTED; stream: %s", stream)
	}

	code, body, m := readHealth(t, ts)
	if code != http.StatusOK {
		t.Errorf("health answered HTTP %d during a model outage; the probes would kill a pod that still serves everything else", code)
	}
	if m["status"] != "degraded" {
		t.Errorf("status = %v during a used-up quota, want degraded", m["status"])
	}
	model, _ := m["model"].(map[string]any)
	if model["state"] != "degraded" || model["last_error_code"] != "MODEL_QUOTA_EXHAUSTED" {
		t.Errorf("model = %v, want state degraded with last_error_code MODEL_QUOTA_EXHAUSTED", model)
	}
	// Codes and times only: the vendor's message names account details, and this
	// endpoint is public.
	for _, leak := range []string{"token-plan", "09-12 11:40"} {
		if strings.Contains(body, leak) {
			t.Errorf("/api/health repeats the vendor's message (%q); it is served to anybody", leak)
		}
	}
}

// A server that nobody told how to watch the model must not claim it is fine.
func TestHealthWithoutAWatchedModelIsUnknownNotOK(t *testing.T) {
	ts := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "x"}}})
	_, _, m := readHealth(t, ts)
	model, _ := m["model"].(map[string]any)
	if m["status"] != "unknown" || model["state"] != "unknown" {
		t.Errorf("status %v model %v, want unknown for both", m["status"], model)
	}
}
