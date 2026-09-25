# M0 → M1a 开发准备入口

更新：2026-09-26。Agent A 已完成首轮具体规划；产品尚未实施。当前唯一实施依据为 [TRACE-M0-M1A-01 r1](m0-m1a-execution-plan.md)，用户可直接把[给 B 的 prompt](m0-m1a-agent-b-prompt.md)转交。本文不再维护另一套工作包。

| 准备项 | 当前结果 | 依据及限制 |
| --- | --- | --- |
| PREP-01 现场/环境/版本 | 文档阶段、无产品工程；按用户要求建立空 README 首提交及文档第二提交 | [本轮证据](../evidence/2026-09-26-m0-m1a-planning.md)、[Git](../dev-rules/repo-and-environment.md#git-delivery)；无远端/CI |
| PREP-02 通用栈 | Vue 固定；组件、构建、Rust驱动、媒体/凭证/测试路线、精确起始版本和替换边界已选 | [比较与实测](../research/2026-09-26-technology-stack.md)；TS6 最小类型/构建/SSR通过，完整集成/媒体资产仍需B验证 |
| PREP-03 两站表 | Bilibili 先 M1a，Douyin 生产 M1b；事实/限制/未知及故障样本明确 | [Bilibili](../research/bilibili-capabilities.md)、[Douyin](../research/douyin-capabilities.md)、ADR-017；真实现场not-run |
| PREP-04 长计划/交接 | 10个连续工作包、35个定向用例，工程→M0风险→M1a→打包/修复→集中审核 | [计划](m0-m1a-execution-plan.md)、[运行契约](../design/m1a-runtime-contracts.md)、[沟通机制](../dev-rules/development-workflow.md#handoff-protocol) |

B 按唯一计划连续完成全部可执行内容，M0 内部门槛通过后同批继续 M1a，不逐工作包请 A 批准。缺真实账号/内容授权只暂停对应现场用例，仍完成其余工程/自动验证，ACT-002/003 不冒充通过。

双方完成任务后都输出可直接给对方的 prompt，用户手动转交；职责不自动启动子 Agent/新任务。事项唯一状态在[总表](../decisions/open-questions.md)，关键内容按[保留规则](../governance/documentation-policy.md#project-records)回写，不仅留聊天。
