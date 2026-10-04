// model/ci.js — CI in the conversation (API.md §CI in the conversation)
// for both views, as app.ci: what the platform's CI says about what a
// conversation pushed — a project task's branch and pull request, the
// branches its coding sessions pushed, a branch or pull request a person
// watches by hand — down to job steps, logs and annotations. web
// (ci-dock.js, ci-cards.js) and native (native/ci.js) draw exactly this.
//
// Where it comes from: GET /runs/{root}/ci (one request in flight per
// conversation; fresh=1 asks the backend to read the platform again where
// that would tell more), the run view's `ci` ({summary, canWatch}: the chip
// before anything is read), and the `ci` stream event (the summary, each
// watch's state and outcome — coalesced per conversation, so the next one
// or the next read repeats whatever one dropped). While the dock shows a
// conversation with anything pending it is read again every 15 s (live).
//
// Everything a build printed — names, titles, summaries, log text,
// annotation messages — is untrusted text: the views draw it as plain text,
// never markdown or HTML, and only http(s) links (the backend keeps no
// other kind). No lit, no DOM: node-tested in
// hack/agent-template-ci.test.mjs.
import { projCall, qs } from './project-api.js';
import { homeOf } from './homes.js';

export const LIVE_MS = 15000; // the dock's re-read while anything is pending
export const LOG_TAIL = 65536;
const DISMISSED = '/api/xbin/prefs/ci-dismissed'; // the person's prefs: the outcome cards closed (<watch>:<outcome>)
const DISMISSED_MAX = 300;

const FAILED = new Set(['failure', 'timed_out', 'action_required', 'startup_failure', 'error']);
const WAITING = new Set(['queued', 'waiting', 'pending', 'requested']);

/** toneOf(status, conclusion): 'ok' | 'bad' | 'run' | 'warn' | 'idle' — a
 * run's, job's, step's or check's; also a watch's or summary's state alone. */
export function toneOf(status, conclusion = '') {
  const s = String(status || ''), c = String(conclusion || '');
  if (s === 'success') return 'ok';
  if (s === 'failure' || FAILED.has(s)) return 'bad';
  if (s === 'in_progress' || (s === 'pending' && !c)) return 'run';
  if (s === 'completed') {
    if (c === 'success') return 'ok';
    if (FAILED.has(c)) return 'bad';
    if (c === 'cancelled' || c === 'stale') return 'warn';
    return 'idle';
  }
  return 'idle';
}

// a step's or job's glyph by tone (✓ ✗ ● ○)
export const GLYPH = { ok: '✓', bad: '✗', run: '●', warn: '⊘', idle: '○' };

/** stripAnsi(s): terminal escapes (colours, cursor moves, titles) and
 * control characters gone; a carriage return ends a line. */
export function stripAnsi(s) {
  return String(s || '')
    .replace(/\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[@-_]|\x9b[0-?]*[ -/]*[@-~]/g, '')
    .replace(/\r\n?/g, '\n')
    .replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f\x80-\x9f]/g, '');
}

/** elapsed(ms): "42s", "2:14", "1:02:03" ('' for nothing or less than 0). */
export function elapsed(ms) {
  const n = Math.floor(Number(ms) / 1000);
  if (!Number.isFinite(n) || n < 0) return '';
  if (n < 60) return `${n}s`;
  const h = Math.floor(n / 3600), m = Math.floor((n % 3600) / 60), s = n % 60;
  const p = (x) => String(x).padStart(2, '0');
  return h ? `${h}:${p(m)}:${p(s)}` : `${m}:${p(s)}`;
}

const span = (from, to, now) => (from > 0 ? (to > 0 ? to : now) - from : NaN);

/** jobProgress(job): {done, total, pct, current} — steps completed of all,
 * and the step under way (else the first not done). */
export function jobProgress(job) {
  const steps = (job && job.steps) || [];
  const done = steps.filter((s) => s.status === 'completed').length;
  const total = steps.length;
  const cur = steps.find((s) => s.status === 'in_progress') || steps.find((s) => s.status !== 'completed');
  const pct = total ? Math.round((done / total) * 100) : job && job.status === 'completed' ? 100 : 0;
  return { done, total, pct, current: job && job.status !== 'completed' && cur ? cur.name : '' };
}

/** chipWords(summary, now, failed?): the top bar's chip — "CI ● 3/5 jobs ·
 * 2:14", "CI ✓", "CI ✗ test (ubuntu)", "CI —" — {text, tone, title}; null
 * with no summary. failed: the first failing job's name, when known. */
export function chipWords(summary, now = Date.now(), failed = '') {
  if (!summary) return null;
  const j = summary.jobs || {};
  switch (summary.state) {
    case 'pending': {
      const t = summary.startedAt ? elapsed(now - summary.startedAt) : '';
      const text = `CI ● ${j.done || 0}/${j.total || 0} jobs${t ? ' · ' + t : ''}`;
      return { text, tone: 'run', title: summary.current ? `running: ${summary.current}` : 'CI is running — open it' };
    }
    case 'success':
      return { text: 'CI ✓', tone: 'ok', title: `CI passed${j.total ? ` (${j.total} job${j.total === 1 ? '' : 's'})` : ''} — open it` };
    case 'failure':
      return { text: failed ? `CI ✗ ${failed}` : 'CI ✗', tone: 'bad', title: `CI failed${j.failed ? `: ${j.failed} of ${j.total}` : ''} — open it` };
  }
  return { text: 'CI —', tone: 'idle', title: 'nothing reported yet — open CI' };
}

// the snapshot's job and step that failed first: {job, step, name}
function firstFailure(checks) {
  for (const r of (checks && checks.workflowRuns) || []) {
    for (const j of r.jobs || []) {
      if (j.status === 'completed' && FAILED.has(j.conclusion)) {
        const st = (j.steps || []).find((s) => FAILED.has(s.conclusion));
        return { job: j.id, run: r.id, name: st ? `${j.name} › ${st.name}` : j.name, short: j.name };
      }
    }
  }
  for (const k of (checks && checks.checks) || []) {
    if (!k.job && FAILED.has(k.conclusion)) return { job: '', check: k.id, name: k.name, short: k.name };
  }
  return null;
}

/** jobRow(job, checks, now): a job as the dock and the ci-job screen draw it. */
export function jobRow(job, checks = [], now = Date.now()) {
  const ck = checks.find((c) => c.id === job.check || c.job === job.id);
  return {
    id: job.id, name: job.name, status: job.status, conclusion: job.conclusion || '', tone: toneOf(job.status, job.conclusion),
    url: job.url || '', check: (ck && ck.id) || job.check || '', annotations: (ck && ck.annotations) || 0, runner: job.runner || '',
    elapsedMs: span(job.startedAt, job.completedAt, now), progress: jobProgress(job),
    steps: (job.steps || []).map((s) => ({ n: s.n, name: s.name, status: s.status, conclusion: s.conclusion || '', tone: toneOf(s.status, s.conclusion),
      elapsedMs: span(s.startedAt, s.completedAt, now) })),
  };
}

/** watchRows(view, now): the dock's rows — per watch its runs (each with
 * its job rows), its other checks and its statuses. */
export function watchRows(view, now = Date.now()) {
  return ((view && view.watches) || []).map((w) => {
    const c = w.checks || {};
    const checks = c.checks || [];
    const jobIds = new Set();
    const runs = (c.workflowRuns || []).map((r) => {
      for (const j of r.jobs || []) jobIds.add(j.id);
      return { id: r.id, name: r.name, event: r.event || '', attempt: r.attempt || 1, status: r.status, conclusion: r.conclusion || '',
        state: r.status === 'completed' ? r.conclusion || 'completed' : r.status, tone: toneOf(r.status, r.conclusion), url: r.url || '',
        failed: r.status === 'completed' && FAILED.has(r.conclusion), elapsedMs: span(r.startedAt, r.status === 'completed' ? r.updatedAt : 0, now),
        jobs: (r.jobs || []).map((j) => jobRow(j, checks, now)) };
    });
    return {
      watch: w.id, source: w.source, run: w.run, title: `${w.repo} · ${w.ref}`, repo: w.repo, ref: w.ref, pr: w.pr || 0, sha: w.sha || '',
      state: w.state, tone: toneOf(w.state), urls: w.urls || {}, since: w.since || 0, error: w.error || '', refusal: w.refusal || '',
      scm: w.scm, outcome: w.outcome || '', runs,
      checks: checks.filter((k) => !(k.job && jobIds.has(k.job))).map((k) => ({ id: k.id, name: k.name, app: k.app || '', status: k.status,
        conclusion: k.conclusion || '', tone: toneOf(k.status, k.conclusion), title: k.title || '', summary: k.summary || '',
        annotations: k.annotations || 0, url: k.detailsUrl || k.url || '' })),
      statuses: (c.statuses || []).map((s) => ({ context: s.context, state: s.state, tone: toneOf(s.state === 'error' ? 'failure' : s.state),
        description: s.description || '', url: s.url || '' })),
    };
  });
}

/** cardOf(watch): the outcome card a watch's outcome makes — {key, tone,
 * text, watch, job} — or null (no final outcome). */
export function cardOf(w) {
  const out = String((w && w.outcome) || '');
  const i = out.lastIndexOf(':');
  if (i < 0) return null;
  const sha = out.slice(0, i), state = out.slice(i + 1);
  if (state !== 'success' && state !== 'failure') return null;
  const where = w.ref || w.repo || 'its branch';
  if (state === 'success') return { key: `${w.id}:${out}`, tone: 'ok', text: `CI passed on ${where}`, watch: w.id, job: '' };
  const f = (w.checks && w.checks.sha === sha) ? firstFailure(w.checks) : null;
  return { key: `${w.id}:${out}`, tone: 'bad', text: `CI failed on ${where}${f ? ' — ' + f.name : ''}`, watch: w.id, job: (f && f.job) || '' };
}

// the person's prefs (xbind's per-person store: a tile frame keeps no
// storage of its own), read once
const xbinPrefs = {
  async load() {
    const r = await globalThis.xbin.fetch(DISMISSED);
    return r.ok ? r.json() : [];
  },
  save: (keys) => globalThis.xbin.fetch(DISMISSED, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(keys) }),
};

/**
 * createCI(app, opts): the CI store.
 *   take(ev)                  stream events: 'ci' (and 'run' of a held root's deletion)
 *   load(root, {fresh})       GET /runs/{root}/ci — one request in flight per root
 *   view(root) chip(root) rows(root) child(root, runId) cards(root) dismiss(key)
 *   log(root, watch, job, {tail, since, until}) annotations(root, watch, check)
 *   watch(root, body) unwatch(root, wid) rerun(root, body)
 *   live(root, on)            the dock shows root: fresh=1 every 15 s while anything is pending
 * opts: now(), setInterval, clearInterval, prefs ({load(), save(keys)}: the
 * dismissed cards, default the person's xbind prefs), debounce (ms)
 */
export function createCI(app, opts = {}) {
  const now = opts.now || (() => Date.now());
  const every = opts.setInterval || ((f, ms) => setInterval(f, ms));
  const stop = opts.clearInterval || ((h) => clearInterval(h));
  const prefs = opts.prefs || xbinPrefs;
  const wait = opts.debounce ?? 300;
  const views = new Map();   // root → CIView
  const readAt = new Map();  // root → when that view was read
  const heard = new Map();   // root → the last ci event's data
  const busy = new Map();    // root → {p, again}
  const errs = new Map();    // root → the last read's error
  const later = new Map();   // root → a re-read timer (events)
  const dismissed = new Set();
  let prefsRead = null;
  let seq = 0; // orders reads and events (a clock may stand still)
  let liveRoot = null, liveT = null;
  const changed = () => app.session && app.session.changed && app.session.changed();
  // the dismissed cards, read once (until then none is hidden: a card shows a moment longer)
  const readPrefs = () => prefsRead || (prefsRead = Promise.resolve().then(() => prefs.load()).then((a) => {
    for (const k of Array.isArray(a) ? a.slice(-DISMISSED_MAX) : []) dismissed.add(String(k));
    changed();
  }, () => {}));
  const call = (root, path, method, body) => projCall(homeOf(root), path, method, body);

  const runView = (root) => {
    const v = app.session && app.session.views && app.session.views.get(root);
    return (v && v.ci) || null;
  };
  const pending = (root) => {
    const v = views.get(root);
    if (v) return (v.watches || []).some((w) => w.state === 'pending' || (w.state === 'none' && !w.error));
    const s = (heard.get(root) || {}).summary || (runView(root) || {}).summary;
    return !!(s && (s.state === 'pending' || s.state === 'none'));
  };

  const ci = {
    async load(root, { fresh = false } = {}) {
      if (root == null) return null;
      const b = busy.get(root);
      if (b) { if (fresh) b.fresh = true; return b.p; }
      const st = { fresh: false, p: null };
      busy.set(root, st);
      st.p = (async () => {
        try {
          const v = await call(root, `/runs/${root}/ci${fresh ? '?fresh=1' : ''}`);
          views.set(root, v);
          readAt.set(root, ++seq);
          errs.delete(root);
          return v;
        } catch (e) {
          errs.set(root, (e && e.message) || String(e));
          throw e;
        } finally {
          busy.delete(root);
          changed();
          if (st.fresh) ci.load(root, { fresh: true }).catch(() => {});
        }
      })();
      return st.p;
    },
    view: (root) => views.get(root) || null,
    error: (root) => errs.get(root) || '',
    loading: (root) => busy.has(root),

    // the summary: the read view's, else the last event's, else the run view's
    summary(root) {
      const v = views.get(root), h = heard.get(root);
      if (h && (!v || h.at > (readAt.get(root) || 0))) return h.summary || null;
      if (v) return (v.watches || []).length ? v.summary : null;
      return (runView(root) || {}).summary || null;
    },
    canWatch(root) {
      const v = views.get(root);
      return !!(v ? v.canWatch : (runView(root) || {}).canWatch);
    },

    chip(root) {
      if (root == null) return null;
      const s = ci.summary(root);
      if (!s) return ci.canWatch(root) ? chipWords({ state: 'none', jobs: {} }, now()) : null;
      const f = s.state === 'failure' ? (views.get(root)?.watches || []).map((w) => firstFailure(w.checks)).find(Boolean) : null;
      return chipWords(s, now(), f ? f.short : '');
    },
    rows: (root) => watchRows(views.get(root), now()),

    // child: CI of what coding agent runId pushed — {tone, title, text}, or null
    child(root, runId) {
      const ws = ((views.get(root) || {}).watches || []).filter((w) => w.run === runId && w.state !== 'gone');
      const ev = ((heard.get(root) || {}).watches || []).filter((w) => w.run === runId && w.state !== 'gone');
      const list = ws.length ? ws : ev;
      if (!list.length) return null;
      const states = list.map((w) => w.state);
      const st = states.includes('failure') ? 'failure' : states.includes('pending') ? 'pending' : states.includes('success') ? 'success' : 'none';
      const tone = toneOf(st);
      const words = { failure: 'CI failed', pending: 'CI running', success: 'CI passed', none: 'CI: nothing reported yet' }[st];
      const refs = ws.map((w) => w.ref).filter(Boolean);
      return { tone, text: `CI ${GLYPH[tone] === '○' ? '—' : GLYPH[tone]}`, title: `${words}${refs.length ? ' on ' + refs.join(', ') : ''}` };
    },

    // cards: each watch's outcome not dismissed (from the read view, or the latest event)
    cards(root) {
      readPrefs();
      const out = [];
      const v = views.get(root);
      const seen = new Set();
      for (const w of (v && v.watches) || []) {
        const c = cardOf(w);
        seen.add(w.id);
        if (c && !dismissed.has(c.key)) out.push(c);
      }
      for (const w of (heard.get(root) || {}).watches || []) {
        if (seen.has(w.id)) continue;
        const c = cardOf({ ...((v && (v.watches || []).find((x) => x.id === w.id)) || {}), ...w });
        if (c && !dismissed.has(c.key)) out.push(c);
      }
      return out;
    },
    dismiss(key) {
      dismissed.delete(key);
      dismissed.add(key);
      for (const k of [...dismissed].slice(0, Math.max(0, dismissed.size - DISMISSED_MAX))) dismissed.delete(k);
      Promise.resolve().then(() => prefs.save([...dismissed])).catch(() => {}); // not kept: closed for this page
      changed();
    },
    ready: () => readPrefs(),

    async log(root, watch, job, { tail = LOG_TAIL, since = 0, until = 0 } = {}) {
      try {
        const r = await call(root, `/runs/${root}/ci/jobs/${encodeURIComponent(job)}/log${qs({ watch, tail, since: since || undefined, until: until || undefined })}`);
        return { ...r, text: stripAnsi(r.text), inProgress: false };
      } catch (e) {
        if (e && e.status === 409 && e.refusal === 'in-progress') {
          return { text: '', bytes: 0, from: 0, complete: false, truncated: false, url: (e.data && e.data.url) || '', inProgress: true };
        }
        throw e;
      }
    },
    async annotations(root, watch, check) {
      const out = [];
      let cursor = '';
      for (let i = 0; i < 4; i++) {
        const r = await call(root, `/runs/${root}/ci/checks/${encodeURIComponent(check)}/annotations${qs({ watch, cursor })}`);
        out.push(...((r && r.items) || []));
        cursor = (r && r.next) || '';
        if (!cursor) break;
      }
      return out;
    },
    async watch(root, body) {
      const w = await call(root, `/runs/${root}/ci/watch`, 'POST', body);
      await ci.load(root).catch(() => {});
      return w;
    },
    async unwatch(root, wid) {
      await call(root, `/runs/${root}/ci/watch/${wid}`, 'DELETE');
      await ci.load(root).catch(() => {});
    },
    async rerun(root, body) {
      const r = await call(root, `/runs/${root}/ci/rerun`, 'POST', body);
      await ci.load(root, { fresh: true }).catch(() => {});
      return r;
    },

    // live: the dock shows root — read it now (fresh), then every LIVE_MS
    // while anything is pending; off: stop.
    live(root, on) {
      if (!on) {
        if (liveRoot === root || root == null) { if (liveT != null) stop(liveT); liveT = null; liveRoot = null; }
        return;
      }
      if (liveRoot === root && liveT != null) return;
      if (liveT != null) stop(liveT);
      liveRoot = root;
      ci.load(root, { fresh: true }).catch(() => {});
      liveT = every(() => { if (pending(root)) ci.load(root, { fresh: true }).catch(() => {}); }, LIVE_MS);
    },
    isLive: (root) => liveRoot === root && liveT != null,

    take(ev) {
      if (!ev) return;
      if (ev.type === 'run' && ev.data && ev.data.deleted) {
        views.delete(ev.run); heard.delete(ev.run); readAt.delete(ev.run);
        return;
      }
      if (ev.type !== 'ci') return;
      const d = ev.data || {};
      const root = d.root || ev.root;
      if (root == null) return;
      heard.set(root, { summary: d.summary || null, watches: d.watches || [], at: ++seq });
      const v = views.get(root);
      if (v) {
        for (const w of v.watches || []) {
          const x = (d.watches || []).find((y) => y.id === w.id);
          if (x) { w.state = x.state; w.outcome = x.outcome; }
        }
      }
      // what is held or shown follows: read again (once per burst)
      if ((v || liveRoot === root) && !later.has(root)) {
        later.set(root, setTimeout(() => { later.delete(root); ci.load(root).catch(() => {}); }, wait));
      }
      changed();
    },
  };
  return ci;
}
