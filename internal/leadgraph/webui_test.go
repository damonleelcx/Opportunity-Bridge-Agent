package leadgraph_test

// P8 fences for docs/20-lead-graph.zh-CN.md §09 图谱.
//
// Two of these read the shipped assets as source rather than executing them.
// That is a weaker instrument - it catches a rule being removed, not a rule
// being wrong - and it is used only where the failure is silent: a graph that
// quietly starts encoding importance looks fine.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

func seeded(t *testing.T) *leadgraph.Store {
	t.Helper()
	s := newStore(t)
	u := mustNode(t, s, amy, leadgraph.ActorUser, unit("A司", "c业务组", "A司", "c业务组"))
	p := person("A司", "王五")
	p.UnitPath = []string{"A司", "c业务组"}
	p.RoleTitle = "组长"
	p.Note = "amy 的私人印象"
	王五 := mustNode(t, s, amy, leadgraph.ActorUser, p)
	b2 := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "b2"))
	mustEdge(t, s, amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeKnows, From: 王五.ID, To: b2.ID, Strength: 2,
		Intel: []leadgraph.Intel{said("认识")},
	})
	_ = u
	return s
}

func serve(t *testing.T, s *leadgraph.Store, v leadgraph.View, path string) *httptest.ResponseRecorder {
	t.Helper()
	h := leadgraph.Handler(s, func(*http.Request) (leadgraph.View, bool) {
		if v.TeamID == "" {
			return leadgraph.View{}, false
		}
		return v, true
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// §09 — the page IS the text list. A reader with no script, a slow link or a
// failed render still gets every person and every badge, because the server
// already put them in the markup.
func TestThePageCarriesTheRosterWithoutAnyScript(t *testing.T) {
	rec := serve(t, seeded(t), amy, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()

	// Everything below is in the HTML the server sent, before one line of
	// script runs.
	script := strings.Index(body, "<script")
	if script < 0 {
		t.Fatal("no script tag at all - this test would prove nothing")
	}
	markup := body[:script]
	for _, want := range []string{"王五", "b2", "c业务组", "组长"} {
		if !strings.Contains(markup, want) {
			t.Errorf("%q reaches the reader only through script", want)
		}
	}
	if !strings.Contains(markup, "待确认") {
		t.Error("the 待确认 badge is script-only")
	}
}

// §09 — the graph starts hidden and is revealed by script. With no script the
// reader is not looking at an empty box where a picture should be.
func TestTheGraphIsHiddenUntilItIsDrawn(t *testing.T) {
	css, err := leadgraph.WebAsset("graph.css")
	if err != nil {
		t.Fatalf("read css: %v", err)
	}
	if !regexp.MustCompile(`#stage\s*\{[^}]*display:\s*none`).MatchString(css) {
		t.Error("the graph stage is not hidden by default")
	}
	if !strings.Contains(css, "#stage.on") {
		t.Error("nothing reveals the stage, so script cannot show it either")
	}
	js, err := leadgraph.WebAsset("graph.js")
	if err != nil {
		t.Fatalf("read js: %v", err)
	}
	if !strings.Contains(js, `stage.classList.add("on")`) {
		t.Error("the script never reveals the stage")
	}
	// And it reveals it only after solving, so the first frame is finished.
	if strings.Index(js, "solve(300)") > strings.Index(js, `stage.classList.add("on")`) {
		t.Error("the stage is revealed before the layout is solved: the reader sees a chaotic frame")
	}
}

// §09 — nothing on the picture encodes importance. A bigger circle is a claim
// about a person that nobody can argue with, because it never says what it means.
func TestNothingOnTheGraphEncodesImportance(t *testing.T) {
	js, err := leadgraph.WebAsset("graph.js")
	if err != nil {
		t.Fatalf("read js: %v", err)
	}
	// Radius is set from a constant, once, and never from data.
	rs := regexp.MustCompile(`setAttribute\("r",\s*([^)]+)\)`).FindAllStringSubmatch(js, -1)
	if len(rs) == 0 {
		t.Fatal("no radius is set at all - this test would prove nothing")
	}
	for _, m := range rs {
		if strings.TrimSpace(m[1]) != "R" {
			t.Errorf("node radius comes from %q, not the constant", m[1])
		}
	}
	if regexp.MustCompile(`stroke-width[^;\n]*(strength|weight|score|rank)`).MatchString(js) {
		t.Error("line thickness is derived from a judgement")
	}
	css, err := leadgraph.WebAsset("graph.css")
	if err != nil {
		t.Fatalf("read css: %v", err)
	}
	// The two encodings that ARE allowed must still be there.
	if !strings.Contains(css, ".mentioned circle") || !strings.Contains(css, "stroke-dasharray") {
		t.Error("the dashed box for a merely-mentioned unit is gone")
	}
	if !regexp.MustCompile(`\.link\.hearsay\s*\{[^}]*stroke-dasharray`).MatchString(css) {
		t.Error("the dashed line for an unconfirmed relationship is gone")
	}
}

// §09 — the payload itself carries no ranking. If it is not in the data, no
// future stylesheet can start encoding it.
func TestTheSnapshotCarriesNoRanking(t *testing.T) {
	s := seeded(t)
	raw, err := json.Marshal(s.Snapshot(amy, time.Now().UTC()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, banned := range []string{`"score"`, `"rank"`, `"weight"`, `"importance"`, `"size"`, `"priority"`} {
		if strings.Contains(body, banned) {
			t.Errorf("the snapshot carries %s", banned)
		}
	}
}

// §09 — the header numbers are computed from the arrays under them, so the
// count and the picture cannot disagree.
func TestCountsMatchWhatIsInThePayload(t *testing.T) {
	s := seeded(t)
	snap := s.Snapshot(amy, time.Now().UTC())
	var people, units, events int
	for _, n := range snap.Nodes {
		switch n.Kind {
		case leadgraph.KindPerson:
			people++
		case leadgraph.KindUnit:
			units++
		case leadgraph.KindEvent:
			events++
		}
	}
	if snap.Counts.People != people || snap.Counts.Units != units || snap.Counts.Events != events {
		t.Errorf("header disagrees with the payload: %+v vs %d/%d/%d", snap.Counts, people, units, events)
	}
	if snap.Counts.Links != len(snap.Links) {
		t.Errorf("link count %d vs %d links", snap.Counts.Links, len(snap.Links))
	}
}

// §04 — the screen obeys the same two boundaries as everything else.
func TestTheScreenStopsAtTheTeamAndTheSeat(t *testing.T) {
	s := seeded(t)

	other := serve(t, s, cara, "/")
	if strings.Contains(other.Body.String(), "王五") {
		t.Error("another team's screen shows this team's graph")
	}

	mate := s.Snapshot(ben, time.Now().UTC())
	var found bool
	for _, n := range mate.Nodes {
		if n.Label == "王五" {
			found = true
			if n.Note != "" {
				t.Errorf("a teammate's screen carries amy's private note: %q", n.Note)
			}
		}
	}
	if !found {
		t.Error("a teammate cannot see a team fact")
	}
	for _, l := range mate.Links {
		if l.Mine || l.Strength != 0 {
			t.Errorf("a teammate's screen carries amy's relationship strength: %+v", l)
		}
	}
}

// §09 — a request the host cannot identify gets nothing, not a default view.
func TestAnUnidentifiedRequestGetsNothing(t *testing.T) {
	s := seeded(t)
	for _, path := range []string{"/", "/data"} {
		rec := serve(t, s, leadgraph.View{}, path)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s served status %d to an unidentified caller", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "王五") {
			t.Errorf("%s leaked data to an unidentified caller", path)
		}
	}
}

// §09 — the page fetches nothing from anywhere. A screen that needs the network
// to render is a screen that fails in the conditions the fallback exists for.
func TestThePageFetchesNothingExternal(t *testing.T) {
	rec := serve(t, seeded(t), amy, "/")
	body := rec.Body.String()
	if regexp.MustCompile(`(src|href)="https?://`).MatchString(body) {
		t.Error("the page pulls something over the network")
	}
	for _, name := range []string{"graph.css", "graph.js"} {
		src, err := leadgraph.WebAsset(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(src, "http://") || strings.Contains(src, "https://") {
			// The SVG namespace is a URL and is not a fetch.
			for _, line := range strings.Split(src, "\n") {
				if (strings.Contains(line, "http://") || strings.Contains(line, "https://")) &&
					!strings.Contains(line, "www.w3.org/2000/svg") {
					t.Errorf("%s reaches the network: %s", name, strings.TrimSpace(line))
				}
			}
		}
	}
}

// §09 — a label is a label. The payload rides in a JSON script block that is
// parsed, not executed.
func TestALabelCannotBecomeScript(t *testing.T) {
	s := newStore(t)
	nasty := person("A司", `</script><script>alert(1)</script>`)
	mustNode(t, s, amy, leadgraph.ActorUser, nasty)

	body := serve(t, s, amy, "/").Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("a label escaped into the page as script")
	}
	if !strings.Contains(body, `type="application/json"`) {
		t.Error("the payload is not carried as JSON, so it is being executed")
	}
}

// §09 — the graph and the list come from ONE read. Two queries would drift
// apart, and the fallback would start contradicting the thing it replaces.
func TestTheGraphAndTheListAreTheSameRead(t *testing.T) {
	s := seeded(t)
	snap := s.Snapshot(amy, time.Now().UTC())

	inGraph := map[string]bool{}
	for _, n := range snap.Nodes {
		if n.Kind == leadgraph.KindPerson {
			inGraph[n.Label] = true
		}
	}
	inList := map[string]bool{}
	for _, g := range snap.Roster {
		for _, p := range g.People {
			inList[p.Label] = true
		}
	}
	for label := range inGraph {
		if !inList[label] {
			t.Errorf("%s is on the graph and not in the fallback list", label)
		}
	}
	for label := range inList {
		if !inGraph[label] {
			t.Errorf("%s is in the fallback list and not on the graph", label)
		}
	}
}

// §09 — the picture must not look TIDIER than the list it is the richer view
// of. Before this, the graph drew only relationship lines: a company's
// structure was in the text and nowhere in the picture, so a group nobody had
// recorded simply did not exist on screen.
//
// Caught by the first walkthrough of a realistic graph.
func TestTheGraphCarriesTheStructureIncludingWhatIsOnlyMentioned(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, unit("A司", "c业务组", "A司", "技术中心", "c业务组"))
	p := person("A司", "王五")
	p.UnitPath = []string{"A司", "技术中心", "c业务组"}
	王五 := mustNode(t, s, amy, leadgraph.ActorUser, p)

	snap := s.Snapshot(amy, time.Now().UTC())

	var mentioned *leadgraph.SnapshotNode
	for i, n := range snap.Nodes {
		if n.Label == "技术中心" {
			mentioned = &snap.Nodes[i]
		}
	}
	if mentioned == nil {
		t.Fatal("a level the user mentioned is missing from the graph but present in the list")
	}
	if mentioned.Presence != leadgraph.PresenceMentioned {
		t.Errorf("it is drawn as %q, so the dashed box never appears", mentioned.Presence)
	}
	if !strings.HasPrefix(mentioned.ID, "mentioned:") {
		t.Errorf("a box that is not a record carries a record-shaped id: %q", mentioned.ID)
	}

	var toPerson, derived int
	for _, l := range snap.Links {
		if l.Derived {
			derived++
		}
		if l.To == 王五.ID && l.Kind == leadgraph.EdgeBelongsTo {
			toPerson++
			if !l.Derived {
				t.Error("a placement inferred from one sentence is presented as a recorded membership")
			}
			if l.Corroboration != leadgraph.Hearsay {
				t.Errorf("a path-only placement claims corroboration %q", l.Corroboration)
			}
		}
	}
	if toPerson != 1 {
		t.Errorf("the person is not connected to their group: %d links", toPerson)
	}
	if derived < 2 {
		t.Errorf("the structure is missing: only %d derived links", derived)
	}
}

// §09 — a derived line is never counted or presented as a recorded one.
func TestDerivedLinesAreDistinguishableFromRecordedOnes(t *testing.T) {
	s := seeded(t)
	snap := s.Snapshot(amy, time.Now().UTC())
	var recorded, derived int
	for _, l := range snap.Links {
		if l.Derived {
			derived++
			if l.Mine || l.Strength != 0 {
				t.Errorf("a derived line carries a personal judgement: %+v", l)
			}
		} else {
			recorded++
		}
	}
	if recorded == 0 || derived == 0 {
		t.Fatalf("want both kinds present to compare, got %d recorded / %d derived", recorded, derived)
	}
}

// Embed mode is the SAME picture with the page's own chrome removed.
//
// 阿桥 renders this page inside a card in the conversation. What must survive is
// everything the picture needs; what must go is what the card already supplies.
//
// #panel is asserted explicitly and is the reason this test exists: graph.js
// returns early when it is missing, so dropping it along with the rest of the
// chrome would have left the embed with no picture at all — and an empty <svg>
// looks exactly like an empty graph, so nothing would have reported it.
func TestTheEmbeddedViewIsTheSamePictureWithoutThePageChrome(t *testing.T) {
	s := seeded(t)
	full := serve(t, s, amy, "/").Body.String()
	embed := serve(t, s, amy, "/?embed=1").Body.String()

	// What the picture needs.
	for _, want := range []string{`id="stage"`, `id="graph"`, `id="panel"`, `id="data"`, "graph.js"} {
		if !strings.Contains(embed, want) {
			t.Errorf("the embed is missing %s, which the picture cannot be drawn without", want)
		}
	}
	// The same data, not a second reading of it.
	if !strings.Contains(embed, "王五") {
		t.Error("the embed carries different data from the page")
	}
	// What the card already supplies.
	if strings.Contains(embed, "<h1>") {
		t.Error("the embed still carries the page title")
	}
	if strings.Contains(embed, `class="wrap"`) {
		t.Error("the embed still carries the text roster the card above it just listed")
	}
	// And the full page is untouched by any of this.
	for _, want := range []string{"<h1>", `class="wrap"`, `id="panel"`} {
		if !strings.Contains(full, want) {
			t.Errorf("embed mode removed %s from the ordinary page too", want)
		}
	}
}

// Anything other than embed=1 is the ordinary page. A mode that turned on for
// any truthy-looking value would strip the roster from a URL somebody typed.
func TestOnlyEmbedEqualsOneTurnsTheChromeOff(t *testing.T) {
	s := seeded(t)
	for _, q := range []string{"/", "/?embed=0", "/?embed=true", "/?embed=", "/?other=1"} {
		if !strings.Contains(serve(t, s, amy, q).Body.String(), `class="wrap"`) {
			t.Errorf("%s dropped the page chrome", q)
		}
	}
}

// The page carries the reader's theme, and only the three states it knows.
//
// It used to carry only a prefers-color-scheme query, which made it ALWAYS
// follow the OS. Embedded inside 阿桥 — whose default is light and whose
// "follow the OS" is an explicit choice — that put a dark picture inside a
// light page for every reader on a dark machine who had not changed anything.
func TestTheGraphPageCarriesTheThemeItWasAskedFor(t *testing.T) {
	s := seeded(t)
	for q, want := range map[string]string{
		"/":                     `data-theme="system"`, // opened on its own: what it always did
		"/?theme=light":         `data-theme="light"`,
		"/?theme=dark":          `data-theme="dark"`,
		"/?theme=system":        `data-theme="system"`,
		"/?theme=purple":        `data-theme="system"`, // not a state; not reflected
		`/?theme="><script>x</`: `data-theme="system"`,
	} {
		body := serve(t, s, amy, q).Body.String()
		if !strings.Contains(body, want) {
			t.Errorf("%s: expected %s", q, want)
		}
	}
	// The allowlist is the point: nothing a caller sends reaches the attribute.
	if body := serve(t, s, amy, `/?theme="><script>x</`).Body.String(); strings.Contains(body, "<script>x<") {
		t.Error("the theme parameter reached the page unfiltered")
	}
}

// Every token has a value in all three theme states.
//
// The palette is defined three times — once bare for light, once behind the OS
// query for "system", once for an explicit dark. A token defined in only one of
// them renders as nothing in the others, which on this page means an invisible
// node or a line that is not there.
func TestTheGraphPageDefinesEveryTokenInEveryThemeState(t *testing.T) {
	css, err := leadgraph.WebAsset("graph.css")
	if err != nil {
		t.Fatalf("read graph.css: %v", err)
	}
	block := func(selector string) map[string]bool {
		i := strings.Index(css, selector)
		if i < 0 {
			t.Fatalf("no %s block in graph.css", selector)
		}
		rest := css[i+len(selector):]
		end := strings.Index(rest, "}")
		if end < 0 {
			t.Fatalf("%s block is unterminated", selector)
		}
		out := map[string]bool{}
		for _, m := range regexp.MustCompile(`--[\w-]+\s*:`).FindAllString(rest[:end], -1) {
			out[strings.TrimSuffix(strings.TrimSpace(m), ":")] = true
		}
		return out
	}
	light := block(":root{")
	if len(light) < 15 {
		t.Fatalf("only %d tokens in the light palette; the fence is not reading the file", len(light))
	}
	// Fonts are not re-declared per theme; only colours flip.
	colour := func(k string) bool { return !strings.HasPrefix(k, "--f-") }
	for _, sel := range []string{`:root[data-theme="system"]{`, `:root[data-theme="dark"]{`} {
		dark := block(sel)
		for k := range light {
			if colour(k) && !dark[k] {
				t.Errorf("%s has no value for %s — it renders as nothing in that theme", sel, k)
			}
		}
	}
}

// The picture must not print a raw enum at a reader.
//
// The detail panel showed "person" and "hearsay" on an otherwise Chinese page —
// found by clicking a node, not by reading anything. The same three
// corroboration words already appear in the page's markup and in 阿桥's result
// cards; a third surface with a fourth vocabulary is how a 名词字典 stops being
// one. See docs/20-lead-graph.zh-CN.md §3.
//
// The expected values are read OUT OF leadgraph.go rather than listed here, so
// a kind added in Go turns this red instead of quietly reaching a reader
// untranslated.
func TestThePictureTranslatesEveryEnumItShows(t *testing.T) {
	src, err := os.ReadFile("leadgraph.go")
	if err != nil {
		t.Fatalf("read leadgraph.go: %v", err)
	}
	js, err := leadgraph.WebAsset("graph.js")
	if err != nil {
		t.Fatalf("read graph.js: %v", err)
	}
	for _, tc := range []struct{ goType, jsTable string }{
		{"NodeKind", "KIND"},
		{"Corroboration", "CORR"},
		{"ContactKind", "CONTACT"},
	} {
		decl := regexp.MustCompile(tc.goType + `\s+=\s+"([a-z_]+)"`)
		values := decl.FindAllStringSubmatch(string(src), -1)
		if len(values) < 3 {
			t.Fatalf("found only %d %s values in leadgraph.go; the fence is not reading the source",
				len(values), tc.goType)
		}
		table := jsObject(t, js, tc.jsTable)
		for _, m := range values {
			if !table[m[1]] {
				t.Errorf("graph.js has no word for %s %q — the reader is shown the enum",
					tc.goType, m[1])
			}
		}
	}
	// The field names in the 待确认 row are an enum too, and they come from
	// store.go's `unconfirmable` table — read from there for the same reason.
	store, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	i := strings.Index(string(store), "var unconfirmable = map[NodeKind][]string{")
	if i < 0 {
		t.Fatal("the unconfirmable table is gone; the fence is reading the wrong thing")
	}
	rest := string(store)[i:]
	fields := regexp.MustCompile(`"([a-z_]+)"`).FindAllStringSubmatch(rest[:strings.Index(rest, "\n}")], -1)
	if len(fields) < 3 {
		t.Fatalf("found only %d unconfirmable fields; the fence is not reading the table", len(fields))
	}
	field := jsObject(t, js, "FIELD")
	for _, m := range fields {
		if !field[m[1]] {
			t.Errorf("graph.js has no word for the unconfirmed field %q — "+
				"the panel shows a Go field name to a reader", m[1])
		}
	}

	// And the raw fields must not reach a row directly, which is how it broke.
	for _, raw := range []string{
		`row("类别", d.kind)`, `row("证实", d.corroboration)`,
		`row("待确认", d.unconfirmed.join(`,
	} {
		if strings.Contains(js, raw) {
			t.Errorf("the detail panel prints a raw enum: %s", raw)
		}
	}
}

// jsObject reads the keys of a `var NAME = { a: "…", b: "…" };` literal.
func jsObject(t *testing.T, src, name string) map[string]bool {
	t.Helper()
	i := strings.Index(src, "var "+name+" = {")
	if i < 0 {
		t.Fatalf("graph.js has no %s table", name)
	}
	rest := src[i:]
	end := strings.Index(rest, "}")
	if end < 0 {
		t.Fatalf("%s table is unterminated", name)
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`(\w+)\s*:`).FindAllStringSubmatch(rest[:end], -1) {
		out[m[1]] = true
	}
	return out
}

// The embed has to fit the box it is given, and the box is an iframe.
//
// The first version sized the two halves with percentages. Percentage heights
// need an ancestor with a resolved height and <body> had none, so they did
// nothing: the svg kept its 60vh/22rem, the document grew to 619px inside a
// 340px frame, and the 详情 panel sat below the fold — reachable only by
// scrolling inside a card nobody would think to scroll. Nothing failed; it just
// was not there.
//
// Source-level, because Go cannot lay out CSS. It guards the height MODEL: a
// resolved height on the root, and a flex column that divides it.
func TestTheEmbedFitsTheFrameItIsGiven(t *testing.T) {
	css, err := leadgraph.WebAsset("graph.css")
	if err != nil {
		t.Fatalf("read graph.css: %v", err)
	}
	// Comments are stripped FIRST — for the second time in this session. The
	// note above these rules quotes the very percentage it warns against, so a
	// fence reading the raw file finds the bug it is supposed to forbid sitting
	// in its own explanation. A fence that can be satisfied, or failed, by
	// prose is not measuring anything.
	css = stripCSSComments(css)
	// From the FIRST embed selector, not from "body.embed": the rule that gives
	// the root its height names html.embed-root first, so slicing at body.embed
	// cut the very selector this fence has to see.
	i := strings.Index(css, "html.embed-root")
	if j := strings.Index(css, "body.embed"); i < 0 || (j >= 0 && j < i) {
		i = j
	}
	if i < 0 {
		t.Fatal("graph.css has no embed rules; this fence no longer guards anything")
	}
	embed := css[i:]
	for _, want := range []struct{ rule, why string }{
		{"height: 100%", "nothing gives the embed a resolved height, so any height inside it is measured against auto"},
		{"html.embed-root", "the ROOT has no height, so height:100% on body resolves against auto and does nothing"},
		{"display: flex", "the two halves are not laid out as a column, so they cannot divide the frame"},
		{"flex: 1 1 auto", "the picture does not take the space left over"},
		{"min-height: 0", "a flex child defaults to its content's height and will overflow the frame"},
	} {
		if !strings.Contains(embed, want.rule) {
			t.Errorf("embed CSS has no %q — %s", want.rule, want.why)
		}
	}
	// Percentages on an auto-height ancestor are the bug, by name.
	if strings.Contains(embed, "#stage { height: 74%") || strings.Contains(embed, "#stage{height:74%") {
		t.Error("the picture is sized with a percentage of an auto-height body again")
	}
}

// stripCSSComments removes /* … */ so a fence reads rules, not notes.
func stripCSSComments(src string) string {
	var b strings.Builder
	for {
		i := strings.Index(src, "/*")
		if i < 0 {
			b.WriteString(src)
			return b.String()
		}
		b.WriteString(src[:i])
		j := strings.Index(src[i:], "*/")
		if j < 0 {
			return b.String()
		}
		src = src[i+j+2:]
	}
}
