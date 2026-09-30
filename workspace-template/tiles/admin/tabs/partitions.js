/**
 * <bx-admin-partitions> — the admin console's runtime → partitions view
 * (docs/partitions.md §Operating people's partitions): every tile that keeps each person's data apart, or asks to.
 * Per tile its mode and any request, how many people hold a partition, how
 * many run, what they hold, and what needs an admin's eye (trust warnings,
 * caps hit, reviewed code only); a row opens the tile's own view
 * (<bx-admin-partition-tile>, partition-tile.js): the mode decision,
 * reviewed code only, limits, people's metadata rows with stop / reset /
 * restore, personal binds, orphans and the mode history. Below the list,
 * the workspace's orphaned partitions and their purge.
 *
 * Reads GET /partitions (the admins' overview: the admin tile's frame under
 * an admin's login reads as that admin; anyone else driving it reads each
 * tile's state only) and polls it every 10 s; follows the partitions /
 * reload / users events. The untracked-files check (?untracked=1, a
 * confined git per partitioned tile) runs once per click, its answer kept
 * across the polls; a purge deletes exactly the orphans it listed, one
 * request each. An xbind without partitioned
 * tiles answers 404: the view says so and asks for nothing more. An admin
 * sees metadata only — never what a partition holds, its vault, its mail or
 * its log (a person may share their log: the tile's logs panel in the shell
 * shows it).
 */
import { LitElement, html, nothing } from 'lit';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import { base } from '../admin-css.js';
import { WithFilter, WithRouter } from '../shared.js';
import {
  modeName, stateWords, requestText, bytesText, tileNotes, when, shortId, orphanWhy, purgeBodies, untrackedOf, withUntracked,
} from './partitions-view.js';
import { partitionsCss } from './partition-tile.js';

export class BxAdminPartitions extends WithRouter(WithFilter(LitElement)) {
  static properties = {
    _d: { state: true },       // GET /partitions (undefined: loading, null: this xbind has none)
    _open: { state: true },    // the tiles whose own view is open
    _purge: { state: true },   // the purge awaiting confirmation: {tile?, partition?, rows}
    _busy: { state: true },
    _untracked: { state: true }, // the last untracked-files check: {at, byTile} (null: never asked)
    _err: { state: true },
    _q: { state: true },
    _cats: { state: true },
  };
  static styles = [base, partitionsCss];

  constructor() { super(); this._open = new Set(); this._purge = null; this._busy = false; this._untracked = null; this._q = ''; this._cats = new Set(); }

  connectedCallback() {
    super.connectedCallback();
    this.load();
    this._timer = setInterval(() => this.load(), 10000);
    this._off = window.xbin?.events.on((e) => {
      if (e.type === 'partitions' || e.type === 'reload' || e.type === 'users') this._soon();
    });
  }
  disconnectedCallback() { super.disconnectedCallback(); clearInterval(this._timer); clearTimeout(this._t); this._off?.(); }
  _soon() { clearTimeout(this._t); this._t = setTimeout(() => this.load(), 300); }
  refresh() { this.load(); for (const el of this.renderRoot.querySelectorAll('bx-admin-partition-tile')) el.load(); }

  async load() {
    try {
      this._d = await api('/partitions');
    } catch (e) {
      if (/404|not found/i.test(String(e?.message))) this._d = null; // an xbind without partitioned tiles
      else this._fail(e);
    }
  }

  // _checkUntracked asks once (a confined git per partitioned tile) and
  // keeps the answer: the polls never ask again; "check again" does.
  async _checkUntracked() {
    this._busy = true;
    try {
      const d = await api('/partitions?untracked=1');
      this._untracked = { at: new Date().toISOString(), byTile: untrackedOf(d) };
      this._d = d;
      this._ok();
    } catch (e) { this._fail(e); }
    this._busy = false;
  }

  _toggle(tile) {
    const s = new Set(this._open);
    s.has(tile) ? s.delete(tile) : s.add(tile);
    this._open = s;
  }

  // ---- purge: orphaned partitions, listed first, deleted on confirmation ----
  _askPurge(rows) { this._purge = { rows }; }
  // _doPurge deletes exactly the confirmed rows, one request each (a body
  // without tile and partition would purge every orphan there is by then).
  async _doPurge() {
    const p = this._purge;
    this._busy = true;
    let done = 0;
    const failed = [];
    for (const body of purgeBodies(p.rows)) {
      try {
        const r = await api('/partitions/purge', jbody(body, 'POST'));
        for (const x of r.purged || []) x.error ? failed.push(x.error) : done++;
      } catch (e) { failed.push(`${body.tile} ${shortId(body.partition)}: ${String(e?.message ?? e)}`); }
    }
    if (failed.length && !done) this._fail(failed.join('; '));
    else {
      this._ok();
      this._emit('bx-admin-notice', `purged ${done} orphaned partition${done === 1 ? '' : 's'}${failed.length ? ` (${failed.length} failed: ${failed.join('; ')})` : ''}`);
    }
    this._purge = null;
    this._busy = false;
    this.refresh();
  }

  _orphans(rows) {
    if (!rows?.length) return html`<p class="muted" data-pt-orphans="0">No orphaned partitions.</p>`;
    return html`<table class="pt" data-pt-orphans=${rows.length}>
      <tr><th>tile</th><th>person</th><th>partition</th><th>why</th><th>since</th><th></th></tr>
      ${rows.map((o) => html`<tr data-pt-orphan=${o.partition}>
        <td class="mono">${o.tile}${o.deployment && o.deployment !== 'main' ? html` <span class="muted">· ${o.deployment}</span>` : nothing}</td>
        <td class="mono">${o.user}</td><td class="mono" title=${o.partition}>${shortId(o.partition)}</td>
        <td>${orphanWhy(o.reason)}</td>
        <td class="mono">${when(o.since)}</td>
        <td><button class="act rm" ?disabled=${this._busy} @click=${() => this._askPurge([o])}>purge…</button></td>
      </tr>`)}
    </table>
    <div class="pt-bar"><button class="act rm" data-pt-purge-all ?disabled=${this._busy} @click=${() => this._askPurge(rows)}>purge all orphans…</button>
      <span class="muted" style="font-size:11px">An orphan is deleted 30 days after its person's deletion or its tile's removal; purging deletes it now, with its backup keys.</span></div>`;
  }

  _purgeAsk() {
    const p = this._purge;
    if (!p) return nothing;
    return html`<div class="pt-ask" data-pt-purge-confirm>
      Delete ${p.rows.length === 1 ? 'this orphaned partition' : `these ${p.rows.length} orphaned partitions`} now — their data, vault,
      registrations and log — and erase their backup keys, so their archives can't be read any more:
      <ul>${p.rows.map((o) => html`<li class="mono">${o.tile} · ${o.user} · ${shortId(o.partition)}</li>`)}</ul>
      <div class="row"><button class="act rm" data-pt-purge-go ?disabled=${this._busy} @click=${() => this._doPurge()}>Purge</button>
        <button class="act" @click=${() => { this._purge = null; }}>cancel</button></div>
    </div>`;
  }

  _row(t) {
    const open = this._open.has(t.tile), tot = t.totals || {};
    const notes = tileNotes(t);
    return html`<tr class="pt-row ${open ? 'open' : ''}" data-pt-tile=${t.tile} data-state=${t.state} @click=${() => this._toggle(t.tile)}>
      <td><span class="caret ${open ? 'o' : ''}">▶</span> <span class="mono">${t.tile}</span></td>
      <td><span class="pt-mode">${modeName(t.spec)}</span></td>
      <td class="pt-state ${t.state}">${stateWords(t.state)}${t.request && !t.request.declined ? html` <span class="muted">→ ${modeName(t.request.spec)}</span>` : nothing}
        ${t.request?.declined ? html` <span class="muted">(declined ${modeName(t.request.spec)})</span>` : nothing}</td>
      <td class="num">${tot.people ?? '—'}</td>
      <td class="num">${tot.running ?? '—'}</td>
      <td class="num">${tot.bytes === undefined ? '—' : bytesText(tot.bytes)}</td>
      <td class="num">${tot.cron === undefined ? '—' : `${tot.cron} · ${tot.bus}`}</td>
      <td>${t.error ? html`<span class="pt-note warn">${t.error}</span>` : nothing}
        ${notes.map((n) => html`<span class="pt-note ${n.kind}">${n.text}</span>`)}</td>
    </tr>
    ${open ? html`<tr><td class="pt-detail" colspan="8" @click=${(e) => e.stopPropagation()}>
      <bx-admin-partition-tile .tile=${t.tile} @bx-admin-partitions-changed=${() => this.load()}></bx-admin-partition-tile>
    </td></tr>` : nothing}`;
  }

  render() {
    const d = this._d;
    if (d === undefined) return html`<div class="muted">loading…</div>`;
    if (d === null) {
      return html`<div data-partitions="unavailable"><p class="muted">This xbind doesn't serve partitioned tiles (no
        <span class="mono">GET /api/xbin/partitions</span>): nothing to show. See
        <a href="/docs/partitions.md" target="_blank">docs/partitions.md</a>.</p></div>`;
    }
    const tiles = withUntracked(d.tiles, this._untracked?.byTile);
    const cats = [...new Set(tiles.map((t) => t.state))];
    const rows = tiles.filter((t) => this._catActive(t.state) && this._match(t.tile, modeName(t.spec)));
    const admin = d.orphans !== undefined; // xbind answers the console an admin's view only when its driver is one
    const u = this._untracked;
    return html`<div data-partitions=${tiles.length}>
      <p class="hint muted pt-intro">Tiles where each person has their own data — their own backend instance, resources, vault and
        log (<a href="/docs/partitions.md" target="_blank">docs/partitions.md</a>).${admin ? html` You see who has a partition, its size and counts,
        never what it holds. Open a tile to decide its mode, stop or reset a person's partition, or restore one from a backup.` : nothing}</p>
      ${admin ? nothing : html`<div class="pt-warn" data-pt-notadmin>You aren't a workspace admin: this view shows each tile's state and
        any request (a tile's manager can keep or switch its mode here), never who holds a partition.</div>`}
      ${d.isolated === false ? html`<div class="pt-warn" data-pt-unisolated>⚠ xbind runs without <span class="mono">--isolate</span>:
        people's partitions can't start here (a tile that asks for them runs no one's).</div>` : nothing}
      ${tiles.length ? html`
        ${this._filterBar('filter by tile or mode…', cats, rows.length, tiles.length)}
        <table class="pt">
          <tr><th>tile</th><th>mode</th><th>state</th><th>people</th><th>running</th><th>data</th><th>cron · bus</th><th></th></tr>
          ${rows.map((t) => this._row(t))}
        </table>
        ${admin ? html`<div class="pt-bar"><button class="act" data-pt-untracked=${u ? 'checked' : ''} ?disabled=${this._busy}
          @click=${() => this._checkUntracked()}>${u ? 'check untracked files again' : 'check untracked files'}</button>
          <span class="muted" style="font-size:11px">files a person left in a partitioned tile's shared directory, which its repository
            doesn't track${u ? html` — checked ${when(u.at)}, ${untrackedSummary(u.byTile)}` : ''}</span></div>` : nothing}`
        : html`<p class="muted" data-pt-none>No tile keeps each person's data apart yet. A tile asks for it with
          <span class="mono">"partition": ["user"]</span> in its xbin.json.</p>`}
      ${requestLines(tiles)}
      ${admin ? html`<h4>orphaned partitions</h4>
        ${this._purgeAsk()}
        ${this._orphans(d.orphans)}` : nothing}
    </div>`;
  }
}

// untrackedSummary: the last check's answer in words.
function untrackedSummary(byTile) {
  const all = Object.values(byTile || {});
  const found = all.filter((r) => r.untrackedCount > 0).length, failed = all.filter((r) => r.untrackedError).length;
  if (!found && !failed) return 'none found';
  return [found ? `${found} tile${found === 1 ? '' : 's'} with untracked files` : '', failed ? `${failed} couldn't be checked` : ''].filter(Boolean).join(', ');
}

// requestLines: the requests waiting for a manager, in words, above the orphans.
function requestLines(tiles) {
  const open = tiles.filter((t) => t.request && !t.request.declined);
  if (!open.length) return nothing;
  return html`<h4>waiting for a decision</h4>${open.map((t) => html`<div class="pt-note warn" data-pt-request=${t.tile}>${requestText(t.tile, t)}</div>`)}`;
}

customElements.define('bx-admin-partitions', BxAdminPartitions);
