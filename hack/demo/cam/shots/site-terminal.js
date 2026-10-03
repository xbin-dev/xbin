// shots/site-terminal.js — a terminal on a tile: Jonas Lindqvist (solutions
// engineer) opens a shell on the onboarding tracker Merrow built and looks at
// its files and its history — Merrow's commits and Priya's, in the tile's own
// git. The shell runs where the tile's code lives (a sandbox under
// --isolate), with the workspace's prompt. A take ends the tile's shells
// and forgets its window first, so retakes start the same.
'use strict';
const site = require('../site');

const TILE = 'apps/onboarding';
const TERM = `bx-frame[src="${TILE}"] bx-terminal`;

// the shell is ready once a line ends in its prompt
async function prompt(cam) {
  await cam.page.locator(TERM).first().evaluate(async (el) => {
    const t = el.testApi();
    for (let i = 0; i < 400; i++) {
      for (let row = 0; row < 80; row++) if (/❯\s*$/.test(t.screenLine(row) || '')) return;
      await new Promise((r) => setTimeout(r, 50));
    }
    throw new Error('no shell prompt');
  });
}
// output: the screen settles (no change for 400 ms)
async function output(cam) {
  let last = '', same = 0;
  for (let i = 0; i < 120 && same < 4; i++) {
    await cam.sleep(100);
    const now = await cam.page.locator(TERM).first().evaluate((el) => { const t = el.testApi(); let s = ''; for (let r = 0; r < 80; r++) s += (t.screenLine(r) || '') + '\n'; return s; });
    same = now === last ? same + 1 : 0;
    last = now;
  }
}

module.exports = async (cam) => {
  // a phone's terminal is a full-screen sheet ~40 columns wide: commands
  // whose output reads at that width
  const commands = String(site.isPhone(cam) ? cam.args.phoneCommands : cam.args.commands).split('|').map((c) => c.trim()).filter(Boolean);
  await cam.mark('window', `bx-frame[src="${TILE}"] .pop`, { optional: true });
  for (const [i, c] of commands.entries()) {
    await cam.mark(`command-${i + 1}`, TERM, { text: c });
    await cam.type(c);
    await cam.press('Enter');
    await output(cam);
  }
  await cam.mark('terminal', TERM);
  await site.shoot(cam, 'shell', site.isPhone(cam)
    ? "A terminal on a phone, on the onboarding tracker: its six small source files, who wrote its commits — Merrow six, Priya two — and when"
    : "Jonas Lindqvist opens a terminal on the onboarding tracker: the tile's files and its git history — Merrow's commits, and Priya's", { settleMs: 900 });
};

module.exports.description = "a terminal on a tile: the onboarding tracker's files and Merrow's commits";
module.exports.defaults = {
  who: 'jonas', screen: 'Onboarding',
  // the tile's own files (what its team wrote; not the workspace's manifest)
  commands: "ls *.js *.md|git log --format='%h %an: %s' -7",
  // ~40 columns: short lines, enough of them to fill the sheet
  phoneCommands: "wc -l *.js|git shortlog -sn|git log --format='%h %<(12)%an %ar' -8",
  pop: { x: 380, y: 95, w: 700, h: 400 },
};

module.exports.setup = async (cam) => {
  await site.signIn(cam, cam.args.who, { screen: cam.args.screen });
  // every take starts the same: no live shell on the tile, its window shut
  const req = cam.page.context().request;
  const list = await req.get(`${cam.o.url}/api/xbin/term/sessions?cwd=${encodeURIComponent(TILE)}`);
  for (const s of list.ok() ? await list.json() : []) await req.delete(`${cam.o.url}/ws/term?session=${encodeURIComponent(s.id)}`);
  await req.delete(`${cam.o.url}/api/xbin/prefs/${encodeURIComponent(`term:${TILE.replaceAll('/', ':')}`)}`);
  await cam.openShell();
  await site.tile(cam, TILE, '.card');
  await cam.fr(TILE, (f) => f.closeTerminal());
  await site.top(cam, TILE);
  // the tile's window, a Bash in it
  await cam.fr(TILE, (f) => { f.open('term'); if (!f.tabs.length) f.newTerm(); });
  if (!site.isPhone(cam)) await cam.fr(TILE, (f, t, box) => f.setPop(box), cam.args.pop);
  await cam.waitSel(`${TERM} textarea`, { state: 'attached', timeout: 30000 });
  await prompt(cam);
  await cam.fr(TILE, (f) => f.focusTerminal());
  await cam.page.keyboard.press('Control+L'); // a fresh screen (start-up can leave a partial-line mark)
  await cam.sleep(300);
};
