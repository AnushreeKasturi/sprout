package main

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestCompletionsCoverEveryFlag(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		out, _, code := runCLI(t, "--completion", shell)
		if code != 0 {
			t.Fatalf("%s: exit %d", shell, code)
		}
		for _, f := range allFlags() {
			want := dash(f.name)
			if shell == "fish" {
				want = "-l " + f.name
				if len(f.name) == 1 {
					want = "-s " + f.name
				}
			}
			if !strings.Contains(out, want) {
				t.Errorf("%s completion is missing %s", shell, want)
			}
		}
	}
	if _, errOut, code := runCLI(t, "--completion", "tcsh"); code != 2 || !strings.Contains(errOut, "bash, zsh") {
		t.Errorf("unknown shell: exit %d %s", code, errOut)
	}
}

func TestManPage(t *testing.T) {
	out, _, code := runCLI(t, "--man")
	if code != 0 || !strings.HasPrefix(out, ".TH SPROUT 1") {
		t.Fatalf("exit %d: %.80s", code, out)
	}
	for _, f := range allFlags() {
		if !strings.Contains(out, ".B "+strings.ReplaceAll(dash(f.name), "-", `\-`)) {
			t.Errorf("man page is missing %s", dash(f.name))
		}
	}
}

// Every flag completion offers for a subcommand must be one it accepts.
func TestSubcommandFlagsExist(t *testing.T) {
	for _, c := range subcommands {
		if c.name == "mcp" {
			continue // mcp's only argument is a root
		}
		for _, f := range c.flags {
			args := []string{c.name, "--" + f.name}
			if f.takesValue {
				args = append(args, "1")
			}
			if _, errOut, code := runCLI(t, append(args, "--help")...); code != 0 {
				t.Errorf("sprout %s doesn't accept --%s: %s", c.name, f.name, errOut)
			}
		}
	}
}

// The bash script completes subcommands, and each one's flags and files.
func TestBashCompletesSubcommands(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("needs bash")
	}
	var script strings.Builder
	writeCompletion(&script, "bash")
	complete := func(words ...string) string {
		dir := t.TempDir()
		write(t, dir, "main.go", "")
		cmd := exec.Command(bash, "-c", script.String()+`
COMP_WORDS=("$@"); COMP_CWORD=$(( ${#COMP_WORDS[@]} - 1 )); _sprout; printf '%s\n' "${COMPREPLY[@]}"`, "x")
		cmd.Args = append(cmd.Args, words...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", words, err, out)
		}
		return string(out)
	}
	if got := complete("sprout", "dep"); got != "deps\ndependents\n" {
		t.Errorf("sprout dep<TAB> = %q", got)
	}
	if got := complete("sprout", "impact", "--s"); got != "--staged\n" {
		t.Errorf("sprout impact --s<TAB> = %q", got)
	}
	if got := complete("sprout", "context", "ma"); got != "main.go\n" {
		t.Errorf("sprout context ma<TAB> = %q", got)
	}
	if got := complete("sprout", "tour", "--l"); got != "--limit\n" {
		t.Errorf("sprout tour --l<TAB> = %q", got)
	}
	if got := complete("sprout", "tour", "--limit", "ma"); strings.Contains(got, "main.go") {
		t.Errorf("tour --limit must not complete filenames: %q", got)
	}
}
