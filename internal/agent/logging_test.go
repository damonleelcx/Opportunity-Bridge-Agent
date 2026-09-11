package agent_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/agent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/intent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/obs"
)

// A run's events reach the process log.
//
// On 2026-09-11 a production turn drew no cards and the pod log could not say
// whether any tool had run: a run's events went only to the browser tab that ran
// it. These fences hold the server-side record: which tools ran, under which run
// id, what was kept for replay, and that every exit says the turn is over.
// See docs/bugfix/2026-09-11-agent-events-never-reached-the-logs.md

// lineWith returns the first log line containing every needle, or "".
func lineWith(out string, needles ...string) string {
	for _, l := range strings.Split(out, "\n") {
		ok := true
		for _, n := range needles {
			if !strings.Contains(l, n) {
				ok = false
				break
			}
		}
		if ok {
			return l
		}
	}
	return ""
}

func withLog(h *harness) *bytes.Buffer {
	var buf bytes.Buffer
	h.ag.Log = slog.New(obs.NewContextHandler(slog.NewTextHandler(&buf, nil)))
	return &buf
}

func TestASuccessfulToolCallIsLoggedUnderItsRunID(t *testing.T) {
	h := newHarness(t, domain.RoleResident, llm.Script{Turns: []llm.ScriptedTurn{
		{ToolCalls: calls("opportunity_search", map[string]any{"query": "养老 护理", "city": "成都"})},
		{ToolCalls: calls("case_task_create", map[string]any{
			"domain": "employment", "title": "Ask the Qingyang day centre about job-002",
			"owner": "resident", "linked_ref": "job-002", "channel_phone": "028-5550-2244"})},
		{Text: "job-002 fits. Call 028-5550-2244, or the Qingyang window, Mon-Fri 09:00-17:00."},
	}}, domain.ConsentStoreProfile)
	buf := withLog(h)
	res := h.run(t, "成都的养老护理岗", intent.IndividualPathway)
	out := buf.String()

	run := "run_id=" + res.RunID
	if lineWith(out, "event.name=agent.tool.requested", run, "tool=opportunity_search", "args_hash=") == "" {
		t.Errorf("no tool.requested line for opportunity_search under %s:\n%s", run, out)
	}
	if lineWith(out, "event.name=agent.tool.succeeded", run, "tool=opportunity_search") == "" {
		t.Errorf("no tool.succeeded line for opportunity_search under %s:\n%s", run, out)
	}
	// The line that would have answered the production question on its own.
	fin := lineWith(out, "event.name=agent.run.finished", run)
	if fin == "" {
		t.Fatalf("no run.finished line under %s:\n%s", run, out)
	}
	if !strings.Contains(fin, "cards_kept=[opportunity_search]") {
		t.Errorf("run.finished does not say which cards were kept: %s", fin)
	}
	// Arguments stay out: the query is the person's words.
	for _, private := range []string{"养老 护理", "028-5550-2244", "Qingyang day centre"} {
		if strings.Contains(out, private) {
			t.Errorf("tool arguments reached the log (%q):\n%s", private, out)
		}
	}
}

func TestARefusedTurnStillSaysItIsOver(t *testing.T) {
	h := newHarness(t, domain.RoleResident, llm.Script{})
	h.ag.Cfg.EnabledIntents = []string{"some_other_intent"}
	buf := withLog(h)
	res := h.run(t, "成都的养老护理岗", intent.IndividualPathway)
	out := buf.String()

	if lineWith(out, "event.name=agent.route.rejected", "error.code=INTENT_DISABLED") == "" {
		t.Errorf("the refusal itself was not logged:\n%s", out)
	}
	// The constant, not a literal: StopRefused's value is "model_refusal", and a
	// guessed literal is how this assertion first went red for the wrong reason.
	if lineWith(out, "event.name=agent.run.finished", "run_id="+res.RunID,
		"stop_reason="+string(agent.StopRefused), "cards_kept=[]") == "" {
		t.Errorf("a refused turn has no terminal line saying it is over:\n%s", out)
	}
	if n := countEvents(res.Events, obs.RunFinished); n != 1 {
		t.Errorf("the browser trace got %d run.finished events, want 1", n)
	}
}

func TestAFailedTurnStillSaysItIsOverOnce(t *testing.T) {
	// An empty script: the first model call fails with SCRIPT_EXHAUSTED.
	h := newHarness(t, domain.RoleResident, llm.Script{})
	buf := withLog(h)
	res, err := h.ag.Run(context.Background(), agent.Input{
		SessionID: h.ses.ID, Message: "成都的养老护理岗", Intent: intent.IndividualPathway,
	})
	if err == nil {
		t.Fatal("an empty script should fail the model call; this fence proves nothing")
	}
	out := buf.String()

	if lineWith(out, "event.name=agent.run.failed", "run_id="+res.RunID, "error.code=MODEL_CALL_FAILED", "cards_kept=[]") == "" {
		t.Errorf("a failed turn has no terminal line saying it is over:\n%s", out)
	}
	if strings.Contains(out, "SCRIPT_EXHAUSTED") {
		t.Errorf("the error message reached the log; only its code may:\n%s", out)
	}
	// fail() emits the terminal event now; the call site used to as well.
	if n := countEvents(res.Events, obs.RunFailed); n != 1 {
		t.Errorf("run.failed was emitted %d times, want exactly 1", n)
	}
}

func countEvents(evs []obs.Event, name obs.Name) int {
	n := 0
	for _, e := range evs {
		if e.Name == name {
			n++
		}
	}
	return n
}
