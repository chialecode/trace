# 当前阶段计划：M2（安装与更新）

- **阶段编号**：M2；计划标识 `TRACE-M2-01`，修订 1（2026-11-09）。
- **角色**：A 规划；B 连续实施与自检；A 集中审查（[开发流程 §2](../dev-rules/workflow.md#agent-handoff)）。给 B 的 prompt 在[第 9 节](#prompt)。
- **分支**：`codex/m2-install-update`，A 已从 `main @ 29c2678`（M1，[PR #1](https://github.com/chialecode/trace/pull/1) 于 2026-11-09 squash 合并）创建。本计划所在的提交是阶段提交的起点；B 的实施、A 的审查修复和人工反馈的修复全部 amend 进这一个 DCO 提交。
- **上一阶段**：M1 的计划与报告在合并提交里：[M1 计划](https://github.com/chialecode/trace/blob/29c2678/docs/delivery/stage-plan.md)、[M1 报告](https://github.com/chialecode/trace/blob/29c2678/docs/delivery/stage-report.md)。
- **决定与需求**：范围记为 [DEC-041](../decisions.md#dec-041)，更新的设计记为 [DEC-042](../decisions.md#dec-042)；需求 FR-STORE-001 与新增 FR-UPD-001～004（[需求](../requirements.md#update)）；验收 AC-27～AC-33（[路线与验收](roadmap.md#acceptance)）。细节的默认值见 [Q-012](../../USER-ACTIONS.md#q-012)，B 按默认实现，用户以后可以改。

用户 2026-11-09 要求：下一阶段先解决安装包的问题，参考用户指定的另一个桌面项目最近一次“安装与自动更新”提交（下称“安装参考项目”）和 MyGo 的推荐做法；通道同样是 `trace`（正式版）和 `trace-dev`（开发版）。A 理解的目标与安装参考项目一致：本机同时装着正式版和开发版；每次改完代码、打包通过后，本机的开发版自动更新；正式版通过互联网更新；整个流程尽量自动化，版本号用 `0.1.x`。

## 1. 范围与退出条件

| 项目 | 约定 |
| --- | --- |
| 阶段内容 | 阶段开始的整理（C0）；依赖盘点与 MyGo 升级（D1）；版本号（W1）；开发版本机自动更新（W2）；正式版联网更新（W3）；更新的安全与数据保护（W4）；一条命令发布与升级演练（W5）；安装体验（W6）；安装与更新的测试（W7）；GitHub Actions 上的构建与每周发布（W8）；文档与用户待办（W9） |
| 不在本阶段 | Windows 代码签名（见 3.1，Q-012 默认不签）；winget、Microsoft Store；macOS 与 Linux 的发布包；应用内的“测试版”通道；产品功能的新需求 |
| 可连续执行 | 本地实现、依赖升级、合成样本、测试、打包、本机开发版的自动安装（W2，用户要求的行为）、ptest 身份的安装演练、文档同步、阶段提交与 amend |
| 外部操作 | 未授权：push、PR、合并、创建标签与 Release、改仓库设置与 secrets。仓库侧的一次性配置是 [ACT-008](../../USER-ACTIONS.md#act-008)；每周自动发布要用户另行授权 [ACT-009](../../USER-ACTIONS.md#act-009)，授权前 `release.yml` 只构建不发布。本阶段的测试不访问 GitHub 与哔哩哔哩：更新源用本机的假发布源 |
| 必须保留 | M1 的全部行为、数据位置与通道隔离（DEC-029）、单实例与托盘、安静测试（[工程规则](../dev-rules/engineering.md#quiet-tests)）、只删登记文件、凭证隔离；`dist/<通道>/` 只留最新一份（DEC-025） |

**退出条件**

| 条件 | 做法 | 工作包 | 验收 |
| --- | --- | --- | --- |
| 版本号按规则生成 | 正式版 `0.1.x`，开发版 `<下一个正式版号>-dev.<UTC 年月日时分>`；由工具统一算出并写进程序和安装包，不为发布手改 `mygo.json`；“设置 › 关于”显示版本、提交与构建时间 | W1 | AC-27 |
| 开发版在本机自动更新 | `tools/package` 打出开发版后，本机装了 `trace-dev` 就让运行中的它保存并退出、静默安装新包、原来在运行就重新打开；正式版绝不受影响；没装就跳过；可以关闭 | W2 | AC-28 |
| 正式版联网更新 | 用 MyGo 的 `mygo.Updater` 从 GitHub Releases 检查、后台下载、校验后替换；顶部一行不打断的“新版本已就绪 · 重启以更新”；有缓存或录制时先说明会暂停什么；不理会时下次启动就是新版本；更新后第一次启动说明一次 | W3 | AC-29 |
| 更新不能被篡改、数据不被破坏 | Ed25519 签名（公钥内置）；签名不对、被改过、版本不更新、不是 HTTPS 的一律拒绝并保留现版本；有结构升级的版本在迁移前备份数据，旧版本不打开更新过的数据 | W4 | AC-30 |
| 一条命令发布 | `go run ./tools/release` 在同一份源码上生成正式版安装包、更新包、清单、`SHA256SUMS`、发布说明，并在 ptest 身份下演练“从上一版升级到这一版”；有授权时才发布 | W5 | AC-31 |
| 安装体验 | 应用图标（开发版另一配色）、全中文的安装界面、发布者与版权、“设置 › 应用”里显示版本；安装包名带版本 | W6 | AC-32 |
| CI 构建与每周发布 | `.github/workflows/release.yml`：每周定时与手动触发；上次发布以来没有新的合并就跳过；在 Windows 运行器上跑检查、打包与升级演练，全部通过才签名；授权前只构建不发布 | W8 | AC-33 |
| 阶段开始的整理 | 去掉文档里以前阶段的标签；按内容整理文件 | C0 | 文档检查；报告的整理记录 |

**阶段完成的判定**：B 自检的必测用例与门禁全部通过，A 集中审查收敛后，才可以请用户授权 push、PR 与合并。人工检查（H-002）与待答复的问题不是合并的前提；产品验收保持 not-run。

**阶段完成后，用户可以：**

- 改完代码、打包通过后，本机的开发版自己装上新版本，原来开着的会自动重开；
- 授权 ACT-009 后，每周一次，只要上周有 PR 合并，GitHub Actions 就自动检查、打包、演练并发布新的正式版；装着的正式版过一会儿提示“新版本已就绪，重启以更新”；
- 在“设置 › 关于”里看到当前版本，手动检查更新，关闭自动检查或自动下载。

## 2. A 已核对的现场（2026-11-09）

- **打包**（`tools/package/main.go`）：临时改写 `mygo.json` 的 `slug`、`identifier`、`windows.displayName`、`windows.installDirName` 区分通道（正式版 `trace` / `io.github.chialecode.trace` / `TRACE`，开发版 `trace-dev` / `io.github.chialecode.trace-dev` / `TRACE Dev`），通过 `MYGO_LDFLAGS` 注入 `appdirs.channel` 与 `appdirs.version`，运行 `go tool mygo build -platform windows/amd64`，再登记替换到 `dist/<通道>/`。版本固定读 `mygo.json` 的 `0.1.0`；`-install` 只在明确要求时静默安装，不处理运行中的应用。
- **程序里的版本**：`internal/shell/shell.go` 写死 `mygo.App.SetVersion("0.1.0")`；`appdirs.version` 已有但没用于显示。“设置 › 关于”有版本号，没有提交与构建时间，也没有更新入口。
- **安装包**：MyGo `v0.8.1` 生成的 NSIS 安装包按用户安装到 `%LOCALAPPDATA%\Programs\<installDirName>`，登记 `HKCU` 卸载项（含 `DisplayVersion`、`InstallLocation`），完成页提供运行与桌面快捷方式。没有图标（`resources/icon.png` 不存在），没有 `i18n`，所以安装界面是英文；没有 `publisher` 与 `copyright`。安装前它用 PowerShell 请运行中的应用关闭主窗口，10 秒后强制结束。**TRACE 关闭窗口是缩到托盘，所以安装器会在 10 秒后强杀正在缓存或录制的 TRACE**，W2 必须先让它自己保存退出。
- **安装测试**：`internal/shell/shell_test.go` 只有一个占位（“requires `TRACE_RUN_INSTALLER_TEST=1` with trace-ptest”），没有真正的安装、升级与卸载测试。`appdirs` 已有 `ptest` 通道，可作为测试身份。
- **更新**：没有任何更新机制。仓库 `chialecode/trace` 公开，没有标签与 Release。
- **CI**：只有 `.github/workflows/ci.yml` 的必需检查 `repository-quality`（Ubuntu、`go run ./tools/check`、只读 token、只允许固定 SHA 的 `actions/checkout`，[工程规则 §4.1](../dev-rules/engineering.md#remote)）。
- **更新签名密钥**：A 已于 2026-11-09 用 `go tool mygo keygen` 生成 Ed25519 密钥对，放在仓库外的 `%APPDATA%\trace-release\`（私钥 `update-signing.key`、公钥 `update-signing.pub`；2026-11-11 从 MyGo 默认目录移来）。公钥是 `PE6JZtvGTY8jvwwou6DHO/qhWjkbrSXHbHd2O3fRZFo=`，B 把它写进正式版的配置；**B 不得重新生成或覆盖这把密钥**，否则本机与 CI 的签名对不上。私钥不入库，备份与放进 CI 见 ACT-008。

## 3. 依据、查证与选型

本机参考索引里没有“安装与更新”的条目；用户在对话里指定了安装参考项目（只参考做法与流程，它是 Electron 应用，代码不适用）。A 已按[参考规则](../dev-rules/workflow.md#local-reference)在索引的 `PROPOSALS.md` 追加登记建议。

<a id="sources"></a>
### 3.1 候选（查询 2026-11-09）

| 需要 | 候选与来源 | 版本、维护与许可 | 判断 |
| --- | --- | --- | --- |
| 应用内更新 | MyGo 自带的 `mygo.Updater`（[auto-updates](https://github.com/egoist/mygo/blob/v0.8.3/docs/updates.md)）：`mygo build` 用 Ed25519 私钥签名每个平台的更新包，写清单与差分包；应用用内置公钥校验后替换自己 | 与现用 MyGo 同一模块；`v0.8.3`（2026-11-07，GitHub Releases），仓库 MIT、2026-11-08 仍有提交 | **采用**：与现有 `mygo build` 安装包是同一套（按用户安装在 `%LOCALAPPDATA%\Programs` 才能自更新），不需要另装更新器；支持 GitHub Releases、差分更新、系统代理 |
| 更新界面 | MyGo 的 `plugins/updater` 与原生界面版 `updater/native`（[updater plugin](https://github.com/egoist/mygo/blob/v0.8.3/docs/plugins/updater.md)） | 同上 | **不采用插件的窗口，用 `mygo.Updater` 自做一行提示**：插件会在下载后直接提供重启，不知道 TRACE 正在缓存或录制；它的窗口样式也与 TRACE 的界面不同。MyGo 文档把“自己的更新界面”列为同等做法 |
| 发布到 GitHub | `mygo build -upload`（经 `gh` 上传到草稿 Release）与 `gh release` | 本机已登录 `gh` | **采用**：不引入第三方 Action |
| Windows 构建与演练机器 | GitHub 托管运行器 `windows-2025` | Windows Server 2025 | **采用，固定写 `windows-2025`**。MyGo 能在 Ubuntu 上构建 Windows 包，但升级演练与冒烟要在 Windows 上跑。本机门禁仍是阶段合并的依据，CI 结果只决定这一次能不能发布 |
| job 之间传文件 | `actions/upload-artifact`、`actions/download-artifact`（GitHub 官方，MIT） | B 实施时查当前正式版并固定完整 SHA | **采用**：让“构建与签名”和“发布”分开，发布的 job 有写权限但不运行仓库代码、不碰私钥。加入 Actions 允许列表属于 ACT-008 |
| Windows 代码签名 | Azure Artifact Signing、开源项目证书、SignPath Foundation、Microsoft Store | — | **本阶段不签**（Q-012 默认）：个人开发者能拿到的签名要么收费、要么有地区或资格限制；未签名时 SmartScreen 会提示，H-002 写明怎样放行。更新本身由 Ed25519 签名保证完整性 |

**依赖盘点（D1）**：MyGo 从 `v0.8.1` 升到 `v0.8.3`。`v0.8.3` 的发行说明里与本阶段直接相关的一项：用它构建的应用在 Linux 与 Windows 上更新时，会删除新版本不再带的文件。其余为界面与渲染修复，没有 Go API 的破坏性变化。B 实施开始时再查一次 MyGo 的最新正式版（不用 `native-sqlitecore` 之类的非版本标签），并对其他依赖跑 `go list -m -u all`，可以升级的逐个升级并跑完整回归；某个升级失败就回退，在报告里写明尝试的版本、失败的命令与复查条件。

## 4. 设计

<a id="w1"></a>
### 4.1 版本号（W1）

- **正式版**：`0.1.x`。从 GitHub 上最高的 `v0.1.*` 标签算出下一个号（本机用 `git ls-remote --tags` 与 `gh release list` 核对，两者不一致时停下说明）；还没有发布时从 `0.1.1` 开始（Q-012：M1 期间的开发包都标着 `0.1.0`，从 `0.1.1` 开始避免两个不同的构建同号）。`mygo.json` 的 `version` 只表示版本线 `0.1.0`，不为每次发布提交改动。升到 `0.2` 或 `1.0` 由用户决定。
- **开发版**：`<下一个正式版号>-dev.<UTC 年月日时分>`，例如 `0.1.1-dev.202611091530`。开发版与正式版是两个独立的通道，互不比较、互不更新。
- **写进程序**：版本、通道、提交短哈希与 UTC 构建时间由 `tools/package` 与 `tools/release` 的同一个函数算出，经 `MYGO_LDFLAGS` 注入 `internal/appdirs`，并写进构建时的 `mygo.json` 副本（MyGo 用它写文件属性、安装包名与卸载项的 `DisplayVersion`）；`shell` 用它调用 `mygo.App.SetVersion`，去掉写死的 `0.1.0`。`go run`、测试等没有注入时显示 `0.0.0-dev`。
- **显示**：“设置 › 关于”显示“版本 0.1.1（正式版）”或“0.1.1-dev.202611091530（开发版）”、提交与构建时间；诊断信息与日志开头也写这三项。`VERSION.txt` 改为同样的字段。

<a id="w2"></a>
### 4.2 开发版在本机自动更新（W2）

- **触发**：`go run ./tools/package`（开发版，默认通道）在包登记到 `dist/trace-dev/` 并校验通过之后，最后一步执行“安装到本机”。也可以单独运行 `go run ./tools/package -install-only`（用 `dist/trace-dev/` 里现有的包）。`TRACE_DEV_AUTO_INSTALL=0` 或在 CI 上（`CI=true`）时跳过并说明。原来的 `-install` 参数去掉（由这一步取代）。
- **只碰开发版**：按 `HKCU` 卸载项里 `io.github.chialecode.trace-dev` 对应的条目（`InstallLocation`、`DisplayVersion`）确认本机装了开发版，没装就跳过。绝不安装、关闭或改动正式版与 ptest 身份。
- **让运行中的开发版自己退出**：用开发版的可执行文件带 `--quit-for-update` 启动一次；它作为第二个实例把参数转给运行中的 TRACE（现有单实例通道）。运行中的 TRACE 收到后：不弹确认，暂停全部缓存任务（与托盘“退出”相同的持久化，重启后为“已暂停”，用户的“继续”不变），正常结束正在进行的录制（收尾成可播放的文件），保存界面状态，记下“为更新而退出”与退出前窗口是打开还是在托盘，然后退出。工具最多等 30 秒；没退出就放弃这次安装、不强杀，说明原因（例如“TRACE Dev 正在收尾录制，30 秒内没有退出，这次没有安装”）。没在运行就直接安装。
- **安装**：用刚登记的开发版安装包静默安装（`/S`，MyGo 的安装包沿用上次的安装目录）；完成后从卸载项核对已装版本等于新版本号。
- **重新打开**：原来在运行的，安装完成后启动它；退出前窗口在托盘的，带 `--background` 只回到托盘（B 核对现有启动参数，没有就新增），否则打开窗口。重新打开后的 TRACE 读到“为更新而退出”的记录，不再提示“已暂停”的意外；任务保持已暂停，由用户继续（与现在托盘退出后重开的行为一致）。
- **结果**：写 `artifacts/package/install-dev.json`（是否安装、旧版本、新版本、是否在运行、是否重开、用时、跳过或失败原因）。安装失败让 `tools/package` 以非零退出，但不回滚已登记的包；跳过不算失败。
- **安静与安全**：这一步是用户要求的、会关闭并更新本机 TRACE Dev 的行为，只在 `tools/package` 里，不在 `go test` 和 `tools/check` 里运行；测试用注入的假卸载项、假进程与假安装器覆盖它的判断。

<a id="w3"></a>
### 4.3 正式版联网更新（W3）

- **只在正式版启用**：只有正式版的构建配置带 `updates`（公钥与 `github: "chialecode/trace"`）；开发版与 ptest 不带，`mygo.Updater.Enabled()` 为假，不检查。更新源是 GitHub Releases，只取已发布的正式 Release，草稿与预发布不算。
- **检查**：启动 1 分钟后检查一次，之后每 24 小时一次（Q-012）；“设置 › 关于”的“检查更新”手动检查。离线或失败时静默，只写日志；手动检查时才显示原因。这些请求只访问 GitHub、只带版本与平台，不计入哔哩哔哩的请求额度（NFR-NET-002 不变），遵循系统代理。
- **下载与就绪**：“自动下载更新”开着（默认开）时，发现新版本就在后台调用 `Install` 下载、校验并替换程序文件；新版本在下次启动时运行，正在运行的 TRACE 不受影响。关着时只提示“有新版本 0.1.x”，用户点“下载并安装”才下载。
- **提示**：已就绪时页面顶部栏下方出现一行不打断的提示：“新版本 0.1.x 已就绪 · 更新内容 · 重启以更新”，可以关掉（这次运行不再出现）。“更新内容”展开 Release 的说明（`CHANGELOG.md` 里这一版的一节）。
- **重启以更新**：没有缓存与录制时直接正常退出并 `App.Relaunch`；有时先说明“会暂停 N 个缓存任务并结束 M 场录制，重启后可以继续缓存”，确认后按托盘“退出”的同一条路径收尾再重开。用户不理会时，下次启动就是新版本。
- **更新后第一次启动**：显示一次“已更新到 0.1.x”，点开看更新内容。
- **设置 › 关于**：当前版本与通道、提交、构建时间；上次检查时间；“自动检查更新”（默认开）、“自动下载更新”（默认开），保存在 `settings.json`；“检查更新”按钮（检查中显示进度，结果写在按钮旁）。开发版这一节写“开发版由本机打包自动更新”，不显示开关。
- **界面层边界**：`mygo.Updater` 只在 `internal/shell`（或新的 `internal/update`，同样属于允许依赖 MyGo 的外壳层）里调用；`internal/ui` 经一个小接口读状态和发命令，后端包不依赖 MyGo（INV-05）。`tools/check` 的依赖方向清单相应更新。

<a id="w4"></a>
### 4.4 更新的安全与数据保护（W4）

- **签名**：MyGo 的 Ed25519 签名与校验（更新包与差分包）；公钥写进正式版的构建配置；私钥只从 `MYGO_UPDATER_PRIVATE_KEY` 读取（本机由 `tools/release` 从第 2 节的密钥文件读入环境，CI 来自 `release` 环境的 secret），缺了就失败、不生成新密钥、不打不签名的正式版。
- **必须拒绝**：签名不对或用别的密钥签的、内容被改过的、版本不比当前新的、地址不是 HTTPS 的、清单格式不对的；拒绝时保留现版本，手动检查时显示“更新包校验失败，已保留当前版本”，日志写原因。B 先确认 MyGo 实际做了哪些检查（读 `v0.8.3` 的 `updater.go` 与测试），MyGo 没做的由 TRACE 在调用前后补上；每一条都要有测试（假更新源，见 W7）。
- **数据保护**：B 核对 `library.db`、`updates.db`、`subscriptions.json`、`settings.json`、`ui-state.json` 现有的结构版本与迁移。**有结构升级时**，迁移前把旧文件复制为 `<文件>.premigrate-<旧版本>`（每个文件只留最近一份）；**较旧的程序遇到较新的结构**时拒绝写入并显示“这些数据来自更新的 TRACE 版本，请安装新版”，不降级、不覆盖。没有版本字段的文件补上版本字段，兼容读取没有版本字段的旧文件。
- **密钥轮换**：本阶段只用一把密钥；换密钥要先发布一个用旧密钥签名、内置了新公钥的版本，步骤写进[工程规则 §4](../dev-rules/engineering.md#git)。

<a id="w5"></a>
### 4.5 一条命令发布与升级演练（W5）

- **`go run ./tools/release`**（B 新建）：要求工作区干净、当前提交就是 `origin/main` 的最新提交（可以用 `-allow-dirty` 只做演练）；按 W1 算出版本；运行 `go run ./tools/check`；用正式版配置与私钥构建；产出 `dist/trace/` 下的安装包、更新包与清单（MyGo 的命名）、差分包（有上一版时）、`install.ps1`、`SHA256SUMS`、`VERSION.txt`、`THIRD-PARTY-NOTICES.txt`。
- **发布说明**：`CHANGELOG.md`（B 新建，中文）里这一版的一节；没有这一节时由上次发布以来合并的 PR 标题生成草稿写进去，提交前给人看（CI 上直接使用生成的那一节）。MyGo 要求文件存在时必须有这一版的一节。
- **升级演练**：在 ptest 身份下（`trace-ptest`，独立的安装目录、数据目录与卸载项）：用同一份源码构建两个版本（例如 `0.1.1-rehearsal.1` 与 `0.1.1-rehearsal.2`，都用一把只在这次演练里生成的临时密钥签名），静默安装旧版，启动它，让它从本机回环地址上的假发布源（只在 ptest 身份里允许用环境变量指定，正式版和开发版不读这个变量）检查、下载、校验并替换，重启后核对运行的版本、卸载项版本与数据仍在；再演练一次被篡改的包被拒绝；最后卸载并确认数据目录保留、安装目录删除。演练不碰本机的正式版与开发版，发现 ptest 已装时先卸载它。结果写 `artifacts/release/rehearsal.json`。
- **发布**：只有带 `-publish` 且用户已授权（ACT-009 或用户在对话里对这一次的明确授权）时才创建标签 `v0.1.x`、上传到 Release 并发布；默认只生成文件与演练。上传用 `mygo build -upload` 或 `gh release`，发布后核对 Release 的文件与 `SHA256SUMS` 一致。

<a id="w6"></a>
### 4.6 安装体验（W6）

- **图标**：新建 `resources/icon.png`（1024×1024，TRACE 的 “T.” 字标，白字蓝底 `#1a73e8`，与界面和托盘一致）与开发版图标（同一字标，深灰底加一个小的 “DEV” 角标）。MyGo 的 `icon-dev.png` 只用于 `mygo dev`，所以开发版通道由 `tools/package` 在构建配置里把 `icon` 指向开发版图标。窗口、任务栏、开始菜单、卸载项与安装包都用对应通道的图标；托盘图标沿用现有的绘制。
- **中文安装界面**：按 MyGo 的做法设 `i18n.source` 为 `zh-Hans` 并提供最小的中文目录（应用名 `TRACE` / `TRACE Dev`、描述“哔哩哔哩内容的订阅、缓存与录制”），安装、卸载与完成页都是中文。MyGo 自己的“应用正在运行”提示仍是英文（MyGo 的已知限制）；W2 与 W3 保证正常更新时不会走到它。
- **发布者与版权**：`publisher` 为 `chialecode`，`copyright` 为 `© 2026 chialecode`，文件属性的描述与产品名分别是 TRACE / TRACE Dev；“设置 › 应用”里显示名称、版本与发布者。
- **安装包名**：`trace-setup-<版本>-amd64.exe`、`trace-dev-setup-<版本>-amd64.exe`；`dist/<通道>/` 仍只留最新一份，旧版本的安装包在登记替换时删除。

<a id="w7"></a>
### 4.7 安装与更新的测试（W7）

- **单元与集成**（`go test`，安静、不碰本机安装）：版本号的计算（无标签、已有 `v0.1.3`、标签与 Release 不一致、开发版时间格式）；`install-dev` 的判断（没装、装了未运行、运行中且 30 秒内退出、运行中不退出、安装失败、版本核对不符、正式版存在时不受影响）用注入的卸载项、进程与安装器；`--quit-for-update` 让运行中的外壳暂停任务、收尾录制、记下窗口状态后退出（合成后端）；更新状态机（检查失败静默、手动检查显示原因、已就绪提示、有任务时的确认文案、更新后第一次启动的说明）；W4 的每一条拒绝；数据结构版本的备份与拒绝。
- **安装演练**（`TRACE_RUN_INSTALLER_TEST=1` 门控，B 在本阶段实际运行并记录；`tools/release` 每次运行）：W5 的 ptest 安装、升级、篡改拒绝与卸载。
- **界面**：`tools/uishots` 增加“设置 › 关于（正式版，可更新）”“更新已就绪的提示”两个场景（场景总数与截图数相应增加，写进工程规则）。
- **冒烟**：`tools/smoke` 核对包里程序报告的版本与 `VERSION.txt` 一致。

<a id="w8"></a>
### 4.8 GitHub Actions 上的构建与每周发布（W8）

- **`.github/workflows/release.yml`**（与 `ci.yml` 分开，`ci.yml` 不变）：每周一 00:00 UTC（北京时间 08:00）定时，另可手动运行（手动时默认只构建不发布）。
- **是否要发布**：上次发布的标签以来 `main` 上没有新合并的 PR 就跳过。第一次（没有标签时）总是构建。
- **job 划分**：`check`（Ubuntu，`go run ./tools/check`，只读 token，没有 secret）→ `build`（`windows-2025`，`go run ./tools/release` 构建、演练与签名，私钥来自只允许 `main` 部署的 `release` 环境，只读 token；产物用 `upload-artifact` 传出）→ `publish`（Ubuntu，`contents: write`，不检出、不运行仓库代码，用 `download-artifact` 取产物，核对 `SHA256SUMS` 后用 `gh` 创建标签与 Release 并上传）。`publish` 只在 ACT-009 授权后由仓库变量 `TRACE_RELEASE_PUBLISH=1` 打开；没打开时只保留构建产物。
- **固定与允许**：Action 一律固定完整 SHA；Go 用运行器自带版本或 `GOTOOLCHAIN=auto`，不加 `setup-go`；不引入第三方 Action。仓库侧的设置（`release` 环境、secret、允许列表）由用户授权 ACT-008 后由 A 完成，B 只写 workflow 与本机可做的静态检查（`tools/actcheck` 或同等检查：固定 SHA、权限、secret 只出现在 `build` 的签名步骤）。

### 4.9 文档与用户待办（W9）

需求 FR-UPD-001～004 与 FR-STORE-001 的状态、路线与验收 AC-27～AC-33、[架构](../design/architecture.md)（更新所在的层、启动参数）、[数据设计](../design/data-and-cache.md)（结构版本、备份、`settings.json` 的更新偏好）、[界面规范](../ui.md)（关于页与更新提示）、[工程规则](../dev-rules/engineering.md)（版本、命令、`dist/trace/`、密钥、release workflow、截图场景数）、[来源与安全](../dev-rules/sources-and-safety.md)（应用对 GitHub 的请求）、交付状态与阶段报告；USER-ACTIONS 的 H-002（实机：开发版自动更新、正式版安装与联网更新、SmartScreen 放行）与 H-001 第 23、24 步的安装说明。

## 5. 阶段开始的整理（C0）

1. **以前阶段的标签**：需求、设计、界面、状态、USER-ACTIONS 与开发规则里用来描述能力的旧编号（修订 3/4、R1～R13、F-0xx、P1～P16、C01～C40、“第 N 轮反馈”等）改为直接描述能力；决定表（DEC）保留编号；测试名不含阶段号或问题编号的保持不变，含的按能力改名（不弱化断言）。指向 M1 计划与报告锚点的链接已由 A 改为合并提交里的固定链接，B 不再改动。
2. **文件**：`dist/trace-0.1.0-windows-x64-*` 旧包目录不在本阶段处理（USER-ACTIONS 本机文件一节）；`tmp/`、`artifacts/` 按 DEC-025 只留最新；仓库里不再使用的文件（例如只为 M1 原型保留的代码或样本）逐个按内容决定保留、精炼或删除。不得删除的见[开发流程 §5.4](../dev-rules/workflow.md#stage-cleanup)。
3. 报告末尾附整理记录。

## 6. 必测用例

| 编号 | 用例 | 对应 |
| --- | --- | --- |
| M2-K01 | 版本号：无标签时正式版为 `0.1.1`；最高 `v0.1.3` 时为 `0.1.4`；开发版格式与 UTC 时间；标签与 Release 不一致时失败并说明 | W1、AC-27 |
| M2-K02 | 包里的程序、文件属性、卸载项、`VERSION.txt` 与“关于”的版本一致；没有注入时为 `0.0.0-dev` | W1、W6 |
| M2-K03 | `install-dev`：没装跳过；未运行直接装；运行中收到 `--quit-for-update` 后保存退出、装好、按原窗口状态重开；30 秒不退出时放弃且不强杀；`TRACE_DEV_AUTO_INSTALL=0` 与 CI 跳过；正式版不受影响 | W2、AC-28 |
| M2-K04 | `--quit-for-update`：缓存任务持久化为已暂停，录制收尾成可播放文件，界面状态保存，记录窗口状态 | W2 |
| M2-K05 | 更新状态机：启动 1 分钟与每 24 小时检查；离线静默；手动检查显示原因；自动下载开/关；已就绪提示；有任务时的确认；更新后第一次启动的说明；开发版不检查 | W3、AC-29 |
| M2-K06 | 拒绝：签名不对、别的密钥、内容被改、版本不更新、非 HTTPS、清单损坏；都保留现版本 | W4、AC-30 |
| M2-K07 | 数据结构升级前备份；旧程序遇到更新的结构拒绝写入并说明 | W4、AC-30 |
| M2-K08 | `tools/release`：缺私钥失败；工作区不干净失败（`-allow-dirty` 只演练）；产物齐全且 `SHA256SUMS` 正确；没有授权不发布 | W5、AC-31 |
| M2-K09 | ptest 安装演练：安装、经假发布源升级、篡改被拒、卸载保留数据；不碰正式版与开发版 | W5、W7、AC-31 |
| M2-K10 | 安装包：中文界面字符串、图标资源（两个通道不同）、发布者、`DisplayVersion` | W6、AC-32 |
| M2-K11 | `release.yml` 静态检查：Action 固定 SHA、各 job 权限、secret 只在签名步骤、没有授权时不发布、无新合并时跳过 | W8、AC-33 |
| M2-K12 | 回归：`go run ./tools/check`、`tools/uishots`（含新场景）、`tools/package` 与 `tools/smoke` 全部通过 | 全部 |

## 7. MVP 与 LOOP

本阶段要贯通的流程：开发版“打包 → 本机自动更新 → 重开”与正式版“发布 → 检查 → 下载校验 → 重启更新”。可记为 LOOP 的深化：差分更新的体积优化、更新说明的排版、winget 与 Microsoft Store、Windows 代码签名、测试版通道、更新失败时的回退帮助。

## 8. 交付与验证

- B 交付前运行并记录：`go run ./tools/check`；`GOOS=linux CGO_ENABLED=0 go vet ./...`；`go run ./tools/uishots -out artifacts/uishots`；`go run ./tools/package`（含本机开发版自动安装，记录 `install-dev.json`）；`go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke`；`TRACE_RUN_INSTALLER_TEST=1` 的安装演练；`go run ./tools/release -allow-dirty`（只演练，不发布）。
- 报告写在 [阶段报告](stage-report.md)，按“工作包 → 实现位置 → 验证证据 → 状态与限制”逐项对应 M2-K01～K12；not-run 照实写（例如真实 GitHub 发布、SmartScreen 体验、CI 上的 `release.yml` 运行）。
- 不 push、不建 PR、不打标签、不发布、不改仓库设置。

<a id="prompt"></a>
## 9. 给 Agent B 的交接 Prompt

```text
你是 TRACE 仓库的 Agent B，执行 M2（安装与更新）的完整实施。
- 仓库：本仓库根目录；分支 codex/m2-install-update（基点 main @ 29c2678，A 的计划提交在其上）。
- 先读 AGENTS.md、docs/delivery/stage-plan.md（TRACE-M2-01 修订 1，全部章节）、docs/decisions.md 的 DEC-041/DEC-042、USER-ACTIONS.md 的 Q-012、ACT-008、ACT-009，再按 AGENTS.md 的触发表读工程规则、来源与安全、架构与数据设计。
- 连续完成 C0、D1、W1～W9 与必测用例 M2-K01～K12，自测并修复，不逐包请求继续。非阻塞的缺项选可逆方案、记下依据后继续，不降低验收。
- 更新签名密钥已由 A 生成在仓库外（计划第 2 节），不得重新生成或覆盖；私钥不入库、不打印。
- 安装演练只用 ptest 身份；tools/package 的本机开发版自动安装是用户要求的行为，可以运行；不得安装、关闭或改动正式版 trace。测试保持安静（工程规则的安静测试）。
- 不访问 GitHub 发布、不请求哔哩哔哩；不 push、不建 PR、不打标签、不发布、不改仓库设置与 secrets。
- 完成后把实现、返工与报告全部 amend 进同一个 DCO 阶段提交（git commit --amend -s），报告写进 docs/delivery/stage-report.md（含整理记录、not-run、给 A 的审查 prompt），在回复里给出交付 SHA 和一个首选下一步。
```
