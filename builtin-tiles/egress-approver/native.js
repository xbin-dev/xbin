// native.js — the egress approver as a native approval queue for the xbin app
// (docs/frontend-kit.md). Same backend calls and the same behaviour as
// index.html: a single-flight poll of GET /state every 1.2 s,
// approve/deny/forget moved locally at once and reconciled by the next poll,
// reverse DNS + RDAP from GET /detail (cached across polls). Whois is a pushed
// screen instead of an inline box. The runtime diffs every render, so an
// unchanged poll sends nothing to the app and a ticking "38s ago" one set —
// the page's "render only when the signature changed" trick isn't needed.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi, jbody } from '/vendor/bx-kit.js';
import { fmtBytes, ago, norm } from './fmt.js';

let st = { pending: [], approved: [], denied: [], clients: 0, egressReady: false };
let loaded = false;        // a first /state arrived
let whois = null;          // {ip, lines} while the detail screen is open
let deniedCollapsed = true; // the page's "denied" <details> starts closed
const cache = {};          // ip -> whois lines (survives polls)

async function act(ip, action) {          // move locally at once, then reconcile
  const v = [...st.pending, ...st.approved, ...st.denied].find((x) => x.ip === ip) || { ip, rdns: '' };
  for (const k of ['pending', 'approved', 'denied']) st[k] = st[k].filter((x) => x.ip !== ip);
  if (action === 'approve') st.approved.unshift(v);
  else if (action === 'deny') st.denied.unshift(v);
  paint();
  try { await selfApi(`/${action}`, jbody({ ip }, 'POST')); } catch { /* the next poll reconciles */ }
  refresh();
}
async function openWhois(ip) {
  whois = { ip, lines: cache[ip] ?? null }; paint();
  if (cache[ip]) return;
  try {
    const d = await selfApi(`/detail?ip=${encodeURIComponent(ip)}`);
    const w = d.rdap || {};
    cache[ip] = [['reverse dns', d.rdns || '(none)'], ['network', w.name], ['range', w.cidr],
      ['org', w.org], ['country', w.country], ['handle', w.handle], ['error', w.error]].filter(([, x]) => x);
  } catch { cache[ip] = [['error', 'lookup failed']]; }
  if (whois?.ip === ip) whois.lines = cache[ip];
  paint();
}

const pendingRow = (v) => html`
  <row title=${v.ip} mono="title" tone="warn" nav subtitle=${v.rdns || 'resolving…'}
       detail=${`↑${fmtBytes(v.bytesOut)} · ${ago(v.lastSeen)}`} @tap=${() => openWhois(v.ip)}>
    <actions>
      <button role="primary" icon="check" @tap=${() => act(v.ip, 'approve')}>approve</button>
      <button role="destructive" icon="xmark" @tap=${() => act(v.ip, 'deny')}>deny</button>
    </actions>
  </row>`;
const approvedRow = (v) => html`
  <row title=${v.ip} mono="title" tone="ok" subtitle=${v.rdns}
       detail=${`↑${fmtBytes(v.bytesOut)} ↓${fmtBytes(v.bytesIn)} · ${ago(v.lastSeen)}`}>
    <actions><button role="destructive" @tap=${() => act(v.ip, 'forget')}>revoke</button></actions>
  </row>`;
const deniedRow = (v) => html`
  <row title=${v.ip} mono="title" subtitle=${v.rdns}>
    <actions><button @tap=${() => act(v.ip, 'forget')}>un-deny</button></actions>
  </row>`;

const main = () => html`
  <screen title="Egress Approver" style="list"
          subtitle=${loaded ? `${st.clients ?? 0} client${st.clients === 1 ? '' : 's'} · egress ${st.egressReady ? 'ready' : 'unbound'}` : 'loading…'}>
    <section title="pending" badge=${String(st.pending.length)}>
      ${!loaded ? html`<progress label="loading…"/>` : st.pending.length ? repeat(st.pending, (v) => v.ip, pendingRow)
        : html`<empty text=${st.clients ? 'no new destinations — all quiet' : 'bind a component’s net to this tile to start gating'}/>`}
    </section>
    <section title="approved" badge=${String(st.approved.length)}>
      ${st.approved.length ? repeat(st.approved, (v) => v.ip, approvedRow) : loaded ? html`<empty text="nothing approved yet"/>` : nothing}
    </section>
    ${st.denied.length ? html`<section title="denied" collapsible collapsed=${deniedCollapsed}
        @toggle=${(e) => { deniedCollapsed = e.collapsed; }}>${repeat(st.denied, (v) => v.ip, deniedRow)}</section>` : nothing}
  </screen>`;
const whoisScreen = (w) => html`
  <screen title=${w.ip} style="list">
    <section title="whois">
      ${w.lines ? w.lines.map(([k, x]) => html`<row title=${k} detail=${String(x)} mono="detail"/>`)
        : html`<progress label="looking up…"/>`}
    </section>
  </screen>`;
const paint = () => render(html`
  <nav @pop=${() => { whois = null; paint(); }}>
    ${main()}
    ${whois ? whoisScreen(whois) : nothing}
  </nav>`);

let inFlight = false;       // single flight, as the page: a slow /state never piles up
async function refresh() {
  if (inFlight) return;
  inFlight = true;
  try { st = norm(await selfApi('/state')); loaded = true; paint(); } catch { /* transient; next tick */ } finally { inFlight = false; }
}
paint();   // at once ("loading…"): the app wants a tree before the backend answers
(function loop() {           // self-scheduling, never overlaps; slower while off screen
  refresh().finally(() => setTimeout(loop, document.visibilityState === 'visible' ? 1200 : 5000));
})();
