// hack/demo/cam/beginframe.js — the deterministic capture: headless Chromium
// (Playwright's chrome-headless-shell) under --enable-begin-frame-control,
// where nothing renders until the camera sends HeadlessExperimental.beginFrame
// with a frame time of its choosing. The camera owns the clock:
//
//   - before the roll, frames are pumped in real time (the page, rAF and
//     Playwright's actionability waits all move as usual);
//   - at the roll the page goes onto virtual time (Emulation.
//     setVirtualTimePolicy), and frame k is rendered at exactly k/fps: the
//     page's clock — performance.now, timers, CSS and Web Animations — gets
//     one frame interval of budget per frame, and the shot's own waits
//     (holds, the typing beat, mouse paths: a FrameClock) run on the same
//     count. The footage is exactly 60 fps however long a frame takes to
//     capture. (Frame times alone are not enough: Blink's animation clock
//     reads the real clock in script tasks, so an animation a script starts
//     would sit in its first keyframe until real time came back round.)
//   - virtual time never runs ahead of real time (a quiet stretch catches
//     up, never overtakes), so what the page waits on from outside — a
//     terminal's echo, a server's answer — never looks slower than it was.
//     It can look FASTER: while frames are slow to capture, virtual time
//     falls behind and outside events land early (README: "beginframe").
//
// While the page is still, each frame is probed without a screenshot: no
// damage → the previous image is held (~1 ms a frame); damage → a second
// BeginFrame a hair later takes the screenshot, and the loop shoots every
// frame outright until a screenshot comes back byte-identical to the last
// (a screenshot always reports damage, so that is how stillness shows). The
// page's rAF thus runs once per video frame, twice only on the frame where a
// still stretch ends. Unique images go to a scratch dir; the cut lays them
// out as a 60 fps sequence and encodes it (rec.js sequence/encodeSequence).
'use strict';
const fs = require('fs');
const path = require('path');
const { Backend, persistentShim, profile, sequence, encodeSequence, probe } = require('./rec');
const { FrameClock, monoMs } = require('./clock');

const FLAGS = ['--enable-begin-frame-control', '--run-all-compositor-stages-before-draw', '--disable-new-content-rendering-timeout',
  '--disable-threaded-animation', '--disable-threaded-scrolling', '--disable-checker-imaging', '--disable-image-animation-resync'];

class BeginFrameBackend extends Backend {
  constructor(o) {
    super(o);
    this.capture = 'beginframe';
    this.clock = new FrameClock(o.fps);
    this.interval = 1000 / o.fps;
    this.fmt = o.frames === 'jpeg' ? 'jpeg' : 'png';
    this.stats = { frames: 0, shots: 0, same: 0, taken: 0, shotMs: 0, slowest: 0 };
    this.gate = null;
    this.vBase = null; // the page's virtual time base, once rolling
  }
  async launch() {
    const o = this.o;
    this.udd = profile(o.tmp);
    // a persistent context: its first page is the one target created with
    // begin-frame control (Playwright's own newPage targets are not)
    this.ctx = await o.pw.chromium.launchPersistentContext(this.udd, {
      headless: true, viewport: null, ignoreDefaultArgs: ['--hide-scrollbars'], args: [...this.launchArgs(), ...FLAGS],
    });
    this.page = this.ctx.pages()[0] || await this.ctx.newPage();
    this.cdp = await this.ctx.newCDPSession(this.page);
    this.ft = monoMs();
    const r = await this.begin(false).catch((e) => e);
    if (r instanceof Error) throw new Error(`HeadlessExperimental.beginFrame is not available in this Chromium: ${r.message}`);
    this.loopDone = this.loop();
  }
  // lib.js login() makes "a context" and "a page": here both are the one
  // frame-controlled page
  browserLike() { return persistentShim(this.ctx, this.page); }
  begin(shot) {
    const p = { frameTimeTicks: this.ft, interval: this.interval };
    if (shot) p.screenshot = { format: shot === true ? this.fmt : shot, quality: this.o.jpegQuality || 92, optimizeForSpeed: true };
    return this.cdp.send('HeadlessExperimental.beginFrame', p);
  }
  // budget(ms): let the page's virtual clock run ms further, and wait until it has
  async budget(ms) {
    const done = new Promise((r) => this.cdp.once('Emulation.virtualTimeBudgetExpired', r));
    await this.cdp.send('Emulation.setVirtualTimePolicy', { policy: 'advance', budget: ms });
    await done;
  }
  async shoot() {
    const s = monoMs();
    const shot = await this.begin(true).catch((e) => ({ error: e }));
    const ms = monoMs() - s;
    this.stats.taken++;
    this.stats.shotMs += ms;
    this.stats.slowest = Math.max(this.stats.slowest, ms);
    return shot;
  }
  // pause(): resolves once the loop is parked between frames; resume() lets it go
  pause() {
    if (!this.gate) {
      const g = {};
      g.parked = new Promise((r) => { g.onParked = r; });
      g.open = new Promise((r) => { g.release = r; });
      this.gate = g;
    }
    return this.gate.parked;
  }
  resume() { const g = this.gate; this.gate = null; g?.release(); }
  async loop() {
    let lastReal = monoMs();
    while (!this.closed) {
      if (this.gate) { this.gate.onParked(); await this.gate.open; lastReal = monoMs(); continue; }
      if (!this.rolling) {
        // real time: keep the page alive, no capture
        const now = monoMs();
        const dt = now - lastReal;
        this.clock.advance(this.clock.now() + dt);
        lastReal = now;
        try {
          if (this.vBase != null) { await this.budget(Math.min(100, Math.max(1, dt))); this.ft += Math.min(100, Math.max(1, dt)); } else this.ft = Math.max(this.ft + 0.01, now);
          await this.begin(false);
        } catch (e) { if (!this.closed) this.o.log(`beginFrame (idle): ${e.message}`); }
        await new Promise((r) => setTimeout(r, this.interval));
        continue;
      }
      // frame k at virtual time k/fps, never ahead of the real clock
      const k = this.frameNo;
      const ahead = k * this.interval - (monoMs() - this.realRoll);
      if (ahead > 0) await new Promise((r) => setTimeout(r, ahead));
      if (k > 0) {
        try { await this.budget(this.interval); } catch (e) { if (this.closed) break; this.o.log(`virtual time: ${e.message}`); await new Promise((r) => setTimeout(r, 50)); continue; }
      }
      this.ft = this.vBase + k * this.interval;
      this.clock.advance(this.vRoll + k * this.interval);
      // the shot's actions that just woke dispatch their input first
      await new Promise((r) => setImmediate(r));
      await new Promise((r) => setImmediate(r));
      // shooting: the page was changing, so screenshot this frame outright
      // (one BeginFrame: the page's rAF runs once per video frame); probing:
      // it was still, so ask for a frame without one first and screenshot
      // only on damage (the second BeginFrame repeats rAF once, on the frame
      // where a still stretch ends)
      let shot = null;
      if (this.shooting) shot = await this.shoot();
      else {
        let res;
        try { res = await this.begin(false); } catch (e) { if (!this.closed) this.o.log(`beginFrame: ${e.message}`); continue; }
        if (res.hasDamage || !this.lastFile) { this.ft += 0.01; shot = await this.shoot(); }
      }
      if (shot && shot.screenshotData) {
        if (shot.screenshotData === this.lastData) { this.stats.same++; this.shooting = false; } else {
          this.lastData = shot.screenshotData;
          this.stats.shots++;
          const file = path.join(this.dir, `u${String(this.stats.shots).padStart(6, '0')}.${this.fmt === 'jpeg' ? 'jpg' : 'png'}`);
          fs.writeFileSync(file, Buffer.from(shot.screenshotData, 'base64'));
          this.lastFile = file;
          this.list.push({ file, t: k * this.interval });
          this.shooting = true;
        }
      } else if (shot && shot.error) this.o.log(`beginFrame screenshot: ${shot.error.message}`);
      this.frameNo = k + 1;
      this.stats.frames++;
    }
  }
  async roll() {
    await this.pause();
    this.dir = path.join(this.o.tmp, 'frames');
    fs.mkdirSync(this.dir, { recursive: true });
    this.list = [];
    this.lastFile = null;
    this.lastData = null;
    this.shooting = false;
    this.frameNo = 0;
    // the page's clock stops here and moves one frame at a time from now on
    const r = await this.cdp.send('Emulation.setVirtualTimePolicy', { policy: 'pause' });
    this.vBase = Math.max(r.virtualTimeTicksBase, this.ft + 0.02);
    this.vRoll = this.clock.now();
    this.realRoll = monoMs();
    this.rolling = true;
    this.resume();
  }
  async cut() {
    if (!this.rolling) return null;
    await this.pause();
    this.rolling = false;
    const end = this.frameNo * this.interval;
    const realMs = monoMs() - this.realRoll;
    this.resume();
    if (!this.list.length) throw new Error('beginframe: no frames captured');
    this.list[0] = { ...this.list[0], t: 0 }; // shown from the roll
    const mp4 = path.join(this.o.out, `${this.o.take}.mp4`);
    const t0 = monoMs();
    await encodeSequence(sequence(this.list, path.join(this.dir, 'seq'), this.frameNo, this.o.fps), mp4, this.o);
    const s = this.stats;
    return {
      file: path.basename(mp4), fps: this.o.fps, firstFrameAt: this.vRoll, durationMs: Math.round(end), frames: this.frameNo, unique: s.shots, sameAsLast: s.same,
      frameFormat: this.fmt, captureRealMs: Math.round(realMs), speed: +(end / realMs).toFixed(3),
      screenshots: s.taken, avgShotMs: s.taken ? +(s.shotMs / s.taken).toFixed(1) : 0, slowestShotMs: Math.round(s.slowest), encodeMs: Math.round(monoMs() - t0),
      probe: await probe(mp4),
    };
  }
  // a still that never shows in the footage: an extra frame between two
  // video frames, taken with the loop parked. before() runs first (cam.js
  // hides the cursor there), after() once it is taken.
  async still(page, file, { before, after } = {}) {
    await this.pause();
    try {
      if (before) await before();
      this.ft += 0.005;
      const r = await this.begin('png');
      if (!r.screenshotData) throw new Error('beginframe still: no screenshot data');
      fs.writeFileSync(file, Buffer.from(r.screenshotData, 'base64'));
      if (after) await after();
    } finally {
      this.resume();
    }
  }
  async close() {
    this.closed = true;
    this.resume();
    await this.loopDone?.catch(() => {});
    await this.ctx?.close().catch(() => {});
    if (this.udd) fs.rmSync(this.udd, { recursive: true, force: true });
  }
}

module.exports = { BeginFrameBackend };
