// 阿桥 — the landing page's script.
//
// Deliberately small. The page is readable, themed and in the right language
// before this file runs; everything here is an improvement on a document that
// already works. That is not a style preference — this page is the first thing
// a stranger sees, often on a borrowed phone on a bad connection, and a landing
// page that renders nothing until a module arrives is a landing page that
// sometimes renders nothing.
//
// It shares the app's three vocabularies rather than restating them: strings
// from i18n.js, the mark from avatar.js, the glyphs from icons.js. A second
// copy of any of them is a second place for the product to disagree with itself.

import { t, setLocale, locale } from "/i18n.js";
import { mountLiquidForm } from "/liquid-form.js";
import { avatar, faviconDataURI } from "/avatar.js";
import { paintIcons } from "/icons.js";

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

function revealSections() {
  const items = $$(".reveal");
  // `shown` on <html> is what actually makes them visible here, not `.in`:
  // see home.css. `.in` goes on too so the two paths leave the same DOM.
  const showAll = () => {
    document.documentElement.classList.add("shown");
    items.forEach((el) => el.classList.add("in"));
  };
  try {
    if (!("IntersectionObserver" in window)) return showAll();
    const io = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (!e.isIntersecting) continue;
        e.target.classList.add("in");
        io.unobserve(e.target);
      }
    }, { rootMargin: "0px 0px -8% 0px", threshold: 0.05 });
    items.forEach((el) => io.observe(el));

    // A document that is not being rendered never runs "update the rendering",
    // and IntersectionObserver callbacks are delivered from there — exactly like
    // requestAnimationFrame. Measured on the live page: zero callbacks in 800ms
    // with document.visibilityState === "hidden", and rAF silent alongside it.
    // So a prerender, a background tab, a print job or a crawler taking a link
    // preview would capture this page with all 53 sections still at opacity 0 —
    // a column of empty space, for a page whose entire job is to be linked.
    //
    // setTimeout keeps running while hidden, which is why the recovery hangs off
    // it rather than off anything the renderer drives. A visible reader never
    // reaches it, so the animation is untouched for the people who can see it.
    if (document.hidden) {
      setTimeout(() => { if (document.hidden) showAll(); }, 900);
    }
  } catch {
    showAll();
  }
}

function boot() {
  // Same mark as the app's sidebar and the same face as the sign-in card, so
  // "what does 阿桥 look like" has one answer on both documents.
  const face = avatar("calm", "阿桥");
  for (const id of ["#navAvatar", "#voiceAvatar"]) {
    const el = $(id);
    if (el) el.innerHTML = face;
  }
  const chat = $("#chatAvatar");
  if (chat) chat.innerHTML = face;
  const icon = $("#favicon");
  if (icon) icon.href = faviconDataURI();
  // The voice section shows the same face at full size rather than a second
  // asset: one illustration, one agent.
  const big = $("#voiceArt");
  if (big) big.innerHTML = avatar("calm", "阿桥");

  paintIcons();

  applyLocale(localStorage.getItem("oba.locale") || "zh-CN");
  applyTheme(localStorage.getItem("oba.theme") || "light");
  bindControls();
  stickyHeader();
  paintBubble();
  loadDeploymentFacts();
  forwardIfSignedIn();
}

// The hero bubble is ThreeUI's Liquid Form shader. Everything about it -- the
// GL lifecycle, the palette, reduced motion, visibility gating -- lives in
// liquid-form.js; this only supplies the element it draws into.
function paintBubble() {
  const host = $(".bubble-wrap");
  const canvas = $(".bubble-gl");
  if (host && canvas) mountLiquidForm(host, canvas);
}

// ── what THIS instance actually has ────────────────────────────────────────
//
// The "honest limits" section makes two claims that are facts about a
// deployment rather than about the product, and it used to write both of them
// down by hand:
//
//   the size of the corpus   — said 21 while the answer was 26. The national
//                              layer added five records and the sentence did
//                              not move.
//   the live nationwide lookup — described only as "not configured", which
//                              stops being true the moment somebody configures
//                              it, and then the honesty section is the one part
//                              of the page that is lying.
//
// Both now come from /api/meta, which is the same place the app reads them for
// its own flags, so the page and the conversation cannot disagree.
//
// They are extra sentences rather than values interpolated into the claims,
// because the claims have to read correctly when this request does not arrive.
// A reader on a bad connection gets the limitation without the deployment
// detail. Nobody gets a stale 21, and nobody ever gets a stray "{records}".
// See docs/bugfix/2026-08-31-honest-limits-were-not-honest.md

let deployment = null;

function fact(id, text) {
  const el = $(id);
  if (!el) return;
  if (!text) { el.hidden = true; return; }
  el.textContent = text;
  el.hidden = false;
}

function renderDeploymentFacts() {
  if (!deployment) {
    fact("#corpusTally", "");
    fact("#liveStatus", "");
    fact("#speechVendor", "");
    return;
  }
  fact("#corpusTally", t("home.limits.l1count")
    .replace("{records}", deployment.records)
    .replace("{guides}", deployment.guides));
  fact("#liveStatus", t(deployment.live ? "home.limits.l4on" : "home.limits.l4off"));
  fact("#speechVendor", t(!deployment.speechVendor
    ? "home.feat.f6off"
    : deployment.speechTrains ? "home.feat.f6trained" : "home.feat.f6sent"));
}

function loadDeploymentFacts() {
  // /api/health, not /api/meta: this page's readers are not signed in, and the
  // sign-in gate's list of open paths is short on purpose. Both endpoints get
  // these three values from one producer in httpapi, so reading the public one
  // cannot disagree with what the conversation shows.
  fetch("/api/health", { headers: { accept: "application/json" } })
    .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
    .then((m) => {
      const records = Number(m.corpus_opportunities);
      const guides = Number(m.corpus_knowledge_docs);
      // A missing field arrives as NaN, and that is the only unusable case.
      //
      // ⚠️ ZERO IS NOT A SHAPE MISMATCH. This guard originally refused `guides <= 0`
      // as well, on the reasoning that a zero count meant a broken payload. Then
      // the twelve invented procedure guides left the product and zero became the
      // true answer — so the guard dropped ALL THREE deployment facts, including
      // the live-lookup line, and the section rendered with three blank slots.
      // A count of zero is a fact about a deployment; only an absent one is a bug.
      // See docs/bugfix/2026-08-31-the-invented-corpus-left-the-product.md
      if (!Number.isFinite(records) || !Number.isFinite(guides) || records < 1 || guides < 0) {
        console.warn("META_UNUSABLE: /api/meta carried no usable corpus counts; " +
          "the deployment facts under \"honest limits\" are omitted", m);
        return;
      }
      deployment = {
        records, guides,
        live: m.live_search_enabled === true,
        // Absent or non-boolean is treated as "there is a vendor", because the
        // sentence this picks is a privacy warning: guessing wrong towards
        // "nothing is sent" is the guess that misleads somebody.
        speechVendor: m.speech_vendor_enabled !== false,
        // Same direction: absent or non-boolean means "assume it trains". The
        // backbone is derived server-side, so this only ever guesses when
        // /api/health answered something unexpected.
        speechTrains: m.speech_vendor_trains_on_text !== false,
      };
      renderDeploymentFacts();
    })
    .catch((e) => {
      console.warn("META_UNAVAILABLE: could not read /api/meta; " +
        "the deployment facts under \"honest limits\" are omitted", e);
    });
}

// ── language ───────────────────────────────────────────────────────────────
//
// The same key the app writes, so a reader who picked English in the app and
// then followed a link back to the front page does not have to pick it again.

function applyLocale(l) {
  setLocale(l);
  for (const b of $$("[data-lang]")) b.classList.toggle("is-on", b.dataset.lang === locale());
  setThemeLabel();
  renderDeploymentFacts();
  document.title = `${t("app.name")} · Opportunity Bridge Agent`;
}

// ── theme ──────────────────────────────────────────────────────────────────
//
// One button that cycles rather than the app's <select>: the header has no room
// for a labelled control, and three states in a fixed order are something a
// button can carry honestly as long as it shows the state it is IN. It does —
// the label is the current theme, and aria-live announces the change.

const THEMES = ["light", "dark", "system"];

function applyTheme(choice) {
  const value = THEMES.includes(choice) ? choice : "light";
  document.documentElement.dataset.theme = value;
  localStorage.setItem("oba.theme", value);
  setThemeLabel();
}

// The label is the theme it is IN, not the one it will switch to. A control
// that names its own next state is a control you have to click to find out
// where you are.
function setThemeLabel() {
  const btn = $("#themeBtn");
  if (!btn) return;
  const now = document.documentElement.dataset.theme || "light";
  btn.textContent = t(`theme.${now}`);
  btn.title = t("ctl.theme");
}

function bindControls() {
  for (const b of $$("[data-lang]")) {
    b.addEventListener("click", () => {
      localStorage.setItem("oba.locale", b.dataset.lang);
      applyLocale(b.dataset.lang);
    });
  }
  const btn = $("#themeBtn");
  if (btn) {
    btn.addEventListener("click", () => {
      const now = document.documentElement.dataset.theme || "light";
      applyTheme(THEMES[(THEMES.indexOf(now) + 1) % THEMES.length]);
    });
  }
}

function stickyHeader() {
  const nav = $(".nav");
  if (!nav) return;
  const mark = () => nav.classList.toggle("is-stuck", window.scrollY > 8);
  mark();
  window.addEventListener("scroll", mark, { passive: true });
}

// ── the door ───────────────────────────────────────────────────────────────
//
// Somebody who is already signed in asked for the app, not the pitch — the
// bookmark that used to open the conversation still opens the conversation.
//
// The check has to be a request: the sign-in cookie is HttpOnly, so the only
// way this document can know is to ask. That means a signed-in reader sees the
// top of this page for the length of one local round trip. The alternative —
// caching "signed in" in localStorage to redirect before paint — is a value
// that goes stale the moment somebody signs out in another tab, and a stale one
// sends a signed-OUT reader to a login form instead of to this page. A brief
// flash for returning readers is the cheaper mistake.
//
// `?stay` opts out, which is how this page stays reachable (and reviewable) for
// the people who already have accounts.
function forwardIfSignedIn() {
  if (new URLSearchParams(location.search).has("stay")) return;
  fetch("/api/auth/me", { headers: { accept: "application/json" } })
    .then((r) => (r.ok ? r.json() : null))
    .then((me) => {
      // `/api/auth/me` answers {username, subject_id} — see meFor in auth.go.
      if (me && me.username) location.replace("/app");
    })
    .catch(() => { /* not signed in, or offline: the landing page is the answer either way */ });
}

// ── entry ──────────────────────────────────────────────────────────────────
//
// Last in the file, not first. `const` declarations are in the temporal dead
// zone until the module body reaches them, so calling boot() from the top threw
// on the first `THEMES` read and took the whole script with it — the page kept
// its pre-JS look and nothing was bound. Bottom of the file is the only place
// these two calls can safely be.
//
// revealSections() runs before boot() and inside its own try: `html.js .reveal`
// is hidden by CSS and only that observer puts it back, so if anything later in
// boot() throws, the reader still gets a readable page rather than a column of
// invisible sections.
revealSections();
boot();
