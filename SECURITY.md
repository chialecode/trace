# TRACE 安全报告

可能涉及凭据泄露（Cookie、站点令牌、签名 URL）、越权访问、任意代码执行或用户数据损失的问题，请通过 [GitHub 私密漏洞报告](https://github.com/chialecode/trace/security/advisories/new)提交。不要在公开 Discussions 里披露可利用的细节、真实 Cookie、签名 URL 或账号信息。

报告请说明受影响的提交或本地包（包目录名与 `SHA256SUMS.txt` 摘要）、前提、复现步骤、影响和可用的合成样本。如果有效的站点 Cookie 已经泄露，先在对应网站退出该会话或修改密码让它失效；只删除 Git 或聊天里的文本，并不能让凭据失效。

项目目前没有正式发行版。维护范围是 main 分支的内容，不承诺安全支持期限或响应时间。产品里凭证怎样保存、日志怎样脱敏，见[技术设计](docs/design/architecture.md#credentials)。
