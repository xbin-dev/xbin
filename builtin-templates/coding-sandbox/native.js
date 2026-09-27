// native.js — the coding-sandbox tile's native view: what the xbin app draws
// with platform UI instead of the web page. It renders the SAME model the
// web view drives (model/), one screen at a time:
//
//   the root: tabs — Sandboxes · Images · Settings (operators) · Yours
//     ├─ an operator's sandbox (native/ops.js): facts, lifecycle, snapshots, sharing
//     ├─ an image, the image editor (native/images.js)
//     ├─ a size, a quota, the layout and mounts (native/settings.js)
//     └─ one of yours (native/mine.js) → its files → a file; its terminal
//   + the sheets: a new sandbox, a new folder
//
// The web's features and this view's are held level by model/features.js
// (native-features.js says what this view implements; the parity test is
// hack/coding-sandbox-ui.test.mjs). A UX change lands in the model and in
// both views in the same change.
import { html, render, repeat, native } from '/vendor/xb-native.js';
import { createApp } from './model/app.js';
import { ui, ctx, act, nextFrame } from './native/ui.js';
import { opsSections, opScreen } from './native/ops.js';
import { imagesSections, imageScreen, imageFormScreen } from './native/images.js';
import { settingsSections, sizeScreen, quotaScreen, advancedScreen } from './native/settings.js';
import { mineSections, createSheet, myScreen, filesScreen, fileScreen, mkdirSheet, termScreen, leaveTerminal } from './native/mine.js';

const app = createApp();
ctx.app = app;

let queued = false;
function paint() {
  if (queued) return;
  queued = true;
  nextFrame(() => { queued = false; draw(); });
}
ctx.paint = paint;

const TABS = [{ value: 'ops', label: 'Sandboxes' }, { value: 'images', label: 'Images' }, { value: 'settings', label: 'Settings' }, { value: 'mine', label: 'Yours' }];

function rootScreen() {
  if (!app.loaded) return html`<screen title="Coding sandboxes" style="form"><section><progress label="loading…"/></section></screen>`;
  if (app.err) return html`<screen title="Coding sandboxes" style="form"><section><notice tone="danger" text=${app.err}/></section></screen>`;
  const tabs = app.operator ? TABS : [];
  if (!tabs.some((t) => t.value === ui.tab)) ui.tab = tabs.length ? tabs[0].value : 'mine';
  const body = ui.tab === 'ops' ? opsSections() : ui.tab === 'images' ? imagesSections() : ui.tab === 'settings' ? settingsSections() : mineSections();
  return html`<screen title="Coding sandboxes" subtitle=${app.operator ? app.backend().name : ''} style="form" refreshable
      @refresh=${() => act('refresh', () => app.load())}>
    ${tabs.length ? html`<toolbar><picker label="Show" style="segmented" value=${ui.tab} options=${tabs}
      @change=${(e) => { ui.tab = e.value; ui.err = ''; ui.msg = ''; paint(); }}/></toolbar>` : ''}
    ${ui.err && !ui.stack.length ? html`<section><notice tone="danger" text=${ui.err}/></section>` : ''}
    ${ui.msg && !ui.stack.length ? html`<section><notice tone="ok" text=${ui.msg}/></section>` : ''}
    ${body}
  </screen>`;
}

const SCREENS = {
  op: opScreen, image: imageScreen, imageForm: imageFormScreen, size: sizeScreen, quota: quotaScreen, advanced: advancedScreen,
  mine: myScreen, files: filesScreen, file: fileScreen, term: termScreen,
};

let screens = [];
let meta = '';
function draw() {
  screens = [{ key: 'root', tpl: rootScreen }, ...ui.stack.map((s, i) => ({ key: `${i}:${s.kind}:${s.id || ''}:${s.path || ''}`, s, tpl: () => SCREENS[s.kind](s) }))];
  render(html`<nav @pop=${pop}>${repeat(screens, (x) => x.key, (x) => x.tpl())}</nav>${createSheet()}${mkdirSheet()}`);
  // the tile's badge in the app's navigator: sandboxes of yours that run
  const running = (app.mine || []).filter((s) => s.state === 'running').length;
  const m = { title: 'Coding sandboxes', badge: running ? String(running) : null };
  const key = JSON.stringify(m);
  if (key !== meta) { meta = key; try { native.meta(m); } catch { /* no app */ } }
}

// pop: the person went back to `depth` screens; the screens above leave (a
// terminal's shell ends with it).
function pop(e) {
  const depth = Math.max(1, Math.min(screens.length, Number(e.depth) || 1));
  for (const x of screens.slice(depth)) if (x.s) leaveTerminal(x.s);
  ui.stack.length = depth - 1;
  ui.err = '';
  const t = ui.stack[ui.stack.length - 1];
  if (t && t.kind === 'files') app.browse(t.id, t.path); // back to a directory: read it again
  paint();
}

app.on(paint);
paint();
app.load();
// while the tile is on screen it follows the manager; hidden, it rests
setInterval(() => {
  if ((globalThis.document?.visibilityState ?? 'visible') === 'visible' && !ui.busy) app.load().catch(() => {});
}, 15000);

export { app };
