// Bans HTML injection sinks in the web app. Database content is hostile, and the CSP is the
// second line of defence, not the first: {@html} and DOM HTML setters are never allowed.
//
// Usage: node scripts/lint.mjs [dir ...]   (default: src)
import { readFile, readdir } from 'node:fs/promises';
import { join } from 'node:path';
import { argv, exit } from 'node:process';
import { parse } from 'svelte/compiler';
import ts from 'typescript';

const sinks = new Set([
  'innerHTML',
  'outerHTML',
  'insertAdjacentHTML',
  'createContextualFragment',
  'parseFromString',
  'setHTMLUnsafe',
  'srcdoc',
]);

const lineAt = (text, offset) => text.slice(0, offset).split('\n').length;

function walk(node, visit, seen = new Set()) {
  if (!node || typeof node !== 'object' || seen.has(node)) return;
  seen.add(node);
  visit(node);
  for (const v of Object.values(node)) walk(v, visit, seen);
}

function lintSvelte(path, text) {
  const out = [];
  walk(parse(text, { modern: true, filename: path }), (n) => {
    if (n.type === 'HtmlTag') out.push(`${path}:${lineAt(text, n.start)}: {@html} renders HTML; render text instead`);
    const name = n.type === 'MemberExpression' && !n.computed ? n.property?.name : undefined;
    if (sinks.has(name)) out.push(`${path}:${lineAt(text, n.property.start)}: ${name} parses HTML; use textContent or DOM nodes`);
  });
  return out;
}

function lintScript(path, text) {
  const out = [];
  const src = ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true);
  const visit = (n) => {
    if (ts.isPropertyAccessExpression(n) && sinks.has(n.name.text)) {
      const line = src.getLineAndCharacterOfPosition(n.name.getStart(src)).line + 1;
      out.push(`${path}:${line}: ${n.name.text} parses HTML; use textContent or DOM nodes`);
    }
    ts.forEachChild(n, visit);
  };
  visit(src);
  return out;
}

export async function lintFile(path) {
  const text = await readFile(path, 'utf8');
  if (path.endsWith('.svelte')) return lintSvelte(path, text);
  if (/\.(ts|js|mjs)$/.test(path) && !path.endsWith('.d.ts')) return lintScript(path, text);
  return [];
}

async function* files(dir) {
  for (const e of await readdir(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) yield* files(p);
    else yield p;
  }
}

if (import.meta.main) {
  const dirs = argv.length > 2 ? argv.slice(2) : ['src'];
  const problems = [];
  for (const d of dirs) for await (const f of files(d)) problems.push(...(await lintFile(f)));
  for (const p of problems) console.error(p);
  if (problems.length) exit(1);
}
