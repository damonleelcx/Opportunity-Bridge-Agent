package agent_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/agent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/intent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
)

// capturing keeps every request that reached the model.
type capturing struct {
	inner llm.Client
	mu    sync.Mutex
	reqs  []llm.Request
}

func (c *capturing) Name() string { return c.inner.Name() }

func (c *capturing) Stream(ctx context.Context, req llm.Request, sink func(llm.Event)) (llm.Response, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	return c.inner.Stream(ctx, req, sink)
}

// The tier the person picks is what the model is asked for. "off" reaches it as
// thinking OFF with no effort: as an effort it would carry no thinking budget,
// which on Qwen is uncapped thinking.
func TestThinkingTierReachesTheModelRequest(t *testing.T) {
	for _, tc := range []struct {
		tier     string
		thinking bool
		effort   string
	}{
		{"", true, "high"}, // no choice: exactly what every turn did before
		{"off", false, ""},
		{"fast", true, "low"},
		{"balanced", true, "medium"},
		{"thorough", true, "high"},
	} {
		h := newHarness(t, domain.RoleResident, llm.Script{Turns: []llm.ScriptedTurn{{Text: "ok"}}})
		capt := &capturing{inner: h.ag.LLM}
		h.ag.LLM = capt
		// Only the first model request matters here; whether the script covers a
		// redraft does not.
		_, _ = h.ag.Run(context.Background(), agent.Input{
			SessionID: h.ses.ID, Message: "hello", Intent: intent.IndividualPathway, Tier: tc.tier,
		})
		if len(capt.reqs) == 0 {
			t.Fatalf("tier %q: the model was never asked", tc.tier)
		}
		for i, req := range capt.reqs {
			if req.Model != h.ag.Cfg.AgentModel {
				continue
			}
			if req.Thinking != tc.thinking || req.Effort != tc.effort {
				t.Errorf("tier %q request %d: thinking=%v effort=%q, want thinking=%v effort=%q",
					tc.tier, i, req.Thinking, req.Effort, tc.thinking, tc.effort)
			}
		}
	}
}

// An unknown tier is refused before the turn starts: no model call, nothing
// written to the conversation.
func TestAnUnknownThinkingTierIsRefusedBeforeAnythingHappens(t *testing.T) {
	h := newHarness(t, domain.RoleResident, llm.Script{Turns: []llm.ScriptedTurn{{Text: "ok"}}})
	capt := &capturing{inner: h.ag.LLM}
	h.ag.LLM = capt
	_, err := h.ag.Run(context.Background(), agent.Input{
		SessionID: h.ses.ID, Message: "hello", Intent: intent.IndividualPathway, Tier: "xhigh",
	})
	if err == nil || !strings.Contains(err.Error(), "THINKING_TIER_INVALID") {
		t.Fatalf("err = %v, want THINKING_TIER_INVALID", err)
	}
	if len(capt.reqs) != 0 {
		t.Errorf("the model was asked %d times for a tier that does not exist", len(capt.reqs))
	}
	if ses, _ := h.st.Session(h.ses.ID); len(ses.History) != 0 {
		t.Errorf("a refused turn wrote %d turns into the conversation", len(ses.History))
	}
}

func TestThinkingTiersKeepOffOutOfTheEffortVocabulary(t *testing.T) {
	want := []string{"off", "fast", "balanced", "thorough"}
	if got := agent.ThinkingTierNames(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("tiers = %v, want %v in that order", got, want)
	}
	for _, tier := range agent.ThinkingTiers {
		switch {
		case tier.Name == "off":
			if tier.Thinking || tier.Effort != "" {
				t.Errorf("off = %+v, want thinking false and no effort", tier)
			}
		case !tier.Thinking:
			t.Errorf("%s turns thinking off; only off may", tier.Name)
		case tier.Effort != "low" && tier.Effort != "medium" && tier.Effort != "high":
			t.Errorf("%s asks for effort %q, which the Qwen budget table does not know - that sends no budget at all",
				tier.Name, tier.Effort)
		}
	}
	if _, ok := agent.LookupThinkingTier(""); ok {
		t.Error("the empty name is a tier; it must mean 'no choice'")
	}
}

// The default is the tier that changes nothing. If an intent ever asks for a
// different effort, "thorough" stops meaning "as before" and the default has to
// be decided again, not silently kept.
func TestTheDefaultTierIsWhatEveryIntentAlreadyAsksFor(t *testing.T) {
	def, ok := agent.LookupThinkingTier(agent.DefaultThinkingTier)
	if !ok {
		t.Fatalf("the default %q is not a tier", agent.DefaultThinkingTier)
	}
	for _, in := range intent.All() {
		if in.Effort != def.Effort || !def.Thinking {
			t.Errorf("intent %s asks for effort %q with thinking on, but the default tier %q is effort %q thinking %v",
				in.ID, in.Effort, def.Name, def.Effort, def.Thinking)
		}
	}
}
