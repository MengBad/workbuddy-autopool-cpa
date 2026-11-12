# WorkBuddy Plugin Architecture

Module map and data flow for the workbuddy plugin. The plugin is a single
`package main` compiled as a c-shared `.so`, loaded by CPA at startup and
driven via the `pluginabi` RPC interface.

## Capability surface (declared in `wbRegistration`)

| Capability | Implementation file | What it does |
|---|---|---|
| `ModelProvider` | `models.go`, `models_config.go`, `models_integrate.go` | Static + dynamic model list, alias reverse-resolution, `oauth-excluded-models` filter |
| `AuthProvider` | `oauth.go`, `authfile.go` | OAuth login flow (CN + Global), token refresh, auth file parse |
| `Executor` | `main.go`, `stream.go`, `payload.go`, `retry.go` | Chat completions, streaming SSE pump, request body rewriting |
| `ManagementAPI` | `management.go`, `panel.go`, `checkin.go`, `credits_handler.go`, `pool_handlers.go`, `models_config.go`, `billing.go`, `usage_config.go` | Dashboard, manual check-in, credits query, import credential, config |
| `UsagePlugin` | `usage.go` | Forward every request's usage record to CPAMP |

## File map (by responsibility)

42 non-test files, ~27.9k lines. One responsibility per file.

### Entry, ABI, config

| File | Responsibility |
|---|---|
| `main.go` | C ABI exports, `handleMethod` dispatch, `wbRegistration` (capabilities + ConfigField), metadata, region constants |
| `usage_config.go` | decode plugin config from `config_yaml` on every register/reconfigure; resolve the CPAMP usage report URL/key; lock-protected config snapshot |
| `update_check.go` | `GET /update/check`: release registry vs build version, 6h cache, silent fail, "skip this version" |
| `redact.go` | `redactSecrets` — every string that can reach a log/error/CPAMP body goes through it |

### Model surface

| File | Responsibility |
|---|---|
| `models.go` | ModelProvider: static + per-auth lists, dynamic discovery, alias reverse resolution, `oauth-excluded-models` |
| `models_config.go` | Model Manager backend: catalog (`modelDef`), per-realm settings, model rows with multiplier / promo / busy / efforts / budget |
| `models_integrate.go` | glue: upstream refresh fan-out, prefix↔realm resolution (`mmDefaultPrefix`), legacy prefix handling, collision validation |
| `promotions.go` | `modelPromotions` parsing + time-window evaluation; per-model busy levels (`/v2/chat/busy`, 3-min TTL, CN only) |
| `models_test_probe.go` | `POST /models/test`: one real ~25k-token probe, classify failure, parse the reset instant out of the upstream message |
| `account_tier.go` | plan tier (free vs paid) for model gating (`11158`) |

### Executor and data plane

| File | Responsibility |
|---|---|
| `payload.go` | `prepareUpstreamBody` single JSON pass: force stream, normalize tools, rewrite system, ensure system message, rewrite model, inject chosen effort |
| `stream.go` | upstream SSE data plane: emit/close, pump with failover, synchronous collect, frame re-shaping |
| `sse_reader.go` | bounded SSE line scanner over the host stream reader |
| `retry.go` | executor-side failover loop: 429 / 6004 / 14018 → retry the same request on another enabled account |
| `failover.go` | non-destructive multi-account cooldowns keyed by account |
| `queue.go` | `6020/6021/6022` queue rejections → operator-readable text; queue never triggers an account swap |
| `client_identity.go` | per-realm client identity headers (`11128` when missing) |
| `attempt_admission.go` | per-account concurrency bound without a local waiting queue |
| `session_affinity.go` | soft session affinity: prefer the last executing account, move to a free same-realm peer |
| `request_roles.go` | request role classification (main / compaction / title) |
| `request_usage.go` | per-request JSONL credit+token log and its query surface |
| `cache_trace.go` | content-free request fingerprints alongside accounting |

### Accounts, pool, lifecycle

| File | Responsibility |
|---|---|
| `pool.go` | account pool: states, credits, quarantine, minimum-available guarantee, persistence keyed by UID |
| `pool_handlers.go` | `/pool*` management endpoints (kept separate so the feature is reviewable in one place) |
| `lifecycle.go` | credit-driven auth lifecycle: CN disable/re-enable, Global delete, unknown credits → no-op |
| `policy.go` | pure decision layer for lifecycle actions (no I/O) |
| `authfile.go` | auth file naming, UID sanitization (path-traversal defense), host.auth read/write/delete |
| `host_auth.go` | host auth-store RPC wrappers (`host.auth.list/get/get_bundle`) |
| `auth_lock.go` | per-account lock set |
| `account_warning.go` | risk-control warnings (`11140`) surfaced separately from credit exhaustion |
| `checkin.go` | CN daily check-in: manual endpoint, 09:00/21:00 scheduler, per-account mutex |
| `dailybonus.go` | Global daily 30-credit reward via the growth centre (idempotent per account per day) |
| `keepalive.go` | daily 22:00 access-token refresh (`X-Refresh-Token` header) |
| `billing.go` | upstream billing surface: check-in status, user resource, payment type, perform-* wrappers |
| `credits_handler.go` | import / check-in toggle / trial / select-auth / credits query endpoints |
| `cache.go` | per-account plan/checkin/credits cache + singleflight |
| `expiry` (in `authfile.go`/`billing.go`) | token expiry decisions |

### OAuth, transport, panel

| File | Responsibility |
|---|---|
| `oauth.go` | browser-driven OAuth login (CN + Global), state polling, token refresh, isolated cookie jars |
| `host_bridge.go` | every upstream HTTP call through the CPA host bridge; `*Direct` variants are the test-only fallback |
| `usage.go` | UsagePlugin: forward the canonical record to CPAMP |
| `management.go` | management route table, plugin-layer auth (constant-time Bearer + per-IP token bucket) |
| `panel.go` | `buildDashboardEx`, credit summarisation, panel resource serving |
| `panel.html` | the embedded panel (~3.9k lines, `go:embed`) |

## Data flow

### Chat completion (streaming)

```
client → CPA → plugin.handleExecStream
  → parseStored(auth file)
  → resolveUpstreamModel(alias → upstream id)
  → prepareUpstreamBody (single JSON pass: forceStream + normalizeTools +
                          rewriteSystem + ensureSystemMessage + rewriteModel +
                          applyChosenEffort)
  → hostHTTPDoStream (via CPA host bridge → request-log captured)
  → pumpUpstreamStream (goroutine)
      → hostStreamReader → bufio.Scanner → SSE lines
      → cleanChunkJSON per line
      → streamEmit → CPA → client
      → sseUsageCollector collects terminal usage object
      → queue.go classifies 6020/6021/6022 before any failover decision
  → publishUsage → forwardUsageToCPAMP (async, via host bridge)
  → invalidateAccountCredits (async)
  → host calls UsagePlugin.HandleUsage → handleUsage → forwardUsageToCPAMP (sync)
```

### Daily check-in (CN, 09:00 / 21:00)

```
schedulerLoop → runAutoCheckin (sem=4 concurrent)
  → processAutoCheckinAccount per account
      → fetchCheckinStatus → performCheckinCall if needed
      → update accountCache (merge, not wipe)
      → reconcileOneAccount → applyExhaustedPolicy
          → policy.go decides: disable (CN) / delete (Global) / reenable (CN)
          → authfile.go applies: hostAuthPersist / deleteAuth
```

### Dashboard load

```
panel.html → /v0/management/plugins/workbuddy/accounts
  → handleManagement (auth + ratelimit)
  → buildDashboardEx (concurrent cachedAccountDetails per account, sem=4)
      → accountDetailFlight singleflight dedups concurrent fetches
      → accountCache hit → return cached
      → miss → 3 concurrent billing API calls (plan/checkin/credits)
  → summarizeCredits
```

## Key design decisions

1. **Host HTTP bridge for all upstream calls.** Every HTTP request to
   CodeBuddy / CPAMP goes through `host.http.do` / `host.http.do_stream` so
   CPA's request-log captures outbound traffic and host transport policy
   (proxy, timeout) applies. The plugin's own `sharedHTTPClient` is a
   fallback used only when the bridge is unavailable (unit tests, hosts
   older than v7.2.x).

2. **Single-flight per account for billing API.** `cachedAccountDetails`
   uses a `sync.Map` of in-flight calls so concurrent dashboard refreshes
   and reconcile ticks for the same account share one upstream fetch
   instead of stampeding the billing API.

3. **Cache merge, never wipe.** All cache writes merge with the previous
   entry (credits + plan + checkin) instead of replacing it. The "early
   already checked in" fast path used to wipe credits/plan; v0.6.31 fixed
   that by always merging.

4. **UID whitelist for auth file names.** `sanitizeUIDForFileName` strips
   any character outside `[a-zA-Z0-9_-]` and caps length at 64, preventing
   path traversal when importing credentials with attacker-controlled UIDs.

5. **Plugin-layer management auth is opt-in.** When `management_key` is
   unset the plugin defers entirely to CPA's management middleware
   (historical default). When set, mutating endpoints require a constant-time
   Bearer match plus a per-IP token bucket.

6. **Routing is per session, never a global selection.** The plugin does not
   register a `scheduler.pick` capability, so CPA's built-in scheduler makes
   the initial account pick. The executor then enforces per-session affinity
   (`session_affinity.go`): a conversation keeps its last executing account
   while a slot is free and moves to a free same-realm peer when it is not.
   There is no panel-selected "active" account — one global choice used to leak
   across sessions, so a failover in one conversation moved another
   conversation onto an unrelated account and re-wrote its prompt cache.
   Bindings persist to `session-affinity.json` so a restart does not move a
   conversation.

7. **No goroutine leaks across hot-reload.** The scheduler loop uses a
   `schedulerStop` channel and is idempotent. The plugin's `Shutdown` is a
   deliberate no-op because c-shared runtime teardown races with Go sync
   primitives (SIGSEGV) — `dlclose` cleans up the whole runtime anyway.

## Integration points with CPA

- **Auth store**: `host.auth.list` / `host.auth.get` / `host.auth.save` —
  plugin never writes auth files directly to disk, always via host RPC.
- **Model registration**: `model.static` / `model.for_auth` RPC, plus
  `oauth-model-alias` / `oauth-excluded-models` from host config.
- **Streaming**: `host.stream.emit` / `host.stream.close` — async SSE
  chunks pushed to the client without blocking the executor return.
- **Usage**: `usage.handle` RPC — host calls `UsagePlugin.HandleUsage`
  after every request with a canonical `pluginapi.UsageRecord`.
- **Management**: `management.register` returns routes under
  `/v0/management/plugins/workbuddy/*` and a panel resource under
  `/v0/resource/plugins/workbuddy/panel`.
