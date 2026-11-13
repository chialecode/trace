# 交付状态

更新：2026-11-14。本页只写已实现的能力、当前阶段、已知限制、LOOP 和开发与上游事项，不按轮次追加。

## 当前阶段

**M3（抖音视频与直播）**（[当前计划 TRACE-M3-01](stage-plan.md)，DEC-043、DEC-044）：分支 `codex/m3-douyin`，基点 `main @ 15e229c`。

- **状态**：B 已完成阶段整理（C0）、全部工作包（W1～W10）与必测用例（M3-K01～K19）。A 集中审查已收敛（[阶段报告 §9](stage-report.md#a-review)）：A 修了 F-001（ACT-010 回执是补写的，文档改为如实说明）、F-002（抖音非机密 Cookie 值把日志里的 `false` 脱敏掉）、F-003（真实检查程序把 CDN 的 302 当成验证页）和 F-004（用户授权的 ACT-012 确认抖音直播 FLV 的 CDN 跳转被生产录制器拒绝，A 已改跳转规则，ACT-014 真实录制通过）；修复后 `go run ./tools/check`、Linux `go vet`、`tools/uishots`（33 个场景、99 张 PNG）、`tools/package`（`0.1.2-dev.202611130248`，已自动更新本机 TRACE Dev）和 `tools/smoke` 全部通过。用户已于 2026-11-14 授权 [ACT-013](../../USER-ACTIONS.md#act-013)（push、PR、squash 合并与清理分支）。实机体验 [H-001](../../USER-ACTIONS.md#h-001) 与安装更新检查 [H-002](../../USER-ACTIONS.md#h-002) 仍为 `not-run`。
- **第六轮实机反馈（DEC-046，[当前计划 §12](stage-plan.md#feedback-6)）**：A 已于 2026-11-14 集中审查并收敛（[阶段报告 §10.5](stage-report.md#f6-review)：修了自定义代理接受账号密码 F-005、“未缓存”筛选同长度重载 F-006，以及两个测试在 Linux CI 上跑播放的问题）。B 已完成 F6-1～F6-8（[阶段报告 §10](stage-report.md#feedback-6)）：哔哩哔哩实况在看图层原地播放（[ACT-019](../../USER-ACTIONS.md#act-019) 1 次查明解析、缓存与片段格式都正常，问题在看图层）、无点图标、通透浅灰配色、52px 图标栏与沉浸式标题栏、“设置 › 网络”代理、用户页“缓存更早的内容”与“全部 / 未缓存”筛选、主页不画圆点；门禁、`tools/uishots`（36 个场景、108 张 PNG）与 `tools/package`（`0.1.2-dev.202611131736`，未安装到本机）通过。实机检查记为 H-001 第 27 步（not-run）。
- **真实站点**：2026-11-13 B 按 [ACT-010](../../USER-ACTIONS.md#act-010) 运行 `go run ./tools/actcheck -act act010` 1 次（账本 `runs=1/1`、API 与播放列表 `15/18`、点播与图片 CDN `6/16` 共 `10,602,566` 字节、没有触发任何停止条件；`artifacts/site-check/act010.json` 是程序写盘被拦后补写的，不是程序回执，见 F-001）：登录对照（V-01）、纯 Go 签名与网关通过（V-02）、置顶作品翻页（V-03）、普通与混排图文解析校验（V-04）、无 Cookie 点播分段下载（V-05）、在线 `reflow/info`（V-06 部分）及同一直播间双号码核对（V-08）均验证通过并回写[抖音协议 §11](../research/douyin.md#a-review)。A 审查时运行 [ACT-011](../../USER-ACTIONS.md#act-011) 1 次（API `3/4`，回执由程序写出）：离线 `room_id=2` 的 `reflow/info` 返回 `status_code=10011`，拿不到 `web_rid`（V-06 已回答）；直播间在播，FLV 第一跳 `302` 后检查程序误停。用户授权后 A 运行 [ACT-012](../../USER-ACTIONS.md#act-012) 1 次（API `3/3`、FLV `2/4` 次 `516` 字节、无停止条件）：两次 FLV 都 `302` 到白名单外的 HTTPS 443 域名，生产录制器拒绝（F-004，已修）；修复后 A 运行 [ACT-014](../../USER-ACTIONS.md#act-014) 1 次（API `2/3`、FLV `2/4` 次 `751,453` 字节、无停止条件）一次通过：跟随 `302` 后连上，选中“原画”，FLV 视频标签是 AVC（`CodecID=7`，不是 `12`/`hvc1`），`1280×720`，音视频 `avc1`+`mp4a`；录了 `21.56` 秒、`15` 个分片、`1` 个分段，收尾为 `735,389` 字节的 MP4，`moov` 在 `mdat` 之前，状态 `completed`（V-07 已回答；没见到 HEVC 流）。
- **实现来源**：按 [DEC-044](../decisions.md#dec-044)，参考代码不直接翻译——签名协议与测试向量写于[抖音协议附录](../research/douyin.md#sign-spec)，HTTP-FLV 解复用重封装与单文件 MP4 `moov` 前置方案写于[直播设计 §2.1](../design/live.md#flv)、[数据设计 §2.6](../design/data-and-cache.md#merge)，均独立实现且零新增第三方依赖。

## 上一阶段

**M2（安装与更新）**：经 PR #2 合并为 `main @ c17bd87`（[M2 计划](https://github.com/chialecode/trace/blob/15e229c/docs/delivery/stage-plan.md)、[M2 报告](https://github.com/chialecode/trace/blob/15e229c/docs/delivery/stage-report.md)），之后 PR #3（Windows 运行器的 8.3 短路径不再被当成目录联接，`main @ 6c27851`）、PR #4～#6 记录发布。2026-11-12 按 ACT-009 打开每周自动发布，第一个正式版 [v0.1.1](https://github.com/chialecode/trace/releases/tag/v0.1.1) 经 `release.yml`（[运行 43138195253](https://github.com/chialecode/trace/actions/runs/43138195253)）发布，清单的版本证明签名已用内置公钥核对。发布环境与密钥备份见 [ACT-008](../../USER-ACTIONS.md#act-008)。实机体验 [H-001](../../USER-ACTIONS.md#h-001) 与安装更新检查 [H-002](../../USER-ACTIONS.md#h-002) 仍为 `not-run`，产品验收未开始。

## 已实现能力

代码与对应自动化输入已实现并验证（M1 见 [M1 报告](https://github.com/chialecode/trace/blob/29c2678/docs/delivery/stage-report.md)，M2 见 [M2 报告](https://github.com/chialecode/trace/blob/15e229c/docs/delivery/stage-report.md)，M3 见[阶段报告](stage-report.md)），用户 H-001 实机体验未执行：

- **按用户安装与数据目录（FR-STORE-001）**：`internal/appdirs` 严格按通道分离路径（`%LOCALAPPDATA%\Programs\trace[-dev]`、`%LOCALAPPDATA%\trace[-dev]`、`文档\trace[-dev]`），单实例锁与文件全用小写，不写 `%APPDATA%` 与 `~\.trace`。
- **多站点凭证管理与自动续期（FR-AUTH-001～005、FR-AUTH-007、FR-DY-001、NFR-SEC-001～003）**：`credential.Manager` 按站点分文件、分熵做 Windows DPAPI 加密（`credentials/bilibili.bin` 完全兼容旧版密文，`credentials/douyin.bin` 使用独立熵 `trace:douyin:credential:v1`）；抖音 Cookie 清洗剥离 `bd_ticket_guard_*` 等动态票据并经 `profile/self` 校验后保存；登录、失效与退出按站点独立（任一站点已登录即可浏览本机内容，未登录站点在发请求前拒绝，单站点失效只暂停本站点的任务与录制）；`RedactString` 同时覆盖两站点凭证；哔哩哔哩扫码凭证支持 CorrespondPath RSA-OAEP 自动续期与待确认恢复。
- **多站点数据结构版本 2 与迁移备份（NFR-DATA-004）**：`library.db`、`updates.db`、`subscriptions.json`、`ui-state.json` 升至结构版本 2，迁移前生成 `.premigrate-1`（备份失败不迁移），SQLite 单事务与 JSON 原子写保证半途失败可回滚且下次启动可续迁；旧版本程序遇到 v2 数据拒绝写入。
- **哔哩哔哩协议、解析与最高画质缓存（FR-LINK-001～002、FR-HIST-001、FR-CACHE-001～009、DEC-037）**：AV/BV/b23.tv/动态/空间/直播链接解析，最近解析缓存本机封面与头像缩略图（`<数据目录>/thumbs/`，按 `<site>:<kind>:<id>` 归属与准确引用清理）；WBI 签名与 412/-352/-412 熔断，下载跳过非白名单 MCDN 节点；默认最高画质与音质、多 P 全部缓存、分片断点续传、mp4ff 1s 交错合并（含 `elst`；大文件 `co64` 门控未执行）、最高音轨另存并为主 MP4 选择可播放音频、画质升级探测与替换、逐文件进度（FR-CACHE-009）。
- **抖音协议、浏览与无水印缓存（FR-DY-002～006、NFR-NET-003）**：纯 Go 标准库签名子包 `internal/site/douyin/sign`（SM3、`dhzx` `a_bogus`、`uifid` 伴随 `x-secsdk-web-signature`，不联网、不读凭证）；分享文本与 `v.douyin.com` 短链（≤ 5 跳白名单）、作品/用户/直播间双号码链接解析；用户页 3:4 作品网格（置顶标识、`max_cursor` 翻页、防访客空壳误判）与作品详情（视频、普通图文、图片/实况/视频页混排图文及配乐条）；无水印最高档选流、单文件 MP4 容器校验与有界内存磁盘流式 `moov` 前置重排、图文逐页与配乐缓存，媒体 CDN 一律不带 Cookie、签名地址不落盘。
- **弹幕与字幕（FR-CACHE-008、FR-LIB-004）**：protobuf 分段弹幕解码与去重合并 XML、B 站 JSON 字幕转 `.vtt`/`.srt`、纯 Go 弹幕排版器与字幕时间轴同步渲染。
- **跨站订阅、检查更新、按站点额度退避与自动缓存（FR-SUBS-001～004、FR-SUB-001～002、FR-DY-007、NFR-NET-002～003）**：单订阅可同时关联哔哩哔哩与抖音账号；按站点分派检查更新（抖音首次建立起点记录 `account_baseline_pinned`，置顶旧作品不当新作品、不提前结束翻页，不确定空结果记“本轮结果不完整”且不移动起点）；定时调度、退避与每小时额度按站点存储在 `site_scheduler_state`（哔哩哔哩 200 次/小时、单轮 ≤ 30 账号；抖音 120 次/小时、单轮 ≤ 20 账号、间隔 ≥ 2.5 秒），单站点风控或熔断不影响另一站点；共用缓存规则自动缓存与哔哩哔哩关注导入。
- **直播状态、HLS fMP4 与 HTTP-FLV 录制（FR-LIVE-001～002、FR-LIVE-004、FR-DY-008～009）**：哔哩哔哩批量开播查询与 HLS fMP4 优先录制、无 fMP4 时自动回退 HTTP-FLV；抖音以主播 `sec_uid` 为唯一主键（`webRid` 作可失效缓存并优先保留数字 `short_id`，支持 `reflow/info` 换号与 `check_alive` 场次结束巡检）走 HTTP-FLV 主路线；纯 Go FLV 解析支持经典 AVC、`CodecID=12` HEVC、Enhanced FLV `hvc1` 与 AAC（校验参数集非空），按 GOP 写 fMP4 分片并复用 fsync checkpoint、断流重取流换段、启动恢复与 mp4ff `Defragment` 收尾。
- **主页信息流、媒体库、站点标记与筛选、看图/纯音频与视频播放器、日志与托盘（FR-FEED-001、FR-IMG-001、FR-LIB-001～006、FR-SET-001～002、FR-SITE-002、FR-RUN-001）**：`mygo/ui` 图标栏 + 上下文侧栏 + 页面（1280/1000/720 三档）；登录页两站点切换、设置“账号”分哔哩哔哩与抖音两组、退避按站点分行展示；用户页、订阅账号列表、主页帖子、媒体库卡片、任务行、最近解析六处显示小字“B 站”/“抖音”（订阅网格头像不加），主页与媒体库支持“全部 / B 站 / 抖音”筛选；Media Foundation + `mygo/audio` 同时支持音视频双轨与纯音频文件（图文配乐 `music.*`，看图层播放视频页或实况片段时自动暂停配乐）；关闭到托盘销毁窗口，最新冒烟的托盘专用工作集 `0.58 MiB`、无 WebView2 子进程。

安装与更新：实现、自动化测试与 ptest 演练通过，v0.1.1 已发布，实机未验收（H-002）：

- **版本号（FR-UPD-001）**：正式版 `0.1.x`（还没有发布时从 `0.1.1` 开始），开发版 `<下一个正式版>-dev.<UTC 时间>`；版本、通道、提交与构建时间注入程序，“设置 › 关于”、诊断、日志、文件属性、安装包名、卸载项与 `VERSION.txt` 一致（[工程规则 §2.1](../dev-rules/engineering.md#versions)）。
- **开发版本机自动更新（FR-UPD-002）**：每次需要用户人工检查时（B 交付整个阶段、A 审查修复或返工后、按用户反馈修复后请用户复查）由 Agent 执行 `go run ./tools/package -install`，用户不用自己装（只为自动验证的打包不装，2026-11-13 用户要求、11-14 纠正）：本机装了 TRACE Dev 就请它 `--quit-for-update`（暂停任务、收尾录制、保存状态），最多等 30 秒、不强杀，静默安装、核对版本、按原窗口状态重开；结果写 `artifacts/package/install-dev.json`（[工程规则 §2.2](../dev-rules/engineering.md#dev-install)）。
- **正式版联网更新（FR-UPD-003）**：只在正式版启用；启动 1 分钟后与每 24 小时检查，手动检查，自动下载（可关），顶部“新版本已就绪”提示、重启前的确认、更新后第一次启动的说明，“设置 › 关于”的两个开关（[架构 §8.1](../design/architecture.md#updates)）。
- **更新的安全与数据保护（FR-UPD-004）**：Ed25519 签名之外，TRACE 补上 HTTPS（含重定向）、清单完整性与大小、版本证明（防止把旧更新包标成新版本）、只下载清单里的文件并边读边核对；数据文件有结构版本，迁移前留 `.premigrate-<旧版本>`，旧程序遇到新结构拒绝启动（[数据 §1](../design/data-and-cache.md#schema-versions)）。
- **发布（AC-31、AC-33）**：`go run ./tools/release` 检查、签名构建、ptest 升级演练后放进 `dist/trace/`，只有授权后才发布；`.github/workflows/release.yml` 每周检查、构建、演练，发布受仓库变量控制（[工程规则 §2.3、§4.2](../dev-rules/engineering.md#release)）。
- **安装体验（FR-STORE-001）**：两个通道各自的图标、中文安装界面、发布者 `chialecode` 与版权，安装包名带版本。

已知限制：没有可用 GPU 的电脑走软件解码，界面不会卡死，但高分辨率 60 fps 视频可能掉帧；本机没有 cgo 工具链，`go test -race` 无法运行（记为 `not-run`）；大于 2 GiB 的 `co64` 大文件门控默认未执行；抖音直播仅提供 HLS（MPEG-TS）或为纯音频电台模式时不录制并说明原因（见 LOOP-008、LOOP-009）；抖音真实直播的 HEVC（`CodecID=12`/`hvc1`）流没在真实站点见到，只由合成测试覆盖（ACT-014 的直播间只给 AVC）。

<a id="loop"></a>
## 后续 LOOP（Agent 侧）

| 编号 | 内容 | 影响与后继 |
| --- | --- | --- |
| LOOP-001 | 用 `refresh_token` 刷新 Cookie | 已实现（FR-AUTH-003） |
| LOOP-002 | 在线播放（不缓存直接播放：Referer 代理、流式解码） | M1 已实现 FR-LIB-002 的本机播放（抖音图文配乐与混排视频页支持在线预览暂存），完整 FR-PLAY-001 在线播放仍在后续范围 |
| LOOP-003 | 任务详情显示每个文件的进度 | 已实现（FR-CACHE-009） |
| LOOP-004 | 直播录制的 HTTP-FLV 后备（FLV 标签解析、时间戳修正、转 MP4） | 已实现（FR-DY-009、FR-LIVE-004，[直播设计 §2.1](../design/live.md#flv)） |
| LOOP-005 | 远程服务器（VPS）无头录制模式与轻量 Web 配置界面 | 通过 `INV-05` 与 `service.Backend` 接口保持后端与 `mygo/ui` 解耦（[架构设计 §9](../design/architecture.md#headless-ext)）；需要时直接为 `internal/service` 增加轻量 `net/http` JSON/Web 控制台或 `mygo/ui` WASM 前端 |
| LOOP-006 | 更新的深化：差分更新的体积、更新说明的排版、Windows 代码签名、winget / Microsoft Store、测试版通道、更新失败时的回退帮助 | 安装与更新已贯通完整流程（[M2 计划 §7](https://github.com/chialecode/trace/blob/15e229c/docs/delivery/stage-plan.md)）；按反馈再排 |
| LOOP-007 | 代码注释里残留的旧计划与问题编号（如“R8”“F-043”“C22”） | 文档、测试名及 M3 改动的全部 Go 文件已清理为直接描述；未触及的文件在后续改到时顺手清理 |
| LOOP-008 | 抖音直播的 HLS-TS 后备（MPEG-TS 解复用，候选 `github.com/bluenviron/mediacommon/v2`） | 当前只录 HTTP-FLV；直播间只有 HLS 时录制失败并说明原因。真实检查或用户反馈出现这种直播间时再做（[选型 §4](../research/2026-11-12-douyin.md#a-review)） |
| LOOP-009 | 纯音频（语音电台）直播录制 | 当前识别语音直播并明确提示暂不支持录制；后续需要时为纯 AAC 直播流补单音频轨 `.m4a` 收尾与播放 |
| LOOP-010 | 抖音扫码登录与关注列表导入 | 当前按 DEC-043 提供粘贴 Cookie 登录，关注导入仅针对哔哩哔哩；后续按 FR-AUTH-006、FR-SITE-001 单独研究协议与风控成本 |
| LOOP-011 | 抖音缓存作者目录可读命名与订阅网格主动查询开播状态 | 当前作者目录采用 `sec_uid` 的 SHA-256 前 12 位防止改名冲突，订阅网格复用最近一次开播结果避免逐人高频请求（Q-014 第 5 条）；按实机体验反馈评估 |
| LOOP-012 | 清零 `download`、`live`、`subscription`、`library` 对 `site/bilibili` 的历史直接引用 | `tools/check` 已锁定现有引用者名单、抖音实现全程经 `internal/site` 接口零耦合；后续可平滑收敛旧哔哩哔哩调用点 |
| LOOP-013 | 抖音直播 FLV 的 CDN 302 跳转修复后的真实录制（M3 审查 F-004） | ACT-012 确认 FLV 第一跳 `302` 到白名单外的 HTTPS 443 域名，A 已改为“第一跳查白名单、之后只允许 HTTPS 443 公共域名”（[直播设计](../design/live.md)读流一节）。已解决：[ACT-014](../../USER-ACTIONS.md#act-014) 真实录制通过（AVC 720p，21.56 秒）。以后如果跳转目标变成 IP 或非 443 端口，录制会报“已拦截”，再按日志评估是否放开 |

<a id="issues"></a>
## 开发与上游事项

| 编号 | 内容 | 状态 |
| --- | --- | --- |
| OSS-001 | MyGo 持续高频迭代（当前 `v0.8.3`） | 按 DEC-032 不保留旧版兼容包袱，持续同步 MyGo 最新版；后端核心包不依赖 MyGo（INV-05），只有 `main.go`、`internal/shell`、`internal/ui`、`internal/player`、`internal/locales`、`tools/uishots` 引用 MyGo。MyGo 的更新签名只覆盖更新包内容、不含版本号，TRACE 在清单里加了版本证明；MyGo 的更新不改卸载项的版本，TRACE 在更新后第一次启动时改；MyGo 自己的“应用正在运行”安装提示仍是英文 |
| OSS-002 | Windows Media Foundation（`IMFSourceReader`）纯 Go COM 调用与 `mygo/audio` 音画同步 | 合成 AVC/AAC 与纯音频（WAV/AAC）读取、Seek 与静音测试通过；真实音频设备体验留 H-001 |
| OSS-003 | `mp4ff` v0.62.0 渐进式 MP4（1 秒交错 + `elst`）、单文件 MP4 流式 `moov` 前置重排及最高音轨后备 | 小样本合并/轨道/时长、尾置 `moov` 磁盘流式前置重排（含 32 位偏移升级 `co64` 合成测试）与失败保留旧文件回归通过；最高 FLAC/杜比另存，主 MP4 用 AAC；真实 FLAC-in-MP4 解码未验证（[媒体选型](../research/2026-11-05-media.md#hires-audio)） |
| OSS-004 | 纯 Go 弹幕排版器与 WebVTT 字幕在 `mygo/ui` 中的叠加性能 | 测试验证：10,000 条弹幕在时间窗二分查找 + 轨道防重叠下单帧耗时远低于 `1 ms` 目标，Seek 无残留 |
| OSS-005 | 直播录像用 `mp4ff` `Defragment` 把 fMP4（含 HLS fMP4 与 HTTP-FLV 按 GOP 重封装的 fMP4）转普通 MP4 | 默认小样本（AVC、`CodecID=12` HEVC、Enhanced FLV `hvc1` + AAC）转换与登记恢复通过；大于 2 GiB 与稀疏 `co64` 门控未执行，不能记为已验证 |
| OSS-006 | `mygo/ui` 窗口关闭到托盘销毁窗口、释放纹理并在同一进程内反复重建 | 托盘测试与最新 `tools/smoke` 通过；真实隐藏窗口、单实例与隔离数据锁/路径检查通过；内存数值见[阶段报告](stage-report.md)及 `artifacts/smoke/smoke.json` |
| OSS-007 | MyGo 调用 NSIS（`v3.13`）生成按用户安装包（`%LOCALAPPDATA%\Programs\trace[-dev]`） | 开发版、正式版与 ptest 安装包都已生成；ptest 的静默安装、更新与卸载演练通过；安装界面的实机外观与 SmartScreen 体验在 H-002，not-run。安装包本身没有文件属性里的版本信息（MyGo 的 NSIS 脚本不写），程序与卸载项有 |
| OSS-008 | 二维码传递模块归档遗漏 LICENSE | 已核对官方仅增加 MIT LICENSE 的下一提交，将许可原文作为精确版本补充嵌入通知，其他缺许可模块停止打包（[来源与移除条件](../../tools/internal/build/licenses/README.md)） |
| OSS-009 | 抖音网页签名（`a_bogus`、`x-secsdk-web-signature`）由 TRACE 按协议规格自写；站点前端约每 3～6 个月局部收紧一次 | 签名模块隔离在 `internal/site/douyin/sign`（纯标准库，不联网、不读凭证），国标 SM3 向量、规格附录固定向量及 ACT-010 真实站点 V-02 全部通过；未来失效时抖音请求按风控停下并登记 S 项，不影响哔哩哔哩（[选型 §4](../research/2026-11-12-douyin.md#a-review)） |
