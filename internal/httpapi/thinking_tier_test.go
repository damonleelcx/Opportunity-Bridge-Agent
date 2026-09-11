package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/agent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/httpapi"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

type requestRecorder struct {
	inner llm.Client
	mu    sync.Mutex
	reqs  []llm.Request
}

func (c *requestRecorder) Name() string { return c.inner.Name() }

func (c *requestRecorder) Stream(ctx context.Context, req llm.Request, sink func(llm.Event)) (llm.Response, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	return c.inner.Stream(ctx, req, sink)
}

func (c *requestRecorder) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

// The tier rides on the message like the locale: the browser keeps the person's
// choice and sends it every time. An unknown tier is refused before the turn
// starts, and meta offers exactly the tiers the server accepts.
func TestThinkingTierTravelsWithTheMessage(t *testing.T) {
	var rec *requestRecorder
	ts := newServerTweaking(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "ok"}, {Text: "ok"}, {Text: "ok"}}}, nil,
		func(s *httpapi.Server) {
			rec = &requestRecorder{inner: s.Agent.LLM}
			s.Agent.LLM = rec
		})
	c := signedIn(t, ts, "tier-chooser")
	res := postAs(t, c, ts.URL+"/api/sessions", map[string]string{"role": "resident"})
	var ses store.Session
	_ = json.NewDecoder(res.Body).Decode(&ses)
	res.Body.Close()

	res = postAs(t, c, ts.URL+"/api/sessions/"+ses.ID+"/messages",
		map[string]string{"message": "hello", "intent": "individual_pathway", "thinking": "off"})
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if rec.count() == 0 {
		t.Fatal("the turn never reached the model")
	}
	if first := rec.reqs[0]; first.Thinking || first.Effort != "" {
		t.Errorf("thinking=off reached the model as thinking=%v effort=%q", first.Thinking, first.Effort)
	}

	before := rec.count()
	res = postAs(t, c, ts.URL+"/api/sessions/"+ses.ID+"/messages",
		map[string]string{"message": "hello", "thinking": "xhigh"})
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "THINKING_TIER_INVALID") {
		t.Errorf("an unknown tier got HTTP %d %s, want 400 THINKING_TIER_INVALID", res.StatusCode, body)
	}
	if rec.count() != before {
		t.Error("an unknown tier still reached the model")
	}

	res = getAs(t, c, ts.URL+"/api/meta")
	var m struct {
		Tiers   []string `json:"thinking_tiers"`
		Default string   `json:"thinking_tier_default"`
	}
	_ = json.NewDecoder(res.Body).Decode(&m)
	res.Body.Close()
	if strings.Join(m.Tiers, ",") != strings.Join(agent.ThinkingTierNames(), ",") {
		t.Errorf("meta offers %v, the server accepts %v", m.Tiers, agent.ThinkingTierNames())
	}
	if m.Default != agent.DefaultThinkingTier {
		t.Errorf("meta default %q, want %q", m.Default, agent.DefaultThinkingTier)
	}
}
