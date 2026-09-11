# 与现有 WeAgent-Frontend 的接入契约

> 原生能力增量：新增 `POST /v1/sessions/{id}/native`、可选 models / compact capabilities、类型化原生回执与独立压缩 turn。三端实现和升级顺序见 [原生能力计划](../../WeAgent-Node/docs/native-controls-plan.md)。原 source-baseline 保留为设计快照，不再冻结可变实现源码的 hash；测试继续校验冻结的页面 / 需求身份、参考设计、当前 live schema 和 capability 门控，并运行前端真实链路回归。

> 2026-09-06 接入更新：相邻小程序 LiveGateway / wire / Runtime 已实现，并通过真实 Gateway + PostgreSQL + 独立 Node 三插件协议 fixture 回归。详细修复、证据与未验收边界见 [前端 Review](../../WeAgent-Frontend/docs/integration-review.md)。本文以下正文保留后端设计阶段的接入清单，含“尚未实现”等历史描述，不再作为当前客户端完成度报告。

## 1. 事实基线与非目标

相邻前端为原生 JS / WXML / WXSS，20 个页面，`services/fake-gateway.js` 提供演示数据。`services/live-gateway.js` 的构造函数当前明确抛出 LIVE_NOT_CONFIGURED，`config.js` 仍为 demo。此次不改变 UI、不更换主题、不重写成 React / TypeScript，也不声称生产适配器已经实现。

现有设计中的 Agent 图标、CLI 风格会话、输入 `/` 的本地预测、紧凑一屏请求详情继续保留。后端为这些交互提供正确的数据与状态，不返回 SVG / HTML 页面，也不决定像素布局。

## 2. 20 个页面的数据边界

| 页面 `pages/.../index` | 数据 / 动作 | 接入注意 |
| --- | --- | --- |
| login | POST auth/wechat | wx.login → 真实兑换；登录失败不进入假在线 |
| home | bootstrap counts + 首屏资源 + changes | counts 来自服务端全量授权范围 |
| sessions | GET sessions，q / nodeId / projectId / agentId / state / 分页 | 搜索不是从已加载首屏中假装搜索全库 |
| inbox | GET requests?bucket=pending/history；notifications；read | 两种请求统一待处理，deciding 禁用、未决不计作已成功 |
| me | bootstrap.user、nodes、audit | 退出清 token / socket / 正文 / 草稿，不关闭远端任务 |
| nodes | GET nodes | revoked 记录可显示，但关联内容无权继续读取 |
| node | GET nodes/{id}，projects / agents / sessions 按 nodeId | revoke 使用独立幂等操作；在线由 Gateway 计算 |
| projects | GET projects | 显示 invalid；不可新建但既有历史保留 |
| project | GET projects/{id} + sessions?projectId | 无绝对目录或任意读取路径 |
| pair | pairings/preview → confirm | 指纹预览与确认分开；扫码只填短码，不自动授权 |
| create | nodes / projects / agents，POST sessions | 加 capabilityRevision；返回回执不等于 Agent 已运行 |
| session | session、events、checkpoint、watch；messages / cancel | 分离业务状态、传输状态、Operation；完成可继续 |
| request | GET requests/{id}；respond | approval / question 统一资源；动态答案校验；一屏样式不变 |
| agent | GET agents/{id} | 版本 / ready / capability；不假设品牌一定支持 queue |
| panel | 已授权 timeline 的 plan / diff / usage item | 保持本地面板；未知费用为 null 而非零 |
| file | GET diff-files/{id} | 只读 Diff 快照；不把 patch 拼成“完整文件” |
| capabilities | 前端本地清单 + Agent/Session capabilities | 演示完成度不等于真实 Agent 支持度 |
| requirement | 前端本地 194 项说明 | 不新增后端“功能已实现”假接口 |
| settings | 本地界面 / 通知偏好 | retention 是部署政策，不把本地 UI 偏好当作已更新云端 |
| diagnostics | 连接 / 版本 / 错误码 / cursor 等脱敏元数据 | emit / setOnline / 注入故障只用于 demo，不开生产公网路由 |

统一 API 前缀为 `/v1`，健康检查除外。细节以 `contracts/http.openapi.json` 为准；WS 两个入口在其 `x-websockets` 中定义，帧模型引用同一 Schema。

## 3. Gateway facade 对照

| 现有方法 | 生产传输 / 数据 | 必要改造 |
| --- | --- | --- |
| snapshot() | bootstrap + 按页加载 + changes 驱动的本地镜像 | 可以继续同步读缓存，但要有 hydrated / loading / stale / nextPageToken，不是同步联网 |
| subscribe(listener) | 一个全局 client WS 的本地事件总线 | 页面只订阅，不为每页开 socket；登录后再真正连接 |
| events(id,after) | GET sessions/{id}/events?after&until | 从数组改为 EventsPage，循环至固定屏障，不固定最多 4 次 |
| operation(id) | 缓存 + GET operations/{id} | 新增异步查询；未知不自动重发原命令 |
| id(prefix) | 客户端安全随机标识 | 不能用 demo 的时间 + 递增数作为安全随机键 |
| create(input,opId) | POST sessions；Idempotency-Key=opId | input 补 Agent capabilityRevision，回执 result.sessionId 导航 |
| send(sessionId,turnId,text,mode,opId) | POST sessions/{id}/messages | turnId → expectedTurnId；补会话 capabilityRevision |
| cancel(sessionId,turnId,opId) | POST sessions/{id}/cancel | 当前轮匹配，确认取消前保持 cancelling |
| respond(requestId,answer,opId) | POST requests/{id}/respond | 补 expectedTurnId、requestRevision、capabilityRevision；构造 Decision |
| previewPair(code) | POST pairings/preview | 无绑定副作用；trim / 大写化后再校验码 |
| confirmPair(ticketId,opId) | POST pairings/confirm | 用户绑定的 ticket，不信任界面传来的 nodeId |
| revoke(nodeId,opId) | POST nodes/{id}/revoke | 确认后清理该 Node 所有派生缓存 |
| markRead(notificationId) | POST notifications/{id}/read | 补 opId 幂等键；按授权资源提交 |

`FakeGateway.data/history/session/emit/card/setOnline` 没有生产接口。真实适配器不使用定时器模拟 confirmed，也不能调用 fake.run() 来代替 Node。

### 响应投影

- Timestamp → `Date.parse()` 得到现有 UI 使用的毫秒；解析失败为协议错误。expires 倒计时使用 bootstrap.serverTime 与客户端时钟偏移，最终仍由服务端判过期。
- 生产 `Session.turnId` 在 idle 可为 null，UI 在未就绪时不能 send/cancel；新增 idle、historyState、revision、capabilityRevision 的处理。
- Agent name 交给既有 `utils/agent-brand.js` 做本地图标选择；服务端不传可执行 SVG 或任意图片 URL。
- `request.decision` 映射旧 `request.answer`：approval 用 choiceId，question 用 answers。deciding / expired 必须显式呈现，不能统一当 cancelled 或 resolved。
- 当前 request / session 控制主要检查 `session.agent.capabilities`，需改为 `session.capabilities` 与 managed / 在线 / 当前轮次的交集；Agent 级能力仅用于新建与概览。
- `Notification.sessionId` 可以为 null（system），Runtime 不能再过滤掉所有没有 sessionId 的系统通知。
- `Operation` 的 accepted/delivered/confirmed/reconciling/failed 驱动控件；提交超时 unknown 是前端暂态，不能立即进入 failed 并允许新的重复提交。

## 4. 生产事件不是 demo-view/1

现有 `stores/timeline.js` 只接受 `version='demo-view/1'`，要求 string turnId，并支持 message.delta / message.completed / card.upsert。新 wire 是 `weagent/1`，某些纯 metadata 事件 turnId 可为 null。**不能只替换一个 version 字符串就算接入。**

实施方式：新增独立 wire validator + projector，生产 view 协议使用 `weagent-view/1`，reducer 明确支持该版本；demo 继续使用原分支。TS 类型 / Go 类型已从 canonical JSON Schema 生成至 `contracts/protocol.generated.d.ts` / `internal/protocol/types_generated.go`，不手写第二套字段真相。Go 运行时校验已实现；小程序 JS validator、wire projector 与 Runtime 接入尚未实现，不能把生成类型当成完整 SDK。

| 生产事件 | view 投影 | 游标处理 |
| --- | --- | --- |
| message.delta | 按 role 分 user / assistant；合并同 itemId | 每条 seq 必须消费；已 final 忽略正文追加但仍推进 seq |
| message.completed | 同 key 完整替换；truncated 显示提示 | 不重复追加；保留 turnId |
| item.upsert | message / tool / plan / diff / usage / result 等明确映射 | 每个 item 的 wire 类型与 UI 类型可能不同 |
| request.upsert | 更新 request 缓存 + approval / question 卡片 | 不能把此事件只更新缓存后丢弃 seq |
| session.state | 更新 Session 元数据 + `view.noop` | 无正文变化也推进 seq |
| queue.updated | 更新队列 + `view.noop` | `/queue` 读取同一镜像 |
| capabilities.updated | 更新会话级能力 + `view.noop` | 禁止旧控件提交 |
| diff.file | 更新授权文件快照缓存 + `view.noop` | 后续 Diff item 引用 fileId |
| 已知版本未知展示事件 | fallback 安全纯文本，保留 id / seq | 不留永久缺口；需要输入的未知交互提示转主机 |

`view.noop` 是待实现的新 reducer 分支，不是当前已有行为。不能把 null turnId 随便归到当前轮；无正文事件直接消费 cursor，不造一个错误的聊天气泡。checkpoint 从完整 TimelineItem 投影一次性安装；其 request 引用要结合当前已授权请求缓存加载，已失效只读提示不重新提供操作。

Tool 输出需映射 `output → detail`，summary → text；Plan steps 映射现有卡片字段；Usage 缺失项不显示为实测 0。Diff 的 wire FileSummary 为 fileId/path/count，旧组件需要的 `files[].id/lines` 必须在打开面板 / 文件时由快照解析获得。当前 file 页删除 removed 行后拼“文件正文”的行为只适合 demo；生产应标为变更片段并展示 patch，不宣称是完整文件或支持任意主机路径。

## 5. Runtime / action 必改项

1. `Runtime.login()` 不能只置 auth=true；先 await 真实认证和初始 hydrate，处理用户取消 / 401 / 服务失败。
2. `recover()` 不能写 `gateway.connection='online'`；连接状态由实际 transport 与屏障补偿结果驱动。只同步已打开 / 关注会话，不在启动时抓取所有历史正文。
3. `command()` 不能在返回的 Promise resolve 后无条件 confirmed，也不能提交后立即 accepted。HTTP 202 后使用服务器回执；confirmed 与 turn completed 分离。
4. `utils/page.js` 的 reconcile 改为 await operation 查询；网络错误维持 unknown、禁止换 opId 重试。相同请求回执可以重复 GET。
5. `loadTimeline()` 支持固定 highWater 的分页和 CURSOR_EXPIRED → checkpoint；先保证正文再保存 cursor；恢复期间缓冲 live 帧。
6. `snapshot()` 当前把不在已加载 sessions 的请求 / 通知过滤掉；分页后必须按引用按需补 GET，不可把“尚未加载”当成“没有权限”。Node remove 使用索引级级联失效，不依赖旧列表恰好包含全部资源。
7. 前端保留页面 epoch：旧账号或撤销前发出的 HTTP 完成后不准重新灌入缓存；缓存键含 userId / auth generation。
8. UI unknown / loading / empty / partial error 分开；服务端搜索与分页接入，不改变现有单屏请求详情样式。

## 6. 194 项需求的归属

`contracts/frontend-coverage.json` 保留 D0 审查时所有需求的原 id、name、status、route，并标明 gateway-v1 / node-v1 / client-local / deferred。`backendImplemented` 仍全部为 false：这是冻结的设计归属表，不是当前 Gateway 代码的运行覆盖表，也不能把 98 个演示交互标成生产实现。当前实现与测试以 README / verification.md 为准，尚未进行前端端到端生产验收。

特别边界：F06 原生历史恢复 ≠ F05 已存历史查看；G11 / N03 原生命令目录 ≠ 本地 slash 提示；M06 只读 Diff 片段 ≠ M05 项目文件浏览；O03 显示上报费用 ≠ 实际计费；R02 当前偏好 ≠ 后端用户可改的留存政策。

## Native workspace extension

| Page | Implementation |
| --- | --- |
| workspace | Typed native controls, project files, attachments, native history, platform metadata and export; no arbitrary command editor. |

## Session sharing and onboarding pages

| Page | Scope |
| --- | --- |
| guide | First-use local onboarding and replay; no credentials retained |
| share | Owned session invitations, expiry, revoke and activity |
| shared | Recipient-only scoped history, diff, send/cancel/respond |

## 个人资料（2026-09-07）

| 页面 | 对接 | 说明 |
| --- | --- | --- |
| profile | GET /v1/me/profile、PATCH /v1/me/profile | 微信头像选择、昵称填写；认证用户隔离、版本冲突检测与资料持久化。 |
| about | 本地静态页面 | 产品信息与版本说明。 |

默认头像仅为占位，不自动读取微信昵称/头像。设置为用户主动选择，不作为登录前提。头像经服务端尺寸/格式/体积校验及 JPEG 重编码，不接受外链；仅鉴权后返回本人头像。
