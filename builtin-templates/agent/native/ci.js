// native/ci.js — CI in the conversation in the native view (API.md §CI in
// the conversation), the web's ci-dock.js and ci-cards.js with the app's
// primitives, over the same model (app.ci, model/ci.js):
//
//   dock         the CI sections of the Coding agents screen (after its
//                coding agents): per watch its branch and PR (↗ in its
//                actions, Stop watching unless it is a task's own), its runs
//                (↗, Re-run failed for a person, confirmed), its jobs with a
//                progress bar and the step under way (a tap: the ci-job
//                screen), other checks and statuses; a refusal to sign in
//                says so; "Watch CI for…" at the end. Its badge rides the
//                Coding agents toolbar button (no toolbar item of its own)
//   ci-job       a job: its steps (✓ ✗ ● ○, durations), its log (plain text;
//                Earlier pages back; the screen's search lists the matching
//                lines; Follow re-reads every 5 s while a job runs where the
//                platform serves partial logs), Open live log ↗ — a job
//                still running where logs come once it ends: its steps and
//                "the log is ready when the job finishes"
//   ci-annotations  a check's annotations: path:line, level, title, message
//   ci-watch     "Watch CI for…": a repo and a branch or a PR number
//   childStatus  a coding agent card's CI words (what it pushed)
//   card         a project board task row's CI words
//   end          outcome cards at the transcript's end (Open logs, Dismiss)
//   menu         "Watch CI for…" in a conversation's ⋯ (when you may)
//
// What a build printed is untrusted: drawn with `text` and `code` (verbatim,
// never markup); only https links open, through the app.
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ui, ctx, push, top, fail, guard, when } from './ui.js';
import { chipWords, elapsed, NATIVE_ICON } from '../model/ci.js';
import { isHttps } from '../model/terminals.js';

const TONE = { ok: 'ok', bad: 'danger', warn: 'warn', run: 'accent', idle: 'muted' };
const icon = (tone) => NATIVE_ICON[tone] || nothing; // a status's glyph (D184): the row's icon, its words the row's
const rootOf = (v) => (v && v.run ? v.run.rootId || v.run.id : null);
const read = new Set(); // conversations read once on opening
const childTpls = new Map(); // a child's CI words → their template
const open = (url) => { if (isHttps(url)) Promise.resolve().then(() => globalThis.xbin.native.open(url)).catch((e) => fail(e)); };

ext.register({
  dock(v) {
    const app = ctx.app;
    const root = rootOf(v);
    if (!app || root == null || (!app.ci.chip(root) && !app.ci.view(root))) return null;
    const c = app.ci.chip(root);
    return { key: 'ci', title: 'CI', badge: c ? c.text : '', tpl: () => sectionsTpl(v, root) };
  },
  toolbar(v) { liveFor(v); return null; },
  childStatus(r) {
    const app = ctx.app;
    const c = app && r && r.id ? app.ci.child(r.rootId || r.id, r.id) : null;
    if (!c) return null;
    const key = `${c.tone}|${c.text}|${c.title}`; // the same words, the same template: the card's memo keeps it (native/harness-child.js)
    if (!childTpls.has(key)) childTpls.set(key, html`<text style="footnote" tone=${TONE[c.tone] || nothing}>${`${c.text} — ${c.title}`}</text>`);
    return childTpls.get(key);
  },
  card(task) {
    if (!task || !task.ci) return null;
    return chipWords(task.ci, Date.now()).text;
  },
  end(v) {
    const app = ctx.app;
    const r = v && v.run;
    if (!app || !r || (r.status === 'waiting_input' && (r.pendingState || {}).harness)) return null; // a coding agent's park is its own
    const root = rootOf(v);
    const cards = app.ci.cards(root);
    if (!cards.length) return null;
    return repeat(cards, (c) => c.key, (c) => html`<message role="system" text=${c.text} @tap=${() => openJob(root, c)}>
      <actions>
        <button icon="terminal" @tap=${() => openJob(root, c)}>${c.job ? 'Open logs' : 'Open CI'}</button>
        <button icon="xmark" @tap=${() => app.ci.dismiss(c.key)}>Dismiss</button>
      </actions>
    </message>`);
  },
  menu(v) {
    const root = rootOf(v);
    if (!ctx.app || root == null || !ctx.app.ci.canWatch(root)) return null;
    return html`<button icon="eye" @tap=${() => push({ kind: 'ci-watch', root, repo: '', ref: '', err: '' })}>Watch CI for…</button>`;
  },
  screen(s) {
    if (s.kind === 'ci-job') return jobScreen(s);
    if (s.kind === 'ci-annotations') return notesScreen(s);
    if (s.kind === 'ci-watch') return watchScreen(s);
    return null;
  },
});

// liveFor: the conversation's CI read once when it opens; re-read every 15 s
// while its Coding agents screen (or a job of it) is shown and anything is not completed;
// at home (no conversation: the list, a page) nothing is
let homeWired = false;
function liveFor(v) {
  const app = ctx.app;
  const root = rootOf(v);
  if (!app) return;
  // back to the list draws no conversation's toolbar: the model going home stops it
  if (!homeWired) { homeWired = true; app.on('home', () => app.ci.live(null, false)); }
  if (root == null) { app.ci.live(null, false); return; }
  if (!read.has(root) && !app.ci.view(root) && app.ci.chip(root)) { read.add(root); app.ci.load(root).catch(() => {}); }
  const shown = (app.ci.chip(root) || app.ci.view(root)) && ui.stack.some((x) => (x.kind === 'hboard' && x.root === root) || (x.kind === 'ci-job' && x.root === root));
  if (shown) app.ci.live(root, true);
  else app.ci.live(null, false);
}

function openJob(root, c) {
  const w = (ctx.app.ci.rows(root) || []).find((x) => x.watch === c.watch);
  const j = w && w.runs.flatMap((r) => r.jobs).find((x) => x.id === c.job);
  if (j) push({ kind: 'ci-job', root, watch: c.watch, job: j.id });
  else push({ kind: 'hboard', root });
}

// --- the sections of the Coding agents screen -------------------------------------------------

function sectionsTpl(v, root) {
  const app = ctx.app;
  const view = app.ci.view(root);
  if (!view) {
    const err = app.ci.error(root);
    return html`<section title="CI">${err ? html`<notice tone="danger" text=${err}/>` : html`<progress label="loading CI…"/>`}</section>`;
  }
  const rows = app.ci.rows(root);
  return html`${repeat(rows, (w) => w.watch, (w) => watchTpl(app, root, view, w))}
    <section title=${rows.length ? '' : 'CI'} footer=${rows.length ? '' : 'Nothing watched yet.'}>
      ${view.canWatch ? html`<row title="Watch CI for…" icon="eye" nav @tap=${() => push({ kind: 'ci-watch', root, repo: '', ref: '', err: '' })}/>` : nothing}
    </section>`;
}

function watchTpl(app, root, view, w) {
  const sub = [w.pr ? `PR #${w.pr}` : '', w.state === 'gone' ? 'branch gone' : w.state,
    w.source === 'task' ? 'the task\'s branch' : w.source === 'pushed' ? 'pushed here' : 'watched by hand', w.since ? 'since ' + when(w.since) : ''].filter(Boolean).join(' · ');
  return html`<section title=${`CI · ${w.title}`} badge=${w.state} footer=${w.error || (w.refusal === 'signin' ? 'Sign in to the provider (Projects → the project\'s settings) to see this CI.' : '')}>
    <row title=${w.title} subtitle=${sub} icon="branch" tone=${TONE[w.tone] || nothing} mono="title">
      <actions>
        ${isHttps(w.urls.branch) ? html`<button icon="external" @tap=${() => open(w.urls.branch)}>Open the branch</button>` : nothing}
        ${isHttps(w.urls.pr) ? html`<button icon="external" @tap=${() => open(w.urls.pr)}>Open PR #${w.pr}</button>` : nothing}
        ${w.source !== 'task' ? html`<button icon="xmark" role="destructive" @tap=${guard(() => app.ci.unwatch(root, w.watch))}>Stop watching</button>` : nothing}
      </actions>
    </row>
    ${w.runs.map((r) => html`<row title=${r.name} subtitle=${[r.event, r.attempt > 1 ? `attempt ${r.attempt}` : '', r.state, elapsed(r.elapsedMs)].filter(Boolean).join(' · ')}
        icon=${icon(r.tone)} tone=${TONE[r.tone] || nothing}>
        <actions>
          ${isHttps(r.url) ? html`<button icon="external" @tap=${() => open(r.url)}>Open the run</button>` : nothing}
          ${view.canRerun && r.failed ? html`<button icon="refresh" confirm=${{ title: `Re-run the failed jobs of ${r.name}?`, message: 'It runs as you, and spends CI time (it may deploy).', label: 'Re-run failed' }}
            @tap=${guard(() => app.ci.rerun(root, { watch: w.watch, runId: r.id, failedOnly: true }))}>Re-run failed</button>` : nothing}
        </actions>
      </row>
      ${r.jobs.map((j) => html`<row title=${j.name} subtitle=${[j.progress.current || (j.status === 'completed' ? j.conclusion : j.status), elapsed(j.elapsedMs)].filter(Boolean).join(' · ')}
          detail=${j.progress.total ? `${j.progress.done}/${j.progress.total}` : nothing} icon=${icon(j.tone)} tone=${TONE[j.tone] || nothing} nav
          @tap=${() => push({ kind: 'ci-job', root, watch: w.watch, job: j.id })}>
        ${j.progress.total ? html`<progress value=${j.progress.pct / 100}/>` : nothing}
      </row>`)}`)}
    ${w.checks.map((k) => html`<row title=${k.name} subtitle=${k.title || k.conclusion || k.status} detail=${k.annotations ? `${k.annotations} annotation${k.annotations === 1 ? '' : 's'}` : nothing}
        icon=${icon(k.tone)} tone=${TONE[k.tone] || nothing} ?nav=${k.annotations > 0} @tap=${() => (k.annotations ? push({ kind: 'ci-annotations', root, watch: w.watch, check: k.id, title: k.name }) : open(k.url))}/>`)}
    ${w.statuses.map((s) => html`<row title=${s.context} subtitle=${s.description || s.state} icon=${icon(s.tone)} tone=${TONE[s.tone] || nothing} @tap=${() => open(s.url)}/>`)}
  </section>`;
}

// --- a job -----------------------------------------------------------------------------------------

function jobOf(root, watch, id) {
  for (const w of ctx.app.ci.rows(root)) {
    if (w.watch !== watch) continue;
    for (const r of w.runs) for (const j of r.jobs) if (j.id === id) return { w, r, j };
  }
  return null;
}

async function loadLog(s, more = {}) {
  s.busy = true;
  try {
    const r = await ctx.app.ci.log(s.root, s.watch, s.job, more);
    if (more.until) { s.log = { ...s.log, text: r.text + s.log.text, from: r.from, truncated: r.truncated }; }
    else if (more.since) { if (r.from >= s.log.bytes) s.log.text += r.text; s.log.bytes = Math.max(s.log.bytes, r.bytes); s.log.complete = r.complete; }
    else s.log = r;
    s.err = '';
  } catch (e) { s.err = (e && e.message) || String(e); }
  s.busy = false;
  ctx.paint();
}

function follow(s) {
  s.follow = !s.follow;
  if (s.followT) clearInterval(s.followT);
  s.followT = null;
  if (s.follow) {
    s.followT = setInterval(() => {
      // the screen gone, the log complete, or a read failed (as on the web): it stops
      if (!ui.stack.includes(s) || !s.log || s.log.complete || s.err) { clearInterval(s.followT); s.followT = null; s.follow = false; ctx.paint(); return; }
      loadLog(s, { since: s.log.bytes });
    }, 5000);
  }
  ctx.paint();
}

function jobScreen(s) {
  const x = jobOf(s.root, s.watch, s.job);
  if (!s.log && !s.busy && !s.err) loadLog(s);
  const j = x && x.j;
  const live = (s.log && s.log.url) || (j && j.url) || '';
  const q = String(s.q || '').trim().toLowerCase();
  const lines = s.log && q ? s.log.text.split('\n').map((l, i) => [i, l]).filter(([, l]) => l.toLowerCase().includes(q)) : [];
  return html`<screen title=${j ? j.name : 'Job'} subtitle=${x ? `${x.r.name} · ${x.w.title}` : ''} style="list" search=${s.q || ''}
      @search=${(e) => { s.q = e.value; ctx.paint(); }}>
    <toolbar>
      ${isHttps(live) ? html`<button icon="external" @tap=${() => open(live)}>Open live log</button>` : nothing}
      ${s.log && !s.log.inProgress && !s.log.complete ? html`<button icon="play" @tap=${() => follow(s)}>${s.follow ? 'Stop following' : 'Follow'}</button>` : nothing}
      ${j && j.annotations && j.check ? html`<button icon="warning" @tap=${() => push({ kind: 'ci-annotations', root: s.root, watch: s.watch, check: j.check, title: j.name })}>Annotations</button>` : nothing}
    </toolbar>
    ${s.err ? html`<section><notice tone="danger" text=${s.err}/></section>` : nothing}
    ${j ? html`<section title="Steps" footer=${j.progress.total ? `${j.progress.done} of ${j.progress.total} done` : ''}>
      ${j.steps.length ? j.steps.map((st) => html`<row title=${st.name} detail=${elapsed(st.elapsedMs) || nothing} icon=${icon(st.tone)} tone=${TONE[st.tone] || nothing}/>`)
        : html`<empty title="no steps yet"/>`}
    </section>` : nothing}
    ${s.log && s.log.inProgress ? html`<section title="Log">
        <notice tone="muted" text="The job is still running: the log is ready when the job finishes."/>
        ${isHttps(live) ? html`<row title="Open live log" subtitle=${live} icon="external" @tap=${() => open(live)}/>` : nothing}
      </section>`
    : s.log ? html`${q ? html`<section title=${`Matches (${lines.length})`}>
          ${lines.length ? html`<code text=${lines.slice(0, 500).map(([i, l]) => `${i + 1}: ${l}`).join('\n')} wrap/>` : html`<empty title=${`nothing matches “${s.q}”`}/>`}
        </section>` : nothing}
        <section title="Log" footer=${s.log.truncated ? 'the end of the log — Earlier reads what comes before' : ''}>
          ${s.log.truncated ? html`<button icon="refresh" ?busy=${!!s.busy} @tap=${() => loadLog(s, { until: s.log.from })}>Earlier</button>` : nothing}
          <code text=${s.log.text} copy/>
        </section>`
    : html`<section><progress label="reading the log…"/></section>`}
  </screen>`;
}

// --- a check's annotations ---------------------------------------------------------------------------

function notesScreen(s) {
  if (!s.items && !s.busy && !s.err) {
    s.busy = true;
    ctx.app.ci.annotations(s.root, s.watch, s.check).then((a) => { s.items = a; }, (e) => { s.err = e.message; }).finally(() => { s.busy = false; ctx.paint(); });
  }
  const tone = { failure: 'danger', warning: 'warn', notice: 'muted' };
  return html`<screen title="Annotations" subtitle=${s.title || ''} style="list">
    ${s.err ? html`<section><notice tone="danger" text=${s.err}/></section>` : nothing}
    <section>
      ${s.items ? (s.items.length ? s.items.map((a) => html`<row title=${`${a.path}:${a.startLine}`} subtitle=${[a.title, a.message].filter(Boolean).join(' — ')}
          detail=${a.level} tone=${tone[a.level] || nothing} mono="title"/>`) : html`<empty title="no annotations"/>`) : html`<progress label="loading…"/>`}
    </section>
  </screen>`;
}

// --- Watch CI for… ------------------------------------------------------------------------------------

function watchScreen(s) {
  const submit = async () => {
    const repo = String(s.repo || '').trim(), ref = String(s.ref || '').trim().replace(/^#/, '');
    if (!/^[\w.-]+\/[\w.-]+$/.test(repo) || !ref) { s.err = 'Name a repo (owner/name) and a branch or a pull request number.'; ctx.paint(); return; }
    s.busy = true; s.err = '';
    ctx.paint();
    try {
      await ctx.app.ci.watch(s.root, /^\d+$/.test(ref) ? { repo, pr: +ref } : { repo, ref });
      if (top() === s) ui.stack.pop();
    } catch (e) { s.err = e.message; }
    s.busy = false;
    ctx.paint();
  };
  return html`<screen title="Watch CI for…" style="form">
    <toolbar><button icon="eye" role="primary" ?busy=${!!s.busy} @tap=${submit}>Watch</button></toolbar>
    ${s.err ? html`<notice tone="danger" text=${s.err}/>` : nothing}
    <section footer="A branch, or a pull request's number (its head branch is watched).">
      <field label="Repo" placeholder="owner/repo" value=${s.repo || ''} @input=${(e) => { s.repo = e.value; }}/>
      <field label="Branch or PR" placeholder="feature, or 42" value=${s.ref || ''} submit="go" @input=${(e) => { s.ref = e.value; }} @submit=${submit}/>
    </section>
  </screen>`;
}
