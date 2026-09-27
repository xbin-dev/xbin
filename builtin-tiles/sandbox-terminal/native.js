// native.js — sandbox-terminal for the xbin app (docs/native.md). Same calls
// as index.html (GET /me, /sandboxes, /keys, /sessions; POST and DELETE
// /keys; PUT /settings; the managers' …/execs, read and ended as you) and the
// same words (./sbxterm.js): the sandboxes grouped by manager with a detail
// screen each (its ssh command, the terminals running in it — End ends one),
// and a keys screen (add, remove, the host key, your live SSH sessions; a
// tile manager sets the address people use, and sees and revokes everyone's
// keys).
//
// The one difference from the page (D96; sbxterm.js TERMINAL_GAP): no
// terminal opens here. The app's `terminal` primitive dials only this tile's
// own routes, and a sandbox's terminal is its manager's — relaying it through
// this tile's backend would turn the verified person the manager checks into
// an asserted one. So the view says so where a terminal would be and offers
// the way there: "Open in the browser" (the workspace the app reached;
// xbin.native.open needs the cap:open-links grant, and without it the link is
// copied instead), where this page opens terminals and attaches to the ones
// running.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { api, selfApi, jbody } from '/vendor/bx-kit.js';
import * as T from './sbxterm.js';

const self = xbin.self;
const slot = xbin.iface ? xbin.iface('sandboxes') : null;
const eps = (slot && slot.endpoints) || [];
const wsOrigin = document.querySelector('meta[name="xbin-ws-origin"]')?.getAttribute('content') || '';
const link = T.browserURL(wsOrigin, globalThis.location && location.href);

let me = null, list = null, keys = null, err = '', note = '', busy = '';
let sessions = [], everyone = null;   // the keys screen's: your live SSH sessions; everyone's keys (a tile manager)
let open = null, screen = '';   // open: the detail screen's sandbox key; screen: 'keys' when pushed
const execs = {};
const draft = { key: '', name: '', keyErr: '', addr: null };

async function load() {
  try {
    const [m, l, k] = await Promise.all([selfApi('/me'), selfApi('/sandboxes'), selfApi('/keys')]);
    me = m; list = l; keys = k.keys || []; err = '';
    if (draft.addr === null) draft.addr = (m.ssh && m.ssh.address) || '';
  } catch (e) { err = String(e.message ?? e); }
  paint();
  const boxes = ((list && list.sandboxes) || []).filter((s) => s.tty && s.state === 'running');
  await Promise.all(boxes.map(async (s) => {
    const ep = T.endpointOf(eps, s.provider);
    if (!ep) return;
    try { execs[T.keyOf(s)] = ((await api(T.execsURL(ep, s.id))) || {}).execs || []; } catch { /* keep what was read */ }
  }));
  paint();
}
async function act(fn) {
  err = ''; note = '';
  try { await fn(); } catch (e) { err = String(e.message ?? e); }
  paint();
}
async function openBrowser() {
  if (!link) { note = 'Open this workspace in a browser, then this tile.'; return paint(); }
  const opened = await xbin.native.open(link).then((r) => r !== false, () => false);
  if (!opened) {
    await xbin.native.copy(link).catch(() => false);
    note = `Copied ${link} — paste it into a browser, then open Sandbox terminals there.`;
  } else note = '';
  paint();
}
const endRunning = (row, r) => act(async () => {
  const ep = T.endpointOf(eps, row.provider);
  if (!ep) return;
  try { await api(T.execURL(ep, row.id, r.id), { method: 'DELETE' }); } catch (e) { if (!/no such|error 40[49]/i.test(e.message)) throw e; }
  execs[row.key] = ((await api(T.execsURL(ep, row.id))) || {}).execs || [];
});
async function addKey() {
  draft.keyErr = T.keyCheck(draft.key);
  if (draft.keyErr) return paint();
  busy = 'key'; paint();
  try {
    const k = await selfApi('/keys', jbody({ publicKey: draft.key.trim(), name: draft.name.trim() }, 'POST'));
    draft.key = ''; draft.name = '';
    note = `Added ${k.name || k.type} (${k.fingerprint}).`;
    keys = (await selfApi('/keys')).keys || [];
  } catch (e) { draft.keyErr = String(e.message ?? e); }
  busy = ''; paint();
}
const removeKey = (k) => act(async () => {
  await selfApi(`/keys/${encodeURIComponent(k.id)}`, { method: 'DELETE' });
  keys = (await selfApi('/keys')).keys || [];
  if (everyone) everyone = (await selfApi('/keys?all=1')).keys || [];
});
// openKeys pushes the keys screen, then reads what only it shows: your live
// SSH sessions and — for a manager of the tile — everyone's keys.
const openKeys = () => {
  screen = 'keys';
  paint();
  return act(async () => {
    sessions = (await selfApi('/sessions')).sessions || [];
    if (me && me.manager && !me.viewedBy) everyone = (await selfApi('/keys?all=1')).keys || [];
  });
};
const saveAddr = () => act(async () => {
  await selfApi('/settings', jbody({ sshAddress: (draft.addr || '').trim() }, 'PUT'));
  me = await selfApi('/me');
  note = 'Saved the address.';
});

const rowsOf = () => T.withSSH(T.groups(list, { eps, me, execs }), me && me.ssh);
const rowOf = (key) => rowsOf().flatMap((g) => g.rows).find((r) => r.key === key) || null;
const browserTpl = (why = false) => html`
  <notice tone="info" title="Terminals open in the browser" text=${why ? T.TERMINAL_GAP : 'In a browser this tile opens them, and attaches to the ones running.'}/>
  <button icon="external" @tap=${openBrowser}>Open in the browser</button>
  ${link ? html`<code copy text=${link}/>` : nothing}`;

const main = () => {
  const gs = rowsOf();
  const why = T.emptyWhy(list, self);
  const st = T.sshState(me && me.ssh, { self, manager: !!(me && me.manager) });
  return html`
  <screen title="Sandbox terminals" subtitle=${me ? `as ${me.user}${me.viewedBy ? ' · read only' : ''}` : nothing} style="list" refreshable @refresh=${load}>
    ${err ? html`<section><notice tone="danger" text=${err}/></section>` : nothing}
    ${note ? html`<section><notice tone="ok" text=${note}/></section>` : nothing}
    <section>${browserTpl()}</section>
    ${repeat(gs, (g) => g.provider, (g) => html`
      <section title=${g.title} footer=${g.provider + (g.tty ? '' : ' · no terminals')}>
        ${g.error ? html`<notice tone="warn" text=${g.error}/>` : nothing}
        ${g.rows.length ? repeat(g.rows, (r) => r.key, (r) => html`
          <row title=${r.name} subtitle=${r.facts} detail=${r.stateLabel} tone=${r.tone} icon="box" nav
               badge=${r.running.length ? `${r.running.length} running` : nothing} @tap=${() => { open = r.key; paint(); }}/>`)
          : g.error ? nothing : html`<empty text="nothing shared with this tile here"/>`}
      </section>`)}
    ${why ? html`<section><empty icon="box" title=${why.title} text=${why.text}/>${why.cmd ? html`<code copy text=${why.cmd}/>` : nothing}</section>` : nothing}
    <section title="SSH">
      <notice tone=${st.ready ? 'ok' : st.tone} title=${st.title} text=${st.text}/>
      ${!st.ready && me && me.ssh && me.ssh.listening ? html`<code copy text=${st.expose}/>` : nothing}
      <row title="Your keys" icon="key" nav detail=${keys ? String(keys.length) : '…'} @tap=${openKeys}/>
    </section>
  </screen>`;
};

const detail = (r) => html`
  <screen title=${r.name} subtitle=${`${T.ICON} ${r.stateLabel}`} style="form">
    ${err ? html`<section><notice tone="danger" text=${err}/></section>` : nothing}
    ${note ? html`<section><notice tone="ok" text=${note}/></section>` : nothing}
    <section>
      <row title="State" detail=${r.stateLabel} tone=${r.tone}/>
      <row title="Who" detail=${r.facts}/>
      <row title="Working directory" detail=${r.workdir || '—'} mono="detail"/>
      <row title="Manager" detail=${r.provider} mono="detail"/>
    </section>
    <section title="SSH" footer=${r.ssh ? 'With a key you registered in this tile (Your keys).' : `The SSH login is ${r.login}; the command shows once SSH is published.`}>
      ${r.ssh ? html`<code copy text=${r.ssh}/>` : html`<row title="SSH login" detail=${r.login} mono="detail"/>`}
    </section>
    <section title="Terminals" footer=${r.open.ok ? nothing : `No terminal onto it now: ${r.open.why}.`}>
      ${browserTpl(true)}
      ${repeat(r.running, (x) => x.id, (x) => html`
        <row title=${`terminal ${x.n}`} subtitle=${x.label} icon="terminal">
          <actions><button role="destructive" icon="stop" ?disabled=${!!(me && me.viewedBy)}
            confirm=${{ title: `End terminal ${x.n} in ${r.name}?`, message: 'What runs in it stops.', label: 'End', destructive: true }}
            @tap=${() => endRunning(r, x)}>End</button></actions>
        </row>`)}
    </section>
  </screen>`;

const keysTpl = () => {
  const ro = !!(me && me.viewedBy);
  const hk = me && me.ssh && me.ssh.hostKey;
  const kh = T.knownHosts(me && me.ssh);
  return html`
  <screen title="Your SSH keys" style="form">
    ${err ? html`<section><notice tone="danger" text=${err}/></section>` : nothing}
    ${note ? html`<section><notice tone="ok" text=${note}/></section>` : nothing}
    <section footer="Swipe a key to remove it; its SSH connections end at once.">
      ${keys && keys.length ? repeat(T.keyRows(keys), (k) => k.id, (k) => html`
        <row title=${k.name} subtitle=${k.inactive ? `${k.fingerprint} · ${k.inactive}` : k.fingerprint} mono="subtitle" detail=${k.inactive ? 'inactive' : k.type} icon="key">
          <actions><button role="destructive" icon="trash" ?disabled=${ro}
            confirm=${{ title: `Remove the key “${k.name}”?`, message: 'Its SSH connections end now.', label: 'Remove', destructive: true }}
            @tap=${() => removeKey(k)}>Remove</button></actions>
        </row>`) : html`<empty icon="key" text="No keys yet — add your public key to log in over SSH."/>`}
    </section>
    ${ro ? nothing : html`<section title="Add a key" footer="ed25519, ECDSA, security keys (sk-…) or RSA of 2048 bits or more. A key is one person's.">
      <field label="Public key" kind="multiline" placeholder="ssh-ed25519 AAAAC3Nza… you@laptop" value=${draft.key} error=${draft.keyErr || nothing}
        @input=${(e) => { draft.key = e.value; draft.keyErr = ''; paint(); }}/>
      <field label="Name" placeholder="default: the key's comment" value=${draft.name} @input=${(e) => { draft.name = e.value; paint(); }}/>
      <button role="primary" icon="plus" ?busy=${busy === 'key'} ?disabled=${!draft.key.trim()} @tap=${addKey}>Add key</button>
    </section>`}
    ${hk ? html`<section title="Host key" footer="What ssh shows the first time you connect.">
      <row title=${hk.type} detail=${hk.fingerprint} mono="detail"/>${kh ? html`<code copy text=${kh}/>` : nothing}
    </section>` : nothing}
    ${sessions.length ? html`<section title="Your SSH sessions">${sessions.map((x) => html`
      <row title=${x.name || x.login} subtitle=${`${x.kind} · from ${x.remote} · since ${T.ago(x.started)}`} icon="terminal"/>`)}</section>` : nothing}
    ${me && me.manager && !ro ? html`<section title="Address people use" footer="host or host:port — where the SSH port is published; the ssh commands show it.">
      <field label="Address" placeholder="sbx.example.com:2222" value=${draft.addr ?? ''} @input=${(e) => { draft.addr = e.value; paint(); }}/>
      <button @tap=${saveAddr}>Save</button>
    </section>` : nothing}
    ${me && me.manager && !ro && everyone ? html`<section title="Everyone's keys" footer="You manage this tile: revoking a key ends its SSH connections at once.">
      ${everyone.length ? repeat(T.keyRows(everyone), (k) => k.id, (k) => html`
        <row title=${k.user} subtitle=${`${k.name} · ${k.fingerprint}${k.inactive ? ` · ${k.inactive}` : ''}`} detail=${k.inactive ? 'inactive' : k.lastUsed} icon="person">
          <actions><button role="destructive" icon="trash"
            confirm=${{ title: `Revoke ${k.user}'s key “${k.name}”?`, message: 'Its SSH connections end now.', label: 'Revoke', destructive: true }}
            @tap=${() => removeKey(k)}>Revoke</button></actions>
        </row>`) : html`<empty icon="key" text="nobody has registered a key"/>`}
    </section>` : nothing}
  </screen>`;
};

const paint = () => {
  if (!list && !err) return render(html`<screen title="Sandbox terminals"><progress label="loading…"/></screen>`);
  if (!list) return render(html`<screen title="Sandbox terminals"><notice tone="danger" text=${err}/></screen>`);
  const r = open ? rowOf(open) : null;
  render(html`<nav @pop=${() => { open = null; screen = ''; paint(); }}>
    ${main()}
    ${r ? detail(r) : screen === 'keys' ? keysTpl() : nothing}
  </nav>`);
};
paint();   // at once ("loading…"): the app wants a tree before the backend answers
load();
