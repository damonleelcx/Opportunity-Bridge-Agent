# 9. Evaluation, reliability, end-to-end tests

## What the suite is

27 cases in `evals/*.jsonl` — 19 turn cases and 8 routing cases — run through
**the real agent path** — the real
router, tool registry, permission checks, guardrails, verifiers and budgets. Only
the model is substituted, by a scripted client.

```bash
make eval          # report to the terminal, non-zero exit on any failure
make eval-live     # routing cases measured against the real classifier model
make eval-report   # full machine-readable report
go test ./...      # the same suite, as an ordinary Go test
```

## Why scripted turns

Because the cases that matter cannot be provoked on demand from a live model: an
invented programme id, an eligibility verdict, a task closed on nothing, a cohort
tag used to discourage somebody, a stuck loop. Scripting the model turn pins the
input precisely; everything downstream of it is the real thing.

The scripted backend is not a product mode. The server refuses to start on it
without a script path, and every trace records the backend name, so a replayed
conversation cannot be mistaken for a live one.

## Categories

A suite of happy paths measures nothing that matters here, so the report breaks
down by category and `TestShippedDatasets` fails if any category is empty.

| Category | Count | Examples |
|---|---|---|
| success | 10 | search → criteria → cited answer with a channel; caseworker orchestration with dependencies; insight with coverage stated; the approval gate releasing; a full answer in Chinese clearing every verifier |
| edge | 5 | empty search stays honest; a bad tool call recovers in one round trip; care duties routed as an access problem |
| adversarial | 12 | invented id blocked; verdict repaired; cohort downranking blocked; consent missing; silent closure refused; suppression undisclosed; approval declined; escalation outranks the topic; stuck loop stopped; **false reassurance repaired**; **an English answer to a Chinese session repaired**; a resident pinning themselves into the analyst intent |

## What is asserted

Outcomes the product cares about, not wording:

```json
"expect": {
  "intent": "individual_pathway",
  "stop_reason": "answered",
  "tools_called": ["opportunity_search", "criteria_explain"],
  "tools_not_called": ["application_submit"],
  "findings_include": ["ELIGIBILITY_VERDICT"],
  "findings_exclude": ["INVENTED_IDENTIFIER"],
  "answer_contains": ["trn-002"],
  "redrafted": true,
  "approval_raised": false
}
```

Asserting on phrasing makes a suite that fails on every improvement.

## What is measured

Pass rate overall, per category and per intent; **tool-call accuracy** (were the
tools that should have been called actually called); median and p95 latency. The
report leads with what failed, because a report that leads with a pass rate
invites nobody to scroll.

## Isolation

Each case builds its own memory-only store. State surviving between cases is the
classic way a suite starts passing for the wrong reason.

## The other tests

| File | Holds |
|---|---|
| `internal/intent/registry_test.go` | every allowed tool and named verifier exists; role boundaries; the unrouted state can call nothing |
| `internal/llm/anthropic_test.go` | the real SDK against a server speaking the real wire format: cache breakpoints, effort, adaptive thinking, `additionalProperties`, error translation, retry hiding partial output |
| `internal/tools/tools_test.go` | validation messages, argument hashing, per-role consent, k-anonymity suppression, no verdict from `criteria_explain`, no closure without evidence |
| `internal/guardrail/guardrail_test.go` | each guard, both directions, with its own fixture |
| `internal/agent/agent_test.go` | the approval gate both ways, allowlist refusals, budgets, trace completeness, memory not replaying tool calls, rollout gate |
| `internal/httpapi/server_test.go` | SSE event kinds reach the interface; errors carry a code and a remedy; consent and deletion work without the agent |
