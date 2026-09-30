// model/harness-store.js — coding harnesses' state and calls for both views
// (D147 §4), as app.harness: the catalog (GET /harnesses,
// read when a view first needs it), the person's preferences (§4.3.12) —
// "Who answers" (prefs/agent) and the sandbox last used per harness
// (prefs/harness-sandbox), both xbind prefs like prefs/class; Auto / Always
// approve per harness (GET/PUT /prefs/harness-mode, the tile's own store: the
// engine reads it when it starts a conversation or a child for them) — what a
// new ask carries, and what a view does to a harness run: its mode and
// options, a permission's option, a question's answer, sign-in, the adapter's
// log, a message that may interrupt, stop, cancel, retry. What the controls
// say is model/harness.js. No lit, no DOM, no dialogs.
//
// Every change emits 'harness' on the app. Calls throw as the backend
// refuses: e.message says why, e.status the status, e.data the whole answer
// (a sign-in to confirm carries {confirm: true}) — except permit and answer,
// whose refusal the card says (Session.noteApprove), as an approval's is.
//
// A call about a run goes to its home (model/homes.js): in a person's
// partition a shared conversation's run is the global instance's. In the
// global instance's own page no coding agent answers new chats
// (model/harness-homes.js harnessesHere: picked() is null there).
import { catalogOf, findHarness } from './harness.js';
import { at, homeOf, runOfPath } from './homes.js';
import { harnessesHere } from './harness-homes.js';

const cid = () => 'h' + Math.random().toString(36).slice(2) + Date.now().toString(36);

// call: the tile's API over xbin.fetch, at the home of the run the path
// names (/runs/<id>…), keeping the refusal whole.
async function call(path, method = 'GET', body = undefined, text = false) {
  const x = globalThis.xbin;
  const opts = body === undefined ? { method } : { method, body: JSON.stringify(body) };
  const r = await x.fetch(`/api/${x.self}${path}`, at(homeOf(runOfPath(path)), opts));
  const raw = await r.text();
  if (text && r.ok) return raw;
  let data;
  try { data = raw ? JSON.parse(raw) : null; } catch { data = raw; }
  if (!r.ok) {
    const e = new Error((data && typeof data === 'object' && data.error) || (typeof data === 'string' && data) || `error ${r.status}`);
    e.status = r.status;
    e.data = data;
    throw e;
  }
  return data;
}

// xbind's per-person prefs (/api/xbin/prefs/<key>): '' / {} when unset.
async function pref(key, dflt) {
  try {
    const r = await globalThis.xbin.fetch(`/api/xbin/prefs/${key}`);
    if (!r.ok) return dflt;
    const v = await r.json();
    return v && typeof v === typeof dflt ? v : dflt;
  } catch { return dflt; }
}
const savePref = (key, v) => globalThis.xbin.fetch(`/api/xbin/prefs/${key}`, { method: 'PUT', body: JSON.stringify(v) });

export const AGENT = 'agent'; // prefs/agent: the built-in agent answers

export function createHarnessStore(app) {
  let loaded = false, inflight = null;
  const emit = () => app.emit('harness');
  // the view's own run (not current()'s merged copy): its park
  const parkOf = (runId) => app.session.views.get(runId)?.run?.pendingState?.park || app.session.runs.get(runId)?.pendingState?.park;

  const hs = {
    catalog: catalogOf(null), // GET /harnesses (model/harness.js catalogOf)
    error: '',                // why the catalog could not be read
    pick: AGENT,              // "Who answers" for new chats: 'agent' or a harness id (prefs/agent)
    sandboxes: {},            // the sandbox last used per harness: {provider: ref} (prefs/harness-sandbox)
    modes: {},                // Auto / Always approve per harness, as set: {provider: 'auto' | 'approve'}
    options: {},              // the next new chat's config options per harness: {provider: {model: …}}

    // load reads the catalog (probe: a running sandbox checked now, §4.2.1)
    // and the per-person settings; one read at a time.
    load(probe = '') {
      if (inflight) return inflight;
      inflight = Promise.all([
        call(`/harnesses${probe ? '?probe=' + encodeURIComponent(probe) : ''}`).then((r) => { hs.catalog = catalogOf(r); hs.error = ''; }, (e) => { hs.error = e.message; }),
        call('/prefs/harness-mode').then((r) => { hs.modes = (r && r.modes) || {}; }, () => {}),
        loaded ? null : pref('agent', '').then((v) => { if (v) hs.pick = v; }),
        loaded ? null : pref('harness-sandbox', {}).then((v) => { hs.sandboxes = v; }),
      ]).finally(() => { inflight = null; loaded = true; emit(); });
      return inflight;
    },
    // ensure reads it once a view needs it.
    ensure() { if (!loaded && !inflight) hs.load().catch(() => {}); },

    find(id) { return findHarness(hs.catalog, id); },
    // picked: the harness for new chats, while the catalog offers it and
    // this page may start one (else null: the built-in agent)
    picked() { const h = hs.pick !== AGENT && harnessesHere() ? hs.find(hs.pick) : null; return h && h.available ? h : null; },
    // choose: who answers new chats — remembered (prefs/agent).
    choose(id) {
      hs.pick = id || AGENT;
      savePref('agent', hs.pick).catch(() => {});
      emit();
    },
    // rememberSandbox: the sandbox a harness last ran in (prefs/harness-sandbox).
    rememberSandbox(provider, ref) {
      hs.sandboxes = { ...hs.sandboxes, [provider]: ref };
      savePref('harness-sandbox', hs.sandboxes).catch(() => {});
      emit();
    },
    // setOption: a config option for the next new chat with provider ('' drops it).
    setOption(provider, id, value) {
      const o = { ...(hs.options[provider] || {}) };
      if (value) o[id] = value; else delete o[id];
      hs.options = { ...hs.options, [provider]: o };
      emit();
    },
    // setting: the person's Auto / Always approve for provider (unset: approve).
    setting(provider) { return hs.modes[provider] || hs.find(provider)?.setting || 'approve'; },
    async setSetting(provider, mode) {
      const r = await call(`/prefs/harness-mode/${encodeURIComponent(provider)}`, 'PUT', { mode });
      hs.modes = { ...hs.modes, [r.provider || provider]: r.mode || mode };
      emit();
      return r;
    },

    // askPart: what a new ask carries for the harness picked (POST /ask
    // {harness: {provider, options?}}; its mode is the person's setting,
    // applied by the backend) — nothing while the built-in agent answers.
    askPart() {
      const h = hs.picked();
      if (!h) return {};
      const options = hs.options[h.id];
      return { harness: { provider: h.id, ...(options && Object.keys(options).length ? { options } : {}) } };
    },

    // --- a harness run --------------------------------------------------------------

    // get: GET /runs/{id}/harness — {harness, session, rules}.
    get(runId) { return call(`/runs/${runId}/harness`); },
    // setMode / setOptionOf: the live session's mode or a config option
    // (PATCH /runs/{id}/harness) — {harness}, the refreshed summary.
    setMode(runId, mode) { return call(`/runs/${runId}/harness`, 'PATCH', { mode }); },
    setOptionOf(runId, id, value) { return call(`/runs/${runId}/harness`, 'PATCH', { option: { id, value } }); },
    // permit answers a permission request with one of the harness's options
    // (pendingState.harness.options[].optionId) — or approve true/false and
    // let the backend pick; feedback (only with a rejection) is sent as the
    // next message. park: the ask it answers (default: the run's). A refusal
    // is said beside the card; answers whether it went.
    async permit(runId, { option, approve, feedback, park } = {}) {
      app.session.clearApproveNote(runId);
      const body = { park: park || parkOf(runId) || undefined };
      if (option) body.option = option;
      if (approve != null) body.approve = !!approve;
      if (feedback) body.feedback = feedback;
      try { await call(`/runs/${runId}/approve`, 'POST', body); return true; } catch (e) { app.session.noteApprove(runId, e); return false; }
    },
    // answer: a parked question — action accept (content: the form's values) | decline | cancel.
    async answer(runId, action, content, park) {
      app.session.clearApproveNote(runId);
      const body = { park: park || parkOf(runId) || '', action, ...(action === 'accept' ? { content: content || {} } : {}) };
      try { await call(`/runs/${runId}/harness/answer`, 'POST', body); return true; } catch (e) { app.session.noteApprove(runId, e); return false; }
    },
    // authenticate: an api-key or device-code sign-in (§4.2.6) — {ok, state}
    // or {ok, device: {url, message}}. The key is sent once and kept nowhere.
    // A shared sandbox answers 409 with e.data.confirm until confirm is true.
    authenticate(runId, method, { apiKey, confirm } = {}) {
      return call(`/runs/${runId}/harness/authenticate`, 'POST', { method, ...(apiKey ? { apiKey } : {}), ...(confirm ? { confirm: true } : {}) });
    },
    // log: the adapter's stderr, its last max bytes (≤ 64 KiB), as text.
    log(runId, max = 65536) { return call(`/runs/${runId}/harness/log?max=${Math.min(65536, Number(max) || 65536)}`, 'GET', undefined, true); },
    // steer: a message to a harness run (a child's too — its parent is told,
    // §4.3.13): queued while it works, or interrupt: true to cut its turn short.
    steer(runId, text, { interrupt = false, files } = {}) {
      return call(`/runs/${runId}/message`, 'POST', { text, clientId: cid(), ...(files ? { files } : {}), ...(interrupt ? { interrupt: true } : {}) });
    },
    stop(runId) { return call(`/runs/${runId}/interrupt`, 'POST'); },     // its turn; queued messages come back
    cancel(runId) { return call(`/runs/${runId}/cancel`, 'POST'); },      // for good (a child's link settles canceled)
    retry(runId) { return call(`/runs/${runId}/resume`, 'POST'); },       // "Signed in? Retry", "Retry resumes its session"
  };
  return hs;
}
