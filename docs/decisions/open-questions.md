# 统一待决定与待处理总表

本文件是开发/上游/后续事项正本；用户决定、配置、授权和人工检查的当前状态在 [USER-ACTIONS](../../USER-ACTIONS.md)，本文只链接，历史记录保留当时结论。更新日期：2026-10-07（A 集中复审通过，历史失败保留；历史[复核报告](../evidence/2026-10-01-m0-m1a-review.md)保留）。已确认的平台、Vue、首版范围、长计划和沟通机制见[有效决定](confirmed-decisions.md)，不重复待批；本轮实施/审查证据见[收尾报告](../delivery/stage-report.md)。

## 使用与关闭

Q = 产品/设计取舍；OSS = 开源选型、集成、许可或上游问题；ACT = 必需人工/环境/外部操作；LOOP = 可延期优化。状态为 `open/in-progress/waiting-human/resolved/deferred`。每项写推荐、依据、影响、负责人、最迟处理点、具体受阻动作和关闭动作；无必要人工动作时由开发者继续完成。

开发缺口不得转为用户验收。只有不影响当前基本使用、数据/来源/凭证/恢复安全和必须门禁的优化才可 deferred，且要有后继轮次/触发条件和复验入口。上游缺陷不能用“等维护者回复”掩盖当前产品失败。

## 需求与设计取舍

| ID / 状态 | 事项、推荐与依据 | 负责人 / 最迟处理点 | 阻塞范围与关闭动作 |
| --- | --- | --- | --- |
| Q-001 / resolved | 2026-09-26 A 完成两站资料/源码能力表，决定 Bilibili 先进入 M1a，Douyin 生产接入 M1b；依据 [ADR-017](architecture-decisions.md) | A 已完成顺序选择，B 实现 | 只关闭技术顺序问题；真实可用性仍依赖 OSS-007/008、ACT-002，不声称已验收 |
| Q-002 / 用户事项 | 使用规模、历史回填和质量偏好，见 [USER-ACTIONS](../../USER-ACTIONS.md#q-002) | 用户待办维护负责人/时机 | 当前状态与关闭条件仅在用户待办维护 |
| Q-003 / resolved | 用户 2026-10-07 指定白色参考图，采用蓝白三栏 DEC-015 | A 已实现原型，后续体验 ACT-004 | 关闭风格待定；主观体验未验收，见[设计](../design-rules/DESIGN.md) |
| Q-004 / 用户事项 | 许可证与分发，见 [USER-ACTIONS](../../USER-ACTIONS.md#q-004) | 用户待办维护负责人/时机 | 不阻塞本地开发；受影响动作见用户待办 |

## 开源集中处理

A 已完成[本轮具体选型](../research/2026-09-26-technology-stack.md)，B 按该选型完成工程集成并回写实测版本。以下分别注明选择、待集成或已观察问题；不能把所有待测项都叫上游缺陷。旧[候选池](../research/open-source-candidates.md)保留历史日期，不再作为未选 Vue 的当前依据。

| ID / 状态 | 事项、推荐与证据 | 负责人 / 最迟处理点 | 阻塞范围与关闭动作 |
| --- | --- | --- | --- |
| OSS-001 / resolved | Vue 用户已定，A 选 Naive UI/Vite/Pinia/Router；B 已建真实工程（Vue 3.5.43+TS 6.0.3+naive-ui 2.45.3+vite 8.3.1），锁文件与 `--frozen-lockfile` 生效，类型检查/Vitest 12 项/生产构建通过 | B 2026-09-26 完成集成；A 复核 [C14/24/33](../evidence/2026-09-26-m0-m1a-delivery.md#c-用例) | 框架集成问题已关闭；资源体积目标仍不作承诺，窗口缩放/IME 实机体验归 ACT-003 |
| OSS-002 / in-progress | Tauri 2.11.6 外壳已建：daemon 独立进程、命名管道+受限 DACL、DPAPI vault、`trace-media` 受控 Range 桥均通过进程级用例 | A 复核 [C09～C13/22/32](../evidence/2026-09-26-m0-m1a-delivery.md#c-用例) / A 轮复核 | 2026-10-01 A 复核：IPC 会话读取非取消安全（F-013）、媒体桥无界读取与 `open_external` 任意路径（F-010）、重定向逐跳校验缺失（F-011），见[复核报告](../evidence/2026-10-01-m0-m1a-review.md)；修复后再交实机（ACT-003）。未推送，无签名/安装包 |
| OSS-003 / resolved | Tokio 1.53.1 + rusqlite 0.40.2（SQLite 3.53.2、FTS5）、migrations、短事务、取消/恢复、租约回收与 FTS5 trigram 中文检索已在自动测试通过 | B 2026-09-26 完成；A 复核 C01～08/17～23/33 | 本轮实现问题关闭；大库性能/资源占用实测在 M1b/c 按真实规模测量后评估 |
| OSS-004 / in-progress | 2026-10-01 A 补查（用户同意写入）：采用 BtbN 9.0 发布分支 `win64-lgpl-shared` **月末**构建，首选 `autobuild-2026-09-30-13-08`（SHA-256 `7157177b…2b880646`），回退 `autobuild-2026-08-31-13-27`；项目锁文件 + 校验下载脚本，daemon 显式路径查找，PATH 仅显式允许，ffmpeg/ffprobe 同版本并由 doctor 报告来源/版本/许可变体，见 [FFmpeg 集成补查](../research/2026-10-01-ffmpeg-integration.md)。现实现只从 PATH 发现、未校验版本（旧机 winget GPL 9.0.1，新机未安装），属审查 F-012 | B 实施获取/查找/诊断/打包与固定样本（F-012）、A 复核资产许可、用户经 Q-004 决定是否随包分发 / 获取与查找在 M0/M1a 返工内；随包分发在公开发布前 | 不阻塞离线闭环；阻塞 C27 的 probe 规格/时长/轨道实测与 durl 合并方案（F-008）。资产内容、`-buildconf` 与第三方库许可尚 not-run，不假称已过 |
| OSS-005 / resolved | B 已建 Vitest+jsdom 12 项、Rust 契约/夹具/进程级用例与 `scripts/verify.py` 五套件；发行包经 doctor 强制不含测试端点与测试二进制 | B 2026-09-26 完成；A 复核 C 用例与打包自检 | 测试基础设施关闭；WDIO/Tauri service 端到端 GUI 自动化未引入，实机 UI 体验归 ACT-003，纯 mock 不替代真实 daemon（本轮闭环用真实 daemon 进程）。2026-10-01 A 在干净环境发现验证入口缺预构建（F-001，A 已修）及闭环断言不足（F-014，交 B） |
| OSS-006 / resolved | 已实测 TS7.0.2 + vue-tsc3.3.11 typecheck 失败，`./lib/tsc` 未导出；固定 TS6.0.3 后类型/构建/SSR 通过，见[试验](../research/2026-09-26-technology-stack.md) | A 2026-09-26 解决本轮选型阻断；B 保留锁定 | 不声称上游已修复；未来升级 TypeScript7 必须先重现同一检查并证实兼容，不用 skip 类型门禁 |
| OSS-007 / in-progress | B 已实现独立 Bilibili 适配器（WBI v1 固定向量、错误分类不盲猜业务码、多P/DASH 候选归一、AVC/AAC 优先），合成夹具与固定向量通过；**真实站点请求/登录/下载 not-run** | B 已交付自动证据、A 复核 [能力表回写](../research/bilibili-capabilities.md#b-实现回写) / ACT-002 具备条件后 B 实测 | 未复制参考实现代码（独立实现，无新增第三方许可义务）；真实接口、风控与播放地址有效期仍待 T 证据；T 之前不宣称站点可用。2026-10-01 A 复核：durl 分段按字节拼接（F-008）、音频非必要导致完整性失真（F-009），先返工再做 T |
| OSS-008 / open | [抖音表](../research/douyin-capabilities.md)区分作者 OAuth 与网页会话；官方列表不证明任意作者/完整媒体；社区 GPL 实现只作资料 | A 刷新并规划、B 实现 / M1b 生产接入前 | M1a 仅异构合成夹具；下一轮明确身份映射/列表/媒体/会话与分发，无需阻塞 Bilibili 或要求用户现在申请应用 |

## 人工、环境和外部操作

| ID / 状态 | 事项与推荐 | 负责人 / 最迟处理点 | 阻塞范围与关闭动作 |
| --- | --- | --- | --- |
| ACT-001 / resolved | 用户已授权并按空 README→文档两次提交建立本地 Git，默认 main，见[记录](../evidence/2026-09-26-m0-m1a-planning.md) | A / 2026-09-26 | 本地基线已建立；远端/CI 不在本次授权动作内，尚未建立，不阻塞 B 本地开发 |
| ACT-002 / 用户事项 | 真实站点受控验证，见 [USER-ACTIONS](../../USER-ACTIONS.md#act-002) | 用户待办维护准备责任/时机 | 未执行边界与操作步骤只在用户待办维护 |
| ACT-003 / 用户事项 | Windows 实机验收，见 [USER-ACTIONS](../../USER-ACTIONS.md#act-003) | 用户待办维护准备责任/时机 | 先由 Agent 补齐缺口，准备好再执行 |

当前无已识别的 LOOP 项；尚未实现的必须需求不能填成可延期优化。未来新增 LOOP 必填影响、可用替代、负责人、后继批次/触发和复测入口。

## 解决记录

事项解决时保留原编号，在原行或新增解决段记录日期、来源、决定/修复、证据与正本更新。涉及人工确认才用 waiting-human，不因为文档写着 open 就停止独立工作。

### 2026-09-26 B 轮解决记录

- OSS-001（Vue 工程集成）：B 建立 `apps/desktop` 真实工程并锁定 TS 6.0.3 组合；证据 `scripts/verify.py --suite desktop`（typecheck + vitest 12 项 + 生产构建）。关闭集成问题，不放宽资源目标。
- OSS-003（Rust 工程与数据层）：B 建立 `crates/core` 领域/迁移/FTS5/对象库/任务租约；证据 `cargo test --workspace --locked`（84 项）中 trace-core 25 项与恢复用例套件。性能实测仍按真实规模在 M1b/c 评估。
- OSS-005（测试基础设施）：B 建立 Vitest/jsdom、Rust 契约/夹具/进程级用例与五套件入口；证据 `scripts/verify.py --all`。WDIO/Tauri GUI 自动化未引入，GUI 体验归 ACT-003。
- OSS-006（TypeScript 版本）：沿用 A 的解决结论，B 只保留锁定，未升级 TS7，未跳过类型门禁。
- 上述解决不构成阶段验收：真实站点（ACT-002）、实机 UI/安装（ACT-003）与 A 集中复核仍未完成。

### 2026-10-01 A 集中复核记录

- 依据用户 2026-10-01 指示"写进 OSS-004 并开始 A 复核"。复核结论：技术审查不通过，交付 rework-required；F-001～F-014 详见[复核报告](../evidence/2026-10-01-m0-m1a-review.md)，F 编号只在该报告维护状态。
- OSS-004 更新为具体资产与集成路线，状态仍为 open，实施归 F-012。
- 本次没有关闭任何事项，也没有新增 Q/LOOP：全部发现都是本轮必须项。

### 2026-10-07 文档与界面更新

- Q-003 按 DEC-015 关闭风格待定；可选体验为 [ACT-004](../../USER-ACTIONS.md#act-004)。
- 用户事项当前状态迁至 USER-ACTIONS，保留编号与历史证据。F-002～F-014 未修复，按当前计划处理。

### 2026-10-07 B 收尾回写

- 本轮执行 TRACE-CLOSEOUT-02，R0～R6/U01～U06 与 F-002～F-014 定向证据见[收尾报告](../delivery/stage-report.md)；原审查表保留当时触发/失败，仅由 A 在复核后关闭 F。
- OSS-002：有界媒体桥/受控 blob 打开、逐跳网络和可取消 IPC 帧已实施，A 复核及原生产品体验仍待进行；不把自动组件测试当 Windows 体验验收。
- OSS-004：锁文件、SHA 下载校验、显式查找、异步调用、固定 AVC/AAC/FLV、doctor 与本地包完成实施；实际 -L 为 LGPL-3.0-or-later。此前“仅 PATH/资产 not-run”是 A 当时结论，最新事实见[FFmpeg 研究回写](../research/2026-10-01-ffmpeg-integration.md#6-b-本地集成实测2026-10-07)。状态 in-progress：待 A 核实实现与许可，正式分发仅链接 Q-004，不把本地包视为发布。
- OSS-005：保留 F-001 预构建；加强整组终态、实际恢复 Profile、媒体摘要、请求/错误哨兵、70 条副本游标、IPC 并发、订阅交互及合成验收准备器。未引入 GUI 端到端新框架。
- OSS-007：durl 独立有序段、音频证据/必要性、封面、未知质量与来源字段已接线；真实站点 T 证据仍 not-run，能力表第 6 节为当前实现事实。
- 没有新增需用户承担的开发缺陷或延期 LOOP；真实服务/实机体验步骤和状态仅在 USER-ACTIONS。收尾复审收敛后才进入下一阶段规划。

### 2026-10-07 A 收尾集中复审

- 开发返工由 B 负责：F-007/F-008/F-015/F-016，最迟在本轮技术通过前处理，阻塞 R2/R3/R5/R6 的完整交付。状态/触发/关闭动作只在[原审查 §4/§10](../evidence/2026-10-01-m0-m1a-review.md#closeout-failures)维护；集中 prompt 与全部 R/U/F 证据见[收尾报告 §9](../delivery/stage-report.md#a-closeout-review)。不另开竞争计划或延期 LOOP。
- OSS-002：原受控媒体/逐跳网络/IPC 缺口已核实修复；当前持续浏览缺陷归 F-016，不把它当原生验收待办。OSS-005：A 保留 4 个新失败回归测试，修实现后重新通过门禁。
- OSS-004：A 核实本轮锁定媒体资产、异步集成和本地包，F-012 原实现问题关闭；正式分发边界只链接 [Q-004](../../USER-ACTIONS.md#q-004)，不把本地包或技术核对视为法律批准。
- OSS-007：必要音频/质量 unknown 原问题通过；分段大小完整性归 F-008。真实站点与原生体验当前动作仅在 [ACT-002](../../USER-ACTIONS.md#act-002)、[ACT-003](../../USER-ACTIONS.md#act-003)，未新增用户阻塞。


### 2026-10-07 B 四项集中返工

- F-007/F-008/F-015/F-016 已完成定向自检与最终 verify 5/5，当前状态仅在[原审查](../evidence/2026-10-01-m0-m1a-review.md#closeout-failures)标已修待复核；A 再次复核前不宣称关闭。修复、失败复验、包和交接见[报告 §10](../delivery/stage-report.md#b-closeout-rework)。
- OSS-002 持续媒体浏览与 OSS-007 分段校验已补实际回归；OSS-005 保留 A 4 个反例，另补传输/分段/订阅和事件竞争测试。没有通用依赖升级，没有新增需用户承担的代码缺口或延期 LOOP。
- 站点/原生体验与分发边界仍只链接 USER-ACTIONS；A 已关闭问题不重建，不新建竞争计划。

### 2026-10-07 A 四项返工复审与 Git 收尾

- F-007/F-008/F-015/F-016 已逐项复核；新增 F-017 是全流程夹具故障目标不固定，A 局部修复并保持原断言，状态只在[原审查](../evidence/2026-10-01-m0-m1a-review.md#closeout-failures)维护。实际命令、失败和复验见[报告 §11](../delivery/stage-report.md#a-closeout-rereview)。没有遗留给 B 的本批开发阻断。
- OSS-002/004/005 的本批自动技术范围已核实；OSS-007 的分段正确性通过，真实站点覆盖仍按能力表与 ACT-002，不以合成源替代。用户动作只链接 [USER-ACTIONS](../../USER-ACTIONS.md)。
- DEC-017/018：本批 Git 历史归并为 M0，关联明确指定远端，后续阶段单提交、push/PR/合并分别核对授权；分支保护和治理 CI 按[Git 正本](../dev-rules/repo-and-environment.md#git-delivery)落实。没有扩大生产平台范围、关闭许可或宣布产品验收。
