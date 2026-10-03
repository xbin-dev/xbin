// hack/ui-harness/passes/themecompat.js — what a tile from before Base Two
// gets (D184; docs/compat.md rule 5; docs/changes/2026-10-03-base-two.md).
// Three probe tiles, each opened in a tab of its own (where xbin.dialog()
// falls back to a <bx-dialog> in the tile's document), in a light and a dark
// system, as the admin:
//   - apps/compatown sets a light palette of its own through the old token
//     names and never opts in: the .bx button under the pointer, the
//     <bx-dialog>'s error line, Delete, Cancel under the pointer and its
//     primary, and a <bx-code>'s well take the document's own colours —
//     every text on its background 4.5:1 or more, no Night patch left;
//   - apps/compatthird is a third-party tile written for the old dark
//     palette: light text hard-coded, white text in fills of the old names,
//     and button.primary / .danger / .quiet / .icon of its own. It stays
//     Night; its own buttons keep their colours and its 20 px icon button
//     its size (the variants and the 28 px controls wait for the opt-in),
//     and the old names keep their old values, so its badges read as they did;
//   - apps/compatin opts in: there the variants and the 28 px controls apply.
// The probes are removed at the end.
const path = require('path');
const { URL, fs, sleep, login, closeCtx, shot, checker } = require('../lib');

const WS = process.env.WS;
const OWN = 'apps/compatown', THIRD = 'apps/compatthird', IN = 'apps/compatin';

const OWN_PAGE = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>own palette</title>
<link rel="stylesheet" href="/vendor/theme.css">
<style>
  /* a tile from before Base Two: a light palette of its own, through the old names */
  :root { --bx-bg: #f3f4f6; --bx-panel: #ffffff; --bx-panel-2: #eef0f3; --bx-border: #d0d5dc; --bx-text: #1d232b;
          --bx-muted: #5b6470; --bx-accent: #0b62d6; --bx-green: #2e7d32; --bx-amber: #9a6700; --bx-red: #c62828; color-scheme: light; }
  body { padding: 16px; }
  bx-code { display: block; height: 160px; margin-top: 12px; border: 1px solid var(--bx-border); }
</style>
<script type="module">
  import '/vendor/bx-dialog.js';
  import '/vendor/bx-code.js';
  const d = document.createElement('bx-dialog');
  d.spec = { title: 'Rename the thing', message: 'The plan: rename it.', error: 'That name is taken.',
    fields: [{ name: 'name', label: 'Name', placeholder: 'a new name' }],
    buttons: [{ label: 'Cancel', value: null }, { label: 'Delete', value: 'del', danger: true }, { label: 'Rename', value: 'ok', primary: true }] };
  d.open = false;
  document.body.append(d);
</script>
</head>
<body class="bx">
<h3>Own palette (old names), not opted in</h3>
<p><button id="plain">Plain button</button> <button id="prim" class="primary">Primary</button> <input placeholder="placeholder"></p>
<bx-code id="code" src="${OWN}"></bx-code>
</body></html>
`;

const THIRD_PAGE = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>third party</title>
<link rel="stylesheet" href="/vendor/theme.css">
<style>
  /* a third-party tile written for the old dark palette: light text hard-coded */
  body { color: #e6e6e6; padding: 12px; }
  .row { display: flex; gap: 6px; align-items: center; margin: 6px 0; flex-wrap: wrap; }
  .fill { padding: 2px 6px; display: inline-block; }
  .ok { background: var(--bx-green); color: #fff; }
  .warn { background: var(--bx-amber); color: #1b1e24; }
  .err { background: var(--bx-red); color: #fff; }
  .red { color: var(--bx-red); }
  /* the tile's own button classes, as many tiles write them */
  button.danger { background: #c62828; color: #fff; border: 0; }
  button.primary { background: #2e7d32; color: #fff; border: 0; }
  button.quiet { background: #333a44; color: #ddd; }
  button.icon { height: 20px; width: 20px; padding: 0; font-size: 11px; }
</style>
</head>
<body class="bx">
<h3>Third-party tile (not opted in)</h3>
<p id="text">Light text hard-coded, <span class="red" id="red">an error in --bx-red</span>.</p>
<div class="row"><span class="fill ok" id="ok">OK white on green</span><span class="fill warn" id="warn">WARN dark on amber</span><span class="fill err" id="errf">ERROR white on red</span></div>
<div class="row"><button id="plain">Plain</button><button class="primary" id="primary">Save</button><button class="danger" id="danger">Delete</button><button class="quiet" id="quiet">Quiet</button><button class="icon" id="icon">x</button></div>
</body></html>
`;

const IN_PAGE = `<!doctype html>
<html lang="en" data-bx-theme="auto">
<head><meta charset="utf-8"><title>opted in</title><link rel="stylesheet" href="/vendor/theme.css"></head>
<body class="bx"><button id="prim" class="primary">Primary</button><button id="plain">Plain</button></body></html>
`;

const FILES = {
  [`${OWN}/xbin.json`]: '{"title":"compat: own palette"}\n', [`${OWN}/index.html`]: OWN_PAGE,
  [`${THIRD}/xbin.json`]: '{"title":"compat: third party"}\n', [`${THIRD}/index.html`]: THIRD_PAGE,
  [`${IN}/xbin.json`]: '{"title":"compat: opted in"}\n', [`${IN}/index.html`]: IN_PAGE,
};

// colours as the browser computes them → [r, g, b] (0–255)
function rgb(s) {
  let m = /rgba?\(([^)]+)\)/.exec(s || '');
  if (m) return m[1].split(/[\s,/]+/).filter(Boolean).slice(0, 3).map(Number);
  m = /color\(srgb ([^)]+)\)/.exec(s || '');
  if (m) return m[1].split(/[\s/]+/).filter(Boolean).slice(0, 3).map((x) => Number(x) * 255);
  return null;
}
const lum = (c) => {
  const [r, g, b] = c.map((v) => { const x = v / 255; return x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4; });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};
const contrast = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };
const hex = (c) => (c ? `#${c.map((v) => Math.round(v).toString(16).padStart(2, '0')).join('')}` : String(c));
const near = (c, h) => c && hex(c) === h.toLowerCase();

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(150);
  }
}

// the colours an element (in the document, or under a shadow root) is drawn in
const look = (page, sel, host = null) => page.evaluate(([sel, host]) => {
  const root = host ? document.querySelector(host)?.shadowRoot : document;
  const el = root?.querySelector(sel);
  if (!el) return null;
  const cs = getComputedStyle(el);
  return { color: cs.color, bg: cs.backgroundColor, border: cs.borderTopColor, h: el.getBoundingClientRect().height };
}, [sel, host]);

async function themeCompat(browser) {
  const { check, done } = checker('theme-compat');
  check(!!WS, 'WS is set (run.sh exports it)');
  for (const [rel, body] of Object.entries(FILES)) {
    const p = path.join(WS, rel);
    fs.mkdirSync(path.dirname(p), { recursive: true });
    fs.writeFileSync(p, body);
  }
  const A = await login(browser, 'admin', 'admin');
  const api = (method, p) => A.ctx.request.fetch(`${URL}/api/xbin${p}`, { method });
  try {
    await until(async () => (await (await api('GET', '/components')).json()).filter((c) => [OWN, THIRD, IN].includes(c.path)).length === 3, 'the probes registered', 20000);
    for (const sys of ['light', 'dark']) {
      const S = await login(browser, 'admin', 'admin', { colorScheme: sys, viewport: { width: 1000, height: 700 } });
      const errors = [];
      S.page.on('pageerror', (e) => errors.push(e.message));
      try {
        // ---- a palette of its own, through the old names ----
        await S.page.goto(`${URL}/c/${OWN}/`);
        await S.page.waitForSelector('#plain');
        await until(() => S.page.evaluate(() => !!customElements.get('bx-dialog') && !!document.querySelector('bx-dialog')?.shadowRoot), 'bx-dialog defined');
        const plain = await look(S.page, '#plain');
        await S.page.hover('#plain');
        await sleep(150);
        const hov = await look(S.page, '#plain');
        const hb = rgb(hov.bg), ht = rgb(hov.color);
        check(hb && lum(hb) > 0.6 && contrast(ht, hb) >= 4.5,
          `${sys}: own palette — a .bx button under the pointer stays light, its text ${contrast(ht, hb).toFixed(2)}:1 on ${hex(hb)} (at rest ${hex(rgb(plain.bg))})`);
        const prim = await look(S.page, '#prim');
        check(!near(rgb(prim.bg), '#0b62d6'),
          `${sys}: own palette — a button.primary of a document that didn't opt in is a plain button (${hex(rgb(prim.bg))})`);
        const code = await until(() => look(S.page, '.main', 'bx-code'), 'the code well');
        const cw = rgb(code.bg);
        check(cw && lum(cw) > 0.6 && contrast([29, 35, 43], cw) >= 4.5, `${sys}: own palette — the <bx-code> well follows the document (${hex(cw)}, its text ${contrast([29, 35, 43], cw).toFixed(2)}:1)`);
        // the dialog, as xbin.dialog() falls back to it in a tile's own tab
        await S.page.evaluate(() => { document.querySelector('bx-dialog').open = true; });
        await until(() => look(S.page, '.err', 'bx-dialog'), 'the dialog open');
        await sleep(200);
        const box = rgb((await look(S.page, '.box', 'bx-dialog')).bg);
        const err = await look(S.page, '.err', 'bx-dialog');
        check(lum(rgb(err.bg)) > 0.6 && contrast(rgb(err.color), rgb(err.bg)) >= 4.5,
          `${sys}: own palette — the dialog's error line is a light tint (${hex(rgb(err.bg))}) with its text ${contrast(rgb(err.color), rgb(err.bg)).toFixed(2)}:1 (${hex(rgb(err.color))})`);
        const del = await look(S.page, 'button.danger', 'bx-dialog');
        check(near(rgb(del.color), '#c62828') && contrast(rgb(del.color), box) >= 4.5,
          `${sys}: own palette — Delete is the document's red, ${contrast(rgb(del.color), box).toFixed(2)}:1 on the dialog (${hex(rgb(del.color))} on ${hex(box)})`);
        const ok = await look(S.page, 'button.primary', 'bx-dialog');
        check(contrast(rgb(ok.color), rgb(ok.bg)) >= 4.5,
          `${sys}: own palette — the dialog's primary: ink ${hex(rgb(ok.color))} on ${hex(rgb(ok.bg))}, ${contrast(rgb(ok.color), rgb(ok.bg)).toFixed(2)}:1`);
        const cancel = await S.page.evaluateHandle(() => document.querySelector('bx-dialog').shadowRoot.querySelector('.btns button:not(.primary):not(.danger)'));
        await cancel.asElement().hover();
        await sleep(150);
        const ch = await S.page.evaluate(() => { const b = document.querySelector('bx-dialog').shadowRoot.querySelector('.btns button:not(.primary):not(.danger)'); const cs = getComputedStyle(b); return { color: cs.color, bg: cs.backgroundColor }; });
        check(lum(rgb(ch.bg)) > 0.6 && contrast(rgb(ch.color), rgb(ch.bg)) >= 4.5,
          `${sys}: own palette — Cancel under the pointer ${hex(rgb(ch.bg))}, its text ${contrast(rgb(ch.color), rgb(ch.bg)).toFixed(2)}:1`);
        await shot(S.page, `theme-compat-own-${sys}`, { fullPage: false });

        // ---- a third-party tile written for the old dark palette ----
        await S.page.goto(`${URL}/c/${THIRD}/`);
        await S.page.waitForSelector('#icon');
        const text = await look(S.page, '#text');
        const panel = rgb(await S.page.evaluate(() => getComputedStyle(document.body).backgroundColor));
        check(contrast(rgb(text.color), panel) >= 4.5 && lum(panel) < 0.1,
          `${sys}: third party — stays Night: its hard-coded light text ${contrast(rgb(text.color), panel).toFixed(2)}:1 on ${hex(panel)}`);
        const own = { danger: ['#c62828', '#ffffff'], primary: ['#2e7d32', '#ffffff'] };
        for (const [id, [bg, fg]] of Object.entries(own)) {
          const b = await look(S.page, `#${id}`);
          check(near(rgb(b.bg), bg) && near(rgb(b.color), fg), `${sys}: third party — its own button.${id} keeps ${bg} / ${fg} (${hex(rgb(b.bg))} / ${hex(rgb(b.color))})`);
        }
        const quiet = await look(S.page, '#quiet');
        check(near(rgb(quiet.bg), '#333a44'), `${sys}: third party — its own button.quiet keeps its background (${hex(rgb(quiet.bg))})`);
        const icon = await look(S.page, '#icon');
        check(Math.round(icon.h) <= 22, `${sys}: third party — its 20 px icon button keeps its size (${icon.h}px tall; 28 px controls wait for the opt-in)`);
        for (const [id, want] of [['ok', '#4caf50'], ['warn', '#f2a71b'], ['errf', '#ef5350']]) {
          const f = await look(S.page, `#${id}`);
          check(near(rgb(f.bg), want), `${sys}: third party — a fill of an old name keeps its old value (${id}: ${hex(rgb(f.bg))}, text ${contrast(rgb(f.color), rgb(f.bg)).toFixed(2)}:1 as before)`);
        }
        const red = await look(S.page, '#red');
        check(contrast(rgb(red.color), panel) >= 4.5, `${sys}: third party — --bx-red as text ${contrast(rgb(red.color), panel).toFixed(2)}:1 on the panel`);
        await shot(S.page, `theme-compat-third-${sys}`, { fullPage: false });

        // ---- opted in: the variants and the 28 px controls ----
        await S.page.goto(`${URL}/c/${IN}/`);
        await S.page.waitForSelector('#prim');
        const ip = await look(S.page, '#prim');
        const accent = rgb(await S.page.evaluate(() => { const p = document.createElement('i'); p.style.color = 'var(--bx-accent)'; document.body.append(p); const c = getComputedStyle(p).color; p.remove(); return c; }));
        check(hex(rgb(ip.bg)) === hex(accent) && Math.round(ip.h) === 28, `${sys}: opted in — button.primary is the accent fill (${hex(rgb(ip.bg))}), 28 px tall (${ip.h})`);
        check(errors.length === 0, `${sys}: no page error (${errors.join(' | ')})`);
      } finally {
        await closeCtx(S.ctx, S.page).catch(() => {});
      }
    }
  } finally {
    for (const t of [OWN, THIRD, IN]) fs.rmSync(path.join(WS, t), { recursive: true, force: true });
    await A.ctx.close();
  }
  done();
}

module.exports = { themeCompat };
