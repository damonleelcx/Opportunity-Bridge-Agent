package leadgraph_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

// Planning from recorded readings of the synthetic mind map. What each reading
// holds was counted from the files themselves before these were written; see
// testdata/screenshot/README.md.

var picture = []byte("the synthetic mind-map png stands in here; only its digest is used")

func planShot(t *testing.T, s *leadgraph.Store, reading string, ov leadgraph.ImportOverrides) leadgraph.ImportPlan {
	t.Helper()
	plan, err := s.PlanScreenshot(amy, "导图截图.png", picture, readFixture(t, reading), ov)
	if err != nil {
		t.Fatalf("plan %s: %v", reading, err)
	}
	return plan
}

// rowOf is the topic number of the person the reading named.
func rowOf(t *testing.T, reading, name string) int {
	t.Helper()
	tr, err := leadgraph.ParseTranscript(readFixture(t, reading))
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range tr.Nodes {
		if n.Name == name {
			return i + 1
		}
	}
	t.Fatalf("%s names nobody %q", reading, name)
	return 0
}

type planned struct {
	label, org, title, note string
	row                     int
}

func people(plan leadgraph.ImportPlan) map[string]planned {
	out := map[string]planned{}
	for i, p := range plan.Proposals {
		out[p.Candidate.Label] = planned{
			label: p.Candidate.Label, org: p.Candidate.Org, title: p.Candidate.RoleTitle,
			note: plan.Notes[i], row: plan.RowOf[i],
		}
	}
	return out
}

func links(plan leadgraph.ImportPlan) []string {
	var out []string
	for _, l := range plan.Links {
		out = append(out, l.From.Label+"→"+l.To.Label)
	}
	return out
}

const goodReading = "reading-qwen3.7-plus.json"

func TestAScreenshotIsPlannedFromItsReading(t *testing.T) {
	s := newStore(t)
	plan := planShot(t, s, goodReading, leadgraph.ImportOverrides{})
	if plan.Source != leadgraph.SourceScreenshot {
		t.Errorf("source = %q, want screenshot", plan.Source)
	}

	// 25 people read; the two who have left are held, so 23 are planned.
	got := people(plan)
	if len(plan.Proposals) != 23 {
		t.Fatalf("planned %d people, want 23 (25 read, 2 held): %v", len(plan.Proposals), got)
	}
	for _, p := range plan.Proposals {
		if p.Decision != leadgraph.DecideCreate {
			t.Errorf("%s: decision %s on an empty graph", p.Candidate.Label, p.Decision)
		}
	}
	for name, want := range map[string]planned{
		"韩子墨":    {org: "星河互联 NOVA", title: "高级视觉"},
		"周予安":    {org: "远航科技", title: "资深ux"},
		"程昱":     {org: "远航科技", title: "创新设计部 负责人"},
		"Marcus": {org: "青岩智能", title: "Creative Team Leader"},
		"宋清和":    {org: "青岩智能", title: "UX专家"},
	} {
		if got[name].org != want.org || got[name].title != want.title {
			t.Errorf("%s planned as %+v, want company %q and title %q", name, got[name], want.org, want.title)
		}
	}

	// The note keeps the topic verbatim, the note hung under the person, and the marks.
	han := got["韩子墨"].note
	for _, want := range []string{"韩子墨 高级视觉", "附注：qmx 品牌", "导图标记：整个节点紫色底"} {
		if !strings.Contains(han, want) {
			t.Errorf("韩子墨's note %q is missing %q", han, want)
		}
	}
	if !strings.Contains(got["沈若溪"].note, "41 女 沈若溪 穿戴设计负责人，华为背景") {
		t.Errorf("a leading number must be kept verbatim, not parsed: %q", got["沈若溪"].note)
	}
	if got["qmx 品牌"].label != "" {
		t.Error("a note under a person was imported as a person")
	}

	// Held, with the words that are the reason.
	held := map[string]bool{}
	for _, h := range plan.Held {
		held[h.Text] = h.Reason == leadgraph.HoldDeparted
	}
	for _, text := range []string{"林默然，设计总监、视觉、包装（已离职去Brightly）", "7 陈望舒 （24年离开）追觅品牌视觉"} {
		if !held[text] {
			t.Errorf("%q was not held as departed: %+v", text, plan.Held)
		}
	}

	// The placeholder is reported, not planned and not dropped.
	if len(plan.Skipped) != 1 || plan.Skipped[0].Reason != leadgraph.SkipPlaceholder || plan.Skipped[0].Detail != "输入文本" {
		t.Errorf("skipped = %+v, want only the placeholder", plan.Skipped)
	}

	// Reporting lines: child reports to parent, both imported.
	want := []string{
		"苏晓棠→Leo Chen", "顾一鸣→Leo Chen", "周予安→陆行舟", "江北辰→陆行舟", "秦朗→陆行舟",
		"Mika→陆行舟", "许南风→陆行舟", "Marcus→白砚秋", "杜明远→白砚秋", "宋清和→杜明远",
	}
	if g := strings.Join(links(plan), " "); g != strings.Join(want, " ") {
		t.Errorf("reporting lines\n got %s\nwant %s", g, strings.Join(want, " "))
	}
	for _, l := range plan.Links {
		if l.Kind != leadgraph.EdgeReportsTo {
			t.Errorf("%s→%s is %s, want reports_to", l.From.Label, l.To.Label, l.Kind)
		}
	}
	// 孟川 is imported; his manager 陈望舒 is held. Said, not dropped.
	if len(plan.UnlinkedRows) != 1 || plan.UnlinkedRows[0].Row != got["孟川"].row ||
		plan.UnlinkedRows[0].Reason != leadgraph.SkipManagerNotImported {
		t.Errorf("unlinked = %+v, want 孟川 (row %d) as manager_not_imported", plan.UnlinkedRows, got["孟川"].row)
	}

	// Every correct name stands on its own in its topic: nothing to flag.
	if len(plan.Checks) != 0 {
		t.Errorf("a correct reading raised checks: %+v", plan.Checks)
	}
	// The evidence says where in the picture each person came from.
	if ex := plan.Proposals[0].Candidate.Intel[0].Excerpt; !strings.Contains(ex, "个主题：") {
		t.Errorf("excerpt %q does not name the topic", ex)
	}
}

func TestAHighlightIsRecordedNotInterpreted(t *testing.T) {
	s := newStore(t)
	for _, reading := range []string{goodReading, "reading-qwen3.8-flash-split.json", "reading-qwen3.7-plus-name-dropped.json"} {
		for i, note := range planShot(t, s, reading, leadgraph.ImportOverrides{}).Notes {
			for _, word := range []string{"目标人选", "重点关注"} {
				if strings.Contains(note, word) {
					t.Errorf("%s: note %d reads a colour as %q: %q", reading, i, word, note)
				}
			}
			// The production-path reading says line_color "blue" on every topic.
			if strings.Contains(note, "蓝色连线") {
				t.Errorf("%s: note %d carries the default connector colour as a mark: %q", reading, i, note)
			}
		}
	}
	// The flash reading reported the inline highlights; they are written as read.
	got := people(planShot(t, s, "reading-qwen3.8-flash-split.json", leadgraph.ImportOverrides{}))
	if n := got["周予安"].note; !strings.Contains(n, "紫色底「34」「海硕」") {
		t.Errorf("inline highlights not recorded as read: %q", n)
	}
	if n := got["秦朗"].note; !strings.Contains(n, "橙色字") || !strings.Contains(n, "加粗") {
		t.Errorf("orange bold text not recorded: %q", n)
	}
}

func TestADepartedPersonIsImportedOnlyWhenSomebodySaysSo(t *testing.T) {
	s := newStore(t)
	chen := rowOf(t, goodReading, "陈望舒")
	plan := planShot(t, s, goodReading, leadgraph.ImportOverrides{Include: []int{chen}})
	got := people(plan)
	if got["陈望舒"].label == "" {
		t.Fatal("陈望舒 was included and still not planned")
	}
	if got["林默然"].label != "" {
		t.Error("including one departed person included the other")
	}
	if !strings.Contains(strings.Join(links(plan), " "), "孟川→陈望舒") {
		t.Errorf("including the manager did not draw 孟川's line: %v", links(plan))
	}
	if len(plan.UnlinkedRows) != 0 {
		t.Errorf("unlinked rows remain after the manager was included: %+v", plan.UnlinkedRows)
	}
}

// A correction to a name is the name the line carries, and a name the picture
// does not say is flagged even when a person typed it.
func TestACorrectedNameIsTheNameOnTheLine(t *testing.T) {
	s := newStore(t)
	du := rowOf(t, goodReading, "杜明远")
	plan := planShot(t, s, goodReading, leadgraph.ImportOverrides{
		Rows: map[int]map[leadgraph.Column]string{du: {leadgraph.ColLabel: "杜明原"}},
	})
	if l := strings.Join(links(plan), " "); !strings.Contains(l, "宋清和→杜明原") || !strings.Contains(l, "杜明原→白砚秋") {
		t.Errorf("lines do not carry the corrected name: %v", links(plan))
	}
	if len(plan.Checks) != 1 || plan.Checks[0].Row != du || plan.Checks[0].Detail != "杜明原" {
		t.Errorf("checks = %+v, want the corrected row flagged as not written in the topic", plan.Checks)
	}
}

func TestANameMissingACharacterIsFlagged(t *testing.T) {
	s := newStore(t)
	const reading = "reading-qwen3.7-plus-name-dropped.json"
	plan := planShot(t, s, reading, leadgraph.ImportOverrides{})
	row := rowOf(t, reading, "罗子")
	if len(plan.Checks) != 1 || plan.Checks[0].Row != row ||
		plan.Checks[0].Check != leadgraph.CheckNameNotInText || plan.Checks[0].Detail != "罗子" {
		t.Fatalf("checks = %+v, want 罗子 (row %d) flagged: the topic says 罗子衿", plan.Checks, row)
	}
	if people(plan)["罗子"].label == "" {
		t.Error("a flag must not stop the row; it is a proposal for a person to correct")
	}
}

// qwen3.8-flash split "视觉总 中台负责人（2023-10-01至今） 白砚秋 …" in two. The plan
// shows exactly what was read: the first half as a skipped non-person, and
// 白砚秋 without a title. Nothing is patched back together.
func TestAFaultyReadingIsPlannedAsItWasRead(t *testing.T) {
	s := newStore(t)
	plan := planShot(t, s, "reading-qwen3.8-flash-split.json", leadgraph.ImportOverrides{})
	var notPerson []string
	for _, sk := range plan.Skipped {
		if sk.Reason == leadgraph.SkipNotAPerson {
			notPerson = append(notPerson, sk.Detail)
		}
	}
	if fmt.Sprint(notPerson) != "[视觉总 中台负责人（2023-10-01至今）]" {
		t.Errorf("skipped as not a person: %v", notPerson)
	}
	bai := people(plan)["白砚秋"]
	if bai.label == "" || bai.title != "" {
		t.Errorf("白砚秋 planned as %+v; the reading gave no title and none may be invented", bai)
	}
	if l := strings.Join(links(plan), " "); !strings.Contains(l, "Marcus→白砚秋") || !strings.Contains(l, "杜明远→白砚秋") {
		t.Errorf("lines under 白砚秋 missing: %v", links(plan))
	}
}

// Two readings of one picture are one source. Keyed on the reading they would be
// two, and a reporting line would corroborate itself on a re-upload.
func TestTwoReadingsOfOnePictureAreOneSource(t *testing.T) {
	s := newStore(t)
	a := planShot(t, s, goodReading, leadgraph.ImportOverrides{})
	b := planShot(t, s, "reading-qwen3.7-plus-name-dropped.json", leadgraph.ImportOverrides{})
	if a.Digest != b.Digest {
		t.Errorf("two readings of one picture have different digests: %s, %s", a.Digest, b.Digest)
	}
	if a.Proposals[0].Candidate.Intel[0].TurnRef != "import:"+a.Digest {
		t.Errorf("evidence is not keyed on the picture: %s", a.Proposals[0].Candidate.Intel[0].TurnRef)
	}
}

func TestPlanningAScreenshotWritesNothing(t *testing.T) {
	s := newStore(t)
	planShot(t, s, goodReading, leadgraph.ImportOverrides{})
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{})); n != 0 {
		t.Errorf("planning wrote %d records", n)
	}
}
