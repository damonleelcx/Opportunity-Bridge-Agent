# The invented corpus left the product

**Reported:** 2026-08-31 — "i don't even think you should keep any sample/ stuff.
just make everything work."
**Area:** `data/`, a new `testdata/corpus/`, every `corpus.Load` call site, the
Makefile, `corpus.IsSample`, `/api/meta`, and the landing-page copy.
**Status:** done for the opportunity corpus. The twelve procedure guides were
removed rather than verified — see "What this cost".

## What shipped before

Twenty-six opportunity records and twelve procedure guides, at organisations that
do not exist: 锦禾精密部件, 安宁社区日间照料中心, 龙泉驿区技工学校, 合兴物流,
川光新能源 — and, worse, 成都市人力资源和社会保障局 and 成都市社会保险事业管理局,
which are the names of real bodies attached to invented records.

## The distinction that decided the change

The corpus was two different things wearing one label.

**Twenty-one local records were pure invention.** No argument: they left.

**Five were real national schemes** — 失业保险金, 职业技能培训补贴,
灵活就业人员社保补贴, 创业担保贷款, 公共就业服务. These exist, their conditions
are published, and they were the most useful thing in the product: in both live
tests, 成都 and 深圳, the top answer was "办失业登记，再申领失业保险金". Deleting them
would have removed real, correct information because it happened to be stored
beside invented information.

So: delete the invented, **verify** the real, and stop shipping anything that
cannot be opened and checked.

## Verified, one at a time, against the official text

| record | source | outcome |
|---|---|---|
| nat-001 失业保险金 | 失业保险条例 第十四条 (rst.sc.gov.cn) | its three conditions match the article **verbatim** |
| nat-002 职业培训补贴 | 就业补助资金管理办法 (gov.cn, 2024) | correct, and **missing a real limit**: 每人累计最多享受 3 次，同一职业不得重复 |
| nat-003 灵活就业社保补贴 | same | **factually wrong** — see below |
| nat-004 创业担保贷款 | 财政部答复 (jrs.mof.gov.cn, 2024) | scheme and subsidy verified; two conditions could NOT be verified and were replaced by an honest "由当地人社和经办银行认定" |
| nat-005 公共就业服务 | 就业促进法 第三十五条 | "免费…办理就业登记、失业登记" verified |

🔴 **nat-003 was wrong in a way that costs somebody a year.** It said the subsidy
runs "不超过 3 年" for everyone. The regulation gives 就业困难人员 up to three years
(extendable to retirement for those within five years of it) but gives
**离校 2 年内未就业高校毕业生 at most two**. A graduate reading the old record would
have expected a third year that does not exist.

That is the argument for verifying rather than deleting, in one line: the error
was invisible until somebody read the source.

## The shape now

`data/` ships only what is real. Every record's `source_ref` is an official URL
somebody can open, replacing `SAMPLE/national/nat-00X`, and `channel.online` is
`www.12333.gov.cn` rather than `sample.example`. Local listings and courses ship
not at all — they come from live search, labelled unverified, with a date and a
link.

`testdata/corpus/` holds the invented corpus. It did not go in the bin because
the tests, the 27 evaluation cases and the offline demo all need a corpus with
the shape of a real one, and 197 references across 16 files cite those ids.
Repointing fourteen `corpus.Load` call sites at a fixture is a change of one path
each; rewriting 197 assertions is how a safety net gets cut while looking
productive. The Dockerfile copies `data/` and nothing else, so nothing there
reaches a deployment.

**`corpus_is_sample` is now derived**, not a literal `true` in the HTTP layer. It
asks the data whether any record still cites `SAMPLE/`. Left as a literal it
would have kept a permanent 「演示语料」 badge over five real national schemes —
telling somebody not to act on the only things here they can act on.

## Proven by asking

The same question that used to answer with 锦禾精密部件, a company that does not
exist:

> 我在成都，工厂上个月关了，做了五年流水线，现在能做什么？

now answers with `nat-001` and its three real conditions, `live-002` 成都双流普工
周结 200–300 元 (published 2025-11-11), `live-001` 成都市人社局
http://cdhrss.chengdu.gov.cn/ — the real portal — and `live-007`, 四川省 2026 年
8–9 月补贴性职业技能培训第一批, 成都等六市共 383 个项目, published five days
earlier. Fabricated organisations in the answer: none.

The replacement is not merely more honest. It is more useful: 383 real training
places beat one invented school.

## What this cost

**The twelve procedure guides are gone**, and `knowledge_search` now returns
nothing. They explained real things — how a social-insurance transfer works, why
consecutive-month criteria read as zero after a move — but they were our
paraphrases with `SAMPLE/guide/` refs, and verifying twelve of them is its own
piece of work. Removing them is the conservative direction; restoring them means
sourcing each one the way the five schemes were sourced.

`criteria_explain` now has five records to explain rather than twenty-six. Those
five are the ones whose conditions are nationally published and stable, which is
the only kind this product could ever have explained honestly.

## Regression tests

| test | file | holds |
|------|------|-------|
| `TestShippedCorpusCarriesNoInventedRecords` | `internal/corpus/corpus_test.go` | no shipped record cites `SAMPLE/`; every source_ref is openable (`http…`); `IsSample()` is false for `data/` **and true for the fixture**, so the fence is not comparing nothing |
| `TestDocsAgreeWithTheLanguageOfTheCorpus` | same | still points at `data/`, deliberately: it is about what ships |
| `TestCorpusTallyIsNotWrittenIntoTheCopy` | `web/interface_test.go` | caught the first draft of the new copy writing "5 条" into the claim — the rule it enforces is the one this session keeps re-learning |

Every other test now loads `testdata/corpus`. All 16 packages green.
