// sheet-open — a team tile whose render is a fragment: the navigation stack
// plus two sheets laid over it. The script taps "Invite" and types an
// address, so the invite sheet (medium and large detents, its own toolbar)
// is open over the list; the other sheet stays closed.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let team = null;
let sheet = '';
let editing = null;
const invite = { email: '', role: 'member', message: true };
let sending = false;

async function load() { team = await selfApi('/team'); paint(); }
const close = () => { sheet = ''; paint(); };
const validEmail = () => /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(invite.email);

const inviteSheet = () => html`
  <sheet open=${sheet === 'invite'} title="Invite teammate" detents=${['medium', 'large']} @dismiss=${close}>
    <toolbar>
      <button role="plain" @tap=${close}>Cancel</button>
      <button role="primary" icon="send" ?disabled=${!validEmail()} ?busy=${sending} @tap=${() => {}}>Send</button>
    </toolbar>
    <field kind="email" label="Email" placeholder="name@company.com" value=${invite.email} submit="send"
           error=${invite.email && !validEmail() ? 'Not an email address yet' : ''} @input=${(e) => { invite.email = e.value; paint(); }}/>
    <picker label="Role" style="segmented" value=${invite.role} @change=${(e) => { invite.role = e.value; paint(); }}
            options=${[{ value: 'member', label: 'Member' }, { value: 'admin', label: 'Admin' }]}/>
    <toggle label="Send a welcome message" value=${invite.message} @change=${(e) => { invite.message = e.value; paint(); }}/>
    <notice tone="info" text=${`${team.seatsLeft} of ${team.seats} seats left on the ${team.plan} plan.`}/>
  </sheet>`;

const roleSheet = () => html`
  <sheet open=${sheet === 'role'} title=${editing ? `Role of ${editing.name}` : 'Role'} detents="large" @dismiss=${close}>
    ${editing ? html`<picker label="Role" style="inline" value=${editing.role}
      options=${[{ value: 'member', label: 'Member' }, { value: 'admin', label: 'Admin' }, { value: 'owner', label: 'Owner' }]}/>` : nothing}
  </sheet>`;

const paint = () => render(!team ? nothing : html`
  <nav>
    <screen title="Team" subtitle=${team.name} style="list">
      <toolbar><button icon="plus" @tap=${() => { sheet = 'invite'; paint(); }}>Invite</button></toolbar>
      <section title="Members" badge=${String(team.members.length)}>
        ${repeat(team.members, (m) => m.email, (m) => html`
          <row title=${m.name} subtitle=${m.email} detail=${m.role} icon="person" nav
               @tap=${() => { editing = m; sheet = 'role'; paint(); }}/>`)}
      </section>
    </screen>
  </nav>
  ${inviteSheet()}
  ${roleSheet()}`);

load();
