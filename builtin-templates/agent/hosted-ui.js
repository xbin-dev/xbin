// hosted-ui.js — the web view's part of non-secure (hosted) conversations
// (model/hosted.js; API.md "Non-secure conversations"). In a person's
// partition of a partitioned agent:
//   - a hosted conversation carries a ⚠ "not private" chip — on its header
//     and on its row in the lists;
//   - opening one into this page session shows the warning (who can read it,
//     whose private resources it uses) with "Start anyway" / "Open without
//     sending" — no "don't show again": a reload is a new page session — and
//     the composer stays locked until it is started;
//   - while its audience is wider than its host confirmed, the composer is
//     locked for everyone ("waiting for <host> to confirm"), and the host is
//     asked, above the composer, to confirm or decline the new people;
//   - once hosting ended (declined, taken back, 7 days unanswered, the host
//     gone), any participant may continue it without the host's resources;
//   - a shared conversation's share dialog offers "Use my private
//     resources…" (the same warning, then hosting) and "Add a copy of my
//     files…" (copies of your own session files; the originals stay yours).
// An unpartitioned instance's page, and the global instance's own, get
// nothing here: no element, no call.
import { html, render, nothing } from '/vendor/lit-all.min.js';
import { jbody } from '/vendor/bx-kit.js';
import { homeApi } from './model/home-api.js';
import { twoHomes, PARTITION_BASE } from './model/homes.js';
import { hostingOf, lockOf, readers, exposed, hostedId, audienceOf } from './model/hosted.js';

const started = new Set(); // hosted conversations started in this page session ("Start anyway")
const warned = new Set();  // …whose warning opened by itself in this page session
let app = null;
let bar = null;
let dlg = null;
let st = null; // the dialog: {mode: 'start'|'host'|'copy', …}
let locked = false;

const CSS = `
#hostbar { flex: none; display: flex; gap: 8px; align-items: center; flex-wrap: wrap; padding: 6px 12px; font-size: 12px;
  border-top: 1px solid var(--bx-border); background: color-mix(in srgb, var(--bx-yellow, #d9a441) 12%, transparent); }
#hostbar[hidden] { display: none; }
.badge.notprivate, .chip.notprivate { color: var(--bx-yellow, #d9a441); border-color: color-mix(in srgb, var(--bx-yellow, #d9a441) 60%, var(--bx-border)); }
.badge.notprivate { cursor: pointer; }
#hostdlg .warnhd { color: var(--bx-yellow, #d9a441); }
#hostdlg ul { margin: 4px 0 0 18px; padding: 0; }
#hostdlg .files { max-height: 180px; overflow: auto; }`;

function mount() {
  if (bar) return;
  const style = Object.assign(document.createElement('style'), { textContent: CSS });
  document.head.append(style);
  bar = Object.assign(document.createElement('div'), { id: 'hostbar', hidden: true });
  document.querySelector('.composer')?.before(bar);
  // a locked composer sends nothing, attachments included
  document.getElementById('send')?.addEventListener('click', (e) => {
    if (locked) { e.preventDefault(); e.stopImmediatePropagation(); }
  }, true);
}

function dialog() {
  if (!dlg) {
    dlg = Object.assign(document.createElement('dialog'), { id: 'hostdlg' });
    document.body.appendChild(dlg);
  }
  return dlg;
}

const me = () => (app && app.me && app.me.user) || '';
const open = (id) => { location.hash = 'c=' + id; };

/** hostedChipTpl: the ⚠ chip on an open hosted conversation's header (its warning, again, on a click). */
export function hostedChipTpl(v) {
  const h = hostingOf(v);
  if (!h) return nothing;
  return html`<span class="badge notprivate" id="hosted-chip" title=${`not private: it uses ${exposed(h)} — click for who can read it`}
    @click=${() => openWarning('start', v)}>⚠ not private</span>`;
}

/** hostedRowChip: the ⚠ chip on a hosted conversation's row in a list. */
export function hostedRowChip(r) {
  if (!r || !r.hosted) return nothing;
  return html`<span class="chip notprivate" title=${`not private: it uses ${exposed(r.hosted)}`}>⚠ not private</span>`;
}

/**
 * hostedPaint (agent.js paint): the open conversation's lock, the strip
 * above the composer, and the warning when a hosted conversation is opened
 * into this page session.
 */
export function hostedPaint(v, a) {
  if (!v && !bar) return;
  app = a;
  mount();
  const lk = lockOf(v, me(), started);
  locked = !!(lk && lk.locked);
  const msg = document.getElementById('msg');
  const send = document.getElementById('send');
  if (locked) {
    msg.disabled = true;
    msg.placeholder = lk.why;
  }
  if (send) send.disabled = locked || !!(a && a.sending);
  bar.hidden = !locked;
  render(locked ? barTpl(v, lk) : nothing, bar);
  if (lk && lk.kind === 'start' && !warned.has(lk.root)) {
    warned.add(lk.root);
    openWarning('start', v);
  }
}

function barTpl(v, lk) {
  const h = hostingOf(v);
  const root = lk.root;
  const act = (fn) => async () => {
    try { await fn(); } catch (e) { alert(e.message); }
  };
  switch (lk.kind) {
    case 'start':
      return html`<span>⚠ ${lk.why}.</span>
        <button class="btn btnsm" id="host-start" @click=${() => openWarning('start', v)}>Read the warning and start…</button>`;
    case 'paused':
      if (lk.isHost) {
        return html`<span id="host-ask">New in this conversation: <b>${lk.pending.join(', ') || 'a wider audience'}</b>.
          Let the agent keep using your private resources with them here?</span>
          <button class="btn btnsm" id="host-confirm" ?disabled=${!h.pendingKey} @click=${act(async () => {
            if (h.pendingKey) await homeApi('', `/hosting/${root}/confirm`, jbody({ seen: h.pendingKey }, 'POST'));
          })}>Confirm</button>
          <button class="btn ghost btnsm" id="host-decline" @click=${act(async () => {
            await homeApi('', `/hosting/${root}/decline`, { method: 'POST' });
          })}>Decline</button>`;
      }
      return html`<span id="host-wait">${lk.why}.${h.dropsAt ? ` Unanswered, it stops using them on ${new Date(h.dropsAt * 1000).toLocaleDateString()}.` : ''}</span>`;
    case 'moving':
      return html`<span id="host-moving">${lk.why}.</span>`;
    case 'ended':
      return html`<span>${lk.why}.</span>${v.access === 'viewer' ? nothing
        : html`<button class="btn btnsm" id="host-continue" @click=${act(async () => {
          const r = await homeApi('global', `/hosted/${root}/continue`, { method: 'POST' });
          app?.convs?.load?.();
          if (r && r.conversation) open(r.conversation);
        })}>Continue without ${h.host}'s private resources</button>`}`;
  }
  return nothing;
}

// --- the warning ------------------------------------------------------------------

/**
 * openWarning shows the non-secure warning: mode 'start' for an open hosted
 * conversation (Start anyway / Open without sending), 'host' before a person
 * lets a shared one use their private resources (then hosting starts).
 */
function openWarning(mode, v, extra = {}) {
  st = { mode, v, busy: false, err: '', ...extra };
  paintDlg();
  if (!dialog().open) dialog().showModal();
}

function paintDlg() {
  if (st) render(st.mode === 'copy' ? copyTpl() : warnTpl(), dialog());
}

function warnTpl() {
  const v = st.v;
  const h = st.mode === 'host' ? { host: me(), resources: ['sandboxes', 'tiles', 'vault'] } : hostingOf(v);
  const acl = st.mode === 'host' ? st.acl : v.acl;
  const title = st.mode === 'host' ? (st.title || 'conversation') : (v.run.title || 'conversation');
  const close = () => dialog().close();
  const start = () => { started.add(v.run.rootId || v.run.id); close(); app?.session?.changed?.(); document.getElementById('msg')?.focus(); };
  return html`<form method="dialog" @submit=${(e) => e.preventDefault()}>
    <div class="dlg-hd warnhd">⚠ “${title}” is not private</div>
    <div class="dlg-bd">
      <div id="host-exposed">${st.mode === 'host'
        ? html`Letting the agent use <b>your</b> private sandboxes, your data in other tiles and your vault here makes this conversation
          non-secure: the agent acts with them in it, and what it reads or writes with them goes into a transcript all of the people below can read.`
        : html`This conversation uses <b>${exposed(h)}</b>: the agent acts with them here, and what it reads or writes with them goes into
          a transcript all of the people below can read.`}</div>
      <div class="field"><label>Who can read it</label>
        <ul id="host-readers">${readers(acl).map((r) => html`<li>${r}</li>`)}</ul></div>
      ${st.mode === 'host' ? html`<div class="muted small">It has no join links. Adding people later pauses it until you confirm them;
        you can take your resources back at any time (then it continues without them).</div>` : nothing}
      ${st.err ? html`<div class="err">${st.err}</div>` : nothing}
    </div>
    <div class="dlg-ft">${st.mode === 'host'
      ? html`<button class="btn ghost" @click=${close}>Cancel</button>
        <button class="btn" id="host-go" ?disabled=${st.busy} @click=${startHosting}>${st.busy ? 'Moving it…' : 'Use my private resources'}</button>`
      : html`<button class="btn ghost" id="host-nosend" @click=${close}>Open without sending</button>
        <button class="btn" id="host-anyway" @click=${start}>Start anyway</button>`}</div>
  </form>`;
}

async function startHosting() {
  st.busy = true; st.err = ''; paintDlg();
  try {
    const r = await homeApi('', '/hosting', jbody({ conversation: st.runId, seen: audienceOf(st.acl) }, 'POST'));
    started.add(r.conversation); // the person just read the warning: it starts here
    warned.add(r.conversation);
    dialog().close();
    st.onDone?.();
    app?.convs?.load?.();
    open(r.conversation);
  } catch (e) {
    st.err = e.message;
  } finally {
    st.busy = false;
    paintDlg();
  }
}

// --- the share dialog's part ------------------------------------------------------------

/**
 * hostTpl: in a person's partition, a shared conversation's share dialog (d:
 * GET /runs/{id}/members) offers to let it use their private resources and
 * to add copies of their files; a hosted one's host may take them back.
 * Nothing elsewhere.
 */
export function hostTpl(runId, d, who, title, close, onChange) {
  const id = Number(runId);
  if (!twoHomes() || !d || !(id > 0) || id >= PARTITION_BASE) return nothing;
  if (hostedId(id)) {
    const h = d.hosted || {};
    if (!who || h.host !== who.user || (h.state !== 'active' && h.state !== 'paused')) return nothing;
    return html`<div class="field"><label>Your private resources</label>
      <div class="prow"><span class="muted">This conversation uses your private resources. Take them back: it pauses until someone
        continues it without them.</span>
      <button class="btn rm btnsm" id="host-stop" @click=${async () => {
        try { await homeApi('', `/hosting/${id}`, { method: 'DELETE' }); close(); onChange?.(); } catch (e) { alert(e.message); }
      }}>Take them back</button></div></div>`;
  }
  return html`<div class="field"><label>Not private: your own resources</label>
    <div class="prow"><span class="muted">Let the agent use your sandboxes, your data in other tiles and your vault in this conversation.
      It becomes non-secure — you are warned first.</span>
    <button class="btn ghost btnsm" id="host-use" @click=${() => {
      close();
      openWarning('host', null, { runId: id, acl: d, title, onDone: onChange });
    }}>Use my private resources…</button></div>
    <div class="prow"><span class="muted">Or add copies of your own session files; your originals stay private.</span>
    <button class="btn ghost btnsm" id="copy-mine" @click=${() => { close(); openCopyIn(id, d, onChange); }}>Add a copy of my files…</button></div></div>`;
}

// --- add a copy of my … -----------------------------------------------------------------

function openCopyIn(runId, acl, onChange) {
  openWarning('copy', null, { runId, acl, onDone: onChange, convs: null, pick: 0, files: null, chosen: new Set() });
  homeApi('', '/conversations?scope=mine&limit=50').then((r) => {
    st.convs = [...(r.pinned || []), ...(r.items || [])].filter((c) => c.id >= PARTITION_BASE);
    paintDlg();
  }).catch((e) => { st.err = e.message; paintDlg(); });
}

async function pickConv(id) {
  st.pick = +id; st.files = null; st.chosen = new Set(); paintDlg();
  try {
    st.files = ((await homeApi('', `/runs/${id}/files`)) || []);
  } catch (e) {
    st.err = e.message;
  }
  paintDlg();
}

async function copyIn() {
  if (!st.chosen.size) { st.err = 'Choose the files to copy.'; paintDlg(); return; }
  st.busy = true; st.err = ''; paintDlg();
  try {
    const files = [...st.chosen].map((path) => ({ run: st.pick, path }));
    const r = await homeApi('', '/copyin', jbody({ conversation: st.runId, files }, 'POST'));
    dialog().close();
    globalThis.xbin?.notify?.('info', `Copied into the conversation: ${(r.files || []).join(', ')}`);
    st.onDone?.();
  } catch (e) {
    st.err = e.message;
  } finally {
    st.busy = false;
    paintDlg();
  }
}

function copyTpl() {
  return html`<form method="dialog" @submit=${(e) => e.preventDefault()}>
    <div class="dlg-hd">Add a copy of my files</div>
    <div class="dlg-bd">
      <div class="field"><label>From your conversation</label>
        ${!st.convs ? html`<div class="muted">loading…</div>` : html`<select id="copy-conv" @change=${(e) => pickConv(e.target.value)}>
          <option value="">choose one of your own conversations…</option>
          ${st.convs.map((c) => html`<option value=${c.id} ?selected=${c.id === st.pick}>${c.title || 'conversation ' + c.id}</option>`)}</select>`}</div>
      ${st.pick ? html`<div class="field files"><label>Its session files</label>
        ${!st.files ? html`<div class="muted">loading…</div>` : !st.files.length ? html`<div class="muted">none</div>`
          : st.files.map((f) => html`<label class="chk"><input type="checkbox" data-path=${f.path} .checked=${st.chosen.has(f.path)}
            @change=${(e) => { if (e.target.checked) st.chosen.add(f.path); else st.chosen.delete(f.path); }}> <span class="mono">${f.path}</span></label>`)}</div>` : nothing}
      <div class="field"><label>Who can read the copies</label><ul id="copy-readers">${readers(st.acl).map((r) => html`<li>${r}</li>`)}</ul>
        <div class="muted small">They go into this shared conversation as its session files. Your originals stay private, where they are.</div></div>
      ${st.err ? html`<div class="err">${st.err}</div>` : nothing}
    </div>
    <div class="dlg-ft"><button class="btn ghost" @click=${() => dialog().close()}>Cancel</button>
      <button class="btn" id="copy-go" ?disabled=${st.busy} @click=${copyIn}>${st.busy ? 'Copying…' : 'Add the copies'}</button></div>
  </form>`;
}
