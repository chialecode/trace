# 技术设计：架构、接口与原生界面

技术设计说明系统“应当怎样工作”，按专题分四份：本页（进程与分层、代码目录、服务接口、站点客户端、凭证、原生界面、不变量、窗口与托盘、远程无头扩展边界）、[数据、缓存与信息流](data-and-cache.md)、[订阅、检查更新与自动缓存](subscriptions.md)、[直播](live.md)。与代码不一致时按[冲突规则](../dev-rules/workflow.md#conflicts)处理。选型依据见[技术选型研究](../research/2026-11-05-stack.md)和[媒体与存储选型](../research/2026-11-05-media.md)，协议见[哔哩哔哩协议资料](../research/bilibili.md)。

<a id="architecture"></a>
## 1. 进程与分层

单个 Go + MyGo 进程（DEC-032）：用 `mygo.NewWindow(mygo.WindowOptions{Content: ui.View(…)})` 与 `github.com/egoist/mygo/ui` 直接渲染原生界面（Windows 下 Direct3D 11 加速，无可用显卡驱动时自动退回 WARP 软件渲染），不启动 WebView2 子进程，也不依赖客户机的浏览器运行时。没有单独的后台守护进程；窗口关到托盘后原生窗口销毁、释放界面纹理与视图状态，进程继续运行，缓存任务、定时检查、自动缓存、直播录制和登录续期都在这个进程里进行，托盘“退出”才结束（DEC-030，第 8 节）。MyGo 的 `userData`、`cache`、`logs` 路径用 `App.SetPath` 改到数据目录里（`<数据目录>/mygo`、`cache`、`logs`），所以 TRACE 不往 `%APPDATA%` 和用户主目录写东西（NFR-DATA-003，数据目录见[数据 §1](data-and-cache.md#storage)）。

```text
internal/ui（mygo/ui 声明式视图、路由、看图层、播放层） ──(Go 接口调用 + 事件订阅)──> internal/service（薄层：登录检查、参数校验、错误转换、DTO）
      │                                                                                      │
      ├── internal/player（Windows Media Foundation + mygo/audio + 纯 Go 弹幕与字幕叠加）     ├── internal/download（任务、分段下载、选流、合并、升级）
      │        │                                                                             ├── internal/library（清单、索引、引用、安全删除、媒体路径白名单）
      │        └──────> LibraryService.MediaPath(key, relPath)（INV-08 路径白名单）           ├── internal/subscription（订阅、检查更新、定时调度、自动缓存）
      │                                                                                      ├── internal/live（开播状态、HLS fMP4 录制与收尾）
      └── internal/shell（唯一管理 MyGo 窗口、托盘、单实例、对话框、外部链接与通知的包）       ├── internal/site/bilibili（站点协议，纯 Go）
                                                                                             ├── internal/credential（DPAPI 凭证、手动凭证解析、原子续期）
                                                                                             └── internal/storage、settings、uistate、appdirs
```

- **依赖方向只能向下（INV-05）**：`internal/site/bilibili`、`internal/download`、`internal/library`、`internal/credential`、`internal/storage`、`internal/subscription`、`internal/live`、`internal/settings`、`internal/uistate`、`internal/appdirs`、`internal/update`、`internal/service` **完全不依赖 MyGo（不引用 `github.com/egoist/mygo` 及其子包）**；只有 `main.go`、`internal/shell`、`internal/ui`、`internal/player`（仅用 `mygo/audio` 与 `*ui.Bitmap`）和 `internal/locales`（MyGo 自带字符串与安装界面的中文目录）引用 MyGo。`internal/site/bilibili` 也不依赖存储、缓存、订阅和录制代码。
- 后端服务需要的外壳能力（推送事件、选目录、打开文件夹、打开外部链接）由 `internal/service` 通过纯 Go 小接口注入，测试和未来的无头模式均可注入对应实现。
- 窗口：标题 “TRACE”（开发通道为 “TRACE Dev”），默认 1280×860，最小 720×560。外部链接一律用系统浏览器打开（`Shell.OpenExternal`）。

<a id="layout"></a>
## 2. 代码目录（DEC-020、DEC-032）

```text
main.go                       组装依赖、获取单实例与数据目录锁、启动服务与原生窗口
mygo.json                     MyGo 打包配置：正式版身份、版本线 0.1.0、图标、发布者与版权、中文目录（i18n）、更新公钥与 GitHub 仓库；
                              打包工具按通道临时改写（开发版 trace-dev、ptest 演练 trace-ptest）
CHANGELOG.md                  正式版每个版本的更新内容（Release 说明与“更新内容”）
assets/                       应用图标 icon.png（正式版）与 icon-dev.png（开发版与 ptest），tools/icons 生成
internal/
  shell/                      MyGo 桌面外壳适配：NewWindow、窗口关闭与重建、安静模式、托盘与菜单、单实例、通知、对话框、外部链接、在文件夹中显示；
                              启动参数（--quit-for-update、--background）、mygo.Updater 的接入与卸载项版本（updates.go）
  update/                     更新逻辑（纯 Go，不依赖 MyGo）：检查与下载的状态机（controller.go）、TRACE 的更新源与校验（source.go）
  locales/                    中文目录 zh-Hans.json 与 MyGo 生成的访问代码（安装界面与 MyGo 自带字符串）
  ui/                         mygo/ui 原生界面：图标栏 + 上下文侧栏 + 页面的自适应布局、路由、登录页、主页信息流、订阅（列表/详情/导入）、
                              解析页、视频详情、用户页、媒体库、任务页、设置页、看图层、播放层及界面内存测试（ui.NewTester）
  player/                     原生媒体播放与叠加：Windows Media Foundation（IMFSourceReader 纯 Go COM 调用）解码 MP4 音视频、
                              mygo/audio 音频时钟与输出、纯 Go 弹幕排版器（danmaku.go）与 WebVTT 字幕解析匹配（subtitle.go）
  appdirs/                    构建信息（版本、通道、提交、构建时间，由 -ldflags 注入）；通道（release / dev / ptest / source）与安装、数据、缓存位置的推算，“文档”文件夹的系统登记位置，数据目录独占锁
  site/bilibili/              站点协议：client.go 请求与节奏、errors.go 错误分类、wbi.go 签名、link.go 链接识别、
                              video.go、user.go、dynamic.go、playurl.go（含清晰度排序表）、login.go 扫码、
                              refresh.go 登录续期、relation.go 关注列表与分组、danmaku.go 弹幕分段、subtitle.go 字幕、
                              live.go 开播状态与取流；testdata/ 夹具
  credential/                 DPAPI 加密保存；手动凭证文本解析（只保留白名单字段）；续期时的原子替换
  service/                    纯 Go 服务层：auth、parse、cache、feed、library、settings、history、subscription、update、following、live、app；dto.go、errors.go
  subscription/               订阅模型与 subscriptions.json（含 .bak）、更新记录（updates.db）、检查更新、定时调度与退避、每小时 200 次令牌桶、自动缓存规则
  live/                       直播录制器：取流、HLS fMP4 分段写入、断流重连與换段、mp4ff Defragment 收尾与校验
  library/                    缓存条目模型、清单（item.json）读写、SQLite 索引、从清单重建、引用、安全删除、信息流查询、MediaPath 路径白名单
  download/                   任务队列与状态机、分段下载器（8 MiB 续传、重试、节点轮换）、选流、合并（mp4ff）、校验、升级探测、弹幕与字幕保存
  settings/、uistate/         设置（settings.json）；界面状态（ui-state.json，主页看过的最新一条、窗口尺寸、最后路由等）
  storage/                    数据目录、原子写、最近解析（history.json）、SQLite 打开与迁移、数据文件的结构版本（schema.go）
tools/check/                  质量门禁（一条命令）
tools/checkdocs/              文档、公开内容与依赖方向检查
tools/uishots/                用 ui.NewTester 渲染全部页面的必测状态与三档宽度，输出 PNG 截图与结构化断言回执 assertions.json
tools/smoke/                  真实进程端到端冒烟（启动 trace.exe、单实例与数据目录锁、托盘关闭与重建、零杂散目录校验）
tools/package/                开发版打包（解包文件 + NSIS 按用户安装包）与本机自动安装（install.go）
tools/release/                正式版发布：检查、构建签名、ptest 升级演练、可选发布；release.yml 的静态检查
tools/icons/                  绘制两个通道的应用图标
tools/internal/build/         打包共用：版本号、通道配置、-ldflags、VERSION.txt、THIRD-PARTY-NOTICES.txt、登记替换、Windows 卸载项与进程读取
```

测试跟着代码走，按能力命名。具体文件名由 B 按需要调整，层次和依赖方向不变。

<a id="services"></a>
## 3. 服务接口（`internal/service`）

`internal/service` 提供纯 Go 业务接口 `BackendAPI`，实现为 `Backend`，由 `internal/ui` 直接在进程内调用（不经过 JSON 跨进程 IPC）。涉及网络的调用接收 `context.Context`；动态 ID、条目键和订阅 UID 使用字符串，进程内 `mid`/`cid` 等数字标识使用 `int64`，不经 JavaScript 数字转换。下表用服务分组描述契约，实际方法签名以代码为准；UI 接口不暴露站点客户端或库对象。除 `AuthService`、`SettingsService` 和 `AppService` 外，所有业务方法先检查登录：没有有效凭证就返回 `auth_required`，**不发请求**（FR-AUTH-005）。`FeedService` 只读本机数据，但同样要求已登录（未登录时整个界面只有登录页和设置页）。

| 服务方法 | 作用 |
| --- | --- |
| `ParseService.Resolve(input)` / `Video(id)` / `User(mid)` / `UserVideos(mid, page)` / `UserDynamics(mid, offset)` | 链接识别与解析；视频与用户成功后写入最近解析（不等缩略图，界面随后调用 `FetchHistoryThumbs` 补上封面与头像，DEC-038）；动态一页 `{items, offset, hasMore}`，每条带 `cacheKey` |
| `AuthService.Status()` | `{state: "logged_in"\|"logged_out"\|"expired"\|"unknown", account?}` |
| `AuthService.StartQR()` / `PollQR(flowId)` / `CancelQR(flowId)` | 扫码流程，返回登录链接（由界面在内存生成二维码图片），不返回任何 Cookie |
| `AuthService.LoginWithCookie(text)` | 手动凭证：解析、用 nav 校验、保存；只返回账号摘要，或不含输入内容的 `{kind, message}` 错误；文本上限 8 KiB（第 5 节） |
| `AuthService.Logout()` | 先暂停全部缓存任务与录制，再删除本机凭证 |
| `AuthService.Renewal()` | 续期状态：`{available, reason?, lastRenewedAt?, lastError?}`（[§5.1](#renewal)） |
| `CacheService.CacheDynamic(id)` / `CacheVideo(bvid)` / `CacheSupplement(bvid)` | 建立缓存任务（或为旧视频补存弹幕和字幕），立即返回任务 ID；同一对象已有进行中的任务时返回该任务 |
| `CacheService.States(keys)` | 批量查询状态圆点 `{key: {dot, label, progress, missing[], upgrade?}}`（[数据 §2.6](data-and-cache.md#merge)） |
| `CacheService.Tasks()` / `Pause(id)` / `Resume(id)` / `Cancel(id)` / `Retry(id)` / `RemoveTask(id)` / `RemoveFinishedTasks()` / `TaskDetail(id)` | 任务列表与操作（已结束任务只支持移除，移除失败/部分完成任务清理登记的 `.part` 临时文件）；详情含逐文件进度清单与尝试记录（[数据 §2.4](data-and-cache.md#tasks)） |
| `CacheService.CheckUpgrade(keys, force)` / `UpgradeCandidates()` / `Upgrade(key)` | 画质升级探测与升级（[数据 §2.7](data-and-cache.md#upgrade)）；`UpgradeCandidates` 返回最多 50 个待检查的视频（最久未检查的在前），任务页逐个调用 `CheckUpgrade` 以显示进度、可取消，遇风控、限流或未登录即停 |
| `FeedService.Page(cursor, direction, order, limit, filter?)` / `Anchor()` / `SaveAnchor(anchor)` / `NewerCount(anchor)` | 主页信息流（[数据 §4](data-and-cache.md#feed)）；`Anchor` 只记看过的最新一条，作下次启动时“有更新”的基准（DEC-040） |
| `LibraryService.List(filter)` / `Item(key)` / `MediaPath(key, relPath)` / `PlanDelete(key)` / `Delete(key)` / `Reveal(key, file)` / `Stats()` | 媒体库查询、本机媒体路径白名单解析（[数据 §3](data-and-cache.md#media-server)）、安全删除、在资源管理器中显示、占用统计 |
| `SettingsService.Get()` / `Update(patch)` / `ChooseCacheRoot()` / `OpenFolder(which)` / `Locations()` / `AddLibraryRoot()` | 设置、三个位置与通道（FR-STORE-001）、选择已有的缓存文件夹并接入（FR-LIB-006，[数据 §1](data-and-cache.md#storage)） |
| `AppService.Quit(confirm)` / `QuitPlan()` / `DiagnosticsSnapshot()` / `RecentLogs(maxLines)` | 退出前列出会暂停的任务与会结束的录制（第 8 节）；后台状态快照（索引重建、检查更新、自动缓存、录制情况）与脱敏后的最近运行日志读取（最多 500 行，DEC-033） |
| `HistoryService.List()` / `Remove(kind, id)` / `Clear()` / `HistoryThumbPath(name)` | 最近解析列表、单条删除、全部清空，以及本机缩略图路径白名单读取（`<数据目录>/thumbs/`，DEC-037） |
| `SubscriptionService.List()` / `Get(id)` / `Create(name, account?)` / `Update(id, patch)` / `Delete(id)` / `Link(id, site, uid)` / `Unlink(id, site, uid)` / `Move(site, uid, toId)` / `PreviewAccount(input)` / `OwnerOf(site, uid)` / `SetRules(id, rules)` / `ListCacheRules()` / `SaveCacheRule(rule)` / `DeleteCacheRule(id)` / `SetSubscriptionRule(id, ruleId)` | 订阅管理（[订阅 §1](subscriptions.md#model)）；关联账号前先用 `PreviewAccount` 解析头像与昵称确认，已属于别的订阅时返回 `conflict` 和原订阅摘要，用户确认后调 `Move` |
| `UpdateService.Check(ids)` / `Cancel()` / `Items(subId, cursor)` | 检查更新与更新记录（[订阅 §2](subscriptions.md#update-check)，不做已看标记，DEC-033）；`Check` 立即返回，进度经 `subs:check` 事件推送；`ids` 为空表示全部 |
| `FollowingService.Groups()` / `Page(groupId, pn)` / `Import(uids)` | 从登录账号的关注列表导入（[订阅 §5](subscriptions.md#following-import)） |
| `LiveService.Status(uids)` / `Start(uid)` / `Stop(recId)` / `Recordings(filter)` | 开播状态与录制（[直播](live.md)）；录制也出现在 `CacheService.Tasks()` 里 |

后端通过进程内事件总线（`EventHub`）推送 `cache:changed`（`{key, dot, progress}`，进度至多每秒一次）、`task:changed`、`feed:changed`（有新条目入库时）、`auth:changed`、`subs:changed`（订阅增删改）、`subs:check`（检查进度 `{done, total, current, stopped?}`）和 `live:changed`（开播状态与录制状态）。当窗口打开时，界面订阅器通过 `win.Update`（主线程改状态并重绘）更新界面状态；窗口关闭到托盘时，界面取消订阅，事件直接丢弃，不排队、不阻塞（INV-13）。

错误类别取[协议资料的错误分类](../research/bilibili.md#errors)，再加上 `invalid_input`、`unsupported_link`、`storage`（磁盘写入、空间不足）、`conflict`（账号已属于别的订阅）。站点及已分类的业务错误用 `*bilibili.SiteError{Kind, Message, Err}` 保留类别；进程内存储/校验错误也必须向调用者返回，不能吞成空结果或成功状态。原始响应、签名 URL 和 Cookie 不进入错误消息。

<a id="bilibili-client"></a>
## 4. 哔哩哔哩客户端

- 一个 `http.Client`：API 请求超时 15 秒，不自动跟随跳转（短链自己处理），公共请求头见协议资料第 2 节。
- 全局 API 请求间隔至少 500 毫秒（互斥锁 + 上次请求时间，可被 `context` 取消）。媒体文件下载不走这个间隔，由下载器的并发上限控制。
- WBI 密钥缓存 30 分钟，-352 时刷新一次；用户页访问标识按作者缓存 30 分钟。
- **只有登录模式**：客户端从凭证模块取 Cookie；取不到时，除扫码的两个 passport 接口和“用候选 Cookie 校验的 nav”外，任何请求在发出前返回 `auth_required`（INV-07）。不获取访客 Cookie。
- 任何接口返回 -101，或 nav 返回 `isLogin=false`：标记凭证 `expired`（不删文件），推送一次 `auth:changed`，异步暂停缓存任务与停止录制。
- 登录与退出后清空 WBI 与访问标识缓存。
- 所有返回体先检查 `code`，再按结构解析；字段缺失按 `schema_changed`。动态 ID 等大整数一律按字符串处理（`id_str`）。

<a id="credentials"></a>
## 5. 凭证

- 文件 `<数据目录>/credentials/bilibili.bin`：JSON（`cookies`、`refresh_token`、`mid`、`obtained_at`、`source: "qr"|"manual"`、`renewed_at`、`confirm_pending`、`pending_old_refresh_token`）经 DPAPI（当前用户范围）加密后原子写入。不用 `mygo.Secrets`（[选型研究 §3](../research/2026-11-05-stack.md#other-choices)）。
- 新写入采用上述 snake_case 字段，读取兼容旧 camelCase。普通替换/状态更新/退出均在成功落盘后才切换内存，失败保留原凭证并返回错误；已被站点确认失效时，即使失效标记无法落盘，也在内存阻止继续使用，原密文保留。
- 只有 `internal/credential` 和 `internal/site/bilibili` 接触明文；服务层只返回账号摘要。
- 日志里涉及请求时只记录路径和 `code`，不记录查询串和 Cookie。
- 扫码流程：每个流程有独立 ID 和 180 秒有效期；取消、过期或新流程开始后，旧流程的轮询结果一律丢弃；同一流程同一时间只有一个站点轮询。
- **手动凭证**：接受浏览器复制的 Cookie 请求头文本（可带 `Cookie:` 前缀，`;` 或换行分隔），上限 8 KiB。只保留 `SESSDATA`、`bili_jct`、`DedeUserID`、`DedeUserID__ckMd5`、`sid`；缺 `SESSDATA` 直接拒绝；用 nav 确认 `isLogin=true`，有 `DedeUserID` 时必须等于 nav 的 `mid`；通过后保存，失败不保存任何内容，也不改变已有凭证和状态；有扫码流程进行时，手动登录成功会取消该流程。输入框提交后立即清空字符串状态；原文只在这一次 Go 方法调用的内存里，不写日志，错误消息不回显。哨兵测试覆盖这条路径（INV-01）。

<a id="renewal"></a>
### 5.1 登录续期（FR-AUTH-003）

- 只对 `source=qr` 且有 `refresh_token` 的凭证续期（粘贴 Cookie 的登录不续期，DEC-031；`AuthService.Renewal()` 返回 `available=false, reason="manual"`）。
- 时机：启动后 1 分钟（或扫码登录确认后 1 分钟），以及应用运行期间每 12 小时；先问 `cookie/info`，`refresh=false` 就只记录检查时间。计入规则请求的每小时 200 次额度，退避期内跳过（[订阅 §3](subscriptions.md#schedule)）。
- 流程按[协议资料 §17](../research/bilibili.md#cookie-refresh)：生成 `correspondPath`（RSA-OAEP SHA-256 加密 `refresh_<timestamp>`）→ 取 `refresh_csrf` → `cookie/refresh`（传入旧 `refresh_token` 与旧 `bili_jct`）→ 用 nav 确认新 Cookie（`isLogin=true` 且 `mid` 不变）→ 加密原子替换凭证文件（新 Cookie + 新 `refresh_token`，`renewed_at`，`confirm_pending=true`，`pending_old_refresh_token=旧refresh_token`）→ 用**新 Cookie（新 `bili_jct` 作为 `csrf`）+ 旧 `refresh_token`** 调用 `confirm/refresh` 成功后清掉 `confirm_pending` 与 `pending_old_refresh_token`。下次续期检查时若 `confirm_pending` 仍在，先用保存的 `pending_old_refresh_token` 补做确认。
- 续期期间客户端持有互斥锁：续期只用一次单独的请求序列，不和其他请求交错。
- 失败处理：网络错误、风控、`schema_changed`（取不到 `refresh_csrf` 等）→ 旧凭证不动，记 `lastError`，下个周期再试，不打断使用；-101 或 86095 → 按“登录已失效”处理（[§4](#bilibili-client)）。任何失败都不写半份凭证。
- 刷新令牌与新旧 Cookie 只在 `internal/credential` 与 `internal/site/bilibili` 里出现；日志只记步骤名与 `code`（NFR-SEC-002，哨兵测试覆盖续期路径）。

<a id="frontend"></a>
## 6. 原生界面（`internal/ui` + `internal/player`）

- **路由状态机**：`/login`（扫码与粘贴凭证两个标签）、`/` 主页信息流、`/subscriptions`（圆形头像网格）、`/subscriptions/import`（从关注列表导入）、`/subscriptions/:id?tab=updates|cached|live|accounts|rules`、`/parse` 解析页、`/video/:id`、`/user/:mid?tab=dynamic|videos`、`/library?type=&sub=&mid=&sort=`、`/tasks`、`/settings/:category`（默认 `account`，含 `logs` 日志分类）。没有有效登录时，除 `/login`、`/settings/*` 外都转到 `/login`；收到 `auth:changed`（变为未登录或已失效）时同样转到 `/login`，关闭看图/播放层并退出原生全屏，记住原路由，重新登录后自动回到原路由，之前已加载的页面状态保留不清空（AC-08、FR-AUTH-005）。
- **图标栏 + 上下文侧栏 + 页面（NFR-UI-001、DEC-039）**：顶层 `ui.Row` 依次是图标栏（`renderRail`，64px）、上下文侧栏（`renderContextPanel`，主页/订阅/媒体库/视频/用户页 ≥ 1280px 为 272px、1000～1279px 为 240px；设置 ≥ 1000px 为 232px、更窄 72px；其余页面和 720～999px 不显示）和页面（`renderCenterColumn`，帖子类页面限宽 680px、设置 860px、媒体库等网格占满）。宽度规则在 `internal/ui/shell.go`。图标栏与侧栏是固定高度容器，只有页面内部的 `ui.List` 或 `ui.ScrollView` 纵向滚动；页面滚到任意深度时图标栏与侧栏的坐标盒不变，全窗口无横向溢出。页面顶部栏（标题、快速解析框、页面操作）不随内容滚动；没有侧栏时，主页“有更新”退回为时间线上方的头像行，媒体库的类型与来源退回为页面里的标签与“作者筛选”。
- **虚拟列表（NFR-PERF-001、NFR-UI-002）**：主页信息流、用户动态与投稿列表、媒体库网格、订阅圆形头像网格均使用 `mygo/ui` 的 `ui.List` 虚拟化渲染，只构建可见区域的条目节点。
  - 主页信息流固定按从新到旧排列（无顺序切换），支持双向游标翻页；每次打开从最新处开始，不恢复位置、不提示“N 条更新的内容”，只把看过的最新一条记为下次启动时的基准（`SaveAnchor`，DEC-040）；帖子显示数据按条目缓存，本机图片后台解码缩放，重绘不逐帧读媒体库；“有更新”列出上次使用时看过的最新内容之后有新缓存内容的订阅/作者头像，点击进入该人的单订阅已缓存视图并按人记录 `homeSeen` 时间戳（DEC-037）。主页帖子不放底部文字按钮，点图片看原图、点视频卡片播放、点头像进用户页。
  - 订阅详情只有“直播”“账号”两个标签和一个缓存规则按钮；规则在设置“缓存规则”里统一管理，多个订阅共用（DEC-040）。不做已看/未读标记或图标栏未读数字角标（DEC-033）。
  - 解析页最近解析条目显示本机缓存的封面/头像缩略图，点条目本身打开详情，单条操作收进右侧“⋯”菜单（在浏览器中打开、删除），标题栏“⋯”提供“全部清空”（二次确认）；媒体库每格只保留缩略图、一行标题、一行元信息和右下角“⋯”菜单（详情、在资源管理器中显示、删除），“重建索引”收纳在设置“存储”分类（DEC-037）。
  - 媒体库缩略图与标题按登记文件路径选择图片、分 P 或录像片段，选定后只打开一次播放器；弹幕与字幕按相同文件匹配。本机详情保留动态引用视频与已知未完成分 P，录像使用自身类型和直播间入口；未知统计/时长不显示成 0。用户点缓存时由缓存器重新获取完整站点元数据，渲染主页与本机详情不自动获取远程图片（[数据契约](data-and-cache.md#feed)、[界面](../ui.md#detail)）。
- **看图层（FR-IMG-001、DEC-033、DEC-040）**：在顶层 `ui.Overlay` 上画半透明遮罩与居中的深色圆角面板（`mediaPanelRect`，点遮罩关闭）；已缓存图片通过 `LibraryService.MediaPath` 读取本机原图，未缓存图片通过无 `Referer`（且 `http://` 转 `https://`）的加载器读取去掉尺寸后缀的原图。只保留少量半透明圆形悬浮按钮（右上角关闭、左右上一张/下一张、底部适应窗口/1:1），鼠标静止约 2 秒后自动隐藏，鼠标移动或按键时重新出现；支持滚轮、快捷键（`+`/`-`/`0`）、双击缩放、按住拖拽平移、左右键切换与 `Esc` 关闭。
- **沉浸式播放层与弹幕字幕（FR-LIB-002、FR-LIB-004、DEC-033）**：
  - 点击已缓存视频或直播录像打开播放层（与看图层相同的浮动面板，全屏时铺满窗口；打开即自动播放，DEC-040），通过 `LibraryService.MediaPath` 获取受白名单保护的本机 `.mp4` 路径，交给 `internal/player`（Windows Media Foundation `IMFSourceReader` 解码音视频 + `mygo/audio` 播放声音 + `*ui.Bitmap` 渲染视频帧）。底部悬浮条只保留圆形播放/暂停、进度条拖动（Seek）、音量、全屏，右上角保留圆形“⋯”更多菜单与关闭；弹幕、字幕、多段切换（“上一段 / 下一段”）、“在文件夹中显示”和“用系统播放器打开”收进“⋯”菜单。鼠标静止约 2 秒后悬浮控件自动隐藏，鼠标移动或按键时重新出现。
  - **拖动与跳转**：界面线程不调用解码器。拖动进度条时 `Controller.Preview` 先暂停声音，只把目标时间交给视频后台线程，后台用 `PreviewFrameAt` 解出目标附近的关键帧显示；松手（或单击）才 `Seek` 一次。`Seek` 只记录目标并请求新画面，声音输出线程随后只移动音频读取器（`splitDecoder.SeekAudio`），不再在两个线程各移动一次两个读取器。播放中跳转时声音先停住，等新位置第一帧画面出来（最长 1.5 秒）再继续，免得声音先跑、画面追赶。`internal/player/seek_test.go` 与真实 Media Foundation 的 `TestPreviewAndAudioSeekOnMediaFoundation` 覆盖。
  - 弹幕叠加层（`internal/player/danmaku.go`）：解析缓存的 `.danmaku.xml`，按当前播放时间 `t` 渲染滚动（mode 1～3）、底部（mode 4）、顶部（mode 5）弹幕；“⋯”菜单提供弹幕开关、透明度滑块（20%～100%）、显示区域与密度选择（低 / 中 / 高 / 不限）。密度按原时间线每秒稳定选取最多 2 / 5 / 10 条或不限，轨道仍防重叠；拖动进度后按新时间窗重排，无残留。偏好写入 `ui-state.json`，重新打开播放器和重建窗口时恢复。
  - 音量按钮打开 0～100% 滑块与静音开关，调整立即作用于播放器并保存偏好。全屏按钮经 `internal/shell` 绑定到 MyGo 窗口的 `SetFullScreen`；切分段保持全屏，关闭播放、登录失效或关闭窗口时恢复窗口状态。
  - 图片加载在后台使用取消上下文与递增请求版本，图片未加载完时也可关闭；迟到结果不能重新打开媒体层或覆盖新图片。当前请求只更新图片与错误信息，保留加载期间的缩放、平移和选中序号。
  - 字幕叠加层（`internal/player/subtitle.go`）：读取缓存的 `.vtt`，“⋯”菜单提供字幕语言选择（AI 字幕标明“AI”，含“关闭”）；没有弹幕或字幕时对应控件禁用并显示原因说明。当前播放偏好统一写入 `ui-state.json.player`，不从 `settings.json.player` 读取。
- **设置日志与诊断**：诊断与日志读取在后台执行，每秒最多一次，界面重绘使用缓存。服务层从日志尾部最多读 1 MiB、返回最多 500 行，写入与展示使用同一脱敏边界（包括凭证字段、JSON/编码字段与带查询串的 URL）。诊断显示真实索引总量、当前检查进度与读取失败原因；日志读取失败保留上次可读内容。
- **输入法组合输入（NFR-UI-001）**：所有单行输入框（解析页、顶部栏快速解析框、添加订阅、关联账号、搜索框）基于 `ui.TextInput` / `ui.SearchField`，Windows IME 组合输入期间（`composing` 状态）按回车只确认候选词，不触发提交；非组合态按回车才提交。手动凭证输入框使用密码掩码（`Secure(true)`），提交后立即清空。
- **视觉与无障碍**：严格遵循[界面规范](../ui.md#visual)的色值（`internal/ui/theme.go`：页面/侧栏/图标栏 `#ffffff`/`#f8f8f6`/`#f1f1ee`、主文字 `#262626`、次文字 `#737373`、强调色蓝 `#1a73e8`（`TRACE_UI_ACCENT=green` 预览绿 `#0a8754`，Q-011）、直播橙 `#c2410c`、状态圆点绿 `#2aae5b` / 黄 `#f3a115` / 红 `#d91f37`）、字号与圆角；界面字体 Inter 4.1 可变字体经 `go:embed` 打包，启动时 `ui.RegisterFont` 注册（`internal/ui/font.go`），中文由 DirectWrite 回退到系统字体；所有图标按钮、状态圆点、输入框与对话框均用 `.Label(…)` 设置无障碍名称（测试用 `tt.AccessByRole` 核对角色与名称），支持键盘 Tab 焦点环与快捷键。

<a id="invariants"></a>
## 7. 不变量

| 编号 | 不变量 |
| --- | --- |
| INV-01 | 凭证明文只存在于 Go 进程内存；手动凭证只从输入框经一次调用单向传入并立即清空输入框。界面状态、日志（含 MyGo 运行日志）、历史记录、清单、索引、错误消息里没有凭证 |
| INV-02 | 风控、限流、未登录、网络、不可得、格式变化、存储失败是不同的错误类别，任何一类都不能显示成空结果或“已缓存” |
| INV-03 | 已取消、已过期或被新流程替代的扫码流程不能写入凭证；无效的手动凭证不能写入 |
| INV-04 | 数据文件只通过“临时文件 + 改名”写入；损坏的文件被保留而不是覆盖；索引数据库可以从清单完整重建 |
| INV-05 | `internal/site/bilibili`、`internal/download`、`internal/library`、`internal/credential`、`internal/storage`、`internal/subscription`、`internal/live`、`internal/settings`、`internal/uistate`、`internal/appdirs`、`internal/update`、`internal/service` 不依赖 MyGo；`internal/site/bilibili` 也不依赖存储、缓存、订阅和录制代码 |
| INV-06 | 只有全部文件校验通过、改为正式名并写好清单的条目才标为已缓存（绿点）；其余状态都带原因 |
| INV-07 | 没有有效登录凭证时，不向站点内容接口、用户主页和媒体 CDN 发任何请求（只有扫码接口和候选凭证的 nav 校验例外） |
| INV-08 | 本地媒体读取（`LibraryService.MediaPath`）只返回索引登记、位于条目文件夹内的真实文件（拒绝 `..`、符号链接/目录联接与跨条目路径）；签名播放地址、直播流地址与字幕 `auth_key` 地址不落盘、不进日志 |
| INV-09 | TRACE 只删除自己登记过的文件和变空的文件夹，从不递归删除条目文件夹或保存位置；录制中的条目不可删除 |
| INV-10 | 规则触发的请求（定时检查、开播检查、自动缓存、续期）只在应用运行、已登录、对应设置开启、不在退避期、没超过每小时 200 次额度时发出 |
| INV-11 | 删除订阅、解除关联不删除任何缓存文件；TRACE 不调用站点的写操作接口（关注、取关、点赞、评论等），登录、续期接口除外 |
| INV-12 | 检查更新从不把一个账号第一次检查前就存在的动态放入更新列表（第一次检查只建立起点，起点之前的动态不进入更新，DEC-033）；更新记录丢失时回到“未建立起点”，不会一次冒出大量历史条目 |
| INV-13 | 窗口关闭或重建不影响后端工作：任务、录制、调度与续期的状态只在后端服务层；没有窗口时推送的事件直接丢弃，不排队、不阻塞；重新打开的窗口从服务重新读取状态 |
| INV-14 | 数据目录同时只被一个 TRACE 进程打开（数据目录独占锁）；TRACE 不在 `%APPDATA%`、用户主目录和安装目录里写数据 |

<a id="tray"></a>
## 8. 窗口生命周期、托盘与单实例（DEC-030）

- **单实例**：`main.go` 先 `mygo.App.SetName("trace"|"trace-dev")`（锁按应用名与标识区分，两个通道互不影响），再调用 `mygo.App.RequestSingleInstanceLock()`；不是第一个实例就退出，第一个实例在 `OnSecondInstance` 里打开（或重建）窗口并置前。另外在数据目录里用 `app.lock`（Windows `LockFileEx` 独占锁）做数据目录锁：拿不到锁时返回明确错误后退出，不打开任何数据文件（INV-14）。
- **托盘**：`WhenReady` 里用 `mygo.NewTray` 建图标（`ID: "trace"`，16×16 与 32×32 的 PNG 由 TRACE 自己绘制：强调蓝底 `#1a73e8` 白色 “T”，有进行中任务或录制时右下角加琥珀色 `#f3a115` 圆点），单击 → 打开窗口；菜单：“打开 TRACE”、一行禁用的状态（“正在缓存 N 项 · 录制 M 场”或“没有进行中的任务”）、“检查全部订阅”、分隔线、“退出”。悬停提示与状态行在任务数变化时更新（至多每 2 秒一次）。
- **关闭到托盘**：注册 `App.OnWindowAllClosed` 让进程在没有窗口时继续运行。原生窗口的 `OnClose` 里：设置 `closeAction=tray`（默认）时不阻止窗口关闭，原生窗口销毁，同时释放 `internal/ui` 与 `internal/player` 持有的图片缓存、视频位图纹理和事件订阅，调用 `runtime.GC()` 与 `debug.FreeOSMemory()`（NFR-RES-001）。第一次关到托盘时用 MyGo 的 `Notification` 发一条系统通知（记在 `ui-state.json` 的 `trayNotified`），之后不再重复提示。`closeAction=quit` 时关闭窗口等同托盘“退出”。
- **重新打开**：按 `ui-state.json` 记下的最后路由和窗口位置大小调用 `mygo.NewWindow` 新建原生窗口；主页从最新处开始（DEC-040）。界面挂载时从 `internal/service` 重新读取任务、订阅、直播与登录状态（INV-13）。
- **退出**：托盘“退出”或窗口里的退出：有排队、进行中的任务或进行中的录制时先确认（有窗口时在窗口里弹确认框；没有窗口时托盘菜单的“退出”变成子菜单“退出（会暂停 N 个任务、结束 M 场录制）”，再点一次才退出）。确认后：任务改为“已暂停（应用关闭）”，录制按[直播 §2](live.md#recorder)收尾（限时 30 秒），然后 `App.Quit()`。
- **后台节律**：没有窗口时，调度器仍是每分钟醒来一次；没有任务、录制和到期的规则时不发请求、不写盘。
- **启动参数**：`--background` 启动时不开窗口、只在托盘；`--quit-for-update` 交给运行中的 TRACE（第二个实例经单实例通道转发），它不弹确认，按托盘“退出”的同一条路径保存（任务改为“已暂停（应用关闭）”、录制收尾、保存路由与窗口状态），在 `ui-state.json` 记下 `update.restart`（原因、窗口是否打开、时间）后退出；没有运行中的 TRACE 时这个参数什么都不做。下次启动若读到 5 分钟内的这条记录，就按记录决定开窗口还是留在托盘（优先于 `--background`），然后删掉记录。

<a id="updates"></a>
## 8.1 更新（FR-UPD-001～004，DEC-042）

- **分层**：检查与下载的状态机和更新源的校验在 `internal/update`（纯 Go，可测试，不依赖 MyGo）；`internal/shell/updates.go` 把它接到 `mygo.Updater`（`SetSource` 换成 TRACE 的更新源，`Check`/`Install`/`App.Relaunch`）；`internal/ui` 只经 `UpdaterAPI` 小接口读状态、发命令。
- **只在正式版启用**：只有 `tools/release` 的正式版构建带 `updates`（MyGo 注入清单地址与公钥）和 TRACE 自己的同一份地址与公钥（`internal/update.feedURL`/`publicKey`）；开发版与源码运行没有，`Enabled()` 为假，关于页写“开发版由本机打包自动更新”。ptest 身份（只在演练里构建）另外可以用环境变量 `TRACE_PTEST_UPDATE_URL` 指到本机回环地址上的假发布源，并在启动 3 秒后检查；正式版与开发版不读这个变量。
- **检查**：启动 1 分钟后一次，之后每 24 小时一次（“自动检查更新”关掉就不检查）；“设置 › 关于”的“检查更新”手动检查。请求经系统代理只访问 GitHub（清单 `releases/latest/download/update-windows-amd64.json` 与它列出的文件），只带版本与平台。自动检查失败时静默，只写日志；手动检查显示原因。
- **校验**（MyGo 之外 TRACE 补上的）：所有请求与重定向都必须是 HTTPS（只有 ptest 的假发布源可以是回环地址上的 HTTP）；清单最大 1 MiB、字段齐全、版本格式正确、大小在 1 GiB 内、地址都是 HTTPS；清单的 `traceAttestation` 是更新密钥对“`trace-update-v1`、版本、更新包签名”的签名——MyGo 的签名只覆盖更新包内容、不含版本号，这一条防止把旧版本的更新包标成新版本号送来；只下载清单里列出的文件，边读边核对大小与签名，不符就在读到结尾前失败。MyGo 自己核对 Ed25519 签名、差分包的起止版本，并只接受比当前新的版本。任何一条不符都拒绝，保留现版本；手动操作时显示“更新包校验失败，已保留当前版本”，日志写原因。
- **下载与替换**：“自动下载更新”开着（默认）时，发现新版本就在后台 `Install`：下载、校验、解包，把程序文件换成新版本（正在运行的程序被改名放在一边，下次启动时删除）；新版本在下次启动时运行。关着时只提示“有新版本”，用户点“下载并安装”才下载。就绪的版本与说明记在 `ui-state.json`（`update.readyVersion`、`readyNotes`）。
- **重启以更新**：没有缓存与录制时直接按托盘“退出”的路径收尾并 `App.Relaunch`；有时先确认“重启会暂停 N 个缓存任务并结束 M 场录制，重启后可以继续缓存。”。
- **更新后第一次启动**：版本比 `update.lastRunVersion` 新时显示一次“已更新到 0.1.x”，可以展开更新内容；同时把本机卸载项（`HKCU\...\Uninstall\<标识>`，只在它的 `InstallLocation` 就是这个程序所在目录时）的 `DisplayVersion` 改成新版本，“设置 › 应用”显示的版本随之正确。
- **数据结构版本**：启动时先核对数据文件的结构版本（[数据 §1](data-and-cache.md#schema-versions)）；遇到比这个版本新的结构就显示“这些数据来自更新的 TRACE 版本，请安装新版”并退出，不写任何文件。

<a id="headless-ext"></a>
## 9. 未来远程服务器（VPS）轻量模式与 Web 配置边界（DEC-032）

全项目改为纯 Go 后，架构通过以下约束保证未来可零成本提取出一个部署在 Linux/Windows VPS 上的无头直播录制与自动缓存程序（如 `cmd/trace-server` 或 `trace -headless`），并通过浏览器 Web 界面进行基础配置（添加/删除直播间与订阅、开关自动录制、扫码或粘贴 Cookie 登录、查看录制状态）：

1. **后端零 GUI 依赖（INV-05）**：`internal/service` 及所有下层包（`site/bilibili`、`subscription`、`live`、`download`、`library`、`credential`、`storage`、`settings`）全部是 `CGO_ENABLED=0` 的标准 Go 代码，不引用 `mygo` 或 Windows GUI 库（平台相关文件如 `dpapi_other.go`、`lock_other.go`、`disk_other.go` 提供 Linux/跨平台实现）。
2. **`internal/service` 接口化**：桌面端的 `internal/ui` 只通过 `service.Backend` 接口调用业务方法。未来新增远程模式时，只需提供两种轻量 Web 入口之一（或同时提供），无需重写任何业务逻辑：
   - **路线 A（标准库 `net/http` JSON API + 内嵌单页 Web 控制台）**：用一个很薄的 `internal/webadmin` 包把 `service.Backend` 的方法（如 `AuthService`、`SubscriptionService`、`LiveService`、`SettingsService`）映射为带 Token 鉴权的 HTTP JSON 端点，并用 `embed.FS` 内嵌一个几十 KB 的静态配置页面，编译出约 10 MB 的单文件 Linux 静态二进制即可在 VPS 上通过浏览器添加直播间与管理录制。
   - **路线 B（`mygo/ui` 直接编译为浏览器 WebAssembly）**：MyGo 原生支持 `mygo build -platform web`（`GOOS=js GOARCH=wasm`），可将 `internal/ui` 编译为运行在浏览器 Canvas/WebGL2 中的前端，通过实现一个走 HTTP 的 `service.Backend` 客户端直接复用同一套 Go 界面。
