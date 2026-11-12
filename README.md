<p align="center"><img src="logo.png" width="120" alt="logo"><br><b>WorkBuddy Intl AutoPool CPA</b><br>
把国内版 / 国际版 WorkBuddy 账号接入 CLIProxyAPI（CPA）——<br>
每次刷新按上游目录读取积分倍率与活动折扣、逐模型显示繁忙度、单个模型真实体积可用性测试、<br>
以及一套围绕账号池的运维能力。</p>

> **本项目是 [`zidanefaqih/codebuddy-intl-cpa`](https://github.com/zidanefaqih/codebuddy-intl-cpa) 的再维护分支（当前 v0.14.3）。**
> 插件内部标识仍是 `workbuddy`，凭据格式、模型名、配置键、面板路径全部沿用，
> 现有账号与客户端无需任何迁移。自动注册/批量补号已在 0.12 退役（见「与 0.11 的差异」）。

<p align="center">
  <a href="https://github.com/MengBad/workbuddy-autopool-cpa/releases/latest"><img alt="release" src="https://img.shields.io/github/v/release/MengBad/workbuddy-autopool-cpa?display_name=tag&sort=semver"></a>
  <a href="https://github.com/MengBad/workbuddy-autopool-cpa/actions/workflows/ci.yml"><img alt="ci" src="https://github.com/MengBad/workbuddy-autopool-cpa/actions/workflows/ci.yml/badge.svg"></a>
  <img alt="license" src="https://img.shields.io/badge/license-MIT-blue">
  <img alt="go" src="https://img.shields.io/badge/go-1.26%2B-00ADD8">
</p>

<p align="center">
  <a href="#安装">安装</a> ·
  <a href="#配置">配置</a> ·
  <a href="#管理-api">管理 API</a> ·
  <a href="#面板">面板</a> ·
  <a href="#已知限制">已知限制</a>
</p>

---

## 目录

| 章节 | 内容 |
|---|---|
| [功能](#功能) | 模型命名 · 目录倍率/活动/繁忙度 · 逐模型推理档 · 上下文预算 · 排队 · 可用性测试 · 诊断栏 |
| [账号池](#账号池运维视角) | 池状态、最低可用保证、隔离、持久化 |
| [与 0.11 的差异](#与-011-的差异重要) | 自动注册为何退役 |
| [安装](#安装) | release 二进制 / 源码构建 / 插件源 |
| [配置](#配置) | 完整 `config.yaml` 与环境变量 |
| [管理 API](#管理-api) | 全部端点 |
| [面板](#面板) | 面板分区与交互 |
| [验证记录](#验证记录2026-11-12线上实测) | 线上实测数字 |
| [已知限制](#已知限制) | 上游限制与刻意的取舍 |
| [致谢与来源](#致谢与来源) | 上游与参照项目 |

---

## 它解决什么问题

你在 CPA 上挂了几个 WorkBuddy 账号。然后你会遇到这些事：

- **到底哪个模型现在便宜？** 上游有夜间折扣、限时免费、订阅优先，倍率随时在变，但 CPA 的模型列表里只有一个名字。
- **这个模型现在忙不忙？** 上游排队时要等几千位，选之前完全不知道。
- **这个模型现在能不能用？** 显示能用，实际长对话每次都 429；或者能调通，但被限流到几小时后。
- **账号还剩多少、什么时候过期、谁在被限流？** 要一个个点开看。
- **出问题了怎么反馈？** 说不清是哪个区域、哪个账号、上游原话是什么。

这个插件把上面每一条都做成面板上能直接看见的东西，并且**所有数字都来自上游原文**，不猜、不写死、估算的地方会明确标出来。

---

## 功能

### 模型命名：`cnwb/` 与 `intlwb/`

两个区域各有自己的命名空间，**默认都开着**：

| 区域 | 前缀 | 例 |
|---|---|---|
| 国内版 (CN) | `cnwb/` | `cnwb/glm-5.3` |
| 国际版 (Global) | `intlwb/` | `intlwb/gpt-5.6-sol` |

`cnwb` = CN WorkBuddy，`intlwb` = International WorkBuddy。旧默认是 `cn/` 与
「国际版裸名」——`cb` 读作 CodeBuddy，而国内侧品牌也叫 CodeBuddy，两个区域在
列表里看起来可以互换，所以换掉了。

**为什么国际版也要带前缀**：裸名会和任何注册了同名上游模型的 provider 撞车。
实测一个裸名 `glm-5.3` 被路由到了 Fireworks 的直连条目，返回
`accounts/fireworks/models/glm-5p3`，**根本没走本插件的账号**——这类冲突无法
从插件侧修复，只能靠命名空间隔离。

**旧前缀仍能识别**：`cn/` 与 `cb/` 保留在归属判定里，已写进客户端配置的旧模型 id
仍能正确解析到对应区域；只是插件不再**发布**这两个前缀。

### 模型目录：倍率、活动、繁忙度

- **积分倍率**（`x0.79` 这种）从上游 `credits` 字段直读。45/69 个模型有倍率，其余如实显示 `?`（上游没给）。
- **活动徽标**：解析上游 `modelPromotions`，在模型行画出「夜间折扣」「限时免费」「订阅优先」。
  - **徽标是「此刻」的函数**：活动有自己的时间窗（`23:00–7:50`，带明确时区）或有效期（`validFrom..validUntil`），
    窗口一关徽标自己消失。跨零点时段按上游声明的时区判断。
  - **折后价直接显示**：`factor` 生效时，模倍率显示折后值，原价用删除线保留在旁边，两个数字不会混淆。
  - 上游把「夜间折扣」拆成两条记录（夜间 23:00–7:50 带 `factor=0.5`，白天 7:50–23:00 是配套徽标 `factor=null`），
    本插件按 `priority` 选当前生效的那条，而不是把两条都画上。
- **繁忙度**（仅国内版）：`POST /v2/chat/busy` 是免费只读探针，一次最多 50 个 id。
  - 画成图标：🚀 空闲 / 🧍 正常 / 🧍🧍 繁忙 / 🧍🧍🧍 拥堵 / 🚫 不可用。
  - **`unknown` 一律不画**——「没被告知」不等于「一切正常」。
  - 等级只活在内存里，不落盘（目录能活过重启，负载只是这一分钟的事）。
  - **国际版没有这个徽标，因为上游国际网关根本没有这条接口**（`/v2/chat/busy` 回 404，同族路径也是）。
    这不是没实现，是数据不存在；插件探测失败就安静退化成没徽标，不挡模型列表。

### 逐模型推理档位

上游为每个模型声明**不同的**推理档位集合，实测：

| 档位集合 | 模型数 | 例子 |
|---|---|---|
| `["low","high","max"]` | 6 | `glm-5.3`、`glm-5.3-flash` |
| `["low","high"]` | 4 | `hy3`、`hy3-b` |
| `["high","xhigh"]` | 3 | `glm-5.2`、`deepseek-v4-flash` |
| `["low","medium","high","xhigh","max"]` | 2 | `space-bunny` |
| `["high"]` | 11 | `hy4-preview-*` |

所以不能给一个全局值——**每个模型行有自己的选择器**，选项就是该模型自己声明的那些。

- **它影响哪些请求**：客户端**没有**指定 `reasoning_effort` 的那些（新对话、压缩摘要、会话标题）。
  客户端明确带了档位，一律尊重客户端的。
- **不会发出模型没声明过的档位**：`effortSupported` 会核对声明列表，
  不合法就跳过而不是把请求打成 400。
- **`hy3` 族跳过**：上游对该族只认 `high`（见 `forceMaxThinking`），
  设别的值会被上游静默忽略，所以不动它。
- **能验证**：注入时写一行日志
  `effort: model="<id>" reasoning_effort=<level> (from settings)`。

### 上下文预算

三档：**原生 / 200K / 500K**。

- 「原生」是**删除覆盖项**，不是写入一个等于今天窗口的数字——
  这样上游日后调大该模型的窗口，这一档会跟着变大。
- 压小预算能让客户端更早压缩对话，适合长会话频繁撞限流的场景。

### 排队：说清楚你排在第几位

上游用三个业务码表达等待：`6020` 等待、`6021` 队列满、`6022` 并发上限。
撞上它们时插件不再转述上游的本地化 key，而是给出：

```
模型排队中：你排在第 8777 位，队列共 9021 个请求。请稍后重新发送这条消息，或换一个模型。
```

两条刻意的设计：

- **排队不换号。** 队列是模型级 + 平台级的，换账号拿到的还是同一个位次。
  把它当账号失败会把一个健康账号误打入 30 分钟冷却——所以 `classifyAccountFailure`
  里排队直接短路成 `outcomeStop`。
- **不自动重试。** 几千位的队列，重试十几次也轮不到，只会把真实请求白白花掉。
- 上游没给位次时如实显示「上游未给位次」，不编数字。

### 模型可用性测试（会花钱，所以只能单个点）

每个模型行有一个「测试」按钮，直接告诉你此刻能不能用、不能用时何时恢复。

- **发的是真实体积请求（约 25k 输入 token）**，不是「ping 一下看通不通」。
  上游限流**按请求体积触发**，小请求看着正常、长对话里却每次被拒——那正是「测试通过、实际被限」的来源。
- **恢复时间来自上游响应体**：429 不带 `Retry-After`（也没有 `X-RateLimit-*`），
  但上游把重置时刻写在 message 里（`…将在 2026-09-30 02:30:30 UTC+8 重置…`）。
  插件解析它并按声明的偏移换算，所以能直接显示「2026-9-30 02:30:30 之后可再用」。
  三处来源都没说时，**如实显示「上游未给出何时恢复」，不编造倒计时**。
- **成功即无冷却**：这个网关在 200 响应上也带 `Retry-After`，值是账号月度积分包的刷新时刻，
  它不是「你在此之前不能用」。所以成功的行只显示「✓ 可用」，绝不附「之后可再用」。
- **只能单个模型手工点，没有批量按钮**：一次覆盖整个列表就是一次不可控支出。
  服务端也据此只接受单模型请求，多模型会被拒。

### 诊断栏（能被整段贴进 issue）

模型设置里新增「诊断」页签，是一张**纯只读事实表**：

| 项 | 内容 |
|---|---|
| 区域 / 上游域名 | `cn` → `copilot.tencent.com`；`global` → `codebuddy.ai` |
| 拉取账号 / 拉取时间 | 这次目录是谁拉的、什么时候拉的 |
| 目录模型数 / 启用的模型数 / 免费模型数 | 三个计数 |
| 有倍率的模型 / 当前有活动的模型 / 有繁忙度的模型 | 三个计数，能立刻看出上游是不是没下发 |
| 前缀 / 上游错误 | 前缀状态、**上游错误原文** |

**「复制诊断全文」**一键拷贝成文本。这一栏不做任何设置，存在的唯一理由就是能被整段贴进问题反馈，
所以里面**没有任何会改设置的控件**。

### 账号池：运维视角

- **池状态**：每个账号的 `active / exhausted / quarantined / disabled / invalid`、积分、隔离截止时间、并发槽位。
- **最低可用保证**：`min_available_accounts`（默认 2）。可用 = 未禁用 + 未隔离 + 积分 > 0 + token 有效或可刷新。
- **池口径**：`pool_realm` 可选 `global` / `any`。
  **如果你只有国内账号，务必设成 `any`** —— 否则可用数永远是 0/N，告警一直响（0.11 遗留的一个真实坑）。
- **手动隔离 / 解除**：面板勾选即可，立即重算池状态。
- **池子不够用时只提示，不自动创建账号**：面板会显示缺口数量并指向「＋ 导入账号」。
  0.12 起没有任何注册入口——见下一节。
- **持久化**：`workbuddy-pool.json`，key 用 **UID**（`auth_index` 每次重启都变，用它当 key 隔离会失效）。
  重启时把未过期窗口灌回路由冷却表，不会重启后又去撞一个已知耗尽的号。

### 沿用上游的能力

每日签到（CN，09:00/21:00）· 国际版每日 30 积分奖励（08/12/16/20，幂等）· 专家试用包一次性领取 ·
22:00 token 保活 · 逐请求积分/Token JSONL 日志 · 会话软粘性 · 每账号 2 并发准入 ·
插件内 429 / 14018 failover（**同 realm 内换号，不依赖宿主**）· 模型前缀与隐藏 · OAuth 区域选择（intl/cn）。

---

## 与 0.11 的差异（重要）

**自动注册已退役。** 0.11 里有一套「账号不够就自动开注册链接」的流程，0.12 删除了：

- 删掉的：`autoregister.go`、`pool_handlers.go` 里的 `/pool/register*` 端点、`auto_register` 配置项、
  所有注册会话状态机与审计历史。
- 保留的：账号池本身（状态、积分、隔离、最低可用、持久化）。
- 池子不够用时现在只做一件事：**在日志里说一次「可用 N/M 低于下限，请从面板添加账号」**，不再自动动作。

理由：创建账号在上游需要一次浏览器 OAuth 登录（Google/GitHub/Apple/X），
这是一个需要人的步骤，做成「自动」只会产生一堆卡住的 pending 会话。补号是运维决策，不是插件该替他做的事。

**Token 救援**：`refresh.py`（在 `cb-register` 目录）用 `X-Refresh-Token` 头刷新 accessToken，
一个号实测续命 30 天。这不是注册，是保活，保留。

---

## 安装

### 方式一：直接用 release 里的二进制（推荐）

每次打 tag 都由 CI 产出 linux/amd64 的 `workbuddy.so`：

```bash
V=0.14.3
curl -fLO "https://github.com/MengBad/workbuddy-autopool-cpa/releases/download/v$V/workbuddy_${V}_linux_amd64.zip"
unzip -o "workbuddy_${V}_linux_amd64.zip"        # 解出 workbuddy.so
cp workbuddy.so /opt/cpa/plugins/workbuddy.so    # Docker 下 /opt/cpa/plugins 是 bind mount
docker restart cpa
docker logs cpa 2>&1 | grep "plugin registered.*workbuddy"
```

### 方式二：从源码构建

```bash
# 1) 备份（先备份再动手，回滚只要一行）
BK=/opt/cpa/backup-$(date +%Y%m%d-%H%M%S); mkdir -p "$BK"
cp -a /opt/cpa/plugins/workbuddy.so "$BK/" ; cp -a /opt/cpa/config.yaml "$BK/"

# 2) 构建（Go 1.26+，必须 CGO；本仓库根目录就是插件包）
git clone https://github.com/MengBad/workbuddy-autopool-cpa.git
cd workbuddy-autopool-cpa
CGO_ENABLED=1 go build -buildmode=c-shared \
  -ldflags "-X main.version=$(cat VERSION)" -o workbuddy.so .

# 3) 部署
cp workbuddy.so /opt/cpa/plugins/workbuddy.so
docker restart cpa

# 4) 确认
docker logs cpa 2>&1 | grep "plugin registered.*workbuddy"
# plugin registered plugin_id=workbuddy ... version=0.14.3
```

回滚：`cp "$BK/workbuddy.so" /opt/cpa/plugins/workbuddy.so && docker restart cpa`

### 插件源（CPA 插件商店）

把下面这个 URL 填进 CPA 的插件源：

```
https://github.com/MengBad/workbuddy-autopool-cpa/releases/latest/download/registry.json
```

`registry.json` 由 release 工作流按 tag 自动盖章（`version` / `release_tag`），
所以插件商店看到的永远是已发布的版本，而不是还没产物的 `main`。

---

## 配置

```yaml
plugins:
  configs:
    workbuddy:
      enabled: true

      # 账号池
      min_available_accounts: 2    # 最低可用账号数（WB_MIN_AVAILABLE）
      pool_realm: any              # global | any —— 只有 CN 号时务必用 any
      pool_check_interval: 2m      # 积分刷新 + 约束检查
      quarantine_minutes: 30       # 无上游 reset 时间时的默认隔离窗口

      # 上游原有
      checkin_auto: true           # CN 每日签到
      lifecycle_auto: true         # 积分耗尽后禁用/删除
      token_keepalive: true        # 22:00 保活
      daily_bonus: true            # Global 每日 30 积分
      oauth_region: intl           # intl | cn
      self_url: http://127.0.0.1:8317
      mm_live_reload: true
      management_key: ""           # 建议用 WB_MANAGEMENT_KEY 环境变量
```

### 环境变量

| 变量 | 作用 |
|---|---|
| `WB_UPSTREAM_GLOBAL` | 覆盖 Global 上游基址 |
| `WB_CACHE_TRACE` | 内容无关请求指纹的默认开关 |
| `WB_MIN_AVAILABLE` | 覆盖 `min_available_accounts` |
| `WB_OAUTH_REGION` | `intl` / `cn` |
| `WB_MANAGEMENT_KEY` | 插件层管理密钥 |
| `WB_SELF_URL` | 保存模型设置后触发热重载的自身地址 |
| `WORKBUDDY_DATA_DIR` | 覆盖数据目录（默认 `<auth dir>/.codebuddy_data`） |

---

## 管理 API

前缀 `/v0/management/plugins/workbuddy`。mutating 接口在配置了 `management_key` 时要求 Bearer。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/models/config?realm=cn\|global` | 目录 + 倍率 + 活动 + 繁忙度 + 诊断事实 |
| POST | `/models/config` | 保存勾选 / 前缀 / 回退 |
| POST | `/models/refresh` | 重拉目录 + 活动，顺带刷新繁忙度 |
| POST | `/models/test` | **单模型**真实体积探测；返回 `ok`/`kind`/`recover_at` |
| GET | `/pool` | 池状态（含每账号状态、积分、隔离、并发） |
| POST | `/pool/config` | `{min_available_accounts}` |
| POST | `/pool/check` | 强制刷新全部账号积分并重算 |
| POST | `/pool/quarantine` | `{auth_index, minutes, reason}` |
| POST | `/pool/release` | `{auth_index}` |
| GET | `/accounts` · `/concurrency` · `/credits` | 账号表 / 并发槽位 / 实时积分 |
| POST | `/refresh` · `/checkin` · `/trial` · `/import` | 刷新 / 签到 / 领试用 / 导入 |
| GET | `/keepalive/status` · `/dailybonus/status` | 保活与每日奖励状态 |
| GET | `/usage` · `/usage/summary` | 逐请求积分/Token 日志 |
| POST | `/oauth/region` | 切换 OAuth 签发区域 |
| GET | `/update/check` | 版本对比 |

---

## 面板

`/v0/resource/plugins/workbuddy/panel`，`go:embed` 进二进制。包含：

- **账号表**：积分、区域、并发、签到状态、今日用量
- **账号池区块**：可用/最低、进度条、逐账号池状态表、隔离/解除、池口径
- **模型管理**（国内/国际两个 tab）：模型列表 · 前缀 · 高级 · **诊断**
  - 模型行 8 列：勾选 / 模型 ID / 名称+徽标 / **推理档** / 上下文 / **预算** / 倍率 / 测试
  - 倍率显示折后价，原价用删除线保留；活动徽标是「此刻」的函数
  - 繁忙度图标：🚀 空闲 / 🧍 正常 / 🧍🧍 繁忙 / 🧍🧍🧍 拥堵 / 🚫 不可用；`unknown` 不画
  - **批量操作**：全部启用 / 全部停用 / 只启用免费 / 重置推理档
    （「全部」指当前筛选下可见的；「免费」依据上游 `x0.00`；都是草稿，需保存）
  - **刷新频率**：工具条右侧五档（5s/15s/30s/1m/5m），并发徽章与用量表共用
- **用量记录**：逐请求 Token / 积分，按账号与时间筛选

---

## 验证记录（2026-11-12，线上实测）

```
/v1/models : cnwb/ 50 个    intlwb/ 0 个（国际版无账号，按设计不发布）    合计 128
CN 目录    : 上游 62 + 目录兜底 6 = 68 行
             有倍率 44   有活动 8   有繁忙度 21   有推理档位 23
busy_at    : 2026-11-12T14:29:13Z（采集成功）
活动样例   : glm-5.2  夜间折扣  7:50–23:00 (Asia/Shanghai)
             hy3     限时免费  2026-07-06 — 2026-11-16
```

`gofmt -l` 无输出，`go vet` 干净，`go test -race -count=1 ./...` 全绿（52 个测试文件），
`CGO_ENABLED=1 go build -buildmode=c-shared` 通过，CI 在 GitHub 上同样四步全绿。

---

## 已知限制

- 依赖 WorkBuddy 客户端接口（非官方开放 API），上游调整时插件可能需要跟进。
- **国际版没有繁忙度**——上游国际网关不提供该接口，不是插件没做。
- **活动徽标只在 CN 出现**——`/v3/config` 的 `modelPromotions` 是 CN 通道下发的；国际版目录里没有这个字段。
- **测试按钮会真实花钱**（约 25k token 一次），所以没有批量版本，这是刻意的。
- 上游宣称 1M 上下文但节流线远低于此；插件按模型原生窗口向 CPA 宣称，长会话撞限流是这条机制的直接结果。
- 非流式请求会被上游拒绝（`11101 Non-stream chat request is currently not supported`），插件内部已强制流式。

---

## 致谢与来源

- [`zidanefaqih/codebuddy-intl-cpa`](https://github.com/zidanefaqih/codebuddy-intl-cpa)（MIT）——本分支的直接基座。
- `Sliverkiss/cpa-plugin`、`lovingfish` 的 workbuddy——上游更早的实现。
- `dingminhua/dsh-connect-workbuddy`（MIT）——**体验层的设计参照**：倍率随目录刷新、活动徽标是「此刻」的函数、
  繁忙度探针只在国内版、可用性测试必须发真实体积、诊断栏要能被整段粘贴、以及「`unknown` 不画」这条原则。
  本项目的实现是独立的（Go + CPA 插件 ABI），未复制其源码。

## License

MIT。
