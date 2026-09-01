// diffhance preprocesses each side of a diff before handing the result
// to a diff tool. It is designed to compose with git, diff, delta, etc.
//
//	diffhance --pre 'jq .' a.json b.json
//	diffhance --rule '*.json:jq .' --rule '*.xml:xmllint --format -' a b
//	diffhance --config .diffhance-rules a b
//	diffhance --print-rules
//	diffhance --print-config-dirs
//	GIT_EXTERNAL_DIFF='diffhance --git --rule "*.json:jq ."' git diff HEAD~1
package main

import (
	"bytes"
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
  diffhance [flags] git [GIT-DIFF-ARGS...]
  diffhance --git [flags] PATH OLD-FILE OLD-HEX OLD-MODE NEW-FILE NEW-HEX NEW-MODE [RENAME-TO RENAME-MODE]

GIT SHORTHAND
  "diffhance [flags] git ..." runs "git diff ..." with GIT_EXTERNAL_DIFF set to
  this binary in --git mode, forwarding any flags given before "git".

FLAGS
  -p, --pre CMD            Shell pipeline applied to BOTH sides (file streamed on stdin)
      --pre-left CMD       Shell pipeline applied to LEFT side only
      --pre-right CMD      Shell pipeline applied to RIGHT side only
  -r, --rule GLOB:CMD      Apply CMD when the file's basename matches GLOB (repeatable).
                           Matching rules are chained in order. Ignored when --pre/--pre-* is set.
      --pre-errors TARGET  Report ignored rule preprocessor failures to "stderr" or PATH.
                           Default: silent.
  -c, --config PATH        Read rules from PATH, one GLOB:CMD per line. Blank lines and
                           lines starting with # are ignored. May be repeated.
                           Existing default configs are also loaded from the platform's
                           config directories as diffhance/rules.
  -d, --diff CMD           Diff backend (default: "diff -u"). Receives preprocessed
                           LEFT and RIGHT as arguments. Bare "hunk" invokes
                           "hunk difftool -- LEFT RIGHT PATH" with the display path.
      --git                Treat positional args as git's external-diff invocation:
                           PATH OLD-FILE OLD-HEX OLD-MODE NEW-FILE NEW-HEX NEW-MODE ...
      --print              Skip diffing. Print "LEFT-PATH\tRIGHT-PATH" of the
                           preprocessed files (kept on disk) for downstream tools.
      --print-rules        Print resolved rules, one per line, then exit.
      --print-config-dirs  Print default config file locations checked, one per line, then exit.
      --no-color           Do not auto-add color flags to the default diff backend.
      --version            Print the release name and exit.
  -h, --help               Show this help and exit.

EXAMPLES
  # Diff two JSON files after running jq on both
  diffhance --pre 'jq .' old.json new.json

  # Use delta as the renderer
  diffhance --pre 'jq .' -d delta old.json new.json

  # Review the preprocessed pair in Hunk
  diffhance --pre 'jq .' -d hunk old.json new.json

  # Per-extension rules
  diffhance -r '*.json:jq .' -r '*.xml:xmllint --format -' a/ b/   # (per file)

  # Rules from a config file
  diffhance --config .diffhance-rules a.json b.json

  # Inspect resolved rules in application order
  diffhance --print-rules

  # Inspect default config file locations
  diffhance --print-config-dirs

  # Pipe two preprocessed files into your own pipeline
  read L R < <(diffhance --print --pre 'jq .' a.json b.json); diff -u "$L" "$R"

  # Use as a git external diff
  GIT_EXTERNAL_DIFF='diffhance --git --rule "*.json:jq ." -d "diff -u"' \
      git diff HEAD~1 HEAD

  # Same, via the git shorthand (flags before "git" are forwarded)
  diffhance -r '*.json:jq .' git HEAD~1 HEAD

EXIT STATUS
  Mirrors the diff backend (0 = identical, 1 = differ, >1 = error).
`

type stringSlice []string

func (s *stringSlice) String() string     { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }

var releaseName = "dev"

const (
	leftPrefix  = "l"
	rightPrefix = "r"
)

type options struct {
	pre, preLeft, preRight string
	diff                   string
	rules                  []string
	git                    bool
	printPaths             bool
	printRules             bool
	printConfigDirs        bool
	version                bool
	noColor                bool
	preErrors              string
	args                   []string
}

func main() {
	if forward, gitArgs, ok := splitGitShorthand(os.Args[1:]); ok {
		code, err := runGitDiff(forward, gitArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "diffhance: %v\n", err)
			if code == 0 {
				code = 2
			}
		}
		os.Exit(code)
	}

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

// valueFlags lists flags that consume the following argument as their value.
var valueFlags = map[string]bool{
	"-p": true, "--pre": true,
	"--pre-left": true, "--pre-right": true,
	"-d": true, "--diff": true,
	"-r": true, "--rule": true,
	"-c": true, "--config": true,
	"--pre-errors": true,
}

// splitGitShorthand detects the "diffhance [flags] git ..." shorthand, where
// the first positional argument is the literal word "git". It returns the
// flags preceding "git" (forwarded to the GIT_EXTERNAL_DIFF command) and the
// arguments following it (passed through to "git diff").
func splitGitShorthand(argv []string) (forward, gitArgs []string, ok bool) {
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "git":
			return argv[:i], argv[i+1:], true
		case a == "--":
			// Explicit end of flags: positionals are LEFT/RIGHT files.
			return nil, nil, false
		case valueFlags[a]:
			i++ // skip the flag's value; parseArgs reports missing values
		case strings.HasPrefix(a, "-") && a != "-":
			// Flag with attached value (or boolean); nothing extra to skip.
		default:
			// First positional is not "git": normal LEFT/RIGHT mode.
			return nil, nil, false
		}
	}
	return nil, nil, false
}

// executablePath is swapped in tests.
var executablePath = func() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		return exe
	}
	return os.Args[0]
}

// runGitDiff implements "diffhance [flags] git [ARGS...]": it runs
// "git diff ARGS..." with GIT_EXTERNAL_DIFF pointing back at this binary in
// --git mode, forwarding any diffhance flags that preceded "git".
func runGitDiff(forward, gitArgs []string) (int, error) {
	gitCommand := []string{"diff"}
	if forwardedDiffUsesHunk(forward) {
		// Git normally puts diff output behind its pager when stdout is a
		// terminal. A direct Hunk backend needs that terminal itself.
		gitCommand = []string{"--no-pager", "diff"}
	}
	c := exec.Command("git", append(gitCommand, gitArgs...)...)
	c.Env = append(os.Environ(), "GIT_EXTERNAL_DIFF="+gitExternalDiffCommand(executablePath(), forward))
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return 2, fmt.Errorf("running git diff: %w", err)
	}
	return 0, nil
}

func forwardedDiffUsesHunk(argv []string) bool {
	backend := ""
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "-d" || a == "--diff":
			if i+1 < len(argv) {
				i++
				backend = argv[i]
			}
		case strings.HasPrefix(a, "--diff="):
			backend = strings.TrimPrefix(a, "--diff=")
		case strings.HasPrefix(a, "-d") && len(a) > 2:
			backend = a[2:]
		case valueFlags[a]:
			if i+1 < len(argv) {
				i++
			}
		}
	}
	return isHunkDifftoolBackend(backend)
}

// gitExternalDiffCommand builds the GIT_EXTERNAL_DIFF command string. Git
// always runs external diff commands through a POSIX shell (on Windows via
// Git's bundled sh), so single-quote quoting applies on every platform.
func gitExternalDiffCommand(exe string, forward []string) string {
	parts := []string{posixQuote(exe), "--git"}
	for _, f := range forward {
		parts = append(parts, posixQuote(f))
	}
	return strings.Join(parts, " ")
}

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
		case a == "--print-rules":
			o.printRules = true
		case a == "--print-config-dirs":
			o.printConfigDirs = true
		case a == "--version":
			o.version = true
		case a == "--no-color":
			o.noColor = true
		case a == "--pre-errors":
			v, err := take(&i, a)
			if err != nil {
				return nil, err
			}
			o.preErrors = v
		case strings.HasPrefix(a, "--pre-errors="):
			o.preErrors = strings.TrimPrefix(a, "--pre-errors=")
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
		case a == "-c", a == "--config":
			v, err := take(&i, a)
			if err != nil {
				return nil, err
			}
			rules, err := readRulesFile(v)
			if err != nil {
				return nil, err
			}
			o.rules = append(o.rules, rules...)
		case strings.HasPrefix(a, "--config="):
			rules, err := readRulesFile(strings.TrimPrefix(a, "--config="))
			if err != nil {
				return nil, err
			}
			o.rules = append(o.rules, rules...)
		case strings.HasPrefix(a, "-c") && len(a) > 2:
			rules, err := readRulesFile(a[2:])
			if err != nil {
				return nil, err
			}
			o.rules = append(o.rules, rules...)
		case strings.HasPrefix(a, "-") && a != "-":
			return nil, fmt.Errorf("unknown flag %q", a)
		default:
			o.args = append(o.args, a)
		}
	}

	if o.diff == "" {
		o.diff = "diff -u"
	}
	if o.printConfigDirs || o.version {
		return o, nil
	}
	rules, err := readExistingRulesFiles(defaultRulesConfigPaths())
	if err != nil {
		return nil, err
	}
	o.rules = append(o.rules, rules...)
	return o, nil
}

var defaultRulesConfigPaths = platformRulesConfigPaths

func platformRulesConfigPaths() []string {
	var paths []string
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		paths = append(paths, filepath.Join(dir, "diffhance", "rules"))
	}

	switch runtime.GOOS {
	case "windows":
		if dir := os.Getenv("ProgramData"); dir != "" {
			paths = append(paths, filepath.Join(dir, "diffhance", "rules"))
		}
	case "darwin":
		if dir, err := os.UserHomeDir(); err == nil && dir != "" {
			paths = append(paths, filepath.Join(dir, ".config", "diffhance", "rules"))
		}
		paths = append(paths, filepath.Join(string(filepath.Separator), "Library", "Application Support", "diffhance", "rules"))
	default:
		dirs := os.Getenv("XDG_CONFIG_DIRS")
		if dirs == "" {
			dirs = filepath.Join(string(filepath.Separator), "etc", "xdg")
		}
		for _, dir := range filepath.SplitList(dirs) {
			if dir != "" {
				paths = append(paths, filepath.Join(dir, "diffhance", "rules"))
			}
		}
	}
	return paths
}

func readExistingRulesFiles(paths []string) ([]string, error) {
	var rules []string
	for _, path := range paths {
		fileRules, err := readRulesFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		rules = append(rules, fileRules...)
	}
	return rules, nil
}

func readRulesFile(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	var rules []string
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rules = append(rules, line)
	}
	return rules, nil
}

func run(o *options) (int, error) {
	if o.version {
		fmt.Println(releaseName)
		return 0, nil
	}

	if o.printConfigDirs {
		printConfigDirs(defaultRulesConfigPaths())
		return 0, nil
	}

	if o.printRules {
		printRules(o.rules)
		return 0, nil
	}

	leftPath, rightPath, leftDisplayPath, rightDisplayPath, err := resolveInputs(o)
	if err != nil {
		return 2, err
	}

	leftCmds, leftIgnorePreprocessErrors := pickPre(o, leftDisplayPath, side("left"))
	rightCmds, rightIgnorePreprocessErrors := pickPre(o, rightDisplayPath, side("right"))
	preErrOut, closePreErrorWriter, err := preprocessErrorWriter(o.preErrors)
	if err != nil {
		return 2, err
	}
	if closePreErrorWriter != nil {
		defer closePreErrorWriter()
	}

	tmpDir, err := os.MkdirTemp("", "diffhance-")
	if err != nil {
		return 2, err
	}
	if !o.printPaths {
		defer os.RemoveAll(tmpDir)
	}

	leftOut, err := stage(tmpDir, leftPrefix, leftDisplayPath, leftPath, leftCmds, leftIgnorePreprocessErrors, preErrOut)
	if err != nil {
		return 2, fmt.Errorf("preprocess left: %w", err)
	}
	rightOut, err := stage(tmpDir, rightPrefix, rightDisplayPath, rightPath, rightCmds, rightIgnorePreprocessErrors, preErrOut)
	if err != nil {
		return 2, fmt.Errorf("preprocess right: %w", err)
	}

	if o.printPaths {
		fmt.Printf("%s\t%s\n", leftOut, rightOut)
		return 0, nil
	}

	leftLabel := filepath.Join(leftPrefix, stageDisplayPath(leftDisplayPath, leftPrefix))
	rightLabel := filepath.Join(rightPrefix, stageDisplayPath(rightDisplayPath, rightPrefix))
	code, err := runDiff(o, leftOut, rightOut, leftLabel, rightLabel, rightDisplayPath)
	// Git treats any non-zero exit from GIT_EXTERNAL_DIFF as fatal. The "files
	// differ" signal (exit 1) is meaningless to git here — it already knew
	// they differ, that's why it invoked us — so swallow it in --git mode.
	if o.git && err == nil && code == 1 {
		code = 0
	}
	return code, err
}

func printRules(rules []string) {
	for _, r := range rules {
		fmt.Println(r)
	}
}

func printConfigDirs(paths []string) {
	for _, path := range paths {
		fmt.Println(path)
	}
}

type side string

// resolveInputs returns left, right, and per-side display paths used for rule
// matching and staged output paths. In --git mode the display paths are the
// repo-relative paths that git provides.
func resolveInputs(o *options) (left, right, leftDisplay, rightDisplay string, err error) {
	if o.git {
		if len(o.args) < 7 {
			return "", "", "", "", fmt.Errorf("--git expects at least 7 positional args, got %d", len(o.args))
		}
		// path old-file old-hex old-mode new-file new-hex new-mode [rename-to rename-mode]
		rightDisplay = o.args[0]
		if len(o.args) >= 9 {
			rightDisplay = o.args[7]
		}
		return o.args[1], o.args[4], o.args[0], rightDisplay, nil
	}
	if len(o.args) != 2 {
		return "", "", "", "", fmt.Errorf("expected exactly 2 positional args (LEFT RIGHT), got %d", len(o.args))
	}
	return o.args[0], o.args[1], o.args[0], o.args[0], nil
}

func pickPre(o *options, displayPath string, s side) ([]string, bool) {
	switch s {
	case "left":
		if o.preLeft != "" {
			return []string{o.preLeft}, false
		}
	case "right":
		if o.preRight != "" {
			return []string{o.preRight}, false
		}
	}
	if o.pre != "" {
		return []string{o.pre}, false
	}
	base := filepath.Base(displayPath)
	var chain []string
	for _, r := range o.rules {
		glob, cmd, ok := strings.Cut(r, ":")
		if !ok {
			continue
		}
		match, err := filepath.Match(glob, base)
		if err == nil && match {
			chain = append(chain, cmd)
		}
	}
	return chain, true
}

// stage either copies src into tmpDir or streams it through cmd, returning
// the path of the staged file. The staged path preserves the original display
// path so diff output stays meaningful.
func stage(tmpDir, side, displayPath, src string, cmds []string, ignorePreprocessErrors bool, preErrorWriter io.Writer) (string, error) {
	sub := filepath.Join(tmpDir, side)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		return "", err
	}
	rel := stageDisplayPath(displayPath, side)
	out := filepath.Join(sub, rel)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}

	if len(cmds) == 0 {
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
		if _, err := io.Copy(f, in); err != nil {
			return "", err
		}
		return out, nil
	}
	if ignorePreprocessErrors {
		return stageIgnoringPreprocessErrors(tmpDir, out, src, cmds, preErrorWriter)
	}

	in, err := openInput(src)
	if err != nil {
		return "", err
	}
	defer in.Close()

	var tempPaths []string
	defer func() {
		for _, path := range tempPaths {
			_ = os.Remove(path)
		}
	}()

	for i, cmd := range cmds {
		isLast := i == len(cmds)-1
		outPath := out
		var f *os.File
		var err error
		if isLast {
			f, err = os.Create(outPath)
		} else {
			f, err = os.CreateTemp(tmpDir, "pre-")
			if err == nil {
				outPath = f.Name()
				tempPaths = append(tempPaths, outPath)
			}
		}
		if err != nil {
			return "", err
		}

		err = runPreprocess(cmd, in, f)
		closeErr := f.Close()
		_ = in.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		if !isLast {
			in, err = os.Open(outPath)
			if err != nil {
				return "", err
			}
		}
	}
	return out, nil
}

func stageIgnoringPreprocessErrors(tmpDir, out, src string, cmds []string, preErrorWriter io.Writer) (string, error) {
	currentPath := src
	var tempPaths []string
	defer func() {
		for _, path := range tempPaths {
			_ = os.Remove(path)
		}
	}()

	for _, cmd := range cmds {
		in, err := openInput(currentPath)
		if err != nil {
			return "", err
		}
		f, err := os.CreateTemp(tmpDir, "pre-")
		if err != nil {
			_ = in.Close()
			return "", err
		}
		tempPath := f.Name()

		runErr, stderr := runPreprocessCapturingStderr(cmd, in, f)
		closeErr := f.Close()
		_ = in.Close()
		if runErr != nil {
			reportIgnoredPreprocessError(preErrorWriter, cmd, runErr, stderr)
			_ = os.Remove(tempPath)
			continue
		}
		if closeErr != nil {
			_ = os.Remove(tempPath)
			return "", closeErr
		}
		currentPath = tempPath
		tempPaths = append(tempPaths, tempPath)
	}

	in, err := openInput(currentPath)
	if err != nil {
		return "", err
	}
	defer in.Close()
	f, err := os.Create(out)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, in); err != nil {
		return "", err
	}
	return out, nil
}

func runPreprocess(cmd string, in io.Reader, out io.Writer) error {
	return runPreprocessWithStderr(cmd, in, out, os.Stderr)
}

func runPreprocessCapturingStderr(cmd string, in io.Reader, out io.Writer) (error, string) {
	var stderr bytes.Buffer
	err := runPreprocessWithStderr(cmd, in, out, &stderr)
	return err, stderr.String()
}

func preprocessErrorWriter(target string) (io.Writer, func() error, error) {
	switch target {
	case "":
		return nil, nil, nil
	case "stderr":
		return os.Stderr, nil, nil
	default:
		f, err := os.Create(target)
		if err != nil {
			return nil, nil, fmt.Errorf("open preprocessor error log %q: %w", target, err)
		}
		return f, f.Close, nil
	}
}

func reportIgnoredPreprocessError(w io.Writer, cmd string, err error, stderr string) {
	if w == nil {
		return
	}
	exitCode := "unknown"
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		exitCode = fmt.Sprint(ee.ExitCode())
	}
	fmt.Fprintf(w, "diffhance: ignored rule preprocessor failure\ncommand: %q\nexit code: %s\nstderr:\n%s", cmd, exitCode, stderr)
	if stderr == "" || !strings.HasSuffix(stderr, "\n") {
		fmt.Fprintln(w)
	}
}

func runPreprocessWithStderr(cmd string, in io.Reader, out io.Writer, stderr io.Writer) error {
	shell, flag := shell()
	c := exec.Command(shell, flag, cmd)
	c.Stdin = in
	c.Stdout = out
	c.Stderr = stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("running %q: %w", cmd, err)
	}
	return nil
}

func stageDisplayPath(displayPath, fallback string) string {
	path := filepath.Clean(displayPath)
	if path == "." || path == string(filepath.Separator) {
		return fallback
	}
	if filepath.IsAbs(path) {
		if rel, err := filepath.Rel(mustGetwd(), path); err == nil && isSafeRelativePath(rel) {
			return rel
		}
		path = filepath.Base(path)
	}
	if !isSafeRelativePath(path) {
		path = filepath.Base(path)
	}
	if path == "." || path == string(filepath.Separator) {
		return fallback
	}
	return path
}

func isSafeRelativePath(path string) bool {
	if path == "" || filepath.IsAbs(path) {
		return false
	}
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if part == ".." {
			return false
		}
	}
	return true
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
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

func runDiff(o *options, left, right, leftLabel, rightLabel, displayPath string) (int, error) {
	cmdline := o.diff
	if !o.noColor && shouldColor() {
		cmdline = injectColor(cmdline)
	}
	if shouldPipeLabeledDiff(cmdline) {
		return runLabeledDiffThroughBackend(cmdline, left, right, leftLabel, rightLabel)
	}
	hunkBackend := isHunkDifftoolBackend(cmdline)
	diffCode := 0
	if hunkBackend {
		equal, err := filesEqual(left, right)
		if err != nil {
			return 2, fmt.Errorf("compare staged files: %w", err)
		}
		if !equal {
			diffCode = 1
		}
	}
	full := diffCommand(cmdline, left, right, leftLabel, rightLabel, displayPath)
	shellBin, flag := shell()
	c := exec.Command(shellBin, flag, full)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	err := c.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if hunkBackend && ee.ExitCode() == 1 {
				// Hunk uses 1 for an application error, not "files differ".
				return 2, nil
			}
			// Treat exit code 1 (differences) as success for diffhance itself.
			return ee.ExitCode(), nil
		}
		return 2, err
	}
	return diffCode, nil
}

func filesEqual(left, right string) (bool, error) {
	l, err := os.Open(left)
	if err != nil {
		return false, err
	}
	defer l.Close()
	r, err := os.Open(right)
	if err != nil {
		return false, err
	}
	defer r.Close()

	leftInfo, err := l.Stat()
	if err != nil {
		return false, err
	}
	rightInfo, err := r.Stat()
	if err != nil {
		return false, err
	}
	if leftInfo.Size() != rightInfo.Size() {
		return false, nil
	}

	leftBuf := make([]byte, 32*1024)
	rightBuf := make([]byte, len(leftBuf))
	for {
		n, readErr := io.ReadFull(l, leftBuf)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return false, readErr
		}
		if n == 0 {
			return true, nil
		}
		if _, err := io.ReadFull(r, rightBuf[:n]); err != nil {
			return false, err
		}
		if !bytes.Equal(leftBuf[:n], rightBuf[:n]) {
			return false, nil
		}
		if readErr == io.ErrUnexpectedEOF {
			return true, nil
		}
	}
}

func runLabeledDiffThroughBackend(cmdline, left, right, leftLabel, rightLabel string) (int, error) {
	diffText, diffCode, err := runLabeledDiff(left, right, leftLabel, rightLabel)
	if err != nil {
		return diffCode, err
	}

	shellBin, flag := shell()
	c := exec.Command(shellBin, flag, cmdline)
	c.Stdin = strings.NewReader(diffText)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return 2, err
	}
	return diffCode, nil
}

func runLabeledDiff(left, right, leftLabel, rightLabel string) (string, int, error) {
	shellBin, flag := shell()
	c := exec.Command(shellBin, flag, labeledDiffCommand(left, right, leftLabel, rightLabel))
	c.Stderr = os.Stderr
	out, err := c.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), ee.ExitCode(), nil
		}
		return string(out), 2, err
	}
	return string(out), 0, nil
}

func diffCommand(cmdline, left, right, leftLabel, rightLabel, displayPath string) string {
	if isHunkDifftoolBackend(cmdline) {
		fields := strings.Fields(cmdline)
		if len(fields) == 1 {
			cmdline += " difftool"
		}
		hasEndOfOptions := false
		for _, field := range fields {
			if field == "--" {
				hasEndOfOptions = true
				break
			}
		}
		if !hasEndOfOptions {
			cmdline += " --"
		}
		return cmdline + " " + shellQuote(left) + " " + shellQuote(right) + " " + shellQuote(displayPath)
	}
	if shouldLabelBareDiff(cmdline) {
		cmdline += " --label " + shellQuote(leftLabel) + " --label " + shellQuote(rightLabel)
	}
	return cmdline + " " + shellQuote(left) + " " + shellQuote(right)
}

func isHunkDifftoolBackend(cmdline string) bool {
	fields := strings.Fields(cmdline)
	if len(fields) == 0 || strings.TrimSuffix(strings.ToLower(filepath.Base(fields[0])), ".exe") != "hunk" {
		return false
	}
	return len(fields) == 1 || fields[1] == "difftool"
}

func labeledDiffCommand(left, right, leftLabel, rightLabel string) string {
	return "diff -u --label " + shellQuote(leftLabel) + " --label " + shellQuote(rightLabel) + " " + shellQuote(left) + " " + shellQuote(right)
}

func shouldLabelBareDiff(cmdline string) bool {
	fields := strings.Fields(cmdline)
	if len(fields) == 0 || filepath.Base(fields[0]) != "diff" {
		return false
	}
	for _, f := range fields[1:] {
		if f == "--" || f == "--label" || strings.HasPrefix(f, "--label=") {
			return false
		}
	}
	return true
}

func shouldPipeLabeledDiff(cmdline string) bool {
	fields := strings.Fields(cmdline)
	if len(fields) == 0 {
		return false
	}
	switch filepath.Base(fields[0]) {
	case "delta":
		return true
	default:
		return false
	}
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
		return windowsCmdQuote(s)
	}
	return posixQuote(s)
}

func windowsCmdQuote(s string) string {
	s = strings.ReplaceAll(s, `%`, `%%`)
	s = strings.ReplaceAll(s, `"`, `""`)
	return `"` + s + `"`
}

func posixQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
