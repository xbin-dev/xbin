// agent.js — the control tile logic. Polls the backend and renders a run list
// plus a live, interleaved timeline (transcript messages + the journal of LLM
// calls / tool calls / compactions / yields) so a run is fully debuggable, and
// wires the steering controls. Vanilla JS; xbin.fetch attributes calls to this
// element (self → admin of its own backend).
const base = `/api/${xbin.self}`;
const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
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

let sel = null;         // selected run id
let lastDetailKey = ''; // cheap change-detection for the timeline

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
    const sub = run.parentId ? `↳ subagent · ` : '';
    el.innerHTML = `<div class="t">${esc(run.title || 'run ' + run.id)}</div>
      <div class="m">${sub}<span class="badge ${esc(run.status)}">${esc(run.status)}</span></div>`;
    el.onclick = () => { sel = run.id; lastDetailKey = ''; loadRuns(); loadDetail(); };
    host.append(el);
  }
}

// --- selected run -------------------------------------------------------

function eventStream(d) {
  // Merge transcript messages and journal steps into one time-ordered stream.
  const evs = [];
  for (const m of d.messages || []) {
    if (m.role === 'system') continue;
    evs.push({ t: m.created, order: m.seq, kind: 'msg', m });
  }
  for (const s of d.steps || []) {
    if (s.kind === 'note' || s.kind === 'compaction' || s.kind === 'yield' || s.kind === 'error' || s.kind === 'llm_call')
      evs.push({ t: s.created, order: 1000 + s.seq, kind: 'step', s });
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
  try { d = JSON.parse(s.detail); } catch { d = { detail: s.detail }; }
  let txt = '';
  if (s.kind === 'llm_call') txt = `LLM · ${esc(d.model || '')} · ${d.latencyMs || 0}ms · in ${d.promptTokens || 0} / out ${d.completionTokens || 0} tok · ${d.toolCalls || 0} tool call(s)`;
  else if (s.kind === 'compaction') txt = `compacted ${d.messages} message(s) → summary (${d.summaryTokens} tok)`;
  else if (s.kind === 'yield') txt = `yield${d.seconds != null ? ' ' + d.seconds + 's' : ''}${d.reason ? ' · ' + esc(d.reason) : ''}`;
  else if (s.kind === 'note') txt = esc(d.text || '');
  else if (s.kind === 'error') txt = '⚠ ' + esc(d.error || '');
  return `<div class="ev step"><div class="body">◆ ${txt}</div></div>`;
}

async function loadDetail() {
  if (sel == null) return;
  let d;
  try { d = await api('GET', `/runs/${sel}`); } catch { return; }
  const run = d.run;

  // Top bar + controls.
  const parked = run.status === 'waiting_input';
  const approval = parked && (run.pending || '').includes('"approval"');
  $('top').innerHTML = `<span class="title">${esc(run.title || 'run ' + run.id)}</span>
    <span class="badge ${esc(run.status)}">${esc(run.status)}</span>
    <button class="btn ghost" data-a="resume">Resume</button>
    <button class="btn ghost" data-a="interrupt">Interrupt</button>
    <button class="btn ghost" data-a="compact">Compact</button>
    <button class="btn ghost" data-a="memory">Memory (${Object.keys(d.memory || {}).length})</button>
    <button class="btn ghost" data-a="delete">Delete</button>`;
  $('top').querySelectorAll('[data-a]').forEach((b) => b.onclick = () => control(b.dataset.a, d));

  // Timeline.
  const key = JSON.stringify([run.status, run.updated, (d.messages || []).length, (d.steps || []).length]);
  if (key !== lastDetailKey) {
    lastDetailKey = key;
    const evs = eventStream(d);
    let html = evs.map((e) => e.kind === 'msg' ? renderMsg(e.m) : renderStep(e.s)).join('');
    if (approval) {
      html += `<div class="ask"><b>Approval needed</b> for a tool call.
        <div style="margin-top:6px"><button class="btn" data-ap="1">Approve</button>
        <button class="btn ghost" data-ap="0">Deny</button></div></div>`;
    } else if (parked && run.result) {
      html += `<div class="ask"><b>The agent is asking:</b><div class="body">${esc(run.result)}</div>
        <div class="muted" style="margin-top:4px">answer below to continue</div></div>`;
    } else if (run.status === 'done' && run.result) {
      html += `<div class="ev step"><div class="body">✓ finished: ${esc(run.result)}</div></div>`;
    }
    const tl = $('timeline');
    const atBottom = tl.scrollHeight - tl.scrollTop - tl.clientHeight < 40;
    tl.innerHTML = html || '<div class="empty">…</div>';
    tl.querySelectorAll('[data-ap]').forEach((b) => b.onclick = async () => {
      await api('POST', `/runs/${sel}/approve`, { approve: b.dataset.ap === '1' }); lastDetailKey = ''; loadDetail();
    });
    if (atBottom) tl.scrollTop = tl.scrollHeight;
  }

  // Composer enabled unless the run is finished.
  const done = run.status === 'done' || run.status === 'error';
  $('msg').disabled = done; $('send').disabled = done;
}

async function control(action, d) {
  if (action === 'memory') return openMemory(d);
  if (action === 'delete') {
    if (!confirm('Delete this run and its history?')) return;
    await api('DELETE', `/runs/${sel}`); sel = null; lastDetailKey = '';
    $('top').innerHTML = '<span class="muted">select or start a run</span>';
    $('timeline').innerHTML = '<div class="empty">—</div>';
    return loadRuns();
  }
  try { await api('POST', `/runs/${sel}/${action}`); } catch (e) { alert(e.message); }
  lastDetailKey = ''; loadDetail();
}

// --- composer -----------------------------------------------------------

async function send() {
  const t = $('msg').value.trim();
  if (!t || sel == null) return;
  $('msg').value = '';
  try { await api('POST', `/runs/${sel}/message`, { text: t }); } catch (e) { alert(e.message); }
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

// --- config -------------------------------------------------------------

$('gear').onclick = async () => {
  const c = await api('GET', '/config');
  $('c-model').value = c.model || ''; $('c-system').value = c.system || '';
  $('c-budget').value = c.tokenBudget || 0; $('c-iters').value = c.maxIters || 0;
  $('c-sub').value = String(!!c.subagents); $('c-appr').value = String(!!c.approve);
  $('c-mcp').value = JSON.stringify(c.mcp || [], null, 0);
  $('cfgdlg').showModal();
};
$('c-save').onclick = async (e) => {
  let mcp = [];
  try { mcp = JSON.parse($('c-mcp').value || '[]'); } catch { alert('MCP servers must be valid JSON'); e.preventDefault(); return; }
  try {
    await api('PUT', '/config', {
      model: $('c-model').value.trim(), system: $('c-system').value,
      tokenBudget: +$('c-budget').value, maxIters: +$('c-iters').value,
      subagents: $('c-sub').value === 'true', approve: $('c-appr').value === 'true', mcp,
    });
  } catch (err) { alert(err.message); e.preventDefault(); }
};

// --- memory editor ------------------------------------------------------

function openMemory(d) {
  const cur = d.memory || {};
  const lines = Object.entries(cur).map(([k, v]) => `${k}: ${v}`).join('\n');
  const val = prompt('Memory blocks (one per line, "key: value"). Save to replace.', lines);
  if (val == null) return;
  const next = {};
  for (const line of val.split('\n')) {
    const i = line.indexOf(':');
    if (i > 0) next[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  (async () => {
    for (const k of Object.keys(cur)) if (!(k in next)) await api('DELETE', `/runs/${sel}/memory?key=${encodeURIComponent(k)}`);
    for (const [k, v] of Object.entries(next)) await api('PUT', `/runs/${sel}/memory`, { key: k, value: v });
    lastDetailKey = ''; loadDetail();
  })();
}

// --- poll ---------------------------------------------------------------

loadRuns();
setInterval(loadRuns, 2500);
setInterval(loadDetail, 1500);
