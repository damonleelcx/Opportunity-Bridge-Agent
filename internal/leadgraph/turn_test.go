package leadgraph_test

// P3 fences for docs/20-lead-graph.zh-CN.md §05.3 路径一.
//
// The claim these defend: a turn stays out of the way (one line, at most one
// question) WITHOUT becoming permissive (nothing needing judgement is written
// just because there was no room to ask).

import (
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

func turn(t *testing.T, s *leadgraph.Store, v leadgraph.View, in leadgraph.TurnInput) leadgraph.TurnResult {
	t.Helper()
	r, err := s.RecordTurn(v, leadgraph.ActorAgent, in)
	if err != nil {
		t.Fatalf("RecordTurn: %v", err)
	}
	return r
}

// §05.3 — a turn that recorded nothing says nothing. A receipt on every turn is
// noise, and noise is how a one-line receipt becomes something users skip.
func TestATurnThatRecordsNothingSaysNothing(t *testing.T) {
	s := newStore(t)
	r := turn(t, s, amy, leadgraph.TurnInput{TurnRef: "t1"})
	if r.Receipt != nil {
		t.Errorf("an empty turn produced a receipt: %+v", r.Receipt)
	}
	if r.Question != nil {
		t.Errorf("an empty turn produced a question: %+v", r.Question)
	}
}

// §05.3 — the receipt names what landed, carries the corroboration so the line
// can say 未证实 without going to look it up, and stops at three.
func TestReceiptNamesAtMostThreeThings(t *testing.T) {
	s := newStore(t)
	r := turn(t, s, amy, leadgraph.TurnInput{TurnRef: "t1", Nodes: []leadgraph.Node{
		person("A司", "王五"), person("A司", "张三"), person("A司", "李四"),
		person("C司", "b2"), person("C司", "b3"),
	}})

	if r.Receipt == nil {
		t.Fatal("five new people produced no receipt")
	}
	if r.Receipt.Created != 5 {
		t.Errorf("receipt undercounts: %+v", r.Receipt)
	}
	if len(r.Receipt.Items) != leadgraph.ReceiptItemCap {
		t.Fatalf("receipt named %d items, cap is %d", len(r.Receipt.Items), leadgraph.ReceiptItemCap)
	}
	it := r.Receipt.Items[0]
	if it.Label == "" || it.Corroboration != leadgraph.Hearsay {
		t.Errorf("receipt item cannot be rendered honestly: %+v", it)
	}
	if len(it.Unconfirmed) == 0 {
		t.Error("receipt item does not carry what is still unconfirmed")
	}
}

// §05.3 — AT MOST ONE question, however many decisions the turn raised.
func TestATurnAsksAtMostOneQuestion(t *testing.T) {
	s := newStore(t)
	base := person("A司", "王五")
	base.RoleTitle = "组长"
	mustNode(t, s, amy, leadgraph.ActorUser, base)
	mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "张三"), "A司", "c业务组"))

	promoted := person("A司", "王五")
	promoted.RoleTitle = "总监"
	r := turn(t, s, amy, leadgraph.TurnInput{TurnRef: "t1", Nodes: []leadgraph.Node{
		promoted,
		inUnit(person("A司", "张总"), "A司", "c业务组"),
		person("C司", "全新的人"),
	}})

	if r.Question == nil {
		t.Fatal("two decisions were raised and none was asked about")
	}
	if len(r.QueuedIDs) != 2 {
		t.Fatalf("want both decisions queued, got %v", r.QueuedIDs)
	}
	if n := len(s.Pending(amy)); n != 2 {
		t.Errorf("queue holds %d, want 2", n)
	}
}

// §05.3 — being quiet is not being permissive: the decisions there was no room
// to ask about are NOT written.
func TestQuietTurnStillWritesNothingThatNeedsJudgement(t *testing.T) {
	s := newStore(t)
	base := person("A司", "王五")
	base.RoleTitle = "组长"
	created := mustNode(t, s, amy, leadgraph.ActorUser, base)

	promoted := person("A司", "王五")
	promoted.RoleTitle = "总监"
	turn(t, s, amy, leadgraph.TurnInput{TurnRef: "t1", Nodes: []leadgraph.Node{promoted}})

	got, _ := s.Node(amy, created.ID)
	if got.RoleTitle != "组长" {
		t.Fatalf("a conflict landed without anybody deciding: %q", got.RoleTitle)
	}
}

// §05.3 — conflicts are asked before merges. A conflict means the graph holds
// something the user's own words contradict; leaving it means the next answer
// cites a fact they just corrected.
func TestConflictsAreAskedBeforeMerges(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "张三"), "A司", "c业务组"))
	base := person("A司", "王五")
	base.RoleTitle = "组长"
	mustNode(t, s, amy, leadgraph.ActorUser, base)

	promoted := person("A司", "王五")
	promoted.RoleTitle = "总监"
	// The merge candidate is listed FIRST, so input order alone would pick it.
	r := turn(t, s, amy, leadgraph.TurnInput{TurnRef: "t1", Nodes: []leadgraph.Node{
		inUnit(person("A司", "张总"), "A司", "c业务组"),
		promoted,
	}})

	if r.Question == nil {
		t.Fatal("nothing was asked")
	}
	if r.Question.Proposal.Decision != leadgraph.DecideConflict {
		t.Errorf("asked about %s first, want conflict", r.Question.Proposal.Decision)
	}
	if r.Question.Key != leadgraph.QuestionChangedOrMistake {
		t.Errorf("question key %q does not match the decision", r.Question.Key)
	}
}

// §05.3 — a question the user did not answer is not asked again next turn.
// Repeating it is nagging, and it trains them to ignore the channel.
func TestAnUnansweredQuestionIsNotRepeated(t *testing.T) {
	s := newStore(t)
	base := person("A司", "王五")
	base.RoleTitle = "组长"
	mustNode(t, s, amy, leadgraph.ActorUser, base)

	promoted := person("A司", "王五")
	promoted.RoleTitle = "总监"
	first := turn(t, s, amy, leadgraph.TurnInput{TurnRef: "t1", Nodes: []leadgraph.Node{promoted}})
	if first.Question == nil {
		t.Fatal("nothing was asked on the first turn")
	}

	second := turn(t, s, amy, leadgraph.TurnInput{TurnRef: "t2", Nodes: []leadgraph.Node{promoted}})
	if second.Question != nil {
		t.Errorf("the same question was asked twice: %+v", second.Question)
	}
	if n := len(s.Pending(amy)); n != 1 {
		t.Errorf("repeating himself queued a duplicate: %d pending", n)
	}
}

// §05.3 — the user repeating a fact does not accumulate duplicate questions.
func TestRepeatedFactsQueueOnce(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "王五"), "A司", "c业务组"))

	merge := inUnit(person("A司", "王总"), "A司", "c业务组")
	for i := range 3 {
		r := turn(t, s, amy, leadgraph.TurnInput{TurnRef: "t", Nodes: []leadgraph.Node{merge}})
		if i > 0 && len(r.QueuedIDs) != 0 {
			t.Errorf("turn %d queued the same decision again: %v", i, r.QueuedIDs)
		}
	}
	if n := len(s.Pending(amy)); n != 1 {
		t.Fatalf("want 1 queued decision, got %d", n)
	}
}

// §05.3 — the answer is applied before the turn's new facts, and it clears the
// queue. Answering is what makes the question worth having asked.
func TestAnsweringAppliesAndClearsTheQueue(t *testing.T) {
	s := newStore(t)
	base := person("A司", "王五")
	base.RoleTitle = "组长"
	created := mustNode(t, s, amy, leadgraph.ActorUser, base)

	promoted := person("A司", "王五")
	promoted.RoleTitle = "总监"
	asked := turn(t, s, amy, leadgraph.TurnInput{TurnRef: "t1", Nodes: []leadgraph.Node{promoted}})

	r := turn(t, s, amy, leadgraph.TurnInput{
		TurnRef: "t2",
		Answer: &leadgraph.Answer{
			PendingID:  asked.Question.PendingID,
			Resolution: leadgraph.Resolution{Choice: leadgraph.ChooseAcceptChange},
		},
		Nodes: []leadgraph.Node{person("C司", "b2")},
	})

	if r.Answered == nil || len(r.Answered.Updated) != 1 {
		t.Fatalf("the answer did not apply: %+v", r.Answered)
	}
	if got, _ := s.Node(amy, created.ID); got.RoleTitle != "总监" {
		t.Errorf("the accepted change did not land: %q", got.RoleTitle)
	}
	if n := len(s.Pending(amy)); n != 0 {
		t.Errorf("the answered question is still queued: %d", n)
	}
	if len(s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindEvent})) != 1 {
		t.Error("accepting a change did not put it on the timeline")
	}
}

// §05.3 — only the seat that was asked may answer, and only a person.
func TestOnlyTheSeatThatWasAskedMayAnswer(t *testing.T) {
	s := newStore(t)
	base := person("A司", "王五")
	base.RoleTitle = "组长"
	mustNode(t, s, amy, leadgraph.ActorUser, base)
	promoted := person("A司", "王五")
	promoted.RoleTitle = "总监"
	asked := turn(t, s, amy, leadgraph.TurnInput{TurnRef: "t1", Nodes: []leadgraph.Node{promoted}})
	id := asked.Question.PendingID

	if _, err := s.Answer(ben, leadgraph.ActorUser, id, leadgraph.Resolution{Choice: leadgraph.ChooseAcceptChange}); err == nil {
		t.Error("a teammate answered a question they never heard")
	}
	if _, err := s.Answer(amy, leadgraph.ActorAgent, id, leadgraph.Resolution{Choice: leadgraph.ChooseAcceptChange}); err == nil {
		t.Error("the agent answered its own question")
	}
	if n := len(s.Pending(amy)); n != 1 {
		t.Errorf("a refused answer disturbed the queue: %d", n)
	}
	if len(s.Pending(ben)) != 0 {
		t.Error("a teammate can see somebody else's queue")
	}
}

// §05.3 — a reply that does not actually answer the question leaves it queued.
// A swallowed question never comes back.
func TestAnAnswerThatDoesNotFitLeavesTheQuestionQueued(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "王五"), "A司", "c业务组"))
	asked := turn(t, s, amy, leadgraph.TurnInput{TurnRef: "t1", Nodes: []leadgraph.Node{
		inUnit(person("A司", "王总"), "A司", "c业务组"),
	}})

	_, err := s.Answer(amy, leadgraph.ActorUser, asked.Question.PendingID,
		leadgraph.Resolution{Choice: leadgraph.ChooseAcceptChange}) // answers a conflict, not a merge
	if err == nil {
		t.Fatal("a mismatched answer was accepted")
	}
	if n := len(s.Pending(amy)); n != 1 {
		t.Errorf("the question was swallowed: %d pending", n)
	}
}

// §05.3 — "never mind" is a separate call from answering, so that declining to
// decide can never be recorded as a decision.
func TestDroppingIsNotAnswering(t *testing.T) {
	s := newStore(t)
	base := person("A司", "王五")
	base.RoleTitle = "组长"
	created := mustNode(t, s, amy, leadgraph.ActorUser, base)
	promoted := person("A司", "王五")
	promoted.RoleTitle = "总监"
	asked := turn(t, s, amy, leadgraph.TurnInput{TurnRef: "t1", Nodes: []leadgraph.Node{promoted}})

	if !s.DropPending(amy, asked.Question.PendingID) {
		t.Fatal("could not drop a pending decision")
	}
	if got, _ := s.Node(amy, created.ID); got.RoleTitle != "组长" {
		t.Errorf("dropping applied the change: %q", got.RoleTitle)
	}
	if n := len(s.Pending(amy)); n != 0 {
		t.Errorf("the dropped item is still queued: %d", n)
	}
}

// §05.3 / Q1 — a departing seat takes their unanswered questions with them.
// Nobody else heard the conversation, so nobody else can answer them.
func TestSeatDepartureTakesTheQueue(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "王五"), "A司", "c业务组"))
	turn(t, s, amy, leadgraph.TurnInput{TurnRef: "t1", Nodes: []leadgraph.Node{
		inUnit(person("A司", "王总"), "A司", "c业务组"),
	}})
	if len(s.Pending(amy)) != 1 {
		t.Fatal("nothing queued to begin with")
	}

	s.ForgetSeat("amy")
	if n := len(s.Pending(amy)); n != 0 {
		t.Errorf("a departed seat left %d unanswerable questions behind", n)
	}
	if len(s.Nodes(ben, leadgraph.NodeFilter{Kind: leadgraph.KindPerson})) != 1 {
		t.Error("the team lost a fact when a seat left")
	}
}

// §05.3 — provenance cannot be lost by an extractor that forgot the turn ref.
func TestTurnRefIsStampedOnIntel(t *testing.T) {
	s := newStore(t)
	n := leadgraph.Node{Kind: leadgraph.KindPerson, Org: "A司", Label: "王五",
		Intel: []leadgraph.Intel{{Kind: leadgraph.IntelUserSaid, Excerpt: "我今天跟王五聊了"}}}
	r := turn(t, s, amy, leadgraph.TurnInput{TurnRef: "turn_42", Nodes: []leadgraph.Node{n}})

	got, ok := s.Node(amy, r.Applied[0])
	if !ok {
		t.Fatal("nothing was written")
	}
	if got.Intel[0].TurnRef != "turn_42" {
		t.Errorf("turn reference lost: %+v", got.Intel[0])
	}
}

// §05.3 — the queue is ordered, so "the oldest thing still waiting" is a
// stable answer rather than whatever the map handed back.
func TestPendingQueueIsOrdered(t *testing.T) {
	s := newStore(t)
	for _, name := range []string{"王五", "张三", "李四"} {
		mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", name), "A司", "c业务组"))
	}
	turn(t, s, amy, leadgraph.TurnInput{TurnRef: "t1", Nodes: []leadgraph.Node{
		inUnit(person("A司", "王总"), "A司", "c业务组"),
		inUnit(person("A司", "张总"), "A司", "c业务组"),
		inUnit(person("A司", "李总"), "A司", "c业务组"),
	}})

	first := s.Pending(amy)
	if len(first) != 3 {
		t.Fatalf("want 3 queued, got %d", len(first))
	}
	for range 20 {
		next := s.Pending(amy)
		for i := range first {
			if first[i].ID != next[i].ID {
				t.Fatalf("queue order changed between identical reads at %d", i)
			}
		}
	}
	var last time.Time
	for _, p := range first {
		if p.At.Before(last) {
			t.Error("the queue is not oldest-first")
		}
		last = p.At
	}
}
