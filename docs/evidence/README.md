# 验证与交付证据

- [M1 r3 A集中复核与局部修复](../delivery/stage-report.md#a-m1-r3-review)、[公开机器摘要](gate/m1-r3-a-review.json)：逐提交输入、原包、回执及934样本核对通过；核实原七项返工，直接修复F-031/032，当前m1-r3-12及324个自动用例通过。当前源码完整正式资源测量934样本/3620.750秒录制，18项独立复算通过；当前包四项真实链通过，328用例/171必需完整，两个最终聚合入口退出0；M1技术退出passed，原生产品验收not-run。

- [M1 r3 B集中返工与被审证据](../delivery/stage-report.md#b-m1-r3-delivery)、[公开机器摘要](gate/m1-r3-b-delivery.json)：六项P1和F-030修复、P14/C14链接流程、322个自动用例、当前24文件包及934个连续样本的完整正式资源测量通过。整体聚合failed，四个真实产品链用例及关联需求仍缺；以上为B交付时事实，A当前结论见上方复核；AC-29/30未齐不能退出M1。

- [M1存储中断与恢复核对](../delivery/stage-report.md#m1-storage-outage-recovery)、[B对应正式复测](../delivery/stage-report.md#m1-r3-final-resource)：保留宿主盘满后的中断目录和缺失计数，不补造原始证据；恢复后核对源码、包、Git，原规模复测完整恢复2.201秒。F-030原45.246秒失败、旧二进制及全部原样本继续保留。

- [M1 r3 A来源诊断与交接](../delivery/stage-report.md#m1-real-source-readiness)、[脱敏摘要](gate/m1-r3-a-readiness.json)：当时两视频/两1080P直播及完整自动解码成功；A修复WBI，发现六项P1并追加链接产品链。诊断不作Core验收；原r1/r2包、长测及失败继续保留，不换绑。

- [M1 r2 A集中复核](../delivery/stage-report.md#a-m1-r2-review)及[公开A摘要](gate/m1-r2-a-review.json)：原F-019/F-021/F-022已核实，新F-023～025三个独立P1反例失败；同一输入下包/资源与原自检证据有效，整体技术结论failed，集中返工不转成用户待办。

- [M1 r2集中实现与自检](../delivery/stage-report.md#b-m1-r2-delivery)：B交付时P11～P13/C11～C13、F-019/F-021/F-022、schema5、认证历史/资源升级/直播逐片标签、包与正式复测的原记录；A当前结论在上方独立入口。

- [M1 r2公开机器摘要](gate/m1-r2-self-check.json)：216项输入、290个唯一用例、133个必需用例、85/66/19需求范围、新m1-r2-2包和当前原始样本摘要。完整回执/bundle只留本地artifacts。

- [M1原r1两站归档与录制](../delivery/stage-report.md#final-m1-delivery)：P0～P10/C01～C10的当时输入、包、失败复验、资源实测和B→A交接；保留历史事实，不用于当前r2放行。

- [M1原r1公开机器摘要](gate/m1-self-check.json)：199个输入、255个用例、98个必需映射、23个包成员及正式原始采样摘要；原始日志、Profile和完整bundle留在本地artifacts，不复制进公开内容。

- [2026-10-07 A 文档与主页交付](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/evidence/2026-10-07-documentation-and-home.md)：本地参考隔离、用户事项、蓝白界面、自审与下一轮计划。

证据只证明特定版本、环境和样本的观测，不自动成为产品约束。格式见[验证模板](../templates/validation-report.md)，当前完成状态只在[交付状态](../delivery/status.md)汇总。

- [2026-09-25 文档框架交付](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/evidence/2026-09-25-documentation-framework.md)：本次文档、检查与范围边界。

- [2026-09-26 A 规划与 Git 基线](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/evidence/2026-09-26-m0-m1a-planning.md)：具体选型/两站表/长计划、TS兼容试验、沟通/文档机制与两次提交。

- [2026-09-26 B 实施交付（M0/M1a）](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/evidence/2026-09-26-m0-m1a-delivery.md)：分支/基点/实际版本、P0～P9 与 C01～C35 逐项结果、失败与复验、离线闭环、打包与 A 复核范围。

- [2026-10-01 A 集中复核（M0/M1a）](https://github.com/chialecode/trace/blob/960984c34487244f31f1238dffc2bd9faaa292e7/docs/evidence/2026-10-01-m0-m1a-review.md)：新机复验、F-001～F-014 发现、A 直接修复、用例状态更正与 A→B 返工 prompt。

未来每轮通常一份交付报告加明确审查部分；原始日志、合成样本和结果可在配套目录保存。保持失败历史与复验版本，不复制明文凭据或真实用户档案。
