// web/xb-native.js widgets (native/spec/tree.md §13): widget() renders a
// second tree, sent with target:"widget" only to an app whose caps list the
// "widget" feature; per-target keys, handlers, counters and remounts; the
// size class; the widget vocabulary. An app without the feature sees exactly
// what it saw before widgets existed.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installHooks } from './xbn/hooks.mjs';

installHooks();
const XB = new URL('../web/xb-native.js', import.meta.url).href;
const { html, repeat, createRuntime } = await import(XB);
const { VOCAB, fullCaps } = await import('/vendor/xb/vocab.js');

const withWidget = (extra = {}) => ({ ...fullCaps(), features: [...fullCaps().features, 'widget'], ...extra });
function mk(caps) {
  const msgs = [];
  const rt = createRuntime({ post: (m) => msgs.push(m), schedule: () => {}, log: false, caps });
  return { rt, msgs };
}

// A tile that renders both trees and re-renders on events and size changes.
function counterTile(rt, log = []) {
  let count = 1;
  const paint = () => {
    rt.render(html`<screen title="Counter"><row title="Count" detail=${count}/><button @tap=${() => { log.push('main'); count++; paint(); }}>+1</button></screen>`);
    rt.widget(html`<stack><text style="largeTitle">${count}</text>${rt.native.widgetSize === 'wide' ? html`<text>wide</text>` : ''}<button icon="plus" @tap=${() => { log.push('widget'); count++; paint(); }}>+1</button></stack>`);
  };
  paint();
  return paint;
}

test('gating: without the "widget" feature widget() sends nothing and the main tree is byte-identical', () => {
  for (const caps of [undefined, fullCaps(), { v: 1, prims: { screen: 1, row: 1, button: 1, stack: 1, text: 1 }, features: ['chart.area'] }]) {
    const a = mk(caps);
    const b = mk(caps);
    counterTile(a.rt);
    a.rt.flush();
    b.rt.render(html`<screen title="Counter"><row title="Count" detail=${1}/><button @tap=${() => {}}>+1</button></screen>`);
    b.rt.flush();
    assert.equal(JSON.stringify(a.msgs), JSON.stringify(b.msgs), 'same messages, byte for byte');
    assert.ok(a.msgs.every((m) => !('target' in m)));
    assert.equal(a.rt.widgetTree, null);
    // the app's calls about widgets do nothing either
    assert.equal(a.rt.xbn.event('r.0', 'tap', {}, 1, 'widget'), false);
    assert.equal(a.rt.xbn.remount('widget'), false);
    assert.equal(a.rt.xbn.widgetSize('wide'), false);
    assert.equal(a.rt.native.widgetSize, 'small');
    assert.equal(a.rt.native.supports('widget'), false);
    assert.equal(a.msgs.length, 1);
  }
});

test('targets: widget trees are mount/patch with target:"widget", their own n; the main tree is unchanged', () => {
  const { rt, msgs } = mk(withWidget());
  const log = [];
  counterTile(rt, log);
  assert.equal(rt.native.supports('widget'), true);
  rt.flush();
  assert.deepEqual(msgs.map((m) => [m.op, m.target, m.n]), [['mount', undefined, 1], ['mount', 'widget', 1]]);
  assert.ok(!('target' in msgs[0]));
  assert.deepEqual(Object.keys(msgs[1]), ['op', 'target', 'v', 'n', 'root']);
  assert.deepEqual(msgs[1].root, { k: 'r', t: 'stack', c: [
    { k: 'r.0', t: 'text', p: { text: '1', style: 'largeTitle' } },
    { k: 'r.2', t: 'button', p: { label: '+1', icon: 'plus' }, e: ['tap'] }] });
  assert.deepEqual(rt.widgetTree, { v: 1, root: msgs[1].root });

  // a widget tap runs the widget's handler (the main tree has a different node at that key)
  assert.equal(rt.xbn.event('r.2', 'tap', {}, 1, 'widget'), true);
  rt.flush();
  assert.deepEqual(log, ['widget']);
  assert.deepEqual(msgs.slice(2).map((m) => [m.op, m.target, m.n]), [['patch', undefined, 2], ['patch', 'widget', 2]]);
  assert.deepEqual(msgs[3].ops, [['set', 'r.0', { text: '2' }]]);
  assert.deepEqual(Object.keys(msgs[3]), ['op', 'target', 'n', 'ops']);
});

test('per-target events: a key and a handler belong to one tree', () => {
  const { rt, msgs } = mk(withWidget());
  const got = [];
  rt.render(html`<button @tap=${() => got.push('main')}>m</button>`);
  rt.widget(html`<button @tap=${() => got.push('widget')}>w</button>`);
  rt.flush();
  assert.equal(msgs[0].root.k, 'r');
  assert.equal(msgs[1].root.k, 'r');
  assert.equal(rt.xbn.event('r', 'tap', {}, 1), true);
  assert.equal(rt.xbn.event('r', 'tap', {}, 1, undefined), true);
  assert.equal(rt.xbn.event('r', 'tap', {}, 1, 'widget'), true);
  assert.deepEqual(got, ['main', 'main', 'widget']);
  assert.equal(rt.xbn.event('r', 'tap', {}, 1, 'sidebar'), false, 'an unknown target is ignored');
  // a widget without a handler at a key the main tree handles runs nothing
  rt.widget(html`<text>no handlers</text>`);
  rt.flush();
  assert.equal(rt.xbn.event('r', 'tap', {}, 2, 'widget'), false);
  assert.deepEqual(got, ['main', 'main', 'widget']);
});

test('width: caps.width is the screen\'s width class (null when the app does not say); xbn.width changes it and tells listeners', () => {
  assert.equal(mk(withWidget()).rt.native.width, null);
  assert.equal(mk(withWidget({ width: 'huge' })).rt.native.width, null);
  const { rt } = mk(withWidget({ width: 'compact' }));
  assert.equal(rt.native.width, 'compact');
  const heard = [];
  const off = rt.native.on('width', (w) => heard.push(w));
  assert.equal(rt.xbn.width('compact'), false, 'no change');
  assert.equal(rt.xbn.width('wide'), false, 'not a width class');
  assert.equal(rt.xbn.width('regular'), true);
  assert.equal(rt.native.width, 'regular');
  off();
  rt.xbn.width('compact');
  assert.deepEqual(heard, ['regular']);
});

test('widgetSize: caps.widgetSize is the initial size; xbn.widgetSize changes it and tells listeners', () => {
  assert.equal(mk(withWidget()).rt.native.widgetSize, 'small');
  assert.equal(mk(withWidget({ widgetSize: 'bogus' })).rt.native.widgetSize, 'small');
  const { rt, msgs } = mk(withWidget({ widgetSize: 'wide' }));
  assert.equal(rt.native.widgetSize, 'wide');
  assert.equal(rt.native.caps.widgetSize, 'wide');
  const paint = counterTile(rt);
  const heard = [];
  const off = rt.native.on('widgetsize', (s) => { heard.push(s); paint(); });
  rt.flush();
  assert.equal(msgs[1].root.c.length, 3, 'wide shows the extra text');
  assert.equal(rt.xbn.widgetSize('wide'), false, 'no change');
  assert.equal(rt.xbn.widgetSize('huge'), false, 'not a size class');
  assert.equal(rt.xbn.widgetSize('small'), true);
  assert.equal(rt.native.widgetSize, 'small');
  rt.flush();
  assert.deepEqual(heard, ['small']);
  assert.deepEqual(msgs.slice(2).map((m) => [m.op, m.target]), [['patch', 'widget']], 'the main tree did not change');
  assert.deepEqual(msgs[2].ops, [['remove', 'r.1']]);
  off();
  rt.xbn.widgetSize('wide');
  assert.deepEqual(heard, ['small']);
  assert.throws(() => rt.native.on('nope', () => {}), /unknown event/);
  // a throwing listener is the tile's uncaught error, not the app's
  rt.native.on('widgetsize', () => { throw new Error('boom'); });
  rt.xbn.widgetSize('small');
  assert.deepEqual(msgs.filter((m) => m.op === 'error').map((m) => [m.kind, m.message]), [['uncaught', 'boom']]);
});

test('remount(target): each tree is sent again on its own', () => {
  const { rt, msgs } = mk(withWidget());
  counterTile(rt);
  rt.flush();
  msgs.length = 0;
  assert.equal(rt.xbn.remount('widget'), true);
  assert.deepEqual(msgs.map((m) => [m.op, m.target, m.n]), [['mount', 'widget', 2]]);
  assert.equal(rt.xbn.remount(), true);
  assert.deepEqual(msgs.map((m) => [m.op, m.target, m.n]), [['mount', 'widget', 2], ['mount', undefined, 2]]);
  // before the tile rendered a widget there is nothing to remount
  const b = mk(withWidget());
  b.rt.render(html`<text>x</text>`);
  b.rt.flush();
  assert.equal(b.rt.xbn.remount('widget'), false);
});

test('the widget vocabulary: other primitives are dropped with a widget-tag error diagnostic', () => {
  assert.deepEqual(VOCAB.widget, { feature: 'widget', sizes: ['small', 'wide'], prims: ['stack', 'text', 'icon', 'badge', 'chart', 'progress', 'button', 'row'] });
  const { rt, msgs } = mk(withWidget());
  rt.render(html`<screen><list><row title="fine in the main tree"/></list></screen>`);
  rt.widget(html`<stack><list><row title="x"/></list><badge tone="ok">3</badge><field label="no"/>tail</stack>`);
  rt.flush();
  const d = msgs.filter((m) => m.op === 'diag');
  assert.deepEqual(d.map((m) => [m.code, m.level, m.target]), [['widget-tag', 'error', 'widget'], ['widget-tag', 'error', 'widget']]);
  assert.match(d[0].message, /<list> cannot be in a widget/);
  const w = msgs.find((m) => m.op === 'mount' && m.target === 'widget');
  assert.deepEqual(w.root.c.map((c) => [c.k, c.t]), [['r.1', 'badge'], ['r.3', 'text']], 'keys do not shift');
  const m = msgs.find((x) => x.op === 'mount' && !x.target);
  assert.equal(m.root.c[0].t, 'list');
});

test('errors about the widget name it: unsupported, exceptions', () => {
  const caps = { v: 1, prims: { stack: 1, text: 1, chart: 1 }, features: ['widget'] };
  const { rt, msgs } = mk(caps);
  rt.render(html`<text>main</text>`);
  rt.widget(html`<stack><chart kind="area"/></stack>`);
  rt.flush();
  assert.deepEqual(msgs.map((m) => [m.op, m.kind, m.target]), [['mount', undefined, undefined], ['error', 'unsupported', 'widget'], ['mount', undefined, 'widget']]);
  rt.widget(html`<stack>${repeat([1], () => { throw new Error('bad item'); })}</stack>`);
  rt.flush();
  assert.deepEqual([msgs.at(-1).op, msgs.at(-1).kind, msgs.at(-1).target], ['error', 'exception', 'widget']);
  // the same finding in both trees is reported for each
  rt.render(html`<stack><chart kind="area"/></stack>`);
  rt.flush();
  assert.equal(msgs.filter((m) => m.op === 'error' && m.kind === 'unsupported').length, 2);
});

test('the default runtime: widget() and xbn with a target, over webkit', async () => {
  const posted = [];
  globalThis.webkit = { messageHandlers: { xbn: { postMessage: (s) => posted.push(JSON.parse(s)) } } };
  const injected = { caps: withWidget({ renderer: 'ios', widgetSize: 'wide' }), state: null };
  globalThis.xbin = { self: 'apps/x', native: injected };
  try {
    const xb = await import(`${XB}?fresh=widget`);
    const got = [];
    xb.render(xb.html`<button @tap=${() => got.push('main')}>m</button>`);
    xb.widget(xb.html`<button @tap=${() => got.push(`widget ${xb.native.widgetSize}`)}>w</button>`);
    globalThis.xbn.frame();
    assert.deepEqual(posted.map((m) => [m.op, m.target]), [['mount', undefined], ['mount', 'widget']]);
    assert.equal(globalThis.xbin.native.widgetSize, 'wide');
    assert.equal(typeof globalThis.xbin.native.on, 'function');
    const sizes = [];
    globalThis.xbin.native.on('widgetsize', (s) => sizes.push(s));
    assert.equal(globalThis.xbn.widgetSize('small'), true);
    assert.deepEqual(sizes, ['small']);
    assert.equal(globalThis.xbn.event('r', 'tap', {}, 1, 'widget'), true);
    assert.equal(globalThis.xbn.event('r', 'tap', {}, 1), true);
    assert.deepEqual(got, ['widget small', 'main']);
    assert.equal(globalThis.xbn.remount('widget'), true);
    assert.deepEqual(posted.at(-1), { op: 'mount', target: 'widget', v: 1, n: 2, root: { k: 'r', t: 'button', p: { label: 'w' }, e: ['tap'] } });
  } finally { delete globalThis.webkit; delete globalThis.xbin; }
});
