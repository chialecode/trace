# 教训

开发中踩过、值得后来者避开的坑。每条：**规则** → 原因 → 证据（PR 号或日期）→ 出现次数。出现 3 次的提升进[开发规则](docs/dev-rules/workflow.md#lessons)或 [AGENTS](AGENTS.md)，并从这里删除。对其他项目也适用的教训，去那些仓库开 issue 分享。

## 本机环境

- **从 Agent 命令里启动常驻程序要先脱离作业对象。** Agent 的命令沙箱结束时会结束作业里的全部进程，托盘常驻的开发版会被一起关掉；先经 `explorer.exe` 的 `Shell.Application.ShellExecute` 启动，不行再 `CREATE_BREAKAWAY_FROM_JOB`（已写进[开发版自动安装](docs/dev-rules/engineering.md#dev-install)）。证据：2026-11-22。出现 1 次。
- **在 Windows 上用 Python 或脚本改文本文件要保留原换行符。** Python 的 `Path.write_text` 会把 `\n` 写成 `\r\n`，整个文件被改成 CRLF，文档检查与源码匹配型测试随之失败。读写用字节或 `newline=""`。证据：2026-11-24 另一个项目。出现 1 次。
- **在 `git worktree` 工作树里先单独跑完 `go run ./tools/check`，推送时用 `git push --no-verify` 并在 PR 写明原因。** `git push` 执行 `pre-push` 钩子时会注入当前工作树的 `GIT_DIR` 环境变量，`tools/release` 测试里的 `git init`、`git commit` 与 `git tag` 继承该变量后会直接改写工作树自身的分支引用与标签，导致 `HEAD` 被测试提交覆盖并报 `tag 'v0.1.1' already exists`。证据：2026-11-25（含 #56）。出现 2 次。
- **Windows 隔离沙箱桌面下不要把 `golang.org/x/sys/windows.EnumWindows` 的 `(r1=0, GetLastError=0)` 当成错误，也不要仅靠 `EnumWindows` 跨桌面判断窗口是否打开。** Agent 沙箱命令运行在零顶层窗口的隔离桌面上，`EnumWindows` 返回 `FALSE` 且 `GetLastError()==0`，`x/sys/windows` 会将其包装成 `syscall.EINVAL`（`invalid argument`），且隔离桌面看不到用户交互桌面上的窗口；应用自身在数据目录写入 `runtime-window.json`（按运行中 PID 核对），再以直接检查 `e1` 的 `EnumWindows` 兜底。证据：#84（2026-11-25）。出现 1 次。
- **在 Windows 上用 MyGo `v0.10.0` 构建 Android 时须将 `JAVA_HOME` 设为 JDK 17、在 `.mygo/android/` 补放 `gradlew.bat`，并在有代理时经 `GRADLE_OPTS` 与参数转发 `-Dhttp(s).proxyHost/Port`。** MyGo `v0.10.0` 的 `internal/mobile/android/embed.go` 遗漏了 `gradlew.bat` 导致 Windows 报找不到文件；固定 Gradle `8.14.3`（内置 Kotlin DSL `2.0.21`）与 `jvmToolchain(17)` 遇 Android Studio JBR `25.0.4` 会抛 `IllegalArgumentException: 25.0.4`；Java/Gradle 不读取 `HTTP(S)_PROXY` 环境变量会在 10 秒后超时。证据：#117（2026-11-26）。出现 2 次。

## 解析与站点

- **把外部 JSON 里的数字转成 `int` 前先夹住范围。** 站点返回的计数可能超过 32 位，`int(parseJSONInt64(x))` 在 32 位平台会溢出，CodeQL 也会报“不正确的整数转换”；统一用夹在 `MinInt32`～`MaxInt32` 的 `parseJSONInt`。证据：#17。出现 1 次。
- **真实站点检查在步骤里遇到本机异常也要写出脱敏回执。** 一次检查的账本只有 1 次运行，程序在中途因本机缺陷崩溃却没写回执，等于白白用掉额度、还要另开编号重新授权；每一步都要有异常恢复，崩溃前先落盘已得结论。证据：ACT-022（2026-11-21）。出现 1 次。
- **脱敏扫描复合格式 Cookie 时要同时登记拆分后的子令牌，并跳过布尔/空值字面量。** 部分站点 Cookie 形如 `1.0.0-<token>` 或 `<token>|<timestamp>|...`，日志或异常若只带出截取后的 `<token>` 会漏过整串匹配；而 `false`、`true` 等字面量直接送入全文秘密扫描会与回执 JSON 布尔值相撞导致拒绝落盘。证据：#52（ACT-026，2026-11-24）。出现 1 次。
- **多同类整数字面量参数的签名/协议函数须用命名结构体或独立槽位边界测试逐项校验，不能让单测复用同一调用顺序做自循环验证。** `pickCanaryByte(digest, start, sentinel, fallback)` 三处调用把 `start` 与 `sentinel` 全部写反且自检测试复制了同样参数，宽容接口未拦截这 3 字节掩盖了缺陷，导致高敏接口 `aweme/favorite` 持续静默返回 0 字节直至单变量对照实测才定位。证据：#88（ACT-028，2026-11-25）。出现 1 次。

## CI

- **测试里不要写死会过期的日期。** 固定的 Cookie 过期时间、证书有效期或“未来”日期到期后测试会突然失败；用相对当前时间计算。证据：2026-11-24 另一个项目的 CI。出现 1 次。

## 直播与存储

- **不要把应用退出超时（30 秒）复用到正常收尾或手动停止，也不要在持库全局锁期间哈希大文件。** 大段（GB 级）MP4 重封装常超过 30 秒，固定墙钟超时会直接中断转换并误报 `convert_failed`；在 `Store.SaveItem` 持锁重算大文件 SHA-256 还会阻塞所有并发录制的 checkpoint 写入与界面读写。改为仅对应用关闭钩子施加 30 秒截止时间，正常收尾改用无进展看门狗与单趟流式哈希，并将文件哈希挪出库锁、直播清单写入使用跳过重算哈希的 `SaveLiveItem`。证据：#69。出现 1 次。
- **直播 HLS 轮询循环不能重复拉取不变的 `EXT-X-MAP`、无视轮询耗时固定整秒休眠，或让单个过期 `.m4s` 404 立刻触发 6 秒整路重连。** 上游滑动窗口通常仅 3～6 秒，叠加耗时休眠、每片 `fsync` 与全量重连会把偶发抖动放大成连片丢段。改为扣除本轮耗时计算休眠、节流落盘、同 URI 初始化段每段只拉一次、对单个过期 `.m4s` 404 先跳过并采用渐进重连退避（500ms 起步）。证据：#69。出现 1 次。
- **往 `trace.log` 写直播诊断日志时不要输出带查询串的完整 URL 或命中脱敏键名的字段名。** `sanitizeLogLine` 命中 `https?://` 或 `sid`/`token`/`expires`/`sign`/`kid`/`sub` 等键名会把整行替换成 `[REDACTED_LOG_LINE]`，导致故障排查线索全部丢失；只记录安全键名与纯主机名（`host=`），并对错误信息剥离 URL。证据：#69。出现 1 次。
- **跨平台文件锁与并发队列断路器测试不要依赖隐式时序或裸阻塞通道。** Linux 下 `os.Open` 不会像 Windows 那样阻止同目录 `os.Rename`，平台相关的共享锁断言应标 `runtime.GOOS == "windows"` 并另配确定性故障注入覆盖非 Windows 回滚路径；`httptest.Server` 里用通道阻塞并发工作槽的测试必须同时 `select` `<-r.Context().Done()` 并在 `t.Cleanup`（于 `srv.Close` 之前）关闭所有门控通道，且验证“运行中任务触发 412/429 连带暂停排队任务”时要先等运行中请求进入门控再入队后续任务，否则快机器上首个任务先跑完释放工作槽会导致断言失败并卡死 `srv.Close` 10 分钟超时。证据：#77。出现 1 次。

- **直播 HLS 不要在首个媒体分片校验通过前就把 `EXT-X-MAP` 初始化头写入 `.part` 磁盘，也不要在已正常产出媒体后遇 1 小时签名过期（HTTP 403）时把健康主 CDN 节点记入失败冷却表。** 切新段时先落盘 `ftyp+moov` 会在首片 `404` 或残片失败时留下 0 样本初始化文件并触发永久 `convert_failed`；而把 1 小时 `403` 误当节点故障拉黑主节点会迫使重连落到 `gotcha` 调度节点，若未放行公网 HTTPS:443 调度厂商域名或未做候选回退就会以 `schema_changed` 直接停录 2～3 分钟。证据：#156（2026-11-27）。出现 1 次。

## 桌面壳与界面

- **不要在持有 `Shell.mu`（或任何主线程非重入互斥锁）期间调用 `ui.NewApp`、`mygo.NewWindow`、`Show`/`SetWindowButtonsTheme` 等 Win32/MyGo 窗口 API，也不要在首次显示窗口前漏绑 `BindEventLoop`。** Windows 下 `ShowWindow` 会同步发送 `WM_GETOBJECT` 并经 `UiaReturnRawElementProvider` 进入 `combase.dll` COM STA 嵌套消息循环，在当前主线程同一协程上重入派发托盘点击或 `RunOnMain` 回调；若 `openOrRestoreMainWindow` 持锁建窗/显示，二次点击托盘图标或后台回调会立刻自死锁 `Shell.mu` 导致整个界面永久未响应，且在 `BindEventLoop` 前调用 `SetWindowButtonsTheme` 还会触发 `settleShow` 让首帧在 `dispatch == nil` 下同步解码磁盘图片。始终以 `Hidden: true` 建窗、无锁完成绑定与状态发布后再显示，并以 `openingWindow` 防止重入重复建窗。证据：#130（2026-11-26）。出现 1 次。
- **不要在循环中逐条广播批量 `EventLibraryChanged`/`EventCacheConflictsChanged` 事件，也不要在 `BindEventLoop` 中无合并地直派 `win.Update` 或在 UI 主线程同步执行重型 SQLite 全表扫描（`BackfillSummary`、`LibraryStatistics`、`CountCachedWorks`）。** MyGo `v0.10.0` 的 `mainLoop.drain()` 会同步循环排空全部队列回调后才返回 Win32 消息泵，后台历史补全一次突增数百条事件且每条都在主线程同步扫全表并反序列化 JSON 时，主线程会单核满载 100% 导致窗口完全卡死数分钟。批量更新须在源头合并为单条事件，`BindEventLoop` 须按语义合并并保持最多 1 个在途投递，重型本地统计/摘要/列表查询按可见页面懒刷新并在桌面分发模式下移入单飞异步协程执行。证据：#171（2026-11-28）。出现 1 次。
