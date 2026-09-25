// hack/agent-template-chat.test.mjs — the agent template's chat model
// (builtin-templates/agent/{chat-fold,tool-heads}.js): how a run's view
// becomes blocks, run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fold, activity, splitAttachments } from '../builtin-templates/agent/chat-fold.js';
import { headline, parseArgs, resultState, argsShown } from '../builtin-templates/agent/tool-heads.js';

const call = (id, name, args) => ({ id, type: 'function', function: { name, arguments: JSON.stringify(args) } });
const m = (id, role, content, extra = {}) => ({ id, seq: id, role, content, created: 100 + id, ...extra });

test('a headline is the model\'s summary, else a reading of the arguments', () => {
  assert.equal(headline('xbin_call', JSON.stringify({ method: 'get', path: '/api/apps/x', summary: 'Look up invoices' })), 'Look up invoices');
  assert.equal(headline('xbin_call', JSON.stringify({ method: 'get', path: '/api/apps/x' })), 'GET /api/apps/x');
  assert.equal(headline('web_search', '{"query":"pangolins"}'), 'Search the web: pangolins');
  assert.equal(headline('js_eval', JSON.stringify({ code: 'a\nb\nc' })), 'Run JavaScript (3 lines)');
  assert.equal(headline('mcp:apps/crm:get_thread', '{}'), 'get_thread · apps/crm');
  assert.equal(headline('subagent_wait', '{"ids":[4,5],"mode":"any"}'), 'Wait for #4, #5 (first)');
  // state_changed's own `summary` is its payload, not a headline override.
  assert.equal(headline('state_changed', '{"summary":"it moved"}'), 'Changed: it moved');
  assert.equal(headline('mystery', '{}'), 'mystery');
});

test('a streamed partial argument list still yields its finished fields', () => {
  const partial = '{"summary":"Fetch the price list","url":"https://ex';
  assert.deepEqual(parseArgs(partial), { summary: 'Fetch the price list' });
  assert.equal(headline('web_fetch', partial), 'Fetch the price list');
  assert.deepEqual(argsShown('{"summary":"x","path":"a.txt"}'), { path: 'a.txt' });
});

test('result states come from the engine\'s placeholders', () => {
  assert.equal(resultState('(running…)'), 'running');
  assert.equal(resultState('(awaiting your approval)'), 'approval');
  assert.equal(resultState('(waiting for subagent #3…)'), 'waiting');
  assert.equal(resultState('error: boom'), 'error');
  assert.equal(resultState('(interrupted by the owner)'), 'stopped');
  assert.equal(resultState('42'), 'done');
});

test('a call and its result are one block, in call order, with reasoning before the text', () => {
  const v = {
    run: { id: 1, status: 'idle' },
    messages: [
      m(1, 'user', 'hi'),
      m(2, 'assistant', 'looking', { reasoning: 'hmm', reasoningMs: 2100, toolCalls: [call('a', 'web_search', { query: 'q' }), call('b', 'note', { text: 'n' })] }),
      m(3, 'tool', 'results', { toolCallId: 'a', name: 'web_search' }),
      m(4, 'tool', '(running…)', { toolCallId: 'b', name: 'note' }),
    ],
  };
  const b = fold(v);
  assert.deepEqual(b.map((x) => x.k), ['user', 'think', 'assistant', 'tool', 'tool']);
  assert.equal(b[1].ms, 2100);
  assert.equal(b[3].state, 'done');
  assert.equal(b[3].result, 'results');
  assert.equal(b[4].state, 'running');
});

test('a subagent is a card carrying the child\'s own blocks — its task is on the card, not in it', () => {
  const parent = {
    run: { id: 1, status: 'awaiting' },
    messages: [m(1, 'user', 'split'), m(2, 'assistant', '', { toolCalls: [call('s', 'subagent_spawn', { task: 'dig', label: 'digger' })] }),
      m(3, 'tool', '(waiting for subagent #7…)', { toolCallId: 's' })],
    links: [{ id: 1, parentId: 1, childId: 7, toolCallId: 's', mode: 'fg', state: 'running', label: 'digger', phase: 'working' }],
  };
  const child = {
    run: { id: 7, status: 'running', parentId: 1 },
    messages: [m(1, 'system', 'sys'), m(2, 'user', 'dig'), m(3, 'assistant', '', { reasoning: 'r', toolCalls: [call('x', 'file_read', { path: 'a' })] }),
      m(4, 'tool', 'A', { toolCallId: 'x' })],
    draft: { text: 'partial', thinking: '', tools: {} },
  };
  const b = fold(parent, (id) => (id === 7 ? child : null));
  const card = b.find((x) => x.k === 'agent');
  assert.equal(card.childId, 7);
  assert.equal(card.state, 'running');
  assert.equal(card.task, 'dig');
  assert.deepEqual(card.blocks.map((x) => x.k), ['think', 'tool', 'draft'], 'no task message inside, the live draft is');
  // Not loaded yet: the card still shows, without blocks.
  assert.equal(fold(parent).find((x) => x.k === 'agent').blocks, null);
});

test('a background spawn finds its child through the spawn step', () => {
  const v = {
    run: { id: 1, status: 'idle' },
    messages: [m(1, 'user', 'go'), m(2, 'assistant', '', { toolCalls: [call('w', 'workflow_spawn', { task: 't' })] }),
      m(3, 'tool', 'started subagent #9 in the background', { toolCallId: 'w' })],
    steps: [{ id: 1, kind: 'spawn', detail: JSON.stringify({ runId: 9, toolCallId: 'w' }), created: 103 }],
    links: [{ id: 2, parentId: 1, childId: 9, toolCallId: '', mode: 'bg', state: 'done', result: 'ANSWER' }],
  };
  const card = fold(v).find((x) => x.k === 'agent');
  assert.equal(card.childId, 9);
  assert.equal(card.state, 'done');
  assert.equal(card.result, 'ANSWER');
});

test('engine notices are not the owner speaking', () => {
  const v = { run: { id: 1 }, messages: [m(1, 'user', 'hi'), m(2, 'user', '[subagent results — your workflow layer reporting, not the owner]\n--- #3 x (done) ---\nR')] };
  assert.deepEqual(fold(v).map((x) => x.k), ['user', 'notice']);
});

test('a creation note precedes the first message; later steps follow the message of their second', () => {
  const v = {
    run: { id: 1 },
    messages: [m(0, 'system', 'sys', { created: 100 }), m(1, 'user', 'go', { created: 100 }), m(2, 'assistant', 'done', { created: 105 })],
    steps: [{ id: 1, kind: 'note', detail: '{"text":"started by schedule #2"}', created: 100 },
      { id: 2, kind: 'finish', detail: '{"result":"ok"}', created: 105 }],
  };
  assert.deepEqual(fold(v).map((x) => x.k + (x.kind ? ':' + x.kind : '')), ['step:note', 'user', 'assistant', 'step:finish']);
});

test('attachments come off the text as files', () => {
  const s = splitAttachments('look\n\n[attached: chart.png (image/png, 2.0 KB), notes.txt (text, 12 B)]');
  assert.equal(s.text, 'look');
  assert.deepEqual(s.files.map((f) => f.path), ['chart.png', 'notes.txt']);
  assert.equal(splitAttachments('(see attached)\n\n[attached: a.png (image/png, 1 B)]').text, '');
});

test('the activity line says what the run is doing', () => {
  assert.equal(activity({ run: { status: 'running' }, draft: { thinking: 'x', text: '', tools: {} } }), 'Thinking…');
  assert.equal(activity({ run: { status: 'running' }, draft: { thinking: 'x', thinkEnd: 5, text: 'y', tools: {} } }), 'Writing…');
  const blocks = [{ k: 'tool', state: 'running', headline: 'Fetch it' }];
  assert.equal(activity({ run: { status: 'running' } }, blocks), 'Running: Fetch it');
  assert.equal(activity({ run: { status: 'awaiting', pendingState: { kind: 'await' } } }), 'Waiting for subagents…');
  assert.equal(activity({ run: { status: 'waiting_input', pendingState: { kind: 'approval' } } }), 'Waiting for your approval');
  assert.equal(activity({ run: { status: 'idle' } }), '');
});
