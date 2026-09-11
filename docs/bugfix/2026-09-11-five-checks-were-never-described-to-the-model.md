# 五项检查从来没有告诉模型它们查什么

**日期**：2026-09-11 · **发现于**：给 talent_sourcing 加校验器 `no_protected_attribute_screening`（PR #50）时，核对 `verifierPlain` 发现。代码审查发现，不是线上报障。
**影响面**：
- `individual_pathway`（居民主流程）的 `next_step_is_tracked`
- `talent_sourcing`（招聘方全部轮次）的 `no_candidate_scoring`、`candidate_anonymity`、`outreach_is_an_ask`、`external_leads_not_candidates`

**严重程度**：中低。
- 检查本身照常运行、照常拦截，没有任何违规回答因此漏过去。
- 损失在于模型事先不知道规则，只能先违规、被打回、再重写，每次多一次模型调用，多花时间也多花钱。
- 其中三项是 block 级（`no_candidate_scoring`、`candidate_anonymity`、`external_leads_not_candidates`）。重写后仍违规时，整条回答会被替换成护栏自己的提示。

## 模型看到了什么

系统提示第 2 层的「THIS TURN IS CHECKED FOR」下面：

```
- no_candidate_scoring: see docs/13-guardrails.md
```

模型只看得到提示词，读不到仓库里的文件。而且 `docs/13-guardrails.md` **在仓库里从来就不存在**：首个提交 `2e93e63` 里 `docs/13` 就是 `13-name-and-voice.md`，护栏文档一直是 `07-guardrails-and-verifiers.md`。

## 这是有意设计吗？

不是。`verifierPlain` 自己的注释写着设计意图：「Telling it the test is not cheating: an unstated test is just a retry tax」。也就是说，每项检查都应该用一句话讲给模型。缺说明的这五项违背了这个意图，不是另一种取舍。

## 根因

**表层**：`guardrail` 注册表里有 22 个校验器，`prompt.verifierPlain` 的 switch 只写了 17 个 case，其余 5 个落到兜底的那一行。

**深层**：说明文字和校验器本身分放在两个包里，由人手工保持一致，没有任何东西把两边绑在一起。兜底那一行从第一天起就指向一个不存在的文件，所以「漏写 case」的后果不是报错，而是一句看起来像引用、其实什么也没说的话。

**制度层**：唯一相关的测试 `TestIntentLayerIsRenderedFromTheRegistry` 有两个盲点：
- 它只检查校验器的**名字**是否出现在提示词里。名字印在冒号前面，后面跟的是说明还是兜底文字，这个检查都成立，所以它**不可能变红**。
- 它只测 `supply_demand_insight` 一个意图，而这个意图的校验器恰好全部都有 case。

**Owner**：三次新增校验器的提交都没有同步补说明。兜底行和那条测不出问题的测试，出自首个提交。

| 提交 | 日期 | 新增的校验器 |
|---|---|---|
| `3402415` | 2026-08-28 | `next_step_is_tracked` |
| `6356467` | 2026-08-31 | `no_candidate_scoring`、`candidate_anonymity`、`outreach_is_an_ask` |
| `e7ff5c2` | 2026-08-31 | `external_leads_not_candidates` |
| `2e93e63` | 2026-08-28 | 兜底行 `see docs/13-guardrails.md` 和那条测不出问题的测试 |

**为什么之前没人发现**：检查照常生效，违规回答照常被打回重写，从外面看一切正常。多出来的那一次重写，只在追踪记录里表现为「redrafted」，而重写本来就是护栏的正常行为。

## 修复

| 改了什么 | 为什么 |
|---|---|
| 给五项各补一句说明，**照着各自函数实际查的内容写** | 例如 `candidate_anonymity` 写的是「手机号、邮箱、证件号、卡号」，因为 `piiPatterns` 只认这四类，**不认姓名**。说明写得比检查宽，模型就会以为有一道并不存在的防线 |
| 兜底文字改为 `not described here; see docs/07-guardrails-and-verifiers.md.`，并加注释说明原因、链接本文 | 兜底现在只会被注册表里不存在的名字触发，而这种情况 `Verify` 自己会报 `UNKNOWN_VERIFIER`。它指向的文件必须真实存在 |
| 新 case 加在 switch 末尾 | PR #50 在 `no_cohort_downranking` 后面插了一个 case，两处改动不相邻，可以各自合并 |

**未采用的方案**：把说明挪进 `guardrail` 注册表，和校验函数放在一起。这样只有一个真相源，结构上就不可能再漏。

没做的原因：要改 `guardrail.Verifier` 的类型和注册表的形状，超出这次修复的范围。新围栏已经能让「注册了却没有说明」立刻变红。如果同类漂移第三次出现，再做这次重构。

## 防退化

围栏：[internal/prompt/prompt_test.go](../../internal/prompt/prompt_test.go) 中的 `TestEveryRegisteredVerifierIsDescribedToTheModel`。

- 遍历 `guardrail.VerifierNames()`，把**全部已注册的校验器**渲染进 `prompt.IntentLayer`，逐个断言说明不是兜底文字。
- 兜底文字**从代码里读出来**：渲染一个不存在的名字拿到它，而不是在测试里再抄一遍。这样以后改兜底措辞，测试也不会变成「跟空字符串比」。
- 兜底文字里引用的每个 `docs/…` 路径都必须真实存在。

**修复前的代码上**（把 `prompt.go` 临时换回 `origin/main` 版本跑，跑完逐字节恢复）：五个校验器各报一条，外加「the fallback cites docs/13-guardrails.md, which does not exist」，共 6 条，与本文描述一致。

**变异演练**（全部 `-count=1`；每条先断言变异已写入、测试确实跑了、失败的是测试而不是编译，再恢复并逐字节比对）：

| # | 变异 | 结果 |
|---|---|---|
| M1 | 删掉 `no_candidate_scoring` 的 case | ✅ 红，点名 `no_candidate_scoring` |
| M2 | 让 `next_step_is_tracked` 的 case 匹配不到 | ✅ 红，点名 `next_step_is_tracked` |
| M3 | 兜底文字改回 `see docs/13-guardrails.md` | ✅ 红，「the fallback cites docs/13-guardrails.md, which does not exist」 |

## 验收

- `GOWORK=off go test ./...`：21 个包全部通过。
- **未做线上活体验证**：提示词不会出现在界面或日志里，线上没有可读的观察点。这次的验证依据是对渲染结果逐条断言，被测对象就是生产环境拼提示词时调用的同一个 `IntentLayer`。
