// chat-transcript — an agent conversation mid-turn: history with user,
// assistant and system messages (sender, time, files, actions), finished and
// live thinking, steps in every tone, a live activity line, a date divider,
// an inline image and markdown, and the composer (attachments, slash
// commands, chips). The script sends a message: the reply streams over an
// event stream that is still open when the tree is taken, and a second
// message typed meanwhile waits as queued.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';

const base = `/api/${xbin.self}/conversations/c-812`;
let conv = null;
let draft = '';
let attachments = [];
let busy = false;
let queued = [];
let live = null; // the streaming turn: {thinking, text, activity, started}

const hhmm = (t) => new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' }).format(new Date(t));

async function load() {
  conv = await (await xbin.fetch(base)).json();
  paint();
}

async function send(text) {
  if (busy) { queued = [...queued, { id: `q${queued.length + 1}`, text }]; paint(); return; }
  const files = attachments.map((a) => ({ name: a.name, mime: a.mime, src: a.src }));
  conv.messages = [...conv.messages, { id: `m${conv.messages.length + 1}`, role: 'user', sender: conv.me, text, files, at: Date.now() }];
  attachments = [];
  busy = true;
  live = { thinking: '', text: '', activity: 'Starting…', started: Date.now() };
  paint();
  const r = await xbin.fetch(`${base}/turns`, { method: 'POST', body: JSON.stringify({ text, files }) });
  const rd = r.body.pipeThrough(new TextDecoderStream()).getReader();
  let buf = '';
  for (;;) {
    const { value, done } = await rd.read();
    if (done) break;
    buf += value;
    for (let i; (i = buf.indexOf('\n\n')) >= 0;) {
      const frame = buf.slice(0, i); buf = buf.slice(i + 2);
      const ev = (frame.match(/^event: (.*)$/m) || [])[1] || 'message';
      const data = JSON.parse((frame.match(/^data: (.*)$/m) || [])[1] || 'null');
      if (ev === 'thinking') live.thinking += data.delta;
      else if (ev === 'activity') live.activity = data.text;
      else if (ev === 'delta') live.text += data.delta;
      paint();
    }
  }
  busy = false; live = null;
  await load();
}

const userMsg = (m) => html`
  <message role="user" sender=${m.sender} text=${m.text} time=${hhmm(m.at)} files=${m.files?.length ? m.files : nothing}
           @tap=${() => {}}/>`;
const assistantMsg = (m) => html`
  ${m.thinking ? html`<thinking text=${m.thinking.text} seconds=${m.thinking.seconds} open=${false}/>` : nothing}
  <message role="assistant" sender=${conv.agent} markdown text=${m.text} time=${hhmm(m.at)} @link=${(e) => xbin.native.open(e.href)}>
    <actions>
      <button icon="copy" copy=${m.text}>Copy</button>
      <button icon="refresh" @tap=${() => {}}>Retry</button>
    </actions>
  </message>`;
const item = (m) => {
  switch (m.role) {
    case 'user': return userMsg(m);
    case 'assistant': return assistantMsg(m);
    case 'system': return html`<message role="system" text=${m.text}/>`;
    case 'step': return html`<step glyph=${m.glyph} tone=${m.tone} text=${m.text}/>`;
    case 'day': return html`<text style="caption" tone="muted">${m.text}</text>`;
    case 'image': return html`<image src=${m.src} alt=${m.alt} aspect="fit" height="m" preview/>`;
    case 'summary': return html`<markdown source=${m.text}/>`;
    default: return nothing;
  }
};

const paint = () => render(!conv ? nothing : html`
  <screen title=${conv.title} subtitle=${`${conv.agent} · ${conv.model}`} style="scroll">
    <transcript follow older @more=${() => {}} @scrolled=${() => {}}>
      <notice tone="info" text=${`Shared with ${conv.sharedWith.join(', ')}`}/>
      ${repeat(conv.messages, (m) => m.id, item)}
      ${live ? html`
        <thinking text=${live.thinking} live seconds=${Math.round((Date.now() - live.started) / 1000)}/>
        ${live.text ? html`<message role="assistant" sender=${conv.agent} markdown streaming text=${live.text}/>` : nothing}
        <activity live text=${live.activity}/>` : nothing}
      ${repeat(queued, (q) => q.id, (q) => html`<message role="user" sender=${conv.me} text=${q.text} queued/>`)}
    </transcript>
    <composer value=${draft} placeholder=${`Message ${conv.agent}`} ?busy=${busy} accept="image/*,.pdf,.txt,.csv"
              upload=${{ method: 'POST', path: `api/${xbin.self}/uploads` }}
              attachments=${attachments.map(({ id, name, mime, progress }) => ({ id, name, mime, progress }))}
              slash=${conv.commands}
              @input=${(e) => { draft = e.value; paint(); }}
              @send=${(e) => { draft = ''; send(e.value); }}
              @stop=${() => {}}
              @uploaded=${(e) => { attachments = [...attachments, { id: e.response.id, name: e.name, mime: e.response.mime, progress: 1, src: e.response.src }]; paint(); }}
              @remove=${(e) => { attachments = attachments.filter((a) => a.id !== e.id); paint(); }}>
      <button icon="sparkles" @tap=${() => { draft = 'Summarize this conversation'; paint(); }}>Summarize</button>
      <button icon="list" @tap=${() => { draft = 'Make a checklist of the next steps'; paint(); }}>Next steps</button>
    </composer>
  </screen>`);

load();
