# 12. DeepSeek

The model boundary (`internal/llm.Client`) exists so a second provider is a new
implementation rather than a fork. DeepSeek is the second implementation.

```bash
cp .env.example .env && chmod 600 .env    # set DEEPSEEK_API_KEY and OBA_BACKEND
make run-deepseek
```

Or without a file:

```bash
DEEPSEEK_API_KEY=sk-... OBA_BACKEND=deepseek make run
```

An exported variable always wins over `.env` — see
[11-operations.md](11-operations.md).

Nothing else changes. Model ids follow the backend automatically, and the whole
agent — routing, tools, guardrails, verifiers, budgets, the approval gate — is
untouched, because none of it knows which provider answered.

## What maps onto what

| Ours | DeepSeek | Note |
|---|---|---|
| agent model | `deepseek-v4-pro` | default when `OBA_BACKEND=deepseek` |
| routing model | `deepseek-v4-flash` | default |
| `Request.Thinking` | `thinking: {type: "enabled"｜"disabled"}` | |
| `Request.Effort` | `thinking.reasoning_effort` | five levels onto three — see below |
| streamed `reasoning_content` | → `EventThinkingDelta` | the interface shows it working |
| `usage.prompt_cache_hit_tokens` | → `Usage.CacheReadTokens` | |
| `finish_reason` | → our stop reasons | `tool_calls`→`tool_use`, `content_filter`→`refusal` |

**Effort** is a table, not a heuristic, because silently collapsing `max` to
`high` would change what a deployment paid for with nothing to grep for:

```
low → low      medium → low      high → high      xhigh → high      max → max
```

## Two shape differences that fail silently

Both have their own test in `internal/llm/deepseek_test.go`, because neither
produces an error — they produce an agent that quietly works less well.

**Tool results.** Our block model carries them inside one user message, which is
what the Anthropic API wants. This API wants each result as its own message with
`role: "tool"`. Get it wrong and the model simply stops seeing tool output.

**Fragmented tool arguments.** Arguments arrive in pieces across chunks, keyed by
index, and must be reassembled before parsing. A half-parsed argument object
looks to the loop like the model choosing not to act.

Also deliberate: `reasoning_content` is **not** replayed as input. It is an
output field here; feeding it back is only defined for the beta
prefix-completion mode, which this application does not use.

## Prompt caching

DeepSeek has no `cache_control` directive — its context caching is automatic and
keyed on the prefix — so the `Cache` flags on the system layers are not sent.
The **layer order still earns its keep**: stable charter and persona first,
volatile per-turn context last is exactly what an automatic prefix cache
rewards. `usage.prompt_cache_hit_tokens` is the way to check it is working, and
it is surfaced in the trace.

## The other route, and why this is not it

DeepSeek also publishes an Anthropic-compatible endpoint at
`https://api.deepseek.com/anthropic`, and it does work with the existing client:

```bash
OBA_BACKEND=anthropic \
ANTHROPIC_BASE_URL=https://api.deepseek.com/anthropic \
ANTHROPIC_API_KEY=$DEEPSEEK_API_KEY \
OBA_AGENT_MODEL=deepseek-v4-pro make run
```

It is a legitimate option, and it is one line. It is not the default here
because its documented gaps land on things this application relies on: **cache
control is not supported at all** there, and only part of `output_config` is.
Its model mapping is also a silent fallback — an unrecognised model id becomes
`deepseek-v4-flash` rather than an error — which is exactly the failure the
startup check below exists to prevent.

## The startup check

Switching provider without changing the model ids is the obvious mistake, and it
fails in the worst possible way: a compatibility layer maps the unrecognised id
onto its own default, so the process keeps running while answering from a model
nobody chose.

```
OBA_AGENT_MODEL="claude-opus-5" is a anthropic model, but OBA_BACKEND=deepseek.
Either set OBA_BACKEND=anthropic, or set OBA_AGENT_MODEL to one of:
deepseek-v4-pro, deepseek-v4-flash, deepseek-v4-flash-vision-exp
```

An id of the *right* family that this build has not seen — a proxy, or a model
released later — is allowed and logged, not blocked. A missing
`DEEPSEEK_API_KEY` is refused at startup, because unlike the Claude SDK there is
no other credential source, so the failure is certain rather than possible.

## Errors worth their own branch

`402 Insufficient Balance` is translated on its own, and is not retried. It
otherwise reads as a generic outage and costs somebody an hour.
