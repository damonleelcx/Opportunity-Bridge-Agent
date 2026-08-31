package intent_test

import (
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/guardrail"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/intent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tools"
)

// The intent registry is the single source of truth for routing, permissions,
// prompt assembly and the interface. These tests exist because a typo in it
// fails silently: a tool name that does not exist simply never gets offered, and
// a verifier name that does not exist simply never runs. Both look like the
// agent behaving oddly rather than like a configuration error.

func TestEveryAudienceHasAnIntent(t *testing.T) {
	// The product brief names four audiences. Each is an intent, not a prompt
	// paragraph - if one is deleted, this fails.
	//
	// talent_sourcing is the fifth and it is NOT from the brief: it was added
	// later, deliberately, to let employers reach people who opted in. It is
	// listed here so the same completeness checks apply to it, and so that
	// deleting it is a decision somebody makes rather than a line that rots.
	// Its own boundary is fenced separately - see TestRecruiterCannotReachResidentIntents
	// and internal/tools/recruiter_test.go.
	want := []intent.ID{
		intent.IndividualPathway,
		intent.LowAccessSupport,
		intent.ServiceOrchestration,
		intent.SupplyDemandInsight,
		intent.TalentSourcing,
	}
	for _, id := range want {
		in, ok := intent.Get(id)
		if !ok {
			t.Fatalf("intent %q is missing from the registry", id)
		}
		if in.Goal == "" {
			t.Errorf("%s: no goal declared", id)
		}
		if len(in.SuccessCriteria) == 0 {
			t.Errorf("%s: no success criteria; there is no way to say whether it worked", id)
		}
		if len(in.CanDo) == 0 || len(in.CannotDo) == 0 || len(in.EscalateWhen) == 0 {
			t.Errorf("%s: boundaries incomplete (can/cannot/escalate)", id)
		}
		if len(in.Workflow) != 5 {
			t.Errorf("%s: expected the five shared stages, got %d", id, len(in.Workflow))
		}
		if in.Directive == "" {
			t.Errorf("%s: no directive for the system prompt", id)
		}
		if in.MaxIterations <= 0 || in.MaxToolCalls <= 0 {
			t.Errorf("%s: budgets must be positive", id)
		}
	}
	if got := len(intent.All()); got != len(want) {
		t.Errorf("registry holds %d intents, expected exactly the %d listed above", got, len(want))
	}
}

func TestAllowedToolsExist(t *testing.T) {
	reg := tools.Default()
	for _, in := range intent.All() {
		if len(in.AllowedTools) == 0 {
			t.Errorf("%s: no tools allowed, so it can only talk", in.ID)
		}
		for _, name := range in.AllowedTools {
			if _, ok := reg.Get(name); !ok {
				t.Errorf("%s allows tool %q, which is not registered", in.ID, name)
			}
		}
	}
}

func TestVerifiersExist(t *testing.T) {
	registered := map[string]bool{}
	for _, n := range guardrail.VerifierNames() {
		registered[n] = true
	}
	for _, in := range intent.All() {
		if len(in.Verifiers) == 0 {
			t.Errorf("%s: no verifiers; its boundaries would be enforced by hope alone", in.ID)
		}
		for _, v := range in.Verifiers {
			if !registered[v] {
				t.Errorf("%s names verifier %q, which is not registered", in.ID, v)
			}
		}
	}
}

func TestRolesReachIntents(t *testing.T) {
	for _, r := range []domain.Role{domain.RoleResident, domain.RoleCaseworker, domain.RoleAnalyst} {
		if len(intent.ForRole(r)) == 0 {
			t.Errorf("role %q can reach no intent", r)
		}
	}
	// The insight intent reports on populations. A resident or a caseworker
	// reaching it would be a route into other people's data.
	if intent.Allows(intent.SupplyDemandInsight, domain.RoleResident) {
		t.Error("a resident must not reach supply_demand_insight")
	}
	if intent.Allows(intent.SupplyDemandInsight, domain.RoleCaseworker) {
		t.Error("a caseworker must not reach supply_demand_insight")
	}
	// The analyst must not reach anything that touches an individual record.
	for _, in := range intent.ForRole(domain.RoleAnalyst) {
		if in.ID != intent.SupplyDemandInsight {
			t.Errorf("an analyst reaches %q, which works on individual records", in.ID)
		}
	}
}

func TestInsightCannotReachIndividualTools(t *testing.T) {
	individualOnly := []string{
		"profile_upsert", "document_prepare", "application_submit",
		"case_task_create", "case_task_update", "case_task_list", "handoff_to_human",
	}
	for _, name := range individualOnly {
		if intent.ToolAllowed(intent.SupplyDemandInsight, name) {
			t.Errorf("supply_demand_insight may call %q, which reaches an individual's record", name)
		}
	}
}

func TestUnroutedIntentCanCallNothing(t *testing.T) {
	reg := tools.Default()
	for _, name := range reg.Names() {
		if intent.ToolAllowed(intent.Unknown, name) {
			t.Errorf("the unrouted state may call %q; before routing succeeds the only legal action is to ask", name)
		}
	}
}

// The verifier exists and passes its own unit tests, and none of that matters
// unless the intent actually names it: an unwired verifier is silent, which is
// exactly the state that left the "Open tasks" panel empty.
// See docs/bugfix/2026-08-28-subject-identity-and-tracked-steps.md
func TestIndividualPathwayRequiresTheStepToBeRecorded(t *testing.T) {
	in, ok := intent.Get(intent.IndividualPathway)
	if !ok {
		t.Fatal("individual_pathway is not in the registry")
	}
	for _, v := range in.Verifiers {
		if v == "next_step_is_tracked" {
			// The escape hatch the remedy names must be reachable, or the
			// redraft is told to call a tool this intent forbids.
			if !intent.ToolAllowed(intent.IndividualPathway, "case_task_update") {
				t.Error("the check tells the model to update an existing task, but this intent cannot call case_task_update")
			}
			return
		}
	}
	t.Error("individual_pathway does not run next_step_is_tracked; a step handed over in text only goes unrecorded")
}

// The delivery toggles sit in the chrome and are visible in EVERY session, so
// every intent has to be able to act on them.
//
// Turning "plain language" off does not flip a client-side setting: it sends a
// message asking the agent to change it, and the agent needs accessibility_set
// to do so. An intent without the tool leaves the model able to say "of course"
// and unable to do anything — the box reappears on the next load, having
// apparently been ignored. Three of the five intents were in exactly that state
// (service_orchestration, supply_demand_insight, talent_sourcing) and the
// toggle was dead in all of them.
//
// This is why universalTools exists, and why this test reads intent.All()
// rather than the literal: an intent cannot switch them off, and a new intent
// cannot forget to name them.
func TestEveryIntentCanChangeHowAnswersAreDelivered(t *testing.T) {
	reg := tools.Default()
	for _, in := range intent.All() {
		var found bool
		for _, name := range in.AllowedTools {
			if name == "accessibility_set" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s cannot call accessibility_set, so the delivery toggles are dead in it: "+
				"the person unticks a box, the model says yes, and nothing changes", in.ID)
		}
	}
	if _, ok := reg.Get("accessibility_set"); !ok {
		t.Fatal("accessibility_set is not registered; this fence no longer guards anything")
	}
}

// Merging the universal tools must not corrupt the per-intent lists.
//
// Appending straight onto a slice literal's backing array lets two intents share
// storage and silently overwrite each other's last entry, which would remove a
// tool from an intent that names it.
func TestUniversalToolsDoNotClobberTheIntentsOwnList(t *testing.T) {
	for _, in := range intent.All() {
		seen := map[string]int{}
		for _, name := range in.AllowedTools {
			seen[name]++
		}
		for name, n := range seen {
			if n > 1 {
				t.Errorf("%s lists %q %d times; the universal merge is duplicating entries", in.ID, name, n)
			}
		}
	}
	// The intents that named their own tools still have them.
	must := map[intent.ID][]string{
		intent.TalentSourcing:       {"candidate_search", "external_talent_scan", "outreach_request"},
		intent.SupplyDemandInsight:  {"gap_analysis"},
		intent.ServiceOrchestration: {"case_task_create", "document_prepare"},
	}
	for id, want := range must {
		in, ok := intent.Get(id)
		if !ok {
			t.Fatalf("%s is missing", id)
		}
		have := map[string]bool{}
		for _, n := range in.AllowedTools {
			have[n] = true
		}
		for _, n := range want {
			if !have[n] {
				t.Errorf("%s lost %q when the universal tools were merged in", id, n)
			}
		}
	}
}
