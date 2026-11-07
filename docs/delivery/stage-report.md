# 阶段报告：M1（Agent B 重新检查与修复，Agent A 集中复核）

- **计划**：[`TRACE-M1-03` 修订 4](stage-plan.md#rev4)，执行角色 Agent B；2026-11-07 首轮复查记录为 F-001～F-013，2026-11-08 复查追加 F-014～F-025，A 集中复核修复 F-026～F-028，随后按首轮实机反馈（DEC-033）由 A 修复 F-029～F-032、B 完成修订 3 的 R1～R7 与补充复核 F-033～F-041 并经 A 集中复核关闭（附 F-042）；第二轮实机反馈（DEC-037）由 A 修复 F-043，B 完成修订 4 的 R8～R13，并按用户再次要求补充复核修复 F-044～F-055（[§13](#rev4-b-recheck)）；A 集中复核修订 4，并按第三轮实机反馈（DEC-038）直接修复 F-056～F-058（[§14](#a-review-rev4)）；第四轮实机反馈由 A 修复拖动卡顿 F-059 并按 DEC-039 改版界面（[§15](#feedback-4)）；第五轮实机反馈由 A 按 DEC-040 处理并修复 F-060、F-061（[§16](#feedback-5)）；阶段范围不变。
- **分支与被测基点**：`codex/m1-mygo-bilibili`；父提交为初始化提交 `b208ad6c5d2de24202226e76f279db01196c7879`（文档基线、A 的修复与 B 的实现/修订 3～4 返工及报告统一 amend 为唯一一个带 DCO 签名的本地阶段提交；最终 SHA 以交付消息和 `dist/trace-dev/VERSION.txt` 为准）。已只读核对远端引用，`origin/main` 仍只有初始化提交 `b208ad6`，阶段分支未推送；未 push、未创建 PR。
- **结论**：修订 3 的 R1～R7 与 F-033～F-041 已由 A 集中复核（[§10](#a-review-rev3)），F-042 与 F-043 已由 A 直接修复（[§11](#feedback-2)）；修订 4 及补充复核（R8～R13、F-044～F-055，[§13](#rev4-b-recheck)）已由 A 集中复核，第三轮实机反馈的 F-056～F-058 与设置调整由 A 直接修复（[§14](#a-review-rev4)），第四轮的 F-059 与界面改版（DEC-039）由 A 完成（[§15](#feedback-4)），第五轮的 DEC-040 与 F-060、F-061 由 A 完成（[§16](#feedback-5)）；完整门禁、69 张截图断言、开发包与隔离冒烟通过，最新验证见 [§16.5](#feedback-5-validation)。C01～C40 是自动化用例映射，测试通过只证明对应输入；A 通过不是产品验收，H-001 仍为 not-run。
- **第二轮实机反馈（2026-11-08）**：播放卡顿由 A 修复为 F-043（[§11](#feedback-2)），产品调整 R8～R13 由 B 完成（[§12](#rev4-b)，DEC-037）。
- **A 复核与实机反馈（2026-11-08）**：首轮复核结论、F-026～F-028 见 [§5](#a-review)；C01～C40 与测试函数的对应见 [§6](#case-map)；首轮实机反馈、ACT-006/007 与 B 修订 3 执行结果见 [§7](#feedback-1)、[§8](#rev3-b)；B 补充复核见 [§9](#b-recheck)；A 对修订 3 的集中复核见 [§10](#a-review-rev3)。

## 1. 复查发现与修复

以下为稳定问题编号；状态均为 **B 已修、待 A 复核**，不代替 A 关闭问题。

| 编号 / 严重度 | 原触发与结果 | 修复位置与实际行为 | 复验入口 |
| --- | --- | --- | --- |
| F-001 / P1 | 交互模式默认用了静音输出，播放画面没有持续按时钟推进；窗口关闭被取消时仍可能消失 | `internal/ui/app.go`、`internal/player/player.go`、`internal/player/mf_windows.go`、`internal/shell/shell.go`：交互输出接 MyGo 音频，安静模式显式注入静音输出；按音频时钟推进画面；音视频使用独立读取器，追帧不回拨音频；退出确认前阻止默认关窗 | `TestMFDecoderPureGoVideoAndAudio`、`TestPlayerDanmaku10kPerfSubtitlesAndNullSink`、`TestQuietModeTrayIconCloseToTrayAndMemory` |
| F-002 / P1 | 远程图片未获取，播放失败、缺弹幕字幕时注入示例内容；本机读取存在绕过白名单的后备拼接 | `internal/ui/app.go`、`internal/ui/overlays.go`、`internal/site/bilibili/client.go`：获取真实原图，无 Cookie/Referer；图片读取/解码限制；错误明确展示；移除合成媒体、示例弹幕字幕与路径后备；多 P/录像可切段；看图支持缩放、平移、适应窗口 | `TestOriginalImageAndMissingMediaNeverUseSyntheticFallback`；截图的真实合成夹具仅由工具准备 |
| F-003 / P1 | 未登录时启动或导航仍能进内容页；接口失效没有统一停活动任务；启动网络失败未标 unknown | `internal/ui/app.go`、`internal/service/backend.go`、`internal/site/bilibili/client.go`：启动/导航守卫、启动凭证检查、unknown 保留凭证、-101 统一事件与任务/录制取消；扫码续期与手动登录边界保留 | `TestAuthGuardAndNamedMultiAccountSubscriptionUI`、现有 service / bilibili 登录夹具 |
| F-004 / P1 | 用户列表只有文字，缺打开/缓存入口，初次列表请求错误被吞；网络操作阻塞窗口 | `internal/ui/app.go`、`internal/ui/views.go`、`internal/ui/requests.go`：补列表操作、30 条投稿分页、保留初次/后续错误及重试；解析、用户分页、原图和订阅检查通过后台请求并在窗口线程应用结果，窗口关闭取消上下文 | `TestRequestRunsOffWindowAndPublishesOnlyOnDispatcher`、现有 UI 输入法与登录往返测试 |
| F-005 / P1 | 订阅按主账号 UID 选择，“更新”读媒体信息流；不能管理完整多账号与规则；导入只有第 1 页 | `internal/ui/subscriptions.go`、`internal/ui/actions.go`：稳定订阅 ID、名称备注、关联预览/迁移确认、解除关联/头像、删除确认、四标签；真正更新表、可见约 1 秒标已看、全部已看；缓存按全部成员过滤；关注分组/分页/失败保留/已归属禁选 | `TestAuthGuardAndNamedMultiAccountSubscriptionUI`、现有订阅 CRUD/导入测试 |
| F-006 / P1 | 新关联账号沿用订阅水位线；后页请求失败可能被当作成功；普通订阅额外查询投稿 | `internal/subscription/manager.go`：每账号独立基线，普通订阅从动态发现，后页错误上报；文件持久化失败返回错误并恢复上份成员视图；定时轮次保守预留最多 3 页的请求额度 | `TestNewlyLinkedAccountGetsIndependentBaseline`、`TestBaselineAndIncrementalCheckThreePageAndMarkSeen`、`TestScheduledCheckBackoffQuotaAndAutoCache` |
| F-007 / P1 | 动态关联视频只处理第 1 P；同视频多 P 并发合并清单可能覆盖；转发原内容文件不完整；206 缺范围也接受 | `internal/download/manager.go`：关联视频全部分 P，按视频串行更新清单，原动态图片/视频与快照登记；严格 Content-Range 与长度校验；传输中发布逐文件速度/字节进度；音质升级按质量顺序比较 | `TestRangeRejectsUnverifiablePartialContent`、现有多 P/动态缓存/Range 夹具 |
| F-008 / P1 | 补存失败仍清空缺失项并标绿；设置的画质、多 P、AI 字幕未到达下载器，部分 UI 选项不被设置校验接受 | `internal/download/manager.go`、`internal/settings/settings.go`、`internal/ui/views.go`：补存保留未恢复项与失败原因；设置传入下载器，选择值与校验一致，保存错误展示；字幕来源及 5 MiB 上限检查 | `TestSupplementFailurePreservesMissingState`、settings / download / bilibili 现有夹具 |
| F-009 / P1 | 库根登记写到缓存旁；删除清单可能接收绝对路径或链接祖先；任意用户 .part 都被当作生成物 | `internal/service/backend.go`、`internal/library/library.go`：库根账本在数据目录；读取、删除和启动待清理均限制路径与链接；未登记 .part 保留；服务状态圆点复用包含引用状态的后端推算 | `TestDeletePlanPreservesUnregisteredPartAndRejectsAbsolutePaths`、现有 MediaPath/引用删除测试 |
| F-010 / P1 | 直播自动录制依赖另一个默认关闭的轮询开关，缺共享额度/退避，任务页不显示录制进度；Linux 包无条件引用 Windows API | `internal/live/manager.go`、`internal/live/disk_windows.go`、`internal/live/disk_other.go`、`internal/ui/views.go`：规则开启后检查、共享退避和保守预留、并发启动串行检查、录制字节/时长/速度事件与任务入口；磁盘查询按平台分离 | 现有 live 流状态/重连/收尾夹具；Linux 交叉编译（不等于运行通过） |
| F-011 / P2 | 主页混入仅浏览的在线记录且只装载固定条数，阅读位置未接通；窄窗设置分类按钮被裁掉 | `internal/ui/app.go`、`internal/ui/views.go`、`internal/library/library.go`：主页只查本机缓存，双向锚点附近加载、继续分页、稳定条目键与阅读位置；更新数只计缓存；窄窗分类/内容两级导航 | `TestBidirectionalKeysetFeedAndAnchor`、UI 三宽度测试与截图 |
| F-012 / P1 | 旧 UI 测试含站点公开图片地址，新增真实加载后可能逸出本机夹具；非 Windows 生产加载还会返回合成播放器 | `internal/site/bilibili/client.go`、`internal/ui/ui_test.go`、`internal/player/mf_other.go`：夹具客户端传输层只允许 loopback；图片样本与回归测试只连本机；非 Windows 生产媒体加载明确报解码器不可用，合成解码只由测试显式注入 | 新图片回归测试、Linux 交叉编译；真实 Windows MF 测试只在 Windows 执行 |
| F-013 / P1 | 初版 `mygo/ui` 未配置主题且直接堆砌默认带框按钮，缺失无框胶囊左栏、`PostCard`（头像/16:9 封面/九宫格/展开收起/状态圆点联动）、蓝色下划线标签、任务逐文件展开详情、订阅规则开关文字标签、编码优先级排序等，视觉与交互偏离 `docs/ui.md` | `internal/ui/theme.go`、`internal/ui/components.go`、`internal/ui/app.go`、`internal/ui/views.go`、`internal/ui/subscriptions.go`、`internal/ui/overlays.go`、`internal/ui/actions.go`：注入 `TraceTheme()`（粉色主色与 `InactiveAccent`）、三栏居中与无框胶囊左栏（底栏头像+独立设置入口）、`PostCard`（圆形头像、16:9 封面、九宫格、超 6 行展开收起、四态圆点与动作胶囊）、`UnderlineTabs`、用户空间 3:1 横幅与压边头像、任务页直播录制置顶与逐文件进度展开、订阅规则完整中文标题与说明、设置页编码顺序 `↑`/`↓` 调整及粘贴 Cookie 不续期说明、看图层滚轮缩放与拖拽平移 | `go test ./internal/ui/...`、`tools/uishots`（39 张三档宽度截图目视复核） |
| F-014 / P1 | 普通凭证更新先改内存再写盘，写失败后状态与密文不一致；失效状态写失败又可能继续使用旧 Cookie | `internal/credential/credential.go`：先复制候选、加密原子写成功后替换内存；普通更新与退出失败保留原凭证。已确认失效时，即使状态落盘失败，也在内存阻止使用并返回存储错误，保留旧密文；补取得/续期时间及旧 camelCase 数据读取兼容 | `TestAtomicReplaceCredential`、`TestExpiredCredentialIsBlockedWhenStatusCannotPersist` |
| F-015 / P1 | 扫码或续期可能在账号未经校验时保存候选；旧流程与新登录并发；二维码仍使用自写编码器 | `internal/site/bilibili/auth_manual.go`、`auth_qr.go`、`auth_refresh.go`、`qrcode.go`：登录、续期、退出串行，候选先 nav 验证同一账号再保存；扫码 revision、取消与单流程轮询隔离；续期先保存待确认记录再 Step 5，状态写失败不吞。接入 A 已选的二维码库，只取矩阵、内存渲染 | `TestRenewalRejectsUnverifiedAccount`、`TestQRConfirmationRequiresValidCandidate`、现有扫码取消与续期五步夹具 |
| F-016 / P1 | 自动请求按轮次估算额度，未覆盖嵌套 API；等待期间取消仍可能扣额度；选流、字幕与响应检查存在不完整边界 | `internal/site/bilibili/automation.go`、`client.go`、`playurl.go`、`subtitle.go`、`live.go`：可取消串行 pacing，发送前逐 API 扣共享额度；嵌套签名/空间/取流请求也受额度与退避约束；限制正文大小、保留脱敏错误分类；视频/音频按档位排序，拒绝多段 durl 冒充备用，试看不缓存；字幕先校验 bvid/cid、AI 开关及分语言错误。F-006/F-010 的保守预检保留，但不再代替实际逐请求记账 | `TestClientGlobalPacing500ms`、现有选流/字幕/直播与 `TestScheduledCheckBackoffQuotaAndAutoCache` 夹具 |
| F-017 / P1 | 合并会整体读取媒体；最高音轨的 AAC 后备失败可能生成静音主文件；升级失败、动态提前中断可能丢已完成文件或旧质量 | `internal/download/video.go`、`mux_disk.go`、`manager.go`：磁盘样本表、约 1 秒交错、保留编辑列表、支持取消；在 `.part` 内校验后替换。FLAC/杜比最高轨另存，主 MP4 用最佳 AAC，后备缺失/失败不生成静音完成视频；升级失败保留旧正式文件与档位；动态中断仍保存图片/hash/全部关联 P 及各 P 缺失状态 | `TestHighAudioWithoutPlayableFallbackNeverCreatesSilentMainVideo`、`TestUpgradeValidationFailureRetainsOriginalMP4`、`TestDynamicRiskStopRetainsCompletedImageAndHash`、现有 mux 小样本 |
| F-018 / P1 | 临时文件认领只看文件名；迟到 worker 能改变暂停/取消状态，并发名额过早释放；任务写库错误被吞；不可核对的 200 或过长 206 被接受 | `internal/download/manager.go`：写文件前持久化完整目录/相对路径的 Owned 登记；只复用可核对的正式文件或已认领断点，取消只删本任务临时文件；worker 退出才释放名额，迟到进度不能逆转状态；存储失败显示 failed/storage，恢复后可重试。200 必须有长度、206 完整核对范围/总长/正文，图片 MIME/50 MiB 上限；重试 ±20% 抖动，进行中事件每秒至多一次；列表按进行中、需处理、终态分组，再按更新时间排序 | `TestCancelOnlyRemovesTaskOwnedParts`、`TestStoppedTasksRejectLateWorkerProgress`、`TestStorageFaultCanResumeAfterDatabaseRecovers`、`TestOwnershipUsesDirectoryAndSupportsValidatedMP4Staging`、`TestMediaLengthAndImageFailureBoundaries`、`TestTaskListKeepsActiveAndRecentlyChangedTasksFirst` |
| F-019 / P1 | 保存位置更新/迁移失败可能先删除旧文件；损坏删除账本被当作空；重建扫描失败后仍清旧行；接入根写失败后已导入；画质探测吞保存错误；已暂停但未退出的 worker 仍可能写入迁移源 | `internal/library/cache_root.go`、`migration.go`、`library.go`、`schema.go`、`internal/settings/settings.go`、`internal/download/manager.go`：先验证并持久化根，再切设置；迁移只复制登记文件，设置落盘成功前不删原文件，失败回滚副本与根；迁移期间守住队列/worker 锁，已暂停但未退出也拒绝。保存/引用/信息流索引、删除 SQL 使用事务；画质探测读/写库失败返回 storage。损坏删除账本原样保留并停止相关变更；扫描/SQL/上限错误返回且不清 stale；根登记失败不导入；保护无法认领的同名清单 | `TestRebuildFailurePreservesRowsAwaitingStaleCleanup`、`TestUnreadableDeletionLedgerStopsMutation`、`TestRootRegistrationFailureDoesNotImportRows`、`TestMigrationPersistsSettingsBeforeRemovingOriginals`、`TestSaveItemPreservesUnclaimedManifest`、`TestUpgradeProbeReturnsStorageFailure`、`TestMigrationWaitsForPausedWorkerToExit` |
| F-020 / P1 | 直播按本地时间建新条目，同场停止后重复自动开始；分片推进未与落盘绑定；恢复扫描任意 `.part`，崩溃尾部无法确认完整边界 | `internal/live/recorder.go`、`manager.go`、`internal/library/checkpoint.go`：官方 HLS fMP4 选流、真实 live_time 场次与同场段号续接；手停抑制同一场自动开始；完整分片 fsync 后保存字节/时长/序号 checkpoint，跳空、序号倒退、MAP 地址/内容变化换段。只恢复清单登记的录制 part（含导入根），截断 checkpoint 后尾部；保护用户同名文件。收尾按磁盘转换，时长误差 ≤ 0.1 秒、限时 30 秒，清单成功保存后才删 part，存储失败不标完成 | `TestRecorderPersistsCompleteFragmentBoundary`、`TestInvalidLiveInitNeverRequestsMediaFragment`、`TestRecoveryUsesRegisteredPartAndTruncatesAfterCheckpoint`、`TestRecordingPreservesUserOwnedTargetName`、现有直播换段/收尾夹具 |
| F-021 / P1 | 订阅文件回滚会删表重建，移动中途 SQL 失败留下半份成员关系；读取失败后旧视图被清空；UID 前导零绕过唯一归属；更新先全库分页再过滤漏条目 | `internal/subscription/mutations.go`、`manager.go`：只恢复受影响的记录，并用事务保护回滚/重载；SQL/file 错误返回；规范化 UID、同目标移动幂等，解除/移动修复头像；成员过滤先于 LIMIT/OFFSET，MarkSeen 写失败返回；保留每个新成员独立基线 | `TestSubscriptionFileFailureRestoresOnlyChangedRows`、`TestMoveSQLFailurePreservesBothSubscriptions`、`TestSubscriptionReloadFailureDoesNotEmptyExistingView`、`TestCanonicalUIDCannotBelongToTwoSubscriptions`、`TestSubscriptionUpdatePaginationFiltersBeforeLimit`、保留 `TestNewlyLinkedAccountGetsIndependentBaseline` |
| F-022 / P1 | UI 接口暴露站点客户端与库对象，可绕过服务鉴权；用户链接初次读取投稿而非默认动态；启动/历史写入失败被吞 | `internal/service/backend.go`、`browsing.go`：`BackendAPI` 只暴露业务方法，除 Auth/Settings/App 外统一检查登录；账号替换前暂停下载、取消检查与录制；用户解析先取资料、默认动态，投稿按标签读取；启动与最近解析/信息流写失败传播为实际错误，状态由后端按文件及引用推算 | `TestServiceAuthIsolationOpenLinkAndRedaction`、`TestServiceBackendContractsAndLogRedaction` |
| F-023 / P2 | 媒体库复用帖子列表，未逐图片/P/录像段呈现；订阅“已缓存”固定首批；任务进度会重载主页；实际行内偏移没有保存/恢复 | `internal/ui/library_view.go`、`feed.go`、`updates.go`、`views.go`：媒体库独立 3 列/窄窗 2 列方形网格，逐文件打开、详情/删除/筛选/排序，60 条分页；订阅已缓存按全部成员、缓存时间分页且每次重新进入从最新开始；主页双向 keyset、稳定键及上一提交帧的行/视口 bounds 保存行内偏移，入库只改新内容数，进度不移动主页 | `TestFeedProgressEventsAndRestartPreserveViewportAnchor`、`TestSubscriptionCachedTabLoadsBeyondFirstPage`、`TestThumbnailCoverAndPlayOverlayShareFullArea`、39 张截图 |
| F-024 / P1 | 登录两标签同时显示、退出没有确认；预览/旧页面请求迟到后污染当前页面；订阅列表未虚拟化；画质检查缺进度和取消，普通失败又可能误显取消 | `internal/ui/login.go`、`network_actions.go`、`requests.go`、`images.go`、`subscriptions.go`、`overlays.go`：只渲染当前登录方式，切手动取消扫码；退出确认后执行；网络请求在后台，成功/失败都核对当前视图与输入；公开图片无 Cookie、并发/尺寸/像素/缓存上限并支持 WebP，失败缩略图不逐帧重试；订阅先筛选再虚拟化，检查进度只更新计数，两个检查入口均可取消，画质只统计实际可升级项 | `TestLoginModesAndLogoutConfirmation`、`TestLateRequestErrorDoesNotChangeAnotherUserPage`、`TestUnavailableThumbnailIsNotRetriedEveryFrame`、`TestSubscriptionListVirtualizesAndSearchResetsScroll`、`TestQualityCheckDistinguishesFailureFromCancellationAndCountsUpgrades`、`TestCancelQualityCheckDoesNotRequestRemainingCandidates` |
| F-025 / P1 | 生成的许可通知对 `reedsolomon v1.0.0` 只写“No separate license file found”，依赖许可未完整随包提供 | `tools/package/main.go`、`licenses/`：核对官方固定提交只为同一源码新增 MIT LICENSE，逐字保存并仅对该精确版本嵌入通知；其他模块缺许可则停止打包。来源、版本比较、原文 SHA 与移除条件见[许可补充](../../tools/package/licenses/README.md) | 最新 `tools/package`、通知中的完整 MIT 原文及 `SHA256SUMS.txt`；未安装/发布 |

## 2. 工作包与证据入口

沿用计划编号；未变输入复用已有测试，本次变更的完整调用链与失败边界按下表交接：

| 工作包 | 实现与检查入口 | 本轮证据口径 |
| --- | --- | --- |
| P1～P2 | `internal/appdirs`、`storage`、`settings`、`uistate`、`credential` | Windows 门禁；DPAPI/原子更新失败、失效阻断、位置更新回滚及旧字段读取 |
| P3～P4 | `internal/site/bilibili` | 合成 HTTP 夹具、签名固定向量、候选验证/并发隔离/续期、逐请求额度、选流、弹幕字幕；真实站点兼容性 not-run |
| P5 | `internal/library` | 路径白名单、登记删除、重建失败保留索引、迁移设置失败保留原文件、成员过滤/排序/分页 |
| P6 | `internal/download` | Range/图片边界、写前认领、队列恢复与迟到进度、任务存储失败重试、多 P/动态中断、磁盘合并、最高音轨/AAC 后备、升级保留原文件 |
| P7 | `internal/subscription` | CRUD/file/SQL 回滚、移动/UID 唯一归属、独立基线、过滤后分页、退避/额度与自动缓存 |
| P8 | `internal/live` | 本机 HLS fMP4 夹具，场次、同场续接、换段、fsync checkpoint、登记恢复/用户文件保护、磁盘转换；不使用真实流 |
| P9 | `internal/service` | 业务接口鉴权、默认动态与延迟投稿、状态/启动/持久化错误、日志脱敏 |
| P10 | `internal/player` | Windows MF 解码与静音时钟/字幕弹幕测试；真实系统音频输出由 H-001 人工检查 |
| P11～P12 | `internal/ui` | UI tester、三宽度、输入法、登录标签/退出确认、多账号、逐文件媒体库、125 条已缓存分页、1000 订阅虚拟化、主页非零行内偏移重启恢复、后台结果与迟到错误、进度及取消 |
| P13 | `internal/shell` | 隔离进程隐藏真实窗口，五次销毁/重建；未登录用允许访问的设置页检查路由恢复，保留精确路由断言 |
| P14 | `tools/uishots` | `artifacts/uishots/assertions.json` 与 51 张 PNG；外框及必需控件可见边界断言不代替全部交互和人工视觉验收 |
| P15 | `tools/check`、`checkdocs`、`package`、`smoke` | Windows 门禁、开发包与安静冒烟；未安装或卸载用户程序 |
| P16 | `internal/sitecheck`、本报告、USER-ACTIONS | 额度门控夹具；ACT-004/005 旧账本与回执保留，ACT-006 未执行 |

原型 PR-1～PR-4 的小样本在对应包默认测试中复验，测试重写 `artifacts/prototypes/pr1.json`～`pr4.json`；`RESULTS.md` 是 shell 测试生成的概要，不能代替各包断言与实际门控状态。磁盘 mux 与直播收尾的最新证据为默认 download/live/player 测试。大于 2 GiB、稀疏 co64 和安装/卸载门控没有开启，不能从回执字段或代码存在推导它们已运行；真实 FLAC-in-MP4 解码也未取得新的证据，当前保存策略见[媒体选型 §1.1](../research/2026-11-05-media.md#hires-audio)。

## 3. 验证与产物

以下保留修订 3 的历史证据（被测源码 `681b6d4` 加 B 当轮修改，即交付 `648932e`）；当前修订 4 的命令和回执以[§13.2](#rev4-b-validation)为准，`artifacts/` 已更新为当前跑次，不能用最新文件反向证明旧跑次。所有日常短测均在约 10 分钟预算内，测试服务器只监听 loopback、音频注入静音输出、真实窗口隐藏。原跑次的审查与实机反馈保留在 §5、§7、§8。

- `go run ./tools/check`：**passed / 0**，最终源码门禁 **23.072s**，16 个含测试包全部通过（未变输入复用 Go 测试缓存；新增及改动测试已运行）。包括格式、vet、全部 Go 测试、INV-05 依赖方向、文档公开内容和 Git 空白检查；输出见 `artifacts/review/check.log`。
- `GOOS=linux CGO_ENABLED=0 go vet ./...`：**passed / 0**，**1.978s**，只证明 Linux 目标的静态检查通过，没有运行 Linux 测试；输出见 `artifacts/review/linux-vet.log`。此前的测试包交叉编译只对应原输入，真实 Ubuntu 运行仍为 not-run。
- `go run ./tools/uishots -out artifacts/uishots`：**passed / 0**，**6.951s**，**51 张 PNG**（17 个场景 × 1280/1000/720 三档宽度，新增 `player-options`、`player-volume`）。`assertions.json` 的布局、固定侧栏、输入法及必需控件可见边界断言通过；本轮用 `view_image` 目视核对的范围见 [§9.3](#b-recheck-uishots)。自动断言与目视核对不代替实机交互验收。
- `go run ./tools/package`：**passed / 0**，**9.717s**，输出 `dist/trace-dev/`；`trace.exe`、安装包、完整许可、版本与 `SHA256SUMS.txt`。未使用 `-install`；输出见 `artifacts/review/package.log`。
- `go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke`：**passed / 0**，**2,877ms**，真实隐藏窗口、无抢焦点、0 个 WebView2 子进程、单实例/数据锁与路径隔离；关到托盘约 2 秒后专用工作集 **643,072 字节（0.61 MiB）**（`PROCESS_MEMORY_COUNTERS_EX2.PrivateWorkingSetSize`，低于 60 MiB），已提交私有内存 **81,211,392 字节（77.45 MiB）**（仅记录）。这些数值不代表长时间驻留；结果见 `artifacts/smoke/smoke.json`。
- 文档收尾后 `go run ./tools/checkdocs` 与 `git diff --check HEAD`：**passed / 0**；源码未再修改，未重复完整测试。提交后以 `go run ./tools/package -refresh-version` 更新版本与清单，最终逐文件核对 SHA-256 及 `VERSION.txt` 的提交字段。

<a id="a-rev3-evidence"></a>
**A 复核修订 3 后的最新证据（2026-11-08，源码为 `648932e` 加 F-042 修复）**：`go run ./tools/check` **passed / 0**（19.488s，`artifacts/review/check.log`）；`GOOS=linux CGO_ENABLED=0 go vet ./...` **passed / 0**（1.054s，`artifacts/review/linux-vet.log`，只证明静态检查）；`go run ./tools/uishots -out artifacts/uishots` **passed / 0**（6.548s，51 张、51 条回执全部通过）；`go run ./tools/package` **passed / 0**（8.855s，`artifacts/review/package.log`）；`go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke` **passed / 0**（2,885ms，`hidden`，托盘专用工作集 618,496 字节 / 0.59 MiB，已提交私有内存 81,358,848 字节，0 个 WebView2）。上面 B 轮的数值只证明 `648932e` 的输入。

最新开发包与 [H-001](../../USER-ACTIONS.md#h-001) 同步，以 `dist/trace-dev/SHA256SUMS.txt` 为准（含 F-001～F-061、R1～R13 与 DEC-038～DEC-040，第五轮反馈修复后重新打包）：

| 产物 | 大小 | SHA-256 |
| --- | --- | --- |
| `trace.exe` | 22,353,920 字节（21.3 MiB） | `1265c2e5b8e51cc5a4c544b92916805e2d0fbb9bac04221803292a241b0f4da3` |
| `trace-dev-setup-0.1.0-amd64.exe` | 7,254,477 字节（6.9 MiB） | `799aad877eac64b5646af370696dd1e794cace1cd8648a7b3847dbe3b33a1bc9` |
| `THIRD-PARTY-NOTICES.txt` | 完整许可，含固定来源的传递模块 MIT 原文与 Inter 字体 OFL 原文 | `90fe04df10b65adbcdd04ca90e5d3527587eccb0493eada296d2babfb8e5fa0e` |

包未安装，冒烟在隔离环境运行。版本文件在最终本地 amend 后由 `tools/package -refresh-version` 对齐提交，避免为报告中的 SHA 再造提交；其最终 hash 只记在包清单里。

此前 M1 实施与修订 3 验证中发生的失败与修正：凭证/服务错误传播改动初次门禁缺少 `errors` 导入，补齐后通过；交互批次把检查文案移出 `actions.go` 后遗留未用 `fmt`，删掉无用导入后 UI/service/download 定向验证通过；迁移守卫回归首次按错误的 Pause 参数/返回数调用，改为实际单参数、两返回值后通过。主页锚点回归最初暴露读取新一帧节点 bounds 无法取得行内偏移，改读前一提交帧 handle 后验证非零偏移重启恢复；测试滚动方向按 MyGo Tester 的正数向下行为修正。直播合成样本改为每秒 30 帧，与 `EXTINF=1` 匹配，并把收尾误差断言收紧到 0.1 秒。旧删除计划与新成员独立基线回归保留，未靠删测试或弱化断言通过。本轮失败与恢复见 §9.2。

**not-run**：`TRACE_RUN_LARGE=1`、`TRACE_RUN_INSTALLER_TEST=1`、`go test -race`（本机无 cgo 工具链）、真实 Ubuntu 运行、[H-001](../../USER-ACTIONS.md#h-001)（含音频设备与原生全屏体验）。本轮未复跑真实站点；ACT-006/007 的实际历史结果见 §7。未执行项不能写为 passed，HLS fMP4 以外的流仍属 LOOP-004。

**计划外请求记录**：新增原图加载后，首次执行旧 UI 样本时存在两个公开图片 URL，导致两次公开图片请求尝试；无 Cookie、无测试账号数据、未保存真实响应，是否到达站点未核实。随后将样本隔离，并在夹具客户端传输层拒绝所有非 loopback 请求，后续验证未再使用站点地址。该尝试不是 ACT-006 的授权检查，也不能作真实兼容性证据。

## 4. 整理与限制

- 保留用户原有 `.env.local`、`.local/`、带日期的旧包及旧真实回执，没有重新使用真实 Cookie。
- `artifacts/uishots/`、`artifacts/smoke/` 和 `dist/trace-dev/` 由已有工具替换为最新一份；没有新建轮次归档文件。
- 原报告中旧包 SHA 和泛化的完成声明被本报告替代，历史可从 Git 查看。
- 本机参考索引已读取；只参考既有能力领域，未复制参考代码或修改索引。接入 A 已选二维码依赖及其 Reed-Solomon 传递依赖，既有 `x/image` 改为直接依赖以注册 WebP 解码；实际版本与许可见[工程规则](../dev-rules/engineering.md#environment)。
- 将下载的封面/视频/磁盘合并、直播录制/恢复/封面、媒体库根/迁移/统计/checkpoint、订阅变更/额度、UI 信息流/媒体库/图片/登录/异步操作分到对应包的职责文件。删除被这些路径替代且已无调用的旧内存样本提取/整文件合并、直播录制与重复 segment helper；保留仍供小样本测试调用的 bytes helper，不增加兼容包装。
- UI 截图、静态检查、小样本媒体和本地门禁只支持工程自检。A 仍需对照计划与需求审查完整调用链、关键错误恢复和界面操作；产品验收没有开始。

<a id="a-review"></a>
## 5. A 集中复核（2026-11-08）

- **角色与基点**：Agent A 复核 B 交付的 `4c055a5ee6013602fc675e8c093391c0fc963965`（父提交 `20010f9`），开始时工作区干净。A 的修复与本报告 amend 进同一个未推送的 DCO 阶段提交，最终 SHA 见交付消息与 `dist/trace-dev/VERSION.txt`。未 push、未创建 PR、未请求真实站点。
- **审查方式**：先跑门禁与 `go test -count=1 ./...`（不用缓存，16 个包全部通过）。再按 REVIEW 的顺序重点阅读：凭证（`credential`、扫码确认与账号替换）、服务层业务接口鉴权（逐个核对除 Auth/Settings/App 外的方法都先调用 `requireLogin`）、媒体库路径白名单/删除/迁移/重建、下载取消与文件认领、外壳内存测量与冒烟断言，并把 C01～C40 逐条对到测试函数（§6）。UI（约 1.1 万行）、直播录制器、订阅管理器和站点协议的其余部分以 B 的定向回归和 `-count=1` 复跑为依据，A 只做了抽查，没有逐行审阅。
- **F-001～F-025**：B 列出的复验测试都存在，并在无缓存复跑中通过。A 读代码核实了 F-014（先写盘再替换内存；已确认失效时在内存中阻断）、F-015（候选凭证先经 nav 校验，并再次确认扫码流程未取消、未过期后才 `SaveQR`）、F-018（运行中取消时，等 worker 退出后才清理，且只清本任务认领的临时文件）、F-019（迁移先复制，设置落盘后再删原件，重建时新根优先）、F-022（业务方法鉴权）、F-025（许可通知含 `reedsolomon v1.0.0` 的 MIT 原文，不再有“No separate license file found”），这几项 **A 已核实关闭**。其余 F 项按 B 的回归复跑通过，并抽查了实现，结论为**测试复验通过、A 抽查未见问题**；没有逐行审阅的部分不作更强的结论。

A 新发现并直接修复的问题：

| 编号 / 严重度 | 触发与实际结果 | 依据 | 修复与复验 | 状态 |
| --- | --- | --- | --- | --- |
| F-026 / P1 | Go 1.23 起，Windows 目录联接（junction）的 `Lstat` 结果是 `ModeIrregular`，不是 `ModeSymlink`。`safeRegisteredPath` 只检查 `ModeSymlink`：条目目录里的 `images` 若是指向外部的联接，`ResolveItemPath` 会放行，`DeleteItem` 会删掉**缓存外**的同名文件；下载写入和迁移复制也走同一个检查。原测试名里有 SymlinkRejection，但没有创建任何链接 | INV-08、INV-09；计划 P5“用 `Lstat` + `EvalSymlinks` 拒绝符号链接和目录联接逃逸” | `internal/library/library.go` 新增 `isLinkLike`（`ModeSymlink` 或 `ModeIrregular`），用于路径的每一级、根目录和 `MediaPath`。新增 `TestJunctionInsideItemIsNeitherServedNorDeleted`（隐藏窗口调用 `mklink /J` 建合成联接）：撤掉修复时 `ResolveItemPath` 放行；临时放开这条断言后，外部文件确实被删除。修复后通过 | A 已修复并复验 |
| F-027 / P1（证据失真） | `CurrentProcessMemoryBytes` 把 `WorkingSetSize` 同时当作 `privateBytes` 返回，而且是在强制 `EmptyWorkingSet` 之后立即读取。PR-4 记录的 0.58 MiB 和冒烟记录的“专用工作集 6.98 MiB”都不是 NFR-RES-001 规定的专用工作集 | NFR-RES-001、计划 PR-4/C35、REVIEW“证据失真” | `internal/shell/priority_windows.go` 改读 `PROCESS_MEMORY_COUNTERS_EX2` 的 `PrivateWorkingSetSize`，另记 `PrivateUsage`（已提交私有内存）。外壳测试和冒烟改为关到托盘后静置约 2 秒再测量，不再额外强制回收，门槛按专用工作集判定。复验：PR-4 专用工作集 0.58 MiB、已提交 83 MiB；冒烟 0.61 MiB、78.48 MiB。专用工作集低主要来自关到托盘时对工作集的修剪；长时间驻留后的数值由 H-001 第 23 步在任务管理器里观察 | A 已修复并复验 |
| F-028 / P2 | 计划 §7 要求报告逐条引用 C01～C40 的测试函数，B 的报告只有工作包表。C17 的慢速夹具和 C32 的账号间隔没有任何断言（测试都把间隔设为 0）；C37 只扫描了 4 个指定文件。测试注释里的 C 编号沿用旧编号（例如 OPML 测试标 C33、WBI 测试标 C09），与计划不一致；状态页 LOOP-001 引用 C31～C35，应为 C08～C10 | 计划 §7、REVIEW 第 5 条 | 新增 §6 对照表，以表为准。新增 `TestSlowMediaUsesIdleTimeoutNotTotalTimeout`（按比例缩放：200ms 空闲超时下，64 KiB 慢速分段耗时远超空闲时长仍一次完成；连接挂起时由空闲超时断开并从断点续传）和 `TestCheckRoundSpacesAccountsByInterval`（3 个账号间隔不小于设定值，首个不延迟）。C37 改为遍历数据、日志、缓存目录下的全部文件（本次 15 个，含数据库和加密凭证）。修正状态页引用。测试注释里的旧编号没有逐个改写，留作建议 | A 已修复并复验 |

**验证（A 修复后）**：`go run ./tools/check` **passed / 0**（最终 22.7s）；修复前、修复后 `go test -count=1 ./...` 均 **passed / 0**；`GOOS=linux CGO_ENABLED=0 go vet ./...` **passed / 0**（只证明能编译，Linux 测试仍为 not-run）；新增和改动的测试定向通过；`tools/package` 与 `tools/smoke` 见 §3。UI 源码没有改动，`artifacts/uishots/` 沿用 B 轮 `2026-11-07T17:28:21Z` 生成的 39 张截图与断言。

**结论**：

- **实现交付**：M1 计划范围已实现，F-001～F-028 在本地证据范围内关闭。
- **技术审查（A）**：通过，范围与抽查边界见上。审查中发现的都是局部问题，已由 A 直接修复，不需要交回 B。
- **产品验收**：未开始。[ACT-006](../../USER-ACTIONS.md#act-006)（真实站点小额复核）、[H-001](../../USER-ACTIONS.md#h-001)（实机体验）、`TRACE_RUN_LARGE=1`、`TRACE_RUN_INSTALLER_TEST=1`、`go test -race` 和真实 Ubuntu 运行仍为 **not-run**。

<a id="case-map"></a>
## 6. 必测用例与证据对照（C01～C40）

“部分”表示计划要求中有子项没有被自动断言覆盖，写在备注里。测试注释中的 C 编号以本表为准。

| 用例 | 测试函数 / 工具断言 | 备注 |
| --- | --- | --- |
| C01 | `TestResolveLinkSupportedTargets`、`TestResolveLinkRejectsUnsupportedAndMaliciousRedirects`、`TestResolveLinkSupportedAndRejected` | |
| C02 | `TestResolveLiveRoomShortID` | |
| C03 | `TestHistoryStoreCapDedupAndSanitize`、`TestAtomicWriteAndHistoryCap`、`TestServiceAuthIsolationOpenLinkAndRedaction`（R11：本机缩略图、无凭证/签名、删除联动）、`TestRevision4FeedbackReworkR8ToR13`（最近解析显示与菜单）、`TestHistoryCleanupKeepsSharedThumbnails`、`TestHistoryClearPreservesUnregisteredFiles`、`TestHistorySharedThumbnailRespectsWindowsFilenameCase`、`TestHistoryThumbnailCommitFailureDiscardsOnlyNewFiles`、`TestHistoryThumbnailCleanupFailureIsRecoverableAfterRestart`、`TestHistoryThumbnailsAreBoundedDecodedAndReencoded`、`TestImageByteLimitRejectsBodiesBeforeOrDuringRead` | 补充失败/共享引用证据见 [§13.1](#rev4-b-recheck) F-044～F-045；全部缩略图测试入口列在该节 |
| C04 | `TestQRLifecycleAndCanceledFlowIsolation`、`TestQRLoginFlowAndCancelIsolation`、`TestQRConfirmationRequiresValidCandidate` | |
| C05 | `TestManualCookieParserWhitelistAndBounds`、`TestManualCookieParseWhitelistAndLimit`、`TestServiceAuthIsolationOpenLinkAndRedaction`、`TestAuthGuardLoginCookieClearOverlaysAndDesktopActions` | |
| C06 | `TestPreRequestAuthGuardZeroRequests`、`TestServiceAuthIsolationOpenLinkAndRedaction`、`TestAuthGuardAndNamedMultiAccountSubscriptionUI` | |
| C07 | `TestAuthGuardLoginCookieClearOverlaysAndDesktopActions`（运行中 -101 → 登录页，重新登录回到原路由）、`TestAuthGuardAndNamedMultiAccountSubscriptionUI` | |
| C08 | `TestCookieRefreshFiveStepAndConfirmResume`（manual 凭证 0 次 `cookie/info`） | |
| C09 | `TestCookieRefreshFullLoopAndPendingConfirm`、`TestCorrespondPathRoundTripAndKeySHA256`、`TestRenewalRejectsUnverifiedAccount` | |
| C10 | `TestCookieRefreshFailurePreservesOldCredential`、`TestAtomicReplaceCredential`、`TestExpiredCredentialIsBlockedWhenStatusCannotPersist` | |
| C11 | `TestClientGlobalPacing500ms`、`TestGlobalPacingAndWBIFixedVectors`、`TestWBISigningFixedVectorAndSingle352Refresh` | |
| C12 | `TestVideoDetailUserSpaceAndArcSearch`、`TestUserCardAndVideosParamsAndErrors` | |
| C13 | `TestDynamicsTypesAndGracefulDegradation`、`TestUserDynamicsTypesAndDegradation`、`TestLateRequestErrorDoesNotChangeAnotherUserPage` | |
| C14 | `TestAuthGuardLoginCookieClearOverlaysAndDesktopActions`（沉浸式看图层：滚轮/按键缩放、指针拖拽平移、双击切换、1:1、适应窗口、切换、缓存图片打开文件夹、2 秒静止自动隐藏与键盘唤醒、Esc）、`TestOriginalImageAndMissingMediaNeverUseSyntheticFallback` | |
| C15 | `TestPlayURLSelectionOrderAndDowngrade`、`TestMuxDASHFilesAndVideoCacheEndToEnd`、`TestMultiPartCachePreservesOtherPartFailures`、`TestQueueConcurrencyDedupStartupRecoveryAndCircuitBreaker`、`TestUnsupportedMediaNodeIsSkippedNotCountedAsFailure`（F-030） | |
| C16 | `TestPR2PureGoDASHMuxAndFMP4Defragment`、`TestHighAudioWithoutPlayableFallbackNeverCreatesSilentMainVideo` | 真实 FLAC-in-MP4 解码未验证 |
| C17 | `TestRangeResume206AndFallback200`、`TestRangeRejectsUnverifiablePartialContent`、`TestSlowMediaUsesIdleTimeoutNotTotalTimeout`（A 新增） | 慢速按比例缩放，没有按 200 KB/s × 8 MiB 实跑 |
| C18 | `TestRiskControlCircuitBreakerPausesQueued`、`TestQueueConcurrencyDedupStartupRecoveryAndCircuitBreaker`、`TestPartialFailureAndRetrySkipsCompleted`、`TestFinishedTasksCanBeRemovedButNotCanceled`（F-031）、`TestRevision3FeedbackReworkR1ToR7`（R7） | |
| C19 | `TestStateDotInference`、`TestOwnershipUsesDirectoryAndSupportsValidatedMP4Staging` | |
| C20 | `TestCheckQualityUpgradeDetection`、`TestUpgradeValidationFailureRetainsOriginalMP4`、`TestUpgradeProbeReturnsStorageFailure`、`TestQualityCheckDistinguishesFailureFromCancellationAndCountsUpgrades`、`TestCancelQualityCheckDoesNotRequestRemainingCandidates` | |
| C21 | `TestPlayURLDanmakuAndSubtitles`、`TestDanmakuProtobufAndSubtitlesVTT`、`TestSupplementFailurePreservesMissingState` | |
| C22 | `TestPlayerDanmaku10kPerfSubtitlesAndNullSink`、`TestMFDecoderPureGoAndMissingCodec`、`TestPaddedRowsAreNotSheared`（F-029）、`TestAuthGuardLoginCookieClearOverlaysAndDesktopActions`（沉浸式播放层：全屏切换、“⋯”菜单含弹幕/字幕/在文件夹中显示/用系统播放器打开、2 秒静止自动隐藏与唤醒、缺解码器 → 用系统播放器打开）、`TestPlayerVolumeAndDensityInputsChangePlaybackAndPersist`（音量/静音/密度/字幕，含 F-042 显示区域中文选项）、`TestAdvanceNeverDecodesOnTheUIThread`、`TestStaleFramesAreDroppedAndShownBufferIsNotReused`、`TestDecodePathsAgreeOnPixels`、`TestDecodeSizeFitsWindowAndKeepsAspect`（F-043） | 真实音频输出由 H-001 检查 |
| C23 | `TestDynamicCacheLinkedVideosAndPartialRetry`、`TestShutdownPersistsPausedWork`、`TestRangeResume206AndFallback200`（尝试记录脱敏） | |
| C24 | `TestBidirectionalKeysetFeedAndAnchor`、`TestFeedProgressEventsAndRestartPreserveViewportAnchor`、`TestRevision4FeedbackReworkR8ToR13`（R8～R10）、`TestFeedAroundTimestampChoosesNearestOlderItemAcrossTies`、`TestHomeActorReturnRestoresRowAndPixelOffset`、`TestHomeActorReadFailureKeepsMainTimelineAndSeenState`、`TestHomePersistenceFailuresAreVisibleAndKeepOldState`、`TestHomeLatestFailurePreservesAnchorAndVisiblePosition`、`TestHomePaginationFailureWaitsForExplicitRetry`、`TestHomeStatisticsReadsAreIndependentOfRedraws`、`TestHomeRenderingAndLocalMediaNeverFetchRemoteImages`、`TestUIStateSeenMapsNeverAliasCommittedState` | 同时间戳双向分页、非零行内位置、失败保留、重绘不查询统计及图片调度 0 次，见 [§13.1](#rev4-b-recheck) F-046～F-051/F-054 |
| C25 | `TestLibraryFilterByTypeSubscriptionAndCreator`、`TestSubscriptionCachedTabLoadsBeyondFirstPage`、`TestRevision4FeedbackReworkR8ToR13`（R12）、`TestLibraryTitleOpensMediaAndFileChoiceDoesNotDependOnPartOrder`、`TestCachedDynamicDetailKeepsReferencedVideo`、`TestLiveLibraryDetailOnlyOffersRecordingActions`、`TestCachedVideoDetailRequestsFreshMetadataAndKeepsKnownMissingPages` | 本机三种详情、按实际文件选段及缓存时获取完整元数据，见 [§13.1](#rev4-b-recheck) F-052～F-053 |
| C26 | `TestPlanDeleteAndDeletePreservesUserFiles`、`TestDeletePlanPreservesUnregisteredPartAndRejectsAbsolutePaths`、`TestUnreadableDeletionLedgerStopsMutation`、`TestJunctionInsideItemIsNeitherServedNorDeleted`（A 新增）、`TestRevision4FeedbackReworkR8ToR13`（R12：媒体库“⋯ → 删除”确认与安全删除）、`TestHistoryClearFailureKeepsConfirmationAndReportsError` | 历史缩略图准确引用清理另见 C03/F-044 |
| C27 | `TestAddLibraryRootLegacyWithoutSettingsJSON`、`TestRootRegistrationFailureDoesNotImportRows` | |
| C28 | `TestSettingsCorruptRecoveryAndUnknownFields`、`TestUIStateCorruptRecoveryAndNormalize`、`TestOpenSQLiteCorruptRecovery`、`TestSubscriptionCRUDLinkMoveAndBackupRecovery`、`TestLibraryIndexRebuildMissingFilesMigrateAndDelete`、`TestRebuildFailurePreservesRowsAwaitingStaleCleanup` | 部分：`updates.db` 损坏重置后“不冒出历史动态”依赖空状态走首次基线（`TestBaselineAndIncrementalCheckThreePageAndMarkSeen`），没有单独的损坏后回归 |
| C29 | `TestSubscriptionCRUDLinkMoveAndBackupRecovery`、`TestCanonicalUIDCannotBelongToTwoSubscriptions`、`TestMoveSQLFailurePreservesBothSubscriptions`、`TestSubscriptionFileFailureRestoresOnlyChangedRows`、`TestRevision3FeedbackReworkR1ToR7`（R2） | |
| C30 | `TestImportFollowingPaginationAndIdempotentCreate`、`TestRelationTagsAndFollowingsPagination`、`TestFollowingGroupsAndListPagination` | |
| C31 | `TestBaselineAndIncrementalCheckThreePageAndMarkSeen`、`TestIncrementalCheckBaselineFailureIsolationAndRiskStop`、`TestNewlyLinkedAccountGetsIndependentBaseline`、`TestSubscriptionUpdatePaginationFiltersBeforeLimit`、`TestRevision3FeedbackReworkR1ToR7`（R1：起点之前的不进入更新、之后的进入，右栏“最近更新”） | |
| C32 | `TestScheduledCheckBackoffQuotaAndAutoCache`、`TestNextRunTimeModes`、`TestCheckRoundSpacesAccountsByInterval`（A 新增） | 间隔按比例缩放为 150ms，生产默认 2s |
| C33 | `TestScheduledCheckBackoffQuotaAndAutoCache`（`since`、每轮 20 个、积压） | |
| C34 | `TestLiveBatch50Encrypted203AndStreamSelection`、`TestLiveTransitionsCarouselFilterAndRecordingRollover`（含 `Quality`/`QualityName`/`Codec` 传递与 `原画 · AVC` 格式化）、`TestLiveStatusBatchCacheAndHLSRecorder`、`TestRecorderPersistsCompleteFragmentBoundary`、`TestRecoveryUsesRegisteredPartAndTruncatesAfterCheckpoint`、`TestInvalidLiveInitNeverRequestsMediaFragment`、`TestRecordingPreservesUserOwnedTargetName`、`TestRecorderFollowsMultivariantPlaylistOnce`（F-032）、`TestNestedMultivariantPlaylistStopsWithReason`（F-032）、`TestRevision3FeedbackReworkR1ToR7`（R4） | 只支持 HLS fMP4（LOOP-004） |
| C35 | `TestQuietModeTrayIconCloseToTrayAndMemory`、`tools/smoke` | 内存口径见 F-027 |
| C36 | `TestResolvePortableAndInstalledPaths`、`TestLockDataDirExclusive`、`TestDocumentsKnownFolderRedirect`、`tools/smoke` | 部分：两个通道的单实例互不影响，只由不同的 `AppName` 和“先 `SetName` 再取锁”的启动顺序保证，没有跨通道的进程测试 |
| C37 | `TestServiceAuthIsolationOpenLinkAndRedaction`（全目录扫描 + `RecentLogs(500)` 脱敏断言）、`credential` 测试、`tools/smoke` 日志哨兵 | |
| C38 | `TestThreeColumnResponsiveFixedSidebarsComposeAndVirtualList`（5000 条、组合态回车）、`TestSubscriptionListVirtualizesAndSearchResetsScroll`（1000 条圆形头像网格虚拟化与搜索重置）、`TestRevision4FeedbackReworkR8ToR13`（R8～R13 界面契约）、`tools/uishots`（63 张三档宽度截图断言，含 `home-sub` 及本机视频/动态/录像详情） | 当前截图证据见 [§13.2](#rev4-b-validation) |
| C39 | `tools/check`、`tools/package`、`tools/smoke`、`TestLockedBundleFileRestoresPreviousPackage`、`TestBundleReplacementKeepsUserFilesAndRemovesObsoleteRegisteredFiles`、`TestBundleInvalidInputAndManifestLeaveOldPackageIntact`、`TestBundleMetadataRefreshPreservesRegisteredBinaries` | 包替换与版本刷新失败恢复，见 [§13.1](#rev4-b-recheck) F-055；安装仍 not-run |
| C40 | `TestQuotaGateBlocksBeforeNetworkRequestAndScansReceipts` | |

<a id="feedback-1"></a>
## 7. 首轮实机反馈与 ACT-006（A，2026-11-08）

用户在复核后试用开发包，授权 ACT-006，并提出 10 条反馈（DEC-033）。A 直接修复缺陷 F-029～F-032（详见[计划 §12.1](stage-plan.md#rev3)），产品调整 R1～R7 交给 B（[计划 §12.2](stage-plan.md#rev3)）。

| 反馈 | 归类 | 状态 |
| --- | --- | --- |
| 1 视频花屏（竖屏） | 缺陷 F-029：解码输出行宽有对齐填充 | A 已修，`TestPaddedRowsAreNotSheared`；用用户缓存的竖屏视频逐帧导出核对 |
| 2 不需要已看标记 | 产品调整 R1 | B 已完成，A 已复核通过（§10） |
| 3 任务不能移除、失败无反馈 | 缺陷 F-031 + 界面补完 R7 | A 已修后端与按钮；B 已补 `LastError` 行内显示、失败移除确认与 `uishots` 任务页场景 |
| 4 偶发下载失败、重试正常 | 缺陷 F-030：首选节点是白名单外的 MCDN 节点 | A 已修，`TestUnsupportedMediaNodeIsSkippedNotCountedAsFailure` |
| 5、8 订阅页杂乱、按钮太多 | 产品调整 R2 | B 已完成，A 已复核通过（§10） |
| 6 图标与含义不符 | 产品调整 R3 | B 已完成，A 已复核通过（§10） |
| 7 录制入口不明显、没有清晰度 | 产品调整 R4 | B 已完成，A 已复核通过（§10） |
| 9 解析页显示后台状态 | 产品调整 R5 | B 已完成，A 已复核通过（§10） |
| 10 看图与播放按钮太多 | 产品调整 R6 | B 已完成，A 已复核通过（§10） |

**ACT-006 执行结果（2026-11-07 20:44～20:45 UTC，1 次运行，未通过）**：检查程序 `tools/actcheck`，回执 `artifacts/site-check/act006.json`（脱敏），账本 `.local/site-check/usage-act006.json`（API `9/20`）。全部 HTTP 状态为 200，没有遇到风控、限流或验证码。

| 步骤 | 结果 |
| --- | --- |
| 粘贴 Cookie 登录 | 通过（`nav` 2 次） |
| 用户动态第 1 页 | 通过：12 条（视频 8、图文 4） |
| 用户投稿第 1 页 | 通过：30 条（总计 615） |
| 缓存最短视频（≤ 180 秒） | 未执行到下载：检查程序先入队再启动下载管理器，启动恢复把任务改成了“暂停”。这是检查程序的顺序错误，产品行为符合设计；已改为先启动再入队 |
| 直播录制 20 秒 | 失败：直播中（`avc`、`qn=10000`），封面已保存，但只请求了 1 次播放列表就在 0.23 秒停止。代码分析确定原因是播放列表结构被判为无效而停止（F-032：最可能是多码率索引）。已修复并补夹具回归；检查程序现在会记录停止原因、错误和播放列表结构（只记标签与计数，不记地址） |

`nav`、动态列表、投稿列表、空间页面、开播状态和 `room_init` 的 ACT-006 额度已经用完，不能在 ACT-006 内复跑。

**ACT-007 执行结果（2026-11-08，1 次运行，通过）**：用户给出真实站点验证的常设授权后，A 用 `go run ./tools/actcheck -act act007` 复跑（含 F-029～F-032 的代码），回执 `artifacts/site-check/act007.json`，账本 API `13/20`，全部 HTTP 200/206。登录、动态、投稿与 ACT-006 一致；缓存最短视频（7 秒竖屏 1080×1920、`avc1.640033`、音质 `30280`）完成，MP4 `9,124,240` 字节，弹幕已存，条目 `complete`、缺失 0；直播录制 20.1 秒（`avc`、原画 `qn=10000`），52 次播放列表与分片请求共 `15,765,109` 字节，按用户停止结束，收尾成 1 段 MP4 `15,729,116` 字节，条目 `complete`。

**F-032 的限制**：ACT-007 记录到的直播播放列表是普通媒体列表（每条分片带站点自定义标签 `#EXT-BILI-AUX`，已加入夹具），不是多码率索引。ACT-006 那次被判无效的播放列表（约 1 KB）没有留下结构记录，确切原因没能复现确认；多码率索引是按代码路径推断的最可能原因，修复本身是对规范内结构的补充。以后再出现时，错误信息会写明具体是哪条规则，检查程序也会记录列表结构。

<a id="rev3-b"></a>
## 8. 修订 3 返工执行结果（B，2026-11-08）

Agent B 保留 A 已修的 F-029～F-032，在 `codex/m1-mygo-bilibili` 分支完成 `TRACE-M1-03` 修订 3 的 R1～R7 全部改造与文档同步：

| 工作包 | 实现位置与主要改动 | 验证入口 |
| --- | --- | --- |
| **R1**（去掉已看标记） | `internal/subscription/manager.go`、`internal/service/backend.go`、`internal/service/browsing.go`、`internal/ui/{components,subscriptions,updates,views}.go`：移除 `MarkSeen`、`MarkAllSeen`、`Unseen` 及界面未读数字、蓝色竖线、“全部标为已看”、左栏订阅未读角标；`updates.db` 的 `seen_at` 列不再读写；首次检查只建立起点、起点之前的动态不写入 `update_item`（INV-12），后续增量新动态才进入更新列表；`Subscription` 增加 `LatestUpdateTs`，主页右栏改为“最近更新”（无新动态时回退显示“最近看过的用户”） | `TestIncrementalCheckBaselineFailureIsolationAndRiskStop`、`TestBaselineAndIncrementalCheckThreePageAndMarkSeen`、`TestNewlyLinkedAccountGetsIndependentBaseline`、`TestSubscriptionUpdatePaginationFiltersBeforeLimit`、`TestRevision3FeedbackReworkR1ToR7` |
| **R2**（订阅页圆形头像网格与详情重构） | `internal/ui/{app,subscriptions,views}.go`：`/subscriptions` 改为 92px 圆形头像网格（下方一行名称自动省略，按宽度自动 3～5 列，1000 个订阅按行虚拟化渲染），正在直播头像带粉色圆环与“直播”小标、录制中带红点与“录制中”小标；顶部只放一个“添加订阅”主按钮与“更多”菜单（检查全部、从关注列表导入）；点击头像进入 `/subscriptions/:id` 详情，详情头部只留头像、名称/备注、“检查更新”主按钮与“管理”菜单（改名、备注、更换头像、删除订阅），下方分“更新”“已缓存”“直播”“账号”“规则”五个标签，每个区域至多一个主按钮 | `TestSubscriptionListVirtualizesAndSearchResetsScroll`（1000 条）、`TestSubscriptionCachedTabLoadsBeyondFirstPage`、`TestRevision3FeedbackReworkR1ToR7`、`uishots` 的 `subscriptions` 与 `subscription-detail` |
| **R3**（图标含义与无靶子形图标） | `internal/ui/theme.go`、`internal/ui/views.go`、`docs/ui.md` §5：将原 `settings` 的双同心圆+十字短线重绘为标准八齿闭合齿轮轮廓 + 中心轴孔；新增 `sliders`（三条水平滑轨+拨杆）用于设置“常规”分类，新增 `logs`（终端框+`>_`）用于设置“日志”分类，改进 `coin` 为圆圈内 `¥`，补充 `close`、`chevron-left/right`、`maximize/fullscreen`、`volume/volume-mute`、`more`；在 `docs/ui.md` §5 列出完整“图标 → 含义”对照表 | `read_file` 人工逐张核对 15 个场景截图（见 §8.2） |
| **R4**（直播录制入口与清晰度） | `internal/site/bilibili/live.go`、`internal/library/schema.go`、`internal/live/{manager,recorder}.go`、`internal/ui/{components,library_view,subscriptions,views}.go`：`LivePlaySelection`、`RoomState`、`LiveRecordingInfo`、`LiveSegmentInfo` 增加 `Quality`、`QualityName`（优先 `g_qn_desc`，回退 `10000=原画、400=蓝光、250=超清、150=高清、80=流畅`）与 `Codec`，由 `FormatLiveQualitySpec` 与 `formatLiveRecordingSpec`（兼容旧版 `Segments[0]` 存量录像元数据）格式化为“原画 · AVC”；用户页直播横幅“开始录制”为粉色主按钮，订阅头像网格直播小标与订阅详情“直播”标签支持一键开始/停止录制并列出“已录制的直播录像”；直播横幅、订阅直播行、任务页录制行与媒体库/主页录像卡片及详情均展示“原画 · AVC” | `TestLiveTransitionsCarouselFilterAndRecordingRollover`、`TestRevision3FeedbackReworkR1ToR7`、`uishots` 的 `subscriptions`、`subscription-detail`、`library`、`tasks`、`user` |
| **R5**（解析页后台状态清理与设置“日志”分类） | `internal/service/backend.go`、`internal/ui/views.go`：`/parse` 不再渲染 `len(a.pending) > 0` 的“正在加载…”等与解析无关的后台状态；`AppService` 新增 `RecentLogs(maxLines)`（最多 500 行，经服务层读取并脱敏 Cookie 与签名参数 `?e=`/`&e=`/`upsig=`）及 `Diagnostics` 后台状态字段（`IndexStatus`、`SubscriptionCheckStatus`、`AutoCacheStatus`、`LiveRecordStatus`）；设置页新增“日志”分类（上方后台状态卡片，下方等宽字体 `monospace` 运行日志框 + “打开日志文件夹”“复制”按钮） | `TestServiceAuthIsolationOpenLinkAndRedaction`、`TestRevision3FeedbackReworkR1ToR7`、`uishots` 的 `parse` 与 `settings-logs`（含 720px 窄窗详情页） |
| **R6**（沉浸式看图与播放） | `internal/ui/{app,overlays,theme}.go`：看图层与播放层改为铺满窗口的深色背景（去除白底卡片框）；只保留半透明圆形悬浮按钮（看图：关闭、上一张/下一张、适应窗口/1:1、本地缓存图片“打开文件夹”；播放：播放/暂停、进度条、音量、全屏切换，弹幕/字幕/分段切换/在文件夹中显示/用系统播放器打开全部收进右上角“⋯”更多菜单）；鼠标或键盘静止满 2 秒自动隐藏悬浮控件，移动或按键（Esc、左右键、空格、`+`/`-`/`0`）立即唤醒 | `TestAuthGuardLoginCookieClearOverlaysAndDesktopActions`（含滚轮缩放、拖拽平移、双击切换、打开文件夹、全屏切换、2 秒自动隐藏与唤醒断言）、`uishots` 的 `image-viewer`、`player`、`player-missing-decoder` |
| **R7**（任务页失败原因、移除确认与截图） | `internal/download/manager.go`、`internal/ui/views.go`、`tools/uishots/main.go`：`Task` 同步 `LastError` 字段并在任务行内直接显示失败原因；点击失败/部分完成任务的“移除”按钮时，行内弹出确认提示“确认移除该任务？会删除未完成的临时文件，已缓存完成的文件会保留”；更新 `uishots` 的 `tasks` 场景同屏覆盖录制中、进行中、失败带原因与移除确认、已完成及顶部“清除已结束（1）” | `TestFinishedTasksCanBeRemovedButNotCanceled`、`TestRevision3FeedbackReworkR1ToR7`、`uishots` 的 `tasks` |

<a id="rev3-uishots"></a>
### 8.2 `tools/uishots` 截图人工核对记录（R3）

B 运行 `go run ./tools/uishots -out artifacts/uishots` 生成 45 张 PNG（15 个场景 × 1280/1000/720），并用 `read_file` 逐张核对了全部 15 个场景图像：

这是 R1～R7 原跑次的记录；当前输出已更新为 §9.3 的 51 张截图。

1. **首轮目视发现并当场修复的 3 处细节**：
   - `settings-logs-1280x860.png` 首轮显示 `[REDACTED_LOG_LINE]`：定位到 `sanitizeLogLine` 的 `"e="` 子串误匹配了 `"mode=dev"` 中的 `e=`，改为匹配签名参数 `"?e="` 与 `"&e="` 后，日志框正常显示等宽脱敏日志（如 `mid=[REDACTED]`）。
   - `subscription-detail-1280x860.png`、`tasks-1280x860.png`、`user-1280x860.png` 首轮中，“直播中 / 录制中”胶囊与“· 原画 · AVC”在长标题挤压下发生缩略：为胶囊与清晰度标签加上 `.Shrink(0)` 后重跑，所有“直播中”“录制中”“· 原画 · AVC”完整无截断。
   - `library-1280x860.png` 首轮中，缩略图下方“详情 / 文件夹 / 删除”三枚胶囊的“删除”右缘略微贴边：为该操作行加上 `.Wrap()` 与紧凑内边距 `.Padding(4, 10)` 后重跑，三枚按钮完整居中显示。
2. **最终 15 个场景核对结论**：
   - 左栏左下角“设置”为清晰的八齿齿轮图标，设置页“常规”为三条水平滑杆图标，“日志”为 `>_` 终端图标，视频详情页“投币”为圆圈内 `¥` 图标，全界面无任何同心圆靶子形图标；
   - `subscriptions` 为 92px 圆形头像网格（带粉色直播环、“直播”小标、红点“录制中”小标，顶部仅“添加订阅”与“更多”）；`subscription-detail` 头部整洁、五标签齐全、“直播”标签显示 `房间号 9001 · 原画 · AVC` 与粉色“开始录制”主按钮；
   - `home` 右栏展示“最近更新”；`parse` 无任何后台加载横幅；`tasks` 同屏展示直播录制（`原画 · AVC`）、“清除已结束 (1)”、进行中任务、带 `LastError` 原因与“会删除未完成的临时文件”移除确认框的失败任务、已完成任务；`image-viewer` 与 `player` 均为全屏深色沉浸式画面加半透明圆形悬浮按钮。

<a id="b-recheck"></a>
## 9. B 复核与补充修复（2026-11-08）

本轮按用户要求继续作为 Agent B 复核修订 3，实现范围仍为 `TRACE-M1-03` 修订 3、R1～R7 与全部基本功能。开始于 `681b6d4308caa018abd21559123c90b69c1025f1`，工作区干净；以下新增修改全部归本轮 B，保留 F-001～F-032，尤其 F-029～F-032。父提交仍为初始化提交 `b208ad6`；本轮仅本地 amend，不 push、不创建 PR，也未请求真实站点。已按本机参考索引复查相应功能领域，未修改参考材料或引入依赖。

用户新增命名约定已回写 DEC-035：人读阶段标签/编号大写，机器标识中的阶段部分小写，阶段参数接受大小写并规范化小写。DEC-036 明确阶段编号与 GitHub PR 编号独立；旧交接 prompt 的父提交与工程规则的“从 0 开始”已改正。当前没有 `verify-stage` 命令，文档只规定后续参数行为。

### 9.1 已复现问题与修复证据

下列问题均已修复并完成定向回归，B 交付时状态为“B 已修待 A 复核”，A 的复核结论见 [§10](#a-review-rev3)。原始失败保留在“触发与实际结果”列；通过证据使用实际测试函数名。

| 编号 / 严重度 | 触发与实际结果 | 修复位置与预期行为 | 定向回归 |
| --- | --- | --- | --- |
| F-033 / P1 | 合成直播取流同时提供低档 AVC 与原画 HEVC，选择器先比较编码，返回低画质 AVC | `internal/site/bilibili/live.go`：在可录的 HLS fMP4 下先比较画质，同画质再按编码偏好；质量名称仍匹配选中流 | `TestLiveSelectionChoosesHighestQualityBeforeCodec`（高画质优先、同档编码偏好） |
| F-034 / P0 | 运行日志的脱敏清单漏掉 `auth_key`、`w_rid`、大写字段、JSON 刷新令牌、编码字段与未知签名查询，合成秘密实际写入日志；旧日志展示也可漏出 | `internal/service/backend.go`：写入和读取共用脱敏函数，识别凭证字段、JSON/大小写/编码形式及带查询串的完整 HTTP URL；正常 `mode=dev` 等状态保留 | `TestRuntimeLogsRedactSignedURLsAndCredentialFields`，保留 C37 原有全目录/DTO 哨兵 |
| F-035 / P1 | 日志页每次重绘都读整份日志，2 MiB 旧长行被读入并展示；诊断把 1000 条查询上限当作总量、吞读错误，关闭窗口后的检查进度不可见，损坏文件一律说“已从备份恢复” | `internal/service/backend.go`、`internal/subscription/{automation,manager}.go`、`internal/ui/{diagnostics,views}.go`：最多读日志尾部 1 MiB/500 行；后台每秒最多一次，重绘用缓存，错误可见且保留旧日志。诊断使用库统计、服务层当前进度与真实读失败，说明保留了损坏文件 | `TestRecentLogsReadOnlyBoundedTailAndRetainLatestLines`、`TestDiagnosticsUsesFullIndexCountAndReportsReadFailures`（1101 项与读失败）、`TestRuntimeLogsRefreshOncePerSecondAndShowReadFailures` |
| F-036 / P1 | 登录失效只切路由，媒体层仍遮住登录页；图片关闭后迟到加载可重新打开旧层，加载期间的缩放/平移会被结果重置 | `internal/ui/{app,media_controls}.go`：失效关闭媒体并取消图片请求，保留原路由/页面；按请求版本丢弃迟到结果，当前结果只更新位图/错误，保留交互 | `TestAuthExpiryClosesMediaOverlaysAndPreservesPage`、`TestClosedImageViewerRejectsLateFetchAndCancelsWork`、`TestNewImageViewerPreservesInteractionWhenFetchesFinish` |
| F-037 / P1 | “全屏”只改内存布尔值，没有调用原生窗口；分段切换还会把布尔值重置 | `internal/ui/{media_controls,overlays,app}.go`、`internal/shell/shell.go`：绑定 MyGo `win.SetFullScreen`；切分段保持全屏，关闭按钮/Esc/失效/关闭窗口恢复 | `TestFullscreenControlsCallWindowAndRestoreOnClose`（注入窗口回调，验证实际调用与退出），真实设备全屏留 H-001 |
| F-038 / P1 | 音量只有静音切换、必需弹幕密度缺失；恢复菜单把区域重置，保存的音量 0 变为 1，偏好保存失败被吞；普通按键不能唤醒隐藏控件，静止页面靠持续重绘计时。没有弹幕/字幕数据仍可操作相关菜单，字幕默认关闭显示为空白 | `internal/player/{player,danmaku}.go`、`internal/uistate/uistate.go`、`internal/ui/{app,overlays,media_controls}.go`：音量滑块/静音及密度控制立即生效并持久化，恢复区域/密度/0 音量，保存失败可见；密度按原时间线稳定筛选，任意按键唤醒，静止用 `c.After` 定时隐藏。无弹幕/字幕时禁用对应控件并说明；字幕关闭显示“关闭”，数据保存空语言标识，重启恢复 | `TestPlayerOffersVolumeAndDensityAndReportsPreferenceFailure`、`TestPlayerVolumeAndDensityInputsChangePlaybackAndPersist`（含字幕选择/关闭/恢复）、`TestPlayerMissingSidecarsExplainAndDisableTheirControls`、`TestPlayerPreferencesRestoreAreaAndSilentVolume`、`TestDanmakuDensityFiltersStableTimelineEntries`、`TestHiddenImageControlsWakeOnOrdinaryKey`，保留 C22 原有暂停/拖动/字幕/10k 性能断言 |
| F-039 / P1 | 新打开的订阅网格只显示已存的直播状态，不查询可见账号，粉环/一键录制不能主动出现；未订阅用户页的状态请求被服务层过滤掉。缓存读取会延长 60 秒期限，用户页还吞掉直播查询失败 | `internal/ui/{visible_live,subscriptions,network_actions,app}.go`、`internal/live/manager.go`：按虚拟列表可见行批量刷新，单批 ≤50，滚动补查；每账号尝试间隔 60 秒，风控停止自动刷新；缓存期限不因读取延长。未订阅账号也能返回状态且不启用自动录制，失败保留资料并显示原因，离页后丢弃迟到结果 | `TestSubscriptionGridRefreshesOnlyVisibleAccountsAndCachesAttempts`（1000 订阅、滚动、缓存）、`TestSubscriptionGridStopsAutomaticLiveRefreshAfterRiskControl`、`TestVisibleUnsubscribedCreatorGetsReadOnlyLiveStatus`、`TestVisibleLiveCacheExpiresFromOriginalFetch`（页面/直播间在第 45 秒重复读取，第 61 秒仍刷新）、`TestUserLiveFailureIsVisibleOnlyOnItsCurrentPage` |
| F-040 / P1 | 合成旧 `updates.db` 中，首次检查保存的历史快照在去掉已看字段后全部进入“更新”列表；已有增量即使曾被查看也应继续保留 | `internal/subscription/{migrations,manager}.go`：一次性事务增加可逆 `baseline_snapshot` 标志，按有效发现时间与基线时间排除旧快照；更新/最近更新/自动缓存/更新条数上限共用过滤。保留原行、快照与旧字段，迁移失败回滚 | `TestLegacyBaselineSnapshotsStayOutOfUpdatesWithoutDeletingRows`（两次重启、已查看增量仍可见、原行保留）、`TestBaselineMigrationFailureRollsBackSchemaAndRecords`，保留 C31 首次起点/增量/风控与独立成员基线 |
| F-041 / P1 | 最近更新聚合查询失败被忽略，订阅被显示为没有新动态；UI 刷新失败清空原列表并吞掉原因 | `internal/subscription/manager.go`、`internal/ui/{app,actions}.go`：传播聚合/查询/扫描错误，UI 保留上次订阅/任务并显示原因；登录失效后不重复读取受鉴权保护的列表 | `TestSubscriptionListReportsUpdateIndexFailure`、`TestSubscriptionReadFailurePreservesViewAndShowsReason` |

### 9.2 验证范围、恢复与限制

- 修复前的定向失败覆盖选流、脱敏/旧长行、失效遮挡、偏好恢复与按键唤醒、网格不查询、旧库混入、索引错误无反馈；全屏窗口调用与诊断总数按完整代码确认缺口，补回归核实。新增原生全屏证据验证的是窗口接口调用，不能代替用户对显示器、音频设备和全屏切换的体验检查。
- F-040 只分类时间可核对的旧快照；缺少/无效时间的旧记录保持可见，未猜测或删除。新增标志可撤销，原有 `seen_at` 只在合成测试中核对未被改写，生产代码不读写。迁移失败的列与数据事务回滚后可重试。
- 新增 `uishots` 的 `player-options` 与 `player-volume` 三档场景及必需控件的可见边界断言；直播/录制画面使用显式合成状态，不启动真实录制。最新完整门禁、截图、打包与冒烟均通过，实际用时与回执见 §3。
- 本轮没有新增真实站点请求，ACT-007 仍只证明原输入；H-001、大文件、安装/卸载、race 与真实 Ubuntu 保持独立验证边界。
- 过程中发现并修复的临时问题：日志测试最初未切入窄窗分类详情，改用真实分类点击；界面断言最初读取不存在的 `Rect.Rect` 字段，按 MyGo 实际返回类型改正；错误传播改动曾在登录失效后递归读取受鉴权保护的任务，15 秒有界回归暴露后改为先转登录并保留列表、避免重复失效处理。日志反例还覆盖混合有效转义与 `50%`、JSON 转义引号；缓存期限与用户页吞错先复现失败，修复后通过。
- 完整门禁曾因 `TestRevision3FeedbackReworkR1ToR7` 的 UI 预置状态被新增真实状态读取覆盖而失败；核对生产状态来源后，为该 UI 夹具注入包含原画/AVC 的完整服务返回，保留全部清晰度与旧录像回退断言。新增音量/密度交互测试采用显式静音合成解码器与弹幕/字幕数据，未靠删除或弱化断言通过。
- 整理范围仍为 M1：保留用户文件、旧包和真实回执；更新当前 `artifacts/uishots/`、`artifacts/smoke/`、`artifacts/review/` 与 `dist/trace-dev/`。读取旧 `artifacts/review/m0-recheck.md` 后确认其结论已由当前报告替代，删除该冗余旧阶段概要，当前 review 类别保留最新命令输出；未新增依赖、一次性仓库脚本或其他阶段源码。

<a id="b-recheck-uishots"></a>
### 9.3 本轮截图目视核对

使用 `view_image` 核对了 22 张选样，覆盖全部 17 个场景；播放选项与音量面板检查三档宽度，日志检查宽窗与 720px 分类详情，其余场景各检查一档。原图位于 `artifacts/uishots/`，全部 51 个布局回执通过；选样不是对 51 张逐张人工验收。

- `player-options`：弹幕开关、透明度、区域、密度、字幕与文件操作均可见；初次核对发现字幕关闭为空白，修复后在三档显示“关闭”。`player-volume`：滑块、静音、全屏及播放控制完整可见。
- 订阅网格/详情、任务、用户页和媒体库的直播/录制入口与“原画 · AVC”文字可见；任务失败原因、移除确认、已完成状态和清除按钮保留。
- 日志页显示真实索引总数与脱敏等宽日志；解析页没有无关后台状态。看图、播放与缺解码器提示保持深色沉浸布局，关闭与系统播放器入口可见。
- 各场景的设置齿轮、常规滑杆、日志图标、投币及悬浮按钮含义与图标表一致，选样未见横向溢出或必需控件裁切。全屏实际设备、声音与长期托盘内存仍由 H-001 验证。

<a id="a-review-rev3"></a>
## 10. A 集中复核修订 3（2026-11-08）

- **角色与基点**：Agent A 复核 B 交付的 `648932e8eee54a1da37b2d16bf832b2bfe77b0f9`（父提交 `b208ad6`），开始时工作区干净。A 的修复与本节 amend 进同一个未推送的 DCO 阶段提交，最终 SHA 见交付消息与 `dist/trace-dev/VERSION.txt`。未 push、未建 PR、未合并，未请求真实站点；测试只用合成/loopback 数据、静音输出和隐藏窗口；没有动用户数据、`.local/`、`.env.local` 和带日期的旧包。
- **审查方式**：先复用未变输入跑门禁（`tools/check` 通过），再跑 `go test -count=1 ./...`（不用缓存，16 个含测试包全部通过）。逐行阅读了 B 补充复核相对 `681b6d4` 的全部源码差异（`internal/live/manager.go`、`site/bilibili/live.go`、`service/backend.go`、`subscription/{manager,automation,migrations}.go`、`player/{player,danmaku}.go`、`uistate`、`shell/shell.go`，以及 `internal/ui` 的 `app`、`actions`、`overlays`、`media_controls`、`diagnostics`、`visible_live`、`network_actions`、`subscriptions`、`views` 差异）和对应回归；F-040 另对照用户首轮实机所用旧版本（`4c055a5`）写 `update_item` 与 `baseline_at` 的实际代码。R1～R7 的主体在 `681b6d4` 之前已合入：A 核对了已看标记在代码与文档中的残留（生产代码只剩建表语句里的 `seen_at` 列）、界面规范/设计/路线/需求的一致性、C14/C22/C31/C38 的断言仍在，并目视核对 `subscriptions`、`subscription-detail`、`settings-logs`、`tasks`、`parse`、`image-viewer`、`player-options`（1280 与 720）、`player-volume`（720）截图。R1～R7 其余界面代码没有逐行审阅，以 B 的定向回归和无缓存复跑为依据。

**F-033～F-041 结论**：

| 编号 | A 核实 | 状态 |
| --- | --- | --- |
| F-033 | 排序改为协议 → 格式 → `current_qn` → 编码偏好，与 DEC-024、[直播设计 §2](../design/live.md#recorder) 一致；`g_qn_desc` 名称按选中流的 qn 取 | A 已核实关闭 |
| F-034 | 写入与读取共用 `sanitizeLogLine`：凭证字段（含大小写、引号/转义、JSON 形式）、任何带查询串的 URL、凭证值本身都整行替换；最多三轮 URL 解码，`50%` 不阻断解码。只会多删，不会漏出 | A 已核实关闭 |
| F-035 | 只读尾部 1 MiB、丢弃残缺首行、最多 500 行；界面每秒最多一次后台读取、重绘用缓存；诊断用 `Statistics` 总数与真实读错，检查进度来自服务层 `CheckProgress` | A 已核实关闭 |
| F-036 | 失效时先关播放/看图层并取消图片请求，再转登录页；按 `imageViewerRevision` 丢弃迟到结果，结果只回填位图与错误，保留缩放/平移与 `ItemKey` | A 已核实关闭 |
| F-037 | 建窗后绑定 `win.SetFullScreen`；`App.Close` 在 `win.Close` 之前退出全屏；切分段不再重置全屏 | A 已核实关闭；真实显示器体验留 H-001 |
| F-038 | 音量 0 不再被改成 1（`Normalize` 只夹到 0～1）；密度按“每秒前 N 条”稳定筛选；区域、密度、字幕“关闭”可恢复；保存失败经 `showError` 显示；任意按键唤醒，静止改用 `c.After`，播放中仍由 `View` 持续刷新 | A 已核实关闭（另见 F-042） |
| F-039 | 可见行一批最多 50 个账号、每账号 60 秒、失败也等下一个间隔；缓存期限按原始取得时间，不因读取延长；未订阅账号只读查询，不启用自动录制；风控、限流、失效停止自动刷新 | A 已核实关闭 |
| F-040 | 旧版本首次检查把起点快照以 `discovered_at = seen_at = 当次时间` 写入，之后才把 `baseline_at` 设为同一秒或更晚，所以 `discovered_at <= baseline_at` 能准确圈出旧快照；迁移只在缺列时执行一次，事务失败回滚可重试，原行保留 | A 已核实关闭；限制见下 |
| F-041 | 最近更新聚合的查询、扫描、迭代错误都向上返回；界面保留上次列表并显示原因，失效时不重复读取 | A 已核实关闭 |

F-001～F-032（含 A 的 F-029～F-032）的回归在本次无缓存复跑中全部通过，修复没有被回退。

**A 新发现并直接修复**：

| 编号 / 严重度 | 触发与实际结果 | 依据 | 修复与复验 | 状态 |
| --- | --- | --- | --- | --- |
| F-042 / P2 | 播放层“⋯”菜单里弹幕“显示区域”下拉直接显示内部值 `quarter` / `half` / `full`（B 轮 `player-options-*.png` 三档都显示 `half`），其余选项都是中文 | [界面规范](../ui.md)的中文界面文案；REVIEW“可复现的局部错误” | `internal/ui/media_controls.go` 增加 `playerAreaLabels`（上方 1/4、上半屏、全屏）与双向映射，`overlays.go` 用标签渲染、选中后写回内部值，存储格式不变；界面规范写明三个选项。`TestPlayerVolumeAndDensityInputsChangePlaybackAndPersist` 增加断言：菜单里不出现机器值，选“全屏”后播放器区域为 1 并持久化。撤掉修复时该断言失败（`exposes the machine value "quarter"`），修复后通过；新截图三档显示“上半屏” | A 已修复并复验 |

**限制与建议（不算缺陷）**：

- F-040：旧版本里首次检查中途失败（已写入投稿快照、随后动态页出错，`baseline_at` 仍为空）的账号，迁移无法按时间判断，这些行保持可见；新代码下一次检查只建立起点，不再新增行。只影响旧库里失败过的首次检查，行没有删除。
- 字幕菜单直接列出站点语言代码（如 `zh-CN`、`ai-zh`），不算错误，但不如中文名直观；看图层背景是半透明深色，能隐约看到下面的页面（播放层是纯黑）。两项等 H-001 体验后再决定是否调整。
- 测试函数名 `TestBaselineAndIncrementalCheckThreePageAndMarkSeen` 仍带已删除的“MarkSeen”字样，断言已按 DEC-033 改写；属命名建议。
- 原生全屏、音频设备、长时间托盘内存、安装/卸载仍由 H-001 人工检查；`TRACE_RUN_LARGE=1`、`TRACE_RUN_INSTALLER_TEST=1`、`go test -race`、真实 Ubuntu 运行仍为 **not-run**。本轮没有新的真实站点证据，ACT-007 只证明当时的输入。

**验证（A 修复后）**：见 [§3 末尾](#a-rev3-evidence)。A 第一次修复后的门禁失败过一次：A 放在 `tmp/a-review/` 下用于对照的旧版本源码副本被 `go vet ./...` 当成包编译；改名为 `.txt` 后通过，与产品代码无关。

**结论**：

- **实现交付**：修订 3 的 R1～R7 与 F-033～F-041 已实现；F-001～F-042 在本地证据范围内关闭。
- **技术审查（A）**：通过，范围与未逐行审阅部分见上。本轮只发现局部问题，已由 A 直接修复，不需要交回 B。
- **产品验收**：未开始，由用户按 [H-001](../../USER-ACTIONS.md#h-001) 独立判断。

<a id="feedback-2"></a>
## 11. 第二轮实机反馈与 F-043（A，2026-11-08）

用户在 A 复核修订 3 后再次试用，提出 7 条反馈。1 是缺陷，A 直接修复为 F-043；2～7 是产品调整，记为 [DEC-037](../decisions.md#dec-037)，按[计划 §13](stage-plan.md#rev4) 的 R8～R13 交给 B。基点为 `9f8b648`，工作区干净；未请求真实站点，未 push。

| 反馈 | 归类 | 状态 |
| --- | --- | --- |
| 1 播放非常卡顿，甚至卡死 | 缺陷 F-043 | A 已修，见下 |
| 2 主页按钮太多 | 产品调整 R8 | B 已完成，待 A 复核（§12） |
| 3 不要“最近看过的用户”，主页标题下列有更新的头像 | 产品调整 R9（口径 Q-009） | B 已完成，待 A 复核（§12） |
| 4 按最后查看时间戳续看，不要顺序切换 | 产品调整 R10 | B 已完成，待 A 复核（§12） |
| 5 解析页封面与头像为空、文字按钮多 | 产品调整 R11 | B 已完成，待 A 复核（§12） |
| 6 媒体库按钮杂乱 | 产品调整 R12 | B 已完成，待 A 复核（§12） |
| 7 保持整洁 | 原则 R13 | B 已完成，待 A 复核（§12） |

**F-043 / P1（A 已修复并复验）**

- **触发与实际结果**：按界面的方式（约每 16 ms 一次）驱动 `9f8b648` 的解码路径，用户自己缓存的三个 60 fps 文件（只读使用）：1080×1920 视频每秒只出 6.6 帧、每次调用阻塞界面线程 p50 133 ms；1920×1080 直播录像 3.3 帧、p50 314 ms；2160×3840 视频 0.2 帧、单次调用 2.8～13 秒（即“卡死”）。CPU 剖析：75% 在 Media Foundation 的软件解码与旧式软件色彩转换里，24% 在 Go 的 BGRA→RGBA 逐像素转换里。
- **原因**：① 帧在界面线程的 `View` 里同步解码，落后时一次追最多 240 帧；② 读取器用 `MF_SOURCE_READER_ENABLE_VIDEO_PROCESSING`（只有软件路径，没有 GPU）；③ 视频与音频两个读取器都选中全部流，各自还在读对方的流；④ 追帧时每个被跳过的帧也做完整转换；⑤ 4K 帧按原尺寸上传，MyGo 每次 `Update` 还要在 CPU 上为缩小显示重建多级纹理。
- **依据**：FR-LIB-002、C22（播放可用、拖动后无残留）；REVIEW P1“核心流程错误”。
- **修复**：`internal/player/video_worker.go` 在单独的 goroutine 里按音频时钟解码，界面线程只上传已经解好的最新一帧；三缓冲交接（MyGo `Bitmap.Update` 在下一次更新前持有缓冲区），按 seek 代次丢弃过期帧，暂停时 seek 后请求一次重绘。`internal/player/mf_hw_windows.go` 与 `mf_windows.go`：视频读取器优先用 D3D11 设备管理器 + 硬件解码 + 视频处理器，在 GPU 上直接输出 RGBA 字节序（`ABGR32`）、全范围 0～255，并缩到长边不超过 1920；任一步失败依次回退到“软件 + 缩放”和原来的软件路径。两个读取器只选各自的流；追帧时目标之前的帧不做读回与转换。
- **复验**：
  - 修复后同样驱动真实的 `Controller`：三个文件都走 `gpu/abgr32`，每秒解出 59.3～60.1 帧（含 2160×3840，缩到 1080×1920），界面线程每次 `Advance` 几乎不耗时，播放中 seek 调用 2～4 ms；打开一个 4K 文件约 218 ms（一次性）。
  - 颜色：真实彩色录像上 GPU 路径与原软件路径的 R/G/B 均值相差不到 0.5（R 33.3、B 39.5，交换通道会立刻显出来），alpha 恒为 255。修复过程中发现 GPU 视频处理器默认输出有限范围（白 235、黑 16，画面发灰），已改为请求全范围；`TestDecodePathsAgreeOnPixels` 守住这一点。
  - 新增 `TestAdvanceNeverDecodesOnTheUIThread`：每帧解码 150 ms 的慢解码器下，`Advance` 最慢不超过 50 ms，暂停 seek 后新帧到达并请求重绘。`TestStaleFramesAreDroppedAndShownBufferIsNotReused`：seek 之前解出的帧不显示，屏幕上的缓冲区不会被交回解码。连续跑 3 次都通过。旧结构下界面线程就是解码线程，这两个测试无法对旧代码编译，失败证据是上面的修复前实测。
  - 完整门禁 `go run ./tools/check` **passed / 0**（36.6s）；`go run ./tools/uishots` **passed / 0**（51 张）。
- **限制**：
  - 没有可用 GPU 的电脑走软件路径：解码已不在界面线程，界面不会卡死，但高分辨率 60 fps 可能掉帧。
  - 打开文件仍在点击时同步进行（4K 约 218 ms）。
  - 真实音频设备下的音画同步与长时间播放由 H-001 人工检查；`go test -race` 仍因没有 cgo 为 not-run。
- **开发包没有更新**：`go run ./tools/package` 失败，原因是你正在从 `dist/trace-dev/trace.exe` 运行 TRACE（PID 5228，16:27 启动），文件被占用。A 没有结束你的进程。打包工具在失败前已经替换了安装包，所以 `dist/trace-dev/` 目前不一致：安装包是新的（6,809,876 字节，含 F-043，构建自未提交的工作区），`trace.exe` 还是 `9f8b648` 的，`SHA256SUMS.txt` 缺失；冒烟因此判失败。关掉这个 TRACE 后重新运行 `go run ./tools/package` 与冒烟即可恢复。在那之前，不要用 `dist/trace-dev/` 做 H-001。

<a id="rev4-b"></a>
## 12. B 执行修订 4（R8～R13，2026-11-08）

- **角色与基点**：Agent B 基于 `645b689`（含 A 的 F-043 修复，父提交 `b208ad6`）执行[计划 §13](stage-plan.md#rev4) 的修订 4（R8～R13，DEC-037）。未修改 `internal/player/` 的 F-043 解码实现；未请求真实站点；测试全用 loopback 夹具、静音输出（`nullSink`）和内存 `ui.NewTester`。在用户退出此前运行中的 `dist/trace-dev/trace.exe` 后，已完成 `go run ./tools/package` 与 `go run ./tools/smoke` 验证。

### 12.1 R8～R13 实现对照

本表记录 `818a163` 首次交付时的行为；缩略图命名/提交清理、主页状态和本机详情等补充修复见 §13，当前契约以对应设计为准。

| 项 | 修改位置 | 实际行为与验证入口 |
| --- | --- | --- |
| R8 主页帖子去按钮 | `internal/ui/{components,views,actions,subscriptions}.go` | 主页信息流（含主时间线与单订阅视图）的 `PostCard`（`InHomeFeed: true`）底部不渲染任何文字按钮，右上角只保留状态圆点；视频动态自动关联引用的缓存视频卡片，点视频卡片直接打开播放层、点图片打开看图层、点头像或昵称进入用户页。订阅详情“更新”与用户页“动态”的 `PostCard`（`InHomeFeed: false`）右上角提供“⋯”菜单（在浏览器中打开、复制链接），底部未缓存时只保留一个“缓存”按钮，已缓存时不显示按钮。“补存弹幕和字幕”与“升级缓存”只放在视频详情页 `/video`。测试：`TestRevision4FeedbackReworkR8ToR13` |
| R9 有更新的头像行与单订阅视图 | `internal/uistate/uistate.go`、`internal/library/{schema,statistics}.go`、`internal/ui/{app,feed,views}.go` | 删除右栏第 4 张卡片（“最近更新 / 最近看过的用户”及底部统计行），右栏仅保留快速解析、正在缓存与录制、正在直播三张卡片。`library.Statistics()` 返回每位作者的最新缓存发布时间 `LatestPubTs`；`uistate.State` 增加 `HomeSeen map[string]int64`。主页标题下方横向滚动行（`主页有更新头像行`）列出 `latestPubTs > max(anchor.PubTs, uiState.HomeSeen[key])` 的订阅/作者（56px 圆形头像 + 一行名称，按 `latestPubTs` 从新到旧排列，无更新时整行不显示）。点头像将 `HomeSeen[key]` 记为 `latestPubTs` 并进入单订阅视图（左上角 `返回主页时间线` 箭头 + 小头像 + 名称，列出该人全部关联账号的已缓存内容，不覆盖主时间线锚点）；点返回恢复主时间线原位置，已点过的头像从该行消失。测试：`TestRevision4FeedbackReworkR8ToR13`、`uishots` 的 `home` 与 `home-sub` |
| R10 固定从新到旧与按时间戳续看 | `internal/uistate/uistate.go`、`internal/library/library.go`、`internal/ui/{app,feed,views}.go` | 去掉主页顶部的“最新在前 / 最早在前”按钮；`uistate.Normalize` 将旧 `order="oldest"` 规范化为 `"newest"` 并保留 `Key`、`PubTs`、`OffsetPx`。`library.FeedPage` 支持仅凭 `PubTs` 的 `around` 双向加载：重启或返回主页时恢复到最后查看的条目与行内偏移（上方为更新内容，下方为更早内容，未发生用户滚动的短列表静置或切页不会覆盖已有锚点）；原锚点条目被删除时自动落到 `<= PubTs` 最近的一条。测试：`TestBidirectionalKeysetFeedAndAnchor`、`TestFeedProgressEventsAndRestartPreserveViewportAnchor`、`TestRevision4FeedbackReworkR8ToR13` |
| R11 解析页缩略图与“⋯”菜单 | `internal/storage/history.go`、`internal/service/backend.go`、`internal/ui/{app,views}.go` | `HistoryEntry` 增加 `AvatarThumb`、`CoverThumb`、`DurationText`；`service.OpenLink` 解析视频或用户成功时，通过无 Cookie、无 `Referer` 的图片通道把封面和头像缩略图存到 `<DataDir>/thumbs/<kind>-<id>-{cover,avatar}.<ext>`（校验尺寸 `<= 10 MiB` 与图像头，文件名不含查询串和签名参数）；删除单条、清空全部或超出 200 条上限淘汰时同步清理无引用缩略图文件。解析页只读本机缩略图显示 16:9 视频封面（含右下角时长角标）与圆形用户头像（缺失时用非空白占位色块），点条目本身（`历史打开：<id>`）打开详情，右侧“⋯”（`历史更多：<id>`）提供“在浏览器中打开”与“删除”，标题栏右侧“⋯”（`最近解析更多`）提供“全部清空”及行内二次确认（`确认清空历史`）。测试：`TestHistoryStoreCapDedupAndSanitize`、`TestServiceAuthIsolationOpenLinkAndRedaction`、`TestRevision4FeedbackReworkR8ToR13`、`uishots` 的 `parse` |
| R12 媒体库精简与重建索引归位 | `internal/ui/{library_view,views}.go` | “重建索引”从媒体库标题栏移入设置“存储”分类。媒体库每格只显示缩略图（右上角状态圆点、右下角时长）、一行标题、一行元信息和右下角“⋯”（`媒体更多：<tileKey>`），点缩略图或标题直接打开播放层或看图层，点“⋯”展开菜单（`媒体菜单：<itemKey>`：详情、在资源管理器中显示、删除）。测试：`TestRevision4FeedbackReworkR8ToR13`、`uishots` 的 `library` 与 `settings` |
| R13 保持整洁原则 | `docs/ui.md`、`docs/requirements.md`、`docs/design/{data-and-cache,architecture,subscriptions}.md`、`docs/delivery/{roadmap,status,stage-report}.md`、`USER-ACTIONS.md` | 界面规范 §1 写明整洁原则（卡片直接点击触发主操作，次要操作收进“⋯”菜单，不在卡片上堆叠多排文字按钮，不放重复导航的统计卡片），所有受影响文档与 H-001 步骤同步更新 |

### 12.2 验证与截图核对

本节是修订 4 首次交付 `818a163` 的记录；当前生成物已由 §13 的补充复核替换，旧哈希和用时只对应原输入。

- `go run ./tools/check`：**passed / 0**（含 `gofmt`、`go vet`、全部 16 个含测试包的单元测试、`INV-05` 依赖方向检查、`tools/checkdocs` 与 `git diff --check HEAD`）。
- `go run ./tools/uishots -out artifacts/uishots`：**passed / 0**，生成 **54 张 PNG**（18 个场景 × 1280/1000/720 三档宽度，新增 `home-sub` 单订阅视图场景），`assertions.json` 全部 54 条布局、固定侧栏、输入法及 `RequiredLabels` 可见边界断言通过。
- `go run ./tools/package`：**passed / 0**（12.213s），更新 `dist/trace-dev/`（`trace.exe` SHA-256 `0a4246d71be9359e44233a6c042970375d0d9fde778c962980a357260aca3667`、`trace-dev-setup-0.1.0-amd64.exe` SHA-256 `4e60c0f22cf48098d1a4f127190dc63f5a28a6bc5ffde9090a1ae60900784272`、`THIRD-PARTY-NOTICES.txt`、`VERSION.txt` 与 `SHA256SUMS.txt`）。
- `go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke`：**passed / 0**（2,831ms，`quietMode=hidden`，托盘专用工作集 `606,208` 字节 / `0.58 MiB`、已提交私有内存 `81,342,464` 字节 / `77.57 MiB`、`webview2=0`，单实例转发与数据目录锁通过）。
- 使用 `read_file` 目视核对了 `home-1280x860.png`、`home-sub-1280x860.png`、`parse-1280x860.png`、`library-1280x860.png`、`user-1280x860.png`、`settings-1280x860.png`：
  - `home`：“主页”标题下方显示有更新的圆形头像行，无“最新在前/最早在前”按钮，帖子底部无文字按钮，右栏无第 4 张“最近更新”卡片；
  - `home-sub`：头部显示返回箭头、小头像与订阅名称，列表仅展示该订阅的已缓存帖子且底部无文字按钮；
  - `parse`：“最近解析”显示本机缓存的 16:9 渐变视频封面（带 `02:00` 角标）与圆形头像，右侧“⋯”菜单展开显示“在浏览器中打开”与“删除”；
  - `library`：标题栏无“重建索引”，每格仅缩略图、一行标题、一行元信息和右下角“⋯”，展开菜单显示“详情”“在资源管理器中显示”“删除”；
  - `user`：未缓存动态底部仅一个“缓存”按钮，右上角有“⋯”按钮；
  - `settings`：“存储”分类包含“重建索引”按钮。

<a id="rev4-b-recheck"></a>
## 13. B 补充复核修订 4（2026-11-08）

- **范围与角色**：用户要求“仅作为 Agent B 复核修改并修复优化，直到可交 A”。继续[计划 §13.5](stage-plan.md#rev4-b-plan-recheck)的 R8～R13 和基本功能失败边界；不代替 A 关闭问题，不新增产品范围。
- **基点与输入**：`codex/m1-mygo-bilibili`，开始时工作区干净，基点 `818a163`，父提交仍为 `b208ad6`。复核修订 4 相对 `645b689` 的历史差异及本轮修改的调用链；测试采用真实合成清单、loopback HTTP、内存测试器与静音输出。`internal/player/` 相对基点无改动，保留 F-043 的异步解码、GPU 路径与回退。
- **数据与权限**：新增历史待清理字段及缩略图提交接口，旧历史文件名仍可读取；状态返回值隔离，失败保留已提交状态。本机详情不将清单冒充站点完整响应，不自动请求远程图片；用户点缓存时仍按已有鉴权流程获取站点元数据。本轮真实站点请求、安装、push、PR、合并均为 0。当前产物和哈希见 §3；收尾统一 amend，最终 SHA 见交付消息与包版本文件。

### 13.1 稳定问题与复验

下列 **F-044～F-055 均为 B 已修、待 A 复核**。记录区分实际复现与代码核对，未把样本/API 使用错误算作产品缺陷。

| 编号 / 严重度 | 触发与原结果 | 修复与实际证据 |
| --- | --- | --- |
| F-044 / P1 | 删除共享缩略图的一个历史引用会删掉仍有引用的图；ID 前缀清理会误删另一个对象；清空会删除目录内未登记的图片。补充 Windows 大小写引用测试又复现同一文件被误删（`thumbnail file not found`） | `storage/history.go` 按准确引用清理，Windows 文件名大小写折叠比较，读取也限制在已引用/暂存图片；不枚举目录删除。`TestHistoryCleanupKeepsSharedThumbnails`、`TestHistoryCleanupNeverUsesIDPrefixes`、`TestHistoryClearPreservesUnregisteredFiles`、`TestHistorySharedThumbnailRespectsWindowsFilenameCase` 通过 |
| F-045 / P1 | 同对象缩略图刷新原地覆盖，历史提交失败仍会改变旧图；新图/删除失败没有可恢复清理登记；原 10 MiB 限制在读取完整原图后才判断，且只校验图像头 | 对身份与内容哈希命名，`RecordWithThumbs` 串行提交，失败仅丢弃新候选；先持久化 `pendingThumbDeletes` 再清理，重启后可重试。读取阶段限字节、完整解码并缩放重编码。`TestHistoryThumbUpdatesCannotOverwriteReferencedFiles`、`TestHistoryThumbnailCommitFailureDiscardsOnlyNewFiles`、`TestHistoryThumbnailCleanupFailureIsRecoverableAfterRestart`、`TestImageByteLimitRejectsBodiesBeforeOrDuringRead`、`TestInvalidImageLimitDoesNotStartRequest`、`TestHistoryThumbnailsAreBoundedDecodedAndReencoded` 通过；声明超限正文读 0 字节，未知长度的 16 字节限额样本只读 17 字节；头像/封面分别验证 128×64 / 640×320、保留小图、拒绝坏图、去除 PNG 文本元数据 |
| F-046 / P2 | 从单订阅返回主时间线，只恢复行号，非零行内偏移从 68 变成 0 | 保存并恢复主 `ListState` 的键、测量高度/行内位置及滚动偏移，单订阅使用独立状态。`TestHomeActorReturnRestoresRowAndPixelOffset` 对真实 110 条合成清单滚动后往返，键一致、像素偏移差不超过 2，通过 |
| F-047 / P1 | 单订阅查询失败被当作部分列表，且查询成功前就保存按人已看时间 | 先成功读取再切换与标记，移除生产合成回退；失败保留主时间线和已看状态。`TestHomeActorReadFailureKeepsMainTimelineAndSeenState` 注入读取失败验证可见错误、列表和 `HomeSeen` 不变，通过 |
| F-048 / P2 | 代码核对发现锚点、按人已看、路由和清空历史写入错误被吞；“看最新”先清空旧锚点/列表；分页错误重绘自动重试 | 保存错误可见，最新读取/提交成功才切换；分页保留已有内容、失败方向及显式重试，单订阅往返也保留主分页错误；清空失败保留确认。`TestHomePersistenceFailuresAreVisibleAndKeepOldState`、`TestHomeLatestFailurePreservesAnchorAndVisiblePosition`、`TestHomePaginationFailureWaitsForExplicitRetry`、`TestHistoryClearFailureKeepsConfirmationAndReportsError` 通过 |
| F-049 / P2 | 代码核对 `UIState.Update` 返回已提交的 `HomeSeen` 映射，调用者可不写盘直接改变内存状态 | `Get` 与 `Update` 返回独立映射。`TestUIStateSeenMapsNeverAliasCommittedState` 验证修改输入、Get/Update 返回值不改变已提交状态，通过；这是静态发现加最终回归，未声称撤掉修复复跑 |
| F-050 / P2 | 85 条同时间戳清单，用时间戳或已删除键恢复选到 `tie_029`，遗漏最近的 `tie_084` | 在同筛选条件下先解析实际锚点，再双向分页，并让结果游标对应合并后的实际首尾。`TestFeedAroundTimestampChoosesNearestOlderItemAcrossTies` 验证时间戳/删除键回退和从中间页向两边继续加载，85 条无重复无遗漏，通过；夹具使用 `SaveItem` 清单，在线发现记录不冒充本机缓存 |
| F-051 / P2 | 主页测试器重绘 17 次触发 17 次全库统计查询，错误也被当作空头像行 | 统计只在本机刷新/入库事件读取，失败保留上次结果并显示原因，媒体库共用该结果。`TestHomeStatisticsReadsAreIndependentOfRedraws` 验证一次刷新后连续重绘不增加查询次数、错误不清旧结果，通过 |
| F-052 / P2 | 媒体库标题不是可点击动作；媒体清单 P01/P02 与 Parts P02/P01 顺序不同时，点击 P01 打开 P02，还会先打开一次首段 | 标题与缩略图共用动作，按实际登记路径先选文件，再只打开一次；弹幕/字幕同样按文件匹配。`TestLibraryTitleOpensMediaAndFileChoiceDoesNotDependOnPartOrder` 的标题/缩略图两入口均验证 P01 路径，通过；样本媒体用于选择验证，不声称该字节样本成功解码 |
| F-053 / P1 | 缓存动态详情丢失引用视频；录像详情被构造成 BVID，出现点播缓存/分 P 操作；本机部分视频详情丢已知缺失 P，并把不完整清单传作站点响应、缺失互动统计当成 0 | 本机详情按真实类型读取，动态保留本机图片/引用视频，录像显示直播间/规格/片段播放且浏览器入口指向房间；视频保留 `ExpectedPages`，未知统计/时长说明未保存，缓存调用传 BVID/选择页，由缓存器获取新元数据。`TestCachedDynamicDetailKeepsReferencedVideo`、`TestLiveLibraryDetailOnlyOffersRecordingActions`、`TestCachedVideoDetailRequestsFreshMetadataAndKeepsKnownMissingPages` 通过；最后一项先复现 `cached detail omitted a known missing part`，最终捕获的请求 `Detail=nil`、包含两页，无站点请求 |
| F-054 / P1 | 给主页测试器绑定真实调度入口后，渲染头像/封面会排队远程图片请求，缺本机动态图还回退到远程图片地址，违反 C24 | 主页/本机详情只复用已有内存位图或本机文件，未命中直接占位；移除主页动态图远程回退。`TestHomeRenderingAndLocalMediaNeverFetchRemoteImages` 检查调度队列和图片调用计数，主页和本机详情均为 0，通过；保留用户页公开图片失败缓存回归 |
| F-055 / P1 | §11 已实际记录占用 `trace.exe` 时，旧打包流程先删/覆盖其他文件，失败留下新旧混包且丢校验清单；版本刷新也先写版本再校验输入 | `tools/package/bundle.go` 校验/暂存输入、备份旧登记文件、失败恢复；备份位于包旁，恢复失败不随构建暂存清理消失。拒绝未登记覆盖，元数据刷新只替换版本与清单。`TestLockedBundleFileRestoresPreviousPackage` 在隔离目录用 Windows 共享锁分别占用程序/版本文件，原全部文件与旧清单保持不变；其余 `TestBundle*` 验证坏输入/清单、未登记文件、旧登记淘汰和保留二进制，均通过；不关闭用户进程 |

<a id="rev4-b-validation"></a>
### 13.2 验证与截图

被测源码为 `818a163` 加本轮修复，后续只同步文档/提交元数据。单轮命令均有界；源码修改后重新验证相关包，最终完整门禁覆盖收尾输入。

| 命令与证据 | 实际结果 | 范围与限制 |
| --- | --- | --- |
| `go test -count=1 ./internal/library ./internal/ui ./tools/package ./internal/site/bilibili ./internal/service ./internal/storage ./internal/uistate` | passed / 0，七个包无缓存通过；随后新增的 Windows 大小写与本机详情补充回归也分别无缓存通过 | 合成/loopback；保留原有效断言 |
| `go run ./tools/check`；`artifacts/review/check.log` | passed / 0，工具 32.168s（外层命令 32.379s） | 格式、vet、17 个含测试包（部分复用 Go 缓存）、INV-05、文档与空白全部通过；受影响七包的无缓存结果另列于上一行 |
| `GOOS=linux CGO_ENABLED=0 go vet ./...`；`artifacts/review/linux-vet.log` | passed / 0，2.496s | 只证明 Linux 目标静态检查，未运行 Ubuntu 测试 |
| `go run ./tools/uishots -out artifacts/uishots` | passed / 0，29.204s，63 张/63 条断言全通过 | 21 场景 × 1280/1000/720；新增 `cached-video`、`cached-dynamic`、`recording-detail`；自动布局/控件可见边界、固定侧栏、组合输入守卫 |
| `go run ./tools/package`；`artifacts/review/package.log` | passed / 0，工具 15.715s（外层命令 16.118s） | 新包/完整许可/版本/清单均存在并通过校验；未安装，哈希见 §3 |
| `go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke` | passed / 0，工具 2,877ms（外层 3.073s） | 隐藏窗口、单实例转发、数据锁与路径隔离、0 WebView2、无抢焦点；托盘专用工作集 622,592 字节 / 0.59 MiB，已提交私有内存 80,867,328 字节 / 77.12 MiB；只观察短时关窗，未证明长期驻留 |

使用 `view_image` 核对最新 **26 张选样，覆盖全部 21 个场景**：`home`、`home-sub`、`library` 检查 1280 与 720；`cached-video` 检查三档；其余 `subscriptions`、`subscription-detail`、`parse`、`video`、`cached-dynamic`、`recording-detail`、`user`、`library-delete`、`tasks`、`settings`、`settings-logs`、`login`、`image-viewer`、`player`、`player-missing-decoder`、`player-options`、`player-volume` 各检查一档。主/单订阅无底部文字按钮，右栏三卡，解析本机缩略图/菜单、媒体库菜单、三种本机详情、未知数据说明及录像规格都可见；没有把 63 张说成逐张人工验收。

本轮首次 60 张截图跑次因新增图片使 720 媒体库的旧菜单目标位于视口下方而失败（59 条通过），改为展开首个可见条目的菜单后保留原断言通过；本机视频详情补充为第 21 场景，最终 63 条全通过。目视发现录像规格前多出分隔符，已修且在最后截图复验。新增代码/测试首次编译的返回类型与接口调用错误已按实际 API 修正；早期时间戳样本误用在线发现记录，改为真实本机清单。上述验证代码/样本问题不计作产品缺陷，未删除或放宽有效断言。

### 13.3 整理、限制与审查边界

- 当前类别只保留最新回执、日志、截图与包，旧历史结论继续留在本报告并注明适用输入。工程规则从旧 17 场景/51 张更新为当前 21/63，测试包更新为 17；H-001 更新包哈希与本机详情/行内位置检查步骤。
- 只读本机参考索引已检查主页时间线、缩略图、媒体动作与包恢复领域，未找到需引入的新参考能力，未复制代码、改索引或新增依赖。旧日期包、`.local/`、`.env.local`、用户媒体与 `artifacts/site-check/` 回执保留。
- **not-run**：真实站点复跑、音频设备与原生全屏实机、安装/卸载、`TRACE_RUN_LARGE=1`、`TRACE_RUN_INSTALLER_TEST=1`、`go test -race`（无 cgo 工具链）、真实 Ubuntu 运行及 H-001。既有 ACT-007 和 F-043 的实测只证明其原输入；本轮保留解码源码并运行对应回归，不声称再次测过用户 60 fps 文件。HLS fMP4 以外仍属 LOOP-004。
- 本轮为 B 自检，未做 A 集中审查或产品验收。A 重点核对缩略图待清理/共享引用、UI 状态所有权、真实锚点与列表状态、本机清单与站点响应边界、播放器按文件选段/附件、包失败回滚。回滚再次受阻时保留备份并报告位置，这是恢复边界，不承诺多文件替换在任意进程中断时原子完成。

### 13.4 下一步与给 Agent A 的交接 Prompt

首选动作：由 **Agent A 集中复核修订 4 及本轮补充修复**，确认 R8～R13 和 F-044～F-055 后再安排 H-001。A 未核实前问题状态保持“B 已修、待 A 复核”；push/PR/合并/发布继续按 [ACT-001](../../USER-ACTIONS.md#act-001) 等具体授权。

```text
请仅作为 Agent A，集中复核 TRACE-M1-03 修订 4（R8～R13、DEC-037）及 B 补充复核 F-044～F-055。
分支 codex/m1-mygo-bilibili，当前 HEAD 为唯一未推送 DCO 阶段提交，父提交 b208ad6；交付 SHA 见交付消息和 dist/trace-dev/VERSION.txt，本轮 B 基点 818a163，修订 4 原基点 645b689。
先读 AGENTS.md、REVIEW.md、USER-ACTIONS.md、docs/delivery/stage-plan.md §13、docs/delivery/stage-report.md §11～§13，以及受影响的数据/界面/工程契约；按规则查只读本机参考索引。
保留 F-001～F-043，尤其 internal/player 的 F-043 异步解码/GPU/回退；不弱化 C03/C24/C25/C26/C38。重点核对历史缩略图准确引用（含 Windows 大小写）和提交/清理失败恢复、HomeSeen 返回值隔离、单订阅失败与位置往返、同时间戳双向分页、主页零图片请求、本机三种详情与缓存时获取完整站点元数据、按登记文件选段和附件、包替换/版本刷新失败回滚。
按 REVIEW 复用输入未变证据，必要时跑 go run ./tools/check、定向无缓存回归和 tools/uishots；当前有 63 张布局回执、开发包与隔离冒烟，未运行项见报告，不当成产品验收。局部问题直接修复，较大缺口一次性交回 B，记录 A 的实际审查范围与结论。
本次仅合成/loopback、静音/隐藏测试，不请求真实站点、不碰用户媒体、旧日期包、.local/、.env.local；不安装、不 push、不建 PR、不合并/发布。核对远端引用后本地修复/报告 amend -s 进同一提交；提交后若只改文档/元数据，用 tools/package -refresh-version 对齐版本并核对清单。H-001 保持 not-run，给出一个首选下一步。
```

<a id="a-review-rev4"></a>
## 14. A 集中复核修订 4 与第三轮实机反馈（2026-11-08）

- **角色与基点**：Agent A 复核 B 交付的修订 4（R8～R13、DEC-037）与补充复核 F-044～F-055，同时处理用户第三轮反馈（三张截图：用户页帖子带一排“在浏览器中打开/复制链接”和“缓存”按钮、哔哩哔哩浮动“⋯”菜单作参考、用户页按钮堆积且只显示一条动态）和随后的设置页要求。分支 `codex/m1-mygo-bilibili`，父提交 `b208ad6`；A 的修复与本节 amend 进同一个未推送的 DCO 阶段提交，最终 SHA 见交付消息与 `dist/trace-dev/VERSION.txt`。未 push、未建 PR、未合并，未请求真实站点；测试只用合成/loopback 数据、静音输出和 `ui.NewTester`；没有动用户数据、`.local/`、`.env.local` 和带日期的旧包。
- **审查方式与实际范围**：复跑完整门禁与受影响包的无缓存测试；逐行读了历史缩略图存储（`internal/storage/history.go` 的引用计数、暂存/待清理、Windows 大小写折叠）、`OpenLink` 与解析/历史调用链、`internal/ui` 中主页、用户页、订阅详情、媒体库、解析页、设置与播放器打开路径。`internal/uistate`、`internal/library` 的时间戳双向分页查询与 `tools/package/bundle.go` 只读了 B 的测试与关键函数，没有逐行审阅。

### 14.1 F-044～F-055 结论

| 编号 | A 核实 | 状态 |
| --- | --- | --- |
| F-044、F-045 | 读完历史存储：只按准确引用清理、大小写折叠比较、新候选按身份+内容哈希命名、失败只丢新候选、`pendingThumbDeletes` 先落盘再删；A 新增的 `AttachThumbs`（F-057）复用同一提交/清理路径，条目已删时不写盘并清掉新暂存文件（`TestHistoryAttachThumbsKeepsPlaceAndSkipsRemovedEntries`） | A 已核实关闭 |
| F-046、F-047、F-048、F-051、F-052、F-054 | 读了对应 UI 调用链（主时间线位置往返、单订阅失败回退、保存/分页错误可见、统计只在事件时读取、按登记文件选段、主页不排远程图片）；无缓存回归通过 | A 已核实关闭 |
| F-049、F-050、F-053、F-055 | 回归无缓存通过，抽读关键函数，未逐行审阅；未发现反证 | 按 B 证据关闭，审阅范围如上 |

R8～R13（DEC-037 的主页、解析页、媒体库整洁调整）在截图与测试中成立；其中“非主页帖子未缓存时保留一个‘缓存’按钮”被第三轮反馈改为收进“⋯”（DEC-038）。

### 14.2 第三轮反馈：A 发现并直接修复

| 编号 / 严重度 | 触发与实际结果 | 根因 | 修复与复验 | 状态 |
| --- | --- | --- | --- | --- |
| F-057 / P1 | 点开视频后要等一阵才跳转（用户反馈 1） | 原流程在界面等待 `OpenLink` 全部完成才换页，而 `OpenLink` 串行做了：视频详情请求 → 下载头像和封面并解码缩放写入缩略图 → `CheckUpgradeKeys` 检查画质更新。打开播放器时 `OpenMediaFile`（Media Foundation）与弹幕 XML 解析也在界面线程 | `OpenVideo`/`OpenUser`/历史条目先切到目标页，用卡片上已有的标题、作者、封面显示“正在打开…”，`OpenLink` 只做详情请求；缩略图（`FetchHistoryThumbs` → `AttachThumbs`）与画质检查在之后后台进行。每次打开有序号，后点的覆盖先点的，离开页面后迟到结果不拉回；失败在该页显示原因和“重试”。播放器在后台打开文件、读弹幕字幕，界面显示加载圈，关闭或换段后迟到的解码器被关闭。`TestOpeningFromCardShowsPageBeforeOpenLinkReturns`（门控后端：`OpenLink` 返回前页面已切换；先点的结果被丢弃）、`TestPlayerOpensFilesOffTheUIThread`、服务层测试断言 `OpenLink` 不再带缩略图 | A 已修，无缓存回归通过；真实网络下的体感留 H-001 |
| F-058 / P1 | 用户页与订阅“已缓存”只显示一两条（用户反馈 2、截图 3） | 列表上方的横幅、统计、直播横幅、标签固定不动，`ui.List` 嵌在外层滚动里且写死 `Height(460)`；1000×860 下只露出 0～1 行 | 头部改为列表第 0 行随内容滚走，列表 `Grow(1)` 占满中栏；用户页、订阅“更新”“已缓存”同改。`TestDetailListsFillTheColumnAndScrollTheirHeader`（列表高 ≥ 700、可见多行、滚动后头部离开视口、矮窗口仍见内容）；新截图场景 `subscription-cached` | A 已修 |
| F-056 / P2 | 帖子与用户页到处是单独文字按钮，“⋯”展开成页面里的一排胶囊（用户反馈 3、截图 1/3） | “⋯”用页面内展开行实现；用户页头部与直播横幅堆了“在浏览器中打开”“订阅该 UP 主”“开始录制”“浏览器观看”，投稿行也有按钮 | 新增 `MoreMenuButton`（MyGo `PopoverMenuButton` 浮动菜单，按钮下方右对齐，危险项红字加分隔线）。帖子、投稿、最近解析、媒体库、订阅页顶部、订阅管理、订阅直播与录像全部改用；帖子无任何文字按钮（“缓存”在菜单首项）；用户页头部只剩“订阅/已订阅”和“⋯”，直播横幅只剩“录制”。回归改用 `Tester.Menu`/`ChooseMenuItem`，断言各菜单完整项目顺序，原有断言未弱化 | A 已修 |

**同时修掉的小问题**：订阅失败原因被吞（现在显示在按钮下）；用户页加载更多的等待键与实际请求键不一致，转圈不显示；订阅“已缓存”类型显示内部值 `video`/`live`（改为“视频/动态/直播录像”）；设置下拉框显示内部值（`best`、`avc`、`tray`、`all` 等）；视频页“进入空间”、订阅详情“检查更新”空状态按钮与设置内容里的分类标题重复出现；从主页直接点头像进入用户页时没有保存主页阅读位置。

### 14.3 设置页与浮动侧栏（DEC-038、Q-010）

- **设置**：打开设置时左栏换成设置侧边栏（返回箭头“设置”，八个分类只有图标和名称）；宽 ≥ 1000 时侧边栏 240px 显示名称，更窄只显示图标。设置项默认没有说明小字，只有“关闭窗口时”“定时检查更新”“缓存保存位置”“添加已有的缓存文件夹”保留一行后果说明；下拉框显示中文选项名，存储值不变；删掉与“编码偏好顺序”第一项重复的“首选视频编码”。`TestSettingsSidebarShowsNamesOnly`。
- **浮动侧栏探索**：MyGo 的圆角、细边框与 `Shadow` 足以把现有侧栏画成离窗口边缘 10px 的浮动面板，位置、宽度、固定不滚动的行为都不变；以 `TRACE_UI_FLOATING_NAV=1` 开关预览，截图场景 `home-floating-nav`（三档宽度目视无裁切）。悬停展开、覆盖内容的停靠栏会遮住中栏并改变 NFR-UI-001 的固定侧栏约定，不推荐。默认保持贴边侧栏，等用户在 [Q-010](../../USER-ACTIONS.md#q-010) 决定。

### 14.4 整理

用户要求清理无用文件。A 用 `golang.org/x/tools/cmd/deadcode@v0.52.0 -test ./...` 找出 11 个任何代码和测试都不调用的函数并删除（`appdirs.AppName/DisplayName/Identifier`、`Lock.Path`、`Store.persistLocked`、`parseM3U8Playlist`、`Controller.LoadFile`、`ParseLiveStreamExtra`、`UnmarshalJSONHelper`、`selectDashVideo`、`codecMatchScore`；计划里对 `AppName()` 的描述改为实际调用的 `loc.AppName`）；删除后复跑为 0。只被测试调用的函数（合成媒体、MP4 检查、站点检查闸门等）是测试工具，保留。删掉根目录误生成的 `uishots.exe`、空目录 `build/` 与 `.mygo/`、`tmp/` 下 A 的临时脚本与日志，以及 `artifacts/review/` 中不再被引用的旧提交说明与旧产物清单；其余 `artifacts/` 只保留最新一份。带日期的旧包（含旧版数据与登录凭证）和 `.local/`（Cookie、检查目标、旧调研抓包）属于用户本机数据，A 先询问；用户选择“旧包全部删除、`.local/` 按推荐清理”后，A 确认没有 TRACE 运行再删除：12 个旧包目录（约 1.6 GB）、两个 `.har` 抓包、哔哩哔哩以外的 6 个 Cookie、`.local/site-check/` 的旧运行记录（约 1.6 GB）；保留 `targets*.json`、`usage*.json`、`bilibili.cookie`，`.env.local` 只留哔哩哔哩 Cookie 路径（`TRACE_TEST_PROXY` 没有代码读取）。H-001 第 25 步改为接入任意已有缓存文件夹，见 [USER-ACTIONS 本机文件](../../USER-ACTIONS.md#local-files)。

<a id="a-review-rev4-validation"></a>
### 14.5 验证

| 命令与证据 | 实际结果 | 范围与限制 |
| --- | --- | --- |
| `go test -count=1 ./internal/ui ./internal/service ./internal/storage ./internal/player` | passed / 0，10.1s | 合成/loopback、静音；新增 6 个回归 |
| `go run ./tools/check`；`artifacts/review/check.log` | passed / 0，工具 18.584s（部分包复用 Go 缓存，受影响包的无缓存结果见上一行） | 删除无用函数后的收尾输入；格式、vet、17 个含测试包、INV-05、文档与空白 |
| `GOOS=linux CGO_ENABLED=0 go vet ./...`；`artifacts/review/linux-vet.log` | passed / 0，1.9s | 只证明 Linux 目标静态检查 |
| `go run ./tools/uishots -out artifacts/uishots` | passed / 0，69 张/69 条断言 | 23 场景 × 1280/1000/720；新增 `subscription-cached`、`home-floating-nav`；用户页、解析页、媒体库、订阅已缓存、设置（1280/720）、浮动侧栏（1280/1000）已目视核对。删无用函数没有改界面代码，未重跑 |
| `go run ./tools/package`；`artifacts/review/package.log` | passed / 0，9.741s | 未安装；哈希见 §3 与 H-001 |
| `go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke` | passed / 0，2,886ms；托盘专用工作集 0.58 MiB，已提交私有内存 79.52 MiB | 隐藏窗口、单实例、路径隔离、0 WebView2；只观察短时关窗 |

**not-run**：H-001 实机体验（含真实网络下的打开速度、音频设备、原生全屏）、真实站点、安装/卸载、`TRACE_RUN_LARGE=1`、`TRACE_RUN_INSTALLER_TEST=1`、`go test -race`（无 cgo 工具链）、真实 Ubuntu 运行。

### 14.6 结论与下一步

- **实现交付**：修订 4 的 R8～R13、F-044～F-055 与 A 的 F-056～F-058、DEC-038 设置调整已实现。
- **技术审查（A）**：通过，范围与未逐行审阅部分见 §14.1。本轮问题都已由 A 直接修复，不需要交回 B。
- **产品验收**：未开始，由用户按 [H-001](../../USER-ACTIONS.md#h-001) 独立判断。
- **首选下一步**：用户用最新 `dist/trace-dev/` 按 H-001 体验（重点：点开视频的速度、用户页与订阅列表、“⋯”菜单、设置侧边栏），顺便看浮动侧栏截图回复 Q-010。push/PR/合并仍按 [ACT-001](../../USER-ACTIONS.md#act-001) 等具体授权。

<a id="feedback-4"></a>
## 15. 第四轮实机反馈：拖动卡顿与界面改版（2026-11-08）

- **角色与基点**：Agent A 处理用户第四轮反馈：① 打开视频后拖动进度条时画面和声音都卡顿、乱跳；② 媒体库都是已缓存的内容，不该有绿色状态圆点；③ 字体与主题参考 Cindy 设计系统；④ 布局参考三张截图（图标栏 + 会话列表 + 内容区；素材库的左侧文件夹 + 网格 + 右侧详情；下载器的粘贴链接栏与 16:9 卡片网格，后续兼顾媒体库功能）；⑤ 强调色不用粉色，试清新的绿或蓝。分支与提交同 §14，A 的修改 amend 进同一个未推送的 DCO 阶段提交。未 push、未建 PR，未请求真实站点；测试只用合成数据、静音输出和 `ui.NewTester`；没有动 `.local/`、`.env.local` 与用户媒体。

### 15.1 F-059：拖动进度条时画面与声音卡顿

| 编号 / 严重度 | 触发与实际结果 | 根因 | 修复与复验 | 状态 |
| --- | --- | --- | --- | --- |
| F-059 / P1 | 播放中按住进度条拖动或单击跳转：画面停住、声音先跳过去再来回跳，松手后一段时间才恢复（用户反馈 1） | 三处叠加：（a）进度条每次 `Changed()` 都调用 `Controller.Seek`，拖动一下就是几十次跳转；（b）每次 `Seek` 由界面所在的调用把音视频两个读取器都移到关键帧，声音输出线程随后又移动一次（同一个跳转做两次，且跨线程交错）；（c）跳转后声音立刻从新位置播放，视频线程还在解码到新位置，画面落后、声音先跑 | 拖动中调用新增的 `Controller.Preview`：暂停声音，只把目标交给视频后台线程，后台用 `MFDecoder.PreviewFrameAt` 取目标附近的关键帧显示；松手（`PressedButton` 结束）或单击才 `Seek` 一次。`Seek` 只记录目标、请求新画面并交给声音线程，后者只移动音频读取器（`splitDecoder.SeekAudio`）。播放中跳转时声音先停住，视频线程发布新位置的第一帧后再继续（最长等 1.5 秒）。新增 `internal/player/seek_test.go`（声音线程只移动音频读取器；拖动只预览、松手只跳一次并恢复播放；播放中跳转等第一帧再出声）与真实 Media Foundation 的 `TestPreviewAndAudioSeekOnMediaFoundation`（合成 MP4） | A 已修；真实 4K/60fps 长视频与音频设备上的体感留 H-001 |

### 15.2 界面改版（DEC-039）

- **布局**：按截图 4/6 的结构改为图标栏（64px，只放图标、任务角标与订阅直播小点）+ 上下文侧栏（主页“全部动态 / 有更新 / 正在直播”、订阅列表、媒体库类型与来源及“N 项 · 大小”、视频与用户页的“最近解析”、设置分类）+ 页面（顶部栏标题、快速解析框、页面操作）。右栏取消：快速解析在顶部栏，缓存与录制数量在任务角标，正在直播在主页侧栏。窄窗（< 1000px）没有侧栏，主页“有更新”和媒体库筛选退回页面里。新代码在 `internal/ui/shell.go`。
- **视觉**：参考 Cindy 的方法（暖灰白底 `#f8f8f6`、`#262626/#737373/#a3a3a3` 三级文字、1px 细线、几乎不用阴影、字重 500/600），色值是 TRACE 自己的。强调色默认蓝 `#1a73e8`，绿 `#0a8754` 以 `TRACE_UI_ACCENT=green` 和截图场景 `home-green-accent` 预览（替换原 `home-floating-nav`，场景数不变），等用户在 [Q-011](../../USER-ACTIONS.md#q-011) 选择；两者白字对比度都 ≥ 4.5:1。直播标记改为橙色 `#c2410c`，托盘图标改为蓝底。代码里的 `ColorAccentPink*`、`Pink*PillButton` 改名为 `ColorAccent*`、`Accent*PillButton`。
- **字体**：Inter 4.1 可变字体随程序打包（`internal/ui/fonts/`，OFL-1.1，来源与哈希见该目录 README 与[选型资料](../research/2026-11-05-stack.md)），`tools/package` 把 OFL 原文写进 `THIRD-PARTY-NOTICES.txt`，缺文件时打包失败。中文由 DirectWrite 回退到系统字体；A 用 `ui.NewTester` 实测 400～700 字重与中文回退正常。
- **状态圆点**（用户追加反馈）：去掉圆点里的对勾、短横和 ×，改为 8px 纯色小点；一条帖子只显示一个圆点，取动态本身与其中视频的较差状态（`postDotStatus`：失败 > 缓存中 > 不完整 > 完整），视频卡片和视频详情页封面上不再另画圆点。`TestPostShowsOneDotForItsWholeContent` 覆盖完整、视频部分缓存两种情况。
- **媒体库**：去掉状态圆点（都是已缓存的内容）；卡片约 240px 宽、按页面宽度每行 2～6 张、16:9 圆角缩略图，标题最多两行。截图 5 的右侧详情面板与截图 6 的“粘贴链接以下载”入口没有做，留到后续媒体库迭代。
- **测试调整**：三栏宽度断言改为图标栏 64px、侧栏 272/240/无（1280/1000/720）且位于图标栏与页面之间、页面滚动时两者不动；设置页“不应出现”的列表改为别的页面的侧栏条目；单订阅视图允许“有更新”留在侧栏（但不能在页面里）；两处滚动距离因 680px 页面与新字体改为 541 才落在行中间。新增 `TestLibraryFiltersLiveInTheContextPanel`、`TestHomePanelListsLiveRooms`。原有断言的意图没有弱化。

### 15.3 目视核对

69 张截图中 A 看过：主页 1280（每条帖子一个小圆点，视频卡片无圆点）与 720（720 无侧栏、头像行退回页面）、媒体库 1280（无圆点、16:9 卡片）、设置 1280、视频详情 1280（圆点在按钮旁、封面无圆点）、订阅详情 1000、用户页 1280、绿色预览 1280。没有发现裁切或重叠。

<a id="feedback-4-validation"></a>
### 15.4 验证

| 命令与证据 | 实际结果 | 范围与限制 |
| --- | --- | --- |
| `go test -count=1 ./internal/ui ./internal/player` | passed / 0，15.2s | 合成数据、静音；含新增的 4 个播放器测试与 3 个界面测试 |
| `go run ./tools/check`；`artifacts/review/check.log` | passed / 0，58.4s（部分包复用 Go 缓存，受影响包的无缓存结果见上一行） | 格式、vet、全部含测试包、INV-05、文档与空白 |
| `GOOS=linux CGO_ENABLED=0 go vet ./...`；`artifacts/review/linux-vet.log` | passed / 0，1.3s | 只证明 Linux 目标静态检查 |
| `go run ./tools/uishots -out artifacts/uishots`；`artifacts/review/uishots.log` | passed / 0，69 张/69 条断言，11.9s | 23 场景 × 1280/1000/720 |
| `go run ./tools/package`；`artifacts/review/package.log` | passed / 0，12.0s | 未安装；哈希见 §3 与 H-001；通知文件含 Inter OFL |
| `go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke`；`artifacts/review/smoke.log` | passed / 0，2,937ms；托盘专用工作集 0.61 MiB，已提交私有内存 80.94 MiB | 隐藏窗口、单实例、路径隔离、0 WebView2 |

**not-run**：H-001 实机体验（拖动进度条的体感、真实 4K/60fps 视频、音频设备、原生全屏）、真实站点、安装/卸载、`go test -race`（无 cgo 工具链）、真实 Ubuntu 运行。

### 15.5 结论与下一步

- **实现交付**：F-059 与 DEC-039 改版已实现；Q-010 浮动侧栏随改版不再采用。
- **技术审查（A）**：通过，问题都已直接修复，不需要交回 B。
- **产品验收**：未开始，由用户按 [H-001](../../USER-ACTIONS.md#h-001) 判断。
- **首选下一步**：用户用最新 `dist/trace-dev/` 按 H-001 体验（重点：第 8 步拖动进度条、第 7 步新布局、媒体库），并回复 [Q-011](../../USER-ACTIONS.md#q-011) 选蓝还是绿。push/PR/合并仍按 [ACT-001](../../USER-ACTIONS.md#act-001) 等具体授权。

<a id="feedback-5"></a>
## 16. 第五轮实机反馈：共用缓存规则、主页流畅度与帖子排版（2026-11-09）

- **角色与基点**：Agent A 处理用户第五轮反馈：① 缓存规则统一管理、多个订阅共用，订阅只选规则；订阅页去掉“更新”“已缓存”“规则”，用一个按钮；② 设置侧栏字体太大，设置内容白、灰、白层层嵌套；③ 主页浏览掉帧；往下滑不要把上面的重新当未读，往上翻过就是看过，不需要恢复；④ 看图和视频不占满整个应用，改为悬浮框，视频自动播放；⑤ 主页图片显示不全、视频封面看不全，视频封面与标题简介左右排列，图片像哔哩哔哩官方那样尽量完整；不缓存播放量、评论量等会变的数据；⑥ 实况图显示有问题。决定见 [DEC-040](../decisions.md#dec-040)。分支与提交同 §15，A 的修改 amend 进同一个未推送的 DCO 阶段提交。未 push、未建 PR，未请求真实站点；测试只用合成数据、静音输出和 `ui.NewTester`；读取了用户本机一条已缓存实况动态的清单（只读，未改动），没有动 `.local/`、`.env.local`。

### 16.1 缺陷

| 编号 / 严重度 | 触发与实际结果 | 根因 | 修复与复验 | 状态 |
| --- | --- | --- | --- | --- |
| F-060 / P1 | 主页快速滚动掉帧（用户反馈 ③） | 用 Go 的 CPU 采样对合成主页（5,000 条）连续滚动取样：界面代码约 90% 的时间花在每帧、每个可见帖子调用 `GetLibraryItem`（读 SQLite 并解析清单 JSON）重建帖子数据；另外本机图片第一次出现时在界面线程按原图尺寸解码 | 帖子显示数据按条目缓存（`postCards`），任务、媒体库、订阅、直播事件到来时整体丢弃重建；本机图片改为后台解码并缩到最长边 1280px（`loadThumbBitmap`，与在线缩略图共用 2 个并发名额），先画浅灰占位。同一合成场景下单帧耗时约 3.9ms → 2.2ms，界面代码采样 0.24s → 0.04s。新增 `TestHomeRedrawsDoNotQueryTheLibraryPerFrame`（重绘 10 次不读媒体库，媒体库事件后才重建）与 `TestLocalThumbnailsLoadInTheBackgroundAndAreScaled`（3000×2000 原图不在调用线程解码，结果缩到 1280 宽） | A 已修；真实机器与真实缓存量下的体感留 H-001 |
| F-061 / P2 | 截图里一条实况动态在 TRACE 显示渐变占位“配图 1 · 点击查看原图”，官方显示竖图与“实况”（用户反馈 ⑥） | 未能用真实数据确认。已知事实：用户本机已缓存的那条实况动态，静帧是 1080×1440 的 PNG（约 2.3 MB），清单完整；截图里的那条没有缓存，走在线缩略图。在线缩略图只要失败一次（网络抖动、读取超时）就被永久记成“没有图”，整个会话都显示占位；另外 TRACE 原先不解析 `live_url`，不知道这是实况照片 | 在线缩略图失败后隔 20 秒、40 秒再试，最多 3 次（`imageRetry`），之后不再重试，避免每帧请求；解析动态 `opus.pics[].live_url` 与 `draw.items[].live_url` 为 `DynamicMedia.LiveURL`，帖子图片左上角标“实况”；缓存时把动态片段存为 `images/NN_live.mp4`（角色 `motion`，静帧的 `motion` 字段指向它，片段下载失败记为部分缓存），看图层对已缓存的实况照片多一个“播放”按钮。占位改为浅灰底块，不再写“点击查看原图”。`TestUnavailableThumbnailIsNotRetriedEveryFrame` 追加“暂停后重试、最多 3 次”；`TestDynamicsTypesAndGracefulDegradation` 追加 `live_url` 解析；新增 `TestDynamicLivePhotoKeepsItsMotionClip`（静帧与片段都保存且带哈希，第二次运行不重复下载片段） | A 已修可能原因；真实实况动态的显示与片段下载留 H-001 第 5、6 步，根因仍是推断 |

### 16.2 改动（DEC-040）

- **共用缓存规则**：`internal/subscription/rules.go` 新增 `CacheRule`、`ListRules`、`SaveRule`、`DeleteRule`、`SetSubscriptionRule`；`subscriptions.json` 增加 `cacheRules`，订阅增加 `ruleId`。订阅的 `rules` 仍是规则落到订阅上的结果，检查、自动缓存与录制的读取路径不变。旧文件读入时补上“默认”规则，每个订阅归到与自己设置完全相同的规则，没有就以摘要命名新建，`since` 不变（`TestExistingSubscriptionsMigrateToSharedRulesUnchanged`）；改规则作用到所有使用它的订阅、自动缓存一直开着时不重置 `since`、删除规则回到“默认”、“默认”不能删（`TestSharedRuleEditsApplyToAllItsSubscriptions`）。服务层 `ListCacheRules`、`SaveCacheRule`、`DeleteCacheRule`、`SetSubscriptionRule`；界面在设置新增“缓存规则”分类（`internal/ui/rules_view.go`），订阅详情头部是“规则：<名称> ▾”弹出菜单（`TestCacheRulesAreSharedAndChosenPerSubscription`）。
- **订阅详情**：只留“直播”（默认）与“账号”标签；“检查更新”进“⋯”菜单；删除 `renderSubscriptionRulesTab`、`renderSubscriptionUpdatesTab`、`renderSubscriptionCachedTab` 与 `updates.go`。
- **设置页**：分类内容不套白卡和灰行，分组以细线分隔，标题 15px；侧栏改用和其他上下文侧栏相同的 14px 条目。
- **主页**：去掉位置恢复（锚点、行内偏移、启动恢复）和“↑ N 条更新的内容”；`markHomeSeen` 只在可见内容比已记下的更新时保存“看过的最新一条”，`loadHomeBaseline` 每次启动读一次作为“有更新”基准。新缓存的内容在回到列表顶部时载入。
- **看图与播放**：`mediaPanelRect` 计算居中面板（四周留 6% 边距、最大 1280×860），背后半透明遮罩，点面板外关闭；播放器全屏时仍铺满。视频在打开回调里直接 `Play()`（`TestPlayerFloatsOverTheAppAndAutoplays`，需要 Media Foundation）。
- **帖子排版**：一张图按记录或解码出的宽高比显示（竖图高 360px，横图最宽 480px，比例限制在 0.5～2.4），两张或四张为两列、其他为三列正方形格子；视频卡片为 200px 宽 16:9 封面在左、标题与简介（各最多两行）和清晰度在右；视频帖子的简介移进卡片，不再在上方重复（`TestPostImagesKeepTheirShapeAndVideosSitBesideTheirText`）。本机视频详情不再显示“互动数据未保存”。核对后确认缓存清单与动态快照本来就不含播放数、评论数等统计，统计只在在线视频详情里显示。

### 16.3 测试调整

- 删除 `TestSubscriptionCachedTabLoadsBeyondFirstPage`：订阅“已缓存”标签已按 DEC-040 移除，主页单订阅视图的分页由已有主页测试覆盖。
- 按新行为重写：主页“看最新”失败保持位置（`TestHomeLatestFailurePreservesAnchorAndVisiblePosition` 改为 `TestHomeLatestFailureKeepsVisiblePosition`，不再有锚点可保留）、从单订阅视图返回恢复滚动位置（比较滚动偏移与首条）、进度事件不打断阅读且重启从最新处开始（`TestFeedProgressEventsAndRestartPreserveViewportAnchor` 改为 `TestFeedProgressEventsKeepPositionAndRestartOpensAtNewest`）、保存失败可见（`TestHomePersistenceFailuresAreVisibleAndKeepOldState` 改由“看过的最新一条”触发写入失败）、R9 头像行用会话基准；订阅详情断言改为“直播”“账号”与规则按钮存在、旧标签与“检查该订阅更新”不存在，“⋯”菜单含“检查更新”；`TestDetailListsFillTheColumnAndScrollTheirHeader` 去掉订阅“已缓存”部分；本机视频详情改为断言不显示“播放 0”“评论 0”。截图场景 `subscription-cached` 换成 `settings-rules`（场景数仍 23），主页两个场景在写入上次会话的基准后调用 `RestartHomeSession` 模拟重启。
- 原有断言的意图没有弱化；删除和重写的都是被用户要求移除或改变的行为。

### 16.4 目视核对

A 看过：主页 1280 与 720（三图为正方形格子、视频封面在左、标题在右，720 头像行在页面里）、本机动态详情 1280、设置“缓存规则”1000、设置“存储”1000（无嵌套框）、订阅详情 1000（规则按钮、两个标签）、看图面板 1280（四周露出变暗的应用）。没有发现裁切或重叠。

<a id="feedback-5-validation"></a>
### 16.5 验证

| 命令与证据 | 实际结果 | 范围与限制 |
| --- | --- | --- |
| `go test -count=1 ./internal/ui ./internal/download ./internal/subscription ./internal/site/bilibili ./internal/service ./internal/library`；`artifacts/review/test.log` | passed / 0，15.6s | 合成数据、静音、`ui.NewTester`、回环地址 |
| `go run ./tools/check`；`artifacts/review/check.log` | passed / 0，50.6s | 格式、vet、全部含测试包、INV-05、文档与空白 |
| `GOOS=linux CGO_ENABLED=0 go vet ./...`；`artifacts/review/linux-vet.log` | passed / 0 | 只证明 Linux 目标静态检查 |
| `go run ./tools/uishots -out artifacts/uishots`；`artifacts/review/uishots.log` | passed / 0，69 张/69 条断言 | 23 场景 × 1280/1000/720 |
| `go run ./tools/package`；`artifacts/review/package.log` | passed / 0，16.9s | 未安装；哈希见 §3 与 H-001 |
| `go run ./tools/smoke -pkg dist/trace-dev -out artifacts/smoke`；`artifacts/review/smoke.log` | passed / 0，3,428ms；托盘专用工作集 0.60 MiB，已提交私有内存 81.68 MiB | 隐藏窗口、单实例、路径隔离、0 WebView2 |

**not-run**：H-001 实机体验（真实缓存量下的滚动流畅度、真实实况动态的显示与片段下载、浮动播放面板与自动播放的体感）、真实站点、安装/卸载、`go test -race`（无 cgo 工具链）、真实 Ubuntu 运行。

### 16.6 结论与下一步

- **实现交付**：DEC-040 的七项改动与 F-060、F-061 已实现；实况图占位的根因是推断，未用真实数据复现。
- **技术审查（A）**：通过，问题都已直接修复，不需要交回 B。
- **产品验收**：未开始，由用户按 [H-001](../../USER-ACTIONS.md#h-001) 判断。
- **首选下一步**：用户用最新 `dist/trace-dev/` 按 H-001 体验（重点：第 7 步主页滚动与帖子排版、第 5 步看图面板与实况图、第 15、18 步共用缓存规则），并回复 [Q-011](../../USER-ACTIONS.md#q-011)。push/PR/合并仍按 [ACT-001](../../USER-ACTIONS.md#act-001) 等具体授权。
