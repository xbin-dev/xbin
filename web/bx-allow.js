/**
 * bx-allow.js — the allowance grammar for browsers (docs/auth.md "Delegated
 * approval", D26/D32): what the admins of an organisation may approve on
 * their own tiles without a workspace admin. Permission sets and an org's
 * extra `allow` entries are lists of these strings:
 *
 *   tile:<pattern>[@<role>]            iface:<service>[@<tile-glob>[#<instance>]]
 *   res:<glob>[@<role>]                cap:<glob>       gpu:<glob>
 *   ingress:host:<glob>  ingress:zone:<glob>  ingress:listen:<port|lo-hi>
 *   net:internet | net:host | net:internet:<…> | net:lan:<…> | net:provider:<…>
 *
 * One shared parser / formatter / validator / describer, so the admin
 * console's permission-set editor, the org card and the organisations tile
 * agree with the server (internal/users/orgs.go parseAllowEntry). The server
 * validates for real — this catches the typo before the round trip and can
 * say in words what an entry does.
 */
import { ruleProblem, ruleLabel } from '/vendor/bx-netrules.js';

// The roles an entry may cap a delegation at (a bare entry delegates ANY
// role — prefer the cap). Manifest-declared custom roles are allowed too.
export const ROLE_CAPS = ['reader', 'writer', 'admin'];

// The reserved capability classes, with what each one means — the label and
// description every approval surface shows (a pending `cap:` row has no
// component to look a description up on). Anything else is a glob.
export const CAP_INFO = {
  'net-admin': { label: 'network admin (net-provider tile)',
    desc: 'keeps CAP_NET_ADMIN / NET_RAW / NET_BIND_SERVICE inside the tile\'s own network namespace — routers, firewalls, VPNs' },
  'containers': { label: 'container host',
    desc: 'keeps user-namespace capabilities and a minimal seccomp floor so rootless podman/docker runs inside the tile' },
  'open-links': { label: 'open links in new tabs',
    desc: 'the tile\'s frontend may open browser tabs/windows that leave its sandbox (target="_blank", window.open) — full-origin pages at a URL the tile chose, so a hostile tile could open a look-alike page; approve for tiles you trust' },
};
export const KNOWN_CAPS = Object.keys(CAP_INFO);
// capInfo('cap:open-links' | 'open-links') → {label, desc} | null
export const capInfo = (t) => CAP_INFO[String(t ?? '').replace(/^cap:/, '')] ?? null;

// One row of the typed editor: {kind, value, role?, provider?, instance?}.
// `value` is the kind's main pattern (tile path, service, glob, port range,
// net rule, or the raw entry).
export const ALLOW_KINDS = [
  { id: 'tile', icon: '▣', label: 'Use a tile',
    help: 'their tiles may be granted a role on this tile (a `uses: [{target, role}]` request)',
    placeholder: 'tile path or pattern — apps/llm-gw, apps/infra-*' },
  { id: 'iface', icon: '⇢', label: 'Bind an interface',
    help: 'their tiles may bind an interface slot of this service — optionally pinned to one provider tile and instance',
    placeholder: 'service — openai, feed, prometheus' },
  { id: 'res', icon: '▤', label: 'Use a resource',
    help: 'their tiles may be granted a role on resources (kv, sqlite, files, pub/sub) whose scope/name matches',
    placeholder: 'scope/name pattern — apps/warehouse/*' },
  { id: 'cap', icon: '✦', label: 'Hold a capability',
    help: 'their tiles may be granted a capability class; the xbin family is never delegable',
    placeholder: 'containers, net-admin, or a glob' },
  { id: 'gpu', icon: '▦', label: 'Use a GPU',
    help: 'their tiles may be granted GPUs matching the pattern',
    placeholder: '* or a device pattern' },
  { id: 'ingress:host', icon: '🌐', label: 'Publish a hostname',
    help: 'their tiles may be published at hostnames matching the glob',
    placeholder: 'app.example.com or *.example.com' },
  { id: 'ingress:zone', icon: '🌐', label: 'Publish under a zone',
    help: 'their tiles may claim a wildcard zone matching the glob',
    placeholder: '*.apps.example.com' },
  { id: 'ingress:listen', icon: '⚓', label: 'Open a host port',
    help: 'their tiles may be published on a host port or range',
    placeholder: '8443 or 8000-8099' },
  { id: 'net', icon: '🖧', label: 'Network reach',
    help: 'prefer a NETWORK SET: it is the ceiling, the allowance and the default at once; a plain net: entry wider than the org\'s sets is refused by the ceiling anyway',
    placeholder: 'internet · lan:10.0.0.0/8 · provider:apps/vpn' },
  { id: 'raw', icon: '⌨', label: 'Raw entry',
    help: 'typed as-is; the server validates it',
    placeholder: 'class:value' },
];

export function allowKind(id) { return ALLOW_KINDS.find((k) => k.id === id) ?? ALLOW_KINDS[ALLOW_KINDS.length - 1]; }

// parseAllow: entry string → editor row. Anything the grammar doesn't cover
// comes back as a `raw` row, so editing a set never loses an entry.
export function parseAllow(str) {
  const s = String(str ?? '').trim();
  const i = s.indexOf(':');
  if (i < 1 || i === s.length - 1) return { kind: 'raw', value: s };
  const cls = s.slice(0, i), rest = s.slice(i + 1);
  const cutRole = (v) => { const j = v.indexOf('@'); return j < 0 ? [v, ''] : [v.slice(0, j), v.slice(j + 1)]; };
  switch (cls) {
    case 'tile': case 'res': { const [value, role] = cutRole(rest); return { kind: cls, value, role }; }
    case 'cap': case 'gpu': case 'net': return { kind: cls, value: rest };
    case 'iface': {
      const [value, prov] = cutRole(rest);
      if (!prov) return { kind: 'iface', value, provider: '', instance: '' };
      const h = prov.indexOf('#');
      return h < 0 ? { kind: 'iface', value, provider: prov, instance: '' }
        : { kind: 'iface', value, provider: prov.slice(0, h), instance: prov.slice(h + 1) };
    }
    case 'ingress': {
      const j = rest.indexOf(':');
      const sub = j < 0 ? rest : rest.slice(0, j), value = j < 0 ? '' : rest.slice(j + 1);
      if (sub === 'host' || sub === 'zone' || sub === 'listen') return { kind: 'ingress:' + sub, value };
      return { kind: 'raw', value: s };
    }
    default: return { kind: 'raw', value: s };
  }
}

// fmtAllow: editor row → entry string ('' when the row is empty).
export function fmtAllow(r) {
  const v = (r?.value ?? '').trim();
  const role = (r?.role ?? '').trim();
  switch (r?.kind) {
    case 'tile': case 'res': return v ? `${r.kind}:${v}${role ? '@' + role : ''}` : '';
    case 'iface': {
      const p = (r.provider ?? '').trim(), inst = (r.instance ?? '').trim();
      return v ? `iface:${v}${p ? '@' + p + (inst ? '#' + inst : '') : ''}` : '';
    }
    case 'cap': case 'gpu': case 'net': return v ? `${r.kind}:${v}` : '';
    case 'ingress:host': case 'ingress:zone': case 'ingress:listen': return v ? `${r.kind}:${v}` : '';
    case 'raw': return v;
    default: return '';
  }
}

const PATTERN_OK = /^[^\s,:@#]+$/;      // a path/glob: no whitespace, separators reserved by the grammar
const ROLE_OK = /^[a-z][a-z0-9-]*$/;    // a manifest role name
const SERVICE_OK = /^[A-Za-z0-9][A-Za-z0-9._*-]*$/;

// allowProblem: '' when the row would pass the server's parser, else a short
// reason — mirrors parseAllowEntry (internal/users/orgs.go) case by case.
export function allowProblem(r) {
  const v = (r?.value ?? '').trim();
  const role = (r?.role ?? '').trim();
  const badRole = role && !ROLE_CAPS.includes(role) && !ROLE_OK.test(role);
  switch (r?.kind) {
    case 'tile':
      if (!v) return 'tile path or pattern required';
      if (!PATTERN_OK.test(v)) return 'a path or glob — no spaces, : @ #';
      if (badRole) return `@${role} is not a role`;
      return '';
    case 'res':
      if (!v) return 'scope/name pattern required';
      if (!PATTERN_OK.test(v)) return 'a scope/name glob — no spaces, : @ #';
      if (badRole) return `@${role} is not a role`;
      return '';
    case 'iface': {
      const p = (r.provider ?? '').trim(), inst = (r.instance ?? '').trim();
      if (!v) return 'service required';
      if (!SERVICE_OK.test(v)) return 'a service name (letters, digits, . _ -)';
      if (p && !PATTERN_OK.test(p)) return 'provider: a tile path or glob';
      if (inst && !p) return 'an instance needs a provider tile';
      if (inst && /[\s,@]/.test(inst)) return 'instance: a name or glob';
      return '';
    }
    case 'cap':
      if (!v) return 'capability required';
      if (v.startsWith('xbin')) return 'the xbin family is never delegable';
      if (!PATTERN_OK.test(v)) return 'a capability class or glob';
      return '';
    case 'gpu':
      if (!v) return 'device pattern required (* for any)';
      if (!PATTERN_OK.test(v)) return 'a device pattern or glob';
      return '';
    case 'ingress:host': case 'ingress:zone':
      if (!v) return 'hostname glob required';
      if (!/^[A-Za-z0-9*][A-Za-z0-9.*-]*$/.test(v)) return 'a hostname or *.glob';
      return '';
    case 'ingress:listen': {
      if (!v) return 'port or lo-hi range required';
      const m = /^\s*(\d{1,5})\s*(?:-\s*(\d{1,5})\s*)?$/.exec(v);
      if (!m) return 'a port or lo-hi range';
      const lo = Number(m[1]), hi = Number(m[2] ?? m[1]);
      if (lo < 1 || hi > 65535 || lo > hi) return 'ports are 1–65535, lo ≤ hi';
      return '';
    }
    case 'net':
      if (!v) return 'a network rule (internet, lan:…, provider:…)';
      return ruleProblem(v);
    case 'raw': {
      if (!v) return 'entry required';
      if (/\s|,/.test(v)) return 'one entry — no spaces or commas';
      if (v === 'xbin' || v.startsWith('xbin:')) return 'the xbin family is never delegable';
      const p = parseAllow(v);
      if (p.kind === 'raw') return 'unknown class — tile: iface: res: cap: gpu: net: ingress:host|zone|listen:';
      return allowProblem(p);
    }
    default: return 'pick what to allow';
  }
}

// describeAllow: an entry (string or row) in plain words, completing the
// sentence "org admins may … on their own tiles".
export function describeAllow(x) {
  const r = typeof x === 'string' ? parseAllow(x) : (x ?? {});
  const v = (r.value ?? '').trim() || '…';
  const as = (r.role ?? '').trim() ? `as ${r.role.trim()}` : 'in any role';
  switch (r.kind) {
    case 'tile': return `let their tiles use ${v} ${as}`;
    case 'iface': {
      const p = (r.provider ?? '').trim(), inst = (r.instance ?? '').trim();
      return `bind a "${v}" interface slot to ${p ? p + (inst ? ` (instance ${inst})` : '') : 'any provider'}`;
    }
    case 'res': return `let their tiles use resources matching ${v} ${as}`;
    case 'cap': return `let their tiles hold the ${capInfo(v)?.label ?? v} capability`;
    case 'gpu': return `let their tiles use GPUs matching ${v}`;
    case 'ingress:host': return `publish their tiles at hostnames matching ${v}`;
    case 'ingress:zone': return `publish their tiles under the ${v} zone`;
    case 'ingress:listen': return `publish their tiles on host port${/-/.test(v) ? 's' : ''} ${v}`;
    case 'net': return `bind ${ruleLabel(v)} as their tiles' network`;
    default: return v;
  }
}
