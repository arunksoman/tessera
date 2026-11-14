// Run with: node --test scripts/
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import { lintFile } from './lint.mjs';

const fixture = (name) => fileURLToPath(new URL(`testdata/${name}`, import.meta.url));

test('flags {@html} in markup', async () => {
  const problems = await lintFile(fixture('bad.svelte'));
  assert.equal(problems.length, 1);
  assert.match(problems[0], /bad\.svelte:6: \{@html\}/);
});

test('flags HTML sinks in scripts', async () => {
  const problems = await lintFile(fixture('bad.ts'));
  assert.equal(problems.length, 1);
  assert.match(problems[0], /bad\.ts:2: innerHTML/);
});

test('ignores comments and strings that only mention it', async () => {
  assert.deepEqual(await lintFile(fixture('good.svelte')), []);
});
