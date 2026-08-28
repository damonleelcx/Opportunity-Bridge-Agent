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
same character is committed here as `web/static/mascot.png`, so keeping the face
costs nothing against the rule above — it is still one binary and still no
external request but the one to the model.

**There are two faces, and that is deliberate.** An illustration cannot change
expression, and the mood that had to survive is `serious` — the one that stops
the face smiling while the agent is refusing. So the illustrated 阿桥 greets, from
the overview panel and from the sign-in screen, and the inline mark
(`avatar.js`, four moods, no request) is what sits beside every message, in the
sidebar and in the tab icon. **The face next to a refusal is the mark, and it is
not smiling.** Swapping the illustration is still a file drop; swapping the mark
would cost the moods, which is the part that is load-bearing.

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

## Kept from before

Streaming SSE, the approval gate showing the exact arguments, the consent card,
plain-language / large-text / read-aloud, browser voice in and out, role
switching, session history, light and dark, and readability at 150% text size.
