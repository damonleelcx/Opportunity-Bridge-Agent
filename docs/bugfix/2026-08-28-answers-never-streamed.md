# The answer never streamed, and never had

**Reported:** 2026-08-28, from a production screenshot — "agent reply is not
streaming".
**Area:** `internal/llm/retry.go`, `internal/agent/agent.go`, `web/static/app.js`.
**Status:** fixed, and measured before and after.

## What the person saw

Nothing, for the better part of a minute, and then the whole answer at once.
With a reasoning model in front, that is a blank screen for as long as the model
thinks — which reads as a product that has hung, not one that is working.

## What was actually happening

Timed on a real turn, with a timestamp on every server-sent event:

| event | first | last |
| --- | --- | --- |
| `routed` | 0.71s | 0.71s |
| `thinking` | 5.80s | — |
| `text` | **57.17s** | **57.17s** |
| `final` | 57.17s | 57.17s |

All 451 text deltas arrived in the same instant, together with `final` and
`close`. Not slow: **not streaming at all**.

It was not the ingress and not the browser. `web/static/app.js` writes the bubble
on every text event, which is correct, and traefik carries no compression
middleware that would buffer. The deltas were being held server-side.

## Root cause

`internal/llm/retry.go` collected every event of an attempt into a slice and
replayed it into the real sink only once the attempt had **succeeded**:

```go
var buffered []Event
resp, err := r.Inner.Stream(ctx, req, func(e Event) { buffered = append(buffered, e) })
if err == nil {
    for _, e := range buffered { sink(e) }
}
```

The rule it was enforcing is right, and its comment said so: *deltas from a
failed attempt must not reach the interface, or the user sees half an answer
followed by a different whole one.* The cure removed streaming from the product.
Every model call is wrapped in `Retrying` ([agent.go](../../internal/agent/agent.go)),
so nothing the model wrote ever reached the reader until the model had stopped
writing.

**Who this belonged to:** the change that introduced `Retrying` — it is in the
initial commit `2e93e63` and has never been touched since. This has therefore
been true for the whole life of the product; nothing regressed. It went unnoticed
because "the model is slow" and "the model is not streaming" look identical from
the outside, and the code that streams — the provider clients and the interface —
is correct on both sides of the thing that broke it.

## The fix

Deltas go **straight through**. The withholding is now charged only to the path
that needs it: an attempt that fails **having already written something** emits
`llm.EventReset`, and the interface drops what it has before the next attempt
starts. Same guarantee, paid for on the rare path instead of every path.

The reset reaches the browser as an optional field on the existing text event
(`{"kind":"text","reset":true}`) rather than as a new event kind, so a client
that does not know about it appends an empty string instead of ignoring an
unknown event — and `final` still corrects the display either way. On reset the
bubble goes back to the typing indicator rather than to empty space, for the
reason the indicator exists at all: an empty grey box reads as a broken answer.

The take-back also fires when **no retry follows**. A non-retryable failure ends
the turn with an error, and half an answer above that error is worse than none.

## Measured after

Same question, same model, two searches:

| event | first | last | count |
| --- | --- | --- | --- |
| `thinking` | 2.57s | 50.45s | 2321 |
| `text` | **50.45s** | **54.55s** | 239 |
| `final` | 54.60s | 54.60s | 1 |

Thinking is visible from 2.6 seconds in, the answer writes itself over four
seconds, and `final` lands after the text rather than with it.

## Regression tests

`internal/llm/anthropic_test.go`, `web/interface_test.go`. Both drilled by
`make drill`.

| Test | Fence |
| --- | --- |
| `TestRetryingStreamsDeltasAsTheyArrive` | a delta reaches the sink *while* the attempt is running |
| `TestRetryingLeavesTheReaderOnlyTheSuccessfulAttempt` | applying the events in order leaves only the successful attempt |
| `TestPartialOutputIsTakenBackEvenWhenNoRetryFollows` | a non-retryable failure clears the screen too |
| `TestClientClearsTheScreenOnReset` | the interface acts on `reset` and restores the typing indicator |

The first of these is the fence for the defect itself. The second replaced
`TestRetryingHidesPartialOutputFromTheUser`, which asserted the **mechanism**
("the sink never sees the failed attempt") rather than the **guarantee**. It is
now written as the reader experiences it — accumulate text, clear on reset — so
it holds under either implementation and would have survived this change without
needing to be edited, which is what a fence should do.
