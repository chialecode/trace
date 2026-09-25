# M0/M1a 通用技术栈比较与选择

调研/复核：2026-09-26（Asia/Shanghai）。角色：Agent A。依据 DEC-002/008/009；选择已进入 [ADR 台账](../decisions/architecture-decisions.md)，执行见[唯一计划](../delivery/m0-m1a-execution-plan.md)。本文记录 A 的选择理由与版本候选；B 创建锁文件后，实际安装版本以锁文件和交付证据为准。

## 1. 结论与证据等级

采用 Vue 3 + TypeScript + Vite + Naive UI + Pinia + Vue Router，桌面沿用 Tauri 2，独立 Rust daemon 使用 Tokio、rusqlite、SQLite 与可替换 FFmpeg/ffprobe。测试分为 Rust 故障/契约测试、Vitest 组件测试和 Windows 原生 WebdriverIO。M1a 不引入常驻 Node/Python 服务、云数据库或单独搜索服务。

“注册表版本/许可”“维护活动”“官方文档描述”“临时兼容实测”“产品集成”是不同证据。此次只有前端最小组合进行了安装、类型检查、生产构建与 SSR 渲染测试；完整 Tauri、Rust 集成、媒体、原生测试、IME 和真实站点均 `not-run`。不得从下载量、stars、单个 PR 关闭或仓库的 latest release 推断兼容性。

资料保存为：[版本查询](2026-09-26/package-versions.json)、[维护样本](2026-09-26/maintenance.json)、[源码摘录及哈希](2026-09-26/source-inspection.json)、[补查版本](2026-09-26/toolchain-followup.json)、[原生测试官方说明](2026-09-26/native-test-docs.json)、[媒体/插件补查](2026-09-26/native-media-followup.json)。这是查询快照，不是生产锁文件。UTC 查询时间落在 09-25 的记录，对应本地 09-26。

## 2. Vue 界面方案

Vue 已由用户指定，不再让 B 重选 React/Svelte。采用 Composition API、SFC、严格 TypeScript 与 Vite SPA；不需要服务端渲染。[Vue 官方起步](https://vuejs.org/guide/quick-start.html)使用 Vite，并列出 TypeScript、Router、Pinia、Vitest；当前要求 Node `^22.18.0 || >=24.12.0`。[Vite 文档](https://vite.dev/guide/)及注册表的 engine 仍需取交集。

| 组件方案 | 当前资料与维护证据 | 对 TRACE 的收益和代价 | 决定与替换条件 |
| --- | --- | --- | --- |
| Naive UI 2.45.3，MIT | 2026-08-27 发布；仓库未归档；有近期 issue/PR 处理，较新的关闭不都代表修复；[官方介绍](https://www.naiveui.com/en-US/os-theme/docs/introduction)提供完整 Vue/TS 组件及主题配置 | 表格、表单、弹窗、消息、中文 locale 和虚拟列表可直接支撑时间线/任务/账号页；无需另建 Sass/Tailwind 层；无样式原语级的完全自由度 | **采用**。仅封装领域状态、主题及少量复用交互；不另写基础弹窗。若实机焦点、IME 或资源问题有可复现且不可局部解决的缺陷，记录 OSS 后比较 Element Plus |
| Element Plus 2.14.6，MIT | 09-18 发布；09-25 有维护活动，当前样本主要是依赖更新，不能当业务缺陷修复证据；[用法](https://element-plus.org/en-US/guide/quickstart.html)支持按需导入 | 成熟管理界面组件，密集表单/表格适合；主题和自动导入配置需结合项目设计，仍要实测虚拟列表与中文输入 | 备用。与 Naive UI 不并装做同类基础组件；没有证据表明其不适用，只是此次 Naive 集成成本更小 |
| Reka UI 2.10.5，MIT | 09-21 发布；[Calendar issue 2960](https://github.com/unovue/reka-ui/issues/2960)及对应 PR 2961 显示实际交互缺陷修复；[文档](https://www.reka-ui.com/docs/overview/introduction)强调无样式与可访问性 | 焦点/键盘原语好，品牌定制自由；完整表格、表单视觉、状态组合需要更多组装 | 暂不采用。后续确需大幅独立视觉系统时重评，不以“更底层”代替 MVP |
| Nuxt UI 4.11.2，MIT | 09-22 发布；活跃维护，表单嵌套 dirty 状态有问题处理样本；[纯 Vue 安装](https://ui.nuxt.com/docs/getting-started/installation/vue)可使用 Vite，无须 Nuxt SSR | 完整组件与 Tailwind 4/Reka 生态；插件、样式与可选集成更多 | 暂不采用。不能以“必须运行 Nuxt 服务”作为淘汰理由；此处不需要该额外组合 |

状态管理只保存 UI 会话、查询缓存与重连游标；领域事实来自 daemon。Router 用 hash history，避免本地资源路由回退问题。Naive 通过具名导入和路由懒加载使用；先以语义 Token 管理颜色/间距/密度，默认跟随系统主题，可手动选择浅/深色，两套均覆盖主要状态。视觉细节由 Q-003 在可用原型上集中确认。

## 3. 其余通用能力比较

| 能力 | 选择 | 替代方案与取舍 | 边界、许可及 B 必证 |
| --- | --- | --- | --- |
| 桌面与构建 | Tauri 2 + 系统 WebView2 + Cargo workspace/pnpm workspace | Electron 自带 Chromium，播放环境较一致但驻留/包体更大；此轮沿用既有 Tauri 决定，无证据要求换架构 | Tauri MIT/Apache-2.0，最终查各包。独立进程启动与 GUI 全退出必须原生测；sidecar 打包不保证后台存活 |
| 异步与 HTTP | Tokio + tokio-util CancellationToken，reqwest 受控 TLS/重定向 | async-std/其他 HTTP 库可行，但 Tokio 与现有 Rust 生态集成更直接；不手写 TLS/HTTP | MIT 或 MIT/Apache-2.0，见注册表；网络/哈希/ffprobe 不占 DB 写事务；取消、限速、超时由应用控制 |
| SQLite 访问 | rusqlite，`bundled` + `backup`，专用 DB actor/线程 | SQLx 0.9.0 有异步、多数据库及查询检查优势；SQLite 单写者、在线备份和直接 API 场景下 rusqlite 更简洁；不能说 SQLx 不安全 | rusqlite MIT；[README](https://github.com/rusqlite/rusqlite)说明 bundled 适合 Windows、backup 提供在线备份。B 查编译选项、FTS5、外键/WAL/FULL、busy/取消，记录实际 SQLite 版本 |
| 搜索 | SQLite FTS5 trigram + 单/双字候选索引 + 字面复核 | Tantivy/Lucene 服务会增加索引及打包复杂度；仅在声明规模测量证明 SQLite 路径不满足时重评 | SQLite public domain；[trigram 文档](https://sqlite.org/fts5.html#the_trigram_tokenizer)明确不足三字限制。索引可重建且不能制造错误“无结果” |
| 本机通信与单写者 | Windows named pipe，Tokio/Windows API + OS 文件锁 | loopback HTTP 会多一个浏览器可访问面；interprocess 2.4.4 可作适配备选，暂不增加一层；fs2 0.4.3 发布于 2018，不以熟悉度标活跃 | 使用当前用户 ACL、远程连接拒绝和握手。锁服务进程持有，不用 PID 文件存在代替锁；Rust 标准锁接口优先，Windows 特性通过 `windows` 包 |
| 凭证保存 | Windows DPAPI current-user 私有 vault，`windows` API | keyring 4.2.0 跨平台抽象便于小令牌，但完整 Cookie 大小/条目上限仍需验证；明文 SQLite/配置不可用 | `windows` MIT/Apache-2.0；DPAPI 是 Windows 系统能力。独立于档案、ACL、失败不退明文，恢复需重新登录。不能阻止同用户恶意进程读取内存 |
| 媒体 | 外部可替换 ffprobe/FFmpeg，先流拷贝封装与探测 | 自写容器/编码器风险大；libav 直接链接增加 ABI/分发复杂度；不为 MVP 引 GPU 转码服务 | FFmpeg 许可随构建变化；结构/时长/轨道检查、取消、无声/双轨、播放失败路径必须固定样本验证 |
| 错误/观测/身份 | serde/serde_json、thiserror、tracing、UUID、sha2、url | 不自研 JSON、哈希算法、URL parser 或日志系统；不把随机 UUID 取代平台稳定身份 | 锁依赖与 features；事件采用结构化白名单字段，禁止敏感对象 Debug/原始响应直出 |
| 测试 | Rust 原生 + Vitest/Vue Test Utils + jsdom；Windows WDIO/Tauri service | Playwright 很适合浏览器端，但此轮再开平行浏览器套件收益小；纯 DOM/SSR 不能证明 native IPC 与进程释放 | MIT 系工具；[Tauri 官方 WebDriver](https://v2.tauri.app/develop/tests/webdriver/)现推荐 WDIO。真实 daemon 用 Rust/进程探针验证，不能 mock 成功代替 |

维护样本包括 Vue 的 pending-unmount 修复、Vite 的 Rolldown runtime 路径修复、Tokio 1.53 分支 backport、rusqlite 的构建环境修复。它们说明上游仍处理工程问题，并不说明当前稳定包已包含每个主分支提交。SQLx 仓库现重定向到 `transact-rs/sqlx`，旧地址可解析不是维护停滞证据。未对每个备选跑完整产品样例；采用理由与试验范围应按上表阅读。

## 4. 首轮版本基线

下面是 A 推荐给 B 的精确起始版本。禁止用浮动 `latest` 替代此表。B 锁直接依赖和全部传递依赖，补许可清单、来源及干净安装证据；同一兼容线修复、安全补丁和确有实证的稳定版本回退可自主完成并记录。跨框架/存储/协议替换须先写影响与 ADR，不静默改变基线。

| 层 | 起始版本 |
| --- | --- |
| 工具链 | Node 24.19.0；Rust/Cargo 1.98.1（Windows MSVC）；pnpm 12.6.0（用项目固定版本入口，不改全局设置）；Python 3.10+ 跑文档检查，本机 3.12.14 |
| Vue 与路由 | vue 3.5.43；pinia 4.0.3；vue-router 5.3.1 |
| 前端构建/类型 | typescript **6.0.3**；vue-tsc 3.3.11；vite 8.3.1；@vitejs/plugin-vue 6.0.9 |
| 组件 | naive-ui 2.45.3 |
| 桌面 | @tauri-apps/api 2.11.1；@tauri-apps/cli 2.11.5；tauri Rust crate 2.11.6；相关 build/plugin 包由 B 核对兼容稳定版后锁定 |
| Rust 主体 | tokio 1.53.1；tokio-util 0.7.19；rusqlite 0.40.2；reqwest 0.13.5 |
| Rust 通用 | serde 1.0.229；serde_json 1.0.151；sha2 0.11.0；tracing 0.1.44；tracing-subscriber 0.3.23；windows 0.62.2；uuid 1.26.1；url 2.5.8；thiserror 2.0.21 |
| 组件测试 | vitest 5.0.2；@vue/test-utils 2.5.1；jsdom 30.1.1 |
| 原生测试 | @wdio/cli、local-runner、mocha-framework、spec-reporter 9.32.0；@wdio/tauri-service 1.4.0；tauri-plugin-wdio-webdriver 1.4.0（仅测试 feature） |
| 媒体 | FFmpeg/ffprobe 同套 **9.0.2** 稳定版本优先；若固定样本回归不兼容，允许有证据回退仍维护的 8.1.3。Windows 实际二进制资产尚未核定，见 OSS-004 |

本机 pnpm 原为 11.24.0，A 的隔离试验使用 npm 安装，故 **pnpm 12 的工程安装尚未实测**。B 可使用项目级 Corepack 或 `npx pnpm@12.6.0`，确认 engine/lockfile 后固定实际入口；不能将系统已装 11 写成已跑 12。Rust 工具版本是本机观测，不代表全部 crate 已编译。

原生 WDIO 默认可通过嵌入 WebDriver 插件运行，不需要外部驱动。插件必须在独立 `e2e` feature 中编译/注册，发行构建禁用，测试端点只在隔离 Profile 启用。若走原生 tauri-driver 备用路线，固定稳定版本及匹配 Edge driver，禁止误把 alpha/latest tag 当产品 Tauri 版本。mock 路线只测渲染状态，进程生命周期必须连接真实 daemon。

## 5. 已完成的前端兼容试验

在仓库外临时目录安装表中的 Vue、组件、构建、状态和测试包，渲染带中文表格、输入、按钮和“覆盖未知”状态的 SFC。

| 尝试 | 实际结果 | 结论 |
| --- | --- | --- |
| TypeScript 7.0.2 + vue-tsc 3.3.11 | 安装 passed；typecheck failed：`ERR_PACKAGE_PATH_NOT_EXPORTED`，无法解析 TypeScript `./lib/tsc` | 不把 peer 范围 `>=5` 当兼容实证；原日志见[失败记录](2026-09-26/vue-stack-probe.json) |
| 仅改 TypeScript 为 6.0.3 | typecheck passed；Vite build passed；Vitest 1 个 SSR 渲染用例 passed | [复验记录](2026-09-26/vue-stack-probe-compatible.json)保存源代码、命令、锁摘要；保留 strict，不跳过 Vue 类型检查 |
| 生产构建观察 | 最小样例 JS 522.96 kB，gzip 158.62 kB；出现 500 kB chunk 警告 | 不是产品包大小/内存数据。B 做路由拆分、按需加载和真实内容测量，不能仅提高阈值消警告 |

工具脚本最初打印 Unicode 检查符时遇到 Windows GBK 编码错误，已改 stdout UTF-8 后完整复跑；这与库类型兼容失败分别记录。此次 SSR 用例不证明键盘、中文 IME、焦点、WebView2 播放或 GUI 关闭后台继续。

该最小样例的 `strict=true`、`skipLibCheck=true`：Vue/项目源码经过类型检查，但依赖声明文件未逐项检查。B 需按实际工程配置验证 API 使用和跨模块类型；不能把这条试验描述成所有依赖声明已完全兼容。

## 6. 媒体分发与可替换边界

[FFmpeg 下载页](https://ffmpeg.org/download.html)显示 9.0.2 于 09-18 发布、8.1.3 于 09-21 发布。源码发布与 Windows 预编译包是两件事。[法律说明](https://ffmpeg.org/legal.html)要求按实际编译配置履行 LGPL/GPL 等义务；进程隔离不免除二进制分发责任。

[BtbN 构建说明](https://github.com/BtbN/FFmpeg-Builds)区分 `lgpl`/`lgpl-shared`、`gpl`、`nonfree`。初始选择为稳定版 win64 LGPL shared 资产路线，不采用 nonfree 或浮动 nightly；本次资产 API 补查被 403 限流，已停止请求，**没有凭空填写下载地址/摘要或声称资产许可已验收**。B 在 P0 核对可取得的确切 tag、资产 URL、SHA-256、`-version/-buildconf`、附带许可证/对应源码，并放入依赖台账。若没有匹配资产，以明确配置的外置已核验后端做本地开发，产品包显示依赖缺失/配置入口；内置打包仍受 OSS-004 门槛约束。不得默默从系统 PATH 选择未知版本。

先处理 AVC/AAC MP4、DASH 音视频流拷贝合成及无声视频，保留来源规格与实际探测规格。不为缩小范围丢弃 Bilibili 必要分 P/音轨，不将平台质量代码等同于实际质量。暂不做转码质量升级；不能播放时提供明确受控外部打开。发布前 Q-004 决定项目许可，开发期间仍保留所有已采用资产的通知与来源。

剩余集成任务全部归[事项总表](../decisions/open-questions.md)的 OSS-001～008，本文不维护第二份进行中清单。
