// projects-stub.mjs — Projects' routes (API.md §Projects) for the browser
// tests, beside backend.mjs's STUB (which stays as it is): PROJ_STUB(seed)
// is a second init script that adds them with window.__route, so it runs
// after STUB — ctx.addInitScript(STUB, seed); ctx.addInitScript(PROJ_STUB, seed).
//
// window.__proj holds the state a test reads and changes:
//   projects  ProjectView rows, each with `home` ('' or 'global': which
//             home's GET /projects lists it — the request's partition)
//   tasks     {pid: [TaskView]}; runs made for new tasks join window.__runs
//   issues    {repo: [scmIssue]}; providers, repos (GET /projects/scm/repos)
//   members   {pid: {owner, members}}; status {pid: ProjectStatus}
//   prRoute   true: the backend has POST /runs/{id}/task/pr (a GET of it
//             answers 405), false: it doesn't (404, plain text, as a mux)
//   signin    the person's provider sign-in: polls left before it is done;
//             its routes answer 409 outside a person's partition
// projSeed() is a seed: an unpartitioned agent with one project ("Web",
// two repos), its tasks in every column and a task conversation each;
// partitionSeed() the same in alice's partition (ids from 2^40), with a
// team project's definition (9) at the shared space.
export function PROJ_STUB(seed) {
  const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
  const plain404 = () => new Response('404 page not found\n', { status: 404, headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
  const P = window.__proj = {
    projects: seed.projects || [], tasks: seed.tasks || {}, issues: seed.issues || {}, providers: seed.providers || [],
    repos: seed.scmRepos || [], members: seed.members || {}, status: seed.status || {}, prRoute: !!seed.prRoute,
    signin: { state: 'none', polls: 2 }, nextPid: 50, nextRun: 500,
  };
  const home = (o) => (o && o.partition) || '';
  const proj = (id) => P.projects.find((p) => p.id === +id);
  const body = (o) => (o && o.body ? JSON.parse(o.body) : {});
  const view = (p) => {
    const ts = P.tasks[p.id] || [];
    const counts = { queued: 0, working: 0, 'needs-you': 0, pr: 0, done: 0 };
    for (const t of ts) counts[t.column] = (counts[t.column] || 0) + 1;
    const { home: _h, ...rest } = p;
    return { level: 'owner', counts, slots: { used: counts.working, max: (p.policy || {}).maxTasks || 3 }, ...rest };
  };
  const taskByRun = (run) => {
    for (const [pid, ts] of Object.entries(P.tasks)) { const t = ts.find((x) => x.run === run); if (t) return { pid: +pid, t }; }
    return null;
  };
  const r = (method, re, fn) => window.__route(method, re, fn);
  const API = '/api/apps/agent';

  // --- scm ---------------------------------------------------------------------
  r('GET', new RegExp(`${API}/projects/scm$`), () => json({ providers: P.providers }));
  r('GET', new RegExp(`${API}/projects/scm/repos\\?(.*)$`), (m) => {
    const q = new URLSearchParams(m[1]).get('q') || '';
    return json({ items: P.repos.filter((x) => `${x.owner}/${x.name}`.includes(q)), next: '' });
  });
  // the sign-in routes are a person's partition's only (409 anywhere else, as K answers)
  const own = (o) => (((o && o.partition) || (window.xbin && window.xbin.partition) || '').startsWith('user:'));
  const notHere = () => json({ error: 'sign in to GitHub from your own space' }, 409);
  r('GET', new RegExp(`${API}/projects/scm/signin\\?`), (m, o) => (own(o) ? json({ state: P.signin.state }) : notHere()));
  r('POST', new RegExp(`${API}/projects/scm/signin$`), (m, o) => {
    if (!own(o)) return notHere();
    P.signin.state = 'pending';
    return json({ state: 'pending', signin: { url: 'https://github.com/login/device', userCode: 'WDJB-MJHT', pollId: 'poll1', intervalMs: 1000, expiresAt: Date.now() + 9e5 } });
  });
  r('GET', new RegExp(`${API}/projects/scm/signin/poll1\\?`), (m, o) => {
    if (!own(o)) return notHere();
    if (--P.signin.polls > 0) return json({ state: 'pending' });
    P.signin.state = 'done';
    for (const pr of P.providers) pr.you = { ...(pr.you || {}), person: { login: 'alice-gh', id: 7 } };
    return json({ state: 'done', identity: { kind: 'person', login: 'alice-gh', id: 7 } });
  });
  r('DELETE', new RegExp(`${API}/projects/scm/signin\\?`), (m, o) => {
    if (!own(o)) return notHere();
    P.signin.state = 'none';
    for (const pr of P.providers) pr.you = { ...(pr.you || {}), person: null };
    return new Response(null, { status: 204 });
  });

  // --- projects ------------------------------------------------------------------
  r('GET', new RegExp(`${API}/projects(\\?.*)?$`), (m, o) => json({ items: P.projects.filter((p) => (p.home || '') === home(o)).map(view), next: '' }));
  r('POST', new RegExp(`${API}/projects$`), (m, o) => {
    const b = body(o);
    if (b.name === 'refuse') return json({ error: 'naming acme/secret for the bot takes a manager, or the agent\'s scm bot rule' }, 403);
    const id = P.nextPid++;
    const p = { id, uid: 'new' + id, name: b.name, slug: b.name.toLowerCase(), kind: b.kind || 'personal', owner: 'alice', visibility: (b.share || {}).visibility || 'private',
      teamRole: 'viewer', scm: b.scm, host: 'github.com', sandboxRef: (b.sandbox || {}).ref || 'apps/coding-sandbox|sb-new', sandboxMade: !(b.sandbox || {}).ref,
      policy: { ...b.policy }, state: 'active', version: 1, home: home(o),
      repos: (b.repos || []).map((x) => ({ slug: x.repo.split('/')[1], repo: x.repo, url: `https://github.com/${x.repo}.git`, defaultBranch: 'main', mode: 'bare', checkout: 'worktree', setup: x.setup || '', state: 'pending' })),
      createdBy: 'alice', createdMs: Date.now(), updatedMs: Date.now() };
    P.projects.unshift(p);
    P.tasks[id] = [];
    return json({ project: view(p), jobs: [] }, 201);
  });
  r('GET', new RegExp(`${API}/projects/(\\d+)$`), (m) => (proj(m[1]) ? json({ project: view(proj(m[1])) }) : json({ error: 'no such project' }, 404)));
  r('PATCH', new RegExp(`${API}/projects/(\\d+)$`), (m, o) => {
    const p = proj(m[1]);
    const b = body(o);
    if (b.version !== p.version) return json({ error: 'the project changed meanwhile', version: p.version }, 412);
    for (const k of ['name', 'policy', 'state', 'visibility', 'teamRole']) if (k in b) p[k] = b[k];
    p.version++;
    p.updatedMs = Date.now();
    return json({ project: view(p) });
  });
  r('DELETE', new RegExp(`${API}/projects/(\\d+)\\?sandbox=(keep|delete)$`), (m) => {
    P.projects = P.projects.filter((p) => p.id !== +m[1]);
    return json({ state: 'deleting' }, 202);
  });
  r('GET', new RegExp(`${API}/projects/(\\d+)/members$`), (m) => json(P.members[m[1]] || { owner: proj(m[1]).owner, members: [] }));
  r('POST', new RegExp(`${API}/projects/(\\d+)/members$`), (m, o) => {
    const ms = P.members[m[1]] = P.members[m[1]] || { owner: proj(m[1]).owner, members: [] };
    const b = body(o);
    ms.members = [...ms.members.filter((x) => x.user !== b.user), { user: b.user, role: b.role }];
    return json(ms);
  });
  r('DELETE', new RegExp(`${API}/projects/(\\d+)/members/([^/?]+)$`), (m) => {
    const ms = P.members[m[1]];
    if (ms) ms.members = ms.members.filter((x) => x.user !== decodeURIComponent(m[2]));
    return new Response(null, { status: 204 });
  });
  r('POST', new RegExp(`${API}/projects/(\\d+)/repos$`), (m, o) => {
    const p = proj(m[1]);
    const b = body(o);
    const repo = { slug: b.repo.split('/')[1], repo: b.repo, url: `https://github.com/${b.repo}.git`, defaultBranch: 'main', mode: 'bare', checkout: 'worktree', setup: b.setup || '', state: 'pending' };
    p.repos = [...p.repos, repo];
    p.version++;
    return json({ repo, jobs: [] }, 201);
  });
  r('PATCH', new RegExp(`${API}/projects/(\\d+)/repos/([^/?]+)$`), (m, o) => {
    const p = proj(m[1]);
    const rp = p.repos.find((x) => x.slug === m[2]);
    Object.assign(rp, body(o));
    return json({ repo: rp });
  });
  r('DELETE', new RegExp(`${API}/projects/(\\d+)/repos/([^/?]+)(\\?force=1)?$`), (m) => {
    const p = proj(m[1]);
    const used = (P.tasks[p.id] || []).some((t) => t.column !== 'done' && (t.repos || []).includes(m[2]));
    if (used && !m[3]) return json({ error: `open tasks use ${m[2]}`, refusal: 'busy' }, 409);
    p.repos = p.repos.filter((x) => x.slug !== m[2]);
    return json({ state: 'removing' }, 202);
  });
  r('GET', new RegExp(`${API}/projects/(\\d+)/status$`), (m) => json(P.status[m[1]] || { sandbox: {}, repos: [], creds: [], jobs: [], slots: { used: 0, max: 3 }, warnings: [] }));
  r('POST', new RegExp(`${API}/projects/(\\d+)/warm$`), () => json({ jobs: [] }, 202));
  r('GET', new RegExp(`${API}/projects/(\\d+)/issues\\?(.*)$`), (m) => {
    const q = new URLSearchParams(m[2]);
    const repo = q.get('repo');
    if (!proj(m[1]).repos.some((x) => x.repo === repo)) return json({ error: `${repo} is not one of the project's repos` }, 400);
    const words = q.get('q') || '';
    return json({ items: (P.issues[repo] || []).filter((i) => i.title.includes(words) && i.state === (q.get('state') || 'open')), next: '' });
  });
  r('GET', new RegExp(`${API}/projects/(\\d+)/tasks(\\?.*)?$`), (m) => {
    const q = new URLSearchParams((m[2] || '').slice(1));
    let ts = P.tasks[m[1]] || [];
    if (q.get('mine')) ts = ts.filter((t) => t.createdBy === ((seed.me || {}).user || 'admin'));
    if (q.get('q')) ts = ts.filter((t) => t.title.includes(q.get('q')) || t.branch.includes(q.get('q')));
    return json({ items: ts, next: '' });
  });
  const mkTask = (pid, spec, issue) => {
    const ts = P.tasks[pid] = P.tasks[pid] || [];
    const n = ts.reduce((x, t) => Math.max(x, t.n), 0) + 1;
    const run = P.nextRun++;
    const title = spec.title || (issue ? issue.title : spec.text.split('\n')[0].slice(0, 60));
    const t = { project: +pid, n, run, title, size: spec.size || 'small', branch: `xbin/k3x9qa/${n}-task`, repos: spec.repos || proj(pid).repos.map((x) => x.slug),
      ws: 'queued', phase: 'open', column: 'queued', state: 'queued', waitingFor: 'slot', runStatus: 'idle', engine: spec.agent ? 'harness' : '', harness: spec.agent ? spec.agent.provider : '',
      checkouts: [], prs: [], createdBy: (seed.me || {}).user || 'admin', createdMs: Date.now(), updatedMs: Date.now(), ...(issue ? { issue } : {}) };
    ts.push(t);
    window.__runs.push({ id: run, title, status: 'idle', parentId: 0, rootId: run, origin: 'project', originId: +pid });
    return t;
  };
  r('POST', new RegExp(`${API}/projects/(\\d+)/tasks$`), (m, o) => {
    const t = mkTask(m[1], body(o));
    return json({ task: t, run: { id: t.run, title: t.title, status: 'idle' } }, 201);
  });
  r('POST', new RegExp(`${API}/projects/(\\d+)/tasks/batch$`), (m, o) => {
    const b = body(o);
    const tasks = [], errors = [];
    for (const is of b.issues) {
      const found = (P.issues[is.repo] || []).find((i) => i.number === is.number);
      if (!found || found.locked) { errors.push({ issue: is, error: 'that issue is locked' }); continue; }
      tasks.push(mkTask(m[1], { size: b.size, agent: b.agent, text: found.title }, { repo: is.repo, number: is.number, title: found.title, url: found.url }));
    }
    return json({ tasks, errors }, 201);
  });
  r('POST', new RegExp(`${API}/projects/(\\d+)/tasks/(\\d+)/cancel$`), (m) => {
    const t = (P.tasks[m[1]] || []).find((x) => x.n === +m[2]);
    Object.assign(t, { state: 'cancelled', phase: 'closed', column: 'done' });
    return json(t);
  });

  // --- a task's conversation ------------------------------------------------------------
  r('GET', new RegExp(`${API}/runs/(\\d+)/task$`), (m) => {
    const f = taskByRun(+m[1]);
    return f ? json(f.t) : json({ error: 'this conversation is no task' }, 404);
  });
  r('POST', new RegExp(`${API}/runs/(\\d+)/task/(refresh|retry|close|cleanup)$`), (m) => {
    const f = taskByRun(+m[1]);
    if (m[2] === 'retry' && f) Object.assign(f.t, { ws: 'preparing', state: 'preparing', column: 'queued', error: '' });
    return json(m[2] === 'close' ? f.t : {}, m[2] === 'close' ? 200 : 202);
  });
  r('GET', new RegExp(`${API}/runs/(\\d+)/task/pr$`), () => (P.prRoute ? json({ error: 'method not allowed' }, 405) : plain404()));
  r('POST', new RegExp(`${API}/runs/(\\d+)/task/pr$`), () => (P.prRoute ? json({ job: { id: 9, kind: 'pr', state: 'queued' } }, 202) : plain404()));
}

// --- the seed -----------------------------------------------------------------------------------

const NOW = Date.now();
export const repo = (name, extra = {}) => ({ slug: name, repo: `acme/${name}`, url: `https://github.com/acme/${name}.git`, defaultBranch: 'main', mode: 'bare',
  checkout: 'worktree', setup: '', state: 'ready', protected: true, ...extra });

export const POLICY = {
  taskClass: 'coding', engine: 'auto', maxTasks: 3, maxOpenTasks: 20, maxTaskCreatesPerDay: 50, autoPR: 'off', checkout: 'worktree', setupTimeoutSec: 600,
  setupBlocking: true, ports: { base: 20000, span: 10, slots: 100 }, fetchEveryMin: 10, protection: 'warn', workflows: false,
  ci: { autoFix: true, maxPerDay: 5, delaySec: 60, logBytes: 8192 }, reviews: { forward: 'trusted', batchSec: 120 }, bigTasks: { mode: 'fork', keepFork: false },
  cleanup: { onMerge: true, onClose: true }, coordinator: { web: false }, membersAsBot: false, futureKey: { kept: true },
};

export function task(pid, n, extra = {}) {
  return { project: pid, n, run: 100 + n, title: `task ${n}`, size: 'small', branch: `xbin/k3x9qa/${n}-task-${n}`, repos: ['web'], ws: 'ready', phase: 'open',
    column: 'working', state: 'working', waitingFor: '', runStatus: 'running', engine: '', harness: '', sandboxRef: 'apps/coding-sandbox|sb-web', fork: false,
    dir: `/work/web/tasks/${n}-task-${n}`, checkouts: [{ repo: 'web', path: `/work/web/tasks/${n}-task-${n}/web`, mode: 'worktree', state: 'ready', setupExit: 0 }],
    prs: [], createdBy: 'alice', createdMs: NOW - n * 1000, updatedMs: NOW, ...extra };
}

export function projSeed(extra = {}) {
  const tasks = [
    task(7, 1, { title: 'Fix the login loop', column: 'pr', state: 'awaiting-review', phase: 'pr', runStatus: 'idle',
      prs: [{ repo: 'acme/web', number: 42, url: 'https://github.com/acme/web/pull/42', state: 'open', draft: false, headSha: 'abc', checks: 'failure' }] }),
    task(7, 2, { title: 'Add dark mode', column: 'working', state: 'working' }),
    task(7, 3, { title: 'Bump deps', column: 'needs-you', state: 'needs-you', waitingFor: 'you', runStatus: 'waiting_input' }),
    task(7, 4, { title: 'Prepare me', column: 'queued', state: 'preparing', ws: 'preparing', runStatus: 'idle', step: 'cloning acme/api',
      checkouts: [{ repo: 'web', path: '/work/web/tasks/4-prepare-me/web', mode: 'worktree', state: 'ready', setupExit: 0 },
        { repo: 'api', path: '/work/web/tasks/4-prepare-me/api', mode: 'worktree', state: 'pending' }], repos: ['web', 'api'] }),
    task(7, 5, { title: 'Merged one', column: 'done', state: 'merged', phase: 'merged', runStatus: 'idle',
      prs: [{ repo: 'acme/web', number: 40, url: 'https://github.com/acme/web/pull/40', state: 'merged', draft: false, headSha: 'def', checks: 'success' }],
      ci: { state: 'success', jobs: { total: 2, done: 2, failed: 0, running: 0, queued: 0 } } }),
    task(7, 6, { title: 'Needs a sign-in', column: 'needs-you', state: 'signin', ws: 'signin', runStatus: 'waiting_input', waitingFor: 'signin', checkouts: [] }),
    task(7, 8, { title: 'Workspace failed', column: 'needs-you', state: 'failed', ws: 'failed', runStatus: 'idle', error: 'git clone: repository not found',
      checkouts: [{ repo: 'web', path: '', mode: 'worktree', state: 'failed', error: 'clone failed' }] }),
  ];
  const web = { id: 7, uid: 'k3x9qa', name: 'Web', slug: 'web', kind: 'personal', owner: 'alice', visibility: 'private', teamRole: 'viewer', level: 'owner',
    scm: 'apps/scm-github', host: 'github.com', sandboxRef: 'apps/coding-sandbox|sb-web', sandboxMade: true, dir: '/work/web', policy: POLICY,
    state: 'active', version: 3, repos: [repo('web'), repo('api', { protected: false })], createdBy: 'alice', createdMs: NOW - 9e6, updatedMs: NOW - 1000, home: '' };
  const old = { ...web, id: 8, uid: 'zz11aa', name: 'Old site', slug: 'old-site', state: 'archived', version: 1, repos: [repo('old')], updatedMs: NOW - 9e7, sandboxMade: false };
  const view = (t) => ({
    run: { id: t.run, title: t.title, status: t.runStatus, parentId: 0, rootId: t.run, origin: 'project', originId: 7,
      task: { first: { seq: 1, text: `do ${t.title}`, from: 'alice', at: NOW / 1000 }, count: 1 },
      pendingState: t.ws === 'signin' ? { kind: 'project', project: { project: 7, n: t.n, ws: 'signin', step: 'waiting for your sign-in',
        signin: { url: 'https://github.com/login/device', userCode: 'ABCD-1234', pollId: 'poll1', intervalMs: 1000, expiresAt: NOW + 9e5 } } } : {} },
    project: { id: 7, name: 'Web', n: t.n, role: 'task' }, projectTask: t,
  });
  return {
    me: { kind: 'user', user: 'alice', level: 'terminal', manager: true, halted: false },
    runs: [{ id: 1, title: 'a chat', status: 'idle', parentId: 0, rootId: 1, activityMs: NOW },
      ...tasks.map((t) => ({ id: t.run, title: t.title, status: t.runStatus, parentId: 0, rootId: t.run, origin: 'project', originId: 7, activityMs: NOW }))],
    views: Object.fromEntries(tasks.map((t) => [t.run, view(t)])),
    projects: [web, old],
    tasks: { 7: tasks, 8: [] },
    providers: [{ scm: 'apps/scm-github', title: 'GitHub', kind: 'github', hosts: ['github.com'], caps: ['checks', 'issues'], identities: ['person', 'bot'],
      you: { identities: ['person', 'bot'], default: 'person', person: null }, app: { configured: true }, events: { healthy: true }, notes: [] }],
    scmRepos: [{ host: 'github.com', owner: 'acme', name: 'web', private: true, permission: 'write', defaultBranch: 'main' },
      { host: 'github.com', owner: 'acme', name: 'api', private: false, permission: 'admin', defaultBranch: 'main' },
      { host: 'github.com', owner: 'acme', name: 'attic', archived: true, permission: 'read', defaultBranch: 'main' }],
    issues: { 'acme/web': [
      { number: 11, title: 'Login loops on Safari', body: '<img src=x onerror="window.__pwned=1"> **bold** steps…', state: 'open', labels: ['bug'], url: 'https://github.com/acme/web/issues/11', author: { login: 'eve' } },
      { number: 12, title: 'Dark mode', body: 'please', state: 'open', labels: [], url: 'https://github.com/acme/web/issues/12', author: { login: 'bob' } },
      { number: 13, title: 'Locked one', body: '', state: 'open', labels: [], locked: true, url: 'https://github.com/acme/web/issues/13', author: { login: 'bob' } },
    ] },
    members: { 7: { owner: 'alice', members: [{ user: 'bob', role: 'participant' }] } },
    status: { 7: { sandbox: { ref: 'apps/coding-sandbox|sb-web', name: 'web', state: 'running', workdir: '/work', shared: false },
      repos: [{ slug: 'web', state: 'ready', fetchedMs: NOW - 60e3, head: '9fceb02abcdef', protected: true }, { slug: 'api', state: 'ready', fetchedMs: NOW - 60e3, head: '1234567890', protected: false }],
      creds: [{ sandbox: 'apps/coding-sandbox|sb-web', host: 'github.com', identity: 'person', login: 'alice-gh', state: 'live', expiresMs: NOW + 3.6e6 }],
      jobs: [{ id: 3, project: 7, kind: 'fetch', repo: 'web', state: 'done', attempts: 1, created: NOW - 9e4, updated: NOW - 6e4 }],
      slots: { used: 1, max: 3 }, warnings: [{ kind: 'unprotected', repo: 'acme/api', text: 'its default branch has no protection: a pushed token could merge through the API' }] } },
    prRoute: false,
    ...extra,
  };
}

// partitionSeed(): projSeed() in a person's partition — "Web" and its tasks'
// conversations at ids from 2^40 (their home: the person's own), and a team
// project's definition (9, carol's) in the shared space. The page needs
// xbin.partition = 'user:alice'.
export function partitionSeed(extra = {}) {
  const B = 2 ** 40;
  const s = projSeed();
  const tasks = s.tasks[7].map((t) => ({ ...t, project: B + 7, run: B + t.run }));
  const byRun = new Map(tasks.map((t) => [t.run - B, t]));
  const runs = s.runs.map((r) => (r.origin === 'project' ? { ...r, id: B + r.id, rootId: B + r.rootId, originId: B + 7 } : r));
  const views = Object.fromEntries(Object.entries(s.views).map(([k, v]) => {
    const ps = v.run.pendingState;
    const run = { ...v.run, id: B + v.run.id, rootId: B + v.run.rootId, originId: B + 7, pendingState: ps && ps.project ? { ...ps, project: { ...ps.project, project: B + 7 } } : ps };
    return [B + +k, { ...v, run, project: { ...v.project, id: B + 7 }, projectTask: byRun.get(+k) }];
  }));
  const web = { ...s.projects[0], id: B + 7 };
  const team = { ...s.projects[0], id: 9, name: 'Team site', kind: 'team', owner: 'carol', level: 'participant', home: 'global' };
  return { ...s, runs, views, projects: [web, team], tasks: { [B + 7]: tasks }, status: { [B + 7]: s.status[7] }, members: {}, ...extra };
}
