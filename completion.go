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

func writeCompletion(w io.Writer, shell string) error {
	fl := allFlags()
	switch shell {
	case "bash":
		var names []string
		var valued []string
		for _, f := range fl {
			names = append(names, dash(f.name))
			if f.takesValue {
				valued = append(valued, dash(f.name))
			}
		}
		fmt.Fprintf(w, `# bash completion for sprout
_sprout() {
    local cur="${COMP_WORDS[COMP_CWORD]}" prev="${COMP_WORDS[COMP_CWORD-1]}"
    case "$prev" in
        --sort) COMPREPLY=($(compgen -W "%s" -- "$cur")); return ;;
        --completion) COMPREPLY=($(compgen -W "bash zsh fish powershell" -- "$cur")); return ;;
        --diff) COMPREPLY=($(compgen -W "$(git for-each-ref --format='%%(refname:short)' 2>/dev/null)" -- "$cur")); return ;;
        %s) return ;;
    esac
    if [[ "$cur" == -* ]]; then
        COMPREPLY=($(compgen -W "%s" -- "$cur"))
        return
    fi
    [[ $COMP_CWORD -eq 1 ]] && COMPREPLY=($(compgen -W "mcp" -- "$cur"))
    COMPREPLY+=($(compgen -d -- "$cur"))
}
complete -o filenames -F _sprout sprout
`, sortValues, strings.Join(valued, "|"), strings.Join(names, " "))
	case "zsh":
		fmt.Fprintln(w, "#compdef sprout\n\n_sprout() {\n  _arguments -s \\")
		for _, f := range fl {
			desc := zshEscape(f.usage)
			switch {
			case f.name == "sort":
				fmt.Fprintf(w, "    '--sort=[%s]:order:(%s)' \\\n", desc, sortValues)
			case f.name == "completion":
				fmt.Fprintf(w, "    '--completion=[%s]:shell:(bash zsh fish powershell)' \\\n", desc)
			case f.name == "diff":
				fmt.Fprintf(w, "    '--diff=[%s]:revision:->refs' \\\n", desc)
			case f.takesValue:
				fmt.Fprintf(w, "    '%s=[%s]:value:' \\\n", dash(f.name), desc)
			default:
				fmt.Fprintf(w, "    '%s[%s]' \\\n", dash(f.name), desc)
			}
		}
		fmt.Fprint(w, `    '1:directory or repository:_files -/' && return
  case $state in
    refs) compadd -- ${(f)"$(git for-each-ref --format='%(refname:short)' 2>/dev/null)"} ;;
  esac
}

compdef _sprout sprout
`)
	case "fish":
		fmt.Fprintln(w, "# fish completion for sprout\ncomplete -c sprout -f -a '(__fish_complete_directories)'")
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
			fmt.Fprintf(w, "complete -c sprout %s%s -d '%s'\n", opt, extra, strings.ReplaceAll(f.usage, "'", `\'`))
		}
	case "powershell":
		var names []string
		for _, f := range fl {
			names = append(names, "'"+dash(f.name)+"'")
		}
		fmt.Fprintf(w, `# PowerShell completion for sprout: add to $PROFILE
Register-ArgumentCompleter -Native -CommandName sprout -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)
    $flags = @(%s)
    $flags | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
        [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterName', $_)
    }
}
`, strings.Join(names, ", "))
	default:
		return fmt.Errorf("--completion supports bash, zsh, fish and powershell, not %q", shell)
	}
	return nil
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
To map a folder named like a command (deps, dependents, impact, context), write ./deps.
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
.fi
.SH SEE ALSO
.BR tree (1),
.BR git (1)
.PP
https://sprout-devlabs.github.io/sprout-web/docs.html
`))
}
