# 技术设计：订阅、检查更新与自动缓存

<a id="subscriptions"></a>
TRACE 以订阅为逻辑入口（DEC-026）：一个订阅代表一个被追踪的人，关联一个或多个站点账号。本页说明订阅的数据、检查更新、定时调度与退避、自动缓存和关注导入；需求见[需求 §2.4](../requirements.md#subscription-scope)，界面见[界面规范 §3.9](../ui.md#subscriptions)，接口见[架构 §3](architecture.md#services)，开播检查与录制见[直播](live.md)。

<a id="model"></a>
## 1. 模型与文件

`subscriptions.json` 是订阅的正本（[数据 §1](data-and-cache.md#storage)），结构：

```json
{
  "version": 5,
  "subscriptions": [{
    "id": "<128 位随机数的十六进制>",
    "name": "显示名称（≤ 64 字）",
    "note": "备注（≤ 500 字）",
    "avatar": {"site": "bilibili", "uid": "94697"},
    "createdAt": "2026-11-07T08:00:00Z",
    "accounts": [
      {"site": "bilibili", "uid": "94697", "name": "昵称快照", "face": "https://…", "addedAt": "…"},
      {"site": "douyin", "uid": "MS4wLjABAAAA…", "uniqueId": "抖音号快照", "webRid": "可选，最近一次能用的直播间号（缓存，可失效）", "name": "…", "face": "…", "addedAt": "…"},
      {"site": "weibo", "uid": "100000001", "name": "昵称快照", "face": "https://…", "addedAt": "…"},
      {"site": "x", "uid": "100000001", "uniqueId": "screen_name 快照", "name": "昵称快照", "face": "https://…", "addedAt": "…"}
    ],
    "links": [
      {"label": "可选显示名（≤ 48 字）", "url": "https://link3.cc/…"}
    ],
    "tags": ["scheduled_check", "cache_video"],
    "rules": {"scheduledCheck": true, "autoCache": {"enabled": true, "types": ["video"], "since": "2026-11-07T08:00:00Z"}, "fullCache": false, "autoRecord": false}
  }]
}
```

- **追踪意图预设与缓存能力标签（DEC-070、DEC-051 ⑧、[DEC-080](../decisions.md#dec-080)，FR-SUBS-004）**：取消单选的命名共用规则与内置“默认”规则，每个订阅直接持有原子能力标签数组 `tags`，一个标签对应一项缓存能力，按固定顺序与 `SubRules` 一一对应：`scheduled_check`（定时检查 ↔ `rules.scheduledCheck`）、`cache_video`（自动缓存视频 ↔ `rules.autoCache.types` 含 `"video"`）、`cache_draw`（自动缓存图文 ↔ `"draw"`）、`cache_word`（自动缓存文字 ↔ `"word"`）、`cache_forward`（自动缓存转发 ↔ `"forward"`）、`full_cache`（全部缓存 ↔ `rules.fullCache`，DEC-061 ①）、`auto_record`（开播录制 ↔ `rules.autoRecord`）。检查更新、自动缓存、定期全部缓存与开播录制照旧读取同步后的 `rules`，下游契约不变。在 7 个原子标签之上提供纯组合的 4 个意图预设（不升 `subscriptions.json` v5 结构版本）：`full_archive`（完整存档，默认：`scheduled_check` + `cache_video` + `cache_draw` + `cache_word` + `cache_forward` + `full_cache`）、`follow_cache`（追更并保存：`scheduled_check` + `cache_video` + `cache_draw`）、`check_only`（只看新动态：`scheduled_check`）、`manual`（仅手动：`[]`），可叠加 `auto_record`（录直播），其余组合归为 `custom`（自定义）；`HasAutoCacheWithoutScheduledCheck` 用于识别“开启了自动缓存标签但未开启 `scheduled_check`”的配置缺口并在界面给出温和提醒。新建订阅入口（添加订阅、加入订阅新建、拆分为新订阅、从关注列表导入）默认传入 `DefaultSubRules()`（即 `full_archive`），支持创建前切换预设或自定义原子标签；已有已保存的订阅读盘时不套用 `DefaultSubRules()`（空标签仍以 `EmptySubRules()` / `tags: []` 加载），绝不回溯改写既有配置。`SetSubscriptionTags(subId, tags)` 更新单个订阅的标签集合；`SetTagSubscriptions(tagId, subIds)` 按单个标签一次批量指定拥有它的订阅（勾选的加上该标签、取消勾选的移除该标签，每个订阅原有的其他标签保留不变）；`BatchUpdateSubscriptionsTags(subIds, mode, tags)` 跨多个订阅一次批量执行 `add`（添加所选标签）、`remove`（移除所选标签）或 `replace`（统一替换为所选标签，传空即清空所选订阅的全部标签），先校验标签 ID 与订阅 ID 合法，整个批量操作只写一次 `subscriptions.json`，文件或 SQL 失败时完整回滚。`autoCache.since` 基线规则：新建订阅若初始带有任一 `cache_*` 标签（或订阅从没有任何 `cache_*` 标签变为至少有一个 `cache_*` 标签）时，把 `rules.autoCache.since` 设为当前时间；只要仍保留至少一个 `cache_*` 标签（例如在视频与图文标签之间增减或开关非自动缓存标签）就保留原来的 `since`；移除全部 `cache_*` 标签时清空 `since`。
- **迁移（v1～v4 → v5）**：读入低于版本 5 的 `subscriptions.json` 时，迁移前先经 `storage.BackupBeforeMigration` 生成 `subscriptions.json.premigrate-<旧版本>`，再将每个既有订阅的有效能力（优先读取旧版绑定的 `cacheRules[ruleId]`，含曾被编辑过的旧 `"default"` 规则；无绑定规则的早期 v1 文件读取订阅自身的 `rules`、`enabled`、`liveAutoRecord` 与旧 `autoCache` 范围）100% 无损转换为规范化 `tags` 并保留原有的 `autoCache.since`，去掉顶层 `cacheRules` 与订阅的 `ruleId` 后经 `WriteMigratedJSONAtomic` 原子写回 `version: 5`。

- 账号键 `<site>:<uid>`（`uid` 一律字符串），在全部订阅里唯一；B 站 UID 先规范化为正整数的十进制字符串，前导零不能绕过归属约束；抖音 `uid` 是 `sec_uid`，原样保存（区分大小写，不折叠 `__`），拒绝空白与含路径分隔符的值；微博 `uid` 规范化为 5～19 位正十进制字符串并去前导零，头像地址清洗签名与百分号编码查询串（保留公开 `sinaimg.cn` 地址），无昵称时默认 `"微博用户 <uid>"`（绝不落回 `"UID 0"`）；X `uid` 规范化为 1～20 位正十进制字符串（去前导零、不超过 `uint64` 的 `rest_id`），`@handle`（`screen_name`）存入 `uniqueId` 只供显示，无昵称时默认 `"@<screen_name>"` 或 `"X 用户 <uid>"`。抖音账号的 `webRid` 是可失效的缓存：第一次得知时写入（贴直播间链接，或开播时经 `reflow/info` 换到），之后用它查开播，返回的主播对不上或房间不可用时清掉重新查（[直播 §5](live.md#douyin)）；它不参与账号键与归属判断；`uniqueId` 只显示。读文件时发现重复（例如手改过）保留第一个，提示一次。一个订阅最多 20 个账号；移到原订阅是幂等操作。
- 昵称与头像是快照：打开用户页、检查更新、导入时顺便更新，不为此单独发请求。
- 订阅不写进 `library.db`（它只是可以从清单重建的索引）；媒体库按订阅筛选时，服务层把订阅展开成账号列表再查索引的作者字段。
- `site` 接受 `bilibili`、`douyin`、`weibo` 与 `x`（DEC-043、DEC-065、DEC-066）；一个订阅可以同时有四个站点的账号。单个订阅内的多个关联账号统一按固定站点顺序 `bilibili` → `douyin` → `weibo` → `x`（同 `site.All()`）稳定排列，同一站点的多个账号保持原有相对顺序（[DEC-071](../decisions.md#dec-071)）；读入、写入与快照导出/解析均维持这一顺序，已指定且仍关联的 `avatar` 保持不变，新建或失去原头像账号时回退到排序后的第一个关联账号。文件版本 1 → 2、2 → 3、3 → 4 与 4 → 5 的迁移见[数据 §1](data-and-cache.md#schema-v2)、[版本 3 说明](data-and-cache.md#schema-v3)、[版本 4 说明](data-and-cache.md#schema-v4)与[版本 5 说明](data-and-cache.md#schema-v5)（`SubscriptionsSchemaVersion = 5`，`UpdatesDBSchemaVersion = 2` 拆开维护）。
- 增删改、关联/移动先保护受影响记录，文件或 SQL 失败返回错误并恢复旧视图；回滚只恢复受影响行，重载使用 SQL 事务，不先清空全部订阅再逐条写入。
- **更改关联订阅（`Move` / `MoveToNew`）**：支持跨哔哩哔哩、抖音、微博与 X 将已关联账号直接转移到另一个已有订阅（`Move`）或在同一把锁与单次原子写盘中新建订阅并移入（`MoveToNew`，经 `Backend.MoveSubscriptionAccountToNew` 暴露），无需先解除关联再重新添加；转移时保留账号快照字段（`uniqueId`、`webRid`、`name`、`face`、`addedAt`）与 `updates.db` 中的检查起点及记录（不置 `unlinked_at`），并同步更新原订阅的头像引用（失去最后一个账号时清空 `avatar` 与 `face`），零站点请求；任一步失败完整回滚内存、文件与 SQLite 视图。
- **自定义外部链接（`CustomLink`，[DEC-074](../decisions.md#dec-074)，`internal/subscription/links.go`）**：`subscriptions[].links` 为向后兼容的可选字段（`omitempty`），用于保存该订阅对应的聚合页或个人主页（如 `link3.cc`、`linktr.ee` 等，每个订阅最多 20 条）；每条含可选 `label`（`≤ 48` 字；省略时自动提取去 `www.` 的域名与去尾部 `/` 的路径，超过 40 字截断）与必填 `url`（严格限制为带有效主机的 `http://` 或 `https://`，主机转小写并去掉默认端口 `:80`/`:443`，拒绝 `javascript:`、`file:`、控制字符、空白、UNC 路径、账号密码以及 Cookie/签名令牌查询参数；按规范化 `url` 去重）。读入文件或快照时自动剔除不合规旧项而不阻断整个文件加载；`SetLinks` 原子更新并支持失败回滚，`Upsert` 未传 `links` 时保留原有链接，`cloneSubscription`、`updates.db` 的 `raw_json`、订阅快照导出与摘要哈希（`Snapshot.Hash`）、远程同步以及勾选“合并进已有订阅”导入（`MergeExisting`）均完整保留或按规范化 `url` 合并。

<a id="update-check"></a>
## 2. 检查更新（FR-SUB-001）

`updates.db`（SQLite，WAL）包含四张业务表（另保留结构版本 1 的旧表 `scheduler_state` 不再写入）：

- `account_state(site, uid, baseline_at, last_checked_at, last_kind, newest_id, newest_ts, note, unlinked_at)`：每个账号的检查起点与上次结果；
- `account_baseline_pinned(site, uid, post_id)`：首次检查建立起点时已在第 1 页置顶（含微博快转、X `TimelinePinEntry` 置顶）的作品 ID，后续检查不会把这些旧置顶或旧快转误判为新作品；
- `update_item(site, uid, dyn_id, pub_ts, type, snapshot, discovered_at, seen_at, baseline_snapshot, auto_task_id)`：起点之后发现的新动态（抖音账号的 `dyn_id` 是 `aweme_id`，`type` 是 `video` 或 `draw`；微博账号的 `dyn_id` 是 `mid`，`type` 是 `video`/`draw`/`word`/`forward`；X 账号的 `dyn_id` 是外层 `status_id`，`type` 映射为 `forward`（转帖与引用帖）/`video`（唯一媒体为非 GIF 视频）/`draw`（有图片或 GIF，含混排）/`word`（纯文字、投票、卡片与文章摘要））（`seen_at` 列仅保留兼容旧表结构，不再读写，DEC-033）。`snapshot` 是 `PostCard` 显示所需的公开字段（与动态列表 DTO 同源，图片只存去掉尺寸后缀的公开地址、公开 `sinaimg.cn` 地址或清洗后的公开 `pbs.twimg.com` 图片地址），剔除微博临时视频/实况地址、X 视频/私有地址与互动计数，没有签名地址和凭证；
- `site_scheduler_state(site, backoff_until, backoff_level, consecutive_failures, last_stop_reason, last_stop_at, token_timestamps_json)`：按站点分开记录退避与每小时额度（含 `bilibili`、`douyin`、`weibo`、`x` 四行）。

每个账号的检查：

1. 还没有起点（`baseline_at` 为空）：只读动态第 1 页，记下最新的动态作为起点；起点之前的已有动态不写入 `update_item`（INV-12、DEC-033）。
2. 已有起点：从第 1 页往后读，直到某页出现起点动态或发布时间不晚于起点（置顶动态不参与这个判断），每个账号每次最多 3 页；起点之后发现的新动态写入 `update_item`。读满 3 页仍没到起点时，在账号上记“可能还有更早的更新没有拿到”。
3. 更新起点为这次看到的最新动态；每个账号最多保留 200 条记录，超出时删最旧的记录。

**抖音账号**（FR-DY-007，[抖音协议 §6.3、§9.2](../research/douyin.md#posts)）按同样三步，差别：

- 读的是作品列表 `aweme/post`（每页 18 条，`max_cursor` 游标）。没有起点时只读第 1 页，起点取非置顶作品里最新的一条（全是置顶时取全页最新的发布时间）；已有起点时每个账号每次最多 2 页。
- 置顶作品（`is_top`）只按 ID 判断是不是新的，**不参与“到起点就停”的判断**；只有非置顶作品命中起点（ID 相同或发布时间不晚于起点）才停止翻页。跨页按 `aweme_id` 去重；游标原地不动或倒退时停止。
- 空包、只有 `{"status_code":0}` 的空壳、第 1 页空而资料显示有作品等**结果不确定**的情况，本账号记“本轮结果不完整”和原因，不移动起点、不写更新；属于风控或未登录的按下一条处理。
- 第 1 页作者信息里有开播中的 `room_id` 时，顺便记下这个账号的开播状态（不额外请求）。

**微博账号**（FR-WB-007，[微博协议 §6.3、§6.5、§6.6、§13.1](../research/weibo.md#a-review-choices)）按同样三步，差别：

- 读的是微博列表 `mymblog`（第 1 页游标为 `""`，翻页游标为 `<page>:<since_id>`）。没有起点时只读第 1 页，将置顶（`isTop`）与快转（`ori_mid`）的 ID 记入 `account_baseline_pinned`，起点取非置顶且非快转里最新的一条（不写 `update_item`）；已有起点时每个账号每次最多 2 页。
- 置顶与快转只按 ID 判断是否新发现（快转归为 `forward`），**绝不触发“到起点就停”的提前结束**，也不用来推进常规时间起点；只有非置顶非快转的常规微博命中起点才停止翻页。不是博主本人的推广卡片剔除；跨页去重，游标原地不动时停止；到达博主可见范围边界（`BottomTipsVisible`）正常结束；`feedKind` 统一为 `"dynamic"`、规范链接为 `https://weibo.com/...`。
- 空壳或第 1 页空而资料显示有微博等**结果不确定**（`UncertainEmpty`）的情况，本账号记“本轮结果不完整”和原因，不移动起点、不写更新。

**X 账号**（FR-X-007，[X 协议 §7、§15.1](../research/x.md#a-review-choices)）按同样三步，差别：

- 读的是帖子列表 `UserTweets`（每页默认 20 条，`Bottom` 游标）。没有起点时只读第 1 页，将 `TimelinePinEntry` 置顶 ID 记入 `account_baseline_pinned`，起点取非置顶条目的外层 `rest_id`（不写 `update_item`）；已有起点时每个账号每次最多 2 页。
- 置顶帖只按 ID 判断是否新发现，**绝不触发“到起点就停”的提前结束**；水位线按外层 `rest_id`（无符号 64 位整数 `uint64`）单调比较（转帖与引用帖按外层雪花 ID 判断，不因原帖时间较早打乱水位）；推广与推荐卡以及对话模块中非目标作者/回复他人的条目剔除；跨页去重、游标停滞即停；到达时间线可见上限正常结束；类型映射为 `forward`（转帖与引用帖）、`video`（唯一媒体为非 GIF 视频）、`draw`（有图片或 GIF，含混排）、`word`（其余纯文字与降级卡片）；`feedKind` 为 `"dynamic"`、规范链接 `https://x.com/i/web/status/<id>`。
- 假空壳 `{"data":{"user":{}}}` 或第 1 页空而资料显示有帖等**结果不确定**（`UncertainEmpty`）的情况，本账号记“本轮结果不完整”和原因，不移动起点、不写更新。

一轮检查（手动“检查全部”、单个订阅或定时）：

- 同一时间只有一轮；已经在检查时再点检查，返回当前一轮的进度。可以取消，在当前请求结束后停止。
- 账号按站点分组检查；哔哩哔哩账号之间至少隔 2 秒，另加客户端的 500 毫秒全局间隔（NFR-NET-002）；抖音账号之间至少隔 2.5 秒，另加抖音客户端的间隔（NFR-NET-003）；微博账号之间至少隔 1.5 秒（`DefaultWeiboAccountInterval = 1.5s`，[DEC-089](../decisions.md#dec-089)），另加微博客户端的 800～1100 毫秒串行间隔（NFR-NET-004）；X 账号之间至少隔 5 秒（`DefaultXAccountInterval`），另加 X 客户端的 4～6 秒串行间隔（NFR-NET-005）。没登录某个站点时，这个站点的账号记“未登录哔哩哔哩/抖音/微博/X，已跳过”，其他站点照常检查。
- `risk_control`、`rate_limited`、`schema_changed`：立即停止**这个站点**在本轮的剩余账号，记录原因和时间，另外三个站点继续；`auth_required`：按[架构 §4](architecture.md#site-layer)处理并停止这个站点；`network`：这个账号失败，连续 3 个账号网络失败就停止整轮；`unavailable`（账号注销、封禁等）：只记在这个账号上，继续下一个。已拿到的结果都保留。
- 这一轮涉及的哔哩哔哩与抖音账号同时查一次开播状态（[直播 §1](live.md#status)）；微博与 X 账号明确排除（0 次开播请求）。
- 不做“已看/未读”状态标记（DEC-033）；解除关联后这个账号的记录保留（重新关联时接着用起点），不属于任何订阅超过 30 天的账号记录在启动时清理；主页侧栏的订阅列表（窄窗时是时间线上方的横向头像条）只用一个圆点表示该订阅有没点开看过的新缓存内容（按订阅记 `homeSeen`，不按单条），正在直播的排最前，然后未读、已读，各按最新缓存内容从新到旧（DEC-053 ②，替代 DEC-037 的“有更新”）。
- 订阅详情默认展示已缓存“内容”信息流（按关联账号查询 `library.db`，DEC-051 ⑦），不再显示未缓存的“更新”列表（DEC-040），`update_item` 仍是自动缓存的来源；下面的分页接口保留给自动缓存与以后的界面。“更新”列表先在 SQL 中按全部订阅成员过滤，再按发布时间分页（UI 每页 200 条），不能先截全库前 200 条再过滤；检查进度事件只更新 done/total，结束后才刷新结果列表。
- 旧库的首次检查快照用 `update_item.baseline_snapshot` 排除：仅在旧表首次增加该列时，把有效 `discovered_at` 不晚于同账号 `baseline_at` 的记录标为 `1`；其他记录及新写入的增量默认为 `0`。迁移在事务中完成，失败时回滚列与标志；原记录、快照与废弃的 `seen_at` 原样保留，生产代码不读写 `seen_at`。缺少或无法解析时间的旧记录保留可见，不猜测其含义。更新分页、最近更新时间、自动缓存与每账号 200 条更新上限只处理标志为 `0` 的记录。
- 最近更新聚合或订阅查询失败时返回错误；界面保留上次成功读取的订阅，显示原因，不把读失败当成“没有订阅/没有更新”。后台检查的进度从服务层读取，关闭窗口后再打开仍可看到当前进度。

<a id="schedule"></a>
## 3. 定时检查、退避与额度（FR-SUB-002、NFR-NET-002）

- `internal/subscription` 的调度器在 `WhenReady` 里启动一个 goroutine，用 `time.Timer` 按 `checkIntervalMin` 触发（[DEC-080](../decisions.md#dec-080)）：只要至少有一个订阅开启了 `rules.scheduledCheck`（且 `mode != "manual"`）就按间隔运行，不再被旧 `settings.json` 中的 `scheduledCheck: false`（`Subscription.Enabled` 字段保留向后兼容，`settings.json` 保持 v1 不升结构版本）阻断；仅在定时激活状态切换（0↔≥1 个开启定时检查的订阅）或调度参数变更时重算定时，常规订阅编辑、快照更新与手动检查落盘不推迟既有倒计时；四个站点都未登录时不触发，只登录部分站点时只查已登录站点的账号。
- 每轮从参与定时检查（`rules.scheduledCheck`）的订阅里取账号：哔哩哔哩最多 30 个、抖音最多 20 个、微博最多 25 个（`DefaultWeiboRoundAccountCap = 25`，[DEC-089](../decisions.md#dec-089)）、X 最多 15 个（`DefaultXRoundAccountCap = 15`，账号间隔 ≥ 5 秒，每账号最多 2 页；未知站点不再回落到哔哩哔哩），各自按 `last_checked_at` 最早的先查，按第 2 节执行。
- **检查计划**（DEC-059 ①、[DEC-080](../decisions.md#dec-080)、[DEC-081](../decisions.md#dec-081)，订阅页“检查计划”）：调度器记下下一轮的时间；`CheckPlan` 只读本机状态（不发请求），给每个订阅账号算出上次检查的时间与结果（`account_state`）和预计查到它的那一轮：同一站点参与定时检查的账号按上面的顺序排好，第 k 个落在第 ⌊k / 每轮上限⌋ 轮；站点在退避时从退避结束后的第一轮算起；订阅未开启`定时检查`、站点未登录或调度处于手动模式时不给时间、只说明原因。微博与 X 账号的 `AutoRecord` 恒为 `false`（不列开播检查频率），当所属规则开启“开播自动录制”时，微博账号的 `LiveNote` 填 `WeiboLiveUnsupportedMessage`（“微博直播暂不支持录制”）、X 账号的 `LiveNote` 填 `XLiveUnsupportedMessage`（“X 直播与 Spaces 暂不支持录制”），不发请求。额度不足推迟的轮次无法预知，实际会顺延。页面顶部“检查全部”等同订阅页的“检查全部”；每行“检查”只查这一个账号（`CheckAccount`，按手动检查执行、不计入额度，退避期间作为一次用户主动单次探测执行，成功即清退避，失败则保持原退避不翻倍，与其他检查不同时进行，DEC-060 ③、[DEC-089](../decisions.md#dec-089)）。
- **按站点分开**（DEC-043、DEC-065、DEC-066）：退避、额度、熔断的状态存在 `updates.db` 的 `site_scheduler_state`（四个站点各一行，[数据 §1](data-and-cache.md#schema-v2)），下面两条都按站点执行，一个站点被停不影响另外三个站点。
- **退避、主动刹车与手动单次探测恢复（[DEC-089](../decisions.md#dec-089)）**：哔哩哔哩、抖音与 X 一轮因风控或限流停止后，下一轮退避间隔为 `interval × 2^n`（最长 24 小时，成功一轮后 n 归零）；**微博退避阶梯与检查间隔解耦**，固定为 **`15 分钟 → 30 分钟 → 1 小时 → 2 小时 → 6 小时`（上限 6 小时）**，任一次微博自动检查或队列 API 正常成功即归零，且距 `LastStopAt` 满 6 小时无新失败时自动衰减归零。`backoff_until` 写进 `updates.db`，重启后仍然有效。对于 X，当响应头 `x-rate-limit-remaining <= 1` 或收到限流错误（实现 `RateLimitResetAt()`）时，经 `RecordSiteAutomaticStopUntil` 主动刹车至 `reset + 0..60s` 随机抖动（最长 24 小时）；`risk_control`（`64`/`226`/`326`/验证/假空壳）按站点指数退避（最长 24 小时）；`schema_changed` 只停 X 不做指数退避。退避期内后台自动任务（定时检查、开播检查、登录续期、自动下载与该站点缓存队列）继续严格退避（0 自动请求）：缓存队列区分纯小时额度耗尽（`ErrKindQuota` 最老令牌到期后随机等 **`15–60 秒`**）与真正风控/限流退避（微博退避结束后随机等 **`2–5 分钟`**，另三站保持 `10–30 分钟`）。**四站手动单次探测恢复**：当用户在任一站点退避期间主动触发 6 类显式操作——①「我的」、② 链接解析/用户页/详情/翻页、③ 手动检查订阅或单账号（含 X）、④ 手动继续/重试下载（批量重试对处于退避期的站点每站至多放行 1 条探测）、⑤ 缓存队列 `waiting_backoff` 卡片「立即重试」、⑥ 设置 › 订阅与直播退避行「重试恢复」（`ProbeSiteBackoff`）——均经 `site.WithManualProbe(ctx)` 放行 **1 次真实请求（0 自动重试）**：若成功则调用 `ClearSiteBackoff(site)` 立即清除该站点退避并唤醒本站点下载与缓存队列；若仍返回 `risk_control` / `rate_limited`，则调用 `RecordSiteManualProbeFailure` 保持原退避不翻倍 `BackoffLevel`（仅在 X `resetAt` 更晚时顺延）并如实反馈错误。
- **额度**：每个站点一个令牌桶，这个站点的定时检查、自动缓存、登录续期、**缓存队列自动翻页与队列触发的复查/重缓存请求**（`source = "queue"`，统一经 `site.WithAutomaticRequests` 包装）以及 **X 前端元数据发现请求**共用（抖音开播相关的请求不占，按自己的节奏限速，[直播 §5](live.md)，DEC-060 ⑤）——哔哩哔哩每小时 200 次、抖音每小时 120 次、微博每小时 180 次（`DefaultWeiboHourlyQuota = 180`，[DEC-089](../decisions.md#dec-089)）、X 每小时 60 次（`DefaultXHourlyQuota = 60`；未知站点返回 0 不回落）API 请求；定时轮次先保守预检可用额度（X 按每账号最多 2 页预检 `2 × 候选账号数`），所有自动 API（含嵌套签名、空间标识、X 前端静态发现、缓存队列翻页与复查详情/取流）仍在 pacing 等待及取消检查后、真正发出前逐次记账。额度不足则停止、推迟或让缓存队列进入 `waiting_backoff`，保留已完成结果，不超额硬发；手动检查不计入。

<a id="auto-cache"></a>
## 4. 自动缓存（FR-CACHE-007）

- 自动缓存来自订阅的能力标签（`cache_video`、`cache_draw`、`cache_word`、`cache_forward`，第 1 节）。订阅从没有 `cache_*` 标签变成至少有一个 `cache_*` 标签时把 `rules.autoCache.since` 设为当前时间；移除全部 `cache_*` 标签时清空，重新添加时重新计时；其余标签变动不动 `since`。
- 每轮检查（手动或定时）结束后，对开启规则的订阅：取这一轮新写入的、`pub_ts ≥ since`、类型在 `types` 里（`video` = 视频动态、抖音视频作品、微博视频微博与 X 单视频帖（不含 GIF），`draw` = 图文与带图文字、抖音图文作品、微博图文与混排、X 多图/GIF/混排，`word` = 纯文字与微博/X 降级卡片，`forward` = 转发、微博快转与 X 转帖/引用帖）、还没有 `auto_task_id` 的记录，调用与手动缓存相同的 `CacheDynamic`（抖音作品是 `CacheDouyinWork`，微博是 `CacheWeiboPost`，X 是 `CacheXPost`），任务带 `source: "auto"`。所有订阅合计一轮最多建 20 个任务，按发布时间从早到晚；剩下的留到下一轮，在订阅上显示“还有 N 条等待自动缓存”。
- 这一轮因风控、限流或未登录停止时不建任务。已经缓存过的动态（索引里已有，微博核对 `weibo:dynamic:<mid>` 与 `weibo:video:<id>`，X 核对 `x:dynamic:<status_id>`）只记 `auto_task_id` 为已有条目，不重复建任务。

<a id="full-cache"></a>
- **全部缓存与已缓存更新复查（DEC-061 ①、DEC-063 ③、DEC-065、DEC-066、[DEC-076](../decisions.md#dec-076)）**：规则的 `fullCache` 打开时，桌面端后台每 10 分钟看一次哪些账号到期（`updates.db` 的 `account_works.full_cache_at` 为空或早于 7 天前，`FullCacheInterval`；远程版 `ProfileRemote` / `trace-server` 不运行缓存队列与冲突检查，所有 `ConflictService` 接口受 `requireDesktop()` 保护，[远程 §4](remote.md#sync)），按上次全部缓存时间从早到晚，为它建一个 DEC-050 ① 的缓存队列（哔哩哔哩列表 `videos`，抖音 `works`，微博 `posts`，X `posts`，`origin = "full_cache"`，持久化至 `cache-queue.json` v3），并把 `full_cache_at` 记为现在。队列自己从新到旧翻完整个列表：没缓存的、失败的和不完整的（`library.NeedsCompletion`）按慢速、不规律的节奏（40～150 秒随机间隔，每 15～25 条长歇 10～25 分钟）一条一条排入下载队列（标记 `source = "queue"`，内部 API 统一纳入 `site.WithAutomaticRequests` 额度记账）；正在缓存与已暂停的直接跳过；由“保留两版”生成的独立归档快照（`meta.IsArchivedSnapshot()`，即 `archivedOf` 非空）直接跳过；其余**已缓存条目**按四站点额度安全策略顺带复查更新与冲突：
  - **抖音 `works`、微博 `posts`、X `posts` 与哔哩哔哩 `dynamics`（零额外请求列表级比对）**：直接复用翻页已拉取的列表条目在内存中与本机 `item.json` 对比，不额外请求单条详情。其中微博已缓存条目在开启 `weiboRemoveWatermark` 且存在缺无水印伴侣图的静态有效主图（`library.WeiboNeedsNoWMBackfill`）且远端 `post` 含可补伴侣图时直接排入补全（0 额外 `statuses/show` 请求，保留有效主图仅补 `NN_nowm` 并写入 `WeiboCacheStandardVersion = 3`）；仅含 DEC-073 前旧格式内联 URL 且等价正文未变时纯离线清洗补写 `text`/`links`（不动媒体文件，[DEC-079](../decisions.md#dec-079)）。抖音 `ListUserPosts` 同步填充无水印候选 `OriginalURLs` 与脱敏媒体标识，并写入进程内短期计划缓存 `douyinWorkPlanCache`（与 `weiboPostPlanCache`、`xPostPlanCache` 一致：5 分钟 TTL、上限 128 条、退出登录即清），队列为抖音未缓存或可升级作品建任务时直接命中内存计划、零额外作品详情请求。
  - **哔哩哔哩 `videos`（三道门限 + 7 天冷却 + 队列节奏）**：投稿列表不含分 P `cid` 与 DASH 流档位，采用三道门限将自动请求压在每小时 200 次额度内：① **门限 A（零请求列表快筛）**：对比列表返回的标题、简介或时长（`|DurationSec - meta.Duration| > 2`）；若有变化，按 `cache-queue` 的 40～150 秒节奏并经 `site.WithAutomaticRequests` 请求 `VideoDetail` 进一步判定；② **门限 B（缓存标准版本与规格上限跳过）**：门限 A 无变化，且本地条目 `status == "complete"`、`!library.NeedsCompletion(meta)`、`!meta.UpgradeAvailable`、`meta.CacheStandardVersion >= library.CurrentCacheStandardVersion`（当前为 `2`）且已达当前清晰度/音质上限（未降级、非最近 14 天内新投稿）时，0 延迟、0 请求跳过；③ **门限 C（7 天 `recheckedAt` 冷却）**：定期 `fullCache` 中若 `meta.RecheckedAt`（或 `UpgradeCheckedAt`）在 7 天以内直接跳过；到期后才按 `cache-queue` 40～150 秒节奏探测 `VideoDetail` / `PlayURL`。
  - **四路分类处理**：(a) **无变化**：零操作跳过（B 站到期探测无变化时写回 `recheckedAt` 与 `cacheStandardVersion`）；(b) **同源安全升级或补齐**：同一媒体标识（相同 `cid`、`mediaId`、`assetIds`）下有更高流规格（如抖音原非 `original` 现可取原画、微博更高短边档、X 更高码率/分辨率 MP4、B 站更高清晰度或高音质）、新支持附件（实况片段、配乐、完整长文、作者线程续帖）或缺失项时，排入 `source = "queue"` 任务无损升级补齐，单调保留已有弹幕、字幕、独立音轨、有效转发 `Origin` 与完整长文（[数据与缓存 §2.10](data-and-cache.md#conflict-protection)）；(c) **远端失效、受限或降级**（已删除/不可见、试看/付费未购/加密、地域受限、微博失效灰图占位 GIF、仅剩 HLS 分段流或清晰度低于本地）：严禁触碰本地 V1，直接跳过；(d) **V1/V2 资源不一致**（换源改 `cid`、删减重排分 P、作品类型翻转、媒体标识/页数/页类型改变，或编辑计数/时间戳推进且正文实质改写）：不覆盖本地文件，写入 `<DataDir>/cache-conflicts.json`（命中 `ignoredRevisions` 同指纹则静默跳过，后续若有新版本 V3 指纹仍重新提醒）并发出 `conflicts:changed` 事件（[数据与缓存 §1](data-and-cache.md#cache-conflicts)）。
  - 遇到风控、限流或额度用完照常进入 `waiting_backoff` 等待；四个站点（哔哩哔哩、抖音、微博、X）由各自独立的站点循环并行推进（每站至多 1 个活跃队列任务），某站点进入退避、限流、额度耗尽或登录失效绝不阻塞其他站点的队列与下载任务（若某站点派生下载任务尚处于 `queued` 时该站点进入退避，该站点队列自动暂停该派生下载并切入 `waiting_backoff`）。支持经 `MoveCacheQueue(id, top|up|down)` 在同站点未结束任务间手动调整排序并原子落盘至 `cache-queue.json`（保持 v3，重启保留）：将等待任务排到 `waiting`/`waiting_backoff` 之前时原任务保留进度与游标退回 `queued`、新首位继承剩余冷却时间（不绕过站点节奏与退避）；若当前站点有正在下载的 `running` 任务，不中断在飞下载，待当前条完成后自动切至新首位（[DEC-088](../decisions.md#dec-088)）；对处于 `waiting_backoff` 的队列调用 `ResumeCacheQueue(id)`（桌面端卡片「立即重试」或移动端继续）触发单次手动探测并在成功后立即清退避（[DEC-089](../decisions.md#dec-089)）。每个站点同时只有一个没结束的缓存队列时才开新的定期自动任务，所以全部缓存的账号一个接一个做，也不和用户自己点的“全部缓存”同时跑。取消的队列不会马上重开，等下一个 7 天。抖音、微博与 X 开始前读一次账号资料（自动请求，退避或额度用完时跳过）记下作品总数。**微博全部缓存范围与专属节奏（[DEC-089](../decisions.md#dec-089)）**：只补博主的原创微博（跳过转发与快转，默认开启 `FullCacheUseFeatureOriginal = true` 使用 `feature=1` 服务端筛选并可切换）；优先复用内存里的列表计划（`cache-queue.json` 只存 `mid` 与游标，不存签名地址）；微博队列采用专属节奏（复用列表计划的 0-API 条目间隔 **8～20 秒**，需调 JSON API 的条目间隔 **20～45 秒**，每 **30～50** 条长歇 **3～8 分钟**，重启延迟 **15～60 秒**；另三站保持默认 `40–150s / 15–25 条 / 10–25m / 1–5m`）；单账号禁言（`is_muteuser`）或 `UncertainEmpty` 仅停止该单账号队列、绝不触发整站退避；到达博主可见范围边界（`BottomTipsVisible` 或 `BottomTipsText != ""`）时正常结束，并把站点的 `BottomTipsText` 写进队列摘要（`Note`/`StatusText`）。**X 全部缓存范围**：沿 `UserTweets` 只补用户的原创帖子（跳过转帖、引用帖与回复他人），优先复用内存计划（`cache-queue.json` 只存 `status_id` 与游标，不存签名地址）；到达 X 时间线可见上限时正常结束并写明“已到达 X 时间线可见上限”，不调用 `SearchTimeline`。
- **历史缓存一键补全队列（[DEC-079](../decisions.md#dec-079)、[DEC-089](../decisions.md#dec-089)）**：通过 `BackfillSummary()`（0 网络请求，经 `ListItemsMetaOnlyBySite` 离线扫描）统计待补全历史微博条目，并经 `StartWeiboBackfill` 在 `cache-queue.json`（v3）内建立持久化补全队列（`kind = "weibo_backfill"`，`origin = "backfill"`，重启自动续跑），按三级零浪费策略执行：① **A 级（纯离线旧格式链接升级）**：仅需清洗 DEC-073 前内联 URL 的条目零延迟、零网络请求调用 `SaveItemMetadataOnly` 写回规范 `text`/`links` 与 `WeiboCacheStandardVersion = 3`；② **B 级（纯 CDN 2～5 秒快速补齐）**：已落盘 `pic_id`（`assetIds` / `notePages[].mediaId`）的微博图片条目直接走无 Cookie `sinaimg.cn/oslarge/<pic_id>.<ext>` 媒体通道补齐缺失的 `images/NN_nowm.<ext>`（不覆盖主图、与主图同 SHA-256 去重、跳过占位图与 404，评估完即标 `WeiboCacheStandardVersion = 3`），队列项间隔 **2～5 秒随机延迟**，**0 微博 JSON API 请求、不占微博每小时 180 次额度、不触发长歇且不受微博 JSON API 退避阻塞**（若可选 `/oslarge/` CDN 遇 `418/429`，仅跳过本条且不标 v3、不触发微博整站退避）；③ **C 级（缺 `pic_id` 的历史条目，严格遵守微博 API 节奏与 180 次/小时额度）**：先按已订阅微博博主 UID 分组逐页拉取 `mymblog` 批量匹配（命中条目的 `pic_infos` 立刻转 2～5 秒纯 CDN 补齐，遇 `UncertainEmpty`/可见范围限制或翻到底则跳过该博主转兜底、不触发整站退避），对转发（`forwards/`）、收藏与非订阅等未覆盖条目再逐条调 `GetPost`（`statuses/show`）按微博专属节奏与 180 次/小时额度补齐。

<a id="account-works"></a>
- **作品总数与已缓存数（DEC-061 ②、[DEC-076](../decisions.md#dec-076) ⑤）**：`account_works(site, uid, works_total, works_at, full_cache_at)` 记每个账号上次解析时站点给的作品总数（`-1` 表示还不知道）：打开哔哩哔哩用户页（`card` 的 `archive_count`）、读投稿列表第一页（`arc/search` 的总数，包括全部缓存翻的第一页）、打开抖音用户页或全部缓存开始时（资料的 `aweme_count`）、打开微博用户页或全部缓存开始时（`profile/info` 的 `statuses_count`，含转发）、打开 X 用户页或全部缓存开始时（资料的 `statuses_count`）。`SubscriptionAccountWorks()` 只读本机：总数来自这张表，已缓存数由单条 `GROUP BY` 查询的 `library.CountCachedWorks` 从 `library_items` 数（哔哩哔哩 `kind = video` 且投稿人是他或 `via` 是为他缓存的合作视频；抖音 `kind` 为 `video` 或 `dynamic` 且作者或 `via` 是他；微博与 X 统计该作者的非转发条目及 `via.uid` 指向他的全部条目；所有站点均排除 `archivedOf` 非空的独立旧版归档快照，保留两版不会重复计入已缓存作品数）。

<a id="following-import"></a>
## 5. 从关注列表导入（FR-SUBS-003、FR-WB-007、FR-X-007）

- 支持哔哩哔哩、微博与 X（抖音的关注导入待实现，DEC-043、[LOOP-010](https://github.com/chialecode/trace/issues/34)）：入口写“从关注列表导入”，对话框内可在哔哩哔哩、微博与 X 间切换，没登录所选站点时提示先登录。哔哩哔哩读登录账号自己的关注列表与关注分组（[协议资料 §16](../research/bilibili.md#followings)），每页 50 个，既有 `RelationTags` / `RelationFollowings` / `ImportCreators` 保持不变；微博经多站点接口（`FollowingGroups`、`FollowingCreatorsPage`、`ImportFollowingCreators`）读 `friendships/friends`，分组筛选只在 `weibo.FollowGroupsEnabled` 打开后提供；X 经 `FollowingCreatorsPageWithCursor` 按字符串游标滚动加载 `Following` 列表、无分组（`FollowingGroups("x")` 返回空）并保留 `UniqueID`（`@handle`）；翻页均由前端滚动触发，不预取。
- 批量导入（`ImportCreators` / `ImportAccountRefs`）：对每个还不属于任何订阅的账号原子建一个订阅，名称、头像用列表里的昵称和头像，不额外发请求；返回建立和跳过的数量。导入只写本机文件，不调用站点的写接口（INV-11）。
