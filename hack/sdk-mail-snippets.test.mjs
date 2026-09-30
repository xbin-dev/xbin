// hack/sdk-mail-snippets.test.mjs — docs/sdk.md's partition mail snippets for
// node and python backends, run by `make js-test` against a stand-in for
// xbind's gateway socket: each block, taken from the page itself, sends an
// item and drains a two-page inbox with the requests xbind's mail routes
// take (docs/protocol.md, POST /partitions/mail).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:http';
import { createRequire } from 'node:module';
import { execFile } from 'node:child_process';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const doc = readFileSync(new URL('../docs/sdk.md', import.meta.url), 'utf8');

// The fenced block of lang in section heading that mentions the mail routes.
function snippet(heading, lang) {
  const start = doc.indexOf(`\n${heading}`);
  assert.ok(start >= 0, `docs/sdk.md has ${heading}`);
  const end = doc.indexOf('\n## ', start + 1);
  const section = doc.slice(start, end < 0 ? undefined : end);
  const blocks = [...section.matchAll(new RegExp('```' + lang + '\\n([\\s\\S]*?)```', 'g'))].map((m) => m[1]);
  const mail = blocks.filter((b) => b.includes('/api/xbin/partitions/mail'));
  assert.equal(mail.length, 1, `${heading}: one ${lang} block about partition mail`);
  return mail[0];
}

const ids = ['000000000000000000000001', '000000000000000000000002', '000000000000000000000003'];
const item = (id, n) => ({ id, from: 'global', topic: 'handoff/dm', data: { n }, at: '2026-09-30T10:00:00Z', expires: '2026-10-07T10:00:00Z' });

// A stand-in gateway on a unix socket: the mail routes, a two-page inbox
// (the first page says more), every request recorded.
async function gateway(t) {
  const dir = mkdtempSync(join(tmpdir(), 'xbin-mail-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const sock = join(dir, 'gw.sock');
  const calls = [];
  const srv = createServer((req, res) => {
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => {
      calls.push(`${req.method} ${req.url} ${body}`.trim());
      const send = (code, v) => { res.writeHead(code, { 'content-type': 'application/json' }); res.end(JSON.stringify(v)); };
      if (req.headers.authorization !== 'Bearer tok') return send(401, { error: 'no token' });
      const u = new URL(req.url, 'http://xbin');
      if (u.pathname === '/api/xbin/partitions/mail' && req.method === 'POST') return send(200, { ok: true, id: 'abcdef0123456789abcdef01' });
      if (u.pathname === '/api/xbin/partitions/mail') {
        return u.searchParams.get('after') === ids[1]
          ? send(200, { items: [item(ids[2], 3)], more: false })
          : send(200, { items: [item(ids[0], 1), item(ids[1], 2)], more: true });
      }
      if (u.pathname === '/api/xbin/partitions/mail/ack') return send(200, { ok: true });
      if (u.pathname === '/cut-off') { res.writeHead(200, { 'content-type': 'application/json' }); return res.end('{"items":['); }
      send(404, { error: 'not here' });
    });
  });
  await new Promise((resolve) => srv.listen(sock, resolve));
  t.after(() => srv.close());
  return { sock, calls };
}

const want = [
  'POST /api/xbin/partitions/mail {"to":"user:alice","topic":"handoff/dm","data":{"text":"hi"}}',
  'GET /api/xbin/partitions/mail?limit=100',
  `POST /api/xbin/partitions/mail/ack {"ids":["${ids[0]}","${ids[1]}"]}`,
  `GET /api/xbin/partitions/mail?limit=100&after=${ids[1]}`,
  `POST /api/xbin/partitions/mail/ack {"ids":["${ids[2]}"]}`,
];

test('node: send, then drain the inbox page by page', async (t) => {
  const { sock, calls } = await gateway(t);
  const block = snippet('## node backend', 'js');
  const AsyncFunction = (async () => {}).constructor;
  const env = { XBIN_GATEWAY: sock, XBIN_TOKEN: 'tok' };
  const run = new AsyncFunction('require', 'process', 'dm', `${block}\nreturn { id, drain, xbind };`);
  const { id, drain, xbind } = await run(createRequire(import.meta.url), { env }, { text: 'hi' });
  assert.equal(id, 'abcdef0123456789abcdef01');
  const seen = [];
  await drain(async (it) => { seen.push(it.data.n); });
  assert.deepEqual(seen, [1, 2, 3]);
  assert.deepEqual(calls, want);
  // a 200 whose body isn't JSON (cut off, a proxy's page) rejects the call —
  // it must not throw in the response's 'end' listener, which would crash the backend
  await assert.rejects(xbind('GET', '/cut-off'), SyntaxError);
});

test('python: send, then drain the inbox page by page', async (t) => {
  const python = await new Promise((resolve) => execFile('python3', ['--version'], (err) => resolve(!err)));
  if (!python) { t.skip('no python3 here'); return; }
  const { sock, calls } = await gateway(t);
  const block = snippet('## python backend', 'python');
  const script = `dm = {"text": "hi"}\n${block}\nseen = []\ndrain(lambda it: seen.append(it["data"]["n"]))\nprint(json.dumps({"id": item_id, "seen": seen}))\n`;
  const out = await new Promise((resolve, reject) => execFile('python3', ['-c', script],
    { env: { ...process.env, XBIN_GATEWAY: sock, XBIN_TOKEN: 'tok' } },
    (err, stdout, stderr) => (err ? reject(new Error(`${err.message}\n${stderr}`)) : resolve(stdout))));
  assert.deepEqual(JSON.parse(out), { id: 'abcdef0123456789abcdef01', seen: [1, 2, 3] });
  // python's json.dumps puts a space after : and , — compare the requests' JSON as values
  const norm = (c) => c.replace(/ (\{.*)$/, (_, j) => ` ${JSON.stringify(JSON.parse(j))}`);
  assert.deepEqual(calls.map(norm), want.map(norm));
});
