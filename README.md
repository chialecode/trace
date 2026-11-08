# TRACE

TRACE 是一个 Windows 桌面应用，用社交网络式的界面浏览并缓存站点内容。当前范围只有哔哩哔哩：登录后粘贴视频链接或用户主页（UID），解析出视频详情、用户资料、动态和投稿；把动态连同原图和视频以最高画质缓存到本机，在主页信息流和媒体库里浏览。

技术栈：全 Go 语言 + [MyGo](https://github.com/egoist/mygo) 原生界面（`github.com/egoist/mygo/ui`，Windows Direct3D 11 / DirectWrite 渲染，不依赖系统 WebView2）。选型依据见[技术选型研究](docs/research/2026-11-05-stack.md)。

## 状态

当前阶段在[当前计划](docs/delivery/stage-plan.md)里，已实现的能力见[交付状态](docs/delivery/status.md)。工程尚未建立时，命令以[工程规则](docs/dev-rules/engineering.md#commands)记录的实际情况为准。

## 入口

- 参与开发：先读 [AGENTS](AGENTS.md)，再看[文档索引](docs/README.md)。
- 需要你处理的事：[USER-ACTIONS](USER-ACTIONS.md)。
- 贡献方式：[CONTRIBUTING](CONTRIBUTING.md)；安全问题：[SECURITY](SECURITY.md)。

## 许可

项目自有源码和文档采用 [Apache-2.0](LICENSE)；第三方依赖保留各自的许可和通知。
