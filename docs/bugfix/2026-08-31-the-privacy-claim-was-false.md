# The landing page promised "no audio leaves your device". Both halves were false.

**Reported:** 2026-08-31, found while auditing the honest-limits copy.
**Area:** `home.feat.f6b` on the landing page (`i18n.js`, `index.html`), the same
bullet in both READMEs, and `deploymentFacts()` in `internal/httpapi/server.go`.
**Status:** fixed and verified in a browser against both deployment shapes. One
consequence is left open deliberately — see the last section.

## What it said

> 用浏览器自己的语音能力，**音频不离开你的设备**。
> The browser's own speech APIs; **no audio leaves your device**.

On a card headed 「朗读与语音输入」, so the claim covered both directions.

## Why it is false, in both directions

### Voice input — the audio does leave

Dictation is `window.SpeechRecognition` (`app.js`, `startDictation`). That API is
implemented by the browser, and on the major browsers it is a network service.
Measured in this project's own Chromium:

```js
SpeechRecognition.available({langs:['zh-CN'], processLocally:true})   // "unavailable"
SpeechRecognition.available({langs:['zh-CN'], processLocally:false})  // "available"
```

On-device recognition for Chinese is not available; only the network path is.
The code does not pass `processLocally` at all, so it takes that path. The audio
reaches the browser maker. It never reaches this service — which is worth saying,
but is not what the sentence said.

### Read-aloud — the answer text leaves

With `OBA_TTS_API_KEY` set, which **production has**, pressing read-aloud POSTs
the answer text to the speech vendor. That text is a person's city, their
unemployment, the benefit they are claiming. On the `s2.1-pro-free` backbone the
vendor's published terms allow using requests to improve model quality.

`docs/17-read-aloud.md` has said this the whole time — its own table reads
"Privacy | the text never leaves the machine | **the answer is sent to the
vendor**", with a section headed "‼️ What the free backbone actually costs". So
the product was not confused about the facts. The technical doc was right and the
page a person actually reads said the opposite.

## Why it happened, and the general lesson

The claim was written when the browser voice was the only voice, and it was
**hardcoded into the copy**. The vendor voice shipped later (PR #14/#15) and made
the sentence false without touching it.

That is the same failure as the corpus count that said 21 when the answer was 26
— a fact about a deployment written into prose that has no way to notice. The
difference is what it costs. A stale number misinforms; **a stale privacy claim
is one somebody may have relied on when deciding what to type.** It is the worst
class of fact to hardcode, because it is true on the machine of whoever wrote it
and false in production.

## The fix

`deploymentFacts()` gains `speech_vendor_enabled` (`s.TTS != nil`), served from
the already-public `/api/health` alongside the corpus counts and
`live_search_enabled`. The card now carries:

- **A claim that is true everywhere**: dictation is transcribed by your browser,
  on the major browsers that means the browser maker's servers, so what you say
  does leave your device; it never passes through this service.
- **A sentence chosen by the deployment**: either "this instance has a speech
  vendor — pressing read-aloud sends the answer text to it, and the free tier's
  terms let it use those requests to improve its models; nothing is sent unless
  you press it", or "this instance has no speech vendor; read-aloud is done
  entirely by your browser and no answer text goes to a third party."

**The unknown case resolves towards the warning**, not the reassurance:
`m.speech_vendor_enabled !== false`. A missing or malformed field produces "text
may be sent". Defaulting the other way would make a page that cannot reach
`/api/health` quietly promise privacy it has not checked — which is the original
bug with extra steps.

## Verified

Browser, against two servers built from this tree:

| deployment | `/api/health` | what the card renders |
|---|---|---|
| `OBA_TTS_API_KEY` set (production-like) | `speech_vendor_enabled: true` | 「本实例接了语音合成厂商：你按下朗读时，答案正文会发给厂商去合成…」 |
| no key | `speech_vendor_enabled: false` | 「本实例没有接语音合成厂商…答案正文不会发给任何第三方。」 / "This instance has no speech vendor…" |

Both languages checked; the line survives a locale switch.

## Regression tests

| test | file | holds |
|------|------|-------|
| `TestVoicePrivacyClaimIsNotHardcoded` | `web/interface_test.go` | the old promise is gone from both languages; both deployment sentences exist; `home.js` branches on `speech_vendor_enabled`; the unknown case defaults to the warning; the element is `hidden` in the markup |
| `TestDeploymentFactsAreReadableWithoutSigningIn` | `internal/httpapi/server_test.go` | `/api/health` reports `speech_vendor_enabled` to a client with no sign-in |
| `TestUnknownBackboneIsAssumedToTrain` | `internal/tts/tts_test.go` | an unrecognised or empty backbone reports that it trains; matching is exact |
| `TestDocsAgreeWithTheLanguageOfTheCorpus` | `internal/corpus/corpus_test.go` | neither README describes the corpus as English while the data is Chinese |

Mutation drills, each asserted to have landed in the file before running:
re-inserting "音频不离开你的设备" reds the first check, and flipping the default to
`=== true` reds the unknown-case check.

## Follow-up: the paid backbone is blocked on the vendor account, not on us

The chosen remedy was option 1 — switch to `s2.1-pro`, whose terms do not permit
training. It cannot be switched on yet:

```
POST https://api.fish.audio/v1/tts   model: s2.1-pro
402 {"status":402,"message":"Insufficient API credit. API credit is managed
     independently from platform credit. Please visit
     https://fish.audio/app/developers ..."}
```

`s2.1-pro-free` answers 200 for the same voice and text in the same run, so the
key is fine; the account's **API credit** (separate from platform credit) is
empty. Flipping `OBA_TTS_MODEL` today would make every synthesis fail with
`TTS_REFUSED`, the endpoint answer 502, and the browser fall back to its own
voice for the rest of each session — while `speech_vendor_enabled` still reported
true, so the page would warn about a request that never succeeds.

What was done instead, so the switch is one line once the account is funded:

`tts.TrainsOnRequests(model)` derives the answer from the backbone, and
`deploymentFacts()` publishes it as `speech_vendor_trains_on_text`. The card now
has three states rather than two — no vendor / vendor that does not train /
vendor that may train — and picks one from the running deployment. Adding credit
and setting `OBA_TTS_MODEL: "s2.1-pro"` in `deploy/k8s/20-config.yaml` changes
the sentence with no edit to any copy.

`paidBackbones` is an **allowlist**: a backbone the table has not heard of is
reported as one that trains. `s1` is deliberately absent because its terms were
not checked. Over-warning costs a reader some caution they did not need;
under-warning costs them something they cannot take back.
`TestUnknownBackboneIsAssumedToTrain` pins the direction, including that matching
is exact so `S2.1-PRO` does not read as paid.

Neither `.env` nor `deploy/k8s/20-config.yaml` was changed. Switching before the
account has credit would break read-aloud.

## ‼️ Left open, deliberately: disclosure is not consent

This change makes the page tell the truth. It does **not** change what happens.

Pressing read-aloud still sends the answer — city, employment status, benefit
being claimed — to a third party whose free tier may train on it, with no consent
step, in a product that has a consent-scope mechanism and asks permission before
merely *storing* the same facts. `docs/17-read-aloud.md` already calls this "at
odds with how the rest of this works".

Three options, none taken here because this is a product decision:

1. **Switch the backbone.** `OBA_TTS_MODEL=s2.1-pro` removes the training clause
   for roughly $0.018 per answer. Smallest change, removes the sharpest edge.
2. **Put read-aloud behind a consent scope**, like the other things that leave
   the person's control.
3. **Drop the vendor voice** and keep the browser's own, which is what an unkeyed
   deployment already does.

Note also that Fish published the free window as running **through 2026-08-31**,
which is today — so option 1 may stop being a choice and start being a bill.
