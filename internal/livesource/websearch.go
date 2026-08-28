package livesource

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WebSearch returns live employers and courses for a city by searching the web.
//
// This is the provider that actually answers "look them up nationwide". It is
// off unless a search API key is configured, and that is a deliberate choice
// rather than an oversight: a keyless scrape of a search engine or of a
// government site is fragile and unwelcome, and a lookup that silently degrades
// is worse than one that says it is not switched on.
//
// The wire shape is Brave Search's specifically: a GET with query parameters,
// answering `web.results[]` with `title`, `url`, `description`. Pointing
// OBA_SEARCH_API_URL at a different vendor only works if that vendor answers
// the same shape to the same request. Serper does NOT - it takes a POST with a
// JSON body and answers `organic[]` with `title`, `link`, `snippet` - and the
// failure is silent: the decode succeeds, `web` is absent, and the lookup
// returns no results with no error, so "there is nothing here" ends up covering
// for "I could not read the answer". Anything that is not Brave-shaped needs
// its own Provider, which is a small file, not a change to the agent.
//
// What it does NOT do is decide the results are true. Everything it returns
// carries the fetched URL and a caveat, and the agent is instructed to present
// them as leads to check rather than as verified openings — an unverified
// listing offered as fact is exactly what this product must not produce.
type WebSearch struct {
	Endpoint string
	APIKey   string
	// KeyHeader is the header the API expects. Brave uses X-Subscription-Token.
	KeyHeader string
	Client    *http.Client
}

const (
	DefaultSearchEndpoint = "https://api.search.brave.com/res/v1/web/search"
	DefaultSearchHeader   = "X-Subscription-Token"
)

func NewWebSearch(endpoint, key, header string) *WebSearch {
	if endpoint == "" {
		endpoint = DefaultSearchEndpoint
	}
	if header == "" {
		header = DefaultSearchHeader
	}
	return &WebSearch{
		Endpoint: endpoint, APIKey: key, KeyHeader: header,
		Client: &http.Client{Timeout: 12 * time.Second},
	}
}

func (w *WebSearch) Name() string { return "websearch" }

// Configured reports whether this provider can run at all.
func (w *WebSearch) Configured() bool { return w != nil && w.APIKey != "" }

type braveResponse struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
		} `json:"results"`
	} `json:"web"`
}

// braveSteer points the search at official and public-service sources rather
// than at the open web: this product sends people to counters, and a random
// aggregator's listing is not something it should put in front of somebody.
//
// It is per-intent because the bodies that publish vacancies and the bodies that
// publish course catalogues are not the same list. The work steer is kept
// byte-identical to the one that shipped before intents existed, because it was
// chosen against live results and this change has no live measurement to replace
// that with; the training steer is new and unmeasured. An intent with no row
// falls back to the work steer rather than searching the bare open web.
var braveSteer = map[Intent]string{
	IntentWork:     "公共就业服务 OR 人力资源和社会保障 OR 公共招聘",
	IntentTraining: "职业技能培训 OR 人力资源和社会保障 OR 补贴性培训",
}

func steerFor(in Intent) string {
	if s, ok := braveSteer[in]; ok {
		return s
	}
	return braveSteer[IntentWork]
}

// Lookup searches once per intent and merges what comes back.
//
// One intent failing is survivable — the other still answers — but every intent
// failing is a failed lookup and must not be reported as an empty city.
func (w *WebSearch) Lookup(ctx context.Context, q Query) ([]Result, error) {
	if !w.Configured() {
		return nil, nil // not switched on; not a failure
	}
	city := strings.TrimSpace(q.City)
	if city == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	intents := q.intents()
	terms := strings.TrimSpace(q.Keyword)
	needles := tradeNeedles(terms, intents)

	seen := map[string]bool{}
	out := make([]Result, 0, limit)
	var firstErr error
	failed := 0

	for _, in := range intents {
		query := intentQuery(city, terms, in) + " " + steerFor(in)
		hits, err := w.fetch(ctx, query, limit)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, r := range hits {
			if r.URL == "" || r.Title == "" {
				continue // a result without a page is not a result
			}
			if seen[r.URL] {
				continue
			}
			// The same three filters Bocha applies. Without the intent filter the
			// label on the result — and so the fraud warning attached to it —
			// would be whichever query happened to return the page rather than
			// what the page is.
			text := r.Title + " " + r.Description + " " + r.URL
			if !mentionsCity(city, text) || !matchesIntent(text, in) || !mentionsAny(text, needles) {
				continue
			}
			seen[r.URL] = true
			out = append(out, Result{
				Kind: KindListing, Region: city, Intent: in,
				Title: collapse(r.Title), Summary: collapse(r.Description), URL: r.URL,
				Source: "网络检索结果（未经核实）",
				Caveat: intentProfiles[in].Caveat,
			})
			if len(out) >= limit {
				break
			}
		}
	}
	if failed == len(intents) && failed > 0 {
		return nil, firstErr
	}
	return out, nil
}

// fetch performs one search. Everything it knows about failure is here, so the
// caller can treat an intent uniformly whether it answered or not.
func (w *WebSearch) fetch(ctx context.Context, query string, limit int) ([]braveHit, error) {
	u, err := url.Parse(w.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("SEARCH_ENDPOINT_INVALID: %w", err)
	}
	qs := u.Query()
	qs.Set("q", query)
	qs.Set("count", fmt.Sprint(limit))
	qs.Set("country", "cn")
	qs.Set("search_lang", "zh-hans")
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set(w.KeyHeader, w.APIKey)

	res, err := w.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("SEARCH_UNREACHABLE: %w", err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("SEARCH_AUTH_FAILED: the search API rejected the key (%d); "+
			"check OBA_SEARCH_API_KEY", res.StatusCode)
	case res.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("SEARCH_RATE_LIMITED: the search API returned 429")
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("SEARCH_FAILED: the search API returned %d", res.StatusCode)
	}

	var parsed braveResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("SEARCH_UNPARSEABLE: %w", err)
	}
	hits := make([]braveHit, 0, len(parsed.Web.Results))
	for _, r := range parsed.Web.Results {
		hits = append(hits, braveHit{Title: r.Title, URL: r.URL, Description: r.Description})
	}
	return hits, nil
}

// braveHit is one raw result, before it is judged.
type braveHit struct {
	Title       string
	URL         string
	Description string
}
