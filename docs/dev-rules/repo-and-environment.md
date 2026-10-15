# 仓库地图、环境与版本

> 当前现场（2026-10-14）：main/origin/main为960984c34487244f31f1238dffc2bd9faaa292e7，工作分支codex/m1-two-site-archive，唯一计划TRACE-M1-01 r3。A被审B为692ada9f11f762e959352762e0a295c13be659f7，当前局部修复与复核记录继续归并同一未推送阶段提交；[A当前证据](../delivery/stage-report.md#a-m1-r3-review)分别保留原包和新m1-r3-12的输入，四项当前包真实链与最终聚合已通过，当前技术结论见报告§18.8，原生验收not-run。媒体资产以根锁文件与fetch_media.py为准，不依赖系统PATH；远端CI仍仅治理。

2026-09-25 建立框架前，目录只有 `docs/README.md`、`docs/requirements.md`、`docs/design.md`，无 `.git`、产品源码、Cargo/npm 工程或 CI。2026-09-26 A 轮增加治理、模板、研究与文档检查；B 轮（M0/M1a）按[唯一计划](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/delivery/m0-m1a-execution-plan.md)建立 Rust workspace、Vue 前端、独立 daemon、适配器与验证/打包脚本。最新事实见[状态](../delivery/status.md)与[本轮证据](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/evidence/2026-09-26-m0-m1a-delivery.md)，每次接手重新检查现场。

| 路径 | 职责 |
| --- | --- |
| 根入口文档 | 理解项目、开发、设计与审查 |
| `Cargo.toml`、`Cargo.lock`、`rust-toolchain.toml` | Rust workspace、锁定依赖、固定工具链 |
| `package.json`、`pnpm-workspace.yaml`、`pnpm-lock.yaml` | Node 工作区与锁定依赖（仅前端/工具链） |
| `crates/core/` | 领域、SQLite/迁移/FTS、对象库、任务与租约、凭证 vault、控制协议、插件宿主 |
| `apps/daemon/` | 独立后台进程（`start/status/shutdown/doctor/version/site-check`）；备份/导出/恢复通过Core控制接口与桌面持久任务执行 |
| `apps/desktop/`、`apps/desktop/src-tauri/` | Vue 3 前端与 Tauri 2 外壳（命令转发、`trace-media` 协议、事件泵） |
| `adapters/bilibili/`、`adapters/douyin/`、`adapters/common/` | 两站生产适配器与安全文档解析；独立进程、双向插件协议，网络/存储由Core持有 |
| `tests/e2e/` | 合成源与夹具适配器、离线闭环、daemon 进程级故障用例 |
| `scripts/check_docs.py` | 无第三方依赖的文档检查 |
| `scripts/verify.py` | 五套件验证入口（offline/desktop/recovery/site/resources） |
| `scripts/gen_licenses.py`、`scripts/package_windows.py` | 依赖许可台账、Windows 便携包与发行自检 |
| `scripts/stage_evidence.py`、`scripts/check_package.py`、`scripts/measure_m1.py` | 必需用例/源码/包/资源/报告聚合门禁；实际内嵌WebView与隔离长测 |
| `.env.example`、`scripts/prepare_site_check.py` | 本地真实站点测试输入模板与零网络准备；配置/授权当前状态只在 USER-ACTIONS，秘密不入库 |
| `docs/requirements.md`、`docs/design.md` | 保持原路径的需求与技术正本 |
| `docs/governance/` | 登记和文档治理 |
| `docs/product-rules/`、`docs/dev-rules/`、`docs/design-rules/` | 专项约束 |
| `docs/decisions/`、`docs/delivery/` | 决定、问题、路线、计划与状态 |
| `docs/research/`、`docs/evidence/`、`docs/templates/` | 资料、事实和模板 |

`apps/desktop/dist/`（前端构建产物，Tauri 编译期嵌入）、`target/`、`node_modules/`、`dist/`、`profiles/` 与 Tauri 生成的 `apps/desktop/src-tauri/gen/schemas/` 为本地产物，不入库，见 `.gitignore`（`src-tauri` 另有嵌套规则）。

## 实际版本与命令

Rust/前端版本由 `rust-toolchain.toml`、`Cargo.lock`、`pnpm-lock.yaml` 与 `package.json` 固定，`--locked`/`--frozen-lockfile` 生效。2026-09-26首次环境记录见[固定版本](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/dev-rules/repo-and-environment.md)；下表媒体与运行时以本轮包manifest/doctor更新，不能以本机曾安装的工具替代随包资产。

| 组件 | 实际版本 | 来源 |
| --- | --- | --- |
| Rust / Cargo | 1.98.1（2026-09-01），含 clippy、rustfmt | `rust-toolchain.toml` |
| Node / pnpm | Node 24.19.0；pnpm 12.6.0（经 corepack） | `package.json#packageManager` |
| TypeScript / Vue / Vite / naive-ui | 6.0.3 / 3.5.43 / 8.3.1 / 2.45.3 | `apps/desktop/package.json` |
| Tauri / tokio / rusqlite | 2.11.6 / 1.53.1 / 0.40.2（SQLite 3.53.2，FTS5 可用） | `Cargo.lock` |
| windows crate | 0.62.2 | `Cargo.lock` |
| WebView2 Runtime | 154.0.4258.62（本机已安装） | 本轮包manifest的doctor及窗口烟测 |
| ffprobe / ffmpeg | n9.0.2-17-g2a571b6068-20260930，win64 LGPL shared | `media-backend.lock.json`、包内media及doctor；不用系统PATH |

在仓库根目录（Git Bash 或 PowerShell 均可）：

```powershell
# 文档检查（无第三方依赖，不联网、不写文件）
python scripts/check_docs.py

# 五套件验证入口；--suite 可选 offline/desktop/recovery/site/resources
python scripts/verify.py --all

# Rust 侧
cargo build --workspace --locked
cargo test --workspace --locked
cargo clippy --workspace --locked --all-targets -- -D warnings
cargo fmt --all -- --check

# 前端侧
corepack pnpm -C apps/desktop run typecheck
corepack pnpm -C apps/desktop run test    # vitest run
corepack pnpm -C apps/desktop run build

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

`capture-*` 类临时诊断命令与本地参考目录不属于本仓库工程，不写入文档正本。安装包（NSIS/WiX）需要额外工具链，本轮只出便携包，见[本轮证据](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/evidence/2026-09-26-m0-m1a-delivery.md)。

## 首次准备与清理环境复跑

```powershell
corepack enable
corepack pnpm -C apps/desktop install --frozen-lockfile
cargo fetch --locked
python scripts/fetch_media.py
```

进程级用例通过 `target/debug` 定位daemon、两站生产适配器和故障探针，`cargo test` 不保证构建其他包的二进制。`scripts/verify.py` 的 offline/recovery 套件先执行一次 `cargo build --workspace --locked` 并核对 `PREBUILD_ARTIFACTS` 中全部文件；单独运行 `cargo test --workspace --locked` 前需自行先构建。原F-001的失败与从无daemon二进制复跑通过证据保留在[固定复核](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/evidence/2026-10-01-m0-m1a-review.md)，当前新增生产进程不能依赖旧debug二进制。

`scripts/verify.py` 允许在受控临时目录内创建 archive/profile，并在结束时清理；不需要管理员权限，也不写系统目录。真实运行数据默认落在 `profile.json` 指定的 `data_root`/`archive_root`（本轮均为仓库外/被忽略目录），凭证经 DPAPI 存于系统用户私有目录。跨平台（Linux/macOS）仅按阶段验证，Windows 编译与运行结果不代表跨平台支持；本轮未在 Linux 复跑。

<a id="git-delivery"></a>
## Git 与交付版本

2026-09-26 按 DEC-011 建立本地 Git，默认分支 main。首提交 `b208ad6` 只有零字节 README；第二提交保存完整 README、文档框架、研究、首轮计划及文档检查脚本。B 轮（M0/M1a 实施）在 `feat/m1a-video-archive` 分支（基点 `7027add`）上提交产品工程、测试、脚本与同步文档，未推送。完整 SHA 用 `git log` 读取，避免文档自引用；初始化/核验记录见[规划证据](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/evidence/2026-09-26-m0-m1a-planning.md)，B 轮提交与验证见[交付证据](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/evidence/2026-09-26-m0-m1a-delivery.md)。当时远端和 CI 尚未建立；当前远端与阶段节奏见下文 DEC-017。

2026-10-07 用户 DEC-017 指定远端 [chialecode/trace](https://github.com/chialecode/trace)，并授权将 `162edf2066e6f6b435a3481976eb5ffc1d0f1601` 及之后本批提交归并为 M0 后推送。阶段父提交是 `7027addebe6f8769118837569bba377a4a80e900`；初始化两提交保持不动。首次检查远端为空，本次在自动门禁与 A 审查收敛后初始化 main；这是空仓首次导入，后续 main 经 PR 审核集成。M0 命名表示本次 Git 范围，产品验收单独判断。

- 正式阶段使用 M0、M1、M2…，M 表示 Milestone（里程碑）。阶段标题、提交 scope 和 PR 标题包含该编号；新分支形如 `codex/m1-<目标>`，当前已存在的分支不为改名而重建。一个阶段一个分支、一个 commit、一个 PR。阶段内部不按 A/B 角色、审查轮次、代码/文档、工作包或修复拆分提交；push 前全部 amend/归并进阶段提交。中间恢复点只在必要时保存，首次 push 前归并。
- 开始检查 HEAD、工作区、upstream 与远端引用，保护其他人的文件；已有同阶段分支继续使用。新阶段默认从更新后的 main 建 `codex/<目标>`；依赖未合并阶段时记录具体基点和依赖，不混入前置阶段提交。
- 归并前用本地备份分支保存原历史；本次用 `git reset --soft 7027addebe6f8769118837569bba377a4a80e900` 归并，不改变工作树。历史失败和被测 SHA 保留在证据中；整理历史本身不是重新测试。原先独立 A/B 提交不 amend 的本批限制由 DEC-017 替代。
- 提交前按明确路径暂存，审阅差异与文件归属，运行相关门禁、文档/公开检查、治理反例和工作树/索引 diff 检查。消息采用 `type(scope): 具体结果`，正文描述最终行为、验证和限制，不写未经验收的产品完成声明。
- 新提交带 DCO `Signed-off-by`，使用当前提交者 Git 身份，以 `git commit -s` 或 `git commit --amend -s` 落实；不借用外部工程作者身份。GPG 签名不作已配置声明。
- A 技术审查收敛且用户明确授权该次 push 后，集中 push 指定分支；不使用 `--all`/`--mirror`，不上传备份分支、真实档案、参考素材或 dist 包。push、PR、合并和发布分别遵守用户授权，已有授权不重复询问。
- 后续阶段 push 后，在用户明确授权相应 PR 操作后创建/更新同一个 PR，说明具体行为、验证和未测边界，审核解决反馈后按授权 squash 合并，使 main 每阶段一个提交。已推送阶段默认追加集中修复，再由 PR squash 收敛；远端改写须另获明确授权并用指定旧 SHA 的 `--force-with-lease`，不能为了单提交擅自强推。
- PR 审核与必需自动检查不能由未运行的 CI 替代。本次准备并接通下述 TRACE 自有治理 CI/分支保护；bot 与自定义 hook 未接入，不复制其他工程脚本或许可。人工体验未运行继续在 USER-ACTIONS 顺延，不伪装产品验收或发布批准。

仅本仓设置 `core.autocrlf=false`、`pull.ff=only`、`fetch.prune=true`；`.gitattributes`/`.editorconfig` 管理文本。没有实际 hook 时不设置虚假的 hooksPath。TRACE 自有源码现依据 DEC-027 采用 [Apache-2.0](../../LICENSE)；历史源码推送与此次许可选择分别记录。正式分发授权仍见 [Q-004](../../USER-ACTIONS.md#q-004)，不自动上传本地便携包。

### main 保护、CI 与远端核验

远端配置以 GitHub 实际回读为准，本地 JSON 是可审阅的期望值：`.github/rulesets/main.json`、`.github/repository-settings.json`、`.github/actions-policy.json`。本次空仓首次导入完成后启用 main 保护；不保留 bootstrap bypass。

| 项目 | TRACE 规则 |
| --- | --- |
| main 写入 | 后续变更必须经 PR；禁止强推/删除，保持线性历史；bypass_actors 为空 |
| 审核 | 全部 review thread 必须解决；单维护者 required approvals=0，不要求自己批准自己，不强制不存在的 code owner |
| 合并 | 只允许 squash；关闭 merge/rebase/auto merge；PR 标题/正文形成提交消息，合并后删除任务分支 |
| 必需检查 | `repository-quality`，绑定 GitHub Actions App 15368；首次真实成功后才启用，不要求每次追平 main |
| CI | `.github/workflows/ci.yml`：面向 main 的 PR opened/synchronize/reopened 和手动触发；无 main push/schedule/路径过滤，10 分钟超时，同 PR 新运行取消旧运行 |
| 检查范围 | Ubuntu 24.04 执行 Python 文档、工作树/索引公开检查和scripts/tests自动发现的治理反例；不冒充 Rust/桌面/Windows 媒体/产品验收，阶段仍须本地五套件与相关门禁 |
| Actions 权限 | contents 只读、checkout 不保留凭据；仅允许固定 SHA 的 checkout，默认 token read，不能创建/批准 PR |
| 其他设置 | 不安装 bot/App，不建立自动合并、定期巡检或无效必需检查；项目许可按DEC-027，发布授权按Q-004 |

远端操作只按明确授权，串行提交配置差异，每次变更间隔至少一秒；403/429 按错误语义停止，不重试绕过限制。检查一次实际状态，运行状态按必要进展有界查询，不频繁轮询或重复触发 CI。首次推送、CI 与保护回读的命令/结果保存于本轮 `artifacts/a-closeout-rereview/`，审查摘要见[收尾报告](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/delivery/stage-report.md#a-closeout-rereview)。

## 2026-10-07 文档与界面增量

开发入口 `corepack pnpm -C apps/desktop run dev`，回环端口 5173；浏览器默认明确标记示例，不启动 daemon。Tauri 默认读真实档案，旧后台页移至 `/status`，其他管理页保留。浅色 Token 在 `src/styles/shell.css`，主页在 `src/styles/home.css`；资料入口见[当前计划](../delivery/stage-plan.md)。

新增 `scripts/check_publication.py`（工作树+索引公开检查）、`scripts/tests/test_governance.py`（隔离反例）及可选 `scripts/prepare_ui_reference.py`（需 Pillow，仅本地裁图）。`python -m unittest discover -s scripts/tests` 跑治理反例；产品构建不依赖 Pillow。

未跟踪文件、个人配置、原图与运行截图按[本地规则](local-reference-and-files.md)处理。新交付默认分支前缀 `codex/`；旧 `feat/m1a-video-archive` 保留，当时 A 在独立文档/界面分支交付；后续本批历史归并按 DEC-017。

## TRACE-CLOSEOUT-02 环境补充

2026-10-07 的工具链仍锁定上述 Rust/Node/pnpm 组合；本机 WebView2 doctor 为 154.0.4258.53。媒体资产准备命令 `python scripts/fetch_media.py` 校验根锁文件的月末 LGPL3 shared 资产到忽略的 target/media，须在包含 F-012 的全套测试前执行；不会静默依赖系统 PATH。实际结果见[B 收尾报告](../delivery/stage-report.md)。

`cargo run -p trace-e2e --bin prepare_acceptance --locked -- artifacts/closeout/acceptance-new` 只接受尚不存在的目录，创建明确标记的离线合成 Profile、12 秒 AVC/AAC、封面和缺失资源以及完整备份；不创建真实站点订阅，不进入发行包。真实验收只按 USER-ACTIONS 中的已核对包与步骤执行。

## 当前阶段的版本与授权补充

阶段提交边界包含A规划、B实现、自检、返工、A审查与人工反馈修复。当前未推送阶段改动按同一commit保存；只有必要恢复检查点可暂存多个提交，首次push前核对归属并归并。amend前核对远端引用与实际推送记录，无upstream不等于未推送；已共享提交默认追加集中修复，不能自动强推。用户未授权远端动作不阻碍本地工作。

A通过后推荐具体授权，不再交B复审；人工not-run不阻止技术收尾，但不能冒充产品验收。报告保存被测基点/源码指纹，最终交付SHA从Git读取，不为了自引用再造提交。新阶段从已更新main开始；前置尚未合并时可使用明确依赖提交并记录关系，前置squash合并后整理后继差异和复验。

2026-10-07只读回查GitHub：main规则active、无bypass、禁止删除/强推、PR讨论解决、approval=0、squash、线性历史、repository-quality绑定Actions App15368。没有更改远端设置，没有推送或创建PR。本轮远端动作仍未授权。

## M1 包、资源与阶段证据入口

本节已有命令的原通过记录对应 r1；A 修改后原 bundle 应拒绝当前输入。r2 的 P13 已在原入口扩展计划/schema5、C11～C13和85/66/19需求映射，具体当前证据见报告§16.8以后，不能只改旧回执指纹。原 measure 二进制、长测原始样本及 m1-9 包保留，不为复核重建后冒充原输入。

`scripts/stage_evidence.py run --kind test|bench|package|report --id <唯一ID> --output <新回执JSON> -- <实际命令>`保存完整输出、退出码和运行前后源码输入；`assemble --receipts ... --package <包目录> --benchmark <基准JSON> --real-source <真实回执...> --output <bundle>`聚合，`check <bundle>`或`verify.py --stage-bundle <bundle>`拒绝缺项、旧源码、失败/重复用例、损坏日志/包/采样及过期文档。独立r3保留原98和r2的133必需用例，增加六项返工/C14及四个真实产品链用例，按当前REQUIRED_CASES逐名检查；全套回执已有的用例不能再重复加入定向回执。verify摘要只说明五套件，不作为逐用例名单。

```powershell
# 使用不存在的输出路径，保留此前通过与失败；包目录取package_windows实际输出。
python scripts/check_package.py <package_windows本次输出的新包目录>
# 正式新测量必须使用与当前源码对应的新包；此处为命令格式。
python scripts/measure_m1.py <新测量目录> --idle 1030 --seconds 3620 --entries 100000 --package <对应源码的新包目录>
# 原r1 bundle：A修改前通过，修改后预期拒绝，不是r2通过命令。
python scripts/stage_evidence.py check artifacts/m1-final-bundle.json
```

包构建显式启用`tauri/custom-protocol`，内嵌生产前端；清单覆盖全部文件和源码输入，包内只有生产两站适配器。check_package使用合成空档案、独立WebView目录和回环CDP，窗口最小化且不激活，不发送键鼠或操作用户浏览器。测量器每5秒采样Core、适配器、媒体子进程和测试窗口的Windows Job累计CPU/当前进程内存与句柄，已退出子进程CPU仍纳入；合成源和驱动器成本也包含在内。基线同时核对10万发布/50万媒体元数据、至少500字正文、100普通/20直播目标、100ms延迟、50行查询和每路至少8Mbit/s。源码、测量脚本和包须在测量期间保持不变，正式结果与精确命令以[阶段报告](../delivery/stage-report.md)为准，短测不能通过时长门禁。

聚合文件写在artifacts中；先保存必要公开摘要、完成报告和所有文档，再生成文档指纹，避免bundle自引用。更改源码后重跑受影响证据，更改文档后重新生成report回执及bundle；原失败回执不覆写或改绑新指纹。便携包移除前先关闭登录后启动并完全退出，保留或备份Profile/数据，避免默认trace-data跟随程序目录被误删。安装器、签名和正式发布未验证。

本地真实请求准备入口为 `python scripts/prepare_site_check.py --site <站点> --package <包目录> --out <新隔离目录>`；它不联网，缺 Cookie 等输入退出2。使用步骤只维护于 [ACT-002](../../USER-ACTIONS.md#act-002)，安全规则见[本地凭证](data-and-security.md#local-site-credentials)。已有 `.env.local` 不覆盖，产品 Vault 不自动导入开发配置。

2026-10-11存储恢复核对：长期写入基准位于虚拟卷时，同时核对工作卷及虚拟磁盘文件所在宿主卷的可用空间，并在运行期间观察变化。虚拟卷声明容量不能证明宿主空间充足；本次宿主盘满导致开发卷卸载的事实及缺失证据见[报告§17.6](../delivery/stage-report.md#m1-storage-outage-recovery)。保留中断目录，未落盘或损坏的原始计数不得补造；恢复后先核对源码、包、Git和旧日志，再用新目录执行原完整规模与时长。不因恢复挂载修改用户配置、减少负载或重绑旧包/长测。

2026-10-11 A复查构建环境：本地依赖链接缺失先后导致Vitest找不到std-env及estree-walker解析错误；普通离线install仅恢复部分，离线force因本地缓存缺tarball失败。实际使用`corepack pnpm -C apps/desktop install --frozen-lockfile --force`按原锁恢复后，52项前端、类型/生产构建及新包检查通过，未升级依赖或修改锁文件。原失败及恢复日志见报告§18，不以已有包曾运行推定开发依赖目录完整。

独立审查crate复用工作区target时使用独立package名称，并逐项核对实际运行的用例名称。相同名称/版本的两个审查crate可能复用同一输出文件；本次一次命中原3项旧探针，改为独立名称后实际8项通过，旧文件与中间回执保留。退出0本身不能证明指定新增探针已经运行。

2026-10-13用户要求后续直播测试不再长时间录制：真实直播产品链继续按ACT-002约60秒的有界录制执行，满足必要封片、重开、导出与解码后停止。60分钟合成资源基线属于单独的阶段性能证据，不自动成为每次直播功能复验的时长；本次已完成长测保留，不因后续短测改写。
