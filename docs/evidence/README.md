# 验证与交付证据

证据只证明特定版本、环境和样本的观测，不自动成为产品约束。当前完成状态只在[交付状态](../delivery/status.md)汇总，格式见[验证模板](../templates/validation-report.md)。

## 当前证据放在哪里

| 内容 | 位置 | 入库 |
| --- | --- | --- |
| 命令回执、测试日志、测量原始样本、包检查输出 | 忽略目录 `artifacts/evidence-runs/<跑次>/`（每次运行带命令、退出码与输入指纹） | 否 |
| 最近一次阶段门禁的公开机器摘要 | `docs/evidence/gate/report.json`，由 `python scripts/stage.py report` 从通过的回执生成；失败、拒绝与未跑项原样记录 | 是 |
| 本阶段交付报告：每个工作包的位置、命令与退出码、失败与复测、未执行边界、整理记录与规则状态 | [阶段报告](../delivery/stage-report.md) | 是 |
| 真实站点检查的一次性预算记录 | 忽略目录 `.local/site-check/`；入库的只有脱敏后的规则状态与结论 | 否 |
| 站点规则的状态与真实检查结果 | [`rule-evidence.json`](rule-evidence.json)：每站一次运行的请求数与媒体字节、每条规则的状态、验证日期与范围、各项检查的结果；由 `real_site_check.py record` 与 `rule_evidence.py sync` 更新，`rule_evidence.py check` 与 `stage.py report` 核对 | 是 |

脱敏要求：不放 Cookie、签名 URL、真实账号名、用户档案或带私人信息的截图；站点内容只写合成示例或字段形状。保持失败历史与复验版本，不用重跑结果覆盖原失败记录。

## 已合并阶段的证据

已合并阶段的计划、报告与轮次证据不留在工作树，Git 历史即归档（[治理 9.2](../governance/documentation-policy.md#stage-cleanup)）。仍有效的结论已提炼进[交付状态](../delivery/status.md)、[有效决定](../decisions/confirmed-decisions.md)和各站能力表；需要回看原文时，在引用它的正本里使用指向完整提交的固定链接，本页不另列历史清单。
