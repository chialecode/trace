# TRACE 文档索引

先读[根入口](../README.md)与 [AGENTS](../AGENTS.md)。实际完成状态在[交付状态](delivery/status.md)，所有文档元数据在[文档登记](governance/document-registry.json)。

## 按问题找正本

| 问题 | 正本 | 职责 |
| --- | --- | --- |
| 如何维护与解决冲突 | [治理](governance/documentation-policy.md) | 权威、状态、负责人、变更和生命周期 |
| 用户需要什么 | [需求](requirements.md)、[产品原则](product-rules/core-product-principles.md) | FR/NFR/AC 与长期产品边界 |
| 系统怎样工作 | [技术设计](design.md) | INV、进程、领域、文件、协议与恢复 |
| 已确认及待处理什么 | [决定目录](decisions/README.md)、[有效决定](decisions/confirmed-decisions.md)、[事项总表](decisions/open-questions.md) | 决定来源和 Q/OSS/ACT/LOOP |
| 怎样开发与交接 | [开发规则](dev-rules/README.md)、[A/B 工作流](dev-rules/development-workflow.md) | 后续 A 计划审核、B 实现自检 |
| 如何选通用开源 | [开源规则](dev-rules/open-source-first.md)、[候选调研](research/open-source-candidates.md) | 资料、维护、整合和上游问题 |
| 网站如何接入 | [站点规则](dev-rules/site-adapters.md)、[多站契约](design/multi-site-contracts.md)、[社交站点研究](research/social-sites-current.md) | 八站逐站研究与真实验证，规则目录、元数据、下载位置、代理与外部工具约束 |
| UI 如何设计 | [设计索引](design-rules/README.md)、[设计正本](design-rules/DESIGN.md) | 交互状态、组件、文案与验证 |
| 先做什么、如何验收 | [路线](delivery/roadmap-and-acceptance.md)、[需求映射](delivery/requirements-map.json) | MVP 切片、完整范围与门槛 |
| 本轮如何实施与复核 | [当前唯一计划](delivery/stage-plan.md)、[A→B 实施 prompt](delivery/stage-plan.md#agent-b-prompt)、[本阶段报告](delivery/stage-report.md) | 阶段范围、工作包、反例、整理清单与交接；两者在下一阶段开始时提炼进正本后删除，已合并阶段的过程文档只在 Git 历史里 |
| 本地参考与未跟踪文件 | [本地规则](dev-rules/local-reference-and-files.md) | 索引、建议、日期比较、文件生命周期和公开检查 |
| 用户需要处理什么 | [用户待办](../USER-ACTIONS.md) | 决定、配置、授权、人工检查的唯一当前状态 |
| 本地真实站点凭证怎样准备 | [凭证规则](dev-rules/data-and-security.md#local-site-credentials)、[配置模板](../.env.example) | 无网络准备、必要输入/授权前置；用户获取步骤与当前状态只在 ACT-002 |
| 代码怎样分层 | [代码结构](design/code-structure-refactor.md) | 整理解决的问题、Core / 适配器 / 桌面端 / 脚本的目录结构与守护规则、开源选型 |
| 具体栈与站点边界 | [技术比较](research/2026-09-26-technology-stack.md)、[Bilibili](research/bilibili-capabilities.md)、[Douyin](research/douyin-capabilities.md)、[运行契约](design/windows-runtime-contracts.md) | 资料事实、选择、已测与待测分开 |
| 工程命令与版本 | [仓库与环境](dev-rules/repo-and-environment.md#实际版本与命令) | 实际工具链版本、锁定方式与准备/构建/测试/打包命令 |
| 项目与依赖许可 | [Apache-2.0](../LICENSE)、[许可台账](delivery/dependency-licenses.md) | 项目自有内容采用Apache-2.0；第三方许可及通知分别保留 |
| 双方如何沟通、保留关键内容 | [交接机制](dev-rules/development-workflow.md#handoff-protocol)、[记录职责](governance/documentation-policy.md#project-records) | 长计划、集中交付、实际交接prompt、A通过后向用户收尾 |
| 实际完成了什么 | [状态](delivery/status.md)、[同一实施/审查报告](delivery/stage-report.md)、[证据位置](evidence/README.md)、[最近一次门禁摘要](evidence/gate/report.json) | 对应版本、环境及范围的事实；门禁摘要随阶段整体替换 |
| 生成物留多久、何时清理 | [生成物约定](dev-rules/repo-and-environment.md#generated-outputs)、[阶段开始的整理](governance/documentation-policy.md#stage-cleanup) | `artifacts/`、`dist/`、`target/` 各类输出的内容与生命周期；过程文档与轮次证据的提炼与删除 |
| 怎样报告安全问题 | [SECURITY](../SECURITY.md) | 私下报告凭据泄露、越权与数据损失 |
| 新材料怎样写 | [模板](templates/README.md) | 按变更规模选择最小材料 |

## TRACE 领域定位

| 领域 | 需求 | 设计 |
| --- | --- | --- |
| 创作者与身份 | [账号](requirements.md#fr-identity) | [领域](design.md#domain) |
| 视频订阅与后台 | [订阅](requirements.md#fr-subscription)、[运行](requirements.md#fr-runtime) | [调度](design.md#scheduler)、[生命周期](design.md#architecture) |
| 文件与质量 | [归档](requirements.md#fr-archive)、[质量](requirements.md#fr-quality) | [提交](design.md#storage)、[回收](design.md#quality) |
| 凭证与插件 | [认证](requirements.md#fr-auth)、[插件](requirements.md#fr-plugin) | [凭证](design.md#credentials)、[插件](design.md#plugins) |
| 浏览与中文搜索 | [浏览](requirements.md#fr-browser) | [搜索](design.md#search) |
| 导出与恢复 | [数据](requirements.md#fr-data) | [恢复](design.md#import-export) |
| 直播与远程 | [直播](requirements.md#fr-live)、[同步](requirements.md#fr-sync) | [录制](design.md#recording)、[同步](design.md#remote-sync) |

需求 → 设计 → 分批/验收映射 → 版本化证据 → 当前状态构成闭环。文档只按已实现、本阶段、待实现三种状态描述功能，不使用以前阶段的标签（[治理 9.3](governance/documentation-policy.md#destaging)）。原有 requirements/design 路径与显式锚点保留，维护时不破坏旧引用。

## 当前阶段入口

当前阶段按[唯一计划](delivery/stage-plan.md)实施，当前状态以[交付状态](delivery/status.md)为准。站点范围、规则目录、统一元数据、自动下载与同步、预览勾选、资源库与下载位置、资源管理器、代理和外部解析工具见[多站契约](design/multi-site-contracts.md)，站点资料见[社交站点研究](research/social-sites-current.md)，抖音与哔哩哔哩的已实现字段、媒体和录制边界见[归档契约](site-archive-contracts.md)。A 规划、B 实施、A 最终审查按[工作流](dev-rules/development-workflow.md)执行；A 通过后无需 B 再审，远端动作只在 USER-ACTIONS 授权后进行。
