// model/harness-child.js — a coding agent the agent started (subagent_spawn
// with `harness`, D-harness §4.4) as its card in the parent's chat, and the
// coding agents below a conversation on its list row (D-harness §4.3.8, §8
// U6), in words both views draw.
//
// The card is the parent's `agent` block (model/fold.js: the spawn call, its
// link and the child's summary). What it says comes from what the tile
// already holds, so a card costs no request: the link's child (§4.3.7), the
// run and `harness` events that came since (Session.runs, §4.3.3) and the
// child's view while one is held. Its last blocks are the one read — the
// child's newest page (loadTail), for a card that is open and on screen
// only; the stream keeps that page current like any view held.
//
// Pure (no DOM, no lit): node-tested in hack/agent-template-harness-child.test.mjs.
import { isHarness, nameOf, monogram, pendingOf, pendingWords, countsWords, planOf } from './harness.js';
import { parseArgs } from './tool-heads.js';
import { ICON } from './sandboxes.js';

export const TAIL = 3;      // the child's blocks a card shows
export const TAIL_READ = 8; // the messages read for them (GET /runs/{id}/view?limit=)

const BUSY = new Set(['running', 'awaiting', 'sleeping', 'queued', 'blocked']);
const argMemo = new WeakMap();
const argsOf = (b) => {
  let a = argMemo.get(b);
  if (!a) argMemo.set(b, (a = parseArgs(b.args)));
  return a;
};
// the harness a spawn call asked for (before its link says more)
const askedFor = (b) => { const h = argsOf(b).harness; return typeof h === 'string' ? h : ''; };

// isHarnessChild: an agent block whose child a coding harness drives — its
// summary says so (fold.js `harness`), or the call asked for one and no
// summary says otherwise yet.
export function isHarnessChild(b) {
  if (!b || b.k !== 'agent') return false;
  if (b.harness) return true;
  if (b.child && 'engine' in b.child) return b.child.engine === 'harness';
  return !!askedFor(b);
}

// childRun: the child's newest summary — the block's (its link's child, or
// its held view's run) with what the stream said since on top (held: the
// session's run summary, which run and harness events keep current).
export function childRun(b, held = null) {
  const run = { ...(b.child || {}), ...(held || {}) };
  if (!run.id) run.id = b.childId || 0;
  if (!run.harness) run.harness = b.harness || { provider: askedFor(b), state: 'starting' };
  run.engine = 'harness';
  return run;
}

// The card's states: a word and a tone (run · warn · bad · ok · '').
export const CARD = {
  starting: { word: 'starting', tone: 'run' },
  working: { word: 'working', tone: 'run' },
  approval: { word: 'needs approval', tone: 'warn' },
  question: { word: 'asks you', tone: 'warn' },
  login: { word: 'needs sign-in', tone: 'warn' },
  lost: { word: 'cut off', tone: 'warn' },
  failed: { word: 'failed', tone: 'bad' },
  canceled: { word: 'canceled', tone: '' },
  done: { word: 'done', tone: 'ok' },
  idle: { word: 'idle', tone: '' },
};

function stateOf(b, run, park) {
  const h = run.harness || {};
  const link = b.link || {};
  if (link.state === 'canceled' || run.status === 'canceled') return 'canceled';
  if (link.state === 'error' || run.status === 'error') return 'failed';
  if (park && CARD[park.kind]) return park.kind;
  if (h.state === 'failed') return 'failed';
  if (h.state === 'lost') return 'lost';
  if (h.state === 'login') return 'login';
  if (BUSY.has(run.status)) return h.state === 'starting' ? 'starting' : 'working';
  if ((link.state && link.state !== 'running') || b.state === 'done') return 'done';
  if (!run.status && !b.link) return 'starting'; // the call is still being made
  return 'idle';
}

const firstLine = (s, n = 140) => {
  const t = String(s || '').split('\n').map((x) => x.trim()).find(Boolean) || '';
  return t.length > n ? t.slice(0, n - 1) + '…' : t;
};
// A delivered child result starts with "--- #id title (outcome) ---".
const stripHead = (s) => String(s || '').replace(/^--- #\d+ .*? ---\n/, '');

function statusLine(key, run, park, name, answer) {
  const h = run.harness || {};
  const a = h.activity || {};
  switch (key) {
    case 'canceled': return 'canceled';
    case 'failed': return firstLine(h.error || run.error || (run.result && !String(run.result).startsWith('(') ? run.result : '')) || `${name} failed`;
    case 'approval': case 'question': case 'login': return pendingWords(park, name);
    case 'lost': return `${name} stopped (${h.error || 'cut off'}) — its next message resumes it`;
    case 'starting': return `Starting ${name}…`;
    case 'working':
      if (a.kind === 'tool' && a.title) return `Running: ${a.title}`;
      if (a.kind === 'thinking') return 'Thinking…';
      if (a.kind === 'writing') return 'Writing…';
      return 'Working…';
    case 'done': return firstLine(answer) || 'done';
  }
  return `${name} is idle`;
}

const money = (c) => (c && c.amount != null ? `${c.currency === 'USD' || !c.currency ? '$' : c.currency + ' '}${Number(c.amount).toFixed(2)}` : '');
const span = (secs) => {
  const s = Math.max(0, Math.round(secs));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  return `${Math.floor(s / 3600)}h${String(Math.floor((s % 3600) / 60)).padStart(2, '0')}m`;
};

/**
 * childCard: what a harness child's card says.
 *   b    the parent's agent block (fold.js)
 *   run  childRun(b, …)
 *   now  ms since the epoch (the elapsed time)
 * → {id, parent, provider, mono, name, title, task, where, state: {key, word,
 *    tone}, status, counts, cost, elapsed, meta (the three joined), plan
 *    (planOf), park (pendingOf: kind approval | question | login, park,
 *    title, harness — the full card data when the summary carries
 *    pendingState), answer, live, can: {stop, cancel, message}}
 */
export function childCard(b, run, now = Date.now()) {
  const h = run.harness || {};
  const provider = h.provider || askedFor(b);
  const name = nameOf({ ...h, provider });
  const link = b.link || {};
  const task = b.task || argsOf(b).task || '';
  const park = pendingOf(run);
  const settled = link.state && link.state !== 'running';
  const answer = settled && link.result && !String(link.result).startsWith('(') ? stripHead(link.result) : '';
  const key = stateOf(b, run, park);
  const sb = h.sandbox || {};
  const sname = sb.name || (sb.ref ? String(sb.ref).split('|').pop() : '');
  const counts = countsWords(h.counts);
  const cost = money(h.usage && h.usage.cost);
  const from = Number(link.created) || 0;
  const to = Number(link.settled) || now / 1000;
  const elapsed = from && to > from ? span(to - from) : '';
  const live = ['starting', 'working', 'approval', 'question', 'login', 'lost'].includes(key);
  return {
    id: run.id || b.childId || 0, parent: link.parentId || run.parentId || 0,
    provider, mono: monogram(provider), name,
    title: link.label || firstLine(task, 100) || run.title || b.headline || name,
    task,
    where: sname ? `${ICON} ${sname}${sb.cwd ? ':' + sb.cwd : ''}` : '',
    state: { key, ...CARD[key] },
    status: statusLine(key, run, park, name, answer),
    counts, cost, elapsed, meta: [counts, cost, elapsed].filter(Boolean).join(' · '),
    plan: planOf(h), park, answer, live,
    // Stop: its turn, or the ask it parked on (a sign-in waits for no turn)
    can: { stop: key === 'working' || key === 'starting' || key === 'approval' || key === 'question', cancel: live, message: key !== 'canceled' },
  };
}

// tailOf: the last blocks of the child a card shows (null: not read yet).
export function tailOf(b, n = TAIL) {
  if (!b.blocks) return null;
  return b.blocks.length > n ? b.blocks.slice(-n) : b.blocks;
}

// loadTail reads the child's newest page into the session (Session.fetchView
// with a small limit) — once, for a card that is open and on screen; the
// stream keeps it current from then on. A view already held (the child was
// opened) is used as it is. True when a read started.
export function loadTail(session, id, n = TAIL_READ) {
  if (!id || session.views.has(id) || session.loading.has(id)) return false;
  session.loading.add(id);
  session.fetchView(id, { paged: true, limit: n }).catch(() => {}).finally(() => { session.loading.delete(id); session.changed(); });
  return true;
}

// stopWords / cancelWords: what Stop and Cancel say (Cancel asks first).
export const stopWords = (c) => `Stop ${c.name}'s turn — the task stays open; message it to go on`;
export const cancelWords = (c) => `Cancel ${c.name}'s task (#${c.id})? It stops for good, and the agent that started it is told it was canceled.`;
// messageWords: the Message box's placeholder and what it says once sent.
export const messageWords = (c) => ({
  placeholder: `Message ${c.name} directly — the agent is told`,
  hint: `Enter sends it (${c.state.key === 'working' ? 'it steers or waits for the running turn' : 'its next prompt'}); ⌘/Ctrl+Enter interrupts its turn first`,
  sent: (interrupt) => `Sent to ${c.name}${interrupt ? ', its turn interrupted' : ''} — ${c.parent ? `#${c.parent}'s` : 'the'} agent is told.`,
});

// kidsWords: a conversation row's coding agents at work below it (§4.3.8
// `kids`: {harness, waiting}) as a chip — {text: "⧉ 2", title} — or null.
// The row's `?` (rules.rowGlyph, row.waiting) says that something waits.
export function kidsWords(r) {
  const k = r && r.kids;
  if (!k || !(k.harness > 0)) return null;
  const n = k.harness;
  const w = k.waiting > 0 ? ` · ${k.waiting === 1 ? 'one run' : k.waiting + ' runs'} below ${k.waiting === 1 ? 'waits' : 'wait'} for you` : '';
  return { text: `⧉ ${n}`, label: `${n} coding agent${n === 1 ? '' : 's'}`, title: `${n} coding agent${n === 1 ? '' : 's'} at work in this conversation${w}` };
}

// isChildRun: a run a harness drives that an agent started (the child's own chat).
export const isChildRun = (r) => isHarness(r) && !!r.parentId;
