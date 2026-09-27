// hack/netrules.test.mjs — the net-slot picker (web/bx-netrules.js
// netOptions), run by `make js-test`: a sandbox-net class (a sandbox
// manager's network, plans/tile-sandbox-runtime.md §4) never offers host or
// a provider tile and has no org/personal default; a net slot keeps both.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { netOptions } from '../web/bx-netrules.js';

const org = { id: 'sales', netSets: ['devs-net'], resolvedNet: ['internet', 'lan:10.42.0.0/16'] };
const server = [
  { id: 'org', label: "org — org:sales's network sets (devs-net): internet, lan:10.42.0.0/16" },
  { id: 'internet', label: 'internet — public internet' },
  { id: 'none', label: 'none — no network for these sandboxes (the same as unbound)' },
  { id: 'set:lab', label: 'set:lab — network set (lan:10.0.0.0/8)' },
  { id: 'set:infra', label: 'set:infra — network set (lan:10.0.0.0/8, host) — says host: never a sandbox network', blocked: true },
];

test('a sandbox-net class: no host, no providers, unbound = no network', () => {
  const opts = netOptions({ org, providers: ['apps/vpn'], options: server, sandbox: true });
  const ids = opts.map((o) => o.id);
  assert.equal(opts[0].id, '');
  assert.match(opts[0].label, /no network/);
  assert.ok(!ids.includes('host'), 'host offered to a sandbox class');
  assert.ok(!ids.includes('apps/vpn'), 'a provider tile offered to a sandbox class');
  for (const id of ['org', 'internet', 'none', 'set:lab', 'set:infra', '__custom']) assert.ok(ids.includes(id), `missing ${id}`);
  const infra = opts.find((o) => o.id === 'set:infra');
  assert.equal(infra.disabled, true);
  assert.match(infra.label, /says host/);
  assert.ok(!opts.find((o) => o.id === 'set:lab').disabled);
});

test('a sandbox-net class keeps server blocks (outside the org sets, the owner allowance)', () => {
  const opts = netOptions({ org, options: [
    { id: 'internet', label: 'internet — public internet — not covered by the org\'s network sets', blocked: true },
    { id: 'none', label: 'none' },
  ], sandbox: true });
  const inet = opts.find((o) => o.id === 'internet');
  assert.equal(inet.disabled, true);
  assert.match(inet.label, /not covered/);
});

test('a pending class row never claims an org or personal default', () => {
  const pending = { component: 'apps/mgr', slot: 'lab', kind: 'sandbox-net', options: server };
  const opts = netOptions({ org, pending, sandbox: true });
  assert.doesNotMatch(opts[0].label, /default/);
});

test('a net slot is unchanged: org default, host and providers offered', () => {
  const opts = netOptions({ org, providers: ['apps/vpn'], options: server.filter((o) => o.id !== 'set:infra') });
  const ids = opts.map((o) => o.id);
  assert.match(opts[0].label, /default: org network/);
  assert.ok(ids.includes('host') && ids.includes('apps/vpn') && ids.includes('none'));
});
