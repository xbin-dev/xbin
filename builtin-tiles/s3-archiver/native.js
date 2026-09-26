// native.js — the S3 archiver's settings as a native form for the xbin app
// (docs/frontend-kit.md). Same calls in the same order as index.html: PUT
// /config, then the two secrets into the tile's vault, then a reload, then
// POST /check — saving always verifies that the bucket is reachable. The
// secret field is `secure` (never persisted or snapshotted by the app) and is
// cleared after a save, as on the page.
import { html, render, nothing } from '/vendor/xb-native.js';
import { jbody } from '/vendor/bx-kit.js';

const self = xbin.self;
// The raw Response: the page reads statuses and error bodies itself.
const call = (p, o) => xbin.fetch(`/api/${self}${p}`, o);
// Vault writes take {"value": …} JSON (docs/auth.md) — a raw body 400s.
const setSecret = (key, value) => xbin.fetch(`/api/xbin/vault/${self}/${encodeURIComponent(key)}`, jbody({ value }, 'PUT'));
const cfg = { endpoint: '', region: '', bucket: '', prefix: '' };
let hasCreds = false, accessKey = '', secretKey = '', busy = '', msg = null;   // msg: {tone, text}

async function load() {
  try {
    const d = await (await call('/config')).json();
    const c = d.config || {};
    for (const k of Object.keys(cfg)) cfg[k] = c[k] || '';
    hasCreds = !!d.hasCreds;
  } catch { /* first load / not admin */ }
  paint();
}
async function save() {
  busy = 'save'; msg = { tone: 'info', text: 'Saving…' }; paint();
  try {
    const r = await call('/config', jbody({ endpoint: cfg.endpoint.trim(), region: cfg.region.trim(),
      bucket: cfg.bucket.trim(), prefix: cfg.prefix.trim() }, 'PUT'));
    if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'save failed');
    if (accessKey.trim() && !(await setSecret('accessKey', accessKey.trim())).ok) throw new Error('could not store access key in the vault');
    if (secretKey && !(await setSecret('secretKey', secretKey)).ok) throw new Error('could not store secret key in the vault');
    secretKey = '';
    await load();
    await check();          // saving verifies it actually reaches the bucket
  } catch (e) { msg = { tone: 'danger', text: String(e.message ?? e) }; }
  finally { busy = ''; paint(); }
}
// Ask the backend to hit the bucket with the stored config + creds.
async function check() {
  msg = { tone: 'info', text: 'Checking connection…' }; paint();
  try {
    const r = await call('/check', { method: 'POST' });
    const d = await r.json().catch(() => ({}));
    msg = r.ok ? { tone: 'ok', text: `Connected — bucket “${d.bucket}” is reachable.` }
      : { tone: 'danger', text: d.error || `Connection failed (${r.status}).` };
  } catch (e) { msg = { tone: 'danger', text: 'Connection check failed: ' + (e.message ?? e) }; }
  paint();
}
const test = async () => { busy = 'test'; await check(); busy = ''; paint(); };
const set = (k) => (e) => { cfg[k] = e.value; paint(); };
const paint = () => render(html`
  <screen title="S3 Archiver" style="form">
    <section footer="Where component backups are stored: any S3-compatible endpoint (AWS, MinIO, R2, B2). Needs egress — bind this tile's net interface.">
      <field label="Endpoint" kind="url" placeholder="https://s3.us-east-1.amazonaws.com" value=${cfg.endpoint} @input=${set('endpoint')}/>
      <field label="Region" placeholder="us-east-1" value=${cfg.region} @input=${set('region')}/>
      <field label="Bucket" placeholder="my-backups" value=${cfg.bucket} @input=${set('bucket')}/>
      <field label="Prefix (optional)" placeholder="xbin/" value=${cfg.prefix} @input=${set('prefix')}/>
    </section>
    <section title="Credentials" footer=${hasCreds ? 'Secret access key · set' : 'Secret access key · not set'}>
      <field label="Access key ID" value=${accessKey} @input=${(e) => { accessKey = e.value; paint(); }}/>
      <field label="Secret access key" kind="secure" placeholder="leave blank to keep" value=${secretKey} @input=${(e) => { secretKey = e.value; paint(); }}/>
    </section>
    <section>
      ${msg ? html`<notice tone=${msg.tone} text=${msg.text}/>` : nothing}
      <button role="primary" ?busy=${busy === 'save'} ?disabled=${!!busy} @tap=${save}>Save &amp; test</button>
      <button ?busy=${busy === 'test'} ?disabled=${!!busy} @tap=${test}>Test connection</button>
    </section>
  </screen>`);
paint();   // at once: the app wants a tree before the backend answers (a cold start can take seconds)
load();
