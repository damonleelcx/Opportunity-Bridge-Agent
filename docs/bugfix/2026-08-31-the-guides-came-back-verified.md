# The twelve guides came back, and six of them were never policy

**Reported:** 2026-08-31 — "12 篇办事指南也逐篇核实恢复", after they were removed
with the invented corpus.
**Area:** `data/knowledge.json`, `corpus.DocKind`, the `knowledge_search` tool
description and result shape.
**Status:** all twelve restored. Six carry an official source that was fetched
and read; six are labelled as this service's own advice.

## The distinction that had to exist first

Reading the twelve showed they were two different things wearing one label.

**Six restate published rules** — the 12333/12345 hotlines, social-insurance
transfer, the residence permit, flexible-employment registration, wage arrears,
and graduate registration. Those can be checked, and must be.

**Six are operating advice** — how to read a set of conditions, what order to do
things in, when a counter beats an app, which certificates cross trades, why
subsidies go unclaimed. There is no regulation behind them. Giving them an
official-looking citation would have been the corpus rule's own failure mode: a
plausible source for something no authority ever said.

So `Doc` gained one field. `kind` is `policy` or `guidance`, it defaults to
`policy` when absent — an untagged document is held to the stricter rule rather
than quietly excused from it — and it travels in the `knowledge_search` result so
the model can tell them apart. The tool description says outright that advice
presented as regulation is worse than either.

Live, asked about residence permits and ordering, the answer separated them
without being asked to:

> 按《居住证暂行条例》(kb-003)：…受理后 15 日内发证，最长不超过 30 日。
> 办理顺序。**这段是我们自己的办事经验，不是政策条文** (kb-009)：…

## What reading the sources found

🔴 **kb-003 said "多数城市十五个工作日内办结".** The 居住证暂行条例 says
「公安机关应当自受理之日起 **15 日内**制作发放居住证」, extendable to at most 30 days in
remote areas. Days, not working days; a national rule, not a local tendency; and
the extension was missing. Somebody counting working days would have expected
three weeks and been wrong about when to chase it.

🔴 **kb-003 also had the entry condition wrong.** It said the permit needs
"住所证明加半年居住记录". Article 2 requires living there **half a year or more** AND
one of 三者之一: 合法稳定就业、合法稳定住所、连续就读. A stable address is one of three
alternatives, not a universal requirement beside the six months.

🔴 **kb-008 asserted a number nobody had checked.** It said the arbitration time
limit "通常是一年". That is very probably right — and I could not fetch the statute
from any `.gov.cn` host to confirm it, after three attempts. So the number came
out. The guide now says the limit exists, is short, and that getting it wrong by
a day can cost the claim, so ask 12333 or the labour inspectorate **today**. The
urgency survives; the unverified figure does not.

kb-002 and kb-004 were correct and gained their sources (国办发〔2009〕66号;
就业补助资金管理办法), with kb-004 picking up the two-thirds cap the 办法 actually
states.

## Sources, each fetched and read

| doc | source |
|---|---|
| kb-001 | 人社政务服务平台 www.12333.gov.cn |
| kb-002 | 国办发〔2009〕66号 城镇企业职工基本养老保险关系转移接续暂行办法 |
| kb-003 | 居住证暂行条例 (gov.cn) — Articles 2, 9 |
| kb-004 | 就业补助资金管理办法 (gov.cn, 2024) |
| kb-008 | 就业促进法 第三十五条 (the channel), with no time limit asserted |
| kb-012 | 离校未就业高校毕业生实名登记 /「131」服务 |

## Regression test

`TestGuidesAreEitherSourcedOrOwned` (`internal/corpus/corpus_test.go`): every
shipped policy document cites something beginning `http`; no guidance document
does, because a URL beside advice reads as an authority saying it; an untagged
document fails outright; and both kinds must be present, or the fence is
comparing nothing.

## Noted, not resolved

`TestDecliningToRankIsNotItselfRanking` failed **once**, during a run that raced
a `gofmt -w` writing the same package. It did not reproduce in eight subsequent
runs — five of the package alone, three of the full suite — and passes
deterministically on its own. Recorded rather than declared fixed.
