// settings.mjs — the ⚙ panel's tabs (managers): Config (model tiers, the
// system prompt, limits; Save sends the whole config back), Features (a
// switch merges into the config), Memory (set, add, delete a run's blocks),
// Files (edit and save a text file with its version), Skills (list, edit,
// save, delete) and MCP — the calls model/actions.js makes for both views.
//
//   node test/settings.mjs     (needs playwright + a chromium build)
//   SHOTS=<dir> node test/settings.mjs   also screenshots each tab
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const seed = {
  runs: [{ id: 1, title: 'plan the quarter', status: 'idle', parentId: 0, rootId: 1, activityMs: 1000 }],
  views: { 1: { access: 'owner', run: { id: 1, title: 'plan the quarter', status: 'idle', parentId: 0, rootId: 1 },
    memory: { goal: 'ship it' }, files: [{ path: 'notes.md', bytes: 5, version: 3, mime: 'text/markdown' }] } },
  me: { kind: 'user', user: 'admin', level: 'terminal', manager: true, epochMs: 0 },
};

const browser = await launch();
const ctx = await browser.newContext({ viewport: { width: 1100, height: 760 } });
await serveTile(ctx);
await ctx.addInitScript(STUB, seed);
await ctx.addInitScript(() => {
  const S = window.__settings = {
    config: { models: { general: 'm-big' }, system: 'be brief', tokenBudget: 1000, maxIters: 5, toolTimeout: 30, subagents: true,
      approve: false, features: { recall: true }, mcp: [{ name: 'x' }] },
    memory: { goal: 'ship it' },
    files: { 'notes.md': { content: 'hello', version: 3 } },
    skills: [{ name: 'triage', description: 'sort the inbox', content: 'step 1', updated: 1700000000 }],
  };
  const j = window.__json;
  window.__route('GET', /\/config$/, () => j(JSON.parse(JSON.stringify(S.config))));
  window.__route('PUT', /\/config$/, (m, o) => { S.config = JSON.parse(o.body); return j({ ok: 'true' }); });
  window.__route('GET', /\/models$/, () => j({ data: [{ id: 'm-big' }, { id: 'm-small' }] }));
  window.__route('GET', /\/features$/, () => j({ keys: ['recall', 'skills', 'vision'], features: S.config.features }));
  window.__route('GET', /\/runs\/1$/, () => j({ run: { id: 1 }, memory: S.memory }));
  window.__route('PUT', /\/runs\/1\/memory$/, (m, o) => { const b = JSON.parse(o.body); S.memory[b.key] = b.value; return j({ ok: 'true' }); });
  window.__route('DELETE', /\/runs\/1\/memory\?key=(.*)$/, (m) => { delete S.memory[decodeURIComponent(m[1])]; return j({ ok: 'true' }); });
  window.__route('GET', /\/runs\/1\/files$/, () => j(Object.entries(S.files).map(([path, f]) => ({ path, bytes: f.content.length, version: f.version, mime: 'text/markdown' }))));
  window.__route('GET', /\/runs\/1\/file\?path=(.*)$/, (m) => { const p = decodeURIComponent(m[1]); return j({ path: p, ...S.files[p] }); });
  window.__route('PUT', /\/runs\/1\/file$/, (m, o) => {
    const b = JSON.parse(o.body);
    const cur = S.files[b.path];
    if (cur && b.version && b.version !== cur.version) return j({ error: 'version conflict' }, 409);
    S.files[b.path] = { content: b.content, version: (cur ? cur.version : 0) + 1 };
    return j({ path: b.path, version: S.files[b.path].version, bytes: b.content.length });
  });
  window.__route('GET', /\/skills$/, () => j(S.skills));
  window.__route('PUT', /\/skills$/, (m, o) => {
    const b = JSON.parse(o.body);
    S.skills = [...S.skills.filter((s) => s.name !== b.name), { ...b, updated: 1700000000 }];
    return j({ ok: 'true' });
  });
  window.__route('DELETE', /\/skills\/(.*)$/, (m) => { S.skills = S.skills.filter((s) => s.name !== decodeURIComponent(m[1])); return j({ ok: 'true' }); });
  window.alert = (msg) => { window.__alerted = msg; };
  window.confirm = () => true;
});
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
await page.goto(`${ORIGIN}/#c=1`);
await page.waitForFunction(() => document.querySelector('#top .title')?.textContent === 'plan the quarter');
const S = () => page.evaluate(() => window.__settings);
const shot = async (name) => { if (process.env.SHOTS) await page.screenshot({ path: `${process.env.SHOTS}/settings-${name}.png` }); };
// redrawn: do something that re-renders the tab, and wait until it has
const redrawn = async (fn) => {
  await page.evaluate(() => { document.getElementById('sbd').firstElementChild.dataset.old = '1'; });
  await fn();
  await page.waitForFunction(() => { const e = document.getElementById('sbd').firstElementChild; return e && !e.dataset.old && !e.classList.contains('empty'); });
};
const tab = async (name, sel) => {
  await page.click(`#tabs .tab[data-tab="${name}"]`);
  await page.waitForSelector(sel);
};

// Config: the tiers list llm-gw's models; Save sends the whole config back
await page.click('#gear');
await page.waitForSelector('#cf-save');
ok('config: the tiers offer the listed models', (await page.$$eval('#cf-general option', (os) => os.map((o) => o.textContent))).join('|') === '— llm-gw default —|m-big|m-small');
ok('config: the current tier is chosen', (await page.$eval('#cf-general', (s) => s.value)) === 'm-big');
await shot('config');
await page.selectOption('#cf-code', 'm-small');
await page.fill('#cf-system', 'be very brief');
await page.click('#cf-save');
await page.waitForFunction(() => document.getElementById('cf-msg')?.textContent === 'saved ✓');
let s = await S();
ok('config: Save sends the whole config, untouched parts included', s.config.models.code === 'm-small' && s.config.system === 'be very brief' &&
  s.config.features.recall === true && s.config.mcp.length === 1 && s.config.subagents === true, JSON.stringify(s.config));

// Features: a switch merges into the config
await tab('features', '[data-f="vision"]');
await shot('features');
ok('features: the switches say what is on', await page.$eval('[data-f="recall"]', (c) => c.checked) && !(await page.$eval('[data-f="vision"]', (c) => c.checked)));
await page.click('[data-f="vision"]');
await page.waitForFunction(() => window.__settings.config.features.vision === true);
s = await S();
ok('features: one switch merges, the rest stays', s.config.features.recall === true && s.config.system === 'be very brief', JSON.stringify(s.config));

// Memory: set, add, delete
await tab('memory', '#madd');
await shot('memory');
await page.fill('[data-v="0"]', 'ship it today');
await redrawn(() => page.click('[data-set="0"]'));
ok('memory: set', (await S()).memory.goal === 'ship it today');
await page.fill('#mk', 'owner');
await page.fill('#mv', 'alice');
await redrawn(() => page.click('#madd'));
ok('memory: added', (await page.$$('[data-del]')).length === 2);
await page.click('[data-del="0"]');
await page.waitForFunction(() => !('goal' in window.__settings.memory));
ok('memory: set, add and delete reach the run', JSON.stringify((await S()).memory) === '{"owner":"alice"}', JSON.stringify((await S()).memory));

// Files: edit a text file; the version loaded goes back
await tab('files', '[data-fe="0"]');
await page.click('[data-fe="0"]');
await page.waitForFunction(() => document.getElementById('fl-body')?.value === 'hello');
await shot('files');
await page.fill('#fl-body', 'hello world');
await redrawn(() => page.click('#fl-save'));
ok('files: saved', (await S()).files['notes.md'].content === 'hello world');
const put = (await page.evaluate(() => window.__calls.filter((c) => c.method === 'PUT' && /\/runs\/1\/file$/.test(c.url)).map((c) => JSON.parse(c.body))))[0];
ok('files: Save sends the version it loaded', put && put.version === 3 && put.path === 'notes.md', JSON.stringify(put));

// Skills: list, new, save, delete
await tab('skills', '#sk-save');
await shot('skills');
ok('skills: the library is listed', (await page.textContent('#sbd')).includes('sort the inbox'));
await page.fill('#sk-name', 'weekly');
await page.fill('#sk-desc', 'the weekly report');
await page.fill('#sk-content', 'gather, write, send');
await redrawn(() => page.click('#sk-save'));
ok('skills: saved', (await S()).skills.some((k) => k.name === 'weekly') && (await page.$$('[data-skdel]')).length === 2);
await page.click('[data-skdel="0"]');
await page.waitForFunction(() => !window.__settings.skills.some((k) => k.name === 'triage'));
ok('skills: saved and deleted', (await S()).skills.map((k) => k.name).join() === 'weekly');

await tab('mcp', '.sec h4');
ok('mcp: says none are bound', (await page.textContent('#sbd')).includes('No MCP servers bound'));
ok('no page errors', errors.length === 0, errors.join('; '));
ok('nothing alerted', !(await page.evaluate(() => window.__alerted)), await page.evaluate(() => window.__alerted));
await browser.close();
done('settings');
