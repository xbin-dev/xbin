// The Swift port of the workspace agent chrome's words (XbinCore:
// DelegationWords.swift, D233) is held to the web's own
// (web/agent-chrome-words.js) by vectors: this test fails while the
// checked-in vectors differ from what the web's module says now —
// regenerate them (native/tools/delegation-words.mjs says how) and port the
// change to Swift in the same commit.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';

test('the delegation words vectors are what agent-chrome-words.js says now', async () => {
  const { default: out } = await import('../native/tools/delegation-words.mjs');
  const file = new URL('../native/ios/Packages/XbinCore/Tests/XbinCoreTests/Resources/delegation-words.json', import.meta.url);
  assert.deepEqual(JSON.parse(fs.readFileSync(file, 'utf8')), JSON.parse(JSON.stringify(out)),
    'run: node native/tools/delegation-words.mjs > native/ios/Packages/XbinCore/Tests/XbinCoreTests/Resources/delegation-words.json');
});
