package leadgraph_test

// P2 fences for docs/20-lead-graph.zh-CN.md §05.2 and §05.4.
//
// The claim these defend: reconciliation is a pure function a person confirms,
// not a judgement the model makes while writing.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

func inUnit(n leadgraph.Node, unit ...string) leadgraph.Node {
	n.UnitPath = unit
	return n
}

// §05.2 — the reason the tool was split off at all: given the same graph and
// the same candidates it must answer the same way, forever.
func TestReconcileIsDeterministic(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "王五"), "A司", "c业务组"))
	mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "张三"), "A司", "c业务组"))
	mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "b2"))

	candidates := []leadgraph.Node{
		inUnit(person("A司", "王总"), "A司", "c业务组"),
		person("A司", "李四"),
		inUnit(person("A司", "张三"), "A司", "c业务组"),
	}

	first, err := json.Marshal(s.Reconcile(amy, candidates))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for i := range 20 {
		again, err := json.Marshal(s.Reconcile(amy, candidates))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("run %d differed from run 0:\nfirst: %s\nagain: %s", i, first, again)
		}
	}
}

// §05.2 — reconcile writes nothing. If it did, the two-tool split would be
// decoration.
func TestReconcileWritesNothing(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "王五"), "A司", "c业务组"))
	before := len(s.Nodes(amy, leadgraph.NodeFilter{}))

	s.Reconcile(amy, []leadgraph.Node{
		inUnit(person("A司", "王总"), "A司", "c业务组"),
		person("A司", "全新的人"),
	})

	if after := len(s.Nodes(amy, leadgraph.NodeFilter{})); after != before {
		t.Fatalf("Reconcile changed the graph: %d -> %d nodes", before, after)
	}
}

// §05.4 — a merge proposal must say WHY, in terms a person can disagree with.
func TestReconcileProposesMergeWithNamedReasons(t *testing.T) {
	s := newStore(t)
	existing := mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "王五"), "A司", "c业务组"))

	ps := s.Reconcile(amy, []leadgraph.Node{inUnit(person("A司", "王总"), "A司", "c业务组")})
	if len(ps) != 1 {
		t.Fatalf("want 1 proposal, got %d", len(ps))
	}
	p := ps[0]
	if p.Decision != leadgraph.DecideMerge {
		t.Fatalf("want merge, got %s", p.Decision)
	}
	if p.Question != leadgraph.QuestionMergeOrNew {
		t.Errorf("no question to put to the user: %q", p.Question)
	}
	if len(p.Merges) != 1 || p.Merges[0].NodeID != existing.ID {
		t.Fatalf("wrong merge candidate: %+v", p.Merges)
	}
	if len(p.Merges[0].Reasons) == 0 {
		t.Fatal("a merge was proposed with no reason attached")
	}
	if p.Merges[0].Reasons[0] != leadgraph.RuleSameOrgSameUnitSharedSurname {
		t.Errorf("unexpected reason: %v", p.Merges[0].Reasons)
	}
}

// §05.4 — never one weak signal. A shared surname matches a third of any
// Chinese company; two different given names are two different people.
func TestReconcileDoesNotMergeOnASingleWeakSignal(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "王五"), "A司", "c业务组"))

	cases := []struct {
		name string
		in   leadgraph.Node
	}{
		{"different given name", inUnit(person("A司", "王六"), "A司", "c业务组")},
		{"different company", inUnit(person("B司", "王总"), "B司", "c业务组")},
		{"different unit", inUnit(person("A司", "王总"), "A司", "b业务组")},
		{"different surname", inUnit(person("A司", "李总"), "A司", "c业务组")},
	}
	for _, c := range cases {
		ps := s.Reconcile(amy, []leadgraph.Node{c.in})
		if ps[0].Decision != leadgraph.DecideCreate {
			t.Errorf("%s: want create, got %s (%+v)", c.name, ps[0].Decision, ps[0].Merges)
		}
	}
}

// §05.4 — a changed value is a conflict, and the default reading offered to
// the user is "the world changed", not "you misremembered".
func TestReconcileFlagsConflictAndSuggestsAnEvent(t *testing.T) {
	s := newStore(t)
	base := person("A司", "王五")
	base.RoleTitle = "组长"
	mustNode(t, s, amy, leadgraph.ActorUser, base)

	promoted := person("A司", "王五")
	promoted.RoleTitle = "总监"
	p := s.Reconcile(amy, []leadgraph.Node{promoted})[0]

	if p.Decision != leadgraph.DecideConflict {
		t.Fatalf("want conflict, got %s", p.Decision)
	}
	if p.Question != leadgraph.QuestionChangedOrMistake {
		t.Errorf("no question to put to the user: %q", p.Question)
	}
	if len(p.Conflicts) != 1 || p.Conflicts[0].Field != "role_title" ||
		p.Conflicts[0].Current != "组长" || p.Conflicts[0].Proposed != "总监" {
		t.Fatalf("conflict not described: %+v", p.Conflicts)
	}
	if p.SuggestedEvent == nil {
		t.Fatal("no event suggested: the default reading of a change was lost")
	}
	if p.SuggestedEvent.Kind != leadgraph.KindEvent || !strings.Contains(p.SuggestedEvent.Label, "总监") {
		t.Errorf("suggested event does not describe the change: %+v", p.SuggestedEvent)
	}
}

// §05.2 — a decision that needs a person is not applied by handing it back.
func TestUnansweredProposalsAreNotApplied(t *testing.T) {
	s := newStore(t)
	base := person("A司", "王五")
	base.RoleTitle = "组长"
	created := mustNode(t, s, amy, leadgraph.ActorUser, base)
	mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "张三"), "A司", "c业务组"))

	promoted := person("A司", "王五")
	promoted.RoleTitle = "总监"
	ps := s.Reconcile(amy, []leadgraph.Node{
		promoted,
		inUnit(person("A司", "张总"), "A司", "c业务组"),
		person("C司", "全新的人"),
	})

	res, err := s.Apply(amy, leadgraph.ActorAgent, ps, nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(res.Pending) != 2 {
		t.Fatalf("want 2 pending (conflict + merge), got %v", res.Pending)
	}
	if len(res.Created) != 1 {
		t.Errorf("the safe proposal should still have been applied: %+v", res)
	}
	if got, _ := s.Node(amy, created.ID); got.RoleTitle != "组长" {
		t.Errorf("an unanswered conflict was applied anyway: %q", got.RoleTitle)
	}
	// 王五 + 张三 from the setup, plus the one safe create. The unanswered merge
	// must not have added a fourth.
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson})); n != 3 {
		t.Errorf("an unanswered merge created or destroyed a record: %d people", n)
	}
}

// §05.4 — "the world changed": the value moves, the old one stays in history,
// and the change becomes an event on the timeline.
func TestAcceptChangeAppliesAndRecordsTheEvent(t *testing.T) {
	s := newStore(t)
	base := person("A司", "王五")
	base.RoleTitle = "组长"
	created := mustNode(t, s, amy, leadgraph.ActorUser, base)

	promoted := person("A司", "王五")
	promoted.RoleTitle = "总监"
	ps := s.Reconcile(amy, []leadgraph.Node{promoted})

	res, err := s.Apply(amy, leadgraph.ActorAgent, ps, map[int]leadgraph.Resolution{
		0: {Choice: leadgraph.ChooseAcceptChange},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(res.Updated) != 1 || len(res.Created) != 1 {
		t.Fatalf("want one update and one event, got %+v", res)
	}
	got, _ := s.Node(amy, created.ID)
	if got.RoleTitle != "总监" {
		t.Errorf("the accepted change did not apply: %q", got.RoleTitle)
	}
	var found bool
	for _, h := range got.History {
		if h.Field == "role_title" && h.From == "组长" && !h.Rejected {
			found = true
		}
	}
	if !found {
		t.Errorf("the old value was not kept in history: %+v", got.History)
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindEvent})); n != 1 {
		t.Errorf("want 1 event on the timeline, got %d", n)
	}
}

// §05.4 — "that report is wrong": nothing changes, the claim is written down,
// and crucially the rejected claim's source does NOT strengthen the record.
func TestKeepCurrentRecordsTheRejectionWithoutStrengtheningTheRecord(t *testing.T) {
	s := newStore(t)
	base := person("A司", "王五")
	base.RoleTitle = "组长"
	created := mustNode(t, s, amy, leadgraph.ActorUser, base)

	rumour := person("A司", "王五", leadgraph.Intel{
		Kind: leadgraph.IntelPublicSource, SourceURL: "https://gossip.example.com/1", Excerpt: "王五要被裁",
	})
	rumour.RoleTitle = "已离职"
	ps := s.Reconcile(amy, []leadgraph.Node{rumour})

	res, err := s.Apply(amy, leadgraph.ActorAgent, ps, map[int]leadgraph.Resolution{
		0: {Choice: leadgraph.ChooseKeepCurrent},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(res.Rejected) != 1 {
		t.Fatalf("the rejection was not reported: %+v", res)
	}
	got, _ := s.Node(amy, created.ID)
	if got.RoleTitle != "组长" {
		t.Errorf("a rejected claim changed the record: %q", got.RoleTitle)
	}
	var rejected bool
	for _, h := range got.History {
		if h.Rejected && h.To == "已离职" {
			rejected = true
		}
	}
	if !rejected {
		t.Errorf("the rejected claim left no trace: %+v", got.History)
	}
	if c := got.Corroboration(); c != leadgraph.Hearsay {
		t.Errorf("a REJECTED claim strengthened the record to %s", c)
	}
}

// §05.4 — merging on a person's say-so keeps the other name as evidence that
// it was ever seen.
func TestMergeIntoFoldsAndKeepsTheOtherName(t *testing.T) {
	s := newStore(t)
	existing := mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "王五"), "A司", "c业务组"))

	incoming := inUnit(person("A司", "王总"), "A司", "c业务组")
	incoming.RoleTitle = "总监"
	ps := s.Reconcile(amy, []leadgraph.Node{incoming})

	if _, err := s.Apply(amy, leadgraph.ActorAgent, ps, map[int]leadgraph.Resolution{
		0: {Choice: leadgraph.ChooseMergeInto, MergeIntoID: existing.ID},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, _ := s.Node(amy, existing.ID)
	if got.RoleTitle != "总监" {
		t.Errorf("the folded record did not gain the new fact: %q", got.RoleTitle)
	}
	var aka bool
	for _, h := range got.History {
		if h.Field == "also_known_as" && h.To == "王总" {
			aka = true
		}
	}
	if !aka {
		t.Errorf("the other name left no trace: %+v", got.History)
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson})); n != 1 {
		t.Errorf("the fold created a second record: %d", n)
	}
}

// §05.2 — a merge without an answer, or with an answer that does not fit the
// question, is refused rather than guessed at.
func TestMergeIntoNeedsATargetAndTheRightChoice(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "王五"), "A司", "c业务组"))
	ps := s.Reconcile(amy, []leadgraph.Node{inUnit(person("A司", "王总"), "A司", "c业务组")})

	if _, err := s.Apply(amy, leadgraph.ActorUser, ps, map[int]leadgraph.Resolution{
		0: {Choice: leadgraph.ChooseMergeInto},
	}); err == nil || !strings.Contains(err.Error(), "RESOLUTION_REQUIRED") {
		t.Errorf("merge with no target: want RESOLUTION_REQUIRED, got %v", err)
	}
	if _, err := s.Apply(amy, leadgraph.ActorUser, ps, map[int]leadgraph.Resolution{
		0: {Choice: leadgraph.ChooseAcceptChange},
	}); err == nil || !strings.Contains(err.Error(), "RESOLUTION_REQUIRED") {
		t.Errorf("wrong answer shape: want RESOLUTION_REQUIRED, got %v", err)
	}
}

// §05.2 — joining two records that both exist is the one destructive act here,
// so the agent may not perform it at all.
func TestMergeNodesIsRefusedToTheAgent(t *testing.T) {
	s := newStore(t)
	keep := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	drop := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王总"))

	if _, err := s.MergeNodes(amy, leadgraph.ActorAgent, keep.ID, drop.ID, said("同一个人")); err == nil ||
		!strings.Contains(err.Error(), "RESOLUTION_REQUIRED") {
		t.Fatalf("want RESOLUTION_REQUIRED, got %v", err)
	}
	if _, ok := s.Node(amy, drop.ID); !ok {
		t.Error("the refused merge deleted a record anyway")
	}
}

// §05.4 / §06 — a merge carries the lines across, folds the duplicates, drops
// the relationship-with-oneself it would otherwise create, and takes each
// seat's private overlay with it.
func TestMergeNodesMovesEdgesAndPrivateOverlay(t *testing.T) {
	s := newStore(t)
	keep := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	drop := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王总"))
	other := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "b2"))

	// One line only the dropped record has, one both have, and one between the
	// two records being merged.
	mustEdge(t, s, amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeKnows, From: drop.ID, To: other.ID, Intel: []leadgraph.Intel{said("王总认识b2")},
	})
	mustEdge(t, s, amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeColleague, From: keep.ID, To: other.ID, Intel: []leadgraph.Intel{said("王五和b2共事")},
	})
	mustEdge(t, s, amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeColleague, From: drop.ID, To: other.ID, Intel: []leadgraph.Intel{said("王总和b2共事")},
	})
	mustEdge(t, s, amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeKnows, From: keep.ID, To: drop.ID, Intel: []leadgraph.Intel{said("这两个人认识")},
	})
	if err := s.Annotate(amy, leadgraph.ActorUser, drop.ID, 0, "王总那条线是我跟的"); err != nil {
		t.Fatalf("annotate: %v", err)
	}

	merged, err := s.MergeNodes(amy, leadgraph.ActorUser, keep.ID, drop.ID, said("同一个人"))
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if merged.Note != "王总那条线是我跟的" {
		t.Errorf("the private note did not survive the merge: %q", merged.Note)
	}
	if _, ok := s.Node(amy, drop.ID); ok {
		t.Error("the dropped record is still readable")
	}

	edges := s.Edges(amy, leadgraph.EdgeFilter{})
	if len(edges) != 2 {
		t.Fatalf("want knows + colleague after folding, got %d: %+v", len(edges), edges)
	}
	for _, e := range edges {
		if e.From == e.To {
			t.Error("the merge left a relationship with oneself")
		}
		if e.From == drop.ID || e.To == drop.ID {
			t.Errorf("an edge still points at the dropped record: %+v", e)
		}
	}
	var aka bool
	for _, h := range merged.History {
		if h.Field == "merged_from" && h.From == "王总" {
			aka = true
		}
	}
	if !aka {
		t.Errorf("the merge left no trace of what was folded in: %+v", merged.History)
	}
}

// §05.2 — applying the same batch twice must not double anything: the whole
// point of the natural key.
func TestApplyIsIdempotent(t *testing.T) {
	s := newStore(t)
	ps := s.Reconcile(amy, []leadgraph.Node{person("A司", "王五"), person("C司", "b2")})

	if _, err := s.Apply(amy, leadgraph.ActorAgent, ps, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := s.Apply(amy, leadgraph.ActorAgent, ps, nil); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson})); n != 2 {
		t.Fatalf("want 2 people after applying the same batch twice, got %d", n)
	}
}
