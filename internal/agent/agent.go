package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/corpus"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/guardrail"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/intent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/livesource"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/obs"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/prompt"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/retrieval"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/talentsource"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tools"
)

// Agent wires the pieces together. It owns nothing mutable itself: everything
// per-conversation lives in the store, which is what lets several conversations
// run at once without a lock here.
type Agent struct {
	Cfg    config.Config
	LLM    llm.Client
	Store  *store.Store
	Corpus *corpus.Corpus
	Index  *retrieval.Index
	Tools  *tools.Registry
	// Live is consulted when the corpus has no local listing for a city. Nil is
	// legitimate: the corpus is then all there is.
	Live livesource.Provider
	// Talent looks for PEOPLE outside the opt-in pool, for the recruiter intent.
	// Nil is the common case and means the pool is all there is.
	Talent talentsource.Provider
}

// Input is one turn.
type Input struct {
	SessionID string
	Message   string
	// Intent, when set, is the intent chip the person selected in the interface.
	Intent intent.ID
	// Sink receives streaming events for the interface. It must not block.
	Sink func(Event)
}

// EventKind is what the interface can render as a turn unfolds.
type EventKind string

const (
	EvRouted     EventKind = "routed"
	EvThinking   EventKind = "thinking"
	EvText       EventKind = "text"
	EvToolStart  EventKind = "tool_start"
	EvToolResult EventKind = "tool_result"
	EvGuardrail  EventKind = "guardrail"
	EvVerify     EventKind = "verify"
	EvApproval   EventKind = "approval"
	EvConsent    EventKind = "consent"
	EvTrace      EventKind = "trace"
	EvFinal      EventKind = "final"
	EvError      EventKind = "error"
)

type Event struct {
	Kind     EventKind              `json:"kind"`
	Text     string                 `json:"text,omitempty"`
	Tool     string                 `json:"tool,omitempty"`
	Args     json.RawMessage        `json:"args,omitempty"`
	Result   json.RawMessage        `json:"result,omitempty"`
	IsError  bool                   `json:"is_error,omitempty"`
	Finding  *guardrail.Finding     `json:"finding,omitempty"`
	Route    *intent.Decision       `json:"route,omitempty"`
	Approval *store.PendingApproval `json:"approval,omitempty"`
	Consent  *tools.ConsentPrompt   `json:"consent,omitempty"`
	Trace    *obs.Event             `json:"trace,omitempty"`
	Final    *Result                `json:"final,omitempty"`
	// Reset, on a text event, means everything streamed so far this turn is void
	// and must be cleared from the screen. It rides on the text event rather than
	// arriving as a kind of its own so that a client which does not know about it
	// appends an empty string instead of ignoring an unknown event — and the
	// final event still corrects the display either way.
	Reset bool `json:"reset,omitempty"`
}

// Result is the completed turn.
type Result struct {
	RunID      string                     `json:"run_id"`
	Intent     string                     `json:"intent"`
	Route      intent.Decision            `json:"route"`
	Answer     string                     `json:"answer"`
	StopReason StopReason                 `json:"stop_reason"`
	Findings   []guardrail.Finding        `json:"findings,omitempty"`
	ToolCalls  []guardrail.ToolCallRecord `json:"tool_calls,omitempty"`
	Approvals  []store.PendingApproval    `json:"pending_approvals,omitempty"`
	Consents   []tools.ConsentPrompt      `json:"consent_requests,omitempty"`
	Usage      llm.Usage                  `json:"usage"`
	Iterations int                        `json:"iterations"`
	Redrafted  bool                       `json:"redrafted"`
	ElapsedMS  int64                      `json:"elapsed_ms"`
	Events     []obs.Event                `json:"trace,omitempty"`
}

// maxRedrafts is one on purpose. A second redraft nearly always produces the
// same failure in different words, and the person is still waiting. One attempt
// to fix, then deliver the guard's own message.
const maxRedrafts = 1

func (a *Agent) Run(ctx context.Context, in Input) (Result, error) {
	runID := fmt.Sprintf("run_%d", time.Now().UnixNano())
	rec := obs.NewRecorder(runID, in.SessionID)
	emit := func(e Event) {
		if in.Sink != nil {
			in.Sink(e)
		}
	}
	rec.Subscribe(func(ev obs.Event) {
		e := ev
		emit(Event{Kind: EvTrace, Trace: &e})
	})
	start := time.Now()

	ses, ok := a.Store.Session(in.SessionID)
	if !ok {
		return Result{}, fmt.Errorf("SESSION_NOT_FOUND: no session %q; start a new conversation", in.SessionID)
	}
	rec.Info(obs.RunStarted, "turn started", map[string]any{
		"role": string(ses.Role), "backend": a.LLM.Name(), "message_chars": len(in.Message),
	})

	// ---- input guards, before routing. Once one of these fires, which intent
	// the message belongs to has stopped being the interesting question.
	var alerts []string
	var findings []guardrail.Finding
	for _, f := range guardrail.DetectEscalation(in.Message) {
		findings = append(findings, f)
		alerts = append(alerts, f.Message)
		ff := f
		emit(Event{Kind: EvGuardrail, Finding: &ff})
		rec.Warn(obs.GuardrailTripped, f.Code, f.Message, map[string]any{"guard": f.Guard})
	}

	// ---- route
	dec, err := intent.Route(ctx, a.LLM, a.Cfg.ClassifierModel, ses.Role, in.Intent, in.Message, intent.ID(ses.Intent))
	if err != nil {
		rec.Error(obs.RouteRejected, "ROUTE_FAILED", err.Error(), nil)
		return a.fail(rec, runID, start, err)
	}
	if !a.Cfg.IntentEnabled(string(dec.ID)) {
		// The rollout gate refuses visibly. A staged rollout that silently
		// answers with something else is worse than one that says "not yet".
		msg := sysMsg(replyLanguage(a.Cfg, ses), msgIntentDisabled, dec.ID)
		rec.Warn(obs.RouteRejected, "INTENT_DISABLED", msg, map[string]any{"intent": string(dec.ID)})
		emit(Event{Kind: EvFinal, Final: &Result{RunID: runID, Intent: string(dec.ID), Answer: msg, StopReason: StopRefused}})
		return Result{RunID: runID, Intent: string(dec.ID), Route: dec, Answer: msg,
			StopReason: StopRefused, Events: rec.Events()}, nil
	}
	in5, _ := intent.Get(dec.ID)
	rec.SetIntent(string(dec.ID))
	rec.Info(obs.RouteResolved, dec.Rationale, map[string]any{
		"intent": string(dec.ID), "method": dec.Method, "confidence": dec.Confidence,
	})
	emit(Event{Kind: EvRouted, Route: &dec})

	_ = a.Store.MutateSession(ses.ID, func(s *store.Session) error {
		s.Intent = string(dec.ID)
		if s.Task.Objective == "" {
			s.Task.Objective = truncate(in.Message, 240)
		}
		s.History = append(s.History, store.Turn{
			Role: "user", Text: in.Message, Intent: string(dec.ID), At: time.Now().UTC(), RunID: runID,
		})
		return nil
	})
	ses, _ = a.Store.Session(in.SessionID)

	// The language every system-authored sentence in this turn is written in.
	lang := replyLanguage(a.Cfg, ses)

	// ---- budgets: process-wide ceilings clamp the per-intent ones, never the
	// other way round, so no intent can widen its own limits.
	budget := NewBudget(
		min(in5.MaxIterations, a.Cfg.MaxIterations),
		min(in5.MaxToolCalls, a.Cfg.MaxToolCalls),
		a.Cfg.MaxOutputTokens,
		a.Cfg.MaxWallClock,
	)
	effort := in5.Effort
	if effort == "" {
		effort = a.Cfg.Effort
	}

	env := tools.Env{
		Cfg: a.Cfg, Store: a.Store, Corpus: a.Corpus, Index: a.Index,
		Session: ses, Rec: rec, Live: a.Live, Talent: a.Talent,
		Approvals: map[string]store.PendingApproval{},
		// One sequence for the whole turn. Built here because this is the only
		// scope that IS the turn: env is made once per Run and reused across
		// every iteration and every tool call within it.
		LiveSeq: &livesource.Sequence{},
	}
	for _, ap := range a.approvedForSession(ses.ID) {
		env.Approvals[ap.ID] = ap
	}

	toolDefs := toolDefinitions(a.Tools.Subset(in5.AllowedTools))
	messages := buildHistory(ses, in.Message)

	var (
		answer      string
		toolRecords []guardrail.ToolCallRecord
		pending     []store.PendingApproval
		consents    []tools.ConsentPrompt
		// seenConsent keeps one turn from raising the same permission card
		// twice. Two places can raise one: the consent_request tool, and the
		// gate in registry.Call when a tool needs a scope that is not held. A
		// turn that does both asked the same question twice on one screen.
		// See docs/bugfix/2026-08-28-consent-asked-twice.md
		seenConsent = map[string]bool{}
		usage       llm.Usage
		corrections []string
		redrafts    int
		stop        = StopAnswered
		// explained records whether the person has already been told why the
		// turn ended early, so the explanation is not appended twice.
		explained bool
	)

	for {
		if reason, hit := budget.CheckStep(); hit {
			stop = reason
			rec.Warn(obs.BudgetExceeded, strings.ToUpper(string(reason)), Explain("en", reason, budget),
				map[string]any{"iterations": budget.Iterations(), "tool_calls": budget.ToolCalls()})
			answer = joinAnswer(answer, Explain(lang, reason, budget))
			explained = true
			break
		}
		budget.StepTaken()
		rec.SetStep(budget.Iterations())

		profile := a.Store.Profile(ses.SubjectID)
		charter, intentLayer, contextLayer := prompt.Layers(prompt.Options{
			Intent: in5, Session: ses, Profile: profile,
			Consent:     a.Store.ConsentAll(ses.SubjectID),
			Tasks:       a.Store.TasksFor(ses.SubjectID),
			Corrections: corrections, Alerts: alerts,
			Locale: replyLanguage(a.Cfg, ses), CitiesCovered: a.Corpus.Cities(),
		})
		req := llm.Request{
			Model: a.Cfg.AgentModel,
			System: []llm.SystemBlock{
				{Text: charter, Cache: true},
				{Text: intentLayer, Cache: true},
				{Text: contextLayer},
			},
			Messages: messages, Tools: toolDefs,
			MaxTokens: a.Cfg.MaxTokens, Effort: effort, Thinking: true,
		}
		rec.Info(obs.ModelRequested, "model request", map[string]any{
			"model": req.Model, "effort": effort, "tools": len(toolDefs),
			"messages": len(messages), "system_chars": len(charter) + len(intentLayer) + len(contextLayer),
		})

		client := llm.Retrying{Inner: a.LLM, Max: a.Cfg.MaxRetries, OnRetry: func(attempt int, err error) {
			rec.Warn(obs.ModelRetried, "MODEL_RETRY", err.Error(), map[string]any{"attempt": attempt})
		}}
		resp, err := client.Stream(ctx, req, func(e llm.Event) {
			switch e.Kind {
			case llm.EventTextDelta:
				emit(Event{Kind: EvText, Text: e.Text})
			case llm.EventThinkingDelta:
				emit(Event{Kind: EvThinking, Text: e.Text})
			case llm.EventReset:
				// An attempt failed after it had already written something. What
				// it wrote is void; the retry starts from a clean screen.
				emit(Event{Kind: EvText, Reset: true})
				// llm.EventToolUse is deliberately NOT turned into an EvToolStart
				// here. It fires when the model announces a tool, which is before
				// the budget check and before the refusal check - so a tool that is
				// about to be blocked, or a response that is about to be refused,
				// used to emit "tool_start" for a call that never started. It also
				// carries no arguments and no tool-use id, so a client had no way to
				// pair it with the real one emitted at the call site below, and the
				// stream carried two tool_start events for every one tool_result.
				// The signal it bought was a status flip a few tens of milliseconds
				// earlier - the only thing between here and the call site is the
				// remainder of this same response stream.
				// See docs/bugfix/2026-08-28-duplicate-tool-start-events.md
			}
		})
		if err != nil {
			rec.Error(obs.RunFailed, "MODEL_CALL_FAILED", err.Error(), nil)
			return a.fail(rec, runID, start, err)
		}
		usage.InputTokens += resp.Usage.InputTokens
		usage.OutputTokens += resp.Usage.OutputTokens
		usage.CacheReadTokens += resp.Usage.CacheReadTokens
		usage.CacheWriteTokens += resp.Usage.CacheWriteTokens
		budget.AddOutput(resp.Usage.OutputTokens)
		rec.Info(obs.ModelResponded, "model response", map[string]any{
			"stop_reason": resp.StopReason, "blocks": len(resp.Blocks),
			"output_tokens": resp.Usage.OutputTokens, "cache_read": resp.Usage.CacheReadTokens,
		})

		if resp.StopReason == "refusal" {
			stop = StopRefused
			rec.Warn(obs.RunFinished, "MODEL_REFUSAL", "the model declined this request",
				map[string]any{"category": resp.RefusalCategory})
			answer = Explain(lang, StopRefused, budget)
			break
		}

		messages = append(messages, llm.Message{Role: llm.RoleAssistant, Blocks: resp.Blocks})
		if t := resp.TextContent(); strings.TrimSpace(t) != "" {
			answer = t
		}

		uses := resp.ToolUses()
		if len(uses) == 0 {
			// ---- verify stage
			vFindings := a.verify(rec, in5, ses, answer, toolRecords)
			for _, f := range vFindings {
				ff := f
				emit(Event{Kind: EvVerify, Finding: &ff})
			}
			findings = append(findings, vFindings...)
			needsRedraft := false
			var remedies []string
			var blocked []guardrail.Finding
			for _, f := range vFindings {
				switch f.Severity {
				case guardrail.Repair:
					needsRedraft = true
					remedies = append(remedies, f.Message+" "+f.Remedy)
				case guardrail.Block:
					needsRedraft = true
					blocked = append(blocked, f)
					remedies = append(remedies, f.Message+" "+f.Remedy)
				}
			}
			if needsRedraft && redrafts < maxRedrafts {
				redrafts++
				corrections = remedies
				// The failed draft is dropped from the history rather than
				// carried forward, so the model is not anchored on the text it
				// has just been told is wrong.
				messages = messages[:len(messages)-1]
				continue
			}
			// A redraft that still fails used to be delivered as though it had
			// passed. The verifiers had already caught both fragments reported
			// from the live deployment; what went wrong is that nobody was told.
			// The answer still goes out — it is usually most of an answer — but
			// it says so, and offers the way out.
			if needsRedraft && redrafts >= maxRedrafts && len(blocked) == 0 {
				var why []string
				for _, f := range vFindings {
					if f.Severity == guardrail.Repair {
						why = append(why, blockReason(lang, f.Code, f.Message))
					}
				}
				if len(why) > 0 {
					answer = joinAnswer(answer, sysMsg(lang, msgUnresolved, strings.Join(why, "；")))
					rec.Warn(obs.VerifierFailed, "UNRESOLVED_AFTER_REDRAFT",
						"the redraft still failed; the answer was delivered with a note saying so",
						map[string]any{"codes": codesOf(vFindings)})
				}
			}
			if len(blocked) > 0 {
				// The block message already names the rule and says nothing was
				// done, so the generic refusal line would only repeat it.
				answer = blockMessage(lang, blocked)
				stop = StopRefused
				explained = true
			}
			break
		}

		// ---- act stage
		var results []llm.Block
		stopped := false
		for _, use := range uses {
			hash := tools.ArgsHash(use.ToolInput)
			if reason, hit := budget.CheckTool(use.ToolName, hash); hit {
				stop = reason
				rec.Warn(obs.BudgetExceeded, strings.ToUpper(string(reason)), Explain("en", reason, budget),
					map[string]any{"tool": use.ToolName})
				// The model is instructed in English; the person is not.
				results = append(results, llm.ToolResult(use.ToolUseID,
					"BUDGET_EXCEEDED: "+Explain("en", reason, budget)+" Stop and answer with what you already have.", true))
				stopped = true
				continue
			}
			budget.ToolTaken(use.ToolName, hash)
			emit(Event{Kind: EvToolStart, Tool: use.ToolName, Args: use.ToolInput})
			rec.Info(obs.ToolRequested, "tool requested",
				map[string]any{"tool": use.ToolName, "args_hash": hash})

			res, callErr := a.Tools.Call(ctx, env, func(n string) bool {
				return intent.ToolAllowed(in5.ID, n)
			}, use.ToolName, use.ToolInput)

			record := guardrail.ToolCallRecord{Name: use.ToolName, Meta: res.Meta}
			_ = json.Unmarshal(use.ToolInput, &record.Args)
			for _, f := range res.Findings {
				ff := f
				findings = append(findings, f)
				emit(Event{Kind: EvGuardrail, Finding: &ff})
				rec.Warn(obs.GuardrailTripped, f.Code, f.Message, map[string]any{"guard": f.Guard, "tool": use.ToolName})
			}
			if res.Approval != nil {
				pending = append(pending, *res.Approval)
				ap := *res.Approval
				emit(Event{Kind: EvApproval, Approval: &ap})
				stop = StopAwaitingApproval
			}
			if res.Consent != nil && !seenConsent[string(res.Consent.Scope)] {
				seenConsent[string(res.Consent.Scope)] = true
				consents = append(consents, *res.Consent)
				cp := *res.Consent
				emit(Event{Kind: EvConsent, Consent: &cp})
			}
			if callErr != nil {
				record.Err = callErr.Error()
				rec.Warn(obs.ToolFailed, codeOf(callErr.Error()), callErr.Error(),
					map[string]any{"tool": use.ToolName})
				results = append(results, llm.ToolResult(use.ToolUseID, callErr.Error(), true))
				emit(Event{Kind: EvToolResult, Tool: use.ToolName, IsError: true,
					Result: json.RawMessage(mustJSON(map[string]any{"error": callErr.Error()}))})
				toolRecords = append(toolRecords, record)
				continue
			}
			payload := mustJSON(res.Content)
			record.Result = payload
			toolRecords = append(toolRecords, record)
			rec.Info(obs.ToolSucceeded, "tool succeeded",
				map[string]any{"tool": use.ToolName, "result_bytes": len(payload)})
			results = append(results, llm.ToolResult(use.ToolUseID, payload, false))
			emit(Event{Kind: EvToolResult, Tool: use.ToolName, Result: json.RawMessage(payload)})
			a.recordFinding(ses.ID, use.ToolName, res)
		}
		messages = append(messages, llm.Message{Role: llm.RoleUser, Blocks: results})
		ses, _ = a.Store.Session(in.SessionID)
		env.Session = ses
		if stopped {
			continue
		}
	}

	// A tool budget can stop the loop while the model still gets one more turn to
	// answer with what it has. That answer is incomplete, and the person is not
	// told why unless we say so — the model was told, in a tool result it may or
	// may not repeat.
	if !explained {
		if why := Explain(lang, stop, budget); why != "" {
			answer = joinAnswer(answer, why)
			explained = true
		}
	}

	if strings.TrimSpace(answer) == "" {
		answer = sysMsg(lang, msgAnswerEmpty)
		if stop == StopAnswered {
			stop = StopFailed
		}
	}

	_ = a.Store.MutateSession(ses.ID, func(s *store.Session) error {
		s.History = append(s.History, store.Turn{
			Role: "assistant", Text: answer, Intent: string(dec.ID), At: time.Now().UTC(), RunID: runID,
		})
		s.Task.Step++
		return nil
	})

	rec.Info(obs.RunFinished, "turn finished", map[string]any{
		"stop_reason": string(stop), "iterations": budget.Iterations(),
		"tool_calls": budget.ToolCalls(), "redrafted": redrafts > 0,
		"output_tokens": usage.OutputTokens, "elapsed_ms": time.Since(start).Milliseconds(),
	})

	result := Result{
		RunID: runID, Intent: string(dec.ID), Route: dec, Answer: answer,
		StopReason: stop, Findings: findings, ToolCalls: toolRecords,
		Approvals: pending, Consents: consents, Usage: usage,
		Iterations: budget.Iterations(), Redrafted: redrafts > 0,
		ElapsedMS: time.Since(start).Milliseconds(), Events: rec.Events(),
	}
	emit(Event{Kind: EvFinal, Final: &result})
	return result, nil
}

// knownRefFor widens the corpus check with the live ids produced this turn.
func knownRefFor(corpusKnows func(string) bool, calls []guardrail.ToolCallRecord) func(string) bool {
	live := map[string]bool{}
	for _, c := range calls {
		ids, _ := c.Meta["live_ids"].([]string)
		for _, id := range ids {
			live[id] = true
		}
	}
	return func(ref string) bool { return corpusKnows(ref) || live[ref] }
}

// replyLanguage resolves which language this turn answers in.
//
// The session wins over the deployment default, because the person choosing a
// language in the interface is a stronger signal than an operator's setting -
// but the deployment default is what applies when they have not chosen.
func replyLanguage(cfg config.Config, ses *store.Session) string {
	if ses != nil && ses.Locale != "" {
		return ses.Locale
	}
	return cfg.ReplyLanguage
}

// verify runs the intent's own checks. It is a separate stage rather than an
// instruction in the prompt because "the model was told not to" is not a control.
func (a *Agent) verify(
	rec *obs.Recorder, in intent.Intent, ses *store.Session,
	answer string, calls []guardrail.ToolCallRecord,
) []guardrail.Finding {
	f := guardrail.Verify(in.Verifiers, guardrail.VerifyInput{
		Intent: string(in.ID), Answer: answer, Role: string(ses.Role),
		Locale:      replyLanguage(a.Cfg, ses),
		AccessNeeds: needStrings(ses.AccessNeeds), ToolCalls: calls,
		// Corpus ids plus anything a live lookup returned this turn. Live results
		// are not in the corpus, but they came back from a tool with a URL
		// attached — which is the property the check actually cares about.
		KnownRef: knownRefFor(a.Corpus.KnownRef, calls),
		ConsentGranted: func(scope string) bool {
			return a.Store.Consent(ses.SubjectID, domain.ConsentScope(scope)).Granted
		},
		KFloor: a.Cfg.KAnonymityFloor,
	})
	if len(f) == 0 {
		rec.Info(obs.VerifierPassed, "all verifiers passed",
			map[string]any{"verifiers": in.Verifiers})
		return nil
	}
	for _, x := range f {
		rec.Warn(obs.VerifierFailed, x.Code, x.Message,
			map[string]any{"severity": string(x.Severity), "evidence": x.Evidence})
	}
	return f
}

// blockMessage is what the person sees when a draft could not be delivered. It
// names the problem rather than pretending the turn simply produced nothing.
func codesOf(fs []guardrail.Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return out
}

func blockMessage(locale string, fs []guardrail.Finding) string {
	var b strings.Builder
	b.WriteString(sysMsg(locale, msgBlockHeader))
	b.WriteString("\n\n")
	seen := map[string]bool{}
	for _, f := range fs {
		if seen[f.Code] {
			continue // the same rule tripping on a redraft is one rule, not two
		}
		seen[f.Code] = true
		fmt.Fprintf(&b, "- %s\n", blockReason(locale, f.Code, f.Message))
	}
	b.WriteString("\n" + sysMsg(locale, msgBlockFooter))
	return b.String()
}

func (a *Agent) recordFinding(sessionID, tool string, res tools.Result) {
	summary, ref := summarise(tool, res)
	if summary == "" {
		return
	}
	_ = a.Store.MutateSession(sessionID, func(s *store.Session) error {
		s.Task.Findings = append(s.Task.Findings, store.Finding{Tool: tool, Summary: summary, SourceRef: ref})
		return nil
	})
}

// summarise keeps the short-term memory small. Storing whole tool payloads would
// make every later turn pay for every earlier retrieval.
func summarise(tool string, res tools.Result) (string, string) {
	m, ok := res.Content.(map[string]any)
	if !ok {
		return "", ""
	}
	switch tool {
	case "opportunity_search":
		rows, _ := m["results"].([]map[string]any)
		var ids []string
		for _, r := range rows {
			if id, ok := r["id"].(string); ok {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return "searched and found nothing", ""
		}
		return "found: " + strings.Join(ids, ", "), ""
	case "criteria_explain":
		id, _ := m["opportunity_id"].(string)
		unknown, _ := m["unknown_count"].(int)
		return fmt.Sprintf("read criteria for %s, %d still unknown", id, unknown), stringOf(m["source_ref"])
	case "gap_analysis":
		n, _ := m["suppressed_cells"].(int)
		return fmt.Sprintf("ran gap analysis grouped by %v, %d cells suppressed", m["group_by"], n), ""
	}
	return "", ""
}

// approvedForSession returns the approvals a human has actually granted in this
// session. A pending approval is, by definition, not permission to do anything.
func (a *Agent) approvedForSession(sessionID string) []store.PendingApproval {
	var out []store.PendingApproval
	for _, ap := range a.Store.ApprovalsFor(sessionID) {
		if ap.Decided && ap.Approved {
			out = append(out, ap)
		}
	}
	return out
}

func (a *Agent) fail(rec *obs.Recorder, runID string, start time.Time, err error) (Result, error) {
	return Result{
		RunID: runID, StopReason: StopFailed, ElapsedMS: time.Since(start).Milliseconds(),
		Events: rec.Events(),
	}, err
}

// buildHistory replays the conversation as plain text turns.
//
// Tool calls from earlier turns are deliberately NOT replayed: their outcomes
// are already summarised into the context layer, and replaying them would pay
// for the same retrieval on every subsequent turn.
func buildHistory(ses *store.Session, latest string) []llm.Message {
	const keep = 12
	h := ses.History
	// The latest user turn was already appended to history by the caller.
	if len(h) > 0 && h[len(h)-1].Role == "user" && h[len(h)-1].Text == latest {
		h = h[:len(h)-1]
	}
	if len(h) > keep {
		h = h[len(h)-keep:]
	}
	var out []llm.Message
	for _, t := range h {
		role := llm.RoleUser
		if t.Role == "assistant" {
			role = llm.RoleAssistant
		}
		if strings.TrimSpace(t.Text) == "" {
			continue
		}
		out = append(out, llm.Message{Role: role, Blocks: []llm.Block{llm.Text(t.Text)}})
	}
	out = append(out, llm.UserText(latest))
	return out
}

func toolDefinitions(ts []tools.Tool) []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(ts))
	for _, t := range ts {
		out = append(out, llm.ToolDef{
			Name: t.Name, Description: t.Description, InputSchema: t.Schema.JSONSchema(),
		})
	}
	return out
}

func needStrings(ns []domain.AccessNeed) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = string(n)
	}
	return out
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("SERIALISATION_FAILED: %v", err)
	}
	return string(b)
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

func joinAnswer(existing, extra string) string {
	if strings.TrimSpace(existing) == "" {
		return extra
	}
	if strings.TrimSpace(extra) == "" {
		return existing
	}
	return existing + "\n\n" + extra
}

func codeOf(msg string) string {
	if i := strings.Index(msg, ":"); i > 0 && i < 48 {
		return msg[:i]
	}
	return "TOOL_ERROR"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
