# 阿桥自己没法把猎源图谱给人看

**日期**：2026-09-10 · **发现于**：把图谱页挂上 URL 之后，用户提出「agent 本身要能在被问到时把东西呈现出来，包括那张图」
**影响面**：招聘方在对话里让阿桥梳理，只拿到一段话；它刚建好的结构进了折叠的「系统运行详情」。
**严重程度**：中偏高。功能全在，但**用户看不见它工作的结果**——这正是这个产品的卖点本身。

## 现象与根因

### 1. 十七个工具，一个结果卡片都没有

界面靠 `cardFor(tool, result)` 决定一次工具调用要不要在对话正文里画卡片。
猎源图谱的工具**一个都不在这张表里**。

根因是**边界两侧都以为对方管**：工具层交付时把「怎么展示」留给界面，界面从来没被告知有新工具。
和「图谱页没有 URL」是同一个形状：**建成了，没有生产者**。

**修**：每个暴露给模型的工具都有 case，**没有例外清单**。有东西可看的画卡片，
安静的给一行「已记下 / 已删掉」。例外清单是这种沉默一个工具一个工具爬回来的路。

### 2. 那张图进不了对话

**修**：`?embed=1` —— **同一张页面**去掉自己的页头和文字清单，用 iframe 放进卡片里。
不写第二份画图代码：两份画同一张图的代码，从任一份改动的那天起就开始互相不一致。

## 走查抓到的、只读代码看不见的

| # | 缺陷 | 为什么代码看不出来 |
|---|---|---|
| 3 | `t(key)` 找不到时**返回 key 本身**，所以 `t(k) \|\| fallback` 里的兜底永远不执行 | 屏幕上会出现 `graph.removed 3` 这种东西，不报错 |
| 4 | 架构卡抬头写「0 组织单元」，底下就列着一个组 | `units` 和 `units_mentioned` 分开计数；抬头只读了前者 |
| 5 | 详情面板把 `person` / `hearsay` / `duty` 直接打给中文读者 | 要点开一个节点才看得到 |
| 6 | 嵌入版用百分比分高度，body 没有确定高度所以**全不生效**：文档 619px 塞进 340px 的框，`详情` 面板掉到看不见的地方 | 没有报错，看起来就像「面板没做」 |
| 7 | 读者选了浅色，嵌进来的图是深色 | 图谱页只有 `prefers-color-scheme`，等于**永远跟随系统**；而阿桥的默认是浅色 |

第 4、5、6、7 条都是**打开页面才看得见**的。九条 Go 围栏全绿的时候它们都在。

## 防退化

| 围栏 | 守住什么 |
|---|---|
| `TestEveryLeadGraphToolIsPresentedInTheConversation` | 名单取自 `tools.LeadGraphToolNames()`，Go 侧加工具就红 |
| `TestEveryQuestionGoCanAskHasASentence` | 常量取自 `leadgraph`，加第三个问题就红 |
| `TestEveryStringTheInterfaceNamesExists` | 命名空间从字符串表**推导**，不手工维护 |
| `TestTheOrgChartCardCountsTheGroupsItDraws` | 抬头必须读 `units_mentioned` |
| `TestTheGraphPictureIsShownOncePerTurn` | 守卫的**读和写**都要在 |
| `TestTheEmbeddedViewIsTheSamePictureWithoutThePageChrome` | 尤其是 `#panel` 必须留下——`graph.js` 缺它直接 return |
| `TestOnlyEmbedEqualsOneTurnsTheChromeOff` | 只有 `embed=1` 生效 |
| `TestTheGraphPageCarriesTheThemeItWasAskedFor` | 三态 + 白名单（非法值不进属性） |
| `TestTheGraphPageDefinesEveryTokenInEveryThemeState` | 每个色值在三种主题下都有定义 |
| `TestTheEmbedFitsTheFrameItIsGiven` | 高度模型：根有确定高度 + flex 分配 |
| `TestThePictureTranslatesEveryEnumItShows` | 枚举取自 `leadgraph.go` 和 `store.go` 的源码 |
| `TestEveryHiddenToggledControlCanActuallyBeHidden` | 见 [上一份](2026-09-10-the-graph-screen-had-no-url.md) |

**变异演练**：G1 6/6、G2 9/9 全红，每条先断言变异真的落地，全部 `-count=1`。

## 演练自己抓到的三条真空围栏

1. 一条按 `t("…")` 找 key 的围栏，**漏掉了绝大多数卡片**——它们的 key 是写在辅助函数的调用点上的。改成按命名空间匹配。
2. 两条围栏匹配到了**自己的解释性注释**（`units_mentioned`、`height:74%`）。现在先剥注释再读。
3. 一条 `-run` 指向了还没写出来的测试名，`go test` 照样 exit 0，看起来像"围栏没红"。

> 一条能被散文满足（或者被散文弄红）的围栏，什么也没量。
