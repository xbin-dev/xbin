// The journey passes' MCP server: one tool, roll_dice {sides?, count?}.
// Streamable HTTP, JSON answers only (no SSE): initialize, ping, tools/list,
// tools/call. xbind hands the backend its socket in XBIN_SOCKET.
const http = require('http');

const TOOL = {
  name: 'roll_dice',
  description: 'Roll dice: count dice (default 1) with the given number of sides (default 6).',
  inputSchema: {
    type: 'object',
    properties: { sides: { type: 'integer', minimum: 2, maximum: 1000 }, count: { type: 'integer', minimum: 1, maximum: 20 } },
  },
};

function roll(args) {
  const sides = Math.min(1000, Math.max(2, Number(args.sides) || 6));
  const count = Math.min(20, Math.max(1, Number(args.count) || 1));
  const rolls = Array.from({ length: count }, () => 1 + Math.floor(Math.random() * sides));
  return `rolled ${count}d${sides}: ${rolls.join(', ')} (total ${rolls.reduce((a, b) => a + b, 0)})`;
}

function answer(msg) {
  switch (msg.method) {
    case 'initialize':
      return { protocolVersion: (msg.params && msg.params.protocolVersion) || '2025-06-18', capabilities: { tools: {} }, serverInfo: { name: 'dice', version: '1.0.0' } };
    case 'ping':
      return {};
    case 'tools/list':
      return { tools: [TOOL] };
    case 'tools/call':
      if (!msg.params || msg.params.name !== TOOL.name) throw Object.assign(new Error('unknown tool'), { code: -32602 });
      return { content: [{ type: 'text', text: roll(msg.params.arguments || {}) }] };
    default:
      throw Object.assign(new Error(`method not found: ${msg.method}`), { code: -32601 });
  }
}

const send = (res, status, body) => { res.writeHead(status, { 'content-type': 'application/json' }); res.end(JSON.stringify(body)); };

const srv = http.createServer((req, res) => {
  const path = new URL(req.url, 'http://tile').pathname;
  if (req.method === 'GET' && path === '/roll') return send(res, 200, { text: roll({}) });
  if (req.method !== 'POST' || path !== '/mcp') return send(res, 404, { error: `no route ${req.method} ${path}` });
  let raw = '';
  req.on('data', (c) => { raw += c; });
  req.on('end', () => {
    let msg;
    try { msg = JSON.parse(raw); } catch { return send(res, 200, { jsonrpc: '2.0', id: null, error: { code: -32700, message: 'bad JSON' } }); }
    if (!msg || msg.id === undefined) { res.writeHead(202); return res.end(); } // a notification
    if (msg.method === 'initialize') res.setHeader('mcp-session-id', 's-' + Date.now().toString(36));
    try { send(res, 200, { jsonrpc: '2.0', id: msg.id, result: answer(msg) }); } catch (e) { send(res, 200, { jsonrpc: '2.0', id: msg.id, error: { code: e.code || -32603, message: e.message } }); }
  });
});
srv.listen(process.env.XBIN_SOCKET);
process.on('SIGTERM', () => srv.close(() => process.exit(0)));
