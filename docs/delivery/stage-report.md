# M0 · TRACE-CLOSEOUT-02 r1 交付与集中复审

日期：2026-10-07。分支 `codex/archive-closeout`；基点 `e352085ecb4041a3375573d6a8fb83b754faa46e`（`codex/documentation-and-home`）。B 完整交付 SHA 为 `9b9716fb050a9d445eae17257b605d7c109194fb`；用户交接文本另保存于本地 `artifacts/closeout/B-to-A.txt`。后续 A 审查提交不改变这个被审版本。

**当前结论：B 四项返工交付完成；A 技术复审 passed；产品验收 not-run。** 最新复审与实际失败/复验见[第 11 节](#a-closeout-rereview)，Git M0 归并、授权和保护安排见第 12 节。B 实现与包见[第 10 节](#b-closeout-rework)，上一轮 A failed 证据见[第 9 节](#a-closeout-review)。第 1～10 节保留各次交付时的事实；唯一计划仍为 TRACE-CLOSEOUT-02 r1，F 状态在[原审查](../evidence/2026-10-01-m0-m1a-review.md)维护。没有真实站点请求、二进制发布、跨聊天消息或子 Agent。

## 1. 现场、范围和保留

开工核对工作目录、分支及完整 HEAD，工作树干净；从指定基点创建本轮分支。已读入口、索引、状态、计划、有效决定、事项、用户待办和专项规则，逐级找到本机 reference 索引并只读使用。此前已有忽略产物原样保留；本轮源码/测试/规则变更属于 B。没有复制参考源码、原图、私人路径或凭证。

保留 F-001 的全套预构建、旧 B 报告 §12 的 DB actor/单连接 IPC/vault/副本正本/测试端点隔离；保留蓝白主页、七个管理视图及后台状态页。真实默认与显式预览隔离；浏览器样例、临时布局夹具和合成验收档案不写入真实档案。抖音生产、直播、探索、远程没有扩展。

## 2. R 工作包证据

| 编号 | 实现与位置 | 自检及限制 |
| --- | --- | --- |
| R0 | 指定基点/分支/归属核对，锁文件准备，逐条重新检查旧断言 | `pnpm install --frozen-lockfile`、`cargo fetch --locked` 已执行；并非复用旧绿色测试结论 |
| R1 | `paths.rs` 共享严格摘要/路径检查；`storage_ops::restore` 校验 manifest、数据库关系、对象摘要后创建运行时 Profile/目录；凭证和运行引用隔离 | 恶意 ID、遗漏/损坏对象；Core::open/recover 后对象可读。恢复拒绝已存在非空目标和链接组件；对象缺失为 absent |
| R2 | `jobs` 到期待重试/存储任务、5 分钟唯一租约、有限重试、取消代次；`core::execute` 统一终态；对象验证/安装/业务提交重入 | 第二资源失败仍 partial；PREPARED/OBJECT_READY/首资源已提交三个重开窗口；暂停/退出/取消。取消不会令已有有效原件消失 |
| R3 | `transport` 流式背压、连接/无进展超时、逐响应校验；durl 分段独立有序保存；音频必要性依据；锁定 FFmpeg 异步 probe/mux | 33 MiB 慢源超过 30 秒、取消、强 ETag Range 全摘要、错误 Range/ETag/状态；当前站点未提供可靠校验器时安全重下。质量无目标比较保持 unknown |
| R4 | 逐跳主机/IP 校验、连接固定到已检查 DNS；持久 FrameReader；新 IPC 会话快照+当前序号后事件 | 未授权/私网/元数据地址重定向拒绝；800 KiB 分片帧穿插事件；重连不重放历史，队列溢出断开重建快照 |
| R5 | daemon 受控 blob 租约/读取/释放/外部打开、Tauri 有界 Range/CSP、MediaBrowser、真实主页选择器/来源/封面/统计 | 512 KiB 响应上限、后缀 Range、负偏移/非法 ID 拒绝；组件覆盖可用/缺失/失败；不接受任意路径打开 |
| R6 | 闭环加强断言、哨兵贯穿请求与错误、副本巡检游标、许可资产、本地包、隔离验收准备器、文档同步 | 当前自动结果见第 5 节，包见第 6 节；真实站点/原生体验保持 not-run |

## 3. U 界面接线证据

| 编号 | 实现与实际检查 | 边界 |
| --- | --- | --- |
| U01 | Tauri 默认真实读取；显式浏览器预览；空态/失败重试/搜索竞态/卸载后详情不覆盖；预览操作不调档案写 API | `home.spec.ts`；浏览器合成布局夹具是只读测试，非真实档案证据 |
| U02 | daemon 创作者、来源账号与订阅状态；成功后重新读取、失败保留原状态；账号筛选贯通 IPC；重复绑定返回实际 ID，跨创作者冲突拒绝 | 组件保存失败/成功与组合参数用例；合成闭环覆盖账户/订阅实际持久化 |
| U03 | 真实来源键/创作者/账号、发布精度、保存程度、unknown 质量；封面从 verified blob 读取，无图占位；媒体列表可见 | 未声称无目标比较即可满足质量；受控媒体 URI 不暴露文件路径 |
| U04 | 主页和内容库统一 archive.timeline 查询；query/creator/account/namespace/kind/save_state/oldest/cursor；全局/筛选统计，页面另标当前已加载 | 65 条含未知时间记录、17 条分页、双向排序无重复；Rust/TS 共用黄金 JSON。计数/列表/领域状态在同一 DB actor 查询中读取 |
| U05 | 保留蓝白三栏；1536×1024、800×900、420×900 隔离合成长中文页面截图；800/420 document scrollWidth 分别为 800/420；Enter 选择与焦点核对 | 截图 `artifacts/closeout/ui-1536.png`、`ui-800.png`、`ui-420.png`；原生 IME/系统缩放不是浏览器检查，not-run，见 ACT-003 |
| U06 | 真实侧栏仅当前 Bilibili 平台、全部/视频类型；未实现入口不开放；媒体编码失败/文件缺失/后台断连明确反馈 | 预览仍保留明确示例的原布局；外部媒体程序实际交互留 ACT-003，安全参数边界自动验证 |

## 4. F 定向修复与故障断言

以下是本轮实施映射，不替代原审查的状态正本；A 核实前不得称已关闭。

| F | 实现位置 | 对应新增/加强的实际断言 |
| --- | --- | --- |
| F-002/P0 | `crates/core/src/paths.rs`、`storage_ops.rs` | `f002_rejects_malicious_ids_before_writes`：相对穿越、绝对路径、短值、非 ASCII 拒绝，目标外零写入 |
| F-003 | `storage_ops::restore` | `f003_restore_opens_and_marks_omitted_or_corrupt_objects` + full_loop：恢复 Profile 打开、对象全文摘要、遗漏 absent/损坏拒绝、凭证引用清空 |
| F-004 | `jobs/mod.rs`、JOB_RETRY | `f004_due_retry_is_bounded_and_f005_old_attempt_cannot_finish`、`f004_three_automatic_retries_then_visible_failure`；full_loop 一次 HTTP503 后自动成功 |
| F-005 | `core::execute`、对象事务 guard | full_loop observe/media 终态；partial；`f005_cancelled_and_stale_commit_are_rejected`；closeout_faults 首资源提交后重开且整组续完 |
| F-006 | `objects::{ready,finish_and_validate,install_object,recover_materializations}`、run_media | `f006_invalid_validation_never_installs_and_can_retry`；closeout_faults PREPARED/OBJECT_READY 重开后同一操作提交，不重复下载造新操作 |
| F-007 | `transport`、StagedWriter、任务监控 | `f007_stream_over_thirty_seconds_and_cancellation`、`f007_checkpoint_resume_hashes_entire_file`、`f007_http_range_resume_checks_identity_and_full_hash`；closeout_faults 暂停/退出/取消 |
| F-008 | `core::run_media` 独立 video_segment | full_loop 两个有序必要分段与各自 blob；FFmpeg 固定 FLV 两段分别 probe；未字节拼接伪造完整容器 |
| F-009 | Bilibili resolver、media_items.acquisition_evidence、领域 save_progress | `f009_audio_waiver_requires_explicit_silent_or_muxed_evidence`；实际音频初始 required；full_loop 音频失败 partial，最终 complete/unknown |
| F-010 | IPC 媒体命令、Tauri media、MediaBrowser | `f010_ranges_are_bounded_and_suffix_semantics_are_correct`；full_loop 错 ID/负 offset/释放失效/超长请求上限；组件内置播放错误与外部打开失败 |
| F-011 | `ConstrainedClient::request` | `f011_redirects_validate_each_host_and_private_address`：每跳校验，不依赖 reqwest 默认 redirect |
| F-012 | 锁文件/获取脚本/media backend/打包 | `f012_rejects_unverified_backend_before_execution` 拒绝错锁/错摘要；`f012_pinned_backend_probes_and_muxes_fixed_avc_aac_samples`：AVC/AAC、无声、流拷贝轨道/时长、FLV；资产 SHA、版本、-L/-buildconf 与 doctor |
| F-013 | `ipc/server.rs`、`client.rs` | `f013_fragmented_frame_survives_competing_events`；daemon_process 真命名管道 800 KiB 请求+变更事件，重连无历史重放 |
| F-014 | full_loop、verify_replicas | 请求前导入哨兵、合成源确认实际收到；错误体带哨兵但档案/备份/日志无明文；70 副本巡检越过第 64 行；waiting_auth 文案更正 |

每个故障用例由最终全套门禁覆盖。网络缓冲有界以实现和大流测试为证，没有测量进程峰值 RSS，不能据此承诺十万条或多 GiB 真实档案的资源目标。

## 5. 实际验证、失败与复验

环境：Windows x64；Rust/Cargo 1.98.1，Node 24.19.0，pnpm 12.6.0，Python 3.12.14；依赖锁定不升级通用栈。运行目录为仓库根。完整本地日志在 `artifacts/closeout/`；发布检查禁止将运行档案与私人路径提交。

| 命令 | 当前结果 | 证据 |
| --- | --- | --- |
| `corepack pnpm -C apps/desktop install --frozen-lockfile`、`cargo fetch --locked` | passed / 0 | 本轮环境准备 |
| `python scripts/fetch_media.py` | passed / 0 | SHA 校验后解压；实际 -L 为 LGPL3，重新获取校验通过 |
| `cargo clippy --workspace --locked --all-targets -- -D warnings` | passed / 0 | `clippy.log` |
| `cargo run -p trace-e2e --bin prepare_acceptance --locked -- artifacts/closeout/acceptance` | passed / 0 | `prepare-acceptance.log`，2 条合成内容、12 秒有声 MP4、封面、完整备份；真实请求 0 |
| 最终 `python scripts/verify.py --all` | passed / 0，5/5 | `verify-final.log`：desktop/offline/recovery/resources/site 全部通过，包含 F-011 私网跳转与 F-012 错后端拒绝最终断言 |
| 最终 `cargo fmt --all -- --check` | passed / 0 | 最终源码格式检查通过 |
| `python scripts/check_docs.py`、`python scripts/check_publication.py`、`python -m unittest discover -s scripts/tests` | passed / 0 | 54 份文档、0 错误；公开内容 0 发现；5 项反例通过；结果留 `final-gates.log` |
| `git diff --check`、暂存内容审阅 | passed / 0 | 最终差异无空白错误；源码/测试/文档归属为本轮 B，忽略产物不提交 |
| `python scripts/package_windows.py` | passed / 0 | 最终 -3 包及逐文件摘要、隔离 daemon smoke 通过 |
| 真实站点、原生 Windows IME/系统缩放/播放器体验、Linux、NSIS/WiX、公开发布 | not-run | 前两项当前动作只见 [USER-ACTIONS](../../USER-ACTIONS.md) |

保留以下失败与修复事实，不把早期绿灯升级为最终通过：

1. 实施初期 SHA2 格式化编译不兼容、游标首批排序断言、FFmpeg 实际版本多出日期后缀、前端 mock 回包顺序均暴露错误，逐一修复后定向复验。
2. 加强闭环最初看不到 partial，发现按 completeness 字符串逆序会选 summary 而非 full；修正当前版本排序。哨兵最初未经过请求，修正 fake probe_auth 返回无效导致缓存被清除的问题，重新证明实际请求路径。
3. 第一轮 verify：旧 Bilibili `!audio.required` 断言失败，site/offline 失败；修订断言并新增无声/混流证据测试。保留 `verify-initial-failed.log`。
4. 第二轮：共享 JSON 路径错误使前端类型失败；IPC 用例发现 creator.create 没有发出变更事件。增加实际事件，测试进程退出 guard，修正路径；保留 `verify-second.json`。该失败曾遗留测试 daemon，按精确测试 PID 结束，无用户后台受影响。
5. 第三轮/workspace 定向复验：退出时取消与任务 future 同时就绪可将任务记 cancelled，导致 expected queued 失败。执行器在写终态前再次核对全局暂停/退出并回队，补首资源提交后重开窗口；保留 `verify-third-failed.log`、`workspace-tests.log`。
6. 新增 IPC 超长读取测试最初错误期待截断，实际合同是超限拒绝；改为先断言拒绝，再按上限读取并检查数据长度。保留 `verify-fourth-failed.log`。
7. 存储验收路径核对发现旧备份按钮缺参数、历史返回空列表、导出/备份后台错误丢失；补默认目标/真实历史/持久任务及成功失败用例；恢复页显示实际 Profile。
8. 最终资源审阅补 Client Drop 取消读取任务，防止媒体短连接泄漏；事件泵重连发出 resync，主页实际监听 change/resync/disconnected 并在卸载清理。新增事件刷新组件断言。
9. 许可元数据初写 LGPL2.1 与实际资产 -L 不符，已改 LGPL-3.0-or-later 并加获取时断言。Rust incremental 偶发 access denied 提示是构建缓存未复用；未关闭安全功能、未弱化测试或 clippy。

## 6. 产物与版本关系

本地便携包：`dist/trace-0.1.0-windows-x64-closeout-3/`，19 个校验清单成员，共 199,019,663 字节（约 189.8 MiB）。前序 closeout/closeout-1/closeout-2 包保留为中途产物，当前交付采用 -3。

| 文件 | SHA-256 |
| --- | --- |
| `trace-desktop.exe` | `4af1b154e792ebe7dd6f16043e43b49536910b36696535bbc30f9661abe6afa0` |
| `trace-daemon.exe` | `66553ed16fbb54b2109d4a9582ee86ecd82899604735970cfd425a6ef8f28ca3` |
| `adapters/trace-adapter-bilibili.exe` | `36ae28c3aefe4cb16d81b1de337df753723f1e7c3412bccf90eba552c2eab20a` |
| `SHA256SUMS.txt` | `255cea73dc08043ae84a465fa0bcb291234e85354c19aceec69f4a6a946559b4` |
| `manifest.json` | `43b872595e11b10ddfb250a9c7cffa56e64ccc003dae421f764e9a88c0a3882a` |

全部清单摘要复核通过；实际包内 daemon 已在隔离合成 Profile 完成 start/status/doctor/shutdown，schema3，shutdown/进程退出码均 0。doctor：WebView2 154.0.4258.53，媒体来源 packaged、版本 n9.0.2-17-g2a571b6068-20260930、lgpl-shared，无失败原因、测试端点为空；证据 `package-final.log`、`package-smoke.json`。

包生成在本轮提交前，manifest 的 git_commit 如实是基点、source_dirty=true；不是声称基点已有修复。它包含本轮最终运行代码（后续仅追加故障断言与文档整理），最终完整交付 SHA 在 Git 和 B-to-A.txt 中对照。没有运行 NSIS/WiX 或分发。

合成验收工具位于 `tests/e2e/src/bin/prepare_acceptance.rs`，不进入便携包；拒绝已存在目标，可复现生成新 Profile，默认不含测试网络端点或真实订阅。步骤、包摘要与用户预期只维护在 ACT-003。本轮包仅本地验证，Q-004 不因此关闭。

## 7. 契约、许可与剩余限制

- Schema 3 为 schema2 加 `media_items.acquisition_evidence`；控制/插件协议仍 0.1，以新增可选查询字段、返回统计/来源字段兼容旧调用。黄金样本在 `crates/contracts/tests/golden/timeline_page.json`，Rust 与 TypeScript 测试共用。[运行契约](../design/m1a-runtime-contracts.md)已回写。
- durl 分段按序独立保存；DASH 有实测音视频且锁定后端可用时生成 `playback-mux-v1` 派生件，保留原件。M1a 不做自动转码/质量替换；来源无质量目标比较时仍 unknown。
- [站点能力](../research/bilibili-capabilities.md)区分合成/固定夹具与真实 T 证据；真实站点、凭据有效期、无声真实样本依旧 not-run。
- 锁定资产实际为 LGPL-3.0-or-later，包含 `--enable-version3`、无 enable-gpl/nonfree。许可文本和源码来源随本地包保存，正式分发还需 Q-004 的源码/第三方通知与共享库替换义务核对；不能把进程隔离当免责。
- A 技术集中复审仍待进行；B 只标已修待复核。没有将必需代码或自动测试移交用户；ACT-002/003 的真实环境行为不以合成测试替代。

<a id="b-to-a-prompt"></a>
## 8. B→A 集中复审模板

```text
请作为 Agent A 集中复审 TRACE-CLOSEOUT-02 r1。分支 codex/archive-closeout，完整交付 SHA 以随附 B-to-A.txt/用户消息为准，并与 git rev-parse codex/archive-closeout 对照；基点 e352085ecb4041a3375573d6a8fb83b754faa46e。
先读 AGENTS.md、docs/README.md、docs/delivery/status.md、docs/delivery/stage-report.md、当前计划、有效决定、事项和 USER-ACTIONS.md，再核对工作区归属。
复核 R0～R6、U01～U06 与原审查 F-002～F-014，F-002/P0 优先；保留 F-001、旧 B 报告 §12、蓝白主页/管理入口和本地参考隔离。逐条检查故障断言和实际实现，不以全套绿色替代定向审查。历史失败保留在原审查与本轮报告 §5；F 仅为已修待复核，由 A 决定是否核实关闭。
重新核对全套 verify、fmt、严格 clippy、文档/公开检查及反例、便携包 manifest/SHA256SUMS/doctor 和合成验收档案。重点看恢复目标外零写入、任务/物化重入、暂停/取消/退出竞争、强 ETag Range 哈希、音频/分段完整性、受控媒体、IPC 并发与快照重连、统计/分页/真实订阅状态。
真实站点、原生 IME/缩放/媒体体验及产品验收 not-run，当前用户动作只维护 USER-ACTIONS；Q-004 未关闭。不要扩展抖音生产、直播、探索或远程，不推送/发布、不自动跨聊天发送或启动子 Agent。结束提供可复制的 A→B 集中反馈 prompt。
```

<a id="a-closeout-review"></a>
## 9. Agent A 集中复审（2026-10-07）

### 9.1 被审版本与三项结论

本次为接收 B 交付后的 Agent A 审查，实际读取代码、断言、日志和包，并独立补充反例；没有参与 B 实施，不沿用 B 自检作为审查结论。被审提交 `9b9716fb050a9d445eae17257b605d7c109194fb`，父提交 `e352085ecb4041a3375573d6a8fb83b754faa46e`。开工 HEAD/分支匹配，工作区与暂存区干净，无未跟踪源码；既有忽略的依赖、构建、包、日志与合成档案均保留。已逐级找到并只读本地 reference 索引，没有复制参考源码/原图，没有使用真实档案或凭据。

| 结论 | 实际判断 |
| --- | --- |
| B 实现交付 | 实现、自检和本地包确实存在；原交付五套件由 A 复跑 5/5 passed。但新增反例揭示必须行为缺口，退回 **rework-required** |
| A 技术复审 | **failed**。F-002/P0 已核实修复；保留 F-007/F-008，新增 F-015/F-016，均为 P1；本次未确认新的 P0。F 当前状态只在原审查表维护 |
| 产品验收 | **not-run / 未通过**。真实站点、Windows 原生 IME/缩放/播放器与外部程序体验未执行，不能由自动门禁或截图替代；用户事项只在 USER-ACTIONS 维护 |

A 未修改运行产品代码；新增 Rust 3 个、Vue 1 个失败回归测试，B 应修实现使其通过，没有删除/跳过/弱化断言。审查文档与这些测试归 A。B 原提交、旧报告 §12、原审查历史失败、本报告 §5、蓝白主页、管理入口与 F-001 均保留。

### 9.2 逐 F 核对

本表记录本次证据，关闭状态以[原审查 §4](../evidence/2026-10-01-m0-m1a-review.md#4-发现)为准。baseline 指 A 在未加入反例、运行代码正好为被审 SHA 时复跑的五套件，不是以下定向核对的替代。

| F | 实现和故障断言核对 | 本次结果 |
| --- | --- | --- |
| F-002/P0 | paths 校验摘要后才切片/拼路径；no_links 检查源/目标组件；restore 先验对象清单。恶意 manifest 用例含穿越、绝对路径、短值、非 ASCII，拒绝时目标不存在、外部哨兵无写入 | passed；原路径注入可关闭；并发文件系统攻击压力测试 not-run |
| F-003 | 正确 Profile/state/objects 布局；源/目标摘要、凭证引用清空、订阅暂停；restore_tests/full_loop 用 Core::open/recover 读取对象、摘要和 absent 副本 | passed；可关闭；旧真实档案与跨平台恢复 not-run |
| F-004 | claim_due 包含到期待重试/存储态；3 次自动重试上限与 JOB_RETRY 代次；lease_tests 验证到期前不认领、不同 owner、第四次失败停止；full_loop 一次 503 后成功 | passed；可关闭；订阅启停另见 F-015 |
| F-005 | execute 写整组终态，commit 同事务查状态/owner/generation/expiry；租约 5 分钟并心跳；observe/media 真终态、音频失败 partial、首资源提交后重开和取消迟到拒绝 | passed；可关闭 |
| F-006 | VALIDATING 失败隔离，ready/install/recover 重验大小/摘要；PREPARED、OBJECT_READY、首资源提交后 Core 重开续完同一操作；失败数据不安装 | passed；可关闭；范围是已测窗口，不冒称每条机器指令都做过强杀 |
| F-007 | 33 MiB/>30 秒、流式背压、全文件哈希、暂停/取消/退出修复成立；A 新反例中分块 206 宣称总长 10，只有 5 字节仍返回 Ok 并安装 | **failed**；EOF 长度校验仍缺失，见原审查 §10 |
| F-008 | 独立有序段取代拼接，各段 blob/FLV probe 的原断言成立；但展开段清空 expected_size/expected_sha256。A 反例声明 8+8，保存 1+1，任务 succeeded、条目 complete | **failed**；段/资源组已知校验依据不能丢失 |
| F-009 | 初始音频 required，有独立音轨必需，明确空音频/混流证据才能豁免并持久化；核心调用领域规则，质量无目标比较为 unknown；夹具与闭环音频失败 partial 匹配 | passed；原状态问题可关闭；真实无声来源 not-run |
| F-010 | 按 blob ID、允许扩展名、摘要生成外部打开副本；租约/512 KiB 读取、负 offset/超限/释放失效、后缀 Range/CSP/组件错误反馈有实现和断言 | passed；原缺实现/任意路径/无界读取可关闭；播放器事件稳定性另见 F-016，原生体验未代验 |
| F-011 | 禁默认重定向/代理；逐跳主机/IP 校验，连接固定到再次检查的 DNS 结果，跨源清凭证头；未授权域、私网/元数据地址跳转用例通过 | passed；原逐跳缺失可关闭；真实 DNS 重绑定压力测试 not-run |
| F-012 | 锁资产/摘要/收据/双程序版本许可检查，异步有限时 probe/mux 有调用方；固定 AVC/AAC/无声/FLV 与错锁/错哈希拒绝通过；A 核对包内 doctor/资产摘要 | passed；本轮集成缺口可关闭；正式分发依 Q-004 |
| F-013 | FrameReader 状态在 select 外；持续事件下 800 KiB 分片命名管道请求解码；新会话当前序号起订阅，重连发 resync，Drop 释放 reader | passed；原丢帧/从零历史重放可关闭；UI 刷新稳定性另见 F-016 |
| F-014 | full_loop 断言终态/partial/complete/恢复/哈希；请求前哨兵经服务端计数证明确实收到，错误体后检查持久文件；70 副本巡检越过 64 行，waiting_auth 文案更正 | passed；原具体证据缺口可关闭；新反例仍是必须门禁，不代表测试穷尽所有错误 |

### 9.3 逐 R/U 结论

| 项 | 结果与范围 |
| --- | --- |
| R0 | passed：基点/单个 B 提交/分支/归属/reference 核对，F-001 预构建与锁保留；Cargo.lock 仅把既有 base64 加入桌面依赖，无通用栈升级 |
| R1 | passed：F-002/F-003 定向断言、恢复可打开和真实对象哈希核实 |
| R2 | 原 F-004/005/006 范围 passed；任务/订阅续行整体 failed（F-015） |
| R3 | failed：慢流/取消/正常续传子集通过，F-007 EOF 与 F-008 段大小待修 |
| R4 | passed：逐跳校验、大帧并发、当前序号重连机制核实；无真实网络压力达标声明 |
| R5 | failed：真实数据/来源/封面/媒体安全已接线，F-015/F-016 阻止可靠启停和持续浏览 |
| R6 | failed：原门禁/包通过，但 A 新增 4 个反例失败；证据完成不等于产品完成 |
| U01 | failed：预览隔离、错误/竞态/卸载原断言通过；事件 refresh 清列表导致选择丢失（F-016） |
| U02 | failed：创建/读回持久化属实；恢复订阅仅变 state，scan job 保持 failed（F-015） |
| U03 | 来源/精度/封面/占位/媒体 ID 读取 passed；完整状态的端到端正确性受 F-008 阻断，UI 读取 complete 不等于内容完整 |
| U04 | passed：同一 actor 的组合条件、scope cursor、65 条/17 条分页、双向排序、未知时间、全局/筛选计数核实；大档案性能 not-run |
| U05 | 自动/浏览器子集 passed：核对键盘/长中文用例及 B 的 1536/800/420 截图；A 查看 1536/420 确认蓝白与窄窗重排。原生 IME/系统缩放 not-run，完整产品条目尚不可 accepted |
| U06 | 安全入口/缺失/编码错误/断连反馈子集 passed；持续媒体浏览 failed（F-016），外部程序实机交互 not-run |

### 9.4 A 实际命令、产物与限制

日志在忽略目录 `artifacts/a-closeout-review/`；新增反例位于 `tests/e2e/tests/a_review_probes.rs` 和原 `home.spec.ts`，随修复复跑。没有用撤回反例来制造全绿。

| 命令 / 检查 | 实际结果 | 证据与边界 |
| --- | --- | --- |
| 原 SHA 上 `python scripts/verify.py --all` | passed / 0，5/5 | `verify-all.log`；在 A 添加反例前完成，B 报告可复现 |
| `cargo test -p trace-e2e --locked --test a_review_probes -- --nocapture` | **failed / 101，3 failed** | `probes-final.log`；Range 5/10、分段 2/16、恢复订阅仍 failed；均为行为断言失败 |
| `corepack pnpm -C apps/desktop exec vitest run src/views/__tests__/home.spec.ts` | **failed / 1，1 failed、12 passed** | `home-probe-final.log`；选择第二条后无关 change 事件使选择变成第一条；播放器节点保持的后续断言一并保留 |
| `cargo fmt --all -- --check` | passed / 0 | `fmt.log`；仅格式化 A 新增 Rust 测试 |
| `cargo clippy --workspace --locked --all-targets -- -D warnings` | passed / 0 | `clippy.log`；包含 A 新测试，已有 incremental access denied 提示不改变成功退出 |
| `corepack pnpm -C apps/desktop run typecheck` | passed / 0 | `typecheck-final.log`；A 夹具补全 size 字段后通过 |
| 包全文件摘要/字节、合成档案核对，包内 daemon start/status/doctor/shutdown | passed / 0 | `package-review-final.log`、`package-review.json`；A 新建隔离 Profile，B 样本只读 |
| `python scripts/check_docs.py` | passed / 0 | 54 份文档、526 个本地链接、0 错误；`docs-final.log` |
| `python scripts/check_publication.py` | passed / 0 | 工作区和索引 0 发现；`publication.log`，提交前再次检查暂存内容 |
| `python -m unittest discover -s scripts/tests` | passed / 0，5 tests | `script-tests.log`；隔离治理反例 |
| `git diff --check`、`git diff --cached --check` | passed / 0 | 本轮源码归属与明确路径暂存检查；`diff-check.log` |
| `python scripts/package_windows.py` 重新打包 | not-run | A 未修改运行代码；独立核对既有包并复用 B 构建证据，不生成无必要的新包 |
| 真实站点/登录/下载/风控/真实无声样本，原生 Windows IME/缩放/媒体体验，Linux/NSIS/WiX/签名/发布 | not-run | 无真实数据/网络授权扩展，不用合成源替代 |

包 SHA256SUMS 自身摘要仍是 `255cea73dc08043ae84a465fa0bcb291234e85354c19aceec69f4a6a946559b4`，19 个成员 + 清单自身，共 199,019,663 字节，全数匹配；manifest 为基点 + source_dirty=true。A 确认其没有冒称干净 B SHA 构建；没有从摘要推导可重现构建已验证，运行代码与包的对应还依赖 B 构建记录，后续代码修复需新包并绑定最终运行源码。

B 合成档案只读核对：2 条 entries、2 个对象（大小和摘要匹配）、0 个 subscriptions、0 个 credentials_state、测试网络白名单为空；未启动 B 验收档案。包内 doctor 为 packaged、锁定 FFmpeg 版本、无失败原因；A 隔离 daemon 启停及进程退出均 0。峰值 RSS/真实大档案资源目标未测，Q-004 没有因本地验证关闭。

保留 A 自身准备失败：首次 Vue 反例误放 beforeEach 导致用例收集失败，已移出；typecheck 指出夹具缺 size，已补全。包脚本首次对 Windows 扩展路径构造 SQLite URI 失败，规范化只读路径后通过。初始日志保留，最终 4 个行为失败均在修正测试工具问题后确认；这些工具准备失败不算产品缺陷。

最终 A 新测试两套定向复跑仍为 Rust 3 failed / Vue 1 failed；增加这些断言后的 `verify.py --all` 整体再次运行是 not-run，已知失败由上述定向命令直接证明，不声称当前工作树全绿。A 交付为独立本地审查提交，父提交保持 B 的完整 SHA，未推送；提交本身保存失败证据，不表示实现通过。

<a id="a-to-b-closeout-prompt"></a>
### 9.5 A→B 集中返工 prompt

```text
请作为 Agent B，按唯一计划 TRACE-CLOSEOUT-02 r1 在 codex/archive-closeout 集中返工，不另建竞争计划。
先读 AGENTS.md 要求的索引、状态、计划、有效决定、事项和 USER-ACTIONS，核对 reference、HEAD 和文件归属。被审 B 提交为 9b9716fb050a9d445eae17257b605d7c109194fb，原基点 e352085ecb4041a3375573d6a8fb83b754faa46e。A 审查提交位于 B 提交之后，先用 git log -1 --format=%H 读取完整 A 基点，保护用户后续修改，不 amend 独立 A/B 历史提交。
集中处理 docs/evidence/2026-10-01-m0-m1a-review.md §10 的 F-007、F-008、F-015、F-016，均为 P1。F-007：分块 206 收尾验证 Content-Range 目标/实收长度，拒绝截短、超长、非预期成功状态，不安装不完整字节。F-008：独立有序段保留每段/总量的已知校验依据，短段不能 succeeded/complete；不要恢复字节拼接或把总大小错误分配到每段。F-015：暂停/恢复订阅要控制关联扫描任务，暂停不消费失败预算，恢复重新调度，覆盖到期/执行中竞争和重启。F-016：事件刷新保持仍存在的选择、已加载页面、媒体节点/播放位置，覆盖无关/相关事件、分页、选择失效和慢请求竞态，不关闭事件刷新来通过。
A 报告在 docs/delivery/stage-report.md §9。保留 tests/e2e/tests/a_review_probes.rs 的 3 个失败回归测试和 home.spec.ts 的 A 事件测试，不删除、跳过或弱化断言。原交付 verify 5/5 真实通过，但 A 新反例 Rust 3 failed / Vue 1 failed；日志在 artifacts/a-closeout-review。
F-002/P0、F-003～006、F-009～014 原问题已由 A 核实关闭，保持修复和回归。保留 F-001、旧 B 报告 §12、原审查历史失败、本报告 §5、蓝白主页/管理入口与本地参考隔离；无需重建已闭合范围。
连续完成返工、自检修复和文档同步；定向通过后跑 verify.py --all、fmt、严格 clippy、文档/公开检查、scripts/tests 和 diff 检查。运行代码修复后重打本地包，核对最终运行源码/manifest/全文件摘要/隔离 daemon smoke，更新同一报告的失败与复验、契约、状态和事项。F 只能标已修待复核，按本地 Git 规则保存集中 B 修复提交。
产品验收仍 not-run；ACT-002/003 与 Q-004 当前状态只在 USER-ACTIONS，开发失败不要转嫁用户。真实站点、原生 IME/缩放/媒体、峰值 RSS/真实大档案、Linux/安装器/签名/发布的未测边界继续如实记录。不扩展抖音生产、直播、探索、远程，不推送、发布、跨聊天发送或启动子 Agent。
完成后给用户可复制 B→A 集中复审 prompt，包含完整新 SHA、报告、四个 F 修复位置、定向/全套结果、新包摘要和剩余 not-run。
```


<a id="b-closeout-rework"></a>
## 10. Agent B 四项集中返工（2026-10-07）

### 10.1 现场与范围

执行唯一计划 TRACE-CLOSEOUT-02 r1，不另建计划。开工 HEAD 为 A 审查提交 `f03e307d4ca53d0f7682e1d8929d2cd4615be493`，分支 codex/archive-closeout，工作区干净；被审 B 为 `9b9716fb050a9d445eae17257b605d7c109194fb`，原基点 `e352085ecb4041a3375573d6a8fb83b754faa46e`。逐级只读 reference 索引；已有忽略的档案、构建、包和失败日志保留。未使用真实档案/凭证，未启动子 Agent、发送消息或推送发布。

本次只返工 F-007/F-008/F-015/F-016，分别覆盖 R3、R2/U02、R5/U01/U04/U06 及 R6 验证。F-002/P0、F-003～006、F-009～014 的 A 核实关闭状态不改写；F-001、旧 B §12、本报告 §5/§9 和原审查历史保留。`a_review_probes.rs` 原文件不改动，home.spec.ts 的 A 事件测试完整保留；新增 B 回归另写，不跳过或弱化 A 断言。蓝白布局、管理入口和本地参考隔离不变。

### 10.2 四项实现与故障断言

| F | 修复位置与行为 | 本轮定向证据 |
| --- | --- | --- |
| F-007 | `transport.rs::download_media` 只接受预期 200/206，声明总长作为写入上限和 EOF 必须值；超长块写入前拒绝、短体返回错误，保留 RECEIVING | A 原 chunked 206 5/10 反例通过；`closeout_rework::f007_chunked_lengths_and_success_statuses_are_exact` 覆盖 11 种长度/状态组合，包括正确续传全文、短体/超长、非预期 201/204/205，失败零对象 |
| F-008 | `plugin.rs::UrlSet` 新增可选逐段校验；Bilibili `map_video_candidates`、fake adapter 同步；`core.rs::run_media/verify_candidate_files` 先按段验证，再核对整组总量/顺序摘要后提交新分段引用；校验依据持久化且不拼接媒体 | A 原两段短体反例通过；新增不同段大小、旧协议仅总量、总量不符、局部未知、错误整组/逐段摘要共 8 个合成场景；好例两个独立对象大小 2/5、complete，坏例零变体且不 succeeded/complete；Rust 新旧 DTO 往返和生产缺 size 夹具 |
| F-015 | `jobs::set_subscription_enabled/claim_due/ClaimedJob::check`、`dispatch::SUBSCRIPTION_UPDATE`、`core::execute/run_scan`：事务启停/撤销租约、恢复原扫描、幂等重复恢复；暂停不消费失败预算 | A 原恢复订阅反例通过；新增未到期→暂停→到期、暂停后 Core 重开、执行中暂停/立即恢复/重复恢复、迟到扫描页拒绝。断言唯一扫描、原 2 次失败预算未增加、旧页零条目、新页已写检查点 |
| F-016 | `HomeView.vue::load/loadDetail` 协调列表/已加载分页与选择；`ArchiveTimelineArgs.entry_id` 经 Tauri/API/feed 同筛选确认窗口外选择；新请求代次拒绝晚到查询/详情；`MediaBrowser.vue` 保留可用变体和 DOM 节点 | A 原无关事件反例通过；新增相关事件跨页保留 e2、变体 b、同一 video 节点及 currentTime=37；选择真正消失时解释切换；旧快照/旧详情晚到不覆盖新选择，播放位置 11 保持；排序移动出窗口仍保留有效选择和位置 19 |

协议仍 0.1，UrlSet 字段缺失默认 None；schema 仍 3，借用既有 acquisition_evidence 保存非敏感校验依据；无通用依赖升级，原 LGPL3 后端/许可不变。[运行契约 §8](../design/m1a-runtime-contracts.md#8-四项集中返工合同2026-10-07b)、[站点能力 §7](../research/bilibili-capabilities.md#7-分段校验返工2026-10-07b)同步。F 当前状态仍只在原审查维护，B 只能标已修待复核。

### 10.3 实际验证与失败保留

运行环境与第 5 节相同，全部命令从仓库根执行。新日志使用 `artifacts/closeout/rework-*`，A 的 `artifacts/a-closeout-review/` 不变。

| 命令 | 结果 / 退出码 | 证据 |
| --- | --- | --- |
| `cargo build --workspace --locked` | passed / 0 | `rework-build.log`，用于定向进程测试前置 |
| `cargo test -p trace-e2e --locked --test a_review_probes -- --nocapture` | passed / 0，3/3 | `rework-a-probes.log`；原短段 saved=0，暂停 task=paused，恢复后可继续 |
| `cargo test -p trace-e2e --locked --test closeout_rework -- --nocapture` | passed / 0，3/3 | `rework-extra-2.log`；最终增加逐段摘要反例由全套覆盖 |
| `corepack pnpm -C apps/desktop exec vitest run src/views/__tests__/home.spec.ts` | passed / 0，17/17 | `rework-home-final.log`，包含原 A 测试和四条 B 事件回归 |
| `corepack pnpm -C apps/desktop run typecheck` | passed / 0 | `rework-typecheck-final.log` |
| `cargo clippy --workspace --locked --all-targets -- -D warnings` | passed / 0 | `rework-clippy-final.log` |
| 最终 `python scripts/verify.py --all` | passed / 0，5/5 | `rework-verify-final.log`，含 A 原反例、8 个分段场景、主页 17 项及已关闭问题的回归 |
| `cargo fmt --all -- --check`、文档/公开检查、`scripts/tests`、diff 检查 | passed / 0 | `rework-final-gates.log`；54 份文档、535 个本地链接、0 错误；公开内容 0 发现，5 个反例通过；提交前检查最终文档与暂存内容 |
| 修改运行代码后的本地包、源码输入对照、摘要与隔离 daemon smoke | passed / 0 | `rework-package.log`、`rework-package-smoke.log/json`，见 10.4 |

保留新失败：新增 B 测试初次编译发现辅助函数需要 `&Arc<Core>` 而非 `&Core`，以及一个 unused import，日志 `rework-extra.log`；修正测试辅助类型/导入后 3/3 passed。既有 job lease 测试原先没有订阅实体，本次补合法的 enabled 订阅夹具以满足认领前置，原断言保留。A 已确认的 Rust 3 failed/Vue 1 failed 仍在 §9 与原日志，不能改写为当时通过。

首次全套返工验证又识别旧合成源把两段固定声明为 98,304 字节，而 media_bytes 按四字节填充后实际为每段 98,307；新校验拒绝该响应，闭环等待超时。修复 FakeSource 用实际生成内容长度构造大小声明，不改媒体字节或成功/失败断言。失败跑次 `rework-verify.log` 保留（offline/recovery failed；脚本只保留输出尾部，offline 具体失败正文被截断，不扩充未经保存的诊断）；合成源修正后的定向闭环与最终全套均通过。

已修正合成源的独立闭环复验：`cargo test -p trace-e2e --locked --test full_loop` passed / 0，1/1，19.24 秒；日志 `rework-full-loop.log`。

### 10.4 新包与剩余边界

本地新包：`dist/trace-0.1.0-windows-x64-closeout-4/`，19 个校验成员，另含清单自身，共 199,172,866 字节。此前 -3 等包保留为历史产物，当前交付使用 -4。

| 产物/输入 | SHA-256 |
| --- | --- |
| SHA256SUMS.txt | `7bcf580c7585bb711834e6abe9de0b7994381a5e6979b3a6e1fc3d56b53f097b` |
| manifest.json | `9824c9d566816b85c71619e9429862416a2f88b10405cba985f942591f4d442e` |
| trace-desktop.exe | `a2fc3228a321049cb74b502e8464a5f2c6b72bad3667f226c1e3629c2060f45d` |
| trace-daemon.exe | `1d58ffd10511d9b895816d52cb7aaa116b77f5aeea3d26a37defd38399edfa7c` |
| adapters/trace-adapter-bilibili.exe | `2e6d25e634d8f9a01cb4db1d60aa6578d325931491941343e36707d8e1dcbf71` |
| source_tree_sha256 | `ad7badac695e492a960f06da88d870280dd149557b2d17ddd2fb274912363158` |

`python scripts/package_windows.py` passed / 0。构建前后核对及包后独立核对的 113 个源码/配置输入摘要一致，全部包内摘要通过；manifest 如实记录 A 基点 `f03e307d4ca53d0f7682e1d8929d2cd4615be493` 和 source_dirty=true，同时保留 source_files_sha256/source_tree_sha256，不能把基点本身当作修复后源码。之后只整理文档与交接，最终提交前再核对输入内容。

`python artifacts/closeout/rework-package-smoke.py` passed / 0：新建独立合成空 Profile，包内 daemon start/status/doctor/shutdown 与进程退出均成功；schema3、WebView2 154.0.4258.53、媒体来源 packaged、锁定版本/变体正确、测试端点为空。该脚本没有打开用户档案，也未改原 B/A 合成档案。源码摘要证明本包对应输入内容，不声称可重现二进制构建；包/日志均忽略，不提交入库。

产品验收 not-run；真实站点、Windows 原生 IME/系统缩放/播放器与外部程序体验、资源测量、Linux、安装器、签名和发布均 not-run。B 组件 DOM/currentTime 断言只证明刷新稳定性，不冒充真实解码/播放。无可靠站点校验器时仍安全重下，分段不承诺无缝拼接，质量无目标比较仍 unknown。ACT-002/003、Q-004 的当前状态只维护 USER-ACTIONS；没有把四项开发缺口转交用户。


<a id="b-to-a-rework-prompt"></a>
### 10.5 本次 B→A 集中复审交接

本次新增一个本地修复提交，父提交为 A `f03e307d4ca53d0f7682e1d8929d2cd4615be493`，不 amend 原 A/B 历史。最终完整 SHA 在 `git rev-parse codex/archive-closeout` 与本地 `artifacts/closeout/rework-B-to-A.txt`、用户交接消息核对，避免提交正文自引用。下列为同批复审要求；完整交接文件附具体新 SHA、包摘要、命令与未测边界。

```text
请作为 Agent A，按唯一计划 TRACE-CLOSEOUT-02 r1 集中复审 codex/archive-closeout 的最新 B 修复提交；完整 SHA 以随附 rework-B-to-A.txt 为准，父提交 f03e307d4ca53d0f7682e1d8929d2cd4615be493。
先读 AGENTS 入口、索引、状态、计划、有效决定、事项、USER-ACTIONS 和 docs/delivery/stage-report.md §10，核对归属及本地 reference。原 B 9b9716f、A f03e307 的报告和失败记录保留。
集中核对 F-007/F-008/F-015/F-016，当前只为已修待复核：传输头与实收 EOF、逐段与整组校验/协议兼容、订阅暂停恢复及迟到页、主页分页/有效选择/媒体节点/播放位置和慢请求。A 原 a_review_probes.rs 文件及 home.spec.ts 事件测试未删改；B 新回归见 closeout_rework.rs 和主页 F-016 describe。
B 最终 verify 5/5、fmt、严格 clippy、文档/公开内容/5 项治理反例及 diff 检查通过；定向 A Rust 3/3、B 扩展 Rust 3/3、主页 17/17、修正合成源后的全流程 1/1。报告 §10.3 保留测试准备失败与首轮 verify 失败，不用绿色测试代替逐 F 核实。
新包为 dist/trace-0.1.0-windows-x64-closeout-4；摘要见 §10.4。核对 manifest 的 113 个输入摘要/源码总摘要、每个包内文件及隔离 daemon smoke；不能把 A 基点+dirty 当成干净修复提交构建。
保留已核实关闭的 F-002/P0、F-003～006、F-009～014，以及 F-001、旧 B §12、蓝白主页/管理入口和本地参考隔离。产品、真实站点、原生 IME/缩放/媒体、资源测量、Linux/安装器/签名/发布均 not-run；当前用户事项只在 USER-ACTIONS，代码缺口集中交回 B。不扩展抖音生产、直播、探索或远程，不推送/发布/跨聊天发送/启动子 Agent。结束提供可复制的 A→B 集中反馈 prompt。
```


<a id="a-closeout-rereview"></a>
## 11. Agent A 四项返工集中复审（2026-10-07）

### 11.1 基点、范围与结论

实际开工 HEAD 为 B `33cfa4cb05a947691a94c4bc78f8e3120cb67e44`，父提交 A `f03e307d4ca53d0f7682e1d8929d2cd4615be493`；分支 codex/archive-closeout，工作区干净。原 B/原始基点仍为 `9b9716fb050a9d445eae17257b605d7c109194fb` / `e352085ecb4041a3375573d6a8fb83b754faa46e`。已读入口、索引、状态、唯一计划、有效决定、事项、USER-ACTIONS 与专项规则；逐级只读本地 reference。保留已有忽略产物与用户合成样本，不清理旧失败日志。

B 实现交付：四项修复具备实际代码与故障证据。A 技术复审：逐项 passed，F-007/F-008/F-015/F-016 关闭；新增 F-017 为故障夹具不稳定，A 局部修复并自检通过。产品验收：not-run，未把 DOM/currentTime、合成源或包内 doctor 当作原生体验。R0～R6/U01～U06 原复核与已闭合 F 保持，只补本次受影响范围，不重建竞争计划。

### 11.2 逐项审查与故障断言

| 项 | A 核对的实际行为与断言 | 判断 |
| --- | --- | --- |
| F-007 / R3 | `download_media` 校验 200/206、Content-Range 起点/终点/总量、强 ETag；使用总量限制写入并在 EOF 比较实收字节。原 5/10 截短反例拒绝；11 种长度/状态场景通过，坏响应保留 RECEIVING 且零 blob | passed；不完整字节不能进入安装路径 |
| F-008 / R3 | `UrlSet` 缺省字段兼容旧协议；Bilibili 对每段保留已知 size，任一未知时总量未知；核心逐段检查后按顺序计算整组 size/hash，再提交新段引用。已存变体和 OBJECT_READY 重用也经过当前校验；原资源排除派生段重新 resolve，避免重复扩展 | passed；独立有序对象，无字节拼接/平均分配总量 |
| F-008 正反对照 | 原短段反例 saved=0/job=failed；B 8 场景覆盖不同大小、短体、局部未知、旧协议总量及错误整组/逐段摘要。A 另补正确逐段+整组摘要、旧协议仅正确整组摘要两个正向对照，共 10 场景；正确结果独立 2/5 字节对象且 complete，错误结果零变体且不 complete | passed；原 8 场景语义与断言未弱化 |
| F-015 / R2/U02 | 订阅和扫描状态同事务更新，暂停/恢复撤销旧 owner 与 generation，claim/check 验证 enabled；旧 page、waiting_plugin 和结束重调度受 guard 约束。实跑暂停后到期、Core 重开、执行中暂停立即恢复、重复恢复、迟到页 | passed；唯一扫描，旧页零条目，原 attempts=2 不增加，新页检查点写入 |
| F-016 / R5/U01/U04/U06 | `HomeView` 同筛选刷新保留页窗口/选择，窗口外选择用 entry_id 同筛选查询确认；列表与详情请求代次拒绝迟到结果；`MediaBrowser` 保留可用 variant 和节点。原 A 无关事件测试及 B 四个相关事件/分页/失效/竞态测试实际运行 | passed；有效选择与 node/currentTime 保持，真正失效解释切换，事件仍启用 |
| F-017 / R6 | 原夹具一次 503 随机落在任一条目的音频上，若落在分段组，整组提交策略使断言要求的 partial 未必出现。A 把故障限定到单视频条目 v-1 的音频，视频先提交的前提成立；`full_loop.rs` 原文件和全部断言不改 | A 局部自修自检 passed；非运行代码修复 |

代码核对覆盖 `core.rs::run_media/verify_candidate_files/commit_candidate`、任务 lease/check、dispatch 同事务版本比较、feed 同筛选 SQL、Tauri/API 参数传递及 Vue 列表/详情竞争。协议 0.1、schema3 与既有生产依赖不变。无新生产缺陷需交回 B。

### 11.3 实际命令、失败与复验

证据目录 `artifacts/a-closeout-rereview/`。最终全套由 `verify_logged_final.py` 调用原 `scripts/verify.py --all` 的 main，只包装 run 保存完整 stdout/stderr，命令、退出码和断言不变；逐命令记录为 `final-pass/commands.json` 与 `command-01`～`command-07.log`。

| 实际命令 | 结果 / 退出码 | 证据 |
| --- | --- | --- |
| `cargo test -p trace-e2e --locked --test a_review_probes --test closeout_rework -- --nocapture` | passed / 0，3+3 tests | `targeted-rust.log`；B 原 8 个分段场景 |
| `corepack pnpm -C apps/desktop exec vitest run src/views/__tests__/home.spec.ts` | passed / 0，17/17 | `home.log`；原 A 事件测试完整保留 |
| `cargo test -p trace-e2e --locked --test closeout_rework -- --nocapture` | passed / 0，3 tests/10 分段场景 | `targeted-extra-fixed.log`；A 正确摘要对照 |
| `cargo build --workspace --locked`；`cargo test -p trace-e2e --locked --test full_loop -- --nocapture` | passed / 0 | `fixture-build.log`、`full-loop-fixed.log`；F-017 修复后 1/1 |
| 原 `python scripts/verify.py --all` 的全套入口，附完整日志包装 | passed / 0，desktop/offline/recovery/resources/site 5/5 | `verify-fixed.log`、`final-pass/commands.json`；桌面 typecheck/Vitest 32/32/构建，Rust 全工作区、故障/恢复/进程/站点套件 |
| `cargo fmt --all -- --check` | passed / 0 | `fmt.log` |
| `cargo clippy --workspace --locked --all-targets -- -D warnings` | passed / 0 | `clippy.log` |
| `python scripts/check_docs.py`、`python scripts/check_publication.py`、`python -m unittest discover -s scripts/tests` | passed / 0；54 份文档、548 个本地链接、0 错误，公开检查零发现，5 个治理反例通过 | `docs.log`、`publication.log`、`script-tests.log` |
| 包全文件/源码输入核对及隔离 daemon 启停 | passed / 0 | `package.log`、`package-review.json` |
| `git diff --check`、`git diff --cached --check` | passed / 0（明确路径暂存后再核对） | `diff-check.log` |
| 原历史/断言保留、初始化历史公开检查 | passed / 0 | `preservation-final.log`、`history-publication.log`；待推送初始化两提交共 68 文件检查零发现 |

保留实际失败：首次 `verify.log` 退出 1，offline/recovery failed，原脚本输出截断，不编造其缺失正文。带完整日志的第二次 `verify-final.log` 退出 1，offline 已通过而 recovery 的 full_loop 失败；`command-05.log` 明确断言 `second resource failure must leave partial visible; attempts=1`，定位 F-017。单独诊断 `full-loop-diagnostic.log` 1/1 曾通过，不能用偶发绿色否认该失败。固定故障目标后，定向、最终全工作区与 recovery 再次运行均通过。

A 测试准备失败另保留：正向摘要对照初次误用 e2e 未声明的 sha2，`targeted-extra-final.log` 编译退出 101；改为用 Python hashlib 计算的固定样本摘要，无新增依赖。保留检查脚本初次按本机默认编码读取 manifest 失败（`preservation.log`），改 UTF-8 后通过。上述准备失败没有当作产品缺陷，未删除/跳过业务断言。

### 11.4 包、保留与限制

A 独立核对 -4 包：19 个校验成员+清单自身，共 199,172,866 字节；SHA256SUMS.txt 为 `7bcf580c7585bb711834e6abe9de0b7994381a5e6979b3a6e1fc3d56b53f097b`，manifest 为 `9824c9d566816b85c71619e9429862416a2f88b10405cba985f942591f4d442e`。113 个原构建输入的文件集合/摘要匹配最终运行源码，总摘要 `ad7badac695e492a960f06da88d870280dd149557b2d17ddd2fb274912363158` 重新计算匹配。A 本次只增加 e2e 验证与 Git/文档配置，未修改这 113 个输入或运行代码，无需重打包。新增 GitHub 配置不属于旧包输入。

manifest 的 `f03e307 + source_dirty=true` 如实保留，不能把该基点本身当作修复后的代码，也不声称可重现二进制构建。包内 daemon 用 A 新建空合成 Profile start/status/doctor/shutdown 与进程退出均 0，媒体来源 packaged、锁定版本、无测试端点。B 原验收档案只读核对 2 entries/2 objects、0 subscriptions/credentials，未启动或改写。

原 A Rust 3 反例文件、Vue A 事件测试、旧 B 报告、报告 §5/§9、原审查历史段落经内容比较保留；F-001、已核实的 F-002/P0、F-003～006、F-009～014 不重建。蓝白主页/管理入口与本地参考隔离保持。真实站点/登录/下载/风控/真实无声样本、原生 Windows IME/缩放/解码播放/外部程序、峰值 RSS/真实大档案目标、Linux/安装器/签名/二进制发布均 not-run。站点无可靠校验器仍安全重下，durl 不承诺无缝，质量无目标仍 unknown。

<a id="a-to-b-rereview-prompt"></a>
### 11.5 A→B 集中反馈 prompt

```text
请作为 Agent B 接收 M0 集中复审结论。仓库分支和最新完整 SHA 先用 git status -sb、git log -1 --format=%H 与 origin/main 核对，保护用户后续修改；按 AGENTS 阅读索引、状态、唯一计划 TRACE-CLOSEOUT-02 r1、有效决定、事项、USER-ACTIONS 和本地 reference。
本次被审 B 为 33cfa4cb05a947691a94c4bc78f8e3120cb67e44，父提交 f03e307d4ca53d0f7682e1d8929d2cd4615be493；用户后续授权从 162edf2066e6f6b435a3481976eb5ffc1d0f1601 起归并为一个 M0 提交，原 SHA 只作历史证据。本次 A 报告见 docs/delivery/stage-report.md §11～12，F 正本仍是 docs/evidence/2026-10-01-m0-m1a-review.md。
F-007/F-008/F-015/F-016 已由 A 实际核实关闭，没有本批开发返工。A 增加正确逐段/有序整组摘要及旧协议摘要正向对照；F-017 夹具故障目标不固定已由 A 限定到单视频条目音频，保留 full_loop 全部断言。最终五套件 5/5，原 A Rust 3/3、B 扩展 3/3、主页17/17与严格静态门禁通过。保留原失败、a_review_probes.rs、A Vue 事件测试、F-001、旧 B §12、报告 §5/§9、已关闭 F 与本地参考隔离。
本地 -4 包的113个原输入、全文件摘要及隔离 daemon smoke 已核对；A 未改运行代码，不重打包。SHA256SUMS.txt 为 7bcf580c7585bb711834e6abe9de0b7994381a5e6979b3a6e1fc3d56b53f097b。产品/真实站点/原生体验/资源测量仍 not-run，当前用户动作只维护 USER-ACTIONS，Q-004 未关闭。
正式阶段统一 M0、M1、M2…，M=Milestone。后续每阶段一个分支、一个提交、一个 PR；push 前阶段内部全部 amend/归并，DCO签名。未经用户明确允许不得 push 或创建 PR，合并/发布也须对应授权；本次 M0 首次推送授权不延伸到下一阶段。main 后续受 PR/讨论解决/squash/禁强推删除/治理CI保护，不绕过。
本次无需重复修复已闭合范围，也不要自行开始生产扩展。下一阶段 M1 等待 A 的唯一计划；用户反馈若暴露代码失败，再集中记录与修复。不要扩展抖音生产/直播/探索/远程，不启动子 Agent 或跨聊天发送。完成下一次获授权工作后给出可复制 B→A prompt。
```

## 12. M0 历史归并与 GitHub 保护

用户在复审中追加 DEC-017～019：指定远端 chialecode/trace，将 `162edf2066e6f6b435a3481976eb5ffc1d0f1601` 起的六个本批历史提交与当前 A 收尾归并为 M0；正式阶段使用 M0/M1/M2…（Milestone）。这替代本批原先禁止归并独立 A/B 提交的限制，保持初始化两提交，父提交为 `7027addebe6f8769118837569bba377a4a80e900`。原 B/A SHA 与失败日志保持历史含义，不把归并当成重新验收。

开工 GitHub 核对：目标是空公开仓库，无分支、无 ruleset；当前用户具备 admin 权限。已关联 origin。归并前用本地 `backup/m0-pre-squash` 保存含 A 收尾的原历史；只推送归并后的 main，不上传备份分支/参考/样本/包。最终阶段 SHA 用 `git log -1 --format=%H main` 与 `git ls-remote origin refs/heads/main` 核对；本地发布收尾日志保存具体结果，避免提交正文自引用。

[Git 正本](../dev-rules/repo-and-environment.md#git-delivery)已按用户要求同步：阶段单提交/单 PR、DCO、明确授权、首次空仓初始化与后续 main PR 集成、只允许 squash、全部讨论解决、禁强推/删除、无常驻 bypass。未来未经明确允许不得 push/创建 PR/合并；本次没有创建 PR 或发布软件包。

`.github/` 保存 TRACE 适用设置与固定 SHA 的治理 workflow。CI 仅做 Python 文档/公开内容/5 个治理反例，Ubuntu 的成功不代替 Windows 产品门禁。执行顺序为本地门禁→归并/首次获准推送→手动首次 CI 实跑→成功后启用 main 必需检查和无绕过保护→回读实际配置。配置文件只是期望值，实际启用/CI URL/最终远端 SHA 以 `artifacts/a-closeout-rereview/github-closeout.json` 的 API 回读与用户最终交接为准；未成功的外部动作不得据此文字视为完成。
