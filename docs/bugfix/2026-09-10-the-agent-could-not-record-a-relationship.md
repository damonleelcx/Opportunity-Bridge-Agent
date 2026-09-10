# 阿桥记不了一条关系线，所以引荐路径永远是空的

**日期**：2026-09-10 · **发现于**：在生产上给用户做端到端演示时
**影响面**：`PathsTo`（引荐路径）——PRD §8.2 自己称为「这个产品最值钱的视图」——**在结构上永远返回空**。
**严重程度**：高。不是错答，是**一个核心视图从未可用过**，而且它的空答案和正确答案长得一模一样。

## 现象

演示时对阿桥说：

> 「鲸峰科技的王五是 c 业务组组长，同组还有高级工程师张三；星轨智能的赵六是算法总监，
> **他跟张三是前同事，两个人很熟**。」

然后：「张三我熟，算 3。我怎么能够到赵六？」

阿桥答：**「从你现在记的人出发，够不到他。」**

图上确实有 2 条「关系」，但都是"谁属于哪个组"的**结构线**（`web/graph.js` 派生出来的），
张三↔赵六那条**根本不存在**。

## 根因

```
UpsertEdge   → 全仓库零个非测试调用点
TurnInput    → 只有 Nodes，没有边
record_turn  → schema 里没有任何表达"关系"的字段
RecordTurn   → 从不碰边
```

阿桥**在结构上无法记录任何一条人与人之间的关系线**。

这是"建成了，没有生产者"在这个功能里的第四次，也是最贵的一次：
存储层、路径算法、图上的画法、降级清单全都写好并且有围栏，
**唯独没有人往里写数据**。

**为什么一直没被发现**：`PathsTo` 的空答案是合法答案——「你还没记过认识谁」和
「这两个人之间没有路」都会返回空。所以它每一次都"正常工作"，
直到有人在真实数据上问了一个**答案已知**的问题。

**为什么单测发现不了**：`path_test.go` 自己用 `UpsertEdge` 造边，
所以它测的是"有边时能不能找到路"，从来没问过"边是怎么来的"。
测试自己充当了生产者——和 `leadgraph.Handler` 那次一模一样的形状。

## 修复

`TurnInput.Links []LinkInput`，随 `record_turn` 一起进来，**在节点之后应用**
（`UpsertEdge` 拒绝团队里不存在的端点，而一条线和它两端的人通常在同一句话里）。

**端点按"公司 + 姓名"解析，不用 id**：模型不可能知道一个它刚刚第一次描述的人的 id。
解析时**忽略 unit_path**——自然键里有路径，而"赵六"就是句子里的叫法，
不会是"星轨智能 › 算法平台组 › 赵六"。

**一个名字命中两条记录 = 拒绝，不是二选一**。画错人身上的一条线，比不画更糟。

**link 上没有 strength 字段**：两个人有多熟只能由人来记（`UpsertEdge` 本来就拒绝
非 user actor 写 strength），agent 从一句话里推断"很熟"正是这个产品不做的猜测。见 §8.2。

**画不上的线要报出来**（`TurnResult.Unlinked`，带原因 key），不能吞掉——
只有看得见的拒绝才是能被纠正的拒绝。

## 同时修的第二个（也是演示时看到的）

对话里那张图显示成**空图**，旁边回执写着「新建 9」。

原因：图被插在"本轮第一张图谱卡片之后"，而第一张卡通常是 `graph_reconcile`——
它**按设计一个字都不写**。等 `record_turn` 真写完，iframe 早就加载过了。
改成**本轮结束时**再插（`finalise`）。

顺带：`王五 · c业务组组长 · c业务组组长`——模型把 `role_title` 和 `duty` 填了同一个值，
卡片老实渲染两遍；相同就只显示一次。

## 防退化

| 围栏 | 守住什么 |
|---|---|
| `TestARelationshipHeardInAConversationBecomesAPathYouCanWalk` | **本文件存在的理由**：记下关系 → 评分 → `PathsTo` 真的走得通，一条断言链 |
| `TestTheModelCanDrawARelationshipThroughRecordTurn` | 走**真 schema 校验**，模型形状的调用真的能落边 |
| `TestTheModelCannotRateARelationshipThroughALink` | strength 进不来 |
| `TestALineToSomebodyUnknownIsReportedRatherThanDropped` | 画不上要报出来 |
| `TestALineToAnAmbiguousNameIsRefused` | 同名不猜 |
| `TestTheAgentCannotWriteRelationshipStrength` | 存储层那道闸门还在 |
| `TestTheGraphPictureIsShownOncePerTurnAndAfterItEnds` | 一轮一次 **且** 在写完之后 |
| `TestEveryUnlinkReasonHasAWord` | 原因 key 有中英文句子，从 `turn.go` 源码推导 |

**变异演练 7/7 全红**，每条先断言变异真的落地，全部 `-count=1`。

## 留给下一个人的话

`path_test.go` 里那些自己造边的用例**不是**这条链路的证据。
如果你要改 `record_turn` 的 schema，
`TestARelationshipHeardInAConversationBecomesAPathYouCanWalk` 是唯一一条
从"用户说的话"一路断言到"引荐路径给出答案"的用例——它红了就说明这个产品又回到了那天的状态。
