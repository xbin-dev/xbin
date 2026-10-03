#!/usr/bin/env node
// hack/demo/cam/stills-manifest.js — shots.json for a directory of stills:
// every still the sidecars (<take>.json) beside them list, with its pixel
// size, the viewport and device scale it was shot at, what it shows (the
// shot's caption) and the marks logged for it (those since the take's
// previous still: name, time, the element's box in CSS px).
//
//   node hack/demo/cam/stills-manifest.js DIR      → DIR/shots.json
'use strict';
const fs = require('fs');
const path = require('path');

// pngSize(file): [width, height] from the PNG header (IHDR)
function pngSize(file) {
  const fd = fs.openSync(file, 'r');
  try {
    const b = Buffer.alloc(24);
    fs.readSync(fd, b, 0, 24, 0);
    if (b.toString('ascii', 12, 16) !== 'IHDR') return null;
    return [b.readUInt32BE(16), b.readUInt32BE(20)];
  } finally { fs.closeSync(fd); }
}

function main(dir) {
  if (!dir) { console.error('usage: stills-manifest.js DIR'); process.exit(2); }
  const stills = [];
  const sidecars = fs.readdirSync(dir).filter((f) => f.endsWith('.json') && f !== 'shots.json').sort();
  for (const f of sidecars) {
    let side;
    try { side = JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8')); } catch { continue; }
    if (!side || !Array.isArray(side.stills) || !side.viewport) continue;
    let prev = -Infinity;
    for (const s of side.stills) {
      const file = path.join(dir, s.file);
      if (!fs.existsSync(file)) { prev = s.t; continue; }
      const px = pngSize(file);
      stills.push({
        file: s.file,
        size: px ? `${px[0]}x${px[1]}` : null,
        viewport: `${side.viewport.width}x${side.viewport.height}`,
        dpr: side.dpr,
        device: side.viewport.width < 820 ? 'phone' : 'desk',
        shot: side.shot,
        persona: side.args?.who || null,
        what: s.caption || null,
        marks: side.marks.filter((m) => m.t > prev && m.t <= s.t).map(({ name, t, box, ...rest }) => ({ name, t, box, ...rest })),
        sidecar: f,
        ...(side.error ? { takeError: side.error.split('\n')[0] } : {}),
      });
      prev = s.t;
    }
  }
  const out = {
    generated: new Date().toISOString(),
    timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    note: 'Stills of the demo film set (hack/demo, Larkspan) by hack/demo/cam/site-stills.sh. Mark boxes are CSS px in the viewport; multiply by dpr for image pixels.',
    stills,
  };
  fs.writeFileSync(path.join(dir, 'shots.json'), JSON.stringify(out, null, 1) + '\n');
  console.log(`${path.join(dir, 'shots.json')}: ${stills.length} stills from ${sidecars.length} sidecars`);
}

main(process.argv[2]);
