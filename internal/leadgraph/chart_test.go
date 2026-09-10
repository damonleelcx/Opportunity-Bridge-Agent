package leadgraph_test

// P4 fences for docs/20-lead-graph.zh-CN.md §09 组织架构图.
//
// The claim these defend: the chart draws its own incompleteness. A tidy tree
// would be read as "the system knows this company", and every part of it the
// user never said could only have been invented.

import (
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

func unit(org, label string, path ...string) leadgraph.Node {
	return leadgraph.Node{
		Kind: leadgraph.KindUnit, Org: org, Label: label, UnitPath: path,
		Intel: []leadgraph.Intel{said(label)},
	}
}

func findNode(ns []leadgraph.ChartNode, label string) *leadgraph.ChartNode {
	for i := range ns {
		if ns[i].Label == label {
			return &ns[i]
		}
		if got := findNode(ns[i].Children, label); got != nil {
			return got
		}
	}
	return nil
}

func countNodes(ns []leadgraph.ChartNode) int {
	n := 0
	for _, x := range ns {
		n += 1 + countNodes(x.Children)
	}
	return n
}

// §09 — the chart contains exactly the levels somebody stated, and nothing else.
func TestChartInventsNoLevels(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, unit("A司", "c业务组", "A司", "技术中心", "c业务组"))
	p := person("A司", "王五")
	p.UnitPath = []string{"A司", "技术中心", "c业务组"}
	mustNode(t, s, amy, leadgraph.ActorUser, p)

	c := s.OrgChart(amy, "A司", time.Time{})

	// 技术中心 and c业务组, and not one box more.
	if got := countNodes(c.Root); got != 2 {
		t.Fatalf("chart has %d boxes, want exactly the 2 that were stated: %+v", got, c.Root)
	}
	if len(c.Root) != 1 || c.Root[0].Label != "技术中心" {
		t.Fatalf("unexpected root: %+v", c.Root)
	}
}

// §09 — a level that only ever appeared inside somebody's path is Mentioned,
// not Recorded, so the interface draws it dashed instead of solid.
func TestMentionedLevelsAreNotDrawnAsRecorded(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, unit("A司", "c业务组", "A司", "技术中心", "c业务组"))
	p := person("A司", "王五")
	p.UnitPath = []string{"A司", "技术中心", "c业务组"}
	mustNode(t, s, amy, leadgraph.ActorUser, p)

	c := s.OrgChart(amy, "A司", time.Time{})
	mid := findNode(c.Root, "技术中心")
	leaf := findNode(c.Root, "c业务组")
	if mid == nil || leaf == nil {
		t.Fatal("expected both levels on the chart")
	}
	if mid.Presence != leadgraph.PresenceMentioned {
		t.Errorf("a level nobody recorded is drawn as %s", mid.Presence)
	}
	if mid.NodeID != "" {
		t.Error("a mentioned level claims a record it does not have")
	}
	if leaf.Presence != leadgraph.PresenceRecorded || leaf.NodeID == "" {
		t.Errorf("a recorded unit is drawn as %s", leaf.Presence)
	}
	if c.Counts.Units != 1 || c.Counts.UnitsMentioned != 1 {
		t.Errorf("counts do not separate recorded from mentioned: %+v", c.Counts)
	}
}

// §09 — somebody we cannot place is LISTED. A missing person looks exactly like
// a person who is not there, and that is the difference between "I don't know"
// and a wrong answer.
func TestPeopleWeCannotPlaceAreListedNotDropped(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五")) // no path, no membership

	c := s.OrgChart(amy, "A司", time.Time{})
	if len(c.Unplaced) != 1 || c.Unplaced[0].Label != "王五" {
		t.Fatalf("a person with no known group vanished: %+v", c)
	}
	if c.Unplaced[0].Placement != leadgraph.PlacedNowhere {
		t.Errorf("placement not stated: %s", c.Unplaced[0].Placement)
	}
	if c.Counts.People != 1 || c.Counts.Unplaced != 1 {
		t.Errorf("counts disagree with the page: %+v", c.Counts)
	}
}

// §09 — how we know where somebody sits is part of the answer: a recorded
// membership and a sentence are not the same evidence.
func TestPlacementSaysHowWeKnow(t *testing.T) {
	s := newStore(t)
	u := mustNode(t, s, amy, leadgraph.ActorUser, unit("A司", "c业务组", "A司", "c业务组"))
	byEdge := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "张三"))
	mustEdge(t, s, amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeBelongsTo, From: byEdge.ID, To: u.ID, Intel: []leadgraph.Intel{said("张三在c组")},
	})
	byPath := person("A司", "王五")
	byPath.UnitPath = []string{"A司", "c业务组"}
	mustNode(t, s, amy, leadgraph.ActorUser, byPath)

	c := s.OrgChart(amy, "A司", time.Time{})
	grp := findNode(c.Root, "c业务组")
	if grp == nil || len(grp.People) != 2 {
		t.Fatalf("both people should sit in c业务组: %+v", c.Root)
	}
	got := map[string]leadgraph.Placement{}
	for _, p := range grp.People {
		got[p.Label] = p.Placement
	}
	if got["张三"] != leadgraph.PlacedByEdge {
		t.Errorf("a recorded membership reported as %s", got["张三"])
	}
	if got["王五"] != leadgraph.PlacedByPath {
		t.Errorf("a path-only placement reported as %s", got["王五"])
	}
}

// §09 / §05.6 — after a reorganisation, an ended membership must NOT fall back
// to the path on the person's own record. Putting them back in the group they
// just left is the single most damaging thing this chart could get wrong,
// because that is exactly when somebody acts on it.
func TestEndedMembershipDoesNotFallBackToTheOldGroup(t *testing.T) {
	s := newStore(t)
	u := mustNode(t, s, amy, leadgraph.ActorUser, unit("A司", "c业务组", "A司", "c业务组"))
	p := person("A司", "王五")
	p.UnitPath = []string{"A司", "c业务组"} // the sentence that first placed him
	created := mustNode(t, s, amy, leadgraph.ActorUser, p)
	e := mustEdge(t, s, amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeBelongsTo, From: created.ID, To: u.ID, Intel: []leadgraph.Intel{said("王五在c组")},
	})

	merger := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	if err := s.CloseEdge(amy, e.ID, merger, said("c组并入b组")); err != nil {
		t.Fatalf("close: %v", err)
	}

	after := s.OrgChart(amy, "A司", merger.Add(24*time.Hour))
	if grp := findNode(after.Root, "c业务组"); grp != nil && len(grp.People) != 0 {
		t.Errorf("王五 is still drawn in the group he left: %+v", grp.People)
	}
	if len(after.Unplaced) != 1 {
		t.Fatalf("he should now be unplaced, got %+v", after.Unplaced)
	}
	var told bool
	for _, a := range after.Ambiguities {
		if a.Kind == leadgraph.AmbiguityMembershipEnded && a.Label == "王五" {
			told = true
			if a.Since == nil || !a.Since.Equal(merger) {
				t.Errorf("the chart cannot say when: %+v", a)
			}
		}
	}
	if !told {
		t.Error("the chart dropped him silently instead of saying his membership ended")
	}

	// And the past still answers.
	before := s.OrgChart(amy, "A司", merger.Add(-24*time.Hour))
	if grp := findNode(before.Root, "c业务组"); grp == nil || len(grp.People) != 1 {
		t.Errorf("history was lost: he should still be in c组 before the merger")
	}
}

// §09 — the same group name at two depths is reported, not resolved. Merging
// them would be a guess; both are what somebody said.
func TestSameLabelInTwoPlacesIsFlaggedNotMerged(t *testing.T) {
	s := newStore(t)
	a := person("A司", "王五")
	a.UnitPath = []string{"A司", "c业务组"}
	mustNode(t, s, amy, leadgraph.ActorUser, a)
	b := person("A司", "张三")
	b.UnitPath = []string{"A司", "技术中心", "c业务组"}
	mustNode(t, s, amy, leadgraph.ActorUser, b)

	c := s.OrgChart(amy, "A司", time.Time{})
	var found *leadgraph.Ambiguity
	for i, x := range c.Ambiguities {
		if x.Kind == leadgraph.AmbiguitySameLabelTwoPlaces {
			found = &c.Ambiguities[i]
		}
	}
	if found == nil {
		t.Fatalf("two groups called c业务组 were silently treated as one: %+v", c.Root)
	}
	if len(found.Paths) != 2 {
		t.Errorf("the ambiguity does not say where: %+v", found)
	}
	if c.Counts.People != 2 {
		t.Errorf("somebody was lost while the chart hedged: %+v", c.Counts)
	}
}

// §09 — the counts describe the tree that was built, not the inputs. A summary
// computed from anything else is how a page says "12 people" above nine.
func TestCountsDescribeWhatIsOnThePage(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, unit("A司", "c业务组", "A司", "c业务组"))
	withDuty := person("A司", "张三")
	withDuty.UnitPath = []string{"A司", "c业务组"}
	withDuty.RoleTitle, withDuty.Duty = "组长", "社招初筛"
	mustNode(t, s, amy, leadgraph.ActorUser, withDuty)
	bare := person("A司", "王五")
	bare.UnitPath = []string{"A司", "c业务组"}
	mustNode(t, s, amy, leadgraph.ActorUser, bare)
	mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "李四")) // unplaced

	c := s.OrgChart(amy, "A司", time.Time{})
	if c.Counts.People != 3 {
		t.Errorf("people: got %d, want 3", c.Counts.People)
	}
	if c.Counts.Unplaced != 1 {
		t.Errorf("unplaced: got %d, want 1", c.Counts.Unplaced)
	}
	if c.Counts.Unconfirmed != 2 {
		t.Errorf("unconfirmed: got %d, want 2 (王五 and 李四)", c.Counts.Unconfirmed)
	}
}

// §09 — two identical reads draw the same picture. A chart that reshuffles
// looks like the company reorganised again.
func TestChartIsOrdered(t *testing.T) {
	s := newStore(t)
	for _, g := range []string{"c业务组", "a业务组", "b业务组"} {
		mustNode(t, s, amy, leadgraph.ActorUser, unit("A司", g, "A司", g))
		for _, who := range []string{"王五", "张三", "李四"} {
			p := person("A司", who+g)
			p.UnitPath = []string{"A司", g}
			mustNode(t, s, amy, leadgraph.ActorUser, p)
		}
	}
	first := s.OrgChart(amy, "A司", time.Time{})
	if first.Root[0].Label != "a业务组" {
		t.Fatalf("groups are not in label order: %s", first.Root[0].Label)
	}
	for range 20 {
		next := s.OrgChart(amy, "A司", time.Time{})
		for i := range first.Root {
			if first.Root[i].Label != next.Root[i].Label {
				t.Fatalf("chart order changed between identical reads at %d", i)
			}
			for j := range first.Root[i].People {
				if first.Root[i].People[j].NodeID != next.Root[i].People[j].NodeID {
					t.Fatalf("people order changed inside %s", first.Root[i].Label)
				}
			}
		}
	}
}

// §04 — the chart stops at the team boundary like every other read.
func TestChartStopsAtTheTeamBoundary(t *testing.T) {
	s := newStore(t)
	p := person("A司", "王五")
	p.UnitPath = []string{"A司", "c业务组"}
	mustNode(t, s, amy, leadgraph.ActorUser, p)

	if c := s.OrgChart(cara, "A司", time.Time{}); c.Counts.People != 0 || len(c.Root) != 0 {
		t.Errorf("another team read this chart: %+v", c)
	}
	if c := s.OrgChart(ben, "A司", time.Time{}); c.Counts.People != 1 {
		t.Errorf("a teammate could not read a team fact: %+v", c.Counts)
	}
}

// §09 — the text fallback tells the same truth as the chart, including the
// dashed boxes. A fallback that looks MORE certain than what it replaces is
// worse than no fallback.
func TestRosterIsTheSameTruthAsTheChart(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, unit("A司", "c业务组", "A司", "技术中心", "c业务组"))
	p := person("A司", "王五")
	p.UnitPath = []string{"A司", "技术中心", "c业务组"}
	mustNode(t, s, amy, leadgraph.ActorUser, p)
	mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "李四")) // unplaced
	other := person("C司", "b2")
	other.UnitPath = []string{"C司", "平台组"}
	mustNode(t, s, amy, leadgraph.ActorUser, other)

	rows := s.Roster(amy, time.Time{})
	if len(rows) == 0 {
		t.Fatal("the fallback is empty while the chart is not")
	}
	if rows[0].Org != "A司" {
		t.Errorf("companies are not ordered: %s", rows[0].Org)
	}

	people := map[string]bool{}
	var mentioned, unplacedBlock bool
	for _, r := range rows {
		for _, p := range r.People {
			people[p.Label] = true
			if len(r.Path) == 0 {
				unplacedBlock = true
			}
		}
		if r.Presence == leadgraph.PresenceMentioned && len(r.Path) > 0 {
			mentioned = true
		}
	}
	for _, want := range []string{"王五", "李四", "b2"} {
		if !people[want] {
			t.Errorf("%s is on the chart but not in the fallback", want)
		}
	}
	if !mentioned {
		t.Error("the fallback lost the distinction between recorded and merely mentioned")
	}
	if !unplacedBlock {
		t.Error("the fallback dropped the people nobody could place")
	}
}
