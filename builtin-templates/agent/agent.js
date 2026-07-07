// agent.js — the control tile logic. Polls the backend and renders a run list
// plus a live, interleaved timeline (transcript messages + the journal of LLM
// calls / tool calls / compactions / yields), streams the in-flight assistant
// draft while a run is running, and wires the steering controls + a tabbed
// settings area (config / features / memory / schedules / skills / MCP).
// Vanilla ES module (no framework, no build step — like the rest of this tile);
// xbin.fetch attributes calls to this element (self → admin of its own backend).
const base = `/api/${xbin.self}`;
const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
const num = (v) => Number(v) || 0;
const clip = (s, n) => { s = String(s ?? ''); return s.length > n ? s.slice(0, n) + '…' : s; };
// Group digits for readability: 123123 → "123 123" (narrow no-break space).
const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');
const errBox = (e) => `<div class="err">${esc(e && e.message ? e.message : e)}</div>`;
const api = async (method, path, body) => {
  const r = await xbin.fetch(base + path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  const d = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(d.error || r.status);
  return d;
};

let sel = null;          // selected run id
let lastDetailKey = '';  // cheap change-detection for the timeline
let detail = null;       // last GET /runs/{id} payload
let models = [];         // model ids from GET /models ({data:[{id}]})
let cfgCache = null;     // last GET /config
let settingsOpen = false;
let activeTab = 'config';
let schedCache = [];     // schedules for the schedules tab (handler lookup by index)
let skillsCache = [];    // skills for the skills tab
let skillSel = null;     // name of the skill being edited (null = new)

// --- runs list ----------------------------------------------------------

async function loadRuns() {
  let runs;
  try { runs = await api('GET', '/runs'); } catch { return; }
  const host = $('runs');
  if (!runs.length) { host.innerHTML = '<div class="empty">no runs yet</div>'; return; }
  host.innerHTML = '';
  for (const run of runs) {
    const el = document.createElement('div');
    el.className = 'run' + (run.id === sel ? ' on' : '');
    const sub = run.parentId ? '↳ subagent · ' : '';
    el.innerHTML = `<div class="t">${esc(run.title || 'run ' + run.id)}</div>
      <div class="m">${sub}<span class="badge ${esc(run.status)}">${esc(run.status)}</span></div>`;
    el.onclick = () => { sel = run.id; lastDetailKey = ''; loadRuns(); loadDetail(); };
    host.append(el);
  }
}

// --- selected run -------------------------------------------------------

function eventStream(d) {
  // Merge transcript messages and the "meta" journal steps into one time-
  // ordered stream. Tool activity (tool_call/tool_result) is shown via the
  // transcript messages (assistant tool chips + tool-role results), and asks
  // via the actionable footer, so those step kinds are omitted here to avoid
  // duplicating them — renderStep still handles every kind for robustness.
  const metaKinds = new Set(['llm_call', 'compaction', 'yield', 'state_changed', 'finish', 'error', 'note']);
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

function renderMsg(m) {
  let calls = '';
  if (m.toolCalls) {
    try {
      calls = JSON.parse(m.toolCalls).map((tc) =>
        `<div class="tc">→ ${esc(tc.function.name)}(${esc(tc.function.arguments || '')})</div>`).join('');
    } catch { /* ignore */ }
  }
  const label = m.role === 'tool' ? `tool · ${esc(m.name)}` : esc(m.role);
  return `<div class="ev ${esc(m.role)}"><div class="role">${label}</div>
    ${m.content ? `<div class="body">${esc(m.content)}</div>` : ''}${calls}</div>`;
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
  try { d = await api('GET', `/runs/${sel}`); } catch { return; }
  detail = d;
  const run = d.run;
  const pend = pendingOf(run);
  const isApproval = run.status === 'waiting_input' && pend.kind === 'approval';

  // Top bar + controls.
  $('top').innerHTML = `<span class="title">${esc(run.title || 'run ' + run.id)}</span>
    <span class="badge ${esc(run.status)}">${esc(run.status)}</span>
    <button class="btn ghost btnsm" data-a="resume">Resume</button>
    <button class="btn ghost btnsm" data-a="interrupt">Interrupt</button>
    <button class="btn ghost btnsm" data-a="compact">Compact</button>
    <button class="btn ghost btnsm" data-a="learn" title="Distill this run into a reusable skill">Learn skill</button>
    <button class="btn ghost btnsm" data-a="mem">Memory (${Object.keys(d.memory || {}).length})</button>
    <button class="btn rm btnsm" data-a="delete">Delete</button>`;
  $('top').querySelectorAll('[data-a]').forEach((b) => b.onclick = () => control(b.dataset.a));

  // Timeline. Include the live draft + status so streaming re-renders.
  const key = JSON.stringify([run.status, run.updated, (d.messages || []).length, (d.steps || []).length, d.draft || '']);
  if (key !== lastDetailKey) {
    lastDetailKey = key;
    const evs = eventStream(d);
    let html = evs.map((e) => e.kind === 'msg' ? renderMsg(e.m) : renderStep(e.s)).join('');

    // Live streaming partial assistant text while running.
    if (run.status === 'running') {
      html += d.draft
        ? `<div class="draft"><div class="role">assistant · streaming</div><div class="body">${esc(d.draft)}<span class="cur"></span></div></div>`
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
    tl.querySelectorAll('[data-ap]').forEach((b) => b.onclick = async () => {
      try { await api('POST', `/runs/${sel}/approve`, { approve: b.dataset.ap === '1' }); } catch (e) { alert(e.message); }
      lastDetailKey = ''; loadDetail();
    });
    if (atBottom) tl.scrollTop = tl.scrollHeight;
  }

  // Composer is usable whenever a run is selected — sending a message to a
  // finished run resumes it (backend sets it idle and re-drives).
  $('msg').disabled = sel == null; $('send').disabled = sel == null;
}

async function control(action) {
  if (action === 'mem') return openSettings('memory');
  if (action === 'delete') {
    if (!confirm('Delete this run and its history?')) return;
    try { await api('DELETE', `/runs/${sel}`); } catch (e) { return alert(e.message); }
    sel = null; detail = null; lastDetailKey = '';
    $('top').innerHTML = '<span class="muted">select or start a run</span>';
    $('timeline').innerHTML = '<div class="empty">—</div>';
    $('msg').disabled = true; $('send').disabled = true;
    if (settingsOpen && activeTab === 'memory') renderTab();
    return loadRuns();
  }
  // resume | interrupt | compact | learn → POST /runs/{id}/{action}
  try { await api('POST', `/runs/${sel}/${action}`); } catch (e) { alert(e.message); }
  lastDetailKey = ''; loadDetail();
}

// --- composer -----------------------------------------------------------

async function send() {
  const t = $('msg').value.trim();
  if (!t || sel == null) return;
  $('msg').value = '';
  // Route to /answer when the run is parked on an ask_user; otherwise /message
  // (the backend aliases them, but this keeps intent explicit).
  const run = detail && detail.run;
  const waiting = run && run.status === 'waiting_input' && pendingOf(run).kind !== 'approval';
  try { await api('POST', `/runs/${sel}/${waiting ? 'answer' : 'message'}`, { text: t }); } catch (e) { alert(e.message); }
  lastDetailKey = ''; loadDetail();
}
$('send').onclick = send;
$('msg').addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); send(); } });

// --- new run ------------------------------------------------------------

$('new').onclick = () => { $('n-goal').value = ''; $('n-title').value = ''; $('n-system').value = ''; $('newdlg').showModal(); };
$('n-create').onclick = async (e) => {
  const goal = $('n-goal').value.trim();
  if (!goal) { e.preventDefault(); return; }
  try {
    const run = await api('POST', '/runs', { goal, title: $('n-title').value.trim(), system: $('n-system').value.trim() });
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
  const fns = { config: tabConfig, features: tabFeatures, memory: tabMemory, schedules: tabSchedules, skills: tabSkills, mcp: tabMcp };
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
  try { const d = await api('GET', '/models'); models = (d.data || []).map((x) => x.id).filter(Boolean); }
  catch { if (!models.length) models = []; }
}

// Config tab: model tiers + system prompt + limits + behavior. Saves the FULL
// merged config (preserving features/mcp/legacy model) via PUT /config.
async function tabConfig(bd) {
  const c = await api('GET', '/config');
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
      await api('PUT', '/config', next); cfgCache = next;
      $('cf-msg').textContent = 'saved ✓';
      setTimeout(() => { const e = $('cf-msg'); if (e) e.textContent = ''; }, 1500);
    } catch (e) { $('cf-msg').textContent = e.message; }
  };
}

// Features tab: a checkbox per capability. Toggling fetches the current config,
// merges {features:{...}}, and PUTs it back.
async function tabFeatures(bd) {
  const f = await api('GET', '/features');
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
      const c = await api('GET', '/config');
      c.features = { ...(c.features || {}), [b.dataset.f]: b.checked };
      await api('PUT', '/config', c); cfgCache = c;
    } catch (e) { alert(e.message); }
    tabFeatures(bd);
  });
}

// Memory tab: the SELECTED run's memory blocks (key→value): edit/add/delete.
async function tabMemory(bd) {
  if (sel == null) { bd.innerHTML = '<div class="empty">select a run to edit its memory blocks</div>'; return; }
  const d = await api('GET', `/runs/${sel}`);
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
    try { await api('PUT', `/runs/${sel}/memory`, { key: keys[i], value: bd.querySelector(`[data-v="${i}"]`).value }); }
    catch (e) { return alert(e.message); }
    tabMemory(bd); loadDetail();
  });
  bd.querySelectorAll('[data-del]').forEach((b) => b.onclick = async () => {
    const i = +b.dataset.del;
    try { await api('DELETE', `/runs/${sel}/memory?key=${encodeURIComponent(keys[i])}`); }
    catch (e) { return alert(e.message); }
    tabMemory(bd); loadDetail();
  });
  $('madd').onclick = async () => {
    const k = $('mk').value.trim();
    if (!k) return;
    try { await api('PUT', `/runs/${sel}/memory`, { key: k, value: $('mv').value }); }
    catch (e) { return alert(e.message); }
    tabMemory(bd); loadDetail();
  };
}

// Schedules tab: cron-agents — list with enable/disable, run-now, delete, and a
// create form. A bad cron expression comes back as a 400 error we surface.
async function tabSchedules(bd) {
  const list = await api('GET', '/schedules');
  schedCache = list || [];
  bd.innerHTML = `
    <div class="sec"><h4>Cron-agents</h4>
      ${schedCache.length ? schedCache.map((s, i) => `
        <div class="card"><div class="ch">
          <input type="checkbox" data-en="${i}" ${s.enabled ? 'checked' : ''} title="enable / disable">
          <span class="nm">${esc(s.name || 'schedule ' + s.id)}</span>
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
      <div class="field"><label>Goal</label><textarea id="sc-goal" rows="2"></textarea></div>
      <div><button class="btn" id="sc-create">Create</button> <span class="err" id="sc-err"></span></div>
    </div>`;
  bd.querySelectorAll('[data-en]').forEach((b) => b.onchange = async () => {
    const s = schedCache[+b.dataset.en];
    try { await api('PUT', `/schedules/${s.id}`, { enabled: b.checked }); } catch (e) { alert(e.message); }
    tabSchedules(bd);
  });
  bd.querySelectorAll('[data-fire]').forEach((b) => b.onclick = async () => {
    const s = schedCache[+b.dataset.fire];
    try { await api('POST', `/schedules/${s.id}/trigger`); } catch (e) { return alert(e.message); }
    loadRuns();
  });
  bd.querySelectorAll('[data-delsc]').forEach((b) => b.onclick = async () => {
    const s = schedCache[+b.dataset.delsc];
    if (!confirm(`Delete schedule "${s.name || s.id}"?`)) return;
    try { await api('DELETE', `/schedules/${s.id}`); } catch (e) { return alert(e.message); }
    tabSchedules(bd);
  });
  $('sc-create').onclick = async () => {
    const name = $('sc-name').value.trim(), cron = $('sc-cron').value.trim(), goal = $('sc-goal').value.trim();
    $('sc-err').textContent = '';
    if (!cron || !goal) { $('sc-err').textContent = 'need a cron expression and a goal'; return; }
    try { await api('POST', '/schedules', { name, cron, goal, watcher: $('sc-watch').checked }); tabSchedules(bd); }
    catch (e) { $('sc-err').textContent = e.message; }
  };
}

// Skills tab: the self-authored skill library — list, view/edit, save, delete.
async function tabSkills(bd) {
  const list = await api('GET', '/skills');
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
    try { await api('DELETE', `/skills/${encodeURIComponent(s.name)}`); } catch (e) { return alert(e.message); }
    if (skillSel === s.name) skillSel = null;
    tabSkills(bd);
  });
  if ($('sk-new')) $('sk-new').onclick = () => { skillSel = null; tabSkills(bd); };
  $('sk-save').onclick = async () => {
    const name = $('sk-name').value.trim();
    $('sk-err').textContent = '';
    if (!name || !$('sk-content').value.trim()) { $('sk-err').textContent = 'need a name and content'; return; }
    try {
      await api('PUT', '/skills', { name, description: $('sk-desc').value.trim(), content: $('sk-content').value });
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
