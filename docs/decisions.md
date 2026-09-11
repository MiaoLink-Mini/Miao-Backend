# 设计决策记录

## 来源优先级

1. 当前请求：以现有 WeAgent-Frontend 为主设计后端。
2. 前端真实代码、调用点、fixture 和测试（基线哈希见 contracts/source-baseline.json）。
3. 上一级 frontend-design.md 与 frontend-function-inventory.md。
4. 上一级 backend-design.md：约束参考，不是覆盖当前产品行为的最终协议。

辅助 backend-design.md 只有 32 节；frontend-design.md 第 46 节提到后端第 38 节，属于失效交叉引用。此次不虚构缺失章节。

| 决策 | 与参考 / 演示的差异 | 原因与取舍 |
| --- | --- | --- |
| ADR-01 请求统一为 InteractionRequest | 从单 Approval 扩展为 approval + question | 现有 request 页已支持单选 / 多选 / 文本、多问题和独立回答 |
| ADR-02 单独 Operation + Turn | 不把 accepted 当作任务完成 | 原生确认、取消、超时对账、同会话继续必须区分 |
| ADR-03 queue 纳入 v1，steer 不纳入 | 只有 explicit Node-local queue | 已有发送模式与 /queue；不实现隐式断网后执行 |
| ADR-04 failed / cancelling / closed / idle | 不照搬参考 error 与粗略状态 | 对齐现有 policy，增加真实 create 就绪窗口；连接状态独立 |
| ADR-05 全部已推送事件 durable | 不做 ephemeral token 预览 | 简化 cursor 不丢历史证明；Node 合并 delta 换取少量延迟 |
| ADR-06 双游标 + checkpoint | 不用最后 seq 直接冷启动 | 原前端内存模式无法覆盖保留期、分页、正文缓存丢失 |
| ADR-07 agent 与会话能力分层 | 不按 Claude/Codex/Pi 品牌硬编码 | 同 Agent 不同会话模式 / 版本可能不同，点击必须重校验 |
| ADR-08 标题搜索 / 筛选进 v1 | 不把所有搜索推到二期 | sessions 页已提供搜索；仅元数据过滤，成本可控 |
| ADR-09 文件接口只接受快照 ID | 不提供工作区任意路径读取 | 保持 Gateway 不碰主机目录；patch 不能冒充完整文件 |
| ADR-10 固定标准库 HTTP、pgx、goose、coder WS | 不同时提供多套技术选项 | 明确实现路线，单实例 PostgreSQL 足够当前规模 |
| ADR-11 分离设计 / Gateway / 前端生产接入 | D0 先冻结合约，当前新增真实 Go Gateway 与独立 fixture；LiveGateway 继续明确未配置 | 避免把协议回归或 demo 冒充微信 / 原生 Agent 生产验收 |
| ADR-12 单 owner / incarnation | 撤销后新公钥 + 新 Node | 牺牲同 ID 重绑定便利，避免旧凭据恢复归属和历史越权 |
| ADR-13 user revision 串行分配 | 每 user 行锁分配变化号 | 第一版单用户个人控制量可接受；后续热点优化必须保留连续语义 |
| ADR-14 接受 journal 不可消除的崩溃未知窗口 | 不承诺 exactly-once | 原生动作和本地 journal 无分布式事务；unknown 优于重复执行 |
| ADR-15 保守的单实例 sequencer | 全局锁覆盖提交 / 订阅 / 有界写，数据库 advisory lock 拒绝第二实例 | 首版保证撤销和 epoch 顺序；代价是慢写会影响其他用户，须后续压测 / 拆锁 |
| ADR-16 保留期以当前 Turn 结束时间为准 | 清理后不续写 / 上传正文，90 天后退休已解决聚合 | 防止 metadata 更新延长正文留存和 Node 重放重新灌入已删内容 |
| ADR-17 保留尚未交接的已确认命令 payload | 队列消费前 / send 新 Turn 上报前不执行终态 24 小时 payload 清理 | native accepted 不是业务完成，不能删掉后续状态上报所需的预留 ID |
| ADR-18 立即断开背压 / 撤销连接 | 不保证 1013 或错误帧到达；未 ACK spool 重放去重 | 避免在 fence 内额外等待关闭握手，以已有 durable cursor 作为恢复依据 |

JSON Schema 是数据类型唯一来源；OpenAPI 只引用。数据库负责结构与关联约束；动态 capability、question option membership、权限 / expiry / 状态迁移由用例事务实现，不能错误地以 Schema 替代。
