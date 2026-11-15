// hack/ui-harness-quietnet.test.mjs — the harness browser's network
// namespace (hack/ui-harness/quietnet.js), without a browser: what
// quietnet.sh starts in there reaches a port on the host's loopback through
// the relay, sees none of the host's interfaces, reads as online, sees no
// network event once it is up, and leaves nothing running behind it.
//
// (That Chromium fails queued requests with ERR_NETWORK_CHANGED on an
// interface change, and does not in here, was measured with a browser: 40
// modules a page for 12 s while veth pairs came and went beside it — 0 pages
// and 100-odd failed requests on the host's network, 75 pages and none in
// the namespace. docs/maintenance.md → "UI harness".)
import assert from 'node:assert/strict';
import { execFile, execFileSync } from 'node:child_process';
import http from 'node:http';
import { createRequire } from 'node:module';
import { test } from 'node:test';

const quietnet = createRequire(import.meta.url)('./ui-harness/quietnet.js');
const skip = quietnet.why() ? `no network namespace here: ${quietnet.why()}` : false;

// inNamespace(env, script): node -e script as "the browser" quietnet.sh execs.
const inNamespace = (env, script) => new Promise((resolve) => {
  execFile(quietnet.WRAPPER, ['-e', script], { env: { ...process.env, ...env, QUIETNET_EXE: process.execPath }, timeout: 30000 },
    (err, stdout, stderr) => resolve({ code: err ? err.code ?? 1 : 0, stdout, stderr }));
});
const relays = () => { try { return execFileSync('pgrep', ['-f', 'quietnet[.]js inner'], { encoding: 'utf8' }).trim().split('\n').filter(Boolean); } catch { return []; } };

test('a port on the host\'s loopback is the same port in the namespace', { skip }, async () => {
  const srv = http.createServer((req, res) => res.end(`host says ${req.url}`));
  await new Promise((r) => srv.listen(0, '127.0.0.1', r));
  const port = srv.address().port;
  const before = relays();
  const ns = await quietnet.namespace([port]);
  try {
    const got = await inNamespace(ns.env, `
      const { execFileSync } = require('child_process');
      (async () => {
        const body = await (await fetch('http://127.0.0.1:${port}/x?y=1')).text();
        const links = execFileSync('ip', ['-o', 'link'], { encoding: 'utf8' }).trim().split('\\n').map((l) => l.split(': ')[1].split('@')[0]).sort();
        // nothing changes in here once the browser is up: three seconds of netlink say nothing
        let events = '';
        try { events = execFileSync('timeout', ['3', 'ip', 'monitor', 'all'], { encoding: 'utf8' }); } catch (e) { events = String(e.stdout || ''); }
        const other = await fetch('http://127.0.0.1:${port + 1 > 65535 ? port - 1 : port + 1}/').then(() => 'reached', (e) => 'refused');
        console.log(JSON.stringify({ body, links, events: events.trim(), other, uid: process.getuid() }));
      })();`);
    assert.equal(got.code, 0, got.stderr);
    const out = JSON.parse(got.stdout);
    assert.equal(out.body, 'host says /x?y=1', 'the request crossed the relay to the host\'s server');
    assert.deepEqual(out.links, ['lo', 'qn0', 'qn1'], 'the namespace has its loopback and the idle pair, none of the host\'s interfaces');
    assert.equal(out.events, '', 'no link, address or route event after start-up');
    assert.equal(out.other, 'refused', 'a port that was not relayed is not there');
  } finally {
    ns.close();
    srv.close();
  }
  // the relay goes with what it served
  const t0 = Date.now();
  let left = relays().filter((p) => !before.includes(p));
  while (left.length && Date.now() - t0 < 5000) { await new Promise((r) => setTimeout(r, 100)); left = relays().filter((p) => !before.includes(p)); }
  assert.deepEqual(left, [], 'no relay outlives its browser');
});

test('the relay goes when the browser never starts', { skip }, async () => {
  const before = relays();
  const ns = await quietnet.namespace([]);
  try {
    const got = await new Promise((resolve) => execFile(quietnet.WRAPPER, [], { env: { ...process.env, ...ns.env, QUIETNET_EXE: '/nonexistent/browser' } }, (err) => resolve(err?.code)));
    assert.equal(got, 127, 'the wrapper says the browser could not be started');
  } finally { ns.close(); }
  const t0 = Date.now();
  let left = relays().filter((p) => !before.includes(p));
  while (left.length && Date.now() - t0 < 5000) { await new Promise((r) => setTimeout(r, 100)); left = relays().filter((p) => !before.includes(p)); }
  assert.deepEqual(left, [], 'no relay left behind');
});
