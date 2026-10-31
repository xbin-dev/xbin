// hack/drag-pointer.test.mjs — the kit's pointer drag (web/bx-kit.js
// dragPointer, D217), run by `make js-test`: node's built-in runner over a
// stand-in window and document. The rule under test: the shield that keeps
// tile iframes from taking the pointer comes down on EVERY end — pointerup,
// a mouse move with no button held, Escape, the window losing focus, a
// cancelled pointer, a lost capture — and the caller hears exactly one of
// onUp / onCancel, once. The shield holds the pointer (pointer capture).
import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

class Shield extends EventTarget {
  constructor() { super(); this.style = {}; this.dataset = {}; this.captured = null; this.removed = false; }
  setPointerCapture(id) { if (id < 0) throw new Error('NotFoundError'); this.captured = id; }
  remove() { this.removed = true; if (document.activeElement === this) document.activeElement = document.body; }
  focus() { this.focused = true; document.activeElement = this; }
}
let shields = [];
globalThis.window = new EventTarget();
globalThis.document = { activeElement: null, createElement: () => new Shield(), body: { tagName: 'BODY', appendChild: (el) => shields.push(el) } };
const { dragPointer } = await import('../web/bx-kit.js');

const ev = (type, props = {}) => Object.assign(new Event(type), { pointerId: 7, pointerType: 'mouse', buttons: 1, clientX: 0, clientY: 0, ...props });
const send = (type, props) => window.dispatchEvent(ev(type, props));
const tick = () => new Promise((r) => setTimeout(r, 5));
// a drag recording what the caller hears
function drag(opts = {}) {
  const log = [];
  const end = dragPointer({
    onMove: (e) => log.push(['move', e.clientX]),
    onUp: (e) => log.push(['up', e?.clientX]),
    ...(opts.cancel === false ? {} : { onCancel: () => log.push(['cancel']) }),
    ...opts,
  });
  return { log, end, shield: shields.at(-1) };
}
const moveIgnored = (log) => { const n = log.length; send('pointermove', { clientX: 999 }); return log.length === n; };

beforeEach(() => { shields = []; });

test('pointerup: onUp with the event, the shield gone, nothing after', () => {
  const d = drag({ ev: ev('pointerdown', { clientX: 1 }) });
  assert.equal(d.shield.captured, 7, 'the shield holds the pointer from the start');
  assert.equal(d.shield.dataset.dragShield, '');
  send('pointermove', { clientX: 10 });
  send('pointerup', { clientX: 12, buttons: 0 });
  assert.deepEqual(d.log, [['move', 10], ['up', 12]]);
  assert.ok(d.shield.removed);
  assert.ok(moveIgnored(d.log));
  d.shield.dispatchEvent(new Event('lostpointercapture')); // the release's own: nothing more
  assert.deepEqual(d.log, [['move', 10], ['up', 12]]);
});

test('no start event: the first move captures', () => {
  const d = drag();
  assert.equal(d.shield.captured, null);
  send('pointermove', { clientX: 3 });
  assert.equal(d.shield.captured, 7);
  d.end();
  assert.deepEqual(d.log, [['move', 3], ['up', 3]], 'the returned function ends it with the last move');
  assert.ok(d.shield.removed);
});

test('a synthetic start (no active pointer) still drags', () => {
  const d = drag({ ev: ev('pointerdown', { pointerId: -1 }) });
  assert.equal(d.shield.captured, null);
  send('pointerup');
  assert.ok(d.shield.removed);
});

test('Escape: the shield goes, onCancel (after the event), the key kept from below', async () => {
  const d = drag({ ev: ev('pointerdown') });
  send('pointermove', { clientX: 5 });
  const k = Object.assign(new Event('keydown', { cancelable: true }), { key: 'Escape' });
  window.dispatchEvent(k);
  assert.ok(k.defaultPrevented);
  await tick();
  assert.ok(d.shield.removed);
  assert.deepEqual(d.log, [['move', 5], ['cancel']]);
  assert.ok(moveIgnored(d.log));
  send('pointerup'); // the button let go afterwards
  assert.deepEqual(d.log, [['move', 5], ['cancel']]);
});

test('other keys do nothing', async () => {
  const d = drag();
  window.dispatchEvent(Object.assign(new Event('keydown'), { key: 'a' }));
  await tick();
  assert.ok(!d.shield.removed);
  d.end();
});

test('without onCancel, a cancelled drag ends with onUp at the last move', () => {
  const d = drag({ cancel: false });
  send('pointermove', { clientX: 8 });
  window.dispatchEvent(new Event('blur'));
  assert.ok(d.shield.removed);
  assert.deepEqual(d.log, [['move', 8], ['up', 8]]);
});

for (const [what, fire] of [
  ['the window losing focus', () => window.dispatchEvent(new Event('blur'))],
  ['a cancelled pointer', () => send('pointercancel')],
  ['a lost capture', (d) => d.shield.dispatchEvent(new Event('lostpointercapture'))],
]) {
  test(`${what}: the shield goes, onCancel once`, () => {
    const d = drag({ ev: ev('pointerdown') });
    fire(d); fire(d);
    assert.ok(d.shield.removed);
    assert.deepEqual(d.log, [['cancel']]);
    assert.ok(moveIgnored(d.log));
  });
}

test('a mouse move with no button held is a release nobody saw: onUp at the last move', () => {
  const d = drag({ ev: ev('pointerdown') });
  send('pointermove', { clientX: 4 });
  send('pointermove', { clientX: 400, buttons: 0 });
  assert.ok(d.shield.removed);
  assert.deepEqual(d.log, [['move', 4], ['up', 4]]);
});

test('a touch or pen move is not judged by its buttons', () => {
  const d = drag();
  send('pointermove', { clientX: 4, pointerType: 'touch', buttons: 0 });
  assert.ok(!d.shield.removed);
  d.end();
});

test('a callback that throws leaves no shield', () => {
  const d = drag({ onUp: () => { throw new Error('boom'); } });
  assert.throws(() => d.end());
  assert.ok(d.shield.removed);
});

// an older shell's tiled drag (D205) kept its own Escape at window capture
// and ended the drag through the returned function: it still decides
test("a caller's own Escape, registered after the drag began, is heard first", async () => {
  let cancelled = false;
  const log = [];
  const end = dragPointer({ onUp: () => log.push(cancelled ? 'up-cancelled' : 'up-commit'), onMove: () => {} });
  const shield = shields.at(-1);
  window.addEventListener('keydown', (k) => { if (k.key === 'Escape') { cancelled = true; end(); } }, true);
  window.dispatchEvent(Object.assign(new Event('keydown'), { key: 'Escape' }));
  await tick();
  assert.deepEqual(log, ['up-cancelled']);
  assert.ok(shield.removed);
});

// a press on the chrome is cancelled, so a tile's page that had the keyboard
// keeps it — and Escape would never reach the shell: the shield takes it for
// the drag and gives it back at the end
const page = () => ({ tagName: 'IFRAME', isConnected: true, focus() { document.activeElement = this; } });
test("a tile's page with the keyboard gives it to the shield, and gets it back; the shell's own focus stays", async () => {
  const p = page();
  document.activeElement = p;
  const d = drag();
  assert.ok(d.shield.focused);
  assert.equal(document.activeElement, d.shield, 'the shield has it during the drag');
  send('pointerup', { buttons: 0 });
  assert.equal(document.activeElement, p, 'the page has it again after the release');
  const c = drag(); // Escape: the same, after the cancel
  window.dispatchEvent(Object.assign(new Event('keydown'), { key: 'Escape' }));
  await tick();
  assert.deepEqual(c.log, [['cancel']]);
  assert.equal(document.activeElement, p, 'the page has it again after Escape');
  const t = { tagName: 'TEXTAREA' }; // a terminal in the shell: left alone
  document.activeElement = t;
  const e = drag();
  assert.ok(!e.shield.focused);
  e.end();
  assert.equal(document.activeElement, t);
  document.activeElement = null;
});

test("the keyboard isn't given back where a callback moved it, nor to a page that's gone", () => {
  const p = page(), other = { tagName: 'INPUT' };
  document.activeElement = p;
  drag({ onUp: () => { document.activeElement = other; } }).end();
  assert.equal(document.activeElement, other, "the callback's choice stands");
  document.activeElement = p;
  const d = drag();
  p.isConnected = false; // re-rendered away mid-drag
  d.end();
  assert.equal(document.activeElement, document.body);
  document.activeElement = null;
});
