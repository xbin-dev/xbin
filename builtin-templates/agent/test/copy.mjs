// copy.mjs — reading and copying an answer (#26, #34; D199) and the judge's
// "not finished" mark (D198), in the real tile against the fake backend.
// What it pins: a closed code fence is highlighted once xbind's
// /vendor/highlight.min.js arrives (and stays plain on an xbind without
// it); Ctrl+C on a selection puts text/html without the chat's chrome and
// text/plain written as markdown reads (numbered items, a table as pipes, a
// fence); an answer's Copy hands its markdown and its rendering to
// xbin.copy, and without xbin.copy copies in the frame; a row the judge
// marked shows "not finished", its menu's "Mark as finished" takes it down,
// and Needs you lists it.
//
//   node test/copy.mjs        (needs playwright + a chromium build)
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const HLJS = fileURLToPath(new URL('../../../web/vendor/highlight.min.js', import.meta.url));
const ANSWER = [
  'Here is the plan:', '', '1. **first** step', '2. second step', '', '| a | b |', '|---|---|', '| 1 | 2 |', '',
  '```js', 'const x = "<b>" + 1; // the sum', '```',
].join('\n');
const seed = {
  runs: [{ id: 1, title: 'the plan', status: 'idle', parentId: 0, rootId: 1, unfinished: { reason: 'it only made a plan', at: 5 } }],
  views: { 1: { run: { id: 1, title: 'the plan', status: 'idle', parentId: 0, rootId: 1 },
    messages: [{ id: 1, runId: 1, seq: 1, role: 'user', content: 'plan it', created: 1 },
      { id: 2, runId: 1, seq: 2, role: 'assistant', content: ANSWER, created: 2 }] } },
};

const browser = await launch();
for (const hl of [true, false]) {
  const ctx = await browser.newContext();
  await serveTile(ctx, { realMarked: true });
  if (hl) await ctx.route('**/vendor/highlight.min.js', (r) => r.fulfill({ contentType: 'text/javascript', body: readFileSync(HLJS, 'utf8') }));
  else await ctx.route('**/vendor/highlight.min.js', (r) => r.fulfill({ status: 404, body: '' }));
  await ctx.addInitScript(STUB, seed);
  await ctx.addInitScript((hl) => {
    window.__copied = [];
    window.__dismissed = [];
    window.__route('POST', /\/runs\/(\d+)\/unfinished\/dismiss$/, (m) => { window.__dismissed.push(+m[1]); return window.__json({ ok: true }); });
    window.__route('GET', /\/needs$/, () => window.__json({ items: window.__dismissed.length ? [] : [{ reason: 'unfinished', subRun: 0,
      run: { id: 1, title: 'the plan', unfinished: { reason: 'it only made a plan', at: 5 } } }] }));
    if (hl) window.xbin = { ...window.xbin, copy: async (d) => { window.__copied.push(d); return true; } }; // an xbind with xbin.copy (STUB's ran first)
  }, hl);
  const page = await ctx.newPage();
  await page.goto(`${ORIGIN}/`);
  await page.waitForSelector('#runs .run');
  if (hl) {
    // the judge's mark: the row's glyph, Needs you, and the row menu's Mark as finished
    ok('a marked row says "not finished"', !!(await page.$('#runs .run .gl.unf')),
      await page.$eval('#runs .run', (r) => r.innerHTML.slice(0, 300)).catch(() => ''));
    ok('its title says why', ((await page.getAttribute('#runs .run .gl.unf', 'title')) || '').includes('it only made a plan'));
    await page.waitForSelector('.home .need[data-reason="unfinished"]', { timeout: 5000 }).catch(() => {});
    ok('Needs you lists it, with the reason', ((await page.textContent('.home .need[data-reason="unfinished"]').catch(() => '')) || '').includes('stopped before finishing: it only made a plan'));
  }
  await page.click('#runs .run');
  await page.waitForSelector('.msg.assistant .md ol');
  if (hl) {
    await page.waitForSelector('.msg.assistant pre code.hljs .hljs-keyword', { timeout: 5000 }).catch(() => {});
    ok('a closed fence is highlighted once the highlighter arrives', !!(await page.$('.msg.assistant pre code.hljs .hljs-keyword')));
    ok('…its text escaped', (await page.textContent('.msg.assistant pre code')).includes('"<b>"') && !(await page.$('.msg.assistant pre code b')));
  } else {
    await page.waitForTimeout(300);
    ok('without the highlighter the code stays plain', !(await page.$('.msg.assistant pre code.hljs')) && !!(await page.$('.msg.assistant pre code.language-js')));
  }
  // Ctrl+C on the answer: both flavours, set in the copy event
  const got = await page.evaluate(() => {
    const md = document.querySelector('.msg.assistant .md');
    const s = getSelection(); s.removeAllRanges();
    const r = document.createRange(); r.selectNodeContents(md); s.addRange(r);
    const dt = new DataTransfer();
    const e = new ClipboardEvent('copy', { clipboardData: dt, bubbles: true, cancelable: true });
    document.dispatchEvent(e);
    return { prevented: e.defaultPrevented, text: dt.getData('text/plain'), html: dt.getData('text/html') };
  });
  ok('the copy event is ours', got.prevented, JSON.stringify(got).slice(0, 200));
  ok('text/plain keeps the numbers, the table, the fence', got.text.includes('1. **first** step\n2. second step') && got.text.includes('| a | b |\n| --- | --- |\n| 1 | 2 |') &&
    got.text.includes('```\nconst x = "<b>" + 1; // the sum\n```'), got.text);
  ok('text/html keeps the list and the table, without the chrome', /<ol>/.test(got.html) && /<table>/.test(got.html) && !/class=|<button|data-k/.test(got.html), got.html.slice(0, 300));
  // the answer's Copy
  await page.hover('.msg.assistant');
  await page.click('.msg.assistant .acts .act');
  await page.waitForTimeout(200);
  if (hl) {
    const c = await page.evaluate(() => window.__copied);
    ok('Copy hands the markdown and its rendering to xbin.copy', c.length === 1 && c[0].text.startsWith('Here is the plan') &&
      /<ol>/.test(c[0].html) && !/class=/.test(c[0].html), JSON.stringify(c).slice(0, 300));
    ok('…and says it took', (await page.getAttribute('.msg.assistant .acts .act', 'title')) === 'copied');
    // Mark as finished, from the row's menu
    await page.click('#runs .run', { button: 'right' });
    await page.click('text=Mark as finished').catch(() => {});
    await page.waitForTimeout(200);
    ok('Mark as finished asks the backend', (await page.evaluate(() => window.__dismissed)).includes(1));
  } else {
    ok('without xbin.copy the frame copies itself', ['copied', 'copy refused — select the text and press Ctrl+C'].includes(await page.getAttribute('.msg.assistant .acts .act', 'title')));
  }
  await ctx.close();
}
await browser.close();
done('copy');
