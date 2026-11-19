# 阶段报告：M5（远程版、上传与导入导出）

计划：[TRACE-M5-01](stage-plan.md)（修订 1）。上一阶段的报告在合并提交里：[M4 报告](https://github.com/chialecode/trace/blob/8327803/docs/delivery/stage-report.md)。

本报告由 B 按[交接模板 §2](../templates/agent-handoff.md)填写；A 的集中审查追加在后面。

## 1. B 交付（2026-11-20）

| 信息 | 内容 |
| --- | --- |
| 执行范围 | C0、D1、W1～W9 与必测用例 M5-K01～K24 全部做完，没有缩减验收。用户没有新增指示。非阻塞的问题按 [Q-022](../../USER-ACTIONS.md#q-022) 的默认值实现；B 另定的 4 项写在 Q-022 第 11～14 条和本报告第 4 节 |
| 版本与工作区 | 分支 `codex/m5-remote`，基点 `main @ 8327803`。全部改动 amend 进阶段提交（计划提交 `ca72b48` 之上，DCO），交付 SHA 在交付消息里。工作区里没有不属于本阶段的未提交文件。`artifacts/backup-20261116` 与 `artifacts/repair-20261115` 是用户真实数据的备份，被忽略也没有动，写进了 USER-ACTIONS 的“本机文件”。没有 push，没有 PR |
| 可运行产物 | **桌面开发版**：`go run ./tools/package -install` 装成 `0.1.3-dev.202611191746`（从 `0.1.3-dev.202611190516` 升级），已在托盘运行（`artifacts/package/install-dev.json`：relaunched、background 都是 true）。`trace-dev-setup-0.1.3-dev.202611191746-amd64.exe` SHA-256 `c2cc14d10158c99348abd590f185f9622e304b409c307a094d31d92311c7880e`（8.0 MiB），`trace.exe` SHA-256 `1f61e58542dcd87efa7c4ed7929aa0540dce3b1c0c24d36d2dc50615ddee7a41`（25.0 MiB），VERSION 的 commit 是 `ca72b48…-dirty`（包在 amend 之前构建，源码与交付提交相同）。**远程版**：`go run ./tools/package -server` 产出 `dist/trace-server/`：`trace-server-0.1.3-dev.202611191749-linux-amd64.tar.gz` SHA-256 `51a7b1d5b0c473ccb29f633f68fdf5c04ab8b3e461ca648814af73bb8c812123`（包 6.9 MiB，程序 16.1 MiB）；`trace-server-0.1.3-dev.202611191749-linux-arm64.tar.gz` SHA-256 `f0d86337db131ba75e0a9ad583acfe4ceb2695110dffa53957b6a34d48010df4`（包 6.4 MiB，程序 15.0 MiB）；另有 `SHA256SUMS`。截图在 `artifacts/uishots/`（64 个场景，192 张），冒烟回执在 `artifacts/smoke/`、`artifacts/smoke-server/`、`artifacts/smoke-server-linux/`。样本全部是合成的：假站点客户端、假 rclone（`internal/upload/fakerclone`，冒烟程序在 `TRACE_FAKE_RCLONE=1` 时充当 rclone）、回环地址上的真实 HTTPS |
| 关键变更与恢复 | 新程序 `cmd/trace-server`（`init`/`run`/`token`/`version`/`upload-test`/`rclone-filter`）。新包：`internal/remote`（协议、连接码、指纹固定的客户端）、`internal/remoteserver`（配置、HTTPS 接口、令牌哈希、失败限流、请求体上限、版本握手、信号收尾）、`internal/upload`（rclone 上传）、`internal/transfer`（扫描、导入、导出）。服务层新增远程运行配置（`remote_profile.go`）、本地远程客户端（`remote_client.go`）、远程接口实现（`remote_server_api.go`）、订阅同步与文件导出导入（`remote_sync.go`）、导入导出任务（`transfer_jobs.go`）。**数据**：结构仍是版本 2，只加了可选字段（`item.json` 的 `cachedBy`、`settings.json` 的 `remote`）、远程数据目录里的新表 `upload_files`（`IF NOT EXISTS`），以及两个新文件 `node.json` 和 Linux 的 `credentials/secret.key`。**临时名**：所有非正式文件都匹配 `*.part` 或 `.trace-*`，DASH 中间文件改名为 `.trace-Pxx.video-<清晰度代码>-<编码 ID>.m4s`，旧名字的进行中下载升级后会被接管续传。**权限**：远程不续期，不做全部缓存，也不能手动缓存（返回 `ErrRemoteUnsupported`）；令牌只存哈希；推送的 Cookie 先经站点校验才保存。**界面**：“设置 › 远程”；媒体库“⋯ → 导入… / 导出…”与导入页；任务页“导入导出”分段；订阅“⋯ → 导出订阅… / 导入订阅…”。**恢复**：导入逐个文件校验大小与 SHA-256，用临时名复制后再改名，`item.json` 最后写；取消或中断后重新扫描导入即可，相同的跳过、缺的补上。上传成功确认后才删除文件，`item.json` 最后上传、最后删除；重启后按 `upload_files` 补传 |

| 工作包 / 用例 | 实现位置 | 实际命令与证据 | 状态与限制 |
| --- | --- | --- | --- |
| C0 整理 | 见第 2 节 | `go run ./tools/checkdocs` OK（包含在 `tools/check` 里） | passed |
| D1 依赖 | `go.mod`/`go.sum` | 见第 3 节 | passed；PR #9 要在合并后关闭（未授权操作 GitHub） |
| W1 临时名、节点与清单（K01、K02） | `internal/storage/atomic.go`（`TempPrefix`、`TempSuffix`、`IsTempName`、`TempSiblingPath`）；`internal/download/video.go`、`douyin.go`、`manager.go`（DASH 中间名、旧名接管）；`internal/live` 的段文件与收尾；`internal/library/node.go`（`node.json`、`LoadOrCreateNode`）、`library.go`（`cachedBy`）、`files.go` | K01：`watchNames` 在 `TestMuxDASHFilesAndVideoCacheEndToEnd`、`TestDouyinVideoCacheRangeRefreshAndSecretScrub`、`TestDouyinPlainAndMixedNotesMusicAndUnknownPage`、`TestCachingContinuesWhenFinishedFilesAreMovedAway`、`TestRecordingContinuesWhenFinishedSegmentsAreMovedAway` 的整个过程中持续检查文件名；`TestLegacyStreamPartResumesUnderTemporaryName`；凭证与 JSON 写入沿用 `TestWriteMigratedJSONAtomicPreRenameFaultCleansTemp`（断言改为新临时名）。K02：`TestCachedByNodeIsStableAndOldItemsStillLoad` | passed |
| W2 远程运行配置与凭证（K03～K06） | `internal/service/remote_profile.go`；`internal/credential/secret.go`、`dpapi_other.go`（Linux 用随机密钥文件做 AES-GCM），`dpapi_windows.go` 不变；`internal/subscription/snapshot.go` | K03：`TestRemoteProfileHasNoRenewalNoFullCacheAndChecksOn`（续期与站点请求计数为 0；全部缓存不建队列；入队返回明确错误；定时检查关不掉）。K04：`TestCachingContinuesWhenFinishedFilesAreMovedAway`、`TestRecordingContinuesWhenFinishedSegmentsAreMovedAway`。K05：`TestSnapshotReplaceKeepsStartPointsAndStopsRemovedAccounts`。K06：`TestLinuxCredentialKeyFile`（WSL 下运行） | passed。**K05 只覆盖一部分**：同步来的账号先建起点、起点前的内容不进更新，由该测试在远程的快照路径上验证；“新内容按规则自动缓存、开播按规则录制”沿用 M3 的 `TestScheduledCheckBackoffQuotaAndAutoCache` 等测试，远程走同一个订阅管理器与后端，没有为远程另写一遍 |
| W3 `trace-server` 与接口（K07～K11） | `cmd/trace-server/main.go`；`internal/remoteserver/config.go`、`server.go`、`run.go`；`internal/remote/client.go`、`code.go`、`protocol.go`；`internal/service/remote_server_api.go` | K07：`TestInitPrivateFilesCodeAndTokenRotation`。K08：`TestAPIAuthFailureLimitBodyLimitAndProtocol`。K09：`TestClientPinsCertificateBeforeAnyRequest`。K10：`TestCredentialPushVerifiesBeforeSaving`、`TestRemoteProfileHasNoRenewalNoFullCacheAndChecksOn`（没有 `refresh_token`）。K11：`TestRunEndToEndWithoutLeakingSecrets`；进程级的 `go run ./tools/smoke -server` | passed。**K11 的录制收尾**：端到端测试用取消停止服务，确认任务暂停与没有泄露；“退出时录制收尾为可播放文件”沿用 `TestStopRecordingWhileLiveFinalizesPlayableFile`（远程与桌面走同一个 `backend.Close`）。SIGTERM 退出码 0 在 WSL 冒烟里验证；Windows 冒烟用 kill 再重启，验证锁会释放（第 5 节） |
| W4 上传（K12～K14） | `internal/upload/upload.go`（`upload_files` 表、按条目排队、先媒体后清单、确认后删除、错误分类与退避、重启补传、`sanitize`）；`internal/upload/fakerclone` | K12：`TestUploadOrderDeleteAndNoPartFiles`、`TestUploadRecordingSegmentsThenFinal`、`TestUploadWaitsForBusyDownload`。K13：`TestUploadFailuresAndKeepFiles`、`TestSanitizeKeepsFirstErrorLineWithoutSecrets`。K14：`TestUploadResumesAfterRestart` | passed（Windows 与 WSL） |
| W5 本地客户端与“设置 › 远程”（K15～K17） | `internal/service/remote_client.go`；`internal/ui/remote_settings.go`；`internal/ui/theme.go` | K15：`TestDesktopConnectAndErrorKinds`（无法连接、证书变了、令牌错、版本不同、远程拒绝 Cookie 各自的文案；页面不可见时没有远程请求）。K16：`TestDesktopPushesCookieAfterLoginAndRetriesPending`。K17：`TestDesktopSyncStateFollowsSnapshotHash`。截图：`settings-remote-*.png`、`settings-remote-connected-*.png`、`settings-remote-unreachable-*.png` | passed |
| W6 资源库导入导出（K18～K21） | `internal/transfer/scan.go`、`apply.go`、`export.go`；`internal/library/merge.go`；`internal/service/transfer_jobs.go`；`internal/ui/transfer_view.go`、`library_view.go`、`views.go`、`tasks_view.go`、`shell.go` | K18：`TestScanSortsSixCategoriesWithoutWriting`。K19：`TestApplyNewSupplementConflictsAndIncomplete`。K20：`TestApplyCancelResumeAndDeleteSource`。K21：`TestExportReimportAndTwoNodeLiveMerge`；服务层 `TestTransferJobsExportEverythingAndImportElsewhere`（导出分页 2 条一页、5 个条目，导出 → 扫描 → 导入，丢弃的计划不能再执行）。截图：`library-import-preview-*.png`、`tasks-transfers-*.png` | passed。**K20 只覆盖一部分**：中断用复制大文件中途取消来模拟，没有真的杀进程。被杀时留下的只会是 `.trace-*` 临时名，重新扫描会忽略它们，下一次复制再覆盖 |
| W7 订阅快照与导出导入（K22） | `internal/subscription/snapshot.go`、`snapshot_import.go`；`internal/service/remote_sync.go`（`ExportSubscriptions`；路径是文件夹时用 `trace-subscriptions.json`）；`internal/ui/transfer_view.go` 的订阅导出导入对话框 | `TestSnapshotReplaceKeepsStartPointsAndStopsRemovedAccounts`、`TestSnapshotReplaceFailureKeepsPrevious`、`TestSnapshotImportPreviewAndApply`。截图：`subscriptions-import-preview-*.png` | passed |
| W8 打包、门禁、冒烟、CI（K23、K24） | `tools/package/server.go`（`-server`）、`main.go`（`devInfo`）；`tools/check/main.go`（规则 9：`trace-server` 不得依赖界面包；`checkServerBuild`：linux amd64/arm64 交叉编译加 `GOOS=linux go vet ./...`；gofmt 覆盖 `cmd`）；`tools/smoke/server.go`；`cmd/trace-server/deploy/`；`.github/workflows/ci.yml` | K23：`go run ./tools/package -server` 产出两个包与 `SHA256SUMS`；`TestWriteTarGzLayoutModesAndLineEndings`；`tools/check` 规则 9 的 3 个合成用例；WSL 里 `sha256sum -c` OK、`file` 显示静态 ELF、`trace-server version` 与桌面版是同一构建号格式（`0.1.3-dev.<时间>`，两个包分开构建所以分钟不同）。K24：见下一行 | passed |
| K24 回归 | — | `go run ./tools/check` ALL CHECKS PASSED（Go 1.27.4）；`go run ./tools/uishots` 64 个场景、192 张图全部通过，三种宽度没有横向滚动，新增 6 个场景；桌面冒烟 `go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke` PASSED（3039 ms，托盘专用工作集 0.61 MiB，WebView2 0）；远程冒烟 Windows `go run ./tools/smoke -server` PASSED（kill 后重启，1196 ms），WSL Ubuntu 用 Linux 包里的程序 PASSED（SIGTERM，77 ms，`artifacts/smoke-server-linux/smoke.json`）；`GOOS=linux go vet ./...` OK。WSL 里还跑了 credential、remoteserver、upload、transfer、subscription、library、storage、download、live、service 的 Linux 测试程序，全部通过 | passed |
| W9 文档 | `docs/design/remote.md`、`data-and-cache.md`、`architecture.md`；`docs/ui.md`；`docs/dev-rules/engineering.md`；`docs/requirements.md`（FR-REMOTE-001～008）；`docs/delivery/roadmap.md`（AC-56～64）、`status.md`；`USER-ACTIONS.md`（Q-022、H-003）；`cmd/trace-server/deploy/README.md` | `go run ./tools/checkdocs` OK | passed |

## 2. 整理记录（C0）

| 对象 | 处理 |
| --- | --- |
| `docs/delivery/stage-report.md`（M4 报告） | 已在合并提交里（固定链接见开头）。仍有效的结论已经在正本里（交付状态、决定、USER-ACTIONS），所以整篇改写为本报告。复查后，仓库里没有指向旧报告或旧计划锚点的相对链接 |
| USER-ACTIONS 里已完成的 ACT | ACT-001～007、009～012、014～021 移到“最近处理完的”，保留锚点，各精炼成一行。ACT-008 仍有效，留在原处 |
| 以前阶段的标签 | `roadmap.md` 的“（M4，”改为能力描述；`status.md` 的 LOOP-010 标签改为能力描述。新测试的注释不带 `M5-Kxx` |
| 根目录 `package.exe` | 是 `go build ./tools/package` 的残留，被误加入了仓库。已 `git rm --cached` 并删除，`.gitignore` 加了 `/*.exe` |
| `dist/`、`artifacts/`、`tmp/` | `dist/` 按类各留一份：`trace/`（发行版，不动）、`trace-dev/`、`trace-server/`。`artifacts/` 里的 `smoke-server`、`smoke-server-linux` 是新的冒烟回执，各一份。两个用户数据备份没有动（见第 1 节）。`tmp/` 是本次实施用的临时脚本与 WSL 测试程序，被忽略，交付后可以整个删除 |
| 测试名 | 新测试都按能力命名（第 1 节的表），没有阶段号或问题编号 |

## 3. 依赖盘点（D1）

- PR #9（`golang.org/x/image` 0.47.0 → 0.48.0）的改动已经并入本阶段提交。关闭 PR 要操作 GitHub，没有授权，所以 PR #9 还开着；合并后 Dependabot 会自动关闭它，不会的话由用户关闭。
- `go run ./tools/deps`：开始时把满 7 天的都升了：`mp4ff` v0.62.0 → v0.62.1、`mygo` v0.8.3 → v0.9.0、`x/image` → v0.48.0、`x/text` v0.43.0 → v0.44.0（间接依赖）。交付前到期 0 个。还在等 7 天的有 2 个：`mp4ff` v0.63.0（第 6 天）、`mygo` v0.10.0（第 1 天）。
- `mygo` v0.9.0 在 `go.mod` 里带进 6 个间接模块（`go-crypto`、`circl`、`compress`、`xz`、`rpm`、`x/crypto`）。它们只被 `mygo/cmd/mygo` 使用，不会链接进 TRACE 或 `trace-server`（`go list -deps` 已核对）。
- `go run ./tools/deps -audit` passed：called 0，没有例外，另有 7 条在代码没调用到的模块里。
- 升级后重新跑了 `tools/check` 与 `uishots`，都通过。

## 4. 实现中的决定（都可逆，没有写成 DEC）

1. **导入选文件夹、导出选目标**：用路径输入框，没有用系统文件对话框（Q-022 第 11 条）。MyGo 没有原生选择器，保留以后换成原生对话框的空间。
2. **任务页的分段叫“导入导出（N）”**（Q-022 第 12 条）。导入与导出是同一类后台任务，合在一个分段里。
3. **没选处理方式的冲突在导入时跳过**；“不同作者文件夹”的冲突保留本地的文件夹（Q-022 第 13 条）。导入页可以一次给全部冲突定处理方式。
4. **导出范围是媒体库当前的整个筛选结果**，不只是已经加载的那一页（Q-022 第 14 条）。实现时发现 `ListItems` 默认最多返回 200 条，导出会截断，已改为按 500 条一页分页导出（`exportPageSize`），有测试覆盖。
5. **导入的文件校验不过时**，记进条目的“缺失”，条目标为部分完成，不当作完成。
6. **两个节点各录了一部分的同一场直播**，导入时按开始时间合并为一场，段号重新编为连续的，`cachedBy` 保留。
7. **DASH 中间文件改名为 `.trace-` 开头**；旧名字的进行中下载会被接管续传（K01）。
8. 远程不做全部缓存。`startDueFullCacheSweeps` 没有专门判断远程：远程配置下全部缓存的规则不建队列，因此它什么也不会启动，测试断言了这一点。

## 5. not-run 与限制

- **H-003（真实 VPS、真实站点、Google Drive）not-run**：本阶段没有发真实站点请求，也没有连接网盘。步骤已按实际命令、包名和界面文案写进 [H-003](../../USER-ACTIONS.md#h-003)，部署说明在 `cmd/trace-server/deploy/README.md`（也随包分发）。
- **`go test -race` not-run**：本机没有 cgo 工具链，与以前的阶段相同。
- **Windows 冒烟不发 SIGTERM**：Windows 不能给子进程发 SIGTERM，所以远程冒烟在 Windows 上 kill 后重启，验证锁释放和数据可以重新打开。正常退出（SIGTERM、退出码 0、录制收尾）在 WSL 用 Linux 包里的程序验证过。
- **导入导出任务只在内存里**：退出 TRACE 后任务列表清空；重新扫描同一个文件夹会继续导入（相同的跳过、缺的补上），不会重复复制。
- **旧版开发版退出慢**：已查明是抖音录制的死锁，见[第 7 节](#feedback-1)第 1 条，已修复。
- **体积**：`trace-server` 程序 16.1 MiB（amd64），桌面 `trace.exe` 25.0 MiB。远程版不链接 MyGo、界面、播放器、托盘与语言包，由 `tools/check` 的规则 9 守护。

## 6. 给 A 的审查 prompt

```text
你是 TRACE 的 Agent A，动作：集中审查 M5（远程版、上传与导入导出）B 的交付。
- 计划：docs/delivery/stage-plan.md（TRACE-M5-01 修订 1）；设计正本 docs/design/remote.md；决定 DEC-062；需求 FR-REMOTE-001～008；验收 AC-56～64；默认值 USER-ACTIONS 的 Q-022（第 11～14 条是 B 另定的）；人工检查 H-003。
- 分支 codex/m5-remote，基点 main @ 8327803；交付 SHA 见 B 的交付消息（计划提交 ca72b48 之上的单个 DCO 提交）。报告 docs/delivery/stage-report.md 第 1～5 节，以及第 7 节（2026-11-20 实机反馈的 5 处修复：抖音开录死锁、抖音共创作品存放位置 DEC-063、图文配乐、主页订阅名、全部缓存补不完整的条目）。
- 重点：凭证与令牌不进日志、状态和文件（remoteserver、upload.sanitize、remote_client）；上传先媒体后 item.json、确认后才删除；导入的临时名、校验与中断恢复（transfer/apply.go）；两节点直播合并；“设置 › 远程”、导入页、任务页“导入导出”、订阅导出导入的交互与 docs/ui.md 是否一致；trace-server 的依赖边界（tools/check 规则 9）；报告第 1 节里标了“只覆盖一部分”的 K05、K11、K20；第 7 节末尾的补充审查点。
- 必须保留：M1～M4 与 DEC-059～061 的行为与测试；数据结构版本 2；INV-01～14；按站点的额度、退避与熔断；安静测试、不碰本机安装的应用；只删登记文件；dist 只留最新一份。
- 验证：go run ./tools/check；需要时跑 go run ./tools/uishots、go run ./tools/smoke -server、go run ./tools/package -server；有 WSL 时可用 Linux 包跑远程冒烟。不发真实站点请求，不连接网盘；H-003 保持 not-run。
- 直接修复局部问题并 amend 进同一个提交（git commit --amend -s），把审查结论追加到 stage-report.md；较大的缺口按交接模板 §3 列差异清单交回 B。
- 未授权：push、PR、合并、标签、发布。
```

<a id="feedback-1"></a>
## 7. 实机反馈（2026-11-20，B 修复）

用户在本机 TRACE Dev 上发现三件事，又提了两项要求。B 查明原因后修复，全部 amend 进阶段提交。

| 编号 | 现象与原因 | 修复 | 证据 | 状态 |
| --- | --- | --- | --- | --- |
| 1 | **订阅检查停止、退出要等 3 分钟**（交付时以为只是退出慢）。旧版退出时留下的 `logs/crash-20261119T175010Z.log` 显示：`StartDouyinRecording` 持有 `startMu`，在缓存里没有新鲜状态时调用 `CheckAccountLiveStatus`。账号开了自动录制又正在直播时，这个调用经 `autoStartDouyin` 再次进入 `StartDouyinRecording`，又去拿 `startMu`，把自己锁死。之后同一账号的开录、订阅检查结束后的直播检查（订阅定时检查因此从 2026-11-19 14:32Z 后再没完成）和退出时的 `live.Manager.Stop` 都卡在这把锁上。这段代码从 M4 起就存在，本阶段没改它 | `internal/live/douyin_live.go`：`CheckAccountLiveStatus` 改为调用 `checkAccountLiveStatus(…, autoStart)`；`StartDouyinRecording` 内部用 `autoStart=false` 只读状态 | `TestDouyinStartWithoutCachedStatusDoesNotLockItself`：在修复前的代码上 5 秒内 panic（“locked startMu twice”），修复后 0.04 s 通过；`go test ./internal/live` 全部通过 | passed |
| 2 | **抖音共创作品存到了发布者下**：猫九酱的“全部缓存”（`cq_ec49fbdb50073bbb`，用户手动开的）翻到了 `7229580276903447820`、`7229197081628396832`，它们的作者是“悠长假期”。作品列表接口会把共创作品列在共创者名下（推断，没有发请求核实）。抖音缓存请求里没有“为哪个账号缓存”，文件夹总是按作者算 | [DEC-063](../decisions.md#dec-063) ①：`DouyinWorkEnqueueRequest` 加 `ForUID`/`ForName`；新条目的发布者不是这个账号时，存到这个账号的 `works/` 下，并记 `via` collab；`download_tasks` 加可选列 `via_json`，任务重启后仍能写对；订阅自动缓存、缓存队列（含恢复）、用户页作品的“缓存”（`CacheDouyinWorkFor`）都带上这个账号。媒体库筛选、作者列表、已缓存数原本就按 `via` 计算，不分站点 | `TestDouyinCoCreationIsStoredUnderTheAccountItWasCachedFor`（成员文件夹、`via`、作者不变、按成员筛选能找到、持久化的任务带 `via`；作者自己的作品没有 `via`；已缓存的条目不搬动） | passed；已经缓存的两条不会自动搬动（H-001 第 37 步写了怎么重新放） |
| 3 | **抖音图文“部分完成”**：本机共 19 条，全是配乐，没有风控。16 条“配乐无可用地址”：`music.play_url` 为空，而图文顶层 `video.play_addr` 里的配乐没有被当作备用地址。3 条“配乐下载失败 (blocked)”：首个地址在 `lf9-music-east.douyinstatic.com`（在允许列表里），失败记录里没有 HTTP 状态；本机 DNS 是代理的 fake-ip（198.18.x.x），不算内网地址，所以推断是跳转离开了允许列表（点播的跳转检查比直播严格） | `internal/site/douyin/work.go` 的 `noteMusicWithFallback`；`internal/download/douyin.go`：下载配乐时 `PublicRedirects=true`，媒体客户端的跳转检查改用 `ValidateRedirectURL`（不允许公网跳转的传输行为不变）；缺失原因由 `musicFailureReason` 写出被拦截的原因或域名 | `TestNoteMusicFallsBackToTopLevelPlayAddr`、`TestMediaRedirectsFollowPublicHostsOnlyWhenAllowed`（HTTP、IP、`.local`、非 443 仍拦截）、`TestMusicFailureReasonNamesTheCause` | passed（合成数据）；真实站点上能不能补上 not-run，用户在任务页重试时核对（H-001 第 37 步） |
| 4 | **主页显示订阅名**（用户新增要求） | DEC-063 ②：`internal/ui/components.go` 的 `homePostSubscription`；账号名后面用 13px 强调色显示订阅名，订阅名与账号名相同时不显示，点它打开这个订阅；为订阅账号缓存的共创或合作作品按 `via` 找到订阅。`docs/ui.md` §3.1 | `TestSiteFiltersPersistenceAndSixSiteBadges` 加了“主页订阅名：”断言；`uishots` 192 张全部通过，`home-*.png` 里能看到“技术频道_抖音号 技术频道_Alpha” | passed |
| 5 | **全部缓存检查未缓存时不看完整性**（用户新增要求）：队列把状态点为黄色的条目一律当作已缓存跳过，部分完成或有缺失的条目永远不会被补上；另外抖音的“已缓存数”只按作者计数，第 2 条的共创作品没有算进成员名下 | DEC-063 ③：`internal/service/cache_queue.go` 的 `queueShouldCache`、`internal/library/schema.go` 的 `NeedsCompletion`。部分完成、有缺失记录、分 P 没有文件的条目会再缓存一次（已有的有效文件跳过），完整的、正在缓存的、已暂停的和只是可以升级画质的照常跳过。`internal/library/works.go` 的抖音计数加上 `via` collab | `TestCacheQueueCompletesIncompleteItems`（7 种状态中正好 4 条入队）；`TestDouyinCoCreationIsStoredUnderTheAccountItWasCachedFor` 加了已缓存数断言（修复前为 0，失败） | passed |

**验证**：`go run ./tools/check` ALL CHECKS PASSED（`go test ./...` 1m16s）；`go run ./tools/uishots` 192 张全部通过（第 5 条没有改界面）；桌面冒烟 PASSED（3034 ms，托盘专用工作集 0.60 MiB，WebView2 0）；远程冒烟 Windows PASSED（kill 后重启），WSL Ubuntu 用新 Linux 包 PASSED（SIGTERM，78 ms）。

**产物**（替换第 1 节的同类产物）：
- 开发版 `0.1.3-dev.202611201130` 已装到本机，在托盘运行；前后两次升级时旧版都很快退出，没有新的崩溃记录。`trace-dev-setup-0.1.3-dev.202611201130-amd64.exe` SHA-256 `cb2b9c6d8ae57d2051380983d97206f5af094061c8b203233392cea64f9907b4`（8.0 MiB）。
- 远程版：`trace-server-0.1.3-dev.202611201130-linux-amd64.tar.gz` SHA-256 `bb2a352547ddb78a1a9a4426ed33ab3cd52264157bfd933c747ca20525924236`，`trace-server-0.1.3-dev.202611201130-linux-arm64.tar.gz` SHA-256 `e9702bbd79d68d1c3992613075dded08e337a3dab7dd16de6e87f55e90c0158b`。

**给 A 的补充审查点**：第 1 条的锁顺序，看还有没有别的路径持有 `startMu` 时进入开录；第 2 条新增的 `via_json` 列（只加列，带默认值）；第 3 条把配乐的跳转放宽到公网 HTTPS，依据是推断，审查时请判断这个放宽是否可以接受；第 5 条让永远补不上的内容每次全部缓存都再请求一次（7 天一次）。

<a id="full-audit-20261121"></a>
## 8. 全仓库代码审查、重构、依赖更新与打包验证（2026-11-21）

在 M5 交付基点之上，按七个领域加双验证员完成了全仓库源码、测试、工具与 CI 工作流的全面审查与收敛，所有改动并入同一个 DCO 阶段提交。

### 8.1 重点缺陷修复与代码重构

- **安全与边界**：`OSDesktop.OpenExternalURL` 严格限制 `http`/`https` 与非空主机（拒绝 `file://`、UNC、用户信息）；`site.GuardedMediaDialer` 接入哔哩哔哩与抖音媒体传输并修补代理切换豁免与内网/CGNAT/NAT64 网段；抖音 `FetchImageLimited`、头像与历史缩略图补齐站点登录校验（INV-07）；远程连接码与令牌改为密码框并即刻清空；`SiteError.Endpoint`、`RoomState.LastError`、日志脱敏与 `sitecheck` 统一剥离签名参数、`*url.Error`、远程令牌、`csrf`、`qrcode_key` 及编码变体；Windows 目录联接点（`ModeIrregular`）与写入/删除无缓存严格路径校验覆盖 `library` 与 `transfer`；`tools/smoke` 与 `tools/uishots` 清理目录限定于 `artifacts/`；`tools/actcheck` 与 `sitecheck` 对未授权接口类别默认拒绝。
- **数据安全与核心管线**：修复 `subscription.Delete` 未命中非数字 ID 时 `mid = 0` 误删纯抖音订阅、解绑/移动原 B 站账号后 `sub_id` 碰撞覆盖他人订阅、`auth_required` 触发退避与自动缓存吞错误；修复 `storage.OpenSQLite` 仅在确认库损坏时才改名恢复与 WAL 页头 schema 守卫；`library.Store` 新增 `SetAllowMovedFiles(true)` 供 `ProfileRemote` 启用，避免远端搬走文件后被降级为 `missing_files`；`upload` 改为 `--files-from-raw`、检查写入错误、修复忙碌/损坏条目 CPU 空转并上报真实 `QueuedBytes`；修复 `MergeItemMetadata` 多节点直播段合并顺序/内容去重/补充字段、`transfer` 导出目录重叠与未传完冲突删除本地文件；修复 `update.Controller` 并发空指针与定时空转；修复下载实况先改名后校验、`t.Status="paused"` 污染、`context.WithCancel` 泄漏与 64 位 `elst`；修复直播封面写入顺序、远端空 `itemDir` 磁盘检查、Enhanced FLV `PacketTypeMetadata = 4`（同步更新 `docs/design/live.md`）、>3s GOP 误判纯音频、跨轨时间线与 `RefreshForAccounts` 房间过滤；修复 Media Foundation 实时预览关闭时 COM 缓冲 UAF、`pendingReads` 提前删除、取消死锁、`splitDecoder` 采样率/声道数未代理、奇数宽高 NV12 越界及字幕重叠匹配。
- **服务、外壳与界面**：释放锁后再发布同步事件；自动更新 `Relaunch()` 改为在 `mygo.App.OnQuit` 幂等清理后端与释放目录锁；`PauseAllActiveWork` 按站点隔离失效状态（解决 LOOP-016）；托盘刷新串行化并持久化窗口尺寸与首次托盘提示；媒体库无封面视频不再整文件读入 `.mp4` 解码；修复远程设置 30 秒刷新负时长空转；`requestGuardedCleanup` 保证关到托盘或丢弃请求时释放 `MediaDecoder`；头像与历史缩略图异步解码与负缓存、媒体库/检查计划/关注导入虚拟化、全切片 FNV 缓存与 IME/对话框统一。
- **结构拆解与去重**：拆解 `download`、`live`、`library`、`subscription`、`service`、`shell`、`ui`、`tools/uishots`、`tools/actcheck` 等超大文件；提取公共 `site.WrapIdleReadCloser`、`SanitizeEndpoint`、`UnwrapURLError`、`LoopbackTransport`、`visitManifestFiles`、共享 Range 下载与直播会话主循环；全仓补齐 100+ 条回归测试，清零历史阶段/问题编号注释（完成 LOOP-007）。

### 8.2 依赖更新与清理记录

| 类别 | 处理 |
| --- | --- |
| 依赖盘点（DEC-048） | 远端 PR #9（`golang.org/x/image` 0.47.0 → 0.48.0）已并入本阶段提交，合并后关闭；升级满 7 天的 `github.com/Eyevinn/mp4ff` v0.62.1 → v0.63.0；`go run ./tools/deps` 剩余 `due: 0`、`waiting: 1`（`github.com/egoist/mygo` v0.10.0，第 3 天，未满 7 天）；`go run ./tools/deps -audit` 通过（`called: 0`、`open: 0`、`expiredExceptions: 0`） |
| 清理无用代码 | 删除已被 `trace-subscriptions.json` 取代且零调用的 OPML 子系统（`internal/subscription/opml.go`、`service.BackendAPI` 的 `ExportOPML`/`ImportOPML` 及对应单测）；删除 4 个零调用未导出函数（`shell.trayStatusLabel`、`subscription.getSchedulerStateLocked`/`saveSchedulerStateLocked`、`ui.subscriptionLiveOn`、`ui.logoutAccount`） |
| 清理测试与文档 | 按能力重命名两个遗留反馈测试文件（`feedback5_test.go` → `tasks_and_sub_accounts_test.go`、`home_feedback4_test.go` → `home_position_and_subs_test.go`）；更新过时的根目录 `README.md`、`docs/dev-rules/engineering.md`（MyGo v0.9.0 / `mp4ff` v0.63.0 / 任务页六组侧栏）、`docs/design/live.md`（`PacketTypeMetadata = 4`）与 `docs/delivery/status.md`（LOOP-007/016、OSS-003）；删除被忽略的 `tmp/` 临时脚本与旧 WSL 测试产物（不动用户备份 `artifacts/backup-20261116` 与 `artifacts/repair-20261115`） |

### 8.3 2026-11-21 开发版下载、媒体库归属与任务卡片反馈修复

针对用户反馈的本机 TRACE Dev 下载“部分完成”、B 站转发视频归属、抖音共创作品详情页入口及任务卡片右侧裁切问题，结合本机只读排障与常设授权内的有界真实站点验证（`internal/sitecheck` 账本，隔离临时目录验证后即删）查明全部根因并完成修复：

| 编号 | 现象与根因 | 修复 | 证据 |
| --- | --- | --- | --- |
| 1 | **抖音图文配乐 `blocked`**（`lf3-static.bytednsdoc.com`）：空 `music.play_url` 图文回退到顶层 `video.play_addr` 时，部分音频位于字节静态域名 `*.bytednsdoc.com`，该后缀未列入抖音媒体白名单 | `internal/site/types.go` 将 `bytednsdoc.com` 加入 `DefaultDouyinMediaHosts()`（仍强制 HTTPS 443、`GuardedMediaDialer` 与零 Cookie） | `TestDouyinBytednsdocMusicHostAllowed`；真实站点临时验证 1 条该类图文一次完成（`complete`、`music.mp3` 完整落盘、0 Cookie 泄露） |
| 2 | **抖音图文视频页/实况片段 HTTP 403**：含 `need_set_token: true`（或 `is_need_set_cookie: true`）的 `v26-web*.douyinvod.com` 直链带 `ply_type=3` 策略参数、直接拉取固定 403；而 `play_addr.url_list` 里的 `www.douyin.com/aweme/v1/play/` 此前被媒体白名单静默丢弃 | `internal/site/douyin/work.go` 透传 `NeedSetToken`；`internal/site/douyin/client.go` 新增 `ResolvePlayRedirect`（仅向主站 `/aweme/v1/play/` 发带 Cookie 且禁止自动跟随的单次请求，校验非水印与登录/验证拦截及公网 HTTPS 443 目标地址）；`internal/download/douyin_video.go` 与 `douyin_note.go` 对 `NeedSetToken=true` 先解析无 Cookie CDN 地址再下载，普通作品在直链刷新后仍 403/404 时回退解析 | `TestParseAwemeWorkPropagatesNeedSetToken`、`TestResolvePlayRedirectSecurityAndBehavior`、`TestDouyinNeedSetTokenPlayRedirectFallbackAndPartialReason`；真实站点临时验证 2 条 `need_set_token=true` 图文（混排视频页与 3 页实况照片）全部一次完成（`complete`、0 Cookie 泄露；3 条合共 7 次 API、20 次媒体请求） |
| 3 | **B 站动态 `code=4101152` 显示“未识别的错误代码”且开播卡片进入动态缓存队列**：`/x/polymer/web-dynamic/v1/detail` 对已失效的 `DYNAMIC_TYPE_LIVE_RCMD` 返回 `code=4101152`（“动态不可见”） | `internal/site/bilibili/errors.go` 将 `4101152` 映射为 `ErrKindUnavailable`（“动态不可见或已删除”）；`internal/service/batch_cache.go` 的 `dynamicQueueCandidate` 将 `DYNAMIC_TYPE_LIVE_RCMD` 标为不可缓存跳过 | `TestClassifyBiliCodeDynamicUnavailable4101152`、`TestDynamicQueueCandidateSkipsLiveRcmd` |
| 4 | **B 站转发动态附带视频在媒体库按原视频作者显示**：磁盘已按 DEC-056 存在转发者的 `forwards/` 目录且带 `via.reason="forward"`，但 `library.Statistics()`、`ListItems()`、`LatestPubByAuthor()` 与 `CountCachedWorks()` 未将 `via.reason="forward"` 归属到转发者，导致原视频作者出现在媒体库作者列表 | `internal/library/statistics.go`、`feed.go`、`works.go` 统一将 `via.reason="forward"` 归入转发者名下，不再为仅因转发缓存的视频生成原上传者作者项或错误统计 | `TestForwardVideoBelongsToForwarderNotUploaderInLibrary`、`TestCollabVideoBelongsToMemberItWasCachedFor` |
| 5 | **抖音共创作品从用户页点进详情页再缓存时丢失“为该账号缓存”上下文**（另确认用户提到的两条旧条目缓存于 2026-11-19，早于 2026-11-20 DEC-063 改动，按规则不自动搬动旧文件夹） | `internal/ui/app.go`、`navigation.go`、`douyin_user_view.go`、`douyin_work_view.go` 对称补齐 `openDouyinWorkFor` 与 `douyinCacheFor`，在用户页点入作品详情再点“缓存视频/缓存图文”时保留 `ForUID`/`ForName` | `TestDouyinWorkDetailPreservesUserContextForCoCreationCache` |
| 6 | **任务页长标题挤出右侧进度百分比、且“部分完成”卡片缺少原因**：卡片内层列与标题行缺少 `Shrink(1)`，且 `manager.go` 结束时清空了 `partial` 任务的 `ErrorMessage` | `internal/ui/tasks_downloads_view.go` 为卡片内层列、标题行与原因文本补 `Shrink(1)`，并将 `partial` 进度条与原因条改为醒目黄色提示；`internal/download/manager.go`、`douyin_note.go`、`douyin_video.go`、`dynamic.go` 写入并保留缺失摘要，`task_store.go` 读取旧空原因 `partial` 任务时自动从 `item.Missing` 回填 | `TestTasksDownloadsLongTitleAndPartialReasonDoNotClip`、`TestDouyinNeedSetTokenPlayRedirectFallbackAndPartialReason` 及 `uishots` 192 张截图（`tasks-720x860.png` 实眼确认无裁切） |

### 8.3a 2026-11-21 订阅“内容”里删除已缓存条目（DEC-064）

| 现象 | 修复 | 证据 |
| --- | --- | --- |
| 已缓存的动态（尤其只有文字的）没有地方删除：媒体库按文件列图片与视频，订阅详情“内容”的帖子没有“⋯” | [DEC-064](../decisions.md#dec-064)：`PostCardData.LocalManage`，订阅“内容”的帖子右上角“⋯”（`renderCachedPostMenu`：“在资源管理器中显示”、红字“删除”），删除走媒体库同一个 `PlanDelete` 与确认弹窗；弹窗标题写“类型：标题”（动态无标题用正文第一行），大小计入一并删除的引用视频，共用视频说明改从动态的角度写 | `TestSubscriptionDetailContentTabCrossSiteZeroRequestsAndEmptyState` 加断言：菜单为“在资源管理器中显示,-,删除”，确认弹窗写“动态：<标题>”，确认后动态离开列表、登记图片从磁盘删除、全程 0 次站点请求 |

### 8.4 最终打包与验证证据（替换第 1、7 节的同类产物）

- **门禁与测试**：`go run ./tools/check` ALL CHECKS PASSED（1m27.34s，含双平台依赖方向检查、Linux amd64/arm64 远程构建与 `GOOS=linux go vet ./...`、`tools/checkdocs` 与 `git diff --check HEAD`）；全仓含测试包全通；`go run ./tools/uishots -out artifacts/uishots` 64 场景 / 192 张 PNG 与全部断言通过（0 横向溢出）。
- **桌面开发版**（基点 `bdff0ea62183`）：`go run ./tools/package -install` 将本机 TRACE Dev 从 `0.1.3-dev.202611210535` 静默升级至 `0.1.3-dev.202611210624`（`artifacts/package/install-dev.json`：`installed=true`、`relaunched=true`、`background=true`、`inJob=true`，2184 ms）。`trace-dev-setup-0.1.3-dev.202611210624-amd64.exe` SHA-256 `f26831005f121091dcc181c837fe5c350bf65f8f5e6aedf6c7d00a3c1cc789cf`（8.2 MiB）；`trace.exe` SHA-256 `5bfdcc5faa66b4153489fb63690051b2a86daf7410719a741e4aa79ef83df43d`（25.4 MiB）。
- **远程版**（基点 `bdff0ea62183`）：`go run ./tools/package -server` 产出 `trace-server-0.1.3-dev.202611210624-linux-amd64.tar.gz` SHA-256 `44b698727ce7b17b02011e58a634f20be08fd270cd48b85481b026e512c930e8`（程序 16.3 MiB）；`trace-server-0.1.3-dev.202611210624-linux-arm64.tar.gz` SHA-256 `36003c7544b00032e3db26829e1a5c846dc1a270d352c7ced49aad9a4ccffef3`（程序 15.2 MiB）。
- **冒烟**：桌面冒烟 `go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke` PASSED（3307 ms，`quietMode=hidden`，托盘专用工作集 `0.61 MiB`，`webview2=0`）；远程冒烟 `go run ./tools/smoke -server` PASSED（1339 ms，`kill+restart`）；Linux 真进程远程冒烟随 GitHub Actions CI 运行。
- **仍为 `not-run`**：`go test -race`（本机无 CGO/gcc）、真实 VPS/站点/Google Drive 实机检查（H-003）及桌面实机体验（H-001/H-002）。
