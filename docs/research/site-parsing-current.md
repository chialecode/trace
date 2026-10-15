# 两站解析资料与实施边界

核对：资料为2026-10-07，补证及实现回写核对于2026-10-08，多站部分另见[社交站点研究](social-sites-current.md)。本文记录资料证据和两站实现边界，**不是站点运行验收**。§1～5保留规划时的资料、候选与未知，§6更新长图文完整响应、特殊资源及协议缺口；当前生产能力以两站能力表和[状态](../delivery/status.md)为准。规划时已查本地站点解析、直播协议和分页实现，再用互联网查找补充资料，当时没有执行真实账号登录、作品下载或录制。字段语义按[归档契约](../site-archive-contracts.md)。

上述“未执行”是规划与补证时的范围。2026-10-08 获得用户具体授权后进行了最小真实登录和给定目标检查，见§7及[审查报告](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md#a-m1-review)；旧资料/自检不因此改写为当时已实测。

## 1. 证据口径

2026-10-08 的集中审查结论与 F-018/019 见[固定报告](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md#a-m1-review)。认证历史、补全确认及本地凭证方案写入[归档契约](../site-archive-contracts.md#authentication-acquisition)；它们不改变本页历史资料的当时结论，也不构成真实站点验证。

官方开放接口说明、社区协议文档、实现观察、媒体报道、TRACE 合成测试、TRACE 真实请求是不同等级。请求路径存在、第三方声称支持、单次取流和完整归档闭环分别判定。网页登录范围不等于作者 OAuth 管理范围；不得把 TikTok 文档套到抖音。参考实现的日志、无界读取、身份池和外部签名服务不继承到 TRACE。

## 2. Bilibili 路径与缺口

| 内容 | 当前资料、请求与关键字段 | 必须处理的差异 |
| --- | --- | --- |
| 空间动态 | `api.bilibili.com/x/polymer/web-dynamic/v1/feed/space`，host_mid、offset；`/x/polymer/web-dynamic/v1/detail`，id；社区[空间](https://github.com/pskdje/bilibili-API-collect/blob/main/docs/dynamic/space.md)、[详情](https://github.com/pskdje/bilibili-API-collect/blob/main/docs/dynamic/detail.md) | 分页持久化；文本、图集、投稿卡、转发和未知卡类型；原作者与转发者分别保留；空页/重复游标不冒充完整 |
| 图文 opus | `/x/polymer/web-dynamic/v1/opus/feed/space`，host_mid/page/offset/type；`/x/polymer/web-dynamic/v1/opus/detail`，id；社区[空间图文](https://github.com/pskdje/bilibili-API-collect/blob/main/docs/opus/space.md)、[图文详情](https://github.com/pskdje/bilibili-API-collect/blob/main/docs/opus/detail.md) | 空间图文明确不包含转发，不能代替动态源；opus/dynamic/article 身份映射要有响应证据，不由数字相等推断 |
| 专栏/长文 | `/x/article/view`，id；社区[专栏正文](https://github.com/pskdje/bilibili-API-collect/blob/main/docs/article/view.md)与[文集](https://github.com/pskdje/bilibili-API-collect/blob/main/docs/article/articles.md) | 旧 cv、新 opus、正文块、嵌图/卡片和合集分别建模；只读摘要不能确认全文完整；旧接口失效时保留可用原文及缺口 |
| 视频 | 沿用[已有能力表](bilibili-capabilities.md)：空间列表、view、WBI playurl，BV/av/cid/分 P、DASH/必要音频、durl 独立段 | 候选质量与实际规格分开；多音轨、字幕/章节/弹幕可用时保存；付费/DRM/登录不足返回明确限制，不凭较高清单承诺能取到 |
| 直播 | room_init/get_info、`api.live.bilibili.com/xlive/web-room/v2/index/getRoomPlayInfo`；room_id、protocol、format、codec、qn；社区[房间/取流](https://github.com/pskdje/bilibili-API-collect/blob/main/docs/live/info.md)、[直播流](https://github.com/pskdje/bilibili-API-collect/blob/main/docs/live/live_stream.md) | 短房号归一到真实 room_id；protocol=0/1，format=flv/ts/fmp4，codec=AVC/HEVC 等值按实际响应解析；current_qn 与 accept_qn 不同；host+base_url+extra 只生成短期访问计划，不能持久化签名 URL |

WBI、Cookie、状态码和域名白名单沿现有站点适配器扩展。FLV 与 HLS 都需完整录制语义，不能仅保存 m3u8 文本。源声明“无需 Cookie”只适用资料中的场景，不能推广到所有质量和账户。

## 3. 抖音路径与缺口

| 内容 | 当前查得路径/结构 | 必须处理的差异 |
| --- | --- | --- |
| 账号/主页/短链 | `www.douyin.com/aweme/v1/web/user/profile/other/`，sec_user_id；分享链接逐跳展开 | sec_uid、uid、unique_id/short_id、web_rid、room.id_str 分命名空间；昵称不作主键；短链跳转重新做网络边界检查 |
| 投稿/空间内容 | `/aweme/v1/web/aweme/post/`，sec_user_id/max_cursor/count；aweme_list/has_more/max_cursor | 保留置顶、乱序、重复、空页与权限条件；账号投稿作为空间订阅主体，关注流是不同可见范围，不可替代完整作者列表 |
| 单条视频/图集 | `/aweme/v1/web/aweme/detail/`，aweme_id；aweme_detail、aweme_type、desc、images、video、music | 按结构与类型共同判定；images 有序保留，混合动图/实况图不能只存封面；play_addr/download_addr/bit_rate/音轨是不同候选；地址名不能证明无水印、原始或完整 |
| 长图文/文章 | 2025-12-26 [科技日报](https://www.stdaily.com/web/gdxw/2025-12/26/content_453850.html)报道抖音长图文正式上线，支持正文、配图与背景音乐 | **必须覆盖**。报道证明产品类型存在，不证明具体下载 API、字段或完整正文。A 尚未取得可核对的完整文章响应；B 需沿详情及页面结构做有界协议验证并补合成 fixture，禁止只保存 desc 或以“抖音没有专栏”略过。独立付费专栏目录未证实，与长图文区分 |
| 合集/附属信息 | `/aweme/v1/web/mix/aweme/`；comment/list、comment/list/reply、danmaku/get_v2 等为独立端点 | 合集关系和当前可获得的元数据保存；评论/回复/弹幕按预算与覆盖保存，不能无限抓取。收藏夹是会话私有能力，不纳入任意作者的投稿全集 |
| 直播房间/取流 | `live.douyin.com/webcast/room/web/enter/`，web_rid；`webcast.amemv.com/webcast/room/reflow/info/`，sec_user_id；room/id_str/status/stream_url，flv_pull_url、hls_pull_url、live_core_sdk_data.pull_data.stream_data | Web 与移动响应的状态码/嵌套结构不混用；音频直播保留 audio-only 证据；quality/codec/URL 分层。ttwid 与登录 Cookie 不相等；A-Bogus/UA/会话绑定需要本地可替换实现与固定向量，不依赖公共签名代理 |

官方[视频列表](https://open.douyin.com/platform/resource/docs/openapi/video-management/douyin/search-video/account-video-list)、[视频数据](https://open.douyin.com/platform/resource/docs/openapi/video-management/douyin/search-video/video-data)、[授权](https://open.douyin.com/platform/resource/docs/develop/permission/web/permission)仅能支持其明示权限；[创建图文](https://partner.open-douyin.com/docs/resource/zh-CN/dop/develop/openapi/video-management/douyin/create-image-text/create-image-text)证明发布类型与权限，**不是下载授权，也不执行发布**。产品选择用户有权观看范围内的本地网页适配路径，不增加云端服务或内置 clientSecret。

异常检测必须结合端点：空详情、HTML 验证页、风控码、认证失效和合法空列表分别处理；不能将所有 HTTP 200、空字节或空数组解释为成功/删除。会话检测返回访客身份也不证明登录。任何响应携带的 Cookie/签名不得进入错误详情。

## 4. 通用能力选型查证

2026-10-07 读取 crates.io 包元数据、GitHub 仓库/近期提交/已关闭问题样例。保留现有 Rust、Vue、SQLite、受控网络与锁定 LGPL3 shared FFmpeg，不为新增站点替换基础栈。

| 候选 | 维护与许可事实 | A 建议及 B 的实测责任 |
| --- | --- | --- |
| [m3u8-rs](https://github.com/rutgersc/m3u8-rs) 6.0.1 | crates.io 更新 2026-07-30；MIT；未归档；提交 124d174 修正 EXT-X-MAP 输出次序；已关闭问题 #94 涉及 METHOD=NONE | 首选作为 HLS 语法解析层。不能据 issue 关闭推定已发版修复；B 验证 MAP、KEY NONE、BYTERANGE、DISCONTINUITY，必要时选兼容替代并记录依据 |
| [hls_m3u8](https://github.com/sile/hls_m3u8) 0.7.0 | crates.io 更新 2026-05-06；MIT OR Apache-2.0；未归档；近期仓库维护包含 #84 移除依赖、#87 严格检查 | HLS 备选。B 比较实际标签支持与未知标签保留；不同时留两套生产解析器 |
| [ammonia](https://github.com/rust-ammonia/ammonia) 4.2.1 | crates.io 更新 2026-10-03；MIT OR Apache-2.0；未归档；#256/#257 为 CSS URL 过滤相关修复，#258 发行 | HTML 净化首选候选，用在无法直接转安全块结构的文章入口。禁任意 style/script/远端自动加载，仍须业务级 URL/blob 白名单；结构化文章优先用类型化 Vue 渲染 |
| 已锁定 FFmpeg/ffprobe | 当前资产与许可依据见[媒体集成](2026-10-01-ffmpeg-integration.md) | 解复用、封装、探测复用现有工具；仅处理受控本地文件/管道，不允许 FFmpeg 自行联网绕过 core 的逐跳校验 |

上述为资料推荐，尚未安装或实测新增库。B 引入前核对最新稳定版与目标 Rust 兼容，锁实际采用版本，记录直接/传递许可与包资产。需要自研的是 TRACE 身份/引用/代次/覆盖与适配映射；不从零写通用 HTML/HLS 解析器。若候选不满足关键场景，保留独立最小反例和替代理由，而非静默省略该功能。

## 5. A规划时尚未证实的范围

真实两站会话、风控、质量、长文完整性、动态媒体与直播签名均 not-run。长图文与特殊卡片规则需要补足，属于开发协议验证工作；不能全转交用户。ACT-002 仅承接真实账号配置与有界请求授权。缺真实条件时继续合成、静态解析与安全实现，但相应真实支持声明保持未验证。API 变化导致当前必须类型无法实现时交付明确阻断及已尝试方案，不将 unsupported 当作整阶段通过。

以上保留2026-10-07 A规划时的未知项。B的协议补证、实际采用与合成反例在下一节；当前逐类型实现和未经真实验证的边界以[Bilibili能力表](bilibili-capabilities.md)、[Douyin能力表](douyin-capabilities.md)及[阶段报告](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md)为准，不把当时未知或后续合成通过改写成真实站点验证。

## 6. B 实施补证（2026-10-07）

本节更新前文 A 规划时的未知项，仍不包含真实账号请求。抖音长图文已找到可核对的具体响应规则：`aweme_type=163`，列表 `article_info.article_content.markdown` 可能在 499 字符截断，必须再取 `/aweme/v1/web/aweme/detail/`。`article_content` 可能为对象或 JSON 编码字符串；`article_info` 提供 article_id/title/type/has_more 等信息。不能把此类响应中的 `video.play_addr` 背景 MP3 误认作主体视频。

固定来源：[长文正文补全报告](https://github.com/njausxl/douyin-image-post-ocr/blob/504d0f6ddd09411d3fe819097f773e644b6ffb7c/docs/长文正文补全报告.md)、[TextWork 响应类型](https://github.com/ikenxuan/amagi/blob/fcc55ac2e9129cd141938f65496cfd18eb949da2/packages/response-types/src/generated/douyin/TextWork/TextWork_V0.ts)。TRACE 的合成用例经过生产适配器子进程，覆盖账号→分页→详情全文/30 图→资源计划与挑战响应；真实文章与媒体读取 not-run。具体通过跑次见[实施记录](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md#m1-implementation)。

本次 A-Bogus 模块采用明确 MIT 许可的[固定公开源码](https://github.com/chen-zeong/DTV/blob/9cd17311fa32575fa68f7e79feafdb165c13edd3/src-tauri/src/platforms/douyin/a_bogus.rs)，保留版权和完整许可于 `adapters/douyin/NOTICE`；仓库核对时仍活跃，最近维护至 2026-10-01。TRACE 将 SM3 换为 RustCrypto 并显式传入时间；独立编译上游实现，仅替换时钟后得到固定向量，当前实现与向量一致。不依赖外部签名服务；该向量不是登录或站点风控验收。

| 实际采用 | 许可 / 整合依据 | 验证与替换边界 |
| --- | --- | --- |
| pulldown-cmark 0.13.4、scraper 0.27.0 | MIT、ISC；解析 Markdown/HTML 为惰性类型块，未保留可执行 HTML | 通用解析复用开源库，资源身份与正文完整性由 TRACE 映射；未知段落仍须记录缺口 |
| sm3 0.5.0 | MIT OR Apache-2.0；RustCrypto 摘要实现 | 只替换签名摘要原语，固定上游向量保护输出 |
| flv-core 0.1.1 | MIT OR Apache-2.0；mediaway 仓库核对时最近维护 2026-09-30 | TRACE 加有界缓冲、头/尾长度校验和录制生命周期；不自研通用解复用器 |
| m3u8-rs 6.0.1 | MIT；沿用 A 候选并做最小反例 | 已验证该版 METHOD=NONE 无 IV 拒绝、key/MAP 不自动跨片继承；窄包装保留 NONE 的取消加密语义并继承状态，见 OSS-010 |
| aes 0.9.3、cbc 0.2.1 | MIT OR Apache-2.0；RustCrypto 普通 AES-128 解密 | 只处理身份 keyformat、正确 IV/轮换，DRM/未知加密拒绝。0.9.0 因 yanked 失败，未强行采用 |
| infer 0.19.0 | MIT；与既有依赖图同版直接依赖 | 图片/音频字节类别检查，失败隔离；不把 URL 后缀当实测格式 |

本节最初补证时，端点/类型矩阵、特殊卡与附属资源及响应变化反例仍未齐全。后续已补文集/合集、音乐卡、JSON ops专栏、实况图、HLS字幕和有限弹幕采样，当前矩阵与定向证据见两站能力表和阶段报告。逐项资料与合成验证不等于真实网站权限、风控或内容完整性的现场通过。

### 6.1 内嵌资源与字幕的固定协议依据

2026-10-07核对Bilibili社区资料固定提交 `271b123a083698bf576101c21f534b3418768a43` 的[opus结构](https://github.com/pskdje/bilibili-API-collect/blob/271b123a083698bf576101c21f534b3418768a43/docs/opus/features.md)、[详情](https://github.com/pskdje/bilibili-API-collect/blob/271b123a083698bf576101c21f534b3418768a43/docs/opus/detail.md)、[文集](https://github.com/pskdje/bilibili-API-collect/blob/271b123a083698bf576101c21f534b3418768a43/docs/article/articles.md)及[播放器](https://github.com/pskdje/bilibili-API-collect/blob/271b123a083698bf576101c21f534b3418768a43/docs/video/player.md)。部分opus结构由网页前端推断，证据级别为社区协议，不能当作TRACE真实响应实测。

TRACE按para_type=6的UGC/OPUS卡建立引用；卡片位置和父正文保持，引用失败独立记为部分覆盖。专栏返回list.id时补取文集成员；每P通过`/x/player/wbi/v2`读取字幕/章节，字幕id使用字符串、cid分组，临时地址只用于下载计划。字幕资源保留原JSON并验证body/from/to/content，离线按用户操作读取文本；未声称所有字幕语言或版权能力真实可用。

Douyin图集补证为[固定ImageAlbumWork类型](https://github.com/ikenxuan/amagi/blob/fcc55ac2e9129cd141938f65496cfd18eb949da2/packages/response-types/src/generated/douyin/ImageAlbumWork/ImageAlbumWork_V0.ts)：images元素可包含video/live_photo_type/download_url_list/url_list。TRACE同时处理嵌套image_post_info、original_image/display_image及实况资源；缺静态图仍保留required身份与可得实况片，完整性保持不足。合成验证不能外推至所有客户端的实况格式。


### 6.2 合集和直播有限历史采样

2026-10-07公开补证：[合集条目/直播初始化端点](https://github.com/Johnserf-Seed/f2/blob/7dab3e2ffffaa2535834d28fca99dbc2e89fa9d3/f2/apps/douyin/api.py)、[请求模型](https://github.com/Johnserf-Seed/f2/blob/7dab3e2ffffaa2535834d28fca99dbc2e89fa9d3/f2/apps/douyin/model.py)、[IM JSON响应字段](https://github.com/Johnserf-Seed/f2/blob/7dab3e2ffffaa2535834d28fca99dbc2e89fa9d3/f2/apps/douyin/filter.py)；这些是社区协议资料，未引入其实现。另一份独立社区实现中的series.list请求与2026-09-16第三方观察补足F2常量表未能证明的列表差异：mix.list的mix_infos/mix_list与series.list的series_infos分别续页，后者带read_new_mix=true、req_from=channel_pc_web；series_id作为mix.aweme的mix_id取得公开成员。两种集合不能假定互为超集，A-Bogus不证明页面安全门禁已经满足。TRACE有限游标枚举与生产进程合成验证已实现，真实权限/安全门禁依ACT-002，不能把第三方观察冒充TRACE实测。

B站[最近历史弹幕协议](https://github.com/SocialSisterYi/bilibili-API-collect/blob/271b123a083698bf576101c21f534b3418768a43/docs/live/danmaku.md)：GET xlive/web-room/v1/dM/gethistory，roomid；data.room和data.admin各是有限最近消息，非历史全集。抖音GET live.douyin.com/webcast/im/fetch，resp_content_type=json、room_id、user_unique_id、有限need_persist_msg_count，data数组的common.method/msg_id/room_id与content/user.nickname；extra.cursor与internal_ext不进入档案。访客ID仅用于协议请求，不充当登录证明。TRACE目前只保存WebcastChatMessage，其他事件计入省略数；源返回空/错误/串房不算成功。字幕若已嵌入画面会随原始录制保存，外部轨由下节Core处理。两站采样均由独立任务/用户选择控制，界面明确sampled_history，不能宣称无缺口弹幕或全量WebSocket录制。

### 6.3 HLS字幕与音乐卡

[RFC8216 §3.5](https://www.rfc-editor.org/rfc/rfc8216#section-3.5)定义WebVTT片、时间映射与初始化区；[§4.3.4.1](https://www.rfc-editor.org/rfc/rfc8216#section-4.3.4.1)定义SUBTITLES组。Core选取master中的对应轨，受控抓取清单/MAP/key/片，保留原始头和片及派生VTT，FFprobe只见本地文件；缺轨/坏片/有界采样分别记录。它证明协议实现与合成场景，未证明两站所有直播间提供外部字幕。

Bilibili固定社区[动态音乐卡](https://github.com/pskdje/bilibili-API-collect/blob/271b123a083698bf576101c21f534b3418768a43/docs/dynamic/all.md)、[opus卡](https://github.com/pskdje/bilibili-API-collect/blob/271b123a083698bf576101c21f534b3418768a43/docs/opus/features.md)、[音频资料](https://github.com/pskdje/bilibili-API-collect/blob/271b123a083698bf576101c21f534b3418768a43/docs/audio/info.md)、[音频流](https://github.com/pskdje/bilibili-API-collect/blob/271b123a083698bf576101c21f534b3418768a43/docs/audio/musicstream_url.md)：major.music或LINK_CARD_TYPE_MUSIC中的id为auid；song/info的id与web/url的sid必须一致。web流type=1为192K，type=-1仅试听，size可能为0，不允许据成功HTTP归档成完整歌曲。TRACE保存必要音频/封面和安全歌曲资料，试听返回权限限制并保留父正文。未请求付费/APP接口或绕过权限。

## 7. A 的有限真实登录与内容请求观察（2026-10-08）

用户主动在本机提供Cookie并授权有效性检查。本次 Bilibili `nav` 返回已认证；Douyin 原 `query/user` 仅返回unknown，不能证明登录，因为访客也能得到客户端身份。A改用固定 `GET /aweme/v1/web/user/profile/self/`，在成功响应中核对 `user.uid` 非零和 `user.sec_uid` 非空，不传待查用户ID，结果只保留安全状态。生产进程合成用例覆盖有效自己资料、访客根ID、伪is_login、零ID/缺字段及认证拒绝；同一Cookie的有限真实请求返回valid。此为F-020修复的观测依据，不承诺该社区接口长期稳定，原包仍为修复前代码。

用户要求本轮确认后，A于2026-10-08 14:48 UTC使用同一套生产debug程序完成两站同接口对照：每站带Cookie与显式不带Cookie各1请求。Bilibili `GET /x/web-interface/nav` 带Cookie为authenticated、无Cookie为auth_required；Douyin `GET /aweme/v1/web/user/profile/self/` 带Cookie为valid/login_proven=true、无Cookie为auth_required。四次HTTP均200，认证拒绝的两次CLI退出1按原样保留；它们是预期负对照，不改写成登录成功。由此确认本次提供的两份Cookie有效，不能把HTTP200、访客ID或Cookie键存在单独作为登录依据。

实现用途也已核对：当前用户端点确认会话本人身份，列表/详情/播放地址解析另行验证访问能力；签名和客户端上下文不能代替登录。TRACE的`pluginhost::handle_proxy_fetch`只在with_credential且相应作用域有凭证时交给Core HTTP层，`transport::build_headers`限制凭证目标域，跨域重定向去掉Cookie/Authorization；适配器不取得Cookie正文。媒体传输按域规则不携带Cookie时仍可源自已认证解析，新获取历史按[契约§7](../site-archive-contracts.md#authentication-acquisition)记录来源上下文，不以CDN头推断匿名。

之后用户提供直播间与主页样本用于登录条件比较。固定房间身份的无Cookie诊断遇HTTP412，Douyin已登录生产枚举遇HTTP403，各一请求后停止。没有得到视频列表或实际直播媒体，不能证明匿名720P限制、登录1080P原画或全部投稿范围；这些是待核查的条件。登录证明、端点可访问、源声明质量、实际规格和历史完整性是五个独立结论。具体原始目标/响应只在本地隔离目录，当前授权与用户动作只在ACT-002维护。


## 8. 作用域与访问分类核查（2026-10-09）

B本轮未新增真实站点请求，也未重复A已经完成的Cookie有效性对照。按已有协议资料与当前代码只读核对：Bilibili生产房间解析经Core代理并使用站点UA/Referer，原标准库无Cookie的room_init诊断HTTP412不等于生产直播流已经测试；Douyin的aweme/post使用sec_user_id、max_cursor及分页参数，协议入口添加当前签名、UA/Referer并由Core按凭证作用域附头。既有签名向量通过不能证明目标端点当前接受这些条件，亦无证据证明刷新Cookie或盲目重试能解除原403/412。

Core元数据HTTP403/412现在分类为PermissionDenied，停止该访问路径并保留受限原因；不产生成功空页、不把有效登录直接改为过期，也不启动普通网络退避来反复撞同一限制。媒体CDN的原403/地址刷新语义继续保留，不能将所有403统一改成同一种失败。离线反例在 `metadata_403_412_stop_as_access_restrictions_without_empty_success_or_cookie_expiry`，当前自动证据见报告。OSS-011仍保留实际内容访问限制，新的真实复验要有具体原因，并遵循ACT-002的现有目标、预算和停止条件。

两站新获取使用本批次同凭证代次的认证证据；已认证解析后不向CDN发送Cookie仍保留该获取身份。普通资源补全候选按实际来源和实测规格比较，在统一页面等待用户决定；直播只保留逐片身份与实际规格，不补录历史。两站匿名/已认证、重叠订阅、CDN零Cookie和原资源保留已接生产适配器加隔离合成传输链，未冒充真实账号的720P/1080P原画或全部投稿对照。


## 9. A真实来源协议补证（2026-10-09）

本次实际目标由用户提供，来源诊断与当前生产链严格分开；全部命令、失败/字节/解码事实见[固定报告](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md#m1-real-source-readiness)，当前实现要求见[链接契约](../site-archive-contracts.md#direct-source-acquisition)。原§7/8的not-run和受限结果保留历史，不改写为当时通过。

Bilibili WBI是img/sub密钥置换后的前32字符，排序/编码后的query只含一个wts；正确摘要和实际URL回归见F-026。旧room_init匿名诊断在相同环境代理下，默认标准库头412，浏览器UA与直播Referer组合200。当前现代`/xlive/web-room/v1/index/getInfoByRoom`及`/xlive/web-room/v2/index/getRoomPlayInfo`在正确WBI/直播头条件下取得实际流；不能仅凭端点常量断言生产取流已经成功。

Douyin的self登录端点与受保护内容端点不同。其网页加载的`@byted/secsdk-strategy`项目34 SDK还为detail/post等固定路径附加访问上下文与`x-secsdk-web-signature`；本次只读参考的来源说明指向站点安全SDK v1.0.40，协议仍须固定向量和当前响应核对，不是官方稳定API承诺。访问者ID从当前Cookie的uifid/uifid_temp/uifidtemp/UIFID/UIFID_TEMP/UIFIDTEMP中按顺序取非空值，verifyFp/fp来自s_v_web_id；签名绑定该ID、秒时间戳、SDK公开算法常量和实际序列化查询（含业务参数及A-Bogus）。编码、顺序及重复参数必须独立验证，不能把别的站点或SDK项目的常量混用。它不是登录证明，也不能用虚构token代替缺输入。

真实同Cookie对照已把detail/post从403/uifid_missing推进到200，当前Core还未接入该签名边界。生产实现应在Core内部绑定凭证代次、host/method/path并隐藏新增敏感查询，适配器仍不读取Cookie。正常post响应超过现有体/帧预算，必须先解决F-029后才谈完整列表；one page/has_more不证明历史覆盖。

Douyin网页直播的展示房号用于`GET https://live.douyin.com/webcast/room/web/enter/`的web_rid，不等于sec_uid或真实room.id；本次该路径取得直播信息。媒体来源可能是HTTP的douyinliving.com，已验证同一地址HTTPS；Bilibili源CDN实际重定向到bytefcdnrd.com。两个媒体域不携带Cookie，必须在窄策略下按每跳公共DNS/IP/TLS核对；动态重定向地址不能固定为第一次出现的某个边缘主机。尚未完成当前生产Core的真实链，依据P14执行，不引入任意域/明文回退。

## 10. 生产接线与对照（2026-10-10）

上节保留A诊断时状态。B已在Core内接入受保护Douyin详情/投稿的SecSDK派生上下文，并保留self、跨域剥离与媒体零Cookie；大型JSON通过显式有界插件消息，生产20条页回放及超限/取消/重开反例均使用当前代码。直播CDN只扩展已证明的域族与同地址HTTPS规范化，协议上限及字段正本在[契约§9](../site-archive-contracts.md#direct-source-acquisition)。

两站视频/直播URL现经生产适配器解析为稳定作品或实际房间，并由持久Core一次性任务处理明确选择。Bilibili公钥nav沿用所选身份，匿名nav的公开键与登录证明分开；原32字符WBI向量不变。DASH的数字质量ID在预览/执行两端使用相同规范化，分离视频/必要音轨完整Core链保留先失败后通过。Douyin视频CDN补入公开UA/Referer后成功接收，live enter的status=4即使缺owner也显示停播，不能把观看者当主播。

这些是接线与具体反例事实；完整真实产品链是否完成，以[固定报告](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md#b-m1-r3-delivery)中匹配包、任务、全部必要对象、导出和完整解码的回执判断。原来源诊断文件没有填入对象库，失败媒体/原包/旧采样继续保留；当前对象或授权依赖只维护于ACT-002，不扩大到作者全历史。


## 11. A当前生产包真实链核对（2026-10-13）

四个用户指定对象已用当时的生产包完成质量选择、任务/认证上下文、全部必要对象、两直播GUI关闭继续与封片、离线重开/范围读取/导出和完整自动解码。实测规格、累计预算、原失败和未知范围只见[固定报告](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md#a-m1-r3-technical-closeout)及两站能力表。原§7～10的当时结论不改写；本次不证明全部类型、全部历史、匿名/会员差异或原生声音体验。
