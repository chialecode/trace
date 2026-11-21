# 交付状态

更新：2026-11-21（M5 远程版、上传与导入导出及全仓库代码审查、重构与依赖更新在分支 `codex/m5-remote` 全部收敛并通过门禁、截图与双冒烟，待推送并经 CI 合并至 `main`；M4 及合并后的三轮实机反馈已合并为 `main @ 8327803`）。本页只写已实现的能力、当前阶段、已知限制、LOOP 和开发与上游事项，不按轮次追加。

## 当前阶段

**M5（远程版、上传与导入导出）**（[当前计划 TRACE-M5-01](stage-plan.md)，DEC-062）：分支 `codex/m5-remote`，基点 `main @ 8327803`。

- **状态**：M5 全部范围（C0、D1、W1～W9、M5-K01～K24）、2026-11-20 实机反馈修复（DEC-063）及 2026-11-21 全仓库代码审查、缺陷修复、质量重构、依赖更新（`mp4ff` v0.63.0、PR #9 并入）、无用代码/文档清理、开发版下载/归属/任务卡片修复及订阅“内容”里删除已缓存条目（DEC-064）均已收敛，门禁、截图、桌面与远程冒烟、`-server` 打包全部通过（[阶段报告](stage-report.md#full-audit-20261121)），本机开发版已更新到 `0.1.3-dev.202611210644`。需求 FR-REMOTE-001～008（[需求 §2.10](../requirements.md#remote-scope)）已实现；AC-56～64 自动化通过，其中真实 VPS、真实站点 Cookie 与 Google Drive 的部分仍是 `not-run`，等用户做 [H-003](../../USER-ACTIONS.md#h-003)。设计正本是[远程设计](../design/remote.md)，默认值见 [Q-022](../../USER-ACTIONS.md#q-022)。

## 上一阶段

**M4（我的：登录账号的站点内容）**：经 PR #8 squash 合并为 `main @ a613735`，合并后的三轮实机反馈（DEC-059～061）经 PR #10 合并为 `main @ 8327803`（[M4 计划](https://github.com/chialecode/trace/blob/8327803/docs/delivery/stage-plan.md)、[M4 报告](https://github.com/chialecode/trace/blob/8327803/docs/delivery/stage-report.md)，含五轮实机反馈与 A 的各次集中审查）。需求 FR-ACCT-001～005 已实现，AC-45～53 自动化与截图通过；[ACT-020](../../USER-ACTIONS.md#act-020) 真实检查了哔哩哔哩四项与抖音关注、收藏，抖音“喜欢”对非浏览器请求固定回 0 字节，现在如实提示（LOOP-014）。实机检查 H-001 第 28～36 步仍是 not-run。上上阶段 M3 经 PR #7 合并为 `main @ f080483`（[M3 计划](https://github.com/chialecode/trace/blob/f080483/docs/delivery/stage-plan.md)、[M3 报告](https://github.com/chialecode/trace/blob/f080483/docs/delivery/stage-report.md)）。

## 已实现能力

代码与对应自动化输入已实现并验证（M1 见 [M1 报告](https://github.com/chialecode/trace/blob/29c2678/docs/delivery/stage-report.md)，M2 见 [M2 报告](https://github.com/chialecode/trace/blob/15e229c/docs/delivery/stage-report.md)，M3 见 [M3 报告](https://github.com/chialecode/trace/blob/f080483/docs/delivery/stage-report.md)），用户 H-001 实机体验未执行：

- **按用户安装与数据目录（FR-STORE-001）**：`internal/appdirs` 严格按通道分离路径（`%LOCALAPPDATA%\Programs\trace[-dev]`、`%LOCALAPPDATA%\trace[-dev]`、`文档\trace[-dev]`），单实例锁与文件全用小写，不写 `%APPDATA%` 与 `~\.trace`。
- **多站点凭证管理与自动续期（FR-AUTH-001～005、FR-AUTH-007、FR-DY-001、NFR-SEC-001～003）**：`credential.Manager` 按站点分文件、分熵做 Windows DPAPI 加密（`credentials/bilibili.bin` 完全兼容旧版密文，`credentials/douyin.bin` 使用独立熵 `trace:douyin:credential:v1`）；抖音 Cookie 清洗剥离 `bd_ticket_guard_*` 等动态票据并经 `profile/self` 校验后保存；登录、失效与退出按站点独立（任一站点已登录即可浏览本机内容，未登录站点在发请求前拒绝，单站点失效只暂停本站点的任务与录制）；`RedactString` 同时覆盖两站点凭证；哔哩哔哩扫码凭证支持 CorrespondPath RSA-OAEP 自动续期与待确认恢复。
- **多站点数据结构版本 2 与迁移备份（NFR-DATA-004）**：`library.db`、`updates.db`、`subscriptions.json`、`ui-state.json` 升至结构版本 2，迁移前生成 `.premigrate-1`（备份失败不迁移），SQLite 单事务与 JSON 原子写保证半途失败可回滚且下次启动可续迁；旧版本程序遇到 v2 数据拒绝写入。
- **哔哩哔哩协议、解析与最高画质缓存（FR-LINK-001～002、FR-HIST-001、FR-CACHE-001～009、DEC-037）**：AV/BV/b23.tv/动态/空间/直播链接解析，最近解析缓存本机封面与头像缩略图（`<数据目录>/thumbs/`，按 `<site>:<kind>:<id>` 归属与准确引用清理）；WBI 签名与 412/-352/-412 熔断，下载跳过非白名单 MCDN 节点；默认最高画质与音质、多 P 全部缓存、分片断点续传、mp4ff 1s 交错合并（含 `elst`；大文件 `co64` 门控未执行）、最高音轨另存并为主 MP4 选择可播放音频、画质升级探测与替换、逐文件进度（FR-CACHE-009）。
- **抖音协议、浏览与无水印缓存（FR-DY-002～006、NFR-NET-003）**：纯 Go 标准库签名子包 `internal/site/douyin/sign`（SM3、`dhzx` `a_bogus`、`uifid` 伴随 `x-secsdk-web-signature`，不联网、不读凭证）；分享文本与 `v.douyin.com` 短链（≤ 5 跳白名单）、作品/用户/直播间双号码链接解析；用户页 3:4 作品网格（置顶标识、`max_cursor` 翻页、防访客空壳误判）与作品详情（视频、普通图文、图片/实况/视频页混排图文及配乐条）；无水印最高档选流、单文件 MP4 容器校验与有界内存磁盘流式 `moov` 前置重排、图文逐页与配乐缓存，媒体 CDN 一律不带 Cookie、签名地址不落盘。
- **弹幕与字幕（FR-CACHE-008、FR-LIB-004）**：protobuf 分段弹幕解码与去重合并 XML、B 站 JSON 字幕转 `.vtt`/`.srt`、纯 Go 弹幕排版器与字幕时间轴同步渲染。
- **跨站订阅、本机头像、订阅详情“内容 / 账号”、缓存队列与批量缓存规则（FR-SUBS-001～004、FR-SUB-001～002、FR-USER-004、FR-DY-007、NFR-NET-002～003、DEC-050 ①、DEC-051 ⑥⑦⑧）**：单订阅可同时关联哔哩哔哩与抖音账号，两站点头像统一保存本机副本（`<数据目录>/avatars/`，离线可用）；订阅详情由“内容”（默认，合并该订阅关联全部账号已缓存的动态、视频、作品与直播录像，纯离线读 `library.db`、零站点请求）和“账号”（每个关联账号一行合并直播状态、“录制/停止录制”与“⋯”菜单，旧 `live` 映射至 `accounts`）两个标签组成；按站点分派检查更新（抖音首次建立起点记录 `account_baseline_pinned`，置顶旧作品不当新作品、不提前结束翻页，不确定空结果记“本轮结果不完整”且不移动起点）；用户页“全部缓存”以独立 `cache-queue.json`（v1，不含凭证与签名地址）驱动后台慢速不规律队列（按站点串行、每次只排 1 条，两条间随机 40～150 秒、每 15～25 条长歇 10～25 分钟，支持暂停、继续、取消与重启恢复）；定时调度、缓存队列自动翻页、退避与每小时额度按站点共享 `site_scheduler_state`（哔哩哔哩 200 次/小时、单轮 ≤ 30 账号；抖音 120 次/小时、单轮 ≤ 20 账号、间隔 ≥ 2.5 秒），单站点风控或熔断不影响另一站点；共用缓存规则支持在“设置 › 缓存规则”展开后点“选择订阅…”批量勾选与取消勾选（`SetSubscriptionsRule` 一次写 `subscriptions.json`，取消勾选回退 `default`），并支持哔哩哔哩关注导入。
- **不限同时录制数的直播录制、录制异常记录与页内实时预览（FR-LIVE-001～002、FR-LIVE-004、FR-DY-008～009、DEC-049 ①、DEC-051 ③）**：哔哩哔哩批量开播查询与 HLS fMP4 优先录制、无 fMP4 时自动回退 HTTP-FLV；抖音以主播 `sec_uid` 为唯一主键（`webRid` 作可失效缓存并优先保留数字 `short_id`，支持 `reflow/info` 换号与 `check_alive` 场次结束巡检）走 HTTP-FLV 主路线；纯 Go FLV 解析支持经典 AVC、`CodecID=12` HEVC、Enhanced FLV `hvc1` 与 AAC（校验参数集非空），按 GOP 写 fMP4 分片并复用 fsync checkpoint、断流重取流换段、启动恢复、子帧分片重叠裁剪与 mp4ff `Defragment` 收尾；不限制同时录制的直播间数量（设置页去掉“同时录制数”，`settings.json` 旧字段仅兼容读取）；录制过程在 `item.json` 可选字段 `live.incidents` 记录 5 类异常事件（`reconnect` 断流重连、`lost_segments` 丢失片段及估算秒数、`slow` 速度持续 30 秒低于媒体码率、`disk` 磁盘空间不足、`convert_failed` 收尾转换失败；同类相邻 1 分钟合并、每场最多 200 条、零额外站点请求），录制行显示“⚠ N 处异常”，并在“任务 › 录制异常”集中展示（已结束场次点击打开 `/library/item` 离线详情）；“任务 › 直播录制”支持只读打开正在写入的本机 `.part` 页内实时预览（不额外连站点、无实时弹幕，离开或停止录制立即释放文件与解码器句柄）。
- **检查计划与开播检查的额度分配（DEC-059）**：任务页第五栏“检查计划”列出每个订阅账号上次检查更新的时间与结果、预计下一次定时检查的时间（按每轮上限与退避推算，`subscription.CheckPlan`，只读本机状态），开播录制订阅另列上次开播检查与大约间隔；抖音开播巡检上次查得早的先查（请求节奏见 DEC-060 ⑤）；直播换段时上一段在后台转换，录制不停；录制结束后直播间状态保留录像文件夹，异常不再显示成第二张卡片。
- **全部缓存、作品数与合作视频归属（DEC-061）**：缓存规则可开“全部缓存”，每个账号每 7 天用慢速缓存队列（`origin = full_cache`）补齐全部作品，每个站点一次一个；“全部账号”与订阅详情“账号”显示上次解析得到的作品总数（`updates.db` 的 `account_works`）与本机已缓存数，打开不发请求；为成员缓存的合作视频按这位成员筛选信息流时出现，媒体库作者列表列在成员下，从用户页点开的视频在视频页缓存时也带上这位成员。
- **查到开播即录、手动检查（DEC-060）**：开了开播录制的账号，手动刷新开播状态或检查更新时在作品列表看到开播，也按规则开始录制；“检查计划”有“检查全部”与每个账号的“检查”（`UpdateService.CheckAccount`）；合并视频的时长允许差 2 秒或站点时长的 3%；抖音开播检查改为按设置的间隔、请求至少隔 6 秒，不占每小时额度，设置页与“检查计划”说明这套规则（DEC-060 ⑤，取代 DEC-059 ②）。
- **主页信息流、纯离线媒体库、解析页侧栏、任务页五栏、看图/纯音频与视频播放器、日志与原生托盘（FR-FEED-001、FR-HIST-001、FR-IMG-001、FR-LIB-001～006、FR-SET-001～002、FR-SITE-002、FR-RUN-001、DEC-049、DEC-050、DEC-051 ②④）**：`mygo/ui` 图标栏 + 上下文侧栏 + 页面（1280/1000/720 三档）；解析页与视频/用户页共用“最近解析”侧栏，主面板只留居中大输入框；媒体库固定按发布时间从新到旧排序，点击卡片播放与“⋯ → 详情”打开 `/library/item` 本机详情全程零网络请求（仅“⋯ → 在解析页打开”联网），删除某个作者最后一条缓存时连同变空的类型与作者目录一起清理（保留站点与保存根目录）；图标栏“任务”数字角标只数排队和进行中的下载任务，任务页侧栏切换“下载任务 / 缓存队列 / 直播录制 / 录制异常”（录制异常角标统计最近 7 天含进行中有异常的录制场数，0 场不显示数字），图标栏“订阅”橙点提示正在直播；登录页两站点切换、设置“账号”分哔哩哔哩与抖音两组、退避按站点分行展示；用户页、订阅账号列表、主页帖子、媒体库卡片、任务行、最近解析六处显示小字“B 站”/“抖音”（订阅网格头像不加），主页与媒体库支持“全部 / B 站 / 抖音”筛选；Media Foundation + `mygo/audio` 同时支持音视频双轨与纯音频文件（图文配乐 `music.*`，看图层实况照片翻到即播且播放视频页或实况片段时自动暂停配乐）；Windows 托盘右键在指针处打开只有“打开 TRACE Dev”“退出”的系统原生菜单（DEC-050 ③），关闭到托盘销毁窗口、无 WebView2 子进程。

- **我的：登录账号的站点内容（FR-ACCT-001～005）**：图标栏第三项“我的”，侧栏“哔哩哔哩”“抖音”两组默认折叠、手风琴展开，窄窗在页面里选择；`AccountService`（`AccountSections`/`AccountFolders`/`AccountPage`）在发请求前检查登录、功能与站点熔断，风控与限流记入该站点的自动停止，列表只在内存里、日志只记功能名、页数与条数；哔哩哔哩关注（分组）、收藏夹（`list-all` + `resource/list`）、稍后再看（WBI，“全部 / 未看完”）、历史记录（游标三元组、按日分组）；抖音关注（`max_time`）、喜欢、收藏（`listcollection` 表单 POST 体参与 `a_bogus`，收藏夹 `collects_id_str`），第 1 页空壳判“登录已失效或被拦截”；条目经 `OpenLink` 打开详情，“⋯”提供缓存、复制链接、在浏览器中打开、加入订阅；全部只读。

- **远程版、上传与导入导出（FR-REMOTE-001～008、DEC-062）**：无界面的 `cmd/trace-server`（`linux/amd64`、`linux/arm64`，`CGO_ENABLED=0`，不链接 MyGo、界面、播放器与外壳）有 `init`、`run`、`token`、`upload-test`、`rclone-filter`、`version`；`go run ./tools/package -server` 打成带 systemd 示例与中文部署说明的 tar.gz。HTTPS（TLS 1.3、自签名证书按连接码里的指纹固定）+ 只存哈希的令牌，同一地址 1 分钟失败 5 次封 10 分钟，请求体上限 1 MiB，协议不同 409。远程用服务层的远程配置运行：不续期、不做全部缓存与补更早内容，Cookie 推送先校验再保存（Linux 上用数据目录里的随机密钥 AES-GCM 加密）。订阅快照整体替换并保留账号起点；内置上传逐条目调用 rclone，先传媒体文件、最后传 `item.json`，确认后只删刚上传的登记文件，失败按 1/5/15/60 分钟退避并分类说明。所有写了一半的文件都是 `*.part` 或 `.trace-*`（含 DASH 分轨）。本地“设置 › 远程”连接、查看状态、同步订阅与 Cookie，本地续期或重新登录后自动推送，推送不了标“Cookie 待同步”并重试。媒体库“⋯ → 导入…”只读扫描后按六类预览、冲突逐条或批量选保留本地 / 用导入的 / 合并，复制先写 `.part` 校验再改名、可中断续做、默认不动来源；“⋯ → 导出…”按全部或当前筛选导出可再导入的文件夹；两台机器各录一部分的直播合并后段号连续；进度在任务页“导入导出”。订阅页可导出、导入 `trace-subscriptions.json`（不含 Cookie），导入先预览。条目清单新增可选的 `cachedBy`，数据结构版本仍是 2。

安装与更新：实现、自动化测试与 ptest 演练通过，v0.1.1 已发布，实机未验收（H-002）：

- **版本号与设置“更新”页（FR-UPD-001、DEC-051 ①）**：正式版 `0.1.x`（还没有发布时从 `0.1.1` 开始），开发版 `<下一个正式版>-dev.<UTC 时间>`；版本、通道、提交与构建时间注入程序，“设置 › 更新”（位于“关于”之前，“关于”页指向“更新”）、诊断、日志、文件属性、安装包名、卸载项与 `VERSION.txt` 一致（[工程规则 §2.1](../dev-rules/engineering.md#versions)）。
- **开发版本机自动更新（FR-UPD-002、DEC-051 ①⑤）**：每次需要用户人工检查时（B 交付整个阶段、A 审查修复或返工后、按用户反馈修复后请用户复查）由 Agent 执行 `go run ./tools/package -install`，用户不用自己装（只为自动验证的打包不装，2026-11-13 用户要求、11-14 纠正）：本机装了 TRACE Dev 就请它 `--quit-for-update`（暂停任务、收尾录制、保存状态），最多等 30 秒、不强杀，静默安装、核对版本、按原窗口状态重开；结果写 `artifacts/package/install-dev.json`（[工程规则 §2.2](../dev-rules/engineering.md#dev-install)）。“设置 › 更新”明确说明开发版不联网检查更新，读取 HKCU 卸载项展示本机已安装的开发版版本（高于当前运行进程时提示重启生效），并在更新后首次启动写入“本机打包安装”（`dev_install`）记录。
- **正式版联网更新与更新记录（FR-UPD-003、DEC-051 ①）**：联网检查只在正式版启用；启动 1 分钟后与每 24 小时检查，支持在“设置 › 更新”查看上次与下次检查时间、点击“立即检查”与管理“自动检查更新”“自动下载更新”两个开关，顶部“新版本已就绪”提示、重启前的确认、更新后第一次启动的说明；`<数据目录>/update-history.json`（版本 1，原子写、不含下载地址与查询串）保存最近 20 次检查记录与最近 50 次更新记录（`auto_update` 自动更新、`manual_install` 手动安装、`dev_install` 本机打包安装）（[架构 §8.1](../design/architecture.md#updates)）。
- **更新的安全与数据保护（FR-UPD-004）**：Ed25519 签名之外，TRACE 补上 HTTPS（含重定向）、清单完整性与大小、版本证明（防止把旧更新包标成新版本）、只下载清单里的文件并边读边核对；数据文件有结构版本，迁移前留 `.premigrate-<旧版本>`，旧程序遇到新结构拒绝启动（[数据 §1](../design/data-and-cache.md#schema-versions)）。
- **发布（AC-31、AC-33）**：`go run ./tools/release` 检查、签名构建、ptest 升级演练后放进 `dist/trace/`，只有授权后才发布；`.github/workflows/release.yml` 每周检查、构建、演练，发布受仓库变量控制（[工程规则 §2.3、§4.2](../dev-rules/engineering.md#release)）。
- **安装体验（FR-STORE-001）**：两个通道各自的图标、中文安装界面、发布者 `chialecode` 与版权，安装包名带版本。

已知限制：没有可用 GPU 的电脑走软件解码，界面不会卡死，但高分辨率 60 fps 视频可能掉帧；本机没有 cgo 工具链，`go test -race` 无法运行（记为 `not-run`）；大于 2 GiB 的 `co64` 大文件门控默认未执行；抖音直播仅提供 HLS（MPEG-TS）或为纯音频电台模式时不录制并说明原因（见 LOOP-008、LOOP-009）；抖音真实直播的 HEVC（`CodecID=12`/`hvc1`）流没在真实站点见到，只由合成测试覆盖（ACT-014 的直播间只给 AVC）；远程版只经本机、WSL Ubuntu 与假站点、假 rclone 验证，真实 VPS、真实 Cookie 推送与 Google Drive 是 `not-run`（H-003）；本地一次只连一个远程；导入、导出与订阅文件的路径直接输入，没有系统的文件夹选择框；Windows 上的远程冒烟不能发控制台信号，改为结束进程后再启动（`SIGTERM` 收尾在 Linux 上验证）。

<a id="loop"></a>
## 后续 LOOP（Agent 侧）

| 编号 | 内容 | 影响与后继 |
| --- | --- | --- |
| LOOP-001 | 用 `refresh_token` 刷新 Cookie | 已实现（FR-AUTH-003） |
| LOOP-002 | 在线播放（不缓存直接播放：Referer 代理、流式解码） | M1 已实现 FR-LIB-002 的本机播放（抖音图文配乐与混排视频页支持在线预览暂存），完整 FR-PLAY-001 在线播放仍在后续范围 |
| LOOP-003 | 任务详情显示每个文件的进度 | 已实现（FR-CACHE-009） |
| LOOP-004 | 直播录制的 HTTP-FLV 后备（FLV 标签解析、时间戳修正、转 MP4） | 已实现（FR-DY-009、FR-LIVE-004，[直播设计 §2.1](../design/live.md#flv)） |
| LOOP-005 | 远程版的网页配置界面 | 无头远程版已实现（FR-REMOTE-001～008，[远程设计](../design/remote.md)），由本地“设置 › 远程”管理；以后需要网页时，在 `internal/remoteserver` 的 HTTPS 接口上加轻量网页或 `mygo/ui` WASM 前端（[架构设计 §9](../design/architecture.md#headless-ext)） |
| LOOP-006 | 更新的深化：差分更新的体积、更新说明的排版、Windows 代码签名、winget / Microsoft Store、测试版通道、更新失败时的回退帮助 | 安装与更新已贯通完整流程（[M2 计划 §7](https://github.com/chialecode/trace/blob/15e229c/docs/delivery/stage-plan.md)）；按反馈再排 |
| LOOP-007 | 代码注释里残留的旧计划与问题编号（如“R8”“F-043”“C22”） | 已在全仓库代码审查与重构中全部清理为直接描述 |
| LOOP-008 | 抖音直播的 HLS-TS 后备（MPEG-TS 解复用，候选 `github.com/bluenviron/mediacommon/v2`） | 当前只录 HTTP-FLV；直播间只有 HLS 时录制失败并说明原因。真实检查或用户反馈出现这种直播间时再做（[选型 §4](../research/2026-11-12-douyin.md#a-review)） |
| LOOP-009 | 纯音频（语音电台）直播录制 | 当前识别语音直播并明确提示暂不支持录制；后续需要时为纯 AAC 直播流补单音频轨 `.m4a` 收尾与播放 |
| LOOP-010 | 抖音扫码登录与关注列表批量导入 | 当前按 DEC-043 提供粘贴 Cookie 登录；“我的 › 抖音 › 关注”可以逐个浏览并加入订阅（FR-ACCT-005），像哔哩哔哩那样按分组批量导入仍在后续；扫码按 FR-AUTH-006 单独研究协议与风控成本 |
| LOOP-011 | 抖音缓存作者目录可读命名与订阅网格主动查询开播状态 | 当前作者目录采用 `sec_uid` 的 SHA-256 前 12 位防止改名冲突，订阅网格复用最近一次开播结果避免逐人高频请求（Q-014 第 5 条）；按实机体验反馈评估 |
| LOOP-012 | 清零 `download`、`live`、`subscription`、`library` 对 `site/bilibili` 的历史直接引用 | `tools/check` 已锁定现有引用者名单、抖音实现全程经 `internal/site` 接口零耦合；后续可平滑收敛旧哔哩哔哩调用点 |
| LOOP-013 | 抖音直播 FLV 的 CDN 302 跳转修复后的真实录制 | ACT-012 确认 FLV 第一跳 `302` 到白名单外的 HTTPS 443 域名，A 已改为“第一跳查白名单、之后只允许 HTTPS 443 公共域名”（[直播设计](../design/live.md)读流一节）。已解决：[ACT-014](../../USER-ACTIONS.md#act-014) 真实录制通过（AVC 720p，21.56 秒）。以后如果跳转目标变成 IP 或非 443 端口，录制会报“已拦截”，再按日志评估是否放开 |
| LOOP-014 | 抖音“我的”的真实验证与“观看历史” | ACT-020 与 2026-11-14 的复查（[抖音协议 §12](../research/douyin.md#account-lists)）：关注、全部收藏真实通过；“喜欢”对非浏览器请求固定回 HTTP 200、0 字节（加网页参数和 `DOWNGRADE` 头都一样），现在提示“只给浏览器里的抖音网页”，不打开退避；要拿到需要浏览器通道（Q-016 第 1 条）。收藏夹（`collects/list`、`collects/video/list`）与 `history/read` 还没有真实证据，观看历史入口按 Q-015 第 3 条隐藏（`douyin.WatchHistoryEnabled=false`）；最后一页是否会给 `aweme_list: null` 仍待确认（现在任何一页都按空壳处理） |
| LOOP-015 | 冒烟偶发“抢焦点” | 2026-11-15 托盘改为原生菜单（F-008）后的开发版 `0.1.2-dev.202611150223` 在 17 次冒烟中有 2 次在 `tray` 与 `restored` 快照报 `StealFocusDetected = true`。B 对同一开发包又连跑 15 次、新开发包连跑 10 次，**都没有复现**，所以根因没有确认。B 做了不放宽检查的加固：安静模式不挂托盘的单击与右键回调、不切全屏（`TestQuietModeSuppressesTrayClickAndMenu`），`reuse_window` 与 `second_instance` 也核对焦点，并新增 `StealFocusDetail`（阶段名、窗口类名与可见性，不含标题）。冒烟中没有人点托盘，所以“托盘回调抢焦点”只是可能的原因之一。下次再出现时按 `StealFocusDetail` 定位具体窗口（[阶段报告 §8、§9](https://github.com/chialecode/trace/blob/8327803/docs/delivery/stage-report.md#b-feedback-1)） |
| LOOP-016 | 一个站点登录失效时，另一个站点的缓存队列也停下 | 已解决：`PauseAllActiveWork` 现按站点检查活跃凭证，只暂停未登录或已失效的站点，不再暂停仍登录站点的缓存队列、下载任务或直播录制 |

<a id="issues"></a>
## 开发与上游事项

| 编号 | 内容 | 状态 |
| --- | --- | --- |
| OSS-001 | MyGo 持续高频迭代（当前 `v0.9.0`） | 按 DEC-032 不保留旧版兼容包袱，持续同步 MyGo 最新版；后端核心包不依赖 MyGo（INV-05），只有 `main.go`、`internal/shell`、`internal/ui`、`internal/player`、`internal/locales`、`tools/uishots` 引用 MyGo。MyGo 的更新签名只覆盖更新包内容、不含版本号，TRACE 在清单里加了版本证明；MyGo 的更新不改卸载项的版本，TRACE 在更新后第一次启动时改；MyGo 自己的“应用正在运行”安装提示仍是英文 |
| OSS-002 | Windows Media Foundation（`IMFSourceReader`）纯 Go COM 调用与 `mygo/audio` 音画同步 | 合成 AVC/AAC 与纯音频（WAV/AAC）读取、Seek 与静音测试通过；真实音频设备体验留 H-001 |
| OSS-003 | `mp4ff` v0.63.0 渐进式 MP4（1 秒交错 + `elst`）、单文件 MP4 流式 `moov` 前置重排及最高音轨后备 | 小样本合并/轨道/时长、尾置 `moov` 磁盘流式前置重排（含 32 位偏移升级 `co64` 合成测试）与失败保留旧文件回归通过；最高 FLAC/杜比另存，主 MP4 用 AAC；真实 FLAC-in-MP4 解码未验证（[媒体选型](../research/2026-11-05-media.md#hires-audio)） |
| OSS-004 | 纯 Go 弹幕排版器与 WebVTT 字幕在 `mygo/ui` 中的叠加性能 | 测试验证：10,000 条弹幕在时间窗二分查找 + 轨道防重叠下单帧耗时远低于 `1 ms` 目标，Seek 无残留 |
| OSS-005 | 直播录像用 `mp4ff` `Defragment` 把 fMP4（含 HLS fMP4 与 HTTP-FLV 按 GOP 重封装的 fMP4）转普通 MP4 | 默认小样本（AVC、`CodecID=12` HEVC、Enhanced FLV `hvc1` + AAC）转换与登记恢复通过；大于 2 GiB 与稀疏 `co64` 门控未执行，不能记为已验证 |
| OSS-006 | `mygo/ui` 窗口关闭到托盘销毁窗口、释放纹理并在同一进程内反复重建 | 托盘测试与最新 `tools/smoke` 通过；真实隐藏窗口、单实例与隔离数据锁/路径检查通过；内存数值见[M3 报告](https://github.com/chialecode/trace/blob/f080483/docs/delivery/stage-report.md)及 `artifacts/smoke/smoke.json` |
| OSS-007 | MyGo 调用 NSIS（`v3.13`）生成按用户安装包（`%LOCALAPPDATA%\Programs\trace[-dev]`） | 开发版、正式版与 ptest 安装包都已生成；ptest 的静默安装、更新与卸载演练通过；安装界面的实机外观与 SmartScreen 体验在 H-002，not-run。安装包本身没有文件属性里的版本信息（MyGo 的 NSIS 脚本不写），程序与卸载项有 |
| OSS-008 | 二维码传递模块归档遗漏 LICENSE | 已核对官方仅增加 MIT LICENSE 的下一提交，将许可原文作为精确版本补充嵌入通知，其他缺许可模块停止打包（[来源与移除条件](../../tools/internal/build/licenses/README.md)） |
| OSS-009 | 抖音网页签名（`a_bogus`、`x-secsdk-web-signature`）由 TRACE 按协议规格自写；站点前端约每 3～6 个月局部收紧一次 | 签名模块隔离在 `internal/site/douyin/sign`（纯标准库，不联网、不读凭证），国标 SM3 向量、规格附录固定向量及 ACT-010 真实站点 V-02 全部通过；未来失效时抖音请求按风控停下并登记 S 项，不影响哔哩哔哩（[选型 §4](../research/2026-11-12-douyin.md#a-review)） |
