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

## Reading a turn

The `final` event carries the whole run: route decision and method, answer, stop
reason, every finding, every tool call with its arguments and declared `Meta`,
approvals raised, token usage, iteration count, whether it was redrafted, and
elapsed time. `GET /api/sessions/{id}` returns the durable side — profile,
tasks, consent, approvals.

Set `OBA_TRANSCRIPT_LOG` to mirror events as JSON lines for shipping elsewhere.
