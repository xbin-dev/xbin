#!/usr/bin/env node
// hack/term-predict-trace.mjs — a differential trace of web/term-predict.js
// for its Swift port (native/ios/Packages/XbinTerm, Predictor.swift). Random
// but seeded sessions — typing, a shell-like echo, wrong echoes, acks, time,
// hidden cursors, resizes, wide cells, RTT and mode changes — are run through
// the JS engine; every step is recorded as a concrete operation plus what the
// engine then reports (null: unchanged). The Swift suite replays the
// operations and must report the same after every step
// (PredictorTraceTests.swift).
//
//   node hack/term-predict-trace.mjs            # rewrite the committed trace
//   node hack/term-predict-trace.mjs --stdout   # print it instead
//
// hack/term-predict-trace.test.mjs (make js-test) fails when the committed
// trace no longer matches the JS engine: a change to web/term-predict.js
// regenerates the trace and ports the change to Swift in the same commit.
//
// Only single-scalar BMP glyphs are used: the port indexes lines by
// character where JS indexes UTF-16 units, which agree exactly there.
import { writeFileSync } from 'node:fs';
import { Predictor } from '../web/term-predict.js';

export const TRACE_PATH = new URL('../native/ios/Packages/XbinTerm/Tests/XbinTermTests/Resources/term-predict-trace.json', import.meta.url).pathname;
const SEED = 0x7e57ab1e, SESSIONS = 80, STEPS = 40;

// mulberry32: a tiny seeded PRNG, so the trace is reproducible.
function prng(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

// The screen the Swift side rebuilds from the same operations.
export class Screen {
  constructor(rows, cols) { this.rows = rows; this.cols = cols; this.cursor = { row: 0, col: 0 }; this.g = Array.from({ length: rows }, () => new Array(cols).fill('')); this.wide = new Set(); }
  charAt(r, c) { return this.g[r]?.[c] ?? ''; }
  widthAt(r, c) { return this.wide.has(`${r}:${c}`) ? 2 : 1; }
  lineAt(r) { return (this.g[r] ?? []).map((c) => c || ' ').join(''); }
  put(r, c, ch) { if (this.g[r] && c >= 0 && c < this.g[r].length) this.g[r][c] = ch; }
  resize(rows, cols) {
    const g = Array.from({ length: rows }, (_, r) => Array.from({ length: cols }, (_, c) => this.g[r]?.[c] ?? ''));
    this.g = g; this.rows = rows; this.cols = cols; this.wide.clear();
    this.cursor = { row: Math.min(this.cursor.row, rows - 1), col: Math.min(this.cursor.col, cols) };
  }
  scroll() { this.g.shift(); this.g.push(new Array(this.cols).fill('')); }
  move(from, to) { this.g[to] = [...this.g[from]]; this.g[from] = new Array(this.cols).fill(''); }
}

// observe: everything the engine exposes, as one comparable line.
export function observe(p, fb) {
  const r = p.render(fb);
  const cells = r.cells.map((c) => `${c.row}:${c.col}:${c.text}${c.underline ? '_' : ''}`).join('|');
  const cur = r.cursor ? `${r.cursor.row},${r.cursor.col}` : '-';
  const anc = p.anchor ? `${p.anchor.row},${p.anchor.col}` : '-';
  return `c=${cells};k=${cur};p=${p.pending()};a=${+p.active()};s=${+p.shown()};f=${+p.flagging};t=${+p.srttTrigger};g=${p.glitchTrigger};h=${+p.cursorHidden};n=${anc};l=${p.learn.length}`;
}

const GLYPHS = 'abcdefghijklmnopqrstuvwxyz0123456789 ->_.ł'; // single-scalar BMP, width 1
const KEYS = ['\x7f', '\x7f', '\r', '\x1b[C', '\x1b[D', '\x1bOC', '\x1bOD', '\x1b[A', '\x1b[3~', '\t', '\x03', '\n', '漢', '́', 'ab', 'xyz'];

// apply runs one operation on the engine and the screen; the Swift side
// implements exactly these (the op letters are the trace's vocabulary).
export function apply(op, p, fb, st) {
  switch (op.o) {
    case 't': p.newUserData(op.s, fb, op.n); p.setLocalFrameSent(++st.seq); break; // typed; the frame is sent
    case 'a': p.setLateAck(op.i); p.cull(fb, op.n); break;                         // an ack arrived (and was parsed)
    case 'c': p.cull(fb, op.n); break;                                             // the screen changed
    case 'p': fb.put(op.r, op.c, op.s); break;                                     // the app wrote a cell
    case 'k': fb.cursor = { row: op.r, col: op.c }; break;                         // the app moved the cursor
    case 'S': fb.scroll(); break;                                                  // the screen scrolled one line
    case 'M': fb.move(op.r, op.c); break;                                          // a row moved (row r → row c)
    case 'z': fb.resize(op.r, op.c); break;                                        // the terminal was resized
    case 'w': fb.wide.add(`${op.r}:${op.c}`); break;                               // a cell holds a wide glyph
    case 'h': p.setCursorHidden(op.b); break;
    case 'r': p.setSrtt(op.n); break;
    case 'm': p.setMode(op.s); break;
    case 'x': p.reset(); break;
    default: throw new Error(`unknown op ${op.o}`);
  }
}

// A shell-ish application: what it does with typed input, as concrete ops.
// It echoes at `at` — the terminal cursor, or (a hidden-cursor TUI) its own
// input field, with the terminal cursor parked elsewhere.
function echoOps(s, at, fb, rnd, moveCursor) {
  const ops = [];
  let { row, col } = at;
  const lie = rnd() < 0.15; // sometimes the app prints something else
  for (const ch of s) {
    const cp = ch.codePointAt(0);
    if (ch === '\x1b') break; // escapes: the app ignores them here
    if (cp === 0x7f) { if (col > 0) { col--; ops.push({ o: 'p', r: row, c: col, s: ' ' }); } continue; }
    if (cp === 0x0d) { col = 0; if (row === fb.rows - 1) ops.push({ o: 'S' }); else row++; continue; }
    if (cp < 0x20) continue;
    if (col >= fb.cols) { col = 0; if (row === fb.rows - 1) ops.push({ o: 'S' }); else row++; }
    ops.push({ o: 'p', r: row, c: col, s: lie ? 'Q' : ch });
    col++;
  }
  at.row = row; at.col = Math.min(col, fb.cols);
  if (moveCursor) ops.push({ o: 'k', r: at.row, c: at.col });
  return ops;
}

export function generate() {
  const rnd = prng(SEED);
  const int = (a, b) => a + Math.floor(rnd() * (b - a + 1));
  const pick = (xs) => xs[Math.floor(rnd() * xs.length)];
  const sessions = [];
  for (let si = 0; si < SESSIONS; si++) {
    const rows = int(1, 7), cols = int(2, 14);
    const fb = new Screen(rows, cols), p = new Predictor(), st = { seq: 0 };
    // prior content: [row, col, glyph] cells
    const cells = [];
    for (let i = int(0, rows * cols / 2); i > 0; i--) {
      const c = [int(0, rows - 1), int(0, cols - 1), pick(rnd() < 0.5 ? [...'ab '] : [...GLYPHS])];
      fb.put(...c); cells.push(c);
    }
    const init = [{ o: 'k', r: int(0, rows - 1), c: int(0, cols) }, { o: 'm', s: pick(['on', 'on', 'on', 'auto', 'auto', 'off']) }, { o: 'r', n: pick([null, 20, 90, 130, 200]) }];
    if (rnd() < 0.5) init.push({ o: 'h', b: true });
    const field = { row: int(0, rows - 1), col: int(0, cols - 1) }; // a hidden-cursor app's input field
    let now = int(0, 1000);
    const steps = [];
    const unanswered = []; // typed input not yet echoed: [seq, text]
    // a step records what the engine reports after it, or null when that is
    // unchanged from the step before (the trace stays small and diffable)
    let last = null;
    const run = (op) => { apply(op, p, fb, st); const o = observe(p, fb); steps.push([op, o === last ? null : o]); last = o; };
    for (const op of init) run(op);
    for (let k = 0; k < STEPS; k++) {
      const x = rnd();
      now += pick([0, 5, 20, 60, 160, 300, 1200]);
      if (x < 0.42) {
        // a keystroke, or a burst typed faster than the echo (a small alphabet:
        // retyping what a cell already shows is its own rule)
        for (let b = rnd() < 0.3 ? int(2, 5) : 1; b > 0; b--) {
          const y = rnd();
          const s = y < 0.4 ? pick([...'ab ']) : y < 0.7 ? pick([...GLYPHS]) : y < 0.8 ? '\x7f' : pick(KEYS);
          run({ o: 't', s, n: now });
          unanswered.push([st.seq, s]);
          now += pick([0, 5, 30]);
        }
      } else if (x < 0.64 && unanswered.length) {
        // the app answers the oldest input, then the ack arrives (usually)
        const [n, s] = unanswered.shift();
        const hidden = p.cursorHidden;
        const at = hidden ? field : { ...fb.cursor };
        for (const op of echoOps(s, at, fb, rnd, !hidden)) run(op);
        run({ o: 'c', n: now });
        if (rnd() < 0.8) run({ o: 'a', i: n, n: now + pick([0, 10, 60]) });
      } else if (x < 0.70) {
        run({ o: 'a', i: int(0, st.seq), n: now }); // an ack for something unanswered: those cells are wrong
      } else if (x < 0.74) {
        run({ o: 'c', n: now + pick([250, 5000, 6000]) }); // a long wait: glitches, the learn TTL
      } else if (x < 0.77) {
        run({ o: 'h', b: rnd() < 0.6 });
      } else if (x < 0.80) {
        run({ o: 'r', n: pick([null, 10, 55, 70, 110, 150, 170, 400]) });
      } else if (x < 0.82) {
        run({ o: 'm', s: pick(['on', 'auto', 'off']) });
      } else if (x < 0.84) {
        run({ o: 'z', r: int(1, 7), c: int(2, 14) }); run({ o: 'c', n: now });
        field.row = Math.min(field.row, fb.rows - 1); field.col = Math.min(field.col, fb.cols - 1);
      } else if (x < 0.86) {
        run({ o: 'w', r: int(0, fb.rows - 1), c: int(0, fb.cols - 1) });
      } else if (x < 0.89 && fb.rows > 1) {
        const r = field.row > 0 ? field.row : int(1, fb.rows - 1); // the field moved up a row (its frame grew)
        run({ o: 'M', r, c: r - 1 }); run({ o: 'c', n: now });
        if (field.row === r) field.row--;
      } else if (x < 0.91) {
        run({ o: 'x' });
      } else if (x < 0.95) {
        run({ o: 'p', r: int(0, fb.rows - 1), c: int(0, fb.cols - 1), s: pick([...GLYPHS]) }); run({ o: 'c', n: now });
      } else {
        run({ o: 'k', r: int(0, fb.rows - 1), c: int(0, fb.cols) }); run({ o: 'c', n: now });
      }
    }
    sessions.push({ rows, cols, cells, steps });
  }
  return { v: 1, source: 'web/term-predict.js', generator: 'hack/term-predict-trace.mjs', seed: SEED, sessions };
}

// On disk an operation is an array: [letter, ...arguments], booleans as 0/1.
const ARGS = { t: ['s', 'n'], a: ['i', 'n'], c: ['n'], p: ['r', 'c', 's'], k: ['r', 'c'], S: [], M: ['r', 'c'], z: ['r', 'c'], w: ['r', 'c'], h: ['b'], r: ['n'], m: ['s'], x: [] };
const compact = (op) => [op.o, ...ARGS[op.o].map((k) => (typeof op[k] === 'boolean' ? +op[k] : op[k]))];
export const expand = ([o, ...a]) => Object.fromEntries([['o', o], ...ARGS[o].map((k, i) => [k, k === 'b' ? !!a[i] : a[i]])]);

// serialize: one step per line, so a diff of the trace reads step by step.
export function serialize(t) {
  const out = [`{"v":${t.v},"source":${JSON.stringify(t.source)},"generator":${JSON.stringify(t.generator)},"seed":${t.seed},"sessions":[`];
  t.sessions.forEach((s, i) => {
    out.push(`{"rows":${s.rows},"cols":${s.cols},"cells":${JSON.stringify(s.cells)},"steps":[`);
    s.steps.forEach(([op, want], j) => out.push(`[${JSON.stringify(compact(op))},${JSON.stringify(want)}]${j < s.steps.length - 1 ? ',' : ''}`));
    out.push(`]}${i < t.sessions.length - 1 ? ',' : ''}`);
  });
  out.push(']}');
  return out.join('\n') + '\n';
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const text = serialize(generate());
  if (process.argv.includes('--stdout')) process.stdout.write(text);
  else { writeFileSync(TRACE_PATH, text); console.log(`wrote ${TRACE_PATH} (${text.length} bytes)`); }
}
