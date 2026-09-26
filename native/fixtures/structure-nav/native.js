// structure-nav — a deployments tile: a navigation stack whose root screen
// lists services (large title, pull to refresh, search, a toolbar with a
// menu) and a pushed detail screen (a form with a disclosure and actions).
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let services = [];
let query = '';
let sort = 'recent';
let open = null; // the service whose detail screen is pushed
let detail = null;
let envOpen = false;
let busy = '';
let error = '';

const ago = (iso) => {
  const s = Math.max(0, Math.round((Date.now() - Date.parse(iso)) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return `${Math.round(s / 86400)} d ago`;
};
const tone = (st) => ({ healthy: 'ok', degraded: 'warn', failed: 'danger', paused: 'muted' })[st] ?? 'muted';

async function load() {
  try { services = (await selfApi('/services')).services; error = ''; } catch (e) { error = e.message; }
  paint();
}
async function show(name) {
  open = name; detail = null; envOpen = false; paint();
  detail = await selfApi(`/services/${encodeURIComponent(name)}`);
  paint();
}
async function act(what) {
  busy = what; paint();
  try { await selfApi(`/services/${encodeURIComponent(open)}/${what}`, { method: 'POST' }); detail = await selfApi(`/services/${encodeURIComponent(open)}`); }
  finally { busy = ''; paint(); }
}

const visible = (env) => services
  .filter((s) => s.env === env && (!query || s.name.includes(query.toLowerCase())))
  .sort((a, b) => (sort === 'name' ? a.name.localeCompare(b.name) : Date.parse(b.deployed) - Date.parse(a.deployed)));

const serviceRow = (s) => html`
  <row title=${s.name} subtitle=${`${s.image}:${s.tag}`} detail=${ago(s.deployed)} icon="server"
       badge=${s.status} tone=${tone(s.status)} nav @tap=${() => show(s.name)}/>`;

const envSection = (env, title) => {
  const list = visible(env);
  return list.length ? html`
    <section title=${title} badge=${String(list.length)}>
      ${repeat(list, (s) => s.name, serviceRow)}
    </section>` : nothing;
};

const root = () => html`
  <screen title="Deployments" subtitle="acme-prod · eu-central" style="list" large refreshable search=${query}
          @refresh=${load} @search=${(e) => { query = e.value; paint(); }}>
    <toolbar>
      <menu label="Sort" icon="filter">
        <button icon="clock" @tap=${() => { sort = 'recent'; paint(); }}>Recently deployed</button>
        <button icon="list" @tap=${() => { sort = 'name'; paint(); }}>Name</button>
        <divider/>
        <button icon="refresh" @tap=${load}>Reload</button>
      </menu>
      <button icon="plus" @tap=${() => {}}>Deploy</button>
    </toolbar>
    ${error ? html`<section><notice tone="danger" title="Could not load services" text=${error}/></section>` : nothing}
    ${envSection('prod', 'Production')}
    ${envSection('staging', 'Staging')}
  </screen>`;

const detailScreen = (s) => html`
  <screen title=${s.name} subtitle=${`${s.image}:${s.tag}`} style="form" @appear=${() => {}}>
    <section title="Rollout">
      <row title="Status" badge=${s.status} tone=${tone(s.status)}/>
      <row title="Replicas" detail=${`${s.ready} / ${s.replicas} ready`}/>
      <row title="Region" detail=${s.region} icon="globe"/>
      <row title="Deployed" detail=${ago(s.deployed)} subtitle=${s.by} icon="clock"/>
      <row title="Commit" detail=${s.commit} mono="detail" icon="branch"/>
    </section>
    <section>
      <disclosure title=${`Environment (${Object.keys(s.env_vars).length})`} open=${envOpen}
                  @toggle=${(e) => { envOpen = e.open; paint(); }}>
        ${repeat(Object.entries(s.env_vars), ([k]) => k, ([k, v]) => html`<row title=${k} detail=${v} mono="all"/>`)}
      </disclosure>
    </section>
    <section title="Redeploy" footer="Runs on the tile's backend; the rollout is visible in the list.">
      <code copy>${`bx deploy ${s.name} --image ${s.image}:${s.tag}`}</code>
      <button role="primary" icon="refresh" ?busy=${busy === 'restart'} @tap=${() => act('restart')}>Restart</button>
      <button role="destructive" icon="back" confirm=${{ title: `Roll back ${s.name}?`, message: `Returns to ${s.previous}.`, label: 'Roll back', destructive: true }}
              ?busy=${busy === 'rollback'} @tap=${() => act('rollback')}>Roll back</button>
    </section>
  </screen>`;

const loading = (name) => html`<screen title=${name} style="form"><section><progress label="Loading…"/></section></screen>`;

const paint = () => render(html`
  <nav @pop=${() => { open = null; detail = null; paint(); }}>
    ${root()}
    ${open ? (detail ? detailScreen(detail) : loading(open)) : nothing}
  </nav>`);

load();
