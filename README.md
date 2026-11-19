# TRACE

TRACE 是一个 Windows 桌面应用（并附带面向 Linux VPS 的无界面远程服务端 `trace-server`），用社交网络式的界面订阅、浏览、缓存并录制站点内容。当前支持哔哩哔哩与抖音：按站点登录，粘贴链接或通过“我的”浏览视频、动态、图文与用户主页；按最高画质与音质缓存到本机，支持订阅检查更新、共用缓存规则、慢速全部缓存队列、不限路数的直播录制与页内实时预览，在主页信息流和纯离线媒体库里浏览；远程版只缓存启动后的新内容与直播并经 `rclone` 上传网盘，支持资源库增量导入导出与订阅同步。

技术栈：全 Go 语言 + [MyGo](https://github.com/egoist/mygo) 原生界面（`github.com/egoist/mygo/ui`，Windows Direct3D 11 / DirectWrite 渲染，不依赖系统 WebView2）。选型依据见[技术选型研究](docs/research/2026-11-05-stack.md)。

## 状态

当前阶段在[当前计划](docs/delivery/stage-plan.md)里，已实现的能力见[交付状态](docs/delivery/status.md)，构建、门禁、打包与冒烟命令见[工程规则](docs/dev-rules/engineering.md#commands)。

## 入口

- 参与开发：先读 [AGENTS](AGENTS.md)，再看[文档索引](docs/README.md)。
- 需要你处理的事：[USER-ACTIONS](USER-ACTIONS.md)。
- 贡献方式：[CONTRIBUTING](CONTRIBUTING.md)；安全问题：[SECURITY](SECURITY.md)。

## 许可

项目自有源码和文档采用 [Apache-2.0](LICENSE)；第三方依赖保留各自的许可和通知。
