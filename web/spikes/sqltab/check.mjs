// Drives the SQL tab end to end in real browsers against the compose databases and SQLite: the shortcut
// list, format, run current statement, result export, and manual commit with commit and
// rollback. Fails on any broken step or page error.
//
// Usage, from the repository root after `make` and `docker compose up -d --wait`:
//   PLAYWRIGHT_DIR=<dir with playwright installed> node web/spikes/sqltab/check.mjs [./tess]
// BROWSERS=webkit and DBS=mysql limit the runs.
import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import path from 'node:path';

const require = createRequire(path.join(process.env.PLAYWRIGHT_DIR, 'x.js'));
const pw = require('playwright');
const bin = process.argv[2] ?? (process.platform === 'win32' ? './tess.exe' : './tess');

const dir = mkdtempSync(path.join(tmpdir(), 'tess-sqltab-'));
const server = (port, tls) => ['--host', '127.0.0.1', '--port', port, '--user', 'tessera', '--database', 'tessera_test',
  '--tls', tls, '--password-stdin'];

// txDDL says whether DDL stays inside a transaction; MySQL and MariaDB commit it at once. big is
// an integer past JavaScript's safe range that the database keeps exactly.
const databases = {
  pg: { tile: 'postgres', args: server('55418', 'disable'), txDDL: true },
  mysql: { tile: 'mysql', args: server('53384', 'DISABLED'), txDDL: false },
  mariadb: { tile: 'mysql', args: server('53114', 'DISABLED'), txDDL: false },
  sqlite: { tile: 'sqlite', args: ['--set', `path=${path.join(dir, 'sqltab.db')}`, '--set', 'create=true'], txDDL: true,
    big: '9223372036854775807' }
};
const env = { ...process.env, TESSERA_CONFIG_DIR: dir, TESSERA_PASSPHRASE: 'sqltab-check-only' };
const dbs = (process.env.DBS ?? Object.keys(databases).join(',')).split(',');
for (const name of dbs) {
  const db = databases[name];
  const add = spawnSync(bin, ['profile', 'add', name, '--tile', db.tile, ...db.args],
    { env, input: 'tessera_dev\n', encoding: 'utf8' });
  if (add.status !== 0) throw new Error(`profile add ${name}: ${add.stderr}`);
}

async function check(page, name, db, step) {
  const editor = page.locator('.cm-content');
  const txOpen = page.getByText('transaction open');
  const setSQL = async (sql) => {
    await editor.click();
    await page.keyboard.press('Control+A');
    await page.keyboard.press('Delete');
    await page.keyboard.type(sql);
    // Typing can open completion, whose popup would cover the editor for the next click.
    await page.keyboard.press('Escape');
  };
  // Runs the script, confirming the safety prompt if one comes, and waits for its results.
  const run = async (sql, keys = 'Control+Enter') => {
    await setSQL(sql);
    await page.keyboard.press(keys);
    const dialog = page.getByRole('dialog');
    const done = page.locator('.rtab, .error-box').first();
    await Promise.race([dialog.waitFor({ timeout: 10000 }), done.waitFor({ timeout: 10000 })]);
    if (await dialog.isVisible()) {
      await dialog.getByRole('button', { name: 'Run' }).click();
      await dialog.waitFor({ state: 'detached' });
    }
    await page.waitForFunction(() => !document.querySelector('.btn.danger'), null, { timeout: 10000 });
    const err = page.locator('.results .error-box');
    if (await err.count()) throw new Error(`"${sql}" failed: ${await err.first().innerText()}`);
  };
  const results = () => page.locator('.grid-wrap').innerText();
  // Opens the export menu from the given control, the Export button by default, and saves label.
  const download = async (label, from = page.getByRole('button', { name: /^Export result/ }), button = 'left') => {
    await from.click({ button });
    const [file] = await Promise.all([page.waitForEvent('download'), page.getByRole('menuitem', { name: label }).click()]);
    return readFileSync(await file.path(), 'utf8');
  };
  const probe = async () => {
    await run('SELECT count(*) * 10 + 7 AS probe FROM sqltab_probe');
    return (await results()).match(/\b(7|17|27)\b/)?.[1];
  };

  await step('open', async () => {
    await page.getByTitle(`Open a SQL tab on ${name}`).click();
    await editor.waitFor();
  });
  await step('shortcut list', async () => {
    if ((await page.locator('kbd').count()) < 10) throw new Error('shortcut keys missing');
  });
  await step('setup', async () => {
    await run('DROP TABLE IF EXISTS sqltab_probe; DROP TABLE IF EXISTS sqltab_probe2; CREATE TABLE sqltab_probe (n int)');
  });
  await step('format', async () => {
    await setSQL("select 'a;b' as s, 1 as one from sqltab_probe where n <> 2 order by n; select 2 as two");
    await page.keyboard.press('Control+Shift+F');
    await page.waitForFunction(() => document.querySelectorAll('.cm-line').length > 3, null, { timeout: 5000 });
    const text = await editor.innerText();
    const words = text.replace(/\s+/g, ' ').trim();
    if (!words.includes("'a;b'") || !/select 2 as two$/i.test(words)) throw new Error(`format changed the SQL: ${words}`);
  });
  await step('run current statement', async () => {
    await run("SELECT 'a;b' AS s; SELECT 2 AS two", 'Control+End');
    await setSQL("SELECT 'a;b' AS s; SELECT 2 AS two");
    await page.keyboard.press('Control+Shift+Enter');
    await page.locator('.rtab').first().waitFor();
    await page.waitForFunction(() => !document.querySelector('.btn.danger'));
    if ((await page.locator('.rtab').count()) !== 1 || !/two/i.test(await results())) throw new Error('last statement not run alone');
    await page.keyboard.press('Control+Home');
    await page.keyboard.press('Shift+F5');
    await page.waitForFunction(() => /a;b/.test(document.querySelector('.grid-wrap')?.textContent ?? ''), null, { timeout: 10000 });
    if ((await page.locator('.rtab').count()) !== 1) throw new Error('first statement not run alone');
  });
  await step('export', async () => {
    const big = db.big ?? '12345678901234567890';
    await run(`SELECT ${big} AS big, '=1+1' AS f, NULL AS z`);
    const csv = await download('CSV');
    if (csv !== `big,f,z\r\n${big},'=1+1,\r\n`) throw new Error(`CSV: ${JSON.stringify(csv)}`);
    const json = await download(/^JSON$/);
    if (!json.includes(`"big":${big}`) || JSON.parse(json)[0].z !== null) throw new Error(`JSON: ${json}`);
    const lines = (await download('JSON Lines')).trim().split('\n');
    if (lines.length !== 1) throw new Error(`JSON Lines: ${lines}`);
  });
  await step('export one of several results', async () => {
    await run("SELECT 'first' AS a; SELECT 'second' AS b");
    await page.locator('.rtab').nth(1).click();
    const csv = await download('CSV', page.getByRole('button', { name: 'Export result 2' }));
    if (csv !== 'b\r\nsecond\r\n') throw new Error(`export button took the wrong result: ${JSON.stringify(csv)}`);
    // Right-clicking a tab exports that result without switching to it.
    const csv1 = await download('CSV', page.locator('.rtab').first(), 'right');
    if (csv1 !== 'a\r\nfirst\r\n') throw new Error(`tab menu took the wrong result: ${JSON.stringify(csv1)}`);
    if ((await page.locator('.rtab.on').innerText()).trim() !== '2. 1 rows') throw new Error('right-click switched tabs');
  });
  await step('manual commit: rollback', async () => {
    await page.getByRole('radio', { name: 'Manual commit' }).click();
    await run('INSERT INTO sqltab_probe VALUES (1)');
    await txOpen.waitFor();
    if (!(await page.getByRole('radio', { name: 'Auto commit' }).isDisabled())) throw new Error('auto commit enabled in a transaction');
    if ((await page.locator('.rtab').count()) !== 1) throw new Error('BEGIN shown as a result');
    await page.getByRole('button', { name: 'Rollback' }).click();
    await txOpen.waitFor({ state: 'detached' });
    const p = await probe(); // this runs in a new transaction, closed below
    if (p !== '7') throw new Error(`rolled-back row still there (${p})`);
    await page.getByRole('button', { name: 'Rollback' }).click();
    await txOpen.waitFor({ state: 'detached' });
  });
  await step('manual commit: commit', async () => {
    await run('INSERT INTO sqltab_probe VALUES (1)');
    await page.getByRole('button', { name: 'Commit' }).click();
    await txOpen.waitFor({ state: 'detached' });
    await page.getByRole('radio', { name: 'Auto commit' }).click();
    if ((await probe()) !== '17') throw new Error('committed row missing');
  });
  await step('manual commit: DDL', async () => {
    await page.getByRole('radio', { name: 'Manual commit' }).click();
    await run('CREATE TABLE sqltab_probe2 (n int)');
    await page.waitForTimeout(200);
    if (db.txDDL !== (await txOpen.isVisible())) throw new Error(`transaction open after DDL should be ${db.txDDL}`);
    if (db.txDDL) {
      await page.getByRole('button', { name: 'Rollback' }).click();
      await txOpen.waitFor({ state: 'detached' });
    }
    await page.getByRole('radio', { name: 'Auto commit' }).click();
  });
  await step('cleanup', async () => {
    await run('DROP TABLE sqltab_probe; DROP TABLE IF EXISTS sqltab_probe2');
  });
}

const results = [];
for (const browserName of (process.env.BROWSERS ?? 'chromium,firefox,webkit').split(',')) {
  for (const name of dbs) {
    const label = `${browserName}/${name}`;
    // A launch URL works once, so each run gets its own server.
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
      browser = await pw[browserName].launch();
    } catch (e) {
      srv.kill();
      results.push({ label, skipped: e.message.split('\n')[0] });
      continue;
    }
    const page = await (await browser.newContext({ acceptDownloads: true })).newPage();
    const problems = [];
    const steps = [];
    page.on('pageerror', (e) => problems.push(`page error: ${e.message}`));
    const step = async (what, f) => {
      if (problems.length) return; // later steps depend on earlier ones
      try {
        await f();
        steps.push(what);
      } catch (e) {
        problems.push(`${what}: ${e.message.split('\n')[0]}`);
        await page.screenshot({ path: path.join(dir, `${browserName}-${name}.png`) }).catch(() => {});
      }
    };
    await page.goto(url);
    await check(page, name, databases[name], step);
    await browser.close();
    // Windows cannot remove the SQLite file while the server still has it open.
    await new Promise((resolve) => {
      srv.once('exit', resolve);
      srv.kill();
    });
    results.push({ label, steps, problems });
  }
}

let failed = false;
for (const r of results) {
  if (r.skipped) {
    console.log(`${r.label}: skipped, ${r.skipped}`);
    continue;
  }
  console.log(`${r.label}: ${r.problems.length ? 'FAIL' : 'ok'} (${r.steps.join(', ')})`);
  for (const p of r.problems) console.log(`  ${p}`);
  failed ||= r.problems.length > 0;
}
if (!failed) rmSync(dir, { recursive: true, force: true });
else console.log(`screenshots in ${dir}`);
process.exit(failed ? 1 : 0);
