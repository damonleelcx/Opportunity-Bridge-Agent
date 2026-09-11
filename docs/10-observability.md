# 10. Observability

Every decision writes one event: the routing call and how it was reached, each
model request with its shape, each tool call with its arguments and outcome,
every guardrail and verifier finding, each retry, each consent check, each
approval, and the stop reason.

Event names are a closed set in `internal/obs/obs.go`, in the form
`agent.<area>.<state>`. Codes are `UPPER_SNAKE_CASE`. Literals at call sites are
not used, because a literal is a name nobody can grep for later.

```
agent.run.started        agent.route.resolved     agent.route.rejected
agent.model.requested    agent.model.responded    agent.model.retried
agent.tool.requested     agent.tool.succeeded     agent.tool.failed
agent.guardrail.tripped  agent.verify.failed      agent.verify.passed
agent.approval.required  agent.approval.granted   agent.consent.checked
agent.retrieval.queried  agent.budget.exceeded    agent.escalation.raised
agent.state.written      agent.run.finished       agent.run.failed
```

## One view, two audiences

The events stream to the interface as they happen and render in the **Trace**
panel. That is deliberate: the operator debugging a bad answer and the person
asking *"why did it say that?"* are looking at exactly the same record.

The trace also carries `backend`, so a replayed scripted conversation can never
be mistaken for a live one.

`TestEveryDecisionIsTraced` fails if the run, the route, the model call, the tool
call and the finish are not all recorded — a trace with a hole in it is worse
than no trace, because it invites a confident wrong conclusion.

## The server log

The trace panel is one audience. The operator reading `kubectl logs` is the
other, and until 2026-09-11 they got nothing: a run's events went **only** to the
browser tab that ran the turn — gone on reload — so the log held one
`http request` line per turn and could not say which tools had run. See
[the bugfix note](bugfix/2026-09-11-agent-events-never-reached-the-logs.md).

`obs.LogSink` now writes a run's events to the process log **as they happen**, so
a turn that hangs or is killed still shows the tool it was in. What reaches the
log is a table, `loggedEvents` in `internal/obs/logsink.go`, and nothing else:

| Logged | Fields that may appear |
|---|---|
| `agent.run.started` / `finished` / `failed` | role, backend, message_chars · stop_reason, iterations, tool_calls, redrafted, output_tokens, elapsed_ms, **cards_kept** |
| `agent.tool.requested` / `succeeded` / `failed` / `rejected` | tool, **args_hash**, result_bytes |
| `agent.budget.exceeded`, `agent.model.retried`, `agent.route.rejected` | iterations, tool_calls, tool · attempt · intent |
| `agent.approval.*` | tool, approval_id |
| `agent.candidate.searched`, `agent.outreach.requested` / `decided`, `agent.talent.external_scanned` | counts · **outreach_id**, status · vendor counts |

Every line also carries `event.name`, `run_id`, `session_id`, `intent`, `step`,
and `error.code` when there is one **shaped like a code** (`UPPER_SNAKE`).

**Never logged** (拍板 2026-09-11): an event's message — on several events it is
the person's words, a route rationale, a verifier quoting the answer, or an
upstream error body; tool arguments (`args_hash` stands in); and `candidate_ref`
(`outreach_id` joins to it in the store). High-volume events with no bearing on
"what ran" — model requests and responses, verifier and guardrail passes — stay
in the trace panel only.

**Every exit says the turn is over.** Each run ends in exactly one
`agent.run.finished` or `agent.run.failed`, including the paths that used to end
silently: a rollout-disabled intent, a routing failure, a model failure.
`cards_kept` names the tools whose results were kept for replay — so "a tool ran
but its card was not kept" is answerable from one line.

**Joining a request to its run.** Every request is given an id, minted by the
server and never taken from the client (an inbound value would be written into
every line). It is returned as `X-Request-Id` and added to every line logged
with that request's context. A message turn's `http request` line also carries
the `run_id` it drove:

```
kubectl -n opportunity-bridge logs deploy/opportunity-bridge | grep run_1789096804305058063
```

## Reading a turn

The `final` event carries the whole run: route decision and method, answer, stop
reason, every finding, every tool call with its arguments and declared `Meta`,
approvals raised, token usage, iteration count, whether it was redrafted, and
elapsed time. `GET /api/sessions/{id}` returns the durable side — profile,
tasks, consent, approvals.
