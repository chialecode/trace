# 小红书接入可行性与五站防风控伪装调研报告

> [!IMPORTANT]
> **时效性与非长期权威声明（`2026-11-28` 时点快照）**
>
> 本文是截至 **2026-11-28** 的静态调研与代码基线审计快照，**不得被视为永久或长期权威结论**。各平台的反爬策略、风控阈值、动态签名与加密算法（例如小红书 `XYS_`/`XYW_`、`xsec_token`、`localStorage["b1"]`，抖音 `a_bogus`/Argus，X GraphQL/`txid`/TLS 检查，以及 B 站与微博风控规则）、开源生态维护状态与许可证均可能随时发生变化；本文第 4 节记录的 TRACE 现有四站代码审计发现亦仅反映 **2026-11-28** 的源码状态（后续跟进见 [#183](https://github.com/chialecode/trace/issues/183) 与 [#184](https://github.com/chialecode/trace/issues/184)）。
>
> **任何后续读者或 AI Agent 在引用本文前，必须先将文档日期（`2026-11-28`）与当前时间进行比对**；若已过时、准备立项实施、或目标站点发生协议/风控变更，必须重新调研并按[站点规则](../dev-rules/sources-and-safety.md#site-rules)与[来源与安全 §1](../dev-rules/sources-and-safety.md#open-source)核查最新情况。

- **调研日期**：2026-11-28
- **范围与合规**：纯静态只读调研——**未向任何站点发起真实 HTTP 请求**；按 [#182](https://github.com/chialecode/trace/issues/182) 归档入库。
- **证据分级**：
  - `[已查证-仓库/实测回执]`：直接核对 TRACE 截至 2026-11-28 的源码、文档与既有脱敏站检回执（`artifacts/site-check/`）。
  - `[已查证-公开一手源]`：官方平台说明、活跃开源仓库元信息/PR/许可证及公开技术标准。
  - `[社区逆向/经验]`：2025–2026 社区逆向与多方工程经验，未经本机实测复现。
  - `[未知/待实测]`：当前无公开确定阈值或尚未在 TRACE 做过实机验证。

---

<a id="summary"></a>
## 核心结论速览

### 1. 小红书（RedNote）是否适合接入 TRACE？
**不建议做完整接入**（订阅轮询、慢速缓存队列、关注导入、直播、`trace-server` VPS 远程版），其**账号风控风险与长期协议维护成本均为五站最高**。主要存在四重结构性阻力：
1. **短时、上下文绑定的 `xsec_token` 墙**：与 TRACE 当前 `PlanDownload(targetID)`、慢速缓存队列（40–150 秒/条 + 长休眠）及失败重试模型直接冲突。
2. **双轨轮换签名（`XYS_`/`mnsv2` + 2026 年 `XYW_` AES）+ 非 Cookie 的 `localStorage["b1"]`**：仅粘贴 `Cookie` 会丢失 `b1`，且签名算法每数周到数月微调、半年至一年大改，无法像 X 的 `queryId` 那样运行时自动发现。
3. **极高账号风控与同端会话冲突**：极易触发「不常见的浏览行为」站内私信提醒、`300011`、`461`/`471` 验证码，且社区普遍反馈 Web 端同端登录易互踢。
4. **Web 端能力残缺**：无可用 Web 关注列表接口（无法做关注导入），直播无稳定 `web_rid` 且依赖按可变小红书号搜索轮询。

> 若后续确实需要支持，**唯一低风险形态**是：**仅桌面端、单链接即点即存的 SSR 解析归档**（依赖带 `xsec_token` 的完整分享链接、立即下载媒体、不做后台轮询与远程同步，并强提示使用小号）。

### 2. 目前四站 + 小红书，仅靠 Cookie 够不够？加上 UA / 更多 Header 就能避免异常提醒吗？
- **仅靠 Cookie 够不够？** **不够。**
  - **小红书 / 抖音 / X**：绝对不够（均强依赖动态请求签名/加密头 + 自洽请求头）。
  - **哔哩哔哩 / 微博**：单靠 Cookie 也不完整（B 站需 WBI + `dm_img_*` + `w_webid` + `Referer` + 桌面 UA；微博需 `Referer` + `X-Requested-With` + `X-XSRF-TOKEN` + 桌面 UA）。
- **加上 `User-Agent` 和更多浏览器 Header 能彻底避免小红书「不常见的浏览行为」提醒吗？** **不能。**
  - 补齐并保证 Header **跨层自洽**是必要的低成本卫生措施（错误或矛盾的 Header 确实会直接触发风控）。
  - 但小红书的异常私信是多层关联风控的结果：**Cookie ↔ `localStorage["b1"]` ↔ `x-s`/`x-s-common`/`x-mns` 签名 ↔ `xsec_token` 浏览链路 ↔ TLS/H2 指纹 ↔ 双端/异地 IP 共用会话 ↔ 纯 API 无前端埋点/无资源加载的机械行为特征**。纯 Header 伪装无法掩盖数据中心 IP、缺失 `b1`/链路或非人类请求模式。
- **对 TRACE 现有四站的关键实测事实** `[已查证-仓库/实测回执]`：
  - TRACE 基于纯 Go 1.27.4 标准库 `net/http`（无 uTLS/H2 伪装）在**低频、已登录、住宅 IP**下，2026 年 11 月的站检回执已验证四站核心读取全部可通（B 站 `ACT-030`、抖音 `ACT-010`/`026`/`028`、微博 `ACT-023`、X `ACT-025` 核心 V-01..V-07 及 `dec077-x-activities`）。
  - 因此**现阶段不应急于引入重型 TLS 伪装依赖**；但审计发现 2026-11-28 代码基线中存在多处**零依赖即可修复的真实 Header 矛盾、跨站 Cookie 泄漏与意外 HTTP/1.1 降级问题**（见第 4 节，跟进于 [#183](https://github.com/chialecode/trace/issues/183)）。

---

<a id="xiaohongshu"></a>
## 一、小红书（RedNote）接入可行性深度调研

### 1.1 TRACE 能力映射与缺口

| TRACE 能力 | 可行性 | 关键协议路径与限制 | 证据等级 |
| --- | --- | --- | --- |
| **登录与严格身份校验** | 可行（有会话冲突风险） | 手动粘贴 Cookie 或 Web 扫码登录；校验必须查 `GET /api/sns/web/v2/user/me` 且同时断言 `code == 0 && data.guest == false && data.user_id != ""`（访客态也会返回 `code == 0, guest: true`）。Web 端普遍反馈同端互踢，桌面端登录易踢掉浏览器会话。 | `[社区逆向/经验]` |
| **链接与 ID 解析** | 可行（强依赖完整参数） | 支持 `xiaohongshu.com/{explore,discovery/item,user/profile,board}/<24-hex>` 与 `xhslink.com/{a,o,m,...}/<code>` 短链跳转；跳转必须保留完整查询串（含 `xsec_token`、`xsec_source`、`app_platform` 与 Base64 `=` 填充）。 | `[社区逆向/经验]` |
| **用户主页与作品列表** | 受限可行 | 签名 `GET /api/sns/web/v1/user/otherinfo` 与 `user_posted?num=30&cursor=...&user_id=...`；SSR 页面 `/user/profile/<uid>` 仅含第 1 页（约 30 条），无法翻页。 | `[社区逆向/经验]` |
| **笔记详情** | 受限可行 | 签名 `POST /api/sns/web/v1/feed`（需 `source_note_id`、`xsec_source`、`xsec_token`），或 SSR HTML 解析 `window.__INITIAL_STATE__`（PC 端 `note.noteDetailMap`，移动端 H5 `noteData.data.noteData`；`app_share` 链接用 PC UA 常跳 `-510001` 需切移动 UA；JSON 需先清理 `undefined`/`new Map(...)` 等非标准字面量）。 | `[社区逆向/经验]` |
| **高清无水印图片** | 可行 | 从 `imageList[].fileId` 拼 `https://sns-img-qc.xhscdn.com/<fileId>?imageView2/2/w/1920/format/jpg`（或去样式后缀），适配 TRACE `MediaTransportSpec`（对 `*.xhscdn.com` 剥离 Cookie）。是否长期免鉴权待实测。 | `[社区逆向/经验]` `[未知/待实测]` |
| **视频笔记** | 可行（CDN 鉴权待验证） | 优先级：`video.consumer.originVideoKey` → `sns-video-{bd,al}.xhscdn.com/<key>`；次选 `video.mediaV2` 高清投屏流；兜底 `video.media.stream.{h264,h265,av1}` 并排除带水印的 `streamType`（`259`/`309`）。 | `[社区逆向/经验]` `[未知/待实测]` |
| **实况图（Live Photo）** | 高度契合 | `image.livePhoto == true` 时读取 `image.stream.{h264,h265,av1}[0].masterUrl` + `backupUrls`，直接对应 TRACE 既有 `MediaImageAsset`（`MediaRole: "live_photo"`，见 [types.go](../../internal/site/types.go#L500-L515)）。 | `[已查证-仓库/实测回执]` `[社区逆向/经验]` |
| **关注列表导入** | **Web 不可用** | PC Web 虽有“关注”信息流页签，但无稳定的关注/粉丝名单枚举接口（仅写接口 `user/follow`）；开源 CLI 维护记录亦证实已移除 Web 关注列表功能。须像抖音一样排除 `FollowingService`。 | `[已查证-公开一手源]` `[社区逆向/经验]` |
| **收藏/点赞/专辑** | 部分可行 | 公开/本人收藏 `GET /api/sns/web/v2/note/collect/page`、点赞 `v1/note/like/page`、专辑 `/board/<id>` SSR。 | `[社区逆向/经验]` |
| **直播状态与录制** | **极脆弱（建议延后）** | 无固定 `web_rid`，每场 `room_id` 变化；按作者查开播需签名调用 `POST /api/sns/web/v1/search/usersearch` 搜索**可修改的 `red_id`** 读 `live_info`，再查 `live-room.xiaohongshu.com/.../current_room_info` 并排除标题含「回放」的场次，拉流地址 `live-source-play(-hw).xhscdn.com/live/<room_id>.{flv,m3u8}`。 | `[社区逆向/经验]` |

### 1.2 稳定 ID 与 `xsec_token` 对 TRACE 架构的冲击
- **稳定 ID**：`user_id`、`note_id`、`board_id` 均为 24 位小写十六进制 MongoDB ObjectId（`^[0-9a-f]{24}$`），适合做主键；`red_id`（小红书号）可由用户修改，只能用于展示 `[已查证-公开一手源]` `[社区逆向/经验]`。
- **`xsec_token` / `xsec_source` 的架构摩擦** `[社区逆向/经验]` `[已查证-仓库/实测回执]`：
  - 由上游入口（`pc_user`、`pc_feed`、`pc_search`、`pc_collect`、`app_share`）按「会话 + 对象 + 来源」动态签发，无公开固定有效期，属短时/上下文绑定令牌（注：约 3–5 分钟是 `x-t` 时间戳窗口，并非 `xsec_token` 的公开固定 TTL，各来源精确 TTL 属 `[未知/待实测]`），遇风控或跨会话立即失效。
  - **直接破坏 TRACE 三处核心模型**：
    1. [types.go](../../internal/site/types.go#L652-L655) 的 `DownloadPlanner.PlanDownload(ctx, targetID, page)` 只传裸 `targetID`，不足以请求小红书详情。
    2. 裸链接 `/explore/<24-hex>` 缺少 `xsec_token` 会直接返回 `302 → /404/sec_*`（`300031`）或 HTTP `406`。
    3. TRACE 的慢速缓存队列（默认每条间隔 40–150 秒、每 15–25 条长休 10–25 分钟，见[架构设计 §3](../design/architecture.md#services)）与失败重试会在真正下载前遭遇令牌过期，需回源重刷 `user_posted`（对无作者上下文的单独链接无法重刷，对深分页成本极高）。

### 1.3 登录态、关键 Cookie 与非 Cookie 秘密 `localStorage["b1"]`
- **关键 Cookie** `[社区逆向/经验]`：
  - `web_session`（主会话，但访客也有，不能作登录依据）；
  - `a1`（约 52 字符客户端设备种子，**参与加密签入 `x-s` 与 `x-s-common`**）；
  - `webId`、`gid` / `gid.sign`、`id_token`、`sec_poison_id`、`loadts`、WAF 路由键（`websectiga`、`acw_tc`、`abRequestId`、`xsecappid`）。
- **核心痛点：`localStorage["b1"]` 不在 Cookie 中**：小红书前端将加密浏览器环境指纹存入 `localStorage.b1`，并在 `x-s-common` 中作为 `x8` 发送，与 `a1`/`x-t`/`x-s` 一起计算校验值 `x9`。用户仅从 DevTools 复制 `Cookie:` 请求头时完全缺失 `b1`；缺 `b1` 或伪造不匹配的 `b1` 是社区最常报告的 `300011` 诱因之一 `[社区逆向/经验]`（具体触发阈值 `[未知/待实测]`）。
- **续期与失效**：无 Web 端 `refresh_token` 自动续期接口；稳定环境下可持续数天至数周，高压风控、跨 IP 漂移或同端重新登录时可能数小时内失效 `[社区逆向/经验]` `[未知/待实测]`。

### 1.4 2025–2026 动态签名机制与轮换节奏
`https://edith.xiaohongshu.com` 核心 API 要求每个请求携带 `x-t`（毫秒时间戳）、`x-s`、`x-s-common`、`x-b3-traceid`（16 hex）、`x-xray-traceid`（32 hex）、`x-mns`（如 `unload`），部分高敏路由还跟踪 `x-rap-param` `[社区逆向/经验]`：
- **双轨并行与灰度切换**：
  1. **`XYS_` / `mnsv2`**：基于前端 JSVMP（`sec_ds_script.js` / `vendor-dynamic.js`），打包约 144 字节二进制载荷（含时间戳、`loadts`、序号、URI 摘要、`a1`、`xhs-pc-web`、环境指纹）经 XOR 与自定义 Base64 生成。
  2. **`XYW_`（`_webmsxyw` AES-128-CBC，2026 年升级）**：2026 年 3–6 月起，`user_posted`、`user/otherinfo` 等端点开始以 **HTTP `406 Not Acceptable`** 拒绝 `XYS_`，要求改为 AES-128-CBC（PKCS#7）的 `XYW_` 格式 `[已查证-公开一手源]`（见 `Cloxl/xhshow` `v0.2.0` / PR #105，2026-06-11）。
- **轮换频率**：键表/盐值/灰度策略每**数周至 2–3 个月**微调，大版本签名体系每 **6–12 个月**演进一次 `[社区逆向/经验]`。

### 1.5 开源方案与 Go 实现可行性（按[来源与安全 §1](../dev-rules/sources-and-safety.md#open-source) 评估）

| 候选方案 | 仓库 / 来源 | 语言与路线 | 许可证 | 2026-11 维护状态 | 对 TRACE（Apache-2.0 / 纯 Go MyGo / `CGO_ENABLED=0` VPS）的适配评估 |
| --- | --- | --- | --- | --- | --- |
| `Cloxl/xhshow` | https://github.com/Cloxl/xhshow | Python 纯算法实现 `XYS_`/`XYW_`/`x-s-common`/`x-rap-param` | **MIT** `[已查证-公开一手源]` | 活跃（2026-06 `v0.2.0` 适配 `XYW_`） | 非 Go 依赖；可作纯 Go 洁净室实现的算法参考 |
| `jackwener/xiaohongshu-cli` | https://github.com/jackwener/xiaohongshu-cli | Python 逆向 Web API CLI | **Apache-2.0** `[已查证-公开一手源]` | 活跃（2026） | 非 Go 依赖；协议与风控处理参考（已证实移除 Web 关注列表） |
| `tamnd/xiaohongshu-cli` | https://github.com/tamnd/xiaohongshu-cli | 纯 Go 自签 HTTPS CLI | **Apache-2.0** `[已查证-公开一手源]` | 体量极小（2026） | 证明纯 Go 签名可行，但体量过小且与 CLI 耦合，不宜作直接依赖 |
| `xpzouying/xiaohongshu-mcp` | https://github.com/xpzouying/xiaohongshu-mcp | Go + `go-rod` 无头 Chromium | **Apache-2.0** `[已查证-公开一手源]` | 活跃（2025–2026） | 需随包/调用 Chromium（150–300 MiB 磁盘 + 200–500 MiB 内存），违背 TRACE 零浏览器桌面与轻量 VPS 设计 |
| `JoeanAmier/XHS-Downloader` | https://github.com/JoeanAmier/XHS-Downloader | Python SSR/API 下载器 | **GPL-3.0（禁止依赖）** `[已查证-公开一手源]` | 活跃（2026） | 许可证不兼容，仅可参考公开协议思路，不可复用代码 |
| `NanmiCoder/MediaCrawler` | https://github.com/NanmiCoder/MediaCrawler | Python Playwright 爬虫 | **非商用许可（禁止）** `[已查证-公开一手源]` | 活跃（2026） | 非 OSI 开源许可，与 Apache-2.0 完全不兼容 |
| `ReaJason/xhs` | https://github.com/ReaJason/xhs | Python + Playwright hook | **MIT** `[已查证-公开一手源]` | 已停滞（约 2025 中） | 过期排除 |
| 小红书开放平台 / `zsmhub/xhs-sdk` | https://open.xiaohongshu.com | 官方 OAuth / 电商商家 API | **Apache-2.0**（SDK）`[已查证-公开一手源]` | 活跃 | **不适用**：面向电商商家，个人开发者无消费端笔记/主页/关注读接口 |
| 嵌入式 JS/WASM（`goja`/`sobek`/`wazero`+`quickjs-ng`） | [抖音选型研究 §1.3–§1.5](2026-11-12-douyin.md) | 纯 Go JS/WASI | MIT / Apache-2.0 | 活跃 | 实测二进制膨胀 `+2.80`～`+9.65 MiB`，仍无 DOM/Canvas/`b1` 环境，且不能合法打包小红书私有混淆 JS |

**四种实现路线对比**：
1. **纯 Go 洁净室签名**：二进制零膨胀、兼容 `CGO_ENABLED=0` VPS，但签名维护成本极高，且无法原生生成 `b1` 指纹。
2. **嵌入式 JS/WASM**：最差——既膨胀体积、缺浏览器环境，又有打包站点私有脚本的版权风险。
3. **无头浏览器（Rod/Playwright）**：抗微调强、能产真实 `b1`/TLS，但破坏 TRACE 无浏览器架构且仍会被云 IP 和机械行为风控。
4. **双 UA SSR 混合（仅解析 `__INITIAL_STATE__`）**：单链接归档免签名，但无法翻页作者历史、不能解裸 ID、高风险 IP 仍会跳 `/404/sec_*` 或 `/login`。

### 1.6 常见错误码与三站维护/风控横向对比
- **错误码映射** `[社区逆向/经验]`：`-100`/`-101` 登录失效；`-104`/`-510001` 权限或 UA 不匹配；`300011` 账号异常软拦截（不要清空 Cookie，应按风控停机）；`300012`/`300013` 限流；`300015` 浏览器环境/签名异常；`300031`（302 → `/404/sec_*`）风控或 `xsec_token` 缺失/过期（**不是已删除**）；HTTP `406` 签名版本错配或缺少 `xsec_token`；HTTP `461`/`471` 验证码硬拦截；`code: 0` + 空列表需标 `UncertainEmpty`。
- **与抖音、X 对比**：
  - **抖音**：纯 Go `a_bogus` + Argus，3–6 个月稳定周期，无 `xsec_token` 墙，无 `b1` 依赖，支持直播 FLV → **维护：中｜账号风险：中**。
  - **X**：`queryId` 1–2 周漂移但可运行时自动发现，纯 Go `txid`，无单帖令牌墙，VPS 易触发 `326` 锁号 → **维护：中高｜账号风险：高**。
  - **小红书**：`XYS_`/`XYW_` 双轨轮换 + `b1` 不可自动发现 + `xsec_token` 破坏延迟队列 + 同端互踢 + 无 Web 关注接口 + 直播脆弱 + 频繁异常私信/`461`/`471` → **维护：极高｜账号风险：极高**。

---

<a id="anomaly-dm"></a>
## 二、小红书「不常见的浏览行为」异常私信成因剖析

该站内私信属于小红书服务端的**软风控预警**（后续往往伴随静默空列表、`300011`/`300015`、HTTP `406`/`461`/`471` 滑块验证乃至账号限流/封禁）`[社区逆向/经验]`，通常由以下 6 层信号叠加触发：

1. **Cookie 与浏览器指纹分裂**：只带 `Cookie`（含 `a1`、`web_session`）请求接口，却缺失或伪造了存于 `localStorage.b1` 的设备指纹，或 `x-s-common` 里的 OS（`x2`）、签名计数（`x10`）与签发 `a1` 的真实浏览器不一致 `[社区逆向/经验]`。
2. **签名头缺失、陈旧或版本错配**：`x-s`/`x-t`/`x-s-common`/`x-mns`/`x-rap-param` 缺失，或在已切 `XYW_` 的端点继续发 `XYS_` `[社区逆向/经验]`。
3. **缺失正常浏览链路（`xsec_token` 溯源断裂）**：不经首页/搜索/个人主页获取新鲜 `xsec_token`，直接用裸 ID 或复用过期/跨会话 `xsec_token` 请求 `feed` / `user_posted` `[社区逆向/经验]`。
4. **单会话双端/异地分裂（粘贴 Cookie 的典型陷阱）**：同一组 `web_session` + `a1` 同时在用户的手机/电脑浏览器和外部脚本/代理/VPS 上活跃，同一会话同时出现两套 IP、TLS 与 UA 轨迹 `[社区逆向/经验]`。
5. **传输层 TLS/H2 与宣称的浏览器头矛盾**：声称自己是桌面 Chrome，却使用 Go `crypto/tls`、Python `requests`/`httpx` 或 Node `undici` 的 TLS JA3/JA4 与 HTTP/2 帧特征 `[社区逆向/经验]`（住宅 IP 具体拦截阈值 `[未知/待实测]`）。
6. **纯行为与埋点异常（最直接对应「不常见的浏览行为」）**：只拉取 `edith.xiaohongshu.com` JSON 或页面 HTML，**没有任何前端曝光/停留/滚动埋点、静态资源加载或随机停顿**，或呈现等间隔定时轮询、深夜规律抓取、批量分页拉取。**即便使用真实 Playwright/Chromium，机械式轮询依然会触发该私信** `[社区逆向/经验]`。

---

<a id="five-site-matrix"></a>
## 三、五站防爬校验层级全景矩阵

| 防护层级 | 哔哩哔哩（Bilibili） | 抖音（Douyin） | 微博（Weibo） | X（Twitter） | 小红书（Xiaohongshu） |
| --- | --- | --- | --- | --- | --- |
| **仅 Cookie 够不够？** | **不够**（需 WBI + `dm_img_*` + `w_webid` + `Referer` + UA） | **不够**（需 `a_bogus` + Argus + UA 绑定查询参数 + Header） | **不够**（需 `Referer` + `X-Requested-With` + `X-XSRF-TOKEN` + UA） | **不够**（需 Bearer + `x-csrf-token` + `x-client-transaction-id` + Header） | **远远不够**（需 `x-s`/`x-s-common`/`x-mns` + `b1` + `xsec_token` + Header + TLS/行为） |
| **L1：Cookie 与自洽性** | `SESSDATA`、`bili_jct`、`DedeUserID` 必填；`buvid3`/`buvid4`/`b_nut`/`bili_ticket` 显著降低 `-352`；扫码可获独立会话与 `refresh_token`。 | `sessionid*` + `ttwid` + `uifid*` + `msToken`（与查询串一致）+ 真实 `s_v_web_id`；**必须剥离**浏览器密钥绑定的 `bd_ticket_guard_*`/`__security_*`（`ACT-010`/`028` 验证）。 | `SUB`（访客也有，不能单凭存在判登录）+ `SUBP` + `WBPSESS` + `XSRF-TOKEN` + `ALF`/`SCF`；PC Cookie 不通 `m.weibo.cn`。 | `auth_token` + `ct0`（须镜像到 `x-csrf-token`，不可自造，需接收 `Set-Cookie` 刷新）+ `twid` + `guest_id` + 可选 `kdt`。 | `web_session` + `a1` + `webId`/`gid`/`id_token`/`loadts`/`sec_poison_id` **外加非 Cookie 的 `localStorage.b1`**。 |
| **L2：`User-Agent` 与强耦合字段** | 需常规桌面 Chrome UA，**不参与 WBI 签名**；当前固定 `Chrome/131` 实测通过（但已陈旧约 2 年）。 | **强密码学耦合**：UA 经 SM3 签入 `a_bogus`，并决定浏览器噪声分段，且必须与查询串 `browser_version`/`os_name`/`browser_info` 一致。 | 需常规桌面 Chrome UA（脚本 UA 易 403/418），不参与签名。 | 需桌面 Chrome UA；未启用 XPFF 时不参与 `txid`，启用 `x-xp-forwarded-for` 时 UA 会加密写入 `navigator_properties`。 | **强耦合**：嵌入 `x-s-common`（OS `x2`、`b1` `x8`）并与 `a1`/`web_session` 原始环境及 `Sec-Ch-Ua*` 交叉核验。 |
| **L3：扩展与站点专用 Header** | 最小头（UA + `Referer`，直播加 `Origin`）在低频住宅 IP 可通；缺 `Sec-Ch-Ua*`/`Sec-Fetch-*`/`Accept-Language`。 | `Referer`、`Origin`（POST/直播）、`Accept-Language`、`Sec-Ch-Ua*`、`Sec-Fetch-*` + Argus 三头（`uifid`/`x-secsdk-web-signature`/`expire`）；不可发未注册 `bd-ticket-guard-*`（`ACT-028` 证实会退化为 `1103`）。 | `Referer: https://weibo.com/`（图片/视频 CDN 亦必填）、`Page-Referer`、`Origin`（POST）、`X-Requested-With`、`X-XSRF-TOKEN`、`Sec-Ch-Ua*`（实测缺 `Sec-Fetch-*` 亦通过 `ACT-023`）。 | `Authorization`、`x-csrf-token`、`x-twitter-auth-type`、`x-twitter-active-user`、`x-twitter-client-language`、`x-client-transaction-id`、`Origin`/`Referer`、`Sec-Ch-Ua*`、`Sec-Fetch-*`（跨子域 `api.x.com` 应为 `same-site`）。 | `Origin`/`Referer`、`Sec-Ch-Ua*`、`Sec-Fetch-Site: same-site`（对 `edith`）、`Sec-Fetch-Mode: cors`、`Sec-Fetch-Dest: empty` + 强制 `x-s`/`x-t`/`x-s-common`/`x-b3-traceid`/`x-xray-traceid`/`x-mns`（及高敏 `x-rap-param`）。 |
| **L4：传输层 TLS（JA3/JA4）与 HTTP/2** | Go stdlib TLS（**且当前 API 实际走 HTTP/1.1**）在住宅 IP 下实测全通（`ACT-002..030`）；海外/VPS IP 或访客高频时敏感。 | Go stdlib TLS + **HTTP/2**（`ACT-028` 实测 `HTTP/2.0`）住宅 IP 可通；VPS/高频对 BDTuring 影响 `[社区逆向/经验]`。 | Go stdlib TLS + **HTTP/2** 住宅 IP 全通（`ACT-023` 31/31 API，0 拦截）。 | Go stdlib TLS + **HTTP/2** 低频住宅 IP 实测可通（`ACT-025` V-01..V-07 & `dec077`）；但在 **VPS IP / 高频 / 搜索** 上 JA4+H2 是触发 `403`/Arkose/`326` 的强放大器 `[社区逆向/经验]`。 | `edith`/`www` 边缘 WAF 对 TLS+H2 敏感度高，社区普遍反馈需 `curl_cffi` 或浏览器级 TLS 降拦截 `[社区逆向/经验]`；住宅低频精确阈值 `[未知/待实测]`。 |
| **L5：行为与网络特征（IP/节奏/会话）** | 登录态 + ≥500ms 串行风控最温和；海外/VPS IP 更易 `-352`/`-412`。 | 对缺损 Cookie 上下文（`ACT-029` `aweme/favorite` 返回 0B + `x-vc-bdturing-parameters`）、高频轮询与云 IP 敏感。 | 对突发并发敏感（`414`/`418`/`432`）；当前 `800ms+抖动` + 180/h 表现稳定。 | **极严格**：15 分钟窗口窄（`UserTweets` 50 次/15 分钟），定时机械轮询、深分页与 **VPS IP** 易触发 `429`/假空壳/`326` 锁号/`64` 封号。 | **五站最严**：综合判定无埋点直调 API、缺 `xsec_token` 链路、双端 IP 分裂、固定间隔、突发抓取与云 IP → 异常私信 → `300011`/`461`/`471`。 |

---

<a id="four-site-audit"></a>
## 四、TRACE 现有四站代码审计结果（2026-11-28 基线快照）

截至 **2026-11-28** 逐行核对 TRACE 现有四站源码，在 Header、Cookie 与传输层上存在以下具体、可验证的基线现状与缺陷（下文所列行号对应 2026-11-28 基线；相关修复与微博 SSO 扫码登录分别跟踪于 [#183](https://github.com/chialecode/trace/issues/183) 与 [#184](https://github.com/chialecode/trace/issues/184)，后续状态以最新代码为准）：

### 4.1 跨站共性问题
1. **意外的 HTTP/1.1 降级（Go `http.Transport` 陷阱）** `[已查证-仓库/实测回执]`：
   - Go 标准库只要设置了自定义 `DialContext` 且未显式设 `ForceAttemptHTTP2: true`，就会自动禁用 HTTP/2。
   - **哔哩哔哩全部 API 与媒体请求目前只走 HTTP/1.1**（[bilibili/client.go](../../internal/site/bilibili/client.go#L178-L201) 的 `apiTransport` 与 `mediaTransport` 均未设 `ForceAttemptHTTP2: true`）。
   - 四站的 `mediaTransport`、通用范围下载器（[range_download.go](../../internal/download/range_download.go#L768-L776)）、直播 FLV（[flv_stream.go](../../internal/live/flv_stream.go#L353-L360)）以及微博/X 的 `redirectTransport`（[weibo/client.go](../../internal/site/weibo/client.go#L277-L286)、[x/client.go](../../internal/site/x/client.go#L295-L304)）也均停留在 HTTP/1.1。仅抖音 `apiTransport`（含其复用的 `redirectClient`，[douyin/client.go](../../internal/site/douyin/client.go#L310-L314)）、微博 `apiTransport`、X `apiTransport`（[x/client.go](../../internal/site/x/client.go#L282)）与 X `assetClient`（[x/frontend.go](../../internal/site/x/frontend.go#L497)）启用了 HTTP/2。
2. **`Accept-Encoding: gzip` 单一压缩头**：所有站点均由 Go 自动注入 `gzip`，不带 Chrome 标准的 `gzip, deflate, br, zstd`。
3. **写死且过期的 Windows Chrome UA（Linux VPS 远程版同样发送 Windows UA）** `[已查证-仓库/实测回执]`：
   - B 站与默认常量使用 **`Chrome/131.0.0.0`**（[types.go](../../internal/site/types.go#L13)、[bilibili/client.go](../../internal/site/bilibili/client.go#L22)），截至 2026-11 已过期约 2 年（而[哔哩哔哩协议资料 §2](bilibili.md)已写到 `Chrome/144.0.0.0`）。
   - 抖音、微博、X 使用 **`Chrome/136.0.0.0`**（[douyin/profile.go](../../internal/site/douyin/profile.go#L15)、[weibo/client.go](../../internal/site/weibo/client.go#L27)、[x/client.go](../../internal/site/x/client.go#L27)），过期约 1.5 年。
   - `opts.UserAgent` 在桌面端（[backend.go](../../internal/service/backend.go#L338-L365)）与 Linux 远程版（[remoteserver/run.go](../../internal/remoteserver/run.go#L54-L64)）均从未注入。
4. **凭证存储与远程推送不含浏览器档案，且自动把 B 站/抖音/微博 Cookie 推到 VPS**：
   - [credential.go](../../internal/credential/credential.go#L78-L99) 仅存 Cookie、`RefreshToken` 与 `DouyinGuard`，不存来源 UA/平台；[remote_client.go](../../internal/service/remote_client.go#L294) 仅推送 Cookie 字符串，且 `afterLocalLogin`（[remote_client.go](../../internal/service/remote_client.go#L415-L427)）对 B 站、抖音、微博自动推送（仅 X 禁用了自动推送，但服务端仍接受手动推送，见 [remote_server_api.go](../../internal/service/remote_server_api.go#L214-L215)），易造成住宅 IP 与云 VPS IP 并发共用同一会话。
5. **非 Chromium UA 注入隐患**：`deriveBrowserProfile`（[weibo/client.go](../../internal/site/weibo/client.go#L124-L147)、[x/client.go](../../internal/site/x/client.go#L105-L128)、[douyin/profile.go](../../internal/site/douyin/profile.go#L53-L105)）若传入 Firefox/Safari UA，仍会照常发送 `Sec-Ch-Ua: "Chromium"... "Google Chrome"...`，一旦直接接入用户 UA 捕获会制造明显反爬破绽。

### 4.2 逐站具体缺陷清单（2026-11-28 基线）
- **抖音（最高优先修复，见 [#183](https://github.com/chialecode/trace/issues/183)）**：
  - **跨站 Cookie 泄漏到 `webcast.amemv.com`**：`ReflowRoomInfo`（[douyin/live.go](../../internal/site/douyin/live.go#L386-L392)）与 `CheckRoomsAlive`（[douyin/live.go](../../internal/site/douyin/live.go#L512-L518)）未设 `guestNoCookie: true`，导致程序把完整 `.douyin.com` Cookie 连同 `Sec-Fetch-Site: cross-site` + `Origin: https://live.douyin.com` 发往不同主域 `webcast.amemv.com`——真实浏览器不可能产生此请求。
  - `ResolvePlayRedirect`（[douyin/client.go](../../internal/site/douyin/client.go#L432)）复用 `applyAPIHeaders`，在 `/aweme/v1/play/` 视频跳转请求上发送 `Accept: application/json`、`Sec-Fetch-Mode: cors`、`Sec-Fetch-Dest: empty`。
  - `webid` 未持久化，每次重启 `NewClient` 重新随机生成（[douyin/client.go](../../internal/site/douyin/client.go#L181-L188)）。
  - 直播巡检 `source == "live"` 按 `DEC-060 ⑤` 绕过 120 次/小时额度（[migrations.go](../../internal/subscription/migrations.go#L765-L777)、[douyin_poll.go](../../internal/live/douyin_poll.go#L16-L44)），在 ≥15 个开启录制的账号下最高可持续到约 600 次/小时，长期风控压力大。
- **X（高优先修复，见 [#183](https://github.com/chialecode/trace/issues/183)）**：
  - **跨子域 `Sec-Fetch-Site` 写错**：[x/client.go](../../internal/site/x/client.go#L1270) 硬编码 `Sec-Fetch-Site: same-origin`，在请求 `https://api.x.com/1.1/account/settings.json`（[x/auth.go](../../internal/site/x/auth.go#L135-L143)）时与 `Origin: https://x.com` 矛盾（Chrome 对跨子域发 `same-site`）。
  - `fetchStaticAsset`（[x/frontend.go](../../internal/site/x/frontend.go#L1152-L1155)）拉取 `abs.twimg.com` 脚本时剥离 `Referer`/`Origin` 且缺 `Sec-Ch-Ua*`/`Sec-Fetch-*`。
  - `t.co` 短链只跟随 3xx（[x/resolve.go](../../internal/site/x/resolve.go#L888-L913)），而 `ACT-025` 已实测桌面 Chrome UA 会收到 HTTP 200 HTML meta-refresh 导致解析失败。
- **微博（见 [#183](https://github.com/chialecode/trace/issues/183)、[#184](https://github.com/chialecode/trace/issues/184)）**：
  - [weibo/client.go](../../internal/site/weibo/client.go#L792-L828) 发送 `Sec-Ch-Ua*`（Chrome 136）却完全不发送 `Sec-Fetch-Site/Mode/Dest`——现代 Chrome 不会只发前者不发后者。
- **哔哩哔哩（见 [#183](https://github.com/chialecode/trace/issues/183)）**：
  - API 全量缺 `Sec-Ch-Ua*`、`Sec-Fetch-*`、`Origin`（含 POST 续期）、`Accept-Language`；所有端点（含直播、空间、HTML、protobuf 弹幕）统一硬编码 `Referer: https://www.bilibili.com/` 与 `Accept: application/json, text/plain, */*`（[bilibili/client.go](../../internal/site/bilibili/client.go#L507-L519)）。
  - 请求节流为**固定 500ms、零抖动**（[bilibili/client.go](../../internal/site/bilibili/client.go#L26)）。
  - Cookie 5 键白名单（[credential.go](../../internal/credential/credential.go#L60-L74)）在手动粘贴时会丢弃有助于降低 `-352` 的 `buvid3`/`buvid4`/`b_nut`。

---

<a id="pitfalls-and-tradeoffs"></a>
## 五、伪装误区与关键设计权衡

### 5.1 为什么“半吊子 Header 伪装”往往比精简 Header 更危险？
现代 WAF 重点做**跨层一致性检验** `[社区逆向/经验]`：
1. **UA 与 `Sec-Ch-Ua` 版本/引擎冲突**（例如 Firefox/Safari UA 配 Chromium `Sec-Ch-Ua`，或 Chrome 144 UA 配旧版本 GREASE）。
2. **OS 与签名载荷冲突**（例如 Windows UA 配 macOS 平台头，或与抖音 `a_bogus` `browser_info` 里的 `Win32` 不一致）。
3. **`Sec-Fetch-*` 语义错误**（如跨子域写 `same-origin`，或对 JSON XHR 写 `navigate`/`document`）。
4. **TLS/H2 与高保真 Chrome Header 冲突**：在 Go 默认 `crypto/tls` JA4 + 字典序 Header + 缺 `br`/`zstd` 上叠加极精细的 Chrome 专属头（如 `Priority`），反而在强校验 WAF（Cloudflare / X / 小红书）眼中构成显著“冒充指纹”。

### 5.2 是否应该把用户浏览器的 `User-Agent` 随 Cookie 一起抓下来？ vs 扫码独立会话
- **不要盲目原样采集用户任意浏览器 UA**：用户若用 Firefox/Safari 或每隔 4 周 Chrome 自动升级，抓下来的 UA 会很快过期或与 TRACE 的 `Sec-Ch-Ua*`/抖音 `browser_info`/潜在 TLS 档案冲突；且共享同一浏览器的 Cookie 依然无法解决**双端并发 IP/TLS 分裂**问题。
- **更优解优先级**：
  1. **支持独立扫码登录的站点优先扫码**（B 站已实现；微博 SSO v2 扫码可行，见[微博协议资料 §2.1](weibo.md#qr-login)，跟进于 [#184](https://github.com/chialecode/trace/issues/184)）：TRACE 从会话诞生起就拥有独立会话与自洽 UA，不与用户日常浏览器撞车。（X 无 Web 扫码；小红书 Web 扫码涉及全套签名且存在同端互踢风险。）
  2. **仅粘贴 Cookie 的站点使用统一、现代且全栈自洽的桌面 Chrome 档案**（全站同步版本，联动 UA、`Sec-Ch-Ua*`、抖音查询串/`a_bogus`），并在设置/远程界面明确提醒避免 VPS 与本地高频并发共用主号。

<a id="go-tls-survey"></a>
### 5.3 Go 生态 TLS / HTTP/2 伪装库评估（2026-11-28）

TRACE 约束：`go 1.27.4`（[go.mod](../../go.mod#L3)）、Apache-2.0（禁 GPL/AGPL）、`trace-server` 强制 `CGO_ENABLED=0` 交叉编译（[tools/check/main.go](../../tools/check/main.go#L460-L481)）、[tools/check/main.go](../../tools/check/main.go#L301-L316) 要求精确包 `internal/site` 纯标准库。

| 候选库 | 仓库 | 许可证 | 2026-11 状态 | 能力范围 | 结论与权衡 |
| --- | --- | --- | --- | --- | --- |
| `imroc/req/v3` | https://github.com/imroc/req | **MIT**（兼容） | 活跃（`v3.61.0`，2026-08-13）`[已查证-公开一手源]` | TLS（`utls`）+ H2 帧/伪头/头顺序 + H3（`ImpersonateChrome`） | **首选备选**：纯 Go `CGO_ENABLED=0`，维护与工程化平衡最好；若需接入，在 `internal/site` 外层封装为注入式 `http.RoundTripper` 并将 UA/`Sec-Ch-Ua*` 与指纹版本 1:1 锁定。 |
| `bogdanfinn/tls-client` | https://github.com/bogdanfinn/tls-client | **BSD-3-Clause**（2025-12 已从 BSD-4 迁移；兼容） | 极活跃（`v1.17.x`–`v1.18.x`）`[已查证-公开一手源]` | 完整 TLS + H2 + H3 + `zstd` + 有序头 + 最新 Chrome 档案 | 指纹最新，但依赖树最重（`fhttp`/`quic-go-utls`）、二进制膨胀显著且需显式关闭 H3 UDP 套接字；若选用须走 [DEC-048](../decisions.md#dec-048) 7 天冷却与 `tools/deps -audit`。 |
| `refraction-networking/utls` | https://github.com/refraction-networking/utls | **BSD-3-Clause**（兼容） | 活跃（`v1.8.2`，2026-01-13）`[已查证-公开一手源]` | **仅 TLS ClientHello（JA3/JA4）**，无 H2 帧与头顺序 | **切勿单独使用**：单独配标准库 `net/http` 会破坏 `h2` ALPN 协商或暴露 Go 的 H2 指纹。 |
| `Noooste/azuretls-client` / `enetx/surf` | GitHub 同名仓库 | **MIT**（兼容） | 2026 活跃 `[已查证-公开一手源]` | TLS + H2 + H3 + 有序头 | 纯 Go 备选，但生态验证度不如前两者。 |
| `Danny-Dasilva/CycleTLS` | https://github.com/Danny-Dasilva/CycleTLS | **GPL-3.0（禁用）** | 活跃 `[已查证-公开一手源]` | TLS + H2 | **直接排除**（违反 Apache-2.0 与本仓许可政策）。 |

> **关键边界**：TLS/H2 伪装**仅在** VPS 远程版访问 X、未来接入小红书纯 HTTP 或四站升级为强拦截 Go JA4 时才值得按需启用；**它无法解决缺失动态签名、`xsec_token`、云 IP 信誉、双端会话分裂或机械轮询行为**。

---

<a id="recommendations"></a>
## 六、面向 TRACE 的分层改进建议

### 第一层：立即可做的高性价比修复（零新依赖、纯 Go 标准库，跟踪于 [#183](https://github.com/chialecode/trace/issues/183)）
1. **统一并刷新全站桌面 Chrome 档案**：在 `internal/site` 抽公共 `BrowserProfile`，将 B 站 `Chrome/131` 与抖音/微博/X `Chrome/136` 统一升级至同一现代版本（如 `Chrome/144+`），同步联动 `User-Agent`、`Sec-Ch-Ua*`（带非 Chromium 守卫防误发）与抖音 `browser_version`/`engine_version`/`a_bogus`。
2. **修复四站已确认的 Header/传输缺陷**：
   - **抖音**：`ReflowRoomInfo` / `CheckRoomsAlive` 对 `webcast.amemv.com` 停止发送 `.douyin.com` Cookie（启用 `guestNoCookie`）；`ResolvePlayRedirect` 不再用 JSON/CORS 头；持久化 `webid`。
   - **X**：按目标域名动态计算 `Sec-Fetch-Site`（`api.x.com` 改为 `same-site`）；补齐 `abs.twimg.com` 脚本请求的 `Referer`/`Sec-*`；`t.co` 兼容 HTML meta-refresh。
   - **微博**：补齐与 `Sec-Ch-Ua*` 配套的 `Sec-Fetch-Site/Mode/Dest`。
   - **B 站**：`apiTransport` 补 `ForceAttemptHTTP2: true` 恢复 HTTP/2（各站 `mediaTransport`/下载器视测试一并评估）；补动态 `Referer`、`Accept-Language`、区分 HTML/protobuf `Accept`；给 500ms 串行间隔增加随机抖动；手动粘贴 Cookie 保留 `buvid3`/`buvid4`/`b_nut`。
3. **登录与远程同步风险提示**：明确告知用户粘贴 Cookie 与浏览器共用会话（避免本地与 VPS 并发刷同一账号；X 与任何未来的高风控站点务必使用专用小号）。

### 第二层：按实测需求演进（中成本）
1. **微博 SSO 扫码登录**（跟踪于 [#184](https://github.com/chialecode/trace/issues/184)）：复用既有 `go-qrcode/v2`，从根源消除微博与浏览器的会话共享。
2. **预留可选 TLS/H2 `RoundTripper` 注入缝隙**：仅当实测证实 VPS 跑 X 或新站点被 Go JA4 拦截时，再在 `internal/site` 外层按需接入 `imroc/req/v3`（MIT），并将 TLS 指纹与 Chrome UA/`Sec-Ch-Ua*` 锁死同一版本。
3. **高敏站点定时检查引入非均匀抖动**：将 `cache-queue` 的随机间隔与长休眠思路扩展到 X（及任何未来高风控站点）的定时巡检；评估抖音大账号数开播巡检的温和限频选项。

### 第三层：须坦诚告知用户的硬边界
1. **小红书不做完整接入是理性取舍**：除非仅做桌面端单链接 SSR 归档且用户接受小号与链路失效限制，否则维护成本与主号风控不成比例。
2. **没有任何 Header 或 TLS 技巧能对主号“包过风控”**：数据中心 IP、缺失非 Cookie 设备态（如小红书 `b1`）、无前端埋点的机械定时拉取、以及验证码挑战（Arkose / BDTuring / GeeTest / 小红书 `461`/`471`）都是不可靠纯 HTTP 伪装跨越的硬边界。
