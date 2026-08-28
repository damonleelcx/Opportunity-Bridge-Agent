# The stream announced every tool call twice

**Found:** 2026-08-28, while verifying an unrelated fix — a live DeepSeek turn
carried 14 `tool_start` events against 7 `tool_result` events.
**Area:** `internal/agent/agent.go`, the SSE event contract, and the trace
disclosure in `web/static/app.js`.
**Status:** fixed.

## A correction to how this was first reported

The original note claimed the duplicate doubled the trace disclosure's step
counter — that a 7-call turn displayed as "13 步". **That was wrong**, and it is
worth recording why, because it would have sent the fix in the wrong direction.

The client already discriminated the two events by accident:

```js
case "tool_start":
  status(t("status.working"), "busy");
  if (ev.args) techItem(turn, ...);   // <- the streamed one has no args
```

`techCount` only increments inside `techItem`, so the argument-less event
rendered nothing and counted nothing. "N 步" counts a `→` row for each call, a
`←` row for each return, and one row per guardrail finding; it was never
one-per-tool, and it was not inflated by this defect.

So the visible symptom was ~nothing. What was real is the wire contract, and one
case where the event was untrue — below.

## Why there were two

**`agent.go:266`, in the model's stream callback.** Fired on `llm.EventToolUse`,
the moment the model announced a tool. Carries a name and nothing else:
`llm.Event` has no `ToolInput` and no tool-use id.

**`agent.go:373`, at the call site.** Fired immediately before
`a.Tools.Call`, carrying the arguments the trace panel renders.

Neither knew about the other, and no client could reconcile them: with no
tool-use id on the streamed event there is nothing to pair on. A consumer that
counted `tool_start` got double; one that counted only args-bearing events — as
ours did, by luck rather than design — got the right number.

## The part that was actually wrong, not just redundant

The streamed emission fires **before the budget check and before the refusal
check**. So:

- a tool that trips `budget.CheckTool` and is never invoked still emitted
  `tool_start`;
- a response that comes back `refusal` and `break`s out of the loop still
  emitted `tool_start` for tools that never ran.

An event named `tool_start` for a call that never started is not a duplicate.
It is a false statement, and it is the reason this was worth fixing at all.

## The decision

Three options were on the table.

| Option | Why not / why |
|---|---|
| Keep the streamed one | It has no arguments, so the trace panel loses everything it displays. |
| Dedupe by tool-use id | `llm.Event` carries `Kind`, `Text` and `ToolName` — no id. Threading one through the model boundary, the agent event and the client is a change to the LLM contract to fix a cosmetic duplicate. Ruled out on cost. |
| **Keep the call-site one** ✅ | It is the only one that knows the arguments, and the only one that fires when the call actually happens. |

What is given up: the "正在查询…" status flips at the call site rather than when
the model announces the tool. Between those two points there is no I/O other
than the remainder of the same response stream — for the last tool in a
response, effectively nothing.

`llm.EventToolUse` itself was left in place. It is a legitimate signal at the
model boundary; the agent simply no longer republishes it, and the `case` was
replaced with a comment saying why, so it is not "restored" by the next reader.

## One consequence that had to be handled

With the argument-less producer gone, `if (ev.args)` in the client changes
meaning. It stopped being "skip the duplicate" and would have become "silently
drop any tool invoked with no arguments" — `consent_check` takes none. The trace
row is now rendered unconditionally, with `{}` shown when there is nothing to
show. A call with no arguments still happened.

## Verification

Live DeepSeek turn, same shape as the one that surfaced this:

```
before:  tool_start = 14   tool_result = 7
after:   tool_start = 5    tool_result = 5    argless: none
```

## Regression tests

`internal/agent/agent_test.go`:

| Test | Fence |
| --- | --- |
| `TestOneToolStartPerToolResult` | one announcement per call, and every one carries arguments |
| `TestBudgetBlockedToolAnnouncesNoStart` | a call the budget refused is not reported as started |

The scripted backend emits `llm.EventToolUse` (`scripted.go:114`), so these
observe the real duplication rather than a simulation of it. Both
mutation-drilled: restoring the streamed emission turns both red, at 6 events
against 3 — the same 2:1 ratio measured live.

```
GOWORK=off go test ./internal/agent/ -count=1
```
