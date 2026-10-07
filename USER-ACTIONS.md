# 需要用户处理的事项

更新：2026-10-07。本文是用户决定、配置、授权和人工检查的唯一当前状态。你可以在聊天回复编号与结果；不需要逐份阅读工程报告。开发缺陷由 Agent 继续处理，状态与职责见[工作流](docs/dev-rules/development-workflow.md#agent-user)。

**现在没有需要你立即处理、才能继续开发的事项。** A 已完成四项返工技术复审及新包 -4 核验，开发缺陷已处理。ACT-003 的隔离实机体验已具备执行条件，你方便时再做；未执行继续保持 not-run，不阻塞已授权的本次 Git 收尾。

## 现在可以做的

<a id="act-004"></a>
### ACT-004 · 查看蓝白主页体验（可选，waiting-human）

B 已补真实主页与媒体接线；此项只收集视觉偏好，不验证下载、直播或恢复。你已指定白色参考风格，不再要求重选主题。

1. 在仓库根执行 `corepack pnpm -C apps/desktop run dev`；若本轮已在运行，直接打开 [本地界面](http://127.0.0.1:5173/)。
2. 预期看到左侧导航、创作者横幅、时间线、右侧详情；顶部明确显示“界面预览 · 示例数据”。
3. 输入“新海诚”，应只看到匹配的示例；清除后点“图片”，应显示海边内容；点击卡片应更新详情。示例下载只解释预览用途，不创建任务。
4. 看文字大小、内容密度和三栏比例，回复“ACT-004：满意”或具体希望调整的位置。可附窗口宽度/缩放比例，不需要提供私人档案。
5. 关闭终端开发服务器不影响真实 daemon；本地预览不启动采集。无需本机参考图也可运行，缺图显示占位。

原生 Windows 输入法、窗口生命周期与真实媒体播放由 ACT-003 验收。此项不阻塞 B 实施、不等于产品验收。

<a id="q-002"></a>
### Q-002 · 使用规模和偏好（open，可顺延）

当前默认按[需求默认策略](docs/requirements.md#defaults)和合成数据实现，非阻塞。方便时告诉 Agent：大约订阅多少位创作者、希望首次回填几天、是否优先分辨率或帧率、档案大约多少条/占多大空间。不需要给目录、账号或作品清单。

Agent 根据你提供的范围补测资源和历史覆盖；未经测量不会声称十万条真实档案已经达标。负责人：A 整理、B 测量。

<a id="q-004"></a>
### Q-004 · 项目许可证和对外分发（open，可顺延）

TRACE 自身许可证和分发方式尚未确定。本次源码托管已获明确授权，仍不发布软件包、不自动添加项目许可证。将来正式授权源码复用或对外发包时，Agent 先给具体许可对比、依赖与媒体资产义务，再请你选择；你现在可只说明预期是个人自用、开源还是闭源分发。

本轮未复制 GPL 参考源码。此项只阻塞受许可影响的正式集成/对外分发，不阻塞文档、UI、修复和本地包验证。不得从参考仓库继承其许可或推送授权。

## 等 Agent 准备好再做

<a id="act-002"></a>
### ACT-002 · 真实站点受控验证（open，暂不执行）

A 技术复审已通过，真实站点仍需 B 准备经过验证的隔离启动入口、请求预算和采样方案；本轮未启动真实请求。当前无需你反复测试或提供 Cookie。

届时你只需确认少量自己有权访问的测试账号/视频和允许请求范围，在本机凭证入口配置登录条件。不要把 Cookie、令牌、签名地址或真实档案发进聊天、Git 或 reference。缺配置只使真实站点用例保持 not-run，合成源与自动故障测试继续。

关闭条件：在已授权范围完成受控实测，能力表记录真实结果、覆盖和未知；不能由离线夹具通过替代。

<a id="act-003"></a>
### ACT-003 · Windows 实机产品验收（waiting-human，可择时执行）

B 已提供隔离样本；A 已完成技术复审、包核对与独立合成启停。**以下实机步骤已准备好，可在方便时执行。** 当前原生体验仍 **not-run**，不能以自动门禁替代；代码缺陷归开发者处理。所有步骤只使用新合成档案，不需要真实 Cookie 或真实档案。

本地便携包：`dist/trace-0.1.0-windows-x64-closeout-4/`；SHA256SUMS.txt 自身摘要为 `7bcf580c7585bb711834e6abe9de0b7994381a5e6979b3a6e1fc3d56b53f097b`；总大小 199,172,866 字节。包内 `SHA256SUMS.txt` 覆盖每个文件（清单自身除外），manifest 记录构建时基点/dirty、113 个输入摘要与总摘要及 doctor。旧无后缀 closeout 包保留作中途验证，不作为当前交付。

在仓库根 PowerShell 执行：

```powershell
# 已准备样本位于 artifacts/closeout/acceptance；想重建时必须选不存在的新目录。
$traceRepo = (Get-Location).Path
python scripts/fetch_media.py
cargo run -p trace-e2e --bin prepare_acceptance --locked -- artifacts/closeout/acceptance-new
$tracePackage = (Resolve-Path 'dist/trace-0.1.0-windows-x64-closeout-4').Path
$env:TRACE_PROFILE = (Resolve-Path 'artifacts/closeout/acceptance-new/profile.json').Path
& "$tracePackage/trace-daemon.exe" doctor --profile $env:TRACE_PROFILE
Start-Process -FilePath "$tracePackage/trace-desktop.exe" -WorkingDirectory $tracePackage
```

也可直接使用 B 已生成的 `artifacts/closeout/acceptance/profile.json`，将上述 TRACE_PROFILE 换到该文件；生成器拒绝复用已存在目录。合成样本包含 12 秒 AVC/AAC 测试图/音调、真实 PNG 封面、一个缺失媒体条目和完整备份，没有网络请求/登录凭证/真实订阅。临时浏览器布局截图不能当作这些原生行为的通过证据。

1. 默认页不应显示“界面预览”或样例平台数量；可选择“合成验收创作者/离线合成来源”。全档案 2 条，合成媒体有实际封面，缺失条目不能显示完整。搜索“合成”，检查中文输入法候选、键盘焦点、800px 窄窗及系统 125%/150% 缩放，文本和操作应可达。
2. 选中“合成媒体：12 秒 AVC/AAC 播放与跳播”，播放应有画面和音调；拖到中间和末尾，观察能否继续。选缺失媒体应解释未保存/缺失。点外部打开，应仅打开合成视频；不要将系统默认播放器不存在误记为下载失败。
3. 关闭 GUI 后执行 `& "$tracePackage/trace-daemon.exe" status --profile $env:TRACE_PROFILE`，应仍能返回后台实例；再开 GUI 能重新读到档案。纯合成静态样本没有正在下载的真实任务；下载中暂停/退出已由自动故障套件验证，真实下载续行由 ACT-002 的获准样本完成。
4. 存储页点“立即备份”，到任务页看到 backup succeeded 后刷新备份历史，应出现已完成备份；也可用合成目录的 acceptance.json 内 backup_id 定位 `backups/<backup_id>`。恢复选择该目录，目标填写新的空目录；在 PowerShell 用 `Join-Path $traceRepo 'artifacts/closeout/acceptance-restored'` 得到绝对路径后填入，避免工作目录不同。预期返回恢复 Profile、2 个已恢复对象、0 个缺失对象；缺失条目本来没有对象，仍显示未开始。
5. 关闭 GUI，先用下方命令完全退出原合成后台，再把 TRACE_PROFILE 指向恢复结果的 profile.json，重新启动桌面。应看到相同正文、封面、视频和缺失状态；恢复档案不会继承凭证或自动订阅。
6. 结束时关闭测试 GUI，再执行：

```powershell
& "$tracePackage/trace-daemon.exe" shutdown --profile $env:TRACE_PROFILE
Remove-Item Env:\TRACE_PROFILE
```

回复“ACT-003：通过”或具体步骤、预期/实际、窗口宽度/缩放与播放器环境；不要贴真实档案、Cookie 或私有路径。A/B 将代码失败带回集中修复，不要求你替开发者补自动测试。负责人：B 准备、A 复核、用户产品体验验收。

## 回复和授权边界

可以分次回复，例如“ACT-004：字号加大一点；Q-002：约 100 位，先回填 7 天”。Agent 将决定回写有效决定及对应正本，失败项改为开发修复，修好后再标待复测。

你已明确授权本次关联 chialecode/trace、将指定历史归并为 M0 并首次推送，以及落实适用分支保护。该授权不延伸到后续阶段；未经你明确允许，Agent 不得 push 或创建 PR，也不得自行合并/发布。以后需要这些动作时，先完成可审阅内容并在此列具体仓库、分支、提交范围和动作，再请求对应授权。未答复不等于批准，已有的相同授权不重复询问。产品验收与 Q-004 未因源码推送而关闭。
