// native/harness-board.js — the Coding agents board in the native view
// (D147 §4.3.6, §8 U7), the web's harness-board.js with the app's
// primitives:
//
//   screen   'hboard' {root}: the coding agents in the open conversation's
//            tree (root null: at home, yours that run or need you) in
//            sections — Needs you, Running, Done — in the order they
//            started: a row each (what it does now, its monogram and state;
//            a tap opens its chat) with its swipe actions (Stop, Message,
//            Cancel task, confirmed); a parked one holds its permission or
//            question (native/harness-ask.js's, answered on the child) — its
//            sign-in says to open it
//            'hmsg' {id, name}: Message — a multiline field, Send (queues or
//            steers) and Send now (interrupts its turn first); the agent that
//            started it is told
//   toolbar  a button while any needs you (not in a coding agent's own chat)
//   menu     Coding agents (N) in a conversation's ⋯
//   main     at home, the main ⋯ menu's Coding agents (N) — home's bar holds
//            Conversations, the class and ⋯ only (a phone's width)
//   task     the Task screen's Delegated section: each coding agent below the
//            run, its state and task, a tap to its chat
//
// The rows are app.board's (model/harness-board.js).
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ui, ctx, push, top } from './ui.js';
import { openChild } from './chat.js';
import { parkTpl } from './harness-child.js';
import { sectioned, emptyWords, delegatedWords } from '../model/harness-board.js';
import { loadTail, cancelWords, messageWords } from '../model/harness-child.js';
import { ownerOf } from '../model/harness-ask.js';
import { HARNESSES, isHarness } from '../model/harness.js';

const TONE = { ok: 'ok', bad: 'danger', warn: 'warn', run: 'accent' };
const rootOf = (v) => (v ? v.run.rootId || v.run.id : null);
const title = (id) => (ctx.app.convs.find(id) || {}).title || '#' + id;
const needLabel = (c) => `${c.needs} coding agent${c.needs === 1 ? ' needs' : 's need'} you`;

ext.register({
  toolbar(v) {
    // not at home (its bar holds Conversations, the class and ⋯; Needs you is
    // on the page and the board in ⋯) nor in a coding agent's own chat (its
    // bar already holds Mode and Model; a phone's bar drops the ⋯ past that)
    if (!v || isHarness(v.run)) return null;
    const root = rootOf(v);
    const c = ctx.app.board.chip(root);
    const dock = ext.dock(v) || []; // other sections of this screen (CI's): the button carries their badge
    // in a conversation only while one waits for you: a phone's bar has little room
    if ((!c || !c.needs) && !dock.length) return null;
    return html`<button icon=${c && c.needs ? 'bell' : 'terminal'} @tap=${() => push({ kind: 'hboard', root })}>${[c && c.needs ? needLabel(c) : 'Coding agents', ...dock.map((d) => d.badge)].filter(Boolean).join(' · ')}</button>`;
  },
  main(before) {
    const c = ctx.app.board.chip(null);
    if (!c) return null;
    return html`<button icon="terminal" @tap=${() => { before(); push({ kind: 'hboard', root: null }); }}>${`Coding agents (${c.needs ? c.needs + ' waiting' : c.n})`}</button>`;
  },
  menu(v) {
    const root = rootOf(v);
    const c = ctx.app.board.chip(root);
    if (!c) return null;
    return html`<button icon="terminal" @tap=${() => push({ kind: 'hboard', root })}>${`Coding agents (${c.needs ? c.needs + ' waiting' : c.n})`}</button>`;
  },
  screen: (s) => (s.kind === 'hboard' ? boardScreen(s) : s.kind === 'hmsg' ? messageScreen(s) : null),
  task(s) {
    const app = ctx.app;
    const rows = app.board.delegated(app.session.merged(s.run) || app.session.current());
    if (!rows.length) return null;
    return html`<section title="Delegated" footer="The coding agents it started, each with its task — tap one for its chat">
      ${repeat(rows, (r) => r.id, (r) => {
        const d = delegatedWords(r);
        return html`<row title=${`#${d.id} ${d.title}`} subtitle=${[d.state.word, d.task !== d.title ? d.task : ''].filter(Boolean).join(' · ')}
          detail=${d.mono} tone=${TONE[d.state.tone] || nothing} nav @tap=${() => openRow(r)}/>`;
      })}
    </section>`;
  },
});

// openRow: its chat — over its parent's in a conversation; at home, as a conversation of its own
function openRow(r) {
  if (ctx.app.sel != null && r.id !== r.root) return openChild(r.id);
  ui.stack.length = 0;
  ctx.app.select(r.id);
}

// act runs a row's action; what it said (or why it failed) stays on the board
const act = (s, fn, note) => async () => {
  s.err = ''; s.note = '';
  try { await fn(); s.note = note; } catch (e) { s.err = (e && e.message) || String(e); }
  ctx.paint();
};

function boardScreen(s) {
  const app = ctx.app;
  const home = s.root == null;
  const rows = app.board.rows(s.root);
  for (const r of rows) if (r.card.park && !r.card.park.harness) loadTail(app.session, r.id); // its park in full, to answer here
  const secs = sectioned(rows);
  return html`<screen title="Coding agents" subtitle=${home ? 'yours — running or waiting for you' : 'in ' + title(s.root)} style="list"
      refreshable @refresh=${() => { app.board.reset(); ctx.paint(); }}>
    ${s.err ? html`<section><notice tone="danger" text=${s.err}/></section>` : s.note ? html`<section><notice tone="ok" text=${s.note}/></section>` : nothing}
    ${secs.length ? repeat(secs, (x) => x.key, (x) => html`<section title=${`${x.title} (${x.rows.length})`}>
        ${repeat(x.rows, (r) => r.id, (r) => rowTpl(s, r, home))}
      </section>`)
    : html`<section><empty title=${app.board.loading(s.root) ? 'loading…' : emptyWords(home, false)}/></section>`}
    ${home ? nothing : (ext.dock(app.session.merged(s.root) || app.session.current()) || []).map((d) => d.tpl())}
  </screen>`;
}

function rowTpl(s, r, home) {
  const app = ctx.app;
  const c = r.card;
  const w = { owner: ownerOf(r.view, app.me), talk: app.rules.access(r.view).talk, name: c.name, access: r.view && r.view.access };
  const park = r.section === 'needs' ? parkTpl(r.b, r.run, c, w) : nothing;
  const sub = [home && r.root !== r.id ? 'in ' + title(r.root) : '', c.status, c.meta].filter(Boolean).join(' · ');
  const acts = w.talk && (c.can.stop || c.can.message || c.can.cancel);
  return html`<row title=${c.title} subtitle=${sub} detail=${`${c.mono} · ${c.state.word}`} icon=${(HARNESSES[c.provider] || {}).icon || 'agent'}
      tone=${TONE[c.state.tone] || nothing} nav @tap=${() => openRow(r)}>
    ${acts ? html`<actions>
      ${c.can.stop ? html`<button icon="stop" @tap=${act(s, () => app.harness.stop(c.id), `Stopped ${c.name}'s turn (#${c.id}).`)}>Stop</button>` : nothing}
      ${c.can.message ? html`<button icon="chat" @tap=${() => push({ kind: 'hmsg', id: c.id, name: c.name, parent: c.parent, key: c.state.key, text: '' })}>Message</button>` : nothing}
      ${c.can.cancel ? html`<button icon="xmark" role="destructive" confirm=${{ title: cancelWords(c), label: 'Cancel task', destructive: true }}
        @tap=${act(s, () => app.harness.cancel(c.id), `Canceled #${c.id}${c.parent ? ' — the agent is told' : ''}.`)}>Cancel task</button>` : nothing}
    </actions>` : nothing}
    ${park}
  </row>`;
}

// Message: straight to the coding agent (POST /runs/{id}/message; Send now interrupts first)
function messageScreen(s) {
  const app = ctx.app;
  const words = messageWords({ name: s.name, parent: s.parent, state: { key: s.key } });
  const send = (interrupt) => async () => {
    const text = String(s.text || '').trim();
    s.err = '';
    if (!text) { s.err = 'Write the message first.'; return ctx.paint(); }
    s.busy = true;
    ctx.paint();
    try {
      await app.harness.steer(s.id, text, { interrupt });
      s.busy = false;
      if (top() === s) ui.stack.pop();
      const b = top();
      if (b && b.kind === 'hboard') { b.note = words.sent(interrupt); b.err = ''; }
    } catch (e) { s.busy = false; s.err = (e && e.message) || String(e); }
    ctx.paint();
  };
  const when = s.key === 'working' ? 'it steers or waits for the running turn' : 'it is its next prompt';
  return html`<screen title=${`Message ${s.name}`} subtitle=${s.parent ? `#${s.id} — the agent that started it is told` : `#${s.id}`} style="form">
    <toolbar>
      <button icon="send" role="primary" ?busy=${!!s.busy} @tap=${send(false)}>Send</button>
      <button icon="bolt" ?disabled=${!!s.busy} @tap=${send(true)}>Send now</button>
    </toolbar>
    ${s.err ? html`<notice tone="danger" text=${s.err}/>` : nothing}
    <section footer=${`Send: ${when}. Send now interrupts its turn first.`}>
      <field kind="multiline" placeholder=${words.placeholder} value=${s.text || ''} @input=${(e) => { s.text = e.value; }}/>
    </section>
  </screen>`;
}
