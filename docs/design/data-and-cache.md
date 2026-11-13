# 技术设计：数据、缓存与信息流

数据目录与各数据文件、缓存条目与下载器、本地媒体协议、主页信息流。总体分层与接口见[架构](architecture.md)，订阅与直播的数据见[订阅](subscriptions.md)与[直播](live.md)。

<a id="storage"></a>
## 1. 数据目录、设置与索引

- **通道与位置**（DEC-029）：构建时用 `-ldflags -X` 写入通道：`release`（`go run ./tools/release`）、`dev`（`go run ./tools/package`）或只在升级演练里构建的 `ptest`；`mygo dev`、`go run`、测试等没有写入通道的构建是 `source`。

  | 通道 | 安装目录（默认） | 数据目录 | 缓存位置（默认，可改） | 名称与标识 |
  | --- | --- | --- | --- | --- |
  | `release` | `%LOCALAPPDATA%\Programs\trace` | `%LOCALAPPDATA%\trace` | `<文档>\trace` | 显示名“TRACE”；文件名一律小写 `trace`（`trace.exe`、安装目录、`mygo.json` 的 `name`/`slug`/`installDirName`）；`io.github.chialecode.trace` |
  | `dev` | `%LOCALAPPDATA%\Programs\trace-dev` | `%LOCALAPPDATA%\trace-dev` | `<文档>\trace-dev` | 显示名“TRACE Dev”；文件名一律小写 `trace-dev`；`io.github.chialecode.trace-dev` |
  | `ptest` | `%LOCALAPPDATA%\Programs\trace-ptest` | `%LOCALAPPDATA%\trace-ptest` | 演练时指向临时目录 | 显示名“TRACE PTest”；`io.github.chialecode.trace-ptest`；只用于安装与更新演练（[工程规则 §2.3](../dev-rules/engineering.md#release)） |
  | `source` | 不安装 | 必须由 `TRACE_DATA_DIR` 指定（工程规则用 `tmp/dev-data`）；没设置时用 `%LOCALAPPDATA%\trace-dev`，由数据目录锁避免与已安装的开发版同时打开 | 同 `dev` | `mygo.json` 的名称；`mygo dev` 自己的单实例标识是 `<identifier>.dev` |

  `<文档>` 是系统登记的“文档”文件夹（`SHGetKnownFolderPath(FOLDERID_Documents)`，可能被重定向到别的盘或 OneDrive），不是简单的 `%USERPROFILE%\Documents`。`TRACE_DATA_DIR` 对所有通道都优先（测试与冒烟用）。数据目录或缓存位置不能创建、不可写时，启动后显示明确的错误，不悄悄换到别处。安装目录只放程序文件（卸载时整个删除），任何数据都不写进去（INV-14）。
- **数据目录里的内容**：`settings.json`、`history.json`、`thumbs/`（最近解析条目的封面与头像缩略图）、`ui-state.json`、`subscriptions.json`（及 `.bak`）、`credentials/`、`library.db`、`updates.db`、`library-roots.json`、`pending-deletes.json`、`mygo/`（MyGo 的 `userData`）、`logs/`、`cache/`（可随时删除的临时文件）、`app.lock`（数据目录锁）。
- **添加已有的缓存文件夹**（FR-LIB-006）：`AddLibraryRoot` 让用户选一个文件夹；如果它含 `library/` 子文件夹，就当作旧便携数据目录，使用其中的 `library/`；有 `settings.json` 时也看其中记的 `cacheRoot`（旧数据目录不一定有这个文件）。扫描 `bilibili/<mid>/{dynamics,videos,lives}/*/item.json`（不跟随链接、单次最多 10 万个清单），按条目键加入索引，保存位置写进 `library-roots.json`；键已存在时保留现有条目、计入“跳过”。文件留在原处，播放、圆点、删除规则与其他条目相同（INV-08、INV-09）。不导入旧的凭证与历史（重新登录即可）。
- 旧版便携包的 `trace-data/`（可执行文件旁）不再自动使用；`cacheRoot` 为空的旧设置值表示“默认缓存位置”，按新规则解释为 `<文档>\trace[-dev]`。
- `history.json` 与 `thumbs/`：最近解析（最多 200 条，原子写，损坏时改名保留并提示一次）。解析成功时先记录条目（保留同一对象已有的缩略图），打开页面不等图片；随后界面在后台调用 `FetchHistoryThumbs`，经无 Cookie、无 `Referer` 的白名单图片通道读取封面与头像，再用 `AttachThumbs` 写回原条目（不改变顺序；条目在此期间被删除或清空时不写文件、不恢复条目）：源数据最多 10 MiB、24,000,000 像素，完整解码后按比例缩小（头像最长边 128、封面最长边 640，不放大小图）并重新编码为 PNG，去除源图元数据。保存到 `<数据目录>/thumbs/<kind>_<sha256>_<role>.png`，哈希包含对象标识、角色与图像内容；对象 ID 的大小写仍影响身份，文件名不含查询串和签名参数。条目记录相对文件名 `coverThumb`、`avatarThumb` 与 `durationText`；旧文件名继续读取，下载或缩略图写入失败保留原图或占位图（DEC-037）。
  缩略图创建与历史提交串行，提交失败保留旧记录和旧图，只清理本次新建且无引用的候选图。删除单条、清空或超出上限淘汰时，只按准确引用清理无引用图，不按 ID 前缀或目录枚举删除；Windows 下引用按文件名大小写不敏感比较。先把待清理文件名 `pendingThumbDeletes` 与新历史一同提交，再删除文件；清理失败返回可见错误，后续记录、删除或清空继续重试，重启不丢待清理记录。只读取历史引用或本次暂存的合法图片文件，拒绝链接、目录与未登记文件；解析页不向站点补发图片请求。
- `settings.json`：`version`、`cacheRoot`（空表示默认缓存位置 `<文档>\trace[-dev]`）、`videoQuality`（`best`/`2160`/`1080`/`720`，默认 `best`）、`codecOrder`（默认 `["avc","av1","hevc"]`）、`audioQuality`（`best`/`192k`，默认 `best`）、`multiPart`（`all`/`first`，默认 `all`）、`concurrency`（1～4，默认 2）、`upgradeCheckOnOpen`（默认 `true`）、`danmaku`（默认 `true`）、`subtitles`（默认 `true`）、`aiSubtitles`（默认 `true`）、`scheduledCheck`（默认 `false`）、`checkIntervalMin`（30/60/120/360，默认 60）、`liveCheckIntervalMin`（1～30，默认 3）、`maxRecordings`（1～4，默认 2）、`player`（`{danmakuOn, danmakuOpacity, danmakuDensity, subtitleLang}`）、`closeAction`（`tray`/`quit`，默认 `tray`）。原子写；未知字段忽略；非法值回到默认并提示一次；损坏时同 `history.json` 的处理。更新偏好 `updates: {autoCheck, autoDownload}`（“自动检查更新”“自动下载更新”，默认都开，只在正式版起作用，FR-UPD-002）。
- `subscriptions.json`：订阅与共用缓存规则（`cacheRules`，订阅用 `ruleId` 选一条，DEC-040）的正本（[订阅 §1](subscriptions.md#model)）。原子写；损坏时改名保留、提示一次、从空开始（订阅不能从别处重建，所以每次成功写入前把上一版保存为 `subscriptions.json.bak`，损坏时提示里给出“从上一版恢复”）。
- `updates.db`：检查更新的状态与更新记录（[订阅 §2](subscriptions.md#update-check)），独立于 `library.db`。损坏时改名保留后重新开始，所有账号回到“未建立起点”，下次检查只建立起点，旧动态不会进入更新列表。旧首次检查快照的可逆标志与迁移边界以订阅设计为准。
- `ui-state.json`：主页看过的最新一条（`anchor: {order: "newest", key, pubTs}`，只作下次启动时“有更新”的基准，不用来恢复位置，DEC-040，第 4 节）、主页有更新头像行的按人已看最新时间戳 `homeSeen`（`{"sub:<id>": <latestPubTs>, "mid:<uid>": <latestPubTs>}`，Q-009、DEC-037）、窗口/路由及播放偏好。旧文件中若 `anchor.order` 为 `"oldest"`，规范化时统一改为 `"newest"` 并保留 `key`、`pubTs`、`offsetPx`。`player` 保存 `volume`（0～1，0 为有效静音音量）、`muted`、`danmakuVisible`、`danmakuOpacity`、`danmakuArea`、`danmakuDensity`（`low`/`medium`/`high`/`unlimited`，默认 `medium`）、`subtitleLang`；旧文件缺少新字段时采用默认值。原子写，失败保留原文件与状态并在界面显示原因；读取和更新返回的状态不与已提交状态共享 `homeSeen` 映射；损坏时改名保留后恢复默认值。更新状态 `update`：`lastRunVersion`（上次运行的版本，用来在更新后第一次启动时说明一次）、`lastCheck`、已下载待重启的 `readyVersion` 与 `readyNotes`、为更新而退出的记录 `restart`（`reason`、`windowOpen`、`fromVersion`、`at`，下次启动读到后删除，[架构 §8](architecture.md#tray)）。
- 当前播放偏好的唯一来源为 `ui-state.json.player`；`settings.json.player` 的旧字段仍可读写和规范化，但当前播放器不读取它们。下载时是否保存弹幕、字幕仍由 `settings.json` 的下载开关控制。
- `library.db`：SQLite（`modernc.org/sqlite`，WAL 模式）索引，表为条目、媒体文件、引用、任务、任务尝试记录、待清理。它只是索引：每个缓存条目文件夹里的 `item.json` 才是正本。启动时 `PRAGMA quick_check` 失败或数据库文件无法解析，就把数据库（连同 `-wal`、`-shm`）改名为 `library.db.corrupt-<时间>` 保留，扫描各保存位置（记录在 `library-roots.json`）的 `item.json` 重建，界面提示一次。文件被占用、没有权限等打开失败不算损坏：显示错误，不改名、不重建。
- 待清理记录同时写进 `pending-deletes.json`（不只在可重建的索引里），索引重建后删到一半的条目不会重新出现。账本读取/解析失败时原样保留、返回错误并停止删除/重建相关变更，不能当作空账本覆盖。重建扫描、清单读写或 SQL 失败时停止并保留尚未确认 stale 的旧索引，只有完整扫描成功才事务清理旧行。

<a id="schema-versions"></a>
- **结构版本与迁移前备份**（FR-STORE-001、FR-UPD-004）：每个数据文件都有结构版本，当前都是 1——`settings.json`、`ui-state.json`、`history.json`、`subscriptions.json` 是顶层的 `version` 字段，`library.db`、`updates.db` 是 SQLite 的 `user_version`（新建或旧的 0 在启动时写成 1）。没有版本字段的旧文件按 1 读取。`internal/storage/schema.go` 登记这些版本，后端启动时先核对再打开任何文件：
  - 遇到**比这个程序新**的结构（例如装回了旧版本）就拒绝启动，显示“这些数据来自更新的 TRACE 版本，请安装新版”，不写、不降级、不覆盖任何文件；
  - 遇到**比这个程序旧**的结构，迁移前先把旧文件复制为 `<文件>.premigrate-<旧版本>`（SQLite 连同 `-wal`），每个文件只留最近一份，再由各自的代码迁移。

<a id="schema-v2"></a>
- **多站点的结构版本 2**（NFR-DATA-004，DEC-043）：加入抖音时以下四个文件升到 2，其余仍是 1。迁移都在一个事务（JSON 为一次原子写）里完成，失败时保留原文件与 `.premigrate-1`，启动显示原因、不带着半迁移的数据继续；迁移可重复执行（已是 2 的文件不再动）。

  | 文件 | 1 → 2 的变化 | 旧数据怎样对应 |
  | --- | --- | --- |
  | `library.db` | 条目、信息流与任务表加 `author_uid TEXT`（站点内作者主键：哔哩哔哩是十进制 `mid`，抖音是 `sec_uid`）及 `(site, author_uid)` 索引；按作者的筛选、统计与“有更新”改用 `(site, author_uid)` | 已有行 `author_uid = CAST(mid AS TEXT)`；`mid` 列保留，哔哩哔哩代码照常写 |
  | `updates.db` | 新表 `site_scheduler_state(site 主键, 退避截止、退避级数、连续失败、最后停止原因与时间、每小时令牌时间戳)` 替代单行的 `scheduler_state`，以及 `account_baseline_pinned(site, uid, post_id)` 记录建立起点时已在列的置顶作品 ID | 原单行搬到 `site = 'bilibili'`；旧表保留不再写；`account_state`、`update_item` 原本就以 `(site, uid)` 为键，不变 |
  | `ui-state.json` | 主页与媒体库的站点筛选 `feedFilter.site` 增加 `"all"`，默认 `"all"` | 版本 1 里只有默认值 `"bilibili"`（界面从未提供站点筛选），迁移时一次性改成 `"all"`；之后用户选的“B 站”原样保留 |
  | `subscriptions.json` | 账号 `accounts[]` 可以是 `site: "douyin"`（`uid` 为 `sec_uid`），并可带 `webRid`、`uniqueId`；订阅校验按站点 | 原有账号原样保留（都是 `bilibili`）；共用缓存规则不变 |

  `history.json` 只新增可选字段、保持 1：最近解析条目原本就带 `site`，删除与缩略图归属改按 `<site>:<kind>:<id>`。旧版本程序遇到版本 2 的文件按上面的规则拒绝启动，不会改写抖音数据。

<a id="cache"></a>
## 2. 缓存

### 2.1 条目与文件夹

缓存以动态为单位（DEC-018）。条目键是 `<站点>:<类型>:<ID>`（`library.CanonicalItemKey`）：哔哩哔哩 `bilibili:dynamic:<id_str>`、`bilibili:video:<bvid>`、直播录像 `bilibili:live:<room_id>-<开播时间>`（[直播 §4](live.md#recording-item)）；抖音视频作品 `douyin:video:<aweme_id>`、图文作品 `douyin:dynamic:<aweme_id>`（与哔哩哔哩的图文动态同属“动态”类，主页与媒体库沿用同样的筛选）、直播录像 `douyin:live:<room_id>`。不带站点的旧键按哔哩哔哩解释。

```text
<保存位置>/bilibili/<mid>/dynamics/<id_str>/item.json、images/01.jpg…、orig.json（转发的原动态快照）
<保存位置>/bilibili/<mid>/videos/<bvid>/item.json、cover.jpg、P01.mp4…、P01.audio-30251.m4a（必要时，见 2.5）、P01.danmaku.xml、P01.zh-CN.vtt、P01.zh-CN.srt、P01.ai-zh.vtt…（见 2.9）
<保存位置>/bilibili/<mid>/lives/<room_id>-<开播时间>/item.json、cover.jpg、part-001.mp4…（见直播设计）
```

抖音（DEC-043）的作者目录用 `sec_uid` 的短哈希（SHA-256 前 12 个十六进制字符），不用昵称，也不用会变的抖音号；完整 `sec_uid` 写在清单的作者里：

```text
<保存位置>/douyin/<sec_uid 短哈希>/works/<aweme_id>/item.json、cover.jpg、video.mp4（视频作品）
<保存位置>/douyin/<sec_uid 短哈希>/works/<aweme_id>/item.json、cover.jpg、images/01.jpg…、images/01_live.mp4…、music.m4a（图文作品，扩展名按实际格式）
<保存位置>/douyin/<sec_uid 短哈希>/lives/<room_id>/item.json、cover.jpg、part-001.mp4…
```

“添加已有的缓存文件夹”同样扫描 `douyin/*/{works,lives}/*/item.json`。文件名只用 ID 和序号，标题写在清单里，避免非法字符和超长路径。视频条目放在 UP 主自己的目录下，被动态引用；投稿列表和视频页直接缓存的也是同一个视频条目。

### 2.2 清单 `item.json`

版本、站点、类型、ID、作者（mid、昵称、头像地址）、发布时间、正文、动态类型、媒体列表（角色、相对路径、字节数、SHA-256、宽高、时长）、引用的视频键、状态、缺失项（是什么、原因）、缓存时间。视频条目的每个分 P 另记：

- `video`：清晰度代码与名称、编码（`codecid`、`codecs`）、宽高、帧率；
- `audio`：音质代码（30251/30250/30280…）与名称，另存的高音质音轨（如有）；
- `available`：缓存当时站点列出的档位快照（`accept_quality`、`support_formats` 的 `quality`/`need_vip`、返回流里的最高档、是否有杜比/无损音频）与探测时间；
- `danmaku`：弹幕文件、条数、取得时间（没开设置时记 `skipped`）；`subtitles`：每种语言的 `lan`、`lan_doc`、是否 AI、VTT 与 SRT 文件。

抖音条目的作者另记 `site`、`uid`（`sec_uid`）与抖音号；视频作品记实际与可选的档位（分辨率、编码、帧率、码率、档位名）；图文记配乐（标题、作者、时长、文件）。

只存公开内容；哔哩哔哩的图片原始 URL 可以存（公开 CDN，去掉尺寸后缀），**视频的签名播放地址不存**；抖音的图片与媒体地址都带签名与过期参数，**一律不存**。播放数、点赞数、评论数这类会变的互动数据不缓存（DEC-040）。

### 2.3 内容范围

| 动态类型 | 保存 |
| --- | --- |
| 视频 | 正文 + 引用的视频条目（封面 + 全部分 P，按第 2.5 节选流） |
| 图文、带图文字 | 正文 + 全部原图；实况照片另存动态片段 `images/NN_live.mp4`（角色 `motion`，静帧的 `motion` 字段指向它；按下面的单文件 MP4 校验并保证 `moov` 在前，系统播放器也能预览；下载或校验失败删掉片段、记为部分缓存，DEC-040、DEC-046 ①） |
| 纯文字 | 正文 |
| 转发 | 转发正文 + 原动态快照，原动态的图片和视频按上两行保存 |
| 专栏 | 标题、摘要、封面与摘要里的图；全文待实现（FR-FEED-002），记为部分缓存 |
| 直播、番剧、课程、音乐、商品等 | 正文与封面，记为部分缓存并写明类型不支持 |

抖音作品（FR-DY-005、FR-DY-006）：

| 作品 | 保存 |
| --- | --- |
| 视频 | 文案、发布信息、封面（`origin_cover` 优先）、一个无水印单文件 MP4（第 2.5 节） |
| 图文 | 文案、发布信息与按顺序的每一页（`images[]` 可以混排三种页）：图片页存无水印原图（`images[].url_list`，不用带水印的 `download_url_list`）；实况照片存静帧与片段（`images/NN_live.mp4`，角色 `motion`）；视频页（有 `images[i].video` 但不是实况照片）存封面 `images/NN.<扩展名>` 与视频 `images/NN.mp4`（角色 `video`，按下面的单文件 MP4 校验，选流规则同视频作品）；认不出的页存能拿到的图片并在清单的该页记 `unknownKind`。配乐 `music.<扩展名>`（角色 `audio`）；配乐没有地址（版权限制）记为缺失项并写明原因 |
| 付费、加密、已删除、私密 | 不缓存，说明原因 |

<a id="tasks"></a>
### 2.4 任务与下载器

一个缓存请求是一个任务，任务内分步骤：取元数据 → 下载图片 → 每个分 P：取播放地址 → 选流 → 下载视频流与音频流 → 合并 → 校验 → 弹幕与字幕（2.9）→ 写清单。任务状态：排队、进行中、已暂停（带原因）、已完成、部分完成、失败、已取消。条目的状态由它的文件和引用的视频条目推算，不单独手写。任务带 `source`（`manual` / `auto`，自动缓存见[订阅 §4](subscriptions.md#auto-cache)）。

**逐文件进度**（FR-CACHE-009）：任务开始时按计划列出文件清单（每张图片、每个分 P 的视频流、音频流、合并后的 MP4、弹幕、每种字幕），每个文件记状态（等待、下载中、校验中、完成、失败、跳过）、已下载与总字节（总字节在拿到第一段响应后才有）、速度；`TaskDetail(id)` 返回这份清单与尝试记录，`task:changed` 事件只带有变化的文件，每秒至多一次。文件清单存在 `library.db` 的任务表里，重启后仍可看到；已完成的任务保留清单直到任务记录被清理。

**分段下载器**（FR-CACHE-005，[媒体与存储选型 §5](../research/2026-11-05-media.md#download)）：

- 带公共请求头和 `Referer: https://www.bilibili.com/`；写到 `*.part`。
- 每次请求一个 8 MiB 的闭区间 `Range: bytes=N-M`，要求 `206` 且 `Content-Range` 的起点与总长一致；第一段若返回 `200`（不支持续传），按整段读取并核对 `Content-Length`；后续段返回 `200` 时截断 `*.part`、换下一个节点从头下；从头下按失败计，同一文件超过 5 次就暂停（网络），不会无限重下。
- 段内错误分类：
  - **传输错误**（读到的字节少于区间长度就结束、超时、连接重置、TLS 错误、HTTP 5xx）：保留已写入的字节，从断点继续，同一段最多重试 5 次，间隔 1、2、4、8、16 秒加 ±20% 抖动；每次失败后换到候选地址列表里的下一个。
  - **地址失效**（403、404、410）：换下一个地址；全部失效时为这个分 P 重新取一次播放地址（只一次），按相同的 `id` 和编码在新结果里找对应流，从断点继续。
  - **风控**（412，或重取播放地址时遇 -352、-412）：立即暂停任务，原因“风控”，不重试。
  - **未登录**（-101）：按[架构 §4](architecture.md#bilibili-client)处理。
  - 重试或地址都用完：任务“已暂停（网络）”，已下载的字节保留，用户点继续后从断点续传。
- **候选地址顺序**：主地址和备用地址里，官方节点（`upos-*` 等 `*.bilivideo.com` 域名、标准端口）排在前面，PCDN 节点（`*.mcdn.bilivideo.cn`、带非标准端口的地址）排在后面；同一分 P 里，最近一次成功的节点优先。
- **尝试记录**：每次失败记一条 `{时间, 文件, 偏移, 节点域名, 尝试序号, 错误类别, HTTP 状态}` 到任务尝试记录（每个任务最多保留 200 条），供任务详情显示；不记查询串和完整地址。
- 结束时核对总长度；校验通过后改为正式名。已改为正式名的流文件和分 P 在继续任务时直接复用，不重下（复用前核对长度与清单）。
- 图片只接受 `image/*`，单张上限 50 MiB，同样分段续传。
- **并发与节奏**：同时进行的任务数取设置（默认 2，1～4，改动对之后开始的任务生效）；每个任务内的文件按顺序下载，所以同时下载的文件数也不超过这个值；取元数据和播放地址走客户端的 500 毫秒间隔。
- **错误**：内容不可得 → 该项记为缺失，任务部分完成；磁盘写入失败或空间不足 → 任务失败，类别 `storage`。
- **重启**：启动时把“排队”“进行中”的任务改为“已暂停（应用关闭）”，不自动开始。
- **取消**：先停 worker，删除本任务写入前已持久化认领的未完成临时文件；认领以完整条目目录、相对路径及任务/清单登记为准，不能仅凭同名 `.part` 认领。已经校验落盘的文件与未认领的用户文件保留，条目状态按实际文件重新推算。暂停/取消后，worker 退出才释放并发名额，迟到进度不能把任务改回进行中；任务状态写库失败显示 storage，恢复后可重试。

### 2.5 选流（DEC-024）

- **视频**：用协议资料 §14 的清晰度排序表，在这次返回的 `dash.video[]` 里取最高的一档（`videoQuality` 为上限时取不超过上限的最高档）；同档内按 `codecOrder` 选编码（默认 AVC → AV1 → HEVC：AVC 在 Windows Media Foundation 下全部机器原生可解；AV1 / HEVC 在装有系统视频扩展时可解，不可解时播放层提示并可一键调用系统播放器打开）。实际编码与偏好不同时记在清单里。
- **音频**：`audioQuality=best` 时按 无损（`dash.flac.audio`，30251）> 杜比（`dash.dolby.audio[]`，30250）> 192K > 132K > 64K 取最高；响应完全没有音频时只存视频。杜比或无损音轨只有取得应用内解码证据后才直接合并；目前将两者原样另存为 `Pxx.audio-<代码>.m4a`，主 MP4 合并最好的普通 AAC，清单记两者关系（[媒体选型 §1.1](../research/2026-11-05-media.md#hires-audio)、Q-005）。已返回最高音轨但 AAC 后备缺失/下载失败时，保留完成的最高轨并记缺失/失败，不生成静音的完成主视频。
- **多 P**：`multiPart=all` 时全部分 P。
- **抖音视频**（DEC-043 ⑤）：档位都在作品详情的 `video.bit_rate[]` 里，不另请求播放地址。先排除带水印地址与 `is_bytevc1 == 2`，再按分辨率档（短边归一到 2160/1440/1080/720/540/480）取最高，`videoQuality` 为上限时取不超过上限的最高档；同档内按 `codecOrder`，再按帧率、码率、文件大小。同一档的地址列表里直连 CDN 优先，回源跳转地址排最后（[抖音协议 §6.5](../research/douyin.md#playurl)）。
- 付费或会员内容只给试看时，不存试看片段，记为部分缓存并写明原因。

<a id="merge"></a>
### 2.6 合并、校验与状态圆点

- **合并**：用 mp4ff 把视频流和音频流合成一个 MP4（Q-003）：`ftyp` + `moov` 在前、音视频约 1 秒一块交错写入、带编辑列表、各轨时长与源流一致（NFR-MEDIA-001）。合并成功后删掉中间流文件；合并失败时保留两个流文件，记为部分缓存“合并失败”。
- **单文件 MP4**（抖音视频、混排图文视频页、抖音与哔哩哔哩的实况片段；哔哩哔哩实况片段不核对预期时长，DEC-046 ①，[ACT-019](../../USER-ACTIONS.md#act-019) 实测站点给的是 `moov` 在前的 AVC MP4）：不合并。下载完成后以懒加载模式核对顶层 box 总长与文件长度一致、非分片结构（不含 `moof`）、至少含一条有效非加密视频轨（有音频轨就一起核对，拒绝 `encv`/`enca`/`sinf` 等 CENC 加密样本项）、正时长且与站点给的预期时长误差不超过 2 秒（不要求 `elst` 编辑列表）。`moov` 已在 `mdat` 之前时直接计算 SHA-256 通过；`moov` 在 `mdat` 之后时用 mp4ff 在磁盘上把 `moov` 移到 `mdat` 之前（按文件区间流式顺序拷贝 `mdat` 载荷、不把整个文件读进内存，平移全部轨道的 `stco`/`co64` 块偏移并在超 32 位上限时自动升级为 `co64`，写同目录临时文件、`fsync` 后原子改名；改写失败清理临时文件、保留原文件不动并记为部分缓存）。图片按文件头与 `Content-Type` 决定扩展名，拒绝 HTML/JSON 伪装的响应。
- **校验**：文件长度与响应一致；合并后的 MP4 能被解析出视频轨（和音频轨），时长与站点给的误差不超过 2 秒；记录 SHA-256。全部通过后才把 `*.part` 改为正式名并写清单（INV-06）。
- **状态圆点**（FR-CACHE-004）由后端推算，前端只负责显示：

  | 圆点 | 条件 |
  | --- | --- |
  | 无 | 未缓存 |
  | 进度圆环 | 排队、进行中 |
  | 绿 | 已完成，且每个分 P 都是缓存时（或最近一次探测时）能拿到的最高档 |
  | 黄 | 部分缓存、已暂停，或探测到可升级 |
  | 红 | 失败、文件缺失、清单损坏 |

  动态的圆点综合它自己的文件和引用的视频条目：任一为红则红，否则任一为黄则黄。

<a id="upgrade"></a>
### 2.7 画质升级探测（FR-CACHE-006）

- 触发：打开已缓存视频的详情页（`upgradeCheckOnOpen`，同一视频 24 小时内最多一次，只取第 1 P 的播放地址）；任务页“检查画质更新”（`UpgradeCandidates` 给出最多 50 个最久未检查的视频，前端逐个调用 `CheckUpgrade` 取第 1 P 的播放地址，走 500 毫秒间隔，显示“正在检查 i / n”、可以取消，遇到风控、限流或未登录立即停止）。
- 比较：把这次返回流里的最高档（视频按排序表，音频按第 2.5 节的顺序）与清单里已缓存的档比较；更高时在清单 `available` 里记录，并标记条目“可升级”（黄点，说明“可升级到 4K / 杜比全景声”）。
- 升级：`Upgrade(key)` 建一个任务，只为档位低于最高档的分 P 重新选流下载，新文件校验通过后才原子替换旧文件，旧文件随后删除；失败时旧文件不动。

<a id="delete"></a>
### 2.8 删除（FR-LIB-003）

- 删动态条目删除它自己的文件；它引用的视频条目只在没有其他动态引用、也不是被直接缓存的情况下一起删除，否则保留并告知。
- **只删登记过的文件**：按清单删除媒体文件、`orig.json`、本条目的 `*.part` 临时文件，最后删除 `item.json`；然后自下而上只删除已经变空的文件夹（`images/`、条目文件夹）。文件夹里还有未登记的文件（例如用户自己放的）时保留文件夹，界面说明“文件夹里还有 N 个不是 TRACE 创建的文件，已保留”。任何情况下都不对条目文件夹做递归删除（INV-09）。
- 确认弹窗先调 `PlanDelete(key)`：它与 `Delete` 用同一份删除计划，列出会删除的条目与登记文件、会保留的共用视频（“这个视频还被 N 条动态引用，将保留视频文件”），不改动任何东西。
- 先改索引并写待清理记录，再删文件；删不掉的（被占用等）留在待清理里，下次启动再删，并跳过已经被重新缓存的条目。
- **改保存位置**：只影响之后的新任务；索引给每个条目记它的保存位置，旧条目照常浏览。
- **可选迁移旧文件**：无进行中/排队缓存任务与录制时才开始；已暂停 worker 完全退出后才能迁移，期间阻止新队列/worker 启动。只复制清单登记的文件，新位置有同名用户文件则停止。副本校验与根登记成功、设置原子落盘成功后才删除登记的原文件；此前失败回滚新副本与根，保留原文件，不搬动整个目录。
- 正在录制的直播条目不能删除（先停止录制）。

<a id="danmaku-subtitles"></a>
### 2.9 弹幕与字幕（FR-CACHE-008）

- 每个分 P 合并校验通过后执行；设置 `danmaku`、`subtitles`、`aiSubtitles` 分别控制，关闭时清单记 `skipped`，不算缺失。
- **弹幕**：按分 P 时长请求 `ceil(时长 / 360 秒)` 个分段（[协议资料 §18](../research/bilibili.md#danmaku)），走 API 的 500 毫秒间隔；protobuf 用 `google.golang.org/protobuf/encoding/protowire` 手工解码，未知字段跳过；合并全部分段、按 `idStr` 去重、按出现时间排序，写成通用 XML（原子写，先 `.part` 再改名）。某一段失败：整份弹幕记为缺失（不写不完整的文件），原因按错误分类；风控、未登录按 2.4 的规则暂停任务。
- **字幕**：`player/wbi/v2` 取字幕列表（校验 `bvid`/`cid`），跳过空地址；AI 字幕只在 `aiSubtitles` 开启时保存；逐个下载字幕 JSON（`*.hdslb.com`，上限 5 MiB，地址含 `auth_key` 只在内存）转成 `P<nn>.<lan>.vtt` 与 `.srt`。没有字幕不算缺失；某个语言下载或解析失败时这一项记为缺失。
- 弹幕和字幕文件登记在清单的媒体列表（角色 `danmaku`、`subtitle`），通过 `Library.MediaPath` 白名单按登记提供；删除时与其他登记文件一起删。
- 缺失项让条目变黄（“补全”只补缺的弹幕或字幕，不重下视频）。在本功能之前缓存的视频，清单里没有 `danmaku`/`subtitles` 字段，按“未保存”处理：圆点不变，详情页显示“补存弹幕和字幕”，点击后建一个只取弹幕与字幕的任务。
- 弹幕是缓存那一刻的快照，之后不自动更新。

<a id="media-server"></a>
## 3. 本机媒体路径白名单（`Library.MediaPath`）

改为 `mygo/ui` 原生界面（DEC-032）后，图片查看器与视频/弹幕/字幕播放器在 Go 进程内直接读取本机文件，不再经过 WebView 自定义协议。所有本机媒体读取统一通过 `Library.MediaPath(key, relPath)`（暴露为 `LibraryService.MediaPath`）解析绝对路径：

- **登记白名单（INV-08）**：只允许打开索引与 `item.json` 里登记在该 `key` 下的媒体文件（封面、图片、`Pxx.mp4`、`video.mp4`、`images/NN_live.mp4`、`images/NN.mp4`、`music.*`、`part-NNN.mp4`、`Pxx.danmaku.xml`、`Pxx.<lan>.vtt`/`.srt`）；未登记的文件名或属于其他条目的路径一律拒绝。在线浏览未缓存的抖音图文配乐或混排视频页时，经 `StagePreviewMedia` 在白名单、不带 Cookie 条件下暂存到 `<数据目录>/cache/preview/` 后再交给本地播放器。
- **路径与链接安全**：路径在服务端由“条目所在保存位置 + 条目相对目录 + 清单里的相对路径”拼出；每段路径拒绝 `.`、`..`、反斜杠、空字节与冒号，用 `filepath.Rel` 确认没有跑出条目文件夹，并用 `os.Lstat` 与 `filepath.EvalSymlinks` 拒绝符号链接或目录联接（Junction）逃逸（INV-08）。
- 缩略图：图片直接读取缓存原图并在 `mygo/ui` 中按网格尺寸缩放渲染；视频与直播录像使用登记的封面文件 `cover.jpg`。

<a id="feed"></a>
## 4. 主页信息流（FR-FEED-001、DEC-037）

- 来源：索引里所有动态条目、没有被任何动态引用、被直接缓存的视频条目（显示成一条“视频”帖子），以及直播录像条目（显示成一条“直播录像”帖子，发布时间取开播时间）。被动态引用的视频不单独出现。只要有清单就出现，不管圆点颜色。
- 排序键：固定按 `(发布时间, 条目键)` 从新到旧（`order` 恒为 `newest`，不再提供正序/倒序切换，DEC-037）。分页用双向键集游标，`direction` 为 `older`/`newer`/`around`，每页 30 条；同一发布时间按条目键稳定排序，翻页不重复不遗漏。
- 位置（DEC-040）：主页每次打开都从最新处开始，不恢复位置，也不提示“N 条更新的内容”。前端在可见条目里有比已记下的更新的发布时间时调用 `SaveAnchor({order: "newest", key, pubTs})`，`ui-state.json` 的 `anchor` 只表示“看过的最新一条”；它在下次启动时读一次，作为这次会话“有更新”的基准，会话内不变。往上翻过即算看过，往下滑不会把内容重新当成未读。保存失败显示原因。新缓存的内容在回到列表顶部时载入（`direction: newer`），当前看到的条目不跳动。“看最新”先成功读取再替换可见时间线，失败保留原列表。
- 主页标题下的有更新头像行（Q-009、DEC-037）：按 `library.Statistics()` 的各作者最新缓存发布时间 `latestPubTs` 聚合到订阅（合并关联账号）及未归属订阅的独立作者；当某个订阅/作者的 `latestPubTs > max(会话基准, uiState.homeSeen[actorKey])`（且基准时间戳大于 0）时，按 `latestPubTs` 从近到远在“主页”标题下方横向列出圆形头像与一行名称。点击后先读取该人的已缓存内容，成功才进入单订阅/单作者视图并保存 `homeSeen`；查询失败保留主时间线，保存失败显示原因。单订阅视图使用独立列表，返回时恢复主时间线的条目、滚动位置和分页错误。统计在本机刷新和媒体库变更时读取，失败保留上次可读结果，不在每次重绘查询；帖子的显示数据按条目缓存在内存里，任务、媒体库、订阅或直播事件到来时整体重建，重绘时不逐帧读媒体库；本机图片在后台解码并缩到最长边 1280px，界面线程不解码原图（DEC-040）；主页图片只用本机文件或已有内存位图，缺失时占位，不自动补发远程图片请求。
