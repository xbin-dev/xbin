// controls — a "new backup job" form: every field kind, toggles, pickers in
// all three styles, hints, a validation error, disabled controls, and the
// return-key label. The script types into it, so the tree shows the values
// the app reported back (controlled fields).
import { html, render } from '/vendor/xb-native.js';

const api = async (path, opt) => {
  const r = await xbin.fetch(`/api/${xbin.self}${path}`, opt);
  if (!r.ok) throw new Error(`${path}: HTTP ${r.status}`);
  return r.json();
};

const job = {
  name: '', endpoint: '', notify: '', secret: '', frequency: 'daily', at: '02:30', start: '',
  retention: 30, parallel: '4', compress: true, encrypt: true, verify: false, klass: 'standard',
  exclude: '', bucketQuery: '',
};
let policy = null;
let buckets = [];
let saving = false;

const set = (k) => (e) => { job[k] = e.value; paint(); };
const parallelError = () => (Number(job.parallel) > policy.maxParallel ? `At most ${policy.maxParallel} on this plan` : '');
const valid = () => job.name.trim() && job.endpoint.startsWith('https://') && !parallelError();

async function load() {
  policy = await api('/policy');
  job.endpoint = policy.defaultEndpoint;
  job.start = policy.today;
  paint();
}
async function findBuckets(q) {
  buckets = (await api(`/buckets?q=${encodeURIComponent(q)}`)).buckets;
  paint();
}

const paint = () => render(!policy ? html`<screen title="New backup job" style="form"/>` : html`
  <screen title="New backup job" style="form">
    <section title="Destination">
      <field kind="text" label="Name" placeholder="nightly-db" value=${job.name} submit="next" @input=${set('name')}/>
      <field kind="url" label="Endpoint" value=${job.endpoint} hint="Any S3-compatible service" @change=${set('endpoint')}/>
      <field kind="search" label="Bucket" placeholder="Search buckets" value=${job.bucketQuery} submit="search"
             hint=${buckets.length ? `${buckets.length} found: ${buckets.join(', ')}` : ''}
             @input=${set('bucketQuery')} @submit=${(e) => findBuckets(e.value)}/>
      <field kind="secure" label="Secret key" placeholder="unchanged" value=${job.secret}
             hint="Stored encrypted; never shown again" @input=${set('secret')}/>
      <field label="Region" value=${policy.region} disabled hint="Set by the endpoint"/>
    </section>
    <section title="Schedule">
      <picker label="Frequency" style="segmented" value=${job.frequency} @change=${set('frequency')}
              options=${[{ value: 'hourly', label: 'Hourly' }, { value: 'daily', label: 'Daily' }, { value: 'weekly', label: 'Weekly' }]}/>
      <field kind="time" label="At" value=${job.at} @change=${set('at')}/>
      <field kind="date" label="Starting" value=${job.start} @change=${set('start')}/>
      <picker label="Keep" style="menu" value=${job.retention} @change=${set('retention')}
              options=${policy.retention.map((d) => ({ value: d, label: `${d} days`, icon: 'archive' }))}/>
      <field kind="number" label="Parallel uploads" value=${job.parallel} error=${parallelError()} @input=${set('parallel')}/>
    </section>
    <section title="Options" footer=${`Encryption is required by the ${policy.plan} plan.`}>
      <toggle label="Compress (zstd)" value=${job.compress} @change=${set('compress')}/>
      <toggle label="Encrypt" value=${job.encrypt} disabled/>
      <toggle label="Verify after upload" value=${job.verify} @change=${set('verify')}/>
      <picker label="Storage class" style="inline" value=${job.klass} @change=${set('klass')}
              options=${[{ value: 'standard', label: 'Standard', icon: 'bolt' }, { value: 'infrequent', label: 'Infrequent access', icon: 'clock' },
                { value: 'glacier', label: 'Archive', icon: 'archive' }]}/>
      <field kind="multiline" label="Exclude" placeholder="one pattern per line" value=${job.exclude} @input=${set('exclude')}/>
      <field kind="email" label="Notify" placeholder="you@example.com" value=${job.notify} submit="done" @input=${set('notify')}/>
    </section>
    <section>
      <button role="primary" icon="check" ?disabled=${!valid()} ?busy=${saving} @tap=${() => {}}>Create job</button>
    </section>
  </screen>`);

load();
