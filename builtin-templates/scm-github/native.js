// native.js — scm-github in the xbin app (docs/native.md): your GitHub
// sign-in (the device flow in your own partition: /scm/signin*), the App's
// status from the global instance, and Forget. Setup and policy are the
// web page's (a manager's desk, not a phone's).
import { html, render, nothing } from '/vendor/xb-native.js';
import { api } from '/vendor/bx-kit.js';

const self = xbin.self;
const part = xbin.partition || '';
const mine = part.startsWith('user:');
const call = (path, opts = {}) => api(`/api/${self}${path}`, opts);
const callG = (path, opts = {}) => call(path, mine ? { ...opts, partition: 'global' } : opts);

let signin = null, gpage = null, err = '', note = '', busy = false, timer = null;

async function load() {
  try {
    if (mine) signin = await call('/scm/signin');
    gpage = await callG('/api/page');
    err = '';
    if (signin?.state === 'pending') poll(signin.signin);
  } catch (e) { err = e.message; }
  paint();
}

async function act(fn) {
  err = ''; note = ''; busy = true; paint();
  try { await fn(); } catch (e) { err = e.message; }
  busy = false;
  await load();
}

function poll(si) {
  clearTimeout(timer);
  timer = setTimeout(async () => {
    try {
      const r = await call(`/scm/signin/${encodeURIComponent(si.pollId)}`);
      if (r.state === 'pending') { signin = r; poll(r.signin || si); paint(); return; }
      note = { done: 'Signed in.', denied: 'GitHub says you declined.', expired: 'The code expired: start again.' }[r.state] || r.error || '';
      timer = null;
      await load();
    } catch (e) { err = e.message; paint(); }
  }, Math.max(1000, si.intervalMs || 5000));
}

const start = () => act(async () => { signin = await call('/scm/signin', { method: 'POST' }); });
const forget = () => act(async () => { await call('/scm/signin', { method: 'DELETE' }); note = 'Forgotten.'; });

function signinSection() {
  if (!mine) {
    return html`<section title="Your GitHub sign-in"><notice tone="muted" text="This copy isn't partitioned: tiles get the App's bot only."/></section>`;
  }
  const s = signin || { state: 'none' };
  if (s.state === 'done') {
    return html`<section title="Your GitHub sign-in" footer="Forget revokes every token handed out for you at GitHub.">
      <row title="Signed in as" detail=${s.identity.login} icon="person"/>
      <button role="destructive" ?busy=${busy} confirm=${{ title: 'Forget your GitHub sign-in?', message: 'Every token handed out for you stops working.', label: 'Forget', destructive: true }}
              @tap=${forget}>Forget</button>
    </section>`;
  }
  if (s.state === 'pending' && s.signin) {
    return html`<section title="Your GitHub sign-in" footer="The code is yours alone: never type someone else's.">
      <row title="Code" detail=${s.signin.userCode} mono="detail"/>
      <button role="primary" icon="link" @tap=${() => xbin.native.open(s.signin.url)}>Open GitHub</button>
      <button copy=${s.signin.userCode}>Copy the code</button>
      <notice tone="muted" text="Waiting for GitHub…"/>
    </section>`;
  }
  return html`<section title="Your GitHub sign-in" footer="Lets the agent push and open pull requests as you, in your own sandboxes.">
    <button role="primary" ?busy=${busy} ?disabled=${!gpage?.configured} @tap=${start}>Sign in with GitHub</button>
  </section>`;
}

function appSection() {
  if (!gpage) return nothing;
  if (!gpage.configured) return html`<section title="The GitHub App"><notice tone="warn" text="Not set up yet: a manager sets it up on the web page."/></section>`;
  const ev = gpage.events || {};
  return html`<section title="The GitHub App">
    <row title="App" detail=${gpage.app.slug}/>
    <row title="Device Flow" detail=${gpage.app.deviceFlow || 'unknown'} tone=${gpage.app.deviceFlow === 'off' ? 'danger' : 'muted'}/>
    <row title="Webhooks" detail=${ev.webhooks || 'unknown'} tone=${ev.webhooks === 'active' && ev.healthy ? 'ok' : 'muted'}/>
    ${gpage.app.installUrl ? html`<button icon="link" @tap=${() => xbin.native.open(gpage.app.installUrl)}>Install on an account</button>` : nothing}
  </section>`;
}

function paint() {
  render(html`<screen title="GitHub" style="list">
    ${err ? html`<section><notice tone="danger" text=${err}/></section>` : nothing}
    ${note ? html`<section><notice tone="ok" text=${note}/></section>` : nothing}
    ${signinSection()}
    ${appSection()}
  </screen>`);
}

paint();
load();
