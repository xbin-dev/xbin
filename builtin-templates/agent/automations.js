// automations.js — the non-UI agents: schedules and watchers now, channels
// and triggers as they land (D83). For now the schedules list and form the
// settings tab shows; the Automations page grows here.

// schedulesTab draws the cron-agents list and the new-schedule form into bd.
// d: {api, jbody, esc, clip}
export async function schedulesTab(bd, d) {
  const { api, jbody, esc, clip } = d;
  const $ = (id) => document.getElementById(id);
  const list = await api('/schedules');
  const schedCache = list || [];
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
    schedulesTab(bd, d);
  });
  bd.querySelectorAll('[data-fire]').forEach((b) => b.onclick = async () => {
    const s = schedCache[+b.dataset.fire];
    try { await api(`/schedules/${s.id}/trigger`, { method: 'POST' }); } catch (e) { return alert(e.message); }
  });
  bd.querySelectorAll('[data-delsc]').forEach((b) => b.onclick = async () => {
    const s = schedCache[+b.dataset.delsc];
    if (!confirm(`Delete schedule "${s.name || s.id}"?`)) return;
    try { await api(`/schedules/${s.id}`, { method: 'DELETE' }); } catch (e) { return alert(e.message); }
    schedulesTab(bd, d);
  });
  $('sc-create').onclick = async () => {
    const name = $('sc-name').value.trim(), cron = $('sc-cron').value.trim(), goal = $('sc-goal').value.trim();
    $('sc-err').textContent = '';
    if (!cron || !goal) { $('sc-err').textContent = 'need a cron expression and a goal'; return; }
    try { await api('/schedules', jbody({ name, cron, goal, watcher: $('sc-watch').checked, toolset: $('sc-toolset').value }, 'POST')); schedulesTab(bd, d); }
    catch (e) { $('sc-err').textContent = e.message; }
  };
}
