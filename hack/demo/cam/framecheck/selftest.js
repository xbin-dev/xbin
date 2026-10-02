#!/usr/bin/env node
// hack/demo/cam/framecheck/selftest.js — prove check.js on videos whose faults
// are known: a clean synthetic counter video (pattern.js frames piped into
// ffmpeg, encoded LOSSY so the decode is tested the way it will be used),
// then copies damaged on purpose by ffmpeg filters — frames dropped
// (select), frames repeated (loop), frames torn (the next frame's lower
// rows overlaid on one frame), black frames before the page appears (tpad)
// — and check that check.js reports exactly those faults and nothing else.
//
//   node selftest.js [--dir DIR] [--size 1280x720] [--keep]
'use strict';
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawn, spawnSync } = require('child_process');
const P = require('./pattern');
const { check, format } = require('./check');

const args = process.argv.slice(2);
const opt = (k, d) => { const i = args.indexOf(k); return i >= 0 ? args[i + 1] : d; };
const [W, H] = opt('--size', '1280x720').split('x').map(Number);
const DIR = opt('--dir', fs.mkdtempSync(path.join(os.tmpdir(), 'framecheck-')));
const N = 600, BASE = 70000, FPS = 60; // 10 s; a counter past 16 bits

// frame(n): one greyscale frame of the pattern for counter n
function frame(n) {
  const buf = Buffer.alloc(W * H, 0);
  const b = P.bits(n), gap = Math.max(2, Math.round(W / 400));
  for (let i = 0; i < P.BLOCKS; i++) {
    if (!b[i]) continue;
    const [fx, fy, fw, fh] = P.cell(i);
    const x0 = Math.round(fx * W) + gap, x1 = Math.round((fx + fw) * W) - gap;
    const y0 = Math.round(fy * H) + gap, y1 = Math.round((fy + fh) * H) - gap;
    for (let y = y0; y < y1; y++) buf.fill(255, y * W + x0, y * W + x1);
  }
  // the info strip: something that changes every frame, like the page's
  const bar = Math.round((n % 120) / 120 * W);
  for (let y = Math.round(H * 0.88); y < Math.round(H * 0.9); y++) buf.fill(160, y * W, y * W + bar);
  return buf;
}

function ffmpeg(argv, input) {
  return new Promise((resolve, reject) => {
    const p = spawn('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-y', ...argv], { stdio: [input ? 'pipe' : 'ignore', 'ignore', 'pipe'] });
    let err = '';
    p.stderr.on('data', (d) => { err += d; });
    p.on('close', (c) => (c === 0 ? resolve() : reject(new Error(`ffmpeg ${argv.join(' ')}: ${err.slice(-400)}`))));
    if (input) input(p.stdin);
  });
}

const LOSSY = ['-c:v', 'libx264', '-preset', 'veryfast', '-crf', '30', '-pix_fmt', 'yuv420p'];

async function main() {
  const clean = path.join(DIR, 'clean.mp4');
  await ffmpeg(['-f', 'rawvideo', '-pix_fmt', 'gray', '-s', `${W}x${H}`, '-r', String(FPS), '-i', '-', ...LOSSY, clean], (stdin) => {
    let n = 0;
    const pump = () => { while (n < N) { if (!stdin.write(frame(BASE + n++))) { stdin.once('drain', pump); return; } } stdin.end(); };
    pump();
  });
  // the damage, by ffmpeg — each case: its filter and what check.js must say
  const cfr = `setpts=N/(${FPS}*TB)`;
  const cases = [
    { name: 'clean', vf: null, want: { frames: N, ok: N, dups: 0, dropped: 0, dropEvents: 0, torn: 0, perfect: true } },
    // frames 100, 250 and 400–402 never reach the file: 5 frames in 3 skips
    { name: 'drops', vf: `select='not(eq(n\\,100)+eq(n\\,250)+between(n\\,400\\,402))',${cfr}`,
      want: { frames: N - 5, dups: 0, dropped: 5, dropEvents: 3, torn: 0, perfect: false } },
    // frame 300 shown three times, frame 500 twice: 3 repeats
    { name: 'dups', vf: `loop=loop=2:size=1:start=300,${cfr},loop=loop=1:size=1:start=502,${cfr}`,
      want: { frames: N + 3, dups: 3, dropped: 0, dropEvents: 0, torn: 0, longestHold: 3, perfect: false } },
    // frames 200 and 350 caught mid-update: rows 3–4 of the blocks already show the next frame
    { name: 'torn', complex: `[0:v]split=2[a][b];[b]trim=start_frame=1,setpts=PTS-STARTPTS,crop=iw:ih*0.6:0:ih*0.4[lo];[a][lo]overlay=0:H*0.4:eof_action=pass:enable='eq(n\\,200)+eq(n\\,350)'[v]`,
      want: { frames: N, torn: 2, dups: 0, dropped: 0, perfect: false } },
    // 12 black frames before the page draws (a capture rolling before the load): not a fault
    { name: 'lead-in', vf: `tpad=start=12:color=black,${cfr}`,
      want: { frames: N + 12, leadingBlank: 12, dups: 0, dropped: 0, torn: 0, perfect: true } },
    // all of it at once
    { name: 'mixed', vf: `select='not(eq(n\\,100)+between(n\\,400\\,402))',${cfr},loop=loop=2:size=1:start=300,${cfr}`,
      want: { frames: N - 4 + 2, dups: 2, dropped: 4, dropEvents: 2, torn: 0, perfect: false } },
  ];
  let fails = 0;
  for (const c of cases) {
    const file = path.join(DIR, `${c.name}.mp4`);
    if (c.vf) await ffmpeg(['-i', clean, '-vf', c.vf, ...LOSSY, file]);
    else if (c.complex) await ffmpeg(['-i', clean, '-filter_complex', c.complex, '-map', '[v]', ...LOSSY, file]);
    const { probe, report } = await check(c.vf || c.complex ? file : clean);
    const bad = Object.entries(c.want).filter(([k, v]) => report[k] !== v).map(([k, v]) => `${k}: want ${v}, got ${report[k]}`);
    console.log(`${bad.length ? 'FAIL' : 'PASS'} ${c.name.padEnd(8)} ${report.verdict}${bad.length ? `\n     ${bad.join('\n     ')}` : ''}`);
    if (bad.length) { fails++; console.log(format(file, probe, report).replace(/^/gm, '     | ')); }
  }
  console.log(`${cases.length - fails}/${cases.length} cases as injected (${W}x${H}, lossy H.264 crf 30) — videos in ${DIR}`);
  if (!args.includes('--keep') && !args.includes('--dir')) fs.rmSync(DIR, { recursive: true, force: true });
  return fails ? 1 : 0;
}

if (spawnSync('ffmpeg', ['-version']).status !== 0) { console.log('SKIP: no ffmpeg'); process.exit(0); }
main().then((c) => process.exit(c), (e) => { console.error(e); process.exit(2); });
