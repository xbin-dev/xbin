// bx-netrules — the one vocabulary for organisation network sets (D54) so the
// admin console, the organisations tile, the shell's tile popover and the
// terminal scope picker never drift: rule kinds, parsing/formatting, labels,
// client-side shape hints (the server is authoritative), and the net-slot
// option list. Plain ES module, no deps (served like bx-multiselect.js).

export const RULE_KINDS = [
  { id: 'internet', label: 'all internet', icon: '🌐', prefix: 'internet', hasValue: false,
    help: 'every public address through the metered egress relay (no LAN)' },
  { id: 'internet-to', label: 'internet destination', icon: '→', prefix: 'internet:', hasValue: true,
    placeholder: 'api.stripe.com:443 · *.github.com · 203.0.113.0/24:443',
    help: 'a public host, hostname glob (one *), address or CIDR, optional :port — hostnames are DNS-pinned' },
  { id: 'lan', label: 'LAN range', icon: '🖧', prefix: 'lan:', hasValue: true,
    placeholder: '10.0.0.0/8', help: 'a private address or CIDR, optional :port' },
  { id: 'host', label: 'host networking', icon: '⚠', prefix: 'host', hasValue: false,
    help: 'the host’s own network stack — no relay, no filtering, no metering' },
  { id: 'provider', label: 'provider tile', icon: '⇢', prefix: 'provider:', hasValue: true,
    placeholder: 'apps/vpn · apps/*', help: 'a net-provider tile (path or glob) tiles may be bound through' },
];

export const SCOPE_ICON = { org: '🏢', internet: '🌐', host: '🖧', none: '⛔' };

/** parseRule('lan:10.0.0.0/8') → {kind:'lan', value:'10.0.0.0/8'} */
export function parseRule(str) {
  const s = String(str ?? '').trim().toLowerCase();
  if (s === 'internet') return { kind: 'internet', value: '' };
  if (s === 'host') return { kind: 'host', value: '' };
  if (s.startsWith('internet:')) return { kind: 'internet-to', value: s.slice(9) };
  if (s.startsWith('lan:')) return { kind: 'lan', value: s.slice(4) };
  if (s.startsWith('provider:')) return { kind: 'provider', value: s.slice(9) };
  return { kind: 'internet-to', value: s };
}

/** fmtRule({kind, value}) → the wire form ('' when the value is missing). */
export function fmtRule(r) {
  const k = RULE_KINDS.find((x) => x.id === r.kind);
  if (!k) return '';
  if (!k.hasValue) return k.prefix;
  const v = String(r.value ?? '').trim().toLowerCase();
  return v ? k.prefix + v : '';
}

const CIDR4 = /^(\d{1,3})(\.\d{1,3}){3}(\/\d{1,2})?(:\d{1,5})?$/;
const V6 = /^\[[0-9a-f:]+\](\/\d{1,3})?(:\d{1,5})?$/;
const HOST = /^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+(:\d{1,5})?$/;

function portProblem(str) {
  const m = /:(\d+)$/.exec(str);
  if (!m) return '';
  const p = Number(m[1]);
  return p >= 1 && p <= 65535 ? '' : 'port must be 1–65535';
}

/**
 * ruleProblem('lan:10.0.0.0') → a short hint, or '' when the shape looks
 * right. Client-side only — the server validates for real.
 */
export function ruleProblem(str) {
  const r = parseRule(str);
  const v = r.value;
  switch (r.kind) {
    case 'internet': case 'host': return '';
    case 'provider': return v && !/\s/.test(v) ? '' : 'name a tile path or glob (apps/vpn, apps/*)';
    case 'lan': {
      if (!v) return 'a private address or CIDR, e.g. 10.0.0.0/8';
      if (v.includes('*')) return 'LAN rules take addresses or CIDRs, not globs';
      if (!CIDR4.test(v) && !V6.test(v)) return 'expected a.b.c.d[/n][:port] or [v6]/n[:port]';
      return portProblem(v);
    }
    case 'internet-to': {
      if (!v) return 'a public host, glob, address or CIDR';
      if (v.includes(',')) return 'one destination per rule';
      if ((v.match(/\*/g) || []).length > 1) return 'a single * only';
      if (v.includes('*') && !HOST.test(v)) return 'globs look like *.example.com[:port]';
      if (CIDR4.test(v) || V6.test(v)) {
        if (/^(10\.|192\.168\.|127\.|169\.254\.|172\.(1[6-9]|2\d|3[01])\.)/.test(v)) return 'private ranges are LAN rules';
        return portProblem(v);
      }
      if (!HOST.test(v)) return 'expected host[:port], *.host, ip[:port] or cidr[:port]';
      return portProblem(v);
    }
  }
  return '';
}

/** ruleLabel('lan:10.0.0.0/8') → '🖧 LAN 10.0.0.0/8' */
export function ruleLabel(str) {
  const r = parseRule(str);
  switch (r.kind) {
    case 'internet': return '🌐 all internet';
    case 'host': return '⚠ host networking';
    case 'lan': return `🖧 LAN ${r.value}`;
    case 'provider': return `⇢ via ${r.value}`;
    default: return `→ ${r.value}`;
  }
}

/** setSummary(rules) → {labels, host, text} */
export function setSummary(rules) {
  const list = rules ?? [];
  const parts = list.map((r) => {
    const p = parseRule(r);
    switch (p.kind) {
      case 'internet': return 'all public internet';
      case 'host': return 'HOST networking';
      case 'lan': return p.value;
      case 'provider': return `via ${p.value}`;
      default: return `${p.value}${p.value.includes('*') || /[a-z]/.test(p.value.split(':')[0]) ? ' (DNS-pinned)' : ''}`;
    }
  });
  return { labels: list.map(ruleLabel), host: list.some((r) => parseRule(r).kind === 'host'), text: parts.join('; ') };
}

/** orgNetLabel(org) → 'org network (devs-net + office-lan)' */
export function orgNetLabel(org) {
  const sets = org?.netSets ?? [];
  return sets.length ? `org network (${sets.join(' + ')})` : 'org network (no sets attached)';
}

/**
 * netOptions({tile, org, providers, pending}) → [{id, label, title}] for a
 * net slot's picker. `org` is the owning org (with netSets/resolvedNet) or
 * null; `providers` are provider-tile paths; `pending` is the server's
 * pending row (its `options` carry the authoritative labels + `default`).
 */
export function netOptions({ org, providers = [], pending } = {}) {
  const byId = new Map((pending?.options ?? []).map((o) => [o.id, o]));
  const out = [];
  const serverLabel = (id, fallback) => byId.get(id)?.label ?? fallback;
  // Unbinding an org tile's net slot falls back to the org default (D54): the
  // server says so on a pending row; for a bound slot (no pending row) infer it
  // from the org's sets.
  const defaultOrg = pending ? pending.default === 'org' : !!(org && ((org.resolvedNet ?? []).length || org.netHost));
  const unbound = defaultOrg
    ? { id: '', label: `— default: ${orgNetLabel(org)} —`, title: (org?.resolvedNet ?? []).map(ruleLabel).join('\n') }
    : { id: '', label: '— unbound (no egress) —', title: '' };
  out.push(unbound);
  if (org) {
    out.push({ id: 'org', label: `${SCOPE_ICON.org} ${orgNetLabel(org)}`,
      title: serverLabel('org', (org.resolvedNet ?? []).map(ruleLabel).join('\n')) });
  }
  out.push({ id: 'internet', label: `${SCOPE_ICON.internet} internet`, title: serverLabel('internet', 'public internet through the relay') });
  out.push({ id: 'host', label: `${SCOPE_ICON.host} host`, title: serverLabel('host', 'share the host network (powerful)') });
  for (const p of providers) out.push({ id: p, label: `⇢ ${p}`, title: serverLabel(p, 'net provider tile') });
  if (org) out.push({ id: 'none', label: `${SCOPE_ICON.none} none — explicitly offline`, title: serverLabel('none', 'no egress') });
  out.push({ id: '__custom', label: 'custom…', title: 'lan:<cidr> or internet:<host|cidr>[:port]' });
  // Mark what the org's sets refuse (the server's label says so).
  for (const o of out) {
    if (/not covered/.test(byId.get(o.id)?.label ?? '')) o.label += ' — not covered';
  }
  return out;
}
