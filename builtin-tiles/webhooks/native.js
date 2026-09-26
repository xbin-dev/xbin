// native.js — the webhooks tile for the xbin app (docs/frontend-kit.md). Same
// calls as index.html (GET/POST /hooks, PUT and DELETE /hooks/{id}, POST
// /hooks/{id}/rotate); the page's cards become a list with a detail screen per
// hook — the same actions, arranged for a phone — and the reveal-once secret
// becomes a sheet. Rotating or removing asks first (the button's confirm), as
// the page's confirm() did.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi as api, jbody } from '/vendor/bx-kit.js';

const self = xbin.self;
let st = null, err = '', shown = null, open = null;    // shown: {id, secret, auth} this once; open: the detail screen's hook id
const draft = { name: '', auth: 'token', preset: '', agent: '' };
const PRESETS = {
  '': { eventIdFrom: '', topicFrom: '' },
  github: { auth: 'hmac', eventIdFrom: 'header:X-GitHub-Delivery', topicFrom: 'header:X-GitHub-Event' },
  gitlab: { auth: 'token', eventIdFrom: 'header:X-Gitlab-Event-UUID', topicFrom: 'json:object_kind' },
};
async function load() { try { st = await api('/hooks'); err = ''; } catch (e) { err = e.message; } paint(); }
async function act(fn) { err = ''; try { await fn(); } catch (e) { err = e.message; } await load(); }
function create() {
  const p = PRESETS[draft.preset] || {};
  const body = { name: draft.name.trim(), auth: p.auth || draft.auth, agent: draft.agent, eventIdFrom: p.eventIdFrom, topicFrom: p.topicFrom };
  return act(async () => { const r = await api('/hooks', jbody(body, 'POST')); shown = { id: r.hook.id, secret: r.secret, auth: r.hook.auth }; draft.name = ''; });
}
const toggle = (h) => act(() => api(`/hooks/${h.id}`, jbody({ enabled: !h.enabled }, 'PUT')));
const rotate = (h) => act(async () => { const r = await api(`/hooks/${h.id}/rotate`, { method: 'POST' }); shown = { id: h.id, secret: r.secret, auth: h.auth }; });
const del = (h) => act(async () => { await api(`/hooks/${h.id}`, { method: 'DELETE' }); open = null; });
const hookURL = (h) => `${st.host ? `https://${st.host}` : 'https://<your hooks host>'}/hook/${h.id}`;
const ago = (s) => { const d = Math.max(0, Date.now() / 1000 - s); return d < 90 ? 'just now' : d < 5400 ? `${Math.round(d / 60)} min ago` : `${Math.round(d / 3600)} h ago`; };
const tone = (status) => ({ 2: 'ok', 4: 'warn', 5: 'danger' })[String(status)[0]] || 'muted';
const set = (k) => (e) => { draft[k] = e.value; paint(); };
const hookOf = (id) => st?.hooks?.find((x) => x.id === id);
const about = (h) => `${h.auth === 'hmac' ? 'signed (HMAC)' : 'token'}${h.agent ? ` · to ${h.agent}` : ''}${h.topicFrom ? ` · topic ${h.name}/‹${h.topicFrom}›` : ''}`;

const main = () => html`
  <screen title="Webhooks" style="list">
    ${err ? html`<section><notice tone="danger" text=${err}/></section>` : nothing}
    <section title="Wiring">
      ${st.agents?.length
        ? html`<row title="Pushes to" detail=${st.agents.map((a) => a.provider).join(', ')}/>`
        : html`<notice tone="warn" text="Not bound to an agent yet"/><code copy text=${`bx bind ${self} agents=apps/agent`}/>`}
      ${st.host ? html`<row title="Public at" detail=${st.host} mono="detail"/>`
        : html`<code copy text=${`bx expose ${self} hooks=apps/traefik --host hooks.example.com`}/>`}
    </section>
    <section title="Hooks">
      ${st.hooks?.length ? repeat(st.hooks, (h) => h.id, (h) => html`
          <row title=${h.name} nav icon="link" badge=${h.enabled ? '' : 'off'} subtitle=${about(h)}
               @tap=${() => { open = h.id; paint(); }}/>`)
        : html`<empty text="none yet"/>`}
    </section>
    <section title="New hook">
      <field label="Name (its events' topic)" placeholder="deploy" value=${draft.name} @input=${set('name')}/>
      <picker label="Sent by" value=${draft.preset} @change=${set('preset')}
              options=${[{ value: '', label: 'anything (a token)' }, { value: 'github', label: 'GitHub (signed)' }, { value: 'gitlab', label: 'GitLab (a token)' }]}/>
      ${draft.preset === '' ? html`<picker label="Checked by" value=${draft.auth} @change=${set('auth')}
              options=${[{ value: 'token', label: 'a token' }, { value: 'hmac', label: 'an HMAC signature (X-Hub-Signature-256)' }]}/>` : nothing}
      ${(st.agents?.length ?? 0) > 1 ? html`<picker label="To" value=${draft.agent} @change=${set('agent')}
              options=${[{ value: '', label: 'every bound agent' }, ...st.agents.map((a) => ({ value: a.provider, label: a.provider }))]}/>` : nothing}
      <button role="primary" ?disabled=${!draft.name.trim()} @tap=${create}>Create</button>
    </section>
    <section title="Recent deliveries">
      ${st.deliveries?.length ? repeat(st.deliveries.slice().reverse(), (x) => `${x.at}:${x.hook}`, (x) => html`
          <row title=${x.topic || x.hook} mono="title" subtitle=${x.result} detail=${ago(x.at)}
               badge=${String(x.status)} tone=${tone(x.status)}/>`)
        : html`<empty text="nothing yet"/>`}
    </section>
  </screen>`;
const detail = (h) => html`
  <screen title=${h.name} style="form">
    <section footer=${about(h)}>
      <toggle label="Enabled" value=${h.enabled} @change=${() => toggle(h)}/>
      <code copy text=${hookURL(h)}/>
    </section>
    <section>
      <button icon="key" confirm=${{ title: 'Make a new secret?', message: 'The old one stops working at once.', label: 'New secret' }}
              @tap=${() => rotate(h)}>New secret</button>
      <button role="destructive" icon="trash" confirm=${{ title: `Remove the hook "${h.name}"?`, message: 'Its URL stops working.', label: 'Remove', destructive: true }}
              @tap=${() => del(h)}>Remove</button>
    </section>
  </screen>`;
const secret = () => html`
  <sheet open=${!!shown} title="Copy this secret now" @dismiss=${() => { shown = null; paint(); }}>
    <text>It is not shown again.</text>
    <code copy text=${shown?.secret ?? ''}/>
    ${shown?.auth === 'token' ? html`
        <text tone="muted">Send it as ?token=… or Authorization: Bearer …</text>
        <code copy text=${hookOf(shown.id) ? `${hookURL(hookOf(shown.id))}?token=${shown.secret}` : ''}/>`
      : html`<text tone="muted">Use it as the webhook secret: the sender signs each body with it (GitHub: Secret).</text>`}
    <button role="primary" @tap=${() => { shown = null; paint(); }}>Done</button>
  </sheet>`;
const paint = () => render(st ? html`
  <nav @pop=${() => { open = null; paint(); }}>
    ${main()}
    ${open && hookOf(open) ? detail(hookOf(open)) : nothing}
  </nav>
  ${secret()}` : html`<screen title="Webhooks">${err ? html`<notice tone="danger" text=${err}/>` : html`<progress label="loading…"/>`}</screen>`);
paint();   // at once ("loading…"): the app wants a tree before the backend answers
load();
