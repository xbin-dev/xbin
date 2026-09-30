// harness-manage.mjs — coding agents for the tile's managers in the web view
// (D147 §8 U8) over the fixtures (test/harness-fixtures.mjs,
// STUB's §4 routes): ⚙ Classes — the Coding agents toolset (the built-in
// coding class has it), which coding agents a class allows ("all" or a
// checklist of the catalog's), and the backend's refusal said when the
// toolset is left without a sandbox, then saved once it is unticked; ⚙
// Coding agents — each coding agent, whether it is available and why not,
// its images, sandboxes, classes, modes and sign-in, and checking a running
// sandbox now.
//
//   node test/harness-manage.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';
import { harnessSeed } from './harness-fixtures.mjs';

const { ok, done } = checker();
const browser = await launch();
const ctx = await browser.newContext({ viewport: { width: 1100, height: 760 } });
await serveTile(ctx);
const seed = harnessSeed();
seed.classes = { classes: [], default: 'internal' }; // the built-ins as they ship
await ctx.addInitScript(STUB, seed);
await ctx.addInitScript(() => {
  window.__alerts = [];
  window.alert = (m) => window.__alerts.push(String(m));
  window.confirm = () => true;
});
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
const text = (sel) => page.textContent(sel).then((t) => (t || '').replace(/\s+/g, ' ').trim());
const puts = () => page.evaluate(() => window.__calls.filter((c) => c.method === 'PUT' && /\/classes$/.test(c.url)).map((c) => JSON.parse(c.body)));
const tab = async (name, sel) => { await page.click(`#tabs .tab[data-tab="${name}"]`); await page.waitForSelector(sel); };

await page.goto(`${ORIGIN}/`);
await page.waitForSelector('#gear:not([hidden])');
await page.click('#gear');
await tab('classes', '.clsrow');

// the built-in coding class carries the toolset — all of them
await page.click('[data-edit="coding"]');
await page.waitForSelector('#clf-save');
ok('classes: the Coding agents toolset is checked on coding', await page.$eval('[data-ts="harness"]', (e) => e.checked));
ok('…all of them', (await page.$eval('#clf-harnessesMode', (e) => e.value)) === 'all');
await page.selectOption('#clf-harnessesMode', 'only');
const names = await page.$$eval('[data-harness]', (els) => els.map((e) => e.dataset.harness).join());
ok('only these: the catalog\'s coding agents', names === 'claude,codex,gemini,opencode', names);
await page.check('[data-harness="claude"]');
await page.check('[data-harness="codex"]');
await page.click('#clf-save');
await page.waitForFunction(() => document.getElementById('cl-msg')?.textContent === 'saved ✓');
let p = (await puts()).pop();
const coding = (b) => b.classes.find((c) => c.id === 'coding');
ok('…saved as a list', JSON.stringify(coding(p).harnesses) === '["claude","codex"]' && coding(p).toolsets.includes('harness'), JSON.stringify(coding(p)));

// unticking the sandbox leaves the toolset lame: the form warns, the backend refuses and says why
await page.click('[data-edit="coding"]');
await page.waitForSelector('#clf-save');
ok('…read back as a list', (await page.$eval('#clf-harnessesMode', (e) => e.value)) === 'only'
  && (await page.$$eval('[data-harness]', (els) => els.filter((e) => e.checked).map((e) => e.dataset.harness).join())) === 'claude,codex');
await page.uncheck('[data-ts="sandbox"]');
ok('no sandbox: the form says what the toolset needs', (await text('#clf-harness-why')).startsWith('⚠ the harness toolset needs sandbox and an egress other than none'));
await page.click('#clf-save');
await page.waitForSelector('.clsform .err');
ok('…and the backend\'s refusal is said', (await text('.clsform .err')) === 'class coding: the harness toolset needs sandbox and an egress other than none — a coding agent must reach its provider',
  await text('.clsform .err'));
await page.uncheck('[data-ts="harness"]');
ok('unticking Coding agents too: no warning', !(await page.$('#clf-harness-why')));
await page.click('#clf-save');
await page.waitForFunction(() => document.getElementById('cl-msg')?.textContent === 'saved ✓');
p = (await puts()).pop();
ok('…it saves: neither toolset, no harnesses', !coding(p).toolsets.includes('harness') && !coding(p).toolsets.includes('sandbox') && !('harnesses' in coding(p)), JSON.stringify(coding(p)));
// egress none only: the same
await page.click('[data-edit="coding"]');
await page.check('[data-ts="sandbox"]');
await page.check('[data-ts="harness"]');
ok('a sandbox that reaches nothing: the same warning', (await text('#clf-harness-why')).startsWith('⚠ the harness toolset needs sandbox'));
await page.check('[data-eg="internet"]');
ok('…until it may reach the internet', !(await page.$('#clf-harness-why')));
await page.click('#clf-cancel');

// ⚙ Coding agents
await tab('harnesses', '.hrow');
const rows = await page.$$eval('.hrow', (els) => els.map((e) => ({ id: e.dataset.harness, t: e.textContent.replace(/\s+/g, ' ').trim() })));
const row = (id) => (rows.find((r) => r.id === id) || {}).t || '';
ok('coding agents: each of the catalog\'s', rows.map((r) => r.id).join() === 'claude,codex,gemini,opencode', rows.map((r) => r.id).join());
ok('…available, its images, sandboxes, classes', /^CC\s*Claude Code available/.test(row('claude')) && row('claude').includes('Coding sandboxes · base — internet, open network')
  && row('claude').includes('api-dev: installed · signed in') && row('claude').includes('Classes ▣ Coding'), row('claude'));
ok('…its modes and sign-in', row('claude').includes('default Ask before acting · Auto: Accept edits · plan: Plan · the owner only: Bypass permissions')
  && row('claude').includes('CLAUDE_CODE_REMOTE=1 claude /login'), row('claude'));
ok('…not signed in where it was', row('codex').includes('api-dev: installed · not signed in'), row('codex'));
ok('…one that isn\'t available says why', row('gemini').includes('not available needs internet access — Coding sandboxes offers none') && row('opencode').includes('Images none has it')
  && row('opencode').includes('no auto mode'), row('gemini') + ' | ' + row('opencode'));
await page.selectOption('#hc-ref', 'apps/coding-sandbox|sb-9c1d');
await page.click('#hc-check');
await page.waitForFunction(() => document.getElementById('hc-msg')?.textContent.includes('checked'));
ok('check a running sandbox now', (await text('#hc-msg')) === 'checked scratch ✓'
  && (await page.evaluate(() => window.__calls.some((c) => c.url.endsWith('/harnesses?probe=' + encodeURIComponent('apps/coding-sandbox|sb-9c1d'))))));
ok('…what it found is listed', (await text('.hrow[data-harness="claude"]')).includes('scratch: installed'), await text('.hrow[data-harness="claude"]'));

ok('no page errors', errors.length === 0, errors.join(' | '));
ok('nothing alerted', !(await page.evaluate(() => window.__alerts.length)), await page.evaluate(() => window.__alerts.join(' | ')));
await browser.close();
done('harness-manage');
