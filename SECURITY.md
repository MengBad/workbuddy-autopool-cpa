# 安全说明

## 这个插件处理什么

- WorkBuddy 的 OAuth 凭据（access / refresh token），存放在 CPA 的 `auths/` 目录。
- 账号池状态（积分、隔离窗口、UID），存放在数据目录的 `workbuddy-pool.json`。
- 逐请求的 token 与积分用量，存放在 `request-usage.jsonl`。

以上文件**全部**列在 `.gitignore` 里。提交前请确认 `git status` 里没有它们。

## 部署建议

- 管理 API 前缀 `/v0/management/plugins/workbuddy`，mutating 接口在配置了
  `management_key` 时要求 Bearer。**生产环境请设置 `WB_MANAGEMENT_KEY`**，
  不要留空。
- 面板本身不鉴权，鉴权由 CPA 宿主负责——不要把管理端口直接暴露到公网。
- 日志里不打印 token 原文；如果你在 issue 里贴日志，仍建议先扫一遍。

## 报告漏洞

请用 GitHub 的私有漏洞报告（Security → Report a vulnerability），不要开公开 issue。
