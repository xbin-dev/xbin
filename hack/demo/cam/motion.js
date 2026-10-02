// hack/demo/cam/motion.js — the camera's motion math: seeded randomness,
// human-like mouse paths and typing rhythm. Pure functions, no I/O: cam.js
// plans with them in Node, and the path functions' SOURCE travels into the
// page (overlay.js), so the drawn cursor and the dispatched mouse events
// follow the same curve. Keep pointAt/bezier/minJerk self-contained (no
// closures over this module): they are serialised with Function#toString.
'use strict';

// rng(seed): a deterministic PRNG (mulberry32) — the same seed gives the
// same paths and typing rhythm, so a retake moves like the take before it.
function rng(seed) {
  let a = 0x9e3779b9;
  for (const ch of String(seed ?? 'cam')) a = Math.imul(a ^ ch.charCodeAt(0), 0x85ebca6b) ^ (a >>> 13);
  const next = () => {
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
  next.range = (lo, hi) => lo + (hi - lo) * next();
  // a standard normal sample (Box–Muller)
  next.gauss = () => Math.sqrt(-2 * Math.log(1 - next())) * Math.cos(2 * Math.PI * next());
  return next;
}

// minJerk: the minimum-jerk position profile of a human reaching movement —
// zero velocity and acceleration at both ends, a bell-shaped speed between.
function minJerk(u) {
  const x = Math.min(1, Math.max(0, u));
  return x * x * x * (10 - 15 * x + 6 * x * x);
}

// bezier: the point at parameter s of a cubic segment {p0, c1, c2, p1}.
function bezier(seg, s) {
  const m = 1 - s;
  const a = m * m * m, b = 3 * m * m * s, c = 3 * m * s * s, d = s * s * s;
  return [
    a * seg.p0[0] + b * seg.c1[0] + c * seg.c2[0] + d * seg.p1[0],
    a * seg.p0[1] + b * seg.c1[1] + c * seg.c2[1] + d * seg.p1[1],
  ];
}

// pointAt(segs, ms): where a path of back-to-back segments {p0, c1, c2, p1,
// dur} is ms after it starts — each segment eased by the minimum-jerk
// profile. Past the end: the last point.
function pointAt(segs, ms) {
  let t = ms;
  for (const seg of segs) {
    if (t <= seg.dur) {
      const u = seg.dur > 0 ? t / seg.dur : 1;
      const x = Math.min(1, Math.max(0, u));
      const s = x * x * x * (10 - 15 * x + 6 * x * x);
      const m = 1 - s;
      const a = m * m * m, b = 3 * m * m * s, c = 3 * m * s * s, d = s * s * s;
      return [
        a * seg.p0[0] + b * seg.c1[0] + c * seg.c2[0] + d * seg.p1[0],
        a * seg.p0[1] + b * seg.c1[1] + c * seg.c2[1] + d * seg.p1[1],
      ];
    }
    t -= seg.dur;
  }
  const last = segs[segs.length - 1];
  return last ? [last.p1[0], last.p1[1]] : [0, 0];
}

const duration = (segs) => segs.reduce((n, s) => n + s.dur, 0);

// fittsMs: movement time by Fitts's law (MT = a + b·log2(D/W + 1)), slowed a
// little for the camera — a real hand is quicker than reads well on video:
// never under ~0.5 ms a px, so a long throw onto a wide target (Fitts's law
// makes that nearly free) still reads as a move — and clamped so it never
// drags. pace scales it (0.5 = brisk).
function fittsMs(dist, width, pace = 1) {
  const id = Math.log2(dist / Math.max(8, width) + 1);
  return Math.min(1150, Math.max(280, 210 + 165 * id, 160 + 0.5 * dist)) * pace;
}

// curve(p0, p1, bend): one segment, its control points ~a third and two
// thirds along the chord, pushed sideways by `bend` px (a hand's arc bows
// more early in the move than late).
function curve(p0, p1, bend) {
  const dx = p1[0] - p0[0], dy = p1[1] - p0[1];
  const len = Math.hypot(dx, dy) || 1;
  const nx = -dy / len, ny = dx / len;
  return {
    p0: [p0[0], p0[1]],
    c1: [p0[0] + dx * 0.3 + nx * bend, p0[1] + dy * 0.3 + ny * bend],
    c2: [p0[0] + dx * 0.72 + nx * bend * 0.55, p0[1] + dy * 0.72 + ny * bend * 0.55],
    p1: [p1[0], p1[1]],
  };
}

// plan(from, to, {r, width, pace}): the segments of one human-like move from
// point to point ([x, y], CSS px). A gentle arc of random side and depth, a
// Fitts's-law duration with some spread, and on a long throw the classic
// two-part reach: a fast primary movement that lands a little short and
// beside the target, then a short corrective one onto it.
function plan(from, to, { r = rng('plan'), width = 40, pace = 1 } = {}) {
  const dx = to[0] - from[0], dy = to[1] - from[1];
  const dist = Math.hypot(dx, dy);
  if (dist < 0.5) return [];
  const side = r() < 0.5 ? -1 : 1;
  const bend = side * dist * r.range(0.04, 0.13);
  const spread = r.range(0.9, 1.12);
  if (dist < 240 || pace < 0.2) {
    return [{ ...curve(from, to, bend), dur: Math.round(fittsMs(dist, width, pace) * spread) }];
  }
  // primary: lands short by 3–7 % of the distance, a few px off the line
  const short = r.range(0.03, 0.07), off = r.range(-1, 1) * Math.min(10, dist * 0.015);
  const ux = dx / dist, uy = dy / dist;
  const aim = [to[0] - ux * dist * short - uy * off, to[1] - uy * dist * short + ux * off];
  const main = { ...curve(from, aim, bend), dur: Math.round(fittsMs(dist, width, pace) * spread * 0.86) };
  const rest = Math.hypot(to[0] - aim[0], to[1] - aim[1]);
  const fix = { ...curve(aim, to, side * rest * 0.1), dur: Math.round(r.range(130, 210) * pace) };
  return [main, fix];
}

// aimPoint(box, r): where a person clicks inside a box — near the middle,
// not dead centre, never near the edge.
function aimPoint(box, r = rng('aim')) {
  const jx = Math.min(box.width * 0.18, 14), jy = Math.min(box.height * 0.16, 6);
  return [box.x + box.width / 2 + r.range(-jx, jx), box.y + box.height / 2 + r.range(-jy, jy)];
}

// typingPlan(text, {cps, r, jitter}): when each character goes down, in ms
// from the first. Log-normal spread around the beat, a beat longer after a
// word or a sentence and before Enter, quicker on a doubled letter — then
// normalised so the take averages exactly `cps` (the brief: ~10 chars/s).
function typingPlan(text, { cps = 10, r = rng('type'), jitter = 0.2 } = {}) {
  const chars = [...text];
  if (!chars.length) return [];
  const gaps = [0];
  for (let i = 1; i < chars.length; i++) {
    const ch = chars[i], prev = chars[i - 1];
    let k = Math.exp(r.gauss() * jitter);
    if (prev === ' ') k *= 1.18;
    if (/[.,;:!?]/.test(prev)) k *= 1.45;
    if (ch === '\n') k *= 2.2;
    if (ch === prev) k *= 0.8;
    gaps.push(Math.min(3, Math.max(0.45, k)));
  }
  const sum = gaps.reduce((a, b) => a + b, 0);
  const scale = sum > 0 ? (chars.length - 1) / sum : 0;
  let t = 0;
  return chars.map((ch, i) => {
    t += gaps[i] * scale * (1000 / cps);
    return { ch, at: Math.round(t) };
  });
}

module.exports = { rng, minJerk, bezier, pointAt, duration, fittsMs, curve, plan, aimPoint, typingPlan };
