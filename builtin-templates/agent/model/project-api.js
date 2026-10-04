// model/project-api.js — the calls of Projects (API.md §Projects), each at
// the home its project or conversation lives in (model/homes.js): in a
// person's partition a project of their own numbers from 2^40 and lives in
// their partition, a team project's definition lives at the global
// instance — so a project id alone says where to call, as a conversation id
// does. Everywhere else there is one home.
//
//   projectApi(app, pid)  the routes of one project (/projects/{pid}/…)
//   taskApi(runId)        a task conversation's (/runs/{id}/task…)
//   scmApi(home)          the scm providers as a home sees them, and the
//                         person's sign-in (/projects/scm…)
//   listProjects(home, q) GET /projects at one home
//   createProject(home, body)
//
// Every call throws as the backend refuses, keeping what a view needs:
// e.status, e.refusal (signin, busy, dirty, limit, barred, class-internal,
// identity…) and e.data, the whole answer (a 412's version, a 409 dirty's
// repos). No lit, no DOM.
import { at, homeOf } from './homes.js';

// projCall: the tile's own API at a home, keeping the refusal whole.
export async function projCall(home, path, method = 'GET', body = undefined) {
  const x = globalThis.xbin;
  const opts = body === undefined ? { method } : { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) };
  const r = await x.fetch(`/api/${x?.self ?? ''}${path}`, at(home, opts));
  const raw = r.status === 204 ? '' : await r.text();
  let data;
  try { data = raw ? JSON.parse(raw) : null; } catch { data = raw; }
  if (!r.ok) {
    const e = new Error((data && typeof data === 'object' && data.error) || (typeof data === 'string' && data) || `error ${r.status}`);
    e.status = r.status;
    e.data = data;
    if (data && typeof data === 'object' && data.refusal) e.refusal = data.refusal;
    throw e;
  }
  return data;
}

// qs: a query string of the set values ('' when none).
export function qs(params) {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params || {})) {
    if (v === undefined || v === null || v === '' || v === false) continue;
    p.set(k, v === true ? '1' : String(v));
  }
  const s = p.toString();
  return s ? '?' + s : '';
}

const enc = encodeURIComponent;

/** projectHome(pid): the home a project lives in ('' this page's backend, 'global'). */
export const projectHome = (pid) => homeOf(pid);

/** projectApi(app, pid): the routes of one project, at its home. */
export function projectApi(app, pid) {
  const home = projectHome(pid);
  const p = `/projects/${pid}`;
  const call = (path, method, body) => projCall(home, path, method, body);
  return {
    home,
    get: () => call(p),
    patch: (body) => call(p, 'PATCH', body),
    remove: (sandbox) => call(`${p}${qs({ sandbox })}`, 'DELETE'),
    members: () => call(`${p}/members`),
    addMember: (user, role) => call(`${p}/members`, 'POST', { user, role }),
    removeMember: (user) => call(`${p}/members/${enc(user)}`, 'DELETE'),
    addRepo: (body) => call(`${p}/repos`, 'POST', body),
    patchRepo: (slug, body) => call(`${p}/repos/${enc(slug)}`, 'PATCH', body),
    removeRepo: (slug, force = false) => call(`${p}/repos/${enc(slug)}${qs({ force })}`, 'DELETE'),
    status: () => call(`${p}/status`),
    warm: () => call(`${p}/warm`, 'POST'),
    issues: (q = {}) => call(`${p}/issues${qs(q)}`),
    tasks: (f = {}) => call(`${p}/tasks${qs(f)}`),
    createTask: (spec) => call(`${p}/tasks`, 'POST', spec),
    batch: (body) => call(`${p}/tasks/batch`, 'POST', body),
    task: (n) => call(`${p}/tasks/${n}`),
    cancel: (n, reason = '') => call(`${p}/tasks/${n}/cancel`, 'POST', reason ? { reason } : {}),
    events: (since = 0, limit = 0) => call(`${p}/events${qs({ since, limit })}`),
    // a membership's (team projects, U2): the definition's changes waiting for the member
    pending: () => call(`/memberships/${pid}/pending`),
    accept: (hash) => call(`/memberships/${pid}/accept`, 'POST', { hash }),
  };
}

/** taskApi(runId): a task conversation's routes, at its home. */
export function taskApi(runId) {
  const home = homeOf(runId);
  const p = `/runs/${runId}/task`;
  const call = (path, method, body) => projCall(home, path, method, body);
  return {
    home,
    get: () => call(p),
    refresh: () => call(`${p}/refresh`, 'POST'),
    retry: () => call(`${p}/retry`, 'POST'),
    close: (opts = {}) => call(`${p}/close`, 'POST', opts),
    cleanup: (force = false) => call(`${p}/cleanup`, 'POST', force ? { force: true } : {}),
    pr: (opts = {}) => call(`${p}/pr`, 'POST', opts),
    // prProbe: does this backend have the "Open PR" route? A GET of a
    // POST-only route answers 405 where it is mounted and 404 where it isn't
    // — nothing is opened to find out.
    prProbe: () => call(`${p}/pr`).then(() => true, (e) => !!e.status && e.status !== 404),
  };
}

/** scmApi(home): the scm providers and the person's sign-in, as a home sees them. */
export function scmApi(home = '') {
  const call = (path, method, body) => projCall(home, path, method, body);
  return {
    providers: () => call('/projects/scm'),
    repos: (scm, q = '', cursor = '') => call(`/projects/scm/repos${qs({ scm, q, cursor })}`),
    signin: (scm) => call(`/projects/scm/signin${qs({ scm })}`),
    startSignin: (scm) => call('/projects/scm/signin', 'POST', { scm }),
    pollSignin: (scm, pollId) => call(`/projects/scm/signin/${enc(pollId)}${qs({ scm })}`),
    forget: (scm) => call(`/projects/scm/signin${qs({ scm })}`, 'DELETE'),
  };
}

/** listProjects(home, q): GET /projects at one home ({items, next}). */
export const listProjects = (home, q = {}) => projCall(home, `/projects${qs(q)}`);

/** createProject(home, body): POST /projects ({project, jobs}). */
export const createProject = (home, body) => projCall(home, '/projects', 'POST', body);
