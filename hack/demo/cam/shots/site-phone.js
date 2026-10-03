// shots/site-phone.js — the workspace on a phone: Priya Raman's screens in
// the shell's narrow layout (under 820 px: a drawer for the sidebar, the
// screen's apps stacked full width, windows as full-screen sheets) — her
// own expense book (her partition: "yours"), the team inbox, and the
// drawer with her apps and tabs. Meant for a phone viewport (390×844 at 3);
// on a desk it films the same screens as a desk shows them.
'use strict';
const site = require('../site');

async function screen(cam, name, tile, sel) {
  await cam.sh((t, id) => t.setScreen(id), site.screenId(name));
  await cam.settle();
  await site.fitPhone(cam);
  const doc = await site.tile(cam, tile, sel);
  await site.top(cam, tile);
  return doc;
}

module.exports = async (cam) => {
  const phone = site.isPhone(cam);
  await screen(cam, 'Expenses', 'apps/expenses', '.it');
  await cam.mark('expenses', site.CARD('apps/expenses'), { optional: true });
  await site.shoot(cam, 'expenses', phone
    ? "Priya Raman's own expense book on her phone: a partition of her own (\"yours\"), drafts, what waits for approval and what's paid"
    : "Priya Raman's own expense book: a partition of her own (\"yours\") — drafts, what waits for approval and what's paid", { settleMs: 1000 });
  await screen(cam, 'Inbox', 'apps/email', '.th');
  await cam.mark('inbox', site.CARD('apps/email'), { optional: true });
  await site.shoot(cam, 'inbox', phone
    ? "The team inbox on Priya's phone: customers' threads, today's meetings from the calendar"
    : "The team inbox: customers' threads and today's meetings from the calendar", { settleMs: 1000 });
  if (phone) {
    // the drawer: her apps by team, her tabs
    await cam.sh((t) => t.setDrawer(true));
    await cam.waitSel('bx-side.drawer.open', { timeout: 10000 });
    await cam.sleep(400); // the drawer's slide (0.2 s)
    await cam.mark('drawer', 'bx-side.drawer');
    await site.shoot(cam, 'drawer', "The shell's drawer on Priya's phone: her apps by team — the CRM and onboarding for Sales, the workspace's own — over her inbox", { settleMs: 600 });
    await cam.sh((t) => t.setDrawer(false));
  }
};

module.exports.description = "the workspace on a phone: Priya's expense book, the inbox, the drawer";
module.exports.defaults = { who: 'priya' };

module.exports.setup = async (cam) => {
  await site.signIn(cam, cam.args.who, { screen: 'Expenses' });
};
