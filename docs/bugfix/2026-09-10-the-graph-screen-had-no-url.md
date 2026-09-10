# 猎源图谱的页面上线了，但没有任何 URL 指向它

**日期**：2026-09-10 · **发现于**：`a534bfd` 部署到 `jobs.heros-agent.space` 之后的收尾核对
**影响面**：招聘方看不到花名册、组织架构图和可拖拽关系图。对话里的 17 个工具不受影响。
**严重程度**：中。没有数据损坏、没有泄漏；是一个**完整建成但用户够不着**的功能。

## 现象

P8 交付了 `internal/leadgraph/webui.go`（`Handler`）、`web/graph.html.tmpl`、`graph.css`、
`graph.js`，`webui_test.go` 11 条用例全绿。部署后在生产上找不到这张图。

全仓库搜 `leadgraph.Handler`，只有一个调用点：

```
internal/leadgraph/webui_test.go:42
```

产品代码里**零调用**。同时 `web/static/` 里没有任何一处链到它。

## 根因

分两层。

**表层**：`httpapi` 只注册了导入路由 `POST /api/sessions/{id}/graph/imports`，没有注册页面路由。
写 P8 时页面和它的宿主是两件事，`Handler` 刻意不认识 cookie 和账号（见 webui.go 的注释：
"authentication is somebody else's job"）——这个设计是对的，但"somebody else"一直没到场。

**深层**：**测试自己充当了唯一的消费者**。`webui_test.go` 直接调 `leadgraph.Handler`，
所以"页面能渲染"这件事被证明了，"页面能被打开"这件事从来没有人问过。一个包只被它自己的
测试调用，静态检查不会说什么，覆盖率也很好看。

这是同一个形状在这个代码库里的又一次复发：**功能齐全，但没有生产者**。

**制度层**：`isOpenPath` 的兜底规则是 `!strings.HasPrefix(path, "/api/")` ——
"不是 /api/ 就是公开的"。这条规则写下时是对的（`/api/` 之外只有 `web/static` 里的文件），
但它对**未来会挂在 /app/ 下的东西**做了一个没人复核过的承诺。图谱页一旦挂上去，
默认就是公开的。修复必须同时改这条规则，否则修好可达性等于开了一个洞。

## 修复

1. `internal/httpapi/server.go`：挂 `GET /app/sessions/{id}/graph` 与
   `/graph/{rest...}`，由 `graphPage` 服务。身份沿用导入路由那两行——`ownedSession` +
   `ses.Role != domain.RoleRecruiter`，席位与团队由 `s.graphTeam(ses.SubjectID)` 推出，
   **和对话工具走同一条规则**（`tools.GraphTeamFor`）。View 经 request context 交给
   `leadgraph.Handler` 的 `resolve`，图谱包仍然不认识 cookie。
2. `internal/httpapi/auth.go`：`isOpenPath` 对 `/app/sessions/` 前缀返回 false。
   这是第二道独立的闸门——即使 `graphPage` 的检查日后被削弱，这个页面仍然需要账号才够得着。
3. `web/static/app.html` / `app.js` / `i18n.js`：聊天页头部加"猎源图谱"入口，
   **仅在 `recruiter` 角色下可见**，href 由 `syncGraphLink()` 按当前会话 id 生成。

## 验收与防退化

`internal/httpapi/graphpage_test.go`（8 条，全部走真 mux、真闸门、真归属检查）：

| 用例 | 守住什么 |
|---|---|
| `TestTheGraphScreenIsServedToTheRecruiterWhoOwnsIt` | 页面可达，且花名册在**脚本之前**就在 markup 里 |
| `TestTheGraphScreenIsReachableWithoutTheTrailingSlash` | 少打一个斜杠不是 404，且落在带斜杠的形式上 |
| `TestTheGraphScreenServesItsOwnAssetsAndData` | `graph.css` / `graph.js` / `/data` 都在同一个 URL 下 |
| `TestTheGraphScreenAndTheConversationReadTheSameBook` | 账号**放进公司**再验——没有公司时两边会碰巧算出同一个团队 |
| `TestTheGraphScreenNeedsAnAccount` | 匿名请求拿不到（演练的是**闸门**，不是 handler） |
| `TestTheGraphScreenRefusesSomebodyElsesSession` | 别人的会话 id 不是入口 |
| `TestTheGraphScreenIsForTheEmployerRoleOnly` | 求职者角色打不开 |
| `TestTheAppShellLinksToAGraphURLThisServerActuallyServes` | **本文件存在的理由**：从发布出去的 `app.js` 里读出 href，拿它去打真 mux。链接烂掉和路由搬家都会让它红 |

另加 `web/interface_test.go: TestEveryHiddenToggledControlCanActuallyBeHidden`（见下一节）。

**变异演练**（逐条删掉守卫，确认对应用例变红，全部 `-count=1`）：**10/10 全红**。
包括从两侧演练链接那条：删掉 app.js 里的 href → 红；把路由改名 → 红。
以及把模板里的 `href="graph.css"` 改成 `/graph.css` → 红（报"页面向浏览器要 X，拿到 404"）。

## 顺带抓到的第二个 BUG：入口按钮对所有角色都可见

**现象**：加完入口按钮后在本地打开页面，DOM 里 `graphLink.hidden === true`，
但 `getComputedStyle` 是 `display: flex`，元素有 34×34 的盒子——**求职者角色也看得见它**，
点进去只会拿到 403。

**根因**：`.icon-btn` 设了 `display: inline-flex`，**盖掉了浏览器默认样式表里的
`[hidden] { display: none }`**。这个坑 `styles.css:540` 早就写过注释，并且维护了一份
"设了 display 又要靠 hidden 切换"的选择器登记（`.gate` / `.gate-field` / `.who`）——
我加了第四个控件，**没往那份登记里加**。登记表往"安全方向"腐烂：漏一行没人会红。

**修复**：`.icon-btn[hidden] { display: none; }`，按既有写法就地补，并把注释指回本文。

**为什么 Go 的围栏抓不到**：它算不了 CSS 层叠。所以新增
`web/interface_test.go: TestEveryHiddenToggledControlCanActuallyBeHidden` ——
扫 `app.html` 里所有带 `hidden` 属性的元素，凡是它的 class/id 在 `styles.css` 里设了
非 `none` 的 display，就必须有对应的 `[hidden]` 伴随规则。这条不再依赖谁记得更新登记表。
演练：删掉 `.icon-btn[hidden]` → 红，且报错直接点名是哪个选择器、哪个元素。

**教训**：**这个 bug 只有打开页面才看得见**。九条 Go 围栏全绿的时候它就在那儿。

## 留给下一个人的话

如果你在重构时看到 `graphPage` 里那句"URL 为什么带 session id"的注释，或者
`isOpenPath` 里 `/app/sessions/` 那三行，**它们不是多余的**。前者是让页面和对话读同一本账，
后者是让"不是 /api/ 就公开"这条老规则不要顺手把某个人的花名册发出去。
