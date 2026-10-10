package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
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
	Budget *int   `json:"budget"` // nil when not given
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
			return budgetArgs([]string{"--ai"}, a.Budget)
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
			return budgetArgs([]string{a.File}, a.Budget)
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
		fmt.Fprintln(out, "Usage: sprout mcp [root]\n       sprout mcp --print-config claude-code|cursor|vscode|claude-desktop [project]\n\nServes Sprout to coding agents over MCP on stdio, for the project at root.\nWithout one, the folder the client says is open (MCP roots), else the current folder.\n--print-config prints the setup for a client, with this sprout's full path.")
		return 0
	}
	if root == "--print-config" {
		return printMCPConfig(args[1:], out, errOut)
	}
	root, err := resolveRoot(root)
	if err != nil {
		fmt.Fprintln(errOut, "sprout mcp:", err)
		return 1
	}
	s := &mcpConn{root: root, explicit: len(args) > 0, enc: json.NewEncoder(out), errOut: errOut}
	fmt.Fprintf(errOut, "sprout %s: MCP server on stdio, root %s\n", resolveVersion(), root)
	if s.broad() {
		fmt.Fprintf(errOut, "sprout mcp: %s isn't a project; tools will refuse it until the client names its open folder or you pass a path: sprout mcp /path/to/project\n", root)
	}

	return s.run(readLines(in))
}

// readLines sends each trimmed line of in, on its own goroutine, so tool
// calls can wait briefly for the client to say which folder is open.
func readLines(in io.Reader) <-chan []byte {
	lines := make(chan []byte)
	go func() {
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 0, 64*1024), 10<<20)
		for sc.Scan() {
			lines <- bytes.Clone(bytes.TrimSpace(sc.Bytes()))
		}
		close(lines)
	}()
	return lines
}

// run answers the client until its input ends, and returns an exit code.
func (s *mcpConn) run(lines <-chan []byte) int {
	var timeout <-chan time.Time
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return s.flush() // end of input: answer what's waiting
			}
			if s.handle(line) != nil {
				return 1
			}
			if len(s.pending) > 0 && timeout == nil {
				timeout = time.After(rootsWait)
			}
		case <-timeout:
			timeout, s.answered = nil, s.asked // the client never said: go on as we are
			if s.flush() != 0 {
				return 1
			}
		}
	}
}

// rootsWait is how long tool calls wait for the client's open folder.
const rootsWait = 3 * time.Second

// handle acts on one line from the client.
func (s *mcpConn) handle(line []byte) error {
	if len(line) == 0 {
		return nil
	}
	var msg struct {
		rpcRequest
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		return s.enc.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcError{-32700, "parse error"}})
	}
	if msg.Method == "" {
		s.takeRoots(msg.ID, msg.Result) // a response to our roots/list
		if s.flush() != 0 {
			return fmt.Errorf("writing a response")
		}
		return nil
	}
	if msg.Method == "tools/call" && s.asked > s.answered {
		s.pending = append(s.pending, msg.rpcRequest) // answered once the open folder is known
		return nil
	}
	return s.serve(msg.rpcRequest)
}

// flush answers the tool calls that waited for the open folder, once it's
// known or no longer expected. It returns an exit code.
func (s *mcpConn) flush() int {
	if s.asked > s.answered {
		return 0
	}
	for len(s.pending) > 0 {
		req := s.pending[0]
		s.pending = s.pending[1:]
		if s.serve(req) != nil {
			return 1
		}
	}
	return 0
}

// runningSprout is the full path of this sprout, for configs: its PATH
// entry when that is this same binary (Homebrew's bin link outlives
// upgrades, the binary's own path is versioned), else the binary itself.
func runningSprout() string {
	self, err := os.Executable()
	if err != nil {
		return "sprout"
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	if onPath, err := exec.LookPath("sprout"); err == nil {
		if abs, err := filepath.Abs(onPath); err == nil {
			if resolved, err := filepath.EvalSymlinks(abs); err == nil && resolved == self {
				return abs
			}
		}
	}
	return self
}

// mcpWords rewrites the CLI's flag names in output for agents, whose tools
// take arguments: "raise --budget" means the budget argument over MCP.
var mcpWords = strings.NewReplacer("--budget", "budget", "--all", "all")

// printMCPConfig prints a client's setup for this sprout: args are the
// client and, for clients that don't know the project, its folder.
func printMCPConfig(args []string, out, errOut io.Writer) int {
	clients := "claude-code, cursor, vscode or claude-desktop"
	if len(args) == 0 || len(args) > 2 {
		fmt.Fprintln(errOut, "sprout mcp: --print-config needs a client:", clients)
		return 2
	}
	exe := runningSprout()
	project := "."
	if len(args) == 2 {
		project = args[1]
	}
	project, err := filepath.Abs(project)
	if err != nil {
		fmt.Fprintln(errOut, "sprout mcp:", err)
		return 1
	}
	server := func(extra map[string]any, mcpArgs ...string) map[string]any {
		m := map[string]any{"command": exe, "args": append([]string{"mcp"}, mcpArgs...)}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	var config any
	where := ""
	switch args[0] {
	case "claude-code":
		fmt.Fprintf(out, "claude mcp add sprout -- %s mcp\n", exe)
		return 0
	case "cursor":
		config, where = map[string]any{"mcpServers": map[string]any{"sprout": server(map[string]any{"type": "stdio"}, "${workspaceFolder}")}}, ".cursor/mcp.json in your project, or ~/.cursor/mcp.json"
	case "vscode":
		config, where = map[string]any{"servers": map[string]any{"sprout": server(map[string]any{"type": "stdio"}, "${workspaceFolder}")}}, ".vscode/mcp.json in your project"
	case "claude-desktop":
		config, where = map[string]any{"mcpServers": map[string]any{"sprout": server(nil, project)}}, "Claude Desktop: Settings > Developer > Edit Config"
	default:
		fmt.Fprintf(errOut, "sprout mcp: unknown client %q; use %s\n", args[0], clients)
		return 2
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	fmt.Fprintf(errOut, "Put this in %s:\n", where)
	fmt.Fprintf(out, "%s\n", data)
	return 0
}

// mcpConn is one client connection. Its root can change once: when no
// root was given, a client that supports MCP roots says which folder is open.
type mcpConn struct {
	root     string
	explicit bool         // given on the command line, or by the client
	canRoots bool         // the client answers roots/list
	asked    int          // roots/list requests sent, with ids "roots-1", "roots-2"…
	answered int          // and how many of them were answered
	pending  []rpcRequest // tool calls waiting for the answer
	enc      *json.Encoder
	errOut   io.Writer
}

// serve answers one request, or acts on a notification.
func (s *mcpConn) serve(req rpcRequest) error {
	switch req.Method {
	case "initialize":
		var p struct {
			Capabilities struct {
				Roots *json.RawMessage `json:"roots"`
			} `json:"capabilities"`
		}
		json.Unmarshal(req.Params, &p)
		s.canRoots = p.Capabilities.Roots != nil
	case "notifications/initialized", "notifications/roots/list_changed":
		s.askRoots()
	}
	var result any
	var rerr *rpcError
	if req.Method == "tools/call" && s.broad() {
		result = toolResult(broadRootMessage, true)
	} else {
		result, rerr = handleMCP(s.root, req)
	}
	if req.ID == nil {
		return nil // notification: never answered
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rerr != nil {
		resp["error"] = rerr
	} else {
		resp["result"] = result
	}
	return s.enc.Encode(resp)
}

// askRoots asks the client which folders are open, unless a root was given.
func (s *mcpConn) askRoots() {
	if s.explicit || !s.canRoots {
		return
	}
	s.asked++
	s.enc.Encode(map[string]any{"jsonrpc": "2.0", "id": fmt.Sprintf("roots-%d", s.asked), "method": "roots/list"})
}

// takeRoots serves the first local folder from a roots/list answer.
func (s *mcpConn) takeRoots(id, result json.RawMessage) {
	var name string
	if json.Unmarshal(id, &name) != nil || !strings.HasPrefix(name, "roots-") {
		return
	}
	s.answered++
	if s.explicit {
		return
	}
	var r struct {
		Roots []struct {
			URI string `json:"uri"`
		} `json:"roots"`
	}
	json.Unmarshal(result, &r)
	for _, root := range r.Roots {
		if dir, ok := fileURIPath(root.URI); ok {
			if resolved, err := resolveRoot(dir); err == nil {
				s.root, s.explicit = resolved, true
				fmt.Fprintf(s.errOut, "sprout mcp: the client's open folder, root %s\n", resolved)
				return
			}
		}
	}
}

// fileURIPath is the local path of a file:// URI.
func fileURIPath(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return "", false
	}
	p := u.Path
	if len(p) > 2 && p[0] == '/' && p[2] == ':' {
		p = p[1:] // file:///C:/project on Windows
	}
	return filepath.FromSlash(p), true
}

// broad reports a root that is the whole filesystem or the home folder,
// which the server reached only because the client started it there: mapping
// it would walk hundreds of thousands of files nobody asked about.
func (s *mcpConn) broad() bool {
	if s.explicit {
		return false
	}
	home, _ := os.UserHomeDir()
	if h, err := filepath.EvalSymlinks(home); err == nil {
		home = h
	}
	return filepath.Dir(s.root) == s.root || s.root == home
}

const broadRootMessage = "Sprout was started in the filesystem root or the home folder, not in a project, " +
	"so it won't map it. Ask the user to give the server the project's path in its MCP config " +
	`("args": ["mcp", "/path/to/project"]).`

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
	text := strings.Replace(out.String(), dir, shown, 1)
	// Notes that succeeded anyway (a map over its budget, --git outside a
	// repository) would otherwise only reach a terminal nobody watches.
	for _, line := range strings.Split(strings.TrimSpace(errOut.String()), "\n") {
		if note := strings.TrimPrefix(line, "sprout: "); note != "" {
			text += "\nNote: " + strings.ReplaceAll(note, dir, shown)
		}
	}
	return mcpWords.Replace(text), nil
}

// budgetArgs adds --budget to args when the agent gave one; 0 or less is an
// error rather than quietly the default.
func budgetArgs(args []string, budget *int) ([]string, error) {
	if budget == nil {
		return args, nil
	}
	if *budget < 1 {
		return nil, fmt.Errorf("budget must be at least 1")
	}
	return append(args, "--budget", strconv.Itoa(*budget)), nil
}

// callGraphTool confines every file argument to the root, then runs the
// tool's subcommand against the root. Errors name paths relative to the
// root: the agent doesn't need to learn where the server runs.
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
		given := a.File
		if a.File, err = confineFile(a.File); err != nil {
			return "", err
		}
		if info, err := os.Stat(a.File); err == nil && info.IsDir() {
			return "", fmt.Errorf("%s is a folder; %s needs a file (for where to start in a folder, call reading_order)", given, tool.Name)
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
		msg := strings.ReplaceAll(errOut.String(), root+string(filepath.Separator), "")
		return "", fmt.Errorf("%s", strings.TrimSpace(strings.ReplaceAll(msg, root, ".")))
	}
	return mcpWords.Replace(out.String()), nil
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
