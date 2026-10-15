# 代码结构优化方案（CHG-20261015-code-structure）

| 项目 | 内容 |
| --- | --- |
| 状态 | draft：A 提出的方案，待用户在 [Q-005](../../USER-ACTIONS.md#q-005) 确认排期；未实施 |
| 依据 | 用户 2026-10-15 要求 A 复核 M1 并给出代码结构优化方案（[DEC-029](../decisions/confirmed-decisions.md#dec-029)） |
| 基点 | 分支 `codex/m1-two-site-archive`，HEAD `1dd1afb`（M1 阶段提交，未推送） |
| 负责 | A 规划与验收；B 实施；不改变产品行为、数据格式、插件协议 |

## 1. 结论

M1 的功能与故障守护是扎实的：5 个验证套件、251 个 Rust 测试、52 个前端用例、严格 Clippy 均通过，两站真实视频、直播和用户视频列表都已在生产二进制上跑通。问题集中在**可维护性**：业务逻辑、SQL 和协议转发堆在少数超长文件和函数里，两个站点适配器各写了一套运行时，测试与脚本按审查轮次命名，证据工具把全部文档绑定进产品指纹，生成物在本机堆积到约 150 GB。继续在这个结构上加站点和订阅规则，成本会持续上升。

建议把本方案作为下一阶段的第一个工作包，**只做行为保持的重构**，每一步保持全部门禁通过，再在新结构上开发订阅规则目录等新功能（见[路线](../delivery/roadmap-and-acceptance.md#待实现的后继范围)）。

## 2. 现状问题（2026-10-15 实测）

| 编号 | 问题 | 证据 | 影响 |
| --- | --- | --- | --- |
| S-1 | IPC 层直接写业务与 SQL | `crates/core/src/ipc/dispatch.rs::execute` 文件 1747 行，其中 `execute` 一个 match 1310 行，SQL 关键字约 135 处；`core.rs` 1966 行，`run_media` 450 行、`run_scan` 238 行 | 改一个命令要读上千行；同一查询在多处重复；无法单测业务规则 |
| S-2 | 没有数据访问层 | SQL 关键字分布在 26 个非测试文件（粗略计数：dispatch 135、db/schema 106、upgrades 90、quality 64、jobs 42、objects 39、deletion 37、feed 35…） | schema 改动要全仓搜字符串；事务边界和查询代价难以审查 |
| S-3 | 任务载荷与状态用字符串 | `job.payload.get("mode")`、`"incremental"/"history"/"deep"`、`state='waiting_plugin'` 等散落各处 | 拼错只在运行时暴露；增加扫描模式要改多处 |
| S-4 | 两个站点适配器两套运行时 | 抖音用 `adapters/common/src/runtime.rs`（有界队列 32、满队列回 RateLimited、处理 shutdown/cancel）；B 站在 `adapters/bilibili/src/main.rs` 与 `api.rs` 自写一套（无界队列、超时和 shutdown 行为不同） | 同类修复要改两遍；行为不一致；第三个站点不知道该抄哪套 |
| S-5 | 站点适配器按“大文件”而非能力拆分 | `adapters/bilibili/src/handlers.rs` 1412 行（枚举、详情、播放地址、字幕、测试混在一起）；抖音 `protocol.rs::detail_for` 217 行 | 单个视频解析、用户列表、直播三条能力没有清晰入口，难以对照站点规则逐项维护 |
| S-6 | UI 到 daemon 三层手写转发 | `apps/desktop/src/lib/api.ts`（手写 TS 类型）→ `src-tauri/src/commands.rs`（69 个 `#[tauri::command]`）→ IPC 字符串命令；全程 `serde_json::Value` | 新增命令要改三处；前后端类型可能悄悄不一致 |
| S-7 | 超长行与格式化失效 | Rust 中 872 行超过 150 字符（rustfmt 遇到长字符串/闭包放弃格式化）；`HomeView.vue` 最长一行 685 字符、32 行超过 200 字符 | 代码审查和 diff 难读；格式检查“通过”但实际未格式化 |
| S-8 | 测试按阶段/轮次命名 | 静态扫描到的 238 个 `#[test]` 函数中 147 个以 `c01_`、`f009_`、`m1_`、`r3_` 等阶段/轮次编号起名；测试文件 `m1_tests.rs`、`m1_r3_rework.rs`、`a_review_probes.rs`、`closeout_rework.rs`；测试二进制 `m1_bench_adapter` 等 | 看名字不知道守护什么行为；与治理“去阶段标签”冲突 |
| S-9 | 证据工具绑定过紧 | `scripts/stage_evidence.py` 把 `docs/` 全部文件和根 Markdown 绑进 bundle，把需求/映射/计划/契约算作包源码输入，需求数 87/68 写死 | 任何文档修改都让已验收的包和 bundle 显示“过期”；新增需求必然打破旧门禁 |
| S-10 | 生成物堆积 | `artifacts/` 约 74 GB（644 项，单个跑次最大 25 GB）、`target/` 约 72 GB、`dist/` 31 个包约 6 GB；`.local/` 有 30 余个一次性脚本 | 曾导致开发盘写满卸载（ACT-005）；真正需要的证据被淹没 |
| S-11 | 文档过度压缩 | `stage-report.md` 超过 1400 行、`status.md` 每段堆叠多轮 SHA 与计数；新读者难以找到当前结论 | 与本次同步的“文档只区分已实现/待实现、去阶段标签”规则冲突 |

## 3. 目标结构

### 3.1 Core（`crates/core`）

```text
crates/core/src/
  app/          # 用例服务：每个用户命令一个函数，负责校验、事务编排、事件
    creators.rs  accounts.rs  subscriptions.rs  acquire.rs  archive.rs
    live.rs  quality.rs  storage.rs  credentials.rs  health.rs
  store/        # 数据访问：按聚合分仓储，只有这里写 SQL
    mod.rs  migrations/  entries.rs  subscriptions.rs  jobs.rs  media.rs
    recordings.rs  credentials.rs  search.rs
  pipeline/     # 后台任务：扫描、详情、媒体、录制，各自一个模块
    scan.rs  observe.rs  media.rs  commit.rs
  jobs/         # 调度、租约、消费者（已有，改为强类型载荷）
  recording/    # 已有：FLV/HLS/分片/字幕
  net/          # transport、site_context、request_budget、pluginhost 代理
  ipc/          # 只做路由：命令名 → app 函数；无 SQL、无业务分支
```

要点：

1. **IPC 只路由。** `dispatch.rs` 变成一张表：命令名、参数类型、是否写操作、对应的 `app::` 函数。参数用 `crates/contracts` 里的强类型结构反序列化，不再 `args.get("...")`。
2. **SQL 只在 `store/`。** 每个仓储提供有名字的方法（`entries::upsert_from_scan`、`jobs::requeue_after` 等），沿用 rusqlite 与现有单写者 DB actor，不引入 ORM。查询代价守护（如直播质量查询的步数上限）随仓储方法一起测。
3. **任务载荷强类型。** `ScanMode { Incremental, History, Deep }`、`JobPayload` 枚举放进 `trace-domain`，数据库里仍存同样的 JSON（`#[serde(rename_all = "snake_case")]`），**不改 schema、不需要迁移**。
4. **拆长函数。** `run_media`、`run_scan`、`ensure_session`、`record_hls` 按步骤拆成可单测的小函数；目标：非测试函数不超过 120 行，超过 60 行需要理由。

### 3.2 站点适配器（`adapters/*`）

```text
adapters/common/src/runtime.rs   # 唯一的 stdio 运行时（有界队列、shutdown/cancel、代理请求）
adapters/<site>/src/
  main.rs        # 只有 serve(dispatch)
  dispatch.rs    # 方法名 → 能力函数
  account.rs     # 账号解析与登录探测
  list.rs        # 用户内容列表（投稿/空间视频/动态…）与游标
  detail.rs      # 单条详情与正文
  media.rs       # 清晰度候选、音轨、字幕
  live.rs        # 房间解析、开播检测、流候选、弹幕
  sign.rs        # 站点签名（WBI、A-Bogus 等），固定向量测试放旁边
  fixtures/      # 脱敏样本
```

B 站迁到 `trace_site_support::runtime::serve`，删掉自写的 `OutSink`/`PendingMap`/代理实现；两个站点的超时、队列上限、shutdown 行为由同一份代码保证。这个结构也是以后“订阅规则目录”的落点：每个能力文件对应目录里的一类可订阅内容。

### 3.3 桌面端（`apps/desktop`）

1. **类型从 Rust 生成。** 用 ts-rs 从 `crates/contracts` 的 DTO 生成 TypeScript 类型，删除 `api.ts` 里手写的同名类型；生成文件入库并加一致性检查（生成后 `git diff --exit-code`）。
2. **Tauri 命令收窄为通用转发。** 69 个逐命令转发改为一个 `daemon_call(command, args)`，命令名对照 contracts 里的白名单常量校验；仍保留只在桌面进程内的少数命令（窗口、外部打开、媒体流）。
3. **视图拆组件。** `HomeView.vue`、`CreatorsView.vue` 按区域拆成组件；Prettier 统一格式（行宽 100），不再出现几百字符的模板行。

### 3.4 测试

按能力重命名并归组，**断言不删不弱化**：

| 现在 | 之后 |
| --- | --- |
| `crates/core/src/m1_tests.rs`、`core_*_tests.rs` | 随模块放 `app/*`、`pipeline/*`、`store/*` 的 `#[cfg(test)]`，名字写守护的行为 |
| `tests/e2e/tests/m1_r3_rework.rs`、`m1_r3_transport.rs`、`a_review_probes.rs`、`closeout_*.rs` | `tests/e2e/tests/` 下按能力：`acquire_link.rs`、`scan_lists.rs`、`live_recording.rs`、`transport_limits.rs`、`recovery.rs`、`lifecycle.rs` |
| `tests/e2e/src/bin/m1_*` | `bench_adapter`、`frame_probe`、`resource_bench` 等能力名 |
| 测试函数 `c01_…`、`f009_…` | 行为名，例如 `douyin_post_list_keeps_cursor_on_empty_page`；旧名 → 新名映射写进阶段报告的整理记录 |

迁移前后用同一脚本统计测试数与断言数，数量下降必须逐条说明。

<a id="evidence-tooling"></a>
### 3.5 脚本与证据

1. `scripts/stage_evidence.py` 拆开两类输入：**产品输入**（源码、锁文件、构建配置、测试夹具）决定包和测试证据；**文档**只由 `check_docs.py` 校验，不进包指纹。需求覆盖从 `requirements-map.json` 读取当前范围，不写死数量。
2. 统一入口：`python scripts/stage.py test|package|bench|report`（替代 `measure_m1.py`、`real_chain_gate.py` 等按阶段命名的入口），运行输出写到忽略目录 `artifacts/evidence-runs/<跑次>/`，最终公开摘要只保留 `docs/evidence/gate/report.json` 一份。
3. 真实站点检查沿用 `trace-daemon site-check`（一次性、有预算、不写档案），把本阶段临时驱动脚本整理成 `scripts/real_site_check.py`，承接状态页 LOOP-001。

### 3.6 工作区整理

按[阶段开始的整理](../governance/documentation-policy.md#stage-cleanup)执行：`artifacts/` 中已提炼的跑次删除，保留仍被状态或 USER-ACTIONS 引用的失败证据与人工检查样本；`dist/` 只留人工检查在用的包和最新包；`target/` 用 `cargo clean` 重建；`.local/` 一次性脚本提炼后删除，`.local/site-secrets/`、`.local/site-check/` 中用户提供的目标与 Cookie 保留。预计释放 140 GB 以上；具体清单由下一阶段 A 计划逐项列出。

## 4. 实施顺序

每一步单独可验证，任一步失败就停在该步修复，不跨步推进。全程不改 schema、插件协议、控制协议字段和用户可见行为。

| 步骤 | 内容 | 完成判据 |
| --- | --- | --- |
| R0 | 冻结基线：记录当前测试清单与断言数、`verify.py --all`、fmt、Clippy 结果 | 基线清单入报告 |
| R1 | 适配器统一运行时（S-4），B 站迁到 `common::runtime` | 两站全部适配器测试通过；新增“队列满/关闭/超时”对两站同一组反例 |
| R2 | 适配器按能力拆文件（S-5） | 文件不超过 600 行；固定签名向量不变 |
| R3 | 强类型任务载荷与扫描模式（S-3） | 旧库 JSON 原样可读的反例；无 schema 变化 |
| R4 | 引入 `store/` 仓储，先迁 dispatch 里的 SQL（S-1/S-2） | `ipc/` 下零 SQL；查询代价守护仍通过 |
| R5 | 拆 `core.rs` 为 `pipeline/*` 与 `app/*`，拆长函数（S-1） | 非测试函数 ≤120 行；`core.rs` 只剩启动与生命周期 |
| R6 | 桌面端生成类型、通用转发、拆视图、Prettier（S-6/S-7） | 前端类型检查、52+ 用例、构建通过；生成物一致性检查接入 `verify.py` |
| R7 | 测试按能力重命名与归组（S-8） | 测试数/断言数不下降或逐条说明；旧名→新名映射完整 |
| R8 | 证据工具拆分与统一入口（S-9），工作区整理（S-10） | 改一份文档不再让包证据过期；`artifacts/` 只剩在用内容 |
| R9 | 全量门禁 + 一次有界真实站点冒烟（两站各：单视频解析、用户列表 2 页、直播探测） | 与 2026-10-15 基线结果一致 |

R1～R3 风险最低、收益直接，可以先做；R4～R5 是主体；R6～R8 可与 R4～R5 并行但不改同一文件。

## 5. 开源选型（2026-10-15 查询 GitHub REST）

| 用途 | 候选 | 维护与许可 | 结论 |
| --- | --- | --- | --- |
| Rust → TypeScript 类型 | [ts-rs](https://github.com/Aleph-Alpha/ts-rs) | MIT；v12.0.0 于 2026-01-31 发布，pushed 2026-10-02，未归档 | **推荐**：只生成类型、不绑框架，接入面最小 |
| Tauri 命令与类型一体生成 | [tauri-specta](https://github.com/specta-rs/tauri-specta) | MIT；仍为 2.0.0-rc（rc.25，2026-05-08） | 不选：长期 RC，且会与“通用转发”方案重叠 |
| SQLite 迁移 | [rusqlite_migration](https://github.com/cljoly/rusqlite_migration) | Apache-2.0；pushed 2026-10-02 | 暂不替换：现有 `db/schema.rs` 的 1～6 逐步迁移已有旧库与中断反例，替换收益小于风险；`store/migrations/` 仅搬位置 |
| 数据访问 | 不引入 ORM | — | 保持 rusqlite + 单写者 actor；仓储是本项目内部分层，不是通用能力自研 |

## 6. 不变与不做

- 不改数据库 schema、插件协议 0.1、控制协议命令名和参数形状、对象库布局、凭证存储。
- 不删除或弱化任何断言；重命名必须有映射。
- 不在重构中夹带新功能；订阅规则目录等新需求在重构完成后的新结构上做。
- 不改变真实站点请求条件（UA、Referer、签名、预算）；R9 冒烟只验证结果一致。

## 7. 风险与回退

| 风险 | 应对 |
| --- | --- |
| 拆分时改变事务边界导致中断窗口变化 | 仓储方法接收调用方的事务，不自开事务；恢复矩阵测试必须通过 |
| 适配器运行时统一后 B 站超时行为变化 | R1 先写两站共用反例再迁移；超时值保持现值 |
| 生成类型与手写调用不一致 | 生成一致性检查进 `verify.py`；前端类型检查失败即停 |
| 大面积重命名掩盖测试丢失 | R0 基线 + R7 计数对比 + 映射表 |

每一步在同一阶段分支上推进，按 Git 规则 amend 进阶段提交；需要回退时以 R0 记录的提交为基点。

## 8. 验收

- `python scripts/verify.py --all`、`cargo fmt --check`、严格 Clippy、文档/公开检查全部通过。
- `ipc/` 下零 SQL；非测试函数 ≤120 行；单文件（不含测试）≤800 行；Rust 超过 150 字符的行只剩无法换行的字面量。
- 两个适配器共用一套运行时；B 站不再有自己的 stdio/代理实现。
- 前端类型由 ts-rs 生成并有一致性检查；Tauri 逐命令转发减少到只剩桌面专有命令。
- 测试名不再带阶段/轮次标签，测试数与断言数有映射对账。
- 修改一份文档不再让包证据显示过期；`artifacts/` 体积回到在用内容。
- R9 真实站点冒烟与 2026-10-15 基线一致。
