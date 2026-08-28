package livesource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Bocha (博查) returns live openings and courses for a city.
//
// Why this and not Brave: the people this product serves are looking for work in
// Chinese cities, on Chinese-language sites, and that is what decides whether the
// feature works at all. Bocha indexes those sites — it is the closest domestic
// equivalent to the Bing Search API and is DeepSeek's own web-search supplier.
// Brave's free tier was also withdrawn in February 2026, so the keyless-ish
// option it used to represent no longer exists either.
//
// It is a separate Provider rather than a re-pointed WebSearch because the wire
// shape is genuinely different — a POST with a JSON body and a bearer token,
// answering `data.webPages.value[]` — and pointing OBA_SEARCH_API_URL at it
// would fail SILENTLY, which is the exact trap already documented on WebSearch.
//
// What it does NOT do is decide the results are true. Everything it returns
// carries the fetched URL, the date the site published it, and a caveat, and the
// agent presents them as leads to check rather than as verified openings.
type Bocha struct {
	Endpoint string
	APIKey   string
	Client   *http.Client
}

const DefaultBochaEndpoint = "https://api.bochaai.com/v1/web-search"

// bochaFreshness is deliberately "noLimit", and this is the one setting here
// that is not obvious, so it is written down.
//
// Asking Bocha for RECENT results makes the answer WORSE, because it trades away
// the city. Measured 2026-08-28 over three cities, counting how many returned
// results actually concern the city that was asked about:
//
//	freshness=noLimit    深圳 5/5    佛山 8/8    成都 8/8    = 21/21
//	freshness=oneMonth   深圳 1/8    佛山 4/8    成都 5/8    = 10/24
//
// The oneMonth results for 深圳 were postings in 永城, 安徽, 临安 and 即墨 — recent,
// and in the wrong province. For somebody deciding whether to spend a morning
// travelling to an address, a fresh listing in another province is worse than a
// stale one in their own city: the stale one wastes a phone call, the wrong-city
// one wastes the trip. Recency is instead handled honestly — the publication date
// travels with every result and is shown to the reader.
const bochaFreshness = "noLimit"

func NewBocha(endpoint, key string) *Bocha {
	if endpoint == "" {
		endpoint = DefaultBochaEndpoint
	}
	return &Bocha{
		Endpoint: endpoint, APIKey: key,
		Client: &http.Client{Timeout: 12 * time.Second},
	}
}

func (b *Bocha) Name() string { return "bocha" }

// Configured reports whether this provider can run at all.
func (b *Bocha) Configured() bool { return b != nil && b.APIKey != "" }

type bochaRequest struct {
	Query     string `json:"query"`
	Freshness string `json:"freshness"`
	Summary   bool   `json:"summary"`
	Count     int    `json:"count"`
}

type bochaResponse struct {
	Code json.Number `json:"code"`
	Msg  string      `json:"msg"`
	Data struct {
		WebPages struct {
			Value []struct {
				Name          string `json:"name"`
				URL           string `json:"url"`
				Snippet       string `json:"snippet"`
				Summary       string `json:"summary"`
				SiteName      string `json:"siteName"`
				DatePublished string `json:"datePublished"`
			} `json:"value"`
		} `json:"webPages"`
	} `json:"data"`
}

func (b *Bocha) Lookup(ctx context.Context, q Query) ([]Result, error) {
	if !b.Configured() {
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
	terms := strings.TrimSpace(q.Keyword)
	if terms == "" {
		terms = "招聘 培训"
	}
	// Over-fetch, because the city filter below discards anything that drifted to
	// another city. Asking for exactly `limit` and then filtering would quietly
	// return fewer results than the caller asked for.
	fetch := limit * 2
	if fetch > 20 {
		fetch = 20
	}

	body, err := json.Marshal(bochaRequest{
		Query:     fmt.Sprintf("%s %s", city, terms),
		Freshness: bochaFreshness,
		Summary:   true,
		Count:     fetch,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.APIKey)

	res, err := b.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("SEARCH_UNREACHABLE: %w", err)
	}
	defer res.Body.Close()
	// 401 and 403 are different operator problems and must not be collapsed: a
	// bad key is a deployment mistake, an empty balance is an invoice. Bocha
	// distinguishes them (401 "Invalid API KEY" vs 403 "not enough money or
	// package quota"), so the message says which one to go and fix.
	switch res.StatusCode {
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("SEARCH_AUTH_FAILED: the search API rejected the key (401); " +
			"check OBA_SEARCH_API_KEY")
	case http.StatusForbidden:
		return nil, fmt.Errorf("SEARCH_QUOTA_EXHAUSTED: the search API accepted the key but " +
			"refused the query (403); the account is out of balance or package quota")
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("SEARCH_RATE_LIMITED: the search API returned 429")
	case http.StatusOK:
	default:
		return nil, fmt.Errorf("SEARCH_FAILED: the search API returned %d", res.StatusCode)
	}

	var parsed bochaResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("SEARCH_UNPARSEABLE: %w", err)
	}
	// Bocha answers 200 with an error code in the body for some failures. Reading
	// only the HTTP status would turn those into "there is nothing in your city".
	if code := parsed.Code.String(); code != "" && code != "200" {
		return nil, fmt.Errorf("SEARCH_FAILED: the search API returned code %s: %s", code, parsed.Msg)
	}

	out := make([]Result, 0, limit)
	for _, r := range parsed.Data.WebPages.Value {
		if r.URL == "" || r.Name == "" {
			continue // a result without a page is not a result
		}
		if !mentionsCity(city, r.Name, r.Snippet, r.Summary, r.URL, r.SiteName) {
			continue
		}
		out = append(out, Result{
			Kind: KindListing, Region: city,
			Title:     collapse(r.Name),
			Summary:   collapse(firstNonEmpty(r.Summary, r.Snippet)),
			URL:       r.URL,
			Source:    sourceLabel(r.SiteName),
			Published: publishedOn(r.DatePublished),
			Caveat: "这是网上搜到的线索，不是本系统核实过的岗位，发布网站多为商业招聘平台。" +
				"点开先看发布日期和单位名称；凡是先收费、先交押金、先办贷款的，都不要答应。" +
				"拿不准就打 12333 核一下再跑一趟。",
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// mentionsCity keeps a result only if it actually concerns the city that was
// asked about.
//
// This is a guard, not a workhorse: on the settings this provider uses, 21 of 21
// results across three cities already passed it. It exists because the failure it
// prevents is the expensive one — a person travelling to an address in another
// province — and because a search index has no obligation to stay in the city we
// asked about. See bochaFreshness for the measurement.
//
// The trailing 市 / 区 is trimmed so 深圳市 asked about matches 深圳 written on the
// page, which is how these sites label themselves.
func mentionsCity(city string, fields ...string) bool {
	needle := strings.TrimSuffix(strings.TrimSuffix(city, "市"), "区")
	if needle == "" {
		return false
	}
	for _, f := range fields {
		if strings.Contains(f, needle) {
			return true
		}
	}
	return false
}

// publishedOn keeps the date part of an RFC3339 timestamp. The reader is being
// asked to judge whether a posting is still live; the hour it was published does
// not help with that, and a full timestamp reads as more precision than the
// answer deserves.
func publishedOn(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Format("2006-01-02")
	}
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func sourceLabel(site string) string {
	site = collapse(site)
	if site == "" {
		return "网络检索结果（未经核实）"
	}
	return site + "（网络检索结果，未经核实）"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// collapse puts a value on one line. These fields are scraped page text and
// arrive with newlines in them, which would break the single-line rendering the
// answer and the interface both assume.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
