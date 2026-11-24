# 失效静态图片公共 CDN 缓存回退与有效缓存防覆盖研究

查询日期：2026-11-25（UTC+8）。本文按[来源与安全 §1](../dev-rules/sources-and-safety.md#open-source)记录失效公开静态图片的第三方公共图片代理/CDN 缓存与网页归档选型依据、协议细节与安全红线；关联决定见[有效决定 DEC-075](../decisions.md#dec-075)，缓存与校验流程见[数据、缓存与信息流 §1、§2.4、§2.5](../design/data-and-cache.md#cache)。

**核查方法与红线声明**：

- **查询渠道**：各项目公开仓库与官方文档（`github.com/weserv/images`、`wsrv.nl/docs/`、`developer.wordpress.com/docs/photon/`、`jetpack.com/support/site-accelerator/`、Internet Archive Wayback Machine 官方文档）、Go 标准库 `net/http` / `image/gif` 能力核对，以及本仓 `go.mod` 与 `tools/check` 依赖门禁规则。
- **零新增外部 Go 依赖**：`internal/site` 仅允许使用 Go 标准库；本文所选方案全部为纯 HTTPS GET URL 构造与二进制文件头校验，无需改动 `go.mod` / `go.sum`。
- **单元测试零外连红线**：仓库内全部自动化测试（`go test`）在 `AllowLocalhost == true` 且未注入本机测试提供方时对公共缓存发 **0 次**外部请求，回退测试一律通过注入 `127.0.0.1` `httptest.Server` 或内存 `RoundTripper` 完成。

---

<a id="problem"></a>
## 1. 问题背景与两类场景（2026-11-25）

1. **已缓存有效图片被失效响应覆盖**：当用户或后台任务对已缓存过的帖子、视频封面、最近解析缩略图或本地订阅头像（7 天到期刷新）触发重试/重缓存时，若源站原图已被删除、返回 HTTP `403`/`404`/`410`，或像新浪微博 `sinaimg.cn` 那样对已删图片返回 HTTP `200` 的单帧灰度删除占位 GIF（如 `480×360` 占位图），缺少前置有效文件保护的路径可能删除或覆盖磁盘上原本完好的图片，或在转发/引用原帖被删后把原帖已缓存图片从 `item.json` 清单中丢弃。
2. **源站刚失效的无签名公开静态图恢复**：对于微博 `wx1..wx4.sinaimg.cn`、X `pbs.twimg.com`、哔哩哔哩 `i0.hdslb.com` 等**无签名公开静态图片 URL**，即使源站边缘已清除（`403`/`404`/`410` 或返回失效占位 GIF），曾被网页引用、RSS 阅读器或互联网归档抓取过的公开原图仍可能保留在公共图片缓存 CDN 边缘或网页归档快照中。

---

<a id="candidates"></a>
## 2. 公共图片缓存与网页归档方案对比（查询日期：2026-11-25）

| 维度 | Jetpack Photon（`i0.wp.com`） | `wsrv.nl`（`weserv/images`） | Internet Archive Wayback Machine（`web.archive.org`） | Google AMP Cache / DuckDuckGo Proxy（不采用） |
| --- | --- | --- | --- | --- |
| **来源与许可证** | Automattic 官方图片加速 CDN（[Photon API 文档](https://developer.wordpress.com/docs/photon/)）；服务端托管，客户端仅拼装标准 HTTPS URL | 开源项目 [`weserv/images`](https://github.com/weserv/images)（Nginx + `libvips` C++ 模块，**BSD-3-Clause**），托管于 Cloudflare 全球边缘 CDN（[`wsrv.nl`](https://wsrv.nl/)） | 非营利互联网档案馆官方网页归档服务（[`web.archive.org`](https://web.archive.org/)） | 闭源专用代理（AMP 需特定域名映射或签名，DuckDuckGo `external-content.duckduckgo.com` 仅供自身搜索结果页使用） |
| **请求格式** | `https://i0.wp.com/<host><escapedPath>?ssl=1` | `https://wsrv.nl/?url=<url-encoded>&n=-1` | `https://web.archive.org/web/0id_/<cleanOriginURL>` | 不适用 |
| **缓存留存特性** | 边缘缓存长期保留（官方文档说明不主动按短 TTL 清空，除非源文件名变更）；源站刚删图时命中率高 | 依赖 Cloudflare 全球边缘缓存；曾在社区网页/阅读器中加载过的热图命中率高，未缓存时回源失败返回 JSON 404 | 历史归档快照永久保存；即使源站数月或数年前已删图，只要曾被归档即可取回原始字节 | 服务条款或防盗链限制不适合作为通用回退 |
| **查询参数与动图支持** | 顶层查询串会被 Photon 解释为缩放指令，因此**仅适用于无查询串的 HTTPS 静态地址**（如 `wx1.sinaimg.cn/large/*.jpg`、`i0.hdslb.com/bfs/album/*.png`）；单图上限约 `50 MiB`；支持 JPG/PNG/GIF/WebP | `url` 参数整体百分号编码，**完整支持带公开格式参数的地址**（如 `pbs.twimg.com/media/<id>?format=jpg&name=orig`）；必须传 `n=-1` 保留多帧 GIF/WebP 全部帧 | 完整保留原始 URL；时间戳 `0` 选取**最早一次抓取快照**（避免取到删图后才抓取的占位图），修饰符 `id_` 禁用 HTML 工具栏改写、原样返回二进制响应（常经 `302` 跳转至 `/web/<14位时间戳>id_/<url>`） | 不适用 |
| **未命中/错误响应** | HTTP `400`/`404`/`502`/`504` 纯文本错误体 | HTTP `404`（或 `400`/`502`）+ `application/json`（`{"status":"error","code":404,...}`） | 未收录时返回 HTTP `404` HTML 页 | 不适用 |
| **结论** | **采用（顺位 1，仅无查询串地址）** | **采用（顺位 2，含 `?format=&name=orig` 地址与多帧动图）** | **采用（顺位 3，历史快照兜底，允许 `archive.org` 域内最多 3 跳 `302`）** | **排除** |

---

<a id="security-boundary"></a>
## 3. 隐私、安全与触发边界（DEC-075）

1. **只在源站确认失效后触发**：
   - 在线读图（`FetchImageLimited`）与缓存下载（`internal/download`）始终**先直连源站 CDN**（微博含 `wx1..wx4.sinaimg.cn` 边缘节点轮换及下载期最多 1 次详情刷新；X / 抖音含下载期最多 1 次详情刷新）。
   - **仅当**直连源站返回 HTTP `403` / `404` / `410` 或返回已识别的源站失效占位图时才尝试公共缓存回退。
   - 遇到 HTTP `429`（限流）、`412` / `414` / `418` / `432`（风控挑战）、`401`（未登录）、超出体积上限或 `context` 取消时**立即停止，绝不触发第三方回退**。
2. **严格的公开静态地址白名单与签名/令牌黑名单**：
   - 仅允许 `https`（默认 443 端口、无用户信息）且主机名匹配以下无签名公开静态图片源站：
     - `*.sinaimg.cn`（**显式排除**带签名的 `*.us.sinaimg.cn` 实况/视频子域），且查询串必须为空、路径扩展名为静态图片；
     - `pbs.twimg.com`，且查询参数仅允许 `format` 与 `name`（如 `?format=jpg&name=orig`）；
     - `*.hdslb.com`（剥离 `@` 缩放后缀后扩展名为静态图片，且查询串为空，排除字幕 `.json` 与带 `auth_key` 地址）。
   - 任何含私有签名、时效令牌或会话参数的 URL（`ssig`、`Expires`、`x-expires`、`KID`、`a_bogus`、`x-bogus`、`msToken`、`x-signature`、`signature`、`sig`、`sign`、`token`、`access_token`、`auth`、`auth_key`、`key`、`secret`、`ticket`、`cookie`、`sub`、`subp`、`xsrf-token`、`wbpsess`、`auth_token`、`ct0`、`kdt`、`twid`、`sessdata`、`bili_jct`、`sessionid`、`sid_guard`、`ttwid` 等），以及抖音签名图床（`*.douyinpic.com`）、X 私有媒体（`ton.x.com` / `ton.twitter.com`）与各站视频流域名，一律由 `IsEligibleForPublicImageCDNFallback` 直接拒绝，**零字节发往任何第三方主机**。
3. **无凭证请求头与网络防逃逸**：
   - 首跳与每次允许的重定向跳均强制剥离 `Cookie`、`Authorization`、`X-Csrf-Token`、`X-XSRF-TOKEN`、`Referer`、`Origin`。
   - 传输层统一走 `site.NewGuardedMediaDialer`（遵循用户系统代理 `site.Proxy`，阻断私网/回环/链路本地 IP），并校验重定向目标域名仍在该提供方的允许域名后缀内（防止开放重定向逃逸）。
4. **响应严格验真与有效文件永不覆盖**：
   - 公共缓存返回的 HTTP `200` 响应必须同时满足：非文本/HTML/JSON `Content-Type`、非伪装文本前缀、严格匹配二进制图片魔数（`SniffImageMagicBytes`，不信任响应头自报类型），且未命中源站失效占位图检测（例如某公共缓存自身也缓存了新浪 `480×360` 灰度删除占位 GIF 时同样拒绝）。
   - 所有落盘路径（四站点作品图片与封面、直播封面、最近解析缩略图 `thumbs/`、订阅与作者本机头像 `avatars/`）均先写临时文件并完成魔数与非占位校验后再原子落盘；目标槽位若已存在归属本条目的有效非占位图片，重缓存或刷新遇到失效/占位响应时**一律保留原有有效文件与清单记录，绝不删除或覆盖**（未登记的用户自放文件仍按 INV-09 报 `storage` 冲突保护）。
