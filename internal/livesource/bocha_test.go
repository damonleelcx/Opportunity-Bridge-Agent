package livesource_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/livesource"
)

// pinned is the date every test in this file pretends it is.
//
// The age cutoff is relative to now, so without pinning, a fixture that passes
// today starts failing on the day its newest entry turns two — a test that
// rots into a false alarm long after anybody remembers why.
var pinned = time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

// bochaServer replays a body as the search API would, and records which
// freshness windows were asked for.
//
// The fixture is a real response captured from api.bochaai.com on 2026-08-28,
// not a hand-written guess: a parser tested against an invented shape proves
// only that the invention parses.
type bochaServer struct {
	*httptest.Server
	mu      sync.Mutex
	windows []string
	queries []string
}

func (s *bochaServer) asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]string(nil), s.windows...)
	sort.Strings(out)
	return out
}

// searched returns the DISTINCT query strings sent, sorted. One query is sent
// per intent and repeated once per freshness window, so the distinct set is what
// says which questions were actually asked.
func (s *bochaServer) searched() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, q := range s.queries {
		if !seen[q] {
			seen[q] = true
			out = append(out, q)
		}
	}
	sort.Strings(out)
	return out
}

func newBochaServer(t *testing.T, status int, bodyFor func(window string) []byte) *bochaServer {
	t.Helper()
	s := &bochaServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST — Bocha is not a GET API", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want a bearer token", got)
		}
		var req struct {
			Freshness string `json:"freshness"`
			Query     string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("request body was not JSON: %v", err)
		}
		s.mu.Lock()
		s.windows = append(s.windows, req.Freshness)
		s.queries = append(s.queries, req.Query)
		s.mu.Unlock()
		w.WriteHeader(status)
		w.Write(bodyFor(req.Freshness))
	}))
	t.Cleanup(s.Close)
	return s
}

func fixedBody(b []byte) func(string) []byte { return func(string) []byte { return b } }

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return b
}

func bocha(url string) *livesource.Bocha {
	b := livesource.NewBocha(url, "test-key")
	b.Now = func() time.Time { return pinned }
	return b
}

// page builds a response with the dates a test needs.
func page(items ...[2]string) []byte {
	var rows []string
	for i, it := range items {
		rows = append(rows, fmt.Sprintf(
			`{"name":%q,"url":"https://example.com/%d","snippet":"深圳岗位","siteName":"某网","datePublished":%q}`,
			it[0], i, it[1]))
	}
	return []byte(`{"code":200,"data":{"webPages":{"value":[` + strings.Join(rows, ",") + `]}}}`)
}

// The wire contract, against a real captured response.
func TestBochaReadsTheRealResponseShape(t *testing.T) {
	srv := newBochaServer(t, http.StatusOK, fixedBody(fixture(t, "bocha_shenzhen.json")))
	res, err := bocha(srv.URL).
		Lookup(context.Background(), livesource.Query{City: "深圳", Keyword: "普工", Limit: 5})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("got %d results, want the fixture's 3 (deduplicated across windows)", len(res))
	}
	for _, r := range res {
		if r.Kind != livesource.KindListing {
			t.Errorf("%q: kind = %q, want listing", r.Title, r.Kind)
		}
		if !strings.HasPrefix(r.URL, "http") {
			t.Errorf("%q: no URL — an unverifiable listing must not be returned", r.Title)
		}
		if r.Title == "" || r.Source == "" || r.Caveat == "" {
			t.Errorf("%+v: a listing without a title, a source or a caveat is not presentable", r)
		}
		if strings.ContainsAny(r.Title, "\n\r") {
			t.Errorf("%q: title spans lines; scraped text must be collapsed", r.Title)
		}
	}
}

// Every window is asked. Asking only one is the defect this replaced: `noLimit`
// alone served listings one to six years old, because it is its own result set
// and largely misses recent postings — it is not "everything, ranked".
func TestBochaAsksEveryFreshnessWindow(t *testing.T) {
	srv := newBochaServer(t, http.StatusOK, fixedBody(page([2]string{"深圳普工", "2026-08-20T00:00:00+08:00"})))
	if _, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{City: "深圳"}); err != nil {
		t.Fatal(err)
	}
	got := srv.asked()
	// Two intents when nothing narrows the lookup, so every window is asked
	// twice: the query string differs per intent, so one intent's results cannot
	// stand in for the other's.
	want := []string{"noLimit", "noLimit", "oneMonth", "oneMonth", "oneWeek", "oneWeek"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("windows asked = %v, want %v", got, want)
	}
}

// The reported defect: a 2020 posting shown to somebody looking for work today.
// Relevance ordering has no opinion about whether a job still exists, so the
// answer is ordered by date here.
func TestBochaAnswersNewestFirst(t *testing.T) {
	srv := newBochaServer(t, http.StatusOK, fixedBody(page(
		[2]string{"深圳 A 旧", "2025-02-12T00:00:00+08:00"},
		[2]string{"深圳 B 新", "2026-08-26T00:00:00+08:00"},
		[2]string{"深圳 C 中", "2026-01-04T00:00:00+08:00"},
	)))
	res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{City: "深圳"})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(res))
	for i, r := range res {
		got[i] = r.Published
	}
	want := []string{"2026-08-26", "2026-01-04", "2025-02-12"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want newest first %v", got, want)
	}
}

// A posting older than the cutoff is an archive page, not a lead. The screenshot
// that prompted this had one from 2020-07-14 on it.
func TestBochaDropsPostingsTooOldToBeALead(t *testing.T) {
	srv := newBochaServer(t, http.StatusOK, fixedBody(page(
		[2]string{"深圳 六年前", "2020-07-14T00:00:00+08:00"},
		[2]string{"深圳 上个月", "2026-07-20T00:00:00+08:00"},
	)))
	res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{City: "深圳"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1 — the 2020 posting must be dropped: %+v", len(res), res)
	}
	if res[0].Published != "2026-07-20" {
		t.Fatalf("kept the wrong one: %+v", res[0])
	}
}

// A result whose date cannot be read is KEPT: it cannot be shown to be stale,
// and discarding it would throw away real listings to enforce a rule about
// dates. It must sort last so it never displaces something dated.
func TestBochaKeepsUndatedResultsButRanksThemLast(t *testing.T) {
	srv := newBochaServer(t, http.StatusOK, fixedBody(page(
		[2]string{"深圳 无日期", ""},
		[2]string{"深圳 有日期", "2026-08-01T00:00:00+08:00"},
	)))
	res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{City: "深圳"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d, want both kept: %+v", len(res), res)
	}
	if res[0].Published != "2026-08-01" || res[1].Published != "" {
		t.Fatalf("undated result did not sort last: %+v", res)
	}
}

// The windows overlap by construction, so the same posting comes back more than
// once. Showing it twice would read as two separate openings.
func TestBochaShowsOnePostingOnce(t *testing.T) {
	srv := newBochaServer(t, http.StatusOK, fixedBody(page(
		[2]string{"深圳 同一条", "2026-08-10T00:00:00+08:00"},
	)))
	res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{City: "深圳"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("the same URL came back %d times across three windows: %+v", len(res), res)
	}
}

// One window failing is survivable — the others still answer. Every window
// failing is a failed lookup and must NOT read as an empty city.
func TestBochaSurvivesOneFailedWindowButNotAll(t *testing.T) {
	good := page([2]string{"深圳 在招", "2026-08-15T00:00:00+08:00"})
	srv := newBochaServer(t, http.StatusOK, func(window string) []byte {
		if window == "oneWeek" {
			return []byte(`{"code":"500","msg":"boom","data":{}}`)
		}
		return good
	})
	res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{City: "深圳"})
	if err != nil {
		t.Fatalf("one failed window sank the whole lookup: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results from the surviving windows, want 1", len(res))
	}

	allBad := newBochaServer(t, http.StatusOK, func(string) []byte {
		return []byte(`{"code":"500","msg":"boom","data":{}}`)
	})
	if _, err := bocha(allBad.URL).Lookup(context.Background(), livesource.Query{City: "深圳"}); err == nil {
		t.Fatal("every window failed and the lookup reported success; " +
			"'nothing in your city' would be covering for 'I could not look'")
	}
}

// The reader is judging whether a posting is still live, so the date has to
// survive the parse — as a date, not a timestamp.
func TestBochaCarriesThePublicationDate(t *testing.T) {
	srv := newBochaServer(t, http.StatusOK, fixedBody(fixture(t, "bocha_shenzhen.json")))
	res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{City: "深圳"})
	if err != nil {
		t.Fatal(err)
	}
	if got := res[0].Published; got != "2025-11-28" {
		t.Fatalf("first result published = %q, want the fixture's newest, 2025-11-28", got)
	}
	for _, r := range res {
		if r.Published == "" {
			t.Errorf("%q: no publication date; the reader cannot tell how old it is", r.Title)
		}
		if len(r.Published) != 10 {
			t.Errorf("%q: published = %q, want a date only", r.Title, r.Published)
		}
	}
}

// A wrong-city lead costs somebody a journey, so a result that does not concern
// the city asked about must never reach the answer. This matters more now that
// narrow windows are queried: they are the ones that drift to other provinces.
func TestBochaDropsResultsFromAnotherCity(t *testing.T) {
	body := `{"code":200,"data":{"webPages":{"value":[
	 {"name":"招聘普工 - 永城信息港","url":"https://example.com/1","snippet":"永城市工厂招工","siteName":"永城信息港","datePublished":"2026-08-12T00:00:00+08:00"},
	 {"name":"深圳宝安招聘普工","url":"https://example.com/2","snippet":"深圳宝安区长白班","siteName":"58同城","datePublished":"2026-08-11T00:00:00+08:00"}
	]}}}`
	srv := newBochaServer(t, http.StatusOK, fixedBody([]byte(body)))
	res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{City: "深圳"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1 — the 永城 posting must be dropped: %+v", len(res), res)
	}
	if !strings.Contains(res[0].Title, "深圳") {
		t.Fatalf("kept the wrong result: %q", res[0].Title)
	}
}

// 深圳市 asked about must match 深圳 as the sites write it, or the filter above
// would throw away every correct result.
func TestBochaCityMatchIgnoresTheAdministrativeSuffix(t *testing.T) {
	srv := newBochaServer(t, http.StatusOK, fixedBody(page(
		[2]string{"深圳宝安招聘普工", "2026-08-11T00:00:00+08:00"},
	)))
	res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{City: "深圳市"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("深圳市 did not match 深圳 on the page: %+v", res)
	}
}

// A bad key and an empty balance are different problems with different owners —
// one is a deployment mistake, the other is an invoice. Collapsing them sends
// somebody to check the wrong thing. Both were observed live on 2026-08-28.
func TestBochaSeparatesABadKeyFromAnEmptyBalance(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   string
	}{
		{"invalid key", http.StatusUnauthorized, "SEARCH_AUTH_FAILED"},
		{"no balance", http.StatusForbidden, "SEARCH_QUOTA_EXHAUSTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newBochaServer(t, tc.status, fixedBody([]byte(`{"code":"x","message":"y"}`)))
			_, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{City: "深圳"})
			if err == nil {
				t.Fatalf("%d returned no error; a failed lookup must not read as an empty city", tc.status)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
		})
	}
}

// Bocha answers 200 with an error code in the body for some failures. Reading
// only the HTTP status would turn those into "there is nothing in your city".
func TestBochaFailsOnAnErrorCodeInsideA200(t *testing.T) {
	srv := newBochaServer(t, http.StatusOK, fixedBody([]byte(`{"code":"500","msg":"internal error","data":{}}`)))
	_, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{City: "深圳"})
	if err == nil {
		t.Fatal("a 200 carrying an error code was treated as success")
	}
	if !strings.Contains(err.Error(), "SEARCH_FAILED") {
		t.Fatalf("error = %v, want SEARCH_FAILED", err)
	}
}

// Not switched on is not a failure: the directory must still answer.
func TestBochaWithNoKeyIsSilentRatherThanFailing(t *testing.T) {
	res, err := livesource.NewBocha("", "").Lookup(context.Background(), livesource.Query{City: "深圳"})
	if err != nil || len(res) != 0 {
		t.Fatalf("unconfigured provider: res=%v err=%v, want empty and no error", res, err)
	}
}

// A page can be in the right city, recent, and not a job at all. Measured on a
// real turn: query="保洁" produced a cosmetic-surgery advertisement, two
// insurance stories and a procurement notice, all in 深圳, all within two days.
func TestBochaDropsPagesThatAreNotAboutHiring(t *testing.T) {
	// Every decoy here mentions 保洁 and the city. If they did not, the keyword
	// filter would remove them and this test would pass with the hiring filter
	// deleted — which is exactly what the first version of it did. The
	// procurement notice is real: it was served to somebody asking for cleaning
	// work.
	body := `{"code":200,"data":{"webPages":{"value":[
	 {"name":"深圳报业集团公共区域清洁保洁服务采购项目中标公告","url":"https://example.com/tender","snippet":"深圳保洁服务采购中标","siteName":"采购网","datePublished":"2026-08-28T00:00:00+08:00"},
	 {"name":"深圳保洁行业协会发布年度报告","url":"https://example.com/report","snippet":"深圳保洁行业发展","siteName":"中华网","datePublished":"2026-08-28T00:00:00+08:00"},
	 {"name":"深圳龙岗招聘保洁工月结3000-5000元","url":"https://example.com/job","snippet":"深圳龙岗保洁招聘","siteName":"鱼泡","datePublished":"2026-08-20T00:00:00+08:00"}
	]}}}`
	srv := newBochaServer(t, http.StatusOK, fixedBody([]byte(body)))
	res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{City: "深圳", Keyword: "保洁"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results, want only the job: %+v", len(res), res)
	}
	if !strings.Contains(res[0].Title, "招聘") {
		t.Fatalf("kept a page that is not about hiring: %q", res[0].Title)
	}
}

// Hiring, current, right city — and about entirely different work. Recency
// cannot make an irrelevant lead worth a journey.
func TestBochaDropsHiringForWorkThatWasNotAskedAbout(t *testing.T) {
	body := `{"code":200,"data":{"webPages":{"value":[
	 {"name":"深圳9所学校招聘图书管理员、生活老师、文员","url":"https://example.com/school","snippet":"深圳学校招聘","siteName":"腾讯新闻","datePublished":"2026-08-26T00:00:00+08:00"},
	 {"name":"深圳保洁员招聘 月薪4000","url":"https://example.com/clean","snippet":"深圳保洁招聘","siteName":"鱼泡","datePublished":"2026-08-01T00:00:00+08:00"}
	]}}}`
	srv := newBochaServer(t, http.StatusOK, fixedBody([]byte(body)))
	res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{City: "深圳", Keyword: "保洁"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || !strings.Contains(res[0].Title, "保洁") {
		t.Fatalf("the newer but unrelated posting was not dropped: %+v", res)
	}
}

// The agent sends the trade, not the intent, so the word that says what KIND of
// thing is wanted has to be added — and it has to be the RIGHT word. Before
// intents existed it was always 招聘, so somebody asking where to learn a trade
// had their search turned into a search for jobs.
//
// It must not be added twice when the caller already said it, in either
// vocabulary.
func TestBochaAsksForTheRightThingPerIntent(t *testing.T) {
	work := []livesource.Intent{livesource.IntentWork}
	training := []livesource.Intent{livesource.IntentTraining}
	for _, tc := range []struct {
		name    string
		keyword string
		intents []livesource.Intent
		want    []string
	}{
		{"nothing narrows it: ask both", "保洁", nil, []string{"深圳 保洁 培训", "深圳 保洁 招聘"}},
		{"work only", "保洁", work, []string{"深圳 保洁 招聘"}},
		{"training only", "养老护理", training, []string{"深圳 养老护理 培训"}},
		{"no trade named", "", training, []string{"深圳 培训"}},
		{"caller already said 招聘", "保洁 招聘", work, []string{"深圳 保洁 招聘"}},
		{"caller already said 培训", "养老护理 培训", training, []string{"深圳 养老护理 培训"}},
		{"caller said 招生, which is the same ask", "焊工 招生", training, []string{"深圳 焊工 招生"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newBochaServer(t, http.StatusOK, fixedBody(page()))
			_, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{
				City: "深圳", Keyword: tc.keyword, Intents: tc.intents,
			})
			if err != nil {
				t.Fatal(err)
			}
			got := srv.searched()
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("searched for %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Fences for docs/bugfix/2026-08-28-live-search-never-looked-for-training.md
// ---------------------------------------------------------------------------

// mixedPage is one job advert and one course advert, both in the right city and
// both about the same trade. Which of the two comes back is the whole question
// this change answers, so every fence below reads from the same body.
const mixedPage = `{"code":200,"data":{"webPages":{"value":[
 {"name":"深圳养老护理员招聘 月薪6000","url":"https://example.com/job","snippet":"深圳养老护理岗位","siteName":"鱼泡","datePublished":"2026-08-20T00:00:00+08:00"},
 {"name":"深圳养老护理员证培训班招生","url":"https://example.com/course","snippet":"深圳养老护理培训 学费可申请补贴","siteName":"某技工学校","datePublished":"2026-08-18T00:00:00+08:00"}
]}}}`

// The defect itself: somebody asking where to LEARN a trade got recruitment
// adverts, because the only question the web was ever asked was 招聘.
func TestBochaReturnsCoursesForATrainingLookup(t *testing.T) {
	srv := newBochaServer(t, http.StatusOK, fixedBody([]byte(mixedPage)))
	res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{
		City: "深圳", Keyword: "养老护理",
		Intents: []livesource.Intent{livesource.IntentTraining},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results, want only the course: %+v", len(res), res)
	}
	if !strings.Contains(res[0].Title, "培训") {
		t.Fatalf("a training lookup returned something that is not a course: %q", res[0].Title)
	}
	if res[0].Intent != livesource.IntentTraining {
		t.Fatalf("intent = %q, want training; the caveat and the answer both read this",
			res[0].Intent)
	}
}

// The mirror image, and the reason this is an intent rather than a wider word
// list: widening recruitmentWords to accept 培训 would have fixed the case above
// by handing every job seeker course adverts instead.
func TestBochaDoesNotOfferCoursesToSomebodyLookingForWork(t *testing.T) {
	srv := newBochaServer(t, http.StatusOK, fixedBody([]byte(mixedPage)))
	res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{
		City: "深圳", Keyword: "养老护理",
		Intents: []livesource.Intent{livesource.IntentWork},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results, want only the job: %+v", len(res), res)
	}
	if !strings.Contains(res[0].Title, "招聘") || res[0].Intent != livesource.IntentWork {
		t.Fatalf("a work lookup returned %q as %q", res[0].Title, res[0].Intent)
	}
}

// Asked for both, both come back, each labelled as what it is — a person who
// has not said whether they want a job or a course is entitled to see both.
func TestBochaReturnsBothWhenNothingNarrowsTheLookup(t *testing.T) {
	srv := newBochaServer(t, http.StatusOK, fixedBody([]byte(mixedPage)))
	res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{
		City: "深圳", Keyword: "养老护理",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d results, want the job and the course: %+v", len(res), res)
	}
	seen := map[livesource.Intent]string{}
	for _, r := range res {
		seen[r.Intent] = r.Title
	}
	if len(seen) != 2 {
		t.Fatalf("both results were labelled the same way: %+v", seen)
	}
}

// The frauds are different, so the warning has to be. A course sold on credit
// with a promised certificate — 培训贷 — is the danger on one side; a "job" that
// asks for a deposit is the danger on the other. Warning about the wrong one is
// worse than not warning.
func TestBochaWarnsAboutTheFraudThatMatchesTheIntent(t *testing.T) {
	srv := newBochaServer(t, http.StatusOK, fixedBody([]byte(mixedPage)))
	res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{
		City: "深圳", Keyword: "养老护理",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[livesource.Intent]string{
		livesource.IntentWork:     "押金",
		livesource.IntentTraining: "培训贷",
	}
	for _, r := range res {
		if r.Caveat == "" {
			t.Fatalf("%q shipped without a caveat", r.Title)
		}
		if !strings.Contains(r.Caveat, want[r.Intent]) {
			t.Errorf("%s result carries the wrong warning: %q", r.Intent, r.Caveat)
		}
	}
	if res[0].Caveat == res[1].Caveat {
		t.Fatal("both results carry the same warning; the intent is not reaching the reader")
	}
}

// The trade filter must keep working once 培训 is a word the caller says.
//
// If the intent vocabulary stayed in the needle list, ANY course page would
// satisfy "is this about what they asked for" simply by being a course page,
// and somebody who wants to learn 养老护理 would be sent to a 保安 course.
func TestBochaKeepsTheTradeFilterWhenTheQuerySaysTraining(t *testing.T) {
	body := `{"code":200,"data":{"webPages":{"value":[
	 {"name":"深圳保安员培训班招生","url":"https://example.com/guard","snippet":"深圳保安培训报名","siteName":"某校","datePublished":"2026-08-22T00:00:00+08:00"},
	 {"name":"深圳养老护理员培训班招生","url":"https://example.com/care","snippet":"深圳养老护理培训报名","siteName":"某校","datePublished":"2026-08-21T00:00:00+08:00"}
	]}}}`
	srv := newBochaServer(t, http.StatusOK, fixedBody([]byte(body)))
	res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{
		City: "深圳", Keyword: "养老护理 培训",
		Intents: []livesource.Intent{livesource.IntentTraining},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || !strings.Contains(res[0].Title, "养老护理") {
		t.Fatalf("the unrelated course was not dropped: %+v", res)
	}
}

// A real turn sent query="数控 培训 转岗 流水线", where only 数控 names the work.
// Requiring every word meant requiring a course page to also say 转岗 and
// 流水线, which no course page does — so the lookup returned nothing, and
// nothing reads to the person as "there is nothing in your city".
func TestBochaMultiWordQueriesDoNotReturnNothing(t *testing.T) {
	body := `{"code":200,"data":{"webPages":{"value":[
	 {"name":"成都数控编程培训班招生简章","url":"https://example.com/cnc","snippet":"成都数控培训 六周开班 学费可补贴","siteName":"某技工学校","datePublished":"2026-08-19T00:00:00+08:00"}
	]}}}`
	srv := newBochaServer(t, http.StatusOK, fixedBody([]byte(body)))
	res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{
		City: "成都", Keyword: "数控 培训 转岗 流水线",
		Intents: []livesource.Intent{livesource.IntentTraining},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("the course was dropped for not repeating every word of the query: %+v", res)
	}
}

// A page that both intents accept must be labelled the same way on every run.
// Draining a channel would make the label depend on which goroutine finished
// first, so two identical turns could carry two different fraud warnings.
func TestBochaLabelsTheSamePageTheSameWayEveryTime(t *testing.T) {
	body := `{"code":200,"data":{"webPages":{"value":[
	 {"name":"深圳技工学校招生 毕业推荐岗位 月薪6000","url":"https://example.com/both","snippet":"深圳招生 培训 岗位","siteName":"某校","datePublished":"2026-08-17T00:00:00+08:00"}
	]}}}`
	srv := newBochaServer(t, http.StatusOK, fixedBody([]byte(body)))
	first := ""
	for i := 0; i < 25; i++ {
		res, err := bocha(srv.URL).Lookup(context.Background(), livesource.Query{City: "深圳"})
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 1 {
			t.Fatalf("run %d returned %d results", i, len(res))
		}
		if first == "" {
			first = string(res[0].Intent)
		}
		if got := string(res[0].Intent); got != first {
			t.Fatalf("run %d labelled the same page %q, earlier runs said %q", i, got, first)
		}
	}
}

// The mapping from what the corpus was asked for onto what the web can be asked
// for. Subsidy and entrepreneurship have no row on purpose: they are
// administered by an authority rather than advertised as pages somebody can turn
// up to, and the honest live answer for them is the official directory entry.
// Asking only for those must WIDEN to everything searchable, never silence the
// lookup — silence reaches the person as "there is nothing in your city".
func TestIntentsForMapsCorpusKindsOntoSearchableIntents(t *testing.T) {
	both := []livesource.Intent{livesource.IntentWork, livesource.IntentTraining}
	for _, tc := range []struct {
		kinds []string
		want  []livesource.Intent
	}{
		{[]string{"training"}, []livesource.Intent{livesource.IntentTraining}},
		{[]string{"job"}, []livesource.Intent{livesource.IntentWork}},
		{[]string{"job", "training"}, both},
		{nil, both},
		{[]string{"subsidy"}, both},
		{[]string{"entrepreneurship"}, both},
		{[]string{"subsidy", "training"}, []livesource.Intent{livesource.IntentTraining}},
		{[]string{"nonsense"}, both},
	} {
		got := livesource.IntentsFor(tc.kinds)
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("IntentsFor(%v) = %v, want %v", tc.kinds, got, tc.want)
		}
	}
}
