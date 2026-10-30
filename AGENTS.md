# AGENTS.md

## Repo Shape
- Single-package Go CLI at the repo root; `main.go` contains argument parsing, execution flow, preprocessing, and diff backend handling.
- Unit tests live in `main_test.go` and cover argument parsing, input resolution, rule selection/applicability, staging, color injection, shell quoting, and key `run` branches.
- `go.mod` declares module `github.com/tlil/diffhance` and Go `1.26`; there are no third-party Go dependencies or `go.sum`.
- The built local binary name `diffhance` is ignored by `.gitignore`; avoid committing binaries or `dist/` artifacts.

## Commands
- Format after Go edits: `gofmt -w main.go main_test.go`.
- Run the unit test suite and compile-check everything: `go test ./...`.
- When test behaviour changes, use `go test -cover ./...` instead; it runs the same full suite and reports statement coverage, so a separate plain test run is unnecessary.
- Build local CLI: `go build -o diffhance .`.
- When checking release-build behaviour, match CI release-style build: `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o diffhance .`.

## Verification And Landing
- Retain the cheap full suite for Go changes, including exit-code, shell, Git and backend regressions; do not replace it with selective tests. Documentation-only edits do not require Go tests or builds.
- Use native tests and a local build as appropriate during iteration. Do not replay CI's platform/release matrix locally for unrelated edits; platform-sensitive or release-build changes warrant the relevant additional checks. Keep CI's full matrix and release checks.
- Record successful verification commands and the revision or worktree state tested. Reuse those results when landing if the relevant code, configuration and toolchain inputs are unchanged; committing alone does not require rerunning them.
- After a rebase, inspect upstream changes and conflict resolutions and rerun checks whose inputs changed. Follow required repository and upstream checks, confirm required CI outcomes for the landed revision, and report any verification omissions accurately.
- Preserve the existing guarded toolchain installation and download cache. Do not add a cache layer or clear caches or snapshots without evidence of a problem.

## Behavior To Preserve
- Exit codes intentionally mirror `diff(1)`: `0` identical, `1` different, `>1` error.
- In `--git` mode, a backend diff exit code of `1` is swallowed to `0` because Git treats non-zero external diff exits as fatal.
- Preprocessors and diff backends run through a shell: `/bin/sh -c` except Windows, where `ComSpec /C` or `cmd /C` is used.
- `--rule GLOB:CMD` matches only `filepath.Base(displayPath)`; all matching rules are applied in resolved order as a shell pipeline; explicit `--pre` / `--pre-left` / `--pre-right` override rules.
- Staged temp paths preserve safe relative `displayPath` directories under `l/` and `r/` so diff headers remain meaningful; unsafe or unrelated absolute display paths fall back to a basename.
- `--config PATH` reads rules as one `GLOB:CMD` per line, ignoring blank lines and lines starting with `#`; config rules are inserted where the flag appears.
- Existing default rules configs are auto-loaded from platform config locations as `diffhance/rules` and appended after explicit `--rule` / `--config` rules; missing default config files are ignored.
- `--print` keeps temp files and prints their paths separated by a tab; normal diff mode deletes temp files on exit.
- `--print-rules` prints the resolved rules to stdout, one per line, after explicit and default configs are loaded; it exits without requiring positional file args or running preprocess/diff.
- `--print-config-dirs` prints the default config file paths checked, one per line, without reading them; it exits without requiring positional file args or running preprocess/diff.
- Default diff backend is `diff -u`; color injection only appends `--color=always` for bare `diff` when stdout is a terminal and `NO_COLOR` is unset.
- Bare `diff` backends get `--label l/... --label r/...` for clean unified diff headers; `delta` backends receive a labeled `diff -u` stream; other non-Hunk custom backends and already-labeled diff commands are left unchanged.
- Bare `hunk` backends expand to `hunk difftool -- LEFT RIGHT PATH`; explicit `hunk difftool` options are preserved, staged paths supply the content, and the unsanitized right display path supplies `PATH`. Diffhance compares the staged files for its `0`/`1` result because Hunk uses exit `1` for application errors, and the `git` shorthand disables Git's pager for direct Hunk backends so Hunk retains terminal access.
- `diffhance [flags] git [args...]` (first positional arg is the literal `git`) runs `git diff [args...]` with `GIT_EXTERNAL_DIFF` set to the current binary (`os.Executable()`) in `--git` mode; flags before `git` are forwarded into that command with POSIX single-quote quoting (git always uses a POSIX shell for external diffs, even on Windows), git's exit code is propagated, and `--` before `git` disables the shorthand.

## CI
- `.github/workflows/build.yml` runs `go test ./...` in a dedicated `test` job before building binaries.
- CI builds `linux-{amd64,arm64}`, `windows-{amd64,arm64}`, and `darwin-arm64` with `CGO_ENABLED=0` and uploads artifacts from `dist/<target>/`.
