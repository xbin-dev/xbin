// shots/site-partitions.js — a person's partitions: Priya Raman's own page
// (/xbin/partitions) — the tiles that keep each person's data apart (Merrow,
// expenses), her own instance of each, what it holds, who can change the
// code that runs on her data, and what she decides. A partitioned tile runs
// an instance per person under xbind --isolate (the set's harness runs so);
// she opens both first, so their instances are up.
'use strict';
const site = require('../site');

module.exports = async (cam) => {
  await cam.mark('page', 'bx-partitions-page');
  await cam.mark('agent', `text=${site.AGENT}`, { optional: true });
  await site.shoot(cam, 'partitions', site.isPhone(cam)
    ? 'Priya Raman\'s partitions on her phone: her own instance of Merrow and of the expenses app, what each holds, and who can change their code'
    : "Priya Raman's partitions page: Merrow and the expenses app run an instance of their own for her — what each holds, whose log it is, who can change the code that runs on her data — and what she decides", { settleMs: 1200 });
};

module.exports.description = "a person's partitions: Priya's own instance of Merrow and of expenses";
module.exports.defaults = { who: 'priya' };

module.exports.setup = async (cam) => {
  // her day starts: Merrow on Today, her expense book — each her own instance
  await site.signIn(cam, cam.args.who, { screen: 'Today' });
  await site.tile(cam, site.AGENT, '#msg');
  await cam.sh((t) => t.setScreen('s-expenses'));
  await site.tile(cam, 'apps/expenses', '.it');
  await cam.sh((t) => t.setScreen('s-today'));
  await cam.sh((t) => t?.flushSave?.());
  await cam.goto('/xbin/partitions');
  await cam.page.waitForSelector('bx-partitions-page', { timeout: 15000 });
  await cam.page.locator('text=apps/expenses').first().waitFor({ timeout: 20000 });
  await cam.page.locator(`text=${site.AGENT}`).first().waitFor({ timeout: 20000 });
};
