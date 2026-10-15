# M2 · TRACE-M2-01 实施与审查记录

本文是 M2 [唯一计划](stage-plan.md) 的交付报告：B 写实现、命令与结果、规则状态与真实证据、整理记录；A 写集中审查结论。B 自检通过不写成 A 已通过，A 技术通过也不等于用户产品验收。

M1 的原交付、失败、返工、复审与真实产品链证据已随 PR 合并（`c1552e0`），全文见[固定链接报告](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md)；当前仍有效的结论只在[交付状态](status.md)维护。

<a id="m2-planning"></a>
## 1. A 规划（2026-10-16）

- 计划：TRACE-M2-01 r1；分支 `codex/m2-multi-site-archive`，基点 `main@c1552e048ea608ba64deae820d127bbd5e969083`。
- 依据：[DEC-031～034](../decisions/confirmed-decisions.md#dec-031)；本阶段需求见[需求](../requirements.md)状态列与[映射](requirements-map.json)。
- A 本次只改治理、需求、设计契约、研究、计划与用户事项，没有改生产代码、没有发起真实站点请求、没有 push。站点资料的证据层级见[社交站点研究](../research/social-sites-current.md)：“资料，未实测”。
- 2026-10-16 用户授权 ACT-007 并提供八站 Cookie 与创作者主页。A 做了一次只读登录与目标探测：共 18 次请求、约 0.90 MB、0 字节媒体；八站都可用。Instagram 首个请求 429 后停止，用户更新 Cookie 并提供浏览器记录后，按浏览器路径重试 2 次均 200。逐站结论见[研究 §10](../research/social-sites-current.md#login-probe-2026-10-16)；这是来源诊断，不是规则验证。
- 交接：[A → B 实施 prompt](stage-plan.md#agent-b-prompt)。

<a id="b-m2-delivery"></a>
## 2. B 交付

被审提交是分支 `codex/m2-multi-site-archive` 的唯一阶段提交（基点 `main@c1552e0`；A 第一轮审查后的返工 R2-1～R2-10 已 amend 进同一个提交，未 push）；它自己的 SHA 不能写进它包含的文件，见 B → A 交接 prompt。相对基点约 735 个文件、+113,694 / −37,347 行（不含本节自身的最后一次修改）。§2～§4 记录当前状态；第一轮的交付由 A 审查的结论保留在 [§5](#a-m2-review)，返工与它的对应关系见[§2.7](#m2-rework-map)。

### 2.1 工作包与实现位置

| 包 | 完整结果与位置 |
| --- | --- |
| P0 | 整理清单全部执行，见[§3](#m2-cleanup-record)；文档去掉阶段标签（`requirements-map.json` 改为已实现 / 当前 / 待实现三态，`check_docs.py` 校验状态、实现文件与测试名） |
| P1 R0～R8 | R0 `scripts/test_inventory.py` 与基线（322 个测试、1760 个断言）；R1 `adapters/common/src/runtime.rs` 统一运行时；R2 两站与共享文档模块按能力拆分（适配器文件 ≤600 行）；R3 `crates/domain/src/jobs.rs` 强类型任务载荷；R4 `crates/core/src/store/`（全部 SQL）与 `app/`（每命令族一个路由和用例），`ipc/` 零 SQL；R5 `pipeline/`、`pluginhost/`、`recording/`、`objects/`、`upgrades/` 拆分，`clippy::too_many_lines` 打开，`scripts/check_structure.py`、`check_file_sizes.py`、`lint_lines.py` 守住结构；R6 ts-rs 生成 23 个 TS 类型（`apps/desktop/src/lib/generated`，`check_generated_types.py` 比对）、Tauri 通用 `daemon_call` 与 64→更多条白名单、Prettier、长视图拆分；R7 测试按能力重命名（[§3](#m2-cleanup-record)）；R8 `scripts/stage.py` 统一入口，文档不再让包证据过期 |
| P2 | `crates/domain/src/sites.rs` 八站注册表；`crates/contracts/src/{rules,unified,catalog}.rs`；schema v7 迁移（统一元数据列与子表、规则目录、存放位置、外部副本、资源库迁移、网络设置、请求与交付表）；`subscriptions.auto_download`；统一元数据写入 `pipeline/observe.rs`、`scan_page.rs` |
| P3 | 规则目录 `catalog.rs`、`store/rules.rs`、`app/sites.rs`；命令 `site.catalog/recognize/refresh`；链接识别按字面段特异度排序；“支持的网站”页面零网络 |
| P4 | `network.rs`（直连、系统、手动代理；系统代理为空即直连）、`transport/rules.rs`（域名按站点收窄）、`ConstrainedClient` 代理路径（socks5h、字面 IP 拒绝、逐跳复查）；TUN 虚拟段保持可直连 |
| P5 | `library/`（迁移、校验、一次提交切换、旧位置保留）、`downloads/`（命名、临时目录写入后改名、旁注 JSON、外部副本检查与同步）及对应 `store/`、`app/` |
| P6 | 订阅“自动下载视频”开关、`media_items.fetch_state`、状态“未下载（按订阅设置）”、命令 `archive.entry.sync`、`archive.recheck`、`archive.entry.recheck`；`media_sync_tests` |
| P7 | `profile_preview.rs`：`source.profile.preview/download`、`source.deliveries`、`source.delivery.cancel`；上限 10 页 / 400 条 / 200 选中，预览 30 分钟过期；`source.confirm` 带下载位置 |
| P8 | 六个新站点适配器 `adapters/{weibo,youtube,tiktok,x,instagram,xiaohongshu}`，共享 `adapters/common/src/kit/`（作品、规则构建、内嵌 JSON、网络、统一分发与测试夹具）；Core 侧元数据 POST、按站点请求头白名单、Core 派生防伪头（适配器永远看不到 Cookie） |
| P9 | `media-tools.lock.json`（yt-dlp 2026.08.19、Deno 2.9.7，含摘要与许可）、`scripts/fetch_media_tools.py`、`crates/core/src/media_tools/`；固定参数、清空环境、超时与输出上限、无 Cookie；缺失降级 |
| P10 | `adapters/{bilibili,douyin}/src/rules.rs`（只有 M1 有真实证据的项是 `implemented`）；`adapters/bilibili/src/browser.rs`（LOOP-002 的浏览器参数与独立签名向量）；`crates/core/src/legacy_metadata.rs` 把旧元数据映射到统一字段并后台有界补齐 |
| P11 | 新页面 `SitesView`、`SettingsView`、`ExplorerView`、`profile/`（主页预览勾选）、`DeliveriesPanel`；时间线排序 / 标签 / 内容形式 / 保存状态、创作者页粘贴任一站主页、订阅开关、链接归档八站；约 90 个新前端测试 |
| P12 | `scripts/stage.py`、`evidence.py`、`rule_evidence.py`、`real_site_check.py`、`core_client.py`；包布局与许可台账；资源管理器十万条发布构建测量进入 `bench` |
| P13 | 真实站点按 ACT-007 执行，结果见[§4](#m2-rule-status)；本节余下内容、整理记录与交接 prompt |

### 2.2 反例组位置

| 组 | 主要位置（测试名按能力命名） |
| --- | --- |
| C01 | `test_inventory.py --compare`：基线 322 个测试、1760 个断言对账，每个旧名都有新名或退役理由；旧库迁移、旧备份、旧适配器（无规则声明）、命令名与参数形状的守护全部保留 |
| C02 | `crates/core/src/catalog_tests.rs`、`adapters/*/src/rules.rs` 的状态测试、`tests/e2e/tests/site_adapters.rs`（真实二进制 hello → 目录 → 链接识别，`rule_status_matches_recorded_real_evidence`）、前端 `sites` 规格 |
| C03 | `unified_metadata_tests.rs`、`legacy_metadata_tests.rs`、`timeline_sort_tests.rs`、各适配器 `site_tests.rs` 的字段映射 |
| C04 | `proxy_tests.rs`、`network_tests.rs`、`transport` 测试 |
| C05 / C06 | `crates/core/src/downloads/*_tests`、`library` 测试 |
| C07 / C08 | `media_sync_tests.rs`、`profile_preview_tests.rs`、`delivery_tests.rs` |
| C09 | 六个适配器的 `site_tests.rs` 与固定向量；`adapters/common/src/kit` 的页面解析测试 |
| C10 | `crates/core/src/media_tools/{tests,pipeline_tests}.rs` |
| C11 | 前端 `explorer`、`settings`、`profile`、`timeline` 规格；`explorer` 十万条测量 |
| C12 | `scripts/tests/test_evidence.py`、`test_rule_evidence.py`、`test_real_site_check.py`、`check_package.py` |

### 2.3 命令与结果

所有命令在仓库根执行；最终回执与原始日志在被忽略的 `artifacts/evidence-runs/m2-r3/`（第一个候选包 `-3` 的失败基准在 `m2-r2/`，原因见 F-063），公开的机器摘要是 `docs/evidence/gate/report.json`（由通过的 `stage.py report` 生成，所以这里不做链接）。

| 命令 | 退出码 | 结果 |
| --- | --- | --- |
| `cargo fmt --all -- --check` | 0 | 无差异 |
| `cargo clippy --workspace --all-targets --locked -- -D warnings` | 0 | 无告警（含 `too_many_lines`） |
| `python scripts/stage.py test --run m2-r3`：`cargo build --workspace --locked`，再 `cargo test --workspace --locked` | 0 | 803 个通过、0 失败、0 忽略（含 `tests/e2e` 的真实二进制用例） |
| 同上：`pnpm -C apps/desktop run verify`（vue-tsc、Prettier、Vitest、Vite 构建） | 0 | 19 个文件、139 个用例通过 |
| 同上：`python -m unittest discover -s scripts/tests -v` | 0 | 44 个通过 |
| `python scripts/test_inventory.py --compare artifacts/evidence-runs/r0-baseline/inventory.json --map artifacts/evidence-runs/r7-renames.json --retired artifacts/evidence-runs/r8-retired.json` | 0 | 基线 322 个测试 / 1760 个断言 → 889 / 4648；每个旧测试都有新名或退役理由（7 个退役），断言数无下降 |
| `python scripts/check_structure.py`（含 `check_generated_types.py`、`check_file_sizes.py`、`lint_lines.py`） | 0 | `ipc/` 零 SQL、0 个超规模文件、0 个可断行超长行、生成类型与 Rust 契约一致 |
| `python scripts/check_docs.py` | 0 | 52 份文档、100 条需求、38 条验收、12 条不变量、18 条决定，0 错误 |
| `python scripts/check_publication.py` | 0 | 0 条公开内容问题 |
| `python scripts/rule_evidence.py check` | 0 | 记录与预算一致；`rule_status_matches_recorded_real_evidence` 把八个适配器的声明与记录逐条对账 |
| `git diff --check` | 0 | 无空白错误 |
| `python scripts/stage.py package --run m2-r3`：`package_windows.py` | 0 | `dist/trace-0.1.0-windows-x64-4`，37 个文件、323.4 MiB（339,091,390 字节），含八个适配器与 `tools/`（yt-dlp、Deno 带摘要与许可） |
| 同上：`python scripts/check_package.py dist/trace-0.1.0-windows-x64-4` | 0 | 不联网地检查包成员与清单摘要，运行嵌入式 WebView 烟测、八个适配器 `hello` 与 `doctor`（WebView2、ffprobe、媒体工具就绪，测试主机列表为空） |
| `python scripts/stage.py bench --run m2-r3 --idle 1030 --seconds 3640 --entries 100000` | 0 | 持续时间 3642.0 秒（空闲窗口 910.8 秒）、十万条元数据与五十万条媒体元数据、354 个满载采样点（四路录制 + 两路下载 + 界面）；热 / 冷查询 P95 302 / 238 毫秒；资源管理器热 / 冷 P95 233 / 215 毫秒；空闲 CPU 0.48%、内存 P95 52.5 MiB；四路录制 CPU 2.7%、内存 P95 66.2 MiB；开始录制 P95 2.0 秒；界面关闭 5.5 秒；恢复 3.2 秒；作业错误 0；峰值私有工作集 192 MiB。这次跑次落在机器较慢的时刻（同机用 R1 的包冒烟是 543 毫秒，接受时是 281 毫秒），所以 233 毫秒是慢状态下的数；第一个候选包 `-3` 在同样的慢状态下热 P95 557 毫秒而失败（§2.5） |
| `python scripts/stage.py report --run m2-r3` | 0 | 生成 `docs/evidence/gate/report.json`（自动证据；A 审查与产品验收未做）。第一次生成失败：`requirements-map.json` 还留着一个已随 R2-4 改名的 X 测试名，门禁报 3 条“缺必需用例”；删去该名字（其余新测试名早已在映射里）后重跑通过，失败的回执原样保留在 `artifacts/evidence-runs/m2-r3-report-attempt1/`。`python scripts/stage.py check` 随后通过 |

### 2.4 本阶段发现的缺陷与复验

| 编号 | 发现与根因 | 修复与复验 |
| --- | --- | --- |
| F-037 | 十万条下资源管理器首屏 / 排序在满载发布构建 P95 约 0.6～0.9 秒（>500 毫秒）：列表与计数对每条求“当前版本”视图；同一测量还暴露基准把边录制边增长的总数当作错页 | 排序与筛选只读 `entries`（EXISTS 修订），名称与最近排序走新索引；基准接受 `total >= entries`；冒烟 100k 约 280 / 273 毫秒；按大小排序要汇总已存字节（约 0.6 秒）不纳入目标，记入 LOOP-003；正式数值见 §2.3 |
| F-038 | 包的 GUI 检查失败：`webview_probe.mjs` 调用已不存在的命令 `daemon_status`，并写死 schema 版本 6 | 改用通用 `daemon_call`（`daemon.status`、`upgrade.list`、`archive.timeline`、`source.list`），schema 版本改为整数检查；包检查复跑通过 |
| F-039 | 包检查报 `package tool deno.exe is not the pinned file`：锁文件里是压缩包的摘要，包里是解出的可执行文件 | 锁文件增加 `member_sha256`；`fetch_media_tools.py` 解压后与 `evidence.py` 按它校验，修正测试夹具 |
| F-040 | 检查脚本没传规则（通道）导致 Bilibili 首轮烧预算；每张图片重读作者页 | 脚本显式带预览规则；适配器对作者页 / 页面状态做有界进程内记忆（首轮回执作废，预算照记） |
| F-041 | 预览默认规则取了非视频通道 | 默认规则按通道排序，视频通道优先 |
| F-042 | kit 适配器的订阅检查没有 `recent_days` 截止，扫到全历史 | kit 按订阅范围截止；`Range.includes` 对无日期条目保持保留 |
| F-043 | Bilibili / Douyin 列表项没有内容形式、数量、时长提示，预览与挑选只能猜 | 两站列表带 `EntryHints` |
| F-044 | `account.resolve` / 账号持久化只认 bilibili / douyin，界面却列了八站 | 泛化为任意已注册站点：通用键校验、按站点主页写法匹配、命名空间检查；测试 `every_site_names_its_accounts_and_a_malformed_key_is_refused`；解析使用凭证作用域 `{id}/g{generation}` |
| F-045 | YouTube 视频服务器对单次长连接限速到几乎不动，真实下载超时 | Core `download_media_ranged`：对 googlevideo 主机按 10 MiB `Range` 分段，必须 206 且 `Content-Range` 完全一致；真实视频下载通过（§4） |
| F-046 | 小红书登录探测在页面没有可读状态时只报 schema 变化，分不清登录失效与改版 | 探测返回 `unknown`，附页面形态（字节数、是否含状态名、标题前 24 字）；测试 `the_login_is_proven_by_the_page_state_and_a_silent_page_proves_nothing` |
| F-047 | 小红书内嵌状态里有 JavaScript 专有值（`new Name(...)`、`NaN`、`Infinity`），严格 JSON 解析失败 | kit 的页面解析容忍这些值并按 `null` 读，失败时给出不引用页面内容的解释；测试 `javascript_only_values_inside_a_state_read_as_null` |
| F-048 | 站点改版时 Core 只带失败分类，现场看不到失败的方法与原因 | 可选诊断日志 `TRACE_ADAPTER_FAULT_LOG`（文件名由环境变量给出，每行方法、分类和适配器自己的简短描述（前 480 个字符，可含页面形态，不含响应正文与凭证），超 256 KiB 截半）；Core 自己的拒绝（如“视频详情不完整”）同样记录并带适配器声明的完整度 |
| F-049 | 小红书视频流的分组名不是编码名（真实页面里是四个页面自取的名字），解析只认 `h264/h265/av1`，于是视频笔记只存成摘要 | 用诊断日志读出页面形态后，`media.rs` 改为读所有分组、按条目自述的编码分类、同编码同尺寸去重；测试 `streams_under_names_of_the_pages_own_are_read_by_what_each_entry_says`（`xiaohongshu` 的 `media.rs`）。额度用尽，没有真实复验 |
| F-050 | 每个下载任务各做一次“已登录”探测：一条有 9 张图的笔记多用 9 次请求，把额度烧光 | Core `auth_proofs`：同站点同凭证代次 2 分钟内复用探测结果，获取记录里的验证时间是探测实际发生的时间；撤销即清除；`two_production_auth_probes_require_batch_proof_and_preserve_expired_unknown` 加了复用与撤销断言 |
| 检查脚本缺陷 | ① 账号识别失败后继续请求（X 白白用掉 24 次）；② 没读 Core 因 HTTP 403 停站的状态，把后续的取消当作普通失败（TikTok）；③ 登录探测显示未登录仍继续；④ 单链接选择把一张图的所有尺寸一起选（Core 拒绝“一个资源一个表示”）；⑤ 增量检查依赖账号识别；⑥ 记录时按标签字典序而不是时间合并；⑦ 旧脚本把“有一条失败”的勾选下载算通过 | 全部修复并加测试（`test_real_site_check.py`，18 个）；X 的发现结果加了失败后的负缓存；受影响的回执已标注，旧的错误通过结论改为失败（小红书 r8） |
| F-051～F-053 | A 在审查时直接修的三处（微博、Instagram 的登录判据，真实测试目标不入库）见 [§5.3](#a-m2-fixes) | 本轮没有改动，真实复验见 §4 |
| F-054 | 凭证 Cookie 会发往该站注册的全部接口域名（`weibo.cn`、`t.cn`、`twitter.com`、`t.co`、`xhslink.com` 等） | R2-1：只发往 Cookie 域名；每站一个反例；两站现有守护保留 |
| F-055 | 订阅范围对没有日期的条目一律判为“在范围内”，范围外的作品也会被自动下载 | R2-2：详情给出确切日期后再判断；范围外不下载、不计入；紧凑年龄（“9h ago”）也能提前截止 |
| F-056 | TikTok 的视频文件被站点 CDN 拒绝（HTTP 403） | R2-3：Core 带页面会话 Cookie；**真实站点验证通过**（视频链接与勾选下载）；工具回退只经离线验证 |
| F-057 | `account.resolve` 不再拒绝非有效凭证，会带过期 Cookie 发请求，并接受任意域名的 `profile_url` | R2-5 |
| F-058 | YouTube 真实列表给的是紧凑年龄（“9h ago”“12d ago”），`parse_ago` 只认完整单词，30 条里 0 条有日期 | `parse_ago` 同时认紧凑与完整单位；以真实页面形态做的夹具测试 `a_compact_age_in_a_list_gives_an_approximate_date…`；真实复验：30 条全部有日期 |
| F-059 | 检查脚本：① 每次增量检查各建一个订阅，同一账号同通道的第二个违反唯一约束；② 一个订阅的三条通道各自读列表，每次检查 21～25 次请求，抖音与哔哩哔哩的额度因此烧光；③ 范围是几天的窗口时第一次检查出现一条作品就暂停，第二次检查“改变了集合” | 复用同一个订阅并 `subscription.update`；取消全历史通道；取最新 4 条里的视频、只读首页（每次约 10～14 次请求）；窗口读完再暂停；`test_real_site_check.py` 对应测试 |
| F-060 | X 的列表把转推的原推媒体算作该条的媒体，而保存的作品按设计不带它们：预览显示“视频”，勾选下载却“没有取回内容” | 列表提示与保存内容一致（转推只算文字）；`site_tests.rs` 加断言；真实复验：预览勾选下载与图片推文链接通过 |
| F-061 | 检查脚本把“下载了 0 个文件”的视频 / 图片链接算通过（X 的转推）；账号没有某种作品（没有纯文字笔记、没有图集）时整条链接检查被判“未执行” | 空下载判失败；账号没有的那一种不挡其余种类的结论（`test_a_video_or_picture_link_that_offers_nothing…`、`test_a_kind_of_work_the_account_does_not_have…`）；X 第二轮之前的“通过”是这个缺陷造成的，已被后一轮回执取代 |
| F-062 | Instagram 把目标主页换成通用错误页时，适配器报“页面改版”（`schema_changed`） | 归为“站点不显示该主页”（`source_unavailable`），`web.rs::profile_error_page_in` 读页面数据；真实页面只经 2 次诊断核对形态，新分类只经离线测试验证 |
| F-063 | R2-9 的第一次一小时基准（包 `-3`）失败：资源管理器热 P95 557 毫秒、冷 509 毫秒（目标 ≤ 500 / 1500；冷的 509 在目标内，只有热超标）。同一台机器上用 R1 的包（第一轮基准 281 毫秒）做冒烟测量也得 543 毫秒，查询类的其他指标同样慢约 2 倍，所以是机器状态（这台机器刚重启 2.7 小时，混合架构 CPU 上同一程序不同时刻速度相差约 2 倍，我另测过），不是 R2 的代码。但余量取决于机器：根因是 OFFSET 要走过跳过的每一行，最后一页与深页的开销随文件夹大小线性增长 | `store/explorer.rs::entry_page`：离末尾比离开头近的页从另一端按相反顺序读取再翻转（排序以条目 ID 作最后一级，空值的位置同样翻转）；测试 `a_page_in_the_far_half_is_the_same_rows_as_reading_the_whole_folder_from_the_start` 用 30 条含重复标题 / 日期与空值的库，八种排序 × 两个方向 × 5 种页大小 × 32 个偏移，逐页与从头整表读法对比（故意把空值位置改错会失败，已验证）。这是 R2 清单之外的改动，因为基准是 R2-9 的验收门；失败的第一次跑次与冒烟记录保留在被忽略的 `artifacts/evidence-runs/m2-r2/` 与 `artifacts/smoke-*`；LOOP-010 随之更新 |

### 2.5 未执行与限制

- **真实站点第二轮的结果**（逐规则见 [§4](#m2-rule-status)）：小红书（预览、笔记链接）、TikTok（预览；视频链接下载在真实站点通过，走 Core 带会话 Cookie 这一条）、X（预览；图片推文链接）、YouTube（增量(关)）通过；微博（数据接口答“需要登录”）、Instagram（目标主页被站点换成错误页）被站点挡住，TikTok 增量被限流；抖音、哔哩哔哩、YouTube 的额度用尽。
- **订阅规则：八站没有一条同时满足“账号识别 + 列表 2 页 + 两次增量”，所以八站的订阅规则都是 `partial`**；每条的限制写明了缺哪一项与原因。增量检查从来没有拿到过两次都通过的真实结果：YouTube 只有自动下载关的一次，抖音与哔哩哔哩用旧脚本在额度用尽时中止（新脚本每次约 10～14 次请求，额度已用完），小红书的列表没有日期，每次检查约 33 次详情请求。
- **没有真实复验**：外部解析工具回退（TikTok 真实站点只用到 Core 带会话 Cookie 这一条）；X 的事务 ID 请求头（站点没有拒绝未签名请求，只经固定向量与离线夹具验证）、视频推文、GIF；YouTube Shorts 与社区帖子的订阅；哔哩哔哩视频通道的浏览器参数（LOOP-002）；Instagram 的列表与下载、微博的列表与下载（被站点挡住）。
- **真实跑次用的包**：第二轮用 `dist/trace-0.1.0-windows-x64-2`（R2-1～R2-7 之后打包）。其后又改了：Instagram 的错误页分类、X 转推的列表提示（X 的复验用的是把新 X 适配器换进该包副本的包，其余文件相同）、检查脚本与规则文案；还有资源管理器的深页读法（F-063，与站点无关）。最终包 `dist/trace-0.1.0-windows-x64-4` 含全部改动，没有在真实站点上整体重跑。
- **手动诊断请求**：微博 5 次、Instagram 2 次、YouTube 1 次，都是单次 GET，计入各站额度，页面只保存在本机被忽略的 `.local/`。
- **基准**：最终包 `-4` 的一小时基准通过（§2.3）。第一个候选包 `-3` 的基准失败在资源管理器热 P95 557 毫秒（> 500），机器当时比接受第一轮时慢约 2 倍（R1 的包同机冒烟 543 对 281 毫秒；刚重启 2.7 小时，混合架构 CPU 上同一程序在不同时刻速度相差约 2 倍），所以在 F-063 里改了深页读法并重打包，冒烟 541 → 244 毫秒，正式跑次 233 毫秒。失败的跑次（`m2-r2`）与冒烟记录原样保留。机器变慢时其他查询类指标也慢约 2 倍（热查询 P95 302 对 142 毫秒），仍在目标内；这个余量取决于机器，LOOP-010 记录。
- **人工检查**：原生体验（输入法、缩放、声音、外部播放器）仍是 not-run，ACT-003 已换成本阶段的新包与合成档案（八站内容、下载文件夹、外部副本）；Linux、安装器、签名与发布未验证。
- **第一轮的基准**：正式基准当时跑了四次（持续时间取最后一次采样差 3.4 秒；机器重启后变慢；清理误删包清单引用的构建日志而重打包），保留在被忽略的 `artifacts/evidence-runs/m2-final-bench-attempt1…3/`；第一个问题已由 R2-7 修复，余量问题见 LOOP-010。

<a id="b-to-a-prompt"></a>
### 2.6 B → A 审查 prompt

```text
你是 Agent A（审查）。请复审 TRACE-M2-01 的 B 返工（第二轮交付）。

对象：分支 codex/m2-multi-site-archive 上唯一的阶段提交（基点 main@c1552e048ea608ba64deae820d127bbd5e969083），SHA 以 `git log -1` 为准（B 的最后回复里有一份，文件里不能写自己的 SHA）；没有 push，没有 PR。工作区应当是干净的。

先读：docs/delivery/stage-plan.md §9（你的返工清单）、docs/delivery/stage-report.md §2（B 的交付，R2-1～R2-10 的位置与结果）、§4（逐规则的真实证据与做不到的规则）、USER-ACTIONS.md 的 ACT-007（第二轮之后）与 ACT-003（新包与合成档案）。机器记录：docs/evidence/rule-evidence.json、docs/evidence/gate/report.json。

请重点核对（不要只信 B 的自检）：
1. R2-1～R2-7 的离线反例：每个都有测试名可对到 docs/delivery/requirements-map.json 的 FR-SITE-003/004/006、FR-AUTH-006、NFR-SEC-001 等。Cookie 只发往 Cookie 域名；抖音、哔哩哔哩的原有守护与签名向量没有被改动（SIGNED_UA 是新增常量，向量不变）。
2. 订阅规则口径：八站订阅规则都是 partial。请判断每条限制写的原因是否准确；特别是小红书的“列表无日期，每次检查约 33 次详情请求”——这是 Core 对未定位条目逐条取详情的结果，我没有加“遇到范围外就停止”的启发式，因为列表不保证按日期排序，担心漏更新；请判断这个取舍。
3. 真实检查的诚实性：回执在 .local/site-check/（被忽略），公开记录只有计数与结论。X 第一次的三条“链接下载通过”其实下载了 0 个文件（转推），已被后一轮取代（F-061）；TikTok 的 post.link 记录为“未执行”，因为站点限流后图集 / 音频的检查没跑，视频链接本身通过（见 §4 的说明）；微博、Instagram 另有 7 次手动诊断请求，已计入额度。
4. 真实跑次与最终包的差异（§2.5）：Instagram 错误页分类、X 转推的列表提示、检查脚本和规则文案是在跑次之后改的；X 的复验用了把新适配器换进 -2 包副本的包。请判断这是否可接受，还是要在最终包上补跑（那需要用户给额度）。
5. 未完成项与原因（都在规则限制与 ACT-007 里）：微博需要用户重新复制 Cookie；Instagram 需要用户确认目标；抖音、哔哩哔哩、YouTube 额度用尽；小红书增量、TikTok 增量、X 视频与增量需要更多额度或新目标。我没有自己提高任何额度。
6. ACT-003：新包与合成档案（artifacts/m2-acceptance，八站 16 条内容、下载文件夹、present / missing 的外部副本）与步骤是否足够覆盖资源管理器、设置页、支持的网站、预览勾选，以及中文输入与缩放。预览勾选需要联网，所以人工检查里只验证界面，不发真实请求，请判断。
7. R2 清单之外的一处改动（F-063）：`crates/core/src/store/explorer.rs::entry_page` 让离末尾比离开头近的页从另一端按相反顺序读取再翻转。动机：第一个候选包 `-3` 的一小时基准在机器慢的时刻失败（资源管理器热 P95 557 毫秒），同机用 R1 的包冒烟也是 543 毫秒（接受那次是 281 毫秒），所以不是 R2 的代码，但余量取决于机器；根因是 OFFSET 走过所有跳过的行。请判断：这个范围扩大是否可接受；等价性测试（八种排序 × 两个方向 × 5 种页大小 × 32 个偏移，库里有重复值与空值）是否足够；页与总数是两次读、不在同一快照，这一点与原来相同。

B 没做 / 做不到的清单见 stage-report §2.5。requirements.md 里“本阶段”行的状态按你的约定保留，等你的验收后再改。

审查后：局部问题请你直接修；较大缺口一次性交回给我。收敛后在 USER-ACTIONS 登记 push / PR / 合并授权请求（我没有做任何远端动作）。
```

<a id="m2-rework-map"></a>
### 2.7 返工 R2-1～R2-10 的位置与结果

A 的返工清单在[计划 §9](stage-plan.md#a-review-rework)。

| 项 | 实现与结果 |
| --- | --- |
| R2-1 | 站点清单的 `cookie_hosts`：微博 `weibo.com`、小红书 `xiaohongshu.com`、Instagram `instagram.com`、X `x.com`、TikTok `tiktok.com`，YouTube 空，抖音、哔哩哔哩保持；`transport.rs` 只把凭证 Cookie 发往这些域名，其余接口域名与重定向目标一律不带（`crates/domain/src/sites.rs`、`crates/core/src/transport.rs`） |
| R2-2 | 订阅范围对没有日期的条目三态判断（范围内 / 范围外 / 待详情定位）：`crates/core/src/scan_range.rs`、`pipeline/standing.rs`、`observe.rs`、`scan_page.rs`；范围外的条目不自动下载、不计入发现数；列表的“N 天前”与紧凑的“9h ago”“2w ago”按近似日期（精度标为近似，留一个单位的误差）提前截止（`adapters/common/src/kit/clock.rs::parse_ago`）；无日期的整页扫描有页数上限 |
| R2-3 | `crates/core/src/session_jar.rs`：Core 在内存里记下站点响应设置的会话 Cookie（按凭证作用域，不落盘、不给适配器、撤销即清除），`pluginhost/proxy.rs` 只在站点自己的视频地址（`session_media`）上与凭证 Cookie 一起带上，CDN 域名永不带；仍被拒绝时 `media_tools/fallback.rs` 改用随包的外部解析工具（只收数字 ID 的 TikTok 视频页，与 YouTube 同样的约束） |
| R2-4 | `adapters/x/data/web_client.json`（查询标识、特性开关与令牌随适配器发布）、`catalog.rs`（先用内置数据、被拒绝才从页面脚本发现、负缓存与 TTL）、`transaction.rs`（按公开算法计算事务 ID 请求头，固定向量来自 MIT 许可参考实现在合成夹具上的输出）、`api.rs`（被拒绝时带事务 ID 重试，之后始终带） |
| R2-5 | `crates/core/src/accounts.rs`：凭证状态不是有效时拒绝且零请求；适配器给的 `profile_url` 只接受该站自己的域名 |
| R2-6 | `adapters/common/src/kit/mod.rs::DESKTOP_UA` 改为格式完整的桌面浏览器标识；抖音、哔哩哔哩的签名向量依赖原值，单设 `SIGNED_UA`，向量不变 |
| R2-7 | `fault_log.rs` 的说明改准；`credential.revoke` 清除登录证明有了测试；基准持续时间取工作负载自己的结束时间（`scripts/benchmark_metrics.py`，LOOP-009） |
| R2-8 | 第二轮真实站点检查，用返工后的包 `dist/trace-0.1.0-windows-x64-2`，结果见 [§4](#m2-rule-status) |
| R2-9 | 重打包 `dist/trace-0.1.0-windows-x64-4`（第一个候选包 `-3` 的基准失败，见 F-063，修复后重打包）、重跑一小时基准、重新生成门禁报告；ACT-003 的新包与合成档案见 USER-ACTIONS；`tests/e2e/src/bin/prepare_acceptance.rs` 现在生成八站内容、下载文件夹与外部副本的合成档案 |
| R2-10 | 本节与 §4 记录本轮实现、命令、真实证据；`requirements.md` 的“本阶段”状态仍等 A 验收 |

反例位置：

| 项 | 主要测试 |
| --- | --- |
| R2-1 | `crates/core/src/pluginhost/proxy.rs`、`transport.rs`、`proxy_tests.rs`、`crates/domain/src/sites.rs` 的测试：每站 Cookie 不发往其他注册域，重定向不带 Cookie，两站现有守护不变 |
| R2-2 | `crates/core/src/undated_range_tests.rs`、`scan_range.rs`、`adapters/common/src/kit/clock.rs`、`adapters/youtube/src/entry.rs`：7 天范围面对 100 条无日期的历史只翻有限页、只自动下载范围内的作品；详情日期在范围外的作品不下载；精度入库；紧凑年龄 |
| R2-3 | `crates/core/src/session_media_tests.rs`、`session_jar.rs`、`media_tools/fallback.rs`、`pipeline_tests.rs`：CDN 永不带 Cookie、会话不跨作用域、撤销即清除、被拒绝才回退到工具、工具失败如实报告 |
| R2-4 | `adapters/x/src/{catalog,transaction,site_tests}.rs`：内置标识先用、被拒才发现、负缓存、签名向量与改版页面 |
| R2-5 | `crates/core/src/accounts.rs` 的测试：失效凭证被拒且零请求、其他域名的主页地址不入库 |
| R2-6 | `adapters/common/src/kit/testing.rs`：夹具拒绝不完整的 User-Agent；两站向量照旧通过 |
| R2-7 | `credential.revoke` 清除登录证明、`test_the_duration_is_the_workloads_own_end_not_the_last_five_second_sample` |

<a id="m2-cleanup-record"></a>
## 3. 整理记录

按[阶段整理清单](stage-plan.md#stage-cleanup-list)：

| 类别 | 处理结果 |
| --- | --- |
| `artifacts/` | 删除 642 项、约 78.6 GB（已提炼的跑次、一次性回执、旧门禁与测量原始样本）；保留 ACT-003 在用的 `closeout/`、仍被状态引用的一阶段真实证据目录和本阶段 `evidence-runs/`；阶段末又删 WebView 测试临时目录、中间日志，以及五次基准跑次（第一轮三次，返工的 `m2-r2`、`m2-r3`）和四个冒烟包副本里的合成工作负载（`archive/`、`data/`、`gui/` 等，共约 40 GB），只留各次的 `benchmark.json`、资源采样、日志和回执（含两次失败的基准与两次失败的 `report`）。当前约 53 MB（含新包的构建日志 `package-commands/`，清单引用它们，不能删）|
| `dist/` | 删除 29 个旧包约 6.0 GB 与一阶段后期包 `…-m1-r3-12`；保留 ACT-003 在用的 `trace-0.1.0-windows-x64-closeout-4` 与本阶段最新包 |
| `target/` | `cargo clean`（74.8 GiB）后由本阶段构建重新生成，不入库 |
| `.local/` | 删除 40 个一次性脚本与 25 个一阶段真实跑次约 4.4 GB；`site-secrets/`、`site-check/` 的目标与本阶段回执保留；用户的浏览器记录 `.local/www.instagram.com.har` 未读未删（ACT-007 提醒用户自己删除） |
| 入库文档 | 去掉按轮次追加的叙述与阶段标签；`m1a-runtime-contracts.md` → `windows-runtime-contracts.md`；删除 7 个旧门禁 JSON，只留最近一次的 `docs/evidence/gate/report.json`；`requirements-map.json` 改为三态 |
| 入库脚本 | 删除 `prepare_site_check.py`（并入 `real_site_check.py prepare`）、`real_chain_gate.py`、`real_source_evidence.py`、`stage_evidence.py`（由 `stage.py` / `evidence.py` 取代）及对应测试；`measure_m1.py` → `measure.py` |
| 入库测试 | 194 个测试改名，7 个旧基线测试退役，断言只增不减：基线 322 个测试 / 1760 个断言 → 当前 889 / 4648（净增 567 个测试，退役 7 个）；`test_inventory.py --compare … --map … --retired …` 通过 |
| `.env.local`、Profile、`node_modules`、本地参考 | 未动 |

测试迁移映射：160 个改名只是去掉阶段前缀（`c01_`、`c01_c02_`、`f009_`、`u04_`、`m1_`、`r3_`、`a_f007_`、`a_` 等），其余不变；34 个改动较大的如下（`旧 → 新`）。

| 旧名 | 新名 |
| --- | --- |
| `m1-archive.spec` 的 7 个用例（`c08_…`、`c02_…` 与两个无前缀用例） | 同名去前缀，文件改为 `archive-components.spec` |
| `api.spec::U04 shared Rust/TypeScript timeline sample …` | `api.spec::shared Rust/TypeScript timeline sample …` |
| `common::c02_c06_long_markdown_…`、`douyin::c01_c02_partial_article_…`、`core::c01_c08_public_search_…`、`core::c04_c06_live_auxiliary_…`、`core::c04_c05_same_session_…`、`core::c01_c06_validation_counts_…`、`core::c04_c07_disk_reserve_…`、`contracts::u04_…`、`core::u04_…` | 去掉双前缀 |
| `e2e::a_f007_…`、`a_u02_…`、`a_f008_…`、`f005_f006_f007_…`、`c01_c02_douyin_article_…`、`a_completed_…`、`a_delete_restore_…`、`a_prepared_bytes_…` | 去掉前缀 |
| `test_stage_evidence::test_r2_contract_requirements_and_configuration_are_required_inputs` | `test_evidence::test_product_inputs_exclude_documents_and_scope_comes_from_the_map` |
| `test_stage_evidence::test_rejects_missing_package_failed_short_benchmark_and_stale_docs` | `test_evidence::test_rejects_missing_package_failed_and_short_benchmark`（文档陈旧不再使包证据过期） |
| `test_stage_evidence` 的其余 7 个（`test_r3_…`、`test_rejects_…`、`test_verbose_…`） | `test_evidence` 同名去 `r3_` |

7 个退役测试都在 `test_stage_evidence`（3 个旧阶段的 `realSource` 实录回执用例，由 `rule-evidence.json` 的逐规则记录与 `test_a_new_site_without_a_real_receipt_cannot_be_implemented` 取代）、`test_site_check_preparation`（3 个，并入 `test_real_site_check`，同样的畸形输入、缺凭证、Cookie 不进输出断言）和 `test_governance`（1 个，阶段先后检查随标签移除，后继为 `test_requirement_status_and_scope_are_checked`）。

测试 / 文件级改名：`tests/e2e/tests/` 的 `a_review_probes` → `acquisition_integrity`、`m1_r3_transport` → `adapter_transport`、`m1_production_adapters` → `production_adapters`、`closeout_faults` → `task_faults`、`closeout_rework` → `transfer_integrity`、`m1_r3_rework` → `upgrade_and_restore`、`m1_startup` → `windows_startup`；`tests/e2e/src/bin/m1_*` 去 `m1_` 前缀；前端 `m1-archive.spec.ts` → `archive-components.spec.ts`。

<a id="m2-doc-sync-cleanup"></a>
### 3.1 文档规范同步与阶段内产物整理（2026-10-24）

依据 [DEC-043](../decisions/confirmed-decisions.md#dec-043)，只动文档与忽略目录，不改产品输入（`crates/`、`adapters/`、`apps/`、`tests/`、`scripts/` 等），冻结的 `m2-r7f` / 包 `-19` 不受影响。

| 类别 | 处理结果 |
| --- | --- |
| 入库文档 | 新增 [SECURITY](../../SECURITY.md) 与[生成物约定](../dev-rules/repo-and-environment.md#generated-outputs)；AGENTS 增加生成物的触发与约束；贡献指南改为“完成一次变更 / 环境与命令 / 本地资料与真实站点 / 许可与安全”；治理 9.3、9.6 与质量门禁写明阶段标签机械检查的现状和阶段内的防堆积做法；交付状态、证据索引、文档索引与 USER-ACTIONS 去掉轮次叙述与历史清单；DEC-019 去掉阶段标签写法，新增 DEC-043 与 ACT-009。阶段标签扫描后，长期正本里只剩 LOOP-014 描述待改的代码标签 |
| `artifacts/evidence-runs/` | 9 个跑次（`m2-r4b`、`m2-r4g`、`m2-r4h`、`m2-r5b`、`m2-r5b-bench-failed`、`m2-r6`、`m2-r6b`、`m2-r7e`、`m2-r7f`）删除基准的合成数据 `bench/{archive,data,gui,vault,probe-0,probe-1}` 共 54 个目录、约 66 GB；回执、日志、`report.json` 与基准 JSON 全部保留，删除后 `stage.py check` 通过 |
| `artifacts/` 其他 | 删除上一阶段的真实跑次目录（864 KB，结论已在已合并的报告里）；`closeout/`（上一阶段的验收样本，约 5.8 MB）、`m2-acceptance/`（ACT-003 在用）、`quick-verify/` 与根目录下本阶段的散落日志保留，下一阶段开始时按生成物约定处理。`artifacts/` 从约 67 GB 降到约 1.0 GB |
| `dist/` | 删除上一阶段的 `trace-0.1.0-windows-x64-closeout-4`（190 MB，USER-ACTIONS 已写明不再用于验收）与空目录 `-16`；本阶段的其余 18 个包（约 5.8 GB，报告里有的还写明原样保留）留到本阶段合并后整理。`dist/` 约 6.1 GB |
| `target/`、`.local/`、`.env.local`、Profile | 未动；`target/` 约 116 GB，下一阶段开始时 `cargo clean`。`.local/weibo.com.har` 未读未删，已在 USER-ACTIONS 提醒用户 |

改脚本或代码会让冻结证据过期，所以阶段标签的机械检查、生成工具复用固定位置、代码与测试里的阶段标签排进交付状态的 LOOP-012～014，在下一阶段开始的整理里处理。

<a id="m2-rule-status"></a>
## 4. 规则状态与真实证据

真实检查只按 [ACT-007](../../USER-ACTIONS.md#act-007) 的对象与额度，用生产便携包经 Core 的控制接口执行（脚本 [`real_site_check.py`](../../scripts/real_site_check.py)）：Cookie 只在隔离 Profile 的 DPAPI 保险库里，适配器看不到；原始回执在被忽略的 `.local/site-check/`；机器记录在 [`rule-evidence.json`](../evidence/rule-evidence.json)，下表只有统计和结论。额度由 Core 对每个 Profile 强制（`acquisition_limits`），Core 在任何 HTTP ≥400 或额度用尽后停站；挑战、限流、登录失败、封禁和权限不明立即停止，不重试、不绕过。

规则 `implemented` 的条件（`rule_evidence.py`）：订阅要账号识别、列表 2 页与增量检查（自动下载关与开各一次）通过（A 审查按计划 P13 收紧，见 [§5](#a-m2-review)）；预览要列表 2 页与勾选下载通过；链接要单链接下载通过；记录里不能有失败或中止的检查。海外四站（Instagram、X、YouTube、TikTok）经 `.env.local` 里用户给的本机代理。

表里的“验证日期”是该规则最近一次通过检查的日期；订阅规则在第一轮的账号识别与列表日期之后又补了增量，没有通过的增量留 `partial`。Instagram 的“账号识别失败”是站点把目标主页换成错误页（适配器报 `source_unavailable`），不是登录失效，所以停止原因栏是“无”。

### 4.1 2026-10-19 最终包后的当前状态

下面各站的旧表保留第一、二轮的检查过程；**本节的状态取代旧表的状态列**，规则文案与 `rule-evidence.json` 以本节和 §8.5 为准。所有真实检查经全局手动代理、隔离 Profile 与生产控制接口执行，遇限流、登录失效、签名拒绝或额度用尽即停。

| 站点 | 规则 | 当前状态 | 最终包上的证据 |
| --- | --- | --- | --- |
| 微博 | `weibo.user.posts`、`weibo.user.preview`、`weibo.post.link` | partial | 账号与列表 2 页通过；图片/文字链接通过；视频链接 0 候选；预览 1 项失败；增量未完成 |
| 小红书 | `xiaohongshu.user.notes`、`xiaohongshu.user.preview`、`xiaohongshu.note.link` | partial | 账号通过；签名续页被 HTTP 461 拒绝，按 DEC-041 停止，完整历史未取完 |
| Instagram | `instagram.user.posts`、`instagram.user.preview`、`instagram.post.link` | partial | 目标主页被站点换成错误页（`source_unavailable`），账号识别失败 |
| X | `x.user.posts`、`x.user.preview`、`x.post.link` | 订阅/链接 partial，预览 implemented | 账号、列表 2 页、预览勾选下载、图片/文字链接通过；前两页无原生视频，视频与增量未做 |
| YouTube | 9 条规则 | 全部 partial | 账号通过；列表页无初始数据（`schema_changed`）后停止；旧包通过记录已标注只对应旧包 |
| TikTok | `tiktok.user.posts`、`tiktok.user.preview`、`tiktok.post.link` | partial | 账号通过；列表被限流，停止；增量与链接未执行 |
| 抖音 | `douyin.user.videos` 等 | 视频预览、视频链接、直播链接 implemented；订阅及其余规则 partial | 账号、列表、预览勾选、视频链接通过；直播链接沿用已合并真实证据；增量在额度用尽时停止 |
| 哔哩哔哩 | `bilibili.user.videos`、`bilibili.user.preview_videos` 等 | 视频链接、直播链接 implemented；其余 partial | 账号、列表、勾选下载、视频链接通过；直播链接沿用已合并真实证据；增量在额度用尽时停止 |



| 规则 | 状态 | 验证日期 | 真实检查 |
| --- | --- | --- | --- |
| `youtube.channel.videos` | partial | 2026-10-17 | 账号识别通过；列表 2 页通过；增量(关)通过；增量(开)未执行 |
| `youtube.channel.preview_videos` | implemented | 2026-10-17 | 账号识别通过；勾选下载通过；列表 2 页通过 |
| `youtube.video.link` | implemented | 2026-10-17 | 链接下载通过 |
| `youtube.post.link` | implemented | 2026-10-17 | 链接下载通过 |
| `youtube.channel.shorts`、`youtube.channel.preview_shorts`、`youtube.channel.posts`、`youtube.channel.preview_posts`、`youtube.short.link` | partial | — | 没有真实检查（本阶段只经合成夹具验证） |

**抖音**（本站累计 68 次元数据请求、6,355,287 媒体字节，不含 A 的探测；停止原因：额度用尽）

| 规则 | 状态 | 验证日期 | 真实检查 |
| --- | --- | --- | --- |
| `douyin.user.videos` | partial | 2026-10-15 | 账号识别通过；列表 2 页通过；增量(关)未执行；增量(开)未执行 |
| `douyin.user.preview_videos` | implemented | 2026-10-17 | 勾选下载通过；账号识别通过；列表 2 页通过 |
| `douyin.video.link` | implemented | 2026-10-13 | 链接下载通过 |
| `douyin.live.link` | implemented | 2026-10-13 | 链接下载通过 |
| `douyin.user.images`、`douyin.user.preview_images`、`douyin.user.articles`、`douyin.user.preview_articles`、`douyin.user.dynamics`、`douyin.user.preview_dynamics`、`douyin.user.live` | partial | — | 没有真实检查（本阶段只经合成夹具验证） |

**哔哩哔哩**（本站累计 66 次元数据请求、1,952,645 媒体字节，不含 A 的探测；停止原因：额度用尽）

| 规则 | 状态 | 验证日期 | 真实检查 |
| --- | --- | --- | --- |
| `bilibili.user.videos` | partial | 2026-10-15 | 账号识别通过；列表 2 页通过；增量(关)未执行；增量(开)未执行 |
| `bilibili.user.preview_videos` | partial | — | 账号识别通过；列表 2 页通过 |
| `bilibili.video.link` | implemented | 2026-10-13 | 链接下载通过 |
| `bilibili.live.link` | implemented | 2026-10-13 | 链接下载通过 |
| `bilibili.user.dynamics`、`bilibili.user.preview_dynamics`、`bilibili.user.opus`、`bilibili.user.preview_opus`、`bilibili.user.articles`、`bilibili.user.preview_articles`、`bilibili.user.live` | partial | — | 没有真实检查（本阶段只经合成夹具验证） |

**TikTok**（本站累计 41 次元数据请求、3,289,739 媒体字节，不含 A 的探测；停止原因：站点限流）

| 规则 | 状态 | 验证日期 | 真实检查 |
| --- | --- | --- | --- |
| `tiktok.user.posts` | partial | — | 账号识别通过；列表 2 页通过；增量(关)中止；增量(开)未执行 |
| `tiktok.user.preview` | implemented | 2026-10-18 | 账号识别通过；列表 2 页通过；勾选下载通过 |
| `tiktok.post.link` | partial | — | 链接下载未执行 |

**小红书**（本站累计 71 次元数据请求、37,015,293 媒体字节，不含 A 的探测；停止原因：无）

| 规则 | 状态 | 验证日期 | 真实检查 |
| --- | --- | --- | --- |
| `xiaohongshu.user.notes` | partial | — | 账号识别通过；列表 2 页通过；增量(关)未执行；增量(开)未执行 |
| `xiaohongshu.user.preview` | implemented | 2026-10-18 | 账号识别通过；列表 2 页通过；勾选下载通过 |
| `xiaohongshu.note.link` | implemented | 2026-10-18 | 链接下载通过 |

**X**（本站累计 59 次元数据请求、4,569,857 媒体字节，不含 A 的探测；停止原因：无）

| 规则 | 状态 | 验证日期 | 真实检查 |
| --- | --- | --- | --- |
| `x.user.posts` | partial | — | 账号识别通过；列表 2 页通过；增量(关)未执行；增量(开)未执行 |
| `x.user.preview` | implemented | 2026-10-18 | 账号识别通过；列表 2 页通过；勾选下载通过 |
| `x.post.link` | partial | — | 链接下载通过 |

**Instagram**（本站累计 9 次元数据请求、0 媒体字节，不含 A 的探测；停止原因：无）

| 规则 | 状态 | 验证日期 | 真实检查 |
| --- | --- | --- | --- |
| `instagram.user.posts` | partial | — | 账号识别失败；列表 2 页未执行；增量(关)未执行；增量(开)未执行 |
| `instagram.user.preview` | partial | — | 账号识别失败；列表 2 页未执行；勾选下载未执行 |
| `instagram.post.link` | partial | — | 链接下载未执行 |

**微博**（本站累计 11 次元数据请求、0 媒体字节，不含 A 的探测；停止原因：登录失效）

| 规则 | 状态 | 验证日期 | 真实检查 |
| --- | --- | --- | --- |
| `weibo.user.posts` | partial | — | 账号识别中止；列表 2 页未执行；增量(关)未执行；增量(开)未执行 |
| `weibo.user.preview` | partial | — | 账号识别中止；列表 2 页未执行；勾选下载未执行 |
| `weibo.post.link` | partial | — | 链接下载未执行 |

第二轮（2026-10-18）用返工后的包 `dist/trace-0.1.0-windows-x64-2` 执行，每站先把上一轮的回执作为起点，只补缺的检查；各站结果：

- 通过：小红书（预览勾选下载、笔记链接）、TikTok（预览勾选下载；视频链接也下载成功，但该规则的记录里图集与音频没有执行，所以仍是“未执行”）、X（预览勾选下载图片推文；图片与文字推文链接）、YouTube（增量(关)）。
- 被站点挡住后按规则停止：微博（数据接口仍答“需要登录”）、Instagram（目标主页被换成通用错误页）、TikTok（增量检查时列表答“无数据”，即限流）。
- 额度用尽：抖音、哔哩哔哩、YouTube（增量(开)、Shorts、社区帖子）。
- 额度不够完成：小红书增量（列表没有日期，每次检查约 33 次详情请求，剩余 28 次）；X 增量（剩余 17 次，且检查账号的前两页没有原生视频）。
- 没有任何一站的订阅规则同时通过账号识别、列表 2 页与两次增量，所以八条订阅规则都是 `partial`。

做不到的规则、已尝试的方案与剩余额度（计入手动诊断后）：

| 规则 | 状态与原因 | 已尝试 | 剩余额度 |
| --- | --- | --- | --- |
| 微博 3 条 | 登录探测（分组列表，判据由 A 在第一轮修正）通过，但主页资料与微博列表接口仍答“需要登录”，带与不带 Cookie 结果相同；已按规则停止，列表与下载没有验证。需要用户重新复制 Cookie（ACT-007） | 桌面端接口与移动端接口各一次；5 次手动单次 GET 诊断页面形态（只存本机 `.local/`）；不重试、不绕过 | 67 |
| Instagram 3 条 | 登录探测通过（页面标明已登录），但目标主页被站点换成通用错误页，走代理与直连结果相同；适配器改为报告“站点不显示该主页”（`source_unavailable`，第一轮是“页面改版”），并按规则停止。需要用户确认目标账号 | 浏览器路径（页面 HTML 取令牌、GraphQL 查询）；2 次诊断；直连与代理各一次 | 48 |
| TikTok 订阅 | 账号识别、列表 2 页通过；增量检查时站点对列表答“无数据”（限流），按规则停止。限流不当作“没有更新” | 先预览与链接，增量放在最后；不重试 | 18 |
| TikTok 链接 | 视频链接下载成功（视频文件由 Core 带页面会话 Cookie 取得，不经适配器）；图集与音频作品在站点限流后没有检查，该规则保持 `partial` | R2-3 的两条路径：会话 Cookie（真实通过）、外部解析工具（只经离线验证） | 同上 |
| X 订阅与链接 | 账号识别、列表 2 页与图片推文预览勾选通过；检查账号的前两页没有原生视频，所以视频推文的链接与增量没有做；站点没有拒绝未签名请求，事务 ID 请求头没有在真实站点上用到 | 内置查询标识直接可用，没有触发重新读取；转推只算文字（F-060） | 17 |
| 小红书订阅 | 账号识别、列表 2 页通过（无发布时间，没有续页）；增量检查每次要逐条取详情来定位范围（首屏 32 条约 33 次请求），剩余额度不够。纯文字笔记与 Core 对范围外条目的停止没有验证 | 没有加“遇到范围外就停”的启发式，因为列表不保证按日期排序；续页签名依赖 Cookie 值，适配器按设计看不到，不做 | 28 |
| YouTube 频道视频订阅 | 增量(关)通过；增量(开)额度用尽 | 脚本每次检查约 10～14 次请求（取最新 4 条里的视频，只读首页） | 0 |
| YouTube Shorts / 社区帖子订阅与预览、Shorts 链接 | 没有真实检查（额度） | — | 0 |
| 抖音 / 哔哩哔哩订阅 | 增量检查额度用尽（旧脚本每次检查 21～25 次请求，F-059 已修，新脚本的额度已用完） | — | 0 |
| 哔哩哔哩预览 | 账号识别与列表 2 页通过，勾选下载额度用尽 | — | 0 |
| 抖音 / 哔哩哔哩 / YouTube 的其余通道 | 只经合成夹具验证 | — | — |

每站剩余额度是用户在 ACT-007 授权的额度减去 A 的探测和本机台账；B 没有提高任何额度，“建议增加”只写在 USER-ACTIONS。

<a id="a-m2-review"></a>
## 5. A 集中审查（2026-10-18）

被审：B 第一轮交付（阶段提交 `600b2bb`，未 push）。结论：**未收敛**。A 直接修了局部问题（§5.3），并已 amend 进阶段提交。较大的缺口一次性交回 B，见[计划 §9](stage-plan.md#a-review-rework)。push / PR / 合并的授权请求要等返工复审收敛后再登记。本节是技术审查，不是产品验收。

### 5.1 逐项核对

| 项 | 结论 | 依据 |
| --- | --- | --- |
| C01 重构不变 | 通过 | A 在基点 `c1552e0` 上独立重建测试清单，与 B 的对账一致（322/1760 → 831/4343，逐测试断言数不降）。按改名映射逐条比对断言文字：15 个测试有差异，原因是格式化、SQL 提成常量，或中文字面量在比对时的编码误差。其中两处是有意的行为变化，已写进契约：只读元数据查询可用 POST（[多站契约 §1](../design/multi-site-contracts.md)）；归档根改由可迁移的资源库给出。“已注册平台”的断言从固定三项改为与注册表一致，比原来更严。没有被删除或放宽的不变量 |
| 规则状态 | 不符合计划口径，已降级 | 计划 P13 的订阅检查清单包含两次增量（自动下载关、开各一次），而 `rule_evidence.py` 只要求账号识别和列表 2 页。A 把两次增量加进订阅的必需检查，`youtube.channel.videos`、`douyin.user.videos`、`bilibili.user.videos` 退回 `partial`，原因写在规则限制里。预览和链接规则不受影响 |
| 真实检查的诚实性 | 计数一致；两站的失败原因判断有误 | 本机台账与公开记录逐站一致：A 的 6 次诊断请求另记，作废跑次计入额度，小红书 r8 的勾选下载在公开记录里按失败处理。但微博、Instagram 的“Cookie 未被认可”是探测判据错了（§5.2）。另有三个已入库的测试含用户真实测试目标的账号标识（F-053） |
| `auth_proofs.rs` | 接受 | 复用范围限于同站点、同凭证代次、2 分钟内。验证时间取探测实际发生的时间。原断言保留，并新增了复用断言。缺一个“经 `credential.revoke` 命令清除”的测试（R2-7） |
| `download_media_ranged` | 接受 | 只用于 googlevideo。要求 206 且 Content-Range 完全一致，不一致直接失败，不做拼接 |
| `fault_log.rs` | 接受，说明要改 | 只在设置了环境变量时写入。写入内容包含适配器自述的页面形态，文档里“只写方法与分类”的说法不准确（R2-7） |
| `accounts.rs` 泛化 | 退步（F-057） | `account.resolve` 不再拒绝非有效凭证，会带着过期 Cookie 发请求；适配器给出的 `profile_url` 没有检查域名（R2-5） |
| Cookie 发送范围 | 缺口（F-054） | 凭证 Cookie 会发往该站注册的全部接口域名，包括 `weibo.cn`、`t.cn`、`twitter.com`、`t.co`、`xhslink.com`；应该只发往 Cookie 域名（R2-1） |
| 没有日期的列表 | 缺陷（F-055） | 订阅范围对没有日期的条目一律判为“在范围内”，YouTube、小红书这类列表的范围因此不起作用，范围外的作品也会被自动下载（R2-2）。原 LOOP-004 只写了“请求数偏高”，低估了影响 |
| 请求标识 | 小缺口 | 六个新站点的 User-Agent 缺 `(KHTML, like Gecko)`，不是格式完整的浏览器标识（R2-6） |
| 规则文案 | 通过 | 没有写入账号、作品清单、Cookie 或签名地址；新增的真实目标守护（§5.3）也没有在规则文案里发现问题 |
| USER-ACTIONS | 已改 | 2026-10-18 用户授权每站更多额度，ACT-007 改为新的累计额度；微博、Instagram 不需要重新复制 Cookie。ACT-003 需要本阶段的新包和新样本，覆盖资源管理器、设置、支持的网站、预览勾选（R2-9） |
| 基准余量 LOOP-010 | 接受 | 578 毫秒那一次，同机冒烟也从约 280 毫秒变成 585 毫秒，说明是机器负载造成的；发布的那次 281 毫秒在目标内。R2-9 在安静的机器上重跑 |
| 门禁 | A 补了一处缺口 | e2e 测试启动 `target/debug` 下的适配器程序，`cargo test` 不会重建它们。A 降级规则后，第一次全量测试测的是旧程序，结果失败。`stage.py test` 现在先跑 `cargo build --workspace --locked` |

<a id="a-m2-diagnosis"></a>
### 5.2 真实站点失败的诊断

A 按 ACT-007 的新额度做了 6 次只读请求（微博 2 次、Instagram 4 次，0 字节媒体），并对照用户浏览器记录里的已登录页面结构。回执只在本机 `.local/site-check/`，细节见[社交站点研究 §11](../research/social-sites-current.md#login-probe-2026-10-18)。各站失败的做法参考了本地参考索引里的同类项目（只读，仓库不写项目名）。

| 站点 | 第一轮现象 | 原因 | 处理 |
| --- | --- | --- | --- |
| 微博 | 登录探测判为未登录 | 配置接口对已登录会话也回答访客；移动站是另一个 Cookie 域名。Cookie 有效 | A 已修：探测改用只有登录才有的分组列表（F-051） |
| Instagram | 登录探测判为匿名 | `USER_ID` 和 `ACCOUNT_ID` 是关联的 Facebook 账号，登录与否都是 0。Cookie 有效，与是否走代理无关 | A 已修：改读 `NON_FACEBOOK_USER_ID`，缺失时读 `IG_USER_EIMU`（F-052） |
| TikTok | 视频文件 403 | 视频地址在 `*.tiktok.com` 下，需要生成它的页面会话，只带 Referer 和 User-Agent 不够 | R2-3：Core 按 Cookie 域名代带会话；仍被拒绝时改用随包的外部解析工具 |
| X | 额度在发现查询标识时用完；事务 ID 请求头没有计算 | 查询标识随前端发布变化 | R2-4：已知标识表作为数据随适配器发布，失效时才去发现；按公开算法计算事务 ID |
| 小红书 | 首屏列表没有日期；下载没有复验 | 没有日期的范围缺陷（F-055）。续页签名依赖 Cookie 值，保持 `partial` | R2-2，R2-8 复验 |
| YouTube | 增量检查没有执行 | 同上，没有日期的范围缺陷 | R2-2、R2-8 |
| 抖音、哔哩哔哩 | 增量检查没有执行（额度用完） | — | R2-8，用新额度 |

<a id="a-m2-fixes"></a>
### 5.3 A 的直接修复

| 编号 | 修复 | 位置 |
| --- | --- | --- |
| F-051 | 微博登录探测改用分组列表：`ok: 1` 且有分组算已登录，`ok: -100` 算访客，没有回答算未知，429 报错；不再访问两个配置接口 | `adapters/weibo/src/account.rs`、`api.rs`、`site_tests.rs` |
| F-052 | Instagram 读取当前用户时只在 `CurrentUserInitialData` 块内读 Instagram 账号 ID：0 算匿名，其他数字算用户，缺失算未知；夹具改成真实的已登录形态 | `adapters/instagram/src/web.rs`、`fixtures/*.html` |
| F-053 | 三个测试里的真实测试目标标识改成合成值。`check_publication.py` 新增 `private-target` 守护：从本机被忽略的 `.local/site-check/*targets*.json` 读取用户的真实目标，工作区或暂存区出现时即失败，且从不打印这些值；附回归测试 | `adapters/x/src/link.rs`、`crates/core/src/accounts.rs`、`tests/e2e/tests/site_adapters.rs`、`scripts/check_publication.py`、`scripts/tests/test_governance.py` |
| 规则口径 | 订阅的必需检查加上两次增量；三条订阅规则降级，限制写明原因 | 三站 `rules.rs`、`scripts/rule_evidence.py`、`scripts/tests/test_evidence.py`、`docs/evidence/rule-evidence.json` |
| 门禁 | `stage.py test` 先构建工作区程序，再跑 Rust 测试 | `scripts/stage.py` |
| 契约与研究 | 只读元数据 POST、Cookie 域名、没有日期的列表、TikTok 媒体、登录探测判据 | [多站契约](../design/multi-site-contracts.md) §1、§3.4、§5.3、§9、§10、§11，[社交站点研究 §11](../research/social-sites-current.md#login-probe-2026-10-18) |

验证（2026-10-18，A 的工作区）：

| 命令 | 结果 |
| --- | --- |
| `cargo build --workspace --locked` 后 `cargo test --workspace --locked --no-fail-fast` | 退出 0；750 通过，0 失败 |
| `cargo fmt --all -- --check`；`cargo clippy --workspace --all-targets --locked -- -D warnings` | 退出 0 |
| `python -m unittest discover -s scripts/tests` | 41 通过 |
| `python scripts/rule_evidence.py check`、`python scripts/check_structure.py`、`python scripts/check_docs.py`、`python scripts/check_publication.py`、`git diff --check` | 全部通过 |

前端套件没有重跑：A 没有改前端。产品输入变了，`docs/evidence/gate/report.json` 和正式包都已过期，由 R2-9 重新生成；在那之前，门禁报告不代表当前代码。

<a id="a-m2-rereview"></a>
## 6. A 复审 B 的返工（2026-10-18）

被审：分支 `codex/m2-multi-site-archive` 上 B 第二轮的阶段提交 `7ff2528`（基点 `main@c1552e0`，未 push），对照 A 第一次审查后的提交 `72988ab` 逐项看差异。结论：**R2-1～R2-7 的离线返工与 F-063 通过；整体仍未收敛。** 原因有三：A 第一次审查对微博的诊断是错的（F-064，A 已修）；小红书增量没做的原因写得不准，检查脚本对没有日期的列表挑不出对象（F-065）；用户 2026-10-18 把 ACT-007 每站额度翻倍，额度用尽的检查现在可以做，并且现有真实证据只覆盖包 `-2`，不是最终包（F-066）。后两项交回 B 第三轮，见[计划 §10](stage-plan.md#a-rereview-rework)。本节是技术审查，不是产品验收；`requirements.md` 的“本阶段”状态继续保留。

### 6.1 逐项核对

| 项 | 结论 | 依据 |
| --- | --- | --- |
| 1. R2-1 Cookie 域名 | 通过 | 站点表 `cookie_hosts` 与计划一致；`build_headers` 对非 Cookie 域名带 Cookie 直接拒绝，`pluginhost/proxy.rs` 把同站其他接口域名改为不带 Cookie 请求、媒体与外站拒绝；跨源重定向去掉 Cookie。反例 `cookies_go_only_to_the_cookie_domain_of_the_site_whatever_the_route`、`the_credential_goes_to_the_cookie_domain_only_and_the_secondary_domains_are_asked_without_it`、`a_cookie_goes_only_to_the_domain_it_was_copied_from`，前两个在映射的 FR-SITE-006 下。抖音、哔哩哔哩的 `cookie_hosts` 等于原 API 域名；两站 `UA` 改为引用 `SIGNED_UA`，字符串与原值逐字相同，签名向量文件未改 |
| 1. R2-2 无日期列表 | 通过 | `scan_range::place` 三态（含近似日期的误差），`scan_page` 跳过详情已判为范围外的条目，`observe` 对范围外条目不排下载、对详情也定不了日期的条目不自动下载视频，`scan.rs` 对全无日期的页设 5 页上限并在覆盖记录里写明；`undated_range_tests.rs` 6 个用例在 FR-SUB 与 FR-SITE-003 下 |
| 1. R2-3 TikTok 视频文件 | 通过 | 会话 Cookie 只在内存、按凭证作用域、有数量与长度上限、撤销即清；只从 Cookie 域名的响应记、只在 `session_media` 站点的视频地址上带；外部工具回退只经离线验证（报告已写明） |
| 1. R2-4 X 列表 | 通过（离线） | 查询标识与特性开关作为数据随适配器发布，失效才发现，有负缓存；事务 ID 只经固定向量验证，真实站点从未要求 |
| 1. R2-5 账号解析 | 通过 | `open_credential` 对非有效凭证在任何请求前拒绝，测试断言代理计数为 0 且适配器未启动；`own_profile_url` 只收本站域名的 https 地址 |
| 1. R2-6、R2-7 | 通过 | 六站 `DESKTOP_UA` 完整；故障日志说明已改准；`credential.revoke` 清除登录证明的测试在 `archive_catalog_tests.rs`；基准持续时间改为工作负载结束时间 |
| 2. 订阅规则口径 | 状态正确，两处原因不准（A 已改文案） | 八站订阅都是 `partial`，与回执一致。小红书：回执写的是“列表里没有带日期的视频可选”，即检查脚本按列表日期挑增量对象，对没有日期的列表根本挑不出，额度不是唯一原因（F-065）。YouTube 一条限制说“增量检查没有真实结果”，与同规则“自动下载关通过”矛盾。取舍：**同意不加“遇到范围外就停止”的启发式。** 小红书首屏有置顶、列表不保证按日期排序，提前停止会漏掉新笔记；代价有界：只有订阅的第一次检查要对首屏约 32 条逐条取详情，之后已定位的条目不再取详情，范围外的不再观察，后续检查只对新条目取详情。所以“每次检查约 33 次”应是“第一次检查约 33 次” |
| 3. 真实检查的诚实性 | 计数与结论一致，一处记录缺失 | 逐站对照本机回执与 `rule-evidence.json`：X 第一轮三条 0 文件的“通过”已被 `w4` 回执取代，公开记录里 `x.post.link` 的通过来自图片推文（选 1 个文件）；TikTok 链接记录取最差结果“未执行”，视频链接本身通过，说明写在规则限制里，偏保守，可以接受。额度台账与公开记录一致（微博 80 − 探测 2 − 台账 11 = 67）。但 B 的 5 次微博手动诊断在 `.local/` 里没有任何回执，只有台账计数（F-067） |
| 4. 微博 | B 的结论对，A 第一次的诊断错了（F-064，A 已修） | A 按 ACT-007 做了 4 次只读请求（[研究 §13](../research/social-sites-current.md#weibo-recheck-2026-10-18)）：不带 Cookie 的分组列表也答 `ok: 1` 并给出分组，字节数与带 Cookie 时相同；主页资料换请求头也答 `-100`；打开主页被转到登录页，按规则停止。Cookie 从 10-17 起就不是登录状态，B 第一轮配置接口给出的“访客”本来是对的。“需要用户重新复制 Cookie”准确；剩余额度足够，不需要再诊断 |
| 5. 真实跑次与最终包 | 不再可接受，交回 B（F-066） | 包 `-2`、X 混合包与最终包 `-4` 的八个适配器和后台程序逐字节都不同。额度用尽时“按 `-2` 记录、写明差异”可以接受；现在额度翻倍，剩余检查要在最终包上跑，已通过的规则在最终包上补一次账号识别与第一页列表的冒烟 |
| 6. 未完成项 | 原因准确（小红书除外，见第 2 项）；额度已翻倍 | 微博、Instagram 是用户事项，记入 USER-ACTIONS 新的[站点规则暂时做不到的情况](../../USER-ACTIONS.md#site-blockers)（S-001、S-002，另记小红书续页 S-003）；其余在第三轮做。B 没有自行提高额度 |
| 7. ACT-003 | 基本足够，A 补了步骤 | 包 `-4` 的 `SHA256SUMS.txt` 摘要与 ACT-003 一致；合成档案 16 条、8 站、下载文件夹里视频与旁注、封面副本记为缺失，与步骤相符。同意预览勾选只验界面、不发真实请求（包里没有测试端点，真实预览由 ACT-007 覆盖）。A 补了：资源管理器、支持的网站、主页勾选三页的中文输入法与缩放检查；按 Q-008 在合成档案里保存一次全局手动代理再改回（只写设置，不发请求）；备份历史里已有生成器做的一条备份。第三轮重打包后 ACT-003 换到最终包 |
| 8. F-063 | 接受 | 排序以条目 ID 收尾，是全序；从末端反向读取再翻转，非空值反向、空值在反向时排前，翻转后与正向完全相同；`lower(r.title)` 这类表达式产生的空值在 SQLite 里升序在前、降序在后，同样能正确翻转。等价性测试覆盖重复值、空值、缺元数据行、八种排序、两个方向、页大小 1/2/5/7/30 与 0～31 的偏移（含越界），足够。“页与总数两次读”：数据库是单连接的执行者，同一个读闭包里插不进写入，实际上是同一状态。范围扩大可以接受：基准是 R2-9 的验收门，根因是 OFFSET 线性走过跳过的行，改动行为等价、有测试 |
| 9. 门禁报告的失败 | 属实 | `artifacts/evidence-runs/m2-r3-report-attempt1/report.json` 状态 failed，错误正是旧 X 测试名引起的 3 条；改名后重跑通过 |

<a id="a-m2-rereview-findings"></a>
### 6.2 新发现与处理

| 编号 | 发现 | 处理 |
| --- | --- | --- |
| F-064 | A 第一次审查把微博登录探测改成分组列表（F-051），但分组列表对访客也答 `ok: 1` 并给出分组，探测会把访客当成已登录；“Cookie 有效”的结论也错了。另外不带登录时主页资料与列表都答 `-100`，站点声明的“登录可选”不准确 | A 已修：探测改回桌面端配置接口，`ok: 1` 且答出当前用户（用户 ID 或 `login: true`）才算已登录，`-100` 或 `login: false` 是访客，其余未知；测试加入“访客的分组列表与移动站都不被询问”的反例；站点表与三条规则改为“需要登录”，限制改写；研究 §11 更正、新增 §13，多站契约 §11 与[站点规则](../dev-rules/site-adapters.md)要求每个登录判据都有不带 Cookie 的对照。“已登录”一侧的形态要等用户的新 Cookie（S-001） |
| F-065 | 小红书增量没做的原因写成“额度不够”，实际是检查脚本只从带日期的列表条目里挑增量对象，对没有日期的列表挑不出；YouTube 一条限制前后矛盾 | A 已改规则文案、机器记录与研究 §12；脚本的无日期增量检查交回 B（R3-3） |
| F-066 | 公开的真实证据来自包 `-2` 与 X 混合包，与最终包逐字节不同 | 交回 B：第三轮在最终包上做（R3-1） |
| F-067 | B 的 5 次微博手动诊断请求在本机没有回执，只在台账里计数 | 交回 B：以后每次手动诊断都在 `.local/site-check/` 留只含形态与计数的回执（R3-1） |

### 6.3 A 的直接修改

| 范围 | 修改 |
| --- | --- |
| 微博（F-064） | `adapters/weibo/src/account.rs`、`site_tests.rs`、`rules.rs`；`crates/domain/src/sites.rs` 微博登录改为“需要登录” |
| 规则文案（F-065） | `adapters/xiaohongshu/src/rules.rs`、`adapters/youtube/src/rules.rs`；`docs/evidence/rule-evidence.json` 里五条规则的说明按新声明更新，微博的请求计数加上 A 的 4 次诊断 |
| 用户决定与授权 | ACT-007 额度翻倍（机器可读一行与说明表）；Q-008、Q-010 的答复记为 [DEC-035](../decisions/confirmed-decisions.md#dec-035)、[DEC-036](../decisions/confirmed-decisions.md#dec-036)；“站点规则做不到时统一记录”记为 [DEC-037](../decisions/confirmed-decisions.md#dec-037)，在 USER-ACTIONS 新增 S 编号的一节；额度翻倍记为 [DEC-038](../decisions/confirmed-decisions.md#dec-038) |
| ACT-003 | 中文输入法与缩放补到资源管理器、支持的网站、主页勾选；Q-008 的代理设置步骤；已有备份的说明 |
| 研究与契约 | [研究 §11～§13](../research/social-sites-current.md#weibo-recheck-2026-10-18)、[多站契约 §11](../design/multi-site-contracts.md#credentials)、[站点规则](../dev-rules/site-adapters.md)、[开发流程](../dev-rules/development-workflow.md#agent-user)、[状态](status.md)、[事项总表](../decisions/open-questions.md) |

真实请求：微博 4 次只读 GET、0 字节媒体（直连），回执在本机 `.local/site-check/m2-a-diag-20261018-3/`，已记入台账。A 没有 push，没有改前端。

### 6.4 验证

见下表；适配器源码变了，最终包 `-4`、一小时基准与 `docs/evidence/gate/report.json` 都已过期，由 B 后续轮次（第四轮）重新生成，A 没有重打包、没有重跑基准。

| 命令 | 结果 |
| --- | --- |
| `cargo fmt --all -- --check` | 通过（A 改的微博规则先按格式要求重排） |
| `cargo clippy --workspace --all-targets --locked -- -D warnings` | 通过 |
| `cargo build --workspace --locked`、`cargo test --workspace --locked --no-fail-fast` | 803 通过，0 失败（44 个测试程序）；格式重排后微博适配器 24 个测试再跑一次，全部通过 |
| `python -m unittest discover -s scripts/tests` | 44 通过 |
| `python scripts/rule_evidence.py check` | 一致（微博、小红书、YouTube 的说明按新声明刷新；微博请求数含 A 的 4 次诊断） |
| `python scripts/check_docs.py`、`python scripts/check_publication.py`、`python scripts/check_structure.py`、`git diff --check` | 全部通过；公开内容 0 条问题 |
| 前端检查 | 没有重跑：A 没有改前端 |
| 最终包、一小时基准、`python scripts/stage.py report` | 没有重跑，见本节开头 |

<a id="a-m2-round-3"></a>
## 7. A 第三轮：微博判据、用户答复与计划 r2（2026-10-18）

被审的是阶段分支上 amend 前的唯一提交 `fb01662` 加 A 的工作区改动；B 第二轮交付的离线返工继续保持通过。结论：**微博登录判据仍未收敛，A 自己上一轮的修复有错（F-068），已在本轮改正并有真实已登录/未登录对照；用户对 Q-009、Q-012、S-001、S-003 的答复已落入 DEC-039～041 与计划 r2。** 九项管理功能与两项工具/签名改动尚未实现，不能写成已交付；剩余真实检查要在第四轮最终包上做。本节是技术审查与计划更新，不是产品验收。

### 7.1 新发现

| 编号 | 发现 | 处理 |
| --- | --- | --- |
| F-068 | A 在 F-064 中把微博探测改成“只读桌面端配置接口，`ok: 1` 且答出用户 ID 或 `login: true` 才算已登录”。2026-10-18 用用户重新复制的新 Cookie 做真实对照时，配置接口答 `ok: 1`，但 `data` 里只有 `ab_test`，没有用户 ID，也没有 `login`；这个判据会把真实已登录会话判成“未知”，所有微博规则都会拒绝运行。F-064 的“不能把访客当已登录”方向对，但只靠配置接口不能证明登录 | A 已改为两步：配置接口 `-100`/401 为访客；`ok: 1` 后再读 `weibo.com/` 首页，`window.$CONFIG` 的 `user.idstr`/`user.id` 为非零数字且（有 `uid` 时）与 `uid` 相等才算已登录；首页被转到 `passport.weibo.com` 为访客；静默、缺字段或 `uid` 不一致为未知；429 为错误。两步都带同一凭证，测试加入不带 Cookie 的对照与“配置 `ok: 1` 但无用户”的反例；分组列表与 `m.weibo.cn` 不再询问。规则文案按新判据更新 |

### 7.2 真实核对与用户答复

- **新微博 Cookie（S-001）：** 用户重新复制后，A 经 ACT-007 经用户代理做 4 次只读请求、0 字节媒体（回执 `.local/site-check/m2-a-diag-20261018-4/`，细节见[研究 §13](../research/social-sites-current.md#weibo-recheck-2026-10-18-new-cookie)）：带 Cookie 的配置接口 `ok: 1` 无用户，首页 `window.$CONFIG` 写出当前用户；不带 Cookie 的配置 `-100`，首页 302 到 `passport.weibo.com`。新 Cookie 确认已登录，S-001 关闭；微博规则仍要等第四轮最终包上的列表/下载/增量检查。
- **Q-009：** 用户要求继续随包锁定 yt-dlp 与 Deno，并增加设置页手动检查更新。按 [DEC-039](../decisions/confirmed-decisions.md#dec-039) 落入计划 §12 R4-5：只由用户点击触发，先用随包公钥核对 `SHA2-256SUMS.sig`，再核对摘要，并排安装、启动检查、空闲切换、保留回退；不自动更新、不用 `-U`。
- **Q-012：** 用户要求九项管理能力本阶段也做。按 [DEC-040](../decisions/confirmed-decisions.md#dec-040) 落入计划 §11；九项需求状态改为“本阶段”，`requirements-map.json` 标为 `current`，工作包 M1～M9、反例组 C13～C17。交付并通过 A 审查前不改为“已实现”。
- **S-003：** 用户要求小红书订阅取到完整历史。按 [DEC-041](../decisions/confirmed-decisions.md#dec-041) 落入计划 §12 R4-6：Core 只把设备标识 `a1` 交给适配器，登录 Cookie 不出 Core；签名用固定向量测试，站点拒绝（406/461 等）即停止并把历史标为“未取完”，不换参数重试。
- **S-002：** Instagram 目标仍被站点换成通用错误页，用户尚未确认；保持 `partial`，第四轮只在用户回复后做。

### 7.3 A 的直接修改

| 范围 | 修改 |
| --- | --- |
| 微博判据（F-068） | `adapters/weibo/src/account.rs` 两步探测；`api.rs` 首页请求头；`site_tests.rs` 已登录/访客/未知/无 Cookie 对照；`rules.rs` 限制文案 |
| 有效决定 | 新增 [DEC-039～041](../decisions/confirmed-decisions.md#dec-039)，修改 DEC-031 对九项能力的顺延 |
| 计划与需求 | [计划 r2](stage-plan.md#m2-management-scope)（§11 M1～M9、§12 R4-1～R4-10、C13～C17）；`requirements.md` 九项改“本阶段”；`requirements-map.json` 九项改 `current`、工作包/反例组/测试计划、全部计划 ID 改 r2；路线删去“管理与插件演进”的后继范围 |
| 用户事项 | S-001、S-003、Q-009、Q-012 移到完成区并保留锚点；ACT-007 更新到第四轮与微博新 Cookie；ACT-003 标注暂缓到最终包；Q-011 仍 open |
| 设计、研究、契约 | [设计 §5.2/§5.3/§7.1](../design.md#domain) 的资料历史、待确认关联、资源比较；[多站契约 §10/§11](../design/multi-site-contracts.md#youtube-media) 已在上一轮写入；[研究 §13](../research/social-sites-current.md#weibo-recheck-2026-10-18-new-cookie) 补第三次核对；[开源候选](../research/open-source-candidates.md#tool-update-2026-10-18) 记录手动更新 |
| 状态与证据 | `status.md` 改为 r2、B 第四轮；`open-questions.md` OSS-012/013 更新；`rule-evidence.json` 计划改 r2、微博请求数 21、更新小红书签名说明；文档登记表 basis 改 r2/DEC-039～041 |

### 7.4 验证

| 命令 | 结果 |
| --- | --- |
| `cargo fmt --all -- --check` | 通过 |
| `cargo clippy --workspace --all-targets --locked -- -D warnings` | 通过 |
| `cargo build --workspace --locked`、`cargo test --workspace --locked --no-fail-fast` | 通过：803 个测试、0 失败、0 忽略；38 个测试程序，另有 6 个无用例的文档测试程序。用 `cargo test --workspace --locked -- --list` 核对清单数量 |
| `python -m unittest discover -s scripts/tests` | 44 通过，0 失败；UTF-8 环境复跑同样通过 |
| `python scripts/check_docs.py` | 通过：52 个文档、800 个本地链接、100 条需求、38 条验收、12 条不变量、18 条决定、0 错误 |
| `python scripts/check_publication.py`、`python scripts/check_structure.py`、`git diff --check` | 通过；公开内容 0 条问题 |
| `python scripts/rule_evidence.py check` | 通过：规则声明、计划 r2、真实证据与累计额度一致 |
| 前端套件 | 没有重跑：A 没有改前端，只改了计划/需求/映射里的前端文件引用 |

A 没有改产品前端；没有重打包、没有重跑一小时基准，`dist/trace-0.1.0-windows-x64-4/`、`docs/evidence/gate/report.json` 与旧真实回执都只对应上一版源码，不能用于第四轮结论。新微博 Cookie 的 4 次请求已计入 ACT-007（微博已用 21）；0 字节媒体，没有 push。

### 7.5 本地收尾与交接

本轮复核并保留已有的 19 个文件改动，补齐 §7.4 的实际结果，并修正第四轮 prompt 对 F-068 所在报告节号的引用。没有删除或移动文件，没有增减既有测试或弱化断言，没有新增真实站点请求。未跟踪的个人配置、凭证、Profile、构建缓存、旧包和本地回执保留原状；最终包、基准与阶段整理仍由 R4-8/R4-10 完成。

首次结构/脚本检查虽然退出 0，但 Python 子进程输出读取线程出现 Windows 默认 GBK 解码异常；用进程环境 `PYTHONUTF8=1` 复跑结构检查及全部 44 个脚本测试后通过，且未再出现该异常。不改脚本或用忽略解码错误的方式隐藏输出。

保存前用 `git ls-remote --heads origin` 核实远端只有 `main@c1552e048ea608ba64deae820d127bbd5e969083`，阶段提交尚未共享。全部本轮修改按明确路径暂存，amend 进同一个带 DCO 签名的阶段提交；最终 SHA 从 Git 读取，不为文档自引用重复改提交。没有 push、PR、合并或发布。

**A 本轮技术收尾完成，整个阶段仍为 `rework-required`。** 首选下一步是由用户把[计划 §12.1](stage-plan.md#agent-b-round-4-prompt)交给 B，连续完成 R4-1～R4-10。九项新增管理需求、工具更新、小红书完整历史与最终包验证尚待 B 交付；八站订阅规则仍为 `partial`。用户当前事项只见 USER-ACTIONS：无当前用户阻塞；S-002 只影响 Instagram 的真实检查，Q-011 按当前默认推进，ACT-003 为 `not-run` 且不阻碍后续技术审查与远端授权。

<a id="b-m2-round-4"></a>
## 8. B 第四轮交付（2026-10-18）

被审对象是阶段分支上的唯一提交；B 的实现、本轮自检、下文记录与最终包都 amend 进它。本节是 B 自检，不是 A 审查或产品验收；真实站点只按 [ACT-007](../../USER-ACTIONS.md#act-007) 的对象与额度执行，结果逐站写在 §4 和 §8.5。共同基础、命令与未执行边界如下。

### 8.1 共同 schema v8、契约与迁移

| 范围 | 实现与证据 |
| --- | --- |
| schema v8 | `crates/core/src/db/migration_v8.rs` 一次建立 `creator_redirects`、`creator_operations`、`identity_candidates`、`live_replay_links`、`import_batches`、`import_items`、`resource_signatures`、`resource_comparisons`、`work_groups`、`work_group_members`、`work_batch_previews`、`plugin_releases`、`management_operations`，并给 `account_observations` 增加 `profile_json`；`schema.rs` 注册 v8，`SCHEMA_VERSION=8` |
| 迁移反例 | `migration_v8_tests::schema_seven_databases_gain_the_management_tables_without_rewriting_content`（旧库保留条目、修订、对象与文件定位）、`an_interrupted_schema_eight_migration_leaves_schema_seven_usable_and_resumable`（中途失败仍是 v7、可重试）、`schema_eight_enforces_its_invariants_and_older_programs_refuse_it`（外键、唯一约束、旧程序拒写新库） |
| 契约 | `crates/contracts/src/control/commands.rs` 增加 46 个管理与工具命令并加入 `UI_COMMANDS`（91→137，`ui_command_tests` 校验无重复、危险命令仍被排除）；`crates/contracts/src/capability.rs` 四类能力契约；`crates/contracts/src/management.rs` 导出 `ManagementOperationDto`、`IdentityCandidateDto`、`ProfileObservationDto`、`ImportBatchDto`、`ImportItemDto`、`PluginReleaseDto`、`CapabilityContractDto` |
| TS | `cargo test -p trace-contracts` 生成 `apps/desktop/src/lib/generated/` 的 7 个新类型；`check_generated_types.py` 通过；`api.ts` 增加对应包装，所有命令字面量都在 `UI_COMMANDS` 内 |
| 审计/事件 | `store::management::record` 为合并、拆分、重关联、候选确认/拒绝、回放关联/拒绝、导入开始/归入、比较决定、批量、插件安装/切换/回退/移除写一行；管理写命令发 `creator.changed`、`account.bound`、`import.changed`、`work.changed`、`plugin.changed` 等 outbox 事件 |

### 8.2 M1～M9 实现、测试与界面路径

| 包 | 需求 | 实现 | 主要测试 | 界面 |
| --- | --- | --- | --- | --- |
| M1 | FR-ID-003 | `crates/core/src/store/identity.rs`（merge/split/relink/operations/redirect，事务内只改 `account_bindings` 与 `creators.status`）；`app/identity.rs`；`app/mod.rs::resolve_creator` | `creator_merge_keeps_archive_identity`、`creator_merge_rejects_cycles`、`creator_split_moves_selected_accounts_only`、`creator_relink_checks_expected_source`、`creator_operations_are_paginated`、`timeline_follows_creator_redirect` | `/management?tab=identity`，创作者页入口「合并、拆分与身份候选」 |
| M2 | FR-ID-005 | `identity.rs` 的 `observe_profile` 写 `account_observations.profile_json` 与同站候选；`store/identity.rs` 的 `profile_history`、`candidates`、`confirm`、`reject`；白名单字段，不含 Cookie/令牌/完整响应 | `profile_history_keeps_observations`、`similar_names_are_candidates_only`、`identity_confirm_is_explicit_and_conflict_safe`、`identity_reject_keeps_history`、`profile_evidence_has_no_credentials` | `/management?tab=identity`，账号页入口「资料历史与待确认关联」 |
| M3 | FR-LIVE-006 | `crates/core/src/replays.rs`；`store/replays.rs`；`live_replay_links` 唯一约束；覆盖区间并集与缺口、`quality::comparison`、`keep_both`/`replay_primary`，不删除分片或对象 | `clipped_replay_keeps_recorded_ranges`、`replay_link_requires_same_account`、`incomparable_quality_keeps_both`、`replay_reject_is_recorded` | `/management?tab=replays` |
| M4 | FR-ARC-004 | `crates/core/src/imports.rs`（扫描、托管复制 staging→对象库、原地索引只读摘要、外部观察）；`app/imports.rs`（inspect/start/status/cancel/resume/assign/check）；`store/imports.rs`；`JobType::Import` | `managed_import_leaves_source_untouched`、`in_place_index_never_moves_or_recycles`、`import_resume_does_not_claim_partial_success`、`duplicate_bytes_reuse_blob`、`external_change_marks_offline`、`import_rejects_link_traversal`、`import_assign_moves_selected_items_only` | `/management?tab=imports` |
| M5 | FR-ARC-006 | `crates/core/src/resources.rs`（SHA-256 分组、同类型/同角色规格过滤、图片感知哈希、FFmpeg 有界抽帧、`resource_signatures` 缓存、`comparison`）；`app/resources.rs`；决定只写 `resource_comparisons` | `exact_duplicates_group_by_sha256`、`similar_candidates_require_signature`、`incomparable_is_not_similar`、`compare_decision_never_deletes` | `/management?tab=compare` |
| M6 | FR-QUALITY-005 | `quality.rs` 的四类派生状态、`filtered_listing`、无条目的 `quality.configure` 排入 `quality-maintenance` 批处理；固定版本回收保护沿用现有 `protected` | `quality_filters_classify_groups`、`incomparable_is_not_lower_quality`、`pinned_variant_survives_recycle_after_policy_change`、`policy_change_reevaluates_affected_entries` | `/management?tab=quality`，升级页入口「质量状态筛选、固定保留与不可比较项」 |
| M7 | FR-BROWSE-006 | `app/work.rs`、`store/work.rs`；作品组、只读比较、批量预览令牌与作用域摘要、逐条结果；批量动作仅标签/组/同步未下载/重新评估 | `work_group_keeps_each_caption_and_source`、`work_batch_requires_matching_preview`、`work_batch_partial_failure_is_per_entry`、`work_compare_is_read_only` | `/management?tab=works` |
| M8 | FR-PLUGIN-005 | `plugin_install.rs`（清单摘要、协议/平台/能力/路径检查、staging→按版本目录、并排升级、空闲切换、回退、移除）；`store/plugin_releases.rs`；启动恢复活动版本 | `plugin_install_verifies_manifest_digests`、`incompatible_plugin_cannot_enable`、`plugin_upgrade_is_side_by_side`、`plugin_rollback_keeps_previous_release`、`plugin_remove_leaves_archive_readable` | `/management?tab=plugins`，模块页入口「安装、升级与能力」 |
| M9 | FR-PLUGIN-007 | `contracts/capability.rs` 四类独立契约；`Core::capability_call` 按活动清单、开关、声明方法与结果字段路由；`Core::notify` 失败只记录；`app/plugins.rs` 的能力列表与独立开关 | `capability_routes_only_declared_methods`、`notification_failure_is_isolated`、`login_assist_never_receives_credentials`、`content_processing_cannot_replace_original`、`optional_extensions_off_keeps_base_archive`；`only_the_device_identifier_reaches_the_site_that_signs_with_it` | `/management?tab=plugins` |

### 8.3 R4-5 yt-dlp 手动更新（DEC-039）

- `crates/core/src/tool_update/mod.rs`、`signature.rs` 与 `crates/core/assets/yt-dlp-public.key`：只由 `tool.status`/`tool.check_update`/`tool.apply_update`/`tool.rollback` 触发，不自动检查、不使用 `-U`。
- 下载 `yt-dlp.exe`、`SHA2-256SUMS`、`SHA2-256SUMS.sig`；先用随包 RSA 公钥验签（实际发布签名是 RSA/SHA-512，代码同时接受 SHA-256），再按 `SHA2-256SUMS` 验可执行文件摘要，写入按版本分开的目录；`update.json` 记录两件工具的摘要；切换前运行 `--version`，确认与发布版本和随包 Deno 均一致；有解析任务时拒绝或推迟，空闲后切换；启动时再次核对清单、摘要与版本，失败回退随包版本并记录原因。
- 固定样本：`tool_update::signature::tests::the_pinned_release_signature_verifies`（真实发布签名）、`a_tampered_sums_file_is_rejected`、`a_tampered_signature_is_rejected`；`an_installed_update_must_match_its_manifest_and_start`（摘要/启动检查）、`a_version_that_was_not_just_checked_is_refused_before_any_download`、`a_status_call_never_checks_for_an_update_by_itself`。
- 界面：`/management?tab=tool`，设置页入口「外部工具更新」。未自动发真实更新请求；真实站点的更新检查没有执行（本轮只做固定样本与离线反例）。

### 8.4 R4-6 小红书完整历史（DEC-041）与 R4-7 无日期增量

- 契约：`PluginRequest.context` 只由宿主注入；`pluginhost::access_context` 仅对小红书从凭证 Cookie 中取出 `a1`，`web_session` 与其他 Cookie 不外传；`kit::SiteAdapter::enumerate_with_context` 默认转调 `enumerate`，只有小红书覆盖。
- 适配器：`adapters/xiaohongshu/src/sign.rs` 计算 `x-s`/`x-s-common`/`x-t`/trace id；`api.rs::signed_notes` 调用 `/api/sns/web/v1/user_posted`，406/461 直接停止并说明历史未取完，不换参数重试；`site.rs` 有 `a1` 时翻页到列表结束，没有时保留首屏兼容路径。固定向量：`a_list_request_matches_the_fixed_vector`、`the_fingerprint_block_matches_the_fixed_vector`；离线夹具：`the_signed_list_continues_the_profile_and_carries_the_device_signature`、`a_refused_signature_stops_the_history_instead_of_being_retried`、`without_the_device_identifier_the_first_screen_is_used_and_no_signed_request_is_made`。
- 无日期增量：`scan_page.rs` 对“列表无日期、详情已定位在范围内、该订阅已完成过观察”的条目不再重复取详情（`a_repeat_incremental_check_does_not_fetch_a_known_undated_detail_again`）；`real_site_check.py` 的 `detail_date` 用单链接预览取详情日期，不选择、不下载；`test_an_undated_list_takes_the_instant_from_the_works_own_page_without_downloading_it` 覆盖。`source.preview` 现在返回详情的 `published_at`/`published_precision`，供检查脚本选对象。
- 全局代理：`real_site_check.py` 在隔离 Profile 里用 `network.set` 的 `scope=global` 设置 `TRACE_TEST_PROXY`，八站都经它；回执记录路线。

### 8.5 命令、结果与真实证据

所有命令在仓库根执行。原始日志与回执在被忽略的 `artifacts/evidence-runs/m2-r4f/`、`.local/site-check/run-m2-r4*/`；公开机器摘要是 `docs/evidence/gate/report.json`（由通过的 `stage.py report` 生成）。

| 命令 | 退出码 | 结果 |
| --- | --- | --- |
| `cargo fmt --all -- --check` | 0 | 无差异 |
| `cargo clippy --workspace --all-targets --locked -- -D warnings` | 0 | 无告警（`too_many_lines`/`too_many_arguments` 只在四个有注释的管理函数上局部放行） |
| `python scripts/stage.py test --run m2-r4h` | 0 | Rust 875 个测试、前端 20 个文件/140 个用例、脚本 45 个测试，全部通过；含真实二进制的 e2e |
| `python scripts/check_structure.py`、`check_generated_types.py`、`check_file_sizes.py`、`lint_lines.py` | 0 | 结构、生成类型、文件大小与断行通过；7 个新生成类型 |
| `python scripts/check_docs.py` | 0 | 52 份文档、100 条需求、38 条验收、12 条不变量、0 错误 |
| `python scripts/check_publication.py` | 0 | 0 条公开内容问题 |
| `python scripts/rule_evidence.py check` | 0 | 规则声明、真实回执与累计额度一致 |
| `python scripts/stage.py package --run m2-r4h` | 0 | `dist/trace-0.1.0-windows-x64-12`：37 个文件、342,213,078 字节；`SHA256SUMS.txt` 36 行，摘要 `ADBF2BA9C9F3FDB7EA09481D6BAC19CD6C4787875BE840298139C7422D10BD58` |
| `python scripts/check_package.py dist/trace-0.1.0-windows-x64-12` | 0 | 包成员与清单摘要、嵌入式 WebView 烟测、八个适配器 `hello`/`doctor` 通过 |
| `python scripts/stage.py bench --run m2-r4h --idle 1030 --seconds 3640 --entries 100000` | 0 | 3642.2 秒（空闲窗口 910.4 秒）、十万条元数据与五十万条媒体元数据、349 个混合负载采样点；热/冷查询 P95 318/242 毫秒；资源管理器热/冷 P95 246/189 毫秒；空闲 CPU 0.18%、内存 P95 52.4 MiB；四路录制 CPU 2.83%、内存 P95 68.1 MiB；开始录制 P95 0.80 秒；恢复 3.29 秒；界面关闭 2.99 秒；作业错误 0；峰值私有工作集 200 MiB |
| `python scripts/stage.py report --run m2-r4h`、`stage.py check` | 0 | 生成 `docs/evidence/gate/report.json`（自动证据；A 审查与产品验收未做），随后 `stage.py check` 通过 |
| `python scripts/real_site_check.py prepare` / `status` | 0 | 八站对象与 Cookie 就绪；执行前剩余额度为微博 139、小红书 128、Instagram 108、X 97、YouTube 100、TikTok 78、抖音 70、哔哩哔哩 70 |

真实站点检查都在最终包或与它同源的候选包上、用全局手动代理（`network.set` 的 `scope=global`，路线写进每份回执）执行；遇到限流、登录失效、签名拒绝或额度用尽即停，不重试、不换参数、不绕过。逐站结果：

| 站点 | 本次包与回执 | 通过 | 未通过/停止 | 规则状态 |
| --- | --- | --- | --- | --- |
| 微博 | `-10` 冒烟 + `-8` 全量，`run-m2-r4f-weibo-smoke`、`run-m2-r4d-weibo1` | 账号识别、列表 2 页（20 条均有日期）、图片与文字推文链接 | 视频推文链接 0 个候选；预览勾选 1 项导出、1 项失败；增量(关)找到 1 条但保存状态 unknown、再次检查集合不变；增量(开)没有视频候选 | 3 条 `partial` |
| 小红书 | `-7`（签名续页尝试），`run-m2-r4c-xhs1` | 账号识别 | 签名列表被站点 HTTP 461 拒绝，按 DEC-041 立即停止，完整历史标为未取完；其余检查未执行 | 3 条 `partial` |
| Instagram | `-8`，`run-m2-r4d-instagram1` | 登录探测未判为失效 | 目标主页仍被站点换成错误页（`source_unavailable`），账号识别失败；S-002 的新目标也没有显示 | 3 条 `partial` |
| X | `-10` 冒烟 + `-8` 全量，`run-m2-r4f-x-smoke`、`run-m2-r4d-x1` | 账号识别、列表 2 页、预览勾选下载、图片与文字推文链接 | 前两页没有原生视频，视频推文与两次增量未执行 | 预览 implemented；订阅与链接 `partial` |
| YouTube | `-8`，`run-m2-r4d-youtube1` | 账号识别 | 列表页返回不含初始数据的页面（`schema_changed`），按规则停止；预览与两种链接未重跑 | 全部 `partial`（旧包的通过记录已标注只对应旧包） |
| TikTok | `-8`，`run-m2-r4d-tiktok1` | 账号识别 | 列表被站点以限流答复，立即停止；增量、链接、预览未执行 | 全部 `partial` |
| 抖音 | `-9` 冒烟与增量，`-8` 全量，`run-m2-r4e-douyin-*` | 账号识别、列表 2 页、预览勾选下载、视频链接 | 增量(关)找到 1 条、保存状态 unknown、第二次检查集合不变，但检查途中额度用尽；增量(开)未执行 | 订阅 `partial`；视频链接 implemented |
| 哔哩哔哩 | `-9`，`run-m2-r4e-bilibili1` | 账号识别、列表 2 页、勾选下载、视频链接（含浏览器参数） | 增量(关)在额度用尽时停止；增量(开)、图文链接未执行 | 订阅与预览 `partial`；视频链接 implemented |

额度（A 在 §10 对账更正）：微博 62、X 74、Instagram 106、TikTok 73、小红书 123、YouTube 97 未用完；抖音与哔哩哔哩为 0，停止原因是额度，不是站点无更新。最终包 `-12` 与执行检查的 `-9`/`-10` 只差规则的说明文字、两个补充测试与包命名；没有改采集或下载行为，报告对这一差异照实记录。

### 8.6 最终包、基准与门禁

- **最终包：** `dist/trace-0.1.0-windows-x64-12`，37 个文件、342,213,078 字节；`SHA256SUMS.txt` 36 行，自身摘要 `ADBF2BA9C9F3FDB7EA09481D6BAC19CD6C4787875BE840298139C7422D10BD58`。`check_package.py` 的成员/清单/WebView/适配器检查通过。
- **一小时基准：** 3642.2 秒（空闲窗口 910.4 秒），十万条元数据、五十万条媒体元数据、349 个混合并行采样点、四路录制、两路下载与界面同时运行；热/冷查询 P95 318/242 毫秒，资源管理器热/冷 P95 246/189 毫秒，空闲 CPU 0.18%、内存 P95 52.4 MiB，四路录制 CPU 2.83%、内存 P95 68.1 MiB，开始录制 P95 0.80 秒，恢复 3.29 秒，界面关闭 2.99 秒，作业错误 0。全部 NFR 目标在阈值内。
- **门禁：** `stage.py report --run m2-r4h` 通过并写出 `docs/evidence/gate/report.json`；`stage.py check` 通过，产品验收仍为 `not-run`。该报告只覆盖自动证据，A 审查与人工验收仍需分别完成。

### 8.7 未执行与限制

- **真实站点：** 微博的视频推文与增量、小红书完整历史与增量、Instagram 目标、YouTube 列表、TikTok 列表、抖音与哔哩哔哩的增量没有通过；原因逐条写在上表与规则限制里。没有为通过而删除检查或放宽口径。
- **包与回执：** 通过记录若来自 `-8`/`-9`/`-10`，在规则限制里写明只对应旧包；最终包 `-12` 与它们只差规则文字、两个补充测试与包命名，没有改采集或下载行为。没有把旧包证据换绑成最终包证据。
- **人工检查：** ACT-003 的原生体验（输入法、缩放、声音、外部播放器）仍是 not-run；本轮新增管理页、导入、比较、回放、作品组、插件与工具更新的界面只经前端套件与包内 WebView 烟测，没有人工逐项操作。
- **未联网的真实更新：** DEC-039 的更新检查/安装只经固定签名样本与离线反例；没有在真实站点或 GitHub 发布上执行下载。
- **小红书签名：** 固定向量与离线夹具通过；真实站点以 461 拒绝，按 DEC-041 停止，没有换参数重试。`a1` 只由 Core 注入，`web_session` 与 Cookie 未离开 Core。

<a id="b-m2-handoff"></a>
## 9. B → A 最终审查交接

审查对象：分支 `codex/m2-multi-site-archive` 的唯一阶段提交（B 完成自检后 amend；最终 SHA 由 Git 读取，不在提交内的文件里自引用）。基点 `main@c1552e048ea608ba64deae820d127bbd5e969083`；未 push、未建 PR、未合并、未发布。

审查入口：

1. 本报告 §8（实现位置、M1～M9 需求/测试/界面路径、R4-5～R4-7、命令与退出码、真实证据、包摘要、未执行边界）；§4.1 是最终包后的规则状态。
2. `docs/evidence/rule-evidence.json` 与 `python scripts/rule_evidence.py check`；八站规则文案在 `adapters/*/src/rules.rs`。
3. `docs/evidence/gate/report.json`（自动门禁；产品验收仍为 not-run）。
4. 最终包 `dist/trace-0.1.0-windows-x64-12` 与 `SHA256SUMS.txt`；原始回执在 `.local/site-check/` 与 `artifacts/evidence-runs/m2-r4h/`（均不提交）。

建议复验范围：

- 完整差异与生产接线：schema v8 迁移、`creator.*`/`identity.*`/`import.*`/`resource.*`/`work.*`/`live.replay.*`/`plugin.*`/`tool.*` 的命令路由、`UI_COMMANDS`、前端 `/management` 与生成类型。
- 管理能力的边界断言：不自动合并/删除、原地索引不移动外部文件、固定保留不回收、移除插件不删档案、关闭扩展不影响基础归档、候选只由用户决定。
- DEC-039：真实签名样本（SHA-512）与摘要/启动/回退反例；确认没有 `-U`、没有自动检查。
- DEC-041：`a1` 只经宿主上下文进入小红书适配器，`web_session`/Cookie 不离开 Core；461 停止且不重试；固定向量与离线夹具。
- R4-7：无日期列表的详情定位、范围外不下载/不计入、重复增量不重复取详情；脚本与固定向量测试。
- 真实回执与规则状态：逐站对照 §8.5、`rule-evidence.json` 和适配器声明；确认停止原因、额度与“不把限流/签名拒绝当作没有更新”。
- 自动门禁：`stage.py check`、`check_docs.py`、`check_publication.py`、`check_structure.py`、`check_generated_types.py`。

未完成/限制：

- 八站订阅规则全部 `partial`：没有一站同时通过账号识别、列表 2 页与自动下载关/开两次增量。微博视频推文 0 候选；小红书签名续页 461；Instagram 目标错误页；YouTube 列表无初始数据；TikTok 列表限流；抖音/哔哩哔哩额度用尽。
- X 预览与抖音/哔哩哔哩视频链接为 `implemented`；其余规则 `partial`。
- 最终包 `-12` 与部分真实回执的 `-8`/`-9`/`-10` 只差规则说明文字、测试或包命名；报告如实标注，没有换绑旧包证据。
- ACT-003 人工原生体验仍是 not-run；真实 yt-dlp 更新下载未执行；Linux、安装器、签名发布不在本阶段。
- 自动检查通过只是 B 自检，不是 A 审查或产品验收；请 A 按[审查口径](../../REVIEW.md)独立核对，不采信报告结论本身。

<a id="a-m2-final-review"></a>
## 10. A 独立集中审查（2026-10-19）

审查基线为 `94d80ac652c08a762dbc091b5d53bca0e8912d8c`，基点为 `main@c1552e048ea608ba64deae820d127bbd5e969083`。本次接手时已有六个未提交文件：小红书 `api.rs`/`site_tests.rs`、Core `media_tools/mod.rs`/`tool_update/mod.rs`/`transport.rs` 和本报告的 §10 草稿；全部保留并逐项独立核实，没有把这些补丁算作原提交已具备的行为。下面替代该草稿中“工作区原始状态干净”和未经本次复验的结论。审查结论为 **rework-required**；未发现 P0，不登记 push、PR 或合并授权。

核查范围包括基点到目标的差异清单、schema v8 的快照/事务迁移与拒旧写、`UI_COMMANDS`→Tauri 通用转发→IPC→app/store→`/management` 的管理命令、生成类型、管理数据边界、DEC-039/041、无日期增量、八站规则声明、逐站原始回执、授权额度、最终包与 gate 输入指纹。命令在白名单内、类型生成通过，不代表参数、返回值和界面动作已经接通；以下缺口均有实际代码路径或反例支持。

### 10.1 A 直接处理的局部问题

| 编号 / 状态 | 触发、实际结果与修复 | 复验 |
| --- | --- | --- |
| F-069 / P1，部分修复、仍待 B | 原提交 `transport.rs::status_error` 与小红书 `signed_notes` 把 406/461 当 `RateLimited`，普通订阅经 `jobs/mod.rs` 自动进入 `waiting_retry`。保留已有补丁改为 `PermissionDenied` 并恢复业务码 `429 => RateLimited`，阻止 60 秒重试；但任务仍进入 waiting_auth，`credential_probe.rs::probe_guarded/wake` 在每次 valid 探测后调用 `store::credentials::wake_waiting_auth` 和 consumers 的 credential_changed，约 6 小时后的自动探测仍会重排签名拒绝任务，尚未满足 DEC-041 | transport 28 项、小红书 28 项通过；扩充 HTTP 200 业务码 406/461/429 的分类与单次请求断言；需补后台调度跨凭证复验的反例，真实站点未重跑 |
| F-070 / P1，部分修复、仍待 B 验收 | 原提交回退到非随包版本后清空活动指针，重启回到随包版本；随包损坏时先切走可用更新再返回失败。保留已有补丁：更新清单保存前一版本，回退持久化非随包指针，先验证随包候选再切换。成功回退、跨重启和损坏候选保留当前版本仍缺动态回归，不据代码推演关闭 | tool update 9 项通过，但它们只覆盖固定签名、篡改、无自动检查、摘要/启动失败等反例 |
| F-076 / P1，已修 | `store/identity.rs::merge` 允许 A→B 后 B→C，而 `resolve` 只走一步，旧 A 的时间线失去迁往 C 的内容。新增事务内 incoming redirect 检查，拒绝把已有重定向的目标继续合并，不改条目和文件 | `identity_tests` 11 项通过；扩展 `creator_merge_rejects_cycles` 验证第二层合并拒绝、绑定不变、旧 A 仍解析到 B |
| F-077 / P1，已修 | `ManagementView.vue::previewBatch` 发 `tag_add`，`app/work.rs` 只接受 `tags_add`，填写标签后预览必失败。改为正确参数 | 管理页真实 tab/输入/按钮交互断言 `tags_add`，Vitest 1 项及前端类型检查通过 |

### 10.2 一次性交回 B 的必须项

下列是本阶段功能或证据缺口，不转成用户人工验收，也不推迟为 LOOP。保留上节修复，按原编号回复实现和复验。

| 编号 / 严重度 | 位置与可复现结果 | 修复与验收 |
| --- | --- | --- |
| F-069 / P1（剩余） | `app/credential_probe.rs` 14/134～135/188～194 的定期 valid 探测，无条件调用 `store/credentials.rs::wake_waiting_auth`（244）与 `jobs/consumers.rs::credential_changed`（167）；当前 PermissionDenied 补丁只消除了计时重试，仍会被该自动路径唤醒 | 给签名拒绝可持久解释的停止状态或等价策略，不与凭证过期恢复混用；覆盖普通订阅签名 461 后，扫描时钟、valid 凭证复验、重启恢复均不再请求；仅显式用户动作可恢复，保留正常认证恢复 |
| F-071 / P1 | `scripts/real_site_check.py::check_incremental`（690～719）：第二轮只比较时间线 entry_id 集合，没有按目标 native key/订阅发现关系/日期过滤，也不核对详情请求。离线 mock 令范围内和范围外条目都 deferred，第二轮详情读取 1→3，仍返回 `ok=true` 和 `second check changed nothing` | 记录按 endpoint/native key 或 observe-attempt 区分的前后快照；确认目标在范围内、范围外零订阅发现/零媒体下载、重复增量零详情请求；自动下载开/关都要有正反例，不用集合不变替代 |
| F-072 / P1 | `ManagementView.vue::mergeCreators/splitCreator/relinkAccount`（62～87）直接写命令，`app/identity.rs`（26～66）直接写事务；用户未先看到账号、订阅、条目数量。`store::identity::scope` 只在写后结果或 get 中存在 | 三类操作均展示对应对象的影响范围，显式二次确认；输入/关系变化后重新预览或拒绝陈旧确认；覆盖取消零写入、实际执行范围和候选显式确认 |
| F-073 / P2 | `plugin_install.rs::remove`（501～515）先提交 `store/plugin_releases.rs::remove` 的 DELETE，再删目录；Windows 文件占用或权限错误时，目录还在、版本记录已丢，重试 NotFound | 改为可重试/可恢复的一致删除流程；目录失败、重启、再次移除均有反例，保留档案可读与回退记录 |
| F-074 / P2 | `ManagementView.vue::startImport`（123～127）不依赖 `importInspect` 即直接 start；可未预览就导入，或预览 A 后改路径 B 直接开始 | 成功预览后才能确认开始，预览与当前路径、模式、目标一致，展示数量/大小/重复/不可读项；输入变化使确认失效；Core 对实际扫描独立校验，不信陈旧预览。补 UI 和服务反例 |
| F-075 / P2 | `core.rs::capability_call`（550～564）只在返回对象时查字段名；null、数组、字符串直接成功，对象字段的类型和必填也未校验。`notify` 把任意 Ok 视作发送成功 | 四类能力分别验证返回对象、字段、必填和类型；覆盖标量、数组、未知字段、错误类型、缺字段；失败隔离且原始归档不被替换 |
| F-078 / P1 | `api.ts::resourceCompare/resourceCompareDecide`（701～719）发 `left_kind/left_id/right_kind/right_id`，`app/resources.rs::side_arg`（41～48）只收 `left_entry_id/right_entry_id` 或 left/right；默认条目比较也失败。UI 还提供 media/variant/blob，而 Core 比较只支持条目 | 统一参数和返回契约、明确且实现允许比较的对象类型；比较及三个决定均从真实 UI→API→Core 走通，决定只记记录，不删除或合并 |
| F-079 / P1 | `ManagementView.vue` 候选表（727～736）没有动作；关联/拒绝只在已有 links 的 `state==='candidate'` 显示，而 `store/replays.rs` 唯一新增行直接写 confirmed，候选查询只读不落行。空关系库永远无法建立首个关联 | 候选行提供显式关联/拒绝、覆盖与质量对比、受约束的主版本选择；测试从零关系开始完成关联与拒绝，保留全部录制分片/独有区间，不可比较不能以回放为主 |
| F-080 / P1 | 管理页质量筛选发 pending/pinned（245～249），Core 派生 pending_upgrade/manually_kept（`quality.rs` 311/321）且精确匹配，两个筛选恒空；表取 reason 而返回 reasons，actions 只有文本无固定/解除动作 | 四类枚举、原因和实际操作接通；覆盖筛选及 pin/unpin/重新评估的界面流程、受影响范围和固定版本回收保护 |
| F-081 / P1 | `tool_update::apply`（372～379）复用持久 Check，成功后不消耗；UI 安装后仍保留按钮。再次安装 V1 时删除重建当前目录，previous=V1，回退链指向自己，原版无法从入口退回 | 在任何下载/删目录前基于当前活动版本拒绝或幂等同版本安装，防止 self previous，处理过期 Check；重复安装不得动当前目录/清单/活动指针/原回退链；补 V0→V1→V2→退 V1→重启仍 V1→退 V0 |
| F-082 / P1 | apply 在 idle 检查（439）后 await 启动探测（444），期间解析任务可持 gate 开始，449 仍切换；rollback 也先 idle 再 await。`MediaTools::idle`（236～243）瞬时取许可即释放，存在确定的调度窗口 | 安装/回退切换与真实解析共用持有式互斥许可，覆盖实际切换和活动指针保存；可控延迟 probe 中启动解析的回归必须拒绝/推迟切换，失败不损当前版本 |
| F-083 / P1（验证缺口） | `plugin_tests::optional_extensions_off_keeps_base_archive` 只关闭 notification，content_processing 仍开，且仅 SQL 播种再读；不能证明计划 C16 的“关闭全部四类→基础归档/时间线/导出” | 关闭全部可选能力，用生产基础采集链保存合成作品、查询时间线、导出并校验；保留档案/凭证边界和通知失败隔离 |
| F-084 / P1（证据） | gate、包和 m2-r4h 保存的输入指纹与目标提交中的 `scripts/tests/test_real_site_check.py` 不符；在排除未提交补丁、处理工作树换行后仍失败。真实回执本身未记包摘要，只能从隔离 Profile 的 adapter_dir 辅助定位包名 | 最终代码完成后真实重跑测试、打包、正式原规模/时长测量、report/check；不手改哈希。新真实回执带包名、SHA256SUMS/源码指纹并由检查器验证；旧回执保留原归属，不补写旧包为新包。没有授权额度/被站点停止的项保持 partial/not-run，不为证据完整再试探 |

### 10.3 本次实际验证

| 命令 / 核查 | 退出码与结果 | 范围 |
| --- | --- | --- |
| `python scripts/stage.py check` | 1：stale source/commands/package/benchmark | 本次工作区；另重建目标提交输入，仍只有脚本测试一项不同并导致同类失败，非 A 修复才造成 |
| `python scripts/check_package.py dist/trace-0.1.0-windows-x64-12` | 1：包 manifest/source/build 指纹不符；嵌入式 WebView 关闭/重连测试通过 | 不宣称包自动门禁通过 |
| `python scripts/check_docs.py`、`check_publication.py`、`check_structure.py`、`check_generated_types.py`、`rule_evidence.py check` | 各 0 | 文档/公开内容/结构/类型/规则记录的机器检查；不能代替语义审查 |
| `python -m unittest discover -s scripts/tests` | 0：45 项 | 含门禁反例；F-071 的漏检仍需新增回归 |
| `cargo test -p trace-core --locked identity_tests:: -- --test-threads=1` | 0：11 项 | 含新增合并链拒绝断言 |
| `cargo test -p trace-core --locked transport:: -- --test-threads=1` | 0：28 项 | 传输分类、代理与 Cookie 作用域 |
| `cargo test -p trace-core --locked tool_update:: -- --test-threads=1` | 0：9 项 | 真实 RSA/SHA-512 样本、篡改与启动/摘要失败等；不证明成功更新/回退闭环 |
| `cargo test -p trace-core --locked undated_range_tests:: -- --test-threads=1` | 0：7 项 | 生产扫描范围外不计发现/不排媒体；已知详情第二轮请求不增加 |
| `cargo test -p trace-adapter-xiaohongshu --locked -- --test-threads=1` | 0：28 项 | 固定签名向量、无 a1 首屏兼容、HTTP/业务码拒绝与限流；单次请求 |
| `corepack pnpm -C apps/desktop exec vitest run src/views/__tests__/management.spec.ts`、`run typecheck` | 各 0：1 项交互测试、类型无误 | 管理 tab、无自动写入、批量标签参数；不覆盖缺失流程 |
| `cargo test -p trace-e2e --locked --test site_adapters rule_status_matches_recorded_real_evidence -- --exact` | 0：1 项 | 八站实际二进制声明与公开规则记录逐条吻合 |
| `cargo clippy -p trace-core -p trace-adapter-xiaohongshu --locked --all-targets -- -D warnings` | 0 | 本轮受影响 Rust crate 的严格检查 |
| `cargo fmt --all -- --check`、`check_file_sizes.py`、`lint_lines.py` | 最终各 0 | 初次 fmt 查出原有补丁及新改动排版差异，格式化后通过，不改测试语义 |

没有全量重建新包、没有重跑一小时测量、没有真实网站/更新下载；原始 B 失败和已保存旧证据未改写。定向 Rust 测试编译有 MSVC 链接器消息 warning，未隐去；测试结果均为零失败。

### 10.4 真实回执、规则与管理边界

- 逐站核对 `.local/site-check/run-m2-r4c-*`～`run-m2-r4f-*` 的原始 receipt 与 Profile：小红书 `-7` 的签名 461 后停止；微博/X/抖音 `-8`，哔哩哔哩与两站增量 `-9`，微博/X 追加冒烟 `-10`。并非每站都跑在 `-12`，不将 Profile 包名或旧通过记录改绑新包；目前回执缺少包摘要的问题见 F-084。
- 全部回执路线为 `proxy · 全局设置`。微博视频 0 候选与预览部分失败、小红书 refused、Instagram source_unavailable、YouTube schema_changed、TikTok rate_limit、抖音/哔哩哔哩 budget 均保留失败或未执行，不把它们记作无更新。Core 无日期范围反例通过，不等于 F-071 的真实增量驱动已能验证同样边界。
- `rule-evidence.json` 八站累计元数据/媒体用量与 ledger 一致。授权先扣已记录的前置探测，再扣 ledger：微博剩 62、X 74、小红书 123、Instagram 106、YouTube 97、TikTok 73、抖音/哔哩哔哩 0。B 摘要微博 70/X 80 漏扣额外冒烟，已修报告与 USER-ACTIONS；本次零新增请求。
- 八站订阅全部 partial。实际 implemented 集合为 `x.user.preview`、`douyin.user.preview_videos`、`douyin.video.link`、`douyin.live.link`、`bilibili.video.link`、`bilibili.live.link`；后两站直播链接沿用已合并证据。B 交接摘要漏列抖音视频预览和两站直播，已修 §4.1 与当前状态；这是文案更正，没有升降规则状态。
- `-12` 包实物 37 文件、342,213,078 字节；SHA256SUMS 自身 SHA-256 为 `ADBF2BA9C9F3FDB7EA09481D6BAC19CD6C4787875BE840298139C7422D10BD58`，成员哈希匹配。实物完整与源码绑定失败是两个事实，后者不能被前者豁免。
- a1 只从宿主为小红书生成的 device_id 上下文进入适配器，未发现 web_session/整段 Cookie 外传；工具执行有 `--no-update`/`--no-cookies`，未发现 `-U` 或自动联网检查。
- 比较决定只存记录；原地索引以非托管外部副本保存，回收用对象库位置及 managed 条件；固定引用有 protected 检查。未发现自动合并/删除、移动或回收外部源文件、因插件移除删档案的路径；插件移除自身的一致性仍需修 F-073，全部扩展关闭的产品链仍缺 F-083。

### 10.5 阶段判断

**实现交付：未完成；A 技术审查：rework-required；产品验收：not-run。** schema 和命令注册已有实现，管理界面与关键故障仍不足以满足计划；本次未发现凭证泄漏或档案自动删除的 P0。ACT-003、真实 yt-dlp 更新、Linux、安装器和签名发布均未执行。当前不新增用户阻塞或远端授权请求，推荐 B 一次性返工后再交 A；人工检查不是以后远端授权的前提。

### 10.6 A → B 集中返工 prompt

```text
请作为 Agent B，在 codex/m2-multi-site-archive 连续完成 TRACE-M2-01 r2 的集中返工。原被审提交为 94d80ac652c08a762dbc091b5d53bca0e8912d8c，基点 main@c1552e048ea608ba64deae820d127bbd5e969083；本轮 A 修复与报告保存后的当前基点以 git log -1 为准。
先读 AGENTS.md、REVIEW.md、stage-plan §11/§12、stage-report §10 与 USER-ACTIONS 的 ACT-007；本轮首选行动是返工，不是申请远端授权。
保留已有 F-069/F-070 补丁，以及 A 新补的 429 分类、F-076 合并链保护、F-077 批量标签参数和对应回归；不要把 F-069/F-070 当已关闭。
一次性完成 F-069 的持久停止、F-070 的动态验收、F-071～F-075、F-078～F-084。重点补管理页比较、首次回放关联、质量操作的实际 UI→API→Core 契约回归；确认范围与取消边界；插件移除失败恢复；四类能力结果校验和全部关闭后的基础归档/时间线/导出；工具同版重复安装、probe 并发切换、连续回退跨重启；无日期增量的目标/范围/详情请求快照。
按原编号记录修复位置、输入、实际结果、命令和退出码。最终代码冻结后重新跑必需自动检查、打包和原规模/时长基准，再生成 report/check；旧日志和回执不改绑。真实站点不为补证据而重试，本轮不追加请求；遇挑战/限流/签名拒绝或已耗尽额度的项保留 partial/not-run。新驱动必须记录包摘要与输入指纹。
同步唯一报告、当前状态及必要契约；需求仍为“本阶段”，ACT-003 仍 not-run。不 push、不建 PR、不合并、不发布；全部 amend 进同一个带 DCO 签名的阶段提交，交付最终 SHA、包摘要、原始证据入口与 B→A 复验 prompt。
```

<a id="b-m2-round-5"></a>
## 11. B 集中返工 F-069～F-084（2026-10-19）

返工基线为 `22b5082a63fd9fcc0c90ce70fc6b8451348d71a3`。A 在 §10.1 直接补的 429 分类、F-076 合并链拒绝、F-077 批量标签参数和原有签名拒绝/回退指针补丁全部保留；本节记录的是在其上完成的生产实现、反例和最终证据。真实站点本轮零新增请求，旧回执和旧规则状态不改绑，八站订阅规则仍全部为 `partial`。

### 11.1 逐项修复与回归

| 编号 | 修复（生产位置） | 回归 |
| --- | --- | --- |
| F-069 | 站点明确拒绝（`PermissionDenied`，含小红书 461）落到可解释的 `waiting_user`，`last_error.class=permission_denied`；`wake_waiting_auth`、凭证变更、凭证泊车、`upgrades::schedule`、低于目标的媒体重排均排除此类停止；重启只恢复 `leased/running`；`jobs/consumers.rs` 不再把它改回 `waiting_auth`；只有 `job.retry`/显式重试恢复 | `permission_refusal_is_only_retried_by_an_explicit_user_action`；普通 `AuthRequired` 唤醒仍由既有 `shared_auth_failure_and_credential_change_do_not_cross_consumers` 覆盖 |
| F-070 | `tool_update` 成功回退保留非随包 `previous`；启动 `restore` 重新校验；损坏候选拒绝且不切换、不改活动指针 | `rollback_survives_restarts_and_a_damaged_candidate_keeps_the_current_version`（V3→V2→重启→V1→重启，损坏 V2 仍保持 V3） |
| F-071 | `scripts/real_site_check.py` 在增量前后记录完整 `requests` 快照与目标条目的 `observe/media` attempt 快照；核对发现条目的 `native_key`、是否落在请求范围、范围外条目、第二次检查详情 attempt 是否增长、自动下载关闭时媒体字节是否为 0；自动下载开启检查目标保存状态；回执记录每次请求的 `endpoint`/`started_at` | `test_an_incremental_check_verifies_range_detail_attempts_and_no_media_on_a_second_pass`（范围外、详情 attempt 增长、第二次媒体字节、成功路径）；既有增量/无日期驱动测试 |
| F-072 | 新增 `creator.merge_preview`、`creator.split_preview`、`creator.relink_preview`，返回源/目标范围、每个账号的订阅与条目数量；执行命令必须带与当前状态重算一致的 `preview_token`，绑定/数量变化即 `VersionConflict`；管理页先预览、显示影响、勾选确认后才可执行；输入变化清空预览 | `identity_previews_show_scope_and_a_changed_binding_invalidates_the_confirmation`；管理页 Vitest 的预览 token 与实际 API 参数 |
| F-073 | 移除插件改为“先原子改名到 `.removing-<version>`，再删目录，最后删记录”；改名/删除失败保留原目录与记录可重试；启动 `reconcile_removals` 完成改名后崩溃的删除并清理残留 | `a_failed_plugin_removal_stays_retryable_and_keeps_the_archive`、`a_removal_interrupted_after_the_directory_is_gone_is_reconciled_at_startup`、既有 `plugin_remove_leaves_archive_readable` |
| F-074 | `import.inspect` 返回覆盖路径、文件集合、模式、目标的 `preview_token`；`import.start` 必须带 token，Core 重新扫描后不一致即拒绝，不写批次；预览显示文件数、字节、不可读、重复、链接跳过；管理页输入/模式/目标变化清空预览，必须勾选确认 | `import_start_requires_the_exact_preview_and_refuses_a_stale_one`；管理页 Vitest 的预览/确认/旧 token |
| F-075 | 四类能力契约增加必填字段与 JSON 类型；`validate_result` 拒绝 null/标量/数组、未声明字段、缺必填、错类型；`notify` 只在返回对象 `delivered=true` 时算成功；失败不替换归档 | 契约单测 `capability_result_shapes_are_checked_for_every_kind`；`capability_results_are_typed_and_a_failure_never_touches_the_archive`（四类成功、六类坏结果、通知 false/true、归档标题与正文不变） |
| F-078 | `resource.compare`/`decide` 统一接受 `left_entry_id`/`right_entry_id`，非 `entry` 的 kind 明确拒绝；UI 只提供条目比较；决定只写比较记录 | `comparison_uses_the_ui_entry_contract_and_only_records_a_decision`（比较、三种决定、不删除、拒绝 media kind）；管理页 Vitest 比较参数 |
| F-079 | 回放候选表增加“关联并保留两者 / 设回放为主 / 拒绝”；零关系可直接建立首条关联，拒绝也落持久记录（upsert rejected）；主版本仍受覆盖完整与质量可比较约束；消息显示覆盖/质量；录制分片和独有区间不动 | `the_first_relation_can_be_linked_or_rejected_from_zero`、既有分片/独有区间测试；管理页 Vitest 候选关联 |
| F-080 | 质量筛选值改为 `pending_upgrade`/`incomparable`/`replaced`/`manually_kept`/`stable`；表格显示 `states` 与 `reasons`；增加固定/解除/重新评估动作，按条目解析当前变体；保存策略返回受影响条目数 | `quality_failure_shared_blob_pins_unique_coverage_and_gc_windows` 增补 `pin_entry` 固定/解除；管理页 Vitest 枚举筛选与固定动作 |
| F-081 | 安装前基于当前活动版本拒绝同版安装，并拒绝与当前版本不一致的过期 Check；拒绝发生在任何下载、删目录、清单、活动指针或回退链写入之前；安装成功后 UI 清除 Check、按钮失效 | `the_same_version_and_a_stale_check_are_refused_before_any_change`（保留目录标记文件、活动指针不变）；`rollback_survives…` 覆盖回退链 |
| F-082 | `MediaTools::exclusive()` 持有真实解析同一个信号量；安装/回退在最终校验、`activate` 和活动指针持久化期间持有许可；已在运行的解析让切换等待，新解析不能插队；失败不损当前版本 | `a_switch_waits_for_a_tool_run_and_holds_the_same_permit`；既有安装启动/摘要反例 |
| F-083 | 可选扩展测试声明并关闭全部四类；用生产 `process_scan_page` 保存合成作品，再查时间线、导出 `entries.jsonl`、读回标题；凭证仍在；通知失败隔离 | `optional_extensions_off_keeps_base_archive` |
| F-084 | 新回执写入包名、`SHA256SUMS.txt` 摘要、manifest 的提交/源码树/协议；`record` 对带包身份的回执重新核对包实物，旧回执没有该字段则保留原归属；最终代码冻结后重跑测试、打包、原规模/时长基准和 report/check | `test_a_new_receipt_is_bound_to_its_package_digest_and_source_tree`；最终命令见 §11.3～§11.4 |

### 11.2 UI→API→Core、取消与故障恢复

- 管理页两条 Vitest 用例覆盖：身份合并预览/确认、导入预览/勾选/旧 token、条目比较参数、零关系回放候选关联、质量枚举筛选与固定；均断言真实 `api.*` 参数，而不是只渲染按钮。
- Rust 侧覆盖同三条链路：`creator.*_preview` 命令→事务前重算 token→`store::identity`；`import.inspect`→`import.start` 重扫描；`resource.compare`/`decide`→比较记录；`live.replay.candidates`→`link`/`reject`；`quality.pin(entry_id)`→当前变体。
- 取消/陈旧确认：拆分空选择、Relink 过期预期、合并/拆分/重关联 token 变化、导入路径/模式/文件变化、同版安装、过期 Check、回放主版本约束、能力结果坏形状均被拒绝且不写目标状态；插件移除失败、改名后崩溃、工具回退跨重启、解析并发都有反例。

### 11.3 最终命令与退出码

| 命令 | 退出码与结果 |
| --- | --- |
| `python scripts/stage.py test --run m2-r5b` | 0：Rust 887 项、前端 20 个文件/141 项、脚本 47 项全部通过；`test-frontend` 同时跑 Prettier、`vue-tsc`、Vitest 与生产构建 |
| `cargo clippy --workspace --locked --all-targets -- -D warnings` | 0 |
| `cargo fmt --all -- --check` | 0 |
| `python scripts/check_docs.py`、`check_publication.py`、`check_structure.py`、`check_generated_types.py` | 各 0（文档 52、链接 801、需求 100、验收 38、必需不变量 12、决定 18） |
| `python scripts/stage.py package --run m2-r5b` | 0：`package/portable` 与 `package/check` 均通过 |
| `python scripts/stage.py bench --run m2-r5b --idle 1030 --seconds 3640 --entries 100000` | 0：时长 3641.2 秒、空闲窗口 910.3 秒；10 万条/50 万媒体元数据；热/冷查询 P95 144/139 ms，资源管理器热/冷 P95 113/105 ms；空闲 CPU 0.18%、内存 P95 51.9 MiB；四路录制 CPU 1.64%、内存 P95 68.4 MiB；启动录制 P95 1.18 秒、恢复 2.13 秒、GUI 关闭 3.02 秒；0 作业错误 |
| `python scripts/stage.py report --run m2-r5b`、`python scripts/stage.py check` | 各 0：`docs/evidence/gate/report.json` 已生成，check 输出 `automated evidence passed; A review pending; product acceptance not-run` |

### 11.4 最终包、原规模/时长基准与门禁

- 最终包 `dist/trace-0.1.0-windows-x64-13/`：37 个文件、342,154,710 字节；`SHA256SUMS.txt` 自身 SHA-256 为 `394D933912D017F9F0CD4CF4BEF57BEFEE66920BBFB768E223C36E6F2FCA99EA`。manifest 的源码树为 `fc98d4f0b298df237f1cded8a4438c2126374269cfe3cdfee830c6d3be970c78`，schema 8、控制协议 0.1、`productAcceptance=not-run`；包内 WebView、八站适配器 hello/doctor 与清单检查通过。manifest 的 `git_commit` 是构建时的 `22b5082` 且 `source_dirty=true`：包由同一冻结工作树构建，最终 amend 只把该工作树写入提交元数据；`sourceInputs`/`source_tree_sha256` 与最终提交的工作树逐项一致，不能把 `git_commit` 字段误读为最终提交号。
- 首次一小时长测在收尾校验时报 `invalid observable timestamps`：四个合成场次的 `started_at` 有两个早于驱动记录的全局 observable 时间，属基准驱动的时钟/时间戳缺陷，不是产品检查通过或失败。失败原始证据保留在 `artifacts/evidence-runs/m2-r5b-bench-failed/`，没有修改判据或把失败样本补写成通过；本轮按同一冻结源码、同一参数重跑，结果如下。
- 原规模/时长基准（重跑）：3641.2 秒、空闲窗口 910.3 秒、10 万条/50 万媒体元数据；热查询 P95 144 ms、冷查询 P95 139 ms，资源管理器热/冷 P95 113/105 ms；空闲 CPU 均值 0.18%、内存 P95 51.9 MiB；四路录制 CPU 均值 1.64%、内存 P95 68.4 MiB；启动录制 P95 1.18 秒、恢复 2.13 秒、GUI 关闭 3.02 秒；352 个混合并行样本、4 次录制启动、0 作业错误。第一次失败样本仍保留在 `m2-r5b-bench-failed/`，没有用重跑覆盖或改判据。
- 自动证据、包和基准绑定的是同一冻结源码；文档编辑不改变产品输入指纹。旧 `-12` 包、旧 gate 报告和旧回执保留原输入与归属，不改绑到本次源码。

### 11.5 真实回执、规则与未执行边界

- 本轮**零新增真实站点请求**；`.local/site-check/` 的既有回执仍只属于各自包（小红书 `-7`、微博/X/抖音 `-8`、哔哩哔哩与增量 `-9`、微博/X 追加 `-10`），没有改绑到新包。新回执代码会记录包摘要与源码树，但本轮没有产生新回执。
- 八站订阅规则全部 `partial`；`x.user.preview`、`douyin.user.preview_videos`、`douyin.video.link`、`douyin.live.link`、`bilibili.video.link`、`bilibili.live.link` 保持 `implemented`。微博视频 0 候选、小红书 461、Instagram 目标错误页、YouTube 列表无初始数据、TikTok 限流、抖音/哔哩哔哩额度用尽均保留为失败或未执行，不写成“没有更新”。
- 授权额度不变：微博 62、X 74、小红书 123、Instagram 106、YouTube 97、TikTok 73、抖音/哔哩哔哩 0。ACT-003 人工原生体验、真实 yt-dlp 更新下载、Linux/安装器/发布签名仍未执行。

### 11.6 本阶段判断

实现交付：待 A 复验；A 技术审查：pending；产品验收：not-run。自动证据通过只说明 B 自检，不是 A 审查或产品验收。

## 12. B → A 集中返工复验交接

**最终提交：** 本次 amend 后的 `HEAD`（准确 SHA 在给 A 的交接消息中给出；报告不写自引用的提交号）。分支 `codex/m2-multi-site-archive`，工作区应为干净；未 push、未建 PR、未合并、未发布。

**入口：** [§11 返工实现与证据](#b-m2-round-5)、§11.1 逐项表、§11.3 命令/退出码、§11.4 包/基准、§11.5 真实回执边界；`docs/evidence/gate/report.json`；`artifacts/evidence-runs/m2-r5b/`（test/package/bench/report 回执与日志，均不提交）；`artifacts/evidence-runs/m2-r5b-bench-failed/`（第一次长测失败的原始证据，不覆盖、不补写）；最终包 `dist/trace-0.1.0-windows-x64-13/` 与 `SHA256SUMS.txt`。

**请 A 重点复验：**

1. F-069：普通订阅收到站点 `PermissionDenied`（含小红书 461）后是否停在 `waiting_user`，扫描时钟、valid 凭证复验、凭证变更/泊车、升级排程、低于目标媒体重排、重启是否都不再请求；只有显式 `job.retry`/用户动作恢复；普通 `AuthRequired` 的认证恢复是否保留。
2. F-070/F-081/F-082：成功回退、跨重启、损坏候选保持当前版本；同版安装与过期 Check 在任何下载/删目录/清单/活动指针/回退链写入前拒绝；安装/回退是否与真实解析共用持有式许可，启动解析时切换等待而不是越过。
3. F-071：增量驱动是否按 `endpoint`/目标 `observe|media` attempt 快照验证目标在范围内、范围外零发现、第二次检查零详情、自动下载关闭零媒体；失败时是否返回 false，而不是用集合不变替代。
4. F-072～F-074：合并/拆分/重关联的预览范围、二次确认、陈旧 token；导入预览 token 覆盖路径/文件/模式/目标，Core 重扫描拒绝陈旧确认；插件移除改名失败、改名后崩溃、重启重试是否保留档案与记录。
5. F-075/F-078～F-080：四类能力返回对象、必填、类型、未知字段；通知 `delivered=false` 的隔离；条目比较 UI→API→Core 契约与三种决定；零关系回放候选的关联/拒绝、覆盖/质量/主版本约束；质量四类筛选、原因和固定/解除/重新评估。
6. F-083/F-084：全部四类可选扩展关闭后，生产 `process_scan_page` 保存、时间线、导出和凭证边界；新回执的包摘要/源码树验证，旧回执不补写新包。
7. 自动门禁：`stage.py check` 是否仍输出 `automated evidence passed; A review pending; product acceptance not-run`；文档、公开内容、结构、生成类型检查；包实物与源码指纹。

**已知限制：** 八站订阅规则全部 `partial`；本轮零新增真实请求，旧回执仍属 `-7`～`-10` 包，不改绑；ACT-003 人工原生体验、真实 yt-dlp 更新下载、Linux/安装器/签名发布 not-run；第一次长测的 observable 时间戳失败保留原证据，不改变判据，重跑通过；包 manifest 的 `git_commit`/`source_dirty` 是构建时快照，最终提交以 `HEAD` 和 `source_tree_sha256` 为准。

这是 B 自检与返工交接，不是 A 审查或产品验收。

<a id="a-m2-rework-review"></a>
## 13. A 独立返工复验（2026-10-19）

被审提交为 `4e8a8a1988334beb1b8eeb42d140e79e959a74f8`，返工基线为 `22b5082a63fd9fcc0c90ce70fc6b8451348d71a3`，原审查对象为 `94d80ac652c08a762dbc091b5d53bca0e8912d8c`；接手时分支、唯一阶段提交及工作区均已核实，工作区干净。独立读生产代码、UI/API、反例和原始证据，不以 §11 的 B 自检结论代替审查。结论仍为 **rework-required**；没有发现 P0，不申请 push、PR、合并或发布授权。局部问题直接处理，下面六项剩余缺口一次性交回 B。

### 13.1 原冻结证据的独立复核

- 修改产品输入前，独立执行 `stage.py check` 成功；逐条校验 gate 的源码输入、命令原始日志摘要、包清单与源码树，未重现 §10 的旧指纹不一致。被审源码树为 `fc98d4f0b298df237f1cded8a4438c2126374269cfe3cdfee830c6d3be970c78`，共 770 项输入。887 项 Rust、141 项前端、47 项脚本是 B 原始日志中的用例数；A 重新读取并核对日志，而非宣称又运行过整个 workspace 的 887 项。
- 独立重跑 `-13` 包检查，两个用例通过，包含真实嵌入式 WebView 的关闭/重连；实物 37 文件、342,154,710 字节。SHA256SUMS 自身 SHA-256 为 `394D933912D017F9F0CD4CF4BEF57BEFEE66920BBFB768E223C36E6F2FCA99EA`，成员摘要正确。这证明原包完整且与原冻结输入相符，不证明下节的新回执验证器能发现所有篡改。
- 从 938 个原始资源样本重新调用同一测量推导，结果与 gate 的测量逐字段一致：空闲 CPU 0.179140%、内存 P95 51.867 MiB；四路录制 CPU 1.635626%、内存 P95 68.426 MiB；热查询 144.363 ms、资源管理器热查询 112.615 ms；启动 1.18197 s、恢复 2.13085 s、GUI 关闭 3.01666 s。原规模、正式时长与阈值没有缩减；A 没有另跑一小时测量。
- 第一次失败基准的 JSON、原始资源样本、生命周期、命令回执和日志仍在原位置；源码输入均与原冻结输入一致，退出码 1。重新推导仍报 `invalid observable timestamps`；四个场次中两个 detected-observable 为负（约 -2.044/-3.892 s），并非删掉失败后改判据。失败日志 SHA-256 为 `be03edb1106e7eea7078eebcf68892a430d4ab2dc38d1d738a340a91b5dd237b`。
- 本次局部产品代码/测试修复改变五个输入，仍为 770 项，修复后源码树为 `08f91296efb92be74a832fa1b0112862c491a36d86e8e5611107cf671101c7aa`；原 gate、包及基准保留原归属，不补写为修复后通过。最终源码上的 stale 拒绝是预期结果，B 完成下节返工后必须冻结新输入、重跑必要测试/包/原规模长测，再生成 report/check。

### 13.2 一次性交回 B 的剩余必须项

保留原编号，不把这些缺口转给人工验收或顺延 LOOP；它们与已通过的机器门禁可以同时存在。

| 编号 / 严重度 | 实际路径与反例 | 完成条件 |
| --- | --- | --- |
| F-069 / P1，仍开放 | [消费者唤醒](../../crates/core/src/jobs/consumers.rs#L156-L171) 与 [认证补全排程](../../crates/core/src/upgrades/schedule.rs#L43) 用逻辑 job 的停止状态/错误判定拒绝，但拒绝实际属于某个 consumer。A 被拒后 reconciliation 选 B，job 变 queued 且错误被覆盖；自动 invalid→valid 凭证探测可将 A 从 waiting_user 改成 waiting_auth 再 queued，单次 valid 也能替 A 排 auth_completion。另一条路径是普通订阅的 incremental/history/deep 独立任务：一个被拒只停自己，另两个仍可 claim，并继续访问同一个签名列表端点。新增单消费者测试没有覆盖这两种实际所有权/扫描模型 | 拒绝按 owner/订阅/请求范围持久停止，所有自动扫描、凭证复验、补全、重排与重启都遵守；不得饿死另一个合法 owner；只有显式用户重试能解除。补双消费者 invalid→valid、valid-only、普通三 lane、重启与显式重试回归，保留 AuthRequired 正常恢复 |
| F-071 / P1，仍开放 | [详情快照](../../scripts/real_site_check.py#L704-L715) 读 `jobs.attempts`，而该字段是失败预算，不是成功执行计数；成功 claim/succeed 不增加它。[第二次增量](../../scripts/real_site_check.py#L808-L833) 只记录而不核对 endpoint 请求，且没有核对 `entry_subscriptions`。离线驱动反例中，同一目标详情成功执行两次、失败计数始终 0、目标订阅发现记录为 0，auto-off 仍返回 ok=true、detail attempts unchanged；auto-on 对没有本订阅发现关系的 complete 条目也返回 true | 用真实成功/失败 attempt 或按 endpoint/native key 可验证的计数，不伪造 attempts 递增；核对目标属于本次订阅、范围外零订阅发现/媒体、重复增量零详情与新任务；开/关均有正反例，预览或其他 lane 的入库不得冒充订阅发现 |
| F-072 / P1，仍开放 | [三种确认操作](../../crates/core/src/app/identity.rs#L179-L228) 在 `db.read(...).await` 重算/比较 token，随后另行 `db.write(...).await` 写关系。[DB actor](../../crates/core/src/db/mod.rs#L49-L52) 仅串行单闭包；已经排队的绑定/数量写入可在两者之间提交。A 的无 sleep、手动 poll/actor 屏障反例实际返回 accounts_moved=2，将预览外的 acc-not-shown 一起迁走；split/relink 同样不原子保证已确认的数量/范围 | 在同一写事务内重算相同影响、比较 token、执行关系变更与审计；不一致即 VersionConflict 且零写入。用 actor 屏障准确安排“校验后、写入前”的变更，不只在调用前改数据 |
| F-073 / P1 可用性、P2 恢复，仍开放 | [删除恢复](../../crates/core/src/plugin_install.rs#L551-L574) 的 remove_dir 错误经 [Core 启动](../../crates/core/src/core.rs#L242) 传播，Windows tombstone 子文件 share_mode(0) 占用反例实际返回错误 32，Core::open 失败，基础档案也不可读。另在 [先删 trash、后删 DB](../../crates/core/src/plugin_install.rs#L529-L545) 之间崩溃，两目录都不存在时启动直接跳过，死 release/回退候选永久保留。删除前 non_active 查询还未向 activate/rollback 预留，存在删文件后才发现版本已 active 的代码级竞态；此竞态未宣称已动态复现 | 持久移除意图/状态覆盖改名、物理删除和 DB 清理；清理失败只能产生可重试告警，不能阻断基础档案启动；activation/rollback 与 removing 互斥或同生命周期锁。覆盖 Windows 显式 share_mode(0) 占用启动、trash 已删/DB 未删重启、再次移除、并发 activate/remove，并核对档案与活动驱动不受损 |
| F-079 / P2 剩余，首次关联已修 | [候选动作](../../apps/desktop/src/views/ManagementView.vue#L292-L312) 已能从零关系关联/拒绝，Core 对不完整或不可比较的 replay_primary 也会拒绝；但界面仅在写后用 toast 显示 complete/quality，[已有关联列](../../apps/desktop/src/views/ManagementView.vue#L576-L600) 没有覆盖范围、录制独有区间或质量。候选“设回放为主”之前也看不到足以作决定的比较；页面声称区间会列在记录里，实际未渲染 | 提供只读覆盖/质量比较及受约束的主版本选择，持久展示 recorded/unique ranges、不可比较原因，不用写操作当预览；从零关联、拒绝、不可比较/剪辑缺段、取消和已有记录展示均走实际 UI→API→Core 回归；保持全部录制分片 |
| F-084 / P2 剩余，原输入绑定已核实 | [包身份验证](../../scripts/real_site_check.py#L65-L98) 只读 manifest 并散列 SHA256SUMS 文件，未逐项核对实际成员；包目录缺失还直接返回成功。合成包保持 manifest/清单不变，仅修改 trace-daemon.exe，校验返回空错误；包完全缺失也为空错误。实际 `-13` 成员正确，不把此验证器缺口误写成当前包被篡改 | 新回执验证须核对包成员的实际摘要/缺失/额外成员及源码树关联；包缺失须失败或明确不可验证，不得报告成功；新增“二进制被改但 sums 未动”和缺包反例。旧真实回执不补写、不换绑；修复冻结后再生成新自动证据 |

### 13.3 A 直接处理的局部问题与已核实项

| 编号 / 状态 | 处理与边界 |
| --- | --- |
| F-085 / P2，局部修复 | [回放候选](../../crates/core/src/store/replays.rs#L208-L233) 把 RFC3339 的 T/时区字符串直接与 SQLite datetime 的空格字符串比较，错误地包含当天过早条目、漏掉次日有效条目。统一转为 datetime 后按真实时刻比较；新增同日下界、次日上界、时区偏移及越界回归 |
| F-086 / P2，局部修复 | [工具真实运行](../../crates/core/src/media_tools/run.rs#L354-L440) 在拿许可前抓取程序路径；等待回退的解析/下载可在切到 V1 后仍执行 V2。将 program 解析放到拿许可之后，新增排队查询/下载启动真正合成 cmd 子进程的反例；不与已修的 F-082 物理运行/切换重叠混淆，不声称执行过真实下载 |
| F-070、F-081、F-082 / 原缺陷已核实 | 非随包回退持久指针、先验坏候选再切换、同版/过期 Check 拒绝以及持许可的最终 current 重查符合原要求。A 加强成功回退测试为 shutdown 后重新 Core::open，再核对 V2/V1 与下一 previous；版本 probe 仍使用合成版本映射。原 F-082 测试仅手持许可+无活动指针的 rollback，证据较窄；A 的 F-086 测试补真实合成解析进程，但仍不是联网成功安装闭环 |
| F-074、F-075 / 原缺陷已核实 | 导入 token 独立扫描绑定路径/文件元数据/模式/目标，目标在写事务再查有效；四类返回值必须 object、允许字段、必填和 JSON 类型，生产路径无条件校验；notify 仅 delivered=true 成功。导入 inspect 明确不散列字节，不能声称识别相同元数据下的字节替换；能力 Core 回归使用 test-only 结果替身，非外部进程线协议测试 |
| F-076、F-077、F-078、F-080 / 原缺陷保留关闭 | 合并链保护、批量 tags_add、条目比较参数和三种只记决定、质量枚举/reasons/pin/unpin/重新评估已接通。pin_entry(false) 会清除全条目的 variant_retention 固定，不把“只解当前变体”的早期猜测登记为缺陷；固定/共享 blob/独有区间守护保留 |
| F-083 / 在明确范围内已核实 | 四类全部关闭后，确实经过生产 process_scan_page 保存 summary、查询时间线和元数据导出；检查凭证行未丢。该测试不执行排队的 observe/media、不用真实插件程序、不导出媒体，亦无 Vault 秘密哨兵，不能扩大为完整媒体采集/秘密隔离的动态证明。代码未发现基础采集受四类开关控制或导出凭证的路径，不据测试范围有限新增一个未证实的功能缺陷 |

### 13.4 本轮实际命令、反例与证据归属

下列测试只使用隔离合成 Profile、合成插件/工具及内存数据库；本轮零新增真实站点/真实更新请求。

| 命令 / 核查 | 退出码与结果 |
| --- | --- |
| 修改前 `python scripts/stage.py check`；文档/公开/结构/生成类型/规则检查 | 各 0；原冻结输入一致 |
| `python -m unittest discover -s scripts/tests` | 0：47 项 |
| `python scripts/check_package.py dist/trace-0.1.0-windows-x64-13` | 0：2 项，含嵌入式 WebView 关闭/重连 |
| `corepack pnpm -C apps/desktop run verify` | 0：141 项、格式/类型/生产构建 |
| 修改前 `cargo test -p trace-core --locked -- --test-threads=1` | 0：421 项、0 failed/ignored，991.99 s；未修改产品输入时编译的原基线 |
| 独立 Python 驱动/SQLite/原始基准复算 | 0；F-071 假阳性、F-084 篡改/缺包漏检及时间比较均已构造，不向真实站点发请求 |
| `cargo test -p trace-core --locked a_review_ -- --nocapture --test-threads=1` | 101：9/9 按预期断言失败；F-069 三项、F-072 一项、F-073 两项及局部 F-085 一项/F-086 两项。先前并行编译曾因 Windows 正在运行原测试 exe 而报 LNK1104（101），原进程结束后重跑才得到这组语义结果；两次日志均保留 |
| 修复后 `cargo test -p trace-core --locked media_tools::tests::` / `replay_tests::` / `tool_update::`（均单线程） | 各 0：24 + 6 + 12 = 42 项，含三条 red→green 新回归与两次真正重开 Core；MSVC linker_messages warning 如实保留 |
| `cargo clippy -p trace-core --locked --all-targets -- -D warnings` | 0 |
| 修复后 `python scripts/stage.py check` | 1：源码、command、package、benchmark/raw inputs stale；与本轮五个产品输入文件变更一致，未生成新报告或改旧哈希 |
| 最终文档/公开/结构/类型/尺寸/长行/规则/脚本与 fmt/diff 检查 | 各 0：52 文档、816 本地链接，0 公开发现/超限文件/可折长行；生成类型一致，规则记录一致；脚本 47 项复跑通过 |

未提交原始证据入口（基线证据和失败编译/失败反例分别保存，不覆盖成成功）：

```text
artifacts/evidence-runs/a-m2-r5-review/audit.log
artifacts/evidence-runs/a-m2-r5-review/adversarial-script.log
artifacts/evidence-runs/a-m2-r5-review/diagnostic-probes.patch
artifacts/evidence-runs/a-m2-r5-review/adversarial-tests.log
artifacts/evidence-runs/a-m2-r5-review/adversarial-tests-rerun.log
artifacts/evidence-runs/a-m2-r5-review/fixed-regressions.log
artifacts/evidence-runs/a-m2-r5-review/clippy.log
artifacts/evidence-runs/m2-r5b/
artifacts/evidence-runs/m2-r5b-bench-failed/
```

八站订阅继续全部 partial，implemented 六条规则及真实预算不变；原真实回执仍属于 `-7`～`-10`，不改绑 `-13`。ACT-003、真实 yt-dlp 更新、Linux、安装器、签名/发布仍 not-run。本轮改动不扩大网站授权，不增加用户阻塞；未 push、未创建 PR、未合并、未发布。

### 13.5 A → B 集中返工 prompt

```text
请作为 Agent B 在 codex/m2-multi-site-archive 连续完成 TRACE-M2-01 r2 的再次集中返工。被审提交为 4e8a8a1988334beb1b8eeb42d140e79e959a74f8；当前基点为 A 保存本报告与局部修复后的 HEAD（以 git log -1 为准），仍只有一个阶段提交。
先读 AGENTS.md、REVIEW.md、stage-plan、stage-report §13、当前状态及 USER-ACTIONS；§11 是 B 上轮自检，不是通过依据。
保留 A 的 F-085 回放时刻比较、F-086 许可后选择工具及对应回归、F-070 真正重开 Core 的回退测试，也保留此前全部修复和断言。
一次性完成 §13.2 的六项：F-069 双 owner 与跨 scan-lane 持久拒绝，F-071 真实详情执行与订阅发现证据，F-072 同事务 token 校验与关系写入，F-073 可恢复删除意图/非阻断启动/activate-remove 并发，F-079 只读覆盖质量与独有区间展示/受约束决定，F-084 实际成员与缺包验证。
先从本机保存的 diagnostic-probes.patch 恢复相应反例，用明确屏障而不是碰运气 sleep 证明修复；成功重复详情不增加 jobs.attempts，不可改变或伪造该字段语义制造通过。保留正常 AuthRequired 恢复、另一个消费者进度、显式重试、档案/凭证/外部文件/固定版本边界。
最终代码冻结后重跑必需自动检查、包、原规模/时长长测及 report/check；本轮 A 修改使旧 gate stale，原 -13/测试/长测只属原输入，不手改指纹或重绑旧证据，失败日志也保留。
本轮不追加真实网站或真实更新请求，不为补证据再次试探；八站订阅 partial、原真实回执归属和预算、ACT-003 not-run 保持。不是人工验收问题，不等待用户决定，也不申请 push/PR/合并。
在唯一报告记录原编号的实际输入、结果、修复位置、命令/退出码和证据限制，更新当前状态与必要契约，amend 进同一个带 DCO 的阶段提交；交付准确 SHA、包摘要、原始证据入口和 B→A 复验 prompt。远端操作仍须 A 收敛后在 USER-ACTIONS 单独请求授权。
```

**实现交付：未完成；A 技术审查：rework-required；产品验收：not-run。** 首选下一步是 B 按上面的同一批必须项返工，不转回用户人工排错。

<a id="b-m2-rework-r6"></a>
## 14. B 第二次集中返工（2026-10-19）

B 从 `81d17f887ac451f4b3e265988cb70faa26eb879d` 开始，按 §13.2/§13.5 处理六项剩余缺口。开工时分支为 `codex/m2-multi-site-archive`、工作区干净；先恢复 A 保存的 `artifacts/evidence-runs/a-m2-r5-review/diagnostic-probes.patch`，用其中的 actor 屏障、双 consumer、三 scan lane 和 Windows 文件占用反例得到 6 个预期失败，再把断言迁入按能力命名的正式回归。A 的 F-085/F-086 正式回归、F-070 重开 Core 回退测试及原失败日志均保留。

### 14.1 六项修复与定向证据

| 编号 / 状态 | 实现与实际行为 | 回归 |
| --- | --- | --- |
| F-069 / 已修待 A 复核 | `jobs/consumers.rs` 把拒绝标记保存在具体 consumer，并把同一订阅的独立扫描 lane 一起停到 `waiting_user`；`jobs/mod.rs` 在失败事务中扩散停止。`store/credentials.rs`、`upgrades/schedule.rs`、`store/subscriptions.rs`、`scan_range.rs`、`accounts.rs`、`app/jobs.rs` 的凭证唤醒、补全、质量/凭证重排、范围变更、history/deep lane 恢复、account profile、`check_now`、启用和重启路径都不复活该状态。另一个 owner 仍可 claim 共享 job；显式 `job.retry` 才清理对应停止并恢复。 | `site_refusal_is_scoped_to_one_consumer_and_needs_explicit_retry`、`site_refusal_stops_all_scan_lanes_until_an_explicit_retry`、`range_and_quality_changes_do_not_reopen_a_refused_subscription`、`enabling_a_refused_subscription_does_not_create_a_fresh_scan_lane`、`permission_refusal_is_only_retried_by_an_explicit_user_action`；既有 `shared_auth_failure_and_credential_change_do_not_cross_consumers` 保持 AuthRequired 正常唤醒 |
| F-071 / 已修待 A 复核 | `scripts/real_site_check.py` 不再读取 `jobs.attempts` 作为详情成功计数，改用 `archive.entry.get` 的 `discovered_by_subscriptions`、`observation_count`、`latest_observation_seq` 与 job身份快照；第二轮核对单一重复 list endpoint、零新增 observe/media job、零媒体字节。自动下载关闭要求目标与所有发现条目 `deferred`，开启要求本订阅发现关系、目标媒体 job 和实际媒体字节。预览或其他 lane 的入库不能冒充本订阅发现。 | `test_an_incremental_check_verifies_range_detail_attempts_and_no_media_on_a_second_pass`（成功、范围外、范围外错误发现、其他 lane、观察增长、endpoint 增长、新 job、自动下载开关正反例）；`test_an_incremental_check_creates_the_subscription_for_one_instant_pauses_it_and_stops_on_a_challenge`、`test_an_undated_list_takes_the_instant_from_the_works_own_page_without_downloading_it` |
| F-072 / 已修待 A 复核 | `app/identity.rs` 的影响范围、账号清单和 token 由同一组同步函数在预览只读事务与确认写事务内计算。merge/split/relink 均在 `db.write` 内重算、比较 token、执行关系变更和审计；不一致返回 `VersionConflict` 且不写关系。 | `merge_confirmation_recomputes_its_scope_inside_the_write_transaction`、`split_and_relink_tokens_are_revalidated_with_their_writes`、`identity_previews_show_scope_and_a_changed_binding_invalidates_the_confirmation` |
| F-073 / 已修待 A 复核 | `store/plugin_releases.rs` 用稳定的 `plugin_remove_intent` 管理操作保存持久移除意图，`plugin.releases` 将其呈现为 `removing`；`plugin_install.rs` 依次改名到 tombstone、删目录、在同一 DB 事务完成记录清理。启动恢复对损坏/缺失 tombstone 只告警并保留可重试状态，不阻断 Core；目录已删而记录未删时会在下次启动清算。activation、rollback 与 remove 共用生命周期锁，removing 版本不能激活。 | `a_locked_plugin_tombstone_does_not_block_archive_startup`（Windows 显式 `share_mode(0)`，重开 Core 后档案可读、`removing` 保留、释放后可重试）、`removal_reconciles_after_files_are_gone_but_the_record_remains`、`a_removal_interrupted_after_the_directory_is_gone_is_reconciled_at_startup`、`a_failed_plugin_removal_stays_retryable_and_keeps_the_archive`、`activation_and_removal_of_the_same_release_are_mutually_exclusive` |
| F-079 / 已修待 A 复核 | `replays::preview` 在候选查询阶段返回只读覆盖、真实/独有区间、质量、`can_replay_primary` 和不可比较原因；`ManagementView.vue` 候选与已有关联都持续渲染这些字段。候选先“查看比较”，取消零写入；只有完整覆盖且质量可比较才启用“设回放为主”，Core 仍做最终拒绝。 | `the_first_relation_can_be_linked_or_rejected_from_zero` 增补只读候选、独有区间、质量和主版本约束；`clipped_replay_keeps_recorded_ranges`、`incomparable_quality_keeps_both`；管理页 `management_view_confirms_previews_and_routes_compare_replay_and_quality_actions` 覆盖比较、禁用、取消、确认与已有关联展示 |
| F-084 / 已修待 A 复核 | `package_identity` 与 `verify_package_identity` 现在逐项解析 `SHA256SUMS.txt`，核对实际成员摘要、缺失和额外文件，复算 manifest 的 `sourceInputs`/`source_files_sha256`/`source_tree_sha256` 关联；包目录缺失直接失败。旧回执无包身份时返回空错误并保留原归属。 | `test_a_new_receipt_is_bound_to_its_package_digest_and_source_tree`：有效包、篡改二进制但清单不动、额外成员、缺失成员、缺包、源码树不一致 |

### 14.2 冻结测试、包与正式长测

冻结产品输入共 **770 项**，源码树 `9f9d8aadf6498639bcaaecaf98ab65f4f18dacd577a19080b52d493cb5a52938`；测试、包、长测和最终 report 共用这一输入。正式跑次为 `artifacts/evidence-runs/m2-r6b/`（本地忽略目录）。

| 检查 | 结果 |
| --- | --- |
| `python scripts/stage.py test --run m2-r6b` | 0：`rust-build` 0；`cargo test --workspace --locked` 0，899 项，327.92 秒；前端 141 项；脚本 47 项 |
| `cargo clippy --workspace --locked --all-targets -- -D warnings`、`cargo fmt --all -- --check` | 各 0 |
| `python scripts/check_structure.py`、`check_generated_types.py`、`check_publication.py`、`lint_lines.py` | 各 0；生成类型一致，0 公开发现、0 超限文件/可折长行 |
| `python scripts/stage.py package --run m2-r6b` | 0：`dist/trace-0.1.0-windows-x64-15/`，37 个文件、342,224,342 字节；`SHA256SUMS.txt` 自身 SHA-256 `30BEE10304310DEBE8F3938CAD1623CD1A39AAAA1ADD294CABE1B637446A4A56`；包检查 2 项通过（含真实嵌入式 WebView 关闭/重连） |
| `python scripts/stage.py bench --run m2-r6b --idle 1030 --seconds 3620 --entries 100000` | 0：正式样本窗 3621.180 秒、idle 窗口 910.031 秒；100,000 条目 / 500,000 媒体元数据；idle CPU 0.261095%、内存 P95 52.027 MiB；四路录制 CPU 2.443448%、内存 P95 67.738 MiB；热查询 P95 289.278 ms、冷查询 P95 130.744 ms、资源管理器热/冷 P95 221.048/103.914 ms；启动 1.141 s、恢复 2.140 s、GUI 关闭 2.991 s；2 路下载、4 路录制、4 次开始、0 job error；原始样本 SHA-256 `6D03AFF177E8CA66246FBDFF700E4CDEFBA24D8663D39CC4ECDA242217BF503F` |
| `python scripts/stage.py report --run m2-r6b`、`python scripts/stage.py check` | 各 0；公开摘要写入 `docs/evidence/gate/report.json`，自动证据通过，A review pending，产品验收 not-run |

manifest 的 `source_tree_sha256` 与测试输入树一致，schema 8、控制协议 0.1、`productAcceptance=not-run`。manifest 的 `git_commit` 是构建时的阶段基点 `81d17f8` 且 `source_dirty=true`；包由本报告对应的工作树构建，最终 amend 只把这些输入写入提交元数据，不能把该字段误读为最终提交号。

### 14.3 证据归属与限制

- A 的 `a-m2-r5-review` 原始日志、失败反例和 `diagnostic-probes.patch` 原样保留；B 新增的 `m2-r6` 旧跑次和 `m2-r6b` 最终跑次均不覆盖原跑次。
- 原 `-13` 包、原测试和原一小时长测继续只属于原输入。交付前审计又收紧了范围/质量/凭证重排、history/deep lane 恢复、account profile 与混合 waiting_auth，因此早期 `-14`/`m2-r6` 也成为旧输入并原样保留；本轮只以 `-15`/`m2-r6b` 作为最终证据。
- 本轮零新增真实站点、真实更新或真实媒体请求；八站订阅规则仍全部 `partial`，原有真实回执仍属于 `-7`～`-10`，预算不变。
- ACT-003、真实 yt-dlp 更新、Linux、安装器、签名与发布仍未验证；自动证据不表示 A 审查或产品验收通过。

### 14.4 B → A 复验 prompt

```text
请作为 Agent A 复核 TRACE-M2-01 r2 的 §14 集中返工。分支 codex/m2-multi-site-archive；交付 SHA 以 git log -1 --format=%H 为准；工作区应干净。先读 AGENTS.md、REVIEW.md、docs/delivery/status.md、USER-ACTIONS.md、docs/delivery/stage-report.md §13～§14，再核对实际代码、测试和 m2-r6b 原始回执。

本轮已按 §13.2/§13.5 完成 F-069/F-071/F-072/F-073/F-079/F-084，并保留 A 的 F-085 回放时刻比较、F-086 许可后选择工具、F-070 重开 Core 回退测试及原失败证据。请重点复验：
1. F-069：拒绝是否按具体 consumer/订阅持久停止，双 owner 的另一个 owner 不被饿死，incremental/history/deep 三 lane 同时停止；invalid→valid、valid-only、重启和质量/补全重排均不复活，显式 retry 才恢复对应请求；AuthRequired 正常恢复保持。
2. F-071：增量驱动是否使用真实 source observation、entry_subscriptions 和 job身份/endpoint 证据；范围外零订阅发现与媒体、第二轮零详情请求/新任务，预览或其他 lane 不得冒充；自动下载开/关正反例均成立。
3. F-072：merge/split/relink 是否在同一写事务内重算影响、比较 token、写关系与审计；actor 屏障下不移动预览外账号，陈旧确认零写入。
4. F-073：tombstone 文件占用、trash 已删/DB 未删、再次移除和并发 activate/remove 是否保持档案可读、可重试、活动驱动不受损；cleanup 告警不得阻断 Core。
5. F-079：UI→API→Core 是否先给出只读覆盖/质量/真实与独有区间，取消零写入，不可比较/剪辑缺段不能设回放为主，已有关联持续显示保存区间，全部分片保留。
6. F-084：新回执是否逐项核对实际包成员、缺失/额外文件、篡改摘要和 manifest 源码树；缺包失败，旧无身份回执不换绑。

证据入口：docs/delivery/stage-report.md §14；artifacts/evidence-runs/m2-r6b/（test/package/bench/report 回执与原始日志）；artifacts/evidence-runs/a-m2-r5-review/（A 原失败证据）；包 dist/trace-0.1.0-windows-x64-15/，SHA256SUMS 自身摘要 30BEE10304310DEBE8F3938CAD1623CD1A39AAAA1ADD294CABE1B637446A4A56。冻结输入 770 项、源码树 9f9d8aadf6498639bcaaecaf98ab65f4f18dacd577a19080b52d493cb5a52938；正式长测和 report/check 均已通过。

请复用有效证据并做必要独立复验，不重复全部一小时长测；局部明确问题直接修复，较大缺口按稳定 F 编号一次性交回 B。不要追加真实站点/真实更新请求，不把原 -13 回执或长测改绑到 -14，不把自动证据写成产品验收。外部操作仍按已有授权；本轮未 push、未建 PR、未合并。
```

**实现交付：B 已完成并冻结；A 技术审查：待复验；产品验收：not-run。**

<a id="a-m2-r6-review"></a>
## 15. A 独立集中复核与新增交互要求（2026-10-20）

被审交付为 `d3561dd091845bac66e99d2a4e1b4abde59a47ae`，接手核实分支 `codex/m2-multi-site-archive`、工作区干净、仍为一个阶段提交。先审实际生产代码、UI/API、测试和原始回执，再补必要独立反例；没有再跑一小时基准，没有追加真实站点/真实更新请求。**A 技术审查仍为 rework-required**：F-071 的证据判断仍有必须缺口；F-069 新发现的局部路径已直接补修。用户新增的沉浸式标题栏/四档文字与响应式布局进入计划，不冒充本次已实现。

### 15.1 必须返工：F-071 / P1

位置：`scripts/real_site_check.py` 的 `job_snapshot/new_jobs/check_incremental`。独立合成驱动反例沿用已有测试夹具，仅改变输入/返回的可观察事实，不联网、不改 `jobs.attempts`。`artifacts/evidence-runs/a-m2-r6-review/driver-probes.py` 与 `.log` 保留完整输入和结果。

| 触发与实际结果 | 影响及完成条件 |
| --- | --- |
| `archive.entry.get` 不含 observation_count/latest_observation_seq；两次 `.get()` 均为 None，仍返回 ok=true、observations unchanged | 证据缺失不是零详情执行。必须要求可验证的实际观察字段/类型/值，并在缺失或不完整快照时明确不可验证 |
| 首轮之前已有范围外条目 `old`，本次订阅错误地发现它；目标条目正常发现。由于仅核对 timeline 新 ID 差集，仍 ok=true，且不读取 old 的发现关系 | 必须比较本订阅全部相关发现关系的增量，覆盖已在库/预览/其它 lane 的条目；范围外零发现与零媒体不能只看新增条目 |
| 目标有本订阅发现关系，但新 media job 是其它订阅/lane 的 failed job；目标 complete 与字节来自其它采集，仍 ok=true | 现有 JobDto/snapshot 只凭 subject ID，无法证明谁执行/成功。需要安全的 subscription/consumer/request/成功执行归属证据及 endpoint/媒体事实；不能用任意 job 存在+全局媒体字节归因给本订阅 |
| 自动下载开启分支首轮下载后立即 return，subscription.check_now 调用数为 0 | 开/关都必须执行第二轮，核对重复 list、零详情/媒体执行、零新任务和发现关系不变；加入 auto-on 第二轮重复详情/媒体的负例，不只测试 off |

以上是**验证器假阳性与覆盖缺口**，没有据此宣称生产 Core 已发生对应错误下载或错误发现。此前 F-071 的 observation/关系/endpoint 修复方向正确，但尚不足以关闭原必须项。由 B 在同一批完成订阅归属与范围快照（包括必要的安全 API/DTO）、驱动与正反例，再交 A；不是交给用户做真实测试，也不消耗新预算。

### 15.2 A 直接修复与已核实项

| 编号 / 本轮结论 | 核查、修复与边界 |
| --- | --- |
| F-069 / 局部补修后关闭已复验路径 | 新增两个真实 DB actor/claim 反例：仅有 history 拒绝时 check_now 插入缺失 incremental，实际可 claim；范围改为 from_now 时 cancel_history_lane 抹去最后一个拒绝状态。原断言均失败，日志 `refusal-red.log` 保留。`store/subscriptions.rs` 现在先检查订阅拒绝，check_now 明确拒绝、范围更新不重排扫描、自动取消 history 不覆盖 waiting_user；拒绝 error 也在显式取消后继续作为持久标记，只有 job.retry 清除。`jobs/consumers.rs`、`store/credentials.rs`、`scan_range.rs` 的相关查询保持同一语义。新增三条正式回归，包含取消后自动路径仍停止、显式 retry 正常恢复；原双 owner、三 lane、invalid→valid/valid-only、history/deep/account-profile、重启与 AuthRequired 守护保留 |
| F-072 / 关闭 | merge/split/relink 在同一个 db.write IMMEDIATE 事务重算影响和 token、写关系和审计；出错回滚。独立复跑 identity_tests 14 项，包含 actor 屏障下预览外账号不迁移、陈旧 split/relink 不写关系/审计。屏障中确认事务先排队时只迁预览内账号，不将后排绑定写入误称为陈旧确认 |
| F-073 / 关闭 | 持久 remove intent、文件改名/删除/DB 收尾与可重试 removing 状态正确，activate/remove 共用生命周期锁。独立 plugin_tests 13 项通过，Windows share_mode(0) 实际告警错误 32 而 Core 重开成功/档案可读；trash 已删而 DB 未删可清算、再次移除与并发动作保持活动目录。告警不是“删除完成” |
| F-079 / 关闭 | 候选只读返回覆盖/规格/真实与独有区间、质量和 can_replay_primary；UI 比较选择与取消先不写，已有关联持续展示 saved ranges。独立 replay_tests 6 项及前端完整 141 项通过，包含从零关联/拒绝、剪辑缺段/不可比较拒设回放主、全部分片保留、取消零写入；仍不代替原生体验 |
| F-084 / 关闭 | 新回执逐项核对实际成员/缺失/额外/摘要、manifest 输入与源码树；缺包失败、旧无身份回执保持原归属。独立脚本 47 项（含篡改反例）通过；原 -15 实物 package_problems 返回空错误、身份与报告一致。旧回执无包身份时不补写为 -15 |
| F-070 / F-085 / F-086 / 保持 | 真正重开 Core 的回退链、回放真实时刻比较、拿许可后选择程序路径的修复与原失败证据保留。独立 tool_update 12、media_tools 24、replay 6 项通过，排队启动的合成子进程不是联网下载证据 |

### 15.3 原始证据独立复核与实际命令

修改产品输入之前 `python scripts/stage.py check` 为 0。逐个读 `m2-r6b/receipts/*.json`，校验原始日志摘要、退出码、sourceUnchanged 和 770 项输入；Rust 899（327.92 秒）、前端 141、脚本 47、包 2 项是原回执核对结果，不虚称又执行 899 项。

从 **934 个连续原始资源样本**重新调用 `benchmark_metrics.derive`，逐字段与原 benchmark 完全一致：3621.180 秒、idle 910.031 秒、100,000 条/500,000 媒体元数据；idle CPU 0.261095%、内存 P95 52.027 MiB；四录制 CPU 2.443448%、内存 P95 67.738 MiB；热查询 289.278 ms、资源管理器热查询 221.048 ms；启动 1.141 s、恢复 2.140 s、GUI 关闭 2.991 s。原 bench 命令总耗时 4726.61 秒（约 78.8 分钟），含空闲、准备与连续负载，不是单元测试耗时。

原 -15 包实物身份独立通过：37 文件、342,224,342 字节；SHA256SUMS 自身摘要 `30BEE10304310DEBE8F3938CAD1623CD1A39AAAA1ADD294CABE1B637446A4A56`，源码树 `9f9d8aadf6498639bcaaecaf98ab65f4f18dacd577a19080b52d493cb5a52938`。初次核对结果在 `evidence-audit.log`，没有重写包、清单或旧回执。

| 本轮实际执行 | 结果与限制 |
| --- | --- |
| 原输入定向 Rust：consumers / management / identity / plugin / replay / tool_update / media_tools | 8 + 8 + 14 + 13 + 6 + 12 + 24 = 85 项执行通过；不是 workspace 全量。曾用 plugin_lifecycle 过滤得到 0 项，随后改用实际 plugin_tests 运行 13 项，不把空过滤算覆盖 |
| `corepack pnpm -C apps/desktop run verify` | 0：141 项，格式、类型、生产构建通过；已有 Vue Router 测试警告保留 |
| 原 -15 的隔离原生回放 UI→API→Core（`native-replay-v2.py` / `replay-native.mjs`） | 0：实际最小化、不抢前台的生产 WebView 点击查候选/查看比较/取消/保留两者/拒绝/设主，真实 IPC/控制协议入原包 Core。取消前后 management.operations 和 links 相同；剪辑/未知质量禁用主版本、完整同规格可设主；3 个合成分片记录均保留、已保存区间持续渲染。首个探针合成规格漏 fps，完整候选被正确判 unknown 而断言失败，原日志保留；补齐合成 fps 后在新隔离 Profile 复跑通过，不算产品缺陷或真实媒体播放验收 |
| `python -m unittest discover -s scripts/tests -v` | 0：47 项；独立额外 driver-probes 的四种假阳性不在原 47 项内，原测试全绿不关闭 F-071 |
| 新增两个拒绝反例 `cargo test -p trace-core --locked jobs::consumers::tests::a_ -- --nocapture --test-threads=1` | 101：2/2 预期失败，完整保留 `refusal-red.log`；第二个断言在 actor 内触发，调用方同时收到 actor dropped job，不能误读为仅环境失败 |
| 局部修复后 consumers 模块；credential / subscription / scan_range 过滤 | 各 0：11 项（1.55 秒）和 16 + 10 + 2 项通过，有重叠不相加宣称独立用例数；最后补显式 retry 断言后再次完整运行 consumers |
| `cargo clippy -p trace-core --locked --all-targets -- -D warnings`、fmt/diff | 0；MSVC linker_messages 与一次 incremental cache 目录拒绝访问提示保留，没有把提示当测试失败或隐去 |
| `python scripts/check_package.py dist/trace-0.1.0-windows-x64-15` | 在新增反例已改变产品输入后执行，1：实物/源码核对明确报 manifest/aggregate/build-command stale；独立嵌入 WebView 关闭/重连用例通过。没有把这个原包的原生烟测写成修复后包通过 |
| 修复后 `python scripts/stage.py check` | 1：source、command、package、benchmark/raw inputs stale，符合五个产品输入改变。保留原 gate/包/长测，不生成假新 report；B 最后冻结后补匹配的新证据 |
| 最终文档/公开/结构/生成类型/长行/规则/脚本与 fmt/diff | 各 0：52 文档、834 本地链接，0 公开发现、0 超限文件/可折长行，生成类型与规则一致；脚本47项复跑通过。对原 report 显式使用其原 sourceInputs 再校验也为0，并记录 currentInputsDiffer=true，只证明原冻结回执仍完整，不替当前 stale 检查 |

本轮局部修复及 LF 行尾规范化后仍为 770 项，五个改变的输入是 `jobs/consumer_tests.rs`、`jobs/consumers.rs`、`scan_range.rs`、`store/credentials.rs`、`store/subscriptions.rs`；源码树 `5cc06b5abe6338fb53f4ebf98fe662bf2d86a04a5eecf4a38a6b13dd7c40efa4`。报告/需求/计划变化不进产品指纹。本轮原始日志均在 `artifacts/evidence-runs/a-m2-r6-review/`，原 `a-m2-r5-review`、`m2-r6b` 和旧失败原样保留。

### 15.4 新增范围与长测安排

[DEC-042](../decisions/confirmed-decisions.md#dec-042) 和[计划 §13](stage-plan.md#desktop-and-fast-validation)纳入 U1 沉浸式标题栏、U2 四档文字与响应式、V1 分层快验；**本轮 A 只完成方案与验收条件，UI 尚未实现**。开源调查沿用现有 Tauri/Vue/Naive UI，未加依赖或更换技术栈，官方依据与查询日期已落盘。

日常改动跑秒至分钟的受影响测试；本轮 A 审查复算旧基准而没有另跑长测。原 78.8 分钟长测用于稳定空闲/连续并发的资源、十万级查询与 GUI 生命周期，不证明网站抓取或产品验收。后续先完成整批返工/新增 UI，再最后冻结集中跑一次需要的新包/正式基准；不在每个工作包和日常 A 复核中重复。短测不能冒充连续一小时，旧输入不能改绑；详见[分层门禁](../dev-rules/quality-gates.md#risk-layered-validation)。

八站订阅仍全部 partial，原 -13/-14/-15 与真实 -7～-10 各属原输入；没有追加真实站点或真实更新请求。ACT-003、真实更新下载、Linux、安装器、签名/发布仍 not-run；未 push、未建 PR、未合并。不新增需用户作决定的阻塞项；USER-ACTIONS 仅同步 DEC-042 已收到、ACT-003 适用包限制，未将开发工作转成用户待办。

### 15.5 A → B 集中实施 prompt

```text
请作为 Agent B 在 codex/m2-multi-site-archive 连续完成 TRACE-M2-01 r2 的本次集中实施。被审交付 d3561dd091845bac66e99d2a4e1b4abde59a47ae；当前基点以 A 保存 §15 后 git log -1 为准，仍只有一个未 push 的带 DCO 阶段提交。
先读 AGENTS/REVIEW、stage-report §15、stage-plan §13、状态与 USER-ACTIONS。保留 A 本轮 F-069 的五个产品输入修改和三条新回归（check_now 缺 lane、from_now/history 拒绝标记、取消后仍拒绝且显式 retry 恢复），以及 F-070/F-085/F-086、原失败/回执/包身份。
一次性完成 F-071：严格 observation 证据、全部相关订阅发现关系（含已在库范围外条目）、目标 subscription/consumer/request 的成功 observe/media 执行与 endpoint 归属；auto-on/off 都做第二轮零详情/媒体/新任务检查。把 a-m2-r6-review/driver-probes.py 的四种假阳性迁入正式回归并保留所有正例，不改 jobs.attempts 语义；安全 DTO 不泄露凭证/临时 URL。
同批实现 DEC-042：沉浸式标题栏（真实窗口动作、拖动/最大化/resize、浏览器不显示伪控件、关闭不退出后台）；设置 100/110/125/150% 字号（默认100%、即时且持久、非法/存储失败可恢复）；全界面和 Naive UI 浮层随字重排，按计划视口/页面矩阵自动验证，不用 transform 固定画布。按官方研究复用已有依赖，不换技术栈。
落实 V1 分层快验，日常不触发一小时基准/真实请求。先完成本批全部源码和测试，再最后冻结统一测试/打包与必要正式长测；不能用旧 -15 绑定新输入，也不能让短测通过正式 report/check。原始失败保留，当前 gate stale 是真实状态，不手改摘要。
不追加真实站点或真实更新请求，八站订阅 partial、预算、ACT-003 not-run 保持；用户事项只在 USER-ACTIONS。记录实际命令/退出码/耗时、匹配包与源码树、证据限制，amend 同一带 DCO 阶段提交并交 A 复核。未获授权不得 push/PR/合并/发布。
```

**实现交付：需补 F-071 与新增范围；A 技术审查：rework-required；产品验收：not-run。** 首选下一步交 B 按同一 prompt 连续完成，不把原生人工检查当开发前提，也不请求远端操作授权。

<a id="b-m2-r7-delivery"></a>
## 16. B 集中实施与最终冻结（2026-10-20）

B 以 A 保存 §15 后的 `fe5a596` 工作树继续，保留 F-069 的五个产品输入修复、三条回归，F-070/F-085/F-086 的修复及所有原失败/回执/包身份。本轮没有追加真实站点、真实更新或远端请求；八站订阅保持 `partial`，ACT-003 仍 `not-run`。

### 16.1 F-071：订阅归属与可验证的增量证据

| 项目 | 实际实现与测试 |
| --- | --- |
| 安全证据 DTO | `archive.subscription.evidence` 继续只返回本地身份、时间、状态和脱敏 endpoint；观察现在按 `acquisition_contexts.subscription_id` 过滤，消费者 job 增加 `job_generation`。请求仍以 `context_id`/`evidence_id` 归属，不读取 `jobs.attempts`，不返回 payload、查询串、Cookie 或临时 URL。`crates/core/src/archive_catalog_tests.rs` 的 `subscription_evidence_exposes_owned_facts_without_job_payloads_or_urls` 与 `subscription_evidence_does_not_borrow_another_subscriptions_job_or_observation` 覆盖脱敏、观察/任务归属和范围外订阅隔离 |
| 严格快照校验 | `scripts/real_site_check.py::evidence_problem` 要求 `budget_available=true`、无截断、唯一 entry、完整 relation/discovery、observation 计数与最新序号一致、context/job/request/acquisition 类型和归属完整；缺失字段不再被当作零活动。请求必须属于现有 context，媒体字节必须属于本轮 context |
| 首轮发现与执行 | 目标必须由本次检查的 `entry_subscriptions` 发现、落在请求范围、观察到新序号；observe job 必须同时是 succeeded consumer 且有新 acquisition context + job generation，详情请求必须来自本轮新请求且不是 list endpoint。范围外已有条目、错误关系、其它 lane 或失败/global job 不能通过 |
| 自动下载两档 | auto-off 要求所有发现条目 `deferred`、无新增 media job/acquisition/媒体字节；auto-on 要求目标有本订阅的 succeeded media consumer、新 context、新媒体请求、实际新增媒体字节和成功 acquisition。两者都执行第二轮 |
| 第二轮 | 关系（含 discovery 时间）、观察明细、context、job 状态/计数/generation、acquisition、媒体证据均逐项不变；只允许一个重复 list endpoint，且不新增详情、媒体或任务。`changed_jobs` 以证据内的 job 状态/requested 计数判断，不再用全局 `job.list` 或 `jobs.attempts` 归因 |
| 回归 | `test_an_incremental_check_verifies_range_detail_attempts_and_no_media_on_a_second_pass`、`test_incremental_evidence_rejects_missing_observations_existing_outside_discovery_and_foreign_media`、`test_a_failed_media_job_cannot_back_an_incremental_download`、`test_auto_on_also_rejects_a_second_detail_job`、既有正例和无日期驱动均通过。A 的 `artifacts/evidence-runs/a-m2-r6-review/driver-probes.py`/`.log` 原样保留；其四种假阳性语义已迁入上述能力命名的正式回归 |

### 16.2 U1：沉浸式标题栏

- `apps/desktop/src-tauri/tauri.conf.json` 使用 `decorations=false`、可调整大小和原生窗口动作；`WindowTitlebar.vue` 提供拖动区域、真实最小化/最大化/还原/关闭按钮、状态图标、键盘焦点与可见错误。搜索、导航和按钮不在拖动区；浏览器预览不渲染伪原生控件。
- `commands.rs` 对窗口动作增加 `main` label 限制；关闭仍只关闭当前 GUI，不调用 `desktop_exit`，后台任务由独立 Core 保持。嵌入式 WebView 检查实际点击关闭/重开并确认后台继续；另验证 `desktop_window_toggle_maximize` 的状态往返。`--show-status` 改为页面加载后延迟导航，避免 router 初始化覆盖通知入口。
- 包内自动检查 `test_minimized_embedded_webview_close_reconnect_and_full_exit` 通过，包含最小化、不抢前台、关闭、重连、完全退出、其它窗口不重启已退出后台；自动检查不替代 ACT-003 的真实 IME/系统缩放/主观体验。

### 16.3 U2：四档字号与响应式

- `stores/display.ts` 提供 100/110/125/150 四档、默认 100%、`localStorage` 持久化、非法值/读写失败恢复和 `storage` + `BroadcastChannel` 多窗口同步；`SettingsDisplay.vue` 放在“设置 → 显示与文字”，说明这是本机显示偏好，不进入档案/备份。
- `App.vue` 的 Naive UI theme overrides 与 `shell.css` 根字号 token 同步缩放按钮、卡片、表格、弹窗、下拉和浮层；页面 CSS 使用相对字号/间距、可换行网格和独立滚动区。所有固定宽度弹窗改为 `min(px, 94vw)`，窄窗工具栏允许换行。没有使用固定画布 `transform`。
- 调试版与最终包内的自动矩阵共 **119** 个观察点：720/980/1280/1536 宽度 × 100/110/125/150% × 设置/主页/订阅/时间线/资源管理器/任务/管理页，另加 360 宽七个页面、150% 模态框与 Naive UI 浮层。根部实际计算字号为 16/17.6/20/24px；页面/浮层无横向溢出、底部可滚动到达、模态框和下拉不越界。真实 IME、Windows 系统缩放和主观阅读体验仍由 ACT-003 记录。

### 16.4 V1：分层快验

`scripts/quick_verify.py` 提供 `ui`、`backend`、`evidence`、`structure`、`all` 五组，输出目录带纳秒后缀避免并发碰撞，摘要固定含 `QUICK ONLY`、`completeStageEvidence=false`、`longBenchmark=false`、`realSiteRequests=0`。`--rust-test` 为 backend 组添加能力命名的定向 Rust 过滤，替代默认的 cargo check；失败立即停止，摘要保留原命令退出码，CLI 返回 1，不删除断言、不创建 `stage.py` 回执。实际全组本机耗时 **65.16 秒**（10 条命令），不含长基准、真实站点、打包或正式 report。

### 16.5 最终冻结与匹配证据

| 命令 / 输入 | 结果 |
| --- | --- |
| `python scripts/stage.py test --run m2-r7e` | 退出 0；Rust 工作区构建 8.88 秒，Rust 305.15 秒/905 项，前端 9.68 秒/148 项，脚本 37.21 秒/53 项 |
| `python scripts/stage.py package --run m2-r7e` | 退出 0；`package/portable` 41.46 秒，`package/check` 57.61 秒/2 项（成员/摘要、八站隔离、嵌入式 WebView 关闭重连与状态入口） |
| `python scripts/stage.py bench --run m2-r7e` | 退出 0；正式命令 4,709.65 秒；工作负载 3,601.18 秒，idle 窗口 905.187 秒；100k 发布/500k 媒体/100k 资源管理器 |
| 基准指标 | idle CPU 0.3044%、内存 P95 52.984 MiB；四路录制 CPU 1.7082%、内存 P95 67.180 MiB；热查询 P95 140.732 ms、资源管理器热 P95 108.892 ms；GUI 关闭 3.044 秒、恢复 2.033 秒；作业错误 0 |
| 最终包 | `dist/trace-0.1.0-windows-x64-18`；37 文件、342,444,318 字节；777 项产品输入源码树 `46e98fe6651dcdf179305eae28cb94d56a4439e694a192a61dfe59c1802df224`；`SHA256SUMS.txt` 自身 SHA-256 `132398D8835626A604EB5D94495AA2509EB625F1211AE117F75161B90DFA95CD`；manifest 构建时 `git_commit=fe5a596`、`source_dirty=true`，最终提交以实际 HEAD 与源码树为准 |
| `python scripts/stage.py report --run m2-r7e` / `python scripts/stage.py check` | 均退出 0；`docs/evidence/gate/report.json` 由 `m2-r7e` 的测试、包、正式基准和文档/公开检查组装并通过；只证明自动证据，A 审查与产品验收仍 pending/not-run |

保留的失败/过期证据：`m2-r7` 的 rust-build 因调试 WebView 遗留的隔离 daemon 锁定 `trace-daemon.exe` 退出 101（该跑次 Rust/前端/脚本仍各自通过）；`m2-r7d` 包检查在 119 点矩阵前暴露 `--show-status` 路由时序问题；`dist/trace-0.1.0-windows-x64-17` 为失败候选包。修复后从 `m2-r7e` 重跑，未覆盖上述日志、未把旧 `-15` 或旧 `-17` 结果改绑到当前源码。`m2-r7e` 的原始回执、日志、采样和包身份保留在忽略目录 `artifacts/evidence-runs/m2-r7e/`。

自动证据不等于 A 技术审查或产品验收。真实站点/更新下载、Linux、安装器、签名发布仍 not-run，八站订阅规则仍全部 `partial`。

### 16.6 B → A 复验 prompt

```text
请作为 Agent A，在 codex/m2-multi-site-archive 复核 TRACE-M2-01 r2 本轮最终冻结交付。先读 AGENTS/REVIEW、stage-report §16、status、USER-ACTIONS、stage-plan §13，再读实际代码与 m2-r7e 原始回执；不要用旧 -15/-17 证据替当前源码。
重点复核：
1. F-071：archive.subscription.evidence 是否严格按本订阅 context/observation 过滤，job generation/consumer/request 是否真实归属；范围外已有条目、缺失 observation、其它 lane/失败 job、auto-on/off 第二轮零详情/媒体/新任务是否都有正反例；jobs.attempts 是否保持失败预算语义。
2. U1：窗口动作是否真实调用 main 窗口，拖动/双击最大化、按钮键盘、错误提示、浏览器隐藏、关闭后后台继续与重开是否由最终包实际验证；状态入口时序修复是否没有掩盖通知入口。
3. U2：100/110/125/150% 实际计算字号、持久化、非法/存储失败、多窗口同步；720/980/1280/1536 和 360 页面/弹窗/浮层是否无横向溢出、底部可达；Naive UI theme 与 CSS token 是否一致。
4. V1：快验的失败是否非零、是否明确不能作为正式 stage 证据、是否覆盖本批能力；最终包 -18 与 m2-r7e 是否匹配同一源码树。
5. 保留 A 的 F-069/F-070/F-085/F-086 修复与原失败证据；检查报告/状态/需求状态没有被写成产品验收通过。真实站点/更新、Linux、安装器、签名发布仍 not-run，八站订阅 partial。
请直接修局部问题、把较大缺口一次性交回 B，并在收敛后给出用户可执行的远端授权请求；本轮未授权 push/PR/合并。
```

<a id="a-m2-r7-review"></a>
## 17. A 独立复核、局部修复与剩余返工（2026-10-20）

被审交付为 `8dd0e49c728d0e6022a0a13895af6f286c75c10b`，分支正确、开工工作区干净，仍为基于 `main@c1552e0` 的唯一未 push 阶段提交。先核对 §16、当前状态、USER-ACTIONS 与计划 §13，再检查实际代码、原始回执和反例；没有用旧 `-15`、`-17` 替代当前冻结输入。结论：**F-071 仍开放；新增 F-087/F-088 已由 A 局部修复并定向复验；整阶段仍需返工，不请求远端授权。**

### 17.1 原冻结身份与原始证据

修改产品输入前，`python scripts/stage.py check` 为 0。独立逐项核对 `m2-r7e/receipts/*.json` 的九份回执：退出码均 0、sourceUnchanged=true、777 项 sourceInputs 与当前源码逐文件一致，原始日志摘要及解析用例与回执一致。Rust 905、前端 148、脚本 53、包 2 项是原回执核对结果，不虚称再次执行了 Rust 工作区全量。

原 `-18` 实物的 37 个文件、342,444,318 字节、实际成员摘要、清单和 manifest 关联全部正确；源码树 `46e98fe6651dcdf179305eae28cb94d56a4439e694a192a61dfe59c1802df224`，SHA256SUMS 自身摘要 `132398D8835626A604EB5D94495AA2509EB625F1211AE117F75161B90DFA95CD`。manifest 的构建基线 `fe5a596`、source_dirty=true 与冻结前工作树一致，不把 Git SHA 当作产品输入摘要。公开 report 与原始跑次 report 内容相同。

从 **930 个连续原始资源样本**重新调用 `benchmark_metrics.derive`，全部派生字段与原 benchmark 一致：工作负载 3601.183 秒、idle 905.187 秒、100,000 发布/500,000 媒体/100,000 资源管理器条目、四路录制、346 个混合并发样本、作业错误 0。idle CPU 0.304362%、内存 P95 52.984 MiB；四录制 CPU 1.708165%、内存 P95 67.180 MiB；热查询 140.732 ms、资源管理器热查询 108.892 ms；GUI 关闭 3.044 秒、恢复 2.033 秒。命令总耗时 4709.654 秒与连续负载时长分开记录；A 没有重跑一小时长测。

### 17.2 必须项与局部修复

| 编号 / 严重度 / 状态 | 实际复现、影响及处理 |
| --- | --- |
| F-071 / P1，仍开放 | `scripts/real_site_check.py:1422/1467/1497/1510` 的首轮 media job/acquisition/字节核查只遍历 changed_relations。独立反例让范围外已有条目的发现时间完全不变，但新增本订阅的 media job：auto-off 新增未完成的 media consumer（job 随订阅暂停，零字节）仍 ok=true；auto-on 对旧条目成功请求并收取 75 字节、同时目标正常完成，也仍 ok=true。第二轮完全不变不能补证首轮遗漏。必须对全部相关条目按任务/context/请求/acquisition/媒体字节分别做前后差异，不得只检查新增或刷新发现关系的条目 |
| F-071 / 同一问题的请求归属剩余 | `scripts/real_site_check.py:1407` 把所有目标新 context 都作为 detail_requests 的来源，没有限定到成功 observe job 的同代执行。独立 auto-on 反例把唯一详情请求的 evidence_id 从 ctx-observe-e1 改为 ctx-media-e1，成功 observe consumer 没有自己的详情请求，仍 ok=true。必须把观察序号、成功 observe consumer、job generation、context 与详情请求连成同一执行证据；不能借同条目的 media 执行背书。显式匿名降级的不同 context 也要保持可验证的同代归属 |
| F-087 / P1，A 局部修复、定向复验通过 | 原来没有任何 Tauri capability。`-18` 直接调用 plugin:window 的 start_dragging / internal_toggle_maximize 均答 not allowed by ACL；data-tauri-drag-region 实际走这些命令，而原包矩阵只调用自定义 desktop_window_toggle_maximize，故原测试全绿仍不能拖动/双击最大化。主页的 plugin:event listen 同样被拒绝，出现“后台事件连接失败”。新增 `src-tauri/capabilities/main-window.json`，仅 local/main 开放拖动、内部最大化和 listen/unlisten，不给远端 origin 或 emit 权限；扩充正式 WebView 探针验证真实拖动区事件的最大化往返、拖动调用及事件监听清理。新嵌入式 release 外壳实际通过 |
| F-088 / P2，A 局部修复、定向复验通过 | `WindowTitlebar.vue` 原样式在 max-width:760px 隐藏 .window-error；720px 是支持的原生最小宽度，窗口动作失败时文本虽已渲染却不可见。原包的同 scoped 属性合成错误节点实际 display=none、宽高 0。移除该隐藏规则，新外壳同节点 display=block、有非零尺寸；正式 WebView 探针新增 720px 错误样式守护。组件既有 rejected IPC 测试继续验证真实错误渲染；CSS 探针不冒充真实系统错误 |

F-071 的三条新反例都返回了错误的 ok=true，完整脚本最后以断言退出 1。最终反例仅替换时钟以免真实等待，wait_found/wait_quiet/wait_still 全部走生产实现；auto-on 的旧条目还具有成功 acquisition 和 complete 状态。它们是**验证器假阳性**，没有据此宣称生产 Core 真的进行了范围外下载。原四类反例与新反例须共同保留，不能仅调整一个样本或绕过断言。

已核实的 F-071 修复部分也保留：Core observation/context/acquisition 按 subscription_id 过滤，job 通过真实 job_consumers 行归属；请求与字节按 context_id 脱敏提取，原始 payload、凭证和临时 URL 不返回。缺失 observation、已有条目错误刷新发现关系、失败/其他订阅 job、auto-on 第二轮重复详情/媒体的现有测试通过；第二轮确实检查关系、观察、context、job generation/计数、acquisition、请求和媒体不变。`jobs.attempts` 仍只在失败路径递增，没有伪造成功计数。

### 17.3 U1 / U2 / V1 的证据边界

- **U1：** 原包的自定义最小化/最大化/关闭动作和 main label 限制正确，关闭不调用 desktop_exit。原包独立包检查 2 项通过，但漏掉 F-087 的插件 ACL 路径。A 修复后用正确 custom-protocol 特性构建嵌入式 release 外壳，生产 WebView 的拖动区双击往返、拖动命令、事件 listen/unlisten、119 点矩阵、关闭后 daemon 继续与重开同 instance 通过。测试保持最小化、不抢前台；未模拟用户物理拖动距离、真实 IME 或系统 DPI。
- **U2：** 四档根计算字号 16/17.6/20/24px、Naive UI theme 和 CSS 字号接入已核实，持久化/非法值/读写失败与 storage 同步的组件回归通过。A 在原包实际重载后仍为 150%/24px；两个隔离同源文档从另一文档选择 110% 后，两边与 localStorage 均为 110%，独立 BroadcastChannel 消息使接收方变为 125%。这是实际双文档同步，不冒充两个独立原生进程的完整体验验收。
- **布局：** 原包独立重跑完整 119 点（4 宽度×4 字号×7 页面，加 360px/150% 七页），以及模态框/Naive UI 浮层，结果通过。原始 observations 中有宽表、tab 和主页筛选条超出可见区域，但都在自身横向滚动区，不能据此认定全页裁切；主区无横向溢出、纵向可达。A 修复后的嵌入式外壳再跑同一矩阵通过。该跑次主要是空/默认合成状态，不把它扩大为所有长中文档案、所有管理弹窗及 ACT-003 的人工体验已覆盖。
- **V1：** 快验不启动 package/bench/report 或真实站点，不生成阶段回执。独立失败替身返回 9，execute 立即停止且保留 9；CLI 自身将失败映射到 1，§16.4 已更正表述。QUICK ONLY 摘要和 60 秒的短资源报告都被正式验证器拒绝；原输入与当前输入仍严格区分。分层快验不是正式阶段门禁。

### 17.4 A 的实际命令与保留证据

| 检查 | 实际结果与限制 |
| --- | --- |
| 修改前 stage check、原始回执/包/资源样本复算 | 各 0；仅证明原 m2-r7e / -18 冻结完整，不关闭验证器漏检 |
| `python -m unittest discover -s scripts/tests -v`；修复后 quick_verify evidence | 各 0：53 项；快验摘要明确 QUICK ONLY，不替代 stage 回执 |
| 前端 `run verify`（修复前、后） | 各 0：148 项、格式/类型/生产构建；既有 Vue Router 警告保留 |
| 定向 Rust：subscription_evidence / consumers / tool_update / media_tools / replay | 各 0：2 + 11 + 12 + 24 + 6 = 55 项；不是 workspace 全量 |
| 原 -18 独立 `check_package.py` | 修改产品输入前 0：2 项；新增拖动区守护后原包 1，含源码 stale 和实际 ACL 失败，不把它说成包被篡改 |
| 独立 driver-probes.py | 1：三条预期必须拒绝的输入均错误通过；完整正反输入与输出保留 |
| A 修复后的 release 外壳构建及嵌入式探针 | 0；`cargo build -p trace-desktop --release --locked --features trace-desktop/custom-protocol`，外壳 SHA-256 `2b6fb5b727bcea5bd65061d5324adbbccacaf487686c2bc9c13fcc6575f6448a`；这是定向外壳验证，不是新便携包/正式冻结 |
| 最终文档/公开/结构/生成类型/长行/规则/fmt/diff | 各 0；52 文档、835 本地链接，0 公开发现/超限文件/可折长行；文档同步后脚本 53 项再通过 |
| 修复后 stage check；原输入显式复验 | 当前 1：source/command/package/benchmark/raw inputs stale，符合 A 三个产品输入变更；按原 sourceInputs 显式校验旧 report 为 0 且 currentInputsDiffer=true，只证明旧冻结仍完整 |

本轮产物入口为 `artifacts/evidence-runs/a-m2-r7-review/`：`evidence-audit.log`、`scripts.log`、`frontend.log` / `frontend-fixed.log`、五组定向 Rust 日志、`driver-probes.py` / `driver-probes-rerun.log` / `driver-probes-production-waits-final.log`、`native-probes.py` / `.mjs`、`native-probes.log`、`native-css-red.log`、`display-sync.log`、`titlebar-red.log`、`titlebar-embedded-build.log`、`native-fixed-embedded.log`、`quick-probes.log`。实际 WebView 原始矩阵保存在各隔离子目录的 first/layout-matrix 日志中。

诊断过程的未成功输入也保留：最初 fixture 直接引用可变字节字典，随后改为 IPC 值快照的 deepcopy 才得到完整三条反例；最初尝试替换不可写的 Tauri invoke，未能触发错误，之后使用带相同 scoped 属性的错误节点仅验证 CSS；第一次直接 cargo release 未启用 custom-protocol，探针找不到生产页面，补上产品打包实际使用的特性后才通过。它们不是额外产品缺陷，也没有用后来的成功覆盖原日志。

A 本轮只改变三个产品输入：新增 local/main capability、标题栏错误样式、WebView 探针；共 778 项，源码树 `cfa19eed133473ff7648b769033885bb6a8b81d48dab3d32cf9226941c1c755b`。原 m2-r7e / -18 / 原始资源样本与 SHA256SUMS 未修改；当前正式 gate 应为 stale，不生成新 report 冒充完整冻结。F-069 五个输入及三条新回归、F-070 真正重开 Core、F-085 时刻比较、F-086 许可后选程序均保留，55 项定向回归覆盖相应守护，原 a-m2-r5/r6 的失败证据仍在。

八站订阅全部 partial，真实站点/真实更新下载、Linux、安装器、签名发布仍 not-run。本轮零真实请求、未 push、未创建 PR、未合并。USER-ACTIONS 仅更新 ACT-003 的适用包/已知限制与审查状态，不新增用户阻塞，也不把 F-071 转成人工检查。

### 17.5 A → B 一次性返工 prompt

```text
请作为 Agent B 在 codex/m2-multi-site-archive 完成 TRACE-M2-01 r2 的剩余 F-071；当前基点以 A 保存 §17 后 git log -1 为准，被审原交付为 8dd0e49c728d0e6022a0a13895af6f286c75c10b，仍只保留一个未 push、带 DCO 的阶段提交。
先读 AGENTS/REVIEW、stage-report §17、stage-plan §13、status 与 USER-ACTIONS。保留 A 的 F-069/F-070/F-085/F-086，以及本轮 F-087 local/main 最小 capability、F-088 窄窗错误可见性、WebView 的真实拖动区/事件权限/CSS 守护；原失败与 -18/m2-r7e 身份不变。
将 artifacts/evidence-runs/a-m2-r7-review/driver-probes.py 的三个假阳性迁入能力命名的回归，保持 IPC 快照值语义及既有全部正例/四类负例。首轮必须对全部相关 entry（不仅 changed_relations）核对新增/变化的 observe/media job、context、acquisition、请求与媒体字节；已有范围外条目不刷新关系也不能漏过新媒体排队或执行。开/关都要拒绝对应反例。
将新观察、成功 observe consumer、同代 job/context 与详情请求严格关联，media context 的详情请求不能冒充 observe；明确处理匿名降级的 context 关系，不伪造 jobs.attempts 或失败预算。继续保持两档第二轮零详情/媒体/新任务、关系不变与安全 DTO 隔离；不要只特判三个固定样本。
先定向验证并自审，再最后冻结集中跑完整测试、匹配的新包与必要正式资源门禁；当前 A 的三个产品输入使旧 gate stale，不能改绑 -18 或旧资源样本。不要在工作包间滚动重跑长测。保留原矩阵与新增权限/错误路径断言，准确区分默认几何、长中文合成状态和原生体验证据。
不追加真实站点或真实更新请求；八站订阅 partial、ACT-003 及 Linux/安装器/签名发布 not-run 保持。同步唯一报告/状态和用户适用包限制，amend 同一带 DCO 的阶段提交，交付准确 SHA、输入树、包摘要、原始回执及 B→A 复验 prompt。本轮不申请或执行 push/PR/合并/发布。
```

**实现交付：仍需补 F-071 与最终冻结；A 技术审查：rework-required；产品验收：not-run。** 首选下一步交 B 完成上述同一批剩余项，不要求用户人工排错或授权远端操作。

<a id="b-m2-r7f-delivery"></a>
## 18. B 完成 F-071 返工与最终冻结（2026-10-20）

B 从 A 保存 §17 后的 `4e8de86021ae6b3190789e22434df40b30aed956` 继续，保留 A 对 F-087 local/main capability、F-088 窄窗错误可见性和 WebView 真实拖动区/事件权限/CSS 的修复，以及 F-069/F-070/F-085/F-086 的全部既有修复。原 `a-m2-r7-review/driver-probes.py`、其假阳性日志、`m2-r7e`、包 `-18` 和更早失败证据均未修改或改绑。本轮没有真实站点/更新请求，没有 push/PR/合并/发布。

### 18.1 F-071：全部相关条目的首轮差异

`scripts/real_site_check.py` 现在先取本订阅首轮前后的完整安全证据，再按 entry 的并集逐项计算：

- 发现关系、observation、context、observe/media consumer job、acquisition、request 和按 context 的媒体字节差异；
- 范围外既有条目不因发现时间未变而被跳过；任一新增/变化活动即拒绝；
- 自动下载关闭时，任何相关条目的 media job、media request、acquired 记录或媒体字节都拒绝；
- 自动下载开启时，每条 media request、采集字节和 acquired 记录都必须由本轮新增的 succeeded media consumer 的精确 job generation/context 支撑，不能借用全局字节、其它条目或旧 context。

首轮目标观察也不再只凭“目标有新 context”通过。新 observation 必须落在本轮成功 observe consumer 的 `job_id + job_generation` context 上；详情请求必须是首轮新增请求，且 `evidence_id` 属于同一 observe 执行。显式匿名降级可在同一 job generation 下产生原 context 和匿名 resolution context：详情请求可留在原 context，最终 observation 可落在 resolution context，两者仍由同一成功执行连接。media context 的请求、失败 observe、旧 context 或未变化的 job 不能冒充。缺失/截断证据继续由 `evidence_problem` 拒绝；`jobs.attempts` 仍只是失败预算，未用于成功计数。

### 18.2 三条新反例、正例与原反例

新增正式回归：

| 回归 | 覆盖 |
| --- | --- |
| `test_incremental_rejects_media_activity_on_an_existing_out_of_range_work` | 迁入 A 的 auto-off 暂停 media job 与 auto-on 旧条目成功请求/75 字节反例；发现关系不变也不能漏检 |
| `test_incremental_detail_request_must_belong_to_the_observe_execution` | 迁入 A 的 `ctx-media-e1` 详情请求假阳性；该请求不再为成功 observe 背书 |
| `test_incremental_observe_accepts_explicit_anonymous_resolution_and_rejects_failure` | 显式匿名 resolution context 的同代正例，以及 failed observe 不能支撑新 observation 的反例 |

原四类反例及正例全部保留并通过：缺失 observation 字段、已有条目错误刷新范围外发现、其它 lane/foreign/failed media、auto-on 第二轮重复详情/media；范围、无日期详情、auto-on/off 第二轮零详情/媒体/新任务、关系/观察/context/job/acquisition/媒体证据不变和安全 DTO 隔离均继续覆盖。脚本全集由 53 项增至 **56 项**；A 的独立探针现三条都以 `ok=false` 结束、整体退出 0，保真输出在 `artifacts/evidence-runs/b-m2-r7f-rework/a-probes-after-fix.log`。

### 18.3 最终冻结与匹配证据

| 命令 / 输入 | 实际结果 |
| --- | --- |
| `python scripts/stage.py test --run m2-r7f` | 退出 0；Rust 工作区构建 9.87 秒，Rust 298.58 秒/905 项，前端 9.55 秒/148 项，脚本 36.88 秒/56 项；全部 `sourceUnchanged=true` |
| `python scripts/stage.py package --run m2-r7f` | 退出 0；`package/portable` 41.34 秒，`package/check` 57.99 秒/2 项（成员/摘要与八站隔离，嵌入式 WebView 关闭/重连/状态入口及拖动权限/CSS 守护） |
| `python scripts/stage.py bench --run m2-r7f` | 退出 0；命令耗时 4,652.66 秒，工作负载 3,601.22 秒，idle 窗口 905.41 秒；100k 发布/500k 媒体/100k 资源管理器；作业错误 0 |
| 基准指标 | idle CPU 0.3111%、内存 P95 52.207 MiB；四路录制 CPU 1.7242%、内存 P95 67.332 MiB；热查询 P95 141.621 ms、资源管理器热 P95 109.311 ms；GUI 关闭 6.167 秒、恢复 2.059 秒 |
| 最终包 | `dist/trace-0.1.0-windows-x64-19`；37 文件、342,475,050 字节；778 项产品输入源码树 `7c311ec63c4083d27ab6ae55875e5ad7361b39db37c86c260352ce89132eeefd`；`SHA256SUMS.txt` 自身 SHA-256 `56EE5A0AE7584413781B8ED6C304EC194B4B14EE99D86EDD3FC6172D5E4D1BB2` |
| 包 manifest | 构建时 `git_commit=4e8de86021ae6b3190789e22434df40b30aed956`、`source_dirty=true`；该 SHA 是 A 保存 §17 后的返工基点，最终阶段 SHA 以 amend 后 HEAD 为准，包身份以产品输入源码树为准 |
| `python scripts/stage.py report --run m2-r7f` / `python scripts/stage.py check` | 均退出 0；公开 gate 由 `m2-r7f` 的测试、`-19`、正式基准及文档/公开检查组装；只证明自动证据，A 审查仍 pending，产品验收 not-run |

原始回执、日志、930 个资源采样与包命令日志在忽略目录 `artifacts/evidence-runs/m2-r7f/`。旧 `m2-r7e` / `-18` 的 777 项输入、905/148/53 回执、包摘要与资源采样保持原身份，只证明原输入；未用新结果覆盖或改绑。`m2-r7` 的 daemon 锁定失败、`m2-r7d` 的状态入口时序失败、失败包 `-17` 和 A 的 `a-m2-r7-review` 原失败记录继续保留。

### 18.4 证据边界与下一步

- U1/U2/WebView 的产品输入与行为来自 A 已在 §17.2/§17.3 定向复验的版本；B 没有再改这些产品输入，`-19` 的包检查重新执行了成员与嵌入式 WebView 守护。119 点几何矩阵仍只证明合成状态，原生 IME、Windows 系统缩放和主观体验继续由 ACT-003 记录为 not-run。
- F-071 新回归是离线驱动/SQLite 契约验证，不是真实站点请求；八站订阅规则仍全部 `partial`，真实更新下载、Linux、安装器和签名发布仍 not-run。
- B 自检不等于 A 技术审查。实现交付现为“已提交待 A 复验”；A 通过前不请求 push/PR/合并授权。ACT-003 可在 `-19` 独立进行，但其结果不替代 A 审查。

### 18.5 B → A 复验 prompt

```text
请作为 Agent A，在 codex/m2-multi-site-archive 复核 TRACE-M2-01 r2 的 F-071 返工与最终冻结。先读 AGENTS/REVIEW、stage-report §17/§18、stage-plan §13、status 与 USER-ACTIONS，再看实际代码、`m2-r7f` 原始回执和包 `-19`；不要用 `-18`/`m2-r7e` 替当前输入。
重点复核：
1. 首轮是否对全部相关 entry（含发现关系未变的范围外旧条目）逐项比较 observation/context/observe/media job/acquisition/request/媒体字节；auto-off 和 auto-on 是否分别拒绝 A 的三条假阳性。
2. 新 observation、succeeded observe consumer、精确 job generation/context 与详情请求是否连成同一执行；media context 不能冒充，显式匿名 resolution 的不同 context 是否仍按同代通过。
3. 原有四类反例、全部正例、auto-on/off 第二轮零详情/媒体/新任务、安全 DTO 隔离与 `jobs.attempts` 失败预算语义是否保持；三条新回归是否在 56 项脚本全集内真实执行。
4. `-19` 与 `m2-r7f` 是否绑定同一 778 项源码树 `7c311ec63c4083d27ab6ae55875e5ad7361b39db37c86c260352ce89132eeefd`；测试、包检查、正式基准、report/check 和文档/公开门禁是否匹配；旧 `-18`/`m2-r7e` 与全部失败证据是否原样。
5. 保留 A 的 F-069/F-070/F-085/F-086/F-087/F-088 修复与 WebView 守护；确认八站 partial、ACT-003 not-run、真实站点/更新、Linux、安装器、签名发布未扩大声明。
请直接修局部问题、把较大缺口一次性交回 B；收敛后登记具体 push/PR/合并授权请求。本轮未授权任何远端动作。
```

**实现交付：已完成并冻结，待 A 复验；A 技术审查：pending；产品验收：not-run。** 首选下一步交 A 按 §18.5 复核，不要求用户人工排错或追加真实请求。

<a id="a-m2-r7f-review"></a>
## 19. A 独立复核 F-071 与最终冻结（2026-10-21）

被审交付为 `2e383ea43ad64c652fe230843e0b71649f2b4e41`，分支 `codex/m2-multi-site-archive`，开工工作区干净，基于 `main@c1552e048ea608ba64deae820d127bbd5e969083` 仍只有一个未 push、带 DCO 的阶段提交。按 §17/§18、计划 §13、REVIEW、状态及 USER-ACTIONS 复核实际实现、原始回执和 `-19`，没有使用 `-18/m2-r7e` 替代当前输入。**F-071 已关闭，未发现新增必须返工项；A 技术审查 passed。** 本轮不修改产品源码、测试、夹具、构建输入或原始交付证据，只同步当前审查/需求状态与具体授权请求。

### 19.1 F-071 的逐项判断

| 范围 | 实现核对与独立复验 | 结论 |
| --- | --- | --- |
| 首轮全部相关条目 | [增量驱动](../../scripts/real_site_check.py) 按 baseline/after entry 并集计算 `first_round_activity`，不再只遍历 changed_relations。发现关系单独比较；observation、context、observe/media consumer job、acquisition、请求与每个 context 字节分别比较 | 范围外旧条目的关系不变也不能漏过新活动；两档分别注入各维度均被拒绝；无新活动的旧条目两档均通过 |
| 原三条假阳性 | 原 `a-m2-r7-review/driver-probes.py` 不变，仅在新目录记录复跑输出；auto-off 的暂停 media consumer、auto-on 的旧条目成功请求/75 字节，以及 media context 冒充 observe 详情均为 `ok=false`，整体退出 0 | 原失败及新通过并列保留；没有用替身 wait 绕过生产逻辑 |
| observe 同代执行 | 新 observation 的 context 须属于本轮变化且 succeeded 的 observe consumer，其 `job_id + job_generation` 与新 context 精确相符；新增详情请求须落在这组 observe context，不借 media、旧 context、失败 consumer 背书 | 独立错代 context/失败 consumer 两档均拒绝；显式匿名 resolution 的请求与最终观察可处于不同 context，同 job/generation 两档均通过，resolution 错代两档均拒绝 |
| media 执行归属 | auto-off 对全部相关条目的新增/变化 media job、媒体请求、acquired 与字节拒绝；auto-on 的新增媒体请求、字节、acquired 须由本条目本轮成功 media consumer 的精确 generation/context 支撑，目标另须有真实请求及正字节 | 不能借全局字节、其它订阅/lane、失败 job 或旧 context 冒充成功获取 |
| 原反例、正例与第二轮 | [正式驱动测试](../../scripts/tests/test_real_site_check.py) 保留缺 observation、既有范围外关系错误刷新、其它 lane/foreign/failed media、auto-on 第二轮重复 detail/media 四类反例及全部正例。开/关均执行第二轮，比较关系、观察、context、job generation/consumer 计数、acquisition、字节与仅一次 list 请求 | 脚本全集真实执行 56 项；三条新能力命名回归各出现一次且 passed，非仅写在文件或额外探针中 |
| 安全 DTO 与失败预算 | [仓储查询](../../crates/core/src/store/archive.rs) 的观察/context/acquisition 按 subscription_id 过滤，job 按真实 job_consumers 归属；[应用层](../../crates/core/src/app/archive.rs) 仅从这些 context 提取脱敏请求与字节，不返回 payload、Cookie、凭证引用或临时 URL。`jobs.attempts` 的成功路径不递增、取消不花失败预算，失败路径保留原递增与 consumer 隔离语义 | 两项 subscription_evidence SQLite 契约与 11 项 consumers 定向测试独立通过；未重定义 attempts 为执行次数 |

独立补充的 24 个输入只使用合成 IPC 值快照与虚拟时钟：两档各验证旧条目零活动正例，以及 observation、context、observe job、media job、acquisition、request、媒体字节七种单独活动；另验证错代、失败 consumer、同代匿名 resolution、匿名错代四种执行边界。`wait_found/wait_quiet/wait_still` 均保留生产实现；这些探针不冒充新增正式测试或真实站点验证。

### 19.2 冻结身份与原始证据

逐文件重新计算当前产品输入，778 项、源码树 **`7c311ec63c4083d27ab6ae55875e5ad7361b39db37c86c260352ce89132eeefd`**。相对 A 保存 §17 的 `4e8de86`，只有 `scripts/real_site_check.py` 与 `scripts/tests/test_real_site_check.py` 两个产品输入变化；F-069/F-070/F-085/F-086、F-087 capability、F-088 CSS 和 WebView 守护逐文件保持。因此本轮不需要再修改产品输入或制造新包/长测。

- `m2-r7f/receipts` 九份回执均退出 0、sourceUnchanged=true，sourceInputs 与当前 778 项逐文件相同；原始日志摘要、解析用例与回执完全一致。Rust 905、前端 148、脚本 56、包 2 项均有真实日志支持；Rust 构建/测试/前端/脚本耗时分别为 9.869/298.577/9.548/36.878 秒。
- `-19` 实物为 37 文件、342,475,050 字节；实际成员与 SHA256SUMS、manifest、报告 inventory 全部一致。SHA256SUMS 自身摘要 `56ee5a0ae7584413781b8ed6c304ec194b4b14ee99d86edd3fc6172d5e4d1bb2`。manifest 仍如实记录构建基线 `4e8de86` 和 source_dirty=true，不改写为本轮提交 SHA；产品身份依源码树而非 amend 后 Git SHA。
- 从 930 个连续原始资源样本重新调用 `benchmark_metrics.derive`，所有派生字段与 benchmark 完全一致：工作负载 3601.217681 秒、idle 窗口 905.406 秒、100k 发布/500k 媒体/100k 资源管理器、346 个混合并发采样、四路录制、作业错误 0。样本 SHA-256 `7fe2379738d333d9884d5c38a064be896fae6b75cae3389146799d9952ab805c`。
- idle CPU 0.311070%、内存 P95 52.207031 MiB；四录制 CPU 1.724162%、内存 P95 67.332031 MiB；热查询 P95 141.621300 ms、资源管理器热 P95 109.310600 ms；GUI 关闭 6.167016 秒、恢复 2.059492 秒。命令总耗时 4652.658 秒与连续负载时长分开记录，没有补造或裁掉慢样本。
- 公开 `docs/evidence/gate/report.json` 与 `m2-r7f/report.json` 内容相同；全部 commands 对应原始 receipt 文件，正式验证器通过源码、包、measure 二进制、日志、原始采样、规则与需求覆盖校验。本轮需求只同步九项管理能力的状态，未改变 current 的适用范围、implementation、caseGroups 或必需 testCases；现有 report/check 仍有效。

旧 `m2-r7e` 的 777 项树 `46e98fe6651dcdf179305eae28cb94d56a4439e694a192a61dfe59c1802df224`、`-18` 的 37 文件/342,444,318 字节及摘要 `132398d8835626a604eb5d94495aa2509eb625f1211ae117f75161b90dfa95cd` 按原 sourceInputs 显式复验仍通过，但与当前输入不同，只证明原冻结。原 m2-r7 的 **rust-build 退出 101**（daemon 文件锁定）、m2-r7d 的 **package-check 退出 1**（状态入口时序失败）、失败包 `-17`、A 原三条 ok=true/断言失败及更早 a-m2-r5/r6 的失败日志/补丁均保留。对旧包、旧跑次和 A 失败证据建立本轮前后摘要清单，4,860 个文件全部不变，不改绑或覆盖旧失败；四项更早失败摘要与 §17 的保存记录相同。

### 19.3 A 的实际命令与证据入口

| 检查 | 实际结果与限制 |
| --- | --- |
| `python -m unittest discover -s scripts/tests -v` | 0：56 项，包括三条新回归、原反例/正例和治理反例；文档收尾后再执行仍通过 |
| 原 driver-probes 与独立 24 输入边界 | 各 0；原三条假阳性全部拒绝，逐维度正反结果与 §19.1 一致；零网络 |
| 定向 Rust：subscription_evidence / consumers / tool_update / media_tools / replay | 各 0：2 + 11 + 12 + 39 + 7 = 71 项。按实际过滤器输出计数，不沿用 §17 的 55 项；不是 workspace 全量 |
| `python scripts/check_package.py dist/trace-0.1.0-windows-x64-19` | 0：2 项，57.841 秒；包含八站隔离、119 点矩阵、真实拖动区双击往返/拖动权限、事件 listen/unlisten、720px 错误 CSS、关闭后 daemon 继续与多窗口重连/退出 |
| 原始回执、实物包、连续资源采样复算 | 0；当前与原 m2-r7e 分别按各自输入验证，没有重跑 Rust/前端全量或一小时长测 |
| 文档、公开、结构、生成类型、长行、规则、Rust fmt、diff 与 stage check | 各 0；文档改动不改变产品指纹，不重生成 m2-r7f 原始回执或公开 report |

证据入口为忽略目录 `artifacts/evidence-runs/a-m2-r7f-review/`：`audit.py`、`evidence-audit.log`、`preserved-inputs.json` / `preservation-final.log`、`scripts.log` / `scripts-final.log`、`original-probes.log`、`driver-boundaries.py` / `.log`、`doc-sanity.py` / `.log`、五组定向 Rust 日志、`package.log`、`gates-final.log`。本轮包检查的完整 WebView observations 在新建 `webview-*` 跑次中；不移动旧跑次。Rust 有既有 linker stdout 警告和增量缓存目录无法 finalize 提示，实际测试返回 0，没有将其说成严格 Clippy 通过。

审计脚本初次错误地把 m2-r7 的 test-rust（实际成功）当作原锁定失败回执，故断言退出 1；对照原日志确认真正失败的是 test-rust-build（101）后修正审计选择并通过，初始日志 `evidence-audit-initial.log` 原样保留。文档核对的初始断言把八站当成八条订阅规则，实际是八站 18 条且全部 partial；错误假设的复现留在 `doc-sanity-initial.log`，正确计数及完整需求范围核对通过。这些是本轮审计脚本的定位/计数错误，不是产品或交付证据被修改。

### 19.4 阶段判断与下一步

**实现交付：已冻结；A 独立技术审查：passed；产品验收：not-run；远端操作/发布：未授权、未执行。** F-071 关闭，F-069/F-070/F-085/F-086/F-087/F-088 修复保持，无新增 Agent 侧必须项，不再交 B 复审。九项管理能力按计划 §6 的既定规则同步需求/映射为“已实现”；这不是产品验收或第三方插件分发结论。

八站订阅仍全部 `partial`，旧真实站点回执只属于原包，不补写为 `-19` 已真实通过；签名拒绝、限流、未知历史与预算停止不是“无更新”。本轮真实站点/更新请求均为零。ACT-003、真实更新下载、Linux、安装器、签名发布继续 not-run；WebView 自动几何/权限/CSS 不能证明物理拖动、IME、系统 DPI 或主观体验。

首选下一步是用户处理 [ACT-008](../../USER-ACTIONS.md#act-008) 的具体 push/PR/合并授权与已披露交付范围；本轮只登记请求，未进行任何远端查询或写入。本轮用户事项变化为 **ACT-003**（技术状态更新，适用包不变、无需因文档收尾重测）与新增 **ACT-008**；人工检查不是远端授权前提。其余事项和预算不变。
