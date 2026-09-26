// split-single — a contacts tile that prefers one column (split
// prefer="single": the detail is pushed over the list even on wide screens).
// The script opens a contact.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';

let people = null;
let open = null;

async function load() {
  people = (await (await xbin.fetch(`/api/${xbin.self}/people`)).json()).people;
  paint();
}

const byLetter = () => {
  const g = new Map();
  for (const p of people) { const k = p.name[0].toUpperCase(); if (!g.has(k)) g.set(k, []); g.get(k).push(p); }
  return [...g.entries()];
};

const detail = (p) => html`
  <screen title=${p.name} subtitle=${p.title} style="form">
    <section>
      <row title="Email" detail=${p.email} icon="mail" @tap=${() => {}}/>
      <row title="Phone" detail=${p.phone} icon="chat" mono="detail"/>
      <row title="Time zone" detail=${p.tz} icon="clock"/>
    </section>
    <section>
      <button icon="copy" copy=${p.email}>Copy email</button>
    </section>
  </screen>`;

const paint = () => render(!people ? nothing : html`
  <split prefer="single">
    <screen title="People" style="list">
      ${repeat(byLetter(), ([k]) => k, ([k, list]) => html`
        <section title=${k}>
          ${repeat(list, (p) => p.email, (p) => html`<row title=${p.name} subtitle=${p.title} icon="person" nav ?selected=${open === p.email}
            @tap=${() => { open = p.email; paint(); }}/>`)}
        </section>`)}
    </screen>
    ${open ? detail(people.find((p) => p.email === open)) : html`<screen title="People"><empty icon="people" title="Pick someone"/></screen>`}
  </split>`);

load();
