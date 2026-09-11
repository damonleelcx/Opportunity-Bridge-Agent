package leadgraph

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ---- planning from a screenshot's reading ----
//
// A reading becomes a table - one row per person, in the alias table's own
// header words - and is planned by planTable like any spreadsheet. What only a
// picture has rides on the table: the topic each row was, who hangs under
// whom, who has left, and the verbatim text. See the WHY in screenshot.go.

// ImportSource says what a plan was read from. Empty is a spreadsheet, so the
// field is absent from every plan that existed before screenshots.
type ImportSource string

const (
	SourceTable      ImportSource = ""
	SourceScreenshot ImportSource = "screenshot"
)

// HeldRow is a person kept out of the import until somebody says to include
// them, with the text that is the reason.
type HeldRow struct {
	Row    int    `json:"row"`
	Reason string `json:"reason"`
	Text   string `json:"text"`
}

// RowCheck is something on a row worth a closer look that does not stop it.
type RowCheck struct {
	Row    int    `json:"row"`
	Check  string `json:"check"`
	Detail string `json:"detail,omitempty"`
}

// Reasons and checks a screenshot adds. Keys, not sentences: the interface
// words them.
const (
	HoldDeparted = "departed"
	// CheckNameNotInText: the proposed name is not written, as a word of its
	// own, in the topic it was read from.
	CheckNameNotInText = "name_not_in_text"

	SkipPlaceholder        = "placeholder"
	SkipNotAPerson         = "not_a_person"
	SkipUnrecognisedTopic  = "unrecognised_topic"
	SkipManagerNotImported = "manager_not_imported"
)

// screenshotHeader is the header of the table a reading becomes. The alias
// table's own first words, so mapColumns reads it like any file.
var screenshotHeader = []string{"姓名", "公司", "职务", "备注"}

// PlanScreenshot works out what importing a screenshot would do, from a model's
// reading of it. It writes nothing and calls no model.
//
// The source is the PICTURE, not the reading: two readings of one screenshot
// differ in small ways, and keying the evidence on the text would make them two
// independent sources, which would corroborate a reporting line on the strength
// of the same picture uploaded twice.
func (s *Store) PlanScreenshot(v View, fileName string, image []byte, reading string, ov ImportOverrides) (ImportPlan, error) {
	return s.planScreenshot(v, fileName, digestOf(string(image)), reading, ov)
}

func (s *Store) planScreenshot(v View, fileName, digest, reading string, ov ImportOverrides) (ImportPlan, error) {
	if !v.valid() {
		return ImportPlan{}, ErrViewRequired
	}
	tr, err := ParseTranscript(reading)
	if err != nil {
		return ImportPlan{}, err
	}
	return s.planTable(v, fileName, digest, tableFromTranscript(tr), ov), nil
}

// kind is what a topic is taken to be. A topic marked as a placeholder is one
// whatever kind was proposed for it.
func (n TranscriptNode) kind() TopicKind {
	if n.Placeholder {
		return TopicPlaceholder
	}
	return TopicKind(strings.ToLower(strings.TrimSpace(string(n.Kind))))
}

// tableFromTranscript lays a reading out as rows of people.
//
//   - A person is a row: the proposed name and title, the company above them,
//     and a note holding the topic's text verbatim, any notes hung under them,
//     and how the topic was marked.
//   - A person under a person is a reporting line (child reports to parent).
//     Only person under person: "韩子墨 → qmx 品牌" is a note, not a report.
//   - A note under anything but a person, a placeholder, or a kind nobody
//     proposed is reported as skipped with its text, never silently dropped.
//   - A departed person is held until somebody includes them.
func tableFromTranscript(tr Transcript) *table {
	t := &table{
		header: screenshotHeader, source: SourceScreenshot,
		text: map[int]string{}, held: map[int]string{}, parent: map[int]int{},
	}
	byID := make(map[int]TranscriptNode, len(tr.Nodes))
	lineOf := make(map[int]int, len(tr.Nodes))
	for i, n := range tr.Nodes {
		byID[n.ID] = n
		lineOf[n.ID] = i + 1
	}
	parentKind := func(n TranscriptNode) TopicKind {
		if n.Parent == nil {
			return ""
		}
		return byID[*n.Parent].kind()
	}

	notesUnder := map[int][]string{}
	for i, n := range tr.Nodes {
		switch n.kind() {
		case TopicPerson, TopicOrg, TopicRoot:
		case TopicAttribute:
			if parentKind(n) == TopicPerson {
				notesUnder[*n.Parent] = append(notesUnder[*n.Parent], strings.TrimSpace(n.Text))
				continue
			}
			t.skipped = append(t.skipped, SkippedRow{Row: i + 1, Reason: SkipNotAPerson, Detail: strings.TrimSpace(n.Text)})
		case TopicPlaceholder:
			t.skipped = append(t.skipped, SkippedRow{Row: i + 1, Reason: SkipPlaceholder, Detail: strings.TrimSpace(n.Text)})
		default:
			t.skipped = append(t.skipped, SkippedRow{Row: i + 1, Reason: SkipUnrecognisedTopic,
				Detail: string(n.Kind) + ": " + strings.TrimSpace(n.Text)})
		}
	}

	for i, n := range tr.Nodes {
		if n.kind() != TopicPerson {
			continue
		}
		line := i + 1
		note := []string{strings.TrimSpace(n.Text)}
		if extra := notesUnder[n.ID]; len(extra) > 0 {
			note = append(note, "附注："+strings.Join(extra, "；"))
		}
		if marks := describeMarks(n); marks != "" {
			note = append(note, "导图标记："+marks)
		}
		t.rows = append(t.rows, []string{
			strings.TrimSpace(n.Name), companyAbove(n, byID), strings.TrimSpace(n.Title), strings.Join(note, " · "),
		})
		t.lines = append(t.lines, line)
		t.text[line] = n.Text
		if n.Departed {
			t.held[line] = HoldDeparted
		}
		if parentKind(n) == TopicPerson {
			t.parent[line] = lineOf[*n.Parent]
		}
	}
	return t
}

// companyAbove is the nearest company above a topic, as written in the picture.
func companyAbove(n TranscriptNode, byID map[int]TranscriptNode) string {
	for steps := 0; n.Parent != nil && steps <= len(byID); steps++ {
		n = byID[*n.Parent]
		if n.kind() == TopicOrg {
			return strings.TrimSpace(n.Text)
		}
	}
	return ""
}

// ---- marks ----
//
// WHY MARKS ARE DESCRIBED AND NEVER INTERPRETED
//
//	The owner first said purple means "target" and yellow means "priority".
//	Then the spike showed every model tested confusing a highlight on part of
//	a line with a fill on the whole topic, and the real screenshot highlights
//	an age ("34") and a degree ("海硕") inline. Reading purple as "target"
//	would mark people for their age. So a mark is written into the private
//	note as what it looks like, and what it means stays the owner's call.
//	Owner's decision, 2026-09-11.

// colourWords names colours in the reader's words. A colour missing here is
// written as the model gave it, rather than dropped.
var colourWords = map[string]string{
	"purple": "紫色", "violet": "紫色", "lavender": "紫色", "yellow": "黄色", "orange": "橙色",
	"green": "绿色", "red": "红色", "blue": "蓝色", "grey": "灰色", "gray": "灰色",
	"pink": "粉色", "black": "黑色", "white": "白色", "brown": "棕色",
}

// unmarked are values a reading reports for how every topic is drawn. The live
// production-path reading on 2026-09-11 said line_color "blue" on every topic,
// though the prompt says to leave it empty when blue: written into notes, every
// person would carry "蓝色连线", which is noise that buries the marks that mean
// something.
var unmarked = map[string]map[string]bool{
	"fill": {"": true, "white": true, "none": true, "transparent": true},
	"text": {"": true, "black": true},
	"line": {"": true, "blue": true},
}

func colourWord(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	if w, ok := colourWords[c]; ok {
		return w
	}
	return c
}

func describeMarks(n TranscriptNode) string {
	var parts []string
	if f := strings.ToLower(strings.TrimSpace(n.Fill)); !unmarked["fill"][f] {
		parts = append(parts, "整个节点"+colourWord(f)+"底")
	}
	var order []string
	byColour := map[string][]string{}
	for _, h := range n.Highlights {
		text := strings.TrimSpace(h.Text)
		if text == "" {
			continue
		}
		c := colourWord(h.Color)
		if _, seen := byColour[c]; !seen {
			order = append(order, c)
		}
		byColour[c] = append(byColour[c], "「"+text+"」")
	}
	for _, c := range order {
		parts = append(parts, c+"底"+strings.Join(byColour[c], ""))
	}
	if c := strings.ToLower(strings.TrimSpace(n.TextColor)); !unmarked["text"][c] {
		parts = append(parts, colourWord(c)+"字")
	}
	if n.Bold {
		parts = append(parts, "加粗")
	}
	if c := strings.ToLower(strings.TrimSpace(n.LineColor)); !unmarked["line"][c] {
		parts = append(parts, colourWord(c)+"连线")
	}
	return strings.Join(parts, "；")
}

// ---- names ----

// standsAlone reports whether name is written in text as a word of its own.
//
// Not a substring check, on evidence: through the production path on 2026-09-11
// the model read "45 罗子衿 （杭州）设计中心负责人" and proposed the name "罗子".
// "罗子" IS in the text, as the first two characters of the real name, so a
// substring check passes the one error it exists to catch. Here the characters
// either side of the name must not continue it - no Han character touching a
// Han name, no letter touching a Latin one.
//
// It flags, it does not refuse. Chinese often writes a name straight into the
// sentence ("张三负责设计"), and a flag there costs a second look.
func standsAlone(name, text string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(name)
	last, _ := utf8.DecodeLastRuneInString(name)
	for from := 0; from <= len(text); {
		i := strings.Index(text[from:], name)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(name)
		before, _ := utf8.DecodeLastRuneInString(text[:start])
		after, _ := utf8.DecodeRuneInString(text[end:])
		if (start == 0 || !continues(before, first)) && (end == len(text) || !continues(after, last)) {
			return true
		}
		from = start + utf8.RuneLen(first)
	}
	return false
}

// continues reports whether two adjacent characters belong to one word.
func continues(a, b rune) bool {
	han := func(r rune) bool { return unicode.Is(unicode.Han, r) }
	latin := func(r rune) bool { return unicode.IsLetter(r) && !han(r) }
	return (han(a) && han(b)) || (latin(a) && latin(b))
}

// ---- reporting lines ----

// planLinks turns who-hangs-under-whom into reporting lines between the rows
// that will be imported, named as they will be after corrections.
//
// A line is drawn only when both people are in the import. When the person is
// but their manager is not - held, skipped or left out - the line is reported
// rather than dropped: "孟川 reports to somebody not imported" is something a
// person can act on by including that somebody.
func planLinks(plan *ImportPlan, t *table, placed map[int]Node, fileName, fileRef string) {
	if len(t.parent) == 0 {
		return
	}
	children := make([]int, 0, len(t.parent))
	for child := range t.parent {
		children = append(children, child)
	}
	sort.Ints(children)
	for _, child := range children {
		boss := t.parent[child]
		c, ok := placed[child]
		if !ok {
			// The person is not imported; their own row says why.
			continue
		}
		b, ok := placed[boss]
		if !ok {
			plan.UnlinkedRows = append(plan.UnlinkedRows, SkippedRow{
				Row: child, Reason: SkipManagerNotImported, Detail: itoa(boss),
			})
			continue
		}
		plan.Links = append(plan.Links, LinkInput{
			Kind:    EdgeReportsTo,
			From:    Endpoint{Kind: KindPerson, Org: c.Org, Label: c.Label},
			To:      Endpoint{Kind: KindPerson, Org: b.Org, Label: b.Label},
			Context: "导图里挂在其下",
			Intel: []Intel{{
				Kind: IntelImported, TurnRef: fileRef,
				Excerpt: fmt.Sprintf("%s 第 %d 个主题「%s」挂在第 %d 个主题「%s」下面",
					fileName, child, t.text[child], boss, t.text[boss]),
			}},
		})
		if plan.LinkRow == nil {
			plan.LinkRow = map[int]int{}
		}
		plan.LinkRow[len(plan.Links)-1] = child
	}
}
