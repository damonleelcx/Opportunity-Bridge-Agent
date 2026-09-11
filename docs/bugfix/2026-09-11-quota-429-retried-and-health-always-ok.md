# A used-up model quota was retried as a rate limit, and /api/health said ok throughout

**Reported:** 2026-09-11, found during a live latency measurement against production's Qwen key.
**Area:** `translateQwenError` (`internal/llm/qwen.go`), the retry layer (`internal/llm/retry.go`), `/api/health` (`internal/httpapi/server.go`).
**Status:** fixed.

**One line:** when the token plan's quota ran out, every model call came back 429. Each one was retried three times as if it were a temporary rate limit, and the person was told "It will be retried; if this persists the answer will be delayed rather than wrong". Meanwhile `/api/health` kept answering `status: ok`, so from outside nothing looked wrong.

## What happened

| Time (UTC) | Observation | Evidence |
|---|---|---|
| 10:49 | A latency spike starts on production's key; its calls succeed | spike log |
| ~10:52 | Every call returns 429 `Your token-plan 1-week quota has been exhausted. The quota will reset at 09-12 11:40:00 UTC.` Each turn fails with `MODEL_RETRIES_EXHAUSTED: gave up after 3 attempts: MODEL_RATE_LIMITED: ... It will be retried ...` | spike log, 39 failed turns |
| 10:53–11:33 | `/api/health` answers `status: ok`. Production has **0** turns in those 40 minutes, so no failed conversation surfaced it either | health response; pod log counts (codes only) |
| 11:46–11:47 | One 1-token request each to `qwen3.8-flash` and `qwen3.8-max` returns 200. The "reset at 09-12 11:40" in the refusal was not when service came back | two requests, 27 + 14 tokens |

## Why

| Layer | Finding |
|---|---|
| Surface | `translateQwenError` chose the error from the HTTP status alone. Every 429 became `MODEL_RATE_LIMITED`, which `retryable()` retries. `/api/health` returned the literal `"status": "ok"`. |
| Design | The vendor tells an account that cannot be served apart from a temporary limit **in the error code, not the status**. A used-up quota and a real rate limit are both 429 (`Throttling.AllocationQuota` vs `Throttling.RateQuota`). The same status-only reading also sent 403 `AllocationQuota.FreeTierOnly` to "rejected the credential" (which prompts reissuing a working key) and 400 `Arrearage` to "a bug in request assembly". Separately, health had no source of truth about the model at all: it could only ever say ok. |
| Institutional | No test pinned a vendor error body, only bare statuses (`TestQwenErrorTranslation`). No health test looked at `status`. The k8s probes read only the HTTP code, so nothing required the body to mean anything. |

### Timeline

| Date | Commit | Event | State |
|---|---|---|---|
| 2026-08-28 | `2e93e63` | First version: 429 is a rate limit, health says `ok` unconditionally, probes on `/api/health` | 💤 reachable from day one; no quota had run out |
| 2026-09-03 | `ff28be0` | Move to Qwen, carrying the status-only mapping over | 💤 same |
| 2026-09-11 | — | Token-plan quota exhausted under a live spike | 🔴 hit |

**Owner:** `2e93e63`. The defect was reachable from the start; the trigger was the first time an account-level refusal occurred. Same-shape precedent: the 402 → `MODEL_BILLING` branch already existed for exactly this reason ("reads as a generic outage and costs somebody an hour"). It covered only the one status that vendor did not also use for something else.

## Fix

**Decisions (damon, 2026-09-11):** check health from real calls, plus one small check when the record is stale; fix all three misread account failures, not only the reported 429; correct the wording only (the person still sees the English error under 出错了, as before).

| | |
|---|---|
| Account failures come from the vendor's code | `qwenAccountFailures`: `Throttling.AllocationQuota` / `insufficient_quota` / `AllocationQuota.FreeTierOnly` → `MODEL_QUOTA_EXHAUSTED`; `Arrearage` → `MODEL_BILLING`. Read before the status. The token plan's weekly refusal has no listed code, so a 429 whose **message** mentions a quota is one too. Only the message is read, because the rate-limit code `Throttling.RateQuota` contains the word. |
| A used-up quota is tried once | `MODEL_QUOTA_EXHAUSTED` is not in `retryable()`. Its text says retrying will not help, what an operator has to do, and carries the vendor's detail (the reset time). |
| A real rate limit is still retried, and no longer promises it | `MODEL_RATE_LIMITED` now reads "it is retried a few times before giving up". That text is what the person sees once the retries are spent. |
| Health knows about the model | `llm.Health` wraps the bare client (below the retry layer, so every attempt counts) and records each call. `/api/health` reports `status` as `ok` / `degraded` / `unknown`, with `model{last_ok_at, last_failure_at, last_error_code, consecutive_failures, last_source, check_enabled}`. Codes and times only: vendor messages name account details and this endpoint is public. |
| When is it degraded | A used-up quota, arrears, a rejected key or an unknown model id: at once. A rate limit, a 5xx or a dropped connection: after three in a row. A success clears it. A call ended by its own context is not counted; a check that times out is. |
| With no traffic | If the newest result is older than 10 minutes, reading health starts one 1-token check against the agent model, thinking off, no tools. At most one per 10 minutes however often health is read (about 2k tokens a day). It needs no timer, because the probes read health every few seconds. Real backend only: on the scripted backend a check would consume a turn of the script. |
| Probes are untouched | HTTP stays 200 in every state. A model outage must not get the pod restarted and take sign-in, the contact-list import and every page that needs no model down with it. |
| Unknown is not ok | Before anything has answered, and on a server with no model watcher, `status` is `unknown`. |

## Not covered, deliberately

- **The person still sees the raw English error.** Localising model failures (the agent's system-message table) was offered and not chosen for this change. Open, P2.
- **Only the agent model is checked.** The token plan's quota looked shared across models on 2026-09-11. If a quota were ever per model, a used-up classifier quota shows only through real calls (the router already falls back to keywords).
- **`MODEL_REQUEST_INVALID` counts as transient.** A malformed request is our bug, not the model being unreachable; three in a row still mark it degraded.
- **No alert.** Health is now truthful, but nothing pages anybody when it turns `degraded`.

## Regression fences

| Test | Fence |
|---|---|
| `TestQwenAccountFailuresAreNotRateLimitsOrBadKeys` (`internal/llm/qwen_account_errors_test.go`) | the three account failures by code, the token-plan message verbatim, and that real rate limits, a bad key and a bad request keep their meaning |
| `TestQuotaExhaustionIsTriedOnceAndSaysSo` | one request, no retry promise, "will not help", the vendor's reset time kept |
| `TestRateLimitIsRetriedAndTheFinalErrorDoesNotPromiseMore` | a real rate limit is retried (3 requests) and the final error does not say it will be retried |
| `TestHealthFollowsRealCalls`, `TestHealthReportsTheFailureCode`, `TestHealthIgnoresCallsEndedByTheirOwnContext` (`internal/llm/health_test.go`) | the state rules above |
| `TestHealthChecksOnlyWhenStaleAndAtMostOncePerWindow`, `TestHealthWithoutACheckNeverCallsTheModel`, `TestHealthCountsACheckThatTimesOut`, `TestModelCheckIsOneTokenAgainstTheAgentModel` | the check: only when stale, once per window, never blocking, a timeout is a failure, one token against the agent model |
| `TestModelHealthChecksOnlyARealBackend` (`cmd/obagent/model_health_test.go`) | no check on the scripted backend |
| `TestHealthSaysDegradedWhenTheModelQuotaIsUsedUp` (`internal/httpapi/model_health_test.go`) | end to end: unknown before, a real turn against a vendor answering the token-plan 429, then `degraded` with `MODEL_QUOTA_EXHAUSTED`, still HTTP 200, no vendor text |
| `TestHealthWithoutAWatchedModelIsUnknownNotOK` | no watcher is `unknown`, not `ok` |

| `TestHealthDoesNotRepeatAnUnrecordedCheckWithinTheWindow` | a check that leaves nothing on the record still cannot be repeated inside the window |

**Reproduction (before the fix):** the five account-failure cases and both retry tests failed exactly as observed in production. The real-rate-limit, bad-key and bad-request cases passed on the old code too, so they guard against over-correcting.

**Mutation drills (2026-09-11, on a copy, `-count=1`, each mutation checked to have landed and to compile): 15 of 15 red.**

| Broken on purpose | Caught by |
|---|---|
| vendor codes ignored | `TestQwenAccountFailuresAreNotRateLimitsOrBadKeys` |
| no quota-message fallback | same |
| fallback reads the code (so `Throttling.RateQuota` becomes a quota) | same |
| `MODEL_QUOTA_EXHAUSTED` made retryable | `TestQuotaExhaustionIsTriedOnceAndSaysSo` |
| rate-limit text promises a retry again | `TestRateLimitIsRetriedAndTheFinalErrorDoesNotPromiseMore` |
| `status` back to the literal `"ok"` | `TestHealthSaysDegradedWhenTheModelQuotaIsUsedUp` |
| `degraded` answers HTTP 503 | same |
| vendor message stored instead of the code | same |
| one rate limit counts as an outage | `TestHealthFollowsRealCalls` |
| a used-up quota not degraded at once | same |
| cancelled calls counted | `TestHealthIgnoresCallsEndedByTheirOwnContext` |
| the check window removed | `TestHealthDoesNotRepeatAnUnrecordedCheckWithinTheWindow` (this test was added when the drill first showed nothing else caught it) |
| the check run inline, blocking health | `TestHealthChecksOnlyWhenStaleAndAtMostOncePerWindow` |
| scripted backend given a check | `TestModelHealthChecksOnlyARealBackend` |
| the check asks for 1000 tokens | `TestModelCheckIsOneTokenAgainstTheAgentModel` |

**Not fenced by a unit test:** the one line in `cmd/obagent/main.go` that makes the wrapped client the one everything uses (`client = health`) and hands it to the server. A test harness assembles the server itself, so it cannot see production's wiring. It is verified after deploy by reading `/api/health` on jobs.heros-agent.space: `model.check_enabled` must be `true`, and within one probe interval `status` must leave `unknown`.

```
GOWORK=off go test ./internal/llm/ ./internal/httpapi/ ./cmd/obagent/ -count=1
```
