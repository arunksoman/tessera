// Usage: PLAYWRIGHT_DIR=<dir with playwright installed> node theme.mjs
import { createRequire } from 'node:module';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const require = createRequire(path.join(process.env.PLAYWRIGHT_DIR, 'x.js'));
const pw = require('playwright');
const url = pathToFileURL(path.join(path.dirname(fileURLToPath(import.meta.url)), 'index.html')).href;

const LIGHT = { bg: 'rgb(255, 255, 255)', fg: 'rgb(28, 31, 36)' };
const DARK = { bg: 'rgb(28, 31, 37)', fg: 'rgb(231, 233, 236)' };

const cases = [
  ['follows a light system', 'light', null, LIGHT],
  ['follows a dark system', 'dark', null, DARK],
  ['user choice beats a light system', 'light', 'dark', DARK],
  ['user choice beats a dark system', 'dark', 'light', LIGHT],
];

let failed = 0;
for (const name of ['chromium', 'firefox', 'webkit']) {
  const browser = await pw[name].launch();
  for (const [label, system, choice, want] of cases) {
    const page = await browser.newPage();
    await page.emulateMedia({ colorScheme: system });
    await page.goto(url);
    const got = await page.evaluate(choice => {
      if (choice) document.documentElement.dataset.theme = choice;
      const s = getComputedStyle(document.querySelector('.cell'));
      return { bg: getComputedStyle(document.getElementById('viewport')).backgroundColor, fg: s.color };
    }, choice);
    const ok = got.bg === want.bg && got.fg === want.fg;
    if (!ok) failed++;
    console.log(ok ? 'ok  ' : 'FAIL', name, label, ok ? '' : JSON.stringify(got));
    await page.close();
  }
  await browser.close();
}
process.exitCode = failed ? 1 : 0;
