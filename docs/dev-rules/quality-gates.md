# 验证门禁与证据

门禁按实际改动及当前阶段执行，不把尚未存在的构建命令写成已通过，也不因 MVP 跳过数据正确性与恢复。

## 当前可执行的文档门禁

在仓库根执行 `python scripts/check_docs.py`，Python 3.10+，无第三方依赖。脚本检查：

- 仓库自有 Markdown 与登记表一一对应，路径存在、字段/状态/日期有效，superseded 有替代路径。
- Markdown 普通相对链接与引用式链接目标存在；本地 Markdown 片段能定位到标题或显式 HTML 锚点；不联网验证外链。
- 每份 Markdown 从根入口沿本地链接可达；`CLAUDE.md` 的 `@AGENTS.md` 单行引用作为专用入口处理。
- FR/NFR 定义唯一，分批映射覆盖所有需求且无多余 ID；映射中的设计锚点、AC 编号和阶段有效。
- AC/INV/ADR 定义不重复；原有需求、AC、INV、ADR 和显式需求/设计锚点没有丢失。
- UTF-8 可读、无 Unicode 替换字符、无个人机器绝对路径。

检查器只支持文档使用的常规 Markdown 链接语法；内联代码和代码块不作为链接或标题解析。复杂 HTML、语义冲突、授权真实性、外链可用性及产品行为仍需自审。变更检查器后在隔离副本验证断链、漏登记、无效锚点或漏需求确实导致非零退出，不能只跑成功路径。

## 产品门禁

| 变更 | 必需验证 | 入口 |
| --- | --- | --- |
| Rust/前端代码 | 格式、严格 Clippy、类型检查、相关单元与集成测试 | [环境](repo-and-environment.md)列出的实际命令；`python scripts/verify.py --suite offline|desktop` |
| 存储/后台 | 故障注入、重试幂等、单写者/生命周期、隔离恢复 | `python scripts/verify.py --suite recovery`、Core 恢复矩阵 |
| 站点适配器 | 固定脱敏夹具、契约、规则目录与失败分类；受控真实来源；列覆盖未知 | `python scripts/verify.py --suite site`（全部站点适配器）；真实来源见下文 |
| UI | 主要状态/导航/键盘自动验证及必要实机检查 | `python scripts/verify.py --suite desktop` |
| 开源与外部工具升级 | 锁版本、许可/资产、官方最小样本与集成回归 | `python scripts/fetch_media.py`、`python scripts/fetch_media_tools.py` 的校验；许可清单重新生成 |
| 打包与资源基线 | 包清单与 SHA256（八个适配器、媒体与外部工具资产、许可）、内嵌 WebView 烟测、原规模/时长的正式合成资源测量（含十万条资源管理器首屏与深页） | `python scripts/stage.py package|bench` |

已有设计中的性能数字是初始目标。资源测量必须记录设备、数据规模、负载、采样方式和所有相关进程；范围未确定前不能把目标当成达标事实。

只运行能证明风险边界的必要测试；可逆文案修改不额外编造业务测试。检查通过后，除新改动、失败或未解决疑虑外不反复扩大测试范围。不得删除/跳过/削弱有效断言来通过门禁。

<a id="risk-layered-validation"></a>
## 按风险分层与测试耗时

日常修改/每轮 A 审查不默认运行资源长测。先跑受影响的测试与静态检查；UI 使用 `python scripts/verify.py --suite desktop`，后台使用能力命名的定向 Rust 测试，证据工具用 `python -m unittest discover -s scripts/tests`。最终冻结再集中执行完整测试和新包检查，避免每个工作包反复全量构建；无产品变更时复核原始回执、摘要和连续采样，不重跑。

明确标记的快速入口是 `python scripts/quick_verify.py`：

- `--suite ui`：前端格式、类型与 Vitest，不构建安装包；
- `--suite backend`：Rust 格式、严格 Clippy 与 `cargo check`；可重复 `--rust-test <能力名>`，改为执行同名定向测试；
- `--suite evidence`：`scripts/tests` 的驱动、门禁与治理反例；
- `--suite structure`：代码结构、生成类型与长行检查；
- `--suite all`：以上四组串行执行。

它只写 `artifacts/quick-verify/<时间>/summary.json`，回执明确标注 `QUICK ONLY`、`completeStageEvidence=false`、`longBenchmark=false`、`realSiteRequests=0`；任一命令失败即非零并在该命令停止。快速入口不创建 `stage.py` 回执、不运行 `bench/package/report`、不读取真实站点配置，也不能被 `stage.py check` 当作完整阶段证据。每份摘要记录命令、退出码、实际耗时和输出尾部；推荐日常按改动选一组，最终冻结仍执行完整测试、匹配包与正式资源长测。

一小时测量不是“网站能否下载”或产品验收：它验证固定大库、持续并发和空闲状态的资源/延迟与 GUI 生命周期（AC-23、NFR-PERF-001～003）。现有正式入口含约 15 分钟稳定空闲和连续一小时负载，外加准备时间，所以总耗时超过一小时；功能和故障恢复反例通常只需秒至分钟。短冒烟能及早发现明显错误，但不能证明长期稳定、完整规模或通过正式阈值。

阶段内先完成实现/返工，最后冻结后才集中跑一轮必要长测。已测源码不变则直接复用；产品输入有变，旧结果继续是原输入的历史基线，不补写新摘要换绑。正式 `stage.py report/check` 仍按实际完整输入严格校验；当前没有跨不同产品输入复用的依赖域协议，不能仅凭“只改一点”冒充新源码已通过正式资源门禁。仅文档变化的处理见下文。快速入口、短测和 A 风险审查的结论须单独标记，不发布成完整阶段资源报告。

## 结果和完成判断

报告命令、退出码、样本、实际观测、被测版本及局限，采用 [验证模板](../templates/validation-report.md)。当前已有本地 Git，记录基点与工作区差异；历史无 Git 证据使用当时文件哈希。

B 自检、A 技术审查、用户产品验收分别判断；`not-run` 和 `blocked` 不算 passed。局部通过只覆盖该子集。缺实现和自动测试仍由开发者完成；真实账号、体验偏好和不可自动的设备操作才进入 ACT。

证据核对区分源码、测试/夹具、构建/锁文件、文档及产物指纹。A 局部修复后保留原 B 回执和包的原指纹；需要当前结果时真实复验，不改绑旧日志、原始采样或 measure 二进制。仅文档变化可以重新核对文档，不要求无关设备长测；阶段聚合必须满足其声明的完整输入边界。必要的旧失败与新的通过并列，不能只交最后一段输出。

<a id="doc-only-changes"></a>
### 文档变化不使产品证据过期

`scripts/stage.py` 的输入指纹只覆盖产品输入（源码、测试与夹具、构建脚本、锁文件），文档不进指纹，需求映射只决定哪些用例必须通过。因此：

1. 只有文档变化时，用 `python scripts/check_docs.py`、`python scripts/check_publication.py`、`python -m unittest discover -s scripts/tests` 和 `git diff --check` 重新核对文档；已有的包、测试回执和资源测量继续有效，不需要重新生成。
2. 产品输入变化后，旧包和跑次只证明原输入，不改写摘要去“重新绑定”；需要当前结果时重新运行，失败的旧记录与新的通过并列保留。
3. 需求映射（当前范围的必需用例）有变化时，`stage.py report` 按新映射重新计算覆盖，缺用例或用例未通过即拒绝。

当前已有治理 CI：面向 main 的 PR 和手动触发，稳定 job 为 repository-quality，运行文档、公开内容及治理反例。它不运行 Rust/桌面/媒体或资源测试；实际保护见[Git 正本](repo-and-environment.md#git-delivery)。新增门禁先本地真实可运行，再接 CI，不能把配置文件当远端已生效。

## 公开内容、治理反例与提交前检查

- `python scripts/check_publication.py`：检查当前公开文件和索引；个人路径、私有文件类别、部分令牌/私钥模式，输出仅路径/规则。忽略文件不读取；图片归属仍须审阅。
- `python -m unittest discover -s scripts/tests`：隔离副本中的断链/漏登记、无效锚点/多当前计划、需求丢失、暂存泄露与误跟踪私有文件反例。
- 文档检查要求 `currentPlan` 与唯一 active execution-plan 一致；忽略 `.local/tmp/artifacts/profiles/archives` 等本地产物。
- 前端变更运行 `python scripts/verify.py --suite desktop`（类型、Vitest、构建）；UI 还检查截图、搜索/筛选/选择、空态、断连、竞态和示例不写 API。含私有画面的截图按[本地规则](local-reference-and-files.md)隔离。
- 提交前 `git diff --check` 及暂存审阅。纯前端/文档改动不机械重跑未修改 Rust 全套；核心返工按当前计划全量验证。

## 阶段证据入口

`python scripts/stage.py test|package|bench|report|check` 是完整阶段证据的唯一入口：每条命令的回执与原始日志写到忽略目录 `artifacts/evidence-runs/<跑次>/`，失败的跑次保留；`report` 把回执、产品输入指纹、`requirements-map.json` 的当前范围和仓库的数据库版本核对后，只有全部通过才复制出公开摘要 `docs/evidence/gate/report.json`。`quick_verify.py` 与 `verify.py` 的套件输出都不是该入口的回执。完整门禁不声明 A 独立审查或产品验收通过。

文档检查另外校验登记表的 `currentStage` 格式、`retireAfter`，以及超过到期阶段仍存在的过程文档（按数字比较）；它不自动判断提炼质量。逐行拒绝 `currentStage` 以外的阶段标签（登记文档、登记字段、代码注释、测试名与配置；当前阶段的计划与报告、门禁原始证据和固定归档链接里的路径除外）尚未实现，见[交付状态](../delivery/status.md#followup-loops)的 LOOP-012。在它实现前，阶段开始时 A 用全文搜索盘点，例如 `git grep -nP "(?<![A-Za-z0-9])[Mm][0-9]+[a-z]?(?![A-Za-z0-9])"`，逐条判断是阶段标签还是同形的数据值（如 `m4s` 容器名、SVG 路径、测试夹具里的媒体 ID），结果列进整理清单。

已合并阶段的过程证据按[阶段开始的整理](../governance/documentation-policy.md#stage-cleanup)在下一阶段提炼进状态等正本后删除；`docs/evidence/gate/` 只留最近一次门禁的结果，Git 历史是归档。忽略目录里各类输出的去留见[生成物约定](repo-and-environment.md#generated-outputs)。

## 原生窗口、长测与真实站点

- 自动化默认隔离 Profile、独立 WebView、回环 CDP，窗口最小化且不激活；不发送前台键鼠、不操作用户浏览器。只为烟测调整遮挡/渲染节流时限于明确测试模式，不改变普通用户窗口行为。DOM/currentTime 不能代替原生输入法、系统缩放和实际音视频体验。
- 资源基准保留原始连续样本、所有产品相关进程及采样口径、设备/负载/窗口/二进制摘要；中止、短测、并发不足不能算正式通过。发现机器繁忙时保留失败数字，说明复跑理由后再测，不能降低目标、删慢样本或把多个短窗口拼成连续长测。
- 真实站点默认不进入自动 CI。按[凭证前置](data-and-security.md#local-site-credentials)核对必要输入与具体授权；缺失/过期只暂停对应真实测试并指向对应用户事项。先运行 `python scripts/real_site_check.py prepare`（无网络：核对目标文件、Cookie 文件格式、代理地址与剩余预算，输出里没有秘密）；准备成功和合成测试均不证明账号或网站可用。
- `python scripts/real_site_check.py run --site <站点> --package <包目录>` 是通用真实站点检查驱动：在隔离 Profile 里启动该包的 Core，经控制协议依次做账号识别、列表两页、预览勾选下载、单链接视频/图片/文字下载，以及自动下载关闭与开启各一次的订阅检查（范围固定在一个已列出作品的发布时刻，请求数有界）；增量证据按目标 `entry_subscriptions`、真实 `source_observations`、job身份和新增endpoint核对，不把失败预算 `jobs.attempts` 当成功次数。每个请求计入本机账本 `.local/site-check/ledger.json`（跨跑次累计，不会超过 USER-ACTIONS 里机器可读的授权额度），遇挑战、限流、登录失败、权限不明或预算用尽立即停止且不重试。Cookie 只经 `credential.import` 进入隔离 Profile 的 Vault，适配器看不到；原始回执只写本机 `.local/site-check/`。`status` 显示剩余预算；`record` 先按回执逐项核对包目录、`SHA256SUMS.txt`、实际成员摘要和manifest源码树，缺包、额外/缺失成员或摘要变化都失败，旧回执没有包身份时保留原归属，通过后才折叠进公开记录（只含计数与结果）。

## 规则状态与真实证据

站点规则的状态（`implemented` / `partial` / `unimplemented`）必须有证据对应，机器核对三处：

1. 适配器在 `hello` 里声明规则；只有带验证日期与范围（`.verified(date, scope)`）的规则才保持 `implemented`，否则协议层降为 `partial`。
2. [`docs/evidence/rule-evidence.json`](../evidence/rule-evidence.json) 是公开记录：每站的运行汇总（日期、请求数、媒体字节、停止原因）和每条规则的状态、验证日期与范围、各项检查的结果。`python scripts/rule_evidence.py check` 核对：订阅规则要有 `account-recognition` 与 `list-two-pages`，预览规则要有 `list-two-pages` 与 `preview-select-download`，链接规则要有 `single-link-download`，三类都必须 passed 才可 `implemented`；有 failed/stopped 检查的规则不能 `implemented`；`partial`/`unimplemented` 必须写明缺什么和已尝试的方式；请求数和媒体字节不得超过授权额度（额度读自 USER-ACTIONS，不读回执自称值）。`python scripts/rule_evidence.py sync` 按已构建的适配器重新生成状态、日期与范围，保留已记录的检查。
3. `tests/e2e` 的 `rule_status_matches_recorded_real_evidence` 把每个真实适配器二进制声明的规则与上述记录逐条比较（状态、入口、日期、范围、规则集合）；`stage.py report` 另把记录的哈希绑进报告。

独立来源诊断、短录制、HTTP 200、自动解码或合成全套都不单独满足真实产品链；真实回执必须绑定当前包、源输入、Core 任务与认证上下文、全部必要对象和实测规格。真实请求只在本地获准范围运行，CI 不消费 Cookie；源码或规范变化后仍需匹配的新包和原规模/时长的正式合成资源结果。
