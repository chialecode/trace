# 抖音网页协议资料

核对：2026-11-12 调研定稿；2026-11-13 按 [ACT-010](../../USER-ACTIONS.md#act-010) 执行 1 次有界真实站点检查并回写结论（脱敏回执 `artifacts/site-check/act010.json`，账本 `apiCalls=15/18`）。内容整理自公开网页技术资料、本机参考索引只读源码观察到的协议现象与 ACT-010 实测记录，端点不是站点对 TRACE 的稳定承诺；站点改版后要重新核对。规则见[站点规则](../dev-rules/sources-and-safety.md#site-rules)与[凭证与真实站点请求](../dev-rules/sources-and-safety.md#credentials)。

证据等级说明（全篇逐节标注查询或实测日期）：

- `实测（ACT-010，2026-11-13）`：使用 TRACE 正式代码在 [ACT-010](../../USER-ACTIONS.md#act-010) 授权额度内对抖音线上接口实测验证的结论。
- `官方文档`：标准规范或浏览器/平台公开文档。
- `源码阅读（功能领域，2026-11-12）`：本机参考索引对应功能领域实现与脱敏回放夹具中直接核实的请求构造、响应结构或异常处理现象（严格只读事实，未复制任何代码）。
- `社区资料（2026-11-12）`：公开技术社区与协议整理资料的观察记录。
- `推测（2026-11-12）`：基于现有结构做的工程推断；未实测锁定的细节记为 `unknown` 或 `not-run`。

本文所有示例 ID、Cookie 片段与地址均为**明显合成示例**（如作品 `7300000000000000001`、用户 `MS4wLjABAAAA_SYNTHETIC_EXAMPLE`、网页直播间号 `100000000001`、场次房间号 `7400000000000000001`），不包含任何真实账号或真实签名链接。

<a id="status"></a>
## 1. 验证状态

截至 2026-11-13，已完成 **ACT-010 有界真实站点检查**（1 次运行）：

- **真实站点请求（ACT-010，2026-11-13）**：共发出 `15/18` 次接口/播放列表请求（`profile/self` 2、`profile/other` 2、`aweme/post` 3、`aweme/detail` 4、`web/enter` 2、`reflow/info` 1、直播 `.m3u8` 播放列表 1）与 `6/16` 次点播及图片 CDN 请求（合计 `10,602,566` 字节），直播拉流 `0` 次；**全程 0 次 HTTP 403/429/444、0 次网关拦截、0 次验证码或滑块挑战、带 Cookie 请求 0 次未登录退化**（`stoppedBy=""`）。
  - **已实测通过（V-01～V-05、V-08、V-06 在线路径）**：登录与访客对照、纯 Go `a_bogus`（`dhzx` 盐值）+ `x-secsdk-web-signature`（`uifid`）+ 剥离 `bd_ticket_guard_*` 后的粘贴 Cookie（即使初始不含 `msToken`）、含置顶作品主页的第 1→2 页 `max_cursor` 翻页、普通图文与图片/视频/配乐混排图文的解析及无 Cookie CDN 拉取、点播 MP4 `Range` 分段拉取与封装校验、同一直播间数字 `short_id` 与可变房间号两种形态的 `web/enter` 解析及在线 `reflow/info` 换号全部一次通过。
  - **ACT-011（2026-11-13，A 运行）**：离线占位 `room_id=2` 的 `reflow/info` 返回 `status_code=10011`，拿不到 `web_rid`（V-06 已回答：离线且没缓存 `web_rid` 的账号只能等开播后由 `room_id` 换取）；直播间在播，原画同时有 FLV 与 HLS，但 FLV 第一跳是 CDN `302`，检查程序误把它当验证页停下，FLV 流头与短录仍为 `not-run`，转 `ACT-012`。
  - **ACT-012（2026-11-13，A 运行）**：两次 FLV 连接的第一跳都是 `302`，目标为 HTTPS、443 端口、**另一个域名**（不是 IP，不在直播 CDN 白名单 `douyincdn.com`/`bytefcdnrd.com`/`amemv.com`/`douyinvod.com`/`zjcdn.com` 内；回执只记形状，不记地址）。拉流 CDN 会把连接调度到各厂商域名，固定后缀名单补不全，因此拉流跳转改为：首个地址查白名单，之后的跳转只要求 HTTPS、443、公共域名（见[直播设计](../design/live.md)）。FLV 流头与短录仍为 `not-run`，转 `ACT-014`。
  - **ACT-014（2026-11-13，A 运行）**：按新跳转规则跟随 `302` 后第二跳 `200`，选中“原画”，FLV 视频标签是 AVC（`CodecID=7`，不是 `12`/`hvc1`），`1280×720`，音视频 `avc1`+`mp4a`；录了 `21.56` 秒、`15` 个分片、`1` 个分段，收尾为 `735,389` 字节的 MP4，`moov` 在 `mdat` 之前，状态 `completed`。V-07 对 AVC 已回答；这个直播间没有 HEVC 流，`CodecID=12`/`hvc1` 仍只由合成样本覆盖。
  - **B 交付时为 `not-run` 的子项（之后由 `ACT-011` 补测）**：因首轮客户端本地两处判断偏差（`ReflowRoomInfo` 的房间号正则要求至少 4 位导致占位 `room_id=2` 在本地被拒未发包；`EnterLiveRoom` 误将外层 `data.enter_mode == 1` / 无条件 `live_room_mode == 2` 当作纯语音直播，在进入 FLV 选流前返回 `语音直播` 错误），**V-06 离线 `room_id=2` 探测**与 **V-07 直播 HTTP-FLV 流头及短录转封装**未发出对应线上请求，保持 `not-run`（两处代码已修复并准备独立小额检查 `-act act011`）。
- **产品范围前置决定**：按[有效决定](../decisions.md)（DEC-043、DEC-018、DEC-024、DEC-026、DEC-040），TRACE 对抖音采用**粘贴 Cookie 登录**、**每站点一个登录账号**、**先登录再发任何站点请求**（访客模式不做产品功能，仅在本文中用于对照确认“确实已登录”与识别未登录退化响应）、视频/图文缓存与订阅检查、直播 HTTP-FLV 录制（不录弹幕）；关注列表导入与登录自动续期不在 M3 范围。

<a id="headers"></a>
<a id="signing"></a>
## 2. 公共请求头、访问上下文与签名

来源：`源码阅读（本机参考索引中的抖音签名与访问上下文实现、单个视频链接解析实现、用户视频列表解析实现、抖音直播录制实现，2026-11-12）`、`社区资料（2026-11-12）`。

### 2.1 公共请求头与浏览器指纹强绑定

抖音 Web 接口将 HTTP 请求头、URL 公共查询参数与 `a_bogus` 签名体内部编码的浏览器环境做交叉一致性校验；三者任一处不一致（例如请求头用 Chrome 144 Windows UA，而查询参数写 Mac 或 `a_bogus` 内部用另一套 UA 编码）会提高触发空包或验证码拦截的概率（`源码阅读（本机参考索引中的抖音签名与访问上下文实现，2026-11-12）`）。

| 请求头 | 主站接口（`www.douyin.com`） | 直播接口（`live.douyin.com` / `webcast.amemv.com`） | 媒体与直播流 CDN |
| --- | --- | --- | --- |
| `User-Agent` | 固定桌面 Chrome UA，例如 `Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36`（与签名和查询参数使用同一常量） | 同左 | 同左 |
| `Referer` | `https://www.douyin.com/`（作品/用户页可带对应页面路径，根路径通用） | `https://live.douyin.com/`（或 `https://live.douyin.com/<web_rid>`） | 点播用 `https://www.douyin.com/`；直播流用 `https://live.douyin.com/` |
| `Origin` | GET 接口通常省略或 `https://www.douyin.com` | `https://live.douyin.com` | 直播拉流带 `https://live.douyin.com`；点播可省略 |
| `Accept` | `application/json, text/plain, */*` | `application/json, text/plain, */*` | `*/*` |
| `Accept-Language` | `zh-CN,zh;q=0.9,en;q=0.8` | 同左 | 可省略 |
| `sec-ch-ua` / `sec-ch-ua-platform` / `sec-ch-ua-mobile` | 与 `User-Agent` 主版本号和 `"Windows"`、`?0` 严格一致 | 同左 | 不必携带 |
| `Cookie` | **必带**用户登录 Cookie（DEC-043） | `live.douyin.com` **必带**（含 `ttwid` 与登录态） | **不带登录 Cookie**（避免向边缘/第三方 CDN 泄露凭证，见[第 7 节](#media-cdn)） |

主站 `/aweme/v1/web/*` 公共查询参数基线（`源码阅读（本机参考索引中的抖音签名与访问上下文实现、用户视频列表解析实现，2026-11-12）`）：

| 参数 | 典型取值 | 绑定与作用说明 |
| --- | --- | --- |
| `device_platform` | `webapp` | 固定 Web 端标识 |
| `aid` | `6383` | 抖音 PC Web 应用 ID；**图文作品必须用 `6383`**（若误传旧移动/分享端 `1128`，图文详情会被服务端以 `filter_reason="images_base"` 过滤为空） |
| `channel` | `channel_pc_web` | 固定渠道名 |
| `pc_client_type` | `1` | Windows PC Web 固定为 `1` |
| `version_code` / `version_name` | `190600` / `19.6.0`（或 `170400` / `17.4.0`） | Web 前端版本号，同一会话内保持一致 |
| `cookie_enabled` | `true` | 浏览器环境声明 |
| `screen_width` / `screen_height` | `1920` / `1080` | 必须与 `a_bogus` 内部编码的屏幕尺寸字符串（如 `1920\|1080`）一致 |
| `browser_language` | `zh-CN` | 浏览器语言 |
| `browser_platform` | `Win32` | 与 Windows UA 对应 |
| `browser_name` / `browser_version` | `Chrome` / `136.0.0.0`（随固定 UA 提取） | **强绑定**：必须从实际发送的 `User-Agent` 解析，不能硬编码成另一个版本 |
| `browser_online` | `true` | 浏览器在线状态 |
| `engine_name` / `engine_version` | `Blink` / 同 `browser_version` | 渲染引擎版本，与 Chrome 版本一致 |
| `os_name` / `os_version` | `Windows` / `10` | 与 UA 中 `Windows NT 10.0` 对应 |
| `cpu_core_num` / `device_memory` | `8` / `8` | 硬件画像常量 |
| `platform` | `PC` | 终端平台类型 |
| `downlink` / `effective_type` / `round_trip_time` | `10` / `4g` / `50` | Network Information 画像常量 |
| `webid` | 19 位数字字符串 | 页面访问标识（见 §2.2）；部分接口缺失时仍可放行，带入更稳妥 |
| `uifid` | 来自 Cookie `uifid` / `UIFID_TEMP` / `UIFID` | 触发网关二次签名时在 `a_bogus` 追加后按序补入查询串（若此前已在查询串中则保持原位、绝不重复追加），并与请求头 `uifid` 同步携带（见 §2.2） |
| `msToken` | 120～128 字符令牌 | 优先取当前 Cookie 中的 `msToken`，使查询串与 Cookie 一致 |

### 2.2 访问上下文令牌与签名参数对照

来源：`源码阅读（本机参考索引中的抖音签名与访问上下文实现、单个视频链接解析实现、用户视频列表解析实现、抖音直播录制实现，2026-11-12）`、`社区资料（2026-11-12）`。

| 参数 / Cookie / 头 | 载体位置 | 生成或下发方式 | 作用 | 哪些接口必需 / 可选 |
| --- | --- | --- | --- | --- |
| `a_bogus` | URL 查询参数（第 1 阶段末尾追加） | 前端 `bdms.js` 生成：对 URL 查询串、POST 体（GET 为空）、固定 `User-Agent` 做 SM3 摘要，连同毫秒时间戳、`aid`、页面 ID（Web 常为 `6241`）、屏幕尺寸特征、浏览器族噪声槽位拼装为字节数组，经自定义 RC4 混淆与变体 Base64（含 `-` 与 `/` 的自定义 64 字符表）编码后做 URL 百分号编码（长度常见约 108～196 字符） | 2024 年中至今抖音 PC Web 与直播 Web 核心请求防篡改/环境摘要签名 | `aweme/detail`、`user/profile/other`、`user/profile/self`、`aweme/post`、`live.douyin.com/webcast/room/web/enter/` **必需**；`webcast.amemv.com/webcast/room/reflow/info/`、`check_alive` 实测实现中**不要求** |
| `X-Bogus` | URL 查询参数（旧版） | 2022–2024 年前端 `webmssdk` 使用的 28 位 MD5 + RC4 变体 Base64（常以 `DFSzsw...` 开头） | 历史签名参数，已被 `a_bogus` 全面取代 | 抖音核心 `/aweme/v1/web/*` 接口已不再接受仅带 `X-Bogus` 的请求；仅个别历史播放重定向路径偶见兼容，TRACE **不采用** |
| `x-secsdk-web-signature` + `uifid` + `x-secsdk-web-expire` | 查询参数（`uifid`、`timestamp`、`x-secsdk-web-signature`）+ 请求头（`uifid`、`x-secsdk-web-signature`、`x-secsdk-web-expire`） | 前端 `secsdk`（`ArgusSecurityPlugin` 两阶段签名）：(1) 先对业务+公共查询串算好 `a_bogus` 并追加 `&a_bogus=<百分号编码>`；(2) 当 Cookie 含 `uifid`（`uifid` / `UIFID_TEMP` / `UIFID`）时，在查询参数对末尾依次追加 `verifyFp` 与 `fp`（仅当存在 `s_v_web_id` 时）、`uifid`（若查询串尚未包含则追加，已包含则保持原位不重复）、`timestamp=<秒级Unix整数>`，按 `URLSearchParams` 规则（`*-._` 不转义）重序列化查询串 `query`，计算 32 位小写十六进制 `sig = md5("{uifid}_{timestamp}_{salt}_{query}")`，在 **URL 查询串末尾**追加 `&x-secsdk-web-signature=<sig>`，并同步设置 **HTTP 请求头** `uifid: <uifid>`、`x-secsdk-web-signature: <sig>`、`x-secsdk-web-expire: <timestamp>`（**注意：`x-secsdk-web-expire` 只写入 HTTP 请求头，绝不写入 URL 查询串**） | 2026 年 9 月起在部分 `/aweme/v1/web/` GET 路径启用的网关层校验；若 Cookie 中有 `uifid` 却未正确配对或受保护路径缺 `uifid`，网关直接返回 HTTP 403 `Blocked by ArgusSecurityPlugin ...` | `aweme/detail`、`aweme/post`、合集/音乐/收藏列表在最新签名实现中列为受保护路径；`user/profile/other`、`user/profile/self` 未列入该 14 条路径表（真实账户粘贴 Cookie 场景下是否强制拦截 `unknown`，列为 [ACT-010](#act-010) 必测项） |
| `msToken` | Cookie + URL 查询参数 | 字节 `mssdk` 接口返回响应头 `x-ms-token` / `Set-Cookie: msToken=...`（真实下发长度常见 **120 或 128 字符**，字母数字与 `_-=`）；无服务端下发时客户端常回退生成 126 位随机字母数字加 `==`（共 128 字符） | 会话级行为令牌，参与 `a_bogus` 签名前的查询串拼装 | 主站与直播接口均建议携带；优先复用用户粘贴 Cookie 自带的 `msToken` 或响应头刷新的新值，保证 Cookie 与 URL 查询串同名值一致 |
| `ttwid` | Cookie（`.douyin.com` / `.bytedance.com`） | 访问页面 `Set-Cookie` 下发，或 `POST https://ttwid.bytedance.com/ttwid/union/register/`（JSON 体 `{"region":"cn","aid":1768,"needFid":false,"service":"www.ixigua.com","migrate_info":{"ticket":"","source":"node"},"cbUrlProtocol":"https","union":true}`）由 `Set-Cookie` 返回 | 跨子域设备/访客跟踪标识 | `live.douyin.com/webcast/room/web/enter/` **必需**（缺失常返回空房间或风控）；浏览器粘贴的完整登录 Cookie 通常已含 `ttwid`，TRACE 不主动向 `bytedance.com` 匿名注册，缺 `ttwid` 时提示用户从已打开过抖音网页的浏览器完整复制 Cookie |
| `webid` | URL 查询参数 | 首屏 HTML `RENDER_DATA` 中的 `user_unique_id` 或 `mssdk` 下发的 19 位数字 ID（部分实现离线生成 `7` 开头的 19 位数字串缓存于会话） | 页面级匿名终端编号 | 主站 `/aweme/v1/web/*` 常规附带；同一会话周期内固定复用即可 |
| `verifyFp` / `s_v_web_id` | Cookie `s_v_web_id` + 查询参数 `verifyFp`、`fp` | 形如 `verify_<毫秒36进制>_<UUID变体>`，浏览器首次加载验证 SDK 或触发滑块后写入 Cookie | 指纹与验证码通行票据 | **可选但敏感**：用户真实 Cookie 若含有效 `s_v_web_id` 可原样保留；已过期的旧 `verify_...` 票据反而可能在部分路径诱发 403，客户端不应凭空伪造假验证码票据 |
| `__ac_nonce` + `__ac_signature` | Cookie | 未带完整会话首次直接 GET `www.douyin.com/...` 或 `live.douyin.com/...` HTML 文档时，服务端先返回极短挑战页并 `Set-Cookie: __ac_nonce=<21位字符>`，前端 `acrawler.js` 计算 `__ac_signature` 后二次请求才返回真实 HTML | HTML 文档首屏反爬挑战锁 | 直接调用 JSON API 且携带有效登录 Cookie 时**通常不触发**；仅在无 Cookie 抓 HTML 首屏（如短链落到需要读 HTML 的页面）时可能遇到 |

### 2.3 近一年（2025–2026）签名与风控演进观察

来源：`源码阅读（本机参考索引中的抖音签名与访问上下文实现、单个视频链接解析实现、用户视频列表解析实现，2026-11-12）`、`社区资料（2026-11-12）`。

1. **`X-Bogus` 退场与 `a_bogus` 稳定为主签名（2024 下半年–2026）**：主站 `/aweme/v1/web/*` 与直播 `webcast/room/web/enter/` 均统一要求 `a_bogus`。公开与参考实现中存在两套常见 `bdms` 盐值分支（早期 `cus` 盐值与 `bdms 1.0.1.19-fix.01` 的 `dhzx` 盐值 + 浏览器族请求头噪声带），需在技术选型与 [ACT-010](#act-010) 中实测确认当前线上接受度（`源码阅读（本机参考索引中的抖音签名与访问上下文实现、用户视频列表解析实现，2026-11-12）`）。
2. **`ArgusSecurityPlugin`（`x-secsdk-web-signature` + `uifid`）网关扩容（2026-09 观察）**：本机参考索引中的签名实现记录到 2026 年 9 月抖音在 `www.douyin.com` 的 14 条 GET 路径（含 `/aweme/v1/web/aweme/detail/`、`/aweme/v1/web/aweme/post/`、`/aweme/v1/web/mix/aweme/`、`/aweme/v1/web/mix/list/` 等，不含 `/aweme/v1/web/user/profile/other/` 与 `/profile/self/`）启用了 `uifid` 与 `x-secsdk-web-signature` 校验，不匹配时直接返回 HTTP 403 明文 `Blocked by ArgusSecurityPlugin ...`（`源码阅读（本机参考索引中的抖音签名与访问上下文实现，2026-11-12）`）。
3. **浏览器绑定动态安全 Cookie 的重放排斥（2025–2026）**：浏览器端 WebCrypto 私钥绑定的票据头/Cookie（如 `bd_ticket_guard_client_data`、`_bd_ticket_crypt_cookie`、`__security_mc_1_s_sdk`）与单次浏览器内存密钥及请求路径绑定；从浏览器整串复制 Cookie 后在桌面客户端静态重放这些字段，反而可能引发网关验签失败（`源码阅读（本机参考索引中的抖音签名与访问上下文实现，2026-11-12）`）。
4. **变更频率评估**：核心 JSON 字段结构（`aweme_detail`、`aweme_list`、`stream_data`）在 2025–2026 年间保持较高向后兼容性，但**签名盐值、网关校验头（如 Argus `uifid`）与未登录分页封锁策略平均每 3～6 个月出现一次局部收紧**（`社区资料（2026-11-12）`）。因此 TRACE 的抖音签名模块必须在架构上隔离为可独立替换/升级的内部子包，不把签名细节泄漏到业务模型。

<a id="auth"></a>
## 3. 登录会话与 Cookie 契约

来源：`源码阅读（本机参考索引中的抖音签名与访问上下文实现、用户视频列表解析实现、多站点更新订阅实现，2026-11-12）`、`社区资料（2026-11-12）`。

### 3.1 粘贴 Cookie 字段分类与清洗规则

按 DEC-043，用户在设置页粘贴浏览器已登录 `www.douyin.com` 后的 `Cookie` 字符串；TRACE 用 Windows DPAPI 加密落盘（[凭证设计](../design/architecture.md#credentials)），日志与错误报告严禁输出任何 Cookie 值。

| 分类 | 字段名 | 作用与处理策略 |
| --- | --- | --- |
| **核心身份态（必需）** | `sessionid`、`sessionid_ss` | 主登录会话令牌（通常两者同值，32 位十六进制字符串）。**本地预检至少要求存在非空 `sessionid`（或 `sessionid_ss`）**；两者皆无时直接本地报错“未检测到登录会话字段（sessionid）”，不发网络请求 |
| **配套身份态（强烈建议保留）** | `sid_tt`、`uid_tt`、`uid_tt_ss`、`sid_guard`、`odin_tt`、`passport_csrf_token`、`passport_csrf_token_default`、`is_staff_user`、`login_time` | ByteDance Passport 统一身份与会话元数据。其中 `sid_guard` URL 解码后形如 `<token>\|<签发Unix秒>\|<有效期秒，常见5184000即60天>\|<到期GMT时间>`，可用于本地辅助展示签发信息，但**不能代替服务端登录校验** |
| **访问上下文与风控 Cookie（建议保留）** | `ttwid`、`UIFID`、`UIFID_TEMP`、`uifid`、`msToken`、`s_v_web_id`、`__ac_nonce`、`__ac_signature` | `ttwid` 为直播接口必需；`UIFID` / `UIFID_TEMP` / `uifid` 供网关 `uifid` 校验使用；`msToken` 与 URL 参数对齐。用户从正常浏览过抖音首页/直播页的浏览器复制时通常已包含 |
| **浏览器内存密钥绑定的动态票据（建议入库或发包前剔除）** | `bd_ticket_guard_client_data`、`bd_ticket_guard_web_version`、`bd_ticket_guard_ree_public_key`、`_bd_ticket_crypt_cookie`、`__security_mc_1_s_sdk`、`__security_server_data_status`、`x_tt_token`、`sdk_source_info`、`fpk1`、`fpk2` | 依赖浏览器内不可导出的临时公私钥对动态签名；客户端脱离原浏览器上下文重放过期值容易被网关判为异常，参考实现统一在会话清洗阶段剥离（`源码阅读（本机参考索引中的抖音签名与访问上下文实现，2026-11-12）`；剥离对 `profile/self` 与 `aweme/post` 的实测影响列入 [ACT-010](#act-010) 核对） |

### 3.2 如何严格确认“确实已登录”（与不带 Cookie 对照）

在抖音 Web 接口上，**绝不能用“HTTP 200”或“接口返回了某个数字 ID”判定已登录**。两类典型误判陷阱已在源码阅读中确认（`源码阅读（本机参考索引中的抖音签名与访问上下文实现、用户视频列表解析实现，2026-11-12）`）：

| 候选接口 | 不带 Cookie（纯访客）表现 | 带有效登录 Cookie 表现 | 能否作为登录校验正本 |
| --- | --- | --- | --- |
| `GET /aweme/v1/web/query/user/` | **误判陷阱**：不带登录态也常返回 HTTP 200、`status_code: 0`，并给出 16 位访客设备 `user_uid`（伴随 `user_uid_type: 0`），且**不含** `sec_uid`、昵称与头像 | 返回 `status_code: 0` 与账号数字 `user_uid`（仍无昵称、头像与 `sec_uid`） | **否**（曾导致参考实现旧版本把匿名 `ttwid` 误判为已登录，且拿不到展示所需的昵称/头像/`sec_uid`） |
| `GET /passport/token/beat/` 或首页 HTML 正则匹配 | 心跳接口对访客也可返回 `{"message":"success"}`；HTML 体积大（1～2 MiB）且易受 `__ac_nonce` 拦截 | 返回成功或渲染首屏用户态 | **否**（开销大、契约脆弱） |
| **`GET https://www.douyin.com/aweme/v1/web/user/profile/self/`**（带公共参数 + `a_bogus`，无需传 `sec_user_id`） | 返回 HTTP 200，但 **`status_code` 非 0**（常见 `2483` 或 `8`，`status_msg` 提示未登录/需要登录）或 **`user` 为 `null`/空对象**，绝不会返回有效用户的 `sec_uid` | 返回 HTTP 200、**`status_code == 0`**，且 `user` 对象非空，同时给出当前登录账号自己的 **`user.uid`**（非 `"0"`）、**`user.sec_uid`**（`MS4wLjABAAAA...`）、**`user.nickname`**、**`user.avatar_thumb.url_list`** | **是（推荐唯一正本）**；一次轻量 JSON 请求同时完成严格登录态验证与设置页当前账号资料（`uid` / `sec_uid` / 昵称 / 头像）读取 |

TRACE 登录确认判定式（缺一不可；精确未登录错误码在 [ACT-010](#act-010) 用“1 次不带 Cookie + 1 次带测试 Cookie”实测锁定）：

1. HTTP 状态码为 `200`，响应为合法 JSON 对象，且未命中[第 9 节](#errors)的任何验证码/网关风控特征；
2. `status_code == 0`；
3. `user` 为非空对象，`user.uid` 为非空且不等于 `"0"` 的数字字符串，`user.sec_uid` 满足 `^MS4wLjAB[A-Za-z0-9_-]{16,}$`，`user.nickname` 非空。

只要上述任一条件不满足：若 `status_code` 明确指示未登录（如 `2483` / `8` 或 `user == null` 且无验证码字段），归类为 `auth_required`（提示 Cookie 无效或已过期）；若出现 `verify_center_decision_conf`、HTTP 403 等，归类为 `risk_control`（不能把风控临时拦截误判成永久清除用户已保存的凭证）。

### 3.3 会话有效期与已登录 / 未登录差异矩阵

- **会话通常多久失效**：从 `sid_guard` 观察到的客户端声明周期常见为 **30～60 天**（`5184000` 秒）（`源码阅读（本机参考索引中的用户视频列表解析实现，2026-11-12）`、`社区资料（2026-11-12）`）；但用户在原浏览器主动退出登录、修改密码、异地环境切换或触发账号安全保护时，服务端会提前吊销 `sessionid`（具体吊销阈值 `unknown`，`待真实验证`）。按 DEC-043 与 Q-007 既有原则，粘贴的 Cookie 与用户浏览器共享同一会话，**TRACE 不做自动续期**，失效后提示用户重新粘贴。
- **已登录与未登录在各接口上的行为差异**：

| 场景 / 接口 | 未登录（访客）表现 | 已登录（带有效 `sessionid` 等）表现 |
| --- | --- | --- |
| 当前账号资料 `/aweme/v1/web/user/profile/self/` | 拒绝返回用户对象（未登录状态码或空 `user`） | 返回当前登录账号完整资料（`uid`、`sec_uid`、`nickname`、头像） |
| 用户作品列表 `/aweme/v1/web/aweme/post/` | **仅第 1 页（`max_cursor=0`）偶可返回**；一旦翻第 2 页及以后（`max_cursor > 0`），固定返回 17 字节空壳 `{"status_code":0}`（无 `aweme_list`、无 `has_more`） | 支持按 `max_cursor` 正常连续翻页直至 `has_more == 0` |
| 作品详情 `/aweme/v1/web/aweme/detail/` | 公开作品在带齐 `a_bogus` + 上下文 Cookie 时通常可读，但高频或缺少 `uifid` 时更容易返回空体/验证码 | 公开作品稳定可读；登录账号有权限的粉丝可见/朋友可见/自己私密作品也可返回媒体流 |
| 视频清晰度档位 `video.bit_rate[]` | 公开作品通常返回常规 720P/1080P 档位；高码率/2K/4K 档位是否对访客裁剪在公开资料中说法不一（`unknown`） | 返回该账号在 Web 端可拿到的全部无水印 `bit_rate[]` 档位 |
| 直播间进入 `/webcast/room/web/enter/` | 带 `ttwid` + `a_bogus` 时可读取部分公开直播间流，但部分直播间开启登录门槛、匿名观众信息被脱敏，且更容易撞上滑块 | 正常返回完整 `stream_data`（含 `origin` 原画、`uhd` 蓝光等档位）；DEC-043 统一要求先登录 |

<a id="links"></a>
## 4. 链接识别与短链展开

来源：`源码阅读（本机参考索引中的抖音签名与访问上下文实现、单个视频链接解析实现、用户视频列表解析实现、抖音直播录制实现，2026-11-12）`。

### 4.1 从分享口令 / 混排文本提取 URL

抖音 App 复制出的分享口令通常把短链与中文文案、话题、标点或表情符号紧挨在一起（例如：`7.56 复制打开抖音，看看【合成示例】的作品 https://v.douyin.com/iSYNTHETIC/ 01/01 abc:/`）。本地提取规则：

1. 在输入文本中查找首个 `https://`（仅接受 `https`；若用户只写了裸域名如 `v.douyin.com/...` 或 `www.douyin.com/...`、`live.douyin.com/...`，可在匹配域名白名单前提下补 `https://`；明确带 `http://`、用户名密码 `user:pass@` 或自定义端口 `:8080` 的 URL 直接按不支持拒绝）。
2. URL 扫描在遇到以下任一字符时立即截断：空白字符、双引号/单引号、尖括号、反引号、中文及全角标点与中日韩统一表意文字（`\u3000-\u303F`、`\u3400-\u4DBF`、`\u4E00-\u9FFF`、`\uFF00-\uFFEF`）以及 Emoji 区段。
3. 截断后再剥离末尾可能粘连的英文半角句读符号：`.,;:!?)]}`。
4. **短链粘连数字口令码剥离**：部分客户端版本复制的口令会把数字口令码无空格紧贴在短链斜杠后（如 `https://v.douyin.com/iSYNTHETIC/7.46`，合成示例）；截断标点后若匹配 `^https://v\.douyin\.com/[A-Za-z0-9_-]+/\d+(?:\.\d+)?/?$`，需剥离尾随的 `/\d+(?:\.\d+)?/?$`，还原为标准短链 `https://v.douyin.com/<code>/`。
5. 若输入本身不含 URL：检查是否为纯 `MS4wLjABAAAA...` 格式的 `sec_uid`（可视为用户主页输入，是否开放裸 ID 输入由 Agent A 在 UI/服务契约中拍板；**纯 19 位数字因无法静态区分 `aweme_id`、`uid`、`room_id` 与 `web_rid`，不应盲目猜测类型**）。

### 4.2 短链与多跳重定向展开规则

短链域名包括 `v.douyin.com`（最常见）与历史短链域名 `v.amemv.com`。展开规则（遵循[站点规则](../dev-rules/sources-and-safety.md#site-rules)与安全边界）：

1. **手动逐跳读取 `Location`**（禁止 HTTP 客户端无条件自动跟随到任意外站），最多 **5 跳**（实测常见 1～3 跳：`v.douyin.com` → `www.iesdouyin.com/share/...` → `www.douyin.com/...` 或 `live.douyin.com/...`）。
2. **每跳安全校验**：目标 URL 必须是 `https`，无用户名密码、无非标准端口，且主机名严格属于抖音允许域集合：`douyin.com`、`www.douyin.com`、`live.douyin.com`、`iesdouyin.com`、`www.iesdouyin.com`、`amemv.com`、`v.amemv.com`、`webcast.amemv.com`（禁止跟随到私有/回环/链路本地 IP 或第三方外链）。
3. **提前收敛与防环**：每拿到一跳 `Location`，先跑本地路由识别——只要已经能从 URL 路径或查询参数无歧义提取出目标类型与 ID（例如第 1 跳落到 `https://www.iesdouyin.com/share/video/7300000000000000001/?...` 或 `https://www.iesdouyin.com/share/user/?sec_uid=MS4wLjABAAAA_SYNTHETIC_EXAMPLE`），**即可停止继续发 HTTP 请求**，不必把后续 HTML 页面真正拉下来！若相邻两跳规范化后指向同一业务目标，视为已到达终点而非死循环；超过 5 跳仍无法识别则报错停止。

常见跳转落点全景（以下 ID 均为**合成示例**）：

| 短链来源场景 | 常见中间跳 / 最终 `Location` 形态 | 提取方式 |
| --- | --- | --- |
| 普通视频分享 | `https://www.iesdouyin.com/share/video/7300000000000000001/?region=CN&...` 或跳往 `https://www.douyin.com/video/7300000000000000001` | 路径段提取 `aweme_id = 7300000000000000001` |
| 图文（笔记/幻灯片）分享 | `https://www.iesdouyin.com/share/note/7300000000000000002/...`、`/share/slides/7300000000000000002/...` 或 `https://www.douyin.com/note/7300000000000000002` | 路径段提取 `aweme_id = 7300000000000000002`（详情接口与视频共用 `aweme/detail`） |
| 用户主页分享 | `https://www.iesdouyin.com/share/user/MS4wLjABAAAA_SYNTHETIC_EXAMPLE?...` 或 **`https://www.iesdouyin.com/share/user/?sec_uid=MS4wLjABAAAA_SYNTHETIC_EXAMPLE&...`**（注意路径末尾为空、ID 在查询参数 `sec_uid` 或 `sec_user_id` 中！）或 `https://www.douyin.com/user/MS4wLjABAAAA_SYNTHETIC_EXAMPLE` | 优先取路径段 `/user/<sec_uid>`，为空时回退读查询串 `sec_uid` / `sec_user_id`（原样保留 `-`、`_` 与连续 `__`） |
| 网页直播间分享 | `https://live.douyin.com/100000000001`、`https://www.iesdouyin.com/share/live/100000000001?...`、`https://www.douyin.com/follow/live/100000000001` | 提取网页直播间号 `web_rid = 100000000001` |
| 手机 App 直播间口令分享（`reflow`） | `https://webcast.amemv.com/douyin/webcast/reflow/7400000000000000001?u_code=...&sec_user_id=MS4wLjABAAAA_SYNTHETIC_EXAMPLE&app_id=1128...` | 路径中的 `7400000000000000001` 是**单场开播 `room_id`**（非 `web_rid`！），查询串常同时带主播 `sec_user_id`；调一次 `webcast.amemv.com/webcast/room/reflow/info/`（见[第 8.1 节](#live)）即可换出 `data.room.owner.web_rid` 与 `data.room.owner.sec_uid` |

### 4.3 路由匹配优先级与不支持链接拒绝表

**关键优先级规则**：`?modal_id=<数字>`（以及精选页 `?vid=<数字>`）**必须在 `/user/<sec_uid>` 之前匹配**！因为在抖音 PC 网页端，用户在博主主页、发现页、搜索结果页点开某个作品弹窗时，浏览器地址栏会变成 `https://www.douyin.com/user/MS4wLjABAAAA_SYNTHETIC_EXAMPLE?modal_id=7300000000000000001` 或 `https://www.douyin.com/discover?modal_id=7300000000000000001`——用户复制该链接的真实意图是**当前打开的单个作品**，而不是整个用户主页或拒绝搜索页（`源码阅读（本机参考索引中的抖音签名与访问上下文实现、单个视频链接解析实现，2026-11-12）`）。

按顺序执行的本地路由判定表（所有 ID 均为**合成示例**）：

| 优先级 | URL 特征 | 识别结果 | 处理说明 |
| --- | --- | --- | --- |
| 1 | 短链域 `v.douyin.com/<code>`、`v.amemv.com/<code>` | 短链待展开 | 按 §4.2 逐跳解析后重新进入第 2～8 步 |
| 2 | 含查询参数 `modal_id=<15~20位数字>` 或（`/jingxuan` 等路径下）`vid=<15~20位数字>`（且不在商城等外部域） | **单个作品**（`aweme_id`） | 即使路径是 `/user/<sec_uid>`、`/discover`、`/recommend`、`/hot`、`/search/...`，只要带合法数字 `modal_id`，一律按单作品详情处理 |
| 3 | `www.douyin.com` / `iesdouyin.com` 下 `/video/<id>`、`/note/<id>`、`/slides/<id>`、`/share/video/<id>`、`/share/note/<id>`、`/share/slides/<id>` | **单个作品**（`aweme_id`） | 视频与图文统一走 `aweme/detail`（见[第 6.1 节](#detail)） |
| 4 | `live.douyin.com/<web_rid>`（纯数字路径段，排除 `/webcast/...` 等内部路径）、`iesdouyin.com/share/live/<web_rid>`、`www.douyin.com/follow/live/<web_rid>` | **直播间**（`web_rid`） | 调 `webcast/room/web/enter/` 解析主播 `sec_uid`、直播状态与流；按 FR-LINK-002 打开主播用户页或进入直播间动作 |
| 5 | `webcast.amemv.com/douyin/webcast/reflow/<room_id>`（可带 `?sec_user_id=...`） | **直播间场次分享**（`room_id`） | 调 `webcast/room/reflow/info/` 换出 `owner.web_rid` 与 `owner.sec_uid`（见[第 8.1 节](#live)） |
| 6 | `/user/self`（浏览器打开“我的主页”时的字面量别名） | **本人主页别名** | `"self"` **不是合法 `sec_uid`！** 若已登录可映射为当前登录账号的 `sec_uid`（来自 `/user/profile/self/`），否则提示不支持该通用地址并引导复制具体用户分享链接 |
| 7 | `/user/<sec_uid>`、`/share/user/<sec_uid>`，或 `/share/user/` 配合查询参数 `sec_uid` / `sec_user_id`（匹配 `^MS4wLjAB[A-Za-z0-9_-]+$`） | **用户主页**（`sec_uid`） | 打开用户资料与作品列表 |
| 8 | **不支持的抖音路径或子域**（本地或短链展开后命中） | **明确拒绝**（`bad_request` 附 `Unsupported=true`；越界跳转归 `blocked`） | 见下表，立即给出中文原因“暂不支持这种抖音链接”，**不再发起后续 API 请求** |

明确拒绝的不支持链接清单（M3 范围外，本地或跳转落定即停）：

- **电商与团购**：`mall.douyin.com`、`jinritemai.com`、`haohuo.jinritemai.com`、路径含 `/shop/`、`/goods/`、商品橱窗分享。
- **聚合页与发现页（不带 `modal_id`）**：合集 `/collection/<mix_id>`、`/mix/<mix_id>`、带 `?mix_id=` 却无 `modal_id`、音乐聚合 `/music/<id>`、话题挑战 `/hashtag/<id>`、`/challenge/<id>`、纯搜索页 `/search/...`、纯推荐页 `/discover`、热榜 `/hot/...`。
- **长视频放映厅 / 短剧 / 小程序**：`/lvdetail/...`、`/vsdetail/...`、`/playlet/...`、小程序 `microapp` 分享链。
- **非安全或外站链接**：非 `https`、含用户名密码或端口、不在允许域列表中的任何域名（如 TikTok 国际版 `tiktok.com` 不属抖音站点适配器）。

<a id="ids"></a>
## 5. 稳定 ID 体系与主播房间映射

来源：`源码阅读（本机参考索引中的抖音签名与访问上下文实现、单个视频链接解析实现、用户视频列表解析实现、抖音直播录制实现、多站点更新订阅实现，2026-11-12）`。

### 5.1 六类核心标识符稳定性对比

**全局精度铁律**：抖音的所有数字型 ID（`aweme_id`、`uid`、`room_id`、`web_rid`）最高可达 19 位 64 位有符号整数（超过 IEEE-754 双精度安全整数上限 `2^53 - 1 = 9007199254740991`）。在 Go 后端反序列化动态 JSON 时必须优先读 `*_str` 字符串字段或启用 `json.Decoder.UseNumber()`，在前端与本地 JSON 清单中**一律以 `string` 存储和传递**，严禁转成 JS `number` 或 `float64`。

| 标识符 | 典型形态（合成示例） | 稳定性 | 作用域与关系 | TRACE 是否可作持久化主键 |
| --- | --- | --- | --- | --- |
| `aweme_id` | 19 位数字串（兼容 15～20 位），如 `"7300000000000000001"` | **永久稳定** | 单个视频或图文作品的全局唯一 Snowflake ID（右移 32 位近似对应秒级发布时间） | **是**（抖音作品主键，缓存目录与去重键） |
| `sec_uid`（接口入参常名 `sec_user_id`） | `MS4wLjABAAAA...` Base64URL 字符串（常见 55～120 字符，含字母数字、`-`、`_`，**可能含连续双下划线 `__`，绝不可折叠或截断**），如 `"MS4wLjABAAAA_SYNTHETIC_EXAMPLE"` | **长期稳定** | 抖音 Web 端所有用户接口（`profile/other`、`aweme/post`）**唯一接受的用户路由键** | **是**（抖音订阅者、作者、媒体库归属的用户主键） |
| `uid` | 11～19 位纯数字串，如 `"1000000000000001"` | 账号内部 ID，但 **Web 接口不接受以 `uid` 查询用户** | 用户内部数字 ID；仅在 `profile/self`、`profile/other`、`author` 响应对象中附带返回 | **否**（仅在凭证元数据或诊断信息中辅助记录，不作路由主键） |
| `unique_id` 与 `short_id` | `unique_id`: `"synthetic_handle"`（用户自定义抖音号）；`short_id`: `"880000001"`（未改名时默认数字抖音号，改名后常变 `"0"`；用户 2026-11-13 观察且 `实测（ACT-010 V-08，2026-11-13）` 确认数字 `short_id` 能直接作 `live.douyin.com/<short_id>` 调通 `web/enter`） | **不稳定（用户可在 App 内修改 `unique_id`）** | App 界面展示的“抖音号”；优先展示非空 `unique_id`，为空时回退展示非 `"0"` 的 `short_id` | **严禁作主键**（仅作 UI 展示字段；当确认为纯数字 `short_id` 且被 `web/enter` 接受时，可优先存入可失效的 `webRid` 寻址缓存） |
| `web_rid` | 常见 8～16 位纯数字串，如 `"100000000001"` | **寻址缓存（非永久不变）**：`实测（ACT-010 V-08，2026-11-13）` 确认同一个直播间同时接受主播数字 `short_id` 与另一个更长可变数字两种 `web_rid` 请求并返回开播房间与主播 `sec_uid`；因此 TRACE 不把 `web_rid` 当身份主键，而是作为绑定在 `sec_uid` 下可校验、可失效回退的寻址缓存（确认数字 `short_id` 可用时优先保留 `short_id`） | `live.douyin.com/webcast/room/web/enter/` 的必填查询参数！注意：**`web/enter` 响应体顶层不一定回显请求传入的 `web_rid`**，调用方必须自己保存传入的 `web_rid` 并核对返回主播 `sec_uid` | **仅作可失效的直播间寻址缓存**（订阅与用户身份主键一律用 `sec_uid`） |
| `room_id`（`id_str`） | 19 位纯数字串，如 `"7400000000000000001"`；未开播时在用户资料中为 `0` | **每场开播变化！（单场次 ID）** | 主播每次点击开播都会分配一个新的 `room_id`，下播后该 `room_id` 永久处于已结束（`status=4` / `alive=false`）状态 | **仅作“单场直播录像条目”去重/场次标识**；**绝不能**当成主播固定直播间号长期写死用来查下一场开播 |

### 5.2 从用户（`sec_uid`）找到直播间 `web_rid` 或当前 `room_id` 的全部途径与局限

这是抖音与哔哩哔哩最大的协议差异之一（B 站无论开播与否都能用 `uid` 调接口直接拿到固定 `room_id`，而抖音把用户 `sec_uid`、网页固定房间号 `web_rid` 与单场 `room_id` 拆成了两套体系）。经跨项目源码比对，从已知 `sec_uid` 获取直播间信息的全部可行途径与局限如下（`源码阅读（本机参考索引中的抖音直播录制实现、多平台无界面录制实现、多站点更新订阅实现、抖音签名与访问上下文实现，2026-11-12）`）：

| 途径 | 调用方式 | 主播**正在直播时**能拿到什么 | 主播**未开播（下播）时**的表现与局限 |
| --- | --- | --- | --- |
| **途径 A：用户资料接口 `GET /aweme/v1/web/user/profile/other/?sec_user_id=<sec_uid>`** | 主站常规 JSON 接口（带 `a_bogus` + 登录 Cookie） | `user.room_id`（或 `room_id_str`）为非 `0` 的 19 位当场 `room_id`；部分响应同时带 `user.room_data`（转义 JSON 字符串，解析后含 `id_str`、`status=2`、`owner.web_rid` 与 `stream_url`） | **局限**：未开播时 `user.room_id == 0`、`live_status == 0`，且**没有 `room_data`，无法在未开播状态下反查出该用户的 `web_rid`**！ |
| **途径 B：用户作品列表 `GET /aweme/v1/web/aweme/post/?sec_user_id=<sec_uid>`** | 订阅检查更新本来就要调的第 1 页作品列表 | 若该用户有作品，列表项 `aweme_list[0].author.room_id` / `room_data` 在开播期间同样会变为非 `0` 当场 `room_id`（可零额外请求顺便获知正在开播！） | **局限**：1) 未开播时 `author.room_id == 0`，拿不到 `web_rid`；2) 若主播 0 作品（纯直播账号），`aweme_list` 为空，拿不到 `author` 节点 |
| **途径 C：直播回流接口 `GET https://webcast.amemv.com/webcast/room/reflow/info/`** | 参数 `type_id=0&live_id=1&room_id=<已知room_id或占位值2>&sec_user_id=<sec_uid>&app_id=1128`（无需 `a_bogus`） | **当传入开播中的 `room_id`（例如刚从途径 A/B 拿到的 `user.room_id`）时**，`data.room` 完整返回本场直播状态 `status=2`、流地址 `stream_url`，且 **`data.room.owner.web_rid` 直接给出该主播稳定的网页直播间号 `web_rid`！** | **局限**：部分直播录制实现在不知道 `room_id` 时尝试传占位 `room_id=2` 配合 `sec_user_id` 查询（`源码阅读（本机参考索引中的抖音直播录制实现，2026-11-12）`）；但该写法对未开播主播或从未开播账号是否仍返回 `owner.web_rid` 还是返回 `status_code=10011`，静态资料无法 100% 保证，列为 [ACT-010](#act-010) 必测项 |
| **途径 D：直接由直播间链接 `live.douyin.com/<web_rid>` 进入** | `GET https://live.douyin.com/webcast/room/web/enter/?web_rid=<web_rid>...` | 无论开播（`status=2`）还是下播（`status=4`），`data.user.sec_uid`（或 `data.data[0].owner.sec_uid`）都能反查出主播 `sec_uid`！ | 需要用户最初输入过一次 `live.douyin.com/<web_rid>` 或直播分享短链，或者以前在主播开播时通过途径 A/B → C 捕获并持久化过 `web_rid` |
| **途径 E：抓取用户主页 HTML 首屏 `https://www.douyin.com/user/<sec_uid>`** | 解析 SSR 脚本中的 `roomData` | 同途径 A，开播时有 `roomId` / `web_rid` | **不推荐**：未开播时同样为空，且单页 HTML 1～2 MiB、易触发 `__ac_nonce` 与滑块挑战 |

**对 TRACE 订阅与开播检查的工程结论**：
1. 订阅数据结构应为抖音订户保存主键 `sec_uid` 的同时，持久化一个可选的 `web_rid` 字段：
   - 若用户粘贴的是直播间链接（`live.douyin.com/<web_rid>` 或直播分享短链），立即可同时拿到 `web_rid` 与主播 `sec_uid` 并落盘；
   - 若用户粘贴的是用户主页或视频链接创建订阅，初始 `web_rid` 可能为空；此后任一时刻只要通过 `profile/other` 或 `aweme/post` 观察到 `room_id > 0`，立即调一次 `reflow/info`（或解析 `room_data`）提取出 `owner.web_rid` 并回写持久化，从此以后该订户就永久拥有了 `web_rid`。
2. 当订户已有 `web_rid` 时，开播检查直接调 `webcast/room/web/enter/?web_rid=<web_rid>` 一步拿到开播状态与流地址；当订户尚无 `web_rid` 时，用 `user/profile/other/?sec_user_id=<sec_uid>` 检查 `user.room_id > 0`（若同时在做作品更新检查，还可直接复用 `aweme/post` 返回的 `author.room_id` 省掉一次请求），发现开播后再经 `reflow/info` 补齐 `web_rid` 与流地址。

<a id="contracts"></a>
## 6. 核心接口契约

来源：`源码阅读（本机参考索引中的抖音签名与访问上下文实现、单个视频链接解析实现、用户视频列表解析实现、多站点更新订阅实现，2026-11-12）`、`社区资料（2026-11-12）`。

<a id="detail"></a>
### 6.1 作品详情：`aweme/detail`（视频与图文通用）

- **端点与方法**：`GET https://www.douyin.com/aweme/v1/web/aweme/detail/`
- **适用范围**：**视频作品（`/video/<id>`）与图文/实况作品（`/note/<id>`、`/slides/<id>`）共用同一个详情接口**。
- **必要参数**：[第 2.1 节](#headers)公共参数（**`aid=6383` 不可改为 `1128`**，否则图文作品会被服务端直接过滤并返回 `filter_detail.filter_reason = "images_base"`）+ `aweme_id=<作品ID>`。
- **签名要求**：`a_bogus`（必需）；位于网关受保护路径列表内，若 Cookie 含 `uifid` 需同步附带 `x-secsdk-web-signature` 与 `uifid`（见[第 2.2 节](#signing)）。
- **一致性校验**：成功时 `aweme_detail.aweme_id` 必须严格等于请求的 `aweme_id`，不一致按 `schema_changed` 处理。

关键响应字段路径（根对象 `aweme_detail`）：

| 字段路径 | 类型 | 含义与边界注意点 |
| --- | --- | --- |
| `aweme_id` | `string` | 作品唯一 ID（如 `"7300000000000000001"`，合成示例） |
| `aweme_type` / `media_type` | `int` | 作品类型枚举（见下表）；**判别图文与视频应以 `len(images) > 0`（含 `image_post_info.images`）为主判据、`aweme_type` 为辅** |
| `desc` | `string` | 作品文案（抖音无独立标题字段，TRACE 取首行非空文本或截断前段作展示标题，全文作描述；可能为空字符串） |
| `create_time` | `int64` | 发布时间（**Unix 秒**，注意列表游标 `max_cursor` 是毫秒而 `create_time` 是秒！） |
| `duration` / `video.duration` | `int64` | 视频时长（常见为**毫秒**，偶见秒级小整数需按量级兼容；图文作品忽略） |
| `author.{sec_uid, uid, nickname, unique_id, short_id, signature, avatar_larger.url_list, avatar_thumb.url_list}` | `object` | 作者信息（列表与详情结构一致，但列表内 `author` 通常不含粉丝数等统计项） |
| `statistics.{digg_count, comment_count, collect_count, share_count, play_count}` | `object` | 互动计数。**关键陷阱：抖音 Web 接口返回的 `statistics.play_count` 恒为占位符 `0`，绝不能在 UI 或元数据里展示为“0 播放”**，应视为站点未公开（置空/不展示） |
| `status.{is_delete, private_status, is_prohibited, in_reviewing}` | `object` | 可见性状态：`private_status == 0` 公开、`1` 仅作者自己可见（私密）、`2` 朋友可见；`is_delete == true` 为已删除（此时外层仍可能保留 `desc`/`author`，但媒体流为空，按 `unavailable` 处理而非 `schema_changed`） |
| `video` | `object` | 视频流容器（详见 [§6.5](#playurl)；注意图文作品也带 `video` 节点，其 `play_addr` 装的是背景音乐音频而非画面！） |
| `images`（备选 `image_post_info.images`） | `array` | 图文/实况页列表（详见 [§6.4](#images)） |
| `music` | `object` | 配乐信息：`id_str`、`title`、`author`、`duration`（秒）、`play_url.url_list`、`is_original_sound`（见 [§6.4](#images)） |
| `text_extra[].hashtag_name` | `array` | 话题标签列表 |
| `filter_detail.{aweme_id, filter_reason, notice, detail_msg}` | `object`（顶层兄弟键） | 当 `aweme_detail` 为 `null` 时，若顶层存在非空 `filter_detail.filter_reason`（如作品已删除、私密、审核未通过），属于明确的 `unavailable`；若 `aweme_detail == null` 且无 `filter_detail`，属于风控拦截 `risk_control`！ |

`aweme_type` 常见取值（`源码阅读（本机参考索引中的单个视频链接解析实现、用户视频列表解析实现、抖音签名与访问上下文实现，2026-11-12）`）：

| `aweme_type` | 常见 `media_type` | 含义与客户端处理 |
| --- | --- | --- |
| `0`、`4` | `4` | 普通短视频（主体在 `video`） |
| `68`（另见 `2`、`150`） | `2`（纯静态图集）或 `42`（含实况照片） | 图文笔记 / 多图幻灯片（主体在 `images[]`）；注意偶有 `aweme_type` 为 `2`/`68`/`150` 但 `images` 为空、`video.bit_rate` 非空的“图文转视频”形态，按有无 `images[]` 判定即可无缝兼容 |
| `55`、`61`、`109` | — | 转发、合拍等衍生视频作品（有 `video` 按视频处理） |
| `101` | — | 信息流内嵌直播间卡片（非点播作品，跳过或引导直播间） |
| 其他未知整数 | — | 不报错丢弃：先看 `images[]` 非空按图文、否则 `video.play_addr` 非空按视频，均无则标明类型暂不支持 |

<a id="user"></a>
### 6.2 用户资料：`user/profile/other`（与 `profile/self`）

- **查看他人资料**：`GET https://www.douyin.com/aweme/v1/web/user/profile/other/`
  - 必要参数：公共参数 + `sec_user_id=<目标用户 sec_uid>` + `source=channel_pc_web` + `publish_video_strategy_type=2` + `personal_center_strategy=1`。
  - 签名要求：`a_bogus`（必需）；不在 14 条 `ArgusSecurityPlugin` 路径表内。
- **查看当前登录账号自己**：`GET https://www.douyin.com/aweme/v1/web/user/profile/self/`（见[第 3.2 节](#auth)，不传 `sec_user_id`）。
- **一致性校验**：`profile/other` 返回的 `user.sec_uid` 必须等于请求的 `sec_user_id`。

`user` 节点关键字段：

| 字段路径 | 类型 | 说明 |
| --- | --- | --- |
| `sec_uid` | `string` | 稳定用户主键 |
| `uid` | `string` | 内部数字 ID |
| `nickname` | `string` | 昵称 |
| `unique_id` / `short_id` | `string` | 抖音号（`unique_id` 为空时回退非 `"0"` 的 `short_id`） |
| `signature` | `string` | 个人简介 |
| `avatar_larger.url_list` / `avatar_medium.url_list` / `avatar_thumb.url_list` | `string[]` | 头像地址列表（优先取 `avatar_larger`，`http://` 统一升为 `https://`） |
| `aweme_count` | `int` | 主页显示作品总数（**仅作参考展示**：含置顶、可能含部分仅粉丝/好友可见或刚删除未刷新计数，实际翻页拉到的公开条数常略少于 `aweme_count`） |
| `follower_count` / `following_count` / `total_favorited` | `int64` | 粉丝数、关注数、获赞总数 |
| `secret` | `int` | `1` 表示私密账号（未关注获批时无法浏览作品列表） |
| `room_id` / `room_id_str` / `live_status` / `room_data` | `int64` / `string` | **开播状态字段**：开播中 `room_id > 0`（`room_data` 为内嵌 JSON 字符串，可解出 `owner.web_rid` 与流信息）；未开播时 `room_id == 0`、`live_status == 0` 且通常不含 `room_data`（见[第 5.2 节](#ids)） |

<a id="posts"></a>
### 6.3 用户作品列表：`aweme/post` 与置顶单调性

- **端点与方法**：`GET https://www.douyin.com/aweme/v1/web/aweme/post/`
- **必要参数**：公共参数（`aid=6383`）+ `sec_user_id=<sec_uid>` + `max_cursor=<首页传 0，后续传上一页返回的 max_cursor>` + `count=18`（浏览器常用 `18`，服务端单页上限约 `20`）+ `locate_query=false` + `show_live_replay_strategy=1` + `need_time_list=0` + `time_list_query=0` + `publish_video_strategy_type=2`。
- **签名与凭证要求**：**必须携带已登录 Cookie**（未登录第 2 页起必被截断为空壳 `{"status_code":0}`）+ `a_bogus` + `x-secsdk-web-signature` / `uifid`（网关受保护路径）。
- **分页字段**：
  - `aweme_list[]`：本页作品数组，单条结构与 `aweme/detail` 的 `aweme_detail` 同构；
  - `has_more`：`1`（或 `true`）表示仍有下一页，`0`（或 `false`）表示已翻到底；
  - `max_cursor`：下一页游标，**13 位毫秒级时间戳整数**（如 `1716300000000`），表示下一页应拉取早于该时间点的作品；翻页时原样转成字符串传给下一次请求的 `max_cursor`，且若出现 `has_more == 1` 但 `next_max_cursor == request_max_cursor`（游标原地踏步），必须立即终止翻页防止死循环；
  - `min_cursor`：本页上界毫秒时间戳。

#### 置顶作品 `is_top` 对时间单调性与 TRACE 订阅检查的决定性影响

源码与真实回放夹具核实（`源码阅读（本机参考索引中的抖音签名与访问上下文实现、用户视频列表解析实现，2026-11-12）`）：

1. **第 1 页置顶乱序现象**：作者最多可置顶 1～3 条旧作品（条目字段 `is_top == 1`）。在 `max_cursor=0` 的第 1 页返回中，`is_top == 1` 的置顶作品固定排在 `aweme_list` 最前面（下标 `0..k-1`），其 `create_time` 可能是数月甚至数年前（例如夹具中 `aweme_list[0]` 为 `is_top=1, create_time=1716200000`，紧随其后的 `aweme_list[1]` 为 `is_top=0, create_time=1716300000`）；除去 `is_top == 1` 条目后，其余常规作品（`is_top == 0`）按 `create_time` **严格降序**排列。
2. **`max_cursor` 是否受置顶污染**：当第 1 页同时包含置顶作品与常规作品时，服务端返回的 `max_cursor` 仍可正常驱动第 2 页继续向历史翻页；翻页跨页时偶有边界作品重复出现，客户端必须按 `aweme_id` 去重。
3. **TRACE 订阅建起点与增量检查更新规则（对应[订阅设计](../design/subscriptions.md#update-check)与 INV-12）**：
   - **首次订阅建立基线（未读数 = 0）**：读取第 1 页后，不能盲目拿 `aweme_list[0]` 当最新锚点！应记录第 1 页所有可见作品的已见集合（或取全页最大 `create_time` 与对应非置顶最新 `aweme_id` 作为高水位线），若一个作者第 1 页全都是置顶作品或少于置顶数，基线取全页 `max(create_time)`。
   - **后续增量检查早停条件**：遍历 `aweme_list` 时，**遇到 `is_top == 1` 的条目只按 `aweme_id` 判断是否为未见过的新作品，绝不能因为某条 `is_top == 1` 旧置顶作品的 `create_time <= 基线时间` 就提前 break 停止扫描第 1 页**！只有遇到 **`is_top != 1`（非置顶）** 且已命中历史基线（`aweme_id == baseline_id` 或 `create_time <= baseline_time`）时，才判定已追平历史并停止继续向后翻页。
4. **私密 / 仅朋友 / 粉丝可见作品在列表中的表现**：
   - 查看**他人**主页时：作者设为私密（`private_status == 1`）或当前登录账号无权查看的朋友可见（`private_status == 2`）作品会被服务端直接从 `aweme_list` 剔除，不返回占位项（这是实际可拉取总数低于 `user.aweme_count` 的正常原因，不算风控或丢页）；
   - 查看**登录账号本人**或已互关好友主页时：有权可见的受限作品会出现在 `aweme_list` 中，其 `status.private_status` 为 `1` 或 `2`。

<a id="images"></a>
### 6.4 图文作品与实况照片（Live Photo）

来源：`源码阅读（本机参考索引中的抖音签名与访问上下文实现、单个视频链接解析实现、用户视频列表解析实现，2026-11-12）`。

当 `aweme.images`（或回退路径 `aweme.image_post_info.images`）为非空数组时，该作品为**图文/实况图集**（常见 `aweme_type == 68`，亦见 `2`、`150`）：

| 字段路径 | 含义与无水印/格式规则 |
| --- | --- |
| `images[i].uri` | 图床资源唯一键 |
| `images[i].width` / `images[i].height` | 单张图片像素宽高 |
| **`images[i].url_list[]`** | **无水印原图 CDN 镜像列表**（通常给出 2～4 个 `*.douyinpic.com` / `*.byteimg.com` 地址）。注意数组不同元素可能是不同转码后缀（常见 `.webp` 与 `.jpeg`；部分实拍图含 `.heic` 源）：若需要桌面端通用解码兼容，优先挑选 `url_list` 中非 `.heic` 的 `.jpeg`/`.png`/`.webp` 直链，下载落盘时以响应 `Content-Type`（`image/jpeg`、`image/webp`、`image/png`）或文件魔数决定最终扩展名，不盲信 URL 路径后缀 |
| `images[i].download_url_list[]` | **带抖音水印图地址**（URL 模板常含 `tplv-dy-water` 或 `watermark` 字样），TRACE 选流时**跳过不用** |
| **`images[i].live_photo_type`** 与 **`images[i].clip_type`** | **实况照片（Live Photo）标识**：普通静态图无 `video`（或 `video: null`）；实况照片单页携带 `live_photo_type == 1`（常见伴随 `clip_type == 5`，其他循环模式可能有不同非零 `clip_type`），且同级带非空 `images[i].video` 对象（仅在 PC Web `aid=6383` 接口完整返回，移动端分享 HTML 常缺失该字段） |
| **`images[i].video.play_addr.url_list[]`**（及 `images[i].video.bit_rate[]`） | **实况照片对应的无水印动态短片**（通常为 3～5 秒、带声音的 H.264/H.265 MP4 单文件，含 `width`、`height`、`duration`、`data_size`）。若声明 `live_photo_type == 1` 但缺失 `video.play_addr.url_list`，应视为结构异常而非静默退化为静态图 |
| `music.play_url.url_list[]` 与 `music.is_original_sound` | 图集配乐/原声 MP3/M4A 地址（可能为空——部分受版权保护曲目 `play_url` 下无 `url_list`）。注意：**判断是否为作者原声应读布尔值 `music.is_original_sound`（而非 `music.is_original`，后者表示音乐人发行曲目）** |
| **视频页（图文里混排的视频，`实测（ACT-010 V-04，2026-11-13）`）** | 抖音图文支持把静态图、实况照片、独立视频页与配乐混排在同一条 `images[]` 作品中：单页含非空 `images[i].video.play_addr`（或 `bit_rate[]`）且 `live_photo_type != 1` 时，按**视频页**解析（按 §6.5 同等规则选无水印单文件 MP4 流，以 `images[i].url_list` 作该页封面；实测混排样本在 `aweme/detail` 与无 Cookie CDN 拉取/容器校验中一次通过）。未知形态单页不报错，保底保存 `images[i].url_list` 图片并标记 `unknownKind` |
| **外层 `aweme.video.play_addr`（必防陷阱！）** | 在图文作品上，顶层 `aweme.video.play_addr` 依然存在，但它里面装的是**背景音乐音频文件**（如 `.mp3` / 音频流），**绝不是视频画面**！解析器必须先判 `len(images) > 0` 走图文分支，绝不能先看 `video.play_addr != nil` 把图文误当成普通视频下载 |

<a id="playurl"></a>
### 6.5 视频清晰度列表与选流规则（DEC-043）

来源：`源码阅读（本机参考索引中的单个视频链接解析实现、用户视频列表解析实现、抖音签名与访问上下文实现，2026-11-12）`。

与哔哩哔哩需要单独再调一次 `playurl` 接口不同，**抖音点播作品的所有清晰度档位直接内嵌在 `aweme/detail` 与 `aweme/post` 返回的 `aweme.video` 对象中**，缓存单个已知作品无需二次请求播放接口。

`video` 下各流地址节点对比：

| 字段路径 | 是否无水印 | 说明 |
| --- | --- | --- |
| **`video.bit_rate[]`** | **无水印** | **选流主数据源**：列出当前作品全部可用转码档位（1080P/720P/540P、不同帧率与 H.264/H.265 编码变体） |
| `video.play_addr` / `video.play_addr_h264` / `video.play_addr_265` | **无水印** | 默认单路播放流；仅在极少数老旧作品 `video.bit_rate[]` 为空数组时作为兜底候选（需过滤掉混入音频后缀的异常地址） |
| `video.download_addr` | **带水印（或加密流）** | 烧录了抖音号水印的下载流（URL 常含 `/playwm/` 或 `watermark=1`）；在付费内容上甚至可能指向加密轨，**TRACE 一律排除** |
| `/aweme/v1/play/?video_id=<uri>&ratio=default&line=0&is_play_url=1&watermark=0...` | 无水印（重定向） | 取站点保存的未转码原画的 302 跳转地址。**TRACE 在画质为“最高”时先用它（[DEC-045](../decisions.md#dec-045)）**：主机 `aweme.snssdk.com`，参数 `video_id=<play_addr.uri>&ratio=default&improve_bitrate=1&watermark=0&line=0`，不带 Cookie；每个 Range 分段都先经它 302 到公共 HTTPS 443 域名。`实测（ACT-018，2026-11-13）`：一条 13.4 秒 4K60 视频的原画为 HEVC `2160×3840`、约 31.8 Mbps、53.3MB，而 `bit_rate[]` 最高档（`adapt_lowest_4_1`）只有 3.6 Mbps、6.0MB。取不到或不是合格单文件 MP4 时回退到 `bit_rate[]` 的直链选流 |

`video.bit_rate[i]` 单档结构字段：

| 字段路径 | 类型 | 含义 |
| --- | --- | --- |
| `gear_name` | `string` | 档位代号，如 `adapt_lowest_1080_1`、`normal_1080_0`、`normal_720_0`、`normal_540_0`（命名规则随服务端实验变化，**不可仅靠字符串前缀排高低，应以实际像素分辨率为准**） |
| `quality_type` | `int` | 内部画质枚举整数（同样不保证严格数值单调，仅作同分辨率同码率时的次级参考） |
| `FPS` | `int` | 帧率（如 `30`、`60`；部分档位可能缺省此键，缺省按 `30` 比较） |
| `bit_rate` | `int64` | 码率（bps） |
| `is_h265` / `is_bytevc1` | `int` | **编码标识**：`0` = H.264（AVC）；`1` = H.265（HEVC / ByteVC1）；偶见 `is_bytevc1 == 2`（ByteVC2，桌面通用播放器通常不支持，选流时应排在 AVC/HEVC 之后或过滤，[ACT-010](#act-010) 核对样本） |
| `format` | `string` | 容器格式，常规点播恒为 `"mp4"`（偶见长视频 `"dash"`） |
| `play_addr.uri` | `string` | 视频资源 ID（如 `"v0d00fg10000synthetic"`，合成示例） |
| `play_addr.url_list[]` | `string[]` | 镜像 URL 列表（通常前 2 条为 `*.douyinvod.com` / `*.zjcdn.com` 直链，最后 1 条为 `https://www.douyin.com/aweme/v1/play/?video_id=...` 回源跳转兜底） |
| `play_addr.width` / `play_addr.height` | `int` | 该档位实际像素宽高（竖屏视频如 `1080 × 1920`，横屏如 `1920 × 1080`） |
| `play_addr.data_size` | `int64` | 文件精确字节大小（可用于预估体积与校验完整性） |
| `play_addr.file_hash` / `play_addr.file_cs` | `string` | 文件摘要标识（部分响应提供，可选存在） |

**付费 / 试看与加密流识别（绝不触碰 DRM）**：
- 注意：`entertainment_product_info.market_info.limit_free.in_free == false` 在普通作品上也会出现（只表示“不在限免活动中”），**不能**当作付费信号。
- 若作品 `charge_info.is_charge_content == true` 且当前登录账号未购买（`has_paid != true`），返回流可能是仅数秒的试看片段，或 MP4 Sample Entry 为 CENC 加密（`encv` / `enca` box）。TRACE 严格遵守版权与访问控制边界：**绝不尝试解密或绕过付费限制**；若识别为未购付费加密流则直接标记 `unavailable` 并向用户说明“付费内容无法缓存”，若仅为公开试看明文流则按试看/不完整提示处理。

**按 DEC-043 的点播选流排序算法**（“先最高分辨率，同档按编码偏好，保存单文件 MP4”）：
1. **过滤无效与带水印项**：剔除 `url_list` 为空、含 `/playwm/` 或 `watermark=1`、以及已知桌面端无法硬解/软解的私有编码（如 `is_bytevc1 == 2`）。
2. **第一优先级——最高分辨率档**：计算短边像素 `short_edge = min(width, height)` 与总像素 `width * height`，将分辨率归一到标准档（如 2160P > 1440P > 1080P > 720P > 540P > 480P，防止 `1072×1920` 与 `1080×1920` 因裁剪几像素被误判跨档），取最高可用分辨率档。
3. **第二优先级——编码偏好（同分辨率档内）**：按用户在设置中配置的编码偏好（如默认优先 H.264 `is_h265 == 0` 保障任意环境兼容，或用户偏好 HEVC `is_h265 == 1`）排序。
4. **第三优先级——帧率与码率**：同分辨率、同编码下，按 `FPS` 降序、`bit_rate` 降序、`data_size` 降序决出最优档；在同一档的 `play_addr.url_list[]` 内，**优先排直链 CDN 域名，把 `/aweme/v1/play/` 排在末尾兜底**。

<a id="media-cdn"></a>
## 7. 媒体文件下载与 CDN 行为

来源：`源码阅读（本机参考索引中的抖音签名与访问上下文实现、单个视频链接解析实现、用户视频列表解析实现，2026-11-12）`、`社区资料（2026-11-12）`。

1. **音画同轨渐进式单文件 MP4（与 B 站 DASH 的核心差异）**：
   - 哔哩哔哩高清晰度点播普遍采用音视频分离的 DASH `.m4s` 双流，下载后必须经 `mp4ff` 合并音视频轨；
   - **抖音常规点播视频（含实况照片动态短片）直接返回音视频已交错封装好的完整单文件 MP4**（含视频轨 H.264/HEVC 与音频轨 AAC），下载校验字节长度一致后直接原子落盘即可播放，**无需二次混流合并**！（唯独图文作品若同时保存多张静态图、实况短片与独立配乐音频，按条目目录内多文件组织）。
2. **必要请求头与凭证隔离铁律**：
   - 必带 `User-Agent`（与 API 相同的桌面 Chrome UA）与 `Referer: https://www.douyin.com/`；
   - **禁止向媒体 CDN 域名发送用户登录 Cookie**（常见官方点播与图片 CDN 后缀：`*.douyinvod.com`、`*.zjcdn.com`、`*.douyinpic.com`、`*.byteimg.com`、`*.douyinstatic.com`、`*.bdxiguaimg.com`、`*.snssdk.com`）。CDN 鉴权完全依靠 URL 查询串或路径中的签名参数，不带 Cookie 既防止会话凭证跨边缘节点泄露，也避免异常会话绑定限速。
   - 特例：极少数作品标记 `video.is_need_set_cookie == true` 且仅能走 `www.douyin.com/aweme/v1/play/` 302 跳转时，只有发往 `www.douyin.com` 的首跳属于主站域，302 落到 CDN 域名后的拉流请求仍**绝不转发 Cookie**。
3. **`Range` 分段拉取与断点续传**：
   - 抖音点播 CDN（`*.douyinvod.com` 等）标准支持 `Range: bytes=<start>-<end>` 并返回 `HTTP 206 Partial Content` 与 `Content-Range`（`源码阅读（本机参考索引中的用户视频列表解析实现、抖音签名与访问上下文实现，2026-11-12）`；具体 8 MiB 分段表现将在 [ACT-010](#act-010) 实测核对）。
   - TRACE 可直接复用[媒体与存储选型 §5](2026-11-05-media.md#download)的分段 `Range` 下载器与 `.part` 续传机制，并根据 `play_addr.data_size` 或首个 `206` 的 `Content-Range` 总长度做严格完整性校验（防提前断连残缺文件入库）。
4. **CDN 签名地址有效期**：
   - 点播直链查询串通常携带 `x-expires=<Unix秒>` 与 `x-signature=<签名>`（或路径十六进制时间戳），实测观察有效期通常在 **1 小时至数小时**量级（各边缘集群最小 TTL 记为 `unknown`，以 URL 内 `x-expires` 实时判断为准）。
   - **安全与日志红线**：所有媒体流与图片签名 URL **严禁写入持久化 `item.json` 或明文日志**（日志只记主机名、偏移字节、HTTP 状态码与重试次数）。下载途中若所有镜像均返回 `403`（链接过期），单任务最多重新调用 1 次 `aweme/detail` 刷新直链后从已确认字节处续传。
5. **已知 CDN 异常表现与镜像轮换**：
   - **冷存储慢节点与 PCDN 陷阱**：主机名含 `-mc-cold`（如 `v3-dy-o-mc-cold.douyinvod.com`）的冷备节点常出现连接不断但吞吐跌至 `< 20 KB/s` 的慢速滴流；而请求 `www.douyin.com/aweme/v1/play/` 被 302 导向第三方随机域名（如 `*.qtaeixd.com` 或带非 443 高端口的 PCDN）常出现 TLS 握手超时或 `403`。因此必须**优先使用 `url_list` 中的 `*.douyinvod.com` / `*.zjcdn.com` HTTPS 443 直链**，在分段吞吐过低、超时或 `403`/`5xx` 时自动轮换同档下一个镜像 URL。
   - **200 HTML/JSON 假成功拦截**：下载器收到响应时必须校验 `Content-Type`（拒绝 `text/html`、`application/json`、`text/plain` 伪装的错误页当成 MP4/图片写入磁盘）。

<a id="live"></a>
## 8. 直播协议

来源：`源码阅读（本机参考索引中的抖音直播录制实现、多平台无界面录制实现、抖音签名与访问上下文实现、多站点更新订阅实现，2026-11-12）`、`社区资料（2026-11-12）`。录制与封装技术选型详见同批调研文档[抖音技术选型 §2–§3](2026-11-12-douyin.md#live-route)。

### 8.1 直播间信息与取流接口对比

| 接口 | 方法与 URL | 核心参数 | 签名与 Cookie 要求 | 适用场景与特点 |
| --- | --- | --- | --- | --- |
| **Web 进入房间接口（主选）** | `GET https://live.douyin.com/webcast/room/web/enter/` | `aid=6383`、`app_name=douyin_web`、`live_id=1`、`device_platform=web`、`language=zh-CN`、`enter_from=web_live`、`cookie_enabled=true`、`screen_width=1920`、`screen_height=1080`、`browser_language=zh-CN`、`browser_platform=Win32`、`browser_name=Chrome`、`browser_version=<同UA>`、`web_rid=<网页直播间号>`、`room_id_str=`（可空）、`enter_source=`、`is_need_double_stream=false` | **必带 `a_bogus`** + `Referer: https://live.douyin.com/` + **Cookie（含 `ttwid` 与登录态）** | 已知 `web_rid` 时最标准的轻量 JSON 接口，一次返回房间状态、主播 `sec_uid` 与最新 `stream_data`；注意**响应不回显 `web_rid`**，调用方需自行保留传入的 `web_rid` |
| **App 分享回流接口（换号与后备）** | `GET https://webcast.amemv.com/webcast/room/reflow/info/` | `type_id=0`、`live_id=1`、`room_id=<单场room_id>`、`sec_user_id=<主播sec_uid，可选>`、`version_code=99.99.99`、`app_id=1128` | 参考实现中**无需 `a_bogus`**（带常规 UA 与登录 Cookie 即可） | 1) 解析 App 口令短链跳出的 `reflow/<room_id>`；2) 从 `user/profile/other` 拿到开播中的 `room_id` 后，**换出稳定的 `data.room.owner.web_rid`** 与流地址！注意此接口中主播信息在 `data.room.owner`，而顶层 `data.user` 是当前请求者会话 |
| **直播间网页 HTML 首屏（仅兜底）** | `GET https://live.douyin.com/<web_rid>` | 路径直传 `web_rid` | 无登录 Cookie 时常遇 `__ac_nonce` + `__ac_signature` 双跳挑战 | 从 HTML 内 `<script>self.__pace_f.push([1,"c:..."])</script>` 提取 `state.roomStore.roomInfo`；体积大且依赖前端 RSC 序列化格式，仅在 JSON 接口改版应急时参考，常规不采用 |

### 8.2 开播状态 `status` 与外层状态枚举

在 `web/enter` 中，房间对象位于 `data.data[0]`（主播对象位于 `data.user`）；在 `reflow/info` 中，房间对象位于 `data.room`（主播对象位于 `data.room.owner`）：

| 字段位置 | 取值 | 含义与 TRACE 处理 |
| --- | --- | --- |
| **`room.status`**（主判据：`data.data[0].status` 或 `data.room.status`） | **`2`** | **正在直播（Live）**——唯一应启动录制并解析 `stream_url` 的状态 |
| `room.status` | **`4`** | **已下播 / 本场直播已结束（Offline）**——停止录制或标记未开播 |
| `room.status` | `1` / `3` | `1` = 准备中 / 初始化，`3` = 暂停中（SDK 枚举）；均无稳定可录画面流，按未正式开播等待处理 |
| 外层 `data.room_status`（仅 `web/enter`） | `0` / `1` / `2` | `0` = 视频直播中，`1` = 语音/电台直播模式，`2` = 未开播；**判别是否开播一律以里层 `data.data[0].status == 2` 为准**。**必防陷阱（ACT-010 实测暴露并修复）**：外层 `data.enter_mode == 1` 是进房路由标记而非语音电台判据（网页直链进房的常规视频直播间也会出现 `enter_mode == 1`），且部分视频推流模式可能带非零 `live_room_mode`；因此**绝不能凭 `data.enter_mode == 1` 判为语音直播**，应仅在外层 `data.room_status == 1`、里层 `live_type_audio == true`，或 `stream_url` 全部有效流均为 `ao`/`only_audio=1`（无任何视频流）时才判为纯语音直播 |
| 顶层 `status_code == 4001038` | `data.prompts: "该内容暂时无法无法查看"` | `web_rid` 不存在或直播间已封禁不可看 → `unavailable` |
| 顶层 `status_code == 10011` | `"Request params error"` | `reflow/info` 传入了不存在的 `room_id` → `unavailable` |
| 顶层 `status_code == 30003` | — | 场次已结束 → 按下播处理 |

### 8.3 新旧流结构对比、清晰度映射与解析优先级

当 `room.status == 2` 时，`room.stream_url` 同时存在新旧两代字段（`源码阅读（本机参考索引中的抖音直播录制实现、多平台无界面录制实现，2026-11-12）`）：

1. **优先级 1（现代主结构）：`room.stream_url.live_core_sdk_data.pull_data`**
   - `pull_data.options.qualities[]`：清晰度元数据数组，每项含 `name`（如 `"原画"`、`"蓝光"`、`"超清"`、`"高清"`、`"标清"`）、`sdk_key`（如 `"origin"`、`"uhd"`、`"hd"`、`"sd"`、`"ld"`）、`level`（整数等级，越大越高）、`v_codec`、`resolution`、`v_bit_rate`、`disable`。
   - **`pull_data.stream_data`**：**第 1 层转义 JSON 字符串**（必须对字符串再做一次 `json.Unmarshal`！）。反序列化后的顶层 `data` 是一个以 `sdk_key` 为键的对象：
     - `data.<sdk_key>.main.flv`：该档位的 HTTP-FLV 拉流地址（`https://pull-flv-*.douyincdn.com/...flv?...`）；
     - `data.<sdk_key>.main.hls`：该档位的 HLS 播放列表地址（`https://pull-hls-*.douyincdn.com/.../index.m3u8?...` 或 `playlist.m3u8`）；
     - `data.<sdk_key>.main.cmaf` / `dash` / `lls`：扩展协议槽位（多数常规直播间为空字符串 `""`）；
     - **`data.<sdk_key>.main.sdk_params`**：**第 2 层转义 JSON 字符串**，解析后包含 `{"VCodec":"h264"|"h265","vbitrate":8000000,"resolution":"1920x1080","gop":2,...}`，**可在真正建立拉流连接前直接预判视频编码是 H.264 还是 H.265**！
   - 双屏/连麦多流补充：部分双屏直播间在 `room.stream_url.pull_datas`（字典）下按子流 ID 提供同样结构的 `stream_data`，主画面 `pull_data.stream_data` 为空时可读取 `pull_datas` 首个有效项。
2. **优先级 2（旧版兼容回退）：`room.stream_url.flv_pull_url` 与 `hls_pull_url_map`**
   - 当极少数房间（或精简回放样本）不含 `live_core_sdk_data.pull_data.stream_data` 时，直接读取字典 `stream_url.flv_pull_url`（键为 `ORIGIN`、`FULL_HD1`、`HD1`、`SD1`、`SD2`）与 `stream_url.hls_pull_url_map`（同键名）以及默认单路 `stream_url.hls_pull_url`。

清晰度档位标识映射表（按画质从高到低）：

| 新版 `stream_data` `sdk_key` | 中文档名 | 旧版 `flv_pull_url` / `hls_pull_url_map` 键 | 说明与特殊陷阱 |
| --- | --- | --- | --- |
| `origin` | 原画 | `ORIGIN`（或部分房间映射在 `FULL_HD1`） | 主播推流最高档（可能不重编码直出，码率与帧率最高） |
| （社区衍生：`real_origin`） | 真原画（社区称呼） | — | 社区部分录制工具观察到 `ao`（纯音频流）URL 去掉查询参数 `&only_audio=1` 后可能拿到未二压源流（`源码阅读（本机参考索引中的抖音直播录制实现，2026-11-12）`）；但该拼接非官方契约且随时可能 403 失效（可用性 `unknown`，TRACE 默认稳健使用标准 `origin` 档，**同时选流时必须检查并剔除任何带 `only_audio=1` 的地址防止误录纯音频**） |
| `uhd` | 蓝光 | `FULL_HD1` | 通常为 1080P 高码率转码档 |
| `hd` | 超清 | `HD1` | 通常为 720P/1080P 标准码率档 |
| `sd` | 高清 | `SD1` | 通常为 540P/720P 档 |
| `ld` / `md` | 标清 / 流畅 | `SD2` | 低码率档 |
| `ao` | 纯音频 | — | URL 含 `only_audio=1`，视频录制选流时**必须排除** |

### 8.4 FLV 与 HLS 档位覆盖、分片容器与 HEVC 编码封装

来源：`源码阅读（本机参考索引中的抖音直播录制实现、多平台无界面录制实现，2026-11-12）`、`社区资料（2026-11-12）`。

1. **FLV 与 HLS 档位可得性**：
   - 在新版 `stream_data.data.<sdk_key>.main` 中，多数常规直播间在 `origin`、`uhd`、`hd`、`sd`、`ld` 下同时给出非空 `flv` 与 `hls` 地址；但社区与多平台录制实现均记录到**部分高码率/PK/特殊推流直播间的 `origin` 原画仅提供 `main.flv`（`main.hls` 为空字符串），或旧版 `stream_url` 中 `flv_pull_url` 档位比 `hls_pull_url_map` 更全**（具体比例将在 [ACT-010](#act-010) 实测核实）。因此按 DEC-043 与[抖音技术选型 §2](2026-11-12-douyin.md#live-route)，TRACE 以 **HTTP-FLV 为首选录制路线**、**HLS 为无 FLV 或传输受阻时的后备路线**。
2. **抖音直播 HLS 分片格式是 MPEG-TS（`.ts`）还是 fMP4（`.m4s`）**：
   - 与哔哩哔哩直播提供 `#EXT-X-MAP` + `.m4s`（fMP4）不同，**抖音直播 `main.hls`（及 `hls_pull_url_map`）返回的 `.m3u8` 播放列表普遍采用传统 MPEG-TS（`.ts`）分片**（`stream_data` 虽预留 `main.cmaf` 键但通常为空；真实直播间是否已有部分节点切到 fMP4 列入 [ACT-010](#act-010) 核实）。这意味着若启用 HLS 后备，录制器不能只认 `.m4s`，需要具备 MPEG-TS 解复用能力（见[抖音技术选型 §3](2026-11-12-douyin.md#flv-ts-mp4ff)）。
3. **原画流视频编码（H.264 vs H.265）与 HTTP-FLV 中的 HEVC 载荷格式**：
   - **编码分布**：转码档（`uhd`/`hd`/`sd`/`ld`）多为 H.264（AVC），而 **`origin` 原画流（尤其是 1080P60、2K/4K 游戏或手机高画质推流）大量采用 H.265（HEVC / ByteVC1）**。选流阶段即可从 `main.sdk_params` 的 `"VCodec"` 字段（`"h264"` 或 `"h265"` / `"bytevc1"`）以及 `stream_url.extra` 提前获知，落流时再以首个视频配置包为准。
   - **HTTP-FLV 承载 HEVC 的两种封装并存（录制器必须双兼容！）**：
     - **国内 CDN 传统私有扩展 `CodecID = 12`（`0x0C`）**：标准 Adobe FLV 规范只定义了 `CodecID = 7`（AVC），国内主流直播 CDN（含字节系 `douyincdn.com`）长期沿用非标准 `CodecID = 12`（关键帧首字节 `(1<<4)|12 = 0x1C`，非关键帧 `0x2C`），后续字节布局与 AVC 类似（`AVCPacketType`: `0` = `HEVCDecoderConfigurationRecord` 序列头、`1` = NALU、`2` = EOS，紧跟 24-bit `CompositionTime` 偏移）；
     - **Enhanced RTMP/FLV v1/v2 标准扩展（FourCC `hvc1`）**：新版 OBS（29+）推流与部分新边缘节点采用 `IsExHeader = 1`（首字节最高位 `0x80` 置位，例如关键帧 SequenceStart 为 `0x90`、CodedFrames 为 `0x91`/`0x93`），紧跟 4 字节 ASCII FourCC `"hvc1"`。
     - 本机参考索引中的无界面录制实现已专门在原生 FLV 解析层同时处理了 `CodecID = 7`（AVC）、`CodecID = 12`（国内私有 HEVC）与 Enhanced FLV FourCC `hvc1`（`源码阅读（本机参考索引中的多平台无界面录制实现，2026-11-12）`）；TRACE 的 FLV 解析器必须同样同时兼容这两种 HEVC 封装，绝不能假设只有 AVC 或只有其中一种 HEVC 头。

### 8.5 直播流有效期、断流表现与重连策略

- **拉流请求头**：`User-Agent`（固定桌面 Chrome UA）、`Referer: https://live.douyin.com/`、`Origin: https://live.douyin.com`，**不带登录 Cookie**（直播 CDN 常见 `*.douyincdn.com`、`*.bytefcdnrd.com` 等；首个拉流地址必须在允许域名内；之后的 301/302 跳转会落到白名单外的调度域名（ACT-012 实测），只跟随 HTTPS、443 端口的公共域名，拒绝 IP、本地主机名与非常规端口 PCDN）。
- **流地址有效期与严禁复用旧 URL**：拉流 URL 含 `expire=` / `sign=` 等边缘鉴权参数（有效期通常数分钟至数十分钟，精确下限 `unknown`）。连接超时建议 **5～10 秒**，读数据空闲超时 **10～15 秒**。一旦发生 EOF、连接重置、读超时或 HTTP `403`/`404`，**绝不能拿旧流 URL 原地重连**——必须退回重新调用一次 `webcast/room/web/enter/`：
  1. 若返回 `room.status == 4`，说明主播正常下播，立即收尾封装当前分段并结束本场录制任务；
  2. 若返回 `room.status == 2`，提取最新签发的流 URL 开启新分段或续接（当检测到 SPS/PPS/VPS 编码参数变化或时间戳发生大幅跳变时，立即封口当前 `.mp4.part` 并切入下一个 `part-%03d.mp4` 分段，防止花屏或音画不同步）。

### 8.6 多主播开播状态查询限制与节奏

- **是否存在按用户或固定房间号的批量开播查询接口**：
  - `GET https://webcast.amemv.com/webcast/room/check_alive/?aid=6383&device_platform=web&room_ids=<id1>,<id2>,...` 虽然支持一次传多个 ID（返回 `data: [{alive: true|false, room_id: 7400000000000000001}]`），但存在**三个致命前提限制**（`源码阅读（本机参考索引中的抖音签名与访问上下文实现，2026-11-12）`）：
    1. 入参 `room_ids` **只接受单场开播的 19 位 `room_id`，不接受 `sec_uid`，也不接受固定的 `web_rid`**！因此它**只能用来批量巡检“已知正在开播的场次是否还没下播”**，无法用来发现一个离线主播何时新开了一场直播（因为新开播会换一个全新的 `room_id`）；
    2. 响应中 `room_id` 是 **64 位 JSON 整数且没有 `room_id_str` 伴随字段**（必须用 `UseNumber()` 解析）；
    3. 不存在的 `room_id` 会被直接从 `data` 数组省略（不能按请求下标一一对应，必须按 `room_id` 键值映射）。
  - **结论**：截至调研日，公开协议中**不存在**像 B 站 `get_status_info_by_uids` 那样按 `sec_uid` 或 `web_rid` 批量查询多个未开播主播是否新开播的 Web 接口。
- **逐个查询的最轻量方案与节奏控制**：
  - 已保存 `web_rid` 的订户：逐个调 `live.douyin.com/webcast/room/web/enter/?web_rid=<web_rid>`（开播时一次请求同时拿到流地址，无需再调第二个接口）；
  - 尚未拿到 `web_rid` 的订户：若该轮本就要做作品更新检查（`aweme/post` 第 1 页），直接顺带读取返回的 `author.room_id > 0`（0 额外请求！）；若仅单独巡检开播状态，则调 `user/profile/other/?sec_user_id=<sec_uid>` 看 `user.room_id > 0`。
  - 轮询节奏建议：仅对用户**显式勾选“自动录制/开播提醒”**的订户开启定时直播巡检，单主播巡检间隔建议 **>= 60～120 秒**（带随机抖动），相邻两个主播请求之间至少间隔 **2～3 秒**，遵守站点级每小时请求上限（见[第 9.3 节](#pacing)）。

### 8.7 直播间未登录访客态与已登录态差异

- 未登录但携带有效 `ttwid` + `a_bogus` 时，部分普通公开直播间的 `web/enter` 仍可返回 `stream_data`；但遇到开启登录可见门槛的直播间、高频巡检或网关策略收紧时，访客更容易收到空房间、清晰度受限或滑块验证挑战，且观众榜等字段会被脱敏。TRACE 按 DEC-043 统一在已登录前提下调用直播接口，不在未登录状态下发起直播轮询或录制。

<a id="errors"></a>
## 9. 返回体、错误码、风控识别与请求节奏

来源：`源码阅读（本机参考索引中的抖音签名与访问上下文实现、单个视频链接解析实现、用户视频列表解析实现，2026-11-12）`、`社区资料（2026-11-12）`。

### 9.1 响应信封、常见 `status_code` 与 TRACE 错误分类映射

主站 `/aweme/v1/web/*` 常规返回顶层 `{"status_code": 0, "status_msg": "", ...}`；直播 `/webcast/*` 常规返回 `{"status_code": 0, "data": {...}, "extra": {...}}`。`status_code == 0` 只是必要条件而非充分条件（必须同时通过 §9.2 的空壳与风控载荷检查）。

对齐仓库现有站点错误分类体系（`auth_required`、`risk_control`、`rate_limited`、`blocked`、`unavailable`、`forbidden`、`bad_request`、`schema_changed`、`network`）：

| HTTP 状态 / `status_code` / 响应特征 | 含义 | TRACE 错误分类 | 客户端动作 |
| --- | --- | --- | --- |
| HTTP 200，`status_code == 0`，业务载荷结构完整 | 正常成功 | — | 正常解析 |
| HTTP 200，**0 字节空响应体**（`Content-Length: 0`） | 签名不匹配、UA 绑定不一致或反爬静默丢包 | `risk_control` | **立即停止，不自动重试** |
| HTTP 200，非 JSON 文本（如 HTML `<title>验证码中间页</title>` 或含 `verify_page`） | 滑块/图形验证码拦截页 | `risk_control` | **立即停止，绝不尝试解算或绕过验证码** |
| HTTP 200，JSON 含非空 `verify_center_decision_conf`、`verify_data`、`captcha`，或 `status_code` 为 `10000` / `10001` | 验证码中心挑战信封（注：直播 `web/enter` 正常响应里 `data.shark_decision_conf` 常为空字符串 `""`，**只有非空挑战配置才算风控**！） | `risk_control` | **立即停止，不自动重试**，提示用户稍后再试或在浏览器正常访问确认账号状态 |
| HTTP 403，响应体含 `Blocked by ArgusSecurityPlugin`（如 `Uifid Not Found` / `Signature Not Found` / `Sign Invalid`） | `uifid` 或 `x-secsdk-web-signature` 网关校验未通过 | `risk_control` | **立即停止，绝不盲目重试**（属确定性签名/Cookie 上下文门禁） |
| HTTP 403（WAF/空体）、HTTP 429、HTTP 444（服务端直接断连） | 频率限制或边缘防火墙拦截 | `rate_limited` | **立即停止当前任务与后台轮询，进入退避** |
| `status_code == 2483` 或 `status_msg` 明确提示未登录；`profile/self` 返回空 `user`；`aweme/post` 在 `max_cursor > 0` 返回 17 字节 `{"status_code":0}` | 未登录或粘贴的 `sessionid` 已过期失效 | `auth_required` | 停止请求，提示用户“抖音登录状态已失效，请在设置页重新粘贴 Cookie” |
| `status_code` 为 `8`、`2154`、`2156` | 网关风控拒绝或登录/权限异常（`8` 在部分接口伴随未登录或访问受限提示，结合 `status_msg` 细分） | 含未登录文案归 `auth_required`，其余归 `risk_control` | 停止自动重试 |
| `aweme/detail` 中 `aweme_detail == null` **且** `filter_detail.filter_reason` 非空；或 `status.is_delete == true`；直播 `status_code == 4001038` / `10011`；`status_code == 2096`（私密账号不可见） | 作品已删除、审核中、私密不可见或直播间不存在 | `unavailable`（私密无权限可用 `forbidden` / `unavailable` 并带具体中文原因） | 正常向用户展示不可得原因，**不触发全局风控熔断** |
| `aweme/detail` 中 `aweme_detail == null` **且无 `filter_detail`**；`profile/other` 中 `user == null` 且 `status_code == 0` | 载荷被风控静默抽空 | `risk_control` | 立即停止，不把风控抽空误判为作品删除或改版 |
| `status_code == 2` / `5`（参数错误）；已知不支持的抖音子路径（商城/合集/话题等） | 请求参数异常或链接类型暂不支持 | `bad_request`（链接不支持附 `Unsupported=true`） | 本地提示，不重试 |
| 短链跳转指向非白名单域名、`http://`、私有 IP 或超过 5 跳 | 跳转越界拦截 | `blocked` | 立即中止跳转 |
| HTTP 200 JSON 合法但核心必需字段路径移位/类型不符 | 站点协议改版 | `schema_changed` | 报结构变化，不重试 |
| DNS 失败、TCP/TLS 超时、连接重置、HTTP 5xx | 瞬态网络故障 | `network` | API 请求仅由用户手动重试；媒体文件按 Range 规则有限次续传 |

### 9.2 核心防误判真值表：“真实 0 作品 / 翻到底” vs “签名失效 / 风控空响应”

在订阅更新检查与用户主页浏览中，**绝对不能把风控空包、签名失效或 Cookie 掉线当成“该用户有 0 个作品”或“已经翻到最后一页”**（否则会导致订阅漏检或错误清空状态）。严格判定真值表如下（`源码阅读（本机参考索引中的抖音签名与访问上下文实现、用户视频列表解析实现，2026-11-12）`）：

| `aweme/post` 响应形态 | `max_cursor` 入参 | 真实原因 | 必须执行的客户端判定 |
| --- | --- | --- | --- |
| HTTP 200，0 字节空 body | 任意 | 反爬 / 签名错误静默丢包 | **判为 `risk_control`（报错停止）**，绝不当空列表！ |
| HTTP 200，仅 `{"status_code":0}`（**缺少 `aweme_list` 键或缺少 `has_more` 键**） | `max_cursor > 0`（第 2 页起） | **Cookie 未登录或已失效触发的访客翻页墙**（最典型陷阱！） | **判为 `auth_required` / `risk_control`**，保留前序已拿到的页，绝不当作“翻到底”！ |
| HTTP 200，仅 `{"status_code":0}` 或 `"aweme_list": null` | `max_cursor == 0`（第 1 页） | 载荷被风控抽空或签名降级 | **判为 `risk_control`**，绝不当 0 作品！ |
| HTTP 200，`status_code == 0`，显式包含数组 `"aweme_list": []` **且** `"has_more": 1` | 任意 | 服务端本批过滤（如本页全是不可见条目）或风控异常 | 若 `next_max_cursor` 有效且异于入参最多允许跟进 1 次，若连续空页或游标不变则按异常中止，不标为正常完结 |
| HTTP 200，`status_code == 0`，**显式包含数组 `"aweme_list": []` 且显式 `"has_more": 0`（或 `false`）**，无任何 `verify_*` 字段 | `max_cursor > 0` | **真正翻到最后一页** | **正常结束分页**（`has_more = false`） |
| HTTP 200，`status_code == 0`，**显式包含数组 `"aweme_list": []` 且显式 `"has_more": 0`**，无任何 `verify_*` 字段 | `max_cursor == 0`（第 1 页） | **交叉校验 `user.aweme_count`**（若已读过 `profile/other`）：<br>• 若 `user.secret == 1` → 私密账号 `forbidden`；<br>• 若 `user.aweme_count == 0` → **用户确实公开 0 作品**；<br>• 若 `user.aweme_count > 5` 却在第 1 页拿到 0 条 → **高度疑似风控静默限流或仅好友可见**，在订阅检查中记为本轮结果不完整，不覆盖已有基线！ | 按左列交叉校验结果分支处理 |

<a id="pacing"></a>
### 9.3 面向 TRACE 桌面端的请求节奏与配额建议（NFR-NET-002）

按[需求](../requirements.md)（NFR-NET-002 限流与配额按站点独立运行，互不挤占 B 站额度）及抖音比 B 站更敏感的网关特征，建议 TRACE 抖音客户端执行以下保守默认值（供 Agent A 在 M3 计划中拍板）：

| 维度 | 建议默认值 | 依据与说明 |
| --- | --- | --- |
| **抖音全局 API 最小请求间隔** | **1000 ms + 0～300 ms 随机抖动**（单站点串行 `concurrency = 1`） | 抖音 `aweme/post` 与 `web/enter` 对亚秒级突发比 B 站（500 ms）更敏感；桌面个人工具 1 秒 1 次体感完全流畅且显著降低滑块率 |
| **单次 API 超时与响应体上限** | 超时 **15 秒**；JSON 响应体上限 **4 MiB** | 与 B 站适配器安全边界一致，防止异常流式响应撑爆内存 |
| **短链展开跳数与超时** | 最多 **5 跳**，单跳超时 **10 秒** | 覆盖 `v.douyin.com` → `iesdouyin.com` → `douyin.com` 典型链路 |
| **用户主页翻页策略** | 仅由用户滚动到底部手动触发，**禁止后台自动预取下一页**；每页 `count=18` | 避免打开一个多产作者主页瞬间连发数十页触发验证码 |
| **定时订阅检查每轮上限** | 每位订户常规只读 `aweme/post` **第 1 页**（追平基线即停；首次或积压最多读 **2 页**）；每轮最多检查 **20 位**抖音订户 | 配合 `is_top` 跳过规则，99% 日常检查每位订户仅需 **1 次**请求（若未单独开直播巡检，连直播状态也可顺带从 `author.room_id` 感知） |
| **定时直播开播巡检节奏** | 仅巡检开启自动录制的订户，单订户间隔 **>= 90 秒**（带 ±15 秒随机抖动），订户间至少间隔 **2.5 秒** | 因无按 `sec_uid`/`web_rid` 批量接口，必须限制并发与频率 |
| **抖音单站点每小时自动请求硬上限** | 后台自动任务（订阅检查 + 直播巡检）合计 **<= 120 次 API 请求 / 小时**（媒体流与直播分片传输不计入 API 配额，由下载并发上限 `2` 独立控制） | 触顶后推迟剩余后台巡检到下一小时窗口，不影响用户前台手动点击 |
| **风控熔断与退避** | 任一后台或前台请求命中 `risk_control`、`rate_limited` 或 `auth_required` 时，**抖音站点的全部后台自动任务立即熔断暂停**（`auth_required` 需用户更新 Cookie 后恢复；风控/限流按指数退避如 `15m → 30m → 60m` 冷却，期间绝不自动连撞验证码） | 严格遵守[站点规则](../dev-rules/sources-and-safety.md#site-rules)：遇挑战或限流立即停止 |

<a id="act-010"></a>
## 10. 待真实验证清单与 ACT-010 最小额度建议

### 10.1 静态调研无法 100% 拍板的待验证项汇总（`unknown` 清单）

以下项目在公开资料与本机参考索引源码中存在多分支演进或缺乏最新线上回执，必须在 [ACT-010](../../USER-ACTIONS.md#act-010) 授权下用小额真实请求最终锁定：

| 编号 | 待验证问题 | 当前静态认知与备选分支 | 对应章节 |
| --- | --- | --- | --- |
| **V-01** | `/aweme/v1/web/user/profile/self/` 在“完全不带 Cookie”与“带有效粘贴 Cookie”下的精确返回码与字段差异 | 源码确认已登录返回 `status_code=0` 与完整 `user.{uid, sec_uid, nickname, avatar_thumb}`；未登录精确 `status_code`（`2483`、`8` 还是 `user: null`）需 1 次无 Cookie + 1 次有 Cookie 对照钉死 | [§3.2](#auth) |
| **V-02** | 纯 Go 实现的 `a_bogus`（`dhzx` 盐值 vs `cus` 盐值）及 `ArgusSecurityPlugin`（`x-secsdk-web-signature` + `uifid`）在真实粘贴 Cookie 下的通过情况 | 参考实现显示 2026-09 起 `aweme/detail` 与 `aweme/post` 在含 `uifid` 时校验 `x-secsdk-web-signature`，且剥离 `bd_ticket_guard_*` 动态 Cookie 更稳；需实测确认正式 Go 客户端构造的请求在 `detail`、`profile/other`、`post` 第 1～2 页与 `web/enter` 上一次通过 | [§2.2](#signing)、[§3.1](#auth) |
| **V-03** | `aweme/post` 第 1 页含置顶作品（`is_top=1`）时 `max_cursor` 翻第 2 页的衔接与去重表现 | 夹具证实 `is_top=1` 排第 1 页最前且 `create_time` 可早于后续常规作品；需在真实含置顶账号上验证第 1→2 页无漏项 | [§6.3](#posts) |
| **V-04** | 图文作品（`images[]`）与实况照片（`live_photo_type=1`、`images[].video.play_addr`）的实际响应格式与图片编码（WebP / JPEG / HEIC）；混排图文里视频页的区分字段、时长与编码（用户 2026-11-13 提供了混排样本） | 夹具证实 `aweme_type=68`、`live_photo_type=1, clip_type=5`；需实测 1 条图文/实况作品的原图与短片直链 `Content-Type` 及无水印表现 | [§6.4](#images) |
| **V-05** | 点播 CDN 直链（`*.douyinvod.com`）对 TRACE 8 MiB 闭区间 `Range` 分段拉取、不带 Cookie 请求头与 `is_bytevc1` 编码字段的真实响应 | 预期返回 `HTTP 206` 单文件渐进式 MP4（音画同轨），需实测 1 条短视频确认 0 断连且本地播放器可直接解码播放 | [§6.5](#playurl)、[§7](#media-cdn) |
| **V-06** | 未开播状态下 `webcast/room/reflow/info/?room_id=2&sec_user_id=<sec_uid>` 能否反查出 `owner.web_rid`，还是必须等主播开播时从 `profile/other` 的 `room_id` 换取 | 录制参考实现存在 `room_id=2` 写法，但回放夹具对无效 `room_id` 返回 `status_code=10011`；需对 1 个已知主播实测确认，决定离线订户首次能否换出 `web_rid` | [§5.2](#ids)、[§8.1](#live) |
| **V-08** | 同一直播间的两种数字（用户给的样本：主播的数字 `short_id` 与另一个会变的数字）：`web/enter` 是否都接受、各自返回的主播 `sec_uid`、本场 `room_id` 与响应里的房间号字段是否一致；`profile/other` 的 `short_id` 是否等于前者 | 用户 2026-11-13 观察：`short_id` 形式不变、另一种会变。决定 `webRid` 缓存优先存哪一种 | §5.1、§5.2 |
| **V-07** | 真实开播直播间的 `stream_data` 中 `origin` 原画是否同时给 FLV 与 HLS、HLS 分片究竟是 `.ts` 还是 `.m4s`、以及原画 HTTP-FLV 若为 HEVC 用的是 `CodecID=12` 还是 Enhanced FLV `hvc1` | 预期 HTTP-FLV 必有 `origin`、HLS 多为 `.ts`、HEVC 可能为 `CodecID=12` 或 `hvc1`；需对 1 个真实开播房间短录约 15～20 秒核实流头与收尾 MP4 可播性 | [§8.3](#live)–[§8.4](#live) |

### 10.2 供 Agent A 写入 `ACT-010` 的最小真实站点检查程序与额度建议

仿照 `USER-ACTIONS.md` 中 [ACT-004](../../USER-ACTIONS.md#act-004)、[ACT-005](../../USER-ACTIONS.md#act-005)、[ACT-007](../../USER-ACTIONS.md#act-007) 的有界检查体例，建议 Agent A 在用户提供测试 Cookie 与检查对象后，按以下最小额度表申请并执行一次有界验证（全程串行、间隔 >= 1.2 秒）：

| 步骤 | 接口 / 目标域名 | 用途（对应待验证项） | 最多请求次数 | 体积 / 时长上限 |
| --- | --- | --- | --- | --- |
| 1. 登录对照校验 | `www.douyin.com/aweme/v1/web/user/profile/self/` | 先不带 Cookie 发 1 次确认未登录拒绝特征，再带测试 Cookie 发 1 次确认 `status_code=0` 且拿到 `uid`/`sec_uid`/昵称/头像（V-01、V-02） | `2` 次（访客 1 + 登录 1） | JSON <= 256 KiB |
| 2. 短链展开（可选，若用户给的是 `v.douyin.com` 短链） | `v.douyin.com` → `iesdouyin.com` / `douyin.com` | 逐跳读取 `Location`（拿到 ID 即停，不拉 HTML 正文） | `3` 跳 | 仅响应头 / <= 64 KiB |
| 3. 用户资料与作品列表分页 | `www.douyin.com/aweme/v1/web/user/profile/other/`、`aweme/post/` | 读取测试目标作者资料 1 次 + 作品列表第 1、2 页各 1 次（验证 `a_bogus`、`uifid`、`is_top` 排序与 `max_cursor` 翻页，V-02、V-03） | `3` 次（资料 1 + 列表 2） | JSON 合计 <= 2 MiB |
| 4. 单作品详情（视频 + 图文/实况） | `www.douyin.com/aweme/v1/web/aweme/detail/` | 读取用户指定的 1 条短视频详情与 1 条图文（含实况更佳）详情（若步骤 3 第 1 页已含同构条目可省去 1 次，最多 2 次，V-04、V-05） | `2` 次 | JSON 合计 <= 1 MiB |
| 5. 点播媒体与原图下载 | `*.douyinvod.com` / `*.zjcdn.com`、`*.douyinpic.com` / `*.byteimg.com` | 对步骤 4 的短视频按 8 MiB `Range` 分段下载单文件 MP4（选 < 30 秒样本），对图文下载 1～2 张无水印原图（及 1 段实况短片若存在），不带 Cookie，校验文件头、时长与 SHA-256（V-04、V-05） | 媒体请求 <= `8` 次 | 合计 <= `40 MiB` |
| 6. 直播间状态、换号与短录验证 | `live.douyin.com/webcast/room/web/enter/`、`webcast.amemv.com/webcast/room/reflow/info/`、`*.douyincdn.com` | 查用户指定的 1 个直播间（及 1 次 `reflow/info` 换号核对 V-06）；若处于开播状态（`status=2`），拉取 HTTP-FLV 流（必要时附 1 次 `.m3u8` 文本检视分片后缀，不拉双份视频流）录制 **15～20 秒**后主动停止，收尾转渐进式 MP4 并校验 `moov`、时长与编码头（V-06、V-07） | API <= `3` 次；直播拉流连接 <= `2` 次 | 录制时长 <= `25 秒`，流量 <= `50 MiB` |

- **汇总硬上限**：主站与直播 JSON API 合计 **<= 13 次请求**；点播与图片 CDN 请求 **<= 8 次（<= 40 MiB）**；直播流连接 **<= 2 次（<= 25 秒、<= 50 MiB）**；单轮总耗时 <= 3 分钟。
- **立即停止条件（零容忍熔断）**：任何一步遇到 HTTP `403`（含 `ArgusSecurityPlugin`）、HTTP `429` / `444`、0 字节空响应、HTML 验证码页、非空 `verify_center_decision_conf` / 滑块挑战，或已登录请求返回未登录错误码时，**立即停止后续全部请求，不做任何自动重试，严禁触碰或解算任何验证码**，原样记录脱敏诊断信息后向用户报告。
- **脱敏回执记录项（不落真实隐私与签名）**：实际调用的端点名、请求次数、HTTP 状态码、`status_code`、流封装与编码类型（如 `flv` + `h264` / `hevc-cid12` / `hevc-hvc1`、HLS 分片扩展名）、下载/录制字节数、分段数、MP4 校验时长误差——**严禁在文档、测试夹具或日志中写入真实测试 Cookie、签名 URL、真实测试账号 `sec_uid`/`uid`/`web_rid`/`room_id`/`aweme_id`**（固化回归夹具时必须全部替换为合成 ID 与合成域名）。

<a id="a-review"></a>
## 11. A 审阅定稿与 ACT-010 实测结论（2026-11-12 定稿，2026-11-13 更新）

A 按[站点规则](../dev-rules/sources-and-safety.md#site-rules)审阅了第 1～10 节，抽查了其中引用的仓库代码事实（`storage/schema.go` 各文件结构版本均为 1、`uistate` 空站点归一为 `bilibili`、下载熔断全局生效、`validateVideoOutput` 强制 `moov` 在前与 `elst`、播放器只按“先有视频轨”打开文件、`scheduler_state` 只有单行），都属实。2026-11-13 B 按 [ACT-010](../../USER-ACTIONS.md#act-010) 运行 `go run ./tools/actcheck -act act010` 1 次（账本 `runs=1`、接口与播放列表请求 `15/18`、点播与图片 CDN 请求 `6/16` 共 `10,602,566` 字节、`stoppedBy=""`；脱敏回执 `artifacts/site-check/act010.json`）。下面是 M3 实现采用的取舍与 V-01～V-08 核对状态：

### 11.1 V-01～V-08 实测核对状态（2026-11-13）

| 编号 | 状态 | 实测结果与工程结论（已脱敏） |
| --- | --- | --- |
| **V-01** | **已验证** | `user/profile/self` 不带 Cookie 发请求被正常识别为 `auth_required`（未触发 403/验证码）；带粘贴 Cookie 请求一次返回登录成功并拿到完整 `uid`、`sec_uid` 与昵称 |
| **V-02** | **已验证** | 纯 Go 标准库实现的 SM3 + `a_bogus`（`dhzx` 盐值）+ `x-secsdk-web-signature`（`uifid` 存在时附加）+ 剥离 `bd_ticket_guard_*` 后的粘贴 Cookie（本次初始 Cookie 不含 `msToken`，由客户端生成 128 位回退串）在 `profile/self`、`profile/other`（2 次）、`aweme/post`（3 次）、`aweme/detail`（4 次）与 `webcast/room/web/enter`（2 次）上**全部一次通过**，0 次 403/429/444、0 次空包或验证码拦截 |
| **V-03** | **已验证** | 目标主页第 1 页含置顶作品时，`max_cursor` 正常驱动第 2 页翻页成功（`aweme/post` 共 3 次请求全部通过），未出现访客空壳退化 |
| **V-04** | **已验证** | 普通图文与“图片/视频/配乐混排”图文均由 `aweme/detail`（`aid=6383`）完整解析（`len(images) > 0` 优先于顶层配乐 `video.play_addr`，混排视频页按 `live_photo_type != 1` 且含 `images[i].video` 识别），图片、混排视频与配乐直链在不带 Cookie 下拉取及封装校验全部通过 |
| **V-05** | **已验证** | 点播 CDN 直链在不带 Cookie 下支持闭区间 `Range` 分段拉取，返回音画同轨渐进式单文件 MP4 并通过 `EnsureSingleMP4Context` 容器校验（连同 V-04 资产合计 6 次请求、`10,602,566` 字节） |
| **V-06** | **已回答**：在线路径可用；离线 `room_id=2` 返回 `status_code=10011`，换不到 `web_rid`（ACT-011） | 对开播中的本场 `room_id` 调 `webcast/room/reflow/info/`（不带 `a_bogus`）一次成功；离线占位 `room_id=2` 因首轮客户端本地 `reRoomDigits` 要求 `>= 4` 位数字在发包前被本地拒绝（0 次网络请求），已放宽为 1～24 位正整数并移入 `ACT-011` 待测 |
| **V-07** | **已验证（AVC）：ACT-014 录到原画 AVC 720p 21.56 秒并收尾为可播放 MP4；HEVC 未在真实站点出现（ACT-011 时检查程序误停 `302`；ACT-012 查明跳转去向为白名单外的 HTTPS 443 域名、被生产策略拒绝，已改跳转规则）** | 开播直播间的 `.m3u8` 播放列表文本在不带 Cookie 下拉取成功（1 次）；随后因首轮 `EnterLiveRoom` 误将外层 `data.enter_mode == 1` / 无条件 `live_room_mode == 2` 标为 `AudioOnly`，在建立 FLV 连接前提前报错退出（直播媒体 `0` 次请求）。已修正为仅凭外层 `data.room_status == 1`、`live_type_audio == true` 或纯音频 `stream_url` 判定语音房，HTTP-FLV 流头（`CodecID=12` vs `hvc1`）与短录收尾归入 `ACT-011` 待测 |
| **V-08** | **已验证** | 同一直播间的两种号码（主播不变的数字 `short_id` 与另一串可变数字）调 `webcast/room/web/enter/` 均通过并指向同一开播直播间与主播 `sec_uid`；代码保持以 `sec_uid` 为唯一身份主键，确认数字 `short_id` 可用时在可失效的 `webRid` 缓存中优先保留 `short_id` |

### 11.2 M3 实现定稿取舍表

| 问题 | 定稿 | 依据与边界 |
| --- | --- | --- |
| 登录确认 | 只用 `user/profile/self` 判定（§3.2 三个条件），不用 `query/user` 与心跳接口；未登录与风控分别归类，风控不清除已保存的凭证 | V-01 已实测确认 |
| 粘贴 Cookie 的处理 | 保留 §3.1 的核心身份态、配套身份态和访问上下文三类字段，丢弃“浏览器内存密钥绑定的动态票据”；至少要有 `sessionid` 或 `sessionid_ss`，只有 `ttwid`/`msToken` 的视为未登录；不向 `bytedance.com` 匿名注册 `ttwid`，缺 `ttwid` 时提示从打开过抖音网页的浏览器重新复制 | V-02 已实测确认剥离动态票据且无初始 `msToken` 时各端点正常通过 |
| 签名 | `a_bogus` 用 `dhzx` 形态；`uifid` 存在时对 §2.3 列出的受保护路径加 `x-secsdk-web-signature`；签名模块在 `internal/site/douyin` 内部隔离，对外只暴露“输入请求上下文、输出完整查询串与附加请求头”。实现来源见[选型 §4](2026-11-12-douyin.md#a-review) | V-02 已实测通过 |
| 链接识别 | 按 §4.3 的优先级；纯 19 位数字不猜类型、提示改贴链接；`sec_uid`（`MS4wLjAB` 开头）可以直接输入；`/user/self` 映射到当前登录账号 | — |
| 用户主键 | 抖音账号以 `sec_uid` 为主键；`uid` 只作诊断；抖音号只显示；`web_rid` 只作为账号上**可失效的缓存**（“最近一次能用的直播间号”），不当身份 | V-08 已实测确认两种房间号均可进入同一直播间，优先缓存已确认的数字 `short_id` |
| 作品类型 | 先看 `images[]` 非空判为图文，再看视频流；其余显示“暂不支持的作品类型”与“在浏览器中打开” | 图文顶层 `video.play_addr` 是配乐，不当成视频（V-04 已确认） |
| 播放数 | `statistics.play_count` 不显示；点赞、评论、收藏、分享只在在线详情页显示，不缓存（DEC-040） | V-04/V-05 详情核对 `play_count` 占位值已隐藏 |
| 点播选流 | 按 §6.5：先最高分辨率档，同档按设置的编码偏好，再按帧率和码率；排除带水印地址与 `is_bytevc1 == 2`；付费或加密内容标为不可缓存并说明原因，不做任何解密 | 与 FR-CACHE-002 的“先画质后编码”一致（DEC-043 ⑤） |
| 媒体下载 | 不向 CDN 带 Cookie；按白名单域名下载，拒绝 HTML/JSON 伪装的响应；地址过期时每个文件最多重新取一次详情；分段续传复用现有下载器 | V-05 已实测确认无 Cookie `Range` 分段拉取与单文件 MP4 封装 |
| 直播状态来源 | 有 `web_rid` 的账号用 `web/enter`；没有的用 `profile/other` 的 `room_id`（检查更新时用 `aweme/post` 第 1 页的 `author.room_id`，不额外请求），开播时经 `reflow/info` 补上 `web_rid` 并保存。每次用缓存的 `web_rid` 都核对返回的主播 `sec_uid`：对不上、或返回不存在/不可看时清掉缓存，改走 `room_id` 路线（同一次检查最多多 1 次请求）。`web/enter` 接受数字 `short_id` 时优先缓存 `short_id` 形式；语音房仅凭外层 `room_status == 1`、`live_type_audio == true` 或纯音频 `stream_url` 判定。`check_alive` 只用于巡检已知场次是否结束 | 在线 `reflow/info` 与双号 `web/enter` 已在 V-06/V-08 验证；离线 `room_id=2` 与 FLV 短录待 `ACT-011` |
| 请求节奏 | 采用 §9.3：抖音 API 全局间隔 1000 ms + 0～300 ms 抖动、串行；规则触发的请求每小时 ≤ 120 次；每轮检查最多 20 个抖音账号、账号之间 ≥ 2.5 秒；开播巡检只对选用了“开播自动录制”规则的账号，每个账号间隔 ≥ 90 秒。都与哔哩哔哩分开计数和退避 | 真实检查后可收紧，放宽要用户确认 |
| 图文混排 | 图文的每一页按类型处理：图片、实况照片、视频页（§6.4 规则），认不出的页保存图片并记明；视频页的选流与校验同视频作品 | V-04 已在真实混排图文样本上验证通过 |
| 真实检查程序 | [ACT-010](../../USER-ACTIONS.md#act-010) 已执行 1 次；未跑完的两个子项由独立小额检查 `ACT-011`（`-act act011`）承接；§10 保留调研时的初稿 | — |
| 空响应判定 | 严格按 §9.2 真值表；任何不确定的空结果都不当成“没有作品”或“到底了”，订阅检查记为“本轮结果不完整”，不移动起点 | — |

<a id="sign-spec"></a>
## 附录：签名规格

来源：`中国国家标准 GB/T 32905-2016《信息安全技术 SM3 密码杂凑算法》`、`源码阅读（本机参考索引中的抖音签名与访问上下文实现，2026-11-12）`。本附录按 [DEC-044](../decisions.md) 记录抖音 Web 请求签名的客观互操作协议事实与确定性合成测试向量，供 `internal/site/douyin/sign` 在洁净室边界下仅使用 Go 标准库独立实现与校验。

### A.1 国密 SM3 杂凑算法（GB/T 32905-2016）

1. **输入与输出**：任意长度字节序列 $M$，输出 256 比特（32 字节，大端序 8 个 `uint32` 字）摘要。
2. **消息填充**：假设消息比特长度为 $l = 8 \times |M|$：
   - 在消息末尾追加 1 个字节 `0x80`；
   - 继续追加 `0x00` 字节直至总字节长度满足 $\text{len} \equiv 56 \pmod{64}$；
   - 追加 $l$ 的 64 比特无符号大端整数（8 字节），填充后总字节数为 64 的正整数倍。
3. **初始值 $IV$（8 个大端 `uint32`）**：
   - `V[0..7] = {0x7380166F, 0x4914B2B9, 0x172442D7, 0xDA8A0600, 0xA96F30BC, 0x163138AA, 0xE38DEE4D, 0xB0FB0E4E}`。
4. **常量与布尔/置换函数**（`rotl32(x, k)` 表示 32 比特循环左移 $k \bmod 32$ 位，算术加均为模 $2^{32}$）：
   - 轮常量：当 $0 \le j \le 15$ 时 $T_j = \text{0x79CC4519}$；当 $16 \le j \le 63$ 时 $T_j = \text{0x7A879D8A}$。
   - 布尔函数：
     - $0 \le j \le 15$：$FF_j(X,Y,Z) = X \oplus Y \oplus Z$，$GG_j(X,Y,Z) = X \oplus Y \oplus Z$；
     - $16 \le j \le 63$：$FF_j(X,Y,Z) = (X \wedge Y) \vee (X \wedge Z) \vee (Y \wedge Z)$，$GG_j(X,Y,Z) = (X \wedge Y) \vee (\neg X \wedge Z)$。
   - 置换函数：$P_0(X) = X \oplus \text{rotl32}(X, 9) \oplus \text{rotl32}(X, 17)$；$P_1(X) = X \oplus \text{rotl32}(X, 15) \oplus \text{rotl32}(X, 23)$。
5. **消息扩展与迭代压缩**（按每块 64 字节顺序处理）：
   - 将当前 64 字节块按大端拆为 $W_0 \dots W_{15}$；
   - 对 $j = 16 \dots 67$：$W_j = P_1(W_{j-16} \oplus W_{j-9} \oplus \text{rotl32}(W_{j-3}, 15)) \oplus \text{rotl32}(W_{j-13}, 7) \oplus W_{j-6}$；
   - 对 $j = 0 \dots 63$：$W'_j = W_j \oplus W_{j+4}$；
   - 令 $(A,B,C,D,E,F,G,H) = V[0..7]$，对 $j = 0 \dots 63$：
     - $SS1 = \text{rotl32}(\text{rotl32}(A, 12) + E + \text{rotl32}(T_j, j),\ 7)$
     - $SS2 = SS1 \oplus \text{rotl32}(A, 12)$
     - $TT1 = FF_j(A,B,C) + D + SS2 + W'_j$
     - $TT2 = GG_j(E,F,G) + H + SS1 + W_j$
     - $D = C,\ C = \text{rotl32}(B, 9),\ B = A,\ A = TT1$
     - $H = G,\ G = \text{rotl32}(F, 19),\ F = E,\ E = P_0(TT2)$
   - 块结束后更新 $V[0..7] = (V_0 \oplus A, V_1 \oplus B, V_2 \oplus C, V_3 \oplus D, V_4 \oplus E, V_5 \oplus F, V_6 \oplus G, V_7 \oplus H)$。
6. **标准测试向量（GB/T 32905-2016 附录 A）**：
   - 输入 `"abc"`（3 字节 `61 62 63`）→ `66c7f0f462eeedd9d1f2d46bdc10e4e24167c4875cf2f7a2297da02b8f4ba8e0`
   - 输入 `"abcd"` 重复 16 次（64 字节）→ `debe9ff92275b8a138604889c18e5a4d6fdb70e5387e5765293dcba39c0c5732`

### A.2 UTF-16 码元字节化、修改版 RC4 与双自定义 Base64 字母表

1. **UTF-16 码元字节化（对应前端 JS 字符串逐码元序列化）**：
   - 将字符串编码为 UTF-16 码元序列（`uint16`，BMP 之外字符拆为高低代理项对）。
   - 对每个码元 $u$：若 $u \le \text{0x00FF}$，输出单字节 `byte(u)`；若 $u > \text{0x00FF}$，输出大端双字节 `byte(u >> 8), byte(u & 0xFF)`。纯 ASCII 字符串在此规则下与 UTF-8 字节完全一致。
2. **修改版 RC4 对称流密码**：
   - **降序 S 盒初始化**：长度为 256 的字节数组 `S`，令 `S[255 - i] = byte(i)`（$i = 0 \dots 255$，即 `S[0] = 255, S[1] = 254, ..., S[255] = 0`）。
   - **乘法密钥调度（KSA）**：令 $j = 0$，对 $i = 0 \dots 255$：
     $$j = (j \times S[i] + j + \text{key}[i \bmod |\text{key}|]) \bmod 256$$
     交换 `S[i]` 与 `S[j]`。
   - **伪随机生成与异或（PRGA）**：令 $i = 0, j = 0$，对输入序列第 $t$ 个字节 $b_t$：
     $$i = (i + 1) \bmod 256,\quad j = (j + S[i]) \bmod 256$$
     交换 `S[i]` 与 `S[j]`，输出 $b_t \oplus S[(S[i] + S[j]) \bmod 256]$。加解密完全同构。
3. **自定义 Base64 编码规则**：
   - 分组与填充同 RFC 4648（每 3 字节大端拼为 24 比特，依次取高到低 4 组 6 比特索引查表；末组余 1 字节补 `"=="`，余 2 字节补 `"="`），仅替换 64 字符字母表：
   - **字母表 A（用于 `User-Agent` 密封串编码）**：
     `ckdp1h4ZKsUB80/Mfvw36XIgR25+WQAlEi7NLboqYTOPuzmFjJnryx9HVGDaStCe`
   - **字母表 B（用于最终 `a_bogus` 字符串编码）**：
     `Dkdpgh2ZmsQB80/MfvV36XI1R45-WUAlEixNLwoqYTOPuzKFjJnry79HbGcaStCe`

### A.3 `a_bogus`（`dhzx` 形态）常量与三路摘要链

1. **协议常量**：
   - 盐值字符串：`"dhzx"`
   - 外层明文魔数对：`(3, 82)`（`0x03, 0x52`）
   - 内层 SDK 版本 4 元组：`(1, 0, 1, 0)`
   - 内层载荷修改版 RC4 密钥：单字节 `0xD3`
   - 双周纪元起点：`epochMs = 1721836800000`（`2024-07-24T16:00:00Z`），双周毫秒跨度 `fortnightMs = 1209600000`（14 天）；若输入时钟 `nowMs < epochMs` 则视为非法时钟拒绝签名。
   - 默认站点参数：`aid = 6383`，`page_id = 6241`，默认桌面窗口特征串 `browser_info = "1920|947|1920|1032|1920|1032|1920|1080|Win32"`。
   - 固定环境探针标量：`marker = 41`、`callBucket = 6`、`initElapsed = 3`、`envFlags = 1`、`nrFlags = 0x21`（`33`）、`nrTag = (0, 0, 0, 0)`、`detectFlags = 14`、`tripwireLocked = 3`。
2. **金丝雀字节选取函数 `canary(digest[32], offset, sentinel, fallback)`**：
   - 从下标 `i = offset` 到 `31` 顺序扫描 32 字节摘要 `digest`：返回第一个满足 `digest[i] != sentinel` 的字节；若 `digest[offset..31]` 全部等于 `sentinel`，返回 `fallback`。
3. **三路输入摘要链**：
   - **查询串链 `dq`**：取第 1 阶段编码后的查询串 `q`（不含 `a_bogus` 本身），以 UTF-8 拼接盐值得到字节串 `[]byte(q + "dhzx")`，做**双重 SM3**（第 2 次 SM3 的输入为第 1 次 SM3 输出的 32 个原始摘要字节）：`dq = SM3(SM3([]byte(q + "dhzx")))`。提取 3 字节：`(q0, q1, qCanary) = (dq[9], dq[18], canary(dq, 11, 3, 12))`。
   - **请求体链 `db`**：若 `Content-Type`（忽略大小写）包含 `"multipart/form-data"`，有效请求体视为空串 `""`，否则取请求体原串 `b`（GET 请求为 `""`）。同样做双重 SM3：`db = SM3(SM3([]byte(effectiveBody + "dhzx")))`。提取 3 字节：`(b0, b1, bCanary) = (db[10], db[19], canary(db, 8, 4, 9))`。
   - **`User-Agent` 链 `dua`**：去除 `User-Agent` 首尾空白（为空则报错），按 §A.2 第 1 条转为码元字节序列，用 3 字节密钥 `[]byte{0x00, 0x01, 0x0E}` 执行修改版 RC4，再用**字母表 A** 做 Base64 编码得到 ASCII 字符串 `uaSealed`，对其 ASCII 字节做**单次 SM3**：`dua = SM3([]byte(uaSealed))`。提取 3 字节：`(u0, u1, uCanary) = (dua[11], dua[21], canary(dua, 12, 5, 13))`。

### A.4 50 槽位标量布局与固定置换表

记小端字节提取算子 $\text{LE}(x, k) = \text{byte}((x \gg (8k)) \ \& \ \text{0xFF})$。设当前毫秒时间戳为 `nowMs`，次级时间戳 `ink = nowMs - 1`，双周计数 `fortnights = byte(((nowMs - 1721836800000) / 1209600000) & 0xFF)`，`infoBytes` 为 `browser_info` 的 §A.2 码元字节序列，`tailBytes` 为十进制格式化字符串 `fmt.Sprintf("%d,", (nowMs + 3) & 0xFF)` 的码元字节序列（长度取值 `2`、`3` 或 `4`）。

各逻辑寄存器定义如下：

| 逻辑槽名 | 字节数 | 定义与取值（多字节整数均为小端序 LSB 在前） |
| --- | --- | --- |
| `S_marker` | 1 | 固定 `41`（`0x29`） |
| `S_fortnights` | 1 | `fortnights` |
| `S_bucket` | 1 | 固定 `6` |
| `S_elapsed` | 1 | 固定 `3` |
| `S_now[0..5]` | 6 | `nowMs` 的 48 比特小端字节 `LE(nowMs, 0..5)` |
| `S_env[0..1]` | 2 | `envFlags = 1` 的 16 比特小端字节 `(1, 0)` |
| `S_nr[0..1]` | 2 | `nrFlags = 0x21` 的 16 比特小端字节 `(0x21, 0)` |
| `S_nrTag[0..3]` | 4 | 固定 `(0, 0, 0, 0)` |
| `S_detect[0..3]` | 4 | `detectFlags = 14` 的 32 比特小端字节 `(14, 0, 0, 0)` |
| `S_query[0..2]` | 3 | `(dq[9], dq[18], canary(dq, 11, 3, 12))` |
| `S_body[0..2]` | 3 | `(db[10], db[19], canary(db, 8, 4, 9))` |
| `S_ua[0..2]` | 3 | `(dua[11], dua[21], canary(dua, 12, 5, 13))` |
| `S_ink[0..5]` | 6 | `ink = nowMs - 1` 的 48 比特小端字节 `LE(ink, 0..5)` |
| `S_tripLock` | 1 | 固定 `3` |
| `S_page[0..3]` | 4 | `page_id`（默认 `6241`）的 32 比特小端字节 `LE(page_id, 0..3)` |
| `S_aid[0..3]` | 4 | `aid`（默认 `6383`）的 32 比特小端字节 `LE(aid, 0..3)` |
| `S_infoLen[0..1]` | 2 | `len(infoBytes)` 的 16 比特小端字节 `LE(len(infoBytes), 0..1)` |
| `S_tailLen[0..1]` | 2 | `len(tailBytes)` 的 16 比特小端字节 `LE(len(tailBytes), 0..1)` |

上述 50 个单字节按以下固定置换表排入 `scalars[0..49]`：

| 输出下标 | +0 | +1 | +2 | +3 | +4 | +5 | +6 | +7 | +8 | +9 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| **`0..9`** | `S_now[5]` | `S_detect[0]` | `S_ua[0]` | `S_ink[1]` | `S_aid[2]` | `S_now[0]` | `S_page[3]` | `S_detect[1]` | `S_env[0]` | `S_query[1]` |
| **`10..19`** | `S_nr[0]` | `S_tripLock` | `S_query[2]` | `S_page[1]` | `S_elapsed` | `S_query[0]` | `S_ink[4]` | `S_detect[3]` | `S_now[1]` | `S_aid[0]` |
| **`20..29`** | `S_fortnights` | `S_body[2]` | `S_now[2]` | `S_page[2]` | `S_ua[2]` | `S_nrTag[0]` | `S_ink[2]` | `S_ink[3]` | `S_bucket` | `S_aid[1]` |
| **`30..39`** | `S_nrTag[1]` | `S_aid[3]` | `S_ua[1]` | `S_body[0]` | `S_nrTag[2]` | `S_nr[1]` | `S_now[4]` | `S_page[0]` | `S_body[1]` | `S_nrTag[3]` |
| **`40..49`** | `S_ink[5]` | `S_detect[2]` | `S_env[1]` | `S_marker` | `S_ink[0]` | `S_now[3]` | `S_infoLen[0]` | `S_infoLen[1]` | `S_tailLen[0]` | `S_tailLen[1]` |

### A.5 位掩码混淆、噪声带约束、XOR 校验和与尾零省略边界

1. **`2 -> 4` 双字节位交织算子 `maskPair(p0, p1, nLow, nHigh)`**：
   - 将两个有效载荷字节 `(p0, p1)` 与两个噪声字节 `(nLow, nHigh)` 按奇偶比特位（`0xAA = 0b10101010`、`0x55 = 0b01010101`）交织为 4 个字节：
     - `out[0] = (nLow & 0xAA) | (p0 & 0x55)`
     - `out[1] = (nLow & 0x55) | (p0 & 0xAA)`
     - `out[2] = (nHigh & 0xAA) | (p1 & 0x55)`
     - `out[3] = (nHigh & 0x55) | (p1 & 0xAA)`
   - 逆向还原：`p0 = (out[0] & 0x55) | (out[1] & 0xAA)`，`p1 = (out[2] & 0x55) | (out[3] & 0xAA)`；噪声还原：`nLow = (out[0] & 0xAA) | (out[1] & 0x55)`，`nHigh = (out[2] & 0xAA) | (out[3] & 0x55)`。
2. **熵源字节读取顺序与噪声约束**：
   - 为支持确定性测试向量注入，单次 `a_bogus` 生成从熵流 `Entropy` 顺序读取 `6 + len(rawBody)/3` 个字节 `e[0], e[1], ...`：
   - **内层版本头 `version[0..7]`（消费 `e[0..3]`）**：
     - `version[0..3] = maskPair(1, 0, e[0], e[1])`；
     - 探针噪声 `nProbe`：令 `v = int(e[2]) % 240`；若 `v > 109`，则 `nProbe = byte(v + (v % 2) + 1)`（落入 `111..241` 的奇数集，跳过 `110..240` 偶数区）；否则 `nProbe = byte(v)`；
     - 绊线噪声 `nTripwire = (e[3] & 0x4D) | 0xB2`（比特位 `1, 4, 5, 7` 恒为 `1`）；
     - `version[4..7] = maskPair(1, 0, nProbe, nTripwire)`。
   - **外层明文头 `header[0..3]`（消费 `e[4..5]`）**：
     - 浏览器族基数 `base` 按 `User-Agent` 小写子串依次匹配首个命中项：含 `"edg"` → `125`；含 `"huawei"` → `170`；含 `"firefox"` → `40`；含 `"chrome"` → `0`；含 `"safari"` → `81`；其余 → `210`；
     - `nBrowserFamily = byte((base + int(e[5]) % 40) & 0xFF)`（桌面 Chrome UA 落入 `0..39`）；
     - `header[0..3] = maskPair(3, 82, e[4], nBrowserFamily)`。
3. **单字节 XOR 校验和与 `rawBody` 拼装**：
   - 校验和仅对 8 字节混淆后版本头 `version[0..7]` 与 50 字节标量块 `scalars[0..49]` 逐字节求异或（**不含** `infoBytes` 与 `tailBytes`）：
     $$\text{checksum} = \left(\bigoplus_{i=0}^{7} \text{version}[i]\right) \oplus \left(\bigoplus_{i=0}^{49} \text{scalars}[i]\right)$$
   - 原始主体帧：`rawBody = scalars[0..49] || infoBytes || tailBytes || []byte{checksum}`。
4. **`3 -> 4` 主体扩展与尾零省略边界**：
   - 噪声位掩码 `(0x91, 0x42, 0x2C)`，数据位掩码 `(0x6E, 0xBD, 0xD3)`（逐位互补，且 `0x91 | 0x42 | 0x2C == 0xFF`）。
   - 按每组 3 字节切分 `rawBody`：
     - **完整 3 字节组 `g[0..2]`**（第 $k$ 组消费熵字节 `n = e[6 + k]`）：输出 4 字节：
       1. `(n & 0x91) | (g[0] & 0x6E)`
       2. `(n & 0x42) | (g[1] & 0xBD)`
       3. `(n & 0x2C) | (g[2] & 0xD3)`
       4. `(g[0] & 0x91) | (g[1] & 0x42) | (g[2] & 0x2C)`
     - **不足 3 字节的尾组 `g`（`len(g)` 为 `1` 或 `2`，不消费熵字节）**：
       - 先原样追加 `g[0]`；
       - 若 `len(g) == 2` **且 `g[1] != 0x00`**，再追加 `g[1]`；
       - **关键边界（尾零省略）**：当 `len(rawBody) % 3 == 2` **且末尾字节 `g[1]`（恰好即 `checksum`）等于 `0x00` 时，该 `0x00` 字节不写入扩展帧**，此时 `expanded` 的总长度比常规余 2 情况少 1 字节！
5. **内层密封与最终编码**：
   - 令 `sealed = modifiedRC4([]byte{0xD3}, version[0..7] || expanded)`；
   - 将未加密的 `header[0..3]` 与 `sealed` 拼接，按 §A.2 **字母表 B** 编码得到最终 `a_bogus` 字符串（总字符数为 4 的整数倍）。

### A.6 反向结构自检解码规则（`decodeABogus`）

单测中的离线反向解码器按以下步骤核验任一生成结果的字节完整性：

1. 校验字符串非空、长度为 4 的整数倍、全部字符属于字母表 B 或尾随 `'='`，逆向还原字节流 `wire`（长度至少 64 字节）；
2. 用 `maskPair` 逆变换解开 `wire[0..3]`，验证明文魔数为 `(3, 82)`，并核对高字节噪声落在对应 `User-Agent` 的浏览器族区间 `[base, base+39]`；
3. 对 `wire[4:]` 执行密钥 `0xD3` 的修改版 RC4 解密得到 `plain`；解开 `plain[0..7]` 的两组 `maskPair`，验证内层版本元组为 `(1, 0, 1, 0)`，并核对探针噪声（避开 `110..240` 偶数）与绊线噪声（`(nTripwire & 0xB2) == 0xB2`）；
4. 对 `plain[8:]` 按每 4 字节一组执行 `4 -> 3` 折叠还原（`g[k] = (c[k] & dataMask[k]) | (c[3] & noiseMask[k])`；末尾不足 4 字节组原样拼接）得到 `unpacked`；
5. 按 §A.4 固定置换逆映射从 `unpacked[0..49]` 还原 `nowMs`、`ink`、`aid`、`page_id`、`infoLen`、`tailLen` 及三路摘要与金丝雀字节，核对 `ink == nowMs - 1`、`S_marker == 41`、`S_env == 1`、`S_detect == 14`、`S_nr == 0x21`、`S_tripLock == 3`；
6. 设预期主体尾下标 `end = 50 + infoLen + tailLen`（即 `checksum` 所在下标，预期完整长度 `expectedLen = end + 1`），重新计算 `expectedChecksum = XOR(plain[0..7]) ^ XOR(unpacked[0..49])`：
   - 若 `len(unpacked) == expectedLen`：要求 `unpacked[end] == expectedChecksum`（且若 `expectedLen % 3 == 2`，此时 `expectedChecksum` 必不为 `0`）；
   - 若 `len(unpacked) == end`：仅当 `expectedLen % 3 == 2` **且 `expectedChecksum == 0x00`** 时合法（命中尾零省略边界），其余长度一律判为结构损坏！

### A.7 `x-secsdk-web-signature`（`ArgusSecurityPlugin`）两阶段签名规则

1. **受保护路径集合（共 14 条，比对前先剥离查询串并归一化首尾单斜杠 `/.../`）**：
   - `/aweme/v1/web/aweme/detail/`
   - `/aweme/v1/web/aweme/post/`
   - `/aweme/v1/web/aweme/favorite/`
   - `/aweme/v1/web/aweme/listcollection/`
   - `/aweme/v1/web/mix/aweme/`
   - `/aweme/v1/web/tab/feed/`
   - `/aweme/v1/web/mix/list/`
   - `/aweme/v1/web/music/aweme/`
   - `/aweme/v1/web/music/list/`
   - `/aweme/v1/web/mix/detail/`
   - `/aweme/v1/web/mix/listcollection/`
   - `/aweme/v1/web/music/detail/`
   - `/aweme/v1/web/collects/list/`
   - `/aweme/v1/web/collects/video/list/`
   - 注：`/aweme/v1/web/user/profile/other/`、`/aweme/v1/web/user/profile/self/`、`/webcast/room/web/enter/` **不在**上述集合内。
2. **上下文提取与触发条件**：
   - `uifid`：按顺序查找 Cookie 中首个非空字段：`uifid`、`uifid_temp`、`uifidtemp`、`UIFID`、`UIFID_TEMP`、`UIFIDTEMP`；
   - `verifyFp`：取 Cookie 中非空的 `s_v_web_id`；
   - **仅当 `uifid` 非空且请求路径命中上述 14 条受保护路径之一时**，执行第 2 阶段网关签名；否则在第 1 阶段把 `("a_bogus", sig1)` 追加到有序参数列表末尾序列化后直接返回，附加请求头为空。
3. **参数编码与两阶段拼接顺序**：
   - **单次百分号编码算子 `encodeComponent`**：ASCII 字母数字 `A-Z`、`a-z`、`0-9` 及四个安全符号 `'-'`、` '_'`、`'.'`、`'*'` 保持原样，其余 UTF-8 字节一律编码为大写 `%XX`（保证参与签名的查询串与最终发上线的查询串逐字节恒等，绝不重排原参数顺序、绝不二次编码）。
   - **第 1 阶段**：按调用方给出的参数顺序将各 `(key, value)` 编码为 `encodeComponent(key) + "=" + encodeComponent(value)` 并以 `"&"` 连接得到 `stage1Query`，按 §A.3～A.5 算出 `sig1`，在有序参数切片末尾追加 `("a_bogus", sig1)`。
   - **第 2 阶段（受保护路径且 `uifid` 非空）**：在含 `a_bogus` 的有序参数切片上依次处理：
     1. 若 `verifyFp` 非空：若切片中尚无 `"verifyFp"` 键则追加 `("verifyFp", verifyFp)`；若尚无 `"fp"` 键则追加 `("fp", verifyFp)`；
     2. 若切片中尚无 `"uifid"` 键，则在末尾追加 `("uifid", uifid)`；**若原查询参数中已存在 `"uifid"` 键，则保留其原有位置与原值，不重复追加**；
     3. 令秒级时间戳十进制串 `stamp = strconv.FormatInt(nowMs / 1000, 10)`：若切片中已有 `"timestamp"` 键则就地更新其值为 `stamp`，否则在末尾追加 `("timestamp", stamp)`；
     4. 用同一 `encodeComponent` 将当前全部有序参数对序列化为 `stage2Query`；
     5. 以固定盐值 `"A96D855A08C0A9707F8BEF0D9A527E4E"` 构造原像字符串 `uifid + "_" + stamp + "_A96D855A08C0A9707F8BEF0D9A527E4E_" + stage2Query`，计算其 32 位小写十六进制 MD5 摘要 `secSig`；
     6. 最终查询串为 `stage2Query + "&x-secsdk-web-signature=" + secSig`；
     7. 同步返回 3 个 HTTP 请求头：
        - `uifid`: `uifid`
        - `x-secsdk-web-signature`: `secSig`
        - `x-secsdk-web-expire`: `stamp`（**仅写入请求头，绝不进入 URL 查询串**）。

### A.8 确定性合成测试向量（全部采用合成输入）

以下三组向量共用固定环境常量：
- `User-Agent`: `Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36`
- `browser_info`: `1920|947|1920|1032|1920|1032|1920|1080|Win32`
- `aid`: `6383`，`page_id`: `6241`，`Body`: `""`，`ContentType`: `""`

#### 向量 1：非受保护路径（仅第 1 阶段，`len(rawBody) == 99`，`99 % 3 == 0`）

- **端点路径**：`/aweme/v1/web/user/profile/other/`
- **固定时钟 `nowMs`**：`1763000000097`
- **上下文令牌**：`uifid = ""`，`verifyFp = ""`
- **有序输入参数表**：

| 序号 | 参数名 | 参数值（合成） |
| --- | --- | --- |
| 1 | `device_platform` | `webapp` |
| 2 | `aid` | `6383` |
| 3 | `channel` | `channel_pc_web` |
| 4 | `sec_user_id` | `MS4wLjABAAAA_SYNTHETIC_USER_01` |
| 5 | `msToken` | `SYNTHETIC_TOKEN_01` |

- **注入熵流（39 字节，十六进制）**：
  `030a11181f262d343b424950575e656c737a81888f969da4abb2b9c0c7ced5dce3eaf1f8ff060d`
- **预期中间态与输出**：
  - `checksum`：`0xfd`；`expanded` 长度：`132` 字节
  - 第 1 阶段签名值（`Signature`）：
    `dJUnkqSiQoWbKd/tmcnD7tnUTLy/NB8y8TidWraTHxP4OhzcC8PoPENWaNErszEkLRBhiH17YxFMbExcTtUwZeHpwmpvSkTfdU5VVgmLZqwsGzkQErjzew6zLw0rURsql55tiIRI/UJ96VnAwHdE/BA9tKoC55WBM3OWk/ubP9B6ZMyAD3nHPBSpYhiO0HVR`
  - 第 1 阶段签名百分号编码值：
    `dJUnkqSiQoWbKd%2FtmcnD7tnUTLy%2FNB8y8TidWraTHxP4OhzcC8PoPENWaNErszEkLRBhiH17YxFMbExcTtUwZeHpwmpvSkTfdU5VVgmLZqwsGzkQErjzew6zLw0rURsql55tiIRI%2FUJ96VnAwHdE%2FBA9tKoC55WBM3OWk%2FubP9B6ZMyAD3nHPBSpYhiO0HVR`
  - 附加请求头 `Headers`：空（0 项）

#### 向量 2：受保护路径 + `uifid` + `verifyFp`（两阶段，`len(rawBody) == 97`，`97 % 3 == 1`）

- **端点路径**：`/aweme/v1/web/aweme/detail/`
- **固定时钟 `nowMs`**：`1763000000000`（秒级 `stamp = "1763000000"`）
- **上下文令牌**：`uifid = "SYNTHETIC_UIFID_02"`，`verifyFp = "verify_synthetic_fp_02"`
- **有序输入参数表**：

| 序号 | 参数名 | 参数值（合成） |
| --- | --- | --- |
| 1 | `device_platform` | `webapp` |
| 2 | `aid` | `6383` |
| 3 | `channel` | `channel_pc_web` |
| 4 | `aweme_id` | `7300000000000000001` |
| 5 | `msToken` | `SYNTHETIC_TOKEN_02` |

- **注入熵流（38 字节，十六进制）**：
  `05101b26313c47525d68737e89949faab5c0cbd6e1ecf7020d18232e39444f5a65707b86919c`
- **预期中间态与输出**：
  - `checksum`：`0xcd`；`expanded` 长度：`129` 字节
  - 第 1 阶段签名值（`Signature`）：
    `mv0fhqywmZWnKdKGmCcY7talj9j/rTWy6TTOboNPtxOeOqUc5mNOPYxAGNxN4MDkgWpkieIHTxzlbddcTGXzZC9kompvuOXW24VcVgvo0qqsGMvQDrbsez6FKwMNUbTqeAVvil46hUr96nVAkHQY/pAySKLC5c8BP3x6k2ucT9Bg1zgAD1c-PdSkKh52`
  - 第 2 阶段追加后参数顺序：`device_platform`, `aid`, `channel`, `aweme_id`, `msToken`, `a_bogus`, `verifyFp`, `fp`, `uifid`, `timestamp`（最终查询串末尾再拼 `&x-secsdk-web-signature=<secSig>`，不含 `x-secsdk-web-expire`）
  - 第 2 阶段 MD5 签名值 `secSig`：`8a3f27d0959b92823749f4cf46d54644`
  - 附加请求头 `Headers`：`uifid = "SYNTHETIC_UIFID_02"`，`x-secsdk-web-signature = "8a3f27d0959b92823749f4cf46d54644"`，`x-secsdk-web-expire = "1763000000"`

#### 向量 3：尾零省略边界 `len(rawBody) % 3 == 2 && checksum == 0x00` + 查询串预置 `uifid` 不重复

- **端点路径**：`/aweme/v1/web/aweme/post/`
- **固定时钟 `nowMs`**：`1763000000007`（`(nowMs + 3) & 0xFF == 10`，`tailBytes = "10,"` 共 3 字节，`len(rawBody) == 98`，`98 % 3 == 2`；秒级 `stamp = "1763000000"`）
- **上下文令牌**：`uifid = "SYNTHETIC_UIFID_03"`，`verifyFp = ""`
- **有序输入参数表（原查询串第 6 项已含 `uifid`）**：

| 序号 | 参数名 | 参数值（合成） |
| --- | --- | --- |
| 1 | `device_platform` | `webapp` |
| 2 | `aid` | `6383` |
| 3 | `sec_user_id` | `MS4wLjABAAAA_SYNTHETIC_USER_03` |
| 4 | `max_cursor` | `0` |
| 5 | `count` | `18` |
| 6 | `uifid` | `SYNTHETIC_UIFID_03` |
| 7 | `msToken` | `SYNTHETIC_TOKEN_03` |

- **注入熵流（38 字节，十六进制）**：
  `da1623303d4a5764717e8b98a5b2bfccd9e6f3000d1a2734414e5b6875828f9ca9b6c3d0ddea`
- **预期中间态与输出**：
  - `checksum`：**`0x00`**；因 `len(rawBody) == 98`（`98 % 3 == 2`）且尾字节 `checksum == 0x00`，触发尾零省略规则，`expanded` 长度由常规 130 字节缩减为 **`129` 字节**
  - 第 1 阶段签名值（`Signature`）：
    `QvUnDFUjmq85OdKt8CcY7SrUG2L/rPuy5TiKRFnPHPz1chMa5mN2PYxWjNxN40DkgRBwie1HxxzlbEVczGXhZHHpqmkvSwiyxU5AV0fo0qN4GzkQDHbheuXzFwMr0csqe55XilR62UJ9gVdAirQY/Bl9tKoCQcSBB1OfkMucN9B6Z06AD3c1PptpNhiq`
  - 第 2 阶段追加后参数顺序：`device_platform`, `aid`, `sec_user_id`, `max_cursor`, `count`, `uifid`（保持第 6 位原位，不重复追加）, `msToken`, `a_bogus`, `timestamp`
  - 第 2 阶段 MD5 签名值 `secSig`：`668d0b177abc76c8f89e56a95b45e575`
  - 附加请求头 `Headers`：`uifid = "SYNTHETIC_UIFID_03"`，`x-secsdk-web-signature = "668d0b177abc76c8f89e56a95b45e575"`，`x-secsdk-web-expire = "1763000000"`
