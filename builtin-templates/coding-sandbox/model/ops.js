// model/ops.js — what the operators' views show, from GET /ops/state
// (API.md "The operators' API"): every sandbox's metadata across the
// consumers, usage against the quotas, the images and their builds, the
// backend and what the substrate offers, the settings as forms. Pure
// functions: no lit, no DOM, no calls — model/app.js makes those.
import * as F from './format.js';

const QUOTA_KEYS = ['sandboxes', 'running', 'memMiB', 'vcpus', 'diskGiB'];
export const QUOTA_LABELS = { sandboxes: 'sandboxes', running: 'running', memMiB: 'memory MiB', vcpus: 'vCPUs', diskGiB: 'disk GiB' };

// --- sandboxes ------------------------------------------------------------------------

// sandboxRows: every sandbox as the operators' table shows it, newest
// activity first; caps are the offer's (snapshots decide that action). Who
// may use each is shown (who), never changed here: only its home consumer
// or its owner there changes that (API.md "The operators' API").
export function sandboxRows(st, now = Date.now()) {
  if (!st) return [];
  const caps = (st.offer && st.offer.caps) || [];
  return (st.sandboxes || []).map((s) => {
    const busy = ['creating', 'deleting', 'starting', 'stopping'].includes(s.state);
    const actions = [];
    if (s.state === 'stopped') actions.push({ id: 'start', label: 'Start' });
    if (s.state === 'running' || s.state === 'starting') actions.push({ id: 'stop', label: 'Stop' });
    if (!busy && caps.includes('snapshots')) actions.push({ id: 'snapshots', label: 'Snapshots…' });
    if (s.state !== 'deleting') {
      actions.push({ id: 'delete', label: 'Delete', danger: true,
        confirm: `Delete ${s.name} (${s.id}) of ${s.consumer}? Its files and snapshots go with it — for everyone who uses it.` });
    }
    return {
      id: s.id, name: s.name, consumer: F.consumerText(s.consumer, s.owner && s.owner.partitionId), owner: F.ownerText(s.owner), state: s.state,
      stateLabel: F.STATES[s.state] || s.state, tone: F.stateTone(s.state), stateDetail: s.stateDetail || '',
      image: (s.image && (s.image.title || s.image.id)) || '', imageId: (s.image && s.image.id) || '',
      size: (s.size && s.size.id) || '', sizeText: F.sizeText(s.size), egress: s.egress || 'none', egressText: F.egressText(s),
      isolation: F.ISOLATION[s.isolation] || s.isolation || '', disk: s.diskBytes ? F.bytes(s.diskBytes) : '',
      lastActive: s.lastActive || 0, lastText: F.ago(s.lastActive, now), shares: s.shares || [], visibility: s.visibility,
      members: s.members || [], who: F.whoText(s), outdated: !!(s.base && s.base.outdated), runtime: s.runtime || '', actions,
    };
  }).sort((a, b) => b.lastActive - a.lastActive || a.id.localeCompare(b.id));
}

// usageRows: each consumer's and person's usage beside the quota that binds
// it (an override, else the default; 0 = no limit), the consumers first.
export function usageRows(st) {
  if (!st || !st.usage) return [];
  const q = (st.config && st.config.quotas) || {};
  const rows = [];
  const add = (kind, who, used, quota, override) => {
    const cells = QUOTA_KEYS.map((k) => {
      const u = (used && used[k]) || 0;
      const lim = (quota && quota[k]) || 0;
      return { key: k, used: u, limit: lim, over: lim > 0 && u > lim, full: lim > 0 && u >= lim, text: lim ? `${u} / ${lim}` : String(u) };
    });
    rows.push({ kind, who, override, cells, full: cells.some((c) => c.full) });
  };
  for (const [c, u] of Object.entries(st.usage.consumers || {}).sort()) {
    const o = q.consumers && q.consumers[c];
    add('consumer', c, u, o || q.consumer, !!o);
  }
  for (const [p, u] of Object.entries(st.usage.people || {}).sort()) {
    const o = q.people && q.people[p];
    add('person', p, u, o || q.person, !!o);
  }
  return rows;
}

// --- the substrate ----------------------------------------------------------------------

// backendInfo: the backend, the substrate's offer and what is wrong with it.
export function backendInfo(st) {
  const rt = (st && st.runtime) || null;
  const offer = (st && st.offer) || null;
  const self = (st && st.self) || 'apps/coding-sandbox';
  const classes = ((rt && rt.egress) || []).filter((e) => e.class !== 'none').map((e) => {
    const word = e.slot || String(e.class).replace(/^class:/, '');
    return {
      slot: word, ref: e.ref || '', reach: e.reach || 'none', note: e.note || '',
      offered: !!(offer && offer.egress.includes(word)),
      bind: `bx bind ${self} ${word}=${word === 'internet' ? 'internet' : 'lan:10.0.0.0/16'}`,
    };
  });
  const errors = [...new Set([st && st.backend && st.backend.error, st && st.runtimeError, st && st.listError].filter(Boolean))];
  // the xbin backend's first hurdle: a workspace admin approves cap:sandboxes
  const hint = errors.some((e) => /cap:sandboxes/.test(e))
    ? 'This tile\'s cap:sandboxes grant waits for a workspace admin: approve it in the tile\'s pending grants (the binding panel, or the admin console).' : '';
  return {
    name: (st && st.backend && st.backend.name) || 'xbin', error: (st && st.backend && st.backend.error) || '',
    runtimeError: (st && st.runtimeError) || '', listError: (st && st.listError) || '', errors, hint,
    modes: (rt && rt.modes) || [], unavailable: (rt && rt.unavailable) || [], users: (rt && rt.users) || '',
    caps: (offer && offer.caps) || [], egress: (offer && offer.egress) || [], notes: (offer && offer.notes) || [],
    classes, limits: (rt && rt.limits) || null, used: (rt && rt.used) || null,
  };
}

// MODES: the operators' isolation setting.
export const MODES = [
  { value: 'auto', label: 'Automatic — a VM where the substrate offers one, else a namespace' },
  { value: 'vm', label: 'VMs only' },
  { value: 'namespace', label: 'Namespaces only' },
];

// modeInfo: the mode setting, and what a new sandbox gets with it now — or
// why none can be made (never another mode than the one chosen).
export function modeInfo(st) {
  const value = (st && st.config && st.config.mode) || 'auto';
  const b = backendInfo(st);
  const has = (m) => b.modes.some((x) => x.mode === m);
  const why = (m) => (b.unavailable.find((x) => x.mode === m) || {}).reason || 'the substrate doesn\'t offer it';
  let now = '', blocked = '';
  if (value === 'auto') now = has('vm') ? 'vm' : has('namespace') ? 'namespace' : ((b.modes[0] && b.modes[0].mode) || '');
  else if (has(value)) now = value;
  if (!now) blocked = value === 'auto' ? (b.unavailable.map((u) => `${u.mode}: ${u.reason}`).join('; ') || 'the substrate runs no sandboxes') : why(value);
  const options = MODES.map((m) => ({ ...m, why: m.value !== 'auto' && !has(m.value) ? why(m.value) : '' }));
  const words = { vm: 'VMs', namespace: 'namespaces', container: 'containers', 'cloud-vm': 'cloud VMs' };
  return { value, now, nowText: now ? `new sandboxes are ${words[now] || now}` : '', blocked, options };
}

// --- images -----------------------------------------------------------------------------

// imageRows: the configured images, each with its build (a setup script's)
// and whether consumers are offered it now. A build that isn't ready keeps
// the previous good one (kept): new sandboxes clone that one while its
// script is the current one, and it goes once a build succeeds.
export function imageRows(st, now = Date.now()) {
  if (!st || !st.config) return [];
  const built = new Map((st.images || []).map((b) => [b.id, b]));
  const offered = (st.offer && st.offer.images) || [];
  return (st.config.images || []).map((im) => {
    const b = built.get(im.id) || null;
    const prev = (b && b.state !== 'ready' && b.previous) || null;
    let build = im.setup ? 'not built yet (the first sandbox of it builds it)' : 'the substrate\'s base: nothing to build';
    if (b) build = b.state === 'ready' ? 'built' : b.state === 'building' ? (prev ? 'rebuilding…' : 'building…') : prev ? 'the rebuild failed' : 'the build failed';
    let kept = '';
    if (prev) {
      const when = F.ago(prev.built, now);
      kept = prev.setupHash === b.setupHash && prev.mode === b.mode
        ? `The previous build${when ? ` (${when})` : ''} is kept: new sandboxes clone it until a build succeeds.`
        : `The previous build${when ? ` (${when})` : ''}, of the script before, is kept until a build succeeds.`;
    }
    return {
      id: im.id, title: im.title || im.id, tools: im.tools || [], setup: im.setup || '', default: !!im.default,
      agents: (im.harnesses || []).map((h) => h.title || h.id),
      buildEgress: im.buildEgress || '', offered: offered.includes(im.id), built: b, buildText: build, kept,
      tone: b ? (b.state === 'ready' ? 'ok' : b.state === 'building' || prev ? 'warn' : 'danger') : 'muted',
      canBuild: !!im.setup && (!b || b.state !== 'building'),
    };
  });
}

// imageForm: an image as the editor holds it (strings), from a config image.
export const imageForm = (im = {}) => ({
  id: im.id || '', title: im.title || '', tools: (im.tools || []).join(', '), setup: im.setup || '',
  buildEgress: im.buildEgress || '', default: !!im.default, was: im.id || '',
});

const ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/;

// applyImage: the images with form (replacing its `was`, or added) — or the
// error that stops it. An edited image keeps what the form doesn't hold (its
// coding agents, `harnesses`; a newer manager's fields).
export function applyImage(images, form) {
  const id = String(form.id || '').trim();
  if (!ID.test(id)) return { error: 'the id is letters, digits, ".", "_" and "-", up to 32' };
  if (images.some((im) => im.id === id && im.id !== form.was)) return { error: `there is an image ${id} already` };
  const prev = (form.was && images.find((x) => x.id === form.was)) || {};
  const im = {
    ...prev, id, title: String(form.title || '').trim() || id, setup: form.setup || '',
    tools: String(form.tools || '').split(/[\s,]+/).filter(Boolean), default: !!form.default,
  };
  if (form.buildEgress) im.buildEgress = form.buildEgress;
  else delete im.buildEgress;
  if (!im.setup) delete im.setup;
  let out = form.was ? images.map((x) => (x.id === form.was ? im : x)) : [...images, im];
  if (im.default) out = out.map((x) => (x.id === id ? x : { ...x, default: false }));
  return { images: out };
}

// removeImage: the images without id (refused for the last one).
export function removeImage(images, id) {
  const out = images.filter((x) => x.id !== id);
  return out.length ? { images: out } : { error: 'a manager offers at least one image' };
}

// --- sizes, quotas, layout ----------------------------------------------------------------

// sizeForms: the sizes as the editor holds them.
export const sizeForms = (sizes) => (sizes || []).map((s) => ({ ...s, title: s.title || '' }));

// applySizes: the edited sizes as config.sizes — or the error.
export function applySizes(forms) {
  const out = [];
  const seen = new Set();
  for (const f of forms) {
    const s = { id: String(f.id || '').trim(), title: String(f.title || '').trim(), memMiB: Number(f.memMiB), vcpus: Number(f.vcpus), diskGiB: Number(f.diskGiB), default: !!f.default };
    if (!ID.test(s.id)) return { error: `size "${s.id}": the id is letters, digits, ".", "_" and "-"` };
    if (seen.has(s.id)) return { error: `size ${s.id} twice` };
    if (!(s.memMiB >= 128 && s.vcpus >= 1 && s.diskGiB >= 1)) return { error: `size ${s.id}: memory ≥ 128 MiB, ≥ 1 vCPU, disk ≥ 1 GiB` };
    if (!s.title) delete s.title;
    seen.add(s.id);
    out.push(s);
  }
  if (!out.length) return { error: 'a manager offers at least one size' };
  return { sizes: out };
}

// quotaRows: the quotas as the editor shows them: the defaults, then each override.
export function quotaRows(config) {
  const q = (config && config.quotas) || {};
  const row = (kind, key, label, v, removable) => ({ kind, key, label, removable, values: Object.fromEntries(QUOTA_KEYS.map((k) => [k, (v && v[k]) || 0])) });
  return [
    row('consumer', '', 'Every consumer (default)', q.consumer, false),
    row('person', '', 'Every person (default)', q.person, false),
    ...Object.entries(q.consumers || {}).sort().map(([k, v]) => row('consumer', k, `Consumer ${k}`, v, true)),
    ...Object.entries(q.people || {}).sort().map(([k, v]) => row('person', k, `Person ${k}`, v, true)),
  ];
}

// setQuota: config.quotas with one row's values (key "" = the default of
// its kind; null values remove an override).
export function setQuota(quotas, kind, key, values) {
  const q = { consumer: {}, person: {}, ...(quotas || {}) };
  const clean = values && Object.fromEntries(QUOTA_KEYS.map((k) => [k, Math.max(0, Math.floor(Number(values[k]) || 0))]).filter(([, v]) => v > 0));
  const map = kind === 'consumer' ? 'consumers' : 'people';
  if (!key) q[kind] = clean || {};
  else {
    q[map] = { ...(q[map] || {}) };
    if (clean) q[map][key] = clean;
    else delete q[map][key];
  }
  return q;
}

// shares: a sandbox's shares with one added (or its people changed) or removed.
// The page shares with consumer tiles; a share with one user partition of a
// partitioned consumer (partitionId) is kept as it is, and removed by its id.
export function shareWith(shares, consumer, users) {
  const c = String(consumer || '').trim();
  if (!c) return { error: 'a share names its consumer tile (apps/…)' };
  const rest = (shares || []).filter((s) => s.consumer !== c || s.partitionId);
  return { shares: [...rest, { consumer: c, users: F.parseUsers(users) }] };
}
export const unshare = (shares, consumer, partitionId = '') =>
  (shares || []).filter((s) => s.consumer !== consumer || (s.partitionId || '') !== partitionId);

// mountText: a configured mount in words.
export const mountText = (m) => `${m.res}${m.path ? '/' + m.path : ''} → ${m.at}${m.ro ? ' (read-only)' : ''}`;

// parseMount: "res:<scope>/<name>[:<sub-path>] <at> [ro]" → a mount, or the error.
export function parseMount(text) {
  const [src, at, ro] = String(text || '').trim().split(/\s+/);
  if (!src || !at) return { error: 'a mount is "res:<scope>/<name>[:<sub-path>] <where> [ro]"' };
  const m = /^(res:[^:]+)(?::(.+))?$/.exec(src);
  if (!m) return { error: 'the source is a filesystem resource of this tile\'s: res:<scope>/<name>' };
  if (!at.startsWith('/')) return { error: 'where it goes is an absolute path' };
  const out = { res: m[1], at };
  if (m[2]) out.path = m[2];
  if (ro === 'ro') out.ro = true;
  return { mount: out };
}
