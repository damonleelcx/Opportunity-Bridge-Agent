# 图谱卡片画出来了，但没人能读它

**日期**：2026-09-10 · **发现于**：用户截图 —— 「the graph did not show」「this card should be drag-able by mouse」「the generated cards are gone after refreshing the page」
**影响面**：招聘方在对话里看到的关系图谱是一条几乎不可见的细带；滚页面会把它越滚越大且回不去；刷新之后**所有**图谱卡片消失。
**严重程度**：高。这三条合起来，等于这个产品最核心的那张图**在用户那边从来没真正可用过**。

## 用户视角的一句话

> 图没显示 —— 其实图一直在，只是被压成了 2px。

---

## 1. 图被压成了细带（「能看」）

### 现象与实测

在**线上实例**上量到的数字（不是读代码推的）：

| 项 | 修复前 | 修复后 |
|---|---|---|
| 卡片 iframe 高 | 340px | 480px |
| 筛选按钮条 | 49.8px | 35px |
| 「详情」面板（空态） | **160px** | 82px |
| 留给画布 | 178px | 318px |
| 实际画出的图宽 | **287px**（可用 758px） | 填满 |
| 屏幕上的节点直径 | **约 2px** | 14px |
| 屏幕上的标签高 | 约 6px | 15px |

### 根因（两层）

**表层**：`viewBox` 是常量 `0 0 1000 620`。在自己的页面上那个框是 `60vh`，常量没问题；在对话卡片里框是「又宽又扁的一条」，SVG 默认的 `xMidYMid meet` 就按**高度**把整张图缩下去。

**深一层，也是真正的那条**：节点半径 `R` 和标签字号是**世界坐标里的常量**。也就是说「把图塞进小框」和「把图上的字缩小」是同一个动作 —— 框一小，人名就跟着没了。这不是排版没调好，是**尺寸的度量单位选错了地方**。

**第三层，为什么面板吃掉了近一半**：`aside{min-height:10rem}` = 160px。这条规则对**页面**是对的（详情面板在图的旁边，40px 高的一列看着像坏了）；对**卡片**是错的（它在图的下面，一行字要花掉 160px）。嵌入版没有解除它，`flex: 0 0 30%` 根本压不下去。

### 修

- `fit()`：按**内容包围盒**取景，并把盒子**撑到**（绝不压缩）宿主框的长宽比，让 `meet` 不留黑边。它是**唯一**设置 `viewBox` 的地方 —— 常量初始值一并删掉，取景和代码相信的取景不会分家。
- `rescale()`：节点半径、标签字号、线宽改成**屏幕尺寸**恒定。
  这**没有**推翻文件头那条「Node radius is a constant」原则。那条说的是「每个节点大小相同，所以大小不能夹带对某个人的判断」—— 仍然成立；变的只是**在哪里量这个常量**：量在读者那边，不是量在数学那边。
- 嵌入版 CSS：筛选条瘦身（不是删掉 —— 独立页面没了之后，这是产品仅剩的筛选控件）；详情面板 `min-height:0` + 按内容伸缩；卡片 340→480px。
- 顺带修掉一个既有 BUG：滚轮缩放里 `var h = w * (H / W)` 用的是**常量**长宽比，`viewBox` 一旦不是 0.62 的比例，缩放就会把画面拉变形。改成读当前 `viewBox` 的比例。

---

## 2. 滚轮劫持 + 没法平移（「不劫持滚轮」「能拖」）

### 现象

`wheel` 处理器无条件 `preventDefault()`。在对话里，卡片是读者要**滚过去**的东西 —— 鼠标停在图上滚页面，页面纹丝不动，图却被悄悄放大。而**拖拽只能拖节点，不能拖画布**，所以放大之后没有任何办法把视野挪回来。

这就是第二张用户截图里「只剩王五和 b业务组两个节点」的成因：他没有缩放，他只是想往下滚。

> **一个进得去、出不来的视口，比没有视口更糟。**
> 缩放和平移是一对；只做一半，那一半就变成陷阱。

### 修

- 缩放改到 `⌘/Ctrl + 滚轮`，裸滚轮直接 `return`，页面拿回自己的滚动。macOS 触控板的捏合手势本来就带 `ctrlKey`，所以「捏合缩放」不需要额外规则就还能用。
- 新增画布平移：在 svg 背景上 `pointerdown` 拖动改 `viewBox` 原点。节点先拿到事件（节点自己的监听器先触发），背景监听器看到 `drag` 已置位就让位 —— 否则拖人会把图从他脚下抽走。
- `重置` 从「恢复常量 viewBox」改成「重新取景」。恢复常量在卡片里等于把读者送回不可读的状态。
- `.hint` 在嵌入版从 `display:none` 改回一行紧凑文案，并写明这两个手势。**没有可见提示的手势等于不存在的功能**。

### 实测抓到、只读代码看不见的

**⌘+滚轮一次就把 `viewBox` 变成 `NaN NaN 483 392`，图永久消失。**

`point()` 拿 `svg.getBoundingClientRect().width` 当除数。一个**刚被 reveal 的**、**框还没布局的**、或者**在隐藏标签页里的** svg，量出来都是 `0×0` —— 但它完全是真实存在的。除以 0 得 `Infinity`，缩放里的 `p.x - (p.x - vb.x) * k` 于是算出 `Infinity - Infinity = NaN`，直接写进 `viewBox`；而且是**永久**的，因为之后每一次平移和缩放都会把这个 `NaN` 读回来再产出一个新的。

这是 `point()` 里的旧洞，被平移复用之后才容易踩到。修法是量不到框时返回当前 `viewBox` 的中心。

**它是被「往一张刚 reload 的卡片上派发一个 wheel 事件」抓到的，不是被读代码抓到的。**

---

## 3. 刷新之后卡片全没了

### 根因

服务端 `agent.cardBearingTools` 是一张**手写的 7 个工具的白名单**，只有名单里的工具结果会被写进会话记录。**17 个猎源图谱工具一个都不在里面。**

这张表自己的注释里，早就把这个失败模式一字不差地写下来了：

> *"a renderer with no entry here draws on a live turn and vanishes on reload, which is the bug this exists to fix."*

然后 17 个渲染器被加进了 `cardFor()`，**一个都没有加进这张表**。

**这是同一个形状的第二次**：[上一份 bugfix](2026-09-10-the-agent-could-not-show-the-graph.md) 修的是客户端 `cardFor` 缺清单，并给它加了围栏（`TestEveryLeadGraphToolIsPresentedInTheConversation`，名单取自 `tools.LeadGraphToolNames()`）。**边界另一侧的那张表没人看，也没有围栏。**

> 一个边界两侧各有一张手工维护的清单，**总会在没人看的那一侧先烂掉**。

**修**：服务端那半改成**从 `tools.LeadGraphToolNames()` 派生**，而不是再列一遍。这是**删掉第二张清单**，不是再加一道围栏去看着它。

### 还有一半在客户端

即使卡片存下来了，`openSession()` 的回放循环只认识 `cardFor`，所以重开会话会画出全部图谱卡片、**唯独没有那张图**。改成调用实时轮次用的**同一个** `showGraphPicture()`。

---

## 4. 独立页面下线（拍板 2026-09-10）

用户原话：**「i don't want a separate page like this. everything should be within the chat and handled by the agent」**

`/app/sessions/{id}/graph/` 现在返回 404，只保留卡片要用的 `?embed=1` 及其资源与快照。

**闸门放在 `internal/httpapi`，不放在 `leadgraph`**：那个包的包注释写明它要能整体搬到独立仓库，而且它那张页面本身没有问题。「这个产品对外暴露哪些界面」是路由层的决定 —— 所以 `leadgraph` 继续对任何挂载它的人提供完整页面，它自己的围栏也继续绿。

同批移除：每张卡片底部的「打开完整图谱 →」、页头的图谱入口按钮、以及它们用到的两条文案（`graph.open` / `ctl.graph`）和孤儿 CSS `.gcard-foot`。**链接必须和 404 同批落地** —— 只做一半，产品里就留着一堆指向 404 的控件，比改之前更糟。

> ⚠️ **随页面一起下线的三样东西**：线索看板（带窗口期天数、出处）、组织变动告警列表、完整花名册。
> 对话里有等价入口（`lead_board` → 线索卡片、`stale_scan` → 待办卡片、`graph_query` / `org_chart` → 名册与架构卡片），但**页面版更丰富**（窗口期剩余天数、团队最近一次联系、多源出处）。
> 这部分**尚未**回流到对话卡片里，是**已知缺口**，需要单独拍板。

---

## 防退化

| 围栏 | 守住什么 | 演练 |
|---|---|---|
| `leadgraph.TestThePictureIsFramedToTheBoxItIsGiven` | 取景来自内容与真实框，`fit()` 在 reveal 之后**无条件**跑一次，`重置` 不恢复常量 | ✅ G1 / G1b |
| `leadgraph.TestABareWheelDoesNotStealThePagesScroll` | 修饰键判断必须在 `preventDefault` **之前**且必须 `return` | ✅ G2 |
| `leadgraph.TestTheBackgroundOfThePictureCanBePanned` | 拖背景改 `viewBox` 而不是改节点；节点按下时背景让位 | ✅ G3 / G3b |
| `leadgraph.TestAnUnmeasurableFrameCannotPoisonTheViewBox` | 除以宽度**之前**先判空 | ✅ G5 |
| `leadgraph.TestNothingOnTheGraphEncodesImportance`（已放宽） | 半径只能是 `R` 或 `R * k`（白名单，不是黑名单）—— 不变量没放宽，只是换了表达式 | ✅ G4 |
| `agent.TestEveryGraphToolTheModelCanCallSurvivesAReload` | 服务端保留名单必须覆盖 `LeadGraphToolNames()` 全部；并断言它**能说不** | ✅ G6 |
| `web.TestAReopenedConversationRedrawsTheGraphPicture` | 回放必须调用**同一个** `showGraphPicture` 并标记轮次 | ✅ D1 / D2 |
| `httpapi.TestTheStandaloneGraphScreenIsGone` | 六种拼法全 404 且 404 里说明去哪儿找；同时 `?embed=1` 仍然可用 | ✅ D3 |
| `httpapi.TestTheRedirectToTheSlashedFormKeepsTheQuery` | 补斜杠的 301 必须带上 query，否则卡片被重定向进 404 | ✅ D4 |
| `httpapi.TestNothingLinksToTheStandaloneGraphScreen` | app.js / app.html / i18n.js 里都不许再指向它 | ✅ D5 / D6 |

演练全部 `-count=1`，且**每条都先断言变异真的落进了文件**再跑测试。

### 演练自己抓到的一条真空围栏

`TestThePictureIsFramedToTheBoxItIsGiven` 第一版查的是「reveal 之后的文本里含 `fit();`」。删掉那次真正的调用之后它**仍然绿** —— 因为后面 `ResizeObserver` 里还有一句 `if (!touched) fit();` 也含这个子串。改成「在 `ResizeObserver` 之前、模块顶层缩进的**无条件** `fit();`」才真的能量到东西。

> 一条能被「另一处恰好长得像的代码」满足的围栏，什么也没量。

## 验收

- `go test ./...` 全绿（`GOWORK=off`，本仓不在仓库根的 `go.work` 里）。
- 图形与交互在**本地 harness**（内存态 store + 真实模板/CSS/JS，卡片框 758×480）上实测：节点 14px、标签 15px、9 个节点全部可读；裸滚轮 `defaultPrevented=false` 且视图不动；`⌘+滚轮` 缩放且数值全为有限数、节点仍 14px；**真实鼠标拖拽**背景平移 `dx=-257.6 / dy=+120.6`、无节点被拖动；点击节点填充详情面板。
### 端到端活体验收（2026-09-10，生产 jobs.heros-agent.space，镜像 `76793b0-085706`）

在**真实部署**上跑完整条路径，不是组件级模拟。会话 `ses_0105`，账号 `sub_0040`。

**部署前对照组**（线上旧版）：`/app.js` 含 `graphLinkRow` / `syncGraphLink`、不含 `showGraphPicture`；
`styles.css` 是 `height: 340px`；`i18n.js` 含 `"graph.open"`。
`GET /api/sessions/ses_0105` → **4 个轮次，`turnsWithCards: 0`** ← 这就是用户报的「刷新卡片全没了」在生产数据上的形态。

| 断言 | 结果 |
|---|---|
| 公开资产：6 项该消失的全消失、4 项该出现的全出现 | **10/10** |
| 独立页面 5 种拼法（`/`、无斜杠、`?embed=0`、`?embed=true`、`?theme=dark`） | 全部 **404**，且 404 正文都带「去对话里找」的说明；`class="wrap"` 不出现 |
| `?embed=1`（带/不带斜杠）+ `graph.css` / `graph.js` / `data` | **200**，含 `id="graph"`，不含花名册 |
| 发一轮新对话（`org_chart` + `rate_contact`） | 实时画出 3 张卡片：组织架构 / 已记下关系强度 / **关系图谱** |
| 卡片里那张图（**这是最初「图没显示」的判据**） | 框 674×480 → 筛选 35 / 提示 24 / 详情 82 / **画布 316**；**9 个节点全部有标签**；**节点直径 14px、标签 15px**；viewBox 全为有限数 |
| 裸滚轮停在图上 | 对话区**滚动了**（+5.5px），图的 viewBox **完全没变** |
| ⌘/Ctrl + 滚轮 | `defaultPrevented=true`、放大生效、数值全有限、**节点仍 14px**（`rescale` 生效，NaN 洞已堵） |
| **真实鼠标**拖背景 | 平移 dx=-200.5 / dy=-129.9，缩放未变，9 节点仍在，无节点被拖动 |
| 服务端保留 | `GET /api/sessions/ses_0105` → 新轮次 `cards: ["org_chart","rate_contact"]`（此前为空） |
| **整页刷新后** | 3 张卡片全部重绘 + **图也重绘**（9 节点 / 14px / stage `on` / viewBox 有限）；无残留链接、无页头图谱入口、导入控件正常 |

> ⚠️ **不可追补**：该会话更早的 4 个轮次刷新后仍然没有卡片。它们是在旧版上跑的，
> 当时服务端**根本没有写下**那些结果——修复只对修复之后发生的轮次生效，这是数据缺失，不是渲染问题。
