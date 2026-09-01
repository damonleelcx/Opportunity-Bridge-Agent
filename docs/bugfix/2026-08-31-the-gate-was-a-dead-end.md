# There was no way out of /app, at either end of signing in

**Found:** 2026-08-31, from a screenshot of `/app` as a first-time visitor sees it.
**Area:** the gate and the sidebar brand in `web/static/app.html`; `.gate`,
`.gate-home` and `.brand` in `web/static/styles.css`; `nav.backHome` in
`web/static/i18n.js`; the `?stay` contract with `forwardIfSignedIn` in
`web/static/home.js`.
**Status:** fixed — three defects, one on top of the next.

## What it looked like

`/app` opens on the sign-in gate. The gate is `position: fixed; inset: 0` and
covers the entire shell — the sidebar, the brand, the language control, every
other link in the product. What is left on screen is the card: username,
password, 登录, 去注册, 忘了密码, and the language pair. None of those leads
anywhere but deeper into the form.

Signing in did not help. The shell has a brand, a conversation list, a composer
and an overview panel, and every one of them stays inside `/app`.

So the product had no route back to `/` from either state — the page that
explains what this is was reachable only by typing the address.

## Why

The landing page links **into** the app in three places (`开始对话`, `进入对话`),
and both screens were built as the app's own first view rather than as somewhere
a person might arrive without context. Inside the shell the brand and the sidebar
are always present, so "how do I get out of here" never came up while the shell
was what was being looked at — and the gate is exactly the state where the shell
is not there.

## Why it mattered more than a missing link

The browser's Back button is the answer only for people who have one and know
it. This is built for a service window: a kiosk in kiosk mode has no chrome, and
the people this is for are the ones least likely to reach for a browser control
rather than something on the page. A pasted link lands on the gate with no
history to go back to at all — Back is disabled there, so for that visitor the
first screen of the product was a password box with no explanation and no exit.

## The fix, in three parts

### 1. A link out of the gate

One anchor in the gate card, above the language pair:

```html
<a id="gateHome" class="link gate-switch gate-home" href="/" data-i18n="nav.backHome">返回首页</a>
```

Three properties it has to keep, each of which fails silently if lost:

- **A plain `<a href="/">`, not a scripted button.** The gate is painted from
  markup before `app.js` runs; a click handler would make the escape hatch
  depend on the script whose failure is one of the reasons somebody is stuck.
- **Untouched by `setGateMode`.** The form switches through four modes —
  `signin`, `signup`, `reset`, `newpass` — and the link is correct in all four,
  so it is stated once in markup rather than re-set per mode.
- **Muted (`--ink-500`), not brand-coloured.** 去注册 and 忘了密码 are what most
  people on this screen actually need. Three equally loud links turn a form into
  a decision. The rule sits **after** `.link` in `styles.css`: both are a single
  class, so only source order makes the muted colour win.

The arrow is drawn by `.gate-home::before`, not carried in the string, so a
translation only ever supplies words.

### 2. The gate can now be scrolled

`.gate` was `overflow: visible` with `align-items: center`. A centred flex item
that outgrows its container overflows in **both** directions, and the part above
the top edge cannot be scrolled to — no scroll position shows it. In 注册 mode the
card is ~886px (username, password, email + hint, invite + hint), so on any
viewport below roughly 750px the bottom of the card was cut off with nothing able
to reach it. That already included the language pair, before this change added a
row; a phone in landscape and a 13" laptop with 大字号 on both land there.

```css
align-items: center;       /* fallback: dropped by browsers that know `safe` */
align-items: safe center;
overflow-y: auto;
```

`safe center` behaves as `start` exactly when centring would overflow, which is
what turns lost overflow into scrollable overflow. Both halves are load-bearing
and each is silent on its own: `overflow-y` without `safe` scrolls to a top that
is still clipped; `safe` without `overflow-y` aligns correctly to content that
still cannot be reached.

### 3. The brand is the way out of the signed-in shell

`<div class="brand">` became `<a class="brand" href="/?stay">`, which is the same
element the landing page's own header already uses. The mark now behaves the same
way on both sides of signing in, rather than being a link on one page and dead on
the other. As an anchor it needs `text-decoration: none` (or the product name and
the wordmark under it render underlined), a `:focus-visible` ring (it is now the
first thing Tab reaches in the shell, and a keyboard user gets no pointer cursor),
and a hover underline on the name — a mark that reacts to nothing reads as
decoration, and the whole point is that somebody notices it is a link.

**`?stay` is the part that makes it work at all**, and it was only found by
clicking the finished link while actually signed in. `forwardIfSignedIn` in
`home.js` sends anyone with a session straight from `/` to `/app`, which is right
for a bookmark and wrong for this link: a plain `href="/"` round-trips and lands
the person back exactly where they started. Because the forward uses
`location.replace`, it leaves no history entry either, so Back does not rescue
them. `?stay` is that function's own documented opt-out — and everyone who
presses this link is by definition signed in, so the plain form would have been
dead for **every** person who has it, while looking correct in the markup, in
review, and in any signed-out browser.

The gate's link stays a plain `/`: nobody looking at the gate has a session, and
in the one case where there might be (a transient `/api/auth/me` failure painting
the gate over a live session) being carried into the app is the right answer.

## Verification

Fences in `web/interface_test.go`, each drilled with `-count=1` to confirm it
goes red:

- `TestGateOffersAWayBackToTheLandingPage` — the anchor exists inside the gate
  form, points at `/`, carries its i18n key, is not `hidden`, and `nav.backHome`
  exists in both language tables.
- `TestTheGateCanBeScrolledWhenItOutgrowsTheViewport` — `.gate` declares
  `overflow-y: auto` **and** `safe center`, with the plain `center` before it.
  Removing either, or reordering them, fails.
- `TestTheBrandLeadsHomeFromInsideTheApp` — the sidebar `.brand` is an `<a>`,
  points at `/?stay`, carries a translated tooltip, clears the underline and has
  a focus ring — **and** `home.js` still honours `?stay`. That last one is a
  contract between two files that never import each other: drop the opt-out and
  the brand silently becomes a no-op again.

Both fences that read markup strip HTML comments first. The comments here quote
the very elements the fences look for, and a fence that reads its own
justification as evidence proves nothing.

Browser, against the scripted backend with `OBA_INVITE_CODES` set:

- The gate link renders in both languages and both themes, is present in
  `signin` and `signup` modes, and lands on `/`.
- At 900×620 in 注册 mode the gate scrolls: `scrollTop` reaches the language pair
  and returns to a fully visible top of the card. Before, both ends were lost.
- Signed in, the brand tooltips 返回首页, takes keyboard focus with a visible ring,
  underlines the name on hover, and clicking it lands on `/?stay` with the
  landing hero rendered **and the session still live** (`/api/auth/me` still
  answers `drilluser2`). 进入对话 returns to the shell, still signed in.
