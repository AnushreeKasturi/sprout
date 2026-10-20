package main

import (
	"slices"
	"testing"
)

func depsOf(g *Graph, rel string) []string {
	id, ok := g.ID(rel)
	if !ok {
		return nil
	}
	var out []string
	for _, d := range g.Deps(id) {
		out = append(out, g.Files[d].Rel)
	}
	slices.Sort(out)
	return out
}

func wantDeps(t *testing.T, g *Graph, rel string, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := depsOf(g, rel); !slices.Equal(got, want) {
		t.Errorf("deps of %s = %v, want %v", rel, got, want)
	}
}

func TestJSImportsSkipCommentsAndSpanLines(t *testing.T) {
	src := []byte(`// import {gone} from './commented';
/* import x from './block'
   export * from './also-block' */
import {
  a,
  b,
} from './multi';
export {c} from "./reexport";
const s = "// not a comment"; import('./lazy');
const re = require('./cjs');
`)
	got := jsImports(src)
	slices.Sort(got)
	want := []string{"./cjs", "./lazy", "./multi", "./reexport"}
	if !slices.Equal(got, want) {
		t.Errorf("jsImports = %v, want %v", got, want)
	}
}

func TestTSConfigPaths(t *testing.T) {
	g := graphFor(t, map[string]string{
		// Comments and trailing commas are allowed in tsconfig files.
		"tsconfig.base.json":            `{ "compilerOptions": { /* aliases */ "baseUrl": ".", "paths": { "@lib/*": ["lib/*"], }, }, }`,
		"app/tsconfig.json":             `{ "extends": "../tsconfig.base.json" }`,
		"app/page.tsx":                  "import {u} from '@lib/util';\nimport {T} from 'types';\nimport React from 'react';\n",
		"lib/util.ts":                   "export const u = 1;\n",
		"types/index.d.ts":              "export type T = string;\n",
		"web/tsconfig.json":             `{ "compilerOptions": { "paths": { "@/*": ["./src/*"] } } }`,
		"web/src/main.ts":               "import {b} from '@/components/button';\n",
		"web/src/components/button.tsx": "export const b = 1;\n",
	})
	wantDeps(t, g, "app/page.tsx", "lib/util.ts", "types/index.d.ts")
	wantDeps(t, g, "web/src/main.ts", "web/src/components/button.tsx")
}

func TestJSWorkspacePackages(t *testing.T) {
	g := graphFor(t, map[string]string{
		"packages/ui/package.json":     `{"name": "@acme/ui", "exports": {".": "./src/index.ts", "./button": "./src/button.tsx", "./icons/*": "./src/icons/*.tsx"}}`,
		"packages/ui/src/index.ts":     "export * from './button';\n",
		"packages/ui/src/button.tsx":   "export const Button = 1;\n",
		"packages/ui/src/icons/x.tsx":  "export const X = 1;\n",
		"packages/api/package.json":    `{"name": "@acme/api", "exports": {".": {"types": "./dist/index.d.ts", "default": "./src/index.ts"}}}`,
		"packages/api/src/index.ts":    "export const api = 1;\n",
		"packages/util/package.json":   `{"name": "@acme/util", "main": "lib/main.js"}`,
		"packages/util/lib/main.js":    "module.exports = {};\n",
		"packages/ui-kit/package.json": `{"name": "@acme/ui-kit"}`,
		"packages/ui-kit/index.ts":     "export const kit = 1;\n",
		"apps/web/app.tsx": `import {Button} from '@acme/ui/button';
import * as ui from '@acme/ui';
import {X} from '@acme/ui/icons/x';
import {api} from '@acme/api';
import util from '@acme/util';
import {kit} from '@acme/ui-kit';
`,
	})
	wantDeps(t, g, "apps/web/app.tsx",
		"packages/ui/src/button.tsx", "packages/ui/src/index.ts", "packages/ui/src/icons/x.tsx",
		"packages/api/src/index.ts", "packages/util/lib/main.js", "packages/ui-kit/index.ts")
}

// TestJSSubpathImports: #specifiers resolve through the "imports" of the
// nearest package.json, named or not: exact keys, the longest pattern, and
// targets that are packages of the repository.
func TestJSSubpathImports(t *testing.T) {
	g := graphFor(t, map[string]string{
		"package.json": `{"imports": {"#db": "./src/db/index.ts", "#utils/*": "./src/utils/*.ts",
			"#utils/special/*": {"types": "./types/*.d.ts", "default": "./src/special/*.ts"}, "#ui": "@acme/ui"}}`,
		"src/db/index.ts":          "export const db = 1;\n",
		"src/utils/fmt.ts":         "export const fmt = 1;\n",
		"src/special/x.ts":         "export const x = 1;\n",
		"packages/ui/package.json": `{"name": "@acme/ui", "exports": "./index.ts"}`,
		"packages/ui/index.ts":     "export const ui = 1;\n",
		"packages/ui/lib/inner.ts": "import {db} from '#db';\n", // @acme/ui has no #db: not the root's
		"src/app.ts": `import {db} from '#db';
import {fmt} from '#utils/fmt';
import {x} from '#utils/special/x';
import {ui} from '#ui';
import {gone} from '#missing';
`,
	})
	wantDeps(t, g, "src/app.ts", "src/db/index.ts", "src/utils/fmt.ts", "src/special/x.ts", "packages/ui/index.ts")
	wantDeps(t, g, "packages/ui/lib/inner.ts")
}
