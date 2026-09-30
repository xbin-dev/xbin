// model/stream.js — the tile's live connection to its backend (GET /stream).
// (stream.js at the tile's root re-exports it.)
//
// Server-sent events over xbin.fetch and a body reader (a sandboxed frame has
// no EventSource with credentials). One connection per tile: it follows the
// selected run's whole tree plus the run list. Nothing here polls:
//
//   - every event carries its cursor ("<gen>.<seq>"); a reconnect hands the
//     last one back and the backend replays what was missed — or answers
//     `reset` (another process, or too old) and the tile re-reads its views;
//   - `bye` means this backend is being replaced: reconnect at once and the
//     successor answers;
//   - a stream that just ends is retried with a short backoff, and the tile
//     shows that it is reconnecting;
//   - in a partitioned instance (model/partition.js) the stream closes while
//     the page is hidden and reconnects from its cursor when it shows again,
//     so a background tab doesn't keep the viewer's partition running. An
//     unpartitioned instance's stream ignores visibility, as it always has.
import { pausesHidden } from './partition.js';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const doc = () => (typeof document !== 'undefined' ? document : null);

export class Live {
  /**
   * @param {string} base      this backend's prefix (/api/<self>)
   * @param {object} on        {event(ev), reset(), state(s)} — s: live | reconnecting
   * @param {object} opts      {deltas}: ask for text.delta / thinking.delta / tool.delta
   *                           (API.md "Deltas") instead of the whole draft text (a tool
   *                           call's whole arguments) every time; {pauseHidden}: close
   *                           while the page is hidden (default: in a partitioned instance)
   */
  constructor(base, on, opts = {}) {
    this.base = base;
    this.on = on;
    this.deltas = !!opts.deltas;
    this.run = null;
    this.cursor = '';
    this.gen = 0;
    this.ctrl = null;
    this.following = false; // follow() was called and close() wasn't since
    this.paused = false;    // closed for a hidden page; reconnects when it shows
    this.pauseHidden = opts.pauseHidden ?? pausesHidden();
    const d = doc();
    if (this.pauseHidden && d) d.addEventListener('visibilitychange', () => this.visibility());
  }

  hidden() { const d = doc(); return this.pauseHidden && !!d && !!d.hidden; }

  // follow (re)connects for a run's tree (null = the run list only), from a
  // cursor a /view returned — once the page shows, when it is hidden.
  follow(run, cursor) {
    this.run = run;
    this.cursor = cursor || '';
    this.gen++;
    this.following = true;
    if (this.ctrl) this.ctrl.abort();
    this.paused = this.hidden();
    if (!this.paused) this.loop(this.gen);
  }

  // resync reconnects from where the stream is: the backend sends every live
  // draft in full again (a delta that did not fit what the client holds).
  resync() { this.follow(this.run, this.cursor); }

  close() {
    this.gen++;
    this.following = false;
    this.paused = false;
    if (this.ctrl) this.ctrl.abort();
  }

  // visibility: hidden, the stream closes (its cursor kept); shown again, it
  // reconnects from there — what it missed is replayed, or `reset` re-reads.
  visibility() {
    if (!this.following) return;
    if (this.hidden()) {
      if (this.paused) return;
      this.paused = true;
      this.gen++;
      if (this.ctrl) this.ctrl.abort();
    } else if (this.paused) {
      this.paused = false;
      this.gen++;
      this.loop(this.gen);
    }
  }

  url() {
    const q = new URLSearchParams();
    if (this.run != null) q.set('run', String(this.run));
    if (this.cursor) q.set('since', this.cursor);
    if (this.deltas) q.set('deltas', '1');
    return `${this.base}/stream?${q}`;
  }

  async loop(gen) {
    let backoff = 400;
    while (gen === this.gen) {
      let bye = false;
      const ctrl = new AbortController();
      this.ctrl = ctrl;
      try {
        const r = await xbin.fetch(this.url(), { signal: ctrl.signal, headers: { Accept: 'text/event-stream' } });
        if (!r.ok || !r.body) throw new Error(`stream: HTTP ${r.status}`);
        this.on.state?.('live');
        backoff = 400;
        const reader = r.body.pipeThrough(new TextDecoderStream()).getReader();
        let buf = '';
        for (;;) {
          const { value, done } = await reader.read();
          if (done || gen !== this.gen) break;
          buf += value;
          let i;
          while ((i = buf.indexOf('\n\n')) >= 0) {
            const chunk = buf.slice(0, i);
            buf = buf.slice(i + 2);
            if (this.chunk(chunk) === 'bye') bye = true;
          }
        }
      } catch { /* aborted, or the connection dropped */ }
      if (gen !== this.gen) return;
      this.on.state?.('reconnecting');
      await sleep(bye ? 50 : backoff);
      backoff = Math.min(backoff * 2, 8000);
    }
  }

  chunk(text) {
    let id = '', data = '';
    for (const line of text.split('\n')) {
      if (line.startsWith('id: ')) id = line.slice(4);
      else if (line.startsWith('data: ')) data += line.slice(6);
    }
    if (!data) return '';
    let ev;
    try { ev = JSON.parse(data); } catch { return ''; }
    if (id && ev.type !== 'hello') this.cursor = id;
    switch (ev.type) {
      case 'hello':
        if (ev.data && ev.data.cursor && !this.cursor) this.cursor = ev.data.cursor;
        return '';
      case 'bye':
        return 'bye';
      case 'reset':
        this.on.reset?.();
        return '';
    }
    this.on.event?.(ev);
    return '';
  }
}
