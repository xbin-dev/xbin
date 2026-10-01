// native/terminal.js — terminals and a coding agent's sign-in in the native
// view (D147 §2.1, §4.2.6, §4.2.8, §8 U5). The app's terminal
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
//   and the Sign in screen — first, for a coding agent that has one, the
//   guided sign-in (D179: the CLI runs in the sandbox — open its page, Copy
//   link, paste the code, Finish; Remember for my other sandboxes in a
//   person's own partition and sandbox, which mints a saved sign-in the
//   backend keeps), then the terminal login ("Use a terminal instead"
//   beside the guided one), an API key in a secure
//   field (sent once, kept nowhere, never a prop), a device code (a link to
//   open and the code to copy), the shared-HOME warning with a confirm on a
//   sandbox others may use, and "Signed in? Retry" (POST /runs/{id}/resume);
//   a sandbox that is gone (or whose manager is down) says so, with Retry
//   only (and no Sign in in the composer, as for one you may not use); a
//   sign-in this page doesn't offer (a partitioned agent's shared
//   conversation, or its global instance: signIn's away) is the notice
//   alone, saying why.
//
// A terminal screen holds the partition its relay reaches (a person's own,
// or the global instance for a shared conversation's run) running while it
// is up: its socket is a held connection (/docs/partitions.md).
//
// What they say is model/terminals.js (the web draws the same: terminals.js,
// signin.js). One terminal at a time: the app's terminal closes its socket
// when its screen goes and names no session to attach again — the web's
// dock keeps several (model/features.js DIFFERENCES.native).
import { html, nothing } from '/vendor/xb-native.js';
import { ICON } from '../model/sandboxes.js';
import { isHarness, harnessOf, findHarness } from '../model/harness.js';
import { signIn, methodLabel, runTerminalSrc, isHttps } from '../model/terminals.js';
import { newGuided, guidedStarted, guidedFailed, guidedFinished, guidedWords, cleanCode, nameFor, shownMethods } from '../model/harness-signins.js';
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

// the device code authenticate gave you, per park: the summary says only
// who started one (the code is the requester's), so the notice shows yours
const devices = new Map();
const devOf = (c) => c.device || devices.get(`${c.run}:${c.park}`) || null;

ext.register({
  end(v) {
    const x = harnessOfView(v);
    if (!x || !x.c) return null;
    const c = x.c;
    const dev = devOf(c);
    return html`<notice tone="warn" title=${`Sign in to ${c.name}`} text=${`${c.title} ${c.view || c.goneText || c.ask || 'Tap Sign in below.'}`}/>
      ${dev && isHttps(dev.url) ? html`<markdown source=${`Open [${dev.url}](${dev.url}) — ${dev.message}`}
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
    const si = !!(x && x.c && x.c.talk); // a sign-in this page offers (not signIn's away: its notice says why)
    if (!x || !(si || term)) return null;
    return html`${si ? html`<button icon="key" @tap=${() => push({ kind: 'signin', run: v.run.id })}>Sign in…</button>` : nothing}
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
// screen's entry; dropped once sent. The guided sign-in's code likewise.
const keys = new WeakMap();
const codes = new WeakMap();

// signin: the Sign in screen for run s.run (model/terminals.js signIn).
function signInTpl(s) {
  const app = ctx.app;
  const v = app.session.current();
  const x = v && v.run.id === s.run ? harnessOfView(v) : null;
  const c = x && x.c;
  if (!c || !c.talk) {
    const signed = !c && s.g && s.g.phase === 'done'; // the guided sign-in just finished: the park went with it
    return html`<screen title="Sign in" style="form"><section>
      ${c ? html`<empty icon="eye" title=${c.title} text=${c.view}/>`
        : signed ? html`<empty icon="check" title="Signed in" text=${s.g.msg}/>`
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
  // with the guided sign-in above, the terminal is the other way: "Use a terminal instead"
  const term = (m) => html`<section title=${c.guided ? 'Use a terminal instead' : m.name} footer=${why ? `No terminal here (${why}): run ${c.command} in a terminal on ${c.sandbox.name}, then Retry.`
      : `Runs ${c.command} in ${c.sandbox.name}, as you — then Retry.`}>
    <row title=${c.guided ? 'Use a terminal instead' : 'Open a login terminal'} icon="terminal" nav ?disabled=${blocked || !!tt.why}
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
  const dev = devOf(c) || s.device;
  const device = (m) => html`<section title=${m.name} footer=${dev ? 'This finishes by itself once you are done on that page.' : 'You get a page to open and a code to enter there.'}>
    ${dev && isHttps(dev.url) ? html`<row title="Open the sign-in page" subtitle=${dev.url} icon="external" @tap=${() => openLink(dev.url)}/>
      ${dev.message ? html`<text selectable text=${dev.message}/>` : nothing}
      ${codeOf(dev.message) ? html`<button icon="copy" copy=${codeOf(dev.message)}>Copy the code</button>` : nothing}`
    : html`<button ?disabled=${blocked} ?busy=${s.busy === 'device'}
      @tap=${run('device', async () => {
        const r = await app.harness.authenticate(c.run, m.id, opts());
        s.device = (r && r.device) || null;
        if (s.device) devices.set(`${c.run}:${c.park}`, s.device);
      })}>${methodLabel(m)}</button>`}
  </section>`;
  // the guided sign-in (D179, model/harness-signins.js): the CLI runs in the
  // sandbox; you open its page and paste the code it shows. Remember mints a
  // saved sign-in instead — its token stays with the backend.
  const g = s.g || (s.g = newGuided());
  const gw = guidedWords(g, c);
  const gstep = (fn) => async () => {
    s.busy = 'guided'; s.err = ''; s.msg = '';
    ctx.paint();
    try { await fn(); } catch (e) {
      if (e && e.status === 409 && e.data && e.data.confirm) { s.needConfirm = true; s.confirm = false; }
      s.g = guidedFailed(s.g, e);
    }
    s.busy = '';
    ctx.paint();
  };
  const remember = !!(g.remember && c.remember.offered);
  const gstart = gstep(async () => {
    const rem = !!(s.g.remember && c.remember.offered); // as it is now: the name field reports without a repaint
    const name = String(s.g.name || '').trim();
    s.g = { ...s.g, phase: 'starting', err: '', msg: '' };
    ctx.paint();
    const r = await app.harness.guided(c.run, rem ? { remember: true, ...(name ? { name } : {}) } : opts());
    s.g = guidedStarted(s.g, r);
  });
  const gfinish = gstep(async () => {
    const code = cleanCode(codes.get(s));
    codes.delete(s);
    if (!code) { s.g = { ...s.g, err: 'Paste the code the sign-in page shows first.' }; return; }
    s.g = { ...s.g, phase: 'finishing', err: '' };
    ctx.paint();
    const r = await app.harness.guided(c.run, { code });
    s.g = guidedFinished(s.g, r, c.name);
    if (r && r.saved) app.harness.loadSignins().catch(() => {});
  });
  const startBlocked = !!s.busy || (!remember && needConfirm);
  const rememberTpl = () => (c.remember.offered ? html`
      <toggle label="Remember for my other sandboxes" value=${!!g.remember} ?disabled=${!!s.busy}
        @change=${(e) => { s.g = { ...s.g, remember: !!e.value }; ctx.paint(); }}/>
      ${g.remember ? html`<field kind="text" label="Name it" placeholder=${nameFor(app.harness.signins, (x.h || {}).provider) || 'e.g. Work'}
        value=${g.name || ''} ?disabled=${!!s.busy} @input=${(e) => { s.g = { ...s.g, name: e.value }; }}/>` : nothing}` : nothing);
  const guidedTpl = () => {
    const waiting = g.phase === 'waiting' || g.phase === 'finishing';
    const foot = [gw.status, !c.remember.offered && g.phase === 'idle' ? c.remember.why : ''].filter(Boolean).join(' ')
      || (g.remember && c.remember.offered ? 'Claude Code makes a one-year token for you; it is kept in your own space, never in a sandbox, and used for your coding agents in sandboxes of your own.' : 'Opens the sign-in page; you paste back the code it shows.');
    return html`<section title=${`Sign in to ${c.name}`} footer=${foot}>
      ${g.phase === 'idle' || g.phase === 'starting' ? html`${rememberTpl()}
        <button role="primary" icon="key" ?disabled=${startBlocked} ?busy=${g.phase === 'starting'} @tap=${startBlocked ? nothing : gstart}>${gw.start || `Sign in to ${c.name}`}</button>` : nothing}
      ${waiting && isHttps(g.url) ? html`<row title="Open the sign-in page" subtitle=${g.url} icon="external" @tap=${() => openLink(g.url)}/>
        <button icon="copy" copy=${g.url}>Copy link</button>
        ${g.paste ? html`<field kind="text" label="Code" placeholder="the code the sign-in page shows" value="" submit="go" ?disabled=${g.phase === 'finishing'}
          @input=${(e) => codes.set(s, e.value)} @submit=${gfinish}/>
        <button role="primary" ?busy=${g.phase === 'finishing'} @tap=${gfinish}>Finish</button>` : nothing}` : nothing}
      ${g.phase === 'done' ? html`<notice tone="ok" text=${g.msg}/>` : nothing}
    </section>`;
  };
  const METHOD = { terminal: term, 'api-key': key, 'device-code': device };
  return html`<screen title=${`Sign in to ${c.name}`} subtitle=${`${ICON} ${c.sandbox.name}`} style="form">
    <toolbar><button icon="refresh" ?busy=${s.busy === 'retry'} @tap=${retry}>Retry</button></toolbar>
    <section><notice tone="warn" text=${c.goneText || c.warn}/></section>
    ${s.err ? html`<section><notice tone="danger" text=${s.err}/></section>` : nothing}
    ${s.msg ? html`<section><notice tone="ok" text=${s.msg}/></section>` : nothing}
    ${c.gone ? nothing : c.ask ? html`<section><notice tone="info" text=${c.ask}/></section>` : html`
      ${c.shared || s.needConfirm ? html`<section footer="Anyone who may use it signs in as you there.">
        <toggle label=${c.confirmLabel} value=${!!s.confirm} @change=${(e) => { s.confirm = !!e.value; ctx.paint(); }}/></section>` : nothing}
      ${c.guided ? guidedTpl() : nothing}
      ${shownMethods(c).map((m) => METHOD[m.kind](m))}`}
    <section footer="Signed in in a terminal, or somewhere else? Retry starts it afresh: it reads the new credentials, and your message is sent again.">
      <button role="primary" ?busy=${s.busy === 'retry'} @tap=${retry}>Signed in? Retry</button>
    </section>
  </screen>`;
}
