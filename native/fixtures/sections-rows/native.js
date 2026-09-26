// sections-rows — a workspace server's settings screen: titled sections with
// badges and footers, collapsible sections, and rows in every shape (icon,
// subtitle, detail, badge, each tone, each mono choice, navigation, selected,
// disabled, swipe actions, and a row with content under it).
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let s = null;
let me = '';
let advancedCollapsed = false;
let focus = '';

const days = (sec) => `${Math.floor(sec / 86400)} d ${Math.floor((sec % 86400) / 3600)} h`;
const pct = (x) => `${Math.round(x * 100)}%`;
const healthTone = { ok: 'ok', warn: 'warn', fail: 'danger', off: 'muted', info: 'accent' };

async function load() {
  s = await selfApi('/status');
  me = s.me;
  paint();
}
async function remove(m) {
  await selfApi(`/members/${encodeURIComponent(m.email)}`, { method: 'DELETE' });
  await load();
}

const member = (m) => html`
  <row title=${m.name} subtitle=${m.email} detail=${m.role} icon=${m.role === 'owner' ? 'star' : 'person'}
       ?selected=${m.email === focus} ?disabled=${m.pending} badge=${m.pending ? 'invited' : nothing}
       tone=${m.pending ? 'muted' : nothing} @tap=${() => { focus = m.email; paint(); }}>
    ${m.email === me ? nothing : html`
      <actions>
        <button icon="key" @tap=${() => selfApi(`/members/${encodeURIComponent(m.email)}/admin`, { method: 'POST' }).then(load)}>Make admin</button>
        <button role="destructive" icon="trash" confirm=${{ title: `Remove ${m.name}?`, message: 'They lose access to every tile at once.', label: 'Remove', destructive: true }}
                @tap=${() => remove(m)}>Remove</button>
      </actions>`}
  </row>`;

const paint = () => render(!s ? html`<screen title="Server" style="list"><section><progress label="Loading…"/></section></screen>` : html`
  <screen title="Server" style="list">
    <section title="Instance" footer=${`Last restart ${s.restarted}.`}>
      <row title="Hostname" detail=${s.host} mono="detail" icon="server"/>
      <row title="Uptime" detail=${days(s.uptime)} icon="clock"/>
      <row title="Version" subtitle=${`${s.latest} is available`} detail=${s.version} icon="download"
           badge="update" tone="accent" nav @tap=${() => {}}/>
      <row title=${s.fingerprint} subtitle="host key (ed25519)" mono="title" icon="key"/>
      <row title="Data directory" subtitle=${s.dataDir} mono="subtitle" icon="folder"/>
      <row title="build" subtitle=${s.goVersion} detail=${s.commit} mono="all" icon="code"/>
    </section>
    <section title="Health" badge=${String(s.health.filter((h) => h.state !== 'ok').length)}>
      ${repeat(s.health, (h) => h.name, (h) => html`
        <row title=${h.name} subtitle=${h.note} icon=${h.icon} badge=${h.label} tone=${healthTone[h.state]}/>`)}
    </section>
    <section title="Storage" footer="Snapshots are pruned after 30 days.">
      <row title="Disk" detail=${`${s.disk.used} of ${s.disk.size}`} icon="database">
        <progress value=${s.disk.ratio} label=${`${pct(s.disk.ratio)} used`}/>
      </row>
      <row title="Snapshots" detail=${String(s.snapshots)} icon="archive" nav @tap=${() => {}}/>
    </section>
    <section title="Members" badge=${String(s.members.length)} footer="Swipe a member for actions.">
      ${repeat(s.members, (m) => m.email, member)}
    </section>
    <section title="Advanced" collapsible collapsed=${advancedCollapsed}
             @toggle=${(e) => { advancedCollapsed = e.collapsed; paint(); }}>
      <row title="Debug logging" detail="off"/>
      <row title="Sandbox" detail="gVisor"/>
    </section>
    <section title="Danger zone" collapsible>
      <row title="Reset workspace" subtitle="Deletes every tile and home directory" icon="warning" tone="danger" nav @tap=${() => {}}/>
    </section>
  </screen>`);

load();
