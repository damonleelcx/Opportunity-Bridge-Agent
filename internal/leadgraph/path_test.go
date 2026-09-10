package leadgraph_test

// P5 fences for docs/20-lead-graph.zh-CN.md §09 引荐路径 and 接触记录.
//
// The claim these defend: the route that gets a reply is not the shortest one,
// and the product must never make it look like it is.

import (
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

// knows wires a relationship edge and returns it.
func knows(t *testing.T, s *leadgraph.Store, v leadgraph.View, a, b leadgraph.Node, why string) leadgraph.Edge {
	t.Helper()
	return mustEdge(t, s, v, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeKnows, From: a.ID, To: b.ID, Intel: []leadgraph.Intel{said(why)},
	})
}

func rate(t *testing.T, s *leadgraph.Store, v leadgraph.View, id string, strength int) {
	t.Helper()
	if err := s.Annotate(v, leadgraph.ActorUser, id, strength, ""); err != nil {
		t.Fatalf("annotate %s: %v", id, err)
	}
}

// §09 — with no strength recorded anywhere, the answer is not "no route", it is
// "you have not told me who you know". They look identical on screen and need
// completely different next steps.
func TestNoStartingPointIsNotTheSameAsNoRoute(t *testing.T) {
	s := newStore(t)
	me := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "张三"))
	target := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "王五"))
	knows(t, s, amy, me, target, "张三认识王五")

	r := s.PathsTo(amy, target.ID, leadgraph.PathOptions{})
	if !r.NoSeeds {
		t.Fatal("a graph with no recorded strength should report NoSeeds")
	}
	if len(r.Paths) != 0 {
		t.Errorf("paths were returned with nowhere to start: %+v", r.Paths)
	}

	// Once the user says who they know, the same graph answers.
	rate(t, s, amy, me.ID, 3)
	r = s.PathsTo(amy, target.ID, leadgraph.PathOptions{})
	if r.NoSeeds {
		t.Fatal("still NoSeeds after a strength was recorded")
	}
	if len(r.Paths) != 1 || len(r.Paths[0].Hops) != 1 {
		t.Fatalf("want one 1-hop path, got %+v", r.Paths)
	}
}

// §09 — a shorter route through somebody you barely know is a WORSE route.
// Offering it first is how a user picks a path that goes nowhere.
func TestStrongerWeakestLinkBeatsFewerHops(t *testing.T) {
	s := newStore(t)
	strong1 := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "张三"))
	strong2 := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "b2"))
	weak := mustNode(t, s, amy, leadgraph.ActorUser, person("D司", "李四"))
	target := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "王五"))

	// Two hops, both strong.
	e1 := knows(t, s, amy, strong1, strong2, "前同事")
	e2 := knows(t, s, amy, strong2, target, "同组")
	// One hop, weak.
	e3 := knows(t, s, amy, weak, target, "群里认识")

	rate(t, s, amy, strong1.ID, 3)
	rate(t, s, amy, weak.ID, 1)
	rate(t, s, amy, e1.ID, 3)
	rate(t, s, amy, e2.ID, 3)
	rate(t, s, amy, e3.ID, 1)

	r := s.PathsTo(amy, target.ID, leadgraph.PathOptions{})
	if len(r.Paths) != 2 {
		t.Fatalf("want both routes, got %d", len(r.Paths))
	}
	first := r.Paths[0]
	if len(first.Hops) != 2 {
		t.Errorf("the short weak route was offered first: %+v", first.Hops)
	}
	if first.Weakest != 3 {
		t.Errorf("weakest link on the strong route: got %d, want 3", first.Weakest)
	}
	if r.Paths[1].Weakest != 1 {
		t.Errorf("weak route weakest: got %d", r.Paths[1].Weakest)
	}
}

// §09 / Q1 — how well two OTHER people know each other is not visible to you,
// because strength is private to the seat that recorded it. The hop must say
// so rather than showing a confident-looking zero.
func TestAHopThatIsNotYoursIsMarkedNotScored(t *testing.T) {
	s := newStore(t)
	mineNode := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "张三"))
	middle := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "b2"))
	target := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "王五"))
	e1 := knows(t, s, amy, mineNode, middle, "前同事")
	knows(t, s, amy, middle, target, "同组")

	rate(t, s, amy, mineNode.ID, 3)
	rate(t, s, amy, e1.ID, 3)
	// Ben rates the second hop. Amy must not see it.
	e2 := s.Edges(ben, leadgraph.EdgeFilter{Endpoint: target.ID})[0]
	rate(t, s, ben, e2.ID, 3)

	r := s.PathsTo(amy, target.ID, leadgraph.PathOptions{})
	if len(r.Paths) != 1 || len(r.Paths[0].Hops) != 2 {
		t.Fatalf("want one 2-hop path, got %+v", r.Paths)
	}
	h := r.Paths[0].Hops
	if !h[0].Mine || h[0].Strength != 3 {
		t.Errorf("your own hop lost its strength: %+v", h[0])
	}
	if h[1].Mine {
		t.Errorf("a teammate's judgement leaked onto your hop: %+v", h[1])
	}
	if h[1].Strength != 0 {
		t.Errorf("a hop you never rated shows a strength: %d", h[1].Strength)
	}
	if r.Paths[0].Weakest != 0 {
		t.Errorf("a path containing a hop that is not yours claims a weakest link: %d", r.Paths[0].Weakest)
	}
}

// §09 — structure is not a warm introduction. Treating a reporting line as a
// path produces the introduction that embarrasses the person who asked for it.
func TestStructureIsNotAPath(t *testing.T) {
	s := newStore(t)
	me := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "张三"))
	boss := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "王五"))
	u := mustNode(t, s, amy, leadgraph.ActorUser, unit("C司", "c业务组", "C司", "c业务组"))
	mustEdge(t, s, amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeReportsTo, From: me.ID, To: boss.ID, Intel: []leadgraph.Intel{said("汇报线")},
	})
	mustEdge(t, s, amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeBelongsTo, From: boss.ID, To: u.ID, Intel: []leadgraph.Intel{said("在c组")},
	})
	rate(t, s, amy, me.ID, 3)

	if r := s.PathsTo(amy, boss.ID, leadgraph.PathOptions{}); len(r.Paths) != 0 {
		t.Errorf("an org-chart line was offered as an introduction: %+v", r.Paths)
	}
}

// §09 / §05.6 — a closed relationship is not a route any more.
func TestAClosedRelationshipIsNotARoute(t *testing.T) {
	s := newStore(t)
	me := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "张三"))
	target := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "王五"))
	e := knows(t, s, amy, me, target, "认识")
	rate(t, s, amy, me.ID, 3)
	rate(t, s, amy, e.ID, 2)

	cut := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	if err := s.CloseEdge(amy, e.ID, cut, said("联系断了")); err != nil {
		t.Fatalf("close: %v", err)
	}

	after := s.PathsTo(amy, target.ID, leadgraph.PathOptions{At: cut.Add(24 * time.Hour)})
	if len(after.Paths) != 0 {
		t.Errorf("a closed relationship still routes: %+v", after.Paths)
	}
	before := s.PathsTo(amy, target.ID, leadgraph.PathOptions{At: cut.Add(-24 * time.Hour)})
	if len(before.Paths) != 1 {
		t.Errorf("history was lost: the route should still have existed before")
	}
}

// §09 — if you already know them, there is nobody to go through.
func TestKnowingThemDirectlyShortCircuits(t *testing.T) {
	s := newStore(t)
	target := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "王五"))
	rate(t, s, amy, target.ID, 2)

	r := s.PathsTo(amy, target.ID, leadgraph.PathOptions{})
	if !r.YouKnowThem {
		t.Fatal("the product sent you looking for an introduction to your own contact")
	}
	if len(r.Paths) != 0 {
		t.Errorf("paths were offered anyway: %+v", r.Paths)
	}
}

// §09 — the result leads with what the team already did. Acting without it is
// how two consultants cold-call the same person in one week.
func TestPathResultLeadsWithTheTeamsLastContact(t *testing.T) {
	s := newStore(t)
	me := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "张三"))
	target := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "王五"))
	e := knows(t, s, amy, me, target, "认识")
	rate(t, s, amy, me.ID, 3)
	rate(t, s, amy, e.ID, 3)

	// BEN made the call, not Amy.
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	if _, err := s.AddTouchpoint(ben, leadgraph.ActorUser, leadgraph.Touchpoint{
		PersonID: target.ID, At: when, Via: leadgraph.ChannelPhone,
		Topic: "问了下他们组的情况", Outcome: "他说等岗位开了通知",
	}); err != nil {
		t.Fatalf("touchpoint: %v", err)
	}

	r := s.PathsTo(amy, target.ID, leadgraph.PathOptions{})
	if r.LastTouch == nil {
		t.Fatal("Amy was about to approach somebody her teammate called last week, and was not told")
	}
	if r.LastTouch.SeatID != "ben" || !r.LastTouch.At.Equal(when) {
		t.Errorf("wrong contact reported: %+v", r.LastTouch)
	}
	if r.LastTouch.Outcome == "" {
		t.Error("the outcome did not travel: she still has to go and ask him")
	}
}

// §09 — deterministic: the same graph draws the same routes in the same order.
func TestPathsAreDeterministic(t *testing.T) {
	s := newStore(t)
	target := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "王五"))
	for _, name := range []string{"张三", "李四", "赵五", "钱六"} {
		n := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", name))
		e := knows(t, s, amy, n, target, name+"认识王五")
		rate(t, s, amy, n.ID, 2)
		rate(t, s, amy, e.ID, 2)
	}
	first := s.PathsTo(amy, target.ID, leadgraph.PathOptions{MaxPaths: 4})
	if len(first.Paths) != 4 {
		t.Fatalf("want 4 routes, got %d", len(first.Paths))
	}
	for range 20 {
		next := s.PathsTo(amy, target.ID, leadgraph.PathOptions{MaxPaths: 4})
		for i := range first.Paths {
			if first.Paths[i].Hops[0].EdgeID != next.Paths[i].Hops[0].EdgeID {
				t.Fatalf("route order changed between identical queries at %d", i)
			}
		}
	}
}

// ---- 接触记录 ----

// §04 (Q1) — contact records are the TEAM's. That is the whole point.
func TestContactRecordsAreVisibleToTheTeam(t *testing.T) {
	s := newStore(t)
	p := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "王五"))
	tp, err := s.AddTouchpoint(amy, leadgraph.ActorUser, leadgraph.Touchpoint{
		PersonID: p.ID, At: time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC),
		Via: leadgraph.ChannelPhone, Topic: "初次沟通", Outcome: "让我下月再联系",
		Note: "他其实挺想动的",
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if tp.Note != "他其实挺想动的" {
		t.Fatalf("the author cannot see their own note: %q", tp.Note)
	}

	seen := s.Touchpoints(ben, p.ID)
	if len(seen) != 1 {
		t.Fatalf("a teammate cannot see the call: %d", len(seen))
	}
	if seen[0].Outcome != "让我下月再联系" {
		t.Error("the outcome did not reach the teammate")
	}
	if seen[0].Note != "" {
		t.Errorf("a private impression leaked to a teammate: %q", seen[0].Note)
	}
	if s.Touchpoints(cara, p.ID) != nil {
		t.Error("another team read the contact record")
	}
}

// §04 (Q1) — when the consultant leaves, the call log stays. It is what the
// firm paid for; only their private impression goes.
func TestContactRecordsSurviveTheSeatLeaving(t *testing.T) {
	s := newStore(t)
	p := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "王五"))
	if _, err := s.AddTouchpoint(amy, leadgraph.ActorUser, leadgraph.Touchpoint{
		PersonID: p.ID, At: time.Now().UTC(), Via: leadgraph.ChannelPhone,
		Topic: "初次沟通", Note: "私人印象",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	s.ForgetSeat("amy")
	left := s.Touchpoints(ben, p.ID)
	if len(left) != 1 || left[0].Topic != "初次沟通" {
		t.Fatalf("the team lost the call log when the consultant left: %+v", left)
	}
	if got := s.Touchpoints(amy, p.ID); len(got) != 1 || got[0].Note != "" {
		t.Errorf("the departed seat's private impression survived: %+v", got)
	}
}

// §05.4 — the same call mentioned twice is one record.
func TestTheSameCallMentionedTwiceIsOneRecord(t *testing.T) {
	s := newStore(t)
	p := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "王五"))
	when := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	in := leadgraph.Touchpoint{PersonID: p.ID, At: when, Via: leadgraph.ChannelPhone, Topic: "初次沟通"}

	a, _ := s.AddTouchpoint(amy, leadgraph.ActorUser, in)
	in.NextStep = "下月再打" // remembered later
	b, err := s.AddTouchpoint(amy, leadgraph.ActorAgent, in)
	if err != nil {
		t.Fatalf("second mention: %v", err)
	}
	if a.ID != b.ID {
		t.Fatalf("the same call produced two records: %s and %s", a.ID, b.ID)
	}
	if b.NextStep != "下月再打" {
		t.Errorf("the agent could not fill in a blank: %q", b.NextStep)
	}
	if len(s.Touchpoints(amy, p.ID)) != 1 {
		t.Error("duplicate records in the log")
	}

	// But it may not rewrite what is already there.
	in.Topic = "其实聊的是别的"
	if _, err := s.AddTouchpoint(amy, leadgraph.ActorAgent, in); err == nil {
		t.Error("the agent rewrote a recorded outcome")
	}
}

// §07 — deleting a person takes the record of having contacted them.
func TestForgettingAPersonTakesTheContactLog(t *testing.T) {
	s := newStore(t)
	p := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "王五"))
	if _, err := s.AddTouchpoint(amy, leadgraph.ActorUser, leadgraph.Touchpoint{
		PersonID: p.ID, At: time.Now().UTC(), Via: leadgraph.ChannelPhone, Topic: "初次沟通",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if gone := s.Forget(amy, p.ID); gone != 2 {
		t.Fatalf("want person + contact removed, got %d", gone)
	}
	if len(s.Touchpoints(amy, p.ID)) != 0 {
		t.Error("a contact record outlived the person it named")
	}
}

// §09 — a follow-up is a promise the person who made it remembers making.
// A teammate's promises in your list is how a follow-up list gets ignored.
func TestFollowUpsAreYourOwn(t *testing.T) {
	s := newStore(t)
	p := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "王五"))
	due := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	for _, v := range []leadgraph.View{amy, ben} {
		if _, err := s.AddTouchpoint(v, leadgraph.ActorUser, leadgraph.Touchpoint{
			PersonID: p.ID, At: time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC),
			Via: leadgraph.ChannelPhone, NextStep: "再打一次", DueAt: &due,
		}); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	mine := s.DueTouchpoints(amy, due)
	if len(mine) != 1 || mine[0].SeatID != "amy" {
		t.Fatalf("want only my own follow-up, got %+v", mine)
	}
	if len(s.DueTouchpoints(amy, due.Add(-48*time.Hour))) != 0 {
		t.Error("a follow-up that is not due yet appeared in the list")
	}
}

// §06 — a contact with no time cannot answer the question contacts exist for.
func TestAContactMustSayWhen(t *testing.T) {
	s := newStore(t)
	p := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "王五"))
	if _, err := s.AddTouchpoint(amy, leadgraph.ActorUser, leadgraph.Touchpoint{
		PersonID: p.ID, Via: leadgraph.ChannelPhone, Topic: "聊了聊",
	}); err == nil {
		t.Error("a contact with no time was accepted")
	}
}
