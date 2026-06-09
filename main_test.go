package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	defaultRulesConfigPaths = func() []string { return nil }
	os.Exit(m.Run())
}

func TestParseArgsValidForms(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want options
	}{
		{
			name: "defaults and positional args",
			argv: []string{"left", "right"},
			want: options{diff: "diff -u", args: []string{"left", "right"}},
		},
		{
			name: "long flags with separate values",
			argv: []string{"--pre", "jq .", "--pre-left", "left-cmd", "--pre-right", "right-cmd", "--diff", "delta", "--rule", "*.json:jq -S .", "left", "right"},
			want: options{pre: "jq .", preLeft: "left-cmd", preRight: "right-cmd", diff: "delta", rules: []string{"*.json:jq -S ."}, args: []string{"left", "right"}},
		},
		{
			name: "long flags with equals values",
			argv: []string{"--pre=jq .", "--pre-left=left-cmd", "--pre-right=right-cmd", "--diff=delta", "--rule=*.xml:xmllint --format -", "left", "right"},
			want: options{pre: "jq .", preLeft: "left-cmd", preRight: "right-cmd", diff: "delta", rules: []string{"*.xml:xmllint --format -"}, args: []string{"left", "right"}},
		},
		{
			name: "short flags with attached values",
			argv: []string{"-pjq .", "-ddelta", "-r*.json:jq .", "left", "right"},
			want: options{pre: "jq .", diff: "delta", rules: []string{"*.json:jq ."}, args: []string{"left", "right"}},
		},
		{
			name: "boolean flags",
			argv: []string{"--git", "--print", "--no-color", "path", "old", "oldhex", "oldmode", "new", "newhex", "newmode"},
			want: options{diff: "diff -u", git: true, printPaths: true, noColor: true, args: []string{"path", "old", "oldhex", "oldmode", "new", "newhex", "newmode"}},
		},
		{
			name: "double dash stops flag parsing",
			argv: []string{"--pre", "cmd", "--", "-not-a-flag", "right"},
			want: options{pre: "cmd", diff: "diff -u", args: []string{"-not-a-flag", "right"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(tt.argv)
			if err != nil {
				t.Fatalf("parseArgs() error = %v", err)
			}
			if !reflect.DeepEqual(*got, tt.want) {
				t.Fatalf("parseArgs() = %#v, want %#v", *got, tt.want)
			}
		})
	}
}

func TestParseArgsConfigRules(t *testing.T) {
	tmp := t.TempDir()
	config := filepath.Join(tmp, "rules.conf")
	if err := os.WriteFile(config, []byte("\n# comment\n*.json:jq -S .\n*.xml:xmllint --format -\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := parseArgs([]string{"--rule", "*.txt:fmt-txt", "--config", config, "--rule=*.yaml:fmt-yaml", "left", "right"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	want := options{
		diff:  "diff -u",
		rules: []string{"*.txt:fmt-txt", "*.json:jq -S .", "*.xml:xmllint --format -", "*.yaml:fmt-yaml"},
		args:  []string{"left", "right"},
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("parseArgs() = %#v, want %#v", *got, want)
	}
}

func TestParseArgsConfigEqualsAndShortAttached(t *testing.T) {
	tmp := t.TempDir()
	config1 := filepath.Join(tmp, "rules1.conf")
	config2 := filepath.Join(tmp, "rules2.conf")
	if err := os.WriteFile(config1, []byte("*.json:jq .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config2, []byte("*.xml:xmllint --format -\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := parseArgs([]string{"--config=" + config1, "-c" + config2, "left", "right"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if want := []string{"*.json:jq .", "*.xml:xmllint --format -"}; !reflect.DeepEqual(got.rules, want) {
		t.Fatalf("rules = %#v, want %#v", got.rules, want)
	}
}

func TestParseArgsErrors(t *testing.T) {
	tests := []struct {
		name    string
		argv    []string
		wantErr string
	}{
		{name: "pre missing value", argv: []string{"--pre"}, wantErr: "flag --pre requires a value"},
		{name: "pre-left missing value", argv: []string{"--pre-left"}, wantErr: "flag --pre-left requires a value"},
		{name: "pre-right missing value", argv: []string{"--pre-right"}, wantErr: "flag --pre-right requires a value"},
		{name: "diff missing value", argv: []string{"--diff"}, wantErr: "flag --diff requires a value"},
		{name: "rule missing value", argv: []string{"--rule"}, wantErr: "flag --rule requires a value"},
		{name: "config missing value", argv: []string{"--config"}, wantErr: "flag --config requires a value"},
		{name: "unknown flag", argv: []string{"--wat"}, wantErr: "unknown flag \"--wat\""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseArgs(tt.argv)
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("parseArgs() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseArgsConfigReadError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.conf")
	_, err := parseArgs([]string{"--config", missing, "left", "right"})
	if err == nil || !strings.Contains(err.Error(), "read config") || !strings.Contains(err.Error(), missing) {
		t.Fatalf("parseArgs() error = %v, want config read error", err)
	}
}

func TestParseArgsLoadsDefaultConfigRules(t *testing.T) {
	tmp := t.TempDir()
	userConfig := filepath.Join(tmp, "user", "diffhance", "rules")
	systemConfig := filepath.Join(tmp, "system", "diffhance", "rules")
	missingConfig := filepath.Join(tmp, "missing", "diffhance", "rules")
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(systemConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userConfig, []byte("*.json:user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(systemConfig, []byte("*.xml:system\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withDefaultRulesConfigPaths(t, []string{userConfig, missingConfig, systemConfig})

	got, err := parseArgs([]string{"--rule", "*.txt:inline", "left", "right"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if want := []string{"*.txt:inline", "*.json:user", "*.xml:system"}; !reflect.DeepEqual(got.rules, want) {
		t.Fatalf("rules = %#v, want %#v", got.rules, want)
	}
}

func TestParseArgsDefaultConfigReadError(t *testing.T) {
	dir := t.TempDir()
	withDefaultRulesConfigPaths(t, []string{dir})

	_, err := parseArgs([]string{"left", "right"})
	if err == nil || !strings.Contains(err.Error(), "read config") || !strings.Contains(err.Error(), dir) {
		t.Fatalf("parseArgs() error = %v, want default config read error", err)
	}
}

func TestParseArgsHelp(t *testing.T) {
	_, err := parseArgs([]string{"--help"})
	if !errors.Is(err, errHelp) {
		t.Fatalf("parseArgs(--help) error = %v, want errHelp", err)
	}

	_, err = parseArgs([]string{"-h"})
	if !errors.Is(err, errHelp) {
		t.Fatalf("parseArgs(-h) error = %v, want errHelp", err)
	}
}

func TestStringSlice(t *testing.T) {
	var s stringSlice
	if err := s.Set("one"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := s.Set("two"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if got := s.String(); got != "one,two" {
		t.Fatalf("String() = %q, want one,two", got)
	}
}

func TestResolveInputs(t *testing.T) {
	tests := []struct {
		name        string
		o           options
		wantLeft    string
		wantRight   string
		wantDisplay string
		wantErr     string
	}{
		{
			name:        "same basename uses basename for display",
			o:           options{args: []string{"old/config.json", "new/config.json"}},
			wantLeft:    "old/config.json",
			wantRight:   "new/config.json",
			wantDisplay: "config.json",
		},
		{
			name:        "different basenames use left path for display",
			o:           options{args: []string{"old/config.json", "new/settings.json"}},
			wantLeft:    "old/config.json",
			wantRight:   "new/settings.json",
			wantDisplay: "old/config.json",
		},
		{
			name:        "git args use provided path and file slots",
			o:           options{git: true, args: []string{"src/app.json", "old-file", "oldhex", "100644", "new-file", "newhex", "100644", "renamed", "100644"}},
			wantLeft:    "old-file",
			wantRight:   "new-file",
			wantDisplay: "src/app.json",
		},
		{
			name:    "non-git requires exactly two args",
			o:       options{args: []string{"only-one"}},
			wantErr: "expected exactly 2 positional args (LEFT RIGHT), got 1",
		},
		{
			name:    "git requires seven args",
			o:       options{git: true, args: []string{"path", "old"}},
			wantErr: "--git expects at least 7 positional args, got 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left, right, display, err := resolveInputs(&tt.o)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("resolveInputs() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveInputs() error = %v", err)
			}
			if left != tt.wantLeft || right != tt.wantRight || display != tt.wantDisplay {
				t.Fatalf("resolveInputs() = (%q, %q, %q), want (%q, %q, %q)", left, right, display, tt.wantLeft, tt.wantRight, tt.wantDisplay)
			}
		})
	}
}

func TestPickPreRulesAndPrecedence(t *testing.T) {
	tests := []struct {
		name        string
		o           options
		displayPath string
		side        side
		want        string
	}{
		{
			name:        "matching rule uses basename only",
			o:           options{rules: []string{"*.json:jq -S ."}},
			displayPath: "src/config.json",
			side:        "left",
			want:        "jq -S .",
		},
		{
			name:        "directory glob does not match basename",
			o:           options{rules: []string{"src/*.json:jq ."}},
			displayPath: "src/config.json",
			side:        "left",
			want:        "",
		},
		{
			name:        "first matching rule wins",
			o:           options{rules: []string{"*.json:first", "config.*:second"}},
			displayPath: "config.json",
			side:        "right",
			want:        "first",
		},
		{
			name:        "invalid and malformed rules are ignored",
			o:           options{rules: []string{"*.json", "[:bad", "*.xml:xmllint"}},
			displayPath: "config.json",
			side:        "left",
			want:        "",
		},
		{
			name:        "global pre overrides rules",
			o:           options{pre: "global", rules: []string{"*.json:rule"}},
			displayPath: "config.json",
			side:        "left",
			want:        "global",
		},
		{
			name:        "left pre overrides global pre and rules",
			o:           options{pre: "global", preLeft: "left-only", rules: []string{"*.json:rule"}},
			displayPath: "config.json",
			side:        "left",
			want:        "left-only",
		},
		{
			name:        "right pre overrides global pre and rules",
			o:           options{pre: "global", preRight: "right-only", rules: []string{"*.json:rule"}},
			displayPath: "config.json",
			side:        "right",
			want:        "right-only",
		},
		{
			name:        "opposite side override does not apply",
			o:           options{preRight: "right-only", rules: []string{"*.json:rule"}},
			displayPath: "config.json",
			side:        "left",
			want:        "rule",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickPre(&tt.o, tt.displayPath, tt.side); got != tt.want {
				t.Fatalf("pickPre() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStageCopiesOrPreprocessesInput(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "input.txt")
	if err := os.WriteFile(src, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := stage(tmp, "left", "nested/display.txt", src, "")
	if err != nil {
		t.Fatalf("stage(copy) error = %v", err)
	}
	assertFileContent(t, out, "hello\n")
	if filepath.Base(out) != "display.txt" {
		t.Fatalf("stage output basename = %q, want display.txt", filepath.Base(out))
	}

	if runtime.GOOS == "windows" {
		t.Skip("shell preprocessing command is POSIX-specific")
	}
	out, err = stage(tmp, "right", "display.txt", src, "tr a-z A-Z")
	if err != nil {
		t.Fatalf("stage(preprocess) error = %v", err)
	}
	assertFileContent(t, out, "HELLO\n")
}

func TestStageUsesSideNameForEmptyDisplayBasename(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "input.txt")
	if err := os.WriteFile(src, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := stage(tmp, "left", string(filepath.Separator), src, "")
	if err != nil {
		t.Fatalf("stage() error = %v", err)
	}
	if filepath.Base(out) != "left" {
		t.Fatalf("stage output basename = %q, want left", filepath.Base(out))
	}
}

func TestStageErrors(t *testing.T) {
	tmp := t.TempDir()
	_, err := stage(tmp, "left", "display.txt", filepath.Join(tmp, "missing.txt"), "")
	if err == nil {
		t.Fatal("stage() error = nil, want missing input error")
	}

	if runtime.GOOS == "windows" {
		t.Skip("preprocessing command is POSIX-specific")
	}
	src := filepath.Join(tmp, "input.txt")
	if err := os.WriteFile(src, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = stage(tmp, "right", "display.txt", src, "exit 7")
	if err == nil || !strings.Contains(err.Error(), `running "exit 7"`) {
		t.Fatalf("stage() error = %v, want command failure", err)
	}
}

func TestInjectColor(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty command unchanged", in: "", want: ""},
		{name: "bare diff gets color", in: "diff -u", want: "diff -u --color=always"},
		{name: "path to diff gets color", in: "/usr/bin/diff -u", want: "/usr/bin/diff -u --color=always"},
		{name: "existing color unchanged", in: "diff -u --color=never", want: "diff -u --color=never"},
		{name: "explicit no-color unchanged", in: "diff -u --no-color", want: "diff -u --no-color"},
		{name: "non-diff backend unchanged", in: "delta --paging=never", want: "delta --paging=never"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := injectColor(tt.in); got != tt.want {
				t.Fatalf("injectColor(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestShouldColorHonorsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if shouldColor() {
		t.Fatal("shouldColor() = true when NO_COLOR is set")
	}
}

func TestShellQuote(t *testing.T) {
	if runtime.GOOS == "windows" {
		if got := shellQuote(`a"b`); got != `"a""b"` {
			t.Fatalf("shellQuote() = %q", got)
		}
		return
	}

	if got := shellQuote("a'b c"); got != `'a'\''b c'` {
		t.Fatalf("shellQuote() = %q, want shell-safe single-quote escaping", got)
	}
}

func TestRunPrintAppliesRulesByFileBasename(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("preprocessing command is POSIX-specific")
	}

	tmp := t.TempDir()
	left := filepath.Join(tmp, "left", "config.json")
	right := filepath.Join(tmp, "right", "config.json")
	if err := os.MkdirAll(filepath.Dir(left), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(right), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(left, []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(right, []byte("def\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout := captureStdout(t, func() {
		code, err := run(&options{
			printPaths: true,
			rules:      []string{"*.txt:ignored", "*.json:tr a-z A-Z"},
			diff:       "diff -u",
			args:       []string{left, right},
		})
		if err != nil {
			t.Fatalf("run() error = %v", err)
		}
		if code != 0 {
			t.Fatalf("run() code = %d, want 0", code)
		}
	})

	parts := strings.Split(strings.TrimSpace(stdout), "\t")
	if len(parts) != 2 {
		t.Fatalf("run --print stdout = %q, want two tab-separated paths", stdout)
	}
	assertFileContent(t, parts[0], "ABC\n")
	assertFileContent(t, parts[1], "DEF\n")
}

func TestRunGitModeSwallowsDiffExitOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("default diff backend is not available on Windows")
	}

	tmp := t.TempDir()
	oldFile := filepath.Join(tmp, "old.txt")
	newFile := filepath.Join(tmp, "new.txt")
	if err := os.WriteFile(oldFile, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, err := run(&options{
		git:     true,
		noColor: true,
		diff:    "diff -u",
		args:    []string{"path.txt", oldFile, "oldhex", "100644", newFile, "newhex", "100644"},
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 in git mode when files differ", code)
	}
}

func TestRunNonGitPreservesDiffExitOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("default diff backend is not available on Windows")
	}

	tmp := t.TempDir()
	left := filepath.Join(tmp, "left.txt")
	right := filepath.Join(tmp, "right.txt")
	if err := os.WriteFile(left, []byte("left\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(right, []byte("right\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, err := run(&options{noColor: true, diff: "diff -u", args: []string{left, right}})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if code != 1 {
		t.Fatalf("run() code = %d, want 1 when files differ outside git mode", code)
	}
}

func TestRunErrors(t *testing.T) {
	code, err := run(&options{diff: "diff -u", args: []string{"only-one"}})
	if err == nil || code != 2 {
		t.Fatalf("run() = (%d, %v), want code 2 with resolve error", code, err)
	}

	tmp := t.TempDir()
	left := filepath.Join(tmp, "left.txt")
	right := filepath.Join(tmp, "right.txt")
	if err := os.WriteFile(left, []byte("left\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(right, []byte("right\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, err = run(&options{preLeft: "exit 9", diff: "diff -u", args: []string{left, right}})
	if runtime.GOOS == "windows" {
		if err == nil || code != 2 {
			t.Fatalf("run() = (%d, %v), want code 2 with preprocess error", code, err)
		}
		return
	}
	if err == nil || code != 2 || !strings.Contains(err.Error(), "preprocess left") {
		t.Fatalf("run() = (%d, %v), want code 2 with left preprocess error", code, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	if string(b) != want {
		t.Fatalf("ReadFile(%q) = %q, want %q", path, string(b), want)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func withDefaultRulesConfigPaths(t *testing.T, paths []string) {
	t.Helper()
	orig := defaultRulesConfigPaths
	defaultRulesConfigPaths = func() []string { return paths }
	t.Cleanup(func() { defaultRulesConfigPaths = orig })
}
