# TRACE 安全报告

可能涉及凭据泄露（Cookie、站点令牌、签名 URL）、越权访问、任意代码执行、档案或用户数据损失的问题，请通过 [GitHub 私密漏洞报告](https://github.com/chialecode/trace/security/advisories/new)提交（本仓库已开启，[ACT-009](USER-ACTIONS.md#act-009)）。不要在公开 Issue 中披露可利用细节、真实 Cookie、签名 URL、账号或私人档案。

报告应说明受影响的提交或本地包（包目录名与 `SHA256SUMS.txt` 摘要）、前提、复现步骤、影响和可用的合成样本。若已暴露有效的站点 Cookie，先在对应网站退出该会话或更改密码使其失效；只删除 Git 或聊天中的文本不能让泄露的凭据失效。

项目目前只有本地便携包，没有正式发行版（[Q-004](USER-ACTIONS.md#q-004)）。维护范围是 main 分支的内容及其配置，不声明未发布版本的安全支持期限或响应 SLA。

产品的凭证隔离、日志与数据规则见[数据与安全](docs/dev-rules/data-and-security.md)，仓库保护与远端设置见[Git 规则](docs/dev-rules/repo-and-environment.md#git-delivery)。
