# 2026-09-26 Agent A 规划与本地 Git 基线

执行者：Agent A（本轮主执行者）；日期：2026-09-26，Asia/Shanghai。任务为通用栈研究、站点表、长计划、A/B 沟通与文档保留机制、Git 初始化。未启动产品实现/Agent B；本轮资料核对和自审不冒充独立产品审核。

## 交付范围

- [通用技术比较](../research/2026-09-26-technology-stack.md)：Vue/四种组件路线、Rust/SQLite/通信凭证/搜索/媒体/测试选择，版本与维护/许可证据、真实兼容试验和替换条件。
- [Bilibili](../research/bilibili-capabilities.md)、[Douyin](../research/douyin-capabilities.md)：独立事实/限制表，身份、历史列表、详情、媒体/质量、认证、错误、过期/恢复与验证范围。
- [运行契约](../design/m1a-runtime-contracts.md)：进程、协议、权限/凭证、传输/播放、搜索、预算。
- [唯一计划](../delivery/m0-m1a-execution-plan.md)：P0～P9、C01～C35，M0 风险至 M1a 打包、自检修复、集中审查；[B prompt](../delivery/m0-m1a-agent-b-prompt.md)。
- [工作流](../dev-rules/development-workflow.md#handoff-protocol)、[模板](../templates/agent-handoff.md)、[文档保留](../governance/documentation-policy.md#project-records)：双方 prompt 必交、唯一计划/报告、版本和稳定事项、用户手动转交。
- 入口、有效决定/ADR、事项、状态/环境同步；本地 Git 与忽略/行尾配置。

## Git 事实

起始无 `.git`，原 README 与全部既有文件保留。参考另一项目的本地优先、同交付集中提交/审查方法，未继承其许可、远端、GitHub、hook 或 CI。

执行 `git init -b main`，仅本仓库配置 autocrlf=false、pull.ff=only、fetch.prune=true。保存原 README 字节，首提交暂时置空且只暂存该文件；之后恢复全文。

首提交 `b208ad6`，消息 `chore: initialize repository`。`git ls-tree -r HEAD` 仅 README，blob 为 `e69de29bb2d1d6434b8b29ae775ad8c2e48c5391`，`git cat-file -s HEAD:README.md` 为 **0**。第二提交保存本报告所在全部文档/研究/检查脚本及配置；完整 SHA 从 `git rev-parse HEAD` 获取，报告不自引用自身 SHA。最终核验提交计数/树和工作区；未推送或建立远端。

## 调研与实际试验

| 检查 | 实际结果 | 范围/局限 |
| --- | --- | --- |
| npm/crates 注册表和上游维护 | passed（取得部分版本/日期/许可/维护资料） | 不是完整供应链审计/最终 lock；研究页链接各快照 |
| Bilibili 固定源码阅读 | passed | MIT bili-sync 投稿/详情/WBI/媒体/凭证；其他常见仓库归档、旧路径404已记录；没有真实站点账号请求 |
| 抖音官方/社区资料阅读 | passed | 作者授权与观看会话不同、媒体证据不足、GPL社区只作资料；真实请求not-run |
| TS7.0.2 + vue-tsc3.3.11 | failed | 安装通过，typecheck报lib/tsc未导出；[原记录](../research/2026-09-26/vue-stack-probe.json)保留 |
| 同组合改TS6.0.3 | passed | [复验](../research/2026-09-26/vue-stack-probe-compatible.json)：typecheck、Vite生产构建、1个Vitest SSR用例；GBK打印异常改UTF-8后完整复跑 |
| 最小前端包体积 | 观察到警告 | JS522.96kB/gzip158.62kB，B需分包/实际测量，不伪称性能通过 |
| Windows FFmpeg具体资产查询 | blocked | GitHub API403限流后停止；版本/构建路线有资料，资产/摘要/许可未验，OSS-004 |
| 产品Rust/Tauri/原生UI/站点/媒体/恢复/包 | not-run | 无产品工程；研究SSR和文档检查不替代产品测试 |

本机观测：Git2.55.0.windows.5、Python3.12.14、Node24.19.0、Rust/Cargo1.98.1、已装pnpm11.24.0。计划选择项目级pnpm12.6.0，未跑工程安装，不改全局工具。隔离研究代码在临时目录运行，repo只存安全源文本/输出/锁摘要，不提交node_modules或样例产品工程。

## 文档检查与自审

运行 `python scripts/check_docs.py`，结果保存为[检查记录](2026-09-26-document-check-results.json)。检查器代码未改，沿用原框架已经验证的反例能力。另核对UTF-8、证据个人路径/敏感字段、暂存差异与 `git diff --cached --check`；外链事实依据已读资料，不由文档脚本联网证明。

自审重点：旧候选不覆盖Vue新决定；TS失败保留；版本与资产分开；作者OAuth不等于任意作者媒体；B站多P/音轨/混流及风控不靠单视频成功推导；长计划保留恢复/凭证门槛；双方prompt填实际版本；不存在的代码/测试写not-run。

A 规划交付可转B，产品阶段尚未验收。下一步与可复制文本在[交接入口](../delivery/m0-m1a-agent-b-prompt.md)；所有集成/上游/人工当前事项仍在[总表](../decisions/open-questions.md)。没有新增必须立即询问用户才可完成文档的决定。
