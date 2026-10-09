package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// MCP (Model Context Protocol) server over stdio: newline-delimited
// JSON-RPC 2.0. Each tool is a thin wrapper that builds CLI arguments and
// calls run(), so the CLI and agents always get identical behaviour.

// mcpInstructions tell the agent when Sprout beats searching by hand.
// Clients add them to the agent's context; without them, agents in our
// evaluation (tools/agent-eval) never used the graph tools, even where grep
// missed most of the answer.
const mcpInstructions = "Sprout maps this codebase: its structure and how files depend on each other, " +
	"resolved the way each language does (Go, TypeScript/JavaScript, Python, Rust, Java, Kotlin). " +
	"Use it instead of grep for questions about relationships between files: dependents and deps for what " +
	"imports a file and what it imports; impact for what a change could break and which tests cover it, " +
	"including tests that reach a file through other files, which text search misses; context before " +
	"editing a file; project_map to get oriented in an unfamiliar repository."

var mcpVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

type rpcRequest struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	args        func(a toolArgs) ([]string, error)
	// run, when set, runs a subcommand against the project root instead of
	// the default view of a directory. File arguments are already confined.
	run func(args []string, root string, out, errOut io.Writer) int
}

type toolArgs struct {
	Path   string `json:"path"`
	Budget int    `json:"budget"`
	Depth  *int   `json:"depth"`
	All    bool   `json:"all"`
	Git    bool   `json:"git"`
	Churn  bool   `json:"churn"`
	Since  string `json:"since"`
	Rev    string `json:"rev"`

	File    string   `json:"file"`
	Files   []string `json:"files"`
	Staged  bool     `json:"staged"`
	Commit  string   `json:"commit"`
	NoTests bool     `json:"noTests"`
}

func schema(props map[string]any, required ...string) map[string]any {
	props["path"] = map[string]any{"type": "string", "description": "Directory relative to the project root. Defaults to the root."}
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var mcpTools = []mcpTool{
	{
		Name: "project_map",
		Description: "Compact overview of a codebase for getting oriented: purpose, stack, languages, " +
			"entry points, config/CI files, uncommitted work, recent hotspots, directory structure, and the " +
			"most-used files with their function and type signatures, " +
			"fitted to a token budget. Call this first in an unfamiliar repository.",
		InputSchema: schema(map[string]any{
			"budget": map[string]any{"type": "integer", "description": "Approximate token budget (default 2000)."},
		}),
		args: func(a toolArgs) ([]string, error) {
			args := []string{"--ai"}
			if a.Budget > 0 {
				args = append(args, "--budget", strconv.Itoa(a.Budget))
			}
			return args, nil
		},
	},
	{
		Name: "tree",
		Description: "Directory tree respecting .gitignore. Optionally mark git status (git) or " +
			"per-path commit counts to find hotspots (churn).",
		InputSchema: schema(map[string]any{
			"depth": map[string]any{"type": "integer", "description": "Maximum depth (default 3; -1 for unlimited)."},
			"all":   map[string]any{"type": "boolean", "description": "Include hidden and ignored entries."},
			"git":   map[string]any{"type": "boolean", "description": "Mark changed files: M, A, D, R, ?, U."},
			"churn": map[string]any{"type": "boolean", "description": "Show commits touching each path."},
			"since": map[string]any{"type": "string", "description": "With churn: only count commits since, e.g. '90 days ago'."},
		}),
		args: func(a toolArgs) ([]string, error) {
			depth := 3 // an unbounded tree of a big repo would flood the agent's context
			if a.Depth != nil {
				depth = *a.Depth
			}
			args := []string{"--depth", strconv.Itoa(depth)}
			if a.All {
				args = append(args, "--all")
			}
			if a.Git {
				args = append(args, "--git")
			}
			if a.Churn {
				args = append(args, "--churn")
				if a.Since != "" {
					args = append(args, "--since", a.Since)
				}
			}
			return args, nil
		},
	},
	{
		Name: "reading_order",
		Description: "Where to start reading an unfamiliar codebase: the README, entry points, then the " +
			"files the rest of the code imports most, with the reason for each.",
		InputSchema: schema(map[string]any{}),
		args:        func(a toolArgs) ([]string, error) { return []string{"--entry"}, nil },
	},
	{
		Name: "diff_tree",
		Description: "Paths changed in a git revision range as a tree with per-directory +/- line totals. " +
			"Use rev 'main...HEAD' to see what the current branch changed.",
		InputSchema: schema(map[string]any{
			"rev":   map[string]any{"type": "string", "description": "Git revision or range, e.g. 'main...HEAD', 'HEAD~3'."},
			"depth": map[string]any{"type": "integer", "description": "Collapse directories below this depth into totals."},
		}, "rev"),
		args: func(a toolArgs) ([]string, error) {
			args := []string{"--diff", a.Rev}
			if a.Depth != nil {
				args = append(args, "--depth", strconv.Itoa(*a.Depth))
			}
			return args, nil
		},
	},
}

// graphTools answer questions about the dependency graph. Paths are
// relative to the project root.
var graphTools = []mcpTool{
	{
		Name: "dependents",
		Description: "Which files depend on a file, with why: the import, or for Go the names they use. " +
			"Tests included. Use before changing a file to see what relies on it.",
		InputSchema: fileSchema(map[string]any{
			"depth":   map[string]any{"type": "integer", "description": "Hops to follow (default 1; -1 for all)."},
			"noTests": map[string]any{"type": "boolean", "description": "Leave test files out."},
		}),
		args: func(a toolArgs) ([]string, error) { return queryArgs(a), nil },
		run: func(args []string, root string, out, errOut io.Writer) int {
			return runQuery("dependents", args, root, out, errOut)
		},
	},
	{
		Name:        "deps",
		Description: "Which files a file depends on, with why. Use to understand what a file builds on.",
		InputSchema: fileSchema(map[string]any{
			"depth": map[string]any{"type": "integer", "description": "Hops to follow (default 1; -1 for all)."},
		}),
		args: func(a toolArgs) ([]string, error) { return queryArgs(a), nil },
		run: func(args []string, root string, out, errOut io.Writer) int {
			return runQuery("deps", args, root, out, errOut)
		},
	},
	{
		Name: "impact",
		Description: "What a change could break: every file depending on the changed ones, directly or through " +
			"others, and the tests to run (with a go test command for Go). The change is the given files, or " +
			"from git: uncommitted changes (default), staged, a revision range, or one commit. " +
			"Use after editing, before running tests or opening a pull request.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"files":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Changed files, relative to the project root."},
			"staged": map[string]any{"type": "boolean", "description": "Use the staged changes."},
			"rev":    map[string]any{"type": "string", "description": "Use the changes in a revision range, e.g. 'main...HEAD'."},
			"commit": map[string]any{"type": "string", "description": "Use the changes in one commit, e.g. 'HEAD'."},
			"all":    map[string]any{"type": "boolean", "description": "List every affected file, not just direct dependents."},
		}},
		args: func(a toolArgs) ([]string, error) {
			args := append([]string{}, a.Files...)
			switch {
			case a.Staged:
				args = append(args, "--staged")
			case a.Rev != "":
				args = append(args, "--diff", a.Rev)
			case a.Commit != "":
				args = append(args, "--commit", a.Commit)
			}
			if a.All {
				args = append(args, "--all")
			}
			return args, nil
		},
		run: runImpact,
	},
	{
		Name: "context",
		Description: "Everything to know before editing one file, fitted to a token budget: what it declares, " +
			"the signatures it uses from each dependency, the files that use it and why, and the tests that " +
			"reach it. Call before editing a file you haven't read the neighbours of.",
		InputSchema: fileSchema(map[string]any{
			"budget": map[string]any{"type": "integer", "description": "Approximate token budget (default 1500)."},
		}),
		args: func(a toolArgs) ([]string, error) {
			args := []string{a.File}
			if a.Budget > 0 {
				args = append(args, "--budget", strconv.Itoa(a.Budget))
			}
			return args, nil
		},
		run: runContext,
	},
}

func init() { mcpTools = append(mcpTools, graphTools...) }

func fileSchema(props map[string]any) map[string]any {
	props["file"] = map[string]any{"type": "string", "description": "File path relative to the project root."}
	return map[string]any{"type": "object", "properties": props, "required": []string{"file"}}
}

func queryArgs(a toolArgs) []string {
	args := []string{a.File}
	if a.Depth != nil {
		args = append(args, "--depth", strconv.Itoa(*a.Depth))
	}
	if a.NoTests {
		args = append(args, "--no-tests")
	}
	return args
}

func serveMCP(args []string, in io.Reader, out, errOut io.Writer) int {
	root := "."
	if len(args) > 0 {
		root = args[0]
	}
	if root == "-h" || root == "--help" || root == "-help" {
		fmt.Fprintln(out, "Usage: sprout mcp [root]\n\nServes Sprout to coding agents over MCP on stdio, for the project at root (default .).")
		return 0
	}
	root, err := resolveRoot(root)
	if err != nil {
		fmt.Fprintln(errOut, "sprout mcp:", err)
		return 1
	}
	fmt.Fprintf(errOut, "sprout %s: MCP server on stdio, root %s\n", resolveVersion(), root)

	enc := json.NewEncoder(out)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 10<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			enc.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcError{-32700, "parse error"}})
			continue
		}
		result, rerr := handleMCP(root, req)
		if req.ID == nil {
			continue // notification: never answered
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rerr != nil {
			resp["error"] = rerr
		} else {
			resp["result"] = result
		}
		if err := enc.Encode(resp); err != nil {
			return 1
		}
	}
	return 0
}

func handleMCP(root string, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		version := mcpVersions[0]
		if slices.Contains(mcpVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "sprout", "version": resolveVersion()},
			"instructions":    mcpInstructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": mcpTools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		i := slices.IndexFunc(mcpTools, func(t mcpTool) bool { return t.Name == p.Name })
		if i < 0 {
			return nil, &rpcError{-32602, "unknown tool: " + p.Name}
		}
		text, err := callTool(root, mcpTools[i], p.Arguments)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(text, false), nil
	}
	if strings.HasPrefix(req.Method, "notifications/") {
		return nil, nil
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

func callTool(root string, tool mcpTool, raw json.RawMessage) (string, error) {
	var a toolArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", fmt.Errorf("invalid arguments: %v", err)
		}
	}
	if tool.run != nil {
		return callGraphTool(root, tool, a)
	}
	dir, err := confine(root, a.Path)
	if err != nil {
		return "", err
	}
	args, err := tool.args(a)
	if err != nil {
		return "", err
	}
	var out, errOut bytes.Buffer
	if code := run(append(args, "--", dir), &out, &errOut); code != 0 {
		return "", fmt.Errorf("%s", strings.TrimSpace(errOut.String()))
	}
	// Show the path the agent asked for, not the user's absolute home path.
	shown := filepath.ToSlash(filepath.Join(".", a.Path))
	return strings.Replace(out.String(), dir, shown, 1), nil
}

// callGraphTool confines every file argument to the root, then runs the
// tool's subcommand against the root.
func callGraphTool(root string, tool mcpTool, a toolArgs) (string, error) {
	if tool.Name != "impact" && a.File == "" {
		return "", fmt.Errorf("file is required")
	}
	confineFile := func(p string) (string, error) {
		if strings.HasPrefix(p, "-") {
			return "", fmt.Errorf("invalid file %q", p)
		}
		return confine(root, p)
	}
	var err error
	if a.File != "" {
		if a.File, err = confineFile(a.File); err != nil {
			return "", err
		}
	}
	for i, f := range a.Files {
		if a.Files[i], err = confineFile(f); err != nil {
			return "", err
		}
	}
	for _, rev := range []string{a.Rev, a.Commit} {
		if strings.HasPrefix(rev, "-") {
			return "", fmt.Errorf("invalid revision %q", rev)
		}
	}
	args, err := tool.args(a)
	if err != nil {
		return "", err
	}
	var out, errOut bytes.Buffer
	if code := tool.run(args, root, &out, &errOut); code != 0 {
		return "", fmt.Errorf("%s", strings.TrimSpace(errOut.String()))
	}
	return out.String(), nil
}

func resolveRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// confine resolves p against root and refuses anything that lands outside
// it, including via "..", absolute paths, or symlinks. An agent's arguments
// are untrusted input.
func confine(root, p string) (string, error) {
	if filepath.IsAbs(p) {
		return "", fmt.Errorf("path must be relative to the project root")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, p))
	if err != nil {
		return "", fmt.Errorf("path %q: %v", p, errReason(err))
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q is outside the project root", p)
	}
	return resolved, nil
}
