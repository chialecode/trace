# 仓库地图、环境与版本

2026-09-25 建立框架前，目录只有 `docs/README.md`、`docs/requirements.md`、`docs/design.md`，无 `.git`、产品源码、Cargo/npm 工程或 CI。2026-09-26 A 轮增加治理、模板、研究与文档检查；B 轮（M0/M1a）按[唯一计划](../delivery/m0-m1a-execution-plan.md)建立 Rust workspace、Vue 前端、独立 daemon、适配器与验证/打包脚本。最新事实见[状态](../delivery/status.md)与[本轮证据](../evidence/2026-09-26-m0-m1a-delivery.md)，每次接手重新检查现场。

| 路径 | 职责 |
| --- | --- |
| 根入口文档 | 理解项目、开发、设计与审查 |
| `Cargo.toml`、`Cargo.lock`、`rust-toolchain.toml` | Rust workspace、锁定依赖、固定工具链 |
| `package.json`、`pnpm-workspace.yaml`、`pnpm-lock.yaml` | Node 工作区与锁定依赖（仅前端/工具链） |
| `crates/core/` | 领域、SQLite/迁移/FTS、对象库、任务与租约、凭证 vault、控制协议、插件宿主 |
| `apps/daemon/` | 独立后台进程（`serve` 常驻、`doctor` 诊断、`backup`/`export`/`import`） |
| `apps/desktop/`、`apps/desktop/src-tauri/` | Vue 3 前端与 Tauri 2 外壳（命令转发、`trace-media` 协议、事件泵） |
| `adapters/bilibili/` | Bilibili 站点适配器（独立进程、双向插件协议） |
| `tests/e2e/` | 合成源与夹具适配器、离线闭环、daemon 进程级故障用例 |
| `scripts/check_docs.py` | 无第三方依赖的文档检查 |
| `scripts/verify.py` | 五套件验证入口（offline/desktop/recovery/site/resources） |
| `scripts/gen_licenses.py`、`scripts/package_windows.py` | 依赖许可台账、Windows 便携包与发行自检 |
| `docs/requirements.md`、`docs/design.md` | 保持原路径的需求与技术正本 |
| `docs/governance/` | 登记和文档治理 |
| `docs/product-rules/`、`docs/dev-rules/`、`docs/design-rules/` | 专项约束 |
| `docs/decisions/`、`docs/delivery/` | 决定、问题、路线、计划与状态 |
| `docs/research/`、`docs/evidence/`、`docs/templates/` | 资料、事实和模板 |

`apps/desktop/dist/`（前端构建产物，Tauri 编译期嵌入）、`target/`、`node_modules/`、`dist/`、`profiles/` 与 Tauri 生成的 `apps/desktop/src-tauri/gen/schemas/` 为本地产物，不入库，见 `.gitignore`（`src-tauri` 另有嵌套规则）。

## 实际版本与命令

2026-09-26 在本机 Windows 11（`win32 10.0.26200`）实测；版本由 `rust-toolchain.toml`、`Cargo.lock`、`pnpm-lock.yaml` 与 `package.json` 固定，`--locked`/`--frozen-lockfile` 生效。

| 组件 | 实际版本 | 来源 |
| --- | --- | --- |
| Rust / Cargo | 1.98.1（2026-09-01），含 clippy、rustfmt | `rust-toolchain.toml` |
| Node / pnpm | Node 24.19.0；pnpm 12.6.0（经 corepack） | `package.json#packageManager` |
| TypeScript / Vue / Vite / naive-ui | 6.0.3 / 3.5.43 / 8.3.1 / 2.45.3 | `apps/desktop/package.json` |
| Tauri / tokio / rusqlite | 2.11.6 / 1.53.1 / 0.40.2（SQLite 3.53.2，FTS5 可用） | `Cargo.lock` |
| windows crate | 0.62.2 | `Cargo.lock` |
| WebView2 Runtime | 153.0.4234.48（已安装） | `trace-daemon doctor` |
| ffprobe / ffmpeg | 9.0.1-full_build（winget 安装，非包内自带） | `trace-daemon doctor`；见 OSS-004 |

在仓库根目录（Git Bash 或 PowerShell 均可）：

```powershell
# 文档检查（无第三方依赖，不联网、不写文件）
python scripts/check_docs.py

# 五套件验证入口；--suite 可选 offline/desktop/recovery/site/resources
python scripts/verify.py --all

# Rust 侧
cargo test --workspace --locked
cargo clippy --workspace --locked --all-targets -- -D warnings
cargo fmt --all -- --check

# 前端侧
corepack pnpm -C apps/desktop run typecheck
corepack pnpm -C apps/desktop run test    # vitest run
corepack pnpm -C apps/desktop run build

# 运行与诊断
cargo run -p trace-daemon -- doctor --profile <profile.json>
cargo run -p trace-daemon -- serve --profile <profile.json>
corepack pnpm -C apps/desktop run tauri dev

# 许可台账与便携包（会跑 release 构建，产物进 dist/，不发布）
python scripts/gen_licenses.py
python scripts/package_windows.py
```

`capture-*` 类临时诊断命令与本地参考目录不属于本仓库工程，不写入文档正本。安装包（NSIS/WiX）需要额外工具链，本轮只出便携包，见[本轮证据](../evidence/2026-09-26-m0-m1a-delivery.md)。

## 首次准备与清理环境复跑

```powershell
corepack enable
corepack pnpm -C apps/desktop install --frozen-lockfile
cargo fetch --locked
python scripts/fetch_media.py
```

进程级用例通过 `target/debug` 定位 `trace-daemon.exe` 与 `trace-fake-adapter.exe`，`cargo test` 不保证构建其他包的二进制。`scripts/verify.py` 的 offline/recovery 套件因此会先执行一次 `cargo build --workspace --locked` 并核对这两个文件（2026-10-01 A 复核 F-001）；单独运行 `cargo test --workspace --locked` 前需自行先构建。2026-10-01 在新机器（Windows `10.0.26300`，WebView2 152.0.4191.66，无 ffmpeg/ffprobe）按上述三条命令从空的 `target/`、`node_modules/` 开始，原脚本 offline 失败、recovery blocked；修复后在 `trace-daemon.exe` 尚不存在的状态下复跑 `verify.py --all` 5/5 通过，见[复核证据](../evidence/2026-10-01-m0-m1a-review.md)。

`scripts/verify.py` 允许在受控临时目录内创建 archive/profile，并在结束时清理；不需要管理员权限，也不写系统目录。真实运行数据默认落在 `profile.json` 指定的 `data_root`/`archive_root`（本轮均为仓库外/被忽略目录），凭证经 DPAPI 存于系统用户私有目录。跨平台（Linux/macOS）仅按阶段验证，Windows 编译与运行结果不代表跨平台支持；本轮未在 Linux 复跑。

<a id="git-delivery"></a>
## Git 与交付版本

2026-09-26 按 DEC-011 建立本地 Git，默认分支 main。首提交 `b208ad6` 只有零字节 README；第二提交保存完整 README、文档框架、研究、首轮计划及文档检查脚本。B 轮（M0/M1a 实施）在 `feat/m1a-video-archive` 分支（基点 `7027add`）上提交产品工程、测试、脚本与同步文档，未推送。完整 SHA 用 `git log` 读取，避免文档自引用；初始化/核验记录见[规划证据](../evidence/2026-09-26-m0-m1a-planning.md)，B 轮提交与验证见[交付证据](../evidence/2026-09-26-m0-m1a-delivery.md)。当时远端和 CI 尚未建立；当前远端与阶段节奏见下文 DEC-017。

2026-10-07 用户 DEC-017 指定远端 [chialecode/trace](https://github.com/chialecode/trace)，并授权将 `162edf2066e6f6b435a3481976eb5ffc1d0f1601` 及之后本批提交归并为 M0 后推送。阶段父提交是 `7027addebe6f8769118837569bba377a4a80e900`；初始化两提交保持不动。首次检查远端为空，本次在自动门禁与 A 审查收敛后初始化 main；这是空仓首次导入，后续 main 经 PR 审核集成。M0 命名表示本次 Git 范围，产品验收单独判断。

- 正式阶段使用 M0、M1、M2…，M 表示 Milestone（里程碑）。阶段标题、提交 scope 和 PR 标题包含该编号；新分支形如 `codex/m1-<目标>`，当前已存在的分支不为改名而重建。一个阶段一个分支、一个 commit、一个 PR。阶段内部不按 A/B 角色、审查轮次、代码/文档、工作包或修复拆分提交；push 前全部 amend/归并进阶段提交。中间恢复点只在必要时保存，首次 push 前归并。
- 开始检查 HEAD、工作区、upstream 与远端引用，保护其他人的文件；已有同阶段分支继续使用。新阶段默认从更新后的 main 建 `codex/<目标>`；依赖未合并阶段时记录具体基点和依赖，不混入前置阶段提交。
- 归并前用本地备份分支保存原历史；本次用 `git reset --soft 7027addebe6f8769118837569bba377a4a80e900` 归并，不改变工作树。历史失败和被测 SHA 保留在证据中；整理历史本身不是重新测试。原先独立 A/B 提交不 amend 的本批限制由 DEC-017 替代。
- 提交前按明确路径暂存，审阅差异与文件归属，运行相关门禁、文档/公开检查、治理反例和工作树/索引 diff 检查。消息采用 `type(scope): 具体结果`，正文描述最终行为、验证和限制，不写未经验收的产品完成声明。
- 新提交带 DCO `Signed-off-by`，使用当前提交者 Git 身份，以 `git commit -s` 或 `git commit --amend -s` 落实；不借用外部工程作者身份。GPG 签名不作已配置声明。
- A 技术审查收敛后集中 push 指定分支；不使用 `--all`/`--mirror`，不上传备份分支、真实档案、参考素材或 dist 包。push、PR、合并和发布分别遵守用户授权，已有授权不重复询问。
- 后续阶段 push 后创建/更新同一个 PR，说明具体行为、验证和未测边界，审核解决反馈后按授权 squash 合并，使 main 每阶段一个提交。已推送阶段默认追加集中修复，再由 PR squash 收敛；远端改写须另获明确授权并用指定旧 SHA 的 `--force-with-lease`，不能为了单提交擅自强推。
- PR 审核与必需自动检查不能由未运行的 CI 替代。本次准备并接通下述 TRACE 自有治理 CI/分支保护；bot 与自定义 hook 未接入，不复制其他工程脚本或许可。人工体验未运行继续在 USER-ACTIONS 顺延，不伪装产品验收或发布批准。

仅本仓设置 `core.autocrlf=false`、`pull.ff=only`、`fetch.prune=true`；`.gitattributes`/`.editorconfig` 管理文本。没有实际 hook 时不设置虚假的 hooksPath。Q-004 仍决定 TRACE 许可与正式分发，本次源码推送不自动授予软件许可证，也不上传本地便携包。

### main 保护、CI 与远端核验

远端配置以 GitHub 实际回读为准，本地 JSON 是可审阅的期望值：`.github/rulesets/main.json`、`.github/repository-settings.json`、`.github/actions-policy.json`。本次空仓首次导入完成后启用 main 保护；不保留 bootstrap bypass。

| 项目 | TRACE 规则 |
| --- | --- |
| main 写入 | 后续变更必须经 PR；禁止强推/删除，保持线性历史；bypass_actors 为空 |
| 审核 | 全部 review thread 必须解决；单维护者 required approvals=0，不要求自己批准自己，不强制不存在的 code owner |
| 合并 | 只允许 squash；关闭 merge/rebase/auto merge；PR 标题/正文形成提交消息，合并后删除任务分支 |
| 必需检查 | `repository-quality`，绑定 GitHub Actions App 15368；首次真实成功后才启用，不要求每次追平 main |
| CI | `.github/workflows/ci.yml`：面向 main 的 PR opened/synchronize/reopened 和手动触发；无 main push/schedule/路径过滤，10 分钟超时，同 PR 新运行取消旧运行 |
| 检查范围 | Ubuntu 24.04 执行 Python 文档、工作树/索引公开检查、5 个治理反例；不冒充 Rust/桌面/Windows 媒体/产品验收，阶段仍须本地五套件与相关门禁 |
| Actions 权限 | contents 只读、checkout 不保留凭据；仅允许固定 SHA 的 checkout，默认 token read，不能创建/批准 PR |
| 其他设置 | 不安装 bot/App，不建立自动合并、定期巡检或无效必需检查；许可仍由 Q-004 决定 |

远端操作只按明确授权，串行提交配置差异，每次变更间隔至少一秒；403/429 按错误语义停止，不重试绕过限制。检查一次实际状态，运行状态按必要进展有界查询，不频繁轮询或重复触发 CI。首次推送、CI 与保护回读的命令/结果保存于本轮 `artifacts/a-closeout-rereview/`，审查摘要见[收尾报告](../delivery/stage-report.md#a-closeout-rereview)。

## 2026-10-07 文档与界面增量

开发入口 `corepack pnpm -C apps/desktop run dev`，回环端口 5173；浏览器默认明确标记示例，不启动 daemon。Tauri 默认读真实档案，旧后台页移至 `/status`，其他管理页保留。浅色 Token 在 `src/styles/shell.css`，主页在 `src/styles/home.css`；资料入口见[当前计划](../delivery/stage-plan.md)。

新增 `scripts/check_publication.py`（工作树+索引公开检查）、`scripts/tests/test_governance.py`（隔离反例）及可选 `scripts/prepare_ui_reference.py`（需 Pillow，仅本地裁图）。`python -m unittest discover -s scripts/tests` 跑治理反例；产品构建不依赖 Pillow。

未跟踪文件、个人配置、原图与运行截图按[本地规则](local-reference-and-files.md)处理。新交付默认分支前缀 `codex/`；旧 `feat/m1a-video-archive` 保留，当时 A 在独立文档/界面分支交付；后续本批历史归并按 DEC-017。

## TRACE-CLOSEOUT-02 环境补充

2026-10-07 的工具链仍锁定上述 Rust/Node/pnpm 组合；本机 WebView2 doctor 为 154.0.4258.53。媒体资产准备命令 `python scripts/fetch_media.py` 校验根锁文件的月末 LGPL3 shared 资产到忽略的 target/media，须在包含 F-012 的全套测试前执行；不会静默依赖系统 PATH。实际结果见[B 收尾报告](../delivery/stage-report.md)。

`cargo run -p trace-e2e --bin prepare_acceptance --locked -- artifacts/closeout/acceptance-new` 只接受尚不存在的目录，创建明确标记的离线合成 Profile、12 秒 AVC/AAC、封面和缺失资源以及完整备份；不创建真实站点订阅，不进入发行包。真实验收只按 USER-ACTIONS 中的已核对包与步骤执行。
