# TRACE

TRACE = trace + record + archive + collect + explorer。围绕创作者持续收录不同平台的内容，形成可追溯、可搜索、可恢复的个人档案。

当前已完成文档框架与 Agent A 首轮规划、建立本地 Git，尚无可运行产品。前端按用户要求使用 Vue，采用 Naive UI/Vite，配合 Tauri 外壳、独立 Rust 后台、SQLite 与统一文件库；[具体选择及最小实测](docs/research/2026-09-26-technology-stack.md)不等于完整产品已验证。

首批优先支持**抖音、哔哩哔哩的视频**。先完成一个平台的“创作者与账号 → 收录 → 浏览/搜索 → 关闭 GUI 后继续 → 导出/恢复”闭环，再逐步补第二个平台、直播、质量升级和完整账号管理。系统顺序为 Windows 桌面、Linux 无界面、其他桌面系统按需验证。完整阶段定义见[路线与验收](docs/delivery/roadmap-and-acceptance.md)。

| 目的 | 入口 |
| --- | --- |
| 开始开发或让 Agent 接手 | [AGENTS](AGENTS.md)、[贡献指南](CONTRIBUTING.md) |
| 理解产品、架构与所有文档 | [文档索引](docs/README.md)、[需求](docs/requirements.md)、[技术设计](docs/design.md) |
| 本轮交给 Agent B | [M0/M1a 长计划](docs/delivery/m0-m1a-execution-plan.md)、[可复制 prompt](docs/delivery/m0-m1a-agent-b-prompt.md) |
| 规划、交付与审查 | [当前状态](docs/delivery/status.md)、[A/B 工作流](docs/dev-rules/development-workflow.md)、[REVIEW](REVIEW.md) |
| 决策和集中问题处理 | [有效决定](docs/decisions/confirmed-decisions.md)、[事项总表](docs/decisions/open-questions.md) |
| 设计界面 | [DESIGN](DESIGN.md) |

通用技术优先整合活跃开源方案，各网站下载规则单独处理。候选调研不等于已采用或已兼容，见[开源规则](docs/dev-rules/open-source-first.md)。

目前可执行的仓库检查：

```powershell
python scripts/check_docs.py
```

该命令仅验证文档登记、链接、稳定锚点与需求覆盖映射。产品安装、启动、构建和测试命令在实现后写入[环境说明](docs/dev-rules/repo-and-environment.md)。
