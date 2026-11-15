// workspace-agent — a person's workspace agent mid-request (D233): the line
// that says what the session is connected to (`connection`), a numbered
// `plan` with a tag a step, the agent's own cards (`card`: the editable
// brief, a refusal, a redirect, a "can't", a report, a failure), the
// approval requests and tile agents it names BY ID (`request`, `session` —
// the app draws those from xbind's own reading; this tile says which, and
// nothing about them), a settled question and a pending one as choice rows
// with "Something else" over two pages, and the composer with what the next
// message is about, quick replies and a hint.
//
// The script says go ahead (a quick reply), drops one context chip, taps the
// other (it is not connected: the scope sheet opens), asks for the model, and
// presses the brief's "Review and start…", which asks the app's chrome to
// show that request (xbin.native.agent).
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let v = null;         // the session's view (GET …/ws/view)
let draft = '';
let context = [];
let replies = [];
let meta = '';
let scope = '';       // '' closed · 'tiles' · 'model'
const brief = {};
const expanded = new Set(['sales']);

async function load() {
  v = await selfApi('/ws/view');
  context = v.context;
  replies = v.replies;
  Object.assign(brief, v.brief.fields, { harness: v.brief.harness, tell: true });
  paint();
}

// "Review and start…": the brief's start is a request xbind holds; the app's
// own chrome shows it, by id (xbin.native.agent — only where the app shows
// this tile as the person's workspace agent). Nothing here can approve it.
let asked = '';
async function review(id) {
  if (!xbin.native.supports('agent')) return;
  asked = (await xbin.native.agent({ type: 'xbin:agent-open-request', id })) ? id : '';
  paint();
}

const clock = (ms) => new Date(ms).toISOString().slice(11, 16);

function reply(e) {
  if (e.id !== 'go') return;
  meta = `you said go ahead · ${clock(Date.now())}`;
  replies = v.next;
  paint();
}

// a card of the agent's own, from its fields
function card(c) {
  switch (c.type) {
    case 'brief': return html`
      <card title=${c.title} subtitle=${c.runsOn} icon="doc" tone="accent" badge=${asked === c.request ? 'asked' : c.state} note=${c.note}>
        ${repeat(c.labels, (f) => f.key, (f) => html`<field label=${f.label} kind="multiline" value=${brief[f.key] ?? ''}
          @change=${(e) => { brief[f.key] = e.value; paint(); }}/>`)}
        <picker label="Coding agent" value=${brief.harness} options=${c.harnesses} @change=${(e) => { brief.harness = e.value; paint(); }}/>
        <toggle label="Tell me when it finishes" value=${brief.tell} @change=${(e) => { brief.tell = e.value; paint(); }}/>
        <button role="primary" @tap=${() => review(c.request)}>Review and start…</button>
      </card>`;
    case 'refusal': return html`
      <card title=${c.title} icon="lock" tone="warn" badge=${c.org}>
        <text>${c.text}</text>
        <notice tone="info" text=${c.note}/>
        <button @tap=${() => { scope = 'model'; paint(); }}>Change the model…</button>
        <button @tap=${() => { scope = 'tiles'; paint(); }}>Open scope</button>
      </card>`;
    case 'redirect': return html`
      <card title=${c.title} tone="muted">
        <text>${c.text}</text>
        ${repeat(c.ways, (w) => w.id, (w) => html`<row title=${w.title} subtitle=${w.text} nav @tap=${() => {}}/>`)}
      </card>`;
    case 'cannot': return html`
      <card title="Can’t do this" icon="info" tone="info">
        <text>${c.text}</text>
        <button role="plain" @tap=${() => {}}>What can it do?</button>
      </card>`;
    case 'report': return html`
      <card title=${c.title} icon="check" tone="ok" badge=${c.count}>
        <markdown source=${c.body}/>
        ${repeat(c.lines, (l, i) => i, (l) => html`<step glyph=${l.glyph} tone=${l.tone ?? nothing} text=${l.text}/>`)}
        <badge tone="muted">${c.took}</badge>
      </card>`;
    case 'failed': return html`
      <card title=${c.title} icon="error" tone="danger">
        <text>${c.text}</text>
        <button @tap=${() => {}}>Yes, start it again</button>
        <button role="plain" @tap=${() => {}}>No, leave it</button>
      </card>`;
    default: return nothing;
  }
}

function entry(x) {
  switch (x.type) {
    case 'user': return html`<message role="user" sender=${x.about ?? nothing} time=${x.time} text=${x.text}/>`;
    case 'assistant': return html`<message role="assistant" markdown text=${x.text}/>`;
    case 'plan': return html`<plan title="Plan" numbered meta=${meta || nothing} note=${meta ? nothing : x.note}
      entries=${x.steps.map((s) => ({ text: s.text, status: s.status }))} tags=${x.steps.map((s) => s.tag)}/>`;
    // by id: what waits, for which tile, and Review… are the app's, from xbind
    case 'request': return html`<request id=${x.id} kind=${x.kind}/>`;
    case 'session': return html`<session id=${x.id} tile=${x.tile}/>`;
    case 'question': return html`<question title=${x.title} schema=${x.schema} style=${x.style} other=${x.other ?? nothing}
      ?paged=${x.paged} submitLabel=${x.submit ?? nothing} skipLabel=${x.skip ?? nothing} settled=${x.settled ?? nothing}
      @submit=${() => {}} @skip=${() => {}}/>`;
    default: return card(x);
  }
}

// the scope sheet is this tile's own (scope is where the session looks, not a permission)
const BOX = { all: 'check-square', some: 'minus-square', none: 'square' };
const scopeSheet = () => html`
  <sheet open=${scope !== ''} title=${scope === 'model' ? 'Change this session’s model' : 'Connected to'} detents=${['medium', 'large']}
         @dismiss=${() => { scope = ''; paint(); }}>
    ${scope === 'model' ? html`
        <section title="Model" footer=${v.scope.modelNote}>
          ${repeat(v.scope.models, (m) => m.id, (m) => html`<row title=${m.name} subtitle=${m.where} badge=${m.private ? 'private' : nothing}
            tone=${m.private ? 'ok' : nothing} ?selected=${m.id === v.connection.model} @tap=${() => {}}/>`)}
        </section>` : html`
        <section>
          <row title="Connected" icon="plug" detail=${String(v.scope.counts.connected)}/>
          <row title="Blocked" icon="lock" tone="warn" detail=${String(v.scope.counts.blocked)}/>
        </section>
        ${repeat(v.scope.orgs, (o) => o.id, (o) => html`
          <section title=${o.name} badge=${`${o.on} of ${o.tiles.length}`} footer=${o.reason ?? nothing} collapsible
                   collapsed=${!expanded.has(o.id)} @toggle=${(e) => { if (e.collapsed) expanded.delete(o.id); else expanded.add(o.id); paint(); }}>
            <row title=${`All of ${o.name}`} icon=${BOX[o.on === o.tiles.length ? 'all' : o.on ? 'some' : 'none']} ?disabled=${!!o.reason} @tap=${() => {}}/>
            ${repeat(o.tiles, (t) => t.path, (t) => html`<row title=${t.name} detail=${t.state} icon=${BOX[t.state === 'connected' ? 'all' : 'none']}
              ?disabled=${t.state === 'blocked'} @tap=${() => {}}/>`)}
          </section>`)}
        <section footer="Scope is where this session looks. It is not a permission — blocked stays blocked whatever you tick.">
          <button role="primary" @tap=${() => { scope = ''; paint(); }}>Apply</button>
        </section>`}
  </sheet>`;

const paint = () => render(!v ? nothing : html`
  <screen title="Workspace agent" subtitle=${v.session} style="scroll">
    <connection connected=${v.connection.connected} not=${v.connection.not} model=${v.connection.model} label=${v.connection.label}
                @tap=${() => { scope = 'tiles'; paint(); }} @model=${() => { scope = 'model'; paint(); }}/>
    <transcript follow>${repeat(v.entries, (x) => x.key, entry)}</transcript>
    <composer value=${draft} placeholder="Reply, or ask for something else…" hint=${v.hint} context=${context} replies=${replies}
              @input=${(e) => { draft = e.value; paint(); }} @send=${() => { draft = ''; paint(); }}
              @context=${(e) => { if (context.find((c) => c.id === e.id)?.tone === 'warn') { scope = 'tiles'; paint(); } }}
              @remove-context=${(e) => { context = context.filter((c) => c.id !== e.id); paint(); }}
              @reply=${reply}/>
  </screen>
  ${scopeSheet()}`);

load();
