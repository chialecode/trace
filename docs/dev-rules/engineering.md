# 工程规则：环境、命令、门禁、Git 与生成物

技术栈与选型理由见[技术选型研究](../research/2026-11-05-stack.md)与[媒体与存储选型](../research/2026-11-05-media.md)，代码结构见[技术设计](../design/architecture.md#layout)。

<a id="environment"></a>
## 1. 环境与版本

本节只记**实际**安装和锁定的版本。按 DEC-032，全项目使用纯 Go + MyGo 原生界面（`github.com/egoist/mygo/ui`），不引入 Node.js、npm、Vite、React 或 TypeScript，也不依赖客户机 WebView2 运行时；后续开发持续与 MyGo 最新版本（当前 `v0.9.1`）同步改动。

| 组件 | 版本 | 锁定方式 | 状态（2026-11-22） |
| --- | --- | --- | --- |
| Go | `go1.27.4 windows/amd64` | `go.mod` 的 `go 1.27.4`（2026-11-14 从 1.27.3 升级，修 GO-2026-6859） | 本机已安装 1.27.3，`GOTOOLCHAIN=auto` 按 `go` 行自动下载 1.27.4 |
| MyGo 库 / CLI | `v0.9.1` | `go.mod` 的 `tool github.com/egoist/mygo/cmd/mygo` 与 `require github.com/egoist/mygo v0.9.1` | 已锁定并实测（`go tool mygo`、`mygo/ui` 原生窗口/测试器、`mygo.Updater`） |
| Go 依赖 | `github.com/egoist/mygo v0.9.1`、`modernc.org/sqlite v1.62.0`、`github.com/Eyevinn/mp4ff v0.63.0`、`google.golang.org/protobuf v1.36.12`（`protowire`）、`github.com/yeqown/go-qrcode/v2 v2.3.0`（二维码矩阵）及传递 `github.com/yeqown/reedsolomon v1.0.0`（均 MIT）、`golang.org/x/image v0.48.0`（WebP 与图标绘制，BSD-3-Clause）、`golang.org/x/sys v0.50.0`（Windows API 与注册表） | `go.mod` 与 `go.sum` 精确版本 | 已实际接入并通过门禁；许可随包收录，二维码传递模块的许可补充见下 |
| NSIS（构建期工具） | `v3.13`（`makensis.exe`） | MyGo 缓存于 `%LOCALAPPDATA%\mygo\nsis-3.13`（zlib/libpng、bzip2、CPL 1.0 许可） | 本机已安装并生成开发版、正式版与 ptest 安装包 |
| Windows Media Foundation | Windows 10/11 系统自带（`mfplat.dll`、`mfreadwrite.dll`、`ole32.dll`） | — | 本机已有，经合成样本解码、拖动与静音测试验证 |

<a id="commands"></a>
## 2. 命令

从仓库根执行（全部为纯 Go 命令，无需 Node.js/npm）：

| 命令 | 作用 | 状态 |
| --- | --- | --- |
| `TRACE_DATA_DIR=tmp/dev-data mygo dev` | 开发模式：Go 改动自动重编译并重启原生窗口应用 | 已实测：冷编译 `2.9s`、增量重编译 `0.98s`（NFR-DEV-001，满足 10 秒以内目标） |
| `go run ./tools/check` | 全部质量门禁（见第 3 节），任一失败就以非零退出 | 已实现并实测通过；最新范围与用时见[b75cf80 报告](https://github.com/chialecode/trace/blob/b75cf80/docs/delivery/stage-report.md) |
| `go run ./tools/deps [-audit]` | [依赖盘点](workflow.md#dependency-inventory)：列出直接依赖模块与 `go` 行满 7 天的新版本，只出报告；`-audit` 用固定版本的 `govulncheck` 查代码实际调用到的已知漏洞，未登记在 `tools/deps/exceptions.json` 或例外过了复查日期则退出码 1，跑不起来退出码 2（`not-run`）。[每周维护](workflow.md#dependency-inventory)与发布前各跑一次，要联网 | 已实现；2026-11-14 首次运行：盘点 2 项满龄（`golang.org/x/sys`、`go` 1.27.4），`-audit` 有 1 条被调用的标准库漏洞（升到 Go 1.27.4 可修），7 条在没调用到的依赖模块里 |
| `go run ./tools/package [-install \| -install-only]` | 构建开发版：按[版本号](#versions)算出 `<下一个正式版>-dev.<UTC 时间>`，临时把 `mygo.json` 换成开发版配置（`trace-dev` 身份、开发版图标、发布者、中文目录，不带更新），经 `MYGO_LDFLAGS` 注入版本、通道、提交与构建时间，运行 `mygo build -platform windows/amd64`；先在 `dist/.staging-<时间>/` 生成安装包 `trace-dev-setup-<版本>-amd64.exe`、解包的 `trace.exe`、`THIRD-PARTY-NOTICES.txt`、`VERSION.txt`、`SHA256SUMS.txt` 并校验，再替换 `dist/trace-dev/` 中登记的文件。只打包，不动本机开发版；`-install` 打包后再[安装到本机](#dev-install)，`-install-only` 只安装 `dist/trace-dev/` 里现有的包。许可文件逐字收录 Go 模块和标准库的 LICENSE/NOTICE；精确版本的官方补充见下，其他缺许可模块会停止打包 | 已实现并实测通过；最新产物与安装结果见[b75cf80 报告](https://github.com/chialecode/trace/blob/b75cf80/docs/delivery/stage-report.md) |
| `go run ./tools/package -server` | 构建远程版：版本号同开发版，`CGO_ENABLED=0 GOOS=linux`、`-trimpath -ldflags "-s -w …"` 交叉编译 `./cmd/trace-server` 的 `amd64` 与 `arm64`，每个架构打一个 `trace-server-<版本>-linux-<架构>.tar.gz`（顶层一个同名文件夹，含 `trace-server`、`cmd/trace-server/deploy/` 里的 `trace-server.service` 与 `README.md`、`THIRD-PARTY-NOTICES.txt`、`VERSION.txt`），以 `SHA256SUMS` 登记，先在 `dist/.staging-server-<时间>/` 生成再替换 `dist/trace-server/` 里登记的文件。不能与 `-install` 同用 | 已实现并实测通过；体积与 WSL 解包核对见[b75cf80 报告](https://github.com/chialecode/trace/blob/b75cf80/docs/delivery/stage-report.md) |
| `go run ./tools/release [-allow-dirty] [-offline] [-plan] [-publish]` | [发布正式版](#release)：检查、构建、签名、ptest 升级演练，产物放进 `dist/trace/` | 已实现；`-allow-dirty -offline` 演练实测通过，没有发布过 |
| `go run ./tools/icons` | 重画 `assets/icon.png`（正式版：深灰底 `#3c4043` 白字 “T”，DEC-046 去掉了点）与 `assets/icon-dev.png`（开发版：同一图标加琥珀色 “DEV” 角标）；两个通道底色、字母高度（250–780）与笔画（110）相同，只差角标（2026-11-14 用户要求），1024×1024；图形在 `internal/brandicon`，托盘图标用同一套图形画 32×32（[界面规范](../ui.md)） | 已实现；图标已入库，只在改图标时运行 |
| `go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke` | 包冒烟：在临时的 `LOCALAPPDATA`、`APPDATA`、`USERPROFILE`、“文档”与 `TEMP`/`TMP` 目录下，以[安静模式](#quiet-tests)启动 `trace.exe`，检查进程存活、程序报告的版本、提交与构建时间与 `VERSION.txt` 一致、无 `msedgewebview2.exe` 子进程、`library.db`、`updates.db` 与 `mygo/` 建在数据目录里、临时 `APPDATA` 与用户主目录下没有新文件夹、单实例与数据目录锁生效、托盘关闭与重建正常、日志无凭证；`HTTP(S)_PROXY` 指向本机关闭的端口，保证不访问真实站点 | 已实现并实测通过，`quietMode=hidden`、`webview2=0`；最新内存与用时见[b75cf80 报告](https://github.com/chialecode/trace/blob/b75cf80/docs/delivery/stage-report.md) |
| `go run ./tools/smoke -server [-server-bin <程序>]` | 远程冒烟（[远程 §7](../design/remote.md#status)）：不给 `-server-bin` 时用本机 `go build ./cmd/trace-server`。在临时目录 `init` → `run` → 用连接码读状态 → 同步合成订阅快照 → 冒烟程序自己充当 rclone（`internal/upload/fakerclone`）上传一个合成条目并确认本机文件已删 → 停止；Linux 发 `SIGTERM` 并确认正常退出、数据目录锁已释放，Windows 改为结束进程后再启动一次；检查数据目录文件权限（Linux）与令牌没有出现在任何文件和输出里。`HTTP(S)_PROXY` 指向本机关闭的端口。回执 `artifacts/smoke-server/smoke.json` | 已实现并实测通过（Windows 本机构建；WSL Ubuntu 用 `-server` 包里的 Linux 程序）；CI 在 `tools/check` 之后运行 |
| `go run ./tools/uishots -out artifacts/uishots` | 界面截图与断言：用 `mygo/ui` 的 `ui.NewTester` 在 1280、1000、720 三档宽度下渲染全部必测页面与状态，直接将 `tt.Image()` 编码为 PNG 截图并写出结构化布局/无障碍断言回执 `assertions.json` | 已实现并实测通过：当前 98 个场景、294 张 PNG，覆盖解析页侧栏与居中大输入框（含微博直播、X 直播与 Spaces 链接提示）、主页纯图标离线刷新按钮与五项站点筛选（全部 / B 站 / 抖音 / 微博 / X，720 宽无溢出断言）、主页缓存微博转发（`home-weibo-retweet`）与 X 筛选（`home-x`）、媒体库离线本机详情（`/library/item`）、任务页六组侧栏（下载任务虚拟化列表、缓存队列详情、直播录制行内“⚠ N 处异常”与实时预览、录制异常、含微博与 X 及直播不支持说明的检查计划、导入导出）、“我的”（折叠、哔哩哔哩四项、抖音三项、微博关注与收藏、X 关注/书签/喜欢、未登录、风控错误、窄窗选择）、订阅详情（“内容”跨站已缓存信息流、“账号”合并直播状态与微博/X 账号无录制按钮断言）、全部账号（含五项筛选、行尾“⋯”与“更改关联订阅”对话框 `move-account-picker`）、关注导入（哔哩哔哩/微博/X 站点切换）、设置“缓存规则”（含批量“选择订阅…”）、“设置 › 账号”四组（含微博与 X 不续期及风险说明）、“设置 › 远程”（含微博与 X 行、X 推送机房 IP 风险确认框 `settings-remote-push-x-confirm`、未连接/已连接/无法连接）、“设置 › 更新”及“关于”、绿色强调色预览（Q-011）、播放选项与音量面板、抖音用户页与作品/图文、登录页四站点分段（含 X 风险提示位于提交按钮上方断言 `login-x`）、微博用户页与单条微博详情，以及 X 用户页（置顶、可见上限、受保护、假空壳错误）与单条 X 帖子详情（长帖混排含 GIF、线程、转帖原帖已删、引用帖、视频、投票/文章/外链卡片、直播/Spaces 卡片、离线详情）、六处“微博”与“X”小字及订阅网格无站点小字断言 |
| `go run ./tools/actcheck -act <act006\|act007\|act010\|act011\|act012\|act014\|act015～020\|act022\|act024\|act025>` | 真实站点有界检查：读取 `.local/site-check/` 的检查对象与持久化账本（发出前超额即拒），在授权写明的额度内（[真实站点请求](workflow.md#site-requests)）串行执行，遇挑战、限流或未登录立即停止，输出脱敏回执 `artifacts/site-check/<act>.json` | 已实现（含微博 `act022`、X `act024` 与拆包补测 `act025` 的独立账本、预算门禁、停止条件与回执脱敏）；合成门禁测试在 `tools/actcheck` 内，真实请求只在对应授权有效且账本未满时运行 |

`reedsolomon v1.0.0` 模块归档缺少 LICENSE；官方下一提交只增加 MIT LICENSE、源码未变。其固定来源、SHA-256 与精确版本补充保存在 [tools/internal/build/licenses/README.md](../../tools/internal/build/licenses/README.md)，由打包工具嵌入通知，升级到自带许可的模块后移除；打包不依赖在线查许可。

`tools/package` 与 `tools/release` 共用 `tools/internal/build`（版本号、通道配置、`-ldflags`、`VERSION.txt`、许可通知、登记替换）。登记替换先暂存全部待替换文件，再备份旧登记文件后替换。文件占用或改名失败时恢复旧文件与校验清单；恢复受阻则保留位于包旁的备份并报告位置，不能让构建临时目录清理删掉唯一旧副本。输入、旧清单与文件名先校验，未登记同名文件拒绝覆盖；正常成功或完整回滚后清理临时目录与备份。失败边界由 `tools/internal/build` 的隔离回归覆盖。`VERSION.txt` 与程序里注入的版本必须来自同一次构建，所以不再有只刷新 `VERSION.txt` 的命令。

<a id="versions"></a>
### 2.1 版本号

- **正式版**：`0.1.x`。下一个号是最高的 `v0.1.*` 标签加一，还没有标签时是 `0.1.1`（第一次发布前的开发包都标着 `0.1.0`）。`tools/release` 用 `git ls-remote --tags` 与 `gh release list` 核对，标签与已发布的 Release 不一致时停下说明；`-offline` 只看本机标签。`mygo.json` 的 `version` 只表示版本线 `0.1.0`，不为每次发布改动；升到 `0.2` 或 `1.0` 由用户决定。
- **开发版**：`<下一个正式版号>-dev.<UTC 年月日时分>`，例如 `0.1.1-dev.202611091530`；只看本机已有的标签（`git fetch --tags` 后才知道新发布），不访问 GitHub。开发版与正式版互不比较、互不更新。
- **写进程序**：版本、通道、提交短哈希（工作区有改动时加 `-dirty`）与 UTC 构建时间由 `tools/internal/build` 算出，经 `MYGO_LDFLAGS` 注入 `internal/appdirs`，并写进构建时的 `mygo.json` 副本（MyGo 用它写文件属性、安装包名与卸载项的 `DisplayVersion`）。`go run`、测试等没有注入时为 `0.0.0-dev`。“设置 › 更新”（及“关于”）、诊断信息、日志开头与 `VERSION.txt` 写同样的字段。

<a id="dev-install"></a>
### 2.2 开发版自动安装到本机

**什么时候装**：每次合并进 `main` 后（本机拉取 `main` 后），以及请用户实机检查前，Agent 执行 `go run ./tools/package -install`，让本机开发版始终是最新的 `main`、用户检查时用的就是要检查的版本；装好的版本写进 PR 或 issue 评论，不让用户自己打包或安装。只为自动验证的 `go run ./tools/package` 不装。安装只在这个命令里，不在 `go test` 与 `tools/check` 里：

1. `TRACE_DEV_AUTO_INSTALL=0` 或 `CI=true` 时跳过并说明。
2. 读 `HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\io.github.chialecode.trace-dev` 的 `InstallLocation` 与 `DisplayVersion`；没装就跳过（第一次请手动运行安装包；首次安装改为自动见 [#42](https://github.com/chialecode/trace/issues/42)）。只碰开发版，不读写正式版与 ptest 的安装。
3. 开发版在运行时，用它的程序带 `--quit-for-update` 启动一次：作为第二个实例把参数转给运行中的 TRACE Dev，它不弹确认，按托盘“退出”的方式暂停缓存任务、收尾录制、保存界面状态，记下“为更新而退出”与窗口是否打开，然后退出。工具最多等 30 秒，没退出就放弃这次安装、不强杀，说明原因。
4. 用新包静默安装（`/S`），从卸载项核对已装版本等于新版本。
5. 安装成功后**总是**以 `--background` 在托盘打开 TRACE Dev（[DEC-052](../decisions.md#dec-052)，用户常驻运行开发版），不论原来是否在运行、窗口是否打开；TRACE 遇到“为本机安装而退出”的记录且带 `--background` 时留在托盘（正式版应用内更新仍按记录恢复窗口）。工具本身在作业对象里时（Agent 命令沙箱可能禁止子进程脱离作业、命令结束即结束作业里的全部进程），先请桌面外壳启动 TRACE Dev（`build.LaunchInstalledOutsideJob`：explorer.exe 的 `Shell.Application.ShellExecute`，再等它出现并取进程号）：它成为 explorer 的子进程，不在命令的任何作业里，环境变量取 explorer 的（2026-11-22 用户要求）。外壳启动不了，或工具不在作业里时，请求新进程脱离当前命令的作业对象（`CREATE_BREAKAWAY_FROM_JOB`）；作业不允许脱离时照常启动，并在结果里记 `inJob=true`、提示从开始菜单打开一次。发布演练要给程序指定环境变量，仍只用脱离作业的方式启动。
6. 结果写 `artifacts/package/install-dev.json`（是否安装、旧版本、新版本、是否在运行、退出用时、是否已在托盘打开、是否仍在作业对象里、跳过或失败原因）。安装失败让命令以非零退出，但不回滚已登记的包；跳过不算失败。

判断逻辑由 `tools/package` 的测试用注入的卸载项、进程与安装器覆盖（没装、未运行时装后在托盘打开、运行中退出后在托盘重开、仍在作业对象里、30 秒不退出、安装失败、版本不符、正式版不受影响）。

<a id="release"></a>
### 2.3 发布正式版

`go run ./tools/release`：

1. 要求工作区干净、`HEAD` 就是 `origin/main` 的最新提交（`-allow-dirty` 跳过这两条，只做演练，不能与 `-publish` 同用；`-offline` 不向 GitHub 核对版本，也不能与 `-publish` 同用）。
2. 按[版本号](#versions)算出 `0.1.x`；读更新签名私钥（见下）；读 `CHANGELOG.md` 里这一版的一节，没有时用上次发布以来 `main` 上合并的提交标题生成草稿写进去：本机运行时停下请人检查并提交，CI 上直接使用。这一节写进 `artifacts/release/notes.md`，也是 Release 的说明和 TRACE 里“更新内容”的文字。
3. 运行 `go run ./tools/check`。
4. 用正式版配置（`trace` 身份、正式版图标、`updates` 指向 GitHub Releases）与私钥构建并签名，在更新清单里加上 TRACE 的版本证明 `traceAttestation`（见[架构 §8](../design/architecture.md#updates)）；产物：安装包 `trace-setup-<版本>-amd64.exe`、更新包 `trace-<版本>-windows-amd64.tar.gz`、清单 `update-windows-amd64.json`、差分包（有上一版时，`-offline` 不做）、`install.ps1`、`VERSION.txt`、`THIRD-PARTY-NOTICES.txt`、`SHA256SUMS`。
5. 在 ptest 身份下演练（`TRACE_RUN_INSTALLER_TEST=1` 的测试跑同一段）：发现 ptest 已装时先退出并卸载、旧数据移到 `%LOCALAPPDATA%\trace-ptest.previous`；用同一份源码构建 `<版本>-rehearsal.1` 与 `.2`（只在这次演练里生成的临时密钥签名，不带差分包），静默安装 `.1`，以 `--background` 与安静模式启动并让它从本机回环地址上的假发布源（`TRACE_PTEST_UPDATE_URL`，只有 ptest 身份读它）检查、下载、校验并替换；`--quit-for-update` 让它退出后重开，核对运行的版本、卸载项版本与数据；再让它遇到一个改过内容的 `.3` 更新包并核对被拒绝、程序文件不变；最后卸载，核对卸载项与安装目录删除、数据目录保留。全程只读正式版与开发版的卸载项、前后比较不变。结果写 `artifacts/release/rehearsal.json`；演练失败就停下，`dist/trace/` 不变。
6. 校验通过后替换 `dist/trace/` 里登记在 `SHA256SUMS` 中的文件；结果写 `artifacts/release/release.json`。
7. 只有带 `-publish` 且用户已授权（每周自动发布已按原 [ACT-009](https://github.com/chialecode/trace/blob/f8af16863dbe885cfd63de048730c07a2b91e7be/USER-ACTIONS.md#act-009) 授权打开，或对话里对这一次的明确授权，以 `TRACE_RELEASE_PUBLISH=1` 表示）时，才用 `gh` 创建草稿 Release `v<版本>` 并上传（清单最后上传），核对文件齐全后改为正式的最新 Release。默认只生成文件与演练。

`-plan` 给每周的 workflow 用：输出 `release=true|false` 与 `version=`（上次发布的标签以来 `main` 没有新提交就是 `false`，还没有标签时是 `true`）。

<a id="update-key"></a>
**更新签名密钥**：Ed25519，公钥在 `mygo.json` 的 `updates.publicKey`（正式版构建内置）。私钥不入库、不打印：本机放在 `%APPDATA%\trace-release\update-signing.key`（公钥 `update-signing.pub` 在同一目录；由 A 用 `mygo keygen` 生成后改名移到这里，用户按 ACT-008 备份），`tools/release` 读入后先从自己的环境里删掉这个变量（`tools/check` 及其测试、`git`、`gh`、演练里的 TRACE 都不继承），只交给 MyGo 构建子进程的 `MYGO_UPDATER_PRIVATE_KEY` 并在进程内签版本证明；CI 上来自 `release` 环境的 secret `MYGO_UPDATER_PRIVATE_KEY`。缺私钥或私钥与公钥不配对时失败，从不生成新密钥、不打不签名的正式版。目前只用这一把密钥。**换密钥**（私钥泄露或计划轮换时）：① 用 `mygo keygen -o <新目录>` 生成新密钥对（它写出的是 `mygo-update.key` / `mygo-update.pub`，切换时改名为 `update-signing.key` / `update-signing.pub`），保存与备份新私钥；② 发布一个过渡版本：用**旧**私钥签名（已安装的 TRACE 只认旧公钥），程序里内置**新**公钥（MyGo 的校验公钥与 TRACE 的 `internal/update.publicKey` 都换成新的）——MyGo 的构建要求签名私钥与 `updates.publicKey` 配对，所以这一步要先改 `tools/release` 并用 ptest 演练，单独写计划；③ 过渡版本发布后，把 `mygo.json` 的 `updates.publicKey`、本机密钥文件与 CI secret 换成新密钥，之后的版本用新私钥签名；④ 没更新到过渡版本的旧版本无法再联网更新，在发布说明里请这些用户手动下载安装。私钥丢失时无法做第 ② 步，只能走第 ④ 步。

<a id="quality-gates"></a>
## 3. 质量门禁

`go run ./tools/check` 串行运行下列检查，任一失败就以非零退出：

| 检查 | 内容 |
| --- | --- |
| Go 格式与静态检查 | `gofmt -l main.go cmd internal tools` 输出为空；`go vet ./...` |
| Go 测试 | `go test ./...`：协议夹具与固定向量（含抖音 SM3/`a_bogus`/`x-secsdk-web-signature`、微博 Base62 与 PC 两步登录、X 雪花 ID、webpack/Vite 与懒加载分包前端元数据、`x-client-transaction-id` 黄金向量、X 两步登录与 `ct0` 同步）、选流、分段下载与重试、合并与单文件 MP4 `moov` 前置校验（含微博 QuickTime `.mov` 实况片段无损改封装与新浪失效占位图回退、X GIF 与 HLS-only 缺失记录）、数据结构版本迁移（含 `subscriptions.json`/`ui-state.json` v3→v4 与 `cache-queue.json` v2→v3）与 `.premigrate-*` 备份、无锁增量索引重建与轻量任务摘要查询、安全删除、信息流分页、媒体路径白名单、四站点凭证与哨兵隔离、混合站点订阅与检查更新、按站点退避/主动刹车与每小时额度、多站点关注导入、登录续期、弹幕与字幕、直播 HLS fMP4（含续录 `lastSequenceAt` 跳号防误判、FLV/HLS 序号隔离与瞬时损坏重连）与 HTTP-FLV（AVC/HEVC `CodecID=12`/Enhanced FLV `hvc1`）录制转换及微博/X 零请求排除、安装与数据位置、托盘与单实例、原生界面 `ui.NewTester` 测试、真实检查器账本门禁与文档检查反例 |
| 依赖方向 | `go list -deps`：`internal/...` 与 `tools/...` 中只有 `internal/shell`、`internal/ui`、`internal/player`、`internal/locales`（MyGo 自带字符串与安装界面的中文目录）、`tools/uishots` 依赖 `github.com/egoist/mygo/...`（INV-05）；`internal/site` 只用 Go 标准库（零 `internal/...` 与第三方依赖）；`internal/site/bilibili`、`internal/site/douyin`（含子包）、`internal/site/weibo`（含子包）与 `internal/site/x`（含子包）不依赖 `storage`、`library`、`download`、`subscription`、`live`、`service`、`ui`，且四个站点包之间互不引用；`internal/site/douyin/sign` 不依赖 `internal/credential` 与 `net/http`；`download`、`live`、`subscription`、`library`、`storage`、`settings`、`uistate` 不依赖 `internal/site/douyin`、`internal/site/weibo` 或 `internal/site/x`；直接引用 `internal/site/bilibili` 的内部包名单锁定（`download`、`library`、`live`、`service`、`subscription`、`ui`），`storage`、`settings`、`uistate` 零 `site/bilibili` 依赖，不再新增引用者；`internal/ui` 不直接引用 `internal/site/douyin`（及 `sign`）、`internal/site/weibo` 或 `internal/site/x`（只用服务层 DTO）；`cmd/trace-server` 不依赖 MyGo、`internal/ui`、`internal/player`、`internal/shell`、`internal/locales`（检查范围含 `./cmd/...`） |
| 远程版构建 | `CGO_ENABLED=0 GOOS=linux` 下 `GOARCH=amd64` 与 `arm64` 各构建一次 `./cmd/trace-server`，再 `GOOS=linux go vet ./...` |
| 文档与公开内容 | `go run ./tools/checkdocs`：相对链接与锚点存在；没有本机绝对路径、哔哩哔哩/抖音/微博/X Cookie、Bearer 令牌或签名参数文本（含 `sessionid=`、`sessionid_ss=`、`sid_guard=`、`msToken=`、带 `a_bogus=` 或 `x-signature` 的地址，微博 `SUB=`、`SUBP=`、`XSRF-TOKEN=`、`WBPSESS=`、`ssig=`、`Expires=`、`KID=`，以及 X `auth_token=`、`ct0=`、`kdt=`、`x-csrf-token`、`Authorization: Bearer` 令牌与带签名参数的 `twimg.com`/`pscp.tv`/`periscope.tv` 或私有 `ton.x.com` 地址，且不误报协议文档里的合成占位），也没有本机参考索引里登记的项目名（运行时读取索引，名单不进仓库） |
| 空白 | `git diff --check HEAD`（只看已跟踪的文件：新文件先 `git add` 再跑门禁） |

只运行能证明风险边界的检查；不得删除、跳过或削弱有效断言来通过门禁。

<a id="quiet-tests"></a>
### 3.0 安静测试

测试、门禁、截图、冒烟和原型都不能打扰用户的前台工作：

- **不出声**：测试与工具不打开系统音频输出；播放相关的测试注入静音输出，解码测试只读取采样做断言。
- **不弹窗、不抢焦点**：界面测试在内存里运行（`ui.NewTester`）。必须建真实窗口时用安静模式（`TRACE_QUIET`）：窗口隐藏，或只显示不激活；不调用置前、聚焦。安静模式只在 `dev`/`source` 通道生效。
- **不启动外部程序**：浏览器、资源管理器、系统播放器、文件选择框、系统通知都经可替换的接口，测试里只记录调用。
- **不触发系统授权框**：测试服务器只监听 `127.0.0.1`；子进程不闪控制台窗口。
- **不碰用户已装或正在运行的 TRACE**：冒烟把 `LOCALAPPDATA`、`APPDATA`、`USERPROFILE`、“文档”和 `TEMP`/`TMP` 都指向临时目录（单实例锁按应用名放在 `TEMP` 里）。不在测试里安装、卸载或关闭 `trace` / `trace-dev`；安装与更新的演练只用单独的身份 `trace-ptest`（`TRACE_RUN_INSTALLER_TEST=1` 的测试与 `tools/release`），以 `--background` 与安静模式运行、缓存目录放在临时目录。`tools/package` 关闭并更新本机 TRACE Dev 是用户要求的打包步骤，不属于测试（见 [2.2](#dev-install)）。
- **少占资源**：打包、大文件和解码类测试用低优先级运行；大样本用稀疏文件，并且默认不跑。

<a id="bounded-validation"></a>
### 3.1 有界验证

日常开发和每轮审查都按约 10 分钟预算验证：优先覆盖改动的主流程、关键失败路径、类型与格式和文档检查；复用输入未变的已有证据。记录实际命令、用时、通过范围和未执行项。预计超出预算的长测，先单独请用户授权。短测、超时、中止都不能冒充完整通过。

### 3.2 结果与证据

PR 的“验证”一节记命令、退出码、样本、实际观察、被测版本和局限。`not-run`、`blocked` 不算通过；局部通过只覆盖那个子集。只改文档时，只重跑文档检查和 `git diff --check`；源码变了，旧包和旧结果只证明原来的输入。

<a id="git"></a>
## 4. Git

默认分支 `main`，远端 [chialecode/trace](https://github.com/chialecode/trace)，`main` 只经 PR 集成、squash 合并。

- 一个 issue 一个小 PR，分支 `<type>/<issue号>-<简短描述>`（如 `fix/42-tray-restore`），Conventional Commits，每个提交 `git commit -s`（DCO，用当前提交者的 Git 身份）；PR 合并时 squash 成一个提交，分支里提交几次都可以（[每次改动的流程](workflow.md#change-loop)）。
- 阶段编号（`M1`～，M 表示 Milestone，DEC-034）只用于路线与验收：人读标签大写，机器标识中的阶段部分小写，命令行阶段参数接受大小写并规范化为小写（DEC-035）；阶段编号独立于 GitHub PR 与 issue 编号（DEC-036）。
- 提交前按明确路径暂存，审阅差异，运行相关门禁和 `git diff --check`。消息格式 `type(scope): 具体结果`，正文写最终行为、验证和限制，不写未经验收的“完成”。
- push 与建 PR 长期授权；低风险 PR 由 Agent 在 CI 与独立审查通过后用 `gh pr merge <号> --squash --match-head-commit <头提交>` 合并，[高风险](workflow.md#high-risk) PR 由用户合并；发布正式版、改远端设置与规则集、改写已推送历史需要用户对具体动作授权（改写时用指定旧 SHA 的 `--force-with-lease`）。
- push 只推指定分支，不用 `--all`/`--mirror`，不推备份分支和 `dist/` 包；不轮询长挂命令（如 `gh pr checks --watch`）。
- 本仓设置 `core.autocrlf=false`、`pull.ff=only`、`fetch.prune=true`。

<a id="remote"></a>
<a id="remote-ci"></a>
### 4.1 远端保护与 CI

GitHub 上 main 的规则集：只能经 PR 写入，禁止强推和删除，保持线性历史，只允许 squash 合并，所有讨论必须解决，必需检查为 `repository-quality`；Actions 只允许 `actions/checkout`、`actions/upload-artifact`、`actions/download-artifact` 且远端强制固定完整 SHA，token 只读，来自 fork 的 PR 运行 workflow 前须维护者批准（所有外部贡献者）。仓库开启 [Issues](https://github.com/chialecode/trace/issues)（只允许协作者创建）与 [Discussions](https://github.com/chialecode/trace/discussions)，关闭 Wiki 与 Projects；PR 仅协作者可创建；允许自动合并与更新分支，合并后删除分支；网页提交要求 DCO 签名；安全问题走私密漏洞报告。远端设置的镜像是 [`.github/repository-settings.json`](../../.github/repository-settings.json)、[`.github/rulesets/main.json`](../../.github/rulesets/main.json) 与 [`.github/actions-policy.json`](../../.github/actions-policy.json)，以 GitHub 实际回读为准，改远端设置时同步更新镜像。

CI 文件 `.github/workflows/ci.yml` 的 job 名必须是 `repository-quality`，在 Ubuntu 上运行 `go run ./tools/check`，再运行远程冒烟 `go run ./tools/smoke -server`（真实 Linux 进程与 `SIGTERM`）；Go 用 runner 预装版本或 `GOTOOLCHAIN` 自动下载，不引入未许可的 action。CI 不替代本地的 Windows 构建与界面检查：Linux 上 DPAPI 与 Media Foundation 换成非 Windows 桩或合成后端（`internal/credential/dpapi_other.go`、`internal/player/mf_other.go`），依赖 Windows 文件共享锁的测试段落跳过（`internal/library/lock_other_test.go`）；没有显示器时，托盘测试里打开真实隐藏窗口的段落跳过（`internal/shell/shell_test.go`，Windows 本机照常运行）。本机没有 cgo 工具链，`go test -race` 无法运行，记为 not-run。本机只完成 Linux `go vet` 与测试包交叉编译，未运行真实 Ubuntu CI；交叉编译的退出成功不等于 Linux 测试通过。

**Dependabot**（[.github/dependabot.yml](../../.github/dependabot.yml)，DEC-048）：Go 模块每周、Actions 每月，冷却期 7 天，同主版本更新合为一组（`golang.org/x/*` 单独一组），关闭自动 rebase，不自动合并。版本更新 PR 在[每周维护](workflow.md#dependency-inventory)时处理，CI 通过后按低风险合并。Dependabot 告警与自动安全修复 PR 已打开（2026-11-14 用户授权，Agent 经 API 打开并核对）；安全修复 PR 不受冷却期限制，有就尽快评审合并。

<a id="release-workflow"></a>
### 4.2 每周发布（`release.yml`）

`.github/workflows/release.yml` 与 `ci.yml` 分开：每周一 00:00 UTC（北京时间 08:00）定时，另可手动运行（手动时默认只构建，勾选 `publish` 才发布）。顶层 `permissions: {}`，三个 job：

| job | 运行器 | 权限 | 做什么 |
| --- | --- | --- | --- |
| `check` | Ubuntu | `contents: read`，没有 secret | `go run ./tools/release -plan`：上次发布以来 `main` 没有新提交就输出 `release=false`，后面的 job 都跳过；否则运行 `go run ./tools/check` |
| `build` | `windows-2025`，`release` 环境 | `contents: read` | `go run ./tools/release`：检查、构建、签名与 ptest 演练；私钥 secret 只出现在这一步；产物（`dist/trace/` 与 `artifacts/release/`）用 `upload-artifact` 传出 |
| `publish` | Ubuntu | `contents: write` | 只在仓库变量 `TRACE_RELEASE_PUBLISH=1`（用户授权每周自动发布后设置）时运行；不检出、不运行仓库代码：`download-artifact` 取产物，`sha256sum -c SHA256SUMS` 与 `VERSION.txt` 核对后用 `gh` 建草稿 Release、上传（清单最后）、核对文件齐全再改为正式的最新 Release |

Action 一律固定完整 SHA（`actions/checkout` v7.0.1、`actions/upload-artifact` v7.0.2、`actions/download-artifact` v8.0.2），不加 `setup-go`（Go 用 `GOTOOLCHAIN=auto`），不引入第三方 Action。这些固定写法由 `tools/release/workflow_test.go` 静态检查（固定 SHA、各 job 权限、secret 只在签名步骤、发布受变量控制、发布 job 不运行仓库代码）。仓库侧的设置（`release` 环境只允许 `main` 部署、secret `MYGO_UPDATER_PRIVATE_KEY`、Action 允许列表）已在 2026-11-09 完成；私钥备份见 [#20](https://github.com/chialecode/trace/issues/20)。

<a id="generated-outputs"></a>
## 5. 生成物

运行输出只写 Git 忽略的目录：

| 位置 | 内容与去留 |
| --- | --- |
| `build/`、`tmp/` | 构建缓存与开发数据，随时可删，由构建重建 |
| `dist/<通道>/`（`dist/trace-dev/`、`dist/trace/`、`dist/trace-server/`） | 每个通道只有最新一份（DEC-025、DEC-029）。`dist/trace-server/`（`tools/package -server`）：两个架构的 `trace-server-<版本>-linux-<架构>.tar.gz` 与 `SHA256SUMS`。`dist/trace-dev/`（`tools/package`）：安装包 `trace-dev-setup-<版本>-amd64.exe`，以及解包的 `trace.exe`（冒烟用）、`THIRD-PARTY-NOTICES.txt`、`VERSION.txt`、`SHA256SUMS.txt`。`dist/trace/`（`tools/release`）：Release 上传的全部文件——安装包 `trace-setup-<版本>-amd64.exe`、更新包与清单、差分包、`install.ps1`、`VERSION.txt`、`THIRD-PARTY-NOTICES.txt`，以 `SHA256SUMS` 登记。工具先在 `dist/.staging-<时间>/`（发布为 `dist/.release-*`、演练为 `dist/.rehearsal-*`，用完删除）生成并校验，再替换上一份登记的文件，目录里其他文件不动。数据不在包旁（DEC-029），替换包不会碰到用户数据。安装包由 MyGo 用 NSIS（`v3.13`，见第 1 节）生成：各通道的 `windows.installerInclude`（`assets/installer/<通道>.nsh` + `shortcuts.nsh`，由 `ChannelConfig` 设置）按 FR-STORE-001 处理快捷方式；按用户安装到 `%LOCALAPPDATA%\Programs\<目录名>`、开始菜单快捷方式、中文安装界面、可在“设置 > 应用”卸载；卸载只删安装目录。旧的带日期的包目录（`dist/trace-<版本>-windows-x64-<日期>[-序号]/`）里可能有用户数据，工具不删，按 [#27](https://github.com/chialecode/trace/issues/27) 处理 |
| `artifacts/<类别>/` | 一次性脚本、截图、日志和测量结果，每类只留最近一次（DEC-025）：类别为 `smoke`、`smoke-server`（远程冒烟；WSL 里用 Linux 包跑的那次另放 `smoke-server-linux`）、`uishots`、`package`（`install-dev.json`）、`release`（`release.json`、`rehearsal.json`、`notes.md`）、`site-check`、`review`；写新一次前，工具只清空该类别目录里的内容（确认路径在 `artifacts/<类别>/` 内、不是链接，不跨目录通配）。PR 里引用的证据就是这一份 |
| `.local/site-secrets/`、`.local/site-check/` | 本机 Cookie 与真实检查记录；不删，不入库 |

一次性命令的输出写进所属跑次目录，不散落在仓库根。入库的只有可公开、经审阅的最终摘要，写在 PR 里。
