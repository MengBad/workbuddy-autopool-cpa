# Release Checklist

本仓库的发布只有一条主线：`main` + tag。CI（`.github/workflows/ci.yml`）
在 tag 上构建、盖章 `registry.json`、发 release。

## 0. 前置门禁（必须全绿）

```bash
make lint            # gofmt -l 必须无输出 + go vet
make test            # go test -race -count=1 ./...
make check-registry  # registry.json 与 VERSION 一致
make build           # CGO_ENABLED=1 c-shared
```

四条任何一条红，不要打 tag。

## 1. 版本号三处同步

| 位置 | 内容 |
|---|---|
| `VERSION` | `0.14.1`（无 `v`，无换行之外的字符） |
| `registry.json` → `plugins[0].version` / `release_tag` | `0.14.1` / `v0.14.1` |
| `CHANGELOG.md` | 顶部加 `## v0.14.1 (YYYY-MM-DD) — 标题` |

`main.go` 的 `version` 变量由 `-ldflags "-X main.version=..."` 注入，
`make build` 和 CI 都从 `VERSION` 读，不需要手改。

`make check-registry` 会核对前两处；CHANGELOG 的标题格式是 release 工作流
抽 release notes 的依据（`^## v<version>`），写错就抽不到正文。

## 2. 打 tag

```bash
git add -A && git commit -m "v0.14.1"
git push origin main
make tag VERSION=0.14.1        # git tag -a v0.14.1 && git push origin v0.14.1
```

tag 必须从 `main` 的 HEAD 打，且必须是 `v<version>` 形状——插件商店按这个形状解析。

## 3. 盯 CI

`gh run watch` 或仓库 Actions 页面。release job 会：

1. 构建 `dist/workbuddy.so`（linux/amd64，`-trimpath -ldflags "-s -w -X main.version=<tag>"`）
2. 打包 `workbuddy_<ver>_linux_amd64.zip`（zip 根目录直接是 `workbuddy.so`，
   这是 CPA 插件商店 `ArchiveName()` 要求的形状）
3. 生成 `.sha256`
4. 把 `registry.json` 的 `version` / `release_tag` 改成 tag 值，作为 release 资产上传
5. 从 `CHANGELOG.md` 抽出该版本小节，作为 `release-notes.md` 与 release 正文

## 4. 按用户视角验收

- [ ] `gh release view vX.Y.Z`：`workbuddy_<ver>_linux_amd64.zip`、
      `.sha256`、`registry.json`、`release-notes.md` 都在。
- [ ] release 附带的 `registry.json` 里 `version` / `release_tag` 与 tag 一致。
- [ ] 下载 zip，解出来是 `workbuddy.so`（不是嵌套目录）。
- [ ] `releases/latest/download/registry.json` 能直接打开。

## 5. 部署验证（可选但推荐）

```bash
V=$(cat VERSION)
curl -fLO "https://github.com/MengBad/workbuddy-autopool-cpa/releases/download/v$V/workbuddy_${V}_linux_amd64.zip"
unzip -o "workbuddy_${V}_linux_amd64.zip"
cp workbuddy.so /opt/cpa/plugins/workbuddy.so
docker restart cpa
docker logs cpa 2>&1 | grep "plugin registered.*workbuddy"
# 期望 version=$V
```

面板打开 `/v0/resource/plugins/workbuddy/panel`，确认模型列表、账号池、
诊断栏都正常渲染，且 `/update/check` 不再提示有新版本。

## 6. 回滚

```bash
cp /opt/cpa/backup-<ts>/workbuddy.so /opt/cpa/plugins/workbuddy.so
docker restart cpa
```

插件是单个 `.so`，回滚就是换文件。状态文件（`workbuddy-pool.json` 等）
向前兼容，不需要一起回滚。

## 关于官方插件商店

默认插件源 `router-for-me/CLIProxyAPI-Plugins-Store` 通过
`releases/latest` 动态解析安装目标，所以 release 一发出来就能装。
但它的 `registry.json` 里 `version` 字段是**显示用回退值**（GitHub API
失败或被限流时用），那个值需要单独提 PR 更新，否则老版本 CPA 会一直显示旧版本号。

提 PR 时：

- 只改 `workbuddy` 那一行 `version`，`schema_version` 不动，版本号不带 `v`。
- 附上 release 链接，并确认所有 `<id>_<version>_<goos>_<goarch>.zip` 资产存在。
