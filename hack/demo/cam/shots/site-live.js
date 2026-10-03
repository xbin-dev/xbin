// shots/site-live.js — an app changed live: Priya's onboarding tracker (the
// app Merrow built) before and after a change to its code lands — a go-live
// timeline across the top — and, on a desk, the change itself as Tomás (an
// admin: code needs admin or a code grant) sees it in the tile's code
// window. The change is a file edit in the tile's work tree, as an agent or
// a terminal makes one (tiles/onboarding/next/: timeline.js and three lines
// of board.js); xbind reloads the tile on its own. The take puts the
// tracker's files back first and last — even when it fails partway — so
// retakes start the same; and it writes only into the film set (its
// branding is the company's: site.assertSet).
//
//   --set ws=<workspace dir>   where the set's tiles are on disk (site-stills.sh passes it)
'use strict';
const fs = require('fs');
const path = require('path');
const site = require('../site');

const SRC = path.resolve(__dirname, '../../tiles/onboarding');
const TILE = 'apps/onboarding';
const dir = (cam) => {
  if (!cam.args.ws) throw new Error('--set ws=<the workspace directory> (where apps/onboarding is on disk)');
  return path.join(cam.args.ws, TILE);
};
// original: the tracker's files as seeded — written only where they differ
// (a write the tile already has would reload it for nothing)
function original(cam) {
  const want = fs.readFileSync(path.join(SRC, 'board.js'));
  const at = path.join(dir(cam), 'board.js');
  let have = null;
  try { have = fs.readFileSync(at); } catch { /* gone: write it */ }
  let wrote = false;
  if (!have || !have.equals(want)) { fs.writeFileSync(at, want); wrote = true; }
  const tl = path.join(dir(cam), 'timeline.js');
  if (fs.existsSync(tl)) { fs.rmSync(tl, { force: true }); wrote = true; }
  return wrote;
}
function changed(cam) {
  fs.copyFileSync(path.join(SRC, 'next/timeline.js'), path.join(dir(cam), 'timeline.js'));
  fs.copyFileSync(path.join(SRC, 'next/board.js'), path.join(dir(cam), 'board.js'));
}
const noTimeline = () => !document.querySelector('onboarding-board')?.shadowRoot?.querySelector('.tl');

module.exports = async (cam) => {
  try { await take(cam); } finally { original(cam); }
};

async function take(cam) {
  const phone = site.isPhone(cam);
  await cam.mark('tracker', site.CARD(TILE), { optional: phone });
  await site.shoot(cam, 'before', phone
    ? "Priya's onboarding tracker on her phone, before the change: a card per customer, soonest go-live first"
    : "Priya Raman's onboarding tracker (the app Merrow built for her team) before the change: a card per customer with progress and the next step");
  const reloads = await cam.fr(TILE, (f) => f.reloads);
  changed(cam);
  await cam.mark('saved', null, { files: ['timeline.js', 'board.js'] });
  // xbind sees the save and reloads the tile; the new page draws the timeline
  await cam.waitFor((t, a) => (t.frameFor(a.tile)?.testApi().reloads ?? 0) > a.n, { tile: TILE, n: reloads }, { timeout: 20000, label: 'the tile reloaded' });
  let doc = await cam.in(TILE);
  await doc.waitForSelector('.tl .mark', { timeout: 20000 });
  await cam.mark('timeline', doc.locator('.tl'));
  await site.shoot(cam, 'after', phone
    ? 'The same tracker moments later: the code change landed live — a go-live timeline across the top, the late step in red'
    : 'The same tracker seconds later: a code change landed and the app reloaded itself — a go-live timeline across the top, the late step in red');
  if (!phone) {
    // the change itself, as the CTO reviews it: the tile's code window on
    // its working-tree diff
    await site.signIn(cam, 'tomas', { screen: 'Build' });
    doc = await site.tile(cam, TILE, '.tl .mark');
    await cam.fr(TILE, (f) => f.open('code'));
    await cam.fr(TILE, (f, t, box) => f.setPop(box), cam.args.pop);
    const code = cam.page.locator(`bx-frame[src="${TILE}"] bx-code`);
    await code.locator('button:has-text("Changes")').first().click();
    await code.locator('.diff:has-text("timelineCss")').first().waitFor({ timeout: 20000 });
    await cam.mark('code-window', `bx-frame[src="${TILE}"] .pop`);
    await cam.mark('diff', code.locator('.diff').first());
    await site.shoot(cam, 'diff', "The same change as Tomás Reyes (CTO) reviews it in the tile's code window: board.js's working-tree diff that draws the new timeline, beside the tracker's history of Merrow's commits", { persona: 'tomas' });
    await cam.fr(TILE, (f) => f.closeTerminal());
  }
}

module.exports.description = 'an app changed live: the onboarding tracker before and after a code change, and the diff';
module.exports.defaults = { who: 'priya', screen: 'Onboarding', ws: process.env.DEMO_WS || '', pop: { x: 470, y: 95, w: 600, h: 470 } };

module.exports.setup = async (cam) => {
  await site.signIn(cam, cam.args.who, { screen: cam.args.screen });
  // only into the film set's files: checked before the first write
  await site.assertSet(cam);
  // (a put-back reloads the tile: let it, before reading its frame)
  if (original(cam)) await cam.sleep(2000);
  const doc = await site.tile(cam, TILE, '.card');
  // the board as it loads after the restore: no timeline left from a take
  await doc.waitForFunction(noTimeline, null, { timeout: 20000 });
  await site.top(cam, TILE);
};
