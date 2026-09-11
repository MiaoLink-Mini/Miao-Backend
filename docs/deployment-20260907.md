# 新服务器后端部署记录

## 当前运行环境

- 服务器：`8.209.221.97`，Debian 12，约 701 MB 内存。
- Gateway：`https://agent.000.moe`，本机监听 `127.0.0.1:18080`。
- WebSocket：`wss://agent.000.moe/v1/ws/client`、`/v1/ws/node`。
- 发布目录：`/opt/weagent/releases/20260907-gateway-01`，`/opt/weagent/current` 指向当前版本。
- Go 1.26.7 静态 Linux amd64 二进制，systemd 服务 `weagent-gateway`，非 root 用户 `weagent`。
- PostgreSQL 18 Alpine 容器 `weagent-db`，镜像固定 digest，持久卷 `weagent-postgres-data`；仅监听 `127.0.0.1:55433`。
- 微信认证模式固定为 `wechat`，私密配置在 `/opt/weagent/private`（目录 700，文件 600）。运行与迁移分别使用数据库账号；运行账号没有超级用户、建库、创建角色或修改迁移版本的权限。
- 新建数据库和 Gateway 签名密钥，没有连接旧服务器，也没有迁移其用户、配对和会话数据。
- 既有 Nginx HTTPS 站点追加 API / WebSocket 路由，官网仍为 `20260907-web-449244c9`。原 Nginx 配置已备份到 `/opt/weagent/backups/nginx-before-backend`。

## 验证结果

- `go test -race -count=1 -timeout=180s ./...`：真实本地 PostgreSQL fixture 测试通过。
- `govulncheck`：No vulnerabilities found。
- 公网 `/healthz`、`/readyz`：200；未认证 `/v1/bootstrap` 和 `dev:owner` 登录：401。
- 微信开发者工具真实 `wx.login` 认证成功，新数据库产生真实认证用户。
- WSS 连接为 online；Gateway 重启后恢复在线，跨心跳周期保持连接。
- 已认证 `/v1/bootstrap`、`/v1/nodes`、`/v1/sessions`、`/v1/subscriptions/config`：均为 200。
- 已确认公网 API `Cache-Control` 为 no-store，当前 EdgeOne 返回 MISS。
- 手工备份成功，并将备份恢复到独立 `weagent_restore_probe` 数据库，迁移版本为 4；验证后删除的仅是该临时恢复库。
- 自动备份 systemd timer 已启用，服务 Result=success、ExecMainStatus=0。
- 官网根页面仍包含版本化资源 `449244c9b45d6def06b8`，未覆盖官网。
- 原完整模拟器截图脚本遇到微信开发者工具文件存储上限；改用不生成截图的认证、接口、实时连接实测。本次不声称截图脚本通过。

## 运维

```sh
systemctl status weagent-gateway --no-pager
journalctl -u weagent-gateway -n 50 --no-pager
curl -fsS http://127.0.0.1:18080/readyz
docker ps --filter name=weagent-db
systemctl start weagent-backup.service
systemctl list-timers weagent-backup.timer
```

备份保存在 `/opt/weagent/backups`，每天 UTC 03:30 起随机延迟最多 10 分钟执行。当前仅有同机备份，应补充离机加密备份及磁盘告警；备份不会自动删除。

内存按小规模使用设置：PostgreSQL 220 MB、shared_buffers=32 MB、max_connections=30；Gateway MemoryHigh=160 MB、MemoryMax=220 MB、GOMEMLIMIT=150 MiB，最多 64 条实时连接、32 个并发请求。这不是 64 活跃任务的容量承诺，未进行生产负载验收。

升级时先备份，再上传至新的发布目录，切换 `current` 并重启 `weagent-gateway`。迁移必须显式执行，不在 Gateway 启动时自动执行。回滚应用只切换兼容的历史二进制；不得对生产库执行 destructive down。当前是新服务器首次后端部署，无上一版后端数据需要保留或回滚。

## 尚未覆盖的边界

- 本次没有在服务器安装 Codex / Pi / Claude Code 的应用层，也没有运行真实付费模型任务。Node 应继续运行在用户授权的工作设备上。
- 未配置审批后的微信订阅消息模板；配置接口 200 不代表订阅通知已开通。
- 微信平台正式版的 request/socket 合法域名及发布审核仍由小程序平台管理；开发者工具实测不替代真机正式版验收。
- Nginx 只将 TCP 对端地址作为可信客户端地址。EdgeOne 后暂按回源节点限流，需站点专属回源 CIDR 后才能安全恢复真实客户端 IP；不会信任任意外部转发头。
- 既有功能缺口见 `docs/FEATURES-72.json`，本次部署不将其改标为完整验收。
- 曾在聊天中提供的 AppSecret / SSH 密码建议随后轮换；未写入发布目录、官网或提交清单。

## 二进制摘要

Gateway SHA-256：`0b6873971b8e4a31dab7364fed169b4c96f11404864445d3cec61b7af4a74ac6`

Migrate SHA-256：`9bd0400c7f1344236a42762c392ea1c15abf566b4c2f9efabfd8815241dbe6ad`
