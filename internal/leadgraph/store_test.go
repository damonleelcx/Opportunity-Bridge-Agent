package leadgraph_test

// P1 fences for docs/20-lead-graph.zh-CN.md §06, §07 and §11.
//
// Each test corresponds to one design decision in the PRD. When one goes red,
// the section named above it says why the behaviour was chosen, so a later
// change is a decision rather than an accident.

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

// Two seats in one team, and one seat in a different team.
var (
	amy  = leadgraph.View{TeamID: "team_a", SeatID: "amy"}
	ben  = leadgraph.View{TeamID: "team_a", SeatID: "ben"}
	cara = leadgraph.View{TeamID: "team_b", SeatID: "cara"}
)

func newStore(t *testing.T) *leadgraph.Store {
	t.Helper()
	s := leadgraph.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	n := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { n = n.Add(time.Minute); return n })
	return s
}

func said(excerpt string) leadgraph.Intel {
	return leadgraph.Intel{Kind: leadgraph.IntelUserSaid, TurnRef: "t1", Excerpt: excerpt}
}

func person(org, label string, in ...leadgraph.Intel) leadgraph.Node {
	if len(in) == 0 {
		in = []leadgraph.Intel{said(label + " 在 " + org)}
	}
	return leadgraph.Node{Kind: leadgraph.KindPerson, Org: org, Label: label, Intel: in}
}

func mustNode(t *testing.T, s *leadgraph.Store, v leadgraph.View, actor leadgraph.Actor, n leadgraph.Node) leadgraph.Node {
	t.Helper()
	got, err := s.UpsertNode(v, actor, n)
	if err != nil {
		t.Fatalf("UpsertNode(%s): %v", n.Label, err)
	}
	return got
}

func mustEdge(t *testing.T, s *leadgraph.Store, v leadgraph.View, actor leadgraph.Actor, e leadgraph.Edge) leadgraph.Edge {
	t.Helper()
	got, err := s.UpsertEdge(v, actor, e)
	if err != nil {
		t.Fatalf("UpsertEdge: %v", err)
	}
	return got
}

// §06 — the headline refusal. Without a source, a line on the graph is
// indistinguishable from an invented one, and it changes who the user calls.
func TestEdgeWithoutIntelIsRejected(t *testing.T) {
	s := newStore(t)
	a := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	b := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "b2"))

	_, err := s.UpsertEdge(amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeColleague, From: a.ID, To: b.ID,
	})
	if err == nil || !strings.Contains(err.Error(), "INTEL_REQUIRED") {
		t.Fatalf("want INTEL_REQUIRED, got %v", err)
	}
	if got := s.Edges(amy, leadgraph.EdgeFilter{}); len(got) != 0 {
		t.Errorf("refused edge still landed: %d", len(got))
	}
}

func TestNodeWithoutIntelIsRejected(t *testing.T) {
	s := newStore(t)
	_, err := s.UpsertNode(amy, leadgraph.ActorAgent, leadgraph.Node{
		Kind: leadgraph.KindPerson, Label: "王五",
	})
	if err == nil || !strings.Contains(err.Error(), "INTEL_REQUIRED") {
		t.Fatalf("want INTEL_REQUIRED, got %v", err)
	}
}

// §04 (Q1 拍板) — facts are the team's. Two seats in one team see one graph.
func TestFactsAreSharedInsideTheTeam(t *testing.T) {
	s := newStore(t)
	n := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	if got, ok := s.Node(ben, n.ID); !ok || got.Label != "王五" {
		t.Fatal("a teammate could not see a fact the team paid for")
	}
}

// §04 (Q1 拍板) — and they stop at the team boundary.
func TestGraphNeverLeavesTheTeam(t *testing.T) {
	s := newStore(t)
	mine := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	theirs := mustNode(t, s, cara, leadgraph.ActorUser, person("A司", "王五"))
	if mine.ID == theirs.ID {
		t.Fatal("two teams collapsed into one record")
	}
	if got := s.Nodes(cara, leadgraph.NodeFilter{}); len(got) != 1 || got[0].ID != theirs.ID {
		t.Fatalf("team_b saw %d nodes, wanted only its own", len(got))
	}
	if _, ok := s.Node(cara, mine.ID); ok {
		t.Error("team_b read a node belonging to team_a by id")
	}
	if n := s.Forget(cara, mine.ID); n != 0 {
		t.Error("team_b deleted a node belonging to team_a")
	}
}

// §04 (Q1 拍板) — judgements are the seat's. This is the half that makes a
// consultant willing to record anything at all.
func TestPrivateNotesNeverLeaveTheSeat(t *testing.T) {
	s := newStore(t)
	n := person("A司", "王五")
	n.Note = "上次电话很敷衍"
	created := mustNode(t, s, amy, leadgraph.ActorUser, n)

	if created.Note != "上次电话很敷衍" {
		t.Fatalf("the writer cannot see their own note: %q", created.Note)
	}
	mine, _ := s.Node(amy, created.ID)
	if mine.Note == "" {
		t.Error("the note did not survive a read by its author")
	}
	theirs, ok := s.Node(ben, created.ID)
	if !ok {
		t.Fatal("a teammate lost the fact along with the note")
	}
	if theirs.Note != "" {
		t.Errorf("a teammate read a private note: %q", theirs.Note)
	}
	// And it is not hiding in the serialised form either.
	b, _ := json.Marshal(theirs)
	if strings.Contains(string(b), "敷衍") {
		t.Errorf("the private note travelled inside the payload: %s", b)
	}
}

// §04 / §09 — two seats can hold different strengths for the same edge,
// because they are different relationships that connect the same two people.
func TestStrengthIsPerSeat(t *testing.T) {
	s := newStore(t)
	a := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "张三"))
	b := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "b2"))
	e := mustEdge(t, s, amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeColleague, From: a.ID, To: b.ID, Strength: 3,
		Intel: []leadgraph.Intel{said("2019-21 同项目")},
	})
	if e.Strength != 3 {
		t.Fatalf("strength did not stick for its author: %d", e.Strength)
	}

	seen := s.Edges(ben, leadgraph.EdgeFilter{})
	if len(seen) != 1 {
		t.Fatalf("teammate lost the edge itself: %d", len(seen))
	}
	if seen[0].Strength != 0 {
		t.Errorf("teammate inherited someone else's judgement: %d", seen[0].Strength)
	}
	if err := s.Annotate(ben, leadgraph.ActorUser, e.ID, 1, ""); err != nil {
		t.Fatalf("teammate could not record their own view: %v", err)
	}
	if got := s.Edges(ben, leadgraph.EdgeFilter{})[0].Strength; got != 1 {
		t.Errorf("teammate's own strength did not stick: %d", got)
	}
	if got := s.Edges(amy, leadgraph.EdgeFilter{})[0].Strength; got != 3 {
		t.Errorf("a teammate's write changed the author's strength: %d", got)
	}
}

// §04 (Q1 拍板) — when a consultant leaves, their judgements go and the team's
// facts stay. This is the whole reason the split exists.
func TestSeatDepartureLeavesTheFacts(t *testing.T) {
	s := newStore(t)
	n := person("A司", "王五")
	n.Note = "私人备注"
	created := mustNode(t, s, amy, leadgraph.ActorUser, n)

	if gone := s.ForgetSeat("amy"); gone == 0 {
		t.Fatal("nothing was removed for the departing seat")
	}
	if _, ok := s.Node(ben, created.ID); !ok {
		t.Error("the team lost a fact when a seat left")
	}
	back, _ := s.Node(amy, created.ID)
	if back.Note != "" {
		t.Errorf("a departed seat's private note survived: %q", back.Note)
	}
}

// §06 — an edge may not join two different teams' graphs.
func TestEdgeCannotCrossTeams(t *testing.T) {
	s := newStore(t)
	mine := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	theirs := mustNode(t, s, cara, leadgraph.ActorUser, person("C司", "b2"))
	_, err := s.UpsertEdge(amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeKnows, From: mine.ID, To: theirs.ID, Intel: []leadgraph.Intel{said("认识")},
	})
	if err == nil || !strings.Contains(err.Error(), "EDGE_ENDPOINT_MISSING") {
		t.Fatalf("want EDGE_ENDPOINT_MISSING, got %v", err)
	}
}

// §05.4 — "王五" and "王总" stay two records here. Deciding they are one is
// Reconcile's proposal and a person's answer.
func TestNoAutoMerge(t *testing.T) {
	s := newStore(t)
	a := mustNode(t, s, amy, leadgraph.ActorAgent, person("A司", "王五"))
	b := mustNode(t, s, amy, leadgraph.ActorAgent, person("A司", "王总"))
	if a.ID == b.ID {
		t.Fatal("two similar names were merged by the store")
	}
	if got := s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson}); len(got) != 2 {
		t.Fatalf("want 2 people, got %d", len(got))
	}
}

// §05.4 — the same fact twice is an update, not a duplicate.
func TestSameFactTwiceIsOneNode(t *testing.T) {
	s := newStore(t)
	a := mustNode(t, s, amy, leadgraph.ActorAgent, person("A司", "王五"))
	b := mustNode(t, s, amy, leadgraph.ActorAgent, person("A司", "王五"))
	if a.ID != b.ID {
		t.Fatalf("same fact produced two nodes: %s and %s", a.ID, b.ID)
	}
	if got := len(b.Intel); got != 1 {
		t.Errorf("the same source repeated itself and was counted twice: %d intel", got)
	}
}

// §05.4 — a second, independent source raises corroboration. The same source
// repeating itself must not, or a rumour promotes itself.
func TestSecondSourceRaisesCorroboration(t *testing.T) {
	s := newStore(t)
	n := mustNode(t, s, amy, leadgraph.ActorAgent, person("A司", "王五"))
	if got := n.Corroboration(); got != leadgraph.Hearsay {
		t.Fatalf("a single report should stay hearsay, got %s", got)
	}
	n = mustNode(t, s, amy, leadgraph.ActorAgent, person("A司", "王五", said("王五 在 A司")))
	if got := n.Corroboration(); got != leadgraph.Hearsay {
		t.Errorf("one source repeating itself promoted a rumour to %s", got)
	}
	n = mustNode(t, s, amy, leadgraph.ActorAgent, person("A司", "王五", leadgraph.Intel{
		Kind: leadgraph.IntelPublicSource, SourceURL: "https://example.com/jd/123", Excerpt: "c 业务组 王五",
	}))
	if got := n.Corroboration(); got != leadgraph.Corroborated {
		t.Errorf("two independent sources should corroborate, got %s", got)
	}
	n = mustNode(t, s, amy, leadgraph.ActorAgent, person("A司", "王五", leadgraph.Intel{
		Kind: leadgraph.IntelPublicSource, SourceURL: "https://a.example.com/notice", Excerpt: "组织调整公告", Announced: true,
	}))
	if got := n.Corroboration(); got != leadgraph.Announced {
		t.Errorf("an official notice should outrank everything, got %s", got)
	}
}

// §05.4 铁律一 — the agent may fill a blank, never replace a value.
func TestAgentCannotOverwriteAnExistingValue(t *testing.T) {
	s := newStore(t)
	base := person("A司", "王五")
	base.RoleTitle = "组长"
	created := mustNode(t, s, amy, leadgraph.ActorUser, base)

	promoted := person("A司", "王五")
	promoted.RoleTitle = "总监"
	if _, err := s.UpsertNode(amy, leadgraph.ActorAgent, promoted); err == nil ||
		!strings.Contains(err.Error(), "CONFLICT_NEEDS_USER") {
		t.Fatalf("want CONFLICT_NEEDS_USER, got %v", err)
	}
	if got, _ := s.Node(amy, created.ID); got.RoleTitle != "组长" {
		t.Errorf("the refused write landed anyway: %q", got.RoleTitle)
	}

	withDuty := person("A司", "王五")
	withDuty.Duty = "负责社招初筛"
	if n := mustNode(t, s, amy, leadgraph.ActorAgent, withDuty); n.Duty != "负责社招初筛" {
		t.Errorf("the agent could not fill an empty field: %q", n.Duty)
	}
}

// §05.4 铁律一 — a person's own edit goes through and leaves a trail.
func TestUserOverwriteLeavesHistory(t *testing.T) {
	s := newStore(t)
	base := person("A司", "王五")
	base.RoleTitle = "组长"
	mustNode(t, s, amy, leadgraph.ActorUser, base)

	promoted := person("A司", "王五")
	promoted.RoleTitle = "总监"
	n := mustNode(t, s, amy, leadgraph.ActorUser, promoted)

	if n.RoleTitle != "总监" {
		t.Fatalf("the edit did not apply: %q", n.RoleTitle)
	}
	if len(n.History) != 1 {
		t.Fatalf("want 1 field change, got %d", len(n.History))
	}
	h := n.History[0]
	if h.Field != "role_title" || h.From != "组长" || h.To != "总监" || h.By != leadgraph.ActorUser {
		t.Errorf("history did not record what changed: %+v", h)
	}
	if h.Why.Excerpt == "" {
		t.Error("history recorded a change with no reason attached")
	}
}

// §05.4 — an update that says nothing about a field must not clear it.
func TestOmittedFieldDoesNotErase(t *testing.T) {
	s := newStore(t)
	base := person("A司", "王五")
	base.RoleTitle, base.Duty = "组长", "负责社招初筛"
	mustNode(t, s, amy, leadgraph.ActorUser, base)

	n := mustNode(t, s, amy, leadgraph.ActorAgent, person("A司", "王五"))
	if n.RoleTitle != "组长" || n.Duty != "负责社招初筛" {
		t.Errorf("an update that mentioned neither field erased one: %+v", n)
	}
}

// §09 — nothing can infer how well two people know each other.
func TestStrengthIsNeverWrittenBySystem(t *testing.T) {
	s := newStore(t)
	a := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "张三"))
	b := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "b2"))
	e := leadgraph.Edge{Kind: leadgraph.EdgeColleague, From: a.ID, To: b.ID,
		Strength: 3, Intel: []leadgraph.Intel{said("2019-21 同项目")}}

	if _, err := s.UpsertEdge(amy, leadgraph.ActorAgent, e); err == nil ||
		!strings.Contains(err.Error(), "STRENGTH_IS_USER_ONLY") {
		t.Fatalf("want STRENGTH_IS_USER_ONLY, got %v", err)
	}
	got := mustEdge(t, s, amy, leadgraph.ActorUser, e)
	if got.Strength != 3 {
		t.Errorf("strength did not stick: %d", got.Strength)
	}
	if err := s.Annotate(amy, leadgraph.ActorAgent, got.ID, 2, ""); err == nil ||
		!strings.Contains(err.Error(), "STRENGTH_IS_USER_ONLY") {
		t.Errorf("the agent set strength through Annotate: %v", err)
	}

	e.Strength = 9
	if _, err := s.UpsertEdge(amy, leadgraph.ActorUser, e); err == nil ||
		!strings.Contains(err.Error(), "STRENGTH_RANGE") {
		t.Fatalf("want STRENGTH_RANGE, got %v", err)
	}
}

// §07 — the forbidden-source list is a refusal, not a setting.
func TestScrapedSourceIsRefused(t *testing.T) {
	s := newStore(t)
	for _, url := range []string{
		"https://www.linkedin.com/in/someone",
		"http://maimai.cn/profile/42",
		"https://zhipin.com/geek/resume/9",
		"https://cn.linkedin.com/in/someone",
	} {
		n := person("A司", "王五", leadgraph.Intel{
			Kind: leadgraph.IntelPublicSource, SourceURL: url, Excerpt: "资料",
		})
		if _, err := s.UpsertNode(amy, leadgraph.ActorAgent, n); err == nil ||
			!strings.Contains(err.Error(), "SOURCE_FORBIDDEN") {
			t.Errorf("%s was accepted (err=%v)", url, err)
		}
	}
	ok := person("A司", "王五", leadgraph.Intel{
		Kind: leadgraph.IntelPublicSource, SourceURL: "https://careers.example.com/jd/1", Excerpt: "c 业务组招聘",
	})
	if _, err := s.UpsertNode(amy, leadgraph.ActorAgent, ok); err != nil {
		t.Errorf("a lawful public source was refused: %v", err)
	}
}

// §06 — a reorganisation closes the old membership instead of deleting it, so
// "which group was he in at the time" still has an answer.
func TestClosedEdgeSurvivesAndStopsBeingCurrent(t *testing.T) {
	s := newStore(t)
	p := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	unit := mustNode(t, s, amy, leadgraph.ActorUser, leadgraph.Node{
		Kind: leadgraph.KindUnit, Org: "A司", Label: "c业务组",
		UnitPath: []string{"A司", "c业务组"}, Intel: []leadgraph.Intel{said("c业务组")},
	})
	e := mustEdge(t, s, amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeBelongsTo, From: p.ID, To: unit.ID, Intel: []leadgraph.Intel{said("王五在c组")},
	})

	merger := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	if err := s.CloseEdge(amy, e.ID, merger, said("c组并入b组")); err != nil {
		t.Fatalf("close: %v", err)
	}
	after := merger.Add(24 * time.Hour)
	if got := s.Edges(amy, leadgraph.EdgeFilter{OpenAt: &after}); len(got) != 0 {
		t.Errorf("a closed edge still reads as current: %d", len(got))
	}
	before := merger.Add(-24 * time.Hour)
	if got := s.Edges(amy, leadgraph.EdgeFilter{OpenAt: &before}); len(got) != 1 {
		t.Errorf("history was lost: the edge should still answer for the past, got %d", len(got))
	}
}

// §06 — Unconfirmed is a state the interface counts, so it must never be null.
func TestUnconfirmedIsNeverNull(t *testing.T) {
	s := newStore(t)
	if n := mustNode(t, s, amy, leadgraph.ActorAgent, person("A司", "王五")); len(n.Unconfirmed) == 0 {
		t.Fatal("a person with no title and no duty should have unconfirmed fields")
	}
	full := person("A司", "王五")
	full.RoleTitle, full.Duty = "组长", "负责社招初筛"
	n := mustNode(t, s, amy, leadgraph.ActorUser, full)
	if len(n.Unconfirmed) != 0 {
		t.Fatalf("a complete record still lists %v", n.Unconfirmed)
	}
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"unconfirmed":[]`) {
		t.Errorf("unconfirmed did not serialise as []: %s", b)
	}
}

// §07 / §11 — deletion is real, and it takes the lines that named the person
// with it, plus every seat's private overlay on them.
func TestForgetTakesTheEdgesAndTheNotesToo(t *testing.T) {
	s := newStore(t)
	a := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	b := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "b2"))
	mustEdge(t, s, amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeKnows, From: a.ID, To: b.ID, Intel: []leadgraph.Intel{said("认识")},
	})
	if err := s.Annotate(amy, leadgraph.ActorUser, a.ID, 0, "私人备注"); err != nil {
		t.Fatalf("annotate: %v", err)
	}

	if gone := s.Forget(amy, a.ID); gone != 2 {
		t.Fatalf("want node + edge removed, got %d", gone)
	}
	if got := s.Edges(amy, leadgraph.EdgeFilter{}); len(got) != 0 {
		t.Errorf("an edge outlived the person it named: %d", len(got))
	}
	again := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	if again.ID == a.ID {
		t.Error("a forgotten record came back under its old id")
	}
	if again.Note != "" {
		t.Errorf("a private note survived the deletion of its subject: %q", again.Note)
	}
	if len(again.History) != 0 {
		t.Error("a forgotten record came back carrying its old history")
	}
}

// §11 — reads are stable: two identical queries return the same order, so an
// unchanged graph never looks like it moved, and Reconcile cannot depend on the
// runtime rather than the data.
func TestReadsAreOrdered(t *testing.T) {
	s := newStore(t)
	for _, name := range []string{"王五", "张三", "李四", "b2"} {
		mustNode(t, s, amy, leadgraph.ActorUser, person("A司", name))
	}
	first := s.Nodes(amy, leadgraph.NodeFilter{})
	for range 20 {
		next := s.Nodes(amy, leadgraph.NodeFilter{})
		for i := range first {
			if first[i].ID != next[i].ID {
				t.Fatalf("read order changed between identical queries at %d", i)
			}
		}
	}
}

// §07 / CLAUDE.md 迁移脚本规范 — every migration runs on every start, so the
// second run has to succeed. Checked against the files that actually ship.
func TestSchemaIsIdempotent(t *testing.T) {
	entries, err := leadgraph.SchemaFS.ReadDir("schema")
	if err != nil {
		t.Fatalf("read schema dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no schema files shipped")
	}
	for _, e := range entries {
		body, err := leadgraph.SchemaFS.ReadFile("schema/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for i, stmt := range strings.Split(string(body), ";") {
			s := strings.TrimSpace(stripSQLComments(stmt))
			if s == "" || !strings.HasPrefix(strings.ToUpper(s), "CREATE") {
				continue
			}
			if !strings.Contains(strings.ToUpper(s), "IF NOT EXISTS") {
				t.Errorf("%s statement %d is not idempotent: %.60s", e.Name(), i, s)
			}
		}
	}
}

func stripSQLComments(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
