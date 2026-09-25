// share.js — the share dialog of a conversation (D83): who can see it (only
// the people you add, or everyone who can open this agent — to read, or to
// write too), the people it is shared with, and join links. Someone it was
// shared with sees the same, read-only, and can leave.
//
// A link is built from the page's own address WITHOUT its query string: a
// credentialless frame's URL carries a frame token (?frame=…), which must
// never end up in something you paste to a colleague.
import { html, nothing, render } from '/vendor/lit-all.min.js';
import { selfApi as api, jbody } from '/vendor/bx-kit.js';

let dlg = null;
let st = null; // {runId, title, me, data, link, err, onChange}

function dialog() {
  if (!dlg) {
    dlg = document.createElement('dialog');
    dlg.id = 'sharedlg';
    document.body.appendChild(dlg);
  }
  return dlg;
}

// linkFor is the address a join token is shared at.
export const linkFor = (token) => `${location.protocol}//${location.host}${location.pathname}#join=${token}`;

/**
 * openShare shows the dialog for a conversation.
 * @param run  {id, title}
 * @param me   GET /me
 * @param onChange  called after anything changed (the list repaints)
 */
export async function openShare(run, me, onChange) {
  st = { runId: run.id, title: run.title, me, data: null, link: '', err: '', onChange };
  paint();
  dialog().showModal();
  await load();
}

async function load() {
  try { st.data = await api(`/runs/${st.runId}/members`); } catch (e) { st.err = e.message; }
  paint();
}

async function act(fn) {
  st.err = '';
  try { await fn(); await load(); st.onChange?.(); } catch (e) { st.err = e.message; paint(); }
}

function paint() {
  if (!st) return;
  render(tpl(), dialog());
}

function tpl() {
  const d = st.data;
  const own = d && (d.owner === st.me.user || (d.owner === '' && st.me.manager) || st.me.kind === 'system');
  const vis = !d ? '' : d.visibility === 'team' ? 'team-' + d.teamRole : 'private';
  const setVis = (v) => act(() => api(`/runs/${st.runId}`, jbody(v === 'private' ? { visibility: 'private' }
    : { visibility: 'team', teamRole: v === 'team-participant' ? 'participant' : 'viewer' }, 'PATCH')));
  const radio = (v, label) => html`<label class="chk"><input type="radio" name="vis" .checked=${vis === v} ?disabled=${!own}
    @change=${() => setVis(v)}> ${label}</label>`;
  return html`<form method="dialog" @submit=${(e) => e.preventDefault()}>
    <div class="dlg-hd">Share “${st.title || 'conversation'}”</div>
    <div class="dlg-bd share">
      ${!d ? html`<div class="muted">loading…</div>` : html`
      <div class="field"><label>Who can see it</label>
        ${radio('private', 'Only you and the people below')}
        ${radio('team-viewer', 'Everyone who can open this agent — to read')}
        ${radio('team-participant', 'Everyone who can open this agent — to read and write')}
      </div>
      <div class="field"><label>People</label>
        ${d.owner ? html`<div class="prow"><span class="mono">${d.owner}</span><span class="muted">owner</span></div>` : nothing}
        ${d.members.map((m) => html`<div class="prow"><span class="mono">${m.user}</span>
          ${own ? html`<select @change=${(e) => act(() => api(`/runs/${st.runId}/members`, jbody({ user: m.user, role: e.target.value }, 'POST')))}>
              <option value="viewer" ?selected=${m.role === 'viewer'}>can read</option>
              <option value="participant" ?selected=${m.role === 'participant'}>can write</option></select>
            <button class="btn ghost btnsm" @click=${() => act(() => api(`/runs/${st.runId}/members/${m.user}`, { method: 'DELETE' }))}>Remove</button>`
          : html`<span class="muted">${m.role === 'participant' ? 'can write' : 'can read'}</span>`}</div>`)}
        ${own ? html`<div class="prow add">
          <input id="sh-user" placeholder="user id (their login name)" autocomplete="off">
          <select id="sh-role"><option value="participant">can write</option><option value="viewer">can read</option></select>
          <button class="btn btnsm" @click=${() => {
            const user = document.getElementById('sh-user').value.trim().toLowerCase();
            if (user) act(() => api(`/runs/${st.runId}/members`, jbody({ user, role: document.getElementById('sh-role').value }, 'POST')));
          }}>Add</button></div>` : nothing}
      </div>
      ${own ? html`<div class="field"><label>Invite link — anyone who can open this agent and has the link joins</label>
        <div class="prow add">
          <select id="sh-lrole"><option value="participant">can write</option><option value="viewer">can read</option></select>
          <select id="sh-lexp"><option value="604800">for 7 days</option><option value="86400">for a day</option><option value="0">no expiry</option></select>
          <button class="btn btnsm" @click=${() => act(async () => {
            const r = await api(`/runs/${st.runId}/links`, jbody({ role: document.getElementById('sh-lrole').value,
              expiresIn: +document.getElementById('sh-lexp').value }, 'POST'));
            st.link = linkFor(r.token);
          })}>Create link</button></div>
        ${st.link ? html`<input class="mono linkout" readonly .value=${st.link} @focus=${(e) => e.target.select()}>
          <div class="muted small">shown once — copy it now</div>` : nothing}
        ${(d.links || []).map((l) => html`<div class="prow"><span class="muted">link #${l.id} · ${l.role === 'participant' ? 'can write' : 'can read'}
            · used ${l.uses}${l.maxUses ? '/' + l.maxUses : ''}${l.expires ? ' · until ' + new Date(l.expires * 1000).toLocaleDateString() : ''}</span>
          <button class="btn ghost btnsm" @click=${() => act(() => api(`/runs/${st.runId}/links/${l.id}`, { method: 'DELETE' }))}>Revoke</button></div>`)}
      </div>` : st.me.user && d.members.some((m) => m.user === st.me.user) ? html`<div class="field">
        <button class="btn rm btnsm" @click=${() => act(async () => {
          await api(`/runs/${st.runId}/members/${st.me.user}`, { method: 'DELETE' });
          dialog().close();
        })}>Leave this conversation</button></div>` : nothing}`}
      ${st.err ? html`<div class="err">${st.err}</div>` : nothing}
    </div>
    <div class="dlg-ft"><button class="btn" @click=${() => dialog().close()}>Done</button></div>
  </form>`;
}

// joinFrom redeems a join link (a #join=… hash, or a pasted link) and
// returns the conversation it opens.
export async function joinFrom(text) {
  const m = /#join=([A-Za-z0-9_-]+)/.exec(text || '');
  if (!m) return null;
  return api('/join', jbody({ token: m[1] }, 'POST'));
}
