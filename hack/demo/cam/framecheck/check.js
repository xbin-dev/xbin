#!/usr/bin/env node
// hack/demo/cam/framecheck/check.js — read a recording of the frame-counter
// page (index.html) back, frame by frame, and say what the capture did:
// every video frame should show the next page frame; a repeat is a
// duplicated frame, a skip is a dropped one, a frame that fails its CRC was
// caught mid-update (torn).
//
//   node check.js <video> [--json FILE] [--list N] [--expect perfect]
//
// ffmpeg decodes the video to small greyscale frames (area-averaged, so
// each block's centre is a clean average); pattern.js decodes the blocks.
// --expect perfect exits 1 unless there were no drops, repeats or torn
// frames between the first and last decoded frame.
'use strict';
const { spawn, spawnSync } = require('child_process');
const fs = require('fs');
const P = require('./pattern');

const SW = 128, SH = 120; // decode size: a block is 16×24 px, its centre ~8×12

function probe(file) {
  const r = spawnSync('ffprobe', ['-v', 'error', '-select_streams', 'v:0', '-show_entries',
    'stream=width,height,r_frame_rate,avg_frame_rate,codec_name,pix_fmt:format=duration', '-of', 'json', file], { encoding: 'utf8' });
  if (r.status !== 0) throw new Error(`ffprobe ${file}: ${r.stderr.trim()}`);
  const j = JSON.parse(r.stdout), s = j.streams[0];
  const rate = (s.avg_frame_rate && s.avg_frame_rate !== '0/0' ? s.avg_frame_rate : s.r_frame_rate).split('/').map(Number);
  return { width: s.width, height: s.height, codec: s.codec_name, pixFmt: s.pix_fmt, fps: rate[0] / (rate[1] || 1), duration: Number(j.format.duration) };
}

// levels(frame): each block's mean brightness over the middle of its cell
function levels(buf) {
  const out = [];
  for (let i = 0; i < P.BLOCKS; i++) {
    const [fx, fy, fw, fh] = P.cell(i);
    const x0 = Math.floor((fx + fw * 0.3) * SW), x1 = Math.ceil((fx + fw * 0.7) * SW);
    const y0 = Math.floor((fy + fh * 0.3) * SH), y1 = Math.ceil((fy + fh * 0.7) * SH);
    let sum = 0, n = 0;
    for (let y = y0; y < y1; y++) for (let x = x0; x < x1; x++) { sum += buf[y * SW + x]; n++; }
    out.push(sum / n);
  }
  return out;
}

// decodeVideo(file) → [{status, value}] per video frame, in order
function decodeVideo(file) {
  return new Promise((resolve, reject) => {
    const ff = spawn('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-threads', '0', '-i', file, '-map', '0:v:0', '-fps_mode', 'passthrough',
      '-vf', `scale=${SW}:${SH}:flags=area,format=gray`, '-f', 'rawvideo', '-'], { stdio: ['ignore', 'pipe', 'pipe'] });
    const size = SW * SH, seq = [];
    let pend = Buffer.alloc(0), err = '';
    ff.stdout.on('data', (d) => {
      pend = pend.length ? Buffer.concat([pend, d]) : d;
      while (pend.length >= size) { seq.push(P.decode(levels(pend.subarray(0, size)))); pend = pend.subarray(size); }
    });
    ff.stderr.on('data', (d) => { err += d; });
    ff.on('close', (code) => (code === 0 ? resolve(seq) : reject(new Error(`ffmpeg: ${err.trim().slice(-500)}`))));
  });
}

// analyze(seq, fps): the capture's record. Between two decoded frames i < j
// the counter should advance by exactly j − i; less is a repeat (a
// duplicated frame), more is a skip (dropped frames). Blank frames before the
// first and after the last decoded one (the page loading, a fade) are
// counted apart and are not faults.
function analyze(seq, fps = 60, { list = 50 } = {}) {
  const r = { frames: seq.length, fps, ok: 0, torn: 0, blank: 0, leadingBlank: 0, trailingBlank: 0, dups: 0, dropEvents: 0, dropped: 0,
    backwards: 0, longestHold: 0, largestJump: 0, first: null, last: null, unique: 0, anomalies: [] };
  const firstOk = seq.findIndex((s) => s.status === 'ok');
  let lastOk = -1;
  for (let i = seq.length - 1; i >= 0; i--) if (seq[i].status === 'ok') { lastOk = i; break; }
  if (firstOk < 0) return { ...r, blank: seq.filter((s) => s.status === 'blank').length, torn: seq.filter((s) => s.status === 'torn').length, verdict: 'no pattern found' };
  r.leadingBlank = firstOk;
  r.trailingBlank = seq.length - 1 - lastOk;
  const note = (i, kind, extra) => { if (r.anomalies.length < list) r.anomalies.push({ frame: i, t: +(i / fps).toFixed(3), kind, ...extra }); };
  let prev = null, hold = 1;
  const seen = new Set();
  for (let i = firstOk; i <= lastOk; i++) {
    const s = seq[i];
    if (s.status !== 'ok') { r[s.status]++; note(i, s.status); continue; }
    r.ok++;
    seen.add(s.value);
    if (prev) {
      const g = i - prev.i, d = s.value - prev.v;
      if (d === 0) { r.dups++; hold++; note(i, 'repeat', { value: s.value, held: hold }); } else {
        hold = 1;
        if (d < 0) { r.backwards++; note(i, 'backwards', { from: prev.v, to: s.value }); } else if (d > g) {
          r.dropEvents++; r.dropped += d - g; note(i, 'skip', { from: prev.v, to: s.value, missing: d - g });
        }
        r.largestJump = Math.max(r.largestJump, d);
      }
      r.longestHold = Math.max(r.longestHold, hold);
    }
    prev = { i, v: s.value };
  }
  r.longestHold = Math.max(r.longestHold, 1);
  r.first = seq[firstOk].value;
  r.last = seq[lastOk].value;
  r.unique = seen.size;
  const span = (lastOk - firstOk + 1) / fps;
  r.uniqueFps = +(r.unique / span).toFixed(2);
  r.pageFps = +((r.last - r.first) / Math.max(1e-9, (lastOk - firstOk) / fps)).toFixed(2);
  r.perfect = !r.dups && !r.dropped && !r.torn && !r.backwards && !r.blank;
  r.verdict = r.perfect ? 'PERFECT: every video frame shows the next page frame'
    : `${r.dups} repeated, ${r.dropped} dropped in ${r.dropEvents} skip(s), ${r.torn} torn, ${r.blank} blank mid-stream`;
  return r;
}

function format(file, p, r) {
  const L = [];
  L.push(`video    ${file}: ${p.width}x${p.height} ${p.codec}/${p.pixFmt}, ${p.fps.toFixed(3)} fps, ${r.frames} frames (${(r.frames / p.fps).toFixed(2)} s)`);
  if (r.first == null) { L.push(`pattern  none found (${r.blank} blank, ${r.torn} torn)`); return L.join('\n'); }
  L.push(`pattern  ${r.ok} decoded, ${r.torn} torn, ${r.blank} blank mid-stream (+${r.leadingBlank} leading, ${r.trailingBlank} trailing blank)`);
  L.push(`counter  ${r.first} → ${r.last}: ${r.unique} distinct page frames, ${r.uniqueFps} per second of video (page counted at ${r.pageFps}/s)`);
  L.push(`faults   ${r.dups} repeated frame(s), ${r.dropped} dropped frame(s) in ${r.dropEvents} skip(s), ${r.backwards} backwards`);
  L.push(`         longest hold ${r.longestHold} frame(s) (${((r.longestHold / p.fps) * 1000).toFixed(0)} ms), largest step ${r.largestJump}`);
  for (const a of r.anomalies.slice(0, 12)) L.push(`         · frame ${a.frame} (${a.t}s) ${a.kind}${a.missing ? ` ${a.from}→${a.to} (−${a.missing})` : ''}${a.held ? ` ${a.value} ×${a.held}` : ''}`);
  if (r.anomalies.length > 12) L.push(`         · … ${r.anomalies.length - 12} more in --json`);
  L.push(`verdict  ${r.verdict}`);
  return L.join('\n');
}

async function check(file, opts = {}) {
  const p = probe(file);
  const seq = await decodeVideo(file);
  return { probe: p, report: analyze(seq, p.fps, opts) };
}

module.exports = { analyze, check, format, decodeVideo, probe, levels, SW, SH };

if (require.main === module) {
  (async () => {
    const a = process.argv.slice(2);
    const file = a.find((x) => !x.startsWith('--') && a[a.indexOf(x) - 1] !== '--json' && a[a.indexOf(x) - 1] !== '--list' && a[a.indexOf(x) - 1] !== '--expect');
    if (!file) { console.error('usage: node check.js <video> [--json FILE] [--list N] [--expect perfect]'); process.exit(2); }
    const opt = (k) => { const i = a.indexOf(k); return i >= 0 ? a[i + 1] : undefined; };
    const { probe: p, report: r } = await check(file, { list: Number(opt('--list')) || 200 });
    console.log(format(file, p, r));
    if (opt('--json')) fs.writeFileSync(opt('--json'), JSON.stringify({ file, probe: p, ...r }, null, 1) + '\n');
    if (opt('--expect') === 'perfect' && !r.perfect) process.exit(1);
  })().catch((e) => { console.error(e.message); process.exit(2); });
}
