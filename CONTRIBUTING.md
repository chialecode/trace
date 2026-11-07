# 贡献指南

AI Agent 与程序员遵循同一套规则：先读 [AGENTS](AGENTS.md)，再看[文档索引](docs/README.md)、[交付状态](docs/delivery/status.md)和[当前计划](docs/delivery/stage-plan.md)。

## 完成一次变更

1. 找到当前计划、有效决定和相关需求编号。局部修复在任务里写清即可；跨模块或改公共接口的变更先写进计划。
2. 改通用能力前按[开源优先](docs/dev-rules/sources-and-safety.md#open-source)调查；网站规则按[站点规则](docs/dev-rules/sources-and-safety.md#site-rules)处理。
3. 实现正常与失败路径，同步受影响的文档。
4. 按[质量门禁](docs/dev-rules/engineering.md#quality-gates)验证，按 [REVIEW](REVIEW.md) 自审后交接。夹具或模拟源通过不代表网站可用。
5. 一个阶段一个分支、一个提交（阶段编号为 M 加整数），push 前全部 amend 进这个提交，带 DCO 签名（`git commit -s`）。push、PR、合并要用户逐项授权（[Git 规则](docs/dev-rules/engineering.md#git)）。

需要用户处理的决定、人工检查、授权和配置只写进 [USER-ACTIONS](USER-ACTIONS.md)。

## 许可与安全

自有源码和文档采用 [Apache-2.0](LICENSE)；不额外引入贡献协议。安全问题按 [SECURITY](SECURITY.md) 私下报告。

外部反馈、缺陷和建议请使用 [Discussions](https://github.com/chialecode/trace/discussions)；为防止垃圾内容，仓库关闭 Issues，PR 仅限协作者创建。
