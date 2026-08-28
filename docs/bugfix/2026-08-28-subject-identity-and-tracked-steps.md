# "Open tasks" was always empty, and every reload made you a new person

**Reported:** 2026-08-28, "ongoing task never shows", alongside "it can't
pinpoint jobs from a city".
**Area:** subject identity (`web/static/app.js`, `store.CreateSession`), the
`next_step_is_tracked` verifier, and the live-search capability flag on
`/api/meta`.
**Status:** fixed. One related gap is **configuration, not code** — see the last
section.

## What it looked like

The overview panel said "还没有任务。达成一致的每一步都会记在这里。" in every
conversation, including ones that ended with a concrete instruction ("下一步做这
一件：打深圳 12333"). Meanwhile the state file held three tasks, so tasks were
being created and nobody could see them.

## Why — two independent causes

### 1. Every page load minted a new person

`boot()` opens a session, and `newSession()` posted `{role, locale}` with **no
subject id**. `CreateSession` mints one when it is not supplied, so a reload, a
role change, or a new conversation produced a brand-new subject.

Tasks, the profile and the consents all hang off the subject
(`TasksFor(ses.SubjectID)`), so all three restarted from nothing every time. The
state file shows it plainly — twelve sessions, twelve subjects:

```
ses_0018 -> sub_0019   ses_0020 -> sub_0021   ses_0022 -> sub_0023
ses_0024 -> sub_0025   ses_0026 -> sub_0027
```

This is worse than an empty panel. The consent card promises "下次不用你再说一
遍……一直保存到你让我删。" A reload deleted the way back to that record, so the
product was making a data promise it could not keep. That is not a UX defect; on
this project's own ordering (安全 > 稳定 > UX) it is the most serious thing in
this document.

This was **known and deferred**: `docs/bugfix/2026-08-28-session-list.md` records
"every page load minted a conversation" and parks collecting the shells as out of
scope. What nobody followed was that the subject changed with them.

### 2. The one check that could have forced a task was satisfied by a phone number

```go
func verifyActionableNextStep(in VerifyInput) []Finding {
	if phoneToken.MatchString(in.Answer) || urlToken.MatchString(in.Answer) ||
		containsAny(...) || ranTool(in, "case_task_create", ...) {
		return nil
	}
```

`||` short-circuits. Every good answer contains 12333, so the first arm always
matched and the `case_task_create` arm was never evaluated. Creating a task was
therefore optional in `individual_pathway`, while ending on a next step was
mandatory — the panel was fed by a tool nothing required anyone to call.

Evidence: across twelve sessions, three tasks. Two were created by
`handoff_to_human` as a side effect. Exactly one came from the model deciding to
track something, and only after the person asked "怎么报名？".

## Three layers

| Layer | Finding |
|---|---|
| Implementation | The client never sent `subject_id`; the server has accepted it all along. |
| Design | Two: identity had no lifetime beyond one page, and "hand over a next step" was never tied to "record it". A panel fed by an optional tool is a panel that is empty by default. |
| Process | `actionable_next_step` verified the *wording* of an answer and was read as verifying the *outcome*. Nothing tested the difference. |

**Owner:** cause 1 is the web client plus the deferred item in the session-list
fix; cause 2 is the `individual_pathway` intent definition.

## What changed

**Identity.** The client stores the server-issued subject id in `localStorage`
and sends it when opening a session. The id is opaque and carries nothing
personal. Because that is now the truth, the retention wording says so in both
languages: it is tied to this device, and clearing browser data loses it. A
promise the system can keep.

**Tracking.** A new verifier, `next_step_is_tracked`, wired into
`individual_pathway`, with a matching line in the directive. It fires only when
the turn retrieved a **named** programme and recorded nothing;
`case_task_update`, `handoff_to_human`, `application_submit` and
`document_prepare` all satisfy it, so a conversation circling one step produces
one record rather than one per turn.

It reads a new `corpus_hits` field on `opportunity_search`'s `Meta` rather than
`result_count`. That distinction is the fix's own bug, caught by the eval suite:
`result_count` folds in live-directory results, whose answer is "your region's
portal is here" — so the first version demanded that a city with no coverage
track a website. `turn-no-results-is-honest` went red and said so.

**Scope deliberately not widened.** `low_access_support` shows the same panel and
has the same gap, but it does not allow `case_task_update`, so the remedy would
tell the model to call a tool that intent forbids. Adding the verifier there
means widening a tool allowlist, which is a separate decision. `TestIndividual
PathwayRequiresTheStepToBeRecorded` asserts the escape hatch is reachable so this
cannot be copied across without noticing.

**Four scripted fixtures were updated**, not to make tests pass but because the
contract changed: a turn that finds a named programme now records the step.
`turn-individual-happy`, `turn-bad-arguments-recover`,
`turn-chinese-answer-passes`, plus the stream-shape test in `httpapi` and two in
`agent`.

## The city question — configuration, not a defect

"It can't pinpoint jobs from a city" is two facts, neither of them a bug:

- `data/opportunities.json` is a Chengdu sample: 21 of 26 records are 成都, the
  other 5 are national. A Shenzhen query matches nothing local, and the agent
  saying "深圳没有哪家瑜伽馆或课程是登记在我这边数据里的，我不替你编一个" is it
  working correctly.
- The provider that *would* return named employers nationwide, `websearch`, is
  off because `OBA_SEARCH_API_KEY` is unset. What remains is `directory`, which
  returns the official regional portal and the hotlines — a real destination,
  never a listing.

The seam is already there (`livesource.Provider`); nothing needs writing to add
a real feed. What was wrong is that **the difference between "there is nothing"
and "I cannot look" was invisible**: startup logs `LIVE_SEARCH_DISABLED`, and
nobody reading the page can see a log. `/api/meta` now carries
`live_search_enabled`, and the interface shows a muted "未接全国检索" flag with
the explanation, reusing the pattern already used for the sample-data flag.

Turning it on is one line:

```
OBA_SEARCH_API_KEY=<a Brave Search API key>
```

## Regression tests

| Test | Fence |
| --- | --- |
| `guardrail.TestNextStepMustBeRecordedNotJustWritten` | a step handed over in text only is sent back — and asserts `actionable_next_step` still passes the same input, documenting why the checks are separate |
| `guardrail.TestRecordingOrUpdatingTheStepSatisfiesTheCheck` | create, update, handoff and submit all satisfy it; no task-per-turn |
| `guardrail.TestNoTrackingDemandedWhenOnlyTheDirectoryAnswered` | a city with no coverage is not asked to track a website |
| `guardrail.TestNoTrackingDemandedWithoutRetrieval` | a clarifying question is not asked to invent a task |
| `intent.TestIndividualPathwayRequiresTheStepToBeRecorded` | the verifier is wired, and its remedy names a reachable tool |
| `tools.TestOpportunitySearchSeparatesCorpusHitsFromLiveResults` | corpus hits stay distinguishable from live results |

All mutation-drilled: reverting each rule turns the matching test red.

```
GOWORK=off go test ./... -count=1
```

## Still open

- **Identity is per-browser.** A different device, or cleared site data, is a
  different person. The wording now says so. Real identity is a product
  decision, not a fix.
- **The empty session shells still accumulate**, unchanged from the session-list
  fix.
- **`low_access_support` still does not require the step to be recorded.**
