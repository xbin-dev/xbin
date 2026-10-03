// harness.mjs — coding-harness conversations in the web view over the
// fixtures (test/harness-fixtures.mjs, STUB's §4 routes), as U1 left them
// (D147 §8 U1): the cards draw every `acp:*` call in the built-in's
// frame (family, headline, outcome — harness-cards.js's since U3), a Task's
// calls open inside it, a park no module answers falls back to the built-in card,
// the direct-steering notice folds — and each web seam (web-ext.js) draws
// where it says once a module hooks it: block, end (taking a harness park
// over), top, paint, newChat.
//
//   node test/harness.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';
import { harnessSeed } from './harness-fixtures.mjs';

const { ok, done } = checker();
const browser = await launch();
const ctx = await browser.newContext();
await serveTile(ctx);
await ctx.addInitScript(STUB, harnessSeed());
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
const go = async (id, sel) => { await page.evaluate((h) => { location.hash = h; }, '#c=' + id); await page.waitForSelector(sel); };

await page.goto(`${ORIGIN}/#c=21`);
await page.waitForSelector('.tcard[data-tool="acp:execute"]');
const cards = await page.$$eval('#timeline .tcard', (els) => els.map((e) => ({
  k: e.dataset.k, fam: e.dataset.fam, tool: e.dataset.tool, ic: e.querySelector('.ic bx-icon')?.getAttribute('name') || e.querySelector('.ic').textContent, hl: e.querySelector('.hl').textContent.trim(),
  oc: e.querySelector('.oc')?.textContent || '', tone: e.querySelector('.oc')?.className || '' })));
const card = (id) => cards.find((c) => c.k === 'c' + id) || {};
ok('every acp call is a card', cards.length === 11, cards.map((c) => c.k).join());
ok('execute: the adapter\'s label, exit 1 as bad news', card('h1:toolu_10').hl.startsWith('Run the flaky test 20 times') && card('h1:toolu_10').oc === 'exit 1'
  && /bad/.test(card('h1:toolu_10').tone), JSON.stringify(card('h1:toolu_10')));
ok('…its command under it', (await page.textContent('[data-k="ch1:toolu_10"] .sub')) === '$ go test ./... -run TestClientRetry -count=20');
ok('edit: the pencil glyph and +5 −1', card('h1:toolu_06').ic === 'pencil' && card('h1:toolu_06').oc === '+5 −1', JSON.stringify(card('h1:toolu_06')));
ok('the families of the harness kinds', ['edit', 'del', 'move', 'search', 'think', 'mode', 'web', 'file', 'box', 'other'].every((f) => cards.some((c) => c.fam === f)),
  [...new Set(cards.map((c) => c.fam))].join());
ok('a Task\'s calls are inside it, not beside it', !(await page.$('#timeline [data-k="ch1:toolu_04"]')));
await page.click('[data-k="ch1:toolu_03"] .tch');
await page.waitForSelector('[data-k="ch1:toolu_03"] [data-k="ch1:toolu_04"]');
ok('…and open with it, its text too', !!(await page.$('[data-k="ch1:toolu_03"] .msg.assistant')));
// the top bar: Memory, Learn skill and Compact are the built-in agent's (Compact: a coding agent's own /compact only)
const topBtns = () => page.$$eval('#top button', (els) => els.map((e) => e.textContent.trim()));
ok('a coding agent\'s top bar: no Memory or Learn skill; Compact, as Claude Code has /compact', !(await topBtns()).some((t) => /^(Memory|Learn skill)/.test(t))
  && (await topBtns()).includes('Compact'), (await topBtns()).join(' | '));

// a park: its module (harness-ask.js, U4) draws it — the built-in card is only the fallback while none answers
await go(22, '.hask');
ok('a harness park is its module\'s card, not the built-in one', !(await page.$('.ask.approve')) && (await page.textContent('.hask')).includes('go test ./...'));
ok('its call waits for the verdict', (await page.textContent('[data-k="ch2:toolu_02"] .st')) === 'needs approval');
ok('the activity line is the harness\'s', (await page.textContent('.activity')).includes('Waiting for your approval'));
// a park of a kind no module answers (one a later backend may add): the built-in card, the fallback
const repark = (ps, result) => page.evaluate(([ps, result]) => {
  const r = window.__views[22].run;
  Object.assign(r, { pendingState: ps, result });
  window.__push({ type: 'run', run: 22, root: 22, data: { id: 22, status: 'waiting_input', pendingState: ps, result, harness: r.harness } });
}, [ps, result]);
const ps22 = await page.evaluate(() => window.__views[22].run.pendingState);
await repark({ kind: 'review', park: 'Xq3review', harness: { message: 'Review the retry policy?' } }, 'Review the retry policy?');
await page.waitForFunction(() => !document.querySelector('.hask'));
ok('a park no module answers falls back to the built-in question card', (await page.textContent('#timeline .ask')).includes('The agent is asking')
  && (await page.textContent('#timeline .ask')).includes('Review the retry policy?') && !(await page.$('#hlogin')));
await repark(ps22, undefined);
await page.waitForSelector('.hask');

// the direct-steering notice and three coding agents under the built-in one
await go(25, '.acard');
ok('three child cards', (await page.$$('.acard')).length === 3);
ok('…the built-in agent\'s top bar keeps Compact, Learn skill and Memory', await topBtns().then((ts) => ['Compact', 'Learn skill', 'Memory'].every((b) => ts.some((t) => t.startsWith(b)))), (await topBtns()).join(' | '));
ok('a person\'s message to a child is told as a notice', (await page.textContent('.notice .nh')).includes('direct message to #26 (Claude Code) from admin'));

// --- the seams: a module hooks each one -------------------------------------------------
await page.evaluate(async () => {
  const { html } = await import('/vendor/lit-all.min.js');
  const { ext, ctx } = await import('/web-ext.js');
  window.__painted = 0;
  ext.register({
    // (the acp:* cards are harness-cards.js's, registered first: the probe takes the answers)
    block: (b) => (b.k === 'assistant' && !b.parent ? html`<div class="probe-block" data-k=${b.id}>${b.text}</div>` : null),
    end: (s) => (s.run.engine === 'harness' ? html`<div class="probe-end">${s.run.harness.state}</div>` : null),
    top: (v) => html`<span class="probe-top">${v ? '#' + v.run.id : 'home'} · ${ctx.app.me.user}</span>`,
    paint: () => { window.__painted++; },
    newChat: (redraw) => {
      let n = 0;
      return { tpl: () => html`<button type="button" class="probe-field" @click=${() => { n++; redraw(); }}>${n}</button>`, body: () => ({ probe: n }) };
    },
  });
});
await go(21, '.probe-block');
ok('block: a module draws a block its way', (await page.$$('.probe-block')).length === 2 && !(await page.$('#timeline > .msg.assistant')));
ok('end: after the transcript', (await page.textContent('.probe-end')) === 'ready');
ok('top: a chip in the top bar (ctx: the app)', (await page.textContent('#top .probe-top')) === '#21 · admin');
ok('paint: after every paint', await page.evaluate(() => window.__painted > 0));
await go(22, '.probe-end');
ok('end: a harness park is the module\'s once it answers', !(await page.$('.ask.approve')));
await go(25, '.acard');
ok('end: a hook answers only for what it knows (not the built-in agent\'s conversation)', !(await page.$('.probe-end')));
await page.click('#home');
await page.waitForSelector('#top .probe-top');
ok('top: at home too', (await page.textContent('#top .probe-top')) === 'home · admin');
await page.click('#newopts');
await page.click('#n-ext .probe-field');
ok('newChat: its field in the dialog, redrawn on its own', (await page.textContent('#n-ext .probe-field')) === '1');
await page.fill('#n-goal', 'a new chat');
await page.click('#n-create');
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'POST' && c.url.endsWith('/ask')));
const ask = await page.evaluate(() => JSON.parse(window.__calls.find((c) => c.method === 'POST' && c.url.endsWith('/ask')).body));
ok('…and its part of the ask', ask.probe === 1 && ask.text === 'a new chat', JSON.stringify(ask));

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('harness');
