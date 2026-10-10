package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Completions and the man page are generated from the flag definitions,
// so they can't drift from what the binary actually accepts.

type flagInfo struct {
	name, usage string
	takesValue  bool
}

func allFlags() []flagInfo {
	var out []flagInfo
	newFlagSet(&flags{}, io.Discard).VisitAll(func(f *flag.Flag) {
		_, isBool := f.Value.(interface{ IsBoolFlag() bool })
		out = append(out, flagInfo{f.Name, f.Usage, !isBool})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func dash(name string) string {
	if len(name) == 1 {
		return "-" + name
	}
	return "--" + name
}

var sortValues = "name size time"

// subcommands and their flags, for completion. TestSubcommandFlagsExist
// checks every flag listed here against the real command.
type subcommand struct {
	name, about string
	flags       []flagInfo
	files       bool // takes file arguments
}

var queryFlagInfo = []flagInfo{
	{"depth", "how many hops to follow (-1 for all)", true},
	{"no-tests", "leave test files out", false},
	{"json", "print JSON", false},
}

var subcommands = []subcommand{
	{"deps", "what a file depends on, and why", queryFlagInfo, true},
	{"dependents", "what depends on a file, and why", queryFlagInfo, true},
	{"impact", "what a change could break, and the tests to run", []flagInfo{
		{"staged", "the staged changes", false},
		{"diff", "the changes in a revision range", true},
		{"commit", "the changes in one commit", true},
		{"depth", "how many hops of dependents to follow", true},
		{"all", "list every affected file", false},
		{"json", "print JSON", false},
	}, true},
	{"context", "what to know before editing a file", []flagInfo{
		{"budget", "approximate token budget", true},
		{"json", "print JSON", false},
	}, true},
	{"mcp", "serve Sprout to coding agents over MCP", nil, false},
	{"tour", "guided repository onboarding", []flagInfo{
		{"json", "print versioned JSON", false},
		{"limit", "maximum reading steps and directories (1-50)", true},
	}, true},
}

func writeCompletion(w io.Writer, shell string) error {
	fl := allFlags()
	switch shell {
	case "bash":
		writeBash(w, fl)
	case "zsh":
		writeZsh(w, fl)
	case "fish":
		writeFish(w, fl)
	case "powershell":
		writePowerShell(w, fl)
	default:
		return fmt.Errorf("--completion supports bash, zsh, fish and powershell, not %q", shell)
	}
	return nil
}

const gitRefs = `$(git for-each-ref --format='%(refname:short)' 2>/dev/null)`

func writeBash(w io.Writer, fl []flagInfo) {
	var names, valued, cmds []string
	for _, f := range fl {
		names = append(names, dash(f.name))
		if f.takesValue {
			valued = append(valued, dash(f.name))
		}
	}
	var subs strings.Builder
	for _, c := range subcommands {
		cmds = append(cmds, c.name)
		var cf []string
		for _, f := range c.flags {
			cf = append(cf, dash(f.name))
			if f.takesValue {
				valued = append(valued, dash(f.name))
			}
		}
		files := "COMPREPLY=()"
		if c.files {
			files = `COMPREPLY=($(compgen -f -- "$cur"))`
		}
		fmt.Fprintf(&subs, "            %s) [[ \"$cur\" == -* ]] && COMPREPLY=($(compgen -W \"%s\" -- \"$cur\")) || %s; return ;;\n",
			c.name, strings.Join(cf, " "), files)
	}
	fmt.Fprintf(w, `# bash completion for sprout
_sprout() {
    local cur="${COMP_WORDS[COMP_CWORD]}" prev="${COMP_WORDS[COMP_CWORD-1]}"
    case "$prev" in
        --sort) COMPREPLY=($(compgen -W "%s" -- "$cur")); return ;;
        --completion) COMPREPLY=($(compgen -W "bash zsh fish powershell" -- "$cur")); return ;;
        --diff|--commit) COMPREPLY=($(compgen -W "%s HEAD" -- "$cur")); return ;;
        %s) return ;;
    esac
    if [[ $COMP_CWORD -gt 1 ]]; then
        case "${COMP_WORDS[1]}" in
%s        esac
    fi
    if [[ "$cur" == -* ]]; then
        COMPREPLY=($(compgen -W "%s" -- "$cur"))
        return
    fi
    [[ $COMP_CWORD -eq 1 ]] && COMPREPLY=($(compgen -W "%s" -- "$cur"))
    COMPREPLY+=($(compgen -d -- "$cur"))
}
complete -o filenames -F _sprout sprout
`, sortValues, gitRefs, strings.Join(valued, "|"), subs.String(), strings.Join(names, " "), strings.Join(cmds, " "))
}

func writeZsh(w io.Writer, fl []flagInfo) {
	zflag := func(f flagInfo, dashes string) string {
		desc := zshEscape(f.usage)
		switch {
		case f.name == "sort":
			return fmt.Sprintf("'--sort=[%s]:order:(%s)'", desc, sortValues)
		case f.name == "completion":
			return fmt.Sprintf("'--completion=[%s]:shell:(bash zsh fish powershell)'", desc)
		case f.name == "diff" || f.name == "commit":
			return fmt.Sprintf("'--%s=[%s]:revision:->refs'", f.name, desc)
		case f.takesValue:
			return fmt.Sprintf("'%s%s=[%s]:value:'", dashes, f.name, desc)
		}
		return fmt.Sprintf("'%s%s[%s]'", dashes, f.name, desc)
	}
	fmt.Fprint(w, "#compdef sprout\n\n_sprout() {\n  local state\n  if (( CURRENT > 2 )); then\n    case $words[2] in\n")
	for _, c := range subcommands {
		fmt.Fprintf(w, "      %s) _arguments -s", c.name)
		for _, f := range c.flags {
			fmt.Fprint(w, " "+zflag(f, "--"))
		}
		if c.files {
			fmt.Fprint(w, " '*:file:_files'")
		}
		fmt.Fprint(w, " ;;\n")
	}
	fmt.Fprint(w, "    esac\n  else\n    _arguments -s \\\n")
	for _, f := range fl {
		d := "--"
		if len(f.name) == 1 {
			d = "-"
		}
		fmt.Fprintf(w, "      %s \\\n", zflag(f, d))
	}
	var cmds []string
	for _, c := range subcommands {
		cmds = append(cmds, c.name+`\:"`+zshEscape(c.about)+`"`)
	}
	fmt.Fprintf(w, `      '1: :->first'
  fi
  case $state in
    refs) compadd -- ${(f)"%s"} HEAD ;;
    first) _alternative 'commands:command:((%s))' 'dirs:directory or repository:_files -/' ;;
  esac
}

compdef _sprout sprout
`, gitRefs, strings.Join(cmds, " "))
}

func writeFish(w io.Writer, fl []flagInfo) {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `\'`) + "'" }
	fmt.Fprintln(w, "# fish completion for sprout")
	fmt.Fprintln(w, "complete -c sprout -a 'unterminated")
	fmt.Fprintln(w, "complete -c sprout -f -n '__fish_use_subcommand' -a '(__fish_complete_directories)'")
	for _, c := range subcommands {
		fmt.Fprintf(w, "complete -c sprout -f -n '__fish_use_subcommand' -a %s -d %s\n", c.name, quote(c.about))
		if c.files {
			fmt.Fprintf(w, "complete -c sprout -F -n '__fish_seen_subcommand_from %s'\n", c.name)
		}
		for _, f := range c.flags {
			extra := ""
			switch {
			case f.name == "diff" || f.name == "commit":
				extra = " -x -a '(git for-each-ref --format=\"%(refname:short)\" 2>/dev/null) HEAD'"
			case f.takesValue:
				extra = " -r"
			}
			fmt.Fprintf(w, "complete -c sprout -n '__fish_seen_subcommand_from %s' -l %s%s -d %s\n", c.name, f.name, extra, quote(f.usage))
		}
	}
	for _, f := range fl {
		opt := "-l " + f.name
		if len(f.name) == 1 {
			opt = "-s " + f.name
		}
		extra := ""
		switch {
		case f.name == "sort":
			extra = " -x -a '" + sortValues + "'"
		case f.name == "completion":
			extra = " -x -a 'bash zsh fish powershell'"
		case f.name == "diff":
			extra = " -x -a '(git for-each-ref --format=\"%(refname:short)\" 2>/dev/null)'"
		case f.takesValue:
			extra = " -r"
		}
		fmt.Fprintf(w, "complete -c sprout -n '__fish_use_subcommand' %s%s -d %s\n", opt, extra, quote(f.usage))
	}
}

func writePowerShell(w io.Writer, fl []flagInfo) {
	var names, subs []string
	for _, f := range fl {
		names = append(names, "'"+dash(f.name)+"'")
	}
	for _, c := range subcommands {
		var cf []string
		for _, f := range c.flags {
			cf = append(cf, "'"+dash(f.name)+"'")
		}
		subs = append(subs, fmt.Sprintf("        '%s' = @(%s)", c.name, strings.Join(cf, ", ")))
	}
	fmt.Fprintf(w, `# PowerShell completion for sprout: add to $PROFILE
Register-ArgumentCompleter -Native -CommandName sprout -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)
    $subcommands = @{
%s
    }
    $words = @($commandAst.CommandElements | Select-Object -Skip 1 | ForEach-Object { $_.ToString() })
    if ($words.Count -gt 0 -and $subcommands.ContainsKey($words[0]) -and $words[0] -ne $wordToComplete) {
        $candidates = $subcommands[$words[0]]
    } elseif ($wordToComplete -like '-*') {
        $candidates = @(%s)
    } else {
        $candidates = @($subcommands.Keys)
    }
    $candidates | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
        [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
    }
}
`, strings.Join(subs, "\n"), strings.Join(names, ", "))
}

func zshEscape(s string) string {
	return strings.NewReplacer("[", `\[`, "]", `\]`, ":", `\:`, "'", `'\''`).Replace(s)
}

// writeManPage emits roff for man(1).
func writeManPage(w io.Writer) {
	r := strings.NewReplacer(`\`, `\e`, "-", `\-`, "'", `\(aq`)
	fmt.Fprintf(w, `.TH SPROUT 1 "%s" "sprout %s" "User Commands"
.SH NAME
sprout \- map your codebase, for you and your AI agent
.SH SYNOPSIS
.B sprout
.RI [ path | repository-url ]
.RI [ flags ]
.br
.B sprout mcp
.RI [ root ]
.br
.B sprout deps
.I file
.RB [ \-\-depth
.IR n ]
.RB [ \-\-no\-tests ]
.RB [ \-\-json ]
.br
.B sprout dependents
.I file
.RB [ \-\-depth
.IR n ]
.RB [ \-\-no\-tests ]
.RB [ \-\-json ]
.br
.B sprout impact
.RI [ file ...]
.RB [ \-\-staged " | " \-\-diff
.IR rev " | "
.B \-\-commit
.IR rev ]
.RB [ \-\-all ]
.RB [ \-\-json ]
.br
.B sprout context
.I file
.RB [ \-\-budget
.IR n ]
.br
.B sprout tour
.RI [ path ]
.RB [ \-\-json ]
.RB [ \-\-limit
.IR n ]
.SH DESCRIPTION
Sprout prints a directory tree that respects .gitignore and counts what it hides.
It can mark git changes and commit hotspots in place, show a revision range as a
tree, suggest a reading order, and print a compact, token\-budgeted project map
for LLMs. \fBsprout mcp\fR serves the same views to coding agents over the Model
Context Protocol.
.PP
\fBsprout deps\fR lists the files a file depends on, and \fBsprout dependents\fR the
files that depend on it, each with the reason: the import, or for Go the names it
uses. \fB\-\-depth\fR follows more hops (\-1 for all); results past the first hop say
which file they were reached through.
.PP
\fBsprout impact\fR lists what a change could affect: every file that depends on the
changed ones, directly or through others, and the tests that reach them, with a
\fBgo test\fR command for Go. The change is the files named, or the uncommitted
changes, \fB\-\-staged\fR, \fB\-\-diff\fR \fIrev\fR or \fB\-\-commit\fR \fIrev\fR.
.PP
\fBsprout context\fR prints what to know before editing a file, fitted to a token
budget (\fB\-\-budget\fR, default 1500): what it declares, the signatures it uses from
each dependency, the files that use it and why, and the tests that reach it.
.PP
\fBsprout tour\fR gives an offline reading plan for a local directory: a README
excerpt, detected ecosystems, layout, likely entry points and commonly used files.
\fB\-\-json\fR emits schema-version-1 JSON; \fB\-\-limit\fR bounds reading steps
and top-level directories (default 8, range 1-50), not analysis. Suggestions are
heuristics. No project code is executed, and tree-view config is not loaded.
.PP
To map a folder named like a command (deps, dependents, impact, context, tour), write ./deps.
.SH OPTIONS
`, time.Now().UTC().Format("2006-01-02"), r.Replace(resolveVersion()))
	for _, f := range allFlags() {
		arg := ""
		if f.takesValue {
			arg = " " + `\fIvalue\fR`
		}
		fmt.Fprintf(w, ".TP\n.B %s%s\n%s\n", r.Replace(dash(f.name)), arg, r.Replace(f.usage))
	}
	fmt.Fprint(w, r.Replace(`.SH FILES
.TP
.I ~/.config/sprout/config
Default flags, one per line (also $SPROUT_CONFIG or $XDG_CONFIG_HOME/sprout/config).
.TP
.I .sproutrc
Per-project default flags, found in the target directory or above it.
.TP
.I .sproutignore
Extra gitignore-style patterns for the target directory.
.SH ENVIRONMENT
.TP
.B NO_COLOR
Disable colors.
.TP
.B SPROUT_CONFIG
Path of the user config file.
.SH EXIT STATUS
0 on success, 1 on a runtime error (missing path, not a git repository, failed clone),
2 on invalid flags or arguments.
.SH EXAMPLES
.nf
sprout -L 2
sprout --ai | pbcopy
sprout --diff main...HEAD -L 2
sprout --size --sort size --max-files 5
sprout github.com/owner/repo --entry
sprout dependents internal/auth/session.go --depth 2
sprout impact --diff main...HEAD
sprout tour
sprout tour ./project --json --limit 12
.fi
.SH SEE ALSO
.BR tree (1),
.BR git (1)
.PP
https://sprout-devlabs.github.io/sprout-web/docs.html
`))
}
