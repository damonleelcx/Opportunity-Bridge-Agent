# The panel said "nothing matched" while the answer listed five openings

**Reported:** 2026-08-28, "博查 Bocha api should be working. it should respond
results. fix it", with a screenshot of two `这次没搜到匹配的记录` cards.
**Area:** `opportunityList()` in `web/static/app.js`.
**Status:** fixed.

## What it looked like

Underneath an answer that named real jobs and gave `hrss.sz.gov.cn` and 12333,
the panel showed two identical cards reading **这次没搜到匹配的记录** — "nothing
matched this time". Nothing else. It read as the search being broken.

## Why it was not the search

The search was working the whole time. Verified while diagnosing:

- Bocha answered `HTTP 200` with 10 results for a 深圳 query.
- The production pod logged `live web search enabled provider=bocha`.
- No `SEARCH_*` error appeared in the pod's logs.

The results were arriving and being used — the agent's prose was written from
them, which is why the answer named employers and dates. Only the **panel** was
wrong.

## Root cause

`opportunityList()` decided whether to say "nothing matched" by looking at one
field:

```js
if (!r.results?.length) { ...card.nothing... }
```

`r.results` is the **corpus** — the vetted, local records. The live lookup's
findings arrive in a different field, `r.live_results`, which the function never
read at all.

For every city the corpus does not cover, `r.results` is empty. The corpus
covers one city (成都). So for effectively every real user, the panel asserted
that nothing was found, regardless of what the live lookup had returned.

Two cards rather than one because the turn called `opportunity_search` twice
(jobs, then training), and each call rendered its own empty card.

This is the same class of defect as
[coverage framed as absence](2026-08-28-session-list.md)'s sibling in the
earlier round: a surface reporting the absence of one source as the absence of
everything. The live lookup was added *after* this renderer was written, and
nothing brought the two together.

**Owner:** the live-lookup change. It added a second source of results and
taught the agent and the tool about it, but not the panel that renders them.

## Also found in the same function

The empty state rendered `r.cities_covered`. The tool has never returned a field
by that name — it returns `cities_with_local_listings` — so it rendered nothing.
It is now removed rather than corrected, because the tool's own note forbids
showing it: *"cities_with_local_listings is for your own reference; do not read
it out to somebody who asked about a different city."* Fixing it to "work" would
have told a person in 深圳 that the service covers 成都.

## What changed

- `opportunityList()` renders corpus results **and** live results, and says
  "nothing matched" only when both are empty.
- A new `liveCard()` renders one live result: title, id, summary, region,
  **the date the site published it**, the source, and a link to the page.
- It is deliberately plainer than `opportunityCard()`. A corpus record has vetted
  criteria and a source reference to expand; a live result has a URL and a date.
  Dressing them identically would claim they carry the same authority. The badge
  states the difference in the reader's own language — `官方入口` /
  `Official service` versus `网上线索 · 未核实` / `Web lead · unverified`.
- The date is shown because a job board posting can be years old. Production has
  served one from **2018**; in the verification run the agent itself wrote
  *"2018-05-23，太旧，基本不用跑"*. Hiding the date would have hidden that.
- Links carry `rel="noopener noreferrer"`: these URLs come from a search index,
  not from us, so the destination gets no handle on the page and no referrer.
- `a.link { text-decoration: none }` in `styles.css`. `.link` was written for
  `<button>`, which has no underline, so its `:hover` rule reads as "underline on
  hover"; an `<a>` starts underlined, so the same class would have been
  underlined always and hover would have done nothing.

## Verified

Locally against the real Bocha API, in the browser, signed in, with a live turn
for 深圳: one `官方入口` card (深圳市, 核实于 2026-08-28, 12333) and five
`网上线索 · 未核实` cards with dates from 2018 to 2026, each with a working link.
`这次没搜到匹配的记录` no longer appears.

## Regression tests

`web/interface_test.go`:

| Test | Fence |
| --- | --- |
| `TestOpportunityPanelConsultsLiveResultsBeforeSayingNothingMatched` | the panel reads `live_results`, and the empty state is guarded on both collections |
| `TestEveryInterfaceStringExistsInEveryLanguage` | a string added to one language table is added to both; a missing key renders as nothing |

Both mutation-drilled: restoring the `!r.results?.length` guard turns the first
red, deleting one `en` key turns the second red.

There is no JavaScript test runner in this repository, so these read the shipped
assets out of the embed and assert on their source. That is weaker than executing
the code — it catches a rule being **removed**, not a rule being **wrong** — and
is used here because the failure was silent and the cost is a wasted journey.

## Known gap, not fixed here

`source` and `caveat` on a live result are composed in Chinese in
`internal/livesource/bocha.go` (`线缆招聘网（网络检索结果，未经核实）`). An English
reader sees them in Chinese. The badge and the date labels are translated, so the
card's meaning survives, but the source line does not. Translating
provider-composed strings is a separate decision about where that boundary sits.
