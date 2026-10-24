# FFmpeg/ffprobe 集成与分发路线（OSS-004 补查）

调研：2026-10-01（Asia/Shanghai）。角色：Agent A。延续[技术栈 §6](2026-09-26-technology-stack.md#6-媒体分发与可替换边界)与 [ADR-009](../decisions/architecture-decisions.md)；当前状态只在[事项总表 OSS-004](../decisions/open-questions.md#开源集中处理)维护。资料快照：[ffmpeg-assets.json](2026-10-01/ffmpeg-assets.json)。本文是 A 的选择与资料判断，**未下载、未解压、未运行**任何资产，不构成集成或许可验收。

## 1. 结论

TRACE 只需要两类媒体能力：ffprobe 读取容器/流规格，ffmpeg 以 `-c copy` 做流拷贝封装；不需要任何编码器。因此维持 09-26 的路线并补足具体做法：

- **资产**：BtbN `win64-lgpl-shared` 9.0 发布分支构建，锁定**月末** autobuild tag + SHA-256，不使用会被每日覆盖的 `latest`。
- **获取**：项目锁文件 + 下载校验脚本放到被忽略的本地目录；开发机与发行包使用同一份资产，换机一条命令恢复。
- **查找**：daemon 按显式配置 → 程序同级 `media/` → 开发期项目目录查找；系统 PATH 只在 profile 显式允许时使用，且记录并校验版本。不得默默从 PATH 选择未知版本（沿用 09-26 规则）。
- **分发**：daemon（不是 Tauri 前端）调用外部进程，因此直接放入便携包 `media/` 目录，不用 Tauri sidecar。是否随包分发仍受 Q-004 约束。

## 2. 方式比较

| 方式 | 收益 | 代价/风险 | 判断 |
| --- | --- | --- | --- |
| winget `Gyan.FFmpeg`（旧开发机 9.0.1-full_build） | 一条命令 | GPL 完整构建，含 TRACE 不用的 x264/x265 等；版本随 winget 升级漂移；经 PATH 发现 | 仅可作临时开发环境，不作产品基线 |
| winget `BtbN.FFmpeg.LGPL.Shared.9.0` | 与推荐资产同源同许可 | 仍经 PATH；当前为 9.0.1-20260831，独立于仓库升级；shared 经 winget 链接时 DLL 解析未实测 | 过渡用 |
| **项目锁定资产 + 校验脚本** | 版本/来源/摘要/许可可追溯；开发与发行一致；可复现 | 需新增锁文件、获取脚本、查找逻辑与打包步骤 | **采用** |
| 自编译精简 LGPL 构建（仅所需 demuxer/muxer/parser/bsf） | 体积可能降到个位数 MiB，许可最清楚 | 需维护 Windows 交叉编译流水线 | 后续优化候选，触发条件为包体或许可审查需要 |
| Rust 直接链接 libav（ffmpeg-next、rsmpeg 等） | 无外部进程 | ABI/构建/分发复杂，ADR-009 已选外部可替换后端 | 不采用 |
| 纯 Rust 容器解析替代 ffprobe | 无外部依赖 | 主要覆盖 MP4，FLV 分段与异常样本不足；仍需另有封装方案 | 不采用 |
| 程序首次运行按需下载 | 包体小 | 产品内下载可执行文件，安全/网络要求高；依赖 Q-004 分发方式 | 待 Q-004 后再评估 |

## 3. 资产与保留策略

BtbN README 写明：每日 12:00 UTC 构建并自动发布；**每月最后一个构建保留两年，最近 14 个每日构建保留**。`lgpl` 不含仅 GPL 的库（主要是 libx264/libx265），`lgpl-shared` 以 libav* 共享库代替静态可执行文件。构建脚本仓库为 MIT，未归档，2026-10-01 仍有推送。

| 候选 | 资产 | 大小 | SHA-256 |
| --- | --- | --- | --- |
| 首选：`autobuild-2026-09-30-13-08` | `ffmpeg-n9.0.2-17-g2a571b6068-win64-lgpl-shared-9.0.zip` | 73.4 MiB | `7157177b8a6cb2174c1650ba8c71b363f2c78cba5330f88c4c02cf5b2b880646` |
| 回退：`autobuild-2026-08-31-13-27` | `ffmpeg-n9.0.1-11-ge47273f4d9-win64-lgpl-shared-9.0.zip` | 67,201,333 B | `83a824f0729a69d143c9865125bb86988a11dd388325f0033711045522068aa0` |

首选是 9.0 发布分支在 `n9.0.2` 之后 17 个提交的快照，不等同于官方 9.0.2 源码包；台账须写实际 git 描述符。两年保留不是永久保留：建立远端后应自存一份同摘要副本，或在到期前按同一流程重新锁定。

## 4. 集成要求（交 B 实现与实测）

1. **锁文件**：记录 tag、URL、SHA-256、git 描述符、变体（lgpl-shared）与预期许可；不写浮动地址。
2. **获取脚本**：下载到 Git 忽略目录，先校验 SHA-256 再解压；记录 `ffmpeg -version`、`ffprobe -version`、`-buildconf`，拒绝含 `--enable-gpl` 或 `--enable-nonfree` 的构建；保存包内 LICENSE 与源码对应信息。
3. **查找与诊断**：显式目录 → 程序同级 `media/` → 开发期目录；PATH 仅显式允许；ffmpeg 与 ffprobe 两者版本都读取并须一致；`doctor` 报告来源、版本、许可变体和缺失原因。
4. **调用方式**：ffprobe 与 ffmpeg 一样经异步进程、超时、取消与无窗口创建标志调用，不在异步任务中同步阻塞。
5. **打包**：`package_windows.py` 复制已校验资产、许可文本与源码信息到 `media/`，写入 manifest 与[许可台账](../delivery/dependency-licenses.md)；包体变化如实记录。
6. **固定样本**：合成 AVC/AAC MP4、DASH 音视频分轨、无声视频、FLV 多段，验证 probe 规格/时长/轨道与流拷贝结果。

## 5. 未验证边界

资产内容、`-buildconf`、lgpl 变体所含第三方库的具体许可、DLL 体积、TRACE 固定样本回归均 `not-run`。发行时的 LGPL 义务（许可文本、对应源码获取方式、可替换共享库）以实际资产与 Q-004 决定的分发方式核定；进程隔离不免除二进制分发义务。

## 6. B 本地集成实测（2026-10-07）

第 5 节是 A 研究时的未运行状态，保留不改。B 已下载首选月末资产，核对 SHA-256 `7157177b8a6cb2174c1650ba8c71b363f2c78cba5330f88c4c02cf5b2b880646`，实际版本为 `n9.0.2-17-g2a571b6068-20260930`，比研究描述多日期后缀。根 `media-backend.lock.json` 与 `scripts/fetch_media.py` 为项目锁定与可复现获取正本。

实测 `-buildconf` 含 --enable-version3、--enable-shared、--disable-static、libopenh264，无 --enable-gpl/--enable-nonfree；`-L` 明确 **LGPL-3.0-or-later**，包内 LICENSE 是 LGPL version 3。曾误写 LGPL2.1，已在本轮修正并加脚本断言。OpenH264、第三方库和整体分发义务不能只靠“lgpl-shared”名字推断；完整公开分发许可核验仍归 Q-004。

固定 AVC/AAC、无声视频、分轨流拷贝后的轨道/时长、两个 FLV 段 probe 用例通过。daemon 实现显式查找、receipt/摘要/版本核验、doctor 来源/变体/原因、异步超时取消和有界输出；本地便携包保存 DLL、LICENSE.txt、SOURCE.txt、verified.json 和全文件摘要。包与门禁事实见[交付报告](https://github.com/chialecode/trace/blob/c1552e048ea608ba64deae820d127bbd5e969083/docs/delivery/stage-report.md)。本地验证不构成对外分发批准。
