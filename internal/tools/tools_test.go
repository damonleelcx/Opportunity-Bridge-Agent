package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/corpus"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/livesource"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/obs"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/retrieval"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

func testEnv(t *testing.T, role domain.Role, consent ...domain.ConsentScope) Env {
	t.Helper()
	c, err := corpus.Load("../../testdata/corpus")
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

// recordingLive is a live provider that answers nothing and remembers what it
// was asked. What reaches it is the whole point of the fences below.
type recordingLive struct{ got []livesource.Query }

func (r *recordingLive) Name() string { return "recording" }
func (r *recordingLive) Lookup(_ context.Context, q livesource.Query) ([]livesource.Result, error) {
	r.got = append(r.got, q)
	return nil, nil
}

// Fence for docs/bugfix/2026-08-28-live-search-never-looked-for-training.md:
// the kinds the caller asked the corpus for must reach the live lookup.
//
// This is the wire that did not exist. livesource could be taught to search for
// courses and it would still never do so, because opportunity_search built its
// live Query from the city and the keyword alone — so the web was asked about
// 招聘 whatever the person wanted. A unit test inside livesource cannot catch
// that: every one of them passes an intent by hand.
func TestOpportunitySearchTellsTheLiveLookupWhatKindWasAskedFor(t *testing.T) {
	for _, tc := range []struct {
		name string
		args string
		want []livesource.Intent
	}{
		{"a training question searches the web for courses",
			`{"query":"养老护理","city":"佛山","kinds":["training"]}`,
			[]livesource.Intent{livesource.IntentTraining}},
		{"a job question still searches for work only",
			`{"query":"焊工","city":"佛山","kinds":["job"]}`,
			[]livesource.Intent{livesource.IntentWork}},
		{"asking for everything asks the web for everything",
			`{"query":"焊工","city":"佛山"}`,
			[]livesource.Intent{livesource.IntentWork, livesource.IntentTraining}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t, domain.RoleResident)
			live := &recordingLive{}
			env.Live = live

			if _, err := Default().Call(context.Background(), env, allowAll,
				"opportunity_search", json.RawMessage(tc.args)); err != nil {
				t.Fatalf("opportunity_search: %v", err)
			}
			if len(live.got) != 1 {
				t.Fatalf("the live provider was called %d times, want once — "+
					"佛山 has no local listings, so the lookup must run", len(live.got))
			}
			if fmt.Sprint(live.got[0].Intents) != fmt.Sprint(tc.want) {
				t.Fatalf("live lookup asked for %v, want %v",
					live.got[0].Intents, tc.want)
			}
		})
	}
}

// The whole wire, end to end: a training question in a city the corpus does not
// cover comes back with a course, labelled as one, carrying the course warning.
//
// Every other fence for this tests one link. This one runs the real chain — the
// tool builds the query, IntentsFor maps the kinds, Bocha shapes the search and
// judges the pages — against a stub standing in for the vendor. It is the
// closest thing to an end-to-end run that works without a search key, and it is
// what would have caught the original defect on its own: each link was
// individually fine and the chain still could not return a course.
func TestATrainingQuestionOutsideTheCorpusComesBackWithACourse(t *testing.T) {
	// Both pages are in the right city and about the right trade. Which one
	// survives is decided entirely by the intent.
	const body = `{"code":200,"data":{"webPages":{"value":[
	 {"name":"佛山养老护理员招聘 月薪6000","url":"https://example.com/job","snippet":"佛山养老护理岗位","siteName":"鱼泡","datePublished":"2026-08-20T00:00:00+08:00"},
	 {"name":"佛山养老护理员证培训班招生","url":"https://example.com/course","snippet":"佛山养老护理培训 学费可申请补贴","siteName":"某技工学校","datePublished":"2026-08-18T00:00:00+08:00"}
	]}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	b := livesource.NewBocha(srv.URL, "test-key")
	b.Now = func() time.Time { return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC) }

	env := testEnv(t, domain.RoleResident)
	env.Live = livesource.Chain{b}

	res, err := Default().Call(context.Background(), env, allowAll, "opportunity_search",
		json.RawMessage(`{"query":"养老护理","city":"佛山","kinds":["training"]}`))
	if err != nil {
		t.Fatalf("opportunity_search: %v", err)
	}
	live, _ := res.Content.(map[string]any)["live_results"].([]livesource.Result)
	if len(live) != 1 {
		t.Fatalf("got %d live results, want the course only: %+v", len(live), live)
	}
	got := live[0]
	if !strings.Contains(got.Title, "培训") {
		t.Errorf("a training question returned %q", got.Title)
	}
	if got.Intent != livesource.IntentTraining {
		t.Errorf("intent = %q, want training", got.Intent)
	}
	if !strings.Contains(got.Caveat, "培训贷") {
		t.Errorf("the course carries the job-scam warning instead of the course one: %q", got.Caveat)
	}
	// The id has to be citable this turn, or the invented-identifier check
	// rejects the very lead the lookup just produced.
	ids, _ := res.Meta["live_ids"].([]string)
	if len(ids) != 1 || ids[0] != got.ID {
		t.Errorf("live_ids = %v, but the result is %q", ids, got.ID)
	}
}

// Two searches in one turn must not both produce a live-003.
//
// The agent searches more than once as a matter of course — once per trade when
// somebody names two, once per intent when it wants both work and courses.
// Observed in production on 2026-08-28: one answer cited live-003 for a welding
// school and for a cookery school. The id is the reader's only handle on an
// unverified lead, and the invented-identifier check cannot object because both
// really were produced this turn.
//
// The unit test in livesource proves the counter counts. This one proves the
// counter is WIRED — that opportunity_search shares one across the turn, which
// is the part that was missing. See
// docs/bugfix/2026-08-28-live-ids-collided-within-a-turn.md
func TestLiveIDsAreUniqueAcrossOneTurn(t *testing.T) {
	const body = `{"code":200,"data":{"webPages":{"value":[
	 {"name":"佛山电焊工培训班招生","url":"https://example.test/weld","snippet":"佛山电焊培训报名","siteName":"某校","datePublished":"2026-08-20T00:00:00+08:00"},
	 {"name":"佛山中式烹调师培训招生","url":"https://example.test/cook","snippet":"佛山烹饪培训学费","siteName":"某校","datePublished":"2026-08-18T00:00:00+08:00"}
	]}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	b := livesource.NewBocha(srv.URL, "test-key")
	b.Now = func() time.Time { return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC) }

	// One Env for both calls, exactly as the agent loop reuses it across a turn.
	env := testEnv(t, domain.RoleResident)
	env.Live = livesource.Chain{b}
	env.LiveSeq = &livesource.Sequence{}
	reg := Default()

	seen := map[string]string{}
	for _, args := range []string{
		`{"query":"电焊","city":"佛山","kinds":["training"]}`,
		`{"query":"烹饪","city":"佛山","kinds":["training"]}`,
	} {
		res, err := reg.Call(context.Background(), env, allowAll, "opportunity_search",
			json.RawMessage(args))
		if err != nil {
			t.Fatalf("opportunity_search %s: %v", args, err)
		}
		live, _ := res.Content.(map[string]any)["live_results"].([]livesource.Result)
		if len(live) == 0 {
			t.Fatalf("%s returned no live results; this fence needs some to number", args)
		}
		for _, r := range live {
			if r.ID == "" {
				t.Fatalf("a live result came back with no id: %q", r.Title)
			}
			if prev, dup := seen[r.ID]; dup {
				t.Fatalf("id %q was handed to both %q and %q in one turn; "+
					"it identifies nothing and the reader cannot ask about either",
					r.ID, prev, r.Title)
			}
			seen[r.ID] = r.Title
		}
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
