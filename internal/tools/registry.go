package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/corpus"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/guardrail"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/obs"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/retrieval"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

// Risk classifies what a tool can do to the world, which is what decides
// whether it needs a human in front of it (step 18).
type Risk string

const (
	// RiskRead touches nothing outside the process.
	RiskRead Risk = "read"
	// RiskWrite changes our own records. Reversible, logged, no gate.
	RiskWrite Risk = "write"
	// RiskIrreversible leaves our boundary or cannot be taken back. Always gated
	// on an explicit human approval of these exact arguments.
	RiskIrreversible Risk = "irreversible"
)

// Env is everything a tool may reach. Passing it explicitly, rather than
// letting tools hold package-level state, is what makes a tool testable and
// makes the permission checks in Call the only way in.
type Env struct {
	Cfg     config.Config
	Store   *store.Store
	Corpus  *corpus.Corpus
	Index   *retrieval.Index
	Session *store.Session
	Rec     *obs.Recorder
	// Approvals holds approval ids granted for this run, keyed by id. A tool
	// with RiskIrreversible executes only if one of these matches its own name
	// and an exact hash of its arguments.
	Approvals map[string]store.PendingApproval
}

// Result is what a tool hands back.
type Result struct {
	// Content is serialised into the tool_result the model reads.
	Content any
	// Meta is read by verifiers, never by the model. Tools declare facts about
	// their own execution here so checks do not have to parse prose.
	Meta map[string]any
	// Findings are guardrail observations raised during execution.
	Findings []guardrail.Finding
	// Approval, when set, means nothing was done: a human must decide first.
	Approval *store.PendingApproval
	// Consent, when set, asks the UI to render a consent request card.
	Consent *ConsentPrompt
	// Signal, when set, is a de-identified demand observation to record. It is
	// written only if the subject granted aggregation consent - checked by Call,
	// not by the tool, so no tool can forget.
	Signal *domain.DemandSignal
}

// ConsentPrompt is the plain-language question a person is actually asked.
type ConsentPrompt struct {
	Scope     domain.ConsentScope `json:"scope"`
	Title     string              `json:"title"`
	Plain     string              `json:"plain"`
	WhatFor   string              `json:"what_for"`
	Retention string              `json:"retention"`
}

type Tool struct {
	Name        string
	Description string
	Schema      *Schema
	Risk        Risk
	// Consent lists scopes that must be granted before the tool runs at all.
	Consent []domain.ConsentScope
	// RoleConsent adds scopes that are required only for particular actors.
	//
	// Why per-role: a resident reading their own tracked tasks needs no
	// permission from anyone. The same read by a caseworker needs the resident's
	// share_with_caseworker consent. A single flat Consent list cannot express
	// that difference, and expressing it inside each tool's Run would put the
	// permission check somewhere a reviewer has to hunt for it.
	RoleConsent map[domain.Role][]domain.ConsentScope
	// Roles restricts a tool to particular actors, independently of the intent
	// allowlist. Empty means any role the intent permits.
	Roles []domain.Role
	Run   func(ctx context.Context, env Env, args map[string]any) (Result, error)
}

type Registry struct {
	byName map[string]Tool
	order  []string
}

func NewRegistry(ts ...Tool) *Registry {
	r := &Registry{byName: map[string]Tool{}}
	for _, t := range ts {
		r.byName[t.Name] = t
		r.order = append(r.order, t.Name)
	}
	sort.Strings(r.order)
	return r
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

func (r *Registry) Names() []string { return append([]string(nil), r.order...) }

// Subset returns the tools an intent is allowed to expose to the model. The
// model is never shown a tool it may not call: a refused call it can see is a
// refusal it will retry.
func (r *Registry) Subset(names []string) []Tool {
	var out []Tool
	for _, n := range names {
		if t, ok := r.byName[n]; ok {
			out = append(out, t)
		}
	}
	return out
}

// Call is the single entry point. Everything that must be true before a tool
// runs is checked here, in a fixed order, so no tool can be reached another way:
//
//	exists -> allowed for this intent -> allowed for this role -> arguments
//	valid -> consent held -> irreversible ones approved -> Run.
func (r *Registry) Call(
	ctx context.Context, env Env, intentAllows func(string) bool,
	name string, raw json.RawMessage,
) (Result, error) {
	t, ok := r.byName[name]
	if !ok {
		return Result{}, fmt.Errorf("TOOL_NOT_FOUND: no tool named %q; available tools are listed in this turn's tool definitions", name)
	}
	if intentAllows != nil && !intentAllows(name) {
		return Result{}, fmt.Errorf("TOOL_NOT_PERMITTED_FOR_INTENT: %q is not available for the current intent; "+
			"if the person's need has changed, say so and the conversation will be re-routed", name)
	}
	if len(t.Roles) > 0 {
		allowed := false
		for _, role := range t.Roles {
			if env.Session != nil && env.Session.Role == role {
				allowed = true
				break
			}
		}
		if !allowed {
			return Result{}, fmt.Errorf("TOOL_NOT_PERMITTED_FOR_ROLE: %q may only be used by %v; the current session is %q",
				name, t.Roles, env.Session.Role)
		}
	}
	args, err := Validate(t.Schema, raw)
	if err != nil {
		return Result{}, err
	}
	required := append([]domain.ConsentScope(nil), t.Consent...)
	if env.Session != nil {
		required = append(required, t.RoleConsent[env.Session.Role]...)
	}
	for _, scope := range required {
		g := env.Store.Consent(env.Session.SubjectID, scope)
		env.Rec.Info(obs.ConsentChecked, "consent checked before tool call",
			map[string]any{"tool": name, "scope": string(scope), "granted": g.Granted})
		if !g.Granted {
			return Result{
					Consent: consentPromptFor(scope),
				}, fmt.Errorf("CONSENT_REQUIRED: %q needs the %q permission, which has not been granted. "+
					"Explain in plain words what would be stored and why, then ask. Do not retry until it is granted", name, scope)
		}
	}
	if t.Risk == RiskIrreversible {
		appr, ok := findApproval(env, name, raw)
		if !ok {
			pending := env.Store.CreateApproval(store.PendingApproval{
				SessionID: env.Session.ID,
				Tool:      name,
				Args:      raw,
				Summary:   fmt.Sprintf("Run %s with the arguments shown.", name),
				Impact:    irreversibleImpact(name),
			})
			env.Rec.Warn(obs.ApprovalRequired, "APPROVAL_REQUIRED",
				"an irreversible tool was requested and is waiting for a human decision",
				map[string]any{"tool": name, "approval_id": pending.ID})
			return Result{Approval: &pending},
				fmt.Errorf("APPROVAL_REQUIRED: %q is irreversible and nothing has been done. "+
					"Approval %s is now waiting. Show the person exactly what will happen and stop this turn; "+
					"do not call this tool again until you are told the approval was granted", name, pending.ID)
		}
		env.Rec.Info(obs.ApprovalGranted, "irreversible tool proceeding on a granted approval",
			map[string]any{"tool": name, "approval_id": appr.ID})
	}

	res, err := t.Run(ctx, env, args)
	if err != nil {
		return res, err
	}
	if res.Signal != nil {
		// Aggregation consent is enforced here, once, rather than in each tool.
		if g := env.Store.Consent(env.Session.SubjectID, domain.ConsentAggregate); g.Granted {
			env.Store.RecordSignal(*res.Signal)
		} else {
			res.Findings = append(res.Findings, guardrail.Finding{
				Guard: "consent", Code: "SIGNAL_NOT_RECORDED", Severity: guardrail.Advisory,
				Message: "A demand signal was produced but not recorded: this subject has not granted aggregation consent.",
			})
		}
		res.Signal = nil
	}
	return res, nil
}

// findApproval requires the approval to be granted, for this tool, and for a
// byte-exact hash of these arguments. Approving a summary and then running
// something else is the failure this guards against.
func findApproval(env Env, name string, raw json.RawMessage) (store.PendingApproval, bool) {
	want := ArgsHash(raw)
	for _, a := range env.Approvals {
		if a.Tool == name && a.Approved && ArgsHash(a.Args) == want {
			return a, true
		}
	}
	return store.PendingApproval{}, false
}

// ArgsHash canonicalises the arguments before hashing so that key order or
// whitespace cannot make an approved call look different.
func ArgsHash(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:8])
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:8])
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:8])
}

func irreversibleImpact(name string) string {
	switch name {
	case "application_submit":
		return "Sends an application to an external authority on this person's behalf. It cannot be recalled, " +
			"and a wrong or duplicate filing can cost them the attempt."
	}
	return "This action leaves our boundary and cannot be undone."
}

func consentPromptFor(scope domain.ConsentScope) *ConsentPrompt {
	switch scope {
	case domain.ConsentStoreProfile:
		return &ConsentPrompt{
			Scope: scope, Title: "Keep what you tell me",
			Plain:     "May I save your skills, your city and what you told me about your situation?",
			WhatFor:   "So you do not have to type it again next time, and so matching can use it.",
			Retention: "Kept until you ask me to delete it. You can see everything I hold and correct it.",
		}
	case domain.ConsentShareCaseworker:
		return &ConsentPrompt{
			Scope: scope, Title: "Let a caseworker see this",
			Plain:     "May a staff member at the service window see your record and work on it with you?",
			WhatFor:   "So you do not have to retell your story at every counter.",
			Retention: "Only staff handling your case. You can withdraw this at any time.",
		}
	case domain.ConsentSubmitOnBehalf:
		return &ConsentPrompt{
			Scope: scope, Title: "File on your behalf",
			Plain:     "May I file an application for you? You will still see and approve each one before it is sent.",
			WhatFor:   "So a filing does not fail because of a form field.",
			Retention: "Each filing is shown to you in full and needs your approval separately.",
		}
	case domain.ConsentAggregate:
		return &ConsentPrompt{
			Scope: scope, Title: "Count me in the statistics",
			Plain:     "May your search be counted, without your name, in figures about what people in your area need?",
			WhatFor:   "So that where jobs or support are missing becomes visible, and services can be moved there.",
			Retention: "No name, no id, no way back to you. Small groups are never reported.",
		}
	}
	return &ConsentPrompt{Scope: scope, Title: string(scope)}
}
