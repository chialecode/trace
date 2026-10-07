# 贡献与开发指南

AI Agent 与程序员遵循同一套需求、契约和验收标准。先读 [AGENTS](AGENTS.md)，再查[文档索引](docs/README.md)和[当前状态](docs/delivery/status.md)。

1. 找到本轮唯一计划、有效决定和相关 FR/NFR/AC/INV。局部修复在任务中说明即可，无需为每处修改创建提案。
2. 修改通用能力前按[开源规则](docs/dev-rules/open-source-first.md)调查；网站适配按[专项规则](docs/dev-rules/site-adapters.md)处理。
3. 按[A/B 工作流](docs/dev-rules/development-workflow.md)实现正常与失败路径，同步有事实变化的正本、验收映射与证据。
4. 执行[质量门禁](docs/dev-rules/quality-gates.md)，按 [REVIEW](REVIEW.md)自审，再集中交接。模拟源通过不代表网站可用。

当前已有本地 Git；先核对分支、基点和工作区归属，保护他人改动，按实际授权保存本地交付。没有远端和 CI 就不能宣称已推送或已通过 CI。已共享历史不擅自改写；发布、推送和合并分别核对授权。

新文档补[登记表](docs/governance/document-registry.json)与导航，沿用稳定编号。项目自有源码和文档采用 [Apache-2.0](LICENSE)，依据 [DEC-027](docs/decisions/confirmed-decisions.md)；第三方声明见[许可台账](docs/delivery/dependency-licenses.md)，对外动作见 [Q-004](USER-ACTIONS.md#q-004)。保留既定DCO要求，不额外引入贡献协议或CI义务。

本地参考、未跟踪文件和临时产物按[专用规则](docs/dev-rules/local-reference-and-files.md)发现与隔离。用户事项只写 [USER-ACTIONS.md](USER-ACTIONS.md)；开发问题留在计划和事项总表。提交前检查工作树和暂存版本，运行文档、公开检查及相关产品门禁。

一个正式阶段使用 M 加整数编号（Milestone）。本地阶段单提交，未推送的实现、返工、A 审查及修复全部 amend；DCO 使用提交者 Git 身份。A 审查通过后向用户收尾，不回交 B 复审。用户事项独立顺延，push、创建 PR、合并、发布各需对应明确授权。阶段切换按治理执行内容盘点与提炼，禁止把用户 Profile 或参考当缓存清理。

获明确PR授权后使用[PR模板](.github/PULL_REQUEST_TEMPLATE.md)，在同一PR集中处理反馈；本地模板存在不表示已经创建PR。

真实站点测试先按[本地凭证规则](docs/dev-rules/data-and-security.md#local-site-credentials)准备；Cookie 等必要输入缺失时只停止对应实测，在 USER-ACTIONS 给用户可操作步骤，继续合成验证和开发。审查期间新增需求修订同一计划，保留原交付证据的版本与失败，不以新要求追溯判旧实现失败。
