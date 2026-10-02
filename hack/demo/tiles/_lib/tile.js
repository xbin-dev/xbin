// tile.js — the few things every demo tile's node backend needs (copied into
// each tile's backend/ by hack/demo/seed.sh): serve HTTP on xbind's socket,
// read the verified caller, keep JSON documents in a filesystem resource,
// call xbind through the gateway, and answer MCP (streamable HTTP, the
// subset an agent or the chat tile uses: initialize, tools/list,
// tools/call). No dependencies.
'use strict';
const http = require('http');
const fs = require('fs');
const path = require('path');

// who: the verified caller (xbind strips inbound X-XBin-* and injects these).
function who(req) {
  const h = req.headers;
  return {
    from: h['x-xbin-from'] || '',
    role: h['x-xbin-role'] || '',
    user: h['x-xbin-user'] || '',
    level: h['x-xbin-user-level'] || '',
    viewedBy: h['x-xbin-viewed-by'] || '',
    partition: h['x-xbin-partition'] || '',
  };
}
// canWrite: the tile itself, its owner, or a person with write access.
const canWrite = (c) => c.from === 'owner' || (c.role === 'admin' && (!c.user || c.level === 'write' || c.level === 'terminal'));

function send(res, code, body, type) {
  const isText = typeof body === 'string';
  res.writeHead(code, { 'content-type': type || (isText ? 'text/plain; charset=utf-8' : 'application/json') });
  res.end(isText ? body : JSON.stringify(body));
}
const json = (res, body, code = 200) => send(res, code, body);
const fail = (res, code, msg) => send(res, code, { error: msg });

function readBody(req, limit = 8 << 20) {
  return new Promise((resolve, reject) => {
    let n = 0; const parts = [];
    req.on('data', (c) => { n += c.length; if (n > limit) { reject(new Error('body too large')); req.destroy(); } else parts.push(c); });
    req.on('end', () => {
      const s = Buffer.concat(parts).toString('utf8');
      if (!s) return resolve(null);
      try { resolve(JSON.parse(s)); } catch (e) { reject(new Error('bad JSON')); }
    });
    req.on('error', reject);
  });
}

// store(resource, file): one JSON document in a filesystem resource,
// written atomically. XBIN_RES_<NAME> is the resource's directory; without
// it (the manifest doesn't use the resource yet) the backend exits, rather
// than keep data anywhere else — xbind starts it again once it does.
function store(resource, file, empty) {
  const dir = process.env['XBIN_RES_' + resource.toUpperCase()];
  if (!dir) {
    console.error(`no ${resource} resource (XBIN_RES_${resource.toUpperCase()}): is it in scope.json and this tile's uses?`);
    process.exit(1);
  }
  const p = path.join(dir, file);
  return {
    load() {
      try { return JSON.parse(fs.readFileSync(p, 'utf8')); } catch { return structuredClone(empty); }
    },
    save(v) {
      fs.mkdirSync(dir, { recursive: true });
      const tmp = p + '.tmp-' + process.pid;
      fs.writeFileSync(tmp, JSON.stringify(v, null, 1));
      fs.renameSync(tmp, p);
    },
  };
}

// xbind(method, path, body): one call of xbind's API (or another tile's)
// through the gateway socket, as this tile.
function xbind(method, p, body) {
  return new Promise((resolve, reject) => {
    const req = http.request({
      socketPath: process.env.XBIN_GATEWAY, method, path: p,
      headers: { authorization: `Bearer ${process.env.XBIN_TOKEN}`, 'content-type': 'application/json' },
    }, (res) => {
      let text = '';
      res.setEncoding('utf8');
      res.on('data', (c) => { text += c; });
      res.on('end', () => {
        if (res.statusCode >= 300) return reject(new Error(`${method} ${p}: ${res.statusCode} ${text.slice(0, 200)}`));
        try { resolve(text ? JSON.parse(text) : null); } catch { resolve(text); }
      });
    });
    req.setTimeout(30000, () => req.destroy(new Error(`${method} ${p}: no answer`)));
    req.on('error', reject);
    req.end(body === undefined ? undefined : JSON.stringify(body));
  });
}

// mcp(server, tools): a POST /mcp handler. tools: [{name, description,
// inputSchema, call(args, caller) → any (JSON-able)}].
function mcp(server, tools) {
  return async (req, res) => {
    let msg;
    try { msg = await readBody(req); } catch (e) { return json(res, { jsonrpc: '2.0', id: null, error: { code: -32700, message: e.message } }); }
    if (!msg || msg.id === undefined) { res.writeHead(202); return res.end(); } // a notification
    const reply = (result) => json(res, { jsonrpc: '2.0', id: msg.id, result });
    const err = (code, message) => json(res, { jsonrpc: '2.0', id: msg.id, error: { code, message } });
    switch (msg.method) {
      case 'initialize':
        res.setHeader('mcp-session-id', 's-' + Date.now().toString(36));
        return reply({ protocolVersion: msg.params?.protocolVersion || '2025-06-18', capabilities: { tools: {} }, serverInfo: server });
      case 'ping':
        return reply({});
      case 'tools/list':
        return reply({ tools: tools.map(({ name, description, inputSchema }) => ({ name, description, inputSchema })) });
      case 'tools/call': {
        const t = tools.find((x) => x.name === msg.params?.name);
        if (!t) return err(-32602, `unknown tool ${msg.params?.name}`);
        try {
          const out = await t.call(msg.params?.arguments || {}, who(req));
          return reply({ content: [{ type: 'text', text: typeof out === 'string' ? out : JSON.stringify(out, null, 1) }] });
        } catch (e) {
          return reply({ content: [{ type: 'text', text: String(e.message || e) }], isError: true });
        }
      }
      default:
        return err(-32601, `method not found: ${msg.method}`);
    }
  };
}

// serve(routes): routes is [[method, /regex/ or 'path', handler(req, res, match, url)]].
function serve(routes) {
  const srv = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://tile');
    for (const [method, pat, fn] of routes) {
      if (method !== req.method && method !== '*') continue;
      const m = typeof pat === 'string' ? (url.pathname === pat ? [pat] : null) : url.pathname.match(pat);
      if (!m) continue;
      try { return await fn(req, res, m, url); } catch (e) {
        console.error(req.method, url.pathname, e);
        return fail(res, 500, String(e.message || e));
      }
    }
    fail(res, 404, `no route ${req.method} ${url.pathname}`);
  });
  srv.listen(process.env.XBIN_SOCKET);
  process.on('SIGTERM', () => srv.close(() => process.exit(0)));
  return srv;
}

// Relative times in fixtures: {"daysAgo": 3, "at": "14:20"}, {"minutesAgo":
// 40}, {"inDays": 9} … resolved against now when data is imported, so a
// fresh seed always looks current. A day that lands on a weekend moves to a
// working day (ahead to Monday, back to Friday): deals close and meetings
// happen on weekdays.
function when(spec, now = Date.now()) {
  if (spec == null) return null;
  if (typeof spec === 'number') return spec;
  if (typeof spec === 'string') return Date.parse(spec);
  const d = new Date(now);
  if (spec.minutesAgo != null) return now - spec.minutesAgo * 60e3;
  if (spec.hoursAgo != null) return now - spec.hoursAgo * 3600e3;
  const days = spec.inDays ?? -(spec.daysAgo ?? 0);
  d.setDate(d.getDate() + days);
  if (days) while (d.getDay() === 0 || d.getDay() === 6) d.setDate(d.getDate() + (days > 0 ? 1 : -1));
  if (spec.at) { const [h, m] = spec.at.split(':').map(Number); d.setHours(h, m || 0, 0, 0); }
  return d.getTime();
}

// zone(tz): read and write wall-clock times ("15:10", the 05:30 run) in the
// time zone the seed's fixtures mean — the seeding machine's, which a tile
// sandbox (UTC) doesn't share. Each tile keeps it with its data, so a
// restart keeps it too.
function zone(tz) {
  if (tz && process.env.TZ !== tz) process.env.TZ = tz;
}

module.exports = { who, canWrite, send, json, fail, readBody, store, xbind, mcp, serve, when, zone };
