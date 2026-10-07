# 哔哩哔哩视频能力表与 M1a 接入边界

日期：2026-09-26；Agent A 资料/源码审阅，Agent B 于同日回写第 5 节实现状态。**推荐作为 M1a 首站；TRACE 真实请求、登录、下载仍 not-run。** 推荐是实现顺序，不是已支持声明。需求依据 FR-ID、FR-SUB、FR-AUTH、FR-ARC、FR-BROWSE；执行见[当前计划](../delivery/stage-plan.md)，未知及实测条件统一在 OSS-007/ACT-002。

## 1. 证据与方案

当前 [bili-sync](https://github.com/amtoaer/bili-sync) 未归档，最近推送 2026-09-10，MIT；README 明确提供 UP 主投稿扫描/下载。A 阅读固定提交 `c900777e78796653fdfb00421879b6a802d1eedd` 的 submission/video/client/credential/analyzer/error 源码，路径、相关行及哈希见[源码证据](2026-09-26/bilibili-source-inspection.json)，仓库信息见[站点快照](2026-09-26/site-and-media-sources.json)。

以下接口属于社区观察到的网页接口，**不是 Bilibili 官方提供给 TRACE 的稳定 API 合同**。A 不从参考项目整包继承调度、数据库、文件库或质量判定；实现独立 Rust Bilibili adapter，复用通用 HTTP/媒体层。可以参考或最小复用 MIT 代码，实际复制时保存 commit、原许可和版权通知并审查语义差异。当前不加 Python/Node 下载服务。

`SocialSisterYi/bilibili-API-collect`、`Nemo2011/bilibili-api` 和 `nilaoda/BBDown` 的当前仓库已归档；旧文章/README 只能解释历史。部分旧源码链接已 404，见[维护快照](2026-09-26/maintenance.json)。不得将它们列为已核对的活跃生产依赖。

## 2. 独立能力表

证据级别：S=固定社区源码可见；R=上游公开问题/文字说明；T=TRACE 受控实测。此次没有 T。

| 能力 | 资料中可观察到的事实 | M1a 实现要求/未知边界 | 证据 |
| --- | --- | --- | --- |
| 账号身份 | submission 用 `mid` 查询 `x/web-interface/card`，adapter 核对返回 mid | PlatformAccount 用平台+mid；昵称/主页是可更新资料。输入支持 mid 和规范主页链接，显示身份解析失败；不依赖昵称作为键 | S：submission.rs、adapter/submission.rs |
| 内容及多 P 身份 | video 用 bvid 获取详情，PageInfo 含 cid/page/part/duration | Entry 固定 canonical bvid，保存 aid 别名（实际取得才关联）；每 P 用 cid 建资源组，page 为展示顺序；同一发布多 P 完成前不得整体显示完整 | S：video.rs；aid 双向映射需 B 固定样本与实测 |
| 投稿枚举 | `x/space/wbi/arc/search`，mid/order=pubdate、pn/ps，返回 vlist 和 count | 有界分页、去重、分页与游标同事务；count 是当时观察数，不证明全历史。页数变化、空页、字段缺失与权限挑战单独处理 | S：submission.rs |
| 历史/增量 | 参考实现同时提供投稿和 dynamic 路线，按发布时间过滤；dynamic 特别处理顶部置顶 | M1a 采用投稿路线；不要照搬“遇旧即停”规则。增量与初始范围、近期复查各自游标；现有观察外的旧编辑覆盖 unknown | S：adapter/submission.rs；全历史可信度 T not-run |
| 正文与观察版本 | `x/web-interface/wbi/view` 返回详细 VideoInfo；列表摘要不足以代表完整详情 | 保存标题/简介/来源/作者时间证据/封面/完整 pages；正文或资源语义变化创建 Revision，播放量变化不制造内容新版 | S：video.rs；删除/编辑稳定信号待 T |
| 媒体解析 | `x/player/wbi/playurl`，bvid+cid、qn/fnval 等，返回 dash 或 durl；源码有备用地址与不同 codec | 候选归一为每 P 视频/音频/混流计划；DASH 先下载校验，再 FFmpeg 流拷贝封装；durl 数组需全段，不能只取 `[0]` | S：video.rs、analyzer.rs；参考实现部分混流只取首段，TRACE 必须修正边界 |
| 无声与音轨 | analyzer 的 VideoAudio 允许 audio=None；存在普通音轨、Dolby/HiRES 分支 | 区分“合法无音轨”和“音频获取失败”。根据详情/候选完整性及 probe 验证，未知保持未知；不把空字段统一解释为无声 | S：analyzer.rs；用合成及授权无声样本证实 |
| 质量/可用性 | 接口请求的 qn 与返回候选、账户权限、实际流可能不同；codec AVC/HEVC/AV1 | 存 requested/available/selected/actual，使用返回事实；不给 4K/原画保证。先完整保存可访问的合适候选，能力下降告警；自动升级在 M1c | S：analyzer.rs；最高可用质量需 T |
| Cookie 与登录 | credential.rs 有二维码生成/轮询、Cookie 提取、nav WBI key、刷新流程 | 首轮提供账号安全输入/撤销/重新验证，至少一个可用登录入口；M1a 可先安全导入用户主动提供的 Cookie，扫码辅助为同批可选增强，自动刷新不作硬门槛。凭证不进入档案、命令行和日志 | S：credential.rs；不得复制其中测试打印秘密的做法 |
| WBI 与会话 | `x/web-interface/nav` 获取 wbi_img 后生成签名；客户端附 Referer/必要 Cookie | 独立版本化站点算法，有固定向量；WBI key 短期内存缓存、失效可重新获取一次并分类，不能无限重试 | S：credential.rs/client.rs；当前网站可用性需 T |
| 过期地址/续传 | 媒体候选含 URL 和 backup_url，地址不是内容身份 | 访问计划只在敏感内存；持久化稳定内容/资源 ID；重解析后验证 ETag/长度/版本，不能拼接不同对象；校验不足时安全重下 | S：analyzer.rs；期限和 Range 支持按响应实测 |
| 风控与失败 | error.rs 区分 risk control、空流、HTTP 状态和业务码；[issue 613](https://github.com/amtoaer/bili-sync/issues/613)记录 `code=0` 但含 `data.v_voucher` | 成功码还需结构验证；voucher/挑战进入 waiting_user，空列表不能据此宣称无作品/全量结束。401/403/412/429、超时及业务码分别分类，数值含义不得盲猜 | S+R；真实业务码只在资料/响应已证明时赋具体语义 |
| 删除/权限变化 | 列表缺失、接口失败、详情不可访问存在不同原因 | 单次消失/404 不直接删历史或标 confirmed_deleted；保留 unavailable/unknown 及证据；可恢复时重新校验 | 未知删除权威信号，不作能力承诺 |
| 非视频扩展 | 上游还有收藏/合集、字幕/弹幕等路径 | 首轮专注 UP 投稿普通视频；番剧/付费/DRM、直播、动态纯图文、收藏整库、评论弹幕不进入 M1a。封面及必要分 P 不可省 | S 路径存在只证明代码，非 TRACE 支持 |

## 3. 一次实施范围与请求约束

M1a 首站闭环：解析 UP 主 → 创建订阅 → 按选择范围枚举投稿 → 详情规范化 → 每 P 必要媒体解析/保存 → 来源/正文/视频浏览 → 中文搜索 → 后台持续 → 离线导出与恢复。默认新增范围与检查周期沿用[需求默认](../requirements.md#defaults)；用户主动选择一个有界近期历史范围便于首轮验证，不能静默全历史下载。

站点初始请求预算由[首轮运行契约](../design/m1a-runtime-contracts.md)统一定义；每次受控实测给出账号/视频范围、页数/字节上限、停止条件、请求统计。遇挑战停止该账号自动重试并给重新授权/稍后再试入口；不自动更换账号、代理或绕过访问限制。模拟用例不需要真实 Cookie。

首轮质量策略仍遵循[质量正本](../design.md#quality)：M1a 不实施自动替换/回收；同一内容各必要资源先完整，保存已观察规格、选择原因和能力差距。AVC/AAC 优先用于兼容预览不等于默默放弃更完整的来源候选；不能直接播放的原件保留并提供外部打开。

## 4. B 必须固化的夹具与现场验证

固定夹具覆盖：正常单 P、双 P DASH、无声、durl 多段、重复/乱序页、count 变化、旧帖编辑、短摘要、`v_voucher` 成功码、空流、登录过期、限流、地址过期、Range 不一致、未知 codec 和部分音频失败。只保留合成账号及去凭证的结构样本，断言有意义的错误与归档结果，不能只断言 JSON 能解析。

授权实测至少覆盖两个创作者、单 P/DASH、一个多 P 或明确记录该范围未验证、关闭 GUI 继续、凭证失效/重新验证、已保存内容离线恢复。无法获得其中样本时 ACT-002 保持未完成，相应能力标 not-run；B 继续全部自动工作并提供具体安全配置步骤。真实测试不能用上游工具下载结果或合成 HTTP 成功替代。

B 交付回写本表的 T 证据、支持条件/日期/适配器版本、实际能力与未覆盖项；不能把整个表批量改成“支持”。

<a id="b-实现回写"></a>
## 5. B 实现回写（2026-09-26，M0/M1a 首轮）

实现位置：`adapters/bilibili/`（独立进程，双向插件协议 0.1），凭据由宿主经 `with_credential` 注入，适配器自身不落盘 Cookie。**本节仍没有任何 T 证据**：下表只记录合成夹具与固定向量的自动验证结果，真实站点请求、登录、下载、风控与播放地址有效期全部 not-run，真实条件与请求预算由 ACT-002 控制。

| 能力 | 实现状态（自动证据） | 仍未验证（T） |
| --- | --- | --- |
| 账号身份 | 已实现 mid 与规范主页链接解析，含非法输入拒绝；夹具 `extract_mid_forms` | 真实 `card` 响应、昵称与头像字段的当前结构 |
| 投稿枚举 | 有界分页 + 游标同页返回；夹具 `enumerate_page_maps_entries_and_cursor` | 页数变化、空页、权限挑战下的真实游标行为 |
| 正文与多 P | bvid 一致性校验、空页拒绝；每 P 按 cid 建资源组；夹具 `observe_single_p_maps_resources`、`observe_multi_p_maps_group_per_cid`、`observe_rejects_bvid_mismatch_and_empty_pages` | 真实 `view` 字段完整性、aid 双向映射样本 |
| 媒体解析 | DASH 候选按 AVC 优先/带宽排序，音频候选独立归一；durl 要求全段（显式拒绝只取首段）；夹具 `playurl_dash_prefers_avc_and_maps_audio`、`playurl_durl_requires_all_segments` | 真实 `playurl` 候选分布、备用地址轮换、Range 支持 |
| 无声与音轨 | DASH 无音频候选 → `audio(required=false)`，不伪造音轨；夹具 `playurl_silent_dash_yields_no_audio_candidate` | 授权无声样本确认“合法无音轨” |
| 空流/不可用 | 空流映射为来源不可用错误而非成功；夹具 `playurl_empty_flow_is_source_unavailable` | 真实空流与地区限制提示差异 |
| WBI 与会话 | 独立实现 wbi-v1（64 位置换表、值剔除 `!'()*`、RFC3986 编码、MD5），4 个固定向量 + TTL 缓存门控；夹具 `mixin_key_fixed_vector`、`sign_fixed_vector_basic`、`sign_filters_forbidden_value_chars`、`sign_percent_encodes_unicode_and_space`、`cache_ttl_gates_freshness` | 真实 nav 取 key、key 失效重取一次的行为 |
| 风控与失败 | 只有 `code==0` 且结构有效才算成功；`data.v_voucher` 即使 code=0 也判为挑战；-101/-412/-352 分别映射鉴权/限流/权限，未知码保持 unknown 不猜测；夹具 `code_zero_without_voucher_is_ok`、`voucher_inside_success_is_challenge`、`known_codes_map_to_proven_classes`、`unknown_code_stays_unknown` | 真实错误码分布与 412/429 触发条件 |
| 删除/权限变化 | 未实现自动删除判定：单次缺失不改变历史状态（沿用能力表要求） | 删除权威信号本身仍未知 |
| Cookie/登录 | 宿主提供安全导入与撤销入口（DPAPI vault）；适配器无屏幕码流程 | 扫码辅助、自动刷新（本轮明确不作硬门槛） |

未实现且不属 M1a：番剧/付费/DRM、直播、动态纯图文、收藏整库、评论弹幕、字幕。未复制 bili-sync 等参考实现代码，本轮不产生新的第三方许可义务（见[许可台账](../delivery/dependency-licenses.md)）。

B 需要的现场条件（未满足前保持 not-run）：两个创作者的授权范围、单 P 与一个多 P 样本、登录/失效流程、关闭 GUI 后继续、离线恢复；命令与记录格式见[交付证据](../evidence/2026-09-26-m0-m1a-delivery.md#人工步骤)。

## 6. TRACE-CLOSEOUT-02 回写（2026-10-07，B 自检）

第 5 节保留首轮历史表述；当前音频、分段与状态行为以下表及运行契约为准。F-008/F-009/F-012 的复核状态只在[原审查](../evidence/2026-10-01-m0-m1a-review.md)维护。当前仍无真实 T 证据。

| 范围 | 当前实现与自动证据 | 真实限制 |
| --- | --- | --- |
| DASH/必要音轨 | observe 音频 required=true；resolver 明确空音轨/混流且没有独立候选才能免除，核心记录 acquisition_evidence；缺字段不等于无声 | 真实无声/混流样本 not-run；不能把候选为空理解为成功 |
| durl | 每段独立有序 video_segment，所有必要段齐才 complete；合成闭环断言两个独立对象，FLV 固定样本各自 probe | 不将字节拼接冒充可播合并文件；多段可分别选择播放 |
| 规格与播放 | 锁定 FFmpeg probe，分轨具备实测后流拷贝到 playback-mux-v1，保留原轨；AVC/AAC 固定测试通过 | 所有真实 codec/播放器环境未验收，外部打开为有约束的备用路径 |
| 封面/来源 | 解析 view 封面 URL，独立 optional cover 任务保存；卡片及详情用 blob URI；来源打开由核心构造 | 封面失败显示占位，不提升必要媒体完整性 |
| 获取/恢复 | 流式背压、任务取消/暂停/退出、503 有界重试；403/404/410 有界重解析；无法证明续传身份时重下 | Range/ETag 合成负例通过，真实地址有效期/风控 not-run |
| 保存与质量 | 使用领域必要资源规则；音频失败为 partial；无质量目标比较为 unknown | 不宣称达到最高质量，不自动升级/删除旧原件 |

本轮实际命令、故障和复验见[B 收尾报告](../delivery/stage-report.md)，真实站点步骤/授权只维护 [ACT-002](../../USER-ACTIONS.md#act-002)。


## 7. 分段校验返工（2026-10-07，B）

A 在第 6 节实现上补反例，发现独立段拆分丢弃已知大小（F-008），历史失败见[原审查 §10](../evidence/2026-10-01-m0-m1a-review.md#closeout-failures)。本次生产 resolver 将 durl 每段 size 传入 UrlSet.expected_size；只在所有段大小已知时求可靠总量，未知一段不会丢失其余已知段依据。核心保留每段/整组校验、独立对象和顺序，验证后才贡献 complete；旧协议仅含总大小时仍校验整组。固定夹具补单段缺 size 的未知总量反例，合成源覆盖 8+8 声明与 1+1 实收、不同段大小、仅总量、局部未知和错误摘要。证据见[本轮返工](../delivery/stage-report.md#b-closeout-rework)。

此次没有新增真实 T 证据、第三方依赖或许可义务；实际站点、播放地址和原生媒体体验边界保持既有用户待办，不承诺分段无缝拼接。
