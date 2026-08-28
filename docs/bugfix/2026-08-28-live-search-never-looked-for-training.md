# The live search never looked for training

**Reported:** 2026-08-28, asked as a question — "培训推荐链路落地了吗？"
**Area:** `internal/livesource`, `internal/tools/builtin.go`, `web/static`.
**Status:** fixed. One limit is worth knowing about, and it is not cosmetic —
see *What this still cannot claim*.

## What was wrong

Half of the training recommendation chain was complete and the other half could
not work, in a way that nothing in the product said out loud.

**The corpus half was fine.** `domain.KindTraining`, five course records in
`data/opportunities.json`, a `kinds` filter in retrieval, a `培训` badge in both
languages, and two evaluation turns that recommend `trn-002` with its published
criteria and a tracked next step. Ask about a course in 成都 or 深圳 and the
answer is right.

**The live half was structurally incapable of returning a course**, and every
city that is not those two goes through the live half:

1. **The intent never reached the lookup.** `livesource.Query` carried
   `City`, `Keyword` and `Limit` — no notion of what kind of thing was wanted —
   and `opportunity_search` built its live query from exactly those three. The
   `kinds` the model had just chosen were dropped on the floor.

2. **The query was rewritten to ask about jobs.** `recruitmentQuery` appended
   `招聘` unless the caller's words already contained a hiring word. `培训` is
   not a hiring word, so *"成都 养老护理培训"* went out as
   *"成都 养老护理培训 招聘"*.

3. **Anything that was not a job advert was discarded.** Every result had to
   pass `isRecruitment`, whose word list — 招聘、岗位、月薪、日结… — contains no
   word a course page uses. A 职业技能培训招生简章 matches none of them and was
   dropped.

There is a fourth, which only shows up once the first three are fixed:

4. **A multi-word query returned nothing at all.** `mentionsAll` required every
   word the agent sent to appear on the page. A real turn sent
   `query="数控 培训 转岗 流水线"`; no course page also says 转岗 and 流水线, so
   the correct number of surviving results was zero.

### Why this appeared now

Defects 2 and 3 were introduced the same day, by the fix for
[the listings being years old](2026-08-28-live-listings-were-years-old.md). Its
third defect — "the search never mentioned work" — was real and the fix was
right for the case it was measured on. What it did was hard-code *one* intent
into a channel that serves two. Before it, `noLimit`'s relevance ranking merely
*happened* to favour job boards, so a course page could still come through by
accident; afterwards, three filters removed it on purpose.

Defect 1 is older and is the load-bearing one: the wire was never there.
`livesource` could have been taught about courses at any point and would still
never have searched for one.

### Who this belonged to

The 2026-08-28 freshness change. It widened the meaning of a shared component
(the live lookup) to match one caller's needs (job seekers) without asking what
the other caller needed. The signal it should have tripped on is in its own
commit message: it named the new constant `recruitmentQuery` in a package whose
own doc comment says it returns "live openings **and courses**".

### What the person saw

In any city outside the corpus, "我想学个技能" returned recruitment adverts, or —
after defect 4 — nothing at all. Nothing is the worse outcome: the answer then
reads as *there is nothing for you here*, which is untrue and is the exact
failure `livesource` exists to prevent.

## The fix

**`livesource.Intent`** — `work` or `training` — is now carried on the query and
decides three things that must stay in step:

| | work | training |
|---|---|---|
| Term added to the query | `招聘` | `培训` |
| Words a page must use | `recruitmentWords` | `trainingWords` |
| Warning carried to the reader | deposits, up-front fees | 培训贷, 包过/包证/包分配 |

They live in one table (`intentProfiles`), because searching for 培训 while
accepting only 招聘 pages returns nothing, and accepting course pages while
warning about job scams tells the reader to watch for the wrong thing.

**Two searches, not one wider filter.** `成都 数控 招聘` and `成都 数控 培训`
return different pages, so a course cannot be recovered by filtering a
recruitment search. Each intent is asked in every freshness window and each
result is judged against the intent that asked for it — which is also what stops
the obvious wrong fix, widening one word list, from handing every job seeker
course adverts.

**The wire.** `opportunity_search` passes `kinds` through
`livesource.IntentsFor`. `job`→work, `training`→training; subsidy and
entrepreneurship have no row, because they are administered by an authority
rather than advertised as pages somebody can turn up to. Asking only for those
*widens* to both rather than searching for nothing: an unmapped kind must never
become silence.

**`mentionsAll` → `mentionsAny`.** With one keyword the two are the same rule, so
the case it was written for — librarian vacancies offered to a cleaner — is
unchanged. With several, the choice was never between precise and loose but
between loose and empty. Intent vocabulary is removed from the word bag first
(`tradeNeedles`), so `培训` cannot be the word that satisfies "is this about what
they asked for" for a course page.

**Determinism.** Results are read back in a fixed request order instead of
drained from a channel, so a page both intents accept is labelled the same way on
every run. Two identical turns must not carry two different fraud warnings.

**Also fixed, because they blocked the same feature end to end:**

- `web/static` renders a course lead as a course lead (`card.liveCourse`), in
  both languages, instead of putting the reader back to guessing from a title.
- `.env.example` still documented Brave's URL as the default while the code had
  defaulted to bocha since the provider was added. An operator following it got
  a bocha-shaped POST sent to Brave's GET endpoint, which fails **silently**.
- `docs/16-live-lookup.md` said there were two providers and did not mention
  bocha at all.

## What this still cannot claim

**`trainingWords` has never been measured against the live index.** The
recruitment list carries a real count — on `深圳 保洁`, 4 of 20 results looked
like recruitment, and 16 of 17 with `招聘` appended. The training list was
written from the vocabulary these pages use and is fenced only against fixtures,
because **no Bocha key was available in this checkout** when it was added. Until
somebody runs it against the live index and counts, the honest statement is that
the path is wired, filtered and fenced, not that its recall is known.

The measurement to run, once a key exists: for three cities × three trades,
count how many returned pages are genuinely course pages before and after the
filter, the same way the recruitment list was counted.

Unchanged and out of scope: when `kinds` is omitted and the corpus *does* hold a
local listing of any kind for that city, the live lookup does not run at all —
so a city with jobs in the corpus but no courses will not be searched live for
courses. That gate reads `countLocal`, which is kind-blind. It only bites on a
partially-covered city, and widening it changes how often the web is called.

## Regression tests

`internal/livesource/bocha_test.go`, `internal/livesource/livesource_test.go`,
`internal/tools/tools_test.go`, `web/interface_test.go`.

| Test | Fence | Drilled |
| --- | --- | --- |
| `TestATrainingQuestionOutsideTheCorpusComesBackWithACourse` | the whole chain, tool → Bocha stub → course | ✅ |
| `TestOpportunitySearchTellsTheLiveLookupWhatKindWasAskedFor` | the wire: `kinds` reaches the lookup | ✅ |
| `TestBochaAsksForTheRightThingPerIntent` | the right term is added, and not twice | ✅ |
| `TestBochaReturnsCoursesForATrainingLookup` | the defect itself | ✅ |
| `TestBochaDoesNotOfferCoursesToSomebodyLookingForWork` | the obvious wrong fix | ✅ |
| `TestBochaReturnsBothWhenNothingNarrowsTheLookup` | both, each labelled | — |
| `TestBochaWarnsAboutTheFraudThatMatchesTheIntent` | 培训贷 vs deposits | ✅ |
| `TestBochaKeepsTheTradeFilterWhenTheQuerySaysTraining` | 保安 course for an 养老护理 question | ✅ |
| `TestBochaMultiWordQueriesDoNotReturnNothing` | defect 4 | ✅ |
| `TestBochaLabelsTheSamePageTheSameWayEveryTime` | determinism | see below |
| `TestIntentsForMapsCorpusKindsOntoSearchableIntents` | `job` is not `work`; unmapped kinds widen | ✅ |
| `TestWebSearchDropsPagesThatDoNotMatchTheIntent` | Brave judges the page too | ✅ |
| `TestLiveCardTellsACourseFromAnOpening` | the badge reads `intent` | ✅ |

`make drill` breaks each rule on purpose and checks the named test goes red;
`scripts/mutation-drill.sh` restores every file it touches, on signals too.

Ten of the thirteen are drilled. **Three are not, and the reason differs:**

- `TestBochaLabelsTheSamePageTheSameWayEveryTime` guards a *nondeterminism*,
  which a single deterministic edit cannot express — reverting to a channel
  drain is a structural change, not a substitution. It is kept because it would
  catch that revert, but it has not been shown to fail on demand.
- `TestBochaReturnsBothWhenNothingNarrowsTheLookup` is covered from both sides by
  the two drilled single-intent fences either side of it.

The drill runner itself was checked against a **negative control**: a harmless
comment edit leaves the test green, which the runner reports as `NOT A FENCE`. A
runner that cannot fail proves nothing about the fences it blesses.
