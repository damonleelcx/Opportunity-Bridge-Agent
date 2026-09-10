package leadgraph_test

// Fences over contact details (拍板 2026-09-10: 存).
//
// The claim: they are the team's, they only ever accumulate, removing one is a
// person's decision that leaves a trail, and every existing gate still holds
// over them - sensitivity, deletion, subject rights, and the export audit.

import (
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

func withPhone(org, label, phone string) leadgraph.Node {
	n := person(org, label)
	n.Contacts = []leadgraph.ContactPoint{{Kind: leadgraph.ContactPhone, Value: phone}}
	return n
}

// A contact is a FACT, so it belongs to the team. The whole reason team mode
// exists is "somebody already called him", and nobody can call without a number.
func TestContactsAreTheTeamsNotTheSeats(t *testing.T) {
	s := newStore(t)
	n := mustNode(t, s, amy, leadgraph.ActorUser, withPhone("A司", "王五", "13800000000"))
	if len(n.Contacts) != 1 {
		t.Fatalf("the contact did not land: %+v", n.Contacts)
	}
	mate, ok := s.Node(ben, n.ID)
	if !ok || len(mate.Contacts) != 1 || mate.Contacts[0].Value != "13800000000" {
		t.Fatalf("a teammate cannot reach the person: %+v", mate.Contacts)
	}
	if other, _ := s.Node(cara, n.ID); len(other.Contacts) != 0 {
		t.Error("contacts crossed a team boundary")
	}
}

// People have two numbers. A second one is not a contradiction of the first, so
// contacts union - which is also why the agent is allowed to add one at all.
func TestContactsAccumulateAndAreNeverOverwritten(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, withPhone("A司", "王五", "13800000000"))

	second := withPhone("A司", "王五", "13900000000")
	second.Contacts = append(second.Contacts, leadgraph.ContactPoint{
		Kind: leadgraph.ContactEmail, Value: "Wang@Example.com",
	})
	n := mustNode(t, s, amy, leadgraph.ActorAgent, second) // the AGENT may add
	if len(n.Contacts) != 3 {
		t.Fatalf("want both numbers and the address, got %+v", n.Contacts)
	}

	// The same number written differently is the same number.
	again := withPhone("A司", "王五", "138-0000-0000")
	again.Contacts = append(again.Contacts, leadgraph.ContactPoint{
		Kind: leadgraph.ContactEmail, Value: "wang@example.com",
	})
	n = mustNode(t, s, amy, leadgraph.ActorAgent, again)
	if len(n.Contacts) != 3 {
		t.Errorf("punctuation and case produced duplicates: %+v", n.Contacts)
	}
}

// Removing one is the half that loses something, so it is a person's decision
// and it leaves a trail.
func TestRemovingAContactIsAPersonsDecisionAndLeavesATrail(t *testing.T) {
	s := newStore(t)
	n := mustNode(t, s, amy, leadgraph.ActorUser, withPhone("A司", "王五", "13800000000"))
	why := said("这个号码停机了")

	if err := s.RemoveContact(amy, leadgraph.ActorAgent, n.ID, leadgraph.ContactPhone, "13800000000", why); err == nil {
		t.Fatal("the agent removed a contact on its own")
	}
	if got, _ := s.Node(amy, n.ID); len(got.Contacts) != 1 {
		t.Fatal("the refused removal happened anyway")
	}

	if err := s.RemoveContact(amy, leadgraph.ActorUser, n.ID, leadgraph.ContactPhone, "138 0000 0000", why); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got, _ := s.Node(amy, n.ID)
	if len(got.Contacts) != 0 {
		t.Fatalf("the contact survived: %+v", got.Contacts)
	}
	var trail bool
	for _, h := range got.History {
		if h.Field == "contact:phone" && h.From == "13800000000" {
			trail = true
		}
	}
	if !trail {
		t.Errorf("the removal left no trail: %+v", got.History)
	}
}

// A contact column is where an ID number gets pasted. A phone number is
// ordinary personal information; 身份证号 is not.
func TestASensitiveValueCannotHideInAContact(t *testing.T) {
	s := newStore(t)
	bad := person("A司", "王五")
	bad.Contacts = []leadgraph.ContactPoint{{Kind: leadgraph.ContactOther, Value: "身份证号 110101..."}}
	if _, err := s.UpsertNode(amy, leadgraph.ActorUser, bad); err == nil ||
		!strings.Contains(err.Error(), "SENSITIVE_FIELD") {
		t.Fatalf("want SENSITIVE_FIELD, got %v", err)
	}
	if _, err := s.UpsertNode(amy, leadgraph.ActorUser, withPhone("A司", "王五", "13800000000")); err != nil {
		t.Errorf("an ordinary phone number was refused: %v", err)
	}
}

// An empty or unlabelled contact is worse than none: it looks like a way to
// reach somebody.
func TestAnEmptyContactIsRefused(t *testing.T) {
	s := newStore(t)
	for _, c := range []leadgraph.ContactPoint{
		{Kind: leadgraph.ContactPhone, Value: "  "},
		{Kind: "carrier pigeon", Value: "x"},
	} {
		n := person("A司", "王五")
		n.Contacts = []leadgraph.ContactPoint{c}
		if _, err := s.UpsertNode(amy, leadgraph.ActorUser, n); err == nil {
			t.Errorf("%+v was accepted", c)
		}
	}
}

// Import reads the columns a headhunter's file is mostly made of.
func TestImportReadsPhoneEmailAndWeChat(t *testing.T) {
	s := newStore(t)
	file := "姓名,公司,手机,邮箱,微信\n" +
		"王五,A司,138-0000-0000,Wang@Example.com,wang_wu\n"
	plan, err := s.PlanImport(amy, "contacts.csv", []byte(file), leadgraph.ImportOverrides{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := s.ApplyImport(amy, plan, nil, day); err != nil {
		t.Fatalf("apply: %v", err)
	}
	ns := s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson})
	if len(ns) != 1 || len(ns[0].Contacts) != 3 {
		t.Fatalf("want three ways to reach him, got %+v", ns)
	}
	kinds := map[leadgraph.ContactKind]string{}
	for _, c := range ns[0].Contacts {
		kinds[c.Kind] = c.Value
		if c.Source == "" {
			t.Errorf("a contact with no source: %+v", c)
		}
	}
	if kinds[leadgraph.ContactPhone] != "138-0000-0000" || kinds[leadgraph.ContactWeChat] != "wang_wu" {
		t.Errorf("contacts were mangled: %+v", kinds)
	}
}

// A sensitive value in a contact column costs that contact, not the row.
func TestASensitiveContactColumnCostsOnlyThatContact(t *testing.T) {
	s := newStore(t)
	file := "姓名,公司,手机,备注\n王五,A司,身份证号110101,靠谱\n"
	plan, err := s.PlanImport(amy, "x.csv", []byte(file), leadgraph.ImportOverrides{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Proposals) != 1 {
		t.Fatalf("the row was lost: %+v", plan.Proposals)
	}
	if len(plan.Proposals[0].Candidate.Contacts) != 0 {
		t.Errorf("the sensitive contact survived: %+v", plan.Proposals[0].Candidate.Contacts)
	}
	var told bool
	for _, sk := range plan.Skipped {
		if sk.Reason == leadgraph.SkipSensitive && strings.Contains(sk.Detail, "身份证号") {
			told = true
		}
	}
	if !told {
		t.Errorf("the user is not told which cell was dropped: %+v", plan.Skipped)
	}
}

// §07 — the person is entitled to know we hold their number, and deleting them
// takes it with them.
func TestSubjectRightsCoverContacts(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, withPhone("A司", "王五", "13800000000"))

	recs := s.SubjectRecords(leadgraph.SubjectMatch{Org: "A司", Label: "王五"}, day)
	if len(recs) != 1 || len(recs[0].Node.Contacts) != 1 {
		t.Fatalf("a subject access request does not disclose their contact: %+v", recs)
	}
	s.ForgetSubject(leadgraph.SubjectMatch{Org: "A司", Label: "王五"}, day)
	for _, n := range s.Nodes(amy, leadgraph.NodeFilter{}) {
		if len(n.Contacts) > 0 {
			t.Errorf("a contact outlived the person: %+v", n.Contacts)
		}
	}
}

// §07 — "who took a copy" is the first question after a leak; "and how many
// ways to reach people were in it" is the second.
func TestAnExportCountsTheContactsItCarried(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, withPhone("A司", "王五", "13800000000"))
	mustNode(t, s, amy, leadgraph.ActorUser, withPhone("C司", "b2", "13900000000"))

	b, err := s.Export(amy, day)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var carried int
	for _, n := range b.Nodes {
		carried += len(n.Contacts)
	}
	if carried != 2 {
		t.Fatalf("the export does not carry the contacts: %d", carried)
	}
	var audited bool
	for _, e := range s.AuditTrail(amy) {
		if e.Action == leadgraph.AuditExport {
			audited = true
			if e.Detail["contacts"] != "2" {
				t.Errorf("the audit entry does not count them: %+v", e.Detail)
			}
		}
	}
	if !audited {
		t.Error("the export was not audited")
	}
}

// The screen needs them - the point of the screen is to make the call - and a
// teammate sees them, unlike the private note beside them.
func TestTheScreenCarriesContactsButNotSomebodyElsesNote(t *testing.T) {
	s := newStore(t)
	n := withPhone("A司", "王五", "13800000000")
	n.Note = "amy 的私人印象"
	created := mustNode(t, s, amy, leadgraph.ActorUser, n)

	snap := s.Snapshot(ben, time.Now().UTC())
	var found bool
	for _, x := range snap.Nodes {
		if x.ID != created.ID {
			continue
		}
		found = true
		if len(x.Contacts) != 1 {
			t.Errorf("a teammate's screen cannot make the call: %+v", x.Contacts)
		}
		if x.Note != "" {
			t.Errorf("the private note travelled with the contact: %q", x.Note)
		}
	}
	if !found {
		t.Fatal("the person is not on the teammate's screen at all")
	}
}

// A contact arrives with the fact, so adding needs no tool; removing does.
func TestContactsThroughTheToolSurface(t *testing.T) {
	s := newStore(t)
	got, err := call(t, s, amy, "record_turn", map[string]any{
		"turn_ref": "t1",
		"candidates": []any{map[string]any{
			"kind": "person", "label": "王五", "org": "A司",
			"contacts": []any{map[string]any{"kind": "phone", "value": "13800000000"}},
			"intel":    []any{map[string]any{"kind": "user_said", "excerpt": "王五的电话是138"}},
		}},
	})
	if err != nil {
		t.Fatalf("record_turn: %v", err)
	}
	res, _ := got.(leadgraph.TurnResult)
	id := res.Applied[0]
	if n, _ := s.Node(amy, id); len(n.Contacts) != 1 {
		t.Fatalf("the contact did not travel with the fact: %+v", n.Contacts)
	}

	if _, err := call(t, s, amy, "contact_remove", map[string]any{
		"node_id": id, "kind": "phone", "value": "13800000000", "why": "停机了",
	}); err != nil {
		t.Fatalf("contact_remove: %v", err)
	}
	if n, _ := s.Node(amy, id); len(n.Contacts) != 0 {
		t.Errorf("the contact survived removal: %+v", n.Contacts)
	}
	// And a channel nobody defined is refused at the schema.
	if _, err := call(t, s, amy, "contact_remove", map[string]any{
		"node_id": id, "kind": "telegram", "value": "x", "why": "y",
	}); err == nil {
		t.Error("an undefined channel was accepted")
	}
}
