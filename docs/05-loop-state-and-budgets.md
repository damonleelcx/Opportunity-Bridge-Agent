# 5. The loop, memory, and when to stop

## The loop

`internal/agent/agent.go`, one turn:

```
input guards (escalation)  ── an escalation outranks whatever the message was about
        ↓
route (role → explicit → only-option → model → keyword fallback)
        ↓
rollout gate ── a disabled intent refuses visibly, with the reason
        ↓
┌───────────────────────────────────────────────┐
│ budget check                                  │
│ assemble 3-layer prompt                       │
│ stream from the model                         │
│ tool calls? → validate → permission → consent │
│               → approval → run → results back │
│ no tool calls? → verify                       │
│   findings? → one redraft with the remedies   │
└───────────────────────────────────────────────┘
        ↓
persist turn, findings, tasks · emit final + trace
```

Two details worth naming:

**The failed draft is dropped from history before a redraft.** Carrying it
forward anchors the model on the text it has just been told is wrong.

**One redraft, not more.** A second attempt nearly always produces the same
failure in different words, and the person is still waiting. After one, the
guard's own message is delivered instead — it says what rule was broken and that
nothing was done.

## Three kinds of memory, kept apart

Collapsing them is the usual cause of an agent that either forgets what it just
did or remembers something it was never told.

| Kind | What | Lifetime |
|---|---|---|
| Short-term task state | slots, findings, step count | cleared when the objective changes |
| Conversation history | the turns | trimmed by budget, never mutated |
| Long-term memory | profile, case tasks, consent, demand signals | consent-scoped, survives the session |

Persistence is a JSON snapshot, and it is an **enhancement, never a dependency**:
a failed load starts empty with a warning, a failed write logs and continues.
Forgetting last week's session is not a reason to refuse today's question.

The person can see everything held about them in the interface, and delete it
(`DELETE /api/sessions/{id}/profile`). A product that offers inspection without
deletion has offered nothing.

## Stopping conditions

Per-intent caps are clamped by process-wide ones, never the other way round, so
no intent can widen its own limits.

| Condition | Default | Why |
|---|---|---|
| iterations | 8 | |
| tool calls | 16 | |
| identical tool call | 2 | the specific shape a stuck loop takes |
| wall clock | 180s | somebody is waiting, possibly at a counter |
| output tokens | 120k | |
| model retries | 2, on transient failures only | a 400 or a 401 retried just makes the user wait for the same error |

Each stop reason has a message a person can act on, not a code:

> I was repeating the same lookup without getting anywhere, so I stopped.
> Something in the question and what I can search do not line up — tell me the
> city and what you are trying to do, or ask for a person.

`TestBudgetStopsARunawayLoopWithSomethingReadable` and the eval case
`turn-repeated-tool-call-stops` hold that.
