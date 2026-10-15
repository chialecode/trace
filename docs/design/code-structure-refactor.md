# 代码结构（CHG-20261015-code-structure）

| 项目 | 内容 |
| --- | --- |
| 状态 | implemented：行为保持的整理已完成，结构与守护如下；不改变产品行为、数据格式、插件协议 |
| 依据 | 用户 2026-10-15 要求给出代码结构优化方案（[DEC-029](../decisions/confirmed-decisions.md#dec-029)）；2026-10-16 确认先做（[DEC-031](../decisions/confirmed-decisions.md#dec-031)） |
| 验证 | 整理前后用同一脚本统计测试与断言，旧名到新名有映射（[交付报告整理记录](../delivery/stage-report.md)）；门禁见[质量门禁](../dev-rules/quality-gates.md) |

## 1. 解决了什么

整理前的问题集中在可维护性：业务逻辑、SQL 和协议转发堆在少数超长文件和函数里，两个站点适配器各写一套运行时，测试与脚本按审查轮次命名，证据工具把全部文档绑进产品指纹，生成物在本机堆积到约 150 GB。

| 问题 | 现在 |
| --- | --- |
| IPC 层直接写业务与 SQL | `ipc/` 只做命令路由与幂等收据，零 SQL；每个命令族一个 `app/` 用例；SQL 只在 `store/` |
| 没有数据访问层 | `store/` 按聚合分仓储，沿用 rusqlite 与单写者 DB actor，不引入 ORM；仓储方法接收调用方的事务，不自开事务 |
| 任务载荷与状态用字符串 | `trace-domain` 里强类型 `JobType` / `ScanMode` / `ScanPayload`；库内 JSON 不变，缺 `mode` 视为增量，未知 `mode` 报错 |
| 两个适配器两套运行时 | `adapters/common` 的 `runtime` 是唯一的 stdio 运行时（有界队列、满队列回限流、shutdown 确认、取消、代理等待随请求截止时间）；所有站点共用，两站另有同一组反例 |
| 适配器按大文件而非能力拆分 | 每站按能力分文件（账号、列表、详情、媒体、直播、签名、规则、分发）；适配器文件不超过 600 行 |
| UI 到 daemon 三层手写转发 | ts-rs 从 `crates/contracts` 生成 TypeScript 类型并入库，一致性检查在 `check_structure.py`；Tauri 端是一个通用 `daemon_call`，命令名对照白名单常量 |
| 超长行与格式化失效 | Rust 超过 150 字符的行只剩无法换行的字面量；前端 Prettier（行宽 100）进 `pnpm verify` |
| 测试按阶段或轮次命名 | 按能力命名并归组；`tests/e2e/src/bin` 用能力名 |
| 证据工具绑定过紧 | `scripts/stage.py` 的产品输入只含源码、锁文件、构建配置与测试夹具；文档只由 `check_docs.py` 校验，改文档不让包证据过期；需求覆盖读自[需求映射](../delivery/requirements-map.json) |
| 生成物堆积 | `artifacts/`、`dist/`、`target/`、`.local/` 按[文档治理](../governance/documentation-policy.md#stage-cleanup)整理，只留在用内容 |
| 文档过度压缩 | 交付报告、状态页只写当前结论；历史报告用固定链接 |

## 2. 结构

### 2.1 Core（`crates/core`）

```text
crates/core/src/
  app/          # 用例服务：每个命令族一个 route，负责校验、事务编排、事件
  store/        # 数据访问：按聚合分仓储，只有这里写 SQL
  pipeline/     # 后台任务：scan、scan_page、observe、acquire、media、mux
  jobs/  recording/  objects/  upgrades/  pluginhost/  transport/  media_tools/
  library/  downloads/  explorer/   # 资源库迁移、下载目录、资源管理器
  network.rs  catalog.rs  legacy_metadata.rs
  ipc/          # 只做路由：命令名 → app 函数；无 SQL、无业务分支
```

规则：

1. IPC 只路由；参数用 `crates/contracts` 的强类型结构反序列化。
2. SQL 只在 `store/`；查询代价守护（如直播质量查询的步数上限）随仓储方法一起测。
3. 非测试函数不超过 120 行（工作区 `clippy::too_many_lines`），非测试文件不超过 800 行（`scripts/check_file_sizes.py`）。
4. 不改数据库既有迁移的语义；新表与列只追加（schema 版本见[技术设计](../design.md)）。

### 2.2 站点适配器（`adapters/*`）

```text
adapters/common/src/runtime.rs   # 唯一的 stdio 运行时
adapters/common/src/kit/         # 统一作品结构、规则声明构建、分页、网络、serve 分发、测试夹具
adapters/<site>/src/
  main.rs  dispatch.rs  rules.rs   # 只有 serve(dispatch)、方法名到能力函数、规则声明
  account.rs  list.rs  detail.rs   # 账号与登录探测、用户内容列表与游标、单条详情
  media.rs  live.rs  sign.rs       # 清晰度与字幕、房间与直播、站点签名（固定向量测试放旁边）
```

站点按能力实现订阅、预览与链接三类入口，规则声明见[多站契约](multi-site-contracts.md)。

### 2.3 桌面端（`apps/desktop`）

类型来自 ts-rs 生成物；Tauri 命令收窄为通用转发加少数桌面专有命令（窗口、外部打开、媒体流）；视图按区域拆成组件，格式由 Prettier 统一。

### 2.4 脚本与证据

1. 产品输入决定包与测试证据；文档不进包指纹。
2. 统一入口：`python scripts/stage.py test|package|bench|report|check`，输出写到忽略目录 `artifacts/evidence-runs/<跑次>/`，公开摘要只保留 `docs/evidence/gate/report.json` 一份。
3. 真实站点检查由 `scripts/real_site_check.py` 驱动，规则状态与真实证据的对应记在 `docs/evidence/rule-evidence.json`，见[质量门禁](../dev-rules/quality-gates.md)。

## 3. 开源选型（2026-10-15 查询 GitHub REST）

| 用途 | 候选 | 维护与许可 | 结论 |
| --- | --- | --- | --- |
| Rust → TypeScript 类型 | [ts-rs](https://github.com/Aleph-Alpha/ts-rs) | MIT；v12.0.0 于 2026-01-31 发布，pushed 2026-10-02，未归档 | 采用：只生成类型、不绑框架，接入面最小 |
| Tauri 命令与类型一体生成 | [tauri-specta](https://github.com/specta-rs/tauri-specta) | MIT；仍为 2.0.0-rc（rc.25，2026-05-08） | 不选：长期 RC，且与通用转发方案重叠 |
| SQLite 迁移 | [rusqlite_migration](https://github.com/cljoly/rusqlite_migration) | Apache-2.0；pushed 2026-10-02 | 暂不替换：现有 `db/schema.rs` 的逐步迁移已有旧库与中断反例，替换收益小于风险 |
| 数据访问 | 不引入 ORM | — | 保持 rusqlite + 单写者 actor；仓储是本项目内部分层，不是通用能力自研 |

## 4. 不变与不做

- 不改插件协议 0.x 的既有字段、控制协议既有命令名与参数形状、对象库布局、凭证存储。
- 不删除或弱化任何断言；重命名必须有映射。
- 不改变真实站点请求条件（UA、Referer、签名、预算）。

## 5. 风险与回退

| 风险 | 应对 |
| --- | --- |
| 拆分改变事务边界导致中断窗口变化 | 仓储方法接收调用方的事务；恢复矩阵测试必须通过 |
| 共享运行时改变某站超时行为 | 两站共用反例；超时值保持原值 |
| 生成类型与手写调用不一致 | 生成一致性检查进 `check_structure.py` 与 `verify.py`；前端类型检查失败即停 |
| 大面积重命名掩盖测试丢失 | 基线清单、数量对账脚本（`scripts/test_inventory.py`）与映射表 |
