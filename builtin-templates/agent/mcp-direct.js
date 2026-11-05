// mcp-direct.js — Settings → MCP's "Connected by URL" section on the web
// (API.md "Direct MCP servers"): the servers people connected by address —
// a person's own (only you) and the global ones (everyone) — each with its
// state and tools, Sign in (the server's authorization page, opened as a real
// link: the tile's frame may not open windows after a request), Sign out,
// Refresh tools and Remove; and the form that adds one: its address, a name,
// for whom (everyone only for a manager), and a static token for a server
// that takes one instead of a sign-in. A sign-in finishes in another tab
// (mcp-callback.html): the list is read again while one is waiting, and when
// the page comes back into view.
//
// What it says and where each call goes is model/mcp-direct.js.
import { esc } from '/vendor/bx-kit.js';
import { listDirect, addDirect, removeDirect, signinDirect, signoutDirect, refreshDirect, stateOf, scopeWords, SIGNIN_WATCH_MS } from './model/mcp-direct.js';

const links = new Map(); // server id → its authorization page, once Sign in asked for it
let err = '';
let watchUntil = 0;
let watchT = null;
let current = null; // the mounted section

/** mountDirectMcp(bd): the section, appended to the MCP tab's body. */
export async function mountDirectMcp(bd) {
  const sec = document.createElement('div');
  sec.className = 'sec';
  sec.id = 'mcpd';
  bd.append(sec);
  current = sec;
  await paint(sec);
}

addEventListener('focus', () => { if (current && current.isConnected) paint(current); });

async function paint(sec) {
  let list;
  try { list = await listDirect(); } catch (e) { list = { servers: [], errors: [e.message] }; }
  if (!sec.isConnected) return;
  draw(sec, list);
  const waiting = list.servers.some((s) => links.has(s.id) && s.state === 'signin');
  for (const s of list.servers) if (s.state !== 'signin') links.delete(s.id);
  clearTimeout(watchT);
  if (waiting && Date.now() < watchUntil) watchT = setTimeout(() => paint(sec), 3000);
}

const host = (u) => { try { return new URL(u).host; } catch { return u; } };

function rowHtml(s) {
  const st = stateOf(s);
  const link = links.get(s.id);
  const oauth = s.auth === 'oauth';
  return `<div class="mcpd-row" data-id="${esc(s.id)}" data-state="${esc(s.state)}">
    <div class="mcpd-hd"><b class="mono">${esc(s.name)}</b>
      <span class="muted small">${esc(scopeWords(s))}${s.title ? ` · ${esc(s.title)}` : ''}</span>
      <span style="flex:1"></span>
      ${s.canEdit && oauth ? `<button class="btn btnsm${s.state === 'signin' ? '' : ' ghost'}" data-act="signin">${s.signedIn ? 'Sign in again' : 'Sign in'}</button>` : ''}
      ${s.canEdit && oauth && s.signedIn ? '<button class="btn ghost btnsm" data-act="signout">Sign out</button>' : ''}
      ${s.state !== 'signin' ? '<button class="btn ghost btnsm" data-act="refresh" title="list its tools again">Refresh</button>' : ''}
      ${s.canEdit ? '<button class="btn rm btnsm" data-act="remove">Remove</button>' : ''}</div>
    <div class="mono muted small mcpd-url">${esc(s.url)}</div>
    <div class="small ${st.tone === 'warn' ? 'warnt' : ''}" data-role="state">${esc(st.text)}${(s.tools || []).length ? ` <span class="muted">— ${esc(s.tools.join(', '))}</span>` : ''}</div>
    ${link && s.state === 'signin' ? `<div class="mcpd-link"><a class="btn btnsm" id="mcpd-open-${esc(s.id)}" href="${esc(link)}" target="_blank" rel="noopener noreferrer">Open ${esc(host(link))} to sign in ↗</a>
      <span class="muted small">then come back here — it finishes in that tab</span></div>` : ''}
  </div>`;
}

function draw(sec, list) {
  const canAdd = list.personal || list.canGlobal;
  const scopes = [list.personal && ['personal', 'only you'], list.canGlobal && ['global', 'everyone']].filter(Boolean);
  sec.innerHTML = `<h4>Connected by URL</h4>
    <div class="hint">Streamable-HTTP MCP servers added by their address — yours alone, or (a manager's) everyone's. One that wants a sign-in
      (OAuth) is signed in to here; its tokens stay in the agent's vault. Their tools are offered as
      <span class="mono">mcp:&lt;name&gt;:&lt;tool&gt;</span> to classes with internal reach — and they reach outside the workspace: what a conversation sends a tool goes to that server.</div>
    ${[...(list.errors || []), ...(err && !canAdd ? [err] : [])].map((e) => `<div class="err">${esc(e)}</div>`).join('')}
    <div id="mcpd-list">${list.servers.length ? list.servers.map(rowHtml).join('') : '<div class="muted small" id="mcpd-none">None yet.</div>'}</div>
    ${canAdd ? `<form id="mcpd-form" class="mcpd-form" autocomplete="off">
      <div class="field"><label>Address</label><input id="mcpd-url" type="url" required placeholder="https://api.example.com/mcp"></div>
      <div class="mcpd-row2">
        <div class="field"><label>Name (optional)</label><input id="mcpd-name" placeholder="from its address" pattern="[a-z][a-z0-9-]{0,31}"></div>
        <div class="field"><label>For</label><select id="mcpd-scope">${scopes.map(([v, l]) => `<option value="${v}">${l}</option>`).join('')}</select></div>
      </div>
      <details><summary class="small">It takes a static token instead of a sign-in</summary>
        <div class="field"><label>Token (sent as Authorization: Bearer)</label><input id="mcpd-token" type="password" autocomplete="off"></div></details>
      <div class="pbtns"><button class="btn btnsm" id="mcpd-add" type="submit">Add server</button><span class="err" id="mcpd-err">${esc(err)}</span></div>
    </form>` : ''}`;
  for (const row of sec.querySelectorAll('.mcpd-row')) {
    const s = list.servers.find((x) => x.id === row.dataset.id);
    for (const b of row.querySelectorAll('button[data-act]')) b.onclick = () => act(sec, s, b.dataset.act, b);
  }
  const form = sec.querySelector('#mcpd-form');
  if (form) form.onsubmit = (e) => { e.preventDefault(); add(sec); };
}

async function startSignin(sec, s) {
  const r = await signinDirect(s);
  links.set(s.id, r.url);
  watchUntil = Date.now() + SIGNIN_WATCH_MS;
}

async function act(sec, s, what, b) {
  err = '';
  b.disabled = true;
  try {
    if (what === 'signin') await startSignin(sec, s);
    else if (what === 'signout') await signoutDirect(s);
    else if (what === 'refresh') await refreshDirect(s);
    else if (what === 'remove') {
      if (!confirm(`Remove the MCP server ${s.name}${s.scope === 'global' ? ' for everyone' : ''}? Its sign-in is forgotten.`)) { b.disabled = false; return; }
      await removeDirect(s);
      links.delete(s.id);
    }
  } catch (e) { err = e.message; }
  await paint(sec);
}

async function add(sec) {
  const v = (id) => sec.querySelector(id)?.value.trim() || '';
  const body = { url: v('#mcpd-url'), name: v('#mcpd-name'), scope: v('#mcpd-scope') || 'personal' };
  const token = v('#mcpd-token');
  if (token) body.token = token;
  const btn = sec.querySelector('#mcpd-add');
  btn.disabled = true;
  btn.textContent = 'Connecting…';
  err = '';
  try {
    const s = await addDirect(body);
    if (s.state === 'signin' && s.canEdit) await startSignin(sec, s); // straight to its sign-in link
  } catch (e) { err = e.message; }
  await paint(sec);
}

const style = document.createElement('style');
style.textContent = `
  #mcpd .mcpd-row { border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 8px; margin: 8px 0; background: var(--bx-panel); }
  #mcpd .mcpd-hd { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
  #mcpd .mcpd-url { overflow-wrap: anywhere; margin: 2px 0; }
  #mcpd .mcpd-link { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-top: 6px; }
  #mcpd .mcpd-row2 { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 0 12px; }
  #mcpd .mcpd-form { margin-top: 10px; }
  #mcpd .warnt { color: var(--bx-amber); }
  #mcpd .pbtns { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin: 6px 0; }
`;
document.head.append(style);
