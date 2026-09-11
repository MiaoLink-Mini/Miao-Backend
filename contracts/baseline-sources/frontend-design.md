# Remote Agent Control — 微信小程序前端设计方案

> 2026-09-05 更新：增加全面适配的移动端交互和验收规范。本文是设计文档，尚无已联调的小程序交付。第 42—48 节扩展完整产品要求；原 MVP 页面继续作为首阶段，按钮可用性以实际验证后的能力为准。

> 2026-09-05 功能遍历补充：页面设计以 [frontend-function-inventory.md](frontend-function-inventory.md) 的 18 个功能域、194 项要求为功能基线。该清单包含入口、验收、状态、原生目录归属及旧文档逐章节反查。原 MVP 表示实现阶段；绘制完整前端时仍需给有条件的原生能力安排入口。新增条目不表示已实现，不替代实际版本与协议核验。

> 文档定位：微信小程序客户端的产品与工程实施规范。
>
> 本文中的页面名、组件名、状态名、事件名均为本项目内部建议规范。前端只消费 Gateway 的 Unified Protocol，不直接理解 Codex、Claude Code、Pi、DeepSeek Harness、OpenCode 等外部 Agent 的原生协议。

---

## 1. 产品目标

微信小程序不是 SSH 客户端，也不是完整桌面 IDE。

产品目标：

> 在手机上以最低操作成本查看、回复、审批、取消和管理远程 Coding Agent Session。

核心使用场景：

- 离开电脑后查看 Agent 是否仍在运行
- 快速回复 Agent 提问
- 处理 Approval
- 查看 Tool Call
- 查看 Diff
- 查看 Plan
- 查看 Agent 最终结果
- Cancel 失控任务
- 查看多个设备与多个 Session
- 查看 Notification Inbox

前端不承担：

- 本地运行 Agent
- 直接 SSH 登录用户主机
- 解析 ANSI / TUI 作为主交互
- 保存 Agent Secret
- 自己决定不同 Agent 的协议差异

---

## 2. 信息架构

最高层级按照用户任务组织，而不是按照 Agent 品牌组织。

推荐一级导航：

```text
Home
Sessions
Inbox
Settings
```

### Home

展示：

- Node 列表
- Node online / offline
- 每个 Node 的活跃 Session 数量
- 最近 Project
- 快速开始 Session

### Sessions

统一展示全部 Agent Session：

```text
Claude  · project-a · waiting approval
Codex   · project-b · running
Pi      · project-c · completed
```

Agent 只是 Session metadata，不是一级导航。

### Inbox

统一聚合：

- Pending Approval
- Waiting Input
- Completed
- Failed
- Node Offline

### Settings

- Node 管理
- Node Pairing
- 已撤销设备
- Notification 配置
- 隐私设置
- Session 历史设置
- 关于

---

## 3. 页面规划

第一版页面：

```text
Login
Home
NodeDetail
PairNode
ProjectList
CreateSession
SessionList
SessionDetail
ApprovalInbox
ApprovalDetail
NotificationInbox
Settings
NodeSettings
```

可后续增加：

```text
DiffDetail
PlanDetail
UsageDetail
FileViewer
Terminal
```

第一版 DiffDetail 可以内嵌 Session 页面，也可作为独立详情页。

---

## 4. 首页 Home

目标：用户 3 秒内知道“哪些设备在线、哪些 Agent 需要我处理”。

建议结构：

```text
Remote Agent

Needs Attention
  2 approvals
  1 waiting input

My Nodes

● Desktop-PC
  Windows
  2 running

● Tokyo VPS
  Linux
  idle

○ Laptop
  macOS
  offline

Recent Sessions
...
```

首页优先级：

1. Needs Attention
2. Online Node
3. Running Session
4. Recent completed Session

不要把统计图放在 MVP 首页核心位置。

---

## 5. Node Detail

Node 页面展示：

- Display Name
- Online / Offline
- Platform
- Node Version
- Last Seen
- Projects
- Running Sessions
- Available Agents

Available Agents 仅根据 Gateway / Node capability metadata 渲染。

例如：

```text
Available Agents

Codex       Ready
Claude Code Ready
Pi          Ready
OpenCode    Ready
```

前端不得自行探测 Agent 是否安装。

---

## 6. Node Pairing

入口：

```text
Settings → Nodes → Add Node
```

支持：

- 扫二维码
- 输入一次性配对码

流程：

```text
Scan / Enter Code
  ↓
Fetch pending node information
  ↓
Show confirmation
  ↓
User confirms
  ↓
Pair success
```

确认页必须显示：

- Node 名称
- Platform
- Node Version
- Pairing Expiration

不能仅凭二维码扫描立即绑定。

---

## 7. Project List

用户看到逻辑 Project：

```text
Desktop-PC

Projects
  grok2api
  reference
  my-app
```

前端不需要知道真实绝对路径。

禁止让用户在小程序 MVP 中直接输入任意服务器文件路径创建 Project。

Project 数据来源只能是 Gateway 已授权资源。

---

## 8. Create Session

创建 Session 流程：

```text
Select Node
  ↓
Select Project
  ↓
Select Agent
  ↓
Optional Agent Profile / Model
  ↓
Enter Prompt
  ↓
Create
```

如果用户从 Project 页面进入：

```text
Node / Project
```

应自动预选，减少步骤。

Agent 选择列表来源于 Node 上报 capability。

不支持的 Agent 不显示或明确标为 unavailable。

---

## 9. Session List

统一 Session 列表，而不是按 Agent 拆页。

每个卡片显示：

- Title
- Agent
- Project
- Node
- Session State
- Node Connectivity
- Updated Time
- Attention Badge

示例：

```text
Fix Docker startup
Codex · grok2api · Desktop-PC
running

Refactor header
Claude · BewlyCat · Desktop-PC
waiting approval
```

状态颜色与图标可以有差异，但不能只靠颜色表达。

---

## 10. Session Detail — 核心页面

这是产品最重要的页面。

禁止做成单纯聊天气泡。

页面应支持混合事件流：

```text
User Message
Assistant Message
Tool Call
Tool Result
Approval
Diff
Plan
Status
Error
Usage
```

推荐结构：

```text
Header
  Agent · Project
  Session State
  Node State

Timeline
  User prompt
  Assistant reasoning/status summary
  Tool cards
  Diff cards
  Approval cards
  Final answer

Composer
  Text input
  Send
  Cancel when running
```

---

## 11. Timeline Event Components

前端必须采用事件组件渲染，不允许把所有 Event 都渲染成 plain text。

推荐组件：

```text
UserMessageCard
AssistantMessageCard
ToolCallCard
ToolResultCard
ApprovalCard
DiffCard
PlanCard
StatusCard
ErrorCard
UsageCard
```

未知事件：

- 不崩溃
- 显示通用 Unsupported Event / Metadata Card
- 开发环境记录协议版本与事件类型

不得通过猜测字段名渲染未知事件。

---

## 12. Message Streaming

Assistant streaming：

- 实时展示 delta
- delta 只更新当前未完成 message block
- `message.completed` 后冻结为最终内容
- reconnect 后根据 Gateway 历史事件重建

禁止：

- 每个 token 创建一个 UI item
- 每个 delta 都写本地永久 storage

需要做节流更新，避免小程序频繁 setData / reactive update 导致性能问题。

---

## 13. Tool Call Card

Tool Call 卡片至少包含：

- Tool Display Name
- State
- Short Summary
- Start / Finish
- Optional Result Summary

状态：

```text
running
completed
failed
```

对于超长 Tool Result：

- 默认折叠
- 显示摘要
- 用户手动展开

不要默认在 Timeline 展开大段日志。

---

## 14. Approval UX

Approval 是移动端核心卖点之一。

Approval Card 显示：

- Agent
- Node
- Project
- 操作描述
- 风险 / 权限摘要
- 可查看详情

按钮根据 capability 展示：

```text
Allow Once
Allow Session
Deny
```

如果 `Allow Session` 不受支持，前端不得自行显示。

Approval 提交时：

- 按钮进入 submitting
- 防止重复点击
- 使用 request identity
- 区分服务端已接收选择与 Node 已回传原生请求；收到完整闭环结果后更新最终状态

过期 Approval：

- 不允许继续点击
- 明确显示 Expired

---

## 15. Approval Inbox

独立 Inbox 页面必须存在。

排序：

1. pending approval
2. waiting input
3. failures
4. completed

每条显示：

```text
Agent
Project
Node
Short description
Age
```

目标是：

> 用户不需要打开每个 Session，就能处理所有需要人工介入的任务。

---

## 16. Diff UX

Diff Card 在 Timeline 中展示摘要：

```text
3 files changed
+42 -17
```

点击查看详细 Diff。

详细视图支持：

- File List
- Added / Removed Lines
- Collapsed File
- Long Diff Lazy Render

MVP 不要求代码编辑。

Diff 是查看用途，不是移动端 IDE。

---

## 17. Plan UX

Agent 支持 Plan capability 时：

```text
Plan
✓ Inspect compose
• Fix service config
○ Run tests
```

Plan 不支持时，整个入口隐藏。

前端根据 capability 渲染，不通过 Agent 名称硬编码。

错误：

```text
if agent == codex show plan
```

正确：

```text
if capability.plan == true
```

字段精确定义以最终协议 schema 为准。

---

## 18. Cancel UX

Session / Turn running 时显示 Cancel。

点击：

```text
Cancel
  ↓
Confirmation if destructive/long task
  ↓
Send idempotent request
  ↓
Show cancelling
  ↓
Receive confirmed state
```

不能仅因为客户端发出 Cancel 就立即在 UI 显示 cancelled。

最终状态以 Gateway / Node Event 为准。

---

## 19. Node Offline UX

当 Node offline：

Session 页面顶部显示：

```text
Node offline
Last known session state: running
```

不要把 Session 本身直接改成 error。

操作：

- Send Prompt 禁用
- Approval 若无法处理则禁用并说明
- Cancel 不允许静默排队
- 历史内容仍可查看

Node 重新上线后：

- WSS 收到 presence update
- UI 自动恢复
- 等待 reconciliation Event

---

## 20. Client WebSocket

原则：

```text
one client runtime
=
one user-level websocket
```

负责：

- Node presence
- Session event
- Approval update
- Notification update

需要：

- reconnect
- exponential backoff
- auth refresh integration
- sequence resume

前端不能把 WebSocket 当持久真相源。

Gateway 历史接口才负责补偿。

---

## 21. Session Event Resume

客户端对每个正在查看 / 活跃 Session 记录已连续应用的持久事件位置。实时预览不得推进该位置。冷启动只有旧游标、没有对应正文时，必须先重建快照或历史基线，不能只请求游标之后的消息。

客户端跟踪：

```text
last confirmed sequence
```

断线恢复：

```text
Reconnect WSS
  ↓
Fetch / sync missing events after N
  ↓
Merge events by sequence
  ↓
Resume realtime stream
```

要求：

- 去重
- 按 sequence 排序
- 不通过时间戳猜顺序
- 遇到 sequence gap 必须主动补取

---

## 22. Client State 分层

建议分成：

```text
AuthState
ConnectionState
NodeState
ProjectState
SessionIndexState
SessionTimelineState
ApprovalState
NotificationState
```

不要使用一个巨大 global store 保存所有 UI 和协议状态。

### 本地临时状态

可以保存：

- 当前页面输入草稿
- last sequence
- 最近查看 Session ID
- UI 展开 / 折叠状态

### 不应长期保存

- Agent Secret
- Node Device Secret
- 完整敏感 Session 副本

---

## 23. HTTP 与 WSS 分工

### HTTP

- Login
- Node list
- Pairing
- Project list
- Session list
- Session metadata
- Event history
- Approval list
- Notification list
- Settings

### WSS

- Realtime Node state
- Realtime Session Event
- Approval push
- Notification push

前端不得为了实时状态使用高频 HTTP polling。

---

## 24. Error UX

错误由稳定 error code 驱动。

前端不得通过：

```text
message.includes("offline")
```

这类字符串匹配判断错误类型。

必须支持的 UX：

### Node Offline

```text
This node is offline.
```

### Approval Expired

```text
This approval is no longer active.
```

### Unauthorized

强制返回安全页面 / 登录处理。

### Protocol Mismatch

提示：

```text
Node / client version is incompatible.
```

并提供升级建议入口。

---

## 25. Capability-driven UI

所有 Agent 特性由 capability 控制。

例如未来可能有：

```text
streaming
resume
fork
approval
diff
plan
attachments
usage
terminal
session_history
model_switch
```

前端不得因为 `agent type` 自行假设能力。

例如：

错误：

```text
Claude always has approval
```

正确：

```text
Gateway says this session supports approval
```

---

## 26. Agent 品牌差异

允许：

- Agent icon
- Agent display name
- Brand color as secondary visual metadata

不允许：

- 每个 Agent 一套业务页面
- 每个 Agent 一套消息解析
- `/codex`、`/claude` 页面逻辑分叉

核心 UI 必须统一。

---

## 27. Notification

小程序内 Notification Inbox 第一版必须有。

展示：

- Pending Approval
- Session Completed
- Session Failed
- Waiting Input
- Node Offline

通知点击后跳转到目标 Session / Approval。

如果后续接微信订阅消息，应视为独立扩展，不影响核心 Inbox。

---

## 28. 安全与隐私

前端不得展示或保存：

- Agent API Key
- SSH Private Key
- Node Device Secret
- Agent Credential

即使 Node 上存在，也不允许通过普通 API 返回。

涉及高风险操作必须至少展示：

- Node
- Project
- Session
- Agent
- 操作摘要

避免用户在手机上误批错设备。

---

## 29. 性能策略

重点优化 Session Timeline。

要求：

- Long list virtualization / incremental render
- Tool Result 默认折叠
- Diff lazy render
- Streaming throttling
- 不对每 token 重建整个 Timeline
- 页面离开时释放不必要订阅

Session 历史很长时：

- 分页加载旧事件
- 默认定位到最新
- 向上滚动加载历史

---

## 30. 空状态

必须设计：

### No Node

```text
No device connected
[Add Node]
```

### Node Offline

```text
Desktop-PC is offline
Last seen ...
```

### No Project

提示用户先在 Node 端注册 / 配置项目，而不是让小程序猜路径。

### No Session

```text
No sessions yet
[Start Session]
```

### No Approval

```text
You're all caught up
```

---

## 31. MVP 页面流程

### 首次用户

```text
Login
 ↓
No Node
 ↓
Add Node
 ↓
Scan QR / Enter Code
 ↓
Confirm Node
 ↓
Home
```

### 创建任务

```text
Home
 ↓
Node
 ↓
Project
 ↓
Agent
 ↓
Prompt
 ↓
Session Detail
```

### 离开电脑后的审批

```text
Mini Program Open
 ↓
Inbox: 2 pending
 ↓
Approval Detail
 ↓
Allow Once
 ↓
Session continues
```

### 断线恢复

```text
WSS disconnect
 ↓
UI shows reconnecting
 ↓
Reconnect
 ↓
Fetch missing event sequences
 ↓
Timeline recovered
```

---

## 32. 前端工程结构

建议：

```text
src/
  app/
  pages/
    home/
    nodes/
    projects/
    sessions/
    approvals/
    notifications/
    settings/

  components/
    session/
    events/
    node/
    approval/
    common/

  services/
    api/
    websocket/
    auth/

  stores/
    auth/
    connection/
    nodes/
    sessions/
    approvals/
    notifications/

  protocol/
  utils/
```

`protocol/` 必须来自统一协议 schema 或由其生成。

禁止在页面中复制协议接口类型。

---

## 33. 组件职责

### 页面组件

负责：

- route lifecycle
- view composition
- page-level loading

### Event Components

负责：

- 纯渲染统一事件
- 不直接调用 Gateway mutation API

### Service

负责：

- HTTP
- WSS
- auth refresh
- protocol decode

### Store

负责：

- normalized state
- merge events
- sequence tracking

禁止组件直接自行维护独立 WebSocket。

---

## 34. 协议处理

收到 Unified Event 后：

```text
Validate
 ↓
Check protocol version
 ↓
Check session authorization context
 ↓
Deduplicate sequence
 ↓
Update Store
 ↓
Render
```

未知字段不能靠名称猜测。

未知 event type：

- 通用 fallback
- 不影响已有 Timeline
- 开发日志记录

---

## 35. 本地缓存策略

允许缓存：

- Node list metadata
- Session list metadata
- Last sequence
- 用户 UI preference

Session 正文缓存：

第一版建议以短期内存缓存为主，避免默认把大量 Agent 内容持久写入小程序本地存储。

如果后续需要离线历史，必须单独设计隐私策略。

---

## 36. 用户交互原则

移动端优先：

- 单手操作
- 重要按钮底部可达
- Approval 操作明确
- 不密集展示完整日志
- 默认摘要、按需展开

对于 Agent Tool Call：

> 用户最需要知道“做了什么、结果如何、是否需要我处理”，而不是观看所有底层 stdout。

---

## 37. 第一版明确不做

Coding Agent 不得擅自把以下内容加入前端 MVP：

- 完整 SSH Terminal
- Remote Desktop
- 文件编辑器
- IDE-style 多窗格布局
- Git GUI 全功能客户端
- Team / Organization
- Marketplace
- MCP 管理页面
- Workflow Builder
- Scheduler UI
- Billing
- P2P 网络配置

---

## 38. 前端测试要求

### Unit

至少覆盖：

- event reducer / merge
- sequence dedup
- sequence gap detection
- capability rendering
- approval disabled state
- node offline interaction state

### Component

至少覆盖：

- ApprovalCard
- ToolCallCard
- DiffCard
- SessionHeader
- Error state

### Integration

使用 Fake Gateway：

1. Node online push
2. Session create
3. message delta
4. tool started/completed
5. approval requested
6. approval resolved
7. WSS disconnect
8. reconnect
9. missing event sync
10. Node offline

测试不得依赖真实 Codex / Claude / Pi。

---

## 39. MVP 验收标准

- 用户可以登录
- 用户可以扫码 / 配对码绑定 Node
- 首页显示 Node 在线状态
- 用户可以浏览 Project
- 用户可以选择可用 Agent
- 用户可以创建 Session
- Session Timeline 支持 streaming message
- Tool Event 有独立卡片
- Approval 有独立卡片
- Approval Inbox 可用
- Diff 可查看
- Usage metadata 可查看
- Cancel 可操作
- Node offline 时操作正确禁用
- WSS 断线可以自动恢复
- Session Event 可以按 sequence 补齐
- Timeline 不因未知事件崩溃
- UI 不包含 Agent-specific 业务分叉
- 前端不保存 Agent Secret

---

## 40. Agent 开发约束

交给 Coding Agent 实现时必须遵守：

1. 不得自行猜测 Gateway 字段。
2. 所有字段以最终 protocol schema 为准。
3. 不得根据 Agent 名称硬编码 capability。
4. 不得通过字符串内容判断 error type。
5. 不得为 Codex / Claude / Pi 创建独立业务架构。
6. 不得把全部 Session Event 当普通聊天消息。
7. 不得把 WebSocket 当作历史真相源。
8. 必须实现 sequence 去重与补偿。
9. 必须避免 streaming 高频刷新整个页面。
10. 不得把 API Key、Credential、Device Secret 落入前端。
11. 任何不确定的微信小程序 API、生命周期、网络限制必须先查实际项目环境与当前官方文档，不得猜测函数名或配置字段。
12. 任何新增页面或功能若超出本文 MVP，必须先记录设计变更，不得擅自扩 scope。

---

## 41. 最终产品形态

```text
Home
  ↓
Nodes / Recent Sessions / Needs Attention

Sessions
  ↓
Unified Agent Sessions

Session Detail
  ↓
Message + Tool + Diff + Approval + Plan + Status

Inbox
  ↓
Approval / Waiting Input / Complete / Failed
```

前端最重要的产品原则：

> 不把手机变成缩小版 IDE，而是把 Agent 的“状态、决策点、结果”提炼成移动端最容易处理的交互。

因此移动端的核心价值排序应始终是：

```text
Approval
> Waiting Input
> Status
> Result
> Diff
> Tool Details
> Raw Terminal Output
```

只要保持这一原则，即使后续接入更多 Coding Agent，前端仍然可以维持统一体验。

---

## 42. 全面适配的界面原则

前端保留统一 Session 页面，同时通过经过 schema 校验的扩展交互显示各 Agent 的独有能力。通用页面不硬编码外部 Agent 的方法名、键名或数据路径；专属功能由 Node Adapter 转换为统一组件可理解的动作描述。

| 功能区域 | 用户操作 | 显示条件 |
| --- | --- | --- |
| 会话 | 新建、继续、恢复历史、原生分支 | 当前 Agent 版本与运行模式已验证支持 |
| 输入 | 发送、运行中追加、完成后排队 | 根据各模式真实生效时机分别展示 |
| 人工处理 | 批准/拒绝工具、回答选择题、输入文本 | 来自当前未结束的原生请求 |
| 成果 | Diff、只读文件、生成文件、图片附件 | 来源归属明确、大小与类型通过检查 |
| 控制 | 取消当前轮次、修改可用模型与档位 | 当前轮次和权限允许，并有原生回执 |
| 扩展 | Skills、命令、扩展确认与状态 | 当前 Node 实际加载并上报的目录 |
| 过程 | 计划、工具进度、子任务、用量 | 原生事件或明确标注的本机数据来源 |

不能只写“全面支持 Codex / Claude / Pi”。设备详情中的兼容性记录应分别列出已验证能力、尚未联调项与接口不提供的项。

## 43. 会话入口与状态细化

创建页区分“新建托管会话”和“恢复历史会话”。只有 Node 已验证对应控制入口时才提供“连接运行中的会话”。只读导入的聊天记录不显示发送或取消按钮。

会话页同时表达三类状态：

- 任务状态：正在执行、等待审批、等待回答、本轮结束、已取消、出错。
- 连接状态：手机连接恢复中、Node 离线、连接正常。
- 控制状态：可发送、等待执行确认、正在对账、仅查看。

本轮结束仍可继续同一会话；不要把完成一轮视为会话永久关闭。Node 离线显示最后已知任务状态及更新时间，不暗示任务仍在正常运行。

多端同时打开时，审批完成、模型变更、取消等更新必须同步。旧页面中的操作如果针对已结束轮次或已处理问题，展示对应结果，不能自动套用到最新请求。

## 44. 输入、审批、问题与原生扩展

输入区明确区分“追加本轮指令”与“完成后执行”；只有后端明确描述其支持情况时显示。Pi RPC 中两种队列语义已有官方说明，不能把它们都包装成“立即执行”。[Pi RPC](https://pi.dev/docs/latest/rpc)

发送动作先显示送达进度，收到原生接受结果后再确认该条消息进入执行链路。取消操作显示“正在取消”，收到实际轮次终态后才显示结束；取消本身不代表已撤回此前文件修改。

人工输入使用独立卡片：

| 输入类型 | 组件行为 |
| --- | --- |
| 工具审批 | 显示目标设备、项目、实际动作和该次可用选项 |
| 选择问题 | 原样保留原生选项的标识与含义，支持已声明的单选或多选 |
| 自由输入 | 回答提交到对应问题，不当成新的普通 Prompt |
| 扩展确认/编辑 | 使用预定义表单组件，保留取消与超时语义 |

Claude SDK 通过 `canUseTool` 接收权限请求和 `AskUserQuestion`；两种情况需在 UI 上分别呈现。[Claude 用户输入](https://code.claude.com/docs/en/agent-sdk/user-input)

Pi RPC 支持扩展选择、确认、输入和编辑对话框；必须等待用户响应的对话框不能静默忽略。需要真实终端的自定义 TUI 组件显示明确的适配限制。[Pi 扩展 UI](https://pi.dev/docs/latest/rpc)

所有可提交卡片都要区分用户已选择、正在回传、原生请求已处理。只有 Gateway 收到 HTTP 请求时，不显示“Agent 已继续”。界面不额外编造原生审批范围。

## 45. 模型、扩展与成果

模型选择列表、推理档位与扩展命令目录来自当前 Node。前端保存其精确标识，展示名称可以独立本地化，但不得用展示名称反推原生标识。

配置页显示已生效值与此次改动的作用范围，例如当前会话或未来会话；没有原生确认时保留旧值并显示失败原因。各 Agent 的 Skills、MCP 和插件是否加载，以真实运行时状态为准。

Diff 页面标明来源：本轮 Agent 变更，或当前项目工作区变更。项目中的用户手动修改不能全部归因于某个 Agent。文件只读查看与附件管理使用已授权资源引用，不拼接远端绝对路径。

图片附件显示上传中、Node 已接收、已提交 Agent 三个阶段，只有对应阶段确认后推进状态。其他文件类型要先核实适配器和模型支持；手机选择成功不代表 Agent 能理解该文件。

子任务、工具输出和用量缺失时明确显示数据未提供，不从普通文本推导精确百分比、价格或 Token 数。只展示接口实际公开返回的推理或摘要信息。

## 46. 微信连接与断线恢复

自建后端的通讯域名按微信当前规则配置 HTTPS / WSS，关闭跳过域名校验后进行真机验证。发布所需实际域名、证书、小程序基础库版本与后台配置由项目环境提供。[微信网络说明](https://developers.weixin.qq.com/miniprogram/dev/framework/ability/network.html)

连接由应用级服务持有；页面只管理业务订阅。使用 `wx.connectSocket` 返回的 SocketTask，同步注册连接事件。接口调用成功不等于网络已打开，还需完成业务认证。[微信网络调优](https://developers.weixin.qq.com/miniprogram/dev/framework/performance/network.html)

恢复流程按后端第 38 节执行：先获取有覆盖位置的快照或已有历史基线，再补齐持久事件，最后与实时订阅衔接。补历史期间到达的实时事件需要缓冲、去重，不能在“补完历史”和“重新订阅”之间留下消息空隙。

前后台切换不改变远端任务生命周期。回到前台后重新校验授权、连接与待处理问题，不依赖后台永久长连接。小程序内 Inbox 保持为第一版核心；微信订阅消息独立接入，具体权限与模板另行读取后台配置，不承诺无限后台推送。

## 47. 前端适配验收

| 场景 | 通过条件 |
| --- | --- |
| 三种 Agent 同时有任务 | 同一会话列表显示，信息和操作不会串到其他设备 |
| 审批与普通问题同时到达 | 两类交互分别处理，回答精确回传 |
| Pi 扩展确认 | 选择、取消、超时均正确结束对应等待 |
| 运行中发送新指令 | 明确显示追加或排队，实际顺序与标签一致 |
| 本轮取消 | 收到真实结束事件后显示终态，不伪称撤回修改 |
| 清空内存后重开小程序 | 已有正文可重建，不因只保存旧游标出现空历史 |
| 断线时消息尚未完成 | 恢复未完成消息及其后续内容，无重复拼接 |
| 已处理审批页面再次提交 | 显示原处理结果，不执行第二次 |
| 未知显示事件 | 已有 Timeline 正常；需要输入的请求给出明确处理状态 |
| 模型、插件加载失败 | 展示真实错误和原有效配置，不提前显示成功 |

Fake Gateway 用于前端独立开发；全面适配验收必须补充 Node 与真实 Agent 的闭环记录，并在指定基础库和真机环境验证。

## 48. 尚需读取的实施输入

现有文档提供总体设计，但未提供本次实现仓库、微信小程序项目配置、主机系统与 Agent 版本、Claude 的实际认证方式。后续实现时先读取这些材料，再冻结目录、依赖、接口 schema 和配置字段。无法取得精确信息时向用户索取，不通过近似名称、版本印象或示例路径补齐。

本次完整产品范围覆盖三种 Agent 的深度适配；额外 Agent 按后端适配门槛逐项加入。终端镜像、远程桌面和无条件接管任意已运行进程不计入已验证能力。

## 49. 功能遍历基线与页面设计顺序

先使用 [frontend-function-inventory.md](frontend-function-inventory.md) 做功能检查，再绘制页面。该清单纳入会话整理、分支树、输入队列、上下文压缩、重试控制、子任务与后台任务、账号额度、扩展诊断、文件版本和检查点还原等原方案未逐项落位的能力。

每个功能编号需在原型中对应一个入口和结果状态。页面按总览、会话、待处理、我的四个一级入口组织；会话页承担状态、时间线、输入和取消当前轮次，其他功能使用明确的二级页面或面板。

绘制顺序：全局状态与导航 → 首次使用与设备配对 → 会话主流程 → 审批与问题 → 历史分支、模型、上下文和队列 → 计划任务与成果 → 扩展诊断、设置和恢复流程。

## 50. 当前设计阶段的完整性门槛

- 功能清单中的每项都有页面/组件入口或明确的内部处理归属。
- 取消当前轮次、停止单个后台任务、停止重试和关闭进程分别表达，不共用含糊操作。
- 原生条件功能在能力详情保留说明；未支持、未联调与暂时不可用分别显示。
- 加载、空、失败、过期、失权、离线、提交中和结果未知都属于原型交付范围。
- 所有外部标识符从实际材料提取；本文新增需求编号只用于设计追踪。
- 原生目录中的高级管理、终端、市场安装和账号副作用动作保留范围记录，不自动进入原 MVP。
- 设计检查不能替代真实 Agent 联调；本轮不依赖仓库路径或账号凭据即可完成需求遍历。
