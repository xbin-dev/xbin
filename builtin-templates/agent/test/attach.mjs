// attach.mjs — attaching files from the composer.
//
// The order of requests is the whole contract: a file has to be in the run's
// session files BEFORE the message naming it arrives, or the backend refuses
// the message. On the home view there is no run yet, so the tile creates one
// held (no message, no drive), uploads into it, and only then sends. This
// drives the real agent.js against a stubbed transport and checks that order,
// the failure paths, the chip row, and how a sent message shows its files.
//
//   node test/attach.mjs        (needs playwright + a chromium build)
import { readFileSync } from 'node:fs';
import { serveKit, tileHtml } from './kit.mjs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));

let chromium;
try {
  ({ chromium } = await import('/usr/local/node/lib/node_modules/playwright/index.mjs'));
} catch {
  try { ({ chromium } = await import('playwright')); } catch {
    console.log('SKIP: playwright not installed');
    process.exit(0);
  }
}

let failures = 0;
const ok = (name, cond, extra = '') => {
  if (!cond) { console.log(`FAIL  ${name}  ← ${extra}`); failures++; }
  return cond;
};

const ORIGIN = 'http://tile.test';
const MODULES = ['agent.js', 'chat-view.js', 'chat-fold.js', 'chat-cards.js', 'chat-md.js', 'stream.js', 'tool-heads.js',
  'conv-groups.js', 'conv-list.js', 'sidebar.js', 'home.js'];
const FILES = { '/': 'index.html', '/index.html': 'index.html', ...Object.fromEntries(MODULES.map((m) => ['/' + m, m])) };

const browser = await chromium.launch();
const page = await browser.newPage();

await page.route(`${ORIGIN}/**`, (route) => {
  const path = new URL(route.request().url()).pathname;
  const file = FILES[path];
  if (!file) return route.fulfill({ status: 404, body: '' });
  let body = readFileSync(join(here, '..', file), 'utf8');
  if (file === 'index.html') {
    body = tileHtml(body).replace(/<link rel="stylesheet" href="\/vendor\/theme.css">/,
      '<style>:root{--bx-border:#ccc;--bx-panel:#fff;--bx-panel-2:#f4f4f4;--bx-text:#111;' +
      '--bx-muted:#777;--bx-accent:#b57e10;--bx-mono:monospace;--bx-red:#c33;--bx-green:#3a3}</style>');
  }
  route.fulfill({ contentType: file.endsWith('.js') ? 'text/javascript' : 'text/html', body });
});
await serveKit(page);
await page.route('**/vendor/lit-all.min.js', (r) => r.fulfill({ contentType: 'text/javascript',
  body: readFileSync(process.env.BX_VENDOR ? join(process.env.BX_VENDOR, 'lit-all.min.js') : join(here, '..', '..', '..', 'web', 'vendor', 'lit-all.min.js'), 'utf8') }));
await page.route('**/vendor/marked.esm.js', (r) =>
  r.fulfill({ contentType: 'text/javascript', body: 'export const marked={parse:(s)=>s,use(){}};' }));

// The stub backend. window.__fail makes the named upload fail once;
// window.__detail is what GET /runs/{id} returns.
await page.addInitScript(() => {
  window.__calls = [];
  window.__fail = {};
  window.__deleted = [];
  const runs = [{ id: 5, title: 'existing task', status: 'idle', parentId: 0, rootId: 5, depth: 0, created: 1, kind: '' }];
  window.__detail = null;
  const res = (status, v) => ({ ok: status < 400, status, json: async () => v, blob: async () => new Blob([v]) });
  window.xbin = {
    self: 'apps/agent',
    fetch: async (url, opt = {}) => {
      const method = opt.method || 'GET';
      const path = url.replace('/api/apps/agent', '');
      const rec = { method, path };
      if (opt.body instanceof Blob) { rec.size = opt.body.size; rec.type = new Headers(opt.headers).get('Content-Type'); }
      else if (typeof opt.body === 'string') { try { rec.body = JSON.parse(opt.body); } catch { rec.body = opt.body; } }
      window.__calls.push(rec);
      if (url.includes('/prefs/')) return res(404, {});
      if (path === '/runs' || path === '/runs?roots=1') return res(200, runs);
      if (path.startsWith('/conversations')) return res(200, { pinned: [], items: runs.map((r) => ({ access: 'owner', mine: true, origin: 'chat', ...r })), next: '' });
      if (path.startsWith('/stream')) return new Response(new ReadableStream({ start() {} }), { headers: { 'Content-Type': 'text/event-stream' } });
      if (path === '/halt') return res(200, { on: false });
      if (path === '/ask' && method === 'POST') return res(200, { id: 42, title: rec.body.text, status: 'idle', kind: 'quick', parentId: 0 });
      let m = path.match(/^\/runs\/(\d+)\/upload\?name=(.*)$/);
      if (m) {
        const name = decodeURIComponent(m[2]);
        if (window.__fail[name]) { delete window.__fail[name]; return res(502, { error: 'storing the file failed: gateway down' }); }
        return res(200, { path: name, mime: rec.type, bytes: rec.size, binary: !/^text\//.test(rec.type) });
      }
      if (/^\/runs\/\d+\/raw/.test(path)) return res(200, 'PNGDATA');
      if (/^\/runs\/\d+\/(message|answer)$/.test(path)) return res(200, { ok: 'true' });
      m = path.match(/^\/runs\/(\d+)$/);
      if (m && method === 'DELETE') { window.__deleted.push(+m[1]); return res(200, { ok: 'true' }); }
      if (m) {
        const id = +m[1];
        return res(200, window.__detail || { run: { id, title: 'run ' + id, status: 'idle' },
          messages: [], steps: [], memory: {}, config: {}, files: [], draft: '', messageFiles: {} });
      }
      if (/^\/runs\/\d+\/files$/.test(path)) return res(200, (window.__detail && window.__detail.files) || []);
      m = path.match(/^\/runs\/(\d+)\/view$/);
      if (m) {
        const id = +m[1];
        const d = window.__detail || { run: { id, title: 'run ' + id, status: 'idle' }, messages: [], files: [], messageFiles: {} };
        return res(200, { cursor: 'g.1', links: [], queued: [], drafts: [], chain: [], steps: [], memory: {}, config: {}, ...d,
          run: { pendingState: {}, ...d.run } });
      }
      return res(200, {});
    },
    download: (name) => { window.__downloaded = name; },
    bus: { on: () => () => {} },
    iface: () => null,
  };
});

const dialogs = [];
page.on('dialog', (d) => { dialogs.push(d.message()); d.accept(); });
page.on('pageerror', (e) => { console.log('PAGEERROR:', e.message); failures++; });
await page.setViewportSize({ width: 700, height: 900 });
await page.goto(`${ORIGIN}/`);
await page.waitForFunction(() => document.querySelectorAll('#runs .run').length > 0, { timeout: 5000 });

const png = Buffer.from('\x89PNG\r\n\x1a\nxxxxxxxx', 'binary');
const calls = () => page.evaluate(() => window.__calls.filter((c) => !c.path.startsWith('/runs?') && c.path !== '/runs' &&
  c.path !== '/halt' && !c.path.includes('prefs') && !c.path.startsWith('/stream') &&
  !(c.method === 'GET' && /^\/runs\/\d+(\/files|\/view)?$/.test(c.path))));
const resetCalls = () => page.evaluate(() => { window.__calls = []; });
const chips = () => page.$$eval('#attach .chip', (els) => els.map((e) => ({ cls: e.className, text: e.textContent.replace(/\s+/g, ' ').trim() })));
const idle = () => page.waitForFunction(() => !document.getElementById('send').disabled, { timeout: 3000 });

// 1. Picking files shows them as chips; nothing is uploaded yet.
ok('no chip row before anything is attached', await page.$eval('#attach', (e) => e.hidden));
await page.setInputFiles('#clipin', [{ name: 'shot.png', mimeType: 'image/png', buffer: png },
                                     { name: 'data.csv', mimeType: 'text/csv', buffer: Buffer.from('a,b\n1,2\n') }]);
let c = await chips();
ok('picked files show as chips', c.length === 2 && c[0].text.includes('shot.png') && c[1].text.includes('data.csv'), JSON.stringify(c));
ok('picking uploads nothing', !(await calls()).some((x) => x.path.includes('/upload')));
await page.click('#attach .chip [data-rm]'); // drop shot.png again
c = await chips();
ok('✕ removes a chip', c.length === 1 && c[0].text.includes('data.csv'), JSON.stringify(c));
await page.setInputFiles('#clipin', [{ name: 'shot.png', mimeType: 'image/png', buffer: png }]);

// 2. Home view: hold → upload each → message, in that order.
await resetCalls();
await page.fill('#msg', 'what do these show?');
await page.click('#send');
await page.waitForFunction(() => window.__calls.some((c) => /\/message$/.test(c.path)), { timeout: 3000 });
await idle();
let seq = (await calls()).map((x) => `${x.method} ${x.path.replace(/\?.*/, '')}`);
ok('home: the run is created held, then files upload, then the message is sent',
  JSON.stringify(seq.slice(0, 4)) === JSON.stringify(['POST /ask', 'PUT /runs/42/upload', 'PUT /runs/42/upload', 'POST /runs/42/message']),
  seq.join(' | '));
let all = await calls();
ok('home: /ask carries hold', all[0].body && all[0].body.hold === true && all[0].body.text === 'what do these show?', JSON.stringify(all[0].body));
ok('home: each upload sends the file itself with its own type',
  all[1].type === 'text/csv' && all[1].size === 8 && all[2].type === 'image/png' && all[2].size === png.length, JSON.stringify(all.slice(1, 3)));
const msg = all.find((x) => /\/message$/.test(x.path));
ok('home: the message names the uploaded paths', JSON.stringify(msg.body.files) === '["data.csv","shot.png"]', JSON.stringify(msg.body));
ok('home: chips and text clear after sending', (await chips()).length === 0 && (await page.inputValue('#msg')) === '');
ok('home: the tile opens the new run', await page.evaluate(() => document.querySelector('#top .title')?.textContent === 'run 42'));

// 3. Home view, upload fails: the empty run is deleted and the chips stay.
await page.click('#home');
await page.setInputFiles('#clipin', [{ name: 'a.png', mimeType: 'image/png', buffer: png }]);
await page.evaluate(() => { window.__fail['a.png'] = true; });
await resetCalls();
await page.fill('#msg', 'look');
await page.click('#send');
await idle();
all = await calls();
ok('home failure: no message is sent', !all.some((x) => /\/message$/.test(x.path)), JSON.stringify(all.map((x) => x.path)));
ok('home failure: the held run is deleted', (await page.evaluate(() => window.__deleted)).includes(42));
c = await chips();
ok('home failure: the chip stays, marked', c.length === 1 && c[0].cls.includes('bad') && c[0].text.includes('gateway down'), JSON.stringify(c));
ok('home failure: the text stays', (await page.inputValue('#msg')) === 'look');
ok('home failure: the error is shown', dialogs.some((d) => d.includes('gateway down')), dialogs.join(' | '));
// Retrying uploads again, into the NEW run (the old one and its upload are gone).
await resetCalls();
await page.click('#send');
await idle();
seq = (await calls()).map((x) => `${x.method} ${x.path.replace(/\?.*/, '')}`);
ok('home retry: a fresh held run, the upload again, then the message',
  JSON.stringify(seq.slice(0, 3)) === JSON.stringify(['POST /ask', 'PUT /runs/42/upload', 'POST /runs/42/message']), seq.join(' | '));

// 4. Inside a run: upload, then message; a failed upload sends no message and
//    a retry re-uploads only what didn't make it.
await page.click('#home');
await page.click('#runs .run');
await page.waitForFunction(() => document.querySelector('#top .title')?.textContent === 'run 5', { timeout: 3000 });
await page.setInputFiles('#clipin', [{ name: 'one.png', mimeType: 'image/png', buffer: png },
                                     { name: 'two.png', mimeType: 'image/png', buffer: png }]);
await page.evaluate(() => { window.__fail['two.png'] = true; });
await resetCalls();
await page.fill('#msg', 'compare');
await page.click('#send');
await idle();
seq = (await calls()).map((x) => `${x.method} ${x.path}`);
ok('in-run failure: no message after a failed upload', !seq.some((x) => /message/.test(x)), seq.join(' | '));
c = await chips();
ok('in-run failure: the uploaded chip is marked done, the failed one bad',
  c.length === 2 && c[0].cls.includes('done') && c[1].cls.includes('bad'), JSON.stringify(c));
await resetCalls();
await page.click('#send');
await idle();
seq = (await calls()).map((x) => `${x.method} ${x.path}`);
ok('in-run retry: only the failed file uploads again, then the message',
  JSON.stringify(seq) === JSON.stringify(['PUT /runs/5/upload?name=two.png', 'POST /runs/5/message']), seq.join(' | '));
all = await calls();
ok('in-run: the message names both files', all[1].body.text === 'compare' && JSON.stringify(all[1].body.files) === '["one.png","two.png"]', JSON.stringify(all[1].body));

// 5. A message with no files sends no `files` at all.
await resetCalls();
await page.fill('#msg', 'plain');
await page.click('#send');
await idle();
all = await calls();
ok('plain message: no uploads, no files field', all.length === 1 && all[0].body.files === undefined, JSON.stringify(all));

// 6. Paste and drop both attach.
await page.evaluate(() => {
  const dt = new DataTransfer();
  dt.items.add(new File(['x'], 'pasted.png', { type: 'image/png' }));
  document.getElementById('msg').dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
});
await page.evaluate(() => {
  const dt = new DataTransfer();
  dt.items.add(new File(['y'], 'dropped.txt', { type: 'text/plain' }));
  const main = document.getElementById('main');
  main.dispatchEvent(new DragEvent('dragenter', { dataTransfer: dt, bubbles: true, cancelable: true }));
  main.dispatchEvent(new DragEvent('drop', { dataTransfer: dt, bubbles: true, cancelable: true }));
});
c = await chips();
ok('paste and drop both attach', c.length === 2 && c[0].text.includes('pasted.png') && c[1].text.includes('dropped.txt'), JSON.stringify(c));
ok('the drop highlight clears', !(await page.$eval('#main', (e) => e.classList.contains('dropping'))));

// 7. Too large: refused before any upload.
await page.evaluate(() => { document.querySelectorAll('#attach [data-rm]').forEach((b) => b.click()); });
await page.evaluate(() => {
  const dt = new DataTransfer();
  dt.items.add(new File([new Uint8Array(16 * 1024 * 1024 + 1)], 'big.bin'));
  document.getElementById('msg').dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
});
await resetCalls();
dialogs.length = 0;
await page.click('#send');
await idle();
ok('too large: marked on the chip', (await chips())[0]?.text.includes('too large'), JSON.stringify(await chips()));
ok('too large: nothing is uploaded', !(await calls()).some((x) => x.path.includes('/upload')));
await page.evaluate(() => { document.querySelectorAll('#attach [data-rm]').forEach((b) => b.click()); });

// 8. The chip row never pushes the composer off the bottom of a short card.
await page.setViewportSize({ width: 700, height: 400 });
await page.setInputFiles('#clipin', Array.from({ length: 14 }, (_, i) =>
  ({ name: `file-with-a-longish-name-${i}.png`, mimeType: 'image/png', buffer: png })));
const geo = await page.evaluate(() => {
  const r = document.querySelector('.composer').getBoundingClientRect();
  const se = document.scrollingElement;
  return { bottom: r.bottom, vh: innerHeight, docScroll: se.scrollHeight - se.clientHeight, docX: se.scrollWidth - se.clientWidth };
});
ok('chips: composer still flush to the bottom', Math.abs(geo.bottom - geo.vh) <= 1, JSON.stringify(geo));
ok('chips: the document does not scroll', geo.docScroll <= 0 && geo.docX <= 0, JSON.stringify(geo));
await page.evaluate(() => { document.querySelectorAll('#attach [data-rm]').forEach((b) => b.click()); });
await page.setViewportSize({ width: 700, height: 900 });

// 9. A sent message shows its files as chips (not the note written for the
//    model); an image gets a thumbnail; a deleted file is marked, not a link.
await page.evaluate(() => {
  window.__detail = {
    run: { id: 5, title: 'run 5', status: 'idle', updated: 99 },
    messages: [{ id: 7, role: 'user', content: 'what is this?\n\n[attached: shot.png (image/png, 12 B), gone.csv (text/csv, 1.0 KB)]' },
               { id: 8, role: 'user', content: '(see attached)\n\n[attached: doc.pdf (application/pdf, 2.0 KB)]' }],
    steps: [], memory: {}, config: {}, draft: '',
    files: [{ path: 'shot.png', bytes: 12, version: 1, mime: 'image/png', binary: true },
            { path: 'doc.pdf', bytes: 2048, version: 1, mime: 'application/pdf', binary: true }],
    messageFiles: { 7: ['shot.png'], 8: ['doc.pdf'] },
  };
});
await page.click('#home');
await page.click('#runs .run');
await page.waitForFunction(() => document.querySelectorAll('.afile').length === 3, { timeout: 3000 });
const bubble = await page.$$eval('.msg.user', (els) => els.map((e) => ({
  body: e.querySelector('.txt')?.textContent || '',
  files: [...e.querySelectorAll('.afile')].map((a) => ({ text: a.textContent.replace(/\s+/g, ' ').trim(), gone: a.classList.contains('gone'), link: a.dataset.afile || '' })),
})));
ok('the note is not shown as text', !bubble[0].body.includes('[attached:') && bubble[0].body.trim() === 'what is this?', JSON.stringify(bubble[0]));
ok('"(see attached)" is not shown either', bubble[1].body === '', JSON.stringify(bubble[1]));
ok('attached files show as chips', bubble[0].files.length === 2 && bubble[0].files[0].link === 'shot.png', JSON.stringify(bubble[0].files));
ok('a deleted file is marked and not a link', bubble[0].files[1].gone && !bubble[0].files[1].link, JSON.stringify(bubble[0].files[1]));
await page.waitForFunction(() => !!document.querySelector('.afile .ic img'), { timeout: 3000 }).catch(() => {});
const thumb = await page.$eval('.afile .ic img', (i) => i.src).catch(() => '');
ok('an attached image gets a thumbnail from the raw route', thumb.startsWith('blob:'), thumb);
ok('a non-image attachment gets no thumbnail fetch', !(await page.evaluate(() => window.__calls.some((c) => c.path.includes('raw?path=doc.pdf')))));

// 10. Clicking a chip opens it in the Files tab: a preview and a download, no
//     textarea editor.
await page.click('.afile[data-afile="shot.png"]');
await page.waitForFunction(() => !!document.getElementById('fl-dl'), { timeout: 3000 });
ok('files tab: no text editor for an attachment', !(await page.$('#fl-body')));
await page.waitForFunction(() => (document.getElementById('fl-img')?.src || '').startsWith('blob:'), { timeout: 3000 }).catch(() => {});
ok('files tab: an image previews', (await page.$eval('#fl-img', (i) => i.src).catch(() => '')).startsWith('blob:'));
const rows = await page.$$eval('#sbd table.tbl tr', (trs) => trs.slice(1).map((t) => t.textContent.replace(/\s+/g, ' ').trim()));
ok('files tab: rows show the type and View', rows.some((r) => r.includes('image/png') && r.includes('View')), rows.join(' | '));
await page.click('#fl-dl');
await page.waitForFunction(() => window.__downloaded, { timeout: 3000 }).catch(() => {});
ok('files tab: download hands the file to the browser', (await page.evaluate(() => window.__downloaded)) === 'shot.png');

await browser.close();
console.log(failures ? `\n${failures} FAILURE(S)` : 'all attach checks passed');
process.exit(failures ? 1 : 0);
