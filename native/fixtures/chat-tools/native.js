// chat-tools — a coding agent's turn with tools: a plan, tool cards in every
// state (writing, running, ok, error, canceled) with chips in every tone and
// each kind of body (code, text, streaming markdown, notice, image, a diff,
// subagents' nested transcripts), a diff with its files, approvals and
// questions (form schemas) both settled and pending, a progress line, and
// the composer disabled while the agent waits. The script opens three tool
// cards (one a subagent waiting on its own question) and answers the first
// approval, leaving the deploy approval pending.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';

const base = `/api/${xbin.self}/sessions/s-77`;
let s = null;
const open = new Set();
let thinkingOpen = true;

async function load() { s = await (await xbin.fetch(base)).json(); paint(); }
// answering an approval or a question: the server returns how it settled
async function answer(x, path, body) {
  const r = await xbin.fetch(`${base}${path}`, { method: 'POST', body: JSON.stringify(body) });
  x.settled = (await r.json()).settled;
  paint();
}
const toggle = (id) => (e) => { if (e.open) open.add(id); else open.delete(id); paint(); };

const body = (b) => {
  switch (b.kind) {
    case 'code': return html`<code wrap=${!!b.wrap}>${b.text}</code>`;
    case 'text': return html`<text tone=${b.tone ?? nothing}>${b.text}</text>`;
    case 'markdown': return html`<markdown source=${b.text} ?streaming=${b.streaming}/>`;
    case 'notice': return html`<notice tone=${b.tone} text=${b.text}/>`;
    case 'image': return html`<image src=${b.src} alt=${b.alt} aspect="fit" height="s"/>`;
    case 'diff': return html`<diff files=${b.files}/>`;
    case 'transcript': return html`<transcript>${repeat(b.items, (x) => x.id, entry)}</transcript>`;
    default: return nothing;
  }
};

function entry(x) {
  switch (x.type) {
    case 'user': return html`<message role="user" text=${x.text}/>`;
    case 'assistant': return html`<message role="assistant" markdown text=${x.text}/>`;
    case 'thinking': return html`<thinking text=${x.text} seconds=${x.seconds} open=${thinkingOpen} @toggle=${(e) => { thinkingOpen = e.open; paint(); }}/>`;
    case 'plan': return html`<plan entries=${x.entries}/>`;
    case 'tool': return html`
      <toolcard title=${x.title} icon=${x.icon} family=${x.family} state=${x.state} chips=${x.chips ?? nothing}
                open=${open.has(x.id)} @toggle=${toggle(x.id)} @open=${() => {}}>
        ${repeat(x.body ?? [], (b, i) => i, body)}
      </toolcard>`;
    case 'diff': return html`<diff files=${x.files} patch=${x.patch} @open-file=${() => {}}/>`;
    case 'approval': return html`
      <approval title=${x.title} text=${x.text} options=${x.options} note=${x.note ?? nothing} ?feedback=${x.feedback}
                settled=${x.settled ?? nothing} @choose=${(e) => answer(x, `/approvals/${x.id}`, { option: e.id, feedback: e.feedback ?? '' })}/>`;
    case 'question': return html`
      <question title=${x.title} schema=${x.schema} settled=${x.settled ?? nothing}
                @submit=${(e) => answer(x, `/questions/${x.id}`, { content: e.content })} @skip=${() => answer(x, `/questions/${x.id}`, { skip: true })}/>`;
    case 'progress': return html`<progress value=${x.value} label=${x.label}/>`;
    default: return nothing;
  }
}

const waiting = () => s.entries.some((x) => x.type === 'approval' && !x.settled);

const paint = () => render(!s ? nothing : html`
  <screen title=${s.title} subtitle=${s.cwd} style="scroll">
    <transcript follow>${repeat(s.entries, (x) => x.id, entry)}</transcript>
    <composer placeholder=${waiting() ? 'Answer the approval above to continue' : 'Message the agent'} ?disabled=${waiting()} value=""/>
  </screen>`);

load();
