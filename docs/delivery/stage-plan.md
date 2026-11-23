# 当前阶段计划：M7（X）

- **阶段编号**：M7；计划标识 `TRACE-M7-01`，**修订 1（2026-11-22）**。修订 0 只定了静态调研（[附录](#research)）；修订 1 在调研交付后定稿路线、范围、工作包、必测用例与给 B 的 prompt。
- **角色**：A 规划；B 连续实施与自检；A 集中审查（[开发流程 §2](../dev-rules/workflow.md#agent-handoff)）。给 B 的 prompt 在[第 10 节](#prompt)。
- **分支**：`codex/m7-x`，A 已从 `main @ b75cf80` 创建。阶段起点提交（修订 0、调研与本修订）之上，B 的实施、A 的审查修复和人工反馈的修复全部 amend 进这一个 DCO 提交。
- **上一阶段**：M6（微博）经 PR #14 合并为 `main @ b75cf80`（[M6 计划](https://github.com/chialecode/trace/blob/b75cf80/docs/delivery/stage-plan.md)、[M6 报告](https://github.com/chialecode/trace/blob/b75cf80/docs/delivery/stage-report.md)）。
- **决定与需求**：[DEC-066](../decisions.md#dec-066)；需求 FR-X-001～009、NFR-NET-005、NFR-SEC-005 与 FR-SITE-002、NFR-DATA-004 的 X 部分（[需求 §2.12](../requirements.md#x-scope)），FR-X-010 是后续；验收 AC-77～AC-88（[路线与验收](roadmap.md#acceptance)）；**协议正本 [X 协议与官方 API 资料](../research/x.md)**，A 的定稿与取舍在 [§15](../research/x.md#a-review)（本计划不重复协议）；选型 [2026-11-22-x.md](../research/2026-11-22-x.md)；默认值 [Q-024](../../USER-ACTIONS.md#q-024)；真实站点检查 [ACT-024](../../USER-ACTIONS.md#act-024)。先例：微博（[DEC-065](../decisions.md#dec-065)、[需求 §2.11](../requirements.md#weibo-scope)、[微博协议 §13](../research/weibo.md#a-review)）。

用户 2026-11-22：“下个阶段我会开始做 X 平台的内容，然后把 X 平台的调研交给另一个 Agent”。A 理解的目标：X 与哔哩哔哩、抖音、微博平级——各自登录、解析、浏览、缓存、订阅与检查更新，共用媒体库、任务、主页信息流和缓存规则；X 出问题（未登录、限流、风控、前端变化）不拖累另外三个站点。路线按调研推荐只做网页协议（粘贴 Cookie），直播与 Spaces 先不做（Q-024 第 1、5 条）。X 和前三个站点最大的不同是账号风险与前端变动面，所以本阶段把**保守节奏、遇事即停、风险说明与运行时发现前端元数据**当作和功能同等的交付物。

## 1. 范围与退出条件

| 项目 | 约定 |
| --- | --- |
| 阶段内容 | 阶段开始的整理与依赖盘点（C0、D1）；站点公共层与凭证接入 X（X1）；X 协议客户端与前端元数据（X2）；解析与浏览（X3）；缓存（X4）；订阅、检查更新、全部缓存与关注导入（X5）；我的 › X（X6）；界面与多站点（X7）；远程版（X8）；工具与真实检查（X9）；文档（X10） |
| 不在本阶段 | 官方 API 路线（含 OAuth 2.0 PKCE）、账号密码登录、`x-xp-forwarded-for`、`SearchTimeline` 与按时间回溯、回复标签与 `UserMedia` 媒体页、列表与书签文件夹（开关默认关）、直播与 Spaces 的状态与录制（LOOP-008、LOOP-009 不动）、只有 HLS 的视频、X Articles 正文、评论与社区笔记、编辑历史多版本、任何写操作、同站多账号、新增第三方依赖 |
| 可连续执行 | 本地实现、合成夹具与 `httptest`、测试、截图、打包与本机开发版自动安装、冒烟、文档同步、阶段提交与 amend；按第 6 节做依赖盘点（当前没有开着的 Dependabot PR） |
| 外部操作 | X 真实请求只在 [ACT-024](../../USER-ACTIONS.md#act-024) 的对象与额度内、用户放好专用测试账号的 Cookie、给出对象并回复授权后，经 `tools/actcheck -act act024` 执行 1 次；没到位就记 `not-run`，不阻塞。2026-11-08 的常设授权不覆盖 X。哔哩哔哩、抖音、微博本阶段只有回归，不发真实请求（ACT-023 另行授权）。未授权：push、PR、合并、标签、发布 |
| 必须保留 | M1～M6 与 DEC-059～066 的全部行为与测试；数据结构守卫（第 4.6 节）；按站点的间隔、额度、退避与熔断；凭证隔离与哨兵；INV-01～14；安静测试；只删登记文件；`dist/<通道>/` 只留最新一份（DEC-025） |

**退出条件**

| 条件 | 做法 | 工作包 | 验收 |
| --- | --- | --- | --- |
| X 登录 | 粘贴 Cookie → 白名单清洗 → 本地查 `auth_token`、`ct0` → 两步严格确认（含无 Cookie 与访客夹具判未登录、`twid` 核对）→ 保存；缺字段、未登录、限流或风控分别说明且不保存；登录页风险说明 | X1、X2、X7 | AC-77 |
| 四站点各自登录 | 只登录任一站点即可进应用；未登录站点的在线操作提示且 0 请求；失效与退出只影响本站点 | X1、X7 | AC-78 |
| 解析 X 链接 | 分享文本、`t.co`、镜像、单帖、用户（含 `/i/user/`、`intent`）进对的页面；直播、Spaces、文章、列表、社区、保留路径提示且 0 请求；纯数字与裸 `@` 不抢路由；雪花 ID 向量 | X1、X2、X3 | AC-79 |
| 单帖与用户页 | 长帖、线程、转帖与引用（原帖不可见）、混排、GIF、视频、卡片降级、受限可见；用户页置顶、翻页、真值表各行、受保护与上限提示 | X2、X3、X7 | AC-80、AC-81 |
| 缓存 | 视频单文件 MP4；正文、线程、原图、GIF、混排；转帖与引用存转发者 `forwards/`；只有 HLS 的视频说明不缓存；CDN 不带 Cookie、签名地址不落盘 | X4 | AC-82 |
| 订阅与规则 | 四站点混合订阅；首次只建起点；置顶不打断；外层 ID 单调；推广剔除；不确定不移起点；类型映射；自动缓存 | X5 | AC-83 |
| 按站点的额度与退避 | 每小时 ≤ 60、每轮 ≤ 15、账号间 ≥ 5 秒、每账号 ≤ 2 页；`remaining <= 1` 主动刹车；X 退避不影响另外三个站点 | X1、X2、X5 | AC-84 |
| 全部缓存与关注导入 | X 慢速队列只补原创、到可见上限正常结束；从 X 关注导入 | X5、X7 | AC-85 |
| 我的 › X | 关注、书签、喜欢；文件夹与列表受开关控制 | X6 | AC-86 |
| 标记、直播降级与远程 | “X”小字与五项筛选；X 账号不查开播；远程手动确认后推送 X Cookie、同步 X 订阅 | X7、X8 | AC-87 |
| 前端变化与数据升级 | 前端元数据运行时发现、单飞冷却、失败报 `schema_changed` 只停 X；`subscriptions.json`/`ui-state.json` v4、`cache-queue.json` v3 迁移与旧版拒绝写入 | X2、X4、X5 | AC-88 |
| 整理与依赖 | 第 6 节 | C0、D1 | 报告的整理记录与盘点结果 |

**阶段完成的判定**：B 自检的必测用例（第 7 节）与门禁全部通过，A 集中审查收敛后，才可以请用户授权 push、PR 与合并。ACT-024、Q-024 的答复与人工检查都不是合并的前提；ACT-024 没做时，报告、交付状态与需求状态必须写明 X“只经合成样本验证”，AC-77、79～82、86、88 的真实站点部分为 `not-run`——X 网页协议的变动面比前三站大，未经真实检查的 X 很可能第一次真实使用就要修，这一点要在报告与 USER-ACTIONS 里说清楚。

**阶段完成后，用户可以：** 在登录页或“设置 › 账号”切到“X”，读过风险说明后粘贴（建议是专用账号的）Cookie 登录；把 X 的分享文本、`t.co` 短链、镜像链接或任意帖子、用户链接贴进解析页，打开单条帖子（含长帖全文与作者线程）或用户主页并缓存视频与图片；在订阅里关联 X 账号，和另外三个站点一起检查更新、按同一套规则自动缓存、全部缓存原创帖子；从 X 关注列表导入订阅；在“我的 › X”看自己的关注、书签与喜欢；在主页和媒体库按“全部 / B 站 / 抖音 / 微博 / X”筛选；确认风险后把 X Cookie 与订阅同步给远程版。

## 2. A 已核对的现场（2026-11-22，`codex/m7-x` @ `9d3551e`）

- **Git**：工作区干净；阶段起点提交含修订 0、调研 [x.md](../research/x.md)（第 1～14 节）、[选型表](../research/2026-11-22-x.md)与文档索引入口，本修订 amend 进同一提交。`gh pr list --state open` 为空：没有开着的 Dependabot PR。
- **站点公共层**：`internal/site/site.go` 只认三站点（`All`、`Valid`、`ParseID`、`DisplayName`、`NormalizeUID`）；`site/registry.go` 的 `reBareKnownDomain` 与分派不含 X 域名，`< 19` 位纯数字归哔哩哔哩、19 位报歧义；`subscription.SiteHourlyQuotaLimit` 对未知站点返回 0（M6 已去掉“落回哔哩哔哩”），所以 X 漏掉分支的表现是**永远拿不到额度**；`internal/live/manager.go` 仍有 `case "", site.Bilibili` 这类空值分支。
- **数据结构**：`subscription` 读文件时经 `site.ParseID` 丢掉不认识站点的账号，之后保存会把它抹掉；`uistate` 把不认识的 `FeedFilter.Site` 改回 `all`、`HomePositionSites` 只有四个键。结论与处理见 [x.md §15](../research/x.md#a-review) 第 14 条与第 4.6 节。
- **可复用**：`credential.Manager` 按站点分文件与熵；`download.EnsureSingleMP4Context`、`looksLikeDisguisedTextBody`、`site.GuardedMediaDialer` 与 `MediaTransportSpec`；微博的 `IsForward` / `Origin*` 转发契约、`image_note` 混排计划、`account_baseline_pinned`、`cache-queue.json` 慢速队列、`FollowingService` 的站点参数；`tools/actcheck`（act010～act022，账本与发出前拒绝超额，含 M6 修好的步骤异常保护）。
- **调研抽查**：A 核对了调研引用的代码事实与一页官方价格（按量计费，Posts 读取 `$0.005`），结论与取舍写在 [x.md §15](../research/x.md#a-review)。
- **本机参考**：按索引只读查看多站点订阅与单链接解析功能领域，调研已逐项读过；仓库不写参考项目名。
- **真实站点**：调研与 A 写计划时对 X 0 次请求。`.env.example` 已加 `TRACE_TEST_X_COOKIE_FILE`。
- **依赖**：`go run ./tools/deps` 用本机 `GOPROXY=https://goproxy.cn,direct` 时报 `not-run`（镜像没有 `modernc.org/sqlite v1.63.0`）；改用 `GOPROXY=https://proxy.golang.org,direct` 后结果：`github.com/egoist/mygo` v0.9.0 → v0.9.1 满 7 天；v0.10.0（2026-11-18 发布，2026-11-25 满 7 天）与 `modernc.org/sqlite` v1.63.0（2026-11-22，2026-11-29 满 7 天）未满。

## 3. 依据、查证与选型

- **协议**：[x.md](../research/x.md) 第 1～14 节（静态调研，2026-11-22，0 次真实请求）与 A 的定稿 [§15](../research/x.md#a-review)；V-01～V-09 待 ACT-024 核对，结论回写 §15.2；V-10～V-12 不在本阶段。
- **路线**：只做网页协议（[x.md §3.6](../research/x.md#route-compare) 的方案 A，§15.1 第 1 条）。官方 API 的费用、密钥分发、`Restricted Use Cases` 与 DEC-062 的冲突写在 x.md §3，留作 FR-X-010。
- **开源选型**：[选型表](../research/2026-11-22-x.md)于 2026-11-22 经 GitHub 搜索接口、`pkg.go.dev` 与 Go 模块代理检索：官方没有 Go SDK，社区官方 API 客户端停维或只有机器人提交，网页客户端 0 个可嵌入（归档、停维或拉入 TLS 伪装等重依赖），`x-client-transaction-id` 只有 0 star 的早期移植与两个无许可的服务。A 采纳：**零新增依赖**，HTTP、Cookie、JSON、雪花 ID、事务头用标准库，媒体复用 `mp4ff` v0.63.0 与 `golang.org/x/image` v0.48.0。LOOP-008 随直播延后，不引入 `mediacommon/v2`。
- **实现来源（[DEC-044](../decisions.md#dec-044)）**：B 只照 x.md 的规格独立实现，不翻译参考实现的代码、命名与函数拆分；调研之外的新做法先补进 x.md 再写代码。
- **B 仍需原型验证**：雪花 ID 4 组向量（x.md §4.2）；事务头第 2 阶段的确定性向量（固定时钟、随机字节与合成密钥）；前端元数据解析器对 webpack 与 Vite 两种合成产物的抽取；GIF 的 MP4 在应用内播放器静音循环。线上行为只能由 ACT-024 判断。

## 4. 实现指引

设计以 x.md 第 2～10 节与 §15 为准，下面是 A 对实现的约束；B 可以选更好的做法，但要在报告里写明。

### 4.1 依赖方向（`tools/check` 强制）

`checkDependencyDirection` 的表增加一行并补检查器测试（违例一条被拒、合规一条放行）：`internal/site/x` 不得依赖 `storage`、`library`、`download`、`subscription`、`live`、`service`、`ui`、`internal/site/bilibili`、`internal/site/douyin`、`internal/site/weibo`；现有站点包同样不得依赖 `internal/site/x`；`download`、`live`、`subscription`、`library`、`storage`、`settings`、`uistate` 只经 `internal/site` 的类型与注册接口使用 X；`internal/ui` 只用服务层 DTO。`cmd/trace-server` 仍不含 MyGo 与界面包。

### 4.2 站点公共层与按站点分支（X1）

- `site.X = "x"`，显示名 `X`，`All()` 顺序哔哩哔哩、抖音、微博、X；`NormalizeUID(X)` 接受 1～20 位正十进制、去前导零、不超过 `uint64`；账号键 `x:<rest_id>`。
- `Registry`：X 主域（`x.com`、`twitter.com`、`mobile.twitter.com`、`mobile.x.com`、`m.twitter.com` 与 `www.`）、`t.co`、镜像域（x.md §4.1）进 `reBareKnownDomain` 与分派；裸域名识别要有边界，`box.com/…`、`fix.com/…` 这类不能误判；分享文本取链接时 X 或镜像链接优先、`t.co` 最后；纯数字规则不变，裸 `@handle` 不识别。`XMediaTransport` 按 x.md §6.2 与 §15.1 第 15 条。
- **按站点分支逐个补齐**（§15.1 第 2 条）：B 开工先 `git grep` 列出所有按站点 `switch`、“空值按哔哩哔哩”与“未知返回 0”的地方，逐个加 X 分支或改为对未知站点报错；报告附清单。表驱动测试证明 X 拿到自己的值：每小时额度 60、每轮 15、账号间隔 5 秒、每账号 2 页、显示名、作者目录、凭证文件、熔断与退避的 `x` 行、开播巡检排除。

### 4.3 X 协议包 `internal/site/x`（X2）

文件划分参考选型表 §7（`snowflake.go`、`resolve.go`、`auth.go`、`frontend.go`（前端元数据发现）、`txid.go`、`client.go`、`graphql.go`、`errors.go`、`post.go`、`user.go`、`account.go`、`adapter.go`、`testdata/`），具体拆分由 B 定。要点：

- **客户端**：NFR-NET-005 的串行节奏、超时与响应上限；接口不自动跟随跳转；请求头按 x.md §2.2 的矩阵（不带 `x-xp-forwarded-for`）；响应先判限流头、HTTP 状态、`errors[]`、空体与假空壳，再解析；ID 一律字符串（`UseNumber`，比较按无符号 64 位整数）。`x-rate-limit-*` 按操作名记在内存，`remaining <= 1` 时到 `reset` 前不再请求这个操作。
- **前端元数据**（§15.1 第 4 条）：运行时从首页 HTML、`main.*.js`、`ondemand.s.*.js` 取公开 Bearer、`queryId`、`features`/`fieldToggles` 键与事务头密钥材料，只在内存；单飞；重新发现每 10 分钟最多 1 次；`abs.twimg.com` 只放行 `/responsive-web/` 路径、不带 Cookie、单文件 ≤ 8 MiB；解析失败报 `schema_changed`。仓库里不写这些值的真实字面量，夹具用明显合成值。
- **事务头**：x.md §10.3 第 1 部分；可注入时钟与随机源，合成黄金向量测试；对全部 GraphQL 请求携带。
- **登录确认**按 §15.1 第 6 条；**`ct0`** 按第 7 条；**`t.co`** 按 x.md §4.1（不带 Cookie、≤ 5 跳、每跳 10 秒、`GuardedMediaDialer`、最终主机白名单、提前收敛）。
- **只用的操作**：`UserByScreenName`、`UserByRestId`、`UserTweets`、`TweetDetail`、`Following`、`Bookmarks`、`Likes`（§15.1 第 8 条）。单帖按 x.md §5（`TweetPreviewDisplay` → `Tombstone`/`Unavailable` → `TweetWithVisibilityResults` 的解包顺序、`note_tweet` 对主帖/转帖/引用帖递归、`t.co` 实体还原、线程拼接设防环与深度上限、媒体顺序与选流、卡片降级、`TweetUnavailable.reason` 映射）；用户页按 x.md §7（`userResult.core` 新路径优先、三种时间线路径兼容、`TimelinePinEntry`、推广与对话模块剔除、游标停滞即停、§7.3 真值表）；回复与线程按 §15.1 第 11 条；“我的”按 x.md §8 与 §15.1 第 16 条。错误映射照 x.md §10.1 每一行。
- 全部用 `httptest` 与合成夹具（x.md 开头列的合成值形状），不连真实站点。

### 4.4 凭证（X1）

`credentials/x.bin`，熵 `trace:x:credential:v1`；输入规则同另外三个站点（8 KiB、`Cookie:` 前缀、`;` 或换行、拒绝控制字符、不回显）；白名单 `auth_token`、`ct0`、`twid`、`guest_id`、`kdt`，缺 `auth_token` 或 `ct0` 直接拒绝（0 请求）；确认后保存 `rest_id`、`screen_name`、昵称、头像；`source = manual`，`Renewal()` 不可用（原因“粘贴的 Cookie 不续期”）；只在响应带合法新 `ct0` 时同步这个字段。`RedactString` 覆盖白名单全部字段与 `x-csrf-token`、`Authorization` 头的值（含 URL 编解码形态）；哨兵测试覆盖粘贴、校验、浏览、缓存、订阅检查、我的与远程推送。

### 4.5 服务与界面接口（X3）

- `AuthService.LoginWithCookie("x", text)`、`Logout("x")`、`Statuses()` 四个站点；按站点的“必须登录”（INV-07）对 X 生效。
- `ParseService`：`Resolve` 分派到 X；新增 `XPost(statusId)`、`XUser(restId)`、`XUserPosts(restId, cursor)`（每条带 `cacheKey`）、`@handle` 换 `rest_id`；成功后写最近解析（带 `site`）。`OpenLink` 认 X 目标。
- `CacheService.CacheXPost(statusId, via?)`；`States` 与任务照常。
- 路由 `/x/post/:id`、`/x/user/:uid`；帖子与卡片复用现有 `PostCard` 与详情组件，界面只用 DTO。

### 4.6 下载、缓存与数据（X4）

- 条目键、目录、文件名与规则类型按 §15.1 第 9、10 条；`via` 转发按 DEC-056；“添加已有的缓存文件夹”与重建索引扫描 `x/*/{posts,forwards}/*/item.json`；媒体库“图片”“视频”“转发”与已缓存数（`CountCachedWorks`）对 X 生效。
- 视频：复用单文件 MP4 路径（有视频轨、时长与站点相差 ≤ 2 秒或 3%）；只有 HLS 的按 §15.1 第 15 条记缺失；GIF 存 MP4 并在清单标 `gif`；图片按文件头定扩展名；地址过期（403、404、410）每个文件最多用 `TweetDetail` 重取一次并按已落盘字节续传；伪装成 HTML/JSON 的响应拒绝；CDN 原始地址只在内存（`json:"-"`）。
- **数据结构版本**（§15.1 第 14 条）：`subscriptions.json`、`ui-state.json` 升到 4，`cache-queue.json` 升到 3，迁移前备份（`.premigrate-3`、`.premigrate-2`）、原子写入、可重复执行；`library.db`、`updates.db`、`history.json`、`settings.json` 先核对旧版（`main @ b75cf80`）读到 `site = "x"` 时的实际行为，只在证实会静默丢弃并回写时才升；报告写明每个文件的核对依据。

### 4.7 订阅、检查更新、全部缓存与关注导入（X5）

- `AccountRef` 可以是 `site: "x"`（`rest_id`，名字快照另存 `screen_name`）；`PreviewAccount` 认 X 链接与分享文本。
- 检查按 x.md §7 与 §15.1 第 11、12 条；`update_item.type` 按第 10 条；`snapshot` 只存公开字段（`pbs.twimg.com` 图片地址可存，视频地址不存）。
- 节奏：`DefaultXHourlyQuota = 60`、每轮 15 个、账号间隔 5 秒，共用 `site_scheduler_state` 的 `x` 行；限流按 `reset` 退避，风控（`64`、`226`、`326`、假空壳）按站点指数退避（最长 24 小时）；只停 X；手动检查不计额度但同样受刹车与熔断约束。
- 全部缓存：列表名 `posts`，`origin = full_cache` 同现有；范围按 §15.1 第 12 条；作品总数取 `statuses_count`。
- 直播：X 账号不进开播检查与状态查询；规则开了“开播自动录制”时，订阅的 X 账号写“X 直播与 Spaces 暂不支持录制”，不发请求。
- 关注导入：`FollowingService` 加 X（`Following`，滚动加载，无分组）；抖音仍不支持（LOOP-010）。

### 4.8 界面（X7，`docs/ui.md` 同步）

登录页四个站点分段（X 段有 x.md §12.3 第 1 条的说明，提交按钮上方）；“设置 › 账号”四组（X 组写明不续期的原因与风险说明的简版）；“我的”侧栏第四组“X”（关注、书签、喜欢）；X 用户页（头部同微博用户页结构，“帖子 N”，一个“帖子”列表 + “全部 / 未缓存”，到上限的底部提示，受保护账号说明，“⋯ → 全部缓存”）；单帖详情（FR-X-003 的结构，作者行显示昵称与 `@handle`；投票、卡片、文章、直播与 Spaces 卡片是摘要 + “在浏览器中打开”）；站点小字“X”出现在六处（订阅网格不加）；主页与媒体库筛选“全部 / B 站 / 抖音 / 微博 / X”在 720 宽不溢出；订阅“账号”里的 X 账号没有直播状态与录制按钮；“检查计划”列出 X 账号；关注导入对话框可切到 X；错误文案按 FR-ERR-001，未登录写“请先登录 X”，限流写预计恢复时间，验证与锁定写“X 要求验证或账号被临时锁定，TRACE 已停止 X 的全部自动请求，请先在浏览器里处理”。

### 4.9 远程版（X8）

`PUT credentials` 接受 `x`，用 X2 的登录确认校验后保存；本地对 X **不自动推送**，“设置 › 远程”的 X 一行要先确认机房 IP 风险提示才推送（确认只对这一次推送有效）；订阅快照包含 X 账号；远程状态的站点登录行包含 X；远程按规则检查 X、自动缓存，不做全部缓存；`-server` 包与远程冒烟覆盖 X 的假站点。

### 4.10 工具（X9）

- `tools/actcheck -act act024`：读 `TRACE_TEST_X_COOKIE_FILE` 与 `.local/site-check/targets-x.json`（形状 `{version, act, providedAt, users[{url, expect[]}], posts[{url, expect[]}], shortLinks[], missing{userId, statusId}}`，`expect` 取 `active|pinned|retweet|quote|longText|thread|mixed|gif|video|poll|card|protected|deletedOrigin`）；步骤、额度、停止条件、账本与脱敏回执完全按 [ACT-024](../../USER-ACTIONS.md#act-024)，媒体步骤用 TRACE 的缓存服务在临时数据目录走正式流程；缺对象的步骤记 `not-run`。`httptest` 测试证明：超额在发出前拒绝、每类停止条件立即停止全部请求、回执不含 Cookie、Bearer、`queryId` 以外的前端值、签名地址与真实 ID 或用户名。
- `tools/checkdocs`、`internal/sitecheck` 与冒烟哨兵加 X 模式（§15.1 第 20 条），对 x.md 里的合成占位不误报。
- `tools/uishots` 新增场景：登录页 X 分段（含风险说明）、设置“账号”四组、X 用户页（置顶、到上限提示、受保护、假空壳错误）、单帖详情（长帖、线程、转帖且原帖已删、引用帖、混排含 GIF、视频、投票与卡片）、我的 › X、关注导入切到 X、五项站点筛选、带“X”小字的帖子/卡片/任务行/最近解析、直播与 Spaces 链接提示、远程 X 推送的确认框；三档宽度无横向滚动。

## 5. 工作包

| 工作包 | 预期结果与关键失败路径 | 完成证据 |
| --- | --- | --- |
| C0 整理 | 第 6 节 | 报告整理记录；`go run ./tools/checkdocs` |
| D1 依赖 | `tools/deps` 盘点与升级、`-audit` | 报告的盘点结果 |
| X1 公共层与凭证 | 第 4.1、4.2、4.4 节；按站点分支清单；`x.bin` 与独立熵；四站点登录互不影响 | M7-K01～K03 |
| X2 协议客户端 | 第 4.3 节；雪花 ID；前端元数据与事务头；登录确认；`ct0`；`t.co`；详情、用户、列表、我的的解析；错误分类与刹车 | M7-K04～K10 |
| X3 解析与浏览 | 第 4.5 节；解析分派、最近解析、详情与用户页的服务与界面 | M7-K06～K08、K19 |
| X4 缓存 | 第 4.6 节；视频、图片、GIF、混排、长帖、线程、转帖与引用、过期重取、白名单、HLS-only；数据版本 | M7-K11、K12、K18 |
| X5 订阅与全部缓存 | 第 4.7 节；检查、额度、刹车、类型映射、全部缓存、直播排除、关注导入 | M7-K13～K15 |
| X6 我的 › X | 关注、书签、喜欢；文件夹与列表开关；只读、只在内存 | M7-K16 |
| X7 界面与多站点 | 第 4.8 节；标记、筛选、登录与设置（含风险说明）、订阅账号行、提示文案 | M7-K03、K19 |
| X8 远程版 | 第 4.9 节 | M7-K17 |
| X9 工具与真实检查 | 第 4.10 节；条件到位时运行 ACT-024 并回写结论 | M7-K20；ACT-024 回执 |
| X10 文档 | 架构（站点包、目录、服务方法、凭证、前端元数据）、数据（条目键与目录、清单、版本 4 与 3 的迁移说明）、订阅（X 检查、额度、刹车、全部缓存、关注导入）、直播（X 不录制，同微博 §6 加一节）、远程（X 手动推送）、界面（第 4.8 节）、工程规则（依赖方向表、`act024`、截图场景数）、交付状态（含 LOOP）、需求与路线的状态、x.md §15.2、USER-ACTIONS（ACT-024 结果、H-001 新增 X 的步骤） | `checkdocs`；A 审查 |

连续完成全部范围、自测和修复，不逐包请求继续；需要用户的事写进 USER-ACTIONS，非阻塞的按 [Q-024](../../USER-ACTIONS.md#q-024) 的默认值实现。用户改了 Q-024 的某条时（尤其第 1 条路线），A 回写需求与本计划后再交给 B。

<a id="cleanup"></a>
## 6. 阶段开始的整理（C0）与依赖盘点（D1）

A 已完成（修订 0 与本修订）：

1. M6 的计划与报告换成 M7 的，指向它们的链接改为 `b75cf80` 的固定链接（修订 0）。
2. USER-ACTIONS“推荐你做的”：M7 的 Q-024、ACT-024 放在最前；把已按推荐实现的 Q-023、Q-022 并进“已按推荐实现的默认值”那一行；每一项的细节仍在各自小节。
3. `.env.example` 加 `TRACE_TEST_X_COOKIE_FILE`。
4. 确认没有开着的 Dependabot PR；预跑了 `tools/deps`（第 2 节）。

B 在本阶段处理：

| 对象 | 处理 |
| --- | --- |
| 文档里以前阶段的标签 | `docs/` 中用阶段号或返工轮次描述能力的文字（当前计划与报告、决定表、调研资料里记录来源的日期与阶段除外）改为能力描述；`git grep -n "M[1-6]\b" docs USER-ACTIONS.md` 列出后逐处判断；锚点与编号不改 |
| `artifacts/`、`dist/`、`tmp/` | 确认是忽略的生成物、各类只留最新一份（DEC-025）：`dist/trace/`、`dist/trace-dev/`、`dist/trace-server/` 各留最新；`artifacts/site-check/act022-login.json` 是 weibo.md §13.2 引用的证据，保留；`artifacts/backup-20261116/`、`artifacts/repair-20261115/` 是用户数据的备份，不删 |
| 测试名 | 新测试按能力命名，不带阶段号或问题编号 |
| 本阶段改到的代码注释 | 旧计划与问题编号顺手改成直接描述；不为此改动无关文件 |

**D1**：

1. 没有开着的 Dependabot PR（2026-11-22 `gh pr list` 为空）；阶段中途出现的，按[开发流程 §5.5](../dev-rules/workflow.md#dependency-inventory)处理。
2. `go run ./tools/deps`：本机镜像缺版本时用 `GOPROXY=https://proxy.golang.org,direct` 运行并在报告写明。满 7 天的全部升级（当前 `github.com/egoist/mygo` v0.9.1；B 交付前复跑时 v0.10.0 若已满 7 天（2026-11-25 起）就直接升 v0.10.0，`modernc.org/sqlite` v1.63.0 2026-11-29 起满 7 天），`go mod tidy`、`go run ./tools/check`；MyGo 的不兼容改动按 DEC-032 跟进修改；失败就回退并在报告写明尝试的版本与失败命令。
3. `go run ./tools/deps -audit`：代码调用到的漏洞先升级修复；无法修复的登记 `tools/deps/exceptions.json`（原因与 `reviewBy`）并写进报告。
4. 两个命令跑不起来（退出码 2）记 `not-run`，不写成通过；交付前与 `tools/check` 一起复跑。

## 7. 必测用例

全部用合成样本（`httptest`、夹具、合成媒体），不连真实站点；测试安静（[工程规则](../dev-rules/engineering.md#quiet-tests)）。合成 ID、用户名、Cookie、令牌与地址用 x.md 开头列的明显合成值。

| 编号 | 用例 | 对应 |
| --- | --- | --- |
| M7-K01 | 公共层：`site.X` 的显示名、`All` 顺序、`NormalizeUID`（1～20 位、去前导零、超 `uint64` 与非数字拒绝）、账号键往返；`Registry` 把 X 主域、`t.co`、镜像与分享文本分给 X，X 或镜像链接优先于 `t.co`，`box.com/…` 等不误判，纯数字仍按原规则，裸 `@handle` 不归 X；依赖方向检查器对 X 的每条规则各一违例一合规 | X1、AC-79 |
| M7-K02 | 按站点分支：表驱动测试证明 X 的每小时额度 60、每轮 15、间隔 5 秒、每账号 2 页、退避与熔断用 `x` 行、作者目录、凭证文件都是 X 自己的值；没有任何路径把 X 当成哔哩哔哩或给 0 额度；X 退避或额度用完时另外三个站点照常，反之亦然；下载熔断只暂停 X 排队的任务 | X1、X5、AC-84 |
| M7-K03 | 凭证与按站点登录：`x.bin` 用独立熵、用错熵解不开，另外三个站点的旧凭证照常读取；只登录 X 时进应用、贴另外三个站点的链接提示且 `httptest` 计数为 0，反之同理；X 失效或退出只暂停 X 的任务；`RedactString` 同时脱敏四个站点 | X1、X7、AC-78 |
| M7-K04 | X Cookie 登录：合格的保存并返回 `rest_id`、`screen_name`、昵称、头像；缺 `auth_token` 或 `ct0` 本地拒绝（0 请求）；站点判未登录（不带 Cookie 的 `401`/`32`、只带访客字段、`200` 带 `errors[]`、HTML 登录页、空体）、`screen_name` 不一致、`twid` 与 `rest_id` 不一致、限流与风控（`429`/`88`、`326`、`226`）分别说明且不保存、不改变已有凭证；广告与 `__cf_bm` 等被剥离；超长与控制字符拒绝且不回显；凭证文件、日志、`ui-state.json` 的哨兵扫描无 Cookie 值 | X2、AC-77 |
| M7-K05 | 雪花 ID 与 `ct0`：x.md §4.2 的 4 组向量双向通过，`< 2^22` 的按非雪花处理、超 63 位拒绝，按字符串处理、不经 `float64`；`Set-Cookie: ct0` 合法时同步保存与请求头、非法字符拒绝；`353` 带新 `ct0` 只重放一次、不带或再现判 `auth_required`；同一请求不出现两个 `ct0` | X2 |
| M7-K06 | 链接：AC-79 列出的每种输入进对的目标；`/<handle>` 与 `intent?screen_name=` 各 1 次请求换 `rest_id`；保留路径（`home`、`explore`、`i`、`settings`、`search` 等）不被当成用户名；`t.co` 不带 Cookie、≤ 5 跳、每跳 HTTPS 与白名单，跳到外站、带账号密码或端口、超过 5 跳时停止；镜像只本地改写且 0 次请求；直播、Spaces、文章、列表、社区、搜索提示且请求数为 0 | X2、X3、AC-79 |
| M7-K07 | 单条帖子：`TweetPreviewDisplay`、`Tombstone`/`Unavailable`、`TweetWithVisibilityResults` 的解包顺序；`note_tweet` 覆盖主帖、转帖与引用帖；`t.co` 实体还原与末尾媒体短链剔除；作者自回复线程拼接（含环与深度上限）；转帖与引用（原帖已删、受保护、地区屏蔽）降级且不报格式错误；4 项混排按原顺序；GIF；视频选最高码率 MP4、只有 m3u8 时标“只有分段流”；投票、卡片、文章、直播与 Spaces 卡片降级且没有缓存视频入口；`userResult.core` 与 `legacy` 两种结构；`TweetUnavailable.reason` 各值分类；返回的 `rest_id` 与请求不符判格式变化 | X2、X3、AC-80 |
| M7-K08 | 用户页：资料两种结构；`TimelinePinEntry` 置顶排前标“置顶”；第 2 页按 `Bottom` 游标、两页无重复、游标停滞时停止；推广、推荐与对话模块里非目标作者剔除，回复他人的不列；x.md §7.3 真值表每一行都有夹具且判定正确（含 `{"data":{"user":{}}}` 判限流而不是“没有帖子”、`UserUnavailable`、受保护）；到时间线可见上限正常结束并提示；风控停在原处可重试 | X2、X3、AC-81 |
| M7-K09 | 错误分类与刹车：x.md §10.1 的每一行映射到正确的类别与“停本轮 / 熔断”动作；`risk_control` 与 `rate_limited` 不清除凭证；没有自动重试（`353` 与前端元数据的单次重试除外）；`x-rate-limit-remaining <= 1` 后到 `reset` 前该操作 0 次请求，手动操作得到带恢复时间的说明 | X2、AC-84 |
| M7-K10 | 前端元数据与事务头：webpack 与 Vite 两种合成产物都抽出 Bearer、`queryId`、`features`；单飞（并发 10 个调用只取一次）；空体 404 或 `400 features cannot be null` 触发重新发现，10 分钟内第二次不再取、报 `schema_changed` 并只停 X；`abs.twimg.com` 请求不带 Cookie、非 `/responsive-web/` 路径与超 8 MiB 拒绝；这些请求计入 X 额度；事务头在固定时钟与随机源下等于黄金向量；仓库与日志里扫不到合成 Bearer 哨兵 | X2、AC-88 |
| M7-K11 | 缓存视频：一条视频帖存成 `video.mp4`（`moov` 在后的合成源被重排）；时长差超限或没有视频轨失败；断开续传；地址过期只用 `TweetDetail` 重取一次；CDN 请求不带 Cookie 与 `Referer`，非白名单主机与 HTML/JSON 响应被拒；只有 HLS 的视频记缺失并说明、不发分片请求；`item.json`、`library.db`、日志里扫不到签名地址哨兵 | X4、AC-82 |
| M7-K12 | 缓存图文：多图按顺序、`name=orig`、扩展名按文件头；GIF 存 MP4 且清单标 `gif`；混排的图片、视频与封面；长帖存全文、线程存在同一条目；单个文件缺失记黄点不让整条失败；转帖与引用存转发者 `forwards/<外层 id>/` 并记 `via`；条目键 `x:dynamic:<id>`，已缓存的不搬动；重建索引能找回 X 条目；媒体库“图片”“视频”“转发”筛选正确 | X4、AC-82 |
| M7-K13 | 混合站点订阅：四站点账号的订阅首次检查只建起点；X 新帖按规则类型（视频、图文、文字、转发）自动缓存；置顶旧帖不算新、不让检查提前结束；转帖用外层 ID 判断；推广卡不计；不确定的空结果记“本轮结果不完整”、起点不动；X 风控时本轮 X 账号停止并说明，另外三个站点照常；没登录 X 时跳过 X 账号 | X5、AC-83 |
| M7-K14 | 额度与节奏：X 规则请求超过每小时 60 次推迟到下一轮（前端元数据请求也计入）；每轮 ≤ 15 个账号且间隔 ≥ 5 秒；每个账号每次最多 2 页；接口间隔 4～6 秒；手动检查不计额度；检查计划列出 X 账号与预计时间 | X5、AC-84 |
| M7-K15 | 全部缓存与关注导入：X 用户页与定期全部缓存建立 `posts` 队列，只排原创、跳过转帖、引用与已完整缓存的，部分完成的再补一次；到可见上限正常结束并带提示；`cache-queue.json` 只有 ID 与游标；作品数取 `statuses_count`。关注导入选 X 时列出全部关注、翻页无重复、已在订阅里的不能选、导入不额外请求；哔哩哔哩与微博导入行为不变。开了开播录制的规则对 X 账号写明不支持、开播检查与状态查询对 X 的请求数为 0 | X5、AC-85、AC-87 |
| M7-K16 | 我的 › X：关注、书签、喜欢翻页无重复；点开用户页与帖子详情；“⋯”加入订阅与缓存；风控、限流、未登录、失效、网络、空与到底分别说明；X 熔断时“我的”停下、另外三个站点不受影响；数据目录与日志里没有列表内容（哨兵）；退出 X 后清空；书签文件夹与列表入口在开关关闭时不出现 | X6、AC-86 |
| M7-K17 | 远程版：本地登录或重新登录 X 后不自动推送；未确认风险时推送按钮不发请求；确认后 `PUT credentials` 对 X 用假站点校验通过才保存、失败原凭证不变；订阅快照含 X 账号并保留起点；远程按规则检查 X 与自动缓存、不做全部缓存；远程判 X 失效后停止 X 请求；日志与状态里没有 X Cookie 哨兵 | X8、AC-87 |
| M7-K18 | 数据升级：v3 的 `subscriptions.json`、`ui-state.json` 与 v2 的 `cache-queue.json` 升级后留 `.premigrate-3`、`.premigrate-2`，原有内容与行为不变，可重复执行；含 `site = "x"` 的新文件被版本号守卫挡住旧版写入（用把版本常量改回的测试构造证明）；迁移中途失败时原文件与备份都在；另外四个文件的核对结论有测试或代码引用作证 | X4、X5、AC-88 |
| M7-K19 | 截图与断言（`tools/uishots`）：第 4.10 节列的场景，三档宽度无横向滚动；断言“X”小字出现在规定的六处、订阅网格头像不加、五项筛选在 720 宽不溢出、X 账号行没有录制按钮、登录页 X 分段有风险说明 | X3、X7、AC-80、AC-81、AC-87 |
| M7-K20 | 工具：`actcheck -act act024` 在 `httptest` 上——账本超额在发出前拒绝、每类停止条件立即停止全部请求、回执不含 Cookie/Bearer/签名地址/真实 ID 与用户名、缺对象的步骤记 `not-run`；`checkdocs` 与 `sitecheck` 对合成的 X Cookie、Bearer 与签名地址报错、对 x.md 的占位不误报；冒烟哨兵覆盖 X | X9 |
| M7-K21 | 回归与门禁：`go run ./tools/check`、Linux `go vet`、`tools/uishots`、桌面与远程冒烟、`tools/package`、`tools/package -server` 全部通过；哔哩哔哩、抖音与微博相关测试名集合不减少（报告列出改动前后的数量） | 全部 |

## 8. MVP 与 LOOP

- 本阶段要贯通的 MVP 流程见[路线 §3](roadmap.md#mvp-loop)的“X”一行。
- 可以记为 LOOP 的深化：官方 API 路线、直播与 Spaces、`x-xp-forwarded-for`、只有 HLS 的视频、回复标签与媒体页、书签文件夹与列表（ACT-024 未确认时）、线程后续续帖、`SearchTimeline` 回溯、文章正文、编辑历史、评论与社区笔记。
- 不得记为 LOOP 的：把访客或失效的 Cookie 当成已登录；假空壳显示成“没有帖子”或“没有更多”；X 用到别的站点的额度、间隔或熔断，或因为缺分支拿到 0 额度；遇到 `226`、`326`、`64`、验证或限流后还自动重试；Cookie、Bearer 或签名地址出现在日志、文件、仓库或远程状态里；`t.co`、`abs.twimg.com` 或媒体 CDN 请求带 Cookie；转帖存错文件夹；任何一个链接形态在 0 请求的要求下发了请求；登录页没有风险说明。

## 9. 交付与验证

- B 交付前：`go run ./tools/check`、`go run ./tools/deps` 与 `-audit`、`go run ./tools/uishots`、桌面冒烟与远程冒烟、`go run ./tools/package -server`、`go run ./tools/package -install`（把最新开发版装到本机并保持 TRACE Dev 在托盘运行，[工程规则 §2.2](../dev-rules/engineering.md#dev-install)）。
- ACT-024：用户已于 2026-11-22 授权，Cookie（`.local/site-secrets/x.cookie`）与对象（`.local/site-check/targets-x.json`，1 个用户、5 条帖子）已备好；A 同日做了 1 次登录预检（`GET x.com/home`，已登录，记在 `usage-act024.json` 的 `login` 类别），`tools/actcheck` 必须读入这份账本、沿用已用次数（登录确认剩 ≤ 4 次，合计仍 ≤ 32）。运行 1 次（B 在客户端实现后，或交给 A 在审查时），结论回写 x.md §15.2、阶段报告，并按结论调整默认（XPFF、书签文件夹与列表开关、`video-s.twimg.com` 与 `PublicRedirects`、只有 HLS 的视频比例、登录确认的接口）；没到位就记 `not-run`。
- 报告（`docs/delivery/stage-report.md`，按[交接模板 §2](../templates/agent-handoff.md)）写：每个工作包与用例的实现位置和证据、按站点分支清单、数据结构版本的核对结论、依赖盘点、开发版版本与包的 SHA-256、ACT-024 的状态与回执、`not-run` 项与限制、整理记录。
- USER-ACTIONS：ACT-024 的结果；H-001 加一步 X 的实机体验（登录与风险说明、解析、缓存、订阅、我的、五项筛选、远程推送确认）；Q-024 有变化就更新。

<a id="prompt"></a>
## 10. 给 Agent B 的交接 Prompt

```text
你是 TRACE 的 Agent B，动作：连续实施 M7（X，原 Twitter）。

- 计划：docs/delivery/stage-plan.md（TRACE-M7-01 修订 1）；协议正本 docs/research/x.md，A 的定稿与取舍在 §15（逐条遵守，与前面章节冲突时以 §15 为准）；选型 docs/research/2026-11-22-x.md；决定 DEC-066；需求 FR-X-001～009、NFR-NET-005、NFR-SEC-005、FR-SITE-002、NFR-DATA-004（需求 §2.12）；验收 AC-77～88；默认值 USER-ACTIONS 的 Q-024；真实检查 ACT-024。
- 分支 codex/m7-x，基点 main @ b75cf80；阶段起点提交（修订 0、调研与修订 1）之上，你的全部改动 amend 进这一个 DCO 提交（git commit --amend -s）。开始时核对一次分支、提交与计划；先读 AGENTS.md 与计划第 1～7 节。
- 范围：C0、D1、X1～X10 与必测用例 M7-K01～K21，连续完成、自测、修复，不逐包请示；非阻塞的按 Q-024 默认值实现并写明。开工先做第 4.2 节的“按站点分支”清单（未知站点现在返回 0 额度，漏一处 X 就永远不跑）。
- 要点：只做网页协议；前端元数据（Bearer、queryId、features、事务头密钥）运行时发现、只放内存，仓库不写真实字面量；不实现 x-xp-forwarded-for；只用 UserByScreenName、UserByRestId、UserTweets、TweetDetail、Following、Bookmarks、Likes；X 节奏 4～6 秒、每小时 60、每轮 15、账号间 5 秒、每账号 2 页、remaining<=1 刹车；遇 226/326/64/验证/限流立即停 X、零重试；t.co、abs.twimg.com 与媒体 CDN 一律不带 Cookie；subscriptions.json 与 ui-state.json 升 v4、cache-queue.json 升 v3，其余文件按证据决定。
- D1：没有开着的 Dependabot PR；go run ./tools/deps 与 -audit（本机 goproxy.cn 缺版本时用 GOPROXY=https://proxy.golang.org,direct 并写明），满 7 天的升级（mygo 至少 v0.9.1，交付前复跑时 v0.10.0 满 7 天就升 v0.10.0）。
- 必须保留：M1～M6 与 DEC-059～066 的行为与测试；INV-01～14；按站点的额度、退避与熔断；凭证隔离与哨兵；安静测试、不碰本机安装的应用；只删登记文件；dist 只留最新一份。不新增第三方依赖。参考实现只读、不翻译代码（DEC-044），仓库不写参考项目名。
- 真实站点：ACT-024 已授权（2026-11-22），Cookie 在 .local/site-secrets/x.cookie（.env.local 的 TRACE_TEST_X_COOKIE_FILE），对象在 .local/site-check/targets-x.json；A 已做 1 次登录预检并记入 .local/site-check/usage-act024.json，actcheck 必须读入并沿用这份账本（登录确认剩 ≤ 4 次，合计仍 ≤ 32）。客户端实现后用 go run ./tools/actcheck -act act024 只在 ACT-024 写明的对象与额度内运行 1 次；遇停止条件立即停、不重试、不碰验证码、不换号；结论回写 x.md §15.2 与报告。没到位记 not-run，不阻塞。另外三个站点不发真实请求。
- 交付：go run ./tools/check、tools/deps 与 -audit、uishots、桌面与远程冒烟、tools/package -server、tools/package -install（装好后 TRACE Dev 保持在托盘运行）；按交接模板写 docs/delivery/stage-report.md；更新正本文档（第 5 节 X10）与 USER-ACTIONS（ACT-024 结果、H-001 新增 X 步骤）。
- 未授权：push、创建 PR、合并、标签、发布。
- 完成后给出交付 SHA、报告入口、本机开发版版本与给 A 的审查 prompt。
```

<a id="research"></a>
## 附录：修订 0 的调研（已完成）

<a id="research-delivery"></a>修订 0（2026-11-22）请调研 Agent 做静态协议、官方 API 与选型调研：R-A 登录与凭证、R-B 官方 API 路线、R-C 链接与稳定 ID、R-D 单条帖子、R-E 媒体下载、R-F 用户主页与订阅检查、R-G 我的、R-H 直播与 Spaces、R-I 错误风控与请求节奏、R-J 开源选型、R-K 规则与账号风险、R-L 范围建议；红线是不向 X 的产品、接口与媒体域名发请求（只读公开开发者文档、价格与政策页及官方 GitHub 仓库例外）、不读取任何 Cookie、浏览器登录数据或开发者密钥、示例一律合成、只改文档。交付是 [x.md](../research/x.md)（第 1～14 节，0 次真实请求，V-01～V-12 待验证清单与 Q、FR 草案）、[选型表](../research/2026-11-22-x.md)与文档索引的入口。A 的审阅定稿在 x.md §15。
