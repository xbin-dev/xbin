// hack/tile-meta.test.mjs — a tile's title, icon and badge (D194,
// web/tile-meta.js), run by `make js-test`: the caps,
// what is cleaned out, null clearing a field, and which source wins.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { CAPS, cleanMeta, mergeMeta, titleOf, iconOf, nameOf } from '../web/tile-meta.js';

test('cleanMeta(): bounded, plain, icon names only; null clears', () => {
  assert.deepEqual(CAPS, { title: 80, icon: 40, badge: 8 });
  assert.deepEqual(cleanMeta({ title: 'x'.repeat(200), icon: 'mail', badge: 123456789 }), { title: 'x'.repeat(80), icon: 'mail', badge: '12345678' });
  assert.deepEqual(cleanMeta({ title: '  Inbox\n(3)‮  ' }), { title: 'Inbox (3)' }, 'control and bidi characters go, space collapses');
  assert.deepEqual(cleanMeta({ icon: '<svg onload=x>' }), {}, 'not an icon name: dropped');
  assert.deepEqual(cleanMeta({ icon: 'eye-slash' }), { icon: 'eye-slash' });
  assert.deepEqual(cleanMeta({ title: null, badge: '' }), { title: null, badge: null });
  assert.deepEqual(cleanMeta({ title: { toString: () => 'evil' } }), {}, 'only strings and numbers');
  assert.deepEqual(cleanMeta(null), {});
  assert.deepEqual(cleanMeta({ other: 'x' }), {}, 'other fields are not carried');
});

test('mergeMeta(): only the given fields change', () => {
  let m = mergeMeta(null, { title: 'Inbox', badge: 3 });
  assert.deepEqual(m, { title: 'Inbox', badge: '3' });
  m = mergeMeta(m, { badge: null, icon: 'mail' });
  assert.deepEqual(m, { title: 'Inbox', icon: 'mail' });
});

test('titleOf()/iconOf(): the page over the manifest over the path', () => {
  assert.equal(nameOf('apps/mail'), 'mail');
  assert.equal(titleOf('apps/mail', null, null), 'mail');
  assert.equal(titleOf('apps/mail', { title: 'Mail' }, null), 'Mail');
  assert.equal(titleOf('apps/mail', { title: 'Mail' }, { title: 'Inbox (3)' }), 'Inbox (3)');
  assert.equal(titleOf('apps/mail', { title: '\u0000' }, {}), 'mail', 'a manifest title that cleans to nothing');
  assert.equal(iconOf({ icon: 'mail' }, null), 'mail');
  assert.equal(iconOf({ icon: 'mail' }, { icon: 'bell' }), 'bell');
  assert.equal(iconOf({ icon: 'nope' }, null, (n) => n === 'mail'), '', 'not in the set: none');
  assert.equal(iconOf({ icon: 'Bad Name' }, null), '');
});
