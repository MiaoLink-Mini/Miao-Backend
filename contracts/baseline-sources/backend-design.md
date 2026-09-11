# Remote Agent Control — 后端设计方案

> 文档定位：公网服务端 / Gateway 的工程实施规范。
>
> 本文中的服务名、模块名、事件名、接口路径、字段名均为本项目内部建议规范，不对应 Claude Code、Codex、Pi、DeepSeek Harness、OpenCode 等外部项目的现有字段。真正对接外部 Agent 的协议转换全部位于用户设备上的 Node Adapter 层。

---

## 1. 项目目标

构建一个面向微信小程序的远程 Coding Agent 控制后端，使用户可以通过手机查看和控制自己开发机 / VPS 上运行的 Coding Agent。

第一阶段目标：

- 用户登录与身份管理
- Node 设备配对与撤销
- Node 长连接管理
- Node 在线 / 离线状态
- Project 元数据管理
- Agent Session 索引管理
- Command 路由
- Event 实时转发
- Event 持久化与断线补偿
- Approval 持久化与审批
- Cancel
- Notification Inbox
- Diff 事件
- Usage 元数据
- Audit Log

明确不属于公网后端的职责：

- 不运行 Codex / Claude Code / Pi / DeepSeek Harness / OpenCode
- 不直接访问用户工作目录
- 不保存 Agent API Key、登录凭据、SSH 私钥
- 不解析 Agent 原生协议
- 不执行 Shell
- 不承担远程开发环境

系统边界：

```text
Mini Program
    |
    | HTTPS / WSS
    v
Public Gateway
    |
    | persistent WSS
    v
Node Daemon
    |
    +-- Codex Adapter
    +-- Claude Adapter
    +-- Pi Adapter
    +-- DeepSeek Harness Adapter
    +-- OpenCode Adapter
    +-- Generic PTY Adapter
```

核心原则：

```text
Agent Runtime = Node
Control Plane = Gateway
Agent Adapter = Node
Remote UI = Mini Program
```

---

## 2. 技术栈

### 2.1 服务端

推荐：

- Go
- PostgreSQL
- HTTPS
- WebSocket over TLS
- Caddy 或 Nginx 作为公网 TLS 入口

第一版不要求：

- Redis
- Kafka
- RabbitMQ
- Kubernetes
- 对象存储
- 多 Gateway 实例

第一版部署模型：

```text
Internet
   |
   v
Caddy / Nginx
   |
   v
Go Gateway
   |
   v
PostgreSQL
```

### 2.2 Go 侧建议

建议：

- HTTP：`net/http`
- Router：轻量 Router，可使用 `chi`
- PostgreSQL Driver：`pgx`
- SQL 类型生成：`sqlc`
- Migration：必须使用独立 migration 工具；最终库由实现 Agent 在建项时从成熟 Go/PostgreSQL 迁移方案中选择，并把选择结果固定在 README 与 Makefile 中
- Logging：结构化日志

WebSocket 库必须在实现前根据其当前文档确认以下能力：

- context cancellation
- ping / pong
- close handshake
- 单连接写串行化
- message size limit
- backpressure 处理

未经确认不得自行假设具体方法名或 API。

---

## 3. 代码结构

建议目录：

```text
cmd/
  gateway/
    main.go

internal/
  auth/
  user/
  node/
  pairing/
  project/
  session/
  event/
  approval/
  notification/
  audit/
  presence/
  routing/
  storage/
  config/

  transport/
    http/
    websocket/

pkg/
  protocol/

migrations/

sql/
  queries/

configs/
```

职责边界：

```text
Transport
   ↓
Application Service
   ↓
Repository / Storage
   ↓
PostgreSQL
```

禁止把 SQL、业务规则、WebSocket 写入同一个 Handler。

---

## 4. 核心实体

第一版核心实体：

```text
User
Node
NodeAccess
PairingRequest
Project
Session
SessionEvent
Approval
Notification
AuditLog
```

### 4.1 User

职责：

- 对应一个可登录用户
- 拥有 Node
- 创建 Session
- 接收 Notification
- 执行 Approval / Cancel 等操作

第一版可按单用户拥有 Node 设计，但数据结构不要阻塞未来共享能力。

### 4.2 Node

Node 代表用户开发机 / VPS 上安装的 Node Daemon。

服务端保存：

- Node ID
- Owner User ID
- Display Name
- Platform Metadata
- Node Version
- Device Public Identity / Verifier
- Created At
- Last Seen At
- Revoked At

服务端不得保存：

- Agent API Key
- 用户系统密码
- SSH 私钥
- 工作目录真实内容
- Agent 登录 Token

Node 在线状态不得单纯依赖数据库布尔字段。

在线状态由：

```text
active websocket connection
+
last heartbeat
```

共同计算。

### 4.3 Project

Cloud Project 是逻辑项目，不是服务器上的真实工作目录。

服务端默认保存：

- Project ID
- Node ID
- Display Name
- Optional Display Metadata
- Created At
- Updated At

真实本地路径只保存在 Node。

客户端永远通过 `Project ID` 操作，不直接向 Gateway 发送本地绝对路径。

### 4.4 Session

Session 表示一个远程 Agent 会话索引。

服务端保存：

- Session ID
- User ID
- Node ID
- Project ID
- Agent Type
- Display Title
- Session State
- Node Connectivity State
- Capability Snapshot
- Created At
- Updated At
- Last Event Sequence

Agent 原生 Session 标识的持有与恢复逻辑以 Node 为主。

### 4.5 SessionEvent

SessionEvent 是客户端断线恢复和历史回放的核心。

每个 Session 必须拥有严格单调递增的 sequence：

```text
Session A
  event 1
  event 2
  event 3
  ...
```

业务事件持久化；高频 delta 允许合并持久化。

### 4.6 Approval

Approval 是独立、可持久化、有状态的资源。

第一版状态：

```text
pending
approved
denied
expired
cancelled
```

Approval 不允许仅作为临时 WebSocket 消息存在。

### 4.7 Notification

第一版 Notification 类型建议覆盖：

- Agent completed
- Agent failed
- Approval requested
- Agent waiting for user input
- Node offline

### 4.8 AuditLog

至少记录：

- User
- Node
- Session
- Action
- Result
- Timestamp

默认禁止把 Prompt、完整 Agent 回复、完整 Shell 输出写入普通 Audit Log。

---

## 5. Node 配对与注册

### 5.1 目标

用户通过微信小程序把自己的 Node Daemon 注册到账号。

此过程不使用 MCP，也不依赖 Agent 插件。

### 5.2 推荐流程

```text
Node first start
   ↓
Generate device identity
   ↓
Request short-lived pairing credential from Gateway
   ↓
Display QR / pairing code
   ↓
User scans / enters code in Mini Program
   ↓
Gateway verifies authenticated user
   ↓
User confirms binding
   ↓
Gateway binds User ↔ Node
   ↓
Node obtains active device authorization
```

要求：

- Pairing Code 短时有效
- Pairing Code 一次性使用
- 绑定前必须显示 Node 基础信息供用户确认
- 绑定后旧 Pairing Code 立即失效
- Node 撤销后现有 WSS 必须立即断开
- 被撤销 Node 的旧身份不得重新连接

---

## 6. WebSocket 连接设计

## 6.1 Node Connection

原则：

```text
1 Node = 1 persistent control WSS
```

Node 上的多个 Session 复用这一条连接。

Node Connection 负责：

- Authentication
- Heartbeat
- Node Presence
- Command Receiving
- Event Upload
- Reconnect
- Session Reconciliation

禁止每个 Agent Session 建立独立 Node WSS。

### 6.2 Client Connection

原则：

```text
1 Mini Program runtime = 1 user-level WSS
```

Client WSS 用于：

- Node status push
- Session update
- Session event
- Approval notification
- Notification inbox update

Session 页面可以通过订阅逻辑减少无关事件，但不要求新开物理 WebSocket。

### 6.3 NodeConnection 与 ClientConnection 必须分离

两者权限模型完全不同。

Node 可以：

- 上报 Event
- 上报 Session State
- 响应 Command
- 上报 Capability

Client 可以：

- 读取有权限的资源
- 发起业务 Command
- Approval
- Cancel

禁止复用一个“GenericSocket”权限模型。

---

## 7. Presence 与 Heartbeat

Node 在线判断：

```text
socket active
AND
heartbeat not expired
```

要求：

- Node 定时发送 heartbeat
- Gateway 记录 last heartbeat
- 超时后将 Node 视为 offline
- socket close 时立即移除内存连接
- Node reconnect 后替换旧连接
- 同一 Node 同时只能有一个有效控制连接

数据库 `last_seen_at` 只用于历史与展示，不作为实时连接对象。

---

## 8. Session 状态模型

统一 Session State 建议：

```text
idle
running
waiting_approval
waiting_input
completed
cancelled
error
```

Node Connectivity 独立表示：

```text
online
offline
```

例如：

```text
Session State = running
Node Connectivity = offline
```

不要因为 Node 离线就覆盖 Session 最后已知业务状态。

Agent Runtime 的真实运行状态以 Node 为准；用户权限、云端资源归属以 Gateway 为准。

---

## 9. Command / Event 模型

必须严格区分：

```text
Command: Client → Gateway → Node
Event:   Node → Gateway → Client
```

### 9.1 Command

第一版至少包含业务能力：

- Create Session
- Send Prompt
- Cancel Session / Turn
- Resolve Approval
- Request Session Reconcile

Node 离线时：

- 实时命令默认立即失败
- 不自动排队
- 不允许旧 Prompt 在 Node 重新上线后自动执行

### 9.2 Event

第一版统一事件分类至少覆盖：

```text
session.created
session.updated
turn.started
turn.completed
message.delta
message.completed
tool.started
tool.updated
tool.completed
approval.requested
approval.resolved
plan.updated
diff.updated
usage.updated
error
heartbeat
```

这些名称为本项目内部协议建议。

服务端不得把 Codex / Claude / Pi 原始事件直接传给前端。

正确链路：

```text
Native Agent Event
   ↓
Node Adapter
   ↓
Unified Event
   ↓
Gateway
   ↓
Mini Program
```

---

## 10. Event 持久化

### 10.1 Sequence

每个 Session 必须维护 sequence。

客户端记录最后确认的 sequence。

客户端重连后：

```text
Client says: last sequence = N
Gateway returns: N+1 ... latest
```

用于解决：

- 微信切后台
- 网络切换
- WebSocket 回收
- 临时断线

### 10.2 高频 Delta

不要把每个 token delta 都同步 INSERT PostgreSQL。

建议：

```text
Realtime:
Node → Gateway → Client

Persistence:
merge deltas
or persist final completed message
```

必须立即持久化：

- approval
- tool state transition
- session state transition
- diff metadata
- error
- final message
- usage summary

### 10.3 历史策略

第一版实现时必须通过配置明确历史保留策略，不允许代码中散落硬编码。

需要支持后续增加：

- retention days
- user delete
- no-content mode

---

## 11. Reconnect 与 Reconciliation

Node 重连流程：

```text
Disconnected
   ↓
Reconnect with backoff
   ↓
Authenticate
   ↓
Register active connection
   ↓
Report active/local sessions
   ↓
Gateway reconcile metadata
```

Node 应上报当前仍存在的 Session 状态。

对账原则：

- Runtime State：Node 优先
- Ownership / Authorization：Gateway 优先
- 不允许 Node 通过上报创建自己无权限关联的 User 资源

---

## 12. Approval Service

Approval 流程：

```text
Agent
 ↓
Node Adapter
 ↓
Node
 ↓
Gateway persist pending approval
 ↓
Push Mini Program
 ↓
User resolves
 ↓
Gateway validates authorization + state
 ↓
Gateway routes resolution
 ↓
Node Adapter
 ↓
Agent
```

第一版 UI 语义：

- Allow Once
- Allow Session（仅当 Node capability 明确支持）
- Deny

服务端不得自行把不支持的审批模式映射成其他模式。

---

## 13. 幂等

所有会改变状态的客户端请求必须支持幂等。

重点：

- Create Session
- Send Prompt
- Resolve Approval
- Cancel

要求：

- Client 为操作生成 request identity
- Gateway 能识别重复请求
- 重复请求返回首次处理结果
- 不得重复下发高风险动作

---

## 14. Backpressure

不得从 Node read loop 直接同步写 Mini Program socket。

推荐：

```text
Node Event
  ↓
Event Service
  ↓
Persistence / Broadcast
  ↓
Client Send Queue
  ↓
Client Write Loop
```

每个 ClientConnection 必须有有界发送队列。

队列持续满时：

- 允许主动断开慢客户端
- 客户端重新连接后依赖 sequence 补事件

禁止无限堆积内存。

---

## 15. Diff 设计

Diff 不应该只视为普通长文本。

事件可以包含：

- Diff metadata
- File summary
- Size
- Inline flag
- Payload reference

小 Diff 可以 inline。

大 Diff 应支持独立 payload 模型。

第一版即使仍保存在 PostgreSQL，也应保留未来迁移对象存储的协议空间。

---

## 16. Terminal 边界

Terminal 不是 Agent Session 的组成部分。

未来如果实现：

```text
TerminalSession
```

应作为独立资源。

Terminal 大量实时 ANSI 输出不进入 SessionEvent 主历史。

只持久化必要生命周期信息：

- opened
- closed
- exit code

第一版后端不要求实现 Terminal。

---

## 17. API 设计原则

建议资源导向。

示例路径仅作为内部设计方向：

```text
/auth
/nodes
/nodes/{node}
/projects
/projects/{project}
/sessions
/sessions/{session}
/sessions/{session}/events
/approvals
/approvals/{approval}
/notifications
```

禁止设计：

```text
/codex/*
/claude/*
/pi/*
```

Gateway 不应知道 Agent 原生 API。

### 17.1 HTTPS 负责

- login
- node list
- node pairing
- project list
- session list
- session metadata
- event history fetch
- approval inbox
- notifications
- settings

### 17.2 WSS 负责

- realtime session event
- node presence
- approval push
- session state push
- command relay
- node control channel

---

## 18. Authorization

任何资源请求必须做资源级授权。

不能只检查：

```text
user authenticated
```

必须检查：

```text
User
  ↓
Resource ownership/access
  ↓
Node ownership/access
```

例如访问 Session：

```text
Session → Node → User access
```

Node 自身也不得跨 User 上报资源。

---

## 19. Secret 与隐私

Gateway 永远禁止保存：

- OPENAI_API_KEY
- ANTHROPIC_API_KEY
- DeepSeek API Key
- Codex credential
- Claude credential
- SSH private key
- Node 本机系统凭据

普通运行日志默认禁止记录：

- Prompt 全文
- Agent 回复全文
- 环境变量
- Shell 完整输出
- Secret

日志应以 metadata、ID、状态、错误类别为主。

---

## 20. Audit

第一版必须实现 Audit Log。

记录：

```text
who
when
node
session
action
result
```

高价值动作：

- Node pairing
- Node revoke
- Session create
- Prompt send
- Approval approve / deny
- Cancel
- Sensitive setting change

---

## 21. Notification Inbox

第一版包含 Notification Inbox。

推荐事件：

```text
approval requested
session completed
session failed
waiting user input
node offline
```

Notification 需要：

- unread / read
- target resource
- created at
- optional action metadata

---

## 22. Database 规划

逻辑表：

```text
users
nodes
node_access
pairing_requests
projects
sessions
session_events
approvals
notifications
audit_logs
idempotency_records
```

未来：

```text
teams
team_members
api_tokens
automation_rules
```

第一版不实现未来表。

要求：

- migration 必须纳入版本控制
- 所有 schema 变更必须 migration
- 不允许生产环境启动时自动 destructive schema sync
- SQL query 独立维护

---

## 23. 协议 Schema

Gateway 使用 Go，Node / 小程序预计使用 TypeScript。

禁止长期人工维护两套互相独立的协议类型。

必须指定一个协议真相源。

第一版推荐：

```text
JSON + JSON Schema
```

要求：

- 协议有明确 version
- Go / TypeScript 类型尽量从 schema 生成
- 未知事件必须可安全拒绝或忽略
- 不能靠大小写、键名近似匹配
- Adapter 与 Gateway 必须严格使用协议定义字段

第一版不需要 Protobuf。

---

## 24. 错误模型

错误必须区分：

- authentication error
- authorization error
- validation error
- node offline
- session unavailable
- approval expired
- duplicate request
- protocol mismatch
- internal error

客户端可展示错误必须有稳定机器可读 error code 和人类可读 message。

不得让前端通过字符串内容判断错误类型。

---

## 25. Observability

至少提供指标：

```text
active_nodes
active_clients
active_sessions
websocket_connections
event_ingest_rate
command_rate
event_delivery_latency
database_latency
error_rate
```

结构化日志至少包含可用时的：

```text
request_id
user_id
node_id
session_id
```

不得为了追踪方便泄漏敏感正文。

---

## 26. Graceful Shutdown

Gateway 停止时：

```text
stop accepting new traffic
 ↓
mark server draining
 ↓
close / notify websocket connections
 ↓
wait in-flight requests
 ↓
flush necessary persistence
 ↓
close database pool
 ↓
exit
```

必须有 shutdown timeout。

---

## 27. 测试要求

### 27.1 Unit Test

至少覆盖：

- authorization
- session state transition
- approval state transition
- idempotency
- event sequence
- node revoke
- reconnect reconciliation

### 27.2 Integration Test

至少覆盖：

```text
Fake Node ↔ Gateway ↔ Fake Client
```

完整链路：

1. Node pairing
2. Node connect
3. Create Session
4. Send Prompt Command
5. Node emit Event
6. Client receive Event
7. Client disconnect
8. Node emit more Event
9. Client reconnect
10. Fetch missing sequence
11. Approval request
12. Approval resolution
13. Node disconnect
14. Reconnect + reconcile

### 27.3 必须使用 Fake Adapter / Fake Node

后端测试不能依赖真实 Codex / Claude / Pi 才能运行。

---

## 28. MVP 验收标准

满足以下条件视为后端 MVP 完成：

- 用户可以完成认证
- Node 可以一次性配对
- Node 可以长期 WSS 在线
- Node 可撤销并立即失效
- 客户端可以看到 Node online/offline
- Node 可以注册 Project metadata
- 用户可以创建统一 Session
- Prompt Command 可以路由到正确 Node
- Node Event 可以实时推到正确用户
- Event 有 per-session sequence
- 客户端断线后可补齐缺失 Event
- Approval 可持久化、查询、处理
- Cancel 可路由
- Notification Inbox 可用
- Diff Event 可用
- Usage metadata 可用
- Audit Log 可用
- Node 离线时危险实时命令不会自动排队重放
- 服务端不保存 Agent Secret
- 测试可在无真实 Agent 的环境运行

---

## 29. 第一版明确不做

以下内容不得由 Agent 擅自加入 MVP：

- Redis
- 多 Gateway 实例
- Kubernetes
- P2P
- Remote Desktop
- 通用 TCP Tunnel
- Agent Runtime in Cloud
- MCP 作为 Node 主控制协议
- Team / RBAC
- Plugin Marketplace
- Terminal
- 大文件对象存储
- 自动任务 Scheduler
- Webhook
- Billing

如果实现过程发现确有依赖，必须先把原因写入设计变更记录，不得直接扩大范围。

---

## 30. 后续扩展方向

### Phase 2

- Session search
- richer diff
- file view
- usage statistics
- Node version management
- capability browser

### Phase 3

```text
Load Balancer
    |
Gateway A / B / C
    |
Redis / shared routing
    |
PostgreSQL
```

Redis 未来职责：

- presence routing
- gateway ownership
- pub/sub or stream relay

PostgreSQL 继续作为持久真相源。

---

## 31. Agent 开发约束

交给 Coding Agent 实现时必须遵守：

1. 不得根据 Agent 名称自行设计外部 Agent 协议字段。
2. Gateway 只处理 Unified Protocol。
3. 任何未在本文定义的业务决策不得自行扩展。
4. 对第三方库具体函数、字段、配置不确定时必须先读取对应官方文档或本仓库实际代码。
5. 不得通过大小写或相似名称猜测配置键。
6. 所有新增数据库字段必须有 migration。
7. 所有状态改变接口必须考虑授权和幂等。
8. 所有 WebSocket 写入必须串行化。
9. 所有发送队列必须有界。
10. 所有敏感信息必须默认从日志排除。
11. 所有协议变更必须同步 schema 与测试。
12. 任何 Agent-specific 逻辑出现在 Gateway 时，应视为架构违规并重构到 Node Adapter。

---

## 32. 最终架构摘要

```text
                 ┌─────────────────┐
                 │  Mini Program   │
                 └────────┬────────┘
                          │ HTTPS/WSS
                          ▼
┌────────────────────────────────────────────┐
│                 Go Gateway                 │
│                                            │
│ Auth          Node Pairing                 │
│ Node WSS      Client WSS                   │
│ Presence      Session Router               │
│ Event Store   Approval Service             │
│ Notification  Audit                        │
└──────────────────────┬─────────────────────┘
                       │
                 PostgreSQL
                       │
                       │ persistent WSS
                       ▼
┌────────────────────────────────────────────┐
│                 Node Daemon                │
│                                            │
│ Adapter Registry                           │
│ Codex / Claude / Pi / DSH / OpenCode ...  │
└────────────────────────────────────────────┘
```

后端的核心不是“运行 Agent”，而是稳定地完成：

```text
Identity
+ Routing
+ Durable Events
+ Approval
+ Reconnect
+ Authorization
```

只要这些边界保持稳定，后续增加新的 Agent Adapter 时无需反复修改公网 Gateway。
