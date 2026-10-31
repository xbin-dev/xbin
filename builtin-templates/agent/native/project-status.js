// native/project-status.js — a project's overview on its native screen (D214;
// API.md §Projects in the UI): model/project-overview.js drawn with the app's
// primitives for native/projects.js's 'project' screen, which keeps the web
// page's order — the status, the board, the coordinator:
//
//   overviewOf(pv)       the overview to draw, or null: a backend from before
//                        it (its 404) — the screen as it was, coordinator first
//   statusTpl(pv, s, it) the Status section: the coordinator, worker agents,
//                        subagents, tokens (Day | Week and a chart — stacked
//                        areas where the app has them, else lines), source
//                        control (the provider's title; a row per repo, a tap
//                        opens it), the sandbox (CPU and memory sparklines).
//                        It folds to one row (collapsible; kept in your prefs:
//                        the model's folded/setFolded). A tap on a part opens
//                        what it counts — the web's tooltip (TIPS) with its
//                        numbers in full: aboutSheet
//   agentWords(it, n)    a task row's agent: which, what it does, its subagents
//   goalTpl(s, it)       the coordinator section's goal row; idle, what wakes it
//   coordBadge(pr, it)   the coordinator's state, for its section's badge
//   aboutSheet(s, it)    the sheet of the part s.about
//
// The overview is read when the screen first shows, on pull to refresh, on
// the project's `project` events and every REFRESH while its screen is on
// top (o.show / o.hide from the `drawn` seam, below). Text from the scm
// provider is drawn as text (the model's plain()), its links opened only when
// https. Charts are gated on what the app draws (`chart`, `chart.area`).
import { html, nothing, repeat, native } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ctx, top } from './ui.js';
import { openUrl } from './project-task.js';
import { projectOverview, stripWords, foldLine, cardAgent, goalWords, coordWords, tokenChart, fmtTokens, TIPS } from '../model/project-overview.js';

const TONE = { run: 'accent', ok: 'ok', bad: 'danger', warn: 'warn', idle: 'muted' };
const CHECKS = { success: ['ok', 'checks passed'], failure: ['danger', 'checks failed'], error: ['danger', 'checks failed'], pending: ['accent', 'checks running'] };
const RANGES = [{ value: 'day', label: 'Day' }, { value: 'week', label: 'Week' }];

const has = (name) => { try { return !!native.supports?.(name); } catch { return false; } };
const count = (x, one, many = `${one}s`) => `${x} ${x === 1 ? one : many}`;
const join = (...xs) => xs.filter(Boolean).join(' · '); // (counts come as words: "0 turns")
const agoWords = (a) => (!a ? '' : a === 'now' ? 'just now' : `${a} ago`);
const turnWords = (c) => (c.since === 'turn now' ? 'turn just started' : c.since); // (ago() under a minute: 'now')
const wakeList = (ws) => ws.map((w) => w.label).filter(Boolean).join(', ') || 'your message';
const about = (s, key) => () => { s.about = key; ctx.paint(); };

/** overviewOf(pv, s): the project's overview store entry, read on first use — null for a team
 * definition or where the backend has no overview (the screen as before, D214). s: the
 * screen's entry, which notes whether its first draw read it (the `drawn` hook). */
export function overviewOf(pv, s) {
  if (!pv || pv.kind === 'team') return null;
  const o = projectOverview(ctx.app);
  if (s && s.ovRead == null) { const x = o.item(pv.id); s.ovRead = !x.loaded && !x.loading; }
  const it = o.ensure(pv.id);
  return it.supported ? it : null;
}

// --- the status ----------------------------------------------------------------------------------

/** statusTpl(pv, s, it): the Status section (s the screen's entry: the part whose sheet is open). */
export function statusTpl(pv, s, it) {
  const o = projectOverview(ctx.app);
  const d = it.data;
  const sec = (kids, footer) => html`<section title="Status" collapsible ?collapsed=${o.folded} footer=${footer || nothing}
      @toggle=${(e) => o.setFolded(!!e.collapsed)}>${kids}</section>`;
  if (!d) {
    return sec(it.err ? html`<notice tone="danger" text=${`Couldn't read the project's status: ${it.err}`}/>
      <row title="Read it again" icon="refresh" @tap=${() => o.load(pv.id)}/>` : html`<progress label="loading…"/>`);
  }
  const [c, w, sa, t, scm, sb] = stripWords(d);
  const rows = html`
    <row title="Coordinator" icon="agent" detail=${c.word} tone=${TONE[c.tone] || nothing} @tap=${about(s, 'coordinator')}
      subtitle=${join(turnWords(c), c.line, c.last && `last: ${join(c.last.label, agoWords(c.last.ago))}`, `${count(c.turnsToday, 'turn')} today`, `${count(c.tasksMade7d, 'task')} / 7 d`)}/>
    <row title="Worker agents" icon="people" detail=${w.slots ? `${w.slots.used}/${w.slots.max} slots` : String(w.total)}
      badge=${w.needsYou ? String(w.needsYou) : nothing} tone=${w.needsYou ? 'warn' : nothing} @tap=${about(s, 'workers')}
      subtitle=${join(count(w.total, 'task'), w.working && `${w.working} working`, w.needsYou && `${w.needsYou} need you`, w.idle && `${w.idle} idle`,
        w.queued && `${w.queued} queued`, w.engines.map((e) => `${e.count} ${e.title}`).join(', '))}/>
    <row title="Subagents" icon="branch" detail=${`${sa.running} running`} @tap=${about(s, 'subagents')}
      subtitle=${join(sa.waiting && `${sa.waiting} waiting`, `${sa.finished24h} finished in 24 h`, sa.failed24h && `${sa.failed24h} failed`,
        sa.by.length && `under ${sa.by.map((x) => (x.count > 1 ? `${x.label} ×${x.count}` : x.label)).join(', ')}`)}/>
    <row title="Tokens" icon="chart" detail=${t.big} @tap=${about(s, 'tokens')}
      subtitle=${t.total ? join(t.span, `workers ${fmtTokens(t.parts.workers)}`, `coordinator ${fmtTokens(t.parts.coordinator)}`, `subagents ${fmtTokens(t.parts.subagents)}`,
        t.cost && `+ ${t.cost} coding agents reported`) : `none in the ${t.span}`}/>
    <picker style="segmented" value=${it.range} options=${RANGES} @change=${(e) => o.setRange(pv.id, e.value)}/>
    ${tokensChart(d)}
    <row title=${scm.title} icon="code" tone=${scm.pulls.failing ? 'danger' : nothing} @tap=${about(s, 'scm')}
      subtitle=${scm.repos.length ? join(`${count(scm.pulls.open || 0, 'PR')} of its tasks open`, scm.pulls.failing && `${scm.pulls.failing} failing`,
        scm.pulls.review && `${scm.pulls.review} awaiting review`, scm.stats && count(scm.openIssues, 'open issue'), scm.stats && `${scm.merged7d} merged in 7 d`) : 'no repos'}/>
    ${repeat(scm.repos, (r) => r.repo, repoRow)}
    ${sandboxRow(sb, d, about(s, 'sandbox'))}`;
  const line = foldLine(d);
  const tone = line.some((x) => x.tone === 'bad') ? 'danger' : line.some((x) => x.tone === 'warn') ? 'warn' : nothing;
  return html`${sec(rows, it.err ? `Couldn't read it again: ${it.err}` : 'Tap a part for what it counts.')}
    ${o.folded ? html`<section><row title=${join(...line.slice(0, 2).map((x) => x.text))} subtitle=${join(...line.slice(2).map((x) => x.text))}
      tone=${tone} @tap=${() => o.setFolded(false)}/></section>` : nothing}`;
}

// the tokens over the range: stacked where the app draws areas (each line the top of its
// band — an area is drawn unstacked), else a line each
function tokensChart(d) {
  if (!has('chart')) return nothing;
  const k = tokenChart(d);
  if (!k.t.some((_, i) => k.workers[i] + k.coordinator[i] + k.subagents[i] > 0)) return nothing;
  const area = has('chart.area');
  const at = (ys) => k.t.map((t, i) => [t, ys[i]]);
  const co = area ? k.coordinator.map((v, i) => v + k.subagents[i]) : k.coordinator;
  const wk = area ? k.workers.map((v, i) => v + co[i]) : k.workers;
  return html`<chart kind=${area ? 'area' : 'line'} x="time" y="number" height="s"
    series=${[{ name: 'worker agents', points: at(wk) }, { name: 'coordinator', points: at(co) }, { name: 'subagents', points: at(k.subagents) }]}/>`;
}

// a repo at its provider: its default branch's checks, open PRs and issues, merged in 7 days — a tap opens it
function repoRow(r) {
  const ck = r.checks && CHECKS[r.checks.state];
  return html`<row title=${r.repo} mono="title" icon="branch" tone=${ck ? ck[0] : nothing} ?nav=${!!r.url} ?disabled=${!r.url} @tap=${() => openUrl(r.url)}
    subtitle=${join(r.branch, ck && ck[1], r.openPulls != null && count(r.openPulls, 'open PR'), r.openIssues != null && count(r.openIssues, 'open issue'),
      r.merged7d != null && `${r.merged7d} merged in 7 d`, r.err)}/>`;
}

// the project's sandbox: its state, size, disk and commands; CPU and memory over the last hour
function sandboxRow(sb, d, tap) {
  if (sb.none) return html`<row title="Sandbox" icon="box" subtitle="none" @tap=${tap}/>`;
  const spark = sb.series.length > 1 && has('chart');
  const at = (key) => [{ name: key, points: sb.series.map((p) => [p.t, +p[key] || 0]) }];
  return html`<row title="Sandbox" icon="box" detail=${sb.state || nothing} tone=${sb.running ? 'ok' : nothing} @tap=${tap}
      subtitle=${join(sb.name, sb.vcpus && `${sb.vcpus} vCPU`, sb.mem, sb.disk && `disk ${sb.disk}${sb.diskMax ? ` of ${sb.diskMax}` : ''}`,
        `${count(sb.commands, 'command')} running`, !spark && sb.cpu && `CPU ${sb.cpu}`, !spark && sb.memUsed && `memory ${sb.memUsed}`)}>
    ${spark ? html`<stack axis="h" gap="s" align="center">
      <text style="caption" tone="muted">${`CPU ${sb.cpu || '–'}`}</text><chart kind="spark" x="time" series=${at('cpu')}/>
      <text style="caption" tone="muted">${`Memory ${sb.memUsed || '–'}`}</text><chart kind="spark" x="time" series=${at('mem')}/>
    </stack>` : nothing}
  </row>`;
}

// --- the board's rows and the coordinator's section --------------------------------------------

/** agentWords(it, n): task n's agent in words — "Claude Code: working · 2 subagents" ('' unknown). */
export function agentWords(it, n) {
  const a = it && it.data ? cardAgent(it.data, n) : null;
  return a ? join(`${a.title}: ${a.state}`, a.subagents && count(a.subagents, 'subagent'), a.tokens && `${a.tokens} tokens`) : '';
}

/** coordBadge(pr, it): what the coordinator does — its run's state as it moves (the preview's),
 * else the overview's word. */
export function coordBadge(pr, it) {
  if (pr && pr.shown && pr.state.word) return pr.state.word;
  if (pr && pr.shown && pr.run) return 'idle';
  const d = it && it.data;
  return d && d.coordinator ? coordWords(d).word.toLowerCase() : '';
}

/** goalTpl(s, it): the goal row (a tap: all of it) — idle, what wakes it, then the goal. */
export function goalTpl(s, it) {
  const d = it && it.data;
  if (!d) return nothing;
  const g = goalWords(d);
  if (g.kind === 'idle') {
    return html`<row title=${`Wakes for ${wakeList(g.wakes)}`} subtitle=${g.text ? `Goal: ${g.text}` : nothing} icon="clock" @tap=${about(s, 'idle')}/>`;
  }
  if (g.kind !== 'goal') return nothing;
  return html`<row title=${`Goal: ${g.text}`} subtitle=${join(g.now && `now: ${g.now}`, g.by === 'you' && 'your latest message to it — it has set no goal yet') || nothing}
    icon="pin" @tap=${about(s, 'goal')}/>`;
}

// --- what a part counts (the web's tooltips) ---------------------------------------------------

// each part's numbers in full: [title, value] ('' or null: left out)
const FACTS = {
  coordinator: (d) => {
    const c = coordWords(d);
    return [['State', join(c.word, turnWords(c))], ['Now', c.line], ['Last event', c.last && join(c.last.label, agoWords(c.last.ago))], ['Turns today', c.turnsToday],
      ['Tasks made in 7 days', c.tasksMade7d], ['Model', c.model], ['Tokens today', c.tokensToday ? fmtTokens(c.tokensToday) : '']];
  },
  workers: (d) => {
    const w = stripWords(d)[1];
    return [['Tasks', w.total], ['Working', w.working], ['Need you', w.needsYou], ['Idle', w.idle], ['Queued', w.queued],
      ['Slots in use', w.slots && `${w.slots.used} of ${w.slots.max}`], ...w.engines.map((e) => [e.title, count(e.count, 'task')])];
  },
  subagents: (d) => {
    const sa = stripWords(d)[2];
    return [['Running', sa.running], ['Waiting', sa.waiting], ['Finished in 24 h', sa.finished24h], ['Failed in 24 h', sa.failed24h],
      ...sa.by.map((x) => [`Under ${x.label}`, x.count])];
  },
  tokens: (d) => {
    const t = stripWords(d)[3];
    return [[`All, ${t.span}`, t.big], ['Worker agents', fmtTokens(t.parts.workers)], ['Coordinator', fmtTokens(t.parts.coordinator)],
      ['Subagents', fmtTokens(t.parts.subagents)], ['Coding agents reported', t.cost]];
  },
  scm: (d) => {
    const scm = stripWords(d)[4];
    return [['Provider', scm.title], ['Its tasks\' PRs open', scm.pulls.open || 0], ['Failing', scm.pulls.failing || 0], ['Awaiting review', scm.pulls.review || 0],
      ['Repo counts', scm.stats ? '' : 'the provider does not report them'], ...scm.repos.filter((r) => r.err).map((r) => [r.repo, r.err])];
  },
  sandbox: (d) => {
    const sb = stripWords(d)[5];
    return sb.none ? [] : [['Name', sb.name], ['State', sb.state], ['Size', join(sb.vcpus && `${sb.vcpus} vCPU`, sb.mem)], ['Disk', sb.disk && `${sb.disk}${sb.diskMax ? ` of ${sb.diskMax}` : ''}`],
      ['Commands running', sb.commands], ['CPU now', sb.cpu], ['Memory now', sb.memUsed && `${sb.memUsed}${sb.mem ? ` of ${sb.mem}` : ''}`]];
  },
  goal: (d) => {
    const g = goalWords(d);
    return [['Goal', g.text], ['Now', g.now], ['Said by', g.by === 'you' ? 'you (your latest message)' : g.by ? 'the coordinator' : '']];
  },
  idle: (d) => goalWords(d).wakes.map((x) => ['Wakes for', x.label]),
};

/** aboutSheet(s, it): what the part s.about counts, and its numbers (the web's tooltip). */
export function aboutSheet(s, it) {
  if (!s.about || !it || !it.data) return nothing;
  const tip = TIPS[s.about] || '';
  const dot = tip.indexOf('. ');
  const title = dot > 0 ? tip.slice(0, dot) : 'About';
  const close = () => { s.about = ''; ctx.paint(); };
  const facts = (FACTS[s.about] || (() => []))(it.data).filter(([, v]) => v || v === 0);
  return html`<sheet open title=${title} detents=${['medium', 'large']} @dismiss=${close}>
    <screen title=${title} style="list">
      <toolbar><button role="primary" @tap=${close}>Done</button></toolbar>
      <section><text>${dot > 0 ? tip.slice(dot + 2) : tip}</text></section>
      ${facts.length ? html`<section>${repeat(facts, ([k], i) => `${i}:${k}`, ([k, v]) => (String(v).length > 24
        ? html`<row title=${k} subtitle=${String(v)}/>` : html`<row title=${k} detail=${String(v)}/>`))}</section>` : nothing}
      ${s.about === 'sandbox' ? usageCharts(it.data) : nothing}
    </screen>
  </sheet>`;
}

// the sandbox's last hour with axes: CPU (of one core) and memory
function usageCharts(d) {
  const ser = (d.sandbox && d.sandbox.usage && d.sandbox.usage.series) || [];
  if (ser.length < 2 || !has('chart')) return nothing;
  return html`<section title="The last hour">
    <text style="caption" tone="muted">CPU, of one core</text>
    <chart kind="line" x="time" y="percent" height="s" series=${[{ name: 'CPU', points: ser.map((p) => [p.t, (+p.cpu || 0) / 100]) }]}/>
    <text style="caption" tone="muted">Memory</text>
    <chart kind="line" x="time" y="bytes" height="s" series=${[{ name: 'memory', points: ser.map((p) => [p.t, +p.mem || 0]) }]}/>
  </section>`;
}

// --- read again while it shows -----------------------------------------------------------------

// drawn: a project's screen on top keeps its overview current (the model's
// timer and `project` events); covered or gone, nothing is read for it. Back
// on top — or a screen opened on what an earlier one read — it is read at once
// (the entry's ovRead: its first draw started the read; ovShown: it was on top).
ext.register({
  drawn() {
    const o = ctx.app && ctx.app.projOverview;
    if (!o) return;
    const t = top();
    const it = t && t.kind === 'project' ? o.items.get(+t.pid) : null;
    if (!it || !it.supported) { o.hide(); return; }
    if (o.shown === +t.pid) return;
    o.show(t.pid);
    if (t.ovShown || !t.ovRead) o.load(t.pid);
    t.ovShown = true;
  },
});

