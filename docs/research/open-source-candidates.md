# 通用开源候选初查

查询日期：2026-09-25。依据用户补充，范围为**前端等通用技术栈**，不选择抖音/哔哩哔哩下载器。规则见[开源优先](../dev-rules/open-source-first.md)，网站另见[适配规则](../dev-rules/site-adapters.md)。

原始查询保存在[快照](open-source-snapshot.json)，包含官方仓库 URL、归档状态、默认分支提交、仓库 latest release、少量近期 issue/PR 元数据及错误。未安装候选、未跑集成，亦未完成逐个 issue 响应和分发许可审计；不能据此定稿生产技术栈。

2026-09-26 后续：用户已指定 Vue，A 已完成[具体比较与选择](2026-09-26-technology-stack.md)。下文保留 09-25 初查的原始判断，不能用它重新提出已决定的框架问题；当前状态见后续研究及事项总表。

## 候选与 TRACE 边界

| 能力 | 候选/官方来源 | 本次证据与限制 | TRACE 后续要验证 |
| --- | --- | --- | --- |
| 桌面外壳 | [Tauri](https://github.com/tauri-apps/tauri) | 既有设计基线；未归档，09-25 默认分支有提交；latest 返回 `tauri-driver-v3.0.0-alpha.0`，不是可直接选的桌面稳定包 | 目标包稳定版本、独立后台、受控 IPC、Windows 打包/关闭/重连，OSS-002 |
| 前端框架 | [React](https://github.com/facebook/react)、[Vue](https://github.com/vuejs/core)、[Svelte](https://github.com/sveltejs/svelte) | 均未归档；所取提交分别为 09-22/09-18/09-25；仓库发行分别返回 v19.3.0/v3.5.43/svelte@5.57.1 | 对比真实列表、状态、表单、中文输入、长期维护与组件生态；不提前选定，OSS-001 |
| 构建 | [Vite](https://github.com/vitejs/vite) | 未归档，09-25 提交，仓库发行 v8.3.1 | 与所选框架/Tauri 兼容、离线资产、开发/打包差异，OSS-001 |
| 组件与样式 | [Radix Primitives](https://github.com/radix-ui/primitives)、[Tailwind CSS](https://github.com/tailwindlabs/tailwindcss) | 均未归档；Radix 提交为 07-31，release API 404，应查实际包发行；Tailwind 提交 09-25，发行 v4.3.3 | Radix 仅 React 候选；Vue/Svelte 选对应生态后另查证。Tailwind 是样式工具，不能代替组件可访问性与设计规范 |
| 异步运行 | [Tokio](https://github.com/tokio-rs/tokio) | 未归档，09-25 提交，发行 tokio-1.53.1 | 取消、超时、阻塞 SQLite/文件任务隔离、进程退出，OSS-003 |
| SQLite 访问 | [rusqlite](https://github.com/rusqlite/rusqlite)、[SQLx](https://github.com/launchbadge/sqlx)、[SQLite 事务](https://sqlite.org/lang_transaction.html) | 仓库元数据未归档；后续提交/发行查询遇 API 限流，未完成维护判断 | 单写事务、迁移、备份、FTS5 构建、锁与中断恢复；只选择一个主访问方案，OSS-003 |
| 媒体探测与封装 | [FFmpeg](https://github.com/FFmpeg/FFmpeg)、[官方法律说明](https://ffmpeg.org/legal.html) | 元数据未归档；本快照后续 API 限流；正式发行和许可应查官网及实际构建 | ffprobe/流拷贝、取消、分片、Windows 二进制来源/构建选项/通知义务，OSS-004 |
| 本地搜索 | [SQLite FTS5](https://sqlite.org/fts5.html)，必要时再比 [Tantivy](https://github.com/quickwit-oss/tantivy) | 原设计优先 SQLite 路径；Tantivy 仓库查询限流，未确认维护状态 | 中文一字/二字/符号正确性、体积和查询；有实测瓶颈才增加引擎，OSS-003 |
| 测试 | Rust 自带测试工具、[Vitest](https://github.com/vitest-dev/vitest)、[Playwright](https://github.com/microsoft/playwright) | 两个 JS 仓库查询限流，未确认维护状态 | 选择与栈匹配的工具；浏览器 E2E 不证明 Tauri/Windows IPC 正确，OSS-005 |

日期为 UTC 查询值的日期部分；快照保留精确时间。仓库默认分支提交只是一项信号，需要核对是否为有效维护以及相关 bug 的响应；不等于维护保障或推荐安装版本。API 返回许可标识可能不完整，不可替代实际分发审计。

## 本轮结论与下一步

有足够候选支撑“通用能力先评估活跃开源”的开发政策；尚无依据为 TRACE 宣称最终框架、组件、驱动或媒体构建已选定。A 在首轮实现计划前围绕实际页面和数据/生命周期需求补比较，B 根据具体方案接入验证，问题只在[OSS-001～005](../decisions/open-questions.md)维护当前状态。

本次候选池不保留下载器推荐。站点方案不能从通用引擎研究直接推导，尤其不能把工具的基本下载能力等同于 TRACE 的完整收录规则。

<a id="social-sites-2026-10-16"></a>
## 2026-10-16：多站订阅与下载的候选

查询方式：GitHub REST（仓库元数据与最新发行）及项目 README，2026-10-16，日期为 UTC。只是资料判断，B 集成时补实测。站点规则本身不选用现成下载器，见[社交站点研究](social-sites-current.md)。

| 用途 | 候选 | 维护与许可 | 结论 |
| --- | --- | --- | --- |
| YouTube 媒体格式与地址 | [yt-dlp](https://github.com/yt-dlp/yt-dlp) | Unlicense；最新稳定发行 2026.08.19，默认分支 2026-10-10 仍有推送，未归档。官方 Windows 可执行文件内含若干第三方组件，其中 mutagen 为 GPLv2+、certifi 为 MPL-2.0 | **采用为可替换执行部件**，只用于 YouTube 的格式解析（必要时下载到临时目录），由 Core 调用。理由：YouTube 媒体地址需要追踪播放器脚本、签名、PO Token 与分段流协议的持续变化，自研维护成本远高于整合；列表、身份、元数据仍由 TRACE 适配器负责 |
| 上项要求的脚本组件 | [yt-dlp-ejs](https://github.com/yt-dlp/ejs) | Unlicense（内含 MIT/ISC 组件）；0.8.0 于 2026-03-17 发行 | 随 yt-dlp 使用；README 写明完整 YouTube 支持需要它 |
| JavaScript 运行时 | [Deno](https://github.com/denoland/deno) | MIT；2026-10-07 有推送 | yt-dlp 推荐且默认启用的运行时，锁定版本随本地包携带。QuickJS 体积更小但较慢，作为备选，B 实测后定 |
| Rust 原生 YouTube 库 | [rusty_ytdl](https://github.com/Mithronn/rusty_ytdl) | MIT；最近推送 2026-01-18，约 180 星 | 不选：维护频率跟不上 YouTube 播放器变化 |
| Instagram 抓取库 | [instaloader](https://github.com/instaloader/instaloader) | MIT；2026-10-14 有推送 | 不作运行依赖：Python 进程会自己发请求、自己管理会话，绕过 Core 的代理、预算与凭证边界；只作协议对照 |
| X 抓取库 | [twscrape](https://github.com/vladkens/twscrape) | MIT；2026-10-05 有推送 | 不作运行依赖，理由同上；它维护的 GraphQL 查询 ID 与特性参数可作对照 |
| 多站图片/视频下载器 | [gallery-dl](https://github.com/mikf/gallery-dl) | GPL-2.0；2026-10-09 有推送 | 不采用：许可与凭证边界都不合适；只作协议对照，不复制代码 |
| 小红书库 | [xhs](https://github.com/ReaJason/xhs) | MIT；最近推送 2025-07-01 | 不采用：一年多未更新，签名方式依赖浏览器环境 |
| 小红书/多站爬虫 | [MediaCrawler](https://github.com/NanmiCoder/MediaCrawler) | 仓库许可证未被 GitHub 识别（非标准许可） | 不采用：许可不清 |
| 代理 | 现有 [reqwest](https://github.com/seanmonstar/reqwest) 的代理能力（HTTP 与 SOCKS5 需开启对应特性） | 已是依赖 | 不引入新库；B 核实所用版本的 SOCKS 特性与系统代理读取，并保持[多站契约 §9](../design/multi-site-contracts.md#proxy)的域名检查 |
| 资源管理器界面 | 现有 Naive UI 的树与虚拟列表表格 | 已是依赖 | 不引入新组件库 |
| Rust → TypeScript 类型 | [ts-rs](https://github.com/Aleph-Alpha/ts-rs) | MIT；v12.0.0 | 见[代码结构优化方案](../design/code-structure-refactor.md)，本阶段整理时接入 |

yt-dlp 与 Deno 的版本、下载地址、SHA-256 与许可证文本按现有媒体资产的方式锁定并随本地包携带；工具自己的 `-U` 不用，yt-dlp 由用户在设置里手动更新（[DEC-039](../decisions/confirmed-decisions.md#dec-039)，依据见[下文](#tool-update-2026-10-18)）。对外分发前需要按其中 GPL/MPL 组件的义务提供源码获取说明，用户决定见 [USER-ACTIONS Q-009](../../USER-ACTIONS.md#q-009)。

<a id="x-web-client-2026-10-18"></a>
## 2026-10-18：X 的查询标识与事务 ID 请求头

查询方式：GitHub REST（仓库元数据）与仓库源码，2026-10-18，日期为 UTC。

| 用途 | 候选 | 维护与许可 | 结论 |
| --- | --- | --- | --- |
| 事务 ID 请求头算法 | [XClientTransaction](https://github.com/iSarabjitDhiman/XClientTransaction) | MIT；约 270 星；最近推送 2026-06-26，未归档，无发行版 | **不作运行依赖**（Python，自己解析页面、自己发请求，绕过 Core 的代理、预算与凭证边界）；**算法按其 MIT 许可的公开实现读懂后在适配器里自写**（`adapters/x/src/transaction.rs`），不复制源码。校验方式：用该实现在本仓库的合成首页与脚本上跑出固定向量（动画密钥与三个请求头），Rust 实现逐条核对；合成页面不含站点的真实数据 |
| 查询标识与特性开关的数据来源 | [TwitterInternalAPIDocument](https://github.com/fa0311/TwitterInternalAPIDocument) | 仓库许可未声明（GitHub 为 NOASSERTION）；约 720 星；最近推送 2026-10-17，未归档 | 只取事实性的数据（三个操作的查询标识、特性开关名与其声明的取值、字段开关名、公开令牌）写进 `adapters/x/data/web_client.json`，不复制文档文本或代码；取得日期 2026-10-18；以后以同一来源或站点脚本更新 |
| X 抓取库 | [twscrape](https://github.com/vladkens/twscrape)（见上） | MIT | 仍只作对照 |

“现有方案不适用”的实证：事务 ID 的计算要读首页的验证密钥与动画、按需脚本里的字节位置，现成实现都是带自己网络栈的 Python 库，不能放进 Core 的受限请求通道；所以自写算法、复用公开实现的向量。真实站点是否接受由[阶段报告 §4](../delivery/stage-report.md)记录的真实检查回答，离线向量只证明与参考实现一致。

<a id="xhs-signing-2026-10-18"></a>
## 2026-10-18：小红书列表续页的签名

查询方式：GitHub REST（仓库元数据、发行、议题）与仓库源码，2026-10-18，日期为 UTC。用户要求小红书取到完整历史（[DEC-041](../decisions/confirmed-decisions.md#dec-041)）。

| 用途 | 候选 | 维护与许可 | 结论 |
| --- | --- | --- | --- |
| 续页签名（`x-s`、`x-s-common`、`x-t` 等请求头） | [xhshow](https://github.com/Cloxl/xhshow) | MIT；约 1100 星；最近推送 2026-06-11，未归档；v0.2.0 于 2026-06-11 发行，加入新的 `XYW_` 签名格式。2026-03 起作者主页列表（`user_posted`）拒绝旧的 `XYS_` 格式、答 406（议题 #104，2026-06-10 关闭）。作者回复显示维护时间有限 | **不作运行依赖**（Python）；**算法按其 MIT 许可的实现读懂后在小红书适配器里自写**，做法与 X 的事务 ID 相同：用该实现在合成输入上跑出固定向量，Rust 实现逐条核对。签名格式变化频繁（2026 年内已换过两次），向量要带格式版本，站点拒绝时按站点拒绝停止 |
| 同上的 TypeScript 移植 | [xhshow-js](https://github.com/renmu123/xhshow-js) | MIT；约 7 星；最近推送 2026-03-11 | 只作对照：最近一次推送早于 `XYW_` 格式出现 |
| 早期小红书库 | [xhs](https://github.com/ReaJason/xhs)（见上） | MIT | 仍不采用 |

签名的输入（读 xhshow 源码 `client.py`、`core/common_sign.py`、`generators/fingerprint.py`）：`x-s` 只用 Cookie 里的 `a1`（设备标识）与请求地址、参数；`x-s-common` 用 `a1` 和一份模拟的浏览器指纹，指纹里有一段 Cookie 文本（模拟页面脚本能读到的 `document.cookie`）和随机的屏幕、显卡等取值。所以签名不需要登录 Cookie `web_session`：适配器只拿 `a1`，指纹里的 Cookie 文本也只放 `a1`。站点是否接受这种指纹，要由真实检查回答；离线向量只证明与参考实现一致。

“现有方案不适用”的实证：现成实现都是 Python 或 TypeScript 库，要么自己发请求，要么要求传入整组 Cookie，不能放进 Core 的受限请求通道，也不符合“登录 Cookie 不出 Core”；所以自写算法、复用公开实现的向量。

<a id="tool-update-2026-10-18"></a>
## 2026-10-18：yt-dlp 的手动更新与签名核对

查询方式：GitHub REST（最新发行与资产列表、仓库源码）与 crates.io，2026-10-18，日期为 UTC。用户要求 yt-dlp 能在设置里手动更新（[DEC-039](../decisions/confirmed-decisions.md#dec-039)）。

- **发布资产**：最新稳定版 2026.08.19（2026-08-19 发布，标为不可变发行）带 `yt-dlp.exe`、`SHA2-256SUMS`、`SHA2-512SUMS` 与两者的 `.sig`。`.sig` 是用项目仓库里 [`public.key`](https://github.com/yt-dlp/yt-dlp/blob/master/public.key) 签的 GPG 分离签名：RSA 4096，2023-02-26 生成，指纹 `AC0C BBE6 848D 6A87 3464 AF4E 57CF 6593 3B5A 7581`，没有到期日。TRACE 随包锁定这把公钥，不在运行时下载公钥。
- **工具自带的 `-U` 不用**：读 `yt_dlp/update.py`，它只拿同一发布里的 `SHA2-256SUMS` 核对新文件的 SHA-256，不核对 `.sig`；摘要文件里找不到对应条目时只给警告、照样替换。签名与摘要都由 TRACE 自己核对，失败就不替换。

| 用途 | 候选 | 维护与许可 | 结论 |
| --- | --- | --- | --- |
| 在 Rust 里核对 OpenPGP 分离签名 | [rpgp](https://github.com/rpgp/rpgp)（crate `pgp`） | MIT OR Apache-2.0；2026-10-06 有推送，未归档；0.21.0 于 2026-10-03 发行，0.20.0 于 2026-06-23；累计下载约 690 万 | **推荐**：纯 Rust，许可与本项目相容；B 选用发行满两周的版本，用 yt-dlp 真实的 `SHA2-256SUMS` 与 `.sig` 做固定样本 |
| 同上 | [sequoia-openpgp](https://gitlab.com/sequoia-pgp/sequoia) | LGPL-2.0-or-later；2.4.2 于 2026-10-14 发行 | 不选：许可义务比 rpgp 重，功能远超所需 |
| 同上 | 调用系统的 `gpg` / `gpgv` | 依赖用户环境；GPL | 不选：Windows 上不一定有，也会碰用户的密钥环 |

<a id="similar-media-2026-10-18"></a>
## 2026-10-18：相似资源对比

查询方式：crates.io，2026-10-18。用于 FR-ARC-006 的“相似候选”（只给候选，由用户决定，不自动删除）。

| 用途 | 候选 | 维护与许可 | 结论 |
| --- | --- | --- | --- |
| 图片感知哈希 | [image_hasher](https://github.com/qarmin/img_hash) | MIT OR Apache-2.0；3.1.1 于 2026-02-21 发行；累计下载约 118 万；原 `img_hash` 的维护分支 | **推荐**：B 实测速度、内存与误报后定；视频用已有 FFmpeg 抽几张关键帧再算图片哈希，不另引视频相似库 |
| 同上 | [img_hash](https://github.com/abonander/img_hash) | MIT OR Apache-2.0；最后发行 2021-05-04 | 不选：已不维护 |
| 同上 | [blockhash](https://github.com/jaehl/blockhash) | MIT；1.0.0 于 2024-04-08 | 备选：算法单一 |

<a id="desktop-display-2026-10-20"></a>
## 2026-10-20：沉浸式外壳与文字缩放

沿用已锁定的 Tauri 2.11、Vue 3.5 与 Naive UI 2.45，不升级或另引窗口/缩放引擎。官方依据：[Tauri 窗口定制](https://v2.tauri.app/learn/window-customization/)、[Window API](https://v2.tauri.app/reference/javascript/api/namespacewindow/)、[Naive UI 主题](https://www.naiveui.com/en-US/os-theme/docs/customize-theme)。查询日期 2026-10-20；GitHub 官方元数据：Tauri 与 Naive UI 均未归档，最近推送分别为 2026-10-19 14:41 UTC、2026-10-05 15:39 UTC；这仅是维护线索，不据此声称本功能已通过兼容测试。许可沿用现有依赖台账，不复制上游源码。

推荐复用 Tauri 的无系统装饰窗口、拖动区域和真实窗口动作；Vue 响应式显示偏好驱动 CSS token 与 Naive UI theme overrides。官方教程明确提供 `decorations`、`data-tauri-drag-region`、`startDragging`、`toggleMaximize`、`minimize` 等能力，现有栈足够，不自研 Windows 消息循环。新窗口权限必须仅限本地 main 窗口与实际使用动作，不打开任意文件/命令权限。备选保留系统标题栏只适用于沉浸式实现出现兼容故障时的显式回退，不冒充已满足用户要求。固定画布 transform 放大不能满足重排，故不选；全面切换组件库的集成与许可成本无必要。B 仍须补实际尺寸、窗口状态和持久化反例。
