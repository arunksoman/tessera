// Runs the built web app under tess serve's real CSP in Chromium, Firefox, and WebKit, drives
// the editor, completion, a query, the grid, browse, and the theme switch, and fails on any
// CSP violation or page error.
//
// Usage, from the repository root after `make` and `docker compose up -d --wait`:
//   PLAYWRIGHT_DIR=<dir with playwright installed> node web/spikes/csp/check.mjs [./tess]
// BROWSERS=webkit limits the browsers, and SKIP="tree and browse,..." skips steps by name.
import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import path from 'node:path';

const require = createRequire(path.join(process.env.PLAYWRIGHT_DIR, 'x.js'));
const pw = require('playwright');
const bin = process.argv[2] ?? (process.platform === 'win32' ? './tess.exe' : './tess');

const dir = mkdtempSync(path.join(tmpdir(), 'tess-csp-'));
const env = { ...process.env, TESSERA_CONFIG_DIR: dir, TESSERA_PASSPHRASE: 'csp-check-only' };
const add = spawnSync(bin, ['profile', 'add', 'pg', '--tile', 'postgres', '--host', '127.0.0.1', '--port', '55418',
  '--user', 'tessera', '--database', 'tessera_test', '--tls', 'disable', '--password-stdin'],
  { env, input: 'tessera_dev\n', encoding: 'utf8' });
if (add.status !== 0) throw new Error(`profile add: ${add.stderr}`);

const results = [];
for (const name of (process.env.BROWSERS ?? 'chromium,firefox,webkit').split(',')) {
  // A launch URL works once, so each browser gets its own server.
  const srv = spawn(bin, ['serve', '--no-browser'], { env });
  const url = await new Promise((resolve, reject) => {
    let out = '';
    const take = (b) => {
      out += b;
      const m = out.match(/https?:\/\/\S+/);
      if (m) resolve(m[0]);
    };
    srv.stdout.on('data', take);
    srv.stderr.on('data', take);
    srv.on('exit', (c) => reject(new Error(`serve exited ${c}: ${out}`)));
  });
  let browser;
  try {
    browser = await pw[name].launch();
  } catch (e) {
    srv.kill();
    results.push({ name, skipped: e.message.split('\n')[0] });
    continue;
  }
  const page = await browser.newPage();
  const problems = [];
  await page.addInitScript(() => {
    window.__csp = [];
    document.addEventListener('securitypolicyviolation', (e) =>
      window.__csp.push(`${e.violatedDirective} blocked ${e.blockedURI || 'inline'} at ${e.sourceFile}:${e.lineNumber}`));
  });
  page.on('pageerror', (e) => problems.push(`page error: ${e.message}`));
  page.on('console', (m) => {
    if (/content.security.policy|refused to/i.test(m.text())) problems.push(`console during ${current}: ${m.text()} (${JSON.stringify(m.location())})`);
  });
  const steps = [];
  let current = 'start';
  const skip = (process.env.SKIP ?? '').split(',');
  const step = async (what, f) => {
    if (skip.includes(what)) return;
    current = what;
    try {
      await f();
      steps.push(what);
    } catch (e) {
      problems.push(`${what}: ${e.message.split('\n')[0]}`);
    }
  };
  await step('load', async () => {
    await page.goto(url);
    await page.getByRole('button', { name: /SQL on pg/ }).waitFor({ timeout: 10000 });
  });
  await step('editor', async () => {
    await page.getByRole('button', { name: /SQL on pg/ }).click();
    await page.locator('.cm-content').click();
    await page.keyboard.type('SELECT id, name FROM customers ORDER BY id');
  });
  await step('completion', async () => {
    await page.keyboard.press('Control+Space');
    await page.locator('.cm-tooltip-autocomplete').waitFor({ timeout: 5000 });
    await page.keyboard.press('Escape');
  });
  await step('run query and grid', async () => {
    await page.keyboard.press('Control+Enter');
    await page.getByText('Ada', { exact: true }).waitFor({ timeout: 10000 });
  });
  await step('tree and browse', async () => {
    await page.getByRole('button', { name: 'pg' }).first().click();
    const label = page.locator('button.label', { hasText: 'customers' }).first();
    for (let i = 0; i < 4 && !(await label.isVisible()); i++) {
      const twisty = page.getByRole('button', { name: 'Expand' }).first();
      if (await twisty.isVisible()) await twisty.click();
      await page.waitForTimeout(300);
    }
    await label.click();
    await page.getByText('alan@example.com').first().waitFor({ timeout: 10000 });
  });
  await step('theme switch', async () => {
    // The app's own control first, then the system setting it follows by default.
    const pick = page.getByRole('combobox', { name: 'Theme' });
    for (const theme of ['dark', 'light', 'system']) {
      await pick.selectOption(theme);
      await page.waitForTimeout(100);
    }
  });
  await step('system theme change', async () => {
    for (const scheme of ['dark', 'light']) {
      await page.emulateMedia({ colorScheme: scheme });
      await page.waitForTimeout(100);
    }
  });
  for (const v of await page.evaluate(() => window.__csp)) problems.push(`CSP: ${v}`);
  await page.screenshot({ path: path.join(dir, `${name}.png`) });
  await browser.close();
  srv.kill();
  results.push({ name, steps, problems });
}

let failed = false;
for (const r of results) {
  if (r.skipped) {
    console.log(`${r.name}: skipped, ${r.skipped}`);
    continue;
  }
  console.log(`${r.name}: ${r.problems.length ? 'FAIL' : 'ok'} (${r.steps.join(', ')})`);
  for (const p of r.problems) console.log(`  ${p}`);
  failed ||= r.problems.length > 0;
}
if (!failed) rmSync(dir, { recursive: true, force: true });
else console.log(`screenshots in ${dir}`);
process.exit(failed ? 1 : 0);
