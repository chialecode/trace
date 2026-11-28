# TRACE 文档索引

先读[根入口](../README.md)和 [AGENTS](../AGENTS.md)。文档分层（DEC-028）：产品正本（需求、路线与验收、决定）→ 技术设计与界面 → 研究资料 → 开发规则；进行中的工作在 [Issues](https://github.com/chialecode/trace/issues)，需要用户本人处理的事项是 [`needs-user` issue](https://github.com/chialecode/trace/issues?q=is%3Aissue+label%3Aneeds-user)。

| 问题 | 正本 |
| --- | --- |
| 用户要什么、做到哪 | [需求](requirements.md)（FR/NFR，状态分已实现、本阶段、待实现） |
| 先做什么、做到什么算基本可用、怎样验收、上游事项与已知限制 | [路线与验收](delivery/roadmap.md)（能力范围、MVP 基线、验收场景 AC、`OSS-*`） |
| 用户决定过什么 | [有效决定](decisions.md) |
| 系统怎样工作 | 技术设计：[架构、接口与原生界面](design/architecture.md)、[数据、缓存与信息流](design/data-and-cache.md)、[订阅、检查更新与自动缓存](design/subscriptions.md)、[直播](design/live.md)、[远程版、上传与导入导出](design/remote.md)、[移动端](design/mobile.md)（计划） |
| 界面怎样做 | [界面规范](ui.md) |
| 为什么选这套技术 | [技术选型研究](research/2026-11-05-stack.md)、[媒体与存储选型](research/2026-11-05-media.md)、[订阅、弹幕与直播选型](research/2026-11-07-subscriptions-live.md)、[抖音签名与直播录制选型](research/2026-11-12-douyin.md)、[X 开源客户端与协议依赖选型](research/2026-11-22-x.md)、[失效静态图片公共 CDN 缓存回退研究](research/2026-11-25-expired-image-cdn.md)、[移动端选型](research/2026-11-26-mobile.md)、[小红书接入可行性与五站防风控伪装调研](research/2026-11-28-xiaohongshu-and-stealth.md)（2026-11-28 时点快照，非长期权威） |
| 站点怎样请求 | [哔哩哔哩协议资料](research/bilibili.md)、[抖音协议资料](research/douyin.md)（静态调研，定稿在 §11）、[微博协议资料](research/weibo.md)（静态调研，定稿在 §13）、[X 协议与官方 API 资料](research/x.md)（静态调研，定稿在 §15） |
| 怎样提需求、开发、审查、合并与每周维护 | [开发流程](dev-rules/workflow.md)、[审查口径](../REVIEW.md)、[教训](../LESSONS.md) |
| 环境、命令、门禁、Git、打包与发布、生成物 | [工程规则](dev-rules/engineering.md) |
| 选型、站点规则、凭证、真实站点请求、公开内容 | [来源与安全](dev-rules/sources-and-safety.md)、[真实站点请求](dev-rules/workflow.md#site-requests) |
| 需要用户做什么 | [`needs-user` issue](https://github.com/chialecode/trace/issues?q=is%3Aissue+label%3Aneeds-user) |
