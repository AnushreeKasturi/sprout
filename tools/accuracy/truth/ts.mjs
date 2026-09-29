// Ground truth for TypeScript and JavaScript, from the TypeScript compiler:
// preProcessFile lists a file's imports (static, dynamic, require, export
// from), and resolveModuleName maps each one to a file using the nearest
// tsconfig.json or jsconfig.json, so "paths" aliases resolve as they do in
// the editor.
//
//   node ts.mjs <repo> <graph.json>   (prints {rel: {specs, localSpecs, targets}})
//
// Workspace packages are linked into node_modules first, as npm, pnpm and
// yarn would, so "@acme/ui" resolves to the package in the repository.

import ts from 'typescript';
import fs from 'node:fs';
import path from 'node:path';

const [repo, graphFile] = process.argv.slice(2).map((p) => path.resolve(p));
const files = JSON.parse(fs.readFileSync(graphFile, 'utf8')).files.filter((f) => f.lang === 'js');
const inScope = new Set(JSON.parse(fs.readFileSync(graphFile, 'utf8')).files.map((f) => f.rel));
const rel = (p) => path.relative(repo, p).split(path.sep).join('/');

linkWorkspaces();

const configs = new Map(); // directory -> compiler options
function optionsFor(dir) {
  if (configs.has(dir)) return configs.get(dir);
  let opts;
  const found = ['tsconfig.json', 'jsconfig.json'].map((n) => path.join(dir, n)).find((p) => fs.existsSync(p));
  if (found) {
    const { config } = ts.readConfigFile(found, ts.sys.readFile);
    opts = ts.parseJsonConfigFileContent(config ?? {}, ts.sys, dir, undefined, found).options;
  } else if (dir === repo || dir === path.dirname(dir)) {
    opts = {};
  } else {
    opts = optionsFor(path.dirname(dir));
  }
  configs.set(dir, opts);
  return opts;
}

function resolveOptions(dir) {
  const o = { ...optionsFor(dir), allowJs: true, allowImportingTsExtensions: true, noEmit: true };
  // Without an explicit strategy, resolve like a modern bundler.
  if (o.moduleResolution === undefined) {
    o.moduleResolution = ts.ModuleResolutionKind.Bundler;
    o.module = ts.ModuleKind.ESNext;
  }
  return o;
}

const out = {};
for (const f of files) {
  const abs = path.join(repo, f.rel);
  const src = fs.readFileSync(abs, 'utf8');
  const specs = ts.preProcessFile(src, true, true).importedFiles.map((i) => i.fileName);
  const opts = resolveOptions(path.dirname(abs));
  const targets = new Set();
  const localSpecs = [];
  for (const spec of specs) {
    const r = ts.resolveModuleName(spec, abs, opts, ts.sys).resolvedModule;
    if (!r) continue;
    const t = rel(fs.realpathSync(r.resolvedFileName));
    if (t.startsWith('..') || t.split('/').includes('node_modules') || !inScope.has(t)) continue;
    localSpecs.push(spec);
    if (t !== f.rel) targets.add(t);
  }
  out[f.rel] = { specs, localSpecs, targets: [...targets] };
}
process.stdout.write(JSON.stringify(out));

function linkWorkspaces() {
  const pkgs = [];
  const walk = (d) => {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      if (e.name === 'node_modules' || e.name.startsWith('.')) continue;
      const p = path.join(d, e.name);
      if (e.isDirectory()) walk(p);
      else if (e.name === 'package.json' && d !== repo) {
        try {
          const name = JSON.parse(fs.readFileSync(p, 'utf8')).name;
          if (name) pkgs.push([name, d]);
        } catch {}
      }
    }
  };
  walk(repo);
  for (const [name, dir] of pkgs) {
    const link = path.join(repo, 'node_modules', name);
    if (fs.existsSync(link)) continue;
    fs.mkdirSync(path.dirname(link), { recursive: true });
    fs.symlinkSync(dir, link, 'dir');
  }
}
