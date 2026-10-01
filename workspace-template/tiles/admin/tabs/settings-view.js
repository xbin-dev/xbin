/**
 * settings-view.js — the words, groups and requests of the admin console's
 * workspace → settings tab (tabs/settings.js; D175, D180, PD-55): pure
 * functions, no element state, unit-tested by hack/admin-settings.test.mjs.
 *
 * One xbind keeps every setting at GET/PUT /workspace-settings (D180). An
 * older one this console may meet (compat.md: the console lags or leads
 * xbind) is told apart by what it answers, never by a version:
 *  - its GET /workspace-settings has no partitionConsent → before D180: the
 *    partitioned tiles' switches are at v0.3.66's /workspace-policies (read
 *    and set there; it publishes `policies`, not `workspace-settings`, for
 *    them) — or, when that route is missing too, there are none;
 *  - GET /workspace-settings missing (Go's mux 404 without an error body) →
 *    before D175: no terminals settings.
 */

export const SETTINGS = [
  {
    key: 'baseAutoUpdate',
    group: 'terminals',
    label: 'Move terminals to a new base image automatically',
    detail: 'at their next start. A running terminal keeps its base until it ends.',
    on: 'on — a terminal (or agent session) on an older base starts on the new one and says so',
    off: 'off — terminals stay on their base; the window offers ⬆ base update',
    unreadable: 'base auto-update is off until the file is fixed',
    notice: (v) => v ? 'base auto-update is on' : 'base auto-update is off — terminal windows offer the update',
  },
  {
    key: 'partitionConsent',
    group: 'partitions',
    label: 'Ask each person before another partitioned tile uses their data',
    off: 'Off: a partitioned tile with a grant on another partitioned tile reaches the data kept there by every person who can open that other tile.',
    on: 'On: it reaches a person\'s data only once they allow it; each person is asked the first time a call is refused. Turning it off keeps their answers for later.',
    confirm: 'Calls between partitioned tiles that nobody has allowed yet are refused from the next call, and each person is asked on their first refusal.',
    unreadable: 'it counts as on (or keeps its last value) until the file is fixed',
    notice: (v) => v ? 'people are now asked before another partitioned tile uses their data' : 'partitioned tiles no longer ask people first (their answers are kept)',
  },
  {
    key: 'credentialResetConfirm',
    group: 'partitions',
    label: 'Credential resets wait for the person',
    off: 'Off: a sign-in link, password or SSO email an admin sets works at once.',
    on: 'On: a sign-in link, password or SSO email set by an admin for someone who holds partitions works only after they confirm, or 24 h after they\'re notified.',
    unreadable: 'it counts as on (or keeps its last value) until the file is fixed',
    notice: (v) => v ? 'credential resets now wait for the person' : 'credential resets work at once again',
  },
];

export const GROUPS = [
  { id: 'terminals', label: 'Terminals' },
  { id: 'partitions', label: 'Partitioned tiles' },
];

export const settingsOf = (group) => SETTINGS.filter((s) => s.group === group);
const PARTITION_KEYS = settingsOf('partitions').map((s) => s.key);

// missingRoute: Go's mux 404/405 without an error body — an xbind older
// than the route (a route's own 404 says why in `error`).
export const missingRoute = (r) => (r?.status === 404 || r?.status === 405) && !(r.body && typeof r.body === 'object' && r.body.error);

const errText = (r) => (r?.body && typeof r.body === 'object' && r.body.error) || `error ${r?.status}`;

/**
 * loadSettings(call) — call(path) answers {status, ok, body} for GET
 * /api/xbin<path>. → {values: {key: bool}, errors: {key: why},
 * groups: {terminals|partitions: {state: 'ok'|'absent'|'error', why?}},
 * legacy: the partitioned tiles' switches are v0.3.66's /workspace-policies}.
 */
export async function loadSettings(call) {
  const out = { values: {}, errors: {}, groups: {}, legacy: false };
  const s = await call('/workspace-settings');
  if (s.ok) {
    out.groups.terminals = { state: 'ok' };
    out.values.baseAutoUpdate = !!s.body?.baseAutoUpdate;
    Object.assign(out.errors, s.body?.errors ?? {});
    if (s.body?.error && !out.errors.baseAutoUpdate) out.errors.baseAutoUpdate = s.body.error; // before D180
    if (s.body && 'partitionConsent' in s.body) {
      out.groups.partitions = { state: 'ok' };
      for (const k of PARTITION_KEYS) out.values[k] = !!s.body[k];
      return out;
    }
  } else if (missingRoute(s)) {
    out.groups.terminals = { state: 'absent', why: 'this xbind has no workspace settings (it predates them)' };
  } else {
    out.groups.terminals = { state: 'error', why: errText(s) };
  }
  const p = await call('/workspace-policies');
  if (p.ok) {
    out.legacy = true;
    out.groups.partitions = { state: 'ok' };
    for (const k of PARTITION_KEYS) out.values[k] = !!p.body?.[k];
  } else if (missingRoute(p)) {
    out.groups.partitions = { state: 'absent', why: 'this xbind has no partitioned tiles' };
  } else {
    out.groups.partitions = { state: 'error', why: errText(p) };
  }
  return out;
}

// saveRequest(state, key, on): the PUT that sets one setting — {path, body}.
export function saveRequest(state, key, on) {
  const s = SETTINGS.find((x) => x.key === key);
  if (!s) throw new Error(`no setting ${key}`);
  const path = s.group === 'partitions' && state?.legacy ? '/workspace-policies' : '/workspace-settings';
  return { path, body: { [key]: on } };
}

// reloadOn(state, e): whether an event means the tab reads again. An xbind
// since D180 publishes `workspace-settings` for every write (and `policies`
// too, for older clients: ignored then, or the tab would read twice); an
// older one `policies` for the partitioned tiles' switches.
export function reloadOn(state, e) {
  if (e?.type === 'workspace-settings') return true;
  return e?.type === 'policies' && (!state || state.legacy);
}
