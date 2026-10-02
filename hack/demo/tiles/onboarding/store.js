// store.js — each customer's onboarding lives under customer/<id> in this
// tile's own kv resource (res:<tile>/state), read and written straight from
// the page: the tile needs no backend.
const res = () => `res:${xbin.self}/state`;
const kv = (key) => `/api/xbin/kv/${res()}/${key}`;

export async function loadCustomers() {
  const r = await xbin.fetch(`/api/xbin/kv/${res()}/?prefix=customer/`);
  if (!r.ok) throw new Error(`kv: HTTP ${r.status}`);
  const { keys } = await r.json();
  const all = await Promise.all((keys || []).map(async (k) => {
    const v = await xbin.fetch(kv(k));
    return v.ok ? v.json() : null;
  }));
  return all.filter(Boolean);
}

export async function saveCustomer(c) {
  const r = await xbin.fetch(kv(`customer/${c.id}`), { method: 'PUT', body: JSON.stringify(c) });
  if (!r.ok) throw new Error(`kv: HTTP ${r.status}`);
}
