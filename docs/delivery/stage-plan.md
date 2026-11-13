# 当前阶段计划：M4（我的：登录账号的站点内容）

- **阶段编号**：M4；计划标识 `TRACE-M4-01`，修订 4（2026-11-15：加第 13 节，第二轮实机反馈；修订 3 的第 11、12 节已由 B 实现、A 审查收敛）。
- **角色**：A 规划；B 连续实施与自检；A 集中审查（[开发流程 §2](../dev-rules/workflow.md#agent-handoff)）。给 B 的 prompt：本轮在[第 13.6 节](#prompt-feedback-2)（最初的在[第 10 节](#prompt)）。
- **分支**：`codex/m4-account`，A 已从 `main @ f080483` 创建。本计划所在的提交是阶段提交的起点；B 的实施、A 的审查修复和人工反馈的修复全部 amend 进这一个 DCO 提交。
- **上一阶段**：M3（抖音视频与直播）经 PR #7 合并为 `main @ f080483`：[M3 计划](https://github.com/chialecode/trace/blob/f080483/docs/delivery/stage-plan.md)、[M3 报告](https://github.com/chialecode/trace/blob/f080483/docs/delivery/stage-report.md)。
- **决定与需求**：[DEC-047](../decisions.md#dec-047)；需求 FR-ACCT-001～005（[需求 §2.9](../requirements.md#account-scope)）；验收 AC-45～AC-53（[路线与验收](roadmap.md#acceptance)）；界面 [§3.11](../ui.md#mine)；服务契约 [架构 §3](../design/architecture.md#services) 的 `AccountService`；协议 [哔哩哔哩 §21](../research/bilibili.md#account-lists)、[抖音 §12](../research/douyin.md#account-lists)。实现细节的默认值见 [Q-015](../../USER-ACTIONS.md#q-015)，真实站点检查是 [ACT-020](../../USER-ACTIONS.md#act-020)。

用户 2026-11-14 要求：把哔哩哔哩和抖音提供的账号接口功能做在一个功能页里——左侧图标栏一个图标，点进去每个网站的功能默认折叠，选一个网站展开功能，点功能主面板出现不同内容（关注列表、收藏、稍后再看、历史记录等），里面的视频、图文都能点击跳转到解析页；具体功能参考本机参考与 GitHub 项目。A 理解的目标：登录账号在两个站点上“自己的”列表在 TRACE 里一处浏览，列表本身只读、不落盘，条目接入已有的详情、缓存与订阅流程，不另造旁路。

## 1. 范围与退出条件

| 项目 | 约定 |
| --- | --- |
| 阶段内容 | 阶段开始的整理（C0）；哔哩哔哩账号列表客户端（W1）；抖音账号列表客户端与表单 POST 签名（W2）；`AccountService` 服务层（W3）；“我的”界面（W4）；真实检查、截图与隐私测试工具（W5）；文档（W6） |
| 不在本阶段 | 任何写操作（取消收藏、删除历史、移出稍后再看、取消关注、点赞）；一键缓存整个收藏夹或稍后再看（Q-015 第 4 条）；哔哩哔哩收藏的合集、追番追剧、点赞与投币的视频；抖音收藏的合集与音乐、稍后再看；列表的本机持久化与离线查看；抖音扫码；新增第三方依赖 |
| 可连续执行 | 本地实现、合成夹具、测试、截图、打包与本机开发版自动安装、冒烟、文档同步、阶段提交与 amend；ACT-020 的 1 次真实只读检查（常设授权内） |
| 外部操作 | 真实请求只在 [ACT-020](../../USER-ACTIONS.md#act-020) 的对象与额度内，经 `tools/actcheck -act act020` 1 次；Cookie 缺失或失效就记 `not-run` 并写进 USER-ACTIONS 的“待你统一处理”，不阻塞。未授权：push、PR、合并、标签、发布 |
| 必须保留 | M1～M3 的全部行为与测试：两站点登录、续期、解析、缓存、订阅、检查更新、录制、代理、一键缓存更早内容与筛选；数据结构版本 2 与守卫（本阶段不改任何数据文件结构）；按站点的间隔、额度、退避与熔断；凭证隔离与哨兵；安静测试；只删登记文件；`dist/<通道>/` 只留最新一份（DEC-025） |

**退出条件**

| 条件 | 做法 | 工作包 | 验收 |
| --- | --- | --- | --- |
| “我的”入口与折叠分组 | 图标栏第三项“我的”；侧栏两组默认折叠、手风琴展开；未登录与失效只给登录入口、不发请求；窄窗在页面里选择 | W3、W4 | AC-45 |
| 哔哩哔哩关注 | 复用已有关注分组与列表接口；订阅状态与加入订阅；点开用户页 | W1、W3、W4 | AC-46 |
| 哔哩哔哩收藏夹 | `list-all` + `resource/list`；默认在前、私密标记、失效灰显、非视频只给浏览器 | W1、W3、W4 | AC-47 |
| 哔哩哔哩稍后再看 | `toview/web`（WBI）；进度、“全部 / 未看完” | W1、W3、W4 | AC-48 |
| 哔哩哔哩历史记录 | `history/cursor` 游标翻页；按日分组；视频、直播可打开，其他只给浏览器；游标不前进时停下 | W1、W3、W4 | AC-49 |
| 抖音关注 | `following/list` 时间游标；点开抖音用户页；加入订阅 | W2、W3、W4 | AC-50 |
| 抖音喜欢与收藏 | `favorite`、`listcollection`（POST 表单签名）、`collects/list` + `collects/video/list`；3:4 网格；第 1 页空壳判失效 | W2、W3、W4 | AC-51 |
| 真实状态 | 风控、限流、网络、失效、格式变化、空、到底分别说明；不自动重试；熔断按站点 | W1～W4 | AC-52 |
| 隐私与条件入口 | 列表只在内存、不进日志与文件（哨兵）；退出登录即清空；抖音观看历史由 ACT-020 的 D-V5 决定 | W2～W5 | AC-53 |
| 阶段开始的整理 | 见第 6 节 | C0 | 文档检查；报告的整理记录 |

**阶段完成的判定**：B 自检的必测用例（第 7 节）与门禁全部通过，A 集中审查收敛后，才可以请用户授权 push、PR 与合并。ACT-020 与人工检查不是合并的前提；ACT-020 没做时，报告与交付状态必须写明“我的”只经合成验证，AC-46～51 的真实站点部分为 `not-run`。

**阶段完成后，用户可以：** 点图标栏“我的”，展开哔哩哔哩看自己的关注（按分组）、收藏夹、稍后再看、历史记录，展开抖音看自己的关注、喜欢、收藏；点任何视频、作品或用户打开详情，返回时回到原位；在条目的“⋯”里缓存、复制链接、在浏览器中打开，把关注的人加入订阅。

## 2. A 已核对的现场（2026-11-14，`main @ f080483`）

- **Git**：`main` 与 `origin/main` 一致，工作区干净；远端只有 `main`（M3 分支已清理）。
- **界面骨架**：路由常量在 `internal/ui/app.go:32`（`RouteHome`…`RouteLogin`），图标栏条目 `mainNavItems`（`internal/ui/views.go:25`，五项），图标栏绘制 `renderRail`（`internal/ui/shell.go`），侧栏宽度按路由 `contextPanelWidth`（`shell.go:18`），页面限宽 `pageWidth`。打开详情时图标栏把 `RouteVideo/RouteUser/RouteDouyinWork/RouteDouyinUser` 算作“解析”高亮。返回只有一级 `PreviousRoute`（`app.go:946`、`NavigateBack` `app.go:980`），所以“我的”必须自己在内存里保存列表与滚动位置，不能依赖返回链。
- **打开详情**：界面 `openLink`（`app.go:1350`）调用 `backend.OpenLink`，后者在 `service/backend.go:1148` 起解析并写最近解析（`history.Record`）与信息流条目。“我的”的条目一律走这条路径（FR-ACCT-003）。
- **可复用的列表行**：哔哩哔哩投稿紧凑行 `renderUserVideoRow`（`views.go:1361`，目前绑 `bilibili.UserSpace`/`UserVideoItem`）、列表结尾与错误 `renderUserListEnd`/`renderUserListError`（`views.go:1184`、`:1349`）、抖音 3:4 卡片 `renderDouyinWorkGridCard`（`douyin_views.go:1262`，绑 `DouyinUserView`）。W4 要把它们抽成与数据来源无关的版本，用户页与“我的”共用，用户页外观不变。
- **服务层**：`BackendAPI`（`service/backend.go:275`）由 12 个服务接口组成；已有 `FollowingService.RelationTags/RelationFollowings`（哔哩哔哩关注，`site/bilibili/relation.go`，ACT-005 真实检查过）；`HistoryService` 是“最近解析”，“我的”的历史要另起名（如 `WatchHistory`），不要混用。
- **哔哩哔哩客户端**：`doBiliJSON`（`client.go:479`）、`RequireAuth`（`:255`）、WBI 签名 `SignWBIParams`（`wbi.go:54`）与密钥缓存已有；500 毫秒全局间隔已有。
- **抖音客户端**：`doAPIRequest`（`client.go:496`）只发 GET，`apiRequestSpec`（`:430`）没有方法与请求体；签名器 `sign.SignInput` 已有 `Body` 字段，`buildABogus` 已对请求体做 SM3（附录 A 的 `db`，`sign.go:203`、`:486`）；受保护路径表已含 `favorite`、`listcollection`、`collects/list`、`collects/video/list`（[抖音协议 附录 A.7](../research/douyin.md#sign-spec)）。登录账号的 `sec_uid` 在凭证状态里（`credential.Status.SecUID`）。
- **工具**：`tools/actcheck` 已有 `act010`～`act019` 的写法（账本、发出前拒绝超额、程序写回执、脱敏扫描）；`tools/uishots` M3 结束时 36 个场景、108 张 PNG；`tools/check` 是唯一门禁入口。
- **真实站点**：本阶段对两个站点的账号列表接口 0 次请求。`.env.local` 已有两站点测试 Cookie 的路径变量；哔哩哔哩测试 Cookie 上次成功使用是 2026-11-08（ACT-007），是否仍有效未知。
- **本机参考**：找到索引（仓库外）。“用户视频列表解析”与“抖音签名与访问上下文”两个领域的本地副本（2026-11-09）覆盖抖音的喜欢、收藏、收藏夹、关注；哔哩哔哩的收藏、稍后再看、历史没有登记参考，A 查了 GitHub（第 3 节）并在索引旁 `PROPOSALS.md` 追加了 P-013。

## 3. 依据、查证与选型

- **不引入依赖**：全部是站点协议与界面工作，复用标准库与已有模块（`mygo/ui`、`site`、`service`）。没有需要选型的通用能力。
- **哔哩哔哩协议事实**（2026-11-14，GitHub REST）：社区接口文档仓库 `SocialSisterYi/bilibili-API-collect` 已归档并清空（默认分支 `deprecated` 只剩 `README.md`），不能再作来源；改按一个活跃的第三方哔哩哔哩客户端（GPL-3.0，2026-11-13 仍有提交，未归档）的请求与模型核对，另参照本机参考里多站点订阅项目的收藏夹与稍后再看路由（AGPL）。结论写在[哔哩哔哩协议 §21](../research/bilibili.md#account-lists)。两者都是 GPL/AGPL：**只取协议事实，不复制代码**。
- **抖音协议事实**：本机参考里两个抖音项目（MIT 与 Apache-2.0，本地副本 2026-11-09，GitHub 上 2026-11-13、2026-11-09 仍有推送）。按 [DEC-044](../decisions.md#dec-044)，事实已写进[抖音协议 §12](../research/douyin.md#account-lists)，B **只按文档独立实现**，不照搬参考实现的结构与命名。
- **未知项**：协议两节的“待真实检查”清单（B-V1～4、D-V1～5）。抖音观看历史只有路径名，没有参数与结构；抖音稍后再看在网页检索与两份参考里都没有接口，不做（DEC-047）。

## 4. 设计要点

### 4.1 站点层（W1、W2）

- 新文件 `internal/site/bilibili/account.go` 与 `internal/site/douyin/account.go`，只放请求、解析与错误分类，返回站点自己的结构；不依赖 `service`、`ui`、`library`（`tools/check` 的依赖方向照旧）。
- **哔哩哔哩**：`FavFolders(ctx, mid)`、`FavResources(ctx, mediaID, pn)`、`ToView(ctx, pn, viewed)`（WBI）、`WatchHistory(ctx, cursor)`；关注复用 `RelationTags/RelationTagUsers/RelationFollowings`。`id`、`media_id`、`oid`、`kid` 按 int64 或字符串解析，不经浮点。失效（`attr` 1/9）、类型（`type` 2/12/24，`business`）、进度（`-1` 看完）在站点层归一。游标与“到底”规则按协议 §21：`has_more=true` 而空列表、历史游标不前进都返回 `schema_changed`。
- **抖音**：`apiRequestSpec` 加 `method`、`form`（有序参数对）与额外请求头；POST 时把编码后的表单体交给签名器的 `Body`，`Content-Type: application/x-www-form-urlencoded`，查询串照旧签名；GET 行为与字节完全不变（已有签名向量全部通过）。`Followings(ctx, secUID, maxTime)`、`Favorites(ctx, secUID, cursor)`、`Collection(ctx, cursor)`（POST）、`CollectFolders(ctx, cursor)`、`CollectFolderWorks(ctx, collectsID, cursor)`；作品条目复用已有 `aweme` 解析。第 1 页空壳与 `aweme_list: null` 按[抖音协议 §12](../research/douyin.md#account-lists)判为登录失效或被拦截，有 `has_more` 的空列表才算空。`WatchHistory` 只在 ACT-020 的 D-V5 通过后实现（第 4.4 节）。
- 两站点的这些请求都是**用户操作触发的手动请求**：走各自的全局间隔、登录检查与熔断（熔断打开时直接返回原因），不计规则每小时额度，不自动重试。

### 4.2 服务层 `AccountService`（W3）

- 新文件 `internal/service/account.go`，接口按[架构 §3](../design/architecture.md#services)：`Sections(site)`、`Folders(site, kind)`、`Page(site, kind, folderID, cursor)`，`BackendAPI` 组合它。DTO 放 `dto.go`：`AccountSection{site, kind, label, available, reason}`、`AccountFolder{id, title, count, private, isDefault}`、`AccountPage{items, cursor, hasMore, total}`、`AccountItem`（字段见架构表），视频与作品带 `cacheKey`（与用户页同一个键生成函数，圆点用 `CacheService.States`），用户带 `accountKey` 与所属订阅（`OwnerOfAccount`）。
- 站点未登录或失效时 `Sections` 只返回一个 `available=false` 的条目与原因，`Page` 在发请求前返回 `auth_required`。
- **隐私**：服务层不缓存、不写文件、不写信息流与最近解析（打开详情时由 `OpenLink` 照常写）；日志只记 `account.<site>.<kind> page=N items=M` 与错误类别，不记标题、昵称、ID、游标值。
- 抖音“观看历史”在 `Sections` 里由一个包内常量开关控制（默认关），D-V5 通过后 B 打开并补实现与测试；不通过就保持关闭并登记 LOOP。

### 4.3 界面（W4）

- 路由 `RouteMine = "/mine"`；`mainNavItems` 在“订阅”后插入 `{RouteMine, "mine", "我的"}`，新线性图标按[界面 §2](../ui.md#nav)的描边规范画（书签或人形，B 选一个与现有图标风格一致的）。`contextPanelWidth` 对 `RouteMine` 与主页同宽；窄窗时页面内显示选择列表（[界面 §3.11](../ui.md#mine)）。
- 状态全部在 `App` 内存里：展开的站点、选中的功能，以及每个“站点 + 功能 + 收藏夹/筛选”的列表状态（条目、游标、是否还有、加载中、错误、滚动偏移、请求代号）。同一列表同时只有一个请求；后发的覆盖先发的；离开页面不取消已发的请求，但结果只写回它自己的列表。`auth:changed` 时清空该站点的全部列表与选中项。
- 行与卡片复用 4.1 节提到的组件（抽出与数据来源无关的版本）；“⋯”菜单项与动作复用已有实现（缓存、复制链接、在浏览器中打开、加入订阅对话框）。点击条目调用已有的 `openLink`，从而记入最近解析、按“点开即跳转”显示。
- 浏览器入口只接受 `https://` 且主机在站点白名单（`bilibili.com`、`b23.tv`、`douyin.com`）内的地址。

### 4.4 抖音观看历史的条件流程

B 完成 W2 的合成测试后先跑 ACT-020（不必等界面）。D-V5 返回 `aweme_list` 与 `has_more`：把参数与字段补进[抖音协议 §12](../research/douyin.md#account-lists)，实现 `WatchHistory` 与夹具，打开开关，UI 按“喜欢”的网格显示。否则：保持关闭，协议 §12 写明现象，在[交付状态 LOOP](status.md#loop) 加一条，在 Q-015 第 3 条写结果。两种结果都不影响阶段完成。

## 5. 工作包

| 工作包 | 预期结果与关键失败路径 | 完成证据 |
| --- | --- | --- |
| C0 阶段开始的整理 | 按第 6 节处理 | 报告的整理记录；`go run ./tools/checkdocs` |
| W1 哔哩哔哩账号列表客户端 | 第 4.1 节四个函数；夹具覆盖：收藏夹默认/私密位、空收藏夹（`data=null`）、失效条目、音频与番剧类型、`bv_id` 别名；稍后再看 WBI 参数与签名、`viewed=2`、`progress=-1`、`is_pgc`；历史游标三元组推进、`business` 各类、游标不前进 → `schema_changed`；`-101` → 失效、`-352/-412` → 风控、HTTP 412、超大响应、非 JSON | `go test ./internal/site/bilibili/...` 中新增的 `TestFav*`、`TestToView*`、`TestWatchHistory*` |
| W2 抖音账号列表客户端 | `apiRequestSpec` 支持 POST 表单且 GET 字节不变；POST 表单体进入 `a_bogus`（用固定时钟与随机源的向量证明表单体变化会改变签名，GET 向量不变）；关注 `min_time`→`max_time` 推进与不前进停下；喜欢/收藏/收藏夹的 `cursor`/`max_cursor` 两种字段；`collects_id_str` 优先；0 字节、`aweme_list:null`、无 `has_more` 的第 1 页判失效或拦截；网关拦截、验证页、HTTP 403/429 分类；请求不带 `x-secsdk-csrf-token`、带协议要求的 `Referer` | `go test ./internal/site/douyin/...` 中新增的 `TestFollowings*`、`TestFavorites*`、`TestCollection*`、`TestCollectFolder*`、`TestPOSTSign*`；已有签名测试不改断言 |
| W3 `AccountService` | 第 4.2 节；未登录、失效、熔断在发请求前返回；条目归一（可打开/失效/不支持、`browserURL` 白名单、`cacheKey` 与订阅归属）；不写文件、不写最近解析；日志不含哨兵标题与昵称；退出登录后再请求返回 `auth_required` | `go test ./internal/service/...` 中新增的 `TestAccount*`（含隐私哨兵：浏览后扫描数据目录与日志） |
| W4 “我的”界面 | 第 4.3 节与[界面 §3.11](../ui.md#mine)；三档宽度无横向溢出；手风琴折叠；首次选择才请求；滚到底加载、同列表不并发；切换功能与从详情返回不重新请求、滚动位置保留；“刷新”从第一页重来；错误与空、到底分开；失效条目不可点；“⋯”动作复用；退出登录清空；输入法与键盘可用（组合输入时回车不提交、Esc 关菜单、焦点可见） | `go test ./internal/ui/...` 中新增的 `TestMine*`；`tools/uishots` 新场景（第 7 节） |
| W5 工具 | `tools/actcheck -act act020`（账本、额度、停止条件、程序写出的回执、脱敏扫描：回执里没有标题、昵称、ID、Cookie）；`tools/uishots` 加“我的”场景并做断言；测试安静、不抢焦点、不碰已安装的 TRACE | `go test ./tools/...`；`artifacts/site-check/act020.json`（或 `not-run` 记录）；`artifacts/uishots/` |
| W6 文档 | 需求 §2.9 状态、路线 AC-45～53 状态、交付状态“已实现能力”、协议两节按真实检查结果改标记（核/未）、Q-015 第 3 条结果、H-001 第 28 步核对、工程规则里的场景数 | `go run ./tools/checkdocs` |
| D1 依赖盘点与漏洞修复（2026-11-14 B 开工后追加，DEC-048） | 按[依赖盘点](../dev-rules/workflow.md#dependency-inventory)执行。追加时（2026-11-14）`go run ./tools/deps -audit` 失败：`GO-2026-6859`（标准库 `net/http`，代码实际调用）在 Go 1.27.4 修复；盘点满 7 天的有 `go` 行 1.27.3 → 1.27.4、`golang.org/x/sys` v0.49.0 → v0.50.0。把 `go.mod` 的 `go` 行升到 1.27.4、升级 `x/sys` 后 `go mod tidy`，跑 `go run ./tools/check`；`x/sys` 涉及 `appdirs`、`credential`（DPAPI）、`library`（长路径），相关测试必须通过。交付前复跑盘点，届时满 7 天的也升级（追加时未满的有 `mp4ff` v0.63.0、`mygo` v0.9.0、`x/image` v0.48.0）；0.x 的次版本升级视同不兼容升级，尤其 `mygo`（界面框架与打包工具）要单独升级并跑 `uishots`、打包与冒烟，失败回退并在报告写明。没升的写原因；不登记漏洞例外，除非确实无法修复 | `go run ./tools/deps -audit` 为 `passed`；`go run ./tools/deps` 的交付前输出与处理结果写进报告 |

注明：连续完成全部范围、自测和修复，不逐包请求继续；需要用户的事写进 USER-ACTIONS，非阻塞的先按默认值实现，真正的阻塞只暂停依赖部分。

## 6. 阶段开始的整理（C0）

| 对象 | 处理 |
| --- | --- |
| 上一阶段的计划与报告 | A 已用本计划替换 M3 计划，并把报告换成 M4 的空报告；M3 原文在 `f080483` 的固定链接里。仓库内指向它们锚点的链接 A 已改成固定链接（`decisions.md` 的 DEC-043、DEC-046，`USER-ACTIONS.md`，`status.md`）。B 复查是否还有指向 `stage-plan.md#`/`stage-report.md#` 旧锚点的链接 |
| 文档里以前阶段的标签 | `docs/research/2026-11-12-douyin.md`（约 26 处）、`docs/research/douyin.md`（约 14 处）、`docs/design/architecture.md`、`docs/dev-rules/engineering.md`、`status.md` 的 LOOP-013，以及 `decisions.md` 里 DEC-027、DEC-034、DEC-041 仍指向“当前计划”的链接：改成直接描述能力或固定链接；决定的“来源”列与研究资料里的日期记录保持原样。USER-ACTIONS 里 H-001 第 26 步的“（M3）”改成“（抖音）” |
| 生成物 | `artifacts/review/`（2026-11-09 的旧审查日志）：删除；`artifacts/release/`：只留最新一份，属于发布工具的产物，不动；`artifacts/site-check/`：回执是真实检查的证据，保留；`artifacts/uishots/`、`artifacts/smoke/`、`artifacts/package/` 由工具覆盖为最新一份；`dist/` 只留两个通道各最新一份 |
| 代码注释 | 按 LOOP-007，改到的文件里顺手清掉旧的计划与问题编号 |
| A 的临时草稿 | `.local/m4/`（A 规划时的草稿，Git 忽略）：B 开工时删除 |

不得删除：`.env.local`、`.local/site-secrets/`、`.local/site-check/` 的对象与账本、用户数据、许可证与第三方声明。

## 7. 必测用例

| 编号 | 场景 | 证据 |
| --- | --- | --- |
| M4-K01 | 哔哩哔哩收藏夹：默认在前、私密标记、空收藏夹为空不是错误、失效条目不可打开、`type=12/24` 只给浏览器、两页无重复 | W1、W3 单测 |
| M4-K02 | 哔哩哔哩稍后再看：WBI 签名参数正确（固定密钥与时间的向量）、`viewed=2`、进度与“已看完”、`count` 判到底 | W1 单测 |
| M4-K03 | 哔哩哔哩历史：游标三元组推进、两页无重复、`archive`→视频、`live`→主播用户页、其他→浏览器；游标不前进时 `schema_changed`，最多 1 次额外请求 | W1、W3 单测 |
| M4-K04 | 哔哩哔哩关注：分组筛选、订阅归属、加入订阅后归属更新 | W3、W4 单测 |
| M4-K05 | 抖音 POST 表单：签名覆盖表单体；GET 已有向量字节不变；请求头与 `Referer` 正确 | W2 单测 |
| M4-K06 | 抖音关注：`max_time` 游标推进与不前进停下；`sec_uid` 作为打开与订阅的键 | W2、W3 单测 |
| M4-K07 | 抖音喜欢与收藏：`cursor`/`max_cursor` 都认、`collects_id_str` 优先、第 1 页空壳判失效而不是空、真正空列表显示空 | W2、W3 单测 |
| M4-K08 | 风控与错误：两站点的风控、限流、网络、失效、格式变化分别分类；不自动重试；熔断时不发请求；一个站点熔断不影响另一个站点的“我的” | W1～W3 单测 |
| M4-K09 | 未登录与失效：`Sections` 只给登录入口；`Page` 发请求前拒绝；`auth:changed` 后界面清空该站点列表 | W3、W4 单测 |
| M4-K10 | 隐私哨兵：用带哨兵标题与昵称的夹具浏览全部功能后，数据目录、缓存目录与日志里没有哨兵；最近解析只在点开详情后出现那一条 | W3 单测 |
| M4-K11 | 界面：默认折叠、手风琴、首次选择才请求、滚到底加载且不并发、切换与返回不重复请求、滚动位置保留、“刷新”重来 | W4 单测 |
| M4-K12 | 点开：视频、作品、用户分别走 `OpenLink` 打开正确页面并记入最近解析；失效条目点击无动作 | W4 单测 |
| M4-K13 | 三档宽度截图与断言：折叠态、B 站收藏夹（含失效行与私密收藏夹下拉）、稍后再看、历史分组、关注、抖音喜欢网格、抖音收藏下拉、未登录、风控错误、窄窗选择列表；无横向溢出 | `tools/uishots` |
| M4-K14 | ACT-020：1 次只读检查，按停止条件停；回执由程序写出且脱敏扫描干净；结论回写协议两节 | `artifacts/site-check/act020.json` 或 `not-run` 记录 |
| M4-K15 | 回归：已有全部测试、截图场景与冒烟通过；用户页的投稿行与抖音作品卡片外观不变（截图断言） | `go run ./tools/check`；`tools/uishots`；`tools/smoke` |

## 8. MVP 与 LOOP

- 本阶段贯通[路线 §3](roadmap.md#mvp-loop)“我的”一行的“MVP 必须”。
- 可以记为 LOOP 的深化：一键缓存整个收藏夹或稍后再看；收藏的合集、追番追剧、抖音收藏的合集与音乐；抖音观看历史（D-V5 不通过时）；列表搜索与排序切换；在“我的”里批量加入订阅。
- 不能记为 LOOP：任何一个功能的翻页、错误分类、空与到底的区分、点开详情、隐私（这些是 MVP 底线）。

## 9. 交付与验证

- 依赖（D1，追加）：交付前跑 `go run ./tools/deps` 与 `go run ./tools/deps -audit`，`-audit` 失败不能交付；联网失败记 `not-run`。**A 审查时专门复核 D1**：它是 B 开工后加进计划的，B 可能没读到；没做或没做完时，A 按审查规则直接补做（局部、可自测），并在报告的审查部分写明。
- 门禁：`go run ./tools/check`（含 `checkdocs`、`go vet`、`go test`、依赖方向）；Linux `go vet` 交叉检查；`go run ./tools/uishots -out artifacts/uishots`；`go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke`。
- B 交付整个阶段时执行 `go run ./tools/package -install` 把最新开发版装到本机，版本写进报告与交付消息（[工程规则 §2.2](../dev-rules/engineering.md#dev-install)）。
- 报告 `docs/delivery/stage-report.md` 按[交接模板 §2](../templates/agent-handoff.md)写：工作包 → 实现位置 → 实际命令与证据 → 状态与限制；附整理记录、ACT-020 结果、not-run 与限制、给 A 的审查 prompt。
- 提交：全部 amend 进本计划所在的阶段提交（DCO），收尾时把提交消息改写为对最终结果的描述，例如 `feat(m4): browse the signed-in accounts' lists under 我的`。

<a id="prompt"></a>
## 10. 给 Agent B 的交接 Prompt

```text
你是 TRACE 的 Agent B，负责连续实施 M4（我的：登录账号的站点内容）。

计划：docs/delivery/stage-plan.md（TRACE-M4-01 修订 1）。先按 AGENTS.md 读入口文档，再读计划引用的正本：
DEC-047、需求 §2.9（FR-ACCT-001～005）、路线 AC-45～53、界面 §3.11、架构 §3 的 AccountService、
哔哩哔哩协议 §21、抖音协议 §12、USER-ACTIONS 的 Q-015 与 ACT-020。

分支 codex/m4-account，基点 main @ f080483；分支上第一个提交是 A 的计划提交，你的全部实现、文档与修复
都 amend 进这一个 DCO 提交（git commit --amend -s）。开工时核对一次分支、提交与工作区；.local/m4/ 是 A 的
草稿，按 C0 删除。

连续完成 C0、W1～W6 与必测用例 M4-K01～K15，不逐包请示：本地实现、合成夹具、测试、截图、打包、冒烟、
文档同步都在授权内。抖音协议按 DEC-044 只按仓库文档独立实现，不照搬参考代码；GPL/AGPL 参考只读事实。
W1、W2 的合成测试通过后先跑 ACT-020（常设授权内，1 次，额度见 USER-ACTIONS；Cookie 缺失或失效就记
not-run 并写进“待你统一处理”），按计划第 4.4 节决定抖音观看历史。

必须保留：M1～M3 的全部行为与测试、数据结构版本 2（本阶段不改数据文件结构）、按站点的间隔/额度/退避/熔断、
凭证隔离与哨兵、安静测试、只删登记文件。不做任何写操作，不新增第三方依赖。

交付前：go run ./tools/check、Linux go vet、tools/uishots、tools/smoke 全部通过；go run ./tools/package -install
更新本机 TRACE Dev；按交接模板写 docs/delivery/stage-report.md（含整理记录、ACT-020 结果、not-run 与限制、
给 A 的审查 prompt），同步需求/路线/交付状态/协议/USER-ACTIONS（H-001 第 28 步、Q-015）。
未授权：push、PR、合并、标签、发布。最终回复给出交付 SHA、开发版版本号和一个首选下一步。
```

<a id="feedback-1"></a>
## 11. 修订 2：M4 实机反馈第一轮（2026-11-14）

用户在 M4 交付后给了 8 条反馈。A 已直接修完其中 4 个缺陷和 1 个小调整，并查清了 1 个站点问题（[阶段报告 §7](stage-report.md#feedback-1)）：缓存规则展开后再缩起闪退（F-003）、直播录像转换失败（F-004）、抖音订阅头像重启后丢失（F-005）、哔哩哔哩实况照片不会动（F-006，改为翻到即播）、抖音“喜欢”空响应让“收藏”也失败（F-007）。剩下 3 条是界面与功能调整（[DEC-049](../decisions.md#dec-049) ①～③），由 B 在本阶段内连续实现，交付后 A 再集中审查。理解与默认值见 [Q-016](../../USER-ACTIONS.md#q-016)。

### 11.1 工作包

| 包 | 内容 | 主要位置 |
| --- | --- | --- |
| R1 解析页 | 上下文侧栏在解析页也显示（`renderContextPanel` 把 `RouteParse` 和视频、用户页一样交给最近解析侧栏）。侧栏标题“最近解析”，右侧一个“⋯”（全部清空，行内确认）；每行是小封面或头像、单行标题、一行“类型 · 站点”，点行打开，悬停出现“⋯”（在浏览器中打开、删除；删除同时清对应缩略图）；当前打开的条目高亮。主面板只剩一个**居中的大输入框**（最宽约 640px，高约 52px，全圆角，左侧当前账号头像，右侧强调色“立即解析”），像搜索引擎首页那样在页面竖直方向略偏上居中；识别失败的一句话说明在输入框下面；主面板不再列最近解析。回车、输入法组合时回车不提交、解析中禁用这些行为不变 | `internal/ui/views.go` `renderParseRoute`、`shell.go` `renderHistoryPanel` |
| R2 媒体库离线 | ① 去掉“发布时间 / 缓存时间”切换，固定按发布时间从新到旧（录像按开播时间）；已保存的旧排序偏好忽略，不报错。② 新增媒体库自己的本机详情路由（如 `/library/item`）：“⋯ → 详情”在这里打开，内容复用现有本机内容详情的渲染，但图标栏仍高亮“媒体库”，上下文侧栏仍是媒体库侧栏，返回回到媒体库原来的筛选与滚动位置。③ 媒体库里的任何点击（缩略图、标题、详情里的图片和视频、录像分段）都不切到解析的路由，也不发站点请求；详情里需要联网的动作（缓存、补存、升级、刷新元数据）不显示，只在“⋯”里留一个“在解析页打开”作为用户明确选择的联网入口。④ 先复现用户说的“点视频或图片跳到解析页”，把所有从媒体库进入 `RouteVideo` / `RouteDouyinWork` 的路径列出来逐一改掉 | `internal/ui/library_view.go` `openLibraryTile`、`OpenLibraryItemDetail`、`shell.go` 图标栏高亮 |
| R3 直播录制标签 | ① 图标栏“任务”的角标只数排队和进行中的缓存任务，不数录制（`activeWorkCount`）。② 任务页顶部两个标签“缓存”“直播录制（N）”，原来排在最上面的录制行移到“直播录制”标签；没有录制时写“现在没有直播在录制”。③ 在“直播录制”里点一场正在录制的直播，在页面里**实时播放录制中的画面**：读正在写入的录像文件（哔哩哔哩是 HLS fMP4 追加，抖音是 FLV 转写的 fMP4），**不另开站点连接**，离开标签、停止录制或这一段结束换段时停止或跟到新段；画面落后直播约 10 秒以内可以接受，打开后约 5 秒内出画。先做时限 1 天的技术验证：播放器能否读一个还在增长的 fMP4（例如给 `internal/player` 加一个读到文件末尾就等待新数据的读取方式）；两条录制路径写出的 `.part` 是否都是可边写边读的分片 MP4。验证不通过的那条路径，界面照实说明“这场录制暂不支持实时预览”，记 LOOP，不用另开连接绕过。④ 实时弹幕不在本轮，预览区不画弹幕，也不显示弹幕开关 | `internal/ui/views.go` `renderTasksRoute`、`shell.go`、`internal/live`、`internal/player` |
| R4 文档 | 按实现同步[界面规范](../ui.md) §2（任务角标）、§3.2（解析页）、§3.6（媒体库、任务页标签与实时预览）；需求与路线如有新条目一并补；报告追加 B 的交付节 | `docs/` |

### 11.2 必测用例（追加）

| 编号 | 用例 |
| --- | --- |
| M4-K16 | 解析页主面板只有输入框（没有最近解析列表）；侧栏列出最近解析，点行打开，“⋯ → 删除”删掉条目和它的缩略图；“全部清空”要行内确认 |
| M4-K17 | 视频页、用户页、解析页三处侧栏是同一份最近解析列表，当前条目高亮 |
| M4-K18 | 媒体库没有排序切换；带旧的“缓存时间”偏好启动时按发布时间排，不报错 |
| M4-K19 | 从媒体库点视频、图片、录像分段、“⋯ → 详情”以及详情里的图片和视频，`CurrentRoute` 始终是媒体库的路由、图标栏高亮“媒体库”，假后端记录的站点请求数为 0；返回后筛选与滚动位置不变 |
| M4-K20 | 只有录制在进行时，“任务”没有角标；有排队或进行中的缓存任务时角标只数这些 |
| M4-K21 | 任务页两个标签，录制行只在“直播录制”里；点正在录制的直播开始实时预览，录像文件继续增长时画面跟着前进；离开标签或停止录制后预览停止、文件句柄释放（录像能正常转换和改名） |
| M4-K22 | 实时预览期间站点请求计数不增加（只有录制本身的请求） |
| M4-K23 | 回归：A 修的 F-003～F-007 的测试（`TestCacheRuleFoldsAgainWithoutCrashing`、`TestFinalizeLivePartTrimsSubFrameFragmentOverlap`、`TestRecoveryConvertsUnconvertedSegmentUnderFinalName`、`TestDouyinSubscriptionKeepsALocalAvatar`、`TestDouyinLikesRefusalLeavesTheSiteRunning`、实况照片自动播放）保持通过 |

### 11.3 不在本轮

- 实时弹幕（哔哩哔哩直播弹幕的 WebSocket 协议、抖音直播弹幕的 protobuf 与签名）：A 在下一阶段先写协议资料、选型（WebSocket 客户端优先用已在依赖图里的 `golang.org/x/net/websocket`，要另行查证）并申请真实检查，再交给 B。
- 抖音“喜欢”的浏览器通道：等用户在 Q-016 第 1 条决定。

### 11.4 交付

同第 9 节：门禁、Linux vet、截图（解析页、媒体库详情、任务页两个标签各补一张）、冒烟、`go run ./tools/package -install`；报告追加“B：第一轮反馈的实现”一节和给 A 的审查 prompt；全部 amend 进同一个阶段提交。

<a id="prompt-feedback-1"></a>
### 11.5 给 Agent B 的交接 Prompt

并入[第 12.5 节](#prompt-feedback-1b)：第 11、12 节一起实施。

<a id="feedback-1b"></a>
## 12. 修订 3：第一轮反馈的补充（2026-11-15）

用户又给了 4 条。A 已修 2 条（[阶段报告 §7](stage-report.md#feedback-1)）：托盘右键菜单只留“打开”“退出”、在指针处用系统原生菜单打开（F-008，DEC-050 ③）；抖音用户页作品多时卡顿、“缓存更早的内容”后更卡（F-009：每帧为全部已加载作品读媒体库状态、每张可见卡片再读一次条目，改为只读可见行并按缓存状态版本缓存）。另外 2 条是 [DEC-050](../decisions.md#dec-050) ①②，和第 11 节一起由 B 实现；节奏的默认值见 [Q-017](../../USER-ACTIONS.md#q-017)。**第 11 节 R3 ② 的“任务页顶部两个标签”由本节 Q2 的侧栏代替**，R3 ①③④ 不变。

### 12.1 设计要点

- **缓存队列任务**（服务层，新文件，如 `internal/service/cache_queue.go`）：一个任务 = 一个账号的一个列表（哔哩哔哩“动态”或“投稿”，抖音“作品”）从最新往更早全部缓存。状态：排队中、进行中、等待（下次时间）、等待退避结束、已暂停、已完成（到列表末尾）、已停止（原因）。保存在数据目录的新文件（如 `cache-queue.json`，自带版本号；不改现有数据文件的结构，版本 2 不变），只存站点、账号 ID、名称、列表、翻页游标、计数、状态与时间，不存地址与凭证；重启后接着做。
- **节奏**（默认值见 Q-017，集中在一处常量，测试可注入时钟与随机源）：同一站点同时只跑一个队列任务，其余按加入顺序排队；每次只把 1 条未缓存的条目排进下载队列，**等它下载结束**再随机等一段（默认 40～150 秒），每缓存 15～25 条（随机）长歇 10～25 分钟；需要下一页时翻页（自动请求，计入站点每小时额度、遵守退避与熔断）；遇到风控、限流、额度用完或站点退避就停在“等待退避结束”，退避过后再随机等 10～30 分钟继续；登录失效暂停，重新登录后继续；应用退出时停下、启动后随机等 1～5 分钟再继续。复用 `batch_cache.go` 已有的候选与翻页逻辑（已缓存或不能缓存的跳过并计数），不另写一套。
- **入口**：用户页“⋯”的“缓存更早的内容”子菜单换成一项“全部缓存”（哔哩哔哩按当前标签是动态还是投稿；私密账号没有）；这个列表已在队列里时换成“在缓存队列中查看”。点“全部缓存”后顶部提示“已加入缓存队列，会在后台慢慢缓存”，不弹确认框。`CacheOlderUserItems` 与 10 / 20 / 50 条的选项删掉，相应的测试改为覆盖新入口；FR-USER-004 与界面 §3.4 改写。
- **Q2 任务页侧栏**：`renderContextPanel` 给 `RouteTasks` 一个侧栏：“下载任务（N）”（N = 排队与进行中）、“缓存队列”分组（每个队列任务一行：头像、名称、小字“已缓存 x · 跳过 y”与状态或下次时间，正在进行的高亮）、“直播录制（N）”。点“下载任务”页面显示现在的任务列表（“检查画质更新”“清除已结束”不变）；点某个队列任务页面显示它的详情：名称与列表、状态与原因、计数、最近缓存的几条（点开本机内容）、操作“暂停 / 继续”“取消”（取消不删已缓存的内容）；点“直播录制”显示第 11 节 R3 的录制与实时预览。窄窗时侧栏退回为页面顶部的分段选择。图标栏“任务”的角标仍只数排队与进行中的下载任务（不数队列任务等待中的条目，也不数录制）。

### 12.2 工作包

| 包 | 内容 |
| --- | --- |
| Q1 缓存队列 | 服务层任务、持久化、节奏与停止规则、事件；`BackendAPI` 增加列出、加入、暂停、继续、取消；用户页入口 |
| Q2 任务页侧栏 | 侧栏、下载任务页、队列任务详情页、直播录制页（与第 11 节 R3 合并实现） |
| Q3 文档 | 需求 FR-USER-004 改写、界面 §3.4 §3.6、数据设计（新文件）、订阅设计（与定时检查共用额度的说明）、交付状态 |

### 12.3 必测用例（追加）

| 编号 | 用例 |
| --- | --- |
| M4-K24 | 假时钟与固定随机源下，一个 300 条的列表按“入队 1 条 → 等下载结束 → 随机间隔”推进，间隔落在默认范围内、每 15～25 条长歇一次；任何时刻这个任务在下载队列里最多 1 条 |
| M4-K25 | 翻页计入站点每小时额度；风控、限流、额度用完时进入“等待退避结束”，不再发请求，退避过后继续；另一个站点的队列不受影响 |
| M4-K26 | 重启后任务与游标恢复，已缓存的不重复入队；登录失效暂停、重新登录后继续；暂停、继续、取消（取消不删已缓存的内容）；同一列表重复加入只保留一个 |
| M4-K27 | 用户页“⋯”只有“全部缓存”（或“在缓存队列中查看”），没有按条数的选项；私密账号没有 |
| M4-K28 | 任务页侧栏三项与队列任务行；点哪项显示哪项；角标只数排队与进行中的下载任务 |
| M4-K29 | 隐私：`cache-queue.json`、日志与事件里没有 Cookie、签名地址与查询串（扩展现有哨兵测试） |
| M4-K30 | 回归：A 修的 F-008、F-009 的测试（`TestTrayMenuOnlyOpensAndQuits`、`TestDouyinWorkGridDoesNotReadTheLibraryEachFrame`）保持通过 |
| M4-K31 | 冒烟连跑 10 次全部通过；若复现 LOOP-015 的 `StealFocusDetected`，查清是否由托盘原生菜单（F-008）引起并修复，查不清就在报告里写明现象与次数 |

### 12.4 不在本轮

- 队列的速度设置界面（先用 Q-017 的默认值；需要时再加到设置“缓存与下载”）。
- 订阅的自动缓存改用队列节奏（订阅自动缓存只处理新动态，量小，保持现状）。

<a id="prompt-feedback-1b"></a>
### 12.5 给 Agent B 的交接 Prompt（修订 3，含第 11、12 节）

```text
你是 TRACE 的 Agent B，继续 M4，实施第一轮实机反馈：计划 docs/delivery/stage-plan.md 第 11 节（修订 2）
与第 12 节（修订 3），两节一起做；第 11 节 R3 ② 的任务页标签由第 12 节 Q2 的侧栏代替。

先按 AGENTS.md 读入口文档，再读：DEC-049、DEC-050、计划第 11、12 节、阶段报告 §7（A 已修的 F-003～F-009）、
界面规范 §2、§3.2、§3.4、§3.6、§3.10、需求 FR-USER-004、batch_cache.go、USER-ACTIONS 的 Q-016、Q-017。

分支 codex/m4-account，在现有的阶段提交上继续；你的实现、测试与文档全部 amend 进这一个 DCO 提交
（git commit --amend -s）。开工时核对一次分支、提交与工作区，保留已有改动。

连续完成 R1～R4、Q1～Q3 与 M4-K16～K31，不逐包请示：本地实现、合成夹具、测试、截图、打包、冒烟、文档同步都在授权内。
R3 先做时限 1 天的技术验证（播放器能否读还在增长的 fMP4；两条录制路径的 .part 能否边写边读），
不通过的路径照实说明并记 LOOP，不另开站点连接。缓存队列的节奏用 Q-017 的默认值，集中在一处常量，
测试注入时钟与随机源，不真的等待。本轮不做实时弹幕，不新增第三方依赖，不发真实站点请求。

必须保留：M1～M4 已有行为与测试（被 DEC-049、DEC-050 替代的除外，相应测试改写而不是删掉）、
现有数据文件结构版本 2（缓存队列用新文件）、按站点的间隔/额度/退避/熔断、凭证隔离与隐私哨兵、
安静测试（不抢焦点、不碰已安装的应用）、只删登记文件。

交付前：go run ./tools/check、Linux go vet、tools/uishots（补解析页、媒体库详情、任务页三项的截图）、
tools/smoke 全部通过；go run ./tools/package -install 更新本机 TRACE Dev；阶段报告追加“B：第一轮反馈的实现”
（工作包 → 位置 → 命令与证据 → 状态与限制、not-run、给 A 的审查 prompt），同步需求、界面规范、数据设计、
交付状态与 USER-ACTIONS（H-001 第 30 步）。
未授权：push、PR、合并、标签、发布。最终回复给出交付 SHA、开发版版本号和一个首选下一步。
```

<a id="feedback-2"></a>
## 13. 修订 4：第二轮实机反馈（2026-11-15）

用户装了 `0.1.2-dev.202611150933` 后给了 8 条反馈（[DEC-051](../decisions.md#dec-051)）。A 已直接处理 4 条（[阶段报告 §10](stage-report.md#feedback-2)）：删除作者最后一条后清掉空文件夹（②）、订阅图标橙点的悬停说明（④）、更新开发版时关闭并重开正在运行的 TRACE Dev（⑤，原有流程，本轮实测）与日志检查（下载进度刷屏、崩溃不留痕迹，已修）、订阅头像两站点都存本机副本并修好用户页订阅不带头像（⑥）。剩下 4 条（①③⑦⑧）由 B 在本阶段内连续实现；理解与默认值见 [Q-018](../../USER-ACTIONS.md#q-018)。基点是 A 本轮的阶段提交（见报告 §10）。

### 13.1 设计要点

- **U1 更新页**（DEC-051 ①）：设置新增分类“更新”（放在“关于”之前，图标用现有的刷新类图标）。内容自上而下：
  - 当前版本卡片：版本、通道（正式版 / 开发版）、提交与构建时间。
  - 更新方式一句话：正式版“打开 TRACE 时每 24 小时自动检查一次（可在下面关闭），也可以立即检查”；开发版“开发版不会自己联网检查；Agent 打包后会自动安装，正在运行的 TRACE Dev 会先退出，装完在托盘打开”。
  - 正式版：上次检查（时间与结果：已是最新 / 发现 x.y.z / 失败原因）、下次检查时间、“立即检查”与原有的“自动检查更新”“自动下载更新”开关（从“关于”移过来，“关于”只留一行“版本与更新见‘更新’”）。开发版：不显示检查按钮与开关，显示“本机安装的开发版”（卸载项里的版本，与正在运行的版本不同时写“已安装更新的版本，重启 TRACE Dev 后生效”）。
  - **检查记录**（最近 20 次：时间、自动/手动、结果）与**更新记录**（最近 50 次：时间、从 X 到 Y、方式：自动更新 / 本机打包安装 / 手动安装）。
  - 记录存数据目录新文件 `update-history.json`（`version: 1`，原子写，更高版本拒绝、损坏备份后重建；不改现有数据文件结构）。更新记录在启动时由 `DecideStartup` 发现版本变化时写一条（开发版记“本机打包安装”；正式版有自动更新的重启标记时记“自动更新”，否则“手动安装”）；检查记录由 `update.Controller` 每次检查结束时写（开发版没有检查记录）。只存版本、时间、方式与结果文字，不存下载地址。
- **L1 录制不限数与录制异常**（DEC-051 ③）：
  - 去掉同时录制数：设置“订阅与直播”里删掉“同时录制数”一行，`live.Manager` 的三处上限检查（手动开始、哔哩哔哩自动录制、抖音录制）删掉；`settings.json` 里的 `maxRecordings` / `live.maxConcurrentRecordings` 继续读入但不再使用（不报错、不改结构）。旧测试中断言上限的改为断言不限。
  - 录制异常事件（录制会话内产生，随录像条目保存在 `item.json` 的 `live.incidents`，可选字段，旧程序忽略；录制中的也在内存里实时可见）：`reconnect`（断流后重新取流：断开时刻、恢复用时）、`lost_segments`（HLS 媒体序号跳号：丢了几段、按目标时长估算的秒数；FLV 断开到重连之间的空档秒数）、`slow`（连续 30 秒下载速度低于码率，记开始时刻与持续时长，恢复后结束）、`disk`（空间不足停止）、`convert_failed`（收尾转换失败，沿用 F-004 的原因）。每类相邻 1 分钟内的合并成一条，每场最多 200 条。不另发站点请求。
  - 界面：“任务 › 直播录制”页里每场录制（进行中与最近结束的）行上有异常时显示一个“⚠ N 处异常”小字；任务侧栏在“直播录制（N）”下加一项“录制异常（N）”（N = 最近 7 天有异常的录制场数，为 0 时显示但不带数字），点开页面按场列出（直播间、开始时间、异常列表：时间、类型的中文说明、影响时长），已结束的可点开录像详情（本机路由，同媒体库）。窄窗与侧栏一致退回为顶部分段选择。
- **S1 订阅详情“内容 / 账号”**（DEC-051 ⑦）：
  - 标签改为“内容”（默认）与“账号”。“内容”是这个订阅**全部关联账号**已缓存内容的信息流：与主页时间线同一套帖子渲染、虚拟化与“到底加载更早”，按发布时间从新到旧，只读本机（与主页“有更新”里点进某人的时间线同一数据源，按订阅的全部账号筛选）；没有内容时写“还没有缓存这个订阅的内容”，并给“检查更新”（订阅“⋯”里已有）的提示文字，不加新主按钮。
  - “账号”合并原“直播”：每个账号一行，站点小字、头像、昵称、UID/抖音号，下面一行直播状态（未开通直播间 / 未开播 / 直播中“标题 · 清晰度”/ 录制中“已录 时长 · 大小”）；行右侧在直播中时是“录制”（强调色）或“停止录制”，其余操作（打开主页、在浏览器中观看、用作头像、解除关联、刷新直播状态）收进行“⋯”。已录制的录像不在这里列（在“内容”里作为录像帖子出现）。底部“关联另一个账号”保持。
  - 旧 `SubDetailTab` 值 `live` 读入时当作 `accounts`。界面规范 §3.9 改写。
- **S2 缓存规则批量指定订阅**（DEC-051 ⑧）：设置“缓存规则”里每条规则展开后，在编辑器下方加“使用这条规则的订阅（N）”一行和“选择订阅…”按钮，弹出对话框：搜索框 + 全部订阅的复选列表（头像、名称，已在用这条规则的预先勾上，在用别的规则的写“当前：规则名”），“全选”“全不选”，底部“应用到 N 个订阅”。应用时一次写入（服务层新增 `SetSubscriptionsRule(ruleID, subIDs)`：把勾上的设为这条规则；原来用这条规则、这次取消勾选的改回“默认”规则），不是逐个调用。默认规则也能这样指定。订阅详情的规则按钮保持。

### 13.2 工作包

| 包 | 内容 | 主要位置 |
| --- | --- | --- |
| U1 更新页 | 13.1 的“更新”分类、`update-history.json`、记录写入点 | `internal/update`、`internal/shell/updates.go`、`internal/service`（新文件读写与 `BackendAPI`）、`internal/ui/views.go` 设置 |
| L1 录制不限数与异常 | 去掉上限；`live.incidents` 事件采集（HLS 跳号、FLV 重连空档、慢速、空间、转换）；任务侧栏“录制异常”与页面 | `internal/live/{recorder,douyin_live,manager}.go`、`internal/library`（可选字段）、`internal/ui/tasks_view.go`、`settings` 界面 |
| S1 订阅详情 | “内容”信息流与“账号”合并直播 | `internal/ui/subscriptions.go`、`feed.go`（复用按作者筛选的数据源）、`internal/service` 若需按订阅筛选的查询 |
| S2 规则批量 | 规则编辑器下的批量选择对话框与 `SetSubscriptionsRule` | `internal/ui/rules_view.go`、`internal/subscription`、`internal/service` |
| D2 文档 | 需求（FR-UPD、FR-SET-002、FR-LIVE-002、订阅详情与规则相关条目）、界面 §3.6 §3.9 设置、数据设计（`update-history.json`、`live.incidents`）、直播设计、交付状态、USER-ACTIONS H-001 第 31 步 | `docs/` |

### 13.3 必测用例（追加）

| 编号 | 用例 |
| --- | --- |
| M4-K32 | 设置“更新”：正式版显示上次/下次检查与记录，“立即检查”后多一条检查记录；开发版不显示检查按钮，显示更新方式说明；启动时版本变化写一条更新记录（开发版“本机打包安装”，正式版带重启标记“自动更新”）；`update-history.json` 版本 1、损坏时备份重建、更高版本拒绝；没有下载地址 |
| M4-K33 | 同时开始 5 场录制（合成直播源）都能开始，没有“已达到同时录制上限”；设置里没有“同时录制数” |
| M4-K34 | 合成 HLS 播放列表媒体序号跳 3 段 → 一条 `lost_segments`（3 段、估算秒数）；FLV 断开 20 秒后重连 → 一条 `reconnect` 与空档秒数；慢速 30 秒 → 一条 `slow`；事件写进录像 `item.json` 的 `live.incidents`，旧清单（无此字段）照常读；全程没有额外站点请求 |
| M4-K35 | 任务侧栏“录制异常（N）”与页面按场列出；录制行显示“⚠ N 处异常”；窄窗顶部分段可达 |
| M4-K36 | 订阅详情默认“内容”，信息流只含该订阅全部账号的已缓存内容（含两个站点的账号），零站点请求；“账号”每行带直播状态，直播中可开始/停止录制；旧 `live` 标签值读成“账号” |
| M4-K37 | 缓存规则“选择订阅…”勾选 3 个订阅应用后，这 3 个订阅的规则变为该规则、取消勾选的回到默认，`subscriptions.json` 只写一次；订阅详情的规则按钮同步显示 |
| M4-K38 | 回归：A 本轮的 `TestDeletingTheLastItemRemovesTheEmptyAuthorFolder`、`TestBilibiliUserPageSubscriptionKeepsALocalAvatar`、`TestTaskStatusLogOnlyLogsChanges`、`TestCrashOutputKeepsThePreviousCrashAndPrunes` 与 F-001～F-011 的测试保持通过 |

### 13.4 不在本轮

- 实时弹幕（仍按 §11.3）。
- 开发版自己联网检查更新（开发版没有发布源；更新页只说明方式并显示本机安装的版本）。
- 录制异常的系统通知（只在任务页显示；需要时再加）。

### 13.5 交付

同第 9 节：门禁、Linux vet、截图（设置“更新”正式版与开发版、任务“录制异常”、订阅详情“内容”“账号”、规则批量选择对话框，各三档宽度）、冒烟、`go run ./tools/package -install`（本机 TRACE Dev 在运行时由工具让它退出、装完再打开）；交付前跑 `go run ./tools/deps` 与 `-audit`；报告追加“B：第二轮反馈的实现”与给 A 的审查 prompt；全部 amend 进同一个阶段提交。

<a id="prompt-feedback-2"></a>
### 13.6 给 Agent B 的交接 Prompt（修订 4）

```text
你是 TRACE 的 Agent B，继续 M4，实施第二轮实机反馈：计划 docs/delivery/stage-plan.md 第 13 节（修订 4）。

先按 AGENTS.md 读入口文档，再读：DEC-051、计划第 13 节、阶段报告 §9、§10（A 已修的 F-010～F-015 与本轮已处理的 ②④⑤⑥）、
界面规范 §3.6、§3.9 与设置、需求 FR-UPD、FR-SET-002、FR-LIVE-002、直播设计、USER-ACTIONS 的 Q-018。

分支 codex/m4-account，在现有的阶段提交上继续（基点与 A 交付 SHA 见报告 §10）；你的实现、测试与文档全部 amend 进
这一个 DCO 提交（git commit --amend -s）。开工时核对一次分支、提交与工作区，保留已有改动。

连续完成 U1、L1、S1、S2、D2 与 M4-K32～K38，不逐包请示：本地实现、合成夹具、测试、截图、打包、冒烟、文档同步都在授权内。
默认值按 Q-018。本轮不新增第三方依赖，不发真实站点请求；录制异常只从录制本身已有的数据推算，不另开连接。

必须保留：M1～M4 已有行为与测试（被 DEC-051 替代的除外，相应测试改写而不是删掉）、现有数据文件结构版本 2
（更新记录用新文件 update-history.json，录制异常用 item.json 的可选字段）、按站点的间隔/额度/退避/熔断、
凭证隔离与隐私哨兵、安静测试（不抢焦点、不碰已安装的应用）、只删登记文件、A 本轮的修复与回归测试。

交付前：go run ./tools/deps、go run ./tools/deps -audit、go run ./tools/check、GOOS=linux go vet ./...、
tools/uishots（补 13.5 列的截图）、tools/smoke 全部通过；go run ./tools/package -install 更新本机 TRACE Dev；
阶段报告追加“B：第二轮反馈的实现”（工作包 → 位置 → 命令与证据 → 状态与限制、not-run、给 A 的审查 prompt），
同步需求、界面规范、数据与直播设计、交付状态与 USER-ACTIONS（H-001 第 31 步）。
未授权：push、PR、合并、标签、发布。最终回复给出交付 SHA、开发版版本号和一个首选下一步。
```

<a id="feedback-4"></a>
## 14. 修订 5：第四轮实机反馈的主页调整（2026-11-16）

用户 2026-11-16 的反馈：A 已修好直播录制的两个缺陷（[阶段报告 §14](stage-report.md#feedback-4)）；主页的两条要求是产品调整（[DEC-053](../decisions.md#dec-053)），由 B 在本阶段内连续实现，按用户对 [Q-019](../../USER-ACTIONS.md#q-019) 的答复。基点是 A 本轮 amend 后的阶段提交（见报告 §14 的交付消息）。

### 14.1 设计要点

- **H1 回到上次阅读的位置**（DEC-053 ①）：
  - `ui-state.json` 新增可选字段 `homePositions: {"all"|"bilibili"|"douyin": {key, pubTs, offsetPx}}`：每个站点筛选下主时间线（不含单订阅视图，单订阅不记位置）可见区最上面那条帖子与它顶边的偏移。滚动停下约 1 秒后保存，离开主页与退出前（`--quit-for-update` 也算）再保存一次；内容没变时不写。旧文件没有这个字段时照常读，结构版本保持 2。现有 `anchor`（看过的最新一条）的含义不变。
  - 每次启动后第一次显示某个站点筛选的主时间线时（启动时的筛选，或之后第一次切到另一个筛选）：以该筛选的位置为游标，读这条及更早的一页（`direction: older`，包含它本身），列表定位到这条并恢复偏移；`feedMoreBefore = true`，滑到顶附近按现有 `loadFeedEdge(true)` 载入更新的内容，当前看到的条目不跳动。这条已被删掉时，用 `pubTs` 定位到不晚于它的最近一条；没有记录时从最新处开始。同一次运行内切到别的页面再回来，保留当时的列表与位置（现有行为）；同一次运行内在筛选之间来回切换，回到每个筛选当时的位置（离开一个筛选前先保存它的位置）。
  - 达到 NFR-PERF-001：5000 条合成数据下 1 秒内回到上次位置。
- **H2 侧栏“订阅”列表**（DEC-053 ②）：
  - 主页侧栏自上而下：“全部动态”、站点筛选，然后直接是订阅列表；**不加任何分节标题**（不写“正在直播”“订阅”“有更新”）。原来单独的“正在直播”分节（`renderLivePanelSection`）与“有更新”都并入这个列表。
  - 每行：头像（现有 `renderAccountAvatar`）加名称，不加状态说明文字。**正在直播**的头像一圈高亮（现有直播圈）；**未读**时头像右下角一个强调蓝色小圆点（约 8px，带一圈背景色描边，样式参考哔哩哔哩动态页的头像条）。排序分三组：正在直播的在最前（组内未读在前，再按最近更新时间），然后未读，然后已读；后两组各按该订阅在当前站点筛选下最新一条已缓存内容的发布时间从新到旧。没有已缓存内容的订阅不列出，正在直播的除外。只列订阅；没有订阅的作者（直接缓存的作品）和没订阅的直播间不进这个列表。点任何一行（包括正在直播的）都打开这个订阅的信息流。
  - 未读的判断：`latest > seen`。`latest` 是该订阅全部关联账号在当前站点筛选下最新的已缓存发布时间；`seen` 是 `homeSeen["sub:<id>"]`，没有这一项时用启动时读到的 `anchor.pubTs` 作为初始值（第一次使用、两者都没有时算已读，新装不会满屏圆点）。在主时间线上往下翻不改变某个订阅的已读状态，只有点开它才算看过。
  - 点击一行：用现有 `openHomeActorFeed` 打开这个订阅的信息流，并写 `homeSeen["sub:<id>"] = max(原值, latest)`；圆点立即消失。**排序在进入主页时算一次并保存为快照**：本次停留在主页期间（含点开、返回“全部动态”、切换站点筛选）只更新圆点与直播圈、不改顺序；新缓存的内容只会让圆点出现，开播或下播只改直播圈，都不挪动行。下次进入主页（从别的页面切回来，或重新启动）重新排序，看过的就移到已读组。
  - 订阅很多时列表要虚拟化（同订阅侧栏 `SubPanelListState` 的做法），重绘不逐帧读媒体库（复用现有 `libraryStatistics` 与按条目缓存）。
  - 窄窗（没有侧栏）时，时间线上方的头像行（现在的 `renderHomeUpdatedAvatarsRow`）改成同样的列表横向展示：圆形头像、下面一行名称、未读圆点，与哔哩哔哩的头像条相同；最前面是“全部动态”。
  - 原来按“有更新”打开单个作者（无订阅）的入口去掉；`homeSeen` 里 `mid:` / 作者键的旧值保留不删。

### 14.2 工作包

| 包 | 内容 | 主要位置 |
| --- | --- | --- |
| H1 回到阅读位置 | 按站点筛选的 `homePositions` 保存与启动后第一次恢复、删除后按 `pubTs` 回退 | `internal/uistate`（可选字段）、`internal/ui/feed.go`、`internal/ui/views.go` 主页、`internal/shell`（退出前保存） |
| H2 订阅列表与圆点 | 侧栏订阅列表（并入正在直播、无分节标题）、未读判断与进入主页时的排序快照、窄窗横向头像条 | `internal/ui/feed.go`（替换 `homeUpdatedActors`）、`internal/ui/shell.go` 主页侧栏、`internal/ui/views.go` |
| D3 文档 | 需求 FR-FEED-001 与 NFR-PERF-001 说明、界面规范 §3.1（主页）与第 66 行附近的“不恢复位置”、数据设计 `ui-state.json`（`homePositions`、`homeSeen` 的新语义）、架构 §主页、订阅设计第 72 行的“有更新”、交付状态、USER-ACTIONS H-001 补一步 | `docs/` |

### 14.3 必测用例（追加）

| 编号 | 用例 |
| --- | --- |
| M4-K39 | “全部”滚到第 40 条、“B 站”滚到第 10 条后“重启”（重建 `App`，`RestartHomeSession`）：两个筛选各自回到自己的那条、偏移相同，“抖音”从最新处开始；往上滑载入更新的条目且当前条目不跳；那条删掉后回到不晚于它的最近一条；单订阅视图不写位置；旧 `ui-state.json` 照常读 |
| M4-K40 | 侧栏“订阅”：3 个未读订阅带圆点排在上面、按最新发布时间排序，已读的在下面无圆点；点开一个未读订阅后圆点消失、位置不变，`homeSeen` 写入；离开主页再回来，它移到已读组的正确位置；新缓存的内容让已读订阅出现圆点但本次不挪位置 |
| M4-K41 | 第一次使用（没有 `anchor` 与 `homeSeen`）不显示任何圆点；只有 `anchor` 时比它新的订阅带圆点；站点筛选为“抖音”时只列有抖音内容的订阅，排序与圆点按抖音内容算 |
| M4-K42 | 正在直播的订阅排最前、头像带直播圈（即使没有已缓存内容），侧栏没有任何分节标题、没有状态说明文字；本次停留中开播不挪位置；1000 个订阅时侧栏滚动不卡顿、重绘不读媒体库；窄窗下时间线上方是横向头像条（“全部动态”在最前、直播圈、未读圆点）；三档宽度截图 |
| M4-K43 | 回归：A 本轮的 `TestRecoveryAcceptsEXTINFDriftAndClearsConvertFailed`、`TestHLSSequenceResetIsNotLostSegments` 与以前各轮的测试保持通过；被 DEC-053 替代的“主页不恢复位置”的测试改写而不是删掉 |

### 14.4 不在本轮

- 图标栏“主页”的未读角标或数字（DEC-053 不要）。
- 按条记已读、把没订阅的作者放进侧栏列表。

### 14.5 交付

同第 9 节：门禁、Linux vet、截图（主页侧栏订阅列表有未读与已读、窄窗头像条、恢复位置后的主页，各三档宽度）、冒烟、`go run ./tools/package -install`；报告追加“B：第四轮反馈的实现”与给 A 的审查 prompt；全部 amend 进同一个阶段提交。

<a id="prompt-feedback-4"></a>
### 14.6 给 Agent B 的交接 Prompt（修订 5）

```text
你是 TRACE 的 Agent B，继续 M4，实施第四轮实机反馈的主页调整：计划 docs/delivery/stage-plan.md 第 14 节（修订 5）。

先按 AGENTS.md 读入口文档，再读：DEC-053（以及被它部分替代的 DEC-040 ④、DEC-033 ①、DEC-037）、计划第 14 节、
阶段报告 §14、界面规范 §3.1、需求 FR-FEED-001 与 NFR-PERF-001、数据设计 ui-state.json 一节、USER-ACTIONS 的 Q-019（用户已答复）。

分支 codex/m4-account，在现有的阶段提交上继续；你的实现、测试与文档全部 amend 进这一个 DCO 提交
（git commit --amend -s）。开工时核对一次分支、提交与工作区，保留已有改动。

连续完成 H1、H2、D3 与 M4-K39～K43，不逐包请示：本地实现、合成夹具、测试、截图、打包、冒烟、文档同步都在授权内。
按用户对 Q-019 的答复实现（站点筛选分别记位置；侧栏无分节标题，正在直播的排最前）。本轮不新增第三方依赖，不发真实站点请求；主页只读本机数据。

必须保留：M1～M4 已有行为与测试（被 DEC-053 替代的除外，相应测试改写而不是删掉）、数据文件结构版本 2
（homePositions 是 ui-state.json 的可选字段）、主页滚动流畅（重绘不逐帧读媒体库、不在界面线程解码原图）、
凭证隔离与隐私哨兵、安静测试（不抢焦点、不碰已安装的应用）、A 本轮的录制修复与回归测试。

交付前：go run ./tools/check、GOOS=linux go vet ./...、tools/uishots（补 14.5 的截图）、tools/smoke 全部通过；
go run ./tools/package -install 更新本机 TRACE Dev（装完它会在托盘打开，确认命令结束后仍在运行）；
阶段报告追加“B：第四轮反馈的实现”（工作包 → 位置 → 命令与证据 → 状态与限制、not-run、给 A 的审查 prompt），
同步需求、界面规范、数据设计、架构、订阅设计、交付状态与 USER-ACTIONS。
未授权：push、PR、合并、标签、发布。最终回复给出交付 SHA、开发版版本号和一个首选下一步。
```
