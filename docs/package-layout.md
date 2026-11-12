# Package layout

## The constraint

`-buildmode=c-shared` requires `package main` with the exported C ABI entry
points in the **same package**. Go does not allow one package to span
directories, so every `.go` file of this plugin lives in the repository root.
That is not sloppiness — moving `executor.go` into `internal/executor/` would
mean re-exporting it across a package boundary, which for a c-shared plugin
buys nothing and breaks `//export`.

## How the root is kept navigable

- **One responsibility per file.** 50 source files, largest is
  `models_config.go` (~1.8k lines, the Model Manager backend). Everything else
  is well under 1k.
- **`main.go` is only the boundary.** It is the single file that imports `"C"`
  and therefore the only place the host function-pointer table can live. It
  holds the ABI exports, `hostCall`, `handleMethod` and `writeResponse` — no
  business logic. It went from 1154 lines to ~230 in the v0.15 split.
- **Every file starts with a one-line purpose comment**, so `ls` plus the first
  line of each file is a table of contents.
- **Every `*_test.go` is named after the file it exercises** (with an optional
  behaviour suffix). A source file with no matching test file is visibly
  untested.
- **Non-Go assets live in their own directories**: `web/` (the embedded panel),
  `docs/`, `scripts/`.

## What was rejected

- `internal/*` package split: see above.
- Moving files into `cmd/` / `pkg/`: same package-boundary problem.
- A `//go:generate` that copies sources around at build time: two copies of
  every file, and the copy is what the compiler sees.
