# 13. 阿桥 — the name, the voice, the face

## The name

**阿桥 / Aqiao.** 桥 is a bridge, which is the product's own metaphor and its
repository name. 阿 in front of a single character is how people address someone
familiar rather than someone official.

That prefix is the whole design decision. This system deliberately holds no
authority over anybody: it does not decide eligibility, it does not score
people, it names who decides instead. A name like 顾问 or "Advisor" or
"Assistant" would claim a standing it does not have, to an audience for whom
official-sounding things have historically meant paperwork they could not
complete. 阿桥 sounds like a neighbour who has filled in this form before.

It is also two syllables and easy to say aloud, which matters: read-aloud is a
first-class delivery mode here, not a garnish.

Changing it is `prompt.AgentName` plus the `app.name` / `agent.name` / `greeting`
keys in `web/static/i18n.js`.

## The voice

`prompt.Persona` — a separate constant from the charter, riding in the same
cached layer, and explicitly subordinate to it.

- Calm and unhurried. Short: if one sentence will do, it is one sentence.
- **Never reassures.** No "don't worry", no "rest assured", no 别担心, no 放心.
- No exclamation marks, no emoji, no "great question", no celebrating.
- Says "I don't know" plainly, once, without apologising for it.
- Treats the person as capable — explains the actual rule, not a simplified
  version of them.
- When it stops itself, it says which rule stopped it and that nothing was done.
- Never speaks as the authority; names who decides.

And the clause the rest hangs from:

> This is how you speak. It is never what you say. Where warmth and accuracy
> conflict, accuracy wins.

### Why a persona needs a guard

Warmth is exactly what erodes an honest "no". "Don't worry, it'll be fine" costs
nothing to write and can cost somebody a morning and a bus fare across a city.

A prompt cannot hold that line on its own, so the persona ships with
`no_false_reassurance` (severity `repair`), wired into `individual_pathway` and
`low_access_support` — the two intents where a person's own prospects are at
stake. It fires on the reassurance phrases the persona forbids, and the remedy
is always the same shape: **what is known, what is not, and who decides.**

`turn-false-reassurance-repaired` in the eval suite holds it.

> This verifier is an addition beyond "give it a name and a voice". It is a
> small one, and it is one line per intent to remove if you would rather not
> have it.

## The reading voice

The persona above is how 阿桥 writes. This is who reads it out, and it is a
separate decision, made in `READING_VOICES` in `web/static/app.js`.

`SpeechSynthesisVoice` gives a name, a language tag and whether it is installed
locally. There is no gender field, no age field, nothing about timbre. So a
female voice cannot be *asked for* — it can only be named. The table lists, best
first, the female voices the three engines this runs in actually ship: Microsoft
(Edge, Windows), Apple (Safari, and Chrome on macOS), Google (Chrome elsewhere).
On macOS 2026-08-28 it resolves to **Sandy (Chinese (China mainland))**; on a
machine with none of them it falls through to the platform default.

Three things that are load-bearing:

- **Falling through must still speak.** A missing voice is not a reason to stay
  silent. Read-aloud sits next to 大字号 as an accessibility control; for some
  readers it is the only way the answer arrives.
- **Languages are compared exactly, not by prefix.** Half the Chinese voices on
  a Mac are `zh-TW`, and `Sandy` exists in both. A `zh` prefix match would read
  a 成都 social-insurance answer in a Taiwanese accent.
- **Pitch is raised to 1.15 and no further**, and rate stays at 0.95. Pitch is
  the only dial the API gives for "younger and friendlier", but age-related
  hearing loss takes the high frequencies first — every step up costs
  comprehension for part of the audience this feature exists for. The named
  voice carries the character; pitch only leans on it.

`TestReadAloudPicksANamedVoiceAndStillSpeaksWithoutOne` in
`web/interface_test.go` holds all three.

## The greeting

Rendered locally from the i18n table on every new conversation, never by the
model: it costs nothing, it is identical every time, and it is where the
boundary gets stated before anybody has asked anything.

> 我是阿桥。你说说情况，我来找能办的事——岗位、培训、补贴，还有具体去哪儿办。
>
> 先说清楚两件事。第一，我不判定你符不符合条件，那是受理机构的事；我会把公布的
> 条件一条条念给你听，能确定的说能确定，不确定的就说不确定。第二，随时可以让我
> 找人来接手，不用等到自己实在弄不动。

## The face

An illustration of 阿桥, committed to this repository and served by this binary —
no bucket, no CDN, no request but the one to the model.

She is a white-haired figure in a white-and-gold uniform under a floating amber
halo. The halo is what does the work at small sizes: it is the one element of the
silhouette still legible at 24px, and 24px is the size that actually matters,
because the avatar sits beside every answer and not only in the header.

Two crops of one reference sheet, wired in `web/static/avatar.js`:

| File | Intrinsic | Where it is drawn |
|---|---|---|
| `web/static/mascot.png` | 360×360, head | every avatar slot — sidebar, sign-in card, tab icon, overview panel, beside every message |
| `web/static/mascot-full.png` | 340×800, full figure | the landing page's voice section (`阿桥 是谁`), once |

Two crops of one character are not two faces; two *drawings* would be. That
distinction is the whole rule, and it is why the previous build's arrangement had
to end — see below.

The landing page draws both at once, the figure with the head crop as a badge
pinned to its corner. That badge was rendering as a 4× zoom on one cheek for as
long as the avatar has been an `<img>`; see
[bugfix/2026-09-02-the-voice-badge-was-sized-by-the-figure-rule.md](bugfix/2026-09-02-the-voice-badge-was-sized-by-the-figure-rule.md).

### What was lost, said plainly

Until recently a second, hand-drawn mark shipped beside the illustration: a
bridge that read as a face, inline SVG, themed from CSS variables. It existed
because it could change expression and an illustration cannot. It had four moods:

| Mood | When | What changed |
|---|---|---|
| `calm` | default | |
| `thinking` | a turn is in flight | a traveller crossed the deck |
| `listening` | voice input is open | two arcs at the right |
| `serious` | the turn was blocked, refused, or stopped early | **the arch flattened** |

Two drawings for one agent is having no face, so the mark went and the
illustration stayed. **`serious` went with it, and nothing has replaced it.** An
interface that keeps smiling while the agent says *"I stopped myself from sending
that answer"* is doing in pixels exactly what the persona forbids in words — so
this is a real cost, not a tidy-up.

What makes it acceptable is that the face was the *second* signal. A blocked turn
already says, in words, which rule stopped it and that nothing was done. Dropping
a redundant shape cue is allowed under *colour never carries meaning alone*;
dropping the words would not be.

`thinking` is the one mood that still shows, and only in one place and only as
motion: `.mascot.is-thinking .mascot-img` in `styles.css` bobs the overview
panel's illustration while a turn is in flight, and stops under
`prefers-reduced-motion`.

⚠️ **No interface string may claim the face changes.** `home.bound.note` and
`home.voice.p3` both claimed it — for as long as the mark had already been gone —
and were corrected alongside the new art. `data-mood` is still written onto the
element, so a future treatment has somewhere to hang.

If someone wants the moods back: the reference sheet the crops come from does
ship an eyes-closed smile and a finger-to-cheek thinking pose, so `thinking` and
`listening` could be restored as two more crops behind `data-mood`. It ships **no
serious expression**, which is the only one that was load-bearing. Restoring the
easy two and not the hard one would make the face look expressive while still
being unable to stop smiling at a refusal — which is worse than a face that
plainly never moves.
