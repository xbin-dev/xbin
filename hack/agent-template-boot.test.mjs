// hack/agent-template-boot.test.mjs — the agent template's entry
// (builtin-templates/agent/boot.js) and the chat kit's revision
// (web/chat/rev.js, D224): the template never asks for more than the xbind
// it ships in serves; a revision that is missing, failing or no number reads
// as no kit. The page itself — the message on an older xbind, the app on
// this one — is the template's test/needs-xbind.mjs. Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

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
