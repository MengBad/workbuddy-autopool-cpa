# Development Guide

本地构建、测试、调试、发布流程。本仓库根目录**就是**插件包（`package main`），
没有 `workbuddy/` 子目录，也没有多模块。

## 环境

- Go 1.26+（与 CPA 宿主的 `go.mod` 对齐）
- **CGO 必须开启**：`-buildmode=c-shared` 依赖它
- 交叉编译另需对应 C 工具链（linux/arm64 用 `aarch64-linux-gnu-gcc`，
  darwin 用 [osxcross](https://github.com/tpoechtrager/osxcross)）

## 构建

```bash
make build
# 等价于
CGO_ENABLED=1 go build -buildmode=c-shared -ldflags "-X main.version=$(cat VERSION)" -o workbuddy.so .
```

产物 `workbuddy.so`（同时会生成 `workbuddy.h`，它只是 c-shared 的副产物，不要提交）。

## 测试

```bash
make test                       # go test -race -count=1 ./...
go test -cover ./...            # 覆盖率
go test -coverprofile=coverage.out ./... && go tool cover -html=coverage.out
```

当前 60 个测试文件（每个都以被测源文件命名），覆盖的关键路径：

- 缓存合并（credits / plan / checkin 在快速路径上不被清空）
- alias 反解（客户端 alias → 上游 model id）与新旧前缀（`cnwb/` `intlwb/` `cn/` `cb/`）
- `tool_choice` 归一（object → string，`none` 抑制 tools）
- SSE chunk 清洗（空 tool_call 壳被剥掉）
- UID 清洗（auth 文件名的路径穿越防御）
- 会话软粘性（sticky + 溢出重绑 + 持久化）
- 积分生命周期迁移（耗尽 → 禁用 / 删除 / 签到后恢复启用）
- 账号池（最低可用、隔离、`CreditsErr` 不得视为可用）
- 活动时间窗（跨零点、全天 `0:00–23:59`、时区偏移）
- 排队分类（6020/6021/6022 不得触发换号）
- 探测失败分类与恢复时间解析

## Lint

```bash
make lint      # gofmt -l（必须空）+ go vet；装了 staticcheck/gocritic/unparam 就跑
```

## 本地调试

插件不能独立运行，必须由 CPA 加载。最短迭代回路：

```bash
# 1) 构建
make build

# 2) 拷进 CPA 的插件目录（按自己的部署改路径）
cp workbuddy.so /opt/cpa/plugins/workbuddy.so

# 3) 重启宿主
docker restart cpa

# 4) 看注册日志
docker logs -f cpa 2>&1 | grep -i workbuddy
# plugin registered plugin_id=workbuddy ... version=0.14.0
```

冒烟测试：

```bash
# 流式（上游拒绝非流式，必须带 stream:true）
curl -N http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer $CPA_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"cnwb/glm-5.3","messages":[{"role":"user","content":"hi"}],"max_tokens":5,"stream":true}'

# 管理 API
curl http://localhost:8317/v0/management/plugins/workbuddy/accounts \
  -H "Authorization: Bearer $MGMT_KEY"

# 面板
open http://localhost:8317/v0/resource/plugins/workbuddy/panel
```

## 发布

完整流程见 [release-checklist.md](release-checklist.md)。最短版本：

```bash
# 1) 改 VERSION + CHANGELOG 顶部加一节
# 2) 本地跑满门禁
make lint && make test && make check-registry
# 3) 提交
git commit -am "v0.14.1"
# 4) 打 tag（CI 会构建、盖章 registry.json、发 release）
make tag VERSION=0.14.1
```

CI（`.github/workflows/ci.yml`）在 tag 上产出
`workbuddy_<ver>_linux_amd64.zip`、`registry.json`、`release-notes.md`。
插件商店读 `releases/latest/download/registry.json`，所以商店看到的永远是已发布版本。

## 代码约定

- 一个文件一个职责，单文件 ≤ 1500 行（`models_config.go` 是已知的唯一例外）
- 新增源文件必须同名字带 `_test.go`；`main.go` 只放 C ABI 边界，不放业务逻辑
- 非 Go 资源放各自的目录：`web/`（面板）、`docs/`、`scripts/`
- 注释用英文（社区惯例）；**面向用户的字符串可以是中文**
- 不轻易加依赖：`go.mod` 目前只有 CPA SDK + 三个间接依赖
- 线格式神圣：`pluginapi` / `pluginabi` 的字段名不改
- 任何提交都必须保持 `go test -race ./...` 全绿
