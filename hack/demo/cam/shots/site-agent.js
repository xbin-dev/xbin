// shots/site-agent.js — the agent at work: Priya asks about two customers
// who slipped last night; the assistant reads the ops report and both
// accounts in the CRM (real MCP calls) and drafts a note for each account
// owner. Filmed mid-task — the tool calls done, the answer still coming —
// and once it has answered. On a desk that is Lark, the team's agent; on a
// phone the workspace's chat app on the same gateway and tools (the agent
// template's page has no narrow layout: its conversation list keeps 220 px
// of a phone's 360). The set's scripted model paces this question
// (hack/demo/data/lark-script.json); a real or replayed model answers it as
// it would. A take clears what an earlier take of it left.
'use strict';
const site = require('../site');

const APPS = {
  // the agent: its composer, a done tool card, "still answering", the answer
  lark: {
    tile: 'apps/lark', composer: '#msg', tools: '#timeline .tcard',
    working: () => document.querySelectorAll('#timeline .tcard').length >= 3
      && !/Note for .+\(Alder Street\)/.test(document.querySelector('#timeline')?.innerText || ''),
    done: () => /Note for .+\(Alder Street\)/.test(document.querySelector('#timeline')?.innerText || '') && (document.querySelector('#stop')?.hidden ?? true),
    answer: '#timeline .msg.assistant',
  },
  chat: {
    tile: 'apps/chat', composer: '#input', tools: 'details.toolcall.ok',
    working: () => document.querySelectorAll('details.toolcall.ok').length >= 3 && /stop/i.test(document.querySelector('#send')?.textContent || ''),
    done: () => /send/i.test(document.querySelector('#send')?.textContent || '') && /Note for .+\(Alder Street\)/.test(document.querySelector('#log')?.innerText || ''),
    answer: '#log .msg.assistant',
  },
};
const app = (cam) => APPS[site.isPhone(cam) ? 'chat' : 'lark'];

module.exports = async (cam) => {
  const a = app(cam);
  const doc = await cam.in(a.tile);
  const composer = doc.locator(a.composer);
  // (inside a tile under the shell's font zoom a pointer lands off target:
  // focus the composer as a click would, then type)
  await composer.focus();
  await cam.type(cam.args.prompt);
  await cam.press('Enter');
  await cam.mark('asked', composer, { text: cam.args.prompt });
  // mid-task: the report and both accounts read, the answer not yet written
  await doc.waitForFunction(a.working, null, { timeout: 60000, polling: 100 });
  // on the phone Priya types on while it works (the composer's placeholder
  // wraps to two clipped lines at this width; her words don't)
  if (site.isPhone(cam)) { await composer.focus(); await cam.type(cam.args.meanwhile); }
  await cam.sleep(1200);
  await cam.mark('tools', doc.locator(a.tools).last());
  await site.shoot(cam, 'working', site.isPhone(cam)
    ? "Priya on her phone, the team's chat assistant at work: last night's ops report and two CRM accounts read, the answer still coming"
    : "Lark, Larkspan's agent, at work: Priya asked about two customers who slipped last night — it has read the ops report and both accounts in the CRM and is writing the notes for their owners");
  await doc.waitForFunction(a.done, null, { timeout: 90000, polling: 250 });
  // from the top of the answer (the chat follows its end): the chat's own
  // scroller only, never the shell around it
  await doc.evaluate((sel) => {
    const all = document.querySelectorAll(sel);
    const el = all[all.length - 1];
    let box = el?.parentElement;
    while (box && box.scrollHeight <= box.clientHeight) box = box.parentElement;
    if (el && box) box.scrollTop += el.getBoundingClientRect().top - box.getBoundingClientRect().top - 6;
  }, a.answer);
  // …and then her reply
  if (site.isPhone(cam)) { await composer.fill(''); await composer.focus(); await cam.type(cam.args.reply); }
  await cam.mark('answer', doc.locator(a.answer).last(), { optional: true });
  await site.shoot(cam, 'answer', site.isPhone(cam)
    ? "The assistant's answer on a phone: last night's numbers for both customers from the ops report and the CRM, and a note for each account owner"
    : "Lark's answer: last night's on-time and exceptions for both customers from the ops report, the CRM's view of each account, and a note drafted for each account owner");
};

module.exports.description = 'the agent at work: Lark mid-task (tool calls done, answer coming), then its answer';
module.exports.defaults = {
  who: 'priya', screen: 'Today',
  prompt: 'Lakeshore and Alder Street slipped last night. Pull both accounts and draft a note to each account owner.',
  title: 'Lakeshore and Alder Street, last night',
  meanwhile: 'And Pinewood?',
  reply: 'Yes, send both',
};

module.exports.setup = async (cam) => {
  await site.signIn(cam, cam.args.who, { screen: cam.args.screen });
  const a = app(cam);
  if (a.tile === 'apps/chat') {
    // the chat app opened from her sidebar, as tall as the phone
    await cam.sh((t, h) => t.setGeom(() => [{ path: 'apps/chat', x: 0, y: 0, w: 912, h }]), cam.args.phoneH || 624);
    const doc = await site.tile(cam, a.tile, a.composer);
    await doc.locator('#clear').dispatchEvent('click');
  } else {
    const doc = await site.tile(cam, a.tile, a.composer);
    // an earlier take's conversation goes (only that one: by its title)
    await doc.evaluate(async (title) => {
      const d = await (await xbin.fetch(`/api/${xbin.self}/conversations`)).json();
      for (const r of [...(d.pinned || []), ...(d.items || [])].filter((r) => r.title === title)) {
        await xbin.fetch(`/api/${xbin.self}/runs/${r.id}`, { method: 'DELETE' });
      }
    }, cam.args.title);
    await doc.locator('#new').dispatchEvent('click');
    await doc.waitForSelector(a.composer, { timeout: 15000 });
  }
  await site.top(cam, a.tile);
  await cam.cursorAt([cam.o.width * 0.6, cam.o.height * 0.3]);
};
