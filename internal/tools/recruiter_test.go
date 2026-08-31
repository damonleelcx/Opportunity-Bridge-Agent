package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/talentsource"
)

// These tests fence the one promise the recruiter role rests on: a recruiter
// learns a person's identity only when that person decides they should.
//
// Every check below is a way that promise could be broken by a plausible future
// edit - a field added to a struct, a filter added to a schema, a consent check
// moved for convenience. See docs/18-recruiter-and-outreach.md.

// seedCandidate puts one person in the store with a deliberately loaded profile:
// every protected attribute the domain can carry is set, so any leak shows up.
func seedCandidate(t *testing.T, env Env, subjectID, city string, skills []string, discoverable bool) {
	t.Helper()
	env.Store.SaveProfile(domain.Profile{
		SubjectID: subjectID,
		City:      city,
		HukouCity: "河南周口",
		Skills:    skills,
		Education: "高中",
		Interests: []string{"稳定的长白班"},
		Experience: []domain.Experience{{
			Title: "数控操作工", Years: 6, Sector: "制造",
			Details: "在东莞某厂，班组长王建国带过我，住宿舍 3 栋 402",
		}},
		Constraints: []string{"必须 17:00 前回家接孩子"},
		Cohorts:     []domain.CohortTag{domain.CohortMigrantWorker, domain.CohortCaregiver, domain.CohortOlderWorker},
		AccessNeeds: []domain.AccessNeed{domain.AccessPlainLanguage, domain.AccessDialect},
	})
	if discoverable {
		env.Store.SetConsent(subjectID, domain.ConsentDiscoverable, true, "test")
	}
}

func search(t *testing.T, env Env, args string) map[string]any {
	t.Helper()
	res, err := Default().Call(context.Background(), env, allowAll, "candidate_search", json.RawMessage(args))
	if err != nil {
		t.Fatalf("candidate_search: %v", err)
	}
	m, ok := res.Content.(map[string]any)
	if !ok {
		t.Fatalf("unexpected content shape %T", res.Content)
	}
	return m
}

// The most important test in the feature. A recruiter may see what somebody can
// do; they may never see who somebody is, where they are registered, what
// household duties they carry, or which support cohort they were tagged into.
//
// It asserts on the SERIALISED result, not on the struct, because that is what
// actually reaches the model and then the employer. A field added to
// candidateCard that nobody meant to expose fails here.
func TestCandidateCardNeverCarriesProtectedAttributes(t *testing.T) {
	env := testEnv(t, domain.RoleRecruiter)
	seedCandidate(t, env, "subj_cand", "成都", []string{"数控", "焊工"}, true)

	got := search(t, env, `{"skills":["数控"],"city":"成都"}`)
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)

	// Each of these is a real screening vector, not a hypothetical one.
	banned := map[string]string{
		"subj_cand":       "the subject id - the candidate_ref exists so this never travels",
		"河南周口":            "household registration (户籍) - hiring on it is discrimination",
		"migrant_worker":  "a cohort tag - these exist to ADD support, never to screen",
		"caregiver":       "a cohort tag revealing caregiving status",
		"older_worker":    "a cohort tag revealing age band",
		"plain_language":  "an access need - it is about serving them, not employing them",
		"dialect":         "an access need",
		"必须 17:00 前回家接孩子": "a constraint that reads as caregiving status",
		"王建国":             "a third party's name, from the free-text experience detail",
		"住宿舍 3 栋 402":     "an address, from the free-text experience detail",
	}
	for needle, why := range banned {
		if strings.Contains(body, needle) {
			t.Errorf("a recruiter can see %q\n  why that is wrong: %s\n  in: %s", needle, why, body)
		}
	}

	// And the things that SHOULD be there, so this cannot be passed by returning
	// nothing at all.
	for _, want := range []string{"cand_", "数控", "成都", "not_requested"} {
		if !strings.Contains(body, want) {
			t.Errorf("the card is missing %q, which a match needs: %s", want, body)
		}
	}
}

// The pool is opt-in. Somebody who never granted the scope is not in it, and no
// search term reaches them.
func TestSearchReturnsOnlyPeopleWhoOptedIn(t *testing.T) {
	env := testEnv(t, domain.RoleRecruiter)
	seedCandidate(t, env, "subj_in", "成都", []string{"数控"}, true)
	seedCandidate(t, env, "subj_out", "成都", []string{"数控"}, false)

	got := search(t, env, `{"skills":["数控"]}`)
	if n, _ := got["pool_size"].(int); n != 1 {
		t.Errorf("pool holds %d people, expected only the one who opted in", n)
	}
	if n, _ := got["matched"].(int); n != 1 {
		t.Errorf("%d matched, expected only the one who opted in", n)
	}
}

// "You can withdraw this at any time" has to be true on the next search, not at
// the next restart. Consent is read live for exactly this reason.
func TestWithdrawingDiscoverabilityEmptiesThePoolAndKillsTheRef(t *testing.T) {
	env := testEnv(t, domain.RoleRecruiter)
	seedCandidate(t, env, "subj_cand", "成都", []string{"数控"}, true)

	before := search(t, env, `{"skills":["数控"]}`)
	cards, _ := before["candidates"].([]candidateCard)
	if len(cards) != 1 {
		t.Fatalf("setup: expected one card, got %d", len(cards))
	}
	ref := cards[0].Ref

	env.Store.SetConsent("subj_cand", domain.ConsentDiscoverable, false, "withdrawn")

	after := search(t, env, `{"skills":["数控"]}`)
	if n, _ := after["pool_size"].(int); n != 0 {
		t.Errorf("pool still holds %d people after withdrawal", n)
	}
	// The ref they were found under must stop working too, or withdrawal only
	// hides them from search while leaving them reachable.
	//
	// The approval is granted first, on purpose. Registry.Call gates irreversible
	// tools BEFORE Run, so stopping at "approval required" would prove nothing
	// about whether the withdrawal is enforced - it would prove only that the
	// gate is in front of it. What matters is that the request is refused even
	// with a human's approval in hand, and that nothing is written either way.
	args := json.RawMessage(`{"candidate_ref":"` + ref + `","position":"数控操作工","org":"测试厂","message":"月薪 7000，长白班，成都新都区。"}`)
	env.Approvals["appr_test"] = store.PendingApproval{
		ID: "appr_test", SessionID: env.Session.ID, Tool: "outreach_request",
		Args: args, Approved: true,
	}
	_, err := Default().Call(context.Background(), env, allowAll, "outreach_request", args)
	if err == nil || !strings.Contains(err.Error(), "CANDIDATE_NOT_AVAILABLE") {
		t.Errorf("a withdrawn person is still reachable by their old ref: %v", err)
	}
	if got := env.Store.OutreachFor("subj_cand"); len(got) != 0 {
		t.Errorf("%d request(s) reached somebody who had withdrawn", len(got))
	}
}

// Two recruiters must not be able to tell they are looking at the same person.
// Without this, the pool is re-identifiable by intersecting search results.
func TestCandidateRefIsNotCorrelatableAcrossRecruiters(t *testing.T) {
	a := CandidateRef("recruiter_a", "subj_1")
	b := CandidateRef("recruiter_b", "subj_1")
	if a == b {
		t.Error("the same person has the same ref for two recruiters; comparing lists re-identifies them")
	}
	if a != CandidateRef("recruiter_a", "subj_1") {
		t.Error("the ref is not stable, so a two-step conversation cannot refer back to a card")
	}
	if strings.Contains(a, "subj_1") {
		t.Errorf("the ref embeds the subject id: %s", a)
	}
}

// Approaching a real person is irreversible, so it is gated on a human. This
// asserts nothing was written while the approval was pending - a gate that logs
// the request but creates it anyway is not a gate.
func TestOutreachRequestIsGatedOnHumanApproval(t *testing.T) {
	env := testEnv(t, domain.RoleRecruiter)
	seedCandidate(t, env, "subj_cand", "成都", []string{"数控"}, true)
	ref := CandidateRef(env.Session.SubjectID, "subj_cand")

	res, err := Default().Call(context.Background(), env, allowAll, "outreach_request",
		json.RawMessage(`{"candidate_ref":"`+ref+`","position":"数控操作工","org":"测试厂","message":"月薪 7000，长白班。"}`))
	if err == nil || !strings.Contains(err.Error(), "APPROVAL_REQUIRED") {
		t.Fatalf("an approach to a real person ran without approval: %v", err)
	}
	if res.Approval == nil {
		t.Error("no approval was raised for a human to decide")
	}
	if got := env.Store.OutreachFor("subj_cand"); len(got) != 0 {
		t.Errorf("%d request(s) were created while the approval was still pending", len(got))
	}
}

// The handshake: pending releases nothing, accepting releases exactly what the
// person typed, withdrawing closes it again.
func TestChannelMovesOnlyOnAcceptanceAndComesBack(t *testing.T) {
	rec := testEnv(t, domain.RoleRecruiter)
	seedCandidate(t, rec, "subj_cand", "成都", []string{"数控"}, true)
	ref := CandidateRef(rec.Session.SubjectID, "subj_cand")

	o := rec.Store.CreateOutreach(domain.Outreach{
		CandidateRef: ref, SubjectID: "subj_cand", RecruiterID: rec.Session.SubjectID,
		RecruiterOrg: "测试厂", Position: "数控操作工", Message: "月薪 7000。",
	})

	// While pending, the recruiter's own listing must carry no channel.
	listed, err := Default().Call(context.Background(), rec, allowAll, "outreach_list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("outreach_list: %v", err)
	}
	raw, _ := json.Marshal(listed.Content)
	if strings.Contains(string(raw), "13800000000") || strings.Contains(string(raw), "subj_cand") {
		t.Errorf("a pending request already exposes the person: %s", raw)
	}

	// The candidate answers, in their own session.
	cand := testEnv(t, domain.RoleResident)
	cand.Store = rec.Store
	cand.Session.SubjectID = "subj_cand"

	// Accepting without naming a contact must be refused, not silently accepted
	// with nothing - that would look like a yes to both sides and reach nobody.
	_, err = Default().Call(context.Background(), cand, allowAll, "outreach_respond",
		json.RawMessage(`{"outreach_id":"`+o.ID+`","decision":"accepted"}`))
	if err == nil || !strings.Contains(err.Error(), "CONTACT_REQUIRED") {
		t.Errorf("an acceptance with no contact detail was allowed: %v", err)
	}

	if _, err := Default().Call(context.Background(), cand, allowAll, "outreach_respond",
		json.RawMessage(`{"outreach_id":"`+o.ID+`","decision":"accepted","contact":"13800000000"}`)); err != nil {
		t.Fatalf("accept: %v", err)
	}
	after, _ := rec.Store.Outreach(o.ID)
	if after.Channel.Phone != "13800000000" {
		t.Errorf("acceptance did not release the contact the person gave: %+v", after.Channel)
	}

	// Withdrawal closes the channel again. Consent that cannot be taken back was
	// never consent.
	if _, err := Default().Call(context.Background(), cand, allowAll, "outreach_respond",
		json.RawMessage(`{"outreach_id":"`+o.ID+`","decision":"withdrawn"}`)); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	final, _ := rec.Store.Outreach(o.ID)
	if final.Channel.Phone != "" {
		t.Errorf("withdrawal left the contact detail in place: %+v", final.Channel)
	}
}

// Answering somebody else's request is the easy bug here and an invisible one
// afterwards, so the store checks the addressee rather than trusting the caller.
func TestOnlyTheAddresseeCanAnswerARequest(t *testing.T) {
	env := testEnv(t, domain.RoleRecruiter)
	o := env.Store.CreateOutreach(domain.Outreach{
		CandidateRef: "cand_x", SubjectID: "subj_cand", RecruiterID: "rec_1",
		Position: "数控操作工", Message: "月薪 7000。",
	})
	if _, err := env.Store.DecideOutreach(o.ID, "somebody_else", domain.OutreachAccepted,
		domain.Channel{Phone: "13800000000"}, ""); err == nil ||
		!strings.Contains(err.Error(), "OUTREACH_NOT_YOURS") {
		t.Errorf("a stranger answered somebody else's request: %v", err)
	}
	if _, err := env.Store.DecideOutreach(o.ID, "subj_cand", domain.OutreachAccepted,
		domain.Channel{Phone: "13800000000"}, ""); err != nil {
		t.Fatalf("the addressee could not answer: %v", err)
	}
	if _, err := env.Store.DecideOutreach(o.ID, "subj_cand", domain.OutreachDeclined,
		domain.Channel{}, ""); err == nil || !strings.Contains(err.Error(), "OUTREACH_ALREADY_DECIDED") {
		t.Errorf("a decided request was answered twice: %v", err)
	}
}

// The schema is the enforcement point for "these are not fields here": a model
// asked to screen on age or 户籍 cannot smuggle it through as an argument.
func TestSearchSchemaOffersNoProtectedFilters(t *testing.T) {
	tool, ok := Default().Get("candidate_search")
	if !ok {
		t.Fatal("candidate_search is not registered")
	}
	for name := range tool.Schema.Properties {
		switch name {
		case "skills", "city", "sectors", "min_years", "limit":
		default:
			t.Errorf("candidate_search offers a %q filter; the pool has no protected fields to filter on", name)
		}
	}
	for _, bad := range []string{
		`{"skills":["数控"],"age_max":35}`,
		`{"skills":["数控"],"hukou":"成都"}`,
		`{"skills":["数控"],"cohorts":["migrant_worker"]}`,
		`{"skills":["数控"],"gender":"male"}`,
	} {
		if _, err := Validate(tool.Schema, json.RawMessage(bad)); err == nil {
			t.Errorf("a protected filter was accepted: %s", bad)
		}
	}
}

// A resident must not be able to search the pool, and a recruiter must not be
// able to answer on somebody's behalf. Both are enforced by Tool.Roles.
func TestRolesCannotReachEachOthersTools(t *testing.T) {
	resident := testEnv(t, domain.RoleResident)
	if _, err := Default().Call(context.Background(), resident, allowAll, "candidate_search",
		json.RawMessage(`{"skills":["数控"]}`)); err == nil ||
		!strings.Contains(err.Error(), "TOOL_NOT_PERMITTED_FOR_ROLE") {
		t.Errorf("a resident searched the candidate pool: %v", err)
	}
	recruiter := testEnv(t, domain.RoleRecruiter)
	if _, err := Default().Call(context.Background(), recruiter, allowAll, "outreach_respond",
		json.RawMessage(`{"outreach_id":"out_0001","decision":"accepted","contact":"13800000000"}`)); err == nil ||
		!strings.Contains(err.Error(), "TOOL_NOT_PERMITTED_FOR_ROLE") {
		t.Errorf("a recruiter answered on a candidate's behalf: %v", err)
	}
}

// Every test above grants ConsentDiscoverable by calling Store.SetConsent
// directly, which bypasses domain.IsConsentScope - and that is exactly how this
// whole feature can be inert while all of them stay green.
//
// It happened. The scope was declared before the tools that read it existed, so
// it was correctly withheld from ConsentScopes() as a permission nothing
// consumed. Once candidate_search landed the premise was gone, but nothing
// failed: POST /api/consent would reject "discoverable_by_employers", the
// consent card could never be raised, nobody could opt in, and candidate_search
// would return an empty pool forever - reporting pool_size 0, which reads as
// "nobody matched" rather than "this feature is switched off".
//
// This is the fence for the reachable path rather than the storable one.
// See docs/18-recruiter-and-outreach.md.
func TestTheScopeThePoolNeedsIsOneAPersonCanActuallyGrant(t *testing.T) {
	scope := string(domain.ConsentDiscoverable)
	if !domain.IsConsentScope(scope) {
		t.Errorf("%s is not a scope the API accepts, so nobody can opt in and the pool is permanently empty", scope)
	}
	if domain.NotYetOffered(scope) {
		t.Errorf("%s is marked as withheld while candidate_search reads it; the pool cannot fill", scope)
	}
	offered := false
	for _, s := range domain.ConsentScopes() {
		if s == domain.ConsentDiscoverable {
			offered = true
		}
	}
	if !offered {
		t.Errorf("%s is not in ConsentScopes(), so the interface renders no control to grant or withdraw it", scope)
	}
	// And it has to be askable in words, or the model can only name the raw id.
	p := ConsentPromptFor(domain.ConsentDiscoverable)
	if p == nil || p.Plain == "" || p.Retention == "" {
		t.Errorf("%s has no plain-language prompt; a person would be asked to agree to an identifier", scope)
	}
}

// With no vendor configured, the scan must REFUSE. An empty result would read as
// "nobody like that exists anywhere" - the most misleading answer this tool can
// give, and the one an employer would act on by walking away.
func TestExternalScanRefusesWhenNoVendorIsConfigured(t *testing.T) {
	env := testEnv(t, domain.RoleRecruiter) // Env.Talent is nil
	_, err := Default().Call(context.Background(), env, allowAll, "external_talent_scan",
		json.RawMessage(`{"skills":["cnc"],"country":"china"}`))
	if err == nil || !strings.Contains(err.Error(), "EXTERNAL_SCAN_NOT_CONFIGURED") {
		t.Errorf("an unconfigured scan returned an answer instead of refusing: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "unknown") {
		t.Errorf("the refusal must tell the model to say UNKNOWN rather than small: %v", err)
	}
}

// External leads are not reachable through this service. outreach_request takes
// a candidate_ref from the pool, and a lead id must not resolve to one.
func TestExternalLeadsCannotBeApproachedThroughThisService(t *testing.T) {
	env := testEnv(t, domain.RoleRecruiter)
	seedCandidate(t, env, "subj_cand", "成都", []string{"数控"}, true)
	args := json.RawMessage(`{"candidate_ref":"lead-001","position":"数控操作工","org":"测试厂","message":"月薪 7000，长白班。"}`)
	env.Approvals["appr_lead"] = store.PendingApproval{
		ID: "appr_lead", SessionID: env.Session.ID, Tool: "outreach_request", Args: args, Approved: true,
	}
	_, err := Default().Call(context.Background(), env, allowAll, "outreach_request", args)
	if err == nil || !strings.Contains(err.Error(), "CANDIDATE_NOT_AVAILABLE") {
		t.Errorf("a vendor lead id was accepted as somebody this service can approach: %v", err)
	}
	if got := env.Store.OutreachFor("subj_cand"); len(got) != 0 {
		t.Errorf("a lead id created an outreach against a pool member: %d", len(got))
	}
}

// stubTalent is a Provider whose Chain-shaped result is fixed, so the tool's own
// handling of a partial answer can be tested without a network.
type stubTalent struct {
	found talentsource.Found
	err   error
}

func (s stubTalent) Name() string { return "stub" }
func (s stubTalent) Find(context.Context, talentsource.Query) (talentsource.Found, error) {
	return s.found, s.err
}

// One vendor answering ZERO is a result and must reach the reader, even when the
// other vendor failed and the combined total is therefore 0.
//
// Regression fence for a defect a live run found: the tool bailed whenever the
// total was zero, which collapsed "every vendor was unreachable" into the same
// outcome as "a vendor answered, and the answer was none". The recruiter was
// then told the scan used only the vendor that had FAILED, because the one that
// worked had been discarded. See docs/18-recruiter-and-outreach.md.
func TestPartialScanKeepsTheVendorThatAnsweredZero(t *testing.T) {
	env := testEnv(t, domain.RoleRecruiter)
	env.Talent = stubTalent{
		found: talentsource.Found{
			PerVendor: []talentsource.VendorResult{
				{Name: "People Data Labs", Total: 0, Caveat: "skills fill rate is low"},
				{Name: "Apollo.io", Error: "APOLLO_HTTP_403: not included in your Free plan"},
			},
		},
		err: fmt.Errorf("PROVIDER_PARTIAL: Apollo.io: APOLLO_HTTP_403"),
	}
	res, err := Default().Call(context.Background(), env, allowAll, "external_talent_scan",
		json.RawMessage(`{"skills":["cnc"],"country":"china"}`))
	if err != nil {
		t.Fatalf("a vendor answered zero, so the scan must return a result: %v", err)
	}
	body, _ := json.Marshal(res.Content)
	for _, want := range []string{"People Data Labs", "Apollo.io", "Free plan"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the breakdown lost %q, so the reader cannot tell which index was searched: %s", want, body)
		}
	}
	if !strings.Contains(string(body), "incomplete") {
		t.Errorf("a partial scan must say it is partial: %s", body)
	}
}

// When NOTHING answered, the tool must still refuse — an empty result there
// really does read as "nobody like that exists anywhere".
func TestScanRefusesWhenEveryVendorFailed(t *testing.T) {
	env := testEnv(t, domain.RoleRecruiter)
	env.Talent = stubTalent{
		found: talentsource.Found{
			PerVendor: []talentsource.VendorResult{
				{Name: "People Data Labs", Error: "PDL_UNREACHABLE"},
				{Name: "Apollo.io", Error: "APOLLO_HTTP_403"},
			},
		},
		err: fmt.Errorf("PROVIDER_PARTIAL: both down"),
	}
	if _, err := Default().Call(context.Background(), env, allowAll, "external_talent_scan",
		json.RawMessage(`{"skills":["cnc"]}`)); err == nil {
		t.Error("with no vendor answering, a result would read as 'nobody like that exists'")
	}
}
