// native/auto-channels.js — chat channels on the Automations screens (D86):
// a channel appears when an adapter tile bound to this agent (a messaging
// bridge) says hello; a manager claims it; its owner decides who may talk
// (pairing codes, allowlists), which lane its conversations run in, and sees
// its sessions and the replies that could not be delivered. The state and
// the actions are model/auto-channels.js — the web's auto-channels.js draws
// the same.
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { ago } from '../model/auto.js';
import { st, draftOf, channelCan, pendingPeers, knownPeers, codeMinutes, claim, save, toggle, pair, peer, forget, resetSession, retry,
  del } from '../model/auto-channels.js';

export function channelRow(p, it) {
  const c = it.config || {};
  const { live } = channelCan(it);
  const badge = it.access === 'claim' ? ['new — claim it', 'accent'] : c.failedDeliveries ? [`${c.failedDeliveries} not delivered`, 'danger']
    : c.pendingPeers ? [`${c.pendingPeers} waiting to pair`, 'accent'] : it.unread ? [`${it.unread} new`, 'accent'] : !live ? ['off', 'muted'] : null;
  const whose = it.access === 'oversee' ? `${it.owner || 'managers'}'s · ` : it.access !== 'owner' && it.owner ? `by ${it.owner} · ` : '';
  return html`<row title=${it.name} subtitle=${`${whose}${it.summary || ''}${c.lastSeen ? ` · heard from ${ago(c.lastSeen)}` : ''}${it.runs ? ` · ${it.runs} conversation${it.runs === 1 ? '' : 's'}` : ''}`}
    icon="chat" badge=${badge ? badge[0] : nothing} tone=${badge ? badge[1] : nothing} nav @tap=${() => p.show('channel', it.id)}/>`;
}

export function channelDetail(p, it) {
  const c = it.config || {};
  const { owner, manage, claim: claiming } = channelCan(it);
  if (!st.draft || st.id !== it.id) st.draft = draftOf(it);
  const info = html`<section>
    <row title=${it.summary || it.name} subtitle=${`${c.botName ? `as ${c.botName}` : ''}${c.lastSeen ? `${c.botName ? ' · ' : ''}last heard from ${ago(c.lastSeen)}` : ''}` || nothing}/>
    ${st.note ? html`<notice tone="ok" text=${st.note}/>` : nothing}
    ${manage && !claiming ? html`<toggle label="On" value=${!!it.enabled} @change=${() => toggle(it, p)}/>
      <button icon="trash" role="destructive" confirm=${{ title: `Remove "${it.name}"?`, destructive: true, label: 'Remove',
        message: 'Its conversations stay; if its adapter is still bound it shows up again, unclaimed.' }} @tap=${() => del(it, p)}>Remove</button>` : nothing}
  </section>`;
  if (claiming) {
    return html`${info}<section>
      <text>${`${c.adapter} connected this ${c.platform || 'chat'} account. Until someone claims it, the bot ignores every message. Claiming makes it yours: its conversations are yours (or your team's), under the rules below — you can change them any time.`}</text>
    </section>${rulesTpl(it, p, true)}`;
  }
  return html`${info}
    ${owner ? pairingTpl(it, p) : nothing}
    ${owner ? peopleTpl(it, p) : nothing}
    ${sessionsTpl(it, p, owner)}
    ${owner && st.failed.length ? failedTpl(it, p) : nothing}
    ${owner ? rulesTpl(it, p, false) : nothing}`;
}

function pairingTpl(it, p) {
  const pending = pendingPeers();
  if (st.draft.dmPolicy !== 'pairing' && !pending.length) return nothing;
  return html`<section title="Pairing" footer=${`Someone new who messages the bot gets a code. If they have an account here they link it themselves (on ${(it.config || {}).adapter}'s page); otherwise they tell you the code and you enter it here.`}>
    <field label="Code" placeholder="code" value=${st.code} submit="done" @input=${(e) => { st.code = e.value; }} @submit=${() => pair(it, p)}/>
    <button @tap=${() => pair(it, p)}>Approve</button>
    ${repeat(pending, (x) => x.peerId, (x) => html`<row title=${x.name || x.peerId} subtitle=${x.peerId} mono="subtitle" detail=${`code valid for ${codeMinutes(x)} min`}>
      <actions><button @tap=${() => peer(it, p, x.peerId, { state: 'allowed' })}>Allow</button>
        <button role="destructive" @tap=${() => peer(it, p, x.peerId, { state: 'blocked' })}>Block</button></actions></row>`)}
  </section>`;
}

function peopleTpl(it, p) {
  const known = knownPeers();
  return html`<section title="People">
    ${known.length ? repeat(known, (x) => x.peerId, (x) => html`<row title=${x.name || x.peerId}
        subtitle=${`${x.peerId}${x.xbinUser ? ` · @${x.xbinUser}, linked ${ago(x.linkedAt)}` : ''}${x.trusted ? ' · trusted' : ''}`}
        badge=${x.state} tone=${x.state === 'blocked' ? 'danger' : nothing}>
      <actions>
        <button @tap=${() => peer(it, p, x.peerId, { state: x.state === 'blocked' ? 'allowed' : 'blocked' })}>${x.state === 'blocked' ? 'Allow' : 'Block'}</button>
        ${st.draft.privateLane ? html`<button @tap=${() => peer(it, p, x.peerId, { trusted: !x.trusted })}>${x.trusted ? 'Untrust' : 'Trust'}</button>` : nothing}
        ${x.xbinUser ? html`<button @tap=${() => peer(it, p, x.peerId, { unlink: true })}>Unlink</button>` : nothing}
        <button role="destructive" @tap=${() => forget(it, p, x.peerId)}>Forget</button>
      </actions></row>`) : html`<empty text="nobody yet"/>`}
    <field label="Allow someone" placeholder=${`their ${(it.config || {}).platform || 'platform'} user id`} value=${st.add} @input=${(e) => { st.add = e.value; }}/>
    <button @tap=${() => { const id = st.add.trim(); st.add = ''; if (id) peer(it, p, id, { state: 'allowed' }); }}>Allow</button>
  </section>`;
}

function sessionsTpl(it, p, owner) {
  return html`<section title="Sessions">
    ${st.sessions.length ? repeat(st.sessions, (s) => s.key, (s) => html`<row title=${s.key} mono="title"
        subtitle=${`${s.lastIn ? ago(s.lastIn) : ''}${s.resets ? ` · started afresh ${s.resets}×` : ''}` || nothing}
        nav=${!!s.runId} @tap=${s.runId ? () => p.on.select(s.runId) : nothing}>
      ${owner && s.runId ? html`<actions><button icon="refresh" @tap=${() => resetSession(it, p, s.key)}>Start afresh</button></actions>` : nothing}
    </row>`) : html`<empty text="none yet — each DM and thread gets its own"/>`}
  </section>`;
}

function failedTpl(it, p) {
  return html`<section title="Not delivered">${repeat(st.failed, (o) => o.id, (o) => html`<row title=${((o.body || {}).text || '').slice(0, 120)}
      subtitle=${(o.error || '').slice(0, 60) || nothing} badge=${o.kind} tone="danger">
    <actions><button icon="refresh" @tap=${() => retry(it, p, o.id)}>Retry</button></actions></row>`)}</section>`;
}

const O = (pairs) => pairs.map(([value, label]) => ({ value, label }));

function rulesTpl(it, p, claiming) {
  const d = st.draft;
  const set = (k, paint = false) => (e) => { d[k] = e.value; if (paint) p.changed(); };
  return html`<section title="Rules">
      <field label="Name" value=${d.name} @input=${set('name')}/>
      <picker label="Who can read its conversations" style="menu" value=${d.visibility}
        options=${O([['private', 'only its owner'], ['team', 'everyone who can open this agent']])} @change=${set('visibility', true)}/>
      <picker label="Direct messages" style="menu" value=${d.dmPolicy} @change=${set('dmPolicy', true)} options=${O([
        ['pairing', 'new people link their account, or pair with a code you approve'], ['linked', 'only people who linked their xbin account'],
        ['allowlist', 'only people you allow'], ['open', 'anyone'], ['disabled', 'off']])}/>
      <picker label="A conversation per" style="menu" value=${d.dmScope} options=${O([['', 'person'], ['main', 'nobody — everyone shares one']])} @change=${set('dmScope', true)}/>
    </section>
    <section title="Groups and channels">
      <picker label="Groups" style="menu" value=${d.groupPolicy} @change=${set('groupPolicy', true)}
        options=${O([['allowlist', 'only the ones listed'], ['open', 'any the bot is in'], ['disabled', 'off']])}/>
      ${d.groupPolicy === 'allowlist' ? html`<field label="Listed (conversation ids)" placeholder="C0123, C0456" value=${d.allow} @input=${set('allow')}/>` : nothing}
      <toggle label="In groups, answer only when mentioned" value=${d.requireMention} @change=${set('requireMention', true)}/>
      <toggle label="…and keep following a thread it answered in" value=${d.followThreads} @change=${set('followThreads', true)}/>
      <toggle label="One conversation per group, not per thread" value=${d.groupThreads === 'parent'}
        @change=${(e) => { d.groupThreads = e.value ? 'parent' : ''; p.changed(); }}/>
      <toggle label="In groups, only people who linked their xbin account" value=${d.linkedOnly} @change=${set('linkedOnly', true)}/>
    </section>
    <section title="Lanes" footer="Otherwise every conversation here runs in the web lane: a reply leaves the workspace, so it never holds internal data. Trust people under People.">
      <toggle label="Trusted people and groups may reach internal systems (the private lane)" value=${d.privateLane} @change=${set('privateLane', true)}/>
      ${d.privateLane ? html`<field label="Trusted group ids" value=${d.trustedGroups} @input=${set('trustedGroups')}/>
        <toggle label="People who linked their xbin account count as trusted" value=${d.trustLinked} @change=${set('trustLinked', true)}/>` : nothing}
    </section>
    <section>
      <picker label="Start a conversation afresh" style="menu" value=${d.reset} @change=${set('reset', true)}
        options=${O([['', 'never (send /new)'], ['idle:3600', 'after an hour of quiet'], ['idle:86400', 'after a day of quiet'], ['daily:4', 'every day at 4:00']])}/>
      <field label="Messages per person per minute" kind="number" placeholder="20" value=${String(d.ratePerMin || '')} @input=${set('ratePerMin')}/>
      <field label="Extra instructions" kind="multiline" placeholder="e.g. answer in the language you are written to" value=${d.system} @input=${set('system')}/>
      <button role="primary" @tap=${() => (claiming ? claim(it, p) : save(it, p))}>${claiming ? 'Claim' : 'Save rules'}</button>
    </section>`;
}
