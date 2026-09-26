// buttons — a release tile: buttons in every role and state (icon, disabled,
// busy, a confirmation, copy-on-tap), a toolbar holding a picker, a badge, a
// menu and a button, and swipe actions on asset rows. The script taps
// "Publish", whose request is still in flight when the tree is taken.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';

const api = async (path, opt) => {
  const r = await xbin.fetch(`/api/${xbin.self}${path}`, opt);
  if (!r.ok) throw new Error(`${path}: HTTP ${r.status}`);
  return r.json();
};

let rel = null;
let channel = 'stable';
let publishing = false;

const size = (b) => (b >= 1 << 20 ? `${(b / (1 << 20)).toFixed(1)} MB` : b >= 1024 ? `${Math.round(b / 1024)} KB` : `${b} B`);

async function load() { rel = await api('/releases/draft'); paint(); }
async function publish() {
  publishing = true; paint();
  try { await api('/releases/draft/publish', { method: 'POST', body: JSON.stringify({ channel }) }); await load(); }
  finally { publishing = false; paint(); }
}

const paint = () => render(!rel ? nothing : html`
  <screen title=${`Release ${rel.tag}`} subtitle=${`${rel.commits} commits since ${rel.previous}`} style="form">
    <toolbar>
      <picker style="menu" value=${channel} @change=${(e) => { channel = e.value; paint(); }}
              options=${[{ value: 'stable', label: 'Stable', icon: 'check' }, { value: 'beta', label: 'Beta', icon: 'sparkles' }]}/>
      <badge tone=${rel.state === 'draft' ? 'muted' : 'ok'}>${rel.state}</badge>
      <menu icon="ellipsis" label="More">
        <button icon="pencil" @tap=${() => {}}>Edit notes</button>
        <button icon="external" @tap=${() => {}}>Open on GitHub</button>
        <divider/>
        <button role="destructive" icon="trash" @tap=${() => {}}>Discard draft</button>
      </menu>
      <button icon="refresh" @tap=${load}>Reload</button>
    </toolbar>
    <section title="Ship" footer=${`Publishes to the ${channel} channel and notifies ${rel.watchers} watchers.`}>
      <button role="primary" icon="upload" ?busy=${publishing} ?disabled=${publishing || !rel.checksOk} @tap=${publish}>Publish release</button>
      <button role="secondary" icon="doc" @tap=${() => {}}>Save draft</button>
      <button role="plain" icon="eye" @tap=${() => {}}>Preview notes</button>
      <button role="destructive" icon="trash" confirm=${{ title: `Delete the ${rel.tag} draft?`, message: 'The tag and its uploaded assets are removed.', label: 'Delete draft', destructive: true }}
              @tap=${() => {}}>Delete draft</button>
    </section>
    <section title="Verify">
      <button icon="shield" disabled @tap=${() => {}}>Notarize (needs a signing key)</button>
      <button icon="copy" copy=${`git tag -s ${rel.tag} ${rel.commit}`}>Copy tag command</button>
      <button icon="terminal" confirm=${{ title: 'Re-run the checks?', message: 'Takes about 6 minutes.', label: 'Re-run' }} @tap=${() => {}}>Re-run checks</button>
    </section>
    <section title="Assets" badge=${String(rel.assets.length)} footer="Swipe an asset to download or delete it.">
      ${repeat(rel.assets, (a) => a.name, (a) => html`
        <row title=${a.name} mono="title" detail=${size(a.size)} icon="file">
          <actions>
            <button icon="download" @tap=${() => {}}>Download</button>
            <button role="destructive" icon="trash" @tap=${() => {}}>Delete</button>
          </actions>
        </row>`)}
    </section>
  </screen>`);

load();
