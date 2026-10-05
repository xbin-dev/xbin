// ci-stub.mjs — CI in the conversation (API.md §CI in the conversation) for
// the tests, beside backend.mjs's STUB (which stays as it is):
//
//   ciSnapshot(sha, opts)  GET /scm/checks as a watch keeps it: a run "ci"
//                          with lint done, "test (ubuntu)" at its 4th step
//                          (opts.failed: done, failed at it) and build
//                          queued; codecov's check failing with 2
//                          annotations; jenkins's status passing
//   ciView(root, opts)     a CIView with one watch of that snapshot
//                          (opts.run: the run it was pushed from)
//   ciSeed(root, opts)     what CI_STUB reads: {views: {root: CIView}, logs (a
//                          log {partial: true} reads as not complete: Follow),
//                          notes} — the log of job 88001 with colours, a
//                          token and 300 lines, codecov's annotations
//   ciRoutes(root, opts)   the same as native-stub.mjs's seed.routes
//   CI_STUB(seed)          a second init script for the browser tests: the
//                          routes over seed.ci with window.__route, after
//                          STUB — ctx.addInitScript(STUB, seed);
//                          ctx.addInitScript(CI_STUB, seed). window.__ci
//                          holds the state (reads, reruns, watches made)
export const SHA1 = '9fceb02d0ae598e95dc970b74767f19372d61af8';
export const SHA2 = '1f2e3d4c5b6a79881726354453627180a9b8c7d6';
export const TOKEN = 'ghs_' + 'A'.repeat(36);

export function ciSnapshot(sha = SHA1, { failed = false, started = 1789990000000 } = {}) {
  const test = failed
    ? { id: '88001', name: 'test (ubuntu)', status: 'completed', conclusion: 'failure', url: 'https://github.com/acme/web/actions/runs/7001/job/88001',
      startedAt: started + 10000, completedAt: started + 94000, runner: 'ubuntu-24.04', check: '88001',
      steps: [{ n: 1, name: 'Set up job', status: 'completed', conclusion: 'success', startedAt: started + 10000, completedAt: started + 12000 },
        { n: 4, name: 'go test ./...', status: 'completed', conclusion: 'failure', startedAt: started + 30000, completedAt: started + 94000 }] }
    : { id: '88001', name: 'test (ubuntu)', status: 'in_progress', conclusion: '', url: 'https://github.com/acme/web/actions/runs/7001/job/88001',
      startedAt: started + 10000, completedAt: 0, runner: 'ubuntu-24.04', check: '88001',
      steps: [{ n: 1, name: 'Set up job', status: 'completed', conclusion: 'success', startedAt: started + 10000, completedAt: started + 12000 },
        { n: 4, name: 'go test ./...', status: 'in_progress', conclusion: '', startedAt: started + 30000, completedAt: 0 },
        { n: 5, name: 'upload', status: 'queued', conclusion: '' }] };
  return {
    sha, ref: 'feature', state: 'failure',
    counts: { total: 4, success: 2, failure: 1, pending: 1, neutral: 0, skipped: 0, cancelled: 0 },
    workflowRuns: [{ id: '7001', name: 'ci', event: 'push', status: failed ? 'completed' : 'in_progress', conclusion: failed ? 'failure' : '',
      url: 'https://github.com/acme/web/actions/runs/7001', startedAt: started, updatedAt: started + 94000, attempt: 1, headSha: sha, headBranch: 'feature',
      jobs: [
        { id: '88000', name: 'lint', status: 'completed', conclusion: 'success', url: 'https://github.com/acme/web/actions/runs/7001/job/88000',
          startedAt: started, completedAt: started + 8000, check: '88000', steps: [{ n: 1, name: 'Set up job', status: 'completed', conclusion: 'success', startedAt: started, completedAt: started + 1000 }] },
        test,
        { id: '88002', name: 'build', status: 'queued', conclusion: '', steps: [] }] }],
    checks: [
      { id: '88000', name: 'lint', status: 'completed', conclusion: 'success', annotations: 0, job: '88000' },
      { id: '88001', name: 'test (ubuntu)', status: test.status, conclusion: test.conclusion, annotations: 0, job: '88001' },
      { id: '88100', name: 'codecov/patch', app: 'codecov', status: 'completed', conclusion: 'failure', url: 'https://github.com/acme/web/runs/88100',
        detailsUrl: 'https://app.codecov.io/gh/acme/web', title: '62% of diff hit (target 80%)', summary: '**not markdown** <b>nor html</b>', annotations: 2, job: '' }],
    statuses: [{ context: 'ci/jenkins', state: 'success', url: 'https://jenkins.example/12', description: 'Build #12 passed' }],
  };
}

export function ciView(root, { failed = false, run = root, outcome = '', canWatch = true, canRerun = true } = {}) {
  const checks = ciSnapshot(SHA1, { failed });
  return {
    root, live: true, canWatch, canRerun,
    summary: failed
      ? { state: 'failure', jobs: { total: 5, done: 4, failed: 2, running: 0, queued: 1 }, startedAt: 1789990000000, updatedAt: 1789990094000, url: 'https://app.codecov.io/gh/acme/web' }
      : { state: 'failure', jobs: { total: 5, done: 3, failed: 1, running: 1, queued: 1 }, current: 'test (ubuntu) › go test ./...', startedAt: 1789990000000, updatedAt: 1789990090000, url: 'https://app.codecov.io/gh/acme/web' },
    watches: [{ id: 4, source: 'pushed', run, scm: 'apps/scm-github', host: 'github.com', repo: 'acme/web', ref: 'feature', pr: 42, sha: SHA1,
      state: 'failure', since: 1789990000000, updatedMs: 1789990090000, fetchedMs: 1789990090000, outcome: outcome || (failed ? `${SHA1}:failure` : ''),
      urls: { pr: 'https://github.com/acme/web/pull/42', branch: 'https://github.com/acme/web/tree/feature', commit: `https://github.com/acme/web/commit/${SHA1}`,
        checks: 'https://github.com/acme/web/pull/42/checks' }, checks }],
  };
}

function bigLog() {
  const lines = ['\x1b[36m##[group]Run go test ./...\x1b[0m', `password=${TOKEN}`];
  for (let i = 1; i <= 300; i++) lines.push(`line ${i}: ok  \tgithub.com/acme/web/pkg${i}\t0.0${i % 10}s`);
  lines.push('--- FAIL: TestLogin (0.01s)', '    login_test.go:12: the loop never ends', 'FAIL');
  return lines.join('\n') + '\n';
}

export function ciSeed(root, opts = {}) {
  return {
    views: { [root]: ciView(root, opts) },
    logs: { 88001: { text: bigLog(), running: !opts.failed && !opts.logReady }, 88000: { text: 'lint ok\n', running: false } },
    notes: { 88100: [{ path: 'auth/login.go', startLine: 12, endLine: 14, level: 'failure', title: 'uncovered', message: 'these lines are not covered' },
      { path: 'auth/session.go', startLine: 3, endLine: 3, level: 'warning', title: '', message: '<img src=x onerror=alert(1)>' }] },
  };
}

// ciRoutes: native-stub.mjs's seed.routes for root's CI (static answers)
export function ciRoutes(root, opts = {}) {
  const s = ciSeed(root, opts);
  const log = s.logs['88001'];
  const text = log.text;
  return [
    ['GET', `/runs/${root}/ci(\\?.*)?$`, s.views[root]],
    ['GET', `/runs/${root}/ci/jobs/88001/log\\?`, log.running
      ? { error: 'the job is still running', refusal: 'in-progress', url: 'https://github.com/acme/web/actions/runs/7001/job/88001' }
      : { text: text.replace(/ghs_A+/, 'ghs_[redacted]'), bytes: text.length, from: 0, complete: true, truncated: false, url: 'https://github.com/acme/web/actions/runs/7001/job/88001' },
    log.running ? 409 : 200],
    ['GET', `/runs/${root}/ci/checks/88100/annotations\\?`, { items: s.notes['88100'], next: '' }],
    ['POST', `/runs/${root}/ci/rerun$`, { runId: '7001', attempt: 2 }, 202],
    ['POST', `/runs/${root}/ci/watch$`, { id: 5, repo: 'acme/web', ref: 'main', state: 'none', urls: {} }, 201],
  ];
}

// CI_STUB: the routes over seed.ci, for the browser (an init script: it
// sees only its argument).
export function CI_STUB(seed) {
  const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
  const S = window.__ci = { views: (seed.ci && seed.ci.views) || {}, logs: (seed.ci && seed.ci.logs) || {}, notes: (seed.ci && seed.ci.notes) || {},
    reads: [], reruns: [], watches: [], unwatched: [] };
  const r = (method, re, fn) => window.__route(method, re, fn);
  const API = '/api/apps/agent';
  r('GET', new RegExp(`${API}/runs/(\\d+)/ci(\\?fresh=1)?$`), (m) => {
    S.reads.push({ root: +m[1], fresh: !!m[2] });
    const v = S.views[m[1]];
    return json(v || { root: +m[1], summary: { state: 'none', jobs: { total: 0, done: 0, failed: 0, running: 0, queued: 0 } }, live: true, canRerun: false, canWatch: true, watches: [] });
  });
  r('GET', new RegExp(`${API}/runs/(\\d+)/ci/jobs/([^/?]+)/log\\?(.*)$`), (m) => {
    const l = S.logs[m[2]];
    const q = new URLSearchParams(m[3]);
    if (!l) return json({ error: 'no such job in this watch\'s CI' }, 404);
    if (l.running) return json({ error: 'the job is still running', refusal: 'in-progress', url: `https://github.com/acme/web/actions/runs/7001/job/${m[2]}` }, 409);
    const text = l.text.replace(/ghs_[A-Za-z0-9]+/g, (t) => 'ghs_' + '[redacted]'.padEnd(t.length - 4, '*'));
    const end = +q.get('until') || text.length;
    const tail = +q.get('tail') || 65536;
    let from = Math.max(+q.get('since') || 0, end - Math.min(tail, 2048)); // a small tail: "Earlier" pages back
    if (from > 0) { const nl = text.indexOf('\n', from - 1); from = nl < 0 ? end : nl + 1; }
    return json({ text: text.slice(from, end), bytes: text.length, from, complete: !l.partial, truncated: from > 0, url: `https://github.com/acme/web/actions/runs/7001/job/${m[2]}` });
  });
  r('GET', new RegExp(`${API}/runs/(\\d+)/ci/checks/([^/?]+)/annotations\\?(.*)$`), (m) => json({ items: S.notes[m[2]] || [], next: '' }));
  r('POST', new RegExp(`${API}/runs/(\\d+)/ci/rerun$`), (m, o) => {
    S.reruns.push(JSON.parse(o.body));
    return json({ runId: '7001', attempt: 2 }, 202);
  });
  r('POST', new RegExp(`${API}/runs/(\\d+)/ci/watch$`), (m, o) => {
    const b = JSON.parse(o.body);
    S.watches.push(b);
    const v = S.views[m[1]] = S.views[m[1]] || { root: +m[1], summary: { state: 'none', jobs: {} }, live: true, canRerun: false, canWatch: true, watches: [] };
    const w = { id: 100 + S.watches.length, source: 'manual', run: +m[1], scm: 'apps/scm-github', host: 'github.com', repo: b.repo, ref: b.ref || 'pr-head', pr: b.pr || 0,
      sha: '', state: 'none', since: Date.now(), updatedMs: Date.now(), fetchedMs: 0, urls: {} };
    v.watches.push(w);
    return json(w, 201);
  });
  r('DELETE', new RegExp(`${API}/runs/(\\d+)/ci/watch/(\\d+)$`), (m) => {
    S.unwatched.push(+m[2]);
    const v = S.views[m[1]];
    if (v) v.watches = v.watches.filter((w) => w.id !== +m[2]);
    return new Response(null, { status: 204 });
  });
}
