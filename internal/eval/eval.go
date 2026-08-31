// Package eval is the evaluation harness (steps 15 and 16 of the build flow).
//
// Two things make it worth having rather than a folder of prompts somebody runs
// by hand:
//
//   - Cases run through the real agent path - the real router, the real tool
//     registry, the real guardrails and verifiers. Only the model is substituted,
//     by a scripted client, so a case can pin down behaviour that a live model
//     would only produce occasionally: an invented program id, an eligibility
//     verdict, a task closed with no evidence, a tool called with bad arguments.
//   - Expectations are about outcomes the product cares about (which verifier
//     fired, whether anything irreversible happened, whether the answer carried a
//     citation), not about the wording of the answer. Asserting on wording makes
//     a suite that fails on every improvement.
package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/agent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/corpus"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/intent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/livesource"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/retrieval"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/talentsource"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tools"
)

// Category is why a case exists. Reporting per category is what stops a suite
// from looking healthy while every adversarial case quietly fails.
type Category string

const (
	CatSuccess     Category = "success"
	CatEdge        Category = "edge"
	CatAdversarial Category = "adversarial"
)

type Case struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"` // "turn" | "route"
	Category Category `json:"category"`
	Why      string   `json:"why"`

	Role    string `json:"role"`
	Message string `json:"message"`
	Pin     string `json:"pin_intent,omitempty"`

	Setup  Setup      `json:"setup,omitempty"`
	Script llm.Script `json:"script,omitempty"`
	// FollowUp, when set, runs a second turn on the same session and the same
	// script after any approval raised by the first turn has been decided. It is
	// how the human-in-the-loop path is tested end to end: raise, approve,
	// resume, and confirm the irreversible tool actually ran with the exact
	// arguments that were shown.
	FollowUp string      `json:"follow_up,omitempty"`
	Expect   Expectation `json:"expect"`
}

type Setup struct {
	Consent []string       `json:"consent,omitempty"`
	Profile domain.Profile `json:"profile,omitempty"`
	// Locale is the answer language for the case. Defaults to "en" so that the
	// English-scripted cases stay meaningful; set "zh-CN" to exercise the
	// language the deployment actually ships with.
	Locale string `json:"locale,omitempty"`
	// ApproveRaised decides every approval the first turn raises, before the
	// follow-up turn runs. false declines them, which is the other half of the
	// gate and just as important to test.
	ApproveRaised *bool `json:"approve_raised,omitempty"`
	// Pool seeds OTHER people into the store as opted-in candidates, which is
	// the only way a talent_sourcing case has anybody to find: Profile above
	// seeds the session's own subject, and for a recruiter that is the recruiter.
	//
	// Each one is granted ConsentDiscoverable, because a pool member who has not
	// opted in is invisible by design and would make every such case vacuous -
	// passing because nothing was searched rather than because the rules held.
	Pool []domain.Profile `json:"pool,omitempty"`
}

type Expectation struct {
	Intent          string   `json:"intent,omitempty"`
	StopReason      string   `json:"stop_reason,omitempty"`
	ToolsCalled     []string `json:"tools_called,omitempty"`
	ToolsNotCalled  []string `json:"tools_not_called,omitempty"`
	FindingsInclude []string `json:"findings_include,omitempty"`
	FindingsExclude []string `json:"findings_exclude,omitempty"`
	AnswerContains  []string `json:"answer_contains,omitempty"`
	AnswerExcludes  []string `json:"answer_excludes,omitempty"`
	Redrafted       *bool    `json:"redrafted,omitempty"`
	ApprovalRaised  *bool    `json:"approval_raised,omitempty"`
	// Error asserts that the case is REFUSED, and that the refusal names this
	// code. A permission boundary that cannot be tested for is not a boundary.
	Error string `json:"error_contains,omitempty"`
}

type CaseResult struct {
	Case     Case          `json:"case"`
	Passed   bool          `json:"passed"`
	Failures []string      `json:"failures,omitempty"`
	Err      string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
	Result   *agent.Result `json:"-"`
}

// Report is the reliability measurement (step 16).
type Report struct {
	Total        int                `json:"total"`
	Passed       int                `json:"passed"`
	ByCategory   map[Category]Tally `json:"by_category"`
	ByIntent     map[string]Tally   `json:"by_intent"`
	ToolAccuracy float64            `json:"tool_call_accuracy"`
	MedianMS     int64              `json:"median_ms"`
	P95MS        int64              `json:"p95_ms"`
	Cases        []CaseResult       `json:"cases"`
}

type Tally struct {
	Total  int `json:"total"`
	Passed int `json:"passed"`
}

// LoadCases reads a JSONL file. Blank lines and // comments are allowed so a
// dataset can explain itself next to the cases.
func LoadCases(path string) ([]Case, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("EVAL_READ_FAILED: cannot open %s: %w", path, err)
	}
	defer f.Close()
	return ReadCases(f, path)
}

func ReadCases(r io.Reader, name string) ([]Case, error) {
	var out []Case
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "//") {
			continue
		}
		var c Case
		if err := json.Unmarshal([]byte(text), &c); err != nil {
			return nil, fmt.Errorf("EVAL_PARSE_FAILED: %s line %d: %w", name, line, err)
		}
		if c.ID == "" {
			return nil, fmt.Errorf("EVAL_CASE_INVALID: %s line %d has no id", name, line)
		}
		if c.Kind == "" {
			c.Kind = "turn"
		}
		out = append(out, c)
	}
	return out, sc.Err()
}

// Runner builds one isolated agent per case, so no case can pass because a
// previous one left state behind.
type Runner struct {
	Corpus *corpus.Corpus
	Cfg    config.Config
	// LiveClient, when set, is used for "route" cases so routing can be measured
	// against the real classifier. Turn cases always use the case's own script.
	LiveClient llm.Client
	// Live is the out-of-corpus lookup, so cases exercise the same path the
	// deployment does.
	Live livesource.Provider
	// Talent is the external people-index lookup, so recruiter cases exercise the
	// same path the product runs. Nil - the default - means external_talent_scan
	// refuses, which is itself the behaviour most cases should see.
	Talent talentsource.Provider
}

func (r *Runner) Run(ctx context.Context, cases []Case) Report {
	rep := Report{
		ByCategory: map[Category]Tally{},
		ByIntent:   map[string]Tally{},
	}
	var durations []int64
	toolExpected, toolMatched := 0, 0

	for _, c := range cases {
		start := time.Now()
		res := r.runOne(ctx, c)
		res.Duration = time.Since(start)
		durations = append(durations, res.Duration.Milliseconds())

		rep.Total++
		if res.Passed {
			rep.Passed++
		}
		cat := rep.ByCategory[c.Category]
		cat.Total++
		if res.Passed {
			cat.Passed++
		}
		rep.ByCategory[c.Category] = cat

		key := c.Expect.Intent
		if key == "" {
			key = "(unspecified)"
		}
		ti := rep.ByIntent[key]
		ti.Total++
		if res.Passed {
			ti.Passed++
		}
		rep.ByIntent[key] = ti

		for _, want := range c.Expect.ToolsCalled {
			toolExpected++
			if res.Result != nil && calledTool(res.Result, want) {
				toolMatched++
			}
		}
		rep.Cases = append(rep.Cases, res)
	}
	if toolExpected > 0 {
		rep.ToolAccuracy = float64(toolMatched) / float64(toolExpected)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	if n := len(durations); n > 0 {
		rep.MedianMS = durations[n/2]
		rep.P95MS = durations[(n*95)/100%n]
	}
	return rep
}

func (r *Runner) runOne(ctx context.Context, c Case) CaseResult {
	res := CaseResult{Case: c}

	role := domain.Role(c.Role)
	if role == "" {
		role = domain.RoleResident
	}
	if !role.Valid() {
		res.Err = fmt.Sprintf("case declares role %q, which is not valid", c.Role)
		return res
	}

	// A memory-only store per case. State that survives between cases is the
	// classic way a suite starts passing for the wrong reason.
	st := store.New("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, s := range corpusSignals(r.Corpus, r.Cfg) {
		st.RecordSignal(s)
	}
	caseLocale := c.Setup.Locale
	if caseLocale == "" {
		caseLocale = "en"
	}
	ses := st.CreateSession(role, "", caseLocale)
	for _, scope := range c.Setup.Consent {
		st.SetConsent(ses.SubjectID, domain.ConsentScope(scope), true, "eval setup")
	}
	if c.Setup.Profile.City != "" || len(c.Setup.Profile.Skills) > 0 {
		p := c.Setup.Profile
		p.SubjectID = ses.SubjectID
		st.SaveProfile(p)
	}
	for i, p := range c.Setup.Pool {
		if p.SubjectID == "" {
			p.SubjectID = fmt.Sprintf("pool_%02d", i+1)
		}
		st.SaveProfile(p)
		st.SetConsent(p.SubjectID, domain.ConsentDiscoverable, true, "eval setup")
	}

	var client llm.Client
	if c.Kind == "route" {
		client = r.LiveClient
	} else {
		if len(c.Script.Turns) == 0 {
			res.Err = "turn case has no script; a turn case must pin the model's behaviour"
			return res
		}
		client = llm.NewScripted(c.Script)
	}

	if c.Kind == "route" {
		dec, err := intent.Route(ctx, client, r.Cfg.ClassifierModel, role, intent.ID(c.Pin), c.Message, "")
		if c.Expect.Error != "" {
			switch {
			case err == nil:
				res.Failures = append(res.Failures,
					fmt.Sprintf("expected refusal containing %q, but routing succeeded to %q", c.Expect.Error, dec.ID))
			case !strings.Contains(err.Error(), c.Expect.Error):
				res.Failures = append(res.Failures,
					fmt.Sprintf("refused with %q, expected it to contain %q", err.Error(), c.Expect.Error))
			}
			res.Passed = len(res.Failures) == 0
			return res
		}
		if err != nil {
			res.Err = err.Error()
			return res
		}
		if c.Expect.Intent != "" && string(dec.ID) != c.Expect.Intent {
			res.Failures = append(res.Failures,
				fmt.Sprintf("routed to %q via %s, expected %q", dec.ID, dec.Method, c.Expect.Intent))
		}
		res.Passed = len(res.Failures) == 0
		return res
	}

	ag := &agent.Agent{
		Cfg: r.Cfg, LLM: client, Store: st, Corpus: r.Corpus,
		Index: retrieval.NewIndex(r.Corpus), Tools: tools.Default(), Live: r.Live,
		Talent: r.Talent,
	}
	out, err := ag.Run(ctx, agent.Input{SessionID: ses.ID, Message: c.Message, Intent: intent.ID(c.Pin)})
	if err != nil {
		res.Err = err.Error()
		return res
	}

	if c.FollowUp != "" {
		// Decide anything the first turn raised, then resume. This is the real
		// sequence a person goes through, and the only way to prove that the
		// approval gate both blocks and then releases.
		approve := true
		if c.Setup.ApproveRaised != nil {
			approve = *c.Setup.ApproveRaised
		}
		for _, ap := range out.Approvals {
			if _, err := st.DecideApproval(ap.ID, approve, "eval decision"); err != nil {
				res.Err = err.Error()
				return res
			}
		}
		out, err = ag.Run(ctx, agent.Input{SessionID: ses.ID, Message: c.FollowUp, Intent: intent.ID(c.Pin)})
		if err != nil {
			res.Err = err.Error()
			return res
		}
	}
	res.Result = &out
	res.Failures = check(c.Expect, out)
	res.Passed = len(res.Failures) == 0
	return res
}

func corpusSignals(c *corpus.Corpus, cfg config.Config) []domain.DemandSignal {
	sigs, err := corpus.LoadSignals(cfg.CorpusDir)
	if err != nil {
		return nil
	}
	return sigs
}

func check(want Expectation, got agent.Result) []string {
	var fails []string
	if want.Intent != "" && got.Intent != want.Intent {
		fails = append(fails, fmt.Sprintf("intent: got %q, want %q", got.Intent, want.Intent))
	}
	if want.StopReason != "" && string(got.StopReason) != want.StopReason {
		fails = append(fails, fmt.Sprintf("stop_reason: got %q, want %q", got.StopReason, want.StopReason))
	}
	for _, tool := range want.ToolsCalled {
		if !calledTool(&got, tool) {
			fails = append(fails, fmt.Sprintf("tool %q was expected but not called successfully", tool))
		}
	}
	for _, tool := range want.ToolsNotCalled {
		if calledTool(&got, tool) {
			fails = append(fails, fmt.Sprintf("tool %q was called but must not be", tool))
		}
	}
	codes := map[string]bool{}
	for _, f := range got.Findings {
		codes[f.Code] = true
	}
	for _, code := range want.FindingsInclude {
		if !codes[code] {
			fails = append(fails, fmt.Sprintf("finding %q was expected but did not fire", code))
		}
	}
	for _, code := range want.FindingsExclude {
		if codes[code] {
			fails = append(fails, fmt.Sprintf("finding %q fired but must not", code))
		}
	}
	low := strings.ToLower(got.Answer)
	for _, s := range want.AnswerContains {
		if !strings.Contains(low, strings.ToLower(s)) {
			fails = append(fails, fmt.Sprintf("answer does not contain %q", s))
		}
	}
	for _, s := range want.AnswerExcludes {
		if strings.Contains(low, strings.ToLower(s)) {
			fails = append(fails, fmt.Sprintf("answer contains %q but must not", s))
		}
	}
	if want.Redrafted != nil && got.Redrafted != *want.Redrafted {
		fails = append(fails, fmt.Sprintf("redrafted: got %v, want %v", got.Redrafted, *want.Redrafted))
	}
	if want.ApprovalRaised != nil {
		raised := len(got.Approvals) > 0
		if raised != *want.ApprovalRaised {
			fails = append(fails, fmt.Sprintf("approval_raised: got %v, want %v", raised, *want.ApprovalRaised))
		}
	}
	return fails
}

func calledTool(r *agent.Result, name string) bool {
	for _, c := range r.ToolCalls {
		if c.Name == name && c.Err == "" {
			return true
		}
	}
	return false
}

// Text renders the report for a terminal. It leads with what failed, because a
// report that leads with a pass rate invites nobody to scroll.
func (r Report) Text() string {
	var b strings.Builder
	var failed []CaseResult
	for _, c := range r.Cases {
		if !c.Passed {
			failed = append(failed, c)
		}
	}
	if len(failed) > 0 {
		fmt.Fprintf(&b, "FAILED (%d)\n", len(failed))
		for _, c := range failed {
			fmt.Fprintf(&b, "  %-34s [%s] %s\n", c.Case.ID, c.Case.Category, c.Case.Why)
			if c.Err != "" {
				fmt.Fprintf(&b, "      error: %s\n", c.Err)
			}
			for _, f := range c.Failures {
				fmt.Fprintf(&b, "      - %s\n", f)
			}
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "%d/%d passed (%.0f%%)\n", r.Passed, r.Total, rate(r.Passed, r.Total))
	for _, cat := range []Category{CatSuccess, CatEdge, CatAdversarial} {
		if tl, ok := r.ByCategory[cat]; ok {
			fmt.Fprintf(&b, "  %-13s %d/%d (%.0f%%)\n", cat, tl.Passed, tl.Total, rate(tl.Passed, tl.Total))
		}
	}
	keys := make([]string, 0, len(r.ByIntent))
	for k := range r.ByIntent {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		tl := r.ByIntent[k]
		fmt.Fprintf(&b, "  %-24s %d/%d\n", k, tl.Passed, tl.Total)
	}
	fmt.Fprintf(&b, "  tool-call accuracy  %.0f%%\n", r.ToolAccuracy*100)
	fmt.Fprintf(&b, "  latency             median %dms, p95 %dms\n", r.MedianMS, r.P95MS)
	return b.String()
}

func rate(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d) * 100
}
