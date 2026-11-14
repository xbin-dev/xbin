// needs-xbind.mjs — the page on an xbind older than it needs (boot.js).
//
// The chat is drawn with xbind's chat kit (/vendor/chat/…), and an instance
// of this template can be newer than the xbind it runs on. What this pins:
// on an xbind that serves the kit the app starts; on one from before the kit
// (its URLs answer 404) the page says "This agent needs xbind ≥ …" with a
// link to how to update, and the app is not started; the same when the kit
// is there at a lower revision than the page needs; and a failure of
// agent.js itself — a throw, a module of its own that is missing — is an
// uncaught error as it always was, never that message.
//
//   node test/needs-xbind.mjs        (needs playwright + a chromium build)
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const BOOT = readFileSync(fileURLToPath(new URL('../boot.js', import.meta.url)), 'utf8');
const XBIND = BOOT.match(/xbind: '(v[\d.]+)'/)[1];
const seed = { runs: [{ id: 1, title: 'a chat', status: 'idle', parentId: 0, rootId: 1 }],
  views: { 1: { run: { id: 1, title: 'a chat', status: 'idle', parentId: 0, rootId: 1 }, messages: [] } } };

const browser = await launch();
// open(opts, routes): the page; `settled` is what shows that boot.js decided
async function open(opts, routes, settled) {
  const ctx = await browser.newContext();
  await serveTile(ctx, opts);
  if (routes) await routes(ctx);
  await ctx.addInitScript(STUB, seed);
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await page.goto(`${ORIGIN}/`);
  await settled(page, errors);
  const needs = await page.$eval('#needs-xbind', (el) => ({ text: el.textContent, href: el.querySelector('a')?.getAttribute('href'),
    target: el.querySelector('a')?.target, kit: el.dataset.kit, color: getComputedStyle(el).borderTopStyle })).catch(() => null);
  const app = { wrap: !!(await page.$('.wrap')), calls: await page.evaluate(() => window.__calls.length) };
  return { ctx, page, errors, needs, app };
}
const until = async (f, ms = 5000) => { for (const t0 = Date.now(); Date.now() - t0 < ms;) { if (await f()) return true; await new Promise((r) => setTimeout(r, 25)); } return false; };
const message = (page) => page.waitForSelector('#needs-xbind', { timeout: 5000 }).catch(() => {});
const started = (page) => page.waitForFunction(() => window.__calls.length > 0, null, { timeout: 5000 }).catch(() => {});

// an xbind with the kit: the app
{
  const { ctx, errors, needs, app } = await open({}, null, started);
  ok('with the kit: the app starts', app.wrap && app.calls > 0, JSON.stringify(app));
  ok('…and nothing says "needs xbind"', needs === null, JSON.stringify(needs));
  ok('…no page errors', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

// an xbind from before the kit: every /vendor/chat/ URL is a 404
{
  const { ctx, page, errors, needs, app } = await open({ noKit: true }, null, message);
  ok('no kit: the page says which xbind it needs', !!needs && needs.text.includes(`This agent needs xbind ≥ ${XBIND} — update xbind`), JSON.stringify(needs));
  ok('…why, in a sentence', !!needs && needs.text.includes('from before the chat kit') && needs.kit === '0', JSON.stringify(needs));
  ok('…with a link to how to update, in a new tab', !!needs && needs.href === '/docs/install.md' && needs.target === '_blank', JSON.stringify(needs));
  ok('…drawn as a panel on the theme', !!needs && needs.color === 'solid', JSON.stringify(needs));
  await page.waitForTimeout(300);
  ok('…and the app is not started: its page is gone, the backend is not asked', !app.wrap && app.calls === 0 && (await page.evaluate(() => window.__calls.length)) === 0, JSON.stringify(app));
  ok('…no page errors: the check itself does not throw', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

// a tile on its own origin: the docs are the workspace's
{
  // xbind's meta, in the head before the page's modules run
  const { ctx, needs } = await open({ noKit: true }, (ctx) => ctx.addInitScript(() => document.addEventListener('readystatechange', () => {
    if (document.readyState !== 'interactive') return;
    document.head.appendChild(Object.assign(document.createElement('meta'), { name: 'xbin-workspace-origin', content: 'https://ws.example' }));
  })), message);
  ok('on a tile origin the link goes to the workspace', !!needs && needs.href === 'https://ws.example/docs/install.md', JSON.stringify(needs));
  await ctx.close();
}

// the kit is there, older than the page needs
{
  const { ctx, needs, app } = await open({}, (ctx) => ctx.route(`${ORIGIN}/boot.js`, (r) =>
    r.fulfill({ contentType: 'text/javascript', body: BOOT.replace('kit: 1,', 'kit: 2,') })), message);
  ok('an older kit: the same message, with both revisions', !!needs && needs.text.includes(`needs xbind ≥ ${XBIND}`)
    && needs.text.includes('serves revision 1') && needs.text.includes('needs revision 2') && needs.kit === '1', JSON.stringify(needs));
  ok('…and no app', !app.wrap && app.calls === 0, JSON.stringify(app));
  await ctx.close();
}

// a revision that is no number is no kit
{
  const { ctx, needs } = await open({}, (ctx) => ctx.route('**/vendor/chat/rev.js', (r) =>
    r.fulfill({ contentType: 'text/javascript', body: 'export const KIT = "soon";' })), message);
  ok('a revision that is not a number: the message', !!needs && needs.kit === '0', JSON.stringify(needs));
  await ctx.close();
}

// agent.js's own failures are not "needs xbind"
for (const [name, body, want] of [
  ['agent.js throws', 'throw new Error("agent.js broke");', /agent\.js broke/],
  ['a module of agent.js is missing', 'import "./no-such-module.js";', /no-such-module|Failed to fetch dynamically imported module|module script/i],
]) {
  let seen = [];
  const { ctx, page, needs } = await open({}, (ctx) => ctx.route(`${ORIGIN}/agent.js`, (r) => r.fulfill({ contentType: 'text/javascript', body })),
    (page, errors) => { seen = errors; return until(() => errors.length > 0); });
  await page.waitForTimeout(200);
  ok(`${name}: an uncaught error, as before`, seen.some((m) => want.test(m)), seen.join(' | '));
  ok(`${name}: never "needs xbind"`, needs === null && !(await page.$('#needs-xbind')), JSON.stringify(needs));
  await ctx.close();
}

await browser.close();
done('needs-xbind');
