# Node Daemon 接入与 Fake Node 验收契约

这是项目自有控制协议，**不是 Agent 原生适配的实现说明**。外部 Agent 的实际启动参数、SDK / RPC、取消语义和授权行为由 Node Adapter 各自核实。Gateway 不解析原生协议，也不因为名字为 Codex / Claude / Pi 就开启能力。

## 1. 连接与认证

先按整体设计完成 enrollment → 手机 preview / confirm → Node poll → challenge / prove，然后使用 Node bearer 的 Authorization header 连接 `/v1/ws/node`。Gateway 返回 `node.ready`，其中 epoch 是本次连接的栅栏代次。随后所有 Node 帧必须带相同 epoch；旧 socket 即使仍可写也不会被接受。

Node 和 client WSS 不互通。帧的 version 为 `weagent/1`；未知控制类型返回 PROTOCOL_UNSUPPORTED 并关闭连接，不能交给原生进程。Node 掉线时原生任务是否继续由本地策略决定，不能因为手机客户端退后台就终止。

上线同步只豁免一种没有原生会话可同步的情况：create operation 已明确失败，预留 Session 为 failed 且从未收到 Node source。`not_seen` 对账使这类操作失败时会释放本连接同步等待，后续重连也不再等待不存在的快照。已 delivered 却报告 not_seen 仍是 SOURCE_CONFLICT；具有 Node source 的失败会话和 unknown/reconciling 操作不适用此豁免。

## 2. 双向消息表

| 方向 | type | 提交 / 消费语义 |
| --- | --- | --- |
| Gateway → Node | node.ready | 提供 nodeId、epoch、serverTime、心跳间隔；不是运行任务 |
| Node ↔ Gateway | node.heartbeat | Node 发 nonce，Gateway 回相同 nonce 和 serverTime；不消耗业务序号 |
| Node → Gateway | node.inventory | 同 inventoryId 分片项目 / Agent 目录；complete 才替换完整镜像 |
| Gateway → Node | command | 预分配资源 ID、operationId、deadlineAt、nodeEpoch、严格类型 payload |
| Node → Gateway | command.ack | journal delivered / 原生 confirmed / 明确 rejected / unknown |
| Gateway → Node | command.query | 仅查询旧 journal，绝不能重新执行 |
| Node → Gateway | command.status | not_seen / delivered / confirmed / rejected / unknown |
| Node → Gateway | node.events | 同版本公开 message / item / diff.file，带稳定 sourceEventId / sourceSequence |
| Node → Gateway | node.session | 权威状态、当前 turn、能力快照、队列镜像；非任意索引导入 |
| Node → Gateway | node.request | 新的 approval / question 定义；Gateway 设置 pending / revision / decision |
| Node → Gateway | node.request.cancel | 指定当前请求取消及公开原因；Gateway 判定竞态后的状态 |
| Gateway → Node | events.ack | 该 session 已持久化的 sourceSequence 与派生事件末尾 gatewaySequence |
| Gateway → Node | error | 脱敏稳定错误码，不含原生全文 / token；Node 拒绝操作使用 command.ack |

Node 消息的时间只作为事件元数据，权限到期和 deadline 以校准后的 Gateway 时间判断。握手后用心跳 / 响应测得时钟偏移；本地时钟不可靠时拒绝执行时间敏感操作，不延长到期请求。

node.session、node.request、node.request.cancel 和 node.events 共用**每 session 一个连续 sourceSequence**，从 1 开始，跨连接 / 进程重启持久化。批内可以多个会话，但每个会话按 sourceSequence 提交与 ACK。目录、心跳、命令回执不占用这个序号。

## 3. 顺序与引用规则

1. Gateway 对 create 创建 Session / starting Turn；send 在命令持久 payload 预留 nextTurnId；queue 同样预留但不创建 active Turn。
2. Node 只能开始 Gateway 已分配的 turnId。开始下一轮时先发 node.session（指明新 turnId），Gateway 验证它来自本 Session 已接受的 command / queue 预留，结束上一 active Turn 后在同事务创建 / 激活新 Turn。随后才发该轮 message / request / diff 事件。不能先发一个未知 turn 的消息再要求 Gateway 自动补造。
3. Native accepted 回执和业务状态事件可能在两条处理路径上先后到达；Gateway 用 operation 与预留 ID 对账，不依赖跨类型网络到达顺序。confirmed 的 result 中各 ID 必须与原命令预留 / 目标一致，不接受跨会话替换。
4. node.request 仅创建/幂等重放定义，不接受 Node 指定 userId、decision 或 Gateway revision。相同 requestId 改 options/required 必须作为协议冲突处理；若原生重新提问，使用新 requestId。
5. NodeSessionState 的 queue 只能包含已在本地 journal 接受的命令项，文本、operationId、顺序不得任意漂移；capabilityRevision 单调递增，重连不能回退。
6. node.events 里的 item.upsert/request 引用只能指向同 session / turn 已授权请求。Diff fileId 必须先提交快照，且 path 规范化为相对 POSIX 展示路径，拒绝绝对路径、反斜杠和 `..` 段；Gateway 不执行文件 IO。
7. source 重发保持 ID、序号和规范化内容摘要不变。Gateway 分配的 createdAt / revision 等派生字段不计入 Node source 摘要；摘要取去除连接 epoch 后的规范化原始 source 消息。重连更换 epoch 不造成 SOURCE_CONFLICT。
8. node.session 只让 Gateway 做确定的状态转换：进程丢失且不能继续时 Session closed；尚未结束的 Turn 记 failed 并给出公开原因，已 completed 的 Turn 不被覆盖。临时离线不能发送 closed 作为“连接状态”。

## 4. 本地 journal / spool

- journal 持久化 operationId、命令规范化 hash、expected turn / revision、deadline、当前阶段、原生关联 ID 与最终回执；API Key 和私钥不混入可上传回执。
- delivered 必须晚于 journal fsync；原生调用前记录执行意图。Adapter 自带幂等时传同一个稳定 key；没有则保留不可消除的原生成功 / journal 未写崩溃窗口，回 unknown。
- 同 ID 不同 hash 拒绝；同 ID 相同 hash 读取原回执，不执行第二次。command.query 的 not_seen 仅当完整 journal 能证明从未进入去重入口；journal 损坏 / 丢失不能返回 not_seen。
- spool 先落本地磁盘再上行，commit ACK 后才能清理；在 Gateway 不可用期间保持有界磁盘队列。到配额上限应暂停上游采集 / 禁止新任务并报告需要干预，不静默丢必需的请求或最终状态。
- epoch 只约束当前通道，不删历史 journal；旧 epoch 未确认的命令重新连接后只查询，不隐式执行。

## 5. 独立 Fake Node 实现与验收

不需要真实 Agent 即可验收 Gateway：Fake Node 用相同身份、HTTP 和 WS，维护一个可重启的本地 journal/spool fixture；提供受测试进程控制的故障点，而不是公开后门 API。

必要脚本：创建并流式完成；single/multiple/text 问题；审批拒绝；排队与取消；能力变更；断连接替换；commit 前后 / ACK 丢失；旧 epoch 回执；同 source 改正文；Node 重启后去重；native accept 后 journal 终态未写；磁盘 / 写队列满。必须检查实际 HTTP/WS 往返、DB 行、cursor 与原生模拟调用次数，不能仅看 UI toast 或模拟计时器。

已实现 `cmd/fake-node` 和 `internal/fakenode`，使用相同 HTTP / WS 协议、Ed25519 身份和持久 JSON journal / spool。`internal/gateway/fakenode_test.go` 真正启动、结束并重启独立子进程，核验同操作只模拟调用一次、接受后崩溃保留 unknown，不与 Gateway 共享内存状态。运行步骤见 [running.md](running.md)。

Fake Node 提供普通完成、`[hold]`、`[question]`、`[approval]` 场景；更完整的动态多选验证、背压、旧 epoch、保留期等由 Gateway 协议 fixture 覆盖。它不是正式 Node Daemon：真实 Agent discovery / acceptance / cancel、长期 journal compaction、磁盘断电一致性及保留期过后残余本地会话的人工清理仍需 Adapter 独立验收。
