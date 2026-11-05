// native/mcp-direct.js — MCP servers connected by URL in the native view
// (API.md "Direct MCP servers"; the web's mcp-direct.js): a section of the
// MCP servers screen (native/settings.js mcpTpl) — every server you see,
// yours (only you) and the global ones (everyone), each with its state and
// tools and, where you may, Sign in (the server's authorization page opened
// by the app: xbin.native.open), Sign out, Refresh and Remove (confirmed) —
// and the pushed screen 'mcp-add': its address, a name, for whom (everyone
// only for a manager), and a static token in a secure field for a server
// that takes one instead of a sign-in (never a prop: dropped once sent).
// A sign-in finishes in the browser (mcp-callback.html): the list is read
// again on the screen's refresh and while one waits.
//
// What it says and where each call goes is model/mcp-direct.js.
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ctx, guard, push, ui } from './ui.js';
import { listDirect, addDirect, removeDirect, signinDirect, signoutDirect, refreshDirect, stateOf, scopeWords, SIGNIN_WATCH_MS } from '../model/mcp-direct.js';

const TONE = { ok: nothing, warn: 'warn', bad: 'danger' };
const secrets = new WeakMap(); // the add screen → the token typed (never on the screen's entry)
const st = { list: null, err: '', loading: false, waitUntil: 0, timer: null };

/** loadDirect(force): the list, read once (again when forced). */
export function loadDirect(force = false) {
  if (st.loading || (st.list && !force)) return;
  st.loading = true;
  listDirect().then((l) => { st.list = l; st.err = ''; }, (e) => { st.err = e.message; })
    .finally(() => {
      st.loading = false;
      clearTimeout(st.timer);
      const waiting = (st.list?.servers || []).some((s) => s.state === 'signin');
      if (waiting && Date.now() < st.waitUntil) st.timer = setTimeout(() => loadDirect(true), 3000);
      ctx.paint();
    });
}

function open(url) {
  if (!/^https?:\/\//i.test(String(url || ''))) return;
  Promise.resolve().then(() => globalThis.xbin.native.open(url)).catch((e) => { st.err = e.message; ctx.paint(); });
}

const act = (fn) => guard(async () => {
  st.err = '';
  try { await fn(); } catch (e) { st.err = e.message; }
  loadDirect(true);
});

const signin = (s) => act(async () => {
  const r = await signinDirect(s);
  st.waitUntil = Date.now() + SIGNIN_WATCH_MS;
  open(r.url);
});

/** directSectionTpl(): the servers connected by URL, for the MCP screen. */
export function directSectionTpl() {
  loadDirect();
  const l = st.list;
  const servers = (l && l.servers) || [];
  const canAdd = !!(l && (l.personal || l.canGlobal));
  return html`
    ${st.err || (l && l.errors.length) ? html`<section>${[st.err, ...((l && l.errors) || [])].filter(Boolean).map((e) => html`<notice tone="danger" text=${e}/>`)}</section>` : nothing}
    <section title="Connected by URL" footer="Streamable-HTTP MCP servers added by address — yours alone, or (a manager's) everyone's; a sign-in's tokens stay in the agent's vault. Their tools reach outside the workspace: what a conversation sends one goes to that server.">
      ${!l ? html`<progress label="loading…"/>` : servers.length ? repeat(servers, (s) => s.id, (s) => {
        const sd = stateOf(s);
        const oauth = s.auth === 'oauth';
        return html`<row title=${s.name} subtitle=${[scopeWords(s), sd.text, s.url].join(' · ')} icon="server" tone=${TONE[sd.tone]}
          detail=${(s.tools || []).length ? String(s.tools.length) : nothing}>
          <actions>
            ${s.canEdit && oauth ? html`<button icon="key" @tap=${signin(s)}>${s.signedIn ? 'Sign in again' : 'Sign in'}</button>` : nothing}
            ${s.canEdit && oauth && s.signedIn ? html`<button icon="xmark" @tap=${act(() => signoutDirect(s))}>Sign out</button>` : nothing}
            ${s.state !== 'signin' ? html`<button icon="refresh" @tap=${act(() => refreshDirect(s))}>Refresh</button>` : nothing}
            ${s.canEdit ? html`<button icon="trash" role="destructive" confirm=${{ title: `Remove ${s.name}${s.scope === 'global' ? ' for everyone' : ''}?`, message: 'Its sign-in is forgotten.', label: 'Remove', destructive: true }}
              @tap=${act(() => removeDirect(s))}>Remove</button>` : nothing}
          </actions></row>`;
      }) : html`<empty icon="server" title="None yet"/>`}
      ${canAdd ? html`<button icon="plus" @tap=${() => push({ kind: 'mcp-add' })}>Add an MCP server</button>` : nothing}
    </section>`;
}

function addScreen(s) {
  const l = st.list || {};
  const scopes = [l.personal && { value: 'personal', label: 'only you' }, l.canGlobal && { value: 'global', label: 'everyone' }].filter(Boolean);
  const scope = s.scope || (scopes[0] && scopes[0].value) || 'personal';
  const save = guard(async () => {
    const url = String(s.url || '').trim();
    if (!url) { s.err = 'Its address first.'; return; }
    const body = { url, name: String(s.name || '').trim(), scope };
    const token = String(secrets.get(s) || '').trim();
    if (token) body.token = token;
    secrets.delete(s);
    s.err = '';
    s.busy = true;
    ctx.paint();
    try {
      const srv = await addDirect(body);
      if (srv.state === 'signin' && srv.canEdit) await signin(srv)();
    } catch (e) { s.busy = false; s.err = e.message; return; }
    s.busy = false;
    loadDirect(true);
    ui.stack.pop();
    ctx.paint();
  });
  return html`<screen title="Add an MCP server" style="form">
    ${s.err ? html`<section><notice tone="danger" text=${s.err}/></section>` : nothing}
    <section footer="A streamable-HTTP MCP server. One that wants a sign-in opens its page next; one that takes a static token is given it below.">
      <field kind="text" label="Address" placeholder="https://api.example.com/mcp" value=${s.url || ''} @input=${(e) => { s.url = e.value; }}/>
      <field kind="text" label="Name (optional)" placeholder="from its address" value=${s.name || ''} @input=${(e) => { s.name = e.value; }}/>
      ${scopes.length > 1 ? html`<picker label="For" value=${scope} options=${scopes} @change=${(e) => { s.scope = e.value; ctx.paint(); }}/>` : nothing}
      <field kind="secure" label="Token (optional)" placeholder="only for a server that takes one" value="" @input=${(e) => secrets.set(s, e.value)}/>
    </section>
    <section><button role="primary" ?busy=${!!s.busy} @tap=${save}>Add</button></section>
  </screen>`;
}

ext.register({ screen: (s) => (s.kind === 'mcp-add' ? addScreen(s) : null) });
