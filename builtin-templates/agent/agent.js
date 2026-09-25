// agent.js — the control tile. Your conversations (conv-list.js / sidebar.js
// — per person, D83; subagents live inside their parent's session), a home
// view of what needs you, the chat of the selected conversation, the render
// pane for render_html output (sandboxed, see frameDoc), the workflow tree,
// and a tabbed settings area (config / features / memory / files / schedules
// / skills / MCP).
//
// Nothing polls. One live stream (stream.js) carries the run list and the
// selected run's whole tree; chat-view.js keeps the views, chat-fold.js turns
// them into blocks, chat-cards.js draws them. No framework beyond lit's
// render(), no build step; xbin.fetch attributes calls to this element.
import { html, render, nothing } from '/vendor/lit-all.min.js';

const $ = (id) => document.getElementById(id);
// esc() comes from the kit and escapes quotes as well as &<>: its output lands
// in ATTRIBUTE position in the settings tabs (title=, data-*, value=) with
// model-controlled data — memory keys the agent writes, skill names it authors.
import { selfApi as api, jbody, esc } from '/vendor/bx-kit.js';
import { Session } from './chat-view.js';
import { queueTpl } from './chat-cards.js';
import { ConvList } from './conv-list.js';
import { sidebarTpl, footTpl, makeSideUI } from './sidebar.js';
import { homeTpl } from './home.js';
import { schedulesTab } from './automations.js';
import { openShare, joinFrom } from './share.js';
// Raw-bytes endpoints (a file's bytes, an upload body) go through xbin.fetch
// directly — the kit's api() parses JSON — so they need this backend's prefix.
const base = `/api/${xbin.self}`;
const num = (v) => Number(v) || 0;
const clip = (s, n) => { s = String(s ?? ''); return s.length > n ? s.slice(0, n) + '…' : s; };
// Group digits for readability: 123123 → "123 123" (narrow no-break space).
const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');
const errBox = (e) => `<div class="err">${esc(e && e.message ? e.message : e)}</div>`;

let sel = null;          // selected run id (null = home)

// Capability lane for NEW asks (immutable per run once started): 'private'
// = internal systems only, 'web' = web only — the exfiltration firewall.
// Persisted via the per-user prefs API, NOT localStorage: tile frames are
// sandboxed opaque origins with no localStorage at all, and touching it throws
// — at module scope that kills the whole tile. The default stands until the
// async load lands.
let toolset = 'private';
async function loadToolsetPref() {
  try {
    const r = await xbin.fetch('/api/xbin/prefs/toolset');
    if (r.ok && (await r.json()) === 'web') { toolset = 'web'; syncToolsetBtn(); }
  } catch { /* keep default */ }
}
const TSET = { private: ['🔒', 'private data — internal systems, no web'], web: ['🌐', 'web — no internal systems'] };
function syncToolsetBtn() {
  const b = $('tset');
  b.textContent = TSET[toolset][0];
  b.title = `Tool mode for new asks: ${TSET[toolset][1]} (click to switch)`;
}
// The home view's words. An instance that specializes the agent (a persona,
// a domain) changes these and nothing else.
const HOME = {
  title: 'Agent',
  tagline: 'conversations · cron-agents',
  hi: 'What do you need?',
  sub: 'Ask below — every question starts a conversation of its own (yours, until you share it); recurring work becomes a cron-agent.',
  examples: [
    'What can you do in this workspace?',
    'Every morning at 8, check…',
    'Call apps/… and summarize what it returns',
  ],
  placeholder: 'ask anything…',
};
let models = [];         // model ids from GET /models ({data:[{id}]})
let cfgCache = null;     // last GET /config
let settingsOpen = false;
let activeTab = 'config';
let skillsCache = [];    // skills for the skills tab
let skillSel = null;     // name of the skill being edited (null = new)
let filesCache = [];     // session files for the files tab (lookup by index)
let filesSel = null;     // path of the file being edited (null = new)
const isHtmlPath = (p) => /\.html?$/i.test(p || '');

// --- the session ----------------------------------------------------------

const session = new Session(base, {
  change: () => paint(),
  runs: () => { paintSide(); if (sel == null) paint(); },
  gone: () => goHome(),
  event: (ev) => onEvent(ev),
  reset: () => { convs.load().catch(() => {}); loadNeeds(); },
});
const convs = new ConvList({ change: () => paintSide(), epoch: () => me.epochMs || 0 });
session.ui.act.select = (id) => selectRun(id);
session.ui.me = () => me.user;
session.ui.act.openFile = (path) => { filesSel = path; openSettings('files'); };

const ACTIVE = new Set(['running', 'awaiting', 'sleeping', 'waiting_input', 'queued', 'blocked']);

// --- the conversation list -------------------------------------------------
//
// Your conversations (GET /conversations — the server lists only what you may
// see), kept current by the stream. A subagent is never a row: it lives inside
// its parent's chat and the workflow tree.

const sideUI = makeSideUI({
  convs, api, selectRun: (id) => selectRun(id), goHome: () => goHome(), paint: () => paintSide(),
  current: () => session.current(), search: () => $('csearch'), me: () => me,
  share: (r) => openShare(r, me, () => convs.load()),
});

function paintSide() {
  render(sidebarTpl(convs, sideUI), $('runs'));
  render(footTpl(convs, sideUI), $('sfoot'));
  syncHalt($('halt').dataset.on === '1');
}

// onEvent sees every stream event: the list keeps itself current, and the
// conversation you are looking at stays read.
let needsDirty = null;
function onEvent(ev) {
  convs.apply(ev);
  if (ev.type === 'revoked' && ev.run === sideUI.sel) {
    goHome();
    xbin.notify?.('info', 'That conversation is no longer shared with you.');
  }
  if (ev.type === 'run' && ev.run === ev.root) {
    const r = convs.find(ev.run);
    if (r && r.unread && ev.run === sideUI.sel && document.visibilityState === 'visible') convs.read(ev.run);
    if (sel == null) { clearTimeout(needsDirty); needsDirty = setTimeout(loadNeeds, 300); }
  }
}

// --- home (no conversation open) -------------------------------------------

let needs = [];
async function loadNeeds() {
  try { needs = (await api('/needs')).items || []; } catch { needs = []; }
  if (sel == null) paint();
}

function goHome() {
  sel = null;
  closePreview(); prevSeen = null; prevDismissed = 0;
  closeWorkflow();
  session.select(null);
  setHash('');
  loadNeeds();
  paintSide(); paint();
}

function homeView() {
  const mcp = xbin.iface && xbin.iface('mcp');
  return homeTpl(HOME, needs, {
    mcpBound: !!(mcp && (mcp.endpoints || []).length),
    pick: (e) => { $('msg').value = e; autosize(); $('msg').focus(); },
    select: (id) => selectRun(id),
  });
}

// setHash keeps a link to what you look at (#c=<id>); a sandboxed frame may
// refuse history changes, which then just don't happen.
function setHash(h) {
  try { history.replaceState(null, '', h ? '#' + h : location.pathname + location.search); } catch { /* sandboxed */ }
}

// --- selecting a run --------------------------------------------------------

async function selectRun(id) {
  if (id == null) return goHome();
  sel = +id;
  closeWorkflow();
  if (preview && preview.runId !== sel) closePreview();
  prevSeen = null;
  try { await session.select(sel); } catch (e) { alert(e.message); return goHome(); }
  setHash('c=' + sel);
  const root = sideUI.sel;
  if (root != null) convs.read(root);
  paintSide(); paint();
  const tl = $('timeline');
  tl.scrollTop = tl.scrollHeight;
}

// --- painting -------------------------------------------------------------------

function topTpl(v) {
  if (!v) return html`<span class="title">${HOME.title}</span><span class="muted" style="font-size:11.5px">${HOME.tagline}</span>`;
  const r = v.run;
  // the tool mode — not who may see it (that is Share)
  const lane = (v.config && v.config.toolset) === 'web' ? '🌐 web' : '🔒 internal';
  const tree = r.parentId || (v.links || []).length;
  const talk = v.access !== 'viewer', own = !v.access || v.access === 'owner' || v.access === 'system';
  return html`<span class="title" title=${r.title || ''}>${r.title || 'run ' + r.id}</span>
    <span class="badge" title="tool mode (immutable for this run)">${lane}</span>
    <span class="badge ${r.status}">${r.status}</span>
    ${talk ? nothing : html`<span class="badge" title="shared with you to read">view only</span>`}
    ${talk && (r.status === 'error' || r.status === 'canceled') ? html`<button class="btn ghost btnsm" @click=${() => control('resume')} title="Drive the run again">Retry</button>` : nothing}
    ${talk ? html`<button class="btn ghost btnsm" @click=${() => control('compact')}>Compact</button>
    <button class="btn ghost btnsm" @click=${() => control('learn')} title="Distill this run into a reusable skill">Learn skill</button>` : nothing}
    <button class="btn ghost btnsm" @click=${() => control('mem')}>Memory (${Object.keys(v.memory || {}).length})</button>
    <button class="btn ghost btnsm" @click=${() => control('files')} title="This run's session files">Files (${(v.files || []).length})</button>
    ${tree ? html`<span class="badge wfchip" @click=${() => control('wf')} title="open the workflow tree">⑂ tree</span>` : nothing}
    <button class="btn ghost btnsm" @click=${() => openShare({ id: r.rootId || r.id, title: r.title }, me, () => convs.load())}
      title=${own ? 'Who can see this conversation' : 'Who this is shared with'}>${own ? 'Share' : 'Shared'}</button>
    ${own ? html`<button class="btn rm btnsm" @click=${() => control('delete')}>Delete</button>` : nothing}`;
}

// paint draws everything that depends on the session. lit patches only what
// changed, so this is cheap enough to run on every streamed token.
function paint() {
  const v = session.current();
  render(topTpl(v), $('top'));
  const tl = $('timeline');
  const atBottom = tl.scrollHeight - tl.scrollTop - tl.clientHeight < 40;
  render(v ? session.template() : homeView(), tl);
  if (atBottom) tl.scrollTop = tl.scrollHeight;
  render(queueTpl(v ? session.queued() : [], (iid) => session.removeQueued(iid).catch((e) => alert(e.message))), $('queue'));
  $('queue').hidden = !(v && session.queued().length);
  const busy = session.busy();
  $('stop').hidden = !busy;
  const viewOnly = !!(v && v.access === 'viewer');
  $('msg').disabled = viewOnly;
  $('msg').placeholder = !v ? HOME.placeholder
    : viewOnly ? 'view only — shared with you to read'
    : busy ? 'steer — delivered at the agent\'s next step…'
    : v.run.status === 'waiting_input' && (v.run.pendingState || {}).kind !== 'approval' ? 'answer the question…' : 'follow up…';
  if (v) syncPreview(v);
  if (wfOpen) treeDirty();
}

// --- workflow view ------------------------------------------------------

let wfOpen = false, wfRoot = null, wfSetKey = '', wfValKey = '', wfSel = null;

const WF_WORDS = { dep: 'waiting on', slot: 'queued — at the concurrency limit',
                   human: 'waiting on you', sleeping: 'sleeping', cancelling: 'cancelling…' };

function openWorkflow(rootId) {
  if (rootId == null) return;
  wfOpen = true; wfRoot = rootId; wfSetKey = ''; wfValKey = '';
  $('workflow').hidden = false;
  $('main').classList.add('wfon');
  loadTree();
}

function closeWorkflow() {
  wfOpen = false;
  $('workflow').hidden = true;
  $('main').classList.remove('wfon');
}

async function loadTree() {
  if (!wfOpen || wfRoot == null) return;
  let t;
  try { t = await api(`/runs/${wfRoot}/tree`); } catch { return; }
  if (!wfOpen) return;
  renderWorkflow(t);
}

// treeDirty re-reads the tree after the stream reported a change: at most one
// request in flight, and one more if anything changed while it was.
let treeBusy = false, treeAgain = false;
function treeDirty() {
  if (treeBusy) { treeAgain = true; return; }
  treeBusy = true;
  loadTree().finally(() => {
    treeBusy = false;
    if (treeAgain) { treeAgain = false; treeDirty(); }
  });
}

// The tree is re-read on every link/status event. A wholesale rebuild would
// drop the hovered row out from under the pointer and kill a button
// mid-click, so rebuild only when the node SET changes, and patch values
// otherwise.
function renderWorkflow(t) {
  const nodes = t.nodes || [];
  const setKey = nodes.map((n) => n.id).join(',');
  const valKey = JSON.stringify(nodes.map((n) => [n.status, n.updated, n.promptTokens, n.blockReason]));
  paintWorkflowHeader(t);
  if (setKey !== wfSetKey) { wfSetKey = setKey; wfValKey = valKey; return buildWorkflow(t); }
  if (valKey === wfValKey) return;
  wfValKey = valKey;
  patchWorkflow(t);
}

function paintWorkflowHeader(t) {
  const by = (t.totals && t.totals.byStatus) || {};
  const root = (t.nodes || []).find((n) => n.id === t.root);
  $('wf-title').textContent = root ? (root.title || 'run ' + t.root) : 'workflow';
  $('wf-title').title = $('wf-title').textContent;
  const parts = [];
  for (const k of ['running', 'queued', 'blocked', 'done', 'error', 'cancelled']) {
    if (by[k]) parts.push(`${by[k]} ${k}`);
  }
  $('wf-counts').textContent = `${(t.totals || {}).nodes || 0} nodes · ${parts.join(' · ') || 'idle'}`;
  const tot = t.totals || {};
  // A rate, not just a total: a total is alarming, a rate is actionable.
  $('wf-cost').textContent =
    `Σ ${fmtN(tot.promptTokens)}↑ ${fmtN(tot.completionTokens)}↓ · ${fmtN(tot.llmCalls)} calls · ${tot.active}/${tot.limit} running`;
}

// A run with no relatives gets no chip at all, so the workflow layer costs a
// plain single run nothing: no extra element in an already-crowded top bar,
// and no /tree request.
function wfCostOf(n) { return num(n.promptTokens) + num(n.completionTokens); }

function nodeRow(n, maxCost) {
  const cost = wfCostOf(n);
  const pct = maxCost > 0 ? Math.round(100 * cost / maxCost) : 0;
  const blocked = n.blockReason === 'dep' && (n.blockedOn || []).length;
  let sub = '', cls = '';
  if (blocked) { sub = `⛔ waiting on ${n.blockedOn.map((i) => '#' + i).join(', ')}`; cls = 'blk'; }
  else if (n.blockReason) { sub = '⏳ ' + (WF_WORDS[n.blockReason] || n.blockReason); cls = 'blk'; }
  else if (n.status === 'error') { sub = '⚠ ' + (n.result || 'failed'); cls = 'bad'; }
  else if (n.lastStep) { sub = n.lastStep; }
  return `<div class="wfn${wfSel === n.id ? ' on' : ''}" data-n="${num(n.id)}" style="--d:${Math.min(num(n.depth), 4)}">
    <span class="nm"><span class="dot ${esc(n.status)}"></span><span class="tt">${esc(n.title || 'run ' + n.id)}</span></span>
    <span class="cost">${cost ? fmtN(cost) : ''}${cost ? `<i class="share"><i style="width:${pct}%"></i></i>` : ''}</span>
    <span class="sub ${cls}">${esc(clip(sub, 160))}</span>
  </div>`;
}

function buildWorkflow(t) {
  const nodes = t.nodes || [];
  const maxCost = Math.max(1, ...nodes.map(wfCostOf));
  // Sorted by creation within a parent, never by status: status-sorting makes
  // rows jump under the cursor on every poll.
  const byParent = new Map();
  for (const n of nodes) {
    const k = n.id === t.root ? -1 : n.parentId;
    if (!byParent.has(k)) byParent.set(k, []);
    byParent.get(k).push(n);
  }
  const out = [];
  const walk = (list) => {
    for (const n of (list || []).sort((a, b) => a.created - b.created)) {
      out.push(nodeRow(n, maxCost));
      walk(byParent.get(n.id));
    }
  };
  walk(byParent.get(-1));
  const body = $('wf-body');
  body.innerHTML = out.join('') || '<div class="empty">no background runs</div>';
  body.querySelectorAll('[data-n]').forEach((el) => el.onclick = () => selectRun(+el.dataset.n));
}

function patchWorkflow(t) {
  const nodes = t.nodes || [];
  const maxCost = Math.max(1, ...nodes.map(wfCostOf));
  for (const n of nodes) {
    const el = $('wf-body').querySelector(`[data-n="${num(n.id)}"]`);
    if (!el) continue;
    const dot = el.querySelector('.dot');
    if (dot) dot.className = 'dot ' + n.status;
    const tmp = document.createElement('div');
    tmp.innerHTML = nodeRow(n, maxCost);
    el.querySelector('.cost').innerHTML = tmp.querySelector('.cost').innerHTML;
    const sub = tmp.querySelector('.sub');
    el.querySelector('.sub').className = sub.className;
    el.querySelector('.sub').textContent = sub.textContent;
  }
}

// me is who the tile is talking for (GET /me, D83): settings, the brake and
// oversight are the managers' — people with write access to the tile.
let me = { manager: true };
async function loadMe() {
  try { me = await api('/me'); } catch { /* an older backend: everything, as before */ }
  $('gear').hidden = !me.manager;
  syncHalt($('halt').dataset.on === '1');
}

async function loadHalt() {
  try {
    const h = await api('/halt');
    syncHalt(!!h.on);
  } catch { /* ignore */ }
}

function syncHalt(on) {
  const b = $('halt');
  b.hidden = !me.manager || (!on && !convs.all().some((r) => ACTIVE.has(r.status) && r.status !== 'waiting_input'));
  b.textContent = on ? '⏻ HALTED' : '⏻';
  b.title = on ? 'Resume — the agent is halted' : 'Stop every running agent now';
  b.dataset.on = on ? '1' : '';
}

// --- render pane --------------------------------------------------------

// The policy the rendered document runs under. sandbox="" on the iframe stops
// scripts, forms and navigation, but it does NOT stop subresource LOADS: a bare
// <img src="https://…/?leak=…"> would still fire a real request from the user's
// browser, which in a private-lane run is exfiltration. Nothing in the platform
// CSP prevents that (there is no img-src on /c/ documents), so this policy is
// the thing that closes it. A CSP the model writes itself can only intersect
// ours, never relax it.
const FRAME_CSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; " +
                  "font-src data:; form-action 'none'; base-uri 'none'";
// Arbitrary HTML assumes a white page and a sane body margin.
const FRAME_CSS = 'html{background:#fff;color:#111;color-scheme:light}' +
                  'body{margin:12px;font:14px/1.5 system-ui,-apple-system,sans-serif}' +
                  'img,svg,video,canvas,table,pre{max-width:100%}' +
                  'pre{overflow-x:auto}table{border-collapse:collapse}';

// frameDoc composes what the render frame parses. The model's file is
// UNTRUSTED, so it is parsed with DOMParser — which produces a document with no
// browsing context: nothing loads, nothing executes, and the pre-pass is free
// of side effects. Re-serializing is also a normalizer: unterminated attributes
// and mismatched tags come back well-formed and correctly escaped, so there is
// no regex guessing at tag syntax (a naive /<meta[^>]+refresh/ misses
// `<meta/http-equiv=refresh …>`, which parses perfectly well).
function frameDoc(src) {
  const doc = new DOMParser().parseFromString(String(src ?? ''), 'text/html');
  let blocked = 0;

  // A meta refresh navigates the FRAME, which sandbox="" permits (only top
  // navigation is blocked) and which no CSP directive covers since navigate-to
  // was dropped from the spec. It is a plain outbound GET — strip it.
  doc.querySelectorAll('meta[http-equiv]').forEach((m) => {
    if (/^\s*refresh\s*$/i.test(m.getAttribute('http-equiv') || '')) { m.remove(); blocked++; }
  });
  // A click on an off-page link is the same GET, one interaction later. Keep
  // in-page anchors (#toc) — fragment navigation inside about:srcdoc is fine.
  doc.querySelectorAll('[target]').forEach((e) => e.removeAttribute('target'));
  doc.querySelectorAll('a[href]').forEach((a) => {
    if (!(a.getAttribute('href') || '').startsWith('#')) a.removeAttribute('href');
  });
  // Count what the policy will refuse. In a private-lane run an unexpected
  // remote image is a signal worth showing the human, not just silence.
  doc.querySelectorAll('img[src], source[src], link[href], use[href], iframe[src], object[data]')
    .forEach((el) => {
      const u = el.getAttribute('src') || el.getAttribute('href') || el.getAttribute('data') || '';
      if (u && !/^(data:|#)/i.test(u)) blocked++;
    });

  const mk = (tag, attrs, text) => {
    const e = doc.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
    if (text) e.textContent = text;
    return e;
  };
  // Order is load-bearing: the CSP meta must be the first thing in <head> in
  // the serialized byte stream, or anything parsed before it escapes it.
  doc.head.prepend(
    mk('meta', { 'http-equiv': 'Content-Security-Policy', content: FRAME_CSP }),
    mk('meta', { charset: 'utf-8' }),
    mk('meta', { name: 'viewport', content: 'width=device-width,initial-scale=1' }),
    mk('base', { target: '_blank' }),   // no href: makes any surviving link inert
    mk('style', {}, FRAME_CSS),
  );
  // Our doctype, emitted first, locks standards mode whatever the model wrote.
  return { html: '<!doctype html>' + doc.documentElement.outerHTML, blocked };
}

let preview = null;     // {runId, path, ver, live}
let prevSig = '';       // (run, path, version) currently loaded IN the frame
let prevSeen = null;    // newest render step seq observed for this run
let prevDismissed = 0;  // render step seq the user closed on

function closePreview() {
  preview = null;
  $('preview').hidden = true;
  $('main').classList.remove('prev-max');
  // prevSig and .srcdoc stay put, so reopening the same file is instant.
}

async function openPreview(path, ver, live) {
  if (sel == null || !path) return;
  preview = { runId: sel, path, ver: num(ver), live: !!live };
  $('preview').hidden = false;
  $('prev-path').textContent = path;
  $('prev-path').title = path;
  // The pane just took height from the timeline. The autoscroll check measures
  // clientHeight at rebuild time, so without re-pinning here the next tick
  // decides we are no longer at the bottom and silently stops following.
  const tl = $('timeline');
  tl.scrollTop = tl.scrollHeight;
  await paintPreview();
}

// paintPreview is the ONLY writer of .srcdoc, and it writes only when the
// (run, path, version) triple changes: assigning srcdoc reloads the frame — a
// white flash and a lost scroll position — so this gate is what stops the 1.5s
// poll from thrashing it.
async function paintPreview() {
  const p = preview;
  if (!p) return;
  const sig = `${p.runId}\u0000${p.path}\u0000${p.ver}`;
  if (sig === prevSig) return;
  let f;
  try {
    f = await api(`/runs/${p.runId}/file?path=${encodeURIComponent(p.path)}`);
  } catch (e) {
    $('prev-warn').hidden = false;
    $('prev-warn').textContent = '⚠ ' + (e.message || e);
    return;
  }
  if (!preview || preview.path !== p.path || preview.runId !== p.runId) return; // stale
  const { html, blocked } = frameDoc(f.content);
  prevSig = sig;
  // PROPERTY assignment, never interpolation into an srcdoc="…" attribute: the
  // DOM takes the raw string, so there is no attribute escaping to get wrong
  // and the model's bytes never touch an innerHTML path. This is the single
  // most important invariant in the render feature.
  $('prevframe').srcdoc = html;
  const stale = p.ver && f.version && f.version !== p.ver;
  $('prev-ver').textContent = f.version ? 'v' + f.version : '';
  $('prev-ver').title = stale ? `this chip rendered v${p.ver}; showing the current v${f.version}` : '';
  const warns = [];
  if (blocked) warns.push(`⚠ ${blocked} external resource${blocked > 1 ? 's' : ''} blocked`);
  if (stale) warns.push(`showing v${f.version} (chip was v${p.ver})`);
  $('prev-warn').hidden = !warns.length;
  $('prev-warn').textContent = warns.join(' · ');
}

// syncPreview follows the run's newest render step. Called from paint on
// every tick; it only acts when a NEW render lands.
function syncPreview(d) {
  if (preview && preview.runId !== sel) closePreview();
  const rs = (d.steps || []).filter((s) => s.kind === 'render');
  const last = rs.length ? rs[rs.length - 1] : null;
  if (!last) { prevSeen = null; return; }
  let det = {};
  try { det = JSON.parse(last.detail); } catch { return; }

  const first = prevSeen === null;
  // Landing on an old finished run should not pop a pane open; a render that
  // arrives while you are watching should.
  const fresh = first ? (Date.now() / 1000 - last.created) < 60 : last.seq > prevSeen;
  prevSeen = last.seq;
  if (!fresh) return;
  if (prevDismissed === last.seq) return;          // the user closed this one
  if (settingsOpen || wfOpen) return;              // don't yank an open view away
  if (document.visibilityState !== 'visible') return;
  if (preview && !preview.live) return;            // the user pinned an older chip
  openPreview(det.path, num(det.version), true);
}

async function rawBlob(run, path) {
  const r = await xbin.fetch(`${base}/runs/${run}/raw?path=${encodeURIComponent(path)}`);
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return r.blob();
}

// refreshView re-reads the selected run's view after an edit the stream does
// not carry (memory blocks, session files).
function refreshView() {
  if (sel != null) session.fetchView(sel).then(paint).catch(() => {});
}

async function control(action) {
  if (action === 'mem') return openSettings('memory');
  if (action === 'files') return openSettings('files');
  if (action === 'wf') { const v = session.current(); return openWorkflow(v ? (v.run.rootId || v.run.id) : sel); }
  if (action === 'delete') {
    if (!confirm('Delete this run and its history?')) return;
    try { await api(`/runs/${sel}`, { method: 'DELETE' }); } catch (e) { return alert(e.message); }
    if (settingsOpen && activeTab === 'memory') renderTab();
    session.runs.delete(sel);
    return goHome();
  }
  // resume | compact | learn → POST /runs/{id}/{action}
  try { await api(`/runs/${sel}/${action}`, { method: 'POST' }); } catch (e) { alert(e.message); }
}

// --- composer -----------------------------------------------------------

// Attachments waiting to be sent. Each is uploaded into the run's session files
// (PUT /runs/{id}/upload) and then named in the message, so the model finds
// them with its file tools and sees images. `path` is set once an upload
// lands, so a retry after a failed message re-sends rather than re-uploads.
const MAX_ATTACH = 16 * 1024 * 1024; // the backend's per-file cap
let attachments = [];   // [{key, file, name, size, type, path?, state?, err?}]
let attachSeq = 0;
let sending = false;

const fmtBytes = (n) => n < 1024 ? `${n} B` : n < 1048576 ? `${(n / 1024).toFixed(0)} KB` : `${(n / 1048576).toFixed(1)} MB`;

function addFiles(list) {
  for (const f of list || []) {
    const a = { key: ++attachSeq, file: f, name: f.name || 'pasted', size: f.size, type: f.type };
    if (f.size > MAX_ATTACH) { a.state = 'bad'; a.err = `too large (max ${fmtBytes(MAX_ATTACH)})`; }
    attachments.push(a);
  }
  renderAttach();
}

function renderAttach() {
  const host = $('attach');
  host.hidden = attachments.length === 0;
  host.innerHTML = attachments.map((a) => `<span class="chip ${a.state || ''}" title="${esc(a.err || a.type || '')}">
    <span class="nm">${esc(a.name)}</span><span class="sz">${a.state === 'up' ? 'uploading…' : a.err ? esc(a.err) : fmtBytes(a.size)}</span>
    <button data-rm="${a.key}" title="remove" ${sending ? 'disabled' : ''}>✕</button></span>`).join('');
  host.querySelectorAll('[data-rm]').forEach((b) => b.onclick = () => {
    attachments = attachments.filter((a) => a.key !== +b.dataset.rm);
    renderAttach();
  });
}

// uploadAttachments puts every not-yet-uploaded attachment into run `id`, in
// order, and returns all their session-file paths. Throws on the first failure
// with that chip marked; chips already uploaded keep their path.
async function uploadAttachments(id) {
  for (const a of attachments) {
    if (a.path) continue;
    a.state = 'up'; a.err = ''; renderAttach();
    const r = await xbin.fetch(`${base}/runs/${id}/upload?name=${encodeURIComponent(a.name)}`, {
      method: 'PUT', headers: { 'Content-Type': a.type || 'application/octet-stream' }, body: a.file,
    });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) {
      a.state = 'bad'; a.err = d.error || `upload failed (${r.status})`; renderAttach();
      throw new Error(`${a.name}: ${a.err}`);
    }
    a.path = d.path; a.state = 'done'; renderAttach();
  }
  return attachments.map((a) => a.path);
}

async function send() {
  if (sending) return;
  const t = $('msg').value.trim();
  if (!t && !attachments.length) return;
  if (attachments.some((a) => a.size > MAX_ATTACH)) {
    return alert('Remove the files that are too large first.');
  }
  sending = true; $('send').disabled = true;
  try {
    // On home: start a fresh quick ask and jump into it (streaming answer).
    if (sel == null) {
      if (!attachments.length) {
        $('msg').value = ''; autosize();
        const run = await api('/ask', jbody({ text: t, toolset }, 'POST'));
        session.runs.set(run.id, run);
        await selectRun(run.id);
        return;
      }
      // With attachments there is no run to upload into yet: create it held
      // (no message, no drive), upload, then send the message into it.
      const title = t || attachments.map((a) => a.name).join(', ');
      const run = await api('/ask', jbody({ text: title, toolset, hold: true }, 'POST'));
      try {
        const files = await uploadAttachments(run.id);
        await api(`/runs/${run.id}/message`, jbody({ text: t, files }, 'POST'));
      } catch (e) {
        // Don't leave an empty run behind; its uploads go with it, so the
        // chips must upload again next time.
        await api(`/runs/${run.id}`, { method: 'DELETE' }).catch(() => {});
        attachments.forEach((a) => { delete a.path; if (a.state === 'done') a.state = ''; });
        throw e;
      }
      $('msg').value = ''; autosize(); attachments = [];
      session.runs.set(run.id, run);
      await selectRun(run.id);
      return;
    }
    const files = attachments.length ? await uploadAttachments(sel) : undefined;
    // While the run works this is queued and delivered at its next step (the
    // strip above the composer shows it until then).
    await session.send(t, files);
    $('msg').value = ''; autosize(); attachments = [];
  } catch (e) {
    alert(e.message);
  } finally {
    sending = false; $('send').disabled = false;
    renderAttach();
  }
}
$('send').onclick = send;
$('clip').onclick = () => $('clipin').click();
$('clipin').onchange = () => { addFiles($('clipin').files); $('clipin').value = ''; };
// Pasting an image (a screenshot) attaches it; pasting text is left alone.
$('msg').addEventListener('paste', (e) => {
  const files = [...(e.clipboardData?.files || [])];
  if (!files.length) return;
  e.preventDefault();
  addFiles(files);
});
// Drop anywhere on the run view. dragenter/leave fire for every child, so the
// highlight is driven by a counter rather than by which element was entered.
{
  const main = $('main');
  let depth = 0;
  const hasFiles = (e) => [...(e.dataTransfer?.types || [])].includes('Files');
  main.addEventListener('dragenter', (e) => { if (!hasFiles(e)) return; e.preventDefault(); depth++; main.classList.add('dropping'); });
  main.addEventListener('dragover', (e) => { if (hasFiles(e)) e.preventDefault(); });
  main.addEventListener('dragleave', () => { if (--depth <= 0) { depth = 0; main.classList.remove('dropping'); } });
  main.addEventListener('drop', (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault(); depth = 0; main.classList.remove('dropping');
    addFiles(e.dataTransfer.files);
    $('msg').focus();
  });
}
// Enter sends, Shift+Enter is a new line — and Enter that confirms an IME
// composition (CJK input) is the IME's, not a send.
$('msg').addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229) { e.preventDefault(); send(); }
});
// The composer grows with its text, up to a third of the tile.
function autosize() {
  const m = $('msg');
  m.style.height = 'auto';
  m.style.height = Math.min(m.scrollHeight, Math.max(80, window.innerHeight / 3)) + 'px';
}
$('msg').addEventListener('input', autosize);
// Stop interrupts the run. Messages still queued come back into the composer
// rather than being sent to a run you just stopped.
$('stop').onclick = async () => {
  try {
    const back = await session.stop();
    const text = back.map((q) => q.text).filter(Boolean).join('\n\n');
    if (text) { $('msg').value = [text, $('msg').value].filter(Boolean).join('\n\n'); autosize(); $('msg').focus(); }
  } catch (e) { alert(e.message); }
};

// Render pane header. Closing remembers WHICH render was dismissed, so the
// poll doesn't immediately reopen the same one.
$('wf-close').onclick = () => closeWorkflow();
$('wf-stop').onclick = async () => {
  if (wfRoot == null || !confirm('Cancel this workflow and every run below it?')) return;
  try { await api(`/runs/${wfRoot}/cancel`, jbody({ scope: 'subtree', reason: 'stopped from the tile' }, 'POST')); }
  catch (e) { return alert(e.message); }
  loadTree();
};
// One click, no confirm — during a runaway every dialog is another second of
// spend. The undo is the same button.
$('halt').onclick = async () => {
  const on = $('halt').dataset.on !== '1';
  try { await api('/halt', jbody({ on }, 'PUT')); } catch (e) { return alert(e.message); }
  syncHalt(on);
  if (wfOpen) loadTree();
};
$('prev-close').onclick = () => { prevDismissed = prevSeen; closePreview(); };
$('prev-max').onclick = () => {
  $('main').classList.toggle('prev-max');
  const tl = $('timeline');
  tl.scrollTop = tl.scrollHeight;
};
$('prev-src').onclick = () => {
  if (!preview) return;
  filesSel = preview.path;
  openSettings('files');
};
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape' || $('newdlg').open || settingsOpen) return;
  if (preview) { prevDismissed = prevSeen; closePreview(); return; }
  if (wfOpen) closeWorkflow();
});
$('home').onclick = goHome;
$('tset').onclick = () => {
  toolset = toolset === 'private' ? 'web' : 'private';
  xbin.fetch('/api/xbin/prefs/toolset', { method: 'PUT', body: JSON.stringify(toolset) }).catch(() => {});
  syncToolsetBtn();
};
syncToolsetBtn();
loadToolsetPref();

// --- new chat ------------------------------------------------------------

$('new').onclick = () => { goHome(); $('msg').focus(); };
// "New chat with options": a title, a system prompt, a lane — the first
// message is the dialog's text.
$('newopts').onclick = () => {
  $('n-goal').value = ''; $('n-title').value = ''; $('n-system').value = ''; $('n-toolset').value = toolset;
  $('newdlg').showModal();
};
$('n-create').onclick = async (e) => {
  const text = $('n-goal').value.trim();
  if (!text) { e.preventDefault(); return; }
  try {
    const run = await api('/ask', jbody({ text, title: $('n-title').value.trim(), system: $('n-system').value.trim(), toolset: $('n-toolset').value }, 'POST'));
    session.runs.set(run.id, run);
    await selectRun(run.id);
  } catch (err) { alert(err.message); }
};
let searchT = null;
$('csearch').oninput = () => {
  clearTimeout(searchT);
  const v = $('csearch').value;
  if (v.includes('#join=')) { $('csearch').value = ''; join(v); return; } // a pasted invite link
  searchT = setTimeout(() => convs.search(v).catch(() => {}), 200);
};
// join redeems an invite link (#join=… — on the tile's URL, or pasted).
async function join(text) {
  try {
    const r = await joinFrom(text);
    if (r) { await convs.load(); selectRun(r.runId); }
  } catch (e) { alert(e.message); }
}

// --- settings panel + tabs ---------------------------------------------

function syncTabs() {
  document.querySelectorAll('#tabs .tab[data-tab]').forEach((b) => b.classList.toggle('on', b.dataset.tab === activeTab));
}
function openSettings(tab) {
  settingsOpen = true;
  if (tab) activeTab = tab;
  $('settings').hidden = false;
  syncTabs();
  renderTab();
}
function closeSettings() { settingsOpen = false; $('settings').hidden = true; }

async function renderTab() {
  const bd = $('sbd');
  const fns = { config: tabConfig, features: tabFeatures, memory: tabMemory, files: tabFiles, schedules: tabSchedules, skills: tabSkills, mcp: tabMcp };
  const fn = fns[activeTab] || tabConfig;
  bd.innerHTML = '<div class="empty">loading…</div>';
  try { await fn(bd); } catch (e) { bd.innerHTML = errBox(e); }
}

$('gear').onclick = () => (settingsOpen ? closeSettings() : openSettings());
$('settings-close').onclick = closeSettings;
document.querySelectorAll('#tabs .tab[data-tab]').forEach((b) => b.onclick = () => {
  activeTab = b.dataset.tab; syncTabs(); renderTab();
});

async function ensureModels(force) {
  if (models.length && !force) return;
  try { const d = await api('/models'); models = (d.data || []).map((x) => x.id).filter(Boolean); }
  catch { if (!models.length) models = []; }
}

// Config tab: model tiers + system prompt + limits + behavior. Saves the FULL
// merged config (preserving features/mcp/legacy model) via PUT /config.
async function tabConfig(bd) {
  const c = await api('/config');
  cfgCache = c;
  await ensureModels(true);
  const m = c.models || {};
  const opt = (v) => `<option value="">— llm-gw default —</option>` +
    models.map((id) => `<option ${id === v ? 'selected' : ''}>${esc(id)}</option>`).join('');
  bd.innerHTML = `
    <div class="sec"><h4>Model tiers</h4>
      <div class="grid4">
        <div class="field"><label>General</label><select id="cf-general">${opt(m.general)}</select></div>
        <div class="field"><label>Code</label><select id="cf-code">${opt(m.code)}</select></div>
        <div class="field"><label>Memory</label><select id="cf-memory">${opt(m.memory)}</select></div>
        <div class="field"><label>Vision (VLM)</label><select id="cf-vlm">${opt(m.vlm)}</select></div>
      </div>
      <div class="hint">Empty tier = the workspace's llm-gw default for that job.${models.length ? '' : ' (no models listed — set an llm-gw backend token)'}</div>
    </div>
    <div class="sec"><h4>Base system prompt</h4><textarea id="cf-system" rows="5">${esc(c.system || '')}</textarea></div>
    <div class="sec"><h4>Limits</h4><div class="grid4">
      <div class="field"><label>Token budget</label><input id="cf-budget" type="number" value="${num(c.tokenBudget)}"></div>
      <div class="field"><label>Max iters / drive</label><input id="cf-iters" type="number" value="${num(c.maxIters)}"></div>
      <div class="field"><label>Tool timeout (s)</label><input id="cf-timeout" type="number" value="${num(c.toolTimeout)}"></div>
    </div></div>
    <div class="sec"><h4>Behavior</h4>
      <label class="chk"><input type="checkbox" id="cf-sub" ${c.subagents ? 'checked' : ''}> Subagents (expose <span class="mono">spawn_subagent</span>)</label>
      <label class="chk"><input type="checkbox" id="cf-appr" ${c.approve ? 'checked' : ''}> Require approval before side-effecting tools</label>
    </div>
    <div><button class="btn" id="cf-save">Save config</button> <span class="muted" id="cf-msg"></span></div>`;
  $('cf-save').onclick = async () => {
    const next = {
      ...cfgCache,
      models: { general: $('cf-general').value, code: $('cf-code').value, memory: $('cf-memory').value, vlm: $('cf-vlm').value },
      system: $('cf-system').value,
      tokenBudget: num($('cf-budget').value), maxIters: num($('cf-iters').value), toolTimeout: num($('cf-timeout').value),
      subagents: $('cf-sub').checked, approve: $('cf-appr').checked,
    };
    try {
      await api('/config', jbody(next, 'PUT')); cfgCache = next;
      $('cf-msg').textContent = 'saved ✓';
      setTimeout(() => { const e = $('cf-msg'); if (e) e.textContent = ''; }, 1500);
    } catch (e) { $('cf-msg').textContent = e.message; }
  };
}

// Features tab: a checkbox per capability. Toggling fetches the current config,
// merges {features:{...}}, and PUTs it back.
async function tabFeatures(bd) {
  const f = await api('/features');
  const keys = f.keys || [];
  const st = f.features || {};
  const desc = {
    recall: 'FTS recall over turns compacted out of the window',
    skills: 'skill-library tools + the injected skills list',
    streaming: 'stream partial assistant text (the live draft)',
    vision: 'send images to the VLM tier',
    parallelTools: "run a turn's tool calls in parallel",
    watcher: 'watcher cron-agents (one persistent run, discard no-change rounds)',
  };
  bd.innerHTML = `<div class="sec"><h4>Features</h4>
    ${keys.map((k) => `<label class="chk"><input type="checkbox" data-f="${esc(k)}" ${st[k] ? 'checked' : ''}>
      <b>${esc(k)}</b> <span class="muted" style="font-weight:400">${esc(desc[k] || '')}</span></label>`).join('')}
    <div class="hint">Each toggle merges into the agent's default config.</div></div>`;
  bd.querySelectorAll('[data-f]').forEach((b) => b.onchange = async () => {
    try {
      const c = await api('/config');
      c.features = { ...(c.features || {}), [b.dataset.f]: b.checked };
      await api('/config', jbody(c, 'PUT')); cfgCache = c;
    } catch (e) { alert(e.message); }
    tabFeatures(bd);
  });
}

// Memory tab: the SELECTED run's memory blocks (key→value): edit/add/delete.
async function tabMemory(bd) {
  if (sel == null) { bd.innerHTML = '<div class="empty">select a run to edit its memory blocks</div>'; return; }
  const d = await api(`/runs/${sel}`);
  const entries = Object.entries(d.memory || {});
  const keys = entries.map((e) => e[0]);
  bd.innerHTML = `<div class="sec"><h4>Memory · run ${sel}</h4>
    ${entries.length ? entries.map(([k, v], i) => `
      <div class="kv"><span class="mono" title="${esc(k)}">${esc(k)}</span>
        <input value="${esc(v)}" data-v="${i}">
        <span><button class="btn ghost btnsm" data-set="${i}">Set</button>
        <button class="btn rm btnsm" data-del="${i}">Del</button></span></div>`).join('') : '<div class="hint">no memory blocks yet</div>'}
    <div class="kv" style="margin-top:10px">
      <input id="mk" placeholder="new key"><input id="mv" placeholder="value">
      <button class="btn btnsm" id="madd">Add</button></div>
  </div>`;
  bd.querySelectorAll('[data-set]').forEach((b) => b.onclick = async () => {
    const i = +b.dataset.set;
    try { await api(`/runs/${sel}/memory`, jbody({ key: keys[i], value: bd.querySelector(`[data-v="${i}"]`).value }, 'PUT')); }
    catch (e) { return alert(e.message); }
    tabMemory(bd); refreshView();
  });
  bd.querySelectorAll('[data-del]').forEach((b) => b.onclick = async () => {
    const i = +b.dataset.del;
    try { await api(`/runs/${sel}/memory?key=${encodeURIComponent(keys[i])}`, { method: 'DELETE' }); }
    catch (e) { return alert(e.message); }
    tabMemory(bd); refreshView();
  });
  $('madd').onclick = async () => {
    const k = $('mk').value.trim();
    if (!k) return;
    try { await api(`/runs/${sel}/memory`, jbody({ key: k, value: $('mv').value }, 'PUT')); }
    catch (e) { return alert(e.message); }
    tabMemory(bd); refreshView();
  };
}

// Files tab: this run's session files — the same store the agent's file_*
// tools write. Human-editable on purpose: fixing a typo and
// hitting Render is the fastest debug loop there is. Optimistic concurrency —
// we send back the version we loaded, so a write the agent made in between
// comes back as a visible 409 instead of silently losing one side.
async function tabFiles(bd) {
  if (sel == null) { bd.innerHTML = '<div class="empty">select a run to see its session files</div>'; return; }
  filesCache = (await api(`/runs/${sel}/files`)) || [];
  const cur = filesSel != null ? filesCache.find((f) => f.path === filesSel) : null;
  let body = '';
  if (cur && !cur.binary) {
    const full = await api(`/runs/${sel}/file?path=${encodeURIComponent(cur.path)}`);
    body = full.content || '';
    cur.version = full.version;
  }
  // An attachment (binary) is never loaded into the textarea: it gets a
  // preview when it is an image, and a download either way.
  const isImg = (f) => f && f.binary && /^image\/(png|jpeg|gif|webp)$/.test(f.mime || '');
  const editor = cur && cur.binary ? `
    <div class="sec"><h4>Attachment · ${esc(cur.path)}
      <button class="btn ghost btnsm" id="fl-new">+ new</button></h4>
      ${isImg(cur) ? '<img id="fl-img" class="fprev" alt="">' : ''}
      <div class="hint">${esc(cur.mime || 'binary')} · ${fmtBytes(num(cur.bytes))} — the agent ${isImg(cur) ? 'sees it with file_view' : 'can list it but not read it as text'}.</div>
      <div style="margin-top:6px"><button class="btn ghost" id="fl-dl">Download</button> <span class="err" id="fl-err"></span></div>
    </div>` : `
    <div class="sec"><h4>${cur ? 'Edit · ' + esc(cur.path) : 'New file'}
      ${cur ? '<button class="btn ghost btnsm" id="fl-new">+ new</button>' : ''}</h4>
      <div class="field"><label>Path</label>
        <input id="fl-path" class="mono" value="${esc(cur ? cur.path : '')}" ${cur ? 'readonly' : ''} placeholder="report.html"></div>
      <div class="field"><label>Content</label>
        <textarea id="fl-body" class="mono" rows="14" spellcheck="false">${esc(body)}</textarea></div>
      <div><button class="btn" id="fl-save">Save</button>
        ${cur && isHtmlPath(cur.path) ? ' <button class="btn ghost" id="fl-render">Render</button>' : ''}
        <span class="err" id="fl-err"></span></div>
    </div>`;
  bd.innerHTML = `
    <div class="sec"><h4>Session files · run ${sel}</h4>
      <div class="tblwrap"><table class="tbl"><tr><th>path</th><th>type</th><th>bytes</th><th>v</th><th></th></tr>
      ${filesCache.length ? filesCache.map((f, i) => `<tr>
        <td class="mono">${esc(f.path)}</td>
        <td class="muted">${esc(f.binary ? f.mime : (f.mime || 'text'))}</td>
        <td class="muted">${fmtN(f.bytes)}</td>
        <td class="muted">${num(f.version)}</td>
        <td style="text-align:right; white-space:nowrap">
          ${isHtmlPath(f.path) ? `<button class="btn ghost btnsm" data-fr="${i}" title="show in the render pane">Render</button> ` : ''}
          <button class="btn ghost btnsm" data-fe="${i}">${f.binary ? 'View' : 'Edit'}</button>
          <button class="btn rm btnsm" data-fd="${i}">Del</button></td></tr>`).join('')
        : '<tr><td colspan="5" class="muted">no files yet — the agent writes these with its file tools; attach your own with 📎</td></tr>'}
      </table></div>
      <div class="hint">Text lives in this run's database; attachments in the tile's blob store. Deleting the run deletes both.</div>
    </div>${editor}`;

  if (cur && cur.binary) {
    const run = sel;
    if ($('fl-img')) rawBlob(run, cur.path).then((b) => {
      const img = $('fl-img');
      if (!img) return;
      img.src = URL.createObjectURL(b);
      img.onload = () => URL.revokeObjectURL(img.src);
    }).catch((e) => { if ($('fl-err')) $('fl-err').textContent = e.message; });
    $('fl-dl').onclick = async () => {
      try { xbin.download(cur.path.split('/').pop(), await rawBlob(run, cur.path)); }
      catch (e) { $('fl-err').textContent = e.message; }
    };
  }

  bd.querySelectorAll('[data-fe]').forEach((b) => b.onclick = () => {
    filesSel = filesCache[+b.dataset.fe].path; tabFiles(bd);
  });
  bd.querySelectorAll('[data-fr]').forEach((b) => b.onclick = () => {
    const f = filesCache[+b.dataset.fr];
    closeSettings(); openPreview(f.path, f.version, false);
  });
  bd.querySelectorAll('[data-fd]').forEach((b) => b.onclick = async () => {
    const f = filesCache[+b.dataset.fd];
    if (!confirm(`Delete "${f.path}"?`)) return;
    try { await api(`/runs/${sel}/file?path=${encodeURIComponent(f.path)}`, { method: 'DELETE' }); }
    catch (e) { return alert(e.message); }
    if (filesSel === f.path) filesSel = null;
    if (preview && preview.path === f.path) closePreview();
    tabFiles(bd); refreshView();
  });
  if ($('fl-new')) $('fl-new').onclick = () => { filesSel = null; tabFiles(bd); };
  if ($('fl-render')) $('fl-render').onclick = () => { closeSettings(); openPreview(cur.path, cur.version, false); };
  if (!$('fl-save')) return;
  $('fl-save').onclick = async () => {
    const path = $('fl-path').value.trim();
    $('fl-err').textContent = '';
    if (!path) { $('fl-err').textContent = 'need a path'; return; }
    try {
      const r = await api(`/runs/${sel}/file`, jbody({
        path, content: $('fl-body').value, version: cur ? cur.version : 0,
      }, 'PUT'));
      filesSel = path;
      // An open pane showing this file must repaint: bump it to the new version.
      if (preview && preview.path === path) openPreview(path, r.version, preview.live);
      tabFiles(bd); refreshView();
    } catch (e) { $('fl-err').textContent = e.message; }
  };
}

// Schedules tab: cron-agents — list with enable/disable, run-now, delete, and a
// create form. A bad cron expression comes back as a 400 error we surface.
// The schedules tab lives with the automations (automations.js).
const tabSchedules = (bd) => schedulesTab(bd, { api, jbody, esc, clip });

// Skills tab: the self-authored skill library — list, view/edit, save, delete.
async function tabSkills(bd) {
  const list = await api('/skills');
  skillsCache = list || [];
  const cur = skillSel != null ? skillsCache.find((s) => s.name === skillSel) : null;
  bd.innerHTML = `
    <div class="sec"><h4>Skills</h4>
      <table class="tbl"><tr><th>name</th><th>description</th><th>updated</th><th></th></tr>
      ${skillsCache.length ? skillsCache.map((s, i) => `<tr>
        <td class="mono">${esc(s.name)}</td>
        <td class="muted">${esc(clip(s.description, 80))}</td>
        <td class="muted">${s.updated ? new Date(s.updated * 1000).toLocaleDateString() : ''}</td>
        <td style="text-align:right; white-space:nowrap">
          <button class="btn ghost btnsm" data-sk="${i}">Edit</button>
          <button class="btn rm btnsm" data-skdel="${i}">Del</button></td></tr>`).join('')
        : '<tr><td colspan="4" class="muted">no skills yet — the agent authors these (use “Learn skill” on a run), or add one below</td></tr>'}
      </table>
    </div>
    <div class="sec"><h4>${cur ? 'Edit skill' : 'New skill'}
      ${cur ? '<button class="btn ghost btnsm" id="sk-new">+ new</button>' : ''}</h4>
      <div class="field"><label>Name</label><input id="sk-name" value="${esc(cur ? cur.name : '')}" ${cur ? 'readonly' : ''}></div>
      <div class="field"><label>Description</label><input id="sk-desc" value="${esc(cur ? cur.description : '')}"></div>
      <div class="field"><label>Content</label><textarea id="sk-content" rows="10">${esc(cur ? cur.content : '')}</textarea></div>
      <div><button class="btn" id="sk-save">Save skill</button> <span class="err" id="sk-err"></span></div>
    </div>`;
  bd.querySelectorAll('[data-sk]').forEach((b) => b.onclick = () => { skillSel = skillsCache[+b.dataset.sk].name; tabSkills(bd); });
  bd.querySelectorAll('[data-skdel]').forEach((b) => b.onclick = async () => {
    const s = skillsCache[+b.dataset.skdel];
    if (!confirm(`Delete skill "${s.name}"?`)) return;
    try { await api(`/skills/${encodeURIComponent(s.name)}`, { method: 'DELETE' }); } catch (e) { return alert(e.message); }
    if (skillSel === s.name) skillSel = null;
    tabSkills(bd);
  });
  if ($('sk-new')) $('sk-new').onclick = () => { skillSel = null; tabSkills(bd); };
  $('sk-save').onclick = async () => {
    const name = $('sk-name').value.trim();
    $('sk-err').textContent = '';
    if (!name || !$('sk-content').value.trim()) { $('sk-err').textContent = 'need a name and content'; return; }
    try {
      await api('/skills', jbody({ name, description: $('sk-desc').value.trim(), content: $('sk-content').value }, 'PUT'));
      skillSel = name; tabSkills(bd);
    } catch (e) { $('sk-err').textContent = e.message; }
  };
}

// MCP tab: read-only status of the bound MCP providers (the multi:true `mcp`
// http interface). xbin.iface('mcp') is null, or {multi, endpoints:[...]}.
function tabMcp(bd) {
  const mcp = (window.xbin && xbin.iface) ? xbin.iface('mcp') : null;
  const eps = (mcp && mcp.endpoints) || [];
  bd.innerHTML = `<div class="sec"><h4>MCP providers</h4>
    ${eps.length ? `<table class="tbl"><tr><th>provider</th><th>endpoint</th></tr>
      ${eps.map((e) => `<tr><td class="mono">${esc(e.provider || e.instance || e.service || '')}</td>
        <td class="mono muted">${esc(e.url || '')}</td></tr>`).join('')}</table>
      <div class="hint">Their tools are offered to the model as <span class="mono">mcp:&lt;server&gt;:&lt;tool&gt;</span>.</div>`
      : `<div class="hint">No MCP servers bound. Bind one or more MCP-providing components in this
         component's <b>Interfaces</b> tab (slot <span class="mono">mcp</span>); their tools then become
         available to the agent.</div>`}
  </div>`;
}

// --- start ------------------------------------------------------------------

paint();
session.start().catch(() => {});
loadMe().then(() => convs.load()).catch(() => {});
loadHalt();
loadNeeds();
// A link to a conversation (#c=<id>) opens it; an invite (#join=…) joins it —
// on load, and when the address changes while the tile is open.
const followHash = () => {
  const m = /(?:^#|&)c=(\d+)/.exec(location.hash);
  if (m && +m[1] !== sel) selectRun(+m[1]);
  else if (location.hash.includes('join=')) join(location.hash);
};
followHash();
addEventListener('hashchange', followHash);
