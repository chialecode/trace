# M2：多站订阅与下载

计划 ID：**TRACE-M2-01 r2**；2026-10-16 制定，2026-10-18 按用户对 Q-009、Q-012、S-001、S-003 的答复更新。A 规划与最终集中审查，B 连续实现与自检，A 通过后直接向用户交付，不再交 B 复审。

## 1. 目标与基点

在同一 Windows 本地应用里，用户可以：

- 为一个创作者关联微博、小红书、Instagram、X、YouTube、抖音、TikTok、哔哩哔哩中任意站点的账号，在一条统一时间线里看到这些账号发布的文字、图片、视频和混合内容；
- 为每个站点账号单独决定是否自动下载视频，没自动下载的视频以后可以勾选同步进资源库；
- 粘贴单个作品链接直接下载，或粘贴作者主页先预览、再勾选批量下载，下载到资源库或自己选的目录（可读文件名）；
- 在设置里改资源库位置、默认下载位置、网络代理和各站点登录；手动检查并更新锁定版本的 yt-dlp，更新前核对发布签名与文件摘要；
- 在“支持的网站”页面看到每个站点支持订阅什么、需要什么条件、最近一次验证是什么时候；
- 在资源管理器页面像浏览文件夹一样浏览资源库和下载目录；
- 合并、拆分和重新关联创作者与账号，查看资料观察历史，确认或拒绝待确认的身份关联；
- 导入本地文件（托管复制或原地索引），比较精确重复与相似资源，跨平台聚合同一作品并批量整理；
- 独立安装、升级和回退站点插件；登录辅助、通知、存储和内容处理扩展各用独立能力契约，关闭可选扩展不影响基础归档。

在此之前先完成行为不变的代码结构整理与工作区清理（DEC-031 / Q-005）。九项原顺延的管理与插件能力已由用户纳入本阶段（DEC-040），见 §11；yt-dlp 手动更新见 DEC-039，小红书完整历史见 DEC-041。

- **分支：** `codex/m2-multi-site-archive`，基点 `main@c1552e048ea608ba64deae820d127bbd5e969083`（M1 的 squash 合并）。本计划所在的提交就是本阶段唯一提交的起点；B 的实现、返工、A 审查记录与修复全部 amend 进它，首次 push 前只有一个带 DCO 签名的提交。push、PR、合并须用户另行授权。
- **已实现的基础：** 见[交付状态](status.md)。两站订阅、链接下载、直播录制、认证历史、质量升级、导出备份恢复的现有行为与断言全部保留。
- **依据：** [DEC-031～034](../decisions/confirmed-decisions.md#dec-031)、[DEC-039～041](../decisions/confirmed-decisions.md#dec-039)、[需求](../requirements.md)中状态为“本阶段”的条目（[需求映射](requirements-map.json)中状态为 `current`）、[多站契约](../design/multi-site-contracts.md)、[代码结构优化方案](../design/code-structure-refactor.md)、[社交站点研究](../research/social-sites-current.md)、[开源候选](../research/open-source-candidates.md#social-sites-2026-10-16)。

## 2. 正本、范围与执行自主权

必读：AGENTS、REVIEW、文档索引、交付状态、有效决定、开发事项、USER-ACTIONS，以及上一段列出的正本和站点、安全、开源、质量、Git 规则。按[本地参考规则](../dev-rules/local-reference-and-files.md)逐级找参考索引，按“类 RSS 订阅规则”“单个视频链接解析”“用户视频列表解析”“抖音签名与访问上下文”等功能条目查阅；参考只读，公开文件不写参考项目名与本机路径，GPL/AGPL 项目不复制代码。

范围外：新站点直播录制、评论与互动数据、Instagram 快拍与精选、X 回复与点赞列表、会员/年龄限制/付费内容、任何站点写操作、RSS/Atom 输出（DEC-034）、远程模式、Linux、安装器与公开发行。原先顺延的九项管理与插件能力已由用户纳入本阶段（DEC-040），按 §11 执行，不再属于范围外；开放第三方插件分发仍不在本阶段，安装入口只接本机目录且不执行未通过兼容性检查的版本。

B 可以自主决定模块拆分、DTO 字段、实现顺序与受约束的技术细节，补测试、锁定兼容依赖、完善 UI、打包与测量。站点签名、查询 ID 等做不到时，按[多站契约](../design/multi-site-contracts.md#rule-catalog)把对应规则如实标 `partial`/`unimplemented` 并写明已尝试的方案，继续其他部分，不静默缩减范围。只有改变数据语义、必须范围或外部权限且无法隔离时才升级给 A；非阻塞的用户决定按 USER-ACTIONS 中的当前默认继续。

## 3. 连续工作包

工作包用于实施与证据定位，不产生额外分支、提交、PR 或逐包审批。P/C 编号只在本计划内有效，测试名按能力命名，不带编号。

| ID / 依赖 | 完整结果 | 关键失败与完成证据 |
| --- | --- | --- |
| P0 / 无 | 执行第 4 节整理清单；记录不入库文件各类别数量、大小与处理结果；测试与脚本的迁移映射留给 P1 | 用户数据、Profile、`.env.local`、Cookie、ACT-003 在用的包与样本不动；文档、公开、反例检查通过 |
| P1 / P0 | [代码结构优化方案](../design/code-structure-refactor.md) R0～R8：统一适配器运行时、适配器按能力拆分、强类型任务载荷、`store/` 仓储、拆 `core.rs` 与长函数、生成 TS 类型与通用转发、测试按能力重命名、证据工具拆分与统一入口 | 每一步全部门禁通过；R0 基线的测试数与断言数逐条对账；不改 schema、插件协议、命令名与参数形状、用户可见行为；改一份文档不再让包证据过期 |
| P2 / P1 | 领域与契约扩展：八站站点注册；规则声明的协议字段；统一元数据列/子表；订阅“自动下载视频”；下载位置与外部副本；代理设置；schema 前进一版的逐步迁移 | 旧库、旧备份、旧适配器（无规则声明）可用；已有订阅迁移为“自动下载视频”开启；迁移中断与旧程序拒写有反例 |
| P3 / P2 | 规则目录汇总与持久化、链接识别、“支持的网站”页面数据接口 | 未真实验证的规则不能是 `implemented`；页面零网络；插件停用仍显示最后声明 |
| P4 / P2 | 网络代理（全局/按站点：直连、系统、手动 HTTP/SOCKS5），代理下的域名边界与 Cookie 作用域，TUN 虚拟 DNS 环境与系统代理为空时的直连 | [多站契约 §9](../design/multi-site-contracts.md#proxy) 全部约束；直连模式现有 DNS 固定与私网拒绝不放宽 |
| P5 / P2 | 资源库位置迁移；默认下载位置与单次另选；临时目录写入后改名；可读命名与旁注 JSON；外部副本记录与“已变化/已不在”；同步到资源库 | [多站契约 §7](../design/multi-site-contracts.md#destinations)；非法字符、保留名、超长路径、冲突、不可写、空间不足；中断不留看似完整的文件；同字节只存一份 |
| P6 / P2、P5 | 订阅“自动下载视频”开关（每个来源账号）、“未下载（按订阅设置）”状态、手动同步、手动补查（单条/时间段/失败项） | [多站契约 §5](../design/multi-site-contracts.md#subscriptions)；未下载不计为缺失或失败；同步幂等；补查不前移游标 |
| P7 / P3、P5 | 先预览再勾选（作者主页分页预览、跨页勾选、批量下载）；八站单链接预览与下载；两者选择下载位置 | [多站契约 §6](../design/multi-site-contracts.md#preview)；预览不下载媒体、不建订阅、有页数与预算上限；已在库条目标出 |
| P8 / P2～P4 | 新站点生产适配器：微博、YouTube、TikTok、X、Instagram、小红书（建议按此顺序）。每站：账号识别与资料、规则声明、列表与续页、详情、媒体候选、统一元数据映射、登录探测、错误分类、脱敏夹具与签名固定向量 | 每站从订阅到离线浏览、从单链接到下载完整闭环；空响应不当作“没有更新”；签名/查询 ID 可单独更新；做不到的规则按 OSS-013 结论标 `partial` |
| P9 / P8（YouTube） | YouTube 媒体的外部解析工具：锁定版本的 yt-dlp 与 Deno 资产（地址、SHA-256、许可文本），Core 调用约束，Core 下载或临时目录提交，工具缺失时降级 | [多站契约 §10](../design/multi-site-contracts.md#youtube-media)；不传 Cookie、不自我更新、有超时与输出上限；真实公开视频完整下载并校验 |
| P10 / P2 | 抖音与哔哩哔哩：已有规则改为声明式并带真实验证日期；已保存安全元数据映射到统一字段（不重新请求旧内容）；自动下载开关；Bilibili 空间视频列表补浏览器参数与固定向量（LOOP-002） | 两站现有全部守护保持；规则状态与 M1 真实证据一致 |
| P11 / P3～P10 | 界面：创作者页多站关联（粘贴任一站主页）、统一时间线、资源管理器页面、设置页（资源库与下载位置、命名、代理、站点登录与风险提示）、支持的网站页、预览勾选页、订阅设置 | [多站契约 §8](../design/multi-site-contracts.md#explorer)；十万条规模的树与列表分页、排序、筛选；恶意文件名不越界；中文输入、键盘、窄窗、空态、错误与部分成功 |
| P12 / P1～P11 | 证据与包：新门禁覆盖本阶段需求与规则状态；便携包携带新工具资产与许可；正式资源测量按原规模与阈值重跑；把 LOOP-001 的真实验证驱动整理成通用脚本 | 旧包与旧长测不换绑；规则 `implemented` 必须对应报告中的真实证据；包内无测试端点与凭证 |
| P13 / P12、P14 | 按 ACT-007 在最终包上做真实站点验证；完成交付报告与 B→A prompt。§11 的功能与 DEC-039/041 都会改包，所以 P13 必须在 P14 和这两项完成后重新打包再执行，之前取得的包证据也要换绑到新包 | 每站：账号识别、列表 2 页、自动下载开/关各一次增量、单链接下载（视频与图文各一）、预览勾选下载到下载目录；遇挑战/限流/封禁即停并记录 |
| P14 / P2、P8、P11 | DEC-040 的九项管理与插件能力、DEC-039 的 yt-dlp 手动更新、DEC-041 的小红书完整历史；详细工作包、测试与证据见 §11、§12 | 每项需求的验收条件有离线反例；需要真实站点的规则在最终包上重新核对；管理功能不改来源 ID、文件身份与已有档案 |

P8 的各站点可以交错，但每个站点先完成一条“用户发布内容”订阅规则的垂直闭环再扩展通道；界面与恢复不能全部留到最后。

<a id="stage-cleanup-list"></a>
## 4. 阶段整理清单

A 于 2026-10-16 盘点：`artifacts/` 约 74 GB（644 项），`target/` 约 72 GB，`dist/` 31 个包约 6 GB。按[阶段开始的整理](../governance/documentation-policy.md#stage-cleanup)处理：

| 路径/类别 | 入库 | 判断与处理 | 提炼去向 |
| --- | --- | --- | --- |
| `docs/delivery/stage-plan.md` | 是 | A 已改写为本计划 | M1 计划见固定链接 `c1552e0` |
| `docs/delivery/stage-report.md` | 是 | A 已把各文档指向它的链接改为固定链接，并为本阶段重建空报告；B 在新报告里写交付 | M1 报告见固定链接 `c1552e0` |
| `docs/delivery/status.md`、`docs/research/*`、`docs/site-archive-contracts.md`、`docs/design.md`、`docs/dev-rules/*`、`docs/evidence/README.md` | 是 | 精炼：删去按轮次追加的过程叙述、旧 SHA 流水账与以前阶段的标签，只留当前结论与链接 | 状态、有效决定、路线 |
| `docs/design/m1a-runtime-contracts.md` | 是 | 名字带阶段标签；仍有效的运行契约提炼进技术设计/归档契约后删除，或改为按能力命名 | 技术设计、归档契约 |
| `docs/delivery/requirements-map.json` | 是 | 去掉 `m1`/`r2`/`r3` 分组，只保留当前有效证据一份；字段按 P1 的新门禁调整 | 映射 |
| `docs/evidence/gate/*.json` | 是 | 只留最近一次阶段门禁摘要，其余删除 | 状态、固定链接 |
| `tests/e2e/tests/` 中按轮次命名的文件、`tests/e2e/src/bin/m1_*`、测试函数的 `c01_`/`f009_`/`m1_`/`r3_` 前缀 | 是 | P1 R7 按能力重命名与归组；断言不删不弱化 | 迁移映射写进报告“整理记录” |
| `scripts/measure_m1.py`、`real_chain_gate.py`、`real_source_evidence.py` 等按阶段命名或一次性的脚本 | 是 | P1 R8 并入统一入口或改按能力命名；无使用方的删除 | 整理记录 |
| `artifacts/`（约 74 GB） | 否 | 已提炼的跑次删除；保留 ACT-003 在用的 `artifacts/closeout/acceptance/` 及其恢复目录、仍被状态或 USER-ACTIONS 引用的失败证据（先抄出数值再决定） | 状态 |
| `dist/`（31 个包） | 否 | 保留 ACT-003 在用的 `trace-0.1.0-windows-x64-closeout-4` 与最新包；其余删除 | — |
| `target/`（约 72 GB） | 否 | `cargo clean` 后重建 | — |
| `.local/` | 否 | 一次性脚本提炼后删除；`.local/site-secrets/`、`.local/site-check/` 中用户提供的目标与 Cookie 保留 | — |
| `.env.local`、Profile、`node_modules`、本地参考目录 | 否 | 保留不动 | — |

清理前解析绝对路径，确认在明确的生成目录内且不是指向真实资料的 junction/symlink；不用跨目录通配或 `git clean -fdx`。

## 5. 必须的定向反例

| 用例组 | 必须断言 | 需求 |
| --- | --- | --- |
| C01 重构不变 | R0 基线测试清单与断言数对账；旧库、旧备份、旧插件协议、命令名与参数形状不变；两站现有全部守护通过 | 全部已实现需求 |
| C02 规则目录 | 未验证不能 `implemented`；`partial` 必须有限制说明；支持页零网络；链接匹配到正确规则；插件停用仍显示 | FR-SITE-001/002；AC-38 |
| C03 统一元数据 | 每站字段映射；取不到为空；统计变化不新建内容版本；Cookie/令牌/签名地址不进元数据与旁注 JSON；排序与筛选结果正确 | FR-SITE-005；AC-31 |
| C04 代理与网络 | 代理下非声明域名、字面 IP、私网与回环目标拒绝；重定向逐跳检查；Cookie 不跨站；代理失败归为网络问题；`198.18.0.0/15` 虚拟地址直连保持可用；系统代理开关为开但地址为空时按直连 | FR-SITE-006；AC-38 |
| C05 下载位置 | 临时目录写入后改名；非法字符、保留名、超长、冲突、不可写、空间不足；外部改动检测；同步入库去重 | FR-ARC-011、FR-DATA-002；AC-33/35 |
| C06 资源库迁移 | 中断续做；校验失败不切换；切换在一次提交里完成；旧位置保留到确认；迁移后备份与恢复 | FR-ARC-008；AC-35 |
| C07 自动下载与同步 | 开、关、来回切换；已有订阅迁移为开；未下载不等于缺失或失败；手动同步幂等；部分失败可重试；补查不前移游标 | FR-SUB-007/008/009；AC-34 |
| C08 预览勾选 | 不下载媒体、不建订阅；跨页勾选；页数与预算上限；已在库标识；预览过期后重新解析 | FR-ARC-010；AC-33 |
| C09 新站点协议 | 每站：账号改名、分页重复/空页/置顶/乱序、混合媒体顺序、长文全文、实况图、转发/引用关系、需要登录/挑战/限流/签名失效分类、空响应不当作没有更新、短链展开边界、签名固定向量 | FR-SITE-003/004；AC-31/32 |
| C10 YouTube 工具 | 固定参数、超时、输出上限、不传 Cookie、禁止自我更新、工具缺失或版本不符时降级、临时目录提交前校验 | FR-SITE-004；OSS-012 |
| C11 界面 | 资源管理器十万条分页与排序、树懒加载、恶意文件名；统一时间线多站排序与筛选、混合卡片、未知发布时间；设置保存与校验；中文输入、键盘、窄窗 | FR-BROWSE-008/009、FR-ID-006；AC-36/37 |
| C12 门禁与包 | 新工具资产入包并有摘要与许可；修改文档不让包证据过期；规则状态与真实证据一致；缺必需用例、陈旧源码指纹、失败基准必须拒绝 | 质量门禁 |
| C13 创作者与账号整理 | 合并、拆分、重新关联只改显式关系；来源 ID、条目、对象摘要与文件路径不变；自合并、环、重复关联、已合并目标被拒绝；操作记录可查；资料观察按时间保留；相似昵称只产生待确认候选，确认前不改关联，拒绝后仍保留观察 | FR-ID-003、FR-ID-005 |
| C14 导入与比较 | 托管复制不改动源文件；原地索引的外部文件不移动、不回收；中断、取消与恢复不产生“已完成”的假象；同字节复用对象；变化后的外部文件标为 changed/offline 而不是覆盖旧观察；精确重复按摘要分组；相似候选不自动删除或合并，用户决定被记录 | FR-ARC-004、FR-ARC-006；AC-14 |
| C15 质量筛选与跨平台聚合 | 待升级、不可比较、已替换、手动保留可筛选和处理；固定保留的版本不被自动回收；变更策略后重新评估；聚合组保留每条文案与来源；批量操作先说明对象和影响范围，失败可逐条重试 | FR-QUALITY-005、FR-BROWSE-006 |
| C16 插件生命周期与能力 | 摘要或协议不符的安装/启用被拒绝；升级并排安装，回退到上一兼容版本；移除插件不删档案；登录辅助、通知、存储、内容处理各走自己的契约，未声明的方法被拒绝，关闭全部可选扩展后基础归档与档案读取仍通过 | FR-PLUGIN-005、FR-PLUGIN-007；AC-18、AC-24 |
| C17 录制与回放关联 | 回放剪辑或缺少区间时，录制独有区间与分片完整保留；账号不匹配、未封口、重复关联被拒绝；质量不可比较时不能选“回放为主”；不自动替换或回收 | FR-LIVE-006 |

以上是最低反例集，B 仍须核对“本阶段”全部需求行。

## 6. 命令、默认值与真实条件

- 现有入口：`python scripts/verify.py --all`、`cargo fmt --all -- --check`、`cargo clippy --workspace --locked --all-targets -- -D warnings`、`python scripts/check_docs.py`、`python scripts/check_publication.py`、`python -m unittest discover -s scripts/tests`、`git diff --check`、`python scripts/package_windows.py`。P1 R8 建立统一入口后，以新入口为准并更新[质量门禁](../dev-rules/quality-gates.md)。
- 用户决定的当前默认（都可逆，不阻塞）：新订阅“自动下载视频”默认关闭（[Q-010](../../USER-ACTIONS.md#q-010)）；海外四站默认跟随系统代理（[Q-008](../../USER-ACTIONS.md#q-008)）；本地包携带 yt-dlp 与 Deno（[Q-009](../../USER-ACTIONS.md#q-009)）；资源管理器与下载命名按[多站契约](../design/multi-site-contracts.md#explorer)（[Q-011](../../USER-ACTIONS.md#q-011)）。
- 用户本轮答复的落地方式：yt-dlp 只保留随包锁定版本，另加设置页的手动“检查更新”，按 [DEC-039](../decisions/confirmed-decisions.md#dec-039) 校验签名与摘要、并排安装、空闲切换和回退，不自动检查、不用 `-U`；九项管理与插件能力按 [DEC-040](../decisions/confirmed-decisions.md#dec-040) 进入本阶段（§11），需求状态先改为“本阶段”，B 交付并通过 A 审查后才改为“已实现”；小红书订阅与主页预览按 [DEC-041](../decisions/confirmed-decisions.md#dec-041) 用 `a1` 签名取到全部历史，签名被站点拒绝时停止并标为“历史未取完”，不换参数重试。微博新 Cookie 已在 2026-10-18 由 A 完成真实登录对照（[研究 §13](../research/social-sites-current.md#weibo-recheck-2026-10-18)），S-001 不再阻塞。
- [ACT-007](../../USER-ACTIONS.md#act-007) 已于 2026-10-16 授权：对象是用户给的每站一个创作者主页（只记在本机 `.local/site-check/m2-targets.json`），单链接对象从这些创作者自己的公开作品里选；海外四站测试走 `.env.local` 的 `TRACE_TEST_PROXY`。A 已做一次登录与目标探测（[研究 §10](../research/social-sites-current.md#login-probe-2026-10-16)），八站都可用；Instagram 按浏览器路径实现（[研究 §10 说明](../research/social-sites-current.md#instagram-browser-path)），不以 `web_profile_info` 为主路径。真实站点只按 ACT-007 写明的对象、预算与 Cookie 执行；先完成离线反例再做真实验证；挑战、验证码、限流、封禁、权限不明即停止并记录，不重复试探、不绕过。抖音与哔哩哔哩沿用 ACT-002 的 Cookie 与样本，因重构需要复验时按 ACT-007 中两站的新预算执行。
- 正式资源测量按原规模、时长与阈值重跑；真实样本不能代替合成正式长测。

## 7. B 交付与 A 审查

B 在新的[阶段报告](stage-report.md)里按 P/C 写实现位置、实际命令与退出码、源码与包、失败与复验、未执行边界、整理记录（含迁移映射），以及每条规则的状态与真实证据。B 自检通过不写成 A 已通过。新发现的问题从 F-037 起编号。

A 先核对被审版本、完整差异与生产接线，再逐 C 核查故障断言与每站规则的真实证据，不以全套绿色代替语义审查；局部问题直接修，较大缺口一次性交回 B。A 通过后直接给用户结论与远端授权建议。

<a id="agent-b-prompt"></a>
## 8. A → B 实施 prompt

```text
请作为 Agent B，在 TRACE 仓库分支 codex/m2-multi-site-archive（基点 main@c1552e048ea608ba64deae820d127bbd5e969083；A 的计划提交是本阶段唯一提交的起点）连续完成 TRACE-M2-01 r1：docs/delivery/stage-plan.md 的 P0～P13 与 C01～C12。
先读 AGENTS.md、REVIEW.md、docs/README.md、docs/delivery/status.md、docs/decisions/confirmed-decisions.md（DEC-031～034）、USER-ACTIONS.md，以及 docs/design/multi-site-contracts.md、docs/design/code-structure-refactor.md、docs/research/social-sites-current.md、docs/research/open-source-candidates.md#social-sites-2026-10-16；按本地参考规则查参考索引对应的功能条目，参考只读、公开文件不写项目名与本机路径。
顺序：P0 阶段整理 → P1 行为不变的代码整理（每步全部门禁通过，测试与断言数对账）→ P2～P7 契约、目录、代理、下载位置、自动下载与同步、预览勾选 → P8/P9 六个新站点与 YouTube 外部工具 → P10 两站对齐 → P11 界面 → P12 门禁与包、正式资源测量 → P13 真实验证与报告。
必须保留：两站现有订阅、链接下载、直播录制、认证历史、质量升级、导出备份恢复的行为与全部断言；适配器不接触 Cookie；限流/挑战/需要登录不等于没有更新或内容删除；已有订阅迁移后保持自动下载。
用户决定按 USER-ACTIONS 的当前默认继续（Q-008～Q-011），不等答复。真实站点只在 ACT-007 授权后、按其中对象与预算执行，遇挑战/限流/封禁即停；做不到的站点规则如实标 partial/unimplemented 并写明尝试过的方案，继续其余部分。
连续完成实现、自检、修复与文档同步，不逐项请求继续；不 push、不建 PR。完成后把实现、命令与结果、规则状态与真实证据、整理记录写进 docs/delivery/stage-report.md，amend 进同一个带 DCO 签名的阶段提交，并给出完整的 B→A 审查 prompt（含最终 SHA、报告入口、未完成项与限制）。需要用户的事只写进 USER-ACTIONS。
```

<a id="a-review-rework"></a>
## 9. A 审查后的返工（2026-10-18）

A 对 B 第一轮交付的审查见[阶段报告 §5](stage-report.md#a-m2-review)。结论：未收敛，以下缺口一次性交回 B，仍在本阶段唯一提交内 amend；计划 ID 不变。A 已直接修好的局部问题（报告 §5.3）不再列入。新缺陷从 F-051 起编号：A 已修的是 F-051～F-053，返工的是 F-054～F-057。

| ID | 完整结果 | 关键失败与完成证据 |
| --- | --- | --- |
| R2-1 Cookie 域名（F-054） | 站点清单增加 Cookie 域名：微博 `weibo.com`、小红书 `xiaohongshu.com`、Instagram `instagram.com`、X `x.com`、TikTok `tiktok.com`；YouTube 不带 Cookie；抖音、哔哩哔哩保持现状。其余接口域名（`weibo.cn`、`t.cn`、`twitter.com`、`t.co`、`xhslink.com`）只发无 Cookie 的请求 | 反例：每站 Cookie 不发往其他注册域；两站现有守护不变；[多站契约 §9](../design/multi-site-contracts.md#proxy) |
| R2-2 没有日期的列表（F-055） | 列表条目没有发布日期时，Core 在详情给出确切日期后再判断是否在订阅范围内：范围外的条目不自动下载、不计入本次更新的发现数；列表的提前截止可以用站点给的近似日期（YouTube 的“N 天前”，精度标为近似，留一个单位的余量）。YouTube、小红书都按这条处理 | 反例：“最近 7 天”的 YouTube 订阅面对 100 条无日期的历史，只翻有限页、只自动下载范围内的作品；详情日期在范围外的作品不下载；精度正确入库；[多站契约 §5.3](../design/multi-site-contracts.md#undated) |
| R2-3 TikTok 视频文件（F-056） | 视频地址在 `*.tiktok.com` 时，媒体请求带上生成该地址的页面请求所用的同一组 Cookie（凭证 Cookie，加 Core 在内存里记下的该站响应设置的会话 Cookie；不落盘、不给适配器、不发往 Cookie 域名之外）；CDN 域名仍不带 Cookie。仍被拒绝时，改用已随包的外部解析工具在临时目录下载（与 YouTube 同样的约束） | 反例：`tiktokcdn` 等 CDN 永不带 Cookie；会话 Cookie 不跨作用域、撤销即清除；真实单链接视频与预览勾选下载通过 |
| R2-4 X 列表 | 查询标识与特性开关作为可单独更新的数据随适配器发布（同 Instagram 的 `data/` 做法），过期时才从站点脚本发现，发现结果有负缓存；站点要求时计算事务 ID 请求头（按 MIT 许可的公开算法自写，带固定向量） | 列表 2 页、视频与图文单链接、预览勾选下载、两次增量；未做到时如实写限制 |
| R2-5 账号解析（F-057） | `account.resolve` 恢复上一阶段行为：给出的凭证状态不是有效时拒绝（需要重新登录），不带过期 Cookie 去请求；适配器给的 `profile_url` 只接受该站自己的域名 | 反例：失效凭证被拒且零请求；其他域名的主页地址不入库 |
| R2-6 请求标识 | 六个新站点适配器改用格式完整的桌面浏览器 User-Agent（含 `(KHTML, like Gecko)`）；抖音、哔哩哔哩的签名固定向量依赖原值，核对后不变 | 固定向量与夹具全部通过 |
| R2-7 小项 | 诊断日志的说明改为“只写方法、分类和适配器自己的简短描述（可含页面形态，不含正文与凭证）”；登录证明经 `credential.revoke` 命令清除的测试；`LOOP-009` 基准持续时间改为工作负载结束时间 | 对应测试 |
| R2-8 真实站点第二轮 | 先完成 R2-1～R2-7 的离线反例并重打包，再按 [ACT-007](../../USER-ACTIONS.md#act-007) 的新额度、用新包执行；每站的检查清单不变（P13）。微博、Instagram 的登录探测已由 A 修正并用真实页面核对（报告 §5.2），Cookie 不需要重新复制。顺序建议：抖音增量 → 哔哩哔哩视频通道、浏览器参数与增量 → YouTube 增量、Shorts、帖子 → 微博 → Instagram → 小红书复验 → TikTok → X | 订阅规则只有账号识别、列表 2 页和两次增量都通过才是 `implemented`（`rule_evidence.py` 已改）；挑战、限流、登录失效即停 |
| R2-9 包、测量与人工检查 | 重打包、按原规模重跑一小时基准、重新生成门禁报告；为 ACT-003 准备本阶段的新包与合成档案（八站内容、下载文件夹、外部副本），并补人工步骤：资源管理器、设置页的文件夹选择与“在资源管理器中打开”、支持的网站、预览勾选，以及这些页面里的中文输入与缩放 | `stage.py check/report` 通过；ACT-003 步骤可直接照做 |
| R2-10 报告 | 在报告 §2、§4 记录本轮实现、命令与结果、真实证据；`requirements.md` 的本阶段状态仍等 A 验收 | B 自检不写成 A 已通过 |

站点失败的参考做法（只读、仓库不写项目名）：本地参考索引中“类 RSS 订阅规则”首选项目的 X 路由（查询标识回退表、从站点文档更新、事务 ID 头）、Instagram 与 YouTube 路由；“抖音签名与访问上下文”首选项目的 TikTok 媒体请求（Referer 与完整 User-Agent、媒体域名清单）。小红书续页原按“依赖 Cookie 值、本阶段保持 partial”处理，该结论已被 [DEC-041](../decisions/confirmed-decisions.md#dec-041) 取代：只把设备标识 `a1` 交给适配器计算签名，按 §12 的 R4-4 完成完整历史和固定向量；站点拒绝签名时仍按拒绝停止。

<a id="agent-b-rework-prompt"></a>
### 9.1 A → B 返工 prompt

```text
请作为 Agent B，在 TRACE 仓库分支 codex/m2-multi-site-archive 继续 TRACE-M2-01：完成 docs/delivery/stage-plan.md §9 的 R2-1～R2-10。
先读 docs/delivery/stage-report.md §5（A 的审查、诊断与已做修复）、stage-plan.md §9、docs/design/multi-site-contracts.md 的 §5.3、§9、§10、§11，以及 USER-ACTIONS.md 的 ACT-007（2026-10-18 用户授权的新累计额度；微博、Instagram 的 Cookie 有效，不需要重新复制）。A 的修复已 amend 进阶段提交（SHA 以 `git log -1` 为准），工作区应当是干净的。
顺序：R2-1～R2-7 的实现与离线反例 → 重打包 → R2-8 真实站点第二轮（新包、新额度，遇挑战/限流/登录失效即停）→ R2-9 基准、门禁报告与 ACT-003 新样本 → R2-10 报告。
必须保留：两站现有全部守护；适配器不接触 Cookie；Cookie 只发往该站的 Cookie 域名；限流、挑战、需要登录不等于没有更新；订阅规则只有账号识别、列表 2 页和两次增量都通过才是 implemented。
做不到的站点规则如实标 partial 并写明尝试过的方案，继续其余部分。不 push、不建 PR。完成后 amend 进同一个带 DCO 签名的阶段提交，并在报告里给出完整的 B→A 审查 prompt（含最终 SHA、未完成项与限制）。需要用户的事只写进 USER-ACTIONS。
```

<a id="a-rereview-rework"></a>
## 10. A 复审后的第三轮（2026-10-18）

A 对 B 第二轮的复审见[阶段报告 §6](stage-report.md#a-m2-rereview)：R2-1～R2-7 的离线返工与 F-063 通过；A 已修 F-064（微博登录探测与“需要登录”）和规则文案（F-065 的文案部分）。用户同日把 ACT-007 每站额度翻倍（DEC-038），答复 Q-008（八站都可经用户的本机代理，DEC-035）、Q-010（默认关闭，DEC-036），并要求站点规则做不到时统一记入 USER-ACTIONS（DEC-037）。剩下的交回 B，仍在本阶段唯一提交内 amend；计划 ID 不变，新缺陷从 F-068 起编号。

| ID | 完整结果 | 关键失败与完成证据 |
| --- | --- | --- |
| R3-1 最终包上的证据（F-066、F-067） | 先完成 R3-3 的脚本改动与其余离线修复，再重打包；第三轮全部真实检查都用这个最终包。每站在最终包上补一次账号识别与第一页列表（约 2～4 次请求），让已 `implemented` 的规则对应最终包；之后再改产品代码就要说明影响，必要时对受影响的站点重做冒烟。公开记录写明所用包名与 `SHA256SUMS.txt` 摘要。每次手动诊断请求都在 `.local/site-check/` 留只含形态与计数的回执 | `rule-evidence.json` 的每站 `run` 指向最终包；台账、公开记录与回执逐站一致 |
| R3-2 按用户环境走代理（DEC-035） | 隔离 Profile 里用 `network.set` 把“全部网站（全局）”设为手动代理（`.env.local` 的 `TRACE_TEST_PROXY`），八站都经它，包括国内四站；不在检查里临时改回直连。某站只在代理下失败时停止并记录，不静默换路线 | 回执里每站的路线是“代理 · 全局设置”；代理下的域名边界、Cookie 作用域（C04）在真实站点上没有放宽 |
| R3-3 没有日期的列表的增量检查（F-065） | `real_site_check.py` 为列表不给日期的站点（小红书；YouTube 列表没有日期时同样适用）设计增量检查：例如先用详情或单链接得到的确切日期选对象，用“最近 N 天”的范围让订阅的第一次检查由详情定位；检查要证明范围外的条目不下载、不计入发现数，第二次检查不重复取详情；每次检查的请求数写进回执。必要时每个增量检查用新的隔离 Profile | `test_real_site_check.py` 覆盖无日期列表；小红书两次增量（自动下载关、开）有真实结果，或如实写明停在哪一步 |
| R3-4 用翻倍的额度补完检查 | 按 [ACT-007](../../USER-ACTIONS.md#act-007) 的新额度：抖音、哔哩哔哩两次增量（哔哩哔哩含视频通道、浏览器参数 LOOP-002、勾选下载）；YouTube 自动下载开的增量、Shorts 与社区帖子订阅；TikTok 两次增量（限流已过）与图集作品链接；X 往后多翻几页找原生视频，做视频推文链接与两次增量（找不到就写明翻了几页）；小红书按 R3-3。顺序建议：抖音 → 哔哩哔哩 → YouTube → 小红书 → TikTok → X | 订阅规则只有账号识别、列表 2 页和两次增量都通过才是 `implemented`；挑战、限流、登录失效即停；额度不是要用完的目标 |
| R3-5 微博、Instagram（用户事项） | 只在用户回复 [S-001](../../USER-ACTIONS.md#s-001)（新微博 Cookie）或 [S-002](../../USER-ACTIONS.md#s-002)（Instagram 目标）之后做。微博先用一次登录探测核对“已登录”一侧的形态：配置接口的回答与 A 的判据（`ok: 1` 且答出用户 ID 或 `login: true`）不符时停止，按回答形态（不含值）修判据并加测试，再做其余检查。用户在第三轮结束前没有回复的，保持 `partial`，S 条目保留 | 微博登录探测的“已登录”判据有真实的已登录与未登录对照 |
| R3-6 规则文案与记录 | 按真实结果更新八站规则的状态、日期与限制；抖音、哔哩哔哩限制里重复、过时的句子合并；`rule-evidence.json` 的链接检查按作品种类分别记录（例如 TikTok 视频链接通过、图集未执行），不只记最差结果。新发现的“规则不对且没有思路”的情况按 DEC-037 记进 USER-ACTIONS 的 S 一节 | `rule_evidence.py check`、`rule_status_matches_recorded_real_evidence` 通过 |
| R3-7 包、测量、门禁与人工检查 | 最终包的一小时基准、`stage.py check/report`；ACT-003 改指最终包（摘要、文件数），合成档案的生成器没变时可沿用 `artifacts/m2-acceptance/`，变了就重建 | `stage.py check/report` 通过；ACT-003 可直接照做 |
| R3-8 报告 | 在报告 §2、§4 记录第三轮实现、命令与结果、真实证据；`requirements.md` 的本阶段状态仍等 A 验收 | B 自检不写成 A 已通过 |

R3-1 的“最终包”只在 §11 与 DEC-039/041 落地前有效；这些改动会改产品代码与包，R3 已取得的包证据必须按 §12 的 R4-8/R4-9 重新换绑。R3-5 的微博部分已由 A 在 2026-10-18 用真实已登录/未登录对照解决（[研究 §13](../research/social-sites-current.md#weibo-recheck-2026-10-18)），B 不再重复修判据，只需在最终包上做微博的其余检查；Instagram 部分仍等 S-002。

<a id="agent-b-rework-3-prompt"></a>
### 10.1 A → B 第三轮 prompt

```text
请作为 Agent B，在 TRACE 仓库分支 codex/m2-multi-site-archive 继续 TRACE-M2-01：完成 docs/delivery/stage-plan.md §10 的 R3-1～R3-8。
先读 docs/delivery/stage-report.md §6（A 的复审、F-064～F-067 与 A 的修改）、stage-plan.md §10、USER-ACTIONS.md 的 ACT-007（2026-10-18 额度翻倍，机器可读一行已改）、站点规则暂时做不到的情况（S-001～S-003）、Q-008 与 Q-010 的答复（DEC-035、DEC-036），以及 docs/research/social-sites-current.md §13。A 的修改已 amend 进阶段提交（SHA 以 git log -1 为准），工作区应当是干净的。
顺序：R3-3 检查脚本对无日期列表的增量检查与其余离线修复 → 重打包（最终包）→ R3-2 隔离 Profile 设全局手动代理 → R3-1/R3-4 每站在最终包上冒烟并补完剩余检查（新额度，遇挑战/限流/登录失效即停）→ R3-5 只在用户回复 S-001/S-002 后做微博、Instagram → R3-6 规则与记录 → R3-7 基准、门禁与 ACT-003 → R3-8 报告。
必须保留：两站现有全部守护；适配器不接触 Cookie；Cookie 只发往该站的 Cookie 域名；限流、挑战、需要登录不等于没有更新；订阅规则只有账号识别、列表 2 页和两次增量都通过才是 implemented；登录判据要有不带 Cookie 的真实对照。
做不到的站点规则如实标 partial 并写明尝试过的方案；规则不对又没有思路的，按 DEC-037 记进 USER-ACTIONS 的 S 一节。不 push、不建 PR。完成后 amend 进同一个带 DCO 签名的阶段提交，并在报告里给出完整的 B→A 审查 prompt（含最终 SHA、未完成项与限制）。需要用户的事只写进 USER-ACTIONS。
```

<a id="m2-management-scope"></a>
## 11. 本阶段纳入的管理与插件能力（DEC-040）

用户 2026-10-18 答复 Q-012：原先按 DEC-031 顺延的九项管理/插件能力在本阶段完成。需求状态从“待实现”改为“本阶段”只表示已排期，不表示已验证；B 接上生产实现与测试后仍由 A 在最终审查时决定是否改为“已实现”。本节的工作包与 §3 的 P 包同属一个阶段、一个分支、一个提交，不另开分支或 PR。

共同基础（每个 M 包都只使用自己需要的部分）：

- **数据：** schema 前进到 v8，新增迁移 `crates/core/src/db/migration_v8.rs`，由现有迁移器在备份快照之后执行。一次迁移建立本节各包所需的表；旧库、旧备份和旧程序拒写新库的反例沿用现有迁移测试。新表至少包括 `creator_redirects`、`creator_operations`、`identity_candidates`、`live_replay_links`、`import_batches`、`import_items`、`resource_signatures`、`resource_comparisons`、`work_groups`、`work_group_members`、`plugin_releases`；`account_observations` 增加 `profile_json`。所有外键、唯一约束和索引与命令的幂等要求一致。
- **契约：** 新命令加进 `crates/contracts/src/control/commands.rs` 的 `UI_COMMANDS` 与参数类型，沿用现有 `Request`/`parse` 校验和 Tauri 通用转发；同步生成 TS 类型与 `apps/desktop/src/lib/api.ts` 包装。列表命令都有 `after`/游标和上限，写命令返回实际影响的对象与数量，不用“成功”掩盖零对象。
- **审计与事件：** 合并、拆分、重新关联、关联确认/拒绝、回放关联、导入、比较决定、批量操作、插件安装/升级/回退都写一条可查询的操作记录；需要界面刷新的写操作发 outbox 事件。记录只含稳定 ID、动作和计数，不含 Cookie、签名值、短期 URL、正文或本机绝对路径。
- **界面：** 复用现有页面与组件；新增路由、导航项和组件必须走现有 hash router 与页面样式，中文输入、键盘、窄窗、空态、失败和部分成功都要有。每个 M 包交付自己的界面入口，不把全部界面留到最后。
- **安全边界：** 适配器仍不接触 Cookie（唯一例外是 DEC-041 的 `a1`）；管理命令不改写来源 ID、原始正文、对象摘要或托管文件路径；精确重复和相似候选都不自动删除、移动或合并；原地索引的外部文件永远不由本应用移动或回收。

### 11.1 M1 创作者合并、拆分与重新关联（FR-ID-003）

**完整结果：** 用户可以在创作者页把一个创作者合并到另一个、把选中的账号拆到新创作者、把单个账号重新关联到另一个创作者；三种操作都先显示受影响账号/订阅/条目数量，确认后执行。来源账号 ID、条目 ID、条目来源键、对象摘要、文件路径和已保存正文都不变；只改显式关系，并留下可查的操作记录。

**实现要点：**

- `creator.merge`：源创作者必须存在且未合并；目标不能是源自己、已合并的创作者或形成环；在一个事务里把源的有效 `account_bindings` 改到目标，源 `creators.status` 记为 `merged`，写 `creator_redirects(source→target)` 与操作记录。旧 ID 的 `creator.get` 跟随一次重定向并在结果里给出 `merged_into`；`creator.list` 默认不把已合并创作者当独立项。已绑定账号的条目因此自然归到目标，**不更新 entries、blobs、replicas 或文件**。
- `creator.split`：为选中且当前有效绑定在源创作者下的账号创建新创作者，移动这些绑定并记录前后 ID；至少选一个账号；源创作者保留，即使已没有账号。拆出的账号历史与订阅不复制、不改写。
- `creator.relink`：把指定账号移到目标创作者；请求必须带当前创作者作为预期，实际不同则冲突而不是“成功”。同一账号仍只有一条有效绑定。
- `creator.operations`：按时间倒序分页返回操作类型、源/目标、账号快照和计数；不返回正文或凭证。
- 时间线、账号列表和订阅页按创作者筛选时解析重定向；只允许一层，写入时拒绝链。

**关键失败与证据：** 自合并、环、目标已合并、重复绑定、拆分空集合、过期预期都返回明确错误；合并前后条目数、对象摘要与文件定位逐条相同；操作记录可查；旧创作者链接仍能打开目标。测试至少覆盖 `creator_merge_keeps_archive_identity`、`creator_merge_rejects_cycles`、`creator_split_moves_selected_accounts_only`、`creator_relink_checks_expected_source`、`creator_operations_are_paginated`、`timeline_follows_creator_redirect`。界面在 `CreatorsView` 增加合并/拆分/重新关联对话框和操作记录；确认文案写明“不移动或删除已保存内容”。

### 11.2 M2 资料观察历史与待确认身份关联（FR-ID-005）

**完整结果：** 每次账号资料观察按时间保存昵称、简介、头像地址、主页地址和观察证据；用户可以翻看历史。相似昵称或资料只生成“待确认”的关联候选，确认前不改变任何绑定；确认是显式操作，拒绝也留记录。

**实现要点：**

- 现有 `account_observations` 已保存昵称/时间/证据；增加 `profile_json`，只放白名单字段（显示名、简介、头像 URL、主页 URL、平台资料字段），不含 Cookie、令牌、签名值或完整响应。`account.profile_history` 按 `sequence` 倒序分页，默认 20 条。
- `identity_candidates(candidate_id, account_id, proposed_creator_id, kind, score, evidence_json, state, created_at, decided_at)`：候选只由已观察资料与现有绑定产生，例如同站另一账号的昵称与某创作者已绑定账号相似；相似度用规范化（大小写、全半角、空白）后的明确规则，阈值和证据写进记录。候选只建议“把这个账号关联到这个创作者”，不自动绑定、不自动合并账号。
- `identity.candidates` 列待确认项；`identity.confirm` 在账号未绑定时显式绑定，已绑同一创作者时幂等，已绑其他创作者时冲突；`identity.reject` 只改候选状态。确认/拒绝都不改历史观察。
- 资料历史与候选的写入在资料观察事务内完成；重复观察同一资料不重复产生同一条待确认候选。

**关键失败与证据：** 相似昵称不会自动改绑定；确认前零绑定变化；确认冲突不覆盖已有绑定；拒绝后候选不再出现但观察仍可查；资料证据不含敏感值；同名不同平台不互相候选（候选只在同站账号间产生）。测试覆盖 `profile_history_keeps_observations`、`similar_names_are_candidates_only`、`identity_confirm_is_explicit_and_conflict_safe`、`identity_reject_keeps_history`、`profile_evidence_has_no_credentials`。界面在 `AccountsView` 增加资料历史与候选确认区。

### 11.3 M3 录制与回放的关联（FR-LIVE-006）

**完整结果：** 用户可以把一场已封口录制与随后出现的回放条目关联；Core 比较录制覆盖区间、回放时长与质量，明确显示回放缺少的区间。回放被剪辑或缺少区间时，录制中的独有内容保留，不自动替换、不自动回收；覆盖完整且质量可比时，用户可以选择以回放为主，但录制分片仍保留。

**实现要点：**

- `live_replay_links(link_id, session_id, entry_id, state, coverage_json, quality_json, decision, evidence_json, created_at, decided_at)`，同一场次与条目唯一。`state` 为 `candidate`/`confirmed`/`rejected`；`decision` 为 `keep_both`/`replay_primary`（不提供自动删除）。
- `live.replay.candidates` 只给出同账号、时间范围重叠的已归档条目作为候选，不自动确认；用户也可以从条目侧指定场次。`live.replay.link` 校验条目与场次属于同一来源账号，计算覆盖：录制分片 `start_ms`/`end_ms` 的并集、缺口、录制总时长；回放时长取已测规格；质量比较复用 `quality::comparison`，不可比较时只允许 `keep_both`。
- 关联本身不删除任何分片、媒体项或对象。回放缺少录制区间时写 `unique_recorded_ranges`，界面显示“录制独有内容已保留”；即使选择 `replay_primary`，原录制仍可打开和导出。
- `live.replay.reject` 记录拒绝及原因；重复关联幂等。

**关键失败与证据：** 回放剪辑时独有区间和分片数不变；账号不匹配、场次未封口、条目已删除、重复关联都明确拒绝；不可比较质量不能选 `replay_primary`；无自动替换或回收。测试覆盖 `clipped_replay_keeps_recorded_ranges`、`replay_link_requires_same_account`、`incomparable_quality_keeps_both`、`replay_reject_is_recorded`。界面在 `LiveRecording`/条目详情增加“关联回放”和覆盖对比。

### 11.4 M4 本地文件导入（FR-ARC-004）

**完整结果：** 用户从本机目录或文件导入内容，选择“托管复制”或“原地索引”；导入有预览、进度、取消、失败重试和中断恢复。托管复制把文件复制进对象库，源文件不删除；原地索引只记录外部文件的观察与哈希，不移动、改名或回收它。没有来源信息的文件进入“待整理”，之后可由用户显式归到创作者。

**实现要点：**

- `import.inspect`：只读预览路径、文件数、总大小、是否跟随目录链接（默认不跟随）、重复项和不可读项，不复制、不建条目、不耗大量哈希时间。
- `import.start`：创建 `import_batches` 与逐文件 `import_items`，后台任务逐个处理；`import.status`、`import.cancel`、`import.resume`。状态至少 `pending`/`copying`/`hashing`/`committed`/`failed`/`cancelled`；取消在文件边界生效，已提交项不回滚。
- 托管复制：流式复制到对象库 staging，边写边算 SHA-256，核对大小与读取前后观察；`finish_and_validate`/`install_object` 的现有提交规则不变；文件先落到临时位置，校验通过后才生成 `blobs`/`replicas(managed=1)` 和条目。复制中源文件变化或读失败则该项失败并可重试，不把变化中的字节当稳定归档。
- 原地索引：对外部文件只读计算哈希并记录大小/修改时间，生成 `blobs` 与 `replicas(managed=0, locator=绝对路径)`；不复制、不移动、不改名。外部文件后来变化/消失时由健康检查标为 `changed`/`offline`/`absent`，保留上次观察；不覆盖旧观察，也不把变化后的字节冒充原对象。
- 条目：为每个成功项建 `namespace='local'` 的合成账号（按批次或按目标创作者分组）与 `native_kind='file'` 条目，`native_entry_key` 用稳定导入项 ID；文件名作标题，原始路径只存在于本机导入记录，不进入公开元数据或导出清单的绝对路径。没有指定创作者时进入“待整理”创作者；`import.assign` 把选中的导入项显式改到目标创作者，仍复用原对象。
- 同字节复用：相同 SHA-256 的 blob 只存一份；不同文件/文件名各自保留条目和导入记录。目录链接、junction、越界路径、循环目录、保留名、超长路径、不可读和空间不足都有明确失败项。
- 清理：失败/取消的 staging 按现有恢复规则清理；原地索引外部文件不在清理和回收候选中。

**关键失败与证据：** 源文件在托管复制后仍在且字节不变；原地索引文件在导入、删除条目和回收后都不被移动或删除；中断后恢复不出现“已完成”的假象；相同字节只存一份；外部文件变化标为 changed/offline；AC-14 通过。测试覆盖 `managed_import_leaves_source_untouched`、`in_place_index_never_moves_or_recycles`、`import_resume_does_not_claim_partial_success`、`duplicate_bytes_reuse_blob`、`external_change_marks_offline`、`import_rejects_link_traversal`。界面新增导入入口（资源库或存储页，按现有 IA 落位）与“待整理”列表。

### 11.5 M5 精确重复与相似资源比较（FR-ARC-006）

**完整结果：** 用户可以按对象摘要查看精确重复，也可以对两条资源做并排比较；精确重复依据 SHA-256，相似候选依据内容特征，默认只展示、不删除。用户可标记“不是重复”或“保留两者”，决定被记录；删除仍走现有显式删除流程。

**实现要点：**

- `resource.duplicates`：按 `media_variants.blob_id` 聚合被多个条目/媒体项引用的对象，返回引用它的条目、角色、表示、大小和摘要；分页。托管副本与原地索引副本都显示，外部路径只在本机界面显示。
- `resource.similar`：以一个条目/媒体项为目标，候选限定同内容类型与同角色，先按时长、宽高、音轨等规格过滤，再对图片用 `image_hasher` 计算感知哈希；视频按现有 FFmpeg 预览/抽帧能力取有界帧计算哈希。派生签名存 `resource_signatures(blob_id, kind, signature, algorithm, created_at)`，按需计算并有上限。无法取得稳定签名时返回 `incomparable` 及原因，不伪造相似。
- `resource.compare`：返回两侧的来源、文案/标题、规格、时长、摘要、缩略图和差异；`resource.compare.decide` 记录 `not_duplicate`/`keep_both`/`different_versions` 之一，不触发删除、回收、合并或替换。
- 候选只用于人工判断；不自动删除、不自动合并条目、不自动改质量选择。

**关键失败与证据：** 精确重复按摘要分组且不跨不同字节；近似图片有候选、无关图片不硬凑；无法计算时是 `incomparable`；决定持久化且不产生删除；不自动修改对象。测试覆盖 `exact_duplicates_group_by_sha256`、`similar_candidates_require_signature`、`incomparable_is_not_similar`、`compare_decision_never_deletes`。界面在资源管理器/资源详情增加重复与相似比较面板。

### 11.6 M6 质量状态筛选与处理（FR-QUALITY-005）

**完整结果：** 质量页和升级页可以按“待升级、不可比较、已替换、手动保留”筛选，逐项查看原因并处理（保留、固定、重新评估）。用户固定保留的版本不会被自动回收；修改质量策略后，受影响的条目重新评估，而不是只重算当前打开的一条。

**实现要点：**

- 在现有 `approval_state`、`asset_selections`、`variant_retention`、`object_recycling` 之上计算每组的四类状态：待升级（有 pending 候选）、不可比较（比较结果 unknown/incomparable）、已替换（当前选择与旧版本/保留版本不同）、手动保留（`pinned` 或 `variant_retention.pinned`）。只读派生，不新增第二份状态正本。
- `quality.list` 的 `entry_id` 变为可选；支持 `state`、`after` 和上限，返回条目、媒体项、变体、原因和可执行动作；旧调用（带 `entry_id`）行为不变。
- `quality.configure` 的 `entry_id` 变为可选；不带条目时保存策略并把重新评估排入现有维护任务，按批处理，返回任务 ID 和预计范围；不阻塞请求，也不谎称已完成。策略变更保留历史选择，固定保留优先于自动回收。
- `quality.pin` 继续作为手动保留/解除；固定版本在回收时受 `protected` 约束，已有行为不弱化。界面在 `QualityPanel`/`UpgradesView` 增加筛选、原因和批量“固定/解除/重新评估”，批量前显示影响对象。

**关键失败与证据：** 四类筛选结果与原因一致；`pinned` 对象在等待期后仍不被回收；策略变更后受影响条目重新评估，固定版本不被改选；未知/不可比较不被标成“更低质量”；无条目的旧调用兼容。测试覆盖 `quality_filters_classify_groups`、`pinned_variant_survives_recycle_after_policy_change`、`policy_change_reevaluates_affected_entries`、`incomparable_is_not_lower_quality`。界面中文空态与窄窗通过前端用例。

### 11.7 M7 跨平台同作品聚合与批量整理（FR-BROWSE-006）

**完整结果：** 用户可以把多个平台的条目显式归入同一“作品”组，查看一条聚合视图；每条保留自己的文案、来源、统计与版本，不互相覆盖。组内可以并排比较版本，批量执行有限的整理动作；执行前必须说明作用对象和影响范围。

**实现要点：**

- `work_groups` 与 `work_group_members`；条目可属于多个组，组删除只删关系，不删条目。`work.group.create/update/delete`、`work.group.add/remove`、`work.list/get`。
- `work.compare` 只读返回组内每条的最新修订、来源账号、发布时间/精度、正文摘要、统计和已选媒体规格；版本比较不合并正文，不把一站的文案写成另一站的。
- `work.batch.preview` 只接受安全动作：加/去标签、加入/移出组、同步未下载媒体、重新评估质量。返回逐条对象、预计影响（下载数量/字节、标签变化、评估条目）和跳过原因；`work.batch.apply` 必须带预览令牌且对象集合未变化，否则拒绝。任何删除、合并创作者、改来源关系都不在批量动作中。
- 操作部分失败时逐条报告，不回滚已成功的独立动作；同一令牌不能重放。

**关键失败与证据：** 聚合保留各自文案与来源；批量预览与执行对象一致；令牌过期/对象变化/空集合拒绝；失败可逐条重试；不提供删除或自动合并。测试覆盖 `work_group_keeps_each_caption_and_source`、`work_batch_requires_matching_preview`、`work_batch_partial_failure_is_per_entry`、`work_compare_is_read_only`。界面新增作品聚合页，并在条目详情提供加入组。

### 11.8 M8 插件独立安装、升级与回退（FR-PLUGIN-005）

**完整结果：** 用户可以从本机目录安装一个带清单的插件，安装时核对文件摘要、协议与能力兼容性；不兼容或摘要不符的版本不能启用。升级采用并排版本目录，确认新版本可用后才切换；保留上一版可回退。移除插件只移除插件文件，不删除任何档案内容。

**实现要点：**

- 清单 `plugin.json` 至少声明 `id`、`name`、`version`、`protocol`、`platform`、`entrypoint`、`files`（相对路径与 SHA-256）、`capabilities`、`limits`、`permissions`。安装只接受本机目录，拒绝绝对路径、`..`、符号链接/junction 越界、未声明文件、摘要不符和超过大小上限的包。
- `plugin_releases(plugin_id, version, manifest_json, path, digest, protocol, capabilities_json, state, installed_at)`；安装复制到 Profile 下按版本分开的插件目录，绝不覆盖随包适配器或旧版本。`plugins_state` 只指向当前活动版本。
- `plugin.install` 只安装、不执行；兼容性检查核对协议版本、平台/能力声明和清单结构。`plugin.activate`/`plugin.upgrade` 在停止新派发并等待相关任务到安全边界后切换；收尾超时则中止并保留旧版本。`plugin.rollback` 切回上一兼容版本；`plugin.remove` 拒绝移除活动版本，移除后档案仍可读。`plugin.releases` 列出全部版本与状态。
- 现有 `plugin.set_state` 在启用前复查兼容性；不兼容返回明确错误且不执行。界面在 `ModulesView` 增加安装路径、版本列表、兼容性、升级/回退/移除和风险提示；本阶段不做网络插件商店，第三方执行权限的开放分发仍不在范围。

**关键失败与证据：** 摘要/协议不符拒绝；路径越界拒绝；升级并排且失败保留旧版；回退只到兼容版本；移除后条目、正文和对象仍可读；未安装/未启用不执行。测试覆盖 `plugin_install_verifies_manifest_digests`、`incompatible_plugin_cannot_enable`、`plugin_upgrade_is_side_by_side`、`plugin_rollback_keeps_previous_release`、`plugin_remove_leaves_archive_readable`。安装流程不联网。

### 11.9 M9 插件能力契约（FR-PLUGIN-007）

**完整结果：** 登录辅助、通知、存储和内容处理扩展各自有独立、版本化的能力契约，不要求实现一个巨大的统一接口。插件在清单里声明能力；关闭任一可选扩展都不影响基础归档、档案读取和已有任务。

**实现要点：**

- 在 `crates/contracts` 增加能力模块：每类能力有 `kind`、版本、方法集、参数/结果、超时、字节上限与错误映射。站点适配器继续使用现有 `resolve_account`/`enumerate`/`observe`/`resolve_media`/`probe_auth` 等契约，不塞进四类扩展。
- `login_assist`：`begin`/`status`/`cancel`，只返回用户可执行的提示与状态，不接收或返回 Cookie/令牌；凭证仍由 Core 的加密存储和站点登录页处理。
- `notification`：有界事件投递，失败只记录并退避，不影响采集/归档；不把正文或凭证写进通知。
- `storage`：只通过 Core 提供的 staging 句柄读写派生内容，不接收最终档案路径，不能绕过对象库或改变托管副本。
- `content_processing`：声明输入/输出类型与上限，产物标为派生内容，原对象、正文和质量选择不被修改；失败可丢弃而不影响原档案。
- 宿主按清单声明的能力和方法路由；未声明的方法直接拒绝。每类能力独立启停，关闭可选扩展时基础归档照常。测试插件至少覆盖四类各一条成功与一条越权/失败路径；不要求本阶段交付 OCR 产品功能。

**关键失败与证据：** 未声明方法被拒绝；通知失败不影响任务；登录辅助拿不到凭证；存储不能写最终路径；内容处理不能改原对象；关闭全部可选扩展后基础采集、时间线和导出仍通过（AC-24）。测试覆盖 `capability_routes_only_declared_methods`、`notification_failure_is_isolated`、`login_assist_never_receives_credentials`、`content_processing_cannot_replace_original`、`optional_extensions_off_keeps_base_archive`。界面在 `ModulesView` 显示能力徽标与独立开关。

### 11.10 完成顺序、证据与验收

建议顺序：共同 schema/契约 → M1、M2（身份关系）→ M4（导入）→ M5（比较）→ M3（回放）→ M6（质量）→ M7（聚合）→ M8（生命周期）→ M9（能力）→ 界面收口。每一步先补对应 C13～C17 的反例，再做下一步；不要把所有界面或迁移留到最后。

每个 M 包在[阶段报告](stage-report.md)中写：实际命令与退出码、实现位置、覆盖的需求 ID 与验收条件、测试名、界面路径、已知限制、未执行的真实环境检查。`requirements-map.json` 在 B 交付时把九项改为 `current`/`applicable: true`，填工作包与用例组；只有 A 审查通过后才改为 `implemented`。管理功能不要求真实站点，使用隔离 Profile 与合成文件；不得用真实个人文件、Cookie 或档案做导入测试。

DEC-039 的 yt-dlp 手动更新用固定发布样本、随包公钥与摘要验证和本地版本目录测试；联网检查只由用户点击触发，B 不主动联网更新，也不使用工具自己的 `-U`。DEC-041 的小红书签名用固定向量与离线夹具测试，真实完整历史在 §12 的最终包检查中做；站点拒绝签名即停止并把历史标为未取完。

<a id="agent-b-round-4"></a>
## 12. 第四轮：管理能力、工具更新与最终包（2026-10-18）

第三轮尚未执行的 R3-2～R3-4、R3-6～R3-8 与本节合并为同一轮；R3-5 的微博判据已由 A 解决，Instagram 仍等 S-002。第三轮所说的“最终包”在 §11、DEC-039、DEC-041 落地后失效，所有包证据按 R4-8/R4-9 重新换绑。

| ID / 依赖 | 完整结果 | 关键失败与完成证据 |
| --- | --- | --- |
| R4-1 身份关系（M1、M2） | 完成 §11.1、§11.2 的 schema、命令、界面和 C13；旧创作者 ID、条目、对象与文件不变 | 合并/拆分/重新关联/候选确认的反例通过；无自动绑定；操作记录可查 |
| R4-2 导入与比较（M4、M5） | 完成 §11.4、§11.5 的导入状态机、原地索引、去重和比较界面；C14 | 托管复制不动源文件；原地索引不被移动/回收；中断恢复；相似不自动删除；AC-14 |
| R4-3 回放、质量与聚合（M3、M6、M7） | 完成 §11.3、§11.6、§11.7；C15、C17 | 剪辑回放保留录制独有区间；四类筛选与固定保留；批量作用范围与部分失败 |
| R4-4 插件生命周期与能力（M8、M9） | 完成 §11.8、§11.9；C16；本机安装、并排升级、回退、四类能力契约 | 摘要/协议/路径失败拒绝；回退可用；移除后档案可读；可选扩展关闭不影响基础归档 |
| R4-5 yt-dlp 手动更新（DEC-039） | 在设置页显示来源与版本；用户点击才检查；按官方发布下载 `yt-dlp.exe`、`SHA2-256SUMS`、`.sig`，用随包公钥验签、再验摘要；通过后并排安装、`--version` 启动检查、空闲切换、保留上一版与随包版本的回退；不使用 `-U` | 坏签名、坏摘要、版本不符、Deno 不兼容、任务进行中均拒绝或推迟；不自动检查；更新记录不写档案；固定样本覆盖验签失败 |
| R4-6 小红书完整历史（DEC-041） | 站点注册表只向适配器提供 `a1`；适配器按固定向量计算 `x-s`/`x-s-common`，续页取到列表结束或订阅边界；主页预览同样完整；`web_session` 等仍不离开 Core；406/461 停止 | 固定向量、无 Cookie 夹具、域名边界通过；真实目标首次检查能翻到历史末尾或如实记录“未取完”；不换参数重试 |
| R4-7 无日期列表的增量检查（R3-3） | 完成 `real_site_check.py` 的无日期列表增量：用详情日期选对象、第一次检查由详情定位、范围外不下载/不计数、第二次不重复取详情；每次请求数进回执；`test_real_site_check.py` 覆盖 | 小红书无日期列表可检查；检查脚本本身不消耗订阅额度时用合成站点；真实结果或停止原因写报告 |
| R4-8 最终包、基准与门禁 | 所有代码与文档完成后重打包；一小时 100k 基准按原规模与阈值；`stage.py check/report`；包携带新工具/许可/摘要；ACT-003 改指最终包（摘要、文件数、合成档案） | 包身份与 `SHA256SUMS.txt` 摘要写进报告和证据；改代码后旧包证据作废；ACT-003 可照做 |
| R4-9 真实站点（最终包） | 在最终包上按 ACT-007 的翻倍额度做八站检查；每站先补账号识别与第一页列表的冒烟，再补未完成项：微博新 Cookie 的全部检查、Instagram 仅 S-002 回复后、X 原生视频/两次增量、小红书完整历史与两次增量、TikTok 两次增量与图集、YouTube 自动下载开的增量/Shorts/社区帖子、抖音与哔哩哔哩两次增量及哔哩哔哩视频通道/浏览器参数/勾选下载；R3-2 的全局代理设置沿用 | 订阅规则只有账号识别、列表 2 页、自动下载关/开两次增量都通过才 `implemented`；挑战/限流/登录失效即停；每站回执与包摘要一致；额度不是目标 |
| R4-10 规则、报告与交接 | 按真实结果更新八站规则、日期、限制与 `rule-evidence.json`（按作品种类分别记录）；报告写 M 组、命令与结果、真实证据、整理与未执行边界；给出完整 B→A prompt | `rule_evidence.py check`、规则状态与证据一致性通过；B 自检不写成 A 已通过 |

<a id="agent-b-round-4-prompt"></a>
### 12.1 A → B 第四轮 prompt

```text
请作为 Agent B，在 TRACE 仓库分支 codex/m2-multi-site-archive 继续 TRACE-M2-01 r2：完成 docs/delivery/stage-plan.md §11 的 M1～M9、§12 的 R4-1～R4-10，并接上尚未执行的 R3-2～R3-4、R3-6～R3-8。A 的修改已 amend 进本阶段唯一提交（SHA 以 git log -1 为准），工作区应当是干净的；不 push、不建 PR。
先读：AGENTS.md、REVIEW.md、docs/README.md、docs/delivery/status.md、docs/decisions/confirmed-decisions.md 的 DEC-031～041、USER-ACTIONS.md 的 ACT-007 与 S-001/S-002/S-003、docs/delivery/stage-report.md §6～§7（F-064～F-068）、docs/delivery/stage-plan.md §9～§12、docs/design/multi-site-contracts.md §1、§10、§11、docs/research/social-sites-current.md §13。参考索引只读；公开文件不写参考项目名与本机路径。
顺序：先补共同 schema v8/契约/TS/迁移反例 → M1/M2 身份关系 → M4 导入 → M5 比较 → M3 回放 → M6 质量 → M7 聚合 → M8 插件生命周期 → M9 能力契约 → 界面收口；再做 R4-5 yt-dlp 手动更新与 R4-6 小红书 a1 签名（离线向量与夹具先过）→ R4-7 无日期增量脚本 → 全部离线门禁通过后重打包（R4-8）→ R4-9 在最终包上按 ACT-007 翻倍额度做真实检查 → R4-10 规则、证据、报告。
必须保留：两站现有订阅、链接下载、直播录制、认证历史、质量升级、导出备份恢复的行为与全部断言；适配器不接触 Cookie（唯一例外是小红书的 a1，且只由 Core 注入）；Cookie 只发往该站 Cookie 域名；限流、挑战、需要登录不等于没有更新；订阅规则只有账号识别、列表 2 页、自动下载关/开两次增量都通过才是 implemented；登录判据必须有真实不带 Cookie 的对照。
管理功能：合并/拆分/重关联/候选确认都必须显式、可审计；不自动合并或删除；导入的原地索引外部文件永远不移动、不回收；相似候选与重复只展示，用户决定；固定保留的版本不被自动回收；移除插件不删档案；关闭可选扩展不影响基础归档。DEC-039 的更新只由用户点击触发，验签和摘要都过才并排安装，不用 -U。DEC-041 只把 a1 交给适配器，签名被拒绝就停止并把历史标为未取完。
真实站点只在 ACT-007 的对象与额度内、在最终包上执行；Instagram 只在用户回复 S-002 后做，微博用新 Cookie 做，小红书按 DEC-041 取完整历史；遇验证码/安全验证/限流/登录失效立即停止该站，不重试、不绕过，其他站点继续。做不到的站点规则如实标 partial 并写明尝试过的方案；规则不对又没有思路的按 DEC-037 记进 USER-ACTIONS 的 S 一节。
完成后把实现位置、实际命令与退出码、M 组需求 ID/测试名/界面路径、真实证据、包摘要、未执行边界写进 docs/delivery/stage-report.md，amend 进同一个带 DCO 签名的阶段提交；给出完整 B→A 审查 prompt（含最终 SHA、报告入口、未完成项与限制）。需要用户的事只写进 USER-ACTIONS。
```

<a id="desktop-and-fast-validation"></a>
## 13. A 复核后的实施补充（2026-10-20）

本节仍属于 TRACE-M2-01 r2 的同一分支/提交，不开新阶段。原 A 复核结果及实际交接见[报告 §15](stage-report.md#a-m2-r6-review)；F-069 的局部修复必须保留，F-071 当时列为必须返工项。用户新增 [DEC-042](../decisions/confirmed-decisions.md#dec-042)，不追溯为旧交付的缺陷；当前实现与复核结果见下段。

该批实现、返工与最后冻结已由 A 独立复核通过，见[报告 §19](stage-report.md#a-m2-r7f-review)；§17 保留原失败，§18 记录 B 返工。F-071 已覆盖发现关系不变的旧条目新增活动，并严格关联成功 observe 同代执行与详情请求。F-087 的 local/main 最小 Tauri capability、F-088 的窄窗错误可见性和 WebView 守护保留并在 `-19` 再验；当前不再有交 B 的必须项。产品验收、真实站点和发布边界不因技术通过扩大，远端动作仍待 ACT-008 授权。

| 工作包 | 结果与实施边界 | 必需验证 |
| --- | --- | --- |
| U1 沉浸式标题栏 | 沿用蓝白外壳和现有 Tauri 能力（[官方研究](../research/open-source-candidates.md#desktop-display-2026-10-20)）。原生标题栏融入 App 顶部；只在桌面显示真实的最小化、最大化/还原和关闭按钮，空白区域可拖动、双击；搜索/导航/按钮不是拖动区。权限只给本地 main 窗口需要的动作；保留 resize、键盘焦点和 GUI/后台分离，不改后台退出授权 | 实际调用、状态图标、浏览器隐藏、按钮键盘/错误路径、隔离 WebView 关闭与重连/后台继续；不能只改 `decorations=false` 就交付 |
| U2 四档文字与响应式 | 设置新增“显示与文字”，100/110/125/150%，默认 100%，即时应用并持久保留。推荐使用应用本地显示偏好，明确不属于档案备份/来源数据；值校验、损坏/存储不可用时可恢复默认并如实提示。Vue 显示偏好驱动统一 CSS 字号/行高与 Naive UI 主题；固定 px 字号逐项接入，浮层也缩放。按内容/可用宽度重排，不能只用 transform 或仅改 html 字号 | 四档真实计算字号与持久化、非法值/读写失败、多窗口偏好同步；[设计矩阵](../design-rules/DESIGN.md#immersive-and-text-scale)中的页面、视口、弹窗/长中文/底部操作。自动布局证据与 ACT-003 原生体验分开 |
| V1 分层快验 | 增加明确标记的快速验证入口（可基于现有 verify suites 组合），文档写明推荐用法、被测输入、实际耗时和覆盖。默认不触发资源长测/真实站点/更新；UI 改动不顺带跑 Rust 全套，后台修复跑受影响测试，最终冻结再跑完整测试/包 | 正反例证明快验失败会非零，短测/旧输入不能通过正式资源证据门禁；不删除或弱化断言，不把快验回执发布成完整阶段 report |
| E1 F-071 | 用订阅范围内/外的发现关系与成功执行证据核查增量，包含已有条目及其他 lane 的活动。严格拒绝缺失 observation 字段；media/observe 执行必须能归属到目标 subscription/consumer/request，不只凭 subject ID、任意 job 或全局字节。自动下载开/关都执行第二轮，并核对所有相关条目的关系、详情/媒体执行和新任务 | 把 A 的 driver-probes 迁入能力命名的回归；覆盖缺失观察、已有范围外条目错误发现、其它 lane/失败 job 假冒、auto-on 第二轮重复详情/媒体，以及既有全部正例。不能重定义 jobs.attempts，不新增真实请求 |

顺序：E1 与 U1/U2/V1 连续完成、定向验证和修复；所有代码/测试冻结后统一构建最终包。日常不跑一小时基准，每次返工不滚动重跑；A 审查原始基线只复算/验摘要。当前 A 修改了后台产品输入，旧 -15 长测不能作为新源码结果：在本阶段**最后一次冻结**后才按正式门禁集中补一轮需要的新资源证据，不在每个工作包或 A 的日常复核后重复。若要跨产品输入复用资源测量，必须先设计并自动验证依赖域/二进制身份，清楚保留旧测量归属；本轮没有授权把短测直接替代正式长测。

源码/包/旧失败保持原身份；新包不重做真实站点或真实更新请求，八站订阅 partial 与 ACT-003 not-run 保持。最终 B→A 交接记录每项实际输入、命令、耗时、限制和唯一提交 SHA。A 技术收敛前不请求远端授权。
