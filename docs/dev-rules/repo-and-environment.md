# 仓库地图、环境与版本

2026-09-25 建立框架前，目录只有 `docs/README.md`、`docs/requirements.md`、`docs/design.md`，无 `.git`、产品源码、Cargo/npm 工程或 CI。本次增加治理、模板、研究与文档检查。最新事实见[状态](../delivery/status.md)，每次接手重新检查现场。

| 路径 | 职责 |
| --- | --- |
| 根入口文档 | 理解项目、开发、设计与审查 |
| `docs/requirements.md`、`docs/design.md` | 保持原路径的需求与技术正本 |
| `docs/governance/` | 登记和文档治理 |
| `docs/product-rules/`、`docs/dev-rules/`、`docs/design-rules/` | 专项约束 |
| `docs/decisions/`、`docs/delivery/` | 决定、问题、路线、计划与状态 |
| `docs/research/`、`docs/evidence/`、`docs/templates/` | 资料、事实和模板 |
| `scripts/check_docs.py` | 无第三方依赖的文档检查 |

产品[目标布局](../design.md#modules)尚待建立，不代表 `apps`、`crates`、`adapters` 已存在。

## 已有命令

在根目录用 Python 3.10+ 执行：

```powershell
python scripts/check_docs.py
```

脚本不访问网络、不安装包、不修改文件，失败返回非零。目前没有产品启动命令，不能声称已通过 Rust、Tauri 或前端测试。

## 建立工程时

B 按 A 的选型验证 Rust、Tauri 系统依赖、WebView2、前端工具链和媒体后端。记录实际版本、锁文件、准备/启动/构建/测试命令与干净环境复跑结果，更新本文。计划中的未有命令标“需建立”。Linux 无界面按阶段验证，不用 Windows 编译结果代替跨平台支持。

<a id="git-delivery"></a>
## Git 与交付版本

2026-09-26 按 DEC-011 建立本地 Git，默认分支 main。首提交 `b208ad6` 只有零字节 README；第二提交保存完整 README、文档框架、研究、首轮计划及文档检查脚本。完整 SHA 用 `git log` 读取，避免文档自引用；初始化/核验记录见[本轮证据](../evidence/2026-09-26-m0-m1a-planning.md)。远端和 CI 尚未建立。

开工核对 `git status --short --branch`、基点/归属，复用合适分支，不 reset/stash 覆盖用户修改。一个明确交付范围一个分支，内部工作包/会话/换 Agent 不另建；默认本轮首次交付一个 commit，同一未推送交付审查修复 amend，独立交付保持各自提交。原初始化两提交是用户规定的历史边界，不合并、不塞产品代码。必要大改恢复点才额外保存，不按工作包数强制提交。

消息采用 `type(scope): 具体结果`，例如 `feat(archive): persist video jobs and restore archives`。提交前审阅修改/新文件归属，明确路径暂存，检查 `git diff --cached` 与 `git diff --cached --check`，运行本批必要门禁。实际被提交内容必须与证据一致；提交保存版本，不等于阶段验收。amend 先确认目标是本轮未推送提交，保留失败/复验记录并向对方提供新 SHA；已共享/推送后默认追加集中修复，禁止擅改共享历史。

仅本仓库设置 `core.autocrlf=false`、`pull.ff=only`、`fetch.prune=true`；`.gitattributes`/`.editorconfig` 规定文本习惯。当前未建立自定义 hook，不配置不存在的 hooksPath。新 clone 按需设置本地配置；不把参考项目 GitHub ruleset、许可、hook、CI 或远端地址直接搬来。

开发任务内的常规本地分支、提交/修复可连续完成；推送、PR、合并、发布分别核对具体授权。此轮没有这些外部动作。项目自身许可证/分发方式仍见 Q-004，不因初始化添加未经确认的 LICENSE。
