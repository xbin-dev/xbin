// The eight tile rewrites of the native design (plans/native.md §18) run
// against a stubbed xbin in node (hack/xbn/node.mjs); each rendered tree must
// equal the JSON printed beside it. The examples are read from the design
// itself, so the design and the runtime cannot drift apart silently. Modules
// the examples import that the tiles have not extracted yet (fmt.js, prom.js,
// chat-core.js) are stand-ins in hack/xbn/testdata/ copied from the pages.
//
// Deviations from the printed trees, each said in the design's own words:
//   devbox      "sheets closed and trimmed from the tree for brevity" — the
//               two closed sheets (r.1.0, r.1.1) are dropped before comparing
//   chat        "on the wire the runtime adds its tokens — omitted here" —
//               message tokens are dropped before comparing
//   prometheus  the printed rate "0.021/s" does not follow from the printed
//               points (12.28 → 12.34 over 6 s is 0.01/s with the page's
//               rateOf); the expected detail is corrected to "12.34 · 0.01/s"
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, mkdtempSync, writeFileSync, copyFileSync, readdirSync, existsSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { runNative } from './xbn/node.mjs';

const ROOT = new URL('..', import.meta.url).pathname;
const DESIGN = readFileSync(join(ROOT, 'plans/native.md'), 'utf8');

// §18.N sections → {name: {js, json}}
function examples() {
  const out = {};
  const parts = DESIGN.split(/^### 18\.\d+ /m).slice(1);
  for (const p of parts) {
    const name = p.split(/\s/)[0];
    const js = p.match(/```js\n([\s\S]*?)```/);
    const json = p.match(/```json\n([\s\S]*?)```/);
    if (js && json) out[name] = { js: js[1], json: JSON.parse(json[1]) };
  }
  return out;
}
const EX = examples();

const NOW = 1790000000000; // 2026-09-21T14:13:20Z
async function run(name, data, steps = []) {
  const ex = EX[name];
  assert.ok(ex, `plans/native.md §18 has no example "${name}"`);
  const dir = mkdtempSync(join(tmpdir(), `xbn-${name}-`));
  try {
    writeFileSync(join(dir, 'native.js'), ex.js);
    const extra = join(ROOT, 'hack/xbn/testdata', name);
    if (existsSync(extra)) for (const f of readdirSync(extra)) copyFileSync(join(extra, f), join(dir, f));
    const r = await runNative({ entry: join(dir, 'native.js'), data: { now: NOW, ...data }, steps });
    assert.deepEqual(r.unmatched, [], 'every request the tile made has a stubbed answer');
    assert.deepEqual(r.errors, [], 'no runtime errors');
    assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
    return { r, expected: structuredClone(ex.json) };
  } finally { rmSync(dir, { recursive: true, force: true }); }
}

test('every §18 example is found in the design', () => {
  assert.deepEqual(Object.keys(EX), ['counter-go', 'calendar', 'egress-approver', 's3-archiver', 'webhooks', 'devbox', 'prometheus-viewer', 'chat']);
});

test('§18.1 counter-go', async () => {
  const { r, expected } = await run('counter-go', {
    self: 'apps/counter-go', routes: { 'GET /api/apps/counter-go/count': { json: { count: 42 } } },
  });
  assert.deepEqual(r.tree, expected);
  assert.equal(r.messages.filter((m) => m.op === 'mount').length, 1);
});

test('§18.2 calendar', async () => {
  const { r, expected } = await run('calendar', {
    self: 'apps/calendar',
    routes: { 'GET /api/apps/calendar/events': { json: { events: [
      { id: 1, day: '2026-09-21', time: '09:30', title: 'standup' },
      { id: 2, day: '2026-09-21', time: '', title: 'lunch with Ana' },
    ] } } },
  });
  assert.deepEqual(r.tree, expected);
});

test('§18.2 calendar: typing, submit and the bus refresh', async () => {
  const events = [{ id: 1, day: '2026-09-21', time: '09:30', title: 'standup' }];
  const { r } = await run('calendar', {
    self: 'apps/calendar',
    routes: {
      'GET /api/apps/calendar/events': [{ json: { events } }, { json: { events: [...events, { id: 3, time: '', title: 'retro' }] } }],
      'POST /api/apps/calendar/events': { json: { ok: true } },
    },
  }, [
    { input: ['r.2.1', 'r'] }, { input: ['r.2.1', 're'] }, { input: ['r.2.1', 'retro'] },
    { event: ['r.2.1', 'submit', { value: 'retro' }] },
    { bus: ['res:apps/calendar/bus/events/created', { id: 3 }] },
  ]);
  const post = r.requests.find((q) => q.method === 'POST');
  assert.deepEqual(post.body, { day: '2026-09-21', time: '', title: 'retro' });
  const patches = r.messages.filter((m) => m.op === 'patch');
  // typing sends nothing back to the field (the app already shows it); the button enables once
  assert.deepEqual(patches[0].ops, [['set', 'r.2.2', { disabled: false }]]);
  // the reset after submit clears the field (it differs from what the field reported)
  assert.ok(patches.some((p) => p.ops.some((o) => o[0] === 'set' && o[1] === 'r.2.1' && o[2].value === '')));
  // the bus event reloads the list: one keyed insert
  assert.deepEqual(patches.at(-1).ops, [['insert', 'r.1', 1, { k: 'r.1.0:3', t: 'row', p: { title: 'retro', detail: '--:--', mono: 'detail' } }]]);
});

const egressState = () => ({
  pending: [{ ip: '203.0.113.7', rdns: 'cdn.example.net', bytesOut: 1536, bytesIn: 0, lastSeen: NOW - 38000 }],
  approved: [{ ip: '198.51.100.20', rdns: 'api.github.com', bytesOut: 48230, bytesIn: 1048576, lastSeen: NOW - 120000 }],
  denied: [], clients: 2, egressReady: true,
});

test('§18.3 egress-approver', async () => {
  const { r, expected } = await run('egress-approver', {
    self: 'apps/egress-approver', routes: { 'GET /api/apps/egress-approver/state': { json: egressState() } },
  });
  assert.deepEqual(r.tree, expected);
});

test('§18.3 egress-approver: an unchanged poll costs nothing, a ticking "ago" one set', async () => {
  const { r } = await run('egress-approver', {
    self: 'apps/egress-approver', routes: { 'GET /api/apps/egress-approver/state': { json: egressState() } },
  }, [{ wait: 1200 }, { wait: 1200 }]);
  const tree = r.messages.filter((m) => m.op === 'mount' || m.op === 'patch');
  assert.equal(tree[0].op, 'mount');
  // 38s → 39s → 40s: one detail set per poll, nothing else
  assert.deepEqual(tree.slice(1).map((m) => m.ops), [
    [['set', 'r.0.0.0:203.0.113.7', { detail: '↑1.5K · 39s ago' }]],
    [['set', 'r.0.0.0:203.0.113.7', { detail: '↑1.5K · 40s ago' }]],
  ]);
});

test('§18.3 egress-approver: whois pushes a screen, pop drops it', async () => {
  const { r } = await run('egress-approver', {
    self: 'apps/egress-approver',
    routes: {
      'GET /api/apps/egress-approver/state': { json: egressState() },
      'GET /api/apps/egress-approver/detail?ip=203.0.113.7': { json: { rdns: 'cdn.example.net', rdap: { name: 'EXAMPLE-CDN', country: 'NL' } } },
    },
  }, [{ tap: 'r.0.0.0:203.0.113.7' }]);
  const root = r.tree.root;
  assert.deepEqual(root.c.map((c) => [c.k, c.t, c.p.title]), [['r.0', 'screen', 'Egress Approver'], ['r.1', 'screen', '203.0.113.7']]);
  assert.deepEqual(root.c[1].c[0].c.map((x) => [x.k, x.p.title, x.p.detail]), [
    ['r.1.0.0.0', 'reverse dns', 'cdn.example.net'], ['r.1.0.0.1', 'network', 'EXAMPLE-CDN'], ['r.1.0.0.2', 'country', 'NL']]);
  const { r: popped } = await run('egress-approver', {
    self: 'apps/egress-approver',
    routes: {
      'GET /api/apps/egress-approver/state': { json: egressState() },
      'GET /api/apps/egress-approver/detail?ip=203.0.113.7': { json: { rdns: 'cdn.example.net', rdap: {} } },
    },
  }, [{ tap: 'r.0.0.0:203.0.113.7' }, { event: ['r', 'pop', { depth: 1 }] }]);
  assert.deepEqual(popped.tree.root.c.map((c) => c.k), ['r.0']);
  assert.deepEqual(popped.messages.filter((m) => m.op === 'patch').at(-1).ops, [['remove', 'r.1']]);
});

test('§18.3 egress-approver: approve moves the row at once (a keyed move, not a rebuild)', async () => {
  const after = egressState();
  after.approved.unshift({ ...after.pending[0] });
  after.pending = [];
  const { r } = await run('egress-approver', {
    self: 'apps/egress-approver',
    routes: {
      'GET /api/apps/egress-approver/state': [{ json: egressState() }, { json: after }],
      'POST /api/apps/egress-approver/approve': { json: { ok: true }, delay: 500 },
    },
  }, [{ tap: 'r.0.0.0:203.0.113.7.0.0' }, { wait: 500 }]);
  assert.deepEqual(r.requests.find((q) => q.method === 'POST').body, { ip: '203.0.113.7' });
  const patches = r.messages.filter((m) => m.op === 'patch');
  // the optimistic render: the pending row goes, an empty state and the approved row arrive
  const ops = patches[0].ops;
  assert.deepEqual(ops.filter((o) => o[0] === 'remove'), [['remove', 'r.0.0.0:203.0.113.7']]);
  assert.deepEqual(ops.filter((o) => o[0] === 'insert').map((o) => [o[1], o[2], o[3].k]),
    [['r.0.0', 0, 'r.0.0.0'], ['r.0.1', 0, 'r.0.1.0:203.0.113.7']]);
  assert.deepEqual(r.tree.root.c[0].c[1].c.map((x) => x.k), ['r.0.1.0:203.0.113.7', 'r.0.1.0:198.51.100.20']);
  assert.equal(r.tree.root.c[0].c[1].p.badge, '2');
});

test('§18.4 s3-archiver (after "Save & test")', async () => {
  const cfg = { endpoint: 'https://s3.eu-central-1.amazonaws.com', region: 'eu-central-1', bucket: 'acme-backups', prefix: 'xbin/' };
  const { r, expected } = await run('s3-archiver', {
    self: 'apps/s3-archiver',
    routes: {
      'GET /api/apps/s3-archiver/config': { json: { config: cfg, hasCreds: true } },
      'PUT /api/apps/s3-archiver/config': { json: { ok: true } },
      'POST /api/apps/s3-archiver/check': { json: { bucket: 'acme-backups' } },
    },
  }, [{ tap: 'r.2.1' }]);
  assert.deepEqual(r.tree, expected);
  assert.deepEqual(r.requests[1].body, { endpoint: cfg.endpoint, region: cfg.region, bucket: cfg.bucket, prefix: cfg.prefix });
  assert.deepEqual(r.requests.map((q) => `${q.method} ${q.url}`), [
    'GET /api/apps/s3-archiver/config', 'PUT /api/apps/s3-archiver/config',
    'GET /api/apps/s3-archiver/config', 'POST /api/apps/s3-archiver/check']);
});

test('§18.5 webhooks', async () => {
  const { r, expected } = await run('webhooks', {
    self: 'apps/webhooks',
    now: 1790000300000,
    routes: { 'GET /api/apps/webhooks/hooks': { json: {
      hooks: [{ id: 'h7', name: 'deploy', enabled: true, auth: 'hmac', agent: '' }],
      agents: [{ provider: 'apps/agent' }],
      host: 'hooks.example.com',
      deliveries: [{ at: 1790000000, hook: 'h7', topic: 'deploy/push', result: 'trigger deploy ran', status: 202 }],
    } } },
  });
  assert.deepEqual(r.tree, expected);
});

test('§18.6 devbox', async () => {
  const { r, expected } = await run('devbox', {
    self: 'apps/devbox',
    routes: { 'GET /api/apps/devbox/state': { json: {
      containers: [{ name: 'dev', image: 'docker.io/library/ubuntu:24.04', state: 'running', status: 'Up 3 hours' }],
      keys: [{ fingerprint: 'SHA256:abc', type: 'ssh-ed25519', comment: 'me@laptop' }],
      podmanVersion: '5.2.1', sshPort: 2222,
    } } },
  });
  const tree = structuredClone(r.tree);
  assert.deepEqual(tree.root.c.map((c) => c.k), ['r.0', 'r.1.0', 'r.1.1'], 'nav + the two sheets');
  assert.deepEqual(tree.root.c.slice(1).map((c) => [c.t, c.p.open]), [['sheet', false], ['sheet', false]]);
  tree.root.c = tree.root.c.slice(0, 1); // trimmed in the design
  assert.deepEqual(tree, expected);
});

test('§18.6 devbox: selecting the keys tab materializes it', async () => {
  const { r } = await run('devbox', {
    self: 'apps/devbox',
    routes: { 'GET /api/apps/devbox/state': { json: {
      containers: [], keys: [{ fingerprint: 'SHA256:abc', type: 'ssh-ed25519', comment: 'me@laptop' }], podmanVersion: '5.2.1', sshPort: 2222,
    } } },
  }, [{ event: ['r.0.0.2', 'change', { key: 'keys' }] }]);
  const tabs = r.tree.root.c[0].c[0].c[1];
  assert.equal(tabs.p.selected, 'keys');
  assert.deepEqual(tabs.c[0].c, [], 'the containers tab is no longer materialized');
  assert.equal(tabs.c[1].c[0].c[0].k, 'r.0.0.2.1.0.0:SHA256:abc');
  const last = r.messages.filter((m) => m.op === 'patch').at(-1);
  // the tab change is not echoed back (the app reported it); the content swaps
  assert.ok(!last.ops.some((o) => o[0] === 'set' && o[1] === 'r.0.0.2'));
});

test('§18.7 prometheus-viewer', async () => {
  const metrics = (v) => ({ text: `# HELP process_cpu_seconds_total Total user and system CPU time spent in seconds.\n# TYPE process_cpu_seconds_total counter\nprocess_cpu_seconds_total ${v}\n` });
  const { r, expected } = await run('prometheus-viewer', {
    self: 'apps/prometheus-viewer',
    iface: { sources: { endpoints: [{ url: 'https://node.example/api/apps/node-exporter', provider: 'apps/node-exporter' }] } },
    routes: { 'GET https://node.example/api/apps/node-exporter/metrics': [metrics(12.28), metrics(12.3), metrics(12.34)] },
  }, [{ wait: 3000 }, { wait: 3000 }]);
  const row = expected.root.c[1].c[0].c[1];
  assert.equal(row.p.detail, '12.34 · 0.021/s');
  row.p.detail = '12.34 · 0.01/s'; // see the header: the printed rate is inconsistent with the printed points
  assert.deepEqual(r.tree, expected);
});

test('§18.8 chat', async () => {
  const { r, expected } = await run('chat', { self: 'apps/chat', iface: { llm: { url: '/api/apps/llm-gw' } } });
  const tree = structuredClone(r.tree);
  const msg = tree.root.c[1].c[3];
  assert.equal(msg.t, 'message');
  assert.deepEqual(msg.p.tokens, [{ t: 'paragraph', c: [{ t: 'text', text: 'There are ' }, { t: 'strong', c: [{ t: 'text', text: '7' }] }, { t: 'text', text: ' open issues labelled ' }, { t: 'codespan', text: 'bug' }, { t: 'text', text: '.' }] }]);
  delete msg.p.tokens; // "on the wire the runtime adds its tokens — omitted here"
  assert.deepEqual(tree, expected);
});
