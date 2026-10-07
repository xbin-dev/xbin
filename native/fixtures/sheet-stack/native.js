// sheet-stack — a team blog's composer on vocabulary rev 2: New post opens a
// full-screen sheet (`detents="full"`: no swipe to dismiss, so it has Cancel)
// and Add link opens a second sheet over it (a `sheet` inside a `sheet` is
// presented on top of it); closing the inner one leaves the outer in place.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let posts = null;
let composing = false;
let linking = false;
let draft = { title: '', body: '' };
let link = { text: '', url: '' };

async function load() { posts = (await selfApi('/posts')).posts; paint(); }

const linkSheet = () => html`
  <sheet open=${linking} title="Add link" detents=${['medium']} @dismiss=${() => { linking = false; paint(); }}>
    <toolbar>
      <button role="plain" @tap=${() => { linking = false; paint(); }}>Cancel</button>
      <button role="primary" ?disabled=${!/^https:\/\//.test(link.url)}
              @tap=${() => { draft.body += ` [${link.text || link.url}](${link.url})`; linking = false; paint(); }}>Add</button>
    </toolbar>
    <section>
      <field label="Text" value=${link.text} @input=${(e) => { link.text = e.value; paint(); }}/>
      <field label="Address" kind="url" placeholder="https://" value=${link.url} @input=${(e) => { link.url = e.value; paint(); }}/>
    </section>
  </sheet>`;

const composer = () => html`
  <sheet open=${composing} title="New post" detents="full" @dismiss=${() => { composing = false; paint(); }}>
    <toolbar>
      <button role="plain" @tap=${() => { composing = false; paint(); }}>Cancel</button>
      <button role="primary" ?disabled=${!draft.title} @tap=${() => {}}>Publish</button>
    </toolbar>
    <section>
      <field label="Title" value=${draft.title} @input=${(e) => { draft.title = e.value; paint(); }}/>
      <field label="Post" kind="multiline" value=${draft.body} @input=${(e) => { draft.body = e.value; paint(); }}/>
    </section>
    <section>
      <button icon="link" @tap=${() => { linking = true; link = { text: '', url: '' }; paint(); }}>Add link</button>
      <button icon="photo" @tap=${() => {}}>Add image</button>
    </section>
    ${linkSheet()}
  </sheet>`;

const paint = () => render(!posts ? nothing : html`
  <screen title="Team blog" style="list">
    <toolbar><button icon="pencil" @tap=${() => { composing = true; paint(); }}>New post</button></toolbar>
    <section title="Published">
      ${repeat(posts, (p) => p.id, (p) => html`<row title=${p.title} subtitle=${p.author} detail=${p.date} nav @tap=${() => {}}/>`)}
    </section>
  </screen>
  ${composer()}`);

load();
