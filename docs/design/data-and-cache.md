# 技术设计：数据、缓存与信息流

数据目录与各数据文件、缓存条目与下载器、本地媒体协议、主页信息流。总体分层与接口见[架构](architecture.md)，订阅与直播的数据见[订阅](subscriptions.md)与[直播](live.md)。

<a id="storage"></a>
## 1. 数据目录、设置与索引

- **通道与位置**（DEC-029）：构建时用 `-ldflags -X` 写入通道：`release`（`go run ./tools/release`）、`dev`（`go run ./tools/package`）或只在升级演练里构建的 `test`；`mygo dev`、`go run`、测试等没有写入通道的构建是 `source`。

  | 通道 | 安装目录（默认） | 数据目录 | 缓存位置（默认，可改） | 名称与标识 |
  | --- | --- | --- | --- | --- |
  | `release` | `%LOCALAPPDATA%\Programs\trace` | `%LOCALAPPDATA%\trace` | `<文档>\trace` | 显示名“TRACE”；文件名一律小写 `trace`（`trace.exe`、安装目录、`mygo.json` 的 `name`/`slug`/`installDirName`）；`io.github.chialecode.trace` |
  | `dev` | `%LOCALAPPDATA%\Programs\trace-dev` | `%LOCALAPPDATA%\trace-dev` | `<文档>\trace-dev` | 显示名“TRACE Dev”；文件名一律小写 `trace-dev`；`io.github.chialecode.trace-dev` |
  | `test` | `%LOCALAPPDATA%\Programs\trace-test` | `%LOCALAPPDATA%\trace-test` | 演练时指向临时目录 | 显示名“TRACE Test”；`io.github.chialecode.trace-test`；只用于安装与更新演练（[工程规则 §2.3](../dev-rules/engineering.md#release)） |
  | `source` | 不安装 | 必须由 `TRACE_DATA_DIR` 指定（工程规则用 `tmp/dev-data`）；没设置时用 `%LOCALAPPDATA%\trace-dev`，由数据目录锁避免与已安装的开发版同时打开 | 同 `dev` | `mygo.json` 的名称；`mygo dev` 自己的单实例标识是 `<identifier>.dev` |

  `<文档>` 是系统登记的“文档”文件夹（`SHGetKnownFolderPath(FOLDERID_Documents)`，可能被重定向到别的盘或 OneDrive），不是简单的 `%USERPROFILE%\Documents`。`TRACE_DATA_DIR` 对所有通道都优先（测试与冒烟用）。数据目录或缓存位置不能创建、不可写时，启动后显示明确的错误，不悄悄换到别处。安装目录只放程序文件（卸载时整个删除），任何数据都不写进去（INV-14）。
- **数据目录里的内容**：`settings.json`、`history.json`、`thumbs/`（最近解析条目的封面与头像缩略图）、`avatars/`（订阅、账号与已缓存媒体库/共创合作作者的本机头像 PNG，四个站点在打开用户页/详情及缓存规划时保存，读取与落盘均核对 PNG 魔数并拒绝源站失效占位图，界面优先用它，七天内不重取，到期刷新失败或遇到失效占位图时保留原本地有效头像不覆盖，不含远程地址；清理时结合订阅、缓存队列与 `library.Store.DistinctAuthorRefs()` 保留媒体库及 `via` 共创作者引用，直到条目删除且无引用后再清理）、`cache-queue.json`（后台缓存队列任务，v3，DEC-050 ①）、`cache-conflicts.json`（V1/V2 缓存内容冲突账本与已忽略版本指纹，v1，[DEC-076](../decisions.md#dec-076)）、`update-history.json`（更新检查记录与版本更新记录，DEC-051 ①）、`ui-state.json`（v4）、`subscriptions.json`（v5，及 `.bak`）、`credentials/`（`bilibili.bin`、`douyin.bin`、`weibo.bin`、`x.bin`）、`library.db`（v2）、`updates.db`（v2）、`library-roots.json`、`pending-deletes.json`、`mygo/`（MyGo 的 `userData`）、`logs/`、`cache/`（可随时删除的临时文件）、`runtime-window.json`（运行期窗口开关状态标记，供开发版自动安装跨桌面判断，退出时清理）、`app.lock`（数据目录锁）。
- **添加已有的缓存文件夹**（FR-LIB-006）：`AddLibraryRoot` 让用户选一个文件夹；如果它含 `library/` 子文件夹，就当作旧便携数据目录，使用其中的 `library/`；有 `settings.json` 时也看其中记的 `cacheRoot`（旧数据目录不一定有这个文件）。扫描 `bilibili/<mid>/{dynamics,videos,lives,forwards}/*/item.json`、`douyin/*/{works,lives}/*/item.json`、`weibo/*/{posts,forwards}/*/item.json` 与 `x/*/{posts,forwards}/*/item.json`（不跟随链接、单次最多 10 万个清单），按条目键加入索引，保存位置写进 `library-roots.json`；键已存在时保留现有条目、计入“跳过”。文件留在原处，播放、圆点、删除规则与其他条目相同（INV-08、INV-09）。不导入旧的凭证与历史（重新登录即可）。
- 旧版便携包的 `trace-data/`（可执行文件旁）不再自动使用；`cacheRoot` 为空的旧设置值表示“默认缓存位置”，按新规则解释为 `<文档>\trace[-dev]`。
- `history.json` 与 `thumbs/`：最近解析（最多 200 条，原子写，损坏时改名保留并提示一次）。解析成功时先记录条目（保留同一对象已有的缩略图），打开页面不等图片；随后界面在后台调用 `FetchHistoryThumbs`，经无 Cookie、无 `Referer` 的白名单图片通道读取封面与头像，再用 `AttachThumbs` 写回原条目（不改变顺序；条目在此期间被删除或清空时不写文件、不恢复条目）：头像源数据最多 10 MiB、封面源数据最多 128 MiB（兼容微博原图、长图与多帧 GIF）、100,000,000 像素，完整解码后按比例缩小（头像最长边 128、封面最长边 640，不放大小图）并重新编码为不超过 10 MiB 的 PNG，去除源图元数据。保存到 `<数据目录>/thumbs/<kind>_<sha256>_<role>.png`，哈希包含对象标识、角色与图像内容；对象 ID 的大小写仍影响身份，文件名不含查询串和签名参数。条目记录相对文件名 `coverThumb`、`avatarThumb` 与 `durationText`；旧文件名继续读取，下载或缩略图写入失败保留原图或占位图（DEC-037）。
  缩略图创建与历史提交串行，提交失败保留旧记录和旧图，只清理本次新建且无引用的候选图。删除单条、清空或超出上限淘汰时，只按准确引用清理无引用图，不按 ID 前缀或目录枚举删除；Windows 下引用按文件名大小写不敏感比较。先把待清理文件名 `pendingThumbDeletes` 与新历史一同提交，再删除文件；清理失败返回可见错误，后续记录、删除或清空继续重试，重启不丢待清理记录。只读取历史引用或本次暂存的合法图片文件，拒绝链接、目录与未登记文件；解析页不向站点补发图片请求。
- `settings.json`：`version`、`cacheRoot`（空表示默认缓存位置 `<文档>\trace[-dev]`）、`videoQuality`（`best`/`2160`/`1080`/`720`，默认 `best`）、`codecOrder`（默认 `["avc","av1","hevc"]`）、`audioQuality`（`best`/`192k`，默认 `best`）、`multiPart`（`all`/`first`，默认 `all`）、`concurrency`（1～4，默认 2）、`upgradeCheckOnOpen`（默认 `true`）、`danmaku`（默认 `true`）、`subtitles`（默认 `true`）、`aiSubtitles`（默认 `true`）、`scheduledCheck`（默认 `false`）、`checkIntervalMin`（30/60/120/360，默认 60）、`liveCheckIntervalMin`（1～30，默认 3）、`maxRecordings`（旧字段，1～4，默认 2；连同 `live.maxConcurrentRecordings` 仅兼容读取与规范化、不改结构版本 1，不再限制同时录制数，DEC-051 ③）、`player`（`{danmakuOn, danmakuOpacity, danmakuDensity, subtitleLang}`）、`closeAction`（`tray`/`quit`，默认 `tray`）。原子写；未知字段忽略；非法值回到默认并提示一次；损坏时同 `history.json` 的处理。更新偏好 `updates: {autoCheck, autoDownload}`（“设置 › 更新”里的“自动检查更新”“自动下载更新”，默认都开，只在正式版起作用，FR-UPD-003、DEC-051 ①）。
- `subscriptions.json`（当前结构版本 5，见[下文版本 5 说明](#schema-v5)）：订阅及其缓存能力标签（`tags`，与 `rules` 一一对应，[DEC-070](../decisions.md#dec-070)）的正本（[订阅 §1](subscriptions.md#model)）。原子写；损坏时改名保留、提示一次、从空开始（订阅不能从别处重建，所以每次成功写入前把上一版保存为 `subscriptions.json.bak`，损坏时提示里给出“从上一版恢复”）。
- `updates.db`：检查更新的状态与更新记录（[订阅 §2](subscriptions.md#update-check)），独立于 `library.db`。DEC-061 新增表 `account_works`（每个账号上次解析得到的作品总数与时间、上次“全部缓存”的时间，[订阅 §4](subscriptions.md#account-works)），启动时 `CREATE TABLE IF NOT EXISTS`，结构版本不变，旧版程序不读它。损坏时改名保留后重新开始，所有账号回到“未建立起点”，下次检查只建立起点，旧动态不会进入更新列表。旧首次检查快照的可逆标志与迁移边界以订阅设计为准。
- `ui-state.json`（当前结构版本 4，见[下文版本 4 说明](#schema-v4)）：主页看过的最新一条（`anchor: {order: "newest", key, pubTs}`，作为没有 `homeSeen` 项的订阅在下次启动时的初始已读基准，DEC-040、DEC-053，第 4 节）、主页阅读位置 `homePositions`（可选，`{"all"|"bilibili"|"douyin"|"weibo"|"x": {key, pubTs, offsetPx}}`，每个站点筛选下主时间线可见区最上面那条与偏移；旧文件没有时照常读，DEC-053 ①）、主页侧栏订阅的已看最新时间戳 `homeSeen`（`{"sub:<id>": <latestPubTs>}`；旧的 `mid:` 与作者键保留不删、不再写，Q-009、DEC-053 ②）、任务页里移除的“最近结束的录制” `dismissedLiveSessions`（可选，`{"<录像条目键>": "<移除时间 RFC3339>"}`，只用来隐藏列表行，8 天后规范化时删除，DEC-057 ①）、窗口/路由及播放偏好。旧文件中若 `anchor.order` 为 `"oldest"`，规范化时统一改为 `"newest"` 并保留 `key`、`pubTs`、`offsetPx`；媒体库固定按发布时间排序（DEC-049 ③），旧文件里若留有按缓存时间排序的偏好则静默忽略。`player` 保存 `volume`（0～1，0 为有效静音音量）、`muted`、`danmakuVisible`、`danmakuOpacity`、`danmakuArea`、`danmakuDensity`（`low`/`medium`/`high`/`unlimited`，默认 `medium`）、`subtitleLang`；旧文件缺少新字段时采用默认值。原子写，失败保留原文件与状态并在界面显示原因；读取和更新返回的状态不与已提交状态共享 `homeSeen`、`homePositions` 映射；`homePositions` 只保留五个筛选键（`all`/`bilibili`/`douyin`/`weibo`/`x`）、键或时间有效的项，偏移小于 0 时取 0；损坏时改名保留后恢复默认值。更新状态 `update`：`lastRunVersion`（上次运行的版本，用来在更新后第一次启动时说明一次）、`lastCheck`、已下载待重启的 `readyVersion` 与 `readyNotes`、为更新而退出的记录 `restart`（`reason`、`windowOpen`、`fromVersion`、`at`，下次启动读到后删除，[架构 §8](architecture.md#tray)）。
- `cache-queue.json`（当前结构版本 3，DEC-050 ①，FR-USER-004，[DEC-079](../decisions.md#dec-079)，见[下文版本 4 说明](#schema-v4)）：保存“全部缓存”后台慢速缓存队列与“历史缓存补全”队列任务，使用独立文件与顶层结构版本 `version: 3`（由后端启动时的 `storage.GuardDataSchemas` 统一核对，遇到高于当前支持的未知版本时在打开任何数据库前返回 `NewerDataError` 拒绝覆盖；v2 → v3 迁移前留 `.premigrate-2` 备份并经 `WriteMigratedJSONAtomic` 原子落盘）。JSON 损坏时改名保留并从空队列恢复。每个任务只存公开调度元数据：任务 ID、站点（`bilibili` / `douyin` / `weibo` / `x`）、账号 ID（B 站 `mid`、抖音 `sec_uid`、微博 `uid`、X `rest_id` 或补全队列 `all`）、显示名称、本机头像键（如有）、列表类型（B 站 `dynamics` / `videos`，抖音 `works`，微博 `posts` / `weibo_backfill`，X `posts`）、可选的队列类别 `kind`（`weibo_backfill` 等，`omitempty` 向后兼容）、补全进度可选字段（`backfillPhase`、`backfillTotal`、`backfillSubUids`、`backfillSubIndex`、`backfillSubCursor`，全 `omitempty` 严格脱敏）、可选的来源 `origin`（`full_cache` 表示由缓存规则的“全部缓存”开的，DEC-061 ①；`backfill` 表示历史缓存补全，DEC-079）、翻页游标（微博为 `<page>:<since_id>`，X 为时间线 `Bottom` 游标，拒绝 URL、查询串与敏感字段）与 `hasMore`、可选的完成说明 `completionNote`（如微博可见范围提示或 X“已到达 X 时间线可见上限”）、计数（已缓存、跳过）、最近缓存的若干条本机摘要（本机条目键 `key` 与标题，供任务页详情点击打开本机内容，封面按条目键从媒体库读）、状态（`queued`、`running`、`waiting`、`waiting_backoff`、`paused`、`completed`、`stopped`）、暂停或停止原因、下次计划执行时间与创建/更新时间戳。**字段边界（NFR-SEC-001～005）**：严禁保存任何 Cookie、签名 URL 或带查询串的地址；应用退出时停下并保存进度，启动后随机等 1～5 分钟继续。结束的任务（`completed`、`stopped`）可以逐个或一次全部从文件中移除（DEC-057 ①），未结束的要先取消；移除不删已缓存的条目。
- `update-history.json`（DEC-051 ①，FR-UPD-001～003）：保存设置“更新”页的检查记录与版本更新记录，同样使用独立新文件与自带顶层结构版本 `version: 1`（**不改动现有 v2 数据文件结构**）。原子写（临时文件 + `fsync` + 改名，权限 `0600`）；遇到高于当前程序支持的 `version > 1` 时拒绝启动且绝不覆盖；JSON 损坏时改名保留备份并从空记录重建，记入损坏恢复提示或日志。保存最近 20 次检查记录 `checks`（时间 RFC3339、触发方式 `auto` / `manual`、脱敏结果文字如“已是最新版本”“发现 x.y.z”“检查失败：<原因>”；仅正式版写入，开发版不联网检查）与最近 50 次更新记录 `updates`（时间 RFC3339、`fromVersion`、`toVersion`、方式：`auto_update`“自动更新” / `dev_install`“本机打包安装” / `manual_install`“手动安装”）。**隐私边界（NFR-SEC-001～003）**：只保存版本号、时间、方式与脱敏结果文字，严禁保存任何下载地址、URL、查询串或凭证，读写均做清洗校验。
<a id="cache-conflicts"></a>
- `cache-conflicts.json`（[DEC-076](../decisions.md#dec-076) ④，FR-CACHE-010，NFR-DATA-005）：保存全部缓存复查及下载层冲突绊线发现的 V1/V2 缓存内容冲突与已忽略版本指纹，使用独立新文件与顶层结构版本 `version: 1`（桌面端 `service.NewBackend` 将其加入 `allSchemas` 交给 `storage.GuardDataSchemas` 统一核对，遇到 `version > 1` 在打开数据库前返回 `NewerDataError` 拒绝启动且绝不覆盖，JSON 损坏时经 `BackupCorruptFile` 改名保留备份并从空账本恢复；`trace-server` 远程版不使用该文件）。通过 `storage.WriteJSONAtomic(..., 0600)` 原子落盘。包含两部分：① `conflicts[]`（按 `itemKey` 去重，保存待处理 `pending`、处理中 `resolving` 与上次失败 `failed` 的冲突项：`id`、`itemKey`、`site`、`kind`、`targetId`、`authorUid`、`authorName`、`title`、`detectedAt`、来源 `origin`、冲突原因类别 `reasonKind`——`part_changed` / `media_changed` / `text_edited` / `resource_mismatch`、两行脱敏摘要 `localSummary` 与 `remoteSummary`、远端结构 SHA-256 指纹 `remoteFingerprint`、可选 `via`、`state`、`lastError`）；② `ignoredRevisions`（`itemKey -> {remoteFingerprint, ignoredAt}`：用户选择“保留本地”后记录当时远端指纹，后续 7 天定期 `fullCache` 扫到同一 `remoteFingerprint` 时静默跳过不重复提示，若源站再次修改产生新的 V3 指纹则重新登记冲突）。**隐私边界（NFR-SEC-001～005）**：入盘前全字段强制脱敏清洗，**严禁保存任何 `http://`/`https://` URL、CDN 主机名、查询串（`?`、`&`、`=`）、Cookie 或令牌**。
- 当前播放偏好的唯一来源为 `ui-state.json.player`；`settings.json.player` 的旧字段仍可读写和规范化，但当前播放器不读取它们。下载时是否保存弹幕、字幕仍由 `settings.json` 的下载开关控制。
- `library.db`：SQLite（`modernc.org/sqlite`，WAL 模式）索引，表为条目、媒体文件、引用、任务、任务尝试记录、待清理。它只是索引：每个缓存条目文件夹里的 `item.json` 才是正本。启动时 `PRAGMA quick_check` 失败或数据库文件无法解析，就把数据库（连同 `-wal`、`-shm`）改名为 `library.db.corrupt-<时间>` 保留，扫描各保存位置（记录在 `library-roots.json`）的 `item.json` 重建，界面提示一次。文件被占用、没有权限等打开失败不算损坏：显示错误，不改名、不重建。
  - **非阻塞增量刷新与轻量查询**：`RefreshIndex` / `rebuildIndex` 使用独立互斥锁 `rebuildMu` 防止并发刷新重叠，先在极短 `RLock` 下快照已有条目、保存根目录与待清理账本，随后**不持有 `Store.mu`** 完成磁盘 `WalkDir` 与文件核对（每个条目只核对一次 `itemDir` 链接 `validateItemDirRead`，避免多图条目重复触发 Windows `EvalSymlinks`/`GetLongPathNameW` 系统调用），并发读取（`FeedPage`、`GetItem`、`GetItemMetaOnly`、`CountCachedWorks`）不再被磁盘扫描卡死；非哈希刷新（`RefreshIndex`）仅在最终短 `Lock` 内对比并增量写入实际新增或变化的条目并协调并发 `SaveItem`/`DeleteItem`（0 变动时零 SQL 写入）。另提供纯索引只读接口 `GetItemMetaOnly`（零磁盘系统调用，供主页帖子卡片、下载任务与队列封面读取）和单条 SQLite `GROUP BY` 的 `CountCachedWorks`（一次查询统计全部四个站点账号的已缓存数，替代逐账号 N+1 查询）。
- 待清理记录同时写进 `pending-deletes.json`（不只在可重建的索引里），索引重建后删到一半的条目不会重新出现。账本读取/解析失败时原样保留、返回错误并停止删除/重建相关变更，不能当作空账本覆盖。重建扫描、清单读写或 SQL 失败时停止并保留尚未确认 stale 的旧索引，只有完整扫描成功才事务清理旧行。

<a id="schema-versions"></a>
- **结构版本与迁移前备份**（FR-STORE-001、FR-UPD-004）：每个数据文件都有结构版本（初始版本都是 1）——`settings.json`、`ui-state.json`、`history.json`、`subscriptions.json`、`cache-queue.json`、`update-history.json`、`cache-conflicts.json` 是顶层的 `version` 字段，`library.db`、`updates.db` 是 SQLite 的 `user_version`（新建或旧的 0 在启动时写入当前版本）。没有版本字段的旧文件按 1 读取。`internal/storage/schema.go`（及启动时接入的 `cache-queue.json`、`cache-conflicts.json` 版本检查）登记这些版本，后端启动时先核对再打开任何文件：
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

<a id="schema-v3"></a>
- **接入微博的按证据升级（DEC-065）**：代码核对确认 `main @ 4b37fc5` 的程序读到含 `site: "weibo"` 的文件时，会在以下 3 个 JSON 文件里**静默丢弃并回写**微博数据，因此只为这 3 个文件升版本，其余文件保持不变。迁移前统一经 `storage.GuardDataSchemas` 留下 `.premigrate-<旧版本>` 备份（备份失败不迁移），采用 `WriteMigratedJSONAtomic` 原子写入且重复打开幂等；旧程序遇到更高版本时在打开数据库前即触发 `storage.NewerDataError` 拒绝启动，避免静默清空微博数据：

  | 文件 | 版本变化 | 升级依据与旧程序（`main @ 4b37fc5`）行为 | 迁移内容 |
  | --- | --- | --- | --- |
  | `subscriptions.json` | `2 → 3`（`.premigrate-2`） | 旧版 `normalizeSubscriptionAccountForLoad` 只认 `bilibili`/`douyin`，读入时剥离全部微博账号并在下次写盘时永久覆盖，且级联导致 `updates.db` 把微博账号视作已解绑而清理 | `version` 升为 3；允许 `site: "weibo"` 账号（规范化 UID、清洗头像签名参数）；既有两站订阅与共用规则原样保留 |
  | `ui-state.json` | `2 → 3`（`.premigrate-2`） | 旧版 `Normalize()` 只认 `all/bilibili/douyin`，会把 `feedFilter.site = "weibo"` 重置为 `"all"` 并丢弃 `homePositions["weibo"]`，任意窗口状态写入即永久覆盖 | `version` 升为 3；`HomePositionSites` 与 `FeedFilter.Site` 增加 `"weibo"`；v2 已选的 `"bilibili"`/`"douyin"`/`"weibo"` 筛选与位置均原样保留（仅旧 v1 `"bilibili"` 仍一次性归 `"all"`） |
  | `cache-queue.json` | `1 → 2`（`.premigrate-1`） | 旧版 `normalizeCacheQueueTarget` 拒绝 `site: "weibo"`，加载时丢弃微博队列并在下次保存时覆写 | `version` 升为 2；支持 `site: "weibo"`、`list: "posts"`、`<page>:<since_id>` 游标与 `completionNote`；由启动期 `GuardDataSchemas` 统一做降级拦截 |
  | `library.db`、`updates.db`、`history.json`、`settings.json` | 保持 `2 / 2 / 1 / 1` | 无表结构变更，旧版读取不静默丢弃 `site = "weibo"` 行（`updates.db` 的清理风险已由 `subscriptions.json` v3 在启动前阻断） | 不迁移；`updates.db` 保持 `UpdatesDBSchemaVersion = 2`（与 `SubscriptionsSchemaVersion = 3` 拆开） |

<a id="schema-v4"></a>
- **接入 X 的按证据升级（DEC-066）**：代码核对确认 `main @ b75cf80` 的程序读到含 `site: "x"` 的文件时，同样会在以下 3 个 JSON 文件里**静默丢弃并回写** X 数据，因此只为这 3 个文件升版本，其余 4 个文件经逐行核对证明不会静默丢弃 `site = "x"` 而保持原版本。迁移前统一经 `storage.GuardDataSchemas` 留下 `.premigrate-<旧版本>` 备份、采用原子写入且重复打开幂等；旧程序遇到更高版本在打开数据库前触发 `storage.NewerDataError` 拒绝启动：

  | 文件 | 版本变化 | 升级依据与旧程序（`main @ b75cf80`）行为 | 迁移内容 |
  | --- | --- | --- | --- |
  | `subscriptions.json` | `3 → 4`（`.premigrate-3`） | 旧版 `normalizeSubscriptionAccountForLoad` 经 `site.ParseID` 剥离全部 `site = "x"` 账号，下次保存永久覆盖 | `version` 升为 4；允许 `site: "x"` 账号（`rest_id` 作 `uid`、`screen_name` 记入 `uniqueId`）；既有三站订阅与共用规则原样保留 |
  | `ui-state.json` | `3 → 4`（`.premigrate-3`） | 旧版 `Normalize()` 会把 `feedFilter.site = "x"` 重置为 `"all"` 并丢弃 `homePositions["x"]`，任意状态写盘即覆盖 | `version` 升为 4；`HomePositionSites` 与 `FeedFilter.Site` 增加 `"x"`；既有筛选与位置原样保留 |
  | `cache-queue.json` | `2 → 3`（`.premigrate-2`） | 旧版 `normalizeCacheQueueTarget` 拒绝 `site = "x"`，加载时丢弃 X 队列并在下次保存时覆写 | `version` 升为 3；支持 `site: "x"`、`list: "posts"`、X 游标清洗与可见上限完成说明；由 `GuardDataSchemas` 统一做降级拦截 |
  | `library.db`、`updates.db`、`history.json`、`settings.json` | 保持 `2 / 2 / 1 / 1` | 核对确认 `library.db` / `updates.db` 均以无枚举限制的 `site TEXT` 存行且无读时删行（`updates.db` 过期清理已被 `subscriptions.json` v4 阻断），`history.json` / `settings.json` 读取与保存保留 `site = "x"` | 不升版本；`library.db` 启动时按 `CREATE INDEX IF NOT EXISTS` 为 `download_tasks` 建 `(status, updated_at)` 索引；`history.json` 增加 X 规范链接回退与 X 敏感字段清洗 |

<a id="schema-v5"></a>
- **缓存能力标签的结构版本 5（DEC-070）**：将单选命名缓存规则（`cacheRules` + `ruleId`）替换为订阅上的原子能力标签 `tags` 时，旧版 v4 程序不读取 `tags` 且会在下次保存时用旧默认规则覆盖写回，因此仅将 `subscriptions.json` 升至版本 5（其余文件不变）：

  | 文件 | 版本变化 | 升级依据与旧版 v4 程序行为 | 迁移内容 |
  | --- | --- | --- | --- |
  | `subscriptions.json` | `4 → 5`（`.premigrate-4`） | v4 程序忽略 `tags`，读入无 `cacheRules`/`ruleId` 的文件时会把订阅回退到全关的 `"default"` 规则并覆写磁盘 | `version` 升为 5；每个订阅的激活能力（含旧 `cacheRules[ruleId]`、曾编辑过的旧 `"default"` 规则及早期 v1 `rules`/`autoCache`）100% 无损迁移为规范化 `tags` 数组并保留 `autoCache.since`，移除 `cacheRules` 与 `ruleId`；v4 程序遇到 v5 文件在打开数据库前触发 `storage.NewerDataError` 拒绝覆盖 |
  | `library.db`、`updates.db`、`ui-state.json`、`cache-queue.json`、`history.json`、`settings.json` | 保持 `2 / 2 / 4 / 3 / 1 / 1` | 不保存 `cacheRules` 或 `ruleId`，下游继续读取 `SubRules` 与 `origin = "full_cache"` | 不升版本 |

<a id="cache"></a>
## 2. 缓存

### 2.1 条目与文件夹

缓存以动态为单位（DEC-018）。条目键是 `<站点>:<类型>:<ID>`（`library.CanonicalItemKey`）：哔哩哔哩 `bilibili:dynamic:<id_str>`、`bilibili:video:<bvid>`、直播录像 `bilibili:live:<room_id>-<开播时间>`（[直播 §4](live.md#recording-item)）；抖音视频作品 `douyin:video:<aweme_id>`、图文作品 `douyin:dynamic:<aweme_id>`（与哔哩哔哩的图文动态同属“动态”类，主页与媒体库沿用同样的筛选）、直播录像 `douyin:live:<room_id>`；微博所有单条微博（含视频微博，避免编辑后换键）为 `weibo:dynamic:<mid>`，没有所属微博的独立视频页为 `weibo:video:<视频对象 ID，冒号换成下划线，如 1034_<id>>`；X 所有单条帖子（含单视频帖、混排、转帖与引用帖）统一为 `x:dynamic:<status_id>`。不带站点的旧键按哔哩哔哩解释。

```text
<保存位置>/bilibili/<mid>/dynamics/<id_str>/item.json、images/01.jpg…、orig.json（转发的原动态快照）
<保存位置>/bilibili/<mid>/videos/<bvid>/item.json、cover.jpg、P01.mp4…、P01.audio-30251.m4a（必要时，见 2.5）、P01.danmaku.xml、P01.zh-CN.vtt、P01.zh-CN.srt、P01.ai-zh.vtt…（见 2.9）
<保存位置>/bilibili/<mid>/lives/<room_id>-<开播时间>/item.json、cover.jpg、part-001.mp4…（见直播设计）
<保存位置>/bilibili/<mid>/forwards/<id_str>/…（这位作者转发的动态，结构同 dynamics/）
<保存位置>/bilibili/<mid>/forwards/<bvid>/…（转发里带的视频，结构同 videos/）
```

抖音（DEC-043）的作者目录用 `sec_uid` 的短哈希（SHA-256 前 12 个十六进制字符），不用昵称，也不用会变的抖音号；完整 `sec_uid` 写在清单的作者里：

```text
<保存位置>/douyin/<sec_uid 短哈希>/works/<aweme_id>/item.json、cover.jpg、video.mp4（视频作品）
<保存位置>/douyin/<sec_uid 短哈希>/works/<aweme_id>/item.json、cover.jpg、images/01.jpg…、images/01_live.mp4…、music.m4a（图文作品，扩展名按实际格式）
<保存位置>/douyin/<sec_uid 短哈希>/lives/<room_id>/item.json、cover.jpg、part-001.mp4…
```

微博（DEC-065）的作者目录直接用纯数字 `uid`（稳定、不含非法字符）：

```text
<保存位置>/weibo/<uid>/posts/<mid>/item.json、cover.<扩展名>、video.mp4（视频微博或独立视频页）
<保存位置>/weibo/<uid>/posts/<mid>/item.json、images/01.<扩展名>…、images/01_nowm.<扩展名>…、images/01_live.mp4…、images/02.mp4…（图文、GIF、实况、混排；`NN_nowm` 为可选无水印最大版伴侣图，DEC-078）
<保存位置>/weibo/<uid>/forwards/<mid>/item.json…（这位博主转发或快转的微博，结构同 posts/，记 via）
```

X（DEC-066）的作者目录用稳定的纯数字 `rest_id`（不随 `@handle` 改名变化）：

```text
<保存位置>/x/<rest_id>/posts/<status_id>/item.json、cover.<扩展名>、video.mp4（只有一个视频的帖子）
<保存位置>/x/<rest_id>/posts/<status_id>/item.json、images/01.<扩展名>…、images/02.mp4…（多图、GIF、混排、长帖与作者线程）
<保存位置>/x/<rest_id>/forwards/<外层 status_id>/item.json…（这位用户的转帖或引用帖，结构同 posts/，记 via）
```

“添加已有的缓存文件夹”与重建索引同样扫描 `douyin/*/{works,lives}/*/item.json`、`weibo/*/{posts,forwards}/*/item.json` 和 `x/*/{posts,forwards}/*/item.json`；`library.CountCachedWorks` 以单条 SQLite `GROUP BY` 统计全部四个站点（对微博与 X 统计该作者自己的非转发条目及 `via.uid` 指向他的全部条目）。文件名只用 ID 和序号，标题写在清单里，避免非法字符和超长路径。视频条目放在 UP 主自己的目录下，被动态引用；投稿列表和视频页直接缓存的也是同一个视频条目。三种例外（DEC-056、DEC-063），都在清单里记 `via`：

- **转发**：转发的动态（含微博的普通转发与快转、X 的转帖与引用帖）及其附带原帖图片/视频/GIF/实况媒体全部存到转发者（或订阅归属账号）的 `forwards/`，转发里带的视频（原作者不是转发者）也存到转发者的 `forwards/`（哔哩哔哩为 `forwards/<bvid>/`，微博统一落在 `<保存位置>/weibo/<转发者或订阅账号 uid>/forwards/<mid>/`，X 统一落在 `<保存位置>/x/<转帖者或订阅账号 rest_id>/forwards/<外层 status_id>/`，不写入原作者的目录；微博从订阅、博主主页或带 `via` 的入口缓存时转发者取自该上下文，直接解析一条快转且没有转发者上下文时按这条微博的 `user` 归属，快转 `user` 是快转者还是原作者待 V-05 真实确认（[微博协议 §4.3](../research/weibo.md#retweets)）），不建原作者的文件夹，也不和转发者自己的 `dynamics/`、`videos/`、`posts/` 混在一起；`via = {reason: "forward", site, uid: 转发者, name, dynamicId}`。信息流查询（`FeedPage`）与最新时间统计（`LatestPubByAuthor`）将 `via.reason = "forward"` 归属至 `via.uid`，并在 `via.uid != author_uid`（如微博快转、X 转帖）时不再归入 `author_uid` 名下。
- **合作视频**：为订阅、缓存队列或用户页里的某位成员缓存哔哩哔哩合作视频（视频详情 `staff` 里有这位成员、但他不是投稿人）时，存到这位成员的 `videos/<bvid>/`，`via = {reason: "collab", site, uid: 成员, name}`；没有成员上下文（直接解析视频链接）时仍存在投稿人的文件夹。从用户页点开视频、再在视频页缓存，也带着这个用户页的成员（DEC-061 ③）。为成员缓存的合作视频在信息流里按作者筛选时也算这位成员的（`feed_items.item_key` 属于 `via.reason = collab` 且 `via.uid` 是他的条目），媒体库作者列表（`Statistics`）把它列在这位成员下而不是投稿人下。
- **抖音共创作品**（DEC-063 ①）：从某个账号的作品列表缓存（订阅自动缓存、缓存队列与“全部缓存”、用户页作品的“缓存”）的作品，发布者不是这个账号时，存到这个账号的 `douyin/<短哈希>/works/<作品 ID>/`，`via = {reason: "collab", site: "douyin", uid: 这个账号的 sec_uid, name}`，作者仍记发布者；信息流筛选、媒体库作者列表与已缓存数和哔哩哔哩合作视频一样按 `via` 算在这个账号下。直接解析作品链接缓存的仍存在发布者的文件夹。缓存任务把 `via` 记在 `download_tasks.via_json`，任务暂停或重启后仍写进清单。

同一条目键只有一个文件夹：条目已经缓存过时沿用原来的文件夹，不搬动；想换位置就在媒体库删除后重新缓存。

- **旧版独立归档条目（`<rawID>__v1-<ts>`，[DEC-076](../decisions.md#dec-076) ⑤）**：用户在任务页“内容冲突”对某条冲突选择 **保留两版**（`keep_both`）且新版 V2 在 `.trace-stage-*` 完整校验通过后，`library.Store.ArchiveItemSnapshot` 在同级类型目录下把本地 V1 独立归档到 `<rawID>__v1-<ts>/`（条目 `id` 为 `<rawID>__v1-<ts>`，规范键为 `<site>:<kind>:<rawID>__v1-<ts>`，清单写 `archivedOf = "<site>:<kind>:<rawID>"`、`directCached = true`），再将原 `<rawID>/` 目录原子切换为新版 V2。归档条目是一等独立媒体库条目，照常出现在主页信息流、订阅“内容”与媒体库并支持离线播放与查看；其删除计划（`PlanDelete` / `Delete`）与 V2 完全独立——删除新版 V2 绝不会连带删除归档 V1（反之亦然）；同时 `archivedOf` 非空的条目在 `CountCachedWorks`（账号已缓存作品数统计）、`UpgradeCandidates`（画质升级候选列表）以及 `cache-queue` / `fullCache` 复查中自动排除，不重复计数、不重复探测。

### 2.2 清单 `item.json`

版本、站点、类型、ID、作者（mid、昵称、头像地址）、发布时间、正文、动态类型、媒体列表（角色、相对路径、字节数、SHA-256、宽高、时长）、引用的视频键、状态、缺失项（是什么、原因）、缓存时间。视频条目的每个分 P 另记：

- `video`：清晰度代码与名称、编码（`codecid`、`codecs`）、宽高、帧率；
- `audio`：音质代码（30251/30250/30280…）与名称，另存的高音质音轨（如有）；
- `available`：缓存当时站点列出的档位快照（`accept_quality`、`support_formats` 的 `quality`/`need_vip`、返回流里的最高档、是否有杜比/无损音频）与探测时间；
- `danmaku`：弹幕文件、条数、取得时间（没开设置时记 `skipped`）；`subtitles`：每种语言的 `lan`、`lan_doc`、是否 AI、VTT 与 SRT 文件；`chapters`（可选，`json:"chapters,omitempty"`，结构 `{fromSec, toSec, title}`，复用 `player/wbi/v2` 的 `view_points` 零额外请求写入，旧清单无此字段完全兼容，`item.json` 保持 `version: 1` 不升结构版本）。

可选的 `via`（DEC-056，见 2.1）说明条目为谁缓存、存在谁的文件夹：`reason`（`forward` 或 `collab`）、`site`、`uid`、`name`，转发另记 `dynamicId`；`author` 仍是原作者或投稿人。另向后兼容地支持可选字段 `links`（`[]library.ItemLink{label, url}`，按正文出现顺序记录可读超链接标签与安全 `http(s)://` 跳转地址，DEC-073）、`origin`（`*library.ItemOrigin`：原博 `site`、`id`、`mblogId`、`canonicalUrl`、`author`、`pubTs`、`title`、`text`、`links`、`card`、`unavailable`、`unavailableReason`、`videoFromOrigin`）、`card`（`*library.ItemRichCard`：`kind`、`label`、`title`、`desc`、`url`）以及 `notePages[].fromOrigin` / `notePages[].noWatermark` / `media[].fromOrigin`（区分外层与原博媒体及微博无水印伴侣图 `role: "image_nowm"`，`item.json` 保持 v1、`library.db` 保持 v2 不升结构版本）；`SanitizeItemMetadata` 自动清洗 `links`、`origin`、`thread[].links` 与 `card` 中的非 `http(s)` 协议、用户凭证、签名参数、Cookie 令牌及媒体 CDN 地址（`weibocdn.com`、`sinaimg.cn`、`video.weibo.com`、`pbs.twimg.com`、`video.twimg.com` 等）同时保留公开跳转地址与正文，`MergeItemMetadata` 与导入导出 `PortableManifest` 完整保留并合并这些字段。旧清单没有这些字段时照常读取，结构版本不变。媒体库把 `via.reason = forward` 或动态类型为 `DYNAMIC_TYPE_FORWARD` 的条目算作“转发”（`FeedFilter.Kind = "forward"`），“图片”“视频”用 `ExcludeForward` 只列作者自己的内容；按作者筛选时也匹配 `via.uid`，`library.db` 为此建表达式索引 `idx_library_items_via (site, json_extract(json_data, '$.via.uid'))`。

可选的 `cachedBy`（DEC-062）记缓存这一条的安装：`node`（`<数据目录>/node.json` 里随机生成的节点 ID）与 `name`；旧条目没有这个字段时照常读取，结构版本不变。导入与合并时保留本地的 `cachedBy`。导出、上传到网盘的副本去掉 `itemDir`、`itemKey`、`rawId`、`totalBytes`，导入后由 `SaveItem` 按内容重新算出（[远程 §6.1](remote.md#export-format)）。

为支持全部缓存更新复查与 V1/V2 冲突保护（[DEC-076](../decisions.md#dec-076)、[DEC-079](../decisions.md#dec-079)，`item.json` 保持 `version: 1` 不升结构版本，旧清单照常读取），清单向后兼容地支持以下可选字段：`cacheStandardVersion`（`int`，通用基线 `CurrentCacheStandardVersion = 2` 保持不变以免影响哔哩哔哩门限 B；微博双份无水印伴侣图与规范链接基线 `WeiboCacheStandardVersion = 3`，记录缓存、复查或历史补全评估通过时的完整性标准基线，`0` 表示旧版清单；微博条目完成无水印补全评估后写入 `3`，即便部分图 `/oslarge/` 404 或与主图同 SHA-256 跳过也不再重复补全）、`recheckedAt`（RFC3339 时间戳，上次全部缓存深度复查时间）、`editTs` 与 `editCount`（源站脱敏编辑时间戳与编辑次数）、`assetIds`（`[]string`，按顺序记录经 `SanitizeMediaToken` 清洗的平台媒体标识，严禁含 URL、查询串、路径分隔符或凭证）、`archivedOf`（当本条目为“保留两版”生成的独立旧版归档快照时，记录原正本规范条目键 `<site>:<kind>:<raw_id>`，经 `ValidateCanonicalItemKey` 校验且禁止自指），以及 `douyinVideo.mediaId` 与 `notePages[].mediaId`（单视频或混排图文页的脱敏媒体标识，用于同源升级判定、微博纯 CDN 无水印补齐与混合编辑本地文件复用）。`SanitizeItemMetadata`、`MergeItemMetadata` 与导出 `PortableManifest` 统一清洗并保留这些字段。

抖音条目的作者另记 `site`、`uid`（`sec_uid`）与抖音号；视频作品记实际与可选的档位（分辨率、编码、帧率、码率、档位名）；图文记配乐（标题、作者、时长、文件）。微博条目的作者记 `site: "weibo"` 与数字 `uid`，视频微博及混排视频记实际与可选档位；媒体库筛选“视频”除 `kind = video` 外也匹配 `media[role='video']`（排除 `gif: true`）与 `notePages[kind='video']`（排除 `gif: true`），使 `weibo:dynamic:<mid>` 与 `x:dynamic:<status_id>` 的视频与混排条目在“视频”下可见。X 条目的作者记 `site: "x"`、数字 `rest_id` 与 `@handle`，另向后兼容地支持可选字段 `thread`（`[]ItemThreadPart{id, pubTs, text, mediaIndexes}`，缓存当刻一并保存的作者自回复线程）与 `notePages[].gif` / `media[].gif`（`gif: true`，标记 X 的 MP4 动图，在看图层与播放层静音循环播放，并在媒体库中归类到“图片”而非“视频”）；仅提供 HLS 分段流的视频记入 `missing`（原因“这条视频只有分段流，暂不支持缓存”）。直播录像条目（[直播 §4](live.md#recording-item)）的 `live` 对象另含可选字段 `live.incidents`（录制异常事件列表：`reconnect`、`lost_segments`、`slow`、`disk`、`convert_failed`，每场最多 200 条，DEC-051 ③）与 `live.lastSequenceAt`（RFC3339Nano，记录上次写入的 HLS 分片时间，供同场重启或续录时防误判跳号；均不改清单结构版本，旧清单照常读取）。

只存公开内容；哔哩哔哩的图片原始 URL（公开 CDN，去掉尺寸后缀）、微博公开的 `sinaimg.cn` 图片地址与 X 公开的 `pbs.twimg.com` 图片地址（清洗带签名或 Cookie 参数的查询串）可以存，**哔哩哔哩视频签名播放地址与直播流地址、抖音全部带签名媒体地址、微博全部视频与实况签名地址（含 `ssig`、`Expires`、`KID` 与 `weibocdn.com`/`video.weibo.com` 地址）、X 全部视频与私有/带签名媒体地址（含 `video.twimg.com`、`ton.x.com`/`ton.twitter.com` 及 Cookie/Bearer/CSRF 令牌）一律不存**。播放数、转发数、点赞数、评论数这类会变的互动数据不缓存（DEC-040）。

### 2.3 内容范围

| 动态类型 | 保存 |
| --- | --- |
| 视频 | 正文 + 引用的视频条目（封面 + 全部分 P，按第 2.5 节选流） |
| 图文、带图文字 | 正文 + 全部原图；实况照片另存动态片段 `images/NN_live.mp4`（角色 `motion`，静帧的 `motion` 字段指向它；按下面的单文件 MP4 校验并保证 `moov` 在前，系统播放器也能预览；下载或校验失败删掉片段、记为部分缓存，DEC-040、DEC-046 ①） |
| 纯文字 | 正文 |
| 转发 | 转发正文 + 原动态快照，原动态的图片和视频按上两行保存，都存在转发者的 `forwards/`（DEC-056）；同一图片或视频在转发与原动态里重复出现时只存一份 |
| 专栏 | 标题、摘要、封面与摘要里的图；全文待实现（FR-FEED-002），记为部分缓存 |
| 直播、番剧、课程、音乐、商品等 | 正文与封面，记为部分缓存并写明类型不支持 |
| 充电专属视频（`upower`）与专属动态（`onlyfans`） | 视频未解锁（`is_upower_exclusive && !is_upower_play`）在调用 `playurl` 前拦截为 `forbidden`（0 次 `playurl` 请求），自动缓存记 `"forbidden"`、慢速缓存队列清晰跳过不重试；已解锁（`is_upower_play`）正常按最高画质与无损音质缓存。未解锁专属动态（`MAJOR_TYPE_BLOCKED`）保留外层展示原因、不入队缓存 |

抖音作品（FR-DY-005、FR-DY-006）：

| 作品 | 保存 |
| --- | --- |
| 视频 | 文案、发布信息、封面（`origin_cover` 优先）、一个无水印单文件 MP4（第 2.5 节） |
| 图文 | 文案、发布信息与按顺序的每一页（`images[]` 可以混排三种页）：图片页存无水印原图（`images[].url_list`，不用带水印的 `download_url_list`）；实况照片存静帧与片段（`images/NN_live.mp4`，角色 `motion`）；视频页（有 `images[i].video` 但不是实况照片）存封面 `images/NN.<扩展名>` 与视频 `images/NN.mp4`（角色 `video`，按下面的单文件 MP4 校验，选流规则同视频作品）；认不出的页存能拿到的图片并在清单的该页记 `unknownKind`。配乐 `music.<扩展名>`（角色 `audio`）；配乐没有地址（版权限制）记为缺失项并写明原因 |
| 付费、加密、已删除、私密 | 不缓存，说明原因 |

微博内容（FR-WB-005、FR-WB-006，[微博协议 §4、§13.1](../research/weibo.md#a-review-choices)）：

| 微博类型 | 保存 |
| --- | --- |
| 视频微博 / 独立视频页（`1034:`） | 正文与发布信息、封面 `cover.<扩展名>`、一个单文件 MP4 `video.mp4`（第 2.5 节选流，第 2.6 节校验并保证 `moov` 前置）；有宿主微博的独立视频页归一为 `weibo:dynamic:<mid>` |
| 图文、长微博、超过 9 图、GIF、实况、混排 | 完整正文（长微博全文）与按 `pic_ids` / `mix_media_info` 原顺序的每一项：主图 `images/NN.<按文件头定的扩展名>`（`role: "image"`，恒存官方最高清原图，GIF 原样存 `.gif`）；开启“微博同时保存无水印版”时对非 GIF 静态图与实况静帧额外保存 `/oslarge/` 伴侣图 `images/NN_nowm.<扩展名>`（`role: "image_nowm"`，`notePages[].noWatermark` 指向它，同 SHA-256 去重、`/oslarge/` 失效静默跳过不记缺失，旧条目重缓存复用主图并补齐伴侣图；媒体库与信息流不重复展示为第二张图，删除/归档/合并/刷新一并同步，[DEC-078](../decisions.md#dec-078)）；实况照片伴侣片段 `images/NN_live.mp4`（角色 `motion`，静帧 `motion` 指向它；QuickTime `.mov` 经 `mp4ff` 无损改封装为 ISO BMFF `.mp4`）、混排视频 `images/NN.mp4`（角色 `video`）与封面；单个主附件缺失记黄点，不让整条失败 |
| 纯文字 | 完整正文（含长微博全文） |
| 转发 / 快转 | 转发正文 + 原微博快照与媒体，统一存在转发者的 `weibo/<转发者 uid>/forwards/<mid>/` 并记 `via`（DEC-056）；原微博已删除或不可见时完整保留转发正文并说明原因 |
| 头条文章、投票、音乐、外链、直播卡片 | 正文与卡片摘要（不把外链或直播卡片当成视频下载），可在浏览器中打开 |
| VIP 受限、加密、已删除、无权查看 | 不缓存，说明原因 |

X 内容（FR-X-005、FR-X-006，[X 协议 §5～§6、§15.1](../research/x.md#a-review-choices)）：

| X 帖子类型 | 保存 |
| --- | --- |
| 单视频帖 | 正文与发布信息、封面 `cover.<扩展名>`、最高码率单文件 MP4 `video.mp4`（第 2.5 节选流，第 2.6 节校验并保证 `moov` 前置）；仅提供 HLS 分段流的视频记入 `missing`（“这条视频只有分段流，暂不支持缓存”），不发分片请求 |
| 多图、GIF、混排、长帖与作者线程 | 完整正文（`note_tweet` 全文与缓存当刻随根帖取到的作者自回复线程 `thread`）、按原顺序的最多 4 项附件：原图 `images/NN.<按文件头定的扩展名>`（只取 `?format=<ext>&name=orig`，不退到压缩档）、GIF 存为 `images/NN.mp4` 并在清单标 `gif: true`（静音循环）、混排视频 `images/NN.mp4` 与封面；单个附件缺失记黄点，不让整条失败 |
| 纯文字 | 完整正文（含长帖与作者线程） |
| 转帖 / 引用帖 | 外层正文 + 原帖快照（含长帖全文）与媒体，统一存在转帖者的 `x/<转帖者 rest_id>/forwards/<外层 status_id>/` 并记 `via`（DEC-056）；原帖已删除、受保护或地区不可见时完整保留外层并说明原因 |
| 投票、外链卡片、X Articles、直播与 Spaces 卡片 | 正文与卡片摘要（不把外链或直播/Spaces 卡片当成视频下载），可在浏览器中打开 |
| 受保护、已删除、封禁、地区屏蔽 | 不缓存，说明原因 |

<a id="tasks"></a>
### 2.4 任务与下载器

一个缓存请求是一个任务，任务内分步骤：取元数据 → 下载图片 → 每个分 P：取播放地址 → 选流 → 下载视频流与音频流 → 合并 → 校验 → 弹幕与字幕（2.9）→ 写清单。任务状态：排队、进行中、已暂停（带原因）、已完成、部分完成、失败、已取消。条目的状态由它的文件和引用的视频条目推算，不单独手写。任务带 `source`（`manual` / `auto` / `queue`，自动缓存与全部缓存队列见[订阅 §4](subscriptions.md#auto-cache)；`source == "queue"` 的任务在执行站点 API 请求时统一包裹 `site.WithAutomaticRequests`，计入对应站点每小时自动请求额度并遵守退避，[DEC-076](../decisions.md#dec-076) ①）。

**逐文件进度与轻量列表查询**（FR-CACHE-009）：任务开始时按计划列出文件清单（每张图片、每个分 P 的视频流、音频流、合并后的 MP4、弹幕、每种字幕），每个文件记状态（等待、下载中、校验中、完成、失败、跳过）、已下载与总字节（总字节在拿到第一段响应后才有）、速度；`TaskDetail(id)` 按需返回这份完整清单与尝试记录，`task:changed` 事件只带有变化的文件，每秒至多一次。文件清单存在 `library.db` 的任务表里（建立索引 `idx_download_tasks_status_updated(status, updated_at)`，不升结构版本），重启后仍可看到；已完成的任务保留清单直到任务记录被清理。为避免任务增多时列表与状态圆点反复反序列化全部任务的 `files_json` 与 `attempts_json`，列表与诊断改走 `ListTasksSummary`（跳过明细 JSON），状态圆点与活跃计数（`Backend.States`/`ActiveWork`）及批量重试扫描改走 `ListNonTerminalTasks`（`WHERE status NOT IN ('completed', 'canceled')`），`fillPartialReason` 使用 `GetItemMetaOnly`（零磁盘系统调用），批量暂停与重试经 `SetOnTaskBatchUpdate` 聚合为单次批量通知，避免触发 M 次重复重载风暴。

**分段下载器**（FR-CACHE-005，[媒体与存储选型 §5](../research/2026-11-05-media.md#download)）：

- 带公共请求头和 `Referer: https://www.bilibili.com/`；写到 `*.part`。
- 每次请求一个 8 MiB 的闭区间 `Range: bytes=N-M`，要求 `206` 且 `Content-Range` 的起点与总长一致；第一段若返回 `200`（不支持续传），按整段读取并核对 `Content-Length`；后续段返回 `200` 时截断 `*.part`、换下一个节点从头下；从头下按失败计，同一文件超过 5 次就暂停（网络），不会无限重下。`downloadRange` 仅在本地 `.part` 已有字节（`offset > 0`）时从 `t.Files` 继承 `totalSize`（若 `offset > totalSize` 则截断重置）；`offset == 0` 时始终重置 `totalSize = -1`，以本次响应的 `Content-Range` 总长为准，防止跨流回退或历史残留字节数造成 `Content-Range` 永久不匹配并支持旧任务重试自愈。
- 段内错误分类：
  - **传输错误**（读到的字节少于区间长度就结束、超时、连接重置、TLS 错误、HTTP 5xx）：保留已写入的字节，从断点继续，同一段最多重试 5 次，间隔 1、2、4、8、16 秒加 ±20% 抖动；每次失败后换到候选地址列表里的下一个。
  - **地址失效**（403、404、410）：换下一个地址；全部失效时为这个分 P 重新取一次播放地址（只一次），按相同的 `id` 和编码在新结果里找对应流，从断点继续。
  - **风控**（412，或重取播放地址时遇 -352、-412）：立即暂停任务，原因“风控”，不重试。
  - **未登录**（-101）：按[架构 §4](architecture.md#bilibili-client)处理。
  - 重试或地址都用完：任务“已暂停（网络）”，已下载的字节保留，用户点继续后从断点续传。
- **候选地址顺序**：主地址和备用地址里，官方节点（`upos-*` 等 `*.bilivideo.com` 域名、标准端口）排在前面，PCDN 节点（`*.mcdn.bilivideo.cn`、带非标准端口的地址）排在后面；同一分 P 里，最近一次成功的节点优先。
- **尝试记录**：每次失败记一条 `{时间, 文件, 偏移, 节点域名, 尝试序号, 错误类别, HTTP 状态}` 到任务尝试记录（每个任务最多保留 200 条），供任务详情显示；不记查询串和完整地址。
- 结束时核对总长度；校验通过后改为正式名。已改为正式名的流文件和分 P 在继续任务时直接复用，不重下（复用前核对长度与清单）。
- 图片只接受 `image/*`，单张上限 128 MiB（与在线原图读取、预览暂存和本机图片读取上限对齐，兼容微博 `largest` 原图、长图与大体积多帧 GIF），同样分段续传。所有站点图片、视频封面与直播封面均先写临时文件，经二进制文件头（魔数）与源站失效占位图校验通过后再原子改名；若目标槽位已存在归属本条目的有效非占位图片，重新缓存或重试遇到源站失效/占位图（含转发原帖被删除）时一律保留原有本地有效图片与清单记录，绝不删除或覆盖（未登记的用户自放文件仍报 `storage` 冲突保留，INV-09、DEC-075）。
- **并发与节奏**：同时进行的任务数取设置（默认 2，1～4，改动对之后开始的任务生效）；每个任务内的文件按顺序下载，所以同时下载的文件数也不超过这个值；取元数据和播放地址走客户端的 500 毫秒间隔。
- **错误**：内容不可得 → 该项记为缺失，任务部分完成；磁盘写入失败或空间不足 → 任务失败，类别 `storage`。
- **重启**：启动时把“排队”“进行中”的任务改为“已暂停（应用关闭）”，不自动开始。
- **批量重试（`RetryActionable` / `RetryAllTasks`）**：按 `IsRetryableTask` 仅匹配 `failed`、`partial` 及非用户手动暂停（`pause_reason != "user"`）的 `paused` 任务，不触碰用户手动暂停、已完成、已取消或活动中的任务；服务层先按四站点登录态过滤 `allowedSites`，只扫描非终态摘要任务并在单个 SQLite 事务中按 `(site, kind, target_id, cid)` 对活动任务与批内去重、清空 `pause_reason`/`error_kind`/`error_message` 并清除 `initialPlans`/`taskFaults` 以便重试时重新规划最新地址，最后通过 `SetOnTaskBatchUpdate` 发出单次聚合通知唤醒下载队列并返回 `BatchRetryResult`（全部因未登录跳过时返回 `auth_required`）。
- **取消**：先停 worker，删除本任务写入前已持久化认领的未完成临时文件；认领以完整条目目录、相对路径及任务/清单登记为准，不能仅凭同名 `.part` 认领。已经校验落盘的文件与未认领的用户文件保留，条目状态按实际文件重新推算。暂停/取消后，worker 退出才释放并发名额，迟到进度不能把任务改回进行中；任务状态写库失败显示 storage，恢复后可重试。

### 2.5 选流（DEC-024）

- **视频**：用协议资料 §14 的清晰度排序表，在这次返回的 `dash.video[]` 里取最高的一档（`videoQuality` 为上限时取不超过上限的最高档）；同档内按 `codecOrder` 选编码（默认 AVC → AV1 → HEVC：AVC 在 Windows Media Foundation 下全部机器原生可解；AV1 / HEVC 在装有系统视频扩展时可解，不可解时播放层提示并可一键调用系统播放器打开）。实际编码与偏好不同时记在清单里。
- **音频**：`audioQuality=best` 时按 无损（`dash.flac.audio`，30251）> 杜比（`dash.dolby.audio[]`，30250）> 192K > 132K > 64K 取最高；响应完全没有音频时只存视频。杜比或无损音轨只有取得应用内解码证据后才直接合并；目前将两者原样另存为 `Pxx.audio-<代码>.m4a`，主 MP4 合并最好的普通 AAC，清单记两者关系（[媒体选型 §1.1](../research/2026-11-05-media.md#hires-audio)、Q-005）。已返回最高音轨但 AAC 后备缺失/下载失败时，保留完成的最高轨并记缺失/失败，不生成静音的完成主视频。
- **多 P**：`multiPart=all` 时全部分 P。
- **抖音视频**（DEC-043 ⑤）：档位都在作品详情的 `video.bit_rate[]` 里，不另请求播放地址。先排除带水印地址与 `is_bytevc1 == 2`，再按分辨率档（短边归一到 2160/1440/1080/720/540/480）取最高，`videoQuality` 为上限时取不超过上限的最高档；同档内按 `codecOrder`，再按帧率、码率、文件大小。同一档的地址列表里直连 CDN 优先，回源跳转地址排最后（[抖音协议 §6.5](../research/douyin.md#playurl)）。`videoQuality = best` 尝试原画跳转流（`cacheDouyinOriginal`）时启用 `PublicRedirects: true`（保持 HTTPS 443、公网 DNS 校验、剥离 Cookie 与最多 5 跳），使逐段 302 跳转至公网 CDN 边缘不被误拦截；原画流失败丢弃 `.orig.part` 时一并清空任务及 `t.Files` 已记录的总字节数（`resetFileProgress`），避免回退转码流（`VideoURLs`）继承原画流总长导致 `Content-Range` 校验失败。
- **微博视频与失效占位图回退**（DEC-065、DEC-075）：从 `playback_list` 过滤掉雪碧图（`scrubber_hd`）与隐藏档，按分辨率档（受 `videoQuality` 上限约束）、编码偏好（`codecOrder`）、帧率与码率排序选最高可用 MP4，缺少 `playback_list` 时回退老 `media_info` 字段；下载走无 Cookie 的 `WeiboMediaTransport`（`Referer: https://weibo.com/`，`PublicRedirects` 关闭；实况首跳专用通道仅首跳放行 `video.weibo.com/media/play`），403 地址过期每个文件最多重取一次详情并按已落盘字节 `Range` 续传，拒绝 HTML/JSON 伪装响应；遇到微博 `risk_control` / `rate_limited` 只暂停微博队列任务（`pauseSitePeers("weibo")`），不影响另外三个站点。对于微博非 GIF 图位及 GIF 图位返回的新浪 `480×360` 单帧灰度删除占位 GIF（不误拒真正多帧微博 GIF），不将其当作原图完成，先换边缘主机（`wx1..wx4.sinaimg.cn`）并把其他档位升到 `/large/` 原图重试，下载任务再经最多 1 次详情重取后尝试无签名公共 CDN 缓存回退（见下条 DEC-075），仍不可得时记入 `missing`（原因 `WeiboImageUnavailableReason`：“图片已在源站失效”）并标记条目为部分完成；旧版已经存下的占位 GIF 在重试或重新缓存时不再复用，删除后重新取。**只存原图**（2026-11-24 用户要求）：备用地址只用与原图同尺寸的档位（`largest`、`original`、`woriginal`、`large`，以及尺寸不小于原图的 `mw2000`），不退到 `bmiddle`、缩略图或更小的档位，原图取不到就记缺失，可以重试。
- **失效无签名公开静态图的公共 CDN 缓存回退**（DEC-075，[研究资料](../research/2026-11-25-expired-image-cdn.md)）：在线读图（`FetchImageLimited`）与缓存下载始终先直连源站 CDN（含边缘轮换与最多 1 次详情重取）；**仅当**源站返回 HTTP `403`/`404`/`410` 或已知失效占位图时（遇到 `429` 限流、`412`/`414`/`418`/`432` 风控或未登录立即停止，绝不回退），对通过严格白名单与签名黑名单检查的**无签名公开静态图片 URL**（`*.sinaimg.cn` 排除 `*.us.sinaimg.cn` 且空查询串、`pbs.twimg.com` 仅含 `format`/`name`、`*.hdslb.com` 剥离 `@` 后缀的静态图；凡带 `ssig`、`Expires`、`KID`、`a_bogus`、`x-signature`、`x-expires`、`auth_key`、`token`、`auth`、`sub`、`auth_token`、`ct0` 等签名/令牌参数或私有域名的地址一律拒绝）按顺序查询 Jetpack Photon（`i0.wp.com`，仅无查询串地址，`<= 50 MiB`）→ `wsrv.nl`（`?url=<encoded>&n=-1`，保留多帧动图）→ Internet Archive Wayback Machine（`/web/0id_/<url>`，取最早一次原始二进制快照，仅允许 `archive.org` 域内最多 3 跳跳转）。所有回退请求强制剥离 `Cookie`、`Authorization`、`X-Csrf-Token`、`Referer`、`Origin`，走 `GuardedMediaDialer`（遵循用户系统代理 `site.Proxy` 并阻断私网/回环 IP），返回字节必须通过严格二进制魔数与非失效占位图校验才采用。
- **X 视频、图片与 GIF**（DEC-066）：视频从 `video_info.variants[]` 取 `video/mp4` 最高码率单文件，走无 Cookie、无 `Referer`、`PublicRedirects` 关闭的 `XMediaTransport`（白名单 `pbs.twimg.com`、`video.twimg.com`），403/404/410 过期每个文件最多经 `TweetDetail` 重取一次并按已落盘字节 `Range` 续传，拒绝 HTML/JSON 伪装响应；仅有 HLS 分段流（`.m3u8`）的视频不发分片请求，记入 `missing`（`XHLSOnlyMissingReason`：“这条视频只有分段流，暂不支持缓存”）。图片只取 `?format=<ext>&name=orig` 原图并按文件头定扩展名，不退到重新压缩的 `?name=large` 或默认尺寸（2026-11-24 用户要求），原图取不到就记缺失，可以重试；GIF 以单文件 MP4 存入 `images/NN.mp4` 并在清单标 `gif: true`；遇到 X `risk_control` / `rate_limited` 只暂停 X 队列任务（`pauseSitePeers("x")`），不影响另外三个站点。
- 付费或会员内容只给试看时，不存试看片段，记为部分缓存并写明原因。

<a id="merge"></a>
### 2.6 合并、校验与状态圆点

- **中间流的文件名**：合并前的视频流与音频流写成 `.trace-Pxx.video-<清晰度代码>-<编码 ID>.m4s`、`.trace-Pxx.audio-<代码>.m4s`（下载中再加 `.part`），与其他临时文件一样以 `.trace-` 开头，所以上传与同步工具排除 `*.part`、`.trace-*` 就不会碰到它们（[远程 §6.1](remote.md#export-format)）。旧版本留下的 `Pxx.video-….m4s` 在续传前先改成新名字，合并成功后旧名字的登记也一起清掉。
- **合并**：用 mp4ff 把视频流和音频流合成一个 MP4（Q-003）：`ftyp` + `moov` 在前、音视频约 1 秒一块交错写入、带编辑列表、各轨时长与源流一致（NFR-MEDIA-001）。合并后音视频两轨的长度差不超过 2 秒或较长一轨的 3%（与站点时长的容差同一条规则）：哔哩哔哩分开转码的两路流本来就可能差几秒（2026-11-23 实例：视频 564.0 秒、音频 569.1 秒），固定 2 秒会让这类长视频永远合并失败。合并成功后删掉中间流文件；合并失败时保留两个流文件，记为部分缓存“合并失败”。
- **单文件 MP4**（抖音视频、混排图文视频页、微博视频与混排视频、X 视频/混排视频与 GIF、三站点实况片段；哔哩哔哩实况片段不核对预期时长，DEC-046 ①，[ACT-019](https://github.com/chialecode/trace/blob/f8af16863dbe885cfd63de048730c07a2b91e7be/USER-ACTIONS.md#act-019) 实测站点给的是 `moov` 在前的 AVC MP4）：不合并。下载完成后以懒加载模式核对顶层 box 总长与文件长度一致、非分片结构（不含 `moof`）、至少含一条有效非加密视频轨（有音频轨就一起核对，拒绝 `encv`/`enca`/`sinf` 等 CENC 加密样本项）、正时长且与站点给的预期时长误差不超过 2 秒或预期时长的 3%（取大的，DEC-060 ①；不要求 `elst` 编辑列表）。`moov` 已在 `mdat` 之前时直接计算 SHA-256 通过；`moov` 在 `mdat` 之后时用 mp4ff 在磁盘上把 `moov` 移到 `mdat` 之前（按文件区间流式顺序拷贝 `mdat` 载荷、不把整个文件读进内存，平移全部轨道的 `stco`/`co64` 块偏移并在超 32 位上限时自动升级为 `co64`，写同目录临时文件、`fsync` 后原子改名；改写失败清理临时文件、保留原文件不动并记为部分缓存）。微博实况片段若是 QuickTime `.mov`（`ftyp=qt  `，含 `wide` box、QuickTime `mhlr` `hdlr` 或 `wave` 包装的 `mp4a`），由 `EnsureLivePhotoMP4Context` 用 `mp4ff` 无损改封装为标准 ISO BMFF `.mp4`（`ftyp=mp42/isom`，规范化 `hdlr`/`mp4a`、`moov` 前置、256 KiB 分块流式拷贝载荷，零转码、零新增依赖）并存为 `images/NN_live.mp4`，保证应用内 Media Foundation 与系统播放器均可播放。图片按文件头与 `Content-Type` 决定扩展名，拒绝 HTML/JSON 伪装的响应。
- **校验**：文件长度与响应一致；合并后的 MP4 能被解析出视频轨（和音频轨），时长与站点给的误差不超过 2 秒或站点时长的 3%（取大的；转码后的流可能比站点标称短几秒，DEC-060 ①），失败原因写明缺轨、结构不对或时长相差多少；记录 SHA-256。全部通过后才把 `*.part` 改为正式名并写清单（INV-06）。
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

- 触发：打开已缓存视频的详情页（`upgradeCheckOnOpen`，同一视频 24 小时内最多一次，只取第 1 P 的播放地址）；任务页“检查画质更新”（`UpgradeCandidates` 给出最多 50 个最久未检查的视频，排除 `archivedOf` 非空的独立归档快照，前端逐个调用 `CheckUpgrade` 取第 1 P 的播放地址，走 500 毫秒间隔，显示“正在检查 i / n”、可以取消，遇到风控、限流或未登录立即停止）。
- 比较：把这次返回流里的最高档（视频按排序表，音频按第 2.5 节的顺序）与清单里已缓存的档比较；更高时在清单 `available` 里记录，并标记条目“可升级”（黄点，说明“可升级到 4K / 杜比全景声”）。
- 升级：`Upgrade(key)`（及队列触发的 `UpgradeWithSource`）建一个任务，只为档位低于最高档的分 P 重新选流下载，按[第 2.10 节](#conflict-protection)在 `.trace-stage-*` 完成校验并经 `.trace-prev-*` 原子替换旧文件，单调保留已校验弹幕、字幕与独立音轨；遇到换 `cid` 冲突或远端降级时分别触发冲突绊线或拒绝覆盖，旧文件原封不动。

<a id="delete"></a>
### 2.8 删除（FR-LIB-003）

- 删动态条目删除它自己的文件；它引用的视频条目只在没有其他动态引用、也不是被直接缓存的情况下一起删除，否则保留并告知。
- **只删登记过的文件**：按清单删除媒体文件、`orig.json`、本条目的 `*.part` 临时文件，最后删除 `item.json`；然后自下而上只删除已经变空的文件夹（`images/`、条目文件夹），再向上删除因此变空的类型文件夹（`videos/`、`works/`、`posts/`、`forwards/` 等）与作者文件夹（`<mid>/`、`<sec_uid 短哈希>/`、`<uid>/`、`<rest_id>/`）；只在保存位置内向上，站点文件夹（`bilibili`、`douyin`、`weibo`、`x`）与保存位置本身不删，一律用只能删空文件夹的 `os.Remove`。文件夹里还有未登记的文件（例如用户自己放的）时保留文件夹，界面说明“文件夹里还有 N 个不是 TRACE 创建的文件，已保留”。任何情况下都不对条目文件夹做递归删除（INV-09）。
- 确认弹窗先调 `PlanDelete(key)`：它与 `Delete` 用同一份删除计划，列出会删除的条目与登记文件、会保留的共用视频（“这条动态引用的 N 个视频单独缓存过或还被别的动态引用，将保留”），不改动任何东西。
- 先改索引并写待清理记录，再删文件；删不掉的（被占用等）留在待清理里，下次启动再删，并跳过已经被重新缓存的条目。
- **改保存位置**：只影响之后的新任务；索引给每个条目记它的保存位置，旧条目照常浏览。
- **可选迁移旧文件**：无进行中/排队缓存任务与录制时才开始；已暂停 worker 完全退出后才能迁移，期间阻止新队列/worker 启动。只复制清单登记的文件，新位置有同名用户文件则停止。副本校验与根登记成功、设置原子落盘成功后才删除登记的原文件；此前失败回滚新副本与根，保留原文件，不搬动整个目录。
- 正在录制的直播条目不能删除（先停止录制）。

<a id="danmaku-subtitles"></a>
### 2.9 弹幕与字幕（FR-CACHE-008）

- 每个分 P 合并校验通过后执行；设置 `danmaku`、`subtitles`、`aiSubtitles` 分别控制，关闭时清单记 `skipped`，不算缺失。
- **弹幕**：按分 P 时长请求 `ceil(时长 / 360 秒)` 个分段（[协议资料 §18](../research/bilibili.md#danmaku)），走 API 的 500 毫秒间隔；protobuf 用 `google.golang.org/protobuf/encoding/protowire` 手工解码，未知字段跳过；合并全部分段、按 `idStr` 去重、按出现时间排序，写成通用 XML（原子写，先 `.part` 再改名）。某一段失败：整份弹幕记为缺失（不写不完整的文件），原因按错误分类；风控、未登录按 2.4 的规则暂停任务。
- **字幕与章节看点**：`player/wbi/v2` 取字幕列表（校验 `bvid`/`cid`），跳过空地址；AI 字幕只在 `aiSubtitles` 开启时保存；逐个下载字幕 JSON（`*.hdslb.com`，上限 5 MiB，地址含 `auth_key` 只在内存）转成 `P<nn>.<lan>.vtt` 与 `.srt`。同时从本次 `player/wbi/v2` 响应的 `data.view_points` 零额外请求解析视频章节看点（过滤无效项、去重并按 `from` 升序排列），写入该分 P 清单的 `chapters` 可选字段（同 `cid` 升级时单调保留）。没有字幕或章节不算缺失；某个语言下载或解析失败时这一项记为缺失。
- 弹幕和字幕文件登记在清单的媒体列表（角色 `danmaku`、`subtitle`），通过 `Library.MediaPath` 白名单按登记提供；删除时与其他登记文件一起删。
- 缺失项让条目变黄（“补全”只补缺的弹幕或字幕，不重下视频）。在本功能之前缓存的视频，清单里没有 `danmaku`/`subtitles` 字段，按“未保存”处理：圆点不变，详情页显示“补存弹幕和字幕”，点击后建一个只取弹幕与字幕的任务。
- 弹幕是缓存那一刻的快照，之后不自动更新。

<a id="conflict-protection"></a>
### 2.10 更新复查、冲突绊线与零数据丢失替换（DEC-076）

当本地条目目录已有有效缓存媒体时，不论入口是手动/定期“全部缓存”队列（[订阅 §4](subscriptions.md#full-cache)），还是手动重新缓存、“补全”、“升级缓存”或任务“重试”，下载层与媒体库层共同执行六条零数据丢失不变量（[DEC-076](../decisions.md#dec-076)）：

1. **下载层全入口冲突绊线（`ErrConflictDetected`）**：未携带显式冲突解决模式（`ConflictResolveKeepBoth` / `ConflictResolveUseRemote`）时，下载执行器在修改任何本地文件或 `item.json` 前核对远端与本地结构——若 B 站已缓存分 P 的 `cid` 改变、删减或重排，或抖音/微博/X 条目类型翻转、`mediaId`/`assetIds` 或页数/页类型改变，或编辑计数/时间戳推进且正文实质改写，立即中止写入，经 `Manager.SetConflictHandler`（`emitConflict`）把脱敏冲突通知记入 `cache-conflicts.json` 并发出 `conflicts:changed` 事件，返回 `ErrConflictDetected`，本地 V1 原封不动。
2. **远端失效、受限与降级硬拦截（`ErrRemoteDegradedRefused`）**：只要本地已有有效媒体，而执行当刻远端已删除/不可见、仅返回试看/付费未购/加密流、地域受限、微博单帧灰度失效占位 GIF、仅剩 HLS 分段流、返回清晰度低于本地已缓存档位，或有效媒体数为 0，**即使上下文显式传入 `use_remote`（“用新版替换”）或 `keep_both` 也一律硬拦截**，返回 `ErrRemoteDegradedRefused`，绝不覆盖或清空本地 V1。
3. **同源升级的单调补充与元数据保护**：同一 `cid` / `mediaId` 的安全升级或补全过程中，严禁覆盖或删除本地已校验通过的弹幕快照 `Pxx.danmaku.xml`、已校验字幕文件 `Pxx.<lan>.vtt`/`.srt` 与独立高音质音轨 `Pxx.audio-*.m4a`；转发条目若本地已有有效原帖 `Origin`（含 `fromOrigin` 媒体）而远端原帖后来变为不可见（`Origin == nil` 或 `Origin.Unavailable`），完整保留本地 `Origin` 与原帖媒体；本地已有完整长文（`LongText`）绝不被列表截断文本覆盖。
4. **混合编辑复用本地已校验附件**：执行 `keep_both` 或 `use_remote` 组装 V2 时，若 V2 保留了未改动的分 P 或图片/视频页（`cid` 或 `mediaId` 与本地 V1 一致，且本地正式文件存在并与 `item.json` 登记的 SHA-256 吻合），当该未改动附件在远端 CDN 已过期失败时，直接把本地已校验 V1 文件复用到 `.trace-stage-<token>/` 中，避免丢失未改动附件。
5. **归档优先（`keep_both`）**：任务页批量默认选项为无损的 **保留两版（推荐）**。全部 V2 文件在 `<itemDir>/.trace-stage-<token>/` 完整下载并通过容器、时长与 SHA-256 校验后，先调用 `ArchiveItemSnapshot` 将本地 V1 完整归档到独立目录 `<rawID>__v1-<ts>/`（标 `archivedOf`）；归档失败则立即中止并保留原 V1 不动。
6. **`.trace-stage-*` / `.trace-prev-*` 原子交换、文件锁回滚与启动自愈**：条目目录内的新版暂存目录 `.trace-stage-<token>/` 与旧版备份目录 `.trace-prev-<token>/` 均以 `.trace-` 开头（满足 `storage.IsTempName`，远程与网盘同步过滤规则 `*.part`、`.trace-*` 自动忽略，[远程 §6.1](remote.md#export-format)）。正式切换时先将将被替换的 V1 正式文件移入 `.trace-prev-<token>/`，再把 `.trace-stage-<token>/` 中的 V2 文件移入 `itemDir`、调用 `SaveItem` 原子写回 `item.json` 与索引，全部成功后才清理 `.trace-prev-<token>/` 及 V2 不再引用的多余登记旧文件（`RegisteredFiles(v1) - RegisteredFiles(v2)`）与空子目录；若任一改名（如内置播放器正在播放触发 Windows 共享锁）或 `SaveItem` 失败，立即将 `.trace-prev-<token>/` 中的全部原文件移回原位并清理 `.trace-stage-<token>/`，冲突项标为 `failed` 并附“（本地原文件未改动）”。若进程在切换瞬间崩溃，下次启动或读取条目时发现 `.trace-prev-*` 内文件匹配当前 `item.json` 登记的 SHA-256 而正式路径缺失，先自动恢复正式文件，再清理残留 `.trace-stage-*`。
7. **显式手动「强制重新下载」（`ForceOverwrite`，[DEC-079](../decisions.md#dec-079)）**：仅允许用户从已缓存详情页 `⋯ → 强制重新下载` 经二次确认弹窗手动触发（`download.WithForceOverwrite(ctx)` / `ForceRecacheItem`），`source="auto"`、`source="queue"` 与 `site.WithAutomaticRequests` 路径严禁开启。执行时跳过本地既有图片/视频/音频/伴侣文件复用，全部媒体在 `.trace-stage-<token>/` 中重新从源站下载并校验（拒绝半残覆盖），但**仍严格执行第 2 条 `ErrRemoteDegradedRefused` 硬拦截**：源站已删除、404/410、占位图、试看、受限或降级时立即报错并清理暂存目录，**绝不覆盖或破坏本机原文件**；全部校验通过后再经第 6 条 `.trace-prev-*` 原子替换本地文件并在文件锁失败时回滚。微博与 X 详情页已缓存时的主按钮「重新缓存」则保持非破坏性按新标准补全（复用有效主图、补齐缺失的 `NN_nowm` 伴侣图与规范链接）。

<a id="media-server"></a>
## 3. 本机媒体路径白名单（`Library.MediaPath`）

改为 `mygo/ui` 原生界面（DEC-032）后，图片查看器与视频/弹幕/字幕播放器在 Go 进程内直接读取本机文件，不再经过 WebView 自定义协议。所有本机媒体读取统一通过 `Library.MediaPath(key, relPath)`（暴露为 `LibraryService.MediaPath`）解析绝对路径：

- **登记白名单（INV-08）**：只允许打开索引与 `item.json` 里登记在该 `key` 下的媒体文件（封面、图片、`Pxx.mp4`、`video.mp4`、`images/NN_live.mp4`、`images/NN.mp4`、`music.*`、`part-NNN.mp4`、`Pxx.danmaku.xml`、`Pxx.<lan>.vtt`/`.srt`）；未登记的文件名或属于其他条目的路径一律拒绝。在线浏览未缓存的抖音图文配乐、混排视频页或各站点实况片段时，经 `StagePreviewMedia` 在白名单、不带 Cookie 条件下暂存到 `<数据目录>/cache/preview/` 后再交给本地播放器。
- **路径与链接安全**：路径在服务端由“条目所在保存位置 + 条目相对目录 + 清单里的相对路径”拼出；每段路径拒绝 `.`、`..`、反斜杠、空字节与冒号，用 `filepath.Rel` 确认没有跑出条目文件夹，并用 `os.Lstat` 与 `filepath.EvalSymlinks` 拒绝符号链接或目录联接（Junction）逃逸（INV-08）。读取时（`MediaPath`）条目文件夹的上级目录在 3 秒内核对过就不再逐级核对（DEC-057 ⑤，主页一屏几十张缩略图不必每张都逐级 `EvalSymlinks`）；文件本身与条目文件夹每次都核对，写入与删除不用这个缓存。
- 缩略图：图片直接读取缓存原图，按显示大小解码（单图最长边 960 px，九宫格与小封面 480 px），解码结果放进按最近使用淘汰的缓存（最多 600 张、320 MiB），切回刚看过的站点筛选时不重新解码（DEC-057 ⑤）；视频与直播录像使用登记的封面文件 `cover.jpg`。

<a id="feed"></a>
## 4. 主页信息流（FR-FEED-001、DEC-037）

- 来源：索引里所有动态条目、没有被任何动态引用、被直接缓存的视频条目（显示成一条“视频”帖子），以及直播录像条目（显示成一条“直播录像”帖子，发布时间取开播时间）。被动态引用的视频不单独出现。只要有清单就出现，不管圆点颜色。
- 排序键：固定按 `(发布时间, 条目键)` 从新到旧（`order` 恒为 `newest`，不再提供正序/倒序切换，DEC-037）。分页用双向键集游标，`direction` 为 `older`/`newer`/`around`，每页 30 条；同一发布时间按条目键稳定排序，翻页不重复不遗漏。
- 位置（DEC-053 ①，替代 DEC-040 的“不恢复位置”）：每个站点筛选的主时间线记 `homePositions[filter] = {key, pubTs, offsetPx}`：`key` 是上一帧可见区最上面那条，`offsetPx` 是它的顶边在列表顶边之上多少像素（由行与列表上一帧的布局框算出）；位置不变约 1 秒后保存，离开主页、点开单订阅、切换筛选与关闭窗口（含关到托盘、退出与 `--quit-for-update`，都经 `App.Close`）时立即保存；与上次写入相同就不写。每次启动后第一次显示某个筛选时，以 `{key + "\x00", pubTs}` 为游标读 `direction: older` 的一页（包含这条本身；这条已删除时就是不晚于它的最近一条），列表先定位到第 0 行、布局后再按偏移滚动（这条已删时偏移为 0），期间不在两端翻页；随后 `feedMoreBefore = true`，在顶端按 `direction: newer` 载入更新的内容，列表按条目键保持当前行不跳。没有记录或读不到时从最新处开始。同一次运行内筛选之间来回切换时，各筛选的条目与位置留在内存里，回来时按那条与偏移重新定位。单订阅视图不记位置。不提示“N 条更新的内容”。`anchor` 仍在可见条目有更新的发布时间时由 `SaveAnchor` 记下，只作没有 `homeSeen` 项的订阅的初始已读基准。保存失败显示原因（同一位置只报一次）。
- 主页侧栏订阅列表（DEC-053 ②，替代“有更新”头像行）：按 `library.Statistics()` 的各作者最新缓存发布时间 `latestPubTs`（只算当前站点筛选的站点）聚合到订阅（合并关联账号），列出有内容或正在直播的订阅（没订阅的作者不列）。未读：`latest > seen`，`seen` 为 `homeSeen["sub:<id>"]`，没有这一项时为启动时读到的 `anchor.pubTs`，为 0 时算已读。顺序快照：每次进入主页时（每个站点筛选第一次列出时）按“正在直播（组内未读在前）→ 未读 → 已读、组内按 `latest` 从新到旧”排一次，存为订阅 ID 序列；停留期间只按快照显示，新符合条件的订阅追加到末尾，删掉的订阅去掉。行列表只在订阅、统计、站点筛选或直播集合变化时重建（按签名比较），重绘只为可见行查直播圈与圆点。点击后先读取该订阅的已缓存内容，成功才进入单订阅视图并把 `homeSeen["sub:<id>"]` 写为原值与 `latest` 的较大者；查询失败保留主时间线，保存失败显示原因。单订阅视图使用独立列表，返回时按主时间线那条与偏移重新定位并恢复分页错误。统计在本机刷新和媒体库变更时读取，失败保留上次可读结果，不在每次重绘查询；帖子的显示数据按条目缓存在内存里，任务、媒体库、订阅或直播事件到来时整体重建，重绘时不逐帧读媒体库；本机图片在后台解码并缩到最长边 1280px，界面线程不解码原图（DEC-040）；主页图片只用本机文件或已有内存位图，缺失时占位，不自动补发远程图片请求。
