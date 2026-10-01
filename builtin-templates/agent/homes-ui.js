// homes-ui.js — the web view's part of a person's two homes (model/homes.js;
// API.md "Partitioned instances" → "Shared conversations"): in their own
// partition,
//   - Share on one of their own conversations opens "Share a copy": a copy
//     goes to the shared space — who can see it, its session files or not,
//     the private original kept or deleted — and opens there;
//   - a shared conversation's share dialog offers "Copy to my own space";
//   - "New chat with options" asks who can see the new chat: only you (your
//     own space) or shared (made in the shared space).
// An unpartitioned instance's page, and the global instance's own, get
// nothing here: no element, no call.
import { html, nothing, render } from '/vendor/lit-all.min.js';
import * as actions from './model/actions.js';
import { publishes, homeOf, twoHomes } from './model/homes.js';

export { publishes };

let dlg = null;
let st = null; // {run, onChange, vis, people, files, keep, busy, err}

function dialog() {
  if (!dlg) {
    dlg = Object.assign(document.createElement('dialog'), { id: 'pubdlg' });
    document.body.appendChild(dlg);
  }
  return dlg;
}

// shareOf: the share a form's choices make ({visibility, teamRole} and/or
// {members}); null: nobody besides you.
export function shareOf(vis, people) {
  const members = String(people || '').split(/[\s,]+/).map((u) => u.trim().toLowerCase()).filter(Boolean)
    .map((user) => ({ user, role: 'participant' }));
  const team = vis === 'team-viewer' || vis === 'team-participant';
  if (!team && !members.length) return null;
  return { ...(team ? { visibility: 'team', teamRole: vis === 'team-participant' ? 'participant' : 'viewer' } : {}), ...(members.length ? { members } : {}) };
}

// open goes to a conversation by its address (#c=<id>: agent.js follows it).
const open = (id) => { location.hash = 'c=' + id; };

/**
 * openPublish shows "Share a copy" for one of the person's own conversations.
 * @param run  {id, title}
 * @param onChange  called after the copy was made (the list reloads); the copy opens
 */
export function openPublish(run, onChange) {
  st = { run, onChange, vis: 'team-participant', people: '', files: false, keep: true, busy: false, err: '' };
  paint();
  dialog().showModal();
}

async function publish() {
  const share = shareOf(st.vis, st.people);
  if (!share) { st.err = 'Choose who can see the copy: the team, or people.'; paint(); return; }
  st.busy = true; st.err = ''; paint();
  try {
    const r = await actions.publish(st.run.id, { share, files: st.files, keep: st.keep });
    dialog().close();
    st.onChange?.();
    if (r && r.run && r.run.id) open(r.run.id);
    if (r && r.left && r.left.length) globalThis.xbin?.notify?.('info', `Not copied (too large): ${r.left.join(', ')}`);
  } catch (e) {
    st.err = e.message;
  } finally {
    st.busy = false;
    paint();
  }
}

function paint() {
  if (st) render(tpl(), dialog());
}

function tpl() {
  const radio = (v, label) => html`<label class="chk"><input type="radio" name="pubvis" .checked=${st.vis === v}
    @change=${() => { st.vis = v; paint(); }}> ${label}</label>`;
  return html`<form method="dialog" @submit=${(e) => e.preventDefault()}>
    <div class="dlg-hd">Share a copy of “${st.run.title || 'conversation'}”</div>
    <div class="dlg-bd share">
      <div class="muted small" id="pubnote">This conversation is in your own space, which only you can open. A copy of its whole
        transcript goes to the shared space — your messages, the agent's answers and everything its tools returned, which can
        quote your private files, memory or sandbox — where the people you choose and the agent's managers can read it. Its
        session files go too only if you add them.</div>
      <div class="field"><label>Who can see the copy</label>
        ${radio('team-viewer', 'Everyone who can open this agent — to read')}
        ${radio('team-participant', 'Everyone who can open this agent — to read and write')}
        ${radio('people', 'Only the people below')}
      </div>
      <div class="field"><label>People (user ids, comma-separated) — they can write</label>
        <input id="pub-people" autocomplete="off" .value=${st.people} @input=${(e) => { st.people = e.target.value; }}></div>
      <div class="field">
        <label class="chk"><input type="checkbox" id="pub-files" .checked=${st.files} @change=${(e) => { st.files = e.target.checked; }}>
          Add its session files to the copy</label>
        <label class="chk"><input type="checkbox" id="pub-keep" .checked=${st.keep} @change=${(e) => { st.keep = e.target.checked; }}>
          Keep my private original (else it is deleted once the copy is made)</label>
      </div>
      ${st.err ? html`<div class="err">${st.err}</div>` : nothing}
    </div>
    <div class="dlg-ft"><button class="btn ghost" @click=${() => dialog().close()}>Cancel</button>
      <button class="btn" id="pub-go" ?disabled=${st.busy} @click=${publish}>${st.busy ? 'Copying…' : 'Share a copy'}</button></div>
  </form>`;
}

/** copyTpl: in a person's partition, a shared conversation's share dialog
 * offers a private copy (nothing elsewhere). */
export function copyTpl(runId, close, onChange) {
  if (!twoHomes() || homeOf(runId) !== 'global') return nothing;
  return html`<div class="field"><label>Your own copy</label>
    <div class="prow"><span class="muted">A private copy in your own space: only you can open it; this one goes on without it.</span>
    <button class="btn ghost btnsm" id="sh-copy" @click=${async () => {
      try { const r = await actions.copyToMine(runId); close(); onChange?.(); open(r.id); } catch (e) { alert(e.message); }
    }}>Copy to my own space</button></div></div>`;
}

/**
 * mountNewShare adds "Who can see it" to the New chat dialog (dlgBody) in a
 * person's partition: only you, the team (to read, or to write), or people
 * you name — with the team, or on their own. It returns what the dialog's
 * Start sends beside its other fields: {share} for a shared chat, {} for one
 * of their own (and always {} elsewhere) — or null when people were chosen
 * and none named (the field says so; the dialog stays open).
 */
export function mountNewShare(dlgBody) {
  if (!twoHomes() || !dlgBody) return () => ({});
  const f = Object.assign(document.createElement('div'), { className: 'field', id: 'n-share-f' });
  let vis = 'mine';
  const draw = (err = '') => render(html`<label>Who can see it</label><select id="n-share" @change=${(e) => { vis = e.target.value; draw(); }}>
      <option value="mine">Only you — in your own space</option>
      <option value="team-participant">Everyone who can open this agent — to read and write (shared space)</option>
      <option value="team-viewer">Everyone who can open this agent — to read (shared space)</option>
      <option value="people">Only the people you name (shared space)</option>
    </select>
    <input id="n-share-people" ?hidden=${vis === 'mine'} autocomplete="off"
      placeholder=${vis === 'people' ? 'user ids, comma-separated — they can write' : 'and people too (optional): user ids, comma-separated'}>
    ${err ? html`<div class="err" id="n-share-err">${err}</div>` : nothing}`, f);
  draw();
  dlgBody.insertBefore(f, dlgBody.children[1] || null);
  return () => {
    vis = f.querySelector('select').value;
    if (vis === 'mine') { draw(); return {}; }
    const s = shareOf(vis, f.querySelector('#n-share-people').value);
    draw(s ? '' : 'Name the people who can see it (their user ids).');
    return s ? { share: s } : null;
  };
}
