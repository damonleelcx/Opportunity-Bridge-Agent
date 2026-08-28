package livesource_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/livesource"
)

// bochaServer replays a body as the search API would. The fixture is a real
// response captured from api.bochaai.com on 2026-08-28, not a hand-written
// guess: a parser tested against an invented shape proves only that the
// invention parses.
func bochaServer(t *testing.T, body []byte, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST — Bocha is not a GET API", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want a bearer token", got)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("request body was not JSON: %v", err)
		}
		if req["freshness"] != "noLimit" {
			t.Errorf("freshness = %v, want noLimit — see bochaFreshness for why", req["freshness"])
		}
		w.WriteHeader(status)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return b
}

// The wire contract, against a real captured response.
func TestBochaReadsTheRealResponseShape(t *testing.T) {
	srv := bochaServer(t, fixture(t, "bocha_shenzhen.json"), http.StatusOK)
	b := livesource.NewBocha(srv.URL, "test-key")

	res, err := b.Lookup(context.Background(), livesource.Query{City: "深圳", Keyword: "普工", Limit: 5})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("got %d results, want 3 from the fixture", len(res))
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

// The reader is being asked to judge whether a posting is still live, so the
// date the site published it has to survive the parse — and as a date, not a
// timestamp. The fixture's first result was published 2024-12-15.
func TestBochaCarriesThePublicationDate(t *testing.T) {
	srv := bochaServer(t, fixture(t, "bocha_shenzhen.json"), http.StatusOK)
	res, err := livesource.NewBocha(srv.URL, "test-key").
		Lookup(context.Background(), livesource.Query{City: "深圳"})
	if err != nil {
		t.Fatal(err)
	}
	if got := res[0].Published; got != "2024-12-15" {
		t.Fatalf("published = %q, want 2024-12-15 (date only)", got)
	}
	for _, r := range res {
		if r.Published == "" {
			t.Errorf("%q: no publication date; the reader cannot tell how old it is", r.Title)
		}
	}
}

// The fence that matters most. Measured on 2026-08-28: asking Bocha for recent
// results returns postings from other provinces (深圳 1/8 city-correct under
// freshness=oneMonth). A wrong-city lead costs somebody a journey, so a result
// that does not concern the city asked about must never reach the answer.
func TestBochaDropsResultsFromAnotherCity(t *testing.T) {
	body := `{"code":200,"data":{"webPages":{"value":[
	 {"name":"招聘普工 - 永城信息港","url":"https://example.com/1","snippet":"永城市工厂招工","siteName":"永城信息港","datePublished":"2026-08-12T00:00:00+08:00"},
	 {"name":"深圳宝安招聘普工","url":"https://example.com/2","snippet":"深圳宝安区长白班","siteName":"58同城","datePublished":"2026-08-11T00:00:00+08:00"}
	]}}}`
	srv := bochaServer(t, []byte(body), http.StatusOK)

	res, err := livesource.NewBocha(srv.URL, "test-key").
		Lookup(context.Background(), livesource.Query{City: "深圳"})
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
	body := `{"code":200,"data":{"webPages":{"value":[
	 {"name":"深圳宝安招聘普工","url":"https://example.com/2","snippet":"长白班","siteName":"58同城","datePublished":"2026-08-11T00:00:00+08:00"}
	]}}}`
	srv := bochaServer(t, []byte(body), http.StatusOK)

	res, err := livesource.NewBocha(srv.URL, "test-key").
		Lookup(context.Background(), livesource.Query{City: "深圳市"})
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
		body   string
		want   string
	}{
		{"invalid key", http.StatusUnauthorized, `{"code":"401","message":"Invalid API KEY"}`, "SEARCH_AUTH_FAILED"},
		{"no balance", http.StatusForbidden, `{"code":"403","message":"You do not have enough money or package quota"}`, "SEARCH_QUOTA_EXHAUSTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := livesource.NewBocha(srv.URL, "test-key").
				Lookup(context.Background(), livesource.Query{City: "深圳"})
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
// only the HTTP status would turn those into "there is nothing in your city",
// which is the silent-degradation this package exists to avoid.
func TestBochaFailsOnAnErrorCodeInsideA200(t *testing.T) {
	srv := bochaServer(t, []byte(`{"code":"500","msg":"internal error","data":{}}`), http.StatusOK)
	_, err := livesource.NewBocha(srv.URL, "test-key").
		Lookup(context.Background(), livesource.Query{City: "深圳"})
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
