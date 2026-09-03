# 3. Model, system prompt, context

## Model choice

| Job | Model | Why |
|---|---|---|
| Agent loop | `qwen3.8-max` | Multi-step tool use over consequential material. The cost of a wrong benefits answer — a wasted trip, a missed deadline, a filing refused — is far above the cost of the tokens. |
| Intent routing | `qwen3.8-flash` | One classification into four buckets, on the latency path of every turn, at roughly a fourteenth of the agent model's rate. |

Both are `OBA_AGENT_MODEL` / `OBA_CLASSIFIER_MODEL`, and neither is read anywhere
except `internal/config`.

### Providers

`OBA_BACKEND` selects the provider, and the model ids follow it:

| Backend | Agent | Router |
|---|---|---|
| `qwen` (default) | `qwen3.8-max` | `qwen3.8-flash` |
| `scripted` | replays a script; tests and offline demos only | |

`qwen` is the only live provider. `scripted` is a fixture, not a provider: it
needs no key and reaches no network.

Because the model ids follow the backend, switching provider is one variable
rather than three that must be kept in step. Details, and the wire-shape
differences that fail silently, are in [12-qwen.md](12-qwen.md).

> **Upgrading from the Anthropic or DeepSeek backends?** Delete any
> `OBA_AGENT_MODEL` / `OBA_CLASSIFIER_MODEL` left in your `.env`. Startup refuses
> the retired ids deliberately — Model Studio is a multi-vendor marketplace and
> answers `deepseek-v4-pro` with a **200**, so a leftover value would keep the
> service looking healthy while billing for a model nobody selected.

**Effort** defaults to `high` and is set per intent. **Adaptive thinking** is on,
with `display: "summarized"` — the API default is `omitted`, which in a chat
interface reads as a long unexplained pause. Somebody waiting on an answer about
their own income deserves to see that something is happening.

Most turns never call the router at all: an analyst can reach exactly one intent,
and a person who tapped an intent chip has already answered the question. When
the router *is* unreachable, routing degrades to a keyword table rather than
failing the turn — a product that stops working because a classifier is down has
put a convenience on the main path.

## The system prompt is three layers

Order matters because the API caches on an exact prefix, and the prefix decides
what can be reused:

```
1. Charter + persona   stable, never varies                   → cache breakpoint
2. Intent              one of four, rendered from the registry → cache breakpoint
3. Context             this person, this turn                  → after the last breakpoint
```

The persona rides inside layer 1 rather than taking a layer of its own: it is as
stable as the charter, and a cache breakpoint is too scarce to spend on text that
never varies independently. See [13-name-and-voice.md](13-name-and-voice.md).

`TestRequestCarriesCacheBreakpointsEffortAndTools` asserts, against a server
speaking the real wire format, that exactly two breakpoints go out and that the
volatile layer is not inside the cached prefix. On Qwen there is no
`cache_control` directive at all — its context cache is automatic and keyed on
the prefix — so the flags are not sent and the *order* does the work instead;
`TestQwenRequestShape` asserts that too.

Layer 2 is *rendered from the intent registry*, not written twice. The prompt and
the enforcement code therefore cannot disagree: change `CannotDo` and both the
instruction and the docs move together.

Layer 2 also tells the model which verifiers will run and what each looks for.
Telling the model the test is not cheating — an unstated test is just a retry tax.

## Context engineering

Layer 3 is assembled from state, never from raw history, and it is capped:

- what is on file about this person, and where each field came from
- which permissions are granted
- which required facts are still missing, so the "ask at most two things" rule is
  actionable rather than aspirational
- what earlier tool calls established, summarised — so a later turn does not pay
  for the same retrieval again
- the tracked tasks
- any verifier remedies, when this is a redraft
- delivery settings in force (plain language, read aloud, low bandwidth, …)

Earlier turns' tool calls are deliberately **not** replayed into the message
history. Their outcomes are already in layer 3.
`TestShortTermMemoryDoesNotReplayToolCalls` holds that line.

## Answering language

**Chinese by default.** `OBA_REPLY_LANGUAGE` accepts `zh-CN` (default), `en`, or
`match`; a session overrides it, and the interface's language selector sets the
session's value on the very next message rather than the next conversation.

This is stated in the prompt as a rule, not left to inference, and it is placed
**first in the context layer** — before the profile, the findings and the tasks.
Two reasons. Everything else the model can see is English: this prompt, the tool
descriptions, the sample corpus. And a rule buried under a screen of context is
the one that gets dropped.

Two carve-outs travel with it, because without them the instruction does damage:

- Tool arguments stay in English — the corpus is indexed in English and the tool
  descriptions say so.
- Programme ids, phone numbers, addresses and opening hours are quoted verbatim.
  A translated address is an invented address.

And because a prompt instruction is not a control, the `reply_language` verifier
checks the delivered answer and asks for a redraft when it is in the wrong
script. See [07-guardrails-and-verifiers.md](07-guardrails-and-verifiers.md).

The interface's own strings live in one table (`web/static/i18n.js`), which also
renders server-side vocabulary — intent audiences, task statuses, service
domains, consent scopes — for a reader. Source code, comments and identifiers are
English throughout; an unmapped value falls back to what the server sent, so a
new term shows up untranslated rather than blank.
