# 开发与上游事项总表

更新：2026-10-21。本文只维护开发/上游事项；用户配置、授权与人工检查只见[USER-ACTIONS](../../USER-ACTIONS.md)，非阻塞LOOP只在[状态](../delivery/status.md#followup-loops)。上一阶段的 F-001～F-036 详情与关闭记录见[固定报告](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md)；本阶段新发现从 F-037 起编号，写在[阶段报告](../delivery/stage-report.md)。真实产品链和各站规则验证是本阶段必须范围，不能转成用户测试或后继LOOP；来源诊断和自动套件不代替真实证据。

<a id="开源集中处理"></a>
## 当前开发事项

| 编号 / 状态 | 内容与依据 | 责任、动作与关闭条件 |
| --- | --- | --- |
| F-071 / resolved | 首轮按全部相关条目比较活动差异，观察与详情请求绑定成功 observe 同代执行；三条独立反例、原四类反例及全部正例通过，见[报告 §19](../delivery/stage-report.md#a-m2-r7f-review) | A 已独立关闭；原失败保留，三条新回归确实在 56 项脚本全集执行；不代表真实增量规则已通过 |
| F-087 / resolved | 拖动区与主页事件缺少 Tauri ACL；A 的仅 local/main 最小 capability 已保留在 `-19`，真实拖动区与 listen/unlisten 守护独立复验通过，见[报告 §19](../delivery/stage-report.md#a-m2-r7f-review) | 保留最小权限与包守护，不向远端 origin 或其它窗口扩权 |
| F-088 / resolved | 支持的 720px 窗口隐藏窗口错误；移除隐藏规则，组件错误回归与 `-19` WebView scoped CSS 守护通过，见[报告 §19](../delivery/stage-report.md#a-m2-r7f-review) | 修复已纳入冻结包，产品原生体验仍见 ACT-003 |
| OSS-001 / resolved | Vue、Naive UI、Vite工程已建立并验证；[现有选型](../research/2026-09-26-technology-stack.md) | B保持锁文件，A审影响范围；新阶段不重选框架 |
| OSS-002 / resolved | Tauri/IPC/受控媒体原实现缺陷已技术复核，守护见[状态](../delivery/status.md) | 新增类型延续有界媒体与快照重连；原生体验仅链接ACT-003 |
| OSS-003 / resolved | Tokio/SQLite/FTS/租约与恢复基线已核实，逐版本迁移与搜索共享分页反例通过 | 大库资源实测及当前版本证据见阶段报告，基线resolved不替代新增范围复核 |
| OSS-004 / resolved | 锁定LGPL3 shared媒体资产、异步调用与本地包已核实，[媒体研究](../research/2026-10-01-ffmpeg-integration.md) | 解决技术集成问题；正式许可/分发只见Q-004，不由resolved代替授权 |
| OSS-005 / resolved | 预构建、故障反例、Rust/Vue套件已经建立，原F-001/017修复保留 | 逐C必需用例、源码/包/采样聚合及拒绝反例由 `scripts/stage.py` 核对，A按最终证据核查 |
| OSS-006 / resolved | TypeScript6兼容已验证；TS7旧尝试失败保留 | 后续升级先复现类型门禁，不跳过检查 |
| OSS-007 / resolved | A 已核对 Bilibili 动态/转发、opus、HTML/JSON ops专栏/文集、视频多P/音乐/字幕及FLV/HLS的生产入口、固定来源与合成断言 | 技术关闭限于该协议集成范围；[能力表](../research/bilibili-capabilities.md)、[审查覆盖](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md#a-m1-review)；真实条件仅链接ACT-002 |
| OSS-008 / resolved | A 已核对 Douyin 生产路由、固定签名向量、type163详情全文/30图、实况/配乐、合集和直播；来源长度丢失按 F-018 局部修复并验证 | 技术关闭限于协议集成；本次真实登录验证通过，权限/完整内容/风控可用性未证实；新包与共享执行权/新增功能按同一计划处理 |
| OSS-009 / resolved | A 已核对全文对象/JSON字符串、列表摘要不降级全文、必要资源和未知结构限制，以及生产进程合成用例的故障断言 | 关闭原资料/合成缺口，不宣称真实响应稳定；具体来源在解析研究，真实条件仅链接ACT-002 |
| OSS-010 / resolved | A 已核对 m3u8-rs 6.0.1 的 NONE/无IV和key/MAP包装、HLS嵌套校验与四个封片提交窗口，并定向复跑 | 保留上游版本约束与反例，升级时先撤包装复验，不因issue关闭直接移除。未向上游发送issue/PR；[补证](../research/site-parsing-current.md#6-b-实施补证2026-10-07) |
| OSS-011 / resolved | Core内签名上下文、有界大页、窄直播CDN与链接显式选择已核实；两站四项获准真实链、全部必要对象、封片/离线读取/导出/完整解码通过 | A按[完整收尾](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md#a-m1-r3-technical-closeout)核对技术退出条件后关闭。原诊断与失败保留；只证明指定对象，不宣称全部类型/历史/账号覆盖。原生验收与更广实测只见ACT-003/002 |
| OSS-012 / in-progress | YouTube 媒体地址依赖播放器脚本解密与 PO Token，计划整合锁定版本的外部解析工具及其 JavaScript 运行时；[开源候选](../research/open-source-candidates.md#social-sites-2026-10-16)、[多站契约 §10](../design/multi-site-contracts.md#youtube-media) | B 已实现并真实验证（2026-10-17）：锁定 yt-dlp 2026.08.19 与 Deno 2.9.7，固定参数、超时、无 Cookie、临时目录提交；一个公开频道的链接下载与勾选 2 条下载均完整入库；视频服务器对单次长连接限速，Core 对该主机按 10 MiB 分段请求（[阶段报告](../delivery/stage-report.md#m2-rule-status)）。工具缺失降级反例通过。用户 2026-10-18 答复 Q-009：继续随包锁定，并增加设置页手动更新（DEC-039）；B 第四轮实现签名/摘要校验、并排安装与回退后再关闭。YouTube 的 Shorts、社区帖子订阅与自动下载开的增量也在第四轮补完 |
| OSS-013 / in-progress | 小红书笔记列表续页、TikTok 作品列表需要前端签名；X 部分接口需要事务 ID 请求头；[社交站点研究](../research/social-sites-current.md) | 结论按站点记录（2026-10-18）：TikTok 作品列表在真实站点无签名返回 2 页，视频文件由 Core 带上页面会话 Cookie 取得（不再 403），预览勾选下载与视频链接通过，增量检查中站点答复“无数据”（限流）后停止；小红书只取首屏，续页签名依赖 Cookie 值。用户 2026-10-18 答复 S-003，按 DEC-041 只把设备标识 `a1` 交给适配器签名，订阅与预览取完整历史；B 第四轮用固定向量与真实检查实现后再关闭。X 的查询标识与特性开关随适配器发布、被拒绝时才从页面脚本发现，事务 ID 请求头按公开算法计算（真实站点没有拒绝未签名请求，签名只经固定向量验证），列表 2 页、预览与图片推文链接通过；原生视频与两次增量在第四轮补。不引入浏览器自动化。额度见 [ACT-007](../../USER-ACTIONS.md#act-007) |

状态使用open/in-progress/resolved/deferred；延期须满足工作流条件，有影响/替代/责任/复测，不能延期本阶段必须项。A核实F后才关闭；B自检只标已修待复核。

## 用户事项导航

Q-002使用规模/偏好、Q-004许可/分发、Q-009已答复（DEC-039）、Q-011命名（open）、Q-012已答复（DEC-040）、ACT-002真实站点、ACT-003原生验收、ACT-004视觉反馈、ACT-005开发盘恢复、ACT-008当前阶段远端授权的状态及步骤只维护于[USER-ACTIONS](../../USER-ACTIONS.md)。Q-001原分批顺序由DEC-020整阶段替代；Q-003蓝白方向已确认；ACT-001本地Git初始化已完成。远端已有保护和治理CI；当前阶段的远端操作尚未授权。

## 历史依据

旧事项的原始解决/失败记录见[固定基线总表](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/decisions/open-questions.md)，原F详情与技术关闭见[固定审查](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/evidence/2026-10-01-m0-m1a-review.md)。当前计划不重新打开原已核实缺陷；新增类型仍须守住相同不变量。
