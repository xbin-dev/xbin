// The Lit reference renderer (web/xb/render.js) in headless Chromium:
//   - every tree it is given — the design's eight (plans/native.md §18) and
//     the review gallery (native/tools/gallery) — draws every node a user
//     can see, as an xb-* element with data-k, without page errors;
//   - the preview host drives a real runtime (/vendor/xb-native.js) end to
//     end: typing, taps, a toggle, a confirmation, a sheet's dismiss and a
//     nav pop reach the tile, and the tile's re-renders reach the screen as
//     patches (including a controlled field reset while it shows typed text).
// Needs Playwright with Chromium (PLAYWRIGHT_DIR, default ~/lcad-wasm, as
// the UI harness); without it the tests skip.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { serve, playwright, designTrees } from '../native/tools/shots.mjs';

const ROOT = new URL('..', import.meta.url).pathname;
let pw = null;
try { pw = playwright(); } catch { /* skipped below */ }
const skip = pw ? false : 'Playwright not found (set PLAYWRIGHT_DIR)';

const TILE = `
import { html, render, nothing } from '/vendor/xb-native.js';
let count = 0, draft = '', on = false, sheet = true, pushed = true;
const paint = () => render(html\`
  <nav @pop=\${() => { pushed = false; paint(); }}>
    <screen title="Test" style="form">
      <section title="s">
        <row title="Count" detail=\${count}/>
        <field label="Name" value=\${draft} @input=\${(e) => { draft = e.value; paint(); }}/>
        <text>\${'hello ' + draft}</text>
        <toggle label="On" value=\${on} @change=\${(e) => { on = e.value; paint(); }}/>
        <button role="primary" @tap=\${() => { count++; paint(); }}>inc</button>
        <button role="destructive" confirm=\${{ title: 'Reset?', label: 'Reset', destructive: true }}
                @tap=\${() => { count = 0; draft = ''; paint(); }}>reset</button>
      </section>
    </screen>
    \${pushed ? html\`<screen title="Pushed"><text>deep</text></screen>\` : nothing}
  </nav>
  <sheet open=\${sheet} title="Hi" @dismiss=\${() => { sheet = false; paint(); }}><text>sheet body</text></sheet>\`);
paint();
`;
const RT_PAGE = `<!doctype html><html><head><meta charset="utf-8">
<meta name="xbin-native-preview" content="1">
<script type="module">
import '/vendor/xb-native.js';
await import('/vendor/xb/preview-host.js');
await import('/t/tile.js');
</script></head><body></body></html>`;

// the preview host with a stand-in xbin whose fetch records what it was asked
const IMG_PAGE = `<!doctype html><html><head><meta charset="utf-8">
<meta name="xbin-native-preview" content="1">
<script>window.fetched = []; window.xbin = { self: 'apps/t', fetch: (u, o) => { window.fetched.push(String(u)); return fetch(u, o); } };</script>
<script type="module">
import '/vendor/xb-native.js';
await import('/vendor/xb/preview-host.js');
</script></head><body></body></html>`;

let srv; let base; let browser;
before(async () => {
  if (skip) return;
  srv = await serve({ '/t/tile.js': TILE, '/t/rt.html': RT_PAGE, '/t/img.html': IMG_PAGE,
    '/c/apps/t/logo.png': 'own', '/c/apps/other/logo.png': 'theirs', '/c/apps/t/sub/logo.png': 'nested',
    '/api/xbin/components': JSON.stringify([{ path: 'apps/t' }, { path: 'apps/t/sub' }, { path: 'apps/other' }]) });
  base = `http://127.0.0.1:${srv.address().port}`;
  browser = await pw.chromium.launch();
});
after(async () => { await browser?.close(); srv?.close(); });

async function page() {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, timezoneId: 'UTC', locale: 'en-US' });
  const p = await ctx.newPage();
  const errors = [];
  p.on('pageerror', (e) => errors.push(`page error: ${e.message}`));
  p.on('console', (m) => { if (m.type() === 'error') errors.push(`console: ${m.text()}`); });
  return { p, ctx, errors };
}

// the keys a user can see: closed sheets, collapsed sections, closed
// disclosures/tool cards, menus and row/message actions (drawn in their popover) hide their children
function visible(n, out = new Set()) {
  out.add(n.k);
  const p = n.p || {};
  if (n.t === 'sheet' && p.open === false) return out;
  if (n.t === 'section' && p.collapsible && p.collapsed) return out;
  if ((n.t === 'disclosure' || n.t === 'toolcard') && !p.open) return out;
  if (n.t === 'menu') return out;
  if (n.t === 'actions') return out; // a row's or message's actions fold into a popover (render-structure.js folded)
  for (const c of n.c || []) visible(c, out);
  return out;
}

function trees() {
  const out = { ...designTrees() };
  const dir = join(ROOT, 'native/tools/gallery');
  for (const f of readdirSync(dir).filter((x) => x.endsWith('.json'))) out[`gallery/${f.slice(0, -5)}`] = JSON.parse(readFileSync(join(dir, f), 'utf8'));
  return out;
}

test('every tree draws every visible node', { skip }, async () => {
  const { p, ctx, errors } = await page();
  await p.goto(`${base}/vendor/xb/fixture.html`);
  await p.waitForFunction(() => window.xbnFixture);
  for (const [name, tree] of Object.entries(trees())) {
    await p.evaluate((t) => window.xbnFixture.load(t), tree);
    const drawn = await p.evaluate(() => {
      const root = document.querySelector('xb-view').shadowRoot;
      return { keys: [...root.querySelectorAll('[data-k]')].map((e) => e.dataset.k),
        unknown: [...root.querySelectorAll('xb-unknown')].map((e) => e.textContent.trim()) };
    });
    const want = [...visible(tree.root)];
    const have = new Set(drawn.keys);
    assert.deepEqual(want.filter((k) => !have.has(k)), [], `${name}: nodes not drawn`);
    assert.equal(drawn.keys.length, have.size, `${name}: a key drawn twice`);
    assert.deepEqual(drawn.unknown, name === 'gallery/content' ? ['mystery-prim'] : [], `${name}: placeholders`);
  }
  assert.deepEqual(errors, []);
  await ctx.close();
});

test('the preview host drives a runtime end to end', { skip }, async () => {
  const { p, ctx, errors } = await page();
  await p.goto(`${base}/t/rt.html`);
  await p.waitForFunction(() => window.xbnPreview);
  assert.deepEqual(await p.evaluate(() => window.xbnPreview.ready), { ok: true });
  const k = (key) => p.locator(`xb-view [data-k="${key}"]`);
  const last = () => p.evaluate(() => window.xbnPreview.messages.filter((m) => m.op === 'patch').at(-1)?.ops ?? null);

  // the sheet is open over the pushed screen; its close button dismisses it
  // (the runtime's shadow already says open:false, so no patch comes back)
  await k('r.1').locator('.sheet-x').click();
  await k('r.1').waitFor({ state: 'hidden' });
  // back pops the pushed screen; the tile drops it
  await k('r.0.1').locator('.back').click();
  await p.waitForFunction(() => window.xbnPreview.tree().root.c[0].c.length === 1);
  assert.deepEqual(await last(), [['remove', 'r.0.1']]);
  await k('r.0.0').waitFor({ state: 'visible' });

  // typing: one input event per key, the text beside it follows by patch,
  // and the field keeps what was typed
  await k('r.0.0.0.1').locator('input').pressSequentially('ab');
  await p.waitForFunction(() => window.xbnPreview.view.shadowRoot.querySelector('[data-k="r.0.0.0.2"]').textContent === 'hello ab');
  assert.equal(await k('r.0.0.0.1').locator('input').inputValue(), 'ab');
  assert.deepEqual(await last(), [['set', 'r.0.0.0.2', { text: 'hello ab' }]], 'no patch echoes the typed value back');

  await k('r.0.0.0.4').locator('button').click();
  await p.waitForFunction(() => window.xbnPreview.view.shadowRoot.querySelector('[data-k="r.0.0.0.0"] .row-detail')?.textContent === '1');

  await k('r.0.0.0.3').locator('[role=switch]').click();
  assert.equal(await k('r.0.0.0.3').locator('[role=switch]').getAttribute('aria-checked'), 'true');
  await p.waitForFunction(() => window.xbnPreview.tree().root.c[0].c[0].c[0].c[3].p.value === true);

  // a confirmation first; cancel does nothing, the confirm label resets —
  // the reset of a controlled field reaches the field that shows "ab"
  await k('r.0.0.0.5').locator('button').click();
  await p.locator('xb-view .as-cancel').click();
  assert.equal(await k('r.0.0.0.1').locator('input').inputValue(), 'ab');
  await k('r.0.0.0.5').locator('button').click();
  await p.locator('xb-view .as-btn.danger').click();
  await p.waitForFunction(() => window.xbnPreview.view.shadowRoot.querySelector('[data-k="r.0.0.0.0"] .row-detail')?.textContent === '0');
  assert.equal(await k('r.0.0.0.1').locator('input').inputValue(), '');
  assert.deepEqual(await p.evaluate(() => window.xbnPreview.errors), []);
  assert.deepEqual(errors, []);
  await ctx.close();
});

test('a row\'s and a message\'s actions open as a popover and fire', { skip }, async () => {
  const { p, ctx, errors } = await page();
  await p.goto(`${base}/vendor/xb/fixture.html`);
  await p.waitForFunction(() => window.xbnFixture);
  const btn = (k, label, extra = {}) => ({ k, t: 'button', p: { label, ...extra }, e: ['tap'] });
  await p.evaluate((t) => window.xbnFixture.load(t), { v: 1, root: { k: 'r', t: 'screen', p: { title: 'T', style: 'scroll' }, c: [
    { k: 'r.0', t: 'list', c: [{ k: 'r.0.0', t: 'row', p: { title: 'a row' }, c: [{ k: 'r.0.0.0', t: 'actions', c: [
      btn('r.0.0.0.0', 'Pin'), btn('r.0.0.0.1', 'Delete', { role: 'destructive', confirm: { title: 'Delete?', label: 'Delete', destructive: true } })] }] }] },
    { k: 'r.1', t: 'transcript', c: [{ k: 'r.1.0', t: 'message', p: { role: 'user', text: 'hi' }, c: [{ k: 'r.1.0.0', t: 'actions', c: [btn('r.1.0.0.0', 'Copy it')] }] }] },
  ] } });
  const root = p.locator('xb-view');
  assert.equal(await root.locator('[data-k="r.0.0.0.0"]').count(), 0, 'folded out of sight');
  await root.locator('[data-k="r.0.0"] .row-more').click();
  await root.locator('.pop [data-k="r.0.0.0.0"] button').click();
  await root.locator('[data-k="r.0.0"] .row-more').click();
  await root.locator('.pop [data-k="r.0.0.0.1"] button').click();
  await root.locator('.as-btn.danger').click(); // a destructive action confirms first
  await root.locator('[data-k="r.1.0"] .m-bubble').click({ button: 'right' });
  await root.locator('.pop [data-k="r.1.0.0.0"] button').click();
  // …and behind the message's ⋯, as the app folds them
  await root.locator('[data-k="r.1.0"] .m-more').click();
  await root.locator('.pop [data-k="r.1.0.0.0"] button').click();
  const taps = await p.evaluate(() => window.xbnFixture.events.map((e) => e[0] + ':' + e[1]));
  assert.deepEqual(taps, ['r.0.0.0.0:tap', 'r.0.0.0.1:tap', 'r.1.0.0.0:tap', 'r.1.0.0.0:tap']);
  assert.deepEqual(errors, []);
  await ctx.close();
});

// Vocabulary rev 2 (D189) in the reference renderer: a split that collapses
// to a stack on a phone and shows both columns when wide, toolbars by place,
// submenus, a row's leading and trailing actions in one menu, a full-screen
// sheet with one stacked over it, and the scroll anchor, jump and edges.
const fixtureTree = (name) => JSON.parse(readFileSync(join(ROOT, `native/fixtures/${name}/expected.json`), 'utf8'));

test('rev 2: a split collapses to a stack, its Back and sidebar report close and columns', { skip }, async () => {
  const { p, ctx, errors } = await page();
  await p.goto(`${base}/vendor/xb/fixture.html`);
  await p.waitForFunction(() => window.xbnFixture);
  await p.evaluate((t) => window.xbnFixture.load(t), fixtureTree('split-collapse'));
  const root = p.locator('xb-view');
  const shown = (sel) => root.locator(sel).first().isVisible();
  // a phone: the deep-linked ticket over the list, with Back to it
  assert.equal(await shown('.split-a'), false);
  assert.equal(await shown('.split-b'), true);
  await root.locator('.split-b .split-back').click();
  assert.equal(await shown('.split-a'), true, 'Back shows the list');
  assert.equal(await shown('.split-b'), false);
  // a tablet: both columns (columns="detail" hides the list until the sidebar button)
  await p.setViewportSize({ width: 1024, height: 768 });
  await p.evaluate((t) => window.xbnFixture.load(t), fixtureTree('split-collapse'));
  assert.equal(await shown('.split-a'), false, 'columns=detail');
  assert.equal(await shown('.split-b .split-back'), false, 'no Back beside the list');
  await root.locator('.split-b .split-cols').click();
  assert.equal(await shown('.split-a'), true, 'the sidebar comes back');
  const evs = await p.evaluate(() => window.xbnFixture.events.map((e) => [e[1], e[2]]));
  assert.deepEqual(evs.filter(([t]) => t !== 'edge'), [['close', {}], ['columns', { value: 'all' }]]);
  assert.deepEqual(errors, []);
  await ctx.close();
});

test('rev 2: toolbars by place, a submenu, and a row\'s leading and trailing actions', { skip }, async () => {
  const { p, ctx, errors } = await page();
  await p.goto(`${base}/vendor/xb/fixture.html`);
  await p.waitForFunction(() => window.xbnFixture);
  await p.evaluate((t) => window.xbnFixture.load(t), fixtureTree('search-toolbars'));
  const root = p.locator('xb-view');
  assert.equal(await root.locator('.bar-lead [data-k="r.0.0"]').count(), 1, 'the leading toolbar in the bar\'s leading end');
  assert.equal(await root.locator('.bar-trail [data-k="r.1.0"]').count(), 1);
  assert.equal(await root.locator('.bottombar [data-k="r.2.1"]').count(), 1, 'the bottom toolbar');
  assert.equal(await root.locator('.bar-trail .spin').count(), 1, 'refreshing: the refresh button spins');
  await root.locator('[data-k="r.0.0"] button').click();
  await root.locator('.pop [data-k="r.0.0.3"] button').click(); // Group by ▸
  await root.locator('.pop [data-k="r.0.0.3.1"] button').click(); // Label
  await root.locator('[data-k="r.3.0:i-405"] .row-more').click();
  const items = await root.locator('.pop button').allTextContents();
  assert.deepEqual(items.map((s) => s.trim()), ['Mark unread', 'Archive', 'Delete'], 'leading first, then trailing');
  await root.locator('.pop [data-k="r.3.0:i-405.0.0"] button').click();
  // search: scopes, then a suggestion (focus shows them) submits
  await root.locator('.search-scopes button', { hasText: 'Closed' }).click();
  await root.locator('.search input').focus();
  await root.locator('.sugg', { hasText: 'label:billing' }).click();
  const evs = await p.evaluate(() => window.xbnFixture.events.map((e) => [e[0], e[1], e[2]]));
  assert.deepEqual(evs.filter(([, t]) => t !== 'edge'), [
    ['r.0.0.3.1', 'tap', {}], ['r.3.0:i-405.0.0', 'tap', {}], ['r', 'scope', { value: 'closed' }],
    ['r', 'search', { value: 'label:billing' }], ['r', 'submit', { value: 'label:billing' }]]);
  assert.deepEqual(errors, []);
  await ctx.close();
});

test('rev 2: a full-screen sheet with a sheet stacked over it', { skip }, async () => {
  const { p, ctx, errors } = await page();
  await p.goto(`${base}/vendor/xb/fixture.html`);
  await p.waitForFunction(() => window.xbnFixture);
  await p.evaluate((t) => window.xbnFixture.load(t), fixtureTree('sheet-stack'));
  const root = p.locator('xb-view');
  assert.equal(await root.locator('[data-k="r.1"] > .sheet.d-full').count(), 1);
  assert.equal(await root.locator('[data-k="r.1"] > .sheet .grabber').count(), 0, 'a cover has no grabber');
  const inner = root.locator('[data-k="r.1.3"] .sheet.d-medium');
  assert.equal(await inner.isVisible(), true);
  // the inner sheet is on top: its field takes the click
  await inner.locator('input').first().click();
  assert.deepEqual(errors, []);
  await ctx.close();
});

test('rev 2: the scroll anchor keeps its child in place; scrollTo jumps; edges report', { skip }, async () => {
  const { p, ctx, errors } = await page();
  await p.goto(`${base}/vendor/xb/fixture.html`);
  await p.waitForFunction(() => window.xbnFixture);
  const msgs = (from, to) => Array.from({ length: to - from + 1 }, (_, i) => ({ k: `r.0.0:m${from + i}`, t: 'message', p: { role: 'assistant', text: `message ${from + i} `.repeat(12) } }));
  const tree = (list, extra = {}) => ({ v: 1, root: { k: 'r', t: 'screen', p: { title: 'Chat' }, c: [
    { k: 'r.0', t: 'transcript', p: { anchor: 'm20', ...extra }, e: ['edge'], c: list }] } });
  await p.evaluate((t) => window.xbnFixture.load(t), tree(msgs(10, 30)));
  const opened = await p.evaluate(() => { const t = window.xbnFixture.view.shadowRoot.querySelector('xb-transcript'); return t.querySelector('[data-k="r.0.0:m20"]').getBoundingClientRect().top - t.getBoundingClientRect().top; });
  assert.ok(opened >= 0 && opened < 40, `the transcript opens at its anchor (${opened})`);
  const top = () => p.evaluate(() => window.xbnFixture.view.shadowRoot.querySelector('[data-k="r.0.0:m20"]').getBoundingClientRect().top);
  await p.evaluate(() => { const t = window.xbnFixture.view.shadowRoot.querySelector('xb-transcript'); t.scrollTop = t.querySelector('[data-k="r.0.0:m20"]').offsetTop - 100; });
  const before = await top();
  await p.evaluate((t) => window.xbnFixture.load(t), tree(msgs(1, 30))); // older messages above
  assert.ok(Math.abs((await top()) - before) <= 1, `the anchor stays put (${before} → ${await top()})`);
  await p.evaluate((t) => window.xbnFixture.load(t), tree(msgs(1, 30), { scrollTo: 'end#1' }));
  await p.waitForFunction(() => window.xbnFixture.events.some((e) => e[1] === 'edge' && e[2].edge === 'end' && e[2].at));
  const atEnd = await p.evaluate(() => { const t = window.xbnFixture.view.shadowRoot.querySelector('xb-transcript'); return t.scrollHeight - t.scrollTop - t.clientHeight; });
  assert.ok(atEnd < 2, 'scrollTo end');
  await p.evaluate((t) => window.xbnFixture.load(t), tree(msgs(1, 30), { scrollTo: 'm5' }));
  await p.waitForFunction(() => window.xbnFixture.events.some((e) => e[1] === 'edge' && e[2].edge === 'end' && !e[2].at));
  assert.deepEqual(errors, []);
  await ctx.close();
});

// A message's image files are thumbnails that open full screen; other files
// are chips (native/fixtures/message-files).
test('message thumbnails open the image preview', { skip }, async () => {
  const { p, ctx, errors } = await page();
  await p.goto(`${base}/vendor/xb/fixture.html`);
  await p.waitForFunction(() => window.xbnFixture);
  const tree = JSON.parse(readFileSync(join(ROOT, 'native/fixtures/message-files/expected.json'), 'utf8'));
  await p.evaluate((t) => window.xbnFixture.load(t), tree);
  const root = p.locator('xb-view');
  assert.equal(await root.locator('.m-thumb').count(), 3, 'two tile photos and the data: label');
  assert.equal(await root.locator('.m-file').count(), 3, 'a PDF, a CSV and an image without a src');
  await root.locator('[data-k="r.0.0:m3"] .m-thumb').click();
  await root.locator('.ov-img img').waitFor();
  assert.deepEqual(await p.evaluate(() => window.xbnFixture.events), [], 'a thumbnail is no tap on the message');
  assert.deepEqual(errors.filter((e) => !/Failed to load resource/.test(e)), []);
  await ctx.close();
});

// Input methods (plans/native.md §24, tree.md §6): nothing is reported
// while a composition is in progress, the commit is reported once, and a
// value the tile sets meanwhile replaces the composed text at the end —
// the app's TextInputGate, here driven through Chromium's IME emulation.
test('text controls hold input back while an input method composes', { skip }, async () => {
  const { p, ctx, errors } = await page();
  await p.goto(`${base}/vendor/xb/fixture.html`);
  await p.waitForFunction(() => window.xbnFixture);
  await p.evaluate((t) => window.xbnFixture.load(t), { v: 1, root: { k: 'r', t: 'screen', p: { title: 'T', style: 'form' }, c: [
    { k: 'f', t: 'field', p: { label: 'Name', value: '' }, e: ['input'] },
    { k: 'm', t: 'field', p: { label: 'Note', kind: 'multiline', value: '' }, e: ['input'] },
    { k: 'c', t: 'composer', p: { value: '' }, e: ['input', 'send'] },
  ] } });
  const cdp = await ctx.newCDPSession(p);
  const ime = (text) => cdp.send('Input.imeSetComposition', { text, selectionStart: text.length, selectionEnd: text.length });
  const commit = async (text) => { await cdp.send('Input.insertText', { text }); await p.evaluate(() => window.xbnFixture.settle()); };
  const events = () => p.evaluate(() => window.xbnFixture.events.map((e) => [e[0], e[1], e[2].value]));

  const input = p.locator('xb-view [data-k="f"] input');
  await input.focus();
  for (const step of ['t', 'と', 'とう', 'とうきょう']) await ime(step);
  assert.equal(await input.inputValue(), 'とうきょう');
  assert.deepEqual(await events(), [], 'nothing while composing');
  await commit('東京');
  assert.deepEqual(await events(), [['f', 'input', '東京']], 'the commit, once');

  // The tile clears the field during the next composition: the text being
  // composed stays until the composition ends, then the tile's value wins.
  await ime('と');
  await p.evaluate(() => window.xbnFixture.apply({ op: 'patch', n: 2, ops: [['set', 'f', { value: '' }]] }));
  assert.equal(await input.inputValue(), '東京と', 'a set never lands mid-composition');
  await commit('都');
  assert.equal(await input.inputValue(), '');
  assert.deepEqual(await events(), [['f', 'input', '東京']], 'the composed text is not reported');

  // Plain typing still reports every change.
  await input.pressSequentially('ab');
  assert.deepEqual((await events()).slice(1), [['f', 'input', 'a'], ['f', 'input', 'ab']]);

  // A multiline field and the composer follow the same rules.
  for (const k of ['m', 'c']) {
    const ta = p.locator(`xb-view [data-k="${k}"] textarea`);
    await ta.focus();
    for (const step of ['ㅎ', '하', '한']) await ime(step);
    await commit('한');
    assert.deepEqual((await events()).filter((e) => e[0] === k), [[k, 'input', '한']], `${k}: one report`);
  }
  assert.deepEqual(errors, []);
  await ctx.close();
});

// xbin.fetch attaches the tile's frame token to whatever URL it is given: the
// preview host loads images (and uploads) through it under the app's rule
// (D103, web/xb/tile-resource.js) — the tile's own /c/ and /api/ paths only,
// never another tile's, a nested tile's, xbind's API or another site — and
// uploads by PUT, POST or PATCH, as the app does.
test('the preview host confines images and uploads as the app does', { skip }, async () => {
  const { p, ctx } = await page();
  await p.goto(`${base}/t/img.html`);
  await p.waitForFunction(() => window.xbnPreview);
  const got = await p.evaluate(async () => {
    const v = window.xbnPreview.view;
    const img = {};
    for (const src of ['https://elsewhere.example/a.png', '/c/apps/other/logo.png', '/api/xbin/whoami', '/t/tile.js',
      '../other/logo.png', 'sub/logo.png', '/c/apps/t/logo.png', 'logo.png']) {
      img[src] = await v.loadImage(src).then((u) => (u.startsWith('blob:') ? 'blob' : u), (e) => String(e.message));
    }
    const file = new File(['x'], 'a b.png', { type: 'image/png' });
    for (const [path, method] of [['upload?name={name}', 'PUT'], ['/api/apps/t/u', 'post'], ['/api/apps/t/u', 'DELETE'],
      ['/api/other/u', 'PUT'], ['/api/apps/tx/u', 'PUT'], ['/api/apps/t/../x', 'PUT'], ['/api/apps/t/%2e%2e/x', 'PUT'],
      ['/api/apps/t/sub/u', 'PUT'], ['/api/xbin/frame-token', 'PUT']]) {
      await v.onupload({ k: 'c', p: { upload: { path, method } } }, file);
    }
    return { img, fetched: window.fetched };
  });
  const refused = 'not a tile resource';
  assert.deepEqual(got.img, {
    'https://elsewhere.example/a.png': refused, '/c/apps/other/logo.png': refused, '/api/xbin/whoami': refused,
    '/t/tile.js': refused, '../other/logo.png': refused, 'sub/logo.png': refused,
    '/c/apps/t/logo.png': 'blob', 'logo.png': 'blob',
  });
  // the tile list is read once; only the tile's own paths are fetched
  assert.deepEqual(got.fetched, ['/api/xbin/components', '/c/apps/t/logo.png', '/c/apps/t/logo.png',
    '/api/apps/t/upload?name=a%20b.png', '/api/apps/t/u']);
  await ctx.close();
});
