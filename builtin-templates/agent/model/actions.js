// model/actions.js — what the views DO to this tile's backend: ask, send
// (with attachments), steer the run (retry/compact/learn, delete, stop the
// workflow), the halt switch, the tool mode for new asks, the conversation
// list's row actions, sharing and joining. Plain calls over the kit's api()
// (xbin.fetch in a tile frame); no lit, no DOM, no dialogs — a view asks
// "are you sure?" itself, then calls these. The Session (session.js) keeps
// the calls that act on the open conversation's own state (send, stop,
// take back a queued message, approve).
import { selfApi as api, jbody } from '/vendor/bx-kit.js';

// --- asking --------------------------------------------------------------

// ask starts a conversation: {text, toolset, title?, system?, hold?} — hold
// creates it without a message or a drive (attachments upload into it first).
// {draft, files} sends the draft the app uploaded into at home instead
// (PUT /ask/upload?draft=<key>, API.md "Attachments").
export const ask = (body) => api('/ask', jbody(body, 'POST'));

// draftKey names a new ask's draft: where the app uploads what is picked at
// home before there is a conversation (8–64 of A–Z a–z 0–9 _ -).
export function draftKey() {
  const c = globalThis.crypto;
  if (c && typeof c.randomUUID === 'function') return 'd' + c.randomUUID().replace(/-/g, '');
  return 'd' + Math.random().toString(36).slice(2) + Date.now().toString(36);
}

// message sends into a run: {text, files?, clientId?}.
export const message = (runId, body) => api(`/runs/${runId}/message`, jbody(body, 'POST'));

// returnedText is what Stop gives back to the composer: the text of the
// messages that were still queued.
export const returnedText = (back) => (back || []).map((q) => q.text).filter(Boolean).join('\n\n');

// --- a run ---------------------------------------------------------------

// control drives a run: resume (Retry) | compact | learn (Learn skill).
export const control = (runId, action) => api(`/runs/${runId}/${action}`, { method: 'POST' });
export const deleteRun = (runId) => api(`/runs/${runId}`, { method: 'DELETE' });

// The workflow tree of a run's root, and stopping all of it.
export const tree = (rootId) => api(`/runs/${rootId}/tree`);
export const cancelTree = (rootId) => api(`/runs/${rootId}/cancel`, jbody({ scope: 'subtree', reason: 'stopped from the tile' }, 'POST'));

// --- who you are, what needs you, the brake ---------------------------------

export const me = () => api('/me');
export const needs = async () => (await api('/needs')).items || [];
export const getHalt = () => api('/halt');
export const setHalt = (on) => api('/halt', jbody({ on }, 'PUT'));

// The tool mode for NEW asks ('private' = internal systems only, 'web' = web
// only — the exfiltration firewall), kept per person by the platform's prefs
// API (a tile frame has no localStorage). loadToolset answers 'web' or ''.
export async function loadToolset() {
  const r = await xbin.fetch('/api/xbin/prefs/toolset');
  return r.ok && (await r.json()) === 'web' ? 'web' : '';
}
export const saveToolset = (toolset) => xbin.fetch('/api/xbin/prefs/toolset', { method: 'PUT', body: JSON.stringify(toolset) });

// --- the conversation list ----------------------------------------------------

export const pin = (convs, r) => convs.patch(r.id, { pinned: !r.pinnedAt });
export const archive = (convs, r) => convs.patch(r.id, { archived: !r.archivedAt });
// rename: a changed, non-empty title only (undefined when there was nothing to do).
export function rename(convs, id, title) {
  const t = String(title || '').trim();
  const r = convs.find(id);
  if (t && (!r || t !== r.title)) return convs.patch(id, { title: t });
  return Promise.resolve(undefined);
}
// leave: someone it was shared with steps out; the row goes.
export async function leave(convs, r, user) {
  await api(`/runs/${r.id}/members/${encodeURIComponent(user)}`, { method: 'DELETE' });
  convs.remove(r.id);
}

// --- sharing (D83) ---------------------------------------------------------------

export const members = (runId) => api(`/runs/${runId}/members`);
// setVisibility: 'private' | 'team-viewer' | 'team-participant'.
export const setVisibility = (runId, v) => api(`/runs/${runId}`, jbody(v === 'private' ? { visibility: 'private' }
  : { visibility: 'team', teamRole: v === 'team-participant' ? 'participant' : 'viewer' }, 'PATCH'));
export const setMember = (runId, user, role) => api(`/runs/${runId}/members`, jbody({ user, role }, 'POST'));
export const removeMember = (runId, user) => api(`/runs/${runId}/members/${user}`, { method: 'DELETE' });
export const createLink = (runId, role, expiresIn) => api(`/runs/${runId}/links`, jbody({ role, expiresIn }, 'POST'));
export const revokeLink = (runId, linkId) => api(`/runs/${runId}/links/${linkId}`, { method: 'DELETE' });

// joinFrom redeems a join link (a #join=… hash, or a pasted link) and
// returns the conversation it opens (null: no token in the text).
export async function joinFrom(text) {
  const m = /#join=([A-Za-z0-9_-]+)/.exec(text || '');
  if (!m) return null;
  return api('/join', jbody({ token: m[1] }, 'POST'));
}

// --- attachments ------------------------------------------------------------------

// Attachments waiting to be sent. Each is uploaded into the run's session files
// (PUT /runs/{id}/upload) and then named in the message, so the model finds
// them with its file tools and sees images. `path` is set once an upload
// lands, so a retry after a failed message re-sends rather than re-uploads.
export const MAX_ATTACH = 16 * 1024 * 1024; // the backend's per-file cap

export const fmtBytes = (n) => n < 1024 ? `${n} B` : n < 1048576 ? `${(n / 1024).toFixed(0)} KB` : `${(n / 1048576).toFixed(1)} MB`;

// A chip a native app uploaded belongs where it was picked (`at`: a run id,
// or 'home' for a new ask's draft) and shows and is sent only there; a
// picked File (the web) has no `at` and goes wherever it is sent.
export class Attachments {
  /** @param on {change()} — a chip changed (added, removed, uploading, failed, done) */
  constructor(on = {}) {
    this.on = on;
    this.items = []; // [{key, file, name, size, type, path?, state?, err?, at?}]; state: '' | up | done | bad
    this.seq = 0;
  }

  // here: the chips at a place (a run id | 'home'); undefined: all of them.
  here(place) { return place === undefined ? this.items : this.items.filter((a) => a.at == null || a.at === place); }

  changed() { this.on.change?.(); }

  // add takes Files (a FileList, a paste, a drop); one over the cap is marked.
  add(list) {
    for (const f of list || []) {
      const a = { key: ++this.seq, file: f, name: f.name || 'pasted', size: f.size, type: f.type };
      if (f.size > MAX_ATTACH) { a.state = 'bad'; a.err = `too large (max ${fmtBytes(MAX_ATTACH)})`; }
      this.items.push(a);
    }
    this.changed();
  }

  // uploaded takes a file a view already put into the run itself (a native
  // app uploads with its own frame token): {name, size, type, path, at?}.
  uploaded(f) {
    this.items.push({ key: ++this.seq, name: f.name, size: f.size, type: f.type, path: f.path, state: 'done', ...(f.at != null ? { at: f.at } : {}) });
    this.changed();
  }

  remove(key) {
    this.items = this.items.filter((a) => a.key !== key);
    this.changed();
  }

  tooBig(place) { return this.here(place).some((a) => a.size > MAX_ATTACH); }

  // clear and unupload are silent: the view repaints when the send settles.
  // clear(place) empties one place (its own chips and the unplaced ones).
  clear(place) { this.items = place === undefined ? [] : this.items.filter((a) => !(a.at == null || a.at === place)); }
  unupload() { this.items.forEach((a) => { delete a.path; if (a.state === 'done') a.state = ''; }); }

  // upload puts every not-yet-uploaded attachment into run `id` (base: this
  // backend's prefix), in order, and returns all their session-file paths.
  // Throws on the first failure with that chip marked; chips already
  // uploaded keep their path.
  async upload(base, id, place) {
    const items = this.here(place);
    for (const a of items) {
      if (a.path) continue;
      a.state = 'up'; a.err = ''; this.changed();
      const r = await xbin.fetch(`${base}/runs/${id}/upload?name=${encodeURIComponent(a.name)}`, {
        method: 'PUT', headers: { 'Content-Type': a.type || 'application/octet-stream' }, body: a.file,
      });
      const d = await r.json().catch(() => ({}));
      if (!r.ok) {
        a.state = 'bad'; a.err = d.error || `upload failed (${r.status})`; this.changed();
        throw new Error(`${a.name}: ${a.err}`);
      }
      a.path = d.path; a.state = 'done'; this.changed();
    }
    return items.map((a) => a.path);
  }
}
