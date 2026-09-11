# 运行与联调

## 1. 本机快速启动（Windows）

要求 Go 1.26.x、原生 PostgreSQL 18；Node.js 22+ 仅用于合约测试和类型生成。当前机器已准备 PostgreSQL 18.3，默认位置 `%LOCALAPPDATA%\WeAgent\postgres-18.3\pgsql\bin`。其他机器安装 [官方 Windows 发行版 / binaries](https://www.postgresql.org/download/windows/)，再设置 `POSTGRES_BIN` 为包含 `pg_ctl.exe`、`initdb.exe`、`psql.exe` 的目录；脚本不会自动下载软件或安装系统服务。

从 `WeAgent-Backend` 目录运行：

```powershell
# 仅在 PostgreSQL 不在默认路径时设置：
# $env:POSTGRES_BIN = 'C:\path\to\pgsql\bin'
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/dev.ps1 -Port 18080 -WithFakeNode
```

- 专用集群监听 `127.0.0.1:55432`，开发库为 `weagent_dev`；不会迁移现有业务数据库。
- Gateway 监听 `http://127.0.0.1:18080`。8080 在当前机器已有其他监听者，示例使用 18080，不停止其他服务。
- 随机数据库密码、持久 HMAC key、Node 私钥 / journal / spool、构建和日志分别放在已忽略的 `.runtime/` / `bin/`，不得上传这些目录。
- 保持此终端运行。Ctrl+C 后按记录的 PID 停止开发子进程，用指定数据目录的 `pg_ctl` 停止专用数据库；不会清空数据。开发脚本的 Stop-Process 不是生产优雅停机验证。
- 不要同时运行 `dev.ps1` 和 `test.ps1`：它们共享同一个专用数据库进程，测试结束会停止它。
- 不带 `-WithFakeNode` 只启动 Gateway。可另开终端手动启动 Node：

```powershell
go run ./cmd/fake-node -gateway http://127.0.0.1:18080 -state-dir .runtime/fake-node
```

## 2. 不依赖前端的 API 联调

开发认证只在显式 `AUTH_MODE=development` 且监听字面量回环地址时允许。`dev:owner` 是本地合成身份，**不是微信授权码**；正式模式不会自动回退到它。以下命令在第二个 PowerShell 终端运行，变量中的 token 不要复制到日志 / 仓库。

```powershell
$base = 'http://127.0.0.1:18080'
$login = Invoke-RestMethod -Method Post -Uri "$base/v1/auth/wechat" `
  -ContentType 'application/json' -Body '{"code":"dev:owner"}'
$headers = @{ Authorization = "Bearer $($login.accessToken)" }

# 从 Fake Node 的终端，或 .runtime/fake-node.stdout.log，读取 12 位配对码。
$code = Read-Host '输入 Fake Node 配对码'
$preview = Invoke-RestMethod -Method Post -Uri "$base/v1/pairings/preview" `
  -Headers $headers -ContentType 'application/json' -Body (@{code=$code} | ConvertTo-Json)
$preview | Select-Object nodeName, platform, keyFingerprint, expiresAt
# 与 Node 本地指纹核对后执行确认；每个新操作生成新 key，网络重试则保留原 key。
$headers['Idempotency-Key'] = [Guid]::NewGuid().ToString('N')
$paired = Invoke-RestMethod -Method Post -Uri "$base/v1/pairings/confirm" `
  -Headers $headers -ContentType 'application/json' -Body (@{ticketId=$preview.ticketId} | ConvertTo-Json)
```

Node 完成轮询确认、签名证明和目录同步后创建会话：

```powershell
$nodeId = $paired.result.nodeId
$device = Invoke-RestMethod -Uri "$base/v1/nodes/$nodeId" -Headers $headers
if (!$device.online) { throw 'Node 尚未就绪，稍后重新查询' }
$projects = Invoke-RestMethod -Uri "$base/v1/projects?nodeId=$nodeId" -Headers $headers
$agents = Invoke-RestMethod -Uri "$base/v1/agents?nodeId=$nodeId" -Headers $headers
$agent = $agents.items[0]
$inputBody = @{
  nodeId=$nodeId; projectId=$projects.items[0].id; agentId=$agent.id
  prompt='Check Gateway integration'; capabilityRevision=$agent.capabilityRevision
} | ConvertTo-Json
$headers['Idempotency-Key'] = [Guid]::NewGuid().ToString('N')
$operation = Invoke-RestMethod -Method Post -Uri "$base/v1/sessions" `
  -Headers $headers -ContentType 'application/json' -Body $inputBody
# 202 只是 accepted。保留 $operation.id，查询最终原生接收回执。
$receipt = Invoke-RestMethod -Uri "$base/v1/operations/$($operation.id)" -Headers $headers
$receipt | Select-Object id, state, result
# confirmed 仍不代表轮次完成；继续查询 Session.state / 历史。
$sessionId = $receipt.result.sessionId
if ($sessionId) {
  Invoke-RestMethod -Uri "$base/v1/sessions/$sessionId" -Headers $headers
  Invoke-RestMethod -Uri "$base/v1/sessions/$sessionId/events?after=0" -Headers $headers
}
```

Fake Node 普通输入生成明确标注模拟的最终消息；`[hold]` 保持运行、`[question]` 产生单选加文本问题、`[approval]` 产生审批，可用于 send / queue / cancel / respond 联调。所有请求字段、状态与 URL 以 [OpenAPI](../contracts/http.openapi.json) 为准。Fake Node 不是真实 Agent，也不提供 shell 后门。

## 3. 使用已有数据库 / 正式认证

Go 二进制只读取进程环境，**不会自动加载 `.env`**。从 [.env.example](../.env.example) 取变量名，通过终端环境或密钥管理注入：

- `DATABASE_URL`：指向专用 PostgreSQL 18 数据库；生产使用受保护网络与适当 TLS，不输出连接串。
- `GATEWAY_HMAC_KEY`：至少 32 个随机字节的标准 Base64，持久保存。它同时用于身份摘要和签名游标；不能每次启动随机重置，否则原身份映射失效。第一版尚无在线 key rotation，变更须做专门迁移。
- `AUTH_MODE=wechat`、`WECHAT_APP_ID`、`WECHAT_APP_SECRET`：实现调用微信 `jscode2session`，保存最小身份摘要，不把 session_key 返回客户端。已用本地 HTTP provider fixture 验证字段，尚未使用真实小程序凭据调用。
- `LISTEN_ADDR`：本地默认 `127.0.0.1:8080`。正式服务置于 HTTPS/WSS 反向代理后，不把 development 模式转发到公网。
- `ALLOWED_ORIGINS`：逗号分隔的完整 Origin；非空 Origin 必须逐字匹配，不能用通配符。无 Origin 的原生小程序 / Node 仍必须发送正确 Bearer token。

设置好环境后：

```powershell
go build -trimpath -o bin/gateway.exe ./cmd/gateway
go build -trimpath -o bin/migrate.exe ./cmd/migrate
.\bin\migrate.exe up
.\bin\migrate.exe status
.\bin\gateway.exe
```

Linux 对应 `go build -o bin/gateway ./cmd/gateway` 和 `bin/migrate up`。迁移显式执行，Gateway 不会自动改表。生产应分离迁移 DDL 角色和应用 DML 角色；`compose.yaml` 的简化起步配置尚未完成角色拆分。

数据库独占 advisory lock 阻止同库启动第二个 Gateway；失去此连接后停止业务和 WS，readiness 返回 503，需要重启服务。数据库不可用不 ACK Node 的未提交事件。SIGINT / SIGTERM 会禁止新控制、关闭连接并限时关闭 HTTP；未知 outbox 保留供重连只查询、不重发。

## 4. 容器配置

`Dockerfile` 构建静态 Gateway / migrate，运行镜像为非 root scratch。`compose.yaml` 提供 PostgreSQL、一次性 migrate 和 Gateway；PostgreSQL 18 持久卷挂载 `/var/lib/postgresql`，遵循 [官方镜像说明](https://hub.docker.com/_/postgres)。

把 `.env.example` 复制为私有 `.env` 并填写实际值：Compose 内部 `DATABASE_URL` 的 hostname 使用 `db`，密码应与 `POSTGRES_PASSWORD` 一致并做 URL 编码。

```powershell
docker compose config --quiet
docker compose up --build -d
```

仅将 Gateway 映射到主机回环 8080，TLS / 合法域名由外部代理配置。本机 Docker daemon 未运行，因此只验证了 Compose 配置解析和 Linux 静态交叉编译，**没有实际构建或启动容器**。

## 5. 保留期、恢复与限制

| 项目 | 当前行为 |
| --- | --- |
| 正文 / Diff | 当前轮结束满 30 天且无未决操作、无未消费队列后清理；返回 `HISTORY_PURGED` |
| 已清理会话 | 不再接收新正文或继续输入，需创建新 Session；旧 source receipt 在保留期内仍可去重 ACK |
| 元数据 / 幂等墓碑 | 完全解决且正文已清理的 Session 在结束满 90 天后移除；未知操作不会随意清除 |
| operation payload | 终态后 24 小时清除；未消费队列、尚未开始的已确认 send 预留保留到完成交接 |
| user_changes | 7 天；过期返回 410，客户端重新 bootstrap |
| checkpoint | 完整不可变快照，10 分钟到期；最大 10000 项 / 32 MiB |
| 清理频率 | 默认每分钟，分批；TTL 是具备清理资格的时间，不承诺跨批次瞬间删完 |

客户端接到 Session `historyState=purged` 应清除该会话正文 / 请求 / Diff 缓存；Session `remove` 应级联清除相关通知和索引。只存 cursor 不存 body 不能保证恢复，必须按 [前端契约](frontend-contract.md) 实现 watch-ready 屏障、HTTP 固定 highWater 补偿和 checkpoint 安装。

单实例全局 sequencer 覆盖提交、订阅及有界写入，优先保证撤销 / epoch 切换不串写；慢写会短暂影响其他连接。每连接上限 256 帧 / 2 MiB，超额直接断开，客户端按历史恢复，不保证收到友好的关闭码。未做吞吐 / 内存峰值压测，不按配置上限宣称生产容量。

HTTP body 64 KiB，WS 消息 256 KiB，HTTP 读写 20 秒，WS 单次写 5 秒；默认总连接 256、HTTP in-flight 128、每用户 client WS 3、每次最多 watch 20 个会话。应用限流基于直连 RemoteAddr，**不信任 X-Forwarded-For**；生产反向代理需配置自己的真实源 IP 限流，否则其下用户共享 IP 额度。

数据库备份 / restore、备份清理延迟、存储加密、真实 Agent 适配和小程序 LiveGateway 不由本次本地回归替代；正式上线前仍需验证。

## 6. 回归与故障诊断

```powershell
npm ci --ignore-scripts
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1 -Race
go vet ./...
node scripts/generate-contracts.cjs --check
# 只测二进制启动，并在检查后停止专用服务：
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/dev.ps1 -Port 18080 -SmokeSeconds 2
```

Go 测试使用 `TEST_DATABASE_URL` 管理连接，每个测试创建、迁移并最终删除自己随机命名的 `weagent_test_*` 库；不会在连接指定的 admin 库里建业务表。外部测试环境需允许建库，禁止将生产连接用于这些 fixture。未设置该变量时集成测试会明确 skip；不能把这种 `go test` 结果当成数据库集成通过。Windows race 检测需要 C 编译器，本机使用 `C:\w64devkit\bin\gcc.exe`。

启动失败时检查专用 `.runtime/gateway.stderr.log` 和 `.runtime/postgres.log`，不要共享未经脱敏的日志。端口冲突选择其他端口，不按进程名批量结束；不要删除运行中的数据库目录。保留期与迁移回滚均在独立 fixture 内测试，禁止在生产执行初始迁移 Down。
