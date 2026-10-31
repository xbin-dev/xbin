// model/project-feed.js — a project's coordinator and its event feed, for
// both views (API.md §Projects in the UI): app.projects' companion for
// what the coordinator (POST /projects/{pid}/coordinator) and the project's
// events (GET /projects/{pid}/events) add to a project's page.
//
//   const f = projectFeed(app);   // one per app, made on first use
//   f.load(pid) → the feed read (every page the first time, then only
//                 what is newer); f.items(pid) newest first
//   f.coord(pid) → {run, busy, err, note, text}; f.openCoordinator(pid),
//                 f.messageCoordinator(pid, text)
//   f.preview(pid) → the coordinator card's small chat (D209): your
//                 coordinator (GET /projects/{pid}/coordinator — read only,
//                 none is made), what it does now, its last messages — and
//                 the overview's chat window (D214): its newest messages
//                 and tool steps, more than fit
//   f.stopCoordinator(pid) → its turn interrupted, as the chat's Stop: the
//                 text of what was still queued to it
//
// The feed is kept current by the `project` stream event (change `event`,
// or any change about a task: a turn ended, a PR moved) — app.projects.take
// is wrapped once, so nothing else in the model changes. An event's body is
// the backend's ({text, by?, sha?, url?, …}): scm text inside it is
// untrusted (redacted there, clipped here) and drawn as plain text, never
// markdown or HTML. Emits `projects`. No lit, no DOM.
//
// The preview's messages are read (GET /runs/{id}/view?limit=) when its
// project's page opens, after Send, and when the coordinator's run moves —
// its `run` events reach its owner whatever the stream follows, at a turn's
// start and end — so it shows each turn's outcome, not its tokens. While
// its own chat is open the session holds it live, and the preview reads that.
import { projCall, qs, projectHome } from './project-api.js';
import { homeOf } from './homes.js';
import { fold, FoldCache, activity, busy } from './fold.js';

// What an event's kind is called, and its tone (run | ok | bad | warn | idle).
export const EVENT_KINDS = {
  'task.created': ['task made', 'idle'], 'task.state': ['turn ended', 'idle'], 'task.human': ['a person wrote to it', 'idle'],
  'task.cancel': ['cancelled', 'idle'], workspace: ['workspace', 'run'], 'pr.opened': ['pull request opened', 'run'],
  'pr.ready': ['checks passed', 'ok'], 'ci.failed': ['CI failed', 'bad'], 'ci.stuck': ['CI keeps failing', 'warn'],
  review: ['review forwarded', 'run'], comment: ['comment (not forwarded)', 'idle'], merged: ['merged', 'ok'], closed: ['closed', 'idle'],
  push: ['someone else pushed', 'warn'], issue: ['issue', 'idle'], note: ['note', 'warn'],
};

/** httpsUrl: an https link, else '' — a link that came from the scm provider or a member's space is drawn only if it is one. */
export const httpsUrl = (u) => (/^https:\/\/[^\s]+$/i.test(String(u || '')) ? String(u) : '');

const TEXT_MAX = 300;
// plain: one line of untrusted text — control and format characters out, clipped
export function plain(s, max = TEXT_MAX) {
  const t = String(s ?? '').replace(/[\u0000-\u0008\u000b-\u001f\u007f​-‏‪-‮⁦-⁩﻿]/g, '').trim();
  return t.length > max ? t.slice(0, max - 1) + '…' : t;
}

/** feedWords(ev): an event as a line — {id, n, kind, label, tone, text, by, url, when, wake}. */
export function feedWords(ev) {
  let body = ev && ev.body;
  if (typeof body === 'string') { try { body = JSON.parse(body); } catch { body = { text: body }; } }
  body = body && typeof body === 'object' ? body : {};
  const [label, tone] = EVENT_KINDS[ev.kind] || [plain(ev.kind, 40) || 'event', 'idle'];
  return {
    id: ev.id, n: ev.n || 0, kind: ev.kind, label, tone,
    text: plain(body.text || body.title || ''),
    by: plain(body.by || '', 60),
    url: httpsUrl(body.url),
    when: ev.created || 0,
    wake: !!ev.wake,
  };
}

// --- the coordinator's preview (D209) -----------------------------------------------------------

export const COORD_TAIL = 3;  // the messages the coordinator card shows
export const COORD_READ = 30; // the messages read for them (GET /runs/{id}/view?limit=): a turn's tool calls and results take several,
                              // and the overview's window (D214) holds more than fit
export const COORD_WINDOW = 30; // the blocks the overview's chat window holds (it shows the newest that fit)
const SAID = new Set(['user', 'assistant', 'draft']);
const STEPS = new Set(['tool', 'agent', 'notice']); // one line each in the overview's window

/** coordTail(blocks, n): the conversation's last n messages — the person's and the agent's
 * (fold.js blocks; its tool calls, thinking and notices stay in its chat). */
export function coordTail(blocks, n = COORD_TAIL) {
  const said = (blocks || []).filter((b) => SAID.has(b.k) && (b.k !== 'user' || b.text));
  return said.length > n ? said.slice(-n) : said;
}

/** coordRecent(blocks, n): the overview's chat window (D214) — the last n of the person's and the
 * agent's messages and its tool steps, notices and subagents (each drawn as one line); its
 * thinking and journal steps stay in its chat. */
export function coordRecent(blocks, n = COORD_WINDOW) {
  const shown = (blocks || []).filter((b) => (SAID.has(b.k) && (b.k !== 'user' || b.text)) || STEPS.has(b.k));
  return shown.length > n ? shown.slice(-n) : shown;
}

export const COORD_CLIP = 700; // a message's characters the preview shows; the rest is in its chat
/** clipSaid(text, max): a message cut for the preview — at a line's end before max, a code
 * fence left open closed again, "…" after; text that fits as it is. */
export function clipSaid(text, max = COORD_CLIP) {
  const t = String(text || '');
  if (t.length <= max) return t;
  let cut = t.slice(0, max);
  const nl = cut.lastIndexOf('\n');
  if (nl > max / 2) cut = cut.slice(0, nl);
  cut = cut.trimEnd();
  if ((cut.match(/^\s*```/gm) || []).length % 2) cut += '\n```';
  return `${cut}\n\n…`;
}
const clipMemo = new WeakMap();
// short: a block as the preview shows it (the same object while it fits; a cut copy, made once, else)
const short = (b) => {
  if (!b.text || b.text.length <= COORD_CLIP) return b;
  let c = clipMemo.get(b);
  if (!c) clipMemo.set(b, (c = { ...b, text: clipSaid(b.text), clipped: true }));
  return c;
};

/** coordState(run): what the coordinator does now — {key: none | working | waiting | failed | idle, word, tone (run | warn | bad | '')}. */
export function coordState(run) {
  if (!run) return { key: 'none', word: '', tone: '' };
  if (run.status === 'waiting_input' || run.waiting) return { key: 'waiting', word: 'waiting for you', tone: 'warn' };
  if (busy(run.status) || run.status === 'queued' || run.status === 'blocked') return { key: 'working', word: 'working', tone: 'run' };
  if (run.status === 'error') return { key: 'failed', word: 'failed', tone: 'bad' };
  return { key: 'idle', word: '', tone: '' };
}

/** The preview's words: no coordinator yet, and what Send does then. */
export const COORD_EMPTY = 'No conversation with it yet — your first message starts it.';

// At most this many pages of 200 read at once. The events route reads
// oldest first only (since=), so a project with more than 1,000 events in
// its window shows its oldest ones first: `more` says newer ones wait, and
// the next read (Read newer, or a `project` event) goes on from the last.
const PAGES = 5;

class Feed {
  constructor(app) {
    this.app = app;
    this.feeds = new Map();  // pid → {items (oldest first), last, loading, err, loaded, more}
    this.coords = new Map(); // pid → {run, busy, err, note, text, …the preview's}
    this.timers = new Map(); // pid → a coalesced read's timer
    this.byRun = new Map();  // a coordinator's run id → its project's id (its run events)
    this.tailTimers = new Map(); // pid → a coalesced tail read's timer
    const pj = app.projects;
    const take = pj.take.bind(pj);
    pj.take = (ev) => { take(ev); this.take(ev); };
  }

  changed() { this.app.emit('projects'); }
  feed(pid) {
    let f = this.feeds.get(+pid);
    if (!f) { f = { items: [], last: 0, loading: false, err: '', loaded: false, more: false, seq: 0 }; this.feeds.set(+pid, f); }
    return f;
  }

  /** items(pid, limit): the feed, newest first — what is held: reading it holds nothing (only
   * ensure, from the Activity screen, does; a held feed is read again on every `project` event). */
  items(pid, limit = 0) {
    const f = this.feeds.get(+pid);
    const all = f ? [...f.items].reverse() : [];
    return limit ? all.slice(0, limit) : all;
  }

  // load reads what is newer than the last event held, page after page
  // (at most PAGES; `more` when newer ones still wait); a read already
  // under way is let be.
  async load(pid) {
    pid = +pid;
    const f = this.feed(pid);
    if (f.loading) { f.again = true; return; }
    f.loading = true;
    const seq = f.seq;
    try {
      let more = false;
      for (let i = 0; i < PAGES; i++) {
        const r = await projCall(projectHome(pid), `/projects/${pid}/events${qs({ since: f.last, limit: 200 })}`);
        if (seq !== f.seq) return; // forgotten meanwhile
        const items = (r && r.items) || [];
        for (const ev of items) if (ev.id > f.last) { f.items.push(ev); f.last = ev.id; }
        more = !!(r && r.next && items.length);
        if (!more) break;
      }
      f.more = more;
      if (f.items.length > 500) f.items = f.items.slice(-500);
      f.err = '';
    } catch (e) { if (seq === f.seq) f.err = e.message; }
    if (seq !== f.seq) return;
    f.loading = false;
    f.loaded = true;
    this.changed();
    if (f.again) { f.again = false; this.load(pid); }
  }

  // ensure: the feed read once when a view shows it.
  ensure(pid) {
    const f = this.feed(pid);
    if (!f.loaded && !f.loading && !f.err) this.load(pid);
    return f;
  }

  forget(pid) { const f = this.feeds.get(+pid); if (f) f.seq++; this.feeds.delete(+pid); }

  // take: a `project` event for a project whose feed is held reads what is new (coalesced, 300 ms);
  // a coordinator's `run` event, its preview's messages (moved).
  take(ev) {
    if (ev && ev.type === 'run' && this.byRun.has(ev.run)) { this.moved(this.byRun.get(ev.run), ev.data || {}); return; }
    if (!ev || ev.type !== 'project') return;
    const d = ev.data || {};
    const pid = +d.id;
    if (!pid || !this.feeds.has(pid)) return;
    if (d.change === 'deleted') { this.forget(pid); this.coords.delete(pid); return; }
    if (!['event', 'task'].includes(d.change) || this.timers.has(pid)) return;
    this.timers.set(pid, setTimeout(() => { this.timers.delete(pid); if (this.feeds.has(pid)) this.load(pid); }, 300));
  }

  // --- the coordinator ----------------------------------------------------------------------

  coord(pid) {
    let c = this.coords.get(+pid);
    if (!c) {
      c = { run: null, busy: '', err: '', note: '', text: '', missing: false,
        // the preview's: look '' | reading | done | failed | unsupported (GET /projects/{pid}/coordinator),
        // view (its newest messages, read), stale (to read again), gen (the page's opening it was read for)
        look: '', lookErr: '', view: null, stale: true, reading: false, again: false, tailErr: '', readAt: 0, gen: -1, cache: new FoldCache() };
      this.coords.set(+pid, c);
    }
    return c;
  }
  found(pid, run) {
    const c = this.coord(pid);
    if (c.run && c.run.id !== run.id) { this.byRun.delete(c.run.id); c.view = null; c.cache = new FoldCache(); }
    c.run = run;
    c.look = 'done';
    this.byRun.set(run.id, +pid);
  }
  setText(pid, text) { this.coord(pid).text = text; this.changed(); }

  // call: POST /projects/{pid}/coordinator — the caller's coordinator, made
  // on first use; `text` queued to it. A backend without the route (the
  // mux's plain 404) says so once and stops offering it.
  async call(pid, text, what) {
    const c = this.coord(pid);
    c.busy = what; c.err = ''; c.note = '';
    this.changed();
    try {
      const r = await projCall(projectHome(pid), `/projects/${pid}/coordinator`, 'POST', text ? { text } : {});
      if (r && r.run && r.run.id) { this.found(pid, r.run); c.stale = true; if (text) this.readTail(pid); } // what you wrote shows in the preview
      return c.run;
    } catch (e) {
      if (e.status === 404 && !(e.data && typeof e.data === 'object')) { c.missing = true; c.err = 'This agent has no project coordinator yet.'; } else c.err = e.message;
      return null;
    } finally { c.busy = ''; this.changed(); }
  }

  /** openCoordinator(pid): your coordinator's run ({id, …}), made if need be — the view opens it. */
  openCoordinator(pid) { return this.call(pid, '', 'open'); }

  /** stopCoordinator(pid): interrupt its turn (POST /runs/{id}/interrupt, as the chat's Stop does) —
   * the text of the messages still queued to it ('' none), for the input to take back. */
  async stopCoordinator(pid) {
    const c = this.coord(pid);
    const id = c.run && c.run.id;
    if (!id || c.busy) return '';
    c.busy = 'stop'; c.err = ''; c.note = '';
    this.changed();
    try {
      const r = await projCall(homeOf(id), `/runs/${id}/interrupt`, 'POST');
      return ((r && r.returned) || []).map((q) => q.text).filter(Boolean).join('\n\n');
    } catch (e) { c.err = e.message; return ''; } finally { c.busy = ''; this.changed(); }
  }

  /** messageCoordinator(pid, text): write to it (it reads it at its next step, or starts a turn). */
  async messageCoordinator(pid, text) {
    const t = String(text ?? this.coord(pid).text ?? '').trim();
    const c = this.coord(pid);
    if (c.busy) return null; // a send (or open) under way: a second Enter queues nothing twice
    if (!t) { c.err = 'Write the message first.'; this.changed(); return null; }
    const run = await this.call(pid, t, 'send');
    if (run) { c.text = ''; c.note = 'Sent to the coordinator.'; this.changed(); }
    return run;
  }

  // --- the coordinator's preview (D209) ---------------------------------------------------------

  async lookup(pid) {
    const c = this.coord(pid);
    if (c.look === 'reading') return;
    c.look = 'reading';
    try {
      const r = await projCall(projectHome(pid), `/projects/${pid}/coordinator`);
      if (r && r.run && r.run.id) this.found(pid, r.run);
      else if (c.look === 'reading') c.look = 'done'; // none yet (a POST meanwhile has found one)
    } catch (e) {
      // a backend without the route: 405 (the path has its POST only), or the mux's plain 404
      if (e.status === 405 || (e.status === 404 && !(e.data && typeof e.data === 'object'))) c.look = 'unsupported';
      else { c.look = 'failed'; c.lookErr = e.message; }
    }
    this.changed();
  }

  // followed: the coordinator's chat is open (it, or a subagent of it) — the
  // session holds it, kept current by the stream.
  followed(id) {
    const s = this.app.session;
    if (!s || s.sel == null) return false;
    const v = s.views.get(s.sel);
    return s.sel === id || !!(v && v.run && v.run.rootId === id);
  }

  // readTail reads the coordinator's newest messages (one read at a time; one
  // asked for meanwhile follows it). A coordinator that is gone (404) is none.
  async readTail(pid) {
    const c = this.coord(pid);
    const id = c.run && c.run.id;
    if (!id) return;
    if (c.reading) { c.again = true; return; }
    c.stale = false;
    c.reading = true;
    try {
      const v = await projCall(homeOf(id), `/runs/${id}/view${qs({ limit: COORD_READ })}`);
      if (c.run && c.run.id === id && v && v.run) {
        for (const k of ['messages', 'steps', 'links']) v[k] = v[k] || [];
        c.view = v;
        c.tailErr = '';
      }
    } catch (e) {
      if (e.status === 404 && c.run && c.run.id === id) { this.byRun.delete(id); c.run = null; c.view = null; c.look = ''; } // deleted: looked up again
      else c.tailErr = e.message;
    }
    c.reading = false;
    c.readAt = Date.now();
    this.changed();
    if (c.again) { c.again = false; this.readTail(pid); }
  }

  // moved: the coordinator's run event — its summary is the session's (Session
  // keeps every run event's), its messages are read again while its project's
  // page shows it (coalesced: at most one read a second), else when it next does.
  moved(pid, d) {
    const c = this.coord(pid);
    if (d.deleted) { if (c.run) this.byRun.delete(c.run.id); c.run = null; c.view = null; c.look = ''; this.changed(); return; }
    const pj = this.app.projects;
    if (this.app.page !== 'projects' || !pj || pj.opened !== pid) { c.stale = true; this.changed(); return; } // read when it shows
    if (!this.tailTimers.has(pid)) {
      const wait = Math.max(300, 1000 - (Date.now() - (c.readAt || 0)));
      this.tailTimers.set(pid, setTimeout(() => {
        this.tailTimers.delete(pid);
        c.stale = true; // (marked only now: a paint meanwhile reads nothing)
        if (c.run && !this.followed(c.run.id)) this.readTail(pid);
      }, wait));
    }
    this.changed(); // its state: the session's run summary
  }

  /**
   * preview(pid): the coordinator card's small chat — what a view draws:
   *   shown   false where the backend can't say whether you have one (an
   *           older one: the card is drawn as it was, Open and the line)
   *   run     your coordinator's summary; null while you have none (the
   *           first message starts it: COORD_EMPTY)
   *   state   coordState(run); line  what it does now while it works or waits
   *   tail    its last COORD_TAIL messages (fold.js blocks), null until read;
   *           more  older ones are in its chat
   *   recent  the overview's window (D214): its last COORD_WINDOW messages and
   *           steps (coordRecent), null until read; older  some are not in it
   *   loading, err  the lookup's or the read's
   * Reads what it lacks: the lookup once, the messages when the project's page
   * opens (app.projects.opens), after Send and when its run moves.
   */
  preview(pid) {
    pid = +pid;
    const c = this.coord(pid);
    const pj = this.app.projects;
    const gen = (pj && pj.opens) || 0;
    if (c.gen !== gen) { c.gen = gen; c.stale = true; if (c.look === 'failed') c.look = ''; }
    if (!c.look) this.lookup(pid);
    if (c.look === 'unsupported' || c.missing) return { shown: false };
    const id = c.run && c.run.id;
    if (!id) {
      return { shown: true, run: null, state: coordState(null), line: '', tail: [], more: false, recent: [], older: false,
        loading: c.look === 'reading' || (c.look === '' && !c.lookErr), err: c.look === 'failed' ? c.lookErr : '' };
    }
    const s = this.app.session;
    const live = this.followed(id);
    if (c.stale && !live && !c.reading) this.readTail(pid);
    let v = null, blocks = null;
    if (live) {
      v = s.merged(id);
      blocks = v ? s.blocks(id, v) : null;
    } else if (c.view) {
      v = { ...c.view, run: { ...c.run, ...c.view.run, ...((s && s.runs.get(id)) || {}) }, draft: null };
      blocks = fold(v, () => null, 0, c.cache);
    }
    const run = v ? v.run : { ...c.run, ...((s && s.runs.get(id)) || {}) };
    const state = coordState(run);
    const tail = blocks ? coordTail(blocks).map(short) : null;
    const all = blocks ? coordTail(blocks, Infinity) : [];
    const every = blocks ? coordRecent(blocks, Infinity) : [];
    const recent = blocks ? every.slice(-COORD_WINDOW).map((b) => (SAID.has(b.k) ? short(b) : b)) : null;
    return {
      shown: true, run, state,
      line: state.key === 'working' || state.key === 'waiting' ? activity(v || { run }, blocks || []) : '',
      tail, more: !!tail && (!!(v && v.hasOlder) || all.length > tail.length),
      recent, older: !!recent && (!!(v && v.hasOlder) || every.length > recent.length),
      loading: !tail && c.reading, err: tail ? '' : c.tailErr,
    };
  }
}

/** projectFeed(app): the app's feed and coordinator store, made on first use. */
export function projectFeed(app) {
  if (!app.projFeed) app.projFeed = new Feed(app);
  return app.projFeed;
}
