// shots/site-warm.js — no still: Priya opens her agent and her expense book,
// which starts her own instance of each (her partitions, under --isolate).
// site-stills.sh runs it before the other shots, so by the partitions
// still her instances have been running for minutes, not seconds.
'use strict';
const site = require('../site');

module.exports = async (cam) => {
  await site.tile(cam, site.AGENT, '#msg');
  await cam.sh((t) => t.setScreen('s-expenses'));
  await site.tile(cam, 'apps/expenses', '.it');
  await cam.sh((t) => t.setScreen('s-today'));
  await cam.sh((t) => t?.flushSave?.());
};

module.exports.description = "no still: Priya's own instances started ahead of the partitions still";
module.exports.defaults = { who: 'priya' };

module.exports.setup = async (cam) => {
  await site.signIn(cam, cam.args.who, { screen: 'Today' });
};
