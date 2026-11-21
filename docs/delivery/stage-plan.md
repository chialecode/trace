# 当前阶段计划：M6（微博）

- **阶段编号**：M6；计划标识 `TRACE-M6-01`，**修订 1（2026-11-21）**。修订 0 只定了静态调研（[附录](#research)）；修订 1 在调研交付后定稿范围、工作包、必测用例与给 B 的 prompt。
- **角色**：A 规划；B 连续实施与自检；A 集中审查（[开发流程 §2](../dev-rules/workflow.md#agent-handoff)）。给 B 的 prompt 在[第 10 节](#prompt)。
- **分支**：`codex/m6-weibo`，A 已从 `main @ 4b37fc5` 创建。阶段起点提交（修订 0、调研与本修订）之上，B 的实施、A 的审查修复和人工反馈的修复全部 amend 进这一个 DCO 提交。
- **上一阶段**：M5（远程版、上传与导入导出）经 PR #11 合并为 `main @ 4b37fc5`（[M5 计划](https://github.com/chialecode/trace/blob/4b37fc5/docs/delivery/stage-plan.md)、[M5 报告](https://github.com/chialecode/trace/blob/4b37fc5/docs/delivery/stage-report.md)）。
- **决定与需求**：[DEC-065](../decisions.md#dec-065)；需求 FR-WB-001～009、NFR-NET-004、NFR-SEC-004 与 FR-SITE-002 的微博部分（[需求 §2.11](../requirements.md#weibo-scope)），FR-WB-010 是后续；验收 AC-65～AC-76（[路线与验收](roadmap.md#acceptance)）；**协议正本 [微博协议资料](../research/weibo.md)**，A 的定稿与取舍在 [§13](../research/weibo.md#a-review)（本计划不重复协议）；默认值 [Q-023](../../USER-ACTIONS.md#q-023)；真实站点检查 [ACT-022](../../USER-ACTIONS.md#act-022)。抖音是第二个站点的先例：[DEC-043](../decisions.md#dec-043)、[需求 §2.8](../requirements.md#douyin-scope)、[M3 计划](https://github.com/chialecode/trace/blob/f080483/docs/delivery/stage-plan.md)。

用户 2026-11-21：“下一个阶段我想做微博，然后我会把微博的内容交给另一个 Agent 调研”。A 理解的目标：微博与哔哩哔哩、抖音平级——各自登录、解析、浏览、缓存、订阅与检查更新，共用媒体库、任务、主页信息流和缓存规则；微博出问题（未登录、风控、接口变化）不拖累另外两个站点。直播与扫码登录按调研建议先不做（Q-023 第 1、2 条）。

## 1. 范围与退出条件

| 项目 | 约定 |
| --- | --- |
| 阶段内容 | 阶段开始的整理与依赖盘点（C0、D1）；站点公共层与凭证接入微博（W1）；微博协议客户端（W2）；解析与浏览（W3）；缓存（W4）；订阅、检查更新、全部缓存与关注导入（W5）；我的 › 微博（W6）；界面与多站点（W7）；远程版（W8）；工具与真实检查（W9）；文档（W10） |
| 不在本阶段 | 微博扫码登录、直播状态与录制（含 HLS-TS，LOOP-008 不动）、评论、原创筛选、按时间段检索、头条文章正文、视频合集、超话与搜索、去水印开关（V-07 未证实时）、画质升级提醒、任何写操作（点赞、关注、收藏）、同站多账号、新增第三方依赖 |
| 可连续执行 | 本地实现、合成夹具与 `httptest`、测试、截图、打包与本机开发版自动安装、冒烟、文档同步、阶段提交与 amend；按[开发流程 §5.5](../dev-rules/workflow.md#dependency-inventory)处理 Dependabot PR（关闭内容已在 `main` 的 PR #9，见第 6 节） |
| 外部操作 | 微博真实请求只在 [ACT-022](../../USER-ACTIONS.md#act-022) 的对象与额度内、用户放好 Cookie、给出对象并回复授权后，经 `tools/actcheck -act act022` 执行 1 次；没到位就记 `not-run`，不阻塞。哔哩哔哩与抖音本阶段只有回归，不发真实请求。未授权：push、PR、合并、标签、发布 |
| 必须保留 | M1～M5 与 DEC-059～064 的全部行为与测试（哔哩哔哩与抖音的登录、解析、缓存、订阅、录制、我的、远程版）；数据结构版本 2 与守卫（第 4.6 节）；按站点的间隔、额度、退避与熔断；凭证隔离与哨兵；INV-01～14；安静测试；只删登记文件；`dist/<通道>/` 只留最新一份（DEC-025） |

**退出条件**

| 条件 | 做法 | 工作包 | 验收 |
| --- | --- | --- | --- |
| 微博登录 | 粘贴 Cookie → 白名单清洗 → 本地查 `SUB` → 严格确认已登录 → 保存；三类失败分别说明且不保存；访客 Cookie 判未登录 | W1、W2、W7 | AC-65 |
| 三站点各自登录 | 只登录任一站点即可进应用；未登录站点的在线操作提示且 0 请求；失效与退出只影响本站点 | W1、W7 | AC-66 |
| 解析微博链接 | 分享文本、`t.cn`、单条微博、视频页、用户页进对的页面；不支持的提示且 0 请求；`mid`/`mblogid` 向量 | W1、W2、W3 | AC-67 |
| 单条微博与用户页 | 长文、转发与原微博不可见、超过 9 图、GIF、实况、混排、视频、卡片降级；用户页置顶、翻页、可见范围提示、空壳与真空区分 | W2、W3、W7 | AC-68、AC-69 |
| 缓存 | 视频单文件 MP4；正文、原图、GIF、实况片段、混排；转发存转发者 `forwards/`；CDN 不带 Cookie、签名地址不落盘 | W4 | AC-70 |
| 订阅与规则 | 三站点混合订阅；首次只建起点；置顶与快转不打断；推广卡片剔除；不确定不移起点；类型映射；自动缓存 | W5 | AC-71 |
| 按站点的额度与退避 | 每小时 ≤ 100、每轮 ≤ 20、间隔 ≥ 3 秒；微博退避不影响另外两个站点 | W1、W5 | AC-72 |
| 全部缓存与关注导入 | 微博的慢速队列只补原创；可见范围边界正常结束；从微博关注导入 | W5、W7 | AC-73、AC-74 |
| 我的 › 微博 | 关注与收藏；赞过与分组受开关控制 | W6 | AC-75 |
| 标记、直播降级与远程 | “微博”小字与四项筛选；微博账号不查开播；远程推送 Cookie 与同步订阅 | W7、W8 | AC-76 |
| 整理与依赖 | 第 6 节 | C0、D1 | 报告的整理记录与盘点结果 |

**阶段完成的判定**：B 自检的必测用例（第 7 节）与门禁全部通过，A 集中审查收敛后，才可以请用户授权 push、PR 与合并。ACT-022、Q-023 的答复与人工检查都不是合并的前提；ACT-022 没做时，报告、交付状态与需求状态必须写明微博“只经合成样本验证”，AC-65、67～70、75 的真实站点部分为 `not-run`。

**阶段完成后，用户可以：** 在登录页或“设置 › 账号”切到“微博”粘贴 Cookie 登录；把微博 App 的分享文本、`t.cn` 短链或任意微博、视频、博主链接贴进解析页，打开单条微博或博主主页并缓存视频与图片（含长微博全文、超过 9 张图、GIF 与实况）；在订阅里关联微博账号，和另外两个站点一起检查更新、按同一套规则自动缓存、全部缓存原创微博；从微博关注列表导入订阅；在“我的 › 微博”看自己的关注与收藏；在主页和媒体库按“全部 / B 站 / 抖音 / 微博”筛选；把微博 Cookie 与订阅同步给远程版。

## 2. A 已核对的现场（2026-11-21，`codex/m6-weibo` @ `eb2d810`）

- **Git**：工作区干净；阶段起点提交含修订 0、调研 [weibo.md](../research/weibo.md)、文档索引入口与 USER-ACTIONS 的 P-014 提示，本修订 amend 进同一提交。GitHub 上开着 3 个 Dependabot PR：#9（`golang.org/x/image` 0.47.0 → 0.48.0，内容已随 M5 并入 `main`，`go.mod` 现为 v0.48.0）、#12（`github.com/cloudflare/circl` 1.6.2 → 1.6.3，间接依赖，修 P-384 标量乘法）、#13（`github.com/klauspost/compress` 1.18.6 → 1.18.7，间接依赖，上游标注为安全版本，修 `s2.NewDict` 越界读）。
- **站点公共层**：`internal/site/site.go` 只认 `bilibili`、`douyin`（`All`、`Valid`、`ParseID`、`DisplayName`、`NormalizeUID` 都是两路分支）；`site/registry.go` 的 `reBareKnownDomain` 与分派不含微博域名，`< 19` 位纯数字归哔哩哔哩；`subscription.NormalizeAndValidateAccount` 对其他站点报“不支持的站点”。
- **“默认按哔哩哔哩”的分支**：`subscription.SiteHourlyQuotaLimit` 的 `default` 返回哔哩哔哩的 200，`normalizeSchedulerSite` 空值归哔哩哔哩。非测试代码里提到抖音站点标识的行约 548 处（含抖音包自身），包外集中在 `service/open_link.go`、`live/douyin_*.go`、`download/douyin_plan.go`、`ui/navigation.go`、`service/auth.go`、`subscription/round.go`、`live/site_provider.go`、`library/schema.go` 等——这就是微博的接入面，也是最容易让微博落进哔哩哔哩默认值的地方（第 4.2 节）。
- **可复用**：`credential.Manager` 按站点分文件与熵；`download.EnsureSingleMP4Context`（单文件 MP4、`moov` 前置、`mp4ff` v0.63.0）、`looksLikeDisguisedTextBody`、`site.GuardedMediaDialer` 与 `MediaTransportSpec`；抖音的 `image_note` 混排计划、`via` 转发与共创、`cache-queue.json` 慢速队列、`account_baseline_pinned`；`tools/actcheck`（act010、act011、act015、act019、act020，账本与发出前拒绝超额）。
- **调研抽查**：A 核对了调研引用的代码事实，结论与取舍写在 [weibo.md §13](../research/weibo.md#a-review)。
- **本机参考**：找到索引；没有专门的微博条目，相关领域是多站点订阅、单链接解析与直播录制（调研已逐项读过，A 抽查多站点订阅参考的微博部分与调研一致）；调研追加的 P-014 待用户处理。仓库不写参考项目名。
- **真实站点**：调研与 A 写计划时对微博 0 次请求。`.env.example` 已由 A 加上 `TRACE_TEST_WEIBO_COOKIE_FILE`。用户随后放好了 `.local/site-secrets/weibo.cookie`（6 个字段：`SUB`、`SUBP`、`XSRF-TOKEN`、`WBPSESS`、`SCF`、`ALF`，没有 `SSOLoginState`），并授权“ACT-022 登录确认”：A 发出 2 次只读请求（第 1 步账本 2/5），确认这份 Cookie 在 `weibo.com` 有效、`m.weibo.cn` 不认，登录确认的做法据此改定（weibo.md §13.1 第 2 条、§13.2）。检查对象还没给，ACT-022 的完整运行还没授权。

## 3. 依据、查证与选型

- **协议**：[微博协议资料](../research/weibo.md)第 1～12 节（静态调研，2026-11-21，0 次真实请求）与 A 的定稿 [§13](../research/weibo.md#a-review)；V-01～V-14 待 ACT-022 核对，结论回写 §13.2；V-15～V-19 随扫码与直播延后。
- **开源选型**：调研 [§10](../research/weibo.md#oss) 于 2026-11-21 经 GitHub 搜索接口、`pkg.go.dev` 与 Go 模块代理检索，受维护、许可合规、可导入的 Go 微博库为 0 个（具名候选逐项排除：停维、无许可、`internal/` 不可导入、重依赖或只做写操作）；通用 Base62 库算法不匹配（微博是 7 位/4 字符分段换算）。A 采纳：**零新增依赖**，HTTP、Cookie、JSON、Base62 用标准库，媒体复用已有的 `mp4ff` v0.63.0 与 `golang.org/x/image` v0.48.0。LOOP-008（HLS-TS）随直播延后，不引入 `mediacommon/v2`。
- **实现来源（[DEC-044](../decisions.md#dec-044)）**：协议规格已写在 weibo.md；B 只照规格独立实现，不翻译参考实现的代码、命名与函数拆分。调研之外的新做法先补进 weibo.md 再写代码。
- **B 仍需原型验证**：Base62 的 8 组向量（含越界拒绝）；`mp4ff` 能否解析 QuickTime 品牌（`ftyp=qt  `）的合成实况片段；Windows Media Foundation 能否播放 `.mov`，不能就用 `mp4ff` 无损改封装为 `.mp4`（weibo.md §13.1 第 11 条）。线上行为只能由 ACT-022 判断。

## 4. 实现指引

设计以 weibo.md 第 2～9 节与 §13 为准，下面是 A 对实现的约束；B 可以选更好的做法，但要在报告里写明。

### 4.1 依赖方向（`tools/check` 强制）

`checkDependencyDirection` 的表增加一行并补检查器测试（违例一条被拒、合规一条放行）：`internal/site/weibo` 不得依赖 `storage`、`library`、`download`、`subscription`、`live`、`service`、`ui`、`internal/site/bilibili`、`internal/site/douyin`；现有站点包同样不得依赖 `internal/site/weibo`；`download`、`live`、`subscription`、`library`、`storage`、`settings`、`uistate` 只经 `internal/site` 的类型与注册接口使用微博；`internal/ui` 只用服务层 DTO。`cmd/trace-server` 仍不含 MyGo 与界面包。

### 4.2 站点公共层与按站点分支（W1）

- `site.Weibo = "weibo"`，显示名“微博”，`All()` 顺序为哔哩哔哩、抖音、微博；`NormalizeUID(Weibo)` 接受 5～19 位正十进制、去前导零；账号键 `weibo:<uid>`。
- `Registry`：微博分派域名、分享文本取链接（站内域名优先，`t.cn` 最后）、纯数字规则不变；`WeiboMediaTransport` 按 weibo.md §13.1 第 9 条。
- **按站点分支逐个补齐**（weibo.md §13.1 第 1 条）：B 开工先 `git grep` 列出所有按站点 `switch` 与“空值/未知按哔哩哔哩”的地方，逐个加微博分支或改为对未知站点报错；报告附清单。用一个表驱动测试证明微博拿到自己的值：每小时额度 100、每轮 20、账号间隔 3 秒、显示名、作者目录、凭证文件、熔断与退避的站点行、开播巡检排除。

### 4.3 微博协议包 `internal/site/weibo`（W2）

文件按 weibo.md §10.2 的划分（`base62.go`、`client.go` 与 `errors.go`、`auth.go`、`resolve.go`、`post.go`、`user.go`、`account.go`、`adapter.go`、`testdata/`），具体拆分由 B 定。要点：

- 客户端：NFR-NET-004 的串行节奏、超时与响应上限；接口不自动跟随跳转；请求头按 §2.7（PC 主栈，`X-XSRF-TOKEN` 镜像 Cookie 里的值）；响应先按 §9.1、§9.2 判跳转、访客页、验证页、0 字节与信封，再解析；ID 一律字符串（`idstr` 或 `UseNumber`）。
- 登录确认按 §13.1 第 2 条（已按 2026-11-21 的真实请求改定：`getBasicInfo` 证明已登录、`profile/info?screen_name=` 换 UID，不用 `m.weibo.cn`）；不带 Cookie 与只带访客 `SUB` 的夹具都必须判未登录。
- `t.cn` 与 `sinaurl` 按 §3.1、§3.4（不带 Cookie、≤ 5 跳、每跳白名单、提前收敛、登录墙判 `auth_required`）。
- 单条微博按 §4（`isGetLongText`、`longtext` 补全、转发与快转、`pic_ids` 顺序、`mix_media_info`、`playback_list` 过滤与选流、`tv/api/component`、卡片降级、`visible`）；用户页按 §6（`profile/info`、`mymblog` 的 `page`+`since_id`、置顶、推广卡片、`bottom_tips`、空结果真值表 §6.5）；“我的”按 §7 与 §13.1 第 13 条。
- 全部用 `httptest` 与合成夹具（合成 ID 用 weibo.md 开头列的形状），不连真实站点。

### 4.4 凭证（W1）

`credentials/weibo.bin`，熵 `trace:weibo:credential:v1`；输入规则同另外两个站点（8 KiB、`Cookie:` 前缀、`;` 或换行、拒绝控制字符、不回显）；按 §2.2 白名单保留、没有 `SUB` 直接拒绝；确认后保存 UID、昵称、头像；`source = manual`，`Renewal()` 不可用（原因“粘贴的 Cookie 不续期”）；响应带新 `XSRF-TOKEN` 时只同步这个上下文字段。`RedactString` 覆盖 §2.2 第 4 条列的全部字段；哨兵测试覆盖粘贴、校验、浏览、缓存、订阅检查与远程推送。

### 4.5 服务与界面接口（W3）

- `AuthService.LoginWithCookie("weibo", text)`、`Logout("weibo")`、`Statuses()` 三个站点；按站点的“必须登录”（INV-07）对微博生效。
- `ParseService`：`Resolve` 分派到微博；新增 `WeiboPost(mid)`、`WeiboVideo(oid)`、`WeiboUser(uid)`、`WeiboUserPosts(uid, cursor)`（游标含 `page` 与 `since_id`，每条带 `cacheKey`）、`/n/` 与个性域名换 UID；成功后写最近解析（带 `site`）。`OpenLink` 认微博目标。
- `CacheService.CacheWeiboPost(mid, via?)`（视频页没有所属微博时按视频对象缓存）；`States` 与任务照常。
- 路由 `/weibo/post/:mid`、`/weibo/user/:uid`；帖子与卡片复用现有 `PostCard` 与详情组件，界面只用 DTO。

### 4.6 下载、缓存与数据（W4）

- 条目键、目录、文件名与规则类型按 weibo.md §13.1 第 3、4 条；`via` 转发按 DEC-056；“添加已有的缓存文件夹”与重建索引扫描 `weibo/*/{posts,forwards}/*/item.json`；媒体库“图片”“视频”按媒体角色列出微博里的图片与视频，“转发”含微博转发；已缓存数（`CountCachedWorks`）对微博数这个作者的全部微博条目（含 `via` 为他的）。
- 视频：复用抖音的单文件 MP4 路径（不要求 `elst`，有视频轨、时长与站点相差 ≤ 2 秒或 3%）；图片按文件头定扩展名、GIF 存原动图；实况按 §13.1 第 11 条；图片档位按第 10 条；地址过期（403）每个文件最多重取一次详情并按已落盘字节续传；伪装成 HTML/JSON 的响应拒绝；签名地址只在内存。
- **数据结构版本**：按 weibo.md §13.1 第 14 条核对，预期保持 2；只在发现旧版本会静默丢弃并回写微博数据时才升版本（并按数据设计的护栏做备份与可重复迁移），报告写明依据与旧版本的实际行为。

### 4.7 订阅、检查更新、全部缓存与关注导入（W5）

- `AccountRef` 可以是 `site: "weibo"`（`uid`）；`PreviewAccount` 认微博链接与分享文本。
- 检查按 weibo.md §6.3、§6.5、§6.6 与 §13.1 第 5、7 条；`update_item.type` 按第 4 条；`snapshot` 只存公开字段（`sinaimg.cn` 公开图片地址可存，视频与实况地址不存）。
- 节奏：`DefaultWeiboHourlyQuota = 100`、每轮 20 个、账号间隔 3 秒，共用 `site_scheduler_state` 的 `weibo` 行；风控、限流只停微博；手动检查不计额度。
- 全部缓存：列表名 `posts`，`origin = full_cache` 同现有；范围按 §13.1 第 6 条（只补原创）；作品总数取 `profile/info` 的 `statuses_count`；到可见范围边界（`bottom_tips_visible`）正常结束并把站点提示写进队列摘要。
- 直播：微博账号不进开播检查与状态查询；规则开了“开播自动录制”时，订阅的微博账号写“微博直播暂不支持录制”，不发请求。
- 关注导入：`FollowingService` 增加站点参数（哔哩哔哩行为与测试不变），微博读 `friendships/friends`（§13.1 第 13 条），每页随滚动加载，按分组筛选只在 V-13 通过的开关打开后提供；抖音仍不支持（LOOP-010）。

### 4.8 界面（W7，`docs/ui.md` 同步）

登录页三个站点分段；“设置 › 账号”三组（微博组写明不续期的原因）；“我的”侧栏第三组“微博”（关注、收藏；赞过受开关）；微博用户页（头部同哔哩哔哩用户页的结构，一个“微博”列表 + “全部 / 未缓存”，底部可见范围提示，“⋯ → 全部缓存”）；单条微博详情（FR-WB-003 的结构；文章、投票、音乐、外链、直播卡片是摘要 + “在浏览器中打开”）；站点小字“微博”出现在六处（订阅网格不加）；主页与媒体库筛选“全部 / B 站 / 抖音 / 微博”在 720 宽不溢出；订阅“账号”里的微博账号没有直播状态与录制按钮；“检查计划”列出微博账号；关注导入对话框可切换站点；直播链接的提示文案“微博直播暂不支持录制，可以在浏览器中打开”。错误文案按 FR-ERR-001，未登录写“请先登录微博”。

### 4.9 远程版（W8）

`PUT credentials` 接受 `weibo`，用 W2 的登录确认校验后保存；订阅快照包含微博账号；远程状态的站点登录行包含微博；远程按规则检查微博、自动缓存，不做全部缓存（与现有远程配置一致）；`-server` 包与远程冒烟覆盖微博的假站点。

### 4.10 工具（W9）

- `tools/actcheck -act act022`：读 `TRACE_TEST_WEIBO_COOKIE_FILE` 与 `.local/site-check/targets-weibo.json`（形状 `{version, act, providedAt, users[{url, expect[]}], posts[{url, expect[]}], shortLinks[], shareTexts[]}`，`expect` 取 `active|pinned|fastRetweet|halfYear|nickname|customDomain|longText|longRetweet|over9|gif|livePhoto|mixed|video|videoPage|deletedOrigin`）；步骤、额度、停止条件、账本与脱敏回执完全按 [ACT-022](../../USER-ACTIONS.md#act-022)，第 9 步用 TRACE 的缓存服务在临时数据目录走正式流程；缺对象的步骤记 `not-run`。`httptest` 测试证明：超额在发出前拒绝、每类停止条件立即停止全部请求、回执不含 Cookie、签名地址与真实 ID。
- `tools/checkdocs`、`internal/sitecheck` 与冒烟哨兵加微博模式（weibo.md §13.1 第 15 条），对 weibo.md 里的合成占位不误报。
- `tools/uishots` 新增场景：登录页微博分段、设置“账号”三组、微博用户页（含置顶、可见范围提示、空壳错误）、单条微博详情（长文、超过 9 图、转发且原微博已删、混排、视频、文章卡片）、我的 › 微博、关注导入的站点切换、四项站点筛选、带“微博”小字的帖子/卡片/任务行/最近解析、直播链接提示；三档宽度无横向滚动。

## 5. 工作包

| 工作包 | 预期结果与关键失败路径 | 完成证据 |
| --- | --- | --- |
| C0 整理 | 第 6 节 | 报告整理记录；`go run ./tools/checkdocs` |
| D1 依赖 | 关闭 PR #9；并入 PR #12、#13 的版本；`tools/deps` 盘点与 `-audit` | 报告的盘点结果 |
| W1 公共层与凭证 | 第 4.1、4.2、4.4 节；按站点分支清单；`weibo.bin` 与独立熵；三站点登录互不影响 | M6-K01～K03 |
| W2 协议客户端 | 第 4.3 节；Base62；登录确认两个候选；`t.cn`；详情、用户、列表、我的的解析与错误分类 | M6-K04～K09 |
| W3 解析与浏览 | 第 4.5 节；解析分派、最近解析、详情与用户页的服务与界面 | M6-K06～K08、K18 |
| W4 缓存 | 第 4.6 节；视频、图片、GIF、实况、混排、长文、转发、过期重取、白名单；数据版本核对 | M6-K10、K11 |
| W5 订阅与全部缓存 | 第 4.7 节；检查、额度、类型映射、全部缓存、直播排除、关注导入 | M6-K12～K15 |
| W6 我的 › 微博 | 关注、收藏；赞过与分组开关；只读、只在内存 | M6-K16 |
| W7 界面与多站点 | 第 4.8 节；标记、筛选、登录与设置、订阅账号行、提示文案 | M6-K03、K18 |
| W8 远程版 | 第 4.9 节 | M6-K17 |
| W9 工具与真实检查 | 第 4.10 节；条件到位时运行 ACT-022 并回写结论 | M6-K19；ACT-022 回执 |
| W10 文档 | 架构（站点包、目录、服务方法、凭证）、数据（条目键与目录、清单、内容范围）、订阅（微博检查、额度、全部缓存、关注导入）、直播（微博不录制）、远程（微博）、界面、工程规则（依赖方向表、`act022`、截图场景数）、交付状态、需求与路线的状态、weibo.md §13.2、USER-ACTIONS（ACT-022 结果、需要的 H-001 新步骤） | `checkdocs`；A 审查 |

连续完成全部范围、自测和修复，不逐包请求继续；需要用户的事写进 USER-ACTIONS，非阻塞的按 [Q-023](../../USER-ACTIONS.md#q-023) 的默认值实现。用户改了 Q-023 的某条时，A 回写需求与本计划后再交给 B。

<a id="cleanup"></a>
## 6. 阶段开始的整理（C0）与依赖盘点（D1）

A 已完成（修订 0 与本修订）：

1. M5 的计划与报告换成 M6 的，指向它们的链接改为 `4b37fc5` 的固定链接（修订 0）。
2. USER-ACTIONS“推荐你做的”：把 H-001 第 27～38 步的 12 行并成一行、把之前已按推荐实现的 Q-014～Q-021 并成一行，M6 的 Q-023、ACT-022 放在最前；每一步的细节仍在 H-001 与各 Q 项里。
3. `.env.example` 加 `TRACE_TEST_WEIBO_COOKIE_FILE`。

B 在本阶段处理：

| 对象 | 处理 |
| --- | --- |
| 文档里以前阶段的标签 | `docs/` 中用阶段号或返工轮次描述能力的文字（当前计划与报告、决定表除外）改为能力描述，例如 USER-ACTIONS 与交付状态里的“M3 实现细节”“第六轮反馈”“M4 起”；锚点与编号不改 |
| `artifacts/`、`dist/`、`tmp/` | 确认是忽略的生成物、各类只留最新一份（DEC-025）；`dist/trace/` 是 2026-11-09 的正式版演练产物，下次 `tools/release` 演练时替换，不单独删；`artifacts/backup-20261116/`、`artifacts/repair-20261115/` 是用户数据的备份，不删（见 USER-ACTIONS 本机文件） |
| 测试名 | 新测试按能力命名，不带阶段号或问题编号 |
| 本阶段改到的代码注释 | 旧计划与问题编号顺手改成直接描述；不为此改动无关文件 |

**D1**：

1. **PR #9**：内容已在 `main`（`golang.org/x/image` v0.48.0），用 `gh pr close 9 --comment "已随 M5（PR #11）并入 main"` 关闭。
2. **PR #12、#13**：上游标为修复（#13 写明是安全版本），按 DEC-048 的安全例外不等 7 天：`go get github.com/cloudflare/circl@v1.6.3 github.com/klauspost/compress@v1.18.7`、`go mod tidy`、`go run ./tools/check`；并入阶段提交，两个 PR 在阶段 PR 合并后关闭（报告记下，合并时由 A 处理）。失败就回退并在报告写明。
3. `go run ./tools/deps`：满 7 天的版本全部升级（`go get <模块>@<版本>`，改 `go` 行后 `go mod tidy`），跑 `go run ./tools/check`；没满 7 天的只记录。
4. `go run ./tools/deps -audit`：代码调用到的漏洞先升级修复；无法修复的登记 `tools/deps/exceptions.json`（原因与 `reviewBy`）并写进报告。
5. 两个命令跑不起来（退出码 2）记 `not-run`，不写成通过；交付前与 `tools/check` 一起复跑。

## 7. 必测用例

全部用合成样本（`httptest`、夹具、合成媒体），不连真实站点；测试安静（[工程规则](../dev-rules/engineering.md#quiet-tests)）。合成 ID、Cookie 与地址用 weibo.md 开头列的明显合成值。

| 编号 | 用例 | 对应 |
| --- | --- | --- |
| M6-K01 | 公共层：`site.Weibo` 的显示名、`All` 顺序、`NormalizeUID`（5～19 位、去前导零、拒绝非数字）、账号键往返；`Registry` 把微博域名与分享文本分给微博、站内链接优先于 `t.cn`、纯数字仍归哔哩哔哩；依赖方向检查器对微博的每条规则各一违例一合规 | W1、AC-67 |
| M6-K02 | 按站点分支：表驱动测试证明微博的每小时额度 100、每轮 20、间隔 3 秒、退避与熔断用 `weibo` 行、作者目录、凭证文件都是微博自己的值；没有任何路径把未知站点当成哔哩哔哩；微博退避或额度用完时另外两个站点照常，反之亦然；下载熔断只暂停微博排队的任务 | W1、W5、AC-72 |
| M6-K03 | 凭证与按站点登录：`weibo.bin` 用独立熵、用错熵解不开，另外两个站点的旧凭证照常读取；只登录微博时进应用、贴哔哩哔哩或抖音链接提示且 `httptest` 计数为 0，反之同理；微博失效或退出只暂停微博的任务；`RedactString` 同时脱敏三个站点 | W1、W7、AC-66 |
| M6-K04 | 微博 Cookie 登录：合格的保存并返回 UID、昵称、头像；缺 `SUB`、站点判未登录（不带 Cookie、只带访客 `SUB`、`ok = -100`、跳登录页）、验证或风控（432、418、HTML 验证页）三类分别说明且不保存、不改变已有凭证；统计字段被剥离；超长与控制字符拒绝且不回显；两个登录确认候选都有夹具；凭证文件、日志、`ui-state.json` 的哨兵扫描无 Cookie 值 | W2、AC-65 |
| M6-K05 | Base62：weibo.md §3.2 的 8 组向量双向通过，非首段 `≥ 10,000,000` 与非法字符、全零被拒；按字符串处理、不经 `float64` | W2、AC-67 |
| M6-K06 | 链接：AC-67 列出的每种输入进对的目标；`/n/` 与个性域名各 1 次请求换 UID；保留路径（`home`、`hot`、`ajax` 等）不被当成个性域名；`t.cn` 不带 Cookie、≤ 5 跳、每跳 HTTPS 与白名单，跳到外站、带账号密码或端口、超过 5 跳时停止，`sinaurl` 在本机解包且外站拒绝、0 次额外请求；直播、超话、搜索、热榜、文章、合集提示且请求数为 0 | W2、W3、AC-67 |
| M6-K07 | 单条微博：长微博从 `isGetLongText` 内联或 `longtext` 补全（转发里的长微博另补）；转发与原微博已删除（`deleted`、`user == null`、提示文本）降级且不报格式错误；超过 9 图按 `pic_ids` 顺序；GIF、实况、`mix_media_info` 顺序；`playback_list` 过滤雪碧图与隐藏档并按分辨率、编码偏好、帧率、码率选流，老视频字段兜底；视频页 `1034:` 有所属微博时归一到微博；文章、投票、直播卡片降级且不给缓存视频的入口；`visible` 的 0、1 与其他值的提示；不存在、已删除、无权查看分别归类；返回的 `idstr` 与请求不符判格式变化 | W2、W3、AC-68 |
| M6-K08 | 用户页：资料字段；第 1 页置顶排前标“置顶”；第 2 页按 `page`+`since_id` 取、两页无重复、游标原地不动时停止；推广与外人卡片剔除；`bottom_tips_visible` 正常结束并带提示；§6.5 真值表每一行（跳转、风控码、缺 `data`、空列表的三种情况与 `statuses_count` 交叉判断）都有夹具且判定正确；风控停在原处可重试 | W2、W3、AC-69 |
| M6-K09 | 错误分类：§9.2 的每一行（含 414、418、429、432、401、403、302 到访客页、HTML 假 200、0 字节、`ok` 0 与 -100、业务码）映射到正确的类别与“停本轮 / 熔断”动作；`risk_control` 与 `rate_limited` 不清除凭证；没有自动重试 | W2 |
| M6-K10 | 缓存视频：一条视频微博存成 `video.mp4`（`moov` 在后的合成源被重排，峰值内存远小于文件）；时长差超限或没有视频轨失败；断开续传；地址过期只重取一次详情；CDN 请求不带 Cookie、`Referer` 正确、非白名单主机与 HTML/JSON 响应被拒；`video.weibo.com` 只对实况首跳 `/media/play` 放行；`item.json`、`library.db`、日志里扫不到 `ssig`/`Expires`/`KID` 哨兵 | W4、AC-70 |
| M6-K11 | 缓存图文：超过 9 张图按顺序、扩展名按文件头；GIF 保存原动图；实况静帧与片段（QuickTime 品牌的合成片段能通过校验并可播放，或被无损改封装）；混排的图片、视频与封面；长微博存全文；单个文件缺失记黄点不让整条失败；转发存转发者 `forwards/<mid>/` 并记 `via`；条目键 `weibo:dynamic:<mid>`，已缓存的不搬动；重建索引能找回微博条目；媒体库“图片”“视频”“转发”筛选正确 | W4、AC-70 |
| M6-K12 | 混合站点订阅：三站点账号的订阅首次检查只建起点；微博新微博按规则类型（视频、图文、文字、转发）自动缓存；置顶与快转的旧微博不算新、不让检查提前结束；推广卡片不计；不确定的空结果记“本轮结果不完整”、起点不动；微博风控时本轮微博账号停止并说明，另外两个站点照常；没登录微博时跳过微博账号 | W5、AC-71 |
| M6-K13 | 额度与节奏：微博规则请求超过每小时 100 次推迟到下一轮；每轮 ≤ 20 个账号且间隔 ≥ 3 秒；每个账号每次最多 2 页；手动检查不计额度；检查计划列出微博账号与预计时间 | W5、AC-72 |
| M6-K14 | 全部缓存：微博用户页与定期全部缓存建立 `posts` 队列，只排原创、跳过已完整缓存的与转发、快转，部分完成的再补一次；到可见范围边界正常结束并带提示；`cache-queue.json` 只有 `mid` 与游标、没有签名地址；作品数取 `statuses_count`；队列遵守微博额度与退避 | W5、AC-73 |
| M6-K15 | 关注导入与直播排除：选微博时列出全部关注、翻页无重复、已在订阅里的不能选、导入建立订阅且不额外请求；哔哩哔哩导入行为不变；分组筛选只在开关打开时出现。开了开播录制的规则对微博账号写明不支持、开播检查与状态查询对微博的请求数为 0 | W5、AC-74、AC-76 |
| M6-K16 | 我的 › 微博：关注与收藏（`data.status` 键）翻页无重复；点开用户页与微博详情；“⋯”加入订阅与缓存；风控、未登录、失效、网络、空与到底分别说明；同一站点熔断时“我的”停下、另外两个站点不受影响；数据目录与日志里没有列表标题与用户（哨兵）；退出微博后清空；赞过与分组入口在开关关闭时不出现 | W6、AC-75 |
| M6-K17 | 远程版：`PUT credentials` 对微博用假站点校验通过才保存、失败原凭证不变；订阅快照含微博账号并保留起点；远程按规则检查微博与自动缓存、不做全部缓存；远程状态显示微博登录；远程判微博失效后停止微博请求；日志与状态里没有微博 Cookie 哨兵 | W8、AC-76 |
| M6-K18 | 截图与断言（`tools/uishots`）：第 4.10 节列的场景，三档宽度无横向滚动；断言“微博”小字出现在规定的六处、订阅网格头像不加、四项筛选不溢出、微博账号行没有录制按钮 | W3、W7、AC-68、AC-69、AC-76 |
| M6-K19 | 工具：`actcheck -act act022` 在 `httptest` 上——账本超额在发出前拒绝、每类停止条件立即停止全部请求、回执不含 Cookie/签名地址/真实 ID、缺对象的步骤记 `not-run`；`checkdocs` 与 `sitecheck` 对合成的微博 Cookie 与签名地址报错、对 weibo.md 的占位不误报；冒烟哨兵覆盖微博 | W9 |
| M6-K20 | 回归与门禁：`go run ./tools/check`、Linux `go vet`、`tools/uishots`、桌面与远程冒烟、`tools/package`、`tools/package -server` 全部通过；哔哩哔哩与抖音相关测试名集合不减少（报告列出改动前后的数量） | 全部 |

## 8. MVP 与 LOOP

- 本阶段要贯通的 MVP 流程见[路线 §3](roadmap.md#mvp-loop)的“微博”一行。
- 可以记为 LOOP 的深化：扫码登录、直播、原创筛选、评论、文章正文、视频合集、去水印开关、画质升级提醒，以及 ACT-022 没能确认的“赞过”与关注分组。
- 不得记为 LOOP 的：把访客或失效的 Cookie 当成已登录；风控空壳显示成“没有微博”或“没有更多”；微博用到哔哩哔哩的额度、间隔或熔断；Cookie 或签名地址出现在日志、文件或远程状态里；转发存错文件夹；任何一个链接形态在 0 请求的要求下发了请求。

## 9. 交付与验证

- B 交付前：`go run ./tools/check`、`go run ./tools/deps` 与 `-audit`、`go run ./tools/uishots`、桌面冒烟与远程冒烟、`go run ./tools/package -server`、`go run ./tools/package -install`（把最新开发版装到本机并保持 TRACE Dev 在托盘运行，[工程规则 §2.2](../dev-rules/engineering.md#dev-install)）。
- ACT-022：用户提供 Cookie、对象并授权后运行 1 次（B 在客户端实现后，或交给 A 在审查时），结论回写 weibo.md §13.2、阶段报告，并按结论调整默认（登录确认接口、赞过与分组开关、去水印地址、`PublicRedirects`、`feature=1`、快转归属）；没到位就记 `not-run`。
- 报告（`docs/delivery/stage-report.md`，按[交接模板 §2](../templates/agent-handoff.md)）写：每个工作包与用例的实现位置和证据、按站点分支清单、数据结构版本的核对结论、依赖盘点、开发版版本与包的 SHA-256、ACT-022 的状态与回执、`not-run` 项与限制、整理记录。
- USER-ACTIONS：ACT-022 的结果；H-001 加一步微博的实机体验（登录、解析、缓存、订阅、我的、筛选）；Q-023 有变化就更新。

<a id="prompt"></a>
## 10. 给 Agent B 的交接 Prompt

```text
你是 TRACE 的 Agent B，动作：连续实施 M6（微博）。

- 计划：docs/delivery/stage-plan.md（TRACE-M6-01 修订 1）；协议正本 docs/research/weibo.md，A 的定稿与取舍在 §13（逐条遵守）；决定 DEC-065；需求 FR-WB-001～009、NFR-NET-004、NFR-SEC-004、FR-SITE-002（需求 §2.11）；验收 AC-65～76；默认值 USER-ACTIONS 的 Q-023；真实检查 ACT-022。
- 分支 codex/m6-weibo，基点 main @ 4b37fc5；阶段起点提交（修订 0、调研与修订 1）之上，你的全部改动 amend 进这一个 DCO 提交（git commit --amend -s）。开始时核对一次分支、提交与计划；先读 AGENTS.md 与计划第 1～7 节。
- 范围：C0、D1、W1～W10 与必测用例 M6-K01～K20，连续完成、自测、修复，不逐包请示；非阻塞的按 Q-023 默认值实现并写明。开工先做第 4.2 节的“按站点分支”清单，避免微博落进哔哩哔哩的默认值。
- D1：按计划第 6 节关闭 Dependabot PR #9（内容已在 main）；把 PR #12、#13 的版本并入阶段提交（PR 本身等阶段 PR 合并后关闭）；跑 go run ./tools/deps 与 -audit。
- 必须保留：M1～M5 与 DEC-059～064 的行为与测试；数据结构版本 2（按第 4.6 节核对，只有证据表明需要时才升版本）；INV-01～14；按站点的额度、退避与熔断；凭证隔离与哨兵；安静测试、不碰本机安装的应用；只删登记文件；dist 只留最新一份。不新增第三方依赖。参考实现只读、不翻译代码（DEC-044），仓库不写参考项目名。
- 真实站点：只在 ACT-022 写明的对象与额度内、用户放好 .local/site-secrets/weibo.cookie（.env.local 的 TRACE_TEST_WEIBO_COOKIE_FILE）、给出对象并授权后，用 go run ./tools/actcheck -act act022 运行 1 次；遇停止条件立即停、不重试、不碰验证码；结论回写 weibo.md §13.2 与报告。没到位记 not-run，不阻塞。哔哩哔哩与抖音不发真实请求。
- 交付：go run ./tools/check、tools/deps 与 -audit、uishots、桌面与远程冒烟、tools/package -server、tools/package -install（装好后 TRACE Dev 保持在托盘运行）；按交接模板写 docs/delivery/stage-report.md；更新正本文档（第 5 节 W10）与 USER-ACTIONS（ACT-022 结果、H-001 新增微博步骤）。
- 未授权：push、创建 PR、合并、标签、发布。
- 完成后给出交付 SHA、报告入口、本机开发版版本与给 A 的审查 prompt。
```

<a id="research"></a>
## 附录：修订 0 的调研（已完成）

<a id="research-delivery"></a>修订 0（2026-11-21）请调研 Agent 做静态协议与选型调研：R-A 登录与凭证、R-B 链接与稳定 ID、R-C 单条微博的内容、R-D 媒体下载、R-E 用户主页与订阅检查、R-F 我的、R-G 直播、R-H 错误与请求节奏、R-I 开源选型、R-J 范围建议；红线是不向微博与新浪的任何域名发请求、不读取任何 Cookie 与浏览器登录数据、示例一律合成、只改文档。交付是 [weibo.md](../research/weibo.md)（第 1～12 节，0 次真实请求；没有值得比较的第三方库，所以没有单独的选型文件，检索结论在 §10），文档索引的入口，以及本机参考索引的 P-014 建议（USER-ACTIONS 已有提示）。A 的审阅定稿在 weibo.md §13。
