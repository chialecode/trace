# 当前交付状态

更新：2026-10-07。当前唯一计划仍为 [TRACE-CLOSEOUT-02 r1](stage-plan.md)，B 实施证据与 A→B 集中反馈在[收尾报告](stage-report.md)。

**B 实现交付已完成；A 技术复审 passed；产品验收 not-run。** A 核对 B `33cfa4cb05a947691a94c4bc78f8e3120cb67e44`（父提交 `f03e307d4ca53d0f7682e1d8929d2cd4615be493`），逐项定向复验通过；另修复全流程夹具的随机故障目标，最终 verify 5/5、严格 clippy、fmt 通过。新包 -4 的 113 个原输入、全文件摘要和独立合成 daemon 启停通过，见[报告 §11](stage-report.md#a-closeout-rereview)及[A→B 反馈](stage-report.md#a-to-b-rereview-prompt)。用户 DEC-017/018 将本批历史归并为 Git M0，后续阶段单提交及授权/PR/审核规则见[Git 正本](../dev-rules/repo-and-environment.md#git-delivery)。

四项 F 的关闭及 A 新增 F-017 夹具修复由[原审查](../evidence/2026-10-01-m0-m1a-review.md#closeout-failures)维护；F-002/P0、F-003～006、F-009～014 已由 A 核实关闭，状态正本仍为[原审查](../evidence/2026-10-01-m0-m1a-review.md)。A 上轮 failed、原 B 自检及本轮首次失败均保留，不将 M0/M1a 标 accepted。下表仍是实现范围摘要，当前复审差异以报告 §11 为准。

| 范围 | 当前实现 | 验证边界 |
| --- | --- | --- |
| 现场与工程 | 从指定 A 完整提交建立 codex/archive-closeout；保留 F-001 预构建和旧 B §12；锁文件沿用 | Git M0 归并与远端保护见报告 §12；未发布软件或启动子 Agent |
| 恢复与数据 | 严格 blob/path/摘要/数据库关系校验，恢复运行时布局、新 Profile、凭证隔离、缺失副本真实 | 恶意 manifest、损坏/遗漏和 Core::open 恢复自动用例 |
| 任务/物化 | 到期待重试/存储任务、有限重试、唯一租约/代次；整组成功、取消迟到拒绝、各物化窗口重入 | PREPARED/OBJECT_READY/首资源提交后重开、暂停/退出/取消；历史失败及复验见报告 |
| 媒体/网络 | 大媒体流式落盘，强 ETag Range 校验；逐跳 DNS/IP/主机校验；durl 独立段、音频证据、质量 unknown | 33 MiB 超 30 秒慢源、Range 摘要/异常、重定向拒绝；不据此承诺真实规模性能 |
| IPC/浏览 | 帧解码可取消重入，重连快照+当前序号；blob 租约/受控外部打开，512 KiB Range/CSP，详情媒体组件 | 大帧事件并发、历史不重放、错 ID/负偏移/超限拒绝、缺失/编码/断连反馈 |
| 真实主页 | 创作者/账号/订阅、来源/封面、统一组合查询与全局/筛选统计；原管理入口保留 | 预览不写档案；组件、65 条分页、三种宽度长中文截图；原生 IME/缩放 not-run |
| 备份/导出 | 真实默认备份目标/历史；持久任务能查询终态和错误，恢复页显示 Profile 和缺失数 | 合成闭环含目标不可写反例；真实档案未使用 |
| 媒体后端/包 | BtbN 月末 LGPL3 shared 资产锁与校验脚本；异步 probe/mux，doctor、许可/源码信息随本地便携包 | 实际包/哈希与门禁结果见报告；Q-004 未关闭 |
| 站点与阶段 | Bilibili 固定夹具/合成闭环实现；Douyin 生产、直播和远程仍 planned，后续正式阶段按 M1、M2… 编号 | 真实站点 not-run；A 技术复审、ACT-002/003 与产品验收分开 |

最终门禁、产物版本关系和本次失败复验见[收尾报告 §11](stage-report.md#a-closeout-rereview)，原交付及 A 失败记录保留在 §5～6/§9。用户动作只维护 [USER-ACTIONS](../../USER-ACTIONS.md)，开发/上游事项见[总表](../decisions/open-questions.md)。本次不存在需要用户先决定才能修复代码或运行合成测试的阻塞。

## 历史依据

- [首轮规划](m0-m1a-execution-plan.md)、[2026-09-26 B 交付](../evidence/2026-09-26-m0-m1a-delivery.md)：保留 P/C 定义、当时的实现/证据和 §12 修复。
- [2026-10-01 A 审查](../evidence/2026-10-01-m0-m1a-review.md)：原始失败和 F 状态正本。
- [2026-10-07 A 文档与蓝白主页](../evidence/2026-10-07-documentation-and-home.md)：本轮基点来源；当时 Rust/原生包 not-run 的记录不改写。
