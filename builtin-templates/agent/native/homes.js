// native/homes.js — a person's two homes in the native view (model/homes.js,
// model/hosted.js; API.md "Partitioned instances" → "Shared conversations",
// "Non-secure conversations"), the web's homes-ui.js and hosted-ui.js share
// dialog parts as sheet forms, in the same words:
//
//   publish   "Share a copy" of one of their own conversations (its row's
//             Share a copy…, the chat's ⋯): who can see the copy, its
//             session files or not, the private original kept or deleted;
//             the copy opens (POST /runs/{id}/publish)
//   copy      a shared conversation's share sheet: Copy to my own space (POST /copy)
//   host      …: Use my private resources… — the warning first, then hosting
//             (POST /hosting); a hosted one's host takes them back (DELETE)
//   copy-in   …: Add a copy of my files… — one of their own conversations,
//             the files to copy, who can read the copies (POST /copyin)
//   new chat  New chat with options asks who can see it (newShareTpl):
//             only you, or the team or people you name (the shared space)
//
// An unpartitioned instance's view, and the global instance's own, get
// nothing here: no section, no call.
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { jbody } from '/vendor/bx-kit.js';
import { ui, ctx, say } from './ui.js';
import { homeApi } from '../model/home-api.js';
import { twoHomes, homeOf, shareOf, PARTITION_BASE } from '../model/homes.js';
import { hostedId, readers, audienceOf } from '../model/hosted.js';
import { markStarted } from './hosted.js';
import { openChat } from './nav.js';

// ui.part: the open form — {mode: 'publish' | 'host' | 'copyin', run: {id, title}, …}
const close = () => { ui.part = null; ctx.paint(); };
const opened = (id) => { ctx.app.convs.load().catch(() => {}); if (id) openChat(id); };

// step runs a form's call: busy while it runs, its failure said on the form.
async function step(f, fn) {
  f.busy = true; f.err = '';
  ctx.paint();
  try { await fn(); } catch (e) { f.err = (e && e.message) || String(e); }
  f.busy = false;
  ctx.paint();
}

/** openPublish: "Share a copy" of one of the person's own conversations (run {id, title}). */
export function openPublish(run) {
  ui.share = null;
  ui.part = { mode: 'publish', run, vis: 'team-participant', people: '', files: false, keep: true, busy: false, err: '' };
  ctx.paint();
}

const PUB_VIS = [
  { value: 'team-viewer', label: 'Everyone who can open this agent — to read' },
  { value: 'team-participant', label: 'Everyone who can open this agent — to read and write' },
  { value: 'people', label: 'Only the people below' },
];
const PUB_NOTE = 'This conversation is in your own space, which only you can open. A copy of its whole transcript goes to the shared space — '
  + 'your messages, the agent\'s answers and everything its tools returned, which can quote your private files, memory or sandbox — '
  + 'where the people you choose and the agent\'s managers can read it. Its session files go too only if you add them.';

function publishTpl(f) {
  const go = () => {
    const share = shareOf(f.vis, f.people);
    if (!share) { f.err = 'Choose who can see the copy: the team, or people.'; ctx.paint(); return; }
    step(f, async () => {
      const r = await ctx.app.actions.publish(f.run.id, { share, files: f.files, keep: f.keep });
      ui.part = null;
      opened(r && r.run && r.run.id);
      if (r && r.left && r.left.length) say(`Not copied (too large): ${r.left.join(', ')}`);
    });
  };
  return html`<sheet open title=${`Share a copy of “${f.run.title || 'conversation'}”`} detents="large" @dismiss=${close}>
    <screen title="Share a copy" subtitle=${f.run.title || nothing} style="form">
      <toolbar>
        <button role="primary" ?busy=${f.busy} ?disabled=${f.busy} @tap=${go}>${f.busy ? 'Copying…' : 'Share a copy'}</button></toolbar>
      ${f.err ? html`<section><notice tone="danger" text=${f.err}/></section>` : nothing}
      <section><notice tone="info" text=${PUB_NOTE}/></section>
      <section title="Who can see the copy">
        <picker style="inline" value=${f.vis} options=${PUB_VIS} @change=${(e) => { f.vis = e.value; ctx.paint(); }}/>
      </section>
      <section title="People" footer="User ids, comma-separated — they can write.">
        <field label="People" placeholder="user ids" value=${f.people} @input=${(e) => { f.people = e.value; }}/>
      </section>
      <section>
        <toggle label="Add its session files to the copy" value=${f.files} @change=${(e) => { f.files = !!e.value; ctx.paint(); }}/>
        <toggle label="Keep my private original (else it is deleted once the copy is made)" value=${f.keep} @change=${(e) => { f.keep = !!e.value; ctx.paint(); }}/>
      </section>
    </screen>
  </sheet>`;
}

// --- a shared conversation's share sheet: its partition sections ----------------------

/**
 * shareExtraTpl (native/share.js): in a person's partition, a shared
 * conversation's share sheet offers a private copy (unless a coding agent's:
 * keeps), its private resources (host: never a coding agent's) and copies of
 * their own files; a hosted one's host may take the resources back. d is
 * GET /runs/{id}/members. Nothing elsewhere.
 */
export function shareExtraTpl(st, d, keeps) {
  const id = Number(st.run.id);
  if (!twoHomes() || !d || !(id > 0) || id >= PARTITION_BASE) return nothing;
  const app = ctx.app;
  const me = (app.me && app.me.user) || '';
  const copy = !keeps && homeOf(id) === 'global' ? html`<section title="Your own copy"
      footer="A private copy in your own space: only you can open it; this one goes on without it.">
    <button icon="copy" @tap=${async () => {
      try { const r = await app.actions.copyToMine(id); ui.share = null; opened(r && r.id); } catch (e) { st.err = e.message; ctx.paint(); }
    }}>Copy to my own space</button>
  </section>` : nothing;
  if (hostedId(id)) {
    const h = d.hosted || {};
    if (!me || h.host !== me || (h.state !== 'active' && h.state !== 'paused')) return copy;
    return html`${copy}<section title="Your private resources"
        footer="This conversation uses your private resources. Take them back: it pauses until someone continues it without them.">
      <button role="destructive" icon="lock" confirm=${{ title: 'Take your private resources back?', label: 'Take them back', destructive: true }}
        @tap=${async () => {
          try { await homeApi('', `/hosting/${id}`, { method: 'DELETE' }); ui.share = null; opened(0); } catch (e) { st.err = e.message; ctx.paint(); }
        }}>Take them back</button>
    </section>`;
  }
  const title = st.run.title || '';
  return html`${copy}<section title="Not private: your own resources"
      footer=${keeps ? 'Add copies of your own session files; your originals stay private.'
        : 'Let the agent use your sandboxes, your data in other tiles and your vault in this conversation. It becomes non-secure — you are warned first. Or add copies of your own session files; your originals stay private.'}>
    ${keeps ? nothing : html`<button icon="shield" @tap=${() => { ui.share = null; ui.part = { mode: 'host', run: { id, title }, acl: d, busy: false, err: '' }; ctx.paint(); }}>Use my private resources…</button>`}
    <button icon="folder" @tap=${() => openCopyIn({ id, title }, d)}>Add a copy of my files…</button>
  </section>`;
}

// --- hosting: the warning, then POST /hosting -------------------------------------------

const HOST_TEXT = 'Letting the agent use your private sandboxes, your data in other tiles and your vault here makes this conversation non-secure: '
  + 'the agent acts with them in it, and what it reads or writes with them goes into a transcript all of the people below can read.';

function hostTpl(f) {
  const go = () => step(f, async () => {
    const r = await homeApi('', '/hosting', jbody({ conversation: f.run.id, seen: audienceOf(f.acl) }, 'POST'));
    markStarted(r.conversation); // the person just read the warning: it starts here
    ui.part = null;
    opened(r.conversation);
  });
  return html`<sheet open title=${`“${f.run.title || 'conversation'}” is not private`} detents="large" @dismiss=${close}>
    <screen title="Not private" subtitle=${f.run.title || nothing} style="form">
      <toolbar>
        <button role="primary" ?busy=${f.busy} ?disabled=${f.busy} @tap=${go}>${f.busy ? 'Moving it…' : 'Use my private resources'}</button></toolbar>
      ${f.err ? html`<section><notice tone="danger" text=${f.err}/></section>` : nothing}
      <section><notice tone="warn" title="Not private" text=${HOST_TEXT}/></section>
      <section title="Who can read it" footer="It has no join links. Adding people later pauses it until you confirm them; you can take your resources back at any time (then it continues without them).">
        ${readers(f.acl).map((r) => html`<row title=${r}/>`)}
      </section>
    </screen>
  </sheet>`;
}

// --- add a copy of my files: POST /copyin -------------------------------------------------

function openCopyIn(run, acl) {
  ui.share = null;
  const f = ui.part = { mode: 'copyin', run, acl, convs: null, pick: 0, files: null, chosen: new Set(), busy: false, err: '' };
  ctx.paint();
  homeApi('', '/conversations?scope=mine&limit=50').then((r) => {
    f.convs = [...(r.pinned || []), ...(r.items || [])].filter((c) => c.id >= PARTITION_BASE);
  }).catch((e) => { f.err = e.message; }).finally(() => ctx.paint());
}

async function pick(f, id) {
  f.pick = +id || 0; f.files = null; f.chosen = new Set(); f.err = '';
  ctx.paint();
  if (!f.pick) return;
  try { f.files = (await homeApi('', `/runs/${f.pick}/files`)) || []; } catch (e) { f.err = e.message; }
  ctx.paint();
}

function copyInTpl(f) {
  const go = () => {
    if (!f.chosen.size) { f.err = 'Choose the files to copy.'; ctx.paint(); return; }
    step(f, async () => {
      const files = [...f.chosen].map((path) => ({ run: f.pick, path }));
      const r = await homeApi('', '/copyin', jbody({ conversation: f.run.id, files }, 'POST'));
      ui.part = null;
      ctx.app.session.refresh?.(f.run.id)?.catch?.(() => {});
      say(`Copied into the conversation: ${((r && r.files) || []).join(', ')}`);
    });
  };
  const options = [{ value: 0, label: 'choose one of your own conversations…' },
    ...(f.convs || []).map((c) => ({ value: c.id, label: c.title || 'conversation ' + c.id }))];
  return html`<sheet open title="Add a copy of my files" detents="large" @dismiss=${close}>
    <screen title="Add a copy of my files" subtitle=${f.run.title || nothing} style="form">
      <toolbar>
        <button role="primary" ?busy=${f.busy} ?disabled=${f.busy} @tap=${go}>${f.busy ? 'Copying…' : 'Add the copies'}</button></toolbar>
      ${f.err ? html`<section><notice tone="danger" text=${f.err}/></section>` : nothing}
      <section title="From your conversation">
        ${!f.convs ? html`<progress label="loading…"/>` : html`<picker label="Conversation" style="menu" value=${f.pick} options=${options}
          @change=${(e) => pick(f, e.value)}/>`}
      </section>
      ${f.pick ? html`<section title="Its session files">
        ${!f.files ? html`<progress label="loading…"/>` : !f.files.length ? html`<empty icon="folder" title="none"/>`
          : repeat(f.files, (x) => x.path, (x) => html`<toggle label=${x.path} value=${f.chosen.has(x.path)}
            @change=${(e) => { if (e.value) f.chosen.add(x.path); else f.chosen.delete(x.path); ctx.paint(); }}/>`)}
      </section>` : nothing}
      <section title="Who can read the copies" footer="They go into this shared conversation as its session files. Your originals stay private, where they are.">
        ${readers(f.acl).map((r) => html`<row title=${r}/>`)}
      </section>
    </screen>
  </sheet>`;
}

/** partitionSheets (native.js, beside the nav): the open form's sheet. */
export function partitionSheets() {
  const f = ui.part;
  if (!f) return nothing;
  if (f.mode === 'publish') return publishTpl(f);
  if (f.mode === 'host') return hostTpl(f);
  if (f.mode === 'copyin') return copyInTpl(f);
  return nothing;
}

// --- new chat with options: who can see it ------------------------------------------------

const NEW_VIS = [
  { value: 'mine', label: 'Only you — in your own space' },
  { value: 'team-participant', label: 'Everyone who can open this agent — to read and write (shared space)' },
  { value: 'team-viewer', label: 'Everyone who can open this agent — to read (shared space)' },
  { value: 'people', label: 'Only the people you name (shared space)' },
];

/** newShareTpl (native/convs.js newChatSheet): "Who can see it" in a person's partition — f is the sheet's state. */
export function newShareTpl(f) {
  if (!twoHomes()) return nothing;
  const vis = f.vis || 'mine';
  return html`<section title="Who can see it" footer=${f.shareErr || nothing}>
    <picker label="Who can see it" style="menu" value=${vis} options=${NEW_VIS} @change=${(e) => { f.vis = e.value; f.shareErr = ''; ctx.paint(); }}/>
    ${vis === 'mine' ? nothing : html`<field label="People" value=${f.people || ''}
      placeholder=${vis === 'people' ? 'user ids, comma-separated — they can write' : 'and people too (optional): user ids, comma-separated'}
      @input=${(e) => { f.people = e.value; }}/>`}
  </section>`;
}

/** newShareBody: what Start sends for it — {share} for a shared chat, {} for
 * one of their own (and always {} elsewhere); null when people were chosen
 * and none named (the section says so and the sheet stays). */
export function newShareBody(f) {
  if (!twoHomes() || !f.vis || f.vis === 'mine') return {};
  const s = shareOf(f.vis, f.people);
  f.shareErr = s ? '' : 'Name the people who can see it (their user ids).';
  return s ? { share: s } : null;
}
