# The same permission was asked for twice

**Reported:** 2026-08-28, "why ask me to authorize twice?" — two identical
"需要你的授权 — 保存你告诉我的信息" cards, one above the other, both showing
已授权.
**Area:** `consent_request` in `internal/tools/builtin.go`, the consent gate in
`internal/tools/registry.go`, event emission in `internal/agent/agent.go`.
**Status:** fixed.

## What it looked like

The person granted "keep what you tell me". A second, identical card appeared
straight after. They granted that one too, which is why both read 已授权 — the
interface gives no way to tell one card from the other, so there was nothing to
do but answer the same question again.

## Why

`consent_request` never read the store. Whatever the current state, it built a
prompt and returned it:

```go
scope := domain.ConsentScope(argStr(a, "scope"))
prompt := consentPromptFor(scope)      // <- no Consent() lookup anywhere
return Result{Consent: prompt, ...}
```

The other place a card can be raised — the gate in `Registry.Call` — does check,
and only raises one when `!g.Granted`. So the same fact had two consumers and
only one of them read it.

What made it fire reliably rather than occasionally is the client. Granting
posts to `/api/consent` **and then sends a follow-up message on the person's
behalf** (`web/static/app.js`, `settle()`): "我已同意 store_profile，请继续。"
That starts a new turn in which the subject of the conversation is the
permission, so the model asks for it — and the tool, not knowing it was already
given, produced a second card. Grant → auto-message → ask again is a loop, and
it is why the two cards were adjacent rather than turns apart.

## Three layers

| Layer | Finding |
|---|---|
| Implementation | `consent_request` is missing an already-granted short-circuit. |
| Design | "Is this permission held" has two consumers and one source of truth, and only the gate read it. The tool's own description says "nothing is granted by calling this" but never says "check first". |
| Process | No test asserted that a held scope raises no card, so the gap was invisible to CI. |

**Owner:** the `consent_request` tool. The gate in `registry.go` was correct
throughout.

**Why it did not show up earlier:** it needs the scope to be granted *and* the
topic to come up again. The auto-message on grant makes that happen every single
time, so this was reproducible from the first grant onward — it was simply never
tested.

## What changed

- `consent_request` reads `env.Store.Consent(...)` first. When the scope is held
  it returns no card and tells the model plainly not to ask again.
- `agent.go` keeps one turn from raising the same scope twice, whichever
  producer raised it. This is the belt to the tool's braces: a turn that calls
  `consent_request` *and* trips the gate used to put two cards on one screen.
- Not changed: the auto-message on grant. With the short-circuit in place it no
  longer produces a card, and removing it would change a user-facing interaction
  — that is a product decision, not a bug fix. **It is still worth a look:** the
  system speaks in the person's voice without them typing.

## Regression tests

`internal/tools/tools_test.go`:

| Test | Fence |
| --- | --- |
| `TestConsentRequestRaisesNoCardWhenAlreadyGranted` | a held scope raises no card, and the model is told why |
| `TestConsentRequestStillAsksWhenNotGranted` | the short-circuit did not swallow the feature |

Both mutation-drilled: removing the check turns the first red; making the
short-circuit unconditional turns the second red.

```
GOWORK=off go test ./internal/tools/ -count=1
```
