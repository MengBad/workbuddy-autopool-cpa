# 更新日志

本分支在 [`zidanefaqih/codebuddy-intl-cpa`](https://github.com/zidanefaqih/codebuddy-intl-cpa) v0.10.0 基础上继续维护，
从 v0.12.0 起由本仓库（[`MengBad/workbuddy-autopool-cpa`](https://github.com/MengBad/workbuddy-autopool-cpa)）发布。

## v0.14.2 (2026-11-12) — 表头与版本号

- **模型列表出现两行表头**。静态的 `.mheadrow` 写在 `.mlist` 外面，而 `mmRender()`
  每次又在列表内部渲染一份 sticky 表头，于是滚动区里同时存在两个表头、列宽还不同步。
  删掉静态那份，表头只在列表内渲染一次；同时把最后一列表头的文字补成「测试」
  （原来是个空 `<span>`，看起来像少了一列）。
- **`main.go` 的 `version` 常量还是 `0.14.0`**。CI 用 `-ldflags -X main.version=<tag>`
  覆盖，所以发布出来的二进制是对的，但 `make build` 本地构建会自称 0.14.0，
  与 `VERSION` 文件不一致。已对齐，`make check-registry` 现在也覆盖这条。

## v0.14.1 (2026-11-12) — 面板修复与发布准备

首个在 [`MengBad/workbuddy-autopool-cpa`](https://github.com/MengBad/workbuddy-autopool-cpa)
上发布的版本。功能与 v0.14.0 相同，修掉了三个面板缺陷并把仓库补成可长期维护的状态。

### 面板

- **账号池区块永远停在「加载中…」**。根因是脚本里的 temporal dead zone：
  `load()` 在脚本末尾执行，而 `lastPool` / `POLL_KEY` / `_concTimer` 等声明写在它之后，
  `restartPollers()` 在顶层被提前调用时抛 `Cannot access 'X' before initialization`，
  整个脚本中断，池区块和用量表都渲染不出来。修法是把状态声明前移、
  把 `restartPollers()` 的引导挪到脚本末尾（在所有声明之后），而不是加 try/catch 掩盖。
- **诊断栏「目录模型数 6」与 68 行表格自相矛盾**。`catalog` 统计的是只存在于
  归档目录里、上游已不再下发的兜底行，标成「目录模型数」会被读成「一共只有 6 个模型」。
  拆成三个独立事实：**上游模型数 / 目录兜底模型数 / 显示的模型数**。
- **注册相关的残留 UI 全部移除**。`自动补号` 开关、`立即注册国际账号`、注册会话卡
  和它们对应的 `poolRegister*` / `poolToggleAuto` / `poolCopySession` 函数在 0.12
  就已经没有后端，只是面板还在显示按钮、点了回 404。现在账号池区块只剩
  最低可用选择、隔离/解除、刷新，以及一个跳转到导入弹窗的「＋ 导入账号」。
  池子不够时的文案改成「请从面板导入凭据补足。插件不会自己创建账号。」——
  这句话现在是真的。

### 仓库

- 新增 `scripts/`：`check-registry.py`（registry 与 VERSION 一致性，进 CI）、
  `compare-cache-traces.py` / `study-cache-misses.py` / `cache_trace_analysis.py`
  （`docs/cache-tracing.md` 一直在让人跑这几个脚本，但它们不在仓库里）、
  `check-darwin-dylib.py`。
- `docs/architecture.md` 的文件职责表重写为真实的 42 个文件分组
  （旧表还写着 `executor.go` / `scheduler.go` / `auth_parse.go`，这些文件 0.12 就不存在了）。
- `docs/development.md`、`docs/release-checklist.md` 按本仓库的真实布局重写
  （根目录就是插件包、单 `main` 分支 + tag、CI 实际跑的步骤）。
- `CONTRIBUTING.md`、`SECURITY.md`、`logo.png`、README 徽章与目录索引。


## v0.14.0 (2026-11-12) — 命名体系

**默认前缀改为 `cnwb/` 与 `intlwb/`**，两个区域都默认启用。

| 区域 | 旧默认 | 新默认 |
|---|---|---|
| 国内 (CN) | `cn/` | `cnwb/` |
| 国际 (Global) | 无前缀（裸名） | `intlwb/` |

- 国际版从「裸名」改为「默认带前缀」，这是本次最实质的改动。
  裸名会和任何注册了同名上游模型的 provider 撞车——实测裸名 `glm-5.3` 被路由到了
  Fireworks 直连条目，返回 `accounts/fireworks/models/glm-5p3`，**根本没走本插件的账号**。
  这类冲突无法从插件侧修复，只能靠命名空间隔离。
- 旧前缀 `cn/` 与 `cb/` 保留在**归属判定**里（`legacyPrefixCN` / `legacyPrefixGlobal`），
  已写进客户端配置的旧模型 id 仍能正确解析；插件只是不再**发布**它们。
- `defaultSettings()` 改为取 `mmDefaultPrefix(realm)`，不再硬编码字符串——默认值只有一个出处。

## v0.13.0 (2026-11-12) — 交互与显示

参照 `dingminhua/dsh-connect-workbuddy` 的体验设计，用 Go + CPA 插件 ABI 独立实现。

- **逐模型推理档位选择器**：解析上游 `reasoning.supportedEfforts` / `defaultEffort`。
  档位集合按模型不同（`["low","high","max"]` 6 个、`["low","high"]` 4 个、
  `["high","xhigh"]` 3 个、五档 2 个、只有 `["high"]` 11 个），所以是逐模型菜单。
  保存到 `realm.<realm>.effort_default.<modelId>`，经 `applyChosenEffort` 真正写入
  `reasoning_effort`；只写模型自己声明过的档位（否则上游 400），且只对客户端未指定档位的
  请求生效。
- **上下文预算**：原生 / 200K / 500K。选「原生」是删除覆盖项而不是写入今天的窗口数字，
  这样上游日后调大窗口会自动跟上。
- **排队位次提示**（`queue.go`）：识别 `6020`/`6021`/`6022`，输出
  「你排在第 8777 位，队列共 9021 个请求」。**排队不换号**（队列是模型级+平台级的，
  换号还是同一位次，当成账号失败会把健康账号误打入冷却），**不自动重试**。
- **刷新频率可配**：五档（5s/15s/30s/1m/5m），并发徽章与用量表共用同一个值。
- **批量操作**：全部启用 / 全部停用 / 只启用免费 / 重置推理档（「全部」指当前筛选下可见的）。
- 修复 `.mrow` 网格从 6 列扩到 8 列；修复 `recordModelEfforts` 有定义无调用导致
  档位设置不生效。

## v0.12.0 (2026-11-12) — 转向维护

- **移除自动注册**：删除 `autoregister.go`、5 个 `/pool/register*` 端点、`auto_register`
  配置项、注册会话状态机与审计历史。账号不足时只在日志说明一次。
  上游创建账号必须过一次浏览器 OAuth，是人工步骤，自动化只会产生卡住的 pending 会话。
- **新增活动徽标**：解析 `modelPromotions`，支持 daily 时间窗（带时区、跨零点）与
  `validFrom/validUntil` 有效期；`factor=0` 识别为限时免费；按 priority 选当前生效的一条。
- **新增繁忙度探针**：`POST /v2/chat/busy`（仅国内版，一次 ≤50 id，免费只读）。
  `unknown` 一律不渲染——「没被告知」不等于「一切正常」。等级只活内存不落盘。
- **可用性测试升级**：从 32 token 小请求改为**真实体积（约 25k 输入 token）**，
  因为上游按请求体积限流；新增 `kind` 分类与 `recover_at`（从响应体解析上游写明的
  重置时刻，上游没给就如实说「未给出」而不是编倒计时）。
- **诊断栏**：模型设置新增只读事实表 + 一键复制全文。
- **新增 `session_dead` 状态**：401/12153 的账号显示「token 被上游拒绝，需重新登录」，
  而不是笼统的「积分未知」。
- **拆分「周期额度」与「总量池」**：`packageSummary` 同时给出 `remain`（本周期可用）和
  `pool_remain`（总容量池），并标记 `cycle_exhausted`。上游按周期判额度，一个包可能
  「池里还剩 500」但**一次都调不通**，这两个数字必须分开显示。
- **可用性语义收紧**：读不到余额本身是信号（`credits_err`），不再乐观当成可用。
