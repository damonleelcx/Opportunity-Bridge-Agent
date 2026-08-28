package tools

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/corpus"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/obs"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/retrieval"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

func testEnv(t *testing.T, role domain.Role, consent ...domain.ConsentScope) Env {
	t.Helper()
	c, err := corpus.Load("../../data")
	if err != nil {
		t.Fatalf("corpus: %v", err)
	}
	st := store.New("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	ses := st.CreateSession(role, "", "en")
	for _, s := range consent {
		st.SetConsent(ses.SubjectID, s, true, "test")
	}
	return Env{
		Cfg:   config.Config{KAnonymityFloor: 5, CorpusDir: "../../data"},
		Store: st, Corpus: c, Index: retrieval.NewIndex(c), Session: ses,
		Rec:       obs.NewRecorder("run_test", ses.ID),
		Approvals: map[string]store.PendingApproval{},
	}
}

func allowAll(string) bool { return true }

// Validation errors are read by the model. "invalid input" costs a round trip
// and often a second wrong guess; naming the field and the fix does not.
func TestValidationErrorsNameTheFieldAndTheFix(t *testing.T) {
	schema := Obj("test", map[string]*Schema{
		"query": StrMin("what to search for", 2),
		"kind":  Str("kind", "job", "training"),
		"limit": Int("how many", 1, 12),
	}, "query")

	_, err := Validate(schema, json.RawMessage(`{"kind":"internship","limit":99}`))
	if err == nil {
		t.Fatal("expected validation to fail")
	}
	msg := err.Error()
	for _, want := range []string{"REQUIRED_MISSING", "$.query", "ENUM_MISMATCH", "job, training", "ABOVE_MAXIMUM"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message is missing %q:\n%s", want, msg)
		}
	}

	_, err = Validate(schema, json.RawMessage(`{"query":"cnc","colour":"blue"}`))
	if err == nil || !strings.Contains(err.Error(), "UNKNOWN_FIELD") {
		t.Errorf("an invented field was accepted: %v", err)
	}
	if _, err := Validate(schema, json.RawMessage(`{"query":"cnc","limit":5}`)); err != nil {
		t.Errorf("a valid call was rejected: %v", err)
	}
}

func TestArgsHashIsStableAcrossKeyOrder(t *testing.T) {
	a := ArgsHash(json.RawMessage(`{"b":2,"a":1}`))
	b := ArgsHash(json.RawMessage(`{"a":1, "b":  2}`))
	if a != b {
		t.Errorf("key order changed the hash: %s vs %s", a, b)
	}
	if a == ArgsHash(json.RawMessage(`{"a":1,"b":3}`)) {
		t.Error("different arguments produced the same hash")
	}
}

func TestConsentIsCheckedBeforeTheToolRuns(t *testing.T) {
	env := testEnv(t, domain.RoleResident) // no consent granted
	reg := Default()
	res, err := reg.Call(context.Background(), env, allowAll, "profile_upsert",
		json.RawMessage(`{"city":"Chengdu"}`))
	if err == nil || !strings.Contains(err.Error(), "CONSENT_REQUIRED") {
		t.Fatalf("profile was written without permission: %v", err)
	}
	if res.Consent == nil {
		t.Error("the refusal did not carry the question to actually ask the person")
	}
	if p := env.Store.Profile(env.Session.SubjectID); p.City != "" {
		t.Error("the tool wrote anyway")
	}
}

// A resident reading their own tasks needs nobody's permission. The same read by
// staff does. A flat consent list cannot express that.
func TestPerRoleConsentOnRecordTools(t *testing.T) {
	reg := Default()

	resident := testEnv(t, domain.RoleResident)
	if _, err := reg.Call(context.Background(), resident, allowAll, "case_task_list", json.RawMessage(`{}`)); err != nil {
		t.Errorf("a resident was blocked from their own tasks: %v", err)
	}

	staff := testEnv(t, domain.RoleCaseworker)
	_, err := reg.Call(context.Background(), staff, allowAll, "case_task_list", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "CONSENT_REQUIRED") {
		t.Fatalf("staff read a resident's record without consent: %v", err)
	}

	staffOK := testEnv(t, domain.RoleCaseworker, domain.ConsentShareCaseworker)
	if _, err := reg.Call(context.Background(), staffOK, allowAll, "case_task_list", json.RawMessage(`{}`)); err != nil {
		t.Errorf("staff with consent were still blocked: %v", err)
	}
}

func TestGapAnalysisIsAnalystOnly(t *testing.T) {
	reg := Default()
	env := testEnv(t, domain.RoleCaseworker, domain.ConsentShareCaseworker)
	_, err := reg.Call(context.Background(), env, allowAll, "gap_analysis", json.RawMessage(`{"group_by":"district"}`))
	if err == nil || !strings.Contains(err.Error(), "TOOL_NOT_PERMITTED_FOR_ROLE") {
		t.Fatalf("a caseworker reached population data: %v", err)
	}
}

func TestGapAnalysisSuppressesSmallCells(t *testing.T) {
	env := testEnv(t, domain.RoleAnalyst)
	sigs, err := corpus.LoadSignals("../../data")
	if err != nil {
		t.Fatalf("signals: %v", err)
	}
	for _, s := range sigs {
		env.Store.RecordSignal(s)
	}
	reg := Default()
	res, err := reg.Call(context.Background(), env, allowAll, "gap_analysis",
		json.RawMessage(`{"city":"Chengdu","group_by":"blocker","kind":"subsidy"}`))
	if err != nil {
		t.Fatalf("gap_analysis: %v", err)
	}
	m := res.Content.(map[string]any)
	if m["suppressed_cells"].(int) < 1 {
		t.Error("a cell below the floor of 5 was not suppressed; the fixture contains one")
	}
	rows := m["rows"].([]map[string]any)
	for _, r := range rows {
		if r["records"].(int) < 5 {
			t.Errorf("row %v is below the anonymity floor", r["group"])
		}
	}
	if got, ok := res.Meta["suppressed_cells"].(int); !ok || got < 1 {
		t.Error("suppression was not declared in Meta, so the verifier cannot see it")
	}
}

func TestCriteriaExplainNeverReturnsAVerdict(t *testing.T) {
	env := testEnv(t, domain.RoleResident, domain.ConsentStoreProfile)
	reg := Default()
	res, err := reg.Call(context.Background(), env, allowAll, "criteria_explain",
		json.RawMessage(`{"opportunity_id":"sub-001"}`))
	if err != nil {
		t.Fatalf("criteria_explain: %v", err)
	}
	m := res.Content.(map[string]any)
	if _, ok := m["eligible"]; ok {
		t.Fatal("the tool returned an eligibility field; only the issuing authority decides")
	}
	for _, c := range m["checks"].([]map[string]any) {
		switch c["status"] {
		case "unknown", "possibly_met":
		default:
			t.Errorf("criterion %v has status %q; the strongest allowed claim is possibly_met",
				c["code"], c["status"])
		}
	}
}

func TestTaskCannotBeClosedWithoutEvidence(t *testing.T) {
	env := testEnv(t, domain.RoleResident, domain.ConsentStoreProfile)
	reg := Default()
	created, err := reg.Call(context.Background(), env, allowAll, "case_task_create",
		json.RawMessage(`{"domain":"training","title":"File the reimbursement","owner":"resident","channel_phone":"12333"}`))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := created.Meta["task_id"].(string)

	_, err = reg.Call(context.Background(), env, allowAll, "case_task_update",
		json.RawMessage(`{"task_id":"`+id+`","status":"done"}`))
	if err == nil || !strings.Contains(err.Error(), "EVIDENCE_REQUIRED") {
		t.Fatalf("a task was closed on nothing: %v", err)
	}
	_, err = reg.Call(context.Background(), env, allowAll, "case_task_update",
		json.RawMessage(`{"task_id":"`+id+`","status":"done","evidence":"receipt 20260827-114"}`))
	if err != nil {
		t.Errorf("a task with evidence was still refused: %v", err)
	}
}

func TestUnknownToolNamesTheAlternative(t *testing.T) {
	env := testEnv(t, domain.RoleResident)
	_, err := Default().Call(context.Background(), env, allowAll, "make_it_happen", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "TOOL_NOT_FOUND") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestEveryToolDeclaresAClosedSchema(t *testing.T) {
	// additionalProperties:false travels to the model. A tool that omits it lets
	// the model invent fields that silently do nothing.
	for _, name := range Default().Names() {
		tool, _ := Default().Get(name)
		if tool.Schema == nil || tool.Schema.Type != "object" {
			t.Errorf("%s: schema is not an object", name)
			continue
		}
		if tool.Schema.AdditionalProperties == nil || *tool.Schema.AdditionalProperties {
			t.Errorf("%s: schema does not close additionalProperties", name)
		}
		if tool.Description == "" {
			t.Errorf("%s: no description; the model has only the name to go on", name)
		}
		if tool.Risk == "" {
			t.Errorf("%s: no risk class, so the approval gate cannot see it", name)
		}
	}
}

// A tool with no required fields used to serialise as `"required": null`, which
// is not valid JSON Schema. The Claude API tolerated it; DeepSeek rejects the
// whole request — so one such tool took down every turn on that provider,
// including tools the turn never intended to call.
//
// This is a fence: it fails on any tool whose wire schema is malformed, not just
// the two that were broken.
func TestEveryToolSerialisesAValidWireSchema(t *testing.T) {
	reg := Default()
	for _, name := range reg.Names() {
		tool, _ := reg.Get(name)
		raw, err := json.Marshal(tool.Schema.JSONSchema())
		if err != nil {
			t.Errorf("%s: schema will not serialise: %v", name, err)
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		req, present := m["required"]
		if !present {
			t.Errorf("%s: no `required` key on the wire", name)
			continue
		}
		if req == nil {
			t.Errorf("%s: sends `\"required\": null`, which providers reject as not an array", name)
			continue
		}
		if _, ok := req.([]any); !ok {
			t.Errorf("%s: `required` is %T on the wire, want an array", name, req)
		}
		if props, ok := m["properties"].(map[string]any); !ok {
			t.Errorf("%s: `properties` is not an object on the wire", name)
		} else if len(props) != len(tool.Schema.Properties) {
			t.Errorf("%s: %d properties on the wire, schema declares %d", name, len(props), len(tool.Schema.Properties))
		}
		if m["additionalProperties"] != false {
			t.Errorf("%s: additionalProperties is %v on the wire, want false", name, m["additionalProperties"])
		}
	}
}

// The two tools that take no arguments are the ones that were broken; they get
// a named case so the regression cannot come back unnoticed.
func TestArgumentlessToolsHaveAnEmptyRequiredArray(t *testing.T) {
	for _, name := range []string{"case_task_list", "consent_check"} {
		tool, ok := Default().Get(name)
		if !ok {
			t.Fatalf("%s is missing from the registry", name)
		}
		got := tool.Schema.JSONSchema()["required"]
		arr, isSlice := got.([]string)
		if !isSlice || len(arr) != 0 {
			t.Errorf("%s: required is %#v, want an empty []string", name, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Regression fences for docs/bugfix/2026-08-28-consent-asked-twice.md
//
// A person granted "keep what you tell me", and was immediately shown the same
// card again. Both cards said "granted" because they clicked both. Reverting
// either rule below turns one of these tests red.

// Fence 1: the tool must read the store before it raises a card. Without this
// check the tool always returns a prompt, and the follow-up turn that granting
// sends ("I have granted store_profile, please continue") produces a second,
// identical card.
func TestConsentRequestRaisesNoCardWhenAlreadyGranted(t *testing.T) {
	env := testEnv(t, domain.RoleResident, domain.ConsentStoreProfile)
	r := Default()

	res, err := r.Call(context.Background(), env, allowAll, "consent_request",
		json.RawMessage(`{"scope":"store_profile","why":"to match without retyping"}`))
	if err != nil {
		t.Fatalf("consent_request on a held scope: %v", err)
	}
	if res.Consent != nil {
		t.Errorf("a card was raised for a permission already granted; the person is asked twice")
	}
	if got, _ := res.Meta["already_granted"].(bool); !got {
		t.Errorf("the model was not told the permission is already held: %+v", res.Meta)
	}
}

// Fence 2: the same call on a scope that is NOT held must still raise a card.
// A short-circuit that swallows every request would "fix" the duplicate by
// removing the feature.
func TestConsentRequestStillAsksWhenNotGranted(t *testing.T) {
	env := testEnv(t, domain.RoleResident)
	r := Default()

	res, err := r.Call(context.Background(), env, allowAll, "consent_request",
		json.RawMessage(`{"scope":"store_profile","why":"to match without retyping"}`))
	if err != nil {
		t.Fatalf("consent_request: %v", err)
	}
	if res.Consent == nil {
		t.Fatal("no card was raised for a permission that is not held; it can never be granted")
	}
	if res.Consent.Scope != domain.ConsentStoreProfile {
		t.Errorf("card scope %q, want store_profile", res.Consent.Scope)
	}
}

// Fence 3 for docs/bugfix/2026-08-28-subject-identity-and-tracked-steps.md:
// opportunity_search must report corpus hits separately from live-directory
// results. next_step_is_tracked reads corpus_hits, and folding the two together
// is what made it demand a task for a city whose only "result" was a website.
func TestOpportunitySearchSeparatesCorpusHitsFromLiveResults(t *testing.T) {
	env := testEnv(t, domain.RoleResident)
	r := Default()

	res, err := r.Call(context.Background(), env, allowAll, "opportunity_search",
		json.RawMessage(`{"query":"养老 护理","city":"成都"}`))
	if err != nil {
		t.Fatalf("opportunity_search: %v", err)
	}
	hits, ok := res.Meta["corpus_hits"].(int)
	if !ok {
		t.Fatalf("corpus_hits missing from Meta: %+v", res.Meta)
	}
	if hits == 0 {
		t.Fatal("the sample corpus has Chengdu care jobs; corpus_hits should not be zero")
	}
	total, _ := res.Meta["result_count"].(int)
	if total < hits {
		t.Errorf("result_count %d is below corpus_hits %d; the two are being confused", total, hits)
	}
}
