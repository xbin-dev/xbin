// native/harness-cards.js — a coding harness's transcript in the native
// view (D147 §3.4, §4.3.2–§4.3.5, §8 U3), the web's harness-cards.js
// with the chat family, hooked on the native seams (native/ext.js):
//
//   block    a `toolcard` per ACP call: its chips (steps, what it came to,
//            its status), the command and its output's tail (`code`), an
//            edit's files and patch (`diff`), the result — and a Claude Task's
//            steps in a nested `transcript`; ↗ opens the call in full
//   menu     Progress (3/7) → the Progress screen
//   screen   `hcall` (one call in full: all of its output, every patch) and
//            `hprogress` (the `plan`, what the conversation changed, usage)
//
// The plan is not drawn at the transcript's end: the end seam is a run's park
// (native/ext.js) — its progress and the context in use sit in the
// start of the conversation's subtitle (native/harness-start.js), all of it on
// its screen.
// Memoized per block and open state (rowTpl asks the seams every render).
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ctx, push, clip, fmtN, cardState, FAMILY_ICON } from './ui.js';
import { blockTpl } from './chat.js';
import { argsShown, parseArgs } from '../model/tool-heads.js';
import {
  isAcp, acpChip, acpKind, commandOf, outputTail, joinPatches, placesOf,
  isSubagentCall, stepsWords, taskOf, resultText,
} from '../model/harness-heads.js';
import { harnessOf, planOf, planEntries, usageBadge, countsWords, nameOf } from '../model/harness.js';

const CUT = 1200;        // a result's characters on the card (↗ shows all)
const OUT = 4000;        // an output's last characters on the card
const PATCH = 32 * 1024; // the patches on a card (whole files; ↗ shows all)

const TONE = { ok: 'ok', bad: 'danger', warn: 'warn', run: 'accent' };
const tone = (t) => (TONE[t] ? { tone: TONE[t] } : {});

ext.register({
  block: (b, depth) => (b.k === 'tool' && (b.acp || isAcp(b.name)) ? cardTpl(b, depth) : null),
  menu: (v) => (harnessOf(v.run) ? html`<button icon="list" @tap=${() => push({ kind: 'hprogress', run: v.run.id })}>${progressLabel(v.run)}</button>` : null),
  screen: (s) => (s.kind === 'hcall' ? callScreen(s) : s.kind === 'hprogress' ? progressScreen(s) : null),
});

const isOpen = (id, dflt) => ctx.app.session.ui.isOpen(id, dflt);
const setOpen = (id) => (e) => { ctx.app.session.open.set(id, !!e.open); ctx.paint(); };

// --- a call's card ------------------------------------------------------------------

const memo = new WeakMap(); // block → {open, tpl}
function cardTpl(b, depth) {
  const failed = b.state === 'error';
  const open = isOpen(b.id, failed); // a failed call opens by itself
  if (b.kids) return buildCard(b, depth, open); // its steps open and close inside it
  const m = memo.get(b);
  if (m && m.open === open && m.depth === depth) return m.tpl;
  const tpl = buildCard(b, depth, open);
  memo.set(b, { open, depth, tpl });
  return tpl;
}

function buildCard(b, depth, open) {
  const acp = b.acp || {};
  const { state } = cardState(b.state);
  const chip = acpChip(acp, b.state);
  const steps = isSubagentCall(b) ? stepsWords(b.kids) : '';
  const chips = [
    ...(steps ? [{ text: steps }] : []),
    ...(b.outcome ? [{ text: b.outcome.text, ...tone(b.outcome.tone) }] : []),
    ...(chip ? [{ text: chip.text, ...tone(chip.tone) }] : []),
  ];
  const orphan = !depth && b.parent && !b.kids; // under a subagent call that isn't held here
  return html`<toolcard title=${(orphan ? '↳ ' : '') + b.headline} icon=${FAMILY_ICON[b.fam] || 'wrench'} family=${b.fam} state=${state}
      chips=${chips.length ? chips : nothing} open=${open} @toggle=${setOpen(b.id)}
      @open=${() => push({ kind: 'hcall', run: ctx.app.sel, id: b.id })}>
    ${bodyTpl(b, acp, depth)}
  </toolcard>`;
}

function bodyTpl(b, acp, depth) {
  const a = parseArgs(b.args);
  const meta = html`<text style="caption" tone="muted" mono>${b.name + (acp.tool ? ' · ' + acp.tool : '') + (acp.title && ![b.headline, acp.tool, commandOf(a)].includes(acp.title) ? ' · ' + acp.title : '')}</text>`;
  if (isSubagentCall(b)) {
    const task = taskOf(a), answer = resultText(b);
    return html`${meta}${task ? html`<text style="footnote" tone="muted" lines=${3}>${'task: ' + task}</text>` : nothing}
      <transcript>${b.kids && b.kids.length ? repeat(b.kids, (x) => x.id, (x) => blockTpl(x, depth + 1))
        : html`<step glyph="•" tone="muted" text=${b.state === 'running' ? 'no steps yet' : 'its steps aren\'t held here'}/>`}</transcript>
      ${answer ? html`<text style="caption" tone="muted">answer</text>${resultTpl(answer)}` : nothing}`;
  }
  switch (acpKind(b.name)) {
    case 'execute': return html`${meta}${execTpl(b, acp, a)}`;
    case 'edit': case 'delete': {
      const d = diffTpl(acp, PATCH);
      if (d) return html`${meta}${d}`;
      break;
    }
    case 'fetch': return html`${meta}${a.url ? html`<text mono selectable>${String(a.url)}</text>` : nothing}${resultTpl(resultText(b))}`;
    case 'think': return html`${meta}${taskOf(a) ? html`<text selectable>${taskOf(a)}</text>` : nothing}${resultTpl(resultText(b))}`;
    case 'switch_mode': return html`${meta}${typeof a.plan === 'string' && a.plan ? html`<markdown source=${a.plan}/>` : nothing}${resultTpl(resultText(b))}`;
    case 'other': {
      const raw = argsShown(b.args);
      return html`${meta}${Object.keys(raw).length ? html`<code text=${clip(JSON.stringify(raw, null, 2), CUT * 4)}/>` : nothing}${resultTpl(resultText(b))}`;
    }
  }
  const places = placesOf(acp);
  return html`${meta}${places.length ? html`<text mono selectable>${places.join('\n')}</text>` : nothing}${resultTpl(resultText(b))}`;
}

// resultTpl: the result's first CUT characters (↗ shows all).
function resultTpl(text) {
  if (!text) return nothing;
  const long = text.length > CUT;
  return html`<code text=${long ? text.slice(0, CUT) + '…' : text}/>
    ${long ? html`<text style="footnote" tone="muted">${`cut at ${CUT} of ${fmtN(text.length)} characters — ↗ shows all`}</text>` : nothing}`;
}

// execTpl: the command, its output's tail (ANSI stripped), the exit code.
function execTpl(b, acp, a, n = OUT) {
  const cmd = commandOf(a) || acp.title || '';
  const has = typeof acp.output === 'string';
  const out = has ? outputTail(acp, n) : null;
  const code = acp.exitCode;
  const cut = out && (out.cut || out.dropped) ? (n === Infinity ? `the first ${fmtN(out.dropped)} bytes were not kept (the last 64 KiB are)` : 'the end of its output — ↗ shows all') : '';
  return html`${cmd ? html`<code text=${'$ ' + cmd} copy wrap/>` : nothing}
    ${has ? (out.text ? html`<code text=${(out.cut ? '…' : '') + out.text} wrap/>` : html`<text style="footnote" tone="muted">${b.state === 'running' ? 'no output yet' : 'no output'}</text>`) : resultTpl(resultText(b))}
    ${cut ? html`<text style="footnote" tone="muted">${cut}</text>` : nothing}
    ${code != null ? html`<text mono tone=${Number(code) === 0 ? 'ok' : 'danger'}>${'exit ' + code}</text>` : nothing}`;
}

// diffTpl: an edit's files and patches as one `diff` (whole files up to max
// characters); null when it carries no diff.
function diffTpl(acp, max) {
  const { patch, files, left } = joinPatches(acp, max);
  if (!files.length) return null;
  const cut = files.filter((d) => d.truncated).length;
  const note = [left ? `${left} patch${left === 1 ? '' : 'es'} left out here — ↗ shows all` : '', cut ? 'a patch past 64 KiB stops at a hunk' : ''].filter(Boolean).join(' · ');
  return html`<diff files=${files.map(({ path, status, add, del }) => ({ path, status, add, del }))} patch=${patch}/>
    ${note ? html`<text style="footnote" tone="muted">${note}</text>` : nothing}`;
}

// --- one call in full (↗) ---------------------------------------------------------------

function findBlock(blocks, id) {
  for (const b of blocks || []) {
    if (b.id === id) return b;
    const inner = findBlock(b.kids, id) || findBlock(b.blocks, id);
    if (inner) return inner;
  }
  return null;
}

function callScreen(s) {
  const b = (ctx.app.sel === s.run && findBlock(ctx.app.session.shown().blocks, s.id)) || s.last;
  if (b) s.last = b;
  if (!b) return html`<screen title="Tool call" style="scroll"><empty title="gone"/></screen>`;
  const acp = b.acp || {};
  const a = parseArgs(b.args);
  const kind = acpKind(b.name);
  const result = resultText(b);
  return html`<screen title=${b.headline} subtitle=${b.name + (acp.tool ? ' · ' + acp.tool : '')} style="scroll">
    ${kind === 'execute' ? execTpl(b, acp, a, Infinity) : nothing}
    ${kind === 'edit' || kind === 'delete' ? diffTpl(acp, Infinity) || nothing : nothing}
    ${placesOf(acp).length ? html`<text mono selectable>${placesOf(acp).join('\n')}</text>` : nothing}
    <text style="caption" tone="muted">arguments</text>
    <code text=${b.args || '{}'} copy wrap/>
    ${result ? html`<text style="caption" tone="muted">${`result · ${fmtN(result.length)} characters`}</text><code text=${result} copy wrap/>` : nothing}
  </screen>`;
}

// --- the plan, the changes, the usage ------------------------------------------------------

function progressLabel(r) {
  const p = planOf(harnessOf(r));
  return p ? `Progress (${p.done}/${p.total})` : 'Progress';
}

// progressScreen: the harness's plan as it keeps it, what the conversation
// changed (across restarts), the context in use and the cost.
function progressScreen(s) {
  const ses = ctx.app.session;
  const r = (ses.current()?.run?.id === s.run ? ses.current().run : ses.views.get(s.run)?.run) || ses.runs.get(s.run);
  const h = harnessOf(r);
  if (!h) return html`<screen title="Progress" style="scroll"><empty title="gone"/></screen>`;
  const p = planOf(h), u = usageBadge(h.usage), c = countsWords(h.counts);
  return html`<screen title="Progress" subtitle=${nameOf(h) + (p ? ` · ${p.text}` : '')} style="scroll">
    <transcript>
      ${p ? html`<plan entries=${planEntries(p)}/>` : html`<step glyph="–" tone="muted" text="no plan this session"/>`}
      <step glyph="✎" tone="muted" text=${c || 'no tool calls yet'}/>
      ${u ? html`<step glyph="◔" tone=${u.tone === 'bad' ? 'danger' : u.tone === 'warn' ? 'warn' : 'muted'} text=${u.title || u.text}/>` : nothing}
    </transcript>
  </screen>`;
}
