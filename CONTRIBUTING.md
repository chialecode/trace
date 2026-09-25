# 贡献与开发指南

AI Agent 与程序员遵循同一套需求、契约和验收标准。先读 [AGENTS](AGENTS.md)，再查[文档索引](docs/README.md)和[当前状态](docs/delivery/status.md)。

1. 找到本轮唯一计划、有效决定和相关 FR/NFR/AC/INV。局部修复在任务中说明即可，无需为每处修改创建提案。
2. 修改通用能力前按[开源规则](docs/dev-rules/open-source-first.md)调查；网站适配按[专项规则](docs/dev-rules/site-adapters.md)处理。
3. 按[A/B 工作流](docs/dev-rules/development-workflow.md)实现正常与失败路径，同步有事实变化的正本、验收映射与证据。
4. 执行[质量门禁](docs/dev-rules/quality-gates.md)，按 [REVIEW](REVIEW.md)自审，再集中交接。模拟源通过不代表网站可用。

先检查目录是否为 Git 仓库。未初始化时，使用文件清单及 SHA-256 对应证据；未来建立 Git 后保护他人改动，按实际授权保存本地交付。没有远端和 CI 就不能宣称已推送或已通过 CI。已共享历史不擅自改写；发布、推送和合并分别核对授权。

新文档补[登记表](docs/governance/document-registry.json)与导航，沿用稳定编号。许可证、第三方声明和发布方式见[事项总表](docs/decisions/open-questions.md)；不从参考工程复制许可证、贡献者签署或特定 CI 义务。
