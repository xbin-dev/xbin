// native/share.js — the share sheet of a conversation (D83): who can see it
// (only the people you add, or everyone who can open this agent — to read,
// or to write too), the people it is shared with, invite links (shown once,
// handed to the share sheet) and, for someone it was shared with, Leave.
// Who may change what is model/rules.js (share); the calls are
// model/actions.js — the web's share.js draws the same dialog.
import { html, repeat, nothing, native } from '/vendor/xb-native.js';
import { ui, ctx, when } from './ui.js';
import { share as shareRules } from '../model/rules.js';

const VIS = [
  { value: 'private', label: 'Only you and the people below' },
  { value: 'team-viewer', label: 'Everyone who can open this agent — to read' },
  { value: 'team-participant', label: 'Everyone who can open this agent — to read and write' },
];
const ROLES = [{ value: 'participant', label: 'can write' }, { value: 'viewer', label: 'can read' }];
const EXPIRY = [{ value: 604800, label: 'for 7 days' }, { value: 86400, label: 'for a day' }, { value: 0, label: 'no expiry' }];
const roleWord = (r) => (r === 'participant' ? 'can write' : 'can read');

// linkFor: the address a join token is shared at — this tile's page, without
// the query (a frame token must never end up in something you paste).
export function linkFor(token) {
  const l = globalThis.location;
  return l ? `${l.protocol}//${l.host}${l.pathname}#join=${token}` : `#join=${token}`;
}

// load (re)reads who it is shared with.
async function load(st) {
  try { st.data = await ctx.app.actions.members(st.run.id); st.err = ''; } catch (e) { st.err = e.message; }
  ctx.paint();
}

async function act(st, fn) {
  st.err = '';
  try { await fn(); await load(st); ctx.app.convs.load().catch(() => {}); } catch (e) { st.err = e.message; ctx.paint(); }
}

export function shareSheet() {
  const st = ui.share;
  if (!st) return nothing;
  const app = ctx.app;
  const A = app.actions;
  if (!st.loading) { st.loading = true; Object.assign(st, { data: null, link: '', err: '', user: '', role: 'participant', linkRole: 'participant', linkExp: 604800 }); load(st); }
  const d = st.data;
  const { own, vis, leave } = shareRules(d, app.me);
  const done = () => { ui.share = null; ctx.paint(); };
  return html`<sheet open title=${`Share “${st.run.title || 'conversation'}”`} @dismiss=${done}>
    <screen title="Share" subtitle=${st.run.title || nothing} style="form">
      ${st.err ? html`<section><notice tone="danger" text=${st.err}/></section>` : nothing}
      ${!d ? html`<section><progress label="loading…"/></section>` : html`
      <section title="Who can see it">${own
        ? html`<picker style="inline" value=${vis} options=${VIS} @change=${(e) => act(st, () => A.setVisibility(st.run.id, e.value))}/>`
        : repeat(VIS, (o) => o.value, (o) => html`<row title=${o.label} ?selected=${vis === o.value}/>`)}</section>
      <section title="People">
        ${d.owner ? html`<row title=${d.owner} mono="title" detail="owner"/>` : nothing}
        ${repeat(d.members, (m) => m.user, (m) => html`<row title=${m.user} mono="title" detail=${roleWord(m.role)}>
          ${own ? html`<actions>
            <button @tap=${() => act(st, () => A.setMember(st.run.id, m.user, m.role === 'participant' ? 'viewer' : 'participant'))}>${m.role === 'participant' ? 'Make reader' : 'Make writer'}</button>
            <button role="destructive" @tap=${() => act(st, () => A.removeMember(st.run.id, m.user))}>Remove</button>
          </actions>` : nothing}</row>`)}
        ${own ? html`<field label="Add someone" placeholder="user id (their login name)" value=${st.user} @input=${(e) => { st.user = e.value; }}/>
          <picker label="Role" style="menu" value=${st.role} options=${ROLES} @change=${(e) => { st.role = e.value; ctx.paint(); }}/>
          <button @tap=${() => { const u = st.user.trim().toLowerCase(); if (u) act(st, async () => { await A.setMember(st.run.id, u, st.role); st.user = ''; }); }}>Add</button>` : nothing}
      </section>
      ${own ? html`<section title="Invite link" footer="Anyone who can open this agent and has the link joins.">
        <picker label="Role" style="menu" value=${st.linkRole} options=${ROLES} @change=${(e) => { st.linkRole = e.value; ctx.paint(); }}/>
        <picker label="Valid" style="menu" value=${st.linkExp} options=${EXPIRY} @change=${(e) => { st.linkExp = +e.value; ctx.paint(); }}/>
        <button @tap=${() => act(st, async () => { const r = await A.createLink(st.run.id, st.linkRole, st.linkExp); st.link = linkFor(r.token); })}>Create link</button>
        ${st.link ? html`<row title=${st.link} mono="title" subtitle="shown once — copy it now">
          <actions><button icon="copy" copy=${st.link}>Copy</button>
            <button icon="share" @tap=${() => native.share({ url: st.link, text: st.run.title || '' })}>Share…</button></actions></row>` : nothing}
        ${repeat(d.links || [], (l) => l.id, (l) => html`<row title=${`link #${l.id}`}
          subtitle=${`${roleWord(l.role)} · used ${l.uses}${l.maxUses ? '/' + l.maxUses : ''}${l.expires ? ' · until ' + when(l.expires * 1000) : ''}`}>
          <actions><button role="destructive" @tap=${() => act(st, () => A.revokeLink(st.run.id, l.id))}>Revoke</button></actions></row>`)}
      </section>` : leave ? html`<section>
        <button role="destructive" confirm=${{ title: 'Leave this conversation?', label: 'Leave', destructive: true }}
          @tap=${() => act(st, async () => { await A.removeMember(st.run.id, app.me.user); ui.share = null; app.convs.remove(st.run.id); if (app.root === st.run.id) app.home(); })}>Leave this conversation</button>
      </section>` : nothing}`}
    </screen>
  </sheet>`;
}
