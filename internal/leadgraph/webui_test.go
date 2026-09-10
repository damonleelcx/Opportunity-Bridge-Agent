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
