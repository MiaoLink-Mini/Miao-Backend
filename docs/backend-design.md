# WeAgent 后端实施设计

状态：2026-09-06 的设计及实施基线；Go Gateway 和独立 Fake Node 已落地，运行说明见 [running.md](running.md)，实际验收与未覆盖范围见 [verification.md](verification.md)。以现有前端代码为产品事实；辅助参考不得覆盖已经存在的交互。所有协议名称均为本项目新定义，不能当作任何 Agent 的现成 API。

## 1. 产品目标与范围

手机端负责查看设备 / 项目 / Agent、创建与继续会话、查看流式历史、取消、处理审批和结构化提问、查看 Diff / Plan / Usage 与通知。公网 Gateway 负责身份、权限、路由、索引、持久化和断线恢复。Node Daemon 负责本地 Agent 生命周期、能力发现、原生协议转换和命令去重。

本版纳入前端现有的 `send`、`cancel`、`approval`、`question`、`queue`、`diff`、`plan`、`usage`；具体是否可用由 Node 提供的**会话级能力快照**决定。服务端标题搜索与节点 / 项目 / Agent / 状态过滤纳入本版，但不做正文全文检索。

不把 `/help /plan /diff /usage /queue /latest` 当作远程命令：当前这些是前端本地快捷操作。模型切换、真实原生命令目录、附件、steer、原生会话接管 / 分支 / 恢复、后台任务、任意文件浏览均另行定义能力协议后实施。没有“根据品牌猜测能力”的分支。

## 2. 架构与模块

```text
微信小程序（一个应用级连接；页面只是订阅者）
   ├─ HTTPS：登录、列表、历史分页、命令提交、回执查询
   └─ WSS /v1/ws/client：变化通知、会话事件、操作回执
                  │
Go Gateway（单实例）
   ├─ auth / access：微信身份兑换、令牌、资源级授权
   ├─ pairing：配对预览、确认、撤销、Node 公钥挑战
   ├─ catalog：Node / Project / Agent / Session 索引
   ├─ commands：幂等、前置条件、回执、超时对账
   ├─ interactions：审批 + 提问 + 到期处理
   ├─ events：落库、去重、序号、投影、checkpoint
   ├─ realtime：Node Registry、连接代次、订阅、有界广播
   ├─ inbox：待处理 / 通知 / 元数据审计
   └─ maintenance：到期清理、保留策略、优雅停机
                  │
           PostgreSQL（唯一持久状态）
                  │
   WSS /v1/ws/node（Node 主动连出，每台一个连接）
                  │
Node Daemon：身份私钥、命令 journal、事件 spool、项目白名单
   └─ Agent Adapter：Codex / Claude Code / Pi / 后续 Agent
      └─ 本地原生进程；Agent 登录凭据只留在设备
```

建议 Go 目录：`cmd/gateway`、`internal/{auth,access,pairing,catalog,command,interaction,event,realtime,store,config,observability}`、`internal/protocol/generated`、`test/fakenode`。模块通过明确接口通信；事务协调位于用例层，不让 HTTP handler 直接拼多表写入。初版显式 SQL + pgx，不同时引入 ORM 与 sqlc；生成类型后再评估 sqlc，迁移来源仍然唯一。

## 3. 权威性与资源模型

| 信息 | 权威来源 | Gateway 行为 |
| --- | --- | --- |
| 用户、Node 归属、撤销 | Gateway | 每次读写和 WS 订阅 / 广播检查归属 |
| 项目有效性、Agent 可用性、原生运行状态 | Node | 校验作用域、维护带版本的镜像 |
| 会话模式 / 能力 | Node 协议 + Gateway 权限交集 | readonly 永远不能控制；旧能力修订不能提交 |
| 会话事件顺序 / 用户变化 revision | Gateway | 事务内分配连续安全整数 |
| 命令是否收到、原生是否接受 | Node journal / Adapter | 不能由 HTTP 成功或 socket write 成功推断 |
| 在线状态 | 连接注册表 + 当前 epoch + 心跳 | 数据库只存 lastSeen，不存永久 online=true |
| 费用、token 使用量 | Adapter 明确上报 | 缺失为 null，不按品牌 / 文本猜测，不当作账单 |

核心模型为 User、NodeAccess、Node、Project、Agent、Session、Turn、Operation、InteractionRequest、QueueItem、SessionEvent、TimelineItem、Checkpoint、DiffFile、Notification、Audit。Session 是可继续的容器；Turn 才是一次执行。标题、名称、徽标不充当资源 ID。

所有资源 ID 都是不透明字符串；服务端生成的 ID 必须不可预测。`operation.id` 特例由客户端 `Idempotency-Key` 提供，数据库主键为 `(user_id,id)`，Node journal 在该设备固定归属命名空间中识别它。字段用 camelCase；时间用 UTC RFC3339；`sequence/revision` 为 0..9007199254740991 的整数，事件序号从 1 开始；将来扩大范围必须升级协议。

## 4. 状态机：不得混成一个 online / busy

### Session / Turn

```text
idle → running ↔ waiting_approval / waiting_input
            └→ cancelling → cancelled
            └→ completed | failed
completed / cancelled / failed → running（新 turnId，原 sessionId）
任意运行状态 → closed（Node 确认原生进程已退出且不可继续）
```

`idle` 用于已建立索引但初始命令尚未被原生接受；前端需新增“等待开始”标签。`completed` 只表示本轮完成，不封死会话。`failed` 对齐前端，不使用辅助文档的 `error` 作为会话状态。断网不把业务状态改为 failed / completed / closed，而是保留最后已知状态并禁止控制。

状态转换还要带当前 turnId、能力修订和 Node epoch。旧轮次的最终消息可以补齐历史，但不能回退当前轮次或重开已结束的请求。相同消息完整终态替换已有全文，随后迟到的 delta 不得继续追加。Node 确认取消前仅为 cancelling；自然完成先发生时保留 completed，不伪造 cancelled。取消不是文件回滚。

### Operation

```text
accepted → delivered → confirmed
    └──────────────→ failed（明确拒绝 / 明确未发送）
accepted / delivered → reconciling → confirmed | failed
```

- accepted：鉴权和幂等事务已提交，尚未证明 Node 收到。
- delivered：Node 已持久化命令 journal 并回执。
- confirmed：Adapter 确认原生接受；排队则为本地队列持久化成功；**不是任务执行完成**。
- failed：有确定证据未执行 / 被拒绝，附稳定错误。
- reconciling：可能执行过，必须查询 Node journal；客户端网络超时显示 `unknown`，不把它持久化成另一个业务成功 / 失败状态。

本地命令 pair / revoke / mark_read 在数据库事务完成后直接 confirmed。远程命令的 confirmed 回执不会因为随后任务失败而反转；任务失败通过 SessionEvent 表达。未知结果不能自动换新 operationId 重发。

### InteractionRequest

approval 与 question 共用生命周期：`pending → deciding → resolved`；未决请求可以 `expired / cancelled`。审批“同意 / 拒绝”的具体 choiceId 放在 decision，不能把批准和状态混用；问题保存 questionId → 值的结构化 answers。

deciding 表示一个客户端已占用决策且正在回传，其他客户端只能查看。明确未发送且尚未到期可回到 pending 并清空预占；发送结果未知必须保持 deciding 并对账。到期后不能接收新决定；若 Node 已在到期前接受，迟到的确认仍可把 deciding 转为 resolved。不能仅因墙钟越过 expiresAt 就覆盖未知的执行结果。

## 5. 登录、令牌与配对

### 用户认证

小程序取得新 `wx.login` code，经 HTTPS 交给 Gateway 的 WeChat provider 适配层；服务端完成身份兑换，以 AppID + 稳定外部身份的带密钥摘要映射用户，返回本系统随机 256-bit opaque access token。数据库只保存令牌哈希，前端只在内存保存；初版不发长期 refresh token，过期重新执行微信登录。建议有效期 2 小时，登出撤销当前 token 并关闭它建立的客户端连接。

AppSecret、身份兑换返回的敏感材料不下发，也不进入日志；session_key 不作为本系统 bearer。微信真实参数、AppID、合法 request / socket 域名、开发 / 生产配置是必须真实联调的门槛，本文不声称已经完成官方平台接入。失败前端返回登录态，重新登录后先刷新归属再恢复页面，不能复用旧权限缓存。

### Node 初次配对

1. Node 本地生成 Ed25519 密钥。匿名限流的 enrollment 接口接受公钥与展示信息，返回 12 位去歧义随机码、独立 256-bit pollToken、5 分钟有效期。短码使用服务端 HMAC 保存；不记录明文。注册本身不授予任何用户访问权。
2. 手机已登录，输入 / 扫描短码调用 preview。返回设备名称、平台、**公钥指纹**与绑定到该用户的短期 ticket；尚未绑定。
3. 用户核对本地设备指纹后确认；锁 enrollment / ticket，检查到期、未占用、公钥未被已绑定或已撤销身份使用。单事务建立 Node、NodeAccess、确认 enrollment、消费 ticket、记录幂等回执与用户变化。竞态只有一个用户成功，其他得到 PAIRING_CONFLICT。
4. Node 以独立 pollToken 查询结果，仅拿 nodeId。随后申请一次性 challenge，签署服务端返回的 UTF-8 signingInput。输入必须包含固定用途 `weagent-node-auth/v1`、nodeId、challengeId、随机 nonce、expiresAt，禁止跨用途签名复用。
5. Gateway 验证公钥签名、未消费、有效期和未撤销后，原子消费 challenge 并签发 10 分钟 Node bearer；该 token 只用于建立 Node WSS。已认证 socket 最长 1 小时，过期前重新取 token 建立新连接，旧连接被 epoch 替换。token 到期不改变已建立 socket 的独立会话期限；撤销则立即终止。

pollToken 只能读自己 enrollment，不能当作 Node / User bearer；challenge 有效期 60 秒。错误不返回其他用户归属细节。一个 Node incarnation 永远只有一个 owner；撤销后必须本地确认并生成新密钥 / 新 nodeId 重配，旧密钥不会因重连复活。

## 6. 资源授权与撤销栅栏

每一个 detail、list、events、checkpoint、diff、operation、request、通知已读和 WS watch 都以已认证 userId 重新关联有效 NodeAccess。别人资源统一 404；已知本人的 readonly / 能力不足用明确业务错误。Node 只能上报自己的项目、Agent、会话、turn 与请求；Node 无权声明 userId、Gateway sequence / revision、资源归属。Schema 通过不代表跨资源授权通过。

所有带 owner 的写事务先锁 user 行，再锁 Node，随后按固定顺序锁 Session、Request、Operation；多个资源按 ID 排序。新 enrollment 在不存在 Node 时按 user → enrollment → ticket 顺序锁定并创建 Node。应用读写的锁顺序要统一，重试明确的数据库死锁 / serialization failure 时沿用同一幂等键。

路由与撤销共享每 Node 的进程内 fencing mutex。撤销持锁完成数据库失效（access、credential_version、token、epoch）、移除连接并关闭，再返回 confirmed；写循环每条命令发送前也必须在该 fence 下核对 epoch / 权限 / deadline。不能只在 HTTP 入口检查一次。Node 已开始执行的行为无法回滚，但撤销后的新命令不会路由给它。

撤销产生 Node remove 变化：前端同步清理其项目 / Agent / Session / 请求 / 通知、相关草稿、事件正文和 operation 控件。回放历史、慢客户端缓存、文件快照也不能绕过撤销。Gateway 在广播时检查 socket 的授权代次，不能把入队前检查当作最终授权。

## 7. 命令事务与幂等

所有业务变更接口要求 `Idempotency-Key`（客户端安全随机 16–128 ASCII 标识）。登录 / 登出 / preview / Node 握手不是 Agent 命令，不使用该业务幂等机制。幂等指纹 = SHA-256(method + 规范化路径 + 规范化 JSON)，包含 expectedTurnId、capabilityRevision 和请求修订；对键排序，不改变数组顺序或正文空白。

远程命令步骤：

1. 限流、鉴权、schema / 大小校验；校验资源属于同 Node、managed、ready、当前可路由、turn / 能力 / request 条件有效。
2. 事务按固定顺序加锁；查 `(user,id)`。同指纹返回原回执，异指纹 409；重复请求即使原条件已变化，也只读取原回执，不重新执行。
3. create 分配 sessionId / turnId，建立 idle / starting 索引；send 分配 nextTurnId；queue 仅预留 future turn ID，不插入 active turn。respond 预占 request 决策。写 operation + 有 deadline 和目标 epoch 的 outbox + 元数据变化 / 审计。
4. 事务提交后返回 202；单写循环发送。默认 dispatch deadline 15 秒，是原生接受截止时间，不是任务运行时限。
5. Node 先持久化 journal，再 delivered；再次检查 deadline、epoch、turn、能力和 request 到期后调用 Adapter。原生确认后 journal / confirmed；明确拒绝则 rejected。Gateway 同事务更新回执和受影响的请求 / 队列投影。

outbox 不是离线执行队列。事务提交后掉线，若明确还没进入 socket write，标记 not_sent / failed；已经开始写或崩溃窗口则 uncertain / reconciling。**重连不发送旧 outbox 命令**，只 `command.query` 查询状态。Node journal 的 not_seen 可证明其去重入口未见过命令；unknown 必须人工确认。原生动作成功但 journal 终态尚未写入时崩溃，不能宣称 exactly-once；没有 Adapter 原生幂等能力时不得盲目重执行。

同一 request 的不同操作键：决策结构完全相同时返回已有 decisionOperation 回执；不同则 409 REQUEST_ALREADY_DECIDED，并允许重新 GET 请求查看权威结果。不把第二个客户端的回答覆盖第一个。回答校验包括已知 questionId、单选 optionId、多选唯一性 / max、required、文本长度、拒绝额外字段；只靠通用 answers Schema 不够。

## 8. 队列、取消与请求竞争

`send`：仅当前轮结束后在同 session 开新轮。`queue`：当前轮 active、非 cancelling 且会话有 queue 能力时，Node 持久化一条有顺序的消息，Gateway 镜像最多 20 条。HTTP queue accepted 不代表已入设备队列，confirmed 才算。

队列由 Node 在当前轮 **completed** 后取队首；取消 / 失败不会自动续跑；重连本身不是触发器。队列保留时允许用户明确发起新轮；新轮完成后再按顺序消费旧队列。队列项进入 starting 时创建 active Turn，执行后标 consumed。V1 不实现队列改序 / 删除，UI 不展示不可用按钮。前端的 `/queue` 只查看队列。

取消与回答抢同一轮：锁 Session 和 Request，先发生的有效转换获胜；取消后新回答 STALE_TURN，尚未发送的 deciding 决策清理为 cancelled，已经交给原生的需要按 Node 最终状态对账。Node 也必须执行相同前置检查，不能依赖 Gateway 提交时的旧检查。

请求详情页一屏布局是前端职责；后端提供紧凑 title / summary 和结构化 questions，不返回 HTML 布局，也不为了塞进一屏丢弃 required 字段。复杂超长请求不隐瞒字段，后续 UI 需独立解决可访问性。

## 9. 持久化事件与投影

本版选择简单、可证明的一致性：**发给客户端的每一个 session event 都已提交数据库**。不实现带 cursor 的临时预览。Node 在 spool 合并 delta（建议 100–200 ms 或 4 KiB），持续输出最终完整 message.completed；每条事件有持久稳定 sourceEventId、会话内 sourceSequence。sourceSequence 跨 Node socket 重连保持连续。

入库事务：按锁顺序校验当前 epoch / Node / Session / Turn → 检查 source 去重与摘要 → 增加 sessions.last_sequence → 写事件 → 更新 timeline_items / Session / Request / DiffFile 等投影 → 分配 user revision / user_changes → commit → ACK sourceSequence → 广播。Node 本地 spool 只有收到 commit ACK 才可删。相同 source ID + 相同摘要返回原 ACK；同 ID 改正文 / 序号为 SOURCE_CONFLICT，绝不重复追加。sourceSequence 有缺口时不越过缺口 ACK，Node 重发缺失段；同一批按会话分组，每组事务提交。

每条 Node source 消息消耗一个 sourceSequence，但可以在一个事务产生多条 Gateway 派生事件。例如 node.session 同时改变 state、capability、queue，派生多条连续 Gateway sequence；source ID / hash / sourceSequence 只挂在这一组的**最后一条事件**，ACK 指向该组最后的 sequence，其余派生事件 source 字段为空。重放检查这个锚点，不把一个 source ID 重复插入多行。

不要用 PostgreSQL `nextval()` 分配会话 cursor：事务回滚会产生空洞。使用锁住的 session 行 counter，在事务回滚时一起回滚。user revision 同理用 users.last_revision；一条资源变化占一个 revision。多客户端广播可以乱序到达，但 reducer 只能消费连续段；缺口通过 HTTP 补偿。

事务同时写独立 `node_event_receipts` 去重账本，记录 source 摘要和该组最后 Gateway sequence，至少保留 90 天；压缩事件前缀不能删除它。账本保留期之外的 sourceSequence 若不大于 last_source_sequence，拒绝并要求人工 / checkpoint 对账，不当作新事件重新追加。

`timeline_items` 是同事务更新的完整投影，key = sessionId + turnId + itemId；final 全文替换，迟到 delta 不改已 complete 的项；unknown display event 保留可见 fallback。Diff 的 path 仅展示，不接收路径执行请求；Node 先上报 `diff.file` 快照，后上报引用 fileId 的 Diff item，Gateway 验证引用属于同 session。大文本先截断并明确 truncated；不能伪装完整。V1 暂不提供大附件下载地址。

## 10. 冷启动、断线、分页与 checkpoint

两类游标互不替代：用户 `revision` 驱动资源索引刷新；Session `sequence` 驱动正文恢复。列表 pageToken 是独立的签名 keyset 游标，绑定 user / filter / sort / 有效期，不得当作 event cursor。

恢复流程：

1. 登录后 `/bootstrap` 在一致性快照内给首屏资源、服务端完整 counts、revision 和 serverTime；每类最多 50 条。数量不能用已缓存首屏长度伪造。
2. 建立唯一客户端 WS，发送 `watch {revision,sessions:[{sessionId,after}]}`。Gateway 先登记订阅并开始缓冲未来事件，再在同一调度屏障捕获 user revision 与 session highWater，发送 ready。之后只推送屏障之后的事件，ready 必須先入单写队列。
3. 客户端保留从 ready 后收到的实时帧，不先覆盖游标。HTTP `/changes?after=本地&until=ready.revision`、`/events?after=本地&until=highWater` 分页补齐至固定上界，再顺序合并已缓冲实时帧。
4. 冷启动没有正文，即使本地存过 cursor 也从 0 加载；只有 body + cursor 原子保存或完整 checkpoint 才可从非零开始。每页核验 sessionId、连续 sequence、nextAfter、hasMore，不以“四次请求结束”作为恢复完成标准。
5. 游标在保留窗口之前返回 410 CURSOR_EXPIRED。session 请求新 checkpoint；它由同一 MVCC 快照复制完整 timeline_items 和覆盖 sequence，10 分钟内不可变。先加载所有 checkpoint items，按 ordinal / firstSequence 顺序拼装，全部成功后一次性安装 body + sequence，再补 checkpoint 之后的事件。过期页不能悄悄换成新 checkpoint。
6. user revision 过期则重做 bootstrap；会话正文可保留但先重新核验权限。客户端处于 recovering 时禁用控制，只有鉴权、metadata 与当前页正文补偿到屏障后才 online。

列表按 `(updatedAt DESC,id DESC)` 等确定顺序分页。加载更多期间 metadata 变化经 user_changes 触发资源 GET / 失效，列表重新排序 / 去重；缺页时重新请求首屏而非假装完整快照。resourceRevision 防止较旧 HTTP 返回覆盖较新 WS 知识。高吞吐时合并同资源失效通知，但**不能丢 user revision 序号**；可以延后 GET，不跳过变化日志。

节点下线仍可查看已授权、已保留历史；控制禁用。HTTP 成功不代表 WS 已恢复；页面销毁只释放自己的订阅，不关闭全局 socket。订阅切换期间 old watch 不先解除到产生空窗；watch 替换是原子屏障。没有权限的任意 session watch 导致该次 watch 整体失败。

## 11. Node 重连与 fencing

Node 鉴权后持有 node fence，并通过数据库递增 connection_epoch。新连接成为唯一注册项，然后关闭旧连接。旧连接的 onClose 只在 `(nodeId,epoch,connectionId)` 仍匹配时移除注册项，不能把新连接标记离线。旧 epoch 的事件、command ACK、heartbeat 都拒绝。

Node inventory 分片带 inventoryId；同一次最多 100 项 / 帧，complete=true 才把未出现项目标 invalid、未出现 Agent 标 unavailable；不可每来一片就删除其余目录。Gateway 根据 socket 归属补 nodeId，自行分配资源 revision；session inventory 只认可 Gateway 已分配或未来显式 adoption 流程建立的 sessionId。没有“上报一个陌生 sessionId 就接管”的入口。

重连先完成目录与会话状态对账，再允许新命令。事件 spool 先补历史，Node journal 查询不重执行；confirmed 的 old turn 回执可以补回，但不能修改当前轮的运行状态。Node 重装遗失 journal / spool 必须显式报告 unknown 并将控制保持只读待确认；不根据“当前没进程”假定旧命令从未执行。

## 12. WebSocket、限流与背压

使用 coder/websocket 的 Accept / Dial、Read(ctx)、Write(ctx)、Ping(ctx)、Close / CloseNow、SetReadLimit。库允许多种并发调用，但 Reader / Read 必须串行；应用仍强制**一个读循环、一个有界业务写循环**。必须持续读取才能处理 ping / pong / close；超时会影响连接生命周期，不能当成“不影响连接的单条消息取消”。升级后使用服务拥有的连接 context，不绑定已返回 handler 的 request.Context。详见 [官方 API](https://pkg.go.dev/github.com/coder/websocket)。

当前默认值（已集中配置，仍需生产容量压测）：

| 参数 | 默认 |
| --- | --- |
| HTTP JSON body | 64 KiB；超过 413 |
| WS 完整消息 | 256 KiB；关闭 1009；字符限额外还检查 UTF-8 编码字节 |
| 写队列 | 每连接最多 256 帧且 2 MiB，总量任一越界就断慢客户端 |
| 心跳 / 判离线 | 15 秒 / 45 秒；单次 write 5 秒 |
| 用户并发 client socket | 3；每个最多 20 个会话订阅 |
| Node 并发 socket | 1；已限制帧大小、目录累计条数和写队列；原生 Adapter 的事件速率 / 磁盘配额另验 |
| HTTP 命令频率 | user 30/min；全部 HTTP IP 120/min / user 180/min；配对预览 5/min/user + IP |
| 公共 enrollment / challenge | 公共入口合计 IP 30/min，enrollment / preview 另共用 IP 5/min；有 in-flight 上限及记录 TTL |
| prompt / 队列 | 4000 Unicode 字符；20 条 |

握手必须先验证 Authorization；不把 token 放 query、Referer、日志或错误文本。非空 Origin 只接受显式 allowlist；微信原生客户端可无 Origin，但必须正常 token 校验；不使用 wildcard 绕过鉴权。压缩默认关闭。严格区分客户端和 Node 权限模型，不能共用一条“任意消息”路由。

客户端写队列满：当前直接 CloseNow，不保证发送 1013；客户端必须按异常断连执行历史恢复，不丢一个必要序号后继续假装成功。Node 写队列满同样断开：确定未写的命令失败，可能已经写出的命令进入对账；数据库不可用不 ACK Node spool。进程内限制全局连接数、并发请求和 checkpoint 字节预算，但这些不是已验证的生产容量。禁止无界 goroutine / channel。

## 13. 数据保留与隐私

普通日志 / audit 仅记录 requestId、operationId、资源 ID、事件类型、耗时、字节数、状态码；不记录 prompt、答案、工具输出、Diff、token、配对码或签名。业务历史表保存正文是产品功能，与“日志不存正文”不是一回事；用户或工具输出可能意外含秘密，Node 上传前脱敏，UI 明示历史留存，存储与备份加密、最小权限访问。

已实现的默认策略：已结束会话正文 / Diff 留 30 天，以当前 Turn.ended_at 为准，不能被 metadata 更新时间延长；活跃会话、未解决操作和未消费队列不被此任务清除。正文已清理且完全解决的会话聚合在结束满 90 天后移除；audit 留 90 天、user_changes 留 7 天，token / enrollment / challenge 到期 24 小时后由分批任务清理。业务 payload 在 operation 终态后 24 小时清除，保留 hash / 最小结果供幂等查询；未消费队列、已确认 send 尚未交接的新 turn 预留保留 payload。reconciling 的去重记录不得过早删除。Node journal 至少覆盖 Gateway 的幂等保留期，磁盘配额耗尽必须停止接收而非静默丢弃。

未来独立事件前缀压缩只能在完整投影与可用 checkpoint 已持久化后实施；当前已实现的是终态会话正文整体保留期清理。清理后 Session.historyState=purged，GET history/checkpoint 返回 HISTORY_PURGED，禁止继续输入与新 source 正文上传，不拿空白 checkpoint 冒充完整恢复。旧 receipt 保留期内可去重 ACK，重连 readiness 不再等待 purged 会话同步。通知 / 摘要只留最低必要信息。用户配置同步、独立删除 UI、零正文保存模式暂不开放；备份恢复期限与清理延迟仍须运维演练，不承诺从所有备份瞬间删除。

## 14. 数据库与部署

初版一个 Go Gateway + PostgreSQL 18 + TLS 反向代理。健康检查 `/healthz` 只确认进程；`/readyz` 需要数据库可用且未 draining，不泄露连接串。连接池设置有限 maxConns，DB 查询 / 锁等待有 timeout；生产不自动在服务启动时跑迁移。

迁移工具固定 goose v3.28.0，部署阶段一次运行 Up；生产应让应用角色只具 DML、迁移角色单独管理 DDL（简化 Compose 尚未拆分角色）。配置包括 listen、database URL、WeChat AppID / secret、HMAC key、Origin、heartbeat、limits、retention；secrets 只来自环境 / 密钥服务，不进入提交。Go 模块、`.env.example`、服务入口、Dockerfile 和 Compose 已落地；本机原生二进制已运行，容器仅做配置校验，见运行指南。

当前实现用进程全局 sequencer 覆盖提交、订阅屏障和有界 WS 写入，比原设计的 per-node fence 更保守；写超时 5 秒会影响其他连接。数据库独占 advisory lock 防止误起多实例，失去锁连接后 fail closed。热点拆锁和容量压测属于后续优化，不承诺多 Gateway 支持。

当前停机：ready=false → 停止新命令和握手 → 取消后台任务、直接关闭 sockets 并等待有界 worker → HTTP Shutdown 最多 20 秒 → 关闭 DB。不保证关闭提示或已提交但尚未发送的 ACK 一定到达；Node 保留未收 ACK 的 spool，重连依靠 receipt 去重。不确定 outbox 只对账、不重发。单实例重启导致暂时 offline 是已接受取舍；不要用数据库 lastSeen 假装无缝在线。完整进程级停机时延 / 压力矩阵仍需部署环境验证。

行锁 / 隔离级别应按 [PostgreSQL 官方锁文档](https://www.postgresql.org/docs/18/explicit-locking.html) 实现；一致性 bootstrap / checkpoint 使用 repeatable read，命令业务锁在 read committed 显式 FOR UPDATE 下完成。锁超时和死锁重试必须有次数上限并保持幂等键。

## 15. 错误与兼容策略

统一 `{error:{code,message,retryable,requestId}}`。400 schema / 字段错误；401 用户认证；404 不存在或非本人；409 幂等冲突、旧 turn / capability、请求已处理；410 请求 / 游标 / 历史到期；413 过大；429 限流并返回 Retry-After；503 Node 离线 / 服务暂不可用。非本人资源不返回敏感 details。HTTP 202 并非静默成功，前端持续跟踪 Operation。

协议版本不匹配：控制消息拒绝，UI 进入不可操作升级提示。已知版本的未知 display event：前端保留事件 id / sequence，展示简洁 fallback，并继续推进 cursor；未知 control command 永不执行。严格 Schema 用于当前服务入站与已知消息，客户端未知展示兼容分支在严格已知消息验证之外实现，不把所有 payload 变成 any。原生外部 payload 不透传到公共 API。

## 16. 实施阶段与验收门槛

| 阶段 | 必须交付 | 才能声称通过 |
| --- | --- | --- |
| D0（设计阶段已交付） | 设计、Schema、OpenAPI、SQL、前端映射、正反例 | 合约 / fixture 检查通过；不能单独证明服务完成 |
| D1 | Go 配置、HTTP、pgx、鉴权 provider、迁移、资源授权 | 原生 PG fixture 上 Up/Down、两用户越权、真实 HTTP handler 测试 |
| D2 | 两类 WS、配对 / 撤销、epoch、Fake Node 进程 | 重连替换、旧 ACK、慢写、失联、撤销竞态、心跳实测 |
| D3 | create/send/queue/cancel/respond、journal / outbox 对账、事件恢复 | 幂等冲突、重复提问回答、过期、取消竞态、崩溃窗口、分页 / checkpoint 测试 |
| D4 | LiveGateway + Runtime 改造，保留独立 demo 模式 | 小程序真机合法域名、登录恢复、输入 / 待处理一屏、无重复会话 / 消息 |
| D5 | 至少一个真实 Node Adapter | 真原生 acceptance / cancel / questions / capability 验证；不靠品牌或 fake 回执 |

Fake Node 必须独立进程连真实 HTTP / WS，不是 Gateway 同进程内改内存。故障矩阵至少覆盖：commit 前后断连、ACK 丢失、重复 source ID、source 改内容、旧 epoch、two-user 越权、同 request 双客户端、deadline 前后、cancel vs complete、queue 重连不自动执行、cold cursor 无正文、checkpoint 过期、流量满队列、DB 不可用、优雅停机和原生接受后 journal 未写的 unknown。

当前 D1–D3 的 Gateway 实现已落地，并通过原生 PostgreSQL、HTTP / WS、独立 Fake Node 故障与 race 回归。具体通过项以验证记录为准，不表示上表所有压力 / 故障组合均已覆盖。D4 前端生产接入和 D5 真实 Adapter 尚未实现；不能用文档或 Schema 检查替代。
