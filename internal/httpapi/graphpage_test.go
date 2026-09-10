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
//  1. a signed-in recruiter reaches their own roster, in the markup, before
//     one line of script runs;
//  2. nobody else reaches it - not another account, not another role, and not
//     an anonymous request;
//  3. the page and the conversation read the SAME book.

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

// The screen is reachable, and it carries the roster in the markup.
func TestTheGraphScreenIsServedToTheRecruiterWhoOwnsIt(t *testing.T) {
	ts, graph, _ := graphServer(t)
	c := signedIn(t, ts, "fay-recruiter")
	ses, seat := recruiterSession(t, c, ts, domain.RoleRecruiter)
	seedPerson(t, graph, seat, "A司", "王五", "组长")

	res, err := c.Get(ts.URL + "/app/sessions/" + ses + "/graph/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("the graph screen is not reachable: %d %s", res.StatusCode, b)
	}
	b, _ := io.ReadAll(res.Body)
	body := string(b)
	script := strings.Index(body, "<script")
	if script < 0 {
		t.Fatal("no script tag at all - this test would prove nothing")
	}
	// Before any script: the same guarantee webui_test makes of the handler,
	// asserted here of the URL a person actually opens.
	for _, want := range []string{"王五", "组长", "c业务组"} {
		if !strings.Contains(body[:script], want) {
			t.Errorf("%q reaches the reader only through script, or not at all", want)
		}
	}
}

// The URL without the trailing slash is one a person types. It must land on the
// page rather than 404, and it must land on the slashed form, because the
// page's own links (graph.css, data) are resolved against it.
func TestTheGraphScreenIsReachableWithoutTheTrailingSlash(t *testing.T) {
	ts, graph, _ := graphServer(t)
	c := signedIn(t, ts, "gus-recruiter")
	ses, seat := recruiterSession(t, c, ts, domain.RoleRecruiter)
	seedPerson(t, graph, seat, "A司", "王五", "组长")

	res, err := c.Get(ts.URL + "/app/sessions/" + ses + "/graph")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if got := res.Request.URL.Path; !strings.HasSuffix(got, "/graph/") {
		t.Errorf("landed on %q, so the page's relative links resolve one level up", got)
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
	page, err := c.Get(base + "/")
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

	res, err := c.Get(ts.URL + "/app/sessions/" + ses + "/graph/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(b), "赵六") {
		t.Errorf("the screen reads a different book from the conversation: %d", res.StatusCode)
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
func TestTheAppShellLinksToAGraphURLThisServerActuallyServes(t *testing.T) {
	src, err := web.Files.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	// The one template literal in the interface that addresses this screen.
	re := regexp.MustCompile(`/app/sessions/\$\{state\.session\.id\}(/graph/?)`)
	m := re.FindSubmatch(src)
	if m == nil {
		t.Fatal("nothing in app.js links to 猎源图谱: the screen is mounted and unreachable")
	}
	// And the control has to be in the markup for the href to live on.
	if !strings.Contains(string(mustAsset(t, "app.html")), `id="graphLink"`) {
		t.Fatal("app.html has no 猎源图谱 control")
	}

	ts, graph, _ := graphServer(t)
	c := signedIn(t, ts, "nia-recruiter")
	ses, seat := recruiterSession(t, c, ts, domain.RoleRecruiter)
	seedPerson(t, graph, seat, "A司", "王五", "组长")

	res, err := c.Get(ts.URL + "/app/sessions/" + ses + string(m[1]))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the interface links at a URL this server does not serve: %d %s", res.StatusCode, b)
	}
	if !strings.Contains(string(b), "王五") {
		t.Error("the linked URL served something that is not the roster")
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
