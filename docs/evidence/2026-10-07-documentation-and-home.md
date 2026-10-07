# 文档规范与蓝白主页交付

日期：2026-10-07；执行者 Agent A；本轮为实现与自审，无子 Agent，不冒充独立 B/A 复核。

## 范围与版本

- 起点 `447825303f7c2cebd9a1bd1df0a0ab67d25bc46e`，开工工作区干净，无远端。
- 本轮分支 `codex/documentation-and-home`；交付 SHA 由包含本报告的提交解析，最终聊天提供完整 SHA，避免自引用。
- 用户授权补齐文档、沟通和本地参考/未跟踪文件规则、创建仓库外 README/PROPOSALS、按原图复刻界面并由 A 制定后续计划；补充要求把用户事项放 USER-ACTIONS。
- 产品核心 Rust 未修改；旧审查 F-002～F-014 未关闭。交付是文档与 UI 增量，M0/M1a 保持 rework-required。

## 实际交付

| 内容 | 位置/结果 | 边界 |
| --- | --- | --- |
| 文档治理 | currentPlan 唯一、用户与开发事项分工、历史计划标 superseded、根入口过期事实修正 | 保留需求/AC/INV、旧 C/F 和历史失败 |
| 本地参考 | 新规则规定向上发现、只读索引、建议流程、文件生命周期、GitHub 日期比较 | 六个外部项目的路径/许可/HEAD/更新时间/重叠功能顺序只在本机索引；未修改或拉取外部仓库 |
| 用户沟通 | USER-ACTIONS 收集 Q-002/Q-004、ACT-002/003/004；总表只链接 | 没有当前阻塞 B 开发的用户事项 |
| 界面 | App 导航、Home 时间线/详情、共享图标/插画、CSS Token、预览 store、原 Home 移至 Status | 原有七管理入口保留；录制/探索明确待开放，浏览器使用示例 |
| 数据/操作 | 类型/平台/关键词/标签/排序/详情联动；真实档案首50条、正文、保存/质量、空态/断连重试 | 创作者/订阅/真实封面/全局统计/媒体接线仍按 U01～U06；不使用旧任意路径打开接口 |
| 图像隔离 | 可选裁图脚本保留原图；Vite 回环服务器固定六文件白名单，拒绝越界链接 | 用户图片在仓库外，生产产物无图片；缺图 CSS 占位 |
| 公开门禁 | check_publication 扫当前公开文件与索引；check_docs 验证唯一计划；隔离反例 | 启发式检测，不能代替图片/法律/语义审阅 |
| 后续计划 | TRACE-CLOSEOUT-02 r1，R0～R6/U01～U06，完整 B prompt | 恢复 P0 优先；全批核心修复、媒体和真实主页、包/证据、自检后集中 A 复核 |

## 验证

环境：Windows，Node 24.19.0、pnpm 12.6.0、Python 3.12；前端沿用现有锁文件，无新增依赖。Pillow 仅用于仓库外裁图。

| 命令/检查 | 结果 | 范围/限制 |
| --- | --- | --- |
| `corepack pnpm -C apps/desktop run verify` | passed | vue-tsc + Vitest 20/20 + Vite 生产构建；含预览不调用 API、筛选联动、真实空/错误/重试、真实状态、路由与迟到请求不覆盖新搜索 |
| `python scripts/check_docs.py` | passed | 53 文档、82 需求、25 AC、12 INV，0 错误；currentPlan 一致 |
| `python scripts/check_publication.py` | passed | 当前文件与索引均 0 findings；不回显正文 |
| `python -m unittest discover -s scripts/tests` | passed | 5 项隔离反例通过；坏链接、锚点/计划、需求、暂存私密路径、误跟踪均能拒绝 |
| 本地浏览器 | passed（所列范围） | 1536×1024 三栏；800×900、420×900 重排；搜索/图片筛选/详情同步，Enter 键选择卡片并切换详情通过；800px 内容区 scrollWidth=clientWidth=581，筛选行内部滚动；控制台无 warn/error |
| Rust 全量/严格 clippy/Windows 新包 | not-run | 本轮未改 Rust 或打包；不能沿用旧结果声称缺陷已解决 |
| 真实站点/原生 IME/产品验收 | not-run | 用户事项中列明，先完成核心返工 |

失败/修正保留：公开检查首跑误把检查器自身正则语法识别为个人路径，已收紧 Unix 用户名模式；反例仍拒绝实际个人路径。初次临时准备发现系统 Python 无 Pillow，改用已有文档工具运行环境，仅生成本地图片，不安装项目依赖。Vite 提醒本地插件 import 扩展名，已补 `.ts`。800px 初次可见类型行导致内容区横向滚动，已调整为筛选行内部滚动；复验上述视口通过。

## 整理与保留

未删除用户文件、历史报告、未结审查或旧提交。原图保留并在仓库外复制；六裁切和含图截图留本机。忽略的 `.local/` 保存本轮可重复文档更新脚本，`apps/desktop/dist/` 为可再生成构建；均不进入提交。需求、契约、历史失败与有效决定继续登记，旧计划标明替代而非删除。

本轮 A 自审完成后交 B 依据[当前计划](../delivery/stage-plan.md#agent-b-prompt)执行；完整可复制 A→B prompt 的唯一保存位置是该节，最终回复同时贴给用户。双方后续必须提供实际版本和 B→A prompt。用户步骤见 [USER-ACTIONS](../../USER-ACTIONS.md)，不在报告重复维护。


## 最终自审补充

- `python scripts/verify.py --suite desktop` passed（类型、20 项 Vitest、生产构建）；`git diff --check` passed。
- 生产输出图像文件为 0；白名单 portrait 请求为 image/webp，未知 README 请求只得到 Vite 的 HTML 页面，不泄露本地文档。
- 本机截图：reference 索引的 evidence/home-desktop.jpg 与 home-narrow.jpg；包含参考作品画面，不纳入 Git。
- 文档/前端源码与报告同次交付，测试基点和实际修改可由本次提交 diff 定位。Rust、原生媒体、真实服务仍 not-run；不得据此关闭原 F。
