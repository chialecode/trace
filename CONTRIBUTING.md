# 贡献与开发指南

TRACE 已有可运行的 Windows 桌面应用，产品验收尚未完成。AI Agent 与程序员遵循同一套需求、契约和验收标准：先读 [AGENTS](AGENTS.md)，再查[文档索引](docs/README.md)和[当前状态](docs/delivery/status.md)；首次定位项目看[仓库与环境](docs/dev-rules/repo-and-environment.md)。

## 完成一次变更

1. 找到本轮唯一计划、有效决定和相关 FR/NFR/AC/INV。局部修复在任务说明中写清即可，无需为每处修改创建提案；跨模块或公共契约调整按[开发流程](docs/dev-rules/development-workflow.md)补方案。仍待用户决定的事见 [USER-ACTIONS](USER-ACTIONS.md)，不把评审稿中的细节当作用户已经批准。
2. 修改通用能力前按[开源规则](docs/dev-rules/open-source-first.md)调查；网站适配按[站点规则](docs/dev-rules/site-adapters.md)处理。
3. 按 [A/B 工作流](docs/dev-rules/development-workflow.md#agent-handoff)实现正常与失败路径，同步有事实变化的正本、需求映射与证据。审查期间新增的需求修订同一计划，保留原交付证据的版本与失败，不以新要求追溯判旧实现失败。
4. 按[质量门禁](docs/dev-rules/quality-gates.md)验证，按 [REVIEW](REVIEW.md) 自审，再集中交接。模拟源或夹具通过不代表网站可用。
5. 按 [Git 规则](docs/dev-rules/repo-and-environment.md#git-delivery)，一个正式阶段（M 加整数编号，M 表示 Milestone）使用一个分支、一个提交：push 前阶段内的实现、返工、A 审查及修复全部 amend 进该提交，消息描述最终结果；每个提交带 DCO 签名（`git commit -s`，使用提交者的 Git 身份）。已推送提交默认追加修复，改写需明确授权。获明确 PR 授权后使用 [PR 模板](.github/PULL_REQUEST_TEMPLATE.md)，在同一 PR 集中处理反馈；本地模板存在不表示已经创建 PR。

本项目采用[连续执行、集中审批](docs/dev-rules/development-workflow.md#continuous-execution)：本地实现、检查、修复和必要的 Git 准备由 Agent 连续处理，不在工作包之间等待“继续”。默认分工是 A 制定计划、B 执行交付、A 集中审查；A 直接修复可确定的局部问题，较大返工一次性交回 B；A 审查通过后向用户收尾，不回交 B 复审。需要用户处理的决定、人工检查、授权和配置只写进 [USER-ACTIONS](USER-ACTIONS.md)，与阶段解耦、可顺延；push、创建 PR、合并、发布各需对应的明确授权。

## 环境与命令

前置与首次准备见[仓库与环境](docs/dev-rules/repo-and-environment.md#首次准备与清理环境复跑)，实际版本与完整命令见[同页](docs/dev-rules/repo-and-environment.md#实际版本与命令)。在仓库根执行：

| 命令 | 作用 |
| --- | --- |
| `python scripts/check_docs.py` | 文档登记、链接、锚点、编号与需求映射 |
| `python scripts/check_publication.py` | 工作树与暂存内容的路径、凭据和私有文件检查 |
| `python -m unittest discover -s scripts/tests` | 证据工具、门禁与治理反例 |
| `python scripts/quick_verify.py --suite ui\|backend\|evidence\|structure\|all` | 日常分层快验，标注 `QUICK ONLY`，不是阶段证据 |
| `python scripts/verify.py --all` | 六套件验证（offline、desktop、recovery、site、resources、structure） |
| `python scripts/stage.py test\|package\|bench\|report\|check` | 阶段证据的唯一入口；输出在 `artifacts/evidence-runs/<跑次>/` |
| `corepack pnpm -C apps/desktop run dev` | 本地界面预览，使用明确标记的示例数据 |

运行输出只写 Git 忽略的生成目录，去留按[生成物约定](docs/dev-rules/repo-and-environment.md#generated-outputs)；阶段切换按[阶段开始的整理](docs/governance/documentation-policy.md#stage-cleanup)盘点与提炼，不把用户 Profile、Cookie 或本地参考当缓存清理。提交前检查工作树和暂存版本，运行文档、公开检查及相关产品门禁。

## 本地资料与真实站点

本地参考、未跟踪文件和临时产物按[专用规则](docs/dev-rules/local-reference-and-files.md)发现与隔离；新文档补[登记表](docs/governance/document-registry.json)与导航，沿用稳定编号。真实站点测试先按[本地凭证规则](docs/dev-rules/data-and-security.md#local-site-credentials)准备；Cookie 等必要输入缺失时只停止对应实测，在 USER-ACTIONS 给出可操作的步骤，继续合成验证和开发。

## 许可与安全

项目自有源码和文档采用 [Apache-2.0](LICENSE)，依据 [DEC-027](docs/decisions/confirmed-decisions.md)；第三方声明见[许可台账](docs/delivery/dependency-licenses.md)，对外分发见 [Q-004](USER-ACTIONS.md#q-004)。保留既定的 DCO 要求，不额外引入贡献协议或 CI 义务。安全问题按 [SECURITY](SECURITY.md) 私下报告。
