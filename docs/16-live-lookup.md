# 16. Looking things up outside the corpus

The corpus can only hold named employers and courses for cities somebody has
loaded data for. Everyone else was getting the national framework and nothing
concrete — correct, but half an answer.

`internal/livesource` is the seam that fills the other half, without weakening
the rule the product rests on: **everything named in an answer must have come
back from a tool this turn, with a source you can open.**

## Three providers

| Provider | Ships | Needs | Returns |
|---|---|---|---|
| `directory` | **enabled** | nothing | The official public-employment-service destination for the person's region, plus 12333 / 12345 |
| `bocha` (博查) | **off unless keyed** | `OBA_SEARCH_API_KEY` | Current openings **and courses** found live, each with its URL, its publication date and a caveat |
| `websearch` (Brave) | **off unless keyed** | `OBA_SEARCH_API_KEY` + `OBA_SEARCH_PROVIDER=brave` | The same, from Brave |

`bocha` is the default, because these searches are for Chinese cities on
Chinese-language sites. The two search vendors answer **different wire shapes**,
so the provider is named by `OBA_SEARCH_PROVIDER` and never inferred from the
URL: pointing the URL at one while the provider says the other fails *silently*
— the request is accepted, nothing parses, and the city reads as empty.

Adding a real feed later — a provincial open-data endpoint, a partner job board,
an official API once granted — is one implementation of `Provider`, not a change
to the agent.

## Intent: work or training

A live lookup is asked for **work**, for **training**, or for both. This is not a
filter applied after the fact; it changes the search itself.

| | work | training |
|---|---|---|
| Term added to the query | `招聘` | `培训` |
| A page must use one of | `recruitmentWords` — 招聘、岗位、月薪、日结… | `trainingWords` — 培训、招生、开班、学费、考证… |
| Warning carried to the reader | deposits, up-front fees | 培训贷, 包过/包证/包分配, up-front tuition |

**Why the search itself and not a filter.** `成都 数控 招聘` and `成都 数控 培训`
return different pages. A training result cannot be recovered by filtering a
recruitment search, because the recruitment search never asked for it.

**Why two lists and not one wider list.** Accepting both vocabularies everywhere
would fix the training case by handing every job seeker course adverts — which is
the noise the 2026-08-28 freshness work was written to remove.

`opportunity_search` passes this through from its `kinds` argument
(`livesource.IntentsFor`):

| `kinds` asked of the corpus | intents asked of the web |
|---|---|
| `["training"]` | training |
| `["job"]` | work |
| omitted, or `["job","training"]` | both |
| `["subsidy"]`, `["entrepreneurship"]` | both |

Subsidies and entrepreneurship support have **no row of their own** on purpose:
they are administered by an authority rather than advertised as pages somebody
can turn up to, so the honest live answer for them is the official directory
entry, not a commercial page about money you can supposedly claim. Asking only
for those therefore *widens* to everything searchable rather than searching for
nothing — a forgotten or unmapped kind must never turn into silence, because
silence reaches the person as "there is nothing in your city".

Each intent is asked in every freshness window, so a two-intent lookup makes six
requests. They run concurrently, and the answers are read back in a fixed order
so that a page both intents accept is labelled the same way on every run.

### What a result has to be about

Three filters, each guarding a different failure:

1. **wrong city** — costs somebody a journey
2. **wrong kind of page** — a procurement notice offered as a cleaning job, or a
   job advert offered to somebody who asked where to study
3. **wrong trade** — librarian vacancies offered to a cleaner

The third one requires **at least one** of the caller's words, not all of them.
The agent sends a bag of words rather than a trade — a real turn sent
`query="数控 培训 转岗 流水线"` — and requiring all four meant requiring a course
page to also say 转岗 and 流水线, which no course page does. With a single word,
"any" and "all" are the same rule, so the case this filter was written for is
unchanged; what changes is only the multi-word query, where the choice was never
between precise and loose but between loose and **empty**.

Intent vocabulary is removed from that word bag first (`tradeNeedles`). Leaving
`培训` in it would let any course page satisfy "is this about what they asked
for" simply by being a course page.

## The directory, and why it is not a guess

31 regions, each URL **fetched and confirmed to answer** on the date recorded in
`verified_at`. Regions whose host did not respond were left out rather than
guessed — `data/service_directory.json` is short by design.

Resolution goes city → alias → the province that actually runs that city's
public employment service, so 佛山 lands on 广东 rather than on nothing. A city
with no entry anywhere still gets a real answer: **12333 and 12345 are nationwide
short codes that reach the caller's own city.** What it does not get is a URL,
and the result says so rather than inventing one.

## Live web search

This is the provider that actually answers "look them up nationwide". It is off
until a key is set, and that is deliberate rather than an oversight:

- A keyless scrape of a search engine or a government site is fragile and
  unwelcome. I tried both while building this: the official
  `job.mohrss.gov.cn` JSON endpoint is session-gated and returns nothing to a
  straightforward client, and DuckDuckGo's HTML endpoint is blocked.
- A lookup that silently degrades is worse than one that says it is not
  switched on. Startup logs `LIVE_SEARCH_DISABLED` when there is no key.

```bash
OBA_SEARCH_API_KEY=...          # bocha (博查) by default
OBA_SEARCH_PROVIDER=bocha       # or brave — never inferred from the URL
OBA_SEARCH_API_URL=...          # must answer the named provider's shape
OBA_SEARCH_KEY_HEADER=...       # brave only; default X-Subscription-Token
```

Queries are steered at official and public-service sources rather than the open
web — this product sends people to counters, and a random aggregator's listing
is not something to put in front of somebody. Every result carries its fetched
URL, its source, and a caveat that travels all the way to the reader — and the
caveat is the one that matches what the result IS: *这是网上搜到的线索，不是本系统
核实过的岗位* for an opening, and a warning about 培训贷 and 包过包证 for a course.
Warning about the wrong fraud is worse than not warning.

## How this stays honest

**Live ids are citable, and only for the turn that produced them.**
`opportunity_search` reports `live_ids` in its Meta; the invented-identifier
check accepts exactly those, plus the corpus. A `live-001` cannot be quoted in a
later turn that did not look it up.

**A failed lookup is not an empty one.** A provider that errors produces a
`LIVE_LOOKUP_FAILED` finding, and the agent is told to say the check could not
run — so "there is nothing in your city" never covers for "I could not look".

**One provider failing does not silence the others.** The directory works
offline and answers when a search API is down.

## What is still missing

**`trainingWords` has never been measured against the live index.**
`recruitmentWords` carries a count taken from real Bocha responses on
2026-08-28 (on `深圳 保洁`: 4 of 20 results looked like recruitment; with `招聘`
appended, 16 of 17). The training list was written from the vocabulary these
pages use and is fenced only against fixtures, because no search key was
available when it was added. **The first live run must count how many course
pages it accepts and rejects, the same way, and correct the list.**


Named employers and courses outside the corpus **require the search key or a
real feed**. The directory is the honest floor — it puts a resident in 深圳 one
click from 深圳's own public employment service instead of an apology — but it
is a destination, not a listing. Wiring a feed is a data-access decision, and
the seam is now the only thing standing between that decision and working
listings nationwide.
