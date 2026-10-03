// hack/demo/cam/cam.js — the camera: a scripted person at the keyboard, for
// promo footage and website stills of the xbin UI. A shot module gets a Cam
// and drives the page like a person would — a visible cursor gliding on
// human-like paths, clicks with a ripple, typing at ~10 characters a second
// — while every beat it cares about goes into a JSON sidecar (cam.mark), so
// the edit derives zooms and callouts from the log and a retake is a rerun.
//
// Positions are CSS px in the viewport (the page is width×height CSS px at
// dpr device px each). Times are ms since the shot started, on the capture's
// own clock (clock.js): wall-monotonic in real-time captures, the frame
// clock in beginframe — either way a sidecar time is a video time once the
// video's startT is subtracted.
//
// Targets (moveTo, click, mark, …) are any of: a selector string (Playwright
// syntax — pierces open shadow roots), a Locator (any frame: cam.in(tile)
// gives a tile's document), a box {x, y, width, height}, or a point [x, y].
// The UI harness's helpers (hack/ui-harness/lib.js) drive the shell's
// testApi(): cam.sh, cam.fr, cam.waitFor, cam.openShell, cam.openTile.
'use strict';
const fs = require('fs');
const path = require('path');
const motion = require('./motion');
const overlay = require('./overlay');

class Cam {
  // o: {backend, lib, out, take, shot, mode, width, height, dpr, fps, seed,
  //     pace, user, pass, url, args, cursorScale, log}
  constructor(o) {
    this.o = o;
    this.backend = o.backend;
    this.clock = o.backend.clock;
    this.lib = o.lib;
    this.args = o.args || {};
    this.page = null;
    this.r = motion.rng(`${o.shot}:${o.seed ?? 1}`);
    this.pos = null; // the cursor (CSS px), null until placed
    this.fast = o.pace === 'fast';
    this.t0 = this.clock.now();
    this.overlaySrc = overlay.source({ scale: o.cursorScale || 1 });
    this.log = o.log || ((...a) => console.log('[cam]', ...a));
    this.sidecarFile = path.join(o.out, `${o.take}.json`);
    this.side = {
      shot: o.shot, take: o.take, mode: o.mode, capture: o.backend.capture, url: o.url,
      viewport: { width: o.width, height: o.height }, dpr: o.dpr, fps: o.fps, seed: o.seed ?? 1, pace: o.pace,
      clock: { kind: this.clock.kind, startedAt: new Date().toISOString(),
        note: 't = ms since the shot started, on the capture clock; video time = t - video.startT' },
      args: this.args, roll: null, cut: null, video: null, marks: [], events: [], stills: [],
    };
  }

  // ---- time --------------------------------------------------------------
  t() { return Math.round((this.clock.now() - this.t0) * 10) / 10; }
  sleep(ms) { return this.clock.sleep(ms); }
  // hold: a beat the audience needs (to read, to notice) — skipped at fast pace
  hold(ms) { return this.fast ? Promise.resolve() : this.clock.sleep(ms); }
  rand(lo, hi) { return this.r.range(lo, hi); }

  // ---- pages -------------------------------------------------------------
  // login: lib.js login() on this capture's browser and context options
  async login(user = this.o.user, pass = this.o.pass) {
    const { page } = await this.lib.login(this.backend.browserLike(), user, pass, this.backend.ctxOpts());
    await this.adopt(page);
    return page;
  }
  // open(url): a page without logging in (a file:// page, a public site)
  async open(url) {
    const ctx = await this.backend.browserLike().newContext(this.backend.ctxOpts());
    const page = await ctx.newPage();
    await this.adopt(page);
    await page.goto(url);
    return page;
  }
  async adopt(page) {
    this.page = page;
    // lib.js logs every console error; a film log only wants the real ones
    page.removeAllListeners('console');
    page.on('console', (m) => { if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) this.log('page console.error:', m.text().slice(0, 200)); });
    await page.context().addInitScript({ content: this.overlaySrc });
    await page.evaluate(this.overlaySrc).catch(() => {});
    await this.syncCursor();
  }
  async goto(url) { await this.page.goto(url.startsWith('/') ? this.o.url + url : url); await this.syncCursor(); }

  // the harness helpers, bound to this page
  sh(fn, arg) { return this.lib.sh(this.page, fn, arg); }
  fr(tile, fn, arg) { return this.lib.fr(this.page, tile, fn, arg); }
  waitFor(fn, arg, opts) { return this.lib.waitFor(this.page, fn, arg, opts); }
  waitSel(sel, opts) { return this.lib.waitSel(this.page, sel, opts); }
  settle() { return this.lib.settle(this.page); }
  openShell() { return this.lib.openShell(this.page).then(() => this.syncCursor()); }
  usePersonalScreen() { return this.lib.usePersonalScreen(this.page); }
  openTile(p) { return this.lib.openTile(this.page, p); }
  // in(tile): the Playwright Frame of a mounted tile's document — locators in
  // it are valid targets (their boxes are in the page's viewport)
  in(tile, opts) { return this.lib.tileFrame(this.page, tile, opts); }
  locator(sel) { return this.page.locator(sel).first(); }

  // ---- targets -----------------------------------------------------------
  async box(target, { timeout = 15000 } = {}) {
    if (!target) return null;
    if (typeof target === 'function') return this.box(await target(this), { timeout });
    if (Array.isArray(target)) return { x: target[0], y: target[1], width: 0, height: 0 };
    if (typeof target.x === 'number' && typeof target.width === 'number') return target;
    const loc = typeof target === 'string' ? this.locator(target) : target;
    await loc.waitFor({ state: 'visible', timeout });
    const b = await loc.boundingBox();
    if (!b) throw new Error(`no box for ${target}`);
    return b;
  }
  // reveal(target): scroll it into view the way a person would (smoothly),
  // and wait until it stops moving
  async reveal(target, { block = 'nearest' } = {}) {
    if (typeof target !== 'string' && !(target && target.evaluate)) return;
    const loc = typeof target === 'string' ? this.locator(target) : target;
    let b = await this.box(loc);
    const vw = this.o.width, vh = this.o.height;
    if (b.y >= 0 && b.x >= 0 && b.y + b.height <= vh && b.x + b.width <= vw) return;
    await loc.evaluate((el, block) => el.scrollIntoView({ block, inline: 'nearest', behavior: 'smooth' }), block);
    for (let still = 0, i = 0; still < 3 && i < 200; i++) {
      await this.clock.sleep(50);
      const n = await loc.boundingBox();
      still = n && Math.abs(n.y - b.y) < 0.5 && Math.abs(n.x - b.x) < 0.5 ? still + 1 : 0;
      b = n || b;
    }
  }

  // ---- the cursor --------------------------------------------------------
  async syncCursor() {
    if (!this.page) return;
    await this.page.evaluate(([src, pos]) => {
      if (!window.__cam) (0, eval)(src);
      if (pos) window.__cam.set(pos[0], pos[1]);
    }, [this.overlaySrc, this.pos]).catch(() => {});
  }
  // cursorAt(target | [x, y]): put the cursor there without a move (set dressing)
  async cursorAt(target) {
    const b = await this.box(target);
    const p = Array.isArray(target) ? target : motion.aimPoint(b, this.r);
    this.pos = p;
    await this.page.mouse.move(p[0], p[1]);
    await this.syncCursor();
  }
  async showCursor(v = true) { await this.page.evaluate((v) => window.__cam?.show(v), v).catch(() => {}); }

  // moveTo(target, {pace, point, reveal}): glide there on a human path;
  // resolves on arrival with the point. pace scales the time (0.5 = brisk),
  // point(box) picks the spot (default: near the middle), reveal: false
  // skips scrolling it into view first. A cursor not yet on screen enters
  // from the right edge.
  async moveTo(target, opts = {}) {
    if (opts.reveal !== false) await this.reveal(target);
    const b = await this.box(target);
    const to = Array.isArray(target) ? target : (opts.point ? opts.point(b) : motion.aimPoint(b, this.r));
    const from = this.pos || [this.o.width + 40, this.o.height * this.rand(0.55, 0.8)];
    const pace = opts.pace ?? 1;
    this.lastBox = b;
    if (this.fast) {
      this.pos = to;
      await this.page.mouse.move(to[0], to[1]);
      await this.syncCursor();
      return to;
    }
    const segs = motion.plan(from, to, { r: this.r, width: Math.max(b.width, 12), pace });
    const hint = await this.cursorHint(target, b);
    const t0 = this.t();
    if (segs.length) {
      await this.page.evaluate(([src, segs, hint]) => { if (!window.__cam) (0, eval)(src); return window.__cam.play(segs, hint); }, [this.overlaySrc, segs, hint]);
      const dur = motion.duration(segs);
      const start = this.clock.now();
      for (;;) {
        const el = this.clock.now() - start;
        const p = motion.pointAt(segs, Math.min(el, dur));
        await this.page.mouse.move(p[0], p[1]);
        if (el >= dur) break;
        await this.clock.tick();
      }
    }
    this.pos = to;
    this.event('move', { from: from.map(Math.round), to: to.map(Math.round), t0, ms: Math.round(this.t() - t0) });
    return to;
  }
  // cursorHint: the cursor the target itself asks for — the overlay can't see
  // into a sandboxed tile's frame, so a move into one takes this for its box
  async cursorHint(target, box) {
    if (!target || Array.isArray(target) || typeof target === 'function' || typeof target.x === 'number') return null;
    const loc = typeof target === 'string' ? this.locator(target) : target;
    const kind = await loc.evaluate((el) => {
      const c = getComputedStyle(el).cursor;
      if (c === 'pointer') return 'pointer';
      if (c === 'text' || (c === 'auto' && (el.isContentEditable || /^(INPUT|TEXTAREA)$/.test(el.tagName)))) return 'text';
      return 'default';
    }).catch(() => 'default');
    return { box, kind };
  }
  async hover(target, opts) { const p = await this.moveTo(target, opts); await this.hold(this.rand(250, 450)); return p; }

  // click(target, {button, clicks}): arrive, settle, press (cursor dips, a
  // ripple), release. Logged as an event with the target's box.
  async click(target, { button = 'left', clicks = 1, ...opts } = {}) {
    const p = await this.moveTo(target, opts);
    const b = this.lastBox;
    if (!this.fast) await this.sleep(this.rand(90, 190));
    const t = this.t();
    for (let i = 0; i < clicks; i++) {
      await this.page.evaluate(([x, y]) => window.__cam?.down(x, y), p).catch(() => {});
      await this.page.mouse.down({ button, clickCount: i + 1 });
      await this.sleep(this.fast ? 0 : this.rand(55, 105));
      await this.page.mouse.up({ button, clickCount: i + 1 });
      await this.page.evaluate(() => window.__cam?.up()).catch(() => {});
      if (i + 1 < clicks) await this.sleep(this.fast ? 0 : this.rand(70, 110));
    }
    this.event('click', { t, at: p.map((v) => Math.round(v * 10) / 10), button, clicks, box: round(b) });
    return p;
  }
  dblclick(target, opts) { return this.click(target, { ...opts, clicks: 2 }); }
  rightClick(target, opts) { return this.click(target, { ...opts, button: 'right' }); }

  // drag(from, to): press on one target, glide to the other, let go
  async drag(from, to, opts = {}) {
    const p = await this.moveTo(from, opts);
    await this.sleep(this.fast ? 0 : this.rand(120, 220));
    const t = this.t();
    await this.page.evaluate(([x, y]) => window.__cam?.down(x, y), p).catch(() => {});
    await this.page.mouse.down();
    await this.sleep(this.fast ? 0 : this.rand(80, 140));
    const q = await this.moveTo(to, { ...opts, reveal: false, pace: (opts.pace ?? 1) * 1.25 });
    await this.sleep(this.fast ? 0 : this.rand(80, 140));
    await this.page.mouse.up();
    await this.page.evaluate(() => window.__cam?.up()).catch(() => {});
    this.event('drag', { t, from: p.map(Math.round), to: q.map(Math.round) });
  }

  // scroll(dy, {at, ms}): a wheel gesture over `at` (default: where the
  // cursor is), eased like a trackpad flick
  async scroll(dy, { at = null, ms = 600, dx = 0 } = {}) {
    if (at) await this.moveTo(at);
    const t = this.t();
    if (this.fast) { await this.page.mouse.wheel(dx, dy); } else {
      const start = this.clock.now();
      let done = 0;
      for (;;) {
        const u = Math.min(1, (this.clock.now() - start) / ms);
        const want = motion.minJerk(u);
        const step = want - done;
        if (step > 0) await this.page.mouse.wheel(dx * step, dy * step);
        done = want;
        if (u >= 1) break;
        await this.clock.tick();
      }
    }
    this.event('scroll', { t, dx, dy, ms });
  }

  // ---- the keyboard ------------------------------------------------------
  // type(text, {cps}): at ~10 characters a second with a human rhythm
  // (motion.typingPlan); "\n" presses Enter. Into whatever has focus.
  async type(text, { cps = 10 } = {}) {
    const t = this.t();
    if (this.fast) {
      for (const part of text.split(/(\n)/)) {
        if (part === '\n') await this.page.keyboard.press('Enter'); else if (part) await this.page.keyboard.type(part);
      }
    } else {
      const plan = motion.typingPlan(text, { cps, r: this.r });
      const start = this.clock.now();
      for (const k of plan) {
        await this.clock.sleepUntil(start + k.at);
        if (k.ch === '\n') await this.page.keyboard.press('Enter'); else await this.page.keyboard.type(k.ch);
      }
    }
    this.event('type', { t, ms: Math.round(this.t() - t), text });
  }
  async typeIn(target, text, opts) { await this.click(target, opts); await this.sleep(this.fast ? 0 : this.rand(150, 300)); await this.type(text, opts); }
  // press(key): one key or chord ("Enter", "Control+C"), after a short beat
  async press(key) {
    await this.sleep(this.fast ? 0 : this.rand(140, 260));
    const t = this.t();
    await this.page.keyboard.press(key);
    this.event('press', { t, key });
  }

  // ---- the log -----------------------------------------------------------
  // mark(name, target?, extra?): a beat for the edit — {name, t, box}; the box
  // is where target is on screen (CSS px), null for a time-only mark
  async mark(name, target = null, extra = {}) {
    const t = this.t();
    let box = null;
    if (target) {
      try { box = round(await this.box(target, { timeout: extra.timeout ?? 5000 })); } catch (e) {
        if (!extra.optional) throw new Error(`mark ${name}: ${e.message.split('\n')[0]}`);
      }
    }
    const { optional, timeout, ...rest } = extra;
    const m = { name, t, box, ...rest };
    this.side.marks.push(m);
    this.flush();
    return m;
  }
  event(kind, data) { this.side.events.push({ kind, ...data }); }

  // still(name, {cursor, caption}): a PNG of the viewport (width×height ×
  // dpr). The cursor is left out unless asked for — except while a
  // real-time capture is rolling, where hiding it would show in the
  // footage. caption: what the frame shows, kept in the sidecar (the
  // website stills' manifest reads it).
  async still(name, { cursor, caption } = {}) {
    const file = path.join(this.o.out, `${this.o.take}-${name}.png`);
    // a recording in real time would film the cursor vanishing; beginframe
    // takes its still between two video frames, so it never does
    const recording = !!this.side.roll && !this.side.cut && this.backend.capture !== 'none';
    const outOfBand = this.backend.capture === 'beginframe';
    const hide = !(cursor ?? (recording && !outOfBand));
    const t = this.t();
    if (outOfBand) {
      await this.backend.still(this.page, file, { before: hide ? () => this.showCursor(false) : null, after: hide ? () => this.showCursor(true) : null });
    } else {
      if (hide) await this.showCursor(false);
      try { await this.backend.still(this.page, file); } finally { if (hide) await this.showCursor(true); }
    }
    this.side.stills.push({ name, t, file: path.basename(file), cursor: !hide, ...(caption ? { caption } : {}) });
    this.flush();
    this.log('still', path.basename(file));
    return file;
  }

  flush() {
    const tmp = `${this.sidecarFile}.tmp`;
    fs.writeFileSync(tmp, JSON.stringify(this.side, null, 1) + '\n');
    fs.renameSync(tmp, this.sidecarFile);
  }
}

const round = (b) => b && { x: Math.round(b.x * 10) / 10, y: Math.round(b.y * 10) / 10, width: Math.round(b.width * 10) / 10, height: Math.round(b.height * 10) / 10 };

module.exports = { Cam };
