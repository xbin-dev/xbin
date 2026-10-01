// native/harness-signins.js — a person's saved sign-ins for coding agents in
// the native view (D179; the web's Coding-agent sign-ins): pushed screens of
// their own kinds, drawn through the `screen` seam and reached from Coding
// agent settings (native/harness-ask.js, everyone's) and the managers'
// Coding agents screen (native/harness-catalog.js):
//
//   signins       per coding agent, its saved sign-ins — name, what it is, its
//                 state (expiring, expired, refused) and the default — each
//                 with Make default and Forget (confirmed) as row actions and
//                 its own screen; "Add a key or token"; outside a person's
//                 own space, why there are none;
//   signin-edit   one: Rename, Make default, Forget;
//   signin-add    a key or token pasted for one coding agent: its name, the
//                 secret in a secure field (never a prop, never on the screen's
//                 entry: dropped once sent), which key it is when the agent
//                 takes several, and whether it becomes the default.
//
// What they say is model/harness-signins.js; the calls app.harness's
// (model/harness-store.js).
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ctx, guard, push, ui } from './ui.js';
import { signinGroups, keyFor, nameFor } from '../model/harness-signins.js';

const TONE = { ok: nothing, warn: 'warn', bad: 'danger' };

// the secret being typed, per Add screen — never a prop; dropped once sent
const secrets = new WeakMap();

// leave: s goes off the stack (and what was pushed over it)
const leave = (s) => { const i = ui.stack.indexOf(s); if (i >= 0) ui.stack.splice(i); ctx.paint(); };

// openSignins pushes the sign-ins screen (read afresh).
export function openSignins() {
  ctx.app.harness.loadSignins().catch(() => {});
  push({ kind: 'signins' });
}

function listScreen(s) {
  const app = ctx.app;
  app.harness.ensureSignins();
  const st = app.harness.signins;
  const reload = guard(() => app.harness.loadSignins());
  if (!st.loaded) return html`<screen title="Coding-agent sign-ins" style="list"><section><progress label="loading…"/></section></screen>`;
  if (!st.available) {
    return html`<screen title="Coding-agent sign-ins" style="list" refreshable @refresh=${reload}>
      <section><notice tone="info" text=${st.why || 'Saved sign-ins aren\'t available here.'}/></section>
    </screen>`;
  }
  const groups = signinGroups(st);
  return html`<screen title="Coding-agent sign-ins" style="list" refreshable @refresh=${reload}>
    <section footer="Your own: kept in your space's vault, never in a sandbox. A coding agent you start in a sandbox of your own that no one else uses signs in with its default — a conversation can pick another from its Account menu.">
      ${groups.some((g) => g.rows.length) ? nothing : html`<empty icon="key" title="No saved sign-ins yet" text="Sign a coding agent in with Remember, or add a key or token below."/>`}
    </section>
    ${s.msg ? html`<section><notice tone="ok" text=${s.msg}/></section>` : nothing}
    ${ui.err ? html`<section><notice tone="danger" text=${ui.err}/></section>` : nothing}
    ${repeat(groups, (g) => g.harness, (g) => html`<section title=${g.name}
        footer=${g.mint ? `A ${g.name} sign-in is made by signing in with Remember (a one-year token), or by pasting a key or token.` : `Paste a ${g.keys.map((k) => k.label).join(' or ')}.`}>
      ${repeat(g.rows, (r) => r.id, (r) => html`<row title=${r.name} subtitle=${`${r.what} · ${r.status.text}`} icon="key" tone=${TONE[r.status.tone] || nothing}
          detail=${r.isDefault ? 'Default' : nothing} nav @tap=${() => push({ kind: 'signin-edit', id: r.id })}>
        <actions>
          ${r.isDefault ? nothing : html`<button icon="check" @tap=${guard(async () => { await app.harness.updateSignin(r.id, { default: true }); s.msg = `${r.name} is ${g.name}'s default now.`; })}>Make default</button>`}
          <button icon="trash" role="destructive" confirm=${{ title: `Forget ${r.name}?`, message: `Coding agents that use it stop now; the next message starts them without it.`, label: 'Forget', destructive: true }}
            @tap=${guard(async () => { await app.harness.forgetSignin(r.id); s.msg = `${r.name} is forgotten.`; })}>Forget</button>
        </actions>
      </row>`)}
      <row title="Add a key or token" icon="plus" nav @tap=${() => push({ kind: 'signin-add', harness: g.harness })}/>
    </section>`)}
  </screen>`;
}

function editScreen(s) {
  const app = ctx.app;
  const st = app.harness.signins;
  const sg = st.list.find((x) => x.id === s.id);
  if (!sg) {
    return html`<screen title="Saved sign-in" style="form"><section><empty icon="key" title="Gone" text="This saved sign-in is gone."/></section></screen>`;
  }
  const g = signinGroups(st).find((x) => x.harness === sg.harness);
  const row = g && g.rows.find((r) => r.id === sg.id);
  if (s.name == null) s.name = sg.name;
  const rename = guard(async () => {
    const name = String(s.name || '').trim();
    if (!name || name === sg.name) return;
    await app.harness.updateSignin(sg.id, { name });
    s.msg = `Renamed to ${name}.`;
  });
  return html`<screen title=${sg.name} subtitle=${g ? g.name : nothing} style="form">
    ${ui.err ? html`<section><notice tone="danger" text=${ui.err}/></section>` : nothing}
    ${s.msg ? html`<section><notice tone="ok" text=${s.msg}/></section>` : nothing}
    <section footer=${row ? `${row.what} · ${row.status.text}` : nothing}>
      <field kind="text" label="Name" value=${s.name} submit="done" @input=${(e) => { s.name = e.value; }} @submit=${rename}/>
      <button @tap=${rename}>Rename</button>
    </section>
    <section footer=${sg.isDefault ? 'The default: your coding agents in sandboxes of your own sign in with it.' : 'Make it the one your coding agents sign in with.'}>
      ${sg.isDefault ? html`<row title="The default" icon="check"/>`
        : html`<button icon="check" @tap=${guard(async () => { await app.harness.updateSignin(sg.id, { default: true }); s.msg = `${sg.name} is the default now.`; })}>Make default</button>`}
    </section>
    <section footer="Coding agents that use it stop now; the next message starts them without it.">
      <button role="destructive" icon="trash" confirm=${{ title: `Forget ${sg.name}?`, label: 'Forget', destructive: true }}
        @tap=${guard(async () => { await app.harness.forgetSignin(sg.id); leave(s); })}>Forget</button>
    </section>
  </screen>`;
}

function addScreen(s) {
  const app = ctx.app;
  const st = app.harness.signins;
  const g = signinGroups(st).find((x) => x.harness === s.harness);
  if (!g) return html`<screen title="Add a key or token" style="form"><section><empty icon="key" title="Not here" text="This coding agent takes no saved sign-in."/></section></screen>`;
  const several = g.keys.length > 1;
  const save = guard(async () => {
    const secret = String(secrets.get(s) || '').trim();
    if (!secret) { s.err = 'Paste the key or token first.'; return; }
    const env = s.env || (keyFor(st, g.harness, secret) || {}).env || '';
    if (!env) { s.err = 'Say which key this is.'; return; }
    secrets.delete(s);
    s.err = '';
    s.busy = true;
    ctx.paint();
    try {
      await app.harness.saveSignin({ harness: g.harness, name: String(s.name || '').trim(), secret, env, isDefault: !!s.isDefault });
    } catch (e) { s.busy = false; s.err = e.message; return; }
    s.busy = false;
    leave(s);
  });
  const opts = [{ value: '', label: 'By how it starts' }, ...g.keys.map((k) => ({ value: k.env, label: k.label }))];
  return html`<screen title=${`Add a ${g.name} sign-in`} style="form">
    ${s.err ? html`<section><notice tone="danger" text=${s.err}/></section>` : nothing}
    <section footer="Sent once to this agent's backend, kept in your space's vault — never shown again, never put in a sandbox's home.">
      <field kind="text" label="Name" placeholder=${nameFor(st, g.harness) || 'e.g. Work'} value=${s.name || ''} @input=${(e) => { s.name = e.value; }}/>
      <field kind="secure" label="Key or token" placeholder="paste it here" value="" @input=${(e) => secrets.set(s, e.value)}/>
      ${several ? html`<picker label="Which key" value=${s.env || ''} options=${opts} @change=${(e) => { s.env = e.value; ctx.paint(); }}/>` : nothing}
      <toggle label="Make it the default" value=${!!s.isDefault} @change=${(e) => { s.isDefault = !!e.value; ctx.paint(); }}/>
    </section>
    <section><button role="primary" ?busy=${!!s.busy} @tap=${save}>Save</button></section>
  </screen>`;
}

ext.register({
  screen(s) {
    if (s.kind === 'signins') return listScreen(s);
    if (s.kind === 'signin-edit') return editScreen(s);
    if (s.kind === 'signin-add') return addScreen(s);
    return null;
  },
});
