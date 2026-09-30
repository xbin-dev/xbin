// native/terminal.js — terminals and a coding agent's sign-in in the native
// view (D-harness §2.1, §4.2.6, §4.2.8, §8 U5). The app's terminal
// primitive dials only the tile's own routes, so every terminal here goes
// through the tile's relays — the backend checks you may use the sandbox,
// then dials its manager's tty as you:
//
// - a pushed Terminal screen (<terminal src>): a shell in a sandbox (a
//   Sandboxes row's Terminal, the ▣ Sandbox screen's Open terminal:
//   GET /sandboxes/{ref}/terminal), a shell at a coding agent's cwd (⋯ →
//   Terminal: GET /runs/{id}/harness/terminal), or its sign-in command
//   (…?login=1) with Retry in the toolbar;
// - a coding agent's sign-in, only while its run is parked on `login` (the
//   `end` seam's rule): a warn notice in the transcript (and the device
//   code's page, once asked), a Sign in button in the composer and in ⋯,
//   and the Sign in screen — the terminal login, an API key in a secure
//   field (sent once, kept nowhere, never a prop), a device code (a link to
//   open and the code to copy), the shared-HOME warning with a confirm on a
//   sandbox others may use, and "Signed in? Retry" (POST /runs/{id}/resume);
//   a sandbox that is gone (or whose manager is down) says so, with Retry
//   only (and no Sign in in the composer, as for one you may not use).
//
// What they say is model/terminals.js (the web draws the same: terminals.js,
// signin.js). One terminal at a time: the app's terminal closes its socket
// when its screen goes and names no session to attach again — the web's
// dock keeps several (model/features.js DIFFERENCES.native).
import { html, nothing } from '/vendor/xb-native.js';
import { ICON } from '../model/sandboxes.js';
import { isHarness, harnessOf, findHarness } from '../model/harness.js';
import { signIn, methodLabel, runTerminalSrc, isHttps } from '../model/terminals.js';
import { access } from '../model/rules.js';
import { ext } from './ext.js';
import { ctx, fail, push, ui } from './ui.js';

// openSandboxTerminal pushes a shell in sandbox ref at cwd (app.sbx.terminal
// through the relay); says why when there is none.
export function openSandboxTerminal(ref, cwd = '') {
  const t = ctx.app.sbx.terminal(ref, cwd);
  if (!t.src) return fail(t.why ? `No terminal: ${t.why}` : 'No terminal here');
  push({ kind: 'term', src: t.src, title: `Terminal · ${t.name}`, subtitle: t.cwd || 'its workdir' });
}

// what a view's run is, for a harness conversation: its sign-in card (or
// null) and its shell (a terminal() check on its sandbox: {shown, why})
function harnessOfView(v) {
  const app = ctx.app;
  if (!v || !isHarness(v.run)) return null;
  const h = harnessOf(v) || {};
  const c = signIn(v, { list: app.sbx.list, entry: findHarness(app.harness.catalog, h.provider), me: app.me });
  const sb = h.sandbox && h.sandbox.ref ? h.sandbox : null;
  const tt = sb ? app.sbx.terminal(sb.ref, sb.cwd) : null;
  if (c || tt) app.sbx.ensure(c && c.sandbox.ref);
  return { h, c, sb, tt };
}

// a harness run's shell at its cwd, through the run's relay
function openRunTerminal(v, x) {
  push({ kind: 'term', src: runTerminalSrc(v.run.id), title: `Terminal · ${x.sb.name || x.tt.name}`, subtitle: x.sb.cwd || 'its workdir' });
}

// the open-links grant opens an https: page outside the app
const openLink = (url) => {
  if (!isHttps(url)) return;
  Promise.resolve().then(() => globalThis.xbin.native.open(url)).catch((e) => fail(e));
};

// the device code's code, when its message has one (ABCD-1234)
const codeOf = (msg) => (String(msg || '').match(/\b[A-Z0-9]{4,}(?:-[A-Z0-9]{4,})+\b/) || [''])[0];

ext.register({
  end(v) {
    const x = harnessOfView(v);
    if (!x || !x.c) return null;
    const c = x.c;
    return html`<notice tone="warn" title=${`Sign in to ${c.name}`} text=${`${c.title} ${c.view || c.goneText || c.ask || 'Tap Sign in below.'}`}/>
      ${c.device && isHttps(c.device.url) ? html`<markdown source=${`Open [${c.device.url}](${c.device.url}) — ${c.device.message}`}
        @link=${(e) => openLink(e.href)}/>` : nothing}`;
  },
  composer(v) {
    const x = harnessOfView(v);
    if (!x || !x.c || x.c.ask || x.c.gone || !x.c.talk) return null;
    return { tpl: () => html`<button icon="key" role="primary" @tap=${() => push({ kind: 'signin', run: v.run.id })}>Sign in</button>` };
  },
  menu(v) {
    // a view-only reader gets neither (the run's relay is a participant's, §4.2.8)
    const x = access(v).talk ? harnessOfView(v) : null;
    const term = !!(x && x.tt && x.tt.shown && !x.tt.why);
    if (!x || !(x.c || term)) return null;
    return html`${x.c ? html`<button icon="key" @tap=${() => push({ kind: 'signin', run: v.run.id })}>Sign in…</button>` : nothing}
      ${term ? html`<button icon="terminal" @tap=${() => openRunTerminal(v, x)}>Terminal</button>` : nothing}`;
  },
  screen(s) {
    if (s.kind === 'term') return termTpl(s);
    if (s.kind === 'signin') return signInTpl(s);
    return null;
  },
});

// --- the screens -------------------------------------------------------------------

// term: the app's terminal on one of the tile's relays; a login screen's
// toolbar retries the coding agent (and leaves, with the Sign in screen it
// was opened from: the chat shows how it went).
function termTpl(s) {
  const retry = async () => {
    try { await ctx.app.harness.retry(s.run); } catch (e) { fail(e); return; }
    const i = ui.stack.indexOf(s);
    const from = ui.stack.findIndex((x) => x.kind === 'signin' && x.run === s.run);
    if (i >= 0) ui.stack.splice(from >= 0 && from < i ? from : i);
    ctx.paint();
  };
  return html`<screen title=${s.title} subtitle=${s.subtitle || nothing} style="scroll">
    ${s.login && s.run ? html`<toolbar><button icon="refresh" role="primary" @tap=${retry}>${`Signed in? Retry ${s.harness}`}</button></toolbar>` : nothing}
    <terminal src=${s.src} title=${s.title}/>
  </screen>`;
}

// the API key being typed, per Sign in screen — never a prop, never on the
// screen's entry; dropped once sent
const keys = new WeakMap();

// signin: the Sign in screen for run s.run (model/terminals.js signIn).
function signInTpl(s) {
  const app = ctx.app;
  const v = app.session.current();
  const x = v && v.run.id === s.run ? harnessOfView(v) : null;
  const c = x && x.c;
  if (!c || !c.talk) {
    return html`<screen title="Sign in" style="form"><section>
      ${c ? html`<empty icon="eye" title=${c.title} text=${c.view}/>`
        : html`<empty icon="check" title="Nothing to sign in" text="The coding agent isn't waiting for a sign-in."/>`}
    </section></screen>`;
  }
  const needConfirm = (c.shared || s.needConfirm) && !s.confirm;
  const blocked = needConfirm || !!s.busy;
  const opts = () => (c.shared || s.needConfirm ? { confirm: true } : {});
  const run = (what, fn) => async () => {
    s.busy = what; s.err = ''; s.msg = '';
    ctx.paint();
    try { await fn(); } catch (e) {
      if (e && e.status === 409 && e.data && e.data.confirm) { s.needConfirm = true; s.confirm = false; }
      s.err = e.message;
    }
    s.busy = '';
    ctx.paint();
  };
  // Retry starts it afresh and, like the login terminal's, leaves: the chat shows how it went
  const retry = run('retry', async () => {
    await app.harness.retry(c.run);
    const i = ui.stack.indexOf(s);
    if (i >= 0) ui.stack.splice(i);
  });
  const tt = app.sbx.terminal(c.sandbox.ref, c.sandbox.cwd);
  const why = app.sbx.list.loaded ? tt.why : ''; // no "why not" while the sandboxes are still being read
  const term = (m) => html`<section title=${m.name} footer=${why ? `No terminal here (${why}): run ${c.command} in a terminal on ${c.sandbox.name}, then Retry.`
      : `Runs ${c.command} in ${c.sandbox.name}, as you — then Retry.`}>
    <row title="Open a login terminal" icon="terminal" nav ?disabled=${blocked || !!tt.why}
      @tap=${blocked || tt.why ? nothing : () => push({ kind: 'term', src: runTerminalSrc(c.run, { login: true }), login: true, run: c.run, harness: c.name,
        title: `Sign in · ${c.name}`, subtitle: `${ICON} ${c.sandbox.name}` })}/>
  </section>`;
  const sendKey = (m) => run('key', async () => {
    const apiKey = keys.get(s) || '';
    keys.delete(s);
    if (!apiKey) throw new Error(`Paste the ${m.name} first.`);
    await app.harness.authenticate(c.run, m.id, { apiKey, ...opts() });
    s.msg = `Signed in to ${c.name}.`;
  });
  const key = (m) => html`<section title=${m.name} footer="Sent once to the agent in the sandbox — kept nowhere.">
    <field kind="secure" label=${m.name} placeholder="paste it here" value="" ?disabled=${blocked} submit="go"
      @input=${(e) => keys.set(s, e.value)} @submit=${sendKey(m)}/>
    <button role="primary" ?disabled=${blocked} ?busy=${s.busy === 'key'} @tap=${sendKey(m)}>Sign in</button>
  </section>`;
  const dev = c.device || s.device;
  const device = (m) => html`<section title=${m.name} footer=${dev ? 'This finishes by itself once you are done on that page.' : 'You get a page to open and a code to enter there.'}>
    ${dev && isHttps(dev.url) ? html`<row title="Open the sign-in page" subtitle=${dev.url} icon="external" @tap=${() => openLink(dev.url)}/>
      ${dev.message ? html`<text selectable text=${dev.message}/>` : nothing}
      ${codeOf(dev.message) ? html`<button icon="copy" copy=${codeOf(dev.message)}>Copy the code</button>` : nothing}`
    : html`<button ?disabled=${blocked} ?busy=${s.busy === 'device'}
      @tap=${run('device', async () => { const r = await app.harness.authenticate(c.run, m.id, opts()); s.device = (r && r.device) || null; })}>${methodLabel(m)}</button>`}
  </section>`;
  const METHOD = { terminal: term, 'api-key': key, 'device-code': device };
  return html`<screen title=${`Sign in to ${c.name}`} subtitle=${`${ICON} ${c.sandbox.name}`} style="form">
    <toolbar><button icon="refresh" ?busy=${s.busy === 'retry'} @tap=${retry}>Retry</button></toolbar>
    <section><notice tone="warn" text=${c.goneText || c.warn}/></section>
    ${s.err ? html`<section><notice tone="danger" text=${s.err}/></section>` : nothing}
    ${s.msg ? html`<section><notice tone="ok" text=${s.msg}/></section>` : nothing}
    ${c.gone ? nothing : c.ask ? html`<section><notice tone="info" text=${c.ask}/></section>` : html`
      ${c.shared || s.needConfirm ? html`<section footer="Anyone who may use it signs in as you there.">
        <toggle label=${c.confirmLabel} value=${!!s.confirm} @change=${(e) => { s.confirm = !!e.value; ctx.paint(); }}/></section>` : nothing}
      ${c.methods.map((m) => METHOD[m.kind](m))}`}
    <section footer="Signed in in a terminal, or somewhere else? Retry starts it afresh: it reads the new credentials, and your message is sent again.">
      <button role="primary" ?busy=${s.busy === 'retry'} @tap=${retry}>Signed in? Retry</button>
    </section>
  </screen>`;
}
