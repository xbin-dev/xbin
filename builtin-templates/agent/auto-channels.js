// auto-channels.js — chat channels on the Automations page (D86). A channel
// appears when an adapter tile bound to this agent (the slack tile, …) says
// hello. A manager claims it; its owner decides who may talk (pairing codes,
// allowlists), which lane its conversations run in, and sees its sessions and
// the replies that could not be delivered. The adapter side is
// /docs/agent-inbox.md.
import { html, nothing } from '/vendor/lit-all.min.js';
import { selfApi as api, jbody } from '/vendor/bx-kit.js';
import { registerKind, ago } from './automations.js';

// What the open channel's detail shows (open() loads it).
const st = { id: 0, peers: [], sessions: [], failed: [], draft: null, code: '', add: '', note: '' };

async function open(id, page) {
  if (st.id !== id) Object.assign(st, { id, peers: [], sessions: [], failed: [], draft: null, code: '', add: '', note: '' });
  const it = page.items.find((i) => i.kind === 'channel' && i.id === id);
  if (!it) return;
  const owner = it.access === 'owner';
  const get = (path, key) => api(`/channels/${id}/${path}`).then((r) => r[key] || []).catch(() => []);
  [st.sessions, st.peers, st.failed] = await Promise.all([
    it.access === 'claim' ? [] : get('sessions', 'sessions'),
    owner ? get('peers', 'peers') : [],
    owner ? get('outbox?state=failed', 'items') : [],
  ]);
  if (!st.draft) st.draft = draftOf(it);
}

// The rules form edits a flat draft of the policy (docs/agent-inbox.md).
function draftOf(it) {
  const pol = (it.config || {}).policy || {};
  const dm = pol.dm || {}, g = pol.groups || {};
  return {
    name: it.name, visibility: it.visibility || 'private',
    dmPolicy: dm.policy || 'pairing', dmScope: dm.scope || '',
    groupPolicy: g.policy || 'allowlist', allow: (g.allow || []).join(', '),
    requireMention: g.requireMention !== false, followThreads: g.followThreads !== false, groupThreads: g.threads || '',
    privateLane: !!pol.privateLane, trustedGroups: (pol.trustedGroups || []).join(', '),
    reset: pol.reset || '', system: pol.system || '', ratePerMin: pol.ratePerMin || '',
    deny: pol.deny ? pol.deny.join(', ') : null, // null: the default list
  };
}

function policyOf(d) {
  const list = (s) => s.split(/[\s,]+/).filter(Boolean);
  const p = {
    dm: { policy: d.dmPolicy },
    groups: { policy: d.groupPolicy, allow: list(d.allow), requireMention: d.requireMention, followThreads: d.followThreads },
  };
  if (d.dmScope) p.dm.scope = d.dmScope;
  if (d.groupThreads) p.groups.threads = d.groupThreads;
  if (d.privateLane) { p.privateLane = true; p.trustedGroups = list(d.trustedGroups); }
  if (d.reset) p.reset = d.reset;
  if (d.system.trim()) p.system = d.system.trim();
  if (+d.ratePerMin > 0) p.ratePerMin = +d.ratePerMin;
  if (d.deny != null) p.deny = list(d.deny);
  return p;
}

// --- actions ---------------------------------------------------------------

// act runs a change and reloads; the note is what it says afterwards (fn may
// return its own).
async function act(page, fn, note = '') {
  page.err = '';
  try {
    const said = await fn();
    st.note = typeof said === 'string' ? said : note;
    await page.load();
  } catch (e) { page.err = e.message; page.changed(); }
}

const path = (it, rest = '') => `/channels/${it.id}${rest}`;
const body = () => ({ name: st.draft.name.trim(), visibility: st.draft.visibility, policy: policyOf(st.draft) });

function claim(it, page) {
  return act(page, async () => { await api(path(it, '/claim'), jbody(body(), 'POST')); st.draft = null; },
    'Claimed — it is yours now. Messages to the bot are answered by the rules below.');
}
function save(it, page) {
  return act(page, async () => { await api(path(it), jbody(body(), 'PUT')); st.draft = null; }, 'Saved.');
}
const toggle = (it, page) => act(page, () => api(path(it), jbody({ enabled: !it.enabled }, 'PUT')));
async function del(it, page) {
  if (!confirm(`Remove "${it.name}"? Its conversations stay; if its adapter is still bound it shows up again, unclaimed.`)) return;
  await act(page, () => api(path(it), { method: 'DELETE' }));
  page.show(null);
}
function pair(it, page) {
  const code = st.code.trim();
  if (!code) return;
  return act(page, async () => {
    const r = await api(path(it, '/pair'), jbody({ code }, 'POST'));
    st.code = '';
    return `Paired with ${r.name || r.peerId}.`;
  });
}
const peer = (it, page, id, patch) => act(page, () => api(path(it, `/peers/${encodeURIComponent(id)}`), jbody(patch, 'PUT')));
const forget = (it, page, id) => act(page, () => api(path(it, `/peers/${encodeURIComponent(id)}`), { method: 'DELETE' }));
const resetSession = (it, page, key) => act(page, () => api(path(it, '/sessions/reset'), jbody({ key }, 'POST')),
  'That session starts afresh with its next message.');
const retry = (it, page, oid) => act(page, () => api(path(it, `/outbox/${oid}/retry`), { method: 'POST' }), 'Queued again.');

// --- the card and the detail -------------------------------------------------

function card(p, it) {
  const c = it.config || {};
  const live = it.enabled || it.access === 'claim';
  return html`<div class="acard2 ${live ? '' : 'off'}" data-auto=${'channel:' + it.id} @click=${() => p.show('channel', it.id)}>
    <div class="ah"><span class="nm">${it.name}</span>
      ${it.access === 'claim' ? html`<span class="badge unread">new — claim it</span>` : nothing}
      ${c.pendingPeers ? html`<span class="badge unread">${c.pendingPeers} waiting to pair</span>` : nothing}
      ${c.failedDeliveries ? html`<span class="badge error">${c.failedDeliveries} not delivered</span>` : nothing}
      ${it.unread ? html`<span class="badge unread">${it.unread} new</span>` : nothing}
      ${live ? nothing : html`<span class="badge">off</span>`}
      <span style="flex:1"></span>
      ${it.access === 'oversee' ? html`<span class="muted small">${it.owner || 'managers'}'s</span>`
        : it.access !== 'owner' && it.owner ? html`<span class="muted small">by ${it.owner}</span>` : nothing}
    </div>
    <div class="as muted small">${it.summary}${c.lastSeen ? ` · heard from ${ago(c.lastSeen)}` : ''}${it.runs ? ` · ${it.runs} conversation${it.runs === 1 ? '' : 's'}` : ''}</div>
  </div>`;
}

function head(it, p) {
  if (it.access !== 'owner' && it.access !== 'oversee') return nothing;
  return html`<label class="chk small"><input type="checkbox" .checked=${it.enabled} @change=${() => toggle(it, p)}> on</label>
    <button class="btn rm btnsm" @click=${() => del(it, p)}>Remove</button>`;
}

function detail(it, p) {
  const c = it.config || {};
  const owner = it.access === 'owner';
  if (!st.draft || st.id !== it.id) st.draft = draftOf(it);
  const info = html`<div class="muted small">${it.summary}${c.botName ? ` · as ${c.botName}` : ''}
    ${c.lastSeen ? ` · last heard from ${ago(c.lastSeen)}` : ''}</div>
    ${st.note ? html`<div class="note small">${st.note}</div>` : nothing}`;
  if (it.access === 'claim') {
    return html`${info}
      <p class="small">${c.adapter} connected this ${c.platform || 'chat'} account. Until someone claims it, the bot ignores every
        message. Claiming makes it yours: its conversations are yours (or your team's), under the rules below — you can change
        them any time.</p>
      ${rulesTpl(it, p, true)}`;
  }
  return html`${info}
    ${owner ? pairingTpl(it, p) : nothing}
    ${owner ? peopleTpl(it, p) : nothing}
    ${sessionsTpl(it, p, owner)}
    ${owner && st.failed.length ? failedTpl(it, p) : nothing}
    ${owner ? rulesTpl(it, p, false) : nothing}`;
}

function pairingTpl(it, p) {
  const pending = st.peers.filter((x) => x.state === 'pending' && x.codeExpires * 1000 > Date.now());
  if (st.draft.dmPolicy !== 'pairing' && !pending.length) return nothing;
  return html`<h5>Pairing</h5>
    <div class="muted small">Someone new who messages the bot gets a code. When they tell it to you, enter it here.</div>
    <div class="chadd"><input class="mono" placeholder="code" .value=${st.code} @input=${(e) => { st.code = e.target.value; }}
        @keydown=${(e) => { if (e.key === 'Enter') pair(it, p); }}>
      <button class="btn btnsm" @click=${() => pair(it, p)}>Approve</button></div>
    ${pending.map((x) => html`<div class="chrow" data-peer=${x.peerId}><span>${x.name || x.peerId}</span>
      <span class="muted small mono">${x.peerId}</span><span style="flex:1"></span>
      <span class="muted small">code valid for ${Math.max(1, Math.round((x.codeExpires * 1000 - Date.now()) / 60000))} min</span>
      <button class="btn ghost btnsm" @click=${() => peer(it, p, x.peerId, { state: 'allowed' })}>Allow</button>
      <button class="btn ghost btnsm" @click=${() => peer(it, p, x.peerId, { state: 'blocked' })}>Block</button></div>`)}`;
}

function peopleTpl(it, p) {
  const known = st.peers.filter((x) => x.state !== 'pending');
  return html`<h5>People</h5>
    ${known.length ? known.map((x) => html`<div class="chrow" data-peer=${x.peerId}><span>${x.name || x.peerId}</span>
      <span class="muted small mono">${x.peerId}</span>
      <span class="badge ${x.state === 'blocked' ? 'error' : ''}">${x.state}</span><span style="flex:1"></span>
      ${st.draft.privateLane ? html`<label class="chk small" title="may reach internal systems (the private lane) and /approve tool calls">
        <input type="checkbox" .checked=${x.trusted} @change=${() => peer(it, p, x.peerId, { trusted: !x.trusted })}> trusted</label>` : nothing}
      <button class="btn ghost btnsm" @click=${() => peer(it, p, x.peerId, { state: x.state === 'blocked' ? 'allowed' : 'blocked' })}>
        ${x.state === 'blocked' ? 'Allow' : 'Block'}</button>
      <button class="btn ghost btnsm" title="they pair again next time" @click=${() => forget(it, p, x.peerId)}>Forget</button></div>`)
      : html`<div class="muted small empty-line">nobody yet</div>`}
    <div class="chadd"><input class="mono" placeholder="their ${(it.config || {}).platform || 'platform'} user id" .value=${st.add}
        @input=${(e) => { st.add = e.target.value; }}>
      <button class="btn ghost btnsm" @click=${() => { const id = st.add.trim(); st.add = ''; if (id) peer(it, p, id, { state: 'allowed' }); }}>Allow</button></div>`;
}

function sessionsTpl(it, p, owner) {
  return html`<h5>Sessions</h5>
    ${st.sessions.length ? st.sessions.map((s) => html`<div class="chrow">
      <a class="mono small" @click=${() => s.runId && p.on.select(s.runId)} title="open its conversation">${s.key}</a>
      <span style="flex:1"></span>
      <span class="muted small">${s.lastIn ? ago(s.lastIn) : ''}${s.resets ? ` · started afresh ${s.resets}×` : ''}</span>
      ${owner && s.runId ? html`<button class="btn ghost btnsm" @click=${() => resetSession(it, p, s.key)}>Start afresh</button>` : nothing}
    </div>`) : html`<div class="muted small empty-line">none yet — each DM and thread gets its own</div>`}`;
}

function failedTpl(it, p) {
  return html`<h5>Not delivered</h5>
    ${st.failed.map((o) => html`<div class="chrow"><span class="badge error">${o.kind}</span>
      <span class="small">${(o.body.text || '').slice(0, 120)}</span><span style="flex:1"></span>
      <span class="muted small" title=${o.error}>${(o.error || '').slice(0, 60)}</span>
      <button class="btn ghost btnsm" @click=${() => retry(it, p, o.id)}>Retry</button></div>`)}`;
}

function rulesTpl(it, p, claiming) {
  const d = st.draft;
  const set = (k) => (e) => { d[k] = e.target.type === 'checkbox' ? e.target.checked : e.target.value; p.changed(); };
  const opt = (k, v, label) => html`<option value=${v} ?selected=${d[k] === v}>${label}</option>`;
  return html`<h5>Rules</h5>
    <div class="row2">
      <div class="field"><label>Name</label><input .value=${d.name} @input=${set('name')}></div>
      <div class="field"><label>Who can read its conversations</label><select @change=${set('visibility')}>
        ${opt('visibility', 'private', 'only its owner')}${opt('visibility', 'team', 'everyone who can open this agent')}</select></div>
    </div>
    <div class="row2">
      <div class="field"><label>Direct messages</label><select @change=${set('dmPolicy')}>
        ${opt('dmPolicy', 'pairing', 'new people pair with a code you approve')}${opt('dmPolicy', 'allowlist', 'only people you allow')}
        ${opt('dmPolicy', 'open', 'anyone')}${opt('dmPolicy', 'disabled', 'off')}</select></div>
      <div class="field"><label>A conversation per</label><select @change=${set('dmScope')}>
        ${opt('dmScope', '', 'person')}${opt('dmScope', 'main', 'nobody — everyone shares one')}</select></div>
    </div>
    <div class="row2">
      <div class="field"><label>Groups and channels</label><select @change=${set('groupPolicy')}>
        ${opt('groupPolicy', 'allowlist', 'only the ones listed')}${opt('groupPolicy', 'open', 'any the bot is in')}
        ${opt('groupPolicy', 'disabled', 'off')}</select></div>
      ${d.groupPolicy === 'allowlist' ? html`<div class="field"><label>Listed (conversation ids)</label>
        <input class="mono" .value=${d.allow} @input=${set('allow')} placeholder="C0123, C0456"></div>` : nothing}
    </div>
    <div class="field">
      <label class="chk small"><input type="checkbox" .checked=${d.requireMention} @change=${set('requireMention')}> in groups, answer only when mentioned</label>
      <label class="chk small"><input type="checkbox" .checked=${d.followThreads} @change=${set('followThreads')}> …and keep following a thread it answered in</label>
      <label class="chk small"><input type="checkbox" .checked=${d.groupThreads === 'parent'}
        @change=${(e) => { d.groupThreads = e.target.checked ? 'parent' : ''; p.changed(); }}> one conversation per group, not per thread</label>
    </div>
    <div class="field"><label class="chk small"><input type="checkbox" .checked=${d.privateLane} @change=${set('privateLane')}>
      trusted people and groups may reach internal systems (the private lane)</label>
      <div class="muted small">Otherwise every conversation here runs in the web lane: a reply leaves the workspace, so it never
        holds internal data. Trust people under People.</div>
      ${d.privateLane ? html`<input class="mono" .value=${d.trustedGroups} @input=${set('trustedGroups')} placeholder="trusted group ids">` : nothing}
    </div>
    <div class="row2">
      <div class="field"><label>Start a conversation afresh</label><select @change=${set('reset')}>
        ${opt('reset', '', 'never (send /new)')}${opt('reset', 'idle:3600', 'after an hour of quiet')}
        ${opt('reset', 'idle:86400', 'after a day of quiet')}${opt('reset', 'daily:4', 'every day at 4:00')}</select></div>
      <div class="field"><label>Messages per person per minute</label>
        <input type="number" min="1" .value=${String(d.ratePerMin || '')} @input=${set('ratePerMin')} placeholder="20"></div>
    </div>
    <div class="field"><label>Extra instructions</label>
      <textarea rows="2" .value=${d.system} @input=${set('system')} placeholder="e.g. answer in the language you are written to"></textarea></div>
    <div><button class="btn" @click=${() => (claiming ? claim(it, p) : save(it, p))}>${claiming ? 'Claim' : 'Save rules'}</button></div>`;
}

registerKind('channel', {
  label: 'Channels', order: 0, card, head, detail, open, runsLabel: 'Conversations',
  empty: 'none — bind a chat adapter (the slack tile) to this agent and it shows up here to claim',
});
