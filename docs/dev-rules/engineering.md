# 工程规则：环境、命令、门禁、Git 与生成物

技术栈与选型理由见[技术选型研究](../research/2026-11-05-stack.md)与[媒体与存储选型](../research/2026-11-05-media.md)，代码结构见[技术设计](../design/architecture.md#layout)。

<a id="environment"></a>
## 1. 环境与版本

本节只记**实际**安装和锁定的版本。按 DEC-032，全项目使用纯 Go + MyGo 原生界面（`github.com/egoist/mygo/ui`），不引入 Node.js、npm、Vite、React 或 TypeScript，也不依赖客户机 WebView2 运行时；后续开发持续与 MyGo 最新版本（当前 `v0.8.3`）同步改动。

| 组件 | 版本 | 锁定方式 | 状态（2026-11-09） |
| --- | --- | --- | --- |
| Go | `go1.27.3 windows/amd64` | `go.mod` 的 `go 1.27.3` | 本机已安装 |
| MyGo 库 / CLI | `v0.8.3` | `go.mod` 的 `tool github.com/egoist/mygo/cmd/mygo` 与 `require github.com/egoist/mygo v0.8.3` | 已锁定并实测（`go tool mygo`、`mygo/ui` 原生窗口/测试器、`mygo.Updater`） |
| Go 依赖 | `github.com/egoist/mygo v0.8.3`、`modernc.org/sqlite v1.62.0`、`github.com/Eyevinn/mp4ff v0.62.0`、`google.golang.org/protobuf v1.36.12`（`protowire`）、`github.com/yeqown/go-qrcode/v2 v2.3.0`（二维码矩阵）及传递 `github.com/yeqown/reedsolomon v1.0.0`（均 MIT）、`golang.org/x/image v0.47.0`（WebP 与图标绘制，BSD-3-Clause）、`golang.org/x/sys v0.49.0`（Windows API 与注册表） | `go.mod` 与 `go.sum` 精确版本 | 已实际接入并通过门禁；许可随包收录，二维码传递模块的许可补充见下 |
| NSIS（构建期工具） | `v3.13`（`makensis.exe`） | MyGo 缓存于 `%LOCALAPPDATA%\mygo\nsis-3.13`（zlib/libpng、bzip2、CPL 1.0 许可） | 本机已安装并生成开发版、正式版与 ptest 安装包 |
| Windows Media Foundation | Windows 10/11 系统自带（`mfplat.dll`、`mfreadwrite.dll`、`ole32.dll`） | — | 本机已有，经合成样本解码、拖动与静音测试验证 |

<a id="commands"></a>
## 2. 命令

从仓库根执行（全部为纯 Go 命令，无需 Node.js/npm）：

| 命令 | 作用 | 状态 |
| --- | --- | --- |
| `TRACE_DATA_DIR=tmp/dev-data mygo dev` | 开发模式：Go 改动自动重编译并重启原生窗口应用 | 已实测：冷编译 `2.9s`、增量重编译 `0.98s`（NFR-DEV-001，满足 10 秒以内目标） |
| `go run ./tools/check` | 全部质量门禁（见第 3 节），任一失败就以非零退出 | 已实现并实测通过；最新范围与用时见[阶段报告](../delivery/stage-report.md) |
| `go run ./tools/package [-install \| -install-only]` | 构建开发版：按[版本号](#versions)算出 `<下一个正式版>-dev.<UTC 时间>`，临时把 `mygo.json` 换成开发版配置（`trace-dev` 身份、开发版图标、发布者、中文目录，不带更新），经 `MYGO_LDFLAGS` 注入版本、通道、提交与构建时间，运行 `mygo build -platform windows/amd64`；先在 `dist/.staging-<时间>/` 生成安装包 `trace-dev-setup-<版本>-amd64.exe`、解包的 `trace.exe`、`THIRD-PARTY-NOTICES.txt`、`VERSION.txt`、`SHA256SUMS.txt` 并校验，再替换 `dist/trace-dev/` 中登记的文件。只打包，不动本机开发版；`-install` 打包后再[安装到本机](#dev-install)，`-install-only` 只安装 `dist/trace-dev/` 里现有的包。许可文件逐字收录 Go 模块和标准库的 LICENSE/NOTICE；精确版本的官方补充见下，其他缺许可模块会停止打包 | 已实现并实测通过；最新产物与安装结果见[阶段报告](../delivery/stage-report.md) |
| `go run ./tools/release [-allow-dirty] [-offline] [-plan] [-publish]` | [发布正式版](#release)：检查、构建、签名、ptest 升级演练，产物放进 `dist/trace/` | 已实现；`-allow-dirty -offline` 演练实测通过，没有发布过 |
| `go run ./tools/icons` | 重画 `assets/icon.png`（正式版：深灰底 `#3c4043` 白字 “T”，DEC-046 去掉了点）与 `assets/icon-dev.png`（开发版：同一图标加琥珀色 “DEV” 角标）；两个通道底色、字母高度（250–780）与笔画（110）相同，只差角标（2026-11-14 用户要求），1024×1024 | 已实现；图标已入库，只在改图标时运行 |
| `go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke` | 包冒烟：在临时的 `LOCALAPPDATA`、`APPDATA`、`USERPROFILE`、“文档”与 `TEMP`/`TMP` 目录下，以[安静模式](#quiet-tests)启动 `trace.exe`，检查进程存活、程序报告的版本、提交与构建时间与 `VERSION.txt` 一致、无 `msedgewebview2.exe` 子进程、`library.db`、`updates.db` 与 `mygo/` 建在数据目录里、临时 `APPDATA` 与用户主目录下没有新文件夹、单实例与数据目录锁生效、托盘关闭与重建正常、日志无凭证；`HTTP(S)_PROXY` 指向本机关闭的端口，保证不访问真实站点 | 已实现并实测通过，`quietMode=hidden`、`webview2=0`；最新内存与用时见[阶段报告](../delivery/stage-report.md) |
| `go run ./tools/uishots -out artifacts/uishots` | 界面截图与断言：用 `mygo/ui` 的 `ui.NewTester` 在 1280、1000、720 三档宽度下渲染全部必测页面与状态，直接将 `tt.Image()` 编码为 PNG 截图并写出结构化布局/无障碍断言回执 `assertions.json` | 当前 33 个场景、99 张 PNG，含单订阅主页、设置“缓存规则”、“设置 › 关于”（正式版，可更新）、更新已就绪的提示、绿色强调色预览（Q-011）、本机视频/动态/录像详情、播放选项与音量面板，以及抖音用户页、视频/本机详情、图文与配乐条、抖音登录分段、多站点账号与退避设置、多站点订阅账号列表和六处站点小字断言；最新断言结果见[阶段报告](../delivery/stage-report.md) |
| `go run ./tools/actcheck -act <act006\|act007\|act010\|act011\|act012\|act014>` | 真实站点有界检查：读取 `.local/site-check/` 的检查对象与持久化账本（发出前超额即拒），在 USER-ACTIONS 写明的额度内串行执行，遇挑战、限流或未登录立即停止，输出脱敏回执 `artifacts/site-check/<act>.json` | 已实现；合成门禁测试在 `tools/actcheck` 内，真实请求只在用户授权对应 ACT 项且账本未满时运行 |

`reedsolomon v1.0.0` 模块归档缺少 LICENSE；官方下一提交只增加 MIT LICENSE、源码未变。其固定来源、SHA-256 与精确版本补充保存在 [tools/internal/build/licenses/README.md](../../tools/internal/build/licenses/README.md)，由打包工具嵌入通知，升级到自带许可的模块后移除；打包不依赖在线查许可。

`tools/package` 与 `tools/release` 共用 `tools/internal/build`（版本号、通道配置、`-ldflags`、`VERSION.txt`、许可通知、登记替换）。登记替换先暂存全部待替换文件，再备份旧登记文件后替换。文件占用或改名失败时恢复旧文件与校验清单；恢复受阻则保留位于包旁的备份并报告位置，不能让构建临时目录清理删掉唯一旧副本。输入、旧清单与文件名先校验，未登记同名文件拒绝覆盖；正常成功或完整回滚后清理临时目录与备份。失败边界由 `tools/internal/build` 的隔离回归覆盖。`VERSION.txt` 与程序里注入的版本必须来自同一次构建，所以不再有只刷新 `VERSION.txt` 的命令。

<a id="versions"></a>
### 2.1 版本号

- **正式版**：`0.1.x`。下一个号是最高的 `v0.1.*` 标签加一，还没有标签时是 `0.1.1`（第一次发布前的开发包都标着 `0.1.0`）。`tools/release` 用 `git ls-remote --tags` 与 `gh release list` 核对，标签与已发布的 Release 不一致时停下说明；`-offline` 只看本机标签。`mygo.json` 的 `version` 只表示版本线 `0.1.0`，不为每次发布改动；升到 `0.2` 或 `1.0` 由用户决定。
- **开发版**：`<下一个正式版号>-dev.<UTC 年月日时分>`，例如 `0.1.1-dev.202611091530`；只看本机已有的标签（`git fetch --tags` 后才知道新发布），不访问 GitHub。开发版与正式版互不比较、互不更新。
- **写进程序**：版本、通道、提交短哈希（工作区有改动时加 `-dirty`）与 UTC 构建时间由 `tools/internal/build` 算出，经 `MYGO_LDFLAGS` 注入 `internal/appdirs`，并写进构建时的 `mygo.json` 副本（MyGo 用它写文件属性、安装包名与卸载项的 `DisplayVersion`）。`go run`、测试等没有注入时为 `0.0.0-dev`。“设置 › 关于”、诊断信息、日志开头与 `VERSION.txt` 写同样的字段。

<a id="dev-install"></a>
### 2.2 开发版自动安装到本机

**什么时候装**（2026-11-13 用户要求、11-14 纠正）：每次需要用户人工检查时——B 交付整个阶段、A 审查修复或返工后、按用户反馈修复后请用户复查——交接的 Agent 先执行 `go run ./tools/package -install`，让用户检查时用的就是要检查的版本，装好的版本写进交接说明与报告；不让用户自己打包或安装。某个工作包或小功能做完、只为自动验证的 `go run ./tools/package` 都不装。安装只在这个命令里，不在 `go test` 与 `tools/check` 里：

1. `TRACE_DEV_AUTO_INSTALL=0` 或 `CI=true` 时跳过并说明。
2. 读 `HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\io.github.chialecode.trace-dev` 的 `InstallLocation` 与 `DisplayVersion`；没装就跳过（第一次请手动运行安装包）。只碰开发版，不读写正式版与 ptest 的安装。
3. 开发版在运行时，用它的程序带 `--quit-for-update` 启动一次：作为第二个实例把参数转给运行中的 TRACE Dev，它不弹确认，按托盘“退出”的方式暂停缓存任务、收尾录制、保存界面状态，记下“为更新而退出”与窗口是否打开，然后退出。工具最多等 30 秒，没退出就放弃这次安装、不强杀，说明原因。
4. 用新包静默安装（`/S`），从卸载项核对已装版本等于新版本。
5. 原来在运行的，安装后重新打开：退出前窗口在托盘的带 `--background`，否则打开窗口；TRACE 自己也读那条记录决定窗口。
6. 结果写 `artifacts/package/install-dev.json`（是否安装、旧版本、新版本、是否在运行、退出用时、是否重开、跳过或失败原因）。安装失败让命令以非零退出，但不回滚已登记的包；跳过不算失败。

判断逻辑由 `tools/package` 的测试用注入的卸载项、进程与安装器覆盖（没装、未运行、运行中退出后重开、30 秒不退出、安装失败、版本不符、正式版不受影响）。

<a id="release"></a>
### 2.3 发布正式版

`go run ./tools/release`：

1. 要求工作区干净、`HEAD` 就是 `origin/main` 的最新提交（`-allow-dirty` 跳过这两条，只做演练，不能与 `-publish` 同用；`-offline` 不向 GitHub 核对版本，也不能与 `-publish` 同用）。
2. 按[版本号](#versions)算出 `0.1.x`；读更新签名私钥（见下）；读 `CHANGELOG.md` 里这一版的一节，没有时用上次发布以来 `main` 上合并的提交标题生成草稿写进去：本机运行时停下请人检查并提交，CI 上直接使用。这一节写进 `artifacts/release/notes.md`，也是 Release 的说明和 TRACE 里“更新内容”的文字。
3. 运行 `go run ./tools/check`。
4. 用正式版配置（`trace` 身份、正式版图标、`updates` 指向 GitHub Releases）与私钥构建并签名，在更新清单里加上 TRACE 的版本证明 `traceAttestation`（见[架构 §8](../design/architecture.md#updates)）；产物：安装包 `trace-setup-<版本>-amd64.exe`、更新包 `trace-<版本>-windows-amd64.tar.gz`、清单 `update-windows-amd64.json`、差分包（有上一版时，`-offline` 不做）、`install.ps1`、`VERSION.txt`、`THIRD-PARTY-NOTICES.txt`、`SHA256SUMS`。
5. 在 ptest 身份下演练（`TRACE_RUN_INSTALLER_TEST=1` 的测试跑同一段）：发现 ptest 已装时先退出并卸载、旧数据移到 `%LOCALAPPDATA%\trace-ptest.previous`；用同一份源码构建 `<版本>-rehearsal.1` 与 `.2`（只在这次演练里生成的临时密钥签名，不带差分包），静默安装 `.1`，以 `--background` 与安静模式启动并让它从本机回环地址上的假发布源（`TRACE_PTEST_UPDATE_URL`，只有 ptest 身份读它）检查、下载、校验并替换；`--quit-for-update` 让它退出后重开，核对运行的版本、卸载项版本与数据；再让它遇到一个改过内容的 `.3` 更新包并核对被拒绝、程序文件不变；最后卸载，核对卸载项与安装目录删除、数据目录保留。全程只读正式版与开发版的卸载项、前后比较不变。结果写 `artifacts/release/rehearsal.json`；演练失败就停下，`dist/trace/` 不变。
6. 校验通过后替换 `dist/trace/` 里登记在 `SHA256SUMS` 中的文件；结果写 `artifacts/release/release.json`。
7. 只有带 `-publish` 且用户已授权（ACT-009，或对话里对这一次的明确授权，以 `TRACE_RELEASE_PUBLISH=1` 表示）时，才用 `gh` 创建草稿 Release `v<版本>` 并上传（清单最后上传），核对文件齐全后改为正式的最新 Release。默认只生成文件与演练。

`-plan` 给每周的 workflow 用：输出 `release=true|false` 与 `version=`（上次发布的标签以来 `main` 没有新提交就是 `false`，还没有标签时是 `true`）。

<a id="update-key"></a>
**更新签名密钥**：Ed25519，公钥在 `mygo.json` 的 `updates.publicKey`（正式版构建内置）。私钥不入库、不打印：本机放在 `%APPDATA%\trace-release\update-signing.key`（公钥 `update-signing.pub` 在同一目录；由 A 用 `mygo keygen` 生成后改名移到这里，用户按 ACT-008 备份），`tools/release` 读入后先从自己的环境里删掉这个变量（`tools/check` 及其测试、`git`、`gh`、演练里的 TRACE 都不继承），只交给 MyGo 构建子进程的 `MYGO_UPDATER_PRIVATE_KEY` 并在进程内签版本证明；CI 上来自 `release` 环境的 secret `MYGO_UPDATER_PRIVATE_KEY`。缺私钥或私钥与公钥不配对时失败，从不生成新密钥、不打不签名的正式版。本阶段只用这一把密钥。**换密钥**（私钥泄露或计划轮换时）：① 用 `mygo keygen -o <新目录>` 生成新密钥对（它写出的是 `mygo-update.key` / `mygo-update.pub`，切换时改名为 `update-signing.key` / `update-signing.pub`），保存与备份新私钥；② 发布一个过渡版本：用**旧**私钥签名（已安装的 TRACE 只认旧公钥），程序里内置**新**公钥（MyGo 的校验公钥与 TRACE 的 `internal/update.publicKey` 都换成新的）——MyGo 的构建要求签名私钥与 `updates.publicKey` 配对，所以这一步要先改 `tools/release` 并用 ptest 演练，单独写计划；③ 过渡版本发布后，把 `mygo.json` 的 `updates.publicKey`、本机密钥文件与 CI secret 换成新密钥，之后的版本用新私钥签名；④ 没更新到过渡版本的旧版本无法再联网更新，在发布说明里请这些用户手动下载安装。私钥丢失时无法做第 ② 步，只能走第 ④ 步。

<a id="quality-gates"></a>
## 3. 质量门禁

`go run ./tools/check` 串行运行下列检查，任一失败就以非零退出：

| 检查 | 内容 |
| --- | --- |
| Go 格式与静态检查 | `gofmt -l` 输出为空；`go vet ./...` |
| Go 测试 | `go test ./...`：协议夹具与固定向量（含抖音 SM3/`a_bogus`/`x-secsdk-web-signature`）、选流、分段下载与重试、合并与单文件 MP4 `moov` 前置校验、数据结构版本迁移与 `.premigrate-1` 备份、索引重建、安全删除、信息流分页、媒体路径白名单、多站点凭证与哨兵隔离、混合站点订阅与检查更新、按站点退避与每小时额度、关注导入、登录续期、弹幕与字幕、直播 HLS fMP4 与 HTTP-FLV（AVC/HEVC `CodecID=12`/Enhanced FLV `hvc1`）录制转换、安装与数据位置、托盘与单实例、原生界面 `ui.NewTester` 测试、真实检查器账本门禁与文档检查反例 |
| 依赖方向 | `go list -deps`：`internal/...` 与 `tools/...` 中只有 `internal/shell`、`internal/ui`、`internal/player`、`internal/locales`（MyGo 自带字符串与安装界面的中文目录）、`tools/uishots` 依赖 `github.com/egoist/mygo/...`（INV-05）；`internal/site` 只用 Go 标准库（零 `internal/...` 与第三方依赖）；`internal/site/bilibili` 与 `internal/site/douyin`（含子包）不依赖 `storage`、`library`、`download`、`subscription`、`live`、`service`、`ui`，且两个站点包互不引用；`internal/site/douyin/sign` 不依赖 `internal/credential` 与 `net/http`；`download`、`live`、`subscription`、`library`、`storage`、`settings`、`uistate` 不依赖 `internal/site/douyin`；直接引用 `internal/site/bilibili` 的内部包名单锁定（`download`、`library`、`live`、`service`、`subscription`、`ui`），`storage`、`settings`、`uistate` 零 `site/bilibili` 依赖，不再新增引用者；`internal/ui` 不直接引用 `internal/site/douyin`（及 `sign`） |
| 文档与公开内容 | `go run ./tools/checkdocs`：相对链接与锚点存在；没有本机绝对路径、哔哩哔哩/抖音 Cookie 或签名参数文本（含 `sessionid=`、`sessionid_ss=`、`sid_guard=`、`msToken=`、带 `a_bogus=` 或 `x-signature` 的地址），也没有本机参考索引里登记的项目名（运行时读取索引，名单不进仓库） |
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

报告记命令、退出码、样本、实际观察、被测版本和局限。`not-run`、`blocked` 不算通过；局部通过只覆盖那个子集。只改文档时，只重跑文档检查和 `git diff --check`；源码变了，旧包和旧结果只证明原来的输入。

<a id="git"></a>
## 4. Git

默认分支 main，远端 [chialecode/trace](https://github.com/chialecode/trace)，main 只经 PR 集成、squash 合并，每个阶段在 main 上留一个提交。

- 阶段编号从 M1 开始递增（M 表示 Milestone，DEC-034）。人读标签与编号用大写，如正文和 PR 标题中的 `M2`、计划编号 `M2-W01`、`M2-A-01`、`M2-K01`；路径、文件名、分支、提交 scope、数据目录中的阶段部分用小写，如 `m2-stage-cases.json`、`codex/m2-desktop-workbench`、`feat(m1)`、`m1-ledger-3`（DEC-035）。一个阶段一个分支、一个提交、一个 PR；不按角色、轮次、工作包拆提交，push 前全部 amend 进阶段提交，提交消息在收尾时改写成对最终结果的描述。
- 阶段编号独立于 GitHub PR 编号（DEC-036）：PR 标题标明所实现的阶段，但 PR 的序号与创建顺序不决定阶段编号；未来 bot 创建的 PR 同样如此。
- 阶段参数约定：接受大小写并统一记录小写，例如 `verify-stage --stage M2` 与 `verify-stage --stage m2` 都应规范化为 `m2`（DEC-035）。当前仓库的 `tools/check`、`tools/uishots`、`tools/package`、`tools/smoke` 没有阶段参数，也没有 `verify-stage` 命令；后续建立此类工具时按本约定实现。
- 新阶段默认从更新后的 main 建分支；依赖未合并阶段时，在计划里写明基点。
- amend 前核对远端引用；已推送的提交默认追加修复，改写远端须用户另行授权，并使用指定旧 SHA 的 `--force-with-lease`。
- 提交前按明确路径暂存，审阅差异，运行相关门禁和 `git diff --check`。消息格式 `type(scope): 具体结果`，正文写最终行为、验证和限制，不写未经验收的“完成”。
- 提交带 DCO：`git commit -s` 或 `git commit --amend -s`，用当前提交者的 Git 身份。
- push、PR、合并、发布分别需要用户授权；push 只推指定分支，不用 `--all`/`--mirror`，不推备份分支和 `dist/` 包。
- 本仓设置 `core.autocrlf=false`、`pull.ff=only`、`fetch.prune=true`。

<a id="remote"></a>
### 4.1 远端保护与 CI

GitHub 上 main 的规则集：只能经 PR 写入，禁止强推和删除，保持线性历史，只允许 squash 合并，所有讨论必须解决，必需检查为 `repository-quality`；Actions 只允许固定 SHA 的 `actions/checkout`，token 只读，来自 fork 的 PR 运行 workflow 前须维护者批准（所有外部贡献者）。为防止垃圾内容，外部只开放 Discussions：关闭 Issues、Wiki 和 Projects，PR 仅协作者可创建；安全问题走私密漏洞报告。远端设置以 GitHub 实际回读为准。

CI 文件 `.github/workflows/ci.yml` 的 job 名必须是 `repository-quality`，在 Ubuntu 上运行文档检查与 `go vet`/`go test ./internal/... ./tools/...`；Go 用 runner 预装版本或 `GOTOOLCHAIN` 自动下载，不引入未许可的 action。CI 不替代本地的 Windows 构建与界面检查：Linux 上 DPAPI 与 Media Foundation 换成非 Windows 桩或合成后端（`internal/credential/dpapi_other.go`、`internal/player/mf_other.go`），依赖 Windows 文件共享锁的测试段落跳过（`internal/library/lock_other_test.go`）；没有显示器时，托盘测试里打开真实隐藏窗口的段落跳过（`internal/shell/shell_test.go`，Windows 本机照常运行）。本机没有 cgo 工具链，`go test -race` 无法运行，记为 not-run。本机只完成 Linux `go vet` 与测试包交叉编译，未运行真实 Ubuntu CI；交叉编译的退出成功不等于 Linux 测试通过。

<a id="release-workflow"></a>
### 4.2 每周发布（`release.yml`）

`.github/workflows/release.yml` 与 `ci.yml` 分开：每周一 00:00 UTC（北京时间 08:00）定时，另可手动运行（手动时默认只构建，勾选 `publish` 才发布）。顶层 `permissions: {}`，三个 job：

| job | 运行器 | 权限 | 做什么 |
| --- | --- | --- | --- |
| `check` | Ubuntu | `contents: read`，没有 secret | `go run ./tools/release -plan`：上次发布以来 `main` 没有新提交就输出 `release=false`，后面的 job 都跳过；否则运行 `go run ./tools/check` |
| `build` | `windows-2025`，`release` 环境 | `contents: read` | `go run ./tools/release`：检查、构建、签名与 ptest 演练；私钥 secret 只出现在这一步；产物（`dist/trace/` 与 `artifacts/release/`）用 `upload-artifact` 传出 |
| `publish` | Ubuntu | `contents: write` | 只在仓库变量 `TRACE_RELEASE_PUBLISH=1`（ACT-009 授权后由 A 设置）时运行；不检出、不运行仓库代码：`download-artifact` 取产物，`sha256sum -c SHA256SUMS` 与 `VERSION.txt` 核对后用 `gh` 建草稿 Release、上传（清单最后）、核对文件齐全再改为正式的最新 Release |

Action 一律固定完整 SHA（`actions/checkout` v7.0.1、`actions/upload-artifact` v7.0.2、`actions/download-artifact` v8.0.2），不加 `setup-go`（Go 用 `GOTOOLCHAIN=auto`），不引入第三方 Action。这些固定写法由 `tools/release/workflow_test.go` 静态检查（固定 SHA、各 job 权限、secret 只在签名步骤、发布受变量控制、发布 job 不运行仓库代码）。仓库侧的设置（`release` 环境只允许 `main` 部署、secret、Action 允许列表加入两个 artifact action）是 ACT-008，由用户授权后 A 完成。

<a id="generated-outputs"></a>
## 5. 生成物

运行输出只写 Git 忽略的目录：

| 位置 | 内容与去留 |
| --- | --- |
| `build/`、`tmp/` | 构建缓存与开发数据，随时可删，由构建重建 |
| `dist/<通道>/`（`dist/trace-dev/`、`dist/trace/`） | 每个通道只有最新一份（DEC-025、DEC-029）。`dist/trace-dev/`（`tools/package`）：安装包 `trace-dev-setup-<版本>-amd64.exe`，以及解包的 `trace.exe`（冒烟用）、`THIRD-PARTY-NOTICES.txt`、`VERSION.txt`、`SHA256SUMS.txt`。`dist/trace/`（`tools/release`）：Release 上传的全部文件——安装包 `trace-setup-<版本>-amd64.exe`、更新包与清单、差分包、`install.ps1`、`VERSION.txt`、`THIRD-PARTY-NOTICES.txt`，以 `SHA256SUMS` 登记。工具先在 `dist/.staging-<时间>/`（发布为 `dist/.release-*`、演练为 `dist/.rehearsal-*`，用完删除）生成并校验，再替换上一份登记的文件，目录里其他文件不动。数据不在包旁（DEC-029），替换包不会碰到用户数据。安装包由 MyGo 用 NSIS（`v3.13`，见第 1 节）生成：各通道的 `windows.installerInclude`（`assets/installer/<通道>.nsh` + `shortcuts.nsh`，由 `ChannelConfig` 设置）按 FR-STORE-001 处理快捷方式；按用户安装到 `%LOCALAPPDATA%\Programs\<目录名>`、开始菜单快捷方式、中文安装界面、可在“设置 > 应用”卸载；卸载只删安装目录。旧的带日期的包目录（`dist/trace-<版本>-windows-x64-<日期>[-序号]/`）里可能有用户数据，工具不删，按 USER-ACTIONS 的[本机文件](../../USER-ACTIONS.md#local-files)处理 |
| `artifacts/<类别>/` | 一次性脚本、截图、日志和测量结果，每类只留最近一次（DEC-025）：类别为 `smoke`、`uishots`、`package`（`install-dev.json`）、`release`（`release.json`、`rehearsal.json`、`notes.md`）、`site-check`、`review`；写新一次前，工具只清空该类别目录里的内容（确认路径在 `artifacts/<类别>/` 内、不是链接，不跨目录通配）。阶段报告引用的证据就是这一份 |
| `.local/site-secrets/`、`.local/site-check/` | 本机 Cookie 与真实检查记录；不删，不入库 |

一次性命令的输出写进所属跑次目录，不散落在仓库根。入库的只有可公开、经审阅的最终摘要，写在阶段报告里。
