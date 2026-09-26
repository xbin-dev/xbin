// native.js — devbox for the xbin app (docs/frontend-kit.md). Same calls as
// index.html: GET /state (polled every 5 s while on screen), POST /containers,
// POST /containers/{name}/start|stop, DELETE /containers/{name}, POST /keys,
// DELETE /keys/{fingerprint}. Containers and SSH keys are two tabs; a
// container's row swipes to start/stop/remove and opens a detail screen with
// its SSH command; the two create forms are sheets behind the toolbar's Add.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi, jbody } from '/vendor/bx-kit.js';

const self = xbin.self;
let s = null, err = '', tab = 'containers', open = null, sheet = '';
const draft = { name: '', image: '', key: '' };

async function refresh() {
  try { s = await selfApi('/state'); err = ''; } catch (e) { err = String(e.message ?? e); }
  paint();
}
async function act(path, method, body) {
  try { await selfApi(path, body ? jbody(body, method) : { method }); await refresh(); return true; }
  catch (e) { err = String(e.message ?? e); paint(); return false; }
}
const create = async () => { if (await act('/containers', 'POST', { name: draft.name.trim(), image: draft.image.trim() })) { draft.name = draft.image = ''; sheet = ''; paint(); } };
const addKey = async () => { if (await act('/keys', 'POST', { key: draft.key.trim() })) { draft.key = ''; sheet = ''; paint(); } };
const running = (c) => c.state === 'running';
const sshCmd = (c) => `ssh -p<host-port> ${c.name}@<xbin-host>`;
const set = (k) => (e) => { draft[k] = e.value; paint(); };

const containerRow = (c) => html`
  <row title=${c.name} mono="title" nav icon="box" subtitle=${c.image}
       badge=${c.status || c.state} tone=${running(c) ? 'ok' : 'muted'} @tap=${() => { open = c.name; paint(); }}>
    <actions>
      <button icon=${running(c) ? 'stop' : 'play'} @tap=${() => act(`/containers/${encodeURIComponent(c.name)}/${running(c) ? 'stop' : 'start'}`, 'POST')}>${running(c) ? 'stop' : 'start'}</button>
      <button role="destructive" icon="trash" confirm=${{ title: `Remove container ${c.name}?`, message: 'Its filesystem is lost.', label: 'Remove', destructive: true }}
              @tap=${() => act(`/containers/${encodeURIComponent(c.name)}`, 'DELETE')}>remove</button>
    </actions>
  </row>`;
const main = () => html`
  <screen title="Devbox" style="list" refreshable @refresh=${refresh}>
    <toolbar><button icon="plus" @tap=${() => { sheet = tab === 'keys' ? 'key' : 'container'; paint(); }}>Add</button></toolbar>
    ${err ? html`<section><notice tone="danger" text=${err}/></section>`
      : s?.error ? html`<section><notice tone="warn" text=${s.error}/></section>` : nothing}
    <tabs selected=${tab} @change=${(e) => { tab = e.key; paint(); }}>
      <tab key="containers" title="Containers" icon="box">
        <section footer=${s?.podmanVersion ? `podman ${s.podmanVersion} · storage persisted` : ''}>
          ${!s ? html`<progress label="loading…"/>` : s.containers?.length ? repeat(s.containers, (c) => c.name, containerRow) : html`<empty text="no containers yet"/>`}
        </section>
        <section title="SSH">
          ${!s ? nothing : s.sshError ? html`<notice tone="danger" text=${`ssh proxy: ${s.sshError}`}/>`
            : html`<text tone="muted">Publish the SSH port to a host port, then connect:</text>
                   <code copy text=${`bx expose ${self} ssh=runtime --listen ${s.sshPort || 2222}`}/>`}
        </section>
      </tab>
      <tab key="keys" title="SSH keys" icon="key">
        <section footer="The SSH proxy is default-deny — nobody can connect until you add a public key here.">
          ${!s ? html`<progress label="loading…"/>` : s.keys?.length ? repeat(s.keys, (k) => k.fingerprint, (k) => html`
              <row title=${k.comment || k.type} subtitle=${k.fingerprint} mono="subtitle" detail=${k.type}>
                <actions><button role="destructive" @tap=${() => act(`/keys/${encodeURIComponent(k.fingerprint)}`, 'DELETE')}>remove</button></actions>
              </row>`) : html`<empty text="no keys — SSH is closed"/>`}
        </section>
      </tab>
    </tabs>
  </screen>`;
const detail = (c) => html`
  <screen title=${c.name} style="form">
    <section>
      <row title="Image" detail=${c.image} mono="detail"/>
      <row title="State" badge=${c.status || c.state} tone=${running(c) ? 'ok' : 'muted'}/>
    </section>
    <section title="Connect"><code copy text=${sshCmd(c)}/></section>
  </screen>`;
const sheets = () => html`
  <sheet open=${sheet === 'container'} title="New container" @dismiss=${() => { sheet = ''; paint(); }}>
    <field label="Name (→ ssh user)" value=${draft.name} @input=${set('name')}/>
    <field label="Image" placeholder="docker.io/library/ubuntu:24.04" value=${draft.image} @input=${set('image')}/>
    <button role="primary" ?disabled=${!draft.name.trim() || !draft.image.trim()} @tap=${create}>create</button>
  </sheet>
  <sheet open=${sheet === 'key'} title="Add SSH key" @dismiss=${() => { sheet = ''; paint(); }}>
    <field kind="multiline" label="Public key" placeholder="ssh-ed25519 AAAA… you@host" value=${draft.key} @input=${set('key')}/>
    <button role="primary" ?disabled=${!draft.key.trim()} @tap=${addKey}>add key</button>
  </sheet>`;
const paint = () => render(html`
  <nav @pop=${() => { open = null; paint(); }}>
    ${main()}
    ${open && s?.containers?.find((c) => c.name === open) ? detail(s.containers.find((c) => c.name === open)) : nothing}
  </nav>
  ${sheets()}`);

paint();   // at once ("loading…"): the app wants a tree before the backend answers
refresh();
setInterval(() => { if (document.visibilityState === 'visible') refresh(); }, 5000);
