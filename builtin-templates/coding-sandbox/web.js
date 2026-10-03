// web.js — the coding-sandbox page on the web: a header with the tabs, and
// the tabs themselves — Sandboxes (every consumer's, web-ops.js), Images
// (web-ops.js), Settings (web-settings.js) for the tile's operators, and
// Yours (web-mine.js: the page as a consumer of its own — create, lifecycle,
// sharing, files, a terminal) for everyone who may open the page. What they
// say is model/, what they do model/app.js; the native view (native.js)
// draws the same model, held level by model/features.js.
import { html, render, nothing } from '/vendor/lit-all.min.js';
import { createApp } from './model/app.js';
import { opsTab, imagesTab, substrateLabel } from './web-ops.js';
import { settingsTab } from './web-settings.js';
import { mineTab } from './web-mine.js';

export function start(root) {
  const app = createApp();
  // ui is the page's own state (never the model's): the tab, what is open,
  // drafts, a message and an error per tab.
  const ui = { tab: '', msg: '', err: '', busy: '', open: {}, forms: {} };
  const paint = () => render(pageTpl(), root);
  ui.paint = paint;
  // run does one thing a control asked for: busy while it runs, the refusal
  // (or msg) shown after.
  ui.run = async (key, fn, msg = '') => {
    ui.busy = key; ui.err = ''; ui.msg = '';
    paint();
    try { const r = await fn(); ui.msg = typeof r === 'string' ? r : msg; } catch (e) { ui.err = e.message; }
    ui.busy = '';
    paint();
  };
  app.on(paint);
  // The substrate xbind's own runtime is: the workspace — by its branding
  // title, the name its people know it by (GET /api/xbin/branding).
  xbin.fetch('/api/xbin/branding').then((r) => (r.ok ? r.json() : null))
    .then((d) => { ui.brand = (d && d.title) || ''; paint(); }).catch(() => {});

  const tabs = () => (app.operator
    ? [['ops', 'Sandboxes'], ['images', 'Images'], ['settings', 'Settings'], ['mine', 'Yours']]
    : [['mine', 'Your sandboxes']]);
  const go = (t) => { ui.tab = t; ui.msg = ''; ui.err = ''; paint(); };

  function pageTpl() {
    if (!app.loaded) return html`<div class="muted pad">loading…</div>`;
    if (app.err) return html`<div class="pad"><h3>Coding sandboxes</h3><div class="err" id="err">${app.err}</div></div>`;
    const t = tabs();
    if (!t.some(([id]) => id === ui.tab)) ui.tab = t[0][0];
    const b = app.backend();
    return html`<header class="top">
        <h3>Coding sandboxes</h3>
        ${app.operator ? html`<span class="pill" title="the substrate the sandboxes run on">${substrateLabel(b, ui)}</span>` : nothing}
        <nav class="tabs" role="tablist">${t.map(([id, label]) => html`<button role="tab" class="tab ${ui.tab === id ? 'on' : ''}"
          id=${'tab-' + id} aria-selected=${ui.tab === id} @click=${() => go(id)}>${label}</button>`)}</nav>
        <span class="grow"></span>
        <button class="ghost" id="refresh" title="Read everything again" @click=${() => ui.run('refresh', () => app.load())}>↻</button>
      </header>
      <main>
        ${ui.err ? html`<div class="err" id="ui-err" role="alert">${ui.err}</div>` : nothing}
        ${ui.msg ? html`<div class="note" id="ui-msg">${ui.msg}</div>` : nothing}
        ${ui.tab === 'ops' ? opsTab(app, ui) : ui.tab === 'images' ? imagesTab(app, ui) : ui.tab === 'settings' ? settingsTab(app, ui) : mineTab(app, ui)}
      </main>`;
  }

  paint();
  app.load();
  // while the page is on screen, it follows the manager (a creation, an
  // image build, another consumer's sandboxes); hidden, it rests
  setInterval(() => { if (document.visibilityState === 'visible' && !ui.busy) app.load().catch(() => {}); }, 10000);
  return app;
}
