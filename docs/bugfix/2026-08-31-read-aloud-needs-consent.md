# Read-aloud sent the answer to a vendor without asking

**Reported:** 2026-08-31, as the follow-up left open by
`2026-08-31-the-privacy-claim-was-false.md`.
**Area:** `internal/domain` (the scope list), `internal/httpapi/tts.go` (the
gate), `internal/tools/registry.go` (the wording), `web/static/app.js` and
`i18n.js`.
**Status:** shipped and verified live against a real speech vendor.

## What was wrong

Fixing the landing-page claim made the product *tell the truth* about read-aloud.
It did not change what read-aloud did.

Pressing it still posted the answer — the person's city, their unemployment, the
benefit they are claiming — to an outside speech vendor, with no question asked.
In a product that raises a permission card before merely **storing** those same
facts. `docs/17-read-aloud.md` had already called this "at odds with how the rest
of this works".

Disclosure is not consent. A sentence on a marketing page is not a thing anybody
agreed to.

## The fix

**A new scope, `read_aloud_via_vendor`**, checked in `speak` before a byte
leaves the process. The gate is in the handler, not in the interface: this
endpoint is reachable by anything holding a sign-in, so the interface is not what
protects anybody.

`/api/tts` now requires `session_id`. The permission belongs to a person, and
without a subject the endpoint had nobody to check — which is how it came to send
answers with nobody having agreed. A client that does not send it gets 404 and
falls back to the browser's voice, which is the safe direction.

**Refusing costs nothing.** The client falls through to `speakLocally`, which is
what an unkeyed deployment does anyway. The 403 is the one failure in
`speakWithVendor` that is *not* silent — every other path stays quiet because the
answer is already on screen, but this one is a question only the person can
answer. `vendorVoice` is deliberately not latched to false on a 403: they may
grant it, and the next answer should use the better voice.

**What the person is asked** comes from `tools.ConsentPromptFor`, the same table
the tool path uses — exported rather than duplicated, because a second copy of
these sentences would be a second policy. The clause about what the vendor may
then do is appended from `tts.TrainsOnRequests(cfg.TTSModel)`, so it tracks the
backbone instead of going stale when a deployment switches.

Live, against the real vendor on the free backbone:

```
1. press with no permission   → 403 CONSENT_REQUIRED, vendor not called
   plain: "…the text of the answer has to be sent to an outside speech service. May I?"
   keep:  "Only the text of that one answer… This deployment's speech backbone
           also permits the vendor to use what it receives to improve its own models."
2. grant, press again          → 200, 104906 bytes of audio
3. withdraw, press again       → 403 CONSENT_REQUIRED, vendor not called
```

## The bug this would have shipped with: four copies of the scope list

Adding a scope meant editing four hand-written lists — the constants in
`domain`, the API's validation switch, the interface's revoke panel, and the
`consent_request` tool schema. Each omission fails differently and silently:

| missed in | what happens |
|---|---|
| the API's switch | granting it answers 400; the card does nothing when pressed |
| the revoke panel | the person can be asked for it and **cannot take it back** — which makes "you can withdraw this at any time" something this service says and does not do |
| the tool schema | the model can never ask for it, and nothing says so |

`domain.ConsentScopes()` and `domain.IsConsentScope()` are now the one list.
The API validates against it, the tool schema is built from it, and `/api/meta`
publishes it so the panel renders whatever the server actually has. An empty list
reaching the panel is logged (`CONSENT_SCOPES_MISSING`) rather than rendering as
an empty panel, which would read as "no permissions to withdraw".

## Regression tests

| test | file | holds |
|------|------|-------|
| `TestSpeakRefusesUntilThePersonHasAgreed` | `internal/httpapi/tts_test.go` | 403 before consent, vendor not called, and the refusal carries the question **plus the training clause that applies to this backbone** |
| `TestSpeakProceedsOnceGranted` | same | granting works and the vendor receives the text |
| `TestWithdrawingReadAloudConsentStopsTheVendor` | same | withdrawal actually stops it |
| `TestPermissionsPanelDoesNotKeepItsOwnScopeList` | `web/interface_test.go` | the panel reads `consent_scopes` from the server and an empty list is reported |
| `TestEveryConsentScopeHasAName` | same | every scope in `domain.ConsentScopes()` has a name in both languages, so nobody is asked to decide about the string `read_aloud_via_vendor` |
| `TestVendorSpeechAlwaysFallsBackToTheBrowsersOwnVoice` | same | unchanged invariant: every vendor failure, 403 included, still reaches the browser's own voice |

Mutation drills, each asserted to have landed in the file first:

- the gate always passing → `TestSpeakRefusesUntilThePersonHasAgreed` reds with
  "the vendor was called 1 times without permission; the text has already left"
- the panel keeping its own array → `TestPermissionsPanelDoesNotKeepItsOwnScopeList` reds
- the scope dropped from `ConsentScopes()` → the API rejects granting it and the
  consent tests red

Note the third: `TestEveryConsentScopeHasAName` iterates the list it checks, so
removing a scope makes it loop one fewer time and stay green. It is not the fence
for that failure — the API validation is. A fence that enumerates what it checks
cannot notice something leaving the enumeration.

## One fence had to be loosened, and why that was not a weakening

`TestVendorSpeechAlwaysFallsBackToTheBrowsersOwnVoice` pinned the fallback with a
regex containing the literal `await speakWithVendor(body)`. Giving read-aloud a
second argument turned it red while the fallback was untouched. The regex now
matches `speakWithVendor([^)]*)` — it still requires the vendor attempt to sit
inside the `if (...) return;` guard with `speakLocally(body);` as the immediately
following statement, which is the property that matters. Pinning an argument list
made the fence fire on a refactor and would eventually have taught somebody to
edit it without reading it.
