package tools_test

// Fences over 猎源图谱 as a capability of this agent.
//
// The claim: it reaches only the employer intent and only the recruiter role,
// the founding sentence still holds over it, and the guarantees it enforces on
// its own side survive being translated into this package's schema.

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/intent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tools"
)

func leadGraphIntent(t *testing.T) intent.Intent {
	t.Helper()
	in, ok := intent.Get(intent.TalentSourcing)
	if !ok {
		t.Fatal("talent_sourcing is gone")
	}
	return in
}

// Every tool the adapter exposes is on the employer intent's allowlist, and on
// no other intent's. A name in one and not the other is a tool the model is
// offered and cannot call, or one it can call from somewhere it should not.
func TestLeadGraphToolsReachOnlyTheEmployerIntent(t *testing.T) {
	names := tools.LeadGraphToolNames()
	if len(names) == 0 {
		t.Fatal("no lead graph tools exposed - this test would prove nothing")
	}
	allowed := map[string]bool{}
	for _, n := range leadGraphIntent(t).AllowedTools {
		allowed[n] = true
	}
	for _, n := range names {
		if !allowed[n] {
			t.Errorf("%s is exposed but not on talent_sourcing's allowlist", n)
		}
	}
	for _, id := range []intent.ID{
		intent.IndividualPathway, intent.LowAccessSupport,
		intent.ServiceOrchestration, intent.SupplyDemandInsight,
	} {
		in, _ := intent.Get(id)
		for _, n := range in.AllowedTools {
			for _, lg := range names {
				if n == lg {
					t.Errorf("%s reaches %s: a resident's intent can touch a recruiter's contact book", n, id)
				}
			}
		}
	}
}

// And the role gate underneath the allowlist. An intent misroute must not be
// enough to hand somebody else's private book to a resident.
func TestLeadGraphToolsAreRecruiterOnly(t *testing.T) {
	reg := tools.Default()
	for _, n := range tools.LeadGraphToolNames() {
		tool, ok := reg.Get(n)
		if !ok {
			t.Fatalf("%s is exposed but not registered", n)
		}
		if len(tool.Roles) != 1 || tool.Roles[0] != domain.RoleRecruiter {
			t.Errorf("%s is reachable by %v, not just the recruiter", n, tool.Roles)
		}
	}
}

// The founding sentence still holds over this intent. Lead Graph scores
// SITUATIONS - a lead's subject is an organisational unit - so the verifier
// that forbids ranking people stays on, and stays true.
func TestTheEmployerIntentStillRefusesToScorePeople(t *testing.T) {
	var found bool
	for _, v := range leadGraphIntent(t).Verifiers {
		if v == "no_candidate_scoring" {
			found = true
		}
	}
	if !found {
		t.Error("no_candidate_scoring was dropped from talent_sourcing when the graph arrived")
	}
}

// The translation has to preserve the closure. additionalProperties:false is
// the whole of "a model cannot smuggle in a match score", and losing it in a
// type conversion would be invisible.
func TestTheTranslatedSchemasStayClosed(t *testing.T) {
	reg := tools.Default()
	for _, n := range tools.LeadGraphToolNames() {
		tool, _ := reg.Get(n)
		if tool.Schema == nil {
			t.Errorf("%s lost its schema in translation", n)
			continue
		}
		if tool.Schema.AdditionalProperties == nil || *tool.Schema.AdditionalProperties {
			t.Errorf("%s accepts arbitrary fields after translation", n)
		}
		if tool.Description == "" {
			t.Errorf("%s lost its description, so the model is guessing", n)
		}
		// And nested objects too - a closed top level over an open nested one
		// is the same hole one layer down.
		for field, sub := range tool.Schema.Properties {
			checkClosed(t, n+"."+field, sub)
		}
	}
}

func checkClosed(t *testing.T, path string, s *tools.Schema) {
	t.Helper()
	if s == nil {
		return
	}
	if s.Type == "object" && (s.AdditionalProperties == nil || *s.AdditionalProperties) {
		t.Errorf("%s is an open object after translation", path)
	}
	for k, v := range s.Properties {
		checkClosed(t, path+"."+k, v)
	}
	checkClosed(t, path+"[]", s.Items)
}

// A deployment with no graph database says so, rather than failing obscurely or
// pretending to have recorded something.
func TestWithoutAGraphTheToolsSaySo(t *testing.T) {
	reg := tools.Default()
	tool, _ := reg.Get("record_turn")
	_, err := tool.Run(context.Background(), tools.Env{
		Session: &store.Session{SubjectID: "acct_1"},
	}, map[string]any{"turn_ref": "t1"})
	if err == nil {
		t.Fatal("a call with no graph reported success")
	}
	if !strings.Contains(err.Error(), "GRAPH_UNAVAILABLE") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

// The seat is the account. Everything private in Lead Graph - notes, relationship
// strengths, "only the seat that was asked may answer" - is about one person, so
// attributing a turn to the wrong one would quietly merge two people's books.
func TestTheSeatIsTheAccount(t *testing.T) {
	graph := leadgraph.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	reg := tools.Default()
	tool, _ := reg.Get("record_turn")

	run := func(subject string) {
		t.Helper()
		if _, err := tool.Run(context.Background(), tools.Env{
			Graph: graph, Session: &store.Session{SubjectID: subject},
		}, map[string]any{
			"turn_ref": "t1",
			"candidates": []any{map[string]any{
				"kind": "person", "label": "王五", "org": "A司",
				"intel": []any{map[string]any{"kind": "user_said", "excerpt": "跟王五聊了"}},
			}},
		}); err != nil {
			t.Fatalf("record_turn as %s: %v", subject, err)
		}
	}
	run("acct_amy")
	run("acct_ben")

	amy := graph.Nodes(leadgraph.View{TeamID: "acct_amy", SeatID: "acct_amy"}, leadgraph.NodeFilter{})
	ben := graph.Nodes(leadgraph.View{TeamID: "acct_ben", SeatID: "acct_ben"}, leadgraph.NodeFilter{})
	if len(amy) != 1 || len(ben) != 1 {
		t.Fatalf("each account should hold its own record: amy=%d ben=%d", len(amy), len(ben))
	}
	if amy[0].ID == ben[0].ID {
		t.Error("two accounts share one record: their books were merged")
	}
}

// A request with no account has nowhere to attribute the graph to, and must not
// fall back to a shared one.
func TestAnAnonymousRequestGetsNoGraph(t *testing.T) {
	graph := leadgraph.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	tool, _ := tools.Default().Get("graph_query")
	if _, err := tool.Run(context.Background(), tools.Env{Graph: graph}, map[string]any{}); err == nil {
		t.Error("a request with no account read the graph")
	}
}

// The team is the firm an OPERATOR placed the account in, never a string the
// account chose. A self-declared org is not an identity: type a competitor's
// name and you are inside their contact book.
func TestTheTeamIsTheOperatorSetOrgNotSomethingTyped(t *testing.T) {
	st := store.New(t.TempDir()+"/state.json", slog.New(slog.NewTextHandler(io.Discard, nil)))
	graph := leadgraph.New(slog.New(slog.NewTextHandler(io.Discard, nil)))

	mk := func(user string) string {
		t.Helper()
		a, err := st.CreateAccount(user, "hash")
		if err != nil {
			t.Fatalf("create %s: %v", user, err)
		}
		return a.SubjectID
	}
	amy, ben, mallory := mk("amy"), mk("ben"), mk("mallory")
	for _, u := range []string{"amy", "ben"} {
		if err := st.SetAccountOrg(u, "Acme猎头"); err != nil {
			t.Fatalf("place %s: %v", u, err)
		}
	}

	record := func(subject, label string) {
		t.Helper()
		tool, _ := tools.Default().Get("record_turn")
		if _, err := tool.Run(context.Background(), tools.Env{
			Graph: graph, Store: st, Session: &store.Session{SubjectID: subject},
		}, map[string]any{
			"turn_ref": "t1",
			"candidates": []any{map[string]any{
				"kind": "person", "label": label, "org": "A司",
				"intel": []any{map[string]any{"kind": "user_said", "excerpt": "聊过"}},
			}},
		}); err != nil {
			t.Fatalf("record as %s: %v", subject, err)
		}
	}
	record(amy, "王五")
	record(mallory, "李四") // not in the firm

	read := func(subject string) []leadgraph.Node {
		t.Helper()
		tool, _ := tools.Default().Get("graph_query")
		got, err := tool.Run(context.Background(), tools.Env{
			Graph: graph, Store: st, Session: &store.Session{SubjectID: subject},
		}, map[string]any{})
		if err != nil {
			t.Fatalf("query as %s: %v", subject, err)
		}
		ns, _ := got.Content.([]leadgraph.Node)
		return ns
	}

	// A colleague in the same firm sees the fact - the team half is awake.
	if got := read(ben); len(got) != 1 || got[0].Label != "王五" {
		t.Fatalf("a teammate cannot see a team fact: %+v", got)
	}
	// Somebody outside it sees only their own.
	got := read(mallory)
	if len(got) != 1 || got[0].Label != "李四" {
		t.Fatalf("an outsider saw the firm's book: %+v", got)
	}
	// An account with no firm is a team of one.
	solo := mk("solo")
	if len(read(solo)) != 0 {
		t.Error("an unplaced account can read somebody else's graph")
	}
}

// Every irreversible Lead Graph tool is gated by THIS product's approval flow,
// the same one that stops application_submit.
func TestTheIrreversibleGraphToolsAreGatedLikeEverythingElse(t *testing.T) {
	reg := tools.Default()
	want := map[string]bool{"import_commit": true, "graph_forget": true, "subject_request": true}
	seen := 0
	for _, n := range tools.LeadGraphToolNames() {
		tool, _ := reg.Get(n)
		if want[n] {
			seen++
			if tool.Risk != tools.RiskIrreversible {
				t.Errorf("%s is %s: it would run without anybody seeing the arguments", n, tool.Risk)
			}
		}
	}
	if seen != len(want) {
		t.Fatalf("only %d of the irreversible tools are exposed", seen)
	}
}

// The upload route and the tools must agree about which team a session is in.
// Disagreeing would stage a file into one graph and import it into another.
func TestTheUploadRouteAndTheToolsAgreeOnTheTeam(t *testing.T) {
	st := store.New(t.TempDir()+"/state.json", slog.New(slog.NewTextHandler(io.Discard, nil)))
	a, err := st.CreateAccount("amy", "hash")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.SetAccountOrg("amy", "Acme猎头"); err != nil {
		t.Fatalf("place: %v", err)
	}
	graph := leadgraph.New(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// What the tools compute.
	tool, _ := tools.Default().Get("record_turn")
	if _, err := tool.Run(context.Background(), tools.Env{
		Graph: graph, Store: st, Session: &store.Session{SubjectID: a.SubjectID},
	}, map[string]any{
		"turn_ref": "t1",
		"candidates": []any{map[string]any{
			"kind": "person", "label": "王五", "org": "A司",
			"intel": []any{map[string]any{"kind": "user_said", "excerpt": "聊过"}},
		}},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	// What the upload route computes - through the SAME exported helper the
	// route uses, not a copy of the rule spelled out here. Spelling it out was
	// the first version, and a mutation of the route's own function left it
	// perfectly green.
	v := leadgraph.View{TeamID: tools.GraphTeamFor(st, a.SubjectID), SeatID: a.SubjectID}
	if got := graph.Nodes(v, leadgraph.NodeFilter{}); len(got) != 1 {
		t.Fatalf("the route and the tools disagree about the team: route sees %d records", len(got))
	}
}
