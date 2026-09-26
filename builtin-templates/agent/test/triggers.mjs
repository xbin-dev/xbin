// triggers.mjs — event triggers on the Automations page (D87): a trigger's
// card and detail (a bus trigger still missing its grant shows the exact
// `uses` entry; its recent events say why one didn't run), test fire, a push
// nothing took offering "Create a trigger", and the form keeping the lane
// firewall (the web lane or announcing needs public data).
//
//   node test/triggers.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const now = Date.now();
const seed = {
  runs: [{ id: 1, title: 'a chat', status: 'idle', parentId: 0, rootId: 1, activityMs: now }],
  automations: [
    { kind: 'trigger', id: 3, name: 'calendar changes', owner: 'admin', access: 'owner', attention: 1, enabled: true, mode: 'persistent',
      lastStatus: 'needs-grant: 403 apps/agent needs role "reader" on res:apps/cal/bus', summary: 'an event on res:apps/cal/bus · one ongoing thread',
      config: { name: 'calendar changes', source: 'bus', sourceRef: 'res:apps/cal/bus', match: 'events/', goal: 'Note what changed',
        mode: 'persistent', toolset: 'private', dataClass: 'private', maxPerHour: 30, visibility: 'private' } },
    { kind: 'channel', id: 8, name: 'Support bot', owner: 'admin', access: 'owner', enabled: true, summary: 'Slack · Beta', config: { state: 'active' } },
  ],
};

const browser = await launch();
const ctx = await browser.newContext();
await serveTile(ctx);
await ctx.addInitScript(STUB, seed);
await ctx.addInitScript((t) => {
  window.__route('GET', /\/triggers\/3\/events$/, () => window.__json({ events: [
    { eventId: 'bus:a', topic: 'events/created', accepted: true, runId: 40, at: t / 1000 - 60 },
    { eventId: 'bus:b', topic: 'events/moved', accepted: false, reason: 'rate', at: t / 1000 - 30 }] }));
  window.__route('GET', /\/triggers\/unmatched$/, () => window.__json({ items: [{ from: 'apps/webhooks', name: 'deploy', count: 2, at: t / 1000 }] }));
  window.__route('POST', /\/triggers\/3\/test$/, () => window.__json({ trigger: 'calendar changes', accepted: true, runId: 41 }));
  window.__route('POST', /\/triggers$/, (m, o) => window.__json({ id: 9, ...JSON.parse(o.body) }));
  window.__route('GET', /\/channels\/8\/sessions$/, () => window.__json({ sessions: [{ key: 'chan:8:dm:U2', runId: 31 }] }));
  window.__route('GET', /\/channels\/8\/(peers|outbox)/, () => window.__json({ peers: [], items: [] }));
}, now);
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
const called = (method, tail) => page.evaluate(([m, t]) => window.__calls.filter((c) => c.method === m && c.url.split('?')[0].endsWith(t)).map((c) => c.body), [method, tail]);
await page.goto(`${ORIGIN}/#auto`);
await page.waitForSelector('.acard2[data-auto="trigger:3"]');
ok('a trigger missing its grant says so', (await page.textContent('.acard2[data-auto="trigger:3"]')).includes('needs a grant'));
ok('a push nothing took offers a trigger', (await page.textContent('.autos-page')).includes('apps/webhooks sent deploy'));

await page.click('.acard2[data-auto="trigger:3"]');
await page.waitForSelector('.autos-page .agoal');
const detail = await page.textContent('.autos-page');
ok('the detail names the grant it needs', detail.includes('{ "target": "res:apps/cal/bus", "role": "reader" }'), detail.slice(0, 300));
ok('its events say why one didn\'t run', detail.includes('over its hourly cap') && detail.includes('ran #40'));
await page.click('.autos-page button:has-text("Test")');
await page.waitForSelector('.autos-page .said');
ok('test fires it', (await page.textContent('.autos-page .said')).includes('Fired a test event'));

// create one from the unmatched push
await page.click('.autos-page .crumb');
await page.click('.chrow button:has-text("Create a trigger")');
await page.waitForSelector('.autos-page textarea');
ok('the form starts from the push', (await page.inputValue('.autos-page input[placeholder="deploys"]')) === 'deploy'
  && (await page.inputValue('.autos-page input[placeholder="apps/webhooks"]')) === 'apps/webhooks');
await page.fill('.autos-page textarea', 'Check the {{topic}} deploy');
await page.selectOption('.autos-page select >> nth=2', 'web');
await page.waitForSelector('.autos-page .err');
ok('the web lane with private data is refused before saving', await page.isDisabled('.autos-page button:has-text("Create")'));
await page.selectOption('.autos-page select >> nth=3', 'public');
await page.selectOption('.autos-page select >> nth=4', 'chan:8:dm:U2');
ok('public data unblocks it', !(await page.isDisabled('.autos-page button:has-text("Create")')));
await page.click('.autos-page button:has-text("Create")');
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'POST' && c.url.endsWith('/triggers')));
const b = JSON.parse((await called('POST', '/triggers'))[0]);
ok('the form sends the trigger', b.name === 'deploy' && b.source === 'push' && b.sourceRef === 'apps/webhooks' && b.match === 'deploy'
  && b.toolset === 'web' && b.dataClass === 'public' && b.deliver === 'chan:8:dm:U2' && b.goal.includes('{{topic}}'), JSON.stringify(b));

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('triggers');
