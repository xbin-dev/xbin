// hack/netrules.test.mjs — the net-slot picker (web/bx-netrules.js
// netOptions), run by `make js-test`: a sandbox-net class (a sandbox
// manager's network, plans/tile-sandbox-runtime.md §4) never offers host or
// a provider tile and has no org/personal default; a net slot keeps both.
// And the bind prompt's (web/bx-bindings.js) starting pick: a pending class
// starts on none, never on internet (WP-11b).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { netOptions, bindPreselect, blockedTitle, ruleLabel, ruleGlyph, scopeGlyph, scopeIcon, SCOPE_ICON, SET_ICON, RULE_KINDS } from '../web/bx-netrules.js';

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

// GET /bindings' pending rows in server order (netBuiltinOptions): on a
// workspace tile internet is the first unblocked option.
const wsServer = [
  { id: 'internet', label: 'internet — public internet' },
  { id: 'none', label: 'none — no network for these sandboxes (the same as unbound)' },
  { id: 'set:infra', label: 'set:infra — network set (lan:10.0.0.0/8, host) — says host: never a sandbox network', blocked: true },
  { id: 'set:lab', label: 'set:lab — network set (lan:10.0.0.0/8)' },
];

test('the bind prompt starts a pending sandbox-net class on none, not internet', () => {
  assert.equal(bindPreselect({ component: 'apps/mgr', slot: 'internet', kind: 'sandbox-net', options: wsServer }), 'none');
  assert.equal(bindPreselect({ component: 'apps/mgr', slot: 'lab', kind: 'sandbox-net', options: server }), 'none'); // org first
  // none missing (never from today's server): nothing is preselected, not internet
  assert.equal(bindPreselect({ kind: 'sandbox-net', options: wsServer.filter((o) => o.id !== 'none') }), '');
});

test('the bind prompt starts any other slot on its first unblocked option (unchanged)', () => {
  const net = [{ id: 'internet', label: 'internet', blocked: true }, { id: 'host', label: 'host' }, { id: 'none', label: 'none' }];
  assert.equal(bindPreselect({ kind: 'net', options: net }), 'host');
  assert.equal(bindPreselect({ kind: 'http', service: 'openai', options: [{ id: 'apps/llm', label: 'apps/llm — openai' }] }), 'apps/llm');
  assert.equal(bindPreselect({ kind: 'net', options: [{ id: 'internet', blocked: true }] }), '');
  assert.equal(bindPreselect({ kind: 'net' }), '');
});

test('a blocked option says why: a set that says host is no sandbox network', () => {
  assert.equal(blockedTitle(wsServer.find((o) => o.id === 'set:infra')), 'a sandbox class can\'t reach the host');
  assert.match(blockedTitle({ label: 'set:lab — network set (lan:10.0.0.0/8) — workspace admins only', blocked: true }), /workspace admin/);
  assert.match(blockedTitle({ label: 'internet — public internet — outside your network allowance (ask a workspace admin)', blocked: true }), /allowance/);
  assert.match(blockedTitle({ label: 'set:vpn — network set (provider:apps/*) — provider-only, not bindable', blocked: true }), /provider-only/);
  assert.equal(blockedTitle({ label: 'internet — public internet — not covered by the org\'s network sets', blocked: true }), 'refused by the owning org\'s network sets');
});

// D184 — words carry no emoji: an option's label is its words (an <option>
// can't hold an icon), its glyph rides along as `icon`; a rule's words and
// glyph come apart the same way. What older consoles print before a label
// (RULE_KINDS' icon, SCOPE_ICON, SET_ICON, scopeIcon) is empty, so they print
// the words alone.
test('labels are words; glyphs ride along by name', () => {
  const pictograph = /[\u{1F300}-\u{1FAFF}☀-➿←-⇿]/u;
  const opts = netOptions({ org, providers: ['apps/vpn'], options: server, pending: { default: 'org' } });
  for (const o of opts) assert.doesNotMatch(o.label, pictograph, `no glyph in ${JSON.stringify(o.label)}`);
  const by = Object.fromEntries(opts.map((o) => [o.id, o]));
  assert.deepEqual([by.org.icon, by['set:lab'].icon, by.internet.icon, by.host.icon, by['apps/vpn'].icon, by.none.icon, by[''].icon],
    ['org', 'link', 'globe', 'network', 'plug', 'error', undefined]);
  assert.equal(by['apps/vpn'].label, 'via apps/vpn');
  assert.deepEqual(['internet', 'host', 'lan:10.0.0.0/8', 'provider:apps/vpn', 'internet:api.example.net:443'].map((r) => [ruleLabel(r), ruleGlyph(r)]),
    [['all internet', 'globe'], ['host networking', 'warning'], ['LAN 10.0.0.0/8', 'network'], ['via apps/vpn', 'plug'], ['to api.example.net:443', 'arrow-right']]);
  assert.deepEqual([scopeGlyph('org'), scopeGlyph('personal'), scopeGlyph('set:infra'), scopeGlyph('bogus'), scopeGlyph('toString')], ['org', 'person', 'link', '', '']);
  assert.deepEqual([scopeIcon('org'), SET_ICON, Object.values(SCOPE_ICON).join('')], ['', '', '']);
  for (const k of RULE_KINDS) { assert.equal(k.icon, '', k.id); assert.ok(k.glyph, k.id); }
});
