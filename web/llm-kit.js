/**
 * llm-kit.js — the words and arithmetic of xbind's LLM connector (D226,
 * docs/llm.md) for every surface that shows it: the /xbin/llm page, the
 * admin console's workspace → LLM tab, and the quota rows of its users and
 * organisations tabs.
 *
 * What it holds: numbers as an admin types and reads them (tokens "2M",
 * cost "$50"), the host's locks (GET /llm/config's `locks`: which setting
 * is "set by your host", and whether every one is), the connector's status
 * in words, an upstream's form (name, base URL, models, prices → the PUT
 * body), a quota's form (→ PUT /llm/quotas), a limit's progress and the
 * plain sentence a person reads at one, and usage rows summed per person
 * and per component for today and this month.
 *
 * The server decides everything — what is locked, whose quota applies,
 * what a call costs; this file only says it. Pure, no imports:
 * hack/llm-kit.test.mjs runs it under node. The calls are
 * /vendor/llm-client.js.
 */

// The limits of a quota, in display order (internal/llm limitNames).
export const LIMITS = Object.freeze(['tokensPerDay', 'tokensPerMonth', 'costPerDay', 'costPerMonth']);
export const LIMIT_META = Object.freeze({
  tokensPerDay: { label: 'tokens per day', short: 'tokens/day', cost: false, month: false },
  tokensPerMonth: { label: 'tokens per month', short: 'tokens/month', cost: false, month: true },
  costPerDay: { label: 'cost per day', short: 'cost/day', cost: true, month: false },
  costPerMonth: { label: 'cost per month', short: 'cost/month', cost: true, month: true },
});
export const UNLIMITED = -1;
// The roles a default model is set for; an admin may add others.
export const ROLES = Object.freeze([
  { id: 'agent', label: 'workspace agent', hint: 'xbin/agent' },
  { id: 'harness', label: 'coding harnesses', hint: 'xbin/harness' },
]);
// The people switch: who may call the connector directly.
export const PEOPLE = Object.freeze([
  { id: 'admins', label: 'Workspace admins', hint: 'the default: adding a key does not let every member spend on it' },
  { id: 'everyone', label: 'Everyone', hint: 'every signed-in person, held by their quota' },
  { id: 'nobody', label: 'Nobody', hint: 'only tiles bound to the connector' },
]);

// ---- numbers ----

const trim = (n, digits) => String(Number(n.toFixed(digits)));

// fmtTokens(n) → "0", "950", "12.3k", "2M", "1.25M", "3.4B": a count to read.
export function fmtTokens(n) {
  n = Number(n);
  if (!Number.isFinite(n)) return '—';
  const a = Math.abs(n);
  if (a < 1000) return String(Math.round(n));
  if (a < 1e6) return `${trim(n / 1e3, a < 1e4 ? 2 : 1)}k`;
  if (a < 1e9) return `${trim(n / 1e6, 2)}M`;
  return `${trim(n / 1e9, 2)}B`;
}

// fmtCost(usd) → "$0", "$0.004", "$1.50", "$1,250": two decimals, more for a
// cost under a cent, none past a thousand.
export function fmtCost(usd) {
  const n = Number(usd);
  if (!Number.isFinite(n)) return '—';
  if (n === 0) return '$0';
  const a = Math.abs(n);
  if (a < 0.01) return `$${trim(n, 4)}`;
  if (a >= 1000) return `$${Math.round(n).toLocaleString('en-US')}`;
  return `$${n.toFixed(2)}`;
}

// limitText(name, v) → the value as an admin types it, exactly: "2M",
// "200k", "1500", "50", "unlimited"; "" for a limit that isn't set.
export function limitText(name, v) {
  v = Number(v);
  if (!v || !Number.isFinite(v)) return '';
  if (v === UNLIMITED) return 'unlimited';
  if (LIMIT_META[name]?.cost) return String(v);
  if (v >= 1e6 && v % 1e6 === 0) return `${v / 1e6}M`;
  if (v >= 1e3 && v % 1e3 === 0) return `${v / 1e3}k`;
  return String(v);
}

// fmtLimit(name, v) → the value to read: "2M tokens", "$50", "unlimited".
export function fmtLimit(name, v) {
  v = Number(v);
  if (v === UNLIMITED) return 'unlimited';
  if (!v || !Number.isFinite(v)) return 'not set';
  return LIMIT_META[name]?.cost ? fmtCost(v) : `${limitText(name, v)} tokens`;
}

// parseLimit(name, text) → {value} or {error}: "" → 0 (not set),
// "unlimited" (also "none", "-1") → -1, tokens "2M" / "200k" / "1500" (whole
// numbers), cost "50" / "$12.50".
export function parseLimit(name, text) {
  const t = String(text ?? '').trim().toLowerCase().replace(/,/g, '');
  const m = LIMIT_META[name] ?? LIMIT_META.tokensPerDay;
  if (!t) return { value: 0 };
  if (t === 'unlimited' || t === 'none' || t === '-1' || t === '∞') return { value: UNLIMITED };
  if (m.cost) {
    const x = /^\$?\s*(\d+(?:\.\d+)?)$/.exec(t);
    if (!x || !(+x[1] > 0)) return { error: `${m.label}: a cost in USD like 50 or 12.50, "unlimited", or empty` };
    return { value: +x[1] };
  }
  const x = /^(\d+(?:\.\d+)?)\s*([kmb]?)$/.exec(t);
  const value = x ? Math.round(+x[1] * { '': 1, k: 1e3, m: 1e6, b: 1e9 }[x[2]]) : 0;
  if (!x || !(value > 0)) return { error: `${m.label}: a number of tokens like 200k or 2M, "unlimited", or empty` };
  return { value };
}

// ---- the host's locks (GET /llm/config, GET /llm/quotas: `locks`) ----

// lockOf(locks, path) → {locked, reason}: the setting at path is the
// host's — named itself, or by "*" (the host locks every setting).
export function lockOf(locks, path) {
  const l = locks?.[path] ?? locks?.['*'];
  return l?.locked ? { locked: true, reason: l.reason || 'set by your host' } : { locked: false, reason: '' };
}
export const isLocked = (locks, path) => lockOf(locks, path).locked;

// fullyLocked(cfg): every setting is the host's.
export const fullyLocked = (cfg) => !!(cfg?.host?.lockAll || cfg?.locks?.['*']?.locked);

// lockLabel(reason) → "Set by your host": the badge beside a locked setting.
export function lockLabel(reason) {
  const r = String(reason || 'set by your host');
  return r.charAt(0).toUpperCase() + r.slice(1);
}

// lockedPaths(cfg) → the locked settings, sorted ("*" left out).
export const lockedPaths = (cfg) => Object.keys(cfg?.locks ?? {}).filter((p) => p !== '*' && cfg.locks[p]?.locked).sort();

// ---- status ----

const VAULT = {
  open: '', plaintext: '',
  sealed: 'The vault is sealed: providers whose key is kept in it answer nothing until an admin unseals it, and no key can be set.',
  locked: 'The vault isn’t set up: a provider key can’t be stored until an admin sets it up (bx vault unseal).',
};
export const vaultWords = (v) => VAULT[v] ?? '';
// vaultShut(cfg): a key can't be stored or read right now.
export const vaultShut = (cfg) => cfg?.vault === 'sealed' || cfg?.vault === 'locked';

// statusOf(cfg) → {state, icon, cls, words}: the connector in a line.
// state: unconfigured | off | sealed | problems | ok.
export function statusOf(cfg) {
  const n = (cfg?.upstreams ?? []).length;
  const who = isLocked(cfg?.locks, 'enabled') ? ' by your host' : '';
  if (!cfg?.configured || !n) {
    return { state: 'unconfigured', icon: 'info', cls: 'info', words: fullyLocked(cfg) ? 'Not set up — your host configures it' : 'Not set up — add a provider to turn it on' };
  }
  if (!cfg.enabled) return { state: 'off', icon: 'power', cls: '', words: `Off — every call is refused (switched off${who})` };
  const inVault = (cfg.upstreams ?? []).filter((u) => u.key?.source === 'vault').length;
  if (vaultShut(cfg) && inVault) {
    return { state: 'sealed', icon: 'lock', cls: 'warn', words: `Vault ${cfg.vault === 'sealed' ? 'sealed' : 'not set up'} — ${inVault} of ${plural(n, 'provider')} can’t be reached` };
  }
  if ((cfg.problems ?? []).length) return { state: 'problems', icon: 'warning', cls: 'warn', words: `On, with ${plural(cfg.problems.length, 'problem')}` };
  return { state: 'ok', icon: 'ok', cls: 'on', words: `On — ${plural(n, 'provider')}` };
}

export const plural = (n, one, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

// keyWords(key) → how an upstream's key reads: never the key.
export function keyWords(key) {
  if (!key?.set) return 'no key';
  if (key.source === 'host') return 'set by your host';
  return key.hint ? `ends in ${String(key.hint).replace(/^…/, '')}` : 'set (in the vault)';
}

// modelsWords(models) → "gpt-5, gpt-5-mini" or what an empty list means.
export const modelsWords = (models) => ((models ?? []).length ? models.join(', ') : 'every model the provider lists');

// testWords(res) → {ok, text}: POST /llm/upstreams/<name>/test's answer.
export function testWords(res) {
  if (!res) return { ok: false, text: '' };
  if (res.running) return { ok: false, text: 'testing…' };
  const ms = Number.isFinite(res.ms) ? ` in ${res.ms} ms` : '';
  if (res.ok) {
    if (res.model) return { ok: true, text: `${res.model} answered${ms}${res.tokens ? ` (${plural(res.tokens, 'token')})` : ''}` };
    return { ok: true, text: `reachable${ms} — it lists ${plural(res.models ?? 0, 'model')}` };
  }
  return { ok: false, text: `${res.error || `failed${res.status ? ` (${res.status})` : ''}`}` };
}

// ---- an upstream's form ----

// validName(name) → '' or what is wrong (internal/llm validUpstream).
export function validName(name) {
  const n = String(name ?? '');
  if (!n) return 'a name, like openai or local';
  if (n === 'xbin') return '"xbin" is taken (xbin/default, xbin/<role>)';
  if (!/^[a-z0-9][a-z0-9._-]{0,62}$/.test(n)) return 'lowercase letters, digits, . _ - (starting with a letter or digit)';
  return '';
}

// validBaseURL(url) → '' or what is wrong.
export function validBaseURL(url) {
  let u;
  try { u = new URL(String(url ?? '').trim()); } catch { return 'the provider’s OpenAI-compatible base URL, like https://api.openai.com/v1'; }
  if (u.protocol !== 'https:' && u.protocol !== 'http:') return 'an http(s) URL';
  if (u.username || u.password) return 'no credentials in the URL — the key has its own field';
  if (u.search || u.hash) return 'no query or fragment in the URL';
  return '';
}

// parseModels(text) → the models a field lists, by comma, space or line;
// duplicates dropped, order kept.
export const parseModels = (text) => [...new Set(String(text ?? '').split(/[\s,]+/).map((s) => s.trim()).filter(Boolean))];

// priceRows(prices) → [{model, in, out, cachedIn}] as text fields, sorted.
export function priceRows(prices) {
  return Object.keys(prices ?? {}).sort().map((model) => {
    const p = prices[model] ?? {};
    return { model, in: numText(p.in), out: numText(p.out), cachedIn: numText(p.cachedIn) };
  });
}
const numText = (v) => (v == null || v === 0 ? '' : String(v));

// pricesBody(rows) → {prices} or {error}: USD per million tokens; a row
// without a model is dropped, a model without in and out is an error.
export function pricesBody(rows) {
  const prices = {};
  for (const r of rows ?? []) {
    const model = String(r?.model ?? '').trim();
    if (!model) continue;
    if (model in prices) return { error: `prices: ${model} is listed twice` };
    const p = {};
    for (const k of ['in', 'out', 'cachedIn']) {
      const t = String(r[k] ?? '').trim().replace(/^\$/, '');
      if (!t) continue;
      if (!/^\d+(\.\d+)?$/.test(t)) return { error: `prices: ${model}: ${k} is USD per million tokens, like 1.25` };
      p[k] = +t;
    }
    if (p.in == null || p.out == null) return { error: `prices: ${model} needs both an input and an output price` };
    prices[model] = p;
  }
  return { prices };
}

const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

// upstreamBody(draft, current) → {name, body} or {error}: PUT
// /llm/upstreams/<name>'s body. draft: {name, baseURL, key, models (text),
// private, prices (rows)}; current: the upstream as GET /llm/config answers
// it, null for a new one. Only what changed is named; the key only when one
// was typed (it is never read back), "" never — removeKey says that.
export function upstreamBody(draft, current) {
  const name = String(draft?.name ?? current?.name ?? '').trim();
  let err = validName(name);
  if (err) return { error: `name: ${err}` };
  const body = {};
  const baseURL = String(draft?.baseURL ?? '').trim().replace(/\/+$/, '');
  if (!current || baseURL !== (current.baseURL ?? '')) {
    err = validBaseURL(baseURL);
    if (err) return { error: `base URL: ${err}` };
    body.baseURL = baseURL;
  }
  const key = String(draft?.key ?? '').trim();
  if (key) {
    if (/\s/.test(key) || key.length > 4096) return { error: 'key: one line without spaces, at most 4096 characters' };
    body.key = key;
  } else if (draft?.removeKey && current?.key?.set) body.key = '';
  const models = parseModels(draft?.models);
  if (!same(models, current?.models ?? [])) body.models = models;
  if (!!draft?.private !== !!current?.private) body.private = !!draft?.private;
  const p = pricesBody(draft?.prices);
  if (p.error) return { error: p.error };
  if (!same(p.prices, current?.prices ?? {})) body.prices = p.prices;
  return { name, body };
}

// upstreamDraft(u) → the form's fields for an upstream (or a new one).
export const upstreamDraft = (u) => ({
  name: u?.name ?? '', baseURL: u?.baseURL ?? '', key: '', removeKey: false, models: (u?.models ?? []).join(', '),
  private: !!u?.private, prices: priceRows(u?.prices),
});

// modelChoices(list, cfg) → the model names a default may take: GET
// /llm/v1/models' ids without the xbin/… ones and the aliases, else each
// upstream's configured list as <upstream>/<model>.
export function modelChoices(list, cfg) {
  const out = new Set();
  for (const m of list?.data ?? []) if (m?.id && m.owned_by !== 'alias' && !String(m.id).startsWith('xbin/')) out.add(m.id);
  for (const u of cfg?.upstreams ?? []) for (const m of u.models ?? []) out.add(`${u.name}/${m}`);
  return [...out].sort();
}

// modelsDraft(cfg) → {model, roles: [[role, model]], aliases: [[alias,
// target]]}: the default models and aliases as the form's rows — the known
// roles first (empty when unset), then the workspace's own, sorted.
export function modelsDraft(cfg) {
  const roles = cfg?.defaults?.roles ?? {}, known = ROLES.map((r) => r.id);
  return {
    model: cfg?.defaults?.model ?? '',
    roles: [...known, ...Object.keys(roles).filter((r) => !known.includes(r)).sort()].map((r) => [r, roles[r] ?? '']),
    aliases: Object.keys(cfg?.aliases ?? {}).sort().map((a) => [a, cfg.aliases[a]]),
  };
}

// modelsChanges(draft, cfg) → {changes} or {error}: PUT /llm/config's body
// for the rows that differ — "" removes a role, an alias or the default; a
// row the host locks is never named. changes is null when nothing differs.
export function modelsChanges(draft, cfg) {
  const locks = cfg?.locks, cur = cfg?.defaults ?? {}, out = {};
  const text = (v) => String(v ?? '').trim();
  if (text(draft?.model) !== (cur.model ?? '') && !isLocked(locks, 'defaults.model')) out.defaults = { model: text(draft.model) };
  const roles = {};
  for (const [role, model] of draft?.roles ?? []) {
    const r = text(role);
    if (!r) { if (text(model)) return { error: 'a role needs a name, like agent or harness' }; continue; }
    if (!/^[a-z][a-z0-9-]{0,31}$/.test(r) || r === 'default') return { error: `role ${r}: a lowercase name (letters, digits, -), and not "default"` };
    if (text(model) !== (cur.roles?.[r] ?? '') && !isLocked(locks, `defaults.roles.${r}`)) roles[r] = text(model);
  }
  if (Object.keys(roles).length) out.defaults = { ...(out.defaults ?? {}), roles };
  const aliases = {}, seen = new Set();
  for (const [alias, target] of draft?.aliases ?? []) {
    const a = text(alias);
    if (!a) { if (text(target)) return { error: 'an alias needs a name — the model name a client sends' }; continue; }
    if (seen.has(a)) return { error: `alias ${a} is listed twice` };
    seen.add(a);
    if (text(target) !== (cfg?.aliases?.[a] ?? '') && !isLocked(locks, `aliases.${a}`)) aliases[a] = text(target);
  }
  for (const a of Object.keys(cfg?.aliases ?? {})) if (!seen.has(a) && !isLocked(locks, `aliases.${a}`)) aliases[a] = '';
  if (Object.keys(aliases).length) out.aliases = aliases;
  return { changes: Object.keys(out).length ? out : null };
}

// ---- quotas ----

// quotaDraft(q) → a quota as the form's text fields.
export function quotaDraft(q) {
  const d = { action: q?.action || 'stop' };
  for (const k of LIMITS) d[k] = limitText(k, q?.[k]);
  return d;
}

// quotaBody(draft) → {quota} or {error}: a quota as PUT /llm/quotas takes
// it; null when the draft sets nothing (which removes it).
export function quotaBody(draft) {
  const q = {};
  for (const k of LIMITS) {
    const v = parseLimit(k, draft?.[k]);
    if (v.error) return { error: v.error };
    if (v.value) q[k] = v.value;
  }
  if (!Object.keys(q).length) return { quota: null };
  if (draft?.action === 'warn') q.action = 'warn';
  return { quota: q };
}

// quotaSummary(q) → "2M tokens/day · $50/month · warns" or "no limits".
export function quotaSummary(q) {
  const parts = LIMITS.filter((k) => q?.[k]).map((k) => {
    const m = LIMIT_META[k], per = m.month ? 'month' : 'day';
    if (q[k] === UNLIMITED) return `unlimited ${m.short}`;
    return m.cost ? `${fmtCost(q[k])}/${per}` : `${limitText(k, q[k])} tokens/${per}`;
  });
  if (!parts.length) return 'no limits';
  return `${parts.join(' · ')} · ${q.action === 'warn' ? 'warns' : 'stops'}`;
}

// sourceWords(source, whose) → where a limit in force comes from; whose
// is "your" for the person reading their own, "their" for an admin's view.
export function sourceWords(source, whose = 'your') {
  const s = String(source ?? '');
  if (s.startsWith('org:')) return `org ${s.slice(4)}`;
  return { user: `${whose} own quota`, default: 'the default', workspace: 'the workspace' }[s] ?? s;
}

// limitPct(l) → 0–100: how much of a limit is used.
export const limitPct = (l) => (l?.value > 0 ? Math.max(0, Math.min(100, Math.round((Number(l.used) / l.value) * 100))) : 0);

// usedWords(l) → "1.2M of 2M tokens", "$12.40 of $50".
export function usedWords(l) {
  if (LIMIT_META[l?.name]?.cost) return `${fmtCost(l.used)} of ${fmtCost(l.value)}`;
  return `${fmtTokens(l?.used)} of ${limitText(l?.name, l?.value)} tokens`;
}

// resetWords(iso, now) → "in 40 min", "in 5 h", "in 3 d" — a window's end
// (UTC midnight, or the first of the month); "" for no time.
export function resetWords(iso, now = Date.now()) {
  const t = Date.parse(iso ?? '');
  if (!Number.isFinite(t)) return '';
  const s = Math.max(0, (t - now) / 1000);
  if (s < 90) return 'in a minute';
  if (s < 3600) return `in ${Math.round(s / 60)} min`;
  if (s < 86400 * 2) return `in ${Math.round(s / 3600)} h`;
  return `in ${Math.round(s / 86400)} d`;
}

// quotaMessage(quota, now) → {level, text} for the person the quota is
// about (GET /llm's `quota`), or null while nothing is reached: a plain
// sentence saying what ran out, what that does and when it is back.
export function quotaMessage(quota, now = Date.now()) {
  const all = [...(quota?.limits ?? []), ...(quota?.workspace ?? [])];
  const l = all.find((x) => x.state === 'stop') ?? all.find((x) => x.state === 'warn');
  if (!l) return null;
  const m = LIMIT_META[l.name] ?? LIMIT_META.tokensPerDay;
  const what = `${m.month ? 'this month’s' : 'today’s'} ${m.cost ? 'LLM budget' : 'LLM tokens'}`;
  const whose = l.source === 'workspace' ? 'The workspace has' : 'You have';
  const back = resetWords(l.resets, now);
  const when = back ? ` It resets ${back} (${m.month ? 'the first of the month' : 'midnight'}, UTC).` : '';
  if (l.state === 'stop') {
    return { level: 'stop', text: `${whose} used all of ${what} (${usedWords(l)}). Calls to the workspace’s models are refused until it resets; a workspace admin can raise the limit.${when}` };
  }
  return { level: 'warn', text: `${whose} passed ${what} (${usedWords(l)}). Calls still go through; your workspace’s admins are told.${when}` };
}

// ---- usage (GET /llm/usage: rows per UTC day) ----

// today(now) → the UTC day, "2026-11-14".
export const today = (now = Date.now()) => new Date(now).toISOString().slice(0, 10);

// personLabel / componentLabel: the two empty keys and the owner token.
export const personLabel = (u) => (u === '' ? 'no person (a tile’s own calls)' : u === 'owner' ? 'owner token' : String(u));
export const componentLabel = (c) => (c === '' ? 'direct calls' : String(c));

const zero = () => ({ requests: 0, failed: 0, tokens: 0, inputTokens: 0, outputTokens: 0, cost: 0, unpriced: 0 });
function add(t, r) {
  t.requests += r.requests ?? 0; t.failed += r.failed ?? 0;
  t.inputTokens += r.inputTokens ?? 0; t.outputTokens += r.outputTokens ?? 0;
  t.tokens += (r.inputTokens ?? 0) + (r.outputTokens ?? 0);
  t.cost += r.cost ?? 0; t.unpriced += r.unpriced ?? 0;
}

// aggregate(rows, by, day) → [{key, label, today, month}] — per-day rows
// of the current month summed by `by` (user | component | model): `month`
// everything, `today` the rows of `day`. Each sum: {requests, failed,
// tokens, inputTokens, outputTokens, cost, unpriced}.
export function aggregate(rows, by = 'user', day = today()) {
  const out = new Map();
  for (const r of rows ?? []) {
    const key = String(r?.[by] ?? '');
    let g = out.get(key);
    if (!g) {
      g = { key, label: by === 'user' ? personLabel(key) : by === 'component' ? componentLabel(key) : key, today: zero(), month: zero() };
      out.set(key, g);
    }
    add(g.month, r);
    if (r.day === day) add(g.today, r);
  }
  return [...out.values()];
}

// totalsOf(groups) → {today, month}: the sums over aggregate()'s groups.
export function totalsOf(groups) {
  const t = { today: zero(), month: zero() };
  for (const g of groups ?? []) { add(t.today, g.today); add(t.month, g.month); }
  return t;
}

// The usage table's columns: [id, header, value(group) → number|string].
export const USAGE_COLS = Object.freeze([
  ['label', '', (g) => g.label.toLowerCase()],
  ['todayTokens', 'tokens today', (g) => g.today.tokens],
  ['todayCost', 'cost today', (g) => g.today.cost],
  ['monthTokens', 'tokens this month', (g) => g.month.tokens],
  ['monthCost', 'cost this month', (g) => g.month.cost],
  ['requests', 'requests', (g) => g.month.requests],
]);

// sortGroups(groups, col, dir) → a sorted copy: by a column id of
// USAGE_COLS, 'desc' (the default for numbers) or 'asc'; ties by label.
export function sortGroups(groups, col = 'monthTokens', dir = col === 'label' ? 'asc' : 'desc') {
  const val = (USAGE_COLS.find((c) => c[0] === col) ?? USAGE_COLS[3])[2];
  const sign = dir === 'asc' ? 1 : -1;
  return [...(groups ?? [])].sort((a, b) => {
    const x = val(a), y = val(b);
    if (x !== y) return (x < y ? -1 : 1) * sign;
    return a.label.localeCompare(b.label);
  });
}

// costWords(sum) → "$1.20", or "$1.20 + 3 unpriced" when some requests used
// a model without a price.
export const costWords = (sum) => `${fmtCost(sum?.cost ?? 0)}${sum?.unpriced ? ` + ${sum.unpriced} unpriced` : ''}`;

// tokensTitle(sum) → "12,000 in · 3,400 out · 14 requests (1 failed)".
export function tokensTitle(sum) {
  const n = (v) => Number(v ?? 0).toLocaleString('en-US');
  return `${n(sum?.inputTokens)} in · ${n(sum?.outputTokens)} out · ${plural(sum?.requests ?? 0, 'request')}${sum?.failed ? ` (${sum.failed} failed)` : ''}`;
}

// ---- alerts (GET /alerts) ----

// setupAlert(alerts) → the "no LLM is set up" alert, or null: what the
// admin console's setup card stands on.
export const setupAlert = (alerts) => (alerts ?? []).find((a) => a?.kind === 'llm-setup') ?? null;
// isLLMAlert(a): one of the connector's (its setup card, a quota reached).
export const isLLMAlert = (a) => a?.kind === 'llm-setup' || a?.kind === 'llm-quota';
