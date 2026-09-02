# 14. The interface

Built to a UX Pilot mockup (`main.html`), in the same visual language — sky-blue
brand, card-based, generous whitespace, a three-column shell — with the parts
that were prototype scaffolding replaced by working ones.

```
┌──────────┬───────────────────────────────────┬──────────────┐
│ 阿桥      │ 为你服务中 / 个人 · 机会路径   ⚙  │ 我的概览      │
│ 对话历史  │                                   │ 进行中任务    │
│  …        │  greeting · question · route pill │ 阿桥 (mascot) │
│           │  answer · opportunity cards       │ 档案记录      │
│ 身份/语言 │  suggestions                      │ 授权          │
│ /意图     │  › 系统运行详情 · 5 步            │              │
│ + 新对话  │  [ input · 🎙 · ➤ ]               │              │
└──────────┴───────────────────────────────────┴──────────────┘
```

## Two departures from the mockup, and why

**No CDN.** The mockup pulls Tailwind, Font Awesome and Google Fonts over the
network. This binary serves every byte it needs: the stylesheet is hand-written
in the same visual language, the eleven icons actually used are inline SVG
(`icons.js`), and the type is a system stack that picks up PingFang / Noto Sans
SC where they exist. A service window on a closed network is a real deployment
target, and it was already true that this app makes no external request but the
one to the model.

**The illustration is served by this binary, not fetched from a bucket.** The
mockup points its mascot slot at a generated PNG on somebody else's storage. The
same character is committed here — `web/static/mascot.png` (head, 360×360) and
`web/static/mascot-full.png` (full figure, 340×800) — so keeping the face costs
nothing against the rule above: still one binary, still no external request but
the one to the model.

**There is one face, and two crops of it.** An earlier build had genuinely two:
the illustration greeted from the overview panel and the sign-in screen, while an
inline SVG mark — a bridge that read as a face — sat beside every message,
because the mark could change expression and an illustration cannot. Two
different drawings for one agent is having no face, so the mark is gone. What
went with it is `serious`, the mood that stopped the face smiling during a
refusal; the illustration keeps its calm expression through a blocked turn and
**nothing in the interface claims otherwise**. The blocked turn says in words
which rule stopped it and that nothing was done, which was always the
load-bearing half of that signal. Full accounting in
[13-name-and-voice.md](13-name-and-voice.md#what-was-lost-said-plainly).

The head crop fills every avatar slot from 32px up. The full figure appears
exactly once, in the landing page's `阿桥 是谁` section, which is the only place
with room to show the whole character. Swapping either is a file drop.

There is also one line of the mockup's copy that could not ship as written: the
mascot says *"王师傅，别担心，我会一步步带你完成报名的！"* — which is exactly the
false reassurance the persona forbids and `no_false_reassurance` blocks. The
mascot now says what 阿桥 actually does: *「条件我一条条念给你听。定不了的，我就说定
不了。」*

## What the mockup got right, and is now real

**Structured result cards.** `opportunity_search` used to render as a JSON blob
in a `<details>`. It now renders as the mockup's card: title with its id, a badge
(green when the record says free, otherwise the amount or the kind), the summary,
location / schedule / phone / deadline as an icon grid, and a footer with the
documents to bring and a **查看详情** that expands the published criteria with
their source references. `criteria_explain`, `gap_analysis`, `handoff_to_human`
and `document_prepare` have cards too.

Nothing in a card is composed by the interface — every field comes from the tool
result, so a card cannot show something the agent was not allowed to say.

**Technical detail folded away.** Tool calls, advisory findings and the trace now
collapse into one `系统运行详情 · N 步` per turn, off by default, with a global
toggle in the header for operators.

That is not a retreat from "the interface shows its own machinery". What changed
is *where*: **anything that altered the answer is in the answer**, written by the
agent in the person's own language — a blocked turn says which rule stopped it
and that nothing was done; an escalation says a human has been asked. Repeating
the finding beside it in developer English said the same thing twice, and a
repair that was successfully fixed is a correction the reader never needed to
see at all. So every finding goes into the collapsed section, and the answer
carries what matters.

**Suggested follow-ups.** Derived from which tools ran this turn — a small
deterministic table, no extra model call. A search offers 怎么报名？and 还有别的吗？;
a criteria readout offers 缺的材料怎么补？; a person is always one tap away.

**Intent as a badge, not a chooser.** Routing happens automatically and shows its
result as a centred pill (`已为你匹配：个人 · 机会路径`) with the method behind a
hover. The picker survives as a third row next to 身份 and 语言, so pinning is
still possible without four chips competing with the conversation.

## The overview panel

Tasks with owner, status and channel; the mascot; **档案记录** — every field held
about the person with the sentence they said it in, and a 清空记录 button; and
**授权**, four rows with a control to grant or withdraw each one. A promise that
consent can be withdrawn is worth nothing without a control for it, and it must
not require asking the agent nicely.

## Language

Everything the interface writes is in `web/static/i18n.js`: `STRINGS` for its own
labels, `TERMS` for server-side vocabulary rendered for a reader — intent
audiences, task statuses, service domains, consent scopes and the consent
questions. An unmapped value falls back to what the server sent, so a new term
shows up untranslated rather than blank.

The sentences the **agent** writes are localised in Go
(`internal/agent/messages.go`): budget stops, the blocked-answer message and its
reasons, the empty-answer fallback, the staged-rollout refusal. Those appear at
the worst moments of a turn, which is where being unreadable costs the most.

## The conversation list

The sidebar lists conversations, not sessions. The distinction matters because
the client creates a session on page load, before anybody has spoken, so a naive
listing showed one row per browser reload — see
[bugfix/2026-08-28-session-list.md](bugfix/2026-08-28-session-list.md).

The rules live in `store.SessionSummaries`, on the server, so that any client
asking `GET /api/sessions` gets the same answer:

- a session nobody has spoken in is not listed (it is still stored, and still
  readable by id — this hides, it does not delete);
- rows are ordered by last activity, so carrying on an old conversation brings
  it back to the top;
- each row carries the opening line as its title, clipped by rune, and a count
  of user turns — and no transcript, because the client refetches this list
  after every turn.

The client never labels a row with an internal id. A conversation opened with
nothing legible gets a said-so label in the reader's language. The list is capped
at 50 rows and says how many older ones are not shown, rather than truncating
silently. Clicking a row while an answer is streaming aborts that turn; the
server still finishes and persists it, so the answer is there when you go back.

## Theme

**Light unless somebody says otherwise.** Three states — light, dark, and
following the OS — and light is the default rather than the OS preference,
because the people this is built for open it on borrowed and public machines
whose OS setting is not theirs. Following the OS is now something a reader opts
into, not something they inherit from whoever used the terminal before them.

The part worth not undoing: **the default lives in CSS, not only in JavaScript.**

```css
:root, :root[data-theme="light"] { color-scheme: light; }   /* the default */
:root[data-theme="dark"]         { color-scheme: dark; }
:root[data-theme="system"]       { color-scheme: light dark; }
```

A theme decided only by a script paints the other one first on every load and
then corrects itself. So bare `:root` — what the document has before any script
runs, and what it keeps if a script never runs at all — is light, and the served
HTML carries no `data-theme` at all.

That forced one change in `applyTheme`. All three states are now stamped on
`<html>`, **including `system`**, which used to be the *absence* of the
attribute. That encoding worked only while `system` was also the default; with
light as the default, an unstamped document has to mean light. The auto-dark
block is therefore guarded by `:root[data-theme="system"]` rather than by
`:root:not([data-theme="light"])`, so the OS preference reaches only the readers
who asked for it.

`DEFAULT_THEME` in `app.js` and bare `:root` in `styles.css` are two statements
of one default. If you change one, change the other, or the first paint and the
script will disagree.

Existing readers keep whatever is in `localStorage["oba.theme"]`, `system`
included. This changed what a new or cleared browser gets, not what anybody had
already chosen.

> **Where this is in the history.** It was committed in `3d3df1b`, whose subject
> is *"Show the live results the panel was already being handed"* — an unrelated
> fix to the search panel. Two changes were in the working tree at once and went
> in together. `git log` on the theme code lands on a commit about search
> results, so this note is the pointer; `git log -S DEFAULT_THEME` finds it.

## The landing page

Until this, `/` **was** the app, and the app opens with a sign-in gate. So the
address on every poster, every forwarded link and every QR code resolved, for
anybody without an account, to an unlabelled username-and-password box. Nothing
on that screen said what 阿桥 is, what it will not do, or that an invite code is
what they are missing. The people this product is for are exactly the ones who
read an unexplained login form as *this is not for me*.

`/` is now a landing page and the app is at `/app`.

### The shape

Seven sections, in the order somebody decides with:

| | |
|---|---|
| Hero | the one-line claim, both buttons, and the bridge |
| 01 · a sample conversation | the product, rendered — routing badge, opportunity card, criteria as met / unsure / unmet, the collapsed trace |
| 02 · five steps | ask → find → read out the criteria → draft and file → follow up or hand over |
| 03 · four audiences | the four intents, in the reader's terms rather than the registry's |
| 04 · the boundary | **it decides no eligibility and scores nobody**, as a table of rule → how it holds → which guard fires |
| 05 · the interface | nine things the reader will actually see |
| 06 · who 阿桥 is | the name, the voice, and the full figure — the one place on either document that shows the whole character |
| 07 · honest limits | the invented corpus, the unwired `application_submit`, dialect-in-the-text-not-the-voice, the unconfigured live lookup |

Section 07 is the one worth defending. A product whose persona ships a check
against false reassurance (`no_false_reassurance`) does not get to oversell
itself on its own front page. The limits are on the page, above the final call
to action, in a warning-toned card — not in a footnote.

Section 04 is the differentiator, and it is deliberately the most detailed thing
on the page. Every other claim here is one an ordinary product could make.

### The redirect, and the escape hatch

A signed-in reader asked for the conversation, not the pitch, so the landing
page forwards them to `/app`. It has to *ask* — the sign-in cookie is HttpOnly,
so `GET /api/auth/me` is the only way this document can know — which means one
local round trip during which they see the top of the landing page. The
alternative, caching "signed in" in `localStorage` to redirect before paint, is
a value that goes stale the moment somebody signs out in another tab, and a
stale one sends a signed-**out** reader to a login form instead of to this page.
The brief flash is the cheaper mistake.

`/?stay` opts out of the forward. That is how the page stays reachable for
anybody who already has an account — including whoever is reviewing a change to
it. (Spelled `?stay=1` here until 2026-08-31; the code tests `has("stay")` and
never reads a value, and two spellings for one flag is how the wrong one gets
copied.)

**The way back is the sidebar brand, and it needs that opt-out.** `/app` used to
have no route to the landing page from either state: the gate covers the whole
shell, and the shell's own controls all stay inside `/app`. The gate now carries
a plain `返回首页` link, and in the shell the brand is an `<a href="/?stay">` —
the same element the landing header uses, so the mark behaves the same way on
both sides of signing in. The query string is not decoration: everybody pressing
that link is signed in by definition, so a plain `/` would be forwarded straight
back to `/app`, with no history entry to return through either. A fence asserts
both ends of that contract, because the two files never import each other. See
[bugfix/2026-08-31-the-gate-was-a-dead-end.md](bugfix/2026-08-31-the-gate-was-a-dead-end.md).

### What it shares with the app, and why the files moved

The landing page is a second document, and a second document is where a product
starts disagreeing with itself: a different blue, a different name for the same
thing, a face drawn two ways. So the shared parts were split out rather than
copied.

| File | Holds | Read by |
|---|---|---|
| `tokens.css` | every colour, radius, the type scale, all three themes | both |
| `avatar.css` | the ink for 阿桥's mark | both |
| `avatar.js` | the geometry of the mark | both |
| `icons.js` | the inlined glyphs | both |
| `i18n.js` | every user-visible string, both languages | both |
| `styles.css` | the app shell's layout | `app.html` |
| `home.css` | the landing page's layout | `index.html` |

`index.html` is the landing page; the conversational shell moved to `app.html`
and is served by name at `/app`, so the URL people bookmark and paste has no
`.html` in it.

### The design system

Two references, one per theme, and the palette in each was **sampled rather than
chosen**: the frames were grid-reduced and the cells sorted by saturation ×
lightness, so the hues in `tokens.css` are the ones actually in the pictures.

| | dark | light |
|---|---|---|
| reference | an iridescent soap bubble on near-black | a violet blob |
| hue | 34–42° champagne → amber → bronze, with a 213–220° blue refraction | 244–249° indigo-violet, ~25–30% saturation, one brighter lavender (`#9c81da`) |
| canvas | `#08080a` | `#f8f7fc` |
| filled surface | gold, carrying dark ink | violet, carrying white |

The violet is deliberately **desaturated**. An earlier pass used a vivid
neon violet across both themes and it fought everything around it; the restraint
is the character of the reference, not a compromise.

**Three jobs a colour can have, and why they are three tokens.** Collapsing them
is how a palette ends up with unreadable buttons, and the wrong one always has
the more obvious name:

- `--brand` is the **accent** — text, icons, hairlines. It must read against the
  canvas, so it is light on dark and dark on light. Putting white on it gives
  2.6:1.
- `--brand-fill` is the **primary action**, and it inverts with the theme exactly
  as the reference does: a white pill on the dark page, a near-black one on the
  light page. No hue, maximum contrast — 17.14:1 and 18.43:1.
- `--aurora-fill` is the **one filled colour surface**: the person's own message
  and the send button. Per-theme, because the two references disagree about what
  colour it is.

`TestFilledSurfacesUseTheFillTokenNotTheAccent` refuses `background: var(--brand)`
anywhere, so the obvious-but-wrong token cannot be reached for by accident.

**The words stay plain.** No gradient type, no coloured headings. All the colour
on the page comes from the picture, and that contrast is the whole composition —
tinting the headline flattens it. `--aur-*` are decorative only, and
`TestPaletteMeetsContrast` enforces that exemption rather than assuming it: use
one as a text colour and the fence tells you to add it to the checked pairs.

### The bubble, and two things that were wrong about it

It is layered CSS radial gradients, not a render — an iridescent form costs that
when you cannot ship a texture. Its colours and alphas are per-theme tokens.

- **It was clipped, and a clipped blur reads as a rendering fault.**
  `overflow: hidden` cut the glow dead against the next section. The reference
  gets away with a hard bottom edge because its whole page sits inside a rounded
  card, so the crop is obviously deliberate; full-bleed it just looks broken. It
  is now faded out with a `mask-image` before the section ends.
- **The animation repainted every frame and could not be seen.** It animated
  `border-radius`, which is imperceptible under an 11px blur and forces a full
  repaint of a 1180px filtered element sixty times a second. It animates
  `transform` only now: the blur is rasterised once and the compositor moves it.
  This audience is on cheap phones, and a hero animation that costs a repaint a
  frame is one that drains a battery to make a shape wobble nobody can see.

**A moving picture cannot be relied on for contrast.** The lede sat on the
bubble's bright core for part of the loop — a failure no static check catches,
because in any single frame it looks fine. `.hero-in::before` puts a soft scrim
between the two, keyed to the theme and off in light.

### Constraints it inherits

The same three the app has, and they are why it does not look like a landing
page usually looks:

- **Every byte comes from the binary.** No CDN, no web font, no icon font. The
  type is the reader's own system stack; the hero picture is inline SVG. This is
  the deployability claim — a service window on a closed network — and it is
  fenced by `TestLandingPageFetchesNothingFromTheNetwork`.
- **Nothing is hidden unless the script is known to be running.** Sections fade
  in on scroll, but the rule that hides them is guarded on `html.js`, stamped by
  an inline script in the head. A reader whose script was blocked or delayed
  gets a readable page rather than a column of empty space. Fenced by
  `TestLandingPageHidesNothingWithoutJavaScript`.
- **…and nothing is hidden from a reader that is never rendered.** This one was
  found on the live site and is worth writing down, because it looks like it
  cannot happen. `IntersectionObserver` callbacks are delivered from *update the
  rendering*, which a hidden document does not run — exactly like
  `requestAnimationFrame`. Measured on `jobs.heros-agent.space`: **zero observer
  callbacks in 800ms** at `visibilityState: "hidden"`, rAF silent alongside it,
  and all 53 sections sitting at `opacity: 0`. A prerender, a background tab, a
  print job or a crawler taking a link preview would have captured a blank page
  — for a page whose entire job is to be linked.

  The recovery hangs off `setTimeout`, the one timer that still runs in a hidden
  document. It also cannot simply add `.in`: that *starts a transition*, and a
  transition only advances while the document is rendering. Measured on the
  first attempt at the fix: 53/53 elements carried `.in` and the headline still
  computed to `opacity: 0`. So the recovery stamps `shown` on `<html>`, and
  `html.shown .reveal` states the final value outright with `transition: none`.
  Same fence.
- **Both languages, or neither.** A missing string does not throw — `t()` falls
  back to the key, so the headline renders as `home.hero.title`, in whichever
  language the author was not reading. Fenced by
  `TestLandingPageStringsExistInBothLanguages`.
- **The markup and the table say the same thing.** The page ships its Chinese as
  real text so it reads before any script runs, which means every Chinese string
  exists twice. Edit only the markup and the page looks right until somebody
  touches the language control, at which point `setLocale` sweeps `textContent`
  and the older wording snaps back over the newer one. Nothing throws; the page
  quietly un-edits itself. Fenced by
  `TestLandingPageMarkupAgreesWithTheChineseTable`.

Two more guard the system itself:

- `TestEveryTokenIsDefinedInEveryTheme` — every `var(--x)` any stylesheet reaches
  for resolves, and anything defined for one theme is defined for the other. A
  token dropped in a palette rewrite resolves to *nothing*, silently.
- `TestPaletteMeetsContrast` — every ink tier and semantic colour is measured
  against the surface it actually sits on, at 4.5:1. Comments carrying measured
  ratios do not re-measure themselves when somebody nudges a hex two shades
  brighter to make a mockup look better; this does. It has caught four real
  misses so far, including `--ink-400` at 4.24:1 and three light values that had
  been checked against `#ffffff` instead of against the canvas.

The routing itself is fenced by
`TestTheLandingPageAndTheAppAreBothServedSignedOut` in `internal/httpapi`: both
documents must be served, and both must be reachable without an account.

## Kept from before

Streaming SSE, the approval gate showing the exact arguments, the consent card,
plain-language / large-text / read-aloud, browser voice in and out, role
switching, session history, light and dark, and readability at 150% text size.
