# The listings were years old, and then briefly they were not jobs

**Reported:** 2026-08-28, "all the jobs are not latest", with a screenshot
showing postings dated 2025-02-12, 2025-04-16 and **2020-07-14**.
**Area:** `internal/livesource/bocha.go`.
**Status:** fixed, with a limit that is worth knowing about (see the last section).

## What was wrong

Three separate defects, found one behind the other.

### 1. Only one freshness window was asked for, and it was the wrong one

The provider asked Bocha for `freshness=noLimit` and nothing else. `noLimit` is
**not** "everything, ranked by relevance" — it is its own result set, and it
largely **misses** recent postings. Measured 2026-08-28, newest result that
actually concerned the city asked about:

| query | `oneWeek` | `noLimit` |
| --- | --- | --- |
| 深圳 保洁 招聘 | 2026-08-26 | 2026-01-04 |
| 深圳 普工 白班 | 2026-08-27 | 2026-03-19 |
| 成都 普工 招聘 | 2026-08-28 | 2025-09-04 |

**Why the original choice was wrong, since the reasoning is the interesting
part.** `noLimit` had been picked on a measurement that counted city-correct
results in the **raw** response, where `oneMonth` scored 10/24 against
`noLimit`'s 21/21. That measurement was taken before the city filter existed,
and the filter is precisely what neutralises a narrow window's weakness: the
wrong-province results `oneMonth` returns are exactly the ones `mentionsCity`
discards. **Measuring the input to a filter and drawing a conclusion about its
output** is the mistake. Counted after filtering, the narrow windows are fine.

Fixed by querying `oneWeek`, `oneMonth` and `noLimit` **concurrently** and
merging. They are not nested in practice — 深圳 保洁 returned 3 city-correct
results for `oneWeek` and 0 for `oneMonth` — so a wider window is not a superset
of a narrower one and all three earn their place.

### 2. Nothing was ordered by date

Bocha orders by relevance, and relevance has no opinion about whether a job
still exists. A 2020 posting sat above a current one. Results are now sorted
newest-first, and anything older than **two years** is dropped: at that age a
posting is an archive page, not a lead. Every result Bocha returned in testing
carried a date (70 of 70), so this discards on evidence. A result whose date
cannot be read is kept and sorted last — it cannot be **shown** to be stale, and
discarding it would throw away real listings to enforce a rule about dates.

### 3. The search never mentioned work — found only after fixing 1 and 2

With the freshness fixed, the results became **current and useless**: a
cosmetic-surgery advertisement, two insurance stories and a cleaning-services
procurement notice. All in 深圳, all within two days, none of them a job.

The cause was upstream of everything above. The agent passes the trade, not the
intent — a real turn sent `query="保洁"` — so the search was literally
`深圳 保洁`. `noLimit` had been concealing this the whole time, because its
relevance ranking happens to favour job boards. Measured on `深圳 保洁`: 4 of 20
results looked like recruitment. With `招聘` appended: **16 of 17**.

Three rules now, each guarding a different failure:

| Rule | Drops |
| --- | --- |
| `mentionsCity` | a posting in another province — costs a journey |
| `isRecruitment` | insurance news, medical advertising, procurement notices |
| `mentionsAll` | current hiring for work nobody asked about (school librarians, for a cleaner) |

## Verified live

Same code, two real turns through the running agent:

```
我在深圳，想找保洁的活     2026-01-04(236d) 2025-10-25 2025-08-20 2025-02-12
我在佛山，会焊工，想找活干  2026-08-24(4d)   2026-08-23 2026-08-04 2026-08-04
```

The noise is gone from both, ordering is newest-first, and the 2020 page no
longer appears.

## The limit, stated plainly

**This cannot manufacture recency the index does not have.** 佛山 焊工 returns
listings four days old because current welding demand is published and indexed;
深圳 保洁 returns nothing fresher than eight months because, in this index, that
is what exists. The date on every card is what makes that visible rather than
hidden, and it is the reader's to judge.

A genuinely current feed would need a different kind of source — a provincial
open-data endpoint or a partner job board — which is one more implementation of
`livesource.Provider`, not a change to the agent.

Also unchanged: many results are **board pages** (`猎聘`, `BOSS直聘`, `鱼泡`)
rather than single postings. Those pages are live even when their declared date
is old, so the date shown for them understates how current they are. Telling the
two apart reliably is not something this can do today, and guessing would be
worse than the date.

## Regression tests

`internal/livesource/bocha_test.go`, all mutation-drilled:

| Test | Fence |
| --- | --- |
| `TestBochaAsksEveryFreshnessWindow` | one window is not enough |
| `TestBochaAnswersNewestFirst` | relevance order does not reach the reader |
| `TestBochaDropsPostingsTooOldToBeALead` | the 2020 case |
| `TestBochaKeepsUndatedResultsButRanksThemLast` | an unreadable date is not evidence of staleness |
| `TestBochaShowsOnePostingOnce` | overlapping windows do not duplicate |
| `TestBochaSurvivesOneFailedWindowButNotAll` | partial failure answers; total failure errors |
| `TestBochaDropsPagesThatAreNotAboutHiring` | procurement notices and industry reports |
| `TestBochaDropsHiringForWorkThatWasNotAskedAbout` | right city, right intent, wrong trade |
| `TestBochaAsksAboutWorkEvenWhenTheCallerDidNot` | `招聘` is added, and not twice |

**One of these was a fake fence when first written.**
`TestBochaDropsPagesThatAreNotAboutHiring` passed with `isRecruitment` deleted,
because its decoys did not contain the keyword and the keyword filter removed
them anyway. Its decoys now all mention 保洁 — including the real procurement
notice — so it fails when and only when the rule it names is gone.
