# 订阅、弹幕与直播的选型

查询日期：2026-11-07（UTC+8）。只对查询时的版本有效；按[开源规则](../dev-rules/sources-and-safety.md#open-source)做最小接入和真实流程验证。需求见 DEC-027、DEC-032 与[需求 §2.2、§2.4、§2.5](../requirements.md#subscription-scope)，设计见[架构 §6](../design/architecture.md#frontend)、[数据 §2.9](../design/data-and-cache.md#danmaku-subtitles)、[订阅](../design/subscriptions.md)与[直播](../design/live.md)。

方法：`proxy.golang.org`（`goproxy.cn`）的 `@latest` 与版本列表、pkg.go.dev 的包与许可页、GitHub 仓库页与 README、本机 MyGo `v0.8.1` 模块源码、本机 Go 1.27.3 标准库。下文**资料**表示资料判断，**原型**表示要由 B 用原型验证。

<a id="danmaku-renderer"></a>
## 1. 原生界面弹幕排版与渲染（纯 Go）

改为 `mygo/ui` 原生界面（DEC-032）后，弹幕叠加层直接在视频画面的 `ui.Stack` / `ui.Canvas` 上由 Go 渲染，不再引入 JavaScript 弹幕库：

| 方案 | 依据 | 结论 |
| --- | --- | --- |
| **自研纯 Go 确定性弹幕排版器（`internal/player/danmaku.go`）+ `mygo/ui` 叠加层（选定）** | 标准库 `encoding/xml` 解析缓存的 `.danmaku.xml`（`<d p="time,mode,size,color,...">text</d>`）；对模式 1～3（从右向左滚动，持续期约 6 秒）、模式 4（底部居中，持续期 4 秒）、模式 5（顶部居中，持续期 4 秒），可将“任意播放时刻 `t` 的可见弹幕集合及其轨道 `(track, x, y)`”建模为基于时间窗口 `[t - maxDuration, t]` 的纯函数：按出现时间二分查找窗口起点，在固定高度轨道上做追及碰撞检测（滚动弹幕保证前一条尾部已离开右边界且后一条到达左边界前不会追上），密度按每秒上限（低 3 / 中 8 / 高 15 / 不限）在分配轨道前稳定过滤，透明度乘到文字与描边 Alpha | 约 200 行纯 Go 代码，零第三方依赖；因为任意时刻 `t` 的画面完全由 `[t - 6s, t]` 窗口决定，**暂停、恢复、任意拖动（Seek）和窗口尺寸变化天然不会产生 DOM 残留或状态漂移**，且可在 `go test` 与 `ui.NewTester` 中做毫秒级确定性断言 |

**原型**（计划 PR-3）：10,000 条弹幕样本下，二分查找 + 轨道分配单帧计算耗时（目标 < 1 ms）、拖动后立即切换到新时间窗无残留、窗口大小变化时轨道数自适应重算。

## 2. Go 端弹幕分段解码

[`google.golang.org/protobuf`](https://pkg.go.dev/google.golang.org/protobuf/encoding/protowire) v1.36.12（2026-08-10），BSD-3-Clause。**推荐**只用 `encoding/protowire` 手写解码外层 `DmSegMobileReply`（字段 1 `elems`）和 `DanmakuElem` 的字段（约 120 行），未知字段用 `ConsumeFieldValue` 跳过；不引入 `protoc` 与生成代码。字段号来自[协议资料 §18](bilibili.md#danmaku)，用合成字节夹具锁定。

<a id="recording"></a>
## 3. 直播录制（纯 Go，不带 ffmpeg）

| 候选 | 版本与日期 | 许可 | 结论 |
| --- | --- | --- | --- |
| [mp4ff](https://pkg.go.dev/github.com/Eyevinn/mp4ff@v0.62.0/mp4#Defragment)（已在依赖里） | v0.62.0，2026-11-04 | MIT | 已有 `mp4.Defragment`：把 fMP4 转成 `moov` 在前的普通 MP4，样本数据原样复制；也能写 fMP4 片段；没有 FLV 解析 |
| [mediacommon](https://github.com/bluenviron/mediacommon) | v2.9.7，2026-10-25 | MIT | 很活跃，有 fMP4、普通 MP4、MPEG-TS 与 H.264/H.265/AAC 工具，没有 FLV；与 mp4ff 功能重叠。只在以后需要 TS 时引入 |
| [gohlslib](https://github.com/bluenviron/gohlslib) | v2.4.7，2026-10-25 | MIT | 有 HLS 客户端与 `pkg/playlist` 解析器；TRACE 的下载要走自己的 HTTP 层（请求头、节奏），自写 m3u8 解析更轻量 |
| gomedia | 最后提交 2024-09 | MIT | 两年没有提交，不作依赖 |
| lal、joy4 | 2024-10、2020-05 | MIT | 完整流媒体服务器或已停滞，排除 |

**推荐路线**：录 HLS fMP4——轮询媒体播放列表（自写约 150 行），按序号把初始化分片与 `.m4s` 原样追加到 `.part`，结束后用 `mp4.Defragment` 生成 `moov` 在前的普通 MP4，与点播缓存的产物一致、在应用内（Media Foundation）和系统播放器都能打开并拖动；初始化分片变化、`EXT-X-DISCONTINUITY`、序号跳空时换新文件，不在一个文件里拼接。HTTP-FLV 后备放到 LOOP-004。

## 4. 字幕

- CC 字幕 JSON 转 WebVTT 与 SRT：自写（约 60 行），测试转义（`&`、`<`、`-->`）与毫秒取整。**资料**
- 应用内字幕渲染：原生播放器直接通过 `Library.MediaPath` 读取缓存的 `.vtt` 文件，解析出 `[]SubtitleCue{StartMs, EndMs, Text}`，随播放时间 `t` 在视频画面底部中央（控件栏上方）用半透明黑底白字渲染当前命中的字幕行。完全在 Go 进程内完成，不存在浏览器跨源 `<track>` 或注册表 MIME 覆盖问题。

## 5. 定时调度

| 候选 | 状态 | 结论 |
| --- | --- | --- |
| robfig/cron v3 | v3.0.1 在 2020-01，主干最后提交 2021-01，MIT | 已停维，不选 |
| [go-co-op/gocron v2](https://github.com/go-co-op/gocron) | v2.23.0，2026-10-26，MIT，活跃 | 需要 cron 表达式时再考虑 |
| 自写 | — | **推荐**：需求只是固定间隔、加倍退避、每轮上限、只在运行且已登录时进行。每分钟醒来一次、用墙钟比较下次到期时间（存 `updates.db`），不依赖一个长计时器（Windows 睡眠唤醒后长计时器可能推迟），注入时钟做测试 |

## 汇总

新增依赖只有 Go 侧 `google.golang.org/protobuf` v1.36.12（只用 `encoding/protowire`）与 `github.com/yeqown/go-qrcode/v2` v2.3.0（登录二维码渲染）；弹幕 XML 生成与排版、字幕转换与叠加、m3u8 轮询、调度器全部用纯 Go 实现，都不涉及 GPL/AGPL。
