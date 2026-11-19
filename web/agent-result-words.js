/**
 * agent-result-words.js — a change-set line's outcome in words
 * (/vendor/agent-result-words.js): agent-chrome-words.js's resultOf, split
 * from it for the size budget (D219) and re-exported there, so the
 * elements, the vectors (native/tools/delegation-words.mjs) and the app's
 * port (DelegationWords.swift) read it as part of the chrome's words. Pure:
 * no DOM, no lit, no imports. Said from xbind's own fields only.
 */

const RESULT = {
  applied: { word: 'applied', icon: 'ok', tone: 'ok' }, failed: { word: 'failed', icon: 'error', tone: 'bad' }, skipped: { word: 'skipped', icon: 'minus', tone: '' },
  stale: { word: 'out of date', icon: 'refresh', tone: 'warn', text: 'Someone changed this after the agent proposed it. What you ticked no longer matches, so xbin left it alone.' },
  rejected: { word: 'left out by you', icon: 'minus', tone: '' }, blocked: { word: "couldn't be applied", icon: 'lock', tone: 'warn' },
  asked: { word: 'sent to an admin', icon: 'clock', tone: '' },
  // a plan's line that waits (item.wait): approved, applied by xbin once its tile provides what was approved — or never
  waiting: { word: 'waiting', icon: 'clock', tone: 'warn' }, lapsed: { word: 'lapsed — never applied', icon: 'clock', tone: '' }, cancelled: { word: 'cancelled — never applied', icon: 'minus', tone: '' },
};
/** waitText(item): where a waiting line stands, from xbind's fields — what it waits for, or why it is held ('' for a line that doesn't wait). */
export function waitText(item) {
  const w = item && item.wait;
  if (!w || item.result !== 'waiting') return '';
  const what = `${w.tile} provides ${w.service}`;
  return w.held ? `Not bound — ${w.held}. xbin applies it once ${what} as you approved it.` : `xbin applies it by itself once ${what}; nothing is bound until then.`;
}
/**
 * builtinText(item): what an applied builtin.update line made (D248), from
 * xbind's out — updated, installed, or filed as a proposal on its tile
 * because it conflicts with the tile's own changes ('' for any other line).
 */
export function builtinText(item) {
  const o = (item && item.kind === 'builtin.update' && item.result === 'applied' && item.out) || null;
  if (!o || typeof o !== 'object') return '';
  const tile = String(o.tile || 'its tile'), files = Number(o.files) || 0, f = `${files} file${files === 1 ? '' : 's'}`;
  switch (o.update) {
    case 'proposed': return `It conflicts with changes made in ${tile}, so xbin filed it there as change proposal #${Number(o.proposal) || 0} instead: nothing in the tile changed. Its tile agent (or a terminal there) merges it — your agent can start one with xbin's brief.`;
    case 'installed': return `Installed in ${tile}: ${f}.`;
    case 'updated': return `Updated ${tile}: ${f}, keeping its own changes.`;
  }
  return '';
}
/** resultOf(item): an item's result as {word, icon, tone, text} ("xbin said: …" for a failure). */
export function resultOf(item) {
  const r = RESULT[item.result] || (item.needsAdmin && !item.result ? { word: 'needs an admin', icon: 'lock', tone: '' } : { word: item.result || '', icon: '', tone: '' });
  const text = item.result === 'failed' ? `xbin said: “${item.error || 'it failed'}”` : item.result === 'blocked' ? String(item.blocked || item.error || '')
    : item.result === 'waiting' ? waitText(item) : ['lapsed', 'cancelled'].includes(item.result) ? String(item.error || '') : builtinText(item) || r.text || '';
  return { ...r, text };
}
