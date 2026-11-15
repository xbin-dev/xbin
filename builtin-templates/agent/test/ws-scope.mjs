// ws-scope.mjs — the workspace agent's page: the scope chip and panel, a
// model change with its what-if and a refused change, and the notebook
// (ws/scope.js, ws/scope-model.js, ws/panels.js) against the stubbed
// backend in workspace mode (test/ws-stub.mjs). What it pins:
//
//   - the chip: the connection line and the model, one button, a sentence
//     for a screen reader;
//   - the panel: the four totals, orgs unfolding to tiles, each tile's
//     state as a glyph and a word, a blocked org's sentence once, the share
//     that put a tile under an org; a blocked tile can't be ticked; ticks
//     are staged and Apply sends the scope (an org whose allowed tiles are
//     all ticked by its id); the foot says scope is focus, not permission;
//     the tree answers the keyboard (arrows, space);
//   - the model: the what-if before anything changes (connects, by name;
//     disconnects; the resulting line), Switch model; a change the
//     conversation's data doesn't allow — from the what-if or a 409 — is the
//     refused card, whose way forward is a new session on that model;
//   - the notebook: the list, a page this session can't read listed but
//     closed with the reason, reading, editing (from the version seen), Add a
//     note, deleting behind a confirmation, clearing a page's org marks only
//     behind its own.
//
//   node test/ws-scope.mjs        (needs playwright + a chromium build)
import { launch, checker } from './backend.mjs';
import { openWs, fresh, SESSIONS, FULL, EMPTY, CONVERSATION, CARDS, now } from './ws-stub.mjs';

const { ok, done } = checker();
const browser = await launch();
const calls = (page, method, re) => page.evaluate(([m, s]) => window.__calls.filter((c) => c.method === m && new RegExp(s).test(c.url)), [method, re.source]);
const text = (page, sel) => page.$eval(sel, (e) => e.textContent.replace(/\s+/g, ' ').trim()).catch(() => '');
const until = async (f, ms = 4000) => { for (const t0 = Date.now(); Date.now() - t0 < ms;) { if (await f()) return true; await new Promise((r) => setTimeout(r, 40)); } return false; };

const MODELS = [{ id: 'hosted/big', name: 'big' }, { id: 'hosted/small', name: 'small' }, { id: 'local/llama', name: 'local-llama' }, { id: 'hosted/fast', name: 'fast' }];
const seed = () => ({
  state: { user: 'ana', mode: 'workspace', delegation: { workspace: true, paused: false }, models: { default: 'hosted/big', models: MODELS } },
  sessions: fresh(SESSIONS), full: fresh(FULL), empty: EMPTY, views: { 1: fresh(CONVERSATION) }, cards: { 1: fresh(CARDS) },
  conn: { 1: { connection: { counts: FULL.counts }, summary: FULL.counts.summary, model: 'hosted/big', handle: 'h1' } },
  whatIf: {
    'local/llama': { backend: { model: 'local/llama', connects: 2, disconnects: 0, allowed: true, counts: {}, summary: '5 tiles · 3 orgs connected · 1 not connected' },
      full: { model: { upstream: 'local', model: 'llama', private: true }, connect: [{ tile: 'finance/ledger', name: 'Ledger' }, { tile: 'apps/supplier', name: 'Supplier portal' }], disconnect: [] } },
    'hosted/small': { backend: { model: 'hosted/small', connects: 0, disconnects: 1, allowed: false, taintedOrgs: ['Finance'], counts: {}, summary: '' },
      full: { model: { upstream: 'hosted', model: 'small', private: false }, connect: [], disconnect: [{ tile: 'sales/orders', name: 'Orders', reason: 'Sales allows private models only' }] } },
    'hosted/fast': { backend: { model: 'hosted/fast', connects: 0, disconnects: 1, allowed: true, counts: {}, summary: '2 tiles · 1 org connected · 4 not connected' },
      full: { model: { upstream: 'hosted', model: 'fast', private: false }, connect: [], disconnect: [{ tile: 'people/calendar', name: 'Team calendar', reason: 'People allows private models only' }] } },
  },
  taint: ['hosted/fast'],
});

// ---- the chip and the panel ----------------------------------------------------------------------
{
  const { ctx, page, errors } = await openWs(browser, seed());
  const chip = await page.$eval('.wsp-chip', (b) => ({ t: [...b.querySelectorAll('.seg')].map((x) => x.textContent.trim()).join(' | '), label: b.getAttribute('aria-label'), exp: b.getAttribute('aria-expanded') }));
  ok('the chip: connected, not connected, the model', chip.t === '3 tiles · 2 orgs | 3 not connected | big', JSON.stringify(chip));
  ok('…one element for a screen reader', chip.label === 'Scope: 3 tiles in 2 orgs connected, 3 not connected. Model big.' && chip.exp === 'false', JSON.stringify(chip));
  await page.click('.wsp-chip');
  await until(async () => !!(await page.$('.wsp-tree')));
  ok('the panel is a dialog named for what it says', (await page.$eval('.wsp-scope', (d) => `${d.getAttribute('role')}:${d.getAttribute('aria-label')}`)) === 'dialog:What this session is connected to');
  const totals = await page.$$eval('.wsp-total', (t) => t.map((x) => x.innerText.replace(/\s+/g, ' ').trim()));
  ok('totals first: connected, hidden by you, blocked, no access', totals.join('|') === '3 connected|1 hidden by you|2 blocked|0 no access', totals.join('|'));
  ok('the model on one row, with Change…', (await text(page, '.wsp-modelrow')).includes('big') && (await text(page, '.wsp-modelrow')).includes('not private') && (await text(page, '.wsp-modelrow')).includes('Change…'));
  const orgs = await page.$$eval('.wsp-ti.org', (o) => o.map((x) => ({ name: x.querySelector('b').textContent, checked: x.getAttribute('aria-checked'), exp: x.getAttribute('aria-expanded'), dis: x.getAttribute('aria-disabled'), t: x.textContent.replace(/\s+/g, ' ') })));
  ok('orgs as tree items: Sales partly ticked, Finance blocked and unfolded, the tiles in no org last', orgs.map((o) => o.name).join() === 'Sales,Finance,Workspace'
    && orgs[0].checked === 'mixed' && orgs[1].dis === 'true' && orgs[1].exp === 'true' && /blocked/.test(orgs[1].t) && /tiles in no org/.test(orgs[2].t), JSON.stringify(orgs));
  const reason = await text(page, '.wsp-reason');
  ok('a blocked org\'s sentence once, with the way to see what a private model would connect', reason.includes('Finance allows private models only') && reason.includes('pinned by a workspace admin') && reason.includes('See what a private model would connect'), reason);
  const ledger = await page.$eval('[data-tile="finance/ledger"]', (x) => ({ checked: x.getAttribute('aria-checked'), dis: x.getAttribute('aria-disabled'), label: x.getAttribute('aria-label') }));
  ok('a blocked tile: dimmed, can\'t be ticked, says why', ledger.dis === 'true' && ledger.label === 'Ledger, blocked: Finance allows private models only, dimmed', JSON.stringify(ledger));
  ok('a tile under an org by a share says so', (await text(page, '[data-tile="apps/supplier"]')).includes('This tile is under finance by its share apps/*'));
  await page.click('[data-tile="finance/ledger"]', { force: true });
  ok('…a click on it changes nothing', !(await page.$eval('.wsp-pop-f button.primary', (b) => !b.disabled)));
  ok('the foot: scope is focus, not permission', (await text(page, '.wsp-pop-f p')).includes('It is not a permission — blocked stays blocked whatever you tick'));
  // unfold Sales and tick the hidden tile, by the keyboard
  await page.focus('.wsp-ti.org[data-key="o:sales"]');
  await page.keyboard.press('ArrowRight');
  await until(async () => !!(await page.$('[data-tile="sales/prices"]')));
  await page.keyboard.press('ArrowDown'); await page.keyboard.press('ArrowDown'); await page.keyboard.press('ArrowDown');
  ok('arrows move through the tree', (await page.evaluate(() => document.activeElement && document.activeElement.dataset.key)) === 't:sales/prices', await page.evaluate(() => document.activeElement && document.activeElement.dataset.key));
  await page.keyboard.press(' ');
  await until(async () => (await page.$eval('[data-tile="sales/prices"]', (x) => x.getAttribute('aria-checked'))) === 'true');
  ok('space ticks; the org is whole again', (await page.$eval('.wsp-ti.org[data-key="o:sales"]', (x) => x.getAttribute('aria-checked'))) === 'true');
  ok('…and the focus stays on the tile', (await page.evaluate(() => document.activeElement && document.activeElement.dataset.key)) === 't:sales/prices');
  // untick Team calendar: the scope is Sales (whole) + nothing of Workspace
  await page.click('.wsp-ti.org[data-key="o:workspace"]');
  await until(async () => !!(await page.$('[data-tile="people/calendar"]')));
  await page.click('[data-tile="people/calendar"]');
  await page.click('.wsp-filter button:nth-child(3)');
  const notShown = await page.$$eval('.wsp-ti.tile', (t) => t.map((x) => x.dataset.tile));
  ok('the filter: Not connected shows what isn\'t', !notShown.includes('sales/orders') && notShown.includes('people/calendar') && notShown.includes('finance/ledger'), notShown.join());
  await page.click('.wsp-pop-f button.primary');
  await until(async () => (await calls(page, 'PATCH', /\/ws\/sessions\/1$/)).length === 1);
  ok('Apply sends the staged scope: a whole org by its id', JSON.stringify(JSON.parse((await calls(page, 'PATCH', /\/ws\/sessions\/1$/))[0].body)) === JSON.stringify({ scope: { orgs: ['sales'], tiles: [] } }),
    (await calls(page, 'PATCH', /\/ws\/sessions\/1$/))[0]?.body);
  ok('…and the panel closes', await until(async () => !(await page.$('.wsp-scope'))));
  await page.click('.wsp-chip');
  await until(async () => !!(await page.$('.wsp-scope')));
  await page.click('.wsp-filter .quiet');
  await page.click('.wsp-pop-f button.primary');
  await until(async () => (await calls(page, 'PATCH', /\/ws\/sessions\/1$/)).length === 2);
  ok('Select all allowed, applied: every allowed tile — scope null', JSON.stringify(JSON.parse((await calls(page, 'PATCH', /\/ws\/sessions\/1$/))[1].body)) === JSON.stringify({ scope: null }));
  ok('no page errors (scope)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

// ---- the model: what-if, switch, refused ----------------------------------------------------------
{
  const { ctx, page, errors } = await openWs(browser, seed());
  await page.click('.wsp-chip');
  await until(async () => !!(await page.$('.wsp-modelrow button')));
  await page.click('.wsp-modelrow button');
  await until(async () => !!(await page.$('.wsp-models')));
  ok('Change…: the models the person may use, the current one marked', (await page.$$eval('.wsp-model', (m) => m.map((x) => x.dataset.model))).join() === MODELS.map((m) => m.id).join() && (await text(page, '.wsp-model[data-model="hosted/big"]')).includes('current'));
  await page.click('.wsp-model[data-model="local/llama"] input');
  await until(async () => (await text(page, '.wsp-whatif')).includes('Connects 2 tiles'));
  const w = await text(page, '.wsp-whatif');
  ok('the what-if before anything changes: connects, by name; disconnects none; the line after', w.includes('If you switch to llama') && w.includes('private') && w.includes('Ledger') && w.includes('Supplier portal')
    && w.includes('Disconnects none') && w.includes('5 tiles · 3 orgs connected · 1 not connected'), w);
  ok('…nothing changed yet', (await calls(page, 'PATCH', /\/ws\/sessions\/1$/)).length === 0);
  await page.click('.wsp-model[data-model="hosted/fast"] input');
  await until(async () => (await text(page, '.wsp-whatif')).includes('Disconnects 1'));
  ok('a change that disconnects lists them with why, and the button says so', (await text(page, '.wsp-whatif')).includes('Team calendar — People allows private models only')
    && (await text(page, '.wsp-models .wsp-pop-f button.primary')) === 'Switch and disconnect 1');
  await page.click('.wsp-models .wsp-pop-f button.primary');
  await until(async () => !!(await page.$('.wsp-refused')));
  ok('a switch refused by the conversation\'s data (409 model_taint): the refused card', (await text(page, '.wsp-refused')).includes('This session can’t move to fast') && (await text(page, '.wsp-refused')).includes('Finance allows private models only'), await text(page, '.wsp-refused'));
  ok('…its ways: a new session on that model, or stay', (await page.$$eval('.wsp-refused button', (b) => b.map((x) => x.textContent.trim()))).join('|') === 'Start a new session on fast|Stay on big');
  await page.click('.wsp-refused button.primary');
  await until(async () => (await calls(page, 'POST', /\/ws\/sessions$/)).length >= 1);
  const made = (await calls(page, 'POST', /\/ws\/sessions$/)).map((c) => JSON.parse(c.body)).pop();
  ok('Start a new session on fast: a new session, from this one, on that model', made.from === 1 && made.model === 'hosted/fast', JSON.stringify(made));
  ok('…and it is the one in view', await until(async () => (await page.evaluate(() => window.wsAgentPage.testApi().sel)) !== 1));
  ok('no page errors (model)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}
{
  // the what-if says it isn't allowed: the refused card before any switch; a policy refusal's button opens the models
  const { ctx, page } = await openWs(browser, seed());
  await page.click('[data-refusal="policy"] button.primary');
  await until(async () => !!(await page.$('.wsp-models')));
  ok('"See what a private model connects…" opens the model\'s what-if', !!(await page.$('.wsp-models')));
  await page.click('.wsp-model[data-model="hosted/small"] input');
  await until(async () => !!(await page.$('.wsp-refused')));
  ok('a what-if that isn\'t allowed: the refused card, nothing switched', (await text(page, '.wsp-refused')).includes('This session can’t move to small') && (await calls(page, 'PATCH', /sessions\/1$/)).length === 0);
  await page.click('.wsp-refused button:not(.primary)');
  ok('Stay closes it', await until(async () => !(await page.$('.wsp-refused'))));
  await ctx.close();
}

// ---- the notebook ------------------------------------------------------------------------------------
{
  const s = seed();
  s.pages = [
    { slug: 'how-ana-likes-things', title: 'How Ana likes things done', summary: 'Reports in bullet points', version: 3, updatedBy: 'ana', updatedMs: now - 86400e3, orgs: [], body: '# How Ana likes things\n\n- bullet points' },
    { slug: 'orders-code', title: 'Orders — how the code is laid out', summary: 'Summary by the code reader', version: 1, updatedBy: 'agent', updatedMs: now, orgs: ['Sales'], tile: 'sales/orders', rev: 'a41f9c2', body: 'The export lives in export.go.' },
    { slug: 'ledger-steps', title: 'Ledger — month-end steps', summary: 'x', version: 2, updatedBy: 'agent', updatedMs: now, orgs: ['Finance'], locked: true, lockedWhy: 'Not available in this session: Finance doesn’t allow this session’s model', body: '' },
  ];
  const { ctx, page, errors } = await openWs(browser, s);
  await page.click('.wsp-menu-b');
  await page.click('.wsp-menu button >> nth=0');
  await until(async () => (await page.$$('.wsp-pg')).length === 3);
  ok('the notebook: every page, with its line and its org tag', (await text(page, '.wsp-nb-h')).includes('3 pages') && (await text(page, '.wsp-pg[data-slug="how-ana-likes-things"]')).includes('yours')
    && (await text(page, '.wsp-pg[data-slug="orders-code"]')).includes('Sales'));
  const closed = await page.$eval('.wsp-pg[data-slug="ledger-steps"]', (x) => ({ t: x.textContent.replace(/\s+/g, ' '), dis: x.querySelector('button').disabled }));
  ok('a page this session may not read: listed, closed, with the reason', closed.dis && closed.t.includes('Not available in this session: Finance doesn’t allow this session’s model'), JSON.stringify(closed));
  ok('the foot says deleting makes the agent forget', (await text(page, '.wsp-nb .wsp-foot')).includes('Deleting a page makes the agent forget it'));
  await page.click('.wsp-pg[data-slug="orders-code"] button');
  await until(async () => (await text(page, '.wsp-pbody')).includes('export.go'));
  ok('reading a page: its text, who wrote it, the tile and revision', (await text(page, '.wsp-pmeta')).includes('about sales/orders at a41f9c2'));
  await page.click('.wsp-nb-h button:has-text("Edit")');
  await page.fill('.wsp-edit textarea', 'The export lives in export.go and export_csv.go.');
  await page.click('.wsp-edit button.primary');
  await until(async () => (await calls(page, 'PUT', /\/ws\/notebook\/orders-code$/)).length === 1);
  const put = JSON.parse((await calls(page, 'PUT', /\/ws\/notebook\/orders-code$/))[0].body);
  ok('an edit is saved from the version seen, its marks kept', put.base === 1 && put.body.includes('export_csv.go') && !('clearStamps' in put), JSON.stringify(put));
  await until(async () => (await text(page, '.wsp-pbody')).includes('export_csv.go'));
  await page.click('.wsp-pmeta button');
  ok('clearing the org marks asks first, and says what it means', (await text(page, '.wsp-confirm')).includes('Clear the marks of Sales? A session on any model could then read this page'));
  await page.click('.wsp-confirm button:has-text("Keep them")');
  ok('…Keep them changes nothing', (await calls(page, 'PUT', /\/ws\/notebook\/orders-code$/)).length === 1 && !(await page.$('.wsp-confirm')));
  await page.click('.wsp-pmeta button');
  await page.click('.wsp-confirm button.danger');
  await until(async () => (await calls(page, 'PUT', /\/ws\/notebook\/orders-code$/)).length === 2);
  ok('…confirmed: clearStamps, from the version seen', JSON.parse((await calls(page, 'PUT', /\/ws\/notebook\/orders-code$/))[1].body).clearStamps === true);
  await page.click('.wsp-nb-h button.danger');
  ok('Delete asks first', (await text(page, '.wsp-confirm')).includes('Delete this page? The agent forgets it'));
  await page.click('.wsp-confirm button.danger');
  await until(async () => (await calls(page, 'DELETE', /\/ws\/notebook\/orders-code$/)).length === 1);
  ok('…and deletes', await until(async () => (await page.$$('.wsp-pg')).length === 2));
  await page.click('.wsp-nb-h button:has-text("Add a note")');
  await page.fill('.wsp-edit input', 'Never email customers without asking');
  await page.fill('.wsp-edit textarea', 'Ana said so on 4 Nov.');
  await page.click('.wsp-edit button.primary');
  await until(async () => (await calls(page, 'PUT', /\/ws\/notebook\/never-email-customers-without-asking$/)).length === 1);
  ok('Add a note: a new page from its title, base 0', JSON.parse((await calls(page, 'PUT', /\/ws\/notebook\/never-email/))[0].body).base === 0);
  await page.click('.wsp-nb-h button[aria-label="back to the notebook"]');
  await until(async () => !!(await page.$('.wsp-pages')));
  await page.click('.wsp-nb-h button[aria-label="back to the conversation"]');
  ok('back to the conversation', await until(async () => !!(await page.$('bx-chat-composer'))));
  ok('no page errors (notebook)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

await browser.close();
done('ws-scope');
