// shots/site-canvas.js — the website's hero: a workspace with several apps
// at once. Maya Okafor (CEO) on her "Company" screen: Lark with her
// pipeline question, the CRM's pipeline board, the nightly ops report and
// the team calendar. On a phone the same screen stacks its apps.
'use strict';
const site = require('../site');

const APPS = [
  ['apps/lark', 'agent', '#timeline .tcard'],
  ['apps/crm', 'crm', '.card'],
  ['apps/ops-report', 'ops-report', '.k'],
  ['apps/calendar', 'calendar', '.ev'],
];

module.exports = async (cam) => {
  for (const [p, name] of APPS) await cam.mark(name, site.CARD(p), { optional: site.isPhone(cam) });
  await cam.mark('answer', (await cam.in('apps/lark')).locator('#timeline .msg.assistant').last(), { optional: true });
  await site.shoot(cam, 'company', site.isPhone(cam)
    ? "Maya Okafor (CEO) on her phone: her Company screen's apps stacked — the CRM's pipeline, last night's ops report, the calendar"
    : "Maya Okafor (CEO)'s Company screen: Lark answering her pipeline question beside the CRM's pipeline board, last night's ops report and the team calendar");
};

module.exports.description = "the hero: Maya's Company screen, four apps on the canvas";
module.exports.defaults = { who: 'maya', screen: 'Company' };

module.exports.setup = async (cam) => {
  // (a phone shows the screen's apps stacked at their own heights: several
  // of them, from the CRM down — the agent's page has no phone layout)
  await site.signIn(cam, cam.args.who, { screen: cam.args.screen, fit: false });
  const lark = await site.tile(cam, 'apps/lark', '#msg');
  await site.openConversation(lark, 'Pipeline overview');
  for (const [p, , sel] of APPS) await site.tile(cam, p, sel);
  await site.top(cam, 'apps/crm');
};
