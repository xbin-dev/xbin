// shots/terminal.js — a tile's window and a shell in it: the tile alone on
// the canvas, the cursor finds its >_ button, the window opens on the
// launcher, Bash starts, and a couple of commands are typed at a human pace.
//
//   node hack/demo/cam/shot.js terminal --out DIR [--set tile=apps/x] [--set commands='ls|git log --oneline -5']
'use strict';

const CARD = (tile) => `.card[data-path="${tile}"]`;
const FRAME = (tile) => `bx-frame[src="${tile}"]`;

module.exports = async (cam) => {
  const { tile } = cam.args;
  const commands = String(cam.args.commands).split('|').map((c) => c.trim()).filter(Boolean);
  await cam.hold(700);
  await cam.mark('tile', CARD(tile));
  await cam.click(`${CARD(tile)} button.term`);
  await cam.waitSel(`${FRAME(tile)} .pop .launcher`, { timeout: 20000 });
  await cam.mark('window', `${FRAME(tile)} .pop`);
  await cam.hold(600);
  await cam.click(`${FRAME(tile)} .launcher button.lcard:has-text("Bash")`);
  const term = `${FRAME(tile)} bx-terminal`;
  await cam.waitSel(`${term} textarea`, { state: 'attached', timeout: 20000 });
  await prompt(cam, term);
  // a fresh screen (a shell's start-up can leave a partial-line marker)
  if (cam.args.clear) await cam.page.keyboard.press('Control+L');
  await cam.mark('terminal', term);
  // a person clicks into the terminal before typing
  await cam.click(term, { point: (b) => [b.x + b.width * 0.45, b.y + b.height * 0.55] });
  for (const [i, c] of commands.entries()) {
    await cam.hold(i ? 900 : 350);
    await cam.mark(`command-${i + 1}`, term, { text: c });
    await cam.type(c);
    await cam.press('Enter');
    await output(cam, term);
    await cam.mark(`output-${i + 1}`, term);
  }
  await cam.hold(900);
  await cam.still('end');
  await cam.hold(900);
};

// prompt: the shell is ready once its last non-empty line ends in a prompt
async function prompt(cam, term) {
  await cam.page.locator(term).first().evaluate(async (el) => {
    const t = el.testApi();
    for (let i = 0; i < 400; i++) {
      for (let row = 0; row < 60; row++) if (/[$#%>❯]\s*$/.test(t.screenLine(row) || '')) return;
      await new Promise((r) => setTimeout(r, 50));
    }
    throw new Error('no shell prompt');
  });
}

// output: wait until the screen settles (no change for 400 ms)
async function output(cam, term) {
  let last = '', same = 0;
  for (let i = 0; i < 100 && same < 4; i++) {
    await cam.sleep(100);
    const now = await cam.page.locator(term).first().evaluate((el) => { const t = el.testApi(); let s = ''; for (let r = 0; r < 60; r++) s += (t.screenLine(r) || '') + '\n'; return s; });
    same = now === last ? same + 1 : 0;
    last = now;
  }
}

module.exports.description = "a tile's window: launcher → Bash → commands typed at ~10 chars/s";
module.exports.defaults = { tile: 'apps/crawler', commands: 'ls|cat xbin.json', clear: 1 };

module.exports.setup = async (cam) => {
  const { tile } = cam.args;
  await cam.login();
  // every take starts the same: no live shell on the tile, its window shut
  // and back at its default place (the window's geometry is a saved pref)
  const req = cam.page.context().request;
  const list = await req.get(`${cam.o.url}/api/xbin/term/sessions?cwd=${encodeURIComponent(tile)}`);
  for (const s of list.ok() ? await list.json() : []) await req.delete(`${cam.o.url}/ws/term?session=${encodeURIComponent(s.id)}`);
  await req.delete(`${cam.o.url}/api/xbin/prefs/${encodeURIComponent(`term:${tile.replaceAll('/', ':')}`)}`);
  await cam.openShell();
  await cam.usePersonalScreen();
  await cam.sh((t, tile) => t.setGeom(() => [{ path: tile, x: 48 * 3, y: 48 * 2, w: 48 * 15, h: 48 * 10 }]), tile);
  await cam.waitSel(`${CARD(tile)} bx-frame`, { state: 'attached' });
  await cam.fr(tile, (f) => f.closeTerminal());
  await cam.page.locator(CARD(tile)).first().evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await cam.settle();
  await cam.cursorAt([cam.o.width * 0.78, cam.o.height * 0.72]);
};
