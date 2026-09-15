// web/term-predict.js — predictive local echo for <bx-terminal>: mosh's
// PredictionEngine (src/frontend/terminaloverlay.cc) in its "experimental"
// flavour, over a duck-typed framebuffer. Imports nothing, so
// hack/term-predict.test.mjs runs it under node (`make js-test`). D70.
//
// The model. Typed bytes produce PREDICTIONS — overlay cells {row, col,
// replacement} and one cursor position — each stamped with the input frame
// whose effect it predicts (`expiration`) and the time it was made. The
// server acks an input frame once it is ECHO_TIMEOUT old (docs/protocol.md
// §/ws/term), so when lateAck ≥ expiration the framebuffer already shows the
// application's answer: a matching cell is confirmed and dropped, a wrong
// cell is dropped alone (experimental: no epoch-wide reset), a blank
// replacement never counts as wrong. Predictions are always computed;
// whether they are DISPLAYED is the mode's call: 'on' always, 'off' never,
// 'auto' when the smoothed RTT is above SRTT_SHOW (hysteresis down to
// SRTT_HIDE) or a prediction has gone unanswered for GLITCH_THRESHOLD ms.
// Displayed predictions are underlined ("flagged") past SRTT_FLAG or on a
// long glitch, so the user can tell a guess from an echo.
//
// Beyond mosh (D71): a HIDDEN terminal cursor. Full-screen programs — Ink
// apps such as Claude Code, most TUIs — hide the cursor, draw their own, and
// echo typed text wherever their input field is; the terminal cursor then
// says nothing. In that state the engine learns the ANCHOR — the cell the
// last typed character appeared in, plus one — from the echo itself, and
// predicts there in overwrite mode (no shift: the field's frame must stay
// put). Until the first echo of an input session nothing is predicted.
//
// The framebuffer is whatever the caller wraps xterm in:
//   { rows, cols, cursor: {row, col}, charAt(row, col) → '' | ' ' | glyph,
//     widthAt(row, col) → 0 | 1 | 2, lineAt(row) → the row's text, one
//     character per cell }
// Left out on purpose (D70): renditions (the overlay draws in the terminal's
// default colours), scroll prediction, wide characters.

export const SRTT_SHOW = 100, SRTT_HIDE = 60;    // ms: auto shows predictions above SHOW, hides again at ≤ HIDE (mosh: 60/40)
export const SRTT_FLAG = 160, SRTT_UNFLAG = 100; // ms: underline predictions above FLAG (mosh's own thresholds)
export const GLITCH_THRESHOLD = 250;             // a prediction pending this long is a glitch: show them regardless of RTT
export const GLITCH_REPAIR_COUNT = 10;           // quick confirmations that cure a glitch
export const GLITCH_REPAIR_MININTERVAL = 150;    // ms between confirmations that count toward the cure
export const GLITCH_FLAG_THRESHOLD = 5000;       // pending this long: show AND underline
export const ECHO_TIMEOUT = 50;                  // the server acks input this long after the PTY took it
export const MAX_CHUNK = 64;                     // longer input chunks (pastes) predict nothing
export const MAX_LEARN = 32;                     // keystrokes remembered for anchor learning
export const LEARN_TTL = 5000;                   // ms a keystroke waits for its echo before it is forgotten

// RFC 6298 smoothing, as mosh's network layer does it.
export const srttUpdate = (prev, r) => (prev == null ? r : prev * 7 / 8 + r / 8);

// wcwidth, compact: 0 for combining marks, 2 for East-Asian wide and emoji,
// 1 for everything else. Only width-1 glyphs are predicted.
export function wcwidth(cp) {
  if ((cp >= 0x0300 && cp <= 0x036f) || (cp >= 0x1ab0 && cp <= 0x1aff) || (cp >= 0x1dc0 && cp <= 0x1dff) ||
      (cp >= 0x20d0 && cp <= 0x20ff) || (cp >= 0xfe20 && cp <= 0xfe2f) || cp === 0x200b || cp === 0x200d) return 0;
  if ((cp >= 0x1100 && cp <= 0x115f) || (cp >= 0x2e80 && cp <= 0x303e) || (cp >= 0x3041 && cp <= 0x33ff) ||
      (cp >= 0x3400 && cp <= 0x4dbf) || (cp >= 0x4e00 && cp <= 0x9fff) || (cp >= 0xa000 && cp <= 0xa4cf) ||
      (cp >= 0xac00 && cp <= 0xd7a3) || (cp >= 0xf900 && cp <= 0xfaff) || (cp >= 0xfe30 && cp <= 0xfe4f) ||
      (cp >= 0xff00 && cp <= 0xff60) || (cp >= 0xffe0 && cp <= 0xffe6) || (cp >= 0x1f300 && cp <= 0x1faff) ||
      (cp >= 0x20000 && cp <= 0x3fffd)) return 2;
  return 1;
}

const blank = (s) => s === '' || s === ' ';
const snapshot = (fb) => Array.from({ length: fb.rows }, (_, r) => fb.lineAt(r));

// findEcho: where did the typed character newly appear? The candidate nearest
// the expected anchor, else the bottom-most (input fields live at the bottom).
function findEcho(L, cur, fb) {
  let best = null;
  for (let r = 0; r < fb.rows; r++) {
    const b = L.before[r] ?? '', c = cur[r] ?? '';
    if (b === c) continue;
    for (let x = 0; x < c.length; x++) {
      if (c[x] !== L.ch || (b[x] ?? ' ') === L.ch) continue;
      const d = L.expect ? Math.abs(r - L.expect.row) * 1000 + Math.abs(x - L.expect.col) : (fb.rows - r) * 1000 + x;
      if (!best || d < best.d) best = { row: r, col: x, d };
    }
  }
  return best;
}
const PENDING = 0, CORRECT = 1, NO_CREDIT = 2, INCORRECT = 3;

// A fresh overlay cell. `orig` is every content this cell had before we
// predicted over it: a prediction that merely restores one of those earns
// no credit (mosh's original_contents rule — too easy to be right by luck).
const freshCell = (orig = []) => ({ active: false, unknown: false, replacement: '', expiration: 0, time: 0, orig });
// mosh's reset_with_orig: an active, known cell carries its replacement into the history.
const resetWithOrig = (c) => (c && c.active && !c.unknown ? freshCell([...c.orig, c.replacement]) : freshCell());

export class Predictor {
  constructor() {
    this.mode = 'auto';
    this.srtt = null;            // smoothed RTT in ms; null until the first pong
    this.localFrameSent = 0;     // input frames sent so far (the caller counts)
    this.lateAck = 0;            // the server's echo ack
    this.srttTrigger = false; this.flagging = false; this.glitchTrigger = 0; this.lastQuick = 0;
    this.lastRows = 0; this.lastCols = 0;
    this.rows = new Map();       // row → Array(cols) of cells (null = no prediction)
    this.cursor = null;          // { row, col, expiration, time } | null
    this.cursorHidden = false;   // the application hid the terminal cursor (DECTCEM off)
    this.anchor = null;          // hidden cursor: { row, col } where the next typed character will appear
    this.learn = [];             // hidden cursor: keystrokes awaiting their echo, to place the anchor
  }

  setMode(m) { if (m === this.mode) return; this.mode = m; if (m === 'off') this.reset(); }
  setSrtt(ms) { this.srtt = ms; }
  setLocalFrameSent(n) { this.localFrameSent = n; }
  setLateAck(n) { this.lateAck = n; }
  setCursorHidden(h) {
    h = !!h;
    if (h === this.cursorHidden) return;
    this.cursorHidden = h; this.anchor = null; this.learn = []; this.cursor = null;
  }
  reset() { this.rows.clear(); this.cursor = null; this.anchor = null; this.learn = []; }

  // Are predictions being displayed right now?
  shown() { return this.mode === 'on' || (this.mode === 'auto' && (this.srttTrigger || this.glitchTrigger > 0)); }
  // Is anything predicted (shown or not)?
  active() { return !!this.cursor || this.pending() > 0; }
  pending() { let n = 0; for (const r of this.rows.values()) for (const c of r) if (c?.active) n++; return n; }

  // ---- input → predictions (mosh: new_user_byte) ----
  newUserData(str, fb, now) {
    if (this.mode === 'off') return;
    this.cull(fb, now);
    const exp = this.localFrameSent + 1;
    if (this.cursorHidden) { this.#anchorInput(str, fb, exp, now); return; }
    // arrows move the predicted cursor (ESC O x is the application-mode spelling)
    if (str === '\x1b[C' || str === '\x1bOC') { this.#initCursor(fb, exp, now); if (this.cursor.col < fb.cols - 1) this.#moveCursor(1, exp, now); return; }
    if (str === '\x1b[D' || str === '\x1bOD') { this.#initCursor(fb, exp, now); if (this.cursor.col > 0) this.#moveCursor(-1, exp, now); return; }
    // any other escape (modified keys, bracketed paste, the terminal's own
    // DA/DSR/mouse/focus replies) and pastes predict nothing
    if (str.includes('\x1b')) return;
    const cps = [...str];
    if (cps.length > MAX_CHUNK) return;
    for (const ch of cps) {
      const cp = ch.codePointAt(0);
      if (cp === 0x7f) this.#backspace(fb, exp, now);
      else if (cp === 0x0d) this.#newlineCR(fb, exp, now);
      else if (cp >= 0x20 && wcwidth(cp) === 1) this.#print(ch, fb, exp, now);
      // other controls and wide/combining glyphs: mosh becomes tentative, which
      // experimental mode ignores — nothing is predicted
    }
  }

  // ---- hidden cursor: predict at the learned anchor (D71) ----
  #anchorInput(str, fb, exp, now) {
    const a = this.anchor;
    if (str === '\x1b[C' || str === '\x1bOC') { if (a && a.col < fb.cols - 1) a.col++; return; }
    if (str === '\x1b[D' || str === '\x1bOD') { if (a && a.col > 0) a.col--; return; }
    if (str.includes('\x1b')) return;
    const cps = [...str];
    if (cps.length > MAX_CHUNK) return;
    for (const ch of cps) {
      const cp = ch.codePointAt(0);
      if (cp === 0x0d) { this.anchor = null; this.learn = []; }   // Enter submits: the field is about to change
      else if (cp === 0x7f) { if (this.anchor && this.anchor.col > 0) { this.anchor.col--; this.#overwrite(fb, this.anchor, '', exp, now); } }
      else if (cp >= 0x20 && wcwidth(cp) === 1) {
        if (this.learn.length < MAX_LEARN) this.learn.push({ ch, expiration: exp, time: now, before: snapshot(fb), expect: this.anchor ? { ...this.anchor } : null });
        if (this.anchor) {
          this.#overwrite(fb, this.anchor, ch, exp, now);
          this.anchor = this.anchor.col + 1 < fb.cols ? { row: this.anchor.row, col: this.anchor.col + 1 } : null;
        }
      }
    }
  }
  #overwrite(fb, at, ch, exp, now) {
    if (fb.widthAt(at.row, at.col) !== 1) return;
    const r = this.#row(at.row, fb.cols);
    const cell = r[at.col] = this.#stamp(resetWithOrig(r[at.col]), exp, now);
    cell.replacement = ch;
    cell.orig.push(fb.charAt(at.row, at.col));
  }
  // learnAnchor moves the anchor to where acked keystrokes actually appeared.
  #learnAnchor(fb, now) {
    if (!this.learn.length) return;
    const cur = snapshot(fb), keep = [];
    for (const L of this.learn) {
      if (this.lateAck < L.expiration) { if (now - L.time < LEARN_TTL) keep.push(L); continue; }
      const at = findEcho(L, cur, fb);
      if (at) this.anchor = at.col + 1 < fb.cols ? { row: at.row, col: at.col + 1 } : null;
    }
    this.learn = keep;
  }

  #row(row, cols) {
    let r = this.rows.get(row);
    if (!r || r.length !== cols) { r = new Array(cols).fill(null); this.rows.set(row, r); }
    return r;
  }
  #initCursor(fb, exp, now) {
    if (!this.cursor) this.cursor = { row: Math.min(fb.cursor.row, fb.rows - 1), col: Math.min(fb.cursor.col, fb.cols - 1), expiration: exp, time: now };
  }
  #moveCursor(d, exp, now) { this.cursor.col += d; this.cursor.expiration = exp; this.cursor.time = now; }
  #stamp(cell, exp, now) { cell.active = true; cell.expiration = exp; cell.time = now; return cell; }

  #print(ch, fb, exp, now) {
    this.#initCursor(fb, exp, now);
    const { row, col } = this.cursor;
    // a wide glyph (or its continuation cell) in the span we would shift would
    // misplace every cell after it: move the cursor, predict no cells
    let plain = true;
    for (let i = col; i < fb.cols; i++) if (fb.widthAt(row, i) !== 1) { plain = false; break; }
    if (plain) {
      const r = this.#row(row, fb.cols);
      // the insert: everything right of the cursor moves one cell right; the
      // last column's content is unknown (a wrap? a lost character?)
      for (let i = fb.cols - 1; i > col; i--) {
        const prev = r[i - 1];
        const cell = r[i] = this.#stamp(resetWithOrig(r[i]), exp, now);
        cell.orig.push(fb.charAt(row, i));
        if (i === fb.cols - 1) cell.unknown = true;
        else if (prev?.active) { if (prev.unknown) cell.unknown = true; else cell.replacement = prev.replacement; }
        else cell.replacement = fb.charAt(row, i - 1);
      }
      const cell = r[col] = this.#stamp(resetWithOrig(r[col]), exp, now);
      cell.replacement = ch;
      cell.orig.push(fb.charAt(row, col));
    }
    this.cursor.expiration = exp; this.cursor.time = now;
    if (col < fb.cols - 1) this.cursor.col++;
    else this.#newlineCR(fb, exp, now); // typed in the last column: assume a wrap
  }

  #backspace(fb, exp, now) {
    this.#initCursor(fb, exp, now);
    if (this.cursor.col <= 0) return;
    this.#moveCursor(-1, exp, now);
    const { row, col } = this.cursor;
    const r = this.#row(row, fb.cols);
    // everything right of the cursor moves one cell left; the last two
    // columns become unknown
    for (let i = col; i < fb.cols; i++) {
      const next = r[i + 1];
      const cell = r[i] = this.#stamp(resetWithOrig(r[i]), exp, now);
      cell.orig.push(fb.charAt(row, i));
      if (i + 2 < fb.cols) {
        if (next?.active) { if (next.unknown) cell.unknown = true; else cell.replacement = next.replacement; }
        else cell.replacement = fb.charAt(row, i + 1);
      } else cell.unknown = true;
    }
  }

  #newlineCR(fb, exp, now) {
    this.#initCursor(fb, exp, now);
    this.cursor.col = 0; this.cursor.expiration = exp; this.cursor.time = now;
    if (this.cursor.row === fb.rows - 1) {
      // no scroll prediction (mosh: "until we have versioned cell
      // predictions"); predict the bottom row blank instead
      const r = this.#row(this.cursor.row, fb.cols);
      for (let i = 0; i < fb.cols; i++) { const cell = r[i] = this.#stamp(r[i] ?? freshCell(), exp, now); cell.replacement = ''; }
    } else this.cursor.row++;
  }

  // ---- validation against the framebuffer (mosh: cull) ----
  #cellValidity(cell, row, col, fb) {
    if (row >= fb.rows || col >= fb.cols) return INCORRECT;
    if (this.lateAck < cell.expiration) return PENDING;
    if (cell.unknown || blank(cell.replacement)) return NO_CREDIT; // a blank is too easy to be right about
    if (fb.charAt(row, col) !== cell.replacement) return INCORRECT;
    return cell.orig.includes(cell.replacement) ? NO_CREDIT : CORRECT;
  }
  #cursorValidity(fb) {
    const c = this.cursor;
    if (c.row >= fb.rows || c.col >= fb.cols) return INCORRECT;
    if (this.lateAck < c.expiration) return PENDING;
    return fb.cursor.row === c.row && fb.cursor.col === c.col ? CORRECT : INCORRECT;
  }

  cull(fb, now) {
    if (this.mode === 'off') return;
    if (fb.rows !== this.lastRows || fb.cols !== this.lastCols) { this.lastRows = fb.rows; this.lastCols = fb.cols; this.reset(); }
    const srtt = this.srtt ?? 0;
    // the RTT triggers, with hysteresis; predictions on screen keep the show trigger
    if (srtt > SRTT_SHOW) this.srttTrigger = true;
    else if (this.srttTrigger && srtt <= SRTT_HIDE && !this.active()) this.srttTrigger = false;
    if (srtt > SRTT_FLAG) this.flagging = true;
    else if (srtt <= SRTT_UNFLAG) this.flagging = false;
    if (this.glitchTrigger > GLITCH_REPAIR_COUNT) this.flagging = true; // a big glitch underlines too
    for (const [row, r] of [...this.rows]) {
      if (row < 0 || row >= fb.rows) { this.rows.delete(row); continue; }
      let live = 0;
      for (let col = 0; col < r.length; col++) {
        const cell = r[col];
        if (!cell?.active) continue;
        switch (this.#cellValidity(cell, row, col, fb)) {
          case INCORRECT: r[col] = null; break;                  // experimental: this cell only
          case CORRECT:
            // quick confirmations slowly cure a glitch
            if (now - cell.time < GLITCH_THRESHOLD && this.glitchTrigger > 0 && now - GLITCH_REPAIR_MININTERVAL >= this.lastQuick) { this.glitchTrigger--; this.lastQuick = now; }
            r[col] = null; break;
          case NO_CREDIT: r[col] = null; break;
          default: // pending: a long wait is a glitch — show predictions even on a fast link
            if (now - cell.time >= GLITCH_FLAG_THRESHOLD) this.glitchTrigger = GLITCH_REPAIR_COUNT * 2;
            else if (now - cell.time >= GLITCH_THRESHOLD && this.glitchTrigger < GLITCH_REPAIR_COUNT) this.glitchTrigger = GLITCH_REPAIR_COUNT;
            live++;
        }
      }
      if (!live) this.rows.delete(row);
    }
    if (this.cursor && this.#cursorValidity(fb) !== PENDING) this.cursor = null; // confirmed or wrong: the real cursor takes over
    if (this.cursorHidden) this.#learnAnchor(fb, now);
  }

  // ---- what to draw ----
  // Runs of consecutive predicted cells that differ from the framebuffer,
  // plus the predicted cursor. Blank predictions draw as spaces (that is how
  // the bottom row clears on Enter); unknown cells draw nothing.
  render(fb) {
    if (!this.shown()) return { cells: [], cursor: null, flagging: false };
    const cells = [];
    for (const [row, r] of this.rows) {
      if (row >= fb.rows) continue;
      let run = null;
      for (let col = 0; col < Math.min(r.length, fb.cols); col++) {
        const cell = r[col];
        let text = null;
        if (cell?.active && !cell.unknown) {
          const cur = fb.charAt(row, col);
          if (blank(cell.replacement) ? !blank(cur) : cell.replacement !== cur) text = blank(cell.replacement) ? ' ' : cell.replacement;
        }
        if (text == null) { run = null; continue; }
        if (run && run.col + run.text.length === col) run.text += text;
        else cells.push(run = { row, col, text, underline: this.flagging });
      }
    }
    return { cells, cursor: this.cursor ? { row: this.cursor.row, col: this.cursor.col } : null, flagging: this.flagging };
  }
}
