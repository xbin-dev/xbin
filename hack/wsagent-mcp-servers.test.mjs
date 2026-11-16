// The outside MCP servers' list on the workspace agent's card (web/wsagent-mcp-words.js, D240):
// what Save sends of a server (its secret only when typed, "" when removed), and what a row and a Test say.
import { test } from 'node:test';
import assert from 'node:assert/strict';

const M = await import('../web/wsagent-mcp-words.js');

test('a secret is sent only when typed, "" when removed, never read back', () => {
  const read = { id: 'gh', server: 'ext:gh', name: 'GitHub', url: 'https://mcp.example.com/mcp', host: 'mcp.example.com', auth: 'bearer', hasSecret: true, orgs: ['eng'], privateNetwork: false };
  const d = M.draftOf(read);
  assert.equal(d.secret, '');
  assert.equal('secret' in M.serverBody(d), false);
  assert.equal(M.serverBody({ ...d, secret: 'ghp-new' }).secret, 'ghp-new');
  assert.equal(M.serverBody({ ...d, dropSecret: true }).secret, '');
  assert.equal('secret' in M.serverBody({ ...d, auth: 'none', secret: 'x' }), false);
  assert.deepEqual(M.serverBody({ ...d, orgs: 'eng, ops,eng' }).orgs, ['eng', 'ops']);
  assert.equal(M.serverBody({ ...d, auth: 'header', header: ' X-Api-Key ' }).header, 'X-Api-Key');
  const w = M.serverWords({ ...d, privateNetwork: true });
  assert.equal(w.host, 'mcp.example.com');
  assert.equal(w.sign, 'bearer token, secret set');
  assert.deepEqual(w.flags, ['only for eng', 'On a private network']);
  assert.equal(M.FLAGS.length, 3);
  for (const f of M.FLAGS) assert.ok(f.warn.length > 60, f.id);
});

test('Test says the tools, or the exact error', () => {
  assert.deepEqual(M.testWords({ ok: false, refusal: 'mcp-auth', error: 'the server refused xbin’s credentials (401)' }),
    { ok: false, line: 'mcp-auth: the server refused xbin’s credentials (401)', tools: [] });
  const ok = M.testWords({ ok: true, protocolVersion: '2025-06-18', serverInfo: { name: 'tracker', version: '2' }, tools: [{ name: 'lookup', readOnly: true }] });
  assert.equal(ok.line, 'It answered (tracker 2, MCP 2025-06-18): 1 tool.');
  assert.deepEqual(ok.tools, [{ name: 'lookup', readOnly: true, description: '' }]);
});
