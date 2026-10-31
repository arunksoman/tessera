// Usage: PLAYWRIGHT_DIR=<dir with playwright installed> node measure.mjs
import { createRequire } from 'node:module';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const require = createRequire(path.join(process.env.PLAYWRIGHT_DIR, 'x.js'));
const pw = require('playwright');
const page_url = pathToFileURL(path.join(path.dirname(fileURLToPath(import.meta.url)), 'index.html')).href;

const assert = (ok, msg) => { if (!ok) throw new Error('assert failed: ' + msg); };

// Records rAF intervals over a scroll pattern. Layout-forcing checks run outside the timed
// intervals, so the measurement does not cause the cost it reports.
const SCROLL = async ({ pattern, frames }) => {
  const v = document.getElementById('viewport');
  const maxTop = v.scrollHeight - v.clientHeight;
  const times = [];
  let blanks = 0;
  let last = performance.now();
  await new Promise(done => {
    let i = 0;
    const tick = () => {
      const now = performance.now();
      if (i > 0) times.push(now - last);
      if (i % 30 === 15 && grid.visibleRows().length === 0) blanks++;
      if (i++ >= frames) return done();
      if (pattern === 'wheel') grid.moveBy(120);
      if (pattern === 'drag') v.scrollTop = (i / frames) * maxTop;
      if (pattern === 'jump') v.scrollTop = Math.random() * maxTop;
      last = performance.now();
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  });
  times.sort((a, b) => a - b);
  const sum = times.reduce((a, b) => a + b, 0);
  return {
    fps: +(1000 / (sum / times.length)).toFixed(1),
    p95ms: +times[Math.floor(times.length * 0.95)].toFixed(1),
    maxms: +times.at(-1).toFixed(1),
    blanks,
  };
};

let failed = false;
for (const name of ['chromium', 'firefox', 'webkit']) {
  const browser = await pw[name].launch({ headless: !process.env.HEADED });
  const page = await browser.newPage({ viewport: { width: 1600, height: 900 } });
  const errors = [];
  page.on('pageerror', e => errors.push(String(e)));
  await page.goto(page_url);

  // Behaviour first: every row must be reachable, and the DOM must stay small.
  const facts = await page.evaluate(async () => {
    const out = {};
    const v = document.getElementById('viewport');
    out.scrollHeight = v.scrollHeight;
    out.domNodes = grid.domNodes();
    const settle = () => new Promise(r => setTimeout(r, 100));
    grid.scrollToRow(0); await settle();
    out.firstRow = grid.visibleRows()[0];
    v.scrollTop = v.scrollHeight; await settle();
    out.lastRow = grid.visibleRows().at(-1);
    out.lastCell = document.querySelector(`[aria-rowindex="${out.lastRow + 1}"] .cell`)?.textContent;
    grid.scrollToRow(500_000); await settle();
    out.middle = grid.visibleRows()[0];
    grid.scrollToRow(500_000); await settle();
    out.topBeforeWheel = grid.topRow();
    return out;
  });
  assert(facts.firstRow === 0, `${name} first row ${facts.firstRow}`);
  assert(facts.lastRow === 999_999, `${name} last row ${facts.lastRow}`);
  assert(facts.lastCell === 'r999999c0', `${name} last cell ${facts.lastCell}`);
  assert(Math.abs(facts.middle - 500_000) <= 1, `${name} middle row ${facts.middle}`);
  assert(facts.domNodes < 1500, `${name} DOM nodes ${facts.domNodes}`);

  // A real wheel event must move a few rows, even though one scrollbar pixel covers 3.5 rows.
  await page.mouse.move(800, 450);
  await page.mouse.wheel(0, 120);
  await page.waitForTimeout(150);
  const moved = await page.evaluate(() => grid.topRow()) - facts.topBeforeWheel;
  assert(moved > 3 && moved < 6, `${name} wheel moved ${moved} rows`);

  const results = {};
  for (const pattern of ['idle', 'wheel', 'drag', 'jump']) {
    await page.evaluate(() => grid.scrollToRow(0));
    results[pattern] = await page.evaluate(SCROLL, { pattern, frames: 300 });
    if (pattern !== 'idle' && results[pattern].blanks > 0) failed = true;
  }
  assert(errors.length === 0, `${name} page errors: ${errors}`);
  console.log(name, browser.version(), 'scrollHeight', facts.scrollHeight, 'domNodes', facts.domNodes, 'wheelRows', moved.toFixed(1));
  for (const [k, v] of Object.entries(results)) console.log('  ', k.padEnd(6), JSON.stringify(v));
  await browser.close();
}
process.exitCode = failed ? 1 : 0;
