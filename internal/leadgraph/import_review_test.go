package leadgraph_test

// Fences over the import review and the follow-up that makes an import worth
// anything.
//
// The claim: an import review is a SCREEN, not a conversation; the agent can
// describe it but cannot decide it; and after importing, something asks the one
// question that turns a full graph into a usable one.

import (
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

const reviewCSV = "姓名,公司,部门,职位,熟悉程度,备注\n" +
	"王五,A司,c业务组,组长,3,老朋友\n" +
	"张三,A司,c业务组,高级工程师,,\n" +
	"李四,A司,c业务组,,,\n" +
	",A司,c业务组,,,\n" + // no name: skipped, line 5
	"b2,C司,平台组,平台负责人,,\n"

// The decisions an import raises must NOT land in the conversation queue. A
// turn is designed to ask at most one question; a review is a screen somebody
// is looking at, and twelve questions belong on it together.
func TestImportDecisionsStayOutOfTheConversationQueue(t *testing.T) {
	s := newStore(t)
	// Something already recorded, so the file raises a decision.
	mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "王五"), "A司", "c业务组"))

	sess, err := s.StageImport(amy, "contacts.csv", []byte("姓名,公司,部门\n王总,A司,c业务组\n李四,A司,c业务组\n"))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	sum, ok := s.ImportSummary(amy, sess.ID)
	if !ok {
		t.Fatal("the staged plan cannot be described")
	}
	if len(sum.Decisions) != 1 {
		t.Fatalf("want one decision on the review screen, got %+v", sum.Decisions)
	}
	if n := len(s.Pending(amy)); n != 0 {
		t.Fatalf("an import put %d questions into the conversation queue", n)
	}
	// The turn that follows is not interrupted by the import either.
	r, err := s.RecordTurn(amy, leadgraph.ActorAgent, leadgraph.TurnInput{TurnRef: "t1"})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if r.Question != nil {
		t.Errorf("the conversation was interrupted by an import decision: %+v", r.Question)
	}
}

// The summary is what the agent reads out. Everything a person needs to act is
// in it, including the line numbers of what was skipped.
func TestTheSummaryIsEnoughToExplainTheImport(t *testing.T) {
	s := newStore(t)
	sess, err := s.StageImport(amy, "从Excel导出.csv", []byte(reviewCSV))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	sum, _ := s.ImportSummary(amy, sess.ID)

	if sum.File != "从Excel导出.csv" || sum.Rows != 5 {
		t.Errorf("the summary cannot name the file it read: %+v", sum)
	}
	if sum.Counts["create"] != 4 {
		t.Errorf("counts wrong: %+v", sum.Counts)
	}
	if sum.Mapping["strength"] != "熟悉程度" {
		t.Errorf("the summary cannot say how it read the columns: %+v", sum.Mapping)
	}
	rows := sum.SkippedRows[leadgraph.SkipNoName]
	if len(rows) != 1 || rows[0] != 5 {
		t.Errorf("the skipped row is not reported by line number: %+v", sum.SkippedRows)
	}
	// Three of the four usable rows carry no strength - which is why path_find
	// will still be empty, and the agent has to say so.
	if sum.RowsWithoutStrength != 3 {
		t.Errorf("want 3 rows without a strength, got %d", sum.RowsWithoutStrength)
	}
}

// A decision carries the line it came from, so the user can look at their own
// file while answering.
func TestADecisionSaysWhichLineItCameFrom(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "王五"), "A司", "c业务组"))
	sess, err := s.StageImport(amy, "x.csv", []byte("姓名,公司,部门\n李四,A司,c业务组\n王总,A司,c业务组\n"))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	sum, _ := s.ImportSummary(amy, sess.ID)
	if len(sum.Decisions) != 1 {
		t.Fatalf("want one decision, got %+v", sum.Decisions)
	}
	d := sum.Decisions[0]
	if d.Label != "王总" || d.Row != 3 {
		t.Errorf("the decision cannot be found in the user's file: %+v", d)
	}
	if d.Question == "" || len(d.Options) == 0 {
		t.Errorf("the decision does not say what is being asked: %+v", d)
	}
}

// Staging holds the plan across requests; committing applies it and clears it.
func TestAStagedPlanSurvivesUntilItIsCommitted(t *testing.T) {
	s := newStore(t)
	sess, err := s.StageImport(amy, "contacts.csv", []byte(reviewCSV))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{})); n != 0 {
		t.Fatalf("staging wrote %d records", n)
	}
	// Still there on a later request.
	if _, ok := s.ImportSummary(amy, sess.ID); !ok {
		t.Fatal("the staged plan did not survive")
	}
	// And findable without quoting an id, because "that file" means the last one.
	if sum, ok := s.ImportSummary(amy, ""); !ok || sum.ID != sess.ID {
		t.Errorf("the most recent staged import is not the default")
	}

	res, err := s.CommitImport(amy, sess.ID, nil, day)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(res.Created) != 4 {
		t.Fatalf("want 4 people, got %+v", res)
	}
	if _, ok := s.ImportSummary(amy, sess.ID); ok {
		t.Error("a committed plan is still staged, so it can be applied twice")
	}
}

// Discarding is a separate call from committing, so "I do not want this" can
// never be recorded as "I applied this".
func TestDiscardingIsNotCommitting(t *testing.T) {
	s := newStore(t)
	sess, _ := s.StageImport(amy, "x.csv", []byte(reviewCSV))
	if !s.DiscardImport(amy, sess.ID) {
		t.Fatal("could not discard")
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{})); n != 0 {
		t.Errorf("discarding wrote %d records", n)
	}
	if _, ok := s.ImportSummary(amy, sess.ID); ok {
		t.Error("the discarded plan is still staged")
	}
}

// A staged import belongs to the seat that uploaded it.
func TestAStagedImportBelongsToTheSeatThatUploadedIt(t *testing.T) {
	s := newStore(t)
	sess, _ := s.StageImport(amy, "x.csv", []byte(reviewCSV))
	if _, ok := s.ImportSummary(ben, sess.ID); ok {
		t.Error("a teammate can read somebody else's staged file")
	}
	if s.DiscardImport(ben, sess.ID) {
		t.Error("a teammate discarded somebody else's staged file")
	}
	if _, err := s.CommitImport(ben, sess.ID, nil, day); err == nil {
		t.Error("a teammate committed somebody else's staged file")
	}
}

// The follow-up that makes an import worth anything: after it, the graph is
// full and path_find still has nowhere to start unless somebody asks.
func TestAfterAnImportSomethingAsksWhoYouActuallyKnow(t *testing.T) {
	s := newStore(t)
	sess, _ := s.StageImport(amy, "contacts.csv", []byte(reviewCSV))
	if _, err := s.CommitImport(amy, sess.ID, nil, day); err != nil {
		t.Fatalf("commit: %v", err)
	}

	gap := s.RatingGap(amy, 0)
	if gap.Rated != 1 {
		t.Errorf("the one rated row did not count: %+v", gap)
	}
	if gap.Unrated != 3 {
		t.Fatalf("want 3 people still unrated, got %d", gap.Unrated)
	}
	if len(gap.Ask) != 3 {
		t.Fatalf("nothing to put in front of the user: %+v", gap.Ask)
	}
	// In name order, and NOT ranked by anything about the people.
	var labels []string
	for _, p := range gap.Ask {
		labels = append(labels, p.Label)
	}
	want := append([]string{}, labels...)
	sortStrings(want)
	if strings.Join(labels, ",") != strings.Join(want, ",") {
		t.Errorf("the list is ranked: %v", labels)
	}
	// The cap keeps it a list somebody answers.
	if got := len(s.RatingGap(amy, 2).Ask); got != 2 {
		t.Errorf("the cap is not honoured: %d", got)
	}
}

// And answering it is what turns a full graph into a usable one.
func TestRatingThroughTheToolsMakesPathFindWork(t *testing.T) {
	s := newStore(t)
	sess, _ := s.StageImport(amy, "contacts.csv", []byte("姓名,公司\n张三,A司\nb2,C司\n"))
	res, err := s.CommitImport(amy, sess.ID, nil, day)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	zhang, b2 := res.Created[0], res.Created[1]
	mustEdge(t, s, amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeKnows, From: zhang, To: b2, Intel: []leadgraph.Intel{said("张三认识b2")},
	})

	if p := s.PathsTo(amy, b2, leadgraph.PathOptions{}); !p.NoSeeds {
		t.Fatal("expected NoSeeds before anybody has said who they know")
	}
	if _, err := call(t, s, amy, "rate_contact", map[string]any{
		"node_id": zhang, "strength": 3, "note": "老同事",
	}); err != nil {
		t.Fatalf("rate_contact: %v", err)
	}
	p := s.PathsTo(amy, b2, leadgraph.PathOptions{})
	if p.NoSeeds || len(p.Paths) != 1 {
		t.Fatalf("one rating should have opened a route: %+v", p)
	}
	if n, _ := s.Node(amy, zhang); n.Note != "老同事" {
		t.Errorf("the note did not land: %q", n.Note)
	}
	if n, _ := s.Node(ben, zhang); n.Note != "" {
		t.Errorf("the rating note reached a teammate: %q", n.Note)
	}
}

// The agent may DESCRIBE an import. Applying two hundred rows' worth of
// decisions is a different act and gets a deletion's gate.
func TestTheAgentCanDescribeAnImportButNotApplyItUnapproved(t *testing.T) {
	s := newStore(t)
	sess, _ := s.StageImport(amy, "contacts.csv", []byte(reviewCSV))

	got, err := call(t, s, amy, "import_summary", map[string]any{})
	if err != nil {
		t.Fatalf("import_summary: %v", err)
	}
	if sum, _ := got.(leadgraph.ImportSummary); sum.ID != sess.ID {
		t.Fatalf("the agent could not describe the staged file: %+v", got)
	}

	args := map[string]any{"import_id": sess.ID}
	if _, err := call(t, s, amy, "import_commit", args); err == nil ||
		!strings.Contains(err.Error(), "APPROVAL_MISSING") {
		t.Fatalf("the agent applied an import unapproved: %v", err)
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{})); n != 0 {
		t.Fatalf("the unapproved import wrote %d records", n)
	}

	if _, err := call(t, s, amy, "import_commit", args, leadgraph.ApprovalFor("import_commit", args)); err != nil {
		t.Fatalf("an approved import was refused: %v", err)
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson})); n != 4 {
		t.Errorf("the approved import produced %d people", n)
	}
}

// The approval covers the RESOLUTIONS too: approving "apply this file" is not
// approving "and merge row 3 into 王五".
func TestTheApprovalCoversTheDecisionsNotJustTheFile(t *testing.T) {
	s := newStore(t)
	existing := mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "王五"), "A司", "c业务组"))
	sess, _ := s.StageImport(amy, "x.csv", []byte("姓名,公司,部门\n王总,A司,c业务组\n"))

	plain := map[string]any{"import_id": sess.ID}
	withMerge := map[string]any{"import_id": sess.ID, "decisions": []any{
		map[string]any{"index": 0, "choice": "merge_into", "merge_into_id": existing.ID},
	}}

	// An approval of the bare apply does not authorise the merge decision.
	if _, err := call(t, s, amy, "import_commit", withMerge, leadgraph.ApprovalFor("import_commit", plain)); err == nil ||
		!strings.Contains(err.Error(), "APPROVAL_MISSING") {
		t.Fatalf("a decision rode in on an approval that never saw it: %v", err)
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson})); n != 1 {
		t.Fatalf("the unapproved decision was applied: %d people", n)
	}

	if _, err := call(t, s, amy, "import_commit", withMerge, leadgraph.ApprovalFor("import_commit", withMerge)); err != nil {
		t.Fatalf("the approved decision was refused: %v", err)
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson})); n != 1 {
		t.Errorf("the merge produced %d people", n)
	}
}

// rate_contact refuses a value outside 1-3 at the schema, before anything runs.
func TestRateContactRefusesAValueOutsideTheScale(t *testing.T) {
	s := newStore(t)
	n := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	for _, bad := range []any{0, 4, 99} {
		if _, err := call(t, s, amy, "rate_contact", map[string]any{"node_id": n.ID, "strength": bad}); err == nil {
			t.Errorf("strength %v was accepted", bad)
		}
	}
	if len(s.Seeds(amy)) != 0 {
		t.Error("a refused rating landed anyway")
	}
}
