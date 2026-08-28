# 17. Reading answers aloud

Read-aloud is not a garnish here. Some of the people this is built for cannot
comfortably read a screen of policy text, and the checkbox that turns it on sits
next to 大字号 for that reason. So the first rule is the one everything else
bends around:

> **The answer is always readable aloud.** Every failure in this file ends with
> the browser's own built-in voice, never with silence.

## Two voices, one of them always available

| | browser voice | vendor voice |
|---|---|---|
| Needs | nothing | `OBA_TTS_API_KEY` + `OBA_TTS_VOICE_ID` |
| Quality | whatever the OS ships | whatever the voice is |
| Cost | none | none on `s2.1-pro-free`, otherwise per byte |
| Privacy | the text never leaves the machine | **the answer is sent to the vendor** |

With no key, `speechSynthesis` reads the answer and startup logs `TTS_DISABLED`.
The browser asks `/api/tts` once, is told 503, and stops asking for the rest of
the session — an unkeyed deployment makes one failed request, not one per answer.

### Which browser voice

`READING_VOICES` in `web/static/app.js`, best first, per language.
`SpeechSynthesisVoice` exposes a name, a language and whether it is local — no
gender, no age. A female voice therefore cannot be *asked for*, only named, so
the table names the ones the three engines actually ship. Languages are compared
**exactly, not by prefix**: half the Chinese voices on a Mac are `zh-TW`, and
`Sandy` exists in both, so a prefix match would read a 成都 social-insurance
answer in a Taiwanese accent.

Pitch is 1.15 and rate 0.95. Pitch is held low deliberately — age-related
hearing loss takes the high frequencies first, so every step up costs
comprehension for part of the audience this feature exists for.

## The vendor path

```
browser ──POST /api/tts──▶ obagent ──POST /v1/tts──▶ Fish Audio
        ◀──── audio/mpeg ──────────  ◀──── audio ────
```

**Why it goes through our own server.** The alternative is the browser calling
the vendor directly, which means shipping the API key to the browser, which
means anyone can spend it. The key stays on this side.

Three things that follow, all load-bearing:

- **It is behind the sign-in.** `Routes()` wraps the whole mux in the gate, so
  the endpoint is protected by default rather than by somebody remembering. An
  endpoint that spends a vendor's budget must never be reachable by a stranger
  with the URL. `TestSpeakRequiresASignIn` holds it.
- **It is capped on this side too.** The browser caps at 3,000 characters; the
  server does not trust that, because the endpoint is reachable by anything
  holding a cookie. Over-long text is truncated with a `TTS_TEXT_TRUNCATED`
  warning rather than refused — half an answer read aloud beats silence — but it
  is never truncated *quietly*.
- **The audio is `no-store`.** It is a rendering of one person's answer, naming
  their city and their situation. It must not sit in a shared cache.

### The voice is a model id, not a setting

Fish picks the voice with `reference_id`, a published model id — the last path
segment of a `fish.audio/m/<id>/` URL. That is why `OBA_TTS_VOICE_ID` exists as
its own variable and why startup **refuses** a key with no voice id: without one
every answer is read in the vendor's default voice, which is the single thing
the person configuring it was trying to change.

The **backbone** is separate from the voice and travels as an HTTP `model`
header, not in the body. Putting it in the body is accepted and ignored, which
would silently bill the paid model — `TestFishSendsTheVoiceInTheBodyAndTheModelInTheHeader`
is the fence.

## ‼️ What the free backbone actually costs

`s2.1-pro-free` bills nothing. Fish's published terms for it say **requests may
be used to improve model quality**, and the text sent is the answer: a named
city, an employment situation, often a benefit the person is trying to claim.

That is a privacy trade, not a free lunch, and it is at odds with how the rest
of this system treats the same data — consent scopes on the profile,
k-anonymity on the aggregate signals, redaction in handoff summaries. Those
exist because this audience is the wrong one to be careless with.

It is the default anyway, because the alternative default is a deployment that
starts spending money the moment somebody sets a key. But it is the default with
this page attached to it, and a deployment serving real users should decide
deliberately between:

| | cost | the answer text |
|---|---|---|
| `s2.1-pro-free` | none | may be used to improve the vendor's models |
| `s2.1-pro` | ~$0.018 per answer | check the paid tier's retention terms |
| no key at all | none | never leaves the machine |

Note also that the free window is dated: Fish published it as running **through
2026-08-31**, with notice promised before changes.

## What is not built

- **No streaming.** The whole file is rendered, then played. Synthesis runs at
  roughly a fifth of real time, so a long answer takes ten seconds or more
  before it starts. Fish supports chunked streaming and the browser can play
  progressive MP3; the reason it is not wired is that streaming through a POST
  needs `MediaSource`, and a GET would put the answer text in a URL.
- **No cache.** Two identical answers render twice. Worth adding if the paid
  backbone is ever switched on; pointless while the free one is in use.
- **No commercial-use check.** Voice models on fish.audio may carry separate
  commercial terms from the API itself. That is a question for whoever's account
  the key belongs to, and this code cannot answer it.
