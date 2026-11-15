// web/agent-prefs.js — a person's agent preferences, as the tile window's
// launcher and the Agent tab use them (D229): the session kind and harness
// they last started, and per harness the mode, model and settings a new
// session starts with. They live on the server (GET/PUT
// /api/xbin/agent/prefs, docs/protocol/agents.md), so every browser the
// person signs into starts a session the same way.
//
// Before D229 the launcher kept its last choice in this browser
// (`localStorage['bx-term-lastkind']`). That value is still read and
// written: it is what a first load moves to the server, and what answers
// when the server can't — an xbind without the route, or a credential the
// route refuses (the root token, --no-auth). Nothing here throws: a failed
// read is "no preferences", a failed write is dropped.
//
// Imports nothing and takes its fetch/storage as arguments (makeAgentPrefs),
// so hack/agent-prefs.test.mjs runs it under node (`make js-test`).

export const LAST_KIND = 'bx-term-lastkind';
const URL_ = '/api/xbin/agent/prefs';
const JSON_HDR = { 'Content-Type': 'application/json' };

// kindOf(prefs) → the launcher's last choice, {kind:'shell'} |
// {kind:'agent', provider}, or null.
export function kindOf(p) {
  if (p?.lastKind === 'shell') return { kind: 'shell' };
  if (p?.lastKind === 'agent' && p.harness) return { kind: 'agent', provider: p.harness };
  return null;
}

// startWith(prefs, provider) → what a new session of `provider` (a row of
// GET /agent/providers) starts with: {mode?, model?, options?} — the saved
// mode only while the provider still lists it and it isn't an explicit
// (bypass) one, which is never a default.
export function startWith(p, provider) {
  const h = p?.harnesses?.[provider?.id];
  if (!h) return {};
  const out = {};
  const modes = provider.modes || [];
  if (h.mode && (!modes.length || modes.some((m) => m.id === h.mode && !m.explicit))) out.mode = h.mode;
  if (h.model) out.model = h.model;
  if (h.options && Object.keys(h.options).length) out.options = { ...h.options };
  return out;
}

// optionPatch(provider, option, value) → the merge patch that remembers one
// setting the person changed in a session of `provider`, or null when it
// isn't one to keep: `option` is {id, category?, name?} as the agent
// reported it ('mode' for the permission mode). An explicit mode is never
// remembered.
export function optionPatch(provider, option, value) {
  if (!provider?.id || !option?.id || typeof value !== 'string' || !value || value.length > 256) return null;
  const isMode = option.id === 'mode' || option.category === 'mode' || (option.name || '').toLowerCase() === 'mode';
  if (isMode) {
    const m = (provider.modes || []).find((x) => x.id === value);
    if ((provider.modes || []).length && (!m || m.explicit)) return null;
    return { harnesses: { [provider.id]: { mode: value } } };
  }
  if (option.id === 'model' || option.category === 'model') return { harnesses: { [provider.id]: { model: value } } };
  if (option.id.length > 64) return null;
  return { harnesses: { [provider.id]: { options: { [option.id]: value } } } };
}

// credentialsChoice(prefs, provider) → the launcher's control for what a new
// session of `provider` runs its model calls on, or null when there is
// nothing to choose: it shows only where both can serve — the row's `llm`
// (GET /agent/providers, D226) says the workspace's LLM connector can and no
// rule forces it — and the server keeps this person's preferences. value is
// their saved choice ('' = none: the workspace's default decides).
export function credentialsChoice(p, provider) {
  const l = provider?.llm;
  if (!p || !provider?.id || !l || !l.connector || l.forced || l.use === 'unavailable') return null;
  const saved = p.harnesses?.[provider.id]?.credentials;
  return {
    value: saved === 'own' || saved === 'connector' ? saved : '',
    options: [{ value: '', name: 'workspace default' }, { value: 'own', name: 'your sign-in' }, { value: 'connector', name: 'LLM by xbin' }],
  };
}

// credentialsPatch(provider, value) → the merge patch that saves that choice
// ('' takes it back), or null for anything else.
export function credentialsPatch(provider, value) {
  if (!provider?.id || !['', 'own', 'connector'].includes(value)) return null;
  return { harnesses: { [provider.id]: { credentials: value || null } } };
}

// merge(target, patch): RFC 7386, as the server applies a PUT.
function merge(t, p) {
  const out = t && typeof t === 'object' && !Array.isArray(t) ? { ...t } : {};
  for (const [k, v] of Object.entries(p)) {
    if (v === null) delete out[k];
    else if (typeof v === 'object' && !Array.isArray(v)) out[k] = merge(out[k], v);
    else out[k] = v;
  }
  return out;
}

export function makeAgentPrefs({ fetch: f = globalThis.fetch, storage = globalThis.localStorage } = {}) {
  let cur = null; // the server's preferences once read (null: not read, or it can't say)
  let loading = null;
  const local = () => { try { return JSON.parse(storage?.getItem(LAST_KIND) || 'null'); } catch { return null; } };
  const setLocal = (k) => { try { storage?.setItem(LAST_KIND, JSON.stringify(k)); } catch { /* storage off */ } };
  // writes go one at a time, in order; the server's answer replaces what is
  // held here only once nothing newer waits (a late answer must not undo a
  // choice made since)
  let chain = Promise.resolve(), waiting = 0;
  const put = (patch) => {
    waiting++;
    chain = chain.then(async () => {
      try {
        const r = await f(URL_, { method: 'PUT', headers: JSON_HDR, body: JSON.stringify(patch) });
        const j = r.ok ? await r.json() : null;
        if (--waiting === 0 && j) cur = j;
      } catch { waiting--; /* offline: the next load reads what the server has */ }
    });
    return chain;
  };
  const api = {
    // load() → the preferences (null when the server can't say). The first
    // load moves this browser's last choice to a server that has none.
    load(force = false) {
      if (loading && !force) return loading;
      loading = (async () => {
        try {
          const r = await f(URL_);
          if (!r.ok) { cur = null; return null; }
          cur = await r.json();
        } catch { cur = null; return null; }
        const was = local();
        if (!cur.lastKind && !cur.harness && was && (was.kind === 'shell' || (was.kind === 'agent' && was.provider))) {
          await put(was.kind === 'shell' ? { lastKind: 'shell' } : { lastKind: 'agent', harness: was.provider });
        }
        return cur;
      })();
      return loading;
    },
    get current() { return cur; },
    // lastKind() → the launcher's last choice: the server's, else this browser's
    lastKind: () => kindOf(cur) ?? local(),
    // rememberKind(k): here at once (the menu reads it synchronously), in
    // this browser, and on the server
    rememberKind(k) {
      if (!k || (k.kind !== 'shell' && !(k.kind === 'agent' && k.provider))) return;
      setLocal(k);
      const patch = k.kind === 'shell' ? { lastKind: 'shell' } : { lastKind: 'agent', harness: k.provider };
      if (cur) cur = merge(cur, patch);
      put(patch);
    },
    // harness: the harness a new agent session starts with, when one was picked
    get harness() { return cur?.harness || (local()?.kind === 'agent' ? local().provider : '') || ''; },
    startWith: (provider) => startWith(cur, provider),
    // whose credentials a new session of `provider` runs on: the control
    // (null: nothing to choose), and saving a choice — resolved once the
    // server has it, so the caller can ask again what a session would run on
    credentialsChoice: (provider) => credentialsChoice(cur, provider),
    rememberCredentials(provider, value) {
      const patch = credentialsPatch(provider, value);
      if (!patch || !cur) return Promise.resolve();
      cur = merge(cur, patch);
      return put(patch);
    },
    // rememberOption(provider, option, value): a setting changed in a session
    rememberOption(provider, option, value) {
      const patch = optionPatch(provider, option, value);
      if (!patch || !cur) return; // a server that keeps none: nothing to remember it in
      cur = merge(cur, patch);
      put(patch);
    },
  };
  return api;
}

// The page's one store: the frames and Agent tabs of a shell share it.
let shared = null;
export const agentPrefs = () => (shared ||= makeAgentPrefs());
