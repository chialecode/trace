# 通用开源候选初查

查询日期：2026-09-25。依据用户补充，范围为**前端等通用技术栈**，不选择抖音/哔哩哔哩下载器。规则见[开源优先](../dev-rules/open-source-first.md)，网站另见[适配规则](../dev-rules/site-adapters.md)。

原始查询保存在[快照](open-source-snapshot.json)，包含官方仓库 URL、归档状态、默认分支提交、仓库 latest release、少量近期 issue/PR 元数据及错误。未安装候选、未跑集成，亦未完成逐个 issue 响应和分发许可审计；不能据此定稿生产技术栈。

2026-09-26 后续：用户已指定 Vue，A 已完成[具体比较与选择](2026-09-26-technology-stack.md)。下文保留 09-25 初查的原始判断，不能用它重新提出已决定的框架问题；当前状态见后续研究及事项总表。

## 候选与 TRACE 边界

| 能力 | 候选/官方来源 | 本次证据与限制 | TRACE 后续要验证 |
| --- | --- | --- | --- |
| 桌面外壳 | [Tauri](https://github.com/tauri-apps/tauri) | 既有设计基线；未归档，09-25 默认分支有提交；latest 返回 `tauri-driver-v3.0.0-alpha.0`，不是可直接选的桌面稳定包 | 目标包稳定版本、独立后台、受控 IPC、Windows 打包/关闭/重连，OSS-002 |
| 前端框架 | [React](https://github.com/facebook/react)、[Vue](https://github.com/vuejs/core)、[Svelte](https://github.com/sveltejs/svelte) | 均未归档；所取提交分别为 09-22/09-18/09-25；仓库发行分别返回 v19.3.0/v3.5.43/svelte@5.57.1 | 对比真实列表、状态、表单、中文输入、长期维护与组件生态；不提前选定，OSS-001 |
| 构建 | [Vite](https://github.com/vitejs/vite) | 未归档，09-25 提交，仓库发行 v8.3.1 | 与所选框架/Tauri 兼容、离线资产、开发/打包差异，OSS-001 |
| 组件与样式 | [Radix Primitives](https://github.com/radix-ui/primitives)、[Tailwind CSS](https://github.com/tailwindlabs/tailwindcss) | 均未归档；Radix 提交为 07-31，release API 404，应查实际包发行；Tailwind 提交 09-25，发行 v4.3.3 | Radix 仅 React 候选；Vue/Svelte 选对应生态后另查证。Tailwind 是样式工具，不能代替组件可访问性与设计规范 |
| 异步运行 | [Tokio](https://github.com/tokio-rs/tokio) | 未归档，09-25 提交，发行 tokio-1.53.1 | 取消、超时、阻塞 SQLite/文件任务隔离、进程退出，OSS-003 |
| SQLite 访问 | [rusqlite](https://github.com/rusqlite/rusqlite)、[SQLx](https://github.com/launchbadge/sqlx)、[SQLite 事务](https://sqlite.org/lang_transaction.html) | 仓库元数据未归档；后续提交/发行查询遇 API 限流，未完成维护判断 | 单写事务、迁移、备份、FTS5 构建、锁与中断恢复；只选择一个主访问方案，OSS-003 |
| 媒体探测与封装 | [FFmpeg](https://github.com/FFmpeg/FFmpeg)、[官方法律说明](https://ffmpeg.org/legal.html) | 元数据未归档；本快照后续 API 限流；正式发行和许可应查官网及实际构建 | ffprobe/流拷贝、取消、分片、Windows 二进制来源/构建选项/通知义务，OSS-004 |
| 本地搜索 | [SQLite FTS5](https://sqlite.org/fts5.html)，必要时再比 [Tantivy](https://github.com/quickwit-oss/tantivy) | 原设计优先 SQLite 路径；Tantivy 仓库查询限流，未确认维护状态 | 中文一字/二字/符号正确性、体积和查询；有实测瓶颈才增加引擎，OSS-003 |
| 测试 | Rust 自带测试工具、[Vitest](https://github.com/vitest-dev/vitest)、[Playwright](https://github.com/microsoft/playwright) | 两个 JS 仓库查询限流，未确认维护状态 | 选择与栈匹配的工具；浏览器 E2E 不证明 Tauri/Windows IPC 正确，OSS-005 |

日期为 UTC 查询值的日期部分；快照保留精确时间。仓库默认分支提交只是一项信号，需要核对是否为有效维护以及相关 bug 的响应；不等于维护保障或推荐安装版本。API 返回许可标识可能不完整，不可替代实际分发审计。

## 本轮结论与下一步

有足够候选支撑“通用能力先评估活跃开源”的开发政策；尚无依据为 TRACE 宣称最终框架、组件、驱动或媒体构建已选定。A 在首轮实现计划前围绕实际页面和数据/生命周期需求补比较，B 根据具体方案接入验证，问题只在[OSS-001～005](../decisions/open-questions.md)维护当前状态。

本次候选池不保留下载器推荐。站点方案不能从通用引擎研究直接推导，尤其不能把工具的基本下载能力等同于 TRACE 的完整收录规则。
