// web.mjs — the coding-sandbox page in a browser against the fake backend
// (stub.mjs, seeded from seed.mjs): the operators' Sandboxes tab (the
// metadata, start/stop/delete, snapshots, who may use each — shown, never
// changed —, usage, orphans), Images (builds, the build a failed rebuild
// keeps, the editor), Settings (mode, networks, sizes, quotas, mounts), and
// Yours — create, lifecycle, the file browser (browse, view, download,
// upload, a folder, remove), the terminal (<bx-terminal src> on the tile's
// own tty route; the element is stubbed) and sharing; a reader (read
// access) sees only theirs, read-only, and the page says why. SHOTS=<dir>
// also writes screenshots of each tab.
//
//   PLAYWRIGHT_DIR=~/lcad-wasm node test/web.mjs   (needs playwright + a chromium build; SKIPs without)
import { readFileSync, readdirSync, mkdirSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { STUB } from './stub.mjs';
import { SEED, READER, NOW } from './seed.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const tileDir = join(here, '..');
const vendor = process.env.BX_VENDOR || join(here, '..', '..', '..', 'web', 'vendor');
const ORIGIN = 'http://tile.test';
const THEME = '<style>:root{--bx-border:#ccc;--bx-panel:#fff;--bx-panel-2:#f4f4f4;--bx-text:#111;--bx-bg:#fafafa;' +
  '--bx-muted:#777;--bx-accent:#b57e10;--bx-mono:monospace;--bx-red:#c33;--bx-green:#3a3}</style>';
// the glyphs (<bx-icon>, D184): the kit's own file next to the vendored lit
// (in a checkout: web/bx-icons.js; BX_ICONS=<path> elsewhere), else an
// element that draws nothing — the page's words are what the test reads
const iconsAt = [process.env.BX_ICONS, join(vendor, '..', 'bx-icons.js')].filter(Boolean).find((p) => existsSync(p));
const ICONS = iconsAt ? readFileSync(iconsAt, 'utf8')
  : "customElements.define('bx-icon', class extends HTMLElement {}); export const hasIcon = () => true; export const ICON_NAMES = [];";
// the terminal element, stubbed: it keeps its src and says a session started
const TERM = `customElements.define('bx-terminal', class extends HTMLElement {
  connectedCallback() { this.textContent = 'terminal → ' + this.getAttribute('src');
    setTimeout(() => this.dispatchEvent(new CustomEvent('bx-session', { detail: { id: 'e77' } })), 10); } });`;

// playwright: PLAYWRIGHT_DIR=<a dir with node_modules/playwright>, a global
// install, or one next to the checkout
let chromium;
for (const at of [process.env.PLAYWRIGHT_DIR && `${process.env.PLAYWRIGHT_DIR}/node_modules/playwright/index.mjs`,
  '/usr/local/node/lib/node_modules/playwright/index.mjs', 'playwright'].filter(Boolean)) {
  try { ({ chromium } = await import(at)); break; } catch { /* the next */ }
}
if (!chromium) { console.log('SKIP: playwright not installed (PLAYWRIGHT_DIR=<dir with node_modules/playwright>)'); process.exit(0); }

let failures = 0;
const ok = (name, cond, extra = '') => {
  if (!cond) { console.log(`FAIL  ${name}  ← ${extra}`); failures++; }
  return cond;
};

const modules = new Set([...readdirSync(tileDir).filter((f) => f.endsWith('.js')),
  ...readdirSync(join(tileDir, 'model')).filter((f) => f.endsWith('.js')).map((f) => `model/${f}`)]);
async function serve(ctx) {
  await ctx.route(`${ORIGIN}/**`, (route) => {
    const path = new URL(route.request().url()).pathname.replace(/^\//, '') || 'index.html';
    if (path === 'vendor/lit-all.min.js') return route.fulfill({ contentType: 'text/javascript', body: readFileSync(join(vendor, 'lit-all.min.js'), 'utf8') });
    if (path === 'vendor/bx-terminal.js') return route.fulfill({ contentType: 'text/javascript', body: TERM });
    if (path === 'vendor/theme.css') return route.fulfill({ contentType: 'text/css', body: '' });
    if (path === 'vendor/bx-icons.js') return route.fulfill({ contentType: 'text/javascript', body: ICONS });
    if (path !== 'index.html' && !modules.has(path)) return route.fulfill({ status: 404, body: '' });
    let body = readFileSync(join(tileDir, path), 'utf8');
    if (path === 'index.html') body = body.replace('<link rel="stylesheet" href="/vendor/theme.css">', THEME);
    return route.fulfill({ contentType: path.endsWith('.js') ? 'text/javascript' : 'text/html', body });
  });
}

const shots = process.env.SHOTS || '';
if (shots) mkdirSync(shots, { recursive: true });
const shot = async (page, name) => { if (shots) await page.screenshot({ path: join(shots, `${name}.png`), fullPage: true }); };

const browser = await chromium.launch();
async function open(seed, { dialogs = [] } = {}) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  await serve(ctx);
  await ctx.addInitScript(STUB, seed);
  const page = await ctx.newPage();
  await page.clock.setFixedTime(NOW);
  const errors = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
  // dialogs answered in turn: true accepts, a string answers a prompt, false dismisses
  page.on('dialog', (d) => {
    const a = dialogs.length ? dialogs.shift() : true;
    if (a === false) d.dismiss(); else d.accept(typeof a === 'string' ? a : undefined);
  });
  await page.goto(`${ORIGIN}/index.html`);
  await page.waitForSelector('header.top');
  return { ctx, page, errors };
}
const calls = (page, method, re) => page.evaluate(([m, s]) => window.__calls.filter((c) => c.method === m && new RegExp(s).test(c.url)), [method, re.source]);
const lastBody = async (page, method, re) => { const c = await calls(page, method, re); return c.length ? JSON.parse(c[c.length - 1].body || 'null') : null; };
const text = (page, sel) => page.$eval(sel, (e) => e.textContent.replace(/\s+/g, ' ').trim()).catch(() => '');
const settle = (page) => page.waitForTimeout(120);

// --- an operator: Sandboxes ------------------------------------------------------------
{
  const dialogs = [];
  const { ctx, page, errors } = await open(SEED, { dialogs });
  await page.waitForSelector('#ops-table');
  const tabs = await page.$$eval('.tabs .tab', (b) => b.map((x) => x.textContent.trim()));
  ok('an operator has four tabs', tabs.join('|') === 'Sandboxes|Images|Settings|Yours', tabs.join('|'));
  const rows = await page.$$eval('#ops-table tr.sb', (r) => r.map((x) => x.dataset.id));
  ok('every sandbox of every consumer, most recently active first', rows.join(',') === 'sb-node,sb-api,sb-term,sb-own,sb-rusty,sb-web', rows.join(','));
  const api = await text(page, 'tr[data-id="sb-api"]');
  ok('a row carries its facts', ['api-dev', 'apps/agent', 'alice (asserted)', 'running', 'base', 'medium', 'internet', 'VM', '700 MiB', 'just now'].every((w) => api.includes(w)), api);
  ok('a creating sandbox says why', (await text(page, 'tr[data-id="sb-node"]')).includes('building the image node'));
  ok('the substrate: modes and what it offers', (await text(page, '#substrate')).includes('vm (kvm)'), await text(page, '#substrate'));
  await shot(page, 'ops');

  await page.click('tr[data-id="sb-api"] button[data-act="stop"]');
  await settle(page);
  ok('stop', (await calls(page, 'POST', /\/ops\/sandboxes\/sb-api\/stop\?wait=30$/)).length === 1);
  await page.click('tr[data-id="sb-web"] button[data-act="start"]');
  await settle(page);
  ok('start', (await calls(page, 'POST', /\/ops\/sandboxes\/sb-web\/start/)).length === 1);

  // snapshots
  await page.click('tr[data-id="sb-term"] button[data-act="snapshots"]');
  await page.waitForSelector('#snaps');
  await page.fill('#snap-name', 'before upgrade');
  await page.click('#snap-take');
  await page.waitForSelector('#snaps tr[data-snap]');
  ok('take a snapshot', (await lastBody(page, 'POST', /\/ops\/sandboxes\/sb-term\/snapshots$/))?.name === 'before upgrade');
  dialogs.push(true);
  await page.click('#snaps button[data-act="restore"]');
  await settle(page);
  ok('restore, confirmed', (await calls(page, 'POST', /\/snapshots\/s-\d+\/restore$/)).length === 1);
  dialogs.push(true);
  await page.click('#snaps button[data-act="drop"]');
  await settle(page);
  ok('delete a snapshot, confirmed', (await calls(page, 'DELETE', /\/ops\/sandboxes\/sb-term\/snapshots\/s-\d+$/)).length === 1);
  await shot(page, 'ops-snapshots');

  // Ports: whether the manager offers them, and a probe of one (a diagnostic, never the page)
  await page.click('tr[data-id="sb-term"] button[data-act="ports"]');
  await page.waitForSelector('#ports #ports-offered');
  ok('Ports: offered', (await text(page, '#ports-offered')).includes('offered'), await text(page, '#ports-offered'));
  await page.fill('#ports-port', '8000');
  await page.fill('#ports-path', '/index.html?x=1');
  await page.click('#ports-probe');
  await page.waitForSelector('#ports-result.ok');
  ok('a probe that answers: status, type, time', (await text(page, '#ports-result')).includes('HTTP 200 · text/html · 4 ms'), await text(page, '#ports-result'));
  ok('…of the port and path asked', (await calls(page, 'GET', /\/ports\/sb-term\/8000\?path=%2Findex\.html%3Fx%3D1$/)).length === 1);
  await page.fill('#ports-port', '5173');
  await page.click('#ports-probe');
  await page.waitForSelector('#ports-result.bad');
  ok('a refusal, and what to do', (await text(page, '#ports-result')).includes('start its server'), await text(page, '#ports-result'));
  await shot(page, 'ops-ports');

  // who may use it: shown, never changed by operators (its home consumer or its owner does)
  ok('who may use it', (await text(page, 'tr[data-id="sb-term"] .who')) === 'everyone its consumer serves · apps/agent (everyone it serves)',
    await text(page, 'tr[data-id="sb-term"] .who'));
  ok('no sharing control for operators', (await page.$$('#ops-table button[data-act="shares"], #ops-table #shares')).length === 0);
  ok('and no PATCH of who may use it', (await calls(page, 'PATCH', /\/ops\/sandboxes\//)).length === 0);

  // usage, orphans, delete
  ok('usage against the quota that binds each', (await text(page, '#usage tr[data-who="apps/agent"]')).includes('4 / 6'), await text(page, '#usage tr[data-who="apps/agent"]'));
  dialogs.push(true);
  await page.click('#orphans button');
  await settle(page);
  ok('an orphan deleted, confirmed', (await calls(page, 'DELETE', /\/ops\/orphans\/s7f7f7f7f7f7f$/)).length === 1);
  dialogs.push(false);
  await page.click('tr[data-id="sb-rusty"] button[data-act="delete"]');
  await settle(page);
  ok('a delete dismissed sends nothing', (await calls(page, 'DELETE', /\/ops\/sandboxes\/sb-rusty$/)).length === 0);
  dialogs.push(true);
  await page.click('tr[data-id="sb-rusty"] button[data-act="delete"]');
  await settle(page);
  ok('a delete, confirmed', (await calls(page, 'DELETE', /\/ops\/sandboxes\/sb-rusty$/)).length === 1 && !(await page.$('tr[data-id="sb-rusty"]')));

  // --- Images
  await page.click('#tab-images');
  await page.waitForSelector('#images');
  ok('images and their builds', (await text(page, '[data-image="node"]')).includes('built') && (await text(page, '[data-image="rust"]')).includes('Could not resolve host'),
    await text(page, '[data-image="rust"]'));
  ok('the base has nothing to build', !(await page.$('[data-image="base"] button[data-act="build"]')));
  ok('an image names its coding agents', (await text(page, '[data-image="base"] .agents')).includes('Claude Code, Codex'), await text(page, '[data-image="base"]'));
  ok('a failed rebuild keeps the previous build, and says so', (await text(page, '[data-image="rust"] .kept')).startsWith('The previous build (3 d ago)') &&
    (await text(page, '[data-image="rust"] .pill.warn')) === 'the rebuild failed', await text(page, '[data-image="rust"]'));
  await shot(page, 'images');
  await page.click('[data-image="rust"] button[data-act="build"]');
  await settle(page);
  ok('build now', (await calls(page, 'POST', /\/ops\/images\/rust\/build$/)).length === 1);
  await page.click('[data-image="node"] button[data-act="edit"]');
  await page.fill('#img-title', 'Node 24');
  await page.fill('#img-tools', 'git node pnpm yarn');
  await page.check('#img-sudo');
  await page.click('#img-save');
  await settle(page);
  const imgs = (await lastBody(page, 'PUT', /\/ops\/config$/))?.images;
  const node = imgs && imgs.find((i) => i.id === 'node');
  ok('edit an image', node && node.title === 'Node 24' && node.tools.join() === 'git,node,pnpm,yarn' && node.setup.includes('pnpm'), JSON.stringify(node));
  ok('an image\'s sudo', node && node.sudo === true && !imgs.find((i) => i.id === 'base').sudo, JSON.stringify(imgs));
  ok('the image says it gives sudo', !!(await page.$('[data-image="node"] [data-sudo]')) && !(await page.$('[data-image="base"] [data-sudo]')));
  await page.click('#image-new');
  await page.fill('#img-id', 'bad id!');
  await page.click('#img-save');
  ok('a bad id is refused before it is sent', (await text(page, '#ui-err')).includes('the id is'));
  await page.fill('#img-id', 'py');
  await page.fill('#img-setup', 'pip install uv');
  await page.check('#img-default');
  await page.click('#img-save');
  await settle(page);
  const imgs2 = (await lastBody(page, 'PUT', /\/ops\/config$/))?.images || [];
  ok('a new image, the default', imgs2.some((i) => i.id === 'py' && i.default && i.setup === 'pip install uv') && imgs2.filter((i) => i.default).length === 1, JSON.stringify(imgs2));
  dialogs.push(true);
  await page.click('[data-image="rust"] button[data-act="remove"]');
  await settle(page);
  ok('remove an image, confirmed', !((await lastBody(page, 'PUT', /\/ops\/config$/))?.images || []).some((i) => i.id === 'rust'));

  // --- Settings
  await page.click('#tab-settings');
  await page.waitForSelector('#mode');
  ok('the mode now', (await text(page, '#mode-now')).includes('new sandboxes are VMs'));
  ok('an unbound class says how to bind it', (await text(page, '#egress tr[data-class="open"]')).includes('bx bind apps/coding-sandbox open='));
  await shot(page, 'settings');
  await page.selectOption('#mode-pick', 'namespace');
  await page.click('#mode-save');
  await settle(page);
  ok('the mode', (await lastBody(page, 'PUT', /\/ops\/config$/))?.mode === 'namespace');
  await page.fill('#sizes tr[data-size="medium"] input[data-k="memMiB"]', '6144');
  await page.dispatchEvent('#sizes tr[data-size="medium"] input[data-k="memMiB"]', 'change');
  await page.click('#sizes-save');
  await settle(page);
  const sizes = (await lastBody(page, 'PUT', /\/ops\/config$/))?.sizes;
  ok('sizes', sizes && sizes.find((s) => s.id === 'medium').memMiB === 6144 && sizes.length === 3, JSON.stringify(sizes));
  await page.selectOption('#quota-kind', 'person');
  await page.fill('#quota-key', 'alice');
  await page.click('#quota-add');
  await page.fill('#quotas tr[data-quota="person:alice"] input[data-k="running"]', '5');
  await page.dispatchEvent('#quotas tr[data-quota="person:alice"] input[data-k="running"]', 'change');
  await page.click('#quotas-save');
  await settle(page);
  const quotas = (await lastBody(page, 'PUT', /\/ops\/config$/))?.quotas;
  ok('a person\'s own quota', quotas && quotas.people && quotas.people.alice.running === 5 && quotas.consumers['apps/agent'].sandboxes === 6, JSON.stringify(quotas));
  await page.fill('#mount-new', 'res:apps/coding-sandbox/cache:go /cache ro');
  await page.click('#mount-add');
  await page.fill('#autostop', '45');
  await page.dispatchEvent('#autostop', 'change');
  await page.click('#adv-save');
  await settle(page);
  const adv = await lastBody(page, 'PUT', /\/ops\/config$/);
  ok('mounts and the idle stop', adv && adv.autoStopMin === 45 && JSON.stringify(adv.mounts) === JSON.stringify([{ res: 'res:apps/coding-sandbox/cache', at: '/cache', path: 'go', ro: true }]), JSON.stringify(adv));
  ok('no page errors (operator)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

// --- an operator: Yours --------------------------------------------------------------------
{
  const dialogs = [];
  const { ctx, page, errors } = await open(SEED, { dialogs });
  await page.click('#tab-mine');
  await page.waitForSelector('#mine .card');
  ok('your sandboxes', (await page.$$eval('#mine .card', (c) => c.map((x) => x.dataset.id))).join() === 'sb-own,sb-team');
  ok('delete only on your own', !!(await page.$('[data-id="sb-own"] button[data-act="delete"]')) && !(await page.$('[data-id="sb-team"] button[data-act="delete"]')));
  await page.click('#new');
  await page.fill('#cf-name', 'scratch');
  await page.selectOption('#cf-image', 'node');
  await page.selectOption('#cf-egress', 'internet');
  await page.click('#cf-create');
  await page.waitForSelector('#detail');
  const made = await lastBody(page, 'POST', /\/sbx\/sandboxes\?wait=60$/);
  ok('create', made && made.name === 'scratch' && made.image === 'node' && made.size === 'small' && made.egress === 'internet' && made.visibility === 'private', JSON.stringify(made));
  await page.click('[data-id="sb-team"] button[data-act="start"]');
  await settle(page);
  ok('start one', (await calls(page, 'POST', /\/sbx\/sandboxes\/sb-team\/start/)).length === 1);

  // files
  await page.click('[data-id="sb-own"] button[data-act="open"]');
  await page.waitForSelector('#entries tr[data-name="README.md"]');
  ok('a directory, directories first', (await page.$$eval('#entries tr', (r) => r.map((x) => x.dataset.name))).join() === 'src,logo.bin,README.md');
  await page.click('#entries tr[data-name="README.md"] .link');
  await page.waitForSelector('#content');
  ok('a text file', (await text(page, '#content')).includes('run `make` to build.'));
  await page.click('#entries tr[data-name="logo.bin"] .link');
  await page.waitForSelector('#binary');
  ok('a binary file says so', true);
  await page.click('#entries tr[data-name="README.md"] button[data-act="download"]');
  await settle(page);
  ok('download', (await page.evaluate(() => window.__downloads)).some((d) => d.name === 'README.md' && d.size === 28), JSON.stringify(await page.evaluate(() => window.__downloads)));
  await page.setInputFiles('#upload', { name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('hello there') });
  await page.waitForSelector('#entries tr[data-name="notes.txt"]');
  const up = (await calls(page, 'PUT', /\/files\/content\?path=%2Fwork%2Fnotes.txt&mkdirs=1$/))[0];
  ok('upload', up && up.body === 'hello there', JSON.stringify(up));
  dialogs.push('build');
  await page.click('#mkdir');
  await page.waitForSelector('#entries tr[data-name="build"]');
  ok('a new folder', (await lastBody(page, 'POST', /\/files\/mkdir$/))?.path === '/work/build');
  dialogs.push(true);
  await page.click('#entries tr[data-name="src"] button[data-act="remove"]');
  await settle(page);
  const rm = await lastBody(page, 'POST', /\/files\/remove$/);
  ok('remove a directory, confirmed', rm && rm.path === '/work/src' && rm.recursive === true, JSON.stringify(rm));
  await page.fill('#path', '/work/src');
  await page.press('#path', 'Enter');
  await settle(page);
  ok('a path typed', (await calls(page, 'GET', /\/files\/list\?path=%2Fwork%2Fsrc$/)).length === 1);
  await page.click('#up');
  await page.waitForSelector('#entries tr[data-name="README.md"]');
  await shot(page, 'yours-files');

  // the terminal
  await page.click('#sub-term');
  await page.waitForSelector('#term bx-terminal');
  const src = await page.$eval('#term bx-terminal', (e) => e.getAttribute('src'));
  ok('a terminal on the tile\'s own tty route', src === '/api/apps/coding-sandbox/sbx/sandboxes/sb-own/tty?cwd=%2Fwork', src);
  await page.waitForTimeout(60);
  await shot(page, 'yours-terminal');
  await page.click('#sub-files');
  await page.click('#sub-term');
  ok('another tab and back keeps the shell', (await page.$$('#term bx-terminal')).length === 1 &&
    (await calls(page, 'DELETE', /\/execs\//)).length === 0);
  await page.click('#term-end');
  await settle(page);
  ok('End ends its shell', (await calls(page, 'DELETE', /\/sbx\/sandboxes\/sb-own\/execs\/e77$/)).length === 1);

  // sharing
  await page.click('#sub-share');
  await page.selectOption('#vis-pick', 'team');
  await settle(page);
  ok('visibility', (await lastBody(page, 'PATCH', /\/sbx\/sandboxes\/sb-own$/))?.visibility === 'team');
  await page.fill('#share-consumer', 'apps/sandbox-terminal');
  await page.click('#share-add');
  await settle(page);
  ok('share yours', JSON.stringify((await lastBody(page, 'PATCH', /\/sbx\/sandboxes\/sb-own$/))?.shares) === JSON.stringify([{ consumer: 'apps/sandbox-terminal', users: '*' }]));

  // ports: this one's agent predates them — said, with what to do
  await page.evaluate(() => window.__route('GET', /\/ports\/sb-own$/, () => window.__json({ offered: true, restartNeeded: true,
    why: 'the sandbox\'s agent predates ports (it started under an older xbind, or from an older VM image): restart the sandbox' })));
  await page.click('#sub-ports');
  await page.waitForSelector('#ports #ports-offered');
  ok('Ports on yours: a restart needed, and why', (await text(page, '#ports-offered')).includes('restart needed') &&
    (await text(page, '#ports-offered')).includes('restart the sandbox'), await text(page, '#ports-offered'));
  dialogs.push(true);
  await page.click('[data-id="sb-own"] button[data-act="delete"]');
  await settle(page);
  ok('delete yours, confirmed', (await calls(page, 'DELETE', /\/sbx\/sandboxes\/sb-own$/)).length === 1 && !(await page.$('#detail')));
  ok('no page errors (yours)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

// --- someone who isn't an operator: read access, so they look -------------------------------------
{
  const { ctx, page, errors } = await open(READER);
  await page.waitForSelector('#mine .card');
  const tabs = await page.$$eval('.tabs .tab', (b) => b.map((x) => x.textContent.trim()));
  ok('a reader sees only theirs', tabs.join('|') === 'Your sandboxes', tabs.join('|'));
  ok('and never asks for the operators\' state', (await calls(page, 'GET', /\/ops\//)).length === 0);
  ok('the page says they may only look, and why', (await text(page, '#readonly-note')).includes('write access'), await text(page, '#readonly-note'));
  ok('no New sandbox, saying why', await page.$eval('#new', (b) => b.disabled && b.title === 'making a sandbox needs write access to this tile') &&
    (await text(page, '#new + .muted')).includes('write access'));
  ok('no lifecycle, no delete', (await page.$$('#mine .card button[data-act]:not([data-act="open"])')).length === 0);
  // a running one: its files to read and download, nothing to change
  await page.click('[data-id="sb-own"] button[data-act="open"]');
  await page.waitForSelector('#entries tr[data-name="README.md"]');
  ok('its files, read-only', !(await page.$('#upload-label')) && !(await page.$('#mkdir')) && !(await page.$('#entries button[data-act="remove"]')) &&
    !!(await page.$('#entries tr[data-name="README.md"] button[data-act="download"]')));
  await page.click('#entries tr[data-name="README.md"] .link');
  await page.waitForSelector('#content');
  ok('a file to read', (await text(page, '#content')).includes('run `make` to build.'));
  ok('no terminal, no sharing, no ports, saying why', await page.$eval('#sub-term', (b) => b.disabled && b.title === 'a terminal needs write access to this tile') &&
    await page.$eval('#sub-share', (b) => b.disabled && /write access/.test(b.title)) && await page.$eval('#sub-ports', (b) => b.disabled && /write access/.test(b.title)));
  await shot(page, 'reader');
  // a stopped one: reading it would start it
  await page.click('[data-id="sb-team"] button[data-act="open"]');
  await page.waitForSelector('#no-files');
  ok('a stopped one says why its files wait', (await text(page, '#no-files')) === 'No files: it is stopped, and starting it needs write access to this tile.', await text(page, '#no-files'));
  ok('nothing was changed', (await page.evaluate(() => window.__calls.filter((c) => c.method !== 'GET').length)) === 0);
  ok('no page errors (reader)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

await browser.close();
console.log(failures ? `\n${failures} FAILURE(S)` : 'all coding-sandbox web checks passed');
process.exit(failures ? 1 : 0);
