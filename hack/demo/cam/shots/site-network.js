// shots/site-network.js — network approvals: Operations' new telematics
// tile asks for network, and Tomás Reyes (CTO, an admin) decides where it
// goes — through the egress approver, where every new destination will wait
// for a person. The take unbinds the tile's `egress` interface (the set has
// it bound there, hack/demo/seed.sh), films the decision with the approver
// picked, then binds it as the set had it.
'use strict';
const site = require('../site');

const TILE = 'apps/telematics';
const SLOT = 'egress';
const ROW = 'bx-bindings .row';

module.exports = async (cam) => {
  const row = cam.page.locator(ROW, { hasText: TILE }).first();
  const select = row.locator('select').first();
  const opts = await select.evaluate((s) => [...s.options].map((o) => ({ value: o.value, label: o.textContent.trim(), disabled: o.disabled })));
  const pick = opts.find((o) => !o.disabled && o.value === 'apps/egress-approver');
  if (!pick) throw new Error(`no egress approver among ${TILE}'s ${SLOT} options: ${JSON.stringify(opts)}`);
  await select.selectOption(pick.value);
  await cam.settle();
  await cam.mark('decision', row, { option: pick.label });
  await cam.mark('approver', site.CARD('apps/egress-approver'), { optional: true });
  await site.shoot(cam, 'decide', site.isPhone(cam)
    ? 'Network approval on a phone: the new telematics tile asks for network, and the admin routes it through the egress approver'
    : "Network approvals: Operations' new telematics tile asks for network and Tomás Reyes (CTO) routes it through the egress approver, where each new destination waits for a person", { settleMs: 900 });
  // the decision made: the tile's egress goes through the approver, as seeded
  await row.locator('button:has-text("bind")').first().dispatchEvent('click');
  await cam.page.locator(ROW, { hasText: TILE }).first().waitFor({ state: 'detached', timeout: 20000 });
};

module.exports.description = "network approvals: a new tile's network, routed through the egress approver";
module.exports.defaults = { who: 'tomas', screen: 'Network' };

module.exports.setup = async (cam) => {
  await site.signIn(cam, cam.args.who, { screen: cam.args.screen });
  // the tile's network undecided again
  await cam.page.context().request.fetch(`${cam.o.url}/api/xbin/bindings`, { method: 'DELETE', data: { component: TILE, slot: SLOT } });
  await cam.openShell();
  await site.tile(cam, TILE, 'telematics-feeds');
  await site.tile(cam, 'apps/egress-approver', '#meta');
  await cam.page.locator(ROW, { hasText: TILE }).first().waitFor({ timeout: 30000 });
};
