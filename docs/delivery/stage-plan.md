# 当前阶段计划：M5（远程版、上传与导入导出）

- **阶段编号**：M5；计划标识 `TRACE-M5-01`，修订 1（2026-11-19）。
- **角色**：A 规划；B 连续实施与自检；A 集中审查（[开发流程 §2](../dev-rules/workflow.md#agent-handoff)）。给 B 的 prompt 在[第 10 节](#prompt)。
- **分支**：`codex/m5-remote`，A 已从 `main @ 8327803` 创建。本计划所在的提交是阶段提交的起点；B 的实施、A 的审查修复和人工反馈的修复全部 amend 进这一个 DCO 提交。
- **上一阶段**：M4（我的）经 PR #8 合并为 `main @ a613735`，合并后的三轮反馈（DEC-059～061）经 PR #10 合并为 `main @ 8327803`：[M4 计划](https://github.com/chialecode/trace/blob/8327803/docs/delivery/stage-plan.md)、[M4 报告](https://github.com/chialecode/trace/blob/8327803/docs/delivery/stage-report.md)。
- **决定与需求**：[DEC-062](../decisions.md#dec-062)；需求 FR-REMOTE-001～008（[需求 §2.10](../requirements.md#remote-scope)）；验收 AC-56～AC-64（[路线与验收](roadmap.md#acceptance)）；**设计正本 [远程设计](../design/remote.md)**（本计划只补实现上的指引，不重复设计）；默认值 [Q-022](../../USER-ACTIONS.md#q-022)；真实 VPS 检查 [H-003](../../USER-ACTIONS.md#h-003)。

用户 2026-11-19 要求：提供部署到远程 VPS 的版本，只获取启动后更新的内容和录制，没有其他功能；VPS 上的内容定时上传到网盘，再从网盘下载到本地导入（假设两边都是最新版）；订阅列表与资源库都能导入导出，订阅从本地经 VPS 的 HTTP 接口同步，资源库从网盘下载后增量导入，冲突由用户选保留或合并；为以后的集群模式做准备；可以单独打包远程版减小体积；远程通常运行在 Debian 12、Ubuntu 22 一类的 Linux 上；本地软件可以连接远程查看状态、手动同步订阅与 Cookie；上传时还在增长的视频不会被上传，元数据要一同上传，不需要停下远程就能导入。用户选了四个推荐项（内置 HTTPS + 令牌、只由本地续期、随订阅同步缓存规则、手动选文件夹导入），并补充“可以在远端软件里内置上传功能，如果你觉得这样更可靠，主要上传到 Google Drive”。A 的结论：内置上传，但传输交给 rclone（DEC-062 ⑨ 的理由）。

## 1. 范围与退出条件

| 项目 | 约定 |
| --- | --- |
| 阶段内容 | 阶段开始的整理与依赖盘点（C0、D1）；可写文件与临时名的统一（W1）；远程运行配置与 Linux 凭证（W2）；`trace-server` 程序、接口与安全（W3）；内置上传（W4）；本地远程客户端与“设置 › 远程”（W5）；资源库导入导出（W6）；订阅导出导入与同步快照（W7）；打包、门禁与 CI（W8）；文档（W9） |
| 不在本阶段 | 集群调度（多台分担账号）与同时连多个远程（FR-REMOTE-009）；远程网页；本地自动从网盘拉取；远程的解析、手动缓存、全部缓存、补更早内容；Windows 版远程程序与 Docker 镜像；远程自我更新（换包重启）；导入时转码或识别相似内容；新的站点能力 |
| 可连续执行 | 本地实现、合成夹具、假 rclone、在回环地址上起真实 HTTPS 服务的端到端测试、截图、打包与本机开发版自动安装、冒烟、文档同步、阶段提交与 amend；本机有 WSL 时在其中运行 Linux 版 `trace-server` 冒烟 |
| 外部操作 | 本阶段不需要 Agent 发真实站点请求，也不连接任何网盘；远程对真实站点与网盘的行为由用户在 [H-003](../../USER-ACTIONS.md#h-003) 检查。未授权：push、PR、合并、标签、发布 |
| 必须保留 | M1～M4 与 DEC-059～061 的全部行为与测试；数据结构版本 2 与守卫（本阶段只加可选字段与 `CREATE TABLE IF NOT EXISTS` 新表，不升结构版本）；按站点的间隔、额度、退避与熔断；凭证隔离与哨兵；INV-01～14；安静测试（不碰本机安装的应用）；只删登记文件；`dist/<通道>/` 只留最新一份（DEC-025） |

**退出条件**

| 条件 | 做法 | 工作包 | 验收 |
| --- | --- | --- | --- |
| 远程程序可打包、在 Linux 上运行 | `cmd/trace-server` 的 `init/run/token/upload-test/version/rclone-filter`；不链接 MyGo；`linux/amd64`、`arm64` 包 | W2、W3、W8 | AC-56、AC-57 |
| 远程接口安全 | HTTPS 自签名 + 指纹、令牌哈希、失败限流、版本握手、日志无敏感内容 | W3、W5 | AC-57、AC-58 |
| 订阅同步与远程规则 | 快照整体替换；起点；按规则缓存与录制；不做全部缓存 | W2、W3、W7 | AC-59 |
| Cookie 推送 | 校验后保存、不续期、本地续期后自动推送与待同步重试、失效停站点 | W2、W3、W5 | AC-60 |
| 上传 | 按条目先媒体后 `item.json`、确认后删除、重试与说明；兼容外部 `rclone move` | W1、W4 | AC-61 |
| 远程状态 | “设置 › 远程”的全部状态与错误分类 | W3、W5 | AC-58 |
| 资源库导入导出 | 预览六类、冲突三种处理、校验、可中断续做、导出 | W6 | AC-62、AC-63 |
| 订阅导出导入 | `trace-subscriptions.json` 与预览 | W7 | AC-63 |
| 集群预留 | 节点 ID、`cachedBy`、按条目合并 | W1、W6 | AC-64 |
| 整理与依赖 | 第 6 节 | C0、D1 | 文档检查；报告的整理记录与盘点结果 |

**阶段完成的判定**：B 自检的必测用例（第 7 节）与门禁全部通过，A 集中审查收敛后，才可以请用户授权 push、PR 与合并。H-003 不是合并的前提；没做时，报告与交付状态必须写明远程版只经本机、CI 与（若有）WSL 验证，真实 VPS、真实站点与 Google Drive 部分为 `not-run`。

**阶段完成后，用户可以：** 在 VPS 上解压 `trace-server`、`init` 拿到连接码、装 systemd 服务；在本地“设置 › 远程”粘贴连接码，看到远程状态，同步订阅与 Cookie；远程按订阅规则缓存新内容与录制，并用 rclone 上传到 Google Drive、删掉 VPS 上已上传的文件；把网盘上的文件夹下载下来，在媒体库“导入”，按预览处理冲突后导入；导出订阅与资源库给另一台 TRACE。

## 2. A 已核对的现场（2026-11-19，`main @ 8327803`）

- **Git**：`main` 与 `origin/main` 一致；远端还有 Dependabot 分支（PR #9，`golang.org/x/image` 0.47.0 → 0.48.0）。
- **无 GUI 后端**：`go list -deps ./internal/service` 不含 MyGo；`GOOS=linux GOARCH=amd64 go build ./internal/service/ ./internal/library/ ./internal/subscription/ ./internal/live/ ./internal/download/` 成功。合并（`internal/download/mux*.go`，mp4ff）、SQLite（`modernc.org/sqlite`）都是纯 Go。`tools/check` 已在 `main.go:228` 一带检查 MyGo 只出现在允许的包里。
- **入口**：只有根目录 `main.go`（桌面，`shell.Run()`）；没有 `cmd/`。`service.NewBackend(Options)`（`internal/service/backend.go`）一次建好全部组件并启动后台循环：续期 `runRenewalLoop`（`backend.go:772`）、全部缓存 `runFullCacheLoop`（`account_works.go`）等；`Options` 已有 `Locations`、`Desktop`、`DisableBgWorkers` 等注入点。`NewDefaultDesktop` 在非 Windows 上会调用系统程序，远程要注入空实现。
- **Linux 平台文件**：`appdirs/lock_other.go` 是真正的 `flock` 独占锁；`appdirs/knownfolder_other.go` 用 `~/Documents`，远程不能用它（数据与导出目录由命令行给出）；`credential/dpapi_other.go` 用常量派生的固定密钥，只适合测试，远程必须换成随机密钥文件（W2）。
- **条目清单**：`library.ItemMetadata`（`internal/library/schema.go:17`）的 `media[]` 已有 `path`、`bytes`、`sha256`；直播每段收尾时算 SHA-256（[直播 §2 第 6 步](../design/live.md)）。`item.json` 可直接当导入清单。
- **临时文件名**：下载与录制用 `<正式名>.part`；合并用 `.trace-mux-*.part`、`.trace-progressive-*.part`；但 `storage.WriteFileAtomic`（`internal/storage/atomic.go:22`）与 `storage/schema.go:314` 用 `<正式名>.tmp-<pid>-<随机>`，凭证用 `.tmp-cred-*`，抖音还有 `.raw`、`.imgtmp`、`.audtmp` 等后缀（`internal/download/douyin.go:1486`），这些不满足“`*.part` 或 `.trace-*`”的统一排除规则（W1）。
- **续期与登录**：续期在服务层 `tryScheduledRenewal`（`backend.go:818`），登录在 `AuthService`；自动推送 Cookie 挂在这两处成功之后（W5）。
- **CI**：`.github/workflows/ci.yml` 的 `repository-quality` 在 Ubuntu 上运行 `go run ./tools/check`，所以新增的跨平台测试会在真实 Linux 上跑。
- **本机参考**：索引里“多平台无界面录制（以后 Linux/headless）”一项（GPL，只参考思路，本地副本 2026-11-10）：它的上传也放在“录制处理完成之后”，并把上传后删除作为开关，与本设计一致；没有可复用的代码，仓库不写它的名字。

## 3. 依据、查证与选型

- **不引入新的 Go 依赖**：HTTPS、自签名证书、令牌与指纹用标准库 `crypto/tls`、`crypto/x509`、`crypto/ecdsa`、`crypto/sha256`；HTTP 用 `net/http`；调用 rclone 用 `os/exec`。
- **上传工具 rclone**（2026-11-19 查询）：GitHub `rclone/rclone`，MIT，未归档，2026-11-18 仍有推送，最新发行 v1.76.0（2026-11-06）。官方文档：`rclone move` 只在复制没有错误时删除源文件，空目录要 `--delete-empty-src-dirs`（rclone.org/commands/rclone_move）；本地后端在上传中发现文件变化会以 “can't copy - source file is being updated” 中止（rclone.org/local）；`--min-age` 只作用于文件（rclone.org/filtering）；Google Drive 每天 750 GiB 上传上限，共用的 client_id 2026 年内停用、需要用户自己的 client_id，每个 client_id 默认每秒约 10 次请求（rclone.org/drive）。结论：rclone 作为用户机器上的外部程序，TRACE 不打包、不读它的配置；Google Drive 授权完全交给 `rclone config`。
- **不直接对接 Google Drive 接口**：要自己处理 OAuth（无界面机器上的授权）、令牌保存与续期、断点续传、限速与每日上限；收益只是少装一个 rclone，风险大，不做（DEC-062）。
- **远程网页不做**：用户只要求本地软件连接远程；[架构 §9](../design/architecture.md#headless-ext)保留以后的选项。

## 4. 实现指引

设计以[远程设计](../design/remote.md)为准，下面是 A 对实现的约束与建议，B 可以选更好的做法，但要在报告里写明。

### 4.1 包与依赖方向

| 包 | 内容 | 可以依赖 |
| --- | --- | --- |
| `internal/remote` | 协议类型（状态、快照、凭证推送、错误）、连接码编码与解码、固定指纹的 HTTPS 客户端 | 标准库、`internal/site`（只用站点 ID） |
| `internal/remoteserver` | 证书与令牌生成、鉴权与失败限流、路由、把请求转到服务层 | `internal/remote`、`internal/service` |
| `internal/upload` | 上传队列、调用 rclone、退避、上传记录 | `internal/library`、`internal/storage` |
| `internal/transfer` | 资源库导入的扫描、判定、执行与导出 | `internal/library`、`internal/storage` |
| `cmd/trace-server` | 命令行、`server.json`、信号处理、组装 | 上面这些与 `internal/service` |

订阅快照的生成与整体替换放在 `internal/subscription`（它拥有这些数据）。全部新包不依赖 MyGo；`tools/check` 增加 `go list -deps ./cmd/trace-server` 不含 MyGo、`internal/player`、`internal/ui`、`internal/shell` 的检查。

### 4.2 远程运行配置（W2）

- `service.Options` 增加运行配置（如 `Profile: ProfileRemote`）。远程下：不启动续期循环、全部缓存循环、开发版安装与更新检查、最近解析与头像下载；“定时检查更新”总开关视为开；规则的 `fullCache` 忽略；入队“全部缓存”和手动缓存的服务方法返回明确错误（远程接口本来也不暴露）；`Desktop` 注入空实现。
- 保存位置固定为导出目录，用户设置里的 `cacheDir` 不生效；远程不做 `RebuildIndex` 以外的文件扫描（升级探测、丢失文件刷新、空间页统计不在远程运行）。
- **文件可能被搬走**：远程判断“已缓存、已排过任务、录到第几段”只看数据库与录制状态（`updates.db` 的 `auto_task_id`、`library.db` 的索引与 `json_data`），不看文件是否还在。录制写新段、重写 `item.json` 之前 `MkdirAll` 条目文件夹。必须有测试：缓存与录制进行中把已完成的文件与 `item.json` 删掉，远程继续工作、不重复下载、下一次 `item.json` 仍列出全部已完成的段。
- **Linux 凭证**：`dpapi_other.go` 改为用 `credentials/` 下随机生成的 32 字节密钥文件（`0600`，`init` 时生成，缺失时生成新的并让旧凭证读不出而按“没有 Cookie”处理），去掉固定派生密钥；Windows 不变。远程的凭证 `source` 记为 `remote_push`，不续期（`Renewal()` 返回不可用，原因“由本地续期”）。

### 4.3 远程程序与接口（W3）

- 连接码：`trace-remote:1:` 加 base64url 的 JSON（`url`、`token`、`fingerprint`、`name`）；`init` 的 `--listen` 与可选的 `--public-address`（写进连接码的地址，默认用监听端口和本机首个非回环地址，打印时提醒按需改）。
- 证书：ECDSA P-256 自签名，有效期 10 年；`token` 命令不换证书。本地客户端 `InsecureSkipVerify` 只能与“比较叶证书 SHA-256”的 `VerifyConnection` 一起出现，并有测试证明指纹不符时请求一个字节都不发出。
- 失败限流按来源 IP 计数（不信任 `X-Forwarded-For`）。请求体上限 1 MiB，读写超时 30 秒。
- 端点与状态内容见[设计 §3、§7](../design/remote.md#api)。状态由服务层已有的读取方法组装（检查计划、任务、录制、站点退避与登录），不为状态另发站点请求。
- `run` 收到 `SIGTERM`/`SIGINT` 时走服务层的退出收尾（任务暂停、录制收尾，限时 30 秒）后退出码 0。

### 4.4 上传（W4）

- 数据：`library.db` 新表 `upload_files(item_key, rel_path, bytes, sha256, uploaded_at, PRIMARY KEY(item_key, rel_path))`（`CREATE TABLE IF NOT EXISTS`，结构版本不变）。
- 触发：条目每次成功 `SaveItem`（包括直播每段收尾）后，把清单里还没上传的正式文件放进上传队列；启动时扫一次索引补上漏掉的。同一时间只有一个 rclone 进程。
- 一批：`rclone copy <条目文件夹> <remote>/<相对路径> --files-from <临时清单> [--config <文件>] <extraArgs>`，退出码 0 后再单独传 `item.json`；临时清单放在数据目录，不放导出目录。
- 删除：开着 `deleteAfterUpload` 时，媒体文件在确认上传后经 library 的登记文件删除路径删除；`item.json`、封面与其他小文件等条目最终完成（不在录制、不在缓存、状态为完成或失败）且最后一版 `item.json` 已上传后再删，然后删空文件夹。索引里的条目保留（远程靠它判断已缓存）。
- 错误分类：找不到 rclone、配置或远端错误、配额或限流（stderr 里的 `quota`/`rateLimit`/`upload limit` 等关键字）、网络、本地文件没了（不再重试这一个文件，状态说明）。stderr 只保留脱敏后的首行（去掉 URL 查询串与形如令牌的长串）。
- 测试用假 rclone（测试二进制自身充当，按环境变量决定成功、失败、慢），不访问网络。

### 4.5 临时文件名统一（W1）

- 条目文件夹与导出目录里的全部临时文件改为 `*.part` 或 `.trace-*`：`WriteFileAtomic` 改为同目录下 `.trace-<文件名>.<pid>-<随机>.tmp`；`storage/schema.go`、凭证的临时名同样处理（凭证不在导出目录，统一是为了规则简单）；抖音的 `.raw`、`.imgtmp`、`.audtmp` 改为以 `.part` 结尾。旧名字的续传与清理识别保留一个版本，避免升级时丢掉进行中的下载。
- 增加一个测试：在缓存、录制、合并、写清单的各个阶段截取条目文件夹的文件列表，凡不是正式文件的名字都匹配 `*.part` 或 `.trace-*`。
- `item.json` 增加可选字段 `cachedBy`（`node`、`name`），节点 ID 存 `<数据目录>/node.json`（首次需要时生成）；桌面与远程都写。

### 4.6 本地客户端与界面（W5）

- 服务层新增 `RemoteService`：读写连接（连接码或三项；令牌进凭证库，地址、名字、指纹进 `settings.json` 的 `remote`）、`RemoteStatus`、`SyncSubscriptionsToRemote`、`PushCredentialToRemote(site)`、`RemoveRemote`。错误分类：无法连接、证书变了、令牌错或被暂时拒绝、版本不同、远程拒绝 Cookie（附远程给的原因）。
- 自动推送：续期成功、扫码或粘贴登录成功后，若配置了远程就异步推送；失败记“Cookie 待同步”（`settings.json` 里只记站点与时间），后台每 10 分钟与每次读远程状态时重试。退出登录不自动清除远程的 Cookie（“设置 › 远程”有“清除远程登录”）。
- 界面：设置新增分类“远程”（`internal/ui/views.go:1524` 一带的分类表），内容按[设计 §7](../design/remote.md#status)。页面可见时每 30 秒刷新，离开即停；请求在后台执行，不阻塞界面。按[界面规范](../ui.md)的现有控件与文案风格；`docs/ui.md` 增加对应小节。

### 4.7 资源库导入导出（W6）

- `transfer.Scan(root)` 只读：找 `item.json`（深度与布局按 `<站点>/<作者>/<种类>/<条目>`，也接受选中 `library` 的上一层），解析、校验路径在条目内（INV-08 同样的规则），按[设计 §6.3](../design/remote.md#import)分类；对本地已有的条目只读索引与文件大小，SHA-256 只在需要比较时计算，并有进度与取消。
- `transfer.Apply(plan, 选择, 选项)` 后台执行：复制到 `.part` → 校验 → 改名 → 合并并写 `item.json` → 更新索引与信息流。“用导入的”替换本地文件前，导入的副本必须已校验落盘；删除本地多余文件走登记文件删除（INV-09）。记录每条的结果，失败的条目不影响其他条目。
- `item.json` 合并放在 `internal/library`（例如 `MergeItemMetadata(local, imported, mode)`），覆盖：分 P 并集、直播段并集按段号排序、`live.incidents` 去重并集、`media` 以实际存在且校验过的文件为准、状态重新计算（全部文件齐全才是完成，INV-06）。
- 界面：媒体库“⋯”菜单加“导入…”“导出…”；导入是一个页面（选择文件夹 → 扫描进度 → 预览分组与冲突逐条或批量选择 → 开始）；执行进度在任务页侧栏新增“导入”一项（与下载任务、缓存队列并列），结束后有结果摘要。导出：选范围（全部或当前筛选）与文件夹，作为同样的后台任务。
- 导入不要求远程程序停下：未传完的条目只导入已校验的文件，下一次导入时这条变成“可补充”。

### 4.8 订阅快照、导出导入（W7）

- `subscription.Manager` 提供 `Snapshot()`（订阅、规则、设置的一部分，带摘要哈希）与 `ReplaceFromSnapshot()`（一次写入，保留按“站点 + UID”的账号状态）；设置的那部分由服务层合并进快照。
- 文件 `trace-subscriptions.json` 与远程同步用同一结构，`format`、`version` 不对就拒绝。本地导入走预览：新订阅、账号已在某个订阅里（默认跳过，可选合并进那个订阅）、规则同 ID 不同内容（保留本地 / 用导入的）；确认后一次写入。导入的订阅不改变已有账号的检查起点。
- 订阅页（[界面 §3.9](../ui.md#subscriptions)）的“⋯”加“导出订阅…”“导入订阅…”。

### 4.9 打包与 CI（W8）

- `go run ./tools/package -server`：交叉编译 `linux/amd64` 与 `linux/arm64`（`CGO_ENABLED=0`、`-trimpath`、与桌面相同的版本注入），输出到 `dist/trace-server/`（只留最新一份），每个架构一个 `trace-server-<版本>-linux-<架构>.tar.gz`，内含 `trace-server`、`trace-server.service`、`README.md`（中文部署说明：目录、用户、防火墙、`init`、systemd、rclone 配置与 client_id、外部定时 rclone 的排除规则、凭证保护的边界、换包升级），另有 `SHA256SUMS`。报告写两个程序与桌面程序的体积。
- `tools/check`：上面的依赖检查；`GOOS=linux GOARCH=amd64` 与 `arm64` 构建 `./cmd/trace-server`。
- `tools/smoke` 增加远程冒烟（可以是 `-server` 选项）：在临时目录 `init` → 启动 → 用连接码读状态 → 同步一个合成快照 → 假 rclone 上传一个合成条目 → 收到信号后退出；Windows 本机与 Ubuntu CI 都能跑（Linux 下跑交叉编译的真程序，Windows 下跑本机构建）。本机有 WSL 时额外用 Linux 包跑一次，没有就记 `not-run`。

## 5. 工作包

| 工作包 | 预期结果与关键失败路径 | 完成证据 |
| --- | --- | --- |
| C0 整理 | 第 6 节 | 报告整理记录；`go run ./tools/checkdocs` |
| D1 依赖 | 处理 PR #9；`tools/deps` 盘点与 `-audit` | 报告的盘点结果 |
| W1 临时名、节点与清单 | 所有非正式文件匹配 `*.part`/`.trace-*`；旧名可续传；`cachedBy` 与 `node.json` | K01、K02 |
| W2 远程运行配置与凭证 | 远程不续期、不做全部缓存；文件被搬走后照常工作；Linux 凭证用随机密钥文件 | K03～K06 |
| W3 `trace-server` 与接口 | 命令、证书、令牌、限流、版本握手、状态、快照替换、凭证推送与校验、信号收尾 | K07～K11 |
| W4 上传 | 先媒体后清单、确认后删除、最终才删 `item.json`、重试与分类、重启补传 | K12～K14 |
| W5 本地客户端与“设置 › 远程” | 连接、状态、同步、自动推送与待同步重试、错误分类、截图 | K15～K17 |
| W6 资源库导入导出 | 扫描与六类判定、三种冲突处理、校验、中断续做、导出可再导入、两节点合并 | K18～K21 |
| W7 订阅快照与导出导入 | 快照与替换、文件导出导入与预览 | K22 |
| W8 打包、门禁、冒烟、CI | `-server` 包、依赖与交叉编译检查、远程冒烟 | K23、K24 |
| W9 文档 | 设计、界面、数据、架构目录、工程规则命令、交付状态、需求状态、USER-ACTIONS（H-003 步骤按实际命令更新） | `checkdocs` |

连续完成全部范围、自测和修复，不逐包请求继续；需要用户的事写进 USER-ACTIONS，非阻塞的按 [Q-022](../../USER-ACTIONS.md#q-022) 的默认值实现。

<a id="cleanup"></a>
## 6. 阶段开始的整理（C0）与依赖盘点（D1）

| 对象 | 处理 |
| --- | --- |
| `docs/delivery/stage-report.md`（M4 报告） | 已在合并提交里（上面的固定链接）；仍有效的结论确认已在正本（状态、决定、USER-ACTIONS），然后改写为 M5 报告。仓库里指向旧报告与旧计划锚点的链接，A 已改成固定提交链接；B 复查没有遗漏 |
| USER-ACTIONS 里已完成的 ACT（ACT-001～007、010～021 等） | 按[开发流程 §3](../dev-rules/workflow.md#agent-user)的“结项”，已处理完的移到“最近处理完的”，保留锚点；内容精炼到一两行 |
| 文档里以前阶段的标签 | `docs/` 中用阶段号或返工轮次描述能力的文字改为能力描述（当前计划与报告除外），例如交付状态、需求、界面里的“第五轮反馈”“M4 起”之类 |
| `artifacts/`、`dist/`、`tmp/`、根目录 `package.exe` | 确认是忽略的生成物，各只留最新一份；`package.exe` 若是 `go build ./tools/package` 的残留就删除，并确认被 `.gitignore` 覆盖 |
| 测试名 | 新测试按能力命名，不带阶段号或问题编号 |

**D1**：PR #9（`golang.org/x/image` 0.47.0 → 0.48.0）并入本阶段提交后关闭；运行 `go run ./tools/deps`，满 7 天的版本全部升级，跑 `go run ./tools/check`；`go run ./tools/deps -audit`。失败或跑不起来按[开发流程 §5.5](../dev-rules/workflow.md#dependency-inventory)记录。

## 7. 必测用例

全部用合成样本与假站点客户端，测试安静、不碰本机安装的应用、不访问网络（假 rclone 也不访问）。

| 编号 | 用例 | 对应 |
| --- | --- | --- |
| M5-K01 | 缓存视频（多 P、合并）、抖音图文与实况、直播录制换段收尾、写清单、凭证保存的过程中，条目文件夹与导出目录里除正式文件外的名字都匹配 `*.part` 或 `.trace-*`；旧名字的 `.part`/`.raw` 进行中下载升级后能续传 | W1 |
| M5-K02 | 新缓存的条目 `item.json` 带 `cachedBy`，节点 ID 在重启后不变；没有该字段的旧条目照常读取 | W1、AC-64 |
| M5-K03 | 远程配置下：没有续期请求（假客户端计数为 0）、开了全部缓存的规则不建队列、入队全部缓存返回明确错误、定时检查总开关为开 | W2、AC-59 |
| M5-K04 | 远程正在缓存一个多 P 视频和录制一场直播时，删除已完成的文件与 `item.json`（模拟 `rclone move`）：缓存继续完成、不重复下载已完成的条目、下一段收尾后的 `item.json` 列出全部已完成的段、没有错误日志 | W2、AC-61 |
| M5-K05 | 远程第一次检查同步来的新账号只建立起点；之后的新内容按订阅规则自动缓存、开播按规则录制；起点之前的内容从不缓存 | W2、AC-59 |
| M5-K06 | Linux 凭证：密钥文件 `0600`、凭证文件不含明文哨兵；密钥丢失时凭证按“没有 Cookie”处理且不崩溃；Windows 路径不变 | W2 |
| M5-K07 | `init` 生成目录权限、证书、令牌哈希与连接码；连接码只打印一次，`server.json` 不含令牌明文；`token` 换令牌后旧令牌 401 | W3、AC-57 |
| M5-K08 | 没有令牌、错误令牌 401；同一 IP 1 分钟失败 5 次后 429，10 分钟后恢复；超过 1 MiB 的请求体 413；协议版本不同 409 并带两边版本 | W3、AC-57 |
| M5-K09 | 客户端用错误指纹连接：握手失败、服务端没有收到任何 HTTP 请求；正确指纹成功 | W3、W5、AC-57 |
| M5-K10 | `PUT credentials`：假站点校验通过才保存、失败时原凭证不变并返回原因；远程站点被判登录失效后状态为“需要重新同步”，该站点不再发请求；推送不含 `refresh_token` | W3、AC-60 |
| M5-K11 | 端到端：回环地址上起真实 HTTPS 服务与真实后端（临时目录、假站点），本地客户端连接、读状态、同步快照、推送 Cookie；收到退出信号时录制收尾、任务暂停、退出码 0；整个过程的日志、状态与数据文件里没有令牌、连接码、Cookie 哨兵 | W3、AC-57、AC-58 |
| M5-K12 | 上传顺序：同一条目先媒体文件后 `item.json`（假 rclone 记录调用顺序）；成功后删除媒体文件，`item.json` 与封面在条目最终完成并上传最后一版后才删；空文件夹删除；`.part` 文件从不出现在上传清单里 | W4、AC-61 |
| M5-K13 | 上传失败：找不到 rclone、退出码非 0、stderr 带配额字样分别得到正确分类与退避；文件在上传前被外部删掉时不无限重试；`deleteAfterUpload` 关时不删任何文件 | W4、AC-61 |
| M5-K14 | 重启后补传：上传进行中退出，重启后从 `upload_files` 与索引补上没传完的，已传的不重复 | W4 |
| M5-K15 | “设置 › 远程”：粘贴连接码连接；无法连接、证书变了、令牌错、版本不同、远程拒绝 Cookie 分别显示对应文案；页面不可见时不发远程请求 | W5、AC-58 |
| M5-K16 | 本地续期成功、扫码登录、粘贴登录后自动推送 Cookie；远程离线时标“Cookie 待同步”，恢复后后台重试成功并清除标记 | W5、AC-60 |
| M5-K17 | 订阅同步后本地显示“已同步”；本地改订阅后显示“有改动未同步”（按快照哈希） | W5、W7、AC-59 |
| M5-K18 | 导入扫描：合成文件夹含新条目、可补充、相同、冲突（同路径不同内容；不同作者文件夹）、未传完（缺文件、大小不符、SHA-256 不符）、无法识别（无清单、版本不对、`..` 路径）各至少一条，分类全部正确；扫描不写任何文件 | W6、AC-62 |
| M5-K19 | 导入执行：新条目导入后在媒体库与主页可见并可读取媒体路径；可补充只补缺的文件；冲突的保留本地 / 用导入的 / 合并三种结果的文件与 `item.json` 正确；未传完的直播先导入已校验的段、补齐文件后再导入变成可补充并完成 | W6、AC-62 |
| M5-K20 | 导入在复制大文件中途取消或进程被杀：没有正式名的半个文件、索引不含未完成的条目；再导入能完成；默认来源不动，勾选删除时只删已导入的文件 | W6、AC-62 |
| M5-K21 | 本地导出的文件夹在另一个数据目录导入得到相同条目；两个节点各录一部分段的同一场直播先后导入后合并为一场、段号连续、`cachedBy` 保留 | W6、AC-63、AC-64 |
| M5-K22 | 订阅快照：替换保留账号起点、移出的账号不再检查、文件不删；导出文件不含 Cookie；导入预览区分新订阅、已订阅的账号、不同的规则，确认后一次写入，失败时不变 | W7、AC-59、AC-63 |
| M5-K23 | `tools/package -server` 产出两个架构的包与 `SHA256SUMS`；`go list -deps ./cmd/trace-server` 不含 MyGo 等界面包；`trace-server version` 与桌面版版本一致 | W8、AC-56 |
| M5-K24 | 回归：`go run ./tools/check` 全部通过；`tools/uishots` 新增“设置 › 远程”（未连接、已连接、无法连接）、导入预览（含冲突）、任务页“导入”、订阅导入预览的场景，三种宽度无横向滚动；`go run ./tools/smoke`（桌面）与远程冒烟通过；`GOOS=linux go vet ./...` 通过 | 全部 |

## 8. MVP 与 LOOP

- 本阶段要贯通的 MVP 流程见[路线 §3](roadmap.md#mvp-loop)的“远程版”一行。
- 可以记为 LOOP 的深化：状态页的图表与历史、上传带宽的时段控制、导入时按作者批量决定冲突以外的更多批量规则、Docker 镜像、远程换包后的本地提示升级。不得记为 LOOP 的：任何会让凭证、令牌出现在日志或文件里的缺口，导入留下半个文件或标错完成状态，上传在确认前删除文件。

## 9. 交付与验证

- B 交付前：`go run ./tools/check`、`go run ./tools/deps` 与 `-audit`、`go run ./tools/uishots`、桌面冒烟与远程冒烟、`go run ./tools/package -server`、`go run ./tools/package -install`（把最新开发版装到本机并保持 TRACE Dev 在托盘运行）。
- 报告（`docs/delivery/stage-report.md`，按[交接模板 §2](../templates/agent-handoff.md)）写：每个工作包与用例的实现位置和证据、两个远程包的 SHA-256 与体积、本机开发版版本、WSL 冒烟是否运行、`not-run` 项（真实 VPS、真实站点、Google Drive，见 H-003）。
- USER-ACTIONS：把 H-003 的步骤按实际的命令、包名与界面文案改准；Q-022 有变化就更新。

<a id="prompt"></a>
## 10. 给 Agent B 的交接 Prompt

```text
你是 TRACE 的 Agent B，动作：连续实施 M5（远程版、上传与导入导出）。

- 计划：docs/delivery/stage-plan.md（TRACE-M5-01 修订 1）；设计正本 docs/design/remote.md；决定 DEC-062；需求 FR-REMOTE-001～008；验收 AC-56～64；默认值 USER-ACTIONS 的 Q-022；人工检查 H-003。
- 分支 codex/m5-remote，基点 main @ 8327803；计划所在提交是阶段提交的起点，你的全部改动 amend 进这一个 DCO 提交（git commit --amend -s）。开始时核对一次分支、提交与计划。
- 范围：C0、D1、W1～W9 与必测用例 M5-K01～K24，连续完成、自测、修复，不逐包请示；非阻塞的按 Q-022 默认值实现并写明。
- 必须保留：M1～M4 与 DEC-059～061 的行为与测试；数据结构版本 2（只加可选字段与 IF NOT EXISTS 新表）；INV-01～14；按站点的额度、退避与熔断；安静测试、不碰本机安装的应用；只删登记文件；dist 只留最新一份。
- 不发真实站点请求、不连接网盘；远程行为全部用假站点客户端与假 rclone 验证。真实 VPS 与 Google Drive 是 H-003，保持 not-run。
- 交付：go run ./tools/check、tools/deps 与 -audit、uishots、桌面与远程冒烟、tools/package -server、tools/package -install（装好后 TRACE Dev 保持在托盘运行）；按交接模板写 docs/delivery/stage-report.md；更新正本文档与 USER-ACTIONS（H-003 步骤按实际改准）。
- 未授权：push、PR、合并、标签、发布。
- 完成后给出交付 SHA、报告入口与给 A 的审查 prompt。
```
