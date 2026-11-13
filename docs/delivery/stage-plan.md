# 当前阶段计划：M3（抖音视频与直播）

- **阶段编号**：M3；计划标识 `TRACE-M3-01`，修订 4（2026-11-13）。修订 2：用户答复 Q-014 第 1 条（[DEC-044](../decisions.md#dec-044)），放好测试 Cookie 并授权 ACT-010 的真实检查。修订 3：用户给了检查对象，并指出抖音图文可以把图片、视频和配乐混排（W5、M3-K10、AC-38 随之扩充）。修订 4：用户给了直播间，并指出同一直播间有不变的数字抖音号和会变的另一个数字——`webRid` 改为可失效的缓存，抖音录像改用 `room_id` 作键（W3、W7、M3-K16、V-08）。
- **角色**：A 规划；B 连续实施与自检；A 集中审查（[开发流程 §2](../dev-rules/workflow.md#agent-handoff)）。给 B 的 prompt 在[第 11 节](#prompt)。
- **分支**：`codex/m3-douyin`，A 已从 `main @ 15e229c` 创建。本计划所在的提交是阶段提交的起点；B 的实施、A 的审查修复和人工反馈的修复全部 amend 进这一个 DCO 提交。
- **上一阶段**：M2（安装与更新）的计划与报告在合并提交里：[M2 计划](https://github.com/chialecode/trace/blob/15e229c/docs/delivery/stage-plan.md)、[M2 报告](https://github.com/chialecode/trace/blob/15e229c/docs/delivery/stage-report.md)。
- **决定与需求**：范围记为 [DEC-043](../decisions.md#dec-043)；需求 FR-AUTH-007、FR-DY-001～009、FR-SITE-002、FR-LIVE-004、NFR-NET-002/003、NFR-SEC-003、NFR-DATA-004（[需求 §2.8](../requirements.md#douyin-scope)）；验收 AC-34～AC-44（[路线与验收](roadmap.md#acceptance)）。参考代码的使用方式见 [DEC-044](../decisions.md#dec-044)；其余实现细节的默认值见 [Q-014](../../USER-ACTIONS.md#q-014)，B 按默认实现；真实站点检查是 [ACT-010](../../USER-ACTIONS.md#act-010)。

用户 2026-11-12 要求：下一阶段把抖音的视频和直播也加上；对 A 的需求建议回复“按推荐”（DEC-043），并派调研助手先写了协议与选型初稿，A 在其上审阅定稿（[抖音协议 §11](../research/douyin.md#a-review)、[选型 §4](../research/2026-11-12-douyin.md#a-review)）。A 理解的目标：同一个 TRACE 里，抖音和哔哩哔哩平级——各自登录、解析、浏览、缓存、订阅、检查更新、开播录制，共用媒体库、任务、主页信息流和缓存规则；一个站点出问题（未登录、风控、签名失效）不拖累另一个。

## 1. 范围与退出条件

| 项目 | 约定 |
| --- | --- |
| 阶段内容 | 阶段开始的整理（C0）；站点公共层与按站点的凭证（W1）；数据结构版本 2（W2）；抖音协议客户端与签名（W3）；抖音浏览：解析、作品详情、用户页（W4）；抖音缓存（W5）；按站点的订阅与检查更新（W6）；直播：抖音状态、FLV 录制、哔哩哔哩 FLV 后备（W7）；登录、设置、站点标记与筛选、纯音频播放（W8）；工具：真实检查、密文扫描、截图场景（W9）；文档（W10） |
| 不在本阶段 | 抖音扫码与短信登录、抖音关注列表导入、合集/话题/商城/搜索、评论与弹幕、抖音直播的 HLS-TS 后备（LOOP-008）、纯音频直播录制、任何写操作（点赞、关注）、解密付费或加密内容、JS/WASM 签名引擎、新增第三方依赖 |
| 可连续执行 | 本地实现、合成样本与夹具、测试、截图、打包与本机开发版自动安装、冒烟、文档同步、阶段提交与 amend |
| 外部操作 | 抖音真实请求只在 [ACT-010](../../USER-ACTIONS.md#act-010) 的对象与额度内、Cookie 与对象到位后、经 `tools/actcheck` 执行 1 次；没到位就记 `not-run`，不阻塞。不请求哔哩哔哩真实站点（本阶段对它只有回归）。未授权：push、PR、合并、标签、发布 |
| 必须保留 | M1/M2 的全部行为：哔哩哔哩的登录、续期、解析、缓存、订阅、录制（HLS fMP4）与它们的测试；数据位置与通道隔离（DEC-029）；结构版本守卫（新数据拒绝启动）；安静测试；只删登记文件；凭证隔离与哨兵；更新签名与发布流程；`dist/<通道>/` 只留最新一份（DEC-025） |

**退出条件**

| 条件 | 做法 | 工作包 | 验收 |
| --- | --- | --- | --- |
| 两个站点各自登录 | 凭证按站点分文件分熵；只登录一个站点也能进应用；另一个站点的链接提示先登录且不发请求；退出、失效只影响本站点 | W1、W8 | AC-34 |
| 抖音 Cookie 登录 | 粘贴 Cookie → 清洗 → `profile/self` 确认 → 保存；三种失败分别说明且不保存；明文不出凭证文件 | W1、W3、W8 | AC-35 |
| 解析抖音链接 | 分享文本、短链、作品、用户、直播间各进对的页面；不支持的给提示且 0 请求 | W3、W4 | AC-36 |
| 抖音用户页 | 资料与作品网格，置顶在前，翻页无重复；空壳判“登录已失效”；风控可重试 | W3、W4 | AC-37 |
| 缓存抖音作品 | 视频：最高分辨率、无水印、`moov` 在前、可播放、断点续传；图文：按顺序的每一页（图片、实况、混排的视频页）与配乐；签名地址不落盘 | W5 | AC-38 |
| 订阅检查与自动缓存 | 混合站点的订阅；抖音按 `aweme/post` 检查，置顶不误判；按共用规则自动缓存；一个站点风控不影响另一个 | W6 | AC-39 |
| 按站点的额度与退避 | 抖音每小时 ≤ 120 次规则请求、单独退避；下载熔断按站点 | W1、W5、W6 | AC-40 |
| 抖音直播录制 | 开播状态；HTTP-FLV（AVC/HEVC/AAC）按 GOP 写 fMP4 分片，断流续录、换段、崩溃恢复；只有 HLS 或语音直播说明原因 | W7 | AC-41 |
| 哔哩哔哩 FLV 后备 | 没有 fMP4 流时走同一条 FLV 管线；有 fMP4 时行为不变 | W7 | AC-42 |
| 数据升级 | 四个文件升到结构版本 2，留 `.premigrate-1`；失败可恢复；旧程序拒绝打开 | W2 | AC-43 |
| 站点标记与筛选 | 规定位置的小字“B 站”/“抖音”；主页与媒体库按站点筛选；旧的“只看 B 站”默认值升级为“全部” | W2、W8 | AC-44 |
| 阶段开始的整理 | 见第 7 节 | C0 | 文档检查；报告的整理记录 |

**阶段完成的判定**：B 自检的必测用例（第 8 节）与门禁全部通过，A 集中审查收敛后，才可以请用户授权 push、PR 与合并。ACT-010 的真实检查、人工检查与 Q-014 的答复都不是合并的前提，但真实检查没做时，报告与交付状态必须写明抖音“只经合成验证”，AC-36～38、AC-41 的真实站点部分为 `not-run`；产品验收保持 `not-run`。

**阶段完成后，用户可以：**

- 在登录页切到“抖音”粘贴 Cookie 登录，与哔哩哔哩并存，在“设置 › 账号”里分别管理；
- 把抖音 App 的分享文本或任意抖音链接贴进解析页，打开作品详情、用户页或直播状态，缓存视频和图文（含实况照片与配乐）；
- 在订阅里关联抖音账号，和哔哩哔哩账号一起检查更新、按同一套缓存规则自动缓存、开播自动录制；
- 在主页和媒体库里按“全部 / B 站 / 抖音”筛选。

## 2. A 已核对的现场（2026-11-12，`main @ 15e229c`）

- **站点包**：只有 `internal/site/bilibili`，它依赖 `internal/credential`；`download`、`live`、`subscription`、`library`、`service` 与 `ui`（9 个文件）都直接引用 `site/bilibili`。错误类型是 `bilibili.SiteError`，规则请求的上下文标记在 `bilibili/automation.go`。`tools/check` 的依赖方向检查只约束 `site/bilibili` 不依赖 `storage`、`library`、`download`、`subscription`、`live`、`service`、`ui`（`forbiddenBilibiliDeps`），以及只有界面相关包依赖 MyGo（INV-05）。
- **凭证**：`internal/credential.Store` 一个文件 `credentials/bilibili.bin`，`Open(path)`；API 全是哔哩哔哩语义（`CSRF`、`RefreshState`、`CommitRefreshStep4` 等）；`RedactString` 只覆盖这一份 Cookie。
- **结构版本**：`internal/storage/schema.go` 的 `DataSchemas` 里六个文件 `Current` 都是 1；`GuardDataSchemas` 在后端启动（`service/backend.go:300`）时先拒绝更新的结构、再对较旧的结构调用 `BackupBeforeMigration` 生成 `.premigrate-<旧版本>`（SQLite 连同 `-wal`），已有测试 `TestSchemaGuard*` 四个。
- **数据形状**：`library.db` 的作者列是 `mid INTEGER`；条目键 `CanonicalItemKey(site, kind, id)` = `site:kind:id`；作者目录 `CreatorDirName(mid, uname)` = `<mid>_<名字>`；哔哩哔哩录像目录 `bilibili/<uid>/lives/<房间>-<开始>`。`updates.db` 的 `scheduler_state` 只有 `id=1` 一行（全局退避与额度）。`ui-state.json` 的站点筛选在空值时归一为 `"bilibili"`（`uistate.go:96`、`:133`）。订阅规则类型是 `video`/`draw`/`word`/`forward`。
- **下载**：`download/manager.go:981` 的 `tripCircuitBreaker` 是全局熔断；`video.go:528` 的输出校验要求 `MoovBeforeMdat && HasElst`（抖音的单文件 MP4 常常没有 `elst`）。
- **播放**：`player/mf_windows.go:173` 的 `OpenMediaFile` 先找视频轨，纯音频文件（抖音图文的配乐）打不开。
- **录制**：`live/recorder.go` 只有 HLS fMP4 输入；`SaveRecordingCheckpoint`、`RecoverOrphanParts`、`finalizeLivePart`（`mp4ff` `Defragment`）可以给 FLV 管线复用。
- **依赖**：`go.mod` 已有 `github.com/Eyevinn/mp4ff v0.62.0`；本阶段零新增依赖。
- **工具**：`tools/actcheck` 以 `-act` 选授权（现有 `act005`～`act007`，有账本与发出前拒绝超额）；`tools/uishots` 当前 25 个场景、75 张 PNG；`tools/smoke -pkg dist/trace-dev -out artifacts/smoke`。
- **真实站点**：对抖音 0 次请求。用户 2026-11-12 放好了 `.local/site-secrets/douyin.cookie`（Git 已忽略；A 只在本机核对字段名：53 个字段，有 `sessionid`、`sessionid_ss`、`sid_guard`、`uid_tt`、`ttwid`、`UIFID`、`odin_tt`，**没有 `msToken`**，还有若干 `bd_ticket_guard_*` 与 `__security_*` 动态票据），`.env.local` 已有 `TRACE_TEST_DOUYIN_COOKIE_FILE`，并授权按 ACT-010 进行真实检查。2026-11-13 用户给了检查对象，A 写进本机 `.local/site-check/targets-douyin.json`（Git 已忽略）：2 位用户、1 条置顶视频、1 条新视频、1 条普通图文、1 条图片/视频/配乐混排的图文，以及 1 个直播间的两种链接（主播的数字抖音号，用户说不变；另一个数字会变）。因此：W3 的 Cookie 清洗必须能处理这种真实形状（丢弃动态票据后仍保留身份与访问上下文）；没有 `msToken` 时按[协议 §2](../research/douyin.md#signing)的回退生成查询参数，是否被接受由 V-02 判断。

## 3. 依据、查证与选型

- **协议**：[抖音协议资料](../research/douyin.md)第 1～10 节（静态调研，2026-11-12）与 A 的定稿 [§11](../research/douyin.md#a-review)；V-01～V-08 待 ACT-010 真实核对，结论回写 §11。
- **选型**：[抖音签名与录制选型](../research/2026-11-12-douyin.md)（查询 2026-11-12，含 `mp4ff`、`mediacommon`、JS/WASM 引擎的版本、维护与许可证据）与 A 的定稿 [§4](../research/2026-11-12-douyin.md#a-review)：签名走路线 (a) 纯 Go 自写（含 SM3），直播走 HTTP-FLV 自写解析 + `mp4ff` 写分片，HLS-TS 后备不做。
- **本机参考**：A 按[参考规则](../dev-rules/workflow.md#local-reference)查过索引里“单个视频链接解析”“用户视频列表解析”“抖音签名与访问上下文”“B 站直播录制”“抖音直播录制”五个能力领域（调研助手已逐项读过，结论在两份调研里）；仓库不写参考项目名。B 写签名与 FLV 前可以再查同一索引。
- **实现来源（[DEC-044](../decisions.md#dec-044)，用户已定）**：参考代码**不直接翻译**，先总结成方案再独立实现。签名先**写规格、再按规格独立实现**——B 把字母表、槽位顺序、掩码、盐值、常量、字节布局和至少 3 组测试向量整理成 [douyin.md](../research/douyin.md) 的附录“签名规格”（锚点 `sign-spec`），再只照着规格写 Go 代码，不逐行翻译参考实现的代码、命名和函数拆分；测试向量写明产生方式（固定时钟、随机源与输入）。链接解析、作品列表与分页、FLV 解析与录制同样：参考过的做法先写成方案（补进对应的协议资料或设计章节），再按方案写代码，不照搬参考实现的代码结构、命名与函数拆分。仓库与安装包不署名参考项目，也不出现参考项目名。
- **B 仍需原型验证**：签名输出在合成向量上与规格一致（结构自检 + 固定种子逐字符）；FLV 解析对三种视频封装的合成流可用；`mp4ff` 在磁盘上把 `moov` 移到前面时内存有界（不整文件读入）。线上是否接受签名只能由 ACT-010 判断。

## 4. 架构解耦边界

目标：加入抖音后，下载、录制、订阅、库与存储都只认识“站点”，不认识某个具体站点的协议；两个站点包互不引用；哔哩哔哩的现有行为与测试不变。设计正本是[架构 §2、§4、§5](../design/architecture.md#site-layer)，本节是 B 的落地边界。

### 4.1 依赖方向（`tools/check` 强制）

`checkDependencyDirection` 从只检查 `site/bilibili` 推广为按表检查，规则写进[工程规则](../dev-rules/engineering.md)的依赖方向一行：

| 包 | 不得依赖 |
| --- | --- |
| `internal/site` | 任何 `internal/...` 包（只用标准库） |
| `internal/site/bilibili` | 现有 7 个包 + `internal/site/douyin` |
| `internal/site/douyin`、`.../douyin/sign` | 与哔哩哔哩相同的 7 个包 + `internal/site/bilibili`；`sign` 还不得依赖 `internal/credential` 与 `net/http`（纯计算） |
| `internal/download`、`internal/live`、`internal/subscription`、`internal/library`、`internal/storage`、`internal/settings`、`internal/uistate` | `internal/site/douyin`（只经 `internal/site` 的类型与注册接口使用抖音）。它们对 `site/bilibili` 的现有引用本阶段不强制清零，但**不得新增**；新写的跨站点代码一律用 `internal/site` |
| `internal/ui` | `internal/site/douyin` 的客户端与签名（界面只用服务层的 DTO；与哔哩哔哩现状不对称是允许的，不倒退哔哩哔哩） |

检查器本身要有单元测试：喂一份合成的 `go list -deps` 输出，每条规则各有一个违例被拒、一个合规被放行。

### 4.2 站点公共层 `internal/site`

只放跨包流动的东西（[架构 §4](../design/architecture.md#site-layer)）：`ID`（`bilibili`、`douyin`）与显示名；`Error{Site, Kind, Code, Message, Err}` 与 `KindOf`/`IsKind`；规则请求上下文标记（从 `bilibili/automation.go` 迁来）；账号键 `<site>:<uid>`；直播状态与取流计划（`LiveTransport`：`hls_fmp4`/`http_flv`）；媒体传输规格（允许的主机、请求头，不带 Cookie）；链接分派 `Registry`（分享文本里取链接、按主机交给站点解析）。站点相关的接口（下载执行器、开播状态与取流、作品列表抓取）也在这里定义，由站点包实现、由 `service` 装配注册。

### 4.3 哔哩哔哩适配（零回归）

- `bilibili.SiteError` 改为 `type SiteError = site.Error`，`errors.As` 与现有断言照常；`automation.go` 留转发函数，旧调用点不必一次改完。
- 哔哩哔哩包保留全部导出、行为和测试；为公共接口写薄适配（可放在 `site/bilibili/adapter.go`），不改变请求、节奏、续期和选流。
- **回归证据**：改动前后 `go test ./internal/site/bilibili/... ./internal/download/... ./internal/live/... ./internal/subscription/...` 的测试名集合不减少、断言不弱化（报告里列出新旧测试数）；HLS fMP4 录制的回归测试原样通过。

### 4.4 抖音包 `internal/site/douyin`

文件划分按[架构 §2](../design/architecture.md#layout)：`client.go`（请求、1000 ms + 0～300 ms 抖动的串行节奏、4 MiB 响应上限、不自动跳转、CDN 白名单）、`profile.go`（固定 UA 与推导出的公共参数）、`sign/`（SM3、`a_bogus`、`x-secsdk-web-signature`，时钟与随机源可注入）、`resolve.go`、`auth.go`、`work.go`、`user.go`、`live.go`、`testdata/`。对外只暴露站点自己的数据类型与公共层接口的实现；Cookie 只经凭证仓的读取接口拿到，不缓存在别处。

### 4.5 凭证 `credential.Manager`

- `Manager` 管 `site → Store`：`bilibili.bin` 的路径、熵与读写**完全不变**（用一份 v0.1.1 写出的合成密文夹具证明能读）；`douyin.bin` 用新熵 `trace:douyin:credential:v1`。
- 哔哩哔哩专有的方法（`CSRF`、续期步骤）留在哔哩哔哩的 `Store` 上；抖音的 `Store` 只有保存、读取 Cookie、资料、状态与清除。
- `Manager.RedactString` 覆盖所有站点当前的 Cookie 值；日志脱敏改用它。
- 状态、失效、退出按站点；`auth:changed` 事件带 `site`；一个站点失效只暂停本站点的缓存任务与录制。

### 4.6 服务与界面

- 服务层按[架构 §3](../design/architecture.md#services)：`AuthService.Statuses()`、`LoginWithCookie(site, text)`、`Logout(site)`；`ParseService.Resolve` 先经 `Registry` 分派，再进 `DouyinWork`/`DouyinUser`/`DouyinUserWorks`/`DouyinLive`；`CacheDouyinWork`；`LiveService.Status(accountKeys)`。“必须登录”的判断按链接所属站点做，发请求前拒绝（INV-07）。
- 界面只用服务层 DTO 渲染抖音页（[界面规范](../ui.md#douyin-pages)）；路由 `/douyin/work/:awemeId`、`/douyin/user/:secUid`；登录重定向按站点。

### 4.7 下载、录制、订阅的站点分派

- **下载**：任务按条目键的站点选执行器；熔断、退避与并发按站点分开（AC-40）；媒体请求按站点的传输规格（白名单主机、`Referer`、不带 Cookie）。
- **录制**：录制器按 `LiveTransport` 选输入：`hls_fmp4`（现有）或 `http_flv`（新）；取流计划来自站点实现；输出、checkpoint、恢复与收尾共用。
- **订阅**：检查器按账号站点分派到“作品列表抓取”；调度、每小时额度与退避存到 `site_scheduler_state`，按站点各算各的。

## 5. 数据结构版本 2 的迁移与备份护栏（W2）

设计正本：[数据设计 · 结构版本 2](../design/data-and-cache.md#schema-v2)。

| 护栏 | 要求 |
| --- | --- |
| 迁移前必备份 | `DataSchemas` 中 `library.db`、`updates.db`、`subscriptions.json`、`ui-state.json` 的 `Current` 改为 2，其余仍为 1。`GuardDataSchemas` 在打开任何文件前对每个旧文件调用 `BackupBeforeMigration`，生成 `.premigrate-1`（SQLite 连同 `-wal`、`-shm`），每个文件只留最近一份。**备份失败（含空间不足）就不迁移**，启动显示原因 |
| 拒绝更新的结构 | 现有行为保留：版本比程序新时拒绝启动、不写任何文件。用一份 `Current` 全为 1 的表模拟 v0.1.1，打开 v2 数据必须被拒绝 |
| 原子、可回滚 | SQLite：加列、回填、建索引、建表、搬行与 `PRAGMA user_version = 2` 在**同一个事务**里；JSON：读 → 转换 → 临时文件 + fsync + 改名，一次写成，带 `version: 2`。任一步失败：事务回滚或不改名，原文件原样、备份保留，后端返回错误，界面显示原因，不带着半迁移的数据继续 |
| 可重复 | 已是 2 的文件不再动；每个文件独立迁移，上次只迁了一部分（例如 `library.db` 成功、`subscriptions.json` 失败）时，下次启动只迁剩下的 |
| 旧数据含义不变 | `library.db`：加 `author_uid TEXT` 并回填 `CAST(mid AS TEXT)`，`mid` 列保留，哔哩哔哩代码照常写；条目键、目录与清单不变。`updates.db`：建 `site_scheduler_state`，原单行搬到 `site='bilibili'`，旧表保留不写。`subscriptions.json`：原账号原样，共用规则不变。`ui-state.json`：只有值为 `"bilibili"` 的站点筛选一次性改成 `"all"`（版本 1 里它只可能是默认值），之后用户再选的“B 站”原样保留 |
| 没有降级路径 | 装回 v0.1.1 会拒绝启动；退回要用 `.premigrate-1` 替换（Q-014 第 6 条已告知用户）。不写自动降级 |
| 测试从 v1 夹具出发 | 测试辅助用冻结在 `testdata` 里的 v1 DDL 与 JSON 生成“v0.1.1 数据目录”（含哔哩哔哩缓存、订阅、共用规则、任务、更新记录、“有更新”状态），迁移后逐项比对；故障注入覆盖“备份后、事务中、改名前”三个点 |

## 6. 工作包

| 工作包 | 预期结果与关键失败路径 | 完成证据 |
| --- | --- | --- |
| C0 整理 | 第 7 节清单 | 文档检查；报告的整理记录 |
| W1 站点公共层与凭证 | 第 4.1～4.5 节；依赖方向表与检查器测试；`Manager` 与两站点的 `Store`；`auth:changed{site}`；按站点的熔断与退避的数据结构 | M3-K01、K02、K03 |
| W2 结构版本 2 | 第 5 节全部护栏；`ui-state` 一次性转换；`site_scheduler_state`；`author_uid` 与按作者查询改用 `(site, author_uid)` | M3-K11 |
| W3 抖音协议 | 签名规格附录 + `sign/`；`resolve`（分享文本、短链 ≤ 5 跳与主机白名单、优先级、19 位纯数字提示）；`auth`（Cookie 清洗、`profile/self` 三条件）；`work`（`images[]` 优先、选流与排除规则、付费/加密不可缓存）；`user`（资料、`aweme/post` 分页与 `is_top`、空壳真值表）；`live`（`web/enter`、`profile/other` 的 `room_id`、`reflow/info` 补 `web_rid`、`check_alive`、取 FLV 地址、语音直播识别；身份只认主播 `sec_uid`，`webRid` 是可失效的缓存，每次核对返回的主播，对不上或不可用就清掉改走 `room_id` 路线；`live.douyin.com/<数字>` 先查出主播再打开）。全部用 `httptest` 夹具，不连真实站点 | M3-K04～K08、K16 |
| W4 抖音浏览 | 解析页分派与最近解析；作品详情（视频、图文 + 配乐条）；用户页（作品网格、置顶、翻页、私密账号、直播横幅）；错误文案按[界面规范](../ui.md#douyin-pages) | M3-K06、K07、K17 |
| W5 抖音缓存 | 单文件 MP4：校验不要求 `elst`，有视频轨且时长与站点相差 ≤ 2 秒；`moov` 在后时在磁盘上重排；`Range` 续传；地址过期每个文件最多重取一次详情；媒体白名单、拒绝 HTML/JSON 伪装；图文按顺序保存每一页——图片、实况（静帧 + 片段）、视频页（封面 + 单文件 MP4，选流与校验同视频作品）、认不出的页存图片并记 `unknownKind`——以及配乐，配乐缺失记黄点；按站点熔断；条目键 `douyin:video|dynamic:<aweme_id>`，目录 `douyin/<sec_uid 的 sha256 前 12 位>/...`；签名地址只在内存 | M3-K09、K10、K13 |
| W6 按站点订阅 | 账号可为抖音（`sec_uid`，可带 `webRid`、`uniqueId`）；添加订阅的账号预览识别抖音；检查按站点分派，抖音按 `aweme/post` 第 1 页与起点比较，置顶不当新作品、不提前结束；不确定的空结果记“本轮结果不完整”、不移动起点；每轮 ≤ 20 个抖音账号、间隔 ≥ 2.5 秒、每小时 ≤ 120 次；规则类型映射（视频 → `video`，图文 → `draw`）；“从 B 站关注列表导入”只针对哔哩哔哩 | M3-K12、K13 |
| W7 直播 | 抖音开播状态（订阅网格显示最近一次结果，不为显示单独请求；开播巡检只对选了“开播自动录制”的账号、每个 ≥ 90 秒）；`live/flv.go` 解析（AVC、`CodecID=12` HEVC、Enhanced FLV `hvc1`、AAC；`SetHEVCDescriptor` 前校验 VPS/SPS/PPS 非空）；`live/remux.go` 按 GOP 写 `part-NNN.mp4.part` 并 fsync checkpoint；断流后重新取流续录；参数集变化或时间戳跳变换新段；崩溃恢复与收尾复用现有代码；只有 HLS 或语音直播时说明原因；抖音录像键 `douyin:live:<room_id>`、目录 `lives/<room_id>`（不用会变的直播间号）；哔哩哔哩没有 fMP4 时改用 `http_stream`+`flv` | M3-K14、K15、K16 |
| W8 界面 | 登录页站点切换与抖音粘贴登录（提交后清空输入）；设置“账号”按站点两组；退避按站点分行；站点小字的位置按[界面规范](../ui.md)（订阅网格头像不加）；主页与媒体库的“全部 / B 站 / 抖音”筛选；播放器支持纯音频文件（配乐），视频优先的旧路径不变 | M3-K03、K10、K17 |
| W9 工具 | `tools/actcheck -act act010`：额度与账本按 [ACT-010](../../USER-ACTIONS.md#act-010)，发出前拒绝超额，停止条件，脱敏回执；`tools/checkdocs` 与 `internal/sitecheck` 的密文扫描加抖音模式（`sessionid=`、`sessionid_ss=`、`sid_guard=`、`msToken=`、带 `a_bogus=` 或 `x-signature` 的地址）；冒烟的哨兵扫描覆盖抖音 Cookie；`tools/uishots` 新增场景。`actcheck` 读取的对象文件形状：`{version, act, providedAt, users[{secUid, url}], works[{awemeId, kind: video|note, expect: [pinned|recent|mixed], url}], liveRooms[{webRid, form, url}]}`；`liveRooms[]` 可列同一直播间的多种号码（`form: short_id|other`），用来回答 V-08；检查时没开播就跳过录制，V-07 的录制部分记 not-run | M3-K18、K17 |
| W10 文档 | 按实际实现回写架构、数据、订阅、直播、界面、工程规则（依赖方向表、截图场景数、`act010`）、交付状态与阶段报告；签名规格附录；ACT-010 的现状 | 文档检查；A 审查 |

连续完成全部范围、自测和修复，不逐包请求继续。需要用户的事写进 USER-ACTIONS；非阻塞的先按默认值实现，真正的阻塞只暂停依赖部分。

## 7. 阶段开始的整理（C0）

A 已完成（本计划提交内）：

1. **以前阶段的标签**：需求、路线、设计、界面、交付状态、开发规则、媒体选型与 USER-ACTIONS 中描述能力的“（M2）”“M1 实现”等改为直接描述；指向 M2 计划与报告的链接改为 `15e229c` 的固定链接；决定表与提交信息不改。
2. **文件**：上一阶段的计划与报告在合并提交里（Git 历史即归档），本页与[阶段报告](stage-report.md)已换成 M3；`tmp/` 下 M2 的审查脚本、验证目录与日志已删除（一次性产物，结论已在 M2 报告）。`tmp/m3-research-notes.md`（调研助手的本机笔记，含参考项目名，不入库）保留到签名规格附录写完，之后由 B 删除；`tmp/m3-plan-edits/`（A 改文档的一次性脚本）在计划提交后删除。`artifacts/` 各类别只有最近一份，B 写新一次时按 DEC-025 替换。

B 在本阶段处理：

3. LOOP-007：本阶段改到的文件里，代码注释中的旧计划与问题编号（如“R8”“F-043”“C22”）顺手改成直接描述；不为此改动无关文件。
4. 报告末尾附整理记录（删除与精炼的路径、理由、提炼去向）。

## 8. 必测用例

全部为合成样本（`httptest`、夹具、合成媒体），不连真实站点；测试保持安静（[工程规则](../dev-rules/engineering.md#quiet-tests)）。

| 编号 | 用例 | 对应 |
| --- | --- | --- |
| M3-K01 | 站点公共层：`Registry` 按主机分派、分享文本取链接；账号键；`bilibili.SiteError` 与 `site.Error` 互通（`errors.As`、`KindOf`）；依赖方向检查器对每条规则各一违例一合规 | W1 |
| M3-K02 | 凭证：v0.1.1 写出的 `bilibili.bin` 合成密文照常读取；`douyin.bin` 用独立的熵，用错熵解不开；退出、失效只影响本站点；`RedactString` 同时脱敏两站点的值 | W1、AC-34、AC-35 |
| M3-K03 | 按站点的“必须登录”：只登录抖音时进应用，粘贴哔哩哔哩链接提示先登录且 `httptest` 计数为 0；反之同理；两站点都退出回登录页；失效只暂停本站点的任务与录制 | W1、W8、AC-34 |
| M3-K04 | 抖音 Cookie 登录：合格的保存并返回资料；缺 `sessionid`/`sessionid_ss`、`profile/self` 判未登录、返回验证页三种情况分别说明且不保存、不改变已有凭证；浏览器动态票据被丢弃；只有 `ttwid`/`msToken` 视为未登录；错误不回显输入；凭证文件、日志、`ui-state.json` 的哨兵扫描无 Cookie 值 | W3、AC-35 |
| M3-K05 | 签名：SM3 的国标示例向量；`a_bogus` 在规格附录的固定时钟与随机源下逐字符一致，且通过结构自检（长度、字母表、槽位）；查询串按发送顺序编码、调用方不二次拼接；`x-secsdk-web-signature` 只在有 `uifid` 且路径受保护时附加；`sign` 包不能联网（依赖方向检查） | W3 |
| M3-K06 | 链接：AC-36 列出的每种输入进对的目标；短链 ≤ 5 跳、每跳 HTTPS 且主机在白名单，跳到非抖音域名、带账号密码或端口时停止；19 位纯数字、合集、话题、商城给出提示且请求数为 0；`sec_uid` 直接输入与 `/user/self` | W3、W4、AC-36 |
| M3-K07 | 用户页：第 1 页置顶排前并标“置顶”；第 2 页按 `max_cursor` 取、两页无重复；第 2 页空壳显示“登录已失效”而不是“没有更多”；风控停在原处可重试；私密账号的文案 | W3、W4、AC-37 |
| M3-K08 | 作品详情与选流：`images[]` 非空判图文、顶层 `play_addr` 当配乐；最高分辨率优先、同档按编码偏好再按帧率与码率；排除水印地址与 `is_bytevc1 == 2`；付费或加密标不可缓存；不显示播放数 | W3、W4 |
| M3-K09 | 缓存视频：`moov` 在后的合成文件在磁盘上重排为 `moov` 在前（峰值内存远小于文件大小）；没有 `elst` 也通过；时长差 > 2 秒或没有视频轨失败；中途断开续传；地址过期只重取一次详情；CDN 请求不带 Cookie、非白名单主机与 HTML/JSON 响应被拒；`item.json`、`library.db`、日志里扫不到合成的签名参数哨兵 | W5、AC-38 |
| M3-K10 | 缓存图文：普通图文与混排图文（图片、实况、视频页交错）的每一页按原顺序与类型保存，视频页有封面与通过校验的 MP4，认不出的页不让整条失败；配乐取不到记缺失（黄点）；视频页或实况片段播放时配乐暂停；纯音频文件能在应用内播放，视频文件仍走原路径 | W5、W8、AC-38 |
| M3-K11 | 结构版本 2：v1 夹具升级后四个文件有 `.premigrate-1`、版本 2、原内容与行为不变（`author_uid` 回填、调度状态搬到 `bilibili` 行、`ui-state` 的 `"bilibili"` 变 `"all"`）；再次启动不再迁移，之后选的“B 站”保留；三个故障注入点后原文件与备份都在、显示原因、下次启动可完成；备份失败不迁移；`Current` 全为 1 的表打开 v2 数据被拒绝且不写文件 | W2、AC-43 |
| M3-K12 | 混合站点订阅：首次检查只建起点；抖音新视频按共用规则自动缓存；置顶旧作品不算新、不让检查提前结束；抖音风控时本轮抖音账号停止并说明，哔哩哔哩照常；不确定的空结果记“本轮结果不完整”、起点不动 | W6、AC-39 |
| M3-K13 | 按站点的额度与退避：抖音规则请求超过每小时 120 次推迟到下一轮；每轮 ≤ 20 个账号且间隔 ≥ 2.5 秒；抖音退避时哔哩哔哩的定时检查与下载不受影响，反之亦然；下载熔断只暂停同站点排队的任务 | W1、W5、W6、AC-40 |
| M3-K14 | FLV 录制：AVC、`CodecID=12` HEVC、Enhanced FLV `hvc1` 三种合成流加 AAC 都录成可播放 MP4；断流后重新取流接着录；参数集变化或时间戳跳变换新段；空 VPS/SPS/PPS 不调用 `SetHEVCDescriptor` 并报错；录制中强制结束，重启后已确认部分收尾为可播放文件；只有 HLS 的直播间与语音直播说明原因 | W7、AC-41 |
| M3-K15 | 哔哩哔哩 FLV 后备：只给 `http_stream`+`flv` 的合成直播间走 FLV 管线成功；有 `http_hls`+`fmp4` 时选择与录制和以前完全一样（现有回归测试原样通过） | W7、AC-42 |
| M3-K16 | 抖音直播状态：有 `web_rid` 用 `web/enter`；没有时用 `profile/other` 或 `aweme/post` 的 `room_id`，开播时经 `reflow/info` 补 `web_rid` 并保存到订阅；缓存的 `webRid` 返回别的主播、不存在或不可看时清掉缓存并改走 `room_id` 路线（同次检查最多多 1 次请求），订阅归属不变；两种形态的 `live.douyin.com/<数字>` 都解析到同一主播；录像键用 `room_id`，换直播间号不产生重复条目；`check_alive` 判断已知场次结束；同一账号巡检间隔 ≥ 90 秒；订阅网格不为显示单独请求 | W3、W7 |
| M3-K17 | 截图与断言（`tools/uishots`）：抖音用户页、视频作品详情、图文与配乐条、登录页的抖音分段、设置“账号”两组、主页与媒体库的站点筛选、带站点小字的帖子/卡片/任务行/最近解析；断言小字出现在规定位置、订阅网格头像不加 | W4、W8、AC-44 |
| M3-K18 | 工具：`actcheck -act act010` 在 `httptest` 上——账本超额在发出前拒绝、每类停止条件立即停止全部请求、回执不含 Cookie/签名地址/真实 ID；`checkdocs` 与 `sitecheck` 对合成的抖音 Cookie 与签名地址报错、对正常文档不误报；冒烟的哨兵覆盖抖音 | W9 |
| M3-K19 | 回归与门禁：`go run ./tools/check`、Linux `go vet`、`tools/uishots`、`tools/package`、`tools/smoke` 全部通过；哔哩哔哩相关测试名集合不减少 | 全部 |

真实站点（ACT-010 的 V-01～V-08）不在上表；到位时由 `tools/actcheck` 执行 1 次，结论回写[抖音协议 §11](../research/douyin.md#a-review)，没到位记 `not-run`。

## 9. MVP 与 LOOP

本阶段要贯通的流程（[MVP 基线](roadmap.md#mvp-loop)的“抖音”行）：抖音“粘贴 Cookie 登录 → 解析链接 → 作品详情 / 用户页 → 缓存视频或图文 → 订阅检查与自动缓存 → 开播自动录制”，与哔哩哔哩并存。

可记为 LOOP 的深化：抖音 HLS-TS 后备（LOOP-008）、纯音频直播录制、抖音扫码登录、`sec_uid` 目录名改为可读名字、订阅网格主动查询抖音直播状态、按真实检查收紧或放宽节奏（放宽要用户确认）、清零 `download`/`live`/`subscription`/`library` 对 `site/bilibili` 的直接引用。新的 LOOP 编号写进[交付状态](status.md#loop)。

## 10. 交付与验证

- B 交付前运行并记录：`go run ./tools/check`；`GOOS=linux CGO_ENABLED=0 go vet ./...`；`go run ./tools/uishots -out artifacts/uishots`；`go run ./tools/package`（含本机开发版自动安装，记录 `install-dev.json`）；`go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke`。单轮验证约 10 分钟，长测先请用户授权。
- ACT-010 到位时：`go run ./tools/actcheck -act act010` 一次，回执 `artifacts/site-check/act010.json`；没到位写 `not-run`。
- 报告写在[阶段报告](stage-report.md)，按“工作包 → 实现位置 → 验证证据 → 状态与限制”逐项对应 M3-K01～K19；写明签名规格附录与其他方案章节的位置（DEC-044）、哔哩哔哩回归的新旧测试数、not-run 与限制。
- 不 push、不建 PR、不打标签、不发布。

<a id="prompt"></a>
## 11. 给 Agent B 的交接 Prompt

```text
你是 TRACE 仓库的 Agent B，执行 M3（抖音视频与直播）的完整实施。
- 仓库：本仓库根目录；分支 codex/m3-douyin（基点 main @ 15e229c，A 的计划提交在其上）。
- 先读 AGENTS.md、docs/delivery/stage-plan.md（TRACE-M3-01 修订 4，全部章节）、docs/decisions.md 的 DEC-043 与 DEC-044、USER-ACTIONS.md 的 Q-014 与 ACT-010，再读 docs/research/douyin.md（含 §11 定稿）、docs/research/2026-11-12-douyin.md（含 §4 定稿），并按 AGENTS.md 的触发表读架构、数据、订阅、直播设计与界面规范、工程规则、来源与安全。
- 连续完成 C0、W1～W10 与必测用例 M3-K01～K19，自测并修复，不逐包请求继续。非阻塞的缺项选可逆方案、记下依据后继续，不降低验收。
- 守住计划第 4 节的依赖方向（tools/check 强制）与第 5 节的结构版本 2 迁移护栏；哔哩哔哩的行为与测试不得回归，不删除或弱化任何测试。
- 按 DEC-044：本机参考里的代码不直接翻译。签名先把协议事实与测试向量写进 docs/research/douyin.md 的附录“签名规格”（锚点 sign-spec），链接解析、作品列表、FLV 录制等参考过的做法先写成方案补进对应文档，再只按文档独立实现，不照搬代码结构、命名与函数拆分；仓库里不写参考项目名与本机路径。
- 零新增依赖。全部测试用合成样本与 httptest，不请求真实抖音与哔哩哔哩；测试保持安静。
- 抖音真实请求只在 ACT-010 的对象与额度内、由 tools/actcheck -act act010 执行 1 次，用户已授权并放好 Cookie（.local/site-secrets/douyin.cookie，.env.local 的 TRACE_TEST_DOUYIN_COOKIE_FILE）与检查对象（.local/site-check/targets-douyin.json，形状见计划 W9）；在 M3-K01～K19 全部通过后运行，直播间没开播时 V-07 的录制部分记 not-run。遇到 403/429/444、0 字节、验证页或滑块、带 Cookie 被判未登录时立即停止，不重试、不换会话，绝不尝试解验证码。Cookie、签名地址、真实 ID 不进仓库、日志与回执。
- 不 push、不建 PR、不打标签、不发布。
- 完成后把实现、返工与报告全部 amend 进同一个 DCO 阶段提交（git commit --amend -s），报告写进 docs/delivery/stage-report.md（含整理记录、not-run、给 A 的审查 prompt），在回复里给出交付 SHA 和一个首选下一步。
```

<a id="feedback-6"></a>
## 12. 第六轮实机反馈（M3-F6，DEC-046，2026-11-13 A 规划）

同一分支 `codex/m3-douyin`、同一阶段提交。A 已在本分支修好并实测：抖音付费误判、本机代理下媒体下载被拦、原画优先（DEC-045，ACT-015～018）。本节是剩下的 8 项，B 连续实现，非阻塞处按下列默认值做。

| 工作包 | 预期结果与关键失败路径 | 完成证据 |
| --- | --- | --- |
| F6-1 哔哩哔哩实况 | 用户给的实况动态（本机 `.local/site-check/targets-bilibili-live-photo.json`）在看图层能播放动态片段、静帧与片段对得上；缓存后用系统程序打开片段也能预览（片段存成带 `moov` 在前的普通 MP4，不是只能应用内解码的格式）。先查清是解析没拿到 `live_url`、缓存没存、片段格式不对，还是看图层没接上；只读真实检查单列一项 ACT（动态详情 ≤ 2、图片与片段 CDN ≤ 10 次、≤ 50 MiB），写进 USER-ACTIONS 后在常设授权内运行 1 次 | 合成夹具回归测试；ACT 回执；看图层截图 |
| F6-2 图标 | `tools/icons` 的“T.”去掉右下角的点，正式版与开发版两套图标、安装包与托盘都更新 | 生成的 `assets/icon*.png`；`go run ./tools/package` |
| F6-3 通透的灰 | 内容区保持白；侧栏/上下文面板改为更浅的中性灰（默认 `#f7f7f7` 左右，去暖色偏黄），输入框与卡片尽量白底加细线，不再白灰层层嵌套；对比度满足正文可读。参考截图：浅灰侧栏、白色内容区、无大面积灰卡片 | `go run ./tools/uishots`；[界面规范 §5](../ui.md) 同步 |
| F6-4 窄栏与沉浸式标题栏 | 图标栏从 64 改为 52 左右；窗口改为 MyGo `TitleBarHidden`（v0.8.3 支持 Windows），内容延伸到顶部，系统最小化/最大化/关闭按钮保留，顶部留出拖动区域与按钮让位（用 `TitleBar()` 的内边距），双击顶部最大化；看图与播放浮层不受影响 | UI 测试；uishots；本机开发版手动看一眼（记 H 项） |
| F6-5 代理设置 | 设置“网络”里：跟随系统（默认）/ 不用代理 / 自定义（`http://`、`socks5://` 地址）。“跟随系统”在 Windows 读系统代理设置（Go 的 `ProxyFromEnvironment` 不读注册表，只读环境变量，需要补），也认 `HTTP(S)_PROXY`。所有站点 API、媒体下载与直播录制都走同一设置；媒体的防内网检查对用户配置的代理放行（复用 `site.GuardedMediaDialer`）；改设置立即生效，不用重启 | 单元测试（三种模式、系统代理解析、媒体守卫）；设置页截图 |
| F6-6 一键缓存更早的内容 | 用户页（哔哩哔哩与抖音）“⋯”里“缓存更早的内容”：选一次缓存的条数（10 / 20 / 50，默认 20），从最早的未缓存或当前列表往后取，只入队未缓存的，排进现有下载队列，遵守站点的每小时额度与风控退避；遇到风控停止剩余入队并说明 | 服务层测试（只入队未缓存、条数上限、风控时停止） |
| F6-7 主页不显示圆点 | 主页只列已缓存的内容，不画缓存状态圆点；用户页、作品页、订阅仍按 DEC-039 ⑥ 显示 | UI 测试 |
| F6-8 只看未缓存 | 用户页列表上方加筛选“全部 / 未缓存”，切换不重新请求站点（在已加载的页内过滤，翻页时继续按筛选显示） | UI 测试 |

默认值（非阻塞，可在报告里提出更好的）：图标栏 52；侧栏灰 `#f7f7f7`、细线 `#ececec`；一键缓存默认 20 条；代理默认“跟随系统”。

**B 交付（2026-11-13）**：8 项均已实现并自测，结果、真实检查与限制见[阶段报告 §10](stage-report.md#feedback-6)；默认值全部按上面采用。

给 Agent B 的交接 Prompt：

```text
你是 TRACE 仓库的 Agent B，执行 M3 第六轮实机反馈（docs/delivery/stage-plan.md §12，F6-1～F6-8，DEC-046）。
- 分支 codex/m3-douyin，在现有阶段提交上继续；先读 AGENTS.md、stage-plan §12、DEC-045/046、docs/ui.md、USER-ACTIONS 的 ACT-015～018，再按触发表读相关设计。
- 连续完成 8 个工作包并自测修复，不逐包请求继续；非阻塞处按 §12 的默认值做。零新增依赖；测试用合成样本、保持安静、不动已安装的应用。
- F6-1 的真实请求只在你写进 USER-ACTIONS 的新 ACT 的对象与额度内运行 1 次（常设授权），遇到风控、验证码、412/-352 立即停止；Cookie、签名地址、真实 ID 不进仓库与回执。
- 交付前运行 go run ./tools/check、go run ./tools/uishots -out artifacts/uishots、go run ./tools/package；结果写进 docs/delivery/stage-report.md 新的一节，amend 进同一个 DCO 提交；不 push、不建 PR。回复里给出交付 SHA 和一个首选下一步。
```
