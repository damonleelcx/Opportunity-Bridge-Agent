# Daily spending ceilings: one per account, one for the whole deployment

**Decided:** 2026-09-01, hours after sign-up stopped needing an invite code.
**Kind:** the other half of that change. Filed here because it closes a gap that
document named out loud and left open.
**Area:** `internal/store/spend.go`, `internal/httpapi/spend.go`,
`Account.SpendDay`/`SpentTokens`, the `deployment_spend` key in `meta`,
`OBA_ACCOUNT_DAILY_TOKENS` / `OBA_DEPLOYMENT_DAILY_TOKENS`.
**Status:** done. Eleven fences, six mutation drills, verified live.

## What was exposed

Removing the invite code left this written down as the accepted trade:

> There is no per-account spend cap. `agent.Budget` is per turn, not per
> account, and nothing counts what an account has spent in total.

The size of it: one turn may run 8 iterations × 16k output tokens
(`OBA_MAX_OUTPUT_TOKENS=120000`), with input — history, tool definitions,
retrieved corpus text — **not bounded at all**. The ingress allows 30 requests a
minute per IP. Nothing anywhere counted the second turn, or the ten-thousandth.

## Why two ceilings and not one

A per-account cap is what was asked for, and on its own it does not bound the
bill. **Sign-up is open**, so accounts are free and unlimited: an allowance of N
tokens per account is an allowance of N × (however many accounts somebody
registers), and at 30 req/min that is on the order of a thousand accounts an
hour. The per-account cap bounds what one person can do; it does not bound what
one person with a script can do.

| | Bounds | Answers |
| --- | --- | --- |
| `OBA_ACCOUNT_DAILY_TOKENS` | one account | "has this person had a fair share today?" |
| `OBA_DEPLOYMENT_DAILY_TOKENS` | every account together | "has the service spent more than I agreed to?" |

Neither is redundant. Only the second actually caps the invoice, and only the
first keeps one heavy account from being the reason everybody else is refused.

**The cost of the second one, stated plainly:** when the deployment ceiling
trips, *nobody* gets an answer until 00:00 UTC. That is inherent to a global
circuit breaker — it is the difference between a bounded bill and an unbounded
one — and it is why the ceiling belongs well above legitimate aggregate use and
why crossing it logs at ERROR.

## Why tokens, and why the UTC day

**Tokens**, because they are what is billed, and because `agent.Run` already
returns them per turn (`Result.Usage`) — the turn handler was discarding the
value. No price table to drift, no new measurement, no model plumbing. Counting
*turns* instead was considered and rejected: one turn can cost 2k tokens or
240k, so a turn cap caps a number that barely correlates with the bill.

Input is counted as well as output — input is the larger half here. Cache reads
are deliberately not added on top; they are already inside the reported input
count, and adding them would charge twice for the cheapest tokens in the request.

**The UTC day**, stored *beside* the count, so the reset is a **comparison**
rather than an event: a counter whose day is not today reads as zero. There is no
nightly sweep, because a nightly sweep is a background job that can fail
silently, and its failure mode is everybody locked out of a service that looks
healthy. The same comparison runs on the write path, so the first charge of a
new day replaces the stale total instead of adding to it.

## Where the state lives — no migration

- **Per account:** two fields on `store.Account`, which persists into the
  existing `accounts.doc JSONB`. Accounts that predate this simply start at zero.
- **Deployment:** one key in the existing `meta` key/value table, beside `seq`
  and `legacy_adopted`.

No DDL in either direction, and rollback-safe: an older binary ignores an unknown
`meta` key and an unknown `doc` field, `saveMeta` upserts without sweeping, so
rolling back and forward again picks the counters up where they were rather than
handing everybody a fresh allowance.

## Where it is enforced

`POST /api/sessions/{id}/messages` is the only path in the HTTP API that reaches
the model — checked by grepping every `Agent.Run` and LLM call site. The gate
stands there, **before the stream opens**, so a refusal is an ordinary JSON error
that the interface already renders inline (`notice(turn, "block", code, message,
remedy)` in `app.js`). No client change was needed.

| Code | HTTP | Says |
| --- | --- | --- |
| `SPEND_CAP_REACHED` | 429 | your account has used today's allowance; here is when it refills |
| `SERVICE_BUDGET_REACHED` | 503 | the service has stopped for everyone; **this is not about you** |

The deployment ceiling is tested first. When both are exceeded, the person needs
the truer sentence — telling them *they* are out of allowance would send them off
to make another account, which is the exact behaviour the ceiling exists to stop.

## Observability

`/api/health` reports `spend_today_tokens` and `spend_ceiling_tokens`. It is in
`health()` and deliberately **not** in `deploymentFacts()`, which also feeds
`/api/meta` and the landing page: these are operating numbers, not something the
product states about itself.

`/api/health` is public, so those two numbers are public. What they leak is
roughly how busy the service is; what they buy is a ceiling set from real use
instead of a guess. Knowing the service is near its limit tells an abuser nothing
they would not learn from their next request.

Each ceiling is announced **once**, by the turn that crossed it, not on every
refusal that follows — otherwise the one line an operator needs would be buried
under the hundreds of bounces it causes.

## Defaults, and the honest status of the numbers

| Setting | Ships as | Basis |
| --- | --- | --- |
| `OBA_ACCOUNT_DAILY_TOKENS` | 2,000,000 | ~40–100 substantial turns. A heavy genuine day sits well under it |
| `OBA_DEPLOYMENT_DAILY_TOKENS` | 50,000,000 | **a placeholder informed by no invoice** |

The second number is a guess and is labelled as one everywhere it appears. Read
`spend_today_tokens` for a week and set it from what the service actually uses
and what you are willing to spend. That is why the gauge shipped with the cap
rather than after it.

`0` disables either ceiling. That direction is deliberate and is fenced: the
opposite reading — absent setting means "capped at zero" — is the exact trap the
invite-code default fell into, where a forgotten setting silently closed the
service. Here a forgotten setting leaves it open, and the gauge is how you find
out.

## What this deliberately does not do

1. **A turn may overshoot.** The gate asks "may this turn start", not "will this
   fit". Killing a turn in flight would waste the tokens already spent and leave
   somebody looking at half a sentence. The overshoot is bounded by the per-turn
   ceilings, which is the job they already do well.
2. **Classifier tokens are not counted.** `intent.Route` does not return usage,
   so its flash-model call is invisible to the cap. Small and bounded — most
   turns never reach it — but it is an undercount, not a rounding artefact, and
   it is written down here rather than discovered later from a bill.
3. **A store write failure means the spend is not recorded**, following the
   store's contract everywhere else. Consequence: while the database is
   unwritable, the caps do not bite. Refusing turns whenever the database is
   unhappy would break the main path to protect a budget, which is the wrong way
   round for this product.
4. **No per-account exemption.** `0` disables a ceiling globally; there is no
   allowlist. Lock yourself out and the fix is to raise the number.
5. **No usage meter in the interface.** The refusal is the whole UI. A running
   "you have X left" is a bigger surface than the problem needs today.
6. **The refusal text is English**, like every other server error here. This is
   the first one ordinary Chinese-speaking users are expected to meet, so it is
   the best argument yet for localising server errors — flagged as its own
   decision rather than smuggled in as a one-off.
7. **A backwards clock jump grants a fresh allowance**, because the day stamp
   would no longer match. Bounded by the deployment ceiling, which the same jump
   also resets — worth knowing, not worth a ratchet on this data.

## Regression tests

`internal/store/spend_test.go` — the counting and the reset:

| Test | Fence |
| --- | --- |
| `TestAddSpendChargesTheAccountAndTheDeployment` | both counters move; one account is not charged for another's turn |
| `TestSpendFromAnEarlierDayReadsAsZero` | the reset, with nothing having swept |
| `TestFirstChargeOfANewDayStartsFromZero` | the write path resets too — otherwise reads look right while writes accumulate on yesterday |
| `TestAddSpendIgnoresNothingAndRefusesToRefund` | no negative charge can hand out free allowance |
| `TestSpendOnAnUnknownAccountIsQuiet` | deleted-mid-request does not panic a turn |

`internal/httpapi/spend_test.go` — the gate, driven through the real endpoint a
browser posts to:

| Test | Fence |
| --- | --- |
| `TestSpendIsRecordedAgainstTheAccountThatSpentIt` | the turn is charged, to the right account |
| `TestAccountSpendCapRefusesTheNextTurn` | 429 `SPEND_CAP_REACHED`, asserted on the code and on the remedy naming a reset time |
| `TestDeploymentCeilingRefusesEveryAccount` | an account that spent nothing is refused too, and is not blamed for it |
| `TestSpendCapsDisabledAtZero` | 0 means unlimited, and spending is still counted so a ceiling can be sized |
| `TestHealthReportsTheDaysSpending` | the gauge moves after a real turn |

`internal/store/pg_test.go` — persistence on the real backend:

| Test | Fence |
| --- | --- |
| `TestSpendCountersSurviveARestart` | both counters come back after a reopen — the account field from `accounts.doc`, the deployment total from the `meta` key |

That last one earns its place: neither counter needed DDL, and a field that is
never written to the database still reads back correctly **from memory**, so
nothing else in the suite would notice. In production the symptom would be a
daily ceiling that silently resets on every pod roll.

**Six mutation drills, all fired:**

| Mutation | Went red |
| --- | --- |
| gate never called | the two cap tests |
| turns never charged | five tests, including the health gauge |
| stale day reads as current | the reset test |
| `0` stops meaning "no ceiling" | the spend tests **and six unrelated tests across the suite** |
| write-path reset removed | `TestFirstChargeOfANewDayStartsFromZero` |
| `meta` key dropped on save | `TestSpendCountersSurviveARestart`, on the deployment half only — the account half still passed, which is the test isolating the two halves correctly |

The fourth is the one worth remembering: getting the zero-value direction
backwards does not fail quietly in a corner, it takes the product down — which is
exactly the visibility that default deserves.

```
GOWORK=off go test ./internal/store/ ./internal/httpapi/ -count=1
make test-pg
```

## Verified live, not just in tests

Against a scripted backend with a 30-token allowance, driving the real endpoints:

- turn 1 answered and charged 59 tokens; turns 2 and 3 refused with 429, and the
  counter **did not move** — a refused turn costs nothing;
- the counter was read back out of the persisted state file, not out of the
  response, because a 200 is not evidence that a number was written;
- with a deployment ceiling instead, a second account that had spent **nothing**
  was refused with 503, and the ERROR fired exactly once with the following
  refusals at WARN;
- the day stamp was moved back and the process restarted: the counters loaded
  from disk, yesterday's total read as zero, the refused account could talk
  again, and the new day's first charge started from zero rather than adding to
  yesterday;
- in the browser, the refusal renders inline in the conversation with its code,
  message and remedy — no client change was needed.

**A note on how nearly this verified nothing.** The first live run showed a
counter of zero after a real turn, which looked exactly like the feature being
broken. It was the fixture: `demo/scripted-turns.json` has empty `text` on its
tool-calling turns, and the scripted backend derives tokens from text length, so
the turn genuinely cost 0. A fixture that reports no cost makes every assertion
about cost vacuously true. The tests here use a reply long enough to cost
something for exactly this reason.
