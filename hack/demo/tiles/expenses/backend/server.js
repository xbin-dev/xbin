// apps/expenses — each person's own expense book. The tile is partitioned
// ("partition": ["user"], docs/partitions.md): every person who opens it
// gets a backend instance and a `data` resource of their own, so nobody —
// not the tile's owner, not an admin viewing as them — reads another
// person's book through it. Books are still keyed by the person
// (X-XBin-User), which keeps the same code right where the tile runs as
// one instance (an xbind without --isolate). API.md has the routes.
'use strict';
const { who, json, fail, readBody, store, serve, when, zone } = require('./tile');

const db = store('data', 'books.json', { books: {} });
let st = db.load();
zone(st.tz);
const RATE = 0.70; // $/mile

const CATEGORIES = ['travel', 'lodging', 'meals', 'mileage', 'events', 'software', 'office', 'other'];
const STATUSES = ['draft', 'submitted', 'approved', 'reimbursed'];

// book: the caller's own (a person, never another's); 403 for anything else
function bookOf(req, res) {
  const c = who(req);
  if (!c.user) { fail(res, 403, 'expenses are per person: open the tile signed in'); return null; }
  if (c.viewedBy) { fail(res, 403, "an admin viewing as someone doesn't see their expenses"); return null; }
  st.books[c.user] ??= { name: '', items: [], seq: 1 };
  return st.books[c.user];
}
const amountOf = (x) => (x.category === 'mileage' ? Math.round((x.miles || 0) * RATE * 100) / 100 : Number(x.amount) || 0);
function view(b) {
  const items = [...b.items].sort((a, b2) => b2.date - a.date);
  const sum = (s) => Math.round(items.filter((x) => x.status === s).reduce((t, x) => t + x.amount, 0) * 100) / 100;
  const paidRecently = Math.round(items.filter((x) => x.status === 'reimbursed' && x.date >= Date.now() - 30 * 864e5).reduce((t, x) => t + x.amount, 0) * 100) / 100;
  return { name: b.name, items, totals: { draft: sum('draft'), submitted: sum('submitted'), approved: sum('approved'), reimbursed: paidRecently }, categories: CATEGORIES, rate: RATE };
}

serve([
  ['GET', '/expenses', (req, res) => { const b = bookOf(req, res); if (b) json(res, view(b)); }],
  ['POST', '/expenses', async (req, res) => {
    const b = bookOf(req, res); if (!b) return;
    const x = (await readBody(req)) ?? {};
    if (!x.merchant || !CATEGORIES.includes(x.category)) return fail(res, 400, 'need {merchant, category, amount | miles}');
    const item = { id: `e${b.seq++}`, date: x.date ? Date.parse(x.date) : Date.now(), merchant: String(x.merchant).slice(0, 80), category: x.category,
      miles: x.category === 'mileage' ? Number(x.miles) || 0 : undefined, note: String(x.note ?? '').slice(0, 200), receipt: !!x.receipt, status: 'draft', trip: x.trip || '' };
    item.amount = amountOf({ ...x, ...item });
    b.items.push(item);
    db.save(st);
    json(res, item);
  }],
  ['POST', '/submit', async (req, res) => {
    const b = bookOf(req, res); if (!b) return;
    const { ids } = (await readBody(req)) ?? {};
    let n = 0;
    for (const x of b.items) if (x.status === 'draft' && (!ids || ids.includes(x.id))) { x.status = 'submitted'; n++; }
    db.save(st);
    json(res, { submitted: n, ...view(b) });
  }],
  ['DELETE', /^\/expenses\/(e\d+)$/, (req, res, m) => {
    const b = bookOf(req, res); if (!b) return;
    const i = b.items.findIndex((x) => x.id === m[1] && x.status === 'draft');
    if (i < 0) return fail(res, 404, 'no such draft');
    b.items.splice(i, 1);
    db.save(st);
    json(res, view(b));
  }],
  // a person fills their own book (the demo seed signs in as them)
  ['POST', '/import', async (req, res) => {
    const b = bookOf(req, res); if (!b) return;
    const fx = (await readBody(req)) ?? {};
    b.name = fx.name || b.name;
    b.items = []; b.seq = 1;
    const now = fx.now || Date.now(); // the seed's clock
    if (fx.tz) { st.tz = fx.tz; zone(fx.tz); }
    for (const x of fx.items ?? []) {
      const date = when({ daysAgo: x.daysAgo ?? 0, at: x.at || '12:00' }, now);
      b.items.push({ id: `e${b.seq++}`, date, merchant: x.merchant, category: x.category, miles: x.miles, note: x.note || '', receipt: !!x.receipt,
        status: STATUSES.includes(x.status) ? x.status : 'draft', trip: x.trip || '', amount: amountOf(x) });
    }
    db.save(st);
    json(res, { items: b.items.length });
  }],
]);
