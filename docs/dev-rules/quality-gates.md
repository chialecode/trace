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

## 随实现启用的产品门禁

| 变更 | 必需验证 | 启用条件 |
| --- | --- | --- |
| Rust/前端代码 | 格式、类型/静态检查、相关单元与集成测试 | B 建立真实命令后写入[环境](repo-and-environment.md) |
| 存储/后台 | 故障注入、重试幂等、单写者/生命周期、隔离恢复 | M0 起按接入范围 |
| 站点 | 固定脱敏夹具、契约、受控真实来源；列覆盖未知 | 每个适配器开放前 |
| UI | 主要状态/导航/键盘自动验证及必要实机检查 | 首个 UI 交付起 |
| 开源升级 | 锁版本、许可/资产、官方最小样本与集成回归 | 每次依赖变更 |
| 产品里程碑 | 对应 FR/AC、安装/打包、备份恢复、阶段资源基线 | 申请该阶段验收前 |

已有设计中的性能数字是初始目标。资源测量必须记录设备、数据规模、负载、采样方式和所有相关进程；范围未确定前不能把目标当成达标事实。

只运行能证明风险边界的必要测试；可逆文案修改不额外编造业务测试。检查通过后，除新改动、失败或未解决疑虑外不反复扩大测试范围。不得删除/跳过/削弱有效断言来通过门禁。

## 结果和完成判断

报告命令、退出码、样本、实际观测、被测版本及局限，采用 [验证模板](../templates/validation-report.md)。当前已有本地 Git，记录基点与工作区差异；历史无 Git 证据使用当时文件哈希。

B 自检、A 技术审查、用户产品验收分别判断；`not-run` 和 `blocked` 不算 passed。局部通过只覆盖该子集。缺实现和自动测试仍由开发者完成；真实账号、体验偏好和不可自动的设备操作才进入 ACT。

证据核对区分源码、测试/夹具、构建/锁文件、文档及产物指纹。A 局部修复后保留原 B 回执和包的原指纹；需要当前结果时真实复验，不改绑旧日志、原始采样或 measure 二进制。仅文档变化可以重新核对文档，不要求无关设备长测；阶段聚合必须满足其声明的完整输入边界。必要的旧失败与新的通过并列，不能只交最后一段输出。

<a id="doc-only-changes"></a>
### 阶段聚合之后的纯文档变化

阶段聚合（`scripts/stage_evidence.py`）把 `docs/` 全部文件和根 Markdown 的摘要绑定进 bundle，并把需求、映射、计划、归档契约算作包的源码输入。因此 A 收尾之后任何文档修改都会让旧 bundle 报 `stale or incomplete documents`。规则如下：

1. 只有文档变化（不含上述被算作包输入的四份文档）时，用 `python scripts/check_docs.py`、`python scripts/check_publication.py`、`python -m unittest discover -s scripts/tests` 和 `git diff --check` 重新核对文档；旧 bundle 继续只证明它绑定的文档版本，**不改写旧 bundle 的摘要去“重新绑定”**。报告写明重跑结果、唯一的拒绝原因和未变的源码输入集合。
2. 需求、映射、计划或归档契约有变化时，属于包输入变化，按原规则需要新包或在报告里明确旧包只对应原输入。
3. 文档绑定与产品证据分离的改造见[代码结构优化方案](../design/code-structure-refactor.md#evidence-tooling)，落地前沿用本条。

当前已有治理 CI：面向 main 的 PR 和手动触发，稳定 job 为 repository-quality，运行文档、公开内容及治理反例。它不运行 Rust/桌面/媒体或阶段资源测试；实际保护见[Git 正本](repo-and-environment.md#git-delivery)。新增门禁先本地真实可运行，再接 CI，不能把配置文件当远端已生效。

## 当前增补门禁（2026-10-07）

- `python scripts/check_publication.py`：检查当前公开文件和索引；个人路径、私有文件类别、部分令牌/私钥模式，输出仅路径/规则。忽略文件不读取；图片归属仍须审阅。
- `python -m unittest discover -s scripts/tests`：隔离副本中的断链/漏登记、无效锚点/多当前计划、需求丢失、暂存泄露与误跟踪私有文件反例。
- 文档检查要求 `currentPlan` 与唯一 active execution-plan 一致；忽略 `.local/tmp/artifacts/profiles/archives` 等本地产物。
- 前端变更运行 `python scripts/verify.py --suite desktop`（类型、Vitest、构建）；UI 还检查截图、搜索/筛选/选择、空态、断连、竞态和示例不写 API。含私有画面的截图按[本地规则](local-reference-and-files.md)隔离。
- 提交前 `git diff --check` 及暂存审阅。纯前端/文档改动不机械重跑未修改 Rust 全套；核心返工按当前计划全量验证。

## 阶段生命周期检查与下一项工程工作

文档检查已校验currentStage为M加非负整数、retireAfter格式，以及超过到期阶段仍存在的过程文档；按数字比较，反例覆盖M9到M10。它不自动判断提炼质量，也不全面拒绝历史标签。M1已建立`scripts/stage_evidence.py`的run/assemble/check及`verify.py --stage-bundle`接线；r1 的源码/98个必需用例/包/正式资源/文档聚合证据见[原B汇总](../delivery/stage-report.md#final-m1-delivery)和[原公开摘要](../evidence/gate/m1-self-check.json)。本地原bundle为`artifacts/m1-final-bundle.json`；A 修改后它应拒绝当前输入，不能代表 r2 通过。r2 的 P13 已在原入口绑定TRACE-M1-01 r2/schema5、C11～C13以及全部85条需求中的66条M1适用范围；缺r2映射不能借r1记录放行，19条后继范围保持not-run。原r2包与完整运行证据见[该版本报告](../delivery/stage-report.md#b-m1-r2-delivery)，旧日志/包/原始采样不得换绑。自动门禁仍不声明A独立审查或产品验收通过。

## 原生窗口、长测与真实站点

- 自动化默认隔离 Profile、独立 WebView、回环 CDP，窗口最小化且不激活；不发送前台键鼠、不操作用户浏览器。只为烟测调整遮挡/渲染节流时限于明确测试模式，不改变普通用户窗口行为。DOM/currentTime 不能代替原生输入法、系统缩放和实际音视频体验。
- 资源基准保留原始连续样本、所有产品相关进程及采样口径、设备/负载/窗口/二进制摘要；中止、短测、并发不足不能算正式通过。发现机器繁忙时保留失败数字，说明复跑理由后再测，不能降低目标、删慢样本或把多个短窗口拼成连续长测。
- 真实站点默认不进入自动 CI。按[凭证前置](data-and-security.md#local-site-credentials)核对必要输入与具体授权；缺失/过期只暂停对应真实测试并指向 ACT-002。先运行 `python -m unittest discover -s scripts/tests -p test_site_check_preparation.py -v` 检查无网络准备及秘密边界；准备成功和合成测试均不证明账号或网站可用。


## 当前r3真实链接退出条件

DEC-028将AC-29/30列为M1技术退出条件：指定两站视频的质量选择/完整生产Core下载和两站≥720P实际录制必须有当前包证据，不能再以真实站点not-run结项。独立来源诊断、短录制、HTTP200、自动解码或合成全套均不单独满足产品链；其他原生人工事项仍按USER-ACTIONS分别维护。

现有stage_evidence已扩展到独立r3的87需求/68适用/19后继、C14及真实回执验证，保留原98和133必需用例及全部拒绝反例。实际通过状态以[当前A复核](../delivery/stage-report.md#a-m1-r3-review)为准。真实回执必须绑定当前包、源输入、Core任务/认证上下文、全部必要对象和实测规格；数据库封片时长、Core范围读取字节、导出及完整解码输入单独核对。旧r1/r2对象/包/测量不可换绑。真实请求只在本地获准范围运行，CI不消费Cookie；新源码/规范变化后仍需匹配的新包和原规模/时长的正式合成资源结果。
