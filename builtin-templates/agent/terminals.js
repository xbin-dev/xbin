// terminals.js — the terminal dock on the web (D-harness §2.1, §8 U5): one
// pane docked under the top bar, holding a tab per terminal — a shell in a
// coding sandbox (the ▣ popover's Open terminal, a Sandboxes row's
// Terminal, ＋ another one here, the top bar's >_ Terminal in a coding
// agent's conversation: a shell at its cwd) or a login tab running a coding
// agent's sign-in command (signin.js). Each tab is a <bx-terminal src>
// dialling the sandbox manager's tty route with the page's own credential
// (xbin.ws — the manager sees the verified person). The tabs are the page's,
// not a conversation's: they survive switching conversations. ✕ ends a
// tab's shell (DELETE its exec at the manager); ▾ Hide folds the dock away
// with the shells still running, leaving a pill in the top bar; Escape
// stays with the shell. What the tabs say is model/terminals.js.
import { html, render, nothing, repeat } from '/vendor/lit-all.min.js';
import { termsOf, tabLabel, tabHead } from './model/terminals.js';
import { isHarness, harnessOf } from './model/harness.js';
import { ext, ctx } from './web-ext.js';

let dock = null;

// termDock(app): the page's dock, made the first time something opens a
// terminal or the top bar asks — {open(spec), close(key), terms}.
export function termDock(app) {
  if (dock) return dock;
  const T = termsOf(app);
  const el = document.getElementById('sbxterm') || document.body.appendChild(Object.assign(document.createElement('div'), { id: 'sbxterm' }));
  let loading = null;
  const draw = () => render(tpl(), el);
  // the dock draws itself; the top bar carries its pill
  app.on('terms', () => { draw(); ctx.paint(); });

  // open shows a new tab for spec (app.sbx.terminal()'s answer; a login tab
  // adds purpose 'login', run and harness) — once the element has loaded.
  async function open(spec) {
    if (!spec || !spec.src) return null;
    try { await (loading || (loading = import('/vendor/bx-terminal.js'))); } catch (e) {
      loading = null;
      alert(`The terminal could not load: ${e.message}`);
      return null;
    }
    return T.open(spec);
  }
  // close takes a tab away and ends its shell at the manager (a terminal a
  // page leaves runs on until the manager ends it).
  function close(key) {
    const t = T.close(key);
    if (t && t.session && !t.ended) app.sbx.endTerminal(t, t.session).catch(() => {});
    return t;
  }
  // again: a New shell where the tab's ended (the login shell, even in a login tab).
  function again(t) {
    const n = app.sbx.terminal(t.ref, t.cwd);
    if (n.src) T.again(t.key, { ...n, purpose: 'shell' });
  }
  // another: ＋ — one more shell in the shown tab's sandbox, at its cwd.
  function another(t) {
    const n = app.sbx.terminal(t.ref, t.cwd);
    if (n.src) open(n);
  }
  // retry: "Signed in? Retry ‹name›" — POST /runs/{id}/resume (the adapter
  // starts afresh and reads the new credentials), then the login tab goes.
  async function retry(t) {
    t.err = '';
    try { await app.harness.retry(t.run); close(t.key); } catch (e) { t.err = e.message; draw(); }
  }

  function tpl() {
    const t = T.current;
    if (!t) return nothing;
    const h = tabHead(t);
    // docked just under the top bar (it wraps on a narrow window), clear of
    // the composer. Escape belongs to the program in the terminal (vim,
    // less): the page's own Escape handling (a popover, the preview) must
    // not see it — so a fixed pane, not a modal <dialog> (which closes on it).
    // Hidden, it stays in the page (display:none): its shells stay connected.
    const top = Math.round((document.getElementById('top')?.getBoundingClientRect().bottom || 40) + 6);
    const style = T.hidden ? 'display:none' : T.max ? '' : `top:${top}px`;
    return html`<div class="sbxterm ${T.max ? 'max' : ''}" id="sbxterm-pane" role="dialog" aria-label=${`Terminal in ${t.name}`} style=${style}
        @keydown=${(e) => { if (e.key === 'Escape') e.stopPropagation(); }}>
      ${T.tabs.length > 1 ? html`<div class="sbxttabs" role="tablist">${repeat(T.tabs, (x) => x.key, (x) => html`<span role="tab"
          class="sbxtab ${x.key === T.active ? 'on' : ''} ${x.ended ? 'ended' : ''}" data-tab=${x.key} aria-selected=${x.key === T.active ? 'true' : 'false'}
          title=${`${tabLabel(x)} — ${x.cwd || 'its workdir'}${x.ended ? ' (ended)' : ''}`} @click=${() => T.select(x.key)}><span class="lb">${tabLabel(x)}</span><button
          class="x" data-close=${x.key} title="Close — ends the shell" @click=${(e) => { e.stopPropagation(); close(x.key); }}>✕</button></span>`)}</div>` : nothing}
      <div class="sbxthd"><b>${h.title}</b><span class="mono muted" title="the working directory">${h.where}</span>
        <span class="muted">${h.manager}</span>
        ${t.ended ? html`<span class="badge" id="sbxterm-ended">${t.ended}</span>` : nothing}
        <span style="flex:1"></span>
        ${h.retry ? html`${h.done ? html`<span class="muted" id="sbxterm-done">${h.done}</span>` : nothing}<button class="btn btnsm" id="sbxterm-retry"
          title="Start it afresh: it reads the new credentials, and your message is sent again" @click=${() => retry(t)}>${h.done ? h.retry : `Signed in? ${h.retry}`}</button>` : nothing}
        ${t.ended ? html`<button class="btn ghost btnsm" id="sbxterm-again" title="Start another shell here" @click=${() => again(t)}>New shell</button>` : nothing}
        <button class="btn ghost btnsm" id="sbxterm-new" title=${`Another shell in ${t.name}`} @click=${() => another(t)}>＋</button>
        <button class="btn ghost btnsm" id="sbxterm-max" title=${T.max ? 'Smaller' : 'Larger'} @click=${() => T.toggleMax()}>${T.max ? '⤡' : '⤢'}</button>
        <button class="btn ghost btnsm" id="sbxterm-hide" title="Hide — the shells keep running" @click=${() => T.hide()}>▾</button>
        <button class="btn ghost btnsm" id="sbxterm-close" title="Close — ends the shell" @click=${() => close(t.key)}>✕</button></div>
      ${h.hint ? html`<div class="hint sbxthint">${h.hint}</div>` : nothing}
      ${t.err ? html`<div class="err sbxthint" id="sbxterm-err">${t.err}</div>` : nothing}
      <div class="sbxtbody">${repeat(T.tabs, (x) => `${x.key}.${x.gen}`, (x) => html`<bx-terminal src=${x.src} data-tab=${x.key}
        style=${x.key === T.active ? 'flex:1 1 0; min-width:0; height:auto' : 'display:none'}
        @bx-session=${(e) => T.session(x.key, e.detail.id)}
        @bx-exit=${() => T.ended(x.key)}></bx-terminal>`)}</div>
    </div>`;
  }

  dock = { open, close, terms: T };
  return dock;
}

// The top bar: the pill of a hidden dock, and in a coding agent's
// conversation >_ Terminal — a shell in its sandbox at its cwd.
ext.register({
  top(v) {
    const app = ctx.app;
    if (!app) return null;
    const d = termDock(app);
    const pill = d.terms.pill();
    const h = v && isHarness(v.run) ? harnessOf(v) : null;
    const sb = h && h.sandbox && h.sandbox.ref ? h.sandbox : null;
    const tt = sb ? app.sbx.terminal(sb.ref, sb.cwd) : null;
    if (tt) app.sbx.ensure();
    if (!pill && !(tt && tt.shown)) return null;
    return html`${pill ? html`<span class="badge termpill" id="sbxterm-pill" role="button" tabindex="0" title="Show the terminals — their shells kept running"
        @click=${() => d.terms.show()}>&gt;_ ${pill}</span>` : nothing}${tt && tt.shown ? html`<button class="btn ghost btnsm" id="hterm" ?disabled=${!!tt.why}
        title=${tt.why ? `No terminal: ${tt.why}` : `A shell in ${tt.name} at ${tt.cwd || 'its workdir'}, as you`}
        @click=${() => d.open(tt)}>&gt;_ Terminal</button>` : nothing}`;
  },
});
