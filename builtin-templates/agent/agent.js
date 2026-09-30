// agent.js — the control tile's web view. Your conversations (sidebar.js —
// per person, D83; subagents live inside their parent's session), a home
// view of what needs you, the chat of the selected conversation, the render
// pane for render_html output (sandboxed, see frameDoc), the workflow tree
// (workflow.js), the Automations page (automations.js: schedules, watchers),
// and a tabbed settings area (config / features / classes / coding agents /
// memory / files / skills / MCP).
//
// The state lives in model/ (shared with the native view): model/app.js wires
// the Session (chat-view.js adds its lit template), the conversation list and
// the Automations page to one live stream — nothing polls — and says where
// you are; model/rules.js says which controls show, model/actions.js talks to
// the backend. This file draws and wires the DOM. No framework beyond lit's
// render(), no build step; xbin.fetch attributes calls to this element.
// Feature modules draw into it through the seams (web-ext.js: the chat's
// blocks and end, the top bar, each paint, the new-chat dialog) —
// harness-web.js imports them.
import { html, render, nothing } from '/vendor/lit-all.min.js';

const $ = (id) => document.getElementById(id);
// esc() comes from the kit and escapes quotes as well as &<>: its output lands
// in ATTRIBUTE position in the settings tabs (title=, data-*, value=) with
// model-controlled data — memory keys the agent writes, skill names it authors.
import { selfApi as api, jbody, esc } from '/vendor/bx-kit.js';
import { Session } from './chat-view.js';
import { ChatWindow } from './chat-window.js';
import { queueTpl } from './chat-cards.js';
import { sidebarTpl, viewsTpl, makeSideUI } from './sidebar.js';
import { homeTpl } from './home.js';
import { AutoPage, autoPageTpl, sideEntryTpl } from './automations.js';
import './auto-channels.js'; // draws the Channels kind on that page
import './auto-triggers.js'; // …and Triggers
import { openShare } from './share.js';
import { makeClassPicker, classOptionsTpl, tabClasses } from './classes.js';
import { tabHarnesses } from './harness-catalog.js';
import { makeSandboxUI } from './sandboxes.js';
import { createApp } from './model/app.js';
import { HOME } from './model/home.js';
import * as rules from './model/rules.js';
import * as actions from './model/actions.js';
import { liveURL, liveFrame, liveLabel } from './live.js';
import { mountLive, unmountLive } from './live-status.js';
import { makePorts } from './ports.js';
import { tabFiles, selectFile } from './settings-files.js';
import { makeWorkflow } from './workflow.js';
import { ext, ctx as extCtx } from './web-ext.js';
import './harness-web.js'; // the coding harnesses' modules (their hooks on ext)
// Raw-bytes endpoints (a file's bytes, an upload body) go through xbin.fetch
// directly — the kit's api() parses JSON — so they need this backend's prefix
// (model/actions.js rawFile, Attachments.upload).
const base = `/api/${xbin.self}`;
const num = (v) => Number(v) || 0;
const clip = (s, n) => { s = String(s ?? ''); return s.length > n ? s.slice(0, n) + '…' : s; };
const errBox = (e) => `<div class="err">${esc(e && e.message ? e.message : e)}</div>`;

// The model (model/app.js): where you are (app.sel — the selected run id,
// null = home; app.page), who you are (app.me), the class for new asks
// (app.classId), what needs you, the halt switch, the composer's attachments.
// The home view's words are HOME (model/home.js) — an instance that
// specializes the agent (a persona, a domain) changes those and nothing else.
// A long conversation (D130): drafts stream as deltas, the open conversation
// is read in pages of 50 messages, and the timeline renders a window of its
// blocks (chat-window.js), letting go of what lies far from the reader.
const app = createApp({
  Session, AutoPage,
  route: (h) => setHash(h),
  visible: () => document.visibilityState === 'visible',
  deltas: true, page: 50,
});
const { session, convs, autos } = app;
Object.assign(extCtx, { app, paint: () => paint() }); // what the seams' modules share (web-ext.js)
const win = new ChatWindow(session);
globalThis.agentChat = { testApi: () => win.testApi() }; // the UI harness's view of the chat's window
// opening or closing a card keeps the reader's view, even at the bottom
{
  const toggle = session.ui.toggle;
  session.ui.toggle = (id, dflt) => { win.keepView(); toggle(id, dflt); };
}

// The class for NEW asks (D116; app.classId, fixed per run once started —
// the exfiltration firewall is the class's): the composer's picker
// (classes.js), at home. Your pick is kept by the per-user prefs API, NOT
// localStorage: tile frames are sandboxed opaque origins with no localStorage
// at all, and touching it throws — at module scope that kills the whole tile.
const classPicker = makeClassPicker(app, $('cpick'));
// The coding sandbox (D115): #ssel beside the model, the top bar's ▣, the Sandboxes dialog.
const ports = makePorts(app, { openLive: (det) => openLive(det), repaint: () => paint() }); // the popover's Ports section
const sbxUI = makeSandboxUI(app, { sel: $('ssel'), dlg: $('sbxdlg'), repaint: () => paint(), popExtra: ports.tpl });
extCtx.sbxUI = sbxUI; // the seams' modules open its dialog (harness-start.js: Create, prefilled)
let models = [];         // model references from GET /models ({data:[{ref, id, provider}]})
let cfgCache = null;     // last GET /config
let settingsOpen = false;
let activeTab = 'config';
let skillsCache = [];    // skills for the skills tab
let skillSel = null;     // name of the skill being edited (null = new)
const isHtmlPath = (p) => /\.html?$/i.test(p || '');

// --- the session ----------------------------------------------------------
//
// What the model says, the page draws: the chat on change, the list and the
// Automations entry when theirs change. app.page is what the main pane shows
// when no conversation is open: null (home) or 'automations'.

app.on('change', () => paint());
app.on('runs', () => { paintSide(); if (app.sel == null) paint(); });
app.on('list', () => { paintSide(); paint(); }); // the top bar's sharing reads the list row
app.on('autos', () => { paintSide(); if (app.page) paint(); });
app.on('needs', () => { if (app.sel == null) paint(); });
app.on('me', () => { $('gear').hidden = !app.me.manager; syncHalt(); });
app.on('halt', () => syncHalt());
app.on('class', () => classPicker.paint(session.current()));
app.on('model', () => paint());
app.on('harness', () => paint()); // the coding agents' catalog and picks (app.harness)
app.on('attach', () => renderAttach());
app.on('sending', () => {
  $('send').disabled = app.sending;
  if (!app.sending) renderAttach();
  else if (app.sel != null && !win.atBottom) win.latest(); // what you send shows at the end: go there
});
app.on('error', (e) => alert(e.message));
// Opening a conversation closes the workflow tree and a preview of another
// run's file; once it is open the chat starts at its end.
app.on('select', (id) => {
  wf.close();
  if (preview && preview.runId !== id) closePreview();
  prevSeen = null;
  win.reset();
});
app.on('selected', () => {
  win.toBottom();
  paintSide(); paint();
});
app.on('home', () => {
  closePreview(); prevSeen = null; prevDismissed = 0;
  wf.close();
  paintSide(); paint();
});
app.on('page', () => { paintSide(); paint(); });
session.ui.act.openFile = (path) => { selectFile(path); openSettings('files'); };
Object.assign(session.ui.act, { openPreview: (path, ver, run) => openPreview(path, ver, false, run), openLive }); // the 🖼 / 📡 lines (a subagent's: its run)

// --- the conversation list -------------------------------------------------
//
// Your conversations (GET /conversations — the server lists only what you may
// see), kept current by the stream. A subagent is never a row: it lives inside
// its parent's chat and the workflow tree.

const sideUI = makeSideUI({
  convs, api, selectRun: (id) => app.select(id), goHome: () => app.home(), paint: () => paintSide(),
  current: () => session.current(), search: () => $('csearch'), me: () => app.me,
  share: (r) => openShare(r, app.me, () => convs.load()),
});

function paintSide() {
  render(sideEntryTpl(autos, app.page === 'automations', () => app.openAutomations()), $('autos'));
  render(sidebarTpl(convs, sideUI), $('runs'));
  render(viewsTpl(convs, sideUI), $('views'));
  syncHalt();
}

// --- home (no conversation open) -------------------------------------------

const goHome = () => app.home();
const selectRun = (id) => app.select(id);

function homeView() {
  const mcp = xbin.iface && xbin.iface('mcp');
  return homeTpl(HOME, app.needs, {
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

// --- painting -------------------------------------------------------------------

function topTpl(v) {
  if (!v) return app.page === 'automations' ? html`<span class="title">Automations</span>`
    : html`<span class="title">${HOME.title}</span><span class="muted" style="font-size:11.5px">${HOME.tagline}</span>${ext.top(null) || nothing}`;
  const r = v.run;
  const t = rules.topBar(v, convs.find(r.rootId || r.id), app.me);
  return html`${t.crumb ? html`<a class="crumb" @click=${() => app.openAutomations(t.crumb.kind, t.crumb.id)}>Automations ›</a>` : nothing}
    <span class="title" title=${r.title || ''}>${t.title}</span>
    <span class="badge clsbadge" title=${t.cls.title}>${t.cls.label}</span>
    ${t.cls.warn ? html`<span class="badge clswarn" title=${t.cls.warnTitle}>${t.cls.warn}</span>` : nothing}
    ${sbxUI.badgeTpl(v)}
    ${t.model ? html`<span class="badge" title="the model this conversation was switched to (the composer's picker)">✦ ${t.model}</span>` : nothing}
    <span class="badge ${r.status}">${r.status}</span>
    ${t.viewOnly ? html`<span class="badge" title="shared with you to read">view only</span>` : nothing}
    ${t.retry ? html`<button class="btn ghost btnsm" @click=${() => control('resume')} title="Drive the run again">Retry</button>` : nothing}
    ${t.compact ? html`<button class="btn ghost btnsm" @click=${() => control('compact')}>Compact</button>
    <button class="btn ghost btnsm" @click=${() => control('learn')} title="Distill this run into a reusable skill">Learn skill</button>` : nothing}
    <button class="btn ghost btnsm" @click=${() => control('mem')}>Memory (${t.memory})</button>
    <button class="btn ghost btnsm" @click=${() => control('files')} title="This run's session files">Files (${t.files})</button>
    ${t.tree ? html`<span class="badge wfchip" @click=${() => control('wf')} title="open the workflow tree">⑂ tree</span>` : nothing}
    ${ext.top(v) || nothing}
    <button class="btn ghost btnsm sharepill ${t.share.tone}" @click=${() => openShare(t.shareRun, app.me, () => convs.load())}
      title=${t.share.title}>${t.share.icon} ${t.share.label}</button>
    ${t.grants.map((g) => html`<span class="badge grantchip" title=${g.title}>${g.label}${g.revoke
      ? html`<button class="linkbtn" title="stop it now" @click=${() => session.revokeGrant(g.run, g.cap).catch((e) => alert(e.message))}>revoke</button>` : nothing}</span>`)}
    ${t.del ? html`<button class="btn rm btnsm" @click=${() => control('delete')}>Delete</button>` : nothing}
    ${taskTpl(v)}`;
}

// The pinned task (D133): the current request — the latest one it was given
// — folded to a line under the controls; unfolded (the toggle, "+N"), every
// request — the task ledger (GET /runs/{id}/asks), read-only.
let taskOpen = 0;    // the run whose task is unfolded
let taskAsks = null; // {run, count, list, err}: its ledger, read when unfolded
function taskTpl(v) {
  const p = rules.pinnedTask(v);
  if (!p) return nothing;
  const id = v.run.id, open = taskOpen === id;
  if (open && !(taskAsks && taskAsks.run === id && taskAsks.count === p.count)) {
    const mine = taskAsks = { run: id, count: p.count, list: null, err: '' };
    actions.asks(id).then((list) => { mine.list = list; }).catch((e) => { mine.err = e.message; }).finally(() => paint());
  }
  const toggle = () => { taskOpen = open ? 0 : id; paint(); };
  return html`<div class="taskpin ${open ? 'open' : ''}">
    <button class="tasktoggle" @click=${toggle} title=${open ? 'fold the task'
      : 'the task, pinned: every request this conversation was given, verbatim — the agent always sees them'}>📌 Task${p.more ? ` (+${p.more})` : ''} ${open ? '▾' : '▸'}</button>
    ${open ? html`<div class="asks">${taskAsks.err ? html`<span class="err">${taskAsks.err}</span>`
      : !taskAsks.list ? html`<span class="muted">loading…</span>`
      : taskAsks.list.map((a) => html`<div class="taskreq"><div class="askhead">#${a.seq} · ${rules.askFrom(a)} · ${new Date(a.at * 1000).toLocaleString()}${a.live ? '' : ' · compacted (the agent sees it pinned)'}</div>
        <div class="asktext">${a.text}</div></div>`)}</div>`
    : html`<span class="taskline" title=${p.text}>${p.line}</span>`}
  </div>`;
}

// paint draws everything that depends on the session. lit patches only what
// changed, and the chat renders a window of its blocks (chat-window.js:
// measured right before the render, corrected right after, so what the
// reader looks at never moves) — cheap enough to run on every streamed token.
let shownPage = '';
function paint() {
  const v = session.current();
  render(topTpl(v), $('top'));
  const tl = $('timeline');
  if (v) {
    win.attach(tl);
    const s = session.shown();
    render(session.template(win.place(s), s), tl);
    win.after();
  } else {
    win.detach();
    render(app.page === 'automations' ? autoPageTpl(autos) : homeView(), tl);
  }
  // a page opens at its top
  const shown = v ? '' : `${app.page}:${autos.open ? autos.open.kind + autos.open.id : ''}:${!!(autos.form || autos.custom)}`;
  if (!v && shown !== shownPage) tl.scrollTop = 0;
  shownPage = shown;
  render(queueTpl(v ? session.queued() : [], (iid) => session.removeQueued(iid).catch((e) => alert(e.message))), $('queue'));
  $('queue').hidden = !(v && session.queued().length);
  const c = rules.composer(v, HOME);
  classPicker.paint(v);
  syncModelPicker(v);
  sbxUI.paint();
  $('stop').hidden = !c.stop;
  $('msg').disabled = c.disabled;
  $('msg').placeholder = c.placeholder;
  if (v) syncPreview(v);
  if (wf.shown) wf.dirty();
  ext.paint(v);
}

// The composer's model (model/rules.js modelPicker): the open conversation's,
// from its next turn — or, at home, the next new chat's; your last pick is
// your default. Grouped by provider when several are bound (D111).
function modelOptsTpl(p) {
  const opt = (o) => html`<option value=${o.value} ?selected=${o.value === p.value}>${o.label}</option>`;
  if (!p.groups.length) return html`${p.options.map(opt)}`;
  return html`${p.options.filter((o) => !o.group).map(opt)}${p.groups.map((g) => html`<optgroup label=${g.label}>
    ${p.options.filter((o) => o.group === g.path).map(opt)}</optgroup>`)}`;
}
function syncModelPicker(v) {
  const p = rules.modelPicker(v, app.model, app.catalog);
  const el = $('msel');
  el.hidden = !p.shown;
  el.disabled = p.disabled;
  el.title = p.title;
  render(modelOptsTpl(p), el);
  if (el.value !== p.value) el.value = p.value;
}
$('msel').onchange = () => app.pickModel($('msel').value).catch((e) => { alert(e.message); paint(); });

// --- workflow view ------------------------------------------------------
//
// The tree of the conversation's runs (workflow.js), from the top bar's ⑂.

const wf = makeWorkflow({ selectRun });

// app.me is who the tile is talking for (GET /me, D83): settings, the brake
// and oversight are the managers' — people with write access to the tile.
// syncHalt draws the brake (model/rules.js says when it shows).
function syncHalt() {
  const b = $('halt');
  const h = rules.halt(app.me, app.halted, convs.all());
  b.hidden = !h.shown;
  b.textContent = h.label;
  b.title = h.title;
  b.dataset.on = app.halted ? '1' : '';
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
  dropLive();
  // prevSig and .srcdoc stay put, so reopening the same file is instant.
}

// dropLive ends a live page (its scripts and connections go with its
// frame) and puts the static pane's controls back.
function dropLive() {
  unmountLive();
  $('livefr')?.remove();
  $('prevframe').hidden = false;
  $('prev-src').hidden = false;
  $('prev-reload').hidden = true;
  $('prev-icon').textContent = '🖼';
}

// openLive shows a preview_port step's page, live (live.js); run: the step's
// (a subagent's, shown in its parent), else the open one.
async function openLive(det, run) {
  if (app.sel == null) return;
  const p = preview = { runId: app.sel, run: run || app.sel, kind: 'live', det, live: true };
  dropLive();
  $('preview').hidden = false;
  $('prevframe').hidden = true;
  $('prev-src').hidden = true;
  $('prev-reload').hidden = false;
  $('prev-icon').textContent = '📡';
  $('prev-path').textContent = $('prev-path').title = liveLabel(det);
  $('prev-ver').textContent = '';
  $('prev-warn').hidden = true;
  if (win.atBottom) win.toBottom();
  let src;
  try { src = await liveURL(p.run, det); } catch (e) {
    if (preview !== p) return;
    $('prev-warn').hidden = false;
    $('prev-warn').textContent = '⚠ ' + (e.message || e);
    return;
  }
  if (preview !== p) return; // closed or replaced meanwhile
  $('livefr')?.remove();
  const f = liveFrame(src);
  $('prevframe').after(f);
  mountLive($('preview'), f, src); // the status strip: what the page's URL answers, and Check
}

async function openPreview(path, ver, live, run) {
  if (app.sel == null || !path) return;
  dropLive();
  preview = { runId: app.sel, run: run || app.sel, path, ver: num(ver), live: !!live };
  $('preview').hidden = false;
  $('prev-path').textContent = path;
  $('prev-path').title = path;
  // The pane just took height from the timeline: a chat that followed its
  // end still does.
  if (win.atBottom) win.toBottom();
  await paintPreview();
}

// paintPreview is the ONLY writer of .srcdoc, and it writes only when the
// (run, path, version) triple changes: assigning srcdoc reloads the frame — a
// white flash and a lost scroll position — so this gate is what stops the 1.5s
// poll from thrashing it.
async function paintPreview() {
  const p = preview;
  if (!p || p.kind === 'live') return;
  const sig = `${p.run}\u0000${p.path}\u0000${p.ver}`;
  if (sig === prevSig) return;
  let f;
  try {
    f = await actions.file(p.run, p.path);
  } catch (e) {
    $('prev-warn').hidden = false;
    $('prev-warn').textContent = '⚠ ' + (e.message || e);
    return;
  }
  if (!preview || preview.path !== p.path || preview.run !== p.run) return; // stale
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
  if (preview && preview.runId !== app.sel) closePreview();
  const rs = (d.steps || []).filter((s) => s.kind === 'render' || s.kind === 'live');
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
  if (settingsOpen || wf.shown) return;            // don't yank an open view away
  if (document.visibilityState !== 'visible') return;
  if (preview && !preview.live) return;            // the user pinned an older chip
  if (last.kind === 'live') return openLive(det);  // preview_port (live.js, D135)
  openPreview(det.path, num(det.version), true);
}

const rawBlob = (run, path) => actions.rawFile(base, run, path);

// refreshView re-reads the selected run's state after an edit the stream does
// not carry (memory blocks, session files); the transcript held stays.
function refreshView() {
  if (app.sel != null) session.refresh(app.sel).then(paint).catch(() => {});
}

async function control(action) {
  if (action === 'mem') return openSettings('memory');
  if (action === 'files') return openSettings('files');
  if (action === 'wf') { const v = session.current(); return wf.open(v ? (v.run.rootId || v.run.id) : app.sel); }
  if (action === 'delete') {
    if (!confirm('Delete this run and its history?')) return;
    try { await actions.deleteRun(app.sel); } catch (e) { return alert(e.message); }
    if (settingsOpen && activeTab === 'memory') renderTab();
    session.runs.delete(app.sel);
    return goHome();
  }
  // resume | compact | learn → POST /runs/{id}/{action}
  try { await actions.control(app.sel, action); } catch (e) { alert(e.message); }
}

// --- composer -----------------------------------------------------------

// Attachments waiting to be sent (app.attach, model/actions.js): uploaded into
// the run's session files and then named in the message. These are their chips.
const { fmtBytes } = actions;
const addFiles = (list) => app.attach.add(list);

function renderAttach() {
  const host = $('attach');
  const attachments = app.attach.items;
  host.hidden = attachments.length === 0;
  host.innerHTML = attachments.map((a) => `<span class="chip ${a.state || ''}" title="${esc(a.err || a.type || '')}">
    <span class="nm">${esc(a.name)}</span><span class="sz">${a.state === 'up' ? 'uploading…' : a.err ? esc(a.err) : fmtBytes(a.size)}</span>
    <button data-rm="${a.key}" title="remove" ${app.sending ? 'disabled' : ''}>✕</button></span>`).join('');
  host.querySelectorAll('[data-rm]').forEach((b) => b.onclick = () => app.attach.remove(+b.dataset.rm));
}

// send: at home a fresh ask that opens with its streaming answer; in a
// conversation a message — while the run works it is queued and delivered at
// its next step (the strip above the composer shows it until then). The text
// box empties once the text is on its way (app.send, model/app.js).
const send = () => app.send($('msg').value, () => { $('msg').value = ''; autosize(); });
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
    const text = await app.stop();
    if (text) { $('msg').value = [text, $('msg').value].filter(Boolean).join('\n\n'); autosize(); $('msg').focus(); }
  } catch (e) { alert(e.message); }
};

// One click, no confirm — during a runaway every dialog is another second of
// spend. The undo is the same button.
$('halt').onclick = async () => {
  try { await app.setHalt(!app.halted); } catch (e) { return alert(e.message); }
  if (wf.shown) wf.load();
};
// Render pane header. Closing remembers WHICH render was dismissed, so the
// poll doesn't immediately reopen the same one.
$('prev-close').onclick = () => { prevDismissed = prevSeen; closePreview(); };
$('prev-max').onclick = () => {
  $('main').classList.toggle('prev-max');
  if (win.atBottom) win.toBottom();
};
$('prev-reload').onclick = () => { if (preview?.kind === 'live') openLive(preview.det, preview.run); };
$('prev-src').onclick = () => {
  if (!preview) return;
  selectFile(preview.path);
  openSettings('files');
};
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape' || $('newdlg').open || $('sbxdlg').open || settingsOpen) return;
  if (classPicker.open) { classPicker.close(); return; }
  if (sbxUI.closePop()) return;
  if (preview) { prevDismissed = prevSeen; closePreview(); return; }
  if (wf.shown) wf.close();
});
$('home').onclick = goHome;

// --- new chat ------------------------------------------------------------

$('new').onclick = () => { goHome(); $('msg').focus(); };
// "New chat with options": a title, a system prompt, a class — the first
// message is the dialog's text; the seams' fields (ext.newChat) add theirs.
let newExt = [];
const drawNewExt = () => render(newExt.map((x) => x.tpl()), $('n-ext'));
$('newopts').onclick = () => {
  $('n-goal').value = ''; $('n-title').value = ''; $('n-system').value = '';
  render(classOptionsTpl(app, app.classId), $('n-class'));
  $('n-class').value = app.classId;
  newExt = ext.newChat(drawNewExt) || [];
  drawNewExt();
  $('newdlg').showModal();
};
$('n-create').onclick = async (e) => {
  const text = $('n-goal').value.trim();
  if (!text) { e.preventDefault(); return; }
  try {
    await app.ask(Object.assign({ text, title: $('n-title').value.trim(), system: $('n-system').value.trim(), class: $('n-class').value },
      ...newExt.map((x) => (x.body ? x.body() : {}))));
  } catch (err) { alert(err.message); }
};
let searchT = null;
$('csearch').oninput = () => {
  clearTimeout(searchT);
  const v = $('csearch').value;
  if (v.includes('#join=')) { $('csearch').value = ''; app.join(v); return; } // a pasted invite link
  searchT = setTimeout(() => convs.search(v).catch(() => {}), 200);
};

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

// what the Files tab (settings-files.js) needs of the page
const filesCtx = { app, $, rawBlob, closeSettings, openPreview, closePreview, refreshView, get preview() { return preview; } };
async function renderTab() {
  const bd = $('sbd');
  const fns = { config: tabConfig, features: tabFeatures, classes: (b) => tabClasses(b, app), harnesses: (b) => tabHarnesses(b, app), memory: tabMemory, files: (b) => tabFiles(b, filesCtx), skills: tabSkills, mcp: tabMcp };
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
  await app.loadModels();
  models = ((app.catalog && app.catalog.data) || []).map((x) => x.ref || x.id).filter(Boolean);
}

// Config tab: model tiers + system prompt + limits + behavior. Saves the FULL
// merged config (preserving features/mcp/legacy model) via PUT /config.
async function tabConfig(bd) {
  const c = await actions.getConfig();
  cfgCache = c;
  await ensureModels(true);
  const m = c.models || {};
  const opt = (v) => `<option value="">— the provider's default —</option>` +
    models.map((id) => `<option ${id === v ? 'selected' : ''}>${esc(id)}</option>`).join('');
  const provs = ((app.catalog && app.catalog.providers) || []).map((p) =>
    `<span class="mono">${esc(p.path)}</span> ${p.ok ? '✓' : `✗ <span class="err">${esc(p.error || 'unreachable')}</span>`}${p.legacy ? ' (by name — bind the llm interface)' : ''}`).join(' · ');
  bd.innerHTML = `
    <div class="sec"><h4>Model tiers</h4>
      <div class="grid4">
        <div class="field"><label>General</label><select id="cf-general">${opt(m.general)}</select></div>
        <div class="field"><label>Code</label><select id="cf-code">${opt(m.code)}</select></div>
        <div class="field"><label>Memory</label><select id="cf-memory">${opt(m.memory)}</select></div>
        <div class="field"><label>Vision (VLM)</label><select id="cf-vlm">${opt(m.vlm)}</select></div>
      </div>
      <div class="hint">Empty tier = the provider's preferred model for that job (llm-gw's per-use default).${models.length ? '' : ' (no models listed — bind the agent\'s llm interface to a provider that has a backend)'}</div>
      ${provs ? `<div class="hint">Models from: ${provs}</div>` : ''}
    </div>
    <div class="sec"><h4>Base system prompt</h4><textarea id="cf-system" rows="5">${esc(c.system || '')}</textarea></div>
    <div class="sec"><h4>Limits</h4><div class="grid4">
      <div class="field"><label title="the prompt size that starts compaction; 0 (or the old default 12000) = 60% of the model's context window, at least 32000">Token budget (0 = from the model)</label><input id="cf-budget" type="number" value="${num(c.tokenBudget)}"></div>
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
      await actions.saveConfig(next); cfgCache = next;
      $('cf-msg').textContent = 'saved ✓';
      setTimeout(() => { const e = $('cf-msg'); if (e) e.textContent = ''; }, 1500);
    } catch (e) { $('cf-msg').textContent = e.message; }
  };
}

// Features tab: a checkbox per capability. Toggling fetches the current config,
// merges {features:{...}}, and PUTs it back.
async function tabFeatures(bd) {
  const f = await actions.features();
  const keys = f.keys || [];
  const st = f.features || {};
  const desc = {
    recall: 'FTS recall over turns compacted out of the window',
    skills: 'skill-library tools + the injected skills list',
    streaming: 'stream partial assistant text (the live draft)',
    vision: 'send images to the VLM tier',
    parallelTools: "run a turn's tool calls in parallel",
    watcher: 'watcher cron-agents (one persistent run, discard no-change rounds)',
    threads: "thread & schedule tools: list and read this conversation's automations and threads — and, with the owner's OK, their other conversations",
  };
  bd.innerHTML = `<div class="sec"><h4>Features</h4>
    ${keys.map((k) => `<label class="chk"><input type="checkbox" data-f="${esc(k)}" ${st[k] ? 'checked' : ''}>
      <b>${esc(k)}</b> <span class="muted" style="font-weight:400">${esc(desc[k] || '')}</span></label>`).join('')}
    <div class="hint">Each toggle merges into the agent's default config.</div></div>`;
  bd.querySelectorAll('[data-f]').forEach((b) => b.onchange = async () => {
    try { cfgCache = await actions.setFeature(b.dataset.f, b.checked); } catch (e) { alert(e.message); }
    tabFeatures(bd);
  });
}

// Memory tab: the SELECTED run's memory blocks (key→value): edit/add/delete.
async function tabMemory(bd) {
  if (app.sel == null) { bd.innerHTML = '<div class="empty">select a run to edit its memory blocks</div>'; return; }
  const entries = Object.entries(await actions.memory(app.sel));
  const keys = entries.map((e) => e[0]);
  bd.innerHTML = `<div class="sec"><h4>Memory · run ${app.sel}</h4>
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
    try { await actions.setMemory(app.sel, keys[i], bd.querySelector(`[data-v="${i}"]`).value); }
    catch (e) { return alert(e.message); }
    tabMemory(bd); refreshView();
  });
  bd.querySelectorAll('[data-del]').forEach((b) => b.onclick = async () => {
    const i = +b.dataset.del;
    try { await actions.deleteMemory(app.sel, keys[i]); }
    catch (e) { return alert(e.message); }
    tabMemory(bd); refreshView();
  });
  $('madd').onclick = async () => {
    const k = $('mk').value.trim();
    if (!k) return;
    try { await actions.setMemory(app.sel, k, $('mv').value); }
    catch (e) { return alert(e.message); }
    tabMemory(bd); refreshView();
  };
}

// Schedules tab: cron-agents — list with enable/disable, run-now, delete, and a
// create form. A bad cron expression comes back as a 400 error we surface.
// Skills tab: the self-authored skill library — list, view/edit, save, delete.
async function tabSkills(bd) {
  skillsCache = await actions.skills();
  const cur = skillSel != null ? skillsCache.find((s) => s.name === skillSel) : null;
  bd.innerHTML = `
    <div class="sec"><h4>Skills</h4>
      <table class="tbl"><tr><th>name</th><th>description</th><th>updated</th><th></th></tr>
      ${skillsCache.length ? skillsCache.map((s, i) => `<tr>
        <td class="mono">${esc(s.name)}</td>
        <td class="muted">${esc(clip(s.description, 80))}${s.owner ? ` <span class="badge" title="learned in their conversation — only their runs see it">${esc(s.owner)}'s</span>` : ''}${s.lane ? ` <span class="badge" title="only runs in this tool mode see it">${s.lane === 'web' ? 'web' : 'internal'}</span>` : ''}</td>
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
    try { await actions.deleteSkill(s.name); } catch (e) { return alert(e.message); }
    if (skillSel === s.name) skillSel = null;
    tabSkills(bd);
  });
  if ($('sk-new')) $('sk-new').onclick = () => { skillSel = null; tabSkills(bd); };
  $('sk-save').onclick = async () => {
    const name = $('sk-name').value.trim();
    $('sk-err').textContent = '';
    if (!name || !$('sk-content').value.trim()) { $('sk-err').textContent = 'need a name and content'; return; }
    try {
      await actions.saveSkill({ name, description: $('sk-desc').value.trim(), content: $('sk-content').value });
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
app.start();
// A link to a conversation (#c=<id>) opens it; an invite (#join=…) joins it;
// #auto[=kind:id] opens the Automations page — on load, and when the address
// changes while the tile is open (model/router.js).
const followHash = () => { app.follow(location.hash); };
followHash();
addEventListener('hashchange', followHash);
