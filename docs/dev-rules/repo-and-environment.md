# 仓库地图、环境与版本

本文说明仓库布局、实际工具版本与命令、Git 与交付规则。最新事实见[状态](../delivery/status.md)，每次接手重新检查现场（`git status --short --branch`）。媒体与外部工具资产以根目录的锁文件与 `fetch_media.py`、`fetch_media_tools.py` 为准，不依赖系统 PATH；远端 CI 目前只覆盖治理检查。

| 路径 | 职责 |
| --- | --- |
| 根入口文档 | 理解项目、开发、设计与审查 |
| `Cargo.toml`、`Cargo.lock`、`rust-toolchain.toml` | Rust workspace、锁定依赖、固定工具链 |
| `package.json`、`pnpm-workspace.yaml`、`pnpm-lock.yaml` | Node 工作区与锁定依赖（仅前端/工具链） |
| `media-backend.lock.json`、`media-tools.lock.json` | 锁定的 LGPL shared 媒体资产；锁定的 YouTube 外部工具（yt-dlp、Deno）及其许可声明 |
| `crates/contracts/`、`crates/domain/` | 控制协议与命令名、插件协议、统一元数据、规则目录等跨进程契约；领域类型与数据库版本 |
| `crates/core/` | SQLite/迁移/FTS、对象库、任务与租约、凭证 vault、插件宿主、站点网络上下文、下载位置与资源管理器、订阅与自动下载 |
| `apps/daemon/` | 独立后台进程（`start/status/shutdown/doctor/version/site-check`）；备份/导出/恢复通过 Core 控制接口与桌面持久任务执行 |
| `apps/desktop/`、`apps/desktop/src-tauri/` | Vue 3 前端与 Tauri 2 外壳（命令转发、`trace-media` 协议、事件泵） |
| `adapters/<站点>/`、`adapters/common/` | 八个站点适配器（bilibili、douyin、weibo、xiaohongshu、instagram、x、youtube、tiktok）与共用的运行时、安全文档解析和夹具工具；独立进程、双向插件协议，网络与存储由 Core 持有 |
| `tests/e2e/` | 合成源与夹具适配器、离线闭环、daemon 进程级故障用例 |
| `scripts/check_docs.py`、`check_publication.py`、`check_structure.py`、`check_generated_types.py` | 文档、公开内容、代码结构与生成类型检查 |
| `scripts/verify.py` | 六套件验证入口（offline/desktop/recovery/site/resources/structure） |
| `scripts/stage.py`、`evidence.py`、`check_package.py`、`measure.py` | 阶段证据唯一入口（test/package/bench/report/check）；包检查（内嵌 WebView）与隔离长测 |
| `scripts/real_site_check.py`、`scripts/rule_evidence.py`、`scripts/core_client.py` | 真实站点有界检查驱动（含零网络 `prepare`）、逐规则真实证据记录的校验与同步、与守护进程说话的共用客户端；配置/授权当前状态只在 USER-ACTIONS，秘密不入库 |
| `scripts/gen_licenses.py`、`scripts/package_windows.py`、`scripts/fetch_media*.py` | 依赖许可台账、Windows 便携包、媒体与外部工具资产的校验下载 |
| `scripts/test_inventory.py` | 测试名与断言数清单，重构前后对账 |
| `docs/requirements.md`、`docs/design.md` | 保持原路径的需求与技术正本 |
| `docs/governance/` | 登记和文档治理 |
| `docs/product-rules/`、`docs/dev-rules/`、`docs/design-rules/` | 专项约束 |
| `docs/decisions/`、`docs/delivery/` | 决定、问题、路线、计划与状态 |
| `docs/research/`、`docs/evidence/`、`docs/templates/` | 资料、事实和模板 |

`apps/desktop/dist/`（前端构建产物，Tauri 编译期嵌入）、`target/`、`node_modules/`、`dist/`、`artifacts/`、`profiles/` 与 Tauri 生成的 `apps/desktop/src-tauri/gen/schemas/` 为本地产物，不入库，见 `.gitignore`（`src-tauri` 另有嵌套规则）。

<a id="generated-outputs"></a>
### 生成物约定

| 位置 | 内容与生命周期 |
| --- | --- |
| `artifacts/evidence-runs/<跑次>/` | `stage.py` 每条命令的回执（`receipts/`）、原始日志、`report.json` 与基准结果（`bench/` 下的 `benchmark.json`、`resource-samples.json` 等 JSON）。失败跑次与审查用的跑次保留到本阶段 PR 合并，下一阶段开始时数值已在报告/状态里的整体删除。`bench/` 下的合成档案与数据（`archive/`、`data/`、`gui/`、`probe-*/`、`vault/`）不是证据，基准写出结果后即可删除，删后用 `python scripts/stage.py check` 复核 |
| `artifacts/evidence-runs/package-commands/` | 打包时的构建命令日志；包内 `manifest.json` 记录其 SHA-256，`stage.py check` 会复核。保留的包还在，它的日志就不删 |
| `artifacts/evidence-runs/webview-<时间>/`、`verify-commands/` | `check_package.py` 每次包检查的隔离 Profile、观察结果与截图；`verify.py` 的命令日志。随所属阶段的跑次一起保留和删除 |
| `artifacts/evidence-runs/<审查名>/` | A/B 审查时的一次性脚本、定向日志与复算结果；报告写明入口，随本阶段的跑次保留和删除 |
| `artifacts/quick-verify/<时间>/` | `quick_verify.py` 的摘要，标注 `QUICK ONLY`，不是阶段证据；看完即可删除 |
| `artifacts/<名称>-acceptance/` 等人工检查目录 | `prepare_acceptance` 生成的合成 Profile 与样本，目录名由 USER-ACTIONS 的检查项写明。检查项还在用就不动；改用新目录后，旧目录在下一阶段开始时删除 |
| `dist/trace-<版本>-windows-x64[-序号]/` | `package_windows.py` 的便携包。同一阶段只保留最新包、USER-ACTIONS 人工检查在用的包和本阶段报告写明仍要保留的包，其余删除 |
| `target/` | Cargo 构建缓存；`stage.py test` 的端到端用例从 `target/debug` 启动进程。阶段开始时 `cargo clean` 后重建 |
| `.local/site-secrets/`、`.local/site-check/` | 本机 Cookie 与真实目标；真实检查的跨跑次额度账本 `ledger.json` 与原始回执。都不删，入库的只有 [`rule-evidence.json`](../evidence/rule-evidence.json) 里脱敏后的状态与计数 |
| `profiles/`、`archives/` 与用户自己用的 Profile | 用户数据，不当缓存清理 |
| `docs/evidence/gate/report.json` | 入库：最近一次阶段门禁的公开摘要，下一次通过的 `stage.py report` 覆盖 |

一次性命令的输出写进所属跑次目录，不直接写在 `artifacts/` 根下。生成目录的路径正本是各脚本（`stage.py` 的 `RUNS`、`package_windows.py` 的输出目录、`quick_verify.py`）；清理前解析绝对路径，确认在上表的生成目录内且不是指向真实资料的 junction/symlink，不用跨目录通配或 `git clean -fdx`。

现有工具与上表还有两处差异，改造见[交付状态](../delivery/status.md#followup-loops)的 LOOP-013：`package_windows.py` 每次新建带序号的包目录、从不覆盖；`stage.py test` 默认按时间新建跑次。在改造前，按上表在阶段内手动删除被取代的包与合成数据。

## 实际版本与命令

Rust/前端版本由 `rust-toolchain.toml`、`Cargo.lock`、`pnpm-lock.yaml` 与 `package.json` 固定，`--locked`/`--frozen-lockfile` 生效。下表媒体与运行时以包 manifest/doctor 为准，不能以本机曾安装的工具替代随包资产。

| 组件 | 实际版本 | 来源 |
| --- | --- | --- |
| Rust / Cargo | 1.98.1（2026-09-01），含 clippy、rustfmt | `rust-toolchain.toml` |
| Node / pnpm | Node 24.19.0；pnpm 12.6.0（经 corepack） | `package.json#packageManager` |
| TypeScript / Vue / Vite / naive-ui | 6.0.3 / 3.5.43 / 8.3.1 / 2.45.3 | `apps/desktop/package.json` |
| Tauri / tokio / rusqlite | 2.11.6 / 1.53.1 / 0.40.2（SQLite 3.53.2，FTS5 可用） | `Cargo.lock` |
| windows crate | 0.62.2 | `Cargo.lock` |
| WebView2 Runtime | 以包检查时本机安装版本为准（doctor 输出） | 包 manifest 的 doctor 及窗口烟测 |
| ffprobe / ffmpeg | n9.0.2-17-g2a571b6068-20260930，win64 LGPL shared | `media-backend.lock.json`、包内 media 及 doctor；不用系统 PATH |
| yt-dlp / Deno | 2026.08.19（Unlicense）/ 2.9.7（MIT）；校验摘要与声明文件见锁文件 | `media-tools.lock.json`、包内 `tools/` 及其 `verified.json` |

在仓库根目录（Git Bash 或 PowerShell 均可）：

```powershell
# 文档与公开内容检查（无第三方依赖，不联网、不写文件）
python scripts/check_docs.py
python scripts/check_publication.py
python -m unittest discover -s scripts/tests

# 六套件验证入口；--suite 可选 offline/desktop/recovery/site/resources/structure
python scripts/verify.py --all

# 日常分层快验（QUICK ONLY；不回执成阶段证据）
python scripts/quick_verify.py --suite ui
python scripts/quick_verify.py --suite backend --rust-test <能力命名的用例过滤>
python scripts/quick_verify.py --suite evidence
python scripts/quick_verify.py --suite structure

# Rust 侧
cargo build --workspace --locked --bins
cargo test --workspace --locked --no-fail-fast
cargo clippy --workspace --locked --all-targets -- -D warnings
cargo fmt --all -- --check

# 前端侧（verify = prettier 检查 + vue-tsc + vitest + 生产构建）
corepack pnpm -C apps/desktop run verify

# 结构与类型
python scripts/check_structure.py
python scripts/check_generated_types.py

# 运行与诊断
cargo run -p trace-daemon -- doctor --profile <profile.json>
cargo run -p trace-daemon -- start --profile <profile.json>
cargo run -p trace-daemon -- status --profile <profile.json>
cargo run -p trace-daemon -- shutdown --profile <profile.json>
corepack pnpm -C apps/desktop run tauri dev

# 许可台账与便携包（会跑 release 构建，产物进 dist/，不发布）
python scripts/gen_licenses.py
python scripts/package_windows.py
```

`capture-*` 类临时诊断命令与本地参考目录不属于本仓库工程，不写入文档正本。安装包（NSIS/WiX）需要额外工具链，目前只出便携包。

## 首次准备与清理环境复跑

```powershell
corepack enable
corepack pnpm -C apps/desktop install --frozen-lockfile
cargo fetch --locked
python scripts/fetch_media.py
python scripts/fetch_media_tools.py
```

进程级用例通过 `target/debug` 定位 daemon、站点适配器和故障探针，`cargo test` 不保证构建其他包的二进制。`scripts/verify.py` 的 offline/recovery 套件先执行一次 `cargo build --workspace --locked` 并核对 `PREBUILD_ARTIFACTS` 中全部文件；单独运行 `cargo test --workspace --locked` 前需自行先 `cargo build --workspace --locked --bins`，改过适配器后尤其如此。

`scripts/verify.py` 允许在受控临时目录内创建 archive/profile，并在结束时清理；不需要管理员权限，也不写系统目录。真实运行数据默认落在 `profile.json` 指定的 `data_root`/`archive_root`（仓库外或被忽略目录），凭证经 DPAPI 存于系统用户私有目录。跨平台（Linux/macOS）仅按阶段验证，Windows 的编译与运行结果不代表跨平台支持。

环境经验：

- 前端依赖目录出现链接缺失（Vitest 找不到间接依赖等）时，用 `corepack pnpm -C apps/desktop install --frozen-lockfile --force` 按原锁恢复，不升级依赖或改锁文件；不能以“包曾运行过”推定开发依赖完整。
- 独立审查用的 crate 复用工作区 `target/` 时使用独立 package 名称，并逐项核对实际运行的用例名称；退出 0 本身不能证明指定新增用例已经运行。
- 长期写入基准位于虚拟卷时，同时核对工作卷及虚拟磁盘文件所在宿主卷的可用空间并在运行期间观察变化；虚拟卷声明容量不能证明宿主空间充足。中断后保留中断目录，未落盘的原始计数不得补造，恢复后先核对源码、包、Git 和旧日志，再用新目录重跑原规模与时长。
- 真实直播产品链按用户约定的有界时长（约 60 秒）录制，满足必要封片、重开、导出与解码后停止；60 分钟的合成资源基线是单独的性能证据，不是每次直播功能复验的时长。

<a id="git-delivery"></a>
## Git 与交付版本

默认分支 main，远端 [chialecode/trace](https://github.com/chialecode/trace)。main 经 PR 集成，每个阶段在 main 上留一个提交。

- 正式阶段以里程碑编号命名，标题、提交 scope 和 PR 标题包含该编号；新分支形如 `codex/<编号>-<目标>`，已存在的分支不为改名而重建。一个阶段一个分支、一个 commit、一个 PR。阶段内部不按 A/B 角色、审查轮次、代码/文档、工作包或修复拆分提交；push 前全部 amend/归并进阶段提交，提交消息在收尾时改写成对最终结果的描述。中间恢复点只在必要时保存，首次 push 前归并。
- 开始检查 HEAD、工作区、upstream 与远端引用，保护其他人的文件；已有同阶段分支继续使用。新阶段默认从更新后的 main 建分支；依赖未合并阶段时记录具体基点和依赖，不混入前置阶段提交。
- amend 前核对远端引用与实际推送记录，无 upstream 不等于未推送；已共享提交默认追加集中修复，由 PR squash 收敛，远端改写须另获明确授权并用指定旧 SHA 的 `--force-with-lease`，不能为了单提交擅自强推。
- 提交前按明确路径暂存，审阅差异与文件归属，运行相关门禁、文档/公开检查、治理反例和 `git diff --check`。消息采用 `type(scope): 具体结果`，正文描述最终行为、验证和限制，不写未经验收的产品完成声明。
- 新提交带 DCO `Signed-off-by`，使用当前提交者 Git 身份，以 `git commit -s` 或 `git commit --amend -s` 落实；不借用外部工程作者身份。GPG 签名不作已配置声明。
- A 技术审查收敛且用户明确授权该次 push 后，集中 push 指定分支；不使用 `--all`/`--mirror`，不上传备份分支、真实档案、参考素材或 dist 包。push、PR、合并和发布分别遵守用户授权。
- 报告保存被测基点与源码指纹，最终交付 SHA 从 Git 读取，不为自引用再造提交。A 通过后推荐具体授权，不再交 B 复审；人工 `not-run` 不阻止技术收尾，但不能冒充产品验收。
- PR 审核与必需自动检查不能由未运行的 CI 替代；bot 与自定义 hook 未接入。用户未授权远端动作不阻碍本地工作。

仅本仓设置 `core.autocrlf=false`、`pull.ff=only`、`fetch.prune=true`；`.gitattributes`/`.editorconfig` 管理文本，`apps/desktop/src/lib/generated/*.ts` 是生成物，不做行尾空白检查。没有实际 hook 时不设置虚假的 hooksPath。TRACE 自有源码采用 [Apache-2.0](../../LICENSE)（DEC-027）。正式分发授权见 [Q-004](../../USER-ACTIONS.md#q-004)，不自动上传本地便携包。

### main 保护、CI 与远端核验

远端配置以 GitHub 实际回读为准，本地 JSON 是可审阅的期望值：`.github/rulesets/main.json`、`.github/repository-settings.json`、`.github/actions-policy.json`。不保留 bootstrap bypass。

| 项目 | TRACE 规则 |
| --- | --- |
| main 写入 | 后续变更必须经 PR；禁止强推/删除，保持线性历史；bypass_actors 为空 |
| 审核 | 全部 review thread 必须解决；单维护者 required approvals=0，不要求自己批准自己，不强制不存在的 code owner |
| 合并 | 只允许 squash；关闭 merge/rebase/auto merge；PR 标题/正文形成提交消息，合并后删除任务分支 |
| 必需检查 | `repository-quality`，绑定 GitHub Actions App 15368 |
| CI | `.github/workflows/ci.yml`：面向 main 的 PR opened/synchronize/reopened 和手动触发；无 main push/schedule/路径过滤，10 分钟超时，同 PR 新运行取消旧运行 |
| 检查范围 | Ubuntu 24.04 执行 Python 文档、工作树/索引公开检查和 scripts/tests 自动发现的治理反例；不冒充 Rust/桌面/Windows 媒体/产品验收，阶段仍须本地六套件与相关门禁 |
| Actions 权限 | contents 只读、checkout 不保留凭据；仅允许固定 SHA 的 checkout，默认 token read，不能创建/批准 PR |
| 其他设置 | 不安装 bot/App，不建立自动合并、定期巡检或无效必需检查；项目许可按 DEC-027，发布授权按 Q-004 |

远端操作只按明确授权，串行提交配置差异，每次变更间隔至少一秒；403/429 按错误语义停止，不重试绕过限制。检查一次实际状态，运行状态按必要进展有界查询，不频繁轮询或重复触发 CI。

## 界面开发与本地资料

开发入口 `corepack pnpm -C apps/desktop run dev`，回环端口 5173；浏览器默认明确标记示例，不启动 daemon。Tauri 默认读真实档案。浅色 Token 在 `src/styles/shell.css`，主页在 `src/styles/home.css`，其余页面共用 `src/styles/pages.css`。

`scripts/check_publication.py`（工作树+索引公开检查）、`scripts/tests/test_governance.py`（隔离反例）和可选的 `scripts/prepare_ui_reference.py`（需 Pillow，仅本地裁图）；产品构建不依赖 Pillow。未跟踪文件、个人配置、原图与运行截图按[本地规则](local-reference-and-files.md)处理。

`cargo run -p trace-e2e --bin prepare_acceptance --locked -- <尚不存在的目录>` 创建明确标记的离线合成 Profile、12 秒 AVC/AAC、封面和缺失资源以及完整备份；不创建真实站点订阅，不进入发行包。真实验收只按 USER-ACTIONS 中的已核对包与步骤执行。

## 包、资源与证据入口

```powershell
# 阶段证据：回执与原始日志写到 artifacts/evidence-runs/<跑次>/，失败跑次保留
python scripts/stage.py test
python scripts/stage.py package
python scripts/stage.py bench --idle 1030 --seconds 3620 --entries 100000
python scripts/stage.py report
python scripts/stage.py check docs/evidence/gate/report.json

# 单独核对一个包
python scripts/check_package.py <package_windows 本次输出的新包目录>
```

包构建显式启用 `tauri/custom-protocol`，内嵌生产前端；清单覆盖全部文件和源码输入，包内含八个站点适配器与 `tools/`（yt-dlp、Deno、声明与 `verified.json`）。`check_package` 使用合成空档案、独立 WebView 目录和回环 CDP，窗口最小化且不激活，不发送键鼠或操作用户浏览器。测量器每 5 秒采样 Core、适配器、媒体与外部工具子进程和测试窗口的 Windows Job 累计 CPU/当前进程内存与句柄，已退出子进程 CPU 仍纳入；合成源和驱动器成本也包含在内。基线同时核对 10 万发布/50 万媒体元数据、至少 500 字正文、100 普通/20 直播目标、100ms 延迟、50 行查询和每路至少 8Mbit/s。源码、测量脚本和包须在测量期间保持不变，短测不能通过时长门禁。

便携包移除前先关闭登录后启动并完全退出，保留或备份 Profile/数据，避免默认 trace-data 跟随程序目录被误删。安装器、签名和正式发布未验证。

本地真实请求的准备入口为 `python scripts/real_site_check.py prepare`（零网络，缺 Cookie 等输入退出 2），执行为 `real_site_check.py run --site <站点> --package <包目录>`；使用步骤只维护于 [USER-ACTIONS](../../USER-ACTIONS.md)，安全规则见[本地凭证](data-and-security.md#local-site-credentials)。已有 `.env.local` 不覆盖，产品 Vault 不自动导入开发配置。
