# 16. Looking things up outside the corpus

The corpus can only hold named employers and courses for cities somebody has
loaded data for. Everyone else was getting the national framework and nothing
concrete — correct, but half an answer.

`internal/livesource` is the seam that fills the other half, without weakening
the rule the product rests on: **everything named in an answer must have come
back from a tool this turn, with a source you can open.**

## Two providers

| Provider | Ships | Needs | Returns |
|---|---|---|---|
| `directory` | **enabled** | nothing | The official public-employment-service destination for the person's region, plus 12333 / 12345 |
| `websearch` | **off unless keyed** | `OBA_SEARCH_API_KEY` | Current openings and courses found live, each with its URL and a caveat |

Adding a real feed later — a provincial open-data endpoint, a partner job board,
an official API once granted — is one implementation of `Provider`, not a change
to the agent.

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
OBA_SEARCH_API_KEY=...          # Brave Search by default
OBA_SEARCH_API_URL=...          # only an API answering Brave's exact shape
OBA_SEARCH_KEY_HEADER=...       # default X-Subscription-Token
```

Queries are steered at official and public-service sources rather than the open
web — this product sends people to counters, and a random aggregator's listing
is not something to put in front of somebody. Every result carries its fetched
URL, its source, and a caveat that travels all the way to the reader: *这是网上
搜到的线索，不是本系统核实过的岗位。*

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

Named employers and courses outside the corpus **require the search key or a
real feed**. The directory is the honest floor — it puts a resident in 深圳 one
click from 深圳's own public employment service instead of an apology — but it
is a destination, not a listing. Wiring a feed is a data-access decision, and
the seam is now the only thing standing between that decision and working
listings nationwide.
