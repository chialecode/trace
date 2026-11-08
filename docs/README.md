# TRACE 文档索引

先读[根入口](../README.md)和 [AGENTS](../AGENTS.md)。文档分层（DEC-028）：产品正本（需求、路线与验收、决定）→ 技术设计与界面 → 研究资料 → 交付（状态、计划、报告）→ 开发规则与模板；需要用户做的事只在根目录的 USER-ACTIONS。

| 问题 | 正本 |
| --- | --- |
| 用户要什么 | [需求](requirements.md)（FR/NFR，状态分已实现、本阶段、待实现） |
| 先做什么、做到什么算基本可用、怎样验收 | [路线与验收](delivery/roadmap.md)（能力范围、MVP 基线、验收场景 AC） |
| 用户决定过什么 | [有效决定](decisions.md) |
| 系统怎样工作 | 技术设计：[架构、接口与原生界面](design/architecture.md)、[数据、缓存与信息流](design/data-and-cache.md)、[订阅、检查更新与自动缓存](design/subscriptions.md)、[直播](design/live.md) |
| 界面怎样做 | [界面规范](ui.md) |
| 为什么选这套技术 | [技术选型研究](research/2026-11-05-stack.md)、[媒体与存储选型](research/2026-11-05-media.md)、[订阅、弹幕与直播选型](research/2026-11-07-subscriptions-live.md) |
| 哔哩哔哩怎样请求 | [哔哩哔哩协议资料](research/bilibili.md) |
| 现在做到哪 | [交付状态](delivery/status.md)（已实现能力、限制、LOOP、开发与上游事项） |
| 本阶段怎么做 | [当前计划](delivery/stage-plan.md)（含给 B 的 prompt）、[阶段报告](delivery/stage-report.md) |
| 怎样开发、交接、整理文档、用本机参考 | [开发流程](dev-rules/workflow.md)、[交接模板](templates/agent-handoff.md)、[审查口径](../REVIEW.md) |
| 环境、命令、门禁、Git、生成物 | [工程规则](dev-rules/engineering.md) |
| 选型、站点规则、凭证、公开内容 | [来源与安全](dev-rules/sources-and-safety.md) |
| 需要用户做什么 | [USER-ACTIONS](../USER-ACTIONS.md) |
