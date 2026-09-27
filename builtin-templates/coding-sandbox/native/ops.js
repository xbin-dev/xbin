// native/ops.js — the operators' Sandboxes tab in the native view (every
// consumer's sandboxes, their usage, the substrate, orphans) and one
// sandbox's screen: its facts, lifecycle, snapshots and sharing. The same
// model as the web's web-ops.js (model/ops.js).
import { html, nothing, repeat } from '/vendor/xb-native.js';
import * as F from '../model/format.js';
import * as O from '../model/ops.js';
import { ui, ctx, act, push, back, set, form } from './ui.js';

const ACT_ICON = { start: 'play', stop: 'stop', delete: 'trash', snapshots: 'archive', shares: 'people' };

// opsSections: the Sandboxes tab's content.
export function opsSections() {
  const app = ctx.app;
  if (app.opsErr) return html`<section><notice tone="danger" text=${app.opsErr}/></section>`;
  if (!app.ops) return html`<section><progress label="loading…"/></section>`;
  const b = app.backend();
  const rows = app.opRows();
  const usage = app.usage();
  const orphans = app.ops.orphans || [];
  return html`
    <section title="Substrate" footer=${b.caps.length ? `offers ${b.caps.join(' ')}` : ''}>
      <row title=${b.name} subtitle=${b.modes.map((m) => m.mode + (m.accel ? ` (${m.accel})` : '')).join(', ') || 'no modes'}
        detail=${b.egress.map((e) => F.EGRESS[e] || e).join(', ')} icon="server"/>
      ${b.errors.map((e) => html`<notice tone="danger" text=${e}/>`)}
      ${b.hint ? html`<notice tone="info" text=${b.hint}/>` : nothing}
      ${b.notes.map((n) => html`<notice tone="info" text=${n}/>`)}
    </section>
    <section title="Sandboxes" badge=${String(rows.length)}>
      ${rows.length ? repeat(rows, (r) => r.id, (r) => html`<row title=${r.name} subtitle=${`${r.consumer} · ${r.owner}`}
          detail=${r.stateLabel} tone=${r.tone} nav @tap=${() => push({ kind: 'op', id: r.id })}>
          <actions>${r.actions.filter((a) => a.id !== 'snapshots' && a.id !== 'shares').map((a) => html`<button icon=${ACT_ICON[a.id]}
            role=${a.danger ? 'destructive' : 'secondary'} confirm=${a.confirm ? { title: `Delete ${r.name}?`, message: a.confirm, label: 'Delete', destructive: true } : undefined}
            @tap=${() => act(`${r.id}:${a.id}`, () => app.opAct(r.id, a.id))}>${a.label}</button>`)}</actions></row>`)
        : html`<empty icon="box" title="No sandboxes yet" text=${`Bind a consumer: bx bind apps/agent sandboxes+=${app.self}`}/>`}
    </section>
    ${usage.length ? html`<section title="Usage against the quotas">
      ${repeat(usage, (u) => u.kind + u.who, (u) => html`<row title=${u.who} subtitle=${u.kind + (u.override ? ' · its own quota' : '')}
        detail=${`${u.cells[0].text} · ${u.cells[1].text} running`} tone=${u.full ? 'warn' : undefined} mono="title"/>`)}
    </section>` : nothing}
    ${orphans.length ? html`<section title="Orphans" footer="The substrate keeps them for this tile; the manager doesn't know them.">
      ${repeat(orphans, (o) => o.name, (o) => html`<row title=${o.name} detail=${F.STATES[o.state] || o.state} mono="title">
        <actions><button icon="trash" role="destructive" confirm=${{ title: 'Delete the orphan?', message: `${o.name} goes at the substrate.`, label: 'Delete', destructive: true }}
          @tap=${() => act('orphan', () => app.dropOrphan(o.name), `${o.name} deleted`)}>Delete</button></actions></row>`)}
    </section>` : nothing}`;
}

// opScreen: one sandbox of any consumer's — metadata, lifecycle, snapshots, sharing.
export function opScreen(s) {
  const app = ctx.app;
  const r = app.opRows().find((x) => x.id === s.id);
  if (!r) return html`<screen title="Sandbox" style="form"><section><notice tone="muted" text="It is gone."/></section></screen>`;
  if (!s.snapsAsked && r.actions.some((a) => a.id === 'snapshots')) { s.snapsAsked = true; app.opSnapshots(r.id); }
  const snaps = app.snaps && app.snaps.id === r.id ? app.snaps : null;
  const sk = 'snap:' + r.id;
  const shk = 'share:ops-' + r.id;
  const sf = form(shk, { consumer: '', users: '' });
  const lifecycle = r.actions.filter((a) => a.id === 'start' || a.id === 'stop' || a.id === 'delete');
  return html`<screen title=${r.name} subtitle=${r.id} style="form" refreshable @refresh=${() => app.load()}>
    ${ui.err ? html`<section><notice tone="danger" text=${ui.err}/></section>` : nothing}
    <section title="Sandbox">
      <row title="State" detail=${r.stateLabel} tone=${r.tone} subtitle=${r.stateDetail || undefined}/>
      <row title="Consumer" detail=${r.consumer} mono="detail"/>
      <row title="Owner" detail=${r.owner}/>
      <row title="Image" detail=${r.image}/>
      <row title="Size" detail=${r.size} subtitle=${r.sizeText}/>
      <row title="Network" detail=${r.egressText}/>
      <row title="Isolation" detail=${r.isolation}/>
      <row title="Disk" detail=${r.disk || '—'}/>
      <row title="Last active" detail=${r.lastText || '—'}/>
    </section>
    <section>
      ${lifecycle.map((a) => html`<button icon=${ACT_ICON[a.id]} role=${a.danger ? 'destructive' : 'primary'} ?busy=${ui.busy === `${r.id}:${a.id}`}
        confirm=${a.confirm ? { title: `Delete ${r.name}?`, message: a.confirm, label: 'Delete', destructive: true } : undefined}
        @tap=${() => act(`${r.id}:${a.id}`, async () => { await app.opAct(r.id, a.id); if (a.id === 'delete') back(); })}>${a.label}</button>`)}
    </section>
    ${r.actions.some((a) => a.id === 'snapshots') ? html`<section title="Snapshots">
      ${!snaps || (!snaps.list && !snaps.err) ? html`<progress label="loading…"/>` : nothing}
      ${snaps && snaps.err ? html`<notice tone="danger" text=${snaps.err}/>` : nothing}
      ${snaps && snaps.list ? repeat(snaps.list, (x) => x.id, (x) => html`<row title=${x.name || x.id} subtitle=${x.id} detail=${F.ago(x.created)} mono="subtitle">
        <actions><button icon="refresh" confirm=${{ title: 'Restore it?', message: `${r.name} goes back to “${x.name || x.id}”: what changed since is lost, its running commands are killed.`, label: 'Restore', destructive: true }}
            @tap=${() => act('restore', () => app.opRestore(r.id, x.id), `${r.name} restored`)}>Restore</button>
          <button icon="trash" role="destructive" confirm=${{ title: 'Delete the snapshot?', message: x.name || x.id, label: 'Delete', destructive: true }}
            @tap=${() => act('snapdel', () => app.opSnapDelete(r.id, x.id))}>Delete</button></actions></row>`) : nothing}
      <field label="Name" placeholder="optional" value=${(ui.forms[sk] || {}).name || ''} @input=${set(sk, 'name')}/>
      <button icon="archive" ?busy=${ui.busy === 'snap'} @tap=${() => act('snap', () => app.opSnapshot(r.id, (ui.forms[sk] || {}).name).then(() => { ui.forms[sk] = null; }), 'snapshot taken')}>Take a snapshot</button>
    </section>` : nothing}
    ${sharesSection(r.shares, (x) => app.opShares(r.id, x), shk, sf)}
  </screen>`;
}

// sharesSection: a sandbox's shares and the form to add one (both tabs').
export function sharesSection(shares, save, key, f) {
  const add = () => {
    const g = ui.forms[key] || f;
    const r = O.shareWith(shares, g.consumer, g.users);
    if (r.error) { ui.err = r.error; ctx.paint(); return; }
    act('share', () => save(r.shares).then(() => { ui.forms[key] = null; }), `shared with ${g.consumer.trim()}`);
  };
  return html`<section title="Shared with" footer="A consumer tile you share it with lists it among its own; with named people, only they use it there.">
    ${shares.length ? repeat(shares, (x) => x.consumer, (x) => html`<row title=${x.consumer} subtitle=${F.usersText(x.users)} mono="title">
      <actions><button icon="xmark" role="destructive" @tap=${() => act('unshare', () => save(O.unshare(shares, x.consumer)), `no longer shared with ${x.consumer}`)}>Stop sharing</button></actions></row>`)
      : html`<row title="no other consumer" tone="muted"/>`}
    <field label="Consumer" placeholder="apps/sandbox-terminal" value=${f.consumer} @input=${set(key, 'consumer')}/>
    <field label="People" placeholder="* (everyone it serves) or alice, bob" value=${f.users} @input=${set(key, 'users')}/>
    <button icon="people" ?busy=${ui.busy === 'share'} @tap=${add}>Share</button>
  </section>`;
}
