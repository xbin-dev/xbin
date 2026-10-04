// scm.js — the scm-github page (index.html). Two halves:
//   your GitHub sign-in — the device flow in your own partition
//     (/scm/signin*, the scm contract's routes, which your page may call);
//   the GitHub App — at the tile's global instance, reached with
//     {partition: 'global'}: managers paste or create the App (/setup/*),
//     set the policy (/api/policy), see installations and webhook health,
//     revoke every bot token. Everyone else sees its status.
// An unpartitioned copy has no sign-ins: the page is the App half only.
// Secrets are write-only: no route answers one (API.md §Page routes).
import { html, render, nothing } from 'lit';
import { api, jbody } from '/vendor/bx-kit.js';

const self = window.xbin?.self || 'apps/scm-github';
const part = window.xbin?.partition || '';
const mine = part.startsWith('user:');             // the page is in a person's partition
const call = (path, opts = {}) => api(`/api/${self}${path}`, opts);
const callG = (path, opts = {}) => call(path, mine ? { ...opts, partition: 'global' } : opts);

const st = { page: null, gpage: null, signin: null, setup: null, insts: null, policy: null, err: '', note: '', busy: false };
const paste = { appId: '', clientId: '', clientSecret: '', privateKey: '', webhookSecret: '', hookUrl: '' };
const mf = { name: '', org: '', publicHost: '', ci: false, workflows: false, public: false, landed: '' };
let pollTimer = null;

async function load() {
  try {
    if (mine) {
      [st.page, st.signin] = await Promise.all([call('/api/page'), call('/scm/signin')]);
    }
    st.gpage = await callG('/api/page');
    if (st.gpage.manager) {
      st.setup = await callG('/setup/app');
      if (st.setup.configured) {
        [st.policy, st.insts] = await Promise.all([callG('/api/policy'), callG('/setup/installations').catch((e) => ({ error: e.message }))]);
      }
    }
    st.err = '';
  } catch (e) { st.err = e.message; }
  paint();
}

async function act(fn, note = '') {
  st.err = ''; st.note = ''; st.busy = true; paint();
  try { await fn(); st.note = note; } catch (e) { st.err = e.message; }
  st.busy = false;
  await load();
}

// --- your sign-in ---------------------------------------------------------------
function startSignin() {
  return act(async () => {
    st.signin = await call('/scm/signin', { method: 'POST' });
    if (st.signin.state === 'pending') schedulePoll(st.signin.signin);
  });
}

function schedulePoll(si) {
  clearTimeout(pollTimer);
  if (!si) return;
  pollTimer = setTimeout(async () => {
    try {
      const r = await call(`/scm/signin/${encodeURIComponent(si.pollId)}`);
      if (r.state === 'pending') { st.signin = r; schedulePoll(r.signin || si); paint(); return; }
      st.note = { done: 'Signed in.', denied: 'GitHub says you declined.', expired: 'The code expired: start again.' }[r.state] || r.error || '';
      await load();
    } catch (e) { st.err = e.message; paint(); }
  }, Math.max(1000, si.intervalMs || 5000));
}

const forget = () => confirm('Forget your GitHub sign-in here? Every token handed out for you stops working, and you sign in again to use GitHub through xbin.')
  && act(() => call('/scm/signin', { method: 'DELETE' }), 'Forgotten.');

function signinTpl() {
  if (!mine) {
    return html`<h4>Your GitHub sign-in</h4><p class="muted small">This copy isn't partitioned, so it keeps no one's
      GitHub sign-in: tiles get the App's bot only.</p>`;
  }
  const s = st.signin || { state: 'none' };
  const configured = st.page?.configured;
  let body;
  if (s.state === 'done') {
    body = html`<div class="row">Signed in as <b>${s.identity.login}</b>
      ${st.page?.person?.registered === false ? html`<span class="warn small">(not yet known to the global instance — it retries)</span>` : nothing}
      <button class="rm" ?disabled=${st.busy} @click=${forget}>Forget</button></div>
      <p class="muted small">Tokens handed out for you are scoped to the repos and permissions a tile asks for, and expire
      within hours; Forget revokes them all at GitHub.</p>`;
  } else if (s.state === 'pending' && s.signin) {
    if (!pollTimer) schedulePoll(s.signin);
    body = html`<p>Open <a href=${s.signin.url} target="_blank" rel="noopener">${s.signin.url}</a> and enter this code:</p>
      <div class="row"><span class="code">${s.signin.userCode}</span>
      <span class="muted small">waiting for GitHub… (expires ${new Date(s.signin.expiresAt).toLocaleTimeString()})</span></div>
      <p class="muted small">The code is yours alone: never type someone else's.</p>`;
  } else {
    body = html`<p class="muted small">Sign in to let the agent push and open pull requests as you, in your own sandboxes.</p>
      <button class="go" ?disabled=${!configured || st.busy} @click=${startSignin}>Sign in with GitHub</button>
      ${configured ? nothing : html`<span class="muted small"> — not yet: no GitHub App is set up.</span>`}`;
  }
  return html`<h4>Your GitHub sign-in</h4>${body}`;
}

// --- the App (managers) --------------------------------------------------------
const set = (o, k) => (e) => { o[k] = e.target.type === 'checkbox' ? e.target.checked : e.target.value; };

function submitManifest() {
  return act(async () => {
    const presets = [mf.ci && 'ci', mf.workflows && 'workflows'].filter(Boolean);
    const r = await callG('/setup/manifest', jbody({ name: mf.name.trim(), org: mf.org.trim(), publicHost: mf.publicHost.trim(), presets, public: mf.public }, 'POST'));
    // GitHub takes the manifest as a form POST, in a new tab (cap:open-links).
    const f = document.createElement('form');
    f.method = 'POST'; f.action = r.postUrl; f.target = '_blank';
    const i = document.createElement('input');
    i.type = 'hidden'; i.name = 'manifest'; i.value = JSON.stringify(r.manifest);
    f.append(i); document.body.append(f); f.submit(); f.remove();
  }, 'GitHub opened in a new tab: create the App there. Without a public host, paste the address GitHub sends you to below.');
}

const finishManifest = () => act(() => callG('/setup/manifest/code', jbody({ url: mf.landed.trim() }, 'POST')), 'The App is set up.');

function submitPaste() {
  const body = { ...paste, appId: Number(paste.appId) };
  for (const k of ['webhookSecret', 'hookUrl']) if (!body[k]) delete body[k];
  return act(async () => {
    await callG('/setup/app', jbody(body, 'POST'));
    for (const k of ['clientSecret', 'privateKey', 'webhookSecret']) paste[k] = '';
  }, 'The App is set up.');
}

function setupFormsTpl() {
  return html`
    <details ?open=${!st.setup?.configured}><summary><b>Create a GitHub App</b> <span class="muted small">(GitHub fills it in from a manifest)</span></summary>
      <div class="row">
        <label>App name<input .value=${mf.name} @input=${set(mf, 'name')} placeholder="acme-xbin"></label>
        <label>Organization (empty: your account)<input .value=${mf.org} @input=${set(mf, 'org')}></label>
        <label>Public host of the hooks exposure<input .value=${mf.publicHost} @input=${set(mf, 'publicHost')} placeholder="scm.example.com"></label>
      </div>
      <div class="row">
        <label class="inline"><input type="checkbox" .checked=${mf.ci} @change=${set(mf, 'ci')}> reruns (actions: write — also lets the App dispatch, cancel and delete runs)</label>
        <label class="inline"><input type="checkbox" .checked=${mf.workflows} @change=${set(mf, 'workflows')}> workflows: write</label>
        <label class="inline"><input type="checkbox" .checked=${mf.public} @change=${set(mf, 'public')}> public (installable on other accounts)</label>
      </div>
      <button class="go" ?disabled=${st.busy || !mf.name.trim()} @click=${submitManifest}>Create on GitHub…</button>
      <div class="row"><label>The address GitHub sent you to<input .value=${mf.landed} @input=${set(mf, 'landed')} placeholder="https://github.com/settings/apps?code=…&state=…"></label>
        <button ?disabled=${st.busy || !mf.landed.trim()} @click=${finishManifest}>Finish</button></div>
    </details>
    <details ?open=${!st.setup?.configured}><summary><b>Paste an existing App</b></summary>
      <div class="row">
        <label>App ID<input .value=${paste.appId} @input=${set(paste, 'appId')}></label>
        <label>Client ID<input .value=${paste.clientId} @input=${set(paste, 'clientId')}></label>
        <label>Client secret<input type="password" autocomplete="off" .value=${paste.clientSecret} @input=${set(paste, 'clientSecret')}></label>
      </div>
      <label>Private key (.pem)<textarea autocomplete="off" .value=${paste.privateKey} @input=${set(paste, 'privateKey')}></textarea></label>
      <div class="row">
        <label>Webhook URL (the hooks exposure + /hook/github)<input .value=${paste.hookUrl} @input=${set(paste, 'hookUrl')}></label>
        <label>Webhook secret (empty: keep, or make one)<input type="password" autocomplete="off" .value=${paste.webhookSecret} @input=${set(paste, 'webhookSecret')}></label>
      </div>
      <button class="go" ?disabled=${st.busy} @click=${submitPaste}>Check and save</button>
      <p class="muted small">Secrets are write-only: once saved, this page shows the key's fingerprint, never the key.</p>
    </details>`;
}

function appTpl() {
  const a = st.setup.app;
  return html`<div class="card">
      <div class="row"><b>${a.slug}</b> <span class="muted">· App ${a.appId} · ${a.owner}</span>
        <a href=${a.settingsUrl} target="_blank" rel="noopener">settings</a>
        <a href=${a.installUrl} target="_blank" rel="noopener">install</a></div>
      <div class="small muted mono">${a.keyFingerprint}</div>
      <div class="small">Webhook: ${st.setup.hook?.url ? html`<span class="mono">${st.setup.hook.url}</span>` : html`<span class="warn">no URL yet</span>`}</div>
      <div class="row small">Device Flow:
        <b class=${st.setup.deviceFlow === 'on' ? 'ok' : st.setup.deviceFlow === 'off' ? 'err' : 'muted'}>${st.setup.deviceFlow || 'unknown'}</b>
        <button ?disabled=${st.busy} @click=${() => act(() => callG('/setup/check', { method: 'POST' }))}>Check</button>
        <span class="muted">Enable it in the App's settings; "Expire user authorization tokens" must stay on.</span></div>
    </div>`;
}

function instTpl() {
  const i = st.insts;
  if (!i) return nothing;
  if (i.error) return html`<p class="err small">${i.error}</p>`;
  return html`<h4>Installations ${i.foreign ? html`<span class="chip warn">${i.foreign} foreign</span>` : nothing}</h4>
    ${i.items.length ? html`<table>${i.items.map((x) => html`<tr>
        <td><b>${x.account}</b> <span class="muted">${x.type}</span></td>
        <td class="small">${x.repositorySelection === 'all' ? 'all repos' : 'selected repos'}${x.suspended ? ' · suspended' : ''}</td>
        <td class="small">${x.foreign ? html`<span class="warn">foreign: served nothing</span>` : html`<span class="ok">allowed</span>`}</td>
        <td class="small"><a href=${x.url} target="_blank" rel="noopener">${x.foreign ? 'remove…' : 'configure'}</a></td></tr>`)}</table>`
      : html`<p class="muted small">Not installed anywhere yet: use the install link above.</p>`}`;
}

function policyTpl() {
  const p = st.policy;
  if (!p) return nothing;
  const lines = (k) => (e) => { p[k] = e.target.value.split(/[\s,]+/).filter(Boolean); };
  const save = () => act(() => callG('/api/policy', jbody({ ...p, personTtlMin: Number(p.personTtlMin) || 0 }, 'PUT')), 'Policy saved; bot tokens cut under the old one aren\'t handed out again.');
  return html`<h4>Policy</h4>
    <div class="row">
      <label>People's partitions and the bot<select .value=${p.botForPeople} @change=${set(p, 'botForPeople')}>
        <option value="off">off — people use their own sign-in only</option>
        <option value="own-access">own-access — the bot, for repos they can push to</option>
        <option value="on">on — the bot, for any repo it may name</option></select></label>
      <label>Person token life cap (min, 0: the epoch)<input type="number" min="0" max="1440" .value=${String(p.personTtlMin)} @input=${set(p, 'personTtlMin')}></label>
    </div>
    ${p.botForPeople !== 'off' ? html`<p class="warn small">A person may then mint a bot token for themselves directly — from their own page —
      outside any consumer's sandbox checks.</p>` : nothing}
    <div class="row">
      <label>Accounts served (orgs, users)<textarea .value=${p.allowedAccounts.join('\n')} @input=${lines('allowedAccounts')}></textarea></label>
      <label>Repos the bot may name (owner/name globs)<textarea .value=${p.botRepos.join('\n')} @input=${lines('botRepos')}></textarea></label>
    </div>
    <div class="row">
      <label class="inline"><input type="checkbox" .checked=${p.allowWorkflows} @change=${set(p, 'allowWorkflows')}> hand out workflows: write when asked</label>
      <label class="inline"><input type="checkbox" .checked=${p.allowRerun} @change=${set(p, 'allowRerun')}> offer reruns (with the App's actions: write)</label>
    </div>
    <div class="row"><button class="go" ?disabled=${st.busy} @click=${save}>Save policy</button>
      <button class="rm" ?disabled=${st.busy} @click=${() => confirm('Revoke every bot token handed out? Consumers ask again and get fresh ones.')
        && act(() => callG('/api/revoke-all', { method: 'POST' }), 'Bot tokens revoked.')}>Revoke all bot tokens</button></div>`;
}

function eventsTpl(ev) {
  if (!ev) return nothing;
  const tone = ev.webhooks === 'active' && ev.healthy ? 'ok' : ev.webhooks === 'inactive' ? 'err' : 'muted';
  return html`<div class="small">Webhooks: <b class=${tone}>${ev.webhooks}</b>${ev.lastDeliveryAt ? html` · last delivery ${new Date(ev.lastDeliveryAt).toLocaleString()}` : nothing}</div>`;
}

function appHalfTpl() {
  const g = st.gpage;
  if (!g) return nothing;
  if (!g.manager) {
    return html`<h4>The GitHub App</h4>
      ${g.configured ? html`<p><b>${g.app.slug}</b> <a href=${g.app.installUrl} target="_blank" rel="noopener">install it on an account</a></p>${eventsTpl(g.events)}`
        : html`<p class="muted">No GitHub App is set up yet: one of this tile's managers sets it up here.</p>`}`;
  }
  return html`<h4>The GitHub App <span class="chip">you manage this tile</span></h4>
    ${st.setup?.configured ? html`${appTpl()}${eventsTpl(g.events)}` : nothing}
    ${setupFormsTpl()}${instTpl()}${policyTpl()}`;
}

function paint() {
  render(html`
    <h3>GitHub <span class="chip">${mine ? 'yours' : part || 'not partitioned'}</span></h3>
    <p class="muted small">Repos, credentials, pull requests and CI from GitHub for the tiles bound to this one
      (<a href="/docs/scm.md" target="_blank" rel="noopener">the scm contract</a>).</p>
    ${st.err ? html`<p class="err">${st.err}</p>` : nothing}
    ${st.note ? html`<p class="ok small">${st.note}</p>` : nothing}
    ${signinTpl()}
    ${appHalfTpl()}`, document.getElementById('app'));
}

paint();
load();
