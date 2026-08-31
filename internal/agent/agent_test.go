package agent_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/agent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/corpus"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/intent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/retrieval"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tools"
)

type harness struct {
	ag  *agent.Agent
	st  *store.Store
	ses *store.Session
}

func newHarness(t *testing.T, role domain.Role, script llm.Script, consent ...domain.ConsentScope) *harness {
	t.Helper()
	c, err := corpus.Load("../../data")
	if err != nil {
		t.Fatalf("corpus: %v", err)
	}
	cfg := config.Config{
		AgentModel: "test", ClassifierModel: "", Effort: "high", MaxTokens: 4096,
		MaxIterations: 8, MaxToolCalls: 12, MaxWallClock: 30 * time.Second,
		MaxOutputTokens: 100000, KAnonymityFloor: 5, CorpusDir: "../../data",
	}
	st := store.New("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	ses := st.CreateSession(role, "", "en")
	for _, s := range consent {
		st.SetConsent(ses.SubjectID, s, true, "test")
	}
	return &harness{
		ag: &agent.Agent{
			Cfg: cfg, LLM: llm.NewScripted(script), Store: st, Corpus: c,
			Index: retrieval.NewIndex(c), Tools: tools.Default(),
		},
		st: st, ses: ses,
	}
}

func (h *harness) run(t *testing.T, msg string, in intent.ID) agent.Result {
	t.Helper()
	res, err := h.ag.Run(context.Background(), agent.Input{SessionID: h.ses.ID, Message: msg, Intent: in})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

func calls(name string, input map[string]any) []struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
} {
	b, _ := json.Marshal(input)
	return []struct {
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}{{Name: name, Input: b}}
}

// An approval authorises one specific action with one specific set of
// arguments. Approving a summary and then running something else is the whole
// failure mode the gate exists to prevent, so it gets its own test.
func TestApprovalDoesNotTransferToDifferentArguments(t *testing.T) {
	shown := map[string]any{
		"opportunity_id": "sub-001", "confirmed_with_person": true,
		"draft": "Application for sub-001, the vocational training fee reimbursement.",
	}
	swapped := map[string]any{
		"opportunity_id": "sub-002", "confirmed_with_person": true,
		"draft": "Application for sub-002, the unemployment start-up support payment.",
	}
	h := newHarness(t, domain.RoleResident, llm.Script{Turns: []llm.ScriptedTurn{
		{ToolCalls: calls("application_submit", shown)},
		{Text: "Nothing filed yet. Confirm the text on screen, or call 028-5553-0011."},
		{ToolCalls: calls("application_submit", swapped)},
		{Text: "I could not proceed. Call 028-5553-0011 or Window 12, Mon-Fri 09:00-16:30."},
	}}, domain.ConsentStoreProfile, domain.ConsentSubmitOnBehalf)

	first := h.run(t, "File it for me.", intent.IndividualPathway)
	if len(first.Approvals) != 1 {
		t.Fatalf("expected one approval to be raised, got %d", len(first.Approvals))
	}
	if _, err := h.st.DecideApproval(first.Approvals[0].ID, true, "approved as shown"); err != nil {
		t.Fatalf("decide: %v", err)
	}

	second := h.run(t, "Yes, go ahead.", intent.IndividualPathway)
	for _, c := range second.ToolCalls {
		if c.Name == "application_submit" && c.Err == "" {
			t.Fatal("a submission ran with arguments the person never saw")
		}
	}
	if len(second.Approvals) != 1 {
		t.Errorf("the swapped call should raise a fresh approval, got %d", len(second.Approvals))
	}
}

func TestApprovalReleasesTheExactActionThatWasShown(t *testing.T) {
	args := map[string]any{
		"opportunity_id": "sub-001", "confirmed_with_person": true,
		"draft": "Application for sub-001, the vocational training fee reimbursement.",
	}
	h := newHarness(t, domain.RoleResident, llm.Script{Turns: []llm.ScriptedTurn{
		{ToolCalls: calls("application_submit", args)},
		{Text: "Nothing filed yet. Approve the text on screen, or call 028-5553-0011."},
		{ToolCalls: calls("application_submit", args)},
		{Text: "Filed and tracked. You still need to complete it at Window 12, Mon-Fri 09:00-16:30, or on 028-5553-0011."},
	}}, domain.ConsentStoreProfile, domain.ConsentSubmitOnBehalf)

	first := h.run(t, "File it for me.", intent.IndividualPathway)
	if _, err := h.st.DecideApproval(first.Approvals[0].ID, true, "yes"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	second := h.run(t, "Yes, go ahead.", intent.IndividualPathway)
	ok := false
	for _, c := range second.ToolCalls {
		if c.Name == "application_submit" && c.Err == "" {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("the approved action never ran; tool calls: %+v", second.ToolCalls)
	}
}

func TestIrreversibleToolDoesNothingOnItsFirstCall(t *testing.T) {
	args := map[string]any{
		"opportunity_id": "sub-001", "confirmed_with_person": true,
		"draft": "Application for sub-001, the vocational training fee reimbursement.",
	}
	h := newHarness(t, domain.RoleResident, llm.Script{Turns: []llm.ScriptedTurn{
		{ToolCalls: calls("application_submit", args)},
		{Text: "Nothing has been filed. Approve it on screen, or call 028-5553-0011."},
	}}, domain.ConsentStoreProfile, domain.ConsentSubmitOnBehalf)
	res := h.run(t, "File it.", intent.IndividualPathway)
	if len(h.st.TasksFor(h.ses.SubjectID)) != 0 {
		t.Error("the filing left a trace before anybody approved it")
	}
	if res.StopReason != agent.StopAwaitingApproval {
		t.Errorf("stop reason %q, want awaiting_approval", res.StopReason)
	}
}

func TestToolsOutsideTheIntentAllowlistAreRefused(t *testing.T) {
	// gap_analysis reaches aggregate data. The individual pathway must not be
	// able to call it even if the model asks.
	h := newHarness(t, domain.RoleResident, llm.Script{Turns: []llm.ScriptedTurn{
		{ToolCalls: calls("gap_analysis", map[string]any{"group_by": "district"})},
		{Text: "I cannot look at that here. Call 12333 for the employment service."},
	}}, domain.ConsentStoreProfile)
	res := h.run(t, "Show me the district numbers.", intent.IndividualPathway)
	found := false
	for _, c := range res.ToolCalls {
		if c.Name == "gap_analysis" {
			found = true
			if !strings.Contains(c.Err, "TOOL_NOT_PERMITTED_FOR_INTENT") {
				t.Errorf("gap_analysis error was %q, want a permission refusal", c.Err)
			}
		}
	}
	if !found {
		t.Fatal("the call was not recorded at all")
	}
}

func TestBudgetStopsARunawayLoopWithSomethingReadable(t *testing.T) {
	same := map[string]any{"query": "潜水艇焊接", "city": "成都"}
	turns := make([]llm.ScriptedTurn, 0, 6)
	for i := 0; i < 5; i++ {
		turns = append(turns, llm.ScriptedTurn{ToolCalls: calls("opportunity_search", same)})
	}
	turns = append(turns, llm.ScriptedTurn{Text: "I did not get anywhere. Call 12333."})
	h := newHarness(t, domain.RoleResident, llm.Script{Turns: turns}, domain.ConsentStoreProfile)
	res := h.run(t, "find me something", intent.IndividualPathway)
	if res.StopReason != agent.StopRepeatedToolCall {
		t.Fatalf("stop reason %q, want repeated_tool_call", res.StopReason)
	}
	// Asserted on the system's own words, not on whatever the script happened to
	// say — the earlier version of this test passed because the scripted answer
	// contained a phone number.
	if !strings.Contains(res.Answer, "repeating the same lookup") {
		t.Errorf("the person was not told why the turn stopped, got: %q", res.Answer)
	}
}

func TestEveryDecisionIsTraced(t *testing.T) {
	h := newHarness(t, domain.RoleResident, llm.Script{Turns: []llm.ScriptedTurn{
		{ToolCalls: calls("opportunity_search", map[string]any{"query": "养老 护理", "city": "成都"})},
		// individual_pathway requires a turn that found a named programme to leave
		// a record of the step, not just a sentence - see next_step_is_tracked.
		{ToolCalls: calls("case_task_create", map[string]any{
			"domain": "employment", "title": "Ask the Qingyang day centre about job-002",
			"owner": "resident", "linked_ref": "job-002", "channel_phone": "028-5550-2244"})},
		{Text: "job-002 fits. Call 028-5550-2244, or the Qingyang window, Mon-Fri 09:00-17:00."},
	}}, domain.ConsentStoreProfile)
	res := h.run(t, "成都的养老护理岗", intent.IndividualPathway)

	want := map[string]bool{
		"agent.run.started": false, "agent.route.resolved": false,
		"agent.model.requested": false, "agent.tool.requested": false,
		"agent.tool.succeeded": false, "agent.run.finished": false,
	}
	for _, e := range res.Events {
		if _, ok := want[string(e.Name)]; ok {
			want[string(e.Name)] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("no %s event was recorded; the trace is the only place to answer \"why did it say that\"", name)
		}
	}
}

func TestShortTermMemoryDoesNotReplayToolCalls(t *testing.T) {
	// The second turn must not re-pay for the first turn's retrieval. Findings
	// are summarised into the context layer instead.
	h := newHarness(t, domain.RoleResident, llm.Script{Turns: []llm.ScriptedTurn{
		{ToolCalls: calls("opportunity_search", map[string]any{"query": "养老 护理", "city": "成都"})},
		// individual_pathway requires a turn that found a named programme to leave
		// a record of the step, not just a sentence - see next_step_is_tracked.
		{ToolCalls: calls("case_task_create", map[string]any{
			"domain": "employment", "title": "Ask the Qingyang day centre about job-002",
			"owner": "resident", "linked_ref": "job-002", "channel_phone": "028-5550-2244"})},
		{Text: "job-002 fits. Call 028-5550-2244."},
		{Text: "As I said, job-002. Call 028-5550-2244, Mon-Fri 09:00-17:00."},
	}}, domain.ConsentStoreProfile)
	h.run(t, "成都的养老护理岗", intent.IndividualPathway)
	h.run(t, "which one again?", intent.IndividualPathway)

	ses, _ := h.st.Session(h.ses.ID)
	if len(ses.Task.Findings) == 0 {
		t.Fatal("nothing was carried forward as a finding")
	}
	found := false
	for _, f := range ses.Task.Findings {
		if f.Tool == "opportunity_search" && strings.Contains(f.Summary, "job-002") {
			found = true
		}
	}
	if !found {
		t.Errorf("the search result was not summarised into short-term memory: %+v", ses.Task.Findings)
	}
}

func TestRolloutGateRefusesVisibly(t *testing.T) {
	h := newHarness(t, domain.RoleResident, llm.Script{Turns: []llm.ScriptedTurn{
		{Text: "should never be reached"},
	}}, domain.ConsentStoreProfile)
	h.ag.Cfg.EnabledIntents = []string{"low_access_support"}
	res := h.run(t, "find me a job", intent.IndividualPathway)
	if res.StopReason != agent.StopRefused {
		t.Errorf("stop reason %q, want a refusal", res.StopReason)
	}
	if !strings.Contains(res.Answer, "not switched on") {
		t.Errorf("a staged rollout must say so, got: %q", res.Answer)
	}
}

// The sentences the SYSTEM writes to the person — budget stops, blocked
// answers, the empty-answer fallback — appear at the worst moments in a turn.
// Leaving them in the code's language is where an unreadable message costs the
// most.
func TestSystemAuthoredMessagesFollowTheAnswerLanguage(t *testing.T) {
	same := map[string]any{"query": "潜水艇焊接", "city": "成都"}
	turns := make([]llm.ScriptedTurn, 0, 6)
	for i := 0; i < 5; i++ {
		turns = append(turns, llm.ScriptedTurn{ToolCalls: calls("opportunity_search", same)})
	}
	turns = append(turns, llm.ScriptedTurn{
		Text: "没查到合适的。打 12333 问问，或者到区就业服务大厅，周一到周五 9:00-17:00。"})

	h := newHarness(t, domain.RoleResident, llm.Script{Turns: turns}, domain.ConsentStoreProfile)
	if err := h.st.MutateSession(h.ses.ID, func(s *store.Session) error {
		s.Locale = "zh-CN"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	res := h.run(t, "随便找点什么", intent.IndividualPathway)
	if res.StopReason != agent.StopRepeatedToolCall {
		t.Fatalf("stop reason %q", res.StopReason)
	}
	if !strings.Contains(res.Answer, "反复查同一件事") {
		t.Errorf("the stop message was not written in the person's language:\n%s", res.Answer)
	}
	if strings.Contains(res.Answer, "I was repeating") {
		t.Error("the English stop message leaked into a Chinese session")
	}
}

func TestBlockedAnswerIsExplainedInTheAnswerLanguage(t *testing.T) {
	h := newHarness(t, domain.RoleResident, llm.Script{Turns: []llm.ScriptedTurn{
		{Text: "申请 sub-914，打 028-5559-0000。"},
		{Text: "还是 sub-914。打 028-5559-0000。"},
	}}, domain.ConsentStoreProfile)
	if err := h.st.MutateSession(h.ses.ID, func(s *store.Session) error {
		s.Locale = "zh-CN"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	res := h.run(t, "有没有给新工人的补贴？", intent.IndividualPathway)
	if !strings.Contains(res.Answer, "拦下来") {
		t.Errorf("the block message was not in Chinese:\n%s", res.Answer)
	}
	// The same rule tripping on the first draft and again on the redraft is one
	// rule, not two; repeating it verbatim reads as two separate problems.
	if strings.Count(res.Answer, "identifiers that are not in the corpus") > 1 {
		t.Errorf("the same finding was listed twice:\n%s", res.Answer)
	}
}

// A redraft that still fails used to go out as though it had passed. The
// verifiers had already caught the fragments reported from the live deployment;
// what went wrong is that nobody was told. The answer still goes out — it is
// usually most of an answer — but it now says so.
func TestUnresolvedFailuresAreDisclosedToTheReader(t *testing.T) {
	// Both drafts are a bare fragment: no citation, no next step.
	h := newHarness(t, domain.RoleResident, llm.Script{Turns: []llm.ScriptedTurn{
		{ToolCalls: calls("opportunity_search", map[string]any{"query": "养老 护理", "city": "成都"})},
		{Text: "（接上面）先这样。"},
		{Text: "不存也没关系。"},
	}}, domain.ConsentStoreProfile)
	if err := h.st.MutateSession(h.ses.ID, func(s *store.Session) error {
		s.Locale = "zh-CN"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	res := h.run(t, "成都的养老护理岗", intent.IndividualPathway)

	if !res.Redrafted {
		t.Fatal("the failing draft was not redrafted")
	}
	if !strings.Contains(res.Answer, "没完全通过") {
		t.Errorf("a still-failing answer went out with no disclosure:\n%s", res.Answer)
	}
	// The content is still delivered — most of an answer beats a refusal — and
	// the reader is given somewhere to go.
	if !strings.Contains(res.Answer, "12333") {
		t.Errorf("the disclosure did not offer a way out:\n%s", res.Answer)
	}
	for _, f := range res.Findings {
		if f.Code == "UNRESOLVED_AFTER_REDRAFT" {
			t.Error("the disclosure must not itself be reported as a finding")
		}
	}
}

// ---------------------------------------------------------------------------
// Regression fence for docs/bugfix/2026-08-28-duplicate-tool-start-events.md
//
// The stream used to carry two tool_start events for every one tool_result: one
// from the model's own stream when it announced the tool, one at the call site.
// Measured on a live turn: 14 against 7. The pair could not be reconciled by any
// client, because the streamed one carries neither arguments nor a tool-use id.
func TestOneToolStartPerToolResult(t *testing.T) {
	h := newHarness(t, domain.RoleResident, llm.Script{Turns: []llm.ScriptedTurn{
		// Two tools in ONE model response, which is where the duplication was
		// most visible: the stream announced both, then the loop invoked both.
		{ToolCalls: append(
			calls("opportunity_search", map[string]any{"query": "养老 护理", "city": "成都"}),
			calls("knowledge_search", map[string]any{"query": "失业登记"})...)},
		{ToolCalls: calls("case_task_create", map[string]any{
			"domain": "employment", "title": "Ask the Qingyang day centre about job-002",
			"owner": "resident", "linked_ref": "job-002", "channel_phone": "028-5550-2244"})},
		{Text: "job-002 fits. Call 028-5550-2244, Mon-Fri 09:00-17:00."},
	}}, domain.ConsentStoreProfile)

	var starts, results int
	var argless []string
	_, err := h.ag.Run(context.Background(), agent.Input{
		SessionID: h.ses.ID, Message: "成都的养老护理岗", Intent: intent.IndividualPathway,
		Sink: func(e agent.Event) {
			switch e.Kind {
			case agent.EvToolStart:
				starts++
				if len(e.Args) == 0 {
					argless = append(argless, e.Tool)
				}
			case agent.EvToolResult:
				results++
			}
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if results == 0 {
		t.Fatal("no tool ran; this test cannot say anything about pairing")
	}
	if starts != results {
		t.Errorf("%d tool_start events against %d tool_result events; every call must announce itself exactly once", starts, results)
	}
	// The surviving emission is the one at the call site, which is the only one
	// that knows the arguments. An argument-less tool_start is the ghost coming
	// back: the trace panel has nothing to show for it.
	if len(argless) > 0 {
		t.Errorf("tool_start arrived without arguments for %v; the trace panel renders these empty", argless)
	}
}

// A tool the budget refuses never runs, so it must not announce that it started.
// The streamed emission fired before the budget check and claimed otherwise.
func TestBudgetBlockedToolAnnouncesNoStart(t *testing.T) {
	same := map[string]any{"query": "养老 护理", "city": "成都"}
	h := newHarness(t, domain.RoleResident, llm.Script{Turns: []llm.ScriptedTurn{
		{ToolCalls: calls("opportunity_search", same)},
		{ToolCalls: calls("opportunity_search", same)}, // identical: the repeat guard trips
		{ToolCalls: calls("case_task_create", map[string]any{
			"domain": "employment", "title": "Ask the Qingyang day centre about job-002",
			"owner": "resident", "linked_ref": "job-002", "channel_phone": "028-5550-2244"})},
		{Text: "job-002 fits. Call 028-5550-2244."},
	}}, domain.ConsentStoreProfile)

	var starts, results int
	_, err := h.ag.Run(context.Background(), agent.Input{
		SessionID: h.ses.ID, Message: "成都的养老护理岗", Intent: intent.IndividualPathway,
		Sink: func(e agent.Event) {
			switch e.Kind {
			case agent.EvToolStart:
				starts++
			case agent.EvToolResult:
				results++
			}
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if starts > results {
		t.Errorf("%d tool_start against %d tool_result: a call the budget refused was reported as started", starts, results)
	}
}

// The failure this reproduces: a model turn returned the router's decision
// object as its answer, the redraft returned it again, and the loop delivered
// it with an apology appended. What the person read was JSON.
//
// The fence is at the agent boundary on purpose. Blocking inside the verifier
// is necessary but not sufficient - what mattered was that the unresolved path,
// which delivers a still-failing draft "because it is usually most of an
// answer", must never be the path a machine object takes to the screen.
// See docs/bugfix/2026-08-31-routing-json-shown-as-answer.md
func TestRoutingObjectNeverReachesTheReader(t *testing.T) {
	const leaked = `{"intent": "individual_pathway", "confidence": 0.92, ` +
		`"rationale": "Same person, same objective; they are asking to have the step tracked."}`

	// Twice: the first is the draft, the second is the redraft. A model that
	// slips once gets a second chance; one that slips twice must be refused.
	h := newHarness(t, domain.RoleResident, llm.Script{Turns: []llm.ScriptedTurn{
		{Text: leaked},
		{Text: leaked},
	}})
	res := h.run(t, "帮我把这一步记下来。", intent.IndividualPathway)

	if strings.Contains(res.Answer, `"confidence"`) || strings.Contains(res.Answer, `"rationale"`) {
		t.Fatalf("the routing object was delivered to the reader:\n%s", res.Answer)
	}
	if res.StopReason != agent.StopRefused {
		t.Errorf("stop reason = %q, want %q: an undeliverable draft must refuse, not pass quietly",
			res.StopReason, agent.StopRefused)
	}
	// The refusal has to say what happened and where to go instead; a blank
	// screen would be the same failure wearing different clothes.
	if len(res.Answer) < 20 {
		t.Errorf("refusal is too short to act on: %q", res.Answer)
	}
	var blocked bool
	for _, f := range res.Findings {
		if f.Code == "ANSWER_IS_MACHINE_OUTPUT" || f.Code == "ROUTING_OBJECT_LEAKED" {
			blocked = true
		}
	}
	if !blocked {
		t.Errorf("nothing recorded why the draft was stopped; findings = %v", res.Findings)
	}
}
