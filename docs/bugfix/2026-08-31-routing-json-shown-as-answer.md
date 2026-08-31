# The reader was shown the router's JSON as the answer

**Reported:** 2026-08-31, screenshot of a conversation whose answer bubble
contained `{"intent": "individual_pathway", "confidence": 0.92, "rationale":
"Same person, same objective; they are asking to have the step tracked."}`
followed by an apology for it.
**Area:** the verify stage (`internal/guardrail/verify.go`), the unresolved
path in the agent loop (`internal/agent/agent.go`), and the demo double
(`internal/llm/scripted.go`, `demo/scripted-turns.json`).
**Status:** fixed. The demo-script fragility is now loud rather than silent;
see "What is still true" at the end.

## What it looked like

One answer bubble, containing in order:

1. the router's decision object, verbatim;
2. four self-check failures, all of them true and none of them the problem —
   "names no record", "no link, no phone number, no service window", "was to be
   written in Chinese, but it is mostly not", "never names 成都";
3. "这是我这次能写出的最好版本".

So the product said its best effort was a JSON blob. On a service whose whole
premise is that it will not tell people comfortable untruths, that is the worst
possible thing to be wrong about.

## Why

Two causes, and both had to hold.

### 1. A legitimate branch shifted the demo script by one turn

`ScriptedClient` replays turns **positionally**, and the *router* and the
*agent* draw from the same cursor. `demo/scripted-turns.json` was written for
the path where nothing goes wrong:

| # | consumer | content |
|---|----------|---------|
| 1 | router   | `{"intent": ...}` |
| 2 | agent    | thinking + `opportunity_search` |
| 3 | agent    | `criteria_explain` |
| 4 | agent    | the real answer |
| 5 | router   | `{"intent": ...}` for the second message |

A redraft is not "something going wrong" — it is the loop working. When a
verifier returned `Repair` on turn 4, the loop dropped the failed draft and
asked the model again. That request took turn **5**, which is the router's
object. Every later turn was off by one for the rest of the session.

Reproduced exactly, by removing the fence below:

```
{"intent": "individual_pathway", "confidence": 0.92, "rationale": "..."}

(I checked this answer against my own rules and it still does not pass: The
answer gives no way to act: no link, no phone number, no service window, and no
task created.. It is the best I produced — ask me to say it again, or call
12333 and ask for a person.)
```

Note what the happy path proves: replaying the script straight through passes
both turns. Reading the script alone would have exonerated it.

### 2. Nothing refused to publish a machine object

This is the cause that matters, because cause 1 is specific to the test double
and cause 2 is not. Every verifier in the registry checks what an answer
*contains*. None checked what an answer *is*. So the JSON was measured against
"does it name a record", "does it give a phone number", "is it in Chinese" —
questions that only make sense once you have prose — it failed four of them as
`Repair`, and the unresolved path then delivered it anyway, because a
still-failing draft "is usually most of an answer".

For a machine object it is not most of an answer. It is not an answer.

## The fix

**A universal verifier, `verifyAnswerIsProse`, at `Block` severity.**

- `ANSWER_IS_MACHINE_OUTPUT` — the whole trimmed answer parses as one JSON
  object or array.
- `ROUTING_OBJECT_LEAKED` — the answer carries `"intent"` and `"confidence"` as
  adjacent JSON keys, which is this service's own routing decision and belongs
  to the route chip in the interface, never to the reader.

Three deliberate choices:

- **Block, not Repair.** A `Repair` that fails twice is delivered with a note
  attached. "The JSON, plus an apology" is not an improvement on the JSON.
  `Block` still allows one redraft first (the loop redrafts on `Block` too), so
  a model that slips once gets its second chance and a model that slips twice
  produces a refusal a person can act on.
- **Universal, not named per intent.** It lives in `universalVerifiers`, not in
  the `verifiers` map, so no intent can omit it and no new intent can forget it.
  Nothing about "an answer is written for a human" is specific to jobs or to
  populations.
- **Detection is narrow on purpose.** Whole-body JSON only, plus the two-key
  routing shape. An answer that quotes JSON inside a fenced block, or that uses
  the word "intent" in a sentence, is prose and stays prose — covered by
  `TestProseIsNotMistakenForMachineOutput`.

**The demo script's router turns are pinned.** Both now carry
`"when_contains": "Role of the person writing:"`, which appears only in a
classifier request. If any agent-side branch shifts the cursor again, the
script fails loudly with `SCRIPT_MISMATCH` instead of quietly handing the
reader an internal object.

**Reader-facing wording** for both codes was added to `blockReasons` in
`internal/agent/messages.go`, so the refusal is in the person's language rather
than falling back to the English finding text.

## How it was verified

Mutation drill — emptying `universalVerifiers` reproduces the reported output
byte for byte, and restoring it refuses:

```bash
GOWORK=off go test ./internal/guardrail ./internal/agent \
  -run 'TestRoutingObject|TestProseIsNot' -count=1
```

Regression tests (`-count=1` matters; a cached PASS proves nothing):

| test | file | holds |
|------|------|-------|
| `TestRoutingObjectIsNeverDeliveredAsAnAnswer` | `internal/guardrail/guardrail_test.go` | fires with **no** verifier names passed, so an intent listing nothing is still covered; both findings are `Block` |
| `TestProseIsNotMistakenForMachineOutput` | `internal/guardrail/guardrail_test.go` | real answers, and prose using the words "intent"/"confidence", are not flagged |
| `TestRoutingObjectNeverReachesTheReader` | `internal/agent/agent_test.go` | end to end: draft **and** redraft both return the object; the reader gets a refusal, `StopRefused`, and a finding saying why |

Full suite green: `GOWORK=off go test ./... -count=1`.

## What is still true

The scripted double remains positional, and the router still shares its cursor
with the agent. That is now *loud* rather than silent, which is the property
that mattered, but a demo run that takes an unscripted branch still ends in a
script error rather than a graceful answer. Giving the router its own lane would
mean an explicit marker on `llm.Request`; it was not done here because the
product-side fence removes the harm, and a new field on a shared request type is
a larger commitment than this bug justifies. If the demo grows more branches,
that is the change to make.
