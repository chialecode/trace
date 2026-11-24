# 微博网页协议资料

核对日期：2026-11-21（静态协议与选型调研定稿，**对微博与新浪全部域名发出的真实网络请求为 0 次**，未读取或使用任何本机 Cookie、浏览器登录数据或真实账号信息）。内容整理自公开标准文档、公开社区协议资料、Go 官方模块代理 / GitHub 元数据，以及本机参考索引相关功能领域只读源码观察到的协议现象；站点端点不是微博对 TRACE 的稳定承诺，实施与真实站点检查前必须按[第 11 节](#verify-list)做有界实测核对。规则见[站点规则](../dev-rules/sources-and-safety.md#site-rules)与[凭证与真实站点请求](../dev-rules/sources-and-safety.md#credentials)。

证据等级说明（全篇逐节标注查询或分析日期）：

- `官方文档`：公开标准规范（如 RFC 6265）、Go 官方模块代理 / GitHub 仓库元数据与许可证声明。
- `源码阅读（功能领域，2026-11-21）`：本机参考索引对应功能领域（多站点订阅参考、单链接解析参考、直播录制参考）与本仓库代码只读观察到的协议构造、响应字段或接入边界（严格只读事实，未复制任何代码，符合 [DEC-044](../decisions.md#dec-044)）。
- `社区资料（2026-11-21）`：公开技术社区与协议整理资料的观察记录。
- `推测（2026-11-21）`：基于现有协议结构与 TRACE 架构约束作出的工程推断。
- `unknown`：静态调研无法锁定或社区资料存在分歧、必须依赖授权后的有界真实站点检查才能确认的细节（统一汇总至[第 11 节](#verify-list)）。

本文全部示例 ID、Cookie 片段、令牌与链接均为**明显合成示例**（如用户 `uid=1000000001`、微博 `mid=5000000000000001` 与对应 `mblogid=O00000001`、视频 `fid=1034:5000000000000001`、直播 `live_id=1022:2321325000000000000001`、图片 `pic_id=001SynThPic01ly1synthetic00001`、分组 `list_id=4000000000000001`、短链 `https://t.cn/A6SYNTHETIC`、凭证占位 `SUB=<合成占位>` / `XSRF-TOKEN=<合成占位>`），不包含任何真实账号、真实凭证或真实签名地址。

<a id="status"></a>
## 1. 验证状态

截至 2026-11-21，本资料为**纯静态调研成果**：

- **真实站点请求**：`0` 次（严格遵守 [M6 阶段计划修订 0](https://github.com/chialecode/trace/blob/b75cf80/docs/delivery/stage-plan.md#research) 红线：未向 `weibo.com`、`weibo.cn`、`m.weibo.cn`、`sina.com.cn`、`sinaimg.cn`、`weibocdn.com`、`t.cn` 等任何微博与新浪域名发过请求，未做浏览器访问）。
- **静态已确认事实（仍待真实验证锁定线上现行行为）**：
  - `mid <-> mblogid` 7 位十进制 / 4 字符 Base62 分段互转算法与 8 组确定性合成测试向量（已在本地纯数学脚本完成双向自检，零网络请求，见[§3.2](#base62)）。
  - PC Ajax 主协议栈（`weibo.com/ajax/*`）相比移动 H5（`m.weibo.cn`）在超过 9 图、图文视频混排 `mix_media_info`、多档视频 `playback_list`、单域会话一致性上的结构优势与核心字段路径（见[§4](#post) 与[§6](#user-feed)）。
  - TRACE 现有代码接入面（`internal/site`、`internal/credential`、`internal/download`、`internal/subscription`、`internal/live`、`cmd/trace-server`）的跨站接口、无 Cookie 媒体白名单、独立额度/退避/熔断边界（`源码阅读（仓库代码，2026-11-21）`）。
  - Go 开源生态中受维护、许可合规、可直接导入的第三方微博客户端/扫码/签名库为 **0 个**，微博 MVP 无需新增 Go 依赖（见[§10](#oss)）。
- **核心 `unknown` 汇总**：共整理为 **14 项 MVP 核心必验项（`V-01`～`V-14`）** 与 **5 项条件验证项（`V-15`～`V-19`，仅在纳入扫码或直播时执行）**，覆盖无 Cookie 严格登录对照、Cookie 最小子集与跨域通用性、`t.cn` 跳转、`/n/` 与个性域名换 `uid`、长微博内联、去水印路径、GIF/实况 `.mov` 容器、视频无 Cookie vs 登录画质、不存在/不可见错误码与 `visible.type` 枚举、`mymblog` 分页/快转/半年可见、CDN `Range` 与 302 域、以及“我的”关注分组/收藏/赞过，详见[第 11 节](#verify-list)。
- **产品原则基线**：延续 [DEC-065](../decisions.md#dec-065)（微博作为第三个站点，先调研后定范围）、[DEC-043](../decisions.md#dec-043)（每站点独立登录与凭证、按站点先登录再发业务请求、无访客产品模式、额度/退避/熔断独立、小字站点标签）、[DEC-044](../decisions.md#dec-044)（只按本文规格洁净室独立实现，不翻译参考代码）、[DEC-047](../decisions.md#dec-047)（“我的”严格只读且仅驻留内存）、[DEC-056](../decisions.md#dec-056)（转发内容存转发者 `forwards/` 目录并记录 `via`）与 [DEC-062](../decisions.md#dec-062)（`trace-server` 无 GUI 依赖、只同步订阅与 Cookie、不做远端续期与全量缓存）。

<a id="auth"></a>
## 2. 登录与凭证（R-A）

<a id="qr-login"></a>
### 2.1 网页扫码登录完整流程（SSO QR Login）

微博网页端扫码登录基于新浪/微博统一通行证 SSO 体系，社区实现存在两代入口（传统 `login.sina.com.cn/sso/qrcode/*` 与新版 `passport.weibo.com/sso/v2/qrcode/*`），状态机与票据换 Cookie 的跨域写域流程一致（`社区资料（2026-11-21）`；两代端点在 2026-11 线上可用性、必选参数与精确返回细节为 `unknown`，若纳入扫码须经 **V-15** 与人工实机验证锁定）：

1. **取码（生成二维码）**：
   - 传统端点：`GET https://login.sina.com.cn/sso/qrcode/image?entry=weibo&size=180&callback=<cb>`（`entry` 亦见 `miniblog` / `sinawap`）（`社区资料（2026-11-21）`）。
   - 新版端点：`GET https://passport.weibo.com/sso/v2/qrcode/image?entry=miniblog&size=180&callback=<cb>`（`社区资料（2026-11-21）`）。
   - 典型请求头：固定桌面 Chrome `User-Agent`、`Referer: https://weibo.com/`（或 `https://passport.weibo.com/`）。
   - 响应（JSONP 包裹 JSON）：`retcode == 20000000`，`data.qrid`（本次扫码会话唯一 ID，如 `<合成会话ID>`）与 `data.image`（二维码图片 URL，常见 `https://v2.qr.weibo.cn/inf/gen?api_key=<合成占位>`；是否同时返回可供 `go-qrcode/v2` 本地绘制的原始扫码 URI 记为 `unknown`，见 **V-15**）。
2. **轮询扫码状态**（建议每 `2` 秒 1 次，前后端共同设总超时如 `180` 秒；会话取消或过期后晚到响应一律丢弃，绝不写入凭证）：
   - 传统端点：`GET https://login.sina.com.cn/sso/qrcode/check?entry=weibo&qrid=<合成会话ID>&callback=<cb>`
   - 新版端点：`GET https://passport.weibo.com/sso/v2/qrcode/check?entry=miniblog&qrid=<合成会话ID>&callback=<cb>`

| `retcode` | 含义 | 客户端动作 | 证据等级 |
| --- | --- | --- | --- |
| `50114001` | 未扫码（等待扫描） | 继续轮询 | `社区资料（2026-11-21）` |
| `50114002` | 已扫码，等待微博 App 确认 | 界面提示“已扫码，请在微博 App 确认”，继续轮询 | `社区资料（2026-11-21）` |
| `20000000` | 已确认，响应携带 `data.alt` 临时登录票据 | 停止轮询，进入第 3 步跨域换 Cookie | `社区资料（2026-11-21）` |
| `50114004` | 二维码已过期 | 停止轮询，提示刷新二维码 | `社区资料（2026-11-21）` |
| `50114003` / `50114015` | 社区报告用于取消授权/会话中断或异常 | 停止轮询，提示重新获取二维码；“手机端点击取消”的精确 `retcode` 记为 `unknown`（留待人工实机核实） | `社区资料（2026-11-21）` / `unknown` |

3. **确认后的跨域写 Cookie 跳转链（SSO Cross-Domain Chain）**：
   - **换票与取跨域列表**：`GET https://login.sina.com.cn/sso/login.php?entry=weibo&returntype=TEXT&crossdomain=1&cdult=3&domain=weibo.com&alt=<合成票据>&savestate=30&callback=<cb>`（新版对应 `passport.weibo.com/sso/v2/login` 变体，确切参数待实测核对）（`社区资料（2026-11-21）`）。
   - 响应返回 `retcode == "0"`（兼容整型 `0`）、`uid`、`nick` 与 **`crossDomainUrlList`**（部分回调形式为 `arrURL` 数组）（`社区资料（2026-11-21）`）。
   - **逐个访问 `crossDomainUrlList` 写入各域 Cookie**（每条 URL 必须校验为 HTTPS 443 端口且主机严格属于允许的通行证域白名单 `passport.weibo.com`、`login.sina.com.cn`、`passport.weibo.cn`、`weibo.com`，严禁向任意外域发请求）：
     - `https://passport.weibo.com/wbsso/login?ssosavestate=<合成占位>&ticket=<合成票据>&retcode=0` → 在 `.weibo.com` 下通过 `Set-Cookie` 写入 `SUB`、`SUBP`、`SSOLoginState`、`ALF`、`SUHB` 等（`社区资料（2026-11-21）`）。
     - `https://login.sina.com.cn/sso/crossdomain?...` → 在 `.sina.com.cn` 下写通行证 Cookie（`社区资料（2026-11-21）`）。
     - `https://passport.weibo.cn/sso/crossdomain?...`（若列表包含）→ 在 `.weibo.cn` 下写移动 H5 域 Cookie（当前线上 `crossDomainUrlList` 是否必然包含 `.weibo.cn` 记为 `unknown`）（`社区资料（2026-11-21）`）。
   - 跳转链完成后，调用[§2.4](#login-verify)的严格登录校验接口确认 `uid` 一致并同步拿到当前 `XSRF-TOKEN`（`推测（2026-11-21）`）。
   - **MVP 取舍提示**：鉴于扫码登录涉及两代 SSO 端点与多域跳转链，对照抖音先例（[DEC-043](../decisions.md#dec-043)），推荐微博 MVP 先采用**粘贴 Cookie 登录**，扫码登录放进后续 loop（见[§12.3](#q-drafts) `Q-WB-1`）。

<a id="cookie-clean"></a>
### 2.2 粘贴 Cookie 字段分类、清洗与脱敏规则

按 TRACE 凭证隔离设计（[架构设计 §5](../design/architecture.md#credentials)），用户在登录页或“设置 › 账号”粘贴浏览器已登录 `weibo.com` 的 `Cookie` 字符串；后端严格清洗、经[§2.4](#login-verify)服务端校验有效后，用 Windows DPAPI（Linux `trace-server` 用 AES-256-GCM + `secret.key`）按独立文件 **`weibo.bin`** 与独立熵 **`trace:weibo:credential:v1`** 加密落盘（`源码阅读（仓库代码，2026-11-21）`）。

| 分类 | Cookie 字段名 | 作用与处理策略 | 证据等级 |
| --- | --- | --- | --- |
| **核心身份态（本地预检必需）** | `SUB` | 微博主身份凭证。**本地预检必须存在非空 `SUB`**，缺失时直接本地返回 `invalid_input`（0 网络请求）。**防误判红线**：新浪访客体系同样下发名为 `SUB` 的 Cookie，且前缀（如 `_2Ak` vs `_2A25`）存在跨版本变化，**绝不能仅凭存在 `SUB` 或前缀正则判定已登录**，必须通过 [§2.4](#login-verify) 服务端严格校验 | `源码阅读（单链接解析参考，2026-11-21）`、`社区资料（2026-11-21）` |
| **核心配套态（强烈建议保留）** | `SUBP`、`ALF`、`SCF`、`WBPSESS`、`XSRF-TOKEN`、`SSOLoginState`、`SUHB`、`PC_TOKEN` | `SUBP` 配合 `SUB` 做通行证与子域校验；`ALF` 记录记住登录到期 Unix 秒；`SCF` 为安全风控标识；`WBPSESS` 维持 PC 网页会话；`XSRF-TOKEN` 镜像至 `X-XSRF-TOKEN` 请求头；`SSOLoginState` 标记 SSO 状态（注：社区偶有报告旧或冲突的 `SSOLoginState` 在 `m.weibo.cn` 触发 `432`，保留与剥离的实际影响列入 **V-01**） | `社区资料（2026-11-21）` |
| **移动端兼容字段（若存在则保留）** | `MLOGIN`（常为 `1`）、`_T_WM` | 移动 H5 域会话标识；从桌面 `weibo.com` 复制时通常不存在 | `社区资料（2026-11-21）` |
| **可安全剥离的动态/统计/界面字段** | `SINAGLOBAL`、`UOR`、`_s_tentry`、`Apache`、`ULV`、`WBStorage`、`wb_view_log_*`、`webim_unReadCount`、`login_sid_t`、`cross_origin_proto`、`WEIBOCN_FROM`、`M_WEIBOCN_PARAMS`、`ariaDefaultTheme`、`ariaFixed`、`ariaStatus`、`s`、`wvr`、`un` | 浏览轨迹、广告归因、未读红点、无障碍主题等纯前端统计 Cookie，剥离可减小请求头体积、避免超出 `8192` 字节上限并减少无用指纹噪声（精确最小可用子集由 **V-01** 确认） | `社区资料（2026-11-21）`、`推测（2026-11-21）` |

**清洗与脱敏规则**（对齐 `internal/credential/credential.go` 与 `douyin.go` 模式，`源码阅读（仓库代码，2026-11-21）`）：

1. 输入超过 `MaxCookieInputBytes = 8192` 字节直接返回 `ErrInputTooLarge`；拒绝包含 `\r`、`\n` 或其他控制字符（防 HTTP 头注入），错误文案绝不回显原始输入。
2. 支持用户整行复制时自动剥离前缀 `Cookie:`；按 `;`（及换行兼容）拆分为 `name=value`（完整保留值中可能出现的 `=` 号，如 Base64 填充），同名键保留最后一个非空值，严格区分大小写。
3. 采用**显式白名单保留**（仅保留上表前三类键名），剔除全部统计与界面字段；校验至少存在非空 `SUB`（否则返回专用 `ErrMissingWeiboSub`）。
4. **全局脱敏红线**：`internal/credential/manager.go` 的 `RedactString` 必须注册 `weibo` 凭证库，将 `SUB`、`SUBP`、`SCF`、`ALF`、`ALC`、`WBPSESS`、`XSRF-TOKEN`、`SSOLoginState`、`SUHB`、`PC_TOKEN`、`_T_WM` 及扫码过程中的 `st`、`alt`、`ticket` 全部纳入掩码清单（含 URL 编解码变体），避免任何日志、错误链或诊断输出泄露凭证。

<a id="ttl-renewal"></a>
### 2.3 会话有效期与续期策略

- **会话有效期**：`ALF` 声明的“记住登录”上限常见为 `30 天`至 `1 年`，但 `SUB` 实际可用期受账号活跃度、IP/环境变化、修改密码、原浏览器登出或风控吊销影响，社区观察常规可维持**数天至数周/数月**（`社区资料（2026-11-21）`；精确无操作失效阈值记为 `unknown`）。
- **被动刷新与主动续期**：浏览器正常访问主站时，服务端偶尔通过 `Set-Cookie` 被动刷新 `XSRF-TOKEN` 或 `SUB`（`社区资料（2026-11-21）`）；但是否存在不重新扫码/输密码即可稳定主动续签 `SUB` 的接口记为 `unknown`。
- **TRACE 策略（对齐 [DEC-031](../decisions.md#dec-031) 与 [DEC-043](../decisions.md#dec-043)）**：粘贴的 Cookie 与用户浏览器共享同一会话，**TRACE 不对粘贴 Cookie 做主动续期或轮换**（避免顶掉浏览器登录或触发异地风控）；仅在响应头下发新 `XSRF-TOKEN` 时通过 `MergeContextCookies` 同步上下文令牌以保持防 CSRF 有效；一旦核心 `SUB` 失效即标记 `expired` / `auth_required` 并提示用户重新粘贴（`推测（2026-11-21）`）。远程版 `trace-server` 按 [DEC-062](../decisions.md#dec-062) 同样从不续期，只接收本地推送。

<a id="login-verify"></a>
### 2.4 严格确认“确实已登录”（含不带 Cookie / 访客对照）

遵守[站点规则](../dev-rules/sources-and-safety.md#site-rules)：**HTTP 200、存在 `SUB` Cookie 或拿到访客身份，都不证明已登录**。必须规避四类常见误判陷阱：

1. **`HTTP 200` 假成功**：`m.weibo.cn/api/config` 未登录时也返回 `HTTP 200` 与 `ok: 1`（但 `data.login: false`）；PC 端未登录有时返回 `HTTP 200` 的 `Sina Visitor System` HTML 跳转页（`源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）`）。
2. **`SUB` 存在不等于已登录**：访客体系同样种下同名 `SUB` / `SUBP`，且前缀规则不构成服务端认证保证（`源码阅读（单链接解析参考，2026-11-21）`、`社区资料（2026-11-21）`）。
3. **查公开他人的 `profile/info?uid=<id>` 不能证明登录**：访客态对部分公开资料也能返回 `ok: 1`，而不传 `uid` 时新版 PC 接口直接返回 `HTTP 400` 或 `ok: 0`（`社区资料（2026-11-21）`）。
4. **抓首页 HTML `window.$CONFIG` 脆弱且重**：易撞访客页且体积大，不作常规校验正本（`推测（2026-11-21）`）。

| 候选校验接口 | 不带 Cookie / 访客态表现 | 带有效登录 Cookie 表现 | 能否作校验正本 | 证据等级 |
| --- | --- | --- | --- | --- |
| **候选 1（PC 同域主选）**：`GET https://weibo.com/ajax/setting/getBasicInfo` | 预期返回 `ok: -100` 或 `302` 跳转登录/访客页（精确返回码 `unknown`，列入 **V-01**） | 返回 `HTTP 200`、`ok == 1`，`data` 含当前登录账号的 `uid`、`screen_name`、`profile_image_url` | **优先验证正本**：与 PC 主栈同域，一次轻量 JSON 请求同时完成严格鉴权与当前账号摘要读取 | `社区资料（2026-11-21）` / 线上细节 `unknown` |
| **候选 2（移动 H5 强备选）**：`GET https://m.weibo.cn/api/config` | 返回 `HTTP 200`、`ok == 1`，但 **`data.login == false`** 且无有效 `data.uid`（含 `loginUrl`） | 返回 `HTTP 200`、`ok == 1`、**`data.login == true`**、非空正整数 `data.uid` 与 `data.st` | **强备选 / 交叉对照**：若候选 1 不可用，可先由本接口拿到 `uid`，再调 `weibo.com/ajax/profile/info?uid=<uid>` 确认 PC 域接受该 Cookie 并补齐昵称头像（跨域通用性列入 **V-01**） | `源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）` |
| **候选 3（辅助断言）**：`GET https://weibo.com/ajax/feed/allGroups?is_new_segment=1&fetch_hot=1` | 未登录返回 `ok: -100` 或 `302` 跳转 | 返回 `ok == 1` 与登录账号分组列表 | 仅作辅助（无当前登录用户 `uid`/昵称/头像） | `社区资料（2026-11-21）` |

**TRACE 严格登录确认判定式（缺一不可）**：

1. HTTP 客户端**禁用自动重定向**；响应必须为 `HTTP 200`、合法 JSON 对象，且未命中[第 9 节](#errors)任何访客跳转、验证码或风控特征；
2. 顶层 `ok == 1`；
3. 若走 `weibo.com/ajax/setting/getBasicInfo`：`data.uid` 为正整数串（`^[1-9][0-9]{4,19}$`）且 `data.screen_name` 非空；若走 `m.weibo.cn/api/config`：`data.login === true` 且 `data.uid` 为正整数串，随后再调 `weibo.com/ajax/profile/info?uid=<uid>` 确认 `ok == 1`、`data.user.idstr == uid` 且 `screen_name` 非空；
4. **必须经无 Cookie 对照锁死（V-01）**：确认同一校验函数在不带 Cookie（及仅带访客 Cookie）时稳定返回 `auth_required` 而非误判已登录；若遇风控/限流归类为 `risk_control` / `rate_limited`，绝不清除用户已保存的有效凭证。

<a id="visitor"></a>
### 2.5 访客体系（Sina Visitor System）与未登录可见边界

- **访客 Cookie 获取机制**（仅用于理解站内跳转与防御误判，TRACE 不作为产品登录方式）：
  - 传统两步法（`源码阅读（单链接解析参考，2026-11-21）`）：① `GET https://passport.weibo.com/visitor/genvisitor?cb=<cb>&fp=<指纹JSON>` 取 `data.tid`；② `GET https://passport.weibo.com/visitor/visitor?a=incarnate&t=<tid>&w=2&c=095&gc=&cb=<cb>&from=weibo&_rand=<r>` 返回 `retcode == 20000000` 并 `Set-Cookie` 种下访客版 `SUB` 与 `SUBP`。
  - 新版入口：`POST https://passport.weibo.com/visitor/genvisitor2`（表单含 `cb=visitor_gray_callback&from=weibo&tid=&webdriver=false` 等）后再调 `incarnate`（`社区资料（2026-11-21）`）。
  - 移动 H5 路径：首次无 Cookie 访问 `https://m.weibo.cn/` 触发 `302` → `visitor.passport.weibo.cn` → 完成后跳回种下 `.weibo.cn` 访客 Cookie（`源码阅读（多站点订阅参考，2026-11-21）`）。
- **未登录 / 访客能看 vs 不能看**：
  - **偶可查看（受限且易变）**：部分公开单条微博详情、公开视频组件 `tv/api/component`、公开直播回放信息，以及部分博主在 `m.weibo.cn` 首屏有限条数（`源码阅读（多站点订阅参考、单链接解析参考，2026-11-21）`）。
  - **不可看或严重裁剪**：`weibo.com/ajax/statuses/mymblog` 常直接返回 `ok: -100` 或仅首屏受限无法翻页；开启“仅登录可见”的主页；仅粉丝/好友圈/超可见时间窗的微博；“我的”关注/分组/收藏/赞过；高清晰度视频（1080P+ 通常要求登录）；且访客 Cookie 极易在数次请求后命中 `HTTP 432` 或 `ok: -100`（`源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）`）。
- **TRACE 定位**：遵守 [DEC-018](../decisions.md#dec-018) 与 [DEC-043](../decisions.md#dec-043)，**不做访客产品模式**，微博内容浏览、缓存与订阅均需先登录微博；访客特征仅用于识别登录失效/风控跳转并拒收误贴的访客 Cookie。

<a id="domain-compat"></a>
### 2.6 `weibo.com` 与 `m.weibo.cn` 会话通用性

- **浏览器域隔离**：`.weibo.com` 与 `.weibo.cn` 属于不同 eTLD+1 域，按 RFC 6265 浏览器不会自动共享 Cookie，网页端靠 SSO `crossDomainUrlList` 分别写域（`官方文档`、`社区资料（2026-11-21）`）。
- **手动注入复用性与边界**：社区与参考实现均观察到，将 `weibo.com` 的 `SUB`（配合 `SUBP`）带入 `m.weibo.cn` 请求头时通常可被后端识别为同一登录态，反之亦然（`源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）`）；但两域存在三项显著差异：
  1. 配套令牌独立：PC 用 `XSRF-TOKEN` / `WBPSESS`，H5 用 `st` / `_T_WM` / `MLOGIN`；
  2. 请求头体系不同：H5 要求移动 UA 与 `MWeibo-Pwa: 1`；
  3. 从 `weibo.com` 粘贴的 Cookie（尤其含 PC `SSOLoginState` 时）直接用于 `m.weibo.cn` 是否更易触发 `432` 记为 **`unknown`**（列入 **V-01**）。
- **结论**：TRACE 统一以 **PC `weibo.com/ajax/*` 为唯一生产主栈**，与用户从桌面浏览器复制 `weibo.com` Cookie 天然同域，避免双域会话分裂（`推测（2026-11-21）`）。

<a id="headers"></a>
### 2.7 必需请求头矩阵

| 请求头 | PC Ajax (`weibo.com/ajax/*`) | PC 视频组件 (`weibo.com/tv/api/component`) | 移动 H5 对照 (`m.weibo.cn/*`) | 媒体 CDN (`*.sinaimg.cn` / `*.weibocdn.com`) | 证据等级 |
| --- | --- | --- | --- | --- | --- |
| `User-Agent` | 固定桌面 Chrome UA | 同左 | 固定移动 Safari/Chrome UA | 同 PC 桌面 Chrome UA | `源码阅读（多站点订阅参考、单链接解析参考，2026-11-21）`、`社区资料（2026-11-21）` |
| `Referer` | `https://weibo.com/`（或对应主页/博文路径） | 完整视频页 URL，**另必带 `Page-Referer: /tv/show/<fid>`** | `https://m.weibo.cn/`（或 `/u/<uid>`、`/detail/<mid>`） | **`https://weibo.com/`**（见 [§5.1](#cdn-domains) 防盗链） | `源码阅读（多站点订阅参考、单链接解析参考，2026-11-21）` |
| `Accept` | `application/json, text/plain, */*` | `application/json, text/plain, */*` | `application/json, text/plain, */*` | `*/*` | `源码阅读（单链接解析参考，2026-11-21）` |
| `Accept-Language` | `zh-CN,zh;q=0.9,en;q=0.8` | 同左 | 同左 | 可省略 | `社区资料（2026-11-21）` |
| `X-Requested-With` | `XMLHttpRequest`（建议恒带） | `XMLHttpRequest` | `XMLHttpRequest`（必需） | 不带 | `源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）` |
| `X-XSRF-TOKEN` | 若 Cookie 含 `XSRF-TOKEN` 则**镜像同名值**（POST 必带，GET 是否强校验 `unknown`，列 **V-01**，建议恒带） | 若存在则镜像 | H5 写操作用 `st`（只读 GET 不强求） | 不带 | `社区资料（2026-11-21）` |
| `MWeibo-Pwa` | 不带 | 不带 | `1`（必需） | 不带 | `源码阅读（多站点订阅参考、单链接解析参考，2026-11-21）` |
| `sec-ch-ua*` | 与固定桌面 Chrome UA 版本一致 | 同左 | 匹配移动 UA 或省 | 不带 | `社区资料（2026-11-21）` |
| `Cookie` | **必带**清洗后的登录 Cookie | 同左 | 同左 | **严禁携带登录 Cookie**（`ApplyHeaders` 显式 `Del("Cookie")`） | `源码阅读（仓库代码，2026-11-21）`、`源码阅读（多站点订阅参考，2026-11-21）` |

<a id="links"></a>
## 3. 链接、短链与稳定 ID（R-B）

<a id="url-forms"></a>
### 3.1 全部链接形态与 `t.cn` 短链规则

以下所有 ID、域名示例均为**合成示例**：

| 类别 | URL 形态（合成示例） | 提取的目标标识与说明 | 证据等级 |
| --- | --- | --- | --- |
| **单条微博（PC 主形态）** | `https://weibo.com/1000000001/O00000001`<br>`https://www.weibo.com/1000000001/5000000000000001`<br>`https://weibo.cn/1000000001/O00000001` | 第 1 段为作者 `uid`；第 2 段为 15～20 位纯数字直接作 `mid`，为字母数字 `mblogid`（常见 8～10 位）则按 [§3.2](#base62) 本地换算为 `mid` | `源码阅读（多站点订阅参考、单链接解析参考，2026-11-21）` |
| **单条微博（详情页）** | `https://weibo.com/detail/5000000000000001`<br>`https://weibo.com/detail/O00000001`<br>`https://weibo.com/2/detail/5000000000000001`<br>`https://m.weibo.cn/status/5000000000000001`<br>`https://m.weibo.cn/status/O00000001`<br>`https://m.weibo.cn/detail/5000000000000001`<br>`https://m.weibo.cn/statuses/show?id=5000000000000001` | 从路径末段或 `id` 查询参数提取 `mid`（若是 `mblogid` 则本地换算为 `mid`） | `源码阅读（多站点订阅参考、单链接解析参考，2026-11-21）` |
| **单条微博（移动渠道后缀）** | `https://m.weibo.cn/1000000001/5000000000000001/qq?wm=3333_2001` | 忽略第 3 段渠道标识（如 `qq`/`wx`），第 2 段提取 `mid` 或 `mblogid` | `源码阅读（单链接解析参考，2026-11-21）` |
| **单条微博（App 分享页）** | `https://share.api.weibo.cn/share/100000001,5000000000000001.html`<br>`https://share.api.weibo.cn/share/100000001.html?weibo_id=5000000000000001`<br>`https://weibointl.api.weibo.com/share/100000001.html?weibo_id=5000000000000001` | 优先取查询参数 `weibo_id`（纯数字 `mid`），其次取文件名逗号后数字段（`,<mid>.html`）；若仅含内部 `share_id` 且无 `weibo_id`，现行 App 分享落点列 **V-02** | `社区资料（2026-11-21）` |
| **用户主页（数字 `uid`）** | `https://weibo.com/u/1000000001`<br>`https://www.weibo.com/u/1000000001`<br>`https://m.weibo.cn/u/1000000001`<br>`https://m.weibo.cn/profile/1000000001`<br>`https://weibo.cn/u/1000000001`<br>`https://weibo.com/1000000001`（单段纯数字） | 直接提取稳定数字 `uid = "1000000001"`（兼容 5～19 位正整数，常见 10 位） | `源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）` |
| **用户主页（`/p/` 容器页）** | `https://weibo.com/p/1005051000000001`（个人）<br>`https://weibo.com/p/1006061000000001`（蓝 V/机构）（兼容 `/home`、`/info` 子路径） | 仅接受 6 位用户容器前缀 **`100505`** 与 **`100606`**（历史 `100206`/`100306` 列 **V-03**），剥离前 6 位得稳定 `uid`。**严禁放行 `100808...`（超话容器）** | `社区资料（2026-11-21）` |
| **用户主页（昵称 `/n/`）** | `https://weibo.com/n/%E5%90%88%E6%88%90%E7%A4%BA%E4%BE%8B%E7%94%A8%E6%88%B7` | URL 解码得 `screen_name`，调 `GET https://weibo.com/ajax/profile/info?screen_name=<nick>` 换取稳定 `uid`（昵称可变，必须换 `uid` 后落库；列 **V-03**） | `社区资料（2026-11-21）` |
| **用户主页（个性域名）** | `https://weibo.com/synthetic_creator`（单段非纯数字且不在保留路径黑名单内） | 调 `GET https://weibo.com/ajax/profile/info?custom=<domain>` 换取稳定 `uid`（个性域名可变，绝不作主键；列 **V-03**） | `社区资料（2026-11-21）` |
| **独立视频专页** | `https://video.weibo.com/show?fid=1034:5000000000000001`（兼容 `%3A` 及十六进制 `1034:a1b2c3d4e5f60001`）<br>`https://weibo.com/tv/show/1034:5000000000000001`<br>`https://m.weibo.cn/s/video/index?object_id=1034:5000000000000001`<br>`https://m.weibo.cn/s/video/show?object_id=1034:5000000000000001` | 提取 `fid`/`oid = "1034:<media_id>"`；若 URL 同带 `mid=<数字>` 可直接关联宿主微博。**防误退化**：确认是 `1034:` 视频页时绝不可把 `<media_id>` 误当博文 `mid` 传给 `statuses/show` | `源码阅读（单链接解析参考，2026-11-21）`、`社区资料（2026-11-21）` |
| **直播 / 回放页** | `https://weibo.com/l/wblive/p/show/1022:2321325000000000000001`<br>`https://weibo.com/l/wblive/m/show/1022:2321325000000000000001`<br>`https://live.weibo.com/show?id=1022:2321325000000000000001` | 提取单场 `live_id`（常见 `1022:232132...` 或 `1042152:...`，见 [§8.1](#live-links)）；MVP 推荐识别后返回 `Kind: "live", Unsupported: true` | `源码阅读（单链接解析参考、直播录制参考，2026-11-21）`、`社区资料（2026-11-21）` |
| **`t.cn` 短链** | `https://t.cn/A6SYNTHETIC`（分享文本中常见 `http://t.cn/A6SYNTHETIC`） | **高风险通用短链**，按下方严格白名单规则逐跳展开 | `社区资料（2026-11-21）` |

#### `t.cn` 短链跳数、安全白名单与 `sinaurl` 解包规则

与 `b23.tv`、`v.douyin.com` 基本只指向站内不同，**`t.cn` 是微博通用外链短址服务，可跳转到任意第三方外站、广告或中转安全页 `weibo.cn/sinaurl?u=<encoded>`**（`源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）`）。因此强制执行：

1. **协议归一**：仅对 `http://t.cn/<code>` 在本地自动升级为 `https://t.cn/<code>` 后发起探针（其他 `http://` 链接一律拒绝）；禁止含 `user:pass@` 或非 443 端口（`https://t.cn/` 直连 `302` 行为列 **V-02**）。
2. **手动逐跳读取 `Location`、绝不带登录 Cookie**：禁止自动跟随，最多 **5 跳**，单跳超时 **10 秒**，发往 `t.cn` 与跳转探针时**强制不带任何 Cookie**，防止凭证泄露到未知落点。
3. **每跳域名白名单**：仅允许跟随到 HTTPS 443 端口且主机严格属于微博允许域集合：`t.cn`、`weibo.com`、`www.weibo.com`、`m.weibo.cn`、`weibo.cn`、`video.weibo.com`、`share.api.weibo.cn`、`weibointl.api.weibo.com`、`live.weibo.com`（由 `GuardedMediaDialer` 或同等拨号校验阻断私有/回环/链路本地 IP；越界归 `blocked`）。
4. **`weibo.cn/sinaurl?u=<encoded>` 本地解包**：若某跳落到 `weibo.cn/sinaurl`（查询参数 `u` 或 `toasturl`），**不发网络请求**，直接 URL 解码目标地址：目标主机在微博允许域白名单内且为 HTTPS 则重入本地路由；指向外部网站立即以 `unsupported_link` / `blocked` 拒绝（“该短链指向微博以外的网站，不支持解析”）。
5. **登录墙区分**：若某跳落到 `passport.weibo.com`、`visitor.passport.weibo.cn`、`login.sina.com.cn` 或 `weibo.com/newlogin`，归类为 `auth_required` 而非普通越界拦截。
6. **提前收敛**：任意一跳 `Location` 只要已经能从 URL 路径或查询参数提取出明确微博/视频/用户/直播标识，立即停止后续跳转，不拉取 HTML 正文。

#### 分享文本提取与裸数字冲突（对接 `internal/site/registry.go`）

- **分享文本提取**：微博 App 复制的文本常为混排文案 + 话题 `#...#` + URL（如 `【合成标题】分享@合成示例用户 的微博 https://m.weibo.cn/status/5000000000000001`）。扩展 `internal/site/registry.go` 的 `reBareKnownDomain` 纳入 `weibo.com`、`www.weibo.com`、`m.weibo.cn`、`weibo.cn`、`video.weibo.com`、`t.cn`、`share.api.weibo.cn`、`weibointl.api.weibo.com`、`live.weibo.com`，并更新 `DetectHostSite` 加入 `weiboDispatchDomains`（`源码阅读（仓库代码，2026-11-21）`）。遇空白、引号、尖括号、CJK/全角/Emoji 截断并剥离末尾英文标点 `.,;:!?)]}`。
- **多 URL 优先级**：若分享文本同时包含正文里的 `t.cn` 与末尾微博主链，**优先选择站内明确域名 URL，最后才回退取 `t.cn`**（`推测（2026-11-21）`）。
- **纯数字与裸 `mblogid` 碰撞边界**：仓库现有 `(*Registry).DetectSite`（`internal/site/registry.go`）将 19 位纯数字本地拒绝为歧义、将 `<19` 位纯数字默认路由给哔哩哔哩 UID（`源码阅读（仓库代码，2026-11-21）`）。微博 `uid`（常见 8～10 位）与 `mid`（常见 16 位）均落在 `<19` 位纯数字区间，而 9 位 `mblogid` 与普通英文词冲突；因此**不应对裸纯数字或裸 9 位字母数字自动猜测为微博**，微博输入应要求带域名的链接或分享文本（是否额外支持显式前缀如 `wb:` 作为 Q 项交由 A 决策，见[§12.3](#q-drafts) `Q-WB-5`）。

<a id="base62"></a>
### 3.2 `mid` 与 `mblogid`（Base62）换算算法与合成测试向量

来源：`源码阅读（单链接解析参考，2026-11-21）`、`社区资料（2026-11-21）`（以下 8 组向量均已通过本地纯数学双向脚本验证，零网络请求）。

- **固定 62 字符表**（下标 `0..61`）：
  `0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ`
  （`0-9` → `0..9`；`a-z` → `10..35`；`A-Z` → `36..61`）
- **注意**：微博换算**不是**整体大整数 Base62，而是**右对齐分段换算**（通用 Base62 库会算出错误结果）。
- **编码（`mid` 十进制字符串 → `mblogid`）**：
  1. 校验输入为非空正整数纯数字串（按字符串处理，严禁转 `float64`），去除无意义前导 `'0'`。
  2. **从右向左**每 **7 位十进制数字**切分为一段（最左段长 `1..7` 位）。
  3. 每段整数（`0..9,999,999 < 62^4 = 14,776,336`，至多 4 个 Base62 字符）转为 Base62 字符串。
  4. **零填充**：**除最左段（首段）外**，每段不足 4 字符时在左侧补 `'0'` 至恰好 4 字符；最左段**绝不补前导 `'0'`**。
  5. 从左向右拼接各段得到 `mblogid`。
- **解码（`mblogid` → `mid` 十进制字符串）**：
  1. 校验输入非空、全字符位于 62 字符表内且不全为 `'0'`。
  2. **从右向左**每 **4 个 Base62 字符**切分为一段（最左段长 `1..4` 字符）。
  3. 每段按 62 进制解码为非负整数 `val`。
  4. **越界必检**：对于**除最左段外的每一段**，因 `ZZZZ = 14,776,335 > 9,999,999`（临界值 `FXsj = 9,999,999`、`FXsk = 10,000,000`），**只要 `val >= 10,000,000` 必须立即报错拒绝**，防止拼出位数错乱的假 `mid`。
  5. **零填充**：**除最左段外**，每段十进制串不足 7 位时左侧补 `'0'` 至恰好 7 位；最左段不补前导 `'0'`。
  6. 从左向右拼接并去除首段冗余前导 `'0'`，结果须为正整数串。

| # | `mid`（十进制字符串） | 分段（右→左 7 位） | `mblogid`（Base62） | 覆盖边界 |
| --- | --- | --- | --- | --- |
| 1 | `5000000000000001` | `50` \| `0000000` \| `0000001` | `O00000001` | 中段全零 `0000000` ↔ `0000`，末段极小双边补零 |
| 2 | `5100123450000062` | `51` \| `0012345` \| `0000062` | `P03d70010` | 中段 3 字符 `3d7` 补 1 零成 `03d7`，末段 `62`→`10` 补 2 零成 `0010` |
| 3 | `4999999999999999` | `49` \| `9999999` \| `9999999` | `NFXsjFXsj` | 中末段均为 7 位最大值 `9999999` ↔ `FXsj` |
| 4 | `6200000010000001` | `62` \| `0000001` \| `0000001` | `1000010001` | 首段 `62 >= 62` 编成 2 字符 `10`（不补 4 位），总长 10 字符 |
| 5 | `3501234567890123` | `35` \| `0123456` \| `7890123` | `z0w7ex6A3` | 中段前导零 `0w7e` + 末段满 4 位 `x6A3` |
| 6 | `5209876543210009` | `52` \| `0987654` \| `3210009` | `Q48VUdt4l` | 常规三段 9 字符合成样本 |
| 7 | `1234567` | `1234567` | `5ban` | 历史 `<= 7` 位单段 `mid`，无补零 |
| 8（负样本） | *非法拒绝* | `50` \| `10000000`（溢出） \| `0000001` | `OFXsk0001` | 非首段 `FXsk = 10,000,000 >= 10^7`，必须解码失败报错 |

<a id="stable-ids"></a>
### 3.3 稳定 ID 与可变 ID 对比

**精度铁律**：微博 `mid`（16 位起，接近/可超 IEEE-754 双精度安全整数 `2^53 - 1`）与 `uid` 一律以 **`string`** 传递和落库，Go 解析动态 JSON 时优先读 `idstr`/`mid` 字符串字段或启用 `json.Decoder.UseNumber()`；并在 `internal/site/site.go` 的 `NormalizeUID(Weibo, uid)` 中校验为 5～19 位正十进制数字并去除前导零（`源码阅读（仓库代码，2026-11-21）`）。

| 标识 | 典型形态（合成示例） | 稳定性 | TRACE 存储与使用约束 | 证据等级 |
| --- | --- | --- | --- | --- |
| **`uid`**（用户数字 ID） | 常见 10 位正数字串（兼容 5～19 位），如 `"1000000001"` | **永久稳定** | **用户与订阅唯一持久化主键**（账户键 `weibo:1000000001`） | `源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）` |
| **`mid` / `idstr`**（微博数字 ID） | 常见 16 位正数字串，如 `"5000000000000001"` | **永久稳定** | **单条微博唯一持久化主键**（缓存目录与去重键） | `源码阅读（多站点订阅参考、单链接解析参考，2026-11-21）` |
| **`mblogid` / `bid`**（Base62 串） | 常见 9 位（或 10 位）字母数字，如 `"O00000001"` | **永久稳定**（与 `mid` 严格双射） | 用于构造规范外链 `https://weibo.com/<uid>/<mblogid>`；入库与去重统一归一为 `mid` | `源码阅读（单链接解析参考，2026-11-21）` |
| **`fid` / `oid`**（视频对象 ID） | `"1034:5000000000000001"` 或 `"1034:a1b2c3d4e5f60001"` | **对象级稳定**（独立于博文 `mid`） | 用于视频专页寻址；若 `tv/api/component` 返回宿主 `mid` 则优先归一到宿主微博（是否总能反查宿主 `mid` 记为 `unknown`，列 **V-09**） | `源码阅读（单链接解析参考，2026-11-21）` |
| **`live_id`**（直播对象 ID） | `"1022:2321325000000000000001"` | **单场次 ID（每次开播变化）** | 绝不可作主播固定主键（见 [§8.1](#live-links)） | `源码阅读（直播录制参考，2026-11-21）`、`社区资料（2026-11-21）` |
| **`screen_name`**（用户昵称，`/n/<昵称>`） | `"合成示例用户"` | **可变（随时可改名）** | **严禁作主键**；仅作展示名与一次性换 `uid` 入口 | `社区资料（2026-11-21）` |
| **`custom` / `domain`**（个性域名） | `"synthetic_creator"` | **可变（可改/可空）** | **严禁作主键**；仅作一次性换 `uid` 入口 | `社区资料（2026-11-21）` |

<a id="url-priority"></a>
### 3.4 路由匹配优先级与不支持链接拒绝表

来源：`源码阅读（多站点订阅参考、单链接解析参考、仓库代码，2026-11-21）`、`社区资料（2026-11-21）`、`推测（2026-11-21）`。

**按顺序执行的本地解析优先级**：

1. **短链展开**：`t.cn/<code>` → 按 [§3.1](#url-forms) 无 Cookie 白名单逐跳展开后重新进入第 2～10 步；
2. **`sinaurl` 拦截解包**：`weibo.cn/sinaurl?u=...` → 解码 `u`，仅微博允许域重入解析，外站立即拒绝；
3. **不支持子域/路径前置拒绝**：优先命中下方拒绝表（如 `s.weibo.com`、`card.weibo.com/article/`、`/ttarticle/`、`/p/100808...`、`/hot/` 等）立即报 `unsupported_link`，**防止落入第 9 步被误当个性域名！**
4. **App 分享页**：`share.api.weibo.cn/share/...`、`weibointl.api.weibo.com/share/...` → 提取 `weibo_id` 或 `,<mid>.html` → 单条微博；
5. **独立视频专页**：`video.weibo.com/show?fid=1034:...`、`weibo.com/tv/show/1034:...`、`m.weibo.cn/s/video/(index|show)?object_id=1034:...` → 视频对象 `fid`；
6. **直播页**：`weibo.com/l/wblive/(p|m)/show/<live_id>`、`live.weibo.com/...` → 直播路由（MVP 推荐返回 `Kind: "live", Unsupported: true` 并引导浏览器打开）；
7. **单条微博**：`weibo.com` / `weibo.cn` / `m.weibo.cn` 下的 `/detail/<id>`、`/2/detail/<id>`、`/status/<id>`、`/statuses/show?id=<id>`、`/<uid>/<mid_or_mblogid>`（含移动端 `/<uid>/<mid>/<channel>`）→ 统一归一为 `mid`；
8. **明确用户主页**：`/u/<uid>`、`/profile/<uid>`、`/p/100505<uid>`、`/p/100606<uid>`、单段纯数字 `/<uid>`（5～19 位）、`/n/<screen_name>` → 用户主页（`/n/` 经 API 换 `uid`）；
9. **个性域名用户主页（最低优先级）**：`weibo.com/<custom>`（仅单段路径，匹配 `^[A-Za-z0-9_.-]{3,64}$`，且**严格排除保留路由黑名单**：`home`、`hot`、`fav`、`like`、`mygroups`、`groups`、`search`、`tv`、`l`、`p`、`u`、`n`、`a`、`z`、`detail`、`status`、`statuses`、`ajax`、`newlogin`、`login`、`login.php`、`signup`、`logout`、`ttarticle`、`article`、`at`、`comment`、`dm`、`message`、`settings`、`set`、`verify`、`about`、`help`、`rank`、`discover`、`friends`、`follow`、`fans`、`album`、`video`、`live`）→ 调 `profile/info?custom=<custom>` 换取稳定 `uid`；
10. **兜底拒绝**：其余一律本地拒绝（`unsupported_link` / `bad_request` 附 `Unsupported=true`，0 次 API 请求）。

**明确拒绝的不支持链接表**（本地或短链落定即停，零额外 API 请求）：

- **超话与话题**：`weibo.com/p/100808...`、`m.weibo.cn/p/index?containerid=100808...`、`s.weibo.com/weibo?...`、`weibo.com/k/...`、`m.weibo.cn/k/...`。
- **搜索、热榜与首页流**：`s.weibo.com/*`、`weibo.com/hot/*`、`weibo.com/a/hot/*`、`m.weibo.cn/search*`、`m.weibo.cn/p/searchall*`、`weibo.com/home*`、`weibo.com/mygroups*`、`weibo.com/fav*`。
- **独立头条文章 / 投票 / 电商 / 视频合集聚合页**：`weibo.com/ttarticle/*`、`card.weibo.com/article/*`（单条微博内嵌文章卡片在 [§4.6](#degrade) 降级显示，但直接粘贴独立文章页链不在 MVP 解析范围）、`vote.weibo.com`、`mall.weibo.com`、`e.weibo.com`、独立视频合集聚合页。
- **登录、通行证与访客验证页**：`passport.weibo.com/*`、`login.sina.com.cn/*`、`weibo.com/newlogin*`、`visitor.passport.weibo.cn/*`。
- **非安全或外站链接**：非 `https://`（仅 `http://t.cn/` 按规则升 HTTPS）、含 `user:pass@`、非 443 端口、私有/回环 IP，以及任何非微博域名（含 `t.cn` / `sinaurl` 指向的第三方外链）。

<a id="post"></a>
## 4. 单条微博的内容（R-C）

<a id="post-detail"></a>
### 4.1 详情接口与 PC Ajax vs 移动 H5 选型对比

| 维度 | PC Ajax（**推荐唯一主栈**） | 移动 H5（仅协议对照，不作生产双栈） | 证据等级 |
| --- | --- | --- | --- |
| 详情端点 | `GET https://weibo.com/ajax/statuses/show?id=<mblogid_or_mid>&locale=zh-CN&isGetLongText=true` | `GET https://m.weibo.cn/statuses/show?id=<mid_or_mblogid>`；全文另调 `/statuses/extend?id=<mid>` | `源码阅读（多站点订阅参考、单链接解析参考，2026-11-21）`、`社区资料（2026-11-21）` |
| 响应信封 | **顶层即微博对象**（无 `data` 外壳），同级带 `ok: 1`；失败 `ok: 0` 或 `-100` | `{"ok": 1, "data": {<微博对象>}}` | `社区资料（2026-11-21）` |
| 核心标识 | `id`（须 `UseNumber`）、`idstr`、`mid`、`mblogid` | `id`、`mid`、`bid` | `源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）` |
| 时间格式 | `created_at` 固定为绝对时间（RFC-like `"%a %b %d %H:%M:%S %z %Y"`，如 `"Sat Nov 21 15:00:00 +0800 2026"`） | 部分列表偶返相对时间（如 `"5分钟前"`），解析误差大 | `社区资料（2026-11-21）` |
| 文本与结构化引用 | `text_raw`（保换行的纯文本）、`text`（HTML）、`isLongText`、`url_struct[]`（短链→长链/标题/行内 `pic_infos`）、`topic_struct[]`、`region_name`、`source`、`edit_count` | `text`（HTML）、`isLongText`、`longText.longTextContent`；常缺 `text_raw` 与 `url_struct` | `社区资料（2026-11-21）` |
| 多图 / 混排 / 视频完整度 | **完整**：`pic_ids[]` + `pic_infos{}`（完整 `>9` 图）、`mix_media_info.items[]`（图文视频混排）、`page_info.media_info.playback_list[]`（多档清晰度与编码） | **残缺**：超过 9 图常被截断为 9 张（参考实现须回退 PC Ajax 补齐）；视频常缺 `playback_list` 高清档 | `源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）` |
| 会话一致性 | 与桌面浏览器复制的 `weibo.com` Cookie 天然同域 | 易撞 `HTTP 432` / 访客跳转，存在跨域同步风险 | `源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）` |

按 [DEC-040](../decisions.md#dec-040)，互动计数 `reposts_count`、`comments_count`、`attitudes_count` 仅在在线详情页展示，**不落盘缓存**。一致性校验：详情返回的 `idstr`/`mid` 必须与请求目标一致，否则按 `schema_changed` 拒绝。

<a id="long-text"></a>
### 4.2 长微博全文（`isLongText`）

1. **判定**：`status.isLongText == true`（列表页中 `text` 末尾常见 `class="expand">展开</span>` 或 `text_raw` 截断）（`社区资料（2026-11-21）`）。
2. **获取路径**：
   - 单条详情优先带 `isGetLongText=true` 调 `/ajax/statuses/show`；若响应直接内联完整 `text_raw` 或 `longText.longTextContent` 可省去二次请求（是否所有长博均内联记为 `unknown`，列 **V-06**）（`源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）`）。
   - 列表页或详情未内联时补调：`GET https://weibo.com/ajax/statuses/longtext?id=<mblogid>`，响应 `{"ok": 1, "data": {"longTextContent": "...", "topic_struct": [...], "url_struct": [...]}}`（社区统一传 `mblogid`，是否兼容数字 `mid` 列 **V-06**）（`社区资料（2026-11-21）`）。
   - **被转发长微博**：当 `retweeted_status.isLongText == true` 时，外层请求未必展开原博全文，须对 `retweeted_status.mblogid` 另调一次 `longtext`（`源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）`）。
   - **正文短链与行内图还原**：正文里的 `http://t.cn/...` 优先用同级 `url_struct[]` 映射 `short_url` → `long_url` / `url_title`（若 `url_struct[i].pic_infos` 非空，其 `woriginal.url` 为行内评论/正文配图），避免为还原显示文本去逐条请求 `t.cn`（`社区资料（2026-11-21）`）。

<a id="retweets"></a>
### 4.3 转发（`retweeted_status`）与快转（`ori_mid`）

1. **标准转发**：外层 `status` 记录转发评语与转发者，内层 `retweeted_status` 为同构微博对象（`社区资料（2026-11-21）`）。
   - **媒体挂点陷阱**：转发视频微博时，部分响应把原视频 `page_info.media_info` 挂在**外层 `status`**，而原图片仍在 **`status.retweeted_status`**（`社区资料（2026-11-21）`）。解析转发微博媒体时必须同时检查外层与内层，并按 `pic_id` / `object_id` 去重。
   - **本地缓存存放对齐（[DEC-056](../decisions.md#dec-056)）**：缓存转发微博（含其中携带的图片与视频）时，统一存于**转发者**文件夹的 `forwards/` 下，并在 `item.json` 记录 `via = {reason: "forward", site: "weibo", uid: <转发者uid>, ...}`，媒体库“转发”筛选自动生效（`源码阅读（仓库代码，2026-11-21）`）。
2. **原微博已删除 / 不可见 / 无权查看**：
   - 识别特征：`retweeted_status.deleted == "1"`（兼容整型 `1`）；或 `retweeted_status.user == null` / 缺 `user.idstr`；或 `text` / `text_raw` 含系统提示（如 `"抱歉，此微博已被作者删除"`、`"由于作者设置，你暂时没有这条微博的查看权限"`、`"目前内容暂不可见"`）（`源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）`；PC Ajax 精确结构列 **V-10**）。
   - **降级策略**：绝不因 `retweeted_status.user == null` 抛错或判 `schema_changed`；完整保留外层转发评语与时间，原博区域降级显示“原微博已删除 / 暂不可见”及平台原文提示（`推测（2026-11-21）`）。
3. **快转（Fast Retweet）**：
   - 无嵌套 `retweeted_status`，顶层带 **`ori_mid`**（原博 ID），常伴 `title.text` / `screen_name_suffix_new`（如 `"转发了"`）（`社区资料（2026-11-21）`）。快转条目的 `user` 与 `created_at` 归属原博还是快转动作存在版本差异，记为 `unknown`（列 **V-05**，直接决定订阅检查单调性规则，见 [§6.3](#monotonicity)）。

<a id="images"></a>
### 4.4 图片（超过 9 图、原图与水印、GIF、实况照片、图文视频混排）

1. **超过 9 图（10～18 张）与顺序保证**：
   - PC Ajax `pic_ids[]` 按作者编排顺序完整给出全部 `pic_id`（最多 18 张），`pic_infos` 为 `map[pic_id]PicInfo`（`社区资料（2026-11-21）`）。解析时**必须按 `pic_ids` 切片顺序索引 `pic_infos[id]`**，严禁直接遍历 Go `map` 导致乱序。
2. **图文视频混排（`mix_media_info`）**：
   - 当存在非空 `mix_media_info.items[]` 时，**优先按 `mix_media_info.items` 顺序解析**（每项 `type == "pic"` 取 `item.data` 同 `PicInfo`，`type == "video"` 取 `item.data.media_info` 与 `item.data.object_id`），否则再走 `pic_ids`+`pic_infos` 与 `page_info`（`社区资料（2026-11-21）`；实际结构列 **V-08**）。该结构可直接映射到仓库现有 `site.DownloadPlan{Kind: "image_note", Images: []MediaImageAsset}` 混排模型（`源码阅读（仓库代码，2026-11-21）`）。
3. **各尺寸档位与去平台水印边界**：
   - `pic_infos[id]` 内常见尺寸对象（含 `{url, width, height, cut_type, type}`）：`thumbnail`、`bmiddle`、`large`、`mw2000`、`original`、`largest`、`largecover`（`社区资料（2026-11-21）`）。
   - **默认最高清优先级**：`largest` → `original` → `mw2000` → `large` → `bmiddle`（并在非空档位中核对 `width * height` 最大者）。TRACE 只缓存原图：备用地址只用与原图同尺寸的档位，不退到 `bmiddle` 与缩略图（2026-11-24 用户要求）（`源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）`）。
   - **去平台水印变体（`unknown`，列 V-07）**：单链接解析参考观察到 `sinaimg.cn` 的 `/mw690/`、`/mw2000/`、`/large/`、`/woriginal/` 可能带右下角平台昵称水印，改写路径为 `/osj1080/` 或 `/oslarge/` 可去水印（`源码阅读（单链接解析参考，2026-11-21）`）；社区另见原图下载接口 `GET https://weibo.com/ajax/common/download?pid=<pic_id>`（`社区资料（2026-11-21）`）。但 `/osj1080/` 可能限宽 1080px、`/oslarge/` 对部分 PNG/GIF/历史图可能 404、`common/download` 是否需登录 Cookie 均为 `unknown`。**推荐策略**：MVP 默认使用 `largest` / `original` 直链保证原分辨率与动图完整，待 **V-07** 实测确认无损后再评估去水印开关（见 [§12.3](#q-drafts) `Q-WB-6`）。
4. **GIF 动图（`type == "gif"`）**：
   - `pic_infos[id].type == "gif"` 同时给出 `largest.url`（原始 `.gif`，`wx1..4.sinaimg.cn`）与 `video`（转码 MP4，常见 `g.us.sinaimg.cn` 或 `f.video.weibocdn.com`，带签名）（`社区资料（2026-11-21）`）。推荐默认保存 `largest.url` 原始 `.gif`（与看图面板兼容、无签名过期风险），在 **V-08** 中确认其确为多帧 GIF 而非静态首帧。
5. **实况照片（`type == "livephoto"`）**：
   - PC Ajax `pic_infos[id]` 标 `type == "livephoto"`：静帧在 `largest.url`，伴侣动态片段在 **`pic_infos[id].video`**（常见 `https://livephoto.us.sinaimg.cn/...mov?...` 或 `https://video.weibo.com/media/play?livephoto=...` 经 302 落至带签名的 `.mov` / `.mp4`）；移动端对照字段为 `pics[i].videoSrc` 或顶层 `live_photo[]`（`源码阅读（多站点订阅参考、单链接解析参考，2026-11-21）`、`社区资料（2026-11-21）`）。
   - **容器兼容提醒**：实况伴侣视频常为 QuickTime **`.mov`**（`video/quicktime`，`ftyp=qt  `），而非标准 `mp42`/`isom`；其与现有 `mp4ff` 及 `EnsureSingleMP4Context` 的兼容性列入 **V-08 / V-12** 核实。

<a id="videos"></a>
### 4.5 视频（`playback_list`、选流、登录差异、独立视频页与合集）

1. **结构位置**：常规微博视频在 `page_info.media_info`（`object_type == "video"` 或 `type == "11"`/`"video"`）；混排视频在 `mix_media_info.items[i].data.media_info`（`社区资料（2026-11-21）`）。必须排除进行中直播卡片（`object_type == "live"` 或 `media_info.live_status == 1`）以及无正片流的电影词条卡片（`page_info.type == "5"`）。
2. **`playback_list[]` 字段契约**（`社区资料（2026-11-21）`）：
   - `meta`：`label`（如 `mp4_4k`、`mp4_2k`、`mp4_1080p`、`mp4_720p`、`mp4_hd`、`mp4_ld`、`hevc_mp4_*`、`scrubber_hd`）、`quality_index`（数值排序键，如 `360`/`480`/`720`/`1080`/`1440`/`2160`）、`quality_label`（如 `"1080p"`）、`quality_desc`（如 `"高清"`/`"超清"`）、`quality_class`（`"HD"`/`"SD"`）、`is_hidden`（bool）。
   - `play_info`：`url`、`mime`（单文件视频为 **`"video/mp4"`**）、`protocol`（常规 `"general"`）、`width`、`height`、`fps`、`bitrate`（bps）、`size`（字节）、`duration`、`video_codecs`（`avc1.*` 为 H.264；`hev1.*`/`hvc1.*` 为 H.265/HEVC）、`audio_codecs`（`mp4a.*` 为 AAC）、`audio_sample_rate`、`audio_channels`、`extension.transcode_info.origin_video_dr`（`"SDR"`/`"HDR"`）。
   - **雪碧图陷阱**：`playback_list` 中包含进度条缩略雪碧图 `label == "scrubber_hd"`，其 **`mime == "image/jpeg"`**！选流必须强过滤 `play_info.mime == "video/mp4"` 且 `!meta.is_hidden` 且 `url != ""`（`社区资料（2026-11-21）`）。
   - **老视频兜底**：仅当 `playback_list` 为空时，回退读 `media_info.{mp4_720p_mp4, mp4_hd_url, stream_url_hd, h265_mp4_hd, hevc_mp4_720p, mp4_sd_url, stream_url, replay_hd}`（可从 URL `label=...&template=WxH` 提取规格）（`社区资料（2026-11-21）`）。
3. **最高档选流规则**（对齐 `FR-CACHE-002` 与 [DEC-043](../decisions.md#dec-043) ⑤）：
   1. 过滤空 URL、`mime != "video/mp4"`、`is_hidden == true` 及非渐进式/未知加密协议项；
   2. 按分辨率归一化短边（`2160P > 1440P > 1080P > 720P > 480P > 360P`）为主、`quality_index` 降序为辅锁定最高可用档（或用户设置的上限内最高档）；
   3. 同分辨率档内按用户编码偏好（H.264 `avc1` vs H.265 `hev1`/`hvc1`）；
   4. 再按 `fps` 降序、`bitrate` 降序、`size` 降序选出最优单文件 MP4（音画同轨，无需混流合并）。
4. **登录 vs 访客画质差异**：社区记录访客/未登录通常限 720P 及以下，登录可解锁 1080P，部分 2K/4K/60fps/HDR 可能额外受限于微博 VIP/SVIP（普通登录号 vs 访客 vs VIP 精确差异记为 `unknown`，列 **V-09**）（`社区资料（2026-11-21）`）。TRACE 只缓存当前登录账号有权拿到的明文档，绝不绕过 VIP 限制。
5. **独立视频专页（`1034:<oid>`）**：
   - 端点：`POST https://weibo.com/tv/api/component?page=/tv/show/1034:<oid>`，表单体 `data={"Component_Play_Playinfo":{"oid":"1034:<oid>"}}`，携带 `Referer` 与 `Page-Referer: /tv/show/1034:<oid>`（`源码阅读（单链接解析参考，2026-11-21）`、`社区资料（2026-11-21）`）。
   - 响应：`code == "100000"`（字符串），`data.Component_Play_Playinfo` 含 `mid`、`title`、`author`、`author_id`、`cover_image`、`duration`、`urls`（如 `map["高清 1080P"]"//f.video.weibocdn.com/..."`，协议相对 URL 须补 `https:`）与 `stream_url` / `stream_url_hd`。
   - **推荐链路**：若返回非空 `mid`，再调 `/ajax/statuses/show?id=<mid>&isGetLongText=true` 复用统一正文与 `playback_list` 解析；若无宿主博文则直接消费 `Component_Play_Playinfo.urls`（列 **V-09**）（`推测（2026-11-21）`）。
6. **视频合集**：单条视频 `media_info.belong_collection` 标明所属合集；用户视频瀑布流另有 `GET https://weibo.com/ajax/profile/getWaterFallContent?uid=<uid>&cursor=<0|next_cursor>`（`next_cursor < 0` 结束）（`社区资料（2026-11-21）`）。独立合集聚合页的现代 PC Ajax 完整契约记为 `unknown`，建议 MVP 拒绝合集聚合链接并延后至 loop。

<a id="degrade"></a>
### 4.6 暂不支持的富媒体卡片与降级策略

| 附件类型 | 识别特征（`page_info`） | 协议现状 | TRACE MVP 推荐降级展示 | 证据等级 |
| --- | --- | --- | --- | --- |
| **头条文章** | `object_type == "article"`（或 `type == "article"`/`"24"`），`page_url` 指向 `card.weibo.com/article/...` 或 `weibo.com/ttarticle/...` | 存在移动端详情接口 `card.weibo.com/article/m/aj/detail?id=<id>`（注意 `is_article_free == 1` 反直觉代表付费试读），富文本复杂且含付费墙 | 保留微博正文 + 展示文章摘要卡（`page_title`、`page_desc`/`content1`、`page_pic` 头图、“头条文章”标签 + “在浏览器中打开”），不爬正文 HTML | `源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）` |
| **投票** | `object_type == "vote"` 或 `page_url` 指向 `vote.weibo.com` | 互动类卡片，无归档媒体流 | 保留微博正文 + 投票标题摘要卡 + “投票（暂不支持）”与浏览器打开入口 | `社区资料（2026-11-21）`、`推测（2026-11-21）` |
| **音乐 / 音频卡** | `object_type == "audio"` / `type == "music"` | 版权音频外链或试听流 | 保留正文 + 曲目信息卡 + 浏览器打开入口 | `社区资料（2026-11-21）`、`推测（2026-11-21）` |
| **外链 / 网页 / 超话 / 地点 / 电影词条** | `object_type` 为 `webpage`/`topic`/`place`/`story`/`collection` 等，或 `type == "5"` | 非博主自产媒体附件 | 保留正文与配图；**严禁**把 `topic`/`search`/`place` 的 `page_pic` 误当微博封面；底部展示普通链接卡 | `源码阅读（单链接解析参考，2026-11-21）`、`社区资料（2026-11-21）` |
| **直播卡片** | `object_type == "live"` 或 `media_info.live_status == 1` | 非点播视频 | 展示直播标题、封面与“直播（暂不支持录制）”提示及浏览器打开入口，绝不误当 MP4 下载 | `社区资料（2026-11-21）` |
| **图文视频混排** | `mix_media_info.items[]` | 非不支持项 | **原生支持**：按 `image_note` 混排页（图片 + 实况 + 视频）完整解析与缓存 | `社区资料（2026-11-21）` |

<a id="visibility"></a>
### 4.7 可见范围识别

- **单条 `visible: {type, list_id}`**：
  - 明确共识：`type == 0` = **公开**；`type == 1` = **仅自己可见**；`list_id > 0` = **定向分组可见**（`社区资料（2026-11-21）`）。
  - **社区分歧（记为 `unknown`，列 V-10）**：不同资料对 `type == 6` 与 `type == 10` 分别标为“好友圈”与“仅粉丝可见”（互换），另有历史枚举 `2`/`3`/`7`。
  - **处理原则**：只要详情接口返回 `ok == 1` 且带正文，说明当前登录账号**有权查看**，绝不因 `visible.type != 0` 拒绝解析或缓存；`0` 标公开、`1` 标“仅自己可见”，其他非零值在实测锁定前统一提示“受限可见（粉丝/好友圈/分组）”并保留原始枚举值用于诊断（`推测（2026-11-21）`）。
- **已删除 / 无权限**：移动端已知 `errno == "20101"`（不存在/已删除 → `unavailable`）、`errno == "20112"`（无查看权限 → `forbidden`）；PC Ajax `/ajax/statuses/show` 精确码列 **V-10**（`社区资料（2026-11-21）`）。
- **博主可见时间窗（半年 / 一年 / 一月 / 三天可见）**：由主页列表响应 `bottom_tips_visible: true` + `bottom_tips_text` 标明（见 [§6.4](#visibility-window)）。

<a id="media-cdn"></a>
## 5. 媒体下载、白名单与跳转规则（R-D）

<a id="cdn-domains"></a>
### 5.1 媒体 CDN 域名、防盗链、签名 TTL 与容器特性

| 资产类型 | 典型 CDN 域名（官方根域） | `Referer` 与防盗链 | Cookie 要求 | 签名参数与 TTL | `Range` 与文件格式 | 证据等级 |
| --- | --- | --- | --- | --- | --- | --- |
| **正文图片 / 封面 / 头像** | `wx1..4.sinaimg.cn`（主力）、`tvax1..4.sinaimg.cn`（头像/镜像，WAF 较 `tva1..4` 宽松）、`tva1..4.sinaimg.cn`、`ww1..4.sinaimg.cn`、`ws1..4.sinaimg.cn`、`n.sinaimg.cn`、`h5.sinaimg.cn`（统一后缀 `sinaimg.cn`） | 异站 `Referer` 返回 `HTTP 403`；**`Referer: https://weibo.com/`**（或无 Referer）放行 | **不带 Cookie** | 常规直链无时效签名；偶带查询参数不作持久信任 | JPEG / PNG / GIF（偶见 WebP）；按 `Content-Type` + 文件魔数定扩展名（列 **V-11**） | `源码阅读（多站点订阅参考、单链接解析参考，2026-11-21）`、`社区资料（2026-11-21）` |
| **点播视频** | `f.video.weibocdn.com`（主力）、`*.weibocdn.com`、`f.us.sinaimg.cn`、`us.sinaimg.cn`、`locallimit.us.sinaimg.cn` | 强校验 **`Referer: https://weibo.com/`**（缺失或异站易 403） | **不带 Cookie** | 带 `Expires=<Unix秒>&ssig=<合成签名>&KID=unistore,video`；社区观察 TTL 约数十分钟～数小时（精确下限 `unknown`，列 **V-12**） | 音画同轨渐进式**单文件 MP4**（AVC/HEVC + AAC）；支持 `Range: bytes=...` 返回 `HTTP 206`（8 MiB 分段与是否 302 跨外域列 **V-12**） | `社区资料（2026-11-21）` |
| **GIF 转码视频** | `g.us.sinaimg.cn`、`f.video.weibocdn.com` | `Referer: https://weibo.com/` | **不带 Cookie** | 同上（带 `Expires`/`ssig`/`KID`） | MP4（默认优先存 `wx*.sinaimg.cn` 原 `.gif`） | `社区资料（2026-11-21）` |
| **实况照片动态片段** | `livephoto.us.sinaimg.cn`；偶由 `video.weibo.com/media/play?livephoto=<合成占位>` 首跳 302 至 `livephoto.us.sinaimg.cn` / `*.weibocdn.com` | `Referer: https://weibo.com/` | **不带 Cookie**（`video.weibo.com/media/play` 无 Cookie 跳转稳定性列 **V-12**） | 落点带 `Expires`/`ssig`/`KID` 签名 | 常见 **`.mov`（`video/quicktime`，`ftyp=qt  `）**，部分 `.mp4`（`video/mp4`）；Range 支持与 `mp4ff` 兼容列 **V-12** | `源码阅读（单链接解析参考，2026-11-21）`、`社区资料（2026-11-21）` |

<a id="media-whitelist"></a>
### 5.2 对齐 `internal/site` 的白名单、跳转与签名不落盘设计

对照 `internal/site/types.go`（`MediaTransportSpec`）、`internal/site/mediadial.go`（`GuardedMediaDialer`）与 `internal/download/range_download.go`（`makeMediaRedirectCheck`、`looksLikeDisguisedTextBody`）（`源码阅读（仓库代码，2026-11-21）`）：

1. **默认媒体白名单 `DefaultWeiboMediaHosts()`（最小后缀集）**：
   - `sinaimg.cn`（覆盖 `wx1..4`、`tvax1..4`、`tva1..4`、`ww1..4`、`ws1..4`、`g.us`、`f.us`、`livephoto.us`、`us`、`n`、`h5`）
   - `weibocdn.com`（覆盖 `f.video.weibocdn.com`、`live.video.weibocdn.com` 等）
   - **实况首跳最小特例**：若实况地址为 `https://video.weibo.com/media/play?livephoto=...`，仅为实况下载构造专用 `MediaTransportSpec` 临时允许精确主机 `video.weibo.com` 且本地强校验路径等于 `/media/play`，**绝不把泛 `weibo.com` 加入全局媒体白名单**（`推测（2026-11-21）`）。
2. **`WeiboMediaTransport(userAgent, allowLocalhost)` 规范**：
   - `Site: site.Weibo`、`AllowedDomains: DefaultWeiboMediaHosts()`、`UserAgent: <固定桌面 Chrome UA>`、`Referer: "https://weibo.com/"`、`Origin: ""`。
   - **强无 Cookie**：`ApplyHeaders(req)` 在首跳与每一跳重定向强制 `req.Header.Del("Cookie")`，确保绝不向任何 CDN 泄露微博登录态。
   - **`PublicRedirects` 策略**：图片恒为 `false`（死锁 `sinaimg.cn`）；视频与实况**初始默认 `false`**，经 **V-12** 实测核对 `f.video.weibocdn.com` / `livephoto.us.sinaimg.cn` 是否会 302 调度至第三方公网边缘域——若实测存在第三方边缘 302，再仿抖音 `ACT-012` 仅为视频/实况开启 `PublicRedirects: true`（首跳仍必须命中 `AllowedDomains`，后续最多 5 跳仅放行 HTTPS 443 公共域名 `IsPublicDNSName`，并由 `GuardedMediaDialer` 阻断回环、私有、链路本地、CGNAT `100.64.0.0/10` 与 NAT64 IP）。
   - **如需认证首跳跳转的例外模式**：若未来实测证实某原图接口（如 `/ajax/common/download?pid=`）确需登录 Cookie 换取 302 落点，严格复用抖音 `Client.ResolvePlayRedirect` + `download.PlayURLResolver` 模式——仅向已校验主站发单跳 `noRedirect` 请求取 `Location`，经 `ValidateRedirectURL` 校验后再由无 Cookie 媒体通道下载。
3. **防伪装响应、过期续传与签名不落盘**：
   - 复用 `looksLikeDisguisedTextBody` 拒绝 `text/html`、`application/json`、`text/plain` 假 200 响应。
   - 所有带签名媒体地址（`DownloadPlan`、`MediaImageAsset`、`RawPost` URL 字段）全部标记 `json:"-"` 仅驻留内存，绝不写入 `item.json`、`updates.db`、`subscriptions.json`、`cache-queue.json`、日志或错误消息（复用 `SanitizeEndpoint`、`UnwrapURLError`、`scrubSignedValues`）。
   - 下载中途遇 `HTTP 403`（疑似 `Expires` 过期）时，单文件最多重新调 `1` 次 `/ajax/statuses/show` 刷新直链并按已落盘字节做 `Range` 续传。
   - **`.mov` 容器校验边界**：现有 `EnsureSingleMP4Context` 使用 `mp4ff v0.63.0` 校验 `moov` 与音视频轨；实况照片 `.mov` 若 `ftyp` 为 QuickTime `qt  `，需在 **V-12** 实测样本上验证 `mp4ff` 是否可直接解封装或需增加实况 `.mov` 轻量头校验分支（不新增外部库）。

<a id="user-feed"></a>
## 6. 用户主页与订阅检查（R-E）

<a id="user-profile"></a>
### 6.1 用户资料接口

- **主选（PC Ajax）**：`GET https://weibo.com/ajax/profile/info?uid=<uid>`（亦支持 `?custom=<个性域名>` 或 `?screen_name=<昵称>` 反查稳定 `uid`，见 **V-03**）（`社区资料（2026-11-21）`）。
  - 响应：`{"ok": 1, "data": {"user": {...}, "tabList": [...], "blockText": ""}}`。
  - 核心字段：`user.id` / `user.idstr`（**稳定主键**）、`screen_name`（昵称）、`avatar_hd` / `avatar_large` / `profile_image_url`（头像，优先前两者）、`description`（简介）、`statuses_count`（历史总微博数，仅参考）、`followers_count`、`friends_count`、`verified`、`verified_type`、`verified_reason`、`domain`（个性域名）、`cover_image_phone`、`data.blockText`（账号封禁/异常/拉黑提示）。
- **扩展资料（可选，默认不额外请求）**：`GET https://weibo.com/ajax/profile/detail?uid=<uid>`（注册时间、公司/实名标签、阳光信用等；主页浏览 1 次 `profile/info` 已足够）（`社区资料（2026-11-21）`）。
- **移动 H5 对照**：`GET https://m.weibo.cn/api/container/getIndex?type=uid&value=<uid>` 返回 `data.userInfo` 与 `tabsInfo.tabs`（微博容器 `107603<uid>`），不作主栈（`源码阅读（多站点订阅参考，2026-11-21）`）。

<a id="user-timeline"></a>
### 6.2 微博列表接口、翻页与筛选

- **主选（PC Ajax）**：`GET https://weibo.com/ajax/statuses/mymblog?uid=<uid>&page=<1..N>&feature=<0|1|2|3|4|10>&since_id=<cursor>`（第 1 页省略或传空 `since_id`）（`社区资料（2026-11-21）`）。
  - 响应：`{"ok": 1, "data": {"list": [...], "since_id": "<string>", "total": <int>, "status_visible": <int>, "bottom_tips_visible": <bool>, "bottom_tips_text": "<string>"}}`。
  - **翻页规则**：每页约 `20` 条（精确页长列 **V-04**）；下一页同时递增 `page` 并原样透传上一页的 `data.since_id`（必须以 `string` 处理）；跨页按 `idstr` 去重；若 `next_since_id == req_since_id`（游标停滞）立即中止以防死循环。
- **`feature` 筛选参数**：`0` = 全部（订阅检查默认，交 TRACE 本地缓存规则过滤类型）、`1` = 原创、`2` = 图片、`3` = 视频、`4` = 音乐、`10` = 头条文章（`源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）`；`feature=1` 是否过滤快转列 **V-05**）。
- **高级时段搜索不作主路径**：`/ajax/statuses/searchProfile`（带 `starttime`/`endtime` 及 `hasori`/`hasret`/`hastext`/`haspic`/`hasvideo`/`hasmuisc`[服务端原拼写]）社区实测单窗口到第 60 页即空且易漏博，不纳入默认翻页路径（`社区资料（2026-11-21）`）。
- **移动 H5 对照**：`m.weibo.cn/api/container/getIndex?containerid=107603<uid>&since_id=...` 混杂非微博杂卡（仅 `card_type == 9 && card.mblog != nil` 为微博），且存在 9 图截断与缺高画质视频，不作主栈（`源码阅读（多站点订阅参考，2026-11-21）`）。

<a id="monotonicity"></a>
### 6.3 置顶、快转与杂项卡片对时间单调性的影响（对照 `douyin.md §6.3`）

来源：`源码阅读（多站点订阅参考、仓库代码，2026-11-21）`、`社区资料（2026-11-21）`、`推测（2026-11-21）`（快转 `created_at` 与 `user` 归属为 `unknown`，列 **V-05**）。

对照[抖音协议资料 §6.3](douyin.md#posts)，微博时间线除“置顶”外还多出“快转（`ori_mid`）”与偶发推广/外人卡片，早停逻辑必须更严格：

| 维度 | 抖音 `aweme/post`（[抖音 §6.3](douyin.md#posts)） | 微博 `ajax/statuses/mymblog` | 对 TRACE 订阅检查的强制规则 |
| --- | --- | --- | --- |
| **置顶标记** | `is_top == 1`（1～3 条，排第 1 页首） | `isTop == 1`（另见 `mblogtype == 2` 或 `title.text == "置顶"`；移动端为 `profile_type_id` 以 `proweibotop` 开头），可有多条 | 同抖音：第 1 页置顶写入 `account_baseline_pinned`，**绝不触发早停** |
| **快转乱序** | 无 | **微博特有**：含 `ori_mid` 的快转按快转时刻排入列表，但内部 `created_at` / `mid` 可能对应数月前的原博（精确归属列 **V-05**） | **必须增强**：快转同样**只按 ID 去重、绝不触发早停**，防止旧原博时间把整页新微博提前截断 |
| **推广 / 外人 / 系统卡** | 无 | 偶见推广/广告（`mblogtype == 1`、`is_paid` 或 `user.idstr != target_uid`）及系统赞/关注行为卡 | 入处理循环前先剔除非本人且非快转的推广/外人卡片（`user.idstr != uid && ori_mid == ""`） |
| **常规条目单调性** | 非置顶严格按 `create_time` 降序 | 非置顶、非快转的常规本人微博按 `created_at`（及 `mid`）降序；编辑微博不改原发布时间 | **仅当遇到常规非置顶、非快转的本人微博**命中基线（`id == newest_id` 或 `pub_ts <= newest_ts`，同秒比 `mid`）才可早停 |
| **翻页游标** | 13 位毫秒时间戳 `max_cursor` | 不透明字符串 `since_id` + 递增 `page` | 需同时判 `since_id == ""`、`bottom_tips_visible` 与游标停滞 |

<a id="visibility-window"></a>
### 6.4 列表完整性与可见时间窗限制

1. **博主可见时间窗（半年 / 一年 / 一月 / 三天可见）**：属于微博原生隐私设置。`profile/info` 的 `statuses_count` 与 `mymblog` 的 `total` 仍显示全量历史数，但翻至时间窗边界时服务端返回 `bottom_tips_visible: true`、`bottom_tips_text`（写明限制文案）并清空 `since_id`（`社区资料（2026-11-21）`；精确字段列 **V-05**）。TRACE 必须在用户页底部与“全部缓存”摘要中如实显示 `bottom_tips_text`，不误报抓取失败。
2. **未登录 / 访客限制**：无登录态调 `mymblog` 常 302 跳登录/访客页或返回 `ok: -100`；访客态即使放行亦仅见极少前几页（具体阈值 `unknown`，列 **V-04**）。
3. **总数 vs 实得数**：`statuses_count` 包含私密、好友圈、超可见时间窗与审核屏蔽内容，实得条数小于 `statuses_count` 属常态；数千条以上深页翻页是否存在服务端隐性上限记为 `unknown`。

<a id="empty-truth-table"></a>
### 6.5 防误判真值表：“真到底 / 真实 0 条” vs “时间窗边界” vs “未登录 / 风控空响应”

来源：`源码阅读（多站点订阅参考、仓库代码，2026-11-21）`、`社区资料（2026-11-21）`、`推测（2026-11-21）`（线上精确未登录/时间窗/空列表响应由 **V-04、V-05、V-10** 实测确认）。

| `mymblog` 响应形态 | `page` | 真实原因 | TRACE 客户端判定 |
| --- | --- | --- | --- |
| HTTP 302 跳 `login.sina.com.cn` / `passport.weibo.com` / `weibo.com/newlogin`，或 HTTP 200 返回 HTML/非 JSON，或 `ok == -100` / HTTP `401` | 任意 | 未登录、Cookie 失效或被踢回访客页 | **`auth_required`**（停本站点，不移动基线） |
| HTTP `403` / `414` / `418` / `429` / `432`、0 字节空响应、或 `ok == 0` 附验证码/挑战特征 | 任意 | 限流、WAF 或验证码风控 | **`risk_control` / `rate_limited`**（触发微博站点熔断与退避，绝不当空列表） |
| HTTP 200、`ok == 1`，缺少 `data` 或 `data.list` 缺键 / `null` | 任意 | 风控静默抽空壳响应 | **`risk_control`**（或 `UncertainEmpty = true`，绝不当 0 条或到底） |
| HTTP 200、`ok == 1`、`data.list == []` 且 **`bottom_tips_visible == true`**（含 `bottom_tips_text`） | 任意 | **到达博主可见时间窗下限**（如半年可见） | **正常结束**（`HasMore = false`，透传提示文案展示给用户） |
| HTTP 200、`ok == 1`、`data.list == []`、`since_id == ""`、无 `bottom_tips_visible` | `page > 1` | **真正翻到最后一页** | **正常结束**（`HasMore = false`） |
| HTTP 200、`ok == 1`、`data.list == []`、`since_id == ""`、无 `bottom_tips_visible` | `page == 1` | 交叉核对 `profile/info`：<br>• `blockText != ""` → 账号封禁/拉黑（`unavailable` / `forbidden`）；<br>• `statuses_count == 0` → **真实 0 条微博**；<br>• `statuses_count > 0` → 疑似风控静默限流或全锁私密 | 前两分支按实处理；第三分支置 **`UncertainEmpty = true`**，订阅检查记“本轮结果不完整”，**不移动基线** |
| HTTP 200、`ok == 1`、`data.list` 非空但 `next_since_id == req_since_id` | 任意 | 游标停滞防死循环 | 立即中止翻页并报 `schema_changed`，保留已加载页 |

<a id="sub-baseline"></a>
### 6.6 首次建基线、增量轮询与“全部缓存”代价评估

- **首次建基线（未读 = 0，复用 `updates.db`，[订阅设计 §2](../design/subscriptions.md#update-check)）**：
  1. 仅拉取 `mymblog` 第 1 页（`feature=0`）；剔除推广/外人杂卡；
  2. 将第 1 页全部置顶（及既有快转）ID 写入已见基线集合（`account_baseline_pinned`）；
  3. 取第 1 页非置顶、非快转常规本人微博的最大 `(pub_ts, mid)` 设为起点（全为置顶时取全页最大 `pub_ts`）；起点及以前的历史条目不进入 `update_item`。
- **增量轮询**：从 `page=1` 按 `since_id` 扫描，单账号单轮上限 **`2` 页**（同抖音）；置顶与快转只查重不早停；常规本人微博命中起点立即早停；新条目按内容类型映射到 `CreatorPost.Type`（`video` / `draw` / `word` / `forward` / `article` / `other`）写入 `update_item` 并推进起点。任何 `UncertainEmpty` / `risk_control` / `auth_required` 均不移动起点。
- **补更早内容（“全部缓存”，`FR-USER-004` / [DEC-061](../decisions.md#dec-061)）可行性与代价**：
  - 适配现有慢速不规律队列（单站点串行、单条间隔 `40–150 秒`、每 `15–25` 条长歇 `10–25 分钟`），沿 `mymblog` 的 `since_id` 顺序回溯，**不可突破博主可见时间窗**。
  - **关键省流优化**：`mymblog` 列表页对非长微博通常已包含完整 `text_raw`、`pic_ids`+`pic_infos` 与视频 `playback_list`，**直接复用列表响应生成缓存计划，不额外调 `statuses/show`**；仅对 `isLongText == true` 补调 `longtext` 或视频签名过期时补拉，可将 API 请求量压减约 `60%–75%`（`推测（2026-11-21）`）。
  - **规模估算**（每页 ~20 条，假设长博占比 ~25%）：`500` 条约需 ~25 次列表 + ~125 次长博补拉 ≈ **`150` 次 API**（配合每小时 100 次自动额度与慢速间隔，跨数小时～半天）；`2,000` 条约需 **`600` 次 API**（跨数天）；万条级老号深页存在较高风控或隐性截断风险（深页上限 `unknown`）。

<a id="mine"></a>
## 7. “我的”：登录账号的内容（R-F）

按 [DEC-047](../decisions.md#dec-047)，本节全部接口**严格只读**（绝不调取消关注、取消收藏、取消点赞等任何写接口），数据**仅驻留内存、绝不写本机文件或把标题/用户名写入日志**，退出微博登录即清零。

<a id="mine-following"></a>
### 7.1 我的关注、关注分组与批量导入订阅

| 功能 | PC Ajax 端点与参数 | 返回结构与翻页 | 限制与未知项 | 批量导入订阅适用性 | 证据等级 |
| --- | --- | --- | --- | --- | --- |
| **我的关注（全部）** | 端点 A：`GET https://weibo.com/ajax/profile/followContent?page=<1..N>&next_cursor=<cursor>&sortType=all`<br>端点 B：`GET https://weibo.com/ajax/friendships/friends?uid=<self_uid>&page=<1..N>`（无 `relate=fans`；加 `relate=fans` 为粉丝） | 端点 A：`{ok: 1, data: {follows: {users[], next_cursor, total_number}, specialAttention: {name, list_id, users[]}, total_number}}`<br>端点 B：`{ok: 1, users[], next_cursor, total_number}`<br>单人含 `idstr`、`screen_name`、`remark`、`avatar_large`/`profile_image_url`、`description`、`followers_count`、`verified_reason`、`special_follow` | 每页约 20～50 人；看**他人**关注受前 5 页/隐私限制，看**本人**关注通常可完整翻页（封号/异常账号被过滤导致实得数 `< total_number`）。端点 A vs B 的起始页（`0` vs `1`）及是否存在深页硬上限列 **V-13** | **完全支持批量导入 TRACE 订阅**（对称哔哩哔哩 `FR-SUBS-003`）：直接提供稳定 `uid`（`idstr`）、昵称与头像，零额外详情请求即可写入本机 `subscriptions.json` | `社区资料（2026-11-21）` |
| **我的关注分组** | 分组目录：`GET https://weibo.com/ajax/feed/allGroups?is_new_segment=1&fetch_hot=1`<br>按分组查成员：社区见 `followContent` / `friendships/friends` 传 `list_id=<gid>`（特别关注已在 `followContent.specialAttention` 中） | `allGroups` 返回 `groups[]` 含 `gid`/`list_id`、`title`、`type`（注意：移动 `m.weibo.cn/feed/group?gid=` 拉取的是分组**微博流**而非博主名单） | 按自定义 `list_id` 拉**分组成员列表**的精确端点参数与返回键在社区资料中存在分歧，记为 **`unknown`**（列 **V-13**） | 经 **V-13** 实测确认后可对齐 B 站提供“按关注分组筛选导入”；未确认前先提供全量关注列表浏览与导入 | `源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）` |

<a id="mine-favorites"></a>
### 7.2 我的收藏

- **端点与参数**：`GET https://weibo.com/ajax/favorites/all_fav?uid=<self_uid>&page=<1..N>&with_total=true`（可选收藏标签目录：`GET https://weibo.com/ajax/favorites/tags?is_show_total=1`）（`社区资料（2026-11-21）`）。
- **返回与翻页**：`{"ok": 1, "data": {"status": [<RawPost>, ...], "total_number": <int>}}`（**注意键名为 `data.status` 而非 `data.list`**，单元素可为直接 `<RawPost>` 或 `{"status": <RawPost>, ...}` 包裹对象，须优先解包内层 `status` 避免外层字段遮蔽 `idstr`）；`ok == 1` 且 `data.status == []` 为正常翻到底（`社区资料（2026-11-21）`；第 1→2 页契约与深度上限列 **V-13**）。
- **媒体与转发字段差异（2026-11 修正）**：`all_fav` 返回的微博对象及内嵌 `retweeted_status` 常省略完整 `pic_infos` 或视频 `playback_list`，解析器须按顺序覆盖：① `mix_media_info.items`（含无内联流的混排视频封面占位）；② `pic_ids` + `pic_infos`（部分缺失项回退补齐）；③ `pics` 数组或数字键映射（含 `large.url`、`videoSrc` 与 `live_photo`）；④ 无 `pic_infos` 时的 `pic_ids` / `pic_urls` / `original_pic` / `bmiddle_pic` / `thumbnail_pic`（将 `sinaimg.cn` 缩略段提升至 `/large/`，保留扩展名并强制 HTTPS）；⑤ 视频 `page_info.urls` / `media_info.{mp4_hd_mp4, hevc_mp4_hd, mp4_ld_mp4, urls}`，以及省略直链时保留 `object_type=video` 封面、标题与时长供列表渲染视频卡片（不误归为普通网页卡）。简略回退条目置 `MediaIncomplete = true`，点开详情或触发缓存下载时仍调 `statuses/show` 补抓完整原图与实况/视频流。
- **限制**：仅限登录账号本人；每页约 `20` 条；长微博与简略媒体在点开详情或缓存时按需补拉 `statuses/show` / `longtext`。

<a id="mine-likes"></a>
### 7.3 我赞过的微博（高不确定度，须实测门控）

- **候选端点**：`GET https://weibo.com/ajax/statuses/likelist?uid=<self_uid>&page=<1..N>`（社区另见 `likeList` 大小写写法；**严防混淆**：`likeList?id=<mid>` 是某条微博的**点赞人列表**而非“我赞过的微博”！移动 H5 历史容器为 `containerid=230869<uid>_-_mix`）（`社区资料（2026-11-21）`）。
- **不确定性（`unknown`，列 V-14）**：该接口缺乏 2025–2026 现代 PC Ajax 完整响应契约实证（路径大小写、参数及返回键名均未锁定）。参照抖音 `ACT-020`（“喜欢”对非浏览器固定回 0 字节、观看历史未证不上线）的教训，**在 V-14 实测通过前不上线“我的 › 微博 › 赞过”入口**；若 V-14 返回 404/403/空壳/非预期结构直接判 `unavailable` 停止且不触发整站退避，记录至 LOOP（`推测（2026-11-21）`）。

<a id="live"></a>
## 8. 直播与录制器复用评估（R-G）

<a id="live-links"></a>
### 8.1 直播链接形态与 `live_id <-> uid` 的结构性差异

1. **已知链接形态**（全部为合成示例）：
   - **PC 网页直播间**：`https://weibo.com/l/wblive/p/show/<live_id>`，其中 `<live_id>` 常见为 `1022:2321325000000000000001`（`1022:232132` + 16 位数字）或 `1042152:<32位十六进制>`（冒号可能编码为 `%3A`）（`源码阅读（直播录制参考、单链接解析参考，2026-11-21）`、`社区资料（2026-11-21）`）。
   - **移动网页直播间**：`https://weibo.com/l/wblive/m/show/<live_id>`（`社区资料（2026-11-21）`）。
   - **历史 / 独立直播域名**：`https://live.weibo.com/show?id=<live_id>`、`https://live.media.weibo.com/...`、`https://zhibo.sina.com.cn/...`（当前线上是否仍生效或 302 跳至 `weibo.com/l/wblive/...` 为 `unknown`，列 **V-16**）（`社区资料（2026-11-21）`）。
   - **博文内嵌直播卡片**：`mymblog` / `statuses/show` 中 `page_info.object_type == "live"`（或 `media_info.live_status == 1`），携带 `page_info.object_id`（即 `<live_id>`）及 `page_info.media_info.{live_status, live_ld, live_hd, replay_hd, stream_url}`（`社区资料（2026-11-21）`）。
   - **App Scheme**：`sinaweibo://chatroom?container_id=<live_id>&live_id=<live_id>&anchor_uid=<uid>&livetype=wblive`（`社区资料（2026-11-21）`）。
2. **`live_id <-> uid` 映射关系（与 B 站、抖音的核心结构差异）**：
   - **`live_id` 是单场次对象 ID，不是主播永久房间号**：与 B 站固定 `room_id`、抖音可缓存 `short_id` / `web_rid` 不同，微博每次开播都会分配新的单场 `live_id`（`源码阅读（直播录制参考，2026-11-21）`、`社区资料（2026-11-21）`）。
   - **正向映射（`live_id -> uid`）**：调用房间详情接口可拿到主播永久数字 `uid`（`show_pc_live.json` 为 `data.user.uid`、`data.user.screenName`、`data.user.avatar`；`l/pc/anchor/live` 为 `data.user_info.name` 及疑似 `id`/`anchor_uid`，精确键名列 **V-17**）。
   - **反向发现（已订阅 `uid -> 是否开播 & 当前 live_id`）的致命局限**：
     1. **无已知按 `uid` 批量查开播的 Web 接口**，也没有固定房间号可直查（是否存在未公开批量接口为 `unknown`）（`社区资料（2026-11-21）`）。
     2. **社区现行做法为“两跳时间线嗅探”**：先逐个 `uid` 请求 `mymblog?uid=<uid>&page=1&feature=0` 扫描 `page_info.object_type == "live"` 提取 `object_id`，再调房间接口核验 `status == 1`（下播后微博卡片仍保留并转回放）（`社区资料（2026-11-21）`）。每次巡检都要拉整页 `mymblog`，额度与风控成本显著高于 B 站（50 人/次）与抖音（`web/enter` 单次轻量请求）。
     3. 若主播开播未发微博、仅粉丝可见或删除了开播提示微博，时间线嗅探必然**漏检**（`推测（2026-11-21）`、`unknown`）。
     4. `profile/info` 是否稳定暴露实时 `live_status` / 当前 `live_id` 为 **`unknown`**（列 **V-16**）；且社区 2025–2026 公开反馈多次记录高频经 `mymblog` 监控直播触发空体/缺 `data` 拦截（`社区资料（2026-11-21）`）。

<a id="live-apis"></a>
### 8.2 开播状态查询与取流接口对照

| 接口 | 方法与路径 | 请求头与凭证要求 | 已知响应路径与状态字段 | 证据等级与未知项 |
| --- | --- | --- | --- | --- |
| **A. PC 直播间 JSON（旧 `wblive` 路径）** | `GET https://weibo.com/l/!/2/wblive/room/show_pc_live.json?live_id=<live_id>` | 桌面 UA、`Referer: https://weibo.com/l/wblive/p/show/<live_id>`；防风控与受限房需登录 `SUB`/`SUBP`/`WBPSESS` | **信封存在版本分歧**：直播录制参考判 `error_code == 0`，读 `data.status == "1"`、`data.title`、`data.user.screenName`、`data.live_origin_flv_url`；单链接解析参考判 `code == 100000`，读 `data.user.{screenName,uid,avatar}`、`data.cover`、`data.live_origin_flv_url`、`data.live_origin_hls_url`、`data.replay_origin_url` | `源码阅读（直播录制参考、单链接解析参考，2026-11-21）`；线上信封与枚举 `unknown`（**V-17**） |
| **B. PC 主播直播 JSON（较新路径）** | `GET https://weibo.com/l/pc/anchor/live?live_id=<live_id>` | 桌面 UA、`Referer: https://weibo.com/...`、登录 Cookie（含 `SUB`、`SUBP`、`XSRF-TOKEN`、`WBPSESS`） | `data.user_info.name`、`data.item.status`（`1`=直播中）、`data.item.desc`（标题）、`data.item.stream_info.pull.live_origin_flv_url`、`data.item.stream_info.pull.live_origin_hls_url` | `社区资料（2026-11-21）`（多平台录制工具于 2025-08 从 A 切至 B）；是否含 `uid`/开播时间及与 A 谁更稳 `unknown`（**V-17**） |
| **C. 主页时间线卡片嗅探** | `GET https://weibo.com/ajax/statuses/mymblog?uid=<uid>&page=1&feature=0` | 标准 PC Ajax 头 + 登录 Cookie | `data.list[].page_info`（`object_type == "live"` → `object_id`、`media_info.{live_status, live_ld, replay_hd}`） | `社区资料（2026-11-21）`；`live_status` 实时性 `unknown`（**V-16**） |
| **D. 出站跳转 / 回放组件** | `GET https://wblive-out.api.weibo.com/2/wblive/room/play?id=<live_id>`；`POST https://weibo.com/tv/api/component`（`oid="1022:..."`） | 桌面 UA、`Referer`、Cookie | `wblive-out` 常 302 至 CDN `.m3u8`/`.flv`；`tv/api/component` 可解析部分已结束 `1022:` 回放为点播 | `源码阅读（单链接解析参考，2026-11-21）`、`社区资料（2026-11-21）`；直播与回放边界 `unknown`（**V-17**） |
| **E. `m.weibo.cn` 直播** | 未见独立稳定的 `m.weibo.cn` 直播取流端点（仅 `container/getIndex` 卡片） | 移动 H5 头 | 同卡片摘要，无原画取流契约 | `源码阅读（多站点订阅参考，2026-11-21）`、`unknown` |

<a id="live-streams"></a>
### 8.3 取流形态、清晰度、CDN 协议与免 Cookie 风险

1. **流封装与编码**：
   - **HTTP-FLV（`.flv`）**：接口 A/B 均暴露 `live_origin_flv_url`（`源码阅读（直播录制参考，2026-11-21）`、`社区资料（2026-11-21）`），但线上并非所有直播间都保证非空 FLV，实际覆盖率为 **`unknown`**（**V-18**）。
   - **HLS（`.m3u8`）**：暴露 `live_origin_hls_url`；**关键 `unknown`**：分片究竟是 **MPEG-TS（`.ts`）** 还是 **fMP4（`.m4s` + `#EXT-X-MAP`）** 无 2026-11 线上回执，社区工具以保存 `.ts` 及传统云直播分发为主，高度疑似 **HLS-TS**（`社区资料（2026-11-21）`、`推测（2026-11-21）`，列 **V-18**）。
   - **RTMP**：Web 接口不提供 RTMP。
   - **编码**：路径命名 `_wb<清晰度>avc.flv` / `_wb1080avc.m3u8` 表明主流为 **H.264/AVC**；是否含 HEVC（`CodecID=12` / Enhanced FLV `hvc1`）与音频是否恒为 AAC 为 **`unknown`**（**V-19**）。
2. **清晰度选择**：`live_origin_flv_url` / `live_origin_hls_url` 为主档；社区存在替换 `_wb[\d]+avc\.flv` 或截断 `_` 拼回后缀的做法（`源码阅读（直播录制参考，2026-11-21）`、`社区资料（2026-11-21）`），但若 URL 带路径签名（如 `auth_key`/`txSecret`/`wsSecret`），改写文件名将破坏签名直接 `403`（列 **V-18**）。
3. **CDN 域名、HTTPS/443 与 Cookie 冲突风险**：
   - 已知域名线索：`*.live.weibo.com`（如 `plwb02.live.weibo.com`、`plmedia.live.weibo.com`）、`live.video.weibocdn.com`、`*.weibocdn.com`、`*.sinaedge.com` 及第三方云直播 CDN 调度节点（`源码阅读（单链接解析参考，2026-11-21）`、`社区资料（2026-11-21）`）。
   - **HTTP/80 明文风险**：社区与参考夹具中多次出现明文 `http://` 或 `:80` 端口流地址，而 TRACE 的 `MediaTransportSpec.ValidateURL` 严格拒绝非 HTTPS 与非 443 端口；线上是否原生支持 HTTPS/443 以及是否 302 调度到白名单外厂商域为 **`unknown`**（**V-19**）。
   - **免 Cookie 红线风险**：TRACE 强制媒体与直播流 CDN **不带登录 Cookie**；微博直播 CDN 在剥离 Cookie、仅带签名与 `Referer: https://weibo.com/` 时能否 `200 OK` 拉流为 **`unknown`**（**V-19**）。

<a id="live-recorder-fit"></a>
### 8.4 与现有录制器（`internal/live`）兼容性与延后建议

依据[直播设计](../design/live.md)与仓库 `internal/live` 源码（`源码阅读（仓库代码，2026-11-21）`）：

| 维度 | TRACE 现有录制器能力 | 与微博直播的匹配度与工程增量 |
| --- | --- | --- |
| **HTTP-FLV 内核** | `OpenHTTPFLVStream` + `FLVRemuxer` + `runSessionLoop` 已与站点解耦（经 `MediaTransportSpec` 注入白名单、`PublicRedirects` 与去 Cookie），支持 AVC `CodecID=7`、国内 HEVC `CodecID=12`、Enhanced FLV `hvc1`/`avc1`、AAC、按 GOP 写 fMP4 `.mp4.part` + `fsync` + 后台 `Defragment` + 页内预览 + 5 类异常 | 若 **V-18 / V-19** 证实稳定提供 HTTPS/443 且免 Cookie 的 HTTP-FLV（AVC/HEVC + AAC），**录制内核 100% 可复用，0 新增 Go 依赖** |
| **HLS fMP4 / HLS-TS** | `internal/live/hls.go` 强制 `#EXT-X-MAP` 且耦合 `*bilibili.Client`；**HLS-TS 未实现（`LOOP-008`）** | 若微博直播大量仅提供 HLS-TS，须触发 `LOOP-008`（引入 `bluenviron/mediacommon/v2`，`~+2.1 MiB`，`+400–550 LOC`） |
| **多站点管理层** | `internal/live/manager.go`、`site_provider.go`、`douyin_*.go` 当前硬编码 B 站与抖音双分支 | 即使仅做 HTTP-FLV，也需把 `Manager` 重构为按 `site.ID` 泛化或新增微博状态机，估算需 **`+900–1,400` 生产 LOC + `~1,000` 测试 LOC** |
| **自动开播巡检** | B 站 50 UID 批量；抖音 `web_rid` + `profile/other` + `check_alive` | 微博无固定房间号与批量接口，高频拉 `mymblog` 会放大整站风控风险并挤占订阅额度 |

**明确建议：微博 MVP 暂不做微博直播录制与自动开播巡检，延后至后续 loop（[§12.3](#q-drafts) `Q-WB-2`）**。MVP 仅需低成本做好两件事：① 链接解析器识别 `weibo.com/l/wblive/...`、`live.weibo.com/...` 并返回 `Kind: "live", Unsupported: true` 引导浏览器打开；② 微博正文与主页中的直播卡片安全降级展示标题/封面/提示与浏览器打开入口。待后续完成 **V-16～V-19** 实测确认 HTTPS/443 + 免 Cookie HTTP-FLV 稳定后，再在独立 loop 按“单链接手动 FLV 录制 → 视轻量开播字段情况决定是否加自动巡检”推进。

<a id="errors"></a>
## 9. 错误、风控与请求节奏（R-H）

<a id="error-envelope"></a>
### 9.1 响应信封与高频拦截 HTTP 状态码

1. **响应信封差异**：
   - **PC `weibo.com/ajax/*`**：通常为 `{"ok": 1, "data": {...}}`；**特例**：`/ajax/statuses/show` 成功时微博字段平铺在顶层（`{"ok": 1, "idstr": "...", "mblogid": "...", "text_raw": "...", ...}`，无 `data` 包装）；失败返回 `{"ok": 0, "msg": "...", "error_type": "toast"|"link", "url": "...", "error_code": <int>, "errno": <int>}` 或 `{"ok": -100, "msg": "...", "url": "https://passport.weibo.com/..."}`（`社区资料（2026-11-21）`）。
   - **移动 `m.weibo.cn/*`**：成功 `{"ok": 1, "data": {...}}`（注意 `/api/config` 未登录也是 `ok: 1` 且 `data.login: false`），失败 `ok: 0` 或 `-100`（`源码阅读（多站点订阅参考，2026-11-21）`）。
   - **视频组件 / 直播接口**：`tv/api/component` 成功为字符串 `code == "100000"`；`show_pc_live.json` 见整型 `code == 100000` 或 `error_code == 0`（`源码阅读（单链接解析参考、直播录制参考，2026-11-21）`）。
2. **高频异常 HTTP 状态码与假 200 陷阱**（`源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）`）：
   - **`HTTP 301 / 302`**（API 客户端必须禁用自动跳转）：`Location` 指向 `passport.weibo.com/visitor/visitor`、`visitor.passport.weibo.cn`、`weibo.com/newlogin`、`login.sina.com.cn`。
   - **`HTTP 414`**：微博网关极具代表性的**自定义频控/限流拦截码**（并非仅 RFC 的 URI Too Long，常见于短时间连续拉列表）。
   - **`HTTP 418`**：反爬指纹或突发高频拦截（常见于 UA/请求头缺失或异常节律）。
   - **`HTTP 432`**：微博自定义**高危风控 / 会话异常 / 极验（Geetest）滑块拦截码**（`weibo.com` 与 `m.weibo.cn` 均高频出现）。
   - **`HTTP 429`**：标准频率限制。
   - **`HTTP 400 / 401 / 403`**：`400` 常见参数缺失（如 `profile/info` 缺 `uid`）；`401` 未登录；`403` 可为未登录、CSRF（`X-XSRF-TOKEN` 不匹配）、WAF 拦截或 `sinaimg.cn` 防盗链。
   - **`HTTP 200` 假成功陷阱**：返回 `text/html`（`<title>Sina Visitor System</title>`、登录页、极验滑块页）或 `0` 字节空体，绝不可当空列表或简单判为 `schema_changed`。

<a id="error-mapping"></a>
### 9.2 到 TRACE 错误分类的映射表（对齐 `internal/site/errors.go`）

| HTTP 状态 / 响应信封特征 | 典型含义 | TRACE 错误分类（`site.ErrKind*`） | 调度器动作（`IsRoundStop` / `IsCircuitBreak`） | 证据等级 |
| --- | --- | --- | --- | --- |
| HTTP 200，`ok == 1`（或视频/直播 `code == "100000"`/`100000`），核心载荷完整 | 正常成功 | — | 正常解析 | `源码阅读（多站点订阅参考、单链接解析参考，2026-11-21）` |
| HTTP `401`；`ok == -100`（尤其 `url` 含 `login`/`passport` 且非滑块）；API 响应 `302` 跳 `weibo.com/newlogin` 或 `login.sina.com.cn`；`m.weibo.cn/api/config` 返回 `data.login == false` | 未登录或 `SUB` 失效 | **`auth_required`** | **停止本轮**（`IsRoundStop=true`），**不推进退避**（`IsCircuitBreak=false`），提示重新登录；不清除因其他错误失败的凭证 | `源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）` |
| HTTP `432`；HTTP `418`；HTTP `403`（WAF/非业务权限）；已带登录 Cookie 仍被 `302` 踢回 `passport.weibo.com/visitor/visitor` 或 `visitor.passport.weibo.cn`；HTTP `200` 返回 HTML 验证码/极验/滑块/`Sina Visitor System` 页或 `0` 字节空体；JSON 含 `geetest`/`captcha`/验证跳转 URL | 风控、指纹拦截或验证码挑战 | **`risk_control`** | **立即停止本轮并触发微博站点熔断与指数退避**（`IsRoundStop=true, IsCircuitBreak=true`），**零自动重试，严禁触碰或解算任何验证码**；不清空用户已存凭证 | `源码阅读（多站点订阅参考，2026-11-21）`、`社区资料（2026-11-21）` |
| HTTP `414`（非 URI 过长导致的网关限流）；HTTP `429`；`ok == 0` 且 `msg` 含“频繁/稍后再试/请求过多” | 账号或 IP 频控 | **`rate_limited`** | **立即停止本轮并触发微博站点熔断与退避**（`IsRoundStop=true, IsCircuitBreak=true`），零自动重试 | `社区资料（2026-11-21）` |
| `ok == 0` 且 `error_code`/`errno` 为 `20003`（用户不存在）、`20101`（微博不存在/已删除），或 `msg` 含“已被作者删除 / 不存在 / 已注销 / 暂不可查看” | 目标不存在、已删除或不可得 | **`unavailable`** | 展示具体中文原因，**不触发站点熔断或退避**（PC Ajax 精确码列 **V-10**） | `社区资料（2026-11-21）` / 部分 `unknown` |
| `ok == 0` 且 `error_code` 为 `20112`，或 `msg` 含“仅粉丝可见 / 好友圈 / 仅博主自己可见 / 无权查看 / 关注后可见” | 权限范围受限 | **`forbidden`** | 展示具体可见范围原因，**不触发站点熔断或退避**（PC Ajax 精确码列 **V-10**） | `社区资料（2026-11-21）` / 部分 `unknown` |
| HTTP `400`（参数无效）；本地或跳转命中不支持链接/类型 | 参数错误或暂不支持 | **`bad_request`** / **`unsupported_link`**（`Unsupported=true`） | 本地提示，不重试 | `社区资料（2026-11-21）` |
| 短链跳往非白名单域、`http://`、私有 IP、超 5 跳 | 安全越界拦截 | **`blocked`** | 立即中止跳转 | `源码阅读（仓库代码，2026-11-21）`、`推测（2026-11-21）` |
| HTTP 200 合法 JSON 但核心字段缺位/类型变化、返回 `uid`/`mid` 与请求不符、游标停滞 | 站点接口改版 | **`schema_changed`** | 报格式变化，不重试 | `源码阅读（仓库代码，2026-11-21）`、`推测（2026-11-21）` |
| DNS / TCP / TLS / 超时 / 连接重置 / HTTP `5xx` | 瞬态网络故障 | **`network`** | API 交由用户手动重试；媒体下载按 Range 有限次续传 | `源码阅读（仓库代码，2026-11-21）`、`推测（2026-11-21）` |

<a id="pacing"></a>
### 9.3 请求节奏、配额、退避与熔断建议（对齐按站点调度器）

对照仓库现有按站点调度常量（`internal/subscription/migrations.go`、`round.go`、`internal/site/douyin/client.go`，`源码阅读（仓库代码，2026-11-21）`）：

| 参数维度 | 哔哩哔哩（现行） | 抖音（现行） | **微博（建议默认值）** | 依据与说明 |
| --- | --- | --- | --- | --- |
| **API 并发度** | 全局串行（`1`） | 站点串行（`1`） | **站点串行（`1`）** | 避免突发并发触发微博 `414` / `418` / `432` |
| **API 最小请求间隔** | `500 ms` | `1000 ms + 0～300 ms` 抖动 | **`1200 ms + 0～400 ms` 随机抖动** | 微博列表/详情对亚秒级固定间隔敏感（`社区资料（2026-11-21）`）；1.2～1.6 秒桌面体感流畅且明显降低频控概率（绝不低于 `1000 ms`） |
| **单次 API 超时 / 响应体上限** | `15 s` / `4 MiB` | `15 s` / `4 MiB` | **`15 s` / `4 MiB`**（短链 `10 s`/跳，最多 `5` 跳） | 与现有双站点安全边界一致 |
| **每小时自动请求硬上限**（`SiteHourlyQuotaLimit`） | `200` 次/小时 | `120` 次/小时 | **`100` 次/小时**（`DefaultWeiboHourlyQuota = 100`） | 长微博可能额外触发 `longtext` 请求，且账号级 `414`/`432` 阈值偏紧，取更保守上限（媒体 CDN 下载不计入 API 额度，仍由下载并发上限 2 独立控制） |
| **定时检查单轮账号上限**（`SiteScheduledRoundCap`） | `30` 个/轮 | `20` 个/轮 | **`20` 个/轮**（`DefaultWeiboRoundAccountCap = 20`） | 与抖音一致；每账号常规仅拉第 1 页（最多 `2` 页），翻页仅由用户手动滚动触发，禁止后台预取 |
| **账号间检查间隔**（`SiteAccountInterval`） | `2000 ms` | `2500 ms` | **`3000 ms`**（`DefaultWeiboAccountInterval = 3s`） | 降低连续多账号拉取 `mymblog` 撞上 `414` 的概率 |
| **退避与熔断隔离**（`site_scheduler_state`） | `site="bilibili"` 独立行 | `site="douyin"` 独立行 | **`site="weibo"` 独立行** | 复用 `RecordSiteAutomaticStop` 与 `download.Manager.pauseSitePeers("weibo")`：`risk_control`/`rate_limited` 仅熔断微博后台任务（定时检查、自动缓存、全部缓存队列、微博排队下载），按 `min(IntervalMinutes * 2^min(level, 10), 24h)` 指数退避，**与 B 站、抖音完全隔离互不连坐**；`auth_required` 终止本轮并标失效、不推进退避等级 |

<a id="oss"></a>
## 10. 开源选型与自研规模评估（R-I）

按 [M6 阶段计划修订 0 §3](https://github.com/chialecode/trace/blob/b75cf80/docs/delivery/stage-plan.md#research-delivery) 与[来源与安全 §1](../dev-rules/sources-and-safety.md#open-source)，因 Go 生态中**无**值得对比引入的第三方微博库（0 个满足维护、许可合规与可导入条件），不另建 `docs/research/2026-11-21-weibo.md`，将完整检索记录、候选排除表、零依赖结论、自研规模估算与 `LOOP-008` 复核统一记于本节。

<a id="oss-search"></a>
### 10.1 Go 生态微博库系统检索记录（查询日期：2026-11-21）

**检索渠道**：① GitHub REST Search API（`api.github.com/search/repositories`，2026-11-21）；② `pkg.go.dev`（公开检索 `site:pkg.go.dev`，2026-11-21）；③ Go 官方模块代理（`https://proxy.golang.org/<module>/@latest` 与 `@v/list`，2026-11-21）。

| 渠道与检索词 | 命中总数 | 结果概况（`官方文档（2026-11-21）`） |
| --- | --- | --- |
| GitHub `weibo language:Go`（按 `updated` 与 `stars` 各查前 10～15） | `140` | 多为热搜榜单、旧 OpenAPI SDK、超话签到 CLI、爬虫应用或非微博同名仓库 |
| GitHub `weibo login language:Go` | `5` | 3 个第三方网站 OAuth2 聚合登录（2019–2023）、1 个无许可停维、1 个 2020 停维无登录包 |
| GitHub `weibo qrcode language:Go` | `0` | 无 Go 微博扫码登录库 |
| GitHub `weibo live language:Go` | `0` | 无 Go 微博直播库 |
| GitHub `mblogid language:Go` | `0` | 无 Go `mid/mblogid` 专用库 |
| GitHub `weibo cookie language:Go` | `0` | 无 Go 微博 Cookie 工具库 |
| GitHub `weibo sign language:Go` | `1` | `rxrw/signup-weibo`（2021 归档，无许可，超话签到） |
| GitHub `sina weibo language:Go` | `10` | 全为 2013–2020 历史 OpenAPI / 爬虫或无许可仓库 |
| `site:pkg.go.dev weibo` 及 `"mblogid" OR "wblive" OR "weibo" "qr" OR "base62"` | — | 命中 `berbai/weibo`、`axiaoxin-com/weibo`、`larrylv/go-weibo`、`bububa/biz-weibo`、`codeGROOVE-dev/sociopath` 及通用 Base62 库 |

**具名候选逐项审查与排除表**（2026-11-21 实查 GitHub API + `proxy.golang.org` + `go.mod`，`官方文档`）：

| 候选模块 / 仓库 | 最新版本与最后推送 | 许可 | 实际定位 | 排除依据 |
| --- | --- | --- | --- | --- |
| [`github.com/berbai/weibo`](https://github.com/berbai/weibo) | `v1.3.2`（`2024-11-02`），2 stars | Apache-2.0 | 个人推文监测工具（`statuses/show`、`mymblog`、`longtext`） | 停更 `> 2` 年；缺 Cookie 清洗、URL 解析、视频、实况、风控分类；根包直引 `database/sql`，`go.mod` 强拉 MySQL 驱动、`cron`、`cli` 等重依赖 |
| [`github.com/axiaoxin-com/weibo`](https://github.com/axiaoxin-com/weibo) | `v1.0.7`（`2020-11-23`），26 stars | MIT | 2020 年旧 OpenAPI + HTML 抓取 | 停维 6 年（Go 1.13）；依赖 `goquery` 及第三方打码平台（违反 TRACE 禁止绕过验证码红线） |
| [`github.com/wujunwei928/parse-video`](https://github.com/wujunwei928/parse-video) | 代理 `v0.0.2`（`2026-04-18`），推送 `2026-11-08`，1103 stars | MIT | 多平台短视频去水印 Web 服务 / CLI（微博约 220 行） | 仅覆盖单视频与基础 `m.weibo.cn` 图，**无登录、无用户主页翻页、无长文/转发/实况/订阅/风控分类**，从全局环境变量读 Cookie；`go.mod` 拖入 `gin`、`cobra`、`goquery`、`resty`、`quic-go`、`sonic` 等几十个重依赖 |
| [`github.com/SurrealGit/weibo-exp`](https://github.com/SurrealGit/weibo-exp) | `v0.9.0`（`2026-11-20`），8 stars | MIT | 超话自动签到/评论/转发删帖 CLI 应用 | **不可作为 Go 库导入**：核心代码全部在 `internal/weibo/` 下（Go 编译器禁止外部导入）；定位为自动化写操作，与 TRACE 只读归档相反 |
| [`github.com/yilong-musk/feedmesh`](https://github.com/yilong-musk/feedmesh) | `v0.1.2`（`2026-11-18`），1 star | MIT | 自托管 RSS 桥接 CLI | **不可作为 Go 库导入**：全部代码位于 `internal/weibo/`；体量极小（1 star） |
| [`github.com/yafengio/weibogo`](https://github.com/yafengio/weibogo) | 伪版本 `2025-07-23`，15 stars | **无许可（NONE）** | 官方 `open.weibo.com` OAuth SDK（Go 1.12） | **无许可证禁止引入**；且官方 OpenAPI 不向个人桌面应用开放通用主页/媒体/订阅能力 |
| [`github.com/bububa/biz-weibo`](https://github.com/bububa/biz-weibo) | `v1.1.0`（`2024-05-03`），3 stars | Apache-2.0 | 微博广告商业推广 API SDK | 与个人内容浏览、缓存、订阅无关 |
| [`github.com/codeGROOVE-dev/sociopath`](https://github.com/codeGROOVE-dev/sociopath) | `v0.9.23`（`2025-12-19`），3 stars | Apache-2.0 | 多站点 OSINT 社交账号探测器 | 非微博内容客户端 |
| [`github.com/larrylv/go-weibo`](https://github.com/larrylv/go-weibo) / `xiocode/weigo` / `despcy/weibo-go` / `Privilege-privacy/Weibo-To-Telegram` | `2014-08` / `2015-11` / `2020-02` / `2023-09` | MIT / NONE / MIT / MIT | 历史 OpenAPI SDK、无登录旧包或 Telegram 转发机器人 | 均长期停维（3～12 年）或无许可或为独立应用 |
| 通用 Base62 库（`jcoene/go-base62`、`deatil/go-encoding/base62`、`inovacc/base62`） | — | — | 通用整数/字节流 Base62 | **算法不匹配**：微博 `mid <-> mblogid` 是右对齐 7 位/4 字符分段换算而非大整数 Base62；标准库自写仅约 70～90 行 |

- **检索结论**：Go 生态中满足“持续维护 + 许可合规 + 可导入库 + 覆盖微博网页端核心协议”的第三方库为 **0 个**。

<a id="zero-new-deps"></a>
### 10.2 零新增依赖结论与自研模块规模估算

**微博 MVP 需要新增 `0` 个 Go 依赖（`go.mod` 零增量）**（`源码阅读（仓库代码，2026-11-21）`）：

- HTTP、Cookie、JSON、URL、HTML 清洗、Base62 换算全部由 **Go 标准库**完成，天然满足 `tools/check` 对 `internal/site` 仅限标准库、站点包隔离以及 `cmd/trace-server` `CGO_ENABLED=0` Linux 交叉编译门禁。
- 图片解码复用已有 `golang.org/x/image v0.48.0` 与标准库 `image/*`；MP4 校验与 `moov` 前置重排复用已有 `github.com/Eyevinn/mp4ff v0.63.0`；若未来启用扫码登录，仓库已直接引入 `github.com/yeqown/go-qrcode/v2 v2.3.0`。

对照仓库双站点体量（`internal/site/douyin` 生产 `6,324` LOC / 测试 `3,226` LOC，其中 `sign` `807` LOC、`live` `1,075` LOC；`internal/site/bilibili` 生产 `5,331` LOC / 测试 `2,085` LOC，`源码阅读（仓库代码，2026-11-21）`），估算 `internal/site/weibo` 自研规模如下（`推测（2026-11-21）`）：

| 自研模块 | 核心职责 | 预估生产 LOC | 预估测试 LOC |
| --- | --- | --- | --- |
| `base62.go` | `mid <-> mblogid` 7 位/4 字符分段 Base62 编解码与越界校验 | `70–90` | `100` |
| `auth.go` | 粘贴 Cookie 白名单清洗、`SUB` 预检、无 Cookie 对照严格登录校验、敏感字段脱敏表 | `220–280` | `200` |
| `auth_qr.go`（可选，建议 loop） | 网页扫码登录（取码/轮询/SSO 跳转链，复用 `go-qrcode/v2`）；MVP 仅粘贴 Cookie 时为 `0` | `250–350` | `200` |
| `resolve.go` | 全形态链接与分享文本解析、`/n/` 与个性域名换 `uid`、`t.cn` ≤5 跳无 Cookie 展开、`sinaurl` 解包、不支持拒绝表 | `450–600` | `350` |
| `client.go` + `errors.go` | HTTP 客户端、必需请求头、响应信封与 `414`/`418`/`432`/登录跳转/访客页拦截分类、站点节流与熔断 | `650–850` | `400` |
| `post.go` | 单条微博详情、长微博全文、转发与原博已删降级、`>9` 图/原图/GIF/实况/混排、视频 `playback_list` 与 `tv/api/component`、卡片降级与可见范围 | `750–950` | `450` |
| `user.go` | 用户资料、`mymblog` 分页（`since_id`+`page`）、置顶与快转处理、半年可见提示、防风控空壳真值表 | `400–550` | `300` |
| `account.go`（“我的”） | 登录账号关注（含分组）、收藏、赞过（实测门控）只读列表 | `350–450` | `250` |
| `adapter.go` | 实现 `site.Resolver`、`DownloadPlanner`、`SubscriptionFetcher` 与 `WeiboMediaTransport` | `250–320` | `180` |
| **合计（`internal/site/weibo`，不含直播与扫码，含“我的”）** | 省去抖音签名机（`~807` LOC）与直播（`~1,075` LOC） | **`~3,150–4,100`** | **`~2,200`** |
| *跨包接入增量* | `internal/site`、`credential`、`download`、`subscription`、`service`、`ui`、`remoteserver` 接线 | `~1,200–1,800` | `~900` |
| *若叠加微博 HTTP-FLV 直播（不推荐 MVP）* | `weibo/live.go` + `internal/live` 多站点泛化 + UI | `+900–1,400` | `+900` |
| *若叠加 HLS-TS（`LOOP-008`）* | 引入 `bluenviron/mediacommon/v2` + `internal/live/ts.go` + 通用 HLS 轮询器 | `+400–550` | `+350` |

<a id="loop-008"></a>
### 10.3 `LOOP-008`（HLS-TS 解复用）候选 2026-11-21 状态复核

对照[交付状态 LOOP-008](https://github.com/chialecode/trace/blob/f8af16863dbe885cfd63de048730c07a2b91e7be/docs/delivery/status.md#loop) 与[抖音选型 §3.1.3](2026-11-12-douyin.md#flv-ts-mp4ff)，于 **2026-11-21** 经 `proxy.golang.org` 与 GitHub REST API 复核最新状态（`官方文档（2026-11-21）`）：

| 模块 | 2026-11-12 评估版本 | 2026-11-21 最新稳定版与发布时间 | 维护与许可 | 集成影响与结论 |
| --- | --- | --- | --- | --- |
| [`github.com/bluenviron/mediacommon/v2`](https://github.com/bluenviron/mediacommon) | `v2.9.7`（`2026-10-25`） | **`v2.10.0`**（`2026-11-15T12:57:13Z`；**未满 7 天，2026-11-22 满 7 天**，[DEC-048](../decisions.md#dec-048)） | 未归档，极活跃（最新提交 `2026-11-20`，106 stars）；**MIT** | `v2.10.0` `go.mod` 要求 `go 1.26.0`（兼容仓库 `go 1.27.4`），直接依赖 `asticode/go-astits v1.17.0`（MIT）、`abema/go-mp4 v1.7.4`（MIT）及间接 `asticode/go-astikit v0.30.0`（MIT）。仍是**唯一推荐的纯 Go HLS-TS 解复用候选**（`CGO_ENABLED=0` 兼容，二进制增量约 `+2.10 MiB`） |
| [`github.com/asticode/go-astits`](https://github.com/asticode/go-astits) | `v1.17.0`（`2026-10-29`） | **`v1.17.0`**（未变） | 未归档，活跃（最新提交 `2026-11-02`，617 stars）；**MIT** | 仅拆 188B TS/PES，不含 Annex-B AU 组装、ADTS 拆帧、B 帧 DTS 推导与 33-bit 回绕；不单独选用（由 `mediacommon/v2` 封装） |
| [`github.com/Eyevinn/mp4ff`](https://github.com/Eyevinn/mp4ff)（已有） | `v0.62.0` | **`v0.63.0`**（`2026-11-13`，仓库已升级） | 未归档，极活跃（最新提交 `2026-11-20`，669 stars）；**MIT** | 负责 fMP4 分片与 `Defragment` 收尾，继续复用 |

**结论**：因建议微博 MVP 延后直播，`LOOP-008` 继续延后、暂不引入 `mediacommon/v2`；未来若抖音或微博实测确需录制纯 HLS-TS 直播间，在 `v2.10.0+` 满 7 天冷却期后一次为全站补齐 HLS-TS 录制输入。

<a id="verify-list"></a>
## 11. 待真实验证清单与最小有界检查程序

<a id="v-items"></a>
### 11.1 待真实验证项清单（`V-01`～`V-19`）

以下汇总全部无法仅靠静态调研锁定的 `unknown` 事项，供 Agent A 在修订 1 中整理为 `USER-ACTIONS.md` 的授权检查（ACT 项）。**分为两类**：`V-01`～`V-14` 属于 **MVP 核心必验项**；`V-15`～`V-19` 属于 **条件验证项（仅当 Q 项决定纳入扫码登录或直播录制时执行）**。

#### A. MVP 核心必验项（`V-01`～`V-14`）

| 编号 | 待验证问题（当前为 `unknown` 或存在社区分歧） | 目标类型 | 建议请求额度 | 对应章节 |
| --- | --- | --- | --- | --- |
| **V-01** | **严格登录校验、无 Cookie 对照、Cookie 清洗最小子集与请求头**：验证 `weibo.com/ajax/setting/getBasicInfo` 与 `m.weibo.cn/api/config` 在“无 Cookie”和“带清洗后粘贴 Cookie（含 `SUB`/`SUBP`/`XSRF-TOKEN`，验证 `SSOLoginState` 留/去与 `X-XSRF-TOKEN` 于 GET 必选性）”下的精确状态码、`ok`、字段路径（`uid`/`screen_name`/`login`/`st`），以及 `profile/info?uid=<self>` 闭环 | 当前登录测试账号（只读） | JSON API `<= 5` 次（无 Cookie 2 + 登录 3） | [§2.2](#cookie-clean)、[§2.4](#login-verify)、[§2.6](#domain-compat)、[§2.7](#headers) |
| **V-02** | **`t.cn` 短链跳转与当前版 App 分享格式**：验证 `https://t.cn/<合成码>`（含 `http://t.cn/` 本地升 HTTPS）无 Cookie 下 `302` 跳数、`Location` 落点与 `weibo.cn/sinaurl` 行为；核对 1～2 条当前版微博 App 分享文本/链接（`m.weibo.cn/status`、`share.api.weibo.cn`、`t.cn`）是否命中本地正则 | 1 个用户提供的公开微博 `t.cn` 短链 + 1～2 条分享文本 | 短链头检 `<= 3` 跳（无 Cookie，不拉正文） | [§3.1](#url-forms)、[§3.4](#url-priority) |
| **V-03** | **昵称 `/n/` 与个性域名反查稳定 `uid`**：验证 `weibo.com/ajax/profile/info?screen_name=<昵称>` 与 `?custom=<个性域名>` 登录态下返回 `data.user.idstr`（及 `/p/100505`、`/p/100606` 一致性） | 1 个公开昵称 + 1 个个性域名博主 | JSON API `<= 2` 次 | [§3.1](#url-forms)、[§3.3](#stable-ids)、[§6.1](#user-profile) |
| **V-04** | **用户资料、`mymblog` `since_id`+`page` 连续翻页与无 Cookie 对照**：核实 `profile/info` 与 `mymblog` 第 1→2 页页长、跨页 0 重复、无访客空壳退化，并以 1 次无 Cookie 请求验证真值表未登录分支 | 1 个公开活跃博主（含置顶/快转更佳） | JSON API `<= 4` 次（资料 1 + 登录 2 页 + 无 Cookie 1） | [§6.1](#user-profile)、[§6.2](#user-timeline)、[§6.5](#empty-truth-table) |
| **V-05** | **置顶、快转（`ori_mid`）、原创筛选与可见时间窗**：确认 `isTop`/`mblogtype`、快转 `ori_mid` 的 `user` 与 `created_at` 归属、`feature=1` 是否过滤快转，以及“半年/三天可见”博主的 `bottom_tips_visible`/`bottom_tips_text` 与 `since_id` 表现 | 复用 V-04 博主 + 可选 1 个开启“半年可见”的公开博主 | JSON API `<= 2` 次 | [§4.3](#retweets)、[§6.3](#monotonicity)、[§6.4](#visibility-window) |
| **V-06** | **单条详情与长微博全文**：确认 `/ajax/statuses/show?id=<mblogid>&locale=zh-CN&isGetLongText=true` 是否直接内联全文及转发长博，确认 `/ajax/statuses/longtext?id=<mblogid>` 是否接受数字 `mid` 及精确响应结构 | 1 条公开长微博（含转发长博更佳） | JSON API `<= 2` 次（`show` 1 + 必要时 `longtext` 1） | [§4.1](#post-detail)、[§4.2](#long-text) |
| **V-07** | **超过 9 图、各尺寸原图与去平台水印规则**：验证 `>9` 图 `pic_ids`+`pic_infos` 完整有序性、`largest` vs `original` vs `mw2000` 像素与水印差异，验证 `/oslarge/`、`/osj1080/` 改写与 `/ajax/common/download?pid=` 是否无损去水印及是否需要 Cookie | 1 条超过 9 图的公开图文微博 | JSON API `<= 1` 次 + 样本图对比 `<= 3` 次（`<= 10 MiB`） | [§4.4](#images) |
| **V-08** | **GIF 动图、实况照片与混排 `mix_media_info`**：验证 `type=="gif"` 的 `largest.url` 是否为多帧 GIF；确认 `type=="livephoto"` 的 `pic_infos[id].video` 形态、跳转与容器（`.mov`/`.mp4`）；核实 `mix_media_info.items[]` 实际字段 | 1 条含 GIF / 实况 / 混排的公开微博 | JSON API `<= 1` 次 | [§4.4](#images) |
| **V-09** | **视频 `playback_list`、无 Cookie vs 登录画质及独立视频页 `1034:`**：核实 `playback_list`（`scrubber_hd` 过滤、H.264/H.265、`quality_index`）、同一 1080P+ 视频无 Cookie 与登录态档位差异，以及 `POST /tv/api/component` 是否返回宿主微博 `mid`、`author_id` 与 `urls` | 1 条公开高画质短视频微博（`< 30s`，含 `1034:` `fid`） | JSON API `<= 3` 次（无 Cookie 1 + 登录 1 + `component` 1） | [§3.1](#url-forms)、[§3.3](#stable-ids)、[§4.5](#videos) |
| **V-10** | **不存在、已删除、无权查看错误码与 `visible.type` 枚举**：确认 PC Ajax `/ajax/statuses/show` 与 `/ajax/profile/info` 的精确 `ok`/`errno`/`error_code`/`msg`（核对 `20003`/`20101`/`20112`）、转发原博已删时的 `retweeted_status` 结构、以及 `visible.type` 中 `6` vs `10` 各自对应好友圈还是仅粉丝可见 | 1 个明显不存在的合成 `uid`/`mid` + 1 条含已删原博转发或受限可见样本（若有） | JSON API `<= 3` 次（严禁主动探测风控） | [§4.3](#retweets)、[§4.7](#visibility)、[§9.1](#error-envelope)、[§9.2](#error-mapping) |
| **V-11** | **图片 CDN 无 Cookie + Referer 拉取与魔数校验**：验证 `wx1..4.sinaimg.cn`、`tvax1..4.sinaimg.cn` 在不带 Cookie + `Referer: https://weibo.com/` 下返回 `200 OK`、正确图片魔数且无跨域 302 | 复用 V-07 / V-08 样本图片 URL | 媒体 CDN `<= 2` 次（`<= 8 MiB`） | [§5.1](#cdn-domains)、[§5.2](#media-whitelist) |
| **V-12** | **视频与实况 CDN 无 Cookie + Range 分段、302 域与本地 `mp4ff` 校验**：验证 `f.video.weibocdn.com` 与 `livephoto.us.sinaimg.cn`（及 `video.weibo.com/media/play?livephoto=...` 若存在）在不带 Cookie + `Referer: https://weibo.com/` 下的 8 MiB `Range`（`HTTP 206`）、`Expires` 剩余 TTL 及是否 302 跨出白名单（决定 `PublicRedirects`）；并在本地对样本跑 `mp4ff v0.63.0` 容器校验（核实 `.mov` `ftyp=qt  ` 兼容） | 复用 V-09 短视频（`< 30s`）+ V-08 实况样本 | 媒体 CDN `<= 5` 次（视频 `<= 3` 次 / `<= 25 MiB` + 实况 `<= 2` 次 / `<= 10 MiB`；本地校验 `0` 请求） | [§5.1](#cdn-domains)、[§5.2](#media-whitelist)、[§10.2](#zero-new-deps) |
| **V-13** | **“我的”关注（含分组）与收藏只读翻页**：验证本人 `profile/followContent` vs `friendships/friends?uid=<self>` 起始页（`0`/`1`）、第 1→2 页 `next_cursor` 与 `total_number`；验证 `feed/allGroups` 与按 `list_id` 拉分组成员参数；验证 `favorites/all_fav?uid=<self>&page=1&with_total=true` 第 1→2 页（核实 `data.status` 键名与 `total_number`） | 登录测试账号本人（只读 GET） | JSON API `<= 6` 次（关注与分组 4 + 收藏 2） | [§7.1](#mine-following)、[§7.2](#mine-favorites) |
| **V-14** | **“我赞过的微博”端点可用性探针**：单次探测 `statuses/likelist?uid=<self>&page=1` 及返回键名，决定是否开放“我的 › 微博 › 赞过”入口；若返回 404/403/空壳/非预期结构直接判 `unavailable` 停止不重试 | 登录测试账号本人（只读 GET） | JSON API `<= 1` 次 | [§7.3](#mine-likes) |

#### B. 条件验证项（`V-15`～`V-19`，仅在 Q 项纳入扫码或直播时执行）

| 编号 | 触发条件 | 待验证问题 | 目标类型 | 建议请求额度 | 对应章节 |
| --- | --- | --- | --- | --- | --- |
| **V-15** | 仅当纳入网页扫码登录时 | 核实 `passport.weibo.com/sso/v2/qrcode/image`（vs `login.sina.com.cn/sso/qrcode/image`）响应结构（`qrid`、`image`）与 1 次未扫码 `check` 的 `retcode`（预期 `50114001`）；不扫码不登录，完整扫码确认/取消与跳转链留人工 H 项 | 公共 SSO 取码接口（无凭证） | 公共 API `<= 2` 次 | [§2.1](#qr-login) |
| **V-16** | 仅当纳入微博直播时 | 验证开播中主播的 `profile/info` 与 `mymblog` 是否返回 `live_status`/`object_type=="live"` 与 `live_id`，已下播旧卡片的 `media_info.live_status` 取值，以及 `live.weibo.com` 是否 302 至 `weibo.com/l/wblive/` | 1 个开播中公开主播 `uid`（+ 可选刚下播 1 个） | JSON API / 头检 `<= 3` 次 | [§8.1](#live-links) |
| **V-17** | 仅当纳入微博直播时 | 对比 `weibo.com/l/pc/anchor/live?live_id=` 与 `show_pc_live.json?live_id=` 真实信封（`code` vs `error_code`、`status` 枚举、是否含 `uid` 与开播时间）、回放与 `tv/api/component` 边界及无 Cookie 差异 | 同上开播 `live_id`（+ 可选已结束回放 1 个） | JSON API `<= 3` 次（`<= 512 KiB`） | [§8.2](#live-apis) |
| **V-18** | 仅当纳入微博直播时 | 确认 `live_origin_flv_url` 是否非空、是否带签名参数（决定能否改写 `_wb*` 后缀）；拉取 1 次 `live_origin_hls_url` `.m3u8` 文本确认分片是 `.ts`（HLS-TS，触发 `LOOP-008`）还是 `.m4s` + `EXT-X-MAP` | 来自 V-17 的开播流清单 | `.m3u8` 文本 `<= 1` 次（`<= 256 KiB`，不拉分片） | [§8.3](#live-streams) |
| **V-19** | 仅当纳入微博直播时 | 验证 `live_origin_flv_url` 域名、HTTPS/443 支持、是否 302 跨白名单域、**无 Cookie 仅带 `Referer: https://weibo.com/` 能否 `200 OK` 拉流**、FLV 编码头（AVC `CodecID=7` vs HEVC `12`/`hvc1` + AAC），短录 15～20 秒经现有 `FLVRemuxer` + `Defragment` 校验 MP4 | 来自 V-17 的开播 FLV 流 | 流连接 `<= 2` 次（`<= 5` 跳），`<= 20 秒` / `<= 40 MiB` | [§8.3](#live-streams)、[§8.4](#live-recorder-fit) |

<a id="check-procedure"></a>
### 11.2 供 Agent A 写入 ACT 的最小有界检查程序与额度建议

仿照[抖音协议资料 §10.2](douyin.md#act-010)与 `internal/sitecheck` 账本体例，建议 Agent A 在用户提供微博测试 Cookie（独立存于 `.local/site-secrets/weibo.cookie`）与公开检查对象后，按以下不重复计数的最小预算执行 1 次有界检查（全程串行、请求间隔 `>= 1.5 秒`）：

| 步骤 | 接口 / 目标域名 | 用途（对应 V 项） | 最多请求次数 | 体积 / 时长上限 |
| --- | --- | --- | --- | --- |
| 1. 登录对照、清洗与请求头 | `weibo.com/ajax/setting/getBasicInfo`、`m.weibo.cn/api/config`、`weibo.com/ajax/profile/info` | 无 Cookie 对照 + 清洗后 Cookie 严格鉴权 + 跨域与 `X-XSRF-TOKEN` 核对（**V-01**） | JSON API `<= 5` 次 | JSON `<= 512 KiB` |
| 2. `t.cn` 短链展开 | `t.cn` → 微博允许域 | 无 Cookie 逐跳读 `Location`，提取 ID 即停，不拉正文（**V-02**） | 重定向探针 `<= 3` 跳 | 仅响应头 / `<= 64 KiB` |
| 3. 昵称与个性域名换 `uid` | `weibo.com/ajax/profile/info`（`?screen_name=` / `?custom=`） | 验证可变入口换取稳定 `uid`（**V-03**） | JSON API `<= 2` 次 | JSON `<= 256 KiB` |
| 4. 用户资料、`mymblog` 分页与边界 | `weibo.com/ajax/profile/info`、`weibo.com/ajax/statuses/mymblog` | 资料 1 + 登录翻 2 页 + 无 Cookie 对照 1 + `feature=1` / 半年可见 2（**V-04、V-05**） | JSON API `<= 6` 次 | JSON `<= 3 MiB` |
| 5. 单条微博详情全形态与视频组件 | `weibo.com/ajax/statuses/show`、`longtext`、`weibo.com/tv/api/component` | 长博 2 + `>9` 图 1 + GIF/实况/混排 1 + 视频无 Cookie/登录/组件 3（**V-06～V-09**） | JSON API `<= 7` 次 | JSON `<= 3 MiB` |
| 6. 不存在 / 已删 / 受限错误码 | `weibo.com/ajax/statuses/show`、`weibo.com/ajax/profile/info` | 锁定 `20003`/`20101`/`20112`、`retweeted_status` 缺原博与 `visible.type`（**V-10**） | JSON API `<= 3` 次 | JSON `<= 256 KiB` |
| 7. 媒体 CDN 无 Cookie 拉取与本地校验 | `*.sinaimg.cn`、`*.weibocdn.com`（及实况 `video.weibo.com/media/play` 若出现） | 水印对比 3 + 图片校验 2 + 短视频 Range 3 + 实况片段 2；本地 `mp4ff` 校验 `0` 请求（**V-07、V-11、V-12**） | 媒体 CDN `<= 10` 次 | 合计 `<= 53 MiB` |
| 8. “我的”只读列表（可按范围随本轮执行或拆单独 ACT） | `weibo.com/ajax/profile/followContent`（或 `friendships/friends`）、`feed/allGroups`、`favorites/all_fav`、`statuses/likelist` | 关注与分组 4 + 收藏 2 页 + 赞过探针 1（**V-13、V-14**） | JSON API `<= 7` 次 | JSON `<= 2 MiB` |

- **MVP 汇总硬上限（零重复计数）**：
  - 核心内容与订阅（步骤 1～7）：JSON API **`<= 23` 次** + 无 Cookie 短链探针 **`<= 3` 跳** + 无 Cookie 媒体 CDN **`<= 10` 次（`<= 53 MiB`）**；
  - 含“我的”只读列表（步骤 1～8 合计）：JSON API **`<= 30` 次** + 无 Cookie 短链探针 **`<= 3` 跳** + 无 Cookie 媒体 CDN **`<= 10` 次（`<= 53 MiB`）**；单轮总耗时 **`<= 4` 分钟**。
  - 条件项独立额度（仅当 Q 项决定纳入时追加）：扫码（**V-15**）公共 SSO API **`<= 2` 次**；直播（**V-16～V-19**）JSON API **`<= 6` 次** + `.m3u8` 文本 **`<= 1` 次（`<= 256 KiB`）** + 直播流连接 **`<= 2` 次（`<= 20 秒`、`<= 40 MiB`）**。
- **立即停止条件（零容忍熔断）**：任何一步遇到非预期 HTTP `414` / `418` / `429` / `432`、HTTP `403`（非 V-07 去水印探针或 V-14 单次探针的明确业务响应）、`302` 跳登录/访客页、HTML 验证码 / Geetest 滑块页、0 字节空响应、或带有效测试 Cookie 仍返回 `ok == -100` / 退化为未登录，**立即停止后续全部请求，不做任何自动重试，严禁触碰或解算任何验证码**，原样输出脱敏回执向用户报告（注：V-07 去水印改写 404/403、V-10 预期不存在/已删业务码、V-14 预期探针 404/空壳仅记录结论后按计划收束，不反复试探）。
- **脱敏回执规范**（写于 `artifacts/site-check/`）：仅记录端点路径（不含查询串签名）、请求计数、HTTP 状态码、`ok`/`errno`/`error_code`、关键结构布尔值（如 `hasLongText`、`picCount`、`livephotoContainer`、`range206`、`redirectDomainKind`）、下载字节数与 `mp4ff` 校验结果；**严禁写入任何 Cookie、令牌、签名 URL、真实账号 `uid`/`mid`/`screen_name`**（固化自动化回归夹具时一律替换为合成值）。

<a id="suggestions-for-a"></a>
## 12. 给 A 的建议（R-J）

本节供 Agent A 编写 `M6 阶段计划修订 1`、更新需求与路线正本以及向用户提交 `USER-ACTIONS.md` 的 Q 项与 ACT 项。

<a id="mvp-table"></a>
### 12.1 微博“MVP 必须 / 可由后续 loop 深化”建议表

对照[路线与验收 §3](../delivery/roadmap.md#mvp-loop) 抖音一行，同时对齐现行全站基线（[DEC-056](../decisions.md#dec-056) 转发目录、`FR-USER-004` 全部缓存慢速队列、[DEC-047](../decisions.md#dec-047)“我的”、[DEC-062](../decisions.md#dec-062) 远程 `trace-server`）：

| 维度 | 微博 MVP 必须能完成的流程 | 可由后续 loop 深化 |
| --- | --- | --- |
| **登录与凭证** | 粘贴 Cookie 登录（白名单清洗、无 Cookie 对照严格校验、独立加密文件 `weibo.bin` + 熵 `trace:weibo:credential:v1`、不自动续期、失效停本站点并提示重贴、与 B 站/抖音登录互不影响） | 网页扫码登录（SSO v1/v2 跳转链，待 V-15 与人工 H 项验证后补齐）；同站多账号 |
| **链接与解析** | 分享文本提取、`t.cn` 无 Cookie ≤5 跳白名单展开与 `sinaurl` 外链拦截、单条微博（含 `mid <-> mblogid` 本地换算）、独立视频（`1034:`）、用户主页（`/u/`、`/p/100505`、`/p/100606`、`/n/`、个性域名）、不支持链接（含直播、超话、搜索、独立头条文章、合集）明确提示且零 API 请求；纯数字不抢 B 站路由；记入最近解析 | 显式裸 ID 前缀（如 `wb:`）；独立头条文章正文解析；视频合集聚合页 |
| **详情与用户页** | 单条微博详情（PC Ajax 主栈、长微博全文展开、转发含原博已删/不可见降级、超过 9 图、GIF 原图、实况照片静帧与动态片段、图文视频混排 `mix_media_info`、最高档视频、头条文章/投票/直播卡片安全降级、可见范围提示、在线互动数不落盘）；用户主页（资料、倒序翻页、置顶标记、“半年/三天可见” `bottom_tips_text` 提示、“只看未缓存”筛选、防风控空壳真值表、加入订阅） | 评论浏览；按时段高级检索；去平台水印路径改写选项（待 V-07 实测证实无损） |
| **缓存与媒体库** | 缓存单条微博（长文、超过 9 张原图、GIF、实况静帧+片段、混排视频）与独立视频（最高分辨率档单文件 MP4、同档按编码偏好、无 Cookie CDN、`Range` 续传、`moov` 前置校验）；转发微博按 DEC-056 存转发者 `forwards/` 目录并记录 `via`；用户页“全部缓存”后台慢速队列（优先复用列表页数据省流）；删除只删登记文件 | 视频画质升级提醒；头条文章离线归档 |
| **订阅、检查与“我的”** | 订阅关联微博账号 `uid`（跨站同人订阅）；手动与定时检查更新（首次仅建起点、置顶 `isTop` 与快转 `ori_mid` 不打断早停、剔除外人推广卡、不确定空响应不移起点）；按共用规则自动缓存；从微博关注列表批量导入订阅；“我的 › 微博”只读内存浏览关注与收藏（赞过与按分组拉成员仅在 V-13/V-14 实测通过后显示） | “我赞过的微博”（待 V-14 证实）；按自定义关注分组筛选成员（待 V-13 证实）；收藏标签筛选与一键缓存收藏 |
| **直播** | 仅识别直播链接与微博内嵌直播卡片，安全提示“微博直播暂不支持录制，可在浏览器中打开”，不报错、不误当点播 MP4 | 微博直播开播状态与 HTTP-FLV 录制（待 V-16～V-19 实测）、`LOOP-008` HLS-TS 后备 |
| **多站点隔离与远程版** | 站点小字标记“微博”与主页/媒体库/订阅筛选；微博独立的 API 间隔（1.2s+抖动）、每小时额度（100 次）、单轮上限（20 账号）、指数退避与下载熔断；`trace-server` 支持同步微博订阅与推送 Cookie（`CGO_ENABLED=0` 零 GUI 依赖） | 微博远端直播录制（随直播 loop 同步开放） |

<a id="fr-drafts"></a>
### 12.2 需求编号草案（`FR-WB-001`～`FR-WB-008` 及跨站/NFR 扩展）

仿照[需求 §2.8](../requirements.md#douyin-scope)（`FR-DY-001`～`009`）与[§3](../requirements.md#nfr) 拟定，供 A 写入需求正本：

| 草案编号 | 需求草案说明 |
| --- | --- |
| **FR-WB-001** | **微博登录**：登录页与“设置 › 账号”的微博一栏支持粘贴浏览器里的 `weibo.com` Cookie（附一句获取说明）。TRACE 按白名单只保留登录所需字段并剥离统计/界面字段，本地预检 `SUB`，经严格无 Cookie 对照的服务端校验确认账号有效（显示头像、昵称、UID）后才加密保存；无效、缺 `SUB`、误贴访客 Cookie 或遇风控/验证时分别说明且不保存；提交后输入框立即清空。不自动续期，失效后提示重新粘贴；可独立退出（删本机微博凭证）。网页扫码登录留待后续 loop |
| **FR-WB-002** | **微博链接**：解析入口接受微博分享文本、`t.cn` 短链（无 Cookie 逐跳白名单展开、最多 5 跳、`weibo.cn/sinaurl` 外链本地拦截）、单条微博链接（`weibo.com/<uid>/<mblogid>` / `<mid>`、`/detail/`、`m.weibo.cn/status/`、App 分享页，本地 `mid <-> mblogid` 换算）、独立视频专页（`1034:` `fid`）、用户主页链接（`/u/<uid>`、`/p/100505`、`/p/100606`、`/n/<昵称>`、个性域名）。直播链接、超话、搜索、热榜、独立头条文章、合集等明确提示“暂不支持这种微博链接”且不发 API 请求；纯数字继续归哔哩哔哩不猜微博。最近解析同样记录微博条目（`FR-HIST-001`） |
| **FR-WB-003** | **单条微博详情**：显示作者（头像、昵称，点进用户页）、发布时间、正文（长微博自动获取全文）、转发原博（原博已删除或无权查看时保留外层评语并降级说明）、多图（完整支持超过 9 图、GIF 原图、实况照片标“实况”可播片段）、图文视频混排（图片/实况/视频按原顺序展示）、单视频（封面、时长、清晰度）与可见范围提示；头条文章、投票、音乐、外链与直播卡片安全降级显示摘要与“在浏览器中打开”；互动计数仅在线展示不落盘。“⋯”菜单提供缓存、复制链接、在浏览器中打开 |
| **FR-WB-004** | **微博用户页**：显示头像、昵称、简介、认证说明、关注/粉丝/微博数；微博列表按发布时间倒序、滚到底取下一页、置顶标“置顶”、支持“只看未缓存”（可选原创筛选），遇到博主“仅展示最近半年/三天”时底部如实显示站点提示文案；风控空壳、未登录失效、真实 0 条与正常到底严格按真值表区分并可停在原处重试。显示所属订阅或“加入订阅”（`FR-USER-003`），支持“⋯ → 全部缓存”后台慢速队列（`FR-USER-004`） |
| **FR-WB-005** | **缓存微博视频**：按微博 `mid`（或独立视频 `fid`）只存一份；按 `playback_list` 过滤雪碧图与隐藏项，取当前账号有权访问的最高分辨率单文件 MP4（同档按设置编码偏好），校验 `moov` 在前与容器完整，连同封面与清单落盘；清单记录实际与可选档位、编码、分辨率、帧率；续传、暂停、重试、圆点与删除同哔哩哔哩/抖音。VIP 受限或加密流不绕过、不缓存并说明；不做画质升级提醒 |
| **FR-WB-006** | **缓存微博正文、多图、实况与混排**：保存长微博全文、发布信息、按 `pic_ids` 顺序的全部原图（含超过 9 图、GIF 动图）、实况照片静帧与动态片段、以及混排视频页 MP4 与封面；单张附件缺失记黄点不拖垮整条；转发微博（含其中图片与视频）按 [DEC-056](../decisions.md#dec-056) 存于转发者的 `forwards/` 目录并在 `item.json` 记录 `via`，原博不可见时完整保存外层转发正文 |
| **FR-WB-007** | **订阅与检查更新**：订阅可关联微博账号（主键 `uid`，一个订阅可同时关联 B 站、抖音、微博账号）；手动/定时检查更新与自动缓存对微博生效（首次仅建起点；置顶与快转 `ori_mid` 不打断早停；剔除外人推广卡；结果不确定时不移动起点）；共用规则的“视频 / 图文 / 文字 / 转发”类型映射到微博内容。未登录微博时跳过微博账号并写明原因，不中断 B 站与抖音。支持从微博关注列表批量导入订阅（对称 `FR-SUBS-003`） |
| **FR-WB-008**（或并入 `§2.9` 编为 `FR-ACCT-007`） | **我的 › 微博**（[DEC-047](../decisions.md#dec-047)）：在“我的”侧栏增加“微博”折叠组，严格只读且仅驻留内存、退出即清。提供登录账号的**关注**（点进用户页、“⋯ → 加入订阅”）与**收藏**（点进微博详情、“⋯ → 缓存”）；按自定义关注分组筛选与**赞过**仅在真实检查 **V-13 / V-14** 确认可用后显示入口，否则隐藏记 LOOP |

**跨站既有需求与非功能需求联动说明（不重复编号）**：

- 扩展 `FR-AUTH-007`（按站点登录扩至三站）、`FR-SITE-001`（微博为第三个站点）、`FR-SITE-002`（小字标签增加“微博”、站点筛选增加“微博”）、`FR-USER-004`（全部缓存慢速队列支持微博并复用列表响应省流）、`FR-REMOTE-003`/`004`（`trace-server` 支持微博订阅同步与 Cookie 推送）。
- 新增 **`NFR-NET-004`（微博请求节奏）**：API 串行、`1200 ms + 0～400 ms` 抖动、超时 `15 s` / 上限 `4 MiB`、`t.cn` 最多 `5` 跳（单跳 `10 s`、无 Cookie）、自动请求 `<= 100` 次/小时、单轮 `<= 20` 账号且账号间隔 `>= 3 s`、按站点独立熔断与指数退避（最长 `24h`）、绝不自动重试或解验证码。
- 新增 **`NFR-SEC-004`（微博凭证隔离）**：独立文件 `weibo.bin` 与独立熵 `trace:weibo:credential:v1` 加密保存；`SUB`、`SUBP`、`SCF`、`ALF`、`WBPSESS`、`XSRF-TOKEN`、`SSOLoginState` 与签名媒体地址绝不进日志、错误、历史、清单、数据库与包（哨兵值测试覆盖）；媒体 CDN 恒不带 Cookie。
- **数据存储版本说明**：多站点接入时 `library.db`、`updates.db`、`subscriptions.json`、`ui-state.json` 曾升至 schema **v2**（[数据设计 §1](../design/data-and-cache.md#schema-v2)）；接入微博时经代码核对确认旧版（`main @ 4b37fc5`）会在 `subscriptions.json`、`ui-state.json` 与 `cache-queue.json` 静默丢弃并回写 `site="weibo"`，因此最终按证据将 `subscriptions.json` 与 `ui-state.json` 升至 **v3**、`cache-queue.json` 升至 **v2**（均带 `.premigrate-*` 备份与 `NewerDataError` 降级拦截，见[数据设计 §1 版本 3 说明](../design/data-and-cache.md#schema-v3)），`library.db` 与 `updates.db` 保持 **v2**、`history.json` 与 `settings.json` 保持 **v1**。

<a id="q-drafts"></a>
### 12.3 Q 项草案表（供 A 整理编号进 `USER-ACTIONS.md`）

以下各项均有安全、可逆的推荐默认值，均为**非阻塞决定**（用户未回复前可按推荐默认值直接实施，后续随时可调）：

| 草案编号 | 问题 | 推荐默认值 | 备选方案 | 理由与可逆性 |
| --- | --- | --- | --- | --- |
| **Q-WB-1** | 微博登录方式：首版仅做粘贴 Cookie，还是同步做网页扫码登录？ | **微博 MVP 仅做粘贴 Cookie 登录**（严格清洗、无 Cookie 对照校验、不自动续期），网页扫码放后续 loop | 同步实现 SSO 扫码登录（需先测 V-15 + 新增 250–350 LOC 与人工真机扫码验证） | 对齐抖音成功先例（DEC-043）；微博 SSO 存在 v1/v2 两代跳转链不确定性；粘贴 Cookie 零风险上线，扫码随时可在 loop 增量补齐（可逆） |
| **Q-WB-2** | 微博直播录制：是否纳入微博 MVP？ | **延后至后续 loop**；MVP 仅识别直播链接与博文直播卡片并提示“暂不支持录制，可在浏览器中打开” | 同步做 HTTP-FLV 录制（需先过 V-16～V-19，额外 `+900–1,400` LOC；若仅 HLS-TS 还需引入 `LOOP-008` `mediacommon/v2` `+~2.1 MiB`） | 微博直播无固定房间号与批量查询接口、两套房间接口信封分歧、HTTP/80 与免 Cookie CDN 均未知，高频拉 `mymblog` 巡检会拖累订阅风控；后续可逆开启（可逆） |
| **Q-WB-3** | “我的 › 微博”纳入范围（关注、收藏、赞过） | **微博 MVP 纳入只读“关注”与“收藏”**；“赞过”与“按自定义分组查成员”仅在 V-13 / V-14 实测通过后才开放入口，否则隐藏记 LOOP | ① 整个“我的 › 微博”放后续 loop；② 无视实测保留“赞过”占位提示 | `all_fav` 与全量关注已有较清晰 PC Ajax 契约；“赞过”缺乏现代实证，按实测门控可避免重演抖音“喜欢”接口空包问题（可逆） |
| **Q-WB-4** | 是否支持从微博关注列表批量导入订阅？ | **微博 MVP 支持**（对称 B 站 `FR-SUBS-003`；若 V-13 证实分组成员接口则附分组筛选，否则先全量勾选导入） | 延后至后续 loop，仅支持手动粘贴主页链接订阅 | 关注列表接口已直接返回稳定 `uid`、昵称与头像，零额外详情请求且对多博主迁移极实用（可逆） |
| **Q-WB-5** | 纯数字输入在解析框的归属（微博 8～10 位 `uid` / 16 位 `mid` 与 B 站 UID 重叠） | **保持现状**：`<19` 位纯数字继续默认解析为哔哩哔哩 UID，不自动猜测为微博；微博通过完整链接、裸域名链接或分享文本解析 | 新增显式前缀（如 `wb:<uid>`），或对纯数字弹窗让用户选站点 | 避免破坏 B 站老用户直接贴纯数字 UID 的既有习惯；后续随时可追加 `wb:` 前缀（可逆） |
| **Q-WB-6** | 实况照片 `.mov` 缓存与图片去平台水印策略 | 实况照片同时保存静帧与伴侣动态片段（若 V-12 发现 `mp4ff` 不认 `.mov` `ftyp=qt  `，做轻量容器兼容而非引外部库）；正文图片默认保存 `largest`/`original` 直链，待 V-07 证实无损后再评估去水印开关 | 实况暂只存静帧；或默认强行把路径改写为 `/osj1080/` | `/osj1080/` 可能缩放到 1080px、`/oslarge/` 存在 404 风险；先保原分辨率与动图完整最稳妥（可逆） |
| **Q-WB-7** | 微博自动请求节奏与每小时配额默认值 | API 串行 `1200 ms + 0～400 ms` 抖动、每小时自动额度 `<= 100` 次、单轮检查 `<= 20` 账号、账号间隔 `>= 3 s`，独立退避与熔断 | 对齐抖音 `120` 次/小时，或更保守 `60` 次/小时 | 微博对短时高频与 `414`/`432` 敏感且长微博需额外 `longtext` 请求；保守起步可在实测后按需调整（可逆） |

<a id="top-risks"></a>
### 12.4 最大的 3 个风险 / `unknown` 摘要

1. **认证会话、风控边界与空列表歧义（R-A / R-E / R-H）**：新浪访客体系同样签发 `SUB` Cookie，PC `weibo.com/ajax/setting/getBasicInfo` 的无 Cookie 精确失败码、`X-XSRF-TOKEN` 在 GET 请求上的强校验边界、`SSOLoginState` 对跨站 `432` 的影响，以及博主“半年/三天可见”与风控静默抽空壳的真实响应细节均须由 **V-01、V-04、V-05、V-10** 实测锁定，否则存在误判已登录或漏检更新的风险。
2. **快转（`ori_mid`）时间乱序、实况 `.mov` 容器与 CDN 302 域边界（R-C / R-D / R-E）**：微博时间线特有的快转与外人推广卡若未从早停逻辑中剔除，会在遇到旧原博快转时截断整页新微博；同时实况照片 `.mov`（`ftyp=qt  `）与 `mp4ff` 的兼容性、视频 CDN `f.video.weibocdn.com` 的无 Cookie + `Range` + 是否 302 跨外域，以及 `visible.type` `6`/`10` 的社区分歧，都必须经 **V-05、V-07、V-08、V-10、V-11、V-12** 实测锁死。
3. **直播无固定房间号的高风控低收益与“赞过 / 分组成员”契约缺失（R-F / R-G）**：微博直播 `live_id` 每次开播变化且无按 `uid` 批量查询接口，两套房间接口信封分歧、HTTP/80 明文及免 Cookie CDN / HLS-TS 均未知（因此推荐微博 MVP 延后直播）；而“我赞过的微博”与“按自定义 `list_id` 拉分组成员”缺乏 2025–2026 现代实证，必须以 **V-13、V-14** 实测为上线门槛，严防无契约盲上导致类似抖音“喜欢”接口空包问题。

<a id="a-review"></a>
## 13. A 审阅定稿（2026-11-21）

A 按[站点规则](../dev-rules/sources-and-safety.md#site-rules)审阅了第 1～12 节，抽查了其中引用的仓库代码事实（`credential.MaxCookieInputBytes = 8192`、`Manager.RedactString`、`download.looksLikeDisguisedTextBody`、`download.EnsureSingleMP4Context`、`site.DefaultDouyinMediaHosts`、`site/registry.go` 的 `reBareKnownDomain` 与 19 位纯数字规则、`subscription.SiteHourlyQuotaLimit`），都属实；另发现 `SiteHourlyQuotaLimit` 与 `normalizeSchedulerSite` 对不认识的站点**落回哔哩哔哩的数值**，这类“默认按哔哩哔哩”的分支是接入第三个站点时最容易漏的地方（见 13.2 第 1 条）。本机参考索引里没有专门的微博条目，相关的是多站点订阅、单链接解析与直播录制三个功能领域；A 抽查了多站点订阅参考里的微博部分，与第 2.4、4.1 节一致（登录 UID 取自 `m.weibo.cn/api/config`，超过 9 张图回退 PC `ajax/statuses/show`）。范围按 [Q-023](https://github.com/chialecode/trace/issues/22) 的推荐默认值定稿，计划见[M6 计划](https://github.com/chialecode/trace/blob/b75cf80/docs/delivery/stage-plan.md)，需求见[需求 §2.11](../requirements.md#weibo-scope)。

<a id="a-review-choices"></a>
### 13.1 实现采用的取舍

1. **按站点分支要逐个补齐**：凡是 `switch` 站点或“站点为空时按哔哩哔哩”的地方（额度、间隔、退避、熔断、作者目录、作品数、文案、开播巡检、界面路由），都要给微博明确的分支；不认识的站点一律报错，不能落进哔哩哔哩。
2. **登录确认（2026-11-21 按 ACT-022 登录确认的 2 次真实请求改定，见 13.2）**：从 `weibo.com` 复制的有效 Cookie，`m.weibo.cn/api/config` 返回 `ok = 1`、`login = false`、没有 UID，**手机域不认这份会话**，所以第 2.4 节的候选 2 不能用来确认，第 2.6 节“通常可通用”的说法对粘贴的 `weibo.com` Cookie 不成立；`weibo.com/ajax/setting/getBasicInfo` 返回 `ok = 1` 与账号自己的设置资料（`data.screen_name` 等 18 个字段），**但没有 UID 字段**（也没有头像）。因此默认流程改为：① `getBasicInfo` 要求 `ok == 1` 且 `data.screen_name` 非空（证明 PC 域已登录）；② 用这个昵称调 `weibo.com/ajax/profile/info?screen_name=<昵称>` 取 `data.user.idstr`、头像，并核对返回的 `screen_name` 与 ① 一致，不一致按“无法确认”处理、不保存。不带 Cookie 时 `getBasicInfo` 的表现（预期 `ok = -100` 或跳登录页）与“只带访客 Cookie”的对照仍待后续 [ACT-023](https://github.com/chialecode/trace/issues/19) 完整运行的 V-01 确认。TRACE 全部请求只走 PC 域；运行中遇到未登录、风控时不换别的接口重试。
3. **条目键与目录**：所有微博都是 `weibo:dynamic:<mid>`（视频微博也一样，避免编辑后换键）；没有所属微博的视频页用 `weibo:video:<视频对象 ID，冒号换成下划线>`。目录 `weibo/<uid>/posts/<mid>/`，转发存 `weibo/<uid>/forwards/<mid>/`，作者目录直接用 UID（纯数字、稳定）。文件：只有一个视频的微博存 `video.mp4` 与 `cover.jpg`；图片 `images/NN.<实际扩展名>`；实况片段 `images/NN_live.<mov 或 mp4>`（角色 `motion`）；混排里的视频 `images/NN.mp4` 与封面（角色 `video`，同抖音混排图文）。
4. **规则类型**：有 `retweeted_status` 或 `ori_mid` 的是 `forward`；唯一的媒体是一个视频的是 `video`；带图片（含混排）的是 `draw`；其余（纯文字，以及只有文章、投票、音乐、外链卡片的）是 `word`。
5. **快转**：按转发处理；条目键优先用快转条目自己的 `mid`，与 `ori_mid` 相同时用它并在 `via` 里记快转者；V-05 确认 `user` 与 `created_at` 的归属后回写本节。快转与置顶都不参与“到起点就停”。
6. **全部缓存**：只补博主的原创微博（不含转发与快转）；V-05 确认 `feature=1` 正好排除转发与快转时用它，否则用 `feature=0` 再在本机过滤。复用列表响应生成缓存计划只能在内存里做，`cache-queue.json` 只存 `mid` 与游标；内存里没有计划或签名地址过期时，下载任务按 `mid` 重新取详情。作品数显示站点的 `statuses_count`（含转发），文案写“微博 N”。
7. **检查更新**：没有起点时只读第 1 页；之后每个账号每次最多 2 页；第 6.3、6.5 节的置顶、快转、推广卡片与不确定空结果规则照用。
8. **请求节奏**：采用第 9.3 节的建议值，写成 [NFR-NET-004](../requirements.md#nfr)。
9. **媒体白名单**：后缀 `sinaimg.cn`、`weibocdn.com`；`video.weibo.com` 只为实况首跳 `/media/play` 单独放行；`Referer: https://weibo.com/`，不带 Cookie；视频与实况的 `PublicRedirects` 默认关，V-12 发现跳到第三方边缘域名时再照抖音的做法打开。
10. **图片与水印**：默认按 `largest` → `original` → `mw2000` → `large` 取最大的一档；微博原图、长图与多帧 GIF 经常超过 JSON 接口的 4 MiB 上限乃至 50 MiB，因此在线原图读取（`FetchImage`）、预览暂存与分段下载统一采用单张 128 MiB 上限与按次读取空闲超时（不与 JSON 响应的 4 MiB 上限混用）；V-07 证实 `oslarge`（或 `ajax/common/download`）无损、无水印且不需要 Cookie 时改为默认用它、失败回退 `largest`，否则保持（Q-023 第 6 条）。
11. **实况片段容器**：按实际容器保存，用 `mp4ff` 校验有视频轨与 `moov`；应用内播放器打不开 `.mov` 时用 `mp4ff` 无损改封装成 `.mp4`（不转码、不加依赖）。
12. **直播**：不录制、不巡检；直播链接与直播卡片按第 8.4 节提示；微博账号不进开播检查。
13. **我的**：关注先按社区资料较完整的 `ajax/friendships/friends?uid=<自己>` 实现（`followContent` 可作备选），收藏 `ajax/favorites/all_fav`；“赞过”与关注分组各有一个开关常量，默认关，V-13、V-14 通过后才打开（同抖音观看历史的做法）。
14. **数据结构版本（已按证据定稿）**：代码核对证实 `main @ 4b37fc5` 会静默丢弃并回写 `subscriptions.json`、`ui-state.json`、`cache-queue.json` 中的 `site = "weibo"`，因此最终将 `subscriptions.json` 与 `ui-state.json` 升至版本 3（`.premigrate-2`）、`cache-queue.json` 升至版本 2（`.premigrate-1`），其余文件保持原版本（`library.db` v2、`updates.db` v2、`history.json` v1、`settings.json` v1），详见[数据设计 §1 版本 3 说明](../design/data-and-cache.md#schema-v3)。
15. **密文扫描**：`tools/checkdocs`、`internal/sitecheck` 与冒烟哨兵增加微博模式（`SUB=` / `SUBP=` / `XSRF-TOKEN=` / `WBPSESS=` 后跟像真值的长串，带 `ssig=`、`Expires=` 与 `KID=` 的媒体地址）；本文里 `SUB=<合成占位>` 这样的占位写法不能误报。

<a id="a-review-verify"></a>
### 13.2 V-01～V-19 核对状态

| 编号 | 状态 | 说明 |
| --- | --- | --- |
| V-01 | 部分验证（2026-11-21） | 用户早先授权的“ACT-022 登录确认”发出 2 次只读请求（账本第 1 步 2/5，HTTP 均 200 JSON，无风控、验证或跳转；脱敏回执 `artifacts/site-check/act022-login.json`）：`m.weibo.cn/api/config` 对粘贴的 `weibo.com` Cookie 判未登录；`weibo.com/ajax/setting/getBasicInfo` 判已登录但不给 UID（结论见 13.1 第 2 条）。随后授权的 [ACT-022](https://github.com/chialecode/trace/blob/f8af16863dbe885cfd63de048730c07a2b91e7be/USER-ACTIONS.md#act-022) 单次完整运行执行了步骤 1～5（未触发任何停止条件，累计消耗 22/37 次接口额度，含早先 2 次），但在步骤 6 因本机 typed-nil 异常中断且未写出正式回执，完整 V-01 对照（无 Cookie / 访客 Cookie、`profile/info?screen_name=`、`X-XSRF-TOKEN` 边界）待后续 [ACT-023](https://github.com/chialecode/trace/issues/19) 留存脱敏回执 |
| V-02～V-14 | not-run（ACT-022 中断，待 ACT-023） | 2026-11-21 授权的 [ACT-022](https://github.com/chialecode/trace/blob/f8af16863dbe885cfd63de048730c07a2b91e7be/USER-ACTIONS.md#act-022) 单次完整运行（`MaxRuns=1`）执行了步骤 1～5 且未触发任何停止条件（累计接口 22/37，媒体 CDN 0 次 / 0 B）；步骤 6 请求不存在用户的 `/ajax/profile/info` 返回 HTTP 200 `ok: 1`，暴露了本机 `site.KindOf` 对 typed-nil `*site.Error` 的空指针异常并立即中止（0 次重试、未改账本、未写出 `artifacts/site-check/act022.json`，临时目录已清理）。本机缺陷已在 `internal/site/errors.go`、`tools/actcheck/` 修复，并据此加固 `internal/site/weibo/user.go`（`{"ok":1,"data":{}}` / `data.user == null` 归为 `unavailable` 而非 `schema_changed`）。因 ACT-022 未留存正式脱敏回执且单次运行上限已耗尽，V-02～V-14 的真实站点结论待用户授权 [ACT-023](https://github.com/chialecode/trace/issues/19) 补测并回写（“赞过”与关注分组默认保持关闭） |
| V-15 | 不在本阶段 | 扫码登录按 Q-023 第 1 条延后 |
| V-16～V-19 | 不在本阶段 | 直播按 Q-023 第 2 条延后 |
