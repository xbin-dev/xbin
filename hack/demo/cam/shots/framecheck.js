// shots/framecheck.js — film the frame counter (framecheck/index.html) for a
// while, to measure a capture path: run it through any mode/capture, then
// `node framecheck/check.js <out>/framecheck.mp4` says which frames the
// capture repeated, dropped or tore. No xbind needed.
//
//   node hack/demo/cam/shot.js framecheck --out DIR --capture screencast --set seconds=10 [--set stress=40]
'use strict';
const path = require('path');

module.exports = async (cam) => {
  await cam.sleep(cam.args.seconds * 1000);
  await cam.mark('page', null, { stats: await cam.page.evaluate(() => window.__fc.report()) });
};

module.exports.description = 'the frame-counter page for N seconds (framecheck/check.js reads it back)';
module.exports.defaults = { seconds: 10, counter: 'raf', stress: 0 };

module.exports.setup = async (cam) => {
  const page = path.join(__dirname, '..', 'framecheck', 'index.html');
  await cam.open(`file://${page}?counter=${cam.args.counter}&stress=${cam.args.stress}&fps=${cam.o.fps}`);
  await cam.showCursor(false);
  await cam.page.waitForFunction(() => window.__fc && window.__fc.frames > 10, null, { timeout: 20000 });
};
