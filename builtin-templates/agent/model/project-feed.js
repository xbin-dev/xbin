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
//
// The feed is kept current by the `project` stream event (change `event`,
// or any change about a task: a turn ended, a PR moved) — app.projects.take
// is wrapped once, so nothing else in the model changes. An event's body is
// the backend's ({text, by?, sha?, url?, …}): scm text inside it is
// untrusted (redacted there, clipped here) and drawn as plain text, never
// markdown or HTML. Emits `projects`. No lit, no DOM.
import { projCall, qs, projectHome } from './project-api.js';

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

// At most this many pages of 200 read at once. The events route reads
// oldest first only (since=), so a project with more than 1,000 events in
// its window shows its oldest ones first: `more` says newer ones wait, and
// the next read (Read newer, or a `project` event) goes on from the last.
const PAGES = 5;

class Feed {
  constructor(app) {
    this.app = app;
    this.feeds = new Map();  // pid → {items (oldest first), last, loading, err, loaded, more}
    this.coords = new Map(); // pid → {run, busy, err, note, text}
    this.timers = new Map(); // pid → a coalesced read's timer
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

  /** items(pid, limit): the feed, newest first. */
  items(pid, limit = 0) {
    const all = [...this.feed(pid).items].reverse();
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

  // take: a `project` event for a project whose feed is held reads what is new (coalesced, 300 ms).
  take(ev) {
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
    if (!c) { c = { run: null, busy: '', err: '', note: '', text: '', missing: false }; this.coords.set(+pid, c); }
    return c;
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
      c.run = (r && r.run) || null;
      return c.run;
    } catch (e) {
      if (e.status === 404 && !(e.data && typeof e.data === 'object')) { c.missing = true; c.err = 'This agent has no project coordinator yet.'; } else c.err = e.message;
      return null;
    } finally { c.busy = ''; this.changed(); }
  }

  /** openCoordinator(pid): your coordinator's run ({id, …}), made if need be — the view opens it. */
  openCoordinator(pid) { return this.call(pid, '', 'open'); }

  /** messageCoordinator(pid, text): write to it (it reads it at its next step, or starts a turn). */
  async messageCoordinator(pid, text) {
    const t = String(text ?? this.coord(pid).text ?? '').trim();
    const c = this.coord(pid);
    if (!t) { c.err = 'Write the message first.'; this.changed(); return null; }
    const run = await this.call(pid, t, 'send');
    if (run) { c.text = ''; c.note = 'Sent to the coordinator.'; this.changed(); }
    return run;
  }
}

/** projectFeed(app): the app's feed and coordinator store, made on first use. */
export function projectFeed(app) {
  if (!app.projFeed) app.projFeed = new Feed(app);
  return app.projFeed;
}
