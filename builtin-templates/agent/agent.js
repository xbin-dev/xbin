// agent.js — the control tile logic. Polls the backend and renders a run list
// plus a live, interleaved timeline (transcript messages + the journal of LLM
// calls / tool calls / compactions / yields), streams the in-flight assistant
// draft while a run is running, and wires the steering controls, the render
// pane for render_html output (sandboxed, see frameDoc), and a tabbed settings
// area (config / features / memory / files / schedules / skills / MCP).
// Vanilla ES module (no framework, no build step — like the rest of this tile);
// xbin.fetch attributes calls to this element (self → admin of its own backend).
import { marked } from '/vendor/marked.esm.js';

const $ = (id) => document.getElementById(id);
// esc() comes from the kit and escapes quotes as well as &<>: its output lands
// in ATTRIBUTE position all over this file (title=, data-*, value=) with
// model-controlled data — tool-call arguments in each call's title (JSON, so
// always full of quotes), link titles from the markdown renderer, memory keys
// the agent writes itself, skill names it authors.
import { selfApi as api, jbody, esc } from '/vendor/bx-kit.js';
const num = (v) => Number(v) || 0;
const clip = (s, n) => { s = String(s ?? ''); return s.length > n ? s.slice(0, n) + '…' : s; };

// Assistant text renders as markdown, sanitized: raw HTML tokens are shown
// escaped (model output is untrusted — an injected <script>/<img> must never
// execute with this tile's frame token), links get safe schemes + a new tab,
// and images render as their source text RATHER THAN LOADING. That last one is
// load-bearing, not belt-and-braces: the platform CSP on /c/ documents is only
// `sandbox allow-scripts allow-forms allow-modals allow-downloads` — there is
// no img-src, no connect-src, nothing stopping a subresource fetch. A model
// -authored <img src="https://…/?leak=…"> would be a live exfiltration beacon,
// so the renderer never emits <img> at all. Everything else is HTML our
// renderer produced from markdown structure. Streaming-tolerant: a parse error
// falls back to escaped text.
marked.use({
  breaks: true,
  renderer: {
    html({ text }) { return esc(text); },
    image({ text, href }) { return `<span class="muted">[image: ${esc(text || href || '')}]</span>`; },
    link({ href, title, tokens }) {
      const h = String(href || '').trim();
      const inner = this.parser.parseInline(tokens);
      if (/^(javascript|data|vbscript):/i.test(h)) return inner;
      return `<a href="${esc(h)}" target="_blank" rel="noopener noreferrer"${title ? ` title="${esc(title)}"` : ''}>${inner}</a>`;
    },
  },
});
const md = (s) => { try { return marked.parse(String(s ?? '')); } catch { return esc(s); } };
// Group digits for readability: 123123 → "123 123" (narrow no-break space).
const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');
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
  tagline: 'quick asks · tasks · cron-agents',
  hi: 'What do you need?',
  sub: 'Ask below — a quick question gets its own run and its answer shows up here; bigger jobs go in a + Task; recurring ones become cron-agents.',
  examples: [
    'What can you do in this workspace?',
    'Every morning at 8, check…',
    'Call apps/… and summarize what it returns',
  ],
  placeholder: 'ask anything…',
};
let lastDetailKey = '';  // cheap change-detection for the timeline
let lastHomeKey = '';    // change-detection for the home view
let runsCache = [];      // last GET /runs
let detail = null;       // last GET /runs/{id} payload
let models = [];         // model ids from GET /models ({data:[{id}]})
let cfgCache = null;     // last GET /config
let settingsOpen = false;
let activeTab = 'config';
let schedCache = [];     // schedules for the schedules tab (handler lookup by index)
let skillsCache = [];    // skills for the skills tab
let skillSel = null;     // name of the skill being edited (null = new)
let filesCache = [];     // session files for the files tab (lookup by index)
let filesSel = null;     // path of the file being edited (null = new)
const isHtmlPath = (p) => /\.html?$/i.test(p || '');

// --- runs list ----------------------------------------------------------

async function loadRuns() {
  let runs;
  try { runs = await api('/runs'); } catch { return; }
  runsCache = runs || [];
  // The sidebar lists TASKS (and their subagents): quick asks live on the
  // home view — except the one you have open.
  const tasks = runsCache.filter((r) => r.kind !== 'quick' || r.id === sel);
  const host = $('runs');
  host.innerHTML = '';
  if (!tasks.length) host.innerHTML = '<div class="empty">no tasks yet</div>';
  for (const run of tasks) {
    const el = document.createElement('div');
    el.className = 'run' + (run.id === sel ? ' on' : '');
    const sub = run.parentId ? '↳ subagent · ' : (run.kind === 'quick' ? '⚡ ' : '');
    el.innerHTML = `<div class="t">${esc(run.title || 'run ' + run.id)}</div>
      <div class="m">${sub}<span class="badge ${esc(run.status)}">${esc(run.status)}</span></div>`;
    el.onclick = () => { sel = run.id; lastDetailKey = ''; loadRuns(); loadDetail(); };
    host.append(el);
  }
  if (sel == null) renderHome();
}

// --- home (no run selected) ----------------------------------------------

function goHome() {
  sel = null; detail = null; lastDetailKey = ''; lastHomeKey = '';
  closePreview(); prevSeen = null; prevDismissed = 0;
  loadRuns(); renderHome();
}

function renderHome() {
  const quick = runsCache.filter((r) => r.kind === 'quick' && !r.parentId).slice(0, 12);
  const key = JSON.stringify(quick.map((r) => [r.id, r.status, r.updated]));
  if (key === lastHomeKey) return;
  lastHomeKey = key;

  $('top').innerHTML = `<span class="title">${esc(HOME.title)}</span><span class="muted" style="font-size:11.5px">${esc(HOME.tagline)}</span>`;
  const ago = (t) => {
    const s = Math.max(0, Date.now() / 1000 - t);
    if (s < 90) return 'now';
    if (s < 5400) return `${Math.round(s / 60)}m`;
    if (s < 129600) return `${Math.round(s / 3600)}h`;
    return `${Math.round(s / 86400)}d`;
  };
  // A plain reply leaves result empty, so the card falls back to the run's
  // last assistant message (GET /runs decorates quick asks with it).
  const answerOf = (r) => {
    if (r.status === 'done') return r.result || r.last || '';
    if (r.status === 'waiting_input') return '❓ ' + (r.result || 'asking you something — open to answer');
    if (r.status === 'running') return '…working';
    if (r.status === 'error') return '⚠ ' + (r.result || 'error');
    return r.result || r.last || '';
  };
  const mcp = xbin.iface && xbin.iface('mcp');
  $('timeline').innerHTML = `<div class="home">
    <div class="hi">${esc(HOME.hi)}</div>
    <div class="sub">${esc(HOME.sub)}${(mcp && (mcp.endpoints || []).length) ? '' : ' No MCP servers are bound yet — see ⚙ → MCP.'}</div>
    <div class="exs">${HOME.examples.map((e) => `<span class="ex">${esc(e)}</span>`).join('')}</div>
    ${quick.length ? `<h5>Recent quick asks</h5>` + quick.map((r) => `
      <div class="qa" data-r="${num(r.id)}">
        <div class="q">⚡ ${esc(r.title)}<span class="badge ${esc(r.status)}">${esc(r.status)}</span><span class="when">${ago(r.updated)}</span></div>
        ${answerOf(r) ? `<div class="a">${esc(clip(answerOf(r), 400))}</div>` : ''}
      </div>`).join('') : '<div class="hint">no quick asks yet — type one below</div>'}
  </div>`;
  $('timeline').querySelectorAll('.ex').forEach((el) => el.onclick = () => { $('msg').value = el.textContent; $('msg').focus(); });
  $('timeline').querySelectorAll('[data-r]').forEach((el) => el.onclick = () => {
    sel = +el.dataset.r; lastDetailKey = ''; loadRuns(); loadDetail();
  });
  $('msg').placeholder = HOME.placeholder;
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

// syncPreview follows the run's newest render step. Called from loadDetail on
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
  if (settingsOpen) return;                        // don't yank an open tab away
  if (document.visibilityState !== 'visible') return;
  if (preview && !preview.live) return;            // the user pinned an older chip
  openPreview(det.path, num(det.version), true);
}

// --- selected run -------------------------------------------------------

function eventStream(d) {
  // Merge transcript messages and the "meta" journal steps into one time-
  // ordered stream. Tool activity (tool_call/tool_result) is shown via the
  // transcript messages (assistant tool chips + tool-role results), and asks
  // via the actionable footer, so those step kinds are omitted here to avoid
  // duplicating them — renderStep still handles every kind for robustness.
  const metaKinds = new Set(['llm_call', 'compaction', 'yield', 'state_changed', 'finish', 'error', 'note', 'render']);
  const evs = [];
  for (const m of d.messages || []) {
    if (m.role === 'system') continue;
    evs.push({ t: m.created, order: m.seq, kind: 'msg', m });
  }
  for (const s of d.steps || []) {
    if (metaKinds.has(s.kind)) evs.push({ t: s.created, order: 1000 + s.seq, kind: 'step', s });
  }
  evs.sort((a, b) => (a.t - b.t) || (a.order - b.order));
  return evs;
}

// Tool calls and results render collapsed to 1-2 lines (click to expand).
// Open state lives in a session-level set keyed by message id, so it survives
// the timeline's innerHTML rebuilds — a native <details> would snap shut on
// every 1.5s poll that changes anything, which is exactly while you are
// reading a running agent. The backend's capToolResult separately bounds what
// the LLM context keeps; this is display only.
const expanded = new Set();

function renderMsg(m) {
  let calls = '';
  if (m.toolCalls) {
    try {
      calls = JSON.parse(m.toolCalls).map((tc, i) => {
        const k = `c${m.id}:${i}`;
        return `<div class="tc xwrap ${expanded.has(k) ? 'on' : ''}" data-x="${k}" title="click to expand / collapse">→ ${esc(tc.function.name)}<span class="xargs">(${esc(tc.function.arguments || '')})</span></div>`;
      }).join('');
    } catch { /* ignore */ }
  }
  if (m.role === 'tool') {
    const k = `t${m.id}`;
    const c = m.content || '';
    const long = c.length > 160 || (c.match(/\n/g) || []).length > 1;
    // runOneTool prefixes every failure with "error: ", so flagging it here
    // makes a failed call visible inside the 2-line clamp instead of hiding
    // behind a click.
    const bad = /^error:/.test(c);
    return `<div class="ev tool xwrap ${bad ? 'bad ' : ''}${expanded.has(k) ? 'on' : ''}" data-tool="${esc(m.name)}">
      <div class="role xtoggle" data-x="${k}" title="click to expand / collapse">tool · ${esc(m.name)}${long ? ` <span class="xhint">· ${fmtN(c.length)} chars</span>` : ''}</div>
      ${c ? `<div class="body clampable">${esc(c)}</div>` : ''}</div>`;
  }
  const body = m.content
    ? (m.role === 'assistant' ? `<div class="body md">${md(m.content)}</div>` : `<div class="body">${esc(m.content)}</div>`)
    : '';
  return `<div class="ev ${esc(m.role)}"><div class="role">${esc(m.role)}</div>
    ${body}${calls}</div>`;
}

// wireExpanders makes [data-x] elements toggle their .xwrap in place (no
// refetch) while recording state for the next rebuild.
function wireExpanders(host) {
  host.querySelectorAll('[data-x]').forEach((el) => el.onclick = (e) => {
    e.stopPropagation();
    const k = el.dataset.x;
    const wrap = el.classList.contains('xwrap') ? el : el.closest('.xwrap');
    const on = !expanded.has(k);
    if (on) expanded.add(k); else expanded.delete(k);
    if (wrap) wrap.classList.toggle('on', on);
  });
}

function renderStep(s) {
  let d = {};
  try { d = JSON.parse(s.detail); } catch { d = { text: s.detail }; }
  let g = '◆', txt = '';
  switch (s.kind) {
    case 'llm_call':
      g = '🧠';
      txt = `${esc(d.model || 'model')} · ${d.latencyMs || 0}ms · in ${fmtN(d.promptTokens)} / out ${fmtN(d.completionTokens)} tok · ${d.toolCalls || 0} tool call(s)${d.finishReason ? ` · ${esc(d.finishReason)}` : ''}`;
      break;
    case 'tool_call':
      g = '🔧';
      txt = `${esc(d.name || '')}(${esc(typeof d.args === 'string' ? d.args : JSON.stringify(d.args || {}))})`;
      break;
    case 'tool_result':
      g = '↩';
      txt = `${esc(d.name || '')} → ${esc(clip(d.result || '', 300))}`;
      break;
    case 'compaction':
      g = '🗜';
      txt = `compacted ${d.messages || 0} message(s) → summary${d.summaryTokens ? ` (${fmtN(d.summaryTokens)} tok)` : ''}`;
      break;
    case 'yield':
      g = '⏸';
      txt = `yield${d.seconds != null ? ` ${d.seconds}s` : ''}${d.reason ? ` · ${esc(d.reason)}` : ''}`;
      break;
    case 'ask':
      g = d.kind === 'approval' ? '🛂' : '❓';
      txt = d.kind === 'approval'
        ? `approval requested${(d.tools || []).length ? ` · ${esc((d.tools || []).join(', '))}` : ''}`
        : `asked: ${esc(d.question || '')}`;
      break;
    case 'state_changed':
      g = '✳';
      txt = `state changed${d.summary ? ` · ${esc(d.summary)}` : ''}`;
      break;
    case 'finish':
      g = '✓';
      txt = `finished${d.result ? `: ${esc(d.result)}` : ''}`;
      break;
    case 'error':
      g = '⚠';
      txt = esc(d.error || d.text || '');
      break;
    case 'render': {
      g = '🖼';
      const on = preview && preview.path === d.path;
      txt = `<span class="rchip${on ? ' on' : ''}" data-render="${esc(d.path || '')}" data-rv="${num(d.version)}"
              title="show this file in the render pane">${esc(d.path || '')}</span>` +
            `<span class="muted"> · v${num(d.version)} · ${fmtN(d.bytes)} B</span>`;
      break;
    }
    default: // note
      g = '•';
      txt = esc(d.text || s.detail || '');
  }
  const cls = s.kind === 'error' ? ' err' : '';
  return `<div class="ev step${cls}"><div class="body"><span class="step-k">${g} ${esc(s.kind)}</span>${txt}</div></div>`;
}

// The parked action stored on a run: {kind:"approval"|"ask", toolCalls?}.
function pendingOf(run) {
  try { return JSON.parse(run.pending || '{}') || {}; } catch { return {}; }
}

async function loadDetail() {
  if (sel == null) return;
  let d;
  try { d = await api(`/runs/${sel}`); } catch { return; }
  if (sel !== d.run.id) return; // stale response after navigation
  detail = d;
  // Outside the change-key guard below: a run switch must always reset the
  // render pane, even when the timeline itself has nothing new to draw.
  syncPreview(d);
  const run = d.run;
  const pend = pendingOf(run);
  const isApproval = run.status === 'waiting_input' && pend.kind === 'approval';

  // Top bar + controls. The lane badge shows the run's immutable toolset.
  const lane = (d.config && d.config.toolset) === 'web' ? '🌐 web' : '🔒 private';
  $('top').innerHTML = `<span class="title">${esc(run.title || 'run ' + run.id)}</span>
    <span class="badge" title="tool mode (immutable for this run)">${lane}</span>
    <span class="badge ${esc(run.status)}">${esc(run.status)}</span>
    <button class="btn ghost btnsm" data-a="resume">Resume</button>
    <button class="btn ghost btnsm" data-a="interrupt">Interrupt</button>
    <button class="btn ghost btnsm" data-a="compact">Compact</button>
    <button class="btn ghost btnsm" data-a="learn" title="Distill this run into a reusable skill">Learn skill</button>
    <button class="btn ghost btnsm" data-a="mem">Memory (${Object.keys(d.memory || {}).length})</button>
    <button class="btn ghost btnsm" data-a="files" title="This run's session files">Files (${(d.files || []).length})</button>
    <button class="btn rm btnsm" data-a="delete">Delete</button>`;
  $('top').querySelectorAll('[data-a]').forEach((b) => b.onclick = () => control(b.dataset.a));

  // Timeline. Include the live draft + status so streaming re-renders.
  const key = JSON.stringify([run.status, run.updated, (d.messages || []).length, (d.steps || []).length, d.draft || '']);
  if (key !== lastDetailKey) {
    lastDetailKey = key;
    const evs = eventStream(d);
    let html = evs.map((e) => e.kind === 'msg' ? renderMsg(e.m) : renderStep(e.s)).join('');

    // Live streaming partial assistant text while running (markdown too —
    // a mid-fence partial parse just renders literally until the fence
    // closes, which reads better than a wall of raw markdown).
    if (run.status === 'running') {
      html += d.draft
        ? `<div class="draft"><div class="role">assistant · streaming</div><div class="body md">${md(d.draft)}<span class="cur"></span></div></div>`
        : `<div class="draft"><div class="role">assistant · thinking<span class="cur"></span></div></div>`;
    }

    // Actionable footer: approve/deny, or the ask to answer below.
    if (isApproval) {
      const tools = (pend.toolCalls || []).map((tc) => tc.function && tc.function.name).filter(Boolean);
      html += `<div class="ask"><b>Approval needed</b>${tools.length ? ` for: <span class="mono">${esc(tools.join(', '))}</span>` : ' for a tool call.'}
        <div style="margin-top:6px"><button class="btn btnsm" data-ap="1">Approve</button>
        <button class="btn ghost btnsm" data-ap="0">Deny</button></div></div>`;
    } else if (run.status === 'waiting_input') {
      html += `<div class="ask"><b>The agent is asking:</b>${run.result ? `<div class="body">${esc(run.result)}</div>` : ''}
        <div class="muted" style="margin-top:4px">answer below to continue</div></div>`;
    }

    const tl = $('timeline');
    const atBottom = tl.scrollHeight - tl.scrollTop - tl.clientHeight < 40;
    tl.innerHTML = html || '<div class="empty">…</div>';
    wireExpanders(tl);
    // Clicking a chip PINS the pane to that file (live=false), so the newest
    // render no longer steals it from under you.
    tl.querySelectorAll('[data-render]').forEach((el) => el.onclick = () =>
      openPreview(el.dataset.render, +el.dataset.rv, false));
    tl.querySelectorAll('[data-ap]').forEach((b) => b.onclick = async () => {
      try { await api(`/runs/${sel}/approve`, jbody({ approve: b.dataset.ap === '1' }, 'POST')); } catch (e) { alert(e.message); }
      lastDetailKey = ''; loadDetail();
    });
    if (atBottom) tl.scrollTop = tl.scrollHeight;
  }

  // Composer: on a run it messages/answers that run (a message to a finished
  // run resumes it); on home it starts a fresh quick ask. Always enabled.
  $('msg').placeholder = run.status === 'waiting_input' ? 'answer the question…' : 'follow up…';
}

async function control(action) {
  if (action === 'mem') return openSettings('memory');
  if (action === 'files') return openSettings('files');
  if (action === 'delete') {
    if (!confirm('Delete this run and its history?')) return;
    try { await api(`/runs/${sel}`, { method: 'DELETE' }); } catch (e) { return alert(e.message); }
    if (settingsOpen && activeTab === 'memory') renderTab();
    return goHome();
  }
  // resume | interrupt | compact | learn → POST /runs/{id}/{action}
  try { await api(`/runs/${sel}/${action}`, { method: 'POST' }); } catch (e) { alert(e.message); }
  lastDetailKey = ''; loadDetail();
}

// --- composer -----------------------------------------------------------

async function send() {
  const t = $('msg').value.trim();
  if (!t) return;
  $('msg').value = '';
  // On home: start a fresh quick ask and jump into it (streaming answer).
  if (sel == null) {
    try {
      const run = await api('/ask', jbody({ text: t, toolset }, 'POST'));
      sel = run.id; lastDetailKey = '';
    } catch (e) { return alert(e.message); }
    loadRuns(); loadDetail();
    return;
  }
  // Route to /answer when the run is parked on an ask_user; otherwise /message
  // (the backend aliases them, but this keeps intent explicit).
  const run = detail && detail.run;
  const waiting = run && run.status === 'waiting_input' && pendingOf(run).kind !== 'approval';
  try { await api(`/runs/${sel}/${waiting ? 'answer' : 'message'}`, jbody({ text: t }, 'POST')); } catch (e) { alert(e.message); }
  lastDetailKey = ''; loadDetail();
}
$('send').onclick = send;
$('msg').addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); send(); } });

// Render pane header. Closing remembers WHICH render was dismissed, so the
// poll doesn't immediately reopen the same one.
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
  if (e.key === 'Escape' && preview && !$('newdlg').open && !settingsOpen) {
    prevDismissed = prevSeen;
    closePreview();
  }
});
$('home').onclick = goHome;
$('tset').onclick = () => {
  toolset = toolset === 'private' ? 'web' : 'private';
  xbin.fetch('/api/xbin/prefs/toolset', { method: 'PUT', body: JSON.stringify(toolset) }).catch(() => {});
  syncToolsetBtn();
};
syncToolsetBtn();
loadToolsetPref();

// --- new run ------------------------------------------------------------

$('new').onclick = () => { $('n-goal').value = ''; $('n-title').value = ''; $('n-system').value = ''; $('newdlg').showModal(); };
$('n-create').onclick = async (e) => {
  const goal = $('n-goal').value.trim();
  if (!goal) { e.preventDefault(); return; }
  try {
    const run = await api('/runs', jbody({ goal, title: $('n-title').value.trim(), system: $('n-system').value.trim(), toolset: $('n-toolset').value }, 'POST'));
    sel = run.id; lastDetailKey = '';
  } catch (err) { alert(err.message); }
  loadRuns(); loadDetail();
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
  detail = d;
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
    tabMemory(bd); loadDetail();
  });
  bd.querySelectorAll('[data-del]').forEach((b) => b.onclick = async () => {
    const i = +b.dataset.del;
    try { await api(`/runs/${sel}/memory?key=${encodeURIComponent(keys[i])}`, { method: 'DELETE' }); }
    catch (e) { return alert(e.message); }
    tabMemory(bd); loadDetail();
  });
  $('madd').onclick = async () => {
    const k = $('mk').value.trim();
    if (!k) return;
    try { await api(`/runs/${sel}/memory`, jbody({ key: k, value: $('mv').value }, 'PUT')); }
    catch (e) { return alert(e.message); }
    tabMemory(bd); loadDetail();
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
  if (cur) {
    const full = await api(`/runs/${sel}/file?path=${encodeURIComponent(cur.path)}`);
    body = full.content || '';
    cur.version = full.version;
  }
  bd.innerHTML = `
    <div class="sec"><h4>Session files · run ${sel}</h4>
      <table class="tbl"><tr><th>path</th><th>bytes</th><th>v</th><th></th></tr>
      ${filesCache.length ? filesCache.map((f, i) => `<tr>
        <td class="mono">${esc(f.path)}</td>
        <td class="muted">${fmtN(f.bytes)}</td>
        <td class="muted">${num(f.version)}</td>
        <td style="text-align:right; white-space:nowrap">
          ${isHtmlPath(f.path) ? `<button class="btn ghost btnsm" data-fr="${i}" title="show in the render pane">Render</button> ` : ''}
          <button class="btn ghost btnsm" data-fe="${i}">Edit</button>
          <button class="btn rm btnsm" data-fd="${i}">Del</button></td></tr>`).join('')
        : '<tr><td colspan="4" class="muted">no files yet — the agent writes these with its file tools</td></tr>'}
      </table>
      <div class="hint">Stored in this run's database, not on disk. Deleting the run deletes them.</div>
    </div>
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
    tabFiles(bd); lastDetailKey = ''; loadDetail();
  });
  if ($('fl-new')) $('fl-new').onclick = () => { filesSel = null; tabFiles(bd); };
  if ($('fl-render')) $('fl-render').onclick = () => { closeSettings(); openPreview(cur.path, cur.version, false); };
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
      tabFiles(bd); lastDetailKey = ''; loadDetail();
    } catch (e) { $('fl-err').textContent = e.message; }
  };
}

// Schedules tab: cron-agents — list with enable/disable, run-now, delete, and a
// create form. A bad cron expression comes back as a 400 error we surface.
async function tabSchedules(bd) {
  const list = await api('/schedules');
  schedCache = list || [];
  bd.innerHTML = `
    <div class="sec"><h4>Cron-agents</h4>
      ${schedCache.length ? schedCache.map((s, i) => `
        <div class="card"><div class="ch">
          <input type="checkbox" data-en="${i}" ${s.enabled ? 'checked' : ''} title="enable / disable">
          <span class="nm">${esc(s.name || 'schedule ' + s.id)}</span>
          <span class="badge" title="tool mode">${s.toolset === 'web' ? '🌐' : '🔒'}</span>
          ${s.watcher ? '<span class="badge">watcher</span>' : ''}
          <button class="btn ghost btnsm" data-fire="${i}">Run now</button>
          <button class="btn rm btnsm" data-delsc="${i}">Del</button>
        </div>
        <div class="hint" style="margin-top:5px">
          <span class="mono">${esc(s.cron)}</span> · ${esc(clip(s.goal, 140))}
          ${s.lastRun ? ` · last ${new Date(s.lastRun * 1000).toLocaleString()}` : ''}
          ${s.runId ? ` · run #${s.runId}` : ''}
        </div></div>`).join('') : '<div class="hint">no cron-agents yet</div>'}
    </div>
    <div class="sec"><h4>New cron-agent</h4>
      <div class="field"><label>Name</label><input id="sc-name"></div>
      <div class="row2">
        <div class="field"><label>Cron (5-field or @every 30m)</label><input id="sc-cron" placeholder="0 9 * * *"></div>
        <div class="field"><label>Mode</label><label class="chk" style="padding-top:4px"><input type="checkbox" id="sc-watch"> Watcher (one persistent run)</label></div>
      </div>
      <div class="field"><label>Tool mode</label><select id="sc-toolset">
        <option value="private">🔒 private data — internal systems, no web</option>
        <option value="web">🌐 web — no internal systems</option>
      </select></div>
      <div class="field"><label>Goal</label><textarea id="sc-goal" rows="2"></textarea></div>
      <div><button class="btn" id="sc-create">Create</button> <span class="err" id="sc-err"></span></div>
    </div>`;
  bd.querySelectorAll('[data-en]').forEach((b) => b.onchange = async () => {
    const s = schedCache[+b.dataset.en];
    try { await api(`/schedules/${s.id}`, jbody({ enabled: b.checked }, 'PUT')); } catch (e) { alert(e.message); }
    tabSchedules(bd);
  });
  bd.querySelectorAll('[data-fire]').forEach((b) => b.onclick = async () => {
    const s = schedCache[+b.dataset.fire];
    try { await api(`/schedules/${s.id}/trigger`, { method: 'POST' }); } catch (e) { return alert(e.message); }
    loadRuns();
  });
  bd.querySelectorAll('[data-delsc]').forEach((b) => b.onclick = async () => {
    const s = schedCache[+b.dataset.delsc];
    if (!confirm(`Delete schedule "${s.name || s.id}"?`)) return;
    try { await api(`/schedules/${s.id}`, { method: 'DELETE' }); } catch (e) { return alert(e.message); }
    tabSchedules(bd);
  });
  $('sc-create').onclick = async () => {
    const name = $('sc-name').value.trim(), cron = $('sc-cron').value.trim(), goal = $('sc-goal').value.trim();
    $('sc-err').textContent = '';
    if (!cron || !goal) { $('sc-err').textContent = 'need a cron expression and a goal'; return; }
    try { await api('/schedules', jbody({ name, cron, goal, watcher: $('sc-watch').checked, toolset: $('sc-toolset').value }, 'POST')); tabSchedules(bd); }
    catch (e) { $('sc-err').textContent = e.message; }
  };
}

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

// --- poll ---------------------------------------------------------------

loadRuns();
setInterval(loadRuns, 2500);
setInterval(loadDetail, 1500);
