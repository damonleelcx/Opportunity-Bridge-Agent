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

  paintIcons();

  applyLocale(localStorage.getItem("oba.locale") || "zh-CN");
  applyTheme(localStorage.getItem("oba.theme") || "light");
  bindControls();
  stickyHeader();
  forwardIfSignedIn();
}

// ── language ───────────────────────────────────────────────────────────────
//
// The same key the app writes, so a reader who picked English in the app and
// then followed a link back to the front page does not have to pick it again.

function applyLocale(l) {
  setLocale(l);
  for (const b of $$("[data-lang]")) b.classList.toggle("is-on", b.dataset.lang === locale());
  setThemeLabel();
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
