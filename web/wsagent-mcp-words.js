/**
 * wsagent-mcp-words.js — the words and shapes of the outside MCP servers'
 * list on the workspace agent's card (<bx-wsagent-mcp-servers>, D240):
 * pure, so node tests them (hack/wsagent-mcp-servers.test.mjs) without a
 * browser.
 */

/** FLAGS: the per-server switches that widen, with what each means. */
export const FLAGS = Object.freeze([
  { id: 'privateNetwork', label: 'On a private network', warn: 'xbin will reach this server at private, loopback or this machine’s own addresses. Only for a server you run on your own network: the list then points into it. Link-local and cloud metadata addresses stay refused.' },
  { id: 'trustReadOnly', label: 'Trust its read-only marks', warn: 'A tool this server marks read-only then runs without asking anyone who turned on “MCP tools marked read-only don’t ask me”. The mark is the server’s own word: trust it only as far as you trust whoever runs it.' },
  { id: 'plainHttp', label: 'Plain http, I know', warn: 'The secret and every argument cross the network unencrypted — readable, and changeable, on the way. Use https unless the server is on a network you control.' },
]);

/** AUTHS: how xbin signs in to a server. */
export const AUTHS = Object.freeze([
  { id: 'none', label: 'No sign-in' }, { id: 'bearer', label: 'Bearer token (Authorization: Bearer …)' }, { id: 'header', label: 'A header of its own' },
]);

export const slug = (s) => String(s || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 32) || 'server';
const orgList = (s) => [...new Set(String(s || '').split(/[\s,]+/).map((x) => x.trim()).filter(Boolean))];

/** draftOf(view): a server as the editor holds it. */
export function draftOf(v = {}) {
  return { id: String(v.id || ''), name: String(v.name || ''), url: String(v.url || ''), auth: v.auth || 'none', header: String(v.header || ''), note: String(v.note || ''),
    orgs: (v.orgs || []).join(', '), plainHttp: !!v.plainHttp, privateNetwork: !!v.privateNetwork, trustReadOnly: !!v.trustReadOnly,
    hasSecret: !!v.hasSecret, secret: '', dropSecret: false, saved: !!v.id, byHost: !!v.byHost };
}

/** serverBody(draft): one server as the PUT sends it — its secret only when typed, "" when removed. */
export function serverBody(d) {
  const out = { id: d.id, name: d.name.trim(), url: d.url.trim(), auth: d.auth, note: d.note.trim(), orgs: orgList(d.orgs),
    plainHttp: !!d.plainHttp, privateNetwork: !!d.privateNetwork, trustReadOnly: !!d.trustReadOnly };
  if (d.auth === 'header') out.header = d.header.trim();
  if (d.auth !== 'none' && d.secret) out.secret = d.secret;
  else if (d.auth !== 'none' && d.dropSecret) out.secret = '';
  return out;
}

/** serverWords(draft): the collapsed row — {host, sign, flags}. */
export function serverWords(d) {
  let host = '';
  try { host = new URL(d.url).host; } catch { host = d.url; }
  const sign = d.auth === 'none' ? 'no sign-in' : `${d.auth === 'bearer' ? 'bearer token' : `header ${d.header || '?'}`}, ${d.secret ? 'a new secret' : d.hasSecret && !d.dropSecret ? 'secret set' : 'no secret yet'}`;
  const flags = FLAGS.filter((f) => d[f.id]).map((f) => f.label);
  if (orgList(d.orgs).length) flags.unshift(`only for ${orgList(d.orgs).join(', ')}`);
  return { host, sign, flags };
}

/** testWords(answer): what Test shows — {ok, line, tools: [{name, readOnly, description}]}. */
export function testWords(a) {
  if (!a) return null;
  if (!a.ok) return { ok: false, line: `${a.refusal ? `${a.refusal}: ` : ''}${a.error || 'it didn’t answer'}`, tools: [] };
  const tools = (a.tools || []).map((t) => ({ name: String(t.name || ''), readOnly: !!t.readOnly, description: String(t.description || '') }));
  const who = a.serverInfo && a.serverInfo.name ? `${a.serverInfo.name}${a.serverInfo.version ? ` ${a.serverInfo.version}` : ''}, ` : '';
  return { ok: true, line: `It answered (${who}MCP ${a.protocolVersion || '?'}): ${tools.length} tool${tools.length === 1 ? '' : 's'}.`, tools };
}
