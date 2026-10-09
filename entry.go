package main

import (
	"fmt"
	"io"
	"strings"
)

type readingStep struct {
	rel, why string
}

// readingOrder suggests where to start in an unfamiliar codebase: what the
// project is, where execution starts, then the code everything else
// depends on, most used first.
func readingOrder(root string, t *Tree, g *Graph, limit int) []readingStep {
	var steps []readingStep
	seen := map[string]bool{}
	add := func(rel, why string) {
		if !seen[rel] && len(steps) < limit {
			seen[rel] = true
			steps = append(steps, readingStep{rel, why})
		}
	}
	if name := readmeFile(root); name != "" {
		add(name, "what the project is")
	}
	entries, _ := keyFiles(t.Root)
	for _, e := range entries {
		if !strings.HasPrefix(e, "+") {
			add(e, "entry point")
		}
	}
	for _, id := range g.Ranked() {
		add(g.Files[id].Rel, "used by "+plural(g.UsedBy(id), "file"))
	}
	return steps
}

func printReadingOrder(w io.Writer, name string, steps []readingStep) {
	if len(steps) == 0 {
		fmt.Fprintln(w, "sprout: no README, entry points or local imports found to rank")
		return
	}
	width := 0
	for _, s := range steps {
		width = max(width, len(s.rel))
	}
	fmt.Fprintf(w, "Reading order for %s\n\n", name)
	for i, s := range steps {
		fmt.Fprintf(w, "%3d. %-*s  %s\n", i+1, width, s.rel, s.why)
	}
}

// writeKeyFiles lists the most imported files with their declarations,
// spending at most budget tokens.
func writeKeyFiles(b *strings.Builder, g *Graph, ranked []FileID, budget int) {
	const maxSymbols = 8
	head := "\n## key files (most used first)\n"
	used := estimateTokens(head)
	var lines []string
	for _, id := range ranked {
		f := g.Files[id]
		syms := f.Symbols
		more := ""
		if len(syms) > maxSymbols {
			more = fmt.Sprintf("; +%d more", len(syms)-maxSymbols)
			syms = syms[:maxSymbols]
		}
		line := fmt.Sprintf("%s (used by %d)", f.Rel, g.UsedBy(id))
		if len(syms) > 0 {
			line += ": " + strings.Join(syms, "; ") + more
		}
		cost := estimateTokens(line + "\n")
		if used+cost > budget {
			break
		}
		used += cost
		lines = append(lines, line)
	}
	if len(lines) > 0 {
		b.WriteString(head + strings.Join(lines, "\n") + "\n")
	}
}
