// shots/site-agent.js — the agent at work: Priya asks the team's agent
// (company.json agent) about two customers who slipped last night; it reads
// the ops report and both accounts in the CRM (real MCP calls) and drafts a
// note for each account owner. Filmed mid-task — the tool calls done, the
// answer still coming, Priya typing her next words — and once it has
// answered. On a desk the agent has the screen to itself; on a phone it
// fills it, in the agent page's narrow layout (one column, the list behind
// ☰). The set's scripted model paces this question
// (hack/demo/data/model-script.json); a real or replayed model answers it
// as it would. A take clears what an earlier take of it left.
'use strict';
const site = require('../site');

// the agent's composer, a done tool card, "still answering", the answer
const A = {
  composer: '#msg', tools: '#timeline .tcard',
  working: () => document.querySelectorAll('#timeline .tcard').length >= 3
    && !/Note for .+\(Alder Street\)/.test(document.querySelector('#timeline')?.innerText || ''),
  done: () => /Note for .+\(Alder Street\)/.test(document.querySelector('#timeline')?.innerText || '') && (document.querySelector('#stop')?.hidden ?? true),
  answer: '#timeline .msg.assistant',
};

module.exports = async (cam) => {
  const phone = site.isPhone(cam);
  const doc = await cam.in(site.AGENT);
  const composer = doc.locator(A.composer);
  // (inside a tile under the shell's font zoom a pointer lands off target:
  // focus the composer as a click would, then type)
  await composer.focus();
  await cam.type(cam.args.prompt);
  await cam.press('Enter');
  await cam.mark('asked', composer, { text: cam.args.prompt });
  // mid-task: the report and both accounts read, the answer not yet written
  await doc.waitForFunction(A.working, null, { timeout: 60000, polling: 100 });
  // Priya types on while it works: her next words, not the composer's
  // placeholder, are what the frame shows there
  await composer.focus();
  await cam.type(cam.args.meanwhile);
  await cam.sleep(1200);
  await cam.mark('tools', doc.locator(A.tools).last());
  await site.shoot(cam, 'working', phone
    ? `Priya on her phone, ${site.company.agent.name} at work: last night's ops report and two CRM accounts read, the answer still coming`
    : `${site.company.agent.name}, Larkspan's agent, at work: Priya asked about two customers who slipped last night — it has read the ops report and both accounts in the CRM and is writing the notes for their owners`);
  await doc.waitForFunction(A.done, null, { timeout: 90000, polling: 250 });
  // from the top of the answer to its first note: the chat's own scroller
  // only, never the shell around it
  await doc.evaluate((sel) => {
    const all = document.querySelectorAll(sel);
    const el = all[all.length - 1];
    let box = el?.parentElement;
    while (box && box.scrollHeight <= box.clientHeight) box = box.parentElement;
    if (el && box) box.scrollTop += el.getBoundingClientRect().top - box.getBoundingClientRect().top - 6;
  }, A.answer);
  // …and then her reply, typed
  await composer.fill('');
  await composer.focus();
  await cam.type(cam.args.reply);
  await cam.mark('answer', doc.locator(A.answer).last(), { optional: true });
  await site.shoot(cam, 'answer', phone
    ? `${site.company.agent.name}'s answer on Priya's phone: last night's numbers for both customers from the ops report and the CRM, and a note for each account owner`
    : `${site.company.agent.name}'s answer: last night's on-time and exceptions for both customers from the ops report, the CRM's view of each account, and a note drafted for each account owner`);
};

module.exports.description = "the agent at work: mid-task (tool calls done, answer coming), then its answer";
module.exports.defaults = {
  who: 'priya', screen: 'Today',
  prompt: 'Lakeshore and Alder Street slipped last night. Pull both accounts and draft a note to each account owner.',
  title: 'Lakeshore and Alder Street, last night',
  meanwhile: 'Keep each note under 100 words',
  reply: 'Yes, send both',
};

module.exports.setup = async (cam) => {
  await site.signIn(cam, cam.args.who, { screen: cam.args.screen, fit: false });
  // the agent alone on her screen: the width a conversation reads at (a
  // phone: the whole screen, its first card)
  await cam.sh((t, a) => t.setGeom(() => [{ path: a.tile, x: 0, y: 0, w: a.w, h: a.h }]),
    { tile: site.AGENT, w: 912, h: site.isPhone(cam) ? 690 : 528 });
  await site.fitPhone(cam);
  const doc = await site.tile(cam, site.AGENT, A.composer);
  // an earlier take's conversation goes (only that one: by its title)
  await doc.evaluate(async (title) => {
    const d = await (await xbin.fetch(`/api/${xbin.self}/conversations`)).json();
    for (const r of [...(d.pinned || []), ...(d.items || [])].filter((r) => r.title === title)) {
      await xbin.fetch(`/api/${xbin.self}/runs/${r.id}`, { method: 'DELETE' });
    }
  }, cam.args.title);
  await doc.locator('#new').dispatchEvent('click');
  await doc.waitForSelector(A.composer, { timeout: 15000 });
  await site.top(cam, site.AGENT);
  await cam.cursorAt([cam.o.width * 0.6, cam.o.height * 0.3]);
};
