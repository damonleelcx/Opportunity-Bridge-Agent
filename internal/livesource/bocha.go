package livesource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
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
	// Now exists so the age cutoff can be tested against fixed dates rather than
	// against whenever the suite happens to run. Nil means time.Now.
	Now func() time.Time
	// Log records a window that failed while others succeeded. Without it a
	// partial lookup is indistinguishable from a thin one.
	Log *slog.Logger

	// MaxInFlight caps how many requests this provider has on the wire at once.
	// Zero means defaultMaxInFlight. See acquire for why the cap exists.
	MaxInFlight int

	// The gate is built on first use rather than in NewBocha so that a Bocha
	// assembled as a struct literal is not silently ungated.
	gateOnce sync.Once
	gate     chan struct{}
}

const DefaultBochaEndpoint = "https://api.bochaai.com/v1/web-search"

// bochaWindows are the freshness windows this provider asks for, and merges.
//
// ── Why more than one, which is the whole point of this file ─────────────────
//
// `noLimit` is NOT "everything, ranked by relevance". It is its own result set,
// and it largely MISSES recent postings. Measured 2026-08-28, newest result that
// actually concerned the city asked about:
//
//	query              oneWeek       noLimit
//	深圳 保洁 招聘      2026-08-26    2026-01-04
//	深圳 普工 白班      2026-08-27    2026-03-19
//	成都 普工 招聘      2026-08-28    2025-09-04
//
// So no single window can be right: asking only `noLimit` serves listings one to
// six years old — which is what somebody reported, with a 2020 posting on screen
// — and asking only a narrow window loses the cities where nothing was posted
// this week. They are queried together and merged.
//
// ── Correcting the earlier reasoning, because it is why this was wrong ───────
//
// This constant was previously a single `noLimit`, justified by a measurement
// that counted city-correct results among the RAW response. That measurement was
// taken before the city filter existed in the pipeline, and the filter is
// precisely what neutralises a narrow window's weakness: `oneMonth` looked bad
// at 10/24 raw, but the wrong-province results it returned are exactly the ones
// mentionsCity discards. Measuring the input to a filter and concluding
// something about its output is the mistake; the numbers above are counted
// AFTER filtering.
//
// `oneMonth` is kept despite overlapping `oneWeek` because the windows are not
// nested in practice — 深圳 保洁 returned 3 city-correct results for oneWeek and
// 0 for oneMonth, so a "wider" window is not a superset of a narrower one.
var bochaWindows = []string{"oneWeek", "oneMonth", "noLimit"}

// maxListingAge drops postings too old to be worth a journey.
//
// Every result Bocha returned in testing carried a date (70 of 70), so this
// discards on evidence rather than on a guess. Two years is the line because a
// posting older than that is an archive page rather than a lead: the reported
// screenshot had a 2020-07-14 listing on it, offered to somebody looking for
// work today. Results are additionally ordered newest-first, so age is visible
// even within what survives.
const maxListingAge = 730 * 24 * time.Hour

// defaultMaxInFlight is one, and one is measured rather than chosen.
//
// Swept against the live API on 2026-08-31, shaped like a real turn — three
// lookups back to back, eighteen requests in total — and run in both orders so
// that one width's leftovers could not be mistaken for the next width's result:
//
//	width  results  429s  windows lost  elapsed
//	  1      15       0        0          4.8s
//	  2      12       8        8          1.7s
//	  3      10      10       10          1.2s
//	  1      15       0        0          5.5s   (repeat, straight after width 3)
//
// Two at a time already fails badly, and serialising is not slow: eighteen
// requests take about five seconds, inside a turn that is allowed 180.
//
// ── The measurement that nearly shipped the wrong number ─────────────────────
//
// An earlier probe concluded the ceiling was three, because three concurrent
// requests succeeded repeatedly. Those probes were single lookups spaced six
// seconds apart: that measures a BURST against an idle vendor, not the sustained
// traffic a real turn produces. Set to three, a live turn still lost four
// windows to 429. Whatever the vendor is counting, it refills slower than we
// empty it, so the only width that holds is the one that never has two requests
// outstanding.
//
// This is a property of the account, not of the code. A deployment on a larger
// plan raises Bocha.MaxInFlight; it is not an env var because there is one
// deployment and a knob nobody sets is a knob that goes stale.
const defaultMaxInFlight = 1

// acquire blocks until this provider may issue another request, or the context
// ends.
//
// Why a gate exists at all. One Lookup fans out six requests - two intents by
// three freshness windows - and a single turn issues several lookups, so an
// ordinary question put roughly eighteen requests on the wire simultaneously.
// The vendor refused most of them, and the refusals are not evenly damaging:
// what dies is whichever window loses the race, which in the measured 深圳 turn
// was `oneWeek`, three times out of three. Losing the freshest window drops the
// answer back to `noLimit`, and that is how somebody was handed openings
// eighteen months old while every log line said the lookup had succeeded - the
// failure docs/bugfix/2026-08-28-live-listings-were-years-old.md exists to
// prevent, arriving through a different door.
//
// What the cap trades. It converts "some requests fail, and the freshest one
// fails first" into "some requests wait". Waiting is bounded by the caller's
// context: the turn's own wall-clock budget already stops a lookup that takes
// too long AND tells the person it stopped, which a silently stale listing does
// not. At the measured width the whole fan-out costs about five seconds, well
// inside the 180s a turn is allowed.
//
// The cap is per provider instance, not per Lookup, because the ceiling belongs
// to the vendor account - two people asking at the same time share it.
// See docs/bugfix/2026-08-31-honest-limits-were-not-honest.md
func (b *Bocha) acquire(ctx context.Context) error {
	b.gateOnce.Do(func() {
		n := b.MaxInFlight
		if n <= 0 {
			n = defaultMaxInFlight
		}
		b.gate = make(chan struct{}, n)
	})
	select {
	case b.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		// Distinct from SEARCH_UNREACHABLE and SEARCH_RATE_LIMITED on purpose:
		// this request was never sent, and an operator reading the logs should
		// not go looking at the vendor for it.
		return fmt.Errorf("SEARCH_NOT_ATTEMPTED: waited for a search slot and the turn ended first: %w", ctx.Err())
	}
}

func (b *Bocha) release() { <-b.gate }

func NewBocha(endpoint, key string) *Bocha {
	if endpoint == "" {
		endpoint = DefaultBochaEndpoint
	}
	return &Bocha{
		Endpoint: endpoint, APIKey: key,
		Client: &http.Client{Timeout: 12 * time.Second},
	}
}

func (b *Bocha) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Bocha) log() *slog.Logger {
	if b.Log != nil {
		return b.Log
	}
	return slog.Default()
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

// Lookup asks every freshness window for every intent, merges what comes back,
// and answers newest-first.
//
// The requests are made CONCURRENTLY. Run in sequence they would add their
// latencies together inside a turn that also has to call a model twice, and the
// slowest of six is a much better bill than the sum of six.
//
// Why the fan-out is a cross product and not a loop over windows alone: the
// query string itself differs per intent — "成都 数控 招聘" and "成都 数控 培训"
// are different searches returning different pages — so an intent cannot be
// served by filtering another intent's results. Each request is filtered against
// the intent that asked for it, which is what keeps a job seeker from being
// handed course adverts when both were asked for.
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
	intents := q.intents()
	// The caller's own words, kept separately from the query we send, because
	// they are also what a result has to be ABOUT to be returned. The intent
	// vocabulary is taken out — see tradeNeedles for why leaving it in would
	// make the trade filter vacuous.
	terms := strings.TrimSpace(q.Keyword)
	needles := tradeNeedles(terms, intents)
	// Over-fetch per request, because the city filter and the age cutoff both
	// discard. Asking for exactly `limit` and then filtering would quietly return
	// fewer results than the caller asked for.
	fetch := limit * 4
	if fetch > 20 {
		fetch = 20
	}

	type ask struct {
		intent Intent
		window string
		query  string
	}
	asks := make([]ask, 0, len(intents)*len(bochaWindows))
	for _, in := range intents {
		query := intentQuery(city, terms, in)
		for _, w := range bochaWindows {
			asks = append(asks, ask{intent: in, window: w, query: query})
		}
	}

	// Answers land in a slice indexed by request rather than in a channel, so
	// they are read back in a FIXED order. Dedup keeps whichever copy it sees
	// first, so draining a channel would make "which intent is this result
	// labelled" depend on which goroutine happened to finish first — an answer
	// that changes between identical turns.
	type outcome struct {
		hits []bochaHit
		err  error
	}
	outs := make([]outcome, len(asks))
	var wg sync.WaitGroup
	for i, a := range asks {
		wg.Add(1)
		go func(i int, a ask) {
			defer wg.Done()
			hits, err := b.fetchWindow(ctx, a.query, a.window, fetch)
			outs[i] = outcome{hits: hits, err: err}
		}(i, a)
	}
	wg.Wait()

	var firstErr error
	failed := 0
	seen := map[string]bool{}
	merged := make([]Result, 0, fetch)
	cutoff := b.now().Add(-maxListingAge)

	for i, a := range asks {
		o := outs[i]
		if o.err != nil {
			failed++
			if firstErr == nil {
				firstErr = o.err
			}
			// One request failing is survivable; saying nothing about it is not.
			b.log().Warn("one search request failed; the answer is built from the rest",
				"code", "SEARCH_WINDOW_FAILED", "window", a.window,
				"intent", string(a.intent), "error", o.err)
			continue
		}
		for _, r := range o.hits {
			if r.URL == "" || r.Name == "" {
				continue // a result without a page is not a result
			}
			if seen[r.URL] {
				continue // the windows overlap; the same posting must appear once
			}
			text := r.Name + " " + r.Snippet + " " + r.Summary + " " + r.URL + " " + r.SiteName
			if !mentionsCity(city, text) || !matchesIntent(text, a.intent) || !mentionsAny(text, needles) {
				continue
			}
			published, ok := publishedTime(r.DatePublished)
			// A posting with no readable date is kept: it cannot be shown to be
			// stale, and dropping it would discard real listings to enforce a rule
			// about dates. It sorts last, so it never displaces something dated.
			if ok && published.Before(cutoff) {
				continue
			}
			seen[r.URL] = true
			merged = append(merged, Result{
				Kind: KindListing, Region: city, Intent: a.intent,
				Title:     collapse(r.Name),
				Summary:   collapse(firstNonEmpty(r.Summary, r.Snippet)),
				URL:       r.URL,
				Source:    sourceLabel(r.SiteName),
				Published: publishedOn(r.DatePublished),
				Caveat:    intentProfiles[a.intent].Caveat,
			})
		}
	}

	// Every request failed, so this is a failed lookup and must not be reported
	// as an empty city. One or two failing still yields an answer.
	if failed == len(asks) {
		return nil, firstErr
	}

	// Newest first. This is the point of the 2026-08-28 change: the reader was
	// being shown a 2020 posting above a current one because the API's own
	// ordering is by relevance, and relevance has no opinion about whether a job
	// still exists.
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].Published > merged[j].Published // ISO dates sort as strings; "" sorts last
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged, nil
}

// bochaHit is one raw result, before it is judged.
type bochaHit struct {
	Name          string
	URL           string
	Snippet       string
	Summary       string
	SiteName      string
	DatePublished string
}

// fetchWindow performs one search. Everything it knows about failure is here, so
// the caller can treat a window uniformly whether it answered or not.
func (b *Bocha) fetchWindow(ctx context.Context, query, window string, count int) ([]bochaHit, error) {
	if err := b.acquire(ctx); err != nil {
		return nil, err
	}
	defer b.release()

	body, err := json.Marshal(bochaRequest{
		Query: query, Freshness: window, Summary: true, Count: count,
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

	hits := make([]bochaHit, 0, len(parsed.Data.WebPages.Value))
	for _, r := range parsed.Data.WebPages.Value {
		hits = append(hits, bochaHit{
			Name: r.Name, URL: r.URL, Snippet: r.Snippet, Summary: r.Summary,
			SiteName: r.SiteName, DatePublished: r.DatePublished,
		})
	}
	return hits, nil
}

// publishedTime parses what the site said, reporting whether it could.
// "Could not read the date" and "published at the zero time" must not be the
// same answer, because one of them would silently discard the result.
func publishedTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if len(s) >= 10 {
		if t, err := time.Parse("2006-01-02", s[:10]); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// mentionsAny requires the result to be about what was actually asked for.
//
// Without it, "深圳 保洁 招聘" returns current recruitment drives for school
// librarians and administrators: in the right city, genuinely about hiring, and
// nothing to do with the work the person came here for. Recency cannot make an
// irrelevant lead worth a journey.
//
// ── Why ANY and not ALL, which is what this used to require ─────────────────
//
// The agent does not send a trade, it sends a bag of words: a real turn sent
// query="数控 培训 转岗 流水线", where only 数控 names the work and the rest is
// context about the person's past. Requiring all four meant requiring a course
// page to also say 转岗 and 流水线, which no course page does — so the lookup
// returned nothing, and nothing reads to the person as "there is nothing in your
// city". Zero results is not precision; it is this module's own failure mode.
//
// The case the ALL rule was actually written for is unaffected: with one needle,
// any and all are the same rule, and the librarian postings it rejects are
// rejected identically. What changes is only the multi-word query, where the
// choice was never between precise and loose but between loose and empty.
//
// Intent vocabulary has already been removed by tradeNeedles, so 培训 cannot be
// the word that satisfies this for a course page. An empty needle list imposes
// nothing: a caller who named no trade still gets the city's pages.
func mentionsAny(text string, needles []string) bool {
	if len(needles) == 0 {
		return true
	}
	for _, n := range needles {
		if strings.Contains(text, n) {
			return true
		}
	}
	return false
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
func mentionsCity(city, text string) bool {
	needle := strings.TrimSuffix(strings.TrimSuffix(city, "市"), "区")
	if needle == "" {
		return false
	}
	return strings.Contains(text, needle)
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
