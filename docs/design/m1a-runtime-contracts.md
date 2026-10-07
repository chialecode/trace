# M1a 运行与集成契约

状态：active。2026-09-26 A 选定首轮约束，2026-10-07 B 按 TRACE-CLOSEOUT-02 实施回写；技术复审和产品验收仍待进行。本文细化[技术设计](../design.md)的 Windows/M1a 落地接口，不复制 INV、Materialization/Job 状态机、备份协议或领域模型。阶段范围与测试正本在[当前计划](../delivery/stage-plan.md)，本轮实际证据见[交付报告](../delivery/stage-report.md)。B 接入时用实际类型、schema 和实现位置补充本文；改变下列边界须说明迁移和使用方。

## 1. 进程、档案和启动

桌面壳、daemon、适配器和 FFmpeg 是独立进程。daemon 负责唯一写入、持久任务和凭证 vault；UI 只通过控制协议访问应用服务。适配器按活动任务启动，可多账号复用、空闲退出；不是每个创作者一个常驻进程。运行 trusted adapters 不等于拥有 OS 沙箱。

首轮支持一个当前用户下的本地托管档案。Profile 包含 archive_id、配置和档案定位，凭证 vault 存在当前用户应用私有目录并按 archive_id 隔离；库、objects、manifests、staging 的布局沿用[存储](../design.md#storage)。不把凭证 vault 放入档案路径。支持显式隔离测试 Profile，拒绝不兼容/同位置冲突；UI 不接受插件提供的任意最终路径。

启动流程：GUI/CLI 先连接本用户、本 archive_id 端点；校验协议/档案 ID 后复用。无法连接时尝试独立启动 daemon，由 daemon 获取 OS 排他锁后再打开数据库；竞争失败者退出并连接胜者。握手成功而不是 PID 文件存在表示 ready。后台进程不绑定 GUI 的 kill-on-drop/进程退出回调或 Job Object；单纯配置 Tauri sidecar 不算验证完成。Windows 进程隐式启动无终端窗口。

关闭主窗口：退出交互壳、播放器、登录辅助 WebView；daemon 持续。再开读取快照与事件游标。完全退出：发送显式 shutdown，停止新调度，取消网络到可恢复边界、保存检查点、结束子进程后关闭数据库/锁；超时给清楚的未结束状态，不能 UI 消失就宣称全部退出。异常杀死用恢复流程处理，不承诺断电零损坏。

## 2. 控制 IPC 与类型正本

Windows 使用 named pipe；端点名以当前用户和 archive_id 派生的标识构造，避免暴露用户路径。服务端配置仅当前用户及必要系统主体访问的安全描述符，拒绝远程管道连接，核对调用用户与档案；不因 endpoint 名难猜就省 ACL。攻击者拥有同用户权限属于单独威胁边界，不能声称此设计隔离同用户任意恶意程序。

控制与事件消息使用 4 字节 little-endian 无符号长度 + UTF-8 JSON，1 MiB 上限，读长度后先校验再分配；上限沿用插件协议初值。握手分别报告应用版本、控制协议 0.1、档案 schema、插件协议与能力。未知必需方法/不兼容版本拒绝；未知可选字段按 schema 策略处理。领域命令采用[核心 API 信封](../design.md#api)，UUID/整数在跨 JS 边界时使用不会丢精度的字符串 ID，时间为显式 UTC 时间戳及来源精度。

Rust DTO/schema 是协议结构正本；TypeScript 类型/运行时校验须与其一致，可使用经查证的生成工具或固定 schema 生成，不能维护两套独立业务模型。跨语言黄金样本覆盖成功、冲突、未知字段、大整数、空值和错误。B 在实际契约模块给出 schema、生成/一致性命令及消费者位置。

最小命令覆盖 creator/account 的创建编辑绑定、subscription 的启停/有界回填/检查、archive 列表详情、job 查询重试取消、credential 导入验证撤销、plugin 启停状态、storage 容量/导出/备份/恢复校验、daemon ready/status/pause/shutdown。未实现的合并、直播、远程命令不暴露假成功入口。长操作返回 job_id/accepted；写命令带幂等键与配置版本，收据同事务保存；断开连接不隐式取消已接受的工作。

事件区分持久变更序号和可合并的瞬时进度；消费落后返回 resync_required。错误至少含稳定 code、可重试性、用户动作、关联 ID，详情只带安全字段。认证失败、挑战/人工处理、限流、离线/超时、存储不足、输入错误、版本冲突、插件不可用和未知远端响应不能全部落到“失败请重试”。

## 3. 适配器与传输

进程协议沿用[插件契约](../design.md#plugins)，方法按实际需要覆盖 hello/capabilities、resolve_account、enumerate、observe、resolve_media、probe_auth、cancel/shutdown。枚举结果含稳定 ID、页/游标、coverage 证据和摘要完整性；详情含正文/资源清单完整性及时间证据。适配器不决定最终归档成功或直接写 SQLite。

核心提供受限 HTTP/凭证使用代理，适配器只获得该账号必要的短期字段/授权能力。先验证 URL scheme、站点/媒体域名与端口、重定向每跳、解析地址范围及凭证发送域；拒绝 file、UNC、localhost/私网/元数据地址和无关域。测试服务使用仅测试构建的显式允许项，发行构建不能被远端响应开启。DNS/重定向校验不能只做字符串后缀判断，Cookie 不随跨域 CDN 重定向传播。

媒体 URL、Cookie、认证头和二维码登录句柄只存敏感内存计划，持久 Job 只存稳定来源/资源身份、代次与验证依据。取流前重解析；续传逐次核验身份/长度/校验器，无法证明时从头重新获取。下载时拒绝 HTML 挑战冒充媒体、超预算响应和错误 Content-Range。组内必要分 P、分段/音轨独立跟踪，部分完成仍保留已有有效文件，但不能把整条 Entry 标成完整。

所有诊断使用白名单字段。不得直接采用上游 Debug/Cookie 打印或完整 HTTP 错误体。可保存脱敏结构摘要、业务码、响应结构版本、错误类别和样本摘要；签名查询、Authorization、Cookie、刷新令牌及本机私有路径不进入日志/SQL WAL/清单/备份。

## 4. Windows 凭证入口

M1a 至少实现主动 Cookie 安全导入、状态验证、失效提示、撤销和重新导入。输入控件默认遮蔽，关闭清空 UI 状态，不写 localStorage/sessionStorage，不放命令行参数或普通配置，不自动读取其他浏览器 Profile。GUI 将输入经受控 IPC 送 daemon，vault 用 DPAPI current-user 加密及私有 ACL；仅保存非敏感引用和能力状态到 SQLite。失败时拒绝保存，不能降为明文。

凭证不随档案/元数据备份导出，恢复后明确显示需要重新登录；撤销使后续计划失效并清理短期缓存。密码内存清除采取可行措施，但 Rust/JS/操作系统可能有拷贝，不能保证清除全部历史内存。扫码辅助可在同批补入，但网页脚本/登录 WebView 不驻留后台；首次关闭 GUI 也释放它。

## 5. 搜索与播放

搜索字段先覆盖创作者名、来源账号显示名、标题和正文；过滤创作者/平台/时间后按发布时间（未知时间单独排序）、Entry ID 稳定分页。原文不变，搜索规范化 v1 使用 NFC + Unicode lowercase，保留标点和空白语义；不做分词同义扩展。实现中采用成熟 Unicode 库并锁版本；每次 normalize 同时适用于索引与查询。

FTS5 长查询、单/双字候选和字面复核按[搜索设计](../design.md#search)执行。特殊字符不得作为用户输入的 FTS/SQL 语法；fallback 有批次/取消能力，结果截断或索引追赶中给状态及继续入口，不能静默当无结果。搜索代次/到达序号与核心变化可比，重建不删除原档案。

本地媒体由 Tauri Rust 资源桥按 Blob/Replica ID 请求 daemon 的受控流读取，支持 Range、取消和读取租约；不将任意绝对路径或无限 base64 文件送 JS。若使用 Tauri 自定义协议，明确 MIME、响应头和来源策略；不为播放器另开无鉴权通用文件 HTTP 服务。直接播放失败时受控外部打开已校验本地对象，参数数组调用并校验路径。首轮不必自动转码，但该失败路径必须可用且原件不变。

## 6. 运行预算与可调整默认

长期产品默认仍以[需求默认提案](../requirements.md#defaults)为正本。以下为首次集成的保守工程初值，可由 B 在固定负载测量后调整并记录依据；不代表网站允许配额或性能承诺。

| 项目 | M1a 初值与行为 |
| --- | --- |
| 每站元数据请求 | 同时 1 个，间隔至少 2 秒并加抖动；优先新检查，历史分页让出预算；Retry-After 优先 |
| 媒体传输 | 全局同时 2 个、同来源账号同时 1 个；FFmpeg/ffprobe 重任务同时 1 个；流式背压，不缓存整个媒体 |
| 超时/重试 | 连接 10 秒、元数据总时限 30 秒；普通瞬时失败最多自动重试 3 次后显式状态；下载按无进展时间判断，不设导致大视频必失败的短总时限 |
| 登录/挑战 | 失败归 waiting_auth/waiting_user，不进入高频自动重试；重新授权/用户重试后开启新 Attempt |
| 一轮枚举预算 | 最多 10 页；未结束则持久化进度、标部分覆盖并按预算续调度；不是只允许保存 10 页或将其当终点 |
| 初始有界历史 | UI 可选从现在、最近 7 天、最近 30 天；缺乏可靠时间跳转时逐页判定并显示进度/未知，不因单个旧置顶停止；完整自定义回填管理在 M1b |
| 原生测试/实测 | 统一隔离 Profile；真实实测默认最多 2 位作者、每位 2 个选定视频、元数据请求 30 次、总下载 1 GiB；ACT-002 可按样本明确调整，达到边界停止 |

DB actor 使用有界队列与短事务，读池上限由测量确定；满载时控制 UI 可见背压，不能无限增长。SQLite 启用外键/WAL/FULL、busy timeout，数据库锁错误有可恢复语义；OS/文件系统持久化限制在证据写清。媒体、备份和搜索任务都有取消与内存边界。

M1a 禁用自动物理回收/质量替换；停订阅不删历史。内部仍有代次与引用保护，对取消/删除旧任务到达做拒绝提交测试，为后续功能保留正确语义。

<a id="closeout-runtime"></a>
## 7. TRACE-CLOSEOUT-02 实现合同（2026-10-07）

### 数据、恢复与任务

当前 schema 为 3：从 2 增加 `media_items.acquisition_evidence`，旧档案经现有事务迁移，恢复接受 schema 1～3 后由 Core::open 迁移。备份格式仍为 1；恢复在写目标前校验 manifest 摘要、对象集合/大小、数据库完整性和外键，拒绝非法 blob ID 与链接组件。输出 `profile.json`、`data/state/catalog.sqlite3`、`archive/objects/sha256/<prefix>/<sha>`；恢复新 archive_id，不继承凭证和活动租约；缺失媒体副本 absent。返回 `profile_path`、`restored_objects`、`missing_objects`、`credentials=require-relogin`。

Job 的 owner 是每次认领唯一令牌，租期五分钟；事务提交核对 owner、状态、目标代次及有效期。queued/waiting_retry/waiting_storage 到期可认领；普通瞬时失败自动重试最多三次，此后 failed；磁盘等待间隔 60 秒，手动重试开启新代次。执行器在所有必要资源处理后统一 succeeded，单对象 COMMITTED 不改变整组终态。暂停/退出在可恢复边界回 queued，取消递增代次，旧结果不能提交。物化验证失败为 QUARANTINED，重试可重收；VALIDATING/PREPARED/OBJECT_READY 根据 expected_size/sha 重新核验后重入提交。

### 传输和媒体

媒体连接 10 秒、读取无进展 60 秒，无媒体总时限；单次写入至多 256 KiB，文件同步后记录检查点。已有检查点重开先重算前缀摘要。Range 必须有强 ETag，校验 206、完整 Content-Range、总长与 ETag；200/变更身份拒绝拼接。当前 Bilibili 计划没有可持久使用的强校验器，所以任务重试重解析并从头下载；独立传输测试证明有强 ETag 时全文件续传哈希正确。429=RateLimited，401=AuthRequired，403/404/410=SourceUnavailable（有界重解析），5xx=TransientNetwork，不保存上游错误体。

逐跳 scheme/主机/地址校验，DNS 连接固定到通过检查的地址，关闭系统代理，跨源剥离 Cookie/Authorization。显式 `test_allowed_hosts` 仅用于合成 Profile，发行示例为空，daemon doctor 对非空发行配置报失败。

durl 为有序 video_segment 必要资源，各段单独存对象；不拼接字节。音频初始必要，只有明确空音轨或混流证据且无独立音频候选才免除，依据落 acquisition_evidence。领域 save_progress 决定保存程度；没有目标比较时 quality_state=unknown。锁定后端可用且分轨已 probe 时生成 playback-mux-v1 派生件，原始轨道保留；不转码。

FFmpeg 查找：Profile.media_dir → 程序同目录 media → debug 开发 target/media → 显式 allow_media_path 后 PATH。前三级核对锁文件 receipt 与每个程序/DLL 摘要；所有来源都核对两程序同版本、许可变体。调用异步、重任务同时一个、输出上限 1 MiB、probe 30 秒/mux 300 秒、可取消且无窗口。锁与获取、许可的事实见[FFmpeg 回写](../research/2026-10-01-ffmpeg-integration.md)。

### 查询和媒体 IPC

协议仍 0.1。`ArchiveTimelineArgs` 新增可选 query/namespace/oldest，保留 creator_id/account_id/kind/save_state/cursor/limit；默认 oldest=false，limit 默认 50、夹在 1～200。Rust `TimelinePageDto/TimelineItemDto/TimelineCounts` 是结构正本；TypeScript `api.ts` 对齐，`timeline_page.json` 是双方共用样本，旧缺 counts 与未知可选字段可反序列化。

单次 timeline 查询在同一 DB actor 回调内读取列表、计数和领域状态。cursor 绑定整个筛选条件及排序；未知时间最后，entry_id 稳定破同时间。counts.global 是全档案 entries，counts.filtered 是全部命中；页面当前已加载另计，不以首页数量冒称全量。附加 account_id/name、native_account_key、creator_id/name、source_key/url、cover_blob_id；源链接仅由核心按已知平台和稳定键构造。搜索为规范化字面条件，不注入 FTS/SQL；主页/内容库组合搜索分页均走此合同。未对十万级数据库做性能验收。

`media.lease {blob_id}` 只接受 sha256 加 64 个小写十六进制摘要，并检查 verified 副本和实际大小；租约有 TTL，至多 256。`media.read {lease_id,offset,length}` 拒绝负偏移、零/超 512 KiB 长度；`media.release` 释放；前端只使用 `trace-media` 的 blob URI。Tauri 桥 GET/HEAD/起点/后缀 Range 每响应至多 512 KiB；非法 Range 返回 416，携带 Content-Range、Accept-Ranges、明确 MIME、nosniff 和受限 CORS，CSP 显式允许媒体协议。

`media.open_external {blob_id}` 由核心重算摘要并复制为核心生成安全扩展名的媒体文件，再用参数数组交系统打开；不接受任意路径。`archive.open_source {entry_id}` 由核心查来源并构造 HTTPS 链接。用户看到编码不兼容/缺失/断连反馈，系统媒体程序体验仍需 ACT-003。

### 事件恢复

FrameReader 将 header/body 偏移保存在 select 外，分片读取可取消重入；不在被竞争丢弃的 future 中使用 read_exact。新连接从当前 outbox 最大 seq 起订阅，客户端先拉快照；不从 0 重放。变更队列最多 256，溢出使连接断开并走重连/快照，瞬时进度可丢弃。Client Drop 取消读取任务并释放短连接；Tauri 事件泵重连发 resync，主页监听变更/断连/重连并重新读取，卸载时移除监听。当前不提供任意历史游标回放 API。creator.changed 在持久创建/编辑事务中发出；源事件与大帧并发已做真命名管道用例。


备份/导出控制命令受理返回 durable job_id，在任务页读取 succeeded/failed/last_error。空参数 backup.start 默认写 Profile 的 data/state/backups 并包含媒体；backup.list 只列有完整 manifest 的本地默认备份（最多 200 份），不是固定空列表。恢复返回已验证 Profile 路径与恢复/缺失对象数量，界面不把“受理”描述成“已恢复”。失败注入覆盖导出目标是既有普通文件、保持原文件且任务 failed。


## 8. 四项集中返工合同（2026-10-07，B）

本节承接 A 在收尾报告 §9 确认的 F-007/F-008/F-015/F-016；不改变 schema3、协议 0.1、必要音频或已核实的恢复/IPC 约束。实际跑次见[报告 §10](../delivery/stage-report.md#b-closeout-rework)。

- **HTTP 完成条件**：新下载只接受 200，续传只接受强 ETag 身份一致的 206；Content-Range 必须从请求偏移开始并到达声明总长。响应声明、上游已知长度必须相容；每块写入前检查累计上限，EOF 检查实收总长。chunked 正常结束不免除长度校验；201/204/205、截短、超长均报错，不能进入 finish/install。失败保留可恢复的 RECEIVING 状态，适配器无可靠校验器时仍安全重下。
- **有序分段校验**：UrlSet 新增可选 expected_size/expected_sha256，描述单段；MediaCandidate 同名字段描述按序全体的总字节数/顺序字节摘要。缺字段为未知，旧消息可读；不将整组大小或摘要分配给每一段。Bilibili 保留每个 durl 正大小，仅所有段大小已知且求和不溢出时提供已知总量。核心逐段校验，整组已知总量/摘要核验后才提交新分段引用；原件仍是独立有序文件，计算整体摘要不创建拼接媒体。只给总量的旧适配器也须经过整组门槛。校验依据以不含 URL 的 acquisition_evidence 和物化 expected 字段持久化，重入继续验证；派生 video_segment 不再次传给 resolver 作为原始资源。
- **订阅暂停/恢复**：subscription.update 的版本比较、订阅状态和关联 scan 的状态变化在同一事务。暂停将非终态及待重试扫描置 paused，撤销租约并增加目标代次，保留游标与已有失败次数；认领及页事务还核对订阅 enabled。迟到页和终态写入不能越过撤销的租约。恢复将关联可恢复扫描重新 queued/立即到期；缺失周期扫描按稳定 job_key 补建。重复 enabled=true 不撤销正在执行的扫描或复制任务。恢复扫描仅回收 leased/running，重启不解除订阅暂停；行政暂停不记一次失败。
- **主页事件协调**：继续监听 change/resync/disconnected；刷新不先清空已有结果。相同筛选重读已加载页，并沿先前末项时间/id 排序边界保留窗口；逐页仍遵守 50 条请求上限。若选择因排序变更暂时不在窗口，使用新增可选 ArchiveTimelineArgs.entry_id 在同一筛选合同确认其是否仍匹配，确认有效则保留，空结果才解释切换；旧调用缺此字段不改变查询。只有接受最新请求结果后才更新列表/统计/游标；请求代次阻止旧查询覆盖新筛选或刷新。仍有效的 selectedId 保留，详情刷新保留现有媒体，实际变更才切换选择并提示。MediaBrowser 按仍可用的 variant_id 保留选择，未变的 blob URL/媒体节点不重新创建；不可用时说明并选择可用资源。用户改变内容选择会开启新的详情代次，晚到旧详情不覆盖当前播放。
- **本地构建对应**：便携包 manifest 同时记录构建基点/dirty、source_files_sha256 和 source_tree_sha256；脚本在构建前后比对输入内容，输入变化则拒绝交付该包。源码摘要不声称可重现二进制构建或公开发布授权。
