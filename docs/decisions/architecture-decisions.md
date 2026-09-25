# 架构与治理决策台账

ADR-001～012 从原 `docs/design.md` 第 19.1 节迁入，编号不变。这里是决定及理由正本；模型、协议正文仍在[技术设计](../design.md)。`accepted-design` 表示继承的设计选择，不表示实测或新的用户签字。

| 编号 | 决策 | 原因与代价 | 状态与验证边界 |
| --- | --- | --- | --- |
| ADR-001 | Tauri 外壳与独立 Rust 后台分离 | GUI 可释放、核心可无界面部署；需本机通信与生命周期验证 | accepted-design；M0/M1a 验证 |
| ADR-002 | 身份、发布、版本、媒体、文件分别建模 | 保留来源并共享文件；查询关联需明确 | accepted-design；按阶段验证 |
| ADR-003 | SQLite + 不可变对象 + 提交日志 | 本地简单、跨文件可恢复；需恢复/回收协议 | accepted-design；M0 故障注入 |
| ADR-004 | 独立适配器与版本化消息契约 | 独立更新、无 GUI 复用及故障隔离；有进程成本 | accepted-design；0.x 原型，非已实现沙箱 |
| ADR-005 | 可比较性与保留引用决定质量 | 避免误删独有信息；保留理由与平局处理 | accepted-design；M1c 完整质量能力 |
| ADR-006 | 凭证与持久档案类型隔离 | 限制泄露；恢复可能需重新登录 | accepted-design；从首次凭证接入即验证 |
| ADR-007 | 任务至少一次、业务效果幂等 | 应对中断/重复；需唯一键、代次、重入 | accepted-design；M0/M1a |
| ADR-008 | 本地 FTS5 与短词候选索引 | 无外部搜索服务、中文支持；成本和性能待测 | accepted-design，2026-09-26 选为首轮路线；OSS-003 保留集成/性能门槛，非已实测 |
| ADR-009 | 通用媒体使用可替换后端 | 限制自行实现媒体格式；需打包/许可验证 | accepted-design，2026-09-26 采用外部 FFmpeg/ffprobe；实际资产/许可仍待 OSS-004 核验 |
| ADR-010 | 节点独立数据库，同步操作与文件 | 离线/网盘可用；需确认/删除协议 | accepted-design，M3 设计范围；未实现 |
| ADR-011 | 远程首版单配置权威、显式交接 | 降低冲突；非权威离线编辑需等待 | accepted-design，M3 范围 |
| ADR-012 | 原始档案与派生预览分开 | 原件稳定、缓存可回收；需容量策略 | accepted-design；首次预览接入即验证 |
| ADR-013 | 文档分层、唯一正本、登记与验证 | 避免 Agent 上下文漂移；增加必要同步成本 | accepted；DEC-001，2026-09-25 |
| ADR-014 | 通用技术开源优先，站点规则独立 | 复用成熟能力、关注 TRACE 整合；需维护/许可/场景证据 | accepted；DEC-002/008，2026-09-25 |
| ADR-015 | 后续 A 规划审核、B 实现，MVP 迭代 | 减少开发来回打断；仍需明确交接和真实证据 | accepted；DEC-003/004/006，2026-09-25 |
| ADR-016 | Vue/Naive UI/Vite、Tokio/rusqlite、Windows DPAPI/named pipe、Vitest/WDIO 为首轮栈 | [具体比较/许可/版本/替换条件](../research/2026-09-26-technology-stack.md)；TS7 不兼容，TS6 最小试验通过；其余 B 集成实测 | accepted-design，Agent A 授权内技术选择，2026-09-26；不是整个栈已兼容的结论 |
| ADR-017 | M1a 先 Bilibili 普通投稿视频，Douyin 生产接入 M1b | 当前可读 Rust 参考链路较完整；抖音官方作者授权不覆盖任意账号/媒体需求，社区路线需单独验证；[两站入口](../delivery/m0-m1a-preparation.md) | accepted-design，Agent A 按 DEC-005/008 选择技术顺序，2026-09-26；真实现场 not-run，Q-001 关闭选择部分 |
| ADR-018 | 长批次计划、仓库交接合同、双方必交 prompt | 减少逐包往返；一份计划/报告和稳定问题编号仍保留真实阻塞与版本证据；[机制](../dev-rules/development-workflow.md#handoff-protocol) | accepted，DEC-010/012/013，2026-09-26；不自动授权子 Agent 或外部消息 |

首轮选择与实测状态分别记录；实际依赖锁和生产能力由 B 交付证实。常规实现选择由技术维护者在授权范围内按证据完成；改变产品语义、分发许可或既有方向才集中请用户处理。重大替代新增 ADR 并标明被替代者，不覆盖旧理由。
