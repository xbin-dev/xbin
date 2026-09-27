// native.mjs — the native view (native.js) in a real browser, as the app
// runs it: a runtime document with /vendor/xb-native.js, the tile's
// native.js, and the reference renderer playing the app (the preview host,
// /vendor/xb/preview-host.js). Against backend.mjs's fake backend and its
// live stream, it walks what a person does: home, the drawer, a
// conversation, a streamed answer, an approval, typing and sending, the
// menu's Files, back, a coding sandbox (the toolbar's picker, ▣, ⋯ → Sandbox,
// Manage) — and checks that nothing errs and no diagnostic is
// raised. The node tests (hack/agent-template-native.test.mjs) cover the
// semantics screen by screen; this proves the same code in WebKit's shoes
// (real frames, fetch streaming, DOMParser for the render preview).
//
//   node test/native.mjs        (needs playwright + a chromium build, and
//                                the xbin checkout's web/ for /vendor/xb*)
import { readFileSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const WEB = process.env.BX_WEB || join(here, '..', '..', '..', 'web');
if (!existsSync(join(WEB, 'xb-native.js'))) {
  console.log('SKIP: /vendor/xb-native.js not found next to the template (set BX_WEB=<xbin checkout>/web)');
  process.exit(0);
}
const { ok, done } = checker();
const now = Math.floor(Date.now() / 1000);
const call = (id, name, args) => ({ id, type: 'function', function: { name, arguments: JSON.stringify(args) } });
const msg = (id, role, content, extra = {}) => ({ id, runId: extra.runId || 1, seq: id, role, content, created: now - 60 + id, ...extra });
const SBX = 'apps/coding-sandbox';
const sbx = (id, extra = {}) => ({ ref: `${SBX}|${id}`, provider: SBX, manager: 'Coding sandboxes', id, name: id, state: 'running', egress: 'none',
  visibility: 'private', owner: { user: 'admin' }, mine: true, canUse: true, canManage: true, canEdit: true, workdir: '/work', ...extra });
const box = (id) => ({ ref: `${SBX}|${id}`, name: id, cwd: '/work', manager: 'Coding sandboxes', egress: 'none' });

const seed = {
  me: { kind: 'user', user: 'admin', manager: true, epochMs: 0 },
  needs: [{ reason: 'approval', run: { id: 3, title: 'send the invoices' } }],
  runs: [
    { id: 1, title: 'plan the quarter', status: 'running', parentId: 0, rootId: 1 },
    { id: 3, title: 'send the invoices', status: 'waiting_input', parentId: 0, rootId: 3 },
    { id: 5, title: 'fix the build', status: 'idle', parentId: 0, rootId: 5 },
  ],
  views: {
    1: {
      access: 'owner', files: [{ path: 'q3.html' }],
      run: { id: 1, title: 'plan the quarter', status: 'running', parentId: 0, rootId: 1 },
      messages: [msg(1, 'user', 'plan it'), msg(2, 'assistant', 'On it — **three** things:\n\n1. invoices\n2. vendors\n3. hiring',
        { toolCalls: [call('c1', 'xbin_call', { method: 'GET', path: '/api/apps/books/invoices', summary: 'Look up open invoices' })] }),
      msg(3, 'tool', 'HTTP 200', { toolCallId: 'c1', name: 'xbin_call' })],
    },
    3: {
      access: 'owner',
      run: { id: 3, title: 'send the invoices', status: 'waiting_input', rootId: 3, pendingState: { kind: 'approval', park: 'p3', toolCalls: [{ function: { name: 'mcp:mail:send' } }] } },
      messages: [msg(1, 'user', 'send them', { runId: 3 })],
    },
    // a coding conversation (D115) working in a sandbox
    5: {
      access: 'owner', class: { id: 'coding', name: 'Coding', icon: '▣', toolsets: ['sandbox', 'web'], managers: 'all', sandboxEgress: ['none'] },
      config: { sandbox: box('api'), attached: [box('api')] },
      run: { id: 5, title: 'fix the build', status: 'idle', parentId: 0, rootId: 5 },
      messages: [msg(1, 'user', 'make the tests pass', { runId: 5 }),
        msg(2, 'assistant', '', { runId: 5, toolCalls: [call('b1', 'bash', { command: 'go test ./...', summary: 'Run the tests' })] }),
        msg(3, 'tool', 'FAIL\n[exit 1 · 14s · job 3]', { runId: 5, toolCallId: 'b1', name: 'bash' })],
    },
  },
  sandboxes: [sbx('api', { boundTo: [5] }), sbx('web', { state: 'stopped' })],
};

const PAGE = `<!doctype html><html><head><meta charset="utf-8"><meta name="xbin-sandbox"><meta name="xbin-native-preview" content="1">
<script type="module">
import '/vendor/xb-native.js';
await import('/vendor/xb/preview-host.js');
await import('/native.js');
</script></head><body></body></html>`;

const browser = await launch();
const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } });
await serveTile(ctx, { realMarked: true });
await ctx.route('**/vendor/xb-native.js', (r) => r.fulfill({ contentType: 'text/javascript', body: readFileSync(join(WEB, 'xb-native.js'), 'utf8') }));
await ctx.route('**/vendor/xb/**', (r) => {
  const name = new URL(r.request().url()).pathname.replace(/^\/vendor\/xb\//, '');
  const f = join(WEB, 'xb', name);
  if (name.includes('..') || !existsSync(f)) return r.fulfill({ status: 404, body: '' });
  return r.fulfill({ contentType: name.endsWith('.js') ? 'text/javascript' : 'text/html', body: readFileSync(f, 'utf8') });
});
await ctx.route(`${ORIGIN}/native.html`, (r) => r.fulfill({ contentType: 'text/html', body: PAGE }));
await ctx.addInitScript(STUB, seed);
await ctx.addInitScript(() => {
  window.__route('GET', /\/runs\/1\/files$/, () => window.__json([{ path: 'q3.html', mime: 'text/html', bytes: 64, version: 2 }]));
  window.__route('GET', /\/runs\/1\/file\?path=q3\.html$/, () => window.__json({ path: 'q3.html', version: 2,
    content: '<h1>Q3 plan</h1><img src="https://evil.example/beacon.png"><meta http-equiv="refresh" content="0;url=https://evil.example/x">' }));
});
const escaped = [];
await ctx.route('**/*evil.example*/**', (r) => { escaped.push(r.request().url()); return r.abort(); });
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
await page.goto(`${ORIGIN}/native.html`);
await page.waitForFunction(() => window.xbnPreview);
ok('the native view draws', (await page.evaluate(() => window.xbnPreview.ready)).ok === true);
const view = page.locator('xb-view');
const shown = (sel) => view.locator(sel).first().waitFor({ state: 'visible', timeout: 5000 }).then(() => true, () => false);
// the nav is the root, or the first node of a root fragment (a sheet laid over it)
await page.evaluate(() => {
  window.navOf = () => { const r = window.xbnPreview.tree().root; return r.t === 'nav' ? r : r.c.find((c) => c.t === 'nav'); };
  window.topTitle = () => { const nav = window.navOf(); return nav.c[nav.c.length - 1].p.title; };
});
const topTitle = () => page.evaluate(() => window.topTitle());
const titled = (t) => page.waitForFunction((x) => window.topTitle() === x, t).then(() => true, () => false);

// home: the greeting and what needs you
ok('home: the greeting', await shown('xb-text:has-text("What do you need?")'));
ok('home: Needs you', await shown('xb-row:has-text("send the invoices")'));

// the drawer: open it, pick a conversation
await view.locator('button[aria-label="Conversations"]').first().click();
ok('the drawer comes from the leading edge', await shown('xb-sheet.drawer'));
await view.locator('xb-sheet xb-row:has-text("plan the quarter") .row-main').click();
ok('the conversation opens', await titled('plan the quarter'), await topTitle());
ok('its tool call is a card', await shown('xb-toolcard:has-text("Look up open invoices")'));
ok('its answer is markdown', await shown('xb-message .md li:has-text("vendors")'));

// streaming: a draft and its deltas
await page.evaluate(() => {
  window.__push({ type: 'text', run: 1, root: 1, data: { text: 'Streaming ' } });
  window.__push({ type: 'text.delta', run: 1, root: 1, data: { delta: 'nicely', at: 10 } });
});
ok('a streamed answer grows by deltas', await shown('xb-message .md:has-text("Streaming nicely")'));
ok('the stream asked for deltas', await page.evaluate(() => window.__calls.some((c) => c.url.includes('/stream?') && c.url.includes('deltas=1'))));

// typing and sending
await view.locator('xb-composer textarea').fill('also hiring');
await view.locator('xb-composer textarea').press('Enter');
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'POST' && c.url.endsWith('/runs/1/message')));
const sent = await page.evaluate(() => JSON.parse(window.__calls.filter((c) => c.url.endsWith('/runs/1/message')).pop().body));
ok('Send posts the message', sent.text === 'also hiring', JSON.stringify(sent));
ok('…and the composer empties', await page.waitForFunction(() => window.xbnPreview.view.shadowRoot.querySelector('xb-composer textarea').value === '').then(() => true, () => false));

// the menu: Files, then back
await view.locator('xb-menu button[aria-label="More"]:visible').first().click();
await view.locator('.pop button:has-text("Files (1)")').click();
ok('Files is pushed', await titled('Files'));
// an HTML file renders as a no-script island that loads nothing external
await view.locator('xb-row:has-text("q3.html") .row-more').click();
await view.locator('.pop button:has-text("Render")').click();
ok('the render preview is pushed', await titled('q3.html'));
ok('it draws the file', await page.waitForFunction(() => {
  const f = window.xbnPreview.view.shadowRoot.querySelector('xb-canvas iframe');
  return f && f.contentDocument === null && f.srcdoc.includes('Q3 plan');
}).then(() => true, () => false));
const doc = await page.evaluate(() => window.xbnPreview.view.shadowRoot.querySelector('xb-canvas iframe').srcdoc);
ok('with the CSP first (DOMParser, as in WebKit)', /^<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="default-src 'none';/.test(doc), doc.slice(0, 120));
ok('the refresh is gone', !/http-equiv="refresh"/i.test(doc));
ok('it says what it blocked', await shown('xb-notice:has-text("2 external resources blocked")'));
await page.waitForTimeout(500);
ok('nothing escaped the island', escaped.length === 0, escaped.join(' '));
await view.locator('.back:visible').click();
await view.locator('.back:visible').click();
ok('back pops it', await page.waitForFunction(() => window.navOf().c.length === 1).then(() => true, () => false));

// an approval: Approve reaches the backend
await view.locator('button[aria-label="Conversations"]').first().click();
await view.locator('xb-sheet xb-row:has-text("send the invoices") .row-main').click();
ok('the approval card', await shown('xb-approval:has-text("mcp:mail:send")'));
await view.locator('xb-approval button:has-text("Approve")').click();
ok('Approve is sent, naming the ask', await page.waitForFunction(() => window.__calls.some((c) => c.url.endsWith('/runs/3/approve') && JSON.parse(c.body).approve === true && JSON.parse(c.body).park === 'p3')).then(() => true, () => false));

// a coding sandbox (D115): ▣ in the subtitle, the card's outcome, the toolbar's picker, ⋯ → Sandbox, Manage
const subtitle = () => page.evaluate(() => window.navOf().c[window.navOf().c.length - 1].p.subtitle || '');
await view.locator('button[aria-label="Conversations"]').first().click();
await view.locator('xb-sheet xb-row:has-text("fix the build") .row-main').click();
ok('a coding conversation opens', await titled('fix the build'));
ok('▣ its sandbox in the subtitle', await page.waitForFunction(() => /▣ api · \/work/.test(window.topTitle() && window.navOf().c.at(-1).p.subtitle)).then(() => true, () => false), await subtitle());
ok('the bash card says what it came to', await shown('xb-toolcard:has-text("exit 1 · 14s · job 3")'));
await view.locator('select[aria-label="Sandbox"]').selectOption({ label: 'web (stopped)' });
ok('the picker binds from the next turn', await page.waitForFunction(() => window.__calls.some((c) => c.method === 'PATCH' && c.url.endsWith('/runs/5')
  && JSON.parse(c.body).sandbox?.ref === 'apps/coding-sandbox|web')).then(() => true, () => false));
ok('…and the subtitle follows', await page.waitForFunction(() => /▣ web · \/work/.test(window.navOf().c.at(-1).p.subtitle)).then(() => true, () => false), await subtitle());
await view.locator('xb-menu button[aria-label="More"]:visible').first().click();
await view.locator('.pop button:has-text("Sandbox: web")').click();
ok('⋯ → Sandbox is pushed', await titled('web'));
ok('its working directory', await shown('xb-field:has-text("Working directory")'));
await view.locator('xb-row:has-text("Manage sandboxes") .row-main').click();
ok('Manage pushes the Sandboxes', await titled('Sandboxes'));
ok('…listing them', await shown('xb-row:has-text("stopped · active here")') && await shown('xb-row:has-text("running · attached here")'));
await view.locator('.back:visible').click();
await view.locator('.back:visible').click();
ok('back to the conversation', await titled('fix the build'));

ok('no page errors', errors.length === 0, errors.join(' | '));
ok('no runtime errors', (await page.evaluate(() => window.xbnPreview.errors)).length === 0, JSON.stringify(await page.evaluate(() => window.xbnPreview.errors)));
const diags = await page.evaluate(() => window.xbnPreview.diagnostics.filter((d) => d.level !== 'info'));
ok('no diagnostics', diags.length === 0, JSON.stringify(diags));
await browser.close();
done('native');
