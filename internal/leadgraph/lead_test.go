package leadgraph_test

// P7 fences for docs/20-lead-graph.zh-CN.md §08 线索评分.
//
// The three constraints, one test each and then some: the subject is a group,
// a score cannot exist without its signals, and scores do not leave.

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

// board seeds a company with a group, three people in it, and an announced
// merger - the PRD's own worked example.
func board(t *testing.T) *leadgraph.Store {
	t.Helper()
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, unit("A司", "c业务组", "A司", "技术中心", "c业务组"))
	// Deliberately uneven: 王五 is complete, 张三 half, 李四 empty. Name order
	// (张三, 李四, 王五) is therefore DIFFERENT from any order by completeness,
	// which is what makes TestPeopleUnderALeadAreInNameOrderNotRanked able to
	// tell the two apart. With three identical records it could not.
	for _, who := range []struct{ name, role, duty string }{
		{"王五", "组长", "负责社招初筛"},
		{"张三", "高级工程师", ""},
		{"李四", "", ""},
	} {
		p := person("A司", who.name)
		p.UnitPath = []string{"A司", "技术中心", "c业务组"}
		p.RoleTitle, p.Duty = who.role, who.duty
		mustNode(t, s, amy, leadgraph.ActorUser, p)
	}
	src := &fakeSource{name: "reg", hosts: []string{"reg.example.com"}, docs: []leadgraph.Document{
		{URL: "https://reg.example.com/1", Kind: leadgraph.DocRegistryChange,
			Org: "A司", Unit: "c业务组", Title: "c业务组并入b业务组", PostedAt: at(-12), FetchedAt: day},
	}}
	if _, err := s.RunDaily(context.Background(), amy, []leadgraph.Source{src}, leadgraph.FetchRequest{}, day, time.Hour); err != nil {
		t.Fatalf("daily: %v", err)
	}
	return s
}

// §08.1 — the subject is a GROUP. The type has no person field at all, so a
// person-level score is not something that can be built and then blocked.
func TestALeadHasNoPersonSubject(t *testing.T) {
	ty := reflect.TypeOf(leadgraph.Lead{})
	for i := range ty.NumField() {
		f := ty.Field(i)
		if f.Name == "People" {
			continue // who you know there, in name order
		}
		if strings.Contains(strings.ToLower(f.Name), "person") ||
			strings.Contains(strings.ToLower(f.Name), "candidate") {
			t.Errorf("Lead carries %s: the subject has stopped being a group", f.Name)
		}
	}
	ls := board(t).LeadBoard(amy, day)
	if len(ls) == 0 {
		t.Fatal("no leads to check")
	}
	if ls[0].UnitLabel == "" {
		t.Error("a lead with no group is a lead about somebody")
	}
}

// §08.2 — a score exists only through its signals. Score is a method over
// Signals, so there is no way to hold one without the other.
func TestAScoreCannotExistWithoutItsSignals(t *testing.T) {
	// The structural half: no settable score field.
	ty := reflect.TypeOf(leadgraph.Lead{})
	if _, ok := ty.FieldByName("Score"); ok {
		t.Fatal("Score is a field: it can now be set without any signals")
	}
	// The behavioural half.
	var empty leadgraph.Lead
	if empty.Score() != 0 {
		t.Errorf("a lead with no signals scores %d", empty.Score())
	}
	ls := board(t).LeadBoard(amy, day)
	l := ls[0]
	if l.Score() == 0 || len(l.Signals) == 0 {
		t.Fatalf("want a scored lead with signals, got %d from %d signals", l.Score(), len(l.Signals))
	}
	sum := 0
	for _, sg := range l.Signals {
		sum += sg.Weight
		if sg.Because == "" {
			t.Error("a signal with no reason attached")
		}
		if sg.Kind == "" {
			t.Error("a signal with no name")
		}
	}
	if sum != l.Score() {
		t.Errorf("the score %d is not the sum of what is shown (%d)", l.Score(), sum)
	}
}

// §08.1 — corroboration is what an event's weight rises with. If a filing and a
// rumour weighed the same, tracking corroboration would be decoration.
func TestAFilingOutweighsARumour(t *testing.T) {
	official := board(t).LeadBoard(amy, day)
	if len(official) == 0 {
		t.Fatal("no lead from the filing")
	}

	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, unit("A司", "c业务组", "A司", "c业务组"))
	p := person("A司", "王五")
	p.UnitPath = []string{"A司", "c业务组"}
	mustNode(t, s, amy, leadgraph.ActorUser, p)
	mustNode(t, s, amy, leadgraph.ActorUser, leadgraph.Node{
		Kind: leadgraph.KindEvent, Org: "A司", Label: "c业务组并入b业务组",
		UnitPath: []string{"A司", "c业务组"}, OccurredAt: at(-12),
		Intel: []leadgraph.Intel{said("听说c组要并进b组")},
	})
	rumour := s.LeadBoard(amy, day)
	if len(rumour) == 0 {
		t.Fatal("no lead from the rumour")
	}
	if rumour[0].Score() >= official[0].Score() {
		t.Errorf("a rumour scored %d, a filing scored %d", rumour[0].Score(), official[0].Score())
	}
}

// §08.1 — groups are ranked; the people under one are not. The moment that list
// is ordered by anything else, the product is ranking people again.
func TestPeopleUnderALeadAreInNameOrderNotRanked(t *testing.T) {
	ls := board(t).LeadBoard(amy, day)
	if len(ls) == 0 || len(ls[0].People) != 3 {
		t.Fatalf("want a lead with three people, got %+v", ls)
	}
	got := make([]string, 0, 3)
	for _, p := range ls[0].People {
		got = append(got, p.Label)
	}
	want := append([]string{}, got...)
	sortStrings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("people are ordered %v, not by name %v", got, want)
	}
}

// §08.3 — scores do not leave in an export.
func TestScoresNeverLeaveInAnExport(t *testing.T) {
	s := board(t)
	if len(s.LeadBoard(amy, day)) == 0 {
		t.Fatal("nothing scored, so this test would prove nothing")
	}
	b, err := s.Export(amy, day)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, banned := range []string{`"signals"`, `"score"`, `"unit_event"`, `"hiring_stopped"`, `"weight"`} {
		if strings.Contains(body, banned) {
			t.Errorf("the export carries %s", banned)
		}
	}
	ty := reflect.TypeOf(leadgraph.ExportBundle{})
	for i := range ty.NumField() {
		if strings.Contains(strings.ToLower(ty.Field(i).Name), "lead") {
			t.Errorf("ExportBundle carries %s", ty.Field(i).Name)
		}
	}
}

// §08.3 — and not in the machine-readable endpoint either. Two independent
// exits, blocked separately.
func TestScoresAreNotInTheMachineReadableAPI(t *testing.T) {
	s := board(t)
	rec := serve(t, s, amy, "/data")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, banned := range []string{`"signals"`, `"score"`, `"unit_event"`, `"hiring_stopped"`} {
		if strings.Contains(body, banned) {
			t.Errorf("/data carries %s", banned)
		}
	}
	// It IS on the product's own screen, which is the point of building it.
	page := serve(t, s, amy, "/").Body.String()
	if !strings.Contains(page, "c业务组并入b业务组") {
		t.Error("the board is not on the screen either, so nothing was gained")
	}
}

// §08.2 — the screen shows the signals, and shows the window's definition next
// to the window. A number the reader has to trust is not an explained number.
func TestTheScreenExplainsWhatItShows(t *testing.T) {
	page := serve(t, board(t), amy, "/").Body.String()
	script := strings.Index(page, "<script")
	markup := page[:script]

	start := strings.Index(markup, "线索看板")
	if start < 0 {
		t.Fatal("the board is script-only")
	}
	end := strings.Index(markup[start:], "</section>")
	if end < 0 {
		t.Fatal("the board section is unterminated")
	}
	// Scoped to the board: the event label also appears in the alert banner
	// above, so a whole-page search would pass with the signals list deleted.
	boardHTML := markup[start : start+end]

	if !strings.Contains(boardHTML, "c业务组并入b业务组") {
		t.Error("the signal behind the score is not shown next to it")
	}
	if !strings.Contains(boardHTML, `class="bar"`) {
		t.Error("the signals list is gone: the score stands on its own")
	}
	// 官方公布, not "announced": the page used to print the raw enum on an
	// otherwise Chinese screen, and the conversation's own result cards now say
	// the same three words (web/static/i18n.js, corr.*). The guarantee is
	// unchanged — a signal must carry how well attested it is — only the word
	// this asserts changed. See 名词字典, docs/20-lead-graph.zh-CN.md §3.
	if !strings.Contains(boardHTML, "官方公布") {
		t.Error("a signal is shown without how well attested it is")
	}
	if !strings.Contains(boardHTML, "窗口期约剩") || !strings.Contains(boardHTML, "起算") {
		t.Error("the window is printed without the convention that defines it")
	}
	if !strings.Contains(boardHTML, "你图上认识") {
		t.Error("the board does not say who you know there")
	}
}

// §08 — the board is not a directory: a group with nothing to say about it is
// absent, not listed at zero.
func TestGroupsWithNoSignalsAreAbsent(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, unit("A司", "安静组", "A司", "安静组"))
	p := person("A司", "王五")
	p.UnitPath = []string{"A司", "安静组"}
	mustNode(t, s, amy, leadgraph.ActorUser, p)

	if ls := s.LeadBoard(amy, day); len(ls) != 0 {
		t.Errorf("a group with no signals was listed: %+v", ls)
	}
}

// §08 — the board is ordered and stable.
func TestTheBoardIsOrderedByScoreAndStable(t *testing.T) {
	s := board(t)
	first := s.LeadBoard(amy, day)
	for i := 1; i < len(first); i++ {
		if first[i-1].Score() < first[i].Score() {
			t.Errorf("board is not in score order at %d", i)
		}
	}
	for range 10 {
		next := s.LeadBoard(amy, day)
		if len(next) != len(first) {
			t.Fatal("board length changed between identical reads")
		}
		for i := range first {
			if first[i].UnitLabel != next[i].UnitLabel {
				t.Fatalf("board order changed at %d", i)
			}
		}
	}
}

func sortStrings(x []string) {
	for i := 1; i < len(x); i++ {
		for j := i; j > 0 && x[j] < x[j-1]; j-- {
			x[j], x[j-1] = x[j-1], x[j]
		}
	}
}

// §08 — one row per SITUATION. An event names a company and a group; there is
// one such situation however many places that name turns up in the chart.
//
// It used to be one row per chart node, which printed the same group twice with
// the same score - once with the people and once without. The first look at a
// realistic board caught it.
func TestOneRowPerGroupNameEvenWhenTheNameSitsInTwoPlaces(t *testing.T) {
	s := newStore(t)
	// The same name at two depths: one from the user, one from a job advert.
	mustNode(t, s, amy, leadgraph.ActorUser, unit("A司", "c业务组", "A司", "技术中心", "c业务组"))
	mustNode(t, s, amy, leadgraph.ActorUser, unit("A司", "c业务组", "A司", "c业务组"))
	p := person("A司", "王五")
	p.UnitPath = []string{"A司", "技术中心", "c业务组"}
	mustNode(t, s, amy, leadgraph.ActorUser, p)
	mustNode(t, s, amy, leadgraph.ActorUser, leadgraph.Node{
		Kind: leadgraph.KindEvent, Org: "A司", Label: "c业务组并入b业务组",
		UnitPath: []string{"A司", "c业务组"}, OccurredAt: at(-12),
		Intel: []leadgraph.Intel{said("听说c组要并进b组")},
	})

	ls := s.LeadBoard(amy, day)
	if len(ls) != 1 {
		for _, l := range ls {
			t.Logf("row: %s · %s score=%d people=%d", l.Org, l.UnitLabel, l.Score(), len(l.People))
		}
		t.Fatalf("the same group produced %d rows", len(ls))
	}
	if len(ls[0].Paths) != 2 {
		t.Errorf("the two places the name sits are not reported: %+v", ls[0].Paths)
	}
	if len(ls[0].People) != 1 {
		t.Errorf("the people were lost or doubled: %+v", ls[0].People)
	}
	// The signals were gathered once, not once per place.
	var events int
	for _, sg := range ls[0].Signals {
		if sg.Kind == leadgraph.SignalUnitEvent {
			events++
		}
	}
	if events != 1 {
		t.Errorf("the same event counted %d times, inflating the score to %d", events, ls[0].Score())
	}
}

// §08 / CLAUDE.md 代码与 UI 语言 — the screen is Chinese and this package is Go.
// A derived signal carries its measurement, not a ready-made English sentence.
func TestDerivedSignalsCarryMeasurementsNotSentences(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, unit("A司", "c业务组", "A司", "c业务组"))
	src := &fakeSource{name: "board", hosts: []string{"careers.example.com"}, docs: []leadgraph.Document{
		{URL: "https://careers.example.com/jd/1", Kind: leadgraph.DocJobPosting,
			Org: "A司", Unit: "c业务组", Title: "招后端", PostedAt: at(-240), FetchedAt: day},
	}}
	if _, err := s.RunDaily(context.Background(), amy, []leadgraph.Source{src}, leadgraph.FetchRequest{}, day, time.Hour); err != nil {
		t.Fatalf("daily: %v", err)
	}
	ls := s.LeadBoard(amy, day)
	if len(ls) != 1 {
		t.Fatalf("want one lead, got %d", len(ls))
	}
	var found bool
	for _, sg := range ls[0].Signals {
		if sg.Kind != leadgraph.SignalHiringStopped {
			continue
		}
		found = true
		if sg.Because != "" {
			t.Errorf("a derived signal carries a sentence: %q", sg.Because)
		}
		if sg.Days < 200 {
			t.Errorf("the measurement is missing or wrong: %d days", sg.Days)
		}
	}
	if !found {
		t.Fatal("no hiring signal to check")
	}
	// And the screen renders it in the page's own language.
	page := serve(t, s, amy, "/").Body.String()
	if !strings.Contains(page, "天没看到这个组发新招聘") {
		t.Error("the signal is not rendered in the page's language")
	}
	if strings.Contains(page, "no advert seen") {
		t.Error("English prose from Go reached a Chinese page")
	}
}
