package httpapi_test

// Fences over 猎源图谱's screen.
//
// The screen existed, complete, for an entire release without a URL: leadgraph.
// Handler had exactly one caller in the tree and it was a test. The tests were
// green because the test was also the only consumer, which is the shape this
// file exists to stop repeating - every assertion below goes through the real
// mux, the real gate and the real ownership checks.
//
// The claims:
//  1. the screen of its own is GONE (拍板 2026-09-10) and nothing links to it:
//     everything a reader can act on is drawn by 阿桥 in the conversation;
//  2. what survives - the embed the card loads, and the assets and snapshot it
//     needs - is still served, and still only to the right person: not another
//     account, not another role, and not an anonymous request;
//  3. the embed and the conversation read the SAME book.
//
// Claim 1 replaced "a recruiter reaches their own roster in the markup". That
// guarantee still exists and is still fenced, one layer down, by
// leadgraph.TestThePageCarriesTheRosterWithoutAnyScript: the package still
// serves a whole page to anybody who mounts it. What changed is which surfaces
// THIS product exposes.

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tools"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/web"
)

// seedPerson puts one person in the book the given view reads.
func seedPerson(t *testing.T, graph *leadgraph.Store, v leadgraph.View, org, name, title string) {
	t.Helper()
	_, err := graph.UpsertNode(v, leadgraph.ActorUser, leadgraph.Node{
		Kind: leadgraph.KindPerson, Org: org, Label: name, RoleTitle: title,
		UnitPath: []string{org, "c业务组"},
		Intel: []leadgraph.Intel{{
			Kind: leadgraph.IntelUserSaid, TurnRef: "t1", Excerpt: name + " 在 " + org,
		}},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// 猎源图谱 has no screen of its own, and the embed still does.
//
// The two live in the same route on purpose - the card loads the same document
// the screen was - so "remove the screen" is one query-string apart from
// "remove the picture". This fence holds both halves at once, because a change
// that gets it half right is invisible from either side alone.
func TestTheStandaloneGraphScreenIsGone(t *testing.T) {
	ts, graph, _ := graphServer(t)
	c := signedIn(t, ts, "fay-recruiter")
	ses, seat := recruiterSession(t, c, ts, domain.RoleRecruiter)
	seedPerson(t, graph, seat, "A司", "王五", "组长")
	base := ts.URL + "/app/sessions/" + ses + "/graph"

	// Gone, and gone by every spelling somebody might have bookmarked.
	for _, q := range []string{"/", "", "/?embed=0", "/?embed=true", "/?embed=", "/?theme=dark"} {
		res, err := c.Get(base + q)
		if err != nil {
			t.Fatalf("get %q: %v", q, err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%q still serves the standalone screen: %d", q, res.StatusCode)
		}
		// A 404 that does not say where the thing went is a dead end, and this
		// one has a live answer: it is in the conversation.
		if !strings.Contains(string(b), "conversation") {
			t.Errorf("%q 404s without telling the reader where the graph is now: %s", q, b)
		}
	}

	// And the embed - the whole reason this route still exists - still works.
	res, err := c.Get(base + "/?embed=1")
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the card's own URL was removed along with the screen: %d %s", res.StatusCode, b)
	}
	if !strings.Contains(string(b), "王五") || !strings.Contains(string(b), `id="graph"`) {
		t.Error("the embed no longer carries a picture that can be drawn")
	}
}

// The redirect to the slashed form must carry the query with it.
//
// The unslashed URL is redirected because the document's own links (graph.css,
// graph.js, data) are relative and only resolve correctly under a trailing
// slash. Now that ?embed=1 is what separates the card from a 404, a redirect
// that dropped the query would send the card into the 404 - and it would look
// like the embed had been removed rather than like the redirect had eaten one
// parameter.
func TestTheRedirectToTheSlashedFormKeepsTheQuery(t *testing.T) {
	ts, graph, _ := graphServer(t)
	c := signedIn(t, ts, "gus-recruiter")
	ses, seat := recruiterSession(t, c, ts, domain.RoleRecruiter)
	seedPerson(t, graph, seat, "A司", "王五", "组长")

	res, err := c.Get(ts.URL + "/app/sessions/" + ses + "/graph?embed=1&theme=light")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the unslashed embed URL does not arrive: %d %s", res.StatusCode, b)
	}
	if got := res.Request.URL.Path; !strings.HasSuffix(got, "/graph/") {
		t.Errorf("landed on %q, so the document's relative links resolve one level up", got)
	}
	if got := res.Request.URL.Query().Get("embed"); got != "1" {
		t.Errorf("the redirect dropped embed=1 (landed with %q), which is the 404 path", got)
	}
	if !strings.Contains(string(b), `id="graph"`) {
		t.Error("what arrived cannot be drawn")
	}
}

// The stylesheet, the script and the snapshot the picture is drawn from all
// hang off the same URL. A page whose assets 404 is a page nobody can use.
func TestTheGraphScreenServesItsOwnAssetsAndData(t *testing.T) {
	ts, graph, _ := graphServer(t)
	c := signedIn(t, ts, "hal-recruiter")
	ses, seat := recruiterSession(t, c, ts, domain.RoleRecruiter)
	seedPerson(t, graph, seat, "A司", "王五", "组长")
	base := ts.URL + "/app/sessions/" + ses + "/graph"

	for _, tc := range []struct{ path, ctype string }{
		{"/graph.css", "text/css"},
		{"/graph.js", "text/javascript"},
	} {
		res, err := c.Get(base + tc.path)
		if err != nil {
			t.Fatalf("get %s: %v", tc.path, err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s: %d", tc.path, res.StatusCode)
			continue
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, tc.ctype) {
			t.Errorf("%s served as %q", tc.path, ct)
		}
		if len(body) == 0 {
			t.Errorf("%s is empty", tc.path)
		}
	}

	// And the addresses the BROWSER will resolve, not the ones this test can
	// spell. The template links `graph.css` relatively; under a prefix that is
	// only correct because the page is served with a trailing slash. Resolving
	// them the way a browser does is what makes that a fence rather than a
	// reasoned guess.
	page, err := c.Get(base + "/?embed=1")
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	html, _ := io.ReadAll(page.Body)
	page.Body.Close()
	refs := regexp.MustCompile(`(?:href|src)="([^"]+\.(?:css|js))"`).FindAllSubmatch(html, -1)
	if len(refs) == 0 {
		t.Fatal("the page references no assets at all - this check would prove nothing")
	}
	for _, m := range refs {
		ref, err := page.Request.URL.Parse(string(m[1]))
		if err != nil {
			t.Errorf("%q is not a URL: %v", m[1], err)
			continue
		}
		got, err := c.Get(ref.String())
		if err != nil {
			t.Errorf("get %s: %v", ref, err)
			continue
		}
		got.Body.Close()
		if got.StatusCode != http.StatusOK {
			t.Errorf("the page asks the browser for %s and gets %d", ref, got.StatusCode)
		}
	}

	res, err := c.Get(base + "/data")
	if err != nil {
		t.Fatalf("data: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("data: %d", res.StatusCode)
	}
	var snap leadgraph.Snapshot
	if err := json.NewDecoder(res.Body).Decode(&snap); err != nil {
		t.Fatalf("data is not a snapshot: %v", err)
	}
	if len(snap.Nodes) == 0 {
		t.Error("the picture would be drawn from an empty snapshot")
	}
}

// The page and the tools must be looking at the same book.
//
// The account is placed in a FIRM on purpose, for the same reason the upload
// fence does it: with no org both sides compute the same team by accident, so a
// page that had stopped sharing the rule would still look correct.
func TestTheGraphScreenAndTheConversationReadTheSameBook(t *testing.T) {
	ts, graph, st := graphServer(t)
	c := signedIn(t, ts, "ivy-recruiter")
	if err := st.SetAccountOrg("ivy-recruiter", "Acme猎头"); err != nil {
		t.Fatalf("place: %v", err)
	}
	ses, seat := recruiterSession(t, c, ts, domain.RoleRecruiter)
	// What the CONVERSATION computes, through the one shared rule.
	seat.TeamID = tools.GraphTeamFor(st, seat.SeatID)
	seedPerson(t, graph, seat, "A司", "赵六", "总监")

	res, err := c.Get(ts.URL + "/app/sessions/" + ses + "/graph/?embed=1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(b), "赵六") {
		t.Errorf("the picture reads a different book from the conversation: %d", res.StatusCode)
	}
}

// An anonymous request must not reach it. This drills the GATE, not the
// handler: /app/... is otherwise the public static shell.
func TestTheGraphScreenNeedsAnAccount(t *testing.T) {
	ts, graph, _ := graphServer(t)
	c := signedIn(t, ts, "jan-recruiter")
	ses, seat := recruiterSession(t, c, ts, domain.RoleRecruiter)
	seedPerson(t, graph, seat, "A司", "王五", "组长")

	// The harness's own client: it trusts the test certificate and has no
	// cookie jar, so it is signed in as nobody.
	res, err := ts.Client().Get(ts.URL + "/app/sessions/" + ses + "/graph/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an anonymous request reached the graph screen: %d", res.StatusCode)
	}
	if strings.Contains(string(b), "王五") {
		t.Error("the roster was served to a request with no account")
	}
}

// Somebody else's session is not a way in.
func TestTheGraphScreenRefusesSomebodyElsesSession(t *testing.T) {
	ts, graph, _ := graphServer(t)
	owner := signedIn(t, ts, "kim-recruiter")
	ses, seat := recruiterSession(t, owner, ts, domain.RoleRecruiter)
	seedPerson(t, graph, seat, "A司", "王五", "组长")
	intruder := signedIn(t, ts, "lee-recruiter")

	res, err := intruder.Get(ts.URL + "/app/sessions/" + ses + "/graph/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("another account opened this roster: %d", res.StatusCode)
	}
	if strings.Contains(string(b), "王五") {
		t.Error("the roster was served to another account")
	}
}

// 猎源图谱 is the employer workflow. The tools are recruiter-only; a screen that
// was not would be the way around them.
func TestTheGraphScreenIsForTheEmployerRoleOnly(t *testing.T) {
	ts, graph, _ := graphServer(t)
	c := signedIn(t, ts, "mia-resident")
	ses, seat := recruiterSession(t, c, ts, domain.RoleResident)
	// Seeded into the seat's own book, so the only thing standing between this
	// request and the roster is the role check.
	seedPerson(t, graph, seat, "A司", "王五", "组长")

	res, err := c.Get(ts.URL + "/app/sessions/" + ses + "/graph/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("a resident opened 猎源图谱: %d", res.StatusCode)
	}
	if strings.Contains(string(b), "王五") {
		t.Error("the roster was served to a resident session")
	}
}

// The screen needs a producer, and the interface is it.
//
// THIS IS THE FENCE THE WHOLE FILE EXISTS FOR. leadgraph.Handler was written,
// tested and shipped with no caller; mounting it fixes half of that, and a URL
// nobody links to is the other half. So this reads the href OUT OF THE SHIPPED
// app.js and drives the real mux with it: the link rotting and the route moving
// both turn it red, and neither can be fixed by editing only one side.
// Nothing in the interface points at the screen that was removed.
//
// A link left behind after the route was closed is worse than the screen was:
// the reader presses a control that used to work and gets a 404, which reads as
// "this product is broken" rather than "this moved". Both spellings are
// checked - the header control it lived on, and the footer row every card
// carried - because the second one was on EVERY graph card.
func TestNothingLinksToTheStandaloneGraphScreen(t *testing.T) {
	js := string(mustAsset(t, "app.js"))
	html := string(mustAsset(t, "app.html"))

	// The embed is the one address that may remain, so the check is for a graph
	// URL that is NOT the embed.
	re := regexp.MustCompile(`(?:href|src)="/app/sessions/\$\{[^}]+\}/graph/?(\?[^"]*)?"`)
	for _, m := range re.FindAllStringSubmatch(js, -1) {
		if !strings.Contains(m[0], "embed=1") {
			t.Errorf("app.js still addresses the removed screen: %s", m[0])
		}
	}
	if strings.Contains(js, "graphLinkRow") {
		t.Error("the card footer that linked to the removed screen is still built")
	}
	if strings.Contains(html, `id="graphLink"`) {
		t.Error("app.html still carries the control that opened the removed screen")
	}
	// The strings the two of them printed have to go too, or the next reader
	// finds "打开完整图谱" in the table and puts the link back.
	i18n := string(mustAsset(t, "i18n.js"))
	for _, key := range []string{`"graph.open"`, `"ctl.graph"`} {
		if strings.Contains(i18n, key) {
			t.Errorf("%s is still in the string table for a control that no longer exists", key)
		}
	}
}

// The interface embeds the graph screen in the conversation. That iframe's URL
// has to be one this server serves — and the src is read out of the shipped
// app.js, so the link and the route cannot drift apart.
func TestTheConversationEmbedsAGraphURLThisServerActuallyServes(t *testing.T) {
	src, err := web.Files.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	re := regexp.MustCompile(`src="/app/sessions/\$\{esc\(state\.session\.id\)\}(/graph/\?embed=1)&theme=`)
	m := re.FindSubmatch(src)
	if m == nil {
		t.Fatal("nothing in app.js embeds the graph: the conversation cannot show the picture")
	}

	ts, graph, _ := graphServer(t)
	c := signedIn(t, ts, "oma-recruiter")
	ses, seat := recruiterSession(t, c, ts, domain.RoleRecruiter)
	seedPerson(t, graph, seat, "A司", "王五", "组长")

	res, err := c.Get(ts.URL + "/app/sessions/" + ses + string(m[1]))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the conversation embeds a URL this server does not serve: %d %s", res.StatusCode, b)
	}
	// It must be the embed, not the whole page, or the card holds a page inside
	// a page — and it must still carry the data the picture is drawn from.
	body := string(b)
	if strings.Contains(body, `class="wrap"`) {
		t.Error("the embedded URL served the full page, chrome and all")
	}
	if !strings.Contains(body, "王五") || !strings.Contains(body, `id="graph"`) {
		t.Error("the embedded URL served something that cannot be drawn")
	}
}

func mustAsset(t *testing.T, name string) []byte {
	t.Helper()
	b, err := web.Files.ReadFile("static/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}
