# 12. Qwen

The model boundary (`internal/llm.Client`) exists so a provider is an
implementation rather than a fork. **Qwen** — served by Alibaba Cloud Model
Studio (DashScope) — is the implementation this build ships, and the only live
one. `scripted` is a fixture, not a provider.

```bash
cp .env.example .env && chmod 600 .env    # set QWEN_API_KEY
make run
```

Or without a file:

```bash
QWEN_API_KEY=sk-... make run
```

An exported variable always wins over `.env` — see
[11-operations.md](11-operations.md).

Nothing else in the agent knows which provider answered: routing, tools,
guardrails, verifiers, budgets and the approval gate are all untouched by this
file.

## ‼️ The key is regional

Model Studio has two hosts, and they do **not** share an account namespace:

| Region | Base URL |
|---|---|
| Beijing (default) | `https://dashscope.aliyuncs.com/compatible-mode/v1` |
| Singapore | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` |

A key issued in one region is rejected by the other with a **401 that is
indistinguishable from a revoked key** — verified against the live service on
2026-09-03, where a working Beijing key returned `invalid_api_key` on the
Singapore host.

So region is part of the *credential*, not a latency preference:
`OBA_QWEN_BASE_URL` and `QWEN_API_KEY` move together or not at all. The
`MODEL_AUTH_FAILED` message says so, because without the hint the obvious next
move is to reissue a key that already works.

## What maps onto what

| Ours | Qwen | Note |
|---|---|---|
| agent model | `qwen3.8-max` | default |
| routing model | `qwen3.8-flash` | default; ~1/14 the rate on both axes |
| `Request.Thinking` | `enable_thinking: true｜false` | a plain bool, always sent — see below |
| `Request.Effort` | `thinking_budget` (tokens) | five levels onto a token count, clamped — see below |
| streamed `reasoning_content` | → `EventThinkingDelta` | the interface shows it working |
| `usage.prompt_tokens_details.cached_tokens` | → `Usage.CacheReadTokens` | **nested**, see below |
| `finish_reason` | → our stop reasons | `tool_calls`→`tool_use`, `content_filter`→`refusal` |

### `enable_thinking` is always stated, never omitted

The `qwen3.8` line thinks **by default**; older ids do not. So an omitted field
does not mean "off" — it means *whatever this particular model prefers*, which is
a per-model spend and latency difference with nothing to grep for. The client
sends an explicit `false` when thinking is off.

### Effort is a token budget, and it is clamped

Qwen has no `reasoning_effort` enum. Its only dial is a ceiling in tokens:

```
low → 1024    medium → 2048    high → 8192    xhigh → 16384    max → 32768
```

**Then clamped to half of `max_tokens`.** Reasoning tokens are billed as *output*
and are drawn from the *same* `max_tokens` allowance as the answer, so a 16k
budget under a 4k ceiling is not high effort — it is a guaranteed truncation that
still bills for the thinking, and the transcript just shows an answer cut off
mid-sentence. Half the ceiling means at least half of what was paid for reaches
the reader.

## Shape differences that fail silently

Each has its own test in `internal/llm/qwen_test.go`, because none of them
produces an error — they produce an agent that quietly works less well.

**Tool results.** Our block model carries them inside one user message, which is
what the Anthropic API wants. This API wants each result as its own message with
`role: "tool"`. Get it wrong and the model simply stops seeing tool output.

**Fragmented tool arguments.** Arguments arrive in pieces across chunks, keyed by
index, and must be reassembled before parsing. A half-parsed argument object
looks to the loop like the model choosing not to act.

**The tool-call id arrives once.** It is present on the *first* fragment of a
call and is an empty string on every fragment after it. Assigning it
unconditionally blanks it, and a `tool_result` whose `tool_call_id` is `""` is
silently dropped by the next request — so the loop looks like a model ignoring
its own tool calls.

**The cache field is nested.** Qwen reports prefix-cache hits at
`usage.prompt_tokens_details.cached_tokens`. The DeepSeek backend this replaced
used a flat `usage.prompt_cache_hit_tokens`. Reading the old path decodes cleanly
to **zero** rather than failing, so the mistake surfaces as "caching never works
on this deployment".

**Usage arrives after the choices end.** The final chunk carries an empty
`choices` list and the usage block, so usage is read outside the choices loop.

Also deliberate: `reasoning_content` is **not** replayed as input. It is an
output field, and the chain of thought has no defined meaning as input on a later
turn.

## Prompt caching

Qwen has no `cache_control` directive — its context cache is automatic and keyed
on the prefix — so the `Cache` flags on the system layers are not sent. The
**layer order still earns its keep**: stable charter and persona first, volatile
per-turn context last is exactly what an automatic prefix cache rewards.
`usage.prompt_tokens_details.cached_tokens` is the way to check it is working,
and it is surfaced in the trace.

## The startup check, and the upgrade hazard it exists for

Until 2026-09-03 this application shipped **Anthropic** and **DeepSeek**
backends. A deployment upgrading in place keeps its `.env`, and that `.env` very
likely still names one of their models. Those ids now go to Model Studio, and
they fail in **opposite** ways:

| Leftover id | What Model Studio does |
|---|---|
| `claude-opus-5` | clean **404**. Loud, easy to diagnose. |
| `deepseek-v4-pro` | **HTTP 200 and a real answer.** |

The second is not a typo in this document. Model Studio is a *multi-vendor
marketplace* and genuinely hosts DeepSeek, so the call succeeds, bills at a rate
this build has no price for, and answers from a model nobody selected in the new
configuration. That is precisely the "keeps working while answering from a model
nobody chose" failure the startup check was written to prevent — and prefix
enumeration can no longer catch it, because there is no longer a `deepseek`
backend row to compare against.

So the retired ids are refused by name:

```
OBA_AGENT_MODEL="deepseek-v4-pro" is a model from the deepseek backend, which
this build no longer supports. It was not simply removed: deepseek-v4-pro still
answers on OBA_BACKEND=qwen, billed at a rate this build has no price for, so
leaving it set would keep working while answering from a model you did not
choose. Set OBA_AGENT_MODEL to one of: qwen3.8-max, qwen3.8-flash, …
```

This is **not** a general "foreign model" check. Model Studio legitimately serves
`ZHIPU/`, `stepfun/` and `deepseek-` ids, and someone may deliberately want one:
an unrecognised id is a **warning**, not a refusal. Only ids that were *our own
defaults* are refused, because only those can be present by pure inertia. The
table in `internal/config/backends.go` can be deleted once no deployment carries
a pre-2026-09 `.env`.

A missing `QWEN_API_KEY` is refused at startup: unlike the Claude SDK this
replaced, there is no OAuth or ambient-credential path, so an empty variable *is*
proof of no credential and the failure is certain rather than possible.

## Errors worth their own branch

`402` is translated on its own as `MODEL_BILLING` and is never retried — an
exhausted balance or free quota otherwise reads as a generic outage and costs
somebody an hour.
