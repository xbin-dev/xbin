// model/project-task.js — the words of a project's tasks, for both views
// (API.md §Projects in the UI): the board's columns and a card's words, a
// task conversation's chips (branch, pull request, the setup outcome), its
// prep card (the workspace being prepared, step by step) and sign-in card,
// "Open PR", the crumb back to its project, and the task's section of the
// pinned task. Pure functions of a TaskView (GET /runs/{id}/task, the run
// view's `projectTask`) and the run view's `project` ({id, name, n, role});
// no lit, no DOM, no calls.
//
// Text from the scm provider (a PR's title, an issue's) is untrusted: the
// views draw these words as plain text, never as markdown or HTML — a
// task's title and an issue's text with their control, direction and
// zero-width characters out, clipped (model/project-feed.js plain).
import { plain } from './project-feed.js';

// The board's columns (derived by the backend, TaskView.column), in order.
export const COLUMNS = [
  { key: 'queued', title: 'Queued' },
  { key: 'working', title: 'Working' },
  { key: 'needs-you', title: 'Needs you' },
  { key: 'pr', title: 'PR' },
  { key: 'done', title: 'Done' },
];

const WS_QUEUED = new Set(['pending', 'queued', 'preparing']);
const WS_NEEDS = new Set(['signin', 'failed', 'blocked']);
const WORKING = new Set(['running', 'awaiting', 'sleeping']);
const DONE = new Set(['merged', 'closed', 'done']);

/** columnOf(task): the board column a task is in — the backend's when it
 * says, else derived the same way (a task from an older answer). */
export function columnOf(t) {
  if (!t) return 'queued';
  if (t.column && COLUMNS.some((c) => c.key === t.column)) return t.column;
  if (DONE.has(t.phase) || t.phase === 'deleted') return 'done';
  if (t.runStatus === 'waiting_input' || WS_NEEDS.has(t.ws)) return 'needs-you';
  if (WS_QUEUED.has(t.ws)) return 'queued';
  if (WORKING.has(t.runStatus)) return 'working';
  if (t.phase === 'pr') return 'pr';
  return 'working';
}

/** columns(tasks): the board — every column with its tasks, in task order (newest first). */
export function columns(tasks) {
  const by = new Map(COLUMNS.map((c) => [c.key, []]));
  for (const t of [...(tasks || [])].sort((a, b) => (b.n || 0) - (a.n || 0))) by.get(columnOf(t)).push(t);
  return COLUMNS.map((c) => ({ ...c, tasks: by.get(c.key) }));
}

// What a task's state (TaskView.state) is called, and its tone.
export const STATES = {
  queued: ['queued', 'idle'], preparing: ['preparing the workspace', 'run'], signin: ['needs a sign-in', 'warn'],
  working: ['working', 'run'], 'needs-you': ['needs you', 'warn'], ci: ['CI running', 'run'], 'ci-failed': ['CI failed', 'bad'],
  'awaiting-review': ['awaiting review', 'idle'], merged: ['merged', 'ok'], closed: ['closed', 'idle'], done: ['done', 'ok'],
  failed: ['failed', 'bad'], cancelled: ['cancelled', 'idle'], blocked: ['cleanup refused', 'warn'], deleted: ['deleted', 'idle'],
};
// What a task waits for (TaskView.waitingFor).
export const WAITING = { you: 'waiting for you', signin: 'waiting for a sign-in', review: 'waiting for a review', ci: 'waiting for CI', slot: 'waiting for a free slot' };

/** stateWords(task): {text, tone} — tone run | ok | bad | warn | idle. */
export function stateWords(t) {
  const [text, tone] = STATES[t && t.state] || [(t && t.state) || (t && t.ws) || '', 'idle'];
  const wait = t && t.waitingFor && WAITING[t.waitingFor];
  return { text: wait && t.state !== 'signin' ? `${text} · ${wait}` : text, tone };
}

// A pull request's state, in words and a tone.
const PR_TONE = { open: 'run', merged: 'ok', closed: 'idle' };
const CHECKS = { pending: ['run', 'checks running'], success: ['ok', 'checks passed'], failure: ['bad', 'checks failed'] };

/** prChip(pr, ci): a pull request's chip — "PR #42 open" (draft said),
 * its checks only while the task has no CI summary (V's CI chip is then
 * the one source): checksTone, the tone whose glyph (model/ci.js ICON) the
 * views draw after the words, and checksText its words (D184). */
export function prChip(pr, ci = null) {
  const state = pr.draft && pr.state === 'open' ? 'draft' : pr.state || 'open';
  const c = !ci && CHECKS[pr.checks];
  return {
    kind: 'pr', text: `PR #${pr.number} ${state}`, tone: c && pr.state === 'open' ? c[0] : PR_TONE[pr.state] || 'idle',
    title: `${pr.repo}#${pr.number}: ${state}${c ? ' — ' + c[1] : ''}`, url: safeUrl(pr.url), checks: c ? pr.checks : '', checksTone: c ? c[0] : '', checksText: c ? c[1] : '',
  };
}

/** prWords(chip): a pull request's chip in words alone — "PR #42 open, checks
 * failed" — where no glyph is drawn (the native view's rows and menus). */
export const prWords = (c) => (c.checksText ? `${c.text}, ${c.checksText}` : c.text);

/** safeUrl: an https link, else '' — a link from the provider (a pull request, the branch, the
 * issue, the sign-in page where a device code is typed) is drawn only if it is one, in both views
 * (the native view's own checks, model/project-feed.js httpsUrl). */
export function safeUrl(u) {
  const s = String(u || '');
  return /^https:\/\/[^\s]+$/i.test(s) ? s : '';
}

/** webUrl(repo): a repo's page on the platform from its clone URL (ProjectRepo.url). */
export function webUrl(r) {
  const u = safeUrl(r && r.url);
  return u ? u.replace(/\.git$/, '').replace(/\/+$/, '') : '';
}

/** taskOf(view): the run view's TaskView (projectTask), or null. */
export const taskOf = (v) => (v && v.projectTask) || null;

/**
 * taskChips(view, project?): the top bar's chips on a task conversation —
 * its branch (a link to it on the platform when the project's repos are
 * known), each pull request with its state (checks while task.ci is null),
 * and the setup outcome when a setup ran. [] when the run is no task.
 */
export function taskChips(v, project = null) {
  const t = taskOf(v);
  if (!t) return [];
  const out = [];
  if (t.branch) {
    const repos = (project && project.repos) || [];
    const mine = repos.filter((r) => !t.repos || !t.repos.length || t.repos.includes(r.slug));
    const links = mine.map((r) => ({ repo: r.repo, url: webUrl(r) ? `${webUrl(r)}/tree/${t.branch.split('/').map(encodeURIComponent).join('/')}` : '' }))
      .filter((l) => l.url);
    out.push({ kind: 'branch', text: t.branch, icon: 'branch', tone: 'idle', title: `the task's branch${mine.length ? ' in ' + mine.map((r) => r.repo).join(', ') : ''}`,
      url: links.length ? links[0].url : '', links });
  }
  for (const pr of t.prs || []) out.push(prChip(pr, t.ci || null));
  const s = setupOutcome(t);
  if (s) out.push({ kind: 'setup', ...s });
  return out;
}

/** setupOutcome(task): the setup scripts' outcome ({text, tone, title}), or null when none ran. */
export function setupOutcome(t) {
  const ran = ((t && t.checkouts) || []).filter((c) => c.setupExit != null);
  if (!ran.length) return null;
  const bad = ran.filter((c) => c.setupExit !== 0);
  if (!bad.length) return { text: 'setup passed', tone: 'ok', title: `setup passed in ${ran.map((c) => c.repo).join(', ')}` };
  return { text: `setup failed: ${bad.map((c) => c.repo).join(', ')}`, tone: 'bad',
    title: `setup failed (exit ${bad.map((c) => `${c.repo}: ${c.setupExit}`).join(', ')}) — the task was told; it works anyway` };
}

// The workspace's states (TaskView.ws), for the prep card.
export const WS = {
  pending: 'waiting to be prepared', queued: 'waiting for a free slot', preparing: 'preparing the workspace', signin: 'waiting for you to sign in',
  ready: 'ready', failed: 'preparing the workspace failed', cleaning: 'cleaning up', cleaned: 'cleaned up', blocked: 'cleanup refused: unpushed or uncommitted work',
};
// A checkout's states (ProjectCheckout.state), as a step: its tone (the
// views draw the tone's glyph, D184) and its words.
const STEP = {
  pending: ['idle', 'waiting'], added: ['run', 'worktree added'], setup: ['run', 'running setup'],
  ready: ['ok', 'ready'], failed: ['bad', 'failed'], removed: ['idle', 'removed'], kept: ['idle', 'kept'],
};
const PREP = new Set(['pending', 'queued', 'preparing', 'signin', 'failed', 'blocked']);

/**
 * prepCard(view, me): the prep card at the end of a task conversation while
 * its workspace isn't ready (or the workspace gate parked its run) —
 * {title, tone, steps: [{repo, tone, text, error}], step, detail,
 * error, retry, signin} — or null. The sign-in card's part (signin: the
 * device code and link) is there only for the person who must sign in: the
 * backend sends ProjectPark.signin to them alone, and the card shows it
 * only when that person is the one looking (the task's creator).
 */
export function prepCard(v, me) {
  const t = taskOf(v);
  const ps = (v && v.run && v.run.pendingState) || {};
  const park = ps.kind === 'project' ? ps.project || {} : null;
  if (!t && !park) return null;
  const ws = (park && park.ws) || (t && t.ws) || '';
  if (!PREP.has(ws) && !park) return null;
  const steps = ((t && t.checkouts) || []).map((c) => {
    const [tone, text] = STEP[c.state] || ['idle', c.state || ''];
    const exit = c.setupExit != null && c.setupExit !== 0 ? ` (setup exit ${c.setupExit})` : '';
    return { repo: c.repo, tone: exit ? 'warn' : tone, text: text + exit, error: c.error || '' };
  });
  const tone = ws === 'failed' ? 'bad' : ws === 'signin' || ws === 'blocked' ? 'warn' : 'run';
  const who = me && typeof me === 'object' ? me.user : me;
  const mine = !t || !t.createdBy || !who || t.createdBy === who;
  const si = park && park.signin && mine ? park.signin : null;
  return {
    ws, title: WS[ws] || ws, tone, steps,
    step: (park && park.step) || (t && t.step) || '',
    detail: (park && park.detail) || '',
    error: (t && t.error) || '',
    retry: ws === 'failed',
    signin: si ? { url: safeUrl(si.url), userCode: si.userCode || '', expiresAt: si.expiresAt || 0, pollId: si.pollId || '', intervalMs: si.intervalMs || 5000 } : null,
    signinElsewhere: ws === 'signin' && !si ? (mine ? 'Sign in to the provider to go on (Projects › the project › Settings).' : `waiting for ${t.createdBy} to sign in`) : '',
  };
}

/**
 * prButton(view, route): "Open PR" — {shown, disabled, title}. Shown on a
 * task you may act on that has a branch and no open pull request, once the
 * backend is known to have the route (route: true | false | null unknown).
 */
export function prButton(v, route, canAct = true) {
  const t = taskOf(v);
  if (!t || route !== true || !canAct) return { shown: false };
  if (!t.branch || ['merged', 'closed', 'done', 'deleted'].includes(t.phase)) return { shown: false };
  if ((t.prs || []).some((p) => p.state === 'open')) return { shown: false };
  const ready = t.ws === 'ready';
  return { shown: true, disabled: !ready, title: ready ? 'open a pull request for this task\'s branch' : 'its workspace isn\'t ready yet' };
}

/** crumb(view): the way back to the conversation's project — {pid, text, title} — or null. */
export function crumb(v) {
  const p = v && v.project;
  if (!p || !p.id) return null;
  const role = p.role === 'coordinator' ? 'its coordinator' : p.n ? `task #${p.n}` : 'a task';
  return { pid: p.id, text: `${p.name || 'project ' + p.id} ›`, title: `back to the project (this conversation is ${role})` };
}

/** taskSection(view): the pinned task's project section — project, task, size, repos, checkouts. */
export function taskSection(v) {
  const t = taskOf(v);
  const p = v && v.project;
  if (!t || !p) return null;
  return {
    pid: p.id, project: p.name || '', n: t.n, size: t.size === 'big' ? 'big — its own sandbox' : 'small — a worktree in the project\'s sandbox',
    repos: (t.repos || []).join(', ') || 'every repo of the project',
    issue: t.issue ? `${t.issue.repo}#${t.issue.number}${t.issue.title ? ' ' + t.issue.title : ''}` : '',
    issueUrl: safeUrl(t.issue && t.issue.url),
    checkouts: (t.checkouts || []).map((c) => ({ repo: c.repo, path: c.path || '', state: c.state || '', mode: c.mode || '' })),
    ports: t.ports ? `${t.ports.base}–${t.ports.base + t.ports.span - 1}` : '',
    dir: t.dir || '',
  };
}

/** cardWords(task): a board card's words — {n, title, state, branch, prs, issue}. */
export function cardWords(t) {
  return {
    n: `#${t.n}`, title: plain(t.title, 200) || `task ${t.n}`, state: stateWords(t), branch: t.branch || '',
    prs: (t.prs || []).map((p) => prChip(p, t.ci || null)),
    issue: t.issue ? `${t.issue.repo}#${t.issue.number}` : '',
    agent: t.engine === 'harness' ? t.harness || 'a coding agent' : '',
    size: t.size === 'big' ? 'big' : '',
  };
}

/** issueWords(i): an issue as the From issues picker draws it, in both views — its title, labels
 * and body are anyone's words (the backend only redacts them): plain, clipped, the body on one
 * line; its link only when https. */
export function issueWords(i) {
  return {
    key: `${i.repo}#${i.number}`, number: i.number, title: plain(i.title, 200),
    labels: (i.labels || []).map((l) => plain(l, 50)).filter(Boolean),
    body: plain(String(i.body || '').replace(/\s+/g, ' '), 240), url: safeUrl(i.url),
  };
}
