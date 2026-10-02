#!/usr/bin/env node
// hack/demo/cam/shot.js — run one scripted shot through the camera.
//
//   node hack/demo/cam/shot.js <shot-module> --out <dir> [options]
//
//   <shot-module>   a path, or a name under hack/demo/cam/shots/ (--list)
//   --out DIR       where the take goes: <take>.mp4|.webm, <take>.json (the
//                   sidecar: marks, events, stills, video timing), stills
//   --mode M        still    PNG stills only (cam.still), no video
//                   video    a master (--capture picks how; default beginframe)
//                   scratch  Playwright recordVideo: a quick webm to block out
//   --capture C     beginframe  headless, frame-exact 60 fps in virtual time
//                   x11         headful on $DISPLAY + ffmpeg x11grab (capture.sh)
//                   screencast  headless, CDP Page.startScreencast frames
//   --size WxH      the page in CSS px (1920x1080)    --dpr N   device px per CSS px (2)
//   --fps N         60       --quality lossless|high   --codec auto|h264_nvenc|libx264
//   --frames png|jpeg        beginframe's frame format (png: lossless, slower)
//   --pace human|fast        fast: no glides, no typing beat, no holds (still's default)
//   --url URL       xbind (default $URL, else http://127.0.0.1:$PORT, PORT 8697)
//   --user U --pass P        login for cam.login() (default $XBIN_USER/$XBIN_PASS, admin/admin)
//   --take NAME     file stem (default: the shot's name)      --seed S   motion seed (1)
//   --set k=v       a shot argument (cam.args.k); repeatable
//   --display :N    the X display for --capture x11 (default $DISPLAY)
//   --cursor-scale N the drawn cursor's size (1 = a desktop cursor at 1×)
//   --keep-frames   keep the captured frame dir (beginframe/screencast)
//
// A shot module exports `async (cam) => {…}` — the action, filmed — and
// optionally `.setup = async (cam) => {…}` (log in, dress the set: not
// filmed), `.description`, `.defaults` (cam.args defaults).
'use strict';
const fs = require('fs');
const os = require('os');
const path = require('path');

const HERE = __dirname;
const REPO = path.resolve(HERE, '../../..');
// a mistake on the command line: said in one line, no stack
const usage = (msg) => Object.assign(new Error(msg), { usage: true });

function parse(argv) {
  const o = { mode: 'video', capture: null, size: '1920x1080', dpr: 2, fps: 60, quality: 'lossless', codec: 'auto', frames: 'png',
    pace: null, url: null, user: process.env.XBIN_USER || 'admin', pass: process.env.XBIN_PASS || 'admin', take: null, seed: 1,
    set: {}, display: process.env.DISPLAY || '', keepFrames: false, list: false, shot: null, out: null, cursorScale: 1 };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const val = () => { if (i + 1 >= argv.length) throw usage(`${a} needs a value`); return argv[++i]; };
    switch (a) {
      case '--out': o.out = val(); break;
      case '--mode': o.mode = val(); break;
      case '--capture': o.capture = val(); break;
      case '--size': o.size = val(); break;
      case '--dpr': o.dpr = Number(val()); break;
      case '--fps': o.fps = Number(val()); break;
      case '--quality': o.quality = val(); break;
      case '--codec': o.codec = val(); break;
      case '--frames': o.frames = val(); break;
      case '--pace': o.pace = val(); break;
      case '--url': o.url = val(); break;
      case '--user': o.user = val(); break;
      case '--pass': o.pass = val(); break;
      case '--take': o.take = val(); break;
      case '--seed': o.seed = val(); break;
      case '--display': o.display = val(); break;
      case '--keep-frames': o.keepFrames = true; break;
      case '--cursor-scale': o.cursorScale = Number(val()); break;
      case '--list': o.list = true; break;
      case '--set': { const kv = val(); const j = kv.indexOf('='); if (j < 1) throw usage(`--set wants key=value, got ${kv}`); o.set[kv.slice(0, j)] = kv.slice(j + 1); break; }
      case '-h': case '--help': o.help = true; break;
      default:
        if (a.startsWith('-')) throw usage(`unknown option ${a}`);
        if (o.shot) throw usage(`one shot per run (${o.shot}, ${a})`);
        o.shot = a;
    }
  }
  const m = /^(\d+)x(\d+)$/.exec(o.size);
  if (!m) throw usage(`--size wants WxH, got ${o.size}`);
  o.width = Number(m[1]); o.height = Number(m[2]);
  if (!['still', 'video', 'scratch'].includes(o.mode)) throw usage(`--mode is still|video|scratch, got ${o.mode}`);
  if (o.mode === 'video') o.capture = o.capture || 'beginframe';
  if (o.capture && !['beginframe', 'x11', 'screencast'].includes(o.capture)) throw usage(`--capture is beginframe|x11|screencast, got ${o.capture}`);
  o.pace = o.pace || (o.mode === 'still' ? 'fast' : 'human');
  o.url = (o.url || process.env.URL || `http://127.0.0.1:${process.env.PORT || 8697}`).replace(/\/$/, '');
  return o;
}

function resolveShot(name) {
  const cands = [path.resolve(name), path.join(HERE, 'shots', name), path.join(HERE, 'shots', `${name}.js`)];
  for (const c of cands) if (fs.existsSync(c) && fs.statSync(c).isFile()) return c;
  throw usage(`no shot module ${name} (node shot.js --list)`);
}

function listShots() {
  const dir = path.join(HERE, 'shots');
  for (const f of fs.readdirSync(dir).filter((f) => f.endsWith('.js')).sort()) {
    const m = require(path.join(dir, f));
    console.log(`${f.replace(/\.js$/, '').padEnd(14)} ${m.description || ''}`);
  }
}

function argValues(defaults, set) {
  const out = { ...(defaults || {}) };
  for (const [k, v] of Object.entries(set)) out[k] = /^-?\d+(\.\d+)?$/.test(v) ? Number(v) : v;
  return out;
}

async function main() {
  const o = parse(process.argv.slice(2));
  if (o.help) { console.log(fs.readFileSync(__filename, 'utf8').split('\n').slice(1, 30).map((l) => l.replace(/^\/\/ ?/, '')).join('\n')); return 0; }
  if (o.list) { listShots(); return 0; }
  if (!o.shot) throw usage('which shot? node shot.js <shot-module> --out <dir> (--list)');
  if (!o.out) throw usage('--out <dir> is required');
  const file = resolveShot(o.shot);
  const shot = require(file);
  const action = typeof shot === 'function' ? shot : shot.action;
  if (typeof action !== 'function') throw usage(`${file}: export an async (cam) => {…}`);
  const name = path.basename(file).replace(/\.js$/, '');
  o.take = o.take || name;
  o.out = path.resolve(o.out);
  fs.mkdirSync(o.out, { recursive: true });

  // lib.js reads URL and OUT when it loads
  process.env.URL = o.url;
  process.env.OUT = o.out;
  const lib = require(path.join(REPO, 'hack/ui-harness/lib.js'));
  const log = (...a) => console.log(`[cam ${name}]`, ...a);

  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'xbin-cam-'));
  const bo = { pw: lib.pw, width: o.width, height: o.height, dpr: o.dpr, fps: o.fps, out: o.out, take: o.take, codec: o.codec,
    quality: o.quality, frames: o.frames, display: o.display, chromium: process.env.CAM_CHROMIUM || '', tmp, log };
  const rec = require('./rec');
  let backend;
  if (o.mode === 'still') backend = new rec.Backend(bo);
  else if (o.mode === 'scratch') backend = new rec.ScratchBackend(bo);
  else if (o.capture === 'x11') backend = new rec.X11Backend(bo);
  else if (o.capture === 'screencast') backend = new rec.ScreencastBackend(bo);
  else backend = new (require('./beginframe').BeginFrameBackend)(bo);

  const { Cam } = require('./cam');
  const cam = new Cam({ backend, lib, out: o.out, take: o.take, shot: name, mode: o.mode, width: o.width, height: o.height, dpr: o.dpr,
    fps: o.fps, seed: o.seed, pace: o.pace, user: o.user, pass: o.pass, url: o.url, args: argValues(shot.defaults, o.set), cursorScale: o.cursorScale, log });
  log(`${o.mode}${backend.capture !== 'none' ? ` (${backend.capture})` : ''} ${o.width}x${o.height}@${o.dpr}x → ${o.out}/${o.take}.*`);
  let failed = null;
  // Ctrl-C: let the browser and ffmpeg go, drop the scratch frames
  process.once('SIGINT', () => {
    log('interrupted');
    Promise.race([backend.close(), new Promise((r) => setTimeout(r, 5000))]).finally(() => { fs.rmSync(tmp, { recursive: true, force: true }); process.exit(130); });
  });
  try {
    await backend.launch();
    if (typeof shot.setup === 'function') await shot.setup(cam);
    await backend.roll(cam.page);
    cam.side.roll = { t: cam.t() };
    cam.flush();
    await action(cam);
  } catch (e) {
    failed = e;
    cam.side.error = String(e.stack || e).split('\n').slice(0, 6).join('\n');
  }
  try {
    if (cam.side.roll) {
      cam.side.cut = { t: cam.t() };
      const v = await backend.cut();
      if (v) {
        const { firstFrameAt, ...rest } = v;
        cam.side.video = { ...rest, startT: Math.round((firstFrameAt - cam.t0) * 10) / 10 };
        log('video', v.file, JSON.stringify(Object.fromEntries(Object.entries(rest).filter(([k]) => k !== 'probe' && k !== 'file'))));
      }
    }
  } catch (e) {
    failed = failed || e;
    cam.side.error = (cam.side.error ? cam.side.error + '\n' : '') + `cut: ${e.message}`;
  } finally {
    cam.flush();
    await backend.close();
    if (o.keepFrames) log('frames kept in', tmp); else fs.rmSync(tmp, { recursive: true, force: true });
  }
  log('sidecar', cam.sidecarFile, `(${cam.side.marks.length} marks, ${cam.side.events.length} events, ${cam.side.stills.length} stills)`);
  if (failed) throw failed;
  return 0;
}

main().then((code) => process.exit(code), (e) => {
  if (e.usage) { console.error(`shot.js: ${e.message}`); process.exit(2); }
  console.error('shot failed:', e.stack || e);
  process.exit(1);
});
