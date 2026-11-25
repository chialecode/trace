# X（原 Twitter）协议与官方 API 资料

核对日期：2026-11-22（静态协议、官方 API 与开源选型调研定稿，**对 X 全部产品、接口与媒体 CDN 域名发出的真实网络请求为 0 次**，未读取或使用任何本机 Cookie、浏览器登录数据或开发者密钥）。内容整理自 X 公开官方开发者文档与官方 GitHub 仓库（`docs.x.com`、`github.com/xdevplatform/*`，无登录无 Cookie 只读查阅）、公开社区协议资料、Go 官方模块代理 / GitHub 元数据，以及本机参考索引相关功能领域与本仓库代码只读观察到的协议现象；网页端内部接口不是 X 对 TRACE 的稳定承诺，实施与真实站点检查前必须按[第 13 节](#verify-list)在授权额度内做有界实测核对。规则见[站点规则](../dev-rules/sources-and-safety.md#site-rules)与[凭证与真实站点请求](../dev-rules/sources-and-safety.md#credentials)。

证据等级说明（全篇逐节标注查询或分析日期）：

- `官方文档`：X 官方开发者文档（`docs.x.com`）、X 官方 GitHub 公开仓库（`github.com/xdevplatform/*`，含 OpenAPI `v2.175`）、公开标准规范（RFC 6265 / RFC 6749 / RFC 8252）以及 Go 官方模块代理 / GitHub 仓库元数据与许可证原文。
- `源码阅读（功能领域，2026-11-22）`：本机参考索引对应功能领域（多站点订阅参考）与本仓库代码只读观察到的协议构造、响应字段或接入边界（严格只读事实，未复制或翻译任何代码，遵守 [DEC-044](../decisions.md#dec-044)）。
- `社区资料（2026-11-22）`：公开技术社区与协议资料的观察记录（含对受红线或 `403` 限制无法直读的条款页经公开搜索引擎交叉核实的条文摘录）。
- `推测（2026-11-22）`：基于现有协议事实、官方费率与 TRACE 架构约束作出的工程推断或费用估算。
- `unknown`：静态调研无法锁定或社区资料存在分歧、必须依赖授权后的有界真实站点检查才能确认的细节（统一汇总至[第 13 节](#verify-list)）。

本文全部示例 ID、用户名、Cookie、令牌与链接均为**明显合成示例**（如用户 `example_user` / `synthetic_creator`、稳定用户 `rest_id=1000000000000000001`、帖子 `status_id=1000000000000000009`、雪花 ID 数学合成向量 `1791257321273778509` / `2124382367241412607`、短链 `https://t.co/Synthetic01`、Space `1SyntheticSpaceId`、直播 `1SyntheticBcastId`、凭证占位 `auth_token=<合成占位>` / `ct0=<合成占位>` / `twid=<合成占位>` / `guest_id=<合成占位>` / `Bearer <公开网页端固定 Bearer 令牌>`），不包含任何真实账号、真实凭证、真实固定令牌原文或真实签名地址。

<a id="status"></a>
## 1. 验证状态

截至 2026-11-22，本资料为**纯静态调研成果**：

- **X 产品 / API / CDN 真实请求**：**`0` 次**（严格遵守 [M7 阶段计划修订 0 §3](https://github.com/chialecode/trace/blob/f8af16863dbe885cfd63de048730c07a2b91e7be/docs/delivery/stage-plan.md#research-delivery) 红线：未向 `x.com`、`twitter.com`、`mobile.twitter.com`、`mobile.x.com`、`api.x.com`、`api.twitter.com`、`t.co`、`*.twimg.com`、`periscope.tv`、`pscp.tv` 等任何产品、接口或媒体域名发过请求，未做浏览器访问，未注册开发者账号，未调用任何 API）。
- **静态已确认事实（仍待真实验证锁定线上现行行为）**：
  - 官方 API v2 截至 `2026-11-22` 的全面 **Pay-Per-Use（按量预付积分）** 转型、逐资源单价表、Owned Reads 12 端点、逐端点限流、OpenAPI `v2.175` 能力缺口，以及 OAuth 2.0 PKCE 与 `Developer Agreement`（更新于 `2026-04-27`）/ `Restricted Use Cases` / `Display Requirements` 现行条款（直接一手核对 `docs.x.com` 与 `github.com/xdevplatform/docs`，见下方查阅清单）。
  - Twitter 64 位雪花 ID（`twepoch = 1288834974657`）位布局与 **4 组合成测试向量**（已在本地 64 位纯整数脚本往返校验，零网络请求，见[§4.2](#snowflake)）。
  - TRACE 现有接入面（`internal/site` 纯标准库约束、`internal/credential` 独立文件与加密熵、`internal/download` 无 Cookie 媒体白名单、`internal/subscription` 置顶/非单调水位线与按站点调度器、`internal/live` 仅支持 HLS-fMP4 与 HTTP-FLV、`cmd/trace-server` `CGO_ENABLED=0` Linux 交叉编译边界）（`源码阅读（仓库代码，2026-11-22）`）。
  - Go 开源生态中受维护、许可合规、可直接导入的 X 官方 SDK / 网页客户端 / 反爬头库均为 **0 个**（选型证据表另见[X 开源客户端与协议依赖选型](2026-11-22-x.md)）。
- **核心 `unknown` 汇总**：共整理为 **9 项网页路线 MVP 核心必验项（`V-01`～`V-09`）** 与 **3 项条件验证项（`V-10`～`V-12`，仅在评估搜索回溯、直播/Spaces 或官方 API 路线时执行）**，覆盖 `settings.json` 无 Cookie 对照与 `kdt` 影响、`ct0` 服务端轮换与 `353` 恢复、`t.co` 跳转与登录墙现状、`x-client-transaction-id` / `x-xp-forwarded-for` 实测强制矩阵与 2026 Vite `x-web` 静态结构、单帖 `note_tweet`/混排/降级、时间线分页/深度/假空壳、`pbs.twimg.com` 与 `video.twimg.com` 无 Cookie + `Range` + HLS 比例、“我的”只读列表与非 Premium 书签文件夹，以及不存在/受保护错误码，详见[第 13 节](#verify-list)。
- **产品原则基线**：延续 [DEC-066](../decisions.md#dec-066)（M7 X 作为第四个站点，先静态调研并比较网页与官方 API 路线）、[DEC-043](../decisions.md#dec-043)（按站点独立登录、先登录再请求、无访客产品模式、额度/退避/熔断独立、小字站点标签）、[DEC-044](../decisions.md#dec-044)（只按规格洁净室独立实现，不复制或翻译参考代码）、[DEC-047](../decisions.md#dec-047)（“我的”严格只读且仅驻留内存）、[DEC-056](../decisions.md#dec-056)（转发存转发者 `forwards/` 目录并记录 `via`）、[DEC-062](../decisions.md#dec-062)（`trace-server` 零 GUI 依赖、只同步订阅与 Cookie、不做远端续期与全量缓存）与 [DEC-065](../decisions.md#dec-065)（微博无直播/零请求排除先例）。

### 查阅的官方公开文档与官方 GitHub 仓库清单（查询日期：`2026-11-22`）

| 序号 | 官方页面 / 官方仓库 URL | 查阅方式与状态 | 用途与核对日期 |
| --- | --- | --- | --- |
| 1 | `https://github.com/xdevplatform/docs`（含 `openapi.json` `v2.175`，`main` 推送 `2026-11-22`） | 直接只读（GitHub 公开官方仓库，无登录） | 官方文档全目录与 OpenAPI Schema 一手核对（`2026-11-22`） |
| 2 | `https://docs.x.com/llms.txt`、`https://docs.x.com/x-api/llms.txt` | 直接只读（无登录无 Cookie） | 官方文档站点索引核对（`2026-11-22`） |
| 3 | `https://docs.x.com/x-api/getting-started/pricing` | 直接只读（`x-api/getting-started/pricing.mdx`） | Pay-Per-Use 完整单价表、Owned Reads、24h 去重、消费上限与 xAI 返积分（`2026-11-22`） |
| 4 | `https://docs.x.com/x-api/getting-started/free-credits` | 直接只读（`free-credits.mdx`） | $20 + 最高 $50 赠金资格与 3 个月过期规则（`2026-11-22`） |
| 5 | `https://docs.x.com/x-api/getting-started/about-x-api`、`getting-access` | 直接只读 | X API v2 概览、控制台 `console.x.com` 开户与凭证类型（`2026-11-22`） |
| 6 | `https://docs.x.com/enterprise-api/getting-started/pricing` | 直接只读 | Enterprise 与 Pay-Per-Use 定位差异（`2026-11-22`） |
| 7 | `https://docs.x.com/x-api/fundamentals/post-cap` | 直接只读 | 300 万/月 Post 读取硬上限、按 App 计费与失败请求不计费（`2026-11-22`） |
| 8 | `https://docs.x.com/x-api/fundamentals/rate-limits`、`https://docs.x.com/fundamentals/rate-limits` | 直接只读 | 逐端点 15 分钟 / 24 小时 Per-App 与 Per-User 限流表（`2026-11-22`） |
| 9 | `https://docs.x.com/changelog` | 直接只读 | 2025-10 至 2026-11 官方发布、调价、写操作移除与旧档位退场记录（`2026-11-22`） |
| 10 | `https://docs.x.com/fundamentals/authentication/oauth-2-0/overview`、`authorization-code`、`user-access-token`、`application-only` | 直接只读 | OAuth 2.0 PKCE Native App、30 秒授权码、2 小时 `access_token`、`offline.access`、精确 `redirect_uri` 与 Bearer Token 规则（`2026-11-22`） |
| 11 | `https://docs.x.com/x-api/fundamentals/fields`、`expansions`、`edit-posts`、`data-dictionary/reference`、`response-codes-and-errors` | 直接只读 | 字段模型、`note_post`、编辑历史、媒体 `variants`、错误码与部分错误（`2026-11-22`） |
| 12 | `https://docs.x.com/x-api/posts/timelines/introduction`、`integrate`；`lookup/introduction`；`search/introduction`；`likes/introduction` | 直接只读 | 3,200 / 800 时间线硬限、转帖截断、`search/all` PPU 可用性与 Likes 规则（`2026-11-22`） |
| 13 | `https://docs.x.com/x-api/posts/bookmarks/introduction`、`integrate`、`quickstart/bookmarks-lookup`；`users/get-bookmarks`、`get-bookmarks-by-folder-id`、`get-following` | 直接只读 | 书签与文件夹仅返 ID 核对、关注只读接口（`2026-11-22`） |
| 14 | `https://docs.x.com/x-api/users/lookup/introduction`、`x-api/media/introduction`、`x-api/media/get-media-by-media-key`、`x-api/articles/introduction` | 直接只读 | 用户资料字段、媒体对象、Articles 仅发稿写接口（`2026-11-22`） |
| 15 | `https://docs.x.com/x-api/spaces/introduction`、`x-api/compliance/batch-compliance/introduction` | 直接只读 | Spaces 结束即失效且无流 URL；Batch Compliance 删改同步机制（`2026-11-22`） |
| 16 | `https://docs.x.com/livestream-api/introduction`、`broadcasts/overview`、`broadcasts/get-broadcast-by-id` | 直接只读 | Livestream API 仅限播主本人所有权且无拉流地址（`2026-11-22`） |
| 17 | `https://docs.x.com/developer-terms/agreement`（Last Updated: `2026-04-27`）、`ppu-agreement`、`policy`、`display-requirements`、`restricted-use-cases` | 直接只读 | 开发者协议、PPU 协议、政策、展示规范、多应用 / BYO 限制与 24 小时删除同步条款（`2026-11-22`） |
| 18 | `https://github.com/xdevplatform/xdk`、`xurl`、`playground`、`chat-xdk` | 直接只读（GitHub REST API，无登录） | 核实 X 官方无 Go API v2 SDK（`2026-11-22`） |
| 19 | `https://help.x.com/en/rules-and-policies/x-automation`、`x-rules`、`platform-manipulation`、`x-limits` | 直接读取遇 HTTP `403`（未绕过）；经公开搜索交叉核实要点 | 自动化规则与平台操控政策（`2026-11-22`，辅佐内容标 `社区资料（2026-11-22）`） |
| 20 | `https://x.com/en/tos`（红线禁直访） | 零直访请求；仅经公开搜索核实条款要点 | 服务条款反抓取与每百万帖 $15,000 违约金条款（`2026-11-22`，标 `社区资料（2026-11-22）`） |

---

<a id="auth"></a>
## 2. 登录与凭证：网页路线（R-A）

<a id="cookie-clean"></a>
### 2.1 粘贴 Cookie 字段分类、清洗与脱敏规则

按 TRACE 凭证隔离设计（[架构设计 §5](../design/architecture.md#credentials)），用户在登录页或“设置 › 账号”的 X 一栏粘贴浏览器已登录 `x.com` 的 `Cookie` 字符串；后端清洗、经[§2.4](#login-verify)严格无 Cookie 对照校验确认有效后，使用 Windows DPAPI（Linux `trace-server` 使用 AES-256-GCM + `secret.key`）存入独立文件 **`credentials/x.bin`** 与独立加密熵 **`trace:x:credential:v1`**（`源码阅读（仓库代码，2026-11-22）`）。

| 分类 | Cookie 字段名 | 作用与典型形态 | 处理策略与清洗规则 | 证据等级 |
| --- | --- | --- | --- | --- |
| **核心身份态（本地预检必需）** | `auth_token` | 主会话凭证（`HttpOnly` / `Secure`，约 40 字符十六进制串，占位 `auth_token=<合成占位>`） | **必需**：缺失直接本地报 `invalid_input`（0 网络请求）；原样保留、加密落盘、严禁明文进日志 | `源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）` |
| **核心 CSRF 态（本地预检必需）** | `ct0` | CSRF 双提交令牌（登录态约 160 字符十六进制，访客态 32 字符，占位 `ct0=<合成占位>`） | **必需**：校验字符集 `^[A-Za-z0-9]+$`（拒绝 `\r`/`\n`），原样保留并镜像至 `x-csrf-token` 请求头；服务端通过 `Set-Cookie` 轮换时经 `MergeContextCookies` 同步 | `社区资料（2026-11-22）` |
| **身份交叉核对态（若存在则保留）** | `twid` | URL 编码用户 ID（格式为 `u%3D<rest_id>`，占位 `twid=<合成占位>`） | 保留用于与服务端校验返回的 `rest_id` 交叉核对；**绝不单独当作已登录证明** | `社区资料（2026-11-22）` |
| **条件配套态（若存在则保留）** | `guest_id` | 访客标识（格式为 `v1%3A<数字>`，占位 `guest_id=<合成占位>`） | **强烈建议保留**：若端点要求 `x-xp-forwarded-for`（XPFF），其 AES 密钥派生命中 `SHA-256(base_key + urlencoded(guest_id))`，必须与 Cookie 中 `guest_id` 一致 | `社区资料（2026-11-22）` |
| **设备信任态（可选保留）** | `kdt` | Known Device Token（已知设备标识，占位 `kdt=<合成占位>`） | 可选保留；对长期锁号风险评分的实际影响记为 `unknown`（列 **V-01**） | `社区资料（2026-11-22）` / `unknown` |
| **应剥离的临时 / 访客 / 广告 / WAF 字段** | `att`、`gt`、`personalization_id`、`guest_id_marketing`、`guest_id_ads`、`lang`、`external_referer`、`__cf_bm` | 登录流临时令牌、未登录访客令牌、广告/追踪/语言与 ~30 分钟短命 Cloudflare 机器人检测 Cookie | **粘贴清洗时统一剥离**，减小请求头体积、避免超出清洗后 `32 KiB`（`32768` 字节）出站头上限并防止过期 `gt`/`__cf_bm` 污染请求 | `社区资料（2026-11-22）`、`推测（2026-11-22）` |

**清洗与脱敏规则**（对齐 `internal/credential` 现有三站规范，`源码阅读（仓库代码，2026-11-22）`）：

1. 原始输入超 `MaxCookieInputBytes = 65536`（64 KiB）立即报 `ErrInputTooLarge`；拒绝任何控制字符（`<0x20`、`0x7f`、`0x80..0x9f`、裸 `\r`、换行头注入）；清洗后出站 `Cookie:` 头超 `MaxCleanedCookieHeaderBytes = 32768`（32 KiB）报 `ErrCleanedCookieTooLarge`；错误消息绝不回显原始粘贴文本。
2. 自动剥离可选前缀 `Cookie:`，按 `;` 拆分 `name=value`（保留值内 `=`），同名键保留最后一个非空值，严格区分大小写，白名单仅保留 `auth_token`、`ct0`、`twid`、`guest_id`、`kdt`。
3. **`ct0` 预热与要求协调**：本机多站点订阅参考曾采用“仅配置 `auth_token` → 首次发一次 `GET https://x.com` 预热从 `Set-Cookie` 拿 `ct0`”的最小配置做法（`源码阅读（多站点订阅参考，2026-11-22）`）；但面向 TRACE 桌面用户从已登录浏览器直接复制 `Cookie` 的场景，要求同时存在非空 `auth_token` 与 `ct0` 可免去登录校验时的额外首页预热请求并提前拦截无效粘贴（`推测（2026-11-22）`）。
4. **全局脱敏红线**：`credential.Manager.RedactString` 必须登记 `x` 凭证仓，掩码所有存储的 `auth_token`、`ct0`、`twid`、`kdt`、`guest_id`（以及若启用官方 API 时的 `access_token`、`refresh_token`、`client_secret`）的原文与 URL 编解码形态，并补充 `x-csrf-token` 请求头与 JSON 键正则脱敏。

<a id="csrf-headers"></a>
### 2.2 `ct0` 与 `x-csrf-token` 双提交校验、轮换红线与必需请求头

- **双提交机制**：X 网页后端严格比对请求头 `Cookie: ... ct0=<V>` 与 `x-csrf-token: <V>`；缺失或不匹配返回 **HTTP `403` + JSON 错误码 `353`**（`"This request requires a matching csrf cookie and header."`）（`社区资料（2026-11-22）`）。
- **轮换红线（关键避坑）**：**严禁客户端在已登录会话上自行随机生成新十六进制串覆盖 `ct0`**（旧访客激活接口可传随机 32 位 UUID，但在已登录会话上客户端自造 `ct0` 会直接破坏会话且无法从 `353` 恢复）（`社区资料（2026-11-22）`）。正确做法是：
  1. 监听每次响应的 `Set-Cookie: ct0=...`（X 约每 `~4` 小时周期性轮换 `ct0`，或在 `353` 响应中同时下发新 `ct0`）；
  2. 校验新值匹配 `^[A-Za-z0-9]+$` 后，通过 `MergeContextCookies` 原子同步至 `x.bin` 与后续 `x-csrf-token` 头；
  3. 若使用 `http.CookieJar`，须清理 host-only 与 `.x.com` 域 Cookie 重复项，确保同一请求绝不发送两个同名 `ct0`；
  4. 收到 `353` 且响应未下发可采纳的新 `ct0` 时，直接判定 `auth_required` 停止本轮（实际 `353` 恢复边界列 **V-02**）。

**已登录 X 网页接口必需请求头矩阵**（`源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）`）：

| 请求头 | 网页 REST / GraphQL（`x.com/i/api/*`） | 媒体 CDN（`pbs.twimg.com` / `video.twimg.com`） | 说明 |
| --- | --- | --- | --- |
| `Authorization` | `Bearer <公开网页端固定 Bearer 令牌>` | 不带 | X 网页前端公开静态令牌，非用户私密凭证（见 [§2.3](#bearer-guest)） |
| `x-csrf-token` | `<ct0>`（与 Cookie `ct0` 逐字符一致） | 不带 | 防 CSRF 双提交必需 |
| `x-twitter-auth-type` | `OAuth2Session` | 不带 | 标记已登录网页会话；未登录/访客对照请求**不得**携带 |
| `x-twitter-active-user` | `yes` | 不带 | 网页客户端标准头 |
| `x-twitter-client-language` | `en`（或 `zh-cn`） | 不带 | 固定值，保持一致 |
| `x-client-transaction-id` | 按 [§10.3](#anti-bot) 动态生成 | 不带 | 对 `UserTweetsAndReplies`、`SearchTimeline` 等端点已强制要求，建议统一接入（覆盖矩阵列 **V-04**） |
| `x-xp-forwarded-for` | 条件携带（按 [§10.3](#anti-bot)） | 不带 | 2025–2026 对搜索等敏感端点引入（覆盖矩阵列 **V-04**） |
| `Referer` / `Origin` | `https://x.com/` / `https://x.com` | 不带（均非必需） | — |
| `User-Agent` / `Sec-Fetch-*` | 固定现代桌面 Chrome UA 与匹配浏览器安全头 | 固定桌面 Chrome UA | 与导出 Cookie 浏览器族一致以降低风控分 |
| `Cookie` | 仅清洗后的登录 Cookie | **强制删除（`Del("Cookie")`）** | 严禁向任何 `*.twimg.com` 或 `t.co` 泄露登录态 |

<a id="bearer-guest"></a>
### 2.3 公开网页 Bearer 令牌、访客令牌与未登录访问边界

1. **网页端公开固定 Bearer 令牌**：X 网页端所有前端 JS 包内置同一串公开静态 Bearer 令牌（区别于历史移动客户端公开令牌），本身不是用户账号密钥，但所有 `x.com/i/api/*` 与 `api.x.com/*` 网页接口均要求在 `Authorization` 头中携带它（`源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）`；2026-11 线上持续有效性列 **V-01**）。本文与公开文档不粘贴令牌字面值，统一记作 `Bearer <公开网页端固定 Bearer 令牌>`。
2. **访客令牌（Guest Token）**：
   - 移动端 / 传统接口：`POST https://api.x.com/1.1/guest/activate.json`（携带公开 Bearer 令牌）返回 `{"guest_token": "..."}`，置于 `x-guest-token` 请求头，社区观察有效期约 **`3` 小时**且受严格 IP 限流（`源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）`）。
   - 网页端备选：无登录访问公开页 HTML（带 `?mx=2`）可从内联脚本提取 `gt` Cookie 并映射到 `x-guest-token`（`源码阅读（多站点订阅参考，2026-11-22）`）。
3. **未登录访问现状与 TRACE 边界**（`源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）`；2026-11 现状列 **V-03**）：
   - X 自 2023 年中起在网页端实施全面登录墙。
   - **Syndication 嵌入接口**（`https://cdn.syndication.twimg.com/tweet-result?id=<id>&token=<token>`，其中 `token` 由 `((Number(id) / 1e15) * Math.PI).toString(36)` 剥除首尾 `0`/`.` 生成）：历史上可无登录取单条公开、非敏感、非受保护、非年龄限制帖子，但受 IP 限流，**不支持用户时间线、完整线程、NSFW/敏感帖或任何私有列表**。
   - **GraphQL 访客态**：参考实现仅在 `UserByScreenName` 允许无 `auth_token` 回退；`TweetResultByRestId` 访客态不稳定且拦截敏感/年龄限制/受保护/回复；未登录 `UserTweets` 已被封锁或仅返约 ~100 条乱序热门帖；`UserByRestId`、`UserMedia`、`SearchTimeline`、`Following`、`Bookmarks`、`Likes` 均严格要求登录。
   - **TRACE 结论**：遵守 [DEC-018](../decisions.md#dec-018) 与 [DEC-043](../decisions.md#dec-043)，**不做访客产品模式**，X 浏览、缓存与订阅均要求先登录；访客态/无 Cookie 仅用于登录严格校验对照（**V-01**）与识别被踢回访客态。

<a id="login-verify"></a>
### 2.4 会话有效期、失效条件与严格登录确认（含无 Cookie 对照）

1. **会话有效期与不可续期性**（`社区资料（2026-11-22）`）：
   - `auth_token` Cookie 标称 `Max-Age` 常达数月至 ~1 年，**无网页刷新令牌（refresh token）续期接口**，与抖音、微博粘贴 Cookie 一致（遵循 [DEC-031](../decisions.md#dec-031)，`Renewal()` 固定 `Available: false, Reason: "粘贴的 Cookie 不续期"`）。
   - **失效触发条件**：在原浏览器点退出登录（服务端立即吊销当前 `auth_token`）、修改密码或触发安全重置（吊销全部会话）、风控锁号（code `326`）、账号封禁（code `64`）或持续异常导致接口返回空 `404`。
2. **严格确认“确实已登录”（两步校验 + 无 Cookie 对照）**：
   - 本机多站点订阅参考缺乏专门的登录校验端点（仅靠业务接口事后 `401`/`403` 暴露失效，`源码阅读（多站点订阅参考，2026-11-22）`），不能直接满足 TRACE[站点规则](../dev-rules/sources-and-safety.md#site-rules)。
   - **推荐主方案（免疫 GraphQL `queryId` 变动）**（`社区资料（2026-11-22）`、`推测（2026-11-22）`；线上细节列 **V-01**）：
     1. **第 1 步（严格鉴权）**：禁用自动重定向，调 **`GET https://api.x.com/1.1/account/settings.json`**（X 真实网页端启动必调的 REST v1.1 端点；`x.com/i/api/1.1/account/settings.json` 于 2026-11-23 实际使用中返回 code 34，不再使用；和网页端一样带 `x-client-transaction-id`，否则 `api.x.com` 也回 code 34，**不受 GraphQL `queryId` 每周漂移影响**；备选为 `GET /1.1/account/verify_credentials.json` 或 GraphQL `Viewer`）。
        - **无 Cookie 对照（红线门禁）**：不带 Cookie（仅带公开 Bearer 或不带认证）预期返回 **HTTP `401` + JSON 错误码 `32`**（`Could not authenticate you`）或 `89`（`Invalid or expired token`），**绝不返回有效 `screen_name`**。
        - **有效登录判据**：必须为 HTTP `200`、合法 JSON、`errors` 为空且返回非空字符串 `screen_name`（匹配 `^[A-Za-z0-9_]{1,15}$`）。
     2. **第 2 步（补齐稳定 `rest_id` 与头像昵称）**：用第 1 步返回的 `screen_name` 调 `UserByScreenName` 获取永久 `rest_id`、显示名 `name` 与头像 `profile_image_url_https`（对齐微博先 `getBasicInfo` 再 `profile/info?screen_name=` 的成熟先例），并核对返回的 `screen_name` 大小写无关一致；若用户粘贴了 `twid`，额外交叉核对 `twid` 解码出的 `rest_id` 与返回 `rest_id` 一致，不一致则拒绝保存且不改动既有凭证。
   - **严禁误判**：HTTP `200` 附 `errors[]`、空体、HTML 登录页或用访客态查公开用户资料，一律不得判定为已登录；校验中途遇到 `429` / `326` / `226` 归类为 `rate_limited` / `risk_control`，绝不清空既有凭证。

<a id="domain-password"></a>
### 2.5 `x.com` 与 `twitter.com` 域迁移及扫码 / 账号密码登录评估

1. **`x.com` vs `twitter.com` Cookie 通用性**（`源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）`）：
   - X 于 **2024-05-17** 完成主域名从 `twitter.com` 向 `x.com` 的全面迁移；现代网页接口基础路径统一为 `https://x.com/i/api`（部分 v1.1 亦可访问 `https://api.x.com`）。
   - 按 RFC 6265，`.twitter.com` 与 `.x.com` 在浏览器中属于不同域；用户在粘贴时**必须从已登录的 `x.com` 标签页复制**，TRACE 提取清洗后的 `auth_token` / `ct0` 统一仅发往 `x.com` / `api.x.com`（旧 `.twitter.com` 残留过期 Cookie 在登录校验阶段将被服务端直接拒收）。
2. **扫码登录可行性**：X 网页端**无标准二维码扫码登录流程**（不同于 B 站与微博），无需为网页路线规划扫码（`社区资料（2026-11-22）`）。
3. **账号密码登录可行性与高危评估**（`源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）`）：
   - X 登录状态机位于 `POST /i/api/1.1/onboarding/task.json?flow_name=login`（依 `flow_token` 串联 `LoginEnterUserIdentifier` → 可选 `LoginEnterAlternateIdentifierSubtask` → `LoginEnterPassword` → `AccountDuplicationCheck` → `LoginAcid` / `LoginTwoFactorAuthChallenge`）。
   - **当前现状**：① 移动端 OAuth 1.0a 路径自 **2025-10 X 引入移动端客户端完整性校验（mobile client attestation）后已完全失效**；② 网页端路径依赖 `guest_token`、`att`、`x-client-transaction-id`、`js_inst?c_name=ui_metrics` 浏览器遥测，高频注入 **Arkose Labs FunCaptcha**（`fc-token`）及邮箱/短信/TOTP 二步验证，非真实浏览器提交密码极易直接触发账号锁定（`326`）或封禁。
   - **结论**：TRACE **绝不实现账号密码登录、绝不保存用户密码**；网页路线仅采用粘贴 Cookie 登录，官方 API 路线采用系统浏览器 OAuth 2.0 PKCE 授权。

---

<a id="official-api"></a>
## 3. 官方 API 路线与双路线对比（R-B）

本节全部官方价格、限流、OpenAPI `v2.175` Schema 与开发者协议事实均于 **`2026-11-22`** 直接只读核对自 `docs.x.com` 与 `github.com/xdevplatform/docs`（`官方文档`）。

<a id="api-pricing"></a>
### 3.1 2026 档位演进与 Pay-Per-Use 按量计费体系

**关键变化：截至 `2026-11-22`，X API v2 自助服务已全面淘汰传统月度订阅（Free / Basic / Pro），改为「预付费积分按量计费（Pay-Per-Use）+ 定制合同（Enterprise）」双轨制。**

- **2025–2026 演进时间线**：
  - `2025-10-20`：启动 Pay-Per-Use 闭门试点（`官方文档`，`docs.x.com/changelog`）。
  - `2026-02-06`：**Pay-Per-Use 正式全面上线**（新控制台 `console.x.com`）；传统 Free 档对新自助开发者退场（仅保留经审批的 Public Utility Apps 免费额度；近期活跃旧 Free 账号获一次性 $10 代金券）（`官方文档`，`docs.x.com/changelog`）。
  - `2026-04-16`（`2026-04-20` 生效）：推出 **Owned Reads** 折扣（`$0.001`/资源）；写操作大幅提价（`POST /2/tweets` 升至 `$0.015`，含 URL 帖 `$0.20`）；**自助档移除关注、点赞、引用发帖写操作**（`官方文档`，`docs.x.com/changelog`；写操作性质由 `社区资料（2026-11-22）` 佐证）。
  - `2026-06-01`：旧 **Basic（$200/月）** 存量订户在账单周期结束后强制迁入 Pay-Per-Use（`社区资料（2026-11-22）`）。
  - `2026-08-14` / `2026-09-01`：旧 **Pro（$5,000/月）** 正式废弃并迁入 Pay-Per-Use 或 Enterprise（`社区资料（2026-11-22）`）。
  - `2026-10-02`（`2026-10-13` 调整）：新付费账号赠金活动——首次绑定合格银行卡并首次充值赠 **$20**，首次自动充值匹配赠金最高 **$50**（合计最高 **$70**；3 个月过期、优先消耗、不可提现）（`官方文档`，`docs.x.com/x-api/getting-started/free-credits`）。

#### 现行方案与读取费率表（`官方文档`，`docs.x.com/x-api/getting-started/pricing`，2026-11-22）

| 档位 / 模式 | 当前状态 | 计费、充值与上限规则 |
| --- | --- | --- |
| **Free / Basic / Pro** | **已退场 / 废弃** | 原 $0 / $200/月 / $5,000/月均不再对新自助开发者开放 |
| **Pay-Per-Use（PPU）** | **当前唯一自助模式**（`console.x.com`） | 无月费、无最低消费；**预付积分**（不可退款、不可跨账号转移、购入积分无过期日；**硬性要求绑定已验证手机号及外币信用卡/借记卡**）；支持月度**消费上限（Spending limit）**与自动充值（5 分钟最多 1 次，余额 ≤ $0 暂停自动充值）；月消费 $200–$1,000+ 返 10%–20% xAI 积分；**每账单周期硬上限 300 万次 Post 读取**（按 App 统计，失败请求不计费） |
| **Enterprise** | 现行（需销售签约） | 定制合同价（非公开，通常数万美元/月量级，`推测（2026-11-22）`）；完全不适用于个人桌面工具 |

| 读取资源类型（按响应返回的对象数计费） | 标准单价 | 适用说明 |
| --- | --- | --- |
| **Posts: Read** | **`$0.005` / 条**（1,000 条 = `$5.00`） | 单帖查询、用户时间线、搜索、列表帖子、书签文件夹二次水合等 |
| **User: Read** | **`$0.010` / 个**（100 个 = `$1.00`） | 按用户名或 ID 查用户资料 |
| **Following / Followers: Read** | **`$0.010` / 个** | 查询**他人**的关注/粉丝列表 |
| **List / Space / Community / Note / Profile Update: Read** | **`$0.005` / 个** | 列表、Spaces、社区、注释、资料变更读取 |
| **Like / Mute / Block: Read** | **`$0.001` / 个** | 点赞、静音、屏蔽读取 |
| **Owned Reads（自有数据折扣）** | **`$0.001` / 资源**（1,000 个 = `$1.00`） | **仅当 `{id}` 等于当前 OAuth 认证用户且该用户是该 Developer App 所有者**时生效；覆盖 **12 个 GET 端点**：`/tweets`、`/mentions`、`/liked_tweets`、`/bookmarks`、`/followers`、`/following`、`/blocking`、`/muting`、`/owned_lists`、`/followed_lists`、`/list_memberships`、`/pinned_lists` |
| **24 小时 UTC 去重** | 同 UTC 日内同资源计 1 次 | 按 UTC 00:00 重置；官方明示为 **soft guarantee**（服务异常边界可能重复计费） |
| **Expansions（`includes.*`）** | 社区观察按各自资源类型另计（`社区资料（2026-11-22）`） | 如 `expansions=author_id,referenced_tweets.id` 返回的用户/原帖计入对应资源消费；`media`/`poll` 扩展是否单计官方未明示，记为 `unknown`（列 **V-12**） |

<a id="api-cost-limits"></a>
### 3.2 TRACE 个人使用费用测算与逐端点限流表

- **TRACE 典型个人使用费用测算**（`推测（2026-11-22）`，依据上述 `官方文档` 单价）：
  - **日常增量订阅检查**（30 个订阅账号，人均日更 2 帖）：1,800 帖/月 × `$0.005` = **`$9.00/月`**；叠加 `referenced_tweets.id` 展开转帖/引用原帖及跨日查资料，实际约 **`$10–$20/月`**。
  - **单账号“全部缓存”一次**（3,200 帖时间线上限）：3,200 × `$0.005` = **`$16.00/账号`**（不含引用原帖与用户展开）；缓存 10 个活跃账号即 **`$160+`**。
  - **“我的”自有数据**（用户自建 App 享 Owned Reads `$0.001`）：500 关注（`$0.50`）+ 800 书签（`$0.80`）+ 1,000 喜欢（`$1.00`）≈ `$2.30`；**但书签文件夹内帖子端点仅返回 Post ID，必须再调 `GET /2/tweets?ids=...` 按标准 `$0.005/帖` 水合**（不在 Owned Reads 列表内）。
  - **媒体 CDN 下载（`pbs.twimg.com` / `video.twimg.com`）**：不消耗 API 积分（`社区资料（2026-11-22）`、`推测（2026-11-22）`）。

- **核心只读端点 15 分钟限流表**（`官方文档`，`docs.x.com/x-api/fundamentals/rate-limits`，2026-11-22；所有响应返回 `x-rate-limit-limit` / `remaining` / `reset`，超限 HTTP `429` JSON 错误码 `88`，且 `type` 字段区分 15 分钟窗、24 小时窗与 `usage-capped` 积分耗尽）：

| 端点 | Per App（Bearer） | Per User（OAuth 2.0 / 1.0a） | 备注 |
| --- | --- | --- | --- |
| `GET /2/users/by/username/:username`、`/2/users/:id` | 300 / 15min | 900 / 15min | 批量查询最多 100 项 |
| `GET /2/users/me` | — | 75 / 15min | 仅用户上下文（严格登录确认） |
| `GET /2/tweets/:id` / `GET /2/tweets` | 450 / 3,500 per 15min | 900 / 5,000 per 15min | 批量最多 100 ID |
| `GET /2/users/:id/tweets` | 10,000 / 15min | 900 / 15min | `max_results=5..100`；上限 3,200 帖（`exclude=replies` 时仅 **800**） |
| `GET /2/tweets/search/recent` | 450 / 15min | 300 / 15min | 近 7 天，`max_results=10..100` |
| `GET /2/tweets/search/all` | 1/sec, 300 / 15min | 1/sec | 回溯至 2006，PPU 可用；`2026-05-04` 起关键词搜索不返转帖 |
| `GET /2/users/:id/following`（及 `/2/users/me/following`） | 300 / 15min | 300 / 15min | `max_results=1..1000` |
| `GET /2/users/:id/bookmarks` | — | 180 / 15min | 仅用户上下文（`bookmark.read`） |
| `GET /2/users/:id/bookmarks/folders` / `.../:folder_id` | 50 / 15min | 50 / 15min | OpenAPI 仅定义用户上下文；文件夹帖子端点**仅返 ID** |
| `GET /2/users/:id/liked_tweets` | 75 / 15min | 75 / 15min | 仅用户上下文（`like.read`），仅本人可见 |
| `GET /2/users/:id/owned_lists` / `pinned_lists` | 15 / 15min | 15 / 15min | 列表帖子 `/2/lists/:id/tweets` 为 900 / 15min |
| `GET /2/spaces/:id` / `by/creator_ids` | 300 / 15min（后者兼 1/sec） | 300 / 15min（后者兼 1/sec） | 仅状态元数据，无音频流 |
| `POST/GET /2/compliance/jobs`、`GET /2/usage/tweets` | 150 / 50 per 15min | — | 合规删改同步与用量查询 |

<a id="api-coverage"></a>
### 3.3 TRACE 需求覆盖矩阵（`官方文档`，OpenAPI `v2.175`，2026-11-22）

| TRACE 需求 | 官方 API v2 端点与字段 | 覆盖度 | 关键缺口与限制 |
| --- | --- | --- | --- |
| **用户查找（用户名 / `rest_id`）** | `GET /2/users/by/username/{u}`、`GET /2/users/{id}`、`GET /2/users/me` | **完整覆盖** | App-Only 无法查 `/2/users/me` 或受保护账号；须 User Context |
| **用户时间线、订阅检查与全部缓存** | `GET /2/users/{id}/tweets`（`since_id`、`until_id`、`exclude`、`pagination_token`） | **部分覆盖** | ① 硬上限 **3,200 帖**，**传 `exclude=replies` 时骤降至 800 帖**；② **无 `UserMedia` 专属媒体页端点**；③ 超 140 字转帖 `text` 被截断，须展开 `referenced_tweets.id`（另计费）；④ 补 3,200 帖以前须借 `search/all`（`$0.005/帖`，不含转帖） |
| **单帖、长帖、线程、编辑历史** | `GET /2/tweets/{id}`（`note_post`、`edit_history_post_ids`、`edit_controls`、`referenced_posts`、`conversation_id`、`withheld`） | **大部分覆盖** | ① **单帖端点不返作者自回复线程**，须按 `conversation_id` 另调搜索计费拼接；② `/2/articles/*` 仅为发稿写接口，`Post.article` 是否返回完整正文记为 `unknown`（**V-12**） |
| **原图、GIF、多档视频直链** | `expansions=attachments.media_keys` + `media.fields=type,url,variants,preview_image_url,width,height,duration_ms,alt_text`；`GET /2/media/{media_key}` | **完整覆盖** | OpenAPI `MediaVariants` 直接返回 `[{bit_rate, content_type, url}]`（含多档 `video/mp4` 与 `.m3u8`），`animated_gif` 同含 MP4；图片可拼 `?name=orig` |
| **我的 › 关注与批量导入** | `GET /2/users/{id}/following`、`/2/users/me/following` | **完整覆盖（只读）** | 2026-04-20 移除的是关注/点赞**写操作**，不影响只读导入 |
| **我的 › 书签与书签文件夹** | `GET /2/users/{id}/bookmarks`、`/bookmarks/folders`、`/folders/{folder_id}` | **部分覆盖** | ① 书签限**最近 800 条滑动窗口**（`社区资料（2026-11-22）`）；② **文件夹端点仅返回 `[{id}]`**，二次拉取须按 `$0.005/帖` 计费 |
| **我的 › 喜欢（Likes）** | `GET /2/users/{id}/liked_tweets` | **仅本人覆盖** | 2024-06-12 起仅本人可见，契合 TRACE “我的”定位 |
| **我的 › 列表（Lists）** | `/owned_lists`、`/followed_lists`、`/pinned_lists`、`/2/lists/{id}/tweets` | **完整覆盖** | 目录限流较紧（15 / 15min） |
| **Spaces（语音与回放）** | `GET /2/spaces/{id}`、`by/creator_ids` | **不可录制 / 无回放** | `Space` Schema **无任何音频流或回放 URL**，且官方明示 **Space 结束后即刻不可查询并报错** |
| **视频直播（Broadcasts）** | `GET /2/broadcasts`、`/{id}` | **不可用** | Livestream API **仅限播主本人访问自己发起的直播**（查他人返回 `404`），且 **无任何拉流地址** |
| **零付费基础读取** | — | **不支持** | 无 Free 档，未绑卡充值无法读取任何数据 |

<a id="api-auth-byo"></a>
### 3.4 Windows 桌面端认证（OAuth 2.0 PKCE vs App-Only）与密钥分发 / BYO 阻碍

1. **OAuth 2.0 Authorization Code with PKCE vs App-Only Bearer**（`官方文档`，`docs.x.com/fundamentals/authentication/oauth-2-0/*`，2026-11-22）：
   - **App-Only Bearer Token**：无用户身份，不能调 `/2/users/me`、书签、本人喜欢、受保护账号，无 Owned Reads 折扣，且官方明示不得分发给终端客户端，**不适用**。
   - **OAuth 2.0 PKCE（Native App / Public Client）**：无 `client_secret`，仅用 `client_id` + 随机 `code_verifier`/`code_challenge`（`S256`）+ `state`。授权地址 `https://x.com/i/oauth2/authorize`，最小只读 scopes：`tweet.read users.read follows.read bookmark.read like.read list.read space.read offline.access`；换票地址 `POST https://api.x.com/2/oauth2/token`；**授权码 `code` 仅 30 秒有效**；**`access_token` 仅 2 小时有效**，靠 `offline.access` 下发的 `refresh_token` 续期；退出时调 `POST https://api.x.com/2/oauth2/revoke`。
   - **本机回调地址**：官方要求 `redirect_uri` **精确匹配**（RFC 6749 §10.6；是否支持 RFC 8252 本地回环动态端口通配为 `unknown`，列 **V-12**）。桌面端须在临时固定回环端口（如 `http://127.0.0.1:<固定端口>/callback`）监听并由系统浏览器回跳完成授权。
2. **开源桌面应用无法分发作者密钥，且 BYO 存在双重摩擦**：
   - **不可分发作者 `client_secret` / Bearer**：与 OAuth 2.0 公共客户端安全模型及 `Developer Agreement §III.A(e)/§III.G`（禁止向第三方提供密钥/令牌）明文规定冲突。
   - **不可分发作者共享 PKCE `client_id`**：PPU **按 Developer App 从 App 所有者余额扣费**且共享 300 万/月上限（`post-cap.mdx`），开源免费软件作者无法替全部用户支付 `$0.005/帖` 账单，且 `§III.L` 限制自助模式不得大规模面向公开终端用户分发。
   - **用户自带开发者 App（BYO）的两重阻碍**：
     1. **使用门槛极高**：每个用户须自行注册 `console.x.com`、验证手机号、绑定外币卡预付不可退积分、建 Native App、配精确本地回调 URL 并填入 `client_id`。
     2. **显性政策冲突（`官方文档`）**：`Restricted Use Cases`（“Multiple applications”）写明：*“Providing the same service or application to different end users counts as a single use case... requiring your end users to register applications for the purpose of using your tool or service could result in enforcement actions against you, your applications, your customers, and/or the end users of your tool or service.”*；`Developer Policy`（“X passwords”）亦限制向用户索取其 developer application 信息。
   - **与远程版 `trace-server`（[DEC-062](../decisions.md#dec-062)）冲突**：`access_token` 仅 2 小时有效，而 DEC-062 规定远程绝不保存刷新令牌、绝不在远端续期；走官方 API 要么要求本地桌面端每 2 小时常驻推送新令牌，要么打破 DEC-062 把 `refresh_token` 交给 VPS。

<a id="api-terms"></a>
### 3.5 开发者协议对离线缓存、删改同步与展示规范的约束（`官方文档`，2026-11-22）

以下均引自 `docs.x.com/developer-terms/{agreement,ppu-agreement,policy,display-requirements,restricted-use-cases}`（`Developer Agreement` 更新于 `2026-04-27`；仅陈述条款事实，不做法律结论）：

1. **24 小时删改同步义务（与 TRACE“永久离线归档”核心定位直接冲突）**：`Developer Agreement §IV.B` / `PPU Agreement §IV.B`（Removals）与 `Developer Policy`（Content compliance）规定，线下存储 X Content 须与 X 线上状态保持同步——原帖删除、账号转为受保护/封禁/停用或内容被屏蔽时，须尽快（或在接到 X/作者书面请求后 **24 小时内**）同步删除或更新本地副本（官方配套提供 `/2/compliance/jobs`）。
2. **协议终止删除义务**：`§VII.I` 规定协议因任何原因终止后须**永久删除持有的全部 X Content 副本（含媒体）**，并在 X 要求时 10 个工作日内提供删除证明。
3. **展示规范（`Display Requirements`，自助客户无豁免）**：要求展示官方 X Logo（与 TRACE **[DEC-014](../decisions.md#dec-014) / `FR-SITE-002`“不用站点图标与品牌色”**冲突）、头像与 `@username` 排版、不可改正文、可点击 `@`/`#`/`t.co` 与原文时间戳链接、编辑历史标记、转帖来源，且**明文禁止在帖子上附加第三方社交动作（条款示例直接包含 `subscribe`）**。
4. **禁止替代客户端、再分发与 AI 训练**：`§III.A(c)` 禁止创建 X Applications 替代品；`§III.A(d),(k)` 与 `Policy` 限制再分发水合数据（注：`policy.mdx` 写 500 条/人/日，而 `restricted-use-cases.mdx` 写 50,000 条/人/日，存在官方文档内部口径不一致），并禁止用于训练或微调任何基础/前沿 AI 模型（Grok 除外）。

<a id="route-compare"></a>
### 3.6 网页协议 vs 官方 API BYO vs 混合路线并列对比与推荐

综合依据：`官方文档（2026-11-22）`（价格、限流、OpenAPI `v2.175` 与开发者条款）、`源码阅读（多站点订阅参考、仓库代码，2026-11-22）`、`社区资料（2026-11-22）` 与 `推测（2026-11-22）`（实现量与个人费用估算）。

| 对比维度 | **方案 A：网页协议（粘贴 Cookie，推荐默认）** | **方案 B：官方 API v2（BYO + Pay-Per-Use，Q 项备选）** | **方案 C：混合路线（同会话混用 / 双后端）** |
| --- | --- | --- | --- |
| **TRACE 能力覆盖** | **最完整**：用户资料、时间线 + `UserMedia` 网格、单帖 + `note_tweet` + 自回复线程 + 编辑历史、原图/GIF/多档视频、关注/书签/文件夹/喜欢/列表、潜在可扩展 Broadcasts/Spaces | **缺口显著**：无他人视频直播、无 Spaces 音频与回放、无 `UserMedia`、单帖不含线程、时间线 3,200（`exclude=replies` 仅 800）、书签 800 且文件夹二次高价水合 | 同会话混用会同时继承两边短板；双后端并行则工程与测试面翻倍 |
| **用户直接费用** | **`$0`**（使用已有 X 账号） | **持续按量付费**：无 Free 档，`$0.005/帖`、`$0.010/用户`（30 订阅月耗约 `$10–$20`，单账号全部缓存约 `$16`，须绑手机号+外币卡预付） | 取决于所选模式 |
| **上手门槛** | 低（与抖音、微博一致：浏览器复制 Cookie 粘贴） | **极高**（建开发者账号、手机验证、绑外币卡充值、建 Native App、配精确本地回调 URL、复制 `client_id` 并 OAuth 授权） | 高 |
| **协议维护稳定性** | **低–中**：`queryId`/`features` 每 1～2 周漂移（叠加 2026 Vite 迁移），`x-client-transaction-id` 与 XPFF 需持续跟进 | **高**：标准 REST + OpenAPI `v2.175` 与规范限流头（但商业计费与自助端点权限本身在 2025–2026 年亦多次突变） | 维护两套协议栈，成本最高 |
| **规则与账号风险** | **高（个人账号风控 + 与 X 服务条款及自动化规则明文限制冲突）**：存在锁号（326）、Arkose 人机验证、429/空壳、降权乃至封号风险，**VPS 机房 IP 风险更高** | **账号封禁风险极低，但并非无条款冲突**：仍与 `Restricted Use Cases`（BYO 注册 App）、`§IV.B`（24h 删改同步）及 `Display Requirements`（X Logo / 禁附订阅按钮）存在直接张力 | 同会话混用仍带 Cookie 锁号风险 |
| **远程版 `trace-server`** | 架构兼容 [DEC-062](../decisions.md#dec-062)（推送 Cookie、不续期），但 **VPS 机房 IP 高风控**需醒目警告 | 与 DEC-062 冲突（`access_token` 仅 2 小时有效，要么本地每 2 小时推送、要么远端存 `refresh_token`） | 同各自模式 |
| **实现量（`internal/site/x`）** | 中–高（约 `2,200–2,800` 生产 LOC，0 新增依赖） | 中（约 `1,000–1,350` 生产 LOC，0 新增 X SDK 依赖） | 极高（`~3,200–4,100+` 生产 LOC） |

**推荐结论（`推测（2026-11-22）`，供 A 写入 Q 项 `Q-X-1`）**：M7 MVP 推荐采用 **方案 A：网页协议（粘贴 Cookie，严格只读、低频保守节奏、界面醒目提示账号风险并强烈建议使用专用测试/小号）**，**不做同会话混合**；同时保持 `internal/site/x` 内部接口隔离，把 **方案 B：官方 API v2（BYO OAuth 2.0 PKCE）** 作为 `Q-X-1` 的清晰备选（适用于绝不愿承担 Cookie 封号风险、愿自建 App 设消费上限自费调用且接受能力裁剪的用户）或后续 LOOP 扩展。

---

<a id="links"></a>
## 4. 链接、短链、嵌入镜像与稳定 ID（R-C）

<a id="url-forms"></a>
### 4.1 全部链接形态、`t.co` 短链规则与第三方嵌入镜像归一化

以下全部示例均为**明显合成示例**（`源码阅读（多站点订阅参考、仓库代码，2026-11-22）`、`社区资料（2026-11-22）`）：

| 类别 | 识别输入形态（合成示例） | 规范形态与稳定 ID 提取 |
| --- | --- | --- |
| **单条帖子** | `https://x.com/example_user/status/1000000000000000009`<br>`https://twitter.com/example_user/status/1000000000000000009`<br>`https://mobile.twitter.com/example_user/statuses/1000000000000000009`<br>`https://mobile.x.com/example_user/status/1000000000000000009`<br>`https://m.twitter.com/example_user/status/1000000000000000009`<br>`https://x.com/i/web/status/1000000000000000009`<br>`https://x.com/i/status/1000000000000000009`<br>兼容尾缀 `/photo/<1-4>`、`/video/<1>`、`/media_tags`、`/retweets`、`/quotes`、`/likes`、`/analytics`、`/history` 与跟踪参数 `?s=20&t=...` | 规范为 `https://x.com/i/status/<id>`（或 `/<handle>/status/<id>`）；稳定主键为 `id`（1～20 位正十进制数字串） |
| **用户主页** | `https://x.com/example_user`（含子标签 `/with_replies`、`/media`、`/highlights`、`/likes`、`/articles`）<br>`https://x.com/i/user/1000000000000000001`<br>`https://x.com/intent/user?user_id=1000000000000000001`<br>`https://x.com/intent/user?screen_name=example_user` | 按 `/i/user/` 或 `user_id=` 直接得稳定 `rest_id`；按 `<handle>`（`^[A-Za-z0-9_]{1,15}$`，大小写不敏感且不在保留路径黑名单内）须调 `UserByScreenName` 换取永久 `rest_id` 后落库 |
| **`t.co` 短链** | `https://t.co/Synthetic01`（分享文本中偶见 `http://t.co/Synthetic01`） | 按下方无 Cookie 白名单逐跳展开；多 URL 分享文本优先取显式 X 链接，最后回退 `t.co` |
| **第三方嵌入镜像（只归一化，零请求镜像）** | `fxtwitter.com`、`fixupx.com`、`vxtwitter.com`、`fixvx.com`、`twittpr.com`（含其 `d.` / `i.` / `g.` / `m.` 子域）、`nitter.net`、`xcancel.com` | 本地将主机重写为 `x.com`，剥离镜像附加路径尾缀（如 `.mp4` / `.jpg`）、`#m` 锚点与 `?s=`/`&t=` 跟踪参数后重入本地路由；**绝不向任何镜像域名发网络请求** |
| **Spaces（语音）** | `https://x.com/i/spaces/1SyntheticSpaceId`（含 `/peek`） | 提取 `space_id`（`[A-Za-z0-9]+`）；MVP 延后直播/Spaces 时返回 `Kind: "live", Unsupported: true` 并引导浏览器打开 |
| **视频直播（Broadcasts）** | `https://x.com/i/broadcasts/1SyntheticBcastId`、历史 `https://periscope.tv/w/1SyntheticBcastId`、`https://pscp.tv/w/1SyntheticBcastId` | 提取 `broadcast_id`（`[A-Za-z0-9]+`）；MVP 延后直播时返回 `Kind: "live", Unsupported: true` 并引导浏览器打开 |
| **X Articles** | `https://x.com/i/article/1000000000000000099`、`https://x.com/example_user/article/1000000000000000099` | MVP 拒绝独立文章页并引导浏览器打开（帖子内嵌文章卡片按 [§5.5](#post-degrade) 降级显示摘要） |
| **列表 / 社区** | `https://x.com/i/lists/1000000000000000055`、`/example_user/lists/<slug>`、`https://x.com/i/communities/1000000000000000066` | 社区链接直接拒绝（不支持）；列表链接视 R-G 范围决定（MVP 延后列表解析时拒绝并提示浏览器打开） |
| **移动端分享文本** | 混排文案 + `https://x.com/...` 或 `https://t.co/...` 或上述镜像链接 | 扩展 `internal/site/registry.go` 提取首个命中 X / 镜像 / `t.co` 的 URL（有 X 主链时不展开 `t.co`） |

#### `t.co` 短链展开硬约束（同微博 `t.cn` 安全模型，`社区资料（2026-11-22）`、`推测（2026-11-22）`；线上响应行为列 **V-03**）

1. `t.co` 是 X 全站通用外链短址，可跳往任意外站。仅对 `http://t.co/<alnum>` 在本地升格为 `https://t.co/<alnum>`，禁止 `user:pass@` 与非 443 端口。
2. 禁用自动重定向，手动逐跳读 `Location`（或 `HEAD`/`GET` 响应头，最多 **`5` 跳**、单跳超时 **`10` 秒**），**全程强制不带任何 Cookie**，并由 `GuardedMediaDialer` 阻断私有/回环/链路本地 IP。
3. **最终落点主机白名单**仅限 `{x.com, twitter.com, mobile.twitter.com, mobile.x.com, m.twitter.com}`（HTTPS 443）；指向任意站外域名立即以 `unsupported_link` / `blocked` 拒绝；任意一跳已能提取合法 X 目标 ID 时立即收敛停止。

<a id="snowflake"></a>
### 4.2 Twitter 雪花 ID（Snowflake）结构、前雪花边界与合成测试向量

Twitter Snowflake 为 **64 位有符号正整数**（最高位 bit 63 恒为 `0`），总计 63 有效位（`官方文档` / `社区资料（2026-11-22）`）：

- **Bits `62..22`（41 位）**：毫秒时间戳偏移 `delta_ms = id >> 22`，基准纪元 **`twepoch = 1288834974657`**（UTC `2010-11-04T01:42:54.657Z`），还原时间戳 `ts_ms = (id >> 22) + 1288834974657`。
- **Bits `21..17`（5 位）**：`datacenter_id = (id >> 17) & 0x1F`（`0..31`）。
- **Bits `16..12`（5 位）**：`worker_id = (id >> 12) & 0x1F`（`0..31`）。
- **Bits `11..0`（12 位）**：同毫秒序列号 `sequence = id & 0xFFF`（`0..4095`）。

**前雪花（Pre-Snowflake）历史 ID 边界**：2010 年 11 月之前的早期帖子（`id < ~29,700,859,247`）以及 **2016 年 2～4 月之前注册的老用户 `rest_id`** 采用连续自增整数而非雪花编码（`社区资料（2026-11-22）`）。因此：`id < (1 << 22)` 必定不是雪花 ID，且**不可假设所有 `rest_id` 或早期帖子 ID 都能还原为准确时间**；业务排序与展示时间应优先采用接口返回的 `created_at`，雪花解码仅作合法性校验、无时间字段兜底与测试辅助。

**4 组经本地 64 位纯整数脚本双向验证的合成测试向量**（2026-11-22，零网络请求）：

| 编号 | 合成 ID（十进制字符串） | 十六进制 | `delta_ms` | 还原 UTC 时间戳（`ts_ms`） | `dc` | `worker` | `seq` | 覆盖边界 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| **SV-1** | `0` | `0x0000000000000000` | `0` | `2010-11-04T01:42:54.657Z`（`1288834974657`） | `0` | `0` | `0` | 纪元零点下界 |
| **SV-2** | `1791257321273778509` | `0x18DBD30B0FD7614D` | `427069025343` | `2024-05-17T00:00:00.000Z`（`1715904000000`） | `11` | `22` | `333` | 域名迁移日常规位值 |
| **SV-3** | `2124382367241412607` | `0x1D7B5281EE871FFF` | `506492225466` | `2026-11-22T06:00:00.123Z`（`1795327200123`） | `3` | `17` | `4095` | 调研日 + 最大 `seq` |
| **SV-4** | `9223372036854775807` | `0x7FFFFFFFFFFFFFFF` | `2199023255551` | `2080-07-10T17:30:30.208Z`（`3487858230208`） | `31` | `31` | `4095` | 63 位有符号上界（溢出防线） |

<a id="stable-ids"></a>
### 4.3 稳定 ID vs 可变 `@handle` 与纯数字冲突

- **精度与稳定主键铁律**（`源码阅读（多站点订阅参考、仓库代码，2026-11-22）`）：
  - X 帖子 ID（19 位雪花 ID）与新用户 `rest_id`（最高 19～20 位）远超 IEEE-754 双精度安全整数 `2^53 - 1`，**全程必须以 `string` 传递、比对（按无符号 64 位整数或长度优先词典序）与落库**，Go 解析 JSON 时优先读 `*_str` / `rest_id` 字符串字段并开启 `UseNumber()`。
  - **稳定主键**：用户主键为数字串 **`rest_id`**（`NormalizeUID(X, uid)` 校验 1～20 位正十进制数字，账号键 `x:<rest_id>`）；帖子主键为 **`status_id`**（`rest_id` / `id_str`）。
  - **可变标识**：**`@handle`（`screen_name`，1～15 位 `[A-Za-z0-9_]`，大小写不敏感）随时可被用户改名甚至转给他人，严禁作订阅或缓存目录主键**，只能作展示与一次性反查 `rest_id` 的入口。
- **纯数字与 `@handle` 输入路由（对齐 `internal/site/registry.go`，`源码阅读（仓库代码，2026-11-22）`）**：
  - **纯数字碰撞**：现有 `DetectSite` 将 `<19` 位纯数字默认路由给哔哩哔哩 UID、将恰好 `19` 位纯数字本地拒绝为歧义、`>19` 位本地拒绝。X 帖子 ID（19 位）与老用户 `rest_id`（<19 位）不能抢占哔哩哔哩 UID，因此**保持裸纯数字规则不变**（`<19` 归 B 站、`19` 仍报歧义），X 目标须通过 URL（如 `x.com/...`、`/i/user/<rest_id>`、`/i/status/<id>`）输入。
  - **裸 `@handle` 输入**：当前 `DetectSite` 不识别裸 `@handle`；若无条件把 `@xxx` 归给 X，会与微博/抖音分享片段里的 `@昵称` 冲突。推荐 MVP **不自动把裸 `@handle` 猜成 X**，要求粘贴完整或裸域名链接（如 `x.com/example_user`），是否额外提供显式前缀（如 `x:@example_user`）交由 `Q-X-8` 拍板。

<a id="url-priority"></a>
### 4.4 路由识别优先级与不支持链接拒绝表

来源：`源码阅读（多站点订阅参考、仓库代码，2026-11-22）`、`社区资料（2026-11-22）`、`推测（2026-11-22）`（对照[微博 §3.4](weibo.md#url-priority)）。

**按顺序执行的本地解析优先级**：

1. **分享文本 URL 提取**：提取所有命中 X 主域、嵌入镜像域或 `t.co` 的 URL（遇空白、引号、尖括号、CJK/全角/Emoji 截断并剥离末尾英文标点 `.,;:!?)]}`）；若同一段文本同时含显式 X/镜像链接与 `t.co`，**优先选用显式 X/镜像链接，最后才回退取 `t.co`**；
2. **第三方嵌入镜像本地重写（零请求镜像）**：命中 `fxtwitter.com`、`fixupx.com`、`vxtwitter.com`、`fixvx.com`、`twittpr.com`（含 `d.`/`i.`/`g.`/`m.` 子域）、`nitter.net`、`xcancel.com` 时，在本地将主机改写为 `x.com`、剥离镜像附加后缀（`.mp4`/`.jpg`）、`#m` 与跟踪参数后重入第 4～9 步；
3. **`t.co` 短链无 Cookie 展开**：命中 `t.co/<alnum>` 时按 [§4.1](#url-forms) 规则（最多 `5` 跳、每跳 `10` 秒、严禁携带 Cookie、最终落点仅允许 X 域名白名单）展开后重入第 4～9 步；落至站外域名立即拒绝；
4. **安全与保留路径 / 不支持路径前置拒绝（必须早于 `<handle>` 匹配）**：校验 HTTPS 443、无 `user:pass@`、非私网 IP；优先拦截下方保留路径黑名单与不支持链接表，直接返回 `unsupported_link` / `blocked`（0 次 API 请求），**防止把 `/home`、`/explore`、`/i/...` 等误当成用户 `<handle>`**；
5. **明确单条帖子路径**：`/<user>/status/<id>`、`/<user>/statuses/<id>`、`/i/web/status/<id>`、`/i/status/<id>`（忽略尾缀 `/photo/<1-4>`、`/video/<1>`、`/media_tags`、`/retweets`、`/quotes`、`/likes`、`/analytics`、`/history`）→ 提取并校验正整数 `status_id`；
6. **直播、Spaces、文章与列表明确路径**：`/i/broadcasts/<id>`、`periscope.tv/w/<id>`、`pscp.tv/w/<id>`、`/i/spaces/<id>` → 返回 `Kind: "live", Unsupported: true` 并引导浏览器打开；`/i/article/<id>`、`/<handle>/article/<id>`、`/i/lists/<id>`、`/<handle>/lists/<slug>` → MVP 拒绝并提示浏览器打开；
7. **明确用户稳定 ID / Intent 路径**：`/i/user/<rest_id>`、`/intent/user?user_id=<rest_id>`（直取 `rest_id`）及 `/intent/user?screen_name=<handle>`（调 `UserByScreenName` 换 `rest_id`）；
8. **单段用户名主页（最低域名优先级）**：`/<handle>`（可选允许合法子标签 `/with_replies`、`/media`、`/highlights`、`/likes`、`/articles`），严格匹配 `^[A-Za-z0-9_]{1,15}$` 且不在保留路径黑名单内 → 调 `UserByScreenName` 换取稳定 `rest_id`；
9. **非 URL 裸输入边界与兜底拒绝**：裸纯数字遵循现有规则（`<19` 归 B 站、`19` 判歧义、`>19` 拒绝），裸 `@handle` 默认不猜 X；其余未命中输入一律本地报 `unsupported_link` / `bad_request`（0 次 API 请求）。

- **保留路径黑名单（绝不当成用户 `<handle>` 解析，本地拦截零请求）**：
  `home`、`explore`、`notifications`、`messages`、`i`、`settings`、`search`、`compose`、`intent`、`tos`、`privacy`、`login`、`logout`、`signup`、`hashtag`、`share`、`download`、`about`、`jobs`、`communities`、`lists`、`bookmarks`、`grok`、`premium`、`verified-orgs`。
- **明确拒绝的不支持链接表（`ErrKindUnsupportedLink`，0 次 API 请求）**：
  - 站内功能页：`/home`、`/explore`、`/search`、`/hashtag/*`、`/messages`、`/notifications`、`/settings`、`/i/bookmarks`（引导去“我的 › X › 书签”浏览）、`/i/grok`、`/i/flow/*`、`/i/trends`、`/i/communities/*`。
  - MVP 延后类型：`/i/spaces/*`、`/i/broadcasts/*`、`periscope.tv/*`、`pscp.tv/*`（明确提示“X 直播与 Spaces 暂不支持录制，可在浏览器中打开”）、`/i/article/*` 与 `/<handle>/article/*`（提示暂不支持独立文章页，可在浏览器打开）、`/i/lists/*`（若 MVP 未纳入列表页解析）。
  - 非安全或外站链接：非 `https://`（仅 `http://t.co/` 本地升 HTTPS）、含 `user:pass@`、非 443 端口、私有/回环 IP，以及 `t.co` 指向的任何站外域名。

---

<a id="post"></a>
## 5. 单条帖子的内容（R-D）

<a id="post-graphql"></a>
### 5.1 GraphQL 详情端点、`queryId` / `features` 变动与 2026 Vite 迁移

- **核心端点模板**（`源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）`）：
  `GET https://x.com/i/api/graphql/<queryId>/<OperationName>?variables=<URL编码JSON>&features=<URL编码JSON>[&fieldToggles=<URL编码JSON>]`
  - **`TweetDetail`**：返回焦点帖子 + 上游父帖 + 作者自回复对话线程（`variables`: `focalTweetId`、`with_rux_injections: false`、`rankingMode: "Relevance"`、`includePromotedContent: false`、`withCommunity: true`、`withQuickPromoteEligibilityTweetFields: false`、`withBirdwatchNotes: true`、`withVoice: true`、`withV2Timeline: true`）。响应路径为 `data.threaded_conversation_with_injections_v2.instructions` → `TimelineAddEntries.entries`（展开 `tweet-*`、`homeConversation-*`、`conversationthread-*`）。
  - **`TweetResultByRestId` / `TweetResultsByRestIds`**：轻量单帖/批量取帖，不带回复线程，适合只需要单条帖子正文与媒体且无需线程上下文的刷新场景（是否需要 `x-client-transaction-id` 列 **V-04**）。
- **`queryId` / `features` / `fieldToggles` 的来源与变动风险**（`源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）`）：
  - **来源与 2026 前端架构迁移**：`queryId`、`features` 与 `fieldToggles` 均共定义于 X 网页前端 JS 打包产物中的 GraphQL 操作元数据——传统上来自 `abs.twimg.com` 的 webpack `main.*.js` 与按需懒加载 chunk；**2026 年 X 正在把部分网页前端迁移至 Vite 打包的 `x-web` Relay chunk**，导致静态正则抽取表达式需要兼顾新旧两套产物格式（2026-11 实际 chunk 形态列 **V-04**）。
  - **`fieldToggles` 作用与漂移表现**：用于按操作控制可选扩展子字段的返回开关（例如 `TweetDetail` 常见 `withArticleRichContentState`、`withArticlePlainText`、`withGrokAnalyze`、`withDisallowedReplyControls`，`UserByScreenName` 携带 `{"withAuxiliaryUserLabels": false}`）；传错键名或缺失必选 toggle 可能触发 HTTP `400` 或导致响应静默缺少对应子字段（`TweetDetail` / `UserByScreenName` 实际必需 toggle 组合列 **V-05**）。
  - **变动频率**：随 X 前端部署约**每 1～2 周**变更 `queryId` 或新增必选 `features` / `fieldToggles` 布尔键。
  - **过期报错特征**：① `queryId` 失效常返回**空响应体 HTTP `404`**（极易与事务头失效或账号风控锁混淆，见 [§10.1](#error-mapping)）或 HTTP `400`（`Query: Unspecified`）；② 缺少必选 `features` 返回 **HTTP `400`**（`The following features cannot be null: ...`）；③ 未压缩的冗长 `features` JSON 可能触碰 **HTTP `414 URI Too Long`**（精简可选 `false` 开关可缓解，但必选非空开关不可删）。
  - **TRACE 维护策略**（`推测（2026-11-22）`）：内置编译期验证过的 `queryId` / `features` / `fieldToggles` 基线常量；遇到疑似 `queryId` 失效时允许**单飞（single-flight）+ 冷却期（如 ≥ 10 分钟）最多 1 次**从 `x.com` 公开前端脚本刷新 `queryId`（严禁高频抓取）；刷新失败直接返回 `schema_changed` 并建“站点限制” issue，绝不循环重试。

<a id="post-schema"></a>
### 5.2 2025/2026 Schema 变化、包装壳解包顺序与长帖（`note_tweet`）水合

以下均为 `2025–2026` 实战中极易踩坑的结构细节（`源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）`；线上实盘确认列 **V-05**）：

1. **用户信息字段迁移（2025/2026 Schema 变更）**：作者展示信息已迁移至 `userResult.core.name`、`userResult.core.screen_name`、`userResult.avatar.image_url`、`userResult.profile_bio.description`；解析器应**优先读取新路径，缺失时再回退 `userResult.legacy.*`**。
2. **包装壳解包顺序（`TweetPreviewDisplay` 必须先于 `TweetWithVisibilityResults` 判断）**：
   - 主帖、转帖（`legacy.retweeted_status_result.result`）与引用帖（`quoted_status_result.result`）外层均可能出现 `__typename == "TweetWithVisibilityResults"`（实际帖子在 `result.tweet`）或 `__typename == "TweetPreviewDisplay"`（订阅者专属预览帖，其预览数据直接在 `result.tweet.{rest_id, text, created_at, core}` 而**无完整 `legacy` 对象**）。
   - **正确顺序**：① 先判 `result.__typename == "TweetPreviewDisplay"`（标为受限预览并读预览字段）；② 再判 `TweetTombstone` / `TweetUnavailable`；③ 最后判 `TweetWithVisibilityResults`（或 `result.tweet != nil`）解包至内层帖子节点。
3. **长帖（`note_tweet`）全链路水合**：
   - 当存在 `note_tweet.note_tweet_results.result.text`（通常 `is_expandable == true`）时，用其全文覆盖被截断在 280 字符的 `legacy.full_text`，并用 `entity_set` 覆盖 `entities` 的 `hashtags` / `symbols` / `urls` / `user_mentions`（可兼读 `richtext` 分段）。
   - **关键细节（`源码阅读（多站点订阅参考，2026-11-22）`，2026-11-05 修复点）**：**不仅主帖，转帖原帖（`retweeted_status_result`）与引用帖（`quoted_status_result`）同样可能包含 `note_tweet`**，解析函数必须对三者递归执行 `note_tweet` 水合，否则被引用长帖会被静默截断。
4. **正文 `t.co` 实体清洗**：用 `entities.urls[]` 将正文 `t.co` 短链替换为 `expanded_url`（若 `expanded_url` 以本帖 `id_str` 结尾则为截断自链，直接删去）；剔除与 `extended_entities.media[].url` 对应的末尾媒体短链。

<a id="threads-retweets"></a>
### 5.3 线程、转帖、引用帖与原帖不可见降级

- **作者自回复线程**：通过 `legacy.conversation_id_str`、`legacy.in_reply_to_status_id_str`、`legacy.in_reply_to_user_id_str` 与 `legacy.self_thread.id_str` 识别；在 `TweetDetail` 中，先收集同次响应全部帖子建立 `rest_id -> Tweet` 索引，沿 `in_reply_to_status_id_str` 向上回溯父帖（设防环集合与最大深度）并按 `conversationthread-*` 顺序拼接同作者连续回复，**零额外请求**即可呈现线程上下文（`源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）`）。
- **转帖（Retweet）与引用帖（Quote）**：
  - **纯转帖**：`legacy.retweeted_status_result.result` 非空；缓存时严格遵循 **[DEC-056](../decisions.md#dec-056)**，连同附带图片与视频存于**转发者**目录 `x/<rest_id>/forwards/<outer_id>/` 并在 `item.json` 记录 `via = {reason: "forward", site: "x", uid: <转发者rest_id>, ...}`，完全复用微博已落地的 `DownloadPlan.IsForward` / `Origin*` / `OriginUnavailable*` 契约（`源码阅读（仓库代码，2026-11-22）`）。
  - **引用帖**：`legacy.is_quote_status == true` 且 `quoted_status_result.result` 存在；外层保留作者评论与媒体，内层附引用原帖摘要/媒体。
  - **原帖已删 / 受保护 / 不可见降级**：当转帖或引用内层为 `TweetTombstone`（取 `tombstone.text.text`）、`TweetUnavailable`（取 `reason`）或空对象 `{}` 时，写入 `OriginUnavailable = true` 与提示文案，**绝不因原帖缺失让外层帖子解析或缓存失败**（`社区资料（2026-11-22）`、`推测（2026-11-22）`）。

<a id="post-media"></a>
### 5.4 图片、GIF、视频多档选流与混排媒体

全部媒体位于 `legacy.extended_entities.media`（缺失时回退 `entities.media`），单帖最多 **4** 个附件且支持**图片 + GIF + 视频混排**，解析时必须严格保持数组原顺序（映射到 TRACE `DownloadPlan{Kind: "image_note", Images: []MediaImageAsset}`，单视频帖映射为 `Kind: "single_mp4"`）（`源码阅读（多站点订阅参考、仓库代码，2026-11-22）`、`社区资料（2026-11-22）`）：

1. **图片（`type == "photo"`）**：
   - 原图规则：将 `https://pbs.twimg.com/media/<media_id>.<ext>[:<size>]` 规范化为 **`https://pbs.twimg.com/media/<media_id>?format=<ext>&name=orig`**（`jpeg` 归一为 `jpg`，兼容 `png`、`webp`；若 URL 已带查询参数则仅把 `name` 置为 `orig`）。
   - 头像大图：将 `profile_image_url_https` 中的 `_normal.` 替换为 `_400x400.` 或移除 `_normal` 取原图。
2. **GIF（`type == "animated_gif"`）**：
   - X 平台不提供原生多帧 `.gif` 下载，所有 GIF 均为静音循环 **MP4**（`video_info.variants[0].url` 位于 `video.twimg.com/tweet_video/<id>.mp4`，`content_type == "video/mp4"`，`bitrate == 0`），封面图取 `media_url_https` 原图。
3. **视频（`type == "video"`）**：
   - `video_info.variants[]` 同时给出多档渐进式 **`video/mp4`**（带整型 `bitrate`，如 432k / 832k / 2176k / 10368k 等）与 **`application/x-mpegURL`**（`.m3u8`，通常无 `bitrate` 字段）。
   - **选流规则**：优先过滤 `content_type == "video/mp4"` 并按 `bitrate` 降序（同码率按分辨率降序）选取最高档单文件 MP4；若某长视频 / 4K / 多音轨视频仅提供 `.m3u8` 或 MP4 档位低于 HLS（2026 线上出现比例记为 `unknown`，列 **V-07**），则视分片形态（fMP4 可复用 `mp4ff`；MPEG-TS 须等待 `LOOP-008`）按实测结论降级或提示。
   - **登录 vs 未登录差异**：未登录态完全隐藏 NSFW / 年龄限制 / 受保护账号视频，登录态可取回当前账号有权看到的全部明文档（`社区资料（2026-11-22）`）。

<a id="post-degrade"></a>
### 5.5 暂不支持类型降级、可见性限制与编辑历史

- **暂不支持内容降级**（对齐 `DownloadPlan.Card`，不丢正文、不误当视频、不爬第三方外页，`社区资料（2026-11-22）`、`推测（2026-11-22）`）：
  - **投票（Polls）**：`card.legacy.name` 匹配 `poll[2-4]choice_*` → 提取选项标签与票数作为文本摘要卡片展示（互动票数仅在线展示、不落盘或仅保留选项标题）。
  - **网页 / 播放器 / 统一卡片 / 直播与 Spaces 卡片**：`summary`、`summary_large_image`、`player`、`unified_card`、`broadcast`、`audiospace` → 提取标题、简介、展开链接与封面缩略图进 `Card` 并提供“在浏览器中打开”；直播/Spaces 卡片明确标注“暂不支持录制”。
  - **X Articles 长文**：`article.article_results.result` → MVP 降级展示 `title`、`preview_text`、`cover_media_img_url` 与“在浏览器中打开”（完整 Draft.js `content_state` 富文本渲染延后至 loop）。
  - **社区笔记（Community Notes）**：`birdwatch_pivot` → 可选保留纯文本提示或安全忽略。
- **受保护 / 已删 / 地域屏蔽 / 敏感内容识别**：
  - `TweetUnavailable.reason` 枚举：`Protected`（受保护账号 → `forbidden`）、`Blocked`（被拉黑 → `forbidden`）、`CountryWithheld`（地区屏蔽 → `unavailable`）、`BounceDeleted` / `Suspended`（删除/封禁 → `unavailable`）、`NsfwLoggedOut` / `NsfwViewerHasNoStatedAge`（敏感/年龄限制 → `forbidden` / 引导浏览器设置出生日期）。
  - 另见 `legacy.possibly_sensitive`、`legacy.withheld_in_countries`、`TweetTombstone`；这些均属于目标级不可见，**绝不能误报为当前登录账号 `auth_required` 或触发整站熔断**。
- **编辑历史（Edit History）**：`edit_control.edit_tweet_ids` 按从初版到最新版顺序给出全部修订 ID（另含 `edits_remaining`、`edit_control_initial`）。建议展示时标明“已编辑”，以请求目标或最新修订 ID 落盘，并通过比对 `edit_tweet_ids` 避免对同一帖的不同编辑版本重复自动缓存（`社区资料（2026-11-22）`、`推测（2026-11-22）`）。

---

<a id="media-cdn"></a>
## 6. 媒体下载、白名单与跳转规则（R-E）

<a id="cdn-domains"></a>
### 6.1 CDN 域名、防盗链、签名与 HLS 容器特性

| 资产类型 | CDN 域名 | `Referer` 与 Cookie | 签名与有效期 | `Range` 与容器形态 | 证据等级 |
| --- | --- | --- | --- | --- | --- |
| **图片、头像、横幅、视频封面、卡片图** | `pbs.twimg.com`（路径 `/media/`、`/profile_images/`、`/profile_banners/`、`/ext_tw_video_thumb/`、`/amplify_video_thumb/`、`/tweet_video_thumb/`、`/card_img/`） | **无需 `Referer`，严禁带 `Cookie`** | 常规无时效签名、长期可访问 | JPEG / PNG / WebP；支持 HTTP `206 Range`（实测列 **V-07**） | `源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）` |
| **点播视频与 GIF MP4** | `video.twimg.com`（可选镜像边缘 `video-s.twimg.com`；路径 `/ext_tw_video/`、`/amplify_video/`、`/tweet_video/`） | **无需 `Referer`，严禁带 `Cookie`** | 常规渐进式 MP4（如 `?tag=12`/`14`/`21`）多为无签名直链；但 HLS 播放列表、受保护或长视频可带短时签名（精确 TTL `unknown`，列 **V-07**） | 渐进式单文件 MP4 支持 HTTP `206 Range`；HLS-only 视频为 Master `.m3u8` → fMP4（`.m4s` + `#EXT-X-MAP`）或 MPEG-TS（`.ts`），线上比例 `unknown`（列 **V-07**） | `社区资料（2026-11-22）` / 部分 `unknown` |
| **排除出媒体白名单的域名** | `abs.twimg.com`（静态 JS/CSS/Emoji）、`ton.x.com` / `ton.twitter.com`（鉴权私信媒体）、`cdn.syndication.twimg.com` | — | — | 绝不纳入媒体下载白名单 | `社区资料（2026-11-22）`、`推测（2026-11-22）` |
| **直播 / Spaces 流媒体域（仅开启直播时）** | `*.video.pscp.tv`、`*.periscope.tv`、`*.pscp.tv` | 不带登录 Cookie | 带短时效签名令牌 | HLS `.m3u8`（TS / LHLS fMP4 / AAC）；MVP 延后直播时不加入白名单 | `社区资料（2026-11-22）` |

<a id="media-whitelist"></a>
### 6.2 对齐 `internal/site` 的白名单、跳转与签名不落盘设计

对照 `internal/site/types.go`（`MediaTransportSpec`）、`internal/site/mediadial.go`（`GuardedMediaDialer`）与 `internal/download/range_download.go`（`源码阅读（仓库代码，2026-11-22）`）：

1. **推荐默认白名单 `DefaultXMediaHosts()`**：`pbs.twimg.com`、`video.twimg.com`（经 **V-07** 确认若出现 `video-s.twimg.com` 边缘域可并入）；绝不把泛 `twimg.com` 或 `x.com` 放进媒体白名单。
2. **`XMediaTransport(userAgent, allowLocalhost)` 规范**：
   - `Site: site.X`、`AllowedDomains: DefaultXMediaHosts()`、`UserAgent: <固定桌面 Chrome UA>`、`Referer: ""`、`Origin: ""`。
   - **强无 Cookie**：首跳与每一跳重定向强制 `req.Header.Del("Cookie")`。
   - **`PublicRedirects` 策略**：默认 **`false`**（仅允许在 `DefaultXMediaHosts()` 内跳转、最多 5 跳、HTTPS 443、经 `GuardedMediaDialer` 阻断回环/私网/CGNAT/NAT64）；仅当 **V-07** 实测发现 `video.twimg.com` 会 302 调度至白名单外公网 CDN 边缘域时，才按抖音先例为视频开启 `PublicRedirects: true`。
3. **防假 200 与签名不落盘**：复用 `looksLikeDisguisedTextBody` 拒绝伪装 HTML/JSON 200 响应；所有原始 CDN URL 字段一律标记 `json:"-"`，绝不写入 `item.json`、`updates.db`、`subscriptions.json`、`cache-queue.json`、日志或错误消息；下载中断按 `8 MiB` `Range` 续传，遇 `403`/`404`/`410` 最多重调 `1` 次详情刷新直链；单文件 MP4 经已有 `mp4ff v0.63.0` 校验轨道与 `moov` 前置重排（CENC/DRM 加密流直接拒绝）。

---

<a id="user-feed"></a>
## 7. 用户主页与订阅检查（R-F）

<a id="user-timelines"></a>
### 7.1 用户资料接口与三类时间线游标翻页

1. **用户资料接口**（`源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）`）：
   - `UserByScreenName`（`{screen_name, withSafetyModeUserFields: true}` + `fieldToggles: {"withAuxiliaryUserLabels": false}`）与 `UserByRestId`（`{userId, withSafetyModeUserFields: true}`），以及批量 `UsersByRestIds`。
   - 关键字段：`rest_id`（稳定主键）、`core.{name, screen_name}`（回退 `legacy.*`）、`avatar.image_url`（回退 `legacy.profile_image_url_https`）、`profile_bio.description`（回退 `legacy.description`）、`legacy.{statuses_count, media_count, followers_count, friends_count, protected, pinned_tweet_ids_str}`、`__typename == "UserUnavailable"`（`reason`: `Suspended` / `Offboarded` / `Blocked`）。
   - **缓存铁律**：严禁缓存包含 GraphQL `errors` 的 HTTP 200 响应。
2. **三类时间线端点与游标翻页**：
   - **`UserTweets`**（主选订阅检查与“帖子”标签，含原帖与转帖，不含回复）：`count: 20`；若响应含 `profile-conversation-*` 模块，应按 `legacy.user_id_str == target_rest_id` 过滤对话中混入的他人帖，并按需过滤非根回复。
   - **`UserTweetsAndReplies`**（含回复）：`count: 20`、`withCommunity: true`；**已知强制要求 `x-client-transaction-id`**（`源码阅读（多站点订阅参考，2026-11-22）`）。
   - **`UserMedia`**（媒体网格）：`count: 20`、`includePromotedContent: false`；**条目结构不同于普通时间线**——第 1 页常在 `TimelineAddEntries` 中 `entryId == "profile-grid-0"` 的 `content.items`（子项前缀 `profile-grid-0-tweet-*`），翻页常在 `TimelineAddToModule.moduleItems` 中（`源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）`）。
   - **响应路径兼容性**：从 `data.user.result` 依次兼容 `timeline.timeline`、`timeline.timeline_v2`、`timeline_v2.timeline` 取 `.instructions`。
   - **游标与噪声过滤**：从 `TimelineAddEntries` / `TimelineReplaceEntry` 提取 `content.cursorType == "Bottom"`（下一页）与 `"Top"` 的 `content.value` 传给 `variables.cursor`；遇到 `nextCursor == reqCursor`（游标停滞）立即中止并报 `schema_changed`。**必须跳过** `promoted-tweet-*`、`who-to-follow-*`、`cursor-*` 等推广与推荐杂卡。

<a id="monotonicity"></a>
### 7.2 置顶与转帖对时间单调性的影响（对照 `douyin.md §6.3` 与 `weibo.md §6.3`）

来源：`源码阅读（多站点订阅参考、仓库代码，2026-11-22）`、`社区资料（2026-11-22）`、`推测（2026-11-22）`（线上翻页细节列 **V-06**）。

| 维度 | 抖音 `aweme/post`（[抖音 §6.3](douyin.md#posts)） | 微博 `mymblog`（[微博 §6.3](weibo.md#monotonicity)） | **X `UserTweets`** | 对 TRACE 订阅检查的强制规则 |
| --- | --- | --- | --- | --- |
| **置顶条目** | `is_top == 1`（1～3 条，排第 1 页首） | `isTop == 1` / `mblogtype == 2` | **独立指令 `instruction.type == "TimelinePinEntry"`** 携带 `instruction.entry`（不在 `TimelineAddEntries` 中） | 置 `IsTop = true`，写入 `account_baseline_pinned`，**绝不参与水位线推进、绝不触发早停** |
| **转帖 / 快转单调性** | 无 | 快转 `ori_mid` 的内部时间可能倒挂为旧原博时间 | **外层转帖外壳自带转帖时刻的新雪花 `rest_id` 与外层 `created_at`（严格单调降序）**；但内层 `retweeted_status_result` 为原帖旧 ID 与旧时间 | **必须使用外层 `rest_id` 与外层 `created_at`** 参与时间线单调性与水位线比对（绝不用内层原帖时间比对）；落盘按 [DEC-056](../decisions.md#dec-056) 存转发者 `forwards/` |
| **推广 / 外人卡片** | 无 | 推广卡 / 外人卡 | `promoted-tweet-*`、`who-to-follow-*` 及对话模块内非目标作者推文 | 入检查循环前按 `entryId` 前缀与 `legacy.user_id_str == target_rest_id` 严格剔除 |
| **常规条目单调性** | 非置顶按 `create_time` 降序 | 非置顶非快转按 `created_at` / `mid` 降序 | 非置顶 `TimelineAddEntries` 条目按外层 `rest_id`（雪花单调）与 `created_at` 降序 | 仅在遇到非置顶常规条目命中基线（`id == newest_id` 或 `id <= newest_id` 按 64 位整数比对）时才可早停 |
| **翻页游标** | 13 位毫秒 `max_cursor` | `since_id` + `page` | 不透明字符串 `Bottom` cursor | 同时校验空游标、空有效条目与游标停滞防死循环 |

<a id="empty-truth-table"></a>
### 7.3 历史深度上限与防误判真值表（含假空壳陷阱）

- **历史深度上限**：`UserTweets` 受 X 平台硬上限约束，最多约 **`~3,200` 条**最近帖子；`UserMedia` 社区常见报告在 **`~800–3,200`** 条之间，**精确深度上限保持 `unknown`**（有界 2 页的 **V-06** 检查仅核实 `profile-grid-0` / `TimelineAddToModule` 分页契约，不做穷尽深页探测以免撞限流）；未登录态被拦截或仅返 ~100 条乱序热门帖（`社区资料（2026-11-22）`）。
- **防误判真值表**（`源码阅读（多站点订阅参考、仓库代码，2026-11-22）`、`社区资料（2026-11-22）`、`推测（2026-11-22）`）：

| 响应形态 | 真实原因 | TRACE 判定（`site.ErrKind*` / `CreatorPostsPage`） | 是否移动订阅水位线 |
| --- | --- | --- | --- |
| HTTP `200`，合法 `timeline.instructions`，`errors` 为空，且 `statuses_count == 0`（首页）或连续非空页后 `Bottom` 游标耗尽/无新帖 | **真实 0 条 / 正常翻到底**（或触及 ~3,200 上限） | 正常结束（`HasMore = false`，触及深页上限时文案明确提示“已到达 X 时间线可见上限”） | **是**（首页建 0 基线 / 正常收束） |
| HTTP `200`，响应为 **`{"data":{"user":{}}}`** 或缺少 `timeline`/`timeline_v2` 对象 | **X 标志性假空壳限流 / 风控陷阱** | **`rate_limited` / `UncertainEmpty = true`**（按 429 同等触发退避，**绝不当 0 条或到底**） | **否** |
| HTTP `200`，`user.result == null` 或 `data.user == null` | 目标用户不存在 | **`unavailable`**（单账号错误，不触发整站熔断） | **否** |
| HTTP `200`，`user.result.__typename == "UserUnavailable"`（`Suspended` / `Blocked` / `Offboarded`） | 目标账号被封禁、注销或拉黑当前账号 | **`unavailable` / `forbidden`**（不触发整站熔断） | **否** |
| HTTP `200`，`user.legacy.protected == true` 且当前登录账号未获准关注 | 受保护私密账号 | **`forbidden`**（提示受保护账号不可见，不触发整站熔断） | **否** |
| HTTP `200`，顶层 `errors[]` 非空 | GraphQL 接口错误 / 限流 / 失效 | 按错误码映射（见 [§10.1](#error-mapping)） | **否** |
| HTTP `401`/`403`（`32`/`64`/`89`/`226`/`326`/`353`）、HTTP `429`（`88`）、空体 `404` | 认证失效、风控锁号、频控或 `queryId`/事务头失效 | **`auth_required` / `risk_control` / `rate_limited` / `schema_changed`** | **否** |

<a id="sub-baseline"></a>
### 7.4 首次建基线、增量轮询与“全部缓存”及 `SearchTimeline` 代价

1. **首次建基线（未读 = 0，复用 `updates.db`）**：仅拉取 `UserTweets` 第 1 页，将 `TimelinePinEntry` 置顶 ID 写入 `account_baseline_pinned`，记录第 1 页非置顶常规条目的最大外层 `rest_id` 与 `pub_ts` 为起点，历史旧帖不进 `update_item`。
2. **增量轮询**：从第 1 页按 `Bottom` 游标扫描，单账号单轮上限 **`2` 页**；跳过推广卡与置顶帖，遇到非置顶常规条目命中水位线即早停；任何 `UncertainEmpty` / `rate_limited` / `risk_control` / `auth_required` 均不移动起点。
3. **补更早内容（“全部缓存”，`FR-USER-004` / [DEC-061](../decisions.md#dec-061)）与为什么 MVP 不做 `SearchTimeline` 自动回溯**：
   - **沿 `UserTweets` 慢速回溯（推荐 MVP 边界）**：复用 TRACE 慢速队列（单站点串行、两条间隔 `40–150` 秒、每 `15–25` 条长歇 `10–25` 分钟），且**优先直接复用 `UserTweets` 列表响应构造下载计划**（仅遇截断或媒体链接过期时补调详情），抵达 X 的 **~3,200 条**时间线可见上限时正常收束并明确提示“已到达 X 时间线可见上限（约 3,200 条）”，与微博半年可见边界体验一致（`推测（2026-11-22）`）。
   - **为什么 MVP 不自动调用 `SearchTimeline`（`from:<handle> since:... until:...`）突破 3,200 限制**：① 搜索端点 15 分钟限流显著严于主页（社区常见约 ~50 次/15 分钟）；② 强制校验 `x-client-transaction-id` 且高概率叠加 `x-xp-forwarded-for`（XPFF），是触发 `code 226` / `326` 锁号的高危接口；③ 搜索结果**不包含转帖**，且对被搜索降权或未建索引的账号会**静默漏帖**（`社区资料（2026-11-22）`）。因此 MVP 严禁自动用 `SearchTimeline` 补抓（如后续评估须先经 **V-10** 实测）。

---

<a id="mine"></a>
## 8. “我的”：登录账号的内容（R-G）

按 [DEC-047](../decisions.md#dec-047) 与 `internal/service/account.go` 架构约束，本节全部接口**严格只读**（绝不调用取消关注、移除书签、取消点赞、改列表等任何写接口），数据**仅驻留内存、绝不落盘或把标题/用户名写进日志**，退出 X 登录即清零；未在真实站点确认的二级标签统一通过编译期默认关闭开关门控（同抖音 `WatchHistoryEnabled`、微博 `LikesEnabled`/`FollowGroupsEnabled`，`源码阅读（仓库代码，2026-11-22）`）。

| 功能 | 网页 GraphQL 操作与参数 | 翻页、限制与未知项 | TRACE 接入与导入适用性 | 证据等级 |
| --- | --- | --- | --- | --- |
| **我的关注（Following）** | `Following`（`{userId: <self_rest_id>, count: 20, cursor}`） | `Bottom` 游标翻页；返回目标用户完整 `rest_id`、`core.{name, screen_name}`、`avatar.image_url`、简介 | **适合作为“我的 › X › 关注”与批量导入订阅（`ImportAccountRefs`，对称 B 站与微博）的数据源**，零额外详情请求即可写入 `subscriptions.json`（实测列 **V-08**） | `社区资料（2026-11-22）` |
| **我的书签（Bookmarks）** | `Bookmarks`（`{count: 20, cursor}`） | 仅登录账号本人可用，`Bottom` 游标翻页；帖子结构同 `UserTweets` | **推荐纳入 MVP 只读浏览**，点开进帖子详情、“⋯ → 缓存”（实测列 **V-08**） | `社区资料（2026-11-22）` |
| **书签文件夹（Bookmark Folders）** | 目录：`BookmarkFoldersSlice`<br>文件夹内帖子：`BookmarkFolderTimeline`（`{bookmark_collection_id, count: 20, cursor}`） | **属于 X Premium 付费功能**——非 Premium 账号预期返回空文件夹列表；非 Premium 与 Premium 的精确响应结构与降级表现记为 `unknown`（列 **V-08**） | 经 **V-08** 实测确认非 Premium 优雅降级（仅显示“全部书签”、不报错）后再开放文件夹下拉，未确认前默认开关关闭 | `社区资料（2026-11-22）` / `unknown` |
| **我喜欢的帖子（Likes）** | `Likes`（`{userId: <self_rest_id>, count: 20, cursor}`） | **自 `2024-06-12` 起 X 将全平台 Likes 设为私密，仅可查询登录账号本人**（查他人返回空或拒绝）；走标准用户时间线指令结构 | 与 TRACE “我的”仅看本人定位完全一致；推荐纳入 MVP（实测列 **V-08**，确认非空响应与反爬头需求） | `源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）` |
| **我的列表（Lists）** | 目录：`ListsManagementPageTimeline`；列表详情：`ListByRestId`；列表帖子：`ListLatestTweetsTimeline`（`{listId, count: 20, cursor}`，路径 `data.list.tweets_timeline.timeline.instructions`，展开 `listConversation-*`） | 游标翻页；列表含自有与关注的列表 | 非核心高频路径，建议先设默认关闭开关或延后至后续 loop，待 **V-08** 实测后再评估开放 | `源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）` |

注：`HomeTimeline` / `HomeLatestTimeline`（首页信息流）不属于个人收藏夹/关注列表，TRACE 主页信息流只读本机已缓存内容（`FR-FEED-001`），不接入首页推荐流。

---

<a id="live"></a>
## 9. 直播与 Spaces 语音评估（R-H）

<a id="live-proto"></a>
### 9.1 视频直播（Broadcasts）与 Spaces 接口及流封装

1. **视频直播（`/i/broadcasts/<id>`）**（`社区资料（2026-11-22）`；2026 线上清晰度档位与分片封装比例 `unknown`，列 **V-11**）：
   - **元数据**：GraphQL `BroadcastQuery`（或旧 v1.1 `GET /1.1/broadcasts/show.json?ids=<id>`）返回 `state`（`RUNNING` / `ENDED`）与 `media_key`（如 `28_<id>`）。
   - **取流与清晰度**：`GET https://x.com/i/api/1.1/live_video_stream/status/<media_key>` 返回 `source.location` / `source.noRedirectPlaybackUrl`（HLS `.m3u8`，位于 `*.video.pscp.tv` / `*.periscope.tv`）与 `source.streamType`（`HLS` / `LHLS`）；分片随推流编码器可为 **MPEG-TS（`.ts`）** 或 **LHLS / fMP4（`.m4s`）**，**不提供 HTTP-FLV**。清晰度方面，部分场次返回含多组 `#EXT-X-STREAM-INF`（`BANDWIDTH`、`RESOLUTION`、`CODECS`）的 Master Playlist 可按最高分辨率/码率选档，另一些直推源仅给单一原生清晰度子播放列表（2026 线上档位阶梯与封装分布记为 `unknown`，列 **V-11**）。
2. **Spaces 语音直播与回放（`/i/spaces/<id>`）**（`源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）`；线上流细节列 **V-11**）：
   - **元数据**：GraphQL `AudioSpaceById`（`variables: {id, isMetatagsQuery: false, withReplays: true, withListeners: false}`，追加 Spaces `features` 开关）返回 `data.audioSpace.metadata.{rest_id, state ("Running"|"Ended"|"NotStarted"|"TimedOut"), title, started_at (Unix ms), is_space_available_for_replay, media_key}` 及 `participants.{admins, speakers}`。
   - **取流**：同样调 `GET /1.1/live_video_stream/status/<media_key>` 获取 HLS `.m3u8`（直播中常为 `dynamic_playlist.m3u8`，回放开启 `is_space_available_for_replay == true` 时常为 `master_playlist.m3u8` / `type=replay`），内容为**纯音频 AAC**（分片为 `.aac` 或 `.ts`）。
3. **“已订阅用户 `rest_id` → 是否正在直播/开 Space”的发现缺口**：
   - **无统一批量视频直播开播查询接口**：网页端没有类似 B 站（50 人/批）的批量查开播接口，发掘视频直播通常只能靠嗅探时间线帖子内的直播卡片或逐个查主页；官方 API v2 的 Livestream API 更是严格限制仅能查播主自己发起的直播。
   - **`fleets/v1/avatar_content` 现行状态存在分歧（记为 `unknown`，列 V-11）**：本机多站点订阅参考中仍保留 `GET https://x.com/i/api/fleets/v1/avatar_content?user_ids=<rest_id>` 读取 `users[<rest_id>].spaces.live_content.audiospace.id` 以发现正在直播的 Space（`源码阅读（多站点订阅参考，2026-11-22）`）；但公开社区资料同时报告传统 Fleets 相关端点已退场或不稳定（`社区资料（2026-11-22）`）。在未获实测验证前不可依赖它做定时开播巡检。

<a id="live-fit"></a>
### 9.2 与现有录制器、`LOOP-008` / `LOOP-009` 的差距及延后建议

对照[直播设计](../design/live.md)与仓库 `internal/live` 实现（`源码阅读（仓库代码，2026-11-22）`）：

| 维度 | TRACE 现有 `internal/live` 能力 | X Broadcasts / Spaces 需求与缺口 |
| --- | --- | --- |
| **流传输与解复用** | 仅支持 **HLS-fMP4**（耦合 `*bilibili.Client`，强求 `#EXT-X-MAP` + `.m4s`）与 **HTTP-FLV**（抖音/B 站后备） | X 不提供 HTTP-FLV；Broadcasts 与 Spaces 均走 HLS，分片高频为 **MPEG-TS（`.ts`）或裸 AAC**，必须先完成 **`LOOP-008`**（引入 `bluenviron/mediacommon/v2 v2.10.0`，MIT，`~+2.10 MiB` + `400–550` LOC 通用 HLS 轮询与 TS 解复用） |
| **纯音频录制与播放** | 录制器遇到无视频轨流直接拒绝（`ErrNoVideoKeyframe` / `LiveStateAudioOnly`），收尾强求视频轨 | Spaces 为**纯语音 AAC**，必须先完成 **`LOOP-009`**（单音频轨 `.m4a` 录制、收尾校验、媒体库音频直播录像展示与播放支持） |
| **多站点录制管理层** | `internal/live/manager.go` 当前仍硬编码哔哩哔哩与抖音双分支 | 需额外重构多站点 `Manager` 且缺乏可靠的 X 按用户批量开播巡检端点，轮询时间线会迅速耗尽 X 极紧的限流配额并放大锁号风险 |

**明确建议（对齐微博 [DEC-065](../decisions.md#dec-065) 先例与 `Q-X-5`）**：**M7 MVP 不实现 X 视频直播与 Spaces 的开播巡检与录制**，采用零请求排除策略：
1. 链接解析器识别 `/i/broadcasts/*`、`/i/spaces/*`、`periscope.tv/*`、`pscp.tv/*`，返回 `Kind: "live", Unsupported: true`，文案明确提示“X 直播与 Spaces 暂不支持录制，可在浏览器中打开”；
2. 帖子与主页中的直播/Spaces 卡片安全降级为摘要卡片 + 浏览器打开入口；
3. X 账号不进入 `internal/live` 开播巡检与自动录制列表；待未来 `LOOP-008`（HLS-TS）与 `LOOP-009`（纯音频直播）落地并经 **V-11** 实测确认流形态后，再在独立 loop 评估支持。

---

<a id="errors"></a>
## 10. 错误、风控、反爬请求头与请求节奏（R-I）

<a id="error-mapping"></a>
### 10.1 HTTP 状态码与 X JSON 错误码映射到 TRACE `site.ErrKind*`

对照 `internal/site/errors.go` 的 15 个标准错误类别与调度器 `IsRoundStop` / `IsCircuitBreak` 契约（`官方文档`、`源码阅读（多站点订阅参考、仓库代码，2026-11-22）`、`社区资料（2026-11-22）`）：

| HTTP 状态 / X JSON 错误码 | 官方或实测含义 | TRACE `site.ErrKind` | 调度器动作（`IsRoundStop` / `IsCircuitBreak`） |
| --- | --- | --- | --- |
| HTTP `401` / code `32` | `Could not authenticate you`（未认证或 Cookie 无效） | **`auth_required`** | 停止本轮、标会话失效、暂停 X 下载，**不推进退避** |
| HTTP `401` / code `89` | `Invalid or expired token`（令牌过期或已吊销） | **`auth_required`** | 同上 |
| HTTP `400` / code `215`、`239` | 认证数据格式错误 / 访客令牌失效 | **`auth_required`** | 停止本轮，不重试 |
| HTTP `403` / code `353` | CSRF `ct0` Cookie 与 `x-csrf-token` 头不匹配 | **`auth_required`**（先查 `Set-Cookie` 一次） | 若响应头下发合法新 `ct0`（`^[A-Za-z0-9]+$`），采纳同步并仅重放该步一次；无新 `ct0` 或再现 `353` 直接判 `auth_required`（**V-02**） |
| HTTP `403` / code `64` | `Your account is suspended`（登录账号已封禁） | **`blocked`**（或 **`risk_control`**） | **立即熔断 X 站点、零自动重试**，提示在系统浏览器核查账号状态 |
| HTTP `403` / code `326` | 账号临时锁定（要求短信/邮箱或人机验证解锁） | **`risk_control`** | **立即熔断 X 站点并推进退避、零自动重试、绝不解验证码**；不清空既有凭证 |
| HTTP `403` / code `226` | `This request looks like it might be automated`（自动化风控拦截） | **`risk_control`** | **立即熔断 X 站点并推进退避、零自动重试** |
| HTTP `429` / code `88`（`x-rate-limit-remaining == 0`） | 15 分钟逐端点频率超限 | **`rate_limited`** | 记录 `x-rate-limit-reset`，**立即熔断本轮并退避至重置时间后、零自动重试** |
| HTTP `429` 或 `200` / code `88`（`remaining > 0` 或连续无头 `88`） | 账号级异常滥用拦截（非正常时间窗耗尽） | **`risk_control`** | **立即熔断 X 站点并指数退避、零自动重试** |
| HTTP `200` 且 `data == {"user":{}}` | 假空壳限流/风控陷阱 | **`rate_limited`** / **`risk_control`** | **按限流/风控熔断，绝不当 0 条或移动水位线** |
| **GraphQL 空体 HTTP `404` / `410`** | **三义陷阱**：① `queryId` 过期；② `x-client-transaction-id` 过期或缺失；③ 账号触发隐蔽风控锁 | **`schema_changed`** / **`risk_control`** | 允许刷新事务密钥（或单飞刷新 `queryId`）**最多 1 次**；仍 404（或 `settings.json` 同步异常）即按 `risk_control` / `schema_changed` 熔断停止 |
| HTTP `400` GraphQL（`The following features cannot be null` / `Query: Unspecified`） | 前端 `features` 开关或 `queryId` 漂移 | **`schema_changed`** | 停止本轮，报接口格式变化 |
| HTTP `400` / code `214` | 请求变量或游标参数无效 | **`bad_request`** | 确定性错误，不重试 |
| HTTP `403` / code `37` | 凭证拒收或目标封禁/无权（**须先检是否伴随 `64`/`326`/`226`**） | **`forbidden`**（若伴随账号态则归 `risk_control`） | 无账号违规码时作单目标错误，不触发整站熔断 |
| HTTP `403` / code `63` | 目标用户已封禁 | **`unavailable`** | 单目标错误，登录账号健康，不熔断 |
| HTTP `403` / code `136`、`161`、`179`、`219` | 被目标拉黑 / 受保护帖子无权查看 / 可见性受限 | **`forbidden`** | 单目标错误，不熔断 |
| HTTP `404` / code `34`、`50`、`144` | 页面/用户/帖子不存在或已删除 | **`unavailable`** | 确定性缺失，不熔断 |
| HTTP `5xx` / code `131` / `LoadShed`（`-1`） | X 服务端内部故障或过载降级 | **`network`** | 不归咎账号；API 交由用户手动重试，退避不误标失效 |

<a id="rate-headers"></a>
### 10.2 限流响应头（`x-rate-limit-*`）与主动刹车机制

- X REST 与 GraphQL 响应均返回标准限流响应头：`x-rate-limit-limit`（当前 15 分钟窗口配额上限）、`x-rate-limit-remaining`（剩余次数）与 `x-rate-limit-reset`（窗口重置 Unix 秒）（`官方文档`、`源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）`）。
- 网页端 GraphQL 15 分钟逐端点限流普遍偏紧（社区常见 `UserTweets` / `TweetDetail` 约 `50–150` 次/15 分钟，`SearchTimeline` / `Following` 约 `50` 次/15 分钟；2026-11 确切数值列 **V-06 / V-08**）。
- **TRACE 主动刹车策略**：在客户端按 `OperationName` 维护内存限流表，**一旦观察到 `x-rate-limit-remaining <= 1`，立即提前刹车暂停向该端点（及本轮自动任务）发请求，直至 `x-rate-limit-reset + 随机抖动`**，尽量避免正面撞击硬 `429` 触发账号级风险升阶（`源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）`）。

<a id="anti-bot"></a>
### 10.3 前端反爬头深挖：`x-client-transaction-id` 与 `x-xp-forwarded-for`（对照抖音 `a_bogus`）

#### 1. `x-client-transaction-id` 两阶段构造规范（`源码阅读（多站点订阅参考，2026-11-22）`、`社区资料（2026-11-22）`）

- **当前强制范围**：已知 `UserTweetsAndReplies`、`SearchTimeline` 已强制校验，且 X 正持续扩大至更多 GraphQL 端点；缺失或过期常返回 **HTTP `404` 或 `403`**（全端点精确矩阵列 **V-04**）。
- **第 1 阶段（拉取页面资源提取会话级密钥 `(keyBytes, animationKey)`，按小时缓存）**：
  1. 拉取 `https://x.com/home` HTML，提取 `<meta name="twitter-site-verification" content="...">` 的 Base64 字节串 `keyBytes`，以及 4 个 SVG 动画节点（`[id^="loading-x-anim"]`，下标 `0..3`）中的 `<path d="...">` 贝塞尔曲线帧数据；
  2. 从 HTML 提取 `ondemand.s` webpack chunk 哈希，拉取 `https://abs.twimg.com/responsive-web/client-web/ondemand.s.<hash>a.js`，用正则 `\([A-Za-z_$][A-Za-z0-9_$]*\[(\d{1,2})\],\s*16\)` 提取动态字节下标（`rowIndex = indices[0]`、`keyByteIndices = indices[1:]`）；
  3. 结合 `keyBytes`、选中的 SVG 路径、三次贝塞尔与 RGB/变换矩阵插值算出十六进制 `animationKey`，长时缓存 `(keyBytes, animationKey)`；遇 HTTP `404` 时强制刷新缓存并**最多重试一次**。
- **第 2 阶段（逐请求本地签名，纯标准库零网络）**：
  1. 计算 `seconds = uint32(unixNow - 1682924400)`（4 字节小端序；纪元 `1682924400` = `2023-05-01T07:00:00Z`）；
  2. 计算 `digest = SHA-256(fmt.Sprintf("%s!%s!%d%s%s", METHOD, PATH, seconds, "obfiowerehiring", animationKey))`；
  3. 拼装明文 `plain = keyBytes || timeBytes(4B LE) || digest[0:16] || byte(3)`；
  4. 生成 1 字节随机掩码 `r`，对 `plain` 逐字节与 `r` 异或，前置 `r` 后做无填充 Base64 编码即为 `x-client-transaction-id`。
- **脆弱点**：一旦 X 调整 `x.com/home` 壳结构（含 2026 Vite `x-web` 迁移）、SVG 动画节点或 `ondemand.s` 压缩模式，第 1 阶段正则即告失效（必须做好明确 `schema_changed` 降级与单飞冷却）。

#### 2. `x-xp-forwarded-for`（XPFF）构造规范（`社区资料（2026-11-22）`）

- **引入背景**：2025 年起引入，已在 `SearchTimeline`、`Followers` 等敏感 GraphQL 端点观察到校验（2026-11 在常规浏览端点是否强制为 `unknown`，列 **V-04**）。
- **原理与算法**：网页端由 Go 编译的 WebAssembly 模块（`loader.FwdForSdk-*.js`，暴露 `window.getForwardedForStr()`）生成，载荷逻辑纯对称加密：
  1. 派生密钥 `key = SHA-256(<WASM 内置 64 字符十六进制基础密钥> + urlencoded(guest_id))`（**依赖粘贴 Cookie 保留的 `guest_id`**）；
  2. 明文 JSON：`{"navigator_properties":{"hasBeenActive":"true","userAgent":"<UA>","webdriver":"false"},"created_at":<unix_ms>}`；
  3. 以随机 12 字节 nonce 执行 **AES-256-GCM** 加密，将 `nonce || ciphertext || tag` 十六进制编码输出；
  4. **有效期仅 `300,000 ms`（5 分钟）**，客户端应缓存约 `4` 分钟即刷新。
- **脆弱点**：若 X 轮换 WASM 内的基础密钥或增补浏览器遥测字段，需要同步更新常量。

#### 3. 维护成本对照：X 反爬头 vs 抖音 `a_bogus`（对照[抖音选型 §4](2026-11-12-douyin.md#a-review)）

| 维度 | 抖音 `a_bogus`（现行） | **X 网页反爬体系（`queryId` + `txid` + `XPFF`）** |
| --- | --- | --- |
| **独立变动目标数** | **`1` 个**（离线纯密码算法 `bdms.js`） | **`3+` 个并行变动面**：① `queryId`/`features`（每 1～2 周 + 2026 Vite `x-web` 迁移）；② `x-client-transaction-id`（动态抓 HTML + SVG + `ondemand.s.*.js`）；③ `x-xp-forwarded-for`（WASM 密钥 + `guest_id`）及 TLS/HTTP2 指纹敏感度 |
| **是否需额外抓网页静态资源** | **否**（100% 离线纯函数计算） | **是**（需抓取 `x.com/home` HTML 与 `abs.twimg.com` 的 `ondemand.s.*.js` 以组装事务密钥） |
| **历史失效周期** | 约每 **3～6 个月**局部调整一次 | `queryId` 约每 **1～2 周**；前端反爬头约每 **数月**随前端重构波动 |
| **综合维护成本判断** | 中等可控（460 LOC 离线单包） | **显著高于抖音，远高于哔哩哔哩与微博**；必须配合保守请求节奏、单飞冷却刷新与 `schema_changed` 明确停机边界 |

<a id="pacing"></a>
### 10.4 人机挑战 / 锁号处置与请求节奏建议（四站调度对齐）

- **人机验证（Arkose Labs FunCaptcha）与账号锁定处置红线**：遇到 code `326`、code `226`、code `64`、Arkose 挑战特征或登录重定向，**立即中止本轮并触发 `site="x"` 独立熔断与指数退避，零自动重试，严禁触碰、绕过或解算任何验证码**；不清空既有凭证，引导用户在系统浏览器手动解锁后再重试。
- **四站点按站点调度参数对齐表**（`源码阅读（仓库代码，2026-11-22）`、`推测（2026-11-22）`）：

| 参数维度 | 哔哩哔哩（现行） | 抖音（现行） | 微博（现行） | **X（建议默认值）** | X 从严依据 |
| --- | --- | --- | --- | --- | --- |
| **API 并发度** | 串行（`1`） | 串行（`1`） | 串行（`1`） | **串行（`1`）** | 杜绝并发抢限流与同秒突发触发 `226`/`326` |
| **API 最小请求间隔** | `500 ms` | `1000 ms + 0～300 ms` | `1200 ms + 0～400 ms` | **`4000 ms + 0～2000 ms` 随机抖动** | X 15 分钟窗口远紧于三站且对固定机械节律极敏感（用户主动点击与自动巡检共享串行闸） |
| **超时 / 响应上限** | `15 s` / `4 MiB` | `15 s` / `4 MiB` | `15 s` / `4 MiB` | **`15 s` / `4 MiB`**（`t.co` `10 s`/跳，≤ 5 跳） | 与现有三站安全上限一致 |
| **每小时自动请求硬上限** | `200` 次/小时 | `120` 次/小时 | `100` 次/小时 | **`60` 次/小时**（可调上界 `<= 80`） | 远低于 15 分钟窗口硬墙，大幅压低 24h 自动化风控分 |
| **定时检查单轮账号上限** | `30` 个/轮 | `20` 个/轮 | `20` 个/轮 | **`15` 个/轮** | 单账号常规 1 页（最多 2 页），控制单轮连发体量 |
| **账号间检查间隔** | `2000 ms` | `2500 ms` | `3000 ms` | **`>= 5000 ms`**（叠加随机抖动） | 拉开跨账号连续查询间距 |
| **动态限流刹车** | — | — | — | **`x-rate-limit-remaining <= 1` 提前等待至 `reset`** | X 独有响应头优势，主动规避硬 `429` |
| **退避与熔断隔离** | `site="bilibili"` | `site="douyin"` | `site="weibo"` | **`site="x"` 独立行（`site_scheduler_state`）** | `min(IntervalMin * 2^min(level,10), 24h)` + `pauseSitePeers("x")`，**与 B 站、抖音、微博完全隔离互不拖累** |

---

<a id="oss"></a>
## 11. 开源选型与自研规模摘要（R-J）

完整检索渠道、查询词与 5 大类具名候选的六维证据表见独立报告 **[X 开源客户端与协议依赖选型](2026-11-22-x.md)**（查询日期 `2026-11-22`）。核心结论摘要如下：

1. **第三方 Go 库核查结果（`官方文档（2026-11-22）`、GitHub REST API / `proxy.golang.org` / `pkg.go.dev`）**：
   - **官方 X API v2 SDK**：X 官方组织（`xdevplatform`）**无 Go 通用 API v2 SDK**（`xdk` 仅生成 Python/TypeScript/Java/C#；`xurl` 为重型 CLI；`playground` 无许可；`chat-xdk` 仅加密私信）。社区 `michimani/gotwi` 近半年仅机器人提交且无 OAuth 2.0 PKCE，`sivchari/gotwtr` 仅 Bearer 且停更 16 月+，`g8rswimmer/go-twitter/v2` 近 4 年停维，`dghubble/go-twitter` 已归档。
   - **非官方网页 / GraphQL 客户端**：**0 个可嵌入库**（`n0madic` 已归档、`imperatrona` 与 `Davincible` 停维、`anatolykoptev/go-twitter` 与 `DezXBT/AgentX` 强拉重型 TLS 伪装/遥测/CLI 依赖且与 TRACE 架构冲突）。
   - **反爬请求头库**：`x-xp-forwarded-for` 为 **0** 个独立 Go 包；`x-client-transaction-id` 仅见 0-star / 3 次提交的极早期 MIT 移植（`ach968/x-client-transaction-id-go`，依赖 `golang.org/x/net`）及两个**无许可证**停维服务端应用（强制排除）。
   - **Snowflake 解码**：`bwmarrin/snowflake` 面向分布式生成，解码仅 15～25 行纯 Go 位运算，无需外部库。
   - **`LOOP-008` / `LOOP-009` 复核**：`github.com/bluenviron/mediacommon/v2` 已升至 **`v2.10.0`**（`2026-11-15`，**2026-11-22 恰满 DEC-048 7 天冷却期**，MIT，新增跨 PES 的 ADTS 解复用修复），仍是唯一 HLS-TS 候选；但因建议 M7 MVP 延后 X 直播与 Spaces，**本阶段暂不引入**（省下约 `+2.10 MiB` 二进制体积）。
2. **依赖增量与自写规模估算（`源码阅读（仓库代码，2026-11-22）`、`推测（2026-11-22）`）**：
   - **网页协议路线（推荐默认）**：**`0` 个新增 Go 第三方依赖**（`go.mod` 零增量），`internal/site/x` 洁净室自写约 **`2,200–2,800` 生产 LOC**（核心协议 `1,800–2,500` LOC）+ `~1,600–2,200` 测试 LOC；跨包接线约 `1,200–1,800` 生产 LOC。
   - **官方 API v2 路线（Q 项备选）**：**`0` 个 X SDK 依赖**（纯标准库 `net/http` + PKCE，或按需评估 `golang.org/x/oauth2`），`internal/site/x` 自写约 **`1,000–1,350` 生产 LOC** + `~800–1,100` 测试 LOC。

---

<a id="risks"></a>
## 12. 平台规则、政策条文与账号风险（R-K）

> 声明：本节**仅客观陈述平台公开条文文本与社区已知的工程/账号风险事实，不做任何法律定性或法律建议**。因调研红线禁止直访 `x.com` 且 `help.x.com` 对直接 HTTP 读取返回 `HTTP 403`（严格遵守未绕过），`docs.x.com` 条款均为 `官方文档（2026-11-22）` 一手核对，`x.com/en/tos` 与 `help.x.com` 条文经公开搜索交叉核实标为 `社区资料（2026-11-22）`。

<a id="policy-facts"></a>
### 12.1 现行官方条文事实摘要（查询日期：`2026-11-22`）

| 文件 / 政策 | 官方 URL | 现行公开条文事实摘要 | 证据等级 |
| --- | --- | --- | --- |
| **X 服务条款（Terms of Service）** | `https://x.com/en/tos`（另见 `https://x.com/tos`） | ① 禁止通过当前公开接口（published interfaces，即官方 API）以外的自动化或非自动化手段访问或搜索服务；② 明文声明未经事先书面同意，禁止以任何形式、为任何目的抓取或爬取服务（*“crawling or scraping the Services in any form, for any purpose without our prior written consent is expressly prohibited”*，已移除历史 `robots.txt` 例外）；③ 包含大规模抓取违约金条款：24 小时内违规请求/浏览/访问超过 `1,000,000` 条帖子，约定每百万帖 `$15,000`（欧盟/英国 `€15,000`）违约金 | `社区资料（2026-11-22）` |
| **X 自动化规则（Automation Rules）** | `https://help.x.com/en/rules-and-policies/x-automation` | ① 自动化行为须通过官方 X API 与授权 OAuth 进行，禁止以脚本操作网页、无头浏览器模拟登录或非官方抓取工具实施自动化；② 严禁批量/激进关注、取关、点赞、非受邀回复及多号协同；③ 纯自动化账号须启用 Automated 标签 | `官方文档`（`policy.mdx` 引用）+ `社区资料（2026-11-22）` |
| **X 平台规则与反操控政策** | `https://help.x.com/en/rules-and-policies/x-rules`、`platform-manipulation` | 禁止平台操控、垃圾信息、规避限流或封禁与非真实批量行为；违规可导致限流、功能受限、账号锁定或永久封禁 | `官方文档`（`agreement.mdx` 引用）+ `社区资料（2026-11-22）` |
| **开发者协议与 PPU 协议** | `https://docs.x.com/developer-terms/agreement`（Last Updated: `2026-04-27`）、`ppu-agreement` | §III.A 禁止逆向工程、干扰 API、构建替代客户端、转授凭证、规避限流、训练基础/前沿 AI 模型；§III.L 限定 PPU 面向 hobbyist 与有限终端用户；§IV.B 要求离线数据在 24 小时内同步删改；§VII.B/E 预付积分不退款且须验证手机号；§VII.I 终止时须永久删除全部本地副本 | `官方文档（2026-11-22）` |
| **开发者政策、受限用例与展示规范** | `https://docs.x.com/developer-terms/policy`、`restricted-use-cases`、`display-requirements` | 禁止收集用户密码/Cookie/开发者密钥；禁止要求终端用户自行注册 App 以使用同一工具；禁止监控追踪敏感群体或事件；展示须带官方 X Logo 与原文链接且禁附第三方“订阅”等操作按钮 | `官方文档（2026-11-22）` |

<a id="account-risk"></a>
### 12.2 个人账号 Cookie 只读自动化已知风险与工程缓解

即便 TRACE **100% 严格只读**（零发帖、零点赞、零关注），使用个人账号网页 Cookie 调用内部 GraphQL 接口仍存在显著已知风险（`社区资料（2026-11-22）`）：

1. **账号临时锁定（`code 326`）**：触发异常流量、非浏览器指纹（TLS/HTTP2/请求头失配）或 IP 跨区跳变时，账号将被锁定并强制要求短信（SMS）或邮箱验证码解锁；未绑有效手机/邮箱的账号难以恢复。
2. **Arkose Labs（FunCaptcha）人机挑战**：高频翻页或异常会话会触发交互式拼图/推理挑战并拒绝后续接口访问。
3. **严格限流与静默假空壳（HTTP `429` / `{"data":{"user":{}}}`）**：15 分钟逐端点限流极紧，持续撞墙会迅速升级为账号锁定或滥用标记。
4. **搜索降权 / 影封（Shadowban / Search Suggestion Ban）**：自动化行为特征可能导致账号在回复排序与搜索建议中被无通知降权，使用主力社交号将直接影响日常使用。
5. **永久封禁（`code 64`）与 VPS 数据中心 IP 高危**：高频固定周期轮询、深度批量翻页，或将住宅浏览器复制的 Cookie 部署至云厂商机房 IP（AWS/GCP/DigitalOcean 等，**直接影响 `trace-server` 远程 VPS 模式**）是社区实测触发锁号与封禁的最高危组合。

**工程与使用缓解措施**：
- **专用小号 / 测试账号隔离**：强烈建议全程使用与主力号隔离的专用备用账号（不承载重要社交关系与资产）。
- **严格只读 + 绝不存密码**：仅保留只读 GET 路径，永不实现任何写操作，永不接收或保存用户密码。
- **低频带抖动 + 主动限流刹车**：采用 [§10.4](#pacing) 保守参数（串行 `4–6s` 间隔、每小时 `<= 60` 次、单轮 `<= 15` 账号、提前在 `x-rate-limit-remaining <= 1` 刹车、禁止后台预取），“全部缓存”严格走慢速队列且不自动跑 `SearchTimeline`。
- **遇挑战/锁号零重试**：触发 `401`/`403`(`64`/`226`/`326`)/`429`/空壳立即熔断 X 站点并指数退避，绝不自动重试、绝不解验证码。
- **网络与会话卫生**：媒体 CDN 与 `t.co` 全程零 Cookie；保持与导出 Cookie 浏览器一致的出口 IP/代理与桌面 Chrome UA，避免本地与远程 VPS 用同一账号并发请求。

<a id="ui-disclosures"></a>
### 12.3 TRACE 界面必须向用户明示的披露项（对齐 [界面规范](../ui.md)）

1. **登录页 › X（Cookie 路线，提交按钮上方醒目可读区，中立陈述事实、不做法律结论）**：
   - 说明此方式通过网页会话 Cookie 访问 X，并非 X 官方 API；X 服务条款与自动化规则限制非官方自动化访问与抓取。
   - 明确提示存在限流、人机验证、临时锁定（需手机/邮箱验证）、搜索降权乃至封禁风险，**强烈建议使用专用的备用/测试账号，避免使用主力个人账号**。
   - 说明 TRACE 对 X 严格只读、不保存密码，Cookie 仅按当前用户 DPAPI 加密保存在本机且不自动续期（失效需重新粘贴）。
2. **设置 › 远程（`trace-server` 推送 X Cookie 处）**：
   - 醒目提示：**云服务器/机房 IP 使用网页 Cookie 访问 X 触发人机验证或锁号的概率显著高于本机住宅网络**；不建议本地定时检查与远程 VPS 同时使用同一 X 账号。
3. **用户页“全部缓存” / 缓存规则启用 X 定时检查处**：
   - 提示 X 限流极紧，后台将按保守低频慢速执行且“全部缓存”止于 X 时间线可见上限（约 3,200 条），遇限流或风控立即停止等待。
4. **错误横幅（`FR-ERR-001`）**：
   - 严格区分“登录已失效”“X 限流（展示预计重置时间）”“X 要求人机验证或账号已临时锁定（code 326/226）”：明确告知 TRACE 已停止 X 全部自动请求、不会自动绕过验证，请先在系统浏览器手动处理后再重试。
5. **若采用或新增官方 API（BYO）模式时的额外披露**：
   - 明示 X API 为**预付按量计费**（读取帖子 `$0.005`/条、单账号全部缓存约 `$16+`），强烈建议在 `console.x.com` 设定月度消费上限（Spending limit）；提示用户审阅 X 开发者协议的删改同步与展示规范，并说明 `offline.access` 令牌将每 2 小时自动刷新。

---

<a id="verify-list"></a>
## 13. 待真实验证清单与最小有界检查程序（R-L）

<a id="v-items"></a>
### 13.1 待真实验证项清单（`V-01`～`V-12`）

以下汇总全部静态调研无法锁定的 `unknown` 事项，供 Agent A 在修订 1 中编入 `USER-ACTIONS.md` 的有界真实检查（ACT 项）。**强烈建议用户在授权检查时提供专用测试/小号的 `x.com` Cookie（单独存于 Git 忽略的 `.local/site-secrets/x.cookie`），切勿使用主力个人账号。**

#### A. 网页路线 MVP 核心必验项（`V-01`～`V-09`）

| 编号 | 待验证问题（当前 `unknown` 或存在社区变化风险） | 目标类型 | 建议请求额度 | 对应章节 |
| --- | --- | --- | --- | --- |
| **V-01** | **严格登录校验、无 Cookie 对照、公开 Bearer、Cookie 最小子集与 `kdt`**：验证 `GET /1.1/account/settings.json`（`x.com/i/api/...` vs `api.x.com/...`）在无 Cookie（预期 HTTP `401` code `32`/`89`）与带清洗后 `auth_token`+`ct0`+`twid`+`guest_id`（对比含/不含 `kdt`）下的响应，以及随后的 `UserByScreenName` 与 `twid` 交叉核对闭环 | 专用测试账号（只读） | JSON API `<= 4` 次（无 Cookie 1 + 登录校验 2 + `UserByScreenName` 1） | [§2.1](#cookie-clean)、[§2.3](#bearer-guest)、[§2.4](#login-verify) |
| **V-02** | **`ct0` 服务端 `Set-Cookie` 轮换与 `353` 恢复边界**：观察正常响应是否下发 `Set-Cookie: ct0=...`，并在单次受控错配头请求下确认 HTTP `403` code `353` 是否附带可采纳的新 `ct0`（严禁反复尝试） | 专用测试账号（只读） | JSON API `<= 1` 次 | [§2.2](#csrf-headers)、[§10.1](#error-mapping) |
| **V-03** | **`t.co` 无 Cookie 跳转与未登录登录墙现状**：验证 1 个公开 X `t.co` 短链在无 Cookie 下的 `302` 跳数与落点；验证无 Cookie 调 `UserTweets` / `TweetResultByRestId`（及可选 Syndication 1 次）的拦截形态，锁定无访客产品模式依据 | 1 个公开 `t.co` 链 + 1 条公开帖 | 短链探针 `<= 3` 跳（无 Cookie）+ 公开 API `<= 2` 次 | [§2.3](#bearer-guest)、[§4.1](#url-forms)、[§4.4](#url-priority) |
| **V-04** | **`x-client-transaction-id` / `x-xp-forwarded-for` 实测强制矩阵与 2026 Vite `x-web` 静态结构**：抓 1 次 `x.com/home` 与对应 `ondemand.s.*.js` 核实 `<meta name="twitter-site-verification">`、`loading-x-anim` SVG 与 2026 Vite chunk 形态；在后续核心端点调用中记录各端点带/不带事务头与 XPFF 的通过情况 | 首页静态壳 + 复用后续端点请求 | 静态资源 `<= 2` 次（`<= 2 MiB`，不重复计业务 API） | [§5.1](#post-graphql)、[§10.3](#anti-bot) |
| **V-05** | **单条帖子全形态解析与 `fieldToggles`**：核实 `TweetDetail` 与 `TweetResultByRestId` 所需 `fieldToggles` 组合、`userResult.core` 新结构、`TweetPreviewDisplay` / `TweetWithVisibilityResults` 解包、`note_tweet`（含转帖/引用长帖）、自回复线程、转引原帖已删（`TweetTombstone`/`TweetUnavailable`）、多图/混排、GIF、投票/卡片/X Articles 降级及 `edit_control` | 2～3 条公开覆盖样本帖（长帖/线程/混排/转引） | JSON API `<= 3` 次 | [§5.1](#post-graphql)、[§5.2](#post-schema)、[§5.3](#threads-retweets)、[§5.4](#post-media)、[§5.5](#post-degrade) |
| **V-06** | **用户资料、`UserTweets` / `UserMedia` 游标翻页、单调性与限流头**：核实 `UserByRestId`、`UserTweets` 第 1→2 页 `Bottom` 游标（含 `TimelinePinEntry` 与转帖外层雪花 ID 单调性）、`UserMedia` 的 `profile-grid-0` / `TimelineAddToModule` 结构、各端点 `x-rate-limit-limit` 实值及无异常空页判别 | 1 个公开活跃创作者（有置顶/转帖/媒体） | JSON API `<= 5` 次（资料 1 + `UserTweets` 2 页 + `UserMedia` 2 页） | [§7.1](#user-timelines)、[§7.2](#monotonicity)、[§7.3](#empty-truth-table)、[§10.2](#rate-headers) |
| **V-07** | **媒体 CDN 无 Cookie 拉取、视频多档 vs HLS-only 与本地 `mp4ff` 校验**：验证 `pbs.twimg.com` `?format=<ext>&name=orig` 原图、`video.twimg.com` 最高码率 MP4 在无 Cookie + 无 Referer 下的 `HTTP 206 Range`、是否带签名参数与是否 302 跨外域；核对是否存在 HLS-only `.m3u8`（fMP4 vs TS）并在本地跑 `mp4ff v0.63.0` 容器校验 | 复用 V-05 / V-06 样本图 + 1 条短视频（`< 30s`） | 媒体 CDN `<= 5` 次（图片 2 + 短视频 Range 3，合计 `<= 25 MiB`；本地校验 `0` 请求） | [§5.4](#post-media)、[§6.1](#cdn-domains)、[§6.2](#media-whitelist) |
| **V-08** | **“我的 › X”只读接口**：验证 `Following`（第 1→2 页，核对批量导入字段）、`Bookmarks`、`BookmarkFoldersSlice`（核对非 Premium 账号空结构优雅降级）与本人 `Likes`（确认响应结构及查他人拒绝表现） | 专用测试账号本人（只读 GET） | JSON API `<= 5` 次 | [§8](#mine) |
| **V-09** | **不存在 / 已删 / 受保护错误码确定性对照**：用明显不存在的合成 ID 与已知受保护/不可见样本锁定 JSON 错误码 `34`/`50`/`63`/`144`/`179` 与 `UserUnavailable` / `TweetUnavailable` 精确形态（严禁主动探测 `226`/`326` 账号级风控） | 1 个明显不存在的合成 ID + 可选 1 个公开受保护目标 | JSON API `<= 2` 次 | [§5.5](#post-degrade)、[§7.3](#empty-truth-table)、[§10.1](#error-mapping) |

#### B. 条件验证项（`V-10`～`V-12`，仅当 Q 项选择对应扩展或路线时执行）

| 编号 | 触发条件 | 待验证问题 | 目标类型 | 建议请求额度 | 对应章节 |
| --- | --- | --- | --- | --- | --- |
| **V-10** | 仅当评估 `SearchTimeline` 历史补抓时 | 验证 `SearchTimeline` 对 `x-client-transaction-id` + XPFF 的校验、实测 `x-rate-limit-limit` 及转帖/回复缺失表现 | 1 个公开创作者时间窗查询 | JSON API `<= 2` 次 | [§7.4](#sub-baseline)、[§10.3](#anti-bot) |
| **V-11** | 仅当纳入 X 直播与 Spaces 录制时 | 验证 `BroadcastQuery`、`AudioSpaceById`、`fleets/v1/avatar_content` 现行可用性，以及 `live_video_stream/status/<media_key>` 返回的 `.m3u8` 清晰度变体（`#EXT-X-STREAM-INF` 多档 vs 单档）与分片容器（MPEG-TS vs LHLS fMP4 vs 裸 AAC） | 1 个进行中的公开 Broadcast / Space | JSON API `<= 3` 次 + `.m3u8` 文本 `<= 1` 次（`<= 256 KiB`，不拉分片） | [§9.1](#live-proto)、[§9.2](#live-fit) |
| **V-12** | 仅当 `Q-X-1` 选择官方 API v2 BYO 路线时 | 验证 Native App OAuth 2.0 PKCE 本机回环回调（含是否支持 RFC 8252 动态端口）、`GET /2/users/me`、`/2/users/:id/tweets` + `media.fields=variants`、`/2/tweets/:id`（核对 `article` 字段是否含正文）及 `console.x.com` 账单对 `expansions` 的实际扣费规则 | 用户自建测试 Developer App（先设低月消费上限如 `$5`） | 官方 API `<= 6` 次（预计账单 `< $0.20`） | [§3.1](#api-pricing)、[§3.3](#api-coverage)、[§3.4](#api-auth-byo) |

<a id="check-procedure"></a>
### 13.2 供 Agent A 写入 ACT 的最小有界检查程序与额度建议

建议 Agent A 在用户确认走网页路线、提供**专用测试账号 Cookie**（存 `.local/site-secrets/x.cookie`）与公开检查对象后，按以下不重复计数的最小预算执行 **1 次**有界检查（全程严格串行、请求间隔 **`>= 5 秒` + 随机抖动**，且遵循 `x-rate-limit-remaining <= 1` 提前停止）：

| 步骤 | 接口 / 目标域名 | 覆盖 V 项 | 最多请求次数 | 体积 / 时长上限 |
| --- | --- | --- | --- | --- |
| 1. 静态壳与反爬头准备 | `x.com/home`、`abs.twimg.com/.../ondemand.s.*.js` | **V-04** | 静态资源 `<= 2` 次 | `<= 2 MiB` |
| 2. 严格登录对照与 `ct0` 核对 | `api.x.com/1.1/account/settings.json`、`UserByScreenName` | **V-01、V-02** | JSON API `<= 5` 次 | `<= 512 KiB` |
| 3. `t.co` 短链与未登录边界 | `t.co`（无 Cookie）+ 公开 API 对照 | **V-03** | 短链探针 `<= 3` 跳 + 公开 API `<= 2` 次 | `<= 256 KiB` |
| 4. 单帖全形态（长帖 / 线程 / 混排 / 转引） | `TweetDetail`、`TweetResultByRestId` | **V-05** | JSON API `<= 3` 次 | `<= 2 MiB` |
| 5. 用户资料、`UserTweets` 与 `UserMedia` 翻页 | `UserByRestId`、`UserTweets`、`UserMedia` | **V-06** | JSON API `<= 5` 次 | `<= 3 MiB` |
| 6. 不存在 / 受保护确定性错误码 | `UserByRestId`、`TweetResultByRestId` | **V-09** | JSON API `<= 2` 次 | `<= 256 KiB` |
| 7. “我的”只读列表 | `Following`、`Bookmarks`、`BookmarkFoldersSlice`、`Likes` | **V-08** | JSON API `<= 5` 次 | `<= 2 MiB` |
| 8. 媒体 CDN 无 Cookie 拉取与本地 `mp4ff` 校验 | `pbs.twimg.com`、`video.twimg.com` | **V-07** | 媒体 CDN `<= 5` 次（本地校验 `0` 次） | `<= 25 MiB` |

- **网页路线 MVP 汇总硬上限（零重复计数）**：
  - 认证/公开 JSON API：**`<= 22` 次**（含步骤 2～7）；
  - 静态资源拉取（步骤 1）：**`<= 2` 次（`<= 2 MiB`）**；
  - 无 Cookie `t.co` 探针：**`<= 3` 跳**；
  - 无 Cookie 媒体 CDN：**`<= 5` 次（`<= 25 MiB`）**；
  - 单轮总请求合计 **`<= 32` 次**，总耗时 **`<= 5` 分钟**。
- **立即停止条件（零容忍熔断）**：任何一步遇到非预期 HTTP `429`（code `88`）、HTTP `403`（code `64` / `226` / `326`）、Arkose / CAPTCHA 挑战、非 V-02 的连续 `353`、带有效测试 Cookie 仍返回 HTTP `401`（code `32`/`89`）或 `{"data":{"user":{}}}` 假空壳、或任何响应头出现 `x-rate-limit-remaining <= 1`，**立即停止后续全部请求，零自动重试，绝不换号或尝试解验证码**，直接输出脱敏回执向用户报告。
- **脱敏回执规范**（写于 `artifacts/site-check/`）：仅记录操作名、请求计数、HTTP 状态码、X 错误码整数、`x-rate-limit-limit`/`remaining`、结构布尔指标（`hasCoreUser`、`hasNoteTweet`、`txidRequired`、`xpffRequired`、`range206`、`hasHLSOnly`、`mp4ffValid`）与字节数；**严禁写入任何 Cookie、Bearer 令牌、`x-csrf-token`、签名 URL、真实账号 `rest_id` / `screen_name` / 帖子原文**。

---

<a id="for-a"></a>
## 14. 给 A 的建议（R-L）

本节供 Agent A 编写 `M7 阶段计划修订 1`、更新需求与路线正本，并向用户提交 `USER-ACTIONS.md` 的 Q 项与 ACT 项。

<a id="route-rec"></a>
### 14.1 路线推荐结论

- **推荐默认路线：方案 A — 网页协议（粘贴 `x.com` Cookie，严格只读、低频保守节奏、界面醒目风险披露并强烈建议专用测试/小号），不做同会话混合。**
- **架构预留**：`internal/site/x` 内部通过清晰接口隔离传输与认证实现，使**方案 B — 官方 API v2（BYO OAuth 2.0 PKCE + Pay-Per-Use）** 可作为 `Q-X-1` 的清晰备选（若用户明确拒绝任何 Cookie 封号风险、愿意自建开发者 App 绑卡设月消费上限并接受无直播/Spaces、无 `UserMedia`、高价全部缓存等裁剪）或后续独立 LOOP。

<a id="mvp-table"></a>
### 14.2 X “MVP 必须 / 可由后续 loop 深化”建议表

对照[路线与验收 §3](../delivery/roadmap.md#mvp-loop) 抖音与微博两行，按 TRACE 全站基线整理：

| 维度 | X MVP 必须能完成的流程 | 可由后续 loop 深化 |
| --- | --- | --- |
| **登录与凭证** | 粘贴 `x.com` Cookie 登录（白名单保留 `auth_token`/`ct0`/`twid`/`guest_id`/`kdt`、剥离追踪与 `__cf_bm`、`GET /1.1/account/settings.json` 无 Cookie 对照 + `UserByScreenName` 核对 `rest_id` 与 `twid`；独立加密文件 `credentials/x.bin` + 熵 `trace:x:credential:v1`；不自动续期、仅被动采纳服务端 `Set-Cookie: ct0`；界面醒目披露规则与小号建议；与 B 站/抖音/微博登录完全独立） | 官方 API v2（BYO OAuth 2.0 PKCE）可选后端；同站多登录账号（`FR-AUTH-006`） |
| **链接与解析** | 分享文本、`t.co` 短链（无 Cookie、≤ 5 跳、落点限 X 域）、第三方嵌入镜像（`fxtwitter`/`fixupx`/`vxtwitter`/`fixvx`/`twittpr`/`nitter.net`/`xcancel.com` 只本地归一化为 `x.com`，零请求镜像）、单帖（含 `/photo/`、`/video/`、`/i/web/status/`）、用户主页（`/<handle>`、`/i/user/<rest_id>`、`/intent/user`）、雪花 ID 校验；直播/Spaces/独立文章/社区/搜索/保留路径本地拒绝且零 API 请求；纯数字继续归 B 站、`@handle` 默认不误抢；记入最近解析 | 显式前缀（如 `x:@handle` / `x:<id>`）；列表（`/i/lists/`）与 X Articles 正文独立解析 |
| **详情与用户页** | 单帖详情（`TweetDetail` + `TweetResultByRestId`、2025/2026 `userResult.core` 兼容、`TweetPreviewDisplay` 先于 `TweetWithVisibilityResults` 解包、`note_tweet` 覆盖主帖+转帖+引用帖、作者自回复线程、转引原帖已删/受保护降级、最多 4 附件含图片+GIF+视频混排、最高码率 MP4、投票/卡片/X Articles/直播卡片安全降级、编辑标记、在线互动数不落盘）；用户页（资料、倒序翻页、置顶标记、“全部 / 未缓存”筛选、防 `{"data":{"user":{}}}` 假空壳真值表、加入订阅） | `UserTweetsAndReplies` 回复标签与 `UserMedia` 独立媒体网格页签；评论浏览；X Articles Draft.js 富文本渲染；社区笔记展示 |
| **缓存与媒体库** | 缓存单帖正文（含 `note_tweet` 全文与自回复线程）、全部原图（`?format=<ext>&name=orig`）、GIF（MP4）与最高码率单文件 MP4（无 Cookie / 无 Referer CDN、`Range` 续传、`mp4ff` `moov` 前置校验）；转帖按 [DEC-056](../decisions.md#dec-056) 存于转发者 `forwards/` 目录并记录 `via`；用户页“全部缓存”慢速队列（沿 `UserTweets` 只补原创、抵达 ~3,200 上限清晰收束、不跑 `SearchTimeline`）；签名地址 `json:"-"` 不落盘 | HLS-only（fMP4 / TS）长视频下载支持（依赖 `LOOP-008`）；编辑修订历史多版本对比 |
| **订阅、检查与“我的”** | 订阅关联 X 账号（稳定主键 `rest_id`，支持四站跨站订阅）；手动/定时检查更新（首次建起点、`TimelinePinEntry` 置顶不打断、转帖按外层雪花 ID 保单调、过滤推广卡、假空壳不移起点）；按共用规则自动缓存；从 X 关注列表批量导入订阅；“我的 › X”只读内存浏览关注、书签、本人喜欢（书签文件夹与列表默认开关关闭，待 V-08 证实后开放） | 书签文件夹筛选（Premium 实测后）；我的列表（Lists）；一键缓存整个书签 |
| **直播与 Spaces** | 识别 Broadcasts 与 Spaces 链接及帖内卡片，安全提示“X 直播与 Spaces 暂不支持录制，可在浏览器中打开”，零开播巡检请求 | 视频直播与 Spaces 录制（依赖 `LOOP-008` HLS-TS 与 `LOOP-009` 纯音频 `.m4a` + **V-11** 实测） |
| **多站点隔离与远程版** | 站点小字标记 **`X`** 与主页/媒体库/订阅四站筛选；X 独立保守节奏（串行 `4–6s`、每小时 `60` 次、单轮 `15` 账号、账号间隔 `>= 5s`、`x-rate-limit-remaining <= 1` 主动刹车、独立熔断退避）；`trace-server` 支持同步 X 订阅与推送 X Cookie（**显式确认与 VPS 机房 IP 高风险警告**） | X 远端直播录制（随直播 loop）；官方 API 模式的远端鉴权方案 |

<a id="fr-drafts"></a>
### 14.3 需求编号草案（`FR-X-001`～`FR-X-010` 与 `NFR-NET-005` / `NFR-SEC-005`）

对照[需求 §2.11](../requirements.md#weibo-scope)（`FR-WB-001`～`010`）与[§3](../requirements.md#nfr) 拟定：

| 草案编号 | 需求草案说明 |
| --- | --- |
| **FR-X-001** | **X 登录（网页 Cookie）**：登录页与“设置 › 账号”的 X 一栏支持粘贴浏览器已登录 `x.com` 的 Cookie（附获取方法与风险披露：非官方 API、只读不存密码、强烈建议使用专用备用/测试账号）。按白名单只保留 `auth_token`、`ct0`、`twid`、`guest_id`、`kdt` 并剥离广告/追踪/`__cf_bm`，本地预检 `auth_token` 与 `ct0` 后经无 Cookie 对照的 `settings.json` + `UserByScreenName`（并核对 `twid`）两步严格确认后才加密保存；无效、缺字段或遇锁号/验证分别说明且不保存；提交后立即清空输入框。不自动续期，仅在响应下发有效 `Set-Cookie: ct0` 时同步上下文 CSRF 令牌；可独立退出，不影响另外三个站点 |
| **FR-X-002** | **X 链接与镜像归一化**：解析入口接受 X 分享文本、`t.co` 短链（无 Cookie、≤ 5 跳、最终落点仅允许 X 域名，外站拒绝）、单帖（`x.com` / `twitter.com` / `mobile.*` / `/i/web/status/` / `/photo/` / `/video/`）、用户主页（`/<handle>`、`/i/user/<rest_id>`、`/intent/user`），以及常见第三方嵌入镜像（`fxtwitter`/`fixupx`/`vxtwitter`/`fixvx`/`twittpr`/`nitter.net`/`xcancel.com`，仅本地重写为 `x.com`，零请求镜像）。直播（`/i/broadcasts/`）、Spaces（`/i/spaces/`）、独立文章、社区、搜索、保留路径等本地拒绝并说明（直播与 Spaces 提示可在浏览器中打开），零 API 请求；纯数字与裸 `@handle` 不抢占现有站点路由。记入最近解析（`FR-HIST-001`） |
| **FR-X-003** | **单条 X 帖子详情**：显示作者（头像、昵称、`@handle`，点击进用户页）、发布时间、完整正文（自动展开 `note_tweet` 长帖及转帖/引用长帖，还原 `t.co` 展开链接）、作者自回复线程、转帖与引用帖（原帖已删、受保护或地区屏蔽时保留外层并说明原因）、最多 4 项图片/GIF/视频混排（原图、GIF 循环视频、视频封面与时长）、编辑标记与受限可见性提示；投票、链接卡片、X Articles 与直播/Spaces 卡片安全降级显示摘要与“在浏览器中打开”；互动计数仅在线展示不落盘。“⋯”菜单提供缓存、复制链接、在浏览器中打开 |
| **FR-X-004** | **X 用户页**：显示头像、昵称、`@handle`、简介、认证状态、关注/粉丝/帖子数；“帖子”列表按时间倒序、滚到底按 `Bottom` 游标加载下一页且跨页去重、置顶标“置顶”、支持“全部 / 未缓存”筛选；遇到 X ~3,200 帖时间线上限、受保护、封禁/拉黑、`{"data":{"user":{}}}` 假空壳、登录失效、风控或真实 0 帖时按真值表严格区分说明，失败停在原处可重试。显示所属订阅或“加入订阅”（`FR-USER-003`），“⋯”提供“全部缓存”（`FR-USER-004`） |
| **FR-X-005** | **缓存 X 视频与 GIF**：按帖子 `status_id` 只存一份；从 `video_info.variants[]` 取当前账号有权访问的最高码率单文件 `video/mp4`（GIF 同样保存 MP4），经无 Cookie 无 Referer CDN 分段续传、`mp4ff` 容器与 `moov` 前置校验后连同封面与清单落盘；清单记录实际与可选码率/分辨率；HLS-only 且为 MPEG-TS 或加密流不绕过、明确说明；不做画质升级提醒 |
| **FR-X-006** | **缓存 X 正文、多图、混排与转帖**：保存完整正文（含 `note_tweet` 与作者自回复线程）、发布信息、按原顺序的全部原图（`?format=<ext>&name=orig`）及混排 GIF/视频页 MP4 与封面；单文件缺失记黄点不拖垮整条；转帖连同其中媒体按 [DEC-056](../decisions.md#dec-056) 存于转发者 `x/<rest_id>/forwards/<outer_id>/` 并记录 `via`，原帖不可见时完整保存外层内容。媒体与签名地址 `json:"-"` 不落盘 |
| **FR-X-007** | **订阅与 X 账号**：订阅可关联 X 账号（永久主键 `rest_id`，单订阅可跨 B 站、抖音、微博、X 四站）；手动/定时检查更新、自动缓存与“全部缓存”对 X 生效（首次建起点；`TimelinePinEntry` 置顶不打断早停；转帖用外层雪花 ID 保单调；剔除推广卡；假空壳不移起点；“全部缓存”仅沿 `UserTweets` 补原创并止于 ~3,200 上限，不自动调用 `SearchTimeline`）。未登录 X 时跳过 X 账号且不中断其余三站。支持从 X 关注列表批量导入订阅（`FR-SUBS-003`） |
| **FR-X-008** | **我的 › X（[DEC-047](../decisions.md#dec-047)）**：“我的”侧栏增加“X”分组，严格只读、仅驻留内存、退出即清；提供登录账号的**关注**、**书签**与**喜欢**（仅本人）；书签文件夹（X Premium）与列表仅在真实检查 **V-08** 证实可用且非 Premium 优雅降级后开放，否则默认隐藏记 LOOP |
| **FR-X-009** | **远程版与 X（[DEC-062](../decisions.md#dec-062)）**：支持同步 X 订阅及可选推送 X Cookie（远程经同样严格两步校验后 AES-GCM 加密保存、从不续期、不做全部缓存）；本地推送入口须展示 **VPS 机房 IP 高风险警告**并需用户显式确认开启（不默认静默自动推送） |
| **FR-X-010** | **X 后续深化**：官方 API v2（BYO OAuth 2.0 PKCE）备选后端、视频直播与 Spaces 录制（依赖 `LOOP-008` / `LOOP-009`）、书签文件夹与列表、`UserMedia` / 回复标签、X Articles 正文渲染、评论与 `SearchTimeline` 手动检索 |

**跨站既有需求与非功能需求联动（不重复编号）**：
- 扩展 `FR-AUTH-007`（四站独立登录）、`FR-SITE-001`（X 为第四个站点）、`FR-SITE-002`（小字标签增加 **`X`**，主页与媒体库筛选扩为“全部 / B 站 / 抖音 / 微博 / X”）、`FR-USER-004`、`FR-REMOTE-003`/`004`、`NFR-DATA-004`（修订 1 前须对照旧版代码核查 `subscriptions.json`、`ui-state.json`、`cache-queue.json` 读到 `site="x"` 时是否静默丢弃，按微博接入时的代码核对证据原则决定是否升级 schema 版本）。
- 新增 **`NFR-NET-005`（X 请求节奏与主动限流刹车）**：API 串行、`4000 ms + 0～2000 ms` 抖动、超时 `15 s` / 上限 `4 MiB`、接口禁用自动跳转、`t.co` 最多 `5` 跳（单跳 `10 s`、无 Cookie）；自动请求 `<= 60` 次/小时、单轮 `<= 15` 账号且账号间隔 `>= 5 s`、单账号最多 `2` 页；监听 `x-rate-limit-remaining <= 1` 主动刹车至 `reset`；遇风控（`64`/`226`/`326`/Arkose/假空壳）、限流（`429`/`88`）或失效时立即停止 X 全部自动任务并独立指数退避（最长 `24h`），零自动重试、绝不解验证码，与 B 站、抖音、微博完全隔离。
- 新增 **`NFR-SEC-005`（X 凭证隔离与脱敏）**：独立文件 `credentials/x.bin` 与独立熵 `trace:x:credential:v1` 加密保存；`auth_token`、`ct0`、`twid`、`guest_id`、`kdt`、`x-csrf-token` 及任何 OAuth 令牌与签名媒体地址绝不进日志、错误、历史、清单、数据库、界面状态、远程状态与包（哨兵测试全链路覆盖，并同步扩充 `tools/checkdocs` 密文扫描规则）；媒体 CDN 与 `t.co` 恒不带 Cookie。

<a id="q-drafts"></a>
### 14.4 Q 项草案表（`Q-X-1`～`Q-X-10`，供 A 编号写入 `USER-ACTIONS.md`）

以下 10 项均有安全、可逆的推荐默认值（均为非阻塞决定，用户未回复前可按推荐默认值推进；其中 **`Q-X-1` 路线选择**影响整体实现形态，建议 A 置顶请用户优先拍板）：

| 草案编号 | 问题 | 推荐默认值 | 备选方案 | 理由与可逆性 |
| --- | --- | --- | --- | --- |
| **Q-X-1** | **接入路线选择**：X 走网页协议（粘贴 Cookie），还是官方 API v2（用户自带开发者 App + 按量付费），还是组合？ | **方案 A：网页协议（粘贴 Cookie，只读低频，强烈建议测试/小号）**；不做同会话混合 | 方案 B：官方 API v2（BYO OAuth 2.0 PKCE + Pay-Per-Use，`$0.005/帖`）；方案 C：双后端可切换 | 2026 年 X 官方 API 已取消全部免费与月费档，按量计费单账号全部缓存约 `$16`、且仍与 BYO/删改同步/Logo 条款冲突并缺失线程、媒体页、直播与 Spaces；保持 `internal/site/x` 接口隔离后，后续仍可独立扩展方案 B（可逆） |
| **Q-X-2** | **站点 ID 与界面显示名** | 内部 `site.ID = "x"`；界面小字标签与筛选项统一显示 **`X`**（设置与说明首次出现可附注“X（原 Twitter）”） | 内部 `twitter` / 显示“X / Twitter” | 简洁对齐 2024-05 后的官方主域 `x.com` 与 TRACE 极简小字标签规范（`FR-SITE-002`），720 宽四站筛选绝不溢出（可逆） |
| **Q-X-3** | **登录方式** | **仅粘贴 `x.com` Cookie**（不实现账号密码登录、不保存密码；X 无网页扫码） | 若 `Q-X-1` 改选官方 API，则改用系统浏览器 OAuth 2.0 PKCE 固定本机回环回调 | 账号密码接口移动端自 2025-10 失效且网页端高危触发 Arkose/锁号，粘贴 Cookie 与抖音/微博一致且最安全（可逆） |
| **Q-X-4** | **专用测试 / 备用账号策略与界面风险提示** | 真实站点检查（ACT）**强烈建议并默认要求使用专用测试/小号**；登录页与设置页明示规则与封号/锁号风险、建议不用主力号（不强行阻止用户粘贴自有账号） | 仅在文档提示、界面不展示风险提示 | 兼顾用户知情权与账号安全，避免主力个人号因只读自动化被 `326` 锁定或搜索降权（可逆） |
| **Q-X-5** | **视频直播（Broadcasts）与 Spaces 语音范围** | **M7 MVP 延后至后续 loop**；仅识别链接与帖内卡片并提示“暂不支持录制，可在浏览器中打开”，零开播巡检请求 | 同步实现 Broadcasts / Spaces 录制（需先落地 `LOOP-008` 引入 `mediacommon/v2` `+2.1 MiB` + `LOOP-009` 纯音频 `.m4a` + **V-11**） | 缺 HLS-TS、缺纯音频录制、缺可靠批量开播发现接口，延后与微博 `DEC-065` 一致且不膨胀体积（可逆） |
| **Q-X-6** | **用户页“全部缓存”范围与深度边界** | **沿 `UserTweets` 慢速队列仅补原创帖**，触及 X ~3,200 帖上限正常停止并明确提示，**不自动调用 `SearchTimeline` 补更早历史** | ① 全部缓存也包含转帖；② 叠加 `SearchTimeline` 按日回溯突破 3,200 | 对齐微博“只补原创 + 如实停在平台可见上限”；`SearchTimeline` 限流紧、缺转帖且高危触发锁号/XPFF（可逆） |
| **Q-X-7** | **X 自动请求节奏与每小时配额默认值** | 串行 `4000 ms + 0～2000 ms` 抖动、每小时自动额度 **`<= 60` 次**、单轮 **`<= 15` 账号**、账号间隔 **`>= 5 s`**、`x-rate-limit-remaining <= 1` 主动刹车 | 放宽至微博档（`100` 次/小时、`20` 账号/轮）或更紧（`30` 次/小时） | X 15 分钟窗与锁号阈值显著严于前三个站点，保守起步更护号，实测后随时可调（可逆） |
| **Q-X-8** | **纯数字与裸 `@handle` 在解析框的路由策略** | **保持现状**：`<19` 位纯数字继续归哔哩哔哩 UID、`19` 位继续报歧义；**裸 `@handle` 不自动猜 X**（须贴 `x.com/...`、镜像链或分享文本） | ① 放行裸 `@handle` 直接路由 X；② 增加显式前缀 `x:@handle` | 避免破坏 B 站老用户习惯及误把微博/抖音 `@昵称` 文本送往 X；后续随时可追加 `x:` 显式前缀（可逆） |
| **Q-X-9** | **“我的 › X”与关注批量导入范围** | **M7 MVP 纳入关注（含批量导入订阅）、书签与本人喜欢**；书签文件夹（X Premium）与列表默认开关关闭，待 **V-08** 证实非 Premium 优雅降级后再开放 | 整个“我的 › X”放后续 loop；或一并开放全部 5 项 | `Following`/`Bookmarks`/`Likes` 均为高频实用且只读安全；门控文件夹/列表可防非 Premium 报错（可逆） |
| **Q-X-10** | **远程版 `trace-server` 的 X Cookie 推送默认行为** | **支持 X 订阅同步与 Cookie 推送，但 X Cookie 推送默认关闭/需用户在阅读“VPS 机房 IP 高锁号风险”警告后显式勾选开启**，绝不随登录静默推往 VPS | ① 同 B 站/抖音/微博登录后自动静默推送；② 完全禁止 X 接入远程版 | 住宅浏览器 Cookie 放云厂商机房 IP 是触发 X `326` 锁号与封禁的头号高危诱因，显式知情开启最负责任（可逆） |

<a id="top-risks"></a>
### 14.5 最大的 3 个风险 / `unknown` 摘要

1. **个人账号锁号 / 封禁与 VPS 机房 IP 风险（R-K / R-A / R-I）**：即便完全只读，使用网页 Cookie 访问 X 内部 GraphQL 仍与 X 服务条款及自动化规则冲突，并面临 `code 326` 临时锁定（需短信/邮箱解锁）、Arkose 人机挑战、搜索降权、`{"data":{"user":{}}}` 假空壳限流乃至 `code 64` 封号风险——在云厂商机房 IP（`trace-server`）下尤为剧烈。同时 `settings.json` 无 Cookie 对照、`kdt` 作用与 `353` `ct0` 轮换恢复边界（**V-01 / V-02**）必须实测锁定。
2. **三层动态前端变动面与 2026 Vite `x-web` 迁移（R-D / R-I）**：与抖音仅 1 个离线签名包不同，X 网页协议同时面对“每 1～2 周变更的 GraphQL `queryId`/`features` + 需动态抓取 `x.com/home` 与 `ondemand.s.*.js` 的 `x-client-transaction-id` + 5 分钟 TTL 的 WASM `x-xp-forwarded-for`”三重变动面，叠加 2026 年 webpack → Vite `x-web` 迁移，各端点当前强制要求矩阵与空体 `404` 三义区分必须由 **V-04** 实测确认，且长期维护成本显著高于前三站。
3. **官方 API 路线的高费用与条款张力、以及视频 HLS-only / 直播 Spaces 缺口（R-B / R-E / R-H）**：2026 年官方 API 已无任何免费与月费档，`$0.005/帖` 令个人高频归档账单高昂（单账号全部缓存约 `$16`），且仍受制于 BYO 多应用政策、24 小时删改同步义务、强制 X Logo/禁附订阅按钮及 `trace-server` 2 小时令牌冲突；而网页路线下高画质/长视频的 HLS-only（MPEG-TS vs fMP4）实际占比（**V-07**）、非 Premium 书签文件夹响应（**V-08**）及直播/Spaces 对 `LOOP-008`/`LOOP-009` 的前置依赖均待按优先级分步收口。

---

<a id="a-review"></a>
## 15. A 审阅定稿（2026-11-22）

A 按[站点规则](../dev-rules/sources-and-safety.md#site-rules)审阅了第 1～14 节与[选型证据表](2026-11-22-x.md)，并抽查了引用的仓库代码事实：`site.ParseID`、`site.Valid` 只认三站点；`subscription.SiteHourlyQuotaLimit` 对不认识的站点**返回 0**（现已改为不再落回哔哩哔哩——所以 X 不显式加分支就永远拿不到额度，而不是“按哔哩哔哩跑”）；`site/registry.go` 的 19 位纯数字规则与 `reBareKnownDomain` 属实；`internal/live/manager.go` 仍有 `case "", site.Bilibili` 这类空值归哔哩哔哩的分支。官方 API 的价格抽查一页（`docs.x.com/x-api/getting-started/pricing`，2026-11-22）：确为无订阅的按量计费，Posts 读取 `$0.005`、User 读取 `$0.010`、Owned Reads `$0.001`，与第 3.1 节一致。A 写计划时对 X 产品、接口与媒体域名 0 次请求。范围按 [Q-024](https://github.com/chialecode/trace/issues/21) 的推荐默认值定稿，计划见[当前计划](https://github.com/chialecode/trace/blob/f8af16863dbe885cfd63de048730c07a2b91e7be/docs/delivery/stage-plan.md)，需求见[需求 §2.12](../requirements.md#x-scope)。

**数据结构版本（A 的代码核对）**：当前 `main @ b75cf80` 读 `subscriptions.json`（v3）时 `normalizeSubscriptionAccountForLoad` 经 `site.ParseID` 丢掉不认识站点的账号，之后任何一次保存都会把它从文件里抹掉；`uistate` 把不认识的 `FeedFilter.Site` 改回 `all`、`HomePositionSites` 只认四个键。与接入微博时发现的问题同类，所以 X 也要升版本（第 15.1 节第 14 条）。

<a id="a-review-choices"></a>
### 15.1 实现采用的取舍

1. **路线**：只做网页协议（粘贴 Cookie，方案 A），不实现官方 API，也不做同会话混用（Q-024 第 1 条）。理由：官方 API 没有免费档，个人归档的费用持续发生；桌面开源应用不能分发作者密钥，要求用户自建开发者 App 又与 `Restricted Use Cases` 的明文冲突；`access_token` 2 小时有效与 DEC-062 远端不续期冲突；还缺线程、媒体页、直播与 Spaces。`internal/site/x` 内部把“前端元数据与请求头”“GraphQL 传输”“解析”分开，以后要加官方 API 只换传输与解析，不动跨包接口。
2. **按站点分支要逐个补齐**（同微博 §13.1 第 1 条）：凡是 `switch` 站点或“空值按哔哩哔哩 / 未知返回 0”的地方，都给 X 明确的值；未知站点一律报错。表驱动测试证明 X 拿到自己的额度、每轮上限、间隔、退避行、显示名、作者目录、凭证文件，且开播巡检排除 X。
3. **站点标识**：`site.X = "x"`，显示名 `X`；`All()` 顺序哔哩哔哩、抖音、微博、X。`NormalizeUID(X)` 接受 1～20 位正十进制、去前导零、不超过 `uint64`；账号键 `x:<rest_id>`。设置与登录页第一次出现处写“X（原 Twitter）”。
4. **前端元数据运行时发现（改动第 5.1 节的“内置基线常量”）**：`queryId`、`features`/`fieldToggles` 的键、公开网页 Bearer 令牌、`x-client-transaction-id` 的密钥材料都在运行时从前端取得，**仓库里不放这些值的字面量**（它们 1～2 周就变，硬编码兜底 `queryId` 过期后会立即触发 404/400 漂移重刷；公开 Bearer 也不进仓库，避免被密文扫描误判）。做法：某次需要时依次取 `x.com` 首页 HTML（带 Cookie，与浏览器一致）、`abs.twimg.com` 上的 `main.*.js` 与 `ondemand.s.*.js`（**不带 Cookie**，只允许这一主机与 `/responsive-web/` 路径，单文件上限 8 MiB），解析 5 项核心操作后只放内存；可选“我的 › X”操作（`Bookmarks`、`Likes`）从 `main.*.js` 拆出时，按需在单飞与 10 分钟冷却保护下拉取懒加载分包（通用兜底候选 ≤ 2 个，专属命名的 `bundle.Bookmark(s)` / `loader.*` / 超长 `shared~*`、Vite `__vite__mapDeps`、`main.*.js` 路由表与 `Promise.all([n.e(...)])` 并列数字分包及入口分包二跳子分包合计 ≤ 4 个），解析兼容对象字面量、Webpack 5 Harmony `n.d(...)`、CommonJS 属性赋值、模块内提升标识符与 `BookmarkTimeline` / `BookmarksTimeline` 别名，且不因可选分包缺失触发整站熔断。失败或站点信号（空体 404、`400 features cannot be null`、`Query: Unspecified`）触发的重新发现每 10 分钟最多 1 次（遇到 `features cannot be null` 时用 `__INITIAL_STATE__` / `main.*.js` 里的开关默认值补齐缺失键后再重放一次），仍失败报 `schema_changed` 并停对应请求。静态分包请求全部计入 X 的串行闸与每小时额度。合成夹具覆盖 webpack 与 Vite（`x-web`）两种产物形态；真实形态由 V-04 确认。
5. **`x-client-transaction-id`**：按第 10.3 节第 1 部分实现（纯标准库，可注入时钟与随机字节做黄金向量），默认对全部 GraphQL 请求携带（与浏览器一致）。**`x-xp-forwarded-for` 不在本阶段实现**：算法需要 X 前端 WASM 里的基础密钥，调研没有给出且不应从仓库外抄写；本阶段用到的操作（`UserByScreenName`、`UserByRestId`、`UserTweets`、`TweetDetail`、`Following`、`Bookmarks`、`Likes`）都不在调研记录的强制范围里。V-04 若证实这些操作要求 XPFF，相应功能按 `schema_changed` 停下并登记 S 项，不绕过。
6. **登录确认**：本地先查 `auth_token` 与 `ct0` 都非空（不做首页预热取 `ct0`），再按第 2.4 节两步：`GET https://api.x.com/1.1/account/settings.json`（不跟随跳转）要求 HTTP 200、无 `errors`、`screen_name` 合法；再 `UserByScreenName` 取 `rest_id`、昵称、头像，核对 `screen_name` 大小写无关一致；带 `twid` 时核对解码出的 `rest_id`，不一致按“无法确认”处理、不保存、不改已有凭证。不带 Cookie 与只带访客字段的夹具都必须判未登录。`Cookie` 白名单 `auth_token`、`ct0`、`twid`、`guest_id`、`kdt`。
7. **`ct0`**：只被动采纳响应里合法的 `Set-Cookie: ct0`（同步保存与 `x-csrf-token`）；收到 `353` 且带新 `ct0` 时只重放这一步一次，否则判 `auth_required`；绝不自造 `ct0`。
8. **只用三个读取操作族**：用户（`UserByScreenName`、`UserByRestId`）、列表（`UserTweets`、`Following`、`Bookmarks`、`Likes`）、详情（`TweetDetail`，详情页、缓存与过期地址重取都用它）。不用 `TweetResultByRestId`、`UserTweetsAndReplies`、`UserMedia`、`SearchTimeline`、列表与书签文件夹接口（少一个操作就少一处漂移）。
9. **条目键与目录**：所有帖子 `x:dynamic:<status_id>`；目录 `x/<rest_id>/posts/<status_id>/`，转帖与引用帖存 `x/<rest_id>/forwards/<外层 status_id>/` 并记 `via = {reason: "forward", site: "x", uid: <转帖者>}`（DEC-056，复用微博的 `IsForward` / `Origin*` / `OriginUnavailable*` 契约）。文件：只有一个视频的帖子存 `video.mp4` 与 `cover.jpg`；图片 `images/NN.<实际扩展名>`；混排里的视频与 GIF 存 `images/NN.mp4`（角色 `video`，GIF 在清单里标 `gif: true`，播放时静音循环）及封面。
10. **规则类型**：纯转帖与引用帖是 `forward`；唯一媒体是一个视频（不含 GIF）的是 `video`；有图片或 GIF（含混排）的是 `draw`；其余是 `word`（投票、卡片、文章只有摘要）。
11. **回复与线程**：列表、检查更新与全部缓存只看目标作者的根帖、转帖与引用帖（`in_reply_to_status_id_str` 为空的外层条目）；回复他人的帖子不列、不缓存。作者自回复线程在缓存根帖时由 `TweetDetail` 一并保存进同一条目（缓存那一刻的线程），之后追加的续帖不另建条目（LOOP）。对话模块里非目标作者的帖子剔除。
12. **检查更新与全部缓存**：没有起点时只读第 1 页，`TimelinePinEntry` 的置顶 ID 写 `account_baseline_pinned`；之后每个账号每次最多 2 页；用外层 `rest_id`（按无符号 64 位整数比较）做水位线，置顶不触发“到起点就停”；推广与推荐卡剔除；第 7.3 节真值表照用（`{"data":{"user":{}}}` 等不确定空结果不移起点）。全部缓存沿 `UserTweets` 只补原创（不含转帖与引用帖），到时间线可见上限正常结束并说明“已到达 X 时间线可见上限”；不用 `SearchTimeline`。作品数取 `statuses_count`，文案写“帖子 N”。
13. **请求节奏**：采用第 10.4 节的建议值，写成 [NFR-NET-005](../requirements.md#nfr)；另加 `x-rate-limit-remaining <= 1` 时等到 `reset` 加 0～60 秒抖动再请求该操作（手动操作提示“X 限流，约 N 分钟后再试”而不是排队等）。
14. **数据结构版本**：`subscriptions.json` 与 `ui-state.json` 升到版本 4（留 `.premigrate-3`），`cache-queue.json` 升到版本 3（留 `.premigrate-2`）；旧版程序遇到新版本拒绝写入（已有守卫）。`library.db`、`updates.db`、`history.json`、`settings.json` 由 B 核对旧版对 `site = "x"` 的行为后决定：只有证实旧版会静默丢弃并回写时才升。
15. **媒体**：白名单 `pbs.twimg.com`、`video.twimg.com`（`DefaultXMediaHosts()`），不带 Cookie 与 `Referer`，`PublicRedirects` 默认关；图片取 `?format=<ext>&name=orig`；视频取 `video/mp4` 最高码率单文件，经 `mp4ff` 校验与 `moov` 前置；**只有 HLS 的视频本阶段不缓存**，清单记缺失并说明“这条视频只有分段流，暂不支持缓存”（LOOP）。`t.co` 不带 Cookie、≤ 5 跳、每跳 10 秒、最终主机只能是 X 域名。第三方嵌入镜像只在本机改写为 `x.com`，0 次请求。
16. **我的 › X**：关注（含批量导入订阅）、书签、喜欢（仅本人）；书签文件夹与列表各有一个默认关闭的开关常量，V-08 证实非 Premium 账号表现后再评估。
17. **直播与 Spaces**：不巡检、不录制、不查状态；链接与帖内卡片提示“X 直播与 Spaces 暂不支持录制，可以在浏览器中打开”，0 次请求。
18. **远程版**：可以同步 X 订阅、推送 X Cookie，但 X 的 Cookie **不随登录或重新登录自动推送**，只在“设置 › 远程”里阅读机房 IP 风险提示并确认后手动推送（Q-024 第 10 条）；远端照常不续期、不做全部缓存。
19. **界面披露**：登录页与“设置 › 账号”的 X 一栏照第 12.3 节写中立的事实说明（不是官方 API、X 条款限制自动化访问、可能限流、验证、锁定或封禁，建议用专用账号、只读、不存密码、不续期），不阻止用户粘贴自己的账号。
20. **密文扫描**：`tools/checkdocs`、`internal/sitecheck` 与冒烟哨兵加 X 模式（`auth_token=`、`ct0=`、`kdt=` 后跟像真值的十六进制长串，`x-csrf-token` 头，`Authorization: Bearer` 后跟长串）；本文里 `auth_token=<合成占位>` 这样的占位不能误报。

<a id="a-review-verify"></a>
### 15.2 V-01～V-12 核对状态

2026-11-25 在独立克隆与持久账本（`.local/site-check/usage-act025.json`，`runs=1/1`）下执行 **1 次** `go run ./tools/actcheck -act act025`（总请求 `22/32`、前端静态 `5` 次共 `1,638,822 B`、`t.co` `1` 跳 `227 B`、媒体 CDN `3` 次共 `5,948,329 B`、全程串行间隔 `>= 5 s` + 随机抖动、耗时 `121 s`、零限流/零风控/零熔断 `stoppedBy=""`、`tempDataDeleted=true`、`secretScanClean=true`，脱敏回执 `artifacts/site-check/act025.json`）。线上观测到的 15 分钟窗限流头实值：`UserTweets` `50`（剩 `45`）、`AccountSettings` `100`（剩 `94`）、`UserByScreenName` `150`（剩 `147`）、`TweetDetail` `300`（剩 `295`）、`Following` `500`（剩 `498`）。总体 `passed=false`（因步骤 6 `v08_mine_lists` 的可选操作 `Bookmarks` 报 `schema_changed`），其余核心链路与正式视频缓存均通过。

| 编号 | 状态 | 说明 |
| --- | --- | --- |
| V-01 | 通过（2026-11-25 ACT-025；无 Cookie 对照行为有线上细节差异） | 2026-11-22 预检（`artifacts/site-check/act024-login.json`）与 2026-11-23 将端点改为 `api.x.com/1.1/account/settings.json` 后，ACT-025 正式两步登录（`api.x.com/1.1/account/settings.json` + `UserByScreenName` + `twid` 交叉核对）在仅含 `auth_token`/`ct0`/`twid`/`guest_id`（**无 `kdt`**）的清洗 Cookie 下一次通过（`loggedIn=true`，`rest_id`/`screen_name`/`name`/`avatar` 均齐备）。**无 Cookie 对照细节**：带公开 Bearer 与 `x-client-transaction-id` 但不带 Cookie/CSRF 调 `api.x.com/1.1/account/settings.json` 时，线上返回 **HTTP `404` + 错误码 `34`**（非预期 `401` code `32`/`89`，客户端将其归类为 `unavailable` 而非 `auth_required`），虽未误判登录，但后续可评估将无 Cookie 对照或登录确认中的 `404 code 34` 归类细节补齐 |
| V-02 | 部分核对 / 已记录（2026-11-25 ACT-025） | 本次 2 分钟运行内正常请求未观察到服务端被动轮换 `ct0`（`passiveCT0RotatedOnNormalCalls=false`）。受控 `x-csrf-token` 错配探针调 `api.x.com/1.1/account/settings.json`（注：该探针未附带 `x-client-transaction-id`）返回 **HTTP `404` + 错误码 `34`**（`mismatchGot353=false`，未下发 `Set-Cookie: ct0`），未能在线上触发 `403 code 353` 恢复路径；生产代码继续保持“绝不自造 `ct0`、仅被动采纳响应 `Set-Cookie: ct0`、仅在带新 `ct0` 的 `353` 时重放一次”的保守策略 |
| V-03 | 部分通过（2026-11-25 ACT-025） | ① **未登录 GraphQL 对照通过**：无 Cookie、无 `x-csrf-token`、无 `x-twitter-auth-type`（仅带公开 Bearer 与 `txid`）调 `TweetDetail` 被线上返回 **HTTP `403`** 拒绝（`unauthRejected=true`，归类 `risk_control`），实证无访客产品模式依据成立；② **`t.co` 无 Cookie 短链探针发现线上差异**：从帖子实体提取的 `t.co` 短链无 Cookie 请求 `1` 跳（`227 B`）返回 **HTTP `200`** 而非 `3xx Location` 跳转（实测 `t.co` 对桌面 UA 可返回短小 HTML 刷新跳转页），当前只认 `3xx Location` 的展开器报 `unavailable`（`shortLinkResolved=false`），待后续另开小 PR 评估是否兼容安全 HTML `meta refresh` 跳转解析 |
| V-04 | 核心通过 / `Bookmarks` 懒加载分片未命中（2026-11-25 ACT-025） | 2026-11-22 ACT-024 仅跑通前 2 次静态请求即因 `Bookmarks` 拆包中止（详见 `artifacts/site-check/act024.json` 与 [#59](https://github.com/chialecode/trace/issues/59) 修复）。ACT-025 实证线上仍为 **webpack** 产物：`x.com/home` HTTP `200`（`317,392 B`，含 `twitter-site-verification` 与 `loading-x-anim` SVG），无 Cookie 拉取 `abs.twimg.com` 的 `main.*.js`（`1,246,356 B`）、`ondemand.s.*.js`（`17,997 B`）及 `2` 个懒加载分包（`57,077 B`，触及 ACT-025 `frontend=5` 预算上限），成功提取公开网页 Bearer、初始化 `x-client-transaction-id`（全量 GraphQL 请求均已携带），并提取全部 5 项核心操作（`UserByScreenName`、`UserByRestId`、`UserTweets`、`TweetDetail`、`Following`）与 `Likes`（合计 `6` 项），全程 **`xpffHeaderSent=false`（无需 XPFF）** 即通过核心接口；但 `Bookmarks` 与 `BookmarkFoldersSlice`（`bookmarkFoldersOpInBundle=false`）在已拉取的 `2` 个懒加载分包中仍未找到，触发步骤 6 `Bookmarks` 报 `schema_changed`（待后续排查是否被 `frontend=5` 静态额度截断或分片命名/导出形态仍未覆盖） |
| V-05 | 通过（2026-11-25 ACT-025，覆盖 3/5 目标） | 在单轮 `postDetail <= 3` 预算上限下成功拉取并解析 3/3 条公开帖（`TweetDetail` HTTP `200` × 3）：① 视频帖（`ruleType=video`，1 个视频，非 HLS-only）；② 引用原帖已删/不可见帖（`isQuote=true`、`ruleType=forward`、`originUnavailable=true`、`hasOriginUnavailableReason=true`，降级正常）；③ 长帖 + 双图 + `t.co` 实体帖（`isLongText=true` 证实 `note_tweet` 展开、`280` 字符、`2` 张图、`4` 个 URL 实体）。`thread`、`mixed`、`gif`、`poll`、`card` 受 3 帖预算上限与目标配置所限本次未覆盖 |
| V-06 | 通过（2026-11-25 ACT-025；发现 `statuses_count`/`pinned_tweet_ids_str` 解析为 0） | `UserByScreenName` + `UserTweets` 两页翻页全部 HTTP `200` 通过：第 1 页 `20` 条（置顶 `1` 条排在最前 `page1PinnedLeading=true`，非置顶外层雪花 ID 单调递减 `page1OuterIDsMonotonicNonPinned=true`，含 `16` 图文/`7` 转帖/`3` 引用，非空壳 `page1UncertainEmpty=false`），第 2 页 `19` 条（游标推进、无停滞、跨页 `0` 重复）。`UserMedia` 按第 15.1 节第 8 条未调用。**待跟进现象**：目标活跃账号实有置顶且至少 `39` 帖，但资料解析结果 `profileStatusesCount=0`、`profilePinnedCount=0`，疑为线上 `UserByScreenName` 中 `statuses_count`/`pinned_tweet_ids_str` 节点变动或缺失，可能影响空壳真值表与“帖子 N”显示，需另行排查 |
| V-07 | 通过（2026-11-25 ACT-025） | 无 Cookie、无 Referer 拉取 `*.twimg.com` 全部通过（`cdnCookieless=true`、`cdnNoReferer=true`、`range206=true`，合计 `3` 请求 `5,948,329 B`）：① 原图探针 `?name=orig` HTTP `200`（JPEG，`875,818 B`，`1080×985`）；② 正式 `CacheXPost` 视频缓存完成（`taskStatus=completed`、`itemStatus=complete`、`indexed=true`、`missingCount=0`、封面 + 视频共 2 文件，MP4 `4,690,252 B`，`avc1` + `mp4a`，`1900×3378`，`4.249 s`，`moovBeforeMdat=true`，`mp4ffValid=true`，非 HLS-only），临时目录密文扫描清零（`tempCacheSecretScanClean=true`） |
| V-08 | 失败 / 部分实证（2026-11-25 ACT-025） | `Following` 实际完成 **2 页**请求并返回 HTTP `200`（`operationCounts.Following=2`、`httpStatusCounts["mine:200"]=2`，限流余量 `500→498`）；随后 `ListBookmarks` 因未在已拉取的 2 个懒加载分包中找到 `Bookmarks` 的 `queryId` 报 `schema_changed` 提前返回，导致回执未写入 `V-08` 子对象且 `ListLikes`（尽管 `Likes` `queryId` 已在 V-04 提取）与 `BookmarkFoldersSlice`（未在包内发现）均未执行。默认开关（书签文件夹与列表）继续保持关闭 |
| V-09 | `not-run`（2026-11-25 ACT-025） | `.local/site-check/targets-x.json` 未配置 `protectedUsers` 与 `missing` 目标，步骤 5 按设计跳过（`0` 次请求） |
| V-10 | 不在本阶段 | 不用 `SearchTimeline`（第 15.1 节第 12 条） |
| V-11 | 不在本阶段 | 直播与 Spaces 延后（第 15.1 节第 17 条） |
| V-12 | 不在本阶段 | 不走官方 API（第 15.1 节第 1 条） |
