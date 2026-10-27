// shots/site-workspace.js — the website's working canvas (S-2): Tomás Reyes
// (CTO) on his Ops screen, the nightly ops report beside its own tile
// window, the IDE (D202): the Files list at the side, the report's page code
// in one pane, a shell on the tile below it with the tile's own history, and
// the backend's logs in the dock along the bottom — the run he just started
// from the app among them. A take ends the tile's shells and forgets its
// window first, so retakes start the same. Desk only: on a phone the window
// is a sheet of its own (the terminal shot's phone take).
'use strict';
const site = require('../site');

const TILE = 'apps/ops-report';
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
  const commands = String(cam.args.commands).split('|').map((c) => c.trim()).filter(Boolean);
  await cam.mark('app', site.CARD(TILE));
  await cam.mark('window', `bx-frame[src="${TILE}"] .pop`);
  for (const [i, c] of commands.entries()) {
    await cam.mark(`command-${i + 1}`, TERM, { text: c });
    await cam.type(c);
    await cam.press('Enter');
    await output(cam);
  }
  await cam.mark('logs', `bx-frame[src="${TILE}"] bx-logs`, { optional: true });
  await site.shoot(cam, 'workspace',
    "Tomás Reyes (CTO)'s Ops screen: the nightly ops report beside its own window — the Files list, the report's code, a shell on the tile with its history, and the backend's logs, the run he just started among them", { settleMs: 900 });
};

module.exports.description = "the working canvas: an app beside its own window — files, code, a shell and the logs (S-2)";
module.exports.defaults = {
  who: 'tomas', screen: 'Ops', font: 15,
  commands: "git log --format='%h %an: %s' -5",
  pop: { x: 640, y: 74, w: 940, h: 900 },
};

module.exports.setup = async (cam) => {
  if (site.isPhone(cam)) throw new Error('site-workspace is a desk shot');
  // the shell's own 15 px (a laptop's 17 px zooms a 1600 px desk too far
  // for the window's three columns)
  await site.signIn(cam, cam.args.who, { screen: cam.args.screen, font: cam.args.font });
  // every take starts the same: no live shell on the tile, its window shut
  const req = cam.page.context().request;
  const list = await req.get(`${cam.o.url}/api/xbin/term/sessions?cwd=${encodeURIComponent(TILE)}`);
  for (const s of list.ok() ? await list.json() : []) await req.delete(`${cam.o.url}/ws/term?session=${encodeURIComponent(s.id)}`);
  await req.delete(`${cam.o.url}/api/xbin/prefs/${encodeURIComponent(`term:${TILE.replaceAll('/', ':')}`)}`);
  await cam.openShell();
  const app = await site.tile(cam, TILE, '.k');
  await cam.fr(TILE, (f) => f.closeTerminal());
  await site.top(cam, TILE);
  // the report run from the app itself (the backend logs it) — once: a
  // retake finds it delivered by him already
  const run = app.locator('button:has-text("Run now")');
  const ran = await app.locator(`text=/delivered .* by ${cam.args.who}/`).count();
  if (!ran && await run.count()) { await run.first().dispatchEvent('click'); await cam.sleep(1500); }
  // the tile's window: a Bash, the page code above it, Files at the side, the logs dock
  await cam.fr(TILE, (f) => { f.open('term'); if (!f.tabs.length) f.newTerm(); });
  // the window's box is kept in the shell's zoomed pixels (a person's font
  // size zooms it): set it, look where the window landed on the page, and
  // correct, until it sits on the box asked for
  const want = cam.args.pop;
  let box = { ...want };
  for (let i = 0; i < 4; i++) {
    await cam.fr(TILE, (f, t, b) => f.setPop(b), box);
    await cam.sleep(300);
    const r = await cam.fr(TILE, (f) => { const e = f.popElement()?.getBoundingClientRect(); return e ? { x: e.x, y: e.y, w: e.width, h: e.height } : null; });
    if (!r || (Math.abs(r.x - want.x) < 2 && Math.abs(r.y - want.y) < 2 && Math.abs(r.w - want.w) < 2 && Math.abs(r.h - want.h) < 2)) break;
    const zx = r.w / box.w, zy = r.h / box.h;
    box = { x: Math.round(box.x + (want.x - r.x) / zx), y: Math.round(box.y + (want.y - r.y) / zy), w: Math.round(want.w / zx), h: Math.round(want.h / zy) };
  }
  await cam.waitSel(`${TERM} textarea`, { state: 'attached', timeout: 30000 });
  await prompt(cam);
  await cam.fr(TILE, (f) => {
    const p = f.ide.focus;
    f.ide.openFile('report.js');
    f.ide.move(f.ide.sessionRef(0), p, 'b');
    f.ide.openTool('files');
    f.ide.setDock(true, 190);
  });
  await cam.page.locator(`bx-frame[src="${TILE}"] bx-code-view pre.code`).waitFor({ timeout: 20000 });
  await cam.page.locator(`bx-frame[src="${TILE}"] bx-code-tree`).waitFor({ timeout: 20000 });
  await cam.fr(TILE, (f) => f.focusTerminal());
  await cam.page.keyboard.press('Control+L'); // a fresh screen (start-up can leave a partial-line mark)
  await cam.sleep(300);
};
