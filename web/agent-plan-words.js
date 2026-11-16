/**
 * agent-plan-words.js — the words and rules of a plan's review
 * (/vendor/agent-plan-words.js; docs/protocol/delegation.md §Plans): a
 * change set whose lines may create tiles and start tile agents, are the
 * person's own to apply or an admin's, and may need earlier lines. Pure
 * functions over xbind's answer, like agent-chrome-words.js beside it —
 * the chrome's dialog (bx-agent-changes.js) draws from these, and the
 * app's port (DelegationPlanWords.swift) is held to them by the vectors
 * native/tools/delegation-words.mjs generates.
 *
 * Nothing here trusts the agent: every sentence is built from xbind's
 * fields, and a kind this chrome doesn't know is never one that can be
 * ticked.
 */
import { CHOICES, sessionRows } from './agent-chrome-words.js';

const plural = (n, one, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
const items = (cs) => (cs && cs.items) || [];

/** PLAN_KINDS: the kinds this chrome knows how to show. A line of any other kind is shown, and can't be ticked. */
export const PLAN_KINDS = Object.freeze(['grant.add', 'grant.revoke', 'binding.set', 'binding.remove', 'org.share', 'limits.set', 'tile.create', 'agent.start']);
export const knownKind = (kind) => PLAN_KINDS.includes(kind);

/**
 * lineState(item, state): what stands between a line and its tick —
 * 'ok' nothing; 'blocked' xbind can't apply it; 'needs-admin' it is an
 * admin's and the person is none; 'opt-in' it is an admin's and the person
 * has not let their agent propose admin changes; 'decided' it has a result
 * already (applied before, or sent to an admin); 'unknown' a kind this
 * chrome doesn't know. state: GET /delegate/state. asked: the set is a
 * request from another person (no opt-in of one's own applies to it).
 */
export function lineState(item, state, asked = false) {
  if (!knownKind(item.kind)) return 'unknown';
  if (item.result) return 'decided';
  if (item.blocked) return 'blocked';
  if (item.needsAdmin) return 'needs-admin';
  if (item.admin && !asked && !(state && state.adminChanges)) return 'opt-in';
  return 'ok';
}

/** neededBy(cs, n): the lines that need line n, directly or through another. */
export function neededBy(cs, n) {
  const out = new Set();
  let grew = true;
  while (grew) {
    grew = false;
    for (const it of items(cs)) {
      if (out.has(it.n) || !(it.needs || []).some((d) => d === n || out.has(d))) continue;
      out.add(it.n); grew = true;
    }
  }
  return [...out].sort((a, b) => a - b);
}

/** canTick(cs, ticks, item, state, asked): the person may tick it now — nothing stands in its way, and every line it needs is ticked or applied. */
export function canTick(cs, ticks, item, state, asked = false) {
  if (lineState(item, state, asked) !== 'ok') return false;
  return (item.needs || []).every((d) => ticks.includes(d) || (items(cs)[d - 1] || {}).result === 'applied');
}

/** planTicks(cs, state, asked): the lines ticked when the review opens — never one with a warning, one that can't be ticked, or one whose needed line isn't ticked. */
export function planTicks(cs, state, asked = false) {
  const ticks = [];
  for (const it of items(cs)) {
    if (CHOICES.warnedStartUnticked && it.warnings && it.warnings.length) continue;
    if (canTick(cs, ticks, it, state, asked)) ticks.push(it.n);
  }
  return ticks;
}

/** setTick(cs, ticks, n, on): the ticks after line n is ticked or unticked — unticking a line unticks the lines that need it. */
export function setTick(cs, ticks, n, on) {
  if (on) return [...new Set([...ticks, n])].sort((a, b) => a - b);
  const gone = new Set([n, ...neededBy(cs, n)]);
  return ticks.filter((x) => !gone.has(x));
}

/** keepTicks(cs, ticks, state, asked): ticks that still stand (kept across a sign-in, or after the opt-in changed), in order. */
export function keepTicks(cs, ticks, state, asked = false) {
  const out = [];
  for (const it of items(cs)) if (ticks.includes(it.n) && canTick(cs, out, it, state, asked)) out.push(it.n);
  return out;
}

const list = (ns) => (ns.length < 2 ? ns.join('') : `${ns.slice(0, -1).join(', ')} and ${ns[ns.length - 1]}`);

/**
 * lineNotes(cs, item, state, asked): what the review says under a line,
 * beside xbind's own words — {tag, needs, neededBy, why}: tag 'admin' |
 * 'needs an admin' | ''; needs "Needs line 1 and 2"; neededBy "Needed by
 * line 5 — unticking this unticks it"; why: why it can't be ticked now.
 */
export function lineNotes(cs, item, state, asked = false) {
  const st = lineState(item, state, asked);
  const by = neededBy(cs, item.n);
  const needs = item.needs || [];
  return {
    tag: item.needsAdmin ? 'needs an admin' : item.admin ? 'admin' : '',
    needs: needs.length ? `Needs line ${list(needs)}${item.deferred ? ' — checked again once that exists' : ''}` : '',
    neededBy: by.length ? `Needed by line ${list(by)} — unticking this unticks ${by.length === 1 ? 'it' : 'them'}` : '',
    why: st === 'unknown' ? 'This page is older than this xbin and does not know this kind of change. Reload the page to review it.'
      : st === 'needs-admin' ? 'Not yours to apply: it takes an admin’s rights. xbin shows what it would change to an admin.'
        : st === 'opt-in' ? 'An admin change: tick it once you let your agent propose those (above).' : '',
  };
}

/**
 * planWords(cs, ticks, state): the review's bar, heading, intro, footer,
 * Apply button, and what it says about admin lines — optIn (the switch to
 * offer, '' none), ask (the Ask an admin row: {text, button, can, why}).
 */
export function planWords(cs, ticks, state) {
  const all = items(cs), total = all.length, n = ticks.length;
  const asked = !!(cs && cs.from);
  const admin = all.filter((it) => it.admin && !it.needsAdmin && !it.blocked && !it.result).length;
  const waits = all.filter((it) => it.needsAdmin && !it.result);
  const blockedBy = waits.flatMap((it) => (it.needs || []).filter((d) => (all[d - 1] || {}).result !== 'applied'));
  const undo = !!(cs && cs.undoOf);
  return {
    bar: asked ? `xbin · request from ${cs.from}` : 'xbin · plan',
    heading: asked ? `${cs.from} asks for ${plural(total, 'admin change')}` : undo ? `Undo: ${plural(total, 'change')}` : `Your agent proposes ${plural(total, 'change')}`,
    intro: asked
      ? `${cs.from}'s workspace agent proposed ${total === 1 ? 'this' : 'these'}, and ${cs.from} asked the workspace's admins to apply ${total === 1 ? 'it' : 'them'}. Each line, its before and after and its warnings are written by xbin from the change itself, as you would apply it. Nothing has been applied.`
      : 'Each line, its before and after and its warnings are written by xbin from the change itself. Nothing has been applied.',
    footer: `${n} of ${total} ticked · applied in order as you. If one fails, later ones are skipped and earlier ones stay.`,
    apply: n ? `Apply ${plural(n, 'change')}…` : 'Apply none',
    reject: asked ? 'Decline' : 'Reject all',
    optIn: !asked && admin && state && state.canAdminChanges && !state.adminChanges
      ? { text: `This plan has ${plural(admin, 'admin change')}.`, label: 'Let my agent propose admin changes',
        note: 'It can then read grants, bindings, org shares and limits, and propose changes to them. Nothing is applied until you tick it and press Apply; you can turn this off in Preferences.' } : null,
    ask: waits.length ? {
      text: `${plural(waits.length, 'line')} ${waits.length === 1 ? 'needs' : 'need'} an admin.`,
      button: 'Ask an admin',
      can: !blockedBy.length,
      why: blockedBy.length ? `Apply line ${list([...new Set(blockedBy)].sort((a, b) => a - b))} first: what waits for an admin needs it.`
        : 'The workspace’s admins get it as a request from you, in xbin’s words. They apply it with their own rights; you can apply the rest now.',
    } : null,
    byHand: (cs && cs.byHand) || [],
  };
}

/** startRows(item): an agent.start line as a single start's sheet draws it (sessionRows) — from xbind's `start`. */
export function startRows(item) {
  const s = (item && item.start) || {};
  return sessionRows({ kind: 'session', tile: s.tile, preview: { tile: s.tile, session: s }, body: JSON.stringify({ prompt: typeof s.prompt === 'string' ? s.prompt : '' }) });
}

/** planNeed(cs): a plan (or a request from another person) as a row of Needs you. */
export function planNeed(cs) {
  const all = items(cs);
  const w = all.filter((it) => it.warnings && it.warnings.length).length, a = all.filter((it) => it.admin && !it.needsAdmin).length, na = all.filter((it) => it.needsAdmin && !it.result).length;
  return {
    title: cs.from ? `${cs.from} asks for ${plural(all.length, 'admin change')}` : `A plan of ${plural(all.length, 'change')}`,
    note: [w ? `${w} ${w === 1 ? 'has' : 'have'} a warning` : '', !cs.from && a ? `${a} ${a === 1 ? 'is' : 'are'} an admin's` : '', na ? `${na} ${na === 1 ? 'needs' : 'need'} an admin` : ''].filter(Boolean).join(' · '),
  };
}

/** askedWords(cs): the results' line about what was sent to, or waits for, an admin ('' nothing). */
export function askedWords(cs) {
  const all = items(cs);
  const asked = all.filter((it) => it.result === 'asked').length, waits = all.filter((it) => it.needsAdmin && !it.result).length;
  return [asked ? `${plural(asked, 'line')} ${asked === 1 ? 'was' : 'were'} sent to the workspace's admins. You are told here when one of them decides.` : '',
    waits ? `${plural(waits, 'line')} ${waits === 1 ? 'needs' : 'need'} an admin and ${waits === 1 ? 'was' : 'were'} not applied.` : ''].filter(Boolean).join(' ');
}
