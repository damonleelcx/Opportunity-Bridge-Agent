# agent 的事件从来没有进过服务端日志

**日期**：2026-09-11 · **发现于**：生产排障——`ses_0105` 的一轮没出卡片，想从日志确认「到底调没调工具」
**影响面**：所有版本、所有请求。从首个提交起，线上日志里**从来没有**任何工具调用、结束原因或审计事件。
**严重程度**：高。不影响用户功能，但「工具跑了、卡片没存」这类问题从服务端**无法定位**；招聘方审计事件（谁搜了候选池、联系了谁、查了哪家外部人才库）**没有任何服务端痕迹**。
**拍板**：2026-09-11，见下文「决策」。

## 现象

`run_1789096804305058063` 前后 120 分钟的 pod 日志只有这种行：

```
time=2026-09-11T03:20:13.517Z level=INFO msg="http request" method=POST path=/api/sessions/ses_0105/messages status=200 duration_ms=9295
```

没有一行提到工具，run id 在日志里一次都没出现。最后只能靠读代码 + 浏览器里的步数，才证明那一轮「0 次工具调用」。

## 根因

**表层**：`obs.Recorder` 只有两种输出方式——`Subscribe`（回调）和 `MirrorTo`（写 JSON 行）。生产里：

| 位置 | 实际情况 |
|---|---|
| `agent.go` `rec.Subscribe(...)` | 唯一的订阅者：转成 `trace` 事件经 SSE 推给**发起这一轮的那个浏览器标签页**，刷新就没了 |
| `Recorder.MirrorTo` | 整个仓库**零调用**，连测试都没有 |
| `Result.Events` | HTTP handler 只取了 `res.Usage` 计费，事件列表被丢弃 |
| `agent.Agent` | 没有 logger 字段；`cmd/obagent/main.go` 里**没有任何 obs 接线** |
| slog 级别 | INFO，不是被过滤掉的 |

**深层**：这是「建成了，没有生产者」——而且是**两个**：

1. `MirrorTo` 写好了，没人调。
2. `OBA_TRANSCRIPT_LOG` 在 `config.go` 里被解析成 `cfg.TranscriptLog`，[10-observability.md](../10-observability.md) 和 [11-operations.md](../11-operations.md) 都写着它能「把 trace 镜像成 JSON 行」——**整个代码里没有一处读这个值**。首个提交 `2e93e63` 起就是这样。

同一个洞里还有三件事：

- 招聘方审计事件（`agent.candidate.searched` 等）走的是同一个 Recorder。`obs.go` 自己的注释说它们是「运维必须能单独审计的」——实际只到了浏览器。
- 三条结束路径**不发结束事件**：意图被灰度关闭、路由失败（`a.fail` 本身不发）、以及模型失败之外的失败。浏览器 trace 在这些情况下同样没有「本轮结束」。
- 整个仓库**没有 request_id**，项目规范要求 WARN/ERROR 必须带。

**制度层**：`cmd/obagent/producer_test.go` 开头列着「建成了没接线」的三次前科，但没有一条测到 obs 的输出端；而 agent 的测试断言的是 `res.Events`——**那正是浏览器那一份内存副本**，所以「trace 完整」的测试全绿时，服务端什么都没有。
**Owner**：首个提交 `2e93e63`（obs 骨架连同两个输出机制一起建好，没有接到进程日志）。

## 决策（拍板 2026-09-11）

| 问题 | 选择 | 为什么 |
|---|---|---|
| 写哪些事件 | **关键事件实时写**，字段白名单 | 实时写：一轮卡住/被杀时仍能看到停在哪个工具；只写关键事件：模型请求/返回、检查通过这类高频事件与「跑了什么」无关 |
| `candidate_ref` | **不写**，只写 `outreach_id` | 日志里不出现能对应到具体个人的标识；`outreach_id` 在数据库里能关联回去 |
| request_id | **这次一起补** | 规范要求；消息请求要能和 agent 事件关联 |

未选：每轮只写一行汇总（卡住/崩溃时没有过程）；全部事件原样写（会把分类器推理、检查结果引用的回答、上游错误原文写进日志）。

## 修复

| 改了什么 | 为什么 |
|---|---|
| `internal/obs/logsink.go`：`LogSink` + **`loggedEvents` 配置表**（事件 → 允许的字段） | 能进日志的范围一张表看全；加事件是一行，不是一个分支 |
| **从不写**：事件 message、工具参数（写 `args_hash`）、`candidate_ref` | message 在多个事件上是用户/分类器/模型写的文字 |
| `error.code` 只在形如 `UPPER_SNAKE` 时才写 | `codeOf` 从报错冒号前截取，`city: 成都 not found` 会得到 `city`——那是文本 |
| 每行带 `event.name`、`run_id`、`session_id`、`intent`、`step` | 按 run id 一次 grep 拿到整轮 |
| `Agent.Log` + `main.go` 传入 | 生产者 |
| **每条结束路径恰好一个结束事件**，带 `stop_reason` 与 **`cards_kept`** | 补上意图关闭、路由失败两条静默路径；`fail()` 统一发 `agent.run.failed`（模型失败原来在调用点发一次，移入后仍只发一次）；「工具跑了卡片没存」看这一行就够 |
| 中间件为每个请求**生成** `request_id`，写入响应头 `X-Request-Id` 和请求行；`Agent.Run` 把 run id 记到请求上，请求行带 `run_id` | 请求行与 agent 事件可关联。**从不采用**客户端传入的 `X-Request-Id`：否则调用方可伪造关联、往每行日志里塞文本 |
| **删除** `OBA_TRANSCRIPT_LOG` 配置项、`Recorder.MirrorTo` 及其 `writer` 字段、[10](../10-observability.md) 与 [11](../11-operations.md) 里的说明（拍板 2026-09-11） | 从来没生效过——设置了也被忽略，所以删除不影响任何现有部署；而接上它会把**全部事件带原文**写进文件，与本次选定的白名单范围相反。留着只会让人以为有一条能用的全量镜像 |
| `obs.ContextHandler`（`main.go` 根 logger）：带 context 记录的行自动加 `request_id`/`run_id`，已有的不重复加 | httpapi 里 11 处 WARN/ERROR 改成 `WarnContext/ErrorContext(r.Context(), …)`，因此都带上 request_id |

**日志量**：原来每轮 1 行；现在约 **3 + 2×工具调用次数** 行（开始、结束、每次工具请求与成功/失败、请求行），加上发生时才有的审计/审批/预算/重试行。

## 没做的、待决的

- 意图被灰度关闭的一轮，`stop_reason` 是 `model_refusal`——沿用返回结果与前端既有的 `StopRefused`，日志如实照抄，未改。
- `store`、`pg`、`leadgraph` 持久化和每日图谱任务里的 WARN/ERROR 不在请求链路上（后台或手里没有请求对象），不带 request_id。
- 「工具失败率」这类信号仍只在日志里，没有做成可外部读取的健康端点。

## 防退化

| 围栏 | 守住什么 |
|---|---|
| `obs.TestOnlyAllowlistedEventsAndFieldsReachTheLog` | 表外事件不写；表外字段、message 不写 |
| `obs.TestOnlyCodeShapedCodesAreLogged` | 只写形如 code 的 code；WARN 级别正确 |
| `obs.TestTheOutreachLineNeverNamesTheCandidate` | `candidate_ref` 永不进日志 |
| `obs.TestARequestContextAddsItsIDsExactlyOnce` | request_id 为服务端生成；request_id/run_id 各恰好一次 |
| `agent.TestASuccessfulToolCallIsLoggedUnderItsRunID` | 工具请求/成功行带 run_id、工具名、args_hash；`cards_kept`；**参数原文不在日志里** |
| `agent.TestARefusedTurnStillSaysItIsOver` | 意图关闭路径有结束行；浏览器 trace 恰好 1 个 |
| `agent.TestAFailedTurnStillSaysItIsOverOnce` | 模型失败路径有结束行、只有 code 没有报错原文、恰好 1 个 `run.failed` |
| `httpapi.TestAMessageTurnCanBeJoinedToItsRequestInTheLog` | 响应头 id；请求行带 request_id 与 run_id（服务端用**普通 handler**，排除 ContextHandler 这个冗余供给者）；工具行带同一个 request_id；伪造的客户端 id 不进日志 |
| `main.TestAgentEventsHaveAProducerInTheLog` | 根 logger 包了 ContextHandler；**agent 字面量内部**有 `Log: log`（`httpapi.Server` 也有这一句，整文件搜索是真空的） |

## 演练

全部 `-count=1`；每条先断言变异落地、测试确实执行且是**测试失败而非编译失败**（编译失败什么也证明不了），再逐字节恢复。**17/17 全红**，4 个被变异的源文件全部逐字节恢复。

| # | 变异 | 应变红的围栏 | 结果 |
|---|---|---|---|
| L1 | 表外事件也写 | `TestOnlyAllowlistedEventsAndFieldsReachTheLog` | ✅ |
| L2 | 所有字段都写 | 同上 | ✅ |
| L3 | 把 message 写进去 | 同上 | ✅ |
| L4 | 任何 code 都写 | `TestOnlyCodeShapedCodesAreLogged` | ✅ |
| L5 | 允许 `candidate_ref` | `TestTheOutreachLineNeverNamesTheCandidate` | ✅ |
| L6 | run_id 重复添加 | `TestARequestContextAddsItsIDsExactlyOnce` | ✅ |
| A1 | 不订阅日志输出端 | `TestASuccessfulToolCallIsLoggedUnderItsRunID` | ✅ |
| A2 | 去掉 `cards_kept` | 同上 | ✅ |
| A3 | 意图关闭路径不发结束事件 | `TestARefusedTurnStillSaysItIsOver` | ✅ |
| A4 | `fail()` 不发结束事件 | `TestAFailedTurnStillSaysItIsOverOnce` | ✅ |
| A5 | `run.failed` 发两次 | 同上 | ✅ |
| H1 | 不把 run id 记到请求上 | `TestAMessageTurnCanBeJoinedToItsRequestInTheLog` | ✅ |
| H2 | 请求行去掉 `run_id` | 同上 | ✅ |
| H3 | 请求行去掉 `request_id` | 同上 | ✅ |
| H4 | 采用客户端的 `X-Request-Id` | 同上 | ✅ |
| C1 | 根 logger 不包 ContextHandler | `TestAgentEventsHaveAProducerInTheLog` | ✅ |
| C2 | agent 不给 logger | 同上 | ✅ |

**第一版 httpapi 围栏是真空的，写演练时发现**：请求行的 `run_id` 有两个供给者——中间件自己加的属性、以及 `ContextHandler` 从 context 补的。测试里服务端也用了 ContextHandler，删掉中间件那段仍然全绿。改成服务端用普通 handler 后，H2/H3 才真的变红。

## 验收

- `GOWORK=off go test ./...` 全绿，演练结束后在恢复的代码上复跑仍全绿。
- **生产环境尚未验证**：需要合并并部署后，发一轮真实对话，用 run id grep `kubectl logs`。
