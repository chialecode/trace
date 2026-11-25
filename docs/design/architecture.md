# 技术设计：架构、接口与原生界面

技术设计说明系统“应当怎样工作”，按专题分五份：本页（进程与分层、代码目录、服务接口、站点客户端、凭证、原生界面、不变量、窗口与托盘、远程版的分层）、[数据、缓存与信息流](data-and-cache.md)、[订阅、检查更新与自动缓存](subscriptions.md)、[直播](live.md)、[远程版、上传与导入导出](remote.md)。与代码不一致时按[冲突规则](../dev-rules/workflow.md#conflicts)处理。选型依据见[技术选型研究](../research/2026-11-05-stack.md)、[媒体与存储选型](../research/2026-11-05-media.md)与[X 开源客户端与协议依赖选型](../research/2026-11-22-x.md)，协议见[哔哩哔哩协议资料](../research/bilibili.md)、[抖音协议资料](../research/douyin.md)、[微博协议资料](../research/weibo.md)与[X 协议与官方 API 资料](../research/x.md)。

<a id="architecture"></a>
## 1. 进程与分层

单个 Go + MyGo 进程（DEC-032）：用 `mygo.NewWindow(mygo.WindowOptions{Content: ui.View(…)})` 与 `github.com/egoist/mygo/ui` 直接渲染原生界面（Windows 下 Direct3D 11 加速，无可用显卡驱动时自动退回 WARP 软件渲染），不启动 WebView2 子进程，也不依赖客户机的浏览器运行时。没有单独的后台守护进程；窗口关到托盘后原生窗口销毁、释放界面纹理与视图状态，进程继续运行，缓存任务、定时检查、自动缓存、直播录制和登录续期都在这个进程里进行，托盘“退出”才结束（DEC-030，第 8 节）。MyGo 的 `userData`、`cache`、`logs` 路径用 `App.SetPath` 改到数据目录里（`<数据目录>/mygo`、`cache`、`logs`），所以 TRACE 不往 `%APPDATA%` 和用户主目录写东西（NFR-DATA-003，数据目录见[数据 §1](data-and-cache.md#storage)）。

```text
internal/ui（mygo/ui 声明式视图、路由、看图层、播放层） ──(Go 接口调用 + 事件订阅)──> internal/service（薄层：登录检查、参数校验、错误转换、DTO）
      │                                                                                      │
      ├── internal/player（Windows Media Foundation + mygo/audio + 纯 Go 弹幕与字幕叠加）     ├── internal/download（任务、分段下载、选流、合并、单文件 MP4 校验、升级）
      │        │                                                                             ├── internal/library（清单、索引、引用、安全删除、媒体路径白名单）
      │        └──────> LibraryService.MediaPath(key, relPath)（INV-08 路径白名单）           ├── internal/subscription（订阅、按站点检查更新、定时调度、自动缓存）
      │                                                                                      ├── internal/live（开播状态、HLS fMP4 与 HTTP-FLV 录制与收尾）
      └── internal/shell（唯一管理 MyGo 窗口、托盘、单实例、对话框、外部链接与通知的包）       ├── internal/site（站点公共层）与 site/{bilibili,douyin,weibo,x}（站点协议，纯 Go）
                                                                                             ├── internal/credential（按站点 DPAPI 凭证 Manager、清洗与续期）
                                                                                             └── internal/storage、settings、uistate、appdirs
```

- **依赖方向只能向下（INV-05）**：`internal/site`（含 `bilibili`、`douyin` 及 `douyin/sign`、`weibo`、`x`）、`internal/download`、`internal/library`、`internal/credential`、`internal/storage`、`internal/subscription`、`internal/live`、`internal/settings`、`internal/uistate`、`internal/appdirs`、`internal/update`、`internal/service` **完全不依赖 MyGo（不引用 `github.com/egoist/mygo` 及其子包）**；只有 `main.go`、`internal/shell`、`internal/ui`、`internal/player`（仅用 `mygo/audio` 与 `*ui.Bitmap`）、`internal/locales`（MyGo 自带字符串与安装界面的中文目录）与 `tools/uishots` 引用 MyGo。`internal/site` 只用标准库；各站点包不依赖存储、缓存、订阅和录制代码，且站点包之间互不引用（完整规则由 `tools/check` 强制，见[工程规则 §3](../dev-rules/engineering.md#quality-gates)）。
- 后端服务需要的外壳能力（推送事件、选目录、打开文件夹、打开外部链接）由 `internal/service` 通过纯 Go 小接口注入，测试和未来的无头模式均可注入对应实现。
- 窗口：标题 “TRACE”（开发通道为 “TRACE Dev”），默认 1280×860，最小 720×560。外部链接一律用系统浏览器打开（`Shell.OpenExternal`）。

<a id="layout"></a>
## 2. 代码目录（DEC-020、DEC-032）

```text
main.go                       组装依赖、获取单实例与数据目录锁、启动服务与原生窗口
cmd/trace-server/             无界面的远程程序（Linux）：init、run、token、upload-test 等命令；deploy/ 放 systemd 示例与中文部署说明
mygo.json                     MyGo 打包配置：正式版身份、版本线 0.1.0、图标、发布者与版权、中文目录（i18n）、更新公钥与 GitHub 仓库；
                              打包工具按通道临时改写（开发版 trace-dev、演练用的 trace-test）
CHANGELOG.md                  正式版每个版本的更新内容（Release 说明与“更新内容”）
assets/                       应用图标 icon.png（正式版）与 icon-dev.png（开发版与 trace-test），tools/icons 生成
internal/
  shell/                      MyGo 桌面外壳适配：NewWindow、窗口关闭与重建、安静模式、托盘与菜单、单实例、通知、对话框、外部链接、在文件夹中显示；
                              启动参数（--quit-for-update、--background）、mygo.Updater 的接入与卸载项版本（updates.go）
  update/                     更新逻辑（纯 Go，不依赖 MyGo）：检查与下载的状态机（controller.go）、TRACE 的更新源与校验（source.go）、检查与更新记录（update-history.json）
  locales/                    中文目录 zh-Hans.json 与 MyGo 生成的访问代码（安装界面与 MyGo 自带字符串）
  ui/                         mygo/ui 原生界面：图标栏 + 上下文侧栏 + 页面的自适应布局、路由、登录页、主页信息流、订阅（列表/详情/导入）、
                              我的、解析页、视频详情、用户页、媒体库（含 /library/item 离线详情与导入页）、任务页（六组侧栏、直播录制实时预览与录制异常、导入导出）、设置页（含“远程”“更新”分类与规则批量选订阅）、看图层、播放层及界面内存测试（ui.NewTester）
  player/                     原生媒体播放与叠加：Windows Media Foundation（IMFSourceReader 纯 Go COM 调用，含增长中 fMP4 .part 的只读实时预览）解码 MP4 音视频、
                              mygo/audio 音频时钟与输出、纯 Go 弹幕排版器（danmaku.go）与 WebVTT 字幕解析匹配（subtitle.go）
  appdirs/                    构建信息（版本、通道、提交、构建时间，由 -ldflags 注入）；通道（release / dev / test / source）与安装、数据、缓存位置的推算，“文档”文件夹的系统登记位置，数据目录独占锁
  site/                       站点公共层（DEC-043）：站点标识与显示名（site.go）、错误类别 Error 与 KindOf/IsKind（errors.go）、规则请求上下文标记
                              （automation.go）、链接分派与分享文本取链接（registry.go）、跨包流动的公共类型（账号键、直播状态与取流计划、
                              媒体传输规格）；不依赖任何具体站点
  site/x/                     X 协议：snowflake.go（雪花 ID 解析与校验）、frontend.go（运行时纯内存发现前端元数据：核心 5 项操作 + Bookmarks/Likes 懒加载分包，单飞冷却，零字面量入库）、
                              txid.go（x-client-transaction-id 纯标准库生成，可注入时钟与随机源）、client.go/graphql.go/errors.go（串行 4～6 秒、x-rate-limit-remaining<=1 主动刹车、ct0 被动同步与错误分类）、
                              auth.go（两步严格登录确认与 twid 核对）、resolve.go（分享文本、t.co 短链与镜像本机改写）、post.go（单帖、长帖、线程、转引与混排/GIF/HLS-only）、
                              user.go（资料、时间线、置顶、可见上限与空壳真值表）、account.go（“我的”关注/书签/喜欢，文件夹与列表受开关控制）、adapter.go（跨站适配）
  site/weibo/                 微博协议：base62.go（mid/mblogid 分段双向换算）、client.go 与 errors.go（PC 主栈请求、节奏、X-XSRF-TOKEN 镜像同步与错误分类）、
                              auth.go（PC 两步严格登录确认）、resolve.go（分享文本、t.cn 短链、sinaurl 与链接分派）、post.go（单条微博、长文、转发、混排与视频选流）、
                              user.go（资料与 mymblog 分页、置顶/快转/可见范围与空壳真值表）、account.go（“我的”关注与收藏，赞过与分组受开关控制）、adapter.go（跨站适配）
  site/douyin/                抖音协议：client.go 请求、节奏与 CDN 白名单、profile.go 与 UA 一致的公共参数、sign/ 子包（SM3、a_bogus、
                              伴随签名，可注入时钟与随机源）、resolve.go 分享文本与短链、auth.go Cookie 清洗与登录确认、work.go 作品详情
                              与选流、user.go 资料与作品分页、live.go 直播间状态与 FLV 取流；testdata/ 合成夹具
  site/bilibili/              站点协议：client.go 请求与节奏、errors.go 错误分类、wbi.go 签名、link.go 链接识别、
                              video.go、user.go、dynamic.go、playurl.go（含清晰度排序表）、login.go 扫码、
                              refresh.go 登录续期、relation.go 关注列表与分组、danmaku.go 弹幕分段、subtitle.go 字幕、
                              live.go 开播状态与取流；testdata/ 夹具
  credential/                 按站点的凭证仓（Manager，每个站点一个文件与加密熵：bilibili.bin、douyin.bin、weibo.bin、x.bin）；DPAPI 加密保存；手动凭证文本解析（按站点的白名单与清洗规则）；续期时的原子替换（只有哔哩哔哩）
  service/                    纯 Go 服务层：auth、parse、cache、cache_queue、feed、library、settings、history、subscription、update、following、account、live、app；dto.go、errors.go
  subscription/               订阅模型与 subscriptions.json（含 .bak）、更新记录（updates.db）、按站点分派的检查更新、按站点的定时调度、退避与每小时额度、自动缓存规则
  live/                       直播录制器：按站点取流，不限同时录制路数；两种输入——HLS fMP4 分片（哔哩哔哩）与 HTTP-FLV（抖音，哔哩哔哩后备；flv.go 解析、remux.go 按 GOP 写 fMP4 分片）；
                              增长中 .part 只读实时预览查询、录制异常事件采集（live.incidents）、断流重连与换段、checkpoint 与启动恢复、分片重叠裁剪与 mp4ff Defragment 收尾校验
  library/                    缓存条目模型、清单（item.json）读写、SQLite 索引、从清单重建、引用、安全删除、信息流查询、MediaPath 路径白名单
  download/                   任务队列与状态机（按站点熔断）、分段下载器（8 MiB 续传、重试、节点轮换）、按站点的执行器与媒体传输规格、选流、合并（mp4ff）、
                              单文件 MP4 校验与 moov 前置、升级探测（只有哔哩哔哩）、弹幕与字幕保存
  settings/、uistate/         设置（settings.json）；界面状态（ui-state.json，主页看过的最新一条、窗口尺寸、最后路由等）
  storage/                    数据目录、原子写、最近解析（history.json）、SQLite 打开与迁移、数据文件的结构版本（schema.go）、临时文件名约定（*.part、.trace-*）
  remote/                     远程协议：连接码、状态与快照等 JSON 类型、按证书指纹固定的 HTTPS 客户端与错误分类（无法连接、证书变了、令牌错、版本不同、被拒）
  remoteserver/               trace-server 的服务端：init 与配置（server.json、证书、令牌哈希）、HTTPS 接口与鉴权限流、磁盘空间、Run 的启动与收尾
  upload/                     远程的内置上传：逐条目调用 rclone、确认后删登记文件、退避与错误分类；fakerclone/ 是测试与冒烟用的假 rclone
  transfer/                   资源库导入（扫描分六类、校验后复制、合并清单）与导出（可携带的 item.json）
tools/check/                  质量门禁（一条命令）
tools/checkdocs/              文档、公开内容与依赖方向检查
tools/uishots/                用 ui.NewTester 渲染全部页面的必测状态与三档宽度，输出 PNG 截图与结构化断言回执 assertions.json
tools/smoke/                  真实进程端到端冒烟（启动 trace.exe、单实例与数据目录锁、托盘关闭与重建、零杂散目录校验）；-server 是远程冒烟（server.go）
tools/package/                开发版打包（解包文件 + NSIS 按用户安装包）与本机自动安装（install.go）；-server 打远程版的 Linux 包（server.go）
tools/release/                正式版发布：检查、构建签名、trace-test 升级演练、可选发布；release.yml 的静态检查
tools/actcheck/               真实站点有界检查程序（act006 / act007 / act010～020 / act022 / act024 / act025，账本门禁与脱敏回执）
tools/icons/                  绘制两个通道的应用图标
tools/internal/build/         打包共用：版本号、通道配置、-ldflags、VERSION.txt、THIRD-PARTY-NOTICES.txt、登记替换、Windows 卸载项与进程读取
```

测试跟着代码走，按能力命名。具体文件名由 B 按需要调整，层次和依赖方向不变。

<a id="services"></a>
## 3. 服务接口（`internal/service`）

`internal/service` 提供纯 Go 业务接口 `BackendAPI`，实现为 `Backend`，由 `internal/ui` 直接在进程内调用（不经过 JSON 跨进程 IPC）。涉及网络的调用接收 `context.Context`；动态 ID、条目键和订阅 UID 使用字符串，进程内 `mid`/`cid` 等数字标识使用 `int64`，不经 JavaScript 数字转换。下表用服务分组描述契约，实际方法签名以代码为准；UI 接口不暴露站点客户端或库对象。除 `AuthService`、`SettingsService` 和 `AppService` 外，所有业务方法先检查登录，而且**按站点**（FR-AUTH-007，DEC-043）：只读本机数据的方法（`FeedService`、`LibraryService`、任务列表、订阅列表与详情、最近解析）要求至少一个站点已登录；会向某个站点发请求的方法（解析、详情、用户页、缓存、检查更新、开播状态、录制、关注导入）要求**这个站点**已登录，否则返回带站点的 `auth_required`，**不发请求**。一个站点都没登录时整个界面只有登录页和设置页。

| 服务方法 | 作用 |
| --- | --- |
| `ParseService.Resolve(input)` | 先从分享文本里取出链接，再按域名分派给对应站点识别（`site.Registry`）；返回带 `site` 的目标，不认识的输入不发请求 |
| `ParseService.DouyinWork(awemeId)` / `DouyinUser(secUid)` / `DouyinUserWorks(secUid, cursor)` / `DouyinLive(webRid)` | 抖音作品详情、用户资料、作品分页 `{items, cursor, hasMore}`（每条带 `cacheKey`）与直播间；成功后写入最近解析（带 `site`） |
| `ParseService.WeiboPost(mid)` / `WeiboVideo(oid)` / `WeiboUser(uid)` / `WeiboUserPosts(uid, cursor)` | 微博单条详情、独立视频页（有宿主微博时归一到单条微博）、用户资料与微博分页 `{items, cursor, hasMore, bottomTipsVisible, bottomTipsText}`（游标为 `<page>:<since_id>`，每条带 `cacheKey`）；`/n/` 与个性域名先换 UID；成功后写入最近解析（带 `site`） |
| `ParseService.XPost(statusId)` / `XUser(restId)` / `XUserByScreenName(screenName)` / `XUserPosts(restId, cursor)` | X 单帖详情（含 `note_tweet` 长帖与作者自回复线程）、用户资料、按 `@handle` 换稳定 `rest_id` 与帖子分页 `{items, cursor, hasMore, reachedVisibleLimit, bottomTipsVisible, bottomTipsText}`（每条带 `cacheKey`）；成功后写入最近解析（带 `site: "x"`） |
| `ParseService.Video(id)` / `User(mid)` / `UserVideos(mid, page)` / `UserDynamics(mid, offset)` | 哔哩哔哩的链接识别与解析；视频与用户成功后写入最近解析（不等缩略图，界面随后调用 `FetchHistoryThumbs` 补上封面与头像，DEC-038）；动态一页 `{items, offset, hasMore}`，每条带 `cacheKey` |
| `AuthService.Status()` / `Statuses()` | 每个站点（`bilibili`、`douyin`、`weibo`、`x`）的 `{site, state: "logged_in"\|"logged_out"\|"expired"\|"unknown", account?}`；`Status()` 保留为哔哩哔哩的简写 |
| `AuthService.StartQR()` / `PollQR(flowId)` / `CancelQR(flowId)` | 扫码流程，返回登录链接（由界面在内存生成二维码图片），不返回任何 Cookie |
| `AuthService.LoginWithCookie(site, text)` | 手动凭证：按站点解析与清洗、向站点确认（哔哩哔哩 nav，抖音 `profile/self`，微博 PC 两步 `getBasicInfo` → `profile/info?screen_name=` 且严格匹配昵称，X 两步 `api.x.com/1.1/account/settings.json` → `UserByScreenName` 且核对 `screen_name` 与 `twid`）、保存；只返回账号摘要，或不含输入内容的 `{kind, message}` 错误；文本上限 8 KiB（第 5 节） |
| `AuthService.Logout(site)` | 先暂停这个站点的缓存任务与录制，再删除这个站点的本机凭证；另外三个站点不受影响 |
| `AuthService.Renewal(site)` | 续期状态：`{available, reason?, lastRenewedAt?, lastError?}`（[§5.1](#renewal)；抖音、微博与 X 的手动 Cookie 固定返回不可用，微博与 X 原因为“粘贴的 Cookie 不续期”） |
| `CacheService.CacheDynamic(id)` / `CacheVideo(bvid)` / `CacheSupplement(bvid)` / `CacheDouyinWork(awemeId)` / `CacheWeiboPost(mid, via?)` / `CacheWeiboVideo(oid)` / `CacheXPost(statusId, via?)` | 建立缓存任务（或为旧视频补存弹幕和字幕），立即返回任务 ID；同一对象已有进行中的任务时返回该任务；`CacheWeiboPost` 与 `CacheXPost` 优先复用内存里未过期的列表/详情计划（签名地址不落盘），无宿主微博的视频页走 `CacheWeiboVideo` |
| `CacheService.States(keys)` | 批量查询状态圆点 `{key: {dot, label, progress, missing[], upgrade?}}`（[数据 §2.6](data-and-cache.md#merge)；只扫描非终态下载任务 `ListNonTerminalTasks`，不反序列化历史已完成任务的明细） |
| `CacheService.Tasks()` / `Pause(id)` / `Resume(id)` / `Cancel(id)` / `Retry(id)` / `RemoveTask(id)` / `RemoveFinishedTasks()` / `TaskDetail(id)` | 下载任务列表与操作（列表与诊断改用轻量 `ListTasksSummary`，跳过 `files_json`/`attempts_json` 反序列化；批量重试/暂停聚合为单次批量事件；已结束任务只支持移除，移除失败/部分完成任务清理登记的 `.part` 临时文件）；详情 `TaskDetail(id)` 按需读取逐文件进度清单与尝试记录（[数据 §2.4](data-and-cache.md#tasks)） |
| `CacheService.ListCacheQueue()` / `FindCacheQueueTask(site, user, list)` / `EnqueueCacheQueue(req)` / `PauseCacheQueue(id)` / `ResumeCacheQueue(id)` / `CancelCacheQueue(id)` | 后台慢速缓存队列（FR-USER-004，DEC-050 ①，替代旧 `CacheOlderUserItems` 按 10/20/50 条入队）：按站点各自串行、每次只排 1 条下载，两条间随机等 40～150 秒、每 15～25 条长歇 10～25 分钟，自动翻页共用对应站点每小时规则额度与退避；进度持久化到 `cache-queue.json`（v3，不含凭证与签名地址）；取消时停止队列并取消派生的未完成下载，保留已入库媒体 |
| `CacheService.CheckUpgrade(keys, force)` / `UpgradeCandidates()` / `Upgrade(key)` | 画质升级探测与升级（[数据 §2.7](data-and-cache.md#upgrade)）；`UpgradeCandidates` 返回最多 50 个待检查的视频（最久未检查的在前），任务页逐个调用 `CheckUpgrade` 以显示进度、可取消，遇风控、限流或未登录即停 |
| `FeedService.Page(cursor, direction, order, limit, filter?)` / `Anchor()` / `SaveAnchor(anchor)` / `NewerCount(anchor)` | 主页信息流（[数据 §4](data-and-cache.md#feed)）；`Anchor` 只记看过的最新一条，作没有 `homeSeen` 的订阅下次启动时的初始已读基准（DEC-040、DEC-053）；阅读位置 `homePositions` 由界面经 `GetUIState`/`UpdateUIState` 读写（DEC-053 ①） |
| `LibraryService.List(filter)` / `Item(key)` / `GetLibraryItemMetaOnly(key)` / `MediaPath(key, relPath)` / `PlanDelete(key)` / `Delete(key)` / `Reveal(key, file)` / `Stats()` | 媒体库查询、纯索引元数据读取（`GetLibraryItemMetaOnly`，零磁盘 `EvalSymlinks` 调用，供主页卡片与下载/队列封面读取）、本机媒体路径白名单解析（[数据 §3](data-and-cache.md#media-server)）、安全删除、在资源管理器中显示、占用统计 |
| `SettingsService.Get()` / `Update(patch)` / `ChooseCacheRoot()` / `OpenFolder(which)` / `Locations()` / `AddLibraryRoot()` | 设置、三个位置与通道（FR-STORE-001）、选择已有的缓存文件夹并接入（FR-LIB-006，[数据 §1](data-and-cache.md#storage)） |
| `AppService.Quit(confirm)` / `QuitPlan()` / `DiagnosticsSnapshot()` / `RecentLogs(maxLines)` | 退出前列出会暂停的任务与会结束的录制（第 8 节）；后台状态快照（索引重建、检查更新、自动缓存、录制情况）、脱敏后的最近运行日志读取（最多 500 行，DEC-033），以及设置“更新”页所需的检查与更新记录读写（`update-history.json` 最近 20 条检查与 50 条更新，DEC-051 ①） |
| `HistoryService.List()` / `Remove(kind, id)` / `Clear()` / `HistoryThumbPath(name)` | 最近解析列表、单条删除、全部清空，以及本机缩略图路径白名单读取（`<数据目录>/thumbs/`，DEC-037） |
| `SubscriptionService.List()` / `Get(id)` / `Create(name, account?)` / `Update(id, patch)` / `Delete(id)` / `Link(id, site, uid)` / `Unlink(id, site, uid)` / `Move(site, uid, toId)` / `PreviewAccount(input)` / `OwnerOf(site, uid)` / `SetRules(id, rules)` / `ListCapabilityTags()` / `SetSubscriptionTags(id, tags)` / `SetTagSubscriptions(tagId, subIds)` / `BatchUpdateSubscriptionsTags(subIds, mode, tags)` | 订阅与缓存能力标签管理（[订阅 §1](subscriptions.md#model)，DEC-070）；关联账号前先用 `PreviewAccount` 解析头像与昵称确认，已属于别的订阅时返回 `conflict` 和原订阅摘要，用户确认后调 `Move`；`SetTagSubscriptions` 按单个标签一次原子写入批量管理关联订阅（保留各订阅的其他标签，DEC-051 ⑧），`BatchUpdateSubscriptionsTags` 跨多个订阅一次原子写入批量添加、移除或替换多个标签（DEC-070） |
| `UpdateService.Check(ids)` / `Cancel()` / `Items(subId, cursor)` | 检查更新与更新记录（[订阅 §2](subscriptions.md#update-check)，不做已看标记，DEC-033）；`Check` 立即返回，进度经 `subs:check` 事件推送；`ids` 为空表示全部 |
| `UpdateService.CheckPlan()` | 任务页“检查计划”：每个订阅账号上次检查更新的时间与结果、预计下一次定时检查的时间，开播录制订阅的上次开播检查与大约间隔；只读本机状态，不发请求（[订阅 §3](subscriptions.md#schedule)，DEC-059） |
| `UpdateService.CheckAccount(subID, site, uid)` | 任务页“检查计划”每行的“检查”：立即检查一个订阅账号的更新，按手动检查执行（[订阅 §3](subscriptions.md#schedule)，DEC-060 ③） |
| `FollowingService.FollowingGroups(site)` / `FollowingCreatorsPage(site, groupId, page)` / `FollowingCreatorsPageWithCursor(site, groupId, cursor)` / `ImportFollowingCreators(site, creators, rules)`（保留哔哩哔哩既有 `RelationTags` / `RelationFollowings` / `ImportCreators`） | 多站点从登录账号的关注列表导入（哔哩哔哩、微博与 X，抖音暂不支持；微博分组筛选受真实检查开关控制；X 通过 `FollowingCreatorsPageWithCursor` 按字符串游标滚动加载 `Following`、无分组，保留 `UniqueID` 且导入经 `ImportAccountRefs` 零额外请求，[订阅 §5](subscriptions.md#following-import)） |
| `AccountService.AccountSections(site)` / `AccountFolders(site, kind)` / `AccountPage(site, kind, folderId, cursor)` | “我的”（FR-ACCT-001～005、FR-WB-008、FR-X-008，DEC-047、DEC-065、DEC-066）：按站点列出可用功能（未登录或功能不可用时不列；抖音提供“关注”“喜欢”“收藏”“观看历史”（`WatchHistoryEnabled` 经 ACT-026 实测确认后默认开启），微博“赞过”/关注分组、X 书签文件夹与列表仍受真实检查结果开关控制，微博默认提供“关注”与“收藏”，X 默认提供“关注”“书签”“喜欢”），收藏夹/分组列表，以及一页 `{items, cursor, hasMore, total?}`；条目是统一的 `AccountItem{kind: video\|work\|post\|user\|unsupported, site, id, title, cover, author, duration, progress, viewedAt, favAt, invalid, typeLabel, browserURL, cacheKey, post?, xPost?}`。只读、只在界面请求时发出（手动请求，不计规则额度但同样遵守站点间隔、熔断与登录检查；站点熔断中直接拒绝，遇风控或限流记入该站点的自动停止，另外三个站点不受影响），结果只在内存里，不写文件、不进日志（日志只记功能、页数与错误类别）；退出登录或换账号时 `auth:changed` 让界面清空。打开详情仍用 `ParseService.OpenLink` |
| `LiveService.Status(accountKeys)` / `Start(accountKey)` / `Stop(recId)` / `Recordings(filter)` / `LiveRecordingPreview(recKey)` | 开播状态、不限同时路数的直播录制、进行中录制的本机 `.part` 实时预览信息与录制异常事件 `incidents`（[直播](live.md)，DEC-049 ①、DEC-051 ③；微博与 X 账号明确排除，0 请求且录制拒绝并分别提示“微博直播暂不支持录制”“X 直播与 Spaces 暂不支持录制”）；录制也出现在 `CacheService.Tasks()` 里（图标栏“任务”数字角标只统计排队和进行中的下载任务） |

后端通过进程内事件总线（`EventHub`）推送 `cache:changed`（`{key, dot, progress}`，进度至多每秒一次）、`task:changed`、`cache_queue.updated`、`feed:changed`（有新条目入库时）、`auth:changed`、`subs:changed`（订阅增删改）、`subs:check`（检查进度 `{done, total, current, stopped?}`）和 `live:changed`（开播状态与录制状态）。当窗口打开时，界面订阅器通过 `win.Update`（主线程改状态并重绘）更新界面状态；窗口关闭到托盘时，界面取消订阅，事件直接丢弃，不排队、不阻塞（INV-13）。

错误类别取[协议资料的错误分类](../research/bilibili.md#errors)，再加上 `invalid_input`、`unsupported_link`、`storage`（磁盘写入、空间不足）、`conflict`（账号已属于别的订阅）。站点及已分类的业务错误用 `*site.Error{Site, Kind, Code, Message, Err}` 保留类别（`bilibili.SiteError` 是它的别名，旧代码与测试不改；抖音分类见[抖音协议 §9](../research/douyin.md#errors)，微博分类见[微博协议 §9](../research/weibo.md#errors)，X 分类见[X 协议 §10.1](../research/x.md#errors)；`KindOf`/`IsKind`/`SiteOf`/`IsUnsupported` 对 typed-nil `*site.Error` 亦安全返回零值）；进程内存储/校验错误也必须向调用者返回，不能吞成空结果或成功状态。原始响应、签名 URL 和 Cookie 不进入错误消息。

<a id="site-layer"></a>
## 4. 站点客户端

公共层 `internal/site` 只放跨包流动的东西：站点标识（`bilibili`、`douyin`、`weibo`、`x`）与显示名（“B 站”“抖音”“微博”“X”）、`All()` 顺序与 `NormalizeUID`、错误类别、规则请求的上下文标记、账号键 `<site>:<uid>`、直播状态与取流计划（`LiveTransport` 区分 `hls_fmp4` 与 `http_flv`）、媒体传输规格（允许的域名、请求头、首跳精确规则；媒体一律不带 Cookie）、链接分派（分享文本里站内链接与镜像优先于 `t.cn`/`t.co`，纯数字仍归哔哩哔哩，裸 `@handle` 不归 X）。四个站点的浏览数据（视频、动态、作品、单条微博、X 帖子、用户）各用自己包里的类型，服务层按站点分别提供方法，界面按站点渲染各自的页面；下载、录制、订阅与库只经公共层的类型和按站点注册的接口（下载执行器、开播状态与取流、作品列表抓取）使用站点，不直接引用抖音、微博或 X 站点包。哔哩哔哩包保留全部导出与测试，通过薄适配实现这些接口，行为不变。

<a id="proxy"></a>**代理**（DEC-046 ⑤）：四个站点的接口、媒体与直播录制的 `http.Transport` 都用 `site.Proxy`，按“设置 › 网络”的三种方式取代理：跟随系统（默认）、不用代理、自定义（`http://`、`https://`、`socks5://`；`ParseProxyURL` 拒绝带账号密码的地址，`settings.json` 是明文，凭证只进凭证库）。`internal/site` 只用标准库，默认只认 `HTTP(S)_PROXY`；桌面壳在建后端前用 `site.SetSystemProxyFunc(netproxy.Proxy)` 装上 MyGo 的系统代理读取（Windows 读 WinHTTP 的用户代理设置，含 PAC 与自动检测；设了代理环境变量时以环境变量为准）。回环目标始终直连。服务层在启动和每次保存设置时调用 `site.SetProxy`，下一个请求立即生效，不用重启；媒体的防内网检查（`site.GuardedMediaDialer`）对选中的代理地址直接拨号，经代理时仍先检查媒体主机解析出的地址。更新检查仍走 MyGo 的系统代理。

<a id="bilibili-client"></a>
### 4.1 哔哩哔哩客户端

- 一个 `http.Client`：API 请求超时 15 秒，不自动跟随跳转（短链自己处理），公共请求头见协议资料第 2 节。
- 全局 API 请求间隔至少 500 毫秒（互斥锁 + 上次请求时间，可被 `context` 取消）。媒体文件下载不走这个间隔，由下载器的并发上限控制。
- WBI 密钥缓存 30 分钟，-352 时刷新一次；用户页访问标识按作者缓存 30 分钟。
- **只有登录模式**：客户端从凭证模块取 Cookie；取不到时，除扫码的两个 passport 接口和“用候选 Cookie 校验的 nav”外，任何请求在发出前返回 `auth_required`（INV-07）。不获取访客 Cookie。
- 任何接口返回 -101，或 nav 返回 `isLogin=false`：标记凭证 `expired`（不删文件），推送一次 `auth:changed`，异步暂停缓存任务与停止录制。
- 登录与退出后清空 WBI 与访问标识缓存。
- 所有返回体先检查 `code`，再按结构解析；字段缺失按 `schema_changed`。动态 ID 等大整数一律按字符串处理（`id_str`）。

<a id="douyin-client"></a>
### 4.2 抖音客户端

协议见[抖音协议资料](../research/douyin.md)，取舍见其 [§11](../research/douyin.md#a-review)，签名与录制选型见[选型 §4](../research/2026-11-12-douyin.md#a-review)。

- 自己的 `http.Client`：API 超时 15 秒、响应上限 4 MiB，不自动跟随跳转；短链逐跳读 `Location`，最多 5 跳，每跳必须是 HTTPS、无账号密码和端口、主机在抖音域名白名单内，识别出目标就停。
- 一个固定的桌面 Chrome UA；`browser_version` 等公共参数与 `sec-ch-ua` 都从这个 UA 推出，签名用同一个 UA。
- 全局 API 请求间隔 1000 毫秒加 0～300 毫秒随机抖动，串行（NFR-NET-003）；和哔哩哔哩的间隔、额度、退避互不相干。
- **只有登录模式**：Cookie 只从凭证仓取；取不到时任何请求在发出前返回 `auth_required`（INV-07），候选 Cookie 的 `profile/self` 确认除外。确认已登录只看 `profile/self`（[协议 §3.2](../research/douyin.md#auth)）。站点判定未登录时标记抖音凭证 `expired`、推送 `auth:changed{site}`、只暂停抖音的任务与录制。
- 签名在 `site/douyin/sign` 内：输入请求上下文（路径、按发送顺序编码的查询串、请求体、UA、环境串、时钟、随机源、可选 `uifid` 与 `verifyFp`），输出完整查询串与附加请求头，调用方不再二次拼接。站点拒绝签名按 `risk_control` 处理。
- 响应先按[协议 §9.1、§9.2](../research/douyin.md#errors)判空包、验证页、网关拦截与未登录，再解析结构；ID 一律按字符串（`*_str` 或 `UseNumber`）。不确定的空结果不当成“没有内容”。
- 除表单 POST 的“全部收藏”（[协议 §12](../research/douyin.md#account-lists)）外，接口都是 GET；POST 的请求体参与 `a_bogus` 计算，表单体与查询串一样按发送字节签名。
- 媒体与直播 CDN 请求带同一 UA 和对应的 `Referer`，**不带 Cookie**；地址只在内存里，不落盘、不进日志。

<a id="weibo-client"></a>
### 4.3 微博客户端

协议见[微博协议资料](../research/weibo.md)，A 的定稿与取舍见其 [§13](../research/weibo.md#a-review)。

- 自己的 `http.Client`：API 超时 15 秒、响应上限 4 MiB，不自动跟随跳转；`t.cn` 短链不带 Cookie、经 `site.GuardedMediaDialer` 逐跳读 `Location`（最多 5 跳、每跳 10 秒、HTTPS 且在微博允许域内，识别出目标即提前收敛；跳登录/访客页判 `auth_required`，外站或超跳判 `blocked`）；`weibo.cn/sinaurl` 在本机解包、零额外请求。
- 全部接口只走 PC 域（`weibo.com`），按[协议 §2.7](../research/weibo.md#headers) 发送 PC Ajax 头，镜像 Cookie 中的 `XSRF-TOKEN` 为 `X-XSRF-TOKEN` 并在收到新 `Set-Cookie` 时同步更新该上下文字段。
- 全局 API 请求间隔 1200 毫秒加 0～400 毫秒随机抖动，串行（NFR-NET-004）；与哔哩哔哩、抖音的间隔、额度、退避与熔断完全独立。
- **只有登录模式**：Cookie 只从凭证仓 `weibo.bin` 取；取不到时除候选 Cookie 校验外任何请求在发出前返回 `auth_required`（INV-07）。严格两步登录确认（[协议 §13.1 第 2 条](../research/weibo.md#a-review-choices)）：先调 `weibo.com/ajax/setting/getBasicInfo` 要求 `ok == 1` 且 `data.screen_name` 非空，再调 `weibo.com/ajax/profile/info?screen_name=<昵称>` 取 `data.user.idstr` 与头像并严格校验返回的 `screen_name` 与第一步一致（不调用 `m.weibo.cn`，不带 Cookie 或访客 `SUB` 均判未登录）。站点判未登录时标记微博凭证 `expired`、推送 `auth:changed{site}`、只暂停微博的任务与规则请求。
- 响应先按[协议 §9.1、§9.2](../research/weibo.md#errors) 判跳转登录/访客页、HTTP 414/418/429/432、HTML 验证页、0 字节与 `ok`/`code` 信封再解析；不自动重试，`risk_control` 与 `rate_limited` 不清除凭证；`mid`/`uid`/`idstr` 一律按字符串处理，Base62（`mid <-> mblogid`）按 7 位/4 字符整数分段换算不经 `float64`；不存在用户的 `{"ok":1,"data":{}}` / `data.user == null` 归类为 `unavailable` 而非 `schema_changed`；不确定的空列表不当成“没有微博”。
- 媒体 CDN（`WeiboMediaTransport`：`sinaimg.cn`、`weibocdn.com`；实况首跳专用 `WeiboLivePhotoMediaTransport` 仅首跳放行 `video.weibo.com/media/play`）带 `Referer: https://weibo.com/`、**不带 Cookie**、`PublicRedirects` 默认关闭；签名地址（`ssig`/`Expires`/`KID`）只在内存，不落盘、不进日志。

<a id="x-client"></a>
### 4.4 X 客户端

协议见[X 协议与官方 API 资料](../research/x.md)，A 的定稿与取舍见其 [§15](../research/x.md#a-review)，选型见[选型表](../research/2026-11-22-x.md)。当前除 ACT-024 已核对 2 次前端静态获取外，完整线上接口验证仍待 [ACT-025](https://github.com/chialecode/trace/issues/18)。

- 独立 `http.Client`：API 超时 15 秒、响应上限 4 MiB（前端静态脚本上限 8 MiB），不自动跟随跳转；全局串行间隔 `4000 ms + 0～2000 ms` 随机抖动（NFR-NET-005），与哔哩哔哩、抖音、微博的间隔、额度、退避与熔断完全独立。不实现 `x-xp-forwarded-for`。
- **运行时纯内存发现前端元数据（`frontend.go`）**：仓库不存公开网页 Bearer、`queryId`、`features`/`fieldToggles` 键与事务头密钥的真实字面量。需要时单飞依次请求 `x.com/home`（带登录 Cookie，与浏览器一致）与 `abs.twimg.com/responsive-web/` 下的 `main.*.js`、`ondemand.s.*.js`（**不带 Cookie**，仅放行这一主机与路径前缀，单文件上限 8 MiB；这些请求同样计入 X 的串行闸与每小时额度），结果只放内存（默认 TTL 1 小时，因漂移触发的重新发现每 10 分钟最多 1 次）。核心发现提取公开 Bearer、事务头密钥材料及 5 项核心操作（`UserByScreenName`、`UserByRestId`、`UserTweets`、`TweetDetail`、`Following`）；可选“我的 › X”操作（`Bookmarks`、`Likes`）与核心解耦，若被前端拆包在 `main.*.js` 之外，仅在调用对应功能时按需从首页 webpack 分包映射中最多单飞拉取 1～2 个候选懒加载分包（同享无 Cookie、`abs.twimg.com/responsive-web/`、≤ 8 MiB、冷却与额度约束），单个可选操作缺失时仅该操作报 `schema_changed`、不阻塞核心 5 项操作与 X 登录；核心缺失或冷却期内仍漂移时报 `schema_changed` 并只停 X。合成夹具同时覆盖 webpack 与 Vite（`x-web`）产物形态。
- **`x-client-transaction-id`（`txid.go`）**：纯 Go 标准库按首页验证元信息、加载动画 SVG 与 `ondemand.s.*.js` 密钥材料生成（可注入时钟与随机源以通过黄金向量测试），对全部 GraphQL 请求携带。
- **只有登录模式与两步确认**：Cookie 只从凭证仓 `x.bin` 取，取不到时除候选校验外任何请求在发出前返回 `auth_required`（INV-07）。白名单保留 `auth_token`、`ct0`、`twid`、`guest_id`、`kdt`，本地先查 `auth_token` 与 `ct0` 非空，再按两步严格确认（`GET api.x.com/1.1/account/settings.json` 不跟随跳转 → `UserByScreenName` 核对大小写无关一致的 `screen_name` 及 `twid` 解码出的 `rest_id`；不带 Cookie 与只带访客字段均判未登录）。只在响应带合法 `Set-Cookie: ct0` 时被动同步保存与 `x-csrf-token`（遇到 `353` 且带新 `ct0` 时只重放一次，不自造 `ct0`）。站点判未登录时标记 X 凭证 `expired`、推送 `auth:changed{site}`、只暂停 X 的任务与规则请求。
- **主动刹车、短链与媒体**：只使用 7 项只读操作（`UserByScreenName`、`UserByRestId`、`UserTweets`、`TweetDetail`、`Following`、`Bookmarks`、`Likes`）；ID 一律按字符串（`UseNumber`，水位比较用 `uint64`）。按操作名记录内存 `x-rate-limit-*` 状态，`remaining <= 1` 时到 `reset + 0..60s` 抖动前不再请求该操作（`RateLimitError.RateLimitResetAt()` 联动订阅调度器）；遇 `64`/`226`/`326`/验证/假空壳 `{"data":{"user":{}}}` 零重试立即停 X 且不清除凭证。`t.co` 短链不带 Cookie、经 `site.GuardedMediaDialer` ≤ 5 跳（每跳 10 秒、终主机限 X 域）；第三方嵌入镜像仅本机改写为 `x.com`（0 请求）。媒体 CDN（`DefaultXMediaHosts()`：`pbs.twimg.com`、`video.twimg.com`）不带 Cookie、不带 `Referer`、`PublicRedirects` 默认关闭；缓存目录为 `x/<rest_id>/{posts,forwards}/<status_id>/`（见[数据 §2.1](data-and-cache.md#cache)）。

<a id="credentials"></a>
## 5. 凭证

- **按站点分开**（DEC-043、DEC-065、DEC-066）：`internal/credential.Manager` 为每个站点管一个凭证文件与加密熵——`credentials/bilibili.bin`（原有文件与熵不变，旧凭证照常读取）、`credentials/douyin.bin`（熵 `trace:douyin:credential:v1`）、`credentials/weibo.bin`（熵 `trace:weibo:credential:v1`）、`credentials/x.bin`（熵 `trace:x:credential:v1`）。状态、失效标记、退出都按站点；脱敏（`RedactString`）覆盖所有四个站点当前的 Cookie 值与敏感字段。只有 `internal/credential` 与对应站点的协议包接触明文。
- **抖音手动凭证**：输入规则同下（8 KiB、`Cookie:` 前缀、`;` 或换行）；按[抖音协议 §3.1](../research/douyin.md#auth)保留身份与访问上下文字段、丢弃浏览器动态票据；`sessionid` 与 `sessionid_ss` 都没有就直接拒绝；用 `profile/self` 确认后保存 `sec_uid`、`uid`、昵称、头像、抖音号；不续期（没有 `refresh_token`）。错误不回显输入，哨兵测试覆盖。
- **微博手动凭证**：输入规则同上（8 KiB、`Cookie:` 前缀、`;` 或换行，拒绝控制字符与换行注入、错误不回显）；按[微博协议 §2.2](../research/weibo.md#cookie-clean) 白名单保留 11 个登录与上下文字段（`SUB`、`SUBP`、`ALF`、`SCF`、`WBPSESS`、`XSRF-TOKEN`、`SSOLoginState`、`SUHB`、`PC_TOKEN`、`MLOGIN`、`_T_WM`）、剥离统计与界面字段；没有 `SUB` 直接拒绝；经 PC 两步严格确认（`getBasicInfo` → `profile/info?screen_name=` 且昵称严格匹配）后保存 `uid`、昵称、头像，`source = "manual"`，`Renewal()` 固定不可用（原因“粘贴的 Cookie 不续期”）；响应带新 `XSRF-TOKEN` 时只同步这一个上下文字段。`RedactString` 覆盖[协议 §2.2](../research/weibo.md#cookie-clean) 列出的全部 14 类敏感字段（含 URL 编码与 JSON 形态），哨兵测试覆盖粘贴、校验、浏览、缓存、订阅检查与远程推送。
- **X 手动凭证**：文件 `<数据目录>/credentials/x.bin`、熵 `trace:x:credential:v1`；输入规则同上（8 KiB、`Cookie:` 前缀、`;` 或换行，拒绝控制字符与换行注入、错误不回显）；按[X 协议 §15.1 第 6 条](../research/x.md#a-review-choices)白名单仅保留 `auth_token`、`ct0`、`twid`、`guest_id`、`kdt`、剥离广告与 `__cf_bm` 等临时字段；缺少 `auth_token` 或 `ct0` 在本机直接拒绝（0 请求）；经两步严格确认（`settings.json` → `UserByScreenName` 且核对 `screen_name` 与 `twid`）后保存 `rest_id`、`screen_name`、昵称、头像，`source = "manual"`，`Renewal()` 固定不可用（原因“粘贴的 Cookie 不续期”）；响应带合法新 `ct0` 时只同步该字段。`RedactString` 覆盖白名单全部字段以及 `x-csrf-token`、`Authorization` 头的值（含 URL 编解码与 JSON 形态），哨兵测试覆盖粘贴、校验、浏览、缓存、订阅检查、“我的”与远程推送。
- 文件 `<数据目录>/credentials/bilibili.bin`：JSON（`cookies`、`refresh_token`、`mid`、`obtained_at`、`source: "qr"|"manual"`、`renewed_at`、`confirm_pending`、`pending_old_refresh_token`）经 DPAPI（当前用户范围）加密后原子写入。不用 `mygo.Secrets`（[选型研究 §3](../research/2026-11-05-stack.md#other-choices)）。
- 新写入采用上述 snake_case 字段，读取兼容旧 camelCase。普通替换/状态更新/退出均在成功落盘后才切换内存，失败保留原凭证并返回错误；已被站点确认失效时，即使失效标记无法落盘，也在内存阻止继续使用，原密文保留。
- 只有 `internal/credential` 和对应站点的协议包（`internal/site/{bilibili,douyin,weibo,x}`）接触明文；服务层只返回账号摘要。
- 日志里涉及请求时只记录路径和 `code`，不记录查询串和 Cookie。
- 日志在 `<数据目录>/logs/trace.log`。下载任务的进度更新每秒一次，日志只记状态变化（排队、进行中、暂停、完成等），不重复记同一状态。界面上显示给用户的错误都同时写一行 `ERROR ui: …`（`LogUserError`，同一行 1 分钟内只写一次）；站点错误附接口路径（不含查询串）与字段解析失败的原因，后台请求失败（如解析用户页时的动态）也写日志（2026-11-16 反馈）。程序崩溃时 Go 运行时把调用栈写进 `logs/crash.log`（`runtime/debug.SetCrashOutput`）；下次启动时非空的 `crash.log` 改名为 `crash-<UTC 时间>.log`（只留最新 5 份），并在 `trace.log` 记一行“上次运行崩溃”。崩溃文件只在本机，不随诊断信息外发（2026-11-15 日志检查）。
- 扫码流程：每个流程有独立 ID 和 180 秒有效期；取消、过期或新流程开始后，旧流程的轮询结果一律丢弃；同一流程同一时间只有一个站点轮询。
- **手动凭证**：接受浏览器复制的 Cookie 请求头文本（可带 `Cookie:` 前缀，`;` 或换行分隔），上限 8 KiB。只保留 `SESSDATA`、`bili_jct`、`DedeUserID`、`DedeUserID__ckMd5`、`sid`；缺 `SESSDATA` 直接拒绝；用 nav 确认 `isLogin=true`，有 `DedeUserID` 时必须等于 nav 的 `mid`；通过后保存，失败不保存任何内容，也不改变已有凭证和状态；有扫码流程进行时，手动登录成功会取消该流程。输入框提交后立即清空字符串状态；原文只在这一次 Go 方法调用的内存里，不写日志，错误消息不回显。哨兵测试覆盖这条路径（INV-01）。

<a id="renewal"></a>
### 5.1 登录续期（FR-AUTH-003）

- 只对 `source=qr` 且有 `refresh_token` 的凭证续期（粘贴 Cookie 的登录不续期，DEC-031；`AuthService.Renewal()` 返回 `available=false, reason="manual"`）。
- 时机：启动后 1 分钟（或扫码登录确认后 1 分钟），以及应用运行期间每 12 小时；先问 `cookie/info`，`refresh=false` 就只记录检查时间。计入规则请求的每小时 200 次额度，退避期内跳过（[订阅 §3](subscriptions.md#schedule)）。
- 流程按[协议资料 §17](../research/bilibili.md#cookie-refresh)：生成 `correspondPath`（RSA-OAEP SHA-256 加密 `refresh_<timestamp>`）→ 取 `refresh_csrf` → `cookie/refresh`（传入旧 `refresh_token` 与旧 `bili_jct`）→ 用 nav 确认新 Cookie（`isLogin=true` 且 `mid` 不变）→ 加密原子替换凭证文件（新 Cookie + 新 `refresh_token`，`renewed_at`，`confirm_pending=true`，`pending_old_refresh_token=旧refresh_token`）→ 用**新 Cookie（新 `bili_jct` 作为 `csrf`）+ 旧 `refresh_token`** 调用 `confirm/refresh` 成功后清掉 `confirm_pending` 与 `pending_old_refresh_token`。下次续期检查时若 `confirm_pending` 仍在，先用保存的 `pending_old_refresh_token` 补做确认。
- 续期期间客户端持有互斥锁：续期只用一次单独的请求序列，不和其他请求交错。
- 失败处理：网络错误、风控、`schema_changed`（取不到 `refresh_csrf` 等）→ 旧凭证不动，记 `lastError`，下个周期再试，不打断使用；-101 或 86095 → 按“登录已失效”处理（[§4](#bilibili-client)）。任何失败都不写半份凭证。
- 刷新令牌与新旧 Cookie 只在 `internal/credential` 与 `internal/site/bilibili` 里出现；日志只记步骤名与 `code`（NFR-SEC-002，哨兵测试覆盖续期路径）。

<a id="frontend"></a>
## 6. 原生界面（`internal/ui` + `internal/player`）

- **路由状态机**：`/login`（先选站点：四段式切换“哔哩哔哩 / 抖音 / 微博 / X”，哔哩哔哩有扫码与粘贴凭证两个标签，抖音、微博与 X 只有粘贴凭证，X 段提交按钮上方显示风险说明）、`/douyin/work/:awemeId`、`/douyin/user/:secUid`、`/weibo/post/:mid`、`/weibo/user/:uid`、`/x/post/:id`、`/x/user/:uid`、`/` 主页信息流、`/subscriptions`（圆形头像网格）、`/subscriptions/import`（从关注列表导入，支持哔哩哔哩、微博与 X 切换）、`/subscriptions/:id?tab=content|accounts`（默认 `content`，旧 `live` 兼容映射为 `accounts`，DEC-051 ⑦；从订阅详情打开账号主页后再点返回，按导航返回栈回到这个 `/subscriptions/:id` 并保留原标签）、`/mine` 我的、`/parse` 解析页、`/video/:id`、`/user/:mid?tab=dynamic|videos`、`/library?type=&sub=&mid=`、`/library/item?key=...`（媒体库纯离线本机详情，DEC-049 ③）、`/tasks`（下载任务 / 缓存队列 / 直播录制 / 录制异常 / 检查计划 / 导入导出，DEC-050 ②、DEC-051 ③、DEC-059 ①、DEC-062）、`/settings/:category`（默认 `account`，含 `remote` 远程、`logs` 日志与 `update` 更新分类，DEC-051 ①、DEC-062）。一个站点都没有有效登录时，除 `/login`、`/settings/*` 外都转到 `/login`；某个站点登录失效（`auth:changed{site}`）而其他站点仍有效时，只有当前页面属于失效站点（这个站点的详情、用户页）才转到登录页并预选这个站点，其他页面留在原处并在顶部提示一次；全部已登录站点都失效时转到 `/login`，关闭看图/播放层并退出原生全屏，记住原路由，重新登录后自动回到原路由，之前已加载的页面状态保留不清空（AC-08、FR-AUTH-005）。
- **图标栏 + 上下文侧栏 + 页面（NFR-UI-001、DEC-039、DEC-049、DEC-050、DEC-051、DEC-069）**：窗口用 MyGo `TitleBarHidden`（DEC-046 ④）：内容延伸到窗口顶部，系统窗口按钮盖在右上角；`View` 每帧读 `c.TitleBar()`，页面顶部栏右侧按按钮宽度让位并 `DragWindow()`，图标栏与侧栏顶部一条与按钮同高的透明拖动条（`窗口拖动区`），看图层与播放层面板上边距不小于按钮高度加 8（`mediaPanelRectBelow`）；测试、全屏与非沉浸窗口里 `TitleBar()` 为零，布局不变。顶层 `ui.Row` 依次是图标栏（`renderRail`，52px，“任务”数字角标只数排队和进行中的下载任务）、上下文侧栏（`renderContextPanel`，主页/订阅/我的/解析/视频/用户/媒体库及 `/library/item`/任务页 ≥ 1280px 为 272px、1000～1279px 为 240px；设置 ≥ 1000px 为 232px、更窄 72px；720～999px 不显示）和页面（`renderCenterColumn`，帖子类页面限宽 680px、设置 860px，解析主面板与媒体库等网格占满）。宽度规则在 `internal/ui/shell.go`。图标栏与侧栏是固定高度容器，只有页面内部的 `ui.List` 或 `ui.ScrollView` 纵向滚动；页面滚到任意深度时图标栏与侧栏的坐标盒不变，全窗口无横向溢出。页面顶部栏（返回按钮、页面标题、页面操作；不含快速解析框，DEC-069）不随内容滚动；没有侧栏时，主页侧栏的订阅列表退回为时间线上方的横向头像条（只构建滚到的条目），媒体库的类型与来源退回为页面里的标签与“作者筛选”，解析页在大输入框下方显示“最近解析”，任务页四组切换退回为页面顶部的分段选择。
- **虚拟列表与非阻塞刷新（NFR-PERF-001、NFR-UI-002）**：主页信息流、用户动态与投稿列表、媒体库网格、订阅圆形头像网格、**下载任务列表（`tasks_downloads_view.go` 固定批量操作栏 + `ui.List` 虚拟化）**均使用 `mygo/ui` 的 `ui.List` 虚拟化渲染，只构建可见区域的条目节点；下载任务与队列卡片封面采用 256px 缩略图与 `GetLibraryItemMetaOnly`，按 `libraryRev` 缓存（上限 500 项），不随每秒一次的下载进度事件清空重建；单条暂停/继续/重试/取消/移除只刷新任务列表，不触发全库重扫。
  - 主页信息流固定按从新到旧排列（无顺序切换），支持双向游标翻页；顶部栏提供纯图标离线“刷新”按钮（`Label("主页刷新")`，在后台协程调用无锁扫描增量更新的 `RefreshLibrary` 并一并预读刷新后的信息流、统计、订阅与作品数，再以单次轻量快照切换更新界面，不阻塞主线程与并发读取、零网络请求、不弹窗；非媒体库路由收到 `EventLibraryChanged` 时仅标脏，进入媒体库时再懒加载）；每个站点筛选记阅读位置，启动后第一次显示时回到那条与偏移（`internal/ui/home_position.go`：`restoreHomePosition`、`stepHomeRestore`、`noteHomePosition`、`flushHomePosition`，DEC-053 ①），不提示“N 条更新的内容”；看过的最新一条仍由 `SaveAnchor` 记下（DEC-040）；帖子显示数据按条目缓存并用 `GetLibraryItemMetaOnly` 避免逐帧磁盘 `EvalSymlinks`，本机图片后台解码缩放，重绘不逐帧读媒体库；侧栏订阅列表（`internal/ui/home_subs.go`：`homeSubscriptionRows` 按签名缓存行、进入主页时定顺序快照，`ui.List` 虚拟化）把正在直播的排最前，未读带圆点，点击进入单订阅已缓存视图并写 `homeSeen["sub:<id>"]`（DEC-053 ②）。主页帖子不放底部文字按钮，点图片看原图、点视频卡片播放、点头像进用户页。
  - 订阅详情改为“内容”（默认，合并该订阅全部跨站关联账号已缓存内容的信息流，与主页时间线同源、只读本机不联网）和“账号”（合并原“直播”，每行同时显示账号信息、直播状态与录制入口）两个标签（DEC-051 ⑦），并在头部展示当前开启的缓存能力标签胶囊（无标签时显示“无缓存标签”）与标签下拉菜单以直接开关标签（DEC-070）；“设置 › 缓存标签”列出全部 7 项原子能力标签，既可点每行的“管理订阅…”通过 `SetTagSubscriptions` 按单标签批量勾选订阅（DEC-051 ⑧），也可点“批量设置标签…”（或订阅列表右上角“⋯ → 批量设置缓存标签…”）通过 `BatchUpdateSubscriptionsTags` 跨多个订阅一次写入批量添加、移除或替换多个标签（DEC-070）。不做已看/未读标记或图标栏未读数字角标（DEC-033）。
  - 解析页侧栏（与视频页、用户页共用“最近解析”侧栏，DEC-049 ②）显示本机缓存的封面/头像缩略图，点条目本身打开详情，单条操作收进右侧“⋯”菜单（在浏览器中打开、删除），标题栏“⋯”提供“全部清空”（二次确认），主面板只留居中大输入框；媒体库固定按发布时间从新到旧排列（无缓存时间排序，DEC-049 ③），每格只保留缩略图、一行标题、一行元信息和右下角“⋯”菜单（详情、在资源管理器中显示、删除），“重建索引”收纳在设置“存储”分类（DEC-037）。
  - 媒体库是独立的离线本机内容（DEC-049 ③）：点卡片直接播放本机媒体或打开看图层（不联网、不改路由到解析页、不写最近解析），“⋯ → 详情”打开纯离线本机详情 `/library/item`（只用 `LibraryService.Item` / `MediaPath` 读本机清单与文件，不调任何站点接口；只有点页面右上角“⋯ → 在解析页打开”才进入对应解析页并发网络请求）。哔哩哔哩、抖音、微博与 X 用户页“⋯”均提供“全部缓存”（已有未结束队列时显示“在缓存队列中查看”，DEC-050 ①）。任务页侧栏在“下载任务（N）”“缓存队列”“直播录制（N）”“录制异常（N）”“检查计划”“导入导出（N）”六组间切换（DEC-050 ②、DEC-051 ③、DEC-059 ①、DEC-062；“录制异常”统计最近 7 天有异常的场数，为 0 时不带数字，已结束场次点开走 `/library/item` 纯离线详情）；“直播录制”展开或点“实时预览”时在页内只读打开正在写入的本机 `.part`（不发额外站点连接，不显示实时弹幕，离开“直播录制”、切到“录制异常”等其他组或停止录制时同步释放文件与解码器句柄，DEC-049 ①）。
- **看图层（FR-IMG-001、DEC-033、DEC-040）**：在顶层 `ui.Overlay` 上画半透明遮罩与居中的深色圆角面板（`mediaPanelRect`，点遮罩关闭）；已缓存图片通过 `LibraryService.MediaPath` 读取本机原图，未缓存图片通过无 `Referer`（且 `http://` 转 `https://`）的加载器读取去掉尺寸后缀的原图。只保留少量半透明圆形悬浮按钮（右上角关闭、左右上一张/下一张、底部适应窗口/1:1），鼠标静止约 2 秒后自动隐藏，鼠标移动或按键时重新出现；支持滚轮、快捷键（`+`/`-`/`0`）、双击缩放、按住拖拽平移、左右键切换与 `Esc` 关闭。实况照片、图文混排视频页与 X GIF（MP4 静音循环）都在看图层里原地播放（`startViewerInlinePage`，独立的 `viewerPlayer`）：已缓存的读本机片段，未缓存的经 `StagePreviewMedia(站点, 地址)` 取到临时文件（哔哩哔哩、抖音、微博与 X 按 `ImageViewer.InlineSite` 区分），DEC-046 ①。
- **沉浸式播放层与弹幕字幕（FR-LIB-002、FR-LIB-004、DEC-033）**：
  - 点击已缓存视频或直播录像打开播放层（与看图层相同的浮动面板，全屏时铺满窗口；打开即自动播放，DEC-040），通过 `LibraryService.MediaPath` 获取受白名单保护的本机 `.mp4` 路径，交给 `internal/player`（Windows Media Foundation `IMFSourceReader` 解码音视频 + `mygo/audio` 播放声音 + `*ui.Bitmap` 渲染视频帧）。纯音频文件（如图文作品配乐 `music.*`，`HasVideo=false`）由同一个 `Controller` 只开音频读取器与输出、不启动视频后台线程，含视频轨的文件仍走原有音视频双读取器路径；看图层播放图文视频页或实况片段时自动暂停配乐条。底部悬浮条只保留圆形播放/暂停、进度条拖动（Seek）、音量、全屏，右上角保留圆形“⋯”更多菜单与关闭；弹幕、字幕、多段切换（“上一段 / 下一段”）、“在文件夹中显示”和“用系统播放器打开”收进“⋯”菜单。鼠标静止约 2 秒后悬浮控件自动隐藏，鼠标移动或按键时重新出现。
  - **拖动与跳转**：界面线程不调用解码器。拖动进度条时 `Controller.Preview` 先暂停声音，只把目标时间交给视频后台线程，后台用 `PreviewFrameAt` 解出目标附近的关键帧显示；松手（或单击）才 `Seek` 一次。`Seek` 只记录目标并请求新画面，声音输出线程随后只移动音频读取器（`splitDecoder.SeekAudio`），不再在两个线程各移动一次两个读取器。播放中跳转时声音先停住，等新位置第一帧画面出来（最长 1.5 秒）再继续，免得声音先跑、画面追赶。`internal/player/seek_test.go` 与真实 Media Foundation 的 `TestPreviewAndAudioSeekOnMediaFoundation` 覆盖。
  - 弹幕叠加层（`internal/player/danmaku.go`）：解析缓存的 `.danmaku.xml`，按当前播放时间 `t` 渲染滚动（mode 1～3）、底部（mode 4）、顶部（mode 5）弹幕；“⋯”菜单提供弹幕开关、透明度滑块（20%～100%）、显示区域与密度选择（低 / 中 / 高 / 不限）。密度按原时间线每秒稳定选取最多 2 / 5 / 10 条或不限，轨道仍防重叠；拖动进度后按新时间窗重排，无残留。偏好写入 `ui-state.json`，重新打开播放器和重建窗口时恢复。
  - 音量按钮打开 0～100% 滑块与静音开关，调整立即作用于播放器并保存偏好。全屏按钮经 `internal/shell` 绑定到 MyGo 窗口的 `SetFullScreen`；切分段保持全屏，关闭播放、登录失效或关闭窗口时恢复窗口状态。
  - 图片加载在后台使用取消上下文与递增请求版本，图片未加载完时也可关闭；迟到结果不能重新打开媒体层或覆盖新图片。当前请求只更新图片与错误信息，保留加载期间的缩放、平移和选中序号。
  - 字幕叠加层（`internal/player/subtitle.go`）：读取缓存的 `.vtt`，“⋯”菜单提供字幕语言选择（AI 字幕标明“AI”，含“关闭”）；没有弹幕或字幕时对应控件禁用并显示原因说明。当前播放偏好统一写入 `ui-state.json.player`，不从 `settings.json.player` 读取。
- **设置日志与诊断**：诊断与日志读取在后台执行，每秒最多一次，界面重绘使用缓存。服务层从日志尾部最多读 1 MiB、返回最多 500 行，写入与展示使用同一脱敏边界（包括凭证字段、JSON/编码字段与带查询串的 URL）。诊断显示真实索引总量、当前检查进度与读取失败原因；日志读取失败保留上次可读内容。
- **输入法组合输入（NFR-UI-001）**：所有单行输入框（解析页、添加订阅、关联账号、搜索框）基于 `ui.TextInput` / `ui.SearchField`，Windows IME 组合输入期间（`composing` 状态）按回车只确认候选词，不触发提交；非组合态按回车才提交。手动凭证输入框使用密码掩码（`Secure(true)`），提交后立即清空。
- **视觉与无障碍**：严格遵循[界面规范](../ui.md#visual)的色值（`internal/ui/theme.go`：页面/侧栏/图标栏 `#ffffff`/`#f8f8f6`/`#f1f1ee`、主文字 `#262626`、次文字 `#737373`、强调色蓝 `#1a73e8`（`TRACE_UI_ACCENT=green` 预览绿 `#0a8754`，Q-011）、直播橙 `#c2410c`、状态圆点绿 `#2aae5b` / 黄 `#f3a115` / 红 `#d91f37`）、字号与圆角；界面字体 Inter 4.1 可变字体经 `go:embed` 打包，启动时 `ui.RegisterFont` 注册（`internal/ui/font.go`），中文由 DirectWrite 回退到系统字体；所有图标按钮、状态圆点、输入框与对话框均用 `.Label(…)` 设置无障碍名称（测试用 `tt.AccessByRole` 核对角色与名称），支持键盘 Tab 焦点环与快捷键。

<a id="invariants"></a>
## 7. 不变量

| 编号 | 不变量 |
| --- | --- |
| INV-01 | 凭证明文只存在于 Go 进程内存；手动凭证只从输入框经一次调用单向传入并立即清空输入框。界面状态、日志（含 MyGo 运行日志）、历史记录、清单、索引、错误消息里没有凭证 |
| INV-02 | 风控、限流、未登录、网络、不可得、格式变化、存储失败是不同的错误类别，任何一类都不能显示成空结果或“已缓存” |
| INV-03 | 已取消、已过期或被新流程替代的扫码流程不能写入凭证；无效的手动凭证不能写入 |
| INV-04 | 数据文件只通过“临时文件 + 改名”写入；损坏的文件被保留而不是覆盖；索引数据库可以从清单完整重建 |
| INV-05 | `internal/site`（含各站点子包）、`internal/download`、`internal/library`、`internal/credential`、`internal/storage`、`internal/subscription`、`internal/live`、`internal/settings`、`internal/uistate`、`internal/appdirs`、`internal/update`、`internal/service` 不依赖 MyGo；`internal/site` 及各站点子包也不依赖存储、缓存、订阅和录制代码，站点子包之间互不引用 |
| INV-06 | 只有全部文件校验通过、改为正式名并写好清单的条目才标为已缓存（绿点）；其余状态都带原因 |
| INV-07 | 某个站点没有有效登录凭证时，不向这个站点的内容接口、用户主页和媒体 CDN 发任何请求（只有哔哩哔哩扫码接口、候选凭证的 nav、`profile/self`、微博 PC 两步 `getBasicInfo`/`profile/info`、X 两步 `settings.json`/`UserByScreenName` 校验及无 Cookie 的 `abs.twimg.com/responsive-web/` 前端静态脚本例外）；媒体、短链、前端静态脚本与直播 CDN 请求从不带 Cookie |
| INV-08 | 本地媒体读取（`LibraryService.MediaPath`）只返回索引登记、位于条目文件夹内的真实文件（拒绝 `..`、符号链接/目录联接与跨条目路径）；签名播放地址、直播流地址与字幕 `auth_key` 地址不落盘、不进日志 |
| INV-09 | TRACE 只删除自己登记过的文件和变空的文件夹，从不递归删除条目文件夹或保存位置；录制中的条目不可删除 |
| INV-10 | 规则与后台队列触发的请求（定时检查、开播检查、自动缓存、缓存队列翻页、续期、X 前端元数据发现）只在应用运行、这个站点已登录、对应设置或队列任务开启、这个站点不在退避期、没超过这个站点的每小时额度（哔哩哔哩 200 次、抖音 120 次、微博 100 次、X 60 次）时发出；一个站点的退避、熔断与额度不影响另外三个站点 |
| INV-11 | 删除订阅、解除关联不删除任何缓存文件；TRACE 不调用站点的写操作接口（关注、取关、点赞、评论等），登录、续期接口除外 |
| INV-12 | 检查更新从不把一个账号第一次检查前就存在的动态放入更新列表（第一次检查只建立起点，起点之前的动态不进入更新，DEC-033）；更新记录丢失时回到“未建立起点”，不会一次冒出大量历史条目 |
| INV-13 | 窗口关闭或重建不影响后端工作：任务、录制、调度与续期的状态只在后端服务层；没有窗口时推送的事件直接丢弃，不排队、不阻塞；重新打开的窗口从服务重新读取状态 |
| INV-14 | 数据目录同时只被一个 TRACE 进程打开（数据目录独占锁）；TRACE 不在 `%APPDATA%`、用户主目录和安装目录里写数据 |

<a id="tray"></a>
## 8. 窗口生命周期、托盘与单实例（DEC-030）

- **单实例**：`main.go` 先 `mygo.App.SetName("trace"|"trace-dev")`（锁按应用名与标识区分，两个通道互不影响），再调用 `mygo.App.RequestSingleInstanceLock()`；不是第一个实例就退出，第一个实例在 `OnSecondInstance` 里打开（或重建）窗口并置前。另外在数据目录里用 `app.lock`（Windows `LockFileEx` 独占锁）做数据目录锁：拿不到锁时返回明确错误后退出，不打开任何数据文件（INV-14）。
- **托盘**：`WhenReady` 里用 `mygo.NewTray` 建图标（`ID: "<appName>-tray"`，32×32 的 PNG 由 `internal/brandicon` 绘制：深灰底 `#3c4043` 白色 “T”，与应用图标一致，开发版与测试身份右下角带琥珀色 “DEV” 角标，有进行中任务或录制时也不加活动圆点，[DEC-072](../decisions.md#dec-072)），悬停提示固定为应用显示名（`"TRACE"` / `"TRACE Dev"`，不被动拼接缓存或录制计数），左键单击 → 打开窗口；Windows 下右键在指针处打开系统原生菜单（`TrackPopupMenuEx`，系统菜单字体，DEC-050 ③），只有“打开 TRACE”（开发版为“打开 TRACE Dev”）和“退出”（其他系统沿用 MyGo 菜单的同样两项）。
- **关闭到托盘**：注册 `App.OnWindowAllClosed` 让进程在没有窗口时继续运行。原生窗口的 `OnClose` 里：设置 `closeAction=tray`（默认）时不阻止窗口关闭，原生窗口销毁，同时释放 `internal/ui` 与 `internal/player` 持有的图片缓存、视频位图纹理和事件订阅，调用 `runtime.GC()` 与 `debug.FreeOSMemory()`（NFR-RES-001）。第一次关到托盘时用 MyGo 的 `Notification` 发一条系统通知（记在 `ui-state.json` 的 `trayNotified`），之后不再重复提示。`closeAction=quit` 时关闭窗口等同托盘“退出”。
- **重新打开**：按 `ui-state.json` 记下的最后路由和窗口位置大小调用 `mygo.NewWindow` 新建原生窗口；主页各站点筛选回到记下的阅读位置（DEC-053 ①）。界面挂载时从 `internal/service` 重新读取任务、订阅、直播与登录状态（INV-13）。
- **退出**：托盘“退出”或窗口里的退出：有排队、进行中的任务或进行中的录制时先确认（有窗口时在窗口里弹确认框；没有窗口时托盘菜单的“退出”变成子菜单“退出（会暂停 N 个任务、结束 M 场录制）”，再点一次才退出）。确认后：任务改为“已暂停（应用关闭）”，录制按[直播 §2](live.md#recorder)收尾（限时 30 秒），然后 `App.Quit()`。
- **后台节律**：没有窗口时，调度器仍是每分钟醒来一次；没有任务、录制和到期的规则时不发请求、不写盘。
- **启动参数**：`--background` 启动时不开窗口、只在托盘；`--quit-for-update` 交给运行中的 TRACE（第二个实例经单实例通道转发），它不弹确认，按托盘“退出”的同一条路径保存（任务改为“已暂停（应用关闭）”、录制收尾、保存路由与窗口状态），在 `ui-state.json` 记下 `update.restart`（原因、窗口是否打开、时间）后退出；没有运行中的 TRACE 时这个参数什么都不做。下次启动若读到 5 分钟内的这条记录，就按记录决定开窗口还是留在托盘（优先于 `--background`），然后删掉记录。

<a id="updates"></a>
## 8.1 更新（FR-UPD-001～004，DEC-042、DEC-051 ①）

- **分层**：检查与下载的状态机、更新源校验及检查记录在 `internal/update`（纯 Go，可测试，不依赖 MyGo）；`internal/shell/updates.go` 把它接到 `mygo.Updater`（`SetSource` 换成 TRACE 的更新源，`Check`/`Install`/`App.Relaunch`）并在启动时按版本变化写入更新记录；`internal/ui` 只经 `UpdaterAPI` 与服务接口读状态、读检查/更新历史与发命令。
- **只在正式版启用联网更新**：只有 `tools/release` 的正式版构建带 `updates`（MyGo 注入清单地址与公钥）和 TRACE 自己的同一份地址与公钥（`internal/update.feedURL`/`publicKey`）；开发版与源码运行没有，`Enabled()` 为假，不在“设置 › 更新”显示检查按钮与开关，而是说明开发版由 Agent 打包后自动安装并重开，同时只读查询卸载项 `HKCU\...\Uninstall\io.github.chialecode.trace-dev` 的 `DisplayVersion` 展示“本机安装的开发版”（与正在运行的版本不同时提示重启生效）；“设置 › 关于”改为“版本与更新见‘更新’”入口。trace-test 身份（只在演练里构建）另外可以用环境变量 `TRACE_TEST_UPDATE_URL` 指到本机回环地址上的假发布源，并在启动 3 秒后检查；正式版与开发版不读这个变量。
- **检查与检查记录**：启动 1 分钟后一次，之后每 24 小时一次（`State()` 暴露下次自动检查时间 `NextCheck`；“自动检查更新”关掉就不检查）；“设置 › 更新”的“立即检查”触发手动检查。请求经系统代理只访问 GitHub（清单 `releases/latest/download/update-windows-amd64.json` 与它列出的文件），只带版本与平台。每次检查结束时 `update.Controller` 往 `<数据目录>/update-history.json` 追加一条检查记录（保留最近 20 条：时间、自动/手动、脱敏结果文字如“已是最新版本”“发现 x.y.z”“检查失败：<原因>”，严禁存下载地址；开发版不联网检查、不写检查记录）。自动检查失败时静默，只写日志与检查记录；手动检查在页面显示原因。
- **校验**（MyGo 之外 TRACE 补上的）：所有请求与重定向都必须是 HTTPS（只有 trace-test 的假发布源可以是回环地址上的 HTTP）；清单最大 1 MiB、字段齐全、版本格式正确、大小在 1 GiB 内、地址都是 HTTPS；清单的 `traceAttestation` 是更新密钥对“`trace-update-v1`、版本、更新包签名”的签名——MyGo 的签名只覆盖更新包内容、不含版本号，这一条防止把旧版本的更新包标成新版本号送来；只下载清单里列出的文件，边读边核对大小与签名，不符就在读到结尾前失败。MyGo 自己核对 Ed25519 签名、差分包的起止版本，并只接受比当前新的版本。任何一条不符都拒绝，保留现版本；手动操作时显示“更新包校验失败，已保留当前版本”，日志写原因。
- **下载与替换**：“自动下载更新”开着（默认）时，发现新版本就在后台 `Install`：下载、校验、解包，把程序文件换成新版本（正在运行的程序被改名放在一边，下次启动时删除）；新版本在下次启动时运行。关着时只提示“有新版本”，用户点“下载并安装”才下载。就绪的版本与说明记在 `ui-state.json`（`update.readyVersion`、`readyNotes`）。
- **重启以更新**：没有缓存与录制时直接按托盘“退出”的路径收尾并 `App.Relaunch`；有时先确认“重启会暂停 N 个缓存任务并结束 M 场录制，重启后可以继续缓存。”。
- **更新后启动与更新记录写入时机**（DEC-051 ①）：启动时 `DecideStartup` 发现有效版本变化（上次运行版本 `update.lastRunVersion` 与当前版本均非空、均非未注入占位，且两者不同）时，向 `update-history.json` 追加一条更新记录（保留最近 50 条：时间、旧版本、新版本、方式，不含下载地址）——开发版通道（`channel == "dev"`）记“本机打包安装”（`dev_install`）；正式版若本次启动消费了有效的 `ui-state.json` `update.restart` 标记则记“自动更新”（`auto_update`），否则记“手动安装”（`manual_install`）。正式版版本比 `update.lastRunVersion` 新时显示一次“已更新到 0.1.x”，可以展开更新内容；同时把本机卸载项（`HKCU\...\Uninstall\<标识>`，只在它的 `InstallLocation` 就是这个程序所在目录时）的 `DisplayVersion` 改成新版本，“设置 › 应用”显示的版本随之正确。
- **数据结构版本**：启动时先核对数据文件的结构版本（[数据 §1](data-and-cache.md#schema-versions)）；遇到比这个版本新的结构就显示“这些数据来自更新的 TRACE 版本，请安装新版”并退出，不写任何文件。

<a id="headless-ext"></a>
## 9. 远程版（DEC-032、DEC-062）

后端零 GUI 依赖（INV-05）让同一套 `internal/service` 能编译成无界面的远程程序 `cmd/trace-server`（Linux，`CGO_ENABLED=0`）：它在服务层用远程配置启动（不续期、不做全部缓存与补更早内容、定时检查总开关常开），由 `internal/remoteserver` 把状态、订阅快照与凭证推送映射为 HTTPS JSON 接口；本地桌面版经 `internal/remote` 客户端连接。远程不提供网页（浏览器控制台与 `mygo/ui` WebAssembly 前端仍是以后的选项）。完整设计见[远程设计](remote.md)。
