/**
 * agent-chrome-store.js — what xbin's chrome for the workspace agent holds
 * of the person's delegation (/vendor/agent-chrome-store.js, D232): the
 * state, the requests that wait, the proposed change sets, the standing
 * approvals — read from /api/xbin/delegate/… with the document's own
 * session, kept current by the `delegate` events xbind sends the person's
 * own sockets (docs/protocol/delegation.md §Events). No polling: an event
 * names a list and that list is read again; a reconnect reads all of it.
 *
 * One store a document (agentStore()). It must run in a document that holds
 * the person's own session — the shell, /xbin/agent — never in a tile's
 * frame: every grant goes out from here (approve, deny, takeBack, apply,
 * resume, setAdminChanges), and xbind refuses them from anything else.
 */
import { onEvent, onReconnect } from '/vendor/events-socket.js';

const BASE = '/api/xbin';

/**
 * call(path, {method, body}): one request with the person's session →
 * {ok, status, data}: never throws for an answer (a 403 carries what the
 * chrome must show next: `stepUp`, `unseen`); status 0 when xbind could not
 * be reached.
 */
export async function call(path, { method = 'GET', body } = {}) {
  try {
    const r = await fetch(BASE + path, { method, credentials: 'same-origin', ...(body !== undefined ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}) });
    const text = await r.text();
    let data = null;
    try { data = text ? JSON.parse(text) : null; } catch { data = { error: text }; }
    return { ok: r.ok, status: r.status, data };
  } catch (e) {
    return { ok: false, status: 0, data: { error: "xbin can't be reached" } };
  }
}

// the refusal of a 409 that says the agent is stopped ('' for any other answer)
const stopOf = (r) => (r.status === 409 && r.data && typeof r.data.refusal === 'string' ? r.data.refusal : '');

const say = (r, dflt) => (r.data && r.data.error) || (r.status ? `${dflt} (${r.status})` : "xbin can't be reached");

class AgentStore {
  constructor() {
    this.state = null;      // GET /delegate/state (null: not read yet, or no delegation here)
    this.llm = null;        // GET /llm: is a language model connected
    this.me = null;         // GET /whoami
    this.requests = [];     // pending requests, oldest first
    this.failed = [];       // starts that were approved and didn't open, which the person may try again (D250)
    this.changesets = [];   // every change set of the person's (proposed first matter)
    this.approvals = [];    // standing approvals
    this.sessions = [];     // the person's tile agent sessions (for the ones that wait)
    this.error = '';        // why the last read failed ('' fine)
    this.loaded = false;
    this.subs = new Set();
    this.started = false;
  }

  /** subscribe(fn) → unsubscribe: fn() after anything here changed. The first subscriber starts the reads and the events. */
  subscribe(fn) {
    this.subs.add(fn);
    if (!this.started) this.start();
    return () => this.subs.delete(fn);
  }

  emit() { for (const fn of [...this.subs]) { try { fn(); } catch { /* a view that is gone */ } } }

  start() {
    this.started = true;
    onEvent((e) => {
      // a session of the person's changed status or came or went (`term`: it may wait for them now): the tray
      // lists those too. Not `session` — that is every event of a running agent
      if (e && e.type === 'term') { this.readSessions(); return; }
      if (!e || e.type !== 'delegate') return;
      const op = e.data && e.data.op;
      // the count rides on the state: every op reads it again, then the list it names
      this.readState();
      if (op === 'request') this.readRequests();
      else if (op === 'approval') this.readApprovals();
      else if (op === 'changeset') this.readChangesets();
    });
    onReconnect(() => this.refresh());
    this.refresh();
  }

  /** refresh(): read everything again (the first load, a reconnect, "Check again"). */
  async refresh() {
    const [me, llm] = await Promise.all([call('/whoami'), call('/llm')]);
    if (me.ok) this.me = me.data;
    if (llm.ok) this.llm = llm.data;
    await this.readState(false);
    await Promise.all([this.readRequests(false), this.readChangesets(false), this.readApprovals(false), this.readSessions(false)]);
    this.loaded = true;
    this.emit();
  }

  async readState(emit = true) {
    const r = await call('/delegate/state');
    // 404: an xbind without delegation; 403: no delegation for this caller — nothing to show either way
    this.error = r.status === 0 ? say(r, '') : '';
    if (r.ok) this.state = r.data; else if (r.status) this.state = null;
    if (emit) this.emit();
  }

  async readRequests(emit = true) {
    // ?failed=1: the starts that failed to open and may be tried again, beside the pending ones (an xbind before D250 ignores it)
    const r = await call('/delegate/requests?failed=1');
    if (r.ok) {
      const all = (r.data && r.data.requests) || [];
      this.requests = all.filter((x) => x.state !== 'failed');
      this.failed = all.filter((x) => x.state === 'failed');
    }
    if (emit) this.emit();
  }

  async readChangesets(emit = true) {
    const r = await call('/delegate/changesets');
    if (r.ok) this.changesets = (r.data && r.data.changesets) || [];
    if (emit) this.emit();
  }

  async readApprovals(emit = true) {
    const r = await call('/delegate/approvals');
    if (r.ok) this.approvals = (r.data && r.data.approvals) || [];
    if (emit) this.emit();
  }

  /** readSessions(): the person's live tile agent sessions (GET /term/sessions: the ones that wait show in Needs you). */
  async readSessions(emit = true) {
    const r = await call('/term/sessions');
    if (r.ok && Array.isArray(r.data)) {
      const next = r.data.filter((x) => x && x.kind === 'agent');
      const same = JSON.stringify(next.map((x) => [x.id, x.status, x.pending, x.questions])) === JSON.stringify(this.sessions.map((x) => [x.id, x.status, x.pending, x.questions]));
      this.sessions = next;
      if (same) return;
    }
    if (emit) this.emit();
  }

  /** request(id): one request as xbind reads it now — the only thing a sheet is ever drawn from. null: not the person's, or gone. */
  async request(id) {
    const r = await call(`/delegate/requests/${encodeURIComponent(id)}`);
    return r.ok ? r.data : null;
  }

  /** changeset(id): one change set as xbind reads it now (null: not the person's, or gone). */
  async changeset(id) {
    const r = await call(`/delegate/changesets/${encodeURIComponent(id)}`);
    return r.ok ? r.data : null;
  }

  // ---- what only the person's own session may send ----

  /** approve(id, body) → {ok, error?, request?}: body is ask.js-free — {grant, scope?, unseen?} (agent-chrome-words.js approveBody). */
  async approve(id, body) {
    const r = await call(`/delegate/requests/${encodeURIComponent(id)}/approve`, { method: 'POST', body });
    this.readState(); this.readRequests(); if (body && body.grant !== 'once') this.readApprovals();
    if (r.ok) return { ok: true, request: r.data && r.data.request };
    // 409 {changed, reload}: what the sheet showed changed since — nothing decided; the sheet looks again (D250 review L2)
    if (r.status === 409 && r.data && r.data.reload) return { ok: false, changed: r.data.changed || true, error: say(r, 'It changed since your sheet showed it') };
    // 409 {refusal}: the agent is stopped (off, paused, held, no agent) — nothing can be allowed now; the sheet says why
    const stopped = stopOf(r);
    return { ok: false, stopped, gone: !stopped && (r.status === 409 || r.status === 404), error: say(r, "It couldn't be approved") };
  }

  /** retry(id): the person's Try again on a start that failed to open — a ticket again, the agent files nothing new (D250). */
  async retry(id, body = {}) {
    const r = await call(`/delegate/requests/${encodeURIComponent(id)}/retry`, { method: 'POST', body });
    this.readState(); this.readRequests();
    if (r.ok) return { ok: true };
    if (r.status === 409 && r.data && r.data.reload) return { ok: false, changed: r.data.changed || true, error: say(r, 'It changed since your sheet showed it') };
    return { ok: false, stopped: stopOf(r), error: say(r, "It couldn't be tried again") };
  }

  async deny(id) {
    const r = await call(`/delegate/requests/${encodeURIComponent(id)}/deny`, { method: 'POST', body: {} });
    this.readState(); this.readRequests();
    return r.ok ? { ok: true } : { ok: false, gone: r.status === 409 || r.status === 404, error: say(r, "It couldn't be denied") };
  }

  /** takeBack(id): end a standing approval (a write running under it is cut). */
  async takeBack(id) {
    const r = await call(`/delegate/approvals/${encodeURIComponent(id)}`, { method: 'DELETE' });
    await Promise.all([this.readApprovals(false), this.readState(false)]);
    this.emit();
    return r.ok || r.status === 204 ? { ok: true } : { ok: false, error: say(r, "It couldn't be taken back") };
  }

  /**
   * apply(id, items, password?) → {ok, set} | {ok: false, stepUp: 'password' |
   * 'signin'} | {ok: false, error}: approve the ticked items of a change
   * set. xbind asks for a recent sign-in first when it has none.
   */
  async apply(id, items, password = '', ticks = {}) { // ticks {signin, trust}: the start lines whose own sign-in lines the person ticked (D230)
    const signin = ticks.signin || [], trust = ticks.trust || [];
    const r = await call(`/delegate/changesets/${encodeURIComponent(id)}/approve`, { method: 'POST',
      // knows: the line kinds newer than every client's that this chrome draws for what they are — xbind applies a line of one only then (D246, D248);
      // shown: what the trust line said trusting means (agent-chrome-words.js approveBody; signins.TrustShown)
      body: { items, knows: ['access.set', 'builtin.update'], ...(password ? { password } : {}), ...(signin.length ? { signin } : {}), ...(trust.length ? { trust, shown: 'signin+home' } : {}) } });
    this.readState(); this.readChangesets();
    if (r.ok) return { ok: true, set: r.data };
    if (r.status === 403 && r.data && r.data.stepUp) return { ok: false, stepUp: r.data.stepUp, error: password ? say(r, 'That password was not accepted') : '' };
    return { ok: false, stopped: stopOf(r), error: say(r, "The changes couldn't be applied") };
  }

  /** ask(id, items?) → {ok, set}: send the lines of a plan that need an admin to the workspace's admins, as a request from the person — their own click. */
  async ask(id, items) {
    const r = await call(`/delegate/changesets/${encodeURIComponent(id)}/ask`, { method: 'POST', body: items && items.length ? { items } : {} });
    this.readChangesets();
    return r.ok ? { ok: true, set: r.data } : { ok: false, stopped: stopOf(r), error: say(r, "The admins couldn't be asked") };
  }

  async reject(id) {
    const r = await call(`/delegate/changesets/${encodeURIComponent(id)}/reject`, { method: 'POST', body: {} });
    this.readState(); this.readChangesets();
    return r.ok ? { ok: true, set: r.data } : { ok: false, error: say(r, "It couldn't be rejected") };
  }

  /** undo(id) → {ok, set}: a NEW change set that undoes what was applied — to review like any other; nothing is undone by this. */
  async undo(id) {
    const r = await call(`/delegate/changesets/${encodeURIComponent(id)}/undo`, { method: 'POST', body: {} });
    this.readState(); this.readChangesets();
    return r.ok ? { ok: true, set: r.data } : { ok: false, error: say(r, 'Nothing of it can be undone') };
  }

  /** setPaused(paused): pause at once (the safe direction), or resume — the person's own click. */
  async setPaused(paused) {
    const r = await call('/delegate/state', { method: 'PUT', body: { paused: !!paused } });
    if (r.ok) this.state = r.data;
    this.emit();
    return r.ok ? { ok: true } : { ok: false, error: say(r, paused ? "It couldn't be paused" : "It couldn't be resumed") };
  }

  async setAdminChanges(on) {
    const r = await call('/delegate/state', { method: 'PUT', body: { adminChanges: !!on } });
    if (r.ok) this.state = r.data;
    this.emit();
    return r.ok ? { ok: true } : { ok: false, error: say(r, "That couldn't be changed") };
  }

  /** setNet({web?, mcp?, mcpReadOnly?}): the person's own web and MCP settings (D239) — on only while the workspace policy allows (409 otherwise, said). */
  async setNet(patch) {
    const r = await call('/delegate/state', { method: 'PUT', body: patch });
    if (r.ok) this.state = r.data;
    this.emit();
    return r.ok ? { ok: true } : { ok: false, error: say(r, "That couldn't be changed") };
  }

  /** setWorkspace(on): an admin turns workspace agents on or off for everyone. */
  async setWorkspace(on) {
    const r = await call('/delegate/workspace', { method: 'PUT', body: { enabled: !!on } });
    await this.readState(false);
    this.emit();
    return r.ok ? { ok: true } : { ok: false, error: say(r, "That couldn't be changed") };
  }

  /** setWho(who): an admin chooses who gets an agent — 'everyone', or 'me' (themselves; people and orgs are added as access later). */
  async setWho(who) {
    const r = await call('/delegate/workspace', { method: 'PUT', body: { who } });
    await this.readState(false);
    this.emit();
    return r.ok ? { ok: true } : { ok: false, error: say(r, "That couldn't be changed") };
  }

  /** audit(before?) → {ok, calls}: a page of what the agent did, newest first. */
  async audit(before = '') {
    const r = await call(`/delegate/audit${before ? `?before=${encodeURIComponent(before)}` : ''}`);
    return r.ok ? { ok: true, calls: (r.data && r.data.calls) || [], skipped: (r.data && r.data.skipped) || 0 } : { ok: false, calls: [], error: say(r, "The list couldn't be read") };
  }

  /** prefs() / setPrefs(patch): the person's agent preferences (GET / PUT /agent/prefs: a merge patch). */
  async prefs() {
    const r = await call('/agent/prefs');
    return r.ok ? r.data : null;
  }

  /** providers() → the coding agents this xbind runs, with their modes (GET /agent/providers: [{id, name, modes: [{id, name, explicit?}], …}]); [] when unread. */
  async providers() {
    const r = await call('/agent/providers');
    return r.ok && Array.isArray(r.data) ? r.data : [];
  }

  async setPrefs(patch) {
    const r = await call('/agent/prefs', { method: 'PUT', body: patch });
    return r.ok ? { ok: true, prefs: r.data } : { ok: false, error: say(r, "That couldn't be saved") };
  }
}

let store = null;
/** agentStore(): the document's one store. */
export function agentStore() {
  if (!store) store = new AgentStore();
  return store;
}
