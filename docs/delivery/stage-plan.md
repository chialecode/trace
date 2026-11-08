# 当前阶段计划：M1（全 Go + MyGo 原生界面）

- **阶段编号**：M1
- **计划标识**：`TRACE-M1-03`，修订 4（2026-11-08，第二轮实机反馈返工，见 [§13](#rev4)；修订 3 见 [§12](#rev3)，修订记录见 §0）
- **分支**：`codex/m1-mygo-bilibili`（父提交为初始化提交 `b208ad6c5d2de24202226e76f279db01196c7879`；文档基础与 M1 实现合并为单一阶段 DCO 提交）
- **适用需求**：FR-AUTH-001～005、FR-LINK-001～002、FR-HIST-001、FR-VIDEO-001、FR-USER-001～003、FR-DYN-001、FR-IMG-001、FR-ERR-001、FR-CACHE-001～009、FR-FEED-001、FR-LIB-001～006、FR-SET-001～002、FR-SUBS-001～003、FR-SUB-001～002、FR-LIVE-001～002、FR-RUN-001、FR-STORE-001；NFR-DEV-001、NFR-UI-001～002、NFR-SEC-001～002、NFR-DATA-001～003、NFR-NET-001～002、NFR-MEDIA-001、NFR-PERF-001、NFR-RES-001
- **不在本阶段**：FR-PLAY-001（在线播放）、FR-FEED-002、FR-LIVE-003、FR-SITE-001、FR-RUN-002、FR-AUTH-006
- **适用验收场景**：AC-01 ～ AC-26（[路线与验收 §4](roadmap.md#acceptance)）
- **核心不变量**：INV-01 ～ INV-14（[架构设计 §7](../design/architecture.md#invariants)）

---

## 0. 修订记录

| 修订 | 内容 |
| --- | --- |
| 1 | 初版：原型 PR-1～PR-4、工作包 P1～P16、必测用例 C01～C40 |
| 2 | 按 MyGo `v0.8.1` 源码改正 API 名称（§2.2）；补单实例与通道隔离、下载超时、投稿列表风控参数、旧缓存文件夹识别、FLAC 兜底；原型代码改为入库、大文件测试改为门控、原型失败时的处理；新增执行顺序（§5）、工作包与用例的对应、MVP 与 LOOP 边界（§8）、阶段整理清单（§9）；**新增“安静测试”（静音、不抢焦点、不碰用户已装应用，§6）与“路径一律小写 `trace`”（§1.5）**；真实站点旧回执只证明旧输入，新实现另立 ACT-006 小额复核 |
| 3 | 首轮实机反馈与 ACT-006 结果（DEC-033）：A 已修 F-029～F-032，B 返工 R1～R7（[§12](#rev3)） |
| 4 | 第二轮实机反馈（DEC-037）：A 已修播放卡顿 F-043，B 返工 R8～R13（[§13](#rev4)） |

---

2026-11-07 用户要求 Agent B 重新检查实现并修复不符合或未完成的行为，2026-11-08 再次要求复查至可交 A。两次均沿用修订 2 的完整范围，复查差异、验证边界以[阶段报告](stage-report.md) F-001～F-025 为准；2026-11-08 A 集中复核通过并直接修复 F-026～F-028（报告 §5～§7）；不将原交付声明代替实际代码与证据。

## 1. 目标与边界

1. **目标（DEC-032）**：纯 Go + [MyGo](https://github.com/egoist/mygo) 原生界面（`github.com/egoist/mygo/ui` + `github.com/egoist/mygo/audio`）实现基本框架与哔哩哔哩全部 M1 功能，不依赖 WebView2；`internal/ui` 只经 `service.Backend` 接口调用 `internal/service`，后端零 GUI 依赖（INV-05），为以后的无头录制模式留边界（[架构设计 §9](../design/architecture.md#headless-ext)）。持续同步 MyGo 最新版（当前 `v0.8.1`），不保留旧版兼容代码。
2. **代码基点**：当前唯一阶段提交的父提交为初始化提交 `b208ad6`，文档基础已并入阶段提交；本轮 B 从 `681b6d4308caa018abd21559123c90b69c1025f1` 的修订 3 实现复核，开始时工作区干净，保留原修复。设计中的草案接口与候选命令不能替代实际代码，当前源码与证据入口见报告。
3. **本机文件保护**：不碰 `dist/` 下带日期的旧包目录（`dist/trace-0.1.0-windows-x64-*`，其中 `20261105-4/trace-data/` 有用户数据，只读）、`.env.local`、`.local/`。`dist/trace-dev/` 由 `tools/package` 更新登记产物，当前包与哈希以[报告 §3](stage-report.md)和 H-001 为准。
4. **真实站点**：本轮 B **不发任何真实站点请求**；测试只连 `127.0.0.1` 上的 `httptest.Server` 夹具。ACT-004～ACT-007 的回执（`artifacts/site-check/`）及 `.local/site-check/` 额度账本原样保留，**只证明当时的实现与输入**。ACT-006 已执行但缓存/直播步骤未通过，ACT-007 已复跑通过；实际结果见[报告 §7](stage-report.md#feedback-1)，本轮不把旧回执写成新代码通过。真实请求的现有授权与额度规则以[USER-ACTIONS](../../USER-ACTIONS.md#standing-site-requests)为准。
5. **路径一律小写 `trace`**（用户 2026-11-07 指示）：TRACE 自己创建或命名的目录与文件只用小写 `trace` / `trace-dev`。这包括可执行文件、安装目录、数据目录、缓存目录、MyGo 子目录、日志、临时目录、安装包、`dist/` 输出、测试临时目录前缀，以及单实例键和应用名。界面显示名（窗口标题、开始菜单与“设置 > 应用”里的名称、托盘提示）仍是 “TRACE” / “TRACE Dev”。做法：
   - `mygo.json`：`name: "trace"`、`slug: "trace"`、`windows.displayName: "TRACE"`、`windows.installDirName: "trace"`、`identifier: "io.github.chialecode.trace"`；dev 通道由 `tools/package` 临时写成 `trace-dev` / `TRACE Dev` / `io.github.chialecode.trace-dev`。产物是 `trace.exe`、`trace[-dev]-setup-0.1.0-amd64.exe`，安装到 `%LOCALAPPDATA%\Programs\trace[-dev]`。
   - `main.go` 启动时先调用 `mygo.App.SetName(loc.AppName)`（`trace` 或 `trace-dev`）。这样 MyGo 内部的默认路径和单实例键也是小写，而且两个通道互不冲突（DEC-029 要求可以同时安装）。
   - 断言：`appdirs` 单元测试检查各通道推算出的每一级 TRACE 自有路径都是小写；冒烟检查新建的目录与文件名不含大写 `TRACE`（C36）。
   - 开始菜单快捷方式的文件名由 MyGo 按显示名生成（`TRACE Dev.lnk`），属于用户看到的名称，作为例外保留；要改成小写，见 USER-ACTIONS 的 Q-008。

---

## 2. 依据、查证与选型

### 2.1 本机参考与开源查证

- 本机参考索引：已按[开发流程 §1.3](../dev-rules/workflow.md#local-reference)找到。本阶段相关的领域有：用户投稿列表的分页与风控参数、B 站直播录制（开播检测、断线重连、分段）、无界面录制的状态机与重连退避、桌面录制列表与任务反馈。只参考做法，不复制代码，仓库里不写项目名。
- 开源依赖已在 2026-11-07 对照模块缓存里的源码核对：`github.com/egoist/mygo v0.8.1`、`github.com/Eyevinn/mp4ff v0.62.0`（`mp4.Defragment` 存在）、`modernc.org/sqlite v1.61.1`。B 于 2026-11-08 接入已选 `github.com/yeqown/go-qrcode/v2 v2.3.0` 及 `github.com/yeqown/reedsolomon v1.0.0`，核对 MIT 许可；既有 `golang.org/x/image v0.46.0` 注册 WebP 解码，改为直接依赖，许可随包收录。

<a id="mygo-api"></a>
### 2.2 MyGo v0.8.1 实际 API 对照（以源码为准）

| 用途 | 实际 API | 注意 |
| --- | --- | --- |
| 原生窗口 | `mygo.NewWindow(mygo.WindowOptions{Title, Width, Height, Content: ui.View(view)})`，在 `mygo.App.WhenReady` 里创建，`mygo.App.Run()` 进入循环 | **没有** `NewNativeWindow` / `NativeWindowConfig` |
| 后台线程更新界面 | `win.Update(func(){…})`（在主线程改状态并重绘）、`win.Invalidate()`、`mygo.RunOnMain` | **没有** `ui.RunOnMain`、`ui.Go`、`ui.Load` |
| 图片与视频帧 | `ui.NewBitmap(img)` → `ui.Image(c, bmp)`；每帧 `bmp.Update(rgba)` | `ui.Image` 接收 `ImageSource`（`*ui.Bitmap` 或 SVG），不能直接传 `*image.RGBA`；**没有** `ui.SetImageLoader` |
| 视频组件 | 不用 `ui.VideoPlayer` | 它没有 Seek、没有音频时钟，桌面开启“减少动态效果”时会停在第一帧；播放层自己按音频时钟驱动 `Bitmap.Update` |
| 无障碍名称 | 元素的 `.Label("…")`；测试用 `tt.Access` / `tt.AccessByRole` | **没有** `AccessibilityLabel` |
| 虚拟列表 | `ui.List(c, &ui.ListState{…}, n, row)` | — |
| 测试器 | `ui.NewTester(view, w, h)`：`Click`、`Type`、`Compose(text, caret)`、`Scroll`、`SetSize`、`Find`（按标签或文字返回 `Rect`）、`Image()`、`Advance(d)` | 在内存里运行，不建窗口、不出声 |
| 单实例 | `mygo.App.RequestSingleInstanceLock()`、`mygo.App.OnSecondInstance` | 锁按“应用名 +（打包后的）标识”区分，socket 放在 `os.TempDir()`；所以必须先 `SetName`，冒烟还要隔离 `TEMP`/`TMP`（§6） |
| 路径 | `mygo.App.SetPath(mygo.PathUserData / PathCache / PathLogs, p)` | 在 `Run` 前设置 |
| 窗口生命周期 | `mygo.App.OnWindowAllClosed(fn)` 让进程在无窗口时继续运行；`win.Hide()`（紧跟 `NewWindow` 调用时窗口完全不出现）、`win.ShowInactive()`（显示但不抢焦点） | — |
| 托盘与通知 | `mygo.NewTray(mygo.TrayOptions{ID, Icon, ToolTip, Menu})`、`mygo.NewNotification(mygo.NotificationOptions{…, Silent})` | — |
| 音频 | `audio.NewPlayer(dec)`，`dec` 实现 `audio.Decoder`（`SampleRate`、`Channels`、`Read([]float32)`、`SetPosition(frame)`、`Len`）；`Player.Position()`、`Seek`、`SetVolume` | 创建 `Player` 就会打开系统默认输出设备（WASAPI）；没有公开的静音设备开关，所以测试不创建它（§6） |
| 打包 | `go tool mygo build`（CLI 记为 `go.mod` 的 `tool` 依赖）；NSIS 安装包 `RequestExecutionLevel user`，装到 `%LOCALAPPDATA%\Programs\<installDirName>` | 安装包会先关闭正在运行的同名程序，并写开始菜单快捷方式和 HKCU 卸载项（§6） |

B 用到其他 MyGo API 时先查 `$(go env GOMODCACHE)/github.com/egoist/mygo@v0.8.1/docs/`，不要按记忆写。

### 2.3 选型与版本

| 领域 | 选定方案与版本 | 边界与约束 |
| --- | --- | --- |
| Go 工具链 | `go 1.27.3`（`CGO_ENABLED=0`） | 全仓库纯 Go |
| 原生外壳与界面 | `github.com/egoist/mygo v0.8.1` | 只有 `main.go`、`internal/shell`、`internal/ui`、`internal/player` 可以引用 `github.com/egoist/mygo/...`（INV-05） |
| 本地播放 | Windows Media Foundation（`IMFSourceReader`，经 `golang.org/x/sys/windows` 做纯 Go COM 调用）解码，画面送 `*ui.Bitmap`，声音经 `internal/player` 的 `AudioSink` 接口交给 `mygo/audio` | `AudioSink` 与解码后端都可以替换：测试注入合成解码器和静音输出（§6） |
| 弹幕与字幕 | `internal/player` 自己写纯 Go 的弹幕排版器与 WebVTT 解析器 | 按播放时刻 `t` 的纯函数计算 |
| 登录二维码 | `github.com/yeqown/go-qrcode/v2 v2.3.0`，只用编码出的矩阵，自己画成 `*image.RGBA` 再 `ui.NewBitmap`（不用它的图片写出器，少带依赖）；备选 `rsc.io/qr` | B 已接入并核对 MIT 许可及传递 `reedsolomon v1.0.0`；只在内存里，不写盘 |
| SQLite | `modernc.org/sqlite v1.61.1` | WAL；`library.db` 可从 `item.json` 重建 |
| MP4 | `github.com/Eyevinn/mp4ff v0.62.0` | 点播合并（`moov` 在前、约 1 秒交错、`elst`）；直播用 `mp4.Defragment` 收尾 |
| 弹幕 protobuf | `google.golang.org/protobuf v1.36.12`（只用 `encoding/protowire`） | 手写解码，未知字段跳过 |

---

## 3. 隔离原型

原型代码**入库**，放在对应包的测试里，或放在 `tools/proto/<名称>/`。`artifacts/` 被 Git 忽略，只放结果：`artifacts/prototypes/RESULTS.md` 与 `pr1.json`～`pr4.json`。B 开工前先清空旧内容（§9）。原型全部按 §6 的安静规则运行。

| 编号 | 验证问题 | 通过标准 | 失败时的处理 |
| --- | --- | --- | --- |
| **PR-1** | MF `IMFSourceReader` 能否在不开 cgo 的情况下解码 MP4 视频帧（RGB32 → RGBA）和音频采样（Float/PCM）、Seek，以及支持 > 2 GiB（`co64`）？本机有没有 HEVC、AV1 解码器？ | 合成 AVC+AAC 样本：帧的宽高正确、非全黑，音频的采样率、声道数正确、波形非零；Seek 后首帧时间戳误差 ≤ 1 帧；堆峰值 < 100 MiB。HEVC、AV1 样本记录“可解码 / 缺解码器”。**只解码读取，不播放出声** | 纯 Go COM 走不通：播放层改为“用系统播放器打开”，弹幕与字幕叠加记为 blocked（C22），其余工作照常推进，报告写明并由 A 决定。缺 HEVC/AV1 解码器：播放层显示“本机缺少 HEVC/AV1 解码器”和“用系统播放器打开”，不能黑屏或无声却不提示 |
| **PR-2** | `mp4ff` 合并 DASH（AAC、FLAC 30251、E-AC-3 30250），以及直播 fMP4（多 `moof`/`trun`、负 CTO）收尾 | 合并后 `ftyp`+`moov` 在 `mdat` 前，约 1 秒交错，带 `elst`，PR-1 能解码；杜比按 Q-005 另存 `P01.audio-30250.m4a`；fMP4 收尾时长误差 ≤ 0.1s，MF 能打开并 Seek | MF 解不了 FLAC-in-MP4：FLAC 也按杜比的办法另存 `Pxx.audio-30251.m4a`，主 MP4 合入最佳 AAC（与[数据设计](../design/data-and-cache.md#merge)的“能播放才合并，否则另存”一致） |
| **PR-3** | 10,000 条弹幕时，单帧排版耗时、密度过滤、拖动后有无残留；WebVTT 解析（HTML 实体、`-->`） | 平均 < 1 ms；从 30s 拖到 120s 后只剩 `[114s, 120s]` 窗口内的弹幕；`ui.NewTester` 导出的画面非空 | 降到“只显示滚动弹幕、限制同屏条数”，记为 LOOP |
| **PR-4** | 原生窗口关到托盘：窗口销毁、内存释放、没有 WebView2 子进程，同一进程内重建 5 次；NSIS 安装与卸载 | (1) **窗口按 §6 的安静方式创建**：关闭后进程继续运行，`msedgewebview2.exe` 为 0，专用工作集 < 60 MB，每次重建就绪 < 2s。(2) 安装包测试**用单独的测试应用名** `trace-ptest`（§6），装到临时目录、覆盖安装、卸载，并核对快捷方式与 HKCU 卸载项已清除 | (1) 不达标：如实记录数值，A 决定是否放宽 NFR-RES-001。(2) 默认不跑，需 `TRACE_RUN_INSTALLER_TEST=1`；没跑就记 not-run |

`co64` 的大文件样本：用 NTFS 稀疏文件（`FSCTL_SET_SPARSE` 后再 Seek 写），不实际写满 2 GiB 数据。这类测试只在 `TRACE_RUN_LARGE=1` 时运行，不进默认的 `go test ./...` 和 CI。

---

## 4. 工作包

每个工作包末尾的“证据”写对应的必测用例。凡是写着“防回归”的地方，括号里的行为描述就是要求本身，不另有出处。

### P1：工程骨架、通道与数据位置（`internal/appdirs`、`internal/storage`、`internal/settings`、`internal/uistate`）
- `go.mod`（`go 1.27.3`，`tool github.com/egoist/mygo/cmd/mygo`）、`mygo.json`（按 §1.5 的小写命名）。
- `internal/appdirs`：三个通道 `release`、`dev`、`source`（`-ldflags -X` 注入）；`AppNameForChannel` 返回 `trace` 或 `trace-dev`；“文档”目录用 `SHGetKnownFolderPath(FOLDERID_Documents)` 取，不引用 MyGo（INV-05）。
  - 默认缓存位置（`cacheRoot` 为空时）：`release` 用 `<文档>\trace`，`dev`、`source` 用 `<文档>\trace-dev`。`TRACE_DATA_DIR` 只覆盖数据目录，不会把缓存挪到数据目录里；测试要隔离缓存时用 `TRACE_CACHE_DIR` 或 `settings.CacheRoot`（防回归）。
  - 数据目录独占锁 `app.lock`（`LockFileEx`，`EXCLUSIVE | FAIL_IMMEDIATELY`）：拿不到锁就立即返回错误，不打开任何数据文件（INV-14）。
- `internal/storage`：原子写（临时文件 → `Sync` → `Close` → `Rename`）；损坏的 JSON 改名为 `<name>.corrupt-<时间>` 保留（NFR-DATA-001）；`history.json`（200 条，按 `kind+id` 去重，不存凭证和签名地址）。
- `internal/settings`、`internal/uistate`：全部字段、默认值、范围校验、忽略未知字段、损坏后改名恢复。
- 证据：C03、C28、C36。

### P2：凭证（`internal/credential`）
- DPAPI（当前用户范围）加解密 `<数据目录>/credentials/bilibili.bin`；非 Windows 平台用测试桩。
- 结构字段：`cookies`、`refresh_token`、`mid`、`uname`、`face`、`obtained_at`、`source`（`qr` | `manual`）、`renewed_at`、`confirm_pending`、`pending_old_refresh_token`。
- 手动 Cookie：上限 8 KiB；可带 `Cookie:` 前缀，`;` 或换行分隔；只保留 `SESSDATA`、`bili_jct`、`DedeUserID`、`DedeUserID__ckMd5`、`sid`；缺 `SESSDATA` 返回 `invalid_input`；任何错误消息都不回显输入（INV-01）。
- 证据：C05、C37。

### P3：哔哩哔哩核心协议（`internal/site/bilibili` 第一部分）
- **两个 HTTP 客户端**：
  - API 客户端：15 秒总超时，不自动跟随重定向；全局互斥 + 500ms 最小间隔，可以用 `context` 取消（NFR-NET-001）。
  - 媒体客户端（P6 使用）：连接与响应头 15 秒超时，读 body 时 30 秒没有进展才算超时，**不设总超时**。否则 8 MiB 分段在约 550 KB/s 以下的网速会每段必然超时。
- 错误分类：`auth_required`、`risk_control`、`rate_limited`、`blocked`、`unavailable`、`network`、`schema_changed`。没有凭证时，除扫码接口和用候选 Cookie 的 `nav` 校验外，请求在发出前就返回 `auth_required`（INV-07）。遇 `-101` 或 `nav` 返回 `isLogin=false` 时调用 `onAuthExpired`。
- WBI 签名，密钥缓存 30 分钟，遇 `-352` 刷新一次。`ResolveLink`：BV、av、`b23.tv`（手动处理一次 302，拒绝跳到外站）、空间链接、`UID:xxx`、纯数字 UID、`live.bilibili.com/<房间号>`（经 `room_init`）（FR-LINK-001/002）。
- 扫码登录：180 秒过期，用 flowId 隔离；取消、过期或开始新流程后，丢弃旧流程的轮询结果（INV-03）。
- 视频详情、用户资料与统计、空间投稿（**按[协议资料 §投稿列表](../research/bilibili.md#arc-search)带 `web_location`、`dm_img_*`、`w_webid` 参数，缺这些参数时真实站点返回 -412**）、用户动态列表（各类型和降级展示；原图 URL 去掉尺寸后缀）、播放地址（清晰度排序表、编码、无损与杜比音轨）。
- 证据：C01、C02、C04、C11、C12、C13。

### P4：哔哩哔哩扩展协议（`internal/site/bilibili` 第二部分）
- **登录续期（FR-AUTH-003）**：`CookieInfo`；`GenerateCorrespondPath`（RSA-OAEP SHA-256）→ `correspond/1/<hex>` 取出 `refresh_csrf`（取不到或非 200 返回 `schema_changed`）；`RefreshCookie`（`csrf` 用旧 `bili_jct`，带旧 `refresh_token`）；`ConfirmRefresh`（防回归：**`csrf` 必须用新 Cookie 的 `bili_jct`，同时传替换前的旧 `refresh_token`**）。
- 关注分组与关注列表（只读，每页 50 条）。
- 弹幕：分段数 `ceil(duration/360)`，`protowire` 解码，按 `idStr` 去重、按 `progress` 排序，原子写出 XML。
- CC 字幕：`x/player/wbi/v2` 列表（核对 `bvid`/`cid`，跳过空 URL，识别 AI 字幕）；字幕 JSON 上限 5 MiB，含 `auth_key` 的地址只留在内存；转换为 `.vtt`/`.srt`，正确转义。
- 直播：按 uid 批量查状态（每批 ≤ 50，区分 `live`/`offline`/`round`/`none`）；`getRoomPlayInfo` 拒绝加密流，从 `http_hls`/`fmp4` 按 `codecOrder` 取最高画质，官方节点排在 MCDN 前。
- 证据：C08、C09、C10、C21、C30、C34。

### P5：媒体库（`internal/library`）
- `library.db`（WAL）与 `item.json` 正本；`quick_check` 失败时改名保留（连同 `-wal`、`-shm`），从 `library-roots.json` 登记的保存位置重建索引（NFR-DATA-002）。
- **`item.json` 兼容**：开工前只读查看旧缓存 `dist/trace-0.1.0-windows-x64-20261105-4/trace-data/library/` 里的 1～2 个 `item.json`，确认字段（顶层有 `author`、`cachedAt`、`dynamicType`、`id`、`kind`、`media`、`pubTs`、`site`、`status`、`typeLabel`、`url`、`version`、`videos`）与[数据设计](../design/data-and-cache.md#cache)一致。不一致就以设计为准，同时保证能读入旧格式。测试夹具用**合成**数据（ID、昵称、URL 全部替换），不复制原文件。
- 状态圆点推算（绿、黄、红、进度环、未缓存，并带无障碍文字）。
- 主页信息流的双向键集分页 `(pub_ts, key)`，可按 `authors` 过滤。
- `MediaPath(key, relPath)` 路径白名单（INV-08）：只允许清单登记的文件；拒绝 `.`、`..`、反斜杠、冒号、空字节；用 `Lstat` + `EvalSymlinks` 拒绝符号链接和目录联接逃逸。
- `PlanDelete`/`Delete`（INV-09，防回归）：正在录制的条目拒绝删除；被别处引用的视频保留；只删登记的文件、`.part` 和 `item.json`，再自底向上删变空的目录；目录里有用户自己的文件时保留，并返回 `preservedUserFiles`；待清理记录写进 `pending-deletes.json`。
- `AddLibraryRoot`（FR-LIB-006）：**只要文件夹里有 `library/` 子文件夹就当作旧数据目录**（`settings.json` 可有可无，旧缓存实际就没有这个文件）；不跟随链接，一次最多扫描 100,000 个清单，重复的键跳过并计数。
- 证据：C19、C24、C25、C26、C27、C28。

### P6：下载（`internal/download`）
- 任务队列，并发 1～4（默认 2）；重启后未完成的任务恢复为“已暂停（应用关闭）”。
- 8 MiB 分段续传：用 P3 的媒体客户端；闭区间 `Range`，校验 `206` 和 `Content-Range`；断连、超时、重置、5xx 从断点重试，最多 5 次（1/2/4/8/16s，±20% 抖动），主备节点轮换；403/404/410 时每个分 P 最多重取一次播放地址；遇 412、`-352`、`-412` 暂停为“已暂停（风控）”，不重试；尝试日志脱敏（INV-08）。
- 选流与合并（NFR-MEDIA-001）：音频按 30251 > 30250 > 30280 > 30232 > 30216；FLAC 和杜比按 PR-2 的结论处理（合入或另存，主 MP4 合入最佳 AAC）；校验大小、轨道、时长（误差 ≤ 2s）和 SHA-256 通过后才改正式名（INV-06）。
- 弹幕和字幕随视频保存，失败的记入 `missing`；`CacheSupplement` 只补存弹幕和字幕。
- 逐文件进度（FR-CACHE-009）：文件计划表持久化在 `library.db`。
- 画质升级：24 小时冷却，`UpgradeCandidates` ≤ 50 个，遇风控即停；`Upgrade` 只重下档位较低的分 P，校验通过后原子替换。
- 证据：C15、C16、C17、C18、C20、C21、C23。

### P7：订阅（`internal/subscription`）
- `subscriptions.json` 原子写，写前把上一版存为 `.bak`；损坏时改名保留，并提供 `RestoreFromBackup`。
- 账号键 `<site>:<uid>` 全局唯一，一个订阅最多 20 个账号；`Link` 遇到冲突返回 `conflict`；`Move` 在同一把锁里原子完成移出和移入（防回归）；删除订阅不删缓存，也不调站点写接口（DEC-031、INV-11）。
- 检查更新（INV-12）：第一次只读第 1 页，全部标为已看并记起点（置顶动态不作起点）；之后最多翻 3 页，读满 3 页还没遇到起点就记 `gap=true`；每个账号保留 200 条；不属于任何订阅超过 30 天的账号记录在启动时清理；账号之间间隔 ≥ 2s（测试注入时钟）；遇风控、限流、未登录整轮停止；连续 3 个 `network` 错误也停止。
- 定时调度：每分钟醒来一次；每轮 ≤ 30 个账号；退避 `interval × 2^n`（上限 24h），持久化；规则触发的请求共用每小时 200 次的令牌桶，额度不够就整轮推迟。
- 自动缓存：开规则时 `since = now`；只在正常结束的轮次后建任务；每轮 ≤ 20 个，超出的记 `backlogCount`。
- 证据：C29、C31、C32、C33。

### P8：直播（`internal/live`）
- 开播状态在内存缓存 60 秒，按批查询。
- HLS fMP4 录制：按 `TARGETDURATION`（夹在 1～5s）轮询，m3u8 上限 1 MiB；`EXT-X-MAP` 每段只写一次，至多每 10s `fsync` 一次；遇 MAP 变化、DISCONTINUITY、序号跳空 > 3、重连时换段；10 秒没有新分片就等 6 秒重新取流，连续 5 分钟取不到就结束；风控或 `-101` 立即停止；剩余空间 < 1 GiB 时停止（原因 `storage`）；每段用 `Defragment` 收尾，校验后改名，失败保留 `.part` 并标黄点；关闭应用时限时 30 秒收尾；启动时把孤儿 `.part` 补转 MP4。
- 自动录制：每 `liveCheckIntervalMin` 检查一次，遵守退避和令牌桶；同一场（`live_time` 相同）被手动停止后，本场不再自动录。
- 证据：C34。

### P9：服务层（`internal/service`）
- `service.Backend` 接口与全部服务（[架构设计 §3](../design/architecture.md#services)）。
- `BootstrapAuth`/`LoginWithCookie`（防回归）：`nav` 遇网络错误或风控时，手动登录返回 `network`（不说成“凭证失效”）；启动检查遇网络错误时状态设为 `unknown`，保留凭证。
- 登录守卫：没有登录时，除 `Auth`、`Settings`、`App` 外一律返回 `auth_required`，0 次网络请求。
- 续期调度：启动后 1 分钟和之后每 12 小时，只对 `source=qr` 的凭证；新 Cookie 经 `nav` 确认（`mid` 不变）后再原子替换，然后调用 `confirm/refresh`。
- `EventHub`：没有订阅者时直接丢弃事件，不阻塞（INV-13）。
- **外部动作接口 `service.Desktop`**（打开链接、在文件夹中显示、用系统播放器打开、选择文件夹、发通知）由 `internal/shell` 实现；测试和安静模式（§6）注入记录型假实现。
- 证据：C05、C06、C07、C08、C37。

### P10：播放器（`internal/player`）
- `Decoder` 接口：`mf_windows.go`（MF，`CGO_ENABLED=0`）和 `synth.go`（确定性的合成帧、合成采样、合成时钟）。
- `AudioSink` 接口：`mygoSink`（`audio.NewPlayer`）和 `nullSink`（只按时钟推进，不打开设备）。
- 以音频位置为主时钟（没有音轨时用单调时钟）驱动 `Bitmap.Update`；Seek 时音画同步跳转。
- 没有可用解码器（HEVC/AV1 缺扩展）时返回明确的错误类别，界面给出提示和“用系统播放器打开”。
- `danmaku.go`（`LayoutAt`：模式 1～5、密度、透明度、防重叠轨道）、`subtitle.go`（`ActiveCuesAt`）。
- 证据：C22，以及 PR-1、PR-3。

### P11：原生界面（一）：外壳、三栏、登录、解析、视频、用户页（`internal/ui`）
- 主题常量（[界面规范 §5](../ui.md#visual)）与线性图标（`ui.Canvas` 路径绘制）。
- 三栏布局：≥ 1280 时 275/600/350；1000～1279 时 88/600/300（只有图标，带 tooltip 和 `.Label`）；720～999 时 88 + 自适应，右栏隐藏，快速解析框移到中栏顶部。左右栏固定，只有中栏滚动（NFR-UI-001）。
- 路由与登录守卫：失效时保存路由和已加载的数据，重新登录后回到原处（FR-AUTH-005）。
- 登录页（二维码 → `ui.NewBitmap`；粘贴凭证框用掩码，提交后立即清空）、解析页（输入法组合态按回车不提交）、视频详情、用户页（订阅归属、直播横幅、动态和投稿的虚拟列表、出错时停在原处并可重试）。
- 后台事件经 `win.Update` 回到主线程（§2.2）。
- 证据：C04、C05、C06、C07、C12、C13、C38。

### P12：原生界面（二）：主页、订阅、媒体库、看图、播放、任务、设置（`internal/ui`）
- 帖子卡片、主页信息流（`ui.List`，5000 条）、订阅（列表、添加、导入、详情四个标签）、媒体库、看图层、播放层（集成 P10；按钮动作经 `service.Desktop`）、任务页（逐文件进度）、设置页（七个分类；窄窗时二级导航）。
- 证据：C14、C22、C23、C24、C25、C26、C29、C30、C31、C38。

### P13：桌面外壳（`internal/shell`、`main.go`）
- **启动顺序**：`appdirs` 解析 → `mygo.App.SetName(loc.AppName)` → `RequestSingleInstanceLock()`（第二个实例把参数转给第一个后退出，不碰数据目录）→ `app.lock` → `SetPath(userData/cache/logs → <数据目录>/mygo、cache、logs)` → 启动服务 → `App.Run()`。
- 托盘：自绘图标（有任务时带蓝点）；状态行；“打开 TRACE”、“检查全部订阅”、“退出”（有活动时二次确认）。
- 关闭到托盘：销毁窗口，释放位图并取消订阅，`runtime.GC()` + `debug.FreeOSMemory()`；第一次关闭发一条通知；从托盘或第二个实例重建窗口（DEC-030、INV-13）。
- 安静模式（§6）在这里实现。
- 证据：C35、C36，以及 PR-4。

### P14：界面截图与断言（`tools/uishots`）
- 用 `ui.NewTester` 在内存里驱动 `internal/ui`（夹具服务、假的 `service.Desktop`、`nullSink`、合成解码器）；在 `1280×860`、`1000×860`、`720×860` 三档下覆盖修订 1 列出的全部场景。
- `tt.Find` 断言：没有横向溢出；中栏滚动前后左右栏坐标不变。输出 `artifacts/uishots/*.png` 与 `assertions.json`。
- 不建真实窗口，不出声（§6）。
- 证据：C38。

### P15：门禁、打包与冒烟（`tools/check`、`tools/checkdocs`、`tools/package`、`tools/smoke`、`.github/workflows/ci.yml`）
- `tools/checkdocs`：相对链接与锚点、无本机绝对路径、无 Cookie 或令牌文本、无参考项目名（运行时读本机索引；**找不到索引时跳过这一项并打印说明**，CI 上就是这种情况），带反例测试。
- `tools/check`：`gofmt -l`、`go vet ./...`、`go test ./...`、INV-05 依赖方向（`go list -deps`）、checkdocs、`git diff --check HEAD`。
- `tools/package`：按 §1.5 临时写入通道的 `mygo.json` 字段，调用 `go tool mygo build -platform windows/amd64`；在 `dist/.staging-<时间>/` 生成并校验，再替换 `dist/trace-dev/`（或 `dist/trace/`）；生成 `THIRD-PARTY-NOTICES.txt`、`VERSION.txt`、`SHA256SUMS.txt`。`-install` 只在用户要求时使用。
- `tools/smoke`：按 §6 的安静规则启动打包好的 `trace.exe`；检查进程存活、0 个 WebView2 子进程、数据文件位置、默认缓存位置、无杂散目录、TRACE 自有路径全部小写、单实例、关到托盘后 < 60 MB、日志里没有哨兵凭证；输出 `artifacts/smoke/smoke.json`。
- CI（`repository-quality`，Ubuntu）：运行 `go run ./tools/check`。Windows 专有的测试用构建标签或 `runtime.GOOS` 跳过；读取 `artifacts/` 的检查在文件不存在时跳过。
- 证据：C39。

### P16：额度门控与收尾
- 不再重写 ACT-004、ACT-005 的检查程序（它们的额度已基本用完，旧回执也只证明旧实现）。改为写一个通用的额度账本门控 `internal/sitecheck`：读取 `.local/site-check/usage-<ACT>.json`，超额的请求在发出前拒绝。单元测试用临时目录和 `httptest` 夹具验证“额度用尽时 0 请求”。ACT-006 授权后由 A 或 B 基于它写检查程序。
- 写阶段报告 `docs/delivery/stage-report.md`，同步更新需求、路线、状态、工程规则（实测的冷/增量构建耗时、包大小、SHA-256）和 USER-ACTIONS（H-001 的新包 SHA-256）。
- 证据：C40。

---

## 5. 执行顺序

按“先有一个能跑的窄流程，再铺满功能”推进。不逐步请示；每完成一个切片就 amend 一次本地提交，避免丢失工作。

1. **切片 A（骨架能启动）**：PR-4(1) → P1 → P13 的最小外壳（安静模式、单实例、托盘）→ P15 的 `tools/check` → 空白三栏界面能在 `ui.NewTester` 里渲染。
2. **切片 B（登录 → 解析 → 详情）**：P2 → P3 → P9 的 Auth/Parse → P11 的登录页和解析页。
3. **切片 C（缓存 → 媒体库 → 播放）**：PR-2 → P5 → P6 → PR-1、PR-3 → P10 → P12 的媒体库、播放层、任务页。
4. **铺满**：P4 → P7 → P8 → P9 其余部分 → P11、P12 其余页面 → P14。
5. **收尾**：P15 的打包与冒烟 → PR-4(2)（可选，门控）→ P16 → 阶段整理（§9）→ 报告。

---

## 6. 安静测试：静音、不抢焦点、不碰用户已装的应用

长期规则见[工程规则 §3.0 安静测试](../dev-rules/engineering.md#quiet-tests)；本节是本阶段的落实方式。目标：B 自测、`tools/check`、`tools/uishots`、`tools/smoke` 和原型运行时，**不发出声音、不弹出或激活窗口、不打开外部程序，也不影响用户正在运行或已安装的 TRACE**，并尽量少占前台的 CPU 和磁盘。

| 风险来源 | 规则 | 落实位置 |
| --- | --- | --- |
| 声音 | 测试与工具里不创建 `audio.Player`（一创建就会打开默认输出设备）。播放器测试注入 `nullSink`；PR-1 只用 `IMFSourceReader` 读取采样做断言，不送去播放 | P10、PR-1、P14 |
| 窗口与焦点 | 默认测试一律用 `ui.NewTester`（在内存里运行，不建窗口）。需要真实窗口的只有 PR-4(1) 和冒烟，它们用**安静模式**：设置 `TRACE_QUIET=1`，窗口在 `NewWindow` 之后立即 `Hide()`，从不显示，也从不调用 `Focus`/`Show`；需要真实画面时设 `TRACE_QUIET=inactive`，只用 `ShowInactive()`，不激活、不抢键盘焦点。第二个实例唤醒在安静模式下也不置前 | P13、PR-4、P15 |
| 通知与托盘 | 安静模式下不发系统通知（产品模式下首次关到托盘的通知设 `Silent: true`，只弹不响）；托盘图标照常创建，退出时移除 | P13 |
| 外部程序与对话框 | “在浏览器中打开”、“在文件夹中显示”、“用系统播放器打开”、选择文件夹都经 `service.Desktop`；测试和安静模式注入记录型假实现，断言“调用了什么”，不真的打开资源管理器、浏览器或播放器 | P9、P12、P14 |
| 防火墙弹窗 | 所有测试服务器只监听 `127.0.0.1:0`，不监听 `0.0.0.0`，避免触发 Windows 防火墙的授权弹窗 | 全部测试 |
| 控制台闪窗 | 工具启动子进程时设 `SysProcAttr{HideWindow: true}`；`trace.exe` 用 `-H windowsgui` 构建，自身不再起控制台子进程 | P15 |
| 单实例串到用户的应用 | 冒烟与 PR-4 把 `LOCALAPPDATA`、`APPDATA`、`USERPROFILE`、“文档”**以及 `TEMP`/`TMP`** 都指向隔离的临时目录（单实例 socket 放在 `TEMP`）；运行前检查 socket 路径确实在临时目录里，否则判失败，防止把参数转给用户正在运行的 TRACE | P15、PR-4 |
| 安装包关掉或卸掉用户的应用 | 安装包测试用单独的应用名 `trace-ptest`（显示名 “TRACE Proto Test”，标识 `io.github.chialecode.trace-ptest`），用 `/S /D=<临时目录>` 安装；结束后核对开始菜单快捷方式和 HKCU 卸载项都已删除。默认不跑（`TRACE_RUN_INSTALLER_TEST=1` 才跑）。**绝不在测试里安装或卸载 `trace` / `trace-dev`**；`tools/package -install` 只在用户要求时用 | PR-4、P15 |
| `mygo dev` 热重载 | 每次重编译都会重建窗口并抢焦点。B 自主工作时不用它，用 `ui.NewTester` 和 `go test`；只在用户要看界面时启动 | 工作习惯 |
| CPU 与磁盘 | 打包、PR-1 和大文件测试用低优先级运行（工具把自身和子进程设为 `BELOW_NORMAL_PRIORITY_CLASS`）；`tools/check` 运行 `go test -p` 时并行数取 CPU 核数的一半；大文件样本用稀疏文件，并且需要 `TRACE_RUN_LARGE=1` 才跑 | P15、§3 |

安静模式只在 `dev`/`source` 通道生效，`release` 忽略 `TRACE_QUIET`。冒烟结果 `smoke.json` 记下用的是哪种模式（`hidden` / `inactive`）；真实可见窗口的体验仍然由用户在 H-001 检查。

---

## 7. 必测用例

每条用例都要有对应的自动化测试或工具断言，并在阶段报告里引用实际的测试函数名和证据文件。

| 编号 | 覆盖 | 必测内容与断言 |
| --- | --- | --- |
| **C01** | FR-LINK-001 | BV、av（大小写、URL 参数）、`b23.tv`（302 到视频或空间，拒绝外站）、空间 URL、`UID:123`、纯数字 UID；非法输入和不支持的链接（`ep`/`ss`/`cv`）返回 `invalid_input`/`unsupported_link`，0 请求 |
| **C02** | FR-LINK-002 | `live.bilibili.com/<房间号>`（含短号）经 `room_init` 换出长号和主播 UID |
| **C03** | FR-HIST-001、INV-01 | 最近解析：写入、去重后排到最前、200 条上限、单条删除与清空；文件里没有 Cookie 和签名地址 |
| **C04** | FR-AUTH-001、INV-03 | 扫码状态机 86101 → 86090 → 0；过期、取消或新流程开始后，旧 flowId 的成功响应被丢弃 |
| **C05** | FR-AUTH-004、INV-01/03 | 手动 Cookie：白名单字段、`nav` 校验并核对 `DedeUserID == mid`；失败时不改动已有凭证；网络错误返回 `network`；提交后输入框清空 |
| **C06** | FR-AUTH-002/005、INV-07 | 没有凭证时，业务方法返回 `auth_required`，夹具收到 0 请求；启动检查 `expired`/`unknown`；退出登录先暂停任务和录制再删凭证 |
| **C07** | FR-AUTH-005 | 运行中遇 `-101`：标为 `expired`，推送 `auth:changed`，暂停任务；界面转到登录页并保留已加载的数据，重新登录后回到原路由 |
| **C08** | FR-AUTH-003、DEC-031 | `source=qr` 才做续期检查；`refresh=false` 只更新检查时间；`manual` 不发 `cookie/info`，返回 `reason="manual"` |
| **C09** | FR-AUTH-003、NFR-SEC-002 | 完整续期闭环；`confirm/refresh` 用新 `bili_jct` + 旧 `refresh_token`；确认失败时，下次先用 `pending_old_refresh_token` 补做 |
| **C10** | FR-AUTH-003、INV-01 | 续期任一步失败：原凭证完好；遇 `-101`/`86095` 转为 `expired` |
| **C11** | NFR-NET-001 | 相邻请求间隔 ≥ 450ms；WBI 固定向量（含 `arc/search` 的 `w_webid` 向量）一致；遇 `-352` 刷新一次密钥 |
| **C12** | FR-VIDEO-001、FR-USER-001～002 | 单 P/多 P 视频、用户资料、投稿分页（带风控参数，按 `bvid` 去重） |
| **C13** | FR-DYN-001、FR-ERR-001、INV-02 | 各类动态的解析与降级；翻第 2 页遇风控或网络错误时保留第 1 页，显示原因和“重试” |
| **C14** | FR-IMG-001、DEC-033 | 沉浸式看图层：本机文件或原图 URL（无外站 Referer，用 https）、圆形悬浮按钮（关闭、上一张/下一张、适应/1:1）、鼠标静止约 2 秒自动隐藏与唤醒、缩放、平移、左右键切换、Esc 关闭 |
| **C15** | FR-CACHE-001/002、DEC-024 | 最高画质与音质选流；多 P 全部缓存；同一 `bvid` 只存一份 |
| **C16** | Q-005、NFR-MEDIA-001 | `moov` 在前、交错、`elst`；FLAC 与杜比按 PR-2 的结论合入或另存，`item.json` 记录 `savedAs` |
| **C17** | FR-CACHE-005 | 发出 512 KiB 后断连、5xx：自动续传，主备节点轮换，SHA-256 一致；**慢速夹具（200 KB/s）下 8 MiB 分段不因总超时失败** |
| **C18** | FR-CACHE-005 | 403/404/410 重取一次地址；412 和 `-352`/`-412` 暂停为风控；重试耗尽暂停为网络；继续后从断点完成 |
| **C19** | FR-CACHE-004、INV-06 | 校验前只有 `.part`；绿、黄、红圆点的条件 |
| **C20** | FR-CACHE-006 | 升级探测（24h 冷却）、批量检查（可取消，遇风控即停）、升级失败时旧文件完好 |
| **C21** | FR-CACHE-008 | 弹幕 XML 与 CC 字幕（AI 字幕受开关控制）；没有字幕不算缺失；失败标黄并可补全；旧视频可以只补弹幕和字幕 |
| **C22** | FR-LIB-002/004、DEC-033 | 沉浸式播放层：解码、Seek、圆形悬浮按钮与“⋯”菜单（弹幕开关/透明度/显示区域、字幕切换、分段切换、文件夹显示、系统播放器打开）、鼠标静止约 2 秒自动隐藏与唤醒、拖动后无残留、偏好持久化；**缺解码器时显示提示和“用系统播放器打开”**；测试用 `nullSink`，不出声 |
| **C23** | FR-CACHE-009 | 逐文件进度与脱敏的尝试记录，重启后仍可查看 |
| **C24** | FR-FEED-001 | 双向信息流、排序切换、无重复无遗漏、锚点恢复、“↑ N 条更新的内容”，0 网络请求 |
| **C25** | FR-LIB-001/005 | 按类型、订阅（展开为全部账号）、独立 UP 主筛选；排序 |
| **C26** | FR-LIB-003、INV-09 | `PlanDelete` 与 `Delete` 一致；引用保护；用户文件保留并计数；录制中拒绝删除 |
| **C27** | FR-LIB-006 | 只有 `library/`、**没有 `settings.json`** 的旧数据目录也能识别（合成夹具模拟旧格式）；不跟随链接；重复键跳过并计数 |
| **C28** | NFR-DATA-001/002 | 各 JSON 文件损坏时改名保留；订阅可从 `.bak` 恢复；`library.db` 重建；`updates.db` 重置起点且不冒出未读 |
| **C29** | FR-SUBS-001、FR-USER-003、DEC-031、INV-11 | 订阅增删改、唯一归属、`conflict` 与 `Move`；删除订阅 0 站点写请求 |
| **C30** | FR-SUBS-003、INV-11 | 关注导入：已订阅的跳过，0 写操作 |
| **C31** | FR-SUB-001、INV-12、DEC-033 | 第一次检查只建起点（起点之前的动态不进入更新列表，不做已看标记）、起点之后的增量更新进入更新列表、3 页上限与 `gap`、风控整轮停止 |
| **C32** | FR-SUB-002、NFR-NET-002、INV-10 | 每轮 30 个、间隔 2s、退避持久化、200/h 令牌桶 |
| **C33** | FR-CACHE-007 | `since` 规则、每轮 20 个上限与积压；被中止的轮次不建任务 |
| **C34** | FR-LIVE-001/002 | 状态四分类；换段；断流后重连；`Defragment` 收尾并入库；空间不足、风控、并发上限；手动停止后本场不再自动录 |
| **C35** | FR-RUN-001、NFR-RES-001、DEC-030、INV-13 | 关到托盘（安静模式）：后台继续、事件丢弃不阻塞、< 60 MB、0 个 WebView2；重建窗口恢复路由；退出前确认；**安静模式下窗口从未成为前台窗口（断言 `GetForegroundWindow` 始终不是 TRACE 的窗口）** |
| **C36** | FR-STORE-001、NFR-DATA-003、DEC-029、INV-14 | 各通道的 `InstallDir`、`DataDir`、默认缓存位置；**所有 TRACE 自有路径都是小写 `trace[-dev]`**；`app.lock` 生效；`%APPDATA%`、用户主目录和安装目录里没有写入；`trace` 和 `trace-dev` 两个通道的单实例互不影响 |
| **C37** | INV-01/08、NFR-SEC-001/002 | 全链路哨兵：扫描所有落盘文件、DTO 和错误消息，哨兵出现 0 次 |
| **C38** | NFR-UI-001/002、NFR-PERF-001、AC-07 | 三档宽度 0 横向溢出、固定侧栏、`tt.Compose` 组合态按回车不提交、5000 条和 500/1000 条虚拟列表 |
| **C39** | INV-05、DEC-032 | 依赖方向；`tools/check` 全绿；打包与冒烟通过；**冒烟全程没有出声、没有窗口成为前台、没有外部程序被启动（假 `Desktop` 的记录与进程列表核对）** |
| **C40** | ACT 额度门控 | `internal/sitecheck` 额度用尽时 0 请求；既有回执脱敏扫描干净（文件不存在时跳过） |

---

## 8. MVP 与 LOOP

- 每一项“MVP 必须”（[路线与验收 §3](roadmap.md#mvp-loop)）都要贯通，不能记为 LOOP。
- 允许 B 记为 LOOP（写明影响、替代方式和触发条件）的只有：PR-3 的降级方案、播放层的快捷键与画中画、托盘菜单的额外项、动画与过渡细节、设置页里非必需的说明文案。
- 原型失败按 §3 的“失败时的处理”执行，只暂停依赖它的用例，其余工作照常推进。不能为了通过而删除或弱化断言。

---

<a id="cleanup"></a>
## 9. 阶段整理清单

B 在切片 A 开始前处理（全部是 Git 忽略的生成目录，先确认路径在仓库内且不是链接）：

| 路径 | 处理 |
| --- | --- |
| `artifacts/prototypes/`（旧的 `pr1`～`pr10` 结果） | 清空，写入本阶段 PR-1～PR-4 的结果 |
| `artifacts/uishots/`、`artifacts/smoke/` | 清空，由本阶段工具重写 |
| `artifacts/site-check/` | 保留 `result.json`（ACT-004）和 `act005.json`；`act004-result.json` 与 `result.json` 内容相同，核对 SHA-256 一致后删除前者 |
| `tmp/checkdocs.exe`、`tmp/checkdocs/`、`tmp/dev.log` | 删除（旧构建产物） |
| `tmp/dev-data/` | 先看内容：只有合成数据就删除；有登录或缓存数据就保留，并在报告里写明 |
| `build/windows-amd64/`、`.mygo/dev/` | 删除，由构建重建 |
| `dist/trace-dev/` | 由 `tools/package` 整体替换，不单独删除 |
| `dist/trace-0.1.0-windows-x64-*`、`.local/`、`.env.local` | 不动（USER-ACTIONS 的本机文件一节）；2026-11-08 用户选择删除旧包并按推荐清理 `.local/`，见[报告 §14.4](stage-report.md#a-review-rev4) |

文档：本阶段文档只描述“全 Go + MyGo 原生界面”的现状；以前阶段的标签按[开发流程 §5.4](../dev-rules/workflow.md#stage-cleanup)去掉。阶段初始整理已执行；本次仍是 M1，更新当前截图、冒烟和开发包，保留用户文件与旧真实回执。被新职责文件替代的无引用下载/直播 helper 已删除，整理记录见报告。

---

## 10. 交付与验证

1. `go run ./tools/check` 0 错误（单轮预算约 10 分钟；超出时拆分并记录）。
2. `go run ./tools/uishots -out artifacts/uishots`：全部断言 `passed=true`。
3. `go run ./tools/package`，然后 `go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke`：全部 `passed=true`，记录安静模式。
4. 门控项（`TRACE_RUN_LARGE`、`TRACE_RUN_INSTALLER_TEST`）跑了就记录结果，没跑就记 `not-run`。
5. Git：全部改动 amend 进 `codex/m1-mygo-bilibili` 上唯一一个带 DCO 签名的提交（父提交 `b208ad6` 初始化提交）；**不 push，不建 PR**。

---

## 11. 给 Agent B 的交接 Prompt

```text
请作为 Agent B 执行 TRACE 阶段计划 TRACE-M1-03 修订 3（全 Go + MyGo v0.8.1 原生界面 M1，包含 §12 的返工与复核补充）。

仓库：本机 TRACE 仓库根目录（Windows）。分支 codex/m1-mygo-bilibili，父提交 b208ad6c5d2de24202226e76f279db01196c7879（初始化提交）；当前已有 M1 实现，文档基础已并入同一个阶段提交，沿报告中的当前实现与待复核 F 编号继续，不从零重做。

先读：
1. AGENTS.md、REVIEW.md、USER-ACTIONS.md、docs/decisions.md（重点 DEC-025～DEC-036）
2. docs/delivery/stage-plan.md 全文（§2.2 MyGo 实际 API 对照、§5 执行顺序、§6 安静测试、§9 整理清单）
3. docs/design/*.md、docs/ui.md、docs/research/bilibili.md
4. 本机参考索引（从仓库根逐级向上找 reference/trace/README.md，只读；仓库文件里不写参考项目名和本机路径）
5. MyGo 源码文档：$(go env GOMODCACHE)/github.com/egoist/mygo@v0.8.1/docs/。API 以源码为准，不要用 NewNativeWindow、ui.RunOnMain、ui.SetImageLoader、AccessibilityLabel 这些不存在的名称

关键要求：
1. 纯 Go（CGO_ENABLED=0），不引入 Node/npm/WebView2；INV-05：后端包不引用 github.com/egoist/mygo。
2. 路径一律小写 trace / trace-dev（mygo.json 的 name、slug、installDirName 都小写；显示名仍是 TRACE / TRACE Dev；启动先 SetName）。
3. 安静测试（§6）：测试不出声（nullSink，不创建 audio.Player）、不建窗口（ui.NewTester）；需要真实窗口时用 TRACE_QUIET 隐藏或 ShowInactive，从不抢焦点；外部动作走假 Desktop；测试服务器只监听 127.0.0.1；冒烟隔离 TEMP/TMP；安装包测试只用 trace-ptest 且默认不跑，绝不安装或卸载 trace / trace-dev；重负载步骤用低优先级运行。
4. 按 §5 的切片顺序推进，先完成 §9 的整理；原型代码入库，结果写到 artifacts/prototypes/；原型失败按 §3 的处理办法继续，不逐包请示。
5. 不发任何真实站点请求；不碰 dist/ 下带日期的旧包、.env.local、.local/（旧缓存的 item.json 只读查看字段，夹具用合成数据）。
6. 完成后依次运行 go run ./tools/check、go run ./tools/uishots -out artifacts/uishots、go run ./tools/package、go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke。
7. 写 docs/delivery/stage-report.md（逐条引用 C01～C40 的测试函数与证据；not-run 如实记），同步更新需求、路线、状态、工程规则与 USER-ACTIONS（新包 SHA-256）；全部 amend 成一个 DCO 提交（git commit --amend -s，只 amend M1 这一个提交，父提交保持 b208ad6），不要 push。
```

---

<a id="rev3"></a>
## 12. 修订 3：首轮实机反馈返工（2026-11-08）

用户在 A 复核后试用开发包并授权 ACT-006，提出 10 条反馈（DEC-033）。A 已直接修好其中的缺陷，产品调整交给 B。仍在 M1 同一分支、同一提交上做；B 不需要发真实站点请求（ACT-007 已由 A 执行并通过）。

### 12.1 A 已完成（B 保留，不要回退）

| 编号 | 反馈 | 处理 | 回归 |
| --- | --- | --- | --- |
| F-029 | 1：视频播放花屏（竖屏） | Media Foundation 解码输出的行宽带对齐填充（如 1080 宽实际每行 1088 像素），原代码按 `MF_MT_DEFAULT_STRIDE` 或紧凑行宽读取导致画面错位；改为优先用 `IMF2DBuffer::Lock2D` 取真实行距（处理负行距），不支持时按“缓冲区长度 ÷ 行数”推算 | `TestPaddedRowsAreNotSheared`；用户缓存的竖屏视频与 1920×1080 录像逐帧导出核对 |
| F-030 | 4：偶发下载失败、重试正常 | 播放地址首选节点有时是不在白名单里的第三方 MCDN 节点，原代码把它当一次下载失败；改为跳过不受支持的节点，全部不受支持时按“不可用”刷新一次播放地址 | `TestUnsupportedMediaNodeIsSkippedNotCountedAsFailure` |
| F-031 | 3：任务页不能移除已完成任务，失败也没反馈 | 新增 `RemoveTask`、`RemoveFinishedTasks`：已完成、已取消、失败、部分完成的任务可移除（失败与部分完成同时清理临时分片，不动已登记文件），进行中的不能移除；已完成的不能再“取消”；任务行只显示当前状态可用的按钮，失败行“重试”为主按钮；操作失败在页面上显示原因；顶部“清除已结束（N）” | `TestFinishedTasksCanBeRemovedButNotCanceled` |
| F-032 | ACT-006 直播录制 0.23 秒即结束 | 播放列表被判无效而停止。按代码路径推断最可能是多码率索引（master playlist）被当成缺少初始化分片的媒体列表；改为跟随码率最高的一路（只跟一层、地址仍过白名单），列表无效时错误里写明具体原因。ACT-007 复跑通过，但那次是普通媒体列表，确切原因未复现（报告 §7） | `TestRecorderFollowsMultivariantPlaylistOnce`、`TestNestedMultivariantPlaylistStopsWithReason` |

### 12.2 B 的工作包

| 包 | 反馈 | 要求 |
| --- | --- | --- |
| R1 | 2：不要已看标记 | 去掉 `MarkSeen`、`MarkAllSeen`、`Unseen`、`updates:changed` 与界面上的未读数字、蓝色竖线、“全部标为已看”、左栏角标；`updates.db` 的 `seen_at` 不再使用（迁移可保留列，不读）。起点逻辑不变：第一次检查只建立起点，起点之前的动态不进入“更新”。主页右栏“订阅更新”改为“最近更新”（最近有新动态的订阅，按最新动态时间）。同步改 `docs/ui.md` §2、§3.9、主页右栏，`docs/design/subscriptions.md` §2，`docs/design/architecture.md`（接口表、事件、INV-12、第 129 行一带），`docs/delivery/roadmap.md` AC-17；C31 的断言改为“起点之前的不进入更新、之后的进入”，不能删掉起点与风控停止的断言 |
| R2 | 5、8：订阅页太杂乱 | 订阅页 = 较大的圆形头像网格（头像约 88～96 px，下面一行名称，过长省略），按宽度自动排列列数，虚拟化保持 C38（1000 个订阅流畅）；正在直播的头像有粉色环和“直播”小标，录制中有红点；页面顶部只有“添加订阅”和一个“更多”菜单（检查全部、从关注导入），搜索框保留。点头像进详情；详情头部只留头像、名称、“检查更新”和一个“管理”菜单（改名、备注、更换头像、删除订阅）；关联账号的增删和“规则”放进详情的“账号”“规则”标签里。每个区域最多一个主按钮 |
| R3 | 6：图标含义 | 逐个核对 `internal/ui` 用到的图标，设置用齿轮，设置分类“常规”用滑杆或齿轮，不再出现靶子形图标；每个图标在 `docs/ui.md` 列一张“图标 → 含义”表；`tools/uishots` 截图人工看一遍并在报告里写明 |
| R4 | 7：直播录制入口与清晰度 | `live.RoomState` 增加 `Quality`、`QualityName`、`Codec`（来自取流结果，`QualityName` 用 `g_qn_desc`，取不到时按 10000=原画、400=蓝光、250=超清、150=高清、80=流畅）；直播横幅里“录制”是粉色主按钮，订阅头像网格的直播标记、订阅详情“直播”标签都能一键开始/停止；录制中、任务行、录像详情都显示“原画 · AVC”这类文字。夹具测试覆盖字段传递与界面文字 |
| R5 | 9：解析页的后台状态 | 解析页不再显示“正在加载”“正在重建索引”等与解析无关的状态（解析本身的加载仍显示）；设置新增“日志”分类：上方是后台状态（索引重建、检查更新、自动缓存、录制的当前情况），下方是最近的运行日志（脱敏后最多约 500 行，等宽字体，“打开日志文件夹”“复制”）。日志读取放在服务层接口，不在界面里直接读文件 |
| R6 | 10：看图与播放按钮太多 | 看图层与播放器改为沉浸式：画面铺满深色背景；只保留少量半透明圆形悬浮按钮（看图：关闭、上一张/下一张、适应/原始大小；播放：播放/暂停、进度条、音量、全屏，弹幕/字幕/在文件夹中显示等收进一个“⋯”菜单）；鼠标静止约 2 秒后控件淡出，移动或按键时出现；原有快捷键（Esc、左右键、空格、滚轮缩放、双击）保持。C14、C22 断言随之更新，不能删掉 |
| R7 | 3（补完） | 在 A 的 F-031 基础上补 `tools/uishots` 任务页场景（进行中、失败带原因、已完成、清除已结束），失败原因用任务的 `LastError` 显示在行内；移除失败任务前提示“会删除未完成的临时文件” |

### 12.3 验收与交付

1. 每个工作包有定向测试；`go run ./tools/check` 全绿；`go run ./tools/uishots -out artifacts/uishots` 三档宽度全部断言通过，新增订阅网格、订阅详情、设置日志、沉浸式看图与播放、任务页场景。
2. 同步需求（`docs/requirements.md` 中标“返工（修订 3）”的条目完成后改回“已实现”）、界面规范、设计与路线；在[阶段报告](stage-report.md)追加 B 的修订 3 结果。
3. `go run ./tools/package`、`go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke`，更新 USER-ACTIONS H-001 的哈希与步骤（订阅网格、录制清晰度、设置日志、沉浸式看图与播放、任务移除）。
4. 全部 amend 进同一个 DCO 提交，不 push、不建 PR、不请求真实站点。

### 12.4 给 Agent B 的交接 Prompt（修订 3）

```text
请作为 Agent B 执行 TRACE 计划 TRACE-M1-03 修订 3（M1 首轮实机反馈返工）。
分支 codex/m1-mygo-bilibili，当前唯一未推送的 DCO 阶段提交（父提交 b208ad6）；先读 AGENTS.md、REVIEW.md、docs/decisions.md 的 DEC-033～DEC-036、docs/delivery/stage-plan.md §12、docs/delivery/stage-report.md §7～§9。
保留 A 已修的 F-029～F-032（§12.1），不要回退。按 §12.2 完成 R1～R7：去掉已看标记、订阅页改为圆形头像网格且配置只在详情里、图标与含义一致、直播录制入口与清晰度、解析页不显示后台状态并新增设置“日志”分类、沉浸式看图与播放、任务页补完。
约束：MyGo v0.8.1 API 以源码为准；INV-05；安静测试（§6）；不发真实站点请求；不碰 dist/ 下带日期的旧包、.env.local、.local/。不删除或弱化 C14、C22、C31、C38 的断言，只按新行为改写。
完成后跑 go run ./tools/check、go run ./tools/uishots -out artifacts/uishots、go run ./tools/package、go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke；同步需求、界面规范、设计、路线、报告和 USER-ACTIONS（H-001 哈希与步骤）；git commit --amend -s 进同一提交，不 push。交回 A 复核。
```

### 12.5 B 复核补充（2026-11-08）

用户要求继续作为 B 复核实现至可交 A。沿用修订 3 与全部基本功能范围，保留 F-001～F-032；补齐日志脱敏与有界异步读取、真实诊断状态、全屏窗口回调、失效登录与迟到图片结果的媒体关闭、播放偏好与音量/弹幕密度、可见订阅账号直播刷新、旧首次检查快照的可逆迁移，以及订阅索引读取失败的反馈。稳定 F 编号、实际回归、被测基点与新包以[报告 §9](stage-report.md#b-recheck)为准。A 已集中复核 R1～R7 与 F-033～F-041 并通过，另直接修复 F-042（[报告 §10](stage-report.md#a-review-rev3)）；修订 3 没有待 B 执行的工作包，后续返工见 [§13](#rev4)。

人读阶段标签与编号大写、机器标识中的阶段部分小写；阶段命令参数按大小写兼容/小写规范化约定实现。阶段编号不与 PR 编号绑定（DEC-035、DEC-036）；本轮只同步约定，不建立计划外的验证命令。

<a id="rev4"></a>
## 13. 修订 4：第二轮实机反馈返工（2026-11-08）

用户在 A 复核修订 3 后再次试用，提出 7 条反馈：1 是缺陷（播放卡顿、卡死），由 A 直接修复为 F-043；2～7 是产品调整（DEC-037），交给 B。仍在 M1 同一分支、同一提交上做；B 不需要真实站点请求。总原则：界面整洁、少按钮，同类条目“点本体完成常用动作，其余收进一个‘⋯’”。

### 13.1 A 已完成（B 保留，不要回退）

| 编号 | 反馈 | 处理 | 回归 |
| --- | --- | --- | --- |
| F-043 | 1：播放非常卡顿、甚至卡死 | 视频帧原来在界面线程的 `View` 里同步解码；解码器用旧的软件色彩转换、没有 GPU；两个 Source Reader 都选中全部流；追帧时每个跳过的帧也做完整的色彩转换。现在：`internal/player/video_worker.go` 在单独的 goroutine 里按音频时钟解码，界面线程只上传最新的一帧（三缓冲，按 seek 代次丢弃过期帧，暂停时 seek 后请求重绘）；`mf_hw_windows.go` 用 D3D11 设备管理器、硬件解码和视频处理器在 GPU 上转成 RGBA 字节序、全范围（0～255）并缩到长边不超过 1920；视频与音频读取器只选各自的流；追帧时跳过的帧不做转换。任一步失败按“GPU → 软件缩放 → 旧软件路径”回退 | `TestAdvanceNeverDecodesOnTheUIThread`、`TestStaleFramesAreDroppedAndShownBufferIsNotReused`、`TestDecodePathsAgreeOnPixels`、`TestDecodeSizeFitsWindowAndKeepsAspect`；用户缓存的 3 个 60 fps 文件（含 2160×3840）实测见[报告 §11](stage-report.md#feedback-2) |

### 13.2 B 的工作包

| 包 | 反馈 | 要求 |
| --- | --- | --- |
| R8 | 2、7：主页按钮太多 | 主页帖子（`PostCard` 在主页的用法）不放任何文字按钮：去掉“内置播放”、补存弹幕字幕、“浏览器打开”“复制链接”“查看封面”等。点视频画面在播放层播放，点图片打开看图层，点头像或昵称进入用户页；状态圆点保留。补全、升级等缓存维护只在视频详情页（从媒体库“⋯ → 详情”进入）。订阅详情、用户页等同样使用 `PostCard` 的地方：未缓存时只保留“缓存”一个按钮，“在浏览器中打开”“复制链接”收进卡片右上角一个“⋯” |
| R9 | 3：有更新的头像 | 去掉右栏“最近更新 / 最近看过的用户”卡片（右栏只留快速解析、缓存卡、正在直播）。中栏“主页”标题下面加一行横向头像（圆形约 56 px，下方一行名称，过长省略，可横向滚动；没有时整行不显示）：列出在“最后查看时间戳”之后有新缓存内容的订阅（不属于订阅的作者按 UP 主单独列），按最新内容时间从近到远。点头像进入只含这个订阅（全部关联账号）的信息流视图，标题栏显示头像、名称和返回箭头，从新到旧列出；返回后回到主时间线原来的位置。口径按 [Q-009](../../USER-ACTIONS.md#q-009) 默认值：打开某个头像即把这个订阅的“已看到”时间记为其最新内容的时间，头像从行里消失，直到有更新的内容；不按单条记已读。数据与锚点都在本机，0 网络请求 |
| R10 | 4：时间线 | 去掉“最新在前 / 最早在前”切换，主页始终新内容在上。锚点记“最后查看的时间戳”（当前看到的条目的发布时间与键），下次打开从那里继续，往上滑或点“↑ N 条更新的内容”看更新的；底部到头显示“已经到最早的缓存了”。旧 `ui-state.json` 里 `order=oldest` 读取时规范为 `newest` 并保留锚点条目。C24 改写为：没有顺序切换控件、锚点恢复、双向加载无重复无遗漏、0 网络请求；不能删掉锚点与双向加载的断言 |
| R11 | 5：解析页 | 解析成功后把封面与头像的缩略图缓存到数据目录（如 `%LOCALAPPDATA%\trace[-dev]\thumbs\`，不进媒体库），最近解析条目引用本机文件；下载不带 Cookie、不带外站 Referer，地址过白名单，大小有上限，失败时显示占位图而不是空白；删除单条或全部清空时一并删掉对应缩略图；文件名不含签名参数。条目点本体打开；“在浏览器中打开”“删除”收进条目右侧一个“⋯”；“全部清空”放进列表标题右侧的“⋯”（确认后执行）。同步数据设计与 C03（缩略图无 Cookie/签名、删除联动） |
| R12 | 6：媒体库 | 网格条目只显示缩略图、标题、一行元信息和右下角一个“⋯”：菜单含“详情”（所属动态或视频详情）、“在资源管理器中显示”、“删除”（沿用确认弹窗与 FR-LIB-003 规则）。去掉“详情 / 文件夹 / 删除 / 内置播放”等文字按钮，点缩略图播放或看图。顶部“重建索引”移到设置“存储”。C25、C26 断言保留并改为经菜单操作 |
| R13 | 7：整洁 | 把“同类条目点本体完成常用动作、其余收进一个‘⋯’；每个区域最多一个主按钮”写进 `docs/ui.md`，按它检查 R8～R12 涉及的页面和右栏，`tools/uishots` 截图逐页目视并在报告里写明；不顺带改动其他页面的功能 |

### 13.3 验收与交付

1. 每个工作包有定向测试；`go run ./tools/check` 全绿；`go run ./tools/uishots -out artifacts/uishots` 三档宽度全部断言通过，新增或更新主页（头像行、单个订阅视图）、解析页（带缩略图的最近解析）、媒体库（“⋯”菜单打开）场景。
2. 同步需求（FR-FEED-001、FR-HIST-001、FR-LIB-001、FR-LIB-003）、界面规范、数据设计与路线；在[阶段报告](stage-report.md)追加 B 的修订 4 结果。
3. `go run ./tools/package`、`go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke`，更新 USER-ACTIONS H-001 的哈希与步骤。
4. 全部 amend 进同一个 DCO 提交，不 push、不建 PR、不请求真实站点。

### 13.4 给 Agent B 的交接 Prompt（修订 4）

```text
请作为 Agent B 执行 TRACE 计划 TRACE-M1-03 修订 4（M1 第二轮实机反馈返工）。
分支 codex/m1-mygo-bilibili，当前唯一未推送的 DCO 阶段提交（父提交 b208ad6，基点见交付消息）；先读 AGENTS.md、REVIEW.md、USER-ACTIONS.md（Q-009）、docs/decisions.md 的 DEC-033～DEC-037、docs/delivery/stage-plan.md §13、docs/delivery/stage-report.md §10～§11，并查只读本机参考索引。
保留 F-001～F-043，尤其 A 刚修的 F-043（internal/player 的解码线程、GPU 路径与回退，不要把解码搬回界面线程）。按 §13.2 完成 R8～R13：主页帖子去按钮、主页标题下的有更新头像行与单个订阅视图（Q-009 默认口径）、去掉顺序切换并按最后查看时间戳续看、解析页缓存缩略图并收起文字按钮、媒体库条目右下角“⋯”菜单、整洁原则写进界面规范。
约束：MyGo v0.8.1 API 以源码为准；INV-05；安静测试（§6）；不发真实站点请求；不碰 dist/ 下带日期的旧包、.env.local、.local/ 和用户的缓存文件。C03、C24、C25、C26、C38 只按新行为改写，不能删除或弱化锚点、双向加载、删除规则和虚拟列表断言。
完成后跑 go run ./tools/check、go run ./tools/uishots -out artifacts/uishots、go run ./tools/package、go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke；同步需求、界面规范、数据设计、路线、报告和 USER-ACTIONS（H-001 哈希与步骤）；git commit --amend -s 进同一提交，不 push。交回 A 复核。
```

<a id="rev4-b-plan-recheck"></a>
### 13.5 B 补充复核与交回 A（2026-11-08）

用户再次要求仅作为 B 复核、修复到可交 A；继续修订 4，不开始新阶段。本轮基点为 `818a163`，分支和父提交不变。复核 R8～R13 的数据与界面调用链，并处理报告 §11 已确认的打包失败恢复缺口：缩略图引用/提交/清理、主页查询和位置/已看状态失败、同时间戳分页、统计读取、逐文件媒体动作、本机详情类型与完整元数据边界、主页零图片请求、包替换和版本刷新失败恢复。稳定问题编号为 **F-044～F-055**，实现、定向回归、完整验证和审查 prompt 以[报告 §13](stage-report.md#rev4-b-recheck)为正本。

整理仍限于本阶段：当前截图增加本机视频、动态、录像详情，保留 21 个场景的最新三档产物；替换 `artifacts/review/`、`artifacts/smoke/` 和 `dist/trace-dev/` 的旧生成物；历史报告保留但注明当前证据正本，工程规则移除旧的场景/测试包计数。保留旧日期包、`.local/`、`.env.local`、用户媒体和真实站点回执；未引入依赖或一次性仓库脚本。

下一接收方为 Agent A，按报告的交接 prompt 集中复核 R8～R13 与 F-044～F-055。F-001～F-043、C03/C24/C25/C26/C38、MyGo v0.8.1 与 INV-05 必须保留；B 未改 `internal/player/`。所有验证使用合成/loopback、静音输出与隐藏窗口，不请求真实站点，不安装、不 push、不建 PR；H-001 仍 not-run。

<a id="rev4-a-review"></a>
### 13.6 A 集中复核与第三轮反馈（2026-11-08）

A 按 13.4 的交接复核了 R8～R13 与 F-044～F-055，并按用户第三轮反馈（DEC-038）直接修复 F-056～F-058、改设置侧边栏、做浮动侧栏预览（Q-010），同时按用户要求清理了无用函数和临时文件。问题都是局部的，没有新的 B 工作包；结论、范围与验证见[阶段报告 §14](stage-report.md#a-review-rev4)。下一步是用户的 H-001 实机体验。

<a id="feedback-4-plan"></a>
### 13.7 第四轮反馈（2026-11-08）

用户第四轮反馈（拖动进度条卡顿、媒体库状态圆点、Cindy 字体与主题、三张布局参考、强调色不用粉色）由 A 直接处理：F-059 修复与 DEC-039 改版，没有新的 B 工作包；结论与验证见[阶段报告 §15](stage-report.md#feedback-4)。下一步仍是用户的 H-001 实机体验，并回复 Q-011。

<a id="feedback-5-plan"></a>
### 13.8 第五轮反馈（2026-11-09）

用户第五轮反馈（共用缓存规则、订阅详情精简、设置页嵌套框、主页掉帧与未读恢复、浮动看图与播放、帖子图片与视频排版、实况照片）由 A 直接处理：DEC-040 与 F-060、F-061，没有新的 B 工作包；结论与验证见[阶段报告 §16](stage-report.md#feedback-5)。下一步仍是用户的 H-001 实机体验（重点第 7、15、18 步），并回复 Q-011。
