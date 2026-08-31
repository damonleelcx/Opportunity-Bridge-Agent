# The "honest limits" section was not honest

**Reported:** 2026-08-31, alongside the routing-JSON leak. The four cards under
"这个版本做不到的事" were quoted back with the instruction to make what they
describe possible.
**Area:** landing-page copy (`web/static/i18n.js`, `index.html`, `home.js`),
`/api/health` and `/api/meta` (`internal/httpapi/server.go`), and both READMEs.
**Status:** cards 1 and 4 fixed and verified against a live provider. Cards 2
and 3 are unchanged and remain true — see "What was not changed and why".

## What it looked like

The section headed 真话 / "Honest limits" carried two statements that were not
true, on the one part of the product whose whole job is to be accurate.

| Claimed | Actual |
|---|---|
| "21 条岗位、12 份办事指南" | 26 opportunity records (9 job, 9 subsidy, 5 training, 3 entrepreneurship) and 12 guides |
| "编号以 `SAMPLE/` 开头，**这个前缀会一直显示到屏幕上**" | Half true. Every record's `source_ref` does begin with `SAMPLE/`, but the identifier an answer quotes is the bare id — `job-001`, `trn-002`, `live-001` — with no prefix. The prefix does not reach the screen; the app's persistent 「演示语料」 flag is what does. |

The same `SAMPLE/` sentence was copied in five places (two banners,
`home.preview.disclaimer`, both READMEs), so it was wrong in five places at once.

## Why

**The count.** It was typed into the copy. The national layer later added
`nat-001`…`nat-005`, and prose has no way to notice. 21 was probably right the
day it was written — which is exactly the failure mode: a number in a sentence
is a fact with no producer, and it decays silently in the safe-looking direction.

**The prefix.** `SAMPLE/` is a `source_ref` prefix. Whoever wrote the sentence
was describing the data file; the reader parses "编号" as the thing they see in
the answer. Both readings are defensible, which is what made it survive review.

**Card 4 had a subtler version of the same problem.** "全国实时检索要单独配置" is
a statement about the *build*. The moment a deployment configures it, the
sentence is stale in the other direction — the honesty section then understates
what the reader is actually getting.

## The fix

**One producer for the facts, and it is the running instance.**
`deploymentFacts()` in `internal/httpapi/server.go` returns
`corpus_opportunities`, `corpus_knowledge_docs` and `live_search_enabled`;
`/api/health` and `/api/meta` both merge it, so the front page and the
conversation cannot disagree.

**Served from `/api/health`, not `/api/meta`.** The landing page's readers are
not signed in. `/api/meta` sits behind the sign-in gate, and the first attempt
did exactly what the old copy did — failed quietly, leaving both sentences
invisible with a 401 in the console. The gate's list of open paths is short on
purpose ("every byte of data, every call that spends model tokens"), and
widening it for a front-page sentence would trade a security boundary for a
decoration. `/api/health` is already open, is already the "what is this
deployment" endpoint, and already reported `corpus_records`.

**The claims carry no numbers; the deployment facts are separate sentences.**
`home.limits.l1count` and `home.limits.l4on` / `l4off` are filled by `home.js`
after the fetch. If the request never arrives the limitation still reads
correctly — the reader loses the tally, not the sentence — and the omission is
logged (`META_UNAVAILABLE` / `META_UNUSABLE`) rather than swallowed. A count of
0 or a missing field is treated as unusable rather than rendered, because
"0 条记录" would be a new false statement in place of the old one.

**The prefix sentence now says what it means** — 依据编号 / "source reference" —
in all five places, and the "reaches the screen" claim is replaced by the flag
that actually does.

## Card 4 was turned on, and verified against the live provider

`OBA_SEARCH_API_KEY` (bocha) was configured locally. `deploy/k8s/10-secrets.yaml`
already declares the key as an External Secret from AWS Secrets Manager
(`opportunity-bridge/model` → `OBA_SEARCH_API_KEY`), so the production path needs
the *value* written there and nothing else.

Live end-to-end, real model and real search, asking as a resident in 深圳 — a
city the corpus does not cover:

- Before: the official portal and 12333, and nothing else.
- After: named employers with district, pay and posting date (`live-002` 龙岗
  普工 5000–6000, `live-003` 宝安 包吃住五险, `live-005` 宏济医疗, `live-006`
  明月星皮具), named courses (`live-011`/`live-012` 智能制造、工业视觉、AI 辅助
  编程), the official portal `live-001` hrss.sz.gov.cn, a tracked task, every
  live result labelled 未经核实 with a fraud caveat, and no eligibility verdict.

## Found while verifying, and fixed: the provider was rate-limiting itself

One `Bocha.Lookup` fans out **six requests** (2 intents × 3 freshness windows)
with no bound, and a turn issues several lookups — so an ordinary question put
roughly eighteen requests on the wire at once. The vendor refused most of them
with 429.

The damage is not the refusal, it is *which* request gets refused. What dies is
whichever window loses the race, and in the measured 深圳 turn that was
`oneWeek`, three times out of three. Losing the freshest window drops the answer
back to `noLimit`, which is how somebody is handed openings a year and a half
old while every log line says the lookup succeeded — the failure
`docs/bugfix/2026-08-28-live-listings-were-years-old.md` exists to prevent,
arriving through a different door.

### The first measurement was wrong, and it nearly shipped

An initial probe concluded the ceiling was three concurrent requests: three
succeeded repeatedly, twelve did not. Those probes were single lookups **spaced
six seconds apart** — a burst against an idle vendor, not the sustained traffic
a real turn produces. Shipped at three, a live turn still lost four windows.

Re-measured properly: three lookups back to back (eighteen requests), swept in
both orders so one width's leftovers could not be read as the next width's
result.

| width | results | 429s | windows lost | elapsed |
|---|---|---|---|---|
| **1** | **15** | **0** | **0** | 4.8s |
| 2 | 12 | 8 | 8 | 1.7s |
| 3 | 10 | 10 | 10 | 1.2s |
| **1** (repeat, straight after width 3) | **15** | **0** | **0** | 5.5s |

Whatever the vendor counts, it refills slower than the fan-out empties it. The
only width that holds is the one that never has two requests outstanding.

### The fix

A per-instance in-flight gate on `Bocha` (`MaxInFlight`, default 1), acquired in
`fetchWindow` and released after. It is per *instance*, not per lookup, because
the ceiling belongs to the vendor account — two people asking at the same time
share it. Waiting is bounded by the caller's context, and a request that never
got a slot reports `SEARCH_NOT_ATTEMPTED` rather than a vendor error, so an
operator is not sent to look at an API that never saw it.

Not an env var: there is one deployment, and a knob nobody sets is a knob that
goes stale. A deployment on a larger plan raises `MaxInFlight`, and should bring
a fresh measurement rather than an intuition.

### Verified on the same live turn, before and after

|  | ungated | gated (width 1) |
|---|---|---|
| search windows lost | 5, three of them `oneWeek` | **0** |
| posting dates cited in the answer | 2025-02 … 2025-11 (up to 18 months old) | **2026-08-25, 2026-08-26** (five and six days old) |

The model no longer has to warn the reader that "发布日期都比较早", because the
listings are from this week.

### The remaining limit is capacity, not correctness

Serialised, a turn's eighteen search requests take about five seconds, and that
budget is shared across everyone using the deployment at once — roughly one
live-search turn per five seconds, inside a 180s turn allowance. Fine for the
current deployment; a real load needs a larger vendor plan, or a smaller
fan-out than 2 intents × 3 windows.

## What was not changed and why

- **Card 2, 「提交」没有对接受理系统.** There is no third-party filing interface
  to connect to. `application_submit` already records and tracks the filing and
  says plainly that the person must still complete it through the channel shown,
  which is the correct behaviour. Making this card false would require inventing
  a submission that did not happen — the exact thing the card exists to refuse.
- **Card 3, 方言是口吻，不是方言.** Speaking a dialect depends on the speech
  vendor carrying that voice; understanding one requires speech recognition,
  which this product does not have in any form — there is no audio input path at
  all. Adding one also contradicts the page's own claim that no audio leaves the
  reader's device, which is a decision to take deliberately rather than in
  passing.

## Regression tests

`-count=1` matters; a cached PASS proves nothing.

| test | file | holds |
|------|------|-------|
| `TestCorpusTallyIsNotWrittenIntoTheCopy` | `web/interface_test.go` | `home.limits.l1b` contains no digit in either language; the tally string exists with both placeholders; `home.js` reads `/api/health` and substitutes both; both elements are `hidden` in the markup |
| `TestSampleClaimDescribesTheSourceRefNotTheVisibleID` | `web/interface_test.go` | the "reaches the screen" claim is gone, and every string mentioning `SAMPLE/` says it is the source reference |
| `TestLiveLookupStatusComesFromTheDeployment` | `web/interface_test.go` | both the on and off strings exist in both languages, and `home.js` branches on `live_search_enabled` |
| `TestDeploymentFactsAreReadableWithoutSigningIn` | `internal/httpapi/server_test.go` | a client with no cookie gets the three facts from `/api/health`, they match the corpus, and nothing personal rode along |
| `TestMetaDeclaresTheLimitsUpFront` | `internal/httpapi/server_test.go` | `/api/meta` reports the same counts as the loaded corpus |
| `TestLookupDoesNotExceedTheInFlightCap` | `internal/livesource/bocha_test.go` | an explicit `MaxInFlight` is honoured, and all six requests are still made — the cap paces the fan-out, it does not shrink it |
| `TestTheShippedDefaultSerialisesSearchRequests` | `internal/livesource/bocha_test.go` | the shipped default never has two requests outstanding; raising it silently reds here |
| `TestWaitingForASlotEndsWithTheTurn` | `internal/livesource/bocha_test.go` | a queued request ends with its context and is reported as never sent, not as a vendor failure |

Mutation drills: putting "21 条岗位、12 份指南" back into `home.limits.l1b` reds
the copy fence; hardcoding `corpus_opportunities: 21` reds the meta fence; and
setting `defaultMaxInFlight` back to an unbounded value reds the cap test with
"peak concurrency 6". All three go green when reverted.
