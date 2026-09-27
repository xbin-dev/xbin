// model/actions.js — what the views DO to this tile's backend: ask, send
// (with attachments), steer the run (retry/compact/learn, delete, stop the
// workflow), the halt switch, the class for new asks, the conversation
// list's row actions, sharing and joining, and the managers' settings (the
// classes among them), a run's memory and session files and the skill
// library (the web's ⚙ tabs, the native view's pushed screens). Plain calls
// over the kit's api() (xbin.fetch in a tile frame); no lit, no DOM, no
// dialogs — a view asks "are you sure?" itself, then calls these. The
// Session (session.js) keeps the calls that act on the open conversation's
// own state (send, stop, take back a queued message, approve).
import { selfApi as api, jbody, sandboxed } from '/vendor/bx-kit.js';

// refusing: the kit's selfApi() with the refusal kept — e.status, and a
// sandbox manager's e.refusal (API.md "Coding sandboxes") beside e.message.
async function refusing(path, opts) {
  const x = globalThis.xbin;
  const f = sandboxed() && x && x.fetch ? x.fetch : fetch;
  const r = await f(`/api/${x?.self ?? ''}${path}`, opts);
  const text = await r.text();
  let data;
  try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!r.ok) {
    const e = new Error((data && typeof data === 'object' && data.error) || `error ${r.status}`);
    e.status = r.status;
    if (data && typeof data === 'object' && data.refusal) e.refusal = data.refusal;
    throw e;
  }
  return data;
}

// --- asking --------------------------------------------------------------

// ask starts a conversation: {text, class, toolset, title?, system?, hold?,
// sandbox?} — class wins over the legacy toolset (the lane); hold creates it
// without a message or a drive (attachments upload into it first).
// {draft, files} sends the draft the app uploaded into at home instead
// (PUT /ask/upload?draft=<key>, API.md "Attachments"). A refusal carries
// e.status (and e.refusal when the sandbox's manager refused).
export const ask = (body) => refusing('/ask', jbody(body, 'POST'));

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

// The class for NEW asks (D116, model/classes.js), kept per person by the
// platform's prefs API (a tile frame has no localStorage) like the model
// pick: loadClassPref answers the id or ''. The lane picked before classes
// ('private' = internal systems only, 'web' = web only) is the fallback:
// loadToolset answers 'web', 'private', or '' when there is none.
export async function loadClassPref() {
  const r = await xbin.fetch('/api/xbin/prefs/class');
  const v = r.ok ? await r.json() : '';
  return typeof v === 'string' ? v : '';
}
export const saveClassPref = (id) => xbin.fetch('/api/xbin/prefs/class', { method: 'PUT', body: JSON.stringify(id) });
export async function loadToolset() {
  const r = await xbin.fetch('/api/xbin/prefs/toolset');
  if (!r.ok) return '';
  const v = await r.json();
  return v === 'web' ? 'web' : v === 'private' ? 'private' : '';
}
export const saveToolset = (toolset) => xbin.fetch('/api/xbin/prefs/toolset', { method: 'PUT', body: JSON.stringify(toolset) });

// classes: GET /classes — {classes, default}: the ones you may start a
// conversation in (a manager sees every one). saveClasses: PUT /classes
// (managers) — the whole set; a refusal carries its status, and a 409 the
// mixed classes it wants confirmed (e.mixed).
export const classes = () => api('/classes');
export async function saveClasses(body) {
  const r = await xbin.fetch(`/api/${xbin.self}/classes`, jbody(body, 'PUT'));
  const data = await r.json().catch(() => null);
  if (!r.ok) {
    const e = new Error((data && data.error) || `error ${r.status}`);
    e.status = r.status;
    e.mixed = (data && data.mixed) || [];
    throw e;
  }
  return data;
}

// The model a person picks for NEW asks ('' = the agent's default) — the
// last one they picked anywhere — kept per person like the tool mode.
export async function loadModelPref() {
  const r = await xbin.fetch('/api/xbin/prefs/model');
  const v = r.ok ? await r.json() : '';
  return typeof v === 'string' ? v : '';
}
export const saveModelPref = (model) => xbin.fetch('/api/xbin/prefs/model', { method: 'PUT', body: JSON.stringify(model) });

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

// --- settings (managers) ------------------------------------------------------------

// The tile-wide config: {models, system, tokenBudget, maxIters, toolTimeout,
// subagents, approve, features, mcp, …}. saveConfig sends the WHOLE config
// back — what a form did not touch rides along as it was.
export const getConfig = () => api('/config');
export const saveConfig = (c) => api('/config', jbody(c, 'PUT'));

// models: the model references the bound LLM providers list (GET /models) —
// what a tier or a pick stores; throws when it cannot say.
export const models = async () => ((await api('/models')).data || []).map((x) => x.ref || x.id).filter(Boolean);
// modelCatalog: the whole answer, for the pickers — {data: [{id, provider,
// ref}], providers: [{path, ok, error?}], error?} (D111).
export const modelCatalog = () => api('/models');
// setRunModel switches a conversation's model from its next turn
// ('' = back to the agent's default).
export const setRunModel = (id, model) => api(`/runs/${id}`, jbody({ model }, 'PATCH'));

// features: {keys, features} — the switches there are and which are on.
// setFeature merges one switch into the current config and saves it; it
// answers the config it saved.
export const features = () => api('/features');
export async function setFeature(key, on) {
  const c = await api('/config');
  c.features = { ...(c.features || {}), [key]: on };
  await api('/config', jbody(c, 'PUT'));
  return c;
}

// --- coding sandboxes (D115, API.md "Coding sandboxes") -------------------------------

// A sandbox reference (<provider>[#inst]|<id>) in a route's path: its
// slashes as they are, the rest percent-encoded (# and | never go raw).
export const sbxPath = (ref) => '/sandboxes/' + String(ref).split('/').map(encodeURIComponent).join('/');
// sandboxes: {sandboxes, managers} — what you may see across the bound managers.
export const sandboxes = (fresh) => api('/sandboxes' + (fresh ? '?fresh=1' : ''));
// createSandbox: {name, provider?, image?, size?, egress?, visibility?,
// conversation?, bind?, cwd?, clientId?} → the sandbox (+ binding).
export const createSandbox = (body) => api('/sandboxes', jbody(body, 'POST'));
export const patchSandbox = (ref, body) => api(sbxPath(ref), jbody(body, 'PATCH'));
export const deleteSandbox = (ref) => api(sbxPath(ref), { method: 'DELETE' });
// sandboxAction: start | stop | archive | thaw, waiting up to `wait` s for it
// to settle; `conversation`: acting as a participant of one it is bound to.
export const sandboxAction = (ref, action, { wait = 20, conversation } = {}) =>
  api(`${sbxPath(ref)}/${action}?wait=${wait}${conversation != null ? `&conversation=${conversation}` : ''}`, jbody({}, 'POST'));
// setRunSandbox: a conversation's binding — {sandbox: {ref, cwd?} | null, detach?: <ref>}.
export const setRunSandbox = (id, body) => api(`/runs/${id}`, jbody(body, 'PATCH'));
// endManagerExec: DELETE a sandbox manager's exec route (url: …/sbx/sandboxes/
// {id}/execs/{eid}, from model/sandboxes.js execSrc) — a terminal's shell,
// ended. Called by the page itself through xbind (its frame token), so the
// manager sees the verified person, as it does the terminal's socket.
export async function endManagerExec(url) {
  const x = globalThis.xbin;
  const r = await (sandboxed() && x && x.fetch ? x.fetch : fetch)(url, { method: 'DELETE' });
  if (!r.ok && r.status !== 404 && r.status !== 409) throw new Error(`ending the terminal: error ${r.status}`);
}
// runConfig: a conversation's stored config and class, re-read (its view's
// newest page of one message: cheap).
export async function runConfig(id) {
  const v = await api(`/runs/${id}/view?limit=1`);
  return { config: (v && v.config) || {}, class: v && v.class };
}

// --- a run's memory blocks and session files ----------------------------------------------

// memory: a run's memory blocks, {key: value}.
export const memory = async (runId) => (await api(`/runs/${runId}`)).memory || {};
export const setMemory = (runId, key, value) => api(`/runs/${runId}/memory`, jbody({ key, value }, 'PUT'));
export const deleteMemory = (runId, key) => api(`/runs/${runId}/memory?key=${encodeURIComponent(key)}`, { method: 'DELETE' });

// files: a run's session files, metadata only ([{path, bytes, version, mime?,
// binary?}]); file: one text file with its content ({content, version}).
export const files = async (runId) => (await api(`/runs/${runId}/files`)) || [];
export const file = (runId, path) => api(`/runs/${runId}/file?path=${encodeURIComponent(path)}`);
// saveFile writes {path, content, version}: the version you loaded (0: a new
// file) — a write the agent made in between comes back as a 409, not lost.
export const saveFile = (runId, body) => api(`/runs/${runId}/file`, jbody(body, 'PUT'));
export const deleteFile = (runId, path) => api(`/runs/${runId}/file?path=${encodeURIComponent(path)}`, { method: 'DELETE' });

// rawFile is a file's bytes as a Blob (an attachment's preview, a download).
// Raw bytes go through xbin.fetch — the kit's api() parses JSON — so it
// takes this backend's prefix.
export async function rawFile(base, runId, path) {
  const r = await xbin.fetch(`${base}/runs/${runId}/raw?path=${encodeURIComponent(path)}`);
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return r.blob();
}

// --- the skill library ---------------------------------------------------------------------

// skills: [{name, description, content, owner?, lane?, updated}].
export const skills = async () => (await api('/skills')) || [];
// saveSkill: {name, description, content} — adds or replaces by name.
export const saveSkill = (s) => api('/skills', jbody(s, 'PUT'));
export const deleteSkill = (name) => api(`/skills/${encodeURIComponent(name)}`, { method: 'DELETE' });

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
