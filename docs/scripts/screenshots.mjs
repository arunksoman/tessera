// Takes the web app screenshots used in the docs, once in the light theme and once in the dark
// theme, against the Sakila sample database in the compose servers. The TUI screenshots come
// from docs/scripts/tui-screenshots.sh.
//
// Usage, from the repository root after `make` and `docker compose up -d --wait`:
//   PLAYWRIGHT_DIR=<dir with playwright installed> node docs/scripts/screenshots.mjs [./tess]
// SHOTS=web-sql,web-browse limits the run to some screenshots.
//
// Nothing here writes to a database: edits are previewed and discarded, and the safety prompt
// is cancelled.
import { spawn, spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import path from 'node:path';

const require = createRequire(path.join(process.env.PLAYWRIGHT_DIR, 'x.js'));
const pw = require('playwright');
const bin = process.argv[2] ?? (process.platform === 'win32' ? './tess.exe' : './tess');
const out = path.resolve('docs/src/assets/screens');
mkdirSync(out, { recursive: true });
const only = process.env.SHOTS?.split(',');

const dir = mkdtempSync(path.join(tmpdir(), 'tess-docs-'));
const env = { ...process.env, TESSERA_CONFIG_DIR: dir, TESSERA_PASSPHRASE: 'docs-screenshots-only' };
const pg = ['--tile', 'postgres', '--host', '127.0.0.1', '--port', '55418', '--user', 'tessera', '--database', 'sakila',
  '--tls', 'disable', '--password-stdin'];
const profiles = {
  sakila: { args: pg, color: 'blue' },
  'sakila-mysql': { args: ['--tile', 'mysql', '--host', '127.0.0.1', '--port', '53384', '--user', 'tessera',
    '--database', 'sakila', '--tls', 'DISABLED', '--password-stdin'], color: 'orange' },
  notes: { args: ['--tile', 'sqlite', '--set', `path=${path.join(dir, 'notes.db')}`, '--set', 'create=true'], color: 'green' },
  'billing-prod': { args: [...pg, '--env', 'production'], color: 'red' }
};
for (const [name, p] of Object.entries(profiles)) {
  const add = spawnSync(bin, ['profile', 'add', name, ...p.args], { env, input: 'tessera_dev\n', encoding: 'utf8' });
  if (add.status !== 0) throw new Error(`profile add ${name}: ${add.stderr}`);
}
// Colours are set in the web app's form; writing them into config.toml is quicker.
const cfgPath = path.join(dir, 'config.toml');
let cfg = readFileSync(cfgPath, 'utf8');
for (const [name, p] of Object.entries(profiles)) {
  cfg = cfg.replace(new RegExp(`^([ \\t]*\\[profiles\\.${name}\\]\\r?\\n)`, 'm'), `$1color = "${p.color}"\n`);
}
writeFileSync(cfgPath, cfg);

async function serve() {
  const srv = spawn(bin, ['serve', '--no-browser'], { env });
  const url = await new Promise((resolve, reject) => {
    let text = '';
    srv.stdout.on('data', (d) => {
      text += d;
      const m = text.match(/https?:\/\/\S+/);
      if (m) resolve(m[0]);
    });
    srv.on('exit', (c) => reject(new Error(`serve exited ${c}: ${text}`)));
  });
  return { srv, url };
}

const browser = await pw.chromium.launch();
try {
  for (const scheme of ['light', 'dark']) {
    const { srv, url } = await serve();
    const ctx = await browser.newContext({ colorScheme: scheme, viewport: { width: 1360, height: 820 }, deviceScaleFactor: 1.5 });
    const page = await ctx.newPage();
    page.on('pageerror', (e) => console.error(`${scheme}: page error: ${e.message}`));
    const shot = async (name, target = page) => {
      if (only && !only.includes(name)) return;
      await page.waitForTimeout(300); // let focus rings and menus settle
      await target.screenshot({ path: path.join(out, `${name}-${scheme}.png`), animations: 'disabled' });
      console.log(`${name}-${scheme}.png`);
    };
    const tree = page.getByRole('navigation', { name: 'Connections' });
    const sql = async (text) => {
      const editor = page.locator('.cm-content').last();
      await editor.click();
      await page.keyboard.press('Control+A');
      await page.keyboard.press('Delete');
      await page.keyboard.insertText(text);
    };

    await page.goto(url);
    await page.getByRole('heading', { name: 'Welcome to Tessera' }).waitFor();
    await shot('web-start');

    await page.getByRole('main').getByRole('button', { name: 'New connection' }).click();
    await page.getByRole('dialog').waitFor();
    await page.getByRole('dialog').getByText('PostgreSQL', { exact: true }).first().click().catch(() => {});
    await shot('web-connection-form');
    await page.keyboard.press('Escape');
    await page.getByRole('dialog').waitFor({ state: 'detached' });

    await page.getByTitle('Open a SQL tab on sakila', { exact: true }).click();
    await tree.getByRole('treeitem', { name: 'Expand public' }).getByRole('button', { name: 'Expand' }).click();
    const film = tree.getByRole('treeitem', { name: 'film', exact: true });
    await sql(
      'SELECT c.name AS category, count(*) AS films, round(avg(f.rental_rate), 2) AS avg_rate\n' +
        'FROM film f\nJOIN film_category fc USING (film_id)\nJOIN category c USING (category_id)\n' +
        'GROUP BY c.name\nORDER BY films DESC;'
    );
    await page.keyboard.press('Control+Enter');
    await page.locator('.rtab').first().waitFor();
    await page.locator('.results [role="gridcell"]').first().waitFor();
    await shot('web-sql');

    await film.getByRole('button', { name: 'film' }).click();
    const cells = page.locator('.browse [role="gridcell"]');
    await cells.first().waitFor();
    await shot('web-browse');

    // An edit, previewed and then discarded.
    const title = page.locator('.browse .tr').first().locator('[role="gridcell"]').nth(1);
    await title.dblclick();
    await page.keyboard.press('Control+A');
    await page.keyboard.insertText('ACADEMY DINOSAUR (DIRECTOR\'S CUT)');
    await page.keyboard.press('Enter');
    await page.getByRole('button', { name: 'Preview and apply…' }).click();
    await page.getByRole('dialog').waitFor();
    await shot('web-edit-preview');
    await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();
    await page.getByRole('button', { name: 'Discard' }).click();

    await film.getByRole('button', { name: 'film' }).click({ button: 'right' });
    await page.getByRole('menu').waitFor();
    await shot('web-context-menu');
    await page.getByRole('menuitem', { name: /^Structure/ }).click();
    await page.locator('.structure, [class*="structure"]').first().waitFor();
    await shot('web-structure');

    // The production prompt, cancelled. The statement would be harmless even if it ran.
    await page.getByRole('button', { name: 'New query tab for billing-prod' }).click({ timeout: 2000 }).catch(async () => {
      await tree.getByRole('button', { name: 'billing-prod' }).click();
      await page.getByRole('button', { name: 'New query tab for billing-prod' }).click();
    });
    await sql('DROP TABLE IF EXISTS old_rentals_2005;');
    await page.keyboard.press('Control+Enter');
    await page.getByRole('dialog').waitFor();
    await shot('web-confirm');
    await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();

    await ctx.close();
    srv.kill();
  }
} finally {
  await browser.close();
  try {
    rmSync(dir, { recursive: true, force: true });
  } catch {}
}
