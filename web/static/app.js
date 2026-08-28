// 阿桥 — conversational interface.
//
// Built to the UX Pilot mockup's layout and visual language, with two
// deliberate departures, both about what this thing has to survive:
//
//   1. No CDN. The mockup loads Tailwind, Font Awesome and Google Fonts over the
//      network. This binary serves every byte it needs, because a service window
//      on a closed network is a real deployment target.
//   2. The mascot is the inline 阿桥 mark, not a hosted illustration. It keeps
//      the mood states — in particular `serious`, which stops it smiling while
//      the agent is refusing — and it adds no external dependency. Dropping an
//      image into web/static/ and pointing at it is a one-line change.
//
// The interface still shows its machinery, but no longer in the way: tool calls,
// advisory findings and the trace fold into one collapsed "系统运行详情" per turn,
// while anything that changed the ANSWER — a repair, a block, an escalation —
// stays visible, because that is not detail, that is the answer.

import { t, term, setLocale, locale, QUICKSTARTS } from "/i18n.js";
import { avatar, setMood, faviconDataURI } from "/avatar.js";
import { icon, paintIcons } from "/icons.js";

const $ = (s) => document.querySelector(s);

const state = {
  session: null,
  meta: null,
  intents: [],
  pinnedIntent: "",
  busy: false,
  abort: null, // AbortController for the turn in flight, so switching conversation can end it
  speak: false,
  showTech: false,
  account: null, // who is signed in; null until /api/auth/me answers
  gateMode: "signin",
};

// ── boot ───────────────────────────────────────────────────────────────────

async function boot() {
  setLocale(localStorage.getItem("oba.locale") || "zh-CN");
  $("#locale").value = locale();
  $("#favicon").href = faviconDataURI();
  paintIcons();
  applyTheme(localStorage.getItem("oba.theme") || "system");
  brandMood("calm");

  wireGate();
  // Who is asking has to be settled before anything else is fetched: every
  // other endpoint answers 401 without it, and a shell that loads and then
  // fails five requests reads as broken rather than as locked.
  state.account = await currentAccount();
  if (!state.account) {
    showGate();
    return;
  }

  try {
    state.meta = await api("GET", "/api/meta");
  } catch {
    status(t("error.network"), "failed");
    return;
  }
  $("#sampleFlag").title = t("banner.sampleTitle2");
  $("#sampleFlag").hidden = !state.meta.corpus_is_sample;
  // A lookup that cannot run must not read as an absence of opportunities.
  $("#liveFlag").title = t("banner.liveOffTitle");
  $("#liveFlag").hidden = state.meta.live_search_enabled !== false;
  paintWho();
  buildRoleSelect();
  wire();
  await newSession($("#role").value);
  await refreshSessions();
}

function buildRoleSelect() {
  const sel = $("#role");
  sel.innerHTML = "";
  for (const role of state.meta.roles) {
    const o = document.createElement("option");
    o.value = role;
    o.textContent = t(`role.${role}`);
    sel.append(o);
  }
}

function wire() {
  $("#locale").addEventListener("change", async (e) => {
    setLocale(e.target.value);
    localStorage.setItem("oba.locale", e.target.value);
    // The gate's submit and switch labels are set in code, so setLocale's sweep
    // over [data-i18n] does not reach them.
    if (!$("#gate").hidden) setGateMode(state.gateMode);
    paintIcons();
    buildRoleSelect();
    brandMood("calm");
    await loadIntents();
    await renderOverview();
    await refreshSessions();
    if (state.session) state.session.locale = e.target.value;
  });
  $("#role").addEventListener("change", (e) => newSession(e.target.value));
  $("#newSession").addEventListener("click", () => newSession($("#role").value));
  $("#intentPick").addEventListener("change", (e) => { state.pinnedIntent = e.target.value; });

  $("#composer").addEventListener("submit", (e) => { e.preventDefault(); send($("#input").value); });
  const input = $("#input");
  input.addEventListener("input", () => {
    input.style.height = "auto";
    input.style.height = Math.min(input.scrollHeight, 160) + "px";
  });
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(input.value); }
  });

  $("#a11yLarge").addEventListener("change", (e) =>
    document.body.classList.toggle("large-text", e.target.checked));
  $("#a11yVoice").addEventListener("change", (e) => {
    state.speak = e.target.checked;
    if (!e.target.checked) window.speechSynthesis?.cancel();
  });
  // Plain language is not a client-side rewrite: it goes through the same
  // accessibility_set path the agent itself uses, so the answer actually changes.
  $("#a11yPlain").addEventListener("change", (e) => {
    if (e.target.checked) send(locale() === "en"
      ? "Please answer in plain words from now on: short sentences, no jargon."
      : "以后请用大白话回答我：句子短一点，不要术语。");
  });

  $("#theme").addEventListener("change", (e) => applyTheme(e.target.value));
  $("#mic").addEventListener("click", startDictation);
  $("#forget").addEventListener("click", forgetProfile);

  $("#techToggle").addEventListener("click", () => {
    state.showTech = !state.showTech;
    $("#techToggle").setAttribute("aria-pressed", String(state.showTech));
    for (const d of document.querySelectorAll("details.tech")) d.open = state.showTech;
  });
}

// ── sessions ───────────────────────────────────────────────────────────────

// Who the conversations belong to comes from the ACCOUNT now.
//
// This used to be an id kept in localStorage, because a page load minted a new
// subject and discarded the profile, the consents and every tracked task. The
// account supersedes it: the server takes the subject from whoever is signed in
// and ignores anything the client says about it, which is also what stops one
// person opening a conversation onto another person's record.
// See docs/bugfix/2026-08-28-data-exposure-no-ownership-checks.md
async function newSession(role) {
  abortTurn();
  state.session = await api("POST", "/api/sessions", { role, locale: locale() });
  state.pinnedIntent = "";
  $("#transcript").innerHTML = "";
  crumb(null);
  greeting();
  await loadIntents();
  await renderOverview();
  await refreshSessions();
}

function greeting() {
  const turn = agentTurn();
  turn.typing = false;
  turn.bubble.textContent = t("greeting");
  turn.bubble.classList.add("bubble-greeting");
  renderSuggestions(turn, []);
}

async function loadIntents() {
  state.intents = await api("GET", `/api/intents?role=${state.session.role}`);
  const sel = $("#intentPick");
  sel.innerHTML = "";
  const auto = document.createElement("option");
  auto.value = "";
  auto.textContent = t("intent.auto");
  sel.append(auto);
  for (const it of state.intents) {
    const o = document.createElement("option");
    o.value = it.id;
    o.textContent = intentLabel(it.id);
    o.disabled = !it.enabled;
    o.title = it.goal;
    sel.append(o);
  }
  sel.value = state.pinnedIntent;
}

const INTENT_LABELS = {
  "zh-CN": {
    individual_pathway: "个人 · 机会路径",
    low_access_support: "高摩擦人群 · 降低门槛",
    service_orchestration: "服务机构 · 任务编排",
    supply_demand_insight: "社会 · 供需断点",
  },
  en: {
    individual_pathway: "Personal · opportunity pathway",
    low_access_support: "High-friction · lower the barrier",
    service_orchestration: "Service org · orchestration",
    supply_demand_insight: "Society · supply-demand gaps",
  },
};
const intentLabel = (id) => (INTENT_LABELS[locale()] || INTENT_LABELS["zh-CN"])[id] || id;

// The list is capped so that a long-running kiosk does not render hundreds of
// rows, but the cap is stated rather than applied silently: SESSION_LIST_MAX
// rows plus a line saying how many older ones are not shown.
const SESSION_LIST_MAX = 50;

async function refreshSessions() {
  const list = await api("GET", "/api/sessions");
  const box = $("#sessions");
  const shown = list.slice(0, SESSION_LIST_MAX);

  // Re-render only when something actually changed. refreshSessions runs after
  // every turn, and rebuilding the DOM unconditionally threw away keyboard
  // focus and the sidebar's scroll position each time.
  // The locale is part of the signature: the list carries localised strings
  // ("untitled", "N older not shown"), so switching language has to repaint it.
  const sig = JSON.stringify([locale(), state.session?.id, shown.map((s) => [s.id, s.title, s.turns]), list.length]);
  if (box.dataset.sig === sig) return;
  box.dataset.sig = sig;

  box.innerHTML = "";
  for (const s of shown) {
    // A conversation whose opening message was only punctuation or whitespace
    // still exists; it gets a said-so label rather than a raw internal id,
    // which told the reader nothing.
    const label = s.title || t("sessions.untitled");
    const b = document.createElement("button");
    b.type = "button";
    b.className = "session" + (s.id === state.session?.id ? " is-active" : "");
    if (s.id === state.session?.id) b.setAttribute("aria-current", "page");
    // The row ellipsises in CSS, so the full opening line lives in the tooltip
    // instead of being cut to a fixed character count with no way to read it.
    b.title = label;
    b.innerHTML = `<span class="ico">${icon("chat")}</span><span class="session-label">${esc(label)}</span>`;
    b.addEventListener("click", () => openSession(s.id));
    box.append(b);
  }
  if (list.length > shown.length) {
    const more = document.createElement("p");
    more.className = "session-more";
    more.textContent = t("sessions.more").replace("{n}", list.length - shown.length);
    box.append(more);
  }
  if (!list.length) {
    const empty = document.createElement("p");
    empty.className = "session-more";
    empty.textContent = t("sessions.empty");
    box.append(empty);
  }
}

async function openSession(id) {
  if (id === state.session?.id && !state.busy) return;
  // Switching away mid-answer used to leave the turn streaming into a transcript
  // that had already been replaced: the answer was written into detached nodes
  // and vanished with no explanation. Abort it instead. The server finishes and
  // persists the turn regardless, so coming back shows the answer.
  abortTurn();
  const d = await api("GET", `/api/sessions/${id}`);
  state.session = d.session;
  $("#role").value = d.session.role;
  $("#transcript").innerHTML = "";
  greeting();
  for (const turn of d.session.history || []) {
    if (turn.role === "user") userTurn(turn.text);
    else agentTurn().bubble.textContent = turn.text;
  }
  crumb(d.session.intent || null);
  await loadIntents();
  await renderOverview();
  await refreshSessions();
  scroll();
}

// ── a turn ─────────────────────────────────────────────────────────────────
//
// The whole turn is scaffolded up front with its slots in a fixed order, so
// that tool results — which arrive BEFORE the answer text — still render below
// the bubble, the way somebody reads it.

function userTurn(text) {
  const el = document.createElement("div");
  el.className = "turn turn-user";
  el.innerHTML =
    `<div class="turn-avatar user">${icon("user")}</div>` +
    `<div class="turn-body"><div class="bubble bubble-user"></div></div>`;
  el.querySelector(".bubble").textContent = text;
  $("#transcript").append(el);
  scroll();
}

function agentTurn() {
  const g = document.createElement("div");
  g.className = "turn-group";
  g.innerHTML = `
    <div class="route-pill" hidden></div>
    <div class="thinking" hidden></div>
    <div class="turn">
      <div class="turn-avatar"></div>
      <div class="turn-body"><div class="bubble bubble-agent"></div></div>
    </div>
    <div class="results" hidden></div>
    <div class="notices" hidden></div>
    <div class="decides" hidden></div>
    <div class="suggest" hidden></div>
    <details class="tech" hidden><summary></summary><div class="tech-body"></div></details>`;
  const turn = {
    root: g,
    pill: g.querySelector(".route-pill"),
    thinking: g.querySelector(".thinking"),
    slot: g.querySelector(".turn-avatar"),
    bubble: g.querySelector(".bubble"),
    results: g.querySelector(".results"),
    notices: g.querySelector(".notices"),
    decides: g.querySelector(".decides"),
    suggest: g.querySelector(".suggest"),
    tech: g.querySelector("details.tech"),
    techBody: g.querySelector(".tech-body"),
    techCount: 0,
  };
  turn.slot.innerHTML = avatar("calm", t("a11y.avatar"));
  // The bubble starts as a typing indicator rather than as empty space: the
  // model can think for a long time before the first token, and an empty grey
  // box reads as a broken answer.
  turn.bubble.innerHTML = '<span class="typing" aria-label="…"><i></i><i></i><i></i></span>';
  turn.typing = true;
  turn.tech.open = state.showTech;
  $("#transcript").append(g);
  scroll();
  return turn;
}

const show = (el) => { el.hidden = false; };

// ── sending ────────────────────────────────────────────────────────────────

// abortTurn ends the turn in flight, if any. Called when the reader navigates
// away from the conversation that turn belongs to.
function abortTurn() {
  if (!state.abort) return;
  state.abort.abort();
  state.abort = null;
}

async function send(text) {
  text = (text || "").trim();
  if (!text || state.busy || !state.session) return;
  // The turn belongs to this conversation. Everything below writes to the
  // screen only while that is still the conversation on screen.
  const sid = state.session.id;
  const ctl = new AbortController();
  state.abort = ctl;
  $("#input").value = "";
  $("#input").style.height = "auto";
  for (const s of document.querySelectorAll(".suggest")) { s.innerHTML = ""; s.hidden = true; }

  userTurn(text);
  const turn = agentTurn();
  setBusy(true);
  status(t("status.thinking"), "busy");
  let streamed = "";

  try {
    const res = await fetch(`/api/sessions/${state.session.id}/messages`, {
      method: "POST",
      signal: ctl.signal,
      headers: { "Content-Type": "application/json" },
      // The locale rides on every message: choosing a language in the sidebar
      // must change the next answer, not the next conversation.
      body: JSON.stringify({ message: text, intent: state.pinnedIntent, locale: locale() }),
    });
    if (!res.ok) {
      const e = await res.json().catch(() => ({ message: res.statusText }));
      notice(turn, "block", `${e.code || t("status.failed")}`, e.message || "", e.remedy);
      return;
    }
    for await (const ev of sse(res.body)) {
      switch (ev.kind) {
        case "routed": routePill(turn, ev.route); crumb(ev.route.intent); break;
        case "thinking": show(turn.thinking); turn.thinking.textContent = clip(turn.thinking.textContent + ev.text, 320); scroll(); break;
        case "text":
          streamed += ev.text;
          turn.typing = false;
          turn.bubble.textContent = streamed;
          scroll();
          break;
        case "tool_start":
          status(t("status.working"), "busy");
          // Rendered unconditionally. This used to be guarded on ev.args, which
          // worked only because the stream carried a second, argument-less
          // tool_start for every call and this was how they were told apart.
          // With that duplicate gone the guard would mean something else
          // entirely: a tool invoked with no arguments would vanish from the
          // trace without a word. A call with no arguments still happened.
          // See docs/bugfix/2026-08-28-duplicate-tool-start-events.md
          techItem(turn, `→ ${term("tool", ev.tool, ev.tool)} · ${ev.tool}`, ev.args ?? {});
          break;
        case "tool_result": renderToolResult(turn, ev); break;
        case "guardrail": case "verify": finding(turn, ev.finding); break;
        case "approval": approvalCard(turn, ev.approval); break;
        case "consent": consentCard(turn, ev.consent); break;
        case "trace": traceRow(turn, ev.trace); break;
        case "final": finalise(turn, ev.final, streamed); break;
        case "error": notice(turn, "block", t("status.failed"), ev.text); break;
      }
    }
  } catch (e) {
    // An abort is the reader leaving this conversation, not a failure. Reporting
    // it would put an error into a transcript nobody is looking at any more.
    if (e?.name !== "AbortError") {
      notice(turn, "block", t("status.failed"), String(e?.message || e));
      if (turn.typing) { turn.typing = false; turn.bubble.textContent = ""; }
    }
  } finally {
    if (state.abort === ctl) state.abort = null;
    setBusy(false);
    status("");
    // Only repaint if this is still the conversation on screen; otherwise the
    // panels would be redrawn for the one the reader just left.
    if (state.session?.id === sid) {
      await renderOverview();
      await refreshSessions();
    }
  }
}

function finalise(turn, final, streamed) {
  // The final answer is authoritative. A redraft or a blocked delivery means the
  // streamed text is NOT what the person should be left with, so it is replaced
  // rather than left on screen next to a correction.
  if (final.answer && final.answer !== streamed) turn.bubble.textContent = final.answer;
  if (turn.typing) {
    // No text ever arrived. Say so rather than leaving the dots blinking for ever.
    turn.typing = false;
    turn.bubble.textContent = final.answer || t("status.noAnswer");
    turn.bubble.classList.add("is-blocked");
  }
  if (final.stop_reason && final.stop_reason !== "answered") {
    turn.bubble.classList.add("is-blocked");
    setMood(turn.slot, "serious", t("a11y.avatar"));
  }
  if (turn.techCount) {
    show(turn.tech);
    turn.tech.querySelector("summary").textContent =
      `${t("tech.title")} · ${turn.techCount} ${t("tech.steps")}`;
  }
  renderSuggestions(turn, final.tool_calls || []);
  if (state.speak && final.answer) speak(final.answer);
  scroll();
}

// ── result cards ───────────────────────────────────────────────────────────

function renderToolResult(turn, ev) {
  techItem(turn, `← ${term("tool", ev.tool, ev.tool)} · ${ev.tool}${ev.is_error ? " ✗" : ""}`,
           ev.result, ev.is_error);
  if (ev.is_error || !ev.result) return;
  const card = cardFor(ev.tool, ev.result);
  if (!card) return;
  show(turn.results);
  turn.results.append(card);
  scroll();
}

function cardFor(tool, r) {
  switch (tool) {
    case "opportunity_search": return opportunityList(r);
    case "criteria_explain": return criteriaCard(r);
    case "gap_analysis": return gapCard(r);
    case "handoff_to_human": return handoffCard(r);
    case "document_prepare": return draftCard(r);
    default: return null;
  }
}

function el(html) {
  const d = document.createElement("div");
  d.innerHTML = html.trim();
  return d.firstElementChild;
}

function opportunityList(r) {
  const wrap = document.createElement("div");
  wrap.style.display = "contents";
  if (!r.results?.length) {
    wrap.append(el(`<div class="notice notice-advisory" style="margin-left:0">
      <span class="notice-title">${esc(t("card.nothing"))}</span>
      ${esc((r.cities_covered || []).join("、"))}</div>`));
    return wrap;
  }
  for (const o of r.results) wrap.append(opportunityCard(o));
  return wrap;
}

function opportunityCard(o) {
  const money = o.amount || (o.salary_min ? `¥${fmt(o.salary_min)}–${fmt(o.salary_max)}` : "");
  const free = /免|free/i.test(o.amount || "");
  const badge = free
    ? `<span class="pill pill-free">${esc(t("card.free"))}</span>`
    : money
      ? `<span class="pill pill-pay">${esc(clip(money, 26))}</span>`
      : `<span class="pill">${esc(t("kind." + o.kind))}</span>`;

  const where = o.channel?.window || [o.district, o.city].filter(Boolean).join(" · ");
  const when = o.schedule || o.channel?.hours || "";
  const facts = [
    where && ["pin", where],
    when && ["calendar", when],
    o.channel?.phone && ["phone", o.channel.phone],
    o.deadline && ["alert", o.deadline],
  ].filter(Boolean);

  const needs = [...new Set((o.criteria || []).map((c) => c.evidence).filter(Boolean))];
  const card = el(`
    <article class="ocard">
      <div class="ocard-body">
        <div class="ocard-top">
          <h3 class="ocard-title">${esc(o.title)}<span class="ocard-id">${esc(o.id)}</span></h3>
          ${badge}
        </div>
        <p class="ocard-sum">${esc(o.summary || "")}</p>
        <div class="ocard-facts">
          ${facts.map(([i, v]) => `<div class="fact"><span class="ico">${icon(i)}</span><span>${esc(v)}</span></div>`).join("")}
        </div>
      </div>
      <div class="ocard-foot">
        <span class="ocard-need">${esc(needs.length ? t("card.need") + needs.join("、") : t("card.needNone"))}</span>
        <button class="ocard-more" type="button">${esc(t("card.detail"))}<span class="ico">${icon("arrow")}</span></button>
      </div>
      <div class="ocard-detail" hidden></div>
    </article>`);

  const detail = card.querySelector(".ocard-detail");
  detail.innerHTML =
    `<p class="ocard-need" style="padding-top:12px">${esc(o.org || "")} · ${esc(o.source_ref)}</p>` +
    (o.criteria || []).map((c) => `
      <div class="crit">
        <span class="crit-status status-unknown">${esc(c.code)}</span>
        <span class="crit-text">${esc(c.text)}
          ${c.evidence ? `<span class="crit-proof">${esc(t("crit.proof") + c.evidence)}</span>` : ""}</span>
      </div>`).join("");

  const btn = card.querySelector(".ocard-more");
  btn.addEventListener("click", () => {
    detail.hidden = !detail.hidden;
    btn.firstChild.textContent = detail.hidden ? t("card.detail") : t("card.detailHide");
  });
  return card;
}

function criteriaCard(r) {
  const card = el(`
    <article class="ocard">
      <div class="ocard-body">
        <div class="ocard-top">
          <h3 class="ocard-title">${esc(t("crit.title"))}<span class="ocard-id">${esc(r.opportunity_id)}</span></h3>
        </div>
        <div class="crit-list"></div>
        <div class="decision-note">${esc(r.decision_note || "")}</div>
      </div>
    </article>`);
  card.querySelector(".crit-list").innerHTML = (r.checks || []).map((c) => `
    <div class="crit">
      <span class="crit-status status-${esc(c.status)}">${esc(t("crit." + c.status) || c.status)}</span>
      <span class="crit-text">${esc(c.criterion)}
        ${c.proof_document ? `<span class="crit-proof">${esc(t("crit.proof") + c.proof_document)}</span>` : ""}</span>
    </div>`).join("");
  return card;
}

function gapCard(r) {
  const rows = (r.rows || []).slice(0, 8);
  const card = el(`
    <article class="ocard">
      <div class="ocard-body">
        <div class="ocard-top">
          <h3 class="ocard-title">${esc(t("gap.title"))}<span class="ocard-id">${esc(r.group_by)}</span></h3>
          <span class="pill">${esc(t("gap.coverage"))} ${esc(String(r.consent_coverage_pct))}%</span>
        </div>
        <div style="overflow-x:auto">
          <table class="gap-table">
            <thead><tr><th>${esc(t("gap.group"))}</th><th>${esc(t("gap.records"))}</th><th>${esc(t("gap.unmet"))}</th></tr></thead>
            <tbody>${rows.map((x) => `<tr><td>${esc(x.group)}</td><td>${esc(String(x.records))}</td><td>${esc(String(x.unmet_rate))}%</td></tr>`).join("")}</tbody>
          </table>
        </div>
        <div class="decision-note">${esc(t("gap.suppressed"))}: ${esc(String(r.suppressed_cells))} · ${esc(r.note || "")}</div>
      </div>
    </article>`);
  return card;
}

function handoffCard(r) {
  const c = r.channel || {};
  return el(`
    <article class="ocard">
      <div class="ocard-body">
        <div class="ocard-top">
          <h3 class="ocard-title">${esc(t("handoff.title"))}</h3>
          <span class="pill pill-pay">${esc(r.urgency || "")}</span>
        </div>
        <div class="ocard-facts">
          ${c.phone ? `<div class="fact"><span class="ico">${icon("phone")}</span><span>${esc(c.phone)}</span></div>` : ""}
          ${c.window ? `<div class="fact"><span class="ico">${icon("pin")}</span><span>${esc(c.window)}</span></div>` : ""}
          ${c.hours ? `<div class="fact"><span class="ico">${icon("calendar")}</span><span>${esc(c.hours)}</span></div>` : ""}
        </div>
      </div>
    </article>`);
}

function draftCard(r) {
  const missing = (r.missing_fields || []).join("、");
  const card = el(`
    <article class="ocard">
      <div class="ocard-body">
        <div class="ocard-top"><h3 class="ocard-title">${esc(t("draft.title"))}</h3></div>
        <pre style="margin:0;white-space:pre-wrap;font-family:var(--mono);font-size:.78em;
             background:var(--surface-2);padding:12px;border-radius:8px;overflow-x:auto"></pre>
        ${missing ? `<div class="decision-note">${esc(t("draft.missing") + missing)}</div>` : ""}
      </div>
    </article>`);
  card.querySelector("pre").textContent = r.draft || "";
  return card;
}

const fmt = (n) => (n ? String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ",") : "");

// ── suggestions ────────────────────────────────────────────────────────────
//
// Derived from which tools actually ran this turn — a small deterministic table,
// no extra model call. A suggestion that does not follow from what just happened
// is a suggestion nobody taps twice.

function renderSuggestions(turn, toolCalls) {
  const ran = new Set(toolCalls.filter((c) => !c.error).map((c) => c.name));
  const out = [];
  if (ran.has("opportunity_search")) out.push("suggest.howApply", "suggest.other");
  if (ran.has("criteria_explain")) out.push("suggest.missingDocs");
  if (ran.has("case_task_create")) out.push("suggest.next");
  if (ran.has("gap_analysis")) out.push("suggest.byCohort", "suggest.dropoff");
  if (!out.length) {
    const id = state.pinnedIntent || state.intents[0]?.id;
    for (const p of ((QUICKSTARTS[id] || {})[locale()] || []).slice(0, 2)) out.push({ raw: p });
  }
  if (!ran.has("handoff_to_human")) out.push("suggest.human");

  turn.suggest.innerHTML = "";
  for (const item of out.slice(0, 3)) {
    const label = typeof item === "string" ? t(item) : item.raw;
    const b = document.createElement("button");
    b.type = "button";
    b.textContent = clip(label, 42);
    b.title = label;
    b.addEventListener("click", () => send(label));
    turn.suggest.append(b);
  }
  if (turn.suggest.children.length) show(turn.suggest);
}

// ── notices, decisions, technical detail ───────────────────────────────────

function routePill(turn, route) {
  show(turn.pill);
  const method = t(`turn.method.${route.method}`) || route.method;
  turn.pill.innerHTML =
    `<span><span class="ico">${icon("route")}</span>${esc(t("route.matched") + intentLabel(route.intent))}` +
    `<button type="button" title="${esc(route.rationale || "")}">${esc(method)}</button></span>`;
}

// Every guardrail and verifier finding goes into the collapsed section, and
// none of them is rendered as a card in the flow.
//
// Not because they do not matter — because the ones that matter are ALREADY in
// the answer, written by the agent in the person's own language: a blocked turn
// says which rule stopped it, an escalation says a human has been asked. Showing
// the finding again next to it repeats the same thing in developer English, and
// a repair that was successfully fixed is a correction the reader never needed
// to see at all.
function finding(turn, f) {
  if (!f) return;
  techItem(turn, `⚑ ${t("sev." + f.severity) || f.severity} · ${term("finding", f.code, f.code)}`,
    { code: f.code, guard: f.guard, message: f.message, remedy: f.remedy, evidence: f.evidence });
}

function notice(turn, severity, title, message, remedy, evidence) {
  show(turn.notices);
  const n = el(`<div class="notice notice-${esc(severity || "advisory")}">
    <span class="notice-title"></span><span class="notice-msg"></span></div>`);
  n.querySelector(".notice-title").textContent = title;
  n.querySelector(".notice-msg").textContent = message || "";
  if (remedy) { const d = document.createElement("div"); d.className = "notice-detail"; d.textContent = remedy; n.append(d); }
  if (evidence?.length) { const d = document.createElement("div"); d.className = "notice-detail"; d.textContent = evidence.join(" | "); n.append(d); }
  turn.notices.append(n);
  scroll();
}

function techItem(turn, label, payload, isError) {
  turn.techCount++;
  const d = document.createElement("details");
  d.className = "tech-item";
  if (isError) d.dataset.error = "1";
  const s = document.createElement("summary");
  s.textContent = label;
  const pre = document.createElement("pre");
  pre.textContent = typeof payload === "string" ? payload : JSON.stringify(payload, null, 2);
  d.append(s, pre);
  turn.techBody.append(d);
  show(turn.tech);
  turn.tech.querySelector("summary").textContent =
    `${t("tech.title")} · ${turn.techCount} ${t("tech.steps")}`;
}

// Trace rows are rendered for a reader, with the raw event name kept as the
// title attribute. The names and codes stay English in Go — they are what you
// grep the logs for — but this panel is on the person's screen, not in a log.
function traceRow(turn, ev) {
  const row = document.createElement("div");
  row.className = `trace-row level-${ev.level}`;
  const label = term("event", ev.name, ev.name);
  const detail = ev.code
    ? term("finding", ev.code, ev.message || ev.code)
    : (label === ev.name ? ev.message || "" : "");
  row.title = `${ev.name}${ev.code ? " · " + ev.code : ""}${ev.message ? "\n" + ev.message : ""}`;
  row.innerHTML = `<span>${esc((ev.at || "").slice(11, 19))}</span>` +
    `<span>${esc(label)}${detail ? " — " + esc(clip(detail, 140)) : ""}</span>`;
  turn.techBody.append(row);
  show(turn.tech);
}

function approvalCard(turn, ap) {
  show(turn.decides);
  const card = el(`<div class="decide">
    <h3></h3><p></p><pre></pre>
    <div class="decide-actions">
      <button class="btn btn-primary" data-yes></button>
      <button class="btn" data-no></button>
    </div></div>`);
  card.querySelector("h3").textContent = t("approval.title");
  card.querySelector("p").textContent = `${ap.tool} — ${t("approval.impact")}: ${ap.impact || ""}`;
  card.querySelector("pre").textContent = prettyArgs(ap.args);
  card.querySelector("[data-yes]").textContent = t("approval.approve");
  card.querySelector("[data-no]").textContent = t("approval.decline");
  card.querySelector("[data-yes]").addEventListener("click", () => decide(card, ap.id, true));
  card.querySelector("[data-no]").addEventListener("click", () => decide(card, ap.id, false));
  turn.decides.append(card);
  scroll();
}

async function decide(card, id, approved) {
  await api("POST", `/api/approvals/${id}`, { approved });
  card.classList.add("is-done");
  card.querySelector(".decide-actions").textContent =
    approved ? t("approval.approved") : t("approval.declined");
  if (approved) send(locale() === "en" ? `I approve request ${id}. Please go ahead.` : `我确认 ${id}，请继续。`);
}

function consentCard(turn, prompt) {
  show(turn.decides);
  const card = el(`<div class="decide">
    <h3></h3><p class="plain"></p><p class="detail"></p>
    <div class="decide-actions">
      <button class="btn btn-primary" data-yes></button>
      <button class="btn" data-no></button>
    </div></div>`);
  // The prompt's own wording is the English source of truth the MODEL reads in
  // the tool result. The person reads the same question in their language.
  // These live in the TERMS table (server-side vocabulary rendered for a
  // reader), so they fall back to the server's own English wording when a scope
  // has no translation yet.
  const say = (part, fallback) => term(`consentP.${prompt.scope}`, part, fallback || "");
  card.querySelector("h3").textContent =
    `${t("consent.title")} — ${say("title", prompt.title || prompt.scope)}`;
  card.querySelector(".plain").textContent = say("plain", prompt.plain);
  card.querySelector(".detail").textContent =
    `${t("consent.what")}: ${say("what", prompt.what_for)}\n` +
    `${t("consent.retention")}: ${say("keep", prompt.retention)}`;
  card.querySelector("[data-yes]").textContent = t("consent.allow");
  card.querySelector("[data-no]").textContent = t("consent.deny");
  const settle = async (granted) => {
    await api("POST", "/api/consent", { session_id: state.session.id, scope: prompt.scope, granted });
    card.classList.add("is-done");
    card.querySelector(".decide-actions").textContent = granted ? t("consent.granted") : t("consent.denied");
    await renderOverview();
    if (granted) send(locale() === "en"
      ? `I have granted ${prompt.scope}. Please continue.` : `我已同意 ${prompt.scope}，请继续。`);
  };
  card.querySelector("[data-yes]").addEventListener("click", () => settle(true));
  card.querySelector("[data-no]").addEventListener("click", () => settle(false));
  turn.decides.append(card);
  scroll();
}

function prettyArgs(v) {
  try {
    if (typeof v === "string") return JSON.stringify(JSON.parse(atob(v)), null, 2);
    return JSON.stringify(v, null, 2);
  } catch { return typeof v === "string" ? v : JSON.stringify(v, null, 2); }
}

function crumb(intentID) {
  $("#crumbIntent").textContent = intentID ? intentLabel(intentID) : t("crumb.waiting");
}

// ── overview panel ─────────────────────────────────────────────────────────

async function renderOverview() {
  if (!state.session) return;
  const d = await api("GET", `/api/sessions/${state.session.id}`);
  state.session = d.session;

  const open = (d.tasks || []).filter((x) => x.status !== "done" && x.status !== "cancelled");
  $("#taskCount").textContent = d.tasks?.length ? `${open.length}/${d.tasks.length}` : "";
  const tasks = $("#tasks");
  tasks.innerHTML = "";
  if (!d.tasks?.length) {
    tasks.innerHTML = `<p class="empty">${esc(t("tasks.empty"))}</p>`;
  } else {
    for (const task of d.tasks) {
      const cls = task.status === "blocked" ? " is-blocked" : task.status === "done" ? " is-done" : "";
      const ico = task.status === "blocked" ? "alert" : task.status === "done" ? "check" : "cap";
      const channel = [task.channel?.phone, task.channel?.window].filter(Boolean).join(" · ");
      tasks.append(el(`<div class="task">
        <div class="task-ico${cls}"><span class="ico">${icon(ico)}</span></div>
        <div class="task-main">
          <div class="task-title">${esc(task.title)}</div>
          <div class="task-meta">${esc(term("status", task.status))} · ${esc(term("domain", task.domain))} · ${esc(term("owner", task.owner || "unassigned"))}
            ${task.blocker ? `<br><span class="blocked">${esc(task.blocker)}</span>` : ""}
            ${channel ? `<br>${esc(channel)}` : ""}</div>
        </div></div>`));
    }
  }

  const p = d.profile || {};
  const rows = [
    ["city", p.city], ["hukou_city", p.hukou_city], ["education", p.education],
    ["skills", (p.skills || []).join("、")], ["constraints", (p.constraints || []).join("；")],
    ["interests", (p.interests || []).join("、")], ["cohorts", (p.cohorts || []).join("、")],
    ["access_needs", (p.access_needs || []).join("、")],
  ].filter(([, v]) => v);
  const rec = $("#records");
  rec.innerHTML = "";
  if (!rows.length) {
    rec.innerHTML = `<p class="empty">${esc(t("records.empty"))}</p>`;
  } else {
    for (const [k, v] of rows) {
      const quote = p.provenance?.[k];
      rec.append(el(`<div class="record">
        <div class="record-k">${esc(term("field", k, k))}</div>
        <div class="record-v">${esc(v)}</div>
        ${quote ? `<div class="record-q">“${esc(clip(quote, 60))}”</div>` : ""}</div>`));
    }
  }

  const cons = $("#consents");
  cons.innerHTML = "";
  const scopes = ["store_profile", "share_with_caseworker", "submit_on_behalf", "aggregate_deidentified"];
  const held = Object.fromEntries((d.consent || []).map((g) => [g.scope, g.granted]));
  for (const scope of scopes) {
    const on = !!held[scope];
    const row = el(`<div class="consent-row">
      <span>${esc(term("scope", scope, scope))}</span>
      <span><span class="consent-state ${on ? "consent-yes" : "consent-no"}">${esc(on ? t("consent.granted") : t("consent.denied"))}</span>
      <button class="link-danger" style="color:var(--brand)"></button></span></div>`);
    const btn = row.querySelector("button");
    btn.textContent = on ? t("consent.revoke") : t("consent.grant");
    // Withdrawing has to be reachable without asking the agent nicely. A promise
    // that consent can be withdrawn is worth nothing without a control for it.
    btn.addEventListener("click", async () => {
      await api("POST", "/api/consent", { session_id: state.session.id, scope, granted: !on });
      await renderOverview();
    });
    cons.append(row);
  }
}

async function forgetProfile() {
  await api("DELETE", `/api/sessions/${state.session.id}/profile`);
  await renderOverview();
}

// ── voice ──────────────────────────────────────────────────────────────────

function speak(text) {
  if (!window.speechSynthesis) return;
  window.speechSynthesis.cancel();
  const u = new SpeechSynthesisUtterance(text.slice(0, 3000));
  u.lang = locale() === "en" ? "en-US" : "zh-CN";
  u.rate = 0.95;
  window.speechSynthesis.speak(u);
}

function startDictation() {
  const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
  if (!SR) {
    status(locale() === "en" ? "No speech recognition in this browser." : "这个浏览器不支持语音输入。");
    return;
  }
  const rec = new SR();
  rec.lang = locale() === "en" ? "en-US" : "zh-CN";
  rec.interimResults = true;
  $("#mic").setAttribute("aria-pressed", "true");
  brandMood("listening");
  status(t("status.listening"), "busy");
  rec.onresult = (e) => { $("#input").value = Array.from(e.results).map((r) => r[0].transcript).join(""); };
  rec.onend = () => {
    $("#mic").setAttribute("aria-pressed", "false");
    brandMood(state.busy ? "thinking" : "calm");
    status("");
  };
  rec.start();
}

// ── plumbing ───────────────────────────────────────────────────────────────

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    const e = await res.json().catch(() => ({}));
    throw new Error(`${e.code || res.status}: ${e.message || res.statusText}${e.remedy ? " — " + e.remedy : ""}`);
  }
  return res.status === 204 ? null : res.json();
}

async function* sse(stream) {
  const reader = stream.getReader();
  const dec = new TextDecoder();
  let buf = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let i;
    while ((i = buf.indexOf("\n\n")) >= 0) {
      const chunk = buf.slice(0, i).trim();
      buf = buf.slice(i + 2);
      if (!chunk.startsWith("data:")) continue;
      try {
        const ev = JSON.parse(chunk.slice(5).trim());
        if (ev.kind === "close") return;
        yield ev;
      } catch { /* a partial frame; the next read completes it */ }
    }
  }
}

function setBusy(b) {
  state.busy = b;
  $("#send").disabled = b;
  brandMood(b ? "thinking" : "calm");
  document.querySelector(".mascot")?.classList.toggle("is-thinking", b);
}

// Both brand marks are painted from the same call. The gate carries one too —
// it covers the sidebar, so it is the only mark on the first screen — and
// painting it here rather than at the gate keeps a single answer to "what does
// the mark look like" instead of two that drift.
function brandMood(mood) {
  for (const slot of [$("#brandAvatar"), $("#gateAvatar")]) {
    setMood(slot, mood, t("a11y.avatar"));
  }
}

// Three states, not two. "system" leaves the page following the OS, which is
// what most people want and what it did before; the other two are an explicit
// override that outranks the OS in both directions. The choice is stamped on
// <html> as data-theme, which every colour token is defined against, and it is
// applied before first paint in boot() so there is no flash of the wrong theme.
function applyTheme(choice) {
  const value = ["system", "light", "dark"].includes(choice) ? choice : "system";
  if (value === "system") delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = value;
  localStorage.setItem("oba.theme", value);
  const sel = $("#theme");
  if (sel) sel.value = value;
}

function status(text, cls) {
  const el = $("#status");
  el.textContent = text || "";
  el.className = "status" + (cls === "busy" ? " is-busy" : "");
}

function scroll() {
  const tr = $("#transcript");
  tr.scrollTop = tr.scrollHeight;
}

function clip(s, n) {
  s = String(s ?? "");
  return s.length > n ? s.slice(0, n) + "…" : s;
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

boot();

// ── the gate ───────────────────────────────────────────────────────────────
//
// One form for both signing in and creating an account. They differ by one
// field and one endpoint, and two forms drift: the second one is where the
// password rule, the error rendering and the language switch stop matching.

async function currentAccount() {
  try {
    return await api("GET", "/api/auth/me");
  } catch {
    return null;
  }
}

function showGate() {
  $("#gate").hidden = false;
  setGateMode(state.gateMode);
  $("#gateUser").focus();
}

function hideGate() {
  $("#gate").hidden = true;
  gateError("");
}

function setGateMode(mode) {
  state.gateMode = mode;
  const up = mode === "signup";
  $("#gateInviteField").hidden = !up;
  $("#gateInvite").required = up;
  $("#gatePass").setAttribute("autocomplete", up ? "new-password" : "current-password");
  $("#gateSubmit").textContent = t(up ? "gate.signUp" : "gate.signIn");
  $("#gateSwitch").textContent = t(up ? "gate.toSignIn" : "gate.toSignUp");
  gateError("");
}

// The server's own message is shown rather than a generic one. Its errors are
// written to be read by the person, and each carries a remedy — replacing them
// with "something went wrong" is how somebody ends up stuck on a wrong invite
// code with nothing to act on.
function gateError(msg) {
  const el = $("#gateError");
  el.textContent = msg || "";
  el.hidden = !msg;
}

function wireGate() {
  $("#gateSwitch").addEventListener("click", () =>
    setGateMode(state.gateMode === "signup" ? "signin" : "signup"));

  for (const b of document.querySelectorAll("[data-gate-lang]")) {
    b.addEventListener("click", () => {
      const l = b.dataset.gateLang;
      setLocale(l);
      localStorage.setItem("oba.locale", l);
      const sel = $("#locale");
      if (sel) sel.value = l;
      setGateMode(state.gateMode);
    });
  }

  $("#gateForm").addEventListener("submit", async (e) => {
    e.preventDefault();
    const btn = $("#gateSubmit");
    if (btn.disabled) return;
    btn.disabled = true;
    const wasLabel = btn.textContent;
    btn.textContent = t("gate.signingIn");
    gateError("");
    try {
      const up = state.gateMode === "signup";
      state.account = await api("POST", up ? "/api/auth/signup" : "/api/auth/signin", {
        username: $("#gateUser").value,
        password: $("#gatePass").value,
        invite_code: up ? $("#gateInvite").value : undefined,
      });
      $("#gatePass").value = "";
      // Reload rather than calling boot() again: boot() binds the interface's
      // event listeners, and running it twice binds them twice — every send
      // would fire two turns.
      location.reload();
    } catch (err) {
      gateError(String(err?.message || err));
      btn.disabled = false;
      btn.textContent = wasLabel;
    }
  });

  $("#signOut").addEventListener("click", async () => {
    // Server-side too, not just this browser: see auth.go.
    try { await api("POST", "/api/auth/signout", {}); } catch { /* going anyway */ }
    location.reload();
  });
}

function paintWho() {
  const who = $("#who");
  if (!state.account) { who.hidden = true; return; }
  who.hidden = false;
  $("#whoName").textContent = state.account.username;
  $("#whoName").title = `${t("gate.signedInAs")}: ${state.account.username}`;
}
