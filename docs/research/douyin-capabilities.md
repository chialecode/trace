# 抖音视频能力表与第二站接入准备

日期：2026-09-26；Agent A 资料/源码审阅。抖音仍是首批优先平台，但生产适配安排在 **M1b**。此次完成独立能力表和规范化边界；M0/M1a 可使用其异构结构合成夹具检验核心契约，不提前交付一个未经验证的抖音下载入口。真实 OAuth、Cookie、列表和媒体下载均 `not-run`。

依据 DEC-005/008；实现顺序理由见 [ADR 台账](../decisions/architecture-decisions.md)。当前未知统一归 OSS-008/ACT-002，资料快照见[官方来源](2026-09-26/douyin-official-sources.json)、[社区源码记录](2026-09-26/source-inspection.json)与[维护快照](2026-09-26/maintenance.json)。

## 1. 两条路径的实际边界

| 路径 | 有证据的能力 | 限制及选择 |
| --- | --- | --- |
| 官方开放平台 | 经应用权限申请、创作者授权后查询账号视频列表及特定视频数据 | `video.list`/`video.data` 不是普通观看用户 Cookie 权限；未证实可枚举任意关注创作者或获取完整可归档媒体计划。不能把自有创作者管理 API 写成全平台下载 API |
| 网页社区接口 | 活跃 TikTokDownloader 中可见抖音 `aweme/post` 和 `aweme/detail` 路线，包含 sec_user_id/aweme_id、游标和作品数组 | 会话、签名、风控、媒体质量/期限均需独立验证。项目 GPL-3.0，只用作资料，不直接复制/捆绑；生产复用需许可及分发决定。不因仓库名字包含 TikTok 而混用两站字段 |

目前官方路径不足以证明 TRACE 的任意创作者收录闭环；社区路径也未完成 TRACE 的现场验证。因此本轮优先有较完整当前 Rust 参考链路的 Bilibili，同时保存抖音的必查问题供 M1b 接续。该决定没有降低抖音的产品优先级，也没有预选 yt-dlp 等工具作为最终实现。

## 2. 独立能力表

级别 O=官方文档；S=社区源码；R=问题讨论；T=TRACE 实测（此次均无 T）。

| 能力 | 可验证资料事实 | TRACE 设计要求与未知 | 证据 |
| --- | --- | --- | --- |
| 账号身份 | OAuth 使用应用上下文中的 open_id；网页 account 接口使用 sec_user_id | PlatformAccount 分别记录 identity namespace 与别名证据，未证实前不把 open_id/sec_uid/uid 相等或直接合并；昵称/短链接不是身份 | O：授权文档；S：account.py |
| 内容身份 | 官方响应有 item_id、video_id；网页详情按 aweme_id 获取 | 持久化来源命名空间+稳定 ID；转换需要可核对映射，item_id 编码不可当媒体哈希 | O：video.list/data；S：detail.py |
| 授权列表 | `GET /video/list/` scope `video.list`，需要用户授权和申请权限；cursor/has_more；资料称实时，暂不支持时间过滤 | 仅授权范围，不能跳到指定时间或承诺任意作者历史。游标和页同事务，客户端时间范围筛选保留 coverage 证据 | O：[账号视频列表](https://open.douyin.com/platform/resource/docs/openapi/video-management/douyin/search-video/account-video-list) |
| 网页列表 | `aweme/v1/web/aweme/post/` 使用 sec_user_id/max_cursor，返回 aweme_list/has_more | 接口可见不等于能稳定枚举；需登录/未登录、置顶、重复游标、空页仍 has_more、乱序/编辑样本；不能单凭时间提前宣称扫描完成 | S：[account.py](https://github.com/JoeanAmier/TikTokDownloader/blob/473c90ff70c663cfb69310fff2b8d5192f200661/src/interface/account.py)；R：issue 740 只作排障线索 |
| 详情与正文 | 官方 `POST /video/data/` 查询授权用户公开视频；隐私视频可能 create_time=0；社区按 aweme_id 获取 aweme_detail | 元数据快照规范化，未知时间保持未知；未返回/时间零不代表删除。完整详情才形成完整 Revision；统计变化与内容版本分离 | O：[视频数据](https://open.douyin.com/platform/resource/docs/openapi/video-management/douyin/search-video/video-data)；S：[detail.py](https://github.com/JoeanAmier/TikTokDownloader/blob/473c90ff70c663cfb69310fff2b8d5192f200661/src/interface/detail.py) |
| 图文/视频区分 | 官方有 media_type，网页作品形态不止视频 | M1b 先普通视频，图集/直播/特殊付费形态明确 unsupported，不对所有 aweme 套同一视频模型 | O+S，类型值/资源完整性需逐样本核对 |
| 媒体候选 | 已查官方 video.list/data 主要提供元数据/封面/统计，未证明完整下载计划；社区详情可能有播放/下载地址和规格 | 下一轮必须核对视频/音频关系、水印、码率/编码、原始与派生、完整长度、鉴权和可下载范围；未知不能默认“原画/无水印” | O 未提供充分证据；S 详情结构，媒体路径 T not-run |
| 登录与权限 | 应用 scope、用户 OAuth token、网页会话是不同能力；官方交换 token 涉及 client_key/clientSecret | 桌面不内置全局 clientSecret。未有服务端授权方案时不能偷偷增加云服务；普通观看者登录不能替代作者授权 | O：[WEB 授权](https://open.douyin.com/platform/resource/docs/develop/permission/web/permission)、[权限](https://open.douyin.com/platform/resource/docs/accession-guide/type-and-permission) |
| 更新通知 | create_video 事件描述为用户通过开发者应用分享视频到抖音，带 share_id | 不证明能收到任意创作者的全部新投稿；轮询/复查仍需单独设计。M1b 不依赖 webhook 部署 | O：[事件目录](https://open.douyin.com/platform/resource/docs/develop/webhooks/event-list) |
| 质量与完整性 | 现有资料不能保证每个账号所有视频取得相同规格/资源 | candidate/selected/actual 分开，任何能力下降显式告警；无声、部分音轨、文件截断纳入固定用例 | T not-run；不从下载器功能表推导 |
| URL 期限与恢复 | 游标/作品 ID 与短期媒体 URL 不同 | 持久化 ID 与进度，重启重解析；ETag/长度/版本不足则重下；签名 URL/Cookie 永不写普通快照 | 基于 INV-10/11 的实现要求，站点实际期限未知 |
| 失败与删除 | 权限、私密、风控、网络、列表缺失可能无法可靠区分 | waiting_auth/waiting_user/rate_limited/retryable/unavailable/unknown 分开；HTML 挑战页和空 JSON 非成功；不因缺失删原件 | O 视频数据及 R 线索；权威删除信号未知 |
| 历史边界与预算 | 服务端时间筛选缺失、会话/权限限制可能改变可见集合 | 记录请求预算、最早观察、页数、终止原因和“本次可见范围”；全历史和所有旧编辑不得承诺 | O+S；跨日期覆盖需 T |

## 3. 本轮保留给 M1b 的实施入口

M0/M1a B 可以建立纯离线的抖音形态合成夹具：账号别名映射未知、aweme 资源列表、cursor/has_more 组合、隐私时间零、视频/图集区分、统计变化、挑战响应。它用于验证核心不会硬编码 Bilibili 的 BV/cid/页码，不需要安装抖音下载器，不向真实网站发请求。

M1b 由 A 在本表基础刷新接口/维护证据，给出“作者 OAuth 管理路径”或“用户授权观看范围内网页适配路径”的具体选择与限制，再一次下达完整实施批次。需要服务端、应用申请或新增许可影响的动作先汇入总表；不把这些缺口让 B 用隐藏浏览器、硬编码密钥或泛用下载器掩盖。

现场验证必须包含两个作者、有界历史、新增与编辑、部分失败、登录失效/恢复、至少两种实际媒体样本、过期地址和离线恢复；按安全输入入口提供账号条件，不能把 Cookie 发进聊天或仓库。缺任一项时按能力保留 not-run，不把“下载成功一个视频”升级为站点验收。
