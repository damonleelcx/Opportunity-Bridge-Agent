# 阿桥 · Opportunity Bridge Agent

[中文](README.zh-CN.md)

![Three columns — conversations on the left; in the middle the question, the routing badge 已为你匹配：个人 · 机会路径 and a CNC training card with its address, hours and the documents to bring; 我的概览 on the right with the open task, 阿桥 and the record kept about the person](docs/assets/interface-mockup.png)

*The UX Pilot mockup this was built to — not a screenshot of the running binary.
Two things in it could not ship as drawn: the CDN it loads, and the mascot's line
"王师傅，别担心…", which is the false reassurance the persona forbids and
`no_false_reassurance` blocks. [What changed and why](docs/14-interface.md).*

**阿桥 (Aqiao)** is a conversational agent that turns **"I don't know what I can do, where to go, or
whether I qualify"** into one executable path: diagnose → recommend → file →
follow up → human fallback.

Built as a system, not as a prompt: **model + instructions + context + tools +
agent loop + state + guardrails + evaluation + observability.** Every one of
those has a place in the tree and a test that holds it there —
[docs/README.md](docs/README.md) maps each build step to the code.

**Live: [jobs.heros-agent.space](https://jobs.heros-agent.space)** — running on
DeepSeek, answering in Chinese, nationwide.
See [docs/15-deployment.md](docs/15-deployment.md) (⚠️ public and
unauthenticated; the exposure is a token bill, not data).

`/` is the landing page — what it does, what it refuses to do, and what it
cannot do yet. The conversation is at **`/app`**, and a signed-in reader opening
`/` is forwarded there. Before this, `/` was the app, which meant a stranger who
followed a link arrived at an unlabelled password box; the write-up is in
[docs/14-interface.md](docs/14-interface.md), "The landing page".

```bash
make demo          # offline, no API key: http://localhost:8787
make env           # create .env from .env.example, then fill in your key
make run           # against the Claude API
make run-deepseek  # against DeepSeek
make check         # gofmt, vet, every test, and the evaluation suite
```

**It answers in Chinese by default** — `OBA_REPLY_LANGUAGE` takes `zh-CN`
(default), `en`, or `match`, and the language selector in the header changes the
next answer, not the next conversation.

---

## The problem it addresses

The macro statement — that people's rising expectations of a good life outrun
uneven and insufficient development — describes a country, not a specification.
The narrow version inside it, and the one software can actually reach:

> An ordinary person's ability to reach stable income, a way up, and public
> support is separated from where those things actually are, by distance,
> language, paperwork, and not knowing what exists.

That gap is made of **information asymmetry** and **transactional friction**.
This agent attacks both. It cannot attack the shortage underneath, and it says
so — see [what it must never do](#the-boundary-that-matters-most).

## Four audiences, four intents

Each bullet of the brief became a first-class **intent** in
[`internal/intent/intent.go`](internal/intent/intent.go), carrying its own goal,
success criteria, boundaries, workflow, required facts, tool allowlist,
verifiers and budgets. Routing, permissions, prompt assembly, the interface's
chips, the eval suite and the docs all read that one registry.

| Intent | For | Does |
|---|---|---|
| **`individual_pathway`** | one person | Records skills, experience, city and constraints; matches real jobs, training, entrepreneurship support and subsidies; reads out published criteria as met / unmet / unknown; drafts material; tracks the follow-up; explains the procedure |
| **`low_access_support`** | graduates, workers changing trade, gig workers, migrant workers, caregiving families | Solves the friction before the topic — plain language, larger text, read aloud, an answer in the person's own variety of Chinese, assisted-at-a-window mode; always offers a phone number or an address with hours; hands off to a person early, with the context already written down |
| **`service_orchestration`** | frontline staff | Stitches employment, training, social insurance, medical insurance, childcare, eldercare and housing procedures into one tracked list per person, with owners, channels and explicit dependencies — so the resident stops being the integration layer between counters |
| **`supply_demand_insight`** | planners | Over consented, de-identified aggregates only: finds where *the jobs are here but people cannot reach them* and *the support exists but nobody claims it*, with a k-anonymity floor, the consent coverage stated next to every figure, and association rather than cause |

## The boundary that matters most

**It does not decide eligibility, and it does not score people.**

Not a disclaimer — the difference between reducing imbalance and automating it.
An agent that quietly ranks people and uses that rank to decide what they are
shown has rebuilt the gatekeeping it was meant to route around, with less
accountability than the counter it replaced.

So the rules are code, not prose:

| Rule | Enforced by |
|---|---|
| No eligibility verdicts | `criteria_explain` cannot return one; `no_eligibility_verdict` blocks answers that state one |
| Situation only ever *adds* support | `no_cohort_downranking` **blocks**; retrieval boosts are additive and each is named |
| No invented programmes | `no_invented_identifiers` checks every id against the corpus and **blocks** |
| Nothing irreversible without a person | approval keyed to a hash of the exact arguments |
| Nothing about a person without permission | four consent scopes, checked before any tool body runs |
| Nothing identifying in an aggregate | k-anonymity floor + `no_identifiers` **block** |
| The answer is in a language the person can read | `reply_language` checks the delivered script, not the instruction |

## Who 阿桥 is

桥 is a bridge — the product's own metaphor. 阿 in front of a single character is
how people address someone familiar rather than someone official, and that prefix
is the design decision: this system holds no authority over anybody. It does not
decide eligibility, it does not score people, it names who decides instead. A
name like "Advisor" would claim a standing it does not have, to an audience for
whom official-sounding things have often meant paperwork they could not finish.

The voice follows from that. Calm, short, never reassuring — no "don't worry",
no 别担心. It says "I don't know" plainly and once. It treats the person as
capable. And because warmth is exactly what erodes an honest "no", the persona
ships with a check rather than a hope: `no_false_reassurance` fires on comfort
that is not backed by a fact, and the remedy is always the same shape — *what is
known, what is not, and who decides.*

The avatar is a bridge that reads as a face: two lantern eyes above the deck, the
arch beneath it doubling as the mouth. It has four moods, and the one that earns
its place is **`serious`** — the arch flattens when a turn is blocked or refused,
because an interface that keeps smiling while the agent says *"I stopped myself
from sending that answer"* is doing in pixels what the persona forbids in words.

Full write-up: [docs/13-name-and-voice.md](docs/13-name-and-voice.md).

## What the interface shows

`/` is the landing page: the claim, one sample conversation rendered exactly as
the app renders it, the five steps, the four audiences, the boundary as a table
of rule → guard, and the honest limits **above** the final call to action — a
product that ships a check against false reassurance does not get to oversell
itself on its own front page. It fetches nothing from the network, hides nothing
without JavaScript, and exists in both languages; all three are fenced in
`web/interface_test.go`.

The conversation itself is at `/app`. Three columns: conversations on the left, the conversation in the middle, **我的
概览** on the right — open tasks, 阿桥, everything held about the person, and the
four permissions with a control to grant or withdraw each.

In the conversation:

- The routed intent as a badge (`已为你匹配：个人 · 机会路径`) with how it was
  decided
- **Opportunity cards** built from the tool results — title and id, the amount or
  a green 学费全免, the summary, location / schedule / phone, the documents to
  bring, and 查看详情 for the published criteria with their sources
- The approval card, showing the exact text that will be filed — nothing before
- The consent card, in plain words: what is kept, what for, for how long
- Suggested follow-ups derived from what just happened
- `› 系统运行详情 · N 步` — every tool call, every finding, the whole trace,
  collapsed, with a global toggle in the header

Anything that **altered the answer is in the answer**, in the person's own
language: a blocked turn says which rule stopped it and that nothing was done.
Everything else folds away. The operator debugging it and the person reading it
are still looking at the same record — it is just no longer in the way.

Built to a UX Pilot mockup; [docs/14-interface.md](docs/14-interface.md) records
what came from it, what had to change (no CDN, and one line of copy that the
persona forbids), and why.

## Try it

```bash
git clone https://github.com/damonleelcx/Opportunity-Bridge-Agent
cd Opportunity-Bridge-Agent
make demo                 # replays a fixed conversation; no API key, no network
```

Then, with credentials — either in `.env`:

```bash
make env          # copies .env.example, chmod 600
# edit .env: OBA_BACKEND=deepseek, DEEPSEEK_API_KEY=sk-...
make run-deepseek
```

or exported, which always wins over the file:

```bash
ANTHROPIC_API_KEY=sk-ant-...  make run           # Claude
DEEPSEEK_API_KEY=sk-...       make run-deepseek  # DeepSeek
```

Both providers drive the same agent — routing, tools, guardrails, verifiers,
budgets and the approval gate never learn which one answered. Model ids follow
`OBA_BACKEND`, and a model id from the wrong provider is refused at startup
rather than silently remapped onto that provider's default.
[docs/12-deepseek.md](docs/12-deepseek.md) covers the mapping and the two wire
shapes that otherwise fail silently.

Things worth doing in the interface:

1. Ask, as a resident: *我在成都，做过五年流水线，工厂关了，能做什么？*
   Watch the routing badge, the searches, and the criteria read out as
   met / unmet / unknown.
2. Tick **大白话** (plain language). It is not a client-side rewrite — it goes
   through the same `accessibility_set` path the agent itself uses.
3. Ask it to file something. Nothing happens until you approve the exact text.
4. Switch the role to **规划/政策分析** and ask which district has jobs people
   cannot reach. Note the suppressed cells and the consent coverage.

## Architecture

```
web/static ── conversational UI (SSE, no build step, embedded in the binary)
     │
internal/httpapi ── transport only; owns no logic
     │
internal/agent ─── the loop: understand → plan → act → verify → respond
     ├── internal/intent ──── the registry: four audiences, fully specified
     ├── internal/prompt ──── 3 layers: charter | intent | context (2 cache breakpoints)
     ├── internal/llm ─────── model boundary (Anthropic · DeepSeek · scripted)
     ├── internal/tools ───── 14 tools; validation, permissions, consent, approval
     ├── internal/retrieval ─ BM25 + hard metadata filters, explainable
     ├── internal/guardrail ─ input guards + the verifiers each intent names
     ├── internal/store ───── task state | history | long-term memory, kept apart
     └── internal/obs ─────── one event per decision, streamed to the UI
```

| Path | |
|---|---|
| `cmd/obagent` | the server |
| `cmd/obaeval` | the evaluation runner |
| `data/` | **sample** corpus — every record's `source_ref` starts with `SAMPLE/` |
| `evals/` | 27 cases: success, edge, adversarial, routing |
| `demo/` | the offline replay script |
| `.env.example` | every variable, documented; `.env` itself is gitignored |
| `web/static/avatar.js` | 阿桥's face — inline SVG, four moods |
| `docs/` | one page per build step, mapped in [docs/README.md](docs/README.md) |
| `deploy/` | Dockerfile inputs, k8s manifests, and the two idempotent scripts |
| `data/service_directory.json` | 31 official public-employment destinations, each URL verified |

## Runtime

Go 1.25+ (the postgres driver requires it). One binary with the interface
embedded. Linux / macOS / Windows.

State has two backends and exactly one is in use at a time. Set
`OBA_DATABASE_URL` and it is postgres, which is what a real deployment uses —
a database configured but unreachable stops the process rather than quietly
writing somewhere else. Leave it unset and it is the JSON snapshot at
`OBA_STATE_PATH`, which is what local development uses; leave that unset too
and the app still works, it just forgets. Reads are served from memory under
both, so nothing on the request path depends on which one you chose.

Beyond the model API, postgres is the only external service, and only when you
ask for it.

## Errors

Every failure carries a code, what it means for the person, and what to do next.

| Code | Means |
|---|---|
| `CONSENT_REQUIRED` | a permission is missing; the question to ask is returned with it |
| `APPROVAL_REQUIRED` | nothing was done; a human must decide first |
| `TOOL_NOT_PERMITTED_FOR_INTENT` / `_FOR_ROLE` | the action is outside this audience's boundary |
| `ARGUMENT_INVALID` | names every bad field and what would fix it |
| `EVIDENCE_REQUIRED` | a task cannot be closed on a report alone |
| `OPPORTUNITY_NOT_FOUND` | the id is not in the corpus — search first, do not guess |
| `MODEL_AUTH_FAILED` / `MODEL_RATE_LIMITED` / `MODEL_UNAVAILABLE` | with the remedy attached |
| `MODEL_BILLING` | DeepSeek balance exhausted — permanent, so it is never retried |
| `SCRIPT_EXHAUSTED` | the scripted backend refusing to improvise |
| `LOCALE_INVALID` | an answer language the service does not offer |
| `ENV_FILE_INVALID` | a bad line in `.env`, named by line number |

## Honest limits

- **The corpus is invented.** Listings, courses, start-up support, subsidies and
  procedure guides, shaped like the real thing so the machinery can be exercised
  end to end. Every record's `source_ref` begins with `SAMPLE/`, and the app
  carries a permanent "sample corpus" flag. The exact counts are reported by
  `/api/meta` (`corpus_opportunities`, `corpus_knowledge_docs`) rather than
  written down here, because a number in prose drifts the moment a record is
  added — it once said 21 while the answer was 26. See
  `docs/bugfix/2026-08-31-honest-limits-were-not-honest.md`.
- **`application_submit` has no live authority endpoint.** It records and tracks
  the filing and says plainly that the person must still complete it through the
  channel shown. A demo that claimed to have filed something would be worse than
  one that says it cannot.
- **A forgotten password is recoverable, and only through a confirmed address.**
  New accounts give an email; existing ones keep working and can add one. An
  unconfirmed address blocks nothing except the reset itself. The reset request
  answers identically whether or not an address is registered, links are
  single-use, and setting a password signs every device out. Mail is off unless
  `OBA_SMTP_HOST`, `OBA_SMTP_FROM` and `OBA_PUBLIC_ORIGIN` are all set, and the
  sign-in page then stops offering a reset rather than showing a dead form. See
  `docs/bugfix/2026-08-31-email-verification-and-reset.md`.
- **Read-aloud through a speech vendor needs the person's permission**
  (`read_aloud_via_vendor`), checked in the handler before any text leaves.
  Refusing costs nothing: the browser's own voice reads the answer instead. See
  `docs/bugfix/2026-08-31-read-aloud-needs-consent.md`.
- **Voice is not private by default, and the page says so.** Dictation is
  transcribed by the browser, which on the major browsers means the browser
  maker's servers — the audio does leave the device, though it never passes
  through this service. Read-aloud uses the browser's own voice unless a speech
  vendor is configured; where one is, the **answer text** is sent to that vendor
  to be rendered, and on the free backbone the vendor's terms allow using those
  requests to improve its models. Which of the two a deployment is doing is read
  from `/api/health` rather than written into the copy. See
  `docs/17-read-aloud.md` and
  `docs/bugfix/2026-08-31-the-privacy-claim-was-false.md`.
- **Dialect lives in the text, not in the voice.** It answers in the variety the
  person is using. Read-aloud has no dialect voice, so those characters are
  spoken in Mandarin. Where it cannot write a variety properly it says so and
  falls back to plain spoken Mandarin rather than faking it, and programme
  names, ids, phone numbers and addresses always stay in their official written
  form. See `docs/bugfix/2026-08-31-dialect-moved-into-the-text.md`.
- **Names, ids, addresses and phone numbers are never translated or restyled** —
  not into the answer's language, and not into a regional variety. A translated
  address is an invented address, and an id the counter does not recognise is
  worse than no id. This is enforced in the language directive rather than left
  to judgement, because a live result or a future feed can arrive in any
  language. (This bullet used to describe the corpus as English-language. It
  is not, and has not been for some time: everything in `data/` is Chinese, and
  a fence in `internal/corpus` now fails when the documents and the data
  disagree. See `docs/bugfix/2026-08-31-the-privacy-claim-was-false.md` for why
  claims like that are derived rather than written down wherever they can be.)

## Licence

Apache 2.0.
