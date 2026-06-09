# diffhance

A tiny, cross-platform Go utility that preprocesses each side of a diff
before diffing — so a noisy single-line JSON diff becomes a clean,
field-by-field diff, an unformatted XML diff becomes a structural diff,
and so on.

It follows the UNIX philosophy: it does one thing, it composes with
`git`, `diff`, `delta`, `difft`, and friends, and it exits with the same
codes `diff(1)` does (0 = identical, 1 = differ, ≥2 = error).

## Install

```sh
go install github.com/tlil/diffhance@latest
# or, from a clone:
go build -o diffhance .
```

No runtime dependencies. Pure Go stdlib. Works on macOS, Linux, and Windows
(`cmd /C` is used as the shell on Windows, `/bin/sh -c` everywhere else).

## Quick start

```sh
# Diff two JSON files after running jq on both
diffhance --pre 'jq -S .' old.json new.json

# Pipe through delta for a pretty side-by-side view
diffhance --pre 'jq -S .' -d 'delta --paging=never' old.json new.json

# Only preprocess files whose basename matches a glob
diffhance -r '*.json:jq -S .' -r '*.xml:xmllint --format -' a b

# Load preprocessing rules from a config file
diffhance --config ~/.config/diffhance/rules a.json b.json

# Inspect the resolved rule set in application order
diffhance --print-rules

# Inspect default config file locations
diffhance --print-config-dirs

# Just emit the preprocessed file paths and pipe them yourself
read L R < <(diffhance --print --pre 'jq -S .' a.json b.json)
diff -u "$L" "$R"
```

## Use as a `git` external diff driver

`git` invokes its external diff with 7 positional args
(`path old-file old-hex old-mode new-file new-hex new-mode`).
The `--git` flag tells diffhance to interpret them.

```sh
# One-off (note: GIT_EXTERNAL_DIFF is split on whitespace by git, so the
# diffhance binary must live at a path without spaces — symlink it if needed)
GIT_EXTERNAL_DIFF="diffhance --git --rule '*.json:jq -S .'" git diff HEAD~1 HEAD

# Pipe the result through delta
GIT_EXTERNAL_DIFF="diffhance --git -r '*.json:jq -S .'" git diff HEAD~1 HEAD | delta
```

In `--git` mode diffhance always exits 0 (even when the files differ),
because git treats any non-zero exit from an external diff as fatal.

## Flags

| Flag                                | Description                                                                                                              |
| ----------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| `-p, --pre CMD`                     | Shell pipeline applied to both sides (file streamed on stdin)                                                            |
| `--pre-left CMD`, `--pre-right CMD` | Per-side overrides                                                                                                       |
| `-r, --rule GLOB:CMD`               | Apply `CMD` when the file's basename matches `GLOB` (repeatable, first match wins). Ignored if `--pre`/`--pre-*` is set. |
| `-c, --config PATH`                 | Read rules from `PATH`, one `GLOB:CMD` per line. Blank lines and lines starting with `#` are ignored.                    |
| `-d, --diff CMD`                    | Diff backend (default `diff -u`). The two preprocessed paths are appended as the last two positional args.               |
| `--git`                             | Treat positional args as git's external-diff invocation                                                                  |
| `--print`                           | Skip diffing; print `LEFT-PATH\tRIGHT-PATH` of the preprocessed files                                                    |
| `--print-rules`                     | Print resolved rules to stdout, one per line, then exit                                                                  |
| `--print-config-dirs`               | Print default config file locations checked, one per line, then exit                                                     |
| `--no-color`                        | Don't auto-inject `--color=always` into the default `diff` backend                                                       |
| `-h, --help`                        | Show help                                                                                                                |

## Rules config

Rules config files use the same `GLOB:CMD` format as `--rule`, one rule per
line. Existing default configs are loaded automatically from `diffhance/rules`
under the platform config directories, then appended after any explicit
`--rule` or `--config` rules.

Default locations:

| Platform | Locations |
| -------- | --------- |
| macOS | `~/Library/Application Support/diffhance/rules`, `~/.config/diffhance/rules`, `/Library/Application Support/diffhance/rules` |
| Linux/Unix | `$XDG_CONFIG_HOME/diffhance/rules` or `~/.config/diffhance/rules`, plus each `$XDG_CONFIG_DIRS/diffhance/rules` or `/etc/xdg/diffhance/rules` |
| Windows | `%AppData%\diffhance\rules`, `%ProgramData%\diffhance\rules` |

## How preprocessing works

Each side is streamed through the preprocessor as
`sh -c "<CMD>"` (or `cmd /C` on Windows) with the source file on
stdin and the captured stdout written to a temp file. That means any
pipeline works:

```sh
diffhance --pre 'jq -S . | grep -v "id":' a.json b.json
diffhance -r '*.proto:buf format -' a.proto b.proto
diffhance -r '*.png:identify -verbose -' a.png b.png
```

Temp files are deleted on exit unless `--print` is given.

## License

MIT
