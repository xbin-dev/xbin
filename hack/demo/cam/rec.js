// hack/demo/cam/rec.js — capture backends: how the camera's browser is
// launched and how its frames reach a file. One per --mode/--capture:
//
//   still       headless, no video: cam.still() writes PNGs (size × dpr)
//   scratch     headless + Playwright recordVideo: a quick webm (25 fps,
//               VP8) to block a shot out — never a master
//   x11         the 4K master path: headful Chromium on an X display
//               (capture.sh starts Xvfb) recorded by ffmpeg x11grab at 60
//               fps into h264_nvenc
//   beginframe  headless with HeadlessExperimental.beginFrame: the camera
//               owns the frame clock, captures every frame (PNG or JPEG) and
//               plays the shot in virtual time — exact 60 fps however slow a
//               frame is to capture
//   screencast  headless with Page.startScreencast: the frames Chromium
//               offers, when it offers them (variable rate) → CFR 60
//
// Every backend renders the same way — headless or not, the page is
// width×height CSS px at a forced device scale factor (never Playwright's
// emulation, which the frame-level captures don't see), with scrollbars on
// (Playwright's headless default hides them; a real browser shows them).
'use strict';
const fs = require('fs');
const path = require('path');
const { spawn, spawnSync } = require('child_process');
const { RealClock, monoMs } = require('./clock');

// ---- ffmpeg ----------------------------------------------------------------

let nvenc;
function hasNvenc() {
  if (nvenc === undefined) {
    const r = spawnSync('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-f', 'lavfi', '-i', 'color=c=black:s=256x144:d=0.1',
      '-c:v', 'h264_nvenc', '-f', 'null', '-'], { encoding: 'utf8', timeout: 30000 });
    nvenc = r.status === 0;
  }
  return nvenc;
}

// encodeArgs: the video codec flags. lossless = H.264 4:4:4 lossless (the
// master: text edges keep their colour, zooms in the edit stay sharp); high
// = 4:2:0 at a low constant QP (plays everywhere). Decoded frames (PNG/JPEG)
// are converted by swscale with the BT.709 matrix and tagged BT.709 through
// and through (setparams: the -color_* options alone come out "unknown").
// rgb: packed RGB straight from x11grab, converted on the GPU — NVENC picks
// the matrix (BT.601) and tags the stream with it, so it decodes exact.
function encodeArgs({ codec = 'auto', quality = 'lossless', rgb = false } = {}) {
  const nv = codec === 'h264_nvenc' || (codec === 'auto' && hasNvenc());
  const lossless = quality === 'lossless';
  const sw = ['-vf', `scale=out_color_matrix=bt709:out_range=tv,format=${lossless ? 'yuv444p' : 'yuv420p'},` +
    'setparams=colorspace=bt709:color_primaries=bt709:color_trc=bt709:range=tv'];
  const conv = rgb && nv ? ['-rgb_mode', lossless ? 'yuv444' : 'yuv420'] : sw;
  if (nv) {
    return lossless
      ? ['-c:v', 'h264_nvenc', '-preset', 'p4', '-tune', 'lossless', '-profile:v', 'high444p', ...conv]
      : ['-c:v', 'h264_nvenc', '-preset', 'p5', '-tune', 'hq', '-rc', 'constqp', '-qp', '16', '-profile:v', 'high', ...conv];
  }
  return lossless
    ? ['-c:v', 'libx264', '-preset', 'ultrafast', '-qp', '0', ...sw]
    : ['-c:v', 'libx264', '-preset', 'medium', '-crf', '14', ...sw];
}

// run(args): ffmpeg/ffprobe to completion → {code, out, err}
function run(cmd, args, { timeout = 600000 } = {}) {
  return new Promise((resolve) => {
    const p = spawn(cmd, args, { stdio: ['ignore', 'pipe', 'pipe'] });
    let out = '', err = '';
    p.stdout.on('data', (d) => { out += d; });
    p.stderr.on('data', (d) => { err += d; if (err.length > 200000) err = err.slice(-100000); });
    const t = setTimeout(() => p.kill('SIGKILL'), timeout);
    p.on('close', (code) => { clearTimeout(t); resolve({ code, out, err }); });
  });
}

async function probe(file) {
  const r = await run('ffprobe', ['-v', 'error', '-select_streams', 'v:0', '-count_packets', '-show_entries',
    'stream=codec_name,profile,pix_fmt,width,height,avg_frame_rate,nb_read_packets:format=start_time,duration,size', '-of', 'json', file]);
  if (r.code !== 0) return null;
  const j = JSON.parse(r.out);
  const s = j.streams?.[0] || {};
  return { codec: s.codec_name, profile: s.profile, pixFmt: s.pix_fmt, width: s.width, height: s.height, fps: s.avg_frame_rate,
    frames: Number(s.nb_read_packets) || null, start: Number(j.format?.start_time), duration: Number(j.format?.duration), bytes: Number(j.format?.size) };
}

// sequence(frames, dir, n, fps): frames = [{file, t}] — unique images, t =
// ms from the roll, ascending — laid out as a constant-rate image sequence
// of n frames: frame k is a symlink to the last image shown by k/fps. A
// held image costs a link, not a copy. (Not the concat demuxer with per-
// image durations: it stamps images in a coarse timebase, and the 60 fps
// output came out with a duplicated frame every twelfth.)
function sequence(frames, dir, n, fps) {
  fs.mkdirSync(dir, { recursive: true });
  const ext = path.extname(frames[0].file);
  let j = 0;
  for (let k = 0; k < n; k++) {
    const at = (k * 1000) / fps + 1e-6;
    while (j + 1 < frames.length && frames[j + 1].t <= at) j++;
    fs.symlinkSync(frames[j].file, path.join(dir, `f${String(k).padStart(6, '0')}${ext}`));
  }
  return path.join(dir, `f%06d${ext}`);
}

// encodeSequence(pattern, out): an image sequence → the master
async function encodeSequence(pattern, outFile, { fps = 60, codec, quality }) {
  const args = ['-hide_banner', '-loglevel', 'error', '-y', '-framerate', String(fps), '-start_number', '0', '-i', pattern,
    ...encodeArgs({ codec, quality }), '-r', String(fps), '-movflags', '+faststart', outFile];
  const r = await run('ffmpeg', args);
  if (r.code !== 0) throw new Error(`ffmpeg encode failed: ${r.err.slice(-800)}`);
}

// ---- backends --------------------------------------------------------------

// profile(dir): a fresh user-data dir that never interrupts a take — no
// "Save password?" after the login (the bubble a headful browser shows), no
// permission prompts. Headless shows none of it; a headful window would film it.
function profile(tmp) {
  const dir = fs.mkdtempSync(path.join(tmp, 'udd-'));
  fs.mkdirSync(path.join(dir, 'Default'), { recursive: true });
  fs.writeFileSync(path.join(dir, 'Default', 'Preferences'), JSON.stringify({
    credentials_enable_service: false,
    profile: { password_manager_enabled: false, password_manager_leak_detection: false, default_content_setting_values: { notifications: 2, geolocation: 2 } },
    autofill: { profile_enabled: false, credit_card_enabled: false },
    translate: { enabled: false },
    browser: { has_seen_welcome_page: true },
  }));
  return dir;
}
// the switches that keep a headful window free of anything but the page
const QUIET = ['--no-first-run', '--no-default-browser-check', '--noerrdialogs', '--disable-infobars', '--disable-session-crashed-bubble',
  '--disable-search-engine-choice-screen', '--disable-component-update', '--deny-permission-prompts', '--password-store=basic', '--test-type',
  '--disable-features=Translate,MediaRouter,PasswordLeakDetection,AutofillServerCommunication,OptimizationHints'];

// persistentShim: what lib.js login() takes as a browser, for a backend that
// films one page of a persistent context — "a new context" and "a new page"
// are both that page
function persistentShim(ctx, page) {
  return { newContext: async () => ({ newPage: async () => page, addInitScript: (s) => ctx.addInitScript(s), close: async () => {} }) };
}

class Backend {
  // o: {pw, width, height, dpr, fps, out, take, codec, quality, frames, display, chromium, theme, log}
  // theme: the system's light or dark the page sees (prefers-color-scheme),
  // which the workspace follows (D184); dark unless asked — Playwright's
  // own default is light
  constructor(o) {
    this.o = o;
    this.clock = new RealClock();
    this.capture = 'none';
    this.browser = null;
  }
  launchArgs() {
    const o = this.o;
    return [`--force-device-scale-factor=${o.dpr}`, `--window-size=${o.width},${o.height}`, '--hide-crash-restore-bubble'];
  }
  async launch() {
    this.browser = await this.o.pw.chromium.launch({ headless: true, ignoreDefaultArgs: ['--hide-scrollbars'], args: this.launchArgs() });
  }
  ctxOpts() { return { viewport: null, deviceScaleFactor: undefined, colorScheme: this.o.theme || 'dark' }; }
  // what lib.js login() takes as a browser: newContext(opts) → {newPage()}
  browserLike() { return this.browser; }
  // roll(): start recording; cut(): stop → {file, firstFrameAt (on this.clock), …}
  async roll() {}
  async cut() { return null; }
  async still(page, file) { await page.screenshot({ path: file, type: 'png' }); }
  async close() { await this.browser?.close().catch(() => {}); }
}

// scratch: Playwright's own recorder — one webm per page, from page creation
class ScratchBackend extends Backend {
  constructor(o) { super(o); this.capture = 'recordVideo'; this.dir = path.join(o.tmp, 'video'); this.pageAt = null; }
  ctxOpts() {
    return { ...super.ctxOpts(), recordVideo: { dir: this.dir, size: { width: this.o.width, height: this.o.height } } };
  }
  browserLike() {
    const b = this.browser, self = this;
    return { newContext: async (opts) => { const ctx = await b.newContext(opts); const np = ctx.newPage.bind(ctx); ctx.newPage = async () => { const p = await np(); self.pageAt = self.clock.now(); self.page = p; return p; }; return ctx; } };
  }
  async roll() { this.rollAt = this.clock.now(); }
  async cut() {
    const page = this.page;
    if (!page) return null;
    const v = page.video();
    await page.context().close();
    const file = path.join(this.o.out, `${this.o.take}.webm`);
    if (v) await v.saveAs(file);
    return { file: path.basename(file), fps: 25, firstFrameAt: this.pageAt ?? this.rollAt, note: 'Playwright recordVideo (VP8, 25 fps, CSS-px size): scratch only, from the page\'s creation', probe: await probe(file) };
  }
}

// x11: headful Chromium in kiosk on DISPLAY, ffmpeg x11grab → mkv (crash-
// safe while recording) → mp4. x11grab stamps each frame with the wall clock
// it grabbed it at, and ffmpeg prints the first stamp, so the video's first
// frame is placed on the camera's clock (startT) from that — not guessed.
class X11Backend extends Backend {
  constructor(o) { super(o); this.capture = 'x11'; }
  // one window, the browser's own startup one: only it opens in --kiosk (a
  // window Playwright opens for a new context keeps its tab strip and
  // toolbar); a persistent context's first page is that window
  async launch() {
    const o = this.o;
    if (!o.display) throw new Error('x11 capture needs an X display: run it through capture.sh (Xvfb), or set DISPLAY');
    this.udd = profile(o.tmp);
    this.ctx = await o.pw.chromium.launchPersistentContext(this.udd, {
      headless: false, viewport: null, colorScheme: o.theme || 'dark',
      executablePath: o.chromium || undefined,
      ignoreDefaultArgs: ['--enable-automation'],
      args: [...this.launchArgs(), '--kiosk', '--window-position=0,0', ...QUIET],
      env: { ...process.env, DISPLAY: o.display },
    });
    this.page = this.ctx.pages()[0] || await this.ctx.newPage();
    // no window manager on Xvfb to make kiosk fullscreen: if the page isn't
    // the whole screen, ask the browser for fullscreen outright
    const vp = await this.page.evaluate(() => [innerWidth, innerHeight]);
    if (vp[0] !== o.width || vp[1] !== o.height) {
      const cdp = await this.ctx.newCDPSession(this.page);
      const { windowId } = await cdp.send('Browser.getWindowForTarget');
      await cdp.send('Browser.setWindowBounds', { windowId, bounds: { windowState: 'fullscreen' } });
      await cdp.detach();
    }
  }
  browserLike() { return persistentShim(this.ctx, this.page); }
  async close() {
    if (this.ff && this.ff.exitCode == null) this.ff.kill('SIGINT'); // a take that failed before its cut
    await this.ctx?.close().catch(() => {});
    if (this.udd) fs.rmSync(this.udd, { recursive: true, force: true });
  }
  async roll() {
    const o = this.o;
    const w = o.width * o.dpr, h = o.height * o.dpr;
    this.mkv = path.join(o.tmp, `${o.take}.mkv`);
    // -fps_mode cfr: a frame the grabber missed is a repeated frame in the
    // file, not a gap in its timeline (check.js counts them); -progress: the
    // frame count, to roll once frames are flowing; loglevel info: the input
    // banner carries the grabber's first timestamp ("start: <s>")
    const args = ['-hide_banner', '-loglevel', 'info', '-nostats', '-progress', 'pipe:1', '-stats_period', '0.1',
      '-f', 'x11grab', '-draw_mouse', '0', '-framerate', String(o.fps), '-video_size', `${w}x${h}`, '-thread_queue_size', '128',
      '-probesize', '32', '-i', `${o.display}+0,0`,
      ...encodeArgs({ codec: o.codec, quality: o.quality, rgb: true }), '-fps_mode', 'cfr', '-r', String(o.fps), '-y', this.mkv];
    this.anchor = { mono: monoMs(), wall: Date.now() };
    this.ff = spawn('ffmpeg', args, { stdio: ['pipe', 'pipe', 'pipe'] });
    this.fferr = '';
    let frames = 0;
    this.ff.stderr.on('data', (d) => { this.fferr += d; });
    this.ff.stdout.on('data', (d) => { const m = String(d).match(/frame=(\d+)/g); if (m) frames = Number(m[m.length - 1].slice(6)); });
    this.ffExit = new Promise((r) => this.ff.on('close', (code) => r(code)));
    for (let i = 0; i < 300 && frames < 3; i++) {
      if (this.ff.exitCode != null) break;
      await new Promise((r) => setTimeout(r, 20));
    }
    if (this.ff.exitCode != null || frames < 3) throw new Error(`ffmpeg x11grab did not start (frames=${frames}): ${this.fferr.slice(-600)}`);
    this.rollAt = this.clock.now();
  }
  async cut() {
    if (!this.ff) return null;
    const cutAt = this.clock.now();
    this.ff.stdin.write('q');
    const code = await Promise.race([this.ffExit, new Promise((r) => setTimeout(() => r('timeout'), 30000))]);
    if (code === 'timeout') { this.ff.kill('SIGINT'); await Promise.race([this.ffExit, new Promise((r) => setTimeout(r, 10000))]); this.ff.kill('SIGKILL'); }
    const p = await probe(this.mkv);
    if (!p) throw new Error(`no readable recording at ${this.mkv}: ${this.fferr.slice(-600)}`);
    // the first frame's stamp, from the input banner: CLOCK_MONOTONIC or the
    // wall clock (x11grab uses the wall clock), whichever it is close to
    const m = /Input #0, x11grab,[\s\S]*?start: ([\d.]+)/.exec(this.fferr);
    const s = m ? Number(m[1]) * 1000 : NaN;
    let firstMono = null, clock = 'unknown';
    if (Math.abs(s - this.anchor.mono) < 120000) { firstMono = s; clock = 'monotonic'; } else if (Math.abs(s - this.anchor.wall) < 120000) { firstMono = this.anchor.mono + (s - this.anchor.wall); clock = 'wall'; }
    const mp4 = path.join(this.o.out, `${this.o.take}.mp4`);
    const r = await run('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-y', '-i', this.mkv, '-c', 'copy', '-movflags', '+faststart', mp4]);
    if (r.code !== 0) {
      const keep = path.join(this.o.out, `${this.o.take}.mkv`); // the take survives as the mkv it was recorded to
      fs.copyFileSync(this.mkv, keep);
      throw new Error(`remux to mp4 failed (the recording is at ${keep}): ${r.err.slice(-600)}`);
    }
    fs.rmSync(this.mkv, { force: true });
    const q = await probe(mp4);
    return { file: path.basename(mp4), fps: this.o.fps, firstFrameAt: firstMono ?? this.anchor.mono, stampClock: clock,
      durationMs: Math.round(cutAt - this.rollAt), probe: q,
      ffmpegWarnings: this.fferr.split('\n').filter((l) => /^\[.*(warn|drop|dup|skip|too large|late)/i.test(l)).slice(-10) };
  }
}

// screencast: Page.startScreencast frames (JPEG), stamped by Chromium's
// swap time; acked one by one, as the protocol wants. Encoded after the cut.
class ScreencastBackend extends Backend {
  constructor(o) { super(o); this.capture = 'screencast'; }
  browserLike() {
    const b = this.browser, self = this;
    return { newContext: async (opts) => { const ctx = await b.newContext(opts); const np = ctx.newPage.bind(ctx); ctx.newPage = async () => { self.page = await np(); return self.page; }; return ctx; } };
  }
  async roll(page) {
    const o = this.o;
    page = page || this.page;
    if (!page) throw new Error('screencast: no page to film (log in or open a page in setup)');
    this.dir = path.join(o.tmp, 'frames');
    fs.mkdirSync(this.dir, { recursive: true });
    this.frames = [];
    this.cdp = await page.context().newCDPSession(page);
    this.anchor = { mono: monoMs(), wall: Date.now() };
    this.rollAt = this.clock.now();
    let n = 0;
    this.cdp.on('Page.screencastFrame', ({ data, metadata, sessionId }) => {
      const file = path.join(this.dir, `f${String(++n).padStart(6, '0')}.jpg`);
      fs.writeFileSync(file, Buffer.from(data, 'base64'));
      // metadata.timestamp: seconds, wall clock → the camera's clock
      const mono = metadata.timestamp ? this.anchor.mono + (metadata.timestamp * 1000 - this.anchor.wall) : this.clock.now();
      this.frames.push({ file, t: mono - this.rollAt });
      this.cdp.send('Page.screencastFrameAck', { sessionId }).catch(() => {});
    });
    await this.cdp.send('Page.startScreencast', { format: 'jpeg', quality: o.jpegQuality || 92, maxWidth: o.width * o.dpr, maxHeight: o.height * o.dpr, everyNthFrame: 1 });
  }
  async cut() {
    if (!this.cdp) return null;
    const end = this.clock.now() - this.rollAt;
    await this.cdp.send('Page.stopScreencast').catch(() => {});
    const frames = this.frames.filter((f) => f.t <= end);
    if (!frames.length) throw new Error('screencast: no frames arrived');
    if (frames[0].t > 0) frames[0] = { ...frames[0], t: 0 }; // hold the first frame from the roll
    const n = Math.max(1, Math.round((end * this.o.fps) / 1000));
    const mp4 = path.join(this.o.out, `${this.o.take}.mp4`);
    await encodeSequence(sequence(frames, path.join(this.dir, 'seq'), n, this.o.fps), mp4, this.o);
    const gaps = frames.slice(1).map((f, i) => f.t - frames[i].t);
    return { file: path.basename(mp4), fps: this.o.fps, firstFrameAt: this.rollAt, durationMs: Math.round(end), captured: frames.length,
      capturedFps: +(frames.length / (end / 1000)).toFixed(2), maxGapMs: Math.round(Math.max(0, ...gaps)), probe: await probe(mp4) };
  }
}

module.exports = { Backend, ScratchBackend, X11Backend, ScreencastBackend, persistentShim, profile, QUIET, encodeArgs, sequence, encodeSequence, hasNvenc, probe, run };
