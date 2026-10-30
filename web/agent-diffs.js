/**
 * agent-diffs.js — a change set reviewed a file at a time (D210, #45). A
 * files.changed event — what a tool call or a whole turn changed — carries
 * the per-file list and the patch inline, capped (64 KiB a call, 192 KiB a
 * turn). Opening a turn that rewrote thousands of lines used to render that
 * whole patch at once and the Agent tab lagged; now its card lists the files
 * and each row opens its own diff, from
 *   1. the file's part of the inline patch, when the patch holds all of it;
 *   2. else `GET /term/sessions/<id>/diff?turn=|toolCallId=&path=` — the
 *      whole file's diff, while the session lives;
 *   3. else what the inline patch holds of it, marked cut.
 * A diff shows its first DIFF_LINES lines until the reader asks for all.
 * No DOM and fetch injected, so hack/agent-diffs.test.mjs runs it under node.
 */
import { fileDiffs } from './code-shared.js';

export const DIFF_LINES = 400; // a file's diff lines shown before "Show all"

// lineCount(s): the lines of a patch (its final newline ends the last one)
export function lineCount(s) {
  if (!s) return 0;
  let n = 0;
  for (let i = s.indexOf('\n'); i >= 0; i = s.indexOf('\n', i + 1)) n++;
  return s.endsWith('\n') ? n : n + 1;
}

// headLines(s, n): the first n lines of s
export function headLines(s, n) {
  let i = -1;
  for (let k = 0; k < n; k++) {
    i = s.indexOf('\n', i + 1);
    if (i < 0) return s;
  }
  return s.slice(0, i + 1);
}

// diffBody(s): one file's diff without git's header lines (its row already
// names the file and its status) — a rename, a mode change or "Binary files
// … differ" stay, as does everything from the first hunk on
const HEADER = /^(diff --git |index |--- |\+\+\+ |new file mode|deleted file mode|similarity index|dissimilarity index)/;
export function diffBody(s) {
  const at = s.search(/^@@/m);
  const kept = (at < 0 ? s : s.slice(0, at)).split('\n').filter((l) => l && !HEADER.test(l));
  return (kept.length ? kept.join('\n') + '\n' : '') + (at < 0 ? '' : s.slice(at));
}

// inlinePart(parts, truncated, path): a file's part of an event's own patch
// (parts: fileDiffs of it) → {text, cut} — cut when the patch was truncated
// inside it (its last part) — or null when the patch does not reach it
export function inlinePart(parts, truncated, path) {
  const i = parts.findIndex((p) => p.path === path);
  if (i < 0) return null;
  return { text: parts[i].text, cut: !!truncated && i === parts.length - 1 };
}

// diffURL(session, sel, path): the full-diff route for one file of a change
// set; sel is {turn} (a turn's) or {toolCallId} (a call's)
export function diffURL(session, sel, path) {
  const q = new URLSearchParams(sel.turn != null ? { turn: String(sel.turn) } : { toolCallId: sel.toolCallId || '' });
  q.set('path', path);
  return `/api/xbin/term/sessions/${encodeURIComponent(session)}/diff?${q}`;
}

const NOT_KEPT = 'This file is past what the transcript keeps of the change, and the session no longer has it';

/**
 * FileDiffs: the diffs a transcript's reader opened. get() answers at once —
 * {state:'ok', text, cut, from:'inline'|'server'}, {state:'loading'} or
 * {state:'none', note} — starting a load when it must; onLoad(owner) when
 * one lands (the owner block re-renders). Entries are kept per session,
 * block and file, the most recent `max` of them.
 */
export class FileDiffs {
  constructor({ fetch: get, onLoad, max = 64 } = {}) {
    this._fetch = get || ((u) => fetch(u));
    this._onLoad = onLoad || (() => {});
    this._max = max;
    this._map = new Map();
    this._parts = new WeakMap(); // a files record → its patch split per file
    this.loads = []; // the route's URLs this asked, in order (the harness counts them)
  }

  _partsOf(f) {
    let p = this._parts.get(f);
    if (!p) { p = fileDiffs(f.patch?.text || ''); this._parts.set(f, p); }
    return p;
  }

  _put(key, e) {
    this._map.delete(key);
    this._map.set(key, e);
    while (this._map.size > this._max) this._map.delete(this._map.keys().next().value);
    return e;
  }

  // ctx {session, live}: live — the session runs, so the route can answer;
  // owner: the block the change set hangs on; f: its {changes, patch};
  // c: the file's row; sel: {turn} | {toolCallId}
  get(ctx, owner, f, c, sel) {
    const key = `${ctx.session || ''}|${owner?.key ?? ''}|${sel.turn != null ? 't' + sel.turn : 'c' + sel.toolCallId}|${c.path}`;
    const had = this._map.get(key);
    if (had) return this._put(key, had);
    const part = inlinePart(this._partsOf(f), f.patch?.truncated, c.path);
    if (part && !part.cut) return this._put(key, { state: 'ok', text: part.text, cut: false, from: 'inline' });
    if (ctx.live && ctx.session) {
      const e = this._put(key, { state: 'loading' });
      this._load(key, e, diffURL(ctx.session, sel, c.path), part, owner);
      return e;
    }
    return this._put(key, part ? { state: 'ok', text: part.text, cut: true, from: 'inline' } : { state: 'none', note: NOT_KEPT });
  }

  async _load(key, e, url, part, owner) {
    this.loads.push(url);
    let next;
    try {
      const r = await this._fetch(url);
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      const text = await r.text();
      next = { state: 'ok', text, cut: r.headers?.get?.('X-Truncated') === 'true', from: 'server' };
    } catch (err) {
      // the session ended (404), a viewer may not ask (403), the network: what the event holds
      next = part ? { state: 'ok', text: part.text, cut: true, from: 'inline' } : { state: 'none', note: NOT_KEPT };
    }
    if (this._map.get(key) === e) this._map.set(key, next);
    this._onLoad(owner);
  }
}
