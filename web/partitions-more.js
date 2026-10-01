// web/partitions-more.js — the partitions page's other sections
// (web/partitions-page.js): consents (with the workspace policy
// partitionConsent on), personal binds, notices and the person's egress
// ledger. Every value is bound as text (lit escapes it).
import { html, nothing } from '/vendor/lit-all.min.js';
import { partitionedTiles, ledgerTotals, ledgerKind, timeText, plural } from '/vendor/partitions-kit.js';

const said = (st) => html`${st.err ? html`<p class="err" role="alert">${st.err}</p>` : nothing}${st.done ? html`<p class="done">${st.done}</p>` : nothing}`;

const pick = (host, key, field, list, label) => html`<select name=${field} aria-label=${label}
    @change=${(e) => host.set(key, { [field]: e.target.value, err: '' })}>
    <option value="" ?selected=${!host.ui(key)[field]}>${label}…</option>
    ${list.map((x) => html`<option value=${x} ?selected=${host.ui(key)[field] === x}>${x}</option>`)}
  </select>`;

// ---- consents: "tile Z may use my data in tile X" (05 §2, PD-13) ----

export function consentsSection(host, m) {
  const c = m.consents;
  if (!m.people) return nothing;
  if (c.error) {
    return html`<section id="consents"><h2>Consents</h2><p class="empty">${c.error}</p></section>`;
  }
  const off = host.readOnly();
  const kept = c.consents ?? [];
  if (!c.policy) {
    return html`<section id="consents"><h2>Consents</h2>
      <p class="lead small">Your workspace doesn't ask you: a partitioned tile holding a grant on another uses the data of everyone
        who can read both, and its code — and whoever can change it — reads and writes yours there. An admin can make the
        workspace ask each person first (the workspace policy <i>partitionConsent</i>).</p>
      ${kept.length ? html`<p class="small muted">Your consents are kept, unused, until the policy is back on:</p>
        ${consentTable(host, kept, off)}` : nothing}
    </section>`;
  }
  const asked = c.asked ?? [], tiles = partitionedTiles(m);
  const key = 'consent:new', st = host.ui(key);
  return html`<section id="consents"><h2>Consents <span class="n">${kept.length}</span></h2>
    <p class="lead small">Your workspace asks you before another partitioned tile uses your data in one: without your consent, its
      calls into your data are refused. Consenting lets that tile's code — and whoever can change it — read and write your data there.</p>
    ${asked.map((a) => askedCard(host, a, off))}
    ${kept.length ? consentTable(host, kept, off) : html`<p class="empty">You haven't let any tile use your data in another.</p>`}
    <div class="card" data-consent-new>
      <div class="row">Let ${pick(host, key, 'from', tiles, 'a tile')} use my data in ${pick(host, key, 'to', tiles, 'this tile')}
        <button class="primary" data-act="consent" ?disabled=${off || !!st.busy} @click=${() => host.consent(st.from, st.to, true, key)}>${st.busy ? 'Allowing…' : 'Allow'}</button></div>
      ${said(st)}
    </div>
  </section>`;
}

function askedCard(host, a, off) {
  // its own UI key: the consent's row (consentTable) shows Take back, not this Allow's answer
  const key = `asked:${a.from}→${a.to}`, st = host.ui(key);
  return html`<div class="card attn" data-asked="${a.from}→${a.to}">
    <p><code>${a.from}</code> asks for your <code>${a.to}</code> data <span class="muted small">(refused ${timeText(a.at)}; asked once a day at most)</span></p>
    ${st.done ? nothing : html`<div class="acts"><button class="primary" data-act="allow-asked" ?disabled=${off || !!st.busy}
      @click=${() => host.consent(a.from, a.to, true, key)}>${st.busy ? 'Allowing…' : 'Allow'}</button>
      <span class="muted small">or ignore it: its calls stay refused</span></div>`}
    ${said(st)}
  </div>`;
}

function consentTable(host, list, off) {
  return html`<table data-consents><thead><tr><th>this tile</th><th>may use your data in</th><th>since</th><th></th></tr></thead><tbody>
    ${list.map((c) => {
      const key = `consent:${c.from}→${c.to}`, st = host.ui(key);
      return html`<tr data-consent="${c.from}→${c.to}"><td class="mono">${c.from}</td><td class="mono">${c.to}</td><td>${timeText(c.at)}</td>
        <td class="n">${st.done ? html`<span class="done">${st.done}</span>` : html`<button class="danger" data-act="revoke" ?disabled=${off || !!st.busy}
          @click=${() => host.consent(c.from, c.to, false)}>${st.busy ? 'Taking back…' : 'Take back'}</button>`}
          ${st.err ? html`<div class="err">${st.err}</div>` : nothing}</td></tr>`;
    })}</tbody></table>`;
}

// ---- personal binds: your own tile, wired into your own partition (05 §3) ----

export function bindsSection(host, m) {
  if (!m.people) return nothing;
  const off = host.readOnly();
  const binds = m.binds;
  const requesters = partitionedTiles(m), providers = [...m.me.owned].sort();
  const key = 'bind:new', st = host.ui(key);
  const form = m.me.admin
    ? html`<p class="muted small">An admin's bind is always a global bind — every person's partition sees it (the admin console's wiring view,
        <code>bx bind</code>); personal binds are for people who aren't admins.</p>`
    : html`<div class="card" data-bind-new>
        <div class="row">Bind my tile ${pick(host, key, 'provider', providers, 'my tile')} into my partition of
          ${pick(host, key, 'requester', requesters, 'a partitioned tile')} as its slot
          <input class="mono" name="slot" placeholder="slot, e.g. mcp" autocomplete="off" spellcheck="false" .value=${st.slot ?? ''}
            @input=${(e) => host.set(key, { slot: e.target.value, err: '' })}>
          <button class="primary" data-act="bind" ?disabled=${off || !!st.busy} @click=${() => host.bindAdd()}>${st.busy ? 'Binding…' : 'Bind'}</button></div>
        ${providers.length ? nothing : html`<p class="muted small">You own no tile personally yet: a personal bind wires a tile you own into your own partition.</p>`}
        ${said(st)}
      </div>`;
  return html`<section id="binds"><h2>Personal binds <span class="n">${binds.length}</span></h2>
    <p class="lead small">A tile you own, wired into your own partition of a partitioned tile's multi slot: only your partition sees it
      and calls it — not other people's, not the global instance. It holds while you own the tile.</p>
    ${m.bindsError ? html`<p class="empty">${m.bindsError}</p>` : binds.length ? html`<table data-binds><thead><tr><th>partitioned tile</th><th>slot</th><th>your tile</th><th>since</th><th></th></tr></thead><tbody>
      ${binds.map((b) => {
        const k = `bind:${b.id}`, bs = host.ui(k);
        return html`<tr data-bind=${b.id}><td class="mono">${b.requester}</td><td class="mono">${b.slot}</td><td class="mono">${b.provider}</td>
          <td>${timeText(b.at)}${b.live ? nothing : html`<div class="warn small">not in effect: ${b.why || 'no longer holds'}</div>`}</td>
          <td class="n">${bs.done ? html`<span class="done">${bs.done}</span>` : html`<button class="danger" data-act="unbind" ?disabled=${off || !!bs.busy}
            @click=${() => host.bindRemove(b)}>${bs.busy ? 'Removing…' : 'Remove'}</button>`}${bs.err ? html`<div class="err">${bs.err}</div>` : nothing}</td></tr>`;
      })}</tbody></table>` : html`<p class="empty">No personal binds.</p>`}
    ${form}
  </section>`;
}

// ---- notices: what xbind told the person ----

export function noticesSection(host, m) {
  const list = m.notices;
  return html`<section id="notices"><h2>Notices <span class="n">${list.length}</span></h2>
    ${list.length ? html`<table data-notices><tbody>${list.map((n) => html`<tr data-notice=${n.kind}>
        <td class="small" style="white-space:nowrap">${timeText(n.at)}</td>
        <td>${n.tile ? html`<code>${n.tile}</code>: ` : nothing}${n.text}${n.hold && m.credentials.some((h) => h.id === n.hold)
          ? html` <a href="#credentials">— waiting for you</a>` : nothing}</td></tr>`)}</tbody></table>`
      : html`<p class="empty">Nothing: xbind tells you here when your data in a tile is deleted or reset, or someone makes a credential for your account.</p>`}
  </section>`;
}

// ---- the ledger: what the person's partitions called, 30 days ----

export function ledgerSection(host, m) {
  if (!m.people) return nothing;
  const rows = ledgerTotals(m.ledger);
  return html`<section id="ledger"><h2>Your partitions' calls, 30 days <span class="n">${plural(rows.length, 'row')}</span></h2>
    <p class="lead small">Counts only, never what was sent: calls and data reaches from your partitions into other tiles, bus
      subscriptions and private triggers.</p>
    ${rows.length ? html`<table data-ledger><thead><tr><th>from your partition of</th><th></th><th>target</th><th class="n">count</th></tr></thead><tbody>
      ${rows.slice(0, 200).map((r) => html`<tr><td class="mono">${r.tile}</td><td>${ledgerKind(r.kind)}</td><td class="mono">${r.target}</td><td class="n">${r.count}</td></tr>`)}
    </tbody></table>` : html`<p class="empty">Nothing counted.</p>`}
  </section>`;
}
