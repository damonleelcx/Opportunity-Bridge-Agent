# Dialect moved into the text, and the first attempt wired it to a state nobody sets

**Reported:** 2026-08-31. The "honest limits" card 「方言是口吻，不是方言」 was to
become something the product can do — in the text, with no dialect voice.
**Area:** `internal/prompt/prompt.go` (the language directive and the delivery
rules), the `low_access_support` registry entry, the landing page, both READMEs
and `docs/01`, `docs/02`, `docs/14`.
**Status:** shipped and verified on a live Cantonese turn. Read-aloud remains
Mandarin-only and now says so.

## What the old policy was, and why it was written

`deliveryRules` said:

> Dialect: use everyday spoken vocabulary rather than written-official
> vocabulary. **Do not attempt to imitate a dialect you cannot write.**

and the registry listed under `CannotDo`: *"Claim to speak a dialect it cannot;
it adjusts register and vocabulary, and says so."*

That was an honest rule written against an assumption: that the model could not
produce a real variety, so anything it produced would be a costume. The
assumption was never re-tested, and nothing in the suite pinned the rule — the
policy existed only as prose inside one `WriteString`.

## Why it changed

Asked directly for spoken Cantonese and Sichuanese, `deepseek-v4-pro` returns:

> 粤语：你去办失业登记**先**，办完**先至**可以**攞**失业保险金，带**埋**身份证**同**解除劳动合同证明。
> 四川话：你先**切**把失业登记办了，办完了才**领得到**…要**带起**。

Postposed 先, 先至, 攞, 埋, 同; 切, 领得到, 带起. That is the syntax of the
varieties, not Mandarin wearing particles. The assumption no longer holds.

## The first attempt failed, and only a live turn showed it

The new rule was put where the old one was: in `deliveryRules`, behind the
`AccessDialect` need. That need is set by an `accessibility_set` tool call.

Live turn, resident, writing in Cantonese and asking in Cantonese to be answered
in Cantonese:

> 我系成都嘅，做咗五年流水线，间厂上个月执笠咗。唔該你用廣東話同我講…

Tools actually called: `profile_upsert`, `knowledge_search` ×2,
`opportunity_search` ×2. **No `accessibility_set`.** So the need was never set,
the rule never entered the prompt, and the answer came back in Mandarin, opening
with:

> 先说清楚：我写不到标准广东话，写错反而会害你看错，所以用简单普通话。

The capability had been wired to a state nobody sets. **Writing in a variety IS
the request**; it does not need a tool call first. Note what this looked like
from the outside: a polite, plausible sentence about a limitation that no longer
existed. Reading the diff would not have caught it — only running it did.

## The fix

One constant, `prompt.DialectPolicy`, appended to the **always-on language
directive** for the `zh` and `match` locales. It says four things:

1. If the person writes in a variety, or asks for one, answer in it, in its real
   spoken forms.
2. Ids, phone numbers, addresses and legal terms stay in their official written
   form **inside** a dialect answer. They are not prose — they are what the
   person says at a counter and types into a form, and a dialectised programme
   name is one the counter does not recognise. (The directive's existing
   carve-out already said this; the dialect rule points at it rather than
   restating it.)
3. If it cannot write the variety properly, say so in one clause and fall back to
   plain spoken Mandarin. This is what survives of the old policy, and it is the
   part worth keeping: an imitation is worse than an honest fallback.
4. Read-aloud has no dialect voice — these characters are spoken in Mandarin.
   Do not promise otherwise.

`AccessDialect` still exists and now means what it should: a **standing
preference**, so a person who set it keeps getting their variety on turns whose
latest message happened to be in standard Chinese. It emits one line and does
not restate the policy, because two copies of a policy in one prompt is how two
copies drift apart.

## Read-aloud was NOT connected to a dialect voice, deliberately

The vendor (Fish Audio) does carry dialect voices — a search returns 467 for
粤语, 49 for 四川话 — and all four test syntheses succeeded. They were not
adopted:

- Every one is tagged `languages: ['zh']`. The platform has no notion of
  "Cantonese"; these are **community uploads**, with no vendor guarantee, which
  the uploader can delete at any time.
- The most popular results are clones of named public figures (黎明, 陈慧琳),
  which a public-service product cannot use.

So the answer is written in the variety and read aloud in Mandarin. That is a
real limit, and it is now stated on the landing page and in both READMEs rather
than left for a person to discover by pressing the button.

## Copy that changed

「方言是口吻，不是方言」 → 「方言只在文字里」 / "Dialect lives in the text, not in
the voice", in `i18n.js` (both languages), `index.html`, `README.md`,
`README.zh-CN.md`, `docs/01-goal-and-boundaries.md`, `docs/02-intents.md`,
`docs/14-interface.md`. The card stays in the honest-limits section because
there is still a limit — it is just a different and smaller one.

## Regression tests

The old policy had **no test at all**, which is how a rule survives its own
premise. `-count=1` matters.

| test | file | holds |
|------|------|-------|
| `TestDialectPolicyAppliesWithoutAnyToolCall` | `internal/prompt/prompt_test.go` | with **no** access needs set — the state the live failure was in — the policy is already in the context layer |
| `TestDialectRuleProtectsWhatThePersonMustReuse` | `internal/prompt/prompt_test.go` | all four clauses survive, in both the `zh-CN` and `match` directives |
| `TestSavedDialectPreferenceDoesNotRestateThePolicy` | `internal/prompt/prompt_test.go` | the policy appears exactly once in a prompt, and the saved preference still adds persistence |

The wording assertions compare on collapsed whitespace, so they test the rule
rather than where the constant happens to wrap.

Mutation drill: deleting the official-written-form clause reds
`TestDialectRuleProtectsWhatThePersonMustReuse` with "the dialect policy no
longer says \"official written form\"". The first attempt at this drill passed
while the mutation had not actually landed — it replaced one fragment of a
concatenated string and the phrase survived in the next fragment. Assert the
mutation is in the file before believing the drill.
