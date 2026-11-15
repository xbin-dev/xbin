// ws-states.mjs — the states of the workspace agent's page (ws/page.js,
// docs/workspace-agent.md §The agent's page) against the stubbed backend in
// workspace mode (test/ws-stub.mjs), and its layout at the dock's narrowest.
// What it pins:
//
//   - paused (from xbind's event, read again): a banner that says what still
//     works, the composer off with "Resume to send a message" — and no
//     Resume here: resuming is the person's click in xbin's chrome;
//   - a stopping quota: when it resets, the composer off until then;
//   - turned off by an admin;
//   - xbin updating (the backend said bye, the connection is down), then
//     back: what continued and what couldn't;
//   - no connection: the banner after a while, the draft kept and not sent,
//     Review… off ("Approvals need a connection"), Try now; back: gone;
//   - "Can't do this" with What can it do?; slowing down, quietly;
//   - at 360 px (the dock's narrowest) and at a phone's width nothing
//     scrolls sideways: the conversation, the scope panel, the sessions, the
//     notebook. WS_SHOTS=<dir> also draws the page there, dark and light,
//     narrow and wide.
//
//   node test/ws-states.mjs        (needs playwright + a chromium build)
import { launch, checker } from './backend.mjs';
import { openWs, fresh, SESSIONS, FULL, EMPTY, CONVERSATION, CARDS, msg, call, now } from './ws-stub.mjs';

const { ok, done } = checker();
const browser = await launch();
const calls = (page, method, re) => page.evaluate(([m, s]) => window.__calls.filter((c) => c.method === m && new RegExp(s).test(c.url)), [method, re.source]);
const text = (page, sel) => page.$eval(sel, (e) => e.textContent.replace(/\s+/g, ' ').trim()).catch(() => '');
const until = async (f, ms = 6000) => { for (const t0 = Date.now(); Date.now() - t0 < ms;) { if (await f()) return true; await new Promise((r) => setTimeout(r, 50)); } return false; };
const composer = (page) => page.$eval('bx-chat-composer textarea', (t) => ({ ph: t.placeholder, off: t.disabled, v: t.value, send: t.closest('bx-chat-composer').querySelector('.bxc-send').disabled }));
const grants = (page) => page.evaluate(() => [...document.querySelectorAll('button')].map((b) => b.innerText.trim()).filter((t) => /^(allow|approve|deny|grant|resume)/i.test(t)));
const base = (extra = {}) => ({ sessions: fresh(SESSIONS), full: FULL, empty: EMPTY, views: { 1: fresh(CONVERSATION) }, cards: { 1: fresh(CARDS) }, ...extra });
const STATE = { user: 'ana', mode: 'workspace', delegation: { workspace: true, paused: false }, models: { default: 'hosted/big', models: [] } };

// ---- paused, from xbind's event -------------------------------------------------------------------
{
  const { ctx, page, errors } = await openWs(browser, base({ state: fresh(STATE) }));
  ok('not paused: the composer takes a message', !(await composer(page)).off);
  await page.evaluate(() => { window.__seed.state.delegation.paused = true; window.__event({ type: 'delegate', data: { op: 'state', user: 'ana' } }); });
  await until(async () => !!(await page.$('.wsp-banner[data-state="paused"]')));
  const b = await text(page, '.wsp-banner[data-state="paused"]');
  ok('paused (xbind\'s state event, read again): the banner says what still works', b.includes('Paused by you') && b.includes('can’t read or change anything') && b.includes('Tile agents that are already running keep going'), b);
  const c = await composer(page);
  ok('…the composer is off: "Resume to send a message"', c.off && c.ph === 'Resume to send a message', JSON.stringify(c));
  ok('…and the page offers no Resume: that is xbin\'s chrome', (await grants(page)).length === 0, (await grants(page)).join());
  ok('no page errors (paused)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

// ---- a stopping quota; turned off -------------------------------------------------------------------
{
  const st = { ...fresh(STATE), quota: { stopped: true, resets: '2030-01-02T01:00:00Z', limitWords: 'your 2M tokens for today' } };
  const { ctx, page } = await openWs(browser, base({ state: st }));
  const b = await text(page, '.wsp-banner[data-state="quota"]');
  const at = await page.evaluate(() => { const d = new Date('2030-01-02T01:00:00Z'); return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`; });
  ok('quota: what ran out, when it resets, that reading still works', b.includes('Today’s limit is used up.') && b.includes(`It resets at ${at}`) && b.includes('You can still read everything here'), b);
  const c = await composer(page);
  ok('…the composer is off until then', c.off && c.ph === `Sending resumes at ${at}`, JSON.stringify(c));
  await ctx.close();
}
{
  // the connector's refusal in the run's error, when the state says nothing
  const v = fresh(CONVERSATION);
  v.run = { status: 'error', result: 'llm: 429 xbin_llm_quota_exceeded (insufficient_quota): user.tokensPerDay reached; resets 2030-01-02T01:00:00Z' };
  const { ctx, page } = await openWs(browser, base({ views: { 1: v } }));
  ok('quota, from the run\'s error: the same banner', (await text(page, '.wsp-banner[data-state="quota"]')).includes('Today’s limit is used up.'));
  await ctx.close();
}
{
  const { ctx, page } = await openWs(browser, base({ state: { ...fresh(STATE), delegation: { workspace: false, paused: false } } }));
  ok('turned off by an admin: the banner, the composer off', (await text(page, '.wsp-banner[data-state="off"]')).includes('Turned off for this workspace.') && (await composer(page)).off);
  await ctx.close();
}

// ---- xbin updating, then back; no connection ----------------------------------------------------------
{
  const s = base();
  s.cards[1].agents[1].status = 'stopped';
  s.cards[1].agents[1].waits = false;
  const { ctx, page, errors } = await openWs(browser, s);
  ok('a tile agent an update cut off: "stopped for the update", and that it can continue', (await text(page, '.wsc-agent[data-agent="8"]')).includes('stopped for the update') && (await text(page, '.wsc-agent[data-agent="8"]')).includes('continue where it stopped'));
  await page.evaluate(() => { window.__bye(); window.__drop(true); });
  await until(async () => !!(await page.$('.wsp-banner[data-state="updating"]')));
  ok('the backend said bye and is gone: "xbin is updating"', (await text(page, '.wsp-banner[data-state="updating"]')).includes('xbin is updating. Back in about a minute.'));
  ok('…the composer says it reconnects', (await composer(page)).ph === 'Reconnecting…' && (await composer(page)).off);
  await page.evaluate(() => { window.__seed.cards[1].agents[0].status = 'running'; window.__drop(false); });
  await until(async () => !!(await page.$('.wsp-banner[data-state="updated"]')), 12000);
  const b = await text(page, '.wsp-banner[data-state="updated"]');
  ok('back: "xbin was updated at …", what continued and what couldn\'t', /xbin was updated at \d\d:\d\d\./.test(b) && b.includes('1 of 2 tile agents continued.') && b.includes('Invoices’ could not.'), b);
  ok('…the updating banner is gone', !(await page.$('.wsp-banner[data-state="updating"]')));
  await page.click('.wsp-banner[data-state="updated"] button');
  ok('…and dismissed', !(await page.$('.wsp-banner[data-state="updated"]')));
  // no connection (no bye): after a while, the banner; the draft stays and isn't sent
  await page.fill('bx-chat-composer textarea', 'Also add the customer number as the first column');
  await page.evaluate(() => window.__drop(true));
  await until(async () => !!(await page.$('.wsp-banner[data-state="offline"]')), 8000);
  const off = await text(page, '.wsp-banner[data-state="offline"]');
  ok('no connection: "Can’t reach the workspace", since when, approvals can’t be given', off.includes('Can’t reach the workspace.') && /loaded at \d\d:\d\d/.test(off) && off.includes('Approvals can’t be given'), off);
  await page.keyboard.press('Enter');
  await page.waitForTimeout(200);
  const c = await composer(page);
  ok('…the draft is kept and not sent', c.v === 'Also add the customer number as the first column' && (await calls(page, 'POST', /\/message$/)).length === 0, JSON.stringify(c));
  ok('…the composer says so', (await text(page, '.wsp-hint')).includes('kept here and is not sent until you’re connected'));
  const rv = await page.$eval('.wsc-req[data-req="12"] button', (b) => ({ dis: b.disabled, t: b.title })).catch(() => null);
  ok('…Review… is off: approvals need a connection', rv && rv.dis && rv.t === 'Approvals need a connection.', JSON.stringify(rv));
  await page.evaluate(() => window.__drop(false));
  await page.click('.wsp-banner[data-state="offline"] button');
  ok('Try now: back, and the banner goes', await until(async () => !(await page.$('.wsp-banner[data-state="offline"]')), 8000));
  ok('no page errors (connection)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

// ---- can't do this; slowing down ------------------------------------------------------------------------
{
  const v = fresh(CONVERSATION);
  v.messages.push(
    msg(20, 'user', 'delete the old Leave requests tile'),
    msg(21, 'assistant', '', { toolCalls: [call('d1', 'tile_call', { tile: 'people/leave', method: 'DELETE', path: '/' })] }),
    msg(22, 'tool', 'an agent can\'t do this, whatever is approved — deleting a tile is its person\'s to do. Tell your person it is theirs to do, and offer the nearest thing you can do', { toolCallId: 'd1', name: 'tile_call' }),
    msg(23, 'assistant', '', { toolCalls: [call('d2', 'tile_status', { tile: 'people/leave' })] }),
    msg(24, 'tool', 'too many actions in a minute: wait a little before the next one', { toolCallId: 'd2', name: 'tile_status' }));
  const { ctx, page, errors } = await openWs(browser, base({ views: { 1: v } }));
  const c = await text(page, '[data-refusal="cannot"]');
  ok('"Can\'t do this": what, that it is the person\'s, and What can it do?', c.includes('Can’t do this') && c.includes('deleting a tile is its person\'s to do') && c.includes('It’s yours to do yourself') && c.includes('What can it do?'), c);
  ok('slowing down: a quiet line', (await text(page, '.wsc-quiet')).includes('Slowing down — the agent is doing a lot at once.'));
  await page.click('[data-refusal="cannot"] button');
  ok('What can it do? shows what it can and can\'t, again', await until(async () => (await text(page, '.wsp-whatcan')).includes('It can’t')));
  ok('no page errors (refusals)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

// ---- narrow: nothing scrolls sideways; screenshots -------------------------------------------------------
const shots = process.env.WS_SHOTS || '';
for (const [width, height] of [[360, 760], [390, 844], [1100, 900]]) {
  for (const scheme of width === 390 ? ['dark'] : ['dark', 'light']) {
    const { ctx, page, errors } = await openWs(browser, base({ state: { ...fresh(STATE), models: { default: 'hosted/big', models: [{ id: 'hosted/big', name: 'big' }, { id: 'local/llama', name: 'local-llama' }] } },
      pages: [{ slug: 'p', title: 'How Ana likes things done', summary: 'Reports in bullet points · never email customers without asking', version: 1, updatedBy: 'ana', updatedMs: now, orgs: [] },
        { slug: 'l', title: 'Ledger — month-end steps', summary: '', version: 1, orgs: ['Finance'], locked: true, lockedWhy: 'Not available in this session: Finance isn’t connected', updatedMs: now }] }), { width, height, scheme });
    const wide = async (what) => {
      const o = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, main: document.querySelector('.wsp-main')?.scrollWidth || 0, mainW: document.querySelector('.wsp-main')?.clientWidth || 0, vw: innerWidth }));
      ok(`${width} px ${scheme}, ${what}: nothing scrolls sideways`, o.doc <= o.vw && o.main <= o.mainW + 1, JSON.stringify(o));
    };
    const shot = (name) => (shots ? page.screenshot({ path: `${shots}/${name}.${width}.${scheme}.png` }) : null);
    await wide('the conversation');
    await shot('conversation');
    await page.evaluate(() => document.querySelector('.wsp-main').scrollTo(0, 0));
    await shot('conversation-top');
    await page.click('.wsp-chip');
    await until(async () => !!(await page.$('.wsp-tree')));
    await wide('the scope panel');
    await shot('scope');
    await page.keyboard.press('Escape');
    await page.click('.wsp-sess');
    await wide('the sessions');
    await shot('sessions');
    await page.keyboard.press('Escape');
    await page.click('.wsp-menu-b');
    await page.click('.wsp-menu button >> nth=0');
    await until(async () => (await page.$$('.wsp-pg')).length === 2);
    await wide('the notebook');
    await shot('notebook');
    ok(`${width} px ${scheme}: no page errors`, errors.length === 0, errors.join(' | '));
    await ctx.close();
  }
}
if (shots) {
  // the empty state and two states, to look at
  for (const scheme of ['dark', 'light']) {
    for (const width of [360, 1100]) {
      const { ctx, page } = await openWs(browser, { sessions: fresh(SESSIONS).slice(0, 1).map((s) => ({ ...s, status: 'idle', requests: 0, agents: 0 })), full: FULL, empty: EMPTY,
        state: { ...fresh(STATE), delegation: { workspace: true, paused: true } } }, { width, height: 820, scheme });
      await page.screenshot({ path: `${shots}/empty-paused.${width}.${scheme}.png` });
      await ctx.close();
    }
  }
}

await browser.close();
done('ws-states');
