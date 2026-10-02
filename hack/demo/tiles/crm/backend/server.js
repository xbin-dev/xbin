// apps/crm — Larkspan's CRM: accounts, contacts, deals and their activity,
// kept as one JSON document in the `data` filesystem resource. The page
// (index.html) is the main client; other tiles read it over the API with a
// reader grant, and agents through its MCP tools (POST /mcp, the `mcp`
// provide). API.md has the routes.
'use strict';
const { who, canWrite, json, fail, readBody, store, mcp, serve, when, zone } = require('./tile');

const db = store('data', 'crm.json', { accounts: [], contacts: [], deals: [], activity: [], team: [], seq: 1 });
let data = db.load();
zone(data.tz);

const STAGES = ['lead', 'qualified', 'demo', 'proposal', 'negotiation', 'won', 'lost'];
const OPEN = new Set(['lead', 'qualified', 'demo', 'proposal', 'negotiation']);
const STAGE_NAMES = { lead: 'Lead', qualified: 'Qualified', demo: 'Demo', proposal: 'Proposal', negotiation: 'Negotiation', won: 'Closed won', lost: 'Closed lost' };

const acct = (id) => data.accounts.find((a) => a.id === id);
const person = (id) => data.team.find((p) => p.id === id);
const lc = (s) => String(s ?? '').toLowerCase();

function findAccount(q) {
  if (!q) return null;
  const s = lc(q).trim();
  return acct(s) || data.accounts.find((a) => lc(a.name) === s) || data.accounts.find((a) => lc(a.name).includes(s) || lc(a.domain).includes(s));
}

function dealView(d) {
  const a = acct(d.account);
  return { ...d, accountName: a?.name ?? d.account, ownerName: person(d.owner)?.name ?? d.owner, stageName: STAGE_NAMES[d.stage] ?? d.stage };
}
function accountView(a) {
  const deals = data.deals.filter((d) => d.account === a.id);
  const last = data.activity.filter((x) => x.account === a.id).reduce((m, x) => Math.max(m, x.at), 0);
  return {
    ...a, ownerName: person(a.owner)?.name ?? a.owner, csmName: person(a.csm)?.name ?? a.csm,
    openDeals: deals.filter((d) => OPEN.has(d.stage)).length,
    openAmount: deals.filter((d) => OPEN.has(d.stage)).reduce((s, d) => s + d.amount, 0),
    contacts: data.contacts.filter((c) => c.account === a.id).length,
    lastActivity: last || null,
  };
}
function activityView(x) {
  return { ...x, accountName: acct(x.account)?.name ?? x.account, whoName: person(x.who)?.name ?? x.who };
}

function summary() {
  const open = data.deals.filter((d) => OPEN.has(d.stage));
  const now = Date.now();
  const closed = data.deals.filter((d) => d.stage === 'won' || d.stage === 'lost');
  const won = closed.filter((d) => d.stage === 'won');
  const byStage = {};
  for (const s of STAGES) {
    const ds = data.deals.filter((d) => d.stage === s);
    byStage[s] = { name: STAGE_NAMES[s], count: ds.length, amount: ds.reduce((t, d) => t + d.amount, 0) };
  }
  const customers = data.accounts.filter((a) => a.status === 'customer');
  return {
    pipeline: open.reduce((s, d) => s + d.amount, 0),
    weighted: Math.round(open.reduce((s, d) => s + d.amount * (d.probability ?? 0) / 100, 0)),
    openDeals: open.length,
    won30: won.filter((d) => d.close >= now - 30 * 864e5).reduce((s, d) => s + d.amount, 0),
    winRate: closed.length ? Math.round(100 * won.length / closed.length) : null,
    arr: customers.reduce((s, a) => s + (a.arr || 0), 0),
    customers: customers.length,
    atRisk: customers.filter((a) => a.health === 'risk').map((a) => a.name),
    renewing90: customers.filter((a) => a.renewal && a.renewal - now < 90 * 864e5).map((a) => ({ name: a.name, renewal: a.renewal, arr: a.arr })),
    byStage,
  };
}

// import: replace everything with a fixture (relative times resolved now).
function importData(fx) {
  const now = fx.now || Date.now(); // the seed's clock: relative times land in working hours
  zone(fx.tz); // …and its wall clock ("at": "15:10")
  let seq = 1;
  const out = { tz: fx.tz, team: fx.team ?? data.team ?? [], accounts: [], contacts: [], deals: [], activity: [], seq: 1 };
  for (const a of fx.accounts ?? []) out.accounts.push({ ...a, since: when(a.since, now), renewal: when(a.renewal, now) });
  for (const c of fx.contacts ?? []) out.contacts.push({ id: c.id ?? `c-${seq++}`, ...c });
  for (const d of fx.deals ?? []) out.deals.push({ ...d, close: when(d.close, now), created: when(d.created, now), updated: now });
  for (const x of fx.activity ?? []) out.activity.push({ id: x.id ?? `a-${seq++}`, ...x, at: when(x.at, now) });
  out.activity.sort((a, b) => b.at - a.at);
  out.seq = seq;
  data = out;
  db.save(data);
  return { accounts: out.accounts.length, contacts: out.contacts.length, deals: out.deals.length, activity: out.activity.length };
}

function log(account, type, text, whoId, deal) {
  const x = { id: `a-${data.seq++}`, account, deal, type, who: whoId, at: Date.now(), text };
  data.activity.unshift(x);
  return x;
}

const accountDetail = (a) => ({
  account: accountView(a),
  contacts: data.contacts.filter((c) => c.account === a.id),
  deals: data.deals.filter((d) => d.account === a.id).map(dealView),
  activity: data.activity.filter((x) => x.account === a.id).slice(0, 12).map(activityView),
});

// ---- MCP tools: what an agent (or the chat tile) may ask ----
const tools = [
  {
    name: 'search_accounts',
    description: 'Search Larkspan CRM accounts (customers and prospects) by name, domain, city or industry. Returns a short row per account.',
    inputSchema: { type: 'object', properties: { query: { type: 'string', description: 'words to look for; empty lists every account' } } },
    call: ({ query }) => {
      const s = lc(query);
      return data.accounts.filter((a) => !s || [a.name, a.domain, a.city, a.industry, a.status].some((v) => lc(v).includes(s)))
        .map((a) => { const v = accountView(a); return { id: v.id, name: v.name, status: v.status, plan: v.plan, vans: v.vans, arr: v.arr, health: v.health, owner: v.ownerName, openDeals: v.openDeals }; });
    },
  },
  {
    name: 'get_account',
    description: 'One CRM account by name or id: its details, contacts, deals and the latest activity.',
    inputSchema: { type: 'object', properties: { query: { type: 'string', description: 'account name, domain or id' } }, required: ['query'] },
    call: ({ query }) => {
      const a = findAccount(query);
      if (!a) throw new Error(`no account matches "${query}"`);
      const d = accountDetail(a);
      const iso = (t) => (t ? new Date(t).toISOString().slice(0, 10) : null);
      return {
        account: { ...d.account, since: iso(d.account.since), renewal: iso(d.account.renewal), lastActivity: iso(d.account.lastActivity) },
        contacts: d.contacts.map(({ name, title, email, phone, note }) => ({ name, title, email, phone, note })),
        deals: d.deals.map(({ id, name, stageName, amount, close, ownerName, probability, next }) => ({ id, name, stage: stageName, amount, close: iso(close), owner: ownerName, probability, next })),
        activity: d.activity.map(({ type, whoName, at, text }) => ({ type, who: whoName, date: iso(at), text })),
      };
    },
  },
  {
    name: 'list_deals',
    description: 'Deals in the pipeline, optionally narrowed to a stage (lead, qualified, demo, proposal, negotiation, won, lost) or an owner.',
    inputSchema: { type: 'object', properties: { stage: { type: 'string' }, owner: { type: 'string', description: 'a person id or name' } } },
    call: ({ stage, owner }) => data.deals
      .filter((d) => (!stage || d.stage === lc(stage)) && (!owner || d.owner === lc(owner) || lc(person(d.owner)?.name).includes(lc(owner))))
      .map((d) => { const v = dealView(d); return { id: v.id, account: v.accountName, name: v.name, stage: v.stageName, amount: v.amount, close: new Date(v.close).toISOString().slice(0, 10), owner: v.ownerName, probability: v.probability, next: v.next }; }),
  },
  {
    name: 'pipeline_summary',
    description: 'The sales pipeline at a glance: open pipeline, weighted forecast, won this quarter, ARR, accounts at risk and renewals in the next 90 days.',
    inputSchema: { type: 'object', properties: {} },
    call: () => {
      const s = summary();
      return { ...s, renewing90: s.renewing90.map((r) => ({ ...r, renewal: new Date(r.renewal).toISOString().slice(0, 10) })) };
    },
  },
];

serve([
  ['GET', '/me', (req, res) => { const c = who(req); json(res, { user: c.user, name: person(c.user)?.name ?? '', canWrite: canWrite(c) }); }],
  ['GET', '/team', (req, res) => json(res, { team: data.team })],
  ['GET', '/summary', (req, res) => json(res, summary())],
  ['GET', '/accounts', (req, res, m, url) => {
    const q = lc(url.searchParams.get('q')), st = url.searchParams.get('status');
    json(res, { accounts: data.accounts.filter((a) => (!st || a.status === st) && (!q || [a.name, a.domain, a.city, a.industry].some((v) => lc(v).includes(q)))).map(accountView) });
  }],
  ['GET', /^\/accounts\/([\w.-]+)$/, (req, res, m) => {
    const a = acct(m[1]);
    if (!a) return fail(res, 404, 'no such account');
    json(res, accountDetail(a));
  }],
  ['GET', '/deals', (req, res, m, url) => {
    const st = url.searchParams.get('stage'), ac = url.searchParams.get('account'), ow = url.searchParams.get('owner');
    json(res, { stages: STAGES.map((s) => ({ id: s, name: STAGE_NAMES[s] })), deals: data.deals.filter((d) => (!st || d.stage === st) && (!ac || d.account === ac) && (!ow || d.owner === ow)).map(dealView) });
  }],
  ['PATCH', /^\/deals\/([\w-]+)$/, async (req, res, m) => {
    const c = who(req);
    if (!canWrite(c)) return fail(res, 403, 'editing deals needs write access to the CRM');
    const d = data.deals.find((x) => x.id === m[1]);
    if (!d) return fail(res, 404, 'no such deal');
    const b = (await readBody(req)) ?? {};
    if (b.stage && b.stage !== d.stage) {
      if (!STAGES.includes(b.stage)) return fail(res, 400, 'unknown stage');
      d.stage = b.stage;
      if (b.stage === 'won') d.probability = 100;
      if (b.stage === 'lost') d.probability = 0;
      log(d.account, 'stage', `Moved "${d.name}" to ${STAGE_NAMES[d.stage]}.`, c.user || 'owner', d.id);
    }
    for (const k of ['next', 'amount', 'probability']) if (b[k] !== undefined) d[k] = b[k];
    d.updated = Date.now();
    db.save(data);
    json(res, dealView(d));
  }],
  ['GET', '/contacts', (req, res, m, url) => {
    const q = lc(url.searchParams.get('q'));
    json(res, { contacts: data.contacts.filter((c) => !q || [c.name, c.email, c.title].some((v) => lc(v).includes(q))).map((c) => ({ ...c, accountName: acct(c.account)?.name ?? c.account })) });
  }],
  ['GET', '/activity', (req, res, m, url) => {
    const ac = url.searchParams.get('account'), n = Math.min(200, Number(url.searchParams.get('limit')) || 50);
    json(res, { activity: data.activity.filter((x) => !ac || x.account === ac).slice(0, n).map(activityView) });
  }],
  ['POST', '/activity', async (req, res) => {
    const c = who(req);
    if (!canWrite(c)) return fail(res, 403, 'logging activity needs write access to the CRM');
    const b = (await readBody(req)) ?? {};
    if (!acct(b.account) || !b.text) return fail(res, 400, 'need {account, type, text}');
    const x = log(b.account, b.type || 'note', String(b.text).slice(0, 2000), c.user || 'owner', b.deal);
    db.save(data);
    json(res, activityView(x));
  }],
  ['POST', '/import', async (req, res) => {
    if (who(req).from !== 'owner') return fail(res, 403, 'import is the workspace owner\'s (the seed)');
    json(res, importData((await readBody(req)) ?? {}));
  }],
  ['POST', '/mcp', mcp({ name: 'larkspan-crm', version: '1.0.0' }, tools)],
]);
