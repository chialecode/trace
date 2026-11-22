# 技术设计：订阅、检查更新与自动缓存

<a id="subscriptions"></a>
TRACE 以订阅为逻辑入口（DEC-026）：一个订阅代表一个被追踪的人，关联一个或多个站点账号。本页说明订阅的数据、检查更新、定时调度与退避、自动缓存和关注导入；需求见[需求 §2.4](../requirements.md#subscription-scope)，界面见[界面规范 §3.9](../ui.md#subscriptions)，接口见[架构 §3](architecture.md#services)，开播检查与录制见[直播](live.md)。

<a id="model"></a>
## 1. 模型与文件

`subscriptions.json` 是订阅的正本（[数据 §1](data-and-cache.md#storage)），结构：

```json
{
  "version": 3,
  "subscriptions": [{
    "id": "<128 位随机数的十六进制>",
    "name": "显示名称（≤ 64 字）",
    "note": "备注（≤ 500 字）",
    "avatar": {"site": "bilibili", "uid": "94697"},
    "createdAt": "2026-11-07T08:00:00Z",
    "accounts": [
      {"site": "bilibili", "uid": "94697", "name": "昵称快照", "face": "https://…", "addedAt": "…"},
      {"site": "douyin", "uid": "MS4wLjABAAAA…", "uniqueId": "抖音号快照", "webRid": "可选，最近一次能用的直播间号（缓存，可失效）", "name": "…", "face": "…", "addedAt": "…"},
      {"site": "weibo", "uid": "100000001", "name": "昵称快照", "face": "https://…", "addedAt": "…"}
    ],
    "ruleId": "default",
    "rules": {"scheduledCheck": true, "autoCache": {"enabled": false, "types": ["video", "draw"], "since": null}, "autoRecord": false}
  }],
  "cacheRules": [
    {"id": "default", "name": "默认", "scheduledCheck": false, "autoCache": false, "autoRecord": false},
    {"id": "rule-<十六进制>", "name": "只存视频", "scheduledCheck": true, "autoCache": true, "types": ["video"], "autoRecord": false}
  ]
}
```

- **缓存规则（DEC-040、DEC-051 ⑧，FR-SUBS-004）**：`cacheRules` 是所有订阅共用的规则，订阅用 `ruleId` 选一条。`rules` 是这条规则落到订阅上的结果（加上订阅自己的 `autoCache.since`），检查、自动缓存与开播录制照旧只读 `rules`，所以规则改动不改变下游契约。`SaveRule` 校验名称（必填、≤ 32 字、不重名）与类型（开启自动缓存时至少一种），把规则套到所有 `ruleId` 等于它的订阅上，文件或 SQL 失败时整体回滚；`SetSubscriptionRule` 让单个订阅改用某条规则；`SetSubscriptionsRule(ruleId, subIds)` 为某条规则一次批量指定使用它的订阅——先校验 `ruleId` 与全部 `subIds` 存在（任一不存在返回错误并整体回滚、不写盘），把 `subIds` 中的订阅设为 `ruleId`，当 `ruleId != "default"` 时把原本用 `ruleId` 但这次取消勾选的订阅改回 `"default"`（编辑 `"default"` 本身时勾上的设为 `"default"`，其他用别的规则且未勾选的订阅保持原规则不变），整个批量操作只写一次 `subscriptions.json`，文件或 SQL 失败时完整回滚；`DeleteRule` 不能删 `default`，删除时它的订阅改用 `default`。`default` 是内置规则，所有选项都关（DEC-054）：`SaveRule` 拒绝修改它（`ErrDefaultRuleLocked`）；新订阅都用它（界面新建与用户页订阅传 `DefaultSubRules()`，`ImportCreators` 新建的订阅不开定时检查）。套用规则（单个与批量）时，自动缓存一直开着就保留原来的 `since`，从关到开才从现在计时，从开到关清空。
- **内置规则复位（DEC-054）**：读入时 `default` 与内置设置不同（以前可编辑，初始为定时检查）就复位为全关；原来用它的订阅按下一条的办法归到与自己设置相同的规则（没有就新建；旧默认规则被改过名的，新规则沿用那个名字，否则以摘要命名，如“定时检查”），行为不变，并立即写回 `subscriptions.json`。
- **迁移**：旧文件没有 `cacheRules` 与 `ruleId`。读入时补上 `default`，每个订阅归到与自己 `rules` 完全相同的规则（`default` 或已有的同设置规则），没有就新建一条，以摘要命名（如“定时检查 · 自动缓存视频”，重名加序号）；订阅的行为和 `since` 不变，写回时带上两个字段。

- 账号键 `<site>:<uid>`（`uid` 一律字符串），在全部订阅里唯一；B 站 UID 先规范化为正整数的十进制字符串，前导零不能绕过归属约束；抖音 `uid` 是 `sec_uid`，原样保存（区分大小写，不折叠 `__`），拒绝空白与含路径分隔符的值；微博 `uid` 规范化为 5～19 位正十进制字符串并去前导零，头像地址清洗签名与百分号编码查询串（保留公开 `sinaimg.cn` 地址），无昵称时默认 `"微博用户 <uid>"`（绝不落回 `"UID 0"`）。抖音账号的 `webRid` 是可失效的缓存：第一次得知时写入（贴直播间链接，或开播时经 `reflow/info` 换到），之后用它查开播，返回的主播对不上或房间不可用时清掉重新查（[直播 §5](live.md#douyin)）；它不参与账号键与归属判断；`uniqueId` 只显示。读文件时发现重复（例如手改过）保留第一个，提示一次。一个订阅最多 20 个账号；移到原订阅是幂等操作。
- 昵称与头像是快照：打开用户页、检查更新、导入时顺便更新，不为此单独发请求。
- 订阅不写进 `library.db`（它只是可以从清单重建的索引）；媒体库按订阅筛选时，服务层把订阅展开成账号列表再查索引的作者字段。
- `site` 接受 `bilibili`、`douyin` 与 `weibo`（DEC-043、DEC-065）；一个订阅可以同时有三个站点的账号。文件版本 1 → 2 与 2 → 3 的迁移见[数据 §1](data-and-cache.md#schema-v2)与[版本 3 说明](data-and-cache.md#schema-v3)（`SubscriptionsSchemaVersion = 3`，`UpdatesDBSchemaVersion = 2` 拆开维护）。
- 增删改、关联/移动先保护受影响记录，文件或 SQL 失败返回错误并恢复旧视图；回滚只恢复受影响行，重载使用 SQL 事务，不先清空全部订阅再逐条写入。
- **更改关联订阅（`Move` / `MoveToNew`）**：支持跨哔哩哔哩、抖音与微博将已关联账号直接转移到另一个已有订阅（`Move`）或在同一把锁与单次原子写盘中新建订阅并移入（`MoveToNew`，经 `Backend.MoveSubscriptionAccountToNew` 暴露），无需先解除关联再重新添加；转移时保留账号快照字段（`uniqueId`、`webRid`、`name`、`face`、`addedAt`）与 `updates.db` 中的检查起点及记录（不置 `unlinked_at`），并同步更新原订阅的头像引用（失去最后一个账号时清空 `avatar` 与 `face`），零站点请求；任一步失败完整回滚内存、文件与 SQLite 视图。

<a id="update-check"></a>
## 2. 检查更新（FR-SUB-001）

`updates.db`（SQLite，WAL）包含四张业务表（另保留结构版本 1 的旧表 `scheduler_state` 不再写入）：

- `account_state(site, uid, baseline_at, last_checked_at, last_kind, newest_id, newest_ts, note, unlinked_at)`：每个账号的检查起点与上次结果；
- `account_baseline_pinned(site, uid, post_id)`：首次检查建立起点时已在第 1 页置顶（及微博快转）的作品 ID，后续检查不会把这些旧置顶或旧快转误判为新作品；
- `update_item(site, uid, dyn_id, pub_ts, type, snapshot, discovered_at, seen_at, baseline_snapshot, auto_task_id)`：起点之后发现的新动态（抖音账号的 `dyn_id` 是 `aweme_id`，`type` 是 `video` 或 `draw`；微博账号的 `dyn_id` 是 `mid`，`type` 是 `video`/`draw`/`word`/`forward`）（`seen_at` 列仅保留兼容旧表结构，不再读写，DEC-033）。`snapshot` 是 `PostCard` 显示所需的公开字段（与动态列表 DTO 同源，图片只存去掉尺寸后缀的公开地址或公开 `sinaimg.cn` 地址），剔除微博临时视频/实况地址与互动计数，没有签名地址和凭证；
- `site_scheduler_state(site, backoff_until, backoff_level, consecutive_failures, last_stop_reason, last_stop_at, token_timestamps_json)`：按站点分开记录退避与每小时额度（含 `bilibili`、`douyin`、`weibo` 三行）。

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

一轮检查（手动“检查全部”、单个订阅或定时）：

- 同一时间只有一轮；已经在检查时再点检查，返回当前一轮的进度。可以取消，在当前请求结束后停止。
- 账号按站点分组检查；哔哩哔哩账号之间至少隔 2 秒，另加客户端的 500 毫秒全局间隔（NFR-NET-002）；抖音账号之间至少隔 2.5 秒，另加抖音客户端的间隔（NFR-NET-003）；微博账号之间至少隔 3 秒，另加微博客户端的间隔（NFR-NET-004）。没登录某个站点时，这个站点的账号记“未登录哔哩哔哩/抖音/微博，已跳过”，其他站点照常检查。
- `risk_control`、`rate_limited`：立即停止**这个站点**在本轮的剩余账号，记录原因和时间，另外两个站点继续；`auth_required`：按[架构 §4](architecture.md#site-layer)处理并停止这个站点；`network`：这个账号失败，连续 3 个账号网络失败就停止整轮；`unavailable`（账号注销、封禁等）：只记在这个账号上，继续下一个。已拿到的结果都保留。
- 这一轮涉及的哔哩哔哩与抖音账号同时查一次开播状态（[直播 §1](live.md#status)）；微博账号明确排除（0 次开播请求）。
- 不做“已看/未读”状态标记（DEC-033）；解除关联后这个账号的记录保留（重新关联时接着用起点），不属于任何订阅超过 30 天的账号记录在启动时清理；主页侧栏的订阅列表（窄窗时是时间线上方的横向头像条）只用一个圆点表示该订阅有没点开看过的新缓存内容（按订阅记 `homeSeen`，不按单条），正在直播的排最前，然后未读、已读，各按最新缓存内容从新到旧（DEC-053 ②，替代 DEC-037 的“有更新”）。
- 订阅详情默认展示已缓存“内容”信息流（按关联账号查询 `library.db`，DEC-051 ⑦），不再显示未缓存的“更新”列表（DEC-040），`update_item` 仍是自动缓存的来源；下面的分页接口保留给自动缓存与以后的界面。“更新”列表先在 SQL 中按全部订阅成员过滤，再按发布时间分页（UI 每页 200 条），不能先截全库前 200 条再过滤；检查进度事件只更新 done/total，结束后才刷新结果列表。
- 旧库的首次检查快照用 `update_item.baseline_snapshot` 排除：仅在旧表首次增加该列时，把有效 `discovered_at` 不晚于同账号 `baseline_at` 的记录标为 `1`；其他记录及新写入的增量默认为 `0`。迁移在事务中完成，失败时回滚列与标志；原记录、快照与废弃的 `seen_at` 原样保留，生产代码不读写 `seen_at`。缺少或无法解析时间的旧记录保留可见，不猜测其含义。更新分页、最近更新时间、自动缓存与每账号 200 条更新上限只处理标志为 `0` 的记录。
- 最近更新聚合或订阅查询失败时返回错误；界面保留上次成功读取的订阅，显示原因，不把读失败当成“没有订阅/没有更新”。后台检查的进度从服务层读取，关闭窗口后再打开仍可看到当前进度。

<a id="schedule"></a>
## 3. 定时检查、退避与额度（FR-SUB-002、NFR-NET-002）

- `internal/subscription` 的调度器在 `WhenReady` 里启动一个 goroutine，用 `time.Timer` 按 `checkIntervalMin` 触发；`scheduledCheck` 关闭或三个站点都未登录时不触发，只登录部分站点时只查已登录站点的账号。
- 每轮从参与定时检查（`rules.scheduledCheck`）的订阅里取账号：哔哩哔哩最多 30 个、抖音最多 20 个、微博最多 20 个（`DefaultWeiboRoundAccountCap = 20`；未知站点不再回落到哔哩哔哩），各自按 `last_checked_at` 最早的先查，按第 2 节执行。
- **检查计划**（DEC-059 ①，任务页“检查计划”）：调度器记下下一轮的时间；`CheckPlan` 只读本机状态（不发请求），给每个订阅账号算出上次检查的时间与结果（`account_state`）和预计查到它的那一轮：同一站点参与定时检查的账号按上面的顺序排好，第 k 个落在第 ⌊k / 每轮上限⌋ 轮；站点在退避时从退避结束后的第一轮算起；规则只手动检查、站点未登录、定时检查关闭时不给时间、只说明原因。微博账号的 `AutoRecord` 恒为 `false`（不列开播检查频率），当所属规则开启“开播自动录制”时，该账号的 `LiveNote` 填 `WeiboLiveUnsupportedMessage`（“微博直播暂不支持录制”），不发请求。额度不足推迟的轮次无法预知，实际会顺延。页面顶部“检查全部”等同订阅页的“检查全部”；每行“检查”只查这一个账号（`CheckAccount`，按手动检查执行、不计入额度，与其他检查不同时进行，DEC-060 ③）。
- **按站点分开**（DEC-043、DEC-065）：退避、额度、熔断的状态存在 `updates.db` 的 `site_scheduler_state`（三个站点各一行，[数据 §1](data-and-cache.md#schema-v2)），下面两条都按站点执行，一个站点被停不影响另外两个站点。
- **退避**：一轮因风控或限流停止后，下一轮间隔为 `interval × 2^n`（n 为连续被停的轮数，最长 24 小时），成功一轮后 n 归零；`backoff_until` 写进 `updates.db`，重启后仍然有效。退避期内开播检查、登录续期和该站点的缓存队列（DEC-050 ①）也暂停：缓存队列遇到风控、限流、每小时额度耗尽或站点退避时进入 `waiting_backoff` 状态并不再发任何请求，等站点退避结束后再随机等 10～30 分钟继续；用户手动操作不受退避限制，但界面提示上次被停的时间和原因。
- **额度**：每个站点一个令牌桶，这个站点的定时检查、自动缓存、登录续期与**缓存队列自动翻页**（`site.WithAutomaticRequests`）共用（抖音开播相关的请求不占，按自己的节奏限速，[直播 §5](live.md)，DEC-060 ⑤）——哔哩哔哩每小时 200 次、抖音每小时 120 次、微博每小时 100 次（`DefaultWeiboHourlyQuota = 100`；未知站点返回 0 不回落）API 请求；定时轮次先保守预检可用额度，所有自动 API（含嵌套签名、空间标识、缓存队列翻页与重新取流）仍在 pacing 等待及取消检查后、真正发出前逐次记账。额度不足则停止、推迟或让缓存队列进入 `waiting_backoff`，保留已完成结果，不超额硬发；手动检查不计入。

<a id="auto-cache"></a>
## 4. 自动缓存（FR-CACHE-007）

- 自动缓存来自订阅所用的缓存规则（第 1 节）。规则让订阅从不自动缓存变成自动缓存时把 `rules.autoCache.since` 设为当前时间；关闭时清空，重新开启重新计时；规则的其他改动不动 `since`。
- 每轮检查（手动或定时）结束后，对开启规则的订阅：取这一轮新写入的、`pub_ts ≥ since`、类型在 `types` 里（`video` = 视频动态、抖音视频作品与微博视频微博，`draw` = 图文与带图文字、抖音图文作品、微博图文与混排，`word` = 纯文字与微博降级卡片，`forward` = 转发与微博快转）、还没有 `auto_task_id` 的记录，调用与手动缓存相同的 `CacheDynamic`（抖音作品是 `CacheDouyinWork`，微博是 `CacheWeiboPost`），任务带 `source: "auto"`。所有订阅合计一轮最多建 20 个任务，按发布时间从早到晚；剩下的留到下一轮，在订阅上显示“还有 N 条等待自动缓存”。
- 这一轮因风控、限流或未登录停止时不建任务。已经缓存过的动态（索引里已有，微博核对 `weibo:dynamic:<mid>` 与 `weibo:video:<id>`）只记 `auto_task_id` 为已有条目，不重复建任务。

<a id="full-cache"></a>
- **全部缓存（DEC-061 ①、DEC-065）**：规则的 `fullCache` 打开时，后台每 10 分钟看一次哪些账号到期（`updates.db` 的 `account_works.full_cache_at` 为空或早于 7 天前，`FullCacheInterval`），按上次全部缓存时间从早到晚，为它建一个 DEC-050 ① 的缓存队列（哔哩哔哩列表 `videos`，抖音 `works`，微博 `posts`，`origin = "full_cache"`，持久化至 `cache-queue.json` v2），并把 `full_cache_at` 记为现在。队列自己从新到旧翻完整个列表，完整缓存了的、正在缓存的、已暂停的（在任务页继续）和只是可以升级画质的直接跳过；没缓存的、失败的和缓存了但不完整的（部分完成、有缺失记录、分 P 没有文件，DEC-063 ③，`library.NeedsCompletion`）按慢速、不规律的节奏一条一条缓存，不完整的再缓存一次时保留已有的有效文件、只补缺的部分；遇到风控、限流或额度用完照常等待。每个站点同时只有一个没结束的缓存队列时才开新的，所以全部缓存的账号一个接一个做，也不和用户自己点的“全部缓存”同时跑。取消的队列不会马上重开，等下一个 7 天。抖音与微博开始前读一次账号资料（自动请求，退避或额度用完时跳过）记下作品总数。**微博全部缓存范围**：只补博主的原创微博（跳过转发与快转，默认 `feature=0` 在本机过滤，支持经 `FullCacheUseFeatureOriginal` 切换）；优先复用内存里的列表计划（`cache-queue.json` 只存 `mid` 与游标，不存签名地址）；到达博主可见范围边界（`BottomTipsVisible`）时正常结束，并把站点的 `BottomTipsText` 写进队列摘要（`Note`/`StatusText`）。

<a id="account-works"></a>
- **作品总数与已缓存数（DEC-061 ②）**：`account_works(site, uid, works_total, works_at, full_cache_at)` 记每个账号上次解析时站点给的作品总数（`-1` 表示还不知道）：打开哔哩哔哩用户页（`card` 的 `archive_count`）、读投稿列表第一页（`arc/search` 的总数，包括全部缓存翻的第一页）、打开抖音用户页或全部缓存开始时（资料的 `aweme_count`）、打开微博用户页或全部缓存开始时（`profile/info` 的 `statuses_count`，含转发）。`SubscriptionAccountWorks()` 只读本机：总数来自这张表，已缓存数由 `library.CountCachedWorks` 从 `library_items` 数（哔哩哔哩 `kind = video` 且投稿人是他或 `via` 是为他缓存的合作视频；抖音 `kind` 为 `video` 或 `dynamic` 且作者或 `via` 是他；微博统计该作者的非转发条目及 `via.uid` 指向他的全部微博条目）。

<a id="following-import"></a>
## 5. 从关注列表导入（FR-SUBS-003、FR-WB-007）

- 支持哔哩哔哩与微博（抖音的关注导入待实现，DEC-043、LOOP-010）：入口写“从关注列表导入”，对话框内可在哔哩哔哩与微博间切换，没登录所选站点时提示先登录。哔哩哔哩读登录账号自己的关注列表与关注分组（[协议资料 §16](../research/bilibili.md#followings)），每页 50 个，既有 `RelationTags` / `RelationFollowings` / `ImportCreators` 保持不变；微博经多站点接口（`FollowingGroups`、`FollowingCreatorsPage`、`ImportFollowingCreators`）读 `friendships/friends`，分组筛选只在 `weibo.FollowGroupsEnabled` 打开后提供；翻页均由前端滚动触发，不预取。
- 批量导入（`ImportCreators` / `ImportAccountRefs`）：对每个还不属于任何订阅的账号原子建一个订阅，名称、头像用列表里的昵称和头像，不额外发请求；返回建立和跳过的数量。导入只写本机文件，不调用站点的写接口（INV-11）。
