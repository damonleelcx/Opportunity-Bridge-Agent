package leadgraph_test

// Fences over the model-facing tool surface.
//
// The claim: a model cannot get past this layer with an argument the tool never
// promised to accept, and cannot perform an irreversible action without a human
// having approved that exact call.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

func call(t *testing.T, s *leadgraph.Store, v leadgraph.View, name string, args map[string]any, ap ...leadgraph.Approval) (any, error) {
	t.Helper()
	return leadgraph.Tools().Call(s, v, name, args, ap)
}

// Every tool closes its object. Forgetting that is the mistake that lets a
// model pass a field nobody designed for.
func TestEveryToolRefusesFieldsItNeverPromised(t *testing.T) {
	r := leadgraph.Tools()
	if len(r.Names()) == 0 {
		t.Fatal("no tools at all")
	}
	for _, name := range r.Names() {
		tool, _ := r.Get(name)
		if tool.Schema.AdditionalProperties == nil || *tool.Schema.AdditionalProperties {
			t.Errorf("%s accepts arbitrary fields", name)
		}
		if tool.Description == "" {
			t.Errorf("%s has no description, so the model is guessing", name)
		}
		if tool.Run == nil {
			t.Errorf("%s has no implementation", name)
		}
	}
}

// §08 — the concrete thing the closed schema prevents.
func TestAScoreCannotBeSmuggledIntoACall(t *testing.T) {
	s := newStore(t)
	_, err := call(t, s, amy, "graph_query", map[string]any{"org": "A司", "匹配度": 87})
	if err == nil || !strings.Contains(err.Error(), "BAD_ARGUMENTS") {
		t.Fatalf("want BAD_ARGUMENTS, got %v", err)
	}
	if !strings.Contains(err.Error(), "匹配度") {
		t.Errorf("the refusal does not name the offending field: %v", err)
	}
}

// Validation happens BEFORE Run. A call that fails it must leave no trace.
func TestABadCallNeverReachesTheStore(t *testing.T) {
	s := newStore(t)
	_, err := call(t, s, amy, "record_turn", map[string]any{
		"turn_ref": "t1",
		"candidates": []any{map[string]any{
			"kind": "person", "label": "王五", // no intel: required
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "BAD_ARGUMENTS") {
		t.Fatalf("want BAD_ARGUMENTS, got %v", err)
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{})); n != 0 {
		t.Errorf("a refused call wrote %d records", n)
	}
}

// An enum is a promise about what the model may say.
func TestAnEnumIsEnforced(t *testing.T) {
	s := newStore(t)
	_, err := call(t, s, amy, "graph_query", map[string]any{"kind": "candidate"})
	if err == nil || !strings.Contains(err.Error(), "BAD_ARGUMENTS") {
		t.Errorf("an invented node kind was accepted: %v", err)
	}
	if _, err := call(t, s, amy, "path_find", map[string]any{"target_id": "x", "max_hops": 99}); err == nil {
		t.Error("a hop count outside the declared range was accepted")
	}
}

// The happy path: a turn recorded through the tool surface behaves exactly like
// one recorded directly, receipt and all.
func TestATurnRecordedThroughTheToolsBehavesTheSame(t *testing.T) {
	s := newStore(t)
	got, err := call(t, s, amy, "record_turn", map[string]any{
		"turn_ref": "turn_7",
		"candidates": []any{map[string]any{
			"kind": "person", "label": "王五", "org": "A司", "role_title": "组长",
			"unit_path": []any{"A司", "c业务组"},
			"intel": []any{map[string]any{
				"kind": "user_said", "excerpt": "我今天跟A司的王五聊了", "turn_ref": "turn_7",
			}},
		}},
	})
	if err != nil {
		t.Fatalf("record_turn: %v", err)
	}
	res, ok := got.(leadgraph.TurnResult)
	if !ok {
		t.Fatalf("unexpected result type %T", got)
	}
	if res.Receipt == nil || res.Receipt.Created != 1 {
		t.Fatalf("no receipt for a recorded fact: %+v", res.Receipt)
	}
	ns := s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson})
	if len(ns) != 1 || ns[0].RoleTitle != "组长" {
		t.Fatalf("the fact did not land: %+v", ns)
	}
	if ns[0].Intel[0].TurnRef != "turn_7" {
		t.Errorf("provenance lost through the tool layer: %+v", ns[0].Intel[0])
	}
}

// §05.2 — an irreversible tool needs a human's yes to THESE arguments.
func TestAnIrreversibleToolNeedsAnApprovalBoundToItsArguments(t *testing.T) {
	s := newStore(t)
	keep := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	other := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "张三"))

	args := map[string]any{"node_id": keep.ID}
	if _, err := call(t, s, amy, "graph_forget", args); err == nil ||
		!strings.Contains(err.Error(), "APPROVAL_MISSING") {
		t.Fatalf("want APPROVAL_MISSING, got %v", err)
	}
	if _, ok := s.Node(amy, keep.ID); !ok {
		t.Fatal("the unapproved deletion happened anyway")
	}

	// An approval for a DIFFERENT call is not an approval for this one.
	wrong := leadgraph.ApprovalFor("graph_forget", map[string]any{"node_id": other.ID})
	if _, err := call(t, s, amy, "graph_forget", args, wrong); err == nil ||
		!strings.Contains(err.Error(), "APPROVAL_MISSING") {
		t.Fatalf("an approval for another record let this one through: %v", err)
	}
	if _, ok := s.Node(amy, keep.ID); !ok {
		t.Fatal("the record was deleted under somebody else's approval")
	}

	right := leadgraph.ApprovalFor("graph_forget", args)
	if _, err := call(t, s, amy, "graph_forget", args, right); err != nil {
		t.Fatalf("an approved deletion was refused: %v", err)
	}
	if _, ok := s.Node(amy, keep.ID); ok {
		t.Error("the approved deletion did not happen")
	}
}

// Read tools need no approval - gating them would teach callers to approve
// everything, which is how an approval stops meaning anything.
func TestReadToolsNeedNoApproval(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	for _, name := range []string{"graph_query", "lead_board", "stale_scan"} {
		if _, err := call(t, s, amy, name, map[string]any{}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The tool surface obeys the same boundaries as everything else.
func TestToolsStopAtTheTeamBoundary(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	got, err := call(t, s, cara, "graph_query", map[string]any{})
	if err != nil {
		t.Fatalf("graph_query: %v", err)
	}
	if ns, _ := got.([]leadgraph.Node); len(ns) != 0 {
		t.Errorf("another team read %d records through the tools", len(ns))
	}
}

// An unknown tool is refused by name rather than ignored.
func TestAnUnknownToolIsRefused(t *testing.T) {
	if _, err := call(t, newStore(t), amy, "candidate_search", nil); err == nil ||
		!strings.Contains(err.Error(), "UNKNOWN_TOOL") {
		t.Errorf("want UNKNOWN_TOOL, got %v", err)
	}
}

// The schema is what gets sent to a model, so it has to serialise to real JSON
// Schema with the closure intact.
func TestSchemasSerialiseAsJSONSchema(t *testing.T) {
	tool, _ := leadgraph.Tools().Get("touchpoint_add")
	raw, err := json.Marshal(tool.Schema)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, want := range []string{`"type":"object"`, `"additionalProperties":false`, `"required":`, `"person_id"`} {
		if !strings.Contains(body, want) {
			t.Errorf("schema is missing %s: %s", want, body)
		}
	}
}

// §05.2 — the split survives at this layer: the tool that judges cannot write.
func TestTheReconcileToolWritesNothing(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "王五"), "A司", "c业务组"))
	before := len(s.Nodes(amy, leadgraph.NodeFilter{}))

	got, err := call(t, s, amy, "graph_reconcile", map[string]any{
		"candidates": []any{map[string]any{
			"kind": "person", "label": "王总", "org": "A司", "unit_path": []any{"A司", "c业务组"},
			"intel": []any{map[string]any{"kind": "user_said", "excerpt": "王总"}},
		}},
	})
	if err != nil {
		t.Fatalf("graph_reconcile: %v", err)
	}
	if ps, _ := got.([]leadgraph.Proposal); len(ps) != 1 || ps[0].Decision != leadgraph.DecideMerge {
		t.Fatalf("unexpected proposals: %+v", got)
	}
	if after := len(s.Nodes(amy, leadgraph.NodeFilter{})); after != before {
		t.Errorf("the read-only tool wrote: %d -> %d", before, after)
	}
	tool, _ := leadgraph.Tools().Get("graph_reconcile")
	if tool.Risk != leadgraph.RiskRead {
		t.Errorf("graph_reconcile is declared %s", tool.Risk)
	}
}

// A contact recorded through the tools lands as a team fact with the private
// half kept private.
func TestTouchpointThroughTheToolsSplitsFactFromImpression(t *testing.T) {
	s := newStore(t)
	p := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "王五"))
	if _, err := call(t, s, amy, "touchpoint_add", map[string]any{
		"person_id": p.ID, "at": "2026-09-01", "via": "phone",
		"topic": "问了下他们组的情况", "outcome": "他说等岗位开了通知", "note": "他其实挺想动的",
	}); err != nil {
		t.Fatalf("touchpoint_add: %v", err)
	}
	mine := s.Touchpoints(amy, p.ID)
	if len(mine) != 1 || mine[0].Outcome == "" || mine[0].Note == "" {
		t.Fatalf("the contact did not land: %+v", mine)
	}
	theirs := s.Touchpoints(ben, p.ID)
	if len(theirs) != 1 || theirs[0].Outcome == "" {
		t.Fatal("the team lost the fact")
	}
	if theirs[0].Note != "" {
		t.Errorf("the private impression reached a teammate: %q", theirs[0].Note)
	}
	if !mine[0].At.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("the date was mangled: %v", mine[0].At)
	}
}

// The MODEL must be able to draw a line, through the real schema.
//
// The store could always draw one; nothing the model could say ever did.
// record_turn took only node candidates, so an agent that heard "他俩是前同事"
// had no field to put it in. This drives the tool exactly as a model would —
// schema validation and all — and then asks the graph whether a line exists.
func TestTheModelCanDrawARelationshipThroughRecordTurn(t *testing.T) {
	s := newStore(t)
	_, err := call(t, s, amy, "record_turn", map[string]any{
		"turn_ref": "t1",
		"candidates": []any{
			map[string]any{"kind": "person", "label": "张三", "org": "鲸峰科技",
				"intel": []any{map[string]any{"kind": "user_said", "excerpt": "张三在鲸峰"}}},
			map[string]any{"kind": "person", "label": "赵六", "org": "星轨智能",
				"intel": []any{map[string]any{"kind": "user_said", "excerpt": "赵六在星轨"}}},
		},
		"links": []any{
			map[string]any{
				"kind":    "colleague_of",
				"from":    map[string]any{"label": "张三", "org": "鲸峰科技"},
				"to":      map[string]any{"label": "赵六", "org": "星轨智能"},
				"context": "前同事",
				"intel":   []any{map[string]any{"kind": "user_said", "excerpt": "他俩是前同事"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("record_turn: %v", err)
	}
	if n := len(s.Edges(amy, leadgraph.EdgeFilter{})); n != 1 {
		t.Fatalf("the model's call left %d lines in the graph", n)
	}
}

// And it still cannot smuggle a strength in through the link.
func TestTheModelCannotRateARelationshipThroughALink(t *testing.T) {
	s := newStore(t)
	_, err := call(t, s, amy, "record_turn", map[string]any{
		"turn_ref": "t1",
		"links": []any{
			map[string]any{
				"kind":     "knows",
				"from":     map[string]any{"label": "张三"},
				"to":       map[string]any{"label": "赵六"},
				"strength": 3,
				"intel":    []any{map[string]any{"kind": "user_said", "excerpt": "很熟"}},
			},
		},
	})
	if err == nil {
		t.Fatal("a link carried a relationship strength the model decided on")
	}
}
