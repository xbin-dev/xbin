// hack/agent-template-boot.test.mjs — the agent template's entry
// (builtin-templates/agent/boot.js) and the chat kit's revision
// (web/chat/rev.js, D224): the template never asks for more than the xbind
// it ships in serves; a revision that is missing, failing or no number reads
// as no kit. The page itself — the message on an older xbind, the app on
// this one — is the template's test/needs-xbind.mjs. The native view
// (native.js) reaches the kit through the template's models and cannot ask
// first: on an older xbind it must fail to load, whole — the app then shows
// the web page, which says what it needs. Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { runNative } from './xbn/node.mjs';

const { NEEDS, kitRevision } = await import('../builtin-templates/agent/boot.js');
const { KIT } = await import('../web/chat/rev.js');

test('the kit\'s revision is a whole number, and the template needs no more than this xbind serves', () => {
  assert.ok(Number.isInteger(KIT) && KIT >= 1, `KIT = ${KIT}`);
  assert.ok(Number.isInteger(NEEDS.kit) && NEEDS.kit >= 1 && NEEDS.kit <= KIT, `the template needs ${NEEDS.kit}, xbind serves ${KIT}`);
  assert.match(NEEDS.xbind, /^v\d+\.\d+\.\d+$/, 'the release the message names');
});

test('kitRevision: the number xbind serves, 0 for anything else', async () => {
  assert.equal(await kitRevision(async () => ({ KIT: 3 })), 3);
  assert.equal(await kitRevision(async () => ({ KIT })), KIT);
  assert.equal(await kitRevision(async () => { throw new TypeError('Failed to fetch dynamically imported module'); }), 0, 'a 404');
  assert.equal(await kitRevision(async () => ({})), 0, 'a module without KIT');
  assert.equal(await kitRevision(async () => ({ KIT: 'soon' })), 0);
  assert.equal(await kitRevision(async () => ({ KIT: -1 })), 0);
});

test('index.html starts the page through boot.js, and boot.js loads agent.js', () => {
  const dir = new URL('../builtin-templates/agent/', import.meta.url);
  const page = readFileSync(new URL('index.html', dir), 'utf8');
  assert.deepEqual([...page.matchAll(/<script type="module" src="([^"]+)">/g)].map((m) => m[1]), ['boot.js']);
  assert.match(readFileSync(new URL('boot.js', dir), 'utf8'), /import\('\.\/agent\.js'\)/);
});

// ---- the native view on an older xbind -------------------------------------------------

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const native = (data) => runNative({ entry: fileURLToPath(new URL('native.js', TPL)), data: { self: 'apps/agent', now: Date.UTC(2026, 10, 14, 12), ...data } });
const OLDER = fileURLToPath(new URL('./xbn/older-xbind.mjs', import.meta.url));

test('the native view reaches the kit through the template\'s models, at its served URLs', () => {
  const src = (f) => readFileSync(new URL(f, TPL), 'utf8');
  const kit = (f) => [...src(f).matchAll(/from '(\/vendor\/chat\/[\w.-]+)'/g)].map((m) => m[1]);
  assert.deepEqual([...new Set(kit('model/harness-heads.js'))].sort(), ['/vendor/chat/blocks.js', '/vendor/chat/tools.js']);
  assert.deepEqual([...new Set(kit('model/harness-ask.js'))].sort(), ['/vendor/chat/ask.js', '/vendor/chat/tools.js']);
  assert.deepEqual(kit('model/harness-signins.js'), ['/vendor/chat/ask.js']);
  assert.ok(NEEDS.kit >= 2, 'tools.js and ask.js came with revision 2');
});

test('the native view on this xbind: a tree', async () => {
  const r = await native({ routes: { '/whoami': { json: { id: 'alice', kind: 'user' } } } });
  assert.deepEqual(r.errors.filter((e) => e.kind === 'module'), []);
  assert.ok(r.tree && r.tree.root, 'native.js loaded and drew');
});

for (const [what, missing] of [
  ['an xbind from before the chat kit', '^/vendor/chat/'],
  ['an xbind that serves revision 1 of it (no tools.js, no ask.js)', '^/vendor/chat/(tools|ask)\\.js$'],
]) {
  test(`the native view on ${what}: native.js fails to load — one module error, no tree, nothing asked — and the app shows the web page`, async () => {
    const r = await native({ setup: OLDER, missing });
    assert.equal(r.fatal, null);
    assert.equal(r.tree, null, 'no tree: nothing of the view ran');
    assert.equal(r.errors.length, 1, JSON.stringify(r.errors));
    assert.equal(r.errors[0].kind, 'module', 'what the app answers by showing the web page (/docs/native.md → Fallback)');
    assert.match(JSON.stringify(r.errors[0]), /\/vendor\/chat\/(blocks|tools|ask)\.js/, 'the error names the module that is not served');
    assert.deepEqual(r.requests, [], 'the backend is not asked: no half-started view');
    assert.deepEqual(r.messages.filter((m) => m.op === 'mount' || m.op === 'patch'), []);
  });
}
