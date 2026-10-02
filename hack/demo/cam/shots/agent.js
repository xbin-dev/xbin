// shots/agent.js — ask the workspace's agent something: the agent tile big on
// the canvas, the cursor goes to its composer, a prompt is typed at a human
// pace and sent, and the shot holds on the answer as it arrives.
//
//   node hack/demo/cam/shot.js agent --out DIR [--set tile=apps/agent] [--set prompt='…']
//
// The answer is whatever model the agent is bound to: on the UI harness that
// is hack/fakeopenai (a scripted test model, which answers "ok: <prompt>" to
// anything it has no script for); a real key or a replayed model gives a
// real answer — this shot only waits for it.
'use strict';
const fs = require('fs');
const path = require('path');

const CARD = (tile) => `.card[data-path="${tile}"]`;
// a take's own conversations, remembered so the next take can remove them:
// a retake starts from the same sidebar, and nothing else is touched
const ledger = (cam) => path.join(cam.o.out, `${cam.o.take}.agent-runs.json`);
const api = (cam, tile, p) => `${cam.o.url}/api/${tile}${p}`;
async function runIds(cam, tile) {
  const r = await cam.page.context().request.get(api(cam, tile, '/runs?roots=1'));
  if (!r.ok()) return [];
  const j = await r.json();
  return (Array.isArray(j) ? j : j.runs || j.items || []).map((x) => x.id).filter(Boolean);
}

module.exports = async (cam) => {
  const { tile, prompt } = cam.args;
  const doc = await cam.in(tile);
  await cam.hold(700);
  await cam.mark('agent', CARD(tile));
  const composer = doc.locator('#msg');
  await cam.click(composer);
  await cam.mark('composer', composer);
  await cam.hold(250);
  await cam.type(prompt);
  await cam.hold(400);
  const before = new Set(await runIds(cam, tile));
  await cam.press('Enter');
  await cam.mark('sent', composer, { text: prompt });
  // the conversation opens with the question, then the answer streams in
  const answer = doc.locator('#timeline .msg.assistant').last();
  await answer.waitFor({ state: 'visible', timeout: 90000 });
  await cam.mark('answer-start', answer);
  await doc.waitForFunction(() => {
    const live = document.querySelector('#timeline .msg.assistant.live');
    const stop = document.querySelector('#stop');
    return !live && (!stop || stop.hidden);
  }, null, { timeout: 120000, polling: 100 });
  await cam.settle();
  await cam.mark('answer', doc.locator('#timeline .msg.assistant:not(.live)').last());
  const mine = (await runIds(cam, tile)).filter((id) => !before.has(id));
  fs.writeFileSync(ledger(cam), JSON.stringify(mine) + '\n');
  await cam.hold(1000);
  await cam.still('answer');
  await cam.hold(1200);
};

module.exports.description = 'the agent tile: type a prompt, send it, hold on the answer';
module.exports.defaults = {
  tile: 'apps/agent',
  prompt: 'Which of our services had errors overnight? Summarize them for the standup.',
};

module.exports.setup = async (cam) => {
  const { tile } = cam.args;
  await cam.login();
  // the last take's conversations go (only those: see ledger)
  try {
    for (const id of JSON.parse(fs.readFileSync(ledger(cam), 'utf8'))) await cam.page.context().request.delete(api(cam, tile, `/runs/${encodeURIComponent(id)}`));
    fs.rmSync(ledger(cam));
  } catch { /* no earlier take */ }
  await cam.openShell();
  await cam.usePersonalScreen();
  // the agent alone on the canvas, large
  const w = Math.floor((cam.o.width - 340) / 48) * 48, h = Math.floor((cam.o.height - 170) / 48) * 48;
  await cam.sh((t, a) => t.setGeom(() => [{ path: a.tile, x: 48, y: 0, w: a.w, h: a.h }]), { tile, w, h });
  await cam.waitSel(`${CARD(tile)} bx-frame`, { state: 'attached' });
  await cam.page.locator(CARD(tile)).first().evaluate((el) => el.scrollIntoView({ block: 'start' }));
  const doc = await cam.in(tile);
  // a fresh question each take: start from the agent's home
  await doc.waitForSelector('#msg', { timeout: 60000 });
  if (await doc.locator('#home').count()) await doc.locator('#home').click();
  await cam.settle();
  await cam.cursorAt([cam.o.width * 0.8, cam.o.height * 0.25]);
};
