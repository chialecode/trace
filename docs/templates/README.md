# 文档模板

模板规定需要保留的信息，不表示对应方案已确定、功能已实现或检查已通过。按[文档治理](../governance/documentation-policy.md)选择最小材料，已有任务或报告信息等价时直接复用，不要求每次变更填齐所有模板。

| 模板 | 适用场景 |
| --- | --- |
| [变更提案](change-proposal.md) | 新功能、跨模块行为或范围调整；局部修复可在现有任务记录 |
| [决定记录](decision-record.md) | 架构、长期依赖、数据格式、公共契约和长期治理规则 |
| [Agent A/B 交接](agent-handoff.md) | 一份计划、一份交付报告与明确审查结论 |
| [验证报告](validation-report.md) | 记录特定版本、环境、样本上的实际结果 |
| [开源评估](open-source-assessment.md) | 引入、升级、替换通用能力或判断是否必须自研 |
| [模块规格](module-spec.md) | 数据、接口、依赖、生命周期和恢复边界 |

新建实际文档时删除模板提示并填写真实路径与编号，更新登记及相关索引。用户决定/配置/授权/人工检查只在 [USER-ACTIONS](../../USER-ACTIONS.md) 维护当前状态；开发/上游事项在[总表](../decisions/open-questions.md)，非阻塞 LOOP 在[交付状态](../delivery/status.md#followup-loops)，当前 F 在同轮审查报告。模板生成的报告引用编号及证据，不维护竞争清单。模板中的空项或示例不是已登记的需求、决定或问题。
