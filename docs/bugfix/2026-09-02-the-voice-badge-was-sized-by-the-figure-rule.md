# The landing page's avatar badge was a blown-up crop of one cheek

**Reported:** 2026-09-02 — found while replacing 阿桥's art, not reported by a
user. **Area:** `web/static/home.css`, the landing page's voice section
(`06 · 阿桥 是谁`). **Status:** fixed, with a fence
(`TestTheVoiceFigureRuleCannotReachTheBadge`).

## What was wrong

The voice section draws 阿桥 twice: a large image, and a small round badge
(`.voice-mark`) pinned to its bottom-right corner. The badge is a **child** of
`.voice-art`, so this rule matched both:

```css
.voice-art img { width: clamp(180px, 40vw, 260px); height: auto; }
```

`.oba-avatar` — the badge's own class — sets `width: 100%`, but at specificity
(0,1,0) it loses to `.voice-art img` at (0,1,1). So the badge's image was laid
out **260px wide inside a 62px `overflow: hidden` box**. What the reader saw in
that badge was not a face: it was a 4× zoom on whichever part of the face
happened to land in the top-left 62×62 of the scaled image.

Measured before the fix, at a 1280px viewport:

```
badge  css = [230, 230]   inside .voice-mark, which is 62 × 62
```

## Why nobody caught it

The rule was written when the badge was an inline `<svg>`, which is why the
stylesheet has a matching `.voice-mark svg { width: 100% }` line — that one *did*
size the badge, because `svg` is not `img`. When `avatar.js` stopped drawing SVG
and started emitting an `<img>`, the `svg` rule stopped applying and the
`.voice-art img` rule silently took over. Nothing failed, nothing logged; a
decorative element in one section of one page just quietly started rendering
wrong, and it is `aria-hidden` so no assistive tech reported it either.

This is the shape worth remembering: **a selector written against one element
type keeps matching after the markup changes underneath it, and a looser
selector nearby inherits the job.** The change that introduced it was in
`avatar.js`; the damage was in `home.css`.

## The fix

Scope the figure rule to a class the figure alone carries, and teach the badge
rule about `img`:

```css
.voice-art .voice-figure { … }                       /* was .voice-art img */
.voice-mark svg, .voice-mark img { width: 100%; … }  /* was svg only */
```

`.voice-figure` is on the `<img>` in `index.html`. The badge now measures
62 × 62, matching its box.

## Why it is not just "add width to .voice-mark img"

That would have fixed the symptom and left `.voice-art img` able to reach the
next thing anybody puts inside `.voice-art`. The rule is about **one specific
image**, so it names that image.

## Fence

`TestTheVoiceFigureRuleCannotReachTheBadge` in `web/interface_test.go`:

1. fails if `.voice-art .voice-figure` is missing — so the rest cannot pass
   vacuously against a stylesheet that no longer sizes the figure;
2. fails on any `.voice-art img` descendant selector, whatever it declares;
3. fails if `.voice-mark img` is not sized;
4. fails if `index.html` drops `class="voice-figure"` or `/mascot-full.png`.

Both failure branches were drilled by mutation and confirmed to go red:
re-loosening the selector trips (1), adding a loose rule *alongside* the scoped
one trips (2).

## Related

- `web/static/avatar.js` — why the avatar is an `<img>` and no longer an SVG.
- [13-name-and-voice.md](../13-name-and-voice.md) — the two crops and what the
  removed SVG mark could do that they cannot.
