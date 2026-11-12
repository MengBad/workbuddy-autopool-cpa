# G16 · Package layout note

## Decision (2026-07-23)

**No `internal/` package split** for the CPA c-shared plugin.

Reasons:

1. `-buildmode=c-shared` requires `package main` with exported C ABI entry
   points in the same main package.
2. Splitting into `internal/*` forces awkward re-exports or multi-package
   cgo glue for little gain.
3. Current modularization already uses **same-package multi-file**
   (42 non-test files, one responsibility each):
   - `main.go` — ABI, dispatch, registration, region constants
   - `oauth.go` — login flow; `payload.go` / `stream.go` — executor data plane
   - `management.go` — routes and auth; `panel.go` + `panel.html` — UI
   - `pool.go` — account pool; `models_config.go` — Model Manager
   - `*_test.go` — unit tests (52 files)

## Done instead of G16

- New domain files (`pool.go`, `promotions.go`, `queue.go`, `models_test_probe.go`)
  rather than growing only `main.go`
- Document boundaries in README

Full DDD `internal/` (as in cli-smart-router) is frozen unless CPA adds a
non-c-shared plugin packaging model.
