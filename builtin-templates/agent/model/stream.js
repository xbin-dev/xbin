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
//     shows that it is reconnecting.

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export class Live {
  /**
   * @param {string} base      this backend's prefix (/api/<self>)
   * @param {object} on        {event(ev), reset(), state(s)} — s: live | reconnecting
   * @param {object} opts      {deltas}: ask for text.delta / thinking.delta (API.md
   *                           "Deltas") instead of the whole draft text every time
   */
  constructor(base, on, opts = {}) {
    this.base = base;
    this.on = on;
    this.deltas = !!opts.deltas;
    this.run = null;
    this.cursor = '';
    this.gen = 0;
    this.ctrl = null;
  }

  // follow (re)connects for a run's tree (null = the run list only), from a
  // cursor a /view returned.
  follow(run, cursor) {
    this.run = run;
    this.cursor = cursor || '';
    this.gen++;
    if (this.ctrl) this.ctrl.abort();
    this.loop(this.gen);
  }

  // resync reconnects from where the stream is: the backend sends every live
  // draft in full again (a delta that did not fit what the client holds).
  resync() { this.follow(this.run, this.cursor); }

  close() {
    this.gen++;
    if (this.ctrl) this.ctrl.abort();
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
