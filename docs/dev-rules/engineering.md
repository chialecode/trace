# 工程规则：环境、命令、门禁、Git 与生成物

技术栈与选型理由见[技术选型研究](../research/2026-11-05-stack.md)与[媒体与存储选型](../research/2026-11-05-media.md)，代码结构见[技术设计](../design/architecture.md#layout)。

<a id="environment"></a>
## 1. 环境与版本

本节只记**实际**安装和锁定的版本。按 DEC-032，全项目使用纯 Go + MyGo 原生界面（`github.com/egoist/mygo/ui`），不引入 Node.js、npm、Vite、React 或 TypeScript，也不依赖客户机 WebView2 运行时；后续开发持续与 MyGo 最新版本（当前 `v0.8.1`）同步改动。

| 组件 | 版本 | 锁定方式 | 状态（2026-11-08） |
| --- | --- | --- | --- |
| Go | `go1.27.3 windows/amd64` | `go.mod` 的 `go 1.27.3` | 本机已安装 |
| MyGo 库 / CLI | `v0.8.1` | `go.mod` 的 `tool github.com/egoist/mygo/cmd/mygo` 与 `require github.com/egoist/mygo v0.8.1` | 已锁定并实测（`go tool mygo` 与 `mygo/ui` 原生窗口/测试器） |
| Go 依赖 | `github.com/egoist/mygo v0.8.1`、`modernc.org/sqlite v1.61.1`、`github.com/Eyevinn/mp4ff v0.62.0`、`google.golang.org/protobuf v1.36.12`（`protowire`）、`github.com/yeqown/go-qrcode/v2 v2.3.0`（二维码矩阵）及传递 `github.com/yeqown/reedsolomon v1.0.0`（均 MIT）、`golang.org/x/image v0.46.0`（WebP，BSD-3-Clause）、`golang.org/x/sys v0.49.0`（Windows API） | `go.mod` 与 `go.sum` 精确版本 | 已实际接入并通过门禁；许可随包收录，二维码传递模块的许可补充见下 |
| NSIS（构建期工具） | `v3.13`（`makensis.exe`） | MyGo 缓存于 `%LOCALAPPDATA%\mygo\nsis-3.13`（zlib/libpng、bzip2、CPL 1.0 许可） | 本机已安装并生成 `trace-dev-setup-0.1.0-amd64.exe` |
| Windows Media Foundation | Windows 10/11 系统自带（`mfplat.dll`、`mfreadwrite.dll`、`ole32.dll`） | — | 本机已有并通过 PR-1 与 P11 验证 |

<a id="commands"></a>
## 2. 命令

从仓库根执行（全部为纯 Go 命令，无需 Node.js/npm）：

| 命令 | 作用 | 状态 |
| --- | --- | --- |
| `TRACE_DATA_DIR=tmp/dev-data mygo dev` | 开发模式：Go 改动自动重编译并重启原生窗口应用 | 已实测：冷编译 `2.9s`、增量重编译 `0.98s`（NFR-DEV-001，满足 10 秒以内目标） |
| `go run ./tools/check` | 全部质量门禁（见第 3 节），任一失败就以非零退出 | 已实现并实测通过；17 个含测试包（含打包失败恢复），最新范围、缓存与用时见[阶段报告](../delivery/stage-report.md) |
| `go run ./tools/package [-channel dev\|release] [-install] [-refresh-version]` | 默认 `dev` 通道，临时设置通道 `mygo.json` 并通过 `MYGO_LDFLAGS` 注入 `appdirs.channel`，运行 `mygo build -platform windows/amd64`，先在 `dist/.staging-<时间>/` 生成安装包 `trace[-dev]-setup-0.1.0-amd64.exe`、解包的 `trace.exe`、`THIRD-PARTY-NOTICES.txt`、`VERSION.txt`、`SHA256SUMS.txt` 并校验，再替换 `dist/<通道>/` 中登记在 `SHA256SUMS.txt` 里的文件；`-install` 静默运行安装包 `/S` 安装到 `%LOCALAPPDATA%\Programs\trace-dev`；`-refresh-version` 在只更新文档或提交号后刷新 `VERSION.txt` 与 `SHA256SUMS.txt`。许可文件逐字收录 Go 模块和标准库的 LICENSE/NOTICE；精确版本的官方补充见下，其他缺许可模块会停止打包 | 已实现并实测通过；本轮未安装，最新产物/用时见[阶段报告](../delivery/stage-report.md) |
| `go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke` | 包冒烟：在临时的 `LOCALAPPDATA`、`APPDATA`、`USERPROFILE`、“文档”与 `TEMP`/`TMP` 目录下，以[安静模式](#quiet-tests)启动 `trace.exe`，检查进程存活、无 `msedgewebview2.exe` 子进程、`library.db`、`updates.db` 与 `mygo/` 建在数据目录里、临时 `APPDATA` 与用户主目录下没有新文件夹、单实例与数据目录锁生效、托盘关闭与重建正常、日志无凭证；`HTTP(S)_PROXY` 指向本机关闭的端口，保证不访问真实站点 | 已实现并实测通过，`quietMode=hidden`、`webview2=0`；最新内存与用时见[阶段报告](../delivery/stage-report.md) |
| `go run ./tools/uishots -out artifacts/uishots` | 界面截图与断言：用 `mygo/ui` 的 `ui.NewTester` 在 1280、1000、720 三档宽度下渲染全部必测页面与状态，直接将 `tt.Image()` 编码为 PNG 截图并写出结构化布局/无障碍断言回执 `assertions.json` | 当前 23 个场景、69 张 PNG，含单订阅主页、设置“缓存规则”、绿色强调色预览（Q-011）、本机视频/动态/录像详情、播放选项与音量面板；最新断言结果见[阶段报告](../delivery/stage-report.md#rev4-b-recheck) |

`reedsolomon v1.0.0` 模块归档缺少 LICENSE；官方下一提交只增加 MIT LICENSE、源码未变。其固定来源、SHA-256 与精确版本补充保存在 [tools/package/licenses/README.md](../../tools/package/licenses/README.md)，由打包工具嵌入通知，升级到自带许可的模块后移除；打包不依赖在线查许可。

打包和 `-refresh-version` 都先暂存全部待替换文件，再备份旧登记文件后替换。文件占用或改名失败时恢复旧文件与校验清单；恢复受阻则保留位于包旁的备份并报告位置，不能让构建临时目录清理删掉唯一旧副本。输入、旧清单与文件名先校验，未登记同名文件拒绝覆盖；正常成功或完整回滚后清理临时目录与备份。失败边界由 `tools/package` 的隔离回归覆盖，不关闭用户正在运行的 TRACE。

<a id="quality-gates"></a>
## 3. 质量门禁

`go run ./tools/check` 串行运行下列检查，任一失败就以非零退出：

| 检查 | 内容 |
| --- | --- |
| Go 格式与静态检查 | `gofmt -l` 输出为空；`go vet ./...` |
| Go 测试 | `go test ./...`：协议夹具与固定向量、选流、分段下载与重试、合并与校验、索引重建、安全删除、信息流分页、媒体路径白名单、凭证与哨兵隔离、订阅与检查更新、退避与额度、关注导入、登录续期、弹幕与字幕、直播录制与转换、安装与数据位置、托盘与单实例、原生界面 `ui.NewTester` 测试、文档检查反例 |
| 依赖方向 | `go list -deps`：`internal/...` 与 `tools/...` 中只有 `internal/shell`、`internal/ui`、`internal/player`、`tools/uishots` 依赖 `github.com/egoist/mygo/...`（INV-05，`internal/service` 及所有后端包零 MyGo 依赖），`internal/site/bilibili` 不依赖 `storage`、`library`、`download`、`subscription`、`live`、`service`、`ui` |
| 文档与公开内容 | `go run ./tools/checkdocs`：相对链接与锚点存在；没有本机绝对路径、Cookie 或令牌样式的文本，也没有本机参考索引里登记的项目名（运行时读取索引，名单不进仓库） |
| 空白 | `git diff --check HEAD`（只看已跟踪的文件：新文件先 `git add` 再跑门禁） |

只运行能证明风险边界的检查；不得删除、跳过或削弱有效断言来通过门禁。

<a id="quiet-tests"></a>
### 3.0 安静测试

测试、门禁、截图、冒烟和原型都不能打扰用户的前台工作：

- **不出声**：测试与工具不打开系统音频输出；播放相关的测试注入静音输出，解码测试只读取采样做断言。
- **不弹窗、不抢焦点**：界面测试在内存里运行（`ui.NewTester`）。必须建真实窗口时用安静模式（`TRACE_QUIET`）：窗口隐藏，或只显示不激活；不调用置前、聚焦。安静模式只在 `dev`/`source` 通道生效。
- **不启动外部程序**：浏览器、资源管理器、系统播放器、文件选择框、系统通知都经可替换的接口，测试里只记录调用。
- **不触发系统授权框**：测试服务器只监听 `127.0.0.1`；子进程不闪控制台窗口。
- **不碰用户已装或正在运行的 TRACE**：冒烟把 `LOCALAPPDATA`、`APPDATA`、`USERPROFILE`、“文档”和 `TEMP`/`TMP` 都指向临时目录（单实例锁按应用名放在 `TEMP` 里）。不在测试里安装、卸载或关闭 `trace` / `trace-dev`；安装包测试用单独的应用名 `trace-ptest`，默认不跑，需显式开启。
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

CI 文件 `.github/workflows/ci.yml` 由 M1 建立，job 名必须是 `repository-quality`，在 Ubuntu 上运行文档检查与 `go vet`/`go test ./internal/... ./tools/...`；Go 用 runner 预装版本或 `GOTOOLCHAIN` 自动下载，不引入未许可的 action。CI 不替代本地的 Windows 构建与界面检查：Linux 上 DPAPI 与 Media Foundation 换成非 Windows 桩或合成后端（`internal/credential/dpapi_other.go`、`internal/player/mf_other.go`），依赖 Windows 文件共享锁的测试段落跳过（`internal/library/lock_other_test.go`）。本机没有 cgo 工具链，`go test -race` 无法运行，记为 not-run。本机只完成 Linux `go vet` 与测试包交叉编译，未运行真实 Ubuntu CI；交叉编译的退出成功不等于 Linux 测试通过。

<a id="generated-outputs"></a>
## 5. 生成物

运行输出只写 Git 忽略的目录：

| 位置 | 内容与去留 |
| --- | --- |
| `build/`、`tmp/` | 构建缓存与开发数据，随时可删，由构建重建 |
| `dist/<通道>/`（`dist/trace-dev/`、`dist/trace/`） | 每个通道只有最新一份（DEC-025、DEC-029）：安装包 `trace[-dev]-setup-<版本>-amd64.exe`，以及解包的 `trace.exe`、`THIRD-PARTY-NOTICES.txt`、`SHA256SUMS.txt`、`VERSION.txt`（冒烟用）。`go run ./tools/package` 先在 `dist/.staging-<时间>/` 生成并校验，再替换上一份登记在 `SHA256SUMS.txt` 里的文件，目录里其他文件不动。数据不在包旁（DEC-029），替换包不会碰到用户数据。安装包由 MyGo 用 NSIS（`v3.13`，见第 1 节）生成（按用户安装到 `%LOCALAPPDATA%\Programs\<目录名>`、开始菜单快捷方式、可在“设置 > 应用”卸载；卸载只删安装目录）。正式版只在用户授权发布时构建与分发（签名与应用自动更新另行决定）。旧的带日期的包目录（`dist/trace-<版本>-windows-x64-<日期>[-序号]/`）里可能有用户数据，工具不删，按 USER-ACTIONS 的[本机文件](../../USER-ACTIONS.md#local-files)处理 |
| `artifacts/<类别>/` | 一次性脚本、截图、日志和测量结果，每类只留最近一次（DEC-025）：类别为 `smoke`、`uishots`、`prototypes`、`site-check`、`review`；写新一次前，工具只清空该类别目录里的内容（确认路径在 `artifacts/<类别>/` 内、不是链接，不跨目录通配）。阶段报告引用的证据就是这一份 |
| `.local/site-secrets/`、`.local/site-check/` | 本机 Cookie 与真实检查记录；不删，不入库 |

一次性命令的输出写进所属跑次目录，不散落在仓库根。入库的只有可公开、经审阅的最终摘要，写在阶段报告里。
