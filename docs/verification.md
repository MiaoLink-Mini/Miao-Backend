# 验证记录

日期：2026-09-06。工作目录：`WeAgent-Backend`。

实际测试环境：Windows、Go 1.26.5 windows/amd64、GCC（w64devkit）、Node.js v24.1.0、npm 11.6.4；**原生 PostgreSQL 18.3** 专用回环集群，以及 JS 测试独立使用的 PGlite 0.5.8（PostgreSQL 18.3 / wasm32）。原生服务来自官方 Windows binaries，不依赖 Docker daemon。

## 本次已执行

```powershell
gofmt -w internal cmd
go vet ./...
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1 -Race
# 另在同类专用数据库 fixture 上采集最终 JSON 测试记录：
go test -race -count=1 -timeout 180s -json ./...
node scripts/generate-contracts.cjs --check
npm test
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/dev.ps1 -Port 18080 -WithFakeNode -SmokeSeconds 3
```

Go 集成命令均设置专用 `TEST_DATABASE_URL`，**未跳过集成测试**。最终 Go JSON 记录统计（包含 21 个顶层测试和 77 个 Schema 子测试；顶层含 1 个子进程 helper）：

```text
Go tests: 98 passed test records (including subtests); 0 skipped; 0 failures
pass weagent/backend/internal/protocol 1.249s
pass weagent/backend/internal/gateway 18.868s
```

`go vet ./...` 退出码 0；race 未报告数据竞争。生成检查输出 `Go/TS contract shapes: 72 definitions`。最终 JS 输出：

```text
tests 101
pass 101
fail 0
cancelled 0
skipped 0
todo 0
```

| 检查 | 结果 |
| --- | --- |
| HTTP / WS 实现 | canonical OpenAPI 注册 35 个 HTTP 操作；两种 WS 独立权限及严格入出站模型；非占位 handler |
| 配置 / 认证 | 缺密钥 / 正式微信参数失败；开发身份限制回环；provider HTTP fixture 校验微信兑换参数；两类 Bearer 不可混用 |
| 配对 / 撤销 | enrollment → preview → confirm → Ed25519 challenge / prove；错误签名、另一用户撤销、撤销后权限与旧凭据检查 |
| 会话 / 请求 | 创建、同 Session 新 Turn、accepted 与 confirmed 区分、幂等冲突、动态未知选项 / 字段拒绝、相同回答共享赢家、不同答案拒绝、审批到期 |
| 队列 / 取消 | 显式队列持久化、取消保留队列；终态 payload 清理保留未消费队列及尚未交接的 send；消费后新轮次正确 |
| 事件 / 历史 | durable before ACK / live、连续 sequence、重复 source 不追加、同 ID 改正文拒绝、final 后迟到 delta 不重复追加、固定 highWater 分页 |
| 冷恢复 | ready 屏障先于 live；checkpoint 完整投影、过期页 410；模拟前缀裁剪后 cursor 410，独立 receipt 仍去重 |
| 重连 / 时间窗 | Node epoch 替换、旧 close 不标新连接离线；HTTP deadline / Gateway 重启只 query journal、不重发 command |
| journal 矛盾 | 已确认 delivered 后再报 not_seen 返回 SOURCE_CONFLICT，保持 reconciling，禁止当作未执行重发 |
| 独立 Fake Node | 真子进程身份 / HTTP / WS / 本地 journal / spool；重启后同操作模拟调用计数仍为 1；接受后崩溃保持 unknown |
| 连接生命周期 | 实际 WS Ping/Pong、心跳超时下线、错误 Origin 拒绝、暂停写循环后的真实有界队列溢出断开 |
| 数据库可用性 | 第二 Gateway 无法取得独占锁；终止记录的 lease PG backend 后 Gateway drain、ready 503、WS 关闭 |
| 保留期 | 当前 Turn 结束满 30 天清理正文 / Diff、阻止再上传；purged 会话不阻塞 Node readiness；满 90 天移除已解决 Session |
| 原生数据库迁移 | 全新隔离 PG 数据库 goose Up → Down → Up，保留无关 fixture 表；实际多连接 pgx 事务 |
| JSON Schema 2020-12 / Ajv strict | 72 个定义全部编译 |
| 合约实例 | 44 正例 + 33 反例；覆盖提问、审批、排队、回执、版本、路径、序号和未知控制 |
| OpenAPI | 3.1.1 通过 Swagger Parser；34 路径 / 35 操作，外部网络解析关闭 |
| 引用 / 鉴权 / 幂等声明 | 本地 $ref 可解析，业务变更声明幂等键，两种 WS 权限分离 |
| 数据库 Up | 原生 PG 与内存 PGlite 均创建 25 张业务表 |
| 数据库约束 | 跨用户 / 跨 Node / 跨 Turn 关联、active turn 唯一、outbox 目标、幂等键、请求预占、事件去重等 fixture 通过 |
| 事务回滚 | counter 和事件一同回滚，重用 sequence=1 无空洞 |
| Down → Up | 删除设计表，保留无关 fixture 表，再成功重建并写入种子 |
| 来源比对 | 前端文件及 3 份上级设计文档 SHA-256 未变 |
| 产品映射 | 20 个页面、194 项需求完整保留原身份 / 状态；98 个仍标为前端演示交互 |
| 边界保护 | LiveGateway 仍抛 LIVE_NOT_CONFIGURED，前端 config 仍是 demo |
| 本机进程 smoke | 构建 Windows Gateway / migrate / fake-node；18080 ready / health、Fake Node enrollment 成功；按记录 PID 清理，18080 / 55432 无残留监听 |
| Linux / 容器配置 | `GOOS=linux GOARCH=amd64 CGO_ENABLED=0` 成功构建 Gateway 和 migrate；仅用 fixture 环境值执行 `docker compose config --quiet`，退出码 0 |

所有数据库写入只在自建专用开发库和随机 `weagent_test_*` fixture 内，没有访问现有业务库或提交真实 Agent 命令。Go race 记录、数据库连接凭据、Node 私钥和日志只放在忽略目录 `.runtime/`。JS 的前端来源检查仍保持 D0 基线，不把这份设计归属表当作全部产品功能已上线。

回归中实际发现并修复：metadata 更新导致清理窗口延长；队列 / 新 Turn 预留被 payload 清理过早移除；purged 会话仍可能被 Node 续写。上述修复均有原生 fixture 回归，不仅修改文档。

## 未执行，不能声称通过

- 真实微信 AppID / AppSecret 的身份兑换、合法域名、TLS 代理和真机 HTTPS/WSS；provider fixture 不是外部微信联调。
- Claude / Codex / Pi 原生 Node Adapter，以及真实原生取消 / 提问 / 能力探测；Fake Node 明确只是模拟器。
- Docker 镜像实际构建 / 运行和 Linux 二进制实际执行：本机 daemon 不可用，交叉编译不等于容器部署通过。
- 生产多用户吞吐、长时间内存峰值、海量事件 / 锁争用、磁盘断电 / 满盘、所有限流边界与故障组合。
- 高负载下进程级 SIGTERM 停机预算和网络故障时每一种关闭码；现有测试验证组件关闭 / 重启，开发 smoke 由脚本停止记录 PID。
- 备份 / restore、数据加密、生产 DDL / DML 角色拆分、密钥轮换和备份保留期运维。
- 小程序 LiveGateway / Runtime 生产接入与 UI 真机回归；本次没有改前端文件或重开开发者工具。

当前交付覆盖 Gateway 后端实现，不能等同于 D4 前端生产接入或 D5 原生 Adapter 完成。后续矩阵见 [整体设计](backend-design.md) 和 [Node 契约](node-contract.md)，复现命令见 [运行指南](running.md)。
