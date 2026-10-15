# M1 · TRACE-M1-01 实施与审查记录

B原r1实现已交付且当时自动自检 **passed**，A对r1的原审查结论保留为 **rework-required**；B r2实现、290项自检和正式资源证据经核对有效。2026-10-09 A最终集中复核 **failed / rework-required**：F-019/F-021/F-022原修复已核实，新增F-023～025三个P1反例实际失败；产品验收 **not-run**。同一[唯一计划](stage-plan.md)仍为r2，当前复核见[§16.13](#a-m1-r2-review)，集中返工见[当前A→B prompt](#a-to-b-m1-rework)，原B最终证据保留[第15节](#final-m1-delivery)及§16.8～16.12，用户依赖只维护于[USER-ACTIONS](../../USER-ACTIONS.md)。历史失败不改写为当时通过；各结果仅适用于明确版本和范围。

## 已合并基线的保留与提炼

旧收尾报告§1～12及原始失败/返工/复审已归档于[固定基点报告](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/delivery/stage-report.md)；F-001～017的当前守护见[状态](status.md)，原发现和复验见[固定审查](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/evidence/2026-10-01-m0-m1a-review.md)。旧证据的通过只适用于当时源码。旧稳定包和验收Profile继续保留，具体操作仍在USER-ACTIONS。

P0整理已核对固定基点是origin/main祖先；11份过时过程文件（8份Markdown、3份JSON）与基点一致，先修全部Markdown入链为完整SHA链接、提炼到当前状态/计划/本报告，再从工作树退役。技术研究、运行契约、ADR、许可证继续保留；原A probes、B closeout_rework、home事件断言和full_loop断言保留原位置，未进行测试迁移。三个原Rust测试body的等价调整及逐测试审计见14.16；原109项均保留。

<a id="m1-planning"></a>
## 13. M1 A 规划与开发流程核对（2026-10-07）

本节为新阶段准备；前文是已完成基线的原交付/失败/复验，不作为M1新增功能通过记录。当前唯一计划TRACE-M1-01 r1，分支codex/m1-two-site-archive，基点960984c34487244f31f1238dffc2bd9faaa292e7。开始工作区干净，A仅修改治理/研究/计划和相应检查，没有修改生产Rust/Vue。未启动B/子Agent，未发送跨聊天消息，未push/PR/发布。

### 13.1 逐专题核对与应用

本次按本地索引逐份读取开发入口、工作流、Git规范、评审、贡献、质量、治理、架构/数据/插件规则、仓库地图和交接/提案/模块/决定/验证模板；另外核对索引、登记、阶段计划/状态/用户待办的组织方式，以及CI/ruleset/PR模板。产品专属需求与全部历史台账未逐行通读，不声称已审阅外部项目全部产品文档。只吸收适用开发规范，TRACE保持自身Rust/Vue/Tauri、档案安全与许可边界。

| 核对专题 | 本仓实际落地 | 边界/未完成项 |
| --- | --- | --- |
| 入口、连续工作、角色与审查 | AGENTS/工作流/REVIEW/CONTRIBUTING与交接/验证/PR模板：A规划、B实施自检、A最终集中审查；A通过后直接向用户收尾 | 不自动启动/消息；不无条件要求双方来回prompt |
| Git与保护 | 沿用本仓阶段单提交、DCO、明确远端授权；本次只读回查main规则active、无bypass、PR+squash+必需检查 | 没有本阶段push/PR授权；不复制额外App/hook/许可证 |
| 用户沟通 | USER-ACTIONS保持唯一用户状态；Q/H/ACT分类允许旧编号保持；用户非阻塞项跨阶段顺延 | 解析/代码缺口不转嫁人工；LOOP转由status唯一维护 |
| 文档治理与清理 | currentStage=M1、retireAfter与数值比较反例；唯一计划替换；提炼当前状态/事项、固定历史链接；P0给出内容分类清单 | 本次未删除旧证据/测试/包/Profile；完整提炼与回归映射由B同阶段执行，不假称完成全仓去标签 |
| 质量与证据 | 修正无CI过期声明，明确治理与产品门禁；要求源码/用例/包/资源对应及反例 | test/bench/package/report聚合尚待B建立；无虚构现成命令 |
| 架构、数据、插件与设计 | 增量契约保持核心数据所有权、短事务、旧代次拒绝、凭据隔离、有界媒体、离线读写与兼容 | 不引入其他产品的AI功能、运行时或UI框架 |
| 研究与选型 | 本地多站解析结合公开互联网检索；两站类型/路径/错误/认证矩阵、详细元数据、HLS/HTML候选维护许可 | 新库未安装/集成实测；真实站点未请求 |
| 长阶段计划与模板 | TRACE-M1-01 r1，P0～P10、C01～C10、清理、完整旧M1需求、包/测量、B→A与A收尾规则 | 这是A规划完成，不是B生产实现完成 |

### 13.2 实际查询与限制

本地只读检查git status/log、跟踪与忽略目录、索引、协议DTO和现有能力；使用GitHub REST只读核对两个仓库的main ruleset，TRACE实际规则与目标一致。互联网读取社区Bilibili动态/opus/专栏/直播协议文档、抖音官方能力说明及长图文报道；读取crates.io/GitHub的HLS/净化候选版本、许可、维护与问题样例。来源和推荐在当前解析研究，不宣称第三方关闭问题已随发行修复。

查询中一次PowerShell通配路径rg失败（db/*路径参数），不作为代码检查成功；公开文档中文通过终端输出时曾出现编码显示异常，接口路径另经搜索正文交叉核对，不使用乱码内容推断业务事实。首次文档检查因本节锚点尚未建立失败1项；随后检查发现事项总表精炼漏保留“开源集中处理”锚点，失败1项。已补兼容锚点并复验，原失败不抹去。

本轮生产站点/媒体/原生产品/打包/性能测试均not-run（本轮没有修改生产实现）；B在本阶段按计划完成。原包与用户验收Profile、F失败和原测试保留。长图文完整响应/签名/特殊资源的协议缺口登记OSS-009，不以unsupported静默缩减目标。

### 13.3 当前交接

A规划与治理完成度、实际自检结果见下方追加记录；B新增生产实施未开始，A产品实现审查not-run，产品验收not-run。用户事项变化仅USER-ACTIONS的ACT-002方案准备和阶段说明，ACT-003/004与Q-002/004原状态继续。完整可复制A→B prompt在唯一计划第8节，最终SHA由Git交付消息提供；本报告不递归写回自身SHA。

### 13.4 A 实际自检

- `python scripts/check_docs.py`：最终结果在artifacts/m1-planning/check-1.log；正文/登记/链接及82个需求、25个AC、12个INV、18个ADR完整。
- `python scripts/check_publication.py`：工作区与暂存内容0发现；脚本是有限规则，已另核对新文档没有本地参考关系/路径与真实资料。
- `python -m unittest discover -s scripts/tests -v`：7/7 passed；包含原5项反例，以及M9/M10数字排序、非法阶段和文档到期反例。
- 工作区与暂存 `git diff --check`：passed。保存本地提交前再次核对暂存版本和归属。
- 原stage-report全文是当前文件的未改动前缀；原A探针、B返工测试、home事件测试相对基点无差异。无生产Rust/Vue改动，故本轮未重跑产品全套/包/原生/性能。
- 新增PR模板，供将来有明确授权时使用；未创建PR，未变更远端设置。

结论：A规划与治理自检passed；M1 B实现尚未交付，A对新增产品实现审查not-run，产品验收not-run。阶段本身未完成，下一步是按唯一计划交B连续实现。最终提交记录与证据索引由Git和artifacts/m1-planning/checks.json定位。


<a id="m1-implementation"></a>
## 14. M1 B 连续实施记录（过程历史）

基点 `dcbdf82cd74e38e6c09330789490ed96d7169c5e`，父基点 `960984c34487244f31f1238dffc2bd9faaa292e7`。接手时工作区干净，新增改动归本次B。未修改main/备份历史，未推送、创建PR、合并、发布、启动子Agent或跨聊天发送。§1～12已按固定链接提炼保留，§13为A规划时事实；以下按各次实施时点记录，尤其历史失败不改写为当时通过。其中“尚未完成/实施中”只描述当时状态，当前结论以第15节为准。

### 14.1 当前实现与定向证据

| P/C | 当前结果 | 证据与边界 |
| --- | --- | --- |
| P1/P4、C01/C02 | 生产 Douyin stdio 适配器、A-Bogus/本地摘要、账号与投稿/详情、type163全文、图集/配乐/实况映射 | `m1-douyin-golden-first.log`、`m1-production-tests-first.log` 通过；未知 shape、会话条件与真实站点仍须完成矩阵 |
| P2、C02/C07 | schema3→4 前一致备份、迁移事务回滚、旧 DTO 默认、惰性文档、安全扩展、正文/观察/媒体任务原子提交 | `m1-data-tests-first.log`；`m1-correctness-tests-second.log` 的4项通过 |
| P3 | Bilibili 动态/opus/专栏及引用内容、房间映射与生产请求接线 | 不是完整类型矩阵通过；文集、字幕和混合卡等仍须补实现/验证 |
| P5、C04/C05 | FLV/HLS 接收、AES/NONE/MAP/range/序列、持久场次、手停、原始接收资源与派生来源、封片恢复 | `m1-recording-tests-third.log` 6项通过；磁盘/退出/休眠/独立音轨及全部崩溃窗口尚需定向证据 |
| P6、C01 | 跨轮次游标访问记录、范围修改重置与租约撤销、凭证分会话、插件停用覆盖录制 | C01 范围与代次定向通过；周期策略和全生命周期并发反例仍需完成 |
| P7、C08 | 两站多类筛选、惰性正文与本地资源、录制视图；A→B→A 通过观察序号选择当前版本，搜索/导出/详情一致 | `m1-correctness-tests-second.log`；后续新增详情 body 断言仍须复跑；旧前端全32项（含home）在 `m1-desktop-second.log` 通过，更新后的前端仍须最终复跑 |
| P8、C07 | 验证新质量后切换、必要音轨阻断、共享/pin/独有直播保护、24h等待、两阶段可恢复回收 | `m1-quality-tests-first.log` 定向通过；偏好与保留 UI、自动升级/回收接线及全备份恢复组合仍须完成 |
| P9、C09/C10 | 源码输入与必需用例拒绝门禁、优化测量构建配置和合成本地负载入口已新增 | Python门禁反例初轮通过；当前缺项必须拒绝 M1；60分钟/原生GUI/包/最终源码指纹尚未完成，不以短烟测替代 |
| P0/P10 | 固定历史链接和既有材料仍保留；本节先记录事实，整理与最终交接尚未完成 | 旧稳定包、验收Profile和原 A/B 断言不删除；无最终阶段 SHA 或 B→A 完成交付声明 |

上表日志均位于忽略的 artifacts 目录，不含真实站点资料；它们绑定各次当时源码，不能直接替代最终源码的聚合门禁。最近的 `m1-check-recording-provenance.log` workspace check 通过，后续新增测量代码仍须再检查。

### 14.2 失败保留与等价修复

- 早期 workspace check 失败依次涉及摘要十六进制输出、锁文件变化、rusqlite backup API 和 aes0.9.0 被撤回。均保留原日志，再修实现/锁版本；不删除失败跑次。
- `m1-desktop-first.log` 的3个原 home 失败来自实现丢失 `.detail-description` 类。恢复原 DOM 契约后，`m1-desktop-second.log` typecheck、32个 Vitest 和 build 通过；未删断言。
- `m1-full-loop-range-first.log` 的 `variants=0 entries=0` 来自合成固定2023日期超出近期365天。fixture 仅将原两组时间平移到执行 UTC 日前1天/前3天，保持两天顺序；所有原断言保留。第二轮 `m1-full-loop-range-second.log` 命中凭证哨兵断言，因为新隔离逻辑要求订阅显式绑定凭证。测试保存 import 的 credential_id 并传给 subscription.create；没有降低凭证或网络断言。第三轮 `m1-full-loop-range-third.log` 通过。
- `m1-correctness-tests-first.log` 新C08测试调用了不存在的 `archive.timeline.query`；改用已声明控制协议常量 `ARCHIVE_TIMELINE` 后第二轮4项通过。
- `m1-check-correctness-first.log` 摘要数组不支持 LowerHex，改逐字节编码；第二轮发现旧测试 Profile 显式构造缺新增配置默认值，补相同默认值。`m1-check-quality-first.log` 与 `m1-check-recording-provenance.log` 通过。
- Windows 若输出 incremental finalize access denied note 或 linker stdout warning，保留输出及真实退出码；不能据其存在改称编译失败，也不能将严格 clippy 的未跑状态改成通过。

### 14.3 当前未执行边界

真实两站账号/凭证/风控/下载录制 not-run，授权只在 USER-ACTIONS ACT-002 维护。原生产品体验、正式包和资源目标未获得本阶段完整证据。当前未完成的代码/自动测试/测量由 B 继续处理，不能转成人工待办或非阻塞优化。最终仍须完整 P0～P10/C01～C10、原回归与治理门禁、自审、独立包、源码指纹、DCO amend 和 B→A 审查 prompt。

### 14.4 有界详情、录制和隔离增量自检

以下是后续定向跑次，仍不代替最终源码全门禁。`m1-workspace-build-second.log` / `m1-workspace-tests-second.log` 全工作区通过，覆盖原 A probes、B closeout_rework、full_loop 和长流；其后代码继续变化。`m1-workspace-build-third.log` 通过。

- C08：`archive.entry.get/page` 按修订、观察、资源分页；`archive.revision.read` 以 Unicode 字符偏移分块读取完整正文/文档/元数据。130 修订、225 观察、260 资源及长中文/emoji 的无损重组和帧上限在 `m1-archive-pages-tests-third.log` 两项通过。头两轮因新 fixture 缺 account_id、observation_id/seq 失败，原日志保留。详情切换拒绝旧返回，历史与直播分片保留已加载页；重复 details toggle 的实现错误保留于 `m1-desktop-pages-second.log` / `m1-desktop-pages-tests-third.log`，修后 `m1-desktop-pages-tests-fourth.log` 35 项通过，原 home 断言保留。后续前端修改仍需复跑。
- C04：FLV 同连接按时间/字节在关键帧轮转；区分原始接收和派生片，写满前成功字节仍计入摘要。修复 AVC sequence-end 空片和 script/config 零时戳重置时钟；第4～7轮失败日志保留，`m1-recording-tests-eighth.log` 10 项通过。涵盖独立 HLS 音轨坏片重试、MAP 的声明时密钥、源时钟回退/跳变、codec 改变、磁盘余量、取消重开后的 RECEIVING/PREPARED/OBJECT_READY/COMMITTED 窗口。休眠用例是合成时钟跳变，不冒充系统休眠实测；后续原始片 provenance 修改还需复跑。
- C05/C06：插件 pending 用 RAII 清理，超时涵盖 stdin 写入，shutdown 封闭新会话并统一取消；hello 核验平台。同平台双凭证、跨站/访客隔离、跨源删除 Cookie、36 次取消请求不泄漏、停一站不影响另一站在 `m1-host-isolation-first.log` 通过。当前测试已按实际范围命名为 `c05_plugin_process_isolation_and_cancelled_request_cleanup`，不表示所有任务生命周期矩阵已完成。`m1-nested-hls-security-first.log` 通过嵌套 variant/key/MAP 私网、IPv4映射IPv6和 file URL 拒绝；release 构建忽略 test_allowed_hosts。
- C07：真实 FFmpeg 生成两种规格视频，走对象提交/probe/选择，修复新变体错误覆盖同资源旧变体规格，`m1-quality-measured-regression-first.log` 通过。相同修订再次观察会重新解析质量；派生 mux 绑定当前选择的确切 blob pair。新增回收恢复摘要校验尚需旧质量套件复跑。
- P6：resume_cursor 参数校验，新增订阅独立 deep lane，check_now 复用固定增量 job；仍须完成旧订阅深查补齐、近期窗口预算、手动单条补查和批量失败重试。

这些增量仍有协议、UI、全任务生命周期与正式包/资源/聚合证据缺项；M1 保持 in-progress。十万条短测仅用于实现反馈，不得作为 60 分钟录制或 GUI 生命周期达标结论。

### 14.5 生产资源、调度与质量增量

以下跑次继续保存在 `artifacts/`；源码随后仍有修改，全部为定向证据。

| 范围 | 新行为与观测 | 验证 |
| --- | --- | --- |
| P3/C02 | opus 投稿卡在正文原位置解析引用视频；子引用失败保留父正文和逐项分类。专栏文集成员、安全播放器章节和各 P 字幕进入元数据/资源计划；字幕 JSON 校验时间段与文本，不信任下载后缀 | `m1-production-tests-second.log` 4项通过；新增引用视频说明断言尚需下一轮生产进程复验 |
| P4/C02 | 扁平及嵌套图集、优先原图、实况片、缺静态图占位、type163的video地址作为BGM；公开作者简介映射后净化 | `m1-douyin-tests-second.log` 5项通过；质量偏好按可用流的宽高/FPS选取并保存对应声明，`m1-douyin-tests-third.log` 6项通过 |
| P5/C04 | HLS MAP使用其声明当时的KEY；同片前KEY/MAP顺序、取消加密、key URI中引号逗号均保留语义 | `m1-recording-tests-ninth.log` 11项通过；m3u8-rs继续负责属性解析，包装仅保留标签时序 |
| P6/C05/C06 | 同会话代理请求有界并发、空闲120秒退出、停止时撤销代理任务；适配器失败仅传递分类。元数据最多连续4次紧急请求后给普通详情一次机会 | `m1-core-tests-third.log` 66项通过，含慢/快请求并行、空闲重启、分类不回显正文、公平调度；API重试/用户动作随后按分类补齐 |
| P6/C01/C08 | 旧订阅补独立deep lane；增量每轮页预算后回首页，保存预算和未访问游标；新增逐条补查、显式失败任务批量重试、观察证据全文分页 | `m1-desktop-controls-typecheck-first.log`/`m1-desktop-controls-tests-first.log` 类型与35项测试通过；第二轮含字幕读取和质量配置，类型与35项测试通过 |
| P8/C07 | 保存可得/历史目标及能力下降证据；质量偏好可配置，切换后旧目标不足以证明新偏好满足。持久低优先级维护分批重评、重建当前源组合的播放文件并安全回收 | `m1-core-tests-seventh.log` 67项通过，包含既有质量/共享/pin/恢复反例；维护任务完整端到端反例仍需补充 |
| P5/C04 | 本地临时场次身份就地升级为来源身份；新场次封口旧场次，手动停止不被重复轮询复活，entry与已有分片保持 | 同一核心第七轮新增用例通过；身份升级的依据在场次证据中保留，原修订仍不可变 |

编译失败 `m1-quality-preference-check-first.log` 是把JSON辅助错误放入仅dev依赖serde_json的domain，移入contracts；第二轮补Core宏导入和仅测试wrapper标记。`m1-core-tests-fourth.log`/`fifth.log` 分别发现未声明hex依赖及新摘要数组无LowerHex，改用既有逐字节十六进制方式。`sixth.log` 的1项失败是Core.open意外建立维护任务影响无调度场景；改为启动scheduler时建立，不改原计数断言，`seventh.log` 67项通过。

### 14.6 资源短测与当前差距

`m1-measure-smoke-3`：十万条/五十万媒体元数据，闲置5秒、录制180秒，退出0，查询p95约1412ms，平均CPU3.56%，峰私有工作集约23.6MiB，峰句柄643，4路录制但并行下载0。`m1-measure-smoke-4`：闲置125秒、录制240秒，退出0，查询p95约583.97ms，平均CPU2.36%，峰私有工作集约33.7MiB，峰句柄1008，并行下载仍0；有录制任务最终plugin_crash。两次源码运行期间有修改，均非验收证据。

已据此修复查询先取页再读取详情、普通详情调度饥饿、测量扫描job固定key和增量触发范围。闲置阶段不再持续每5秒发四个查询，首轮冷查询后只保留正常调度；旧闲置CPU口径不作为达标依据。第五轮短测用于定位双站下载和录制，最终1020秒闲置/3600秒录制、GUI生命周期、重启与检测延迟、独立包及聚合门禁仍 not-run。任何状态字段仍须由实际样本判断，不能用目标数字填充。

### 14.7 帧取消、恢复回归与本轮扩展

- `m1-host-framing-tests-first.log`：代理任务完成会取消正在读取的半帧，`read_exact` 已消费的字节因下一次读取而错位。改为每帧固定读 future，代理完成只继续等待；独立进程先写6字节、待代理回复再补余帧，三轮大中文帧断言通过。源码短测6任务库没有 `last_error`；不是通过减少并发掩盖问题。
- `m1-closeout-faults-regression-fifth.log`：原prepared/object_ready/between_resources/pause/shutdown/cancel全部窗口通过，字节和数量断言保持。根因是恢复fixture已有资源声明为空，新实现按整份声明比较，非质量字段也触发重复下载；现只投影实际质量字段，已提交操作遇真正规格变化有独立身份。定位时WAV替换无效，已撤回；前四轮失败保留。
- `m1-document-semantics-second.log` / `m1-document-metadata-tests-first.log`：Markdown/HTML列表、表格、标题、引用、代码块与安全省略原因通过；不执行来源HTML。`_trace_omissions`仅保留固定原因及数量，不回显原字段路径和值。
- `m1-account-identity-tests-first.log`：真实生产账号解析接线、固定身份/改名观察、长ID、跨站别名隔离、转发/原文及跨类型同号验证。新增account.resolve/history，资料观察追加保存；周期account_profile复用订阅凭证及代次，UI可离线分页读取。账号自动刷新和全生命周期仍需补矩阵。
- `m1-clippy-first.log`首次严格检查发现四项静态问题（嵌套条件、无效借用、FLV参数数目、测试HTTP读取长度）；逐项修后`m1-clippy-second.log`与后续`m1-clippy-third.log`通过，未添加lint豁免。
- `m1-governance-tests-first.log`：旧执行计划退役后，反例fixture不再创建第二份有效计划。现在隔离副本中真实创建/登记第二计划，保留原exactly-one-active和missing-anchor断言；`m1-governance-tests-second.log`9项通过。
- `m1-workspace-tests-fourth.log`：F-015最后计数由(1,2,0)变成(3,2,0)，M1新建深查和质量维护各一项。测试迁移仅将增量代次/预算查询定位到scan:s:incremental，仍严格断言(1,2,0)；另检查整个任务表恰好包含增量、深查、质量维护三项，不允许额外任务。原暂停、重启、迟到页面和请求次数断言全部保留。第五轮全套尚在复验。
- P0已提炼基线事实并将全部入链改到main基点完整SHA，退役11份过程副本（8 Markdown + 3 JSON，111211字节），49份现行登记文档检查通过。未移动/删除A probes、B rework、home测试及旧验收包/Profile；只有上述有明确映射的F-015查询适配新增任务模型。

### 14.8 一致导出与生产垂直链路

`archive_export`从一致数据库快照逐页导出，无10000条静默上限；当前archive.json、全部revision、来源观察、账号观察、直播会话/缺口分别保留。资源按MIME保留扩展名与SHA256文件名，复制后再次核对长度/摘要，缺失明示；完成清单最后原子发布，失败保留incomplete.json，不覆盖已有导出目录。账号元数据再过安全过滤。`m1-export-check-first.log`十六进制格式编译失败及`m1-export-tests-first.log`usize SQL类型失败均保留，修后`m1-export-tests-second.log`通过10006条分页、正文版本/账号历史/原图/缺口、坏文件无完成清单反例。

`m1-vertical-tests-fourth.log`通过：真实Bilibili和Douyin二进制经stdio，单元测试只替换元数据proxy响应和HTTPS媒体到本地HTTP的路由；这些注入点使用cfg(test)，产品构建不存在。七类条目覆盖B站专栏、动态、opus、多P视频以及抖音图集/实况/配乐、type163全文、视频；全部进入Core扫描/观察/资源任务、实际HTTP字节/对象提交、离线读取。统计变更不重下已验证资源，图文编辑创建两份完整版本。

第二/三轮垂直测试揭示MP4音频容器被文件头识别为video/mp4后遭误拒。现在对象层允许音频的合法容器，Core提交通用音频前必须probe实际音轨，图片必须有实测宽高；纯容器名/URL后缀不再充当该类资源的完整性证明。第一轮测试编译问题亦保留。`m1-desktop-fourth.log`新增fixture返回了DTO没有的字段，修为真实返回形状；`m1-desktop-fifth.log`类型、38项Vitest和生产构建通过，包含资料历史分页/换账号迟到结果、列表表格惰性渲染，原home断言不变。

另修复B站旧置顶视频触发日期过滤后提前break，改为逐项过滤并保留未知日期；has_more依来源count而非过滤后条数。增量扫描达到单轮页预算后保留并续跑未访问游标，直到来源结束才回到首页；不再反复读取前两页而饿死后续近期编辑。新增反例正在全套复验。

资源短测5：闲置125秒+录制300秒，2下载/4录制，hot p95约429.75ms、闲置CPU约0.172%，但有plugin_crash。短测6在帧修复后同规模通过进程退出，query p95约432.42ms、p99约450.98ms、闲置CPU约0.305%/私有工作集p95约19.56MiB、录制CPU约1.914%/私有工作集p95约23.25MiB、峰句柄654，任务库错误为零。两次运行中源码都有修改，status仍incomplete；不替代最终77分钟、GUI/重启/检测延迟、包和源指纹门禁。

本轮仍有直播弹幕/字幕附属、合集完整枚举、质量维护与全任务生命周期、正式包和资源聚合等必须缺口。M1继续in-progress，尚无最终SHA、B完成交接或A审查通过声明。


### 14.9 账号、合集、直播附属与执行中生命周期

保持同一分支和A规划HEAD，未提交、未推送。`m1-workspace-tests-fifth.log`和`m1-workspace-tests-sixth.log`分别在对应当时源码全workspace通过，含原A probes、closeout_rework和home守护；其后本节仍有代码变化，均不是最终源码证据。`m1-workspace-build-sixth.log`发现测试Profile未补三个新预算字段，补实际默认值后第七/八轮构建通过。现在Profile可配置并显示metadata最小间隔/抖动/每轮页预算，live按订阅实际周期运行；来源观测仍受有限预算。

账号profile任务仍按稳定account_id唯一保存。首个订阅暂停时，会在事务中转接到同账号另一个enabled订阅的凭证范围并撤销旧租约；全暂停/插件停用保留等待状态，显式取消不复活，attempts不重置。`m1-profile-transfer-first/second.log`分别暴露新测试缺range_json和违反每账号/类型唯一约束，修正为真实两类型订阅；`m1-profile-transfer-third.log`通过切换、迟到拒绝、全暂停、插件停用及取消反例。

Douyin枚举增加posts→mix.list→series.list→mix.aweme的独立有界状态。每次enumerate只发一个来源请求，游标最多携带20个待处理合集ID；空页has_more继续、源cursor不前进拒绝、超预算由core持久续跑。不同类型仍使用原生类型/ID，账号串线拒绝，合集页失败不宣称全集结束。`m1-douyin-enumeration-first.log`通过独立游标/乱序/空页/大ID反例；配乐/实况身份现在随内容身份改变，签名轮换不变。重建两站进程后的`m1-vertical-tests-fifth.log`七类从扫描到实际HTTP字节及离线读取通过，73.45秒。

直播弹幕提供用户可选的有限历史采样，独立低优先级任务约10秒一次，不影响主媒体任务成败。B站普通/管理员最近历史和抖音IM JSON历史均接生产适配器；公共ID、署名、文本与来源时间经core再次净化，按场次/消息ID去重，独立保留采样范围/省略数/失败分类。无设备ID、internal_ext或签名地址落档。不是WebSocket全量捕获，窗口之间消息可能缺失；嵌入画面的字幕随原件保留，外部字幕流的完整处理仍需补齐。新表随未发布的schema4迁移建立；当前已发布兼容输入仍为schema3。导出追加live-chat.jsonl和live-chat-samples.jsonl；离线界面惰性分页、纯文本呈现并拒绝旧场次返回。

`m1-live-auxiliary-first.log`保留新代码的摘要格式/usize SQL编译失败；第二轮三项定向测试通过。`m1-production-adapters-third.log`保留测试遗漏url依赖；第四轮locked拒绝未更新依赖图；离线更新现有锁定url引用后第五轮5项生产进程测试通过，包含实际合集/采样端点、身份/类型/秘密断言。真实网站仍not-run。

订阅创建UI新增明确凭证选择和弹幕开关，传递共享SubscriptionCreateArgs；原界面只在解析账号时使用凭证，新订阅会丢失选择，这一缺口已修。subscription.list按account_id过滤。`api.spec.ts`“订阅范围映射”的账号acc-1/30天/3600秒三个断言逐项迁移到共享DTO字段，值和精度不变；增测访客默认、凭证ID、直播采样与from_now，未删除断言。`m1-desktop-seventh.log`保留旧字段断言失败，第八轮类型/38项Vitest/build通过；原home事件断言未动。第六轮通过verify封装运行但runner读取不到子日志用例，cases为空，不可作为阶段用例证据；第八轮直接运行pnpm，实际解析38项。runner增加Rust可执行文件/模块级身份、Python和Vitest verbose解析及UTF-8环境，`m1-governance-tests-fourth.log`10项反例通过。

`c05_all_job_types_plugin_isolation_and_shutdown_sealing`覆盖六类采集任务×四种操作×两个站点，共24个双站场景：真实core executor/租约/对象提交/FLV封片，站点来源用专用合成协议进程控制阻塞点，不替代上述生产解析测试，专用peer不注册进产品或打包。暂停立即恢复、停B站时抖音继续、逐任务取消、退出均在实际请求或HTTP接收中发生；旧结果不提交，失败预算不消耗，录制闭合对象哈希核验。第一轮缺hello夹具；第二轮发现停插件先杀进程后撤租约导致attempts从2变3的真实竞态，现改为事务撤销后停进程。退出先暂停调度并撤销活动租约；失效job.fail不再产生进度事件。`m1-lifecycle-matrix-third.log`89.15秒通过，前两轮失败保留。`m1-clippy-fifth.log`在该矩阵修复前通过，最终必须重跑。

本节仍为in-progress：外部字幕流、质量策略反复切换/同表示规格变化及恢复组合、正式包隔离烟测、资源GUI/恢复/检测延迟和77分钟测量、最终指纹聚合及文档交付未闭环，不转为用户人工项或A已审。

### 14.10 字幕、维护任务与首包验证

直播订阅可独立配置弹幕历史采样与外部字幕，创建和修改均经共享DTO/IPC/Tauri接入。账号详情读取创作者全部账号的订阅。HLS master的SUBTITLES轨按RFC8216有界读取，每轮最多4轨、每轨4片、总5秒；所有清单、MAP、密钥和片走受控网络，VTT原文与派生对象保留，字幕失败独立记录、不改变主A/V成败，无外部轨明示。`m1-subtitles-first.log`通过，保留原四片/音轨/恢复断言后追加坏字幕重试、去重、中文原文验证；真实站点外部字幕仍not-run。`m1-export-backup-first.log`验证弹幕大整数ID、采样范围、账号历史、手动保留和真实媒体字节经全量/仅元数据备份恢复；恢复订阅暂停、凭证清空。

获取操作身份现由representation、质量规格、expected_size/hash共同决定，签名URL轮换不重建身份。已提交但损坏或缺失的对象使用稳定repair后缀，保留原收据；切换分辨率/帧率目标会重新获取和重评。派生播放件要求第0源为当前video、第1源为当前audio。`m1-quality-acquisition-first.log`缺json宏编译失败，第二轮5项通过。全套`m1-workspace-tests-seventh.log`随后发现原prepared窗口多出操作：shutdown增加代次令恢复不能复用durable操作。现退出先暂停调度、撤销租约并保留代次，再取消Core与等待worker；新Core实例隔离旧租约。closeout_faults仅更新夹具操作身份计算，数量、字节、窗口和迟到断言不变；`m1-closeout-faults-regression-sixth.log`全部窗口通过，52.31秒。原失败保留。

备份和导出仅在租约有效事务内发布完成清单，复制改异步、哈希移出执行线程；质量回收同样检查当前租约。`m1-local-lifecycle-first.log`42.12秒通过backup/export/quality三类任务在archive gate等待时的暂停恢复、取消、退出、停插件矩阵；未冒充大文件复制中途的全时点注入。`m1-media-network-first.log`验证本地FFprobe/FFmpeg只允许file/pipe和媒体格式白名单，伪装HLS/concat被拒且本地监听零连接。`m1-document-tables-first.log`验证表格正文及图像行列位置不丢失。订阅默认按from_now，普通300秒、直播独立30秒。

资源脚本改为记录Windows Job累计CPU（含已退出子进程）、精确GUI进程集合、实际desktop-events连接、开关/重连时刻、开播到首字节及完整Core重开恢复时间；聚合从原始样本重新推导，短测或缺GUI必须失败。此处恢复测时是Core完整重开，不声称操作系统进程崩溃测时。新脚本尚待运行；`m1-clippy-seventh.log`发现Result别名误用，修后`m1-clippy-eighth.log`严格workspace/all-targets通过。门禁增加日志重新解析、根文档指纹、证据路径与指标篡改反例；`m1-governance-tests-fifth.log`和`sixth.log`11项通过。

`m1-package-first.log`成功生成首个M1便携包，22文件、196.1MiB；包含两站、许可台账、实际构建日志和二进制摘要。`m1-package-smoke-first.log`失败于exe库存名单遗漏锁定资产自带ffplay，尚未完成进程烟测，不记C09通过。`m1-rust-eighth`的workspace build成功，但嵌套PowerShell变量展开令后续条件错误，cargo test未运行，cases为空，不能算测试证据；直接执行cargo test的第九轮正在复验。本阶段继续in-progress，未提交、未推送。

### 14.11 首包桌面失败、资源短测与继续修复

`m1-rust-ninth.log`最终退出0，完整workspace通过，包含原A探针/closeout_faults/closeout_rework；之后本节仍有源码修改，不作为最终指纹证据。`m1-list-images-first.log`验证HTML/Markdown列表中图片标记与前后文字、编号、必要资源位置；`m1-subtitle-provenance-first.log`在原录制用例追加分离VTT头/片的两份原件来源链和停止后不再增加字幕，全部断言通过。

使用Windows原生窗口检查首包时，页面显示localhost连接拒绝；Tauri事件线程已有连接，不能据此认为页面可用。根因是直接cargo release未启用`tauri/custom-protocol`，未进入生产内嵌前端模式。现新增桌面custom-protocol特性并由package_windows显式启用，check_package核对构建参数；新包及实际页面复验仍待运行。旧稳定包/Profile原样保留，不将这次发现改写成历史已验证。

`m1-measure-smoke-7`为125秒闲置/300秒负载、十万条/五十万媒体元数据短测：热查询p95 919.63ms、冷查询p95 816.41ms、闲置CPU约0.975%、闲置私有工作集p95 18.72MiB、四路录制CPU约4.092%、工作集p95 107.29MiB、GUI关闭约2.79秒、Core重开约1.27秒。未观测到两路并行下载，9条scan错误为transient_network，GUI未完成重连，运行期间源码变化；脚本退出1，status failed。新测量器不再把短测退出0升级为验收。已发现合成HTTP服务单次read未保证请求头完整，改为有界读到头结束；下一轮验证该原因，不先宣称错误已消失。质量维护原按全部正文修订搜索是否有副本，改从实际media_variants关联修订；时间线减少scoped临时物化，结果语义和旧断言不变，性能需复测。

Bilibili新增动态/opus音乐卡生产路径：音频ID→song/info→web/url，保留歌曲安全元数据、封面和必要音频，严格区分type=-1试听与完整流。`m1-music-production-first.log`通过完整歌曲与试听权限反例，试听时父正文可读且完整性不足，不伪称已保存完整音频。来自mix/series页面的集合覆盖现追加进每条source_observation，详情不返回mix_info也不丢来源关系。`site-check`有界协议入口和逐跳请求计数/失败停止正在自检，当前未执行任何真实账号请求。


### 14.12 协议、预算、查询与需求再核对（2026-10-08，实施中）

专栏新增JSON ops正文解析，保留标题、行内格式、列表、引用、图片及av/cv卡顺序；坏JSON与未知嵌入不冒充完整。av请求核对aid再映射BV，opus公式读取latex_content并保存列表编号/层次。公开依据为API文档固定提交271b123a083698bf576101c21f534b3418768a43的article/view.md。`m1-article-delta-first`通过；Rust全套第十轮`m1-protocol-reserve-tenth`失败于新生产夹具漏WBI view路由，待复验。原A探针和返工测试在该轮通过，失败记录保留。

共享磁盘预算随并发录制峰值、分钟余量和替换双份资源更新，逐块检查；storage.usage显示实际预留。`m1-storage-reserve-first`通过。资源smoke11/12观察到两路下载/四路录制且任务错误为零，但热查询p95为580.879ms/545.5048ms，均不达500ms目标；短测且源码变化，不作为最终验收。随后增加scope/order索引与限定字符精确FTS快路径，待下一轮测量。

新增日期、当前修订实际高度/codec筛选和别名/标签/来源URL检索；`m1-query-filters-first`夹具字段错误保留，second三项通过。凭证valid/authenticated统一，网络失败为unknown；`m1-query-auth-eleventh`编译失败修后twelfth通过。订阅质量DTO/数据库/视频检查仅通过`m1-quality-policy-first`编译，尚未完成UI/直播验证。门禁核对包内全文件、精确exe集合、构建日志与源码摘要，`m1-governance-ninth`12项反例通过，含真实symlink/junction越界拒绝。首包必须重新构建。

重新核对M1必须需求仍需补：严格/宽松质量UI和直播、凭证更新/重绑/后台合并告警、创作者别名标签编辑与归档、全历史/起止范围及配置编辑、删除范围/代次/等待回收、可选登录后启动、旧库来源URL索引重建。这些都是B开发工作，不转交用户或A实现。正式长测、新包、最终全套和需求映射未完成；M1保持in-progress，未提交、未推送。

### 14.13 管理、严格质量、凭证、告警与删除（2026-10-08，实施中）

严格质量现在逐候选筛选，并在实际媒体探测后再次验证；缓存命中及录制恢复同样不能绕过。直播不达标区间记录 strict_quality_unrecorded 和起止范围；原始捕获作为独立来源证据保留，不冒充达标播放分片。宽松策略保存期望/实际及降级标记。`m1-strict-quality-third` 的7项定向测试通过，前两轮编译失败保留；原质量用例追加实际64/声称256与旧选择不变断言，未弱化旧覆盖。

创作者页面加入显示名/备注/别名/标签、归档与恢复；归档不停止订阅。订阅支持从现在、最近天数、全历史和含首尾的日期范围，普通/直播检查周期、质量目标、凭证与附属轨配置。凭证更新沿用稳定ID，以新加密文件和数据库代次原子重绑，撤销旧任务代次并安排持久验证；网络失败unknown，授权恢复只重排有效订阅，取消/暂停保留。`m1-credential-update-first` 因夹具漏adapter目录失败，second补配置后通过；真实站点验证not-run。

后台新增合并告警、关联任务分页和Windows通知提交状态，GUI关闭仍由daemon处理。通知提交不等于用户看见，原生显示尚待补证。可选登录后启动使用固定exe/Profile和HKCU Run，参数引用/注入反例通过；未在用户机器启用启动项。删除预览绑定版本、资源及保留状态，分缓存/副本/档案，保留共享引用/固定副本并阻止旧任务复活；显式恢复校验代次，不恢复已取消任务。删除共享字节、陈旧预览、缓存范围和恢复用例已通过。`m1-controls-second` 中51项断言通过，但执行期间源码变化，收据拒绝作为最终证据；首次夹具编译失败仍保留。

`m1-production-adapters-sixth` 8项通过，修复前轮article ops/WBI夹具缺路由；`m1-management-ui-second`前端类型/测试/构建通过；严格clippy第十三轮两项失败修复后fourteenth通过。上述均为中途证据。独立资源库、时间展示、缓存入口、凭证探测生命周期、删除恢复边界和Windows接线仍在继续核对；正式长测、新包、最终源码对应门禁尚未完成，M1继续in-progress。


### 14.14 资源库、缓存、启动与后台验证（2026-10-08，实施中）

独立 `/resources` 页面经 `archive.resources` 稳定分页返回全部资源/变体、实际规格、保存时间、可用副本数及发布/账号/创作者来源，包含未保存资源；链接可精确定位主页发布。平台导航按实际插件清单显示，保留原 home 平台断言。`m1-resources-ui-first` 因静态平台入口撞旧断言失败，second 因重复字段类型失败，third 前端类型/全部测试/构建通过；原断言未删改。媒体保存时间与直播检测、本机捕获、封片时间分别显示，源时钟按相对秒显示，不能当作 Unix 时间。

搜索索引与派生播放件有独立预算/状态/清理重建入口。索引计费是保守逻辑估算，SQLite 空闲页保留复用；正文与原件不按缓存删除。清理暂停索引，重建换代并为每条记录产生工作；停用/预算满/重建期间不返回看似完整的搜索结果。派生构建串行预留与提交复查，清理不删除来源封面。`m1-cache-controls-second` 夹具漏账号失败，third 定向通过；全套仍须冻结源码后复验。显式手动删除默认等待7天，与质量替换24小时分开。

凭证探测加入原生命周期矩阵，现为7类采集任务×4种动作×两站；结果和后继调度均检查当前代次，网络 unknown 持久退避上限1小时。`m1-credential-lifecycle-second` 102.55秒通过。`m1-background-startup-first` 验证真实桌面后台模式双启动复用一个 daemon，显式坏 Profile 非零退出，未写入用户登录启动项。完全退出的窗口接线和其他窗口不得重新拉起后台正在补验。

Windows 通知首次失败定位为 Setting 的 HRESULT 80070490（应用尚无偏好条目），改为在此特定状态继续 Show，其他失败仍记录。禁弹窗探针第一轮因空通知组清理返回80070057；加入自有组后 `m1-windows-notification-history-second.log` 验证 API 提交、TRACE 自有 tag 的通知历史和定向清理，退出0。该证据不表示用户看见通知；无启动项开启、无账号请求。依据为 Microsoft Windows 通知与 Run/RunOnce API 文档，完整路径和实际测试按本页及环境入口追溯。

资源短测 `m1-measure-smoke-13` 为十万发布/五十万媒体元数据：热查询p95 294.3441ms、冷查询p95 220.4023ms、查询p99 321.4822ms、闲置CPU均值0.82548%、闲置私有工作集p95 48.55MiB、四录制CPU均值1.42448%、工作集p95 56.89MiB、检测到首收字节p95 0.881秒、Core重开0.9001秒。由于时长、两下载并行与GUI证据不足，退出1；smoke11/12超查询目标失败仍保留，不以本次局部性能改善覆盖旧结果。正式长测尚未执行。

用户要求尽量不影响前台，后续包与测量采用隔离 Profile、最小化窗口、独立 WebView 数据目录和回环 CDP 自动检查，不发送键鼠输入；原生IME、缩放和肉眼播放仍按 ACT-003。本阶段继续实施，完整包、正式测量、逐P/C门禁与最终提交尚未完成。

### 14.15 缓存恢复、删除边界与真实包窗口复验（2026-10-08）

播放缓存清除后不会被质量维护自动重新保留；预算为0时拒绝复用或新建派生件，原视频和音频仍保留。放宽预算后的重建使用新的repair操作，原COMMITTED历史不改写；重新验证的同blob/位置副本可由recycled/missing恢复为verified。`c08_playback_cache_clear_rebuild_budget_and_originals`实际生成音视频并经提交、mux、清理、重启与重建验证。删除恢复只恢复原件，派生件按预算生成，不重启已取消任务。活跃读取租约、手动固定、共享引用和外部原件都阻止回收；trash移动后、unlink之前明确恢复仍能救回字节。

新增schema3无残留Outbox的旧正文/来源搜索迁移、墓碑重扫及备份恢复反例。`m1-storage-boundaries-first.log`的9过1失败是夹具误用不存在的media.read；改用media.read_range后second的10项通过。`m1-playback-cache-first.log`的7项C08通过。随后`m1-rust-final1.json`完整196项、`m1-desktop-final1.json`前端41项及类型/构建、`m1-clippy-fifteenth.json`严格Clippy、`m1-governance-tenth.json`12项反例均通过且各自sourceUnchanged=true；后续窗口和脚本修改使这些收据不能作为最终指纹证据。

`m1-package-second.json`生成22文件的m1-1包，约198MiB，生产前端内嵌。`m1-package-smoke-second.log`在同步命名管道上并发阻塞读/写死锁，定位后正常关闭本次隔离daemon并结束测试器；修为PeekNamedPipe有界顺序读取，不影响产品IPC。`m1-webview-first.log`误将导航文字当路由已加载，改为等待真实页面节点及实例号；`m1-webview-second.log`继续发现产品最小化后show会恢复普通窗口。现在通过SW_SHOWMINNOACTIVE从隐藏配置一次显示为最小化且不激活，断连文案也按实际重连语义修正。

`m1-package-third.json`重新生成m1-2包；`m1-package-smoke-third.json`两项通过（12.035秒）：全文件/编译输入/两站进程故障隔离，以及真实内嵌主页、资源库、后台页、关闭后daemon继续、第二窗重连、双窗口完全退出竞争和所有窗口子进程释放。窗口经OS检查处于最小化且未成为前台；CDP只驱动专用测试进程，未发送键鼠、未读取其他浏览器。此证据不是IME/主观体验/原生媒体解码验收。正式长测和最终聚合仍在继续。

### 14.16 模块收尾、通知激活与测量器复验（2026-10-08）

模块停用的持久状态与仍在执行的工作者分开：Core按任务计数跟踪从执行到安全收尾的完整生命周期；`plugin.list`返回`active_tasks`，停用但未收尾时显示`stopping`，归零才显示已停用。模块页每秒刷新收尾状态，卸载释放计时器，已安装默认启用的模块不再错误显示启用按钮。C05两站全任务矩阵增加执行/归零断言，C08新增`c08_module_stop_shows_draining_until_workers_finish`。首轮前端夹具漏next_cursor、Clippy第十六轮暴露返回值与多余mut，均修复；第二轮42项前端验证及第十七轮严格Clippy通过。

微软[固定官方说明](https://github.com/MicrosoftDocs/windows-uwp/blob/7b637bdf12299f628e78fa7598d92c48d6a49fb3/windows-apps-src/design/shell/tiles-and-notifications/toast-desktop-apps.md)明确：无COM激活器的普通桌面通知只能使用协议激活。此前仅有历史提交证据，默认foreground点击无法工作；现为ToastGeneric protocol激活，按档案派生独立scheme，HKCU命令只包含同包桌面程序、`--show-status`与固定Profile，不拼接`%1`或来源URI。运行配置经既有锁定tempfile 3.27.0原子发布为按摘要命名的只增文件，路径转绝对；损坏/不同的已有文件拒绝覆盖，未读取或复制vault内容。新增固定命令、注入/损坏拒绝及档案区分反例。测试通知改用独立Probe应用ID、禁弹窗、自有tag定向清理；包检查直接以不激活的最小化窗口验证初始状态页，不点击系统通知。

通知首轮编译失败为当前sha2返回数组不实现LowerHex，改为逐字节十六进制编码。`m1-clippy-eighteenth`、`m1-rust-final3`（197项、205.478秒）、`m1-desktop-final4`（42项与类型/构建）、`m1-governance-final3`（12项）和`m1-fmt-final3`通过，各收据源码未变。前端final3的命令启动因Windows未写corepack.cmd失败，没有伪造成功收据，使用实际命令复验为final4。编译的Windows linker中文输出警告和增量缓存拒绝访问提示保留于原日志；编译与严格Clippy均退出0。

`m1-package-final5`生成m1-4包，22文件、约198MiB；`m1-package-smoke-final5`两项通过（12.041秒），包含新的通知初始状态页验证。许可清单重新生成：Rust539、Node168，没有升级tempfile或Windows绑定版本。该包尚须完成长测与最终聚合。

资源短测14在两次窗口/重连成功后因重复把WebView子进程加入另一个Job而发生WinError5，退出1；原日志、progress和GUI证据保留，未补造缺失的采样文件。改为每个桌面从创建时单独持有Job，采样合计Core/各窗口的累计CPU与实际PID，窗口退出后仍保留累计值。短测15完整运行，四录制和两下载加两录制、两次窗口与重连均取得证据；只因正式时长不足拒绝。其热查询p95 140.9252ms、冷查询p95 121.9561ms、idle CPU 0.203125%/私有工作集p95 49.96875MiB、四录制CPU 1.41082%/内存p95 59.29297MiB、GUI释放4.1261秒、Core重开1.0718秒。冷查询使用新的Core/SQLite连接，不清系统文件缓存；恢复前确认至少100个未完成普通任务，重开后实际IPC可访问。门禁已拒绝缺少这两项原始证明的资源结果。正式测量另开新目录，短测不作为时长通过。

原测试保留审计按main基点逐个匹配109个Rust测试，106个body的全部词法token/字面量/断言一致，无原测试迁移路径。仅3项body变化：F005/006/007从8条断言到8条，操作身份随资源/表示/摘要规则调整，原中断窗口与字节/数量不变；F015从5到6条，精确定位incremental后仍检查原(1,2,0)，并新增整个任务表只允许incremental/deep/quality三项；full_loop从45到45条，显式绑定导入凭证，日期夹具仍相差两天且在执行日前，原秘密/恢复断言保留。原A探针无差异，home既有断言未删改，仅新增资源来源定位用例；api订阅DTO等价说明见14.9。审计JSON/日志在artifacts，三个变更均经本轮完整Rust执行通过。

### 14.17 公共搜索、创作者DTO与长测前收敛（2026-10-08）

逐FR核对发现旧`search.query`没有复用已修好的时间线查询：一/二字只检查前2000候选，长词最多200行，并忽略部分创作者/平台/类型/日期筛选和游标。新增2110条记录的定向用例，先用2105条其他平台记录阻挡目标，再检查各长度查询、组合筛选、连续页和跨筛选游标拒绝。首轮`m1-search-contract-first.log`真实复现请求Bilibili却返回Douyin；复用`feed::query`与共享`SearchQueryArgs`后，第二轮继续暴露创作者aliases/tags为JSON字符串而非数组。现构造类型化`CreatorDto`，坏JSON明确失败，改名/别名/标签/备注及归档恢复不改变来源绑定、发布身份或订阅开启状态。

第三轮漏导入`OptionalExtension`导致编译失败，第四轮新用例误查不存在的subscriptions.enabled列；改为检查真实state='enabled'，保留原筛选、数组和身份断言。`m1-search-contract-fifth.json`退出0。桌面公共搜索仍兼容原query/kind/limit调用，并转发共享范围及续页请求；新增前端用例验证这一接线。SQL只截取短正文片段，避免每页把整份长文载入内存。

第一次正式测量`m1-measure-final-1`在闲置阶段因上述开发缺陷被主动中止；`m1-benchmark-final1.json`退出1、222.695秒，原采样/日志保留，未进入正式录制窗口，不将它解释为完整资源目标的实测失败，也不补造通过。修复后使用新的目录、源码指纹和m1-5包重新开始正式测量。分位数统一为nearest-rank，4次冷查询的p95取最慢值；门禁新增同一个mixed样本必须同时有至少2下载和2录制，只有分别出现的最大值不能通过。原反例继续保留。

最终长测前的`m1-rust-final4.json`通过198项，`m1-desktop-final6.json`通过43项及类型/生产构建，`m1-governance-final5.json`通过12项，`m1-fmt-final4.json`与`m1-clippy-nineteenth.json`退出0；199个源码/测试/脚本/锁文件输入与当前工作树一致。`m1-package-final6.json`生成m1-5包，22文件、207701652字节；`m1-package-smoke-final6.json`两项通过、12.193秒。包说明改为退出后台并保留/备份Profile与数据后再移除程序，避免默认数据目录随解压目录误删。测试保留审计更新为198项总数，原109项全部匹配、106项body一致，新增用例没有替代或删减原断言。完整五套件、正式长测与最终文档门禁仍须以最终交付节实际结果判断。

### 14.18 提交字节一致性与保留盘点（2026-10-08）

`m1-verify-final2.json`五套件5/5通过、251.814秒。随后提交一致性审查发现55个源码输入仍为CRLF，而`.gitattributes`规定LF；原工作树回执相互一致，但重新检出提交后的字节摘要会不同。第二次正式测量`m1-measure-final-2`因此在闲置阶段主动中止，原failed回执/采样保留，未当作完整长录制或资源达标证据。55份文本只做CRLF→LF转换，未改代码/断言内容；199个输入逐一比较Git clean-filter前后字节，全部一致，变更明细保存在`artifacts/m1-source-line-endings.json`。最终收据和包重新生成，旧回执不改绑新指纹。

本次盘点只统计明确的生成目录，不跟随符号链接或junction、不读取凭证正文：`.local`36文件/2006146字节，`artifacts`6133文件/14031627774字节，`dist`261文件/2446707805字节，前端构建26文件/841019字节；均保留。target/node_modules/生成schema按构建缓存与依赖保留，未做无意义的逐文件清理。计数是长测重启前时点，后续跑次会增加产物。原closeout-4稳定包20文件/199172866字节，原acceptance样本14文件/5259910字节，全部保留。

11份退役文档在固定基点合计111211字节；先提炼当前状态、失败守护与约束，并将全部入链改为完整SHA，再删除工作树副本。旧计划/prompt转当前唯一计划，旧交付/审查/主页过程转本报告与状态，日期化研究和ADR继续保留。`m1-doc-cleanup-first.log`已验证当时49份文档、431条本地链接与全部82需求/25AC/12INV/18ADR；最终新增摘要与文档变化仍需新的治理检查，不复用该早期结果。

### 14.19 正式负载基线复核

LF输入的`m1-rust-final5.json`198项、`m1-desktop-final7.json`43项、`m1-governance-final6.json`12项、`m1-fmt-final5.json`、`m1-clippy-twentieth.json`及`m1-verify-final3.json`五套件均通过；m1-6包与两项实际窗口/隔离烟测通过。随后核对需求5.2发现生成器的16字正文重复25次实际只有400字符，未达到约500字符基线；在下一次正式测量开始前改为512字符。没有将400字符短测或两次中止跑次升级为完整资源验收。

生成后从数据库实际检查正文最小/最大长度一致及100个普通/20个直播订阅数，ready/raw样本保留正文长度、目标数、100ms合成延迟、50行查询和实际流字节率。聚合门禁同步拒绝长度不足、目标数不足、延迟/页大小不足或流速不足；Python原用例中新增400字、99普通/19直播、99ms、49行和低于8Mbit/s的反例，原断言全部保留。代码变更限于测量器/派生/门禁与反例，产品流程未改；仍按严格全输入规则重新生成最终回执和m1-7包，旧收据不改绑。

### 14.20 必需前端用例名称预检与修复

资源基线补正后的Rust198/Vue43/Python12/包2项及五套件通过；预检255条收据时，`m1-required-preflight-first.json`发现17个C08必需名未匹配。原Vitest日志含“文件 > describe分组 > 用例名”，解析器误把分组并入name；完整日志中实际执行成功，不能据总数直接放行。新增嵌套分组/同名不同组/失败状态断言，原五条日志断言保持；`m1-vitest-receipt-first.json`退出1复现，修复后`second.json`退出0。name取叶子用例名，id仍保留文件与全部分组，避免不同用例被合并。

预检重新解析后255个完整身份无重复、98个必需名各出现一次，63条M1需求引用的用例全部存在。该检查只定位解析问题，旧执行收据不改写；最终收据必须由修复后的脚本重新实际执行生成。文档审查同时修正根入口过时阶段说明、设计中的旧分批顺序、schema3/禁回收的历史口径，以及media.read_range和timeline/详情各自页上限，未改变产品行为。

### 14.21 公开夹具路径与最终差异检查（2026-10-08）

交付前实际公开检查发现`windows_integration.rs`中新测试的合成Profile路径仍使用用户目录形式，触发personal-absolute-path；`git diff --check`另发现core Cargo.toml多余的末尾空行。原失败已以`m1-publication-final-first.json/.log`、`m1-diff-final-first.json/.log`保留。没有削弱公开检查规则或加白名单：夹具改为专用Synthetic Profiles目录，继续包含空格、中文及完整精确引用断言；Cargo仅去掉一个末尾空行。14份文档另统一LF以保证Git与文档指纹字节一致。

正式第三次测量`m1-measure-final-3`在闲置阶段被明确中止，命令回执`m1-benchmark-final3.json`退出1、738.971秒、sourceUnchanged=true；部分原始采样和中止说明保留。它不是完整录制或资源目标的实测结论。仅上述两个输入变化，新199输入集合SHA256为`aa08fb244bd9d18b667473f9f2a89e35a80e381d44e4e8d10ffadfbdbda6862b`，明细在`m1-publication-fixture-fix.json`。按严格输入一致门禁重新实际生成测试/包/资源收据，旧收据不改绑。

修正后的workspace build、198项Rust、43项Vue及类型/构建、12项Python、fmt、严格Clippy、五套件5/5、公开检查和工作树diff已通过；m1-9新包和两项实际包检查通过，正式第四次资源测量继续运行。原109项Rust保留审计另存`m1-test-preservation-final3.json/.log`，原失败和final2审计均保留。下节在新收据全部就绪后更新到这次输入和正式第四次资源跑次。

### 14.22 旧验收版本配对与包库存口径

收尾文档核对发现ACT-003仍教用户以当前工作树生成新样本，再交旧closeout-4包打开；当前生成器已随M1使用schema4，旧包仅接受schema3。现改为直接使用原已保存的acceptance Profile，保留全部六步人工行为与预期、旧包及一致备份；需要重建时由Agent从固定基点隔离准备。只读核对原一致备份meta中的schema_version为3，未启动该Profile对应的Core，也未进行数据库迁移或改写。旧包仍为20文件、199172866字节，SHA256SUMS摘要仍为`7bcf580c7585bb711834e6abe9de0b7994381a5e6979b3a6e1fc3d56b53f097b`。

新包构建输出“22文件”指SHA256SUMS覆盖的成员，不包括该清单自身；实际m1-9库存23文件、207700767字节，门禁验证全部23份。此处补正统计口径，没有更改包或采样输入。最新包内嵌状态页截图`artifacts/m1-webview-1791446572350544700/status.png`已视觉核对蓝白布局、管理入口、两站入口及后台操作；它不证明原生IME、系统缩放或媒体解码验收。

<a id="final-m1-delivery"></a>
## 15. M1 B 交付证据汇总

本节为当前交付汇总：P0～P10的B实现与自动证据已闭环，C01～C10必需用例、实际包、正式长测与阶段聚合通过，状态ready-for-review。A独立审查、真实站点验证、产品验收均not-run。§13保留A规划时事实，§14保留实施各时点与失败复验，不能混用不同源码指纹。经审阅的[公开机器摘要](../evidence/gate/m1-self-check.json)包含逐输入/用例/包成员摘要及实际资源值；原始日志和完整bundle保留于本地artifacts。

### 15.1 P0～P10 对应关系

| 工作包 | 当前实现与交付物 | 自检与边界 |
| --- | --- | --- |
| P0 | 原阶段11份过程文档提炼及固定SHA入链、唯一计划/报告、原测试及稳定包/Profile保留 | 14.16/14.18逐测试与目录审计；最终文档/公开/索引检查另见15.3 |
| P1 | 两站每类入口/参数/身份/分页/认证/媒体关系的固定协议来源，独立合成响应、WBI/A-Bogus向量与受控site-check | [解析研究](../research/site-parsing-current.md)、两站能力表；合成/资料证据，真实请求not-run |
| P2 | contracts/domain类型化正文/附件/安全元数据、统计观察与正文版本分离、schema3→4迁移/旧协议默认及分块读取 | C02/C06/C07；旧完整正文不被摘要覆盖，旧库无Outbox也重建索引 |
| P3 | Bilibili账号/动态/转发/opus/HTML和JSON ops专栏/文集/视频多P/字幕/音乐卡及直播生产适配 | C01/C02/C03生产进程与Core自动链；真实账户权限与返回差异未测 |
| P4 | Douyin账号/短链/投稿、图集/实况/配乐、type163详情全文/30图、视频/合集及直播生产适配 | 生产进程调用真实映射逻辑，C01/C02/C06；签名向量不是风控通过证明 |
| P5 | 持久直播场次、FLV/HLS/独立音轨、MAP/KEY/BYTERANGE、刷新/重连/缺口、封片及附属字幕/有限弹幕 | C04/C05/C06/C07；普通AES-128受控解密，DRM/未知加密明确受限；弹幕不是无缺口全集 |
| P6 | 历史/增量/深查独立游标、持久范围/质量/周期、优先级/预算、补查/批量重试、全任务生命周期 | C01/C05；两站7类采集任务×4类控制动作，另含本地维护与跨窗口退出 |
| P7 | 蓝白主页、管理入口、创作者编辑/归档、惰性全文、版本/资源/录制/质量、资源库来源定位、共享搜索分页 | C08与原home事件断言；实际包内嵌页通过，原生IME/缩放/解码仍依ACT-003 |
| P8 | 实际质量探测/严格与宽松策略、引用保护和安全回收、删除预览/墓碑/恢复、备份/导出及新Profile恢复 | C07/C08；共享/固定/独有区间保护，恢复无凭证继承、目标外零写入 |
| P9 | 82条需求映射（63条M1适用）、98个具名必需用例、源码/构建/包/采样/文档拒绝门禁、生产两站便携包 | passed；C09/C10、936份原始连续样本及完整聚合通过；所有63条M1行的自动证据已对应 |
| P10 | 同一报告同步自审、失败复验、限制、单阶段DCO提交及B→A审查prompt | passed；交付SHA由Git和交付消息提供，A待审；无push/PR/合并/发布或自动消息 |

上述P0～P10的B自动自检均passed，第三列保留各自的现场验证限制。需求定义保持在[需求正本](../requirements.md)，[机器映射](requirements-map.json)逐行记录实现路径、P/C、用例与证据；19条后继范围保持not-run，不据M1中的基础复用声称M2/M3已完成。

### 15.2 C01～C10 定向证据

| 组 / 必需用例数 | 本轮核对的失败边界与实际用例示例 | 证据入口 |
| --- | --- | --- |
| C01 / 10 | 身份别名/转发/跨类型同号、置顶乱序、空页has_more/重复游标、独立合集游标、历史范围与账号资料执行权；`c01_identity_alias_repost_type_collision_and_persistent_cursor_cycle` | Rust全套、生产适配器进程及site-check反例；全部具名要求以stage_evidence.py为准 |
| C02 / 10 | 超摘要全文、type163详情对象/JSON字符串、30图/实况/配乐、专栏ops/av→BV、列表/表格内图片、统计与正文版本；`c01_c02_douyin_article_account_to_full_document_through_production_process` | Rust全套、类型化文档前端用例；必要资源缺失不得完整 |
| C03 / 5 | 原11种HTTP长度/状态组合、逐段及整组摘要、强ETag/Range、不预期2xx、30秒以上流及取消；原F-007/008和A探针 | Rust全套closeout_faults/a_review_probes/closeout_rework；原断言及文件位置保留 |
| C04 / 10 | FLV时钟/编码/休眠/磁盘，HLS真实TS/独立音轨/NONE/MAP/范围/序列滑窗，4个提交中断窗口、同场/新场次、严格质量缺口 | Rust录制与恢复矩阵；原件/封片哈希、缺口和手动停止均有断言 |
| C05 / 11 | 暂停立即恢复、旧租约/代次、停模块/退出/取消/迟到事件，半帧读不能被代理完成取消，通知固定Profile激活与后台双启动 | Rust全任务矩阵、Windows禁弹窗历史探针；原生通知可见/点击不由此代填 |
| C06 / 8 | 初始/重定向/DNS/IP及嵌套HLS私网、跨域头、秘密递归净化、错误与导出、凭证重绑/能力unknown/合并告警 | Rust秘密哨兵、ConstrainedClient/PluginHost与本地FFmpeg拒远程输入反例 |
| C07 / 9 | schema3一致备份/迁移回滚/来源搜索重建、真实规格与严格候选、共享/pin/独有区间、回收窗口、墓碑重扫与备份 | Rust迁移/恢复/删除/质量与导出用例；原F-002～009同步复验 |
| C08 / 30 | 两站全类型组合筛选/统计/分页、1/2/长词、2110候选不截断、慢返回/事件竞态、原媒体节点/播放位置、资源库/缓存/收尾显示 | Rust查询/IPC与Vue用例；前端原32项全部保留，其中home原17项加1项来源定位，当前前端共43项 |
| C09 / 2 | 包全文件及生产两站注册/进程隔离，实际内嵌WebView关闭/重连/多窗完全退出；长测同时2下载+2录制及4路录制 | package smoke两项与正式resource-samples；资源目标仅以完整长测为准 |
| C10 / 3 | 旧指纹、缺/失败/重复用例、漏包/篡改manifest与构建日志、失败/短基准、非同时并行样本、过期文档必须拒绝 | Python治理与stage_evidence反例；遍历/绝对/链接路径等另外计入12项Python总数 |

必需数合计98，完整产品/治理/包用例总数与收据见下一节。表中C编号是当前M1语义组；旧五套件内部保留的历史C编号不用于替换本表。

### 15.3 最终命令与字节指纹

当前LF源码/测试/脚本/锁文件共199个输入，集合SHA256为`aa08fb244bd9d18b667473f9f2a89e35a80e381d44e4e8d10ffadfbdbda6862b`。算法为按路径排序的`sourceInputs` JSON，使用紧凑分隔符后取SHA256；逐文件摘要与构建收据一致。A规划基点为`dcbdf82cd74e38e6c09330789490ed96d7169c5e`，父基点main/origin/main为`960984c34487244f31f1238dffc2bd9faaa292e7`。最终Git SHA仅从实际提交后聊天交接取得，不递归写入报告本身。

以下命令均由`stage_evidence.py run`实际执行；路径位于artifacts，保存完整输出、退出码、运行前后输入和逐用例。全部最终回执exitCode=0、sourceUnchanged=true，输入与本节一致；通过只覆盖各自范围。

| 实际命令 | 最终回执 | 结果 / 秒 |
| --- | --- | --- |
| `cargo build --workspace --locked` | `m1-workspace-build-final8.json` | passed / 7.993 |
| `cargo test --workspace --locked` | `m1-rust-final8.json` | 198 passed / 189.447 |
| `corepack.cmd pnpm -C apps/desktop run verify` | `m1-desktop-final10.json` | typecheck、43 Vitest、build passed / 7.067 |
| `python -m unittest discover -s scripts/tests -v` | `m1-governance-final9.json` | 12 passed / 5.380 |
| `cargo fmt --all -- --check` | `m1-fmt-final8.json` | passed / 0.556 |
| `cargo clippy --workspace --locked --all-targets -- -D warnings` | `m1-clippy-twentythird.json` | passed / 1.823 |
| `python scripts/package_windows.py` | `m1-package-final10.json` | passed / 69.270 |
| `python scripts/check_package.py dist/trace-0.1.0-windows-x64-m1-9` | `m1-package-smoke-final10.json` | 2 passed / 12.162 |
| `python scripts/verify.py --all` | `m1-verify-final6.json` | 五套件5/5 passed / 250.059 |
| `python scripts/measure_m1.py artifacts/m1-measure-final-4 --idle 1030 --seconds 3620 --entries 100000 --package dist/trace-0.1.0-windows-x64-m1-9` | `m1-benchmark-final4.json` | 正式长测passed / 4704.616 |

`m1-required-preflight-final2.json`核对255项完整用例身份不重复、98个必需名各一次、63条M1映射用例/实现路径无缺项及实际包校验。它不代替正式资源门禁。原测试审计`m1-test-preservation-final3.json`绑定本节源码，原109项保留，三个等价调整说明见14.16；没有迁移或删除测试。

最终文档、公开内容和工作树diff检查回执分别为`artifacts/m1-report-final.json`、`m1-publication-final.json`、`m1-diff-final.json`，实际命令分别为`python scripts/check_docs.py`、`python scripts/check_publication.py`、`git diff --check`。先完成所有文档与公开摘要，再生成`artifacts/m1-final-bundle.json`；`python scripts/stage_evidence.py check artifacts/m1-final-bundle.json`和`python scripts/verify.py --stage-bundle artifacts/m1-final-bundle.json`重新读取并验证完整证据，输出保留于`artifacts/m1-final-gate.log`。bundle不放入docs，避免文档摘要自引用；最终提交还须核对暂存diff与HEAD逐文件字节，结果保留于本地交付审计。

### 15.4 本地包与正式测量

最终本地包`dist/trace-0.1.0-windows-x64-m1-9`，23文件、207700767字节（198.08MiB）；`SHA256SUMS.txt`自身SHA256为`6a0f0ccd114a0f8581f26963718268147df73134a4ae27ecef32148f645c105a`。manifest与构建日志记录本节199个输入及4个生产程序，清单覆盖其余22个包成员，实际库存另计清单自身为23文件。构建器输出的“22文件”不含清单自身，最终门禁按完整库存核对。构建时Git为A规划HEAD和dirty工作树，交付提交通过逐输入字节对应；不改写构建现场。桌面SHA256为`186c8ae3d7e8a847b24ecdc76b8a171fb1ec9754405d2f49149b07d54a435549`；正式测量必须使用这份程序，不能借用旧debug桌面。锁定媒体、两站许可材料随包，许可台账列Rust539/Node168；本次最终重包未再升级依赖。

测量设备：Windows11专业工作站版10.0.26300，Intel Core i5-14600K，14核/20逻辑处理器，68422864896字节物理内存，固定ReFS卷；未据卷类型声称底层SSD特性。WebView2为154.0.4258.62。CPU按20个逻辑处理器的整机时间归一，内存为所有被测进程私有工作集之和。Core/受控网络/调度/对象/IPC使用继承release优化的measure构建，仅开启合成网络所需debug_assertions；上游适配器和HTTP为合成源，GUI为上述实际便携包。

正式命令：`python scripts/stage_evidence.py run --kind bench --id m1-benchmark --output artifacts/m1-benchmark-final4.json -- python scripts/measure_m1.py artifacts/m1-measure-final-4 --idle 1030 --seconds 3620 --entries 100000 --package dist/trace-0.1.0-windows-x64-m1-9`。实际命令耗时4704.616秒，退出0；936个原始进程样本覆盖4676.921秒，最长采样间隔5.016秒。有效闲置窗口905.391秒，持续录制窗口3625.930秒；四路录制、两路下载加两路录制均观测到，mixed同时并行样本360个，任务错误0。

| 资源/响应指标 | 实测 | 原目标与判断 |
| --- | --- | --- |
| 数据与负载 | 100000发布、500000媒体元数据，正文512字符，100普通/20直播目标，100ms合成HTTP延迟，50行查询，每路1112866 B/s | 全部达到固定基线，未缩小输入 |
| 闲置CPU均值 / 私有工作集p95 | 0.31204% / 50.6758MiB | ≤1% / ≤150MiB，passed |
| 四路录制CPU均值 / 私有工作集p95 | 1.50984% / 65.6602MiB | ≤5% / ≤300MiB，passed |
| 热查询p95 / 冷连接查询p95 | 164.4213ms / 134.0365ms | ≤500ms / ≤1500ms，passed |
| 全窗口查询p99 | 176.6291ms | 原始样本派生，不用p95替代尾部观测 |
| 可观测开播→持久检测p95 | 24.0114秒 | 每路≤1.2×实际轮询周期+10秒，逐原始时间戳passed |
| 检测→首次接收p95 | 0.8070秒 | ≤10秒，passed |
| GUI关闭释放 / Core完整重开 | 7.6551秒 / 8.1659秒 | ≤10秒 / ≤30秒，passed |
| 全窗口CPU均值 / 采样区间峰值 | 1.19356% / 4.984375% | 全部Windows Job累计CPU，含已退出子进程 |
| 私有工作集总和峰值 / 句柄总和峰值 | 193503232字节（184.5391MiB） / 3880 | 包含两次实际桌面/WebView；均为5秒采样所见峰值 |
| 工作集总和峰值 / 累计传输字节计数 | 536743936字节 / 15891788013字节 | 工作集可能重复计算共享页；传输计数不等于去重后档案大小 |
| 峰队列 / 卷报告最小可用空间 | 226 / 69814697857024字节 | 合成队列与卷报告值，不外推物理盘容量或真实档案 |

Core重开前有202个未完成普通任务，重开后实际IPC可访问，metadataEntries仍为100000；两次GUI探针均验证内嵌页、生命周期控件、schema4及相同后台instanceId。原始样本`artifacts/m1-measure-final-4/resource-samples.json`为1605579字节，SHA256 `73a32a4254e23e7fce166db2c720650d307092290f2a6614c7d537da395ea0da`；测量程序与adapter二进制摘要同时保存在原始build回执中。各进程名称的观测峰值、采样口径和完整计数见公开机器摘要，原始PID不复制进公开内容。

测量每5秒保存连续原始进程/累计CPU/内存/句柄/磁盘/队列/吞吐/查询样本；Windows Job保留已退出工作者CPU。GUI使用新Profile、独立WebView目录、最小化不激活及回环CDP，不发键鼠；冷查询用新Core/SQLite连接，不清系统文件缓存；恢复计时为完整Core重开，崩溃正确性另由故障矩阵证明。合成源与Rust负载驱动器成本计入，外部Python采样器与CDP测试驱动器属于测量工具。采样峰值不是采样间隔内无法观测到的瞬时峰值，结果不外推真实账号档案。

### 15.5 失败保留与最终复验对应

下表是§14原始过程的索引，原日志均留在artifacts；没有把历史失败改绑最终源码或改写为当时通过。最终用例执行及输入均由15.3收据证明，资源跑次另按15.4判断。

| 原失败或证据缺项 | 处理与保留的断言 | 最终复验 |
| --- | --- | --- |
| `m1-desktop-first.log`原home三项失败；resources初轮又触发旧平台入口断言 | 恢复detail-description及真实平台入口，原home事件/竞态/媒体节点断言保留；增加来源定位 | `m1-desktop-final10.json`，43项通过，home原17项全部仍在 |
| full-loop第一/二轮日期范围及凭证哨兵失败 | 仅平移两天间隔的日期，并显式绑定导入凭证；45条原断言保留 | `offline_full_loop_scan_observe_media_search_backup_restore`，Rust最终通过 |
| `m1-closeout-faults-regression-first.log`及后续操作窗口失败；workspace第四轮F-015计数失败 | 获取身份纳入表示/规格/摘要，退出不破坏durable操作；F-015仍精确检查(1,2,0)，新增全任务清单断言 | `f005_f006_f007_task_windows_restart_pause_shutdown_and_cancel`及`f015_pause_due_running_resume_idempotency_and_restart`，Rust最终通过；逐测试审计保留 |
| `m1-host-framing-tests-first.log`半帧被代理完成取消 | 每帧保持同一读取future；保留先6字节再补余帧的大中文并发用例 | `c05_proxy_completion_cannot_cancel_partial_frame_read`和原F-013探针，Rust最终通过 |
| `m1-lifecycle-matrix-second.log`停插件意外增加失败次数 | 先事务撤租约再关闭进程，迟到结果不能写入或消耗失败预算；7类采集任务×4动作×两站 | `c05_all_job_types_plugin_isolation_and_shutdown_sealing`，Rust最终通过；本地维护任务另有矩阵 |
| 首包未启用内嵌前端；包烟测第二轮管道死锁；WebView第二轮意外恢复普通窗口 | 构建显式custom-protocol；探针有界读取；桌面SW_SHOWMINNOACTIVE，验证最小化且非前台 | `m1-package-smoke-final10.json`两项通过；页面、关闭/重连、多窗完全退出及两站隔离有实际包证据 |
| 缓存重建/删除恢复及旧库来源搜索缺口 | 原件与派生件分开，重建保留操作历史，墓碑及共享/固定/读取租约保护；旧schema3无Outbox仍重建索引 | `c08_playback_cache_clear_rebuild_budget_and_originals`及C07迁移/删除恢复组，Rust最终通过 |
| `m1-search-contract-first.log`跨平台返回；第二轮aliases/tags类型错误 | 公共search复用时间线全范围查询，2110候选分页；CreatorDto数组及身份/订阅不变断言 | `c01_c08_public_search_pages_full_scope_and_creator_edits_keep_identity`与前端共享范围转发用例，最终通过 |
| 最终公开检查夹具路径与Cargo EOF空行失败 | 仅改合成目录位置，保留空格/中文及完整精确引用断言；移除空行，不弱化规则 | `m1-publication-final-second.json`与`m1-diff-final-second.json`通过；新指纹全套与新包重新实跑，第三次中止测量保留 |
| 早期短测查询超过500ms、并行不足、plugin_crash；smoke14采样器Job冲突 | 查询/调度/帧处理及每窗独立Job分别修复；短测和变动源码继续拒绝；smoke15只证明短窗口 | 正式时长、同样本2下载+2录制、四录制及目标值必须由15.4完整跑次和聚合门禁证明 |
| 正式final1发现公共搜索缺陷、final2发现Git换行摘要不一致、final3发现公开夹具路径而中止 | 三次failed回执、部分样本及中止原因保留；55份输入仅CRLF→LF，逐项核对Git过滤前后字节 | 最终199输入与包、各命令一致；完整长测使用final4新目录，不将中止解释为完整资源达标 |
| 正文负载实际400字；`m1-required-preflight-first.json`缺17个前端必需名 | 负载改512字且核对数据库实际值；Vitest name取叶子、id保留文件/分组，原断言加反例 | `m1-governance-final9.json`12项通过；最终预检255身份/98必需/63条M1映射无缺项 |

早期编译、类型及严格Clippy失败和各次修复详见§14，不因最终全套通过省略。所有原A Rust probes仍在原文件，测试未迁移；三个有语义调整的旧Rust测试按14.16逐项证明等价，不能仅以总数上升替代该证明。

### 15.6 交付适用范围与待审事项

- 本阶段自动证据只对应15.3的源码输入和15.4的Windows x64便携包。真实两站登录、权限、风控、账号内容完整性及媒体下载/直播仍not-run，真实条件及有界采样方案只维护于[ACT-002](../../USER-ACTIONS.md#act-002)。B没有读取其他浏览器登录态；代码和自动用例缺口不转为人工待办。
- 抖音长图文已按type163详情完整响应实现并通过生产进程合成链，列表摘要不能完成归档。真实站点字段变化、客户端差异和私有内容可访问性未经现场验证。独立付费专栏目录缺资料与已实现长图文分开，不据此略过长图文。
- 弹幕为用户可选的有限历史采样，保存范围、去重和缺口；不是WebSocket消息全集。外部字幕依来源提供的HLS轨捕获，来源无轨或附属失败独立明示。DRM及未知加密不冒充已解密，必要资源不足不得显示完整。
- 实际包的内嵌页、最小化、不激活、关闭/重连/完全退出已有自动证据；原生输入法、系统缩放、通知肉眼可见/点击、音视频真实解码体验仍按[ACT-003](../../USER-ACTIONS.md#act-003)择时检查，旧closeout-4包及其验收Profile保留。OS休眠用例为合成时钟跳变；系统休眠期间的真实媒体连续性未实测。
- 资源结论是本设备及合成规模的样本，不代表真实账号十万条档案；5秒采样峰值、未清OS缓存的冷连接及完整Core重开各有明确口径。安装器、其他系统、软件签名、正式分发未验证；许可决定仍见Q-004。
- OSS-007～010保持B已实现/自检、待A核实技术关闭，状态只维护于[事项总表](../decisions/open-questions.md)。A审查not-run，产品验收not-run。没有新增必须实现缺口被延期为LOOP；外部动作按现有具体授权边界执行。

### 15.7 B→A 集中审查 prompt

以下供用户手动转交。B自动门禁已通过；最终完整SHA附在交付消息中，报告不递归写入自身SHA，A须核对实际HEAD及本节指纹。审查记录与局部修复继续归并同一阶段提交。

```text
请作为 Agent A，在 trace 工作区的 codex/m1-two-site-archive 分支，对唯一计划 docs/delivery/stage-plan.md 的 TRACE-M1-01 r1 做最终集中审查。先核对 HEAD 与B交付消息中的完整SHA、工作区及用户后续修改。main基点为960984c34487244f31f1238dffc2bd9faaa292e7，A规划提交为dcbdf82cd74e38e6c09330789490ed96d7169c5e；本阶段仅一个未推送提交，A规划与B实施已归并。

按AGENTS.md读取索引、状态、有效决定、事项、USER-ACTIONS及适用规则，重点读取同一报告docs/delivery/stage-report.md第13～15节、docs/research/site-parsing-current.md、两站能力表、docs/site-archive-contracts.md和requirements-map.json。不要创建竞争计划或仅凭聊天“通过”作结论。

B交付覆盖P0～P10：两站动态/图文/文章/视频/直播生产适配，抖音type163必须从详情取得对象或JSON字符串形式全文，30图/配乐/实况等必要资源，历史/增量/编辑和安全元数据，离线浏览搜索，质量保留/安全回收，旧库迁移/备份恢复，后台生命周期、包和正式资源测量。生产路径不能用fake adapter替代；合成来源只是隔离协议测试和资源负载。

按C01～C10核对实际代码与故障断言，尤其正文完整性/资源组、类型身份/分页、FLV/HLS与四个提交窗口、全任务暂停恢复/停模块/取消/退出竞争、网络和秘密隔离、旧库及质量回收、UI事件/竞态/媒体节点，以及源码/用例/包/采样/文档缺项拒绝。逐P/C映射及失败复验在报告15.1/15.2/15.5，全部82条需求中63条M1适用，19条后继范围仍not-run。

最终199个源码/测试/脚本/锁文件输入的SHA256为aa08fb244bd9d18b667473f9f2a89e35a80e381d44e4e8d10ffadfbdbda6862b。最终收据：artifacts/m1-rust-final8.json（198项）、m1-desktop-final10.json（43项和类型/构建）、m1-governance-final9.json（12项）、m1-package-smoke-final10.json（2项），合计255个完整身份、98个必需用例；五套件收据m1-verify-final6.json为5/5。fmt、严格Clippy、构建、治理及完整输出位置均在报告。先复用仍匹配的有效证据；有变更、失败或未解决疑虑才复跑对应范围。

实际包为dist/trace-0.1.0-windows-x64-m1-9，23文件、207700767字节；SHA256SUMS.txt自身摘要6a0f0ccd114a0f8581f26963718268147df73134a4ae27ecef32148f645c105a。正式原始样本及结果在artifacts/m1-measure-final-4/，命令回执artifacts/m1-benchmark-final4.json，最终bundle为artifacts/m1-final-bundle.json。先执行python scripts/stage_evidence.py check artifacts/m1-final-bundle.json及python scripts/verify.py --stage-bundle artifacts/m1-final-bundle.json，核对实际包库存、原始采样派生值与全源码指纹；不能以短测/中止跑次或测试总数代替语义审查。

保留F-001～017、原A Rust probes、B closeout_rework、原home事件断言、历史失败、蓝白主页和管理入口。测试审计artifacts/m1-test-preservation-final3.json：原109项Rust全部保留，106项body词法一致，3项等价调整详见14.16；没有迁移/删除/跳过旧测试。11份旧过程材料已先提炼、改固定完整SHA入链再退役，抽查提炼和历史链接；保留dist/trace-0.1.0-windows-x64-closeout-4及artifacts/closeout/acceptance，不清理用户资料或旧失败。

真实账号请求仅按USER-ACTIONS ACT-002的具体授权执行；当前真实站点和原生产品验收not-run。登录/权限/风控等真实条件缺失由A按该入口后续处理，不要求B重复补已完成的自动实现。测试尽可能使用隔离Profile、独立WebView、回环CDP与最小化不激活窗口，不发送前台键鼠、不读取用户浏览器。有限弹幕、外部字幕和资源测量口径按报告15.6及能力表审查。

A直接修复明确局部问题并做定向复验，将审查记录与修复amend进同一阶段提交并DCO签名；不改main或备份历史。只有较大耦合实现缺口才一次性给全部稳定F编号、复现、依据和验收的集中返工prompt。未经用户明确允许，不push、创建PR、合并、发布、启动子Agent或自动跨聊天发送。

审查通过后，更新同一报告、状态和事项，分别说明B实现、自检、A技术审查、产品验收和发布结论，直接向用户收尾并给下一步；不再交B重复复审。最终SHA由实际Git读取并提供给用户。
```

<a id="a-m1-review"></a>
## 16. M1 A 集中审查与同计划修订（2026-10-08）

**当前结论：M1 / rework-required，A技术复核failed。** 原B r2自检和正式资源证据按对应输入保留；F-018～022原范围已核实，A本次修复F-026，F-023～025/F-027～029仍须集中实现。DEC-028已纳入同一计划r3的P14/C14；已取得四份真实媒体诊断样本，但尚无TRACE链接到Core完整归档/录制的产品链证据。F当前正本为§16.3，最新诊断与交接见§16.16/16.17。§13～15及本节各次历史失败、原包/长测保持当时事实；新增链接交互不追溯算作B违反r1/r2。

### 16.1 被审版本与证据核对

- 被审 B：`772ab9a6c523a22385f024abafa66aef6ed7f0f1`；main/origin/main：`960984c34487244f31f1238dffc2bd9faaa292e7`；原 A 规划：`dcbdf82cd74e38e6c09330789490ed96d7169c5e`。开始时为 `codex/m1-two-site-archive`、干净工作区，相对 main 一提交；A 改动归本次审查，用户后来提供的本地凭据保持忽略且不入提交。
- 修改前实际执行 `python scripts/stage_evidence.py check artifacts/m1-final-bundle.json`、`python scripts/verify.py --stage-bundle artifacts/m1-final-bundle.json`，均退出0。逐一核对199项输入的磁盘字节、Git HEAD字节、原收据与包manifest，零差异；原集合摘要仍为 `aa08fb244bd9d18b667473f9f2a89e35a80e381d44e4e8d10ffadfbdbda6862b`。
- 独立核对 m1-9 库存23文件、207700767字节和全部成员摘要；SHA256SUMS自身仍为 `6a0f0ccd114a0f8581f26963718268147df73134a4ae27ecef32148f645c105a`。复用原两项实际包烟测，本次没有把包重新构建或替换。
- 独立读取936个原始样本并复算录制3625.9299439秒、闲置905.391秒、同时2下载+2录制360样本、热查询p95 164.4213ms、闲置CPU均值0.3120422537%、四录制CPU均值1.5098369582%、闲置私有工作集p95 50.67578125MiB、任务错误0；采样连续，GUI两次连接为同一后台，重开前202个未完成普通任务，重开后IPC可达。没有用短测或中止跑次替代。原 `target/measure` 二进制摘要已核对，未重建它们。
- 只读 `git ls-remote --heads origin` 仅见上述 main；GitHub ruleset 24632608 为 active，无bypass，PR/squash/讨论解决/线性历史/禁删除与强推/required repository-quality 均在。未改变远端配置、main或备份历史，未push/创建PR/合并/发布。

独立原始审计在 `artifacts/a-m1-review/baseline-audit.json`、`independent-metrics-retirement.json`。A 当前源码/测试/脚本输入为201项，摘要 `32842821b669bddcf068d7046a0ac4f02fbdafb9d2a4f5eca3098438829eb55d`；该摘要不是新包或长测通过证明。当前摘要、定向结果与限制的公开索引见[审查机器摘要](../evidence/gate/m1-a-review.json)。

提交前归属、原测试、报告§13～15、measure二进制和保留包核对保存于 `precommit-audit.json`；本次明确归属的39个公开文件按名单暂存。最终文档/工作树与暂存公开检查、DCO、相对main的一提交、保护引用和提交字节核对由 `git-delivery.json`承接，最终SHA从实际Git读取后提供，不递归写回报告。该本地审查提交保留F-019失败，不表示阶段门禁已通过。

### 16.2 逐 C 语义审查

| 组 / P 映射 | A 实际核对内容及证据方式 | 判断与边界 |
| --- | --- | --- |
| C01 / P1/P3/P4/P6 | 读取两站枚举/身份映射、游标、`process_scan_page`及具名反例；生产子进程测试使用真实适配器执行文件、仅在proxy边界供合成响应。另新增重叠订阅反例 | 原身份/分页证据可用；F-019 使多订阅持续观察失败，不能整体标通过；真实一页列表也不证明全部内容 |
| C02 / P2/P3/P4 | 核对 type163 详情而非desc、对象/JSON字符串、30图/配乐/实况；B站HTML/ops/av→BV/opus、必要音乐与试听、正文和统计分离；读取真实路由及生产子进程断言并复跑9项 | 原映射和合成故障范围通过，完整真实文章/视频仍未验；没有由fake adapter替代生产接线 |
| C03 / P3/P4/P8 | 追踪 MediaCandidate/UrlSet→下载→逐片与整组校验→引用提交；复跑原 closeout_rework 的11种HTTP组合与8种分段场景；补源data_size传播反例 | F-018 原失败后 A 已修并核实；原包没有修复。保留强ETag、长于30秒流与完整哈希旧断言，不用已收字节的哈希冒充来源完整 |
| C04 / P5 | 读取 FLV/HLS/MAP/KEY/BYTERANGE/独立音轨/封片恢复与缺口；定向复跑四提交窗口，核对真实TS合成样本及原片哈希断言 | 被测合成窗口通过；休眠为合成时钟，真实媒体/当前直播分辨率与码率未测 |
| C05 / P6 | 核对 claim/check、暂停/恢复、停模块、取消/退出与记录收尾；原单订阅恢复断言重跑通过，新增两订阅共享observe失败 | F-019 未解决，不能以原7类任务矩阵通过覆盖该遗漏；修复需同时审 media 的旧payload和租约 |
| C06 / P1/P2/P5 | 读取 ConstrainedClient逐跳DNS/IP、凭证域、嵌套HLS/key/MAP、本地媒体输入及安全metadata；定向重跑嵌套网络反例；真实登录探测发现 F-020 | 原网络/秘密反例通过；F-020 A 已修并用合成与获准真实登录验证。无浏览器登录库读取，无凭据/账号正文入公开证据 |
| C07 / P2/P8 | 核对 schema3→4快照/事务回滚/旧程序拒写、恢复目标验证及可用Profile、质量比较/共享与pin/独有区间、墓碑与回收；复用对应原有效回执 | 原 r1 恢复与保留边界未发现新增阻断；r2 新获取记录仍需新迁移和恢复证据，不以旧schema测试代填 |
| C08 / P7 | 读取HomeView、MediaBrowser、ArchiveDocument、ResourcesView与共享查询；核对原home事件、慢列表/详情、排序移窗、媒体节点/currentTime、中文分页及统计断言 | 前端未被A改动，复用原43项及类型/构建证据；原生IME/缩放/真实解码仍not-run，新认证筛选和统一确认页尚未实现 |
| C09 / P9 | 核对完整输入、实际包库存、原始采样派生值及measure二进制，区分生产包GUI和合成负载/适配器 | 原B版本的正式证据有效；当前A改动后的包/正式长测not-run，不改绑原结果 |
| C10 / P0/P9/P10 | 读取聚合的输入/用例/日志/包/采样/文档拒绝条件及反例；修改前复跑两入口；修改后再检查陈旧拒绝 | 原r1门禁有效，当前输入须拒绝原bundle。r2新C组/计划/schema/用例须由P13接入；脚本不得自动给出A或产品验收通过 |

逐需求映射保留 B r1 的自检证据，并单列 A 的审查范围。新增3条需求后共85条，66条M1适用、19条后继；这不改变原B“82条/63适用/255用例/98必需”的历史计数。

### 16.3 当前 F 正本

原 F-001～017 保持固定基线的状态与故障断言。本次编号续用，不复用旧编号，不另建F状态清单。下列触发、旧位置与原反例保留A当时观测；B本轮修复及复验在[§17](#b-m1-r3-delivery)，本节标题维护当前F状态。已修待A复核不等于AC-29/30或阶段通过。 2026-10-11的A复核证据见[§18](#a-m1-r3-review)；后文旧失败与当时“待A复核”的措辞保持历史语境。当前关闭覆盖已核实的代码、自动反例、正式资源及§18.8四项获准真实链；AC-29/30技术证据passed，原生产品验收not-run。

<a id="f-018"></a>
#### F-018 / P1：抖音来源声明长度未进入传输约束 — A已核实关闭（当前源码范围）

触发：详情 `video.play_addr.data_size=4096`，URL合法且有可解析视频。原 `detail_for` 调用 common `candidate` 后 `expected_size=None`，单段同样缺少来源长度；在HTTP未提供可用总长时，Core失去该来源约束，不能靠对已收字节计算哈希证明完整。依据 r1 C03、FR-ARC、必要资源完整性契约。

位置：`adapters/douyin/src/protocol.rs::detail_for`、`sized_candidate`；原 common `candidate`不持有源data_size。A增加 `declared_douyin_media_length_reaches_candidate`，修复前返回None而非Some(4096)，实际失败在 `length-failed.log`。修复将正数大小传到整候选与单UrlSet，覆盖选定码率视频、静态图、实况、音乐和封面；0/缺失保持unknown，负数/字符串/溢出明确拒绝，没有把总量平分给分段。

实际复验：抖音11/11通过，包含不同角色与不同大小、候选选择、非法声明；原HTTP/分段反例通过。`douyin-final2.json/.log`是当前定向证据。原包、原长测不含本修复，P13须重新交付对应产物。

<a id="f-019"></a>
#### F-019 / P1：重叠订阅的共享任务被首个订阅永久占有 — A已核实关闭（原故障范围）

2026-10-09 A核对消费者发现/请求/认领/失败预算及Core两条链，独立执行原复现1/1和C11的19/19，包括首owner四种状态、控制/凭证/重启和两站生产适配器字节链。证据为 `artifacts/a-m1-r2-review/f019-original.json/.log`、`c11-directed.json/.log`。本项原阻断已解除；完成后的候选寿命与跨任务物化归属另按F-023/F-025处理，不能用本项关闭推定整个M1通过。下文保留原发现及B交付时的待复核事实。

触发步骤（隔离合成Profile、零网络）：同一个Douyin账号有dynamic和video两订阅；dynamic先发现视频71并排入observe；暂停dynamic；仍启用的video再次发现同一条视频。预期video可以继续详情与必要资源获取，暂停的旧租约不能写入。实际唯一observe仍是 `state=paused`、`payload.subscription_id=first`，没有可认领的第二订阅工作。已成功的同键任务重排也保留旧payload；media的同类upsert同样需要修复。

位置：`crates/core/src/core.rs::process_scan_page` 的 `observe:{ns}:{kind}:{key}` upsert（只更新succeeded且不改payload）、`persist_detail` 的 `media:{rid}:{group}`；`crates/core/src/jobs/mod.rs::set_subscription_enabled/claim_due/ClaimedJob::check` 按payload订阅判断准入；请求凭证在 `Core::execute` 中按该订阅取得。依据 FR-SUB-001/003/004、r1 C01/C05 及多类型订阅独立推进契约。

可复制复现：`cargo test -p trace-core --locked overlapping_subscription_can_continue_same_entry_after_owner_pause -- --nocapture`。A原始 `subscription-failed.log`、中间 `subscription-final.json/.log` 与当前 `subscription-final2.json/.log` 均保留；最终子命令退出101，1项failed、0 ignored，实际错误明确包含paused和first。该反例未删除、skip或减弱，A交接时全套不能声称绿色。B本轮原样复现失败后完成修复；定向反例通过，共享消费者矩阵与两站生产服务链通过，当前整套证据和修复位置见§16.8以后。A尚未复核本次修复。

验收必须同时覆盖：

1. 同一内容经两个订阅到达，原owner为queued/running/paused/succeeded时，活跃消费者都能继续；不重复entry/媒体组，不依赖重新恢复已暂停订阅。
2. observe和media两条链都有合法消费者/执行作用域；不同凭证与质量策略不串用，不能仅把paused改queued或盲目覆盖payload。
3. 暂停、取消、停模块、删除、立即恢复、重启与迟到页/媒体并发，旧租约/代次均拒绝，显式取消不复活，失败预算不被控制动作消耗。
4. 原 F-015 `(1,2,0)`、A原probes、closeout_rework、事件及恢复断言保持；新增完整生产服务链的重叠订阅测试和r2认证历史共同验证。

这是发现关系、执行作用域、凭证和资源提交的耦合缺口；B采用持久发现关系、逐消费者需求与冻结执行作用域修复，未以局部重排替代模型。A原始失败记录继续保留，关闭仍须A实际复核。

<a id="f-020"></a>
#### F-020 / P1：抖音登录检查选用了不能证明登录的接口 — A已核实关闭（当前源码及本次登录范围）

用户提供Cookie并明确授权后，原m1-9包的 `probe_auth` 请求 `query/user`，HTTP200却返回unknown；该端点可为访客分配ID，原代码仅接受未获证明的根 `is_login`。这会使有效凭证无法成为Core允许关联订阅的valid登录主体。不能把它归咎于用户重新复制Cookie。来源资料与实际有界结果分别保存，未复制参考实现代码。

A补生产子进程测试 `douyin_auth_probe_requires_self_profile_not_guest_uid`，修复前退出101，断言实际走query/user而非当前用户端点，见 `auth-probe-initial.json/.log`。修复 `protocol.rs::dispatch(probe_auth)` 改为 `user/profile/self`，在成功响应中要求非零数值uid与非空sec_uid；只输出状态与能力，不输出UID/昵称。访客根ID/伪is_login、零ID、缺字段保持unknown，认证拒绝保持AuthRequired。`request_budget.rs`只补固定端点的安全日志名称，不放宽网络权限。

修复后该5场景生产进程用例通过，全部9项生产协议测试通过；用同一用户Cookie和A实际构建的生产debug daemon/adapter再发1次请求，结果valid/login_proven=true。用户要求本轮完成确认后，又以同一self端点做带Cookie/不带Cookie各1请求：分别valid/login_proven=true与auth_required；两次HTTP均200，CLI分别退出0/1。Bilibili同nav端点对照也分别authenticated与auth_required。原始结果只留本地，公开无身份字段。负对照按原样保存，不把协议200算成登录。质量和内容权限仍unknown；本次成功不外推所有账号、长期有效期或全站访问。

<a id="f-021"></a>
#### F-021 / P1：满负载元数据节流类内竞争使消费者长期等待 — A已核实关闭

2026-10-09 A读取FIFO实现及取消/普通等待/跨站断言，独立C11复跑通过；从原始采样复算当前352个mixed并发样本、350个四路纯录制样本，并核对37个相邻区间两站同operation字节同时增加。原682样本失败保留，原2秒间隔/抖动/四次紧急预算未改变。当前正式时长和全部派生值通过，见 `artifacts/a-m1-r2-review/independent-metrics.json`；以下为原失败和B修复记录。

B首次当前源码正式测量进入mixed阶段后，两路录制保持运行，但只出现一个state=running的media，另一个observe长期running且没有进入媒体接收；后台paused=false、订阅enabled、无认证错误或失败尝试。原节流只限制普通/紧急两类比例，同类等待者同时醒来竞争最后请求时间，较新的调用可反复抢占；合法消费者未获得有界推进。该运行有682个连续原始样本、录制计时2361.374秒、同时2下载+2录制样本0，不能算正式通过。仅停止所属合成工作进程，让采样器保存失败结果；没有缩短后继正式时长。

原始证据为 `artifacts/b-m1-r2/measure-final/resource-samples.json`、`benchmark.json`、`final3/benchmark.json/.log` 和 `pacing-starvation-diagnostics.json`。第一次r2包/自动用例对应输入 `ed0198988b1a2a27b1a75db1018406c4cdc6f4a54ee4bff1a0308724c4628476`；不得将后续代码重新绑定这些记录。

新增 `c11_metadata_pacing_fifo_and_urgent_budget_prevent_consumer_starvation` 在修复前退出101，实际紧急顺序为4/0/3/2/1，见 `pacing-failed.json/.log`。修复复用现有Tokio FIFO Mutex，在命名空间内分普通/紧急队列；两队首继续受原2秒最小间隔、抖动及最多4次紧急优先预算约束，取消释放排位，平台独立。修复后同一反例退出0，覆盖FIFO、最老等待者取消、普通等待上界及跨站独立，见 `pacing-fixed.json/.log`。本项纳入同一r2的P6/P11/P13和C05/C11，最终全套、新包及完整正式复测另见本节B交付；A核实后才关闭。

<a id="f-022"></a>
#### F-022 / P1：直播质量查询扫描无关片段，正式热查询未达标 — A已核实关闭

2026-10-09 A核对按条目场次限定的查询与64项SQL代码缓存，独立计步反例1/1通过，状态仍实时读取；另从937个连续原始样本独立复算热查询p95为317.8358ms，满足500ms门槛，全部18项派生值与原回执一致。原903.249ms及60020步失败均保留。证据为 `artifacts/a-m1-r2-review/query-cost.json/.log`、`independent-metrics.json`；不是仅凭单元步数关闭。

该轮输入`5d13cbc5e40357d56732f5ad68e6d6d75b9fd807190f67c54cdae2ffcc2f3acc`的第二次r2正式测量已完整结束：939个连续样本、905.828秒有效闲置、3622.714秒录制、354个同时2下载+2录制样本，任务错误0。工作负载退出0，但热查询p95为903.249ms，超过500ms，测量器/回执退出1；其余门槛通过。`measure-final2/resource-samples.json`摘要为`d68e9e29335617369169adb8856c21313f926c866983432f0005391984d1aaa2`，原始结果和`final4/benchmark.json/.log`保留为failed，不能归入后继通过聚合。

只读同一合成库的EXPLAIN与计步定位到`quality::state`的直播退化查询：SQLite从全库`live_fragments`扫描后再匹配场次；没有任何录制场次的条目也扫描无关历史。50次读取约308万VM步；先限定条目场次的相关EXISTS约1400步。这是已复现的额外成本，不把所有间歇性慢样本无证据归因于机器繁忙。未改变全库搜索/计数/认证条件、数据规模、时长或资源上限。

新增`c08_live_quality_query_ignores_unrelated_recording_fragments`使用实际迁移和一万片无关历史，原查询执行60020 VM步，超过5000步守护，`query-cost-baseline.json/.log`退出101。修复先筛本条目场次，再检查片段的严格缺口/实际降级，空场次、其他场次、目标片段与实际规格状态均断言；`query-cost-fixed.json/.log`退出0。列表、质量与认证摘要复用现有rusqlite有界预编译语句缓存（64项），只缓存SQL代码、不缓存结果；每次仍实时读取旧选择、修订和认证历史。最初两份新夹具遗漏必填列的失败也保留为`query-cost-failed`与`query-cost-failed2`。

本项纳入同一r2的P7/P13、C08/C09及必需用例。原全套/旧包仍只证明原输入；修复后的全部自动检查、新包和正式资源复测必须另行通过，A核实后才关闭。

<a id="f-023"></a>
#### F-023 / P1：共享任务正常转交会立即废弃已完成消费者的升级候选 — A已核实关闭（本次复核范围）

触发：已有匿名低清资源；两个启用的订阅以不同凭证成为同一media任务的消费者。第一个消费者完整下载高清候选，`upgrades::finish`后为pending，旧选择仍低清；随后正常调用`ClaimedJob::succeed`，仅调和到第二个待执行消费者。第二个消费者尚未获取任何资源，原选择、正文、凭证、订阅配置都未改变，`upgrade.list`却把第一候选永久标为stale。实际反例结果为pending→stale / comparison_scope_changed。

位置：`jobs/consumers.rs::finish/reconcile`转交时递增逻辑任务`target_generation`；`upgrades.rs::snapshot/confirmation_current/invalidate_stale`把候选绑定到该共享任务的当前代次。执行租约撤销与某一消费者已经完成的确认依据被混为一体。依据r2 P11/P12、C11、FR-QUALITY-006：完整候选应进入统一页面供用户决定，正常调度另一个独立消费者不应撤回尚有效的已获取结果。

反例：`a_completed_shared_consumer_keeps_its_downloaded_upgrade_available`。直接调用当前Core消费者、对象提交与升级服务，实际断言失败；不是构造一个伪造的失败回执。修复须区分消费者请求/控制代次与共享执行租约，使正常转交不永久失效候选；同消费者真正取消、改策略/凭证、删除、正文或旧选择变化仍拒绝旧确认。覆盖后继消费者结果相同、较低、无提升、失败及正常新候选；有可用升级时不能只剩被自动废弃的候选。不能仅移除全部代次检查。

<a id="f-024"></a>
#### F-024 / P1：删除后恢复把已拒绝候选写成当前选择 — A已核实关闭（本次复核范围）

触发：低清automatic为当前选择，完整高清候选由用户明确Reject；确认旧选择仍低清。随后对同条目执行真实`deletion::preview/apply`的copies范围，立即按返回代次调用`deletion::restore`；当前选择变成`high / rejected`。再执行`quality::evaluate`仍保持该错误选择。旧对象字节未丢失，问题是确认边界被恢复路径越过。

位置：`deletion.rs::restore`按最新非派生变体恢复`asset_selections`，没有过滤`approval_state`；`quality.rs::evaluate`找不到合格的当前基准后直接continue，不能纠正这个选择。依据C07/C11、FR-QUALITY-003/006及契约§7.2：恢复档案/副本不等于接受用户已拒绝的升级。

反例：`a_delete_restore_does_not_promote_a_rejected_upgrade_to_selection`，实际`("high","rejected")`，期望`("low","automatic")`。修复须在恢复及所有自动选择/派生使用路径统一执行候选准入；没有已批准资源时保留未选择，不把pending/rejected改为accepted。覆盖copies/archive、pending/rejected、没有旧版、已固定/共享及已回收副本；恢复后质量维护、播放派生和导出不能绕过原决定。现有UI默认过滤仍保留，本反例不声称原生播放器已实际播放拒绝资源。

<a id="f-025"></a>
#### F-025 / P1：不同登录补全任务共用物化ID，新任务被旧准备记录取消 — A已核实关闭（本次复核范围）

触发：旧completion-media任务代次1，为某media和候选保存到OBJECT_READY后取消；更新并验证新的凭证代次，建立另一个completion-media任务，其任务代次也从1开始。媒体、表示、规格、大小/摘要相同，生产`acquisition_operation`为二者构造同一ID。`uncommitted_operation`仅绕开COMMITTED，因此仍选旧OBJECT_READY记录；`ObjectStore::ready`核对新任务租约及旧文件，却不核对物化归属，实际返回true。随后commit正确拒绝不同job_id并返回Cancelled；按当前失败处理，新补全任务进入cancelled，未取得独立的新身份字节。

位置：`core.rs::acquisition_operation/uncommitted_operation/run_media`、`objects/mod.rs::ready/start_receiving/commit`。物化ID只含media、任务代次及候选摘要，缺少独立逻辑请求身份；新r2补全会为相同媒体创建多个不同任务。依据C03/C07/C11、不可变获取身份与恢复契约：同一请求的重开可复用自己的准备结果，另一请求或凭证身份不能复用旧任务的接收证据，也不能因此被当成用户取消。

反例：`a_prepared_bytes_from_cancelled_job_are_not_ready_for_new_completion`，使用生产ID公式和当前公共对象服务；实际ready=true、commit=Cancelled、新任务状态cancelled。该反例定位对象服务和任务失败处理，未冒充完整真实站点下载。修复须建立可重入且隔离逻辑请求的物化身份，所有ready/重用/重置阶段核对归属；保留正确的commit屏障。覆盖两个补全任务、普通media与补全交错、凭证换代、RECEIVING/PREPARED/OBJECT_READY/首资源已提交后重开；同任务恢复不多建变体，新身份必须有自身获取证据，旧记录/旧文件继续保留。

三个反例共同入口：`cargo test --manifest-path artifacts/a-m1-r2-review/probes/Cargo.toml --locked --offline --target-dir target --no-fail-fast -- --nocapture`。当前真实子命令退出101，0 passed / 3 failed / 0 ignored；最终回执为 `artifacts/a-m1-r2-review/independent-probes-final2.json/.log`，初始两项和中间三项失败均保留。反例运行在隔离合成Profile，直接使用被审Core；独立审查crate源码/锁文件及摘要保留，B修复时将断言纳入规范测试和阶段门禁。当前生产源码、既有测试和216项规范输入未修改。

<a id="f-026"></a>
#### F-026 / P1：WBI使用64字符密钥且重复发送wts — A已核实关闭（源码签名范围）

位置：`adapters/bilibili/src/wbi.rs::mixin_key`、`handlers.rs::signed_get`。原算法遍历全部64个置换项，未按协议取前32字符；`sign`的query已含wts，`signed_get`又附加一次。原两个nav/mixin测试错误地期待64字符，使测试绿色不能证明协议正确。影响签名的枚举、详情、播放地址等现有生产路径，依据P1/C01/C03和WBI既有契约；与旧匿名room_init诊断的412原因分开处理。

A增加`.take(32)`及生产复用的`wbi::signed_url`，只附加一次w_rid；修正两个错误nav密钥期望，保留原三个任意密钥sign向量及其断言。新增`c14_signed_url_uses_32_character_key_and_one_timestamp`核对完整实际构造路径，固定结果：key为`ea1db124af3c7062474693fa704f4ff8`（32字符），query为`bar=2&baz=3&foo=1&wts=1702204169`，MD5为`e0ed91b768b58beb346ad3116de6ab68`。

正确独立反例位于`artifacts/a-m1-real-readiness/wbi-verified-probe/`。`--features baseline c14_`导入保留的原Git源码，退出101、1 failed；相同断言导入当前源码，退出0、2 passed（含实际URL回归）。回执`f026-correct-oracle-baseline`和`f026-correct-oracle-fixed`原样保留。当前Bilibili26/26、生产适配器9/9、对应构建/严格Clippy及fmt通过；命令见§16.16。本关闭不宣称旧包已含修复，也不代替真实Core接线验收。

A最初独立探针及首次新增期望误多写了一个字符，形成33字符预期与len=32相矛盾，`f026-wbi-baseline`不能单独作为有效关闭依据；`bilibili-final`因此23 passed/3 failed。原错误探针、输出及`wbi-oracle-correction.json`均保留，后用独立Python置换/MD5核对并重新执行上述有效的旧失败/新通过对照，没有把测试自身错误归咎于产品。

<a id="f-027"></a>
#### F-027 / P1：Douyin受保护详情/投稿请求缺少SecSDK访问上下文 — A已核实关闭（本次复核范围）

位置：`adapters/douyin/src/protocol.rs::api`；Core的`pluginhost::handle_proxy_fetch`和凭证/传输边界需共同接线。生产请求已有aid/device_platform/业务参数/A-Bogus和Cookie，但没有端点所需的访问者上下文与`x-secsdk-web-signature`。同一有效Cookie、同一生产适配器：self登录成功，详情/投稿均HTTP403，安全归类为`uifid_missing`，不是Cookie失效或作者没有内容。

在隔离代理补充现有Cookie中的访问者ID、设备指纹和SecSDK签名后，详情HTTP200/业务0、81,416字节且生产适配器解析成功；投稿HTTP200/业务0、20条、has_more=1、1,454,679字节。投稿随后因本地代理转发超过帧预算失败，不能称该生产链已通过，另见F-029。原失败、补充后的响应及登录观察均保留，命令/回执见§16.16；没有刷新Cookie、伪造msToken、读取浏览器或绕过验证码。

验收：保持原F-020 self登录判断，按确证的host/method/path加入Core内的凭证派生签名上下文，绑定同一凭证代次；fixed-vector覆盖编码/顺序/唯一参数及实际发送字节。Cookie/UIFID/指纹/短期签名不交给适配器、UI或final_url，不进日志/参数/快照/导出/包；跨域重定向剥离。缺上下文、过期凭证、明确挑战和普通网络未知分别可解释。两站生产任务的访问/获取身份及原C06/C11/C13守护通过，指定Douyin视频完整下载；投稿页必须继续通过F-029的有界处理，不能只让HTTP变200。

<a id="f-028"></a>
#### F-028 / P1：真实直播来源与默认媒体域/HTTPS契约不兼容 — A已核实关闭（本次复核范围）

位置：`crates/core/src/transport.rs::host_rules/validate_url/open_media`、`adapters/douyin/src/live.rs::map`及P14房间入口接线。指定Bilibili直播的站点媒体地址跳转到`bytefcdnrd.com`，指定Douyin直播返回`douyinliving.com`的HTTP FLV；这两个来源CDN都不在默认媒体清单，后者也未规范化为已经实测可用的同host/path/query HTTPS。当前Core会拒绝scheme/host，不能直接完成这些真实来源的录制。

独立故障断言`c14_known_live_media_cdns_work_without_user_allowlist_or_cookies`读取当前Core默认规则，两个域族同时缺失，`integration-probes`退出101；合成域不含用户房号。真实诊断分别在窄CDN策略、每跳公共地址/TLS核验下得到1080P AVC/AAC样本。原HTTP拒绝、Bilibili动态重定向地址拒绝与最终接收均保留；不能以添加用户测试白名单或允许任意HTTP绕过产品缺口。Bilibili音轨8082端口的初次拒绝属于A诊断助手限制，Core本身允许HTTPS非443端口，不将它计为产品缺陷。

验收：按来源证据注册窄媒体域并保持与凭证API域分离；只对已证明可用的CDN同地址升级HTTPS，逐跳DNS/IP/证书与嵌套HLS检查不减。负例覆盖伪后缀、跨站、私网/重绑定、明文回退和媒体Cookie泄漏。P14须从真实房间URL识别别名/房间/主播并经Core录制，两站各至少60秒实收，封片实际≥720P、必要音轨/哈希/恢复/导出通过；不以诊断脚本样本代填。

<a id="f-029"></a>
#### F-029 / P1：正常投稿单页超过固定元数据预算，无法完成有界归档 — A已核实关闭（本次复核范围）

位置：`adapters/douyin/src/enumeration.rs::Cursor::request`固定count=20；`adapters/common/src/runtime.rs::Client::fetch`固定max_bytes=524288；`transport.rs::fetch_metadata`、`pluginhost::handle_proxy_fetch`与1MiB插件帧。F-027补齐后，指定作者首个20条页返回合法JSON、1,454,679字节，明显超过512KiB；直接base64转发也超过1MiB帧。既有小夹具未覆盖该正常来源规模。

真实对照`douyin-post-secsdk`的HTTP200/20条已取得，但A隔离助手曾直接把超限响应写给生产适配器而出现BrokenPipeError；此为助手没有遵守产品帧预算，保留为失败，不称Core崩溃。另用当前生产适配器请求和实际Core受限HTTP服务回放同大小的20条合成页，Core正确拒绝超限、错误类别unknown，适配器返回ok=false，没有完成归档。反例`c14_production_post_page_survives_bounded_metadata_transport`退出101；与F-028共2 failed/0 passed，原回执`integration-probes.json/.log`保留。

验收：协调页大小、HTTP体、序列化开销与协议预算，可采用有界页大小和有限缩页/大对象协议；完整遍历所选边界而不丢条目、全文或必要资源，失败不推进游标、不标历史完成，取消/重启可继续。覆盖20条约1.45MB、单条大正文、空页has_more、异常超限及并发/取消；端点仍超限须明确受限，不能无限重试。不得删除512KiB/1MiB守护、静默截断或把请求永久失败当可下载。该项属于原M1订阅/完整性义务，不是用户配置问题。

<a id="f-030"></a>
#### F-030 / P1：长录制后的已提交暂存记录逐行同步拖慢启动恢复 — A已核实关闭（本次复核范围）

B在当前r3的C07/C09完整正式测量中发现：10万条/50万媒体元数据、完整3621.335秒录制及361个混合并发样本均完成，任务错误0，但`Core::open + recover + IPC可达`耗时45.246秒，超过原30秒门槛。原`resource-formal-2`、945个样本、生命周期、查询日志和当时measure二进制完整保留；其他资源指标通过不抵消这项失败。

位置：`objects/mod.rs::recover_materializations`的COMMITTED暂存收尾。文件安装早已清理目录，但约10,798条已提交记录仍保留staging_path；旧恢复按每行独立FULL事务清空，形成上万次持久同步。新增`c07_recovery_bounds_committed_cleanup_writes_and_keeps_objects`在实际SQLite/WAL上重现：4097行产生4097个WAL帧、15.279秒，写入放大断言失败。

修复以rowid键集分批读取，每批最多64条；先完成受控暂存目录清理，再用一个短事务确认该批相同COMMITTED路径。目录/路径须属于相应操作，拒绝链接和越界；崩溃在文件清理与事务确认之间仅重复清理不存在的目录。已验证对象、物化历史、未完成文件的重算/安装和副本哈希校验保持。最终定向复验4097行降为222帧、0.915秒，原对象恢复套件8/8通过，并断言对象字节、全部历史、重入及越界目录不被删除。其后使用当前m1-r3-11包完成原规模/时长的完整正式复测：恢复202个未完成普通任务且IPC可达耗时2.201秒，934个样本及18项指标复算通过，见[§17.7](#m1-r3-final-resource)。原45.246秒失败和存储中断跑次保留，本项仍待A复核。

<a id="f-031"></a>
#### F-031 / P2：共享候选的比较读取了后继消费者质量偏好 — A已修并自检关闭（当前自动验证范围）

触发：原消费者prefer_fps=true，已选4K30，新候选1080P60完整且pending；正常succeed转交另一消费者，候选仍有效，`upgrade.resources`却由higher变成lower。`upgrades.rs::resources_on`读取共享job的当前execution_scope，转交清空或换成后继偏好。A独立`shared-policy-probe`实际失败，输出保留；本项是比较依据错误，不声称旧对象被替换。

修复：新共享确认保存原prefer_fps，比较及确认沿用；旧token保持原形状，仅在原订阅配置版本仍相同时恢复旧偏好，缺依据显示unknown。没有新增schema或改写迁移。`c14_shared_candidate_comparison_keeps_originating_quality_preference`覆盖正常转交、下一消费者不同偏好、可接受原候选、旧token和后续配置改变的历史比较，`shared-policy-fixed`通过。

<a id="f-032"></a>
#### F-032 / P1：一次性直播在明确停播后未进入封口路径 — A已修并自检关闭（当前自动验证范围）

触发：链接预览/确认及场次登记成功；录制重解析已收到明确offline，`source_snapshot`作为预览守护返回SourceUnavailable，既有场次仍waiting。A独立`live-offline-probe`和生产适配器驱动的`live-production-baseline`实际失败；未发真实请求。

修复：已有录制的resolve_live将同房间明确offline交给原recording封口；Douyin省略身份时只接受宿主已白名单化的offline并沿用请求已绑定房间。预览仍拒绝停播，权限/未知/其他错误仍失败。`c14_direct_live_offline_seals_but_denial_and_unknown_do_not`通过两站生产适配器、拒绝/未知反例、封口、离线重开及零作者扫描断言，`live-production-fixed`通过。

### 16.4 实际命令、反例与限制

本节保留上轮A审查r1及本地登录验证的原命令与当时结论；当前r2实际复核在§16.13，旧失败不改写。

除修改前原bundle核对，A新跑次均位于 `artifacts/a-m1-review/`；由 `stage_evidence.py run`记录的包装命令在子进程失败时退出1，JSON保留真实子命令退出码101。下表不把包装退出码当Rust用例结果。

| 实际命令 / 检查 | 结果 | 证据 |
| --- | --- | --- |
| 原bundle两个检查入口（修改前） | 0 / passed | 16.1独立核对与原bundle |
| 抖音源长度新增反例（修复前） | 101 / failed | `length-failed.log`；不覆写 |
| `cargo test -p trace-adapter-douyin --locked -- --nocapture` | 0 / 11 passed | `douyin-final2.json/.log`，原`douyin-fixed`与`douyin-final`保留 |
| `cargo test -p trace-core --locked overlapping_subscription_can_continue_same_entry_after_owner_pause -- --nocapture` | 101 / 1 failed | `subscription-final2.json/.log`；F-019仍开，原跑次保留 |
| `cargo test -p trace-e2e --locked --test closeout_rework -- --nocapture` | 0 / 3 passed | `range-and-group.log`及`targeted-tests.json` |
| `cargo test -p trace-core --locked c04_closed_capture_four_commit_windows_survive_cancel_and_restart -- --nocapture` | 0 / 1 passed | `live-commit-windows.log` |
| `cargo test -p trace-core --locked c06_nested_hls_private_network_key_map_and_credential_scopes -- --nocapture` | 0 / 1 passed | `nested-network.log` |
| 抖音登录生产路由新增反例（修复前→后） | 101 failed → 0 passed | `auth-probe-initial`、`auth-probe-fixed` JSON及log |
| `cargo build -p trace-daemon -p trace-adapter-douyin --locked` | 0 / passed | `build-final.json/.log`；仅debug，不重建measure或发行包 |
| `cargo test -p trace-e2e --locked --test m1_production_adapters -- --nocapture` | 0 / 9 passed | `production-final.json/.log`；保留原8项另加1项 |
| `cargo fmt --all -- --check` | 0 / passed | `fmt-final.json/.log` |
| `cargo clippy --workspace --locked --all-targets -- -D warnings` | 0 / passed | `clippy-final.json/.log` |
| 新凭证准备脚本隔离反例 | 0 / 3 passed | `test_site_check_preparation.py`：缺项零输出、所选站点/秘密隔离、重复/格式/大小拒绝 |
| `python -m unittest discover -s scripts/tests -v` | 0 / 15 passed | `python-final.json/.log`，含全部原12项及新增3项 |
| `python scripts/check_docs.py` | 0 / passed | `docs-control.json/.log`；49文档、507本地链接、85需求、28AC、12INV，零错误；提交前最终同命令回执为`docs-final2.json/.log`，之前回执保留 |
| `python scripts/check_publication.py` | 0 / 0发现 | `publication-control.json/.log`；工作树与索引，启发式检查另辅以人工文件归属核对；暂存后结果随Git交付审计保存 |
| 原bundle的check及verify入口（修改后） | 均1 / 按预期拒绝 | `stale-bundle-check.json/.log`、`stale-bundle-verify.json/.log`：源码、文档、包、命令与原始基准输入不匹配，原回执不改绑 |
| `git diff --check` | 0 / passed | 本地工具实际执行；暂存后的最终检查随Git交付审计保存 |
| 本地凭证前置（用户保存前→后） | 两站各2 blocked → 两站各0 prepared | `credential-preflight.json`、`credential-authorized-preflight-1.json`；此处均零网络，prepared不等于验证登录 |
| `python artifacts/a-m1-review/check_auth_controls.py` | 批处理0；每站带Cookie的site-check为0，无Cookie为1/auth_required | `auth-controls.json`及4份独立site-check日志；使用当前生产debug程序，无Cookie结果是预期负对照 |

部分Rust测试输出有Windows linker消息及增量目录权限提示；实际测试运行/结果和退出码如上，没有删除目录或取消断言来绕过。A当前没有机械重复完整Rust/前端/五套件、发行包或长测；未改前端复用原有效证据，F-019失败保留，后继必须满足r2全部门禁。

真实操作按用户本轮授权并使用新隔离目录，没有前台键鼠/浏览器操作：

| 范围 | 实际调用与结果 | 能证明什么 |
| --- | --- | --- |
| Bilibili登录 | m1-9 `site-check --execute-authorized`、仅probe_auth、1请求，退出0/authenticated | 本次提供的Cookie在该登录端点有效；不公开会员/账号字段 |
| Douyin登录原包 | 同样1请求，退出0但能力unknown | 协议请求成功不等于登录证明；F-020原始观测 |
| Douyin登录A修复 | `target/debug/trace-daemon.exe site-check`配A生产适配器、仅probe_auth、剩余预算2中实际1请求，退出0/valid | 本次当前用户检查成功；没有将真实响应写入普通档案 |
| Bilibili同接口登录对照 | A生产debug程序，nav带Cookie/不带Cookie各1请求；HTTP均200，退出0/authenticated及1/auth_required | 本次Cookie有效，空凭证不会被误判登录；没有读取浏览器登录态 |
| Douyin同接口登录对照 | 同一A生产debug程序，self带Cookie/不带Cookie各1请求；HTTP均200，退出0/valid与login_proven=true及1/auth_required | 修复后的判定能区分本次有效会话与访客；负对照失败回执保留 |
| 给定直播间身份准备 | 固定HTTPS room_init 的无Cookie诊断请求1次，HTTP412，工具退出1 | 该诊断使用标准库请求，未进入生产probe_live或媒体接收；不能用它宣称生产录制失败或720P上限 |
| 给定抖音主页视频列表 | A生产site-check，已登录、最多2请求预算中实际1次HTTP403，子命令退出1/source_unavailable，停止 | 没有收到列表，不证明空列表、Cookie失效或作者无视频；匿名对照未继续，未证明全历史 |

原登录3请求，加本次同接口对照4请求，登录累计7请求；给定目标内容诊断共2请求。对照发生于2026-10-08 14:48 UTC，每站每种模式独立Profile/输出目录、最多1请求、30秒，跑次间隔至少2秒。实际命令形态为 `target/debug/trace-daemon.exe site-check --profile .local/site-check/<site>-auth-control-<mode>-1/profile.json --request-file .local/site-check/<site>-auth-control-<mode>-1/request.json --execute-authorized --output .local/site-check/<site>-auth-control-<mode>-1/result`；site为bilibili/douyin，mode为authenticated/anonymous。带Cookie只传受保护文件路径，匿名计划明确credential_file=null；不改原Cookie文件。程序摘要、状态码和计数在 `auth-controls.json`，逐项结果校验在 `auth-controls-audit.json`。

Cookie原文件仍留本机secret目录，真实目标/原始结果只留 `.local/site-check/`，公开只保留状态/计数，当前用户操作与授权仅在 ACT-002。没有实际媒体下载、订阅创建、真实录制、原生产品验收或发布。列表/直播质量请求条件由开发者在已有范围内核查，不能为绿灯弱化挑战/限流停止规则。两站Cookie确认已由A在本轮完成，不作为B返工或用户重新配置的前置。

参考用途和TRACE接线一并核对：当前用户接口用于确认会话本人，客户端访客身份、请求签名、内容端点的访问能力各自独立。Core按站点/凭证作用域取值，只有with_credential且目标在允许API域时附Cookie，跨域重定向移除认证头，适配器不取得秘密正文。媒体CDN不带Cookie不能倒推解析时匿名；r2契约与C11已补该正反例，具体结论见[解析研究§7](../research/site-parsing-current.md#7-a-的有限真实登录与内容请求观察2026-10-08)。未将外部实现源码、私有路径或浏览器提取流程移入产品。

### 16.5 逐文档流程核对与保留审查

本轮按入口、治理、工作流、Git、数据、架构、质量、设计、模板、状态/计划和用户待办逐份核对适用规则。只同步TRACE自身约束；外部技术栈、业务模块、许可、阈值和操作授权未移入。本机只读核对的路径/摘要留在本地审计，公开内容不记录私有参考关系。

| TRACE正本 | 核对结果 / 本次同步 |
| --- | --- |
| AGENTS.md | 保留入口和A规划/B实施/A最终审查；不授权子Agent或自动消息，无需重写一致条款 |
| CLAUDE.md | 仅引用AGENTS，保留 |
| CONTRIBUTING.md | 补真实输入前置、同计划修订和原证据范围 |
| REVIEW.md | 补逐项语义覆盖、新需求不追溯为旧缺陷、旧包不冒充含A修复 |
| docs/README.md | 当前r2/A审查与凭证入口；历史B交付保留单独入口 |
| dev-rules/README.md | 触发导航覆盖实际专项规则，保持 |
| governance/documentation-policy.md | 补用户环境保护、按内容整理、受保护测试与逐文档差异记录 |
| governance/document-registry.json | 当前计划路径仍唯一，更新实际核对日期/依据，未建立新计划文件 |
| development-workflow.md | 同计划范围修订、证据分类复用、失败反例的本地审查提交；A通过后直接收尾 |
| repo-and-environment.md | 实际命令/工具/本地准备与r1/r2证据边界；只读核对远端保护，不虚构hook或产品CI |
| architecture-and-contracts.md | 明确共享身份与消费者执行权、凭证/质量作用域和提交屏障 |
| data-and-security.md | 本地配置/秘密分离、缺输入停止、显式匿名对照、授权与站点失败停止 |
| local-reference-and-files.md | 逐级索引、只读、.env.example与本机配置分开；已有用户值不覆盖 |
| quality-gates.md | 指纹/旧证据、原生不抢焦点、连续长测、真实条件与当前r2门禁待补 |
| site-adapters.md | 当前全类型范围、登录与可访问性分开，修正LOOP错误归属 |
| open-source-first.md | 保留先查证/后集成、补丁移除条件和具体上游发送授权，无需改既定技术栈 |
| product-rules/core-product-principles.md | 保留身份、来源、恢复与真实质量原则，新细节只在需求/契约维护 |
| design-rules/DESIGN.md | 保留蓝白主页/管理入口，补非前台验证及认证时间线/确认页的状态边界 |
| templates/README.md | 修正用户待办/OSS/LOOP/F的唯一维护位置，避免总表包办全部状态 |
| templates/agent-handoff.md | 新旧范围、A已修与B待修、证据复用/失效、实际Git基点 |
| templates/validation-report.md | 增加复用/未重跑边界与真实输入来源；不代填产品验收 |
| templates/module-spec.md | 共享消费者、停止准入、提交屏障、旧代次与恢复 |
| templates/change-proposal.md | 保留按规模最小材料和需求→契约→验收，复用本计划无需另提案 |
| templates/decision-record.md | 保留来源/替代/迁移/重审，当前新增决定写有效决定正本 |
| templates/open-source-assessment.md | 保留官方版本/维护/许可/边界证据，未复制项目专用工具或权限 |
| .github/PULL_REQUEST_TEMPLATE.md | 已包含阶段、行为、验证、恢复、限制与分别授权，保持 |
| USER-ACTIONS.md | Cookie教程、当前输入/授权/真实结果唯一入口；既有人工验收和许可状态保留 |
| 状态/唯一计划/需求映射 | r2新增P/C与85条需求，原B证据和A失败分开；没有竞争计划 |

历史整理抽查：11份退役文件在固定main基点均可读，合计111211字节；抽查原F-001预构建、F-002目标外零写入、F-015暂停、F-016媒体节点、F-017夹具顺序及原B实现保留项，当前守护/状态与完整SHA入链仍在。原报告§5/§9失败、旧B§12要求由固定完整SHA追溯；本次没有删除旧包、原Profile或失败日志。

原A `a_review_probes.rs` 与基点无差异；closeout_rework的HTTP/分段函数保持词法一致。三项旧Rust body调整逐hunk核对：物化操作ID随实际表示/大小摘要变化；F-015精确限定原增量任务并增加全任务清单，保留 `(1,2,0)`；full_loop将既有哨兵显式关联订阅，原45断言保留。原home事件断言和媒体节点未被A更改。原109项保留审计与A新增反例都保留，不以总数增加掩盖删除。

### 16.6 r2 新增范围与 A 交接时判断

P11：修F-019并实现逐获取认证历史、两个时间线和新迁移；P12：登录后有界补全、完整候选、统一用户确认和直播逐片标记；P13：沿用本地凭证准备并完善执行前置，接入新用例/计划/schema、产出当前包/自动/正式资源证据。详细验收只在[唯一计划](stage-plan.md)和[契约§7](../site-archive-contracts.md#authentication-acquisition)维护，不从旧self-check计完成。B不得简单将现有quality选择页称为已实现统一候选确认。

| 判断 | 本次结论 |
| --- | --- |
| B实现交付 | 原r1 P0～P10代码与产物已交付，存在F-019；r2新增范围待B实施 |
| B自动自检 | 原被审输入的255身份/98必需及五套件证据核对属实，覆盖未包含A新反例 |
| A技术审查 | 已执行，failed / rework-required；F-018/F-020局部关闭，F-019未解决 |
| 产品验收 | not-run；两站本次登录验证通过不等于下载/录制或原生体验通过 |
| 远端与发布 | 未push/PR/合并/发布，无本阶段对应授权；Q-004保持原状态 |

以上表格与下方prompt保留A交接时的判断和待办；B已接手同一计划，后续实施与实际证据从§16.8起追加。A最终通过后直接向用户收尾，不要求B复审A。

<a id="a-to-b-m1-r2-initial-rework"></a>
### 16.7 上轮 A→B 集中实施与返工 prompt（历史）

本段保留上轮A交接内容，当前集中返工使用[§16.14](#a-to-b-m1-rework)。

报告与A修改归入同一个未推送阶段提交。这里用实际Git读取命令定位交接提交，最终消息提供完整SHA，避免文档递归包含自身哈希。

```text
请作为 Agent B，在 TRACE 的 codex/m1-two-site-archive 分支，按唯一计划 docs/delivery/stage-plan.md 的 TRACE-M1-01 r2 集中实施与返工。先读取 AGENTS、索引、状态、有效决定、事项、USER-ACTIONS 和适用规则，执行 git log/status 核对 A 交付消息的完整SHA与用户后续修改。main 基点 960984c34487244f31f1238dffc2bd9faaa292e7，原被审B为772ab9a6c523a22385f024abafa66aef6ed7f0f1。本阶段相对main只保留一个DCO提交，后续amend同一阶段；不改main或备份历史，不push/PR/合并/发布，不启动子Agent或自动跨聊天发送。

当前审查/F正本是 docs/delivery/stage-report.md §16，§13～15保留原r1规划/交付与失败。优先修 F-019/P1：同账号dynamic先发现视频→暂停该订阅→仍启用的video再次发现；共享observe仍paused且payload绑定首订阅。复现命令 cargo test -p trace-core --locked overlapping_subscription_can_continue_same_entry_after_owner_pause -- --nocapture，当前1项失败。连同media同键upsert、发现关系/合法消费者、凭证与质量作用域、租约/代次、已取消/删除和重启竞态一起修；不可仅改paused为queued或盲目换payload。验收按§16.3全部条件。

保留 A 已修 F-018 的来源data_size传播及非法声明反例，F-020 的抖音user/profile/self登录验证及生产进程反例。保留 F-001～017、原A Rust probes、closeout_rework、home事件和媒体节点断言、原失败、蓝白主页/管理入口、旧稳定包/验收Profile、本地参考隔离。不因新类型重建已闭合范围，不删除/skip/弱化测试。

连续完成 r2 P11～P13/C11～C13：逐获取保存anonymous/authenticated/expired/unknown历史，两个时间线共用稳定内容身份和同筛选分页/统计；同blob不抹去不同获取历史。认证依据解析身份和同代次验证，已登录解析后无Cookie的CDN传输仍保留该获取身份，匿名回退另记上下文，禁止为标签向CDN发Cookie。登录验证后有界重查普通资源，取该身份当时可得最高质量，完整新候选进入统一资源升级页，用户接受/拒绝/保留两份；确认绑定旧选择/修订/代次，失败或迟到不伤旧版。直播仅记录逐片认证与实际分辨率码率等规格，混合和未知明示，不补录过去、不进普通升级队列。旧schema3/4、备份/恢复、跨订阅、凭证更新、取消/停模块/退出、重启与旧确认都补定向反例。新增需求是用户追加，不追溯改写原B自检历史。

本地 Cookie 方案已建立：.env.example、scripts/prepare_site_check.py、对应3项隔离反例；实际配置只在忽略的.env.local/.local。只读取任务必需键，保留用户文件，秘密不入日志/命令行/包/导出/Git。缺输入按ACT-002停对应真实测试并给步骤；原生测试用隔离Profile、独立WebView/回环CDP和不激活窗口。A已完成两站同接口带Cookie/不带Cookie对照：带凭证分别authenticated/valid，无凭证均auth_required，四次HTTP均200；Cookie有效性已经确认，不留给B或要求用户重新复制。此前给定目标查询出现HTTP412/403，已停止，不能把登录成功当全站下载可用。现有具体授权、范围和当前结果只看USER-ACTIONS；不要无理由重复请求、绕过挑战或扩展对象/媒体范围。

按OSS-011核查上述真实内容端点的请求条件与错误分类，保留受限结果；具体目标在本机.local/site-check/user-targets.json。未取得可靠结果时如实说明具体范围，不把平台条件自动写成已修实现。将新增用例、计划修订/schema、.env.example等新输入和全部85需求中的66条M1适用范围接入现有stage_evidence门禁；保持19条后继范围未完成。原199输入/255用例/98必需、m1-9包及长测仅证明原B版本，不改绑收据或重写原始采样。新包另目录，在最终源码上真实执行需要的自动门禁与正式资源测量，缺项/陈旧/失败必须拒绝；不得降低长测规模/时长或借旧measure二进制冒充当前运行。

报告复用同一文件，逐F/P/C回写实现、命令、退出码、故障/复验及包/指纹/限制；同步状态、能力表、需求映射和登记。完成后提供完整B→A复审prompt及实际Git读取的最终SHA。A复核通过后直接向用户收尾，不再交你重复复审。
```


<a id="b-m1-r2-delivery"></a>
### 16.8 B 的 r2 集中实现与自审（2026-10-09）

本轮以 A 交付 `141d261db8569100564b3f88b897ba4ca00b0f61` 接手同一 `codex/m1-two-site-archive` 分支；实际 HEAD 与 main 基点核对一致，开工工作区干净，所有本轮公开改动归属 B。未创建竞争计划、子 Agent 或新聊天，也未自动跨聊天发送。继续采用同阶段未推送提交 amend 与原 DCO 身份；最终 SHA 由实际 Git 读取交付，文档不递归包含自身提交摘要。

F-019 的发现事实、消费者需求和执行租约已分开：`entry_subscriptions`只登记有证据的发现关系；`job_consumers`分别保存每个订阅的请求/完成序号、等待状态、失败次数和退避。`process_scan_page`与`persist_detail`都经同一注册/调和入口；稳定 entry/资源组不复制，有效执行中的消费者不被抢租约，暂停或失效的消费者让出执行机会。认领时冻结订阅版本、凭证 ID/代次和质量配置；认领、心跳、解析、对象提交都检查作用域。暂停/恢复、取消、停模块、删除、凭证变更及重启拒绝旧工作；控制操作不记失败尝试，显式取消不因发现新消费者自动复活。Core凭证缓存使用平台/凭证/代次作用域，匿名执行清除未限定的缓存身份。

| P/C | 主要实现及自审边界 | 对应反例 |
| --- | --- | --- |
| P11，C01/C05/C11，F-019 | `jobs/consumers.rs`、`jobs/scope.rs`、Core扫描/详情/媒体链；不同凭证和质量不串用，首owner的queued/running/paused/succeeded均有活跃消费者继续 | 原 `overlapping_subscription_can_continue_same_entry_after_owner_pause`；共享observe/media状态、控制/重启/失败预算矩阵；两站生产adapter加合成HTTP字节链 |
| P11，C07/C11 | 新迁移5；迁移1～4逐字保留。schema3/4各步迁移前一致备份，失败事务回滚。旧变体认证unknown；不可变获取上下文和资源观察由触发器保护 | 原schema3备份/回滚与新schema4失败注入；同blob不同身份、全量/元数据备份及无凭证恢复 |
| P11，C08/C11 | `acquisitions.rs`和DTO、共享feed/search筛选、详情逐变体历史；同一entry可出现在多个认证时间线，分页/计数/定位共用条件和游标；混合/失效/未知明确 | 67条多身份分页/计数/定位、游标跨筛选拒绝、历史不可变、惰性历史页；原home事件/媒体节点断言完整保留 |
| P12，C06/C11 | 本批次同代次生产登录探测才确认authenticated。解析身份贯穿无Cookie的CDN传输；明确匿名回退另建上下文，失败只记录安全分类 | 两站真实adapter进程的认证/失效/未知测试；本地HTTP实际校验零Cookie/Authorization，匿名与已认证各传输一次；同字节仍独立记录 |
| P12，C05/C07/C11 | `upgrades.rs`：登录验证后持久补全，固定发现上界，每次至多16条、续行间隔2秒；普通资源取该身份当时可得最高组合，所有必要轨/段完整才待确认 | 重复验证不无限重排，40条样本只处理38条普通非删除记录；候选不借旧音轨凑完整、旧版一直可读；直播零补录 |
| P12，C08/C11 | 统一资源升级页按平台/创作者/状态分页；10候选/页、20比较资源/页；逐项或明确所选1～50项接受/拒绝/保留两份。展示新旧身份、实际规格/字节、内容编辑/无提升/不可比较及回收影响 | 预期旧选择/修订/资源、订阅、凭证、工作代次及质量配置联合校验；缺失/损坏文件、迟到/重复确认、另一个窗口选择、取消/停模块/删除、重启/恢复反例 |
| P12，C04/C12 | 直播逐片保留认证和实际宽高/码率/FPS/编码；无法探测为unknown，混合状态和gap可见。关闭分片恢复仍使用当时上下文；安全导出保留事实 | 混合认证、未知/失效缺口、实际TS规格、重启、导出；登录后无历史补录或普通升级任务 |
| P13，C06/C10/C13 | 沿用本地凭证准备及3项原隔离反例；元数据HTTP403/412为访问受限，不返回空列表，不直接将Cookie标过期。门禁绑定r2、schema5、新增输入/必需用例及85/66/19需求范围 | 缺r2映射不能借r1记录放行；旧schema/计划、缺/失败/重复用例、失配源码/包、短测/原始采样和文档缺项拒绝 |

P6/P11/P13的F-021以命名空间/优先级FIFO保证合法消费者推进，保留原节流与紧急预算；C05/C11反例、当前352个同时并发样本和37个实际双路字节增长区间共同自检。P7/P13的F-022以场次范围和有界SQL预编译缓存减少无关读取，C08/C09计步反例与完整正式热查询317.836ms共同自检；A复核前均保持已修待复核。

`upgrade_candidates/upgrade_members`保存比较快照，pending/rejected变体不参与自动质量选择；默认播放器与正文内嵌图片均排除它们。新候选通过对象实际哈希复验后才在事务中切换；保留两份固定新旧副本，接受后的回收仍检查等待期、固定、共享与读取租约。正文编辑形成新修订并有独立说明，旧修订及资源继续保存；数据没有可比较证据时不伪造提升。

自审发现并修复两项新回归，未改旧断言：第一轮完整Rust运行的 `closeout_faults` 在between_resources窗口产生3个变体，原断言要求2个。修复仅复用同一任务/代次已提交的旧副本，缺认证证据的缓存复核仍为unknown，不给新登录身份虚构下载；六个中断窗口复验通过。第二轮完整Rust运行的 `full_loop` 恢复后显示pending_upgrade而原断言要求unknown；修复同一逻辑请求补齐自身资源时不建立新升级、恢复时失效旧候选，并让质量标签与确认共用有效性检查。原全流程复验通过，新恢复反例保留全部获取历史且拒绝旧确认。两次完整失败及定向复验均保留，不由后来通过覆盖。

本段只记录实现与已经执行的定向自检；最终整套、包和正式测量由下节对应实际输入回执给出。


### 16.9 当前源码自动证据与全部失败保留

最终自动用例回执在 `artifacts/b-m1-r2/final5/`。216项源码/测试/脚本/构建/锁文件及新增规范输入的集合摘要为 `f8a7d480ddb520d2b52f14f52145c4b1b6f1695b033d7e184a8b8f61140f86b7`（按路径排序的JSON、紧凑分隔符、SHA-256）。包manifest、每条命令前后输入及正式测量均按同一集合核对；报告/状态/需求映射/登记另由最终文档指纹绑定。之前 `freeze.json/freeze2.json/freeze3.json/freeze4.json` 和 `final/`、`final2/`、`final3/`、`final4/` 均为过程中实际版本，保留但不加入最终通过聚合。

| 实际命令 | 子命令退出码 / 已执行结果 | 当前回执 |
| --- | --- | --- |
| `cargo build --workspace --locked` | 0；先构建所有生产进程与测试驱动，保留F-001 | `final5/build.json/.log` |
| `cargo fmt --all -- --check` | 0 | `final5/fmt.json/.log` |
| `cargo clippy --workspace --locked --all-targets -- -D warnings` | 0 | `final5/clippy.json/.log` |
| `cargo test -p trace-core --locked c11_ -- --nocapture` | 0；19 passed、0 failed、0 ignored；作为定向report回执，不重复计用例 | `final5/acquisition.json/.log` |
| `cargo test --workspace --locked --no-fail-fast -- --nocapture` | 0；224个完整Rust用例身份，0 failed、0 ignored；包括F-018/F-019/F-020和原全部守护 | `final5/rust.json/.log` |
| `corepack.cmd pnpm -C apps/desktop run verify` | 0；类型检查、48个Vitest用例和生产构建通过 | `final5/desktop.json/.log` |
| `python -m unittest discover -s scripts/tests -v` | 0；16项通过，含原治理、3项凭证准备及r2证据反例 | `final5/python.json/.log` |
| `python scripts/verify.py --all --json` | 0；offline/desktop/recovery/site/resources五套件全部通过 | `final5/verify.json/.log`及所链接逐命令完整输出 |
| `python scripts/package_windows.py` | 0；新目录中的当前生产包 | `final5/package.json/.log`及包manifest的build_commands |
| `python scripts/check_package.py dist/trace-0.1.0-windows-x64-m1-r2-2` | 0；2项实际包检查通过 | `final5/package-smoke.json/.log` |

224 Rust + 48前端 + 16 Python + 2包检查 = **290个唯一用例身份**；原98个必需用例全部保留，r2当前共**133个必需用例**，覆盖C01～C13。按用例身份去重，定向复跑与五套件汇总不再次灌入用例总数。requirements-map的旧m1事实不改写，新增r2映射单列全部85条需求中的66条M1适用范围；其余19条保持not-run。门禁要求每条适用映射有现存实现及真实通过用例，不从r1兜底；NFR还必须满足本次原始测量。

本轮的失败与修复轨迹如下；所有路径相对 `artifacts/b-m1-r2/`，JSON保留子命令真实退出码，包装层源输入不一致时也会失败。中间通过只证明各自输入，不能改绑为final5。

| 原跑次 / 退出码 | 原因与处理 | 复验 |
| --- | --- | --- |
| `f019-baseline` / 101 | 原重叠订阅反例原样失败，确认paused首owner阻断 | `shared-consumers-1` 3/3；生产重叠链及最终Rust通过 |
| `core-first` / 101（101通过、2失败） | 严格直播夹具在claim后绕过接口改策略；全任务矩阵中的无关credential字段被错误施加代次检查。前者改为认领前设置策略、再经真实更新接口验证旧租约失效；后者限制检查到凭证/补全作用域 | 原严格直播与全任务生命周期在最终Rust通过，保留原断言并新增旧租约检查 |
| `acquisition-first` / 101（6通过、1失败） | 新夹具重复插入已存在的quality_policy；改为真实upsert | 后续C11与最终Rust通过 |
| `acquisition-second`、`core-second`、`core-third` / 101 | 新两站生产链的Bilibili合成响应先缺严格质量声明，后DASH使用错误字段名；修正为实际base_url/frame_rate等协议字段，未放宽适配器 | `production-overlap`通过；最终两站字节/认证/旧选择断言通过 |
| `clippy-first` / 101 | 消费者SQL返回tuple复杂度触发严格Clippy；引入内聚类型别名 | `clippy-second/third`及最终严格Clippy通过 |
| `desktop-first` / 2；`desktop-third.log` 启动失败 | 验证入口不支持`--output`；Windows子进程需解析`corepack.cmd`。保留原错误，使用现有stage_evidence输出参数和正确命令 | 后续真实类型/Vitest/build通过 |
| `desktop-fourth` / 2；`desktop-fifth` / 1 | 新播放器夹具缺必填size，随后使用非法blob ID被既有安全过滤拒绝；补齐合法合成数据，不放宽媒体URL校验 | `desktop-sixth/seventh`及最终48项通过 |
| `final/rust` / 101 | between_resources旧窗口出现3个变体而非2个；修复同任务/代次的已提交缓存复用，未知历史仍unknown | `restart-fixed` / 0，六个原窗口和全部原断言通过；最终Rust通过 |
| `final2/rust` / 101 | 原离线全流程恢复后误显示pending_upgrade；区分同一次获取补齐与新升级，并统一失效确认/质量判断 | `full-loop-fixed` / 0；新增待确认备份恢复及最终Rust通过 |
| `final4/benchmark` / 1 | 完整939样本、并发满足，但热查询p95为903.249ms；定位无关直播片段扫描，保留完整失败 | `query-cost-baseline` / 101（60020步），`query-cost-fixed` / 0；最终全套通过，正式measure-final3另行判定 |
| `query-cost-failed`、`query-cost-failed2` / 101 | 新计步夹具遗漏订阅range_json/updated_at必填列；补齐真实schema要求 | 同一计步反例已真实复现旧查询失败，随后修复复验通过 |

当前r2脚本还实际执行旧 `artifacts/m1-final-bundle.json` 的 `check` 与 `verify.py --stage-bundle`，两者均退出1，按预期拒绝旧计划/schema、缺新增用例、失配输入/包和原始测量。原负向回执为 `r1-rejected-current.json/.log` 与 `r1-verify-rejected-current.json/.log`；final4、最终final5输入分别又实际执行两入口，`r1-rejected-final5.json/.log`和`r1-verify-rejected-final5.json/.log`均退出1。预期拒绝的回执保留，不加入最终通过聚合。

首次r2完整自动回执 `final3/` 为222 Rust/48前端/16 Python/2包检查，对应旧r2输入ed0198988b1a2a27b1a75db1018406c4cdc6f4a54ee4bff1a0308724c4628476。其正式测量因F-021未取得同时2下载+2录制样本而中止，保留682个原始样本，不能进入当前通过聚合。`pacing-failed`退出101，紧急顺序4/0/3/2/1；`pacing-fixed`退出0。随后final4新增FIFO/取消/普通等待上界/跨站独立反例，223 Rust/48前端/16 Python/2包自动自检通过；第二次完整长测并发满足，但热查询p95超限，见F-022。当前final5再加入无关直播历史查询步数反例及有界SQL预编译缓存，224 Rust/48前端/16 Python/2包与五套件通过，本次完整正式长测也已通过，原始指标见§16.10；2秒请求间隔、抖动和4次紧急预算未放宽。

Windows链接器的stdout提示、前端隔离路由夹具的未注册路径提示保留完整输出；它们没有代替退出码或断言判断。没有删除测试、增加skip/ignore、改写失败采样或弱化既有值来取得通过。

### 16.10 当前包与正式资源测量

实际当前包为 `dist/trace-0.1.0-windows-x64-m1-r2-2/`，23个文件、208475909字节。`SHA256SUMS.txt`自身摘要为 `b4055f314174e0f7a52c764c6c13813305136d8364bf949b6a1dc6fae67201e8`；全部成员、生产两站注册、媒体资产、构建日志与216项输入均已实际核对，集合摘要为f8a7d480ddb520d2b52f14f52145c4b1b6f1695b033d7e184a8b8f61140f86b7。构建时manifest保留A交接HEAD及dirty状态；最终amend后按每个输入的Git blob字节对应交付提交，不修改manifest伪造构建时提交。

独立WebView测试在新的隔离Profile上通过schema5、认证筛选/升级查询、页面导航、同一后台关闭重连与完全退出；未激活窗口或发送前台键鼠。`artifacts/m1-webview-1791492650801871200/`保留当前包实际结果和截图；与前次r2相同的前端输入保持蓝白外壳、资源升级导航及后台管理入口。它只证明包内页面/控制链；DOM或截图不证明原生解码、声音、中文输入法或系统缩放体验，产品验收仍not-run。

正式当前命令为 `python scripts/measure_m1.py artifacts/b-m1-r2/measure-final3 --idle 1030 --seconds 3620 --entries 100000 --package dist/trace-0.1.0-windows-x64-m1-r2-2`，由 `final5/benchmark.json`记录。规模和时长保持原正式范围：10万发布、50万媒体元数据、正文中位数至少500字符、100普通/20直播目标、100ms HTTP延迟、50行查询、每路至少1MB/s、四路纯录制与同时2下载+2录制，包含GUI打开/关闭/重连、重启与任务恢复。

当前正式复测 **passed，退出0**，命令耗时4742.806秒；937个原始连续样本覆盖4684.984秒，最长采样间隔5.016秒。有效闲置窗口906.157秒、持续录制窗口3621.461秒，同时2下载+2录制样本352个，4个录制起始点、任务错误0。`third-run-transfer-observations.jsonl`另有37个相邻观察区间同时记录两站相同operation的接收字节正增长，证明实际传输；它只是补充诊断，不代替正式原始样本。

| 当前正式指标 | 实测 | 门槛 / 口径 |
| --- | --- | --- |
| 闲置累计CPU均值 / 私有工作集p95 | 0.512315% / 52.152 MiB | ≤1% / ≤150 MiB |
| 四路纯录制CPU均值 / 私有工作集p95 | 2.376916% / 61.746 MiB | ≤5% / ≤300 MiB |
| 热查询p95 / 新连接查询p95 | 317.836 ms / 258.790 ms | ≤500 / ≤1500 ms；未清OS文件缓存 |
| 起录p95 / 任务恢复 | 1.448 s / 11.099 s | ≤10 / ≤30 s |
| GUI关闭释放 | 6.973 s | ≤10 s，同后台打开/关闭/重连 |
| 总接收字节 / 队列峰值 / 句柄峰值 | 15827105203 / 227 / 3833 | 原始计数，不是瞬时带宽或生命周期峰值 |

原始样本为 `artifacts/b-m1-r2/measure-final3/resource-samples.json`，SHA-256 `e12615c7ae788ece5a5ba28deae955e2fc1242e582a90747f0a8345101124a82`。`benchmark.json`的全部派生值已从原始计数逐项重算相等；构建回执、measure二进制、包内GUI、216项输入和完整进程样本均匹配。采样包含Core/合成源、适配器、ffprobe及包内GUI/WebView，退出子进程CPU仍由Windows Job累计；外部Python采样器/CDP驱动属于测量工具。10万发布/50万媒体元数据、512字正文、100/20检查目标、100ms延迟和每路1112866字节/秒的合成负载保持原规模。恢复使用202个未完成普通任务并确认服务可访问；资源结论仅限本机、本次合成负载，原生体验与真实内容仍not-run。

本轮公开脱敏摘要为 [m1-r2-self-check.json](../evidence/gate/m1-r2-self-check.json)，最终本地聚合为 `artifacts/b-m1-r2/final-bundle.json`。`stage_evidence.py check`与`verify.py --stage-bundle`核对源码、全部用例、包、样本和最终文档；最终文档/公开/差异回执为`final-docs.json`、`final-publication.json`、`final-diff.json`及`final-index-diff.json`，均以其真实退出码交付。66条M1适用需求的r2状态/证据已回写，19条后继保留not-run；旧m1对象逐条与A交接相同。

首次r2包 `dist/trace-0.1.0-windows-x64-m1-r2/` 保留23文件/208386817字节，SHA256SUMS自身c580cb0c92bcc661bb5c7999e2d73a0b001d8101d6ae15dde6fa759c688f5ee5。其 `measure-final/` 跑次因F-021中止、结果failed：682个连续样本、录制计时2361.374秒、同时2下载+2录制样本0；退出造成最终恢复/起始汇总缺项，不能伪造补齐。当前复测使用新目录和新包，原字节/失败不改写。

第二次r2包 `dist/trace-0.1.0-windows-x64-m1-r2-1/` 保留23文件/208453381字节，SHA256SUMS自身936a2163cdfe85d4fd9d80b9a6ebe7841806d27a7acd540b2e2a3d999a52c28a；`measure-final2/`完整失败及原始样本摘要只按F-022记录，不能改绑为当前修复。当前新包与第三次测量使用另一目录。

启动前已按原始build摘要将r1和首次失败r2的两个measure二进制分别复制到 `artifacts/b-m1-r2/preserved-r1-measure/` 与 `preserved-failed-r2-measure/`，再由当前源码真实重建target/measure。第二次失败r2的measure二进制也按原始摘要保留在`preserved-second-failed-r2-measure/`。原r1长测、m1-9、closeout-4及其manifest保持不变，不能用它们代替当前结果。

### 16.11 真实边界、守护与整理

B本轮零新增真实站点请求。A两站Cookie正/负对照仍为已有登录有效性依据，未要求用户重复复制。B核查请求UA/Referer、签名、分页及凭证作用域，并补元数据403/412离线访问受限分类；原媒体403重新解析机制保留。实际直播间诊断与Douyin投稿列表仍未取得可用内容，OSS-011保持相应限制；没有把受限响应当空列表、把登录成功当全站可用、虚构720P/1080P原画差异或全部投稿覆盖。具体输入/授权与用户获取步骤只在ACT-002，后继复验须有理由并在既有目标/预算内。

保留审计为 `artifacts/b-m1-r2/test-preservation-final5.json`：A交接的202项Rust名称全部保留，197项函数体词法一致，5项受schema/执行作用域影响的调整逐hunk核对；main基线原109项也全部存在。原A probes、closeout_rework、closeout_faults、生产协议用例、F-018/F-020实现、凭证准备及原3项反例整体与A交接一致；原home只追加新用例，事件和媒体节点断言不变。原报告§13～15、迁移1～4逐字保留。

| 5项旧函数体调整 | 等价或增强依据 |
| --- | --- |
| 导出、feed筛选、质量保留各1项 | 原位置INSERT改为显式列名以兼容新增列；原数据值及32/5/21个断言分别不变 |
| schema3迁移1项 | 成功结果由字面schema4改为当前SCHEMA_VERSION；6个断言及schema3失败回滚/pre-schema-4备份仍保留 |
| 严格直播质量1项 | 初始策略在claim前设置；策略改变走实际subscription.update并要求旧租约拒绝，重新认领后验证宽松回退；原5断言保留并新增2项 |

`preservation-before-final.json`核对旧稳定包closeout-4（20文件/199172866字节）和原m1-9（23文件/207700767字节）的全部成员，以及原验收Profile摘要与Git保护引用；未清理用户目录、原Profile、旧包、失败日志、真实站点结果或本地参考。新增产物仅在忽略的本轮artifacts、新包和构建缓存中；不把秘密、Cookie、真实目标/正文/截图或参考项目私有关系放进Git。公开保留本次必要机器摘要和同一报告，原r1公开摘要保持历史内容。

文档继续按一个事实一个正本同步：需求/同一计划与契约、实现证据/状态、两站能力与OSS-011、用户当前动作、需求映射、索引和登记。没有新依赖或通用框架替换；锁文件、许可和受控媒体资产沿用既定版本。19条后继需求不改成完成，没有把本阶段必须行为登记成后续LOOP。



收尾保留审计曾发现三个历史审计文件名随当前回执编号被误替换，已仅将§13～15内这三处恢复为A交接原名；其余字节原已相同。`history-reference-restoration.json`保留差异，当前`test-preservation-final5.json`重新通过。没有更改历史命令、结果或采样。

<a id="b-to-a-m1-r2-review"></a>
### 16.12 B→A 集中复审 prompt

§16.10的正式测量和当前输入聚合已通过，下面保存完整转交内容，不自动跨聊天发送。最终消息另附实际Git读取的SHA，避免文档递归包含自身提交摘要。

```text
请作为 Agent A，在 TRACE 工作区的 codex/m1-two-site-archive 分支，按唯一计划 docs/delivery/stage-plan.md 的 TRACE-M1-01 r2 做最终集中复核，不创建竞争计划。先读AGENTS、索引、状态、有效决定、事项、USER-ACTIONS和适用规则，核对B最终消息中的实际完整SHA、当前HEAD、工作区归属及用户后续修改。main基点960984c34487244f31f1238dffc2bd9faaa292e7，A本轮交接141d261db8569100564b3f88b897ba4ca00b0f61，原被审B r1为772ab9a6c523a22385f024abafa66aef6ed7f0f1。本阶段相对main仅一个未推送DCO提交；明确局部修复及复核记录amend同一阶段，不改main或备份历史，不push、创建PR、合并、发布、启动子Agent或自动跨聊天发送。

当前F与报告正本为docs/delivery/stage-report.md §16，B r2实现/失败/当前证据从§16.8开始；§13～15及§16.1～16.7保留原r1与A交接时事实。F-018来源长度与F-020 Douyin self登录探测保持A已核实修复；F-019/F-021/F-022均仅标“已修待复核”，请根据实际代码和反例核实后关闭，不从B自检自动推定A通过。

优先复核F-019：process_scan_page和persist_detail的同键observe/media不再由首订阅永久占有；entry_subscriptions保存有证据的发现，job_consumers隔离每个消费者的需求序号/失败预算，认领冻结订阅版本/凭证代次/质量，现有有效租约不被抢占。queued/running/paused/succeeded首owner、不同凭证和策略、暂停立即恢复、取消、停模块、删除、重启、迟到结果都须同时守住两条链。原复现命令cargo test -p trace-core --locked overlapping_subscription_can_continue_same_entry_after_owner_pause -- --nocapture已从原失败复验通过；原失败在artifacts/a-m1-review/subscription-final2.json/.log及artifacts/b-m1-r2/f019-baseline.json/.log，不得覆盖。检查新的生产两站重叠订阅字节链，不能只看状态矩阵。

按P11～P13/C11～C13复核：每次获取有不可变anonymous/authenticated/expired/unknown观察；本批次同凭证代次的生产登录证明与资源获取关联；已认证解析后的CDN请求不带Cookie仍保留该解析身份，匿名回退另建上下文。同blob多次获取独立，旧schema3/4通过迁移5保留unknown，迁移1～4未改。认证筛选、分页、计数、定位、逐资源详情共用稳定entry身份，混合不被一个附件覆盖。

登录验证后普通资源补全有固定发现上界、每次16条及2秒续行间隔，持久/去重且排除直播/删除/抑制条目。完整最高可得候选进入统一资源升级页，用户逐项或明确选择范围批量接受/拒绝/保留两份；候选与旧选择隔离，确认绑定完整旧选择/固定状态摘要、正文修订、媒体/订阅/凭证/任务代次和质量。实际对象哈希复验后在事务内切换。缺资源、失败、无提升、正文编辑、不可比较、旧点击、重复确认、取消/停模块/删除、另一窗口改变选择和恢复都需语义核对；旧资源、固定/共享引用和回收等待期不受损。默认播放器和正文图片不能自动选待确认/已拒绝候选。

新增F-021来自第一次r2正式测量：同类元数据调用竞争导致observe/media长期running而无字节接收，mixed同时2下载+2录制样本0。该682样本失败跑次保留在measure-final，旧r2包也保留。当前用Tokio FIFO Mutex按命名空间/优先级排队，保留原最小间隔/抖动/4次紧急预算；新增反例先失败后通过，取消释放排位、普通请求有界推进且跨站独立。F-019和F-021均仅“已修待复核”，请核对当前measure-final3确有并发样本和完整时长后关闭，不以单元FIFO或第一次部分长测代填。

新增F-022来自第二次完整长测：939样本、354个同时2下载+2录制样本、任务错误0，但热查询p95为903.249ms超过500ms。measure-final2及final4/benchmark原失败不改写。quality::state曾从全库直播片段开始扫描，无场次条目也遍历一万片；计步反例先在60020步失败，改为先限定条目场次后通过。列表/质量/认证查询只缓存64项SQL预编译代码，每次重读实际状态；严格缺口与实际降级、其他场次不污染、实时状态改变均有断言。先核对当前正式第三次结果，再决定关闭F-022，不用单元步数代替500ms门槛。

重点看两项B自审回归的实质修复，原断言没有改：final/rust的between_resources曾多生成一个变体，现只允许同一任务/代次复核自身已提交的旧副本，缺认证证据仍unknown/cache_verified，不为新登录身份虚构下载。final2/rust的full_loop曾在恢复后误显示pending_upgrade，现区分同一请求补齐与新的升级请求，恢复失效候选，质量状态和确认共用比较有效性。定向restart-fixed/full-loop-fixed和最终Rust通过。查新C11旧缓存与待确认备份恢复反例，勿以改期望值代替修复。

直播按片记录当时获取身份及实际分辨率、码率、帧率、编码；无法探测、混合和gap保持明确，封片恢复/导出保留原上下文。登录后旧直播零补录、零进入普通升级队列。原FLV/HLS、独立音轨、MAP/KEY/BYTERANGE、四个提交窗口与原F守护保持。

当前216项输入的集合SHA256为f8a7d480ddb520d2b52f14f52145c4b1b6f1695b033d7e184a8b8f61140f86b7。artifacts/b-m1-r2/final5下rust=224、desktop=48、python=16、package-smoke=2，合计290个唯一用例身份；133个必需用例保留原98并覆盖C01～C13。build/fmt/clippy/verify/package实际回执均在该目录，五套件5/5。新包dist/trace-0.1.0-windows-x64-m1-r2-2有23文件、208475909字节，SHA256SUMS.txt自身摘要b4055f314174e0f7a52c764c6c13813305136d8364bf949b6a1dc6fae67201e8。核对所有输入的磁盘/Git blob/回执/manifest字节，不能仅凭测试总数或旧HEAD字段判断。

正式新测量在artifacts/b-m1-r2/measure-final3，回执final5/benchmark.json；命令参数idle=1030、seconds=3620、entries=100000，配当前m1-r2-2包。检查原始连续样本、所有相关进程累计CPU/私有工作集/句柄、50万媒体元数据、512字正文、100普通/20直播检查、100ms延迟、50行查询、四路纯录制与同时2下载+2录制、GUI同后台打开/关闭/重连及恢复。脚本实际重建了当前measure程序，原r1二进制已按摘要复制到preserved-r1-measure，原采样未换绑。正式结果已通过：937样本、闲置906.157秒、录制3621.461秒、同时2下载+2录制样本352个、任务错误0，热查询p95 317.836ms；另有两站实际接收字节同步增长诊断。完整派生值/限制见报告16.10；未完成、失败、缺样本或源码已变都必须拒绝。

先执行python scripts/stage_evidence.py check artifacts/b-m1-r2/final-bundle.json及python scripts/verify.py --stage-bundle artifacts/b-m1-r2/final-bundle.json，读取docs/evidence/gate/m1-r2-self-check.json与requirements-map.json的独立r2对象；全部85条中66条M1适用须有真实证据，19条后继仍not-run。原r1/schema4/199输入/255用例/98必需/m1-9及长测仅证明历史版本；当前门禁已实际拒绝旧bundle，负回执保留。先核对仍匹配的有效证据，遇改动、失败或具体疑虑再做相关复验，不机械重复已匹配的全套或正式长测。

保留test-preservation-final5审计：A交接202项Rust全在、197项体词法一致，5项是显式列名/schema常量/真实策略更新兼容，原断言不减；main原109项也全在。原A Rust probes、closeout_rework/closeout_faults、生产协议用例、F-018/F-020和3项本地凭证反例整体未动，home只增加测试。旧closeout-4、artifacts/closeout/acceptance、m1-9、原正式材料/失败和本地参考全部保留。按文件归属审查，不清理用户资料。

真实边界只看USER-ACTIONS ACT-002：A已证明两份Cookie在同登录端点有效，无需用户重新复制；本轮B零新增真实请求。原直播间诊断HTTP412和Douyin已登录列表HTTP403仍受限。B已核查请求条件并补元数据PermissionDenied离线分类，不能当空列表/直接Cookie过期，也不能把登录成功当全站下载可用或虚构720P/1080P原画/全部投稿。OSS-011后续只有在有具体原因、现有对象/预算和停止条件内才真实复验，不绕过挑战或扩对象。配置仍只在忽略的.env.local/.local；秘密不入日志/参数/包/导出/Git。

原生检查使用隔离Profile、独立WebView、回环CDP、不激活窗口，不操作用户浏览器或前台键鼠。现有自动包截图/DOM/currentTime不代替实际解码、声音、IME、缩放与外部播放器体验；产品验收和许可/发布分别保持真实状态。开发/上游只在事项总表，用户当前动作只在USER-ACTIONS，非阻塞LOOP只在状态维护。

A直接修复明确局部问题并定向复验，将复核/修复和DCO归入同一未推送阶段提交；只有较大实现缺口才一次性交回全部稳定F编号、复现、依据与验收。A核实后更新同一报告/状态/事项，分别给出B实现、自检、A技术复核、产品验收和发布结论；通过后直接向用户收尾及说明下一步，不要求B重复复审。如A修改源码、测试或门禁中的规范输入，原包/长测仍只证明修改前版本，须按当前门禁产生匹配的新证据，不能改绑；仅审查文档变化则重做文档检查与聚合。最终SHA从实际Git读取提供。
```


<a id="a-m1-r2-review"></a>
### 16.13 A r2 最终集中复核（2026-10-09）

**结论：failed / rework-required。** 本轮被审B提交为 `b670e3b7aee88bb72e4369c5d3bab0160c460cf2`，main基点 `960984c34487244f31f1238dffc2bd9faaa292e7`，上一轮A交接 `141d261db8569100564b3f88b897ba4ca00b0f61`。开始时HEAD、分支、工作树与索引符合交付，相对main只有一个带DCO的未推送阶段提交。A已独立核实关闭F-019/F-021/F-022的原故障范围，F-018/F-020保持已核实；新增[F-023](#f-023)、[F-024](#f-024)、[F-025](#f-025)三个P1反例阻止本阶段技术通过。它们不是新增产品要求，也不转交用户人工验收。

本轮仅修改审查、状态、索引、需求审查映射和必要公开摘要；生产源码、规范测试、锁文件、唯一计划及216项规范输入均未变。独立故障crate保存在忽略的 `artifacts/a-m1-r2-review/probes/`，使用当前Core公共服务和隔离合成Profile。三个问题跨共享请求交接、候选确认、恢复选源及物化重入，需要一致的请求归属和批准边界；仅放宽代次或commit保护会破坏已有保障，因此一次性集中交B实现，不以局部绕过制造通过。最终审查记录amend同一未推送阶段并DCO签名；最终SHA从Git读取，在交付消息及本地 `artifacts/a-m1-r2-review/git-delivery.json` 给出，不递归写入本报告。

#### 输入、包、历史与原始资源核对

首先实际执行 `python scripts/stage_evidence.py check artifacts/b-m1-r2/final-bundle.json` 和 `python scripts/verify.py --stage-bundle artifacts/b-m1-r2/final-bundle.json`，修改文档前均退出0。A的 `baseline_audit.py` 独立比较216项Git blob、磁盘、命令回执、公开摘要及manifest，全部一致，集合SHA-256为 `f8a7d480ddb520d2b52f14f52145c4b1b6f1695b033d7e184a8b8f61140f86b7`。B的290个用例身份唯一，133个必需用例保留原98项；85条需求中66条M1适用、19条后继not-run。原m1映射保留，r2的B自检字段与A审查字段分别维护。

当前包 `dist/trace-0.1.0-windows-x64-m1-r2-2/` 的实际库存为23文件、208475909字节；所有成员摘要和构建回执匹配，SHA256SUMS自身摘要 `b4055f314174e0f7a52c764c6c13813305136d8364bf949b6a1dc6fae67201e8`，manifest摘要 `45a0f6a607be999be0c1d18bbde4df42e107080d67ec6724cce8e3f7ccc1c096`。manifest保留真实构建时A交接HEAD/dirty，不改成最终amend后SHA。A未重建包或measure程序；当前二进制及r1、两次失败r2的preserved副本均逐摘要核对，不换绑。

`independent_metrics.py`直接读取937个连续原始样本，独立复算全部18项派生指标，不调用B的derive函数。原始样本摘要仍为 `e12615c7ae788ece5a5ba28deae955e2fc1242e582a90747f0a8345101124a82`，结果全部与B回执一致：

| A独立核对项 | 实际值及结论 |
| --- | --- |
| 连续采样 / 闲置 / 录制 | 4684.984秒，最大间隔5.016秒；有效闲置906.157秒、录制3621.461秒，满足时长 |
| 实际并发与接收 | 同时至少2下载+2录制352个样本；纯录制窗口350个样本均为4录制；另有37个相邻区间两站相同operation的已接收字节同时正增长，任务错误0 |
| CPU / 私有工作集 | 闲置均值0.512315% / p95 52.152 MiB；四路纯录制均值2.376916% / p95 61.746 MiB，全部满足门槛 |
| 查询 | 热查询p95 317.8358ms；新Core连接查询p95 258.7901ms，未清OS文件缓存；均满足500/1500ms门槛 |
| 起录 / 恢复 / GUI | 起录p95 1.447991秒；202个未完成普通任务的恢复11.099277秒且服务可访问；GUI关闭6.972970秒，两次连接同一后台实例 |
| 负载与统计边界 | 10万发布、50万媒体元数据、512字正文、100普通/20直播检查目标、100ms延迟、50行查询、每流1112866字节/秒；统计包含相关进程及退出子进程的累计CPU，限于本机合成负载 |

原失败 `measure-final` 的682样本/并发0，以及 `measure-final2` 的939样本/热查询903.249ms均保留；本次通过不改写原失败。A核对 `test-preservation-final5.json` 的202项A交接Rust测试及main原109项全在；197项函数体词法一致，5项兼容/增强调整逐hunk核对，原断言未减。原A probes、closeout_rework/closeout_faults、生产协议用例、F-018/F-020与3项凭证准备反例保留；home事件与媒体节点断言保留。§13～15逐字节不变，迁移1～4原样；11份已退役过程材料的提炼和完整SHA入链抽查有效。旧closeout-4/原验收Profile、m1-9、原采样/失败、本地参考及用户配置不清理。

#### 逐C语义复核

| P / C | A核对方式与结论 |
| --- | --- |
| P1～P4/P6/P11；C01 身份/分页/编辑 | 核对生产扫描、详情、发现关系和消费者调和；复跑原F-019及C11的两站生产adapter→HTTP字节链，确认其他订阅暂停时活跃消费者继续，凭证/质量与失败预算隔离。原故障关闭；候选交接问题另列F-023 |
| P2～P4；C02 正文/必要附件 | 复核仍匹配的生产协议/全文与附件断言，Douyin type163详情对象/JSON字符串、30图及必要音轨不退化成列表摘要；长度F-018实现未变。技术证据通过，真实全类型内容not-run |
| P3/P4/P8；C03 长度/摘要/重入 | 原HTTP11组、独立分段/整组摘要、大流/ETag及首资源提交恢复证据有效；补充跨逻辑请求OBJECT_READY反例失败，F-025揭示原单请求重开通过未覆盖的新路径 |
| P5/P12；C04/C12 录制与身份 | 核对FLV/HLS/独立音轨/MAP/KEY/BYTERANGE、四提交窗口守护及逐片认证/实际规格；定向复跑C12通过。unknown/mixed/gap和原上下文保留，登录不补录旧直播；原生解码/声音不据此宣称通过 |
| P6/P11/P12；C05 控制与代次 | C11共享消费者控制、失败预算、暂停即恢复、取消/停模块/删除/重启/迟到拒绝通过；已完成候选随正常交接失效及新补全误取消分别为F-023/F-025，不能用控制矩阵通过替代这两条链 |
| P1/P3～P6/P13；C06 网络/秘密 | 核对逐跳DNS/IP、嵌套HLS、CDN头隔离、安全元数据/错误分类；生产认证探测与同代次获取、匿名回退的C11定向用例通过。403/412不写空列表或Cookie过期；本轮零真实请求 |
| P2/P8/P11/P12；C07 迁移/恢复/质量 | schema3/4→5、旧库unknown/不可变历史、备份恢复、旧缓存不虚构新身份及待确认恢复失效的C11用例通过。另加删除恢复反例却选中rejected资源，F-024；跨请求准备记录重入失败F-025，本组整体未通过 |
| P7/P11/P12；C08 UI/IPC | 核对认证筛选/分页/计数/定位共用entry及逐资源详情、保留的事件/媒体节点/竞态断言；48项前端证据有效，A定向查询成本守护通过。F-024证实Core选择错误，未声称UI/原生播放器已播放该资源；原生体验not-run |
| P9/P13；C09 包/资源 | Git/磁盘/manifest/回执的216输入、实际包库存与全部摘要、原始负载/并发/进程指标和恢复独立复核通过；F-021/F-022据实际第三次正式结果关闭，非用单元FIFO/步数替代资源门槛 |
| P9/P13；C10 门禁 | 原B bundle在审查修改前两个入口通过；核对旧r1/缺用例/失败/陈旧/坏包和短测拒绝断言。本轮将A实际失败回执纳入新聚合，技术失败必须使聚合拒绝；原B bundle不改写 |
| P11/P12；C11 获取/升级 | 19项原C11定向通过，证明现有断言范围；新增三个跨场景反例0/3通过。F-023～025为本轮必须行为缺口，整体failed；不能只看测试总数、旧选择未丢或commit最后拒绝就放行 |
| P13；C13 本地前置/证据 | 本地配置入口及3项凭证准备反例与A交接字节相同，16项Python和r2/schema5/85需求门禁证据有效。ACT-002已有正负登录证明继续保留；不重复索取Cookie、不扩展真实对象或授权 |

#### A实际定向命令与局限

下列新回执统一在 `artifacts/a-m1-r2-review/`；Rust真实子命令退出码记录在JSON，失败包装入口退出1，原输出不覆盖。

| 实际命令 | 结果 | 回执/原始日志 |
| --- | --- | --- |
| `cargo test -p trace-core --locked overlapping_subscription_can_continue_same_entry_after_owner_pause -- --nocapture` | 0，1 passed | `f019-original.json/.log`；旧A及B baseline失败继续保留 |
| `cargo test -p trace-core --locked c11_ -- --nocapture` | 0，19 passed | `c11-directed.json/.log`；含生产两站字节链/身份、控制、确认、迁移恢复 |
| `cargo test -p trace-core --locked c08_live_quality_query_ignores_unrelated_recording_fragments -- --nocapture` | 0，1 passed | `query-cost.json/.log` |
| `cargo test -p trace-core --locked c12_fragment_authentication_actual_specs_gaps_restart_export_and_no_backfill -- --nocapture` | 0，1 passed | `live-auth.json/.log` |
| `python artifacts/a-m1-r2-review/baseline_audit.py` | 0，版本/输入/库存/历史独立核对通过 | `baseline-audit.json`；仅初始干净B HEAD的审计，不在文档修改后冒充相同现场 |
| `python artifacts/a-m1-r2-review/independent_metrics.py` | 0，18项派生值及保存二进制核对通过 | `independent-metrics.json` |
| `cargo test --manifest-path artifacts/a-m1-r2-review/probes/Cargo.toml --locked --offline --target-dir target --no-fail-fast -- --nocapture` | 101，0 passed / 3 failed / 0 ignored | `independent-probes-final2.json/.log`；F-023/024/025；初始两项与中间三项失败均保留 |

独立反例只使用合成元数据、实写对象和当前公共Core服务。F-025按生产物化ID公式复现对象/任务交互，不把它描述为完整真实站点下载；F-024证明持久选择越过明确拒绝，旧字节仍在，不夸大为数据丢失或实际播放。B完整224 Rust、48 desktop、16 Python、2 package-smoke及fmt/Clippy/build/五套件证据仍对同一规范输入有效；A按具体疑虑新增/复跑，不机械重跑全套、包或80分钟长测。

两站Cookie在2026-10-08 22:48（上海）由A完成同端点带/不带凭证对照；本轮未新增真实请求。原Bilibili诊断HTTP412和Douyin已登录列表HTTP403仍受限，登录成功不证明720P/1080P原画、全部投稿或真实录制可用。OSS-011后继只按既有对象/预算、具体原因和停止条件处理；凭证、真实原文与目标不进公开材料。原生IME/缩放/实际音视频/外部播放器、真实全类型验收、Linux/安装器/签名/发布保持not-run；用户动作只见ACT-002/003与Q-004。

| 独立结论 | 当前结果 |
| --- | --- |
| B实现交付 | 已交付r2实现与证据；发现必须修复的F-023～025，阶段仍rework-required |
| B自动自检 | passed，仅限290个原用例及其对应构建/包/资源范围，不代替A新增交叉反例 |
| A技术复核 | failed；F-019/F-021/F-022原范围已核实关闭，F-018/F-020保持；三个新P1未解决 |
| 产品验收 | not-run；开发缺口先由Agent处理，现有自动DOM/currentTime与资源跑次不代替原生体验 |
| 远端与发布 | 本阶段无授权且未执行push、PR、合并或发布；main与备份历史保持 |

公开脱敏摘要见 [m1-r2-a-review.json](../evidence/gate/m1-r2-a-review.json)，包含独立反例源码/锁文件/日志摘要及复核边界。原B `final-bundle.json`保留审查前文档指纹；当前审查另聚合到 `artifacts/a-m1-r2-review/final-bundle.json`，纳入失败的report-kind回执，既不重复计入B已有用例，也不掩盖实际101。审查新增测试尚在忽略目录，B须将同等断言纳入规范输入和必需门禁后修复。

收尾文档预检退出0：49份Markdown、525个本地链接、85需求、28AC、12INV、18ADR，零错误；公开内容预检退出0、零发现，工作树及索引diff检查均退出0。原B bundle在本轮文档修改后检查退出1，唯一原因是文档指纹陈旧，`old-bundle-after-docs.json/.log`保留该结果；216项规范输入及原包/长测仍匹配。

A第一次聚合预检误将 `final5/benchmark.json` 的执行回执传给需要派生指标的 `--benchmark` 参数，因缺指标等项被拒绝；原 `bundle-preview.json`、`bundle-preview-result.json/.log`及初始辅助脚本保留，这是A聚合命令输入错误，不是新产品故障。改用原始 `measure-final3/benchmark.json` 后，`bundle-preview2.json`仅以 `failed command a-r2-independent-probes-final2` 拒绝，其余源码/用例/包/采样匹配；没有改写旧benchmark或屏蔽失败。

收尾同命令的新回执为 `docs-final.json`、`publication-final.json`、`diff-final.json`、`index-diff-final.json`，归属/历史/包保留审计为 `precommit-audit.json`及 `precommit-audit-data.json`。最终聚合用 `python artifacts/a-m1-r2-review/assemble_review.py final`，并实际执行 `python scripts/stage_evidence.py check artifacts/a-m1-r2-review/final-bundle.json` 与 `python scripts/verify.py --stage-bundle artifacts/a-m1-r2-review/final-bundle.json`；输出分别保存在 `final-bundle-result.json/.log`、`final-bundle-check.json/.log`、`final-bundle-verify.json/.log`。这些入口按实际三个失败拒绝交付；文档通过或B原290用例绿色均不能覆盖A结论。

<a id="a-to-b-m1-rework-r2"></a>
### 16.14 A→B 原r2集中返工 prompt（历史）

下段保留当时r2实现交接；DEC-028追加真实链接要求后的当前集中入口为[§16.17](#a-to-b-m1-rework)。F详情/故障原样在§16.3，原B实现与失败在§16.8～16.12，不将本历史文本作为竞争计划。

```text
请作为Agent B，在TRACE工作区的codex/m1-two-site-archive分支，按唯一计划docs/delivery/stage-plan.md的TRACE-M1-01 r2，集中修复A最终复核的F-023/F-024/F-025三个P1。先读AGENTS、索引、状态、有效决定、事项、USER-ACTIONS及适用规则，核对用户附带的最终A完整SHA、工作区归属和后续用户修改。main基点960984c34487244f31f1238dffc2bd9faaa292e7；原被审B r2是b670e3b7aee88bb72e4369c5d3bab0160c460cf2，上一轮A交接141d261db8569100564b3f88b897ba4ca00b0f61。当前只有一个未推送DCO阶段提交。沿用同一计划/报告，归并实现、返工和审查为同一提交；不改main/备份历史，不push、创建PR、合并、发布、启动子Agent或自动跨聊天发送。

先看docs/delivery/stage-report.md §16.3、§16.13及docs/evidence/gate/m1-r2-a-review.json。F-018/F-020保持A已核实；F-019共享消费者原故障、F-021公平排队、F-022查询成本已由A用生产链/定向反例和正式原始样本核实关闭，不重开无变化范围。原失败均保留。

原三项故障可一起复现：cargo test --manifest-path artifacts/a-m1-r2-review/probes/Cargo.toml --locked --offline --target-dir target --no-fail-fast -- --nocapture。当前子命令101，0 passed/3 failed/0 ignored；原回执independent-probes-final2.json/.log，独立crate的Cargo.toml/Cargo.lock/src/lib.rs摘要在公开A摘要中。先保留原失败和探针，再把等价故障断言纳入仓库规范测试/必需门禁，不能只调整fixture或放宽期望。

F-023/P1：两个不同凭证的启用订阅共享media任务。已有匿名低清，第一个消费者完整取得高清候选并finish为pending；正常succeed使第二消费者待运行，尚无新字节，第一候选已被upgrade.list永久标stale/comparison_scope_changed。位置jobs/consumers.rs::finish/reconcile与upgrades.rs::snapshot/confirmation_current/invalidate_stale。把共享执行租约与已完成消费者的确认范围分开，正常交接不废弃仍有效候选；同消费者真正取消、订阅/凭证/质量/正文/旧选择变化仍拒绝旧确认。覆盖后继消费者相同、较低、无提升、失败和新候选；不能简单移除代次屏障。

F-024/P1：旧低清automatic选中，高清候选已明确Reject；copies删除后立即restore，asset_selections变为high/rejected，quality::evaluate也不修正。位置deletion.rs::restore及quality.rs::evaluate。恢复/自动选择/派生使用须保持审批边界，选原合格副本或无选择，不把恢复等同接受pending/rejected。覆盖copies/archive、pending/rejected、无旧版、固定/共享/已回收副本、后续质量维护/播放派生/导出。反例仅证明错误Core选择，旧字节未丢，原UI过滤仍在；修复不能删除原历史候选或改成自动接受。

F-025/P1：旧completion-media任务代次1的相同候选已OBJECT_READY后取消；新凭证代次产生另一个job_id，任务代次仍1。acquisition_operation只用media+任务代次+候选摘要导致同ID；uncommitted_operation不避开旧未提交记录，ObjectStore::ready错误返回true，commit正确因不同job_id拒绝Cancelled，正常失败处理把新任务cancelled。位置core.rs::acquisition_operation/uncommitted_operation及objects/mod.rs::ready/start_receiving/commit。物化身份按逻辑请求可重入且隔离，ready/重用/重置核对归属；保留commit屏障，同任务恢复不多建变体，不给新登录身份虚构旧下载。覆盖不同补全任务、普通任务交错、凭证换代以及RECEIVING/PREPARED/OBJECT_READY/首资源提交后的重开。

三项需连同旧缓存between_resources与待确认备份恢复守护验证，不能让修一处又破坏另一处。保留原A probes、closeout_rework/closeout_faults、生产协议用例、F-001～022历史与原断言、home事件/媒体节点、蓝白主页/管理入口、§13～15原字节、迁移1～4、原m1需求对象。本轮A未改216项规范输入；当前指纹f8a7d480ddb520d2b52f14f52145c4b1b6f1695b033d7e184a8b8f61140f86b7，对应B final5的290唯一/133必需和m1-r2-2包/measure-final3，只证明修复前版本。

A独立复算当前正式跑次937连续样本、闲置906.157秒、录制3621.461秒、混合352样本、37区间两站实际字节同时增长、任务错误0、热查询p95 317.8358ms、恢复11.099277秒。原失败两次长测及所有preserved measure二进制不改绑。你修改规范源码/测试/门禁后，真实运行匹配当前输入的相关旧/新测试、静态检查、构建、新包及当前门禁所需正式资源测量和聚合，不能仅改旧回执摘要。重建target/measure前保护按原摘要保存的各版本；旧包closeout-4/原验收Profile、m1-9、三次r2包和所有原始失败保留。当前A聚合因三个真实反例退出非零是正确拒绝，不通过修改聚合器去屏蔽失败。

两站Cookie已由A在同登录端点完成正负对照，不重复索取；原Bilibili诊断412与Douyin已登录列表403仍受限。真实请求只按USER-ACTIONS ACT-002既有对象/预算、有具体原因时复验，遇挑战停止；不以登录成功声明全站可下载，不虚构720P/1080P原画或全部投稿。秘密不入Git/日志/参数/包/导出。本轮A零新增真实请求，原生IME/缩放/实际解码/声音/外部播放器和产品验收仍not-run。

连续完成集中实现与自检，F只标已修待A复核；同一报告保留原失败、逐F定位/修复/新反例和实际命令，同步状态/事项/独立r2需求审查范围。用户当前动作只在USER-ACTIONS，代码缺口不得转成用户待办或后继LOOP。最终提交DCO并amend同阶段，实际Git读取最终完整SHA。提供一次B→A集中复审prompt；A通过后直接向用户交付，不要求B再审A。
```


<a id="m1-user-feedback-20261009"></a>
### 16.15 用户视觉确认、项目许可选择与真实功能边界（2026-10-09）

用户本次明确反馈“ACT-004 已完成，没什么问题”，并确定项目采用Apache-2.0。当前用户事项只回写[USER-ACTIONS](../../USER-ACTIONS.md)，长期许可选择记为DEC-027，根[LICENSE](../../LICENSE)保存标准Apache License 2.0完整正文（SHA-256 `cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30`）。第三方依赖、已采用源码与媒体资产继续保留各自许可和通知；未引入新依赖、额外贡献协议或对外动作授权。

本次仅落实项目许可正文与文档，没有修改产品实现、构建配置、规范测试或216项源码输入。原包不改写、不补造新库存；后继B因F-023～025返工产生的新包应随附根LICENSE及既有第三方/媒体法律材料，并验证清单完整。原B自检与A反例仍适用相同源码，整体技术状态保持rework-required；原A复核及用户确认属于不同证据范围。

| 范围 | 当前真实事实 |
| --- | --- |
| ACT-004主页视觉 | 用户确认完成，限定为主页视觉/布局反馈，详见用户事项正本 |
| 两站生产下载/录制代码 | 已有生产适配及Core接线，合成协议/故障/资源测试已有证据；F-023～025仍待修复 |
| Bilibili真实视频下载 / 直播录制 | 均not-run；登录正负对照完成，房间身份诊断HTTP412后停止，没有进入实际媒体接收/录制 |
| Douyin真实视频下载 / 直播录制 | 均not-run；登录正负对照完成，已登录投稿列表HTTP403后停止，没有实际下载或录制 |
| 清晰度及覆盖 | 未证实匿名/登录的720P、1080P、原画差异，也未证实全部投稿覆盖；不把登录有效或合成长测当真实内容验收 |
| 产品与发布 | ACT-003整体原生验收仍not-run；许可选择已明确，对外发布/推送/PR/合并没有新增授权 |

本次答复不触发新的真实站点请求。后继仍由开发者处理代码缺口和具体请求条件；只有实际缺少或失效的输入才交用户配置，不重复索取已有Cookie。真实验证保持ACT-002的具体对象、预算与停止条件，不因本次状态询问扩大范围。

收尾沿用同一M1未推送DCO提交。新增文档聚合保存在 `artifacts/a-m1-user-feedback-20261009/final-bundle.json`，原A/B聚合和失败均保留；只有文档指纹与新增许可检查更新，三个实际失败继续阻止技术放行。最终SHA从Git读取记录在该目录的 `git-delivery.json` 和交付消息。

本次文档预检退出0：49份Markdown、543个本地链接、85需求、28AC，零错误；公开检查退出0、零发现。`license-check.json/.log`确认Apache-2.0正文、ACT-004记录、原报告完整保留及216项规范输入未变。收尾重新运行同一文档/公开/工作树和索引差异检查，回执为该目录的`docs-final`、`publication-final`、`diff-final`、`index-diff-final`；`assemble_feedback.py`及两个stage-bundle入口继续因原A失败退出1，原输出另存，不修改旧聚合。

<a id="m1-real-source-readiness"></a>
### 16.16 A真实来源诊断、局部修复与M1 r3退出条件（2026-10-09）

用户在§16.15之后明确要求本阶段完成“视频地址→清晰度选项→下载”和“真实直播地址→720P以上录制”，并提供两站各一视频及一直播地址。A按新授权继续，既有本机Cookie保持原样；启动本次续行时HEAD为`8f7e2c9f1d4419016e7553790c050a3f0236af9e`，相对main一提交且跟踪工作区干净。新要求回写DEC-028和同一计划TRACE-M1-01 r3，新增FR-ARC-009、FR-LIVE-008、AC-29/30及P14/C14；当前87条需求中68条M1适用、19条后继。原m1/r2需求对象保留历史，r3独立表达待实现/复验范围，不另建计划。

#### 请求原因与参考结论

按仓库外索引只读核对两站相关解析、Cookie、受保护端点和直播协议实现；未改动参考工程、用户浏览器或系统代理，也未复制GPL代码。公开只记录TRACE自己的协议/实测结论，具体参考路径、版本和阅读范围留在本机审计。沿用§16.5逐文档流程核对，本次复读受影响的A/B职责、阶段单提交、授权、质量与文档规则；差异落到本计划、需求/契约、状态/事项和USER-ACTIONS，不复制外部技术栈或操作权限。

| 问题 | 本次实际对照 | 可得结论与边界 |
| --- | --- | --- |
| 原Bilibili房间诊断412 | 同一旧room_init端点、同一环境代理、匿名1请求：标准库默认头412；另一个独立跑次补浏览器UA和直播Referer后200/业务0/正在直播 | 原诊断的请求头组合是可复现差异，不是Cookie失效证明；没有进一步把差异归为单独某一头或所有房间固定规则。原412仍保留，不推定当时生产Core已录制失败 |
| Bilibili WBI | 实际源码64字符/重复wts；正确独立32字符向量对旧源码失败，对A修复后通过 | F-026源码范围关闭；与room_init请求头诊断分别记录。现代直播getInfoByRoom/getRoomPlayInfo采用正确WBI、直播头和已验证身份时取得真实流 |
| Douyin详情403 | 同一生产适配器和Cookie，self登录成功、detail返回403/uifid_missing；在隔离代理补SecSDK上下文后200，81,416字节，生产适配器解析成功 | F-027是访问签名上下文缺口，不要求用户重新复制Cookie；Core生产接线尚待实现 |
| Douyin投稿403 | 同Cookie/self成功，原post返回403/uifid_missing；补上下文后200、20条、has_more=1，1,454,679字节 | 已确认原投稿端点缺少该上下文；这页随后受F-029的大小/帧预算阻断，不能说已完整归档或全历史可用 |
| 直播URL/媒体 | 指定Douyin web_rid经web enter得到流；Bilibili媒体重定向到bytefcdnrd.com，Douyin FLV来源为HTTP douyinliving.com，同地址HTTPS接收成功 | 当前生产入口/默认媒体策略仍需P14/F-028；必须保留逐跳公共地址、TLS及媒体零Cookie，不能靠用户填测试白名单完成支持 |

Douyin所需访问者ID/指纹已在用户提供的Cookie中；无msToken仍完成本次受保护请求，不生成伪造token。登录来自self/nav证据，访问者ID和签名不当登录。隔离助手只在受保护路径补参数/签名，详情与投稿均保持原业务参数及A-Bogus；未来生产接线必须由Core保持代次和秘密隔离，不能照搬助手的宽响应转发。

A隔离HTTP助手起初因本机DNS返回测试网段而拒绝连接，后按公开DNS答复逐地址验证并固定实际IP、保持TLS主机名验证。本机Core的地址分类并未同样拒绝该网段，不能将助手的拒绝写成原412/403的Core原因；没有修改系统网络配置。Bilibili旧请求头对照仍走原标准库环境代理，以免混淆这两个条件。此诊断网络路径也不是当前产品网络栈已通过的证据。

#### 实际保存的四份媒体

全部目标来自本次用户给定链接；源解析前的同身份登录证明已取得，媒体CDN请求没有Cookie/Authorization。实收媒体累计Bilibili **370,097,957字节**、Douyin **169,588,348字节**，均在每站512MiB诊断预算内；每站直播约60秒接收。原始响应、目标、签名地址和媒体只留本机`.local/site-check/`，不进入Git、普通日志或包。

| 样本 | 实际接收/保存 | 本地探测和完整解码 | SHA-256 |
| --- | --- | --- | --- |
| Bilibili视频 | 完整4K AVC视频344,068,682字节，约32.8秒接收；必要AAC音轨5,385,435字节，stream-copy后349,599,098字节 | 3840×2160，约29.97fps，AAC双声道，218.559667秒；完整自动解码退出0 | `78b85814c51e7afbe867cba4dced7b897d71638a563170a35f27f3af4c2ffe80`（含音轨MP4） |
| Douyin视频 | 完整9,352,828字节，HTTP长度与来源data_size均一致 | 1080×1920，30fps，AAC双声道，22.833333秒；完整自动解码退出0 | `ccc3d5fa6f1a2e32280640f8a0971808a125cdc86f82865e8d9a71ac955cbb1e` |
| Bilibili直播 | 墙钟60.235秒，接收20,643,840字节，保留完整FLV tag共20,634,932字节；尾部8,908字节未完整tag不作封片 | 1920×1080，约60fps，AAC；原tag跨度68.416秒，含CDN初始缓冲；另存归零MP4为68.435秒且完整解码退出0 | `ae2cb7ac4b903da883cea3af7e7ae6e610411a8ee3016c02ce6924cb385e255e`（FLV） |
| Douyin直播 | 墙钟60.000秒，接收160,235,520字节，保留完整tag共160,232,600字节；尾部2,920字节未完整tag不作封片 | 1920×1080，约59.94fps，AAC；原tag跨度62.933秒；另存归零MP4为62.957秒且完整解码退出0 | `1f2ee34173e6819ab128d6efd6289906f649b819d42b9a458eb3fe33d825d04e`（FLV） |

本次视频接口返回的实际候选：Bilibili从360P/480P/720P/1080P到4K，含不同编码/码率，选择4K AVC+AAC；Douyin返回1080×1920、720×1280、576×1024等多码率表示，选择1080×1920候选。只证明这些对象当时返回的候选，不宣称所有账号都可取4K、匿名只能720P或登录即可获取全部投稿。

直播归零派生文件未重编码且保留原FLV：Bilibili为20,630,069字节，摘要`cc689829b6324c2aaedd81ca7acf2b08e556b5ec8a5a217a5f99e31a8f9bbb1c`；Douyin为160,217,514字节，摘要`b4a46b3dc1b5f518fefc7613ecea4b4a346b66c914d5fcbbaccae73608f45ed6`。Douyin原FLV容器duration约17559秒是源绝对时间戳，不能写成录制了数小时。自动解码只用包内锁定FFmpeg、file/pipe输入、两线程、完整文件与错误即失败；没有前台播放、声音判断或浏览器操作。

**以上是实站来源可行性诊断，不是TRACE Core/GUI端到端验收。** 没有通过正式Core创建这些媒体的持久任务/资源引用，也没有完成新的链接质量选择UI；P14必须用当前生产包重新证明这一链路。原生IME、系统缩放、用户实际听到声音、外部播放器和ACT-003整体仍not-run，ACT-004已完成的用户反馈保持。

#### 实际命令、失败及保留

新命令均经`python scripts/stage_evidence.py run --kind <test|report> --id <id> --output artifacts/a-m1-real-readiness/<name>.json <下列命令>`保存真实退出码及完整日志；表中为子命令结果。诊断回执的生产组件为原m1-r2-2包/生产Douyin适配器及独立助手，不把源码快照字段当作新生产包证明。

| 实际命令/用途 | 结果与回执 |
| --- | --- |
| `python .local/site-check/m1-readiness-20261009/diagnose.py <mode> --execute-authorized --public-dns`；mode为bilibili-video-wbi32、bilibili-live-wbi32、douyin-original、douyin-secsdk、douyin-live-enter | 正确签名的源解析成功；原Douyin详情403失败保留。每跑次最多6请求/60秒、每次15秒、间隔2秒，未知/拒绝立即停止；各自原日志和私有summary可核对 |
| `python .local/site-check/m1-readiness-20261009/compare_requests.py <mode> --execute-authorized` | `bilibili-legacy-default`退出1/412，`bilibili-legacy-browser`退出0/200；`douyin-post-original`退出1/403，`douyin-post-secsdk`退出1（两次HTTP200后助手BrokenPipeError）。每个Bilibili跑次1请求，Douyin每跑次self+post各1，不请求后续页 |
| `python .local/site-check/m1-readiness-20261009/receive_media.py <mode> --execute-authorized` | `bilibili-video-complete`、`douyin-video`、`bilibili-live-cdn`、`douyin-live-https`四份成功回执均退出0；Bilibili视频补音轨复用已核对视频字节，没有重复下载4K视频。原视频音轨端口/直播scheme/重定向失败独立保留 |
| `python .local/site-check/m1-readiness-20261009/validate_saved_media.py` | `local-saved-media-decode`退出0，四份完整解码通过；两个直播归零转封装，零网络，原文件摘要复核一致 |
| `cargo test --manifest-path artifacts/a-m1-real-readiness/wbi-verified-probe/Cargo.toml --locked --offline --target-dir target --features baseline c14_ -- --nocapture` | `f026-correct-oracle-baseline`：101，1 failed；来源是保存的原Git源码，固定SHA和摘要可核对 |
| 同一WBI命令去掉`--features baseline` | `f026-correct-oracle-fixed`：0，2 passed；导入当前实际源码及URL构造回归 |
| `cargo test -p trace-adapter-bilibili --locked -- --nocapture` | `bilibili-final2`：0，26/26；初次错误A向量的`bilibili-final`：101，23/26，保留 |
| `cargo build -p trace-adapter-bilibili --locked`；`cargo clippy -p trace-adapter-bilibili --locked --all-targets -- -D warnings`；`cargo fmt --all -- --check` | `bilibili-build`、`bilibili-clippy`、`fmt-final2`均0；未重建包或target/measure |
| `cargo test -p trace-e2e --locked --test m1_production_adapters -- --nocapture` | `production-adapters`：0，9/9；没有删减原F-020/全文/分段断言 |
| `cargo test --manifest-path artifacts/a-m1-real-readiness/probes/Cargo.toml --locked --offline --target-dir target --no-fail-fast -- --nocapture` | `integration-probes`：101，0 passed/2 failed；F-028默认媒体域与F-029生产适配器→实际Core限长回放。只用合成数据/回环，没有Cookie/站点请求 |

独立新crate首次从零生成锁文件遇离线索引中的yanked版本，未执行测试；`integration-baseline`保留解析失败。随后复用既有已锁依赖版本，`integration-build`退出0，项目Cargo.lock未变；不把首次工具准备失败算作产品反例。原错误WBI探针、历史媒体失败及所有后继正确复验分开保存。

助手在诊断过程中有明确迭代；`preserved-a-handoff/`保存当前元数据助手、媒体phase2和CDN最终助手，源码摘要见公开审计。早期被改写的助手版本没有逐跑次冻结，报告仅保留其原命令/日志及当时观测，不把当前脚本摘要追溯冒充所有历史跑次。新compare/本地解码助手及独立探针另外保存摘要；B不可直接把这些私有诊断代码作为发布路径。

#### 当前结论和证据适用范围

| 项目 | 结论 |
| --- | --- |
| B实现与原自检 | 原r2交付和290项自检/正式资源结果仍只证明当时版本；既有六个未结P1及新增P14待实现 |
| A技术复核 | failed；F-026局部修复与定向复验完成，F-023～025/F-027～029集中交B实现，不要求B复审A |
| 两站真实媒体 | 指定来源的两视频完整接收及两段1080P直播、四份自动完整解码通过；TRACE正式链接/任务/对象/GUI链仍未通过 |
| 产品验收 | ACT-004视觉反馈保持完成；ACT-003整体及原生音视频/IME/缩放/外部程序仍not-run，现有自动解码不代填 |
| 许可、远端和发布 | Apache-2.0项目许可已落实，后继新包须含根LICENSE；本阶段未push/创建PR/合并/发布，无新增此类授权 |

规范输入现在包含A的WBI修复及r3计划/需求/契约变化；原216输入指纹`f8a7d480ddb520d2b52f14f52145c4b1b6f1695b033d7e184a8b8f61140f86b7`和m1-r2-2包/measure-final3仅作历史，不能换绑。原r1/r2包、全部正式/失败采样及preserved measure二进制、closeout-4/原验收Profile、用户配置/媒体和本地参考都保留。§13～15、迁移1～4、原A probes/closeout/home断言及原m1/r2映射逐项核对；当前WBI两个错误nav期望的修正另列，不隐称全部函数体未动。

当前公开脱敏摘要为[m1-r3-a-readiness.json](../evidence/gate/m1-r3-a-readiness.json)。治理/公开检查、旧bundle的明确拒绝、源码及Git归属审计在同一`artifacts/a-m1-real-readiness/`追加，最终SHA从实际Git读取；本次不制造一个通过的r3产品bundle。B完成P14和集中返工后需按当前输入实际构建新包、运行完整正式资源与全部当前门禁。

收尾预检已退出0：49份文档、549个本地链接、87需求/30AC/12INV/18ADR，公开内容0发现，Python治理反例16/16。当前216项规范输入集合摘要为`b8f26956b0dd39ae43fe12aad1e06e25dec5f633d1093e2310b8164bc625a5c2`，相对续行基点恰有5项变化：两个WBI源码文件和计划/需求/契约。原B bundle在`stage_evidence.py check`和`verify.py --stage-bundle`两个入口都实际退出1，明确拒绝旧源码、计划/需求范围、包和长测；回执为`old-bundle-check`及`old-bundle-verify`。当前规范下再次执行两个集成反例仍为101、0/2通过，`integration-probes-final`保留，不屏蔽失败。

最终文档、公开内容、工作树/索引差异检查分别写`docs-final`、`publication-final`、`diff-final`、`index-diff-final`，归属/保留审计为`preservation-source-audit.json`；实际远端只读查询仅见原main。提交前按明确22文件名单暂存，amend同一DCO阶段提交；提交后Git blob、引用和干净状态审计写`git-delivery.json`，最终SHA只由实际Git交付，不递归写回本报告。

<a id="a-to-b-m1-rework"></a>
### 16.17 A→B 当前r3集中实施与返工 prompt

```text
请作为Agent B，在TRACE的codex/m1-two-site-archive分支按唯一计划docs/delivery/stage-plan.md的TRACE-M1-01 r3，连续完成M1集中返工及P14/C14。先读AGENTS、索引、状态、有效决定、事项、USER-ACTIONS和适用专项规则，核对用户附带的最终A完整SHA、实际HEAD/工作区及本地reference，保护用户后续修改。main基点960984c34487244f31f1238dffc2bd9faaa292e7，原B r2交付b670e3b7aee88bb72e4369c5d3bab0160c460cf2；A实站诊断开始时8f7e2c9f1d4419016e7553790c050a3f0236af9e，最终以本次附带Git SHA为准。仍是一个未推送DCO阶段提交，不改main/备份历史，不push、创建PR、合并、发布、启动子Agent或自动跨聊天发送。

当前F正本在同一报告§16.3，新实站原因/文件/命令/边界在§16.16，公开摘要docs/evidence/gate/m1-r3-a-readiness.json。原r2实现、自检及A审查历史继续保留。集中处理F-023/F-024/F-025/F-027/F-028/F-029六个P1，保留A已核实的F-018～022以及本次F-026局部修复；不重建无变化的已闭合范围。

F-023：共享media第一个消费者完整高清候选pending后，正常succeed转交第二消费者，尚无新字节即被upgrade.list改为stale。位置jobs/consumers.rs::finish/reconcile及upgrades.rs::snapshot/confirmation_current/invalidate_stale。分开共享执行租约和已完成消费者的确认范围，正常交接保留候选，真实取消/订阅、凭证、质量、正文、旧选择变化仍拒绝旧确认。覆盖后继结果相同/较低/无提升/失败/新候选，不能简单移除代次屏障。

F-024：旧低清automatic选中，高清已Reject；copies删除后restore把asset_selections改为high/rejected，quality::evaluate也不纠正。位置deletion.rs::restore及quality.rs::evaluate。所有恢复/自动选择/派生使用保持审批边界；覆盖copies/archive、pending/rejected、无旧版、固定/共享/已回收、后续质量/播放派生/导出，不删除历史或自动接受候选。

F-025：旧completion-media代次1在OBJECT_READY后取消；新凭证的另一job_id代次仍1，acquisition_operation只含media+代次+候选摘要而碰撞。ready错误复用旧准备字节，commit正确拒绝所有权后新任务被cancelled。位置core.rs::acquisition_operation/uncommitted_operation及objects/mod.rs::ready/start_receiving/commit。物化ID与重用按逻辑请求隔离且可重入，保留commit屏障；覆盖两补全/普通任务交错、凭证换代及四提交窗口，同请求重开不多建变体，新身份不得虚构下载。

这三项原复现：cargo test --manifest-path artifacts/a-m1-r2-review/probes/Cargo.toml --locked --offline --target-dir target --no-fail-fast -- --nocapture。原independent-probes-final2为101、0 passed/3 failed，原文件/失败不覆盖；先移入规范测试并保持故障语义，连同between_resources旧缓存与待确认备份恢复守护核对。

F-026已由A修复：WBI取32字符，生产signed_url只发一次wts，正确固定key为ea1db124af3c7062474693fa704f4ff8，对应示例MD5 e0ed91b768b58beb346ad3116de6ab68。26项Bilibili及9项生产适配器通过。保留新增真实URL向量和原三个sign向量；两个旧nav期望原先错误地断言64字符，已明确纠正。A最初33字符的错误探针仅作历史，不用它回归；正确独立探针是artifacts/a-m1-real-readiness/wbi-verified-probe，旧源码模式失败、当前模式2/2通过。

F-027：同一有效Cookie/self登录通过，生产Douyin详情和投稿缺SecSDK上下文而403/uifid_missing。隔离补齐现有Cookie的访问者ID/指纹和x-secsdk-web-signature后详情200且适配器解析，投稿200/20条/has_more=1；没有msToken也成功，不要伪造。按本地reference及TRACE研究核对受保护host/method/path和编码/顺序；Core持有凭证派生上下文及代次，适配器/UI/final_url/日志/参数/导出/包不得收到这些秘密。保留self登录判断、跨域头隔离、C06/C11/C13。成功HTTP不代替F-029分页处理或媒体归档。

F-028：默认媒体策略缺bytefcdnrd.com和douyinliving.com；真实Bilibili CDN重定向用前者，Douyin直播返回后者的HTTP FLV，同一地址HTTPS已实测。按有证据的窄CDN策略和HTTPS规范化贯通Core，不让用户填测试白名单，不回退明文、不绕过逐跳DNS/IP/TLS或嵌套HLS校验。媒体零Cookie；伪后缀/私网/重绑定/跨站负例保留。诊断音轨8082端口的拒绝是A助手限制，不能误修Core端口策略。

F-029：生产post固定20条，实站合法页1,454,679字节，common Client固定max_bytes=524288，base64后也超过1MiB帧。A实际Core+生产适配器合成回放安全拒绝但整页失败；助手直接转发的大帧BrokenPipe历史不冒称Core崩溃。协调有界页大小/有限缩页与必要的大对象协议，保证全文、必要资源、完整分页和失败游标不前移；超大单条、空页has_more、并发/取消/重启都覆盖。不能静默截断或取消大小上限。F-028/029原复现：cargo test --manifest-path artifacts/a-m1-real-readiness/probes/Cargo.toml --locked --offline --target-dir target --no-fail-fast -- --nocapture，原integration-probes为101、0/2通过；该探针使用原m1-r2-2生产适配器和当前Core，迁入规范时改为当前实际构建并保留等价断言。

P14/C14是M1本次必须完成的完整产品流程：粘贴Bilibili/Douyin视频URL→解析稳定身份和当前身份可得质量/编码/必要音轨/分段→用户明确选择→同一Core持久一次性请求/对象库归档→离线读取；无需先订阅全账号。既有资源仍走统一升级确认；预览过期、凭证换代、双击幂等、取消/重启、降级/必要资源变化均有反例。直播URL先识别展示房号/真实房间/主播，不能把房号当账号；现代Bilibili getInfoByRoom/getRoomPlayInfo使用正确WBI与直播UA/Referer，Douyin web room用web_rid/enter路径已取得流。实现时保留全部原FLV/HLS、独立音轨、KEY/MAP/BYTERANGE和四提交窗口。

真实退出条件AC-29/30不能写not-run后结项。用户给定的四个目标只在本机.local/site-check/m1-readiness-20261009/cases.json，旧主页在既有本地请求文件；凭证/授权/预算以USER-ACTIONS ACT-002为准，不重新索取已有效Cookie。先完成离线修复再用当前正式包在隔离Profile做一次有界生产复验：两站各一个完整视频（Bilibili样本实际4K及必要音轨）和一段至少60秒、实际短边≥720的真实直播；GUI关闭继续、停止封片/重开/导出/完整本地解码，记录任务/认证上下文/实收/全部必要对象哈希。A已取得两视频及两1080P直播并完整解码，只证明源可行，严禁把这些文件手动放入对象库代填。不得对真实旧档案覆盖；失败/挑战立即停止该跑次，目标停播或凭证失效仅暂停依赖部分并给具体原因，独立开发继续。

原m1/r2映射对象、§13～15原字节、迁移1～4、A旧probes、closeout_rework/closeout_faults、home事件/媒体节点和F-001～029历史都保留。采用独立r3映射：87条需求/68条M1/19条后继，原98和r2的133必需用例不能消失，新增C14、六个F反例和真实产品链门禁。现有stage_evidence仍是r2门禁，需完整更新r3语义/计划/规范输入和真实回执拒绝，不只换标签。旧bundle/缺证据/失败/陈旧输入/低于720P/缺音轨/诊断脚本都不得放行。

本次A只改WBI和规范/审查文档，原m1-r2-2/measure-final3仍证明修复前输入f8a7d480ddb520d2b52f14f52145c4b1b6f1695b033d7e184a8b8f61140f86b7。你完成修改后按当前输入运行相关及整套门禁、构建新包（含项目LICENSE/原第三方通知）与原标准完整正式资源测量/聚合，不能改绑旧回执或以60秒实站样本替代60分钟合成长测。保留closeout-4/原验收Profile、m1-9、全部r2包/失败采样及preserved measure二进制；重建前核对保护范围。

开发缺口只在报告F及事项总表，用户当前动作只在USER-ACTIONS，非阻塞LOOP只在状态；不把必须项转给用户测试。ACT-004已按用户反馈完成，Apache-2.0许可证已决定，ACT-003原生整体仍not-run；自动DOM/currentTime/FFmpeg解码不代填原生体验。持续完成实现、自检、修复和文档同步，只把F标已修待A复核；amend同一未推送DCO阶段提交，实际Git读取完整SHA，提供一次B→A集中复审prompt。A通过后直接向用户收尾，不再要求B复审A。
```

<a id="b-m1-r3-delivery"></a>
## 17. B M1 r3 集中返工与验收

本轮按唯一TRACE-M1-01 r3和§16.17连续实施，基点为A交付0a86273c602d1698a96962730dd5c67557b08e88，工作区起始干净。六项P1、P14/C14的当前实现与最终证据在本节集中记录，完整资源测量发现的F-030也在本轮修复。当前输入的自动检查、新包及完整正式资源测量通过；四个真实产品链用例仍缺，AC-29/30保持blocked，M1为rework-required。A审查尚未进行，原§13～16及全部旧成功/失败证据保留。

### 17.1 承接与受保护基线

B开始于A交付SHA，未切换main、未创建竞争计划。依AGENTS及本地参考规则逐级确认索引，只读核对对应功能与许可；参考材料没有成为授权或构建依赖。凭据和四个目标仍使用ACT-002已有本机输入，不要求重新复制Cookie。

§13～15原始字节保持，内容摘要仍为`af6d68e894acc6fe522959b974e84ed82bc36a5ea157bb3779f7021f68ae00cb`。原m1/r2需求对象及r2审查元数据逐项对照A基点，未改变；独立r3为87条需求、68条M1适用、19条后继。迁移1～5不改写，由schema6增加新结构。原A probes、closeout断言、home事件/媒体节点、旧包、验收Profile、旧正式/失败测量均保留。旧target/measure两执行文件另存`artifacts/m1-r3/preserved-pre-r3-measure/`，摘要清单用于新测量前后核对；不改绑旧测量。

### 17.2 实现边界

F-023按完成消费者的请求序号保存升级确认范围，正常共享执行权交接保留完整候选；取消、订阅/凭证/正文/选择/固定状态变化仍失效。F-024保存删除前选择并对恢复、自动选择和播放派生实施审批检查，拒绝/待确认候选不被恢复为当前资源。F-025在物化身份中加入job_id，开始接收、ready和commit都检查原所有者；原schema5键仅由同一任务/资源/代次恢复，不能碰撞另一补全。收集中断的同任务可复用自身已提交的pending变体，不复制另一身份的获取记录。

F-027的SecSDK上下文在Core中按固定host/method/path派生；固定查询编码/次序向量和实际回环HTTP断言共同验证，跨域剥离派生头/查询，final_url不含上下文。self登录证明与访问签名独立，不生成msToken。F-028按来源证据加入两个窄CDN域，Douyin该直播CDN只做同地址HTTPS规范化，所有逐跳域/IP/TLS及媒体零Cookie约束保留。F-029增加显式有界分片消息，1.45MB/20条生产适配器回放完整通过；超限、坏摘要、乱序、并发、超时和进程重开有拒绝断言。普通帧仍有1MiB限制，旧端预算参数不等于实际响应大小。

P14通过`/acquire`进入原Core：稳定链接解析、安全选项、五分钟预览、显式确认、持久source_acquire、媒体或录制任务、原对象库和离线档案。one_shot仅复用凭证/控制范围，数据库禁止作者扫描；视频必要音轨/全部有序分段不得省略，已有资源仍走统一升级确认。双击以及不同直播预览在排队阶段去重，身份/质量变化返回冲突。实现及协议的唯一正本见[归档契约§9](../site-archive-contracts.md#direct-source-acquisition)。

新增定向测试实际覆盖两站生产适配器→本地HTTP→Core资源→离线重开及真实旧/新媒体的显式升级；没有拿诊断媒体填对象库。真实执行器只通过生产包GUI/IPC驱动，所有网络接收与对象提交均由Core完成。门禁独立读取数据库中的任务、认证代次、所有必要变体、物化记录、封片源时间、Core范围读取字节、导出与完整解码输入，不能用成功标签或诊断文件通过。

### 17.3 中间失败与修复记录

全部原A失败及B中间回执留原路径。`artifacts/m1-r3/`中的initial/second/directed等为实施过程采样，不作为最终聚合输入；命令退出码及sourceUnchanged分别保存，不将测试进程成功但输入变化的跑次冒充当前证据。

| 回执 / 原观测 | 具体处理与复验边界 |
| --- | --- |
| core-initial、data-network、data-network-2 | 新任务身份参数、Profile字段及类型推导的编译失败；修正接线后相应定向用例通过，未改原业务断言 |
| source-directed | Bilibili all_variants的已复合音轨分支未标示silent_or_muxed，必要资源检查正确拒绝；修复后两站生产视频链通过 |
| ui-directed | Windows子进程找不到corepack；改用实际corepack.cmd命令，原失败日志保留 |
| clippy-final-1 | 活跃录制行类型过于复杂；提取类型别名，严格Clippy通过 |
| rust-final-1 | 旧proxy.fetch请求4MiB预算被新增512KiB参数上限拒绝，原closeout及离线闭环失败；宿主保留旧预算小响应能力，过大普通响应明确返回错误而不是丢帧 |
| rust-final-2 | 原prepared恢复断言发现重复物化；新增预览字段不应改变旧物化比较，恢复原签名字段并只在新source选项/缓存核对中扩展，按同任务所有权接续旧键 |
| legacy-reentry | 原closeout六个窗口、十种分段组合、暂停恢复、完整离线闭环均实际通过；原测试文件和断言未弱化 |
| package-1、package-2 | 两个包均实际构建，分别绑定其当时输入；随上面修正成为历史包，不冒充当前交付，也未覆盖或换绑 |
| real-douyin-01 / 1次元数据、0媒体 | GUI驱动器在初始路由尚未完成时导航，未进入来源请求；等待实际main内容后点击产品路由。三个冷启动离线窗口验证导航且未发新站点请求 |
| real-douyin-02 / 7次元数据、0媒体 | self/detail均200，首个CDN请求403后立即停止。对照A已成功请求的公开头，生产候选缺UA/Referer；补入Douyin视频及直播候选，并由Core实际HTTP反例核对媒体零Cookie |
| real-douyin-03 / 9次元数据、9,352,828媒体字节 | 生产GUI选择1080×1920选项，Core完成视频接收和对象提交；直播预览失败而中止，尚无该跑次的导出/重开/完整解码，不计AC-29/30通过 |
| real-douyin-04 / 3次元数据、0媒体 | 先检查直播，enter为HTTP200/业务0、单房间status=4且无owner。只保留字节数/业务码/数量/状态/owner是否存在的安全观察；停止该目标。修复停播响应缺身份时误报结构变化，Core只传白名单离线状态，不转发原始消息；依赖只见ACT-002 |
| real-bilibili-01 / 3次元数据、0媒体 | 登录验证成功，公钥bootstrap仍请求匿名nav，泛用API信封把其未登录状态当签名不可用。请求沿用所选范围；只允许无挑战且完整的公开WBI键用于签名，-101仍不是登录证明，原32字符算法和向量不改。原失败包/回执保留 |
| real-bilibili-02 / 20次元数据、52,324,675媒体字节 | 生产GUI选择直播，Core实录、GUI关闭继续和停止封片成功；视频媒体任务在接收前被选择守护拒绝。数字quality_id经safe_metadata变成字符串，导致预览和执行摘要不同；统一新source规格规范化，原acquisition_spec不变。`dash-normalization-baseline`实际失败，`dash-normalization-fixed`通过分離视频/必要音轨接收、原选择保留、Core播放派生及离线重开。整个跑次仍失败，不冒充AC-29/30完整证据；后续依赖仅见ACT-002 |
| resource-formal-1 / 主动停止、保留完整已采样部分 | 提交一致性核对发现需求JSON/许可台账为CRLF而Git要求LF；原包匹配工作区，但不匹配规范化后的提交字节。在闲置采样阶段停止自有工作负载，原始计数、失败benchmark及当时二进制全部保留。统一换行不改需求内容；按Git过滤后的233项输入逐一比较，字节差异为0，重新生成包/回执和完整正式测量 |
| resource-formal-2 / 完整945样本、恢复45.246秒未达标 | 完整录制3621.335秒、闲置905.75秒、混合并发361样本、任务错误0；查询/CPU/内存/录制启动/GUI关闭均通过，原恢复30秒上限失败。按F-030量化并修复已提交暂存路径的逐行持久写放大；保存全部采样和二进制，再生成新包和同规模/时长正式结果 |
| docs-pre-formal4 / 文档检查退出1 | §17.5将A探针、closeout、主页和WBI以斜杠串写，触发个人路径规则；改为准确枚举，未改变检查器。docs-post-path-fix实际退出0，49文档/570本地链接/87需求/30验收/12不变量检查通过；原失败回执保留 |

旧r1和r2 bundle在现有验证函数中明确拒绝，结果在`old-bundle-rejection.json`；旧回执字节未变。F-026继续使用A的32字符正确独立向量，最初33字符错误探针仅保留历史。最终通过证据与真实边界将在下节逐项列明。

保护审计`preservation-final-2.json`逐项核对迁移1～5、原98/r2的133必需用例，以及closeout-4、m1-9、三个r2包的全部清单文件。首份审计使用不存在的MIGRATION_002/003常量名，造成两项假阴性；按实际MIGRATION_002_TRIGGERS和内联第三迁移核对后均未变，首份审计仍保留。中间Rust全套先后245及246用例通过，UI52、Python19通过；后续公钥bootstrap修正后这些仍只证明对应原输入，最终证据须重新运行并记录当前输入。

### 17.4 首次完整资源复测前的检查与包

本节记录发现F-030前的版本：输入233项，集合SHA-256为`ce112b46b2314f7b52e82c8350f776668277610d7a407b764bae81710a7fcb72`。测试、脚本、规范需求与契约都参与输入摘要；Git过滤后的源文件字节已逐一核对一致。各回执在`artifacts/m1-r3/`保存真实命令、原输出、退出码与sourceUnchanged，不从旧回执换绑摘要。F-030修改后本节包及回执只证明原输入，最终证据另列。

| 回执 / 实际命令 | 结果与范围 |
| --- | --- |
| `rust-final-7` / `cargo test --workspace --locked --offline --no-fail-fast` | 248项通过，0失败/忽略；包含原A/closeout、六项F、P14、分离DASH和大页/网络/恢复反例 |
| `ui-final-8` / `corepack.cmd pnpm -C apps/desktop exec vitest run --reporter=verbose` | 52项通过；原home事件/媒体节点断言保持，新链接预览/选择/晚到事件/停止/错误状态通过 |
| `python-final-9` / `python -m unittest discover -s scripts/tests -v` | 19项通过；包含r3真实回执、数据库所有权/封片、实际尺寸/音轨、解码输入、历史预算和授权上限的拒绝反例 |
| `post-eol-fmt-1` / `cargo fmt --all -- --check` | 退出0 |
| `post-eol-clippy-1` / `cargo clippy --workspace --locked --all-targets -- -D warnings` | 退出0；工具链的增量缓存无法复用提示不影响编译结果，原输出保留 |
| `post-eol-verify-1` / `python scripts/verify.py --all --json` | 五套件5/5通过；包含类型检查、前端生产构建、离线、恢复、生产协议夹具和资源规则，不代替真实站点AC-29/30 |
| `package-11` / `python scripts/package_windows.py` | 实际构建`dist/trace-0.1.0-windows-x64-m1-r3-10`，sourceUnchanged=true；新目录，未覆盖前十个r3包或旧稳定包 |
| `package-check-11` / `python scripts/check_package.py dist/trace-0.1.0-windows-x64-m1-r3-10` | 2项通过；完整包清单、无测试端点、两站注册，以及实际最小化WebView关闭/重连/完全退出 |
| `ui-visual-final-2` / `python artifacts/m1-r3/ui-visual-final-2/run.py` | 当前包1280/760宽的链接页截图及实际导航，0站点请求、无前台输入；不代替原生IME/声音/缩放验收 |
| `original-r2-probes-current-2` / `cargo test --manifest-path artifacts/m1-r3/original-r2-probes-current/Cargo.toml --locked --offline --target-dir target --no-fail-fast -- --nocapture` | A原三项断言的副本3/3通过。原Cargo/src/lock摘要未变；初次直接使用原锁被--locked正确拒绝，失败保留，当前依赖边只在副本离线更新 |
| `wbi-current-2` / `cargo test --manifest-path artifacts/a-m1-real-readiness/wbi-verified-probe/Cargo.toml --locked --offline --target-dir target -- --nocapture` | A正确32字符独立探针2/2通过，原错误33字符探针仅保留历史 |

当前自动套件共321个唯一用例身份，包含168个必需用例中的164个；尚缺四个必须真实产品链用例，不能据此宣布阶段通过。原98与r2的133项全部保留。中间`rust-final-6`原日志实际248 passed，但一行并发诊断使解析器只读到247个名称，B另跑该C11名称并保留回执；最终`rust-final-7`使用标准捕获输出，248个名称全部可解析，不修改旧输出或弱化解析器。

当前包24个文件、209,420,052字节，包含根LICENSE、Douyin通知、依赖许可台账及锁定媒体许可/源码说明。manifest摘要`35b6bf39d79ac72ed492ec7b0f3deb7d31e884afd841f4b3bc13195e763b8cd4`，SHA256SUMS自身摘要`229146bfab317f36892ee2f87caf8f91e3abb84482ee685f7226cde2681d79d5`；清单记录构建时A基点与dirty状态，实际内容由当前233项输入与所有包成员摘要证明。最终交付提交SHA从Git读取，不修改包的旧构建字段。

保留盘点`retained-inventory-final-1.json`在正式新测量前记录：artifacts 22,096文件/44,706,479,627字节；本地site-check 2,061文件/1,289,316,326字节；dist 663文件/5,998,382,821字节；锁定媒体资产238文件/443,118,902字节。只统计元数据，未读取无关凭据或沿链接遍历；构建/依赖缓存保持，未逐文件审查或清理。公开证据当时393,164字节，原包、Profile、正式/失败样本与新中间包均保留；最终摘要另行汇入本节。

### 17.5 F-030后的当前输入、完整检查与新包

F-030修复后继续冻结233项源码、测试、脚本、锁文件及规范输入。交付核对发现需求映射的描述性`r3Implementation.requiredCases`仍为早期165，已同步为实际169；原m1/r2对象及其98/133必需用例没有变化。该数字修正前的`rust-final-8`实际249/249通过，仍保留为其原输入的回执。最终集合SHA-256为`0c47d1764921fb2cd4898eb16211d821873c6ca0a2e9a29ffa3003b6a339ae72`，`final-input-audit.json`逐项验证Git规范字节无差异；本节以下检查均实际重新运行，sourceUnchanged=true且输入集合相等。

| 当前回执 / 实际命令 | 结果与适用范围 |
| --- | --- |
| `rust-final-9` / `cargo test --workspace --locked --offline --no-fail-fast` | 249/249通过，0失败/忽略；保留原A、closeout、原r2和新增F/C14反例，标准捕获输出全部可解析 |
| `ui-final-10` / `corepack.cmd pnpm -C apps/desktop exec vitest run --reporter=verbose` | 52/52通过，原主页事件和媒体节点断言保持 |
| `python-final-11` / `python -m unittest discover -s scripts/tests -v` | 19/19通过；真实回执、任务/认证所有权、必要对象/音轨、封片/时长/尺寸及累计授权预算拒绝反例保持 |
| `fmt-final` / `cargo fmt --all -- --check` | 退出0 |
| `clippy-final` / `cargo clippy --workspace --locked --offline --all-targets -- -D warnings` | 退出0 |
| `verify-final` / `python scripts/verify.py --all --json` | desktop/offline/recovery/resources/site五套件5/5通过，耗时379.819秒；原完整子命令日志保留 |
| `package-12` / `python scripts/package_windows.py` | 退出0，实际生成新目录`dist/trace-0.1.0-windows-x64-m1-r3-11`，包含本次F-030；耗时139.635秒 |
| `package-check-12` / `python scripts/check_package.py dist/trace-0.1.0-windows-x64-m1-r3-11` | 2/2通过，完整包/许可/组件及实际最小化桌面与同一Core关闭重连、完全退出 |
| `original-r2-probes-current-3` / 原三项探针副本的`cargo test --manifest-path artifacts/m1-r3/original-r2-probes-current/Cargo.toml --locked --offline --target-dir target --no-fail-fast -- --nocapture` | 3/3通过；原探针文件和失败回执未修改 |
| `wbi-current-3` / `cargo test --manifest-path artifacts/a-m1-real-readiness/wbi-verified-probe/Cargo.toml --locked --offline --target-dir target -- --nocapture` | 7/7通过：两个正确32字符独立C14探针及导入源码的五项原测试；未使用最初33字符错误期望 |
| `ui-visual-final-3` / `python artifacts/m1-r3/ui-visual-final-3/run.py` | 当前包1280/760宽链接页及实际导航通过，截图已查看，0站点请求、无前台输入；原生体验仍单独判断 |
| `preservation-final-4.json` / `python artifacts/m1-r3/audit_delivery.py --package dist/trace-0.1.0-windows-x64-m1-r3-11 --output artifacts/m1-r3/preservation-final-4.json` | 原§13～15、迁移1～5、A探针、closeout、主页与WBI文件、A公开摘要及原m1/r2映射一致；包/输入/许可校验无错误，原cases.json摘要未变 |

四类计入聚合的test回执共322个唯一用例，169项必需中165项通过；另四项为`c14_real_{bilibili,douyin}_{video,live}`，只能由当前包的完整真实链补齐。上述原探针、五套件和截图回执为report，不重复计数。真实中间跑次继续按§17.3保留，不能因它们已接收部分媒体而冒充当前包四项通过。

新包24个成员、209,444,628字节，含根LICENSE、Douyin通知、依赖许可台账及锁定媒体许可/源码说明。manifest摘要`6ffafa5d35ccbc3e7b51345fd74e56770636a030655984fc4f51d6d34f8a6024`，SHA256SUMS自身摘要`5a519ac0561955f9311da03d35d01d08722a06d87f760560d08511284d32e06a`。构建时A基点/dirty字段按原现场保留，最终提交通过逐输入字节对应，不能追改manifest。旧包、旧长测及本轮全部r3包均保留；该包的完整正式测量见§17.7，不由定向恢复测试代填。


<a id="m1-storage-outage-recovery"></a>
### 17.6 存储中断与恢复核对（2026-10-11）

`resource-formal-3`在2026-10-10进入混合负载后发生宿主盘满：08:48的Windows VHDMP事件126/11记录虚拟卷写入返回0xC000007F，随后事件2卸载开发卷，System disk事件15/51记录设备未就绪和I/O错误。当时宿主卷剩余0字节，仓库及原始输出不可访问；包装器读取日志失败并退出1，没有完成最终回执。最后一次聊天工具可读进度为elapsedSeconds=4426.5961731、2下载+2录制、5177片封片、14,903,185,044字节实收；这只是当时观察，不能代替完整采样或60分钟/恢复门槛。

用户于2026-10-11确认开发盘已恢复。B重新读取同一分支、A基点和main基点，233项输入仍精确匹配§17.5的0c47d176…9ae72；原322个自动用例、格式/Clippy/五套件、包构建与原探针回执的输入和原日志摘要均一致。`post-recovery-preservation-1.json`再次核对受保护正文/迁移/映射、原cases.json和当前包全部文件通过；`post-recovery-git-fsck`实际执行`git fsck --full --no-reflogs`退出0，`post-recovery-package-check-1`实际执行当前包检查退出0，包含新隔离Profile上的启动、关闭重连和退出。没有因恢复盘符重建计划、覆盖用户文件或改变真实站点授权；用户事项仅见[ACT-005](../../USER-ACTIONS.md#act-005)。

恢复后的中断目录实际保留2116个文件、8,044,120,705字节（不含随后保存的二进制副本），未沿链接遍历。`resource-formal-3.json`、`benchmark.json`和`resource-samples.json`不存在，progress.json不能解析，包装日志为零字节。B保留这些现状，没有填造最终计数或修改原输出；当前可读measure程序另外复制到`measure-final-3/post-recovery-binaries/`并记录摘要，该摘要只证明恢复后读到的字节，不冒充中断前未落盘的构建摘要。完整核对在`artifacts/m1-r3/storage-outage-recovery.json`。此前仓库不可访问时的应急记录未在原位置找到，本次依据已保存的会话工具输出与实际恢复现场重新记录事实，不声称该临时副本仍存在。

本次恢复时实际可用空间约为宿主卷1.14TiB、开发卷796GiB，已与仅反映虚拟容量的逻辑空间分别核对。正式复测继续使用当前未变更的m1-r3-11包和完整原规模、时长、门槛，在新的`measure-final-4`目录执行；中断跑次保持failed/interrupted，不能拼接为新正式结果。源码未变化，旧自动回执经过原始日志/输入核对继续有效；新的正式资源结果和最终文档聚合另行生成。

<a id="m1-r3-final-resource"></a>
### 17.7 当前包完整正式资源复测（2026-10-11）

实际执行以下命令，子命令和包装器均退出0，sourceUnchanged=true，输入与§17.5的233项及当前包完全一致。运行期间未重建程序、修改规范输入、降低规模/时长或过滤慢样本。

```powershell
python scripts/stage_evidence.py run --kind bench --id r3-resource-formal-4 --output artifacts/m1-r3/resource-formal-4.json -- python scripts/measure_m1.py artifacts/m1-r3/measure-final-4 --idle 1030 --seconds 3620 --entries 100000 --package dist/trace-0.1.0-windows-x64-m1-r3-11
```

| 正式观测 | 实际结果与边界 |
| --- | --- |
| 原始采样与时长 | 934个连续样本，采样窗口4665.860秒、最大间隔5.016秒；有效闲置905.157秒、录制3621.222秒 |
| 实际并发与接收 | 349个采样点同时至少2下载+2录制；纯录制350个样本全部4路活跃；累计实收15,862,850,922字节，任务错误0 |
| 闲置CPU / 内存 | 全相关进程CPU均值0.176568%，私有工作集p95 50.777344MiB，满足1% / 150MiB |
| 四录制CPU / 内存 | CPU均值1.556561%，私有工作集p95 62.765625MiB，满足5% / 300MiB |
| 查询 | 热查询p95 288.3109ms，冷连接查询p95 126.2469ms，满足500 / 1500ms；未清OS文件缓存 |
| 起录与发现 | 起录p95 1.407999秒，发现p95 28.016859秒，满足原10秒起录和30秒轮询的发现公式 |
| 完整恢复 | 10万条库、202个未完成普通任务，Core完整重开/恢复且IPC可达2.201118秒，低于原30秒门槛 |
| 实际GUI生命周期 | 两次当前包内嵌页面及r2/r3查询均成功、同一Core实例；关闭释放最大3.713482秒，之后无GUI仍继续接收 |
| 其他原始指标 | 全窗口私有工作集峰值201,097,216字节，句柄峰值3852、队列峰值227；工作卷最小空闲850,727,026,688字节 |
| 设备与负载 | Windows 11 10.0.26300、AMD64、20逻辑处理器；10万发布/50万媒体元数据、512字正文、100普通/20直播检查目标、100ms延迟、50行查询、每流1,112,866字节/秒 |

原始样本`measure-final-4/resource-samples.json`的SHA-256为`3539fcdb7ccc31e44ef8199005fb9d6f9dbcd425b554d1b5a5e557d46c4d6c35`。`formal4-metrics-check.json/.log`实际执行本地`audit_formal4_metrics.py`，不调用生产derive函数，复算18项派生指标全部一致；同时核对时长、连续性、完整规模、四录制窗口、恢复、GUI、包/源码/构建日志和二进制摘要。只读物化观察另记录21个相邻区间中两站同一下载operation实收字节均正增长，原记录在`formal4-transfer-observations.jsonl`。这是B自检，不冒充A独立审查。

当前两份measure二进制已复制到`measure-final-4/preserved-binaries/`并复核：`m1_resource_bench.exe`为`cafcc326352c5e23914100461980bb4bdb9da018afe96dbb57de150a056fd560`，`m1_bench_adapter.exe`为`25beaf0326c15653f4bd99374de84f36344052335d4e1f5c5bcf6fa62563ab0b`。原始进程计数包括合成源、驱动器及已退出子进程的累计CPU；宿主与工作卷的独立空间观察保存在`measure-final-4-host-space.jsonl`，不改动正式采样器。结果仅证明本机合成负载，不能替代AC-29/30真实产品链、全历史覆盖或原生体验。

<a id="m1-r3-final-gate"></a>
### 17.8 聚合门禁、未齐证据与交付边界

当前公开摘要为[经脱敏的B交付证据](../evidence/gate/m1-r3-b-delivery.json)。四类test回执共有322个唯一身份、全部passed；原98和r2的133个必需用例都保留。独立r3现有169个必需用例中165个通过，缺少`c14_real_bilibili_video`、`c14_real_bilibili_live`、`c14_real_douyin_video`、`c14_real_douyin_live`。87条需求中68条M1适用、19条后继；43条已有完整映射证据，另25条适用需求同时要求上述真实链，不能计为通过。

加入当前包及完整正式资源结果后，实际预聚合`provisional-bundle.json`为failed。两个现有CLI入口均实际运行，退出1，日志分别为`delivery-gate-pre-stage`和`delivery-gate-pre-verify`：

```powershell
python scripts/stage_evidence.py check artifacts/m1-r3/provisional-bundle.json
python scripts/verify.py --stage-bundle artifacts/m1-r3/provisional-bundle.json
```

两入口的30条拒绝完全一致：25条M1需求尚未取得完整真实证据、4个必需用例缺失、1项缺少真实生产Core产品链回执。没有陈旧源码、损坏包、重复用例、失败资源指标或缺原始日志等其他错误；门禁没有因自动用例全绿而放行。最终文档、公开摘要和登记完成后在`artifacts/m1-r3/final-bundle.json`重新绑定文档指纹，最终两入口日志使用`delivery-gate-stage-1`和`delivery-gate-verify-1`，不修改预聚合或旧回执。

AC-29/30仍为blocked，M1保持rework-required。本轮已有Douyin视频对象提交和Bilibili直播封片的中间事实只保留在§17.3，没有混入当前包的四项完整证据，也没有把A诊断文件导入对象库。后续真实执行的对象/额度依赖只在[ACT-002](../../USER-ACTIONS.md#act-002)维护，开发盘恢复不构成预算增加；Cookie无需重新提供。ACT-003原生声音、IME、缩放、外部程序体验仍not-run，ACT-004/005的已完成反馈与本阶段技术退出分别判断。

F-023/F-024/F-025/F-027/F-028/F-029及F-030均只标“已修待 A 复核”。B完成当前可独立执行的实现、自检、打包、完整资源验证和记录；A复核与四个真实链用例未通过前不能称M1结项。未执行push、PR、合并、发布、子Agent或跨聊天发送。阶段仍按同一未推送DCO提交amend，最终SHA从Git实际读取，在交付消息及本地`artifacts/m1-r3/git-delivery.json`提供；本报告保留被测A基点和输入摘要，不递归填入自己的提交SHA。

<a id="b-to-a-m1-r3-review"></a>
### 17.9 B→A 一次集中复审 prompt

下列仓库内文本以本次交付消息中的实际完整SHA定位；它不创建另一份实施计划。用户复制转交，B未自动发送。

```text
请作为 Agent A，对 TRACE 的 codex/m1-two-site-archive 分支进行一次集中复审。以 B 最终交付消息的实际完整 SHA 为被审版本，main 基点为 960984c34487244f31f1238dffc2bd9faaa292e7；本轮 B 承接的 A SHA 为 0a86273c602d1698a96962730dd5c67557b08e88。先核对 HEAD、工作区归属和用户后续修改，再读 AGENTS、索引、状态、有效决定、事项和 USER-ACTIONS。唯一计划仍为 docs/delivery/stage-plan.md 的 TRACE-M1-01 r3，不创建竞争计划。

按 docs/delivery/stage-report.md §16.17 的合同复核 §17 的 B 交付；F 正本仍在 §16.3，来源诊断与原失败在 §16.16。集中核实 F-023/F-024/F-025/F-027/F-028/F-029 及完整长测新增的 F-030。B 只标“已修待 A 复核”，不自称独立 A 通过。保留 F-018～022 和 F-026 的已核实范围；WBI 正确独立入口是 artifacts/a-m1-real-readiness/wbi-verified-probe，最初 33 字符期望仅留历史。

当前包为 dist/trace-0.1.0-windows-x64-m1-r3-11，233 项规范输入的集合 SHA-256 为 0c47d1764921fb2cd4898eb16211d821873c6ca0a2e9a29ffa3003b6a339ae72。按最终报告逐文件核对提交 blob、包成员、回执、完整资源采样和文档；保留 manifest 原构建 HEAD/dirty，不换绑旧包或旧测量。重点核实共享消费者确认身份、拒绝资源恢复、物化所有权、Core SecSDK 隔离、窄 CDN/HTTPS、正常大页有界协议及一次性链接任务不扫描整作者。P14 的预览过期、身份换代、幂等、取消/重开、降级、必要资源变化和旧资源升级确认都须核对实际断言。

当前 322 个唯一自动用例通过，169 个必需用例中 165 个有当前证据；原 98 和 r2 的 133 项保留。独立 r3 映射为 87/68/19，43 条已有完整映射证据，25 条适用需求仍依赖四个真实链用例。按报告实际执行 stage_evidence.py check 和 verify.py --stage-bundle，使用 artifacts/m1-r3/final-bundle.json；四个真实链缺失时应保持非零，不能通过改标签、遗漏必需项或诊断媒体代填。AC-29/30 尚未通过，M1 不能结项；进一步真实执行仅按 USER-ACTIONS ACT-002 的最新用户答复、累计预算和停止条件，既有 Cookie 无需重索取。部分旧跑次已经接收媒体，但不是当前包完整产品链证据。

复核 resource-formal-4 的934个原始样本、3621.222秒录制和18项指标，核对F-030的2.201秒完整恢复及349个混合并发样本。resource-formal-2 的恢复超限、resource-formal-3 的存储中断及缺失原始数据均须保留。原 §13～15、m1/r2 映射、迁移1～5、A probes、closeout、主页事件/媒体节点、旧包及全部正式/失败样本不改写。局部问题由 A 直接修复和相应验证，较大实现缺口一次性集中反馈；修复与审查记录继续 amend 同一未推送 DCO 阶段提交，不改 main/备份历史，不 push、建 PR、合并、发布、启动子 Agent 或跨聊天发送。必须条件补齐且 A 通过后直接向用户收尾，不要求 B 再审 A。
```


<a id="a-m1-r3-review"></a>
## 18. A M1 r3 集中复核与局部修复（2026-10-11）

本节按§16.17复核§17，唯一计划仍为TRACE-M1-01 r3。被审完整SHA为`692ada9f11f762e959352762e0a295c13be659f7`，B承接A的`0a86273c602d1698a96962730dd5c67557b08e88`；main基点`960984c34487244f31f1238dffc2bd9faaa292e7`。对B的部分为A独立核对；A新增两项局部修复由A自检，并分别保留修复前反例与当前验证，不冒充另一执行者独立审查A的改动。原B自检不改写成A当时通过。

### 18.1 现场、提交字节与旧证据

起始HEAD、分支和用户指定一致，工作区干净，无用户后续修改。只读远端查询只有main，未推送本阶段；本轮不修改main/备份历史，不启动子Agent或跨聊天发送。按入口、唯一计划、有效决定、事项/用户待办与适用规则完成触发阅读，并找到仓库外本地参考索引，只读使用。

`artifacts/a-m1-r3-review/input-package-preservation-2.json`逐项核对233个输入的提交blob、索引、工作树和B回执，均匹配`0c47d1764921fb2cd4898eb16211d821873c6ca0a2e9a29ffa3003b6a339ae72`；70个文档输入、19个命令回执及原日志摘要一致。m1-r3-11的24个成员/209,444,628字节、编译日志和项目/第三方许可均核对，manifest原`git_commit=0a86273…`及`source_dirty=true`保持原样。30个保留包的原清单逐一一致；63份正式/失败原始样本与保留measure二进制另存只读摘要。初次A审计的内联第三迁移正则漏掉换行/尾逗号，形成一条审计假阴性；修正审计器后通过，首份失败回执保留，未修改迁移或生产断言。

原§13～15字节、迁移1～5、m1/r2映射及原98/133必需用例、A探针、closeout、主页事件/媒体节点和正确WBI源码不变。WBI独立复跑使用`artifacts/a-m1-real-readiness/wbi-verified-probe`，7/7通过；33字符错误探针仍只作历史。

修改前实际执行`python scripts/stage_evidence.py check artifacts/m1-r3/final-bundle.json`和`python scripts/verify.py --stage-bundle artifacts/m1-r3/final-bundle.json`，均退出1，且各有相同30条拒绝：25条关联需求、四个真实用例、缺一组真实产品链回执。回执`gate-baseline-stage`、`gate-baseline-verify`保留；无陈旧源码、包或资源错误。B的322个唯一自动用例、169必需/165已有、87/68/19及43条完整映射证据均核对。

### 18.2 语义复核、定向执行和完整资源复算

F-023的确认已绑定原消费者请求序号；F-024恢复及选择触发器保留automatic/accepted/kept审批边界；F-025物化键/receiving/ready/commit核对任务所有权与兼容旧键。原三个A独立探针副本3/3通过，当前返工、生产协议及有界传输合并18/18通过。共享候选仍存在下述比较文案局部残留，按新F-031单列，不抹去原pending→stale故障的已修事实。

F-027固定端点的SecSDK派生、实际发出字节和跨域剥离由Core处理；F-028窄媒体域及同地址HTTPS规范化保持逐跳/IP/媒体零Cookie约束；F-029的8MiB请求、12MiB逻辑消息、1MiB普通帧、192KiB分片/4组/30秒界限及故障断言均核对。Core C14定向10/10、协议分片2/2、Bilibili27/27、Douyin12/12通过，完整真实产品链仍不能由这些结果替代。

P14预览五分钟、身份换代、幂等、取消/重开、必要资源和实际规格变化、DASH必要音轨、旧选择升级确认、one_shot数据库扫描屏障均核对；来源服务5/5、原C11的19/19及对象恢复8/8实际通过。新增发现F-032：已确认录制的明确停播被预览守护挡住，已直接修复，见下节。未改变既有F-018～022和F-026的核实范围。

A的`metrics-recompute.json/.log`及`independent-metrics.json`不调用生产derive，独立复算resource-formal-4全部18项指标一致：934个连续样本，窗口4665.860秒、最大间隔5.016秒、闲置905.157秒、录制3621.222秒；349个同时2下载+2录制点、350个四录制点、任务错误0，21个相邻观察区间两站同operation字节同时正增长。热查询p95为288.3109ms，完整Core重开/恢复及IPC可达2.201118秒，包含202个未完成普通任务；原生命周期文件、原构建/保存二进制和GUI关闭后字节增长均核对。采样中去重后的2900次查询与独立查询文件的2904次分别统计；完整查询文件热p95为288.5852ms，仍满足500ms，没有删慢样本或将两种口径混写。

resource-formal-2的945样本和恢复45.246460秒超限继续保留为failed；resource-formal-3的最终回执/benchmark/原始采样仍不存在，progress仍不可解析，其存活文件摘要与中断记录一致，没有补造计数。F-030分页64条、清理后每页FULL确认及越界拒绝经源码、8项恢复测试和上述完整长测核实；A修改后新输入须另建新包/长测，不换绑这些旧事实。

### 18.3 A局部修复与新反例

新发现F-031、F-032的触发、修复及当前状态只维护于[§16.3](#f-031)。两项均由A直接修复，独立失败和定向通过回执保留；当前包及完整资源证据在下一节单独绑定。

### 18.4 当前修复输入的最终验证

当前233项规范输入集合为`fed27badf50de158f304e0aa14c9eb4038acb29ba9b268322e84ed46cdb4c266`；相对被审B有7项变化：两份Core实现、两份测试、现有门禁必需名单、独立r3映射和归档契约。全部输入与Git过滤后的规范字节一致。原98/133项以及B的249个Rust用例全部保留，新加两项Rust反例；独立r3的171个必需用例中167个已通过，仍缺四个真实链。87/68/19和43条完整映射证据的口径不变。

| 当前回执 / 实际命令 | 结果与范围 |
| --- | --- |
| `final1-prebuild` / `cargo build --workspace --locked --offline` | 退出0，先构建当前生产进程及探针 |
| `final1-rust` / `cargo test --workspace --locked --offline --no-fail-fast` | 251/251，0失败/忽略；原249项及两个新增反例全部具名 |
| `final1-fmt` / `cargo fmt --all -- --check` | 退出0 |
| `final1-clippy` / `cargo clippy --workspace --locked --offline --all-targets -- -D warnings` | 退出0 |
| `final1-python` / `python -m unittest discover -s scripts/tests -v` | 19/19，原治理/聚合拒绝断言保持 |
| `final3-ui` / `corepack.cmd pnpm -C apps/desktop exec vitest run --reporter=verbose` | 52/52，原主页事件/媒体节点和链接交互守护保持 |
| `final3-verify` / `python scripts/verify.py --all --json` | 五套件5/5，277.695秒，类型/生产构建、离线、恢复、协议和资源规则通过 |
| `final3-original-probes` / 原三项A探针副本的`cargo test --manifest-path artifacts/m1-r3/original-r2-probes-current/Cargo.toml --locked --offline --target-dir target --no-fail-fast` | 3/3；原文件和失败回执不变 |
| `final3-wbi` / `cargo test --manifest-path artifacts/a-m1-real-readiness/wbi-verified-probe/Cargo.toml --locked --offline --target-dir target` | 7/7，正确32字符向量和原导入测试 |
| `final4-new-independent-probes` / `cargo test --manifest-path artifacts/a-m1-r3-review/semantic-probes/Cargo.toml --locked --offline --target-dir target --lib --no-fail-fast` | 8/8，含新增质量比较与明确停播独立探针 |
| `final3-package` / `python scripts/package_windows.py` | 退出0，80.955秒，实际生成新目录m1-r3-12 |
| `final3-package-check` / `python scripts/check_package.py dist/trace-0.1.0-windows-x64-m1-r3-12` | 2/2，完整包及实际最小化桌面关闭/重连/退出；四类test回执共324个唯一自动用例 |

上表所有回执均为sourceUnchanged=true并精确对应当前输入。m1-r3-12有24个成员、209,423,636字节；manifest摘要`e5ff3efafcf3ab6ea630a7338c696eac815a9a1feaac7cb92549816ab7e36a12`，SHA256SUMS自身摘要`1d839206f69a79f5277d27f512fb8c92424f134ced687699a6aad85edf66e019`。原构建HEAD为被审B的692ada9f…，dirty=true，原字段不修改；最终提交用规范输入逐blob对应。包清单、媒体锁及全部许可均独立核对。1280/760宽的既有B链接页截图已查看，A未改前端，原截图仍只证明其原包；新包窗口行为由本次实际包检查证明。

环境故障保留：`final1-ui`在测试启动前缺std-env；按原锁文件普通离线install恢复部分依赖后，`final2-ui`暴露estree-walker解析错误。离线force因缺缓存tarball退出1，随后`corepack.cmd pnpm -C apps/desktop install --frozen-lockfile --force`恢复成功，再得52项通过。所有失败、恢复命令与原输出保留，没有改版本或锁文件。另一次同名独立审查crate共用target命中了原3项可执行文件，`final3-new-independent-probes`仅实际执行3项，不能算新增探针通过；A为新crate使用独立package名称并保持原crate文件，`final4`明确运行8项且通过。原3项旧记录及本次中间记录均保留。

<a id="a-m1-r3-final-resource"></a>
### 18.5 当前源码完整正式资源复测

本轮使用新m1-r3-12包执行原完整规模与时长，资源子命令和包装回执均退出0，sourceUnchanged=true。下面是实际命令的仓库相对路径写法；完整argv和原日志保存在`resource-formal-1.json/.log`。运行中没有调整233项规范输入、包、测量二进制、规模、时长或门槛。

```powershell
python scripts/stage_evidence.py run --kind bench --id a-r3-resource-formal-1 --output artifacts/a-m1-r3-review/resource-formal-1.json -- python scripts/measure_m1.py artifacts/a-m1-r3-review/measure-final-1 --idle 1030 --seconds 3620 --entries 100000 --package dist/trace-0.1.0-windows-x64-m1-r3-12
```

| 当前正式观测 | 实际结果与边界 |
| --- | --- |
| 连续性与时长 | 934个原始样本，采样窗口4666.484秒，最大间隔5.016秒；有效闲置905.203秒、录制3620.750秒 |
| 实际负载 | 348个同时至少2下载+2录制点；纯录制350个点均为4路活跃；累计实收15,827,257,012字节，任务错误0 |
| 闲置CPU / 内存 | 全相关进程CPU均值0.189668%，私有工作集p95 50.640625MiB；原目标1% / 150MiB |
| 四录制CPU / 内存 | CPU均值1.380012%，私有工作集p95 66.015625MiB；原目标5% / 300MiB |
| 查询 | 采样热查询p95 144.9766ms，冷连接p95 136.9484ms；原目标500 / 1500ms，未清OS文件缓存 |
| 起录 / 发现 | 起录p95 0.735985秒，发现p95 29.148050秒；符合10秒起录及原30秒轮询的发现公式 |
| 完整恢复 | 10万条库、202个未完成普通任务，Core完整重开/恢复且IPC可达1.161541秒；原目标30秒 |
| GUI生命周期 | 两次当前包内嵌页面、r2/r3查询和同一Core重连核对；关闭释放最大3.765164秒，关闭后仍继续接收 |
| 其他指标 | 全窗口私有工作集峰值204,898,304字节、句柄峰值3837、队列峰值227；正式采样工作卷最小空闲844,348,731,392字节 |
| 设备与规模 | Windows 11 10.0.26300、AMD64、20逻辑处理器；10万发布/50万媒体元数据、512字正文、100普通/20直播检查目标、100ms延迟、50行查询、每流1,112,866字节/秒 |

`current-metrics-recompute.json/.log`实际执行A的`audit_current_metrics.py`，不调用生产derive；`current-independent-metrics.json`中18项独立复算全部一致，并核对原始生命周期、工作量、查询文件、构建日志、包和保留二进制。采样去重后2904次查询，独立完整查询文件有2908次，后者热p95为144.9766ms；两个口径分别保留，没有删除慢样本。只读物化观察另有56个相邻区间，两站同一operation的实际字节同时增长；接收开始前的空记录保留且不计为字节增长。

原始样本SHA-256为`cda1f4538550ee0b3c840e03476bdf709ebcac112de22feede0dea8f0da030f0`。新measure二进制独立保存于`measure-final-1/preserved-binaries/`：`m1_resource_bench.exe`为`0ddaf8bb84a22b7329eb8290a94c613ab936a5bbfdb78a82f7178607d1e08447`，`m1_bench_adapter.exe`为`18ad5e46a8c55977ac41836a5b3349db498450a655fc405fbfeec1f50cdec00c`；与本次实际运行和target/measure逐项一致。宿主/工作卷独立空间记录共158次，宿主最小剩余1,126,858,518,528字节。CPU包含所有产品相关进程、合成驱动器及已退出子进程的累计值。旧B包/二进制和全部旧成功、失败、中断样本不换绑；本结果仅证明本机完整合成负载，不能替代AC-29/30或原生体验。

<a id="a-m1-r3-final-gate"></a>
### 18.6 聚合门禁、审查结论与用户依赖（2026-10-13收尾）

当前[脱敏A摘要](../evidence/gate/m1-r3-a-review.json)分别保留B被审输入与A当前输入。新包、完整资源和四类test回执聚合为324个唯一自动用例；171个必需中167个有当前证据。原98/133项保留，独立r3仍为87条需求、68条适用、19条后继；43条完整映射通过，25条适用需求仍依赖四个真实产品链。

预聚合`artifacts/a-m1-r3-review/provisional-bundle.json`的两个CLI入口实际退出1，`gate-pre-stage`与`gate-pre-verify`各30条拒绝完全一致：25条关联需求、四个必需真实用例、缺一组真实生产Core回执。没有其他源码、包、资源或日志错误。文档/公开摘要完成后生成独立`final-bundle.json`，不覆盖原B bundle；最终实际入口如下，回执为`gate-final-stage`、`gate-final-verify`，仍各退出1且为相同30条拒绝。

```powershell
python scripts/stage_evidence.py check artifacts/a-m1-r3-review/final-bundle.json
python scripts/verify.py --stage-bundle artifacts/a-m1-r3-review/final-bundle.json
```

本次B实现与对应证据的A集中复核在上述范围通过，F当前状态只见§16.3；A修复的F-031/032由A完成反例、全套、新包及完整资源自检。M1整体技术退出仍blocked、阶段仍rework-required：`c14_real_bilibili_video`、`c14_real_bilibili_live`、`c14_real_douyin_video`、`c14_real_douyin_live`未取得当前包的完整证据。既有A诊断、B旧部分跑次、合成套件都没有代填AC-29/30。本轮没有新的较大实现缺口需要集中交回B，也不要求B复审A。

ACT-002的当前对象/额度依赖、累计预算与停止条件不变；本轮A真实站点请求为0，Cookie无需重配。实际逐跑次参数及累计扣减另经`real-budget-audit.json`核对，用户当前事项仍只在[USER-ACTIONS](../../USER-ACTIONS.md#act-002)维护。依赖补齐后由Agent在同一M1继续当前包四个真实产品链；ACT-003原生整体验收not-run，ACT-004/005已完成反馈保留。

最终文档、公开内容、差异检查及`final-preservation`再次核对当前输入与受保护正文/映射/旧包/原始样本。修复、验证与审查记录归并同一个未推送DCO阶段提交，不改main或备份历史；最终完整SHA由Git实际读取并记录于本地`artifacts/a-m1-r3-review/git-delivery.json`和交付消息，不递归写入本报告。未执行push、PR、合并、发布、子Agent或跨聊天发送。

<a id="a-m1-r3-real-continuation"></a>
### 18.7 2026-10-13跨聊天续行：三项真实链通过及剩余依赖

本次由用户要求继续既有A工作，起始HEAD为`b075488ceefec7c3eded44dc850430fe27894b89`，已有USER-ACTIONS授权更新保留。未改变233项产品/规范输入、m1-r3-12包或原资源测量；原§18.6的30条拒绝是三项真实证据聚合前的历史结果，本节记录三项通过时的中间状态，最终以§18.8为准。原驱动在两站任务完成后误用`export.start`，实际契约为`storage.export.start`；本机恢复脚本通过正确Core命令导出和离线重开，预算文件哈希不变、额外站点请求0。两份原失败回执不改写，恢复视频回执为`resume-bilibili-video`与`resume-douyin-video`。

| 当前包真实用例 | 实测规格 | 保存时长 | 核实范围 |
| --- | --- | --- | --- |
| bilibili video | 3840×2160 / h264＋aac | 218.560秒 | 2个必要对象 / 349,454,117字节；Core范围读取、导出、完整本地解码通过 |
| douyin video | 1080×1920 / h264＋aac | 22.833秒 | 1个必要对象 / 9,352,828字节；Core范围读取、导出、完整本地解码通过 |
| douyin live | 1080×1920 / h264＋aac | 68.426秒 | 1个必要对象 / 53,090,163字节；Core范围读取、导出、完整本地解码通过 |

Douyin直播新跑次`real-douyin-live-03`实际退出0；同Core实例、关闭GUI期间继续、重新打开停止、封片、导出及离线范围读取均执行时持久化于新目录。旧两站直播对象在本次离线恢复中均完整解码，封片时长分别72.050/67.417秒，但原内存生命周期字段未保存；严格real门禁拒绝两条旧直播回执，不补造Core实例。新Douyin直播取代其旧不完整回执，仅计一次必需用例。

Bilibili本机补跑`real-bilibili-live-02`因Python导入路径缺失在联网前退出；修正仅限忽略目录驱动。`real-bilibili-live-03`产生5次元数据/0媒体，GUI选择成功后后台/最小化断言失败，原回执仅存AssertionError、没有细分原因。驱动改为最多5秒观察等待后，`real-bilibili-live-04`确认直播成功，12次元数据/0媒体；媒体CDN返回302后record任务记录TransientNetwork，具体下一跳失败原因unknown，重解析后额度停止。没有证据证明挑战、Cookie失效或具体TLS/DNS/域原因，不修改窄CDN守护，不继续碰撞。Douyin旧补跑`live-douyin-02`的8次元数据/0媒体及原失败也继续计入累计。

三条真实回执由`real_chain_gate.validate_real`重新读取源输入、当前包manifest、Core目录快照、认证上下文、逐对象摘要、导出和解码，只有`missing one of four real product cases`，无三条回执自身错误。自动套件324项与三项真实用例合计327个唯一身份，171个必需中170个覆盖。独立r3映射仍87/68/19，43条需求完整、25条需全部四项真实链；实际assemble产生`artifacts/a-m1-r3-review/three-real-bundle-1.json`并退出1，27条拒绝由25条关联需求、缺Bilibili直播必需用例和未齐四项真实链组成。这份中间bundle未单独运行两个CLI检查（not-run），最终入口在§18.8实际执行。原B bundle和A原final-bundle保留，不换绑旧回执。该中间状态下AC-29技术证据passed，AC-30与M1技术退出blocked，原生产品验收not-run。

当前累计扣减和用户依赖只见ACT-002；本地`continuation-audit.json`保留逐跑次预算文件哈希。持久化媒体计数低于实时回执可能源自最后不足1MiB计数块未刷新，所以额度始终按各跑次`max(media_bytes,media_reserved)`保守合计。Bilibili60/60次、424,673,280字节预留；Douyin46/50次、124,780,544字节预留；每站仍≤512MiB。三项当前包证据、旧已封片对象及全部失败保留，无用户档案覆盖、秘密出库或远端操作。仅文档和本机证据变化，Rust/前端/正式资源重跑not-run，继续使用逐输入核对仍有效的原回执。


<a id="a-m1-r3-technical-closeout"></a>
### 18.8 2026-10-14技术收尾：10月13日四项真实链通过

用户在§18.7之后再次指定同一Bilibili直播对象并授权追加10次元数据；总上限按ACT-002改为70次，所有此前60次与媒体计数继续纳入。新隔离跑次`real-bilibili-live-05`实际使用10次元数据、38,139,804实收媒体字节，原命令回执退出0且sourceUnchanged=true。与前三条真实链使用同一m1-r3-12包和233项规范输入；仅忽略目录的证据驱动补导入路径、正确export命令、最多5秒后台状态观察和执行时checkpoint，不改产品源码、规范、守护或既有断言。

| 当前包完整真实用例 | 实际视频 / 音轨 | 完整保存时长 | 必要对象 / 文件字节 |
| --- | --- | --- | --- |
| bilibili video | 3840×2160 / h264＋aac | 218.560秒 | 2个 / 349,454,117字节 |
| douyin video | 1080×1920 / h264＋aac | 22.833秒 | 1个 / 9,352,828字节 |
| douyin live | 1080×1920 / h264＋aac | 68.426秒 | 1个 / 53,090,163字节 |
| bilibili live | 1920×1080 / h264＋aac | 68.869秒 | 1个 / 38,127,006字节 |

两视频分别显示质量并由桌面明确选择；Bilibili保存4K AVC及独立必要AAC，派生播放文件完整解码。两直播分别选流、Core实收、关闭GUI后同一实例持续、重开GUI停止封片、Core重启离线读取/范围字节、导出哈希和所有必要文件完整解码。`complete-audit.json`额外匹配执行时checkpoint、原GUI关闭日志/停止结果和生命周期时钟；四份真实回执均由原`real_chain_gate`逐项验证，无诊断文件导入对象库、无作者scan任务，认证上下文和物化所有权保持。旧72.050/67.417秒对象与漏存生命周期、窗口断言、CDN重定向/预算停止及其他原失败继续保留，未改成历史通过。

累计Bilibili70/70次，实收计数合计461,968,624字节、保守预留463,470,592；Douyin46/50次，实收计数合计124,014,548字节、保守预留124,780,544，均在每站512MiB内。所有跑次最小请求间隔复核≥2秒（计时浮点容差仅用于核对）。实时回执和持久块计数分别保留，累计以各跑次最大计数/预留扣减。此次成功不解释先前未记录的CDN下一跳故障原因，不宣称全部房间/匿名会员/全类型或全部历史覆盖。

预聚合`four-real-bundle-pre.json`已退出0；最终独立bundle为`artifacts/a-m1-r3-review/four-real-bundle-final.json`。328个唯一用例全部passed，其中324个原自动用例、4个获准真实链；171/171必需用例、87条需求中的68条M1适用全部有完整证据，19条后继not-run。当前包24成员、原构建HEAD/dirty、233项输入集合`fed27badf50de158f304e0aa14c9eb4038acb29ba9b268322e84ed46cdb4c266`及934连续资源样本/18指标均保留原绑定。原B/A/三项partial bundle保留各自文档和回执摘要；不覆盖旧数据。

最终实际检查入口为：

```powershell
python scripts/stage_evidence.py check artifacts/a-m1-r3-review/four-real-bundle-final.json
python scripts/verify.py --stage-bundle artifacts/a-m1-r3-review/four-real-bundle-final.json
```

两个最终入口与文档、公开内容、差异检查退出0，完整源码/包/资源/真实链/文档聚合无错误。当前B实现及被审证据的A集中复核passed，A局部F-031/032及本次获准真实链按A自检分别标注；AC-29/30技术证据passed，M1技术退出passed，无必须开发缺口或未结返工。阶段accepted仅指技术交付；ACT-003原生声音/IME/缩放/外部程序仍not-run，其他真实类型/全历史、Linux、安装器、签名、发布不声明通过。ACT-002指定对象验证完成，用户当前动作仅在USER-ACTIONS维护。

本次仅文档和忽略目录证据变化，原233项输入逐blob与HEAD一致、包成员及原正式资源结果复核有效；Rust/前端全套和正式长测重新执行not-run，无变化范围复用原完整回执。文档登记、能力表与公开摘要同步，当前profile、凭据、用户资料、旧包/失败证据均保留，不进行清理。通用实测脚本仍需迁入本机驱动改进，作为非阻塞工具维护LOOP-001在状态页承接；完整当前包验证已使用保存的修正驱动和原严格门禁，不改变规范输入来换绑旧包。按既有授权amend同一个未推送DCO阶段提交；不push、建PR、合并、发布、启动子Agent或跨聊天发送，不要求B再复审A。

<a id="a-m1-user-scope-review"></a>
## 19. A 按用户界定范围复核 M1（2026-10-15）

### 19.1 范围与基点

- **审查者：** A（本会话执行者，自审本轮文档改动；B 未参与本轮）。
- **依据：** 用户 2026-10-15 指示（[DEC-029](../decisions/confirmed-decisions.md#dec-029)）：M1 应完成 Bilibili 与 Douyin 的直播录制、单个视频解析和用户视频列表解析；同时同步开发流程与文档规范、给出代码结构优化方案。计划范围仍以 [TRACE-M1-01 r3](stage-plan.md) 为准，本节不改变它。
- **基点：** 分支 `codex/m1-two-site-archive`，阶段提交 `1dd1afb6882c852c4af26d7da444c5928049ad49`（基于 `main@960984c`，未推送）。本轮只改文档，产品源码、锁文件和四份包输入文档（需求、映射、计划、归档契约）均未改动。

### 19.2 门禁（在阶段提交的源码上实跑）

| 检查 | 结果 |
| --- | --- |
| `python scripts/verify.py --all` | 5/5 套件通过（desktop、offline、recovery、resources、site），exit 0 |
| `cargo fmt --all -- --check` | exit 0 |
| `cargo clippy --workspace --locked --all-targets -- -D warnings` | exit 0，无警告 |
| `python -m unittest discover -s scripts/tests` | 19 项通过 |
| 文档、公开内容、`git diff --check` | 本轮文档改动后重跑，结果见 19.6 |

### 19.3 三项能力逐项结论

| 能力 | 证据 | 结论 |
| --- | --- | --- |
| 直播录制（两站） | 2026-10-13 生产包 m1-r3-12 真实产品链：Bilibili 1920×1080＋AAC 68.869 秒、Douyin 1080×1920＋AAC 68.426 秒；选流、Core 实收、关闭 GUI 后继续、重开停止封片、离线读取、导出、完整解码（[§18.8](#a-m1-r3-technical-closeout)） | passed（技术） |
| 单个视频解析（两站） | 同日同包：Bilibili 3840×2160＋独立 AAC 218.560 秒、Douyin 1080×1920＋AAC 22.833 秒；链接解析、列出可得清晰度、明确选择后入库与解码（§18.8） | passed（技术） |
| 用户视频列表解析（两站） | 自动：site 套件的协议夹具与生产适配器进程、offline 套件的扫描续页/游标/中断恢复。真实：2026-10-15 用 m1-r3-12 的生产适配器和 `trace-daemon site-check`（隔离 Profile、请求间隔 ≥2.5 秒、每次有请求上限）对用户此前提供的两站主页样本各取 2 页，见 19.4 | passed（协议层真实验证＋产品链自动验证） |

**限制：** 列表从订阅到入库的完整产品链只有自动测试，没有用真实作者跑完整订阅；更多页、全历史、匿名差异未验证。作者主页目前只能订阅，没有“预览后勾选”界面——这不是 M1 计划内的必须项，作为产品决定登记为 [Q-006](../../USER-ACTIONS.md#q-006)。

### 19.4 真实列表检查（2026-10-15）

| 站点 | 请求（全部 200） | 第 1 页 | 第 2 页 |
| --- | --- | --- | --- |
| Douyin | 登录探测、账号解析、投稿列表 ×2，共 4 次 | 20 条视频，`max_cursor` 前进，`has_more` 为真 | 原始 20 条中 19 条视频（1 条非视频按 video 通道筛除），游标继续前进 |
| Bilibili | nav ×3、card、`x/space/wbi/arc/search` ×2，共 6 次 | 30 条视频，WBI 签名通过 | 30 条视频，页码前进，仍有更多 |

两站两页均按发布时间衔接、无重叠。合计 10 次元数据请求、0 字节媒体，没有挑战或限流；不写档案、不建订阅。原始回执只在本机忽略目录，本报告不记录账号、作品清单或签名 URL；授权依据与累计记在 [ACT-002](../../USER-ACTIONS.md#act-002)。

### 19.5 复核发现

| 编号 / 严重度 | 发现 | 处理 |
| --- | --- | --- |
| F-033 / 建议 | Bilibili 空间视频列表只带 mid/pn/ps/order 与 WBI 签名，未带部分浏览器实现会附加的 `dm_img_*`、`w_webid` 等风控参数；本次未复现 -352，更高频或匿名时有风险 | [LOOP-002](status.md#followup-loops)，随代码整理 R2 补齐并加固定向量 |
| F-034 / 建议 | 代码结构：`ipc/dispatch.rs::execute` 1310 行、SQL 分布在 26 个非测试文件、两站适配器两套运行时、69 个手写 Tauri 转发、147 个测试按阶段/轮次命名等 | [代码结构优化方案](../design/code-structure-refactor.md)，排期见 [Q-005](../../USER-ACTIONS.md#q-005) |
| F-035 / 建议 | 证据工具把全部文档绑定进包/聚合指纹并写死 87/68 需求数，文档修改让已验收的 bundle 报 stale | [LOOP-003](status.md#followup-loops)；落地前按[纯文档变化规则](../dev-rules/quality-gates.md#doc-only-changes) |
| F-036 / 建议 | 生成物约 150 GB（`artifacts/` 74 GB/644 项、`target/` 72 GB、`dist/` 31 个包 6 GB），曾导致开发盘写满（ACT-005） | 下一阶段开始时按[阶段整理](../governance/documentation-policy.md#stage-cleanup)清理，保留 ACT-003 在用的包与样本 |

没有 P0/P1/P2 缺陷，不需要交回 B 返工。

### 19.6 流程文档同步与本轮文档门禁

参照用户另一项目的流程规范，更新了 [AGENTS.md](../../AGENTS.md)、[REVIEW.md](../../REVIEW.md)、[开发流程](../dev-rules/development-workflow.md)、[文档治理](../governance/documentation-policy.md)（阶段整理与去阶段标签）、[本地参考规则](../dev-rules/local-reference-and-files.md)、[质量门禁](../dev-rules/quality-gates.md#doc-only-changes)、[交接模板](../templates/agent-handoff.md)，并按同一格式重写 [USER-ACTIONS](../../USER-ACTIONS.md)（新增 ACT-006、Q-005—Q-007，旧锚点保留）。用户新增的订阅规则目录与“支持的网站”页面记为[路线候选](roadmap-and-acceptance.md#待实现的后继范围)（[DEC-030](../decisions/confirmed-decisions.md#dec-030)）；本地参考索引在仓库外更新，仓库内不写项目名。

文档改动后的门禁：

| 检查 | 结果 |
| --- | --- |
| `python scripts/check_docs.py` | passed：50 份文档、685 个本地链接、87 条需求、0 错误 |
| `python scripts/check_publication.py` | passed：0 处公开内容问题 |
| `python -m unittest discover -s scripts/tests` | 19 项通过 |
| `git diff --check` | exit 0 |
| `python scripts/stage_evidence.py check artifacts/a-m1-r3-review/four-real-bundle-final.json` | exit 1，唯一原因 `stale or incomplete documents` |
| `python scripts/verify.py --stage-bundle artifacts/a-m1-r3-review/four-real-bundle-final.json` | exit 1，唯一原因同上 |

两个聚合入口的拒绝是本轮文档改动的预期结果：按[纯文档变化规则](../dev-rules/quality-gates.md#doc-only-changes)，旧 bundle 继续只证明它绑定的文档版本，不改写摘要去重新绑定。本轮没有改动产品源码、锁文件或四份包输入文档，m1-r3-12 包、正式资源测量和四项真实产品链的证据仍对应原输入有效；Rust/前端全套已在 19.2 重跑通过，正式长测未重跑（not-run，源码无变化）。

### 19.7 结论与下一步

- **实现交付：** 用户界定的三项能力在两站都已实现并有真实证据。
- **技术审查：** passed（A 复核；本轮文档改动为 A 自审）。没有需要 B 返工的缺陷。
- **产品验收：** 原生体验 [ACT-003](../../USER-ACTIONS.md#act-003) 仍 not-run，不是远端授权的前提。
- **首选下一步：** 请用户授权 [ACT-006](../../USER-ACTIONS.md#act-006)（push 阶段分支、创建 PR、`repository-quality` 通过后 squash 合并）。合并后 A 按 [Q-005](../../USER-ACTIONS.md#q-005) 规划下一阶段：先做行为不变的[代码结构整理](../design/code-structure-refactor.md)与工作区清理，再做订阅规则目录与“支持的网站”页面。本轮 USER-ACTIONS 新增 ACT-006、Q-005、Q-006、Q-007，ACT-002 增加 2026-10-15 列表检查记录；无阻塞项。
- **远端授权：** 用户 2026-10-15 回复“授权 ACT-006 的（1）（2）（3）”。本节随阶段提交 push，并经 PR 在 `repository-quality` 通过后 squash 合并；合并结果由 Git 历史记录，并在下一阶段开始时写入交付状态。
