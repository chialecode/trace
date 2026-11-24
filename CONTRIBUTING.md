# 贡献指南

AI Agent 与程序员遵循同一套规则：开工前先读 [AGENTS](AGENTS.md)，再看[文档索引](docs/README.md)与[有效决定](docs/decisions.md)。

## 完成一次改动

1. 从 [Issues](https://github.com/chialecode/trace/issues) 找到对应需求或缺陷；跨模块或改公共接口的改动，先在 issue 里写清方案。
2. 改通用能力前按[开源优先](docs/dev-rules/sources-and-safety.md#open-source)调查，并先查[本机参考索引](docs/dev-rules/workflow.md#local-reference)；网站规则按[站点规则](docs/dev-rules/sources-and-safety.md#site-rules)处理。
3. 按[每次改动的流程](docs/dev-rules/workflow.md#change-loop)推进：一个 issue 一个小 PR，分支 `<type>/<issue号>-<简短描述>`，Conventional Commits，每个提交 `git commit -s`（DCO）。
4. 按[质量门禁](docs/dev-rules/engineering.md#quality-gates)安静验证，PR 里写清验证证据；由非作者按 [REVIEW](REVIEW.md) 审查后合并。夹具或模拟源通过不代表网站可用。

## 许可、讨论与安全

自有源码和文档采用 [Apache-2.0](LICENSE)，不另签贡献协议，每次提交通过 `git commit -s` 附带 DCO（`Signed-off-by`）声明。安全问题按 [SECURITY](SECURITY.md) 私密报告。

Issues 与 Pull Request 只允许协作者创建；外部反馈、问题交流与功能建议请用 [Discussions](https://github.com/chialecode/trace/discussions)。
