# How this was built

The brief was a list of twenty steps for building an agent as a system rather
than as "an LLM plus a prompt". This page is the map from each step to the place
in this repository where it actually lives, so a reviewer can check the claim
rather than take it.

| # | Step | Where it lives |
|---|---|---|
| 1 | Define the goal | [01-goal-and-boundaries.md](01-goal-and-boundaries.md) · `internal/intent/intent.go` (`Goal`, `SuccessCriteria` per intent) |
| 2 | Define boundaries | [01-goal-and-boundaries.md](01-goal-and-boundaries.md) · `CanDo` / `CannotDo` / `EscalateWhen` per intent · `internal/prompt/prompt.go` (`Charter`) |
| 3 | Design the workflow | [02-intents.md](02-intents.md) · `Workflow` (understand → plan → act → verify → respond) per intent |
| 4 | Choose the model | [03-model-and-prompt.md](03-model-and-prompt.md) · `internal/config/config.go` |
| 5 | Design the system prompt | [03-model-and-prompt.md](03-model-and-prompt.md) · `internal/prompt/prompt.go` |
| 6 | Build context engineering | [03-model-and-prompt.md](03-model-and-prompt.md) · `prompt.ContextLayer` |
| 7 | Add tools | [04-tools-and-validation.md](04-tools-and-validation.md) · `internal/tools/builtin.go` |
| 8 | Build the agent loop | [05-loop-state-and-budgets.md](05-loop-state-and-budgets.md) · `internal/agent/agent.go` |
| 9 | Manage state and memory | [05-loop-state-and-budgets.md](05-loop-state-and-budgets.md) · `internal/store/store.go` |
| 10 | Add retrieval | [06-retrieval.md](06-retrieval.md) · `internal/retrieval`, `internal/corpus` |
| 11 | Validate every action | [04-tools-and-validation.md](04-tools-and-validation.md) · `internal/tools/schema.go`, `tools.Registry.Call` |
| 12 | Add guardrails | [07-guardrails-and-verifiers.md](07-guardrails-and-verifiers.md) · `internal/guardrail` |
| 13 | Set stopping conditions | [05-loop-state-and-budgets.md](05-loop-state-and-budgets.md) · `internal/agent/budget.go` |
| 14 | Build evaluation datasets | [09-evaluation-and-reliability.md](09-evaluation-and-reliability.md) · `evals/*.jsonl` |
| 15 | Measure reliability | [09-evaluation-and-reliability.md](09-evaluation-and-reliability.md) · `internal/eval` |
| 16 | Add observability | [10-observability.md](10-observability.md) · `internal/obs` |
| 17 | Human approval for high-risk actions | [08-approval.md](08-approval.md) · `tools.RiskIrreversible`, `store.PendingApproval` |
| 18 | Test end to end | [09-evaluation-and-reliability.md](09-evaluation-and-reliability.md) · `internal/eval/eval_test.go`, `internal/httpapi/server_test.go` |
| 19 | Deploy gradually | [11-operations.md](11-operations.md) · `OBA_ENABLED_INTENTS` |
| 20 | Continuously improve | [11-operations.md](11-operations.md) |

Two pages sit outside the twenty steps:

| | |
|---|---|
| [12-deepseek.md](12-deepseek.md) | The second model provider, and what changes at the boundary |
| [13-name-and-voice.md](13-name-and-voice.md) | 阿桥 — the name, the persona, the guard that keeps it honest, the avatar |
| [17-read-aloud.md](17-read-aloud.md) | Reading answers aloud: the browser voice that always works, the optional vendor voice, and what the free tier costs in privacy |
| [16-live-lookup.md](16-live-lookup.md) | Looking things up outside the corpus: the provider seam, the verified directory, and the key-gated live search |
| [15-deployment.md](15-deployment.md) | Live on k3s at jobs.heros-agent.space — the shape, four decisions, and what is not protected |
| [14-interface.md](14-interface.md) | The landing page at `/` and the conversational UI at `/app`: what came from the mockup, what had to change, and where the machinery went |
| [18-recruiter-and-outreach.md](18-recruiter-and-outreach.md) | The fifth audience: employers searching an opt-in pool, why nobody is in it by default, and the handshake that has to happen before a name moves |

## Reading order

If you are reviewing this for the first time, read
[01-goal-and-boundaries.md](01-goal-and-boundaries.md) and
[02-intents.md](02-intents.md), then open `internal/intent/intent.go`. That one
file is the spine: routing, permissions, prompt assembly, the interface's intent
chips, the evaluation suite and these docs all read from it.
