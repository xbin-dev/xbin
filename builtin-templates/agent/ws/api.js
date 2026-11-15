// ws/api.js — every call the workspace agent's page makes: its backend's
// workspace routes (API.md §Workspace mode), the conversation's own run
// routes, and the one read it makes of xbind itself — the full connection
// view, blocked tiles named, which xbind answers only for this page's own
// frame (docs/protocol/delegation.md §Agent sessions; /docs/protocol/agents.md).
//
// Each call answers {ok, status, data}: the page shows a refusal in its own
// words (a 409 model_taint carries the new-session offer), so nothing here
// throws for a status.

const self = () => `/api/${globalThis.xbin?.self ?? ''}`;

/** call(path, opts): this backend's route (absolute: /api/xbin/… for xbind's) → {ok, status, data}. */
export async function call(path, { method = 'GET', body, signal } = {}) {
  const url = path.startsWith('/api/') ? path : self() + path;
  let r;
  try {
    r = await globalThis.xbin.fetch(url, { method, signal, ...(body !== undefined ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}) });
  } catch (e) {
    return { ok: false, status: 0, data: { error: String((e && e.message) || e || 'no connection') } };
  }
  let data = null;
  try { const t = await r.text(); data = t ? JSON.parse(t) : null; } catch { data = null; }
  return { ok: r.ok, status: r.status, data: data ?? {} };
}

const q = (o) => { const s = new URLSearchParams(Object.entries(o).filter(([, v]) => v != null && v !== '')).toString(); return s ? '?' + s : ''; };

export const ws = {
  state: () => call('/ws/state'),
  sessions: () => call('/ws/sessions'),
  main: () => call('/ws/sessions', { method: 'POST', body: { main: true } }),
  // "New session…": copies the scope and model of the session in view (from)
  newSession: (from, extra = {}) => call('/ws/sessions', { method: 'POST', body: { ...(from ? { from } : {}), ...extra } }),
  patch: (id, body) => call(`/ws/sessions/${id}`, { method: 'PATCH', body }),
  drop: (id) => call(`/ws/sessions/${id}`, { method: 'DELETE' }),
  connection: (id, whatIf) => call(`/ws/sessions/${id}/connection${q({ whatIf })}`),
  cards: (id) => call(`/ws/sessions/${id}/cards`),
  putBrief: (id, bid, body) => call(`/ws/sessions/${id}/briefs/${bid}`, { method: 'PUT', body }),
  startDefaults: (id, bid) => call(`/ws/sessions/${id}/briefs/${bid}/start-defaults`),
  startBrief: (id, bid) => call(`/ws/sessions/${id}/briefs/${bid}/start`, { method: 'POST', body: {} }),
  needs: () => call('/ws/needs'),
  empty: (session) => call(`/ws/empty${q({ session })}`),
  notebook: (session) => call(`/ws/notebook${q({ session })}`),
  page: (slug, session) => call(`/ws/notebook/${encodeURIComponent(slug)}${q({ session })}`),
  putPage: (slug, body) => call(`/ws/notebook/${encodeURIComponent(slug)}`, { method: 'PUT', body }),
  dropPage: (slug) => call(`/ws/notebook/${encodeURIComponent(slug)}`, { method: 'DELETE' }),
  prefs: () => call('/ws/prefs'),
  putPrefs: (body) => call('/ws/prefs', { method: 'PUT', body }),
  // the conversation (the template's run routes, open in workspace mode)
  send: (run, text, clientId) => call(`/runs/${run}/message`, { method: 'POST', body: { text, ...(clientId ? { clientId } : {}) } }),
  stop: (run) => call(`/runs/${run}/interrupt`, { method: 'POST', body: {} }),
  takeBack: (run, iid) => call(`/runs/${run}/inbox/${iid}`, { method: 'DELETE' }),
  // xbind's own: the full view of what a session is connected to, for this page's frame only
  fullConnection: (handle, whatIf) => call(`/api/xbin/agents/connection${q({ session: handle, whatIf })}`),
  // …and the person's delegation state, which the page's credential may read (docs/protocol/delegation.md): asked when the
  // backend could not — xbind refuses a paused or turned-off agent its every call, this read too (state: delegationError)
  delegation: () => call('/api/xbin/delegate/state'),
};

/** state(): GET /ws/state, with `delegation` read from xbind itself when the backend was refused it. */
export async function state() {
  const r = await ws.state();
  if (r.ok && r.data && !r.data.delegation && r.data.delegationError) {
    const d = await ws.delegation();
    if (d.ok && d.data && typeof d.data === 'object') r.data.delegation = d.data;
  }
  return r;
}
