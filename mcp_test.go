package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// mcpSession sends requests to the server and returns responses by id.
func mcpSession(t *testing.T, root string, reqs ...string) map[float64]map[string]any {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := serveMCP([]string{root}, strings.NewReader(strings.Join(reqs, "\n")), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	resps := map[float64]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("bad response %q: %v", line, err)
		}
		id, _ := r["id"].(float64)
		resps[id] = r
	}
	return resps
}

func toolText(t *testing.T, r map[string]any) (string, bool) {
	t.Helper()
	res, ok := r["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", r)
	}
	content := res["content"].([]any)[0].(map[string]any)
	return content["text"].(string), res["isError"].(bool)
}

func TestMCPHandshakeAndTools(t *testing.T) {
	dir := setupTestDir(t)
	resps := mcpSession(t, dir,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"project_map","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"tree","arguments":{"path":"src"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
	)
	if len(resps) != 5 {
		t.Errorf("notifications must not be answered; got %d responses", len(resps))
	}
	if v := resps[1]["result"].(map[string]any)["protocolVersion"]; v != "2025-06-18" {
		t.Errorf("protocolVersion = %v", v)
	}
	if ins, _ := resps[1]["result"].(map[string]any)["instructions"].(string); !strings.Contains(ins, "impact") {
		t.Errorf("initialize should tell agents when to use the graph tools: %q", ins)
	}
	if n := len(resps[2]["result"].(map[string]any)["tools"].([]any)); n != 8 {
		t.Errorf("tools/list returned %d tools", n)
	}
	if text, isErr := toolText(t, resps[3]); isErr || !strings.Contains(text, "## structure") {
		t.Errorf("project_map failed: %s", text)
	}
	text, isErr := toolText(t, resps[4])
	if isErr || !strings.HasPrefix(text, "src\n") || !strings.Contains(text, "main.go") {
		t.Errorf("tree on subdir: %q", text)
	}
	if strings.Contains(text, dir) {
		t.Errorf("absolute root path leaked into tool output: %q", text)
	}
	if resps[5]["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Errorf("unknown method: %v", resps[5])
	}
}

func TestMCPConfinesPaths(t *testing.T) {
	dir := setupTestDir(t)
	outside := t.TempDir()
	if runtime.GOOS != "windows" {
		if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"..", "../..", "src/../..", outside, "escape", "--all"} {
		if p == "escape" && runtime.GOOS == "windows" {
			continue
		}
		arg, _ := json.Marshal(map[string]string{"path": p})
		resps := mcpSession(t, dir, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tree","arguments":`+string(arg)+`}}`)
		if text, isErr := toolText(t, resps[1]); !isErr {
			t.Errorf("path %q escaped the root:\n%s", p, text)
		}
	}
}

func TestMCPDiffRejectsInjection(t *testing.T) {
	dir := setupGitRepo(t)
	resps := mcpSession(t, dir, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"diff_tree","arguments":{"rev":"--output=pwned"}}}`)
	if _, isErr := toolText(t, resps[1]); !isErr {
		t.Error("option-like rev must be rejected")
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Error("git wrote a file from an injected option")
	}
}

func TestMCPGraphTools(t *testing.T) {
	dir := impactRepo(t)
	write(t, dir, "web/x.ts", "export const x = 3;\n")
	resps := mcpSession(t, dir,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dependents","arguments":{"file":"a/a.go","depth":2}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"deps","arguments":{"file":"web/y.ts"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"impact","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"impact","arguments":{"files":["a/a.go"]}}}`,
	)
	for id, want := range map[float64][]string{
		1: {"3 files (1 test) depend on a/a.go", "c/c.go       via b/b.go · uses b.B"},
		2: {"web/y.ts depends on 1 file", "web/x.ts  imports ./x.js"},
		3: {"(uncommitted changes)", "web/x.ts", "web/y.ts  imports ./x.js"},
		4: {"Affected: 2 files, 1 directly", "go test ./b"},
	} {
		text, isErr := toolText(t, resps[id])
		if isErr {
			t.Errorf("call %v failed: %s", id, text)
		}
		for _, w := range want {
			if !strings.Contains(text, w) {
				t.Errorf("call %v: missing %q in:\n%s", id, w, text)
			}
		}
		if strings.Contains(text, dir) {
			t.Errorf("call %v leaked the absolute root: %s", id, text)
		}
	}
}

func TestMCPGraphToolsConfineFiles(t *testing.T) {
	dir := impactRepo(t)
	outside := filepath.Join(t.TempDir(), "x.go")
	os.WriteFile(outside, []byte("package x\n"), 0o644)
	for _, call := range []string{
		`{"name":"dependents","arguments":{"file":"../x.go"}}`,
		`{"name":"deps","arguments":{"file":"` + filepath.ToSlash(outside) + `"}}`,
		`{"name":"deps","arguments":{"file":"--json"}}`,
		`{"name":"deps","arguments":{}}`,
		`{"name":"impact","arguments":{"files":["../../etc/passwd"]}}`,
		`{"name":"impact","arguments":{"rev":"--output=pwned"}}`,
		`{"name":"impact","arguments":{"commit":"-x"}}`,
	} {
		resps := mcpSession(t, dir, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+call+`}`)
		if text, isErr := toolText(t, resps[1]); !isErr {
			t.Errorf("%s should fail:\n%s", call, text)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Error("git wrote a file from an injected option")
	}
}

// Agents get errors they can act on: paths relative to the project, the
// tool to call instead, and no server paths.
func TestMCPErrorsForAgents(t *testing.T) {
	dir := impactRepo(t)
	call := func(args string) (string, bool) {
		resps := mcpSession(t, dir, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+args+`}`)
		return toolText(t, resps[1])
	}
	resolved, _ := filepath.EvalSymlinks(dir)
	for _, c := range []struct{ args, want string }{
		{`{"name":"deps","arguments":{"file":"a"}}`, "a is a folder; deps needs a file (for where to start in a folder, call reading_order)"},
		{`{"name":"context","arguments":{"file":"."}}`, "reading_order"},
		{`{"name":"deps","arguments":{"file":"missing.go"}}`, "missing.go"},
		{`{"name":"project_map","arguments":{"budget":0}}`, "budget must be at least 1"},
		{`{"name":"context","arguments":{"file":"a/a.go","budget":-5}}`, "budget must be at least 1"},
	} {
		text, isErr := call(c.args)
		if !isErr || !strings.Contains(text, c.want) || strings.Contains(text, dir) || strings.Contains(text, resolved) || strings.Contains(text, "sprout ") {
			t.Errorf("%s: %q", c.args, text)
		}
	}
	if text, isErr := call(`{"name":"project_map","arguments":{"budget":300}}`); isErr {
		t.Errorf("a valid budget failed: %s", text)
	}
}

// mcpRun runs a session started with args (none: "sprout mcp" in the
// current folder) and returns every line it wrote.
func mcpRun(t *testing.T, args []string, reqs ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := serveMCP(args, strings.NewReader(strings.Join(reqs, "\n")), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	return out.String()
}

// Started without a path, the server asks a client that supports MCP roots
// which folder is open, and serves that.
func TestMCPRootsFromClient(t *testing.T) {
	started, open := t.TempDir(), t.TempDir()
	write(t, open, "only_in_open.txt", "x")
	chdir(t, started)
	uri := "file://" + filepath.ToSlash(open)
	if !strings.HasPrefix(uri, "file:///") {
		uri = "file:///" + filepath.ToSlash(open) // Windows: C:/…
	}
	out := mcpRun(t, nil,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"roots":{"listChanged":true}}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":"roots-1","result":{"roots":[{"uri":"`+uri+`","name":"open"}]}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"tree","arguments":{}}}`,
	)
	if !strings.Contains(out, `"method":"roots/list"`) || !strings.Contains(out, "only_in_open.txt") {
		t.Fatalf("roots not used:\n%s", out)
	}

	// A root given on the command line wins: the client isn't asked.
	if out := mcpRun(t, []string{started},
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{"roots":{}}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	); strings.Contains(out, "roots/list") {
		t.Fatalf("asked for roots despite an explicit root:\n%s", out)
	}
}

// Started in the home folder (as desktop apps may do) with nothing better
// to go on, tools refuse instead of walking the whole home folder.
func TestMCPRefusesHomeFolder(t *testing.T) {
	home := t.TempDir()
	write(t, home, "main.go", "package main\n")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	chdir(t, home)
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"project_map","arguments":{}}}`
	if out := mcpRun(t, nil, call); !strings.Contains(out, "won't map it") || !strings.Contains(out, `"isError":true`) {
		t.Fatalf("home folder mapped without a word:\n%s", out)
	}
	if strings.Contains(mcpRun(t, nil, call), home) {
		t.Fatal("the refusal reveals the home path")
	}
	// Asked for explicitly, it's served.
	if out := mcpRun(t, []string{home}, call); strings.Contains(out, `"isError":true`) {
		t.Fatalf("explicit home refused:\n%s", out)
	}
}

// A map over its budget says so to the agent, not only to stderr.
func TestMCPBudgetNote(t *testing.T) {
	dir := impactRepo(t)
	resps := mcpSession(t, dir, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"project_map","arguments":{"budget":5}}}`)
	if text, isErr := toolText(t, resps[1]); isErr || !strings.Contains(text, "Note: the smallest map of this project is about") || strings.Contains(text, dir) {
		t.Errorf("budget note missing:\n%s", text)
	}
}

// --print-config prints valid setup for each client, with sprout's path.
func TestMCPPrintConfig(t *testing.T) {
	for client, key := range map[string]string{"cursor": "mcpServers", "vscode": "servers", "claude-desktop": "mcpServers"} {
		var out, errOut bytes.Buffer
		if code := serveMCP([]string{"--print-config", client}, strings.NewReader(""), &out, &errOut); code != 0 {
			t.Fatalf("%s: exit %d: %s", client, code, errOut.String())
		}
		var cfg map[string]map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		if err := json.Unmarshal(out.Bytes(), &cfg); err != nil {
			t.Fatalf("%s: not JSON: %v\n%s", client, err, out.String())
		}
		s := cfg[key]["sprout"]
		if s.Command == "" || len(s.Args) != 2 || s.Args[0] != "mcp" || !strings.Contains(errOut.String(), "Put this in") {
			t.Errorf("%s: %+v", client, s)
		}
	}
	var out, errOut bytes.Buffer
	serveMCP([]string{"--print-config", "claude-code"}, strings.NewReader(""), &out, &errOut)
	if !strings.HasPrefix(out.String(), "claude mcp add sprout -- ") {
		t.Errorf("claude-code: %q", out.String())
	}
	if code := serveMCP([]string{"--print-config", "zed"}, strings.NewReader(""), &out, &errOut); code != 2 {
		t.Errorf("unknown client: exit %d", code)
	}
}

// A tool call that arrives before the client says which folder is open
// waits for the answer, instead of running on the wrong folder.
func TestMCPCallWaitsForRoots(t *testing.T) {
	started, open := t.TempDir(), t.TempDir()
	write(t, open, "only_in_open.txt", "x")
	chdir(t, started)
	uri := "file://" + filepath.ToSlash(open)
	if !strings.HasPrefix(uri, "file:///") {
		uri = "file:///" + filepath.ToSlash(open)
	}
	out := mcpRun(t, nil,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{"roots":{}}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"tree","arguments":{}}}`, // before the answer
		`{"jsonrpc":"2.0","id":"roots-1","result":{"roots":[{"uri":"`+uri+`"}]}}`,
	)
	if !strings.Contains(out, "only_in_open.txt") {
		t.Fatalf("the early call didn't wait for the open folder:\n%s", out)
	}
}

// Notes over MCP name the tool's arguments, not the CLI's flags.
func TestMCPArgumentNames(t *testing.T) {
	dir := impactRepo(t)
	resps := mcpSession(t, dir, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"project_map","arguments":{"budget":5}}}`)
	if text, _ := toolText(t, resps[1]); strings.Contains(text, "--budget") || !strings.Contains(text, "over budget 5") {
		t.Errorf("CLI flag names reached the agent:\n%s", text)
	}
}

// --print-config names the sprout that's running, not another one on PATH.
func TestMCPPrintConfigRunningBinary(t *testing.T) {
	var out, errOut bytes.Buffer
	serveMCP([]string{"--print-config", "claude-desktop"}, strings.NewReader(""), &out, &errOut)
	self, _ := os.Executable()
	self, _ = filepath.EvalSymlinks(self)
	if !strings.Contains(out.String(), strings.ReplaceAll(self, `\`, `\\`)) {
		t.Errorf("config doesn't name the running binary %s:\n%s", self, out.String())
	}
}
