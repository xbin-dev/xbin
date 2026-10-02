// apps/ops-report — the nightly ops report. A cron job (registered by this
// backend at start, 05:30 every day) renders the night that just ended: the
// routes the platform planned per customer, on-time arrivals, the API and
// driver app's health, support load, incidents and notes. A report is
// rendered from the model in `data` (imported once) with a variation seeded
// by its date, so it reads the same whenever it is rendered. Each new
// report is announced on the `bus` (report/published) — the assistant's
// morning brief reads it. API.md has the routes.
'use strict';
const { who, canWrite, json, fail, readBody, store, xbind, mcp, serve, zone } = require('./tile');

const SELF = process.env.XBIN_COMPONENT || 'apps/ops-report';
const db = store('data', 'ops.json', { model: null, events: {}, reports: {}, runs: [] });
let st = db.load();
zone(st.tz); // nights and the 05:30 run on the seeding machine's clock

const pad = (n) => String(n).padStart(2, '0');
const dayKey = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
const daysAgoKeyFrom = (n, now) => { const d = new Date(now); d.setHours(12, 0, 0, 0); d.setDate(d.getDate() - n); return dayKey(d); };
const daysAgoKey = (n) => daysAgoKeyFrom(n, new Date());
const round = (v, n = 0) => Math.round(v * 10 ** n) / 10 ** n;
const clamp = (v, lo, hi) => Math.max(lo, Math.min(hi, v));

// rng(seed): a small deterministic generator (mulberry32) per report date.
function rng(seed) {
  let a = 0;
  for (const c of seed) a = (a * 31 + c.charCodeAt(0)) >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function render(date) {
  const m = st.model;
  if (!m) throw new Error('no model imported yet');
  const r = rng('larkspan:' + date);
  const ev = st.events[date] || {};
  const dow = new Date(date + 'T12:00:00').getDay();
  const volume = dow === 0 ? 0.52 : dow === 6 ? 0.78 : 1;
  const customers = m.customers.map((c) => {
    const routes = Math.max(1, Math.round(c.routes * volume * (0.94 + 0.11 * r())));
    const stops = Math.round(routes * c.stopsPerRoute * (0.95 + 0.1 * r()));
    const onTime = round(clamp((ev.customers?.[c.id]?.onTime) ?? (c.onTime + (r() - 0.5) * 1.8), 85, 99.6), 1);
    const exceptions = Math.max(0, Math.round(stops * (100 - onTime) / 100 * (0.3 + 0.15 * r())));
    return { id: c.id, name: c.name, routes, stops, onTime, exceptions };
  });
  const stops = customers.reduce((s, c) => s + c.stops, 0);
  const p = { ...m.platform, ...(ev.set || {}) };
  const incidents = ev.incident ? [ev.incident] : [];
  const report = {
    date, renderedAt: Date.now(),
    totals: {
      routes: customers.reduce((s, c) => s + c.routes, 0), stops,
      onTime: round(customers.reduce((s, c) => s + c.onTime * c.stops, 0) / stops, 1),
      exceptions: customers.reduce((s, c) => s + c.exceptions, 0),
      milesSavedPct: round(6.4 + r() * 2.2, 1),
    },
    platform: {
      planSecondsP95: ev.set?.planSecondsP95 ?? round(p.planSecondsP95 * (0.9 + 0.22 * r()), 1),
      apiRequestsM: round(p.apiRequestsM * volume * (0.93 + 0.14 * r()), 2),
      apiErrorPct: round(p.apiErrorPct * (0.6 + 0.9 * r()) + (incidents.some((i) => i.sev <= 3) ? 0.05 : 0), 3),
      apiP95ms: Math.round(p.apiP95ms * (0.9 + 0.2 * r()) + (incidents.some((i) => i.sev <= 3) ? 60 : 0)),
      uptimePct: incidents.some((i) => i.sev <= 3) ? 99.96 : 100,
      drivers: Math.round(p.drivers * volume * (0.97 + 0.05 * r())),
      crashFreePct: round(clamp(p.crashFreePct + (r() - 0.5) * 0.3, 98.5, 99.95), 2),
    },
    support: {
      opened: ev.set?.ticketsOpened ?? Math.max(1, Math.round(p.ticketsOpened * (0.6 + 0.8 * r()) * volume)),
      closed: Math.max(1, Math.round(p.ticketsClosed * (0.6 + 0.8 * r()) * volume)),
      firstResponseMin: Math.round(p.firstResponseMin * (0.7 + 0.6 * r())),
    },
    customers, incidents, notes: ev.notes || [],
  };
  report.status = incidents.some((i) => i.sev <= 3) ? 'attention' : 'ok';
  return report;
}

const headline = (rep) => ({ date: rep.date, status: rep.status, routes: rep.totals.routes, stops: rep.totals.stops, onTime: rep.totals.onTime,
  apiP95ms: rep.platform.apiP95ms, incidents: rep.incidents.length, renderedAt: rep.renderedAt });
const latest = () => { const ks = Object.keys(st.reports).sort(); return ks.length ? st.reports[ks[ks.length - 1]] : null; };

async function run(date, by) {
  const rep = render(date);
  st.reports[date] = rep;
  st.runs.unshift({ at: Date.now(), by, date });
  st.runs = st.runs.slice(0, 50);
  db.save(st);
  try {
    await xbind('POST', '/api/xbin/bus/publish', { resource: `res:${SELF}/bus`, topic: 'report/published', data: { ...headline(rep), notes: rep.notes, incidents: rep.incidents } });
  } catch (e) { console.error('publish:', e.message); }
  return rep;
}

// the nightly job: 05:30 every day (idempotent; re-registered at every start)
async function registerCron() {
  try {
    await xbind('PUT', '/api/xbin/cron/jobs', { name: 'nightly-report', resource: `res:${SELF}/cron`, schedule: '30 5 * * *', path: '/run', role: 'writer' });
  } catch (e) { console.error('cron:', e.message); }
}

const tools = [
  {
    name: 'latest_report',
    description: "Larkspan's most recent nightly ops report: routes planned, stops, on-time rate, platform health, support load, incidents and notes.",
    inputSchema: { type: 'object', properties: {} },
    call: () => { const l = latest(); if (!l) throw new Error('no report yet'); return l; },
  },
  {
    name: 'get_report',
    description: 'The nightly ops report for one date (YYYY-MM-DD).',
    inputSchema: { type: 'object', properties: { date: { type: 'string' } }, required: ['date'] },
    call: ({ date }) => { const rep = st.reports[date]; if (!rep) throw new Error(`no report for ${date}`); return rep; },
  },
  {
    name: 'list_reports',
    description: 'Headline numbers of the recent nightly reports, newest first (for trends).',
    inputSchema: { type: 'object', properties: { limit: { type: 'number' } } },
    call: ({ limit }) => Object.keys(st.reports).sort().reverse().slice(0, limit || 14).map((k) => headline(st.reports[k])),
  },
];

serve([
  ['GET', '/me', (req, res) => json(res, { user: who(req).user, canRun: canWrite(who(req)) })],
  ['GET', '/reports', (req, res) => json(res, { reports: Object.keys(st.reports).sort().reverse().map((k) => headline(st.reports[k])), runs: st.runs.slice(0, 10) })],
  ['GET', '/latest', (req, res) => { const l = latest(); return l ? json(res, l) : fail(res, 404, 'no report yet'); }],
  ['GET', /^\/reports\/(\d{4}-\d{2}-\d{2})$/, (req, res, m) => (st.reports[m[1]] ? json(res, st.reports[m[1]]) : fail(res, 404, 'no report for that night'))],
  ['POST', '/run', async (req, res, m, url) => {
    const c = who(req);
    if (c.from !== 'xbin/cron' && !canWrite(c)) return fail(res, 403, 'running the report needs write access');
    const date = url.searchParams.get('date') || daysAgoKey(1);
    json(res, headline(await run(date, c.from === 'xbin/cron' ? 'schedule' : c.user || 'owner')));
  }],
  ['POST', '/import', async (req, res) => {
    if (who(req).from !== 'owner') return fail(res, 403, "import is the workspace owner's (the seed)");
    const fx = (await readBody(req)) ?? {};
    const days = fx.renderDays ?? 14;
    zone(fx.tz);
    st = { tz: fx.tz, model: { customers: fx.customers, platform: fx.platform }, events: {}, reports: {}, runs: [] };
    // "last night" is the newest night the 05:30 job has rendered: before
    // 05:30 that is the night before yesterday's
    const base = new Date();
    if (base.getHours() * 60 + base.getMinutes() < 5 * 60 + 30) base.setDate(base.getDate() - 1);
    const daysAgoKey = (n) => daysAgoKeyFrom(n, base);
    for (const n of fx.nights ?? []) st.events[daysAgoKey(n.daysAgo)] = n;
    // the history, oldest first, each rendered when the schedule would have
    // (05:30 the morning after); the newest is announced like a real run
    const morningAfter = (k) => { const d = new Date(k + 'T05:30:00'); d.setDate(d.getDate() + 1); return Math.min(d.getTime(), Date.now()); };
    for (let d = days; d >= 1; d--) {
      const k = daysAgoKey(d), rep = render(k);
      rep.renderedAt = morningAfter(k);
      st.reports[k] = rep;
      st.runs.unshift({ at: rep.renderedAt, by: 'schedule', date: k });
    }
    st.runs = st.runs.slice(0, 50);
    db.save(st);
    const last = latest();
    try {
      await xbind('POST', '/api/xbin/bus/publish', { resource: `res:${SELF}/bus`, topic: 'report/published', data: { ...headline(last), notes: last.notes, incidents: last.incidents } });
    } catch (e) { console.error('publish:', e.message); }
    json(res, { reports: Object.keys(st.reports).length });
  }],
  ['POST', '/mcp', mcp({ name: 'larkspan-ops-report', version: '1.0.0' }, tools)],
]);

registerCron();
