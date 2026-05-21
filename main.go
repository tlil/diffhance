// diffhance preprocesses each side of a diff before handing the result
// to a diff tool. It is designed to compose with git, diff, delta, etc.
//
//	diffhance --pre 'jq .' a.json b.json
//	diffhance --rule '*.json:jq .' --rule '*.xml:xmllint --format -' a b
//	GIT_EXTERNAL_DIFF='diffhance --git --rule "*.json:jq ."' git diff HEAD~1
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const usage = `diffhance - preprocess each side of a diff before diffing

USAGE
  diffhance [flags] LEFT RIGHT
  diffhance --git [flags] PATH OLD-FILE OLD-HEX OLD-MODE NEW-FILE NEW-HEX NEW-MODE [RENAME-TO RENAME-MODE]

FLAGS
  -p, --pre CMD            Shell pipeline applied to BOTH sides (file streamed on stdin)
      --pre-left CMD       Shell pipeline applied to LEFT side only
      --pre-right CMD      Shell pipeline applied to RIGHT side only
  -r, --rule GLOB:CMD      Apply CMD when the file's basename matches GLOB (repeatable).
                           First matching rule wins. Ignored when --pre/--pre-* is set.
  -d, --diff CMD           Diff backend (default: "diff -u"). Receives preprocessed
                           LEFT and RIGHT as the last two positional args.
      --git                Treat positional args as git's external-diff invocation:
                           PATH OLD-FILE OLD-HEX OLD-MODE NEW-FILE NEW-HEX NEW-MODE ...
      --print              Skip diffing. Print "LEFT-PATH\tRIGHT-PATH" of the
                           preprocessed files (kept on disk) for downstream tools.
      --no-color           Do not auto-add color flags to the default diff backend.
  -h, --help               Show this help and exit.

EXAMPLES
  # Diff two JSON files after running jq on both
  diffhance --pre 'jq .' old.json new.json

  # Use delta as the renderer
  diffhance --pre 'jq .' -d delta old.json new.json

  # Per-extension rules
  diffhance -r '*.json:jq .' -r '*.xml:xmllint --format -' a/ b/   # (per file)

  # Pipe two preprocessed files into your own pipeline
  read L R < <(diffhance --print --pre 'jq .' a.json b.json); diff -u "$L" "$R"

  # Use as a git external diff
  GIT_EXTERNAL_DIFF='diffhance --git --rule "*.json:jq ." -d "diff -u"' \
      git diff HEAD~1 HEAD

EXIT STATUS
  Mirrors the diff backend (0 = identical, 1 = differ, >1 = error).
`

type stringSlice []string

func (s *stringSlice) String() string     { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }

type options struct {
	pre, preLeft, preRight string
	diff                   string
	rules                  []string
	git                    bool
	printPaths             bool
	noColor                bool
	args                   []string
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		if errors.Is(err, errHelp) {
			fmt.Fprint(os.Stdout, usage)
			return
		}
		fmt.Fprintf(os.Stderr, "diffhance: %v\n\n%s", err, usage)
		os.Exit(2)
	}
	code, err := run(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "diffhance: %v\n", err)
		if code == 0 {
			code = 2
		}
	}
	os.Exit(code)
}

var errHelp = errors.New("help requested")

// parseArgs implements a small POSIX-ish flag parser so we can support both
// "--pre CMD" and "--pre=CMD" plus short forms like "-p CMD" / "-pCMD",
// without taking on a third-party dependency.
func parseArgs(argv []string) (*options, error) {
	o := &options{diff: ""}

	take := func(i *int, name string) (string, error) {
		if *i+1 >= len(argv) {
			return "", fmt.Errorf("flag %s requires a value", name)
		}
		*i++
		return argv[*i], nil
	}

	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--":
			o.args = append(o.args, argv[i+1:]...)
			i = len(argv)
		case a == "-h" || a == "--help":
			return nil, errHelp
		case a == "--git":
			o.git = true
		case a == "--print":
			o.printPaths = true
		case a == "--no-color":
			o.noColor = true
		case a == "-p", a == "--pre":
			v, err := take(&i, a)
			if err != nil {
				return nil, err
			}
			o.pre = v
		case strings.HasPrefix(a, "--pre="):
			o.pre = strings.TrimPrefix(a, "--pre=")
		case strings.HasPrefix(a, "-p") && len(a) > 2:
			o.pre = a[2:]
		case a == "--pre-left":
			v, err := take(&i, a)
			if err != nil {
				return nil, err
			}
			o.preLeft = v
		case strings.HasPrefix(a, "--pre-left="):
			o.preLeft = strings.TrimPrefix(a, "--pre-left=")
		case a == "--pre-right":
			v, err := take(&i, a)
			if err != nil {
				return nil, err
			}
			o.preRight = v
		case strings.HasPrefix(a, "--pre-right="):
			o.preRight = strings.TrimPrefix(a, "--pre-right=")
		case a == "-d", a == "--diff":
			v, err := take(&i, a)
			if err != nil {
				return nil, err
			}
			o.diff = v
		case strings.HasPrefix(a, "--diff="):
			o.diff = strings.TrimPrefix(a, "--diff=")
		case strings.HasPrefix(a, "-d") && len(a) > 2:
			o.diff = a[2:]
		case a == "-r", a == "--rule":
			v, err := take(&i, a)
			if err != nil {
				return nil, err
			}
			o.rules = append(o.rules, v)
		case strings.HasPrefix(a, "--rule="):
			o.rules = append(o.rules, strings.TrimPrefix(a, "--rule="))
		case strings.HasPrefix(a, "-r") && len(a) > 2:
			o.rules = append(o.rules, a[2:])
		case strings.HasPrefix(a, "-") && a != "-":
			return nil, fmt.Errorf("unknown flag %q", a)
		default:
			o.args = append(o.args, a)
		}
	}

	if o.diff == "" {
		o.diff = "diff -u"
	}
	return o, nil
}

func run(o *options) (int, error) {
	leftPath, rightPath, displayPath, err := resolveInputs(o)
	if err != nil {
		return 2, err
	}

	leftCmd := pickPre(o, displayPath, side("left"))
	rightCmd := pickPre(o, displayPath, side("right"))

	tmpDir, err := os.MkdirTemp("", "diffhance-")
	if err != nil {
		return 2, err
	}
	if !o.printPaths {
		defer os.RemoveAll(tmpDir)
	}

	leftOut, err := stage(tmpDir, "left", displayPath, leftPath, leftCmd)
	if err != nil {
		return 2, fmt.Errorf("preprocess left: %w", err)
	}
	rightOut, err := stage(tmpDir, "right", displayPath, rightPath, rightCmd)
	if err != nil {
		return 2, fmt.Errorf("preprocess right: %w", err)
	}

	if o.printPaths {
		fmt.Printf("%s\t%s\n", leftOut, rightOut)
		return 0, nil
	}

	code, err := runDiff(o, leftOut, rightOut)
	// Git treats any non-zero exit from GIT_EXTERNAL_DIFF as fatal. The "files
	// differ" signal (exit 1) is meaningless to git here — it already knew
	// they differ, that's why it invoked us — so swallow it in --git mode.
	if o.git && err == nil && code == 1 {
		code = 0
	}
	return code, err
}

type side string

// resolveInputs returns left, right, and a "display" path used for rule
// matching. In --git mode the display path is the repo-relative path that
// git provides as argv[0].
func resolveInputs(o *options) (left, right, display string, err error) {
	if o.git {
		if len(o.args) < 7 {
			return "", "", "", fmt.Errorf("--git expects at least 7 positional args, got %d", len(o.args))
		}
		// path old-file old-hex old-mode new-file new-hex new-mode [rename-to rename-mode]
		return o.args[1], o.args[4], o.args[0], nil
	}
	if len(o.args) != 2 {
		return "", "", "", fmt.Errorf("expected exactly 2 positional args (LEFT RIGHT), got %d", len(o.args))
	}
	// When the two sides have the same basename, use it for rule matching;
	// otherwise fall back to the left side.
	lb, rb := filepath.Base(o.args[0]), filepath.Base(o.args[1])
	disp := o.args[0]
	if lb == rb {
		disp = lb
	}
	return o.args[0], o.args[1], disp, nil
}

func pickPre(o *options, displayPath string, s side) string {
	switch s {
	case "left":
		if o.preLeft != "" {
			return o.preLeft
		}
	case "right":
		if o.preRight != "" {
			return o.preRight
		}
	}
	if o.pre != "" {
		return o.pre
	}
	base := filepath.Base(displayPath)
	for _, r := range o.rules {
		glob, cmd, ok := strings.Cut(r, ":")
		if !ok {
			continue
		}
		match, err := filepath.Match(glob, base)
		if err == nil && match {
			return cmd
		}
	}
	return ""
}

// stage either copies src into tmpDir or streams it through cmd, returning
// the path of the staged file. The staged filename preserves the original
// basename so diff output stays meaningful.
func stage(tmpDir, side, displayPath, src, cmd string) (string, error) {
	sub := filepath.Join(tmpDir, side)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		return "", err
	}
	base := filepath.Base(displayPath)
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = side
	}
	out := filepath.Join(sub, base)

	in, err := openInput(src)
	if err != nil {
		return "", err
	}
	defer in.Close()

	f, err := os.Create(out)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if cmd == "" {
		if _, err := io.Copy(f, in); err != nil {
			return "", err
		}
		return out, nil
	}

	shell, flag := shell()
	c := exec.Command(shell, flag, cmd)
	c.Stdin = in
	c.Stdout = f
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("running %q: %w", cmd, err)
	}
	return out, nil
}

// openInput opens src for reading. /dev/null on Windows is mapped to NUL so
// the git deletion/creation case still works cross-platform.
func openInput(src string) (io.ReadCloser, error) {
	if runtime.GOOS == "windows" && src == "/dev/null" {
		src = "NUL"
	}
	return os.Open(src)
}

func shell() (string, string) {
	if runtime.GOOS == "windows" {
		if s := os.Getenv("ComSpec"); s != "" {
			return s, "/C"
		}
		return "cmd", "/C"
	}
	return "/bin/sh", "-c"
}

func runDiff(o *options, left, right string) (int, error) {
	cmdline := o.diff
	if !o.noColor && shouldColor() {
		cmdline = injectColor(cmdline)
	}
	full := cmdline + " " + shellQuote(left) + " " + shellQuote(right)
	shellBin, flag := shell()
	c := exec.Command(shellBin, flag, full)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	err := c.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// Treat exit code 1 (differences) as success for diffhance itself.
			return ee.ExitCode(), nil
		}
		return 2, err
	}
	return 0, nil
}

// shouldColor returns true when stdout is a terminal and NO_COLOR is unset.
func shouldColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// injectColor adds `--color=always` to a bare `diff` invocation so colors
// survive even though we're running it via /bin/sh. Other backends (delta,
// difft, ...) handle color themselves; we leave them alone.
func injectColor(cmdline string) string {
	fields := strings.Fields(cmdline)
	if len(fields) == 0 {
		return cmdline
	}
	if filepath.Base(fields[0]) != "diff" {
		return cmdline
	}
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "--color") || f == "--no-color" {
			return cmdline
		}
	}
	return cmdline + " --color=always"
}

func shellQuote(s string) string {
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
