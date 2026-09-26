// The DOM-free modules builtin tile pages share with their native views
// (plans/native.md §18): egress-approver's fmt.js, prometheus-viewer's
// prom.js and chat's chat-core.js — the engine that used to be the chat
// page's inline script. The pages' own rendering is checked in a browser by
// the UI harness's tilePages pass; these pin the logic both views rely on.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fmtBytes, ago, norm } from '../builtin-tiles/egress-approver/fmt.js';
import { parseProm, rateOf, fmtN, fmtRate, labelStr, scrape, histKey, asEndpoints, epLabel, HIST } from '../builtin-tiles/prometheus-viewer/prom.js';
import { Chat, splitThink, SEP } from '../builtin-tiles/chat/chat-core.js';

test('egress fmt: bytes, ago, norm', () => {
  assert.equal(fmtBytes(0), '0');
  assert.equal(fmtBytes(512), '512B');
  assert.equal(fmtBytes(1536), '1.5K');
  assert.equal(fmtBytes(1048576), '1.0M');
  const now = Date.now();
  assert.equal(ago(0), '—');
  assert.equal(ago(now - 38_500), '38s ago');
  assert.equal(ago(now - 125_000), '2m ago');
  assert.equal(ago(now - 3 * 3600_000), '3h ago');
  assert.equal(ago(now - 2 * 86400_000), '2d ago');
  assert.deepEqual(norm({ clients: 1 }), { clients: 1, pending: [], approved: [], denied: [] });
});

test('prom: parse, labels, rate, formatting', () => {
  const m = parseProm(`# HELP req_total Requests.\n# TYPE req_total counter\nreq_total{b="x",a="q\\"uote"} 12 1700000000\nreq_total{b="y"} +Inf\n# TYPE g gauge\ng 0.000123\nbare_untyped 7\n# junk line\n`);
  assert.deepEqual([...m.keys()], ['req_total', 'g', 'bare_untyped']);
  const r = m.get('req_total');
  assert.equal(r.type, 'counter');
  assert.equal(r.help, 'Requests.');
  assert.deepEqual(r.samples[0], { labels: { b: 'x', a: 'q"uote' }, key: 'req_total{a=q"uote,b=x}', value: 12 });
  assert.equal(r.samples[1].value, Infinity);
  assert.equal(labelStr(r.samples[0].labels), '{b="x", a="q"uote"}');
  assert.equal(labelStr({}), '');
  assert.equal(m.get('bare_untyped').type, 'untyped');
  assert.equal(rateOf([{ t: 0, v: 1 }]), null);
  assert.equal(rateOf([{ t: 0, v: 10 }, { t: 2000, v: 14 }]), 2);
  assert.equal(rateOf([{ t: 0, v: 10 }, { t: 2000, v: 4 }]), 0, 'a counter reset is 0, not negative');
  assert.equal(fmtN(123123123), '123 123 123');
  assert.equal(fmtN(-0.000123456), '-0.000123');
  assert.equal(fmtN(12.345), '12.35');
  assert.equal(fmtN(NaN), 'NaN');
  assert.equal(fmtRate(0), '0');
  assert.equal(fmtRate(0.0166), '0.017');
  assert.equal(fmtRate(2197.8), '2 198');
  assert.deepEqual(asEndpoints({ url: '/api/apps/x', service: 'prometheus' }), [{ url: '/api/apps/x', provider: 'prometheus' }]);
  assert.equal(epLabel({ provider: 'apps/a', instance: 'b' }), 'apps/a#b');
});

test('prom: scrape keeps a bounded history per source and reports failures', async () => {
  const hist = new Map();
  const eps = [{ url: '/api/apps/gw/', provider: 'apps/gw' }, { url: '/api/apps/down', provider: 'apps/down' }];
  let n = 0;
  const urls = [];
  const fetch = async (u) => {
    urls.push(u);
    if (u.includes('down')) return new Response('no', { status: 502 });
    return new Response(`c_total ${++n}\n`);
  };
  for (let i = 0; i < HIST + 5; i++) {
    const res = await scrape(eps, hist, fetch);
    assert.equal(res[1].error, 'HTTP 502');
    assert.equal(res[1].metrics, null);
    assert.equal(res[0].error, '');
  }
  assert.deepEqual(urls.slice(0, 2), ['/api/apps/gw/metrics', '/api/apps/down/metrics'], 'one /metrics under each url, trailing slash trimmed');
  const h = hist.get(histKey(0, 'c_total{}'));
  assert.equal(h.length, HIST);
  assert.equal(h.at(-1).v, HIST + 5);
});

// a scripted OpenAI-compatible + MCP backend for Chat
function backend(script) {
  const calls = [];
  const fetch = async (url, opts = {}) => {
    const body = opts.body ? JSON.parse(opts.body) : null;
    calls.push({ url, body, headers: opts.headers || {} });
    const h = script[url];
    if (!h) return new Response(JSON.stringify({ error: 'no' }), { status: 404 });
    const r = typeof h === 'function' ? h(body, calls, opts) : h;
    return r instanceof Response ? r : await r;
  };
  return { fetch, calls };
}
const sse = (...chunks) => new Response(chunks.map((c) => `data: ${typeof c === 'string' ? c : JSON.stringify(c)}\n\n`).join(''), { headers: { 'content-type': 'text/event-stream' } });
const delta = (d, finish = null) => ({ choices: [{ index: 0, delta: d, finish_reason: finish }] });
const json = (v, init) => new Response(JSON.stringify(v), { headers: { 'content-type': 'application/json' }, ...init });

test('chat-core: splitThink', () => {
  assert.deepEqual(splitThink('<think>hmm</think>\n\nHi', ''), { thinking: 'hmm', content: 'Hi' });
  assert.deepEqual(splitThink('  <think>still', 'api '), { thinking: 'api still', content: '' });
  assert.deepEqual(splitThink('<thi', ''), { thinking: '', content: '' }, 'a partial tag is held back');
  assert.deepEqual(splitThink('plain', 'r'), { thinking: 'r', content: 'plain' });
});

test('chat-core: models across endpoints (aliases first, failures noted)', async () => {
  const { fetch } = backend({
    '/api/a/v1/models': json({ data: [{ id: 'z-model' }, { id: 'a-model' }, { id: 'fast', alias_of: 'z-model' }] }),
    '/api/b/v1/models': new Response('down', { status: 503 }),
  });
  const chat = new Chat({ llm: { endpoints: [{ url: '/api/a', provider: 'apps/a' }, { url: '/api/b', provider: 'apps/b', instance: 'eu' }] }, fetch });
  const seen = [];
  for (const t of ['note', 'models', 'change']) chat.addEventListener(t, () => seen.push(t));
  await chat.loadModels();
  assert.deepEqual(chat.models, [
    { value: `0${SEP}fast`, label: 'fast → z-model — apps/a' },
    { value: `0${SEP}a-model`, label: 'a-model — apps/a' },
    { value: `0${SEP}z-model`, label: 'z-model — apps/a' },
  ]);
  assert.equal(chat.model, `0${SEP}fast`);
  assert.equal(chat.note, 'Some providers failed: apps/b#eu');
  assert.equal(chat.noteError, false);
  assert.deepEqual(seen, ['models', 'change', 'note', 'change']);

  const none = new Chat({});
  await none.loadModels();
  assert.match(none.note, /^No LLM bound yet/);
  assert.equal(none.noteError, true);
});

test('chat-core: a turn with an MCP tool call, then the answer', async () => {
  const completions = [
    sse(delta({ role: 'assistant', tool_calls: [{ index: 0, id: 'c1', type: 'function', function: { name: 'm0_search', arguments: '{"q":' } }] }),
      delta({ tool_calls: [{ index: 0, function: { arguments: '"bug"}' } }] }),
      delta({ tool_calls: [{ index: 1, id: 'c2', type: 'function', function: { name: 'nope', arguments: '' } }] }), delta({}, 'tool_calls'), '[DONE]'),
    sse(delta({ reasoning_content: 'Seven.' }), delta({ content: '<b>7</b> bugs' }), delta({}, 'stop'), '[DONE]'),
  ];
  const mcp = [
    json({ jsonrpc: '2.0', id: 1, result: { protocolVersion: '2025-03-26' } }, { headers: { 'content-type': 'application/json', 'Mcp-Session-Id': 's1' } }),
    new Response(null, { status: 202 }),
    // an SSE answer: a notification first, then the matching result
    new Response(`data: {"jsonrpc":"2.0","method":"notifications/progress"}\n\ndata: ${JSON.stringify({ jsonrpc: '2.0', id: 2, result: { tools: [{ name: 'search', description: 'Search.' }] } })}\n\n`, { headers: { 'content-type': 'text/event-stream' } }),
    json({ jsonrpc: '2.0', id: 3, result: { content: [{ type: 'text', text: '7 found' }, { type: 'image' }] } }),
  ];
  const { fetch, calls } = backend({
    '/api/llm/v1/models': json({ data: [{ id: 'm' }] }),
    '/api/llm/v1/chat/completions': () => completions.shift(),
    '/api/gh/mcp': () => mcp.shift(),
  });
  const chat = new Chat({ llm: { url: '/api/llm', service: 'openai' }, mcp: { endpoints: [{ url: '/api/gh/', provider: 'apps/gh' }] }, fetch });
  const events = [];
  for (const t of ['user', 'busy', 'round', 'round-end', 'tool', 'tool-result', 'fail']) chat.addEventListener(t, (e) => events.push(t + (t === 'busy' ? `:${e.detail.busy}` : '')));
  await Promise.all([chat.loadModels(), chat.loadTools()]);
  assert.deepEqual(chat.tools.map((t) => t.function.name), ['m0_search']);
  assert.equal(chat.send('  how many bugs?  '), true);
  assert.equal(chat.send('again'), false, 'one turn at a time');
  assert.equal(chat.busy, true);
  await chat.running;
  assert.equal(chat.busy, false);
  assert.deepEqual(events, ['user', 'busy:true', 'round', 'round-end', 'tool', 'tool-result', 'tool', 'tool-result', 'round', 'round-end', 'busy:false']);
  const h = chat.history;
  assert.deepEqual(h.map((t) => t.role), ['user', 'assistant', 'tool', 'tool', 'assistant']);
  assert.equal(h[0].text, 'how many bugs?');
  assert.deepEqual([h[2].label, h[2].args, h[2].state, h[2].result], ['apps/gh · search', '{"q":"bug"}', 'ok', '7 found\n[image]']);
  assert.deepEqual([h[3].label, h[3].args, h[3].state, h[3].result], ['nope', '{}', 'error', 'unknown tool: nope']);
  assert.deepEqual([h[4].think, h[4].text, h[4].streaming, h[4].thinking], ['Seven.', '<b>7</b> bugs', false, false]);
  const mcpCalls = calls.filter((c) => c.url === '/api/gh/mcp');
  assert.deepEqual(mcpCalls.map((c) => c.body.method), ['initialize', 'notifications/initialized', 'tools/list', 'tools/call']);
  assert.equal(mcpCalls[3].headers['Mcp-Session-Id'], 's1');
  assert.equal(mcpCalls[3].headers['MCP-Protocol-Version'], '2025-03-26');
  assert.deepEqual(mcpCalls[3].body.params, { name: 'search', arguments: { q: 'bug' } });
  const second = calls.filter((c) => c.url.endsWith('/chat/completions'))[1].body;
  assert.equal(second.model, 'm');
  assert.deepEqual(second.messages.map((m) => m.role), ['user', 'assistant', 'tool', 'tool']);
  assert.equal(second.tool_choice, 'auto');
  assert.deepEqual(chat.messages.at(-1), { role: 'assistant', content: '<b>7</b> bugs' });
});

test('chat-core: truncation, errors, stop and a new chat', async () => {
  const answers = [
    () => sse(delta({ content: '<think>a</think>b' }), delta({}, 'length')),
    () => new Response('upstream exploded', { status: 500 }),
  ];
  const { fetch } = backend({ '/api/llm/v1/chat/completions': () => answers.shift()() });
  const chat = new Chat({ llm: { url: '/api/llm' }, fetch });
  chat.send('one', `0${SEP}m`); await chat.running;
  assert.deepEqual([chat.history[1].think, chat.history[1].text, chat.history[1].error, chat.history[1].errorKind], ['a', 'b', '— response truncated (token limit) —', 'truncated']);
  chat.send('two', `0${SEP}m`); await chat.running;
  assert.deepEqual([chat.history.at(-1).error, chat.history.at(-1).errorKind], ['Error: 500: upstream exploded', 'error']);
  assert.equal(chat.history.at(-2).streaming, false, 'the failed round stops streaming');

  // stop: the in-flight request's signal aborts
  const stopped = backend({ '/api/llm/v1/chat/completions': (_b, _c, opts) => new Promise((_res, rej) => opts.signal.addEventListener('abort', () => rej(new DOMException('aborted', 'AbortError')))) });
  const c2 = new Chat({ llm: { url: '/api/llm' }, fetch: stopped.fetch });
  c2.send('slow', `0${SEP}m`);
  c2.abort(); await c2.running;
  assert.deepEqual([c2.history.at(-1).error, c2.history.at(-1).errorKind, c2.busy], ['— stopped —', 'stopped', false]);
  // a new chat mid-turn: history and messages go; the stopped line lands in the new chat (as the page always did)
  const c3 = new Chat({ llm: { url: '/api/llm' }, fetch: stopped.fetch });
  let resets = 0;
  c3.addEventListener('reset', () => resets++);
  c3.send('slow', `0${SEP}m`);
  c3.reset(); await c3.running;
  assert.equal(resets, 1);
  assert.deepEqual(c3.history.map((t) => [t.role, t.error]), [['assistant', '— stopped —']]);
  assert.deepEqual(c3.messages, []);
});
