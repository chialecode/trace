# 社交站点解析研究

查阅：2026-10-16，Agent A。范围：本阶段新增的微博、小红书、Instagram、X（Twitter）、YouTube、TikTok；抖音与哔哩哔哩已实现，见[两站解析研究](site-parsing-current.md)、[Bilibili 能力表](bilibili-capabilities.md)和 [Douyin 能力表](douyin-capabilities.md)。

**证据等级：资料，另有一次有限登录探测。** 第 1～9 节来自官方公开文档、社区协议观察和本地参考索引中各站点的解析实现，**未在 TRACE 生产代码中实测**；第 10 节是 A 于 2026-10-16 按 ACT-007 做的一次只读登录与目标探测，不是生产规则验证。端点、参数和签名会随站点前端更新而变化；B 实施前先用脱敏样本核对，真实验证按 [USER-ACTIONS ACT-007](../../USER-ACTIONS.md#act-007) 进行。规则设计见[多站契约](../design/multi-site-contracts.md)。

## 1. 总览

| 站点 | 官方开放接口能否满足 | 推荐取数方式 | 登录 | 主要风险 | 风险 |
| --- | --- | --- | --- | --- | --- |
| 微博 | 不能：开放平台只开放授权用户自己的时间线 | 网页端 JSON 接口（桌面 ajax，移动 container 作回退） | 推荐 | 访客限制、频率限制 | 中 |
| 小红书 | 没有公开内容接口 | 页面内嵌状态 + 网页端接口 | 推荐 | 列表续页需要前端签名，登录拦截与验证码 | 高 |
| Instagram | 不能：Graph API 只服务应用管理的专业账号 | 网页端接口与 GraphQL | 需要 | 账号风控、请求频率、GraphQL 文档 ID 变化 | 高 |
| X | 能但收费（X API v2 付费） | 网页端 GraphQL | 需要 | 查询 ID 与特性参数随版本变、事务 ID 请求头、账号风控 | 高 |
| YouTube | 元数据能（Data API v3，需 API Key 与配额）；媒体不能 | 网页端公开接口取列表与元数据；媒体由外部解析工具 | 不需要 | 媒体地址需要播放器脚本解密与 PO Token | 中（元数据）/高（媒体） |
| TikTok | 不能：Display API 只服务授权用户，Research API 需审批 | 页面内嵌状态 + 网页端接口 | 可选 | 列表接口需要前端签名，CDN 下载需要会话 | 高 |

结论：六个新站都需要站点专用规则；只有 YouTube 媒体解析交给外部工具（理由见[开源候选](open-source-candidates.md#social-sites-2026-10-16)）。

## 2. 微博

| 项目 | 资料 |
| --- | --- |
| 身份 | 用户 `uid`（数字，稳定）；自定义主页名可变。微博 `id`/`mid`（数字）与链接中的短编号（base62）一一对应 |
| 链接 | `weibo.com/u/<uid>`、`weibo.com/<自定义名>`、`m.weibo.cn/u/<uid>`、`m.weibo.cn/profile/<uid>`；详情 `weibo.com/<uid>/<短编号>`、`m.weibo.cn/detail/<id>`、`m.weibo.cn/status/<id>` |
| 列表 | 桌面 `weibo.com/ajax/statuses/mymblog?uid=&page=&feature=0`（页码分页，可带 `since_id`）；移动 `m.weibo.cn/api/container/getIndex?type=uid&value=<uid>&containerid=107603<uid>&since_id=` |
| 详情 | `weibo.com/ajax/statuses/show?id=<短编号>`；长文全文 `weibo.com/ajax/statuses/longtext?id=`（移动 `m.weibo.cn/statuses/extend?id=`） |
| 媒体 | 图片 `pic_infos`（`largest`/`original`/`mw2000`）；实况图为 `livephoto` 类型并带视频地址；视频在 `page_info.media_info`（多个清晰度地址）或混合媒体 `mix_media_info` |
| 元数据 | 发布时间、来源客户端、IP 属地（`region_name`）、转发/评论/点赞数、话题、链接卡片、被转发微博、长文标记、编辑次数与时间、置顶、可见范围 |
| 登录 | Cookie（`SUB` 等）；未登录走访客系统，可读范围有限且更易被限 |
| 建议 | 用桌面接口 + 登录 Cookie；访客与移动接口作为回退。转发保留被转发微博的关系。头条文章本阶段只保存摘要与链接（`partial`） |

## 3. 小红书

| 项目 | 资料 |
| --- | --- |
| 身份 | 用户 `user_id`、笔记 `note_id`（均为 24 位十六进制，稳定）。访问笔记通常需要配套的 `xsec_token`（来自列表或分享链接） |
| 链接 | 主页 `www.xiaohongshu.com/user/profile/<user_id>`；笔记 `www.xiaohongshu.com/explore/<note_id>?xsec_token=…`、`/discovery/item/<note_id>`；短链 `xhslink.com/…` |
| 列表 | 主页页面内嵌状态（`__INITIAL_STATE__`）含首屏笔记；续页 `edith.xiaohongshu.com/api/sns/web/v1/user_posted?num=30&cursor=&user_id=&xsec_token=…` 需要前端脚本生成的 `x-s`、`x-t`、`x-s-common` 请求头 |
| 详情 | 笔记页面内嵌状态（`note.noteDetailMap`）；或签名接口 `/api/sns/web/v1/feed` |
| 媒体 | 图片 `imageList`（`urlDefault`、`infoList` 多尺寸）；实况图的 `stream.h264[].masterUrl`；视频 `media.stream` 下 h264/h265/av1 多个流，按流类型挑无水印的最高规格 |
| 元数据 | 标题、正文、话题（`tagList`）、提及（`atUserList`）、发布时间与最后更新时间、IP 属地、点赞/收藏/评论/分享数、笔记类型 |
| 登录 | Cookie（`a1`、`web_session`、`webId`）；未登录常被重定向到登录或出现验证码 |
| 建议 | 已实现主页首屏 + 笔记详情（新笔记出现在首屏）。用户要求完整历史（[DEC-041](../decisions/confirmed-decisions.md#dec-041)）：续页签名在适配器内纯计算，输入只有 Cookie 里的设备标识 `a1`，由 Core 单独交给适配器，登录 Cookie 不出 Core；算法与输入的依据见[开源候选](open-source-candidates.md#xhs-signing-2026-10-18)。站点拒绝签名时停止，不引入浏览器自动化 |

## 4. Instagram

| 项目 | 资料 |
| --- | --- |
| 身份 | 用户 `pk`（数字，稳定）；用户名可变。帖子 `pk` 与链接短码（`/p/<短码>/`、`/reel/<短码>/`）可互转 |
| 资料 | `www.instagram.com` 的 `api/v1/users/web_profile_info` 端点（参数 `username`），需带网页端应用 ID 请求头（`x-ig-app-id`）；返回用户 ID 与最近约 12 条 |
| 列表 | 登录后 `www.instagram.com/api/v1/feed/user/<user_id>/?count=12&max_id=`；或网页端 GraphQL（`/graphql/query`，按 `doc_id` 指定查询，需页面令牌），`doc_id` 会变 |
| 详情 | `www.instagram.com/api/v1/media/<pk>/info/`（登录）或按短码的 GraphQL 查询 |
| 媒体 | `image_versions2.candidates` 取最大；`video_versions` 取最大；轮播 `carousel_media` 逐项混合；`video_dash_manifest` 可能有更高规格（音视频分离） |
| 元数据 | 发布时间、正文、话题、被标记用户、地点名称、点赞（可能被作者隐藏）/评论/播放数、类型（帖子/轮播/Reels）、商业合作标记、Reels 配乐、无障碍描述 |
| 登录 | 需要（`sessionid`、`ds_user_id`、`csrftoken`）。频繁请求会触发验证或临时封禁 |
| 建议 | 按浏览器的实际路径实现（见 [§10 实测](#instagram-browser-path)）：主页 HTML 取用户 ID 与页面令牌，GraphQL 取资料与帖子列表；`doc_id` 与查询名做成适配器可更新的数据。`web_profile_info` 浏览器已不调用，2026-10-16 首个请求即 429，只作回退。预算保守（每页间隔数秒），遇到验证立即停止 |

## 5. X（Twitter）

| 项目 | 资料 |
| --- | --- |
| 身份 | 用户 `rest_id`（数字，稳定）；`screen_name` 可变。推文 ID 为时间有序的数字 |
| 链接 | `x.com/<screen_name>`、`twitter.com/<screen_name>`；推文 `x.com/<screen_name>/status/<id>` |
| 接口 | 网页端 GraphQL：`/i/api/graphql/<queryId>/UserByScreenName`、`UserTweets`、`UserMedia`、`TweetResultByRestId`/`TweetDetail`。需要网页端公开 Bearer、Cookie `auth_token` 与 `ct0`（并作 `x-csrf-token`），以及 `features`/`fieldToggles` 参数；部分接口要求 `x-client-transaction-id` 请求头（由首页与前端脚本计算） |
| 分页 | 返回时间线指令，`cursor-bottom` 续页；置顶推文单独给出 |
| 媒体 | 图片取原图（`name=orig`）；视频与 GIF 在 `video_info.variants`，取码率最高的 MP4 |
| 元数据 | 发布时间、全文（长推文 `note_tweet`）、话题、提及、展开后的链接、语言、来源客户端、浏览/点赞/转推/回复/引用/书签数、敏感标记、转推与引用关系、会话 ID、编辑记录 |
| 登录 | 需要；未登录基本看不到用户时间线 |
| 官方 | X API v2 付费，可作为将来的可选来源，本阶段不用 |
| 建议 | 查询 ID 与特性参数做成适配器内可更新的数据，并能从前端脚本发现；事务 ID 请求头做可行性试验，必要时只覆盖不需要它的接口并标 `partial`。转推、引用保存关系；回复与点赞列表不在本阶段 |

## 6. YouTube

| 项目 | 资料 |
| --- | --- |
| 身份 | 频道 ID（`UC…`，稳定）；`@handle` 可变。视频 ID 11 位；社区帖子 ID `Ug…` |
| 链接 | `youtube.com/@<handle>`、`/channel/<id>`、`/c/…`、`/user/…`；视频 `watch?v=`、`youtu.be/`、`/shorts/<id>`；帖子 `/post/<id>` |
| 列表 | 网页端公开接口 `youtubei/v1/browse`（频道 ID + 标签页参数：视频、Shorts、帖子），续页令牌分页，不需要 API Key。官方 [Data API v3](https://developers.google.com/youtube/v3/docs) 也能列上传（上传播放列表 + `playlistItems.list`，每日配额默认 10000 单位），但需要用户自备 API Key |
| 元数据 | `youtubei/v1/player` 与 `next`：标题、简介、关键词、时长、播放数、点赞、发布/上传日期、分类、是否直播/首映、章节（简介时间戳）、字幕轨列表 |
| 媒体 | 格式地址需要用播放器脚本解出签名与 `n` 参数，部分客户端还要 PO Token，且服务器在推广分段流协议；自行实现等于长期追踪 YouTube 播放器变化 |
| 社区帖子 | 帖子标签页返回文字、图片、投票、附带视频的帖子 |
| 登录 | 公开内容不需要；会员、年龄限制内容本阶段不支持 |
| 建议 | 列表与元数据由 TRACE 适配器实现；视频格式与地址交给锁定版本的外部解析工具，见[多站契约 §10](../design/multi-site-contracts.md#youtube-media) |

## 7. TikTok

| 项目 | 资料 |
| --- | --- |
| 身份 | `secUid`（稳定）；`@uniqueId` 可变；数字用户 ID。作品 ID 为时间有序数字。与抖音是不同站点，身份与规则不混用 |
| 链接 | `www.tiktok.com/@<uniqueId>`；视频 `/@<u>/video/<id>`；图片帖子 `/@<u>/photo/<id>`；短链 `vm.tiktok.com/…`、`vt.tiktok.com/…` |
| 资料与详情 | 页面内嵌状态 `__UNIVERSAL_DATA_FOR_REHYDRATION__`：`webapp.user-detail`（含 `secUid` 与统计）、`webapp.video-detail`（作品结构） |
| 列表 | `www.tiktok.com/api/post/item_list/?secUid=&count=&cursor=` 需要设备参数、`msToken` 与前端签名（`X-Bogus` 等），签名不对时返回空内容而不是错误 |
| 媒体 | `video.bitrateInfo[]` 多清晰度（含 H.265）、`playAddr`；图片帖子 `imagePost.images[].imageURL`；配乐 `music.playUrl`。CDN 下载需要与页面相同的会话 Cookie 与 Referer |
| 元数据 | 发布时间、正文、话题与提及（`textExtra`）、播放/点赞/评论/分享/收藏数、配乐信息、地区、时长与尺寸、合拍/拼接关系、广告标记 |
| 登录 | 公开内容可选 |
| 建议 | 详情走页面内嵌状态（不需要签名）；列表签名做可行性试验，空响应必须当作“未知”而不是“没有更新”；做不到时 `user.videos` 标 `partial`，保留单链接与预览中首屏内容 |

## 8. 抖音与哔哩哔哩的补充

- 两站的规则在各自适配器的 `rules.rs` 声明（投稿视频、图集、长图文、全部投稿、直播订阅与预览，以及视频、直播间链接，哔哩哔哩另有动态、图文、专栏、音乐卡），只有已有真实证据的规则标 `implemented` 并带日期和范围，其余标 `partial` 并写明只经合成夹具验证。短链（`b23.tv`、`v.douyin.com`）与作者主页、直播间、移动端分享页的域名都能被识别；`space.bilibili.com` 只用于识别链接，列表请求仍走接口域名。
- 统一元数据：Core 的 `legacy_metadata` 把两站已保存的安全元数据映射到[多站契约 §4](../design/multi-site-contracts.md#metadata)的统一字段，不重新请求旧内容。新观察没有自带统一字段时同样读自己的记录；此前保存的条目由后台有界、幂等地补齐。映射的字段是公开链接、时长与尺寸、分类、合集、话题与提及、IP 属地、转发来源、置顶与广告标记和计数。
- Bilibili 空间视频列表带浏览器会附加的参数（`web_location`、`order_avoided`、`dm_img_*`，以及从作者空间页取得的 `w_webid`），查询按固定顺序书写并参与 WBI 签名，有固定向量测试（[状态 LOOP-002](../delivery/status.md#followup-loops)）。空间页每位作者每 30 分钟最多多读一次；读不到身份时列表照常发出。新形式对真实站点的验证在 P13 完成前未做。

## 9. 共同风险

| 风险 | 应对 |
| --- | --- |
| 签名与查询参数随前端更新而变 | 放在适配器内可单独更新的数据与模块里；固定向量测试；失败归类为 `schema_changed`，不前移游标 |
| 空响应冒充“没有更新” | 每站定义“确认为空”的条件；其余空响应记为未知并退避 |
| 账号风控 | 保守预算与间隔，遇到验证/挑战立即停止；建议用不常用的账号；不做任何写操作 |
| 网络不可达 | 按站点代理设置（[多站契约 §9](../design/multi-site-contracts.md#proxy)） |
| 媒体地址短期有效 | 地址不入库；下载前重新解析；过期重解析，不静默降级 |

<a id="login-probe-2026-10-16"></a>
## 10. 2026-10-16 A 登录与目标探测（有限实测）

按 [ACT-007](../../USER-ACTIONS.md#act-007) 授权，用用户提供的 Cookie 和每站一个创作者主页做一次只读探测：六个新站点用一次性诊断脚本，抖音与哔哩哔哩用上一版生产包的 `site-check`。网络环境为代理软件的 TUN 模式：本机 DNS 对**所有**站点（含国内站点）都返回 `198.18.0.0/15` 虚拟地址；Instagram、X、YouTube、TikTok 经本机 HTTP 代理，其余直连。合计 18 次请求、约 0.90 MB 响应、0 字节媒体（含 Instagram 重试）；原始回执只在本机 `.local/site-check/`，这里只写结论。

| 站点 | 请求 | 观察 | 对实现的含义 |
| --- | --- | --- | --- |
| 微博 | `weibo.com/ajax/profile/info`、`ajax/statuses/mymblog` 第 1 页，各 1 次，均 200 | 目标 uid 一致；首页 22 条，带 `since_id` 续页标记 | 桌面 ajax 路线可用；登录状态本身未单独探测 |
| 小红书 | 作者主页 HTML 1 次，200 | `__INITIAL_STATE__` 可解析，`user.loggedIn` 为真，基本资料存在，首屏 32 条笔记 | 首屏可从页面状态取；续页仍需签名（OSS-013） |
| Instagram | 先 `api/v1/users/web_profile_info` 1 次，**429**，停止；用户更新 Cookie 并提供一份正常浏览的浏览器记录后，按浏览器路径重试 2 次（直连经 TUN），均 200 | 主页 HTML 含用户 ID 与页面令牌；第 1 页 12 条帖子（10 图、1 视频、1 轮播），有下一页游标 | 按浏览器路径实现，见下方说明；429 的原因（端点、Cookie 或出口）未单独区分 |
| X | `1.1/account/settings.json`（`x.com/i/api` 与 `api.x.com` 各 1 次）均 404、错误码 34；首页 HTML 2 次，200 | 该 1.1 端点对网页会话不可用；首页 `__INITIAL_STATE__.session.user_id` 存在，**已登录** | 登录探测改用首页会话状态；目标识别与列表走 GraphQL，未探测 |
| TikTok | 作者主页 HTML 1 次，200 | `__UNIVERSAL_DATA_FOR_REHYDRATION__` 中 `webapp.user-detail` 状态码 0，uniqueId 一致，有 id/secUid/作品数；`webapp.app-context.user.uid` 存在，**已登录** | 账号识别可从页面状态取；列表接口签名仍待 OSS-013 |
| YouTube | 频道页 HTML 1 次，200，无 Cookie | `ytInitialData` 存在，可取 `UC…` 频道 ID；有 Videos、Shorts、Posts 标签 | 三个通道入口都在；媒体仍按多站契约 §10 |
| 哔哩哔哩 | 生产 `site-check`：登录、账号、空间视频第 1 页，共 4 次，均 200 | 已登录；账号键与目标一致；首页 30 条，仍有更多 | 既有规则对新目标仍可用 |
| 抖音 | 生产 `site-check`：登录、账号，共 2 次，均 200 | 登录有效；账号键与目标一致 | 同上；该目标的投稿列表已在 2026-10-15 验证 |

另一个与代理设计有关的观察：注册表的系统代理开关为开但地址为空，等同没有系统代理；现有 Core 把 `198.18.0.0/15` 视为可连接地址，两站原有的真实链路正是在这种环境下通过的。

<a id="instagram-browser-path"></a>
**Instagram 的浏览器路径（2026-10-16，来自用户正常浏览作者主页的浏览器记录，只提炼结构）：** 浏览器打开主页时不调用 `web_profile_info`，而是：

1. `GET /<用户名>/` 返回 HTML，内含 `profile_id`、页面令牌 `DTSGInitialData.token`（表单 `fb_dtsg`）与 `LSD.token`（表单 `lsd` 与请求头 `x-fb-lsd`）。
2. 资料：`POST /api/graphql`，查询名 `PolarisProfilePageContentQuery`，变量含 `id`（用户 ID），返回 `user` 与 `viewer`。
3. 帖子列表：`POST /graphql/query`，查询名 `PolarisProfilePostsQuery`，变量为 `username` 与 `data`（`count: 12` 等），返回 `xdt_api__v1__feed__user_timeline_graphql_connection`（`edges[].node` 的 `media_type` 1 图、2 视频、8 轮播；`page_info.end_cursor` 续页）。
4. 请求头：`x-ig-app-id`（网页端固定值）、`x-asbd-id`、`x-csrftoken`（等于 Cookie 的 `csrftoken`）、`x-fb-friendly-name`（查询名）、`x-fb-lsd`，表单编码提交；表单的 `jazoest` 由 `fb_dtsg` 计算。

`doc_id` 与上述常量会随 Instagram 前端发布变化，适配器把它们作为可单独更新的数据，失效时归类为 `schema_changed` 并停止，不当作“没有更新”。浏览器记录本身含登录凭证，只在本机使用，不进仓库。

<a id="login-probe-2026-10-18"></a>
## 11. 2026-10-18 A 对登录探测的核对（有限实测）

第一轮真实检查里，微博与 Instagram 都被生产适配器判成“未登录”。A 按 [ACT-007](../../USER-ACTIONS.md#act-007) 的新额度直连（经 TUN）做了 6 次只读请求，并对照用户已登录浏览器的页面结构，当时判断两份 Cookie 都是有效的登录会话、错的是探测的判据。Instagram 的结论有带与不带 Cookie 的对照，成立；微博那一行缺“不带 Cookie”的对照，后来证明是错的（[§13](#weibo-recheck-2026-10-18)）。回执只在本机 `.local/site-check/`，这里只写结论。

| 站点 | 请求 | 观察 | 结论 |
| --- | --- | --- | --- |
| 微博 | `weibo.com/ajax/config/get_config` 1 次，200；`ajax/feed/allGroups` 1 次，200 | 同一 Cookie：配置接口答 `ok: -100`（访客墙），分组列表答 `ok: 1` 并返回分组；Cookie 的登录字段还有约三周才到期 | 当时的结论“配置接口不能证明登录、改用分组列表”是错的：分组列表对访客也答 `ok: 1` 并给出分组，配置接口的 `-100` 是对的（Cookie 那时已经不是登录状态），见 [§13](#weibo-recheck-2026-10-18)。移动站 `m.weibo.cn` 是另一个 Cookie 域名，这一点成立 |
| Instagram | 首页 HTML 带 Cookie 3 次、不带 Cookie 1 次，均 200 | `CurrentUserInitialData` 里 `USER_ID`、`ACCOUNT_ID` 在已登录和未登录时都是 0（它们是关联的 Facebook 账号）；`NON_FACEBOOK_USER_ID`、`IG_USER_EIMU` 带 Cookie 时不为 0、不带时为 0；改用完整的浏览器 User-Agent 不改变结果。用户浏览器记录里的已登录页面是同样的形态 | 探测改读 Instagram 账号 ID。走直连或走代理不是原因 |

另外两处参考结论：TikTok 网页端的视频地址在 `*.tiktok.com` 下，需要生成它的页面会话（浏览器会带上同站 Cookie），只靠 Referer 与 User-Agent 不够，见[多站契约 §10](../design/multi-site-contracts.md#youtube-media)；X 的 GraphQL 查询标识随前端发布变化，同类项目把一份已知可用的标识表随程序发布并从公开整理的文档更新，网页端请求还带事务 ID 请求头。

<a id="round-2-2026-10-18"></a>
## 12. 2026-10-18 B 的第二轮真实检查观察（有限实测）

按 [ACT-007](../../USER-ACTIONS.md#act-007) 的新额度，经生产包的产品接口做只读检查（含 8 次手动单次诊断请求：微博 5、Instagram 2、YouTube 1，都计入额度）。原始回执只在本机 `.local/site-check/`，这里只写对实现有影响的观察；逐规则的结果见[阶段报告 §4](../delivery/stage-report.md#m2-rule-status)。

| 站点 | 观察 | 对实现的含义 |
| --- | --- | --- |
| X | 随适配器发布的查询标识与特性开关直接被站点接受，没有触发重新读取，也没有拒绝未签名的请求；检查账号的列表里“有视频”的条目都是转推（列表曾把原推的媒体算作转推自己的，保存的作品并不带） | 事务 ID 请求头只经固定向量验证；列表提示改为与保存内容一致（转推只算文字） |
| YouTube | 频道视频列表给的是紧凑的年龄（“9h ago”“12d ago”“2w ago”“1mo ago”），不是完整单词 | `parse_ago` 同时认紧凑单位与完整单词，范围判断仍按带误差的近似日期 |
| TikTok | 视频地址 `*.tiktok.com` 带上页面响应设置的会话 Cookie 与凭证 Cookie 后可下载（不再 403）；连续约 30 次请求后列表接口开始答复“无数据” | 会话 Cookie 只在 Core 内存里保留；限流按规则停止，不当作没有更新 |
| 小红书 | 首屏 32 条没有发布时间也没有后续页；视频笔记的流在换了名字的分组里，按条目自述的编码分类后可下载 | 无日期的列表要逐条取详情才能判断范围：订阅的第一次检查约 33 次请求，之后只对新条目取详情；检查脚本按列表日期挑增量对象，对这种列表挑不出，要另行设计 |
| 微博 | 同一份 Cookie：分组列表答“已登录”，主页资料与微博列表接口答“需要登录”（`ok: -100`），带 Cookie、带主页 Referer 与不带 Cookie 的结果相同；2026-10-16 A 的探测里这两个接口曾答 200 | 停止，等用户重新复制 Cookie；原因见 [§13](#weibo-recheck-2026-10-18)：Cookie 已经不是登录状态，分组列表不能证明登录 |
| Instagram | 检查目标的主页 HTML 里的页面路由是通用错误页（页面类型“主页”），没有主页 ID；走代理与直连一样；页面状态仍标明 Instagram 账号已登录 | 适配器把它归为“站点不显示该主页”（来源不可用），不再当作页面改版；是否是目标账号的原因要用户在浏览器里确认 |

<a id="weibo-recheck-2026-10-18"></a>
## 13. 2026-10-18 A 对微博登录的复核（有限实测）

B 第二轮报告“分组列表通过、主页资料答需要登录”，A 按 [ACT-007](../../USER-ACTIONS.md#act-007) 的额度直连做了 4 次只读请求（0 字节媒体），先补上 §11 缺的“不带 Cookie”对照。回执只在本机 `.local/site-check/`。

| 请求 | 结果 |
| --- | --- |
| `ajax/feed/allGroups`，**不带** Cookie | 200，`ok: 1`，返回分组，字节数与 §11 带 Cookie 时完全相同 |
| `ajax/profile/info?uid=…`，带 Cookie 与 Core 的请求头（含防伪头） | 200，`ok: -100` |
| 同上，换成第一次探测用的短 User-Agent、不带防伪头 | 200，`ok: -100` |
| 用户主页 `weibo.com/u/…`，带 Cookie | 302，转到新浪登录页，按规则停止 |

结论：

- 分组列表对访客也答 `ok: 1` 并给出默认分组，不能证明登录；§11 据此做的“修正”把访客当成了已登录。探测改回桌面端配置接口，`-100` 算访客。当时写的“已登录”判据（回答里有用户 ID 或 `login: true`）是猜的，用新 Cookie 核对后是错的，见下面的第三次核对。
- 这份 Cookie 已经不是登录状态：2026-10-16 它还能取到主页资料和微博列表，2026-10-17 起所有接口都按访客回答，与请求头、User-Agent 无关。`ALF` 等字段没有到期，说明是站点那边让会话失效了（例如浏览器之后刷新或重新登录了会话）。处理：用户重新复制 Cookie（[ACT-007](../../USER-ACTIONS.md#act-007)）。
- 不带登录时主页资料与微博列表都答 `-100`，TRACE 不使用站点的访客身份，所以微博的规则标为“需要登录”。

<a id="weibo-recheck-2026-10-18-new-cookie"></a>
### 第三次核对：用户重新复制的 Cookie（2026-10-18）

用户按 [S-001](../../USER-ACTIONS.md#s-001) 重新复制了 Cookie，并提供了一份已登录浏览器的 HAR（只在本机 `.local/`，导出时浏览器已去掉请求里的 Cookie）。A 先离线读 HAR 的形态，再按 ACT-007 经用户代理做了 4 次只读请求（0 字节媒体），每个判据都有不带 Cookie 的对照；回执只在本机 `.local/site-check/`。

| 请求 | 带新 Cookie | 不带 Cookie |
| --- | --- | --- |
| `ajax/config/get_config` | 200，`ok: 1`，`data` 里只有 `ab_test`，没有用户 ID，也没有 `login` | 200，`ok: -100` |
| 首页 `weibo.com/` | 200，页面的 `window.$CONFIG` 有 `uid` 与 `user`（`idstr` 为非零数字，与 `uid` 相同） | 302，转到 `passport.weibo.com` 的访客登录 |

HAR 里已登录会话的配置接口回答与上表相同，首页的 `window.$CONFIG` 形态也相同。

结论：

- 新 Cookie 是登录状态，S-001 解决。
- 第二次核对后写的判据会把这个已登录会话判成“未知”，所有规则都会因此拒绝运行（F-068）。判据改为两步：配置接口 `-100` 为访客；`ok: 1` 之后读首页，`window.$CONFIG` 写出当前用户才算已登录；首页被转到 `passport.weibo.com` 为访客；其余为未知（[多站契约 §11](../design/multi-site-contracts.md#credentials)）。
- 列表、下载与增量检查由 B 第三轮在最终包上做。

<a id="round-4-2026-10-19"></a>
## 14. 2026-10-19 B 第四轮最终包观察（有限实测）

按 [ACT-007](../../USER-ACTIONS.md#act-007) 的剩余额度，在最终包 `dist/trace-0.1.0-windows-x64-12` 上用隔离 Profile、产品控制接口和全局手动代理（DEC-035）做只读检查；原始回执只在本机 `.local/site-check/run-m2-r4*/`，逐规则结果见[阶段报告 §8.5](../delivery/stage-report.md#b-m2-round-4)。这里只写对实现有影响的观察。

| 站点 | 观察 | 对实现的含义 |
| --- | --- | --- |
| 微博 | 登录探测与账号识别、列表 2 页（20 条均有日期）通过；图片/文字推文链接通过；视频推文详情给出 0 个媒体候选；预览勾选 1 项导出、1 项失败；增量(关)找到 1 条但保存状态 `unknown`，第二次检查集合不变；增量(开)没有视频候选 | 微博视频详情还有缺口；订阅规则保持 `partial` |
| 小红书 | 账号识别通过；带 `a1` 的签名续页请求被站点以 HTTP 461 拒绝，按 DEC-041 立即停止，完整历史标为未取完；没有换参数重试 | 签名固定向量与离线夹具通过，但站点不接受本次请求；规则保持 `partial` |
| Instagram | 登录探测未报失效；新目标主页仍被站点换成通用错误页（`source_unavailable`），账号识别失败 | 目标本身或站点对该会话的限制仍未解除；不重试、不绕过 |
| X | 账号、列表 2 页、预览勾选、图片/文字推文链接通过；前两页仍无原生视频 | 订阅与视频推文规则保持 `partial` |
| YouTube | 账号识别通过；列表页返回 200 但不含初始数据（`schema_changed`） | 与列表没有日期是不同问题：这是页面形态变化或访问环境触发的页面；规则保持 `partial` |
| TikTok | 账号识别通过；列表请求被站点以限流答复（`rate_limited`），立即停止 | 连续请求后的限流仍然存在；不重试 |
| 抖音 / 哔哩哔哩 | 账号、列表、预览勾选、视频链接通过；增量检查在额度用尽时停止，抖音的保存状态仍为 `unknown` | 订阅规则保持 `partial`；增量检查的请求数仍偏高，后续检查脚本可再压缩 |

处理：本轮没有删除检查、放宽“订阅要两次增量”的口径，也没有把限流、签名拒绝或额度用尽写成“没有更新”。规则状态以[阶段报告 §4.1](../delivery/stage-report.md#m2-rule-status) 和 `docs/evidence/rule-evidence.json` 为准。

## 15. 2026-10-19 B 集中返工后的边界

F-069～F-084 的返工只改检查驱动、管理流程、工具更新和证据绑定，没有追加任何真实站点请求，因此 §14 的逐站观察和旧包归属不变。候选包更新为 `dist/trace-0.1.0-windows-x64-13`；新的真实检查在获得新授权或站点状态变化后才会产生，新回执会带包摘要与源码树，旧回执不补写新包名。八站订阅规则仍全部 `partial`。
