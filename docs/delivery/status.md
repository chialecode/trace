# 交付状态

更新：2026-11-09。本页只写已实现的能力、当前阶段、已知限制、LOOP 和开发与上游事项，不按轮次追加。

## 当前阶段

**M2（安装与更新）：版本号、开发版本机自动更新、正式版联网更新、一条命令发布与每周发布流程、中文安装界面与图标**（[当前计划 TRACE-M2-01](stage-plan.md)，DEC-041、DEC-042）。

- **状态**：B 已于 2026-11-09 完成实施与自测，A 同日审查完毕并修复两处局部问题（[阶段报告 §8](stage-report.md#review)）；经 PR #2 squash 合并为 `main @ c17bd87`。2026-11-12 按 ACT-009 打开每周自动发布，第一个正式版 [v0.1.1](https://github.com/chialecode/trace/releases/tag/v0.1.1) 经 `release.yml`（[运行 43138195253](https://github.com/chialecode/trace/actions/runs/43138195253)）发布，清单的版本证明签名已用内置公钥核对。细节按 [Q-012](../../USER-ACTIONS.md#q-012) 的默认值；仓库的发布环境与密钥备份见 [ACT-008](../../USER-ACTIONS.md#act-008)，每周自动发布见 [ACT-009](../../USER-ACTIONS.md#act-009)。
- **Git 与分支**：M1 已于 2026-11-09 经 PR #1 squash 合并为 `main @ 29c2678`（[M1 计划](https://github.com/chialecode/trace/blob/29c2678/docs/delivery/stage-plan.md)、[M1 报告](https://github.com/chialecode/trace/blob/29c2678/docs/delivery/stage-report.md)）。M2 经 [PR #2](https://github.com/chialecode/trace/pull/2) 合并为 `main @ c17bd87`。
- **真实站点与实机**：M2 不请求哔哩哔哩，不访问 GitHub 发布；更新只用本机回环地址上的假发布源演练过。实机体验 [H-001](../../USER-ACTIONS.md#h-001) 与安装更新检查 [H-002](../../USER-ACTIONS.md#h-002) 为 `not-run`，产品验收未开始。
- **发布环境（ACT-008，2026-11-09 完成仓库设置）**：`release` 环境只允许 `main` 部署；环境 secret `MYGO_UPDATER_PRIVATE_KEY` 从本机密钥文件直接读入（已核对与 `mygo.json` 的公钥配对）；Actions 允许列表按完整 SHA 加入 `actions/upload-artifact` v7.0.2、`actions/download-artifact` v8.0.2（`actions/checkout@*` 原已允许）。
- **第一次手动只构建运行**（[42607654678](https://github.com/chialecode/trace/actions/runs/42607654678)）：`check` 通过；`build` 在 `tools/release` 自己的 `tools/check` 一步失败，没有签名、没有上传。原因：Windows 运行器的 `%TEMP%` 里用户目录是 8.3 短名 `RUNNER~1`，`filepath.EvalSymlinks` 会展开短路径，`internal/library` 的 `rejectDirectoryLinks` 把这种差异当成目录联接，用临时目录的 19 个测试失败（M1 起就有；`ci.yml` 只在 Ubuntu 上跑，没有暴露）。修复经 [PR #3](https://github.com/chialecode/trace/pull/3) 合并为 `main @ 6c27851`：比较前先用 `GetLongPathName` 展开短路径（不跟随联接），由 `TestShortPathNamesAreNotLinksButJunctionsAre` 覆盖。
- **第二次手动只构建运行**（[42609435239](https://github.com/chialecode/trace/actions/runs/42609435239)，`main @ 6c27851`）：`check` 1 分 23 秒、`build` 5 分 15 秒通过，`publish` 按设计跳过。产物 `release-0.1.1`：`SHA256SUMS` 的 6 个文件全部核对通过；`VERSION.txt` 为 `0.1.1`、`stable`、`6c278512cd2a`；`release.json` 为 `offline: false`、`allowDirty: false`、`rehearsal: passed`、`published: false`；运行器上的 ptest 演练 10 步全部通过；清单用内置公钥经 `update.Source.Verify` 核对通过。正式版仍未发布：发布要 [ACT-009](../../USER-ACTIONS.md#act-009) 授权。

## 已实现能力

已在 M1 代码与对应自动化输入中实现并验证；A 技术审查结论见 [M1 报告](https://github.com/chialecode/trace/blob/29c2678/docs/delivery/stage-report.md)，用户 H-001 实机体验未执行：

- **按用户安装与数据目录（FR-STORE-001）**：`internal/appdirs` 严格按通道分离路径（`%LOCALAPPDATA%\Programs\trace[-dev]`、`%LOCALAPPDATA%\trace[-dev]`、`文档\trace[-dev]`），单实例锁与文件全用小写，不写 `%APPDATA%` 与 `~\.trace`。
- **凭证管理与自动续期（FR-AUTH-001～003）**：Windows DPAPI 加密 `credentials/bilibili.bin`，扫码/手动登录/续期串行、候选 nav 校验后保存；普通写失败保留旧凭证，已确认失效即使标记写失败也停止使用；扫码凭证支持 CorrespondPath RSA-OAEP 续期与待确认恢复，时钟/传输可注入。
- **哔哩哔哩协议、解析与最高画质缓存（FR-LINK-001～002、FR-HIST-001、FR-CACHE-001～009、DEC-037）**：AV/BV/b23.tv/动态/空间/直播链接解析，解析页只显示解析相关状态，最近解析缓存本机封面与头像缩略图（`<数据目录>/thumbs/`，无 Cookie/Referer、不含签名查询串；有界完整解码/缩放、内容哈希命名、按准确引用清理，提交与清理失败可恢复）、点条目直接打开、操作收进“⋯”菜单；WBI 签名与 412/-352/-412 熔断，下载跳过非白名单 MCDN 节点；默认最高画质与音质、多 P 全部缓存、分片断点续传、mp4ff 1s 交错合并（含 `elst`；大文件 `co64` 门控未执行）、最高音轨另存并为主 MP4 选择可播放音频、画质升级探测与替换、逐文件进度（FR-CACHE-009）、已结束任务移除与失败任务移除确认。
- **弹幕与字幕（FR-CACHE-008、FR-LIB-004）**：protobuf 分段弹幕解码与去重合并 XML、B 站 JSON 字幕转 `.vtt`/`.srt`、纯 Go 弹幕排版器与字幕时间轴同步渲染。
- **订阅、检查更新、自动缓存与关注导入（FR-SUBS-001～004、FR-SUB-001～002、DEC-033、DEC-037、DEC-040）**：订阅页为圆形头像网格（含直播粉环与一键录制小标，1000 个订阅虚拟化滚动），可见账号按 60 秒间隔查询直播；详情含头像/名称/缓存规则按钮/“⋯”菜单（含检查更新）与“直播”“账号”两个标签；缓存规则在设置里统一管理、多个订阅共用，旧订阅按原设置迁移（DEC-040）；1 账号归属 1 订阅（迁移确认、删除不删缓存不取关）；首次检查只建立起点、起点之前的动态不进入更新列表、不做已看标记与未读角标，旧库首次快照通过可逆标志排除；读取失败保留列表并显示原因；定时检查（单轮 ≤ 30 账号、412/-352 指数退避）、按共用规则自动缓存与关注列表分页导入。
- **直播状态与录制（FR-LIVE-001～002、DEC-033）**：开播轮询、真实场次/同场段号、HLS fMP4 原画流优先录制（支持跟随一层多码率 master playlist），传递并展示清晰度与编码（如“原画 · AVC”），用户页直播横幅、订阅头像网格与详情“直播”标签支持一键开始/停止录制；完整分片 fsync checkpoint 与登记恢复；mp4ff 磁盘转换普通 MP4，默认小样本校验时长误差 ≤ 0.1 秒（大文件 `co64` 门控未执行）并入库。
- **主页信息流、媒体库、沉浸式看图/播放器、日志与托盘（FR-FEED-001、FR-IMG-001、FR-LIB-001～006、FR-SET-001、FR-RUN-001、DEC-033、DEC-037）**：`mygo/ui` 图标栏 + 上下文侧栏 + 页面（1280/1000/720 三档，DEC-039；快速解析在页面顶部栏，正在直播在主页侧栏），主页固定从新到旧排列、按实际时间戳/键双向分页，每次打开从最新处开始、不恢复位置不标未读（DEC-040）、有更新头像行与单订阅视图往返、主页帖子底部零文字按钮、图片按原比例与视频封面在左（DEC-040）；帖子显示数据按条目缓存、本机图片后台解码缩放；读取/提交失败保留原列表与状态，分页失败由用户显式重试，重绘不查询全库统计，主页/本机详情不自动补抓远程图片。独立逐文件媒体库网格（16:9 卡片，每格仅缩略图（无状态圆点）、最多两行标题、一行元信息和“⋯”菜单，“重建索引”移入设置“存储”）与订阅成员分页；缩略图/标题按登记文件打开，视频/动态/录像本机详情保留真实类型与已知缺失，不虚构互动统计，缓存操作获取完整站点元数据。Media Foundation（含行距对齐填充处理；GPU 解码与缩放、解码不在界面线程；拖动只预览、松手跳一次、声音等新画面）+ `mygo/audio` 本地播放；浮在应用上方的深色看图/播放面板（视频自动播放，DEC-040）、音量/静音、弹幕显示区域与密度和原生全屏，偏好保存与恢复；静止约 2 秒隐藏控件，任意按键唤醒，登录失效关闭媒体，迟到图片结果不覆盖交互；设置九个分类（含“缓存规则”），内容不套嵌套卡片，“日志”在后台有界读取并显示真实诊断及读错；关闭到托盘销毁窗口、单实例唤醒恢复，最新冒烟的托盘专用工作集 `0.59 MiB`、无 WebView2 子进程（数值口径及限制见报告）。

M2 实现、自动化测试与 ptest 演练通过，实机未验收（H-002）：

- **版本号（FR-UPD-001）**：正式版 `0.1.x`（还没有发布时从 `0.1.1` 开始），开发版 `<下一个正式版>-dev.<UTC 时间>`；版本、通道、提交与构建时间注入程序，“设置 › 关于”、诊断、日志、文件属性、安装包名、卸载项与 `VERSION.txt` 一致（[工程规则 §2.1](../dev-rules/engineering.md#versions)）。
- **开发版本机自动更新（FR-UPD-002）**：`go run ./tools/package` 打包后，本机装了 TRACE Dev 就请它 `--quit-for-update`（暂停任务、收尾录制、保存状态），最多等 30 秒、不强杀，静默安装、核对版本、按原窗口状态重开；结果写 `artifacts/package/install-dev.json`（[工程规则 §2.2](../dev-rules/engineering.md#dev-install)）。
- **正式版联网更新（FR-UPD-003）**：只在正式版启用；启动 1 分钟后与每 24 小时检查，手动检查，自动下载（可关），顶部“新版本已就绪”提示、重启前的确认、更新后第一次启动的说明，“设置 › 关于”的两个开关（[架构 §8.1](../design/architecture.md#updates)）。
- **更新的安全与数据保护（FR-UPD-004）**：Ed25519 签名之外，TRACE 补上 HTTPS（含重定向）、清单完整性与大小、版本证明（防止把旧更新包标成新版本）、只下载清单里的文件并边读边核对；数据文件有结构版本，迁移前留 `.premigrate-<旧版本>`，旧程序遇到新结构拒绝启动（[数据 §1](../design/data-and-cache.md#schema-versions)）。
- **发布（AC-31、AC-33）**：`go run ./tools/release` 检查、签名构建、ptest 升级演练后放进 `dist/trace/`，只有授权后才发布；`.github/workflows/release.yml` 每周检查、构建、演练，发布受仓库变量控制（[工程规则 §2.3、§4.2](../dev-rules/engineering.md#release)）。
- **安装体验（FR-STORE-001）**：两个通道各自的图标、中文安装界面、发布者 `chialecode` 与版权，安装包名带版本。

已知限制：没有可用 GPU 的电脑走软件解码，界面不会卡死，但高分辨率 60 fps 视频可能掉帧；本机没有 cgo 工具链，`go test -race` 无法运行（记为 `not-run`）；直播录制 MVP 只支持 HLS fMP4 流（仅提供 FLV/TS 的直播间见 LOOP-004）。

<a id="loop"></a>
## 后续 LOOP（Agent 侧）

| 编号 | 内容 | 影响与后继 |
| --- | --- | --- |
| LOOP-001 | 用 `refresh_token` 刷新 Cookie | 已在 M1 实现（FR-AUTH-003） |
| LOOP-002 | 在线播放（不缓存直接播放：Referer 代理、流式解码） | M1 已实现 FR-LIB-002 的本机播放，FR-PLAY-001 在线播放仍在后续范围 |
| LOOP-003 | 任务详情显示每个文件的进度 | 已在 M1 实现（FR-CACHE-009） |
| LOOP-004 | 直播录制的 HTTP-FLV 后备（FLV 标签解析、时间戳修正、转 MP4） | MVP 只录 HLS fMP4；直播间没有 fMP4 流时录制失败并说明原因。ACT-005 真实直播间已验证提供 `http_hls`/`fMP4` 原画流；后续若遇到仅提供 FLV 的直播间再实现（[直播设计 §2](../design/live.md#recorder)） |
| LOOP-006 | 更新的深化：差分更新的体积、更新说明的排版、Windows 代码签名、winget / Microsoft Store、测试版通道、更新失败时的回退帮助 | M2 只做完整流程（[计划 §7](stage-plan.md)）；按反馈再排 |
| LOOP-007 | 代码注释里还留着 M1 的旧编号（如“R8”“F-043”“C22”） | M2 只清理了文档与测试名；改到相应文件时顺手改成直接描述 |
| LOOP-005 | 远程服务器（VPS）无头录制模式与轻量 Web 配置界面 | M1 通过 `INV-05` 与 `service.Backend` 接口保持后端与 `mygo/ui` 解耦（[架构设计 §9](../design/architecture.md#headless-ext)）；未来需要时直接为 `internal/service` 增加轻量 `net/http` JSON/Web 控制台或 `mygo/ui` WASM 前端 |

<a id="issues"></a>
## 开发与上游事项

| 编号 | 内容 | 状态 |
| --- | --- | --- |
| OSS-001 | MyGo 持续高频迭代（M2 升到 `v0.8.3`） | 按 DEC-032 不保留旧版兼容包袱，持续同步 MyGo 最新版；后端核心包不依赖 MyGo（INV-05），只有 `main.go`、`internal/shell`、`internal/ui`、`internal/player`、`internal/locales`、`tools/uishots` 引用 MyGo。MyGo 的更新签名只覆盖更新包内容、不含版本号，TRACE 在清单里加了版本证明；MyGo 的更新不改卸载项的版本，TRACE 在更新后第一次启动时改；MyGo 自己的“应用正在运行”安装提示仍是英文 |
| OSS-002 | Windows Media Foundation（`IMFSourceReader`）纯 Go COM 调用与 `mygo/audio` 音画同步 | 合成 AVC/AAC 读取、Seek 与静音测试通过；真实音频设备体验留 H-001 |
| OSS-003 | `mp4ff` v0.62.0 渐进式 MP4（1 秒交错 + `elst`）、磁盘合并及最高音轨后备 | 小样本合并/轨道/时长与失败保留旧文件回归通过；最高 FLAC/杜比另存，主 MP4 用 AAC；真实 FLAC-in-MP4 解码未验证（[媒体选型](../research/2026-11-05-media.md#hires-audio)） |
| OSS-004 | 纯 Go 弹幕排版器与 WebVTT 字幕在 `mygo/ui` 中的叠加性能 | 测试验证：10,000 条弹幕在时间窗二分查找 + 轨道防重叠下单帧耗时远低于 `1 ms` 目标，Seek 无残留 |
| OSS-005 | 直播录像用 `mp4ff` `Defragment` 把 fMP4 转普通 MP4（含 > 2 GiB `co64`、多 `moof`/`trun` 与负合成时间偏移） | 默认小样本转换/登记恢复通过；大于 2 GiB 与稀疏 `co64` 门控未执行，不能记为已验证 |
| OSS-006 | `mygo/ui` 窗口关闭到托盘销毁窗口、释放纹理并在同一进程内反复重建 | 托盘测试与最新 `tools/smoke` 通过；真实隐藏窗口、单实例与隔离数据锁/路径检查通过；内存数值见报告及 `artifacts/smoke/smoke.json` |
| OSS-007 | MyGo 调用 NSIS（`v3.13`）生成按用户安装包（`%LOCALAPPDATA%\Programs\trace[-dev]`） | 开发版、正式版与 ptest 安装包都已生成；ptest 的静默安装、更新与卸载演练通过；安装界面的实机外观与 SmartScreen 体验在 H-002，not-run。安装包本身没有文件属性里的版本信息（MyGo 的 NSIS 脚本不写），程序与卸载项有 |
| OSS-008 | 二维码传递模块归档遗漏 LICENSE | 已核对官方仅增加 MIT LICENSE 的下一提交，将许可原文作为精确版本补充嵌入通知，其他缺许可模块停止打包（[来源与移除条件](../../tools/internal/build/licenses/README.md)） |
