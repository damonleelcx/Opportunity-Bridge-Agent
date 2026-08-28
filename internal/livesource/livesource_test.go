package livesource_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/livesource"
)

func dir(t *testing.T) *livesource.Directory {
	t.Helper()
	d, err := livesource.LoadDirectory("../../data")
	if err != nil {
		t.Fatalf("directory: %v", err)
	}
	return d
}

// The point of the directory: a city the corpus has no listings for still gets
// somewhere real to go, in its own region, with a URL that was verified.
func TestDirectoryAnswersCitiesTheCorpusDoesNotCover(t *testing.T) {
	d := dir(t)
	for _, city := range []string{"深圳", "广州", "武汉", "苏州", "青岛"} {
		res, err := d.Lookup(context.Background(), livesource.Query{City: city})
		if err != nil {
			t.Fatalf("%s: %v", city, err)
		}
		if len(res) != 1 {
			t.Fatalf("%s: %d results, want 1", city, len(res))
		}
		r := res[0]
		if !strings.HasPrefix(r.URL, "http") {
			t.Errorf("%s: no official URL, got %q", city, r.URL)
		}
		if r.Phone != "12333" || r.Kind != livesource.KindDirectory {
			t.Errorf("%s: %+v", city, r)
		}
		if r.Verified == "" {
			t.Errorf("%s: no verified_at — an unverified destination is a guess", city)
		}
	}
}

// A city with no entry of its own resolves to the province that actually runs
// its public employment service, rather than falling through to nothing.
func TestDirectoryFallsBackToTheProvince(t *testing.T) {
	res, err := dir(t).Lookup(context.Background(), livesource.Query{City: "佛山"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || !strings.Contains(res[0].Region, "广东") {
		t.Fatalf("佛山 did not resolve to 广东: %+v", res)
	}
}

// An unknown city is still answerable: 12333 is a nationwide short code that
// reaches the caller's own city. What must not happen is a made-up URL.
func TestUnknownCityGetsTheHotlineAndNoInventedURL(t *testing.T) {
	res, err := dir(t).Lookup(context.Background(), livesource.Query{City: "克拉玛依"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("%d results", len(res))
	}
	if res[0].URL != "" {
		t.Errorf("an unlisted city was given a URL: %q", res[0].URL)
	}
	if res[0].Phone != "12333" || !strings.Contains(res[0].Caveat, "克拉玛依") {
		t.Errorf("%+v", res[0])
	}
}

// Off by default, and its absence is silence rather than a failure — the
// directory still has to answer when no key is configured.
func TestWebSearchIsInertWithoutAKey(t *testing.T) {
	ws := livesource.NewWebSearch("", "", "")
	if ws.Configured() {
		t.Fatal("reported configured with no key")
	}
	res, err := ws.Lookup(context.Background(), livesource.Query{City: "深圳"})
	if err != nil || len(res) != 0 {
		t.Fatalf("res=%v err=%v; an unconfigured provider must be silent, not failing", res, err)
	}
}

func TestWebSearchParsesResultsAndKeepsTheCaveat(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.Query().Get("q"))
		gotKey = r.Header.Get("X-Subscription-Token")
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"web": map[string]any{"results": []map[string]string{
			{"title": "深圳市养老护理员招聘", "url": "https://hrss.sz.gov.cn/x", "description": "深圳养老护理岗位 月薪6000"},
			{"title": "深圳养老护理员培训班招生", "url": "https://hrss.sz.gov.cn/y", "description": "深圳养老护理培训 学费可补贴"},
			{"title": "no url", "url": "", "description": "dropped"},
		}}})
	}))
	defer srv.Close()

	res, err := livesource.NewWebSearch(srv.URL, "k", "").
		Lookup(context.Background(), livesource.Query{City: "深圳", Keyword: "养老护理"})
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "k" {
		t.Errorf("key header not sent: %q", gotKey)
	}
	// Steered at official sources: this product sends people to counters, and a
	// random aggregator's listing is not something to put in front of somebody.
	// One search per intent, each steered at the bodies that publish that kind of
	// thing — vacancies and course catalogues are not the same list.
	joined := strings.Join(queries, " || ")
	for _, want := range []string{"深圳 养老护理 招聘", "公共就业服务", "深圳 养老护理 培训", "职业技能培训"} {
		if !strings.Contains(joined, want) {
			t.Errorf("queries %q are missing %q", joined, want)
		}
	}
	if len(res) != 2 {
		t.Fatalf("%d results — want the job and the course, with the page-less one dropped: %+v",
			len(res), res)
	}
	seen := map[livesource.Intent]bool{}
	for _, r := range res {
		if r.Caveat == "" || r.Kind != livesource.KindListing {
			t.Errorf("a live lead shipped without its caveat: %+v", r)
		}
		seen[r.Intent] = true
	}
	if !seen[livesource.IntentWork] || !seen[livesource.IntentTraining] {
		t.Errorf("results were not labelled work and training: %+v", res)
	}
}

// Brave gets the same three filters Bocha has. Without the intent filter the
// LABEL on a result — and so the fraud warning attached to it — would be
// whichever query happened to return the page rather than what the page is.
func TestWebSearchDropsPagesThatDoNotMatchTheIntent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"web": map[string]any{"results": []map[string]string{
			{"title": "深圳养老护理行业协会年度报告", "url": "https://example.com/report", "description": "深圳养老护理行业发展"},
		}}})
	}))
	defer srv.Close()

	res, err := livesource.NewWebSearch(srv.URL, "k", "").
		Lookup(context.Background(), livesource.Query{City: "深圳", Keyword: "养老护理"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Fatalf("a page that is neither a vacancy nor a course was returned as a lead: %+v", res)
	}
}

// "Nothing found" and "the source was unreachable" must not read the same.
func TestWebSearchFailuresAreErrorsNotEmptyResults(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "SEARCH_AUTH_FAILED"},
		{http.StatusTooManyRequests, "SEARCH_RATE_LIMITED"},
		{http.StatusInternalServerError, "SEARCH_FAILED"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		_, err := livesource.NewWebSearch(srv.URL, "k", "").
			Lookup(context.Background(), livesource.Query{City: "深圳"})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("status %d gave %v, want %s", tc.status, err, tc.want)
		}
	}
}

// One provider failing must not take the others down: the directory works
// offline and has to answer when a search API is having a bad day.
func TestChainKeepsGoingWhenOneProviderFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	chain := livesource.Chain{dir(t), livesource.NewWebSearch(srv.URL, "k", "")}
	res, errs := chain.LookupAll(context.Background(), livesource.Query{City: "深圳"})
	if len(res) == 0 {
		t.Fatal("a failing search provider suppressed the directory")
	}
	if len(errs) != 1 {
		t.Fatalf("the failure was not reported: %v", errs)
	}
	// LookupAll no longer numbers anything: ids have to be unique across the
	// whole turn, and only the caller knows where the turn starts. See
	// TestSequenceNumbersAcrossAWholeTurn.
	if res[0].ID != "" {
		t.Errorf("LookupAll assigned an id (%q); numbering belongs to the turn, and "+
			"numbering per lookup is what made two searches both produce live-003", res[0].ID)
	}
	_ = fmt.Sprint(chain.Name())
}

// Two searches in one turn must not both produce a live-003.
//
// The agent searches more than once as a matter of course — once per trade when
// somebody names two, once per intent when it wants both work and courses.
// Observed in production on 2026-08-28: one answer cited live-003 for a welding
// school and for a cookery school, and the reader had no way to say which one
// they meant. See docs/bugfix/2026-08-28-live-ids-collided-within-a-turn.md
func TestSequenceNumbersAcrossAWholeTurn(t *testing.T) {
	seq := &livesource.Sequence{}
	first := []livesource.Result{{Title: "a"}, {Title: "b"}}
	second := []livesource.Result{{Title: "c"}, {Title: "d"}}
	seq.Assign(first)
	seq.Assign(second)

	got := []string{first[0].ID, first[1].ID, second[0].ID, second[1].ID}
	want := []string{"live-001", "live-002", "live-003", "live-004"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ids across two lookups = %v, want %v", got, want)
	}

	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("id %q was handed out twice; it identifies nothing", id)
		}
		seen[id] = true
	}
}

// The same lead found by two searches keeps one id.
//
// The city's own service directory answers EVERY lookup, so a turn that searches
// twice gets it twice. Numbering strictly by arrival gave one office two ids and
// the answer then cited both, as though they were two places to go.
func TestSequenceGivesOneLeadOneID(t *testing.T) {
	seq := &livesource.Sequence{}
	first := []livesource.Result{
		{Title: "深圳人社", URL: "https://hrss.sz.gov.cn/"},
		{Title: "电焊培训班", URL: "https://example.test/weld"},
	}
	second := []livesource.Result{
		{Title: "深圳人社", URL: "https://hrss.sz.gov.cn/"},
		{Title: "烹饪培训班", URL: "https://example.test/cook"},
	}
	seq.Assign(first)
	seq.Assign(second)

	if first[0].ID != second[0].ID {
		t.Errorf("one office got two ids (%q and %q); the answer will cite both",
			first[0].ID, second[0].ID)
	}
	if second[1].ID == first[1].ID {
		t.Errorf("two different pages share id %q", second[1].ID)
	}
	if got := []string{first[0].ID, first[1].ID, second[1].ID}; strings.Join(got, ",") != "live-001,live-002,live-003" {
		t.Errorf("ids = %v, want live-001,live-002,live-003 with no number spent on the repeat", got)
	}
}

// A lead with no URL is still singular: the hotline-only directory entry for a
// city the corpus has no site for must not consume a new id per lookup.
func TestSequenceMatchesURLlessLeadsByTitle(t *testing.T) {
	seq := &livesource.Sequence{}
	a := []livesource.Result{{Title: "克拉玛依：公共就业服务", Region: "克拉玛依"}}
	b := []livesource.Result{{Title: "克拉玛依：公共就业服务", Region: "克拉玛依"}}
	seq.Assign(a)
	seq.Assign(b)
	if a[0].ID != b[0].ID {
		t.Errorf("the same hotline entry got %q and %q", a[0].ID, b[0].ID)
	}
}

// A caller with no sequence still gets usable ids. The non-Chain path used to
// return results with NO id at all, which the answer cannot cite.
func TestNilSequenceStillNumbersFromOne(t *testing.T) {
	var seq *livesource.Sequence
	res := []livesource.Result{{Title: "a"}, {Title: "b"}}
	seq.Assign(res)
	if res[0].ID != "live-001" || res[1].ID != "live-002" {
		t.Fatalf("nil sequence produced %q, %q", res[0].ID, res[1].ID)
	}
}
