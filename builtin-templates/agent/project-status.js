// project-status.js — a project's status on its overview page (D214; API.md
// §Projects in the UI), on the web: the words are model/project-overview.js's,
// these draw them.
//
//   stripTpl(o, it, pid)  the status strip (#pov-strip, ~125 px): six cells —
//                         coordinator, worker agents, subagents, tokens (Day
//                         or Week, a stacked chart), source control (the
//                         provider's title as the label; ↗ only for https),
//                         sandbox (CPU and memory over the last hour) — or,
//                         folded (o.folded, kept per person), its one line
//                         (#pov-line); a placeholder of the same height while
//                         it is first read, and Retry when that failed
//   foldTpl(o)            "Status ▾" for the page's top bar (#pov-fold)
//   agentLineTpl(a)       a board card's agent line (cardAgent): who works
//                         on it, what it does, its running subagents
//   cardTip(t, a)         the card's tooltip
//
// Every cell, chip and button carries its tooltip (data-tip, TIPS;
// tooltips.js shows them). Text from the scm provider is plain, links only
// https (the model's words already are).
import { html, svg, nothing } from '/vendor/lit-all.min.js';
import { stripWords, foldLine, tokenChart, sparkline, fmtTokens, TIPS } from './model/project-overview.js';
import './tooltips.js';

const caret = (open) => html`<bx-icon name=${open ? 'caret-down' : 'caret-right'}></bx-icon>`;
const help = html`<bx-icon class="ihelp" name="question"></bx-icon>`;
const dot = (tone) => html`<span class="pdot" data-tone=${tone || 'idle'}></span>`;

/** foldTpl(o): the top bar's Status ▾ — folds the strip to one line. */
export function foldTpl(o) {
  return html`<button class="btn ghost btnsm povfold" id="pov-fold" aria-expanded=${o.folded ? 'false' : 'true'}
    data-tip=${TIPS.fold} @click=${() => o.setFolded(!o.folded)}>Status${caret(!o.folded)}</button>`;
}

/** stripTpl(o, it, pid): the strip, its folded line, or its placeholder. */
export function stripTpl(o, it, pid) {
  const d = it.data;
  if (!d) {
    return html`<div class="povstrip povwait" id="pov-strip" role="region" aria-label="Project status">
      <div class="povc">${it.err ? html`<span class="err small">Couldn't read the project's status: ${it.err}</span>
        <button class="btn ghost btnsm" id="pov-retry" @click=${() => { it.err = ''; o.load(pid); }}>Retry</button>`
        : html`<span class="muted small">Reading the project's status…</span>`}</div></div>`;
  }
  if (o.folded) {
    const unfold = () => o.setFolded(false);
    return html`<div class="povline" id="pov-line" role="button" tabindex="0" data-tip=${TIPS.line} @click=${unfold}
        @keydown=${(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); unfold(); } }}>
      ${foldLine(d).map((x) => html`<span data-key=${x.key} data-tone=${x.tone || nothing}>${x.key === 'coordinator' ? dot(x.tone) : nothing}${x.text}</span>`)}
      <span class="sp"></span>${caret(false)}</div>`;
  }
  const [c, w, s, t, scm, sb] = stripWords(d);
  return html`<div class="povstrip" id="pov-strip" role="region" aria-label="Project status">
    ${cell(c, coordCell(c))}${cell(w, workersCell(w))}${cell(s, subsCell(s))}
    ${cell(t, tokensCell(t, d, o, it, pid), rangeTpl(o, it, pid))}${cell(scm, scmCell(scm))}
    ${cell(sb, sandboxCell(sb), sb.name ? html`<span class="mono nm">${sb.name}</span>` : nothing)}
  </div>`;
}

function cell(x, body, head = nothing) {
  return html`<div class="povc" data-cell=${x.key} tabindex="0" role="group" aria-label=${x.title} data-tip=${x.tip}>
    <h4><span>${x.title}</span>${head}<span class="sp"></span>${x.key === 'tokens' ? nothing : help}</h4>${body}</div>`;
}

const kv = (...parts) => html`<div class="kv">${parts}</div>`;
const num = (n) => html`<b>${n}</b>`;
const stack = (parts) => html`<div class="stack" aria-hidden="true">${parts.filter(([n]) => n > 0).map(([n, tone]) => html`<span data-tone=${tone} style=${`flex:${n}`}></span>`)}</div>`;
const legend = (parts) => html`<div class="legend">${parts.map(([text, tone]) => html`<span>${tone ? html`<i data-tone=${tone}></i>` : nothing}${text}</span>`)}</div>`;

function coordCell(c) {
  return html`<div class="big">${dot(c.tone)}<span>${c.word}</span>${c.since ? html`<small>${c.since}</small>` : nothing}</div>
    <div class="ln">${c.line || nothing}</div>
    ${c.last ? html`<div class="kv muted" data-tip=${TIPS.last}><span>last:</span>${num(c.last.label)}${c.last.ago ? html`<span>· ${c.last.ago}</span>` : nothing}</div>` : nothing}
    ${kv(num(c.turnsToday), html`<span class="muted">turns today ·</span>`, num(c.tasksMade7d), html`<span class="muted">tasks / 7 d</span>`)}`;
}

function workersCell(w) {
  return html`<div class="big"><span>${w.total}</span><small>tasks</small>${w.slots ? html`<span>· ${w.slots.used}/${w.slots.max}</span><small data-tip=${TIPS.slots}>slots</small>` : nothing}</div>
    ${stack([[w.working, 'run'], [w.needsYou, 'warn'], [w.idle, 'idle'], [w.queued, 'none']])}
    ${legend([[`${w.working} working`, 'run'], [`${w.needsYou} need you`, 'warn'], [`${w.idle} idle`, 'idle'], ...(w.queued ? [[`${w.queued} queued`, 'none']] : [])])}
    <div class="kv chips">${w.engines.map((e) => html`<span class="chip" data-engine=${e.engine} data-tip=${TIPS.engines}>${e.count} ${e.title}</span>`)}</div>`;
}

function subsCell(s) {
  return html`<div class="big"><span>${s.running}</span><small>running ·</small><span>${s.waiting}</span><small>waiting</small></div>
    ${stack([[s.running, 'sub'], [s.waiting, 'warn'], [s.finished24h, 'none']])}
    ${kv(num(s.finished24h), html`<span class="muted">finished (24 h) ·</span>`, num(s.failed24h), html`<span class="muted">failed</span>`)}
    ${s.by.length ? html`<div class="kv muted"><span>on</span>${s.by.map((x, i) => html`${num(x.label)}${x.count > 1 ? html`<span>×${x.count}</span>` : nothing}${i < s.by.length - 1 ? html`<span>,</span>` : nothing}`)}</div>` : nothing}`;
}

function rangeTpl(o, it, pid) {
  const r = it.range === 'week' ? 'week' : 'day';
  const set = (x) => () => { if (x !== r) o.setRange(pid, x); };
  return html`<span class="seg" id="pov-range" role="group" aria-label="Range" data-tip=${TIPS.range}>
    <button data-range="day" aria-pressed=${r === 'day' ? 'true' : 'false'} @click=${set('day')}>Day</button><button data-range="week" aria-pressed=${r === 'week' ? 'true' : 'false'} @click=${set('week')}>Week</button></span>`;
}

function tokensCell(t, d) {
  const p = t.parts || {};
  return html`<div class="big"><span>${t.big}</span><small>${t.span}</small></div>
    ${legend([[`work ${fmtTokens(p.workers)}`, 'run'], [`coord ${fmtTokens(p.coordinator)}`, 'coord'], [`sub ${fmtTokens(p.subagents)}`, 'sub']])}
    ${t.cost ? html`<div class="kv muted" data-tip=${TIPS.cost}><span>+</span>${num(t.cost)}<span>coding agents</span></div>` : nothing}
    ${stackedTpl(tokenChart(d))}`;
}

const CHECK = { success: ['ok', 'ok', 'passing'], failure: ['bad', 'error', 'failing'], error: ['bad', 'error', 'failing'], cancelled: ['bad', 'error', 'cancelled'],
  pending: ['run', 'wait', 'running'], running: ['run', 'wait', 'running'], queued: ['run', 'wait', 'queued'] };
const REPOS = 2; // the repos a cell has room for

function scmCell(x) {
  const pulls = x.pulls || {};
  return html`${x.repos.slice(0, REPOS).map((r) => {
    const [tone, icon, word] = (r.checks && CHECK[r.checks.state]) || ['idle', '', ''];
    return html`<div class="repo" data-repo=${r.repo}>${dot(tone)}
      ${r.url ? html`<a href=${r.url} target="_blank" rel="noopener noreferrer" data-tip=${TIPS.repo}>${r.repo} ↗</a>` : html`<span class="ellip">${r.repo}</span>`}
      ${r.branch ? html`<span class="muted mono" data-tip=${`${r.branch}. Its checks: ${word || 'none reported'}.`}>${r.branch}${icon ? html`<bx-icon name=${icon}></bx-icon>` : nothing}</span>` : nothing}
      ${r.err ? html`<bx-icon class="rerr" name="warning" data-tip=${`Couldn't read it. ${r.err}`}></bx-icon>` : nothing}</div>`;
  })}
    ${x.repos.length > REPOS ? html`<div class="kv muted">+${x.repos.length - REPOS} more</div>` : nothing}
    <div class="kv">${num(pulls.open || 0)}<span class="muted">PRs</span>
      ${pulls.failing ? html`<span class="chip" data-tone="bad" data-tip=${TIPS.prFailing}>${pulls.failing}<bx-icon name="error"></bx-icon></span>` : nothing}
      ${pulls.review ? html`<span class="chip" data-tone="warn" data-tip=${TIPS.prReview}>${pulls.review} review</span>` : nothing}</div>
    ${x.stats ? kv(num(x.openIssues), html`<span class="muted">issues ·</span>`, num(x.merged7d), html`<span class="muted">merged / 7 d</span>`) : nothing}`;
}

const SB_TONE = { running: 'ok', starting: 'run', creating: 'run', stopping: 'run', error: 'bad', failed: 'bad' };
const cap = (s) => (s ? s[0].toUpperCase() + s.slice(1) : 'Unknown');

function sandboxCell(sb) {
  if (sb.none) return html`<div class="ln muted">No sandbox yet: the first task's workspace makes it.</div>`;
  const cpu = sparkline(sb.series, 'cpu'), mem = sparkline(sb.series, 'mem');
  return html`<div class="big">${dot(SB_TONE[sb.state] || 'idle')}<span>${cap(sb.state)}</span><small>${sb.commands} cmd${sb.commands === 1 ? '' : 's'}</small></div>
    <div class="kv muted" data-tip=${sb.disk ? `Disk: ${sb.disk} used${sb.diskMax ? ` of ${sb.diskMax}` : ''}.` : nothing}><span>${[sb.vcpus ? `${sb.vcpus} vCPU` : '', sb.mem].filter(Boolean).join(' · ')}</span>${sb.disk ? html`<span>·</span>${num(sb.diskShort || sb.disk)}` : nothing}</div>
    ${cpu.length > 1 ? html`<div class="spark2">
      <div data-tip=${TIPS.cpu}><div class="lbl"><span>CPU</span><b>${sb.cpu}</b></div>${lineTpl(cpu, 100, 'run', 'CPU, the last hour')}</div>
      <div data-tip=${TIPS.mem}><div class="lbl"><span>Mem</span><b>${sb.memUsed}</b></div>${lineTpl(mem, sb.memMax || 0, 'coord', 'Memory, the last hour')}</div>
    </div>` : html`<div class="ln muted">${sb.running ? 'CPU and memory aren\'t reported here.' : ''}</div>`}`;
}

// --- charts (SVG, drawn in the theme's colours: .povch [data-tone]) --------------------------

function stackedTpl(ch) {
  const n = ch.t.length;
  if (n < 2) return html`<div class="ln muted">No tokens counted yet.</div>`;
  const W = 200, H = 34, max = ch.max * 1.08;
  const x = (i) => ((i / (n - 1)) * W).toFixed(1), y = (v) => (H - (v / max) * H).toFixed(1);
  const a = ch.workers, b = a.map((v, i) => v + ch.coordinator[i]), c = b.map((v, i) => v + ch.subagents[i]), z = a.map(() => 0);
  const layer = (top, bot, tone) => svg`<path data-tone=${tone} d=${`M${top.map((v, i) => `${x(i)},${y(v)}`).join(' L')} L${bot.map((_, i) => `${x(n - 1 - i)},${y(bot[n - 1 - i])}`).join(' L')} Z`}/>`;
  return html`<svg class="povch spark" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" role="img" aria-label="tokens over the range: workers, coordinator, subagents">
    ${layer(a, z, 'run')}${layer(b, a, 'coord')}${layer(c, b, 'sub')}</svg>`;
}

function lineTpl(vals, top, tone, label) {
  const n = vals.length, W = 100, H = 30, max = Math.max(top, ...vals) || 1;
  const x = (i) => ((i / (n - 1)) * W).toFixed(1), y = (v) => (H - (v / max) * (H - 2) - 1).toFixed(1);
  const pts = vals.map((v, i) => `${x(i)},${y(v)}`).join(' L');
  return html`<svg class="povch spark" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" role="img" aria-label=${label}>
    ${svg`<path class="area" data-tone=${tone} d=${`M0,${H} L${pts} L${W},${H} Z`}/><path class="line" data-tone=${tone} d=${`M${pts}`}/>`}</svg>`;
}

const subs = (n) => `${n} subagent${n === 1 ? '' : 's'}`;

/** agentLineTpl(a): a board card's agent line (a: model/project-overview.js cardAgent). */
export function agentLineTpl(a) {
  return html`<div class="ptagent">${dot(a.tone)}<span>${[a.title, a.state, a.subagents ? subs(a.subagents) : '', a.tokens ? `${a.tokens} tokens` : ''].filter(Boolean).join(' · ')}</span></div>`;
}

/** cardTip(t, a): a card's tooltip (its title is the card's number and title). */
export function cardTip(t, a) {
  const who = a ? ` Its agent: ${a.title}, ${a.state}${a.subagents ? `, with ${subs(a.subagents)} running` : ''}.` : '';
  return `${t.run ? 'Opens its conversation.' : 'Its conversation was deleted.'}${who}`;
}

const style = document.createElement('style');
style.textContent = `
  .pdot { width: 8px; height: 8px; flex: none; display: inline-block; background: var(--bx-muted); border-radius: var(--bx-radius); }
  .pdot[data-tone="run"] { background: var(--bx-accent); animation: povpulse 1.6s ease-in-out infinite; }
  .pdot[data-tone="ok"] { background: var(--bx-ok); }
  .pdot[data-tone="warn"] { background: var(--bx-warn); }
  .pdot[data-tone="bad"] { background: var(--bx-danger); }
  @keyframes povpulse { 50% { opacity: .45; } }
  @media (prefers-reduced-motion: reduce) { .pdot[data-tone="run"] { animation: none; } }
  .bx .povfold { gap: 2px; }
  /* 1. the strip: six cells of one height — three a row on a narrow page, two on a phone */
  .pov .povstrip { display: grid; grid-template-columns: repeat(6, minmax(0, 1fr)); gap: 1px; background: var(--bx-border); border: 1px solid var(--bx-border); border-radius: var(--bx-radius); }
  @container pov (max-width: 960px) { .pov .povstrip { grid-template-columns: repeat(3, minmax(0, 1fr)); } }
  @container pov (max-width: 560px) { .pov .povstrip { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
  .pov .povstrip.povwait { grid-template-columns: minmax(0, 1fr); }
  .pov .povc { background: var(--bx-panel); padding: 5px 8px 6px; height: 124px; box-sizing: border-box; display: flex; flex-direction: column; gap: 2px; min-width: 0; overflow: hidden; cursor: help; }
  .pov .povwait .povc { cursor: default; align-items: flex-start; justify-content: center; gap: 6px; }
  .pov .povc > * { flex-shrink: 0; }
  .pov .povc:focus-visible { outline: var(--bx-focus-outline); outline-offset: -2px; }
  .pov .povc h4 { margin: 0; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted);
    display: flex; align-items: center; gap: 6px; min-height: 20px; }
  .pov .povc h4 .sp { flex: 1; }
  .pov .povc h4 .nm { text-transform: none; letter-spacing: 0; font-weight: 400; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
  .pov .ihelp { --bx-icon-size: 13px; color: var(--bx-muted); }
  .pov .povc .big { font: var(--bx-font-title); display: flex; align-items: center; gap: 5px; white-space: nowrap; overflow: hidden; }
  .pov .povc .big small { font: var(--bx-font-meta); color: var(--bx-muted); }
  .pov .povc .ln { font: var(--bx-font-meta); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-height: 16px; }
  .pov .povc .kv { display: flex; gap: 4px; font: var(--bx-font-meta); white-space: nowrap; overflow: hidden; align-items: center; min-height: 18px; }
  .pov .povc .kv > * { flex: none; }
  .pov .povc .kv b { font-weight: 600; color: var(--bx-text); }
  .pov .povc .kv.chips { margin-top: auto; }
  .pov .chip { display: inline-flex; align-items: center; gap: 3px; border: 1px solid var(--bx-border); padding: 0 5px; font: var(--bx-font-meta); border-radius: var(--bx-radius);
    white-space: nowrap; --bx-icon-size: 12px; background: none; }
  .pov .chip[data-tone="bad"] { color: var(--bx-danger); border-color: var(--bx-danger); }
  .pov .chip[data-tone="warn"] { color: var(--bx-warn); border-color: var(--bx-warn); }
  .pov .chip[data-tone="ok"] { color: var(--bx-ok); border-color: var(--bx-ok); }
  .pov .stack { display: flex; height: 6px; border-radius: var(--bx-radius); overflow: hidden; background: var(--bx-panel-2); margin: 2px 0; }
  .pov .stack span { display: block; height: 100%; }
  .pov [data-tone="run"] { --pov-c: var(--bx-accent); }
  .pov [data-tone="coord"] { --pov-c: var(--bx-term-cyan); }
  .pov [data-tone="sub"] { --pov-c: var(--bx-term-magenta); }
  .pov [data-tone="warn"] { --pov-c: var(--bx-warn); }
  .pov [data-tone="idle"] { --pov-c: var(--bx-muted); }
  .pov [data-tone="none"] { --pov-c: var(--bx-border); }
  .pov .stack span, .pov .legend i { background: var(--pov-c); }
  .pov .legend { display: flex; flex-wrap: wrap; gap: 0 8px; font: var(--bx-font-meta); color: var(--bx-muted); }
  .pov .legend i { display: inline-block; width: 8px; height: 8px; margin-right: 3px; vertical-align: -1px; }
  .pov .povch.spark { width: 100%; height: 30px; display: block; margin-top: auto; flex: 1 1 0; min-height: 16px; }
  .pov .povch path { fill: var(--pov-c); stroke: none; }
  .pov .povch path:not(.area):not(.line) { opacity: .85; }
  .pov .povch path.area { opacity: .18; }
  .pov .povch path.line { fill: none; stroke: var(--pov-c); stroke-width: 1.4; vector-effect: non-scaling-stroke; }
  .pov .spark2 { display: grid; grid-template-columns: 1fr 1fr; gap: 6px; margin-top: auto; }
  .pov .spark2 .povch.spark { height: 28px; flex: none; }
  .pov .spark2 .lbl { font: var(--bx-font-meta); color: var(--bx-muted); display: flex; justify-content: space-between; }
  .pov .spark2 .lbl b { color: var(--bx-text); }
  .pov .seg { display: inline-flex; border: 1px solid var(--bx-border); border-radius: var(--bx-radius); overflow: hidden; text-transform: none; letter-spacing: 0; }
  .bx .pov .seg button { background: none; border: 0; border-radius: 0; min-height: 0; padding: 0 6px; cursor: pointer; color: var(--bx-muted); font: var(--bx-font-meta); }
  .bx .pov .seg button[aria-pressed="true"] { background: var(--bx-panel-2); color: var(--bx-text); box-shadow: inset 0 -2px 0 var(--bx-accent); }
  .pov .repo { display: flex; gap: 6px; align-items: center; font: var(--bx-font-meta); min-width: 0; white-space: nowrap; }
  .pov .repo a { overflow: hidden; text-overflow: ellipsis; min-width: 0; color: var(--bx-link); text-decoration: none; }
  .pov .repo a:hover { text-decoration: underline; }
  .pov .repo .mono { display: inline-flex; align-items: center; gap: 2px; font-family: var(--bx-mono); --bx-icon-size: 12px; }
  .pov .repo .rerr { color: var(--bx-warn); --bx-icon-size: 12px; }
  .pov .ellip { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
  /* folded: one line */
  .pov .povline { display: flex; flex-wrap: wrap; gap: 2px 14px; align-items: center; padding: 3px 8px; border: 1px solid var(--bx-border); background: var(--bx-panel);
    font: var(--bx-font-meta); color: var(--bx-muted); cursor: pointer; border-radius: var(--bx-radius); }
  .pov .povline > span { display: inline-flex; align-items: center; gap: 5px; }
  .pov .povline [data-tone="warn"] { color: var(--bx-warn); }
  .pov .povline [data-tone="bad"] { color: var(--bx-danger); }
  .pov .povline .sp { flex: 1; }
  /* a card's agent */
  .pov .ptagent { display: flex; gap: 6px; align-items: center; font: var(--bx-font-meta); color: var(--bx-muted); margin-top: 2px; min-width: 0; }
  .pov .ptagent span:last-child { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
`;
document.head.append(style);
