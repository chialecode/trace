# TRACE

TRACE = trace + record + archive + collect + explorer。围绕创作者持续收录不同平台的内容，形成可追溯、可搜索、可恢复的个人档案。

当前已有可运行的 Vue/Tauri 桌面工程、独立 Rust 后台和本地 Git，M0/M1a 仍需修复上轮审查发现，真实站点尚未验收。2026-10-07 增加蓝白三栏主页、本地示例预览、文档/参考治理及下一轮收敛计划。前端按用户要求使用 Vue，采用 Naive UI/Vite，配合 Tauri 外壳、独立 Rust 后台、SQLite 与统一文件库；[具体选择及最小实测](docs/research/2026-09-26-technology-stack.md)不等于完整产品已验证。

首批优先支持**抖音、哔哩哔哩的视频**。先完成一个平台的“创作者与账号 → 收录 → 浏览/搜索 → 关闭 GUI 后继续 → 导出/恢复”闭环，再逐步补第二个平台、直播、质量升级和完整账号管理。系统顺序为 Windows 桌面、Linux 无界面、其他桌面系统按需验证。完整阶段定义见[路线与验收](docs/delivery/roadmap-and-acceptance.md)。

| 目的 | 入口 |
| --- | --- |
| 开始开发或让 Agent 接手 | [AGENTS](AGENTS.md)、[贡献指南](CONTRIBUTING.md) |
| 理解产品、架构与所有文档 | [文档索引](docs/README.md)、[需求](docs/requirements.md)、[技术设计](docs/design.md) |
| 本轮交给 Agent B | [当前连续计划](docs/delivery/stage-plan.md)、[可复制 prompt](docs/delivery/stage-plan.md#agent-b-prompt) |
| 规划、交付与审查 | [当前状态](docs/delivery/status.md)、[A/B 工作流](docs/dev-rules/development-workflow.md)、[REVIEW](REVIEW.md) |
| 用户现在需要做什么 | [USER-ACTIONS](USER-ACTIONS.md) |
| 本机参考和未跟踪文件 | [本地参考与文件规则](docs/dev-rules/local-reference-and-files.md) |
| 决策和集中问题处理 | [有效决定](docs/decisions/confirmed-decisions.md)、[事项总表](docs/decisions/open-questions.md) |
| 设计界面 | [DESIGN](DESIGN.md) |

通用技术优先整合活跃开源方案，各网站下载规则单独处理。候选调研不等于已采用或已兼容，见[开源规则](docs/dev-rules/open-source-first.md)。

目前可执行的仓库检查：

```powershell
python scripts/check_docs.py
python scripts/check_publication.py
python -m unittest discover -s scripts/tests
corepack pnpm -C apps/desktop run dev
```

文档和公开检查不代表产品测试。最后一条启动[本地界面预览](http://127.0.0.1:5173/)，使用明确标记的示例数据；完整准备、产品验证和桌面启动见[环境说明](docs/dev-rules/repo-and-environment.md)。用户操作步骤只在 [USER-ACTIONS](USER-ACTIONS.md) 维护。
