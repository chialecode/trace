# 阶段报告：M2（安装与更新）

- **计划**：[TRACE-M2-01 修订 1](stage-plan.md)。
- **状态**：B 已于 2026-11-09 完成 C0、D1、W1～W9 与 M2-K01～K12 的实施与自测；A 同日集中审查完毕（[第 8 节](#review)），两处局部问题已修复并 amend，没有交回 B 的缺口。可以请用户授权 push 与 PR。产品验收没有开始（H-001、H-002 为 not-run）；没有发布过正式版。
- **上一阶段**：M1 的完整报告在合并提交里：[M1 报告](https://github.com/chialecode/trace/blob/29c2678/docs/delivery/stage-report.md)。

## 1. 执行范围与工作区

| 信息 | 内容 |
| --- | --- |
| 执行范围 | 计划第 1 节全部内容；没有缩减验收。B 实施时的偏差与理由见第 4 节，都是可逆的 |
| 分支与基点 | `codex/m2-install-update`，基点 `main @ 29c2678`；A 的计划提交与 B 的实现、自测返工、本报告合在同一个 DCO 提交里（交付 SHA 在交付消息里给出） |
| Git 状态 | 没有 push、没有 PR、没有标签、没有发布；没有改仓库设置与 secrets |
| 未提交文件 | `tmp/`（B 的编辑脚本与验证日志）、`dist/`、`artifacts/`（忽略的生成物，按 DEC-025 只留最新） |
| 外部请求 | 没有请求哔哩哔哩；没有访问 GitHub 发布（`tools/release` 用 `-offline` 运行，见第 4 节） |
| 更新签名密钥 | 用 A 生成在仓库外的密钥（[工程规则 §2.3](../dev-rules/engineering.md#update-key)）：`tools/release` 在进程内读取并核对它与 `mygo.json` 的公钥一致；没有重新生成、覆盖或打印。升级演练另用进程内的临时密钥 |
| 本机安装 | B 只装过、卸过 ptest 身份（TRACE PTest）。A 审查时用户已手动装上 TRACE Dev 并开着它：A 的 `go run ./tools/package` 按设计让它退出、更新并重开（第 8 节）；ptest 演练又装卸一次。正式版没有安装在本机，没有被安装、关闭或改动 |

## 2. 可运行产物

| 产物 | 位置 | 说明 |
| --- | --- | --- |
| 开发版 | `dist/trace-dev/`（`go run ./tools/package`） | A 复验时从交付提交 `6fe74e0` 构建：版本 `0.1.1-dev.202611091059`；安装包 `trace-dev-setup-0.1.1-dev.202611091059-amd64.exe`，SHA-256 `4603a9c60a3659a732503973de42324566a333de9b35c552608df8ee3aa85a52`；`VERSION.txt`、`SHA256SUMS.txt`、`THIRD-PARTY-NOTICES.txt`。A 的修复只改 `tools/release` 与文档，程序内容不变 |
| 正式版演练包 | `dist/trace/`（`go run ./tools/release -allow-dirty -offline`） | 版本 `0.1.1`，用真实更新密钥签名、带版本证明；安装包、更新包 `tar.gz`、清单 `update-windows-amd64.json`、`install.ps1`、`VERSION.txt`、`THIRD-PARTY-NOTICES.txt`、`SHA256SUMS`。A 修复后的最后一次演练见第 8 节。`-allow-dirty` 的产物只能演练，不能发布 |
| 证据 | `artifacts/package/install-dev.json`、`artifacts/smoke/`、`artifacts/uishots/`（含新场景 `settings-about`、`update-ready`）、`artifacts/release/`（`notes.md`、`rehearsal.json`、`release.json`） | 生成物不入库 |

## 3. 工作包与必测用例

| 用例 / 工作包 | 实现位置 | 证据（测试函数与命令） | 状态与限制 |
| --- | --- | --- | --- |
| **M2-K01** 版本号（W1） | `tools/internal/build/build.go`（`NextStableVersion`、`DevVersion`、`LatestTag`、标签与 Release 的核对）；`tools/release/main.go` 的 `-plan` | `TestNextStableVersion`（无标签 `0.1.1`，最高 `v0.1.3` 得 `0.1.4`）、`TestDevVersionUsesUTCMinute`、`TestTagsAndReleasesMustAgree`（`TestNextStableVersion` 里另测按数字取最高标签） | passed。开发版只看本机标签，不联网 |
| **M2-K02** 版本一致（W1、W6） | `internal/appdirs`（`BuildInfo`，`-ldflags -X` 注入）；`tools/internal/build`（`LDFlags`、`VERSION.txt`、`WithConfig` 写 `mygo.json` 的版本）；`internal/ui/updates_view.go`（关于页）；`internal/shell/uninstall_entry*.go`（卸载项） | `TestBuildInfoDefaultsAndInjected`（未注入为 `0.0.0-dev`）、`TestLDFlagsCarryBuildInfo`、`TestVersionFileRoundTrip`、`TestSyncUninstallVersion`；`tools/smoke` 核对运行中程序报告的版本、提交、构建时间、通道与 `VERSION.txt` 一致（`versionMatchesVersionTxt: true`）；ptest 演练核对安装后与更新后卸载项的 `DisplayVersion`；`trace.exe` 文件属性有产品名、版本、发布者与版权 | passed。NSIS 安装包本身没有文件属性里的版本（MyGo 的 NSIS 脚本不写），版本在文件名与卸载项里 |
| **M2-K03** `install-dev`（W2） | `tools/package/install.go`（`devInstaller.Run`、`skipReason`、`restartWindowState`）、`install_windows.go`；`tools/internal/build/winsys_windows.go`（卸载项与进程读取、静默运行、分离启动） | `TestInstallDevSkipsWhenNotInstalled`、`TestInstallDevWhenNotRunning`、`TestInstallDevQuitsInstallsAndReopensAsItWas`（窗口 / 托盘 / 记录缺失三种）、`TestInstallDevGivesUpWhenTraceDevDoesNotQuit`（30 秒、不强杀）、`TestInstallDevFailures`、`TestInstallDevSkipsOnRequestAndOnCI`、`TestRestartWindowStateReadsRecentRecord`、`TestWindowsReadersOnlyRead`；`go run ./tools/package` 实跑：退出码 0、11 秒；`install-dev.json` 为 `skipped: true`，原因“本机没有安装 TRACE Dev” | passed。B 交付时本机没装 TRACE Dev，`install-dev.json` 为跳过；A 审查时用户已装上并开着它，A 实跑 `go run ./tools/package`：`wasRunning: true`、306 ms 内自己退出（TRACE Dev 日志：`work paused for exit`、`quitting for update (dev-install, window open: true)`）、`0.1.1-dev.202611090316 → 0.1.1-dev.202611091059`、`relaunched: true`（按原样打开窗口），共 1508 ms。只按身份 `trace-dev` 查找与操作，正式版不受影响；带缓存任务的人工检查仍在 H-002 第 2 步 |
| **M2-K04** `--quit-for-update`（W2） | `internal/shell`（启动参数、单实例转发、退出路径与 `update.restart` 记录）；`internal/service`（`PauseForExit`）；`internal/live`（录制收尾） | `TestQuitForUpdateSavesWorkAndWindowState`、`TestParseLaunchArgs`、`TestDecideStartupAfterUpdate`、`TestPauseForExitKeepsTasksPausedForRestart`、`TestStopRecordingWhileLiveFinalizesPlayableFile` | passed |
| **M2-K05** 更新状态机（W3） | `internal/update/controller.go`；`internal/shell/updates.go`（接到 `mygo.Updater`）；`internal/ui/updates_view.go`（关于页、顶部提示、确认框）；`internal/settings`、`internal/uistate`（偏好与状态） | `TestUpdateScheduleFirstMinuteThenDaily`、`TestUpdateOfflineAutomaticCheckIsSilent`、`TestUpdateManualCheckReportsResult`、`TestUpdateAutoDownloadOnReadyBanner`、`TestUpdateAutoDownloadOffOnlyOffers`、`TestUpdateFailedInstallKeepsCurrentVersion`、`TestUpdateRestartPromptAndDisabledChannel`（含开发版不检查）、`TestUpdateNoticeAfterUpdate`；`tools/uishots` 的 `settings-about`、`update-ready` 场景（必需的无障碍标签断言通过，B 看过三个宽度的截图） | passed。真实网络上的检查与下载 not-run（还没有发布） |
| **M2-K06** 拒绝不可信更新（W4） | `internal/update/source.go`（HTTPS 与重定向、清单大小与字段、版本证明、只开清单里的文件、边读边核对） | `TestUpdateSourceRejections`（签名不对、别的密钥、内容被改、版本证明不符）、`TestUpdateSourceCorruptManifestRejected`、`TestUpdateSourceNotNewerIsNoUpdate`、`TestUpdateSourceRefusesPlainHTTPFeed`、`TestUpdateSourceRefusesRedirectToHTTP`、`TestUpdateSourceOpensOnlyManifestFiles`、`TestUpdateSourceAcceptsSignedNewerRelease`、`TestVersionCompare`；ptest 演练里被改过一个字节的 `.3` 更新包被拒，日志 `update 0.1.1-rehearsal.3 not installed: ... is not signed with the app's key`，版本保持 `.2` | passed |
| **M2-K07** 数据结构版本（W4） | `internal/storage/schema.go`；`internal/service/backend.go`（启动先核对）；`main.go`（系统对话框） | `TestSchemaGuardRefusesNewerJSONWithoutWriting`、`TestSchemaGuardRefusesNewerSQLite`、`TestSchemaGuardBacksUpBeforeMigrationKeepingLatest`、`TestSchemaGuardCurrentDataUntouched`、`TestBackendRefusesDataOfNewerTrace`、`TestBackendStampsAndRefusesNewerDatabase` | passed。现在所有结构都是 1，迁移前备份的路径只由测试走到 |
| **M2-K08** `tools/release`（W5） | `tools/release/main.go`、`changelog.go`；`tools/internal/build/files.go`、`bundle.go` | `TestSigningKeyRequired`、`TestReleaseNeedsCleanTreeOnOriginMain`、`TestPublishNeedsAuthorization`（A 补：`-publish` 与 `-offline` 同用时在构建前失败）、`TestSigningKeyKeptFromChildProcesses`（A 补）、`TestReleaseFiles`（产物齐全、`SHA256SUMS` 正确、清单的版本证明可由 `update.Source` 验证、不带 `trace.exe` 与点文件、演练失败时 `dist/trace` 不变）、`TestChangelog`；实跑 `go run ./tools/release -allow-dirty -offline`：退出码 0、78 秒；`release.json` 为 `rehearsal: passed`、`published: false`，7 个文件（没有 `trace.exe` 与点文件），`dist/trace/SHA256SUMS` 核对通过 | passed。真实发布 not-run |
| **M2-K09** ptest 安装演练（W5、W7） | `tools/release/rehearsal_windows.go`（本机回环地址上的假发布源、`TRACE_PTEST_UPDATE_URL`、静默安装、升级、篡改、卸载） | `TRACE_RUN_INSTALLER_TEST=1 go test ./tools/release -run TestInstallerRehearsal`：`--- PASS: TestInstallerRehearsal (37.84s)`；旧版为更新退出用了 318 ms；`tools/release` 实跑里的同一演练（`artifacts/release/rehearsal.json`）；`TestSplitUninstallCommand` | passed：安装 `.1` → 经假发布源下载、校验、重启后为 `.2`（卸载项版本随之更新）→ `.3` 篡改被拒 → 卸载后安装目录删除、`%LOCALAPPDATA%\trace-ptest` 保留；前后核对正式版与开发版的卸载项不变 |
| **M2-K10** 安装包（W6） | `mygo.json`（图标、发布者、版权、`i18n`）；`assets/icon.png`、`icon-dev.png`（`tools/icons` 绘制）；`internal/locales/zh-Hans.json`；`tools/internal/build`（`ChannelConfig`） | `TestIconsAndChineseCatalog`（两个通道图标不同、中文目录含安装界面字符串）、`TestChannelConfigs`、`TestWithConfigRestoresOnFailure`；ptest 演练读取的卸载项 `DisplayVersion` | passed（文件与配置层面）。安装界面的实机外观、开始菜单与“设置 › 应用”里的图标 not-run（H-002 第 1、3 步）；MyGo 自带的“应用正在运行”提示仍是英文 |
| **M2-K11** `release.yml`（W8） | `.github/workflows/release.yml` | `TestReleaseWorkflow`：Action 固定完整 SHA；`permissions: {}`，只有 `publish` 有写权限；签名私钥只在构建步骤的环境里；没有仓库变量 `TRACE_RELEASE_PUBLISH=1` 不发布；`-plan` 在没有新提交时跳过 | passed（静态检查）。A 于 2026-11-09 用 `git ls-remote` 对照 GitHub 核实三个固定 SHA，都与标签一致且是各自的最新版本（第 8 节）。CI 上的运行 not-run |
| **M2-K12** 回归 | 全部 | `go run ./tools/check`、`GOOS=linux CGO_ENABLED=0 go vet ./...`、`go run ./tools/uishots -out artifacts/uishots`、`go run ./tools/package`、`go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke`：退出码都是 0（check 27 秒，ALL CHECKS PASSED；vet 1 秒；uishots 75 张截图 all passed；package 11 秒；smoke PASSED，`versionMatchesVersionTxt: true`、trayPrivateWS 0.56 MiB、webview2 0） | passed |
| **D1** 依赖 | `go.mod`、`go.sum` | MyGo `v0.8.1 → v0.8.3`（2026-11-09 用 `go list -m -versions` 再查，仍是最新版本）；`golang.org/x/image v0.46.0 → v0.47.0`；`modernc.org/sqlite v1.61.1 → v1.62.0`（随之 `modernc.org/libc v1.79.0 → v1.80.1`）；`go list -m -u all` 列出的其他间接依赖保持最小版本选择的结果，不单独升级；升级后完整回归通过 | passed |
| **W9** 文档与用户待办 | 见第 6 节的文档清单；`USER-ACTIONS.md` | `go run ./tools/check` 的文档检查 | passed |

## 4. 偏差与理由（都可逆）

- **`tools/release` 用 `-offline` 演练**：计划写 `go run ./tools/release -allow-dirty`；不加 `-offline` 会用 `gh` 读 GitHub 上的 Release 列表来核对标签，而这次交接不允许访问 GitHub 发布。`-offline` 只跳过这一核对（版本按本机标签算），其余检查、签名构建、演练全做。
- **图标放在 `assets/`，不在 `resources/`**：MyGo 会把 `resources/` 原样放到程序旁边；图标只需编进程序与安装包。
- **去掉 `tools/package -refresh-version`**：`VERSION.txt` 总是和程序出自同一次构建，不再单独刷新。`-channel` 与 `-install` 也去掉：正式版只由 `tools/release` 构建，`-install` 由自动安装取代（`-install-only` 用现有的包）。
- **正式版发布物不带 `trace.exe`**：用户装安装包，更新走更新包；单独的程序文件没有用处，还会被当成另一种分发方式。
- **演练前把以前的 ptest 数据移到 `trace-ptest.previous`**：保证每次演练从空数据开始，又不删除用户可能想看的上一次结果。
- **ptest 的首次检查是启动 3 秒后**（正式版 1 分钟）：只在设了 `TRACE_PTEST_UPDATE_URL` 的 ptest 身份上生效，正式版与开发版不读这个变量。
- **更新后由 TRACE 自己改卸载项的版本**：MyGo 的更新只换文件；TRACE 在更新后第一次启动时，只在卸载项的 `InstallLocation` 就是程序所在目录时改 `DisplayVersion`。
- **代码注释里的旧编号没有清理**：C0 只要求文档与测试名；注释留作 [LOOP-007](status.md#loop)，改到相应文件时顺手改。研究资料（有日期的记录）、决定表与已完成事项表里的历史版本号保持原样。

## 5. not-run 与限制

| 项目 | 原因 | 去向 |
| --- | --- | --- |
| 真实 GitHub 发布、正式版联网检查与下载 | 这次交接不允许访问 GitHub 发布、不发布 | ACT-008、ACT-009 授权后的第一次发布；H-002 第 3～5 步 |
| CI 上的 `release.yml` 运行 | 不 push、不改仓库设置 | 合并后由 ACT-008（secret 与 `release` 环境）、ACT-009（发布授权）开始 |
| 开发版自动更新时有缓存任务或录制 | A 实跑时 TRACE Dev 没有进行中的任务 | H-002 第 2 步 |
| 安装界面、图标、SmartScreen 的实机体验 | 需要用户在自己的桌面上看 | H-002 |
| 实机体验 | — | H-001 |
| `go test -race` | 本机没有 cgo 工具链（与 M1 相同，[工程规则](../dev-rules/engineering.md)） | 有 cgo 工具链的机器 |
| 差分更新 | 离线演练无法取到已发布的清单，`deltas: 0`；第一次发布时也没有上一版 | LOOP-006 |

## 6. 整理记录（C0）

- **文档里的旧标签**：`requirements.md`、`roadmap.md`、`status.md`、`architecture.md`、`data-and-cache.md`、`ui.md`、`engineering.md`、`USER-ACTIONS.md` 里的 R1～R13、F-0xx、P1～P16、C01～C40、PR-1～PR-4、“第 N 轮反馈”改为直接描述能力；决定表、有日期的研究记录与计划里描述历史的引文保留。
- **测试名**：`internal/ui/feedback3_test.go → detail_pages_test.go`、`feedback4_test.go → context_panels_test.go`、`feedback5_test.go → home_and_media_test.go`；`TestRevision3FeedbackReworkR1ToR7 → TestSubscriptionGridParseStatusLogsAndTaskRemoval`、`TestRevision4FeedbackReworkR8ToR13 → TestHomePostsUpdatedRowParseHistoryAndLibraryMenus`；注释与失败消息里的编号去掉，断言不变。
- **只为 M1 原型保留的代码**：`download_test.go`、`player_test.go`、`shell_test.go` 里写原型回执（`artifacts/prototypes/*.json`、`RESULTS.md`）的代码与两个别名测试删除（被别名的测试保留）；`player_test.go` 的 `ptsAfterSeek` 因此补上 `< 0` 的检查；`pr4HelperResult → trayHelperResult`。
- **文件**：删除未入库的 `artifacts/prototypes/` 与仓库根目录下未入库的 `trace.exe`（旧的手动构建）；`tools/package` 的登记替换代码与许可文本移到 `tools/internal/build/`，供 `tools/release` 共用。`dist/trace-0.1.0-windows-x64-*` 旧包目录按计划不在本阶段处理。
- **生成物**：`dist/trace-dev/`、`dist/trace/`、`artifacts/` 只留这次的最新一份（DEC-025）。

## 7. 给 A 的审查 Prompt

```text
你是 TRACE 仓库的 Agent A，集中审查 M2（安装与更新）。
- 计划：docs/delivery/stage-plan.md（TRACE-M2-01 修订 1）；报告：docs/delivery/stage-report.md。
- 分支 codex/m2-install-update，基点 main @ 29c2678，交付 SHA 见 B 的交付消息（单个 DCO 提交，含你的计划）。
- 先按报告第 3 节逐项核对 M2-K01～K12 的实现与证据，重点看：
  1. internal/update/source.go 的校验是否覆盖计划 4.4 的每一条（HTTPS 与重定向、清单大小与字段、traceAttestation、只开清单里的文件、边读边核对），以及与 mygo.Updater 的接法（internal/shell/updates.go）；
  2. tools/package/install.go 在 TRACE Dev 不退出时绝不强杀、只操作 trace-dev 身份；
  3. tools/release 的顺序：缺私钥、工作区不干净、未授权发布都在构建前失败；演练失败时 dist/trace 不变；发布时清单最后上传；
  4. .github/workflows/release.yml 的权限、secret 范围与发布条件；固定的 Action SHA 需要对照 GitHub 核实（B 没有访问 GitHub）；
  5. 报告第 4 节的偏差（尤其 -offline 演练、去掉 -refresh-version/-channel/-install、发布物不带 trace.exe）是否接受。
- 复验命令：go run ./tools/check；GOOS=linux CGO_ENABLED=0 go vet ./...；go run ./tools/uishots -out artifacts/uishots；go run ./tools/package；go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke；TRACE_RUN_INSTALLER_TEST=1 go test ./tools/release -run TestInstallerRehearsal -count=1 -timeout 20m；go run ./tools/release -allow-dirty -offline。
- 边界：安装演练只用 ptest 身份，不安装、关闭或改动正式版 trace；更新签名私钥在仓库外，不重新生成、不打印；不请求哔哩哔哩、不访问 GitHub 发布；不 push、不建 PR、不打标签、不发布、不改仓库设置与 secrets。
- 局部问题直接修复并 amend 进同一个 DCO 提交（git commit --amend -s），较大缺口按交接模板第 3 节一次性交回 B；审查结论写进本报告，给出新的交付 SHA 与一个首选下一步。
```

<a id="review"></a>
## 8. A 的审查结论（2026-11-09）

**结论**：接受交付。M2-K01～K12 的实现与证据逐项核对通过；两处局部问题由 A 直接修复并 amend 进同一个 DCO 提交，没有需要交回 B 的缺口。阶段可以请用户授权 push 与 PR；合并不代表产品验收（H-001、H-002 仍为 not-run）。

**复验**（交付提交 `6fe74e0`，都在仓库根目录运行，退出码 0）：`go run ./tools/check`（ALL CHECKS PASSED，26 秒）；`GOOS=linux CGO_ENABLED=0 go vet ./...`；`go run ./tools/uishots -out artifacts/uishots`（75 张，all passed）；`go run ./tools/package`（13 秒，含本机 TRACE Dev 的真实自动更新，见 M2-K03）；`go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke`（PASSED）；`TRACE_RUN_INSTALLER_TEST=1 go test ./tools/release -run TestInstallerRehearsal -count=1 -timeout 20m`（PASS，39 秒）；`go run ./tools/release -allow-dirty -offline`（76 秒，`rehearsal: passed`、`published: false`，7 个文件）。演练前后本机的卸载项只有 `io.github.chialecode.trace-dev`，正式版没有安装、没有被改动。

**重点核对**

1. **`internal/update/source.go` 与接法**：计划 4.4 的每一条都有实现与测试。请求与每次重定向都要 HTTPS（只有设了 `TRACE_PTEST_UPDATE_URL` 的 ptest 可以用回环地址上的 HTTP）；清单最大 1 MiB、字段齐全、版本格式、大小不超过 1 GiB、清单里的地址都是 HTTPS、签名格式正确；`traceAttestation` 用内置公钥核对“版本 + 更新包签名”；不比当前新的清单原样交给 MyGo 而文件表清空，`Open` 只开最近一次核对过的清单列出的文件；边读边核对大小与 Ed25519 签名，不符时在读到结尾前报错。MyGo `v0.8.3` 的 `download` 用 `io.Copy` 读到结尾并自己再核对一次大小与签名；`ApplyDelta` 要求差分包已签名的索引里的起止版本等于当前版本与清单版本，而清单版本已由版本证明约束，所以差分包不能被换成别的版本，版本证明不必再覆盖差分包。`internal/shell/updates.go` 只在注入了地址与公钥时 `SetSource`，`Enabled()` 仍由 MyGo 的构建注入决定，开发版不检查；`Install` 只装最近一次 `Check` 的结果。
2. **`tools/package/install.go`**：`processes` 接口没有结束进程的方法；按 `trace-dev` 卸载项的 `InstallLocation` 拼出可执行文件、按完整路径比对进程；不退出时 30 秒后放弃并说明“没有强制结束它”，也不运行安装包（MyGo 安装器自己的 10 秒强杀因此碰不到运行中的 TRACE Dev）；`--quit-for-update` 在没有运行中实例时作为第一个实例直接返回，不会启动 TRACE。真实路径已由 A 实跑通过（M2-K03）。
3. **`tools/release` 的顺序**：`-publish` 与 `-allow-dirty` 同用、未授权发布、读不到 `mygo.json` 都在任何构建前失败；工作区不干净与 `HEAD` 不是 `origin/main` 在算版本前失败；缺私钥或私钥与公钥不配对在构建前失败（`TestSigningKeyRequired` 断言没有调用构建）。演练失败时在 `PublishBundleWith` 之前返回，`dist/trace` 不变（`TestReleaseFiles`）。本机发布与 CI 的 `publish` job 都先建草稿 Release、最后上传清单、核对文件齐全后才改为正式的最新 Release。
4. **`release.yml`**：顶层 `permissions: {}`；`check` 与 `build` 只读，只有 `publish` 有 `contents: write`，且不检出、不运行仓库代码；私钥只在 `build` 的“Build, sign and rehearse”一步（`release` 环境）；`publish` 需要 `TRACE_RELEASE_PUBLISH == '1'` 且（定时运行或手动勾选 `publish`），并核对 `SHA256SUMS` 与 `VERSION.txt` 的版本和通道；`-plan` 没有新提交时输出 `release=false`，后面的 job 都跳过。固定的 Action SHA 用 `git ls-remote` 对照 GitHub 核实（只读，没有访问 TRACE 的发布）：`actions/checkout` `3d3c42e5aac5ba805825da76410c181273ba90b1` = `v7.0.1`、`actions/upload-artifact` `cf430e030ddbb5b0abf93d22962f4752f3646cd9` = `v7.0.2`、`actions/download-artifact` `9000827ccba6bdab643e8b6fd33ac0654aef8333` = `v8.0.2`，都是各自的最新标签。加入 Actions 允许列表仍属于 ACT-008。
5. **第 4 节的偏差**：全部接受。`-offline` 演练是这次交接不能访问 GitHub 的直接结果，其余检查、签名构建与演练照做（A 另外禁止它与 `-publish` 同用，见下）；去掉 `-refresh-version`、`-channel`、`-install` 后，正式版只由 `tools/release` 构建、开发版由自动安装覆盖，文档里没有留下引用；发布物不带单独的 `trace.exe` 合理（安装包与更新包都带程序）；图标放 `assets/`、ptest 首次检查 3 秒、`trace-ptest.previous`、更新后由 TRACE 改卸载项版本、注释旧编号留给 LOOP-007，都可逆，也不降低验收。

**A 的修复（已 amend）**

| 问题 | 影响 | 修复 | 验证 |
| --- | --- | --- | --- |
| 私钥留在 `tools/release` 进程的环境里：CI 上 `MYGO_UPDATER_PRIVATE_KEY` 是这一步的环境变量，`tools/release` 启动的 `go run ./tools/check`（及其运行的全部测试）、`git`、`gh` 都会继承它，与工程规则“只交给构建子进程”不符 | 私钥暴露给与签名无关的子进程 | `main` 用 `takeSigningKeyEnv` 读出后立即从进程环境里删除，只经 `options.getenv` 读取，再由 `buildChannel` 交给 MyGo 构建；[工程规则 §2.3](../dev-rules/engineering.md#update-key) 写明 | `TestSigningKeyKeptFromChildProcesses` |
| `-publish` 可以与 `-offline` 同用：版本只按本机标签算，跳过与 GitHub 标签、Release 的核对，也不做差分包 | 可能发布错误的版本号 | `run` 在构建前拒绝；工程规则的发布步骤第 1 条写明 | `TestPublishNeedsAuthorization` 新增一例 |

**修复后的复验**：`go test ./tools/release ./internal/update ./tools/package -count=1`、`go run ./tools/check`、`go run ./tools/release -allow-dirty -offline` 都通过（check 24 秒 ALL CHECKS PASSED；release 在修复后的工作区上构建、签名并演练，`rehearsal: passed`、`published: false`）。程序代码没有改动，`tools/package`、`tools/smoke`、`tools/uishots` 与安装演练沿用上面对 `6fe74e0` 的结果。

**仍需用户处理**：ACT-008（备份密钥、建 `release` 环境与 secret、允许两个官方 Action）；ACT-009（每周自动发布）；H-002（带缓存任务的开发版自动更新、正式版安装与联网更新、SmartScreen）；H-001。
