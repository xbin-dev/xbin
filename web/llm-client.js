/**
 * llm-client.js — every call the LLM connector's surfaces make (D226,
 * /docs/protocol/llm.md), in one place: a route that moves is a change
 * here and nowhere else.
 *
 *   const llm = llmClient(fetcher);       // fetch by default (the person's
 *   const r = await llm.config();         // session); the admin console
 *   if (r.ok) …r.data… else …r.error…     // passes xbin.fetch
 *
 * Every method resolves {ok, status, data, error, locked} and never
 * throws: `error` is the server's words (or what a status means here),
 * `locked` the settings a 409 names as the host's. Pure, no imports:
 * hack/llm-kit.test.mjs runs it under node.
 */
const q = encodeURIComponent;
const BASE = '/api/xbin/llm';

// errorText(status, data): the server's words — management routes answer
// {error}, the OpenAI-compatible ones {error: {message}} — or the status.
export function errorText(status, data) {
  const e = data && typeof data === 'object' ? data.error : null;
  if (typeof e === 'string' && e) return e;
  if (e && typeof e === 'object' && e.message) return String(e.message);
  if (status === 404) return 'this xbind has no LLM connector (it predates it)';
  if (status === 403) return 'the LLM connector’s settings are a workspace admin’s';
  return status ? `error ${status}` : 'xbind can’t be reached';
}

export function llmClient(fetcher) {
  const f = fetcher ?? ((...a) => globalThis.fetch(...a));
  async function call(method, path, body) {
    try {
      const r = await f(BASE + path, body === undefined ? { method, headers: { Accept: 'application/json' } }
        : { method, headers: { Accept: 'application/json', 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      const text = await r.text().catch(() => '');
      let data = null;
      try { data = text ? JSON.parse(text) : null; } catch { data = null; }
      return { ok: r.ok, status: r.status, data, error: r.ok ? '' : errorText(r.status, data), locked: (!r.ok && Array.isArray(data?.locked)) ? data.locked : [] };
    } catch (e) {
      return { ok: false, status: 0, data: null, error: errorText(0, null) + (e?.message ? ` (${e.message})` : ''), locked: [] };
    }
  }
  const query = (o) => {
    const s = Object.entries(o ?? {}).filter(([, v]) => v != null && v !== '').map(([k, v]) => `${k}=${q(v)}`).join('&');
    return s ? `?${s}` : '';
  };
  return {
    call,
    // the connector as the caller meets it: {configured, enabled, callable, admin?, user?, quota?, …}
    status: () => call('GET', ''),
    // admins: the configuration in force, with `locks`
    config: () => call('GET', '/config'),
    // {enabled?, people?, defaults?: {model?, roles?}, aliases?} → the view
    putConfig: (changes) => call('PUT', '/config', changes),
    putUpstream: (name, body) => call('PUT', `/upstreams/${q(name)}`, body),
    deleteUpstream: (name) => call('DELETE', `/upstreams/${q(name)}`),
    // → {ok, model?, models?, status?, ms, tokens?, error?}
    testUpstream: (name, model) => call('POST', `/upstreams/${q(name)}/test`, model ? { model } : {}),
    // {period?, from?, to?, user?, component?, model?} → {rows, totals, scope}
    usage: (filter) => call('GET', `/usage${query(filter)}`),
    // → {quotas, locks, effective?}
    quotas: (user) => call('GET', `/quotas${query({ user })}`),
    // {workspace?, default?, users?: {id: quota|null}, orgs?: {id: quota|null}}
    putQuotas: (rules) => call('PUT', '/quotas', rules),
    // one subject's quota: 'workspace' | 'default' | 'user:<id>' | 'org:<id>'; null removes
    putQuota: (subject, quota) => call('PUT', '/quotas', quotaRules(subject, quota)),
    // the models on offer (the OpenAI-compatible list)
    models: () => call('GET', '/v1/models'),
    // the setup alert, gone for the workspace
    dismissSetup: () => call('POST', '/setup/dismiss'),
    // is the setup alert up (GET /alerts, an admin's)? → true | false
    setupPending: async () => {
      try {
        const r = await f('/api/xbin/alerts', { method: 'GET', headers: { Accept: 'application/json' } });
        return r.ok && ((JSON.parse(await r.text()) ?? {}).alerts ?? []).some((a) => a?.kind === 'llm-setup');
      } catch { return false; }
    },
  };
}

// directory(fetcher) → {people, orgs}: the ids the quota overrides offer —
// GET /users and GET /orgs, each empty when it can't be read.
export async function directory(fetcher) {
  const f = fetcher ?? ((...a) => globalThis.fetch(...a));
  const ids = async (path, key) => {
    try {
      const r = await f(`/api/xbin${path}`, { method: 'GET', headers: { Accept: 'application/json' } });
      if (!r.ok) return [];
      return ((JSON.parse(await r.text()) ?? {})[key] ?? []).map((x) => x?.id).filter(Boolean).sort();
    } catch { return []; }
  };
  const [people, orgs] = await Promise.all([ids('/users', 'users'), ids('/orgs', 'orgs')]);
  return { people, orgs };
}

// quotaRules(subject, quota) → PUT /llm/quotas's body for one subject.
export function quotaRules(subject, quota) {
  const s = String(subject ?? '');
  if (s === 'workspace' || s === 'default') return { [s]: quota };
  if (s.startsWith('user:')) return { users: { [s.slice(5)]: quota } };
  if (s.startsWith('org:')) return { orgs: { [s.slice(4)]: quota } };
  return {};
}

// quotaOf(quotas, subject) → that subject's quota in GET /llm/quotas's
// `quotas`, or null.
export function quotaOf(quotas, subject) {
  const s = String(subject ?? '');
  if (s === 'workspace' || s === 'default') return quotas?.[s] ?? null;
  if (s.startsWith('user:')) return quotas?.users?.[s.slice(5)] ?? null;
  if (s.startsWith('org:')) return quotas?.orgs?.[s.slice(4)] ?? null;
  return null;
}

// quotaLockPath(subject) → the `locks` path that holds a subject's quota.
export function quotaLockPath(subject) {
  const s = String(subject ?? '');
  return `quotas.${s.startsWith('user:') ? 'users' : s.startsWith('org:') ? 'orgs' : s}`;
}
