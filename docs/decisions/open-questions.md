# 开发与上游事项总表

更新：2026-10-13。本文只维护开发/上游事项；用户配置、授权与人工检查只见[USER-ACTIONS](../../USER-ACTIONS.md)，非阻塞LOOP只在[状态](../delivery/status.md#followup-loops)。当前F-018～032详情与状态由[报告§16.3](../delivery/stage-report.md#f-023)维护，B被审交付保留在[§17](../delivery/stage-report.md#b-m1-r3-delivery)，A集中复核与局部修复见[§18](../delivery/stage-report.md#a-m1-r3-review)。P14/C14四个真实产品链仍是本阶段必须范围，不能转成用户测试或后继LOOP；来源诊断和自动套件不代替AC-29/30。

<a id="开源集中处理"></a>
## 当前开发事项

| 编号 / 状态 | 内容与依据 | 责任、动作与关闭条件 |
| --- | --- | --- |
| OSS-001 / resolved | Vue、Naive UI、Vite工程已建立并验证；[现有选型](../research/2026-09-26-technology-stack.md) | B保持锁文件，A审影响范围；新阶段不重选框架 |
| OSS-002 / resolved | Tauri/IPC/受控媒体原实现缺陷已技术复核，守护见[状态](../delivery/status.md) | 新增类型延续有界媒体与快照重连；原生体验仅链接ACT-003 |
| OSS-003 / resolved | Tokio/SQLite/FTS/租约与恢复基线已核实，M1新增schema3迁移与搜索共享分页反例通过 | 大库资源实测及当前版本证据见阶段报告，基线resolved不替代新增范围复核 |
| OSS-004 / resolved | 锁定LGPL3 shared媒体资产、异步调用与本地包已核实，[媒体研究](../research/2026-10-01-ffmpeg-integration.md) | 解决技术集成问题；正式许可/分发只见Q-004，不由resolved代替授权 |
| OSS-005 / resolved | 预构建、故障反例、Rust/Vue套件已经建立，原F-001/017修复保留 | M1补齐逐C必需用例、源码/包/采样/文档聚合及拒绝反例，A按最终证据核查 |
| OSS-006 / resolved | TypeScript6兼容已验证；TS7旧尝试失败保留 | 后续升级先复现类型门禁，不跳过检查 |
| OSS-007 / resolved | A 已核对 Bilibili 动态/转发、opus、HTML/JSON ops专栏/文集、视频多P/音乐/字幕及FLV/HLS的生产入口、固定来源与合成断言 | 技术关闭限于该协议集成范围；[能力表](../research/bilibili-capabilities.md)、[审查覆盖](../delivery/stage-report.md#a-m1-review)；真实条件仅链接ACT-002 |
| OSS-008 / resolved | A 已核对 Douyin 生产路由、固定签名向量、type163详情全文/30图、实况/配乐、合集和直播；来源长度丢失按 F-018 局部修复并验证 | 技术关闭限于协议集成；本次真实登录验证通过，权限/完整内容/风控可用性未证实；新包与共享执行权/新增功能按同一计划处理 |
| OSS-009 / resolved | A 已核对全文对象/JSON字符串、列表摘要不降级全文、必要资源和未知结构限制，以及生产进程合成用例的故障断言 | 关闭原资料/合成缺口，不宣称真实响应稳定；具体来源在解析研究，真实条件仅链接ACT-002 |
| OSS-010 / resolved | A 已核对 m3u8-rs 6.0.1 的 NONE/无IV和key/MAP包装、HLS嵌套校验与四个封片提交窗口，并定向复跑 | 保留上游版本约束与反例，升级时先撤包装复验，不因issue关闭直接移除。未向上游发送issue/PR；[补证](../research/site-parsing-current.md#6-b-实施补证2026-10-07) |
| OSS-011 / resolved | Core内签名上下文、有界大页、窄直播CDN与链接显式选择已核实；当前m1-r3-12四项获准真实链、全部必要对象、封片/离线读取/导出/完整解码通过 | A按[完整收尾§18.8](../delivery/stage-report.md#a-m1-r3-technical-closeout)核对技术退出条件后关闭。原诊断与失败保留；只证明指定对象，不宣称全部类型/历史/账号覆盖。原生验收与更广实测只见ACT-003/002 |

状态使用open/in-progress/resolved/deferred；延期须满足工作流条件，有影响/替代/责任/复测，不能延期本阶段必须项。A核实F后才关闭；B自检只标已修待复核。

## 用户事项导航

Q-002使用规模/偏好、Q-004许可/分发、ACT-002真实站点、ACT-003原生验收、ACT-004视觉反馈、ACT-005开发盘恢复的当前状态及步骤只维护于[USER-ACTIONS](../../USER-ACTIONS.md)。Q-001原分批顺序由DEC-020整阶段替代；Q-003蓝白方向已确认；ACT-001本地Git初始化已完成。远端已有保护和治理CI，M1远端操作尚未授权。

## 历史依据

旧事项的原始解决/失败记录见[固定基线总表](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/decisions/open-questions.md)，原F详情与技术关闭见[固定审查](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/evidence/2026-10-01-m0-m1a-review.md)。当前计划不重新打开原已核实缺陷；新增类型仍须守住相同不变量。
