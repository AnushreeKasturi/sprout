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
