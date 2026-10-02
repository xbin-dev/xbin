// hack/demo/cam/clock.js — the camera's clocks. Every wait a shot makes
// (cam.hold, the typing beat, each step of a mouse path) runs on one, and a
// mark's `t` is read from it, so `t` is always in the footage's own time.
//
//   RealClock   CLOCK_MONOTONIC (process.hrtime) — the capture runs in real
//               time: still, scratch, x11, screencast.
//   FrameClock  virtual time advanced by the frame loop, one frame interval
//               per captured frame (beginframe): however long a frame takes
//               to capture, the footage plays at exactly the shot's pace.
'use strict';

const monoMs = () => Number(process.hrtime.bigint()) / 1e6;

class RealClock {
  constructor() { this.kind = 'monotonic'; }
  now() { return monoMs(); }
  sleep(ms) { return new Promise((r) => setTimeout(r, Math.max(0, ms))); }
  sleepUntil(t) { return this.sleep(t - this.now()); }
  // about one display frame: the step of a dispatched mouse path
  tick() { return this.sleep(1000 / 60); }
}

class FrameClock {
  constructor(fps = 60) {
    this.kind = 'frames';
    this.frameMs = 1000 / fps;
    this.t = 0;
    this.waiters = []; // {t, resolve}, sorted by t
  }
  now() { return this.t; }
  sleep(ms) { return this.sleepUntil(this.t + Math.max(0, ms)); }
  sleepUntil(t) {
    if (t <= this.t + 1e-6) return Promise.resolve();
    return new Promise((resolve) => {
      const i = this.waiters.findIndex((w) => w.t > t);
      this.waiters.splice(i < 0 ? this.waiters.length : i, 0, { t, resolve });
    });
  }
  // the next frame
  tick() { return this.sleepUntil(this.t + this.frameMs * 0.5); }
  // advance(to): the frame loop moved time on; wake every due sleeper
  advance(to) {
    this.t = Math.max(this.t, to);
    while (this.waiters.length && this.waiters[0].t <= this.t + 1e-6) this.waiters.shift().resolve();
  }
}

module.exports = { RealClock, FrameClock, monoMs };
