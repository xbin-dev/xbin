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

let srv; let base; let browser;
before(async () => {
  if (skip) return;
  srv = await serve({ '/t/tile.js': TILE, '/t/rt.html': RT_PAGE });
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
