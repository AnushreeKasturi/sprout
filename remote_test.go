package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteURL(t *testing.T) {
	cases := map[string]string{
		"github.com/charmbracelet/bubbletea": "https://github.com/charmbracelet/bubbletea",
		"gitlab.com/group/project.git":       "https://gitlab.com/group/project.git",
		"https://github.com/a/b":             "https://github.com/a/b",
		"git@github.com:a/b.git":             "git@github.com:a/b.git",
		"ssh://git@host.example/a/b":         "ssh://git@host.example/a/b",
		"src":                                "",
		"./github.com/x":                     "",
		"github.com/only-owner":              "",
		"not a url/with spaces/in it":        "",
	}
	for in, want := range cases {
		got, ok := remoteURL(in)
		if got != want || ok != (want != "") {
			t.Errorf("remoteURL(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	// Something on disk is always local, even if it looks like a URL.
	dir := t.TempDir()
	local := filepath.Join(dir, "github.com", "a", "b")
	os.MkdirAll(local, 0o750)
	wd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(wd)
	if _, ok := remoteURL("github.com/a/b"); ok {
		t.Error("an existing directory must win over the shorthand")
	}
}

func TestCloneRemote(t *testing.T) {
	src := setupGitRepo(t)
	write(t, src, ".sproutrc", "--ignore README.md\n") // must not be honoured
	git(t, src, "add", "-A")
	git(t, src, "commit", "-qm", "init")
	p := filepath.ToSlash(src)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // file:///C:/... on Windows
	}
	url := "file://" + p

	out, errOut, code := runCLI(t, url, "--churn")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(errOut, "cloning") || !strings.Contains(out, "main.go") || !strings.Contains(out, "README.md") {
		t.Errorf("clone output (a cloned .sproutrc must be ignored):\n%s\n%s", out, errOut)
	}
	if strings.Contains(out, os.TempDir()) {
		t.Errorf("the temp path should not be shown:\n%s", out)
	}
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "sprout-*"))
	for _, m := range matches {
		if _, err := os.Stat(filepath.Join(m, filepath.Base(src))); err == nil {
			t.Errorf("clone left behind in %s", m)
		}
	}

	if _, errOut, code := runCLI(t, "file:///definitely/not/a/repo"); code != 1 || !strings.Contains(errOut, "couldn't clone") {
		t.Errorf("bad clone: exit %d: %s", code, errOut)
	}
}

func TestMCPRefusesRemote(t *testing.T) {
	dir := setupTestDir(t)
	arg, _ := json.Marshal(map[string]string{"path": "github.com/a/b"})
	resps := mcpSession(t, dir, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tree","arguments":`+string(arg)+`}}`)
	if _, isErr := toolText(t, resps[1]); !isErr {
		t.Error("MCP must not clone remote repositories")
	}
}
