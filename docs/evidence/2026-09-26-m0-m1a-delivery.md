# M0/M1a 实施交付证据（Agent B）

> 2026-10-01 追加：A 集中复核结论为 rework-required，发现与对本文用例状态的更正见[复核报告](2026-10-01-m0-m1a-review.md)；本文其余内容保持 B 交付时原样。

计划：[TRACE-M0-M1A-01 r1](../delivery/m0-m1a-execution-plan.md)；角色：实现与自检。记录日期 2026-09-26，全部命令在本机（Windows 11 `win32 10.0.26200`，x86_64）实测；本报告保留原始失败与复验结果，**不构成产品验收**。

## 1. 结论摘要

- 计划 P0～P9 的可执行范围已完成：Rust workspace、领域与持久化、对象库恢复、独立 daemon 与控制协议、DPAPI 凭证 vault、插件宿主与 Bilibili 适配器、Vue 3 桌面 UI、中文搜索、备份/导出/恢复、Windows 便携包、验证入口与文档同步。
- 自动证据全绿：`cargo test --workspace --locked` 84 项、`cargo clippy --workspace --locked --all-targets -- -D warnings` 零告警、`cargo fmt --check` 通过、前端 `vue-tsc`+`vitest` 12 项+生产构建通过、`python scripts/verify.py --all` 5 套件通过、便携包构建与发行自检通过。
- 真实站点与实机体验仍为 **not-run**：没有真实账号、真实视频或用户实机操作，[C34](#c-用例)、[C35](#c-用例)、[C33](#c-用例) 未执行；ACT-002、ACT-003 保持 open。M0/M1a 处于 **ready-for-review**，未验收。
- 过程中出现并已修复的真实缺陷 7 类（死锁、ACL、IPC 连接拓扑、适配器 stdio 锁、测试注入、断言错误、严格 lint 门禁），原始症状与复验见[第 6 节](#失败与复验)。
- 未推送、未建 PR、未合并、未发布；未启动子 Agent；凭证未进入仓库、日志、导出或本报告。

## 2. 基点、分支与提交

| 项 | 值 |
| --- | --- |
| 初始化历史 | `b208ad6`（零字节 README）→ `7027add`（文档框架与首轮计划） |
| 本轮基点 | `7027addebe6f8769118837569bba377a4a80e900`（main，第二提交） |
| 实施分支 | `feat/m1a-video-archive` |
| 交付提交 | 本轮一个交付 commit；SHA 用 `git log`/`git rev-parse HEAD` 读取，不在文档内自引用 |
| 远端/CI | 无。未 push、未建 PR、未合并、未发布 |
| 工作区 | 基点后交付提交共 117 个文件（新增 110 / 修改 7），均由本轮实施产生；未 reset/stash 覆盖用户文件。Tauri 生成的 `src-tauri/gen/schemas` 与构建产物不入库，见嵌套 `.gitignore` |

## 3. 实际环境与版本

| 组件 | 实际值 | 固定方式 |
| --- | --- | --- |
| Rust / Cargo | 1.98.1（2026-09-01） | `rust-toolchain.toml`（含 clippy/rustfmt） |
| Node / pnpm | Node 24.19.0；pnpm 12.6.0 | `package.json#packageManager` + corepack |
| TypeScript | 6.0.3 | `apps/desktop/package.json`；沿用 OSS-006（TS7.0.2 与 vue-tsc 3.3.11 不兼容，A 轮已实测失败） |
| Vue / Vite / naive-ui / vue-router / pinia | 3.5.43 / 8.3.1 / 2.45.3 / 4.6.4 / 3.0.9 | `pnpm-lock.yaml` |
| Tauri / tokio / rusqlite / windows | 2.11.6 / 1.53.1 / 0.40.2（SQLite 3.53.2，FTS5 可用）/ 0.62.2 | `Cargo.lock` |
| WebView2 Runtime | 153.0.4234.48（已安装） | `trace-daemon doctor` 实测 |
| ffprobe / ffmpeg | 9.0.1-full_build（本机 winget 安装，**不在包内**） | `trace-daemon doctor`；A 轮资料写 9.0.2，实际本机为 9.0.1，如实记录并留在 OSS-004 |
| Python | 3.11+（仅脚本） | `scripts/*.py`，无第三方依赖 |

`schema_version=2`、`control_protocol=0.1`、`plugin_protocol=0.1` 在包清单与契约测试中固化。

<a id="工程"></a>
## 4. 交付内容与 P0～P9 映射

| 工作包 | 实际实现 | 自动证据 |
| --- | --- | --- |
| P0 工程与依赖 | Cargo workspace（`crates/{domain,contracts,core}`、`apps/{daemon,desktop}`、`adapters/bilibili`、`tests/e2e`）、pnpm workspace（`apps/desktop`）、`rust-toolchain.toml`、双锁文件、`scripts/gen_licenses.py` 许可台账（Rust 519 包 + 前端 168 包，强 copyleft 无，弱著佐逐项列出） | offline/desktop 套件；`docs/delivery/dependency-licenses.md` |
| P1 领域与可控源 | `trace-domain`：ID/实体/正交状态/Revision 语义摘要/质量判定/错误分类/搜索规范化；`trace-contracts`：控制与插件协议结构；`tests/e2e` 合成源（`FakeSource`）与 `fake` 平台夹具适配器（异构：namespace、游标、零时间、异构资源） | `cargo test -p trace-domain`（25）、`-p trace-contracts`（5+7 golden）、`-p trace-e2e --lib` |
| P2 存储与恢复 | 迁移 + 单写 DB actor + Outbox；流式接收（增量哈希 + 检查点）、校验、对象安装、业务提交状态机、引用与复制件、租约与收据、恢复扫描（RECEIVING/VALIDATING/PREPARED/OBJECT_READY/COMMITTED 矩阵）、完整性巡检 | `cargo test -p trace-core`（25，含 `recovery_tests`、`lease_tests`）；recovery 套件 |
| P3 daemon/IPC/凭证 | `trace-daemon`（`start`/`status`/`shutdown`/`doctor`/`version`；单实例锁 + 端点探测、命名管道受限 DACL、4 字节长度帧 + 1 MiB 上限、多会话、事件与进度推送）；DPAPI vault（DPAPI 加密 + 受保护目录 ACL）；桌面前端命令转发 | `daemon_process` 用例 7 步；`vault::tests`（含 `vault_dir_acl_is_restricted`） |
| P4 UI 与媒体读取 | 7 视图（Home/Creators/Accounts/Timeline/Tasks/Storage/Modules）、正交状态中文文案、命令薄转发 29 个、`trace-media` 自定义协议（仅 64 位 hex 对象名 + 有界 Range 206/200） | desktop 套件（typecheck+vitest 12+build）；`trace-desktop-lib` 单测 |
| P5 搜索/导出/恢复 | FTS5 trigram + 单/双字候选 + 字面复核的搜索与索引代次/滞后处理；catalog 导出、在线元数据 + 完整备份、空目录恢复与格式拒写、凭证 `require-relogin` | core 搜索/备份用例；`full_loop`（备份→恢复→条目保留→凭证清空） |
| P6 Bilibili 首站 | 独立适配器：mid/主页解析、投稿分页枚举、WBI v1、view/detail 规范化、多 P 资源组、DASH/durl 候选归一、错误分类（含 `v_voucher` 挑战）、宿主注入 Cookie | `cargo test -p trace-adapter-bilibili`（19，含 WBI 固定向量与夹具）；site 套件；回写见 [Bilibili 能力表 §5](../research/bilibili-capabilities.md#b-实现回写) |
| P7 整合与自检 | 进程级闭环（真实 daemon + 适配器子进程 + 合成源）、故障与恢复用例、`scripts/verify.py` 五套件入口（含 blocked 非 0 语义）、修复全部自动可处理缺陷 | `python scripts/verify.py --all` 5/5 |
| P8 包与现场 | `trace-daemon doctor`（媒体后端/WebView2/目录/profile 与测试端点自检）、`scripts/package_windows.py` 便携包（4 文件 21.6 MiB、SHA256、manifest、发行 profile 无测试端点、无测试二进制） | 打包脚本自检 + resources 套件；[第 9 节](#打包) |
| P9 收敛与交接 | 状态/环境/事项/能力表/登记表同步，本报告，交付提交，B→A 审查 prompt | `python scripts/check_docs.py` |

未实现且不属本轮：抖音生产接入（M1b，仅合成异构夹具）、扫码登录辅助、自动刷新、自动质量升级/回收（M1c）、番剧/直播/收藏/字幕/弹幕、NSIS 安装包、跨平台构建。

<a id="离线闭环"></a>
## 5. 验证入口与最终结果

```text
$ python scripts/verify.py --all
[PASS] desktop: 桌面前端：类型检查 + Vitest + 生产构建
[PASS] offline: 离线测试：工作区全部单元/契约/适配器夹具测试
[PASS] recovery: 恢复与进程级：对象库恢复矩阵、离线闭环、daemon 双启动/退出/重启
[PASS] resources: 静态与资源检查：凭证哨兵不回显、发行配置无测试端点、依赖许可台账存在
[PASS] site: 站点适配器：Bilibili 夹具/WBI 固定向量/错误分类

5 个套件全部通过
```

`cargo test --workspace --locked` 分项（合计 84 项，0 失败）：

| 目标 | 结果 |
| --- | --- |
| `trace-adapter-bilibili` | 19 passed / 0.00s |
| `trace-contracts`（lib + golden） | 5 + 7 passed |
| `trace-core` | 25 passed / 0.08s |
| `trace-domain` | 25 passed / 0.00s |
| `trace-desktop-lib` | 1 passed |
| `trace-e2e --test daemon_process` | 1 passed / 1.23s |
| `trace-e2e --test full_loop` | 1 passed / 9.10s |

前端：`vue-tsc --noEmit` 通过，`vitest run` 12 项通过（api.spec.ts 4、format.spec.ts 8），`vite build` 成功。Rust：strict clippy 零告警，`cargo fmt --all -- --check` 通过。文档：`python scripts/check_docs.py` 通过。

<a id="失败与复验"></a>
## 6. 失败与复验

以下保留本轮真实失败与修复后复验，不删除失败历史。

| # | 原始症状（实际输出要点） | 根因与修复 | 复验 |
| --- | --- | --- | --- |
| F1 | `cargo test -p trace-core` 挂起至超时（exit 143），DB 用例不结束 | `DbActor::drop` 内部 join，与仍持有 `DbHandle` 发送端的后台任务互等形成死锁。改为 detach：丢弃句柄，队列关闭后 actor 线程自行退出并析构连接 | trace-core 25 项 0.08s 通过；`full_loop`/`daemon_process` 亦通过 |
| F2 | vault 目录 ACL 用例断言失败（`"../evil"` 过滤后为 `"evil"` 而非 `"_evil"`）；早前 `SetSecurityInfo` 对自相对 SD 失败 | 自相对 SDDL 必须经 `GetSecurityDescriptorDacl` 解析后再应用；测试断言语义修正 | 新增正式用例 `vault_dir_acl_is_restricted`：`CreateFileW` + `FILE_FLAG_BACKUP_SEMANTICS` 读取 SDDL，断言 `D:P` 且仅当前用户/SY/BA |
| F3 | daemon 进程级用例：客户端请求全部超时；同时第二实例误报占用（exit 3） | 客户端建立两条连接（读 + 写）而服务端每实例只接受一条全双工会话，写侧连接被当作新会话的 Hello 解析，且并发打开触发 `ERROR_PIPE_BUSY`。改为单连接 + `tokio::io::split()`，并对 231 做 100ms×24 重试 | `daemon_process` 7 步（首启/双启动 already-running/错 archive 拒绝/ACL 校验/超长帧拒绝且服务存活/优雅退出/重启）1.23s 通过 |
| F4 | 夹具适配器 `enumerate` 35s 超时，宿主收不到页 | `proxy_get` 二次 `stdin().lock()` 与主循环持有的 `BufReader` 冲突。改为由主循环把 reader 传入 | `full_loop` 从超时恢复到 9.10s 通过；诊断用 eprintln 已清理 |
| F5 | 离线闭环中适配器请求 404/相对路径 | 合成源地址未注入适配器进程。测试在 `Core::open` 前设置环境变量（单用例二进制，已注 SAFETY） | 闭环枚举 2 页、观察、媒体提交全部完成 |
| F6 | 闭环断言 `verified=0` 失败 | 复制件状态在 `replicas.state`，不在 `blobs`。查询改为 `JOIN replicas` | `verified >= 2` 通过 |
| F7 | 严格 clippy（`-D warnings`）多轮失败：`loop`+`match` 可写 `while let`、多余 `mut`、`iter().cloned()`、字段重复赋值、可折叠 `if`、`map_identity`、`bool_assert_comparison`、`type_complexity`、`needless_question_mark`、缩进残留导致的块不闭合 | 逐项按编译器建议改；`media.rs` 的 Range 块在 let-chain 转换后整体重写 | 严格 clippy 零告警、`cargo fmt --check` 通过；84 项测试与 5 套件复跑全绿，无回归 |

已知非阻塞观察：`target/debug/incremental` 在 Windows 偶发“拒绝访问”提示（不影响编译结果）；`fake` 平台的 `test_allowed_hosts` 仅在测试 profile 中出现，发行 profile 为空（doctor 与 resources 套件强制）。

<a id="c-用例"></a>
## 7. C 用例状态

术语：**通过**＝有对应自动证据；**部分**＝已实现且有间接证据但缺指定注入/样本；**not-run**＝缺真实条件或未注入，不声称通过。

| 用例 | 状态 | 证据与说明 |
| --- | --- | --- |
| C01 重复/乱序页、事务中断后重取 | 通过 | 扫描页事务把 Entry upsert/观察/游标/覆盖同一事务提交（`core.rs` `process_scan_page`，注释标 §9.1）；`enumerate_page_maps_entries_and_cursor`、`idempotency_receipt_key_is_unique`、闭环 2 页游标推进 |
| C02 改名/主页、旧帖编辑、统计、短摘要 | 通过 | domain：`stats_change_does_not_create_revision`、`body_edit_creates_new_revision_and_revert_reuses`、`resource_change_creates_revision`、`summary_never_overwrites_full_revision`、`published_time_is_optional`、`unknown_time_is_representable` |
| C03 各窗口 kill/restart 可重入 | 部分 | `recovery_matrix_is_reentrant` 覆盖 RECEIVING/VALIDATING/PREPARED/OBJECT_READY/COMMITTED 重入；daemon 重启恢复由进程用例覆盖。未做“在每个窗口精确注入 kill”的逐窗口用例（后继：M1a 现场前补注入式 kill 用例） |
| C04 两来源同字节、重复到达、损坏对象 | 通过 | `duplicate_bytes_reuse_and_corrupt_target_isolated`（合法复用且损坏隔离进 trash，不静默覆盖） |
| C05 旧租约/代次、取消后迟到结果 | 部分 | `stale_leases_requeue_but_terminal_states_do_not_revive`、`job_terminal_states_reject_reactivation`、取消命令与提交前代次重检（`objects::commit` 短事务）；“迟到结果被拒”为代码路径 + 租约级用例，无独立时序注入用例 |
| C06 下载中 ENOSPC/存储失联 | not-run | 错误分类与 `waiting_storage` 状态已实现（`CoreError::StorageFull`），但未注入磁盘满/失联；后继：M1b 前补注入用例并记录恢复行为 |
| C07 COMMITTED 后 Outbox 未消费/重复消费 | 部分 | Outbox 与业务提交同事务、搜索/事件消费按 `seq` 幂等推进（`outbox.processed_at`），`full_loop` 验证提交后搜索与事件可见；未做“消费端崩溃后不重下”的专项断言 |
| C08 verified 对象缺失/损坏 | 通过 | `verify_replicas_marks_absent_and_corrupt`：真实标记 absent/corrupt、清 `verified_at`、发出 `replica.state_changed` |
| C09 双 GUI/CLI 竞争启动 | 通过（进程级） | `daemon_process` 步骤 1–2：首启握手就绪，第二实例识别既有端点并成功退出；锁失败不打开可写库（`run_start` 分支） |
| C10 幂等键超时重发、旧 if_revision | 通过 | 闭环中同幂等键重发返回同一 `creator_id`；`idempotency_receipt_key_is_unique`；冲突返回明确错误码（contracts 用例） |
| C11 慢速下载时关/杀 GUI 后再开 | 部分 | 架构上 daemon 独立于 GUI（事件/请求各有连接，GUI 崩溃不影响任务）；进程用例验证 daemon 退出/重启，未做“慢速下载 + 杀 GUI + 10 秒内交互释放”的实测（属 ACT-003） |
| C12 暂停/恢复/完全退出与故障杀进程 | 部分 | `daemon.shutdown` 优雅退出与重启恢复有进程级证据（步骤 6–7），检查点安全由恢复矩阵覆盖；`daemon.pause` 命令存在，未做故障杀进程后检查点比对 |
| C13 凭证哨兵不泄露 | 通过 | 闭环中导入合成哨兵凭证后全落盘文件（DB/WAL/vault/备份/日志）扫描无明文；resources 套件防止哨兵进入文档与 profile |
| C14 全程键盘/焦点/IME/长标题/窄窗/125%/150% 缩放 | not-run | 前端类型检查、12 项单测与生产构建通过，但无实机 UI 交互验证；ACT-003 |
| C15 HTML/脚本、file/javascript、路径穿越/UNC、坏帧 | 通过 | 传输层白名单 + 逐跳重定向 + 私网/元数据地址拒绝（`transport::tests` 4 项）；超长帧被拒且服务存活（进程步骤 5）；媒体协议只接受 64 位 hex 对象名 |
| C16 大媒体 Range 跳播/取消、缺副本、codec 不兼容 | 部分 | `trace-media` 实现有界 Range（206/200、`Accept-Ranges`）与只读对象路径；`read_range` 有 `MEDIA_CHUNK_MAX` 上限；播放器实机跳播/取消与 codec 不兼容提示归 ACT-003 |
| C17 中文一/二/长词、大小写、组合字、通配符 | 通过 | `grams_cover_single_and_double_chars`、`fts_quoting_neutralizes_syntax`、`nfc_and_lowercase`、`like_escape_handles_wildcards`、`snippet_is_bounded`；闭环 3 字以上 FTS trigram 命中合成标题 |
| C18 索引滞后/重建/中断/取消 | 部分 | 索引代次与滞后重排（`search.rs` 从 outbox 重建，`indexed_offset` 可回拨）已实现；未做中断/取消注入用例 |
| C19 停用/移走 adapter、断网 | 部分 | 适配器状态与 `waiting_plugin` 语义已实现（`plugin.list`/`plugin.set_state`、宿主失败分类）；未做真实“移走二进制 + 断网后离线可读”用例（C20 已覆盖离线读取） |
| C20 边采集边备份、恢复空 Profile | 通过 | 闭环：备份（manifest + catalog）→ 恢复 → 条目保留、`credentials=require-relogin`、恢复库 `credentials_state` 为空 |
| C21 导出重名/保留名/恶意路径、缺媒体 | 部分 | 导出/恢复路径经 profile 根约束与格式校验（`storage_ops`），恢复拒写不匹配格式；恶意路径与 Windows 保留名的专项注入用例未做 |
| C22 非当前用户管道、错 archive/协议、超长帧、跳私网 | 通过 | 管道 DACL 受保护且仅当前用户/SY/BA（进程步骤 4 + `verify_pipe_acl`）；错 archive_id 握手被拒（步骤 3）；超长帧拒绝（步骤 5）；URL 规则拒绝非 https/私网/未知主机（`transport::tests`） |
| C23 迁移中断、旧程序遇新 schema、坏备份 | 通过 | `migrations_apply_idempotently_and_reject_newer_schema`（新 schema 拒写）；恢复对不匹配备份拒绝；迁移在事务中执行 |
| C24 GUI 断连/重连/游标过期 | 部分 | 事件泵断线 2s 重连、`resync` 事件与快照重同步路径已实现；状态文案区分 saved/partial/coverage unknown/waiting_auth（format.ts 单测）；未做实机断连重连观测（ACT-003） |
| C25 B 站重复/置顶/乱序页、count 变化、预算耗尽 | 部分 | 身份唯一（`entries` 唯一键 + 幂等 job_key）、分页有界与游标推进、置顶/摘要不覆盖完整 Revision；`count` 仅作观察值。未针对 B 站真实分页乱序/预算耗尽做夹具矩阵 |
| C26 code=0+v_voucher、缺字段、HTML、空流、429/412/超时 | 通过（夹具） | `code_zero_without_voucher_is_ok`、`voucher_inside_success_is_challenge`、`known_codes_map_to_proven_classes`、`unknown_code_stays_unknown`、`playurl_empty_flow_is_source_unavailable`；真实码分布 not-run |
| C27 单 P、双 P DASH、音轨失败、无声、durl 多段 | 通过（夹具） | `observe_single_p_maps_resources`、`observe_multi_p_maps_group_per_cid`、`playurl_dash_prefers_avc_and_maps_audio`、`playurl_silent_dash_yields_no_audio_candidate`、`playurl_durl_requires_all_segments`（拒绝只取首段）；`all_required_saved_is_complete`、`audio_failure_keeps_entry_partial_not_complete` |
| C28 URL 过期/备用地址、200 非 206、错 Content-Range、ETag 变化 | 部分 | 候选含 `backup_url`、重解析后校验长度/哈希、校验不足安全重下（对象身份=内容哈希）；未做 200/206 与 Content-Range 不一致的注入用例 |
| C29 过期/撤销、质量下降、重新输入 | 部分 | vault 支持导入/验证/撤销；凭证失效映射 `AuthRequired` → `waiting_user`，不默称最高质量（保存 requested/available/selected/actual）；真实失效流程 not-run |
| C30 抖音形态异构夹具 | 通过（夹具） | `fake` 平台使用独立 namespace/游标/零时间/异构资源（`video`+`audio(required=false)`），核心不依赖 bvid/cid；`protocol_versions_are_stable_in_m1a` |
| C31 现在/近期范围、改范围/取消、休眠断网恢复 | 部分 | 订阅范围（`recent_days` 等）与检查周期、`subscription.check_now`、取消语义已实现并在闭环中使用；休眠断网恢复未注入 |
| C32 Windows 包隔离启动/关闭再开/退出、缺依赖诊断、无测试件 | 通过 | 便携包构建 + `doctor` 自检（webview2/ffprobe/profile/测试端点）；包内无 `fake`/`e2e` 命名文件；启动/关闭/退出由进程用例覆盖（用的是 debug 构建的同一 daemon 逻辑），**便携包实机双击启动归 ACT-003** |
| C33 10 创作者/1 万正文/5 万资源性能基线 | not-run | 需要预热与 15 分钟测量窗口和真实规模数据；不冒充十万/直播承诺。后继：M1b 前在受控机器执行并记录进程/队列/搜索 P95 |
| C34 授权真实作者/视频闭环 | not-run | 缺账号与授权样本，ACT-002；Bilibili 能力表相应行保持 not-run |
| C35 用户/独立实机安装与体验 | not-run | ACT-003；步骤与预期见[第 10 节](#人工步骤) |

## 8. 兼容、数据与恢复说明

- **对象提交状态机**：`RECEIVING → VALIDATING → PREPARED → OBJECT_READY → COMMITTED`，另有 `SUPERSEDED`/`QUARANTINED`。无 `state` 列的 `blobs` 只表达内容身份；可用性真实状态在 `replicas.state`（`verified`/`staged`/`absent`/`corrupt`）。
- **恢复矩阵**（`recover_materializations`，启动时执行并在 `daemon start` 的 JSON 中回报）：RECEIVING 截断到已持久化检查点；VALIDATING/PREPARED 重算哈希后安装；最终位置已有匹配对象时走重入提交；两侧都缺失则回到 RECEIVING；COMMITTED 但 staging 残留则清理，不撤销成功档案。
- **损坏隔离**：目标位置已有不同字节时不覆盖，重命名为 `corrupt-<时间>-<哈希前缀>` 移入 trash；同字节来源独立复用。
- **备份/恢复**：备份为 catalog（SQLite 备份 API 快照）+ manifest；恢复默认 `credentials=require-relogin`，恢复产物不含凭证状态；格式不匹配拒绝写入。
- **schema 兼容**：`schema_version=2`；遇到更高 schema 版本拒写而不是静默降级；迁移幂等。
- **协议版本**：控制协议与插件协议均为 `0.1`，帧长上限 1 MiB，未知可选字段容忍（契约用例）。
- **未完成兼容项**：跨平台构建与 Linux 无界面运行未验证；NSIS 安装包未生成（需额外工具链与网络）；旧版本档案的前向迁移样本未构造（当前只有 schema 2）。

<a id="打包"></a>
## 9. 打包

`python scripts/package_windows.py` 产物 `dist/trace-0.1.0-windows-x64/`（4 文件，21.6 MiB，`target=x86_64-pc-windows-msvc`）：

| 文件 | SHA256 |
| --- | --- |
| `trace-desktop.exe` | `74cc56db1e85b2d915ada008ee057c7f01b3f0529c204186559542c57a06d27a` |
| `trace-daemon.exe` | `a1b8efa3a4579739cb15545690e54ee4784b916728cca85e688887904b25c998` |
| `adapters/trace-adapter-bilibili.exe` | `f1843a11507f38ff69529d8264c1734b9cfaf0674fda01ae136e104861490d86` |
| `profile.example.json` | `4ff41995c39799f0e06d072e2473b45aabe691768a04ccd22ed6f3598024aede` |

- `manifest.json` 记录版本、目标、构建时间、`control_protocol`/`plugin_protocol`/`schema_version`、doctor 结果与安装/卸载说明；其中 `git_commit` 为构建时的 HEAD（基点 `7027add`），因为打包发生在交付提交之前。二进制身份以上表 SHA256 为准；构建后未再改动任何源码（已核对无源码文件晚于打包时间）。
- `SHA256SUMS.txt` 为全文件摘要，`README.md` 写明运行、依赖诊断与卸载。发行 profile 的 `test_allowed_hosts` 为空，doctor 自检强制；包内无测试适配器或测试端点。
- **未内置 FFmpeg/ffprobe**：缺失时仍可保存原件并提供外部打开，探测/封装能力受限且界面如实显示未知（OSS-004 仍 open）。
- 包目录 `dist/` 不入库；重建命令与本文哈希可作对照。

<a id="人工步骤"></a>
## 10. 人工步骤

以下步骤需要人工或授权条件，B 不代为执行；完成后回填结果即可关闭对应事项。

**ACT-002（真实站点受控实测，未完成）**

1. 准备：使用一个合法可用的 Bilibili 账号与 1～2 个**已获授权**的创作者范围（至少一个单 P、尽量一个多 P），准备可接受的最小请求预算。
2. 配置隔离档案：复制包内 `profile.example.json` 为 `profile.json`，把 `data_root`/`archive_root` 指向临时目录；不要开启 `test_allowed_hosts`。
3. 启动：`trace-daemon.exe start --profile profile.json`；GUI 用 `trace-desktop.exe`（自动复用已有 daemon）。
4. 录入凭证：在「模块/登录」页使用账号凭证导入入口（仅内存与 DPAPI vault；不要粘贴到聊天或写进文件）。
5. 执行范围：先 `解析账号 → 创建订阅（近期有界范围，如 30 天）→ 单次检查`，观察任务中心的枚举/详情/媒体阶段；对多 P 视频确认每 P 资源组。
6. 关闭 GUI 再打开，确认任务继续；随后执行离线恢复（备份 → 在空目录恢复 → 确认条目可用、需重新登录）。
7. 记录：每条请求的站点、页数、字节数、失败分类与时间；把结果回填到 [Bilibili 能力表 §5](../research/bilibili-capabilities.md#b-实现回写) 的 T 列，并把 `ACT-002` 状态改为 resolved 或按实际阻塞写明。

**ACT-003（实机体验与安装，未完成）**

1. 在干净用户环境解压便携包，改 `profile.json` 路径到临时目录，双击 `trace-desktop.exe`。
2. 检查：窗口缩放 125%/150%、中文 IME、长标题、窄窗、全程键盘可达；媒体播放跳播与外部打开；关闭 GUI 后任务继续。
3. 复核诊断：`trace-daemon.exe doctor --profile profile.json`（应输出 JSON 且 `test_allowed_hosts_empty=true`）。
4. 记录实际结果（成功/失败/异常提示），失败项按 [事项总表](../decisions/open-questions.md) 新建或更新编号，不用“环境问题”一笔带过。

## 11. 集中事项与 not-run 边界

| 编号 | 状态 | 本轮变化 |
| --- | --- | --- |
| OSS-001 / OSS-003 / OSS-005 | resolved | Vue 工程、Rust 数据层、测试基础设施已集成并有自动证据；不放宽资源/性能承诺 |
| OSS-002 | in-progress | Tauri 外壳与 IPC/ACL/DPAPI 已通过进程级证据；剩实机安装与 A 复核 |
| OSS-004 | open | 实测为外部 ffprobe 9.0.1（本机 winget），包内未内置；资产核定与分发策略待 A/用户 |
| OSS-006 | resolved（沿用 A） | TS 6.0.3 锁定；未升级 TS7，未跳过类型门禁 |
| OSS-007 | in-progress | 适配器实现与夹具完成，真实站点 T 证据缺；未复制参考实现代码 |
| OSS-008 | open | 抖音仅合成夹具，M1b 生产接入前处理 |
| ACT-002 / ACT-003 | open | 见[第 10 节](#人工步骤)；相关 C 用例保持 not-run |

明确 not-run、不因自动证据改写：真实站点请求/登录/下载（C34）、实机 UI 与安装（C14/C35）、性能基线（C33）、磁盘满注入（C06）、索引中断（C18）、插件移除 + 断网（C19）、导出恶意路径（C21）、逐窗口 kill（C03）、Range/Content-Range 异常注入（C28）、窗口期真实失效（C29）、休眠断网恢复（C31）。

## 12. A 复核范围与定向复验

建议 A 优先复核（按风险从高到低）：

1. **恢复与对象库不变量**：`crates/core/src/objects/mod.rs` 的提交状态机与 `recover_materializations`、`verify_replicas`；对照 `docs/design.md#invariants` 的 INV-03/04/05/11 与 `docs/design/m1a-runtime-contracts.md` §6.4。复验：`cargo test -p trace-core --locked --lib`。
2. **IPC 与凭证边界**：`crates/core/src/ipc/{client,server}.rs`（单连接全双工、帧上限、DACL 校验、重试）、`crates/core/src/vault.rs`（DPAPI + 目录 ACL）。复验：`cargo test -p trace-e2e --locked --test daemon_process` 与 `vault::tests`。
3. **领域语义**：`crates/domain/src/{revision,quality,states,time}.rs` 的语义摘要、正交状态、未知时间表示；对照需求 FR-ARC/FR-QUALITY 与 AC-01/04/19。
4. **站点边界**：`adapters/bilibili/src/{wbi,api,handlers}.rs` 的固定向量、错误分类（不猜测未知码）、durl 全段、无声与空流区分；对照 [能力表 §5](../research/bilibili-capabilities.md#b-实现回写) 与 C25～C29。
5. **离线闭环与哨兵**：`tests/e2e/tests/full_loop.rs` 的断言强度（是否真的验证了“凭证不落盘”“恢复后凭证为空”），可用 `python scripts/verify.py --suite recovery` 复跑。
6. **打包与发行配置**：`scripts/package_windows.py`、`dist/trace-0.1.0-windows-x64/manifest.json`（本地产物）、`docs/delivery/dependency-licenses.md` 的强 copyleft 判定逻辑（`scripts/gen_licenses.py`）。
7. **文档一致性**：`docs/delivery/status.md`、`docs/decisions/open-questions.md`、本报告与代码实际行为是否一致；`python scripts/check_docs.py`。

必须保留的实现与修复（不要回退）：DB actor drop 不 join；客户端单连接 + `split` + `ERROR_PIPE_BUSY` 重试；适配器 reader 透传（不得二次加锁 stdin）；vault 自相对 SDDL 经 `GetSecurityDescriptorDacl` 解析；`replicas.state` 作为可用性正本（不要给 `blobs` 加 state）；测试 `test_allowed_hosts` 只出现在测试 profile；发行 profile 恒为空。

复验建议顺序：`python scripts/check_docs.py` → `python scripts/verify.py --all` → `cargo clippy --workspace --locked --all-targets -- -D warnings` →（可选）`python scripts/package_windows.py` 比对本文 SHA256。

## 13. 阶段结论

- M0 的风险门槛（工程/领域/恢复/IPC/凭证/发行配置）在**自动证据**层面达到可复核状态；A 的独立复核与用户实机验证未进行，故不宣告 M0 验收。
- M1a 的 Bilibili 普通投稿视频闭环在合成源与夹具层面完整可跑（枚举→详情→媒体→搜索→备份→恢复），但真实站点 not-run，故不宣告 M1a 验收。
- 本轮交付范围与限制已全部写入状态、事项、能力表与本报告；下一轮（M1b：抖音生产接入 + 现场测试 + 性能基线）以 A 复核结论与 ACT-002/003 进展为输入。
