# Answers arrived as raw Markdown

**Reported:** 2026-08-28, from a production screenshot — "response format is raw".
**Area:** `internal/prompt/prompt.go` (the charter).
**Status:** fixed by making the charter own the format.

## What the person saw

Literal `**深圳市人力资源和社会保障局（live-001）**` on screen, and dash bullets
rendered as dashes. The model was writing Markdown; nothing rendered it.

## Root cause: neither side owned the format

- `web/static/app.js` writes the answer with `textContent`, which is deliberate —
  the answer contains scraped page titles and organisation names, and putting
  model output through an HTML renderer is a decision, not a default.
- The charter said **nothing at all** about output format. The only mention of
  markup anywhere in the prompt was inside the read-aloud accessibility rules.

So the model was free to reach for Markdown — which it does the moment an answer
has several leads in it — and every surface took it literally.

There is a second surface that made this worse than cosmetic: `speak(final.answer)`
hands the **raw** text to the browser's speech synthesiser. With the read-aloud
setting on, a pair of asterisks is spoken as the word "asterisk". This product's
interface carries 大白话 / 大字号 / 读给我听 toggles; the audience those exist for
is exactly the audience that markup noise costs the most.

**Not a regression.** The gap has always been there. It became visible when the
live lookup started returning several leads per turn
([2026-08-28-live-search-never-looked-for-training.md](2026-08-28-live-search-never-looked-for-training.md)):
more items to list means more list-shaped answers, and the model formats lists.

## The fix, and why this side of the boundary

Two options were on the table:

| | render Markdown in the interface | make the charter forbid it |
| --- | --- | --- |
| Reach | the screen only — TTS still needs its own stripping | screen, speech, and any surface added later |
| Cost | a renderer plus an HTML-injection surface fed by scraped page text | one paragraph |
| Consistency | a second formatting rule alongside the read-aloud one | the same rule the read-aloud path already states |

The charter won. It is one rule in one place, it reaches every surface at once,
and it does not put model-authored text through an HTML renderer.

Added under HOW YOUR ANSWERS READ:

> Write plain text, never Markdown. No asterisks for emphasis, no hash headings,
> no dash or asterisk bullet markers, no tables. Your answer is shown exactly as
> you write it and the read-aloud setting speaks it exactly as you write it, so a
> pair of asterisks arrives as two asterisks on the screen and as the word
> "asterisk" in somebody's ear. Separate points with a blank line. Number them 1.
> 2. 3. only when the order matters.

This changes the cached prompt prefix, so the first request after deployment
pays a cache miss. Once.

## Verified

A real turn through the running product afterwards, on the question that produced
the reported screenshot: **0 bold markers, 0 headings, 0 bullet markers, 0
tables.** The model used blank lines and numbered steps.

## Regression tests

`internal/prompt/prompt_test.go` — `TestCharterForbidsMarkdown`.

**This fence is weaker than the others in this repo and it is worth being honest
about that.** It asserts the rule is present in the charter; it cannot assert the
model obeys it, and it is not mutation-drilled, because deleting the rule and
asserting the rule is missing are the same edit. What makes that acceptable here
is that the failure is not silent: a model that slips puts asterisks on screen,
where the reader sees them — which is exactly how this was reported.

**Not done, and available if drift shows up:** a deterministic verifier that
raises an advisory finding when the delivered answer still contains Markdown.
It would sit alongside the existing verifiers in `internal/guardrail/verify.go`
and make the slip visible in the trace instead of only on screen. Left out
because it is a new check on every turn to catch a cosmetic fault that is already
visible, and adding it is a decision rather than part of this repair.
