package leadgraph_test

import (
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

// A screenshot import from staging to the graph, over the recorded readings.

func stageShot(t *testing.T, s *leadgraph.Store, reading string) leadgraph.ImportSession {
	t.Helper()
	sess, err := s.StageScreenshot(amy, "导图截图.png", picture, readFixture(t, reading))
	if err != nil {
		t.Fatalf("stage %s: %v", reading, err)
	}
	return sess
}

func personID(t *testing.T, s *leadgraph.Store, label string) string {
	t.Helper()
	var found []string
	for _, n := range s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson}) {
		if n.Label == label {
			found = append(found, n.ID)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d people named %q, want 1", len(found), label)
	}
	return found[0]
}

func reportsTo(s *leadgraph.Store) []leadgraph.Edge {
	return s.Edges(amy, leadgraph.EdgeFilter{Kind: leadgraph.EdgeReportsTo})
}

// Every row of a screenshot is a model's proposal, so the review shows every
// person with the text they were read from, not only the rows with a question.
func TestAStagedScreenshotShowsEveryPersonForChecking(t *testing.T) {
	s := newStore(t)
	sess := stageShot(t, s, goodReading)
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{})); n != 0 {
		t.Fatalf("staging wrote %d records", n)
	}
	sum, ok := s.ImportSummary(amy, sess.ID)
	if !ok {
		t.Fatal("the staged screenshot has no summary")
	}
	if sum.Source != leadgraph.SourceScreenshot || len(sum.People) != 23 {
		t.Fatalf("summary source %q with %d people, want screenshot with 23", sum.Source, len(sum.People))
	}
	for _, p := range sum.People {
		if p.Text == "" || !strings.Contains(p.Text, strings.Fields(p.Label)[0]) && p.Label != "Marcus" {
			t.Errorf("row %d (%s) is shown without the text it was read from: %q", p.Row, p.Label, p.Text)
		}
	}
	if len(sum.Held) != 2 || sum.Links != 10 || len(sum.UnlinkedRows) != 1 || len(sum.Checks) != 0 {
		t.Errorf("summary held=%d links=%d unlinked=%d checks=%d, want 2, 10, 1, 0",
			len(sum.Held), sum.Links, len(sum.UnlinkedRows), len(sum.Checks))
	}
}

func TestCommittingAScreenshotDrawsTheReportingLines(t *testing.T) {
	s := newStore(t)
	sess := stageShot(t, s, goodReading)
	res, err := s.CommitImport(amy, sess.ID, nil, day)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(res.Created) != 23 || res.Linked != 10 || len(res.Unlinked) != 0 {
		t.Fatalf("result created=%d linked=%d unlinked=%+v, want 23, 10, none", len(res.Created), res.Linked, res.Unlinked)
	}
	edges := reportsTo(s)
	if len(edges) != 10 {
		t.Fatalf("%d reporting lines in the graph, want 10", len(edges))
	}
	song, du := personID(t, s, "宋清和"), personID(t, s, "杜明远")
	var line *leadgraph.Edge
	for i, e := range edges {
		if e.From == song && e.To == du {
			line = &edges[i]
		}
		if e.From == du && e.To == song {
			t.Error("宋清和's line points the wrong way: the manager reports to the report")
		}
	}
	if line == nil {
		t.Fatal("宋清和 → 杜明远 was not drawn")
	}
	// One picture is one source: the line is unconfirmed until something else says so.
	if line.Corroboration() != leadgraph.Hearsay {
		t.Errorf("a line read from one screenshot is %s, want hearsay", line.Corroboration())
	}
	// The note is this seat's own, and carries the note hung under the person.
	for _, n := range s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson}) {
		if n.Label == "韩子墨" && !strings.Contains(n.Note, "附注：qmx 品牌") {
			t.Errorf("韩子墨's private note = %q", n.Note)
		}
	}
}

// Uploading the same picture again adds no line and no second source, so a
// reporting line cannot corroborate itself.
func TestTheSameScreenshotTwiceAddsNoLineAndNoSource(t *testing.T) {
	s := newStore(t)
	for round := 1; round <= 2; round++ {
		sess := stageShot(t, s, goodReading)
		if _, err := s.CommitImport(amy, sess.ID, nil, day); err != nil {
			t.Fatalf("commit %d: %v", round, err)
		}
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson})); n != 23 {
		t.Errorf("%d people after importing the same picture twice, want 23", n)
	}
	edges := reportsTo(s)
	if len(edges) != 10 {
		t.Fatalf("%d reporting lines after importing the same picture twice, want 10", len(edges))
	}
	for _, e := range edges {
		if len(e.Intel) != 1 || e.Corroboration() != leadgraph.Hearsay {
			t.Errorf("line %s carries %d pieces of evidence (%s); the same picture twice is one source",
				e.ID, len(e.Intel), e.Corroboration())
		}
	}
}

// Two people called 杜明远 at 青岩智能 already: the lines to him are not drawn
// to either, and each one says why.
func TestALineToAnAmbiguousNameIsReportedNotGuessed(t *testing.T) {
	s := newStore(t)
	for _, unit := range []string{"设计中心", "平台部"} {
		if _, err := s.UpsertNode(amy, leadgraph.ActorUser, leadgraph.Node{
			Kind: leadgraph.KindPerson, Label: "杜明远", Org: "青岩智能", UnitPath: []string{"青岩智能", unit},
			Intel: []leadgraph.Intel{{Kind: leadgraph.IntelUserSaid, TurnRef: "turn-1", Excerpt: "青岩智能" + unit + "的杜明远"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	sess := stageShot(t, s, goodReading)
	res, err := s.CommitImport(amy, sess.ID, nil, day)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	var ambiguous []string
	for _, u := range res.Unlinked {
		if u.Reason == leadgraph.LinkEndpointAmbiguous {
			ambiguous = append(ambiguous, u.From+"→"+u.To)
		}
	}
	joined := strings.Join(ambiguous, " ")
	if !strings.Contains(joined, "杜明远→白砚秋") || !strings.Contains(joined, "宋清和→杜明远") {
		t.Errorf("lines touching the ambiguous 杜明远 were not reported as ambiguous: %+v", res.Unlinked)
	}
	for _, e := range reportsTo(s) {
		for _, n := range s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson}) {
			if n.Label == "杜明远" && (e.From == n.ID || e.To == n.ID) {
				t.Errorf("a line was drawn to one of two people named 杜明远: %+v", e)
			}
		}
	}
}

func TestIncludingADepartedManagerImportsTheirLine(t *testing.T) {
	s := newStore(t)
	sess := stageShot(t, s, goodReading)
	chen := rowOf(t, goodReading, "陈望舒")
	if _, err := s.RestageImport(amy, sess.ID, leadgraph.ImportOverrides{Include: []int{chen}}); err != nil {
		t.Fatalf("restage: %v", err)
	}
	sum, _ := s.ImportSummary(amy, sess.ID)
	if len(sum.Held) != 1 || sum.Links != 11 {
		t.Errorf("after including 陈望舒: held=%d links=%d, want 1 and 11", len(sum.Held), sum.Links)
	}
	if _, err := s.CommitImport(amy, sess.ID, nil, day); err != nil {
		t.Fatalf("commit: %v", err)
	}
	meng, chenID := personID(t, s, "孟川"), personID(t, s, "陈望舒")
	for _, e := range reportsTo(s) {
		if e.From == meng && e.To == chenID {
			return
		}
	}
	t.Error("孟川 → 陈望舒 was not drawn after 陈望舒 was included")
}

// A correction re-plans from the reading that was staged, with the same source.
// Nothing in this package can read a picture, so no correction can ask a model
// again; the digest staying put is what shows it is the same reading.
func TestACorrectionReplansFromTheStagedReading(t *testing.T) {
	s := newStore(t)
	sess := stageShot(t, s, goodReading)
	du := rowOf(t, goodReading, "杜明远")
	again, err := s.RestageImport(amy, sess.ID, leadgraph.ImportOverrides{
		Rows: map[int]map[leadgraph.Column]string{du: {leadgraph.ColLabel: "杜明原"}},
	})
	if err != nil {
		t.Fatalf("restage: %v", err)
	}
	if again.Plan.Digest != sess.Plan.Digest || again.Plan.Source != leadgraph.SourceScreenshot {
		t.Errorf("a correction changed the source: %s/%q -> %s/%q",
			sess.Plan.Digest, sess.Plan.Source, again.Plan.Digest, again.Plan.Source)
	}
	sum, _ := s.ImportSummary(amy, sess.ID)
	found := false
	for _, p := range sum.People {
		found = found || p.Label == "杜明原"
	}
	if !found || len(sum.Checks) != 1 {
		t.Errorf("after correcting the name: in people=%v, checks=%+v", found, sum.Checks)
	}
}
