// ci-dock.js — CI in the conversation on the web (API.md §CI in the
// conversation), inside the coding agents' UI (harness-board.js), not beside
// it. The words and the data are app.ci's (model/ci.js).
//
//   top          the CI chip after the coding agents chip: "CI running 3/5
//                jobs · 2:14", "CI passed", "CI failed: test (ubuntu)", each
//                after its status glyph (D184), "CI —" (nothing
//                reported yet, or nothing watched but you may) — opens the
//                right dock on its CI tab; absent at home
//   dock         the CI section of the right dock (#hboard): per watch its
//                branch (↗), PR (↗), state, since when, ✕ unwatch (not a
//                task's own); per run its name, event, attempt, state, time
//                (↗) and Re-run failed (a person's, confirmed); per job a
//                progress bar, its step, time (↗), its steps when expanded,
//                Log and its annotations; other checks and statuses (↗);
//                "Watch CI for…" at the end. A refusal to sign in offers the
//                sign-in (project-new.js's card), then Retry
//   the log      in the dock instead of the section until ← Back (the dock
//                wider): the tail (64 KiB; Earlier pages back), plain text,
//                search with next and previous, Follow while a job runs
//                where the platform serves partial logs (every 5 s); a job
//                still running where it serves logs only once a job ends:
//                its steps, "the log is ready when the job finishes", Open
//                live log ↗
//   childStatus  a coding agent card's CI glyph (what it pushed)
//   card         a project board task's CI chip — opens the task on CI
//   paint        the dock's CI tab live (re-read every 15 s while anything
//                is pending); a conversation's CI read once when it opens
//
// Every text here is what a build printed — untrusted: drawn as text (lit
// escapes it), never markdown or HTML; links only http(s). openCI(root, {job,
// watch}) opens the dock on CI (on that job's log) — V's own modules only.
import { html, nothing, repeat } from '/vendor/lit-all.min.js';
import { ext, ctx } from './web-ext.js';
import { openDock, dockTab } from './harness-board.js';
import { signinTpl } from './project-new.js';
import { chipWords, elapsed, ICON } from './model/ci.js';

const rootOf = (v) => (v ? v.run.rootId || v.run.id : null);
const safe = (u) => (/^https?:\/\//i.test(String(u || '')) ? u : '');
const out = (u, label, title) => (safe(u) ? html`<a class="cilink" href=${safe(u)} target="_blank" rel="noopener noreferrer" title=${title || 'on the platform'}>${label}</a>` : nothing);
const repaint = () => ctx.paint();
// a status's glyph (D184): the tone's bx-icon, a hollow square for one not started
const glyph = (tone) => (ICON[tone] ? html`<bx-icon name=${ICON[tone]}></bx-icon>` : html`<span class="cisq"></span>`);
const mark = (tone) => html`<span class="cist" data-tone=${tone}>${glyph(tone)}</span>`;

// the section's state: what is open, the log viewer, the watch form
const st = { root: null, open: new Set(), notes: new Map(), log: null, form: { repo: '', ref: '', err: '', busy: false }, note: '', err: '' };
const read = new Set(); // conversations read once on opening

ext.register({
  top: (v) => chipTpl(v),
  dock: (v) => {
    const app = ctx.app;
    const root = rootOf(v);
    if (!app || root == null || (!app.ci.chip(root) && !app.ci.view(root))) return null;
    const c = app.ci.chip(root);
    // the tab's badge, short (the strip doesn't wrap): the state's word, a run's progress
    const badge = !c || c.tone === 'idle' ? '' : c.tone === 'ok' ? 'passed' : c.tone === 'bad' ? 'failed' : c.text.replace(/^CI (running )?/, '');
    return { key: 'ci', title: 'CI', badge, tpl: () => sectionTpl(v) };
  },
  childStatus: (r) => childTpl(r),
  card: (task) => cardTpl(task),
  paint: (v) => paintCI(v),
});

/** openCI(root, {job, watch}): the right dock on CI — on a job's log when named. */
export function openCI(root, { job = '', watch = 0 } = {}) {
  st.log = null;
  if (job && watch) openLog(root, watch, job);
  openDock('ci');
}

// --- the chip, the child's glyph, the board's chip ------------------------------------------

function chipTpl(v) {
  const app = ctx.app;
  const root = rootOf(v);
  const c = app && root != null ? app.ci.chip(root) : null;
  if (!c) return null;
  const on = dockTab() === 'ci';
  const go = () => openCI(root);
  return html`<span class="badge cichip" id="cichip" role="button" tabindex="0" data-tone=${c.tone} aria-pressed=${on ? 'true' : 'false'} title=${c.title}
    @click=${go} @keydown=${(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); go(); } }}>${ICON[c.tone] ? glyph(c.tone) : nothing}${c.text}</span>`;
}

function childTpl(r) {
  const app = ctx.app;
  if (!app || !r || !r.id) return null;
  const c = app.ci.child(r.rootId || r.id, r.id);
  if (!c) return null;
  return html` <span class="cichild" role="button" tabindex="0" data-tone=${c.tone} title=${c.title} @click=${(e) => { e.stopPropagation(); openCI(r.rootId || r.id); }}
    @keydown=${(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); e.stopPropagation(); openCI(r.rootId || r.id); } }}>${ICON[c.tone] ? glyph(c.tone) : nothing}${c.text}</span>`;
}

function cardTpl(task) {
  if (!task || !task.ci || !ctx.app) return null;
  const c = chipWords(task.ci, Date.now());
  const open = async (e) => {
    e.stopPropagation();
    e.preventDefault();
    if (!task.run) return;
    await ctx.app.select(task.run);
    openCI(task.run);
  };
  return html`<button class="badge cicard" data-tone=${c.tone} title=${c.title} @click=${open}>${ICON[c.tone] ? glyph(c.tone) : nothing}${c.text}</button>`;
}

// --- live, read once ----------------------------------------------------------------------

function paintCI(v) {
  const app = ctx.app;
  if (!app) return;
  const root = rootOf(v);
  if (root !== st.root) { st.root = root; st.open.clear(); st.notes.clear(); stopFollow(); st.log = null; st.note = ''; st.err = ''; }
  const shown = root != null && dockTab() === 'ci';
  if (shown) app.ci.live(root, true);
  else app.ci.live(null, false);
  if (!shown && followT) { stopFollow(); if (st.log) st.log.follow = false; } // the dock closed or on another tab: following a log stops
  if (root != null && !read.has(root) && !app.ci.view(root) && app.ci.chip(root)) { read.add(root); app.ci.load(root).catch(() => {}); }
  const wrap = document.querySelector('.wrap');
  if (wrap) wrap.classList.toggle('ciwide', shown && !!st.log);
}

// --- the section ----------------------------------------------------------------------------

const toggle = (k) => { if (st.open.has(k)) st.open.delete(k); else st.open.add(k); repaint(); };

async function act(fn, done) {
  st.err = ''; st.note = '';
  try { await fn(); st.note = done || ''; } catch (e) { st.err = (e && e.message) || String(e); }
  repaint();
}

function sectionTpl(v) {
  const app = ctx.app;
  const root = rootOf(v);
  if (st.log && st.log.root === root) return logTpl(app, st.log);
  const view = app.ci.view(root);
  if (!view) {
    const err = app.ci.error(root);
    return html`<div class="cisec">${err ? html`<div class="err small">${err} <button class="lnk" @click=${() => app.ci.load(root).catch(() => {})}>Retry</button></div>`
      : html`<div class="muted small cimuted">loading…</div>`}${formTpl(app, root, { canWatch: app.ci.canWatch(root) })}</div>`;
  }
  const rows = app.ci.rows(root);
  return html`<div class="cisec" id="cisec">
    ${st.err ? html`<div class="err small" role="alert">${st.err}</div>` : st.note ? html`<div class="muted small" role="status">${st.note}</div>` : nothing}
    ${rows.length ? repeat(rows, (w) => w.watch, (w) => watchTpl(app, root, view, w))
      : html`<div class="muted small cimuted">Nothing watched yet${view.canWatch ? ': name a branch or a pull request below, or push one from a coding agent' : ''}.</div>`}
    ${formTpl(app, root, view)}
  </div>`;
}

function watchTpl(app, root, view, w) {
  const since = w.since ? new Date(w.since).toLocaleString() : '';
  return html`<div class="ciwatch" data-watch=${w.watch} data-state=${w.state}>
    <div class="ciwh">
      ${mark(w.tone)}
      ${safe(w.urls.branch) ? out(w.urls.branch, w.title, 'the branch on the platform') : html`<span class="mono">${w.title}</span>`}
      ${w.pr ? out(w.urls.pr, `PR #${w.pr}`, 'the pull request') || html`<span>PR #${w.pr}</span>` : nothing}
      <span class="cistate" data-tone=${w.tone}>${w.state === 'gone' ? 'branch gone' : w.state}</span>
      ${w.source !== 'task' ? html`<button class="lnk ciun" title="stop watching" @click=${() => act(() => app.ci.unwatch(root, w.watch), 'Stopped watching.')}><bx-icon name="xmark" label="stop watching"></bx-icon></button>` : nothing}
    </div>
    <div class="muted small ciwsub">${w.source === 'task' ? 'the task\'s branch' : w.source === 'pushed' ? 'pushed from this conversation' : 'watched by hand'}${since ? ` · since ${since}` : ''}${w.sha ? ` · ${w.sha.slice(0, 7)}` : ''}
      ${out(w.urls.checks, '↗ checks', 'its checks on the platform')}</div>
    ${w.refusal === 'signin' ? html`<div class="cisign">${signinTpl(app.projects, w.scm, 'the provider')}
        <button class="lnk" @click=${() => app.ci.load(root, { fresh: true }).catch(() => {})}>Retry</button></div>`
      : w.error ? html`<div class="err small">${w.error}</div>` : nothing}
    ${w.runs.map((r) => runTpl(app, root, view, w, r))}
    ${w.checks.length ? html`<div class="cigroup">${w.checks.map((k) => checkTpl(app, root, w, k))}</div>` : nothing}
    ${w.statuses.length ? html`<div class="cigroup">${w.statuses.map((s) => html`<div class="cirow cistatus">
        ${mark(s.tone)}<span class="ciname">${s.context}</span>
        <span class="muted small cidesc">${s.description}</span>${out(s.url, '↗')}</div>`)}</div>` : nothing}
    ${!w.runs.length && !w.checks.length && !w.statuses.length && !w.error && w.refusal !== 'signin'
      ? html`<div class="muted small cimuted">${w.state === 'gone' ? 'Its branch is gone.' : 'Nothing reported on it yet.'}</div>` : nothing}
  </div>`;
}

function runTpl(app, root, view, w, r) {
  const rerun = () => {
    if (!confirm(`Re-run the failed jobs of ${r.name}? It runs as you, and spends CI time (it may deploy).`)) return;
    act(() => app.ci.rerun(root, { watch: w.watch, runId: r.id, failedOnly: true }), `Re-running the failed jobs of ${r.name}.`);
  };
  return html`<div class="cirun" data-run=${r.id}>
    <div class="cirow cirunh">
      ${mark(r.tone)}<b class="ciname">${r.name}</b>
      <span class="muted small">${r.event}${r.attempt > 1 ? ` · attempt ${r.attempt}` : ''} · ${r.state}${elapsed(r.elapsedMs) ? ` · ${elapsed(r.elapsedMs)}` : ''}</span>
      ${out(r.url, '↗', 'the run on the platform')}
      ${view.canRerun && r.failed ? html`<button class="btn ghost btnsm cirerun" @click=${rerun}>Re-run failed</button>` : nothing}
    </div>
    ${r.jobs.map((j) => jobTpl(app, root, w, j))}
  </div>`;
}

function jobTpl(app, root, w, j) {
  const k = `job:${w.watch}:${j.id}`;
  const open = st.open.has(k);
  const p = j.progress;
  return html`<div class="cijob" data-job=${j.id} data-tone=${j.tone}>
    <div class="cirow" role="button" tabindex="0" aria-expanded=${open ? 'true' : 'false'} @click=${() => toggle(k)}
      @keydown=${(e) => { if (e.target === e.currentTarget && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); toggle(k); } }}>
      <span class="tw"><bx-icon name=${open ? 'caret-down' : 'caret-right'}></bx-icon></span>${mark(j.tone)}
      <span class="ciname">${j.name}</span>
      ${p.total ? html`<span class="cibar" title=${`${p.done} of ${p.total} steps`}><span style=${`width:${p.pct}%`}></span></span>` : nothing}
      <span class="muted small cicur">${p.current || (j.status === 'completed' ? j.conclusion : j.status)}${elapsed(j.elapsedMs) ? ` · ${elapsed(j.elapsedMs)}` : ''}</span>
      ${out(j.url, '↗', 'the job on the platform')}
    </div>
    ${open ? html`<div class="cisteps">
        ${j.steps.length ? j.steps.map((s) => html`<div class="cistep" data-tone=${s.tone}>${mark(s.tone)}
          <span class="ciname">${s.name}</span><span class="muted small">${elapsed(s.elapsedMs)}</span></div>`) : html`<div class="muted small">no steps yet</div>`}
        <div class="ciacts">
          <button class="lnk cilog" @click=${(e) => { e.stopPropagation(); openLog(root, w.watch, j.id, j.name); }}>Log</button>
          ${j.annotations && j.check ? html`<button class="lnk" @click=${() => toggleNotes(app, root, w.watch, j.check)}>${j.annotations} annotation${j.annotations === 1 ? '' : 's'}</button>` : nothing}
        </div>
        ${j.check ? notesTpl(w.watch, j.check) : nothing}
      </div>` : nothing}
  </div>`;
}

function checkTpl(app, root, w, k) {
  return html`<div class="cicheck" data-check=${k.id}>
    <div class="cirow">${mark(k.tone)}<span class="ciname">${k.name}</span>
      <span class="muted small cidesc">${k.title}</span>${out(k.url, '↗', 'where the check says to look')}</div>
    ${k.annotations ? html`<button class="lnk small cinotesbtn" @click=${() => toggleNotes(app, root, w.watch, k.id)}>${k.annotations} annotation${k.annotations === 1 ? '' : 's'}</button>` : nothing}
    ${notesTpl(w.watch, k.id)}
  </div>`;
}

// annotations: path:line, level, title, message — plain text
async function toggleNotes(app, root, watch, check) {
  const k = `${watch}:${check}`;
  if (st.notes.has(k)) { st.notes.delete(k); repaint(); return; }
  st.notes.set(k, { busy: true, items: [], err: '' });
  repaint();
  const n = st.notes.get(k);
  try { n.items = await app.ci.annotations(root, watch, check); } catch (e) { n.err = e.message; }
  n.busy = false;
  repaint();
}

function notesTpl(watch, check) {
  const n = st.notes.get(`${watch}:${check}`);
  if (!n) return nothing;
  if (n.busy) return html`<div class="muted small">loading…</div>`;
  if (n.err) return html`<div class="err small">${n.err}</div>`;
  return html`<div class="cinotes">${n.items.map((a) => html`<div class="cinote" data-level=${a.level}>
    <span class="mono">${a.path}:${a.startLine}</span> <span class="cilevel" data-level=${a.level}>${a.level}</span>${a.title ? html` <b>${a.title}</b>` : nothing}
    <div class="cimsg">${a.message}</div></div>`)}</div>`;
}

function formTpl(app, root, view) {
  if (!view || !view.canWatch) return nothing;
  const f = st.form;
  const submit = async (e) => {
    e.preventDefault();
    const repo = f.repo.trim(), ref = f.ref.trim().replace(/^#/, '');
    if (!/^[\w.-]+\/[\w.-]+$/.test(repo) || !ref) { f.err = 'Name a repo (owner/name) and a branch or a pull request number.'; repaint(); return; }
    f.busy = true; f.err = '';
    repaint();
    try {
      await app.ci.watch(root, /^\d+$/.test(ref) ? { repo, pr: +ref } : { repo, ref });
      f.repo = ''; f.ref = '';
    } catch (e2) { f.err = e2.message; }
    f.busy = false;
    repaint();
  };
  return html`<form class="ciform" id="ciform" @submit=${submit}>
    <div class="small"><b>Watch CI for…</b></div>
    <input id="ci-repo" aria-label="Repo (owner/repo)" placeholder="owner/repo" .value=${f.repo} @input=${(e) => { f.repo = e.target.value; }}>
    <input id="ci-ref" aria-label="Branch, or PR number" placeholder="branch, or PR number" .value=${f.ref} @input=${(e) => { f.ref = e.target.value; }}>
    <button class="btn btnsm" id="ci-watch" ?disabled=${f.busy}>Watch</button>
    ${f.err ? html`<div class="err small">${f.err}</div>` : nothing}
  </form>`;
}

// --- the log viewer -------------------------------------------------------------------------------

let followT = null;
const stopFollow = () => { if (followT) clearInterval(followT); followT = null; };

async function openLog(root, watch, job, name = '') {
  stopFollow();
  const lg = st.log = { root, watch, job, name, text: '', from: 0, bytes: 0, complete: true, truncated: false, url: '', inProgress: false,
    busy: true, err: '', q: '', hit: 0, follow: false };
  repaint();
  try {
    Object.assign(lg, await ctx.app.ci.log(root, watch, job), { busy: false });
  } catch (e) { lg.err = e.message; lg.busy = false; }
  if (st.log === lg) repaint();
}

async function earlier(lg) {
  lg.busy = true;
  repaint();
  try {
    const r = await ctx.app.ci.log(lg.root, lg.watch, lg.job, { until: lg.from });
    lg.text = r.text + lg.text;
    lg.from = r.from;
    lg.truncated = r.truncated;
  } catch (e) { lg.err = e.message; }
  lg.busy = false;
  repaint();
}

function follow(lg) {
  lg.follow = !lg.follow;
  stopFollow();
  if (lg.follow) {
    followT = setInterval(async () => {
      if (st.log !== lg) { stopFollow(); return; }
      try {
        const r = await ctx.app.ci.log(lg.root, lg.watch, lg.job, { since: lg.bytes });
        if (r.from >= lg.bytes) lg.text += r.text;
        lg.bytes = Math.max(lg.bytes, r.bytes);
        lg.complete = r.complete;
        if (r.complete) { lg.follow = false; stopFollow(); }
      } catch (e) { lg.err = e.message; lg.follow = false; stopFollow(); }
      repaint();
    }, 5000);
  }
  repaint();
}

// the job's row in the snapshot (its steps for a running job's viewer)
function jobOf(root, watch, job) {
  for (const w of ctx.app.ci.rows(root)) {
    if (w.watch !== watch) continue;
    for (const r of w.runs) for (const j of r.jobs) if (j.id === job) return j;
  }
  return null;
}

// textTpl: the log as text, the search's hits marked (lit text, never HTML)
function textTpl(lg) {
  const q = lg.q;
  if (!q) return lg.text;
  const parts = [];
  const low = lg.text.toLowerCase(), needle = q.toLowerCase();
  let i = 0, n = 0;
  for (let at = low.indexOf(needle); at >= 0 && n < 2000; at = low.indexOf(needle, at + needle.length), n++) {
    parts.push(lg.text.slice(i, at), html`<mark class=${n === lg.hit ? 'cur' : ''} data-hit=${n}>${lg.text.slice(at, at + q.length)}</mark>`);
    i = at + q.length;
  }
  parts.push(lg.text.slice(i));
  lg.hits = n;
  return parts;
}

function jump(lg, d) {
  if (!lg.hits) return;
  lg.hit = (lg.hit + d + lg.hits) % lg.hits;
  repaint();
  requestAnimationFrame(() => document.querySelector('#cilog mark.cur')?.scrollIntoView({ block: 'center' }));
}

function logTpl(app, lg) {
  const back = () => { stopFollow(); st.log = null; repaint(); };
  const j = jobOf(lg.root, lg.watch, lg.job);
  const name = (j && j.name) || lg.name || lg.job;
  const head = html`<div class="cilogh">
    <button class="lnk" id="ci-back" @click=${back}>← Back</button><b class="ciname">${name}</b>
    ${out(lg.url || (j && j.url), 'Open live log ↗', 'the job\'s log on the platform')}</div>`;
  if (lg.busy && !lg.text) return html`<div class="cilogv">${head}<div class="muted small">loading…</div></div>`;
  if (lg.err) return html`<div class="cilogv">${head}<div class="err small">${lg.err}</div></div>`;
  if (lg.inProgress) {
    return html`<div class="cilogv" id="cilogwait">${head}
      <div class="small">The job is still running: the log is ready when the job finishes.</div>
      ${j ? html`<div class="cisteps">${j.steps.map((s) => html`<div class="cistep" data-tone=${s.tone}>${mark(s.tone)}
        <span class="ciname">${s.name}</span><span class="muted small">${elapsed(s.elapsedMs)}</span></div>`)}</div>` : nothing}
      ${out(lg.url || (j && j.url), 'Open live log ↗', 'watch it live on the platform')}
    </div>`;
  }
  const body = textTpl(lg); // first: it counts the hits the bar shows
  return html`<div class="cilogv">${head}
    <div class="cilogbar">
      <input id="ci-search" type="search" aria-label="Search the log" placeholder="search the log" .value=${lg.q}
        @input=${(e) => { lg.q = e.target.value; lg.hit = 0; repaint(); }}
        @keydown=${(e) => { if (e.key === 'Enter') { e.preventDefault(); jump(lg, e.shiftKey ? -1 : 1); } }}>
      <button class="btn ghost btnsm" id="ci-prev" title="previous" @click=${() => jump(lg, -1)}>↑</button>
      <button class="btn ghost btnsm" id="ci-next" title="next" @click=${() => jump(lg, 1)}>↓</button>
      <span class="muted small" id="ci-hits">${lg.q ? `${lg.hits ? lg.hit + 1 : 0}/${lg.hits || 0}` : ''}</span>
      ${!lg.complete ? html`<button class="btn ghost btnsm" id="ci-follow" aria-pressed=${lg.follow ? 'true' : 'false'} @click=${() => follow(lg)}>Follow</button>` : nothing}
    </div>
    ${lg.truncated ? html`<button class="lnk small" id="ci-earlier" ?disabled=${lg.busy} @click=${() => earlier(lg)}>Earlier</button>` : nothing}
    <pre class="cilog mono" id="cilog">${body}</pre>
  </div>`;
}

// the chip's, the section's and the log's look
const style = document.createElement('style');
style.textContent = `
  .badge.cichip, .badge.cicard { cursor: pointer; text-transform: none; letter-spacing: 0; font: var(--bx-font-meta); }
  .badge.cicard { background: none; }
  .cichip[data-tone="ok"], .cicard[data-tone="ok"], .cichild[data-tone="ok"], .cist[data-tone="ok"], .cistate[data-tone="ok"] { color: var(--bx-ok); border-color: var(--bx-ok); }
  .cichip[data-tone="bad"], .cicard[data-tone="bad"], .cichild[data-tone="bad"], .cist[data-tone="bad"], .cistate[data-tone="bad"] { color: var(--bx-danger); border-color: var(--bx-danger); }
  .cichip[data-tone="run"], .cicard[data-tone="run"], .cichild[data-tone="run"], .cistate[data-tone="run"] { color: var(--bx-info); border-color: var(--bx-info); }
  .cist[data-tone="run"], .cichip[data-tone="run"] bx-icon, .cicard[data-tone="run"] bx-icon, .cichild[data-tone="run"] bx-icon { color: var(--bx-accent); }
  .cichip[data-tone="warn"], .cicard[data-tone="warn"], .cichild[data-tone="warn"], .cist[data-tone="warn"], .cistate[data-tone="warn"] { color: var(--bx-warn); border-color: var(--bx-warn); }
  .cichip[data-tone="idle"], .cist[data-tone="idle"] { color: var(--bx-muted); }
  .badge.cichip[aria-pressed="true"] { background: var(--bx-panel-2); }
  .cichild { display: inline-flex; align-items: center; gap: 4px; font: var(--bx-font-meta); margin-left: 4px; cursor: pointer; --bx-icon-size: 12px; }
  .cist { display: inline-flex; flex: none; width: 16px; justify-content: center; align-self: center; }
  .cisq { box-sizing: border-box; width: 8px; height: 8px; border: 1px solid currentColor; }
  .wrap.dockon.ciwide { grid-template-columns: 220px minmax(0, 1fr) minmax(340px, 50vw); }
  .cisec { display: flex; flex-direction: column; gap: 8px; padding-top: 8px; }
  .ciwatch { border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 8px; background: var(--bx-panel); min-width: 0; }
  .ciwh, .cirow { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
  .ciwh .cilink:first-of-type, .ciwh .mono { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
  .ciwh .cistate { margin-left: auto; font: var(--bx-font-meta); }
  .ciwsub { margin: 2px 0 4px; overflow-wrap: anywhere; }
  .cirun { margin-top: 8px; } .cirunh .cirerun { margin-left: auto; }
  .cijob .cirow { cursor: pointer; padding: 2px 0 2px 4px; }
  .ciname { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
  .cicur, .cidesc { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; flex: 1; }
  .cibar { flex: none; width: 48px; height: 4px; background: var(--bx-panel-2); overflow: hidden; align-self: center; }
  .cibar > span { display: block; height: 100%; background: var(--bx-info); }
  .cijob[data-tone="bad"] .cibar > span { background: var(--bx-danger); } .cijob[data-tone="ok"] .cibar > span { background: var(--bx-ok); }
  .cisteps { padding: 2px 0 4px 28px; } .cistep { display: flex; gap: 8px; align-items: baseline; font: var(--bx-font-meta); }
  .ciacts { display: flex; gap: 12px; margin-top: 4px; }
  .cigroup { margin-top: 8px; border-top: 1px solid var(--bx-border); padding-top: 4px; }
  .cinotes { padding: 2px 0 4px 16px; } .cinote { font: var(--bx-font-meta); margin: 4px 0; }
  .cinote .cimsg { white-space: pre-wrap; overflow-wrap: anywhere; color: var(--bx-muted); }
  .cilevel[data-level="failure"] { color: var(--bx-danger); } .cilevel[data-level="warning"] { color: var(--bx-warn); }
  .ciform { display: flex; flex-wrap: wrap; gap: 4px; align-items: center; border-top: 1px solid var(--bx-border); padding-top: 8px; }
  .ciform .small { flex-basis: 100%; } .ciform input { flex: 1; min-width: 8em; }
  .cimuted { padding: 4px 2px; }
  .cilogv { display: flex; flex-direction: column; gap: 8px; padding-top: 8px; min-height: 0; height: 100%; }
  .cilogh, .cilogbar { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .cilogh .ciname { flex: 1; } .cilogbar input { flex: 1; min-width: 6em; }
  .cilog { flex: 1; margin: 0; padding: 8px; overflow: auto; font: var(--bx-font-code); white-space: pre-wrap; overflow-wrap: anywhere;
    background: var(--bx-code-bg); border: 1px solid var(--bx-border); border-radius: var(--bx-radius); min-height: 12em; }
  .cilog mark { background: var(--bx-warn-bg); color: inherit; }
  .cilog mark.cur { background: var(--bx-selection); color: var(--bx-selection-text); box-shadow: inset 0 -2px 0 var(--bx-accent); }
`;
document.head.append(style);
