# TRACE

TRACE = trace + record + archive + collect + explorer。围绕创作者持续收录不同平台的内容，形成可追溯、可搜索、可恢复的个人档案。

当前工程采用Vue/Naive UI/Vite、Tauri、独立Rust后台、SQLite与统一文件库。蓝白主页和管理入口连接同一档案服务，关闭界面后后台继续运行。M1仍在集中返工：A已定位真实412/403请求条件、修复WBI，并取得两站真实视频及1080P直播诊断样本；链接选择清晰度到Core归档/录制的完整产品链及当前开发缺口尚待完成。范围、证据和限制见[当前状态](docs/delivery/status.md)与[阶段报告](docs/delivery/stage-report.md#m1-real-source-readiness)，技术选择见[选型记录](docs/research/2026-09-26-technology-stack.md)。

当前M1范围覆盖**抖音、哔哩哔哩的动态、图文、专栏/长图文、视频与直播订阅归档**，包括历史/增量、正文与必要资源、离线浏览搜索、质量保留、备份恢复及后台生命周期。两站真实权限和接口差异按各自[能力表](docs/research/site-parsing-current.md)说明。系统顺序为Windows桌面、Linux无界面、其他桌面系统按需验证；完整阶段定义见[路线与验收](docs/delivery/roadmap-and-acceptance.md)。

| 目的 | 入口 |
| --- | --- |
| 开始开发或让 Agent 接手 | [AGENTS](AGENTS.md)、[贡献指南](CONTRIBUTING.md) |
| 理解产品、架构与所有文档 | [文档索引](docs/README.md)、[需求](docs/requirements.md)、[技术设计](docs/design.md) |
| 本轮计划与交接 | [当前连续计划](docs/delivery/stage-plan.md)、[当前实施/返工入口](docs/delivery/stage-report.md#a-to-b-m1-rework) |
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

项目自有源码和文档采用 [Apache License 2.0](LICENSE)。第三方依赖、适配器内已采用源码与媒体资产继续按各自[许可和通知](docs/delivery/dependency-licenses.md)处理；发布动作按现有授权边界执行。
