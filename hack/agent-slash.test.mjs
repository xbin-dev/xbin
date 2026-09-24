// hack/agent-slash.test.mjs — the Agent tab's slash-command completion
// (web/agent-slash.js, D77), run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { slashQuery, matchCommands, commandHint } from '../web/agent-slash.js';

const cmds = [
  { name: 'review', description: 'Review the pending changes', hint: 'what to focus on' },
  { name: 'compact', description: 'Summarize the conversation' },
  { name: 'init', description: 'Write a CLAUDE.md' },
  { name: 'pr-comments', description: 'Show pull request comments' },
];

test('slashQuery: only a bare "/name" being typed opens the menu', () => {
  assert.equal(slashQuery('/'), '');
  assert.equal(slashQuery('/Rev'), 'rev');
  assert.equal(slashQuery('/review '), null); // completed: the args follow
  assert.equal(slashQuery('hi /review'), null);
  assert.equal(slashQuery(''), null);
});

test('matchCommands: prefix first, then substring in name or description, capped', () => {
  assert.deepEqual(matchCommands(cmds, '').map((c) => c.name), ['review', 'compact', 'init', 'pr-comments']);
  assert.deepEqual(matchCommands(cmds, 'c').map((c) => c.name), ['compact', 'review', 'init', 'pr-comments']);
  assert.deepEqual(matchCommands(cmds, 'comm').map((c) => c.name), ['pr-comments']);
  assert.deepEqual(matchCommands(cmds, 'zzz'), []);
  assert.equal(matchCommands(cmds, '', 2).length, 2);
});

test('commandHint: the input hint after a completed command, until args are typed', () => {
  assert.deepEqual(commandHint(cmds, '/review '), { name: 'review', hint: 'what to focus on' });
  assert.equal(commandHint(cmds, '/review tests'), null);
  assert.equal(commandHint(cmds, '/compact '), null); // takes no input
});
