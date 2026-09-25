# M0/M1a：交给 Agent B 的 prompt

这是[唯一计划 TRACE-M0-M1A-01 r1](m0-m1a-execution-plan.md)的转交入口，不另维护工作范围。下方路径相对仓库根；B 开始核对实际分支/版本，用户无需重复前端、平台或阶段顺序。

```text
请作为 TRACE 的 Agent B，负责实现与自检，按 docs/delivery/m0-m1a-execution-plan.md（TRACE-M0-M1A-01 r1）连续完成 P0～P9 全部可执行范围。Agent A 已完成规划与通用栈研究；不要在建好骨架、跑通模拟源或完成一个工作包后提前交回。

先读 AGENTS.md、docs/README.md、docs/delivery/status.md、有效决定、A/B 工作流和唯一计划。基线为 main 上保存完整文档及本计划的第二提交，第一提交 b208ad6 仅空 README。用 git log/status 确认完整基点 SHA 和已有修改归属，创建或复用 feat/m1a-video-archive，保留初始化历史，不重置用户文件，不把代码 amend 到文档提交。

前端固定 Vue，栈与版本依据 docs/research/2026-09-26-technology-stack.md；采用 TypeScript 6.0.3，保留 TS7/vue-tsc 不兼容证据，不跳过类型检查。遵守 docs/design/m1a-runtime-contracts.md 和 docs/design.md。M1a 先哔哩哔哩普通投稿视频，按 docs/research/bilibili-capabilities.md 独立接入；抖音能力表用于第二站和合成契约，不从下载工具支持表推导归档能力。

一次覆盖工程/锁文件、核心与持久任务/恢复、独立后台/IPC/凭证、Vue UI、中文搜索、导出恢复、Bilibili 适配、自动故障用例、自检修复、Windows 包和现场步骤、文档证据。M0 风险通过后同批继续 M1a，不逐包询问继续。真实账号/内容授权缺失仅暂停对应现场用例，完成其余工作，ACT-002/003 保留 not-run，不宣称 M1a 已验收。凭证不进聊天/Git/日志/导出，不访问未授权真实档案。

当前产品工程/测试/包未建立，现有检查仅 python scripts/check_docs.py；其他入口由你建立、实际执行、回写环境文档。A 的前端临时试验或上游案例不替代产品测试。逐 C01～C35 记录覆盖与限制，保留原失败和修复复验，不删弱断言。

持续维护 docs/evidence/m0-m1a-delivery.md：基点/分支/版本、P/C→实现→证据、命令结果、包/摘要、兼容恢复、集中事项和阶段判断。关键设计、契约、依赖许可、运行/恢复说明、状态和登记同步到文档。自检收敛后按 Git 规则保存本地交付，不逐包提交。不开子 Agent/新任务，不 push/PR/合并/发布。

结束时输出结果、真实限制、集中人工步骤，并给出可直接复制给 Agent A 的完整审查 prompt：含计划 ID/修订/路径、实际分支/基点/交付完整 SHA、报告和证据、待处理编号、必须保留实现/修复、审核与定向复验范围；要求 A 集中复核，完成后也提供给 B 的下一步 prompt。不能只给链接或说“请 A 审查”。
```
