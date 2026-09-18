// layout.mjs — the tile is an app shell, so two things must hold at EVERY size
// the shell can hand it: the document never scrolls, and the message composer
// is always on screen at the bottom.
//
// This exists because the failure was silent and easy to reintroduce: a flat
// `min-height: 640px` on .wrap kept the layout 640px tall inside a shorter
// card, which pushed the composer below the fold with no visible cause. Card
// bodies embed tiles with height="100%" (shell/bx-shell.js), so the tile gets
// whatever height the card has — often well under 640.
//
//   node test/layout.mjs        (needs playwright + a chromium build)
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));

let chromium;
try {
  ({ chromium } = await import('/usr/local/node/lib/node_modules/playwright/index.mjs'));
} catch {
  try { ({ chromium } = await import('playwright')); } catch {
    console.log('SKIP: playwright not installed');
    process.exit(0);
  }
}

// Load the real index.html for its real CSS; drop the module (it needs the
// xbin global and /vendor) and stub the theme tokens.
const html = readFileSync(join(here, '..', 'index.html'), 'utf8')
  .replace(/<script type="module"[\s\S]*?<\/script>/g, '')
  .replace(/<link rel="stylesheet" href="\/vendor\/theme.css">/,
    '<style>:root{--bx-border:#ccc;--bx-panel:#fff;--bx-panel-2:#f4f4f4;--bx-text:#111;' +
    '--bx-muted:#777;--bx-accent:#b57e10;--bx-mono:monospace;--bx-red:#c33;--bx-green:#3a3}</style>');

// What loadDetail() actually puts in the top bar: controls that wrap onto
// several rows at the 480px the tile has in a minimum-width column.
const TOPBAR = `<span class="title">a fairly long run title that takes room</span>
  <span class="badge">🔒 private</span><span class="badge running">running</span>
  <button class="btn ghost btnsm">Resume</button><button class="btn ghost btnsm">Interrupt</button>
  <button class="btn ghost btnsm">Compact</button><button class="btn ghost btnsm">Learn skill</button>
  <button class="btn ghost btnsm">Memory (3)</button>
  <button class="btn rm btnsm">Delete</button>`;

let failures = 0;
const ok = (name, cond, extra = '') => {
  if (!cond) { console.log(`FAIL  ${name}  ← ${extra}`); failures++; }
  return cond;
};

const browser = await chromium.launch();
const page = await browser.newPage();

const SCENARIOS = [
  ['empty', () => {}],
  ['busy timeline', () => {
    document.getElementById('timeline').innerHTML =
      Array.from({ length: 80 }, (_, i) => `<div class="ev user"><div class="body">message ${i}</div></div>`).join('');
  }],
  ['settings open', () => {
    document.getElementById('settings').hidden = false;
    document.getElementById('sbd').innerHTML = '<div style="height:3000px">tall settings</div>';
  }],
];

// 220px is far below anything the shell produces; if it holds there it holds.
const HEIGHTS = [220, 320, 420, 500, 640, 900];
const WIDTHS = [700, 1100];

for (const [w] of WIDTHS.map((x) => [x])) {
  for (const [label, setup] of SCENARIOS) {
    for (const h of HEIGHTS) {
      await page.setViewportSize({ width: w, height: h });
      await page.setContent(html);
      await page.evaluate(TOPBAR ? ((t) => { document.getElementById('top').innerHTML = t; }) : null, TOPBAR);
      await page.evaluate(setup);
      await page.waitForTimeout(20);

      const m = await page.evaluate(() => {
        const se = document.scrollingElement;
        const r = (s) => {
          const el = document.querySelector(s);
          if (!el || el.hidden) return null;
          const b = el.getBoundingClientRect();
          return { top: Math.round(b.top), bottom: Math.round(b.bottom), h: Math.round(b.height) };
        };
        return {
          vScroll: se.scrollHeight - se.clientHeight,
          hScroll: se.scrollWidth - se.clientWidth,
          composer: r('.composer'),
          timeline: r('#timeline'),
          vh: innerHeight,
        };
      });

      const tag = `w=${w} ${label.padEnd(16)} vh=${String(h).padStart(3)}`;
      ok(`${tag}: no vertical document scroll`, m.vScroll === 0, `scroll=${m.vScroll}`);
      // The 700px rule: a tile must never scroll sideways.
      ok(`${tag}: no horizontal document scroll`, m.hScroll === 0, `scroll=${m.hScroll}`);
      ok(`${tag}: composer on screen`, m.composer && m.composer.bottom <= m.vh + 1,
        `bottom=${m.composer?.bottom} vh=${m.vh}`);
      ok(`${tag}: composer flush to the bottom`, m.composer && Math.abs(m.composer.bottom - m.vh) <= 1,
        `bottom=${m.composer?.bottom} vh=${m.vh}`);
      ok(`${tag}: composer not squashed`, m.composer && m.composer.h >= 30, `h=${m.composer?.h}`);
    }
  }
}

// At a comfortable height the timeline must keep room to read.
await page.setViewportSize({ width: 700, height: 900 });
await page.setContent(html);
const roomy = await page.evaluate(() => ({
  timeline: Math.round(document.getElementById('timeline').getBoundingClientRect().height),
}));
ok('900px: timeline keeps room', roomy.timeline >= 250, `h=${roomy.timeline}`);

await browser.close();
console.log(failures ? `\n${failures} FAILURE(S)` : `all layout checks passed (${HEIGHTS.length} heights × ${SCENARIOS.length} states × ${WIDTHS.length} widths)`);
process.exit(failures ? 1 : 0);
