# 技术设计：移动端

本页说明 TRACE 移动端（手机应用）与电脑端“移动端服务”应当怎样工作（[DEC-085](../decisions.md#dec-085)，需求 [§2.13](../requirements.md#mobile-scope)，界面 [§3.12](../ui.md#mobile)）。**全部是计划，尚未实现**：下文的目录、文件、端口、接口与命令都不代表已经存在，实现时按 issue 拆分（第 11 节）并以代码为准回写。进程分层见[架构](architecture.md)，信息流与媒体路径白名单见[数据与缓存](data-and-cache.md)，证书与令牌的做法沿用[远程设计](remote.md)，选型见[移动端选型](../research/2026-11-26-mobile.md)。

<a id="scope"></a>
## 1. 定位与边界

- 手机是电脑端 TRACE 的**遥控器和查看器**，不是独立的 TRACE：数据、凭证、站点请求、缓存、录制只在电脑端进行。
- **电脑端没在运行，手机就不能用**：手机只连正在运行的电脑端（窗口关到托盘、进程在后台运行也算运行，DEC-030）；连不上时整屏显示原因（第 8 节），不显示任何旧内容。
- 手机**从不**接触站点：不存 Cookie，不向任何站点或站点 CDN 发请求；图片与视频也从电脑端取。手机本机不保存内容（只保存配对记录与令牌，第 7.3 节）。
- 只在局域网连接；外网访问由用户自备 VPN（如 Tailscale，地址落在 `100.64.0.0/10`）。TRACE 不做端口映射（UPnP），不经任何云服务或中转。
- 第一版只做 Android；iOS 以后再做（[#123](https://github.com/chialecode/trace/issues/123)）。

<a id="roles"></a>
## 2. 角色与数据流

```text
手机 TRACE（MyGo 原生界面，Android）                      电脑端 TRACE（同一进程）
  界面 ──HTTPS JSON（证书指纹固定 + 设备令牌）──────────>  internal/companion（移动端服务）──> internal/service（现有服务层）
  界面 <──WebSocket 事件（任务、录制、开播、新内容、下线）──     │                                   ├─ 信息流、媒体库、订阅、任务、录制
  系统播放器 ──http://127.0.0.1（手机内回环转发）──┐            │                                   └─ 只有电脑端发站点请求
                                                  └──HTTPS──>  媒体（MediaPath 白名单 + Range）
```

- `internal/companion` 是服务层之上的又一个调用方，地位与 `internal/ui` 相同：只调用 `internal/service` 的公开方法，不直接碰库、下载器、订阅与录制的内部对象。
- 控制命令（第 5.3 节）在电脑端按**用户的手动操作**执行，站点间隔、每小时额度、退避、熔断与登录检查完全不变（NFR-MOB-NET-001）。

<a id="service"></a>
## 3. 电脑端移动端服务

- **包**：`internal/companion`（纯 Go，不依赖 MyGo，INV-05）；证书生成、令牌哈希与常数时间比较、失败限流复用 `internal/remote` / `internal/remoteserver` 已有的实现（必要时下沉为共用小包）。`trace-server` 不链接它。
- **开关**：`settings.json` 新增可选字段 `companion: {enabled, port}`（结构版本不变，旧版忽略）；默认 `enabled=false`。打开后才监听，关掉立即停止监听并断开全部手机（先推送 `bye{reason: "disabled"}`）。
- **端口**：默认正式版 `47381`、开发版 `47382`（远程版是 `47380`），设置里可改；端口被占用时说明并保持关闭，不自动换端口。演练身份 `trace-test` 不提供移动端服务（第 10 节）。
- **监听地址**：只在私有地址（`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`）、IPv6 ULA 与链路本地地址，以及**虚拟网卡（隧道类型，如 Tailscale）上**的 `100.64.0.0/10` 地址上监听；物理网卡上的 `100.64.0.0/10` 是运营商级 NAT 地址，按公网对待、不监听；不监听公网地址。网卡变化时（插拔网线、切换 Wi-Fi、VPN 连上或断开）重新计算。“设置 › 移动端”提示 Windows 防火墙只放行“专用网络”。
- **证书**：第一次打开时生成自签名 ECDSA P-256 证书，放 `<数据目录>/companion/tls/`（`0600`）；只有用户点“重置移动端身份”才重新生成，重新生成会让全部已配对手机失效。
- **设备文件**：`<数据目录>/companion.json`（`version: 1`，`0600`，原子写，损坏按 NFR-DATA-001 处理）：节点 ID（复用 `node.json`）与设备列表 `{id, name, model, platform, channel, tokenHash, pairedAt, lastSeenAt, lastAddr, homePositions}`。`homePositions` 是这台手机的主页阅读位置（同 FR-FEED-001 的结构），存在电脑端，手机本机不存。实现时（[#118](https://github.com/chialecode/trace/issues/118)）同时写进[数据与缓存 §1](data-and-cache.md#storage)与 `GuardDataSchemas`；新数据文件按高风险清单处理。
- **退出与更新**：托盘“退出”、`--quit-for-update` 与关闭开关时，先向在线的手机推送 `bye{reason: "exit" | "update" | "disabled"}`，再关闭监听；不为此延长退出时间（推送最多等 1 秒）。
- **资源**：开关关闭时不监听、不起协程；没有手机在线时只有一个空闲的监听。缩略图缓存放 `<数据目录>/cache/companion-thumbs/`（按最近使用淘汰，上限 256 MiB），删除条目时一并失效。

<a id="pairing"></a>
## 4. 配对与设备

1. 用户在电脑端“设置 › 移动端”点“添加手机”：生成一次性配对码（128 位随机数，5 分钟有效，只能用一次，同时只有一个有效），显示二维码。二维码内容是 `trace-pair:` 加 base64url 的 JSON：`{v, node, name, channel, port, addrs[], fp, code}`（`addrs` 是监听中的全部地址，`fp` 是证书叶子 DER 的 SHA-256）。扫码时手机用 `fp` 固定证书，所以中间人无法冒充电脑。
2. **手动配对**（不能扫码时）：对话框另外显示地址、8 位短配对码（同样 5 分钟、一次性，输错 5 次作废）和**电脑身份码**（`fp` 的前 64 位，十六进制分 4 组，如 `3F7A-91C2-0B5E-D844`）。手机输入地址与短码后先建立 TLS 连接、不发送短码，显示它实际看到的证书算出的身份码，请用户与电脑屏幕上的逐组核对；用户确认一致后才提交短码，并从此固定这个指纹；不一致或用户取消就断开、不发送任何东西。这样局域网里的中间人即使转发短码，手机看到的也是中间人自己的证书、身份码对不上。64 位前缀要求攻击者为一个固定指纹做约 2^64 次密钥生成才能撞上，不可行。
3. 手机用固定的指纹依次尝试地址，`POST /api/v1/pair` 提交 `{code, deviceName, model, platform, channel, appVersion, pairSecret}`，其中 `pairSecret` 是手机生成的 128 位随机数。通道不一致直接拒绝（`409 channel_mismatch`）。电脑端返回 `pairId`（128 位随机数），只在内存里与这次请求的 `pairSecret` 的哈希绑定，2 分钟过期。
4. 电脑端弹出“允许『<手机名>』连接这台电脑？”（允许 / 拒绝，2 分钟不处理视为拒绝）；手机长轮询 `GET /api/v1/pair/{pairId}`（请求头带 `pairSecret`，每次最多 60 秒）等结果，`pairSecret` 不符一律 `404`。允许后电脑端生成 32 字节设备令牌，只存 SHA-256，令牌只在第一次成功的轮询响应里返回一次，随后删除 `pairId`。
5. 设备上限 5 台；满了时配对请求返回 `409 device_limit`，电脑端提示先移除一台。
6. **移除**：电脑端“移除”立即删除令牌哈希并关闭这台手机的全部连接（先推送 `bye{reason: "revoked"}`）；之后旧令牌一律 `401`。手机“解除配对”调用 `DELETE /api/v1/devices/self` 后清掉本机配对记录与令牌；电脑不在线时只清本机，电脑端那条记录由用户手动移除。
7. **重新扫码更新地址**：已配对的手机扫到同一节点、同一指纹的新二维码时，只更新地址列表、不再走确认（配对码仍作废）。

<a id="api"></a>
## 5. 接口与事件

<a id="security"></a>
### 5.1 传输与安全

- **传输**：HTTPS，TLS 1.3，手机只认配对时的证书指纹，不信任系统证书库；指纹不符拒绝连接并提示“电脑的身份变了”，不自动信任新证书。
- **鉴权**：除 `pair` 两个端点外，每个请求带 `Authorization: Bearer <设备令牌>`；同一来源 1 分钟失败 5 次后 10 分钟内一律 `429`（与远程版相同）。
- **来源过滤**：只接受 `10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`、`169.254.0.0/16`、`fc00::/7`、`fe80::/10`、回环，以及到达虚拟网卡的 `100.64.0.0/10` 来源（与监听地址的规则一致，第 3 节），其余来源一律返回 `403 forbidden_source`（不进入鉴权，也不读请求体）。
- **限制**：请求体上限 64 KiB；没有 CORS，不提供网页；响应只含界面需要的 DTO，不含 Cookie、站点签名地址、流地址与本机绝对路径（条目只用条目键与清单里的相对路径）。
- **版本**：请求与响应带 `X-Trace-Companion-Protocol`（从 `1` 开始，与远程版协议各自编号）、程序版本与通道；协议不同返回 `409 protocol_mismatch` 并带两边版本（NFR-MOB-VER-001）。
- **日志**：记连接、配对、移除与错误类别、设备 ID 与来源地址；不记令牌、配对码、请求体与媒体路径的查询串。哨兵测试覆盖。

<a id="endpoints"></a>
### 5.2 只读端点（`/api/v1/`，JSON）

| 方法与路径 | 对应的服务层 | 说明 |
| --- | --- | --- |
| `GET status` | `AppService`、`AuthService.Statuses`、`SettingsService.Locations` | 电脑名、版本、通道、运行时长、四个站点的登录状态与昵称、缓存位置剩余空间；电脑端一个站点都没登录时 `loggedIn=false` |
| `GET feed` | `FeedService.Page` | 参数同电脑端：游标、方向、站点筛选、订阅；每页 30 条 |
| `GET feed/subscriptions` | 主页侧栏同一查询 | 订阅头像条：直播中、未读圆点、排序同电脑端 |
| `POST feed/seen` | 主页“点开订阅”同一路径 | 标记一个订阅已读，与电脑端共享（DEC-085 ⑥），电脑端界面随 `feed:changed` 更新 |
| `GET/PUT feed/position` | `companion.json` 的 `homePositions` | 这台手机自己的阅读位置 |
| `GET library`、`GET library/items/{key}` | `LibraryService.List`、`Item` | 媒体库与本机详情 |
| `GET media/{key}/{relPath}` | `LibraryService.MediaPath` | 第 6 节 |
| `GET subscriptions`、`GET subscriptions/{id}`、`GET subscriptions/accounts` | `SubscriptionService`、`LiveService.Status` | 订阅列表、详情（内容走 `feed`）、全部账号；开播状态只读最近一次查到的，不为手机额外查询 |
| `GET tasks/downloads`、`GET tasks/downloads/{id}` | `CacheService.Tasks`、`TaskDetail` | 下载任务与逐文件详情 |
| `GET tasks/queues`、`GET live/recordings`、`GET live/incidents`、`GET checks/plan`、`GET conflicts`、`GET transfers` | 对应服务方法 | 缓存队列、直播录制、录制异常、检查计划（手机放在“订阅”里，同电脑端）、内容冲突（只读）、导入导出（只读）；手机按电脑端任务页的四组（进行中、直播、待处理、记录）组织 |
| `GET events` | `EventHub` | WebSocket，第 5.4 节 |

<a id="commands"></a>
### 5.3 控制端点

| 方法与路径 | 对应的服务层 |
| --- | --- |
| `POST tasks/downloads/{id}/pause`、`resume`、`cancel`、`retry`、`remove`；`POST tasks/downloads/clear-finished` | `CacheService.Pause` 等，规则与电脑端完全相同 |
| `POST tasks/queues/{id}/pause`、`resume`、`cancel` | `CacheService.PauseCacheQueue` 等 |
| `POST live/start`（`accountKey`）、`POST live/recordings/{id}/stop` | `LiveService.Start`、`Stop`；微博与 X 账号按现有规则拒绝 |
| `POST checks`（`subIds`，空为全部）、`POST checks/account`、`POST checks/cancel` | `UpdateService.Check`、`CheckAccount`、`Cancel` |
| `DELETE devices/self` | 手机解除配对 |

- 命令返回服务层的结果或错误类别（`auth_required` 带站点、`risk_control`、`rate_limited`、`conflict` 等），手机按 FR-ERR-001 的口径显示。
- 第二版增加 `POST parse-and-cache`、`PUT subscriptions/{id}/tags`、`POST conflicts/{id}/resolve`（[#122](https://github.com/chialecode/trace/issues/122)）。
- 不提供：登录与凭证、设置修改、删除媒体、导入导出、远程版管理、退出电脑端（FR-MOB-025）。

<a id="events"></a>
### 5.4 事件

- `GET events` 升级为 WebSocket，转发 `task:changed`、`cache:changed`、`cache_queue.updated`、`feed:changed`、`subs:changed`、`subs:check`、`live:changed`、`auth:changed`（只带站点与状态），另有 `bye{reason}`。进度类事件按连接合并，至多每秒一次。
- 心跳：服务端每 5 秒 ping，15 秒收不到 pong 就关闭；手机 15 秒收不到任何帧就判定断开（FR-MOB-006）。
- 每台手机的发送队列有上限（256 条），满了就丢弃最旧的进度事件并标记“需要全量刷新”，不阻塞 `EventHub`（INV-13）。

<a id="media"></a>
## 6. 媒体

- 一律经 `LibraryService.MediaPath(key, relPath)` 白名单读取登记文件（INV-08），支持 `Range` 与 `If-Range`，按实际文件扩展名返回准确的 `Content-Type`（如 `video/mp4`、`audio/flac`、`image/jpeg` 等），响应带 `Cache-Control: no-store`。
- **缩略图**：`?w=480` 返回电脑端缩放后的 JPEG（视频与录像用登记的封面）；**看图**：`?w=2560` 按最长边缩放，“查看原图”不带参数返回原文件；GIF、实况片段与视频按原文件返回，不转码。
- **视频**：系统播放器（`plugins/videoplayer`）不认自签名证书，手机端在 Go 里开一个只监听 `127.0.0.1` 随机端口的转发：播放器请求 `http://127.0.0.1:<端口>/media/...?t=<一次性票据>`，转发校验票据后用固定指纹的 TLS 1.3 与设备令牌向电脑端请求并原样回传（含 `Range` / `If-Range` 与 `206 Partial Content`）。票据 128 位随机、只在内存、绑定一个条目文件、播放器关闭即作废。Android 9+（`targetSdk=36`）默认拦截对 `127.0.0.1` 的明文请求，须由手机端的 Kotlin 原生模块提供 `network_security_config.xml`（仅放行 `127.0.0.1` 与 `localhost`，其余主机一律禁用明文；经原型 [#117](https://github.com/chialecode/trace/issues/117) 实测通过）。
- 电脑端不转码。手机不能解码的编码（AV1、HEVC、FLAC 等，或超出设备解码器上限的分辨率）采用**两层判定**：优先由 Kotlin 原生模块调用 `MediaCodecList(ALL_CODECS)` 预检目标编码与分辨率上限，再以运行期播放器错误回调（`State.Error != ""`）以及“预期有画面但 `State.Ready && Width == 0 && Height == 0`”（Android `MediaPlayer` 丢弃视频轨只播音轨时不报错误）兜底，命中后明确说明原因（FR-MOB-014）。

<a id="app"></a>
## 7. 手机端应用

### 7.1 工程与依赖

- 独立的 MyGo 应用 `cmd/trace-mobile/`，自己的 `mygo.json`：名称“TRACE”，标识符 `io.github.chialecode.trace`（正式版图标与旁边的 `-dev` 图标由 MyGo 自动按通道选用），`android.minSdk` `26`，`android.permissions` 声明 `["CAMERA"]`（`INTERNET`、`ACCESS_NETWORK_STATE`、`POST_NOTIFICATIONS` 由 MyGo 自动加入清单）；可选 `android.adaptiveIcon` 与 `android.splash`。开发版由 MyGo 自动加后缀（“TRACE Dev”、`io.github.chialecode.trace.dev`），两者可在同一台手机并存（与电脑端通道隔离一致）。
- 依赖边界：只链接 MyGo、`ui`、`ui/mobile`、`plugins/videoplayer`、手机端自有的小型 Android Kotlin 原生模块（一并承载 `network_security_config.xml`、`MediaCodecList` 解码能力查询，以及基于 AndroidX CameraX `Preview`+`ImageAnalysis` 与 `com.google.zxing:core` 的离线二维码扫码 `native.View`），以及新的纯 Go 客户端包 `internal/companionclient`（协议类型与 DTO 与电脑端共用无依赖的 `internal/companionapi`）；**不链接** `internal/site/...`、`internal/credential`、`internal/download`、`internal/live`、`internal/subscription`、`internal/library`、`internal/service`。`tools/check` 用 `go list -deps ./cmd/trace-mobile` 检查（同 `trace-server` 的做法）。实现时同步更新[架构 §1](architecture.md#architecture) 的 INV-05 包列表（`internal/companion`、`internal/companionapi`、`internal/companionclient` 不依赖 MyGo；`cmd/trace-mobile` 是新的 MyGo 引用方）与 `tools/check` 的依赖方向清单（[#118](https://github.com/chialecode/trace/issues/118)、[#119](https://github.com/chialecode/trace/issues/119)）。
- 帖子卡片、时间与大小格式化等纯展示代码，能与电脑端共用的抽成不依赖服务层的小包；不为共用而把 `internal/ui` 整体搬进手机端。

### 7.2 界面结构

- `mygo.NewTabs` 五个标签：主页、媒体库、订阅、任务、我的电脑；各标签内用 `ui.Router` 且 `SystemStack = true`（Android 返回键与预测性返回、iOS 左滑返回）。细节见[界面 §3.12](../ui.md#mobile)。
- 列表用虚拟化列表、`PullToRefresh`；看图用 `ui/mobile.Zoomable`；网格列数按宽度（< 600 dp 两列，600～900 三列，> 900 四列）。
- 图片由客户端取字节后交给 `ui.Image`（只放内存，上限 128 MiB，按最近使用淘汰）；不用会按缓存头写磁盘的 `ui/netimage`。

### 7.3 本机保存的东西

- `preferences`：配对记录 `{node, name, channel, fp, addrs[], lastAddr, pairedAt}`；手机端自己的界面设置（暂无）。
- `mygo.Secrets`：设备令牌（Android 密钥库加密）。
- 不保存任何内容、缩略图、列表与阅读位置；系统在后台结束应用时只用 `App.OnSaveState` 保存当前路由，恢复后先重新连接再取数据。解除配对清掉以上全部。

### 7.4 生命周期

- 前台（`resume`、`App.OnActivate`）才连接；进入后台（`suspend`）立即关闭 WebSocket 与转发、停止播放，不常驻后台、不用推送服务。
- 自动重连只在前台进行，按 2、5、10、30 秒退避，之后每 30 秒；手机没联网（`mygo.Network`）时不重试，显示“手机没有连接网络”。

<a id="states"></a>
## 8. 连接状态与错误

| 状态 / 判定 | 手机显示 | 操作 |
| --- | --- | --- |
| 没有配对记录 | 配对引导页 | 扫码 / 手动输入 |
| 正在连接 | 连接页（电脑名、正在尝试的地址） | 取消 |
| TCP 被拒（`ECONNREFUSED`） | “电脑上的 TRACE 没有运行，或没有打开‘允许手机连接’” | 重试、重新扫码 |
| 超时 / 主机不可达 | “找不到电脑：电脑可能关机、睡眠，或手机与电脑不在同一网络” | 重试、重新扫码 |
| 手机没联网 | “手机没有连接网络” | — |
| 证书指纹不符 | “电脑的身份变了，请重新扫码”（不自动信任） | 重新扫码 |
| `401` | “这台电脑已移除本机，请重新扫码” | 重新扫码 |
| `403`（来源不被接受） | “电脑只接受同一局域网或 VPN 内的连接” | 重试 |
| `409 protocol_mismatch` | 两边版本，以及“请更新手机上的 TRACE / 电脑上的 TRACE” | 打开下载页（更新手机时） |
| `429` | “尝试次数过多，请 10 分钟后再试” | — |
| `status.loggedIn=false` | “请先在电脑上登录一个站点” | 重试 |
| `bye: exit / update / disabled / revoked` | “电脑上的 TRACE 已退出 / 正在更新 / 已关闭手机连接 / 已移除本机” | 重试、重新扫码 |
| 已连接 | 正常页面；重新连上后回到断开前的路由 | — |

- 断开时立即盖上连接页、停止播放、清空内存里的列表与图片；不显示断开前的内容。
- 地址失效时依次尝试配对记录里的全部地址；都失败才进入上表的错误页。

<a id="risks"></a>
## 9. 原型验证结论（[#117](https://github.com/chialecode/trace/issues/117)）

以下四项风险均已在隔离原型与 Android 16（API 36，x86_64 模拟器）及本机 Windows 11 环境中实测验证，脱敏回执见 `artifacts/mobile-proto/proto-report.json`：

1. **系统播放器与自签名证书（通过，须配 `networkSecurityConfig`）**：
   - **未配置 `networkSecurityConfig` 时被拦截**：Android 16（`targetSdk=36`）的 `MediaHTTPConnection` 在发起 socket 连接前即拒绝 `http://127.0.0.1:<端口>`（代理命中 0 次，`NuCachedSource2` 返回 `-1` 重试）。
   - **写入方式**：MyGo `v0.10.0` 的 `mygo.json` 无 `networkSecurityConfig` 字段，`mygo-plugin.json` 的 `android.application` 也只能向 `<application>` 内追加子标签而不能改 `<application>` 属性。经实测，在 MyGo **Kotlin 原生模块**中放置 `kotlin/src/main/AndroidManifest.xml`（设 `android:networkSecurityConfig="@xml/trace_network_security_config"`）与 `kotlin/src/main/res/xml/trace_network_security_config.xml`（`<base-config cleartextTrafficPermitted="false" />`，仅对 `127.0.0.1` 与 `localhost` 放行明文），Gradle 清单合并后即生效（`aapt2` 与 `NetworkSecurityPolicy` 均核对 `127.0.0.1=true`、`10.0.2.2=false`）。
   - **播放、Range 拖动与票据隔离通过**：手机端 Go 侧固定叶证书 DER SHA-256 指纹 + TLS 1.3 连接电脑模拟服务，回环代理校验一次性 `?t=<票据>`（错票据与销毁后旧票据均返回 `403`）。在 10.22 MiB（150 秒、`moov` 前置）合成 AVC+AAC MP4 上，`plugins/videoplayer` 正常起播（`640×360`），`Seek` 跳转 `331 ms → 112,743 ms` 时电脑端实收 `Range: bytes=7778518-` 并返回 `206 Partial Content`（2,941,246 字节）。无需启用备选自研解码或明文票据方案。
2. **Windows 防火墙（已观测本机行为；真机局域网入站留实机检查）**：
   - 标准用户（中等完整性级别、无管理员提权）下，按用户路径运行的 Go 进程调用 `net.Listen` 监听本机局域网 IPv4（`:47382`）本身不会报错；本机与模拟器 NAT（`10.0.2.2`）因源自本机进程可直达，不能代替真实局域网入站验证。
   - 本机 Windows Defender 防火墙三类配置文件均为 `ON` + `BlockInbound,AllowOutbound` + `InboundUserNotification=Enable`，且当前 Wi-Fi 归类为 `Public`。未通过管理员 UAC 批准放行时，Windows **自动为该可执行文件创建了两条持久的 `Inbound Block` 规则（`Public` 配置、TCP 与 UDP）**；一旦存在阻止规则，后续重启不会再弹提示。
   - **设计落点**：“设置 › 移动端”须写明三点：① 首次开启触发 Windows 防火墙提示时需管理员确认，建议仅勾选“专用网络”；② 若当前 Wi-Fi 在 Windows 中为“公用网络”，需先在系统网络设置中改为“专用网络”；③ 若曾误点取消生成阻止规则，需在“允许应用通过 Windows Defender 防火墙”里手动为 TRACE 勾选“专用”放行。真机跨设备局域网连接留待实现阶段的 `needs-user` 实机检查确认。
3. **手机解码能力与“不能播放”判定（通过，确定两层判定方案）**：
   - Android 16 模拟器通过 Kotlin 原生模块查询 `MediaCodecList(ALL_CODECS)`，检出 `video/avc`（硬解至 4096×4096）、`video/hevc`（硬解至 4096×4096）、`video/av01`（`c2.android.av1-dav1d.decoder` 软解，上限 2048×2048，不含 4K）、`audio/flac`（`c2.android.flac.decoder`）与 `audio/mp4a-latm`。
   - 经回环代理 + `plugins/videoplayer` 实测：合成 AVC、HEVC、AV1（320×240，3 秒）与遵循 RFC 9639 的 FLAC-in-MP4（`fLaC`/`dfLa`，48 kHz 立体声，3.072 秒）均正常起播与拖动。注：裸 `.flac` 若被服务端错标为 `Content-Type: video/mp4` 会触发 `MediaPlayer error 1/-2147483648`，因此电脑端媒体端点须按扩展名返回准确 `Content-Type`。
   - **关键陷阱**：对“视频轨为不支持编码 + 音轨为有效 AAC”的合成 MP4，Android `MediaPlayer` **不触发错误回调**，而是报告 `ready=true, playing=true, err="", width=0, height=0`（黑屏只播声音）；对仅含不支持视频轨的 MP4 则报 `MediaPlayer error -38/0` 而非稳定的 `UNSUPPORTED` 码。因此**不能只靠播放器错误码**，定案为两层判定（第 6 节）：① 播放前用 `MediaCodecList` 核对条目编码与分辨率上限；② 运行期同时拦截 `State.Error != ""` 与 `State.Ready && 预期视频 && Width == 0 && Height == 0`。
4. **构建链与二维码扫描（通过，发现三项 Windows 上游限制并定案扫码方案）**：
   - **MyGo `v0.10.0` Windows 构建限制与绕过**：① `internal/mobile/android/embed.go` 未嵌入 `gradlew.bat`，而 `cmd/mygo/android.go` 在 Windows 上调用 `.mygo/android/gradlew.bat` 会直接报找不到文件，须在 `.mygo/android/` 补放调用 `gradle-wrapper.jar` 的 `gradlew.bat`（`mygo` 更新宿主文件时不会删除该文件）；② MyGo 固定 Gradle `8.14.3`（内置 Kotlin DSL `2.0.21`）且配置 `jvmToolchain(17)` 但未启用 foojay 解析器，使用 Android Studio 自带 JBR `25.0.4` 会以 `IllegalArgumentException: 25.0.4` 崩溃，须将 `JAVA_HOME` 指向 **JDK 17**（实测 Eclipse Temurin `17.0.21.1+2` 通过）；③ 本机存在代理时，Java/Gradle 不读取 `HTTP(S)_PROXY` 环境变量，须经 `GRADLE_OPTS` 与参数转发 `-Dhttp.proxyHost`/`-Dhttps.proxyHost`。
   - **耗时、体积与标识实测**：首次冷构建 `mygo build -platform android`（三 ABI 含首次下载 Gradle/AndroidX 依赖）`4m 16s`，APK `39.5 MB` / AAB `16.9 MB`；热构建单 ABI `mygo build -platform android/arm64` `4.8s`，APK `14.2 MB` / AAB `6.9 MB`；加入 Kotlin 原生模块（含 CameraX `1.6.2` + `com.google.zxing:core:3.5.4` + `network_security_config`）后单 ABI APK `14.6 MB`（R8 压缩后仅增 `+0.38 MiB`）。`mygo dev -platform android` 首次冷构建+安装 `37.5s`，仅改 Go 代码增量热推送 `libmygoapp.so` 耗时 `2.6s`。未配置 `android.keystore` 时默认以调试密钥（`CN=Android Debug`）签名；开发版自动加 ` Dev` 与 `.dev` 后缀并与正式版包名共存。
   - **二维码扫描定案**：采用同一 Kotlin 原生模块提供 `native.View`（`ui.NativeView` 嵌入），以 **AndroidX CameraX `1.6.2`（`Preview` + `ImageAnalysis`，Apache-2.0）+ `com.google.zxing:core:3.5.4`（Apache-2.0）** 本地离线实时解码，并在解码器里加 `LuminanceSource.invert()` 回退以兼容深色反相二维码（已实测识别 TRACE `yeqown/go-qrcode/v2` 生成的常规与反相 `trace-pair:` 二维码）。不采用 `plugins/camera`（只支持落盘拍照 `TakePhoto`、无实时帧流）、纯 Go 解码库（许可或维护度不符，见[选型 §4](../research/2026-11-26-mobile.md)）或 Google Play Services Code Scanner（强依赖 GMS，国内无 GMS 手机不可用）。

<a id="build"></a>
## 10. 版本、通道、构建与分发

- **版本**：手机端版本号跟随电脑端的版本线（`0.1.x`，开发版带 `-dev.<时间>`）；能否连接只看协议版本，手机端不必每周跟着更新（NFR-MOB-VER-001）。
- **通道**：正式版手机只配对正式版电脑端，开发版只配对开发版（二维码与 `pair` 都校验 `channel`）；`trace-test` 演练身份不参与移动端。
- **构建环境**：**JDK 17**（MyGo `v0.10.0` 固定 Gradle `8.14.3` + `jvmToolchain(17)`，与 Android Studio 附带的 JBR 25 不兼容，需通过 `JAVA_HOME` 指定 JDK 17）、Android SDK（`compileSdk`/`targetSdk` 36，`build-tools` 36.0.0）、NDK，`mygo doctor` 检查；Windows 上使用 MyGo `v0.10.0` 需在 `.mygo/android/` 补放 `gradlew.bat`（并在使用代理时向 Gradle 转发 `-Dhttp(s).proxyHost/Port`），待上游修复后移除该绕过（`OSS-010`）。Gradle 与依赖在第一次构建时下载。iOS 只能在装 Xcode 的 Mac 上构建。
- **打包**（计划）：`go run ./tools/package -mobile` 调用 `mygo build -platform android`（或按需单 ABI `android/arm64`，约 `14.6 MB`；全三 ABI APK 约 `39.5 MB`），输出 `dist/trace-mobile/<通道>/`（只留最新一份）；开发版用调试签名。正式版签名密钥放仓库外与 GitHub `release` 环境（同更新签名私钥的做法），生成与保管要用户授权；APK 由发布流程附在 GitHub Release（DEC-085 ⑦），不上架应用商店。打包与发布改动属于高风险清单。
- **手机端更新**：没有应用内自更新；遇到 `protocol_mismatch` 且需要更新手机时，打开 GitHub Release 页面。

<a id="phases"></a>
## 11. 分期与 issue

| 阶段 | 内容 | issue |
| --- | --- | --- |
| 准备 | MyGo 升级到 v0.10.0；本机 Android 构建环境 | [#115](https://github.com/chialecode/trace/issues/115)、[#116](https://github.com/chialecode/trace/issues/116) |
| 原型 | 第 9 节四项风险 | [#117](https://github.com/chialecode/trace/issues/117) |
| MVP | 电脑端移动端服务；手机端工程、配对与连接状态；主页、媒体库、看图与视频；订阅与任务的查看与控制 | [#118](https://github.com/chialecode/trace/issues/118)、[#119](https://github.com/chialecode/trace/issues/119)、[#120](https://github.com/chialecode/trace/issues/120)、[#121](https://github.com/chialecode/trace/issues/121) |
| 第二版 | 分享链接让电脑缓存、标签与冲突处理、弹幕字幕与章节、录制实时预览、局域网自动发现、手机连接时阻止电脑睡眠 | [#122](https://github.com/chialecode/trace/issues/122) |
| 以后 | iOS；经远程 VPS 等方式的外网访问；任务完成通知 | [#123](https://github.com/chialecode/trace/issues/123) |

实现到可以实机检查时，再建 `needs-user` issue：Android 真机上的配对、浏览、播放、断开与 Windows 防火墙检查；正式版 APK 签名密钥的生成与保管授权。

<a id="verify"></a>
## 12. 验证

- **单元**：配对码一次性与过期、手动配对时身份码不符就中止且不发送短码、`pairId` 不带发起时的 `pairSecret` 拿不到令牌、令牌只能取一次、通道校验、令牌哈希与撤销、监听地址的选择（物理网卡上的 `100.64.0.0/10` 不监听）、私有来源过滤、失败限流、协议版本、`MediaPath` 越界、事件队列满时不阻塞、`companion.json` 损坏恢复；令牌、配对码不进日志与响应的哨兵测试。
- **回环集成**：用 `internal/companionclient` 当作手机，对接真实 `Backend` 与合成媒体库：配对 → 读信息流与媒体库 → Range 读视频 → 控制下载任务 → 电脑端退出时收到 `bye`。
- **界面**：手机端用 `ui.Tester` 在 360×800、412×915、800×1280 截图与断言（连接页的每种原因、主页、媒体库、看图、任务、订阅）；电脑端“设置 › 移动端”进 `tools/uishots` 三档宽度。
- **模拟器冒烟**（有构建环境时）：`mygo dev -platform android` 安装后用自动化插件走一遍配对与浏览；CI 暂不跑。
- **实机**：`needs-user` 检查，没做保持 `not-run`。
