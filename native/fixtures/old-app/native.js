// old-app — a tile written for vocabulary rev 2 that does not check
// xbin.native.supports(), opened by an app that renders rev 1 (the caps in
// data.json: every primitive at revision 1, as the app released before D189
// sends them). The runtime still builds the tree, but reports each rev-2
// prop, event, enum value and child it meets as `unsupported` — and the app
// shows the tile's web page instead (tree.md §8, docs/native.md "Older
// apps"). A tile that wants its native view on such an app branches on
// supports('split', 2) and renders the rev-1 way; this one says which it got.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let builds = null;
let open = null;

async function load() { builds = (await selfApi('/builds')).builds; open = builds[0].id; paint(); }

const paint = () => render(!builds ? nothing : html`
  <split detail=${open !== null} columns="auto" @close=${() => { open = null; paint(); }}>
    <screen title="Builds" subtitle=${xbin.native.supports('split', 2) ? 'rev 2 app' : 'rev 1 app'} style="list" refreshable ?refreshing=${false} @refresh=${load}>
      <toolbar place="bottom">
        <menu icon="filter" label="Show">
          <button @tap=${() => {}}>All branches</button>
          <menu label="Branch">
            <button @tap=${() => {}}>main</button>
            <button @tap=${() => {}}>release/2.4</button>
          </menu>
        </menu>
      </toolbar>
      <list>
        ${repeat(builds, (b) => b.id, (b) => html`<row title=${b.title} subtitle=${b.branch} detail=${b.took}
            tone=${b.ok ? 'ok' : 'danger'} ?selected=${b.id === open} @tap=${() => { open = b.id; paint(); }}/>`)}
      </list>
    </screen>
    <screen title=${builds.find((b) => b.id === open)?.title ?? ''}>
      <text>${builds.find((b) => b.id === open)?.summary ?? ''}</text>
    </screen>
  </split>`);

load();
