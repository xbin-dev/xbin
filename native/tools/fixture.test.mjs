// The fixture runner's plain parts: data.json → a run, the expected.json
// layout, the readable tree diff and the vocabulary coverage.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { toRun, toSteps, format, treeDiff, firstDiff } from './fixture-lib.mjs';
import { required, seen, report } from './coverage.mjs';
import { VOCAB } from '../../web/xb/vocab.js';

test('data.json → runNative options: defaults pin now, tz and locale', () => {
  const r = toRun('/x/native.js', { self: 'apps/x', routes: { '/a': { json: 1 } }, interactions: [{ after: 5 }] });
  assert.equal(r.data.now, Date.parse('2026-09-21T14:13:20Z'));
  assert.equal(r.data.tz, 'UTC');
  assert.equal(r.data.locale, 'en-US');
  assert.deepEqual(r.steps, [{ wait: 5 }]);
  assert.equal(toRun('/x', { now: 1790000000000, tz: 'Europe/Warsaw' }).data.tz, 'Europe/Warsaw');
  assert.throws(() => toRun('/x', { rotues: {} }), /unknown key "rotues"/);
  assert.throws(() => toRun('/x', { now: 'yesterday' }), /not a time/);
});

test('interactions → steps', () => {
  assert.deepEqual(toSteps([
    { select: 'button[label=Save]', type: 'tap' },
    { after: 1000, k: 'r.0.1', type: 'input', payload: { value: 'x' }, n: 3 },
    { after: 50, bus: ['res:apps/x/bus/a', { id: 1 }] },
    { bus: { topic: 't', data: 2 } },
    { visibility: 'hidden' },
    { resolve: ['c1', true] },
    { after: 3000, note: 'a poll' },
  ]), [
    { event: { type: 'tap', payload: {}, select: 'button[label=Save]' } },
    { wait: 1000 }, { event: { type: 'input', payload: { value: 'x' }, k: 'r.0.1', n: 3 } },
    { wait: 50 }, { bus: ['res:apps/x/bus/a', { id: 1 }] },
    { bus: ['t', 2] },
    { visibility: 'hidden' },
    { resolve: ['c1', true] },
    { wait: 3000 },
  ]);
  assert.throws(() => toSteps([{ k: 'r', select: 'x', type: 'tap' }]), /k or select, not both/);
  assert.throws(() => toSteps([{ k: 'r' }]), /needs a "type"/);
  assert.throws(() => toSteps([{ type: 'tap' }]), /needs "k" or "select"/);
  assert.throws(() => toSteps([{ k: 'r', type: 'tap', bus: ['t', 1] }]), /one action/);
  assert.throws(() => toSteps([{ tap: 'r' }]), /unknown key "tap"/);
  assert.throws(() => toSteps([{ after: -1 }]), /non-negative/);
});

const TREE = { v: 1, root: { k: 'r', t: 'screen', p: { title: 'T' }, c: [
  { k: 'r.0', t: 'section', c: [
    { k: 'r.0.0', t: 'row', p: { title: 'a', detail: '1' }, e: ['tap'] },
    { k: 'r.0.1', t: 'button', p: { label: 'Go' }, e: ['tap'] },
    { k: 'r.0.2', t: 'stack', c: [] },
  ] },
] } };

test('format: one node per line, valid JSON, wire key order', () => {
  const text = format(TREE);
  assert.equal(text, [
    '{"v":1,"root":{"k":"r","t":"screen","p":{"title":"T"},"c":[',
    '  {"k":"r.0","t":"section","c":[',
    '    {"k":"r.0.0","t":"row","p":{"title":"a","detail":"1"},"e":["tap"]},',
    '    {"k":"r.0.1","t":"button","p":{"label":"Go"},"e":["tap"]},',
    '    {"k":"r.0.2","t":"stack","c":[]}',
    '  ]}',
    ']}}',
    '',
  ].join('\n'));
  assert.deepEqual(JSON.parse(text), TREE);
  const leaf = { v: 1, root: { e: ['x'], t: 'text', k: 'r', p: { text: 'hi' } } };
  assert.equal(format(leaf), '{"v":1,"root":{"k":"r","t":"text","p":{"text":"hi"},"e":["x"]}}\n');
  // markdown tokens: a line per block
  const md = { v: 1, root: { k: 'r', t: 'screen', c: [{ k: 'r.0', t: 'markdown', p: { tokens: [{ t: 'hr' }, { t: 'paragraph', c: [] }], streaming: false }, e: ['link'] }] } };
  assert.equal(format(md), [
    '{"v":1,"root":{"k":"r","t":"screen","c":[',
    '  {"k":"r.0","t":"markdown","p":{"tokens":[',
    '      {"t":"hr"},',
    '      {"t":"paragraph","c":[]}',
    '    ],"streaming":false},"e":["link"]}',
    ']}}',
    '',
  ].join('\n'));
  assert.deepEqual(JSON.parse(format(md)), md);
});

test('treeDiff names nodes by key and says what changed', () => {
  assert.deepEqual(treeDiff(TREE, structuredClone(TREE)), []);
  const b = structuredClone(TREE);
  const sec = b.root.c[0];
  sec.c[0].p.detail = '2';
  sec.c[1].e = [];
  sec.c.reverse();
  sec.c.push({ k: 'r.0.3', t: 'notice', p: { tone: 'ok', text: 'saved' } });
  sec.c = sec.c.filter((c) => c.k !== 'r.0.2');
  assert.deepEqual(treeDiff(TREE, b), [
    '- r.0.2 (stack) missing from r.0',
    '+ r.0.3 (notice) unexpected in r.0: {"tone":"ok","text":"saved"}',
    '~ r.0 (section) children order: expected ["r.0.0","r.0.1"], got ["r.0.1","r.0.0"]',
    '~ r.0.0 (row) p.detail: expected "1", got "2"',
    '~ r.0.1 (button) e: expected ["tap"], got []',
  ]);
  const c = structuredClone(TREE);
  c.root.c[0].c[0].p.chips = [{ text: 'x' }];
  c.root.c[0].c[1].t = 'toggle';
  delete c.root.c[0].c[2].c;
  assert.deepEqual(treeDiff(TREE, c), [
    '~ r.0.0 (row) p.chips: unexpected [{"text":"x"}]',
    '~ r.0.1: type expected button, got toggle',
    '~ r.0.2 (stack) c: expected [], got absent',
  ]);
  assert.deepEqual(firstDiff({ a: [1, { b: 2 }] }, { a: [1, { b: 3 }] }), ['.a[1].b', 2, 3]);
});

test('coverage: the vocabulary defines the items, trees exercise them', () => {
  const req = required(VOCAB);
  for (const item of ['prim fragment', 'event composer@uploaded', 'prop button.confirm.destructive', 'value field.kind=secure',
    'value text.style=caption2', 'value stack.gap=xxl', 'token icon=sparkles', 'token height=xl', 'child list>notice',
    'value picker.options[].icon=plus', 'md heading depth=6', 'md table align=none']) {
    assert.equal(req.has(item), item !== 'value picker.options[].icon=plus', item);
  }
  assert.ok(!req.has('prop markdown.tokens'), 'runtime props are not required');
  const got = seen(VOCAB, { v: 1, root: { k: 'r', t: 'list', p: { style: 'inset' }, e: ['more'], c: [
    { k: 'r.0', t: 'row', p: { title: 'a', tone: 'ok', nav: true, icon: 'gear' } },
    { k: 'r.1', t: 'markdown', p: { tokens: [{ t: 'heading', depth: 2, c: [{ t: 'strong', c: [{ t: 'text', text: 'x' }] }] },
      { t: 'list', ordered: true, start: 3, loose: false, items: [{ c: [], task: true, checked: false }] }] } },
    { k: 'r.2', t: 'picker', p: { options: [{ value: 1, label: 'One', icon: 'plus' }] } },
  ] } });
  for (const item of ['prim list', 'event list@more', 'value list.style=inset', 'child list>row', 'child list>markdown',
    'value row.tone=ok', 'value row.nav=true', 'token icon=gear', 'md heading depth=2', 'md inline strong', 'md list ordered',
    'md list start>1', 'md list task unchecked', 'md list tight', 'prop picker.options[].value', 'token icon=plus']) {
    assert.ok(got.has(item), item);
  }
  const r = report(VOCAB, { one: { v: 1, root: { k: 'r', t: 'spacer' } } });
  assert.equal(r.total, req.size);
  assert.deepEqual(r.by['prim spacer'], ['one']);
  assert.ok(r.missing.includes('prim divider'));
});
