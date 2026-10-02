// crm.js — what the CRM knows about a customer (plan, vans, health,
// owner), through apps/crm's API: this tile holds a reader grant there.
const cache = new Map();

export async function crmAccount(id) {
  if (!cache.has(id)) {
    cache.set(id, xbin.fetch(`/api/apps/crm/accounts/${encodeURIComponent(id)}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((d) => d?.account ?? null)
      .catch(() => null));
  }
  return cache.get(id);
}
