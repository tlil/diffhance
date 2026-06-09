# AGENTS.md

## Repo Shape
- Single-package Go CLI at the repo root; `main.go` contains argument parsing, execution flow, preprocessing, and diff backend handling.
- Unit tests live in `main_test.go` and cover argument parsing, input resolution, rule selection/applicability, staging, color injection, shell quoting, and key `run` branches.
- `go.mod` declares module `github.com/tlil/diffhance` and Go `1.26`; there are no third-party Go dependencies or `go.sum`.
- The built local binary name `diffhance` is ignored by `.gitignore`; avoid committing binaries or `dist/` artifacts.

## Commands
- Format after Go edits: `gofmt -w main.go main_test.go`.
- Run the unit test suite and compile-check everything: `go test ./...`.
- Check statement coverage when test behavior changes: `go test -cover ./...`.
- Build local CLI: `go build -o diffhance .`.
- Match CI release-style build: `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o diffhance .`.

## Behavior To Preserve
- Exit codes intentionally mirror `diff(1)`: `0` identical, `1` different, `>1` error.
- In `--git` mode, a backend diff exit code of `1` is swallowed to `0` because Git treats non-zero external diff exits as fatal.
- Preprocessors and diff backends run through a shell: `/bin/sh -c` except Windows, where `ComSpec /C` or `cmd /C` is used.
- `--rule GLOB:CMD` matches only `filepath.Base(displayPath)` and first match wins; explicit `--pre` / `--pre-left` / `--pre-right` override rules.
- `--print` keeps temp files and prints their paths separated by a tab; normal diff mode deletes temp files on exit.
- Default diff backend is `diff -u`; color injection only appends `--color=always` for bare `diff` when stdout is a terminal and `NO_COLOR` is unset.

## CI
- `.github/workflows/build.yml` runs `go test ./...` in a dedicated `test` job before building binaries.
- CI builds `linux-{amd64,arm64}`, `windows-{amd64,arm64}`, and `darwin-arm64` with `CGO_ENABLED=0` and uploads artifacts from `dist/<target>/`.
