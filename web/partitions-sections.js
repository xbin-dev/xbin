// web/partitions-sections.js — the partitions page's first sections
// (web/partitions-page.js): the header, credentials waiting for the person,
// partition mode switch decisions for tile managers, and the person's own
// partitions with each tile's trust panel. Every value is bound as text
// (lit escapes it): a tile's partitionNote and paths are the tiles' own
// words.
import { html, nothing } from '/vendor/lit-all.min.js';
import {
  modeName, switchDeletes, deletesNothing, switchLabel, wipedText, decisions, whoDecides, plural,
  resetConfirm, rowState, usageText, mailText, credentialText, credentialAsk, edgesInto, ledgerTotals, ledgerKind, timeText,
} from '/vendor/partitions-kit.js';

// the marker: the shell's half-split teal disc (PD-53, design A)
export const mark = html`<svg class="mark" viewBox="0 0 16 16" role="img" aria-label="partitioned"><circle cx="8" cy="8" r="6.5"
  fill="none" stroke="currentColor" stroke-width="1.6" style="color: var(--pt-part)"/><path d="M8 1.5 A6.5 6.5 0 0 0 8 14.5 Z"
  fill="currentColor" style="color: var(--pt-part)"/></svg>`;

const said = (st) => html`${st.err ? html`<p class="err" role="alert">${st.err}</p>` : nothing}${st.done ? html`<p class="done">${st.done}</p>` : nothing}`;

// ---- header ----

export function headerSection(host, m) {
  const me = m.me;
  const who = me.kind === 'user' ? `${me.name || me.id} (${me.id})` : me.kind === 'root' ? 'the root token' : me.id || 'unknown';
  const pol = m.policies;
  return html`
    <header>
      <h1>${mark} Your partitions</h1>
      <span class="who">signed in as <b>${who}</b>${me.admin ? ' · workspace admin' : ''}</span>
      <span class="grow"></span>
      <a class="small" href="/">← workspace</a>
    </header>
    <p class="lead">Tiles that keep each person's data apart run one instance per person — your partition — holding
      your data, vault and registrations. Here you see them, who can change the code that runs on your data, and
      what you decide. Nobody else reads your partition through the workspace — not the tile's writers, not admins
      (<a href="/docs/partitions.md" target="_blank" rel="noopener">how partitions work</a>).</p>
    <div class="row small">
      <span class="chip ${pol.partitionConsent ? 'ok' : 'off'}" title="the workspace setting partitionConsent">consent before another tile uses your data: ${pol.partitionConsent ? 'on' : 'off'}</span>
      <span class="chip ${pol.credentialResetConfirm ? 'ok' : 'off'}" title="the workspace setting credentialResetConfirm">credential resets wait for you: ${pol.credentialResetConfirm ? 'on' : 'off'}</span>
    </div>
    ${me.impersonatedBy ? html`<div class="banner">View-as (${me.impersonatedBy}): read-only, and a person's partitions stay theirs — this page shows little.</div>` : nothing}
    ${me.kind === 'root' ? html`<div class="banner">The root token is no person: it holds no partitions, consents or personal binds. Sign in as yourself to see yours; as the owner you still decide switches here.</div>` : nothing}
    ${m.errors.map((e) => html`<div class="banner err">${e}</div>`)}`;
}

// ---- credentials an admin made for the person (held) ----

export function credentialsSection(host, m) {
  const decided = host.credDecided(), gone = new Set(decided.map((d) => d.id));
  const held = m.credentials.filter((h) => !gone.has(h.id));
  if (!held.length && !decided.length) return nothing;
  return html`<section id="credentials">
    <h2>Credentials waiting for you <span class="n">${held.length}</span></h2>
    ${decided.map((d) => html`<p class=${d.warn ? 'banner warn' : 'done'} role=${d.warn ? 'alert' : 'status'} data-cred-decided=${d.id}>${d.text}</p>`)}
    ${held.length ? html`<p class="lead small">Someone else made a way into your account. It works only once you allow it — or 24 hours after you
      were told, if you don't answer. Not you, or not expected? Refuse it.</p>` : nothing}
    ${held.map((h) => credentialCard(host, h))}
  </section>`;
}

// Refusing is one click (it costs at most a fresh link); allowing — the act
// that could hand the account to whoever made it — asks first, naming who
// made it and when.
function credentialCard(host, h) {
  const key = `cred:${h.id}`, st = host.ui(key), busy = !!st.busy || host.readOnly();
  return html`<div class="card attn" data-cred=${h.id}>
    <p>${credentialText(h)}</p>
    ${st.asking ? html`<div class="confirm" data-confirm="allow">
        <p class="warn">${credentialAsk(h)}</p>
        <div class="acts">
          <button class="danger" data-act="allow-yes" ?disabled=${busy} @click=${() => host.credential(h, true)}>${st.busy === 'allow' ? 'Allowing…' : 'Allow'}</button>
          <button class="primary" data-act="refuse" ?disabled=${busy} @click=${() => host.credential(h, false)}>${st.busy === 'refuse' ? 'Refusing…' : 'Refuse'}</button>
          <button ?disabled=${busy} @click=${() => host.set(key, { asking: false, err: '' })}>Cancel</button>
        </div></div>` : html`<div class="acts">
        <button class="primary" data-act="refuse" ?disabled=${busy} @click=${() => host.credential(h, false)}
          title="revokes it: a link stops working, a password or email is dropped">${st.busy === 'refuse' ? 'Refusing…' : 'Refuse'}</button>
        <button data-act="allow" ?disabled=${busy} @click=${() => host.set(key, { asking: true, err: '' })}>Allow…</button>
      </div>`}
    ${said(st)}
  </div>`;
}

// ---- partition mode switch decisions (tile managers) ----

export function decisionsSection(host, m) {
  const list = decisions(m), decided = host.decided();
  if (!list.length && !decided.length) return nothing;
  return html`<section id="decisions">
    <h2>Partition mode switches to decide <span class="n">${list.length}</span></h2>
    ${decided.map((d) => html`<p class="done" data-decided=${d.tile}>${d.text}</p>`)}
    ${list.length ? html`<p class="lead small">You manage these tiles. Their code asks for another partition mode than the one they run in, and
      they hold data. <b>Keep the current mode</b> deletes nothing; switching deletes what it says, after you type the tile's path.</p>` : nothing}
    ${list.map((t) => decisionCard(host, t))}
  </section>`;
}

function decisionCard(host, t) {
  const key = `mode:${t.tile}`, st = host.ui(key), busy = !!st.busy || host.readOnly();
  const none = deletesNothing(t.from, t.to), dry = st.dry ?? {};
  const state = t.declined ? html`<span class="chip">kept ${modeName(t.from)}</span>` : html`<span class="chip warn">paused</span>`;
  return html`<div class="card ${t.declined ? '' : 'attn'}" data-decide=${t.tile}>
    <div class="head"><h3>${t.tile}</h3>${state}<span class="chip">${modeName(t.from)} → ${modeName(t.to)}</span>
      ${t.since ? html`<span class="muted small">asked since ${timeText(t.since)}</span>` : nothing}</div>
    <p>${t.declined
      ? html`A manager kept ${modeName(t.from)}: the tile runs as before. Its code still asks for ${modeName(t.to)}; switching deletes ${switchDeletes(t.from, t.to)}.`
      : html`Until a manager decides, ${t.tile} doesn't run. Switching deletes <b>${switchDeletes(t.from, t.to)}</b>; keeping the current mode deletes nothing.`}</p>
    ${t.note ? html`<p class="note">${t.tile} says: ${t.note}</p>` : nothing}
    ${st.open ? switchConfirm(host, t, st, dry, none, busy) : html`<div class="acts">
      ${t.declined ? nothing : html`<button class="primary" data-act="keep" ?disabled=${busy} @click=${() => host.keep(t)}
        title="the tile runs again in ${modeName(t.from)}; nothing is deleted">${st.busy === 'keep' ? 'Keeping…' : 'Keep the current mode'}</button>`}
      <button class=${none ? '' : 'danger'} data-act="switch" ?disabled=${busy} @click=${() => host.switchCount(t)}
        title="shows what the switch deletes and keeps, then asks you to type ${t.tile}">${st.busy === 'count' ? 'Counting…' : switchLabel(t.from, t.to) + '…'}</button>
    </div>`}
    ${said(st)}
  </div>`;
}

function switchConfirm(host, t, st, dry, none, busy) {
  const key = `mode:${t.tile}`, keeps = Array.isArray(dry.keeps) ? dry.keeps : [], managers = Array.isArray(dry.managers) ? dry.managers : [];
  return html`<div class="confirm" data-confirm="switch">
    <p>Switching <code>${t.tile}</code> from ${modeName(t.from)} to ${modeName(t.to)} deletes <b>${dry.deletes || switchDeletes(t.from, t.to)}</b>.</p>
    ${none ? html`<p class="muted">Nothing is deleted: the global instance starts empty.</p>` : html`<p data-counts>It deletes: ${wipedText(dry.wiped)}.</p>`}
    ${dry.people ? html`<p>${plural(dry.people, 'person', 'people')} whose partition is deleted will be told.</p>` : nothing}
    ${keeps.length ? html`<p class="muted small">It keeps:</p><ul class="small" data-keeps>${keeps.map((k) => html`<li>${k}</li>`)}</ul>` : nothing}
    ${managers.length ? html`<p class="warn">These sandbox managers don't keep people apart (their hello lacks "partitions"): ${managers.join(', ')} —
      each person's partition would see every person's sandboxes there. Update them, or switch anyway.</p>
      <label><input type="checkbox" .checked=${!!st.yes} @change=${(e) => host.set(key, { yes: e.target.checked })}> switch anyway</label>` : nothing}
    ${none ? nothing : html`<p class="warn">This can't be undone.</p>`}
    <div class="acts">
      <label>Type <code>${t.tile}</code> to confirm
        <input class="mono" name="confirm" autocomplete="off" spellcheck="false" placeholder=${t.tile} .value=${st.typed ?? ''}
          @input=${(e) => host.set(key, { typed: e.target.value, err: '' })}></label>
      <button class=${none ? 'primary' : 'danger'} data-act="switch-go" ?disabled=${busy} @click=${() => host.switchGo(t)}>${st.busy === 'switch' ? 'Switching…' : switchLabel(t.from, t.to)}</button>
      <button ?disabled=${busy} @click=${() => host.set(key, { open: false, err: '' })}>Cancel</button>
    </div>
  </div>`;
}

// ---- the person's partitions ----

export function partitionsSection(host, m) {
  // tiles running people's partitions (paused ones included), any tile the
  // caller holds a partition of, and — for a reader who doesn't decide — a
  // tile paused on its way to partitions (managers see it under decisions)
  const tiles = m.tiles.filter((t) => t.from.user || t.rows.length || t.state === 'pending' && !t.manage);
  return html`<section id="partitions">
    <h2>Partitioned tiles you use <span class="n">${tiles.length}</span></h2>
    ${tiles.length ? tiles.map((t) => partitionCard(host, m, t))
      : html`<p class="empty">No tile you can read keeps each person's data apart yet.</p>`}
  </section>`;
}

function partitionCard(host, m, t) {
  const row = t.rows[0] ?? null, part = t.state === 'partitioned' && t.from.user;
  const chips = html`<span class="chip ${part ? 'part' : ''}">${modeName(t.from)}</span>
    ${t.state === 'pending' ? html`<span class="chip warn">paused: ${modeName(t.from)} → ${modeName(t.to)} requested</span>` : nothing}
    ${t.state === 'invalid' ? html`<span class="chip warn">doesn't run: ${t.error || 'invalid partition request'}</span>` : nothing}
    ${row?.running ? html`<span class="chip ok">running</span>` : nothing}`;
  return html`<div class="card ${part ? 'part' : ''}" data-tile=${t.tile}>
    <div class="head"><h3>${t.tile}</h3>${chips}</div>
    ${t.state === 'pending' && !t.manage ? html`<p class="muted small">Until a manager decides, it doesn't run. Who decides: ${whoDecides(t.owner)}.</p>` : nothing}
    ${part || row ? ownPartition(host, m, t, row) : nothing}
    ${t.trust && t.from.user ? trustPanel(m, t) : nothing}
  </div>`;
}

function ownPartition(host, m, t, row) {
  if (!m.people) return html`<p class="muted small">Only a person signed in as themselves has a partition here.</p>`;
  if (!row) return html`<p class="muted small">You have no partition here yet: it starts, empty, the first time you use ${t.tile}.</p>`;
  const share = row.logShare, mail = mailText(row.mail);
  const inst = row.instance;
  return html`<dl class="facts">
      <dt>your partition</dt><dd>${rowState(row)}${row.lastStarted ? html` <span class="muted">· last started ${timeText(row.lastStarted)}</span>` : nothing}</dd>
      <dt>holds</dt><dd data-usage>${usageText(row)}</dd>
      ${mail ? html`<dt>inbox</dt><dd>${mail}</dd>` : nothing}
      ${inst?.error ? html`<dt>last error</dt><dd class="err">${inst.error}</dd>` : nothing}
      ${row.crashLoop ? html`<dt>restarts</dt><dd class="warn">crash loop (${row.restarts || 0} restarts)</dd>` : nothing}
      <dt>log</dt><dd>${share?.until ? html`shared with the tile's managers and admins until ${timeText(share.until)}` : 'private: only you read it'}</dd>
    </dl>
    ${ownActs(host, t, row)}
    ${resetConfirmBox(host, t)}
    ${restoreBox(host, t)}`;
}

function ownActs(host, t, row) {
  const shareSt = host.ui(`share:${t.tile}`), partSt = host.ui(`part:${t.tile}`), off = host.readOnly();
  const shared = !!row.logShare?.until;
  return html`<div class="acts">
      ${row.running ? html`<button data-act="stop" ?disabled=${off || !!partSt.busy} @click=${() => host.stop(t)}
        title="stops your instance; your data stays and the next request starts it">${partSt.busy ? 'Stopping…' : 'Stop'}</button>` : nothing}
      ${shared ? html`<button data-act="unshare" ?disabled=${off || !!shareSt.busy} @click=${() => host.unshareLog(t)}>Stop sharing the log</button>`
        : html`<button data-act="share" ?disabled=${off || !!shareSt.busy} @click=${() => host.shareLog(t)}
            title="the tile's managers and admins may read your partition's backend log meanwhile">Share the log for</button>
          <input type="number" min="1" max="14" aria-label="days" .value=${String(shareSt.days ?? 7)} @input=${(e) => host.set(`share:${t.tile}`, { days: e.target.value })}> days`}
      <button data-act="backups" ?disabled=${off || !!host.ui(`restore:${t.tile}`).busy} @click=${() => host.backups(t)}>Restore from a backup…</button>
      <button class="danger" data-act="reset" ?disabled=${off} @click=${() => host.set(`reset:${t.tile}`, { open: true, err: '', done: '' })}>Reset…</button>
    </div>
    ${said(partSt)}${said(shareSt)}`;
}

function resetConfirmBox(host, t) {
  const key = `reset:${t.tile}`, st = host.ui(key), want = resetConfirm(t.tile, host.me());
  if (!st.open) return said(st);
  return html`<div class="confirm" data-confirm="reset">
    <p>Resetting deletes your partition of <code>${t.tile}</code> — its data, vault, registrations, ledger, log, mail, terminal layers and
      agent-session history — and erases its backup keys, so its backups can't be read any more. This can't be undone.</p>
    <div class="acts"><label>Type <code>${want}</code>
      <input class="mono" name="confirm" autocomplete="off" spellcheck="false" placeholder=${want} .value=${st.typed ?? ''}
        @input=${(e) => host.set(key, { typed: e.target.value, err: '' })}></label>
      <button class="danger" data-act="reset-go" ?disabled=${!!st.busy} @click=${() => host.reset(t)}>${st.busy ? 'Resetting…' : 'Reset my partition'}</button>
      <button ?disabled=${!!st.busy} @click=${() => host.set(key, { open: false, err: '' })}>Cancel</button></div>
    ${said(st)}
  </div>`;
}

function restoreBox(host, t) {
  const key = `restore:${t.tile}`, st = host.ui(key), want = resetConfirm(t.tile, host.me());
  if (!st.open) return said(st);
  const close = html`<button ?disabled=${!!st.busy} @click=${() => host.set(key, { open: false, err: '' })}>Close</button>`;
  if (!st.archiver) return html`<div class="confirm" data-confirm="restore"><p class="muted">No archiver keeps this workspace's backups, so there is nothing to restore from.</p>${close}</div>`;
  if (!st.versions?.length) return html`<div class="confirm" data-confirm="restore"><p class="muted">${st.archiver} holds no backup of your partition of ${t.tile} yet.</p>${close}${said(st)}</div>`;
  const dry = st.dry;
  return html`<div class="confirm" data-confirm="restore">
    <p>Restoring replaces your partition of <code>${t.tile}</code> — its data, vault and registrations — with a backup's.</p>
    <div class="acts"><label>Version <select @change=${(e) => host.set(key, { version: e.target.value, dry: null })}>
      ${st.versions.map((v) => html`<option value=${v.version} ?selected=${v.version === st.version}>${timeText(v.time) || v.version}${v.size ? ` · ${v.size} bytes` : ''}</option>`)}
    </select></label>
    ${dry ? nothing : html`<button data-act="restore-check" ?disabled=${!!st.busy} @click=${() => host.restoreCheck(t)}>${st.busy === 'check' ? 'Checking…' : 'Check it'}</button>`}${close}</div>
    ${dry ? html`<p class="small">It restores ${plural((dry.resources ?? []).length, 'resource')}${dry.resources?.length ? html`: <code>${dry.resources.join(', ')}</code>` : ''}, your vault and your registrations.</p>
      <div class="acts"><label>Type <code>${want}</code>
        <input class="mono" name="confirm" autocomplete="off" spellcheck="false" placeholder=${want} .value=${st.typed ?? ''}
          @input=${(e) => host.set(key, { typed: e.target.value, err: '' })}></label>
        <button class="danger" data-act="restore-go" ?disabled=${!!st.busy} @click=${() => host.restore(t)}>${st.busy === 'restore' ? 'Restoring…' : 'Restore'}</button></div>` : nothing}
    ${said(st)}
  </div>`;
}

// ---- the trust panel: who can change the code that runs on your data ----

function trustPanel(m, t) {
  const tr = t.trust, warn = Array.isArray(tr.warnings) ? tr.warnings : [], provs = Array.isArray(tr.providers) ? tr.providers : [];
  const into = edgesInto(m.ledger, t.tile), out = ledgerTotals(m.ledger, t.tile);
  const writers = (w) => (Array.isArray(w) && w.length ? w.join(', ') : 'nobody but admins');
  return html`<details class="trust" ?open=${warn.length > 0}>
    <summary>Trust: who can change the code that runs on your data${warn.length ? html` <span class="chip warn">${plural(warn.length, 'warning')}</span>` : nothing}</summary>
    ${warn.map((w) => html`<p class="warn small">⚠ ${w}</p>`)}
    <dl class="facts">
      <dt>its code</dt><dd>${writers(tr.writers)} (writers), and ${tr.admins || 'every workspace admin'}</dd>
      <dt>saves</dt><dd>${tr.liveReload ? html`<span class="warn">reach it live</span>` : 'don\'t reach it live'}${tr.protected ? ' · primary protected' : ''}${tr.reviewedOnly ? ' · reviewed code only' : ''}</dd>
      ${tr.lastCodeChange ? html`<dt>last code change</dt><dd>${codeChange(tr.lastCodeChange)}</dd>` : nothing}
      <dt>global binds</dt><dd>${provs.length ? html`every person's partition of ${t.tile} calls ${provs.map((p, i) => html`${i ? ', ' : ''}<code>${p.tile}</code>
          <span class="muted">(${writers(p.writers)}${p.liveReload ? '; live' : ''}${p.protected ? '; protected' : ''}${p.lastCodeChange ? `; changed ${codeChange(p.lastCodeChange)}` : ''})</span>`)} — their code sees your calls`
        : 'none: no tile outside the partitions is bound to it'}</dd>
      ${t.binds.length ? html`<dt>your personal binds</dt><dd>${t.binds.map((b, i) => html`${i ? ', ' : ''}<code>${b.slot}</code> → <code>${b.provider}</code>`)}</dd>` : nothing}
      <dt>used your data here</dt><dd>${into.length ? into.map((e, i) => html`${i ? ', ' : ''}<code>${e.from}</code> ${plural(e.count, 'time')}`) : 'no other tile, in the last 30 days'}</dd>
    </dl>
    ${out.length ? html`<table><thead><tr><th>your partition here, 30 days</th><th>target</th><th class="n">count</th></tr></thead><tbody>
      ${out.slice(0, 20).map((r) => html`<tr><td>${ledgerKind(r.kind)}</td><td class="mono">${r.target}</td><td class="n">${r.count}</td></tr>`)}</tbody></table>` : nothing}
  </details>`;
}

function codeChange(c) {
  if (!c || typeof c !== 'object') return String(c ?? '');
  const at = timeText(c.at || '');
  const by = c.by ? ` by ${c.by}` : '';
  const how = c.how ? ` (${c.how}${c.result && c.result !== 'ok' ? `: ${c.result}` : ''})` : '';
  return `${at}${by}${how}`.trim() || 'unknown';
}
