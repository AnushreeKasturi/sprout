package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// shorthand matches host/owner/repo, e.g. github.com/charmbracelet/bubbletea.
var shorthand = regexp.MustCompile(`^[a-z0-9.-]+\.[a-z]{2,}/[\w.-]+/[\w.-]+(?:\.git)?/?$`)

// remoteURL reports whether arg names a repository to clone rather than a
// local directory, and the URL to clone it from. Anything that exists on
// disk is always local.
func remoteURL(arg string) (string, bool) {
	if _, err := os.Stat(arg); err == nil {
		return "", false
	}
	for _, scheme := range []string{"https://", "http://", "ssh://", "git://", "file://"} {
		if strings.HasPrefix(arg, scheme) {
			return arg, true
		}
	}
	if strings.HasPrefix(arg, "git@") && strings.Contains(arg, ":") {
		return arg, true
	}
	if shorthand.MatchString(arg) {
		return "https://" + strings.TrimSuffix(arg, "/"), true
	}
	return "", false
}

// cloneRemote makes a throwaway clone and returns its directory and a
// cleanup function. history fetches every commit (but no old file
// contents) for --git, --churn and --diff; otherwise one commit is enough.
func cloneRemote(url string, history bool, stderr io.Writer) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "sprout-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(tmp) }

	// Ctrl-C mid-clone or mid-walk shouldn't leave a copy of the repo behind.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		if _, ok := <-sig; ok {
			cleanup()
			os.Exit(130)
		}
	}()
	done := cleanup
	cleanup = func() {
		signal.Stop(sig)
		close(sig)
		done()
	}

	name := strings.TrimSuffix(filepath.Base(strings.TrimRight(url, "/")), ".git")
	if i := strings.LastIndexAny(name, ":/"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || name == "." || name == ".." {
		name = "repo"
	}
	dir := filepath.Join(tmp, name)

	args := []string{"clone", "--quiet", "--no-tags"}
	if history {
		args = append(args, "--filter=blob:none")
	} else {
		args = append(args, "--depth=1", "--single-branch")
	}
	fmt.Fprintf(stderr, "sprout: cloning %s\n", url)
	cmd := exec.Command("git", append(args, "--", url, dir)...) // "--": the URL can't be read as an option
	// Credential helpers (gh auth setup-git, the macOS keychain) still sign
	// in; git just doesn't stop to ask for a username nobody can type.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("couldn't clone %s: the repository doesn't exist, is private, or can't be reached (for a private one, set up git credentials, e.g. gh auth setup-git)", url)
	}
	return dir, cleanup, nil
}
