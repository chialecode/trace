# 抖音归档与录制能力表

2026-10-13 获准实测（当时的生产包）：视频1080×1920 AVC＋AAC完整22.833秒；直播1080×1920 AVC＋AAC封片68.426秒。两链Core身份/任务/对象、桌面选择、直播GUI关闭继续、导出/离线重开及所有必要文件完整解码已核实，证据见[固定报告](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md#a-m1-r3-technical-closeout)；只适用两个指定对象，其他类型/全历史及匿名与会员质量差异未验证。

核对：2026-10-17。self登录判断保持，Core内SecSDK上下文、有界大页、直播CDN/HTTPS与视频/直播链接入口已接入，并随多站规则目录声明规则。真实生产视频接收、停播响应、原失败及完整链边界见[固定报告](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md#b-m1-r3-delivery)；本阶段的复验结果见[状态](../delivery/status.md)。A原有[来源诊断](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md#m1-real-source-readiness)与产品链证据分开。配置、对象和预算依赖只见[ACT-002](../../USER-ACTIONS.md#act-002)。

早期准备表、当时源码/官方资料与未知保存在[固定历史](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/research/douyin-capabilities.md)。现行来源与固定协议证据以[两站解析研究](site-parsing-current.md)为正本，不把第三方支持声明当作TRACE实测。

type163全文/资源关系与共享作用域/认证历史已由早期审查核对，F-018来源长度和F-020登录探测保持。当时的[包与长测](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md#b-m1-r2-delivery)只对应当时输入；候选交接、恢复和物化等修复的状态见[固定报告](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md#f-023)，不由能力表自行关闭。

| 类型/入口 | 当前生产行为 | 合成证据与限制 |
| --- | --- | --- |
| 账号、主页与短链 | `user/profile/other`解析sec_uid，保存uid、公开别名与资料观察；分享链接逐跳验证。跨站/账号不混合，昵称不作身份 | 身份、改名、长ID和跨账号隔离用例；访问者OAuth/open_id不自动映射 |
| 会话与签名 | `user/profile/self`核对会话的非零uid及sec_uid；访客ID不当登录证明。A-Bogus与Core内受保护详情/投稿SecSDK上下文分层，Cookie和派生访问头不进入插件/UI | 固定编码/查询顺序/摘要向量及实际Core跨域HTTP隔离通过；self登录与来源访问能力分开，不生成msToken。真实成功与拒绝只按§17具体跑次判断 |
| 投稿/动态订阅 | `aweme/post`，sec_user_id/max_cursor/count；大型JSON使用显式有界插件消息；dynamic lane按账号公开投稿归档视频/图集/长文 | 20条约1.45MB生产进程/Core回放完整通过；超限/缺片/坏摘要/并发/取消/重启失败不前移游标。置顶/乱序/空页has_more与无删除权威证据的unknown保持。2026-10-15生产二进制登录态对一位作者真实取2页：第1页20条视频，第2页20条中19条视频（1条非视频按video通道筛除）；max_cursor前进、仍有更多、两页无重叠 |
| 视频 | `aweme/detail`核对aweme_id与类型；候选play_addr/download_addr/bit_rate按实际宽高/FPS与质量偏好选择，封面独立 | 生产进程→Core→HTTP字节→对象→离线读取通过；地址名不证明无水印/原画，实际规格由probe确认 |
| 视频链接选择 | 明确展示当前来源候选、必要资源和实际声明规格；确认后持久一次性任务进入原对象库，媒体附公开UA/Referer而无Cookie | 链接预览/确认/取消/重启、降级及凭证换代有反例；原视频接收成功不代替最终整包的重开、导出与完整解码证据 |
| 图集/实况 | images和image_post_info；优先原始静态图，保留顺序/缺图占位、motion片、必要配乐，安全作者/合集字段 | 扁平/嵌套、不同尺寸、缺静态图但有实况、正文和配乐编辑/签名轮换反例；不能把封面当实况完整保存 |
| 长图文 | aweme_type=163，详情article_info/article_content为对象或JSON字符串；Markdown全文有序转类型块，30图、表格/列表/代码等安全保存 | 列表约499字符截断不能完成归档；必须请求详情。video.play_addr为背景MP3时保存为music。生产进程全文/资源/离线链通过，真实完整性仍not-run |
| 合集/系列 | 投稿→mix.list→series.list→mix.aweme分别有界续页；series_id用于成员查询，空页不能代表全量结束 | 独立游标和生产端点用例；集合页覆盖保存在条目观察，详情无mix_info不丢来源。独立付费专栏目录仍无足够资料 |
| 房间与直播 | 原sec_uid/reflow订阅入口保持；直接URL用web_rid/enter，身份取房间owner及其uid回退，外层观看者不作主播。选流进入共用录制服务，已停播可缺owner仍明确显示离线 | douyinliving.com按来源证据规范化同地址HTTPS，默认窄CDN策略、公开直播头和媒体零Cookie。保留FLV/HLS/音频直播的旧守护；直接直播最低规格、封片/离线证据按§17判断 |
| 弹幕/字幕 | 用户可选WebcastChatMessage有限历史JSON采样，消息ID去重、串房拒绝；外部HLS字幕依Core轨道捕获，画面字幕随原件 | 省略数/采样范围/失败独立，非全量WebSocket；真实站点是否提供外部字幕轨未验证 |
| 历史、质量和恢复 | 复用Core增量/深查、版本观察、必要资源验证、质量保留/回收、备份导出/新Profile恢复 | 两站共用C01～C08与原F守护，范围见[归档契约](../site-archive-contracts.md)，不建立第二套对象库 |

统一入口支持按需安全导入用户主动提供的Cookie、撤销和重新验证。真实执行仅按ACT-002已授权的账号、内容类型、页数、字节/时间与停止条件进行；`trace-daemon site-check`默认dry-run，具体协议检查用隔离输出目录和显式执行标志，不启动订阅或下载。遇权限/挑战停止。不能因代码接线、单次登录或合成通过宣称两站真实全功能已验收。

A同Cookie对照的self成功、detail/post缺上下文403及补齐后200保留；原20条/1,454,679字节超过旧固定预算的失败未改写。当前明确的大页协议和拒绝边界以[契约§9](../site-archive-contracts.md#direct-source-acquisition)为正本。原视频1080×1920、原画直播1920×1080约59.94fps仅适用于指定诊断样本，不推广成全站规格或全历史覆盖。
