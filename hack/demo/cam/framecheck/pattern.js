// hack/demo/cam/framecheck/pattern.js — the frame-counter pattern, shared by
// the page that draws it (index.html, as a classic script) and the checker
// and generator that read and write it (Node, CommonJS).
//
// The top 80 % of the frame is an 8×4 grid of large blocks, row-major, white
// = 1, black = 0:
//   blocks  0–19  the counter, least significant bit first (20 bits)
//   blocks 20–27  CRC-8 (poly 0x07) of the counter's three bytes
//   blocks 28–31  1 0 1 0, always — finds the pattern and sets the threshold
// The bottom 20 % is free for a human-readable line. Each block is read at
// its centre, so it survives scaling, chroma subsampling, lossy encoding and
// a few pixels of window chrome; a frame captured mid-update (torn) fails the
// CRC instead of decoding to a wrong number.
(function (root) {
  const COLS = 8, ROWS = 4, AREA = 0.8, COUNTER_BITS = 20, CRC_BITS = 8;
  const SYNC = [1, 0, 1, 0];

  function crc8(n) {
    let c = 0;
    for (const byte of [n & 0xff, (n >>> 8) & 0xff, (n >>> 16) & 0xff]) {
      c ^= byte;
      for (let i = 0; i < 8; i++) c = c & 0x80 ? ((c << 1) ^ 0x07) & 0xff : (c << 1) & 0xff;
    }
    return c;
  }

  // bits(n): the 32 block values for counter n
  function bits(n) {
    const v = n % (1 << COUNTER_BITS);
    const out = [];
    for (let i = 0; i < COUNTER_BITS; i++) out.push((v >>> i) & 1);
    const c = crc8(v);
    for (let i = 0; i < CRC_BITS; i++) out.push((c >>> i) & 1);
    return out.concat(SYNC);
  }

  // decode(levels): block brightnesses (0–255, 32 of them) → {status, value}
  // status: ok | blank (no pattern: the sync blocks don't read 1 0 1 0) |
  // torn (the CRC fails: a frame caught between two updates)
  function decode(levels) {
    const hi = (levels[28] + levels[30]) / 2, lo = (levels[29] + levels[31]) / 2;
    if (hi - lo < 64) return { status: 'blank' };
    const th = (hi + lo) / 2;
    const b = levels.map((l) => (l > th ? 1 : 0));
    let v = 0;
    for (let i = 0; i < COUNTER_BITS; i++) v |= b[i] << i;
    let c = 0;
    for (let i = 0; i < CRC_BITS; i++) c |= b[COUNTER_BITS + i] << i;
    if (c !== crc8(v)) return { status: 'torn' };
    return { status: 'ok', value: v };
  }

  // cell(i): block i's rectangle as fractions of the frame [x, y, w, h]
  function cell(i) {
    const c = i % COLS, r = Math.floor(i / COLS);
    return [c / COLS, (AREA * r) / ROWS, 1 / COLS, AREA / ROWS];
  }

  const P = { COLS, ROWS, AREA, COUNTER_BITS, crc8, bits, decode, cell, BLOCKS: COLS * ROWS };
  if (typeof module !== 'undefined' && module.exports) module.exports = P; else root.FramePattern = P;
})(typeof self !== 'undefined' ? self : this);
