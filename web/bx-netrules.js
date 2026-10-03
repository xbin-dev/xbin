// bx-netrules — the one vocabulary for organisation network sets (D54) so the
// admin console, the organisations tile, the shell's tile popover and the
// terminal scope picker never drift: rule kinds, parsing/formatting, labels,
// client-side shape hints (the server is authoritative), and the net-slot
// option list. Plain ES module, no deps (served like bx-multiselect.js).
//
// Glyphs (D184): words carry no emoji. A place that can draw an icon draws
// the glyph named here (/vendor/bx-icons.js) before the words: `glyph` on a
// rule kind, `icon` on a net-slot option, scopeGlyph() for a scope, and
// ruleGlyph() for a rule; an <option> shows the words alone. RULE_KINDS'
// `icon`, SCOPE_ICON, SET_ICON and scopeIcon() are what older admin
// consoles, organisations tiles and shells print before a label: they are
// empty now, so those print the words alone.

export const RULE_KINDS = [
  { id: 'internet', label: 'all internet', icon: '', glyph: 'globe', prefix: 'internet', hasValue: false,
    help: 'every public address through the metered egress relay (no LAN)' },
  { id: 'internet-to', label: 'internet destination', icon: '', glyph: 'arrow-right', prefix: 'internet:', hasValue: true,
    placeholder: 'api.stripe.com:443 · *.github.com · 203.0.113.0/24:443',
    help: 'a public host, hostname glob (one *), address or CIDR, optional :port — hostnames are DNS-pinned' },
  { id: 'lan', label: 'LAN range', icon: '', glyph: 'network', prefix: 'lan:', hasValue: true,
    placeholder: '10.0.0.0/8', help: 'a private address or CIDR, optional :port' },
  { id: 'host', label: 'host networking', icon: '', glyph: 'warning', prefix: 'host', hasValue: false,
    help: 'the host’s own network stack — no relay, no filtering, no metering' },
  { id: 'provider', label: 'provider tile', icon: '', glyph: 'plug', prefix: 'provider:', hasValue: true,
    placeholder: 'apps/vpn · apps/*', help: 'a net-provider tile (path or glob) tiles may be bound through' },
];

/** The glyph of a terminal scope or a binding ref (bx-icons.js names). */
export const SCOPE_GLYPH = Object.freeze({ org: 'org', personal: 'person', internet: 'globe', host: 'network', none: 'error' });
/** A named network set (D65) as a terminal scope or a binding ref: 'set:<name>'. */
export const SET_GLYPH = 'link';
/** scopeGlyph('set:infra-net') → 'link'; scopeGlyph('org') → 'org'; unknown → ''. */
export function scopeGlyph(id) {
  const s = String(id ?? '');
  return s.startsWith('set:') ? SET_GLYPH : (Object.hasOwn(SCOPE_GLYPH, s) ? SCOPE_GLYPH[s] : '');
}
/** Older callers print these before a label: empty, so the words stand alone (D184). */
export const SCOPE_ICON = Object.freeze({ org: '', personal: '', internet: '', host: '', none: '' });
export const SET_ICON = '';
export const scopeIcon = () => '';
/** scopeLabel('set:infra-net') → 'net set: infra-net' (the server labels the rest). */
export function scopeLabel(id) {
  const s = String(id ?? '');
  return s.startsWith('set:') ? `net set: ${s.slice(4)}` : s;
}

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

// addrProblem checks the parts a regex can't: octet range and prefix length.
function addrProblem(str) {
  const v4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})(?:\/(\d{1,2}))?/.exec(str);
  if (v4) {
    if (v4.slice(1, 5).some((o) => Number(o) > 255)) return 'octets are 0–255';
    if (v4[5] !== undefined && Number(v4[5]) > 32) return 'prefix length is 0–32';
    return '';
  }
  const v6 = /^\[[0-9a-f:]+\](?:\/(\d{1,3}))?/.exec(str);
  if (v6 && v6[1] !== undefined && Number(v6[1]) > 128) return 'prefix length is 0–128';
  return '';
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
      return addrProblem(v) || portProblem(v);
    }
    case 'internet-to': {
      if (!v) return 'a public host, glob, address or CIDR';
      if (v.includes(',')) return 'one destination per rule';
      if ((v.match(/\*/g) || []).length > 1) return 'a single * only';
      if (v.includes('*') && !HOST.test(v)) return 'globs look like *.example.com[:port]';
      if (CIDR4.test(v) || V6.test(v)) {
        if (/^(10\.|192\.168\.|127\.|169\.254\.|172\.(1[6-9]|2\d|3[01])\.)/.test(v)) return 'private ranges are LAN rules';
        return addrProblem(v) || portProblem(v);
      }
      if (!HOST.test(v)) return 'expected host[:port], *.host, ip[:port] or cidr[:port]';
      return portProblem(v);
    }
  }
  return '';
}

/** ruleLabel('lan:10.0.0.0/8') → 'LAN 10.0.0.0/8' (words: ruleGlyph() is its glyph) */
export function ruleLabel(str) {
  const r = parseRule(str);
  switch (r.kind) {
    case 'internet': return 'all internet';
    case 'host': return 'host networking';
    case 'lan': return `LAN ${r.value}`;
    case 'provider': return `via ${r.value}`;
    default: return `to ${r.value}`;
  }
}
/** ruleGlyph('lan:10.0.0.0/8') → 'network': the rule kind's glyph. */
export const ruleGlyph = (str) => RULE_KINDS.find((k) => k.id === parseRule(str).kind)?.glyph ?? '';

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
 * bindPreselect(pending) → the option a bind prompt's picker starts on for a
 * pending row of GET /bindings (its `options` in server order). A
 * `sandbox-net` class starts on `none`: its first unblocked option is
 * usually `internet`, and an approver who clicks through must not grant a
 * manager's sandboxes the internet. Any other slot starts on its first
 * unblocked option. '' when there is nothing to start on.
 */
export function bindPreselect(pending) {
  const opts = pending?.options ?? [];
  if (pending?.kind === 'sandbox-net') return opts.some((o) => o.id === 'none' && !o.blocked) ? 'none' : '';
  return opts.find((o) => !o.blocked)?.id ?? '';
}

/**
 * blockedTitle(option) → why a picker greys out a server-blocked option,
 * read off its server label: a set that says host is never a sandbox
 * class's network; binding a set is a workspace admin's act (D65); the
 * personal owner's allowance (D88); otherwise the owning org's network sets
 * (D54).
 */
export function blockedTitle(o) {
  const l = o?.label ?? '';
  if (/says host/.test(l)) return 'a sandbox class can\'t reach the host';
  if (/workspace admins only/.test(l)) return 'binding a network set is a workspace admin\'s act';
  if (/outside your network allowance/.test(l)) return 'outside your network allowance';
  if (/not bindable/.test(l)) return 'a provider-only set can\'t be bound';
  return 'refused by the owning org\'s network sets';
}

/**
 * netOptions({org, providers, pending, options}) → [{id, label, title,
 * disabled?, icon?}] for a net slot's picker (icon: the scope's glyph, for a
 * picker that can draw one; an <option> shows the label). `org` is the owning org (with
 * netSets/resolvedNet) or null; `providers` are provider-tile paths;
 * `options` is the server's option list for the slot (GET /bindings
 * netOptions[comp] — bound or not), else `pending` is its pending row (its
 * `options` carry the authoritative labels, `default` and `blocked`). Named
 * sets (D65) come only from the server list — a workspace-admin act, and
 * the server says which are blocked for this caller or uncovered here. A
 * choice the org's network sets refuse comes back `disabled` (and labelled
 * "not covered") so a picker cannot submit it — the server would answer
 * 400 and a <select> left on the refused value reads as a success.
 *
 * `sandbox: true` builds a sandbox-net slot's picker instead (a sandbox
 * manager's network class, D120 — docs/isolation.md §Network egress;
 * `options` = GET /bindings sandboxNetOptions[comp]): unbound means no network — there
 * is no org or personal default — and host networking and provider tiles
 * are never offered.
 */
export function netOptions({ org, providers = [], pending, options, sandbox = false } = {}) {
  const list = options ?? pending?.options ?? [];
  const byId = new Map(list.map((o) => [o.id, o]));
  const out = [];
  const serverLabel = (id, fallback) => byId.get(id)?.label ?? fallback;
  // Coverage of the two builtins without a pending row (the slot is bound and
  // the picker offers a re-bind): the org card's live reach says so.
  const hasSets = !!(org && ((org.resolvedNet ?? []).length || org.netHost || (org.netSets ?? []).length));
  // why a choice is refused ('' = it isn't): outside the org's sets, or —
  // for a personal tile's owner — outside their own allowance (D88)
  const refused = (id) => {
    const o = byId.get(id);
    if (o) {
      if (/outside your network allowance/.test(o.label ?? '')) return 'outside your allowance';
      return o.blocked || /not covered/.test(o.label ?? '') ? 'not covered' : '';
    }
    if (!hasSets) return '';
    if (id === 'host') return org.netHost ? '' : 'not covered';
    if (id === 'internet') return !(org.resolvedNet ?? []).includes('internet') && !org.netHost ? 'not covered' : '';
    return '';
  };
  // A personal tile (D88): the server offers `personal` — the owner's
  // personal network — labelled with its sets (or "none attached").
  const personal = byId.get('personal');
  const personalSets = !!personal && !/none attached/.test(personal.label ?? '');
  // Unbinding an org tile's net slot falls back to the org default (D54): the
  // server says so on a pending row; for a bound slot (no pending row) infer it
  // from the org's sets.
  const defaultOrg = !sandbox && (pending ? pending.default === 'org' : !!(org && ((org.resolvedNet ?? []).length || org.netHost)));
  const defaultPersonal = !sandbox && (pending ? pending.default === 'personal' : personalSets);
  const unbound = sandbox ? { id: '', label: '— unbound: no network —', title: 'these sandboxes get no network until the class is bound' }
    : defaultOrg
    ? { id: '', label: `— default: ${orgNetLabel(org)} —`, title: (org?.resolvedNet ?? []).map(ruleLabel).join('\n') }
    : defaultPersonal ? { id: '', label: '— default: personal network —', title: personal?.label ?? '' }
      : { id: '', label: '— unbound (no egress) —', title: '' };
  out.push(unbound);
  if (personal) out.push({ id: 'personal', label: 'personal network', title: personal.label, disabled: !personalSets, icon: SCOPE_GLYPH.personal });
  if (org) {
    out.push({ id: 'org', label: orgNetLabel(org),
      title: serverLabel('org', (org.resolvedNet ?? []).map(ruleLabel).join('\n')), icon: SCOPE_GLYPH.org });
  }
  for (const o of list) {
    if (!String(o.id).startsWith('set:')) continue;
    const l = o.label ?? '';
    const why = /workspace admins only/.test(l) ? 'workspace admins only' : /not covered/.test(l) ? 'not covered'
      : /not bindable/.test(l) ? 'not bindable' : /says host/.test(l) ? 'says host' : '';
    out.push({ id: o.id, label: `${scopeLabel(o.id)}${why ? ` — ${why}` : ''}`, title: l, disabled: !!o.blocked, set: true, icon: SET_GLYPH });
  }
  out.push({ id: 'internet', label: 'internet', title: serverLabel('internet', 'public internet through the relay'), icon: SCOPE_GLYPH.internet });
  if (!sandbox) {
    out.push({ id: 'host', label: 'host', title: serverLabel('host', 'share the host network (powerful)'), icon: SCOPE_GLYPH.host });
    for (const p of providers) out.push({ id: p, label: `via ${p}`, title: serverLabel(p, 'net provider tile'), icon: 'plug' });
  }
  if (sandbox) out.push({ id: 'none', label: 'none — no network, decided', title: serverLabel('none', 'no network'), icon: SCOPE_GLYPH.none });
  else if (org || personalSets) out.push({ id: 'none', label: 'none — explicitly offline', title: serverLabel('none', 'no egress'), icon: SCOPE_GLYPH.none });
  out.push({ id: '__custom', label: 'custom…', title: sandbox
    ? 'lan:<cidr>, internet:<host|cidr>[:port], or set:<name> (a network set without host — workspace admins)'
    : 'lan:<cidr>, internet:<host|cidr>[:port], or set:<name> (a network set — workspace admins)' });
  // Mark what the org's sets refuse (the server's label says so) and keep it
  // out of reach; the set rows already carry their own reason.
  for (const o of out) {
    const why = o.id && o.id !== '__custom' && !o.set ? refused(o.id) : '';
    if (why) { o.label += ` — ${why}`; o.disabled = true; }
  }
  return out;
}
