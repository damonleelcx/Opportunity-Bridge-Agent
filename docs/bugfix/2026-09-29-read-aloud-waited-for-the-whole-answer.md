# Read-aloud waited for the whole answer, and long answers never got read

**Reported:** 2026-09-29, from production: "it's not reading to me", with
`Failed to load resource: the server responded with a status of 502` in the
console. **Area:** `internal/tts/fish.go`, `internal/httpapi/tts.go`,
`speakWithVendor` in `web/static/app.js`. **Status:** fixed, with fences
(`TestFishReturnsBeforeTheRenderHasFinished`,
`TestFishEndsARenderThatStopsSendingAudio`,
`TestSpeakStreamsAudioAsItIsRendered`).

## What was wrong

The first suspect was an expired key. It wasn't one: the production key
rendered audio when called directly, and `/api/health` reported the vendor as
enabled. The production log showed the actual problem:

```
/api/tts 200  22210ms
/api/tts 200  12834ms
/api/tts 502  TTS_TRUNCATED: reading audio from fish: context deadline exceeded (Client.Timeout ...)  90002ms
```

The free backbone (`s2.1-pro-free`) renders at roughly a tenth of real time.
Measured against Fish with the production voice:

| answer | first audio byte | whole render |
|---|---|---|
| ~70 chars | 1.2s | 6.5s |
| ~420 chars | 1.3s | 39s |
| ~1,400 chars | 1.2s | 118s |

Fish itself streams: the first audio arrives in about a second. But every
layer on our side waited for the whole render:

1. `Fish.Speak` did `io.ReadAll(resp.Body)`, under an `http.Client{Timeout: 90s}`
   that covers the **body** too. So any answer whose render took over 90s
   failed, and every other answer arrived only after its full render.
2. `/api/tts` wrote the finished `[]byte` in one call.
3. The browser did `await r.blob()` before creating the `Audio`.

A long answer meant a minute and a half of silence, then a 502. The browser
voice did take over after that, but by then the person had given up waiting.

## Fix

- `tts.Speech.Audio` is now an `io.ReadCloser`. `Fish.Speak` returns as soon as
  the **first byte** has arrived. Failures before that byte (refusal, empty
  audio, unreachable) are still errors, so the browser voice still covers them.
- The 90s whole-response timeout is gone. It is replaced by the two ways a
  render can actually hang: `ResponseHeaderTimeout` (30s, no answer at all) and
  `StallTimeout` (30s with no new audio bytes, logged as `TTS_STALLED`).
- `/api/tts` copies and flushes each chunk as it arrives. A failure mid-stream
  just ends the audio and is logged as `TTS_TRUNCATED` at WARN. There is no
  fallback at that point, because the person has been listening and the browser
  voice would start the answer again from the top.
- The browser plays through `MediaSource` when `audio/mpeg` is supported
  (Chrome, Edge, Firefox) and appends chunks as they arrive. Where it is not
  supported, it keeps the old blob path. That path is still slow for long
  answers, but it no longer fails at 90s.

## Why nobody caught it

The tests used a fake vendor that answered instantly, so "wait for all of it"
and "stream it" looked the same. The 90s timeout's comment even said synthesis
was "roughly a fifth of real time". That was never measured against the free
backbone, and it is about half as fast as that.
