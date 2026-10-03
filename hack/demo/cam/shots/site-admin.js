// shots/site-admin.js — the admin console: Tomás Reyes (CTO, a workspace
// admin) on its people — Larkspan's thirteen accounts, their teams, their
// access and when they last signed in — and then viewing the workspace as
// Priya Raman sees it: read-only, under a banner, until he exits. A view-as
// link works once, for two minutes, in the browser that minted it. The
// view-as frame is her Onboarding screen (her team's tracker): what an
// admin may see as her — her own partitions (her agent's conversations)
// stay hers, which a frame can't tell from broken.
'use strict';
const site = require('../site');

const ADMIN = 'tiles/admin';

module.exports = async (cam) => {
  const phone = site.isPhone(cam);
  const doc = await cam.in(ADMIN);
  await cam.mark('people', doc.locator(phone ? 'text=STRUCTURE' : 'text=USERS').first(), { optional: true });
  await site.shoot(cam, 'people', phone
    ? "The admin console on a phone: who is where — the workspace's admins, each team's people, their tiles and allowances"
    : "The admin console as Tomás Reyes (CTO) uses it: Larkspan's thirteen people, their teams and roles, what they can reach and when they last signed in", { settleMs: 1200 });
  // view as Priya: a one-shot link, opened in this browser
  const r = await cam.page.context().request.post(`${cam.o.url}/api/xbin/impersonate`, { data: { user: cam.args.as } });
  if (!r.ok()) throw new Error(`impersonate: HTTP ${r.status()} ${await r.text()}`);
  await cam.goto((await r.json()).url);
  await cam.page.waitForSelector('bx-shell', { timeout: 15000 });
  await cam.waitSel('.viewas', { timeout: 15000 });
  await cam.waitFor((t) => !!t && t.screens.length > 0, null, { timeout: 15000, label: 'her screens' });
  await site.tile(cam, 'apps/onboarding', '.card');
  await cam.mark('banner', '.viewas');
  await cam.mark('tracker', site.CARD('apps/onboarding'), { optional: true });
  await site.shoot(cam, 'view-as', phone
    ? "Viewing as Priya Raman on a phone: her onboarding screen as she sees it, read-only, under the view-as banner"
    : "Tomás viewing the workspace as Priya Raman sees it: her Onboarding screen — her team's tracker, her sidebar and tabs — read-only, under a banner until he exits", { settleMs: 1500 });
  // back to his own session
  await cam.page.locator('.viewas button').first().dispatchEvent('click');
  await cam.page.waitForSelector('.viewas', { state: 'detached', timeout: 15000 }).catch(() => {});
};

module.exports.description = "the admin console: Larkspan's people, then viewing the workspace as Priya";
module.exports.defaults = { who: 'tomas', screen: 'Admin', as: 'priya' };

module.exports.setup = async (cam) => {
  // Priya's screens as the set has them (what the view-as shows is hers),
  // open on Onboarding
  await site.signIn(cam, cam.args.as, { screen: 'Onboarding' });
  // a denser table than the seeded 17 px: 15 px, the console as wide and
  // tall as the canvas then is (a phone: the shell's 13 px, stacked)
  await site.signIn(cam, cam.args.who, { screen: cam.args.screen, font: site.isPhone(cam) ? 13 : 15 });
  if (!site.isPhone(cam)) await cam.sh((t, a) => t.setGeom(() => [{ path: a, x: 0, y: 0, w: 1152, h: 624 }]), ADMIN);
  // the console opens on its people (the tab is the page's hash, read at load)
  await site.tile(cam, ADMIN, 'text=runtime');
  const doc = await cam.in(ADMIN);
  // (a phone: the access map's structure — the people table needs a desk)
  await doc.evaluate((h) => { location.hash = h; location.reload(); }, site.isPhone(cam) ? '#map' : '#users');
  const fresh = await site.tile(cam, ADMIN, site.isPhone(cam) ? 'text=STRUCTURE' : 'text=USERS');
  await fresh.waitForSelector('text=priya', { timeout: 20000 });
  await site.top(cam, ADMIN);
};
