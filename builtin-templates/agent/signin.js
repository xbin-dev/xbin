// signin.js — a coding agent's sign-in on the web (D-harness §4.2.6,
// §4.3.4, §8 U5): the card at the end of a harness conversation parked on
// `login` (only then — the `end` seam's rule: a park this module doesn't
// draw is left to its own module or the built-in card). It offers the
// agent's methods:
//
// - a terminal login: a login tab in the terminal dock (terminals.js) running
//   the agent's sign-in command in its sandbox, at its cwd, as you — then
//   "Signed in? Retry" (POST /runs/{id}/resume: the adapter starts afresh
//   and reads the credentials);
// - an API key: a password field; the key goes once to
//   POST /runs/{id}/harness/authenticate and is kept nowhere — the field is
//   emptied as it is sent, and nothing draws it;
// - a device code: authenticate answers the page and the code to enter there
//   (a link); the run leaves `login` by itself once that is done.
//
// It warns that the credentials land in the sandbox's shared HOME, and on a
// sandbox others may use asks for a confirm first (sent as confirm: true).
// Someone who may not use the sandbox is told whom to ask; a sandbox that is
// gone (or whose manager is down) is said as such, with Retry and no methods;
// a view-only reader sees what it waits for, and no actions. What it says is
// model/terminals.js signIn.
import { html, nothing } from '/vendor/lit-all.min.js';
import { signIn, methodLabel, isHttps } from './model/terminals.js';
import { findHarness } from './model/harness.js';
import { termDock } from './terminals.js';
import { ext, ctx } from './web-ext.js';

// per park (a run parked on login again starts afresh): {confirm, busy, err,
// msg, device, needConfirm} — never a key
const state = new Map();
const stOf = (c) => {
  const k = `${c.run}:${c.park}`;
  if (!state.has(k)) state.set(k, { confirm: false, busy: '', err: '', msg: '', device: null, needConfirm: false });
  return state.get(k);
};

ext.register({
  end(s) {
    const app = ctx.app;
    const v = app && app.session.current();
    if (!v || !s.run || v.run.id !== s.run.id) return null;
    const c = signIn(v, { list: app.sbx.list, entry: findHarness(app.harness.catalog, (v.run.harness || {}).provider), me: app.me });
    if (!c) return null;
    app.sbx.ensure();
    return cardTpl(app, c);
  },
});

function cardTpl(app, c) {
  const x = stOf(c);
  const redraw = () => ctx.paint();
  const needConfirm = (c.shared || x.needConfirm) && !x.confirm;
  const blocked = needConfirm || !!x.busy;
  const opts = () => (c.shared || x.needConfirm ? { confirm: true } : {});
  // a refusal to confirm (409 {confirm: true}): ask for it, then try again
  const refused = (e) => {
    if (e && e.status === 409 && e.data && e.data.confirm) { x.needConfirm = true; x.confirm = false; }
    x.err = (e && e.message) || String(e);
  };
  const run = async (what, fn) => {
    x.busy = what; x.err = ''; x.msg = '';
    redraw();
    try { await fn(); } catch (e) { refused(e); }
    x.busy = '';
    redraw();
  };
  const term = (m) => {
    const tt = app.sbx.terminal(c.sandbox.ref, c.sandbox.cwd, c.command);
    // while the sandboxes are still being read there is no "why not" yet (not "offers no terminals")
    const why = app.sbx.list.loaded ? tt.why : '';
    return html`<button class="btn btnsm" data-method=${m.id} data-kind="terminal" ?disabled=${blocked || !tt.src}
        title=${why ? `No terminal: ${why}` : `Runs ${c.command} in ${c.sandbox.name}, as you`}
        @click=${() => { termDock(app).open({ ...tt, purpose: 'login', run: c.run, harness: c.name }); x.msg = 'Finish signing in in the terminal, then Retry.'; redraw(); }}>${methodLabel(m)}</button>
      ${why && c.command ? html`<div class="hint" id="hl-noterm">No terminal here (${why}): run <span class="mono">${c.command}</span> in a terminal on ${c.sandbox.name}, then Retry.</div>` : nothing}`;
  };
  // the key is read from the field as it is sent and the field emptied at once
  const sendKey = (m) => (e) => {
    e.preventDefault();
    const input = e.target.querySelector('input');
    const apiKey = input.value;
    input.value = '';
    if (!apiKey) return;
    run('key', async () => {
      await app.harness.authenticate(c.run, m.id, { apiKey, ...opts() });
      x.msg = `Signed in to ${c.name}.`;
    });
  };
  const key = (m) => html`<form data-method=${m.id} data-kind="api-key" @submit=${sendKey(m)}>
      <input type="password" autocomplete="off" spellcheck="false" placeholder=${methodLabel(m)} aria-label=${methodLabel(m)} ?disabled=${blocked}>
      <button class="btn btnsm" ?disabled=${blocked}>${x.busy === 'key' ? 'Signing in…' : 'Sign in'}</button></form>`;
  const device = (m) => html`<button class="btn btnsm" data-method=${m.id} data-kind="device-code" ?disabled=${blocked}
      @click=${() => run('device', async () => { const r = await app.harness.authenticate(c.run, m.id, opts()); x.device = (r && r.device) || null; })}>${x.busy === 'device' ? 'Asking…' : methodLabel(m)}</button>`;
  const METHOD = { terminal: term, 'api-key': key, 'device-code': device };
  const dev = c.device || x.device;
  // a view-only reader: what it waits for, no actions
  if (!c.talk) return html`<div class="ask hlogin" id="hlogin" data-run=${c.run}><b>${c.title}</b><div class="hint" id="hl-view">${c.view}</div></div>`;
  return html`<div class="ask hlogin" id="hlogin" data-run=${c.run}>
    <b>${c.title}</b>
    ${c.gone ? html`<div id="hl-gone">⚠ ${c.goneText}</div>` : html`<div class="hint" id="hl-warn">⚠ ${c.warn}</div>`}
    ${c.gone ? nothing : c.ask ? html`<div id="hl-ask">${c.ask}</div>` : html`
      ${c.shared || x.needConfirm ? html`<label class="chk" id="hl-shared"><input type="checkbox" id="hl-confirm" .checked=${!!x.confirm}
        @change=${(e) => { x.confirm = e.target.checked; redraw(); }}> ${c.confirmLabel}</label>` : nothing}
      <div class="hlm">${c.methods.map((m) => METHOD[m.kind](m))}</div>`}
    ${dev && isHttps(dev.url) ? html`<div class="hldev" id="hl-device">Open <a id="hl-device-link" href=${dev.url} target="_blank" rel="noopener noreferrer">${dev.url}</a>
      ${dev.message ? html`<div class="mono" id="hl-device-msg">${dev.message}</div>` : nothing}
      <div class="hint">This finishes by itself once you are done there.</div></div>` : nothing}
    ${x.err ? html`<div class="err" id="hl-err">${x.err}</div>` : nothing}
    ${x.msg ? html`<div class="muted" id="hl-msg">${x.msg}</div>` : nothing}
    <div class="acts"><button class="btn btnsm" id="hl-retry" ?disabled=${!!x.busy} title="Start it afresh: it reads the new credentials, and your message is sent again"
      @click=${() => run('retry', async () => { await app.harness.retry(c.run); x.msg = 'Retrying…'; })}>Signed in? Retry</button></div>
  </div>`;
}

// A coding agent's card in its parent's chat draws its child's sign-in with
// this (harness-child.js): c = signIn(<the child's view>) — the answers go to the child.
export { cardTpl as signInTpl };
