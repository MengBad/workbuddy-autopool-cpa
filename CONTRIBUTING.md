# 贡献

## 提交前必须通过

```bash
gofmt -l .                      # 必须无输出
go vet ./...
go test -race -count=1 ./...
CGO_ENABLED=1 go build -buildmode=c-shared -o /tmp/workbuddy.so .
```

CI（`.github/workflows/ci.yml`）跑的正是这四步，任何一步红了都不会合并。

## 改动约定

- **面板改动要能在离线状态下验证**：`panel.html` 用 `go:embed` 进二进制，
  改完必须真的构建一次并打开面板确认，不能只看 diff。
- **新增上游字段要留证据**：解析某个上游 JSON 字段时，在提交说明里贴一段真实响应片段
  （脱敏后）。上游改了字段，我们宁可显式退化也不要猜。
- **不猜数字**：上游没给倍率就显示 `?`，没给恢复时间就写「上游未给出何时恢复」。
  任何估算都要在 UI 上标明是估算。
- **测试只加不减**：修 bug 必须附一个能复现该 bug 的测试。

## 版本与发布

1. 改 `VERSION`（如 `0.14.1`）。
2. 在 `CHANGELOG.md` 顶部加一节 `## v0.14.1 (YYYY-MM-DD) — 标题`。
3. 提交后打 tag 并推送：`git tag -a v0.14.1 -m "v0.14.1" && git push origin v0.14.1`。
4. CI 会构建、盖章 `registry.json`、并发布 release 与 `workbuddy_<ver>_linux_amd64.zip`。

## 报告问题

面板「模型管理 → 诊断」页签有「复制诊断全文」，把整段贴进 issue。
里面有区域、上游域名、目录计数、前缀状态和**上游错误原文**，比描述「不能用」有用得多。
