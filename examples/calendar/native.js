// examples/calendar/native.js — today's events as a native list with an add
// form, for the xbin app (docs/frontend-kit.md). Same calls as index.html: GET
// /events, POST /events, and a reload on this tile's own bus
// (`res:<self>/bus/events/…`), so an event added anywhere — here, on the web
// page, by another app — shows up at once.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi, jbody } from '/vendor/bx-kit.js';

let events = null, err = '';
const draft = { time: '', title: '' };

async function load() {
  try { events = (await selfApi('/events')).events; err = ''; } catch (e) { err = String(e.message ?? e); }
  paint();
}
async function add() {
  if (!draft.title.trim()) return;
  try {
    // the same day the page sends: its UTC date (kept identical on purpose)
    await selfApi('/events', jbody({ day: new Date().toISOString().slice(0, 10), time: draft.time, title: draft.title }, 'POST'));
    draft.time = ''; draft.title = '';   // the bus event reloads the list
  } catch (e) { err = String(e.message ?? e); }
  paint();
}
const set = (k) => (e) => { draft[k] = e.value; paint(); };
const paint = () => render(html`
  <screen title="Today" style="list">
    ${err ? html`<section><notice tone="danger" text=${err}/></section>` : nothing}
    <section title="today">
      ${events === null ? html`<progress label="loading…"/>`
        : events.length === 0 ? html`<empty title="nothing today"/>`
        : repeat(events, (e) => e.id, (e) => html`<row title=${e.title} detail=${e.time || '--:--'} mono="detail"/>`)}
    </section>
    <section title="new event">
      <field label="Time" placeholder="HH:MM" value=${draft.time} @input=${set('time')}/>
      <field label="Title" placeholder="new event" value=${draft.title} @input=${set('title')} @submit=${add}/>
      <button role="primary" ?disabled=${!draft.title.trim()} @tap=${add}>add</button>
    </section>
  </screen>`);

// own resources through xbin.self — never a hardcoded install path
xbin.bus.on(`res:${xbin.self}/bus/events/`, load);
paint();   // at once ("loading…"): the app wants a tree before the backend answers
load();
