# 技术设计：订阅、检查更新与自动缓存

<a id="subscriptions"></a>
TRACE 以订阅为逻辑入口（DEC-026）：一个订阅代表一个被追踪的人，关联一个或多个站点账号。本页说明订阅的数据、检查更新、定时调度与退避、自动缓存和关注导入；需求见[需求 §2.4](../requirements.md#subscription-scope)，界面见[界面规范 §3.9](../ui.md#subscriptions)，接口见[架构 §3](architecture.md#services)，开播检查与录制见[直播](live.md)。

<a id="model"></a>
## 1. 模型与文件

`subscriptions.json` 是订阅的正本（[数据 §1](data-and-cache.md#storage)），结构：

```json
{
  "version": 2,
  "subscriptions": [{
    "id": "<128 位随机数的十六进制>",
    "name": "显示名称（≤ 64 字）",
    "note": "备注（≤ 500 字）",
    "avatar": {"site": "bilibili", "uid": "94697"},
    "createdAt": "2026-11-07T08:00:00Z",
    "accounts": [
      {"site": "bilibili", "uid": "94697", "name": "昵称快照", "face": "https://…", "addedAt": "…"},
      {"site": "douyin", "uid": "MS4wLjABAAAA…", "uniqueId": "抖音号快照", "webRid": "可选，最近一次能用的直播间号（缓存，可失效）", "name": "…", "face": "…", "addedAt": "…"}
    ],
    "ruleId": "default",
    "rules": {"scheduledCheck": true, "autoCache": {"enabled": false, "types": ["video", "draw"], "since": null}, "autoRecord": false}
  }],
  "cacheRules": [
    {"id": "default", "name": "默认", "scheduledCheck": true, "autoCache": false, "autoRecord": false},
    {"id": "rule-<十六进制>", "name": "只存视频", "scheduledCheck": true, "autoCache": true, "types": ["video"], "autoRecord": false}
  ]
}
```

- **缓存规则（DEC-040，FR-SUBS-004）**：`cacheRules` 是所有订阅共用的规则，订阅用 `ruleId` 选一条。`rules` 是这条规则落到订阅上的结果（加上订阅自己的 `autoCache.since`），检查、自动缓存与开播录制照旧只读 `rules`，所以规则改动不改变下游契约。`SaveRule` 校验名称（必填、≤ 32 字、不重名）与类型（开启自动缓存时至少一种），把规则套到所有 `ruleId` 等于它的订阅上，文件或 SQL 失败时整体回滚；`SetSubscriptionRule` 让订阅改用某条规则；`DeleteRule` 不能删 `default`，删除时它的订阅改用 `default`。套用规则时，自动缓存一直开着就保留原来的 `since`，从关到开才从现在计时。
- **迁移**：旧文件没有 `cacheRules` 与 `ruleId`。读入时补上 `default`，每个订阅归到与自己 `rules` 完全相同的规则（`default` 或已有的同设置规则），没有就新建一条，以摘要命名（如“定时检查 · 自动缓存视频”，重名加序号）；订阅的行为和 `since` 不变，写回时带上两个字段。

- 账号键 `<site>:<uid>`（`uid` 一律字符串），在全部订阅里唯一；B 站 UID 先规范化为正整数的十进制字符串，前导零不能绕过归属约束；抖音 `uid` 是 `sec_uid`，原样保存（区分大小写，不折叠 `__`），拒绝空白与含路径分隔符的值。抖音账号的 `webRid` 是可失效的缓存：第一次得知时写入（贴直播间链接，或开播时经 `reflow/info` 换到），之后用它查开播，返回的主播对不上或房间不可用时清掉重新查（[直播 §5](live.md#douyin)）；它不参与账号键与归属判断；`uniqueId` 只显示。读文件时发现重复（例如手改过）保留第一个，提示一次。一个订阅最多 20 个账号；移到原订阅是幂等操作。
- 昵称与头像是快照：打开用户页、检查更新、导入时顺便更新，不为此单独发请求。
- 订阅不写进 `library.db`（它只是可以从清单重建的索引）；媒体库按订阅筛选时，服务层把订阅展开成账号列表再查索引的作者字段。
- `site` 接受 `bilibili` 与 `douyin`（DEC-043）；一个订阅可以同时有两个站点的账号。文件版本 1 → 2 的迁移见[数据 §1](data-and-cache.md#schema-v2)。
- 增删改、关联/移动先保护受影响记录，文件或 SQL 失败返回错误并恢复旧视图；回滚只恢复受影响行，重载使用 SQL 事务，不先清空全部订阅再逐条写入。

<a id="update-check"></a>
## 2. 检查更新（FR-SUB-001）

`updates.db`（SQLite，WAL）包含四张业务表（另保留结构版本 1 的旧表 `scheduler_state` 不再写入）：

- `account_state(site, uid, baseline_at, last_checked_at, last_kind, newest_id, newest_ts, note, unlinked_at)`：每个账号的检查起点与上次结果；
- `account_baseline_pinned(site, uid, post_id)`：首次检查建立起点时已在第 1 页置顶的作品 ID，后续检查不会把这些旧置顶误判为新作品；
- `update_item(site, uid, dyn_id, pub_ts, type, snapshot, discovered_at, seen_at, baseline_snapshot, auto_task_id)`：起点之后发现的新动态（抖音账号的 `dyn_id` 是 `aweme_id`，`type` 是 `video` 或 `draw`）（`seen_at` 列仅保留兼容旧表结构，不再读写，DEC-033）。`snapshot` 是 `PostCard` 显示所需的公开字段（与动态列表 DTO 同源，图片只存去掉尺寸后缀的公开地址），没有签名地址和凭证；
- `site_scheduler_state(site, backoff_until, backoff_level, consecutive_failures, last_stop_reason, last_stop_at, token_timestamps_json)`：按站点分开记录退避与每小时额度。

每个账号的检查：

1. 还没有起点（`baseline_at` 为空）：只读动态第 1 页，记下最新的动态作为起点；起点之前的已有动态不写入 `update_item`（INV-12、DEC-033）。
2. 已有起点：从第 1 页往后读，直到某页出现起点动态或发布时间不晚于起点（置顶动态不参与这个判断），每个账号每次最多 3 页；起点之后发现的新动态写入 `update_item`。读满 3 页仍没到起点时，在账号上记“可能还有更早的更新没有拿到”。
3. 更新起点为这次看到的最新动态；每个账号最多保留 200 条记录，超出时删最旧的记录。

**抖音账号**（FR-DY-007，[抖音协议 §6.3、§9.2](../research/douyin.md#posts)）按同样三步，差别：

- 读的是作品列表 `aweme/post`（每页 18 条，`max_cursor` 游标）。没有起点时只读第 1 页，起点取非置顶作品里最新的一条（全是置顶时取全页最新的发布时间）；已有起点时每个账号每次最多 2 页。
- 置顶作品（`is_top`）只按 ID 判断是不是新的，**不参与“到起点就停”的判断**；只有非置顶作品命中起点（ID 相同或发布时间不晚于起点）才停止翻页。跨页按 `aweme_id` 去重；游标原地不动或倒退时停止。
- 空包、只有 `{"status_code":0}` 的空壳、第 1 页空而资料显示有作品等**结果不确定**的情况，本账号记“本轮结果不完整”和原因，不移动起点、不写更新；属于风控或未登录的按下一条处理。
- 第 1 页作者信息里有开播中的 `room_id` 时，顺便记下这个账号的开播状态（不额外请求）。

一轮检查（手动“检查全部”、单个订阅或定时）：

- 同一时间只有一轮；已经在检查时再点检查，返回当前一轮的进度。可以取消，在当前请求结束后停止。
- 账号按站点分组检查；哔哩哔哩账号之间至少隔 2 秒，另加客户端的 500 毫秒全局间隔（NFR-NET-002）；抖音账号之间至少隔 2.5 秒，另加抖音客户端的间隔（NFR-NET-003）。没登录某个站点时，这个站点的账号记“未登录抖音/哔哩哔哩，已跳过”，其他站点照常检查。
- `risk_control`、`rate_limited`：立即停止**这个站点**在本轮的剩余账号，记录原因和时间，另一个站点继续；`auth_required`：按[架构 §4](architecture.md#site-layer)处理并停止这个站点；`network`：这个账号失败，连续 3 个账号网络失败就停止整轮；`unavailable`（账号注销、封禁等）：只记在这个账号上，继续下一个。已拿到的结果都保留。
- 这一轮涉及的账号同时查一次开播状态（[直播 §1](live.md#status)）。
- 不做“已看/未读”状态标记（DEC-033）；解除关联后这个账号的记录保留（重新关联时接着用起点），不属于任何订阅超过 30 天的账号记录在启动时清理；主页的“有更新”（上下文侧栏；窄窗时是时间线上方的头像行）按订阅/作者在最后查看时间戳之后的新缓存内容从近到远排列（DEC-037、DEC-039）。
- 订阅详情不再显示“更新”列表（DEC-040），`update_item` 仍是自动缓存的来源；下面的分页接口保留给自动缓存与以后的界面。“更新”列表先在 SQL 中按全部订阅成员过滤，再按发布时间分页（UI 每页 200 条），不能先截全库前 200 条再过滤；检查进度事件只更新 done/total，结束后才刷新结果列表。
- 旧库的首次检查快照用 `update_item.baseline_snapshot` 排除：仅在旧表首次增加该列时，把有效 `discovered_at` 不晚于同账号 `baseline_at` 的记录标为 `1`；其他记录及新写入的增量默认为 `0`。迁移在事务中完成，失败时回滚列与标志；原记录、快照与废弃的 `seen_at` 原样保留，生产代码不读写 `seen_at`。缺少或无法解析时间的旧记录保留可见，不猜测其含义。更新分页、最近更新时间、自动缓存与每账号 200 条更新上限只处理标志为 `0` 的记录。
- 最近更新聚合或订阅查询失败时返回错误；界面保留上次成功读取的订阅，显示原因，不把读失败当成“没有订阅/没有更新”。后台检查的进度从服务层读取，关闭窗口后再打开仍可看到当前进度。

<a id="schedule"></a>
## 3. 定时检查、退避与额度（FR-SUB-002、NFR-NET-002）

- `internal/subscription` 的调度器在 `WhenReady` 里启动一个 goroutine，用 `time.Timer` 按 `checkIntervalMin` 触发；`scheduledCheck` 关闭或两个站点都未登录时不触发，只登录一个站点时只查这个站点的账号。
- 每轮从参与定时检查（`rules.scheduledCheck`）的订阅里取账号：哔哩哔哩最多 30 个、抖音最多 20 个，各自按 `last_checked_at` 最早的先查，按第 2 节执行。
- **按站点分开**（DEC-043）：退避、额度、熔断的状态存在 `updates.db` 的 `site_scheduler_state`（每个站点一行，[数据 §1](data-and-cache.md#schema-v2)），下面两条都按站点执行，一个站点被停不影响另一个站点。
- **退避**：一轮因风控或限流停止后，下一轮间隔为 `interval × 2^n`（n 为连续被停的轮数，最长 24 小时），成功一轮后 n 归零；`backoff_until` 写进 `updates.db`，重启后仍然有效。退避期内开播检查和续期也暂停；用户手动操作不受退避限制，但界面提示上次被停的时间和原因。
- **额度**：每个站点一个令牌桶，这个站点的定时检查、开播检查、自动缓存与续期共用——哔哩哔哩每小时 200 次、抖音每小时 120 次 API 请求；定时轮次先保守预检可用额度，所有自动 API（含嵌套签名、空间标识与重新取流）仍在 pacing 等待及取消检查后、真正发出前逐次记账。额度不足则停止或推迟并保留已完成结果，不超额硬发；手动检查不计入。

<a id="auto-cache"></a>
## 4. 自动缓存（FR-CACHE-007）

- 自动缓存来自订阅所用的缓存规则（第 1 节）。规则让订阅从不自动缓存变成自动缓存时把 `rules.autoCache.since` 设为当前时间；关闭时清空，重新开启重新计时；规则的其他改动不动 `since`。
- 每轮检查（手动或定时）结束后，对开启规则的订阅：取这一轮新写入的、`pub_ts ≥ since`、类型在 `types` 里（`video` = 视频动态与抖音视频作品，`draw` = 图文与带图文字、抖音图文作品，`word` = 纯文字，`forward` = 转发）、还没有 `auto_task_id` 的记录，调用与手动缓存相同的 `CacheDynamic`（抖音作品是 `CacheDouyinWork`），任务带 `source: "auto"`。所有订阅合计一轮最多建 20 个任务，按发布时间从早到晚；剩下的留到下一轮，在订阅上显示“还有 N 条等待自动缓存”。
- 这一轮因风控、限流或未登录停止时不建任务。已经缓存过的动态（索引里已有）只记 `auto_task_id` 为已有条目，不重复建任务。

<a id="following-import"></a>
## 5. 从关注列表导入（FR-SUBS-003）

- 只针对哔哩哔哩（抖音的关注导入待实现，DEC-043、LOOP-010）：入口写“从 B 站关注列表导入”，没登录哔哩哔哩时提示先登录。只读登录账号自己的关注列表与关注分组（[协议资料 §16](../research/bilibili.md#followings)），每页 50 个，翻页由前端滚动触发，不预取。
- `Import(uids)`：对每个还不属于任何订阅的账号建一个订阅，名称、头像用列表里的昵称和头像，不额外请求；返回建立和跳过的数量。导入只写本机文件，不调用站点的写接口（INV-11）。
