# PostgreSQL 数据模型

`00002_native_control.sql` 为原生控制扩展 operations / audit 的约束；已有 native 记录时 Down 会拒绝并回滚，不能删除回执以绕过降级约束。旧环境需先执行新版迁移再启动新版 Gateway / Node。

`migrations/00001_initial.sql` 是实际运行的初始迁移，使用 goose Up / Down 格式，并嵌入 `cmd/migrate`。JS 验证使用全新内存 PGlite；Go 集成测试使用独立原生 PostgreSQL 18 的随机命名可丢弃数据库，不访问已有业务表。

| 聚合 | 表 |
| --- | --- |
| 身份 / 归属 | users、user_tokens、nodes、node_access、node_tokens |
| 配对 / 密钥证明 | node_enrollments、pairing_tickets、node_challenges |
| 目录 / 运行索引 | projects、agents、sessions、turns |
| 幂等与控制 | operations、command_outbox、queue_items、interaction_requests |
| 事件 / 历史 | session_events、node_event_receipts、timeline_items、checkpoints、checkpoint_items、diff_files |
| 用户变化 / 待办 | notifications、user_changes、audit_entries |

## 已落入 DDL 的关键约束

- 项目与 Agent 必须属于 Session 同一个 Node（复合外键）。Session owner 必须存在 NodeAccess。
- current_turn_id 必须属于当前 Session（延迟外键），一个 Session 最多一个 active Turn（部分唯一索引）。
- 请求的 turn/session/user、通知的 session/user、操作的 session/user/node 不允许串资源。
- 同 user + operationId 只有一条操作；outbox 的 nodeId 必须与操作一致。
- 同 session 的 sequence 唯一，source ID / sourceSequence 去重；独立 receipt 不随事件压缩丢失。
- Queue position 与 operation 唯一；reserved nextTurnId 尚未开始时不是 active Turn。
- Timeline item key 包含 turnId，不合并不同轮次同名 item。
- 状态枚举、基础数值范围、公钥 / 摘要长度、request 决策预占、到期先后关系有 CHECK。

## 必须由服务事务保证、DDL 不能替代的约束

1. 已撤销 NodeAccess 仍留历史行，FK 存在不等于仍有权限；每次查询 / 写入 / 广播要检查 revoked_at。
2. sequence 无缺口靠行锁 counter + 同事务写，不是 UNIQUE 自动保证。
3. 状态是否能从 A 到 B、请求 options / required、能力和当前轮次、deadline、Node epoch 需要用例验证。
4. JSONB 内容先通过 canonical Schema，不能把任意原生 payload 落入公共投影。
5. operation.request_hash 相同判断与保留期限、source 摘要比较、Node journal 不重执行由服务保证。
6. Diff snapshot 引用必须校验同 session；display_path 永远不能用于服务器文件系统 IO。
7. checkpoints 的 item_count、sequence 和复制内容必须来自一个一致性快照。

## 迁移与回滚

设置进程环境 `DATABASE_URL` 后，使用项目内嵌 goose 的迁移入口，不把密码放进命令行参数：

```sh
go run ./cmd/migrate up
go run ./cmd/migrate status
# 仅空 fixture / 可丢弃测试副本：初始迁移 Down 会删除以上业务表和内容。
go run ./cmd/migrate -allow-destructive-down down
```

不要对生产执行本初始 Down 来“恢复数据”。上线前备份并验证 restore；真实增量版本采用 expand / contract。已在原生 PostgreSQL 18.3 的 fixture 内完成 goose Up / Down / Up，验证无关表保留；实际 Gateway 使用多连接 pgx 池和用户行锁。数据库独占 advisory lock 用于单实例门禁，不替代事务。生产并发负载、锁争用上限、备份和灾备恢复尚未验收。

运行 `scripts/test.ps1 -Race` 自动管理专用 PostgreSQL 进程；测试只删除自己随机创建且前缀校验通过的 `weagent_test_*` 库。正文 / 元数据清理位于 `internal/gateway/maintenance.go`，已有原生 fixture 测试覆盖 30 / 90 天、未决操作和队列保留，不需要也不应在现有用户数据库中手动演练 Down。
