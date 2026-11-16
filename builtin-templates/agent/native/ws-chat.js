// native/ws-chat.js — a workspace agent's conversation in the app (D233): the
// transcript — the run's rows (native/blocks.js) with the agent's own cards
// set where the conversation made them (ws/place.js compose, the same
// placing the web page draws) — and the composer with its context chip,
// quick replies and hint. The words are ws/words.js; what happens on a tap
// is the store's (ws/store.js), shared with the web page.
//
// Nothing here grants. A request and an admin change set are a `request`
// node: the tile names xbind's id, THE APP draws the card and its Review…
// from xbind's own reading and presents its own sheet. A tile agent is a
// `session` node the app draws from xbind's session directory. A brief's
// "Review and start…" files the request and asks the app's chrome to open
// it (xbin.native.agent); there is no Allow, Deny or Resume in this file.
//
// Older apps: every node of vocabulary D233 is asked for first
// (xbin.native.supports) and has a fallback the app can draw — a notice
// for a request or a tile agent, markdown and composer chips for a card,
// the rev-1 question and plan, a plain composer.
import { html, repeat, nothing, native } from '/vendor/xb-native.js';
import { ctx } from './ui.js';
import { isOpen, setOpen, thinkTpl, assistantTpl, draftTpl, noticeTpl, toolTpl, agentTpl } from './blocks.js';
import { winOf, more, anchors, scrolled, edge, pillOf, jump } from './win.js';
import { W, aboutOf, aboutLabel, agentCard, requestCard, briefCard, refusalCard, possessive } from '../ws/words.js';
import { UPDATES } from '../ws/place.js';
import * as dock from '../ws/dock.js';
import { reviewable } from '../ws/store.js';

/** has(name, rev): the app draws this primitive at this revision (or lists this feature). */
export const has = (name, rev = 1) => { try { return !!native.supports?.(name, rev); } catch { return false; } };
/** compact(): a phone's width (an app that doesn't say is drawn as one). */
export const compact = () => { try { return native.width !== 'regular'; } catch { return true; } };

// What the app has of vocabulary D233. The three card nodes are a
// transcript's children only from its rev 3.
const T3 = () => has('transcript', 3);
const CARD = () => has('card') && T3();
const REQUEST = () => has('request') && T3();
const SESSION = () => has('session') && T3();
const D233 = () => has('card') || has('connection');

// the web's glyph names → the vocabulary's icons (an older app has no plug or tick boxes)
const ICON = { signal: 'network', grid: 'box', compact: 'archive', stop: 'stop', 'chevron-down': 'expand' };
export const ico = (name) => {
  if (!name) return nothing;
  if (name === 'plug') return D233() ? 'plug' : 'link';
  return ICON[name] || name;
};
const TONE = { bad: 'danger', warn: 'warn', ok: 'ok' };
export const tone = (t) => TONE[t] || nothing;

// The store this view draws (native/ws.js binds it), and the cards' buttons
// an app without `card` shows as composer chips (collected per draw).
let st = null;
let late = [];
export function bind(store) { st = store; }

// ---- cards -----------------------------------------------------------------------------------

// cardTpl(c): a card of the tile's own — c: {key, title, subtitle, icon,
// tone, badge, note, parts, buttons}. parts: {text, style?, tone?, mono?} |
// {caption} | {field: {label, value, input}} | {picker: {label, value,
// options, change}} | {notice: {tone, text}}; buttons: {label, role?, icon?,
// tap, disabled?}. Without `card`: its words as markdown, its buttons among
// the composer's chips.
function cardTpl(c) {
  const parts = (c.parts || []).filter(Boolean);
  const buttons = (c.buttons || []).filter(Boolean);
  if (!CARD()) {
    const md = [c.title ? `**${c.title}**${c.badge ? ` — ${c.badge}` : ''}` : '', c.subtitle || '',
      ...parts.map((p) => (p.caption ? `_${p.caption}_` : p.field ? `_${p.field.label}_: ${p.field.value || '—'}` : p.notice ? p.notice.text : p.picker ? '' : p.text || '')),
      c.note || ''].filter(Boolean).join('\n\n');
    for (const b of buttons) if (!b.disabled) late.push(b);
    return html`<markdown source=${md}/>`;
  }
  return html`<card title=${c.title || nothing} subtitle=${c.subtitle || nothing} icon=${c.icon ? ico(c.icon) : nothing} tone=${c.tone || nothing}
      badge=${c.badge || nothing} note=${c.note || nothing}>
    ${parts.map(partTpl)}
    ${buttons.map((b) => html`<button role=${b.role || nothing} icon=${b.icon ? ico(b.icon) : nothing} ?disabled=${!!b.disabled} @tap=${b.tap}>${b.label}</button>`)}
  </card>`;
}
function partTpl(p) {
  if (p.caption) return html`<text style="caption" tone="muted">${p.caption}</text>`;
  if (p.field) return html`<field kind="multiline" label=${p.field.label} value=${p.field.value} @input=${p.field.input}/>`;
  if (p.picker) return html`<picker label=${p.picker.label} style="menu" value=${p.picker.value} options=${p.picker.options} @change=${p.picker.change}/>`;
  if (p.notice) return html`<notice tone=${p.notice.tone || 'info'} text=${p.notice.text}/>`;
  return html`<text style=${p.style || nothing} tone=${p.tone || nothing} ?mono=${!!p.mono} selectable>${p.text}</text>`;
}

// ---- a request for the person's approval, a tile agent: the app's own nodes --------------------------

const KINDS = ['call', 'session', 'template', 'changeset'];

// requestTpl: a request xbind holds is the app's to draw, by id. One the
// brief's own button confirms (no id of xbind's) is a line of this view's.
function requestTpl(q) {
  const w = requestCard(q, Date.now(), st.tileName);
  if (!reviewable(q)) {
    const local = w.local && w.waits;
    return cardTpl({ icon: local ? 'shield' : w.glyph, tone: local ? nothing : tone(w.tone), title: local ? W.readyToStart : w.head, subtitle: w.what,
      parts: [w.result ? { text: w.result } : null, w.line && !local ? { text: w.line, style: 'footnote', tone: 'muted' } : null], note: local ? W.startFromBrief : '' });
  }
  const again = w.button === W.askAgain ? cardTpl({ buttons: [{ label: W.askAgain, tap: () => st.send(`Please ask again: ${q.title || w.what}`) }] }) : nothing;
  if (REQUEST()) return html`<request id=${q.ref} kind=${KINDS.includes(q.kind) ? q.kind : 'call'}/>${again}`;
  // an app from before the node can't review it: say so; what came of a decided one is words
  return w.waits ? html`<notice tone="warn" title=${W.updateToReview} text=${w.what}/>`
    : html`<notice tone=${w.tone === 'bad' ? 'danger' : w.tone === 'ok' ? 'ok' : 'muted'} title=${w.head} text=${[w.what, w.line, w.result].filter(Boolean).join(' · ')}/>`;
}

// sessionTpl: a tile agent the agent started. Its session is the app's to
// draw (xbind's directory; Open goes to the app's agent screen); before it
// has one, and in an app from before the node, its words.
function sessionTpl(a) {
  if (a.session && SESSION()) return html`<session id=${a.session} tile=${a.tile || nothing}/>`;
  const w = agentCard(a, Date.now(), st.tileName);
  if (!a.session) return cardTpl({ icon: w.glyph, tone: tone(w.tone), title: `${w.tile} · ${w.harness}`, badge: w.word, parts: [w.line ? { text: w.line } : null], note: w.note });
  return html`<notice tone=${w.waits ? 'warn' : 'muted'} title=${`${w.tile} · ${w.harness} — ${w.word}`} text=${[w.line, w.note, 'Open it from the tile.'].filter(Boolean).join(' ')}/>`;
}

// ---- the brief ("scope, then hand off") ----------------------------------------------------------

function briefTpl(blk) {
  const b = blk.data;
  const w = briefCard(b, st.tileName, st.startDefaults(b));
  const bag = st.bag(blk.k);
  const editing = !!bag.editing && w.editable && CARD();
  const save = async () => {
    bag.busy = true; bag.err = ''; st.S.rev++; ctx.paint();
    const err = await st.briefSave(b, bag.vals || {});
    bag.busy = false; bag.err = err || '';
    if (!err) { bag.editing = false; bag.vals = null; }
    ctx.paint();
  };
  const start = async () => {
    bag.busy = true; bag.err = ''; ctx.paint();
    bag.err = (await st.briefStart(b)) || '';
    bag.busy = false; ctx.paint();
  };
  const pick = async (e) => { bag.busy = true; ctx.paint(); bag.err = (await st.briefSave(b, { harness: String(e.value ?? '') })) || ''; bag.busy = false; ctx.paint(); };
  const fields = w.fields.flatMap((f) => (editing
    ? [{ field: { label: f.label, value: (bag.vals || {})[f.key] || '', input: (e) => { bag.vals[f.key] = e.value; } } }]
    : [{ caption: f.label }, { text: f.value || '—' }]));
  const buttons = editing
    ? [{ label: 'Save', role: 'primary', disabled: bag.busy, tap: save }, { label: 'Cancel', disabled: bag.busy, tap: () => { bag.editing = false; bag.vals = null; ctx.paint(); } }]
    : [w.button ? { label: w.button, role: 'primary', icon: w.button === W.start ? 'play' : 'shield', disabled: bag.busy || w.none || (st.offline() && w.button !== W.start), tap: start } : null,
      w.editable && CARD() ? { label: 'Edit', icon: 'pencil', role: 'plain', tap: () => { bag.vals = Object.fromEntries(w.fields.map((f) => [f.key, f.value])); bag.editing = true; ctx.paint(); } } : null];
  return cardTpl({ icon: 'doc', title: w.title, badge: w.status,
    parts: [...fields, bag.err ? { notice: { tone: 'danger', text: bag.err } } : null,
      w.harnesses.length > 1 && w.editable && !editing ? { picker: { label: 'Coding agent', value: w.picked, options: w.harnesses.map((h) => ({ value: h.id, label: h.name })), change: pick } } : null,
      w.runsOn ? { text: w.runsOn, style: 'footnote', tone: 'muted' } : null,
      w.signin ? { notice: { tone: 'warn', text: w.signin.elsewhere } } : null], // the app has no sign-in card: say so (D230)
    note: w.editable ? w.note : '', buttons });
}

// ---- xbin's refusals, a redirect, a "can't" ---------------------------------------------------------

const WAYS = {
  scope: () => ({ label: 'Open scope', tap: () => st.openScope({}) }),
  model: () => ({ label: 'Change the model…', tap: () => st.openScope({ models: true }) }),
  whatCan: () => ({ label: W.whatCanItDo, tap: () => st.showWhatCan() }),
};
function refusalTpl(f) {
  const r = refusalCard(f, st.tileName);
  if (r.quiet) return html`<step glyph="·" tone="muted" text=${r.text}/>`;
  return cardTpl({ icon: r.icon, tone: r.kind === 'policy' || r.kind === 'taint' ? 'warn' : nothing, title: r.title,
    parts: [r.text ? { text: r.text } : null], note: r.note || '',
    buttons: r.ways.map((x, i) => ({ ...WAYS[x](), ...(i === 0 ? { role: 'primary' } : {}) })) });
}

// a request about code: the tile's own agent takes it as written (the app opens a new session of it, the words
// as its draft — nothing is sent), or the person says the outcome
function redirectTpl(d) {
  const tile = d.tile ? st.tileName(d.tile) || d.tile : '';
  return cardTpl({ icon: 'code', title: 'The workspace agent doesn’t open or edit code. Two ways forward:',
    parts: [d.question ? { text: d.question } : null,
      d.waits && tile ? { text: `Hand it over: it opens in ${tile} with your message as written.`, style: 'footnote', tone: 'muted' } : null,
      d.waits ? { text: 'Or say what should be true when it’s done; the agent briefs the tile’s agent.', style: 'footnote', tone: 'muted' } : null],
    note: d.waits ? '' : d.answer != null ? 'You answered in the conversation.' : W.replaced,
    buttons: d.waits ? [tile ? { label: `Hand this to ${possessive(tile)} own agent`, icon: 'external', tap: () => st.chrome(dock.openSession('new', d.tile, d.draft)) } : null,
      { label: 'Tell me the outcome instead', icon: 'pencil', tap: () => st.fill('The outcome I want: ') }] : [] });
}

// what it can't do, and the nearest things it can (each a reply)
function cantTpl(d) {
  return cardTpl({ icon: 'info', title: 'Can’t do this', parts: [d.question ? { text: d.question } : null],
    note: d.answer != null ? `You answered: ${d.answer}` : '',
    buttons: [...(d.waits ? d.choices.map((c) => ({ label: c, tap: () => st.send(c) })) : []), WAYS.whatCan()] });
}

// ---- the plan, a question ------------------------------------------------------------------------

// planTpl: the session's plan — numbered steps, each tagged with what it does or where it stands (plan rev 2);
// an app of rev 1 gets the tag in the step's line and the note under the list.
function planTpl(p) {
  if (has('plan', 2)) {
    const tags = p.entries.map((e) => ({ text: (e.tag && e.tag.text) || '', ...(e.tag && e.tag.icon ? { icon: ico(e.tag.icon) } : {}), ...(e.tag && TONE[e.tag.tone] ? { tone: TONE[e.tag.tone] } : {}) }));
    return html`<plan title=${p.title || 'Plan'} ?numbered=${!!p.numbered} entries=${p.entries.map((e) => ({ text: e.content, status: e.status }))} tags=${tags}
      note=${p.note || nothing} meta=${p.meta || nothing}/>`;
  }
  return html`<plan entries=${p.entries.map((e, i) => ({ text: `${p.numbered ? `${i + 1}. ` : ''}${e.content}${e.tag && e.tag.text ? ` — ${e.tag.text}` : ''}`, status: e.status }))}/>
    ${p.note ? html`<text style="footnote" tone="muted">${p.note}</text>` : nothing}`;
}

// askTpl: the agent's question (ws/place.js askBlock). Its choices are rows with "Something else" riding on them
// (question rev 2; rev 1: a menu and a field); the answer is the person's next message, and Skip is never consent.
function askTpl(a) {
  if (a.action === 'cancel') return html`<step glyph="?" tone="muted" text=${`${a.message} — ${W.replaced}`}/>`;
  if (a.mode !== 'form') return a.action ? nothing : cardTpl({ icon: 'question', title: a.message, note: W.answerBelow });
  const q2 = has('question', 2);
  const choices = a.schema.properties.answer.enum || [];
  const schema = { type: 'object', properties: {
    answer: { type: 'string', title: q2 ? '' : 'Choose', enum: choices },
    other: { type: 'string', title: W.somethingElse, description: W.somethingElseHint },
  } };
  const answer = (e) => { const c = (e && e.content) || {}; const text = String(c.other || c.answer || '').trim(); if (text) st.send(text); };
  return html`<question title=${a.message} schema=${schema} settled=${a.action === 'accept' ? a.content || {} : nothing}
    style=${q2 ? 'rows' : nothing} other=${q2 ? { answer: 'other' } : nothing} submitLabel=${q2 ? W.answer : nothing} skipLabel=${q2 ? W.skip : nothing}
    @submit=${a.action ? nothing : answer} @skip=${a.action ? nothing : () => st.send(W.skipSent)}/>`;
}

// ---- a block the conversation placed: a card of the agent's, the plan, a question -----------------------

function placedTpl(b) {
  if (b.kind === 'plan') return planTpl(b);
  if (b.kind === 'ask') return askTpl(b);
  if (b.kind !== 'host') return nothing;
  switch (b.type) {
    case 'wsession': return sessionTpl(b.data);
    case 'request': return requestTpl(b.data);
    case 'brief': return briefTpl(b);
    case 'refusal': return refusalTpl(b.data);
    case 'redirect': return redirectTpl(b.data);
    case 'cant': return cantTpl(b.data);
    case 'replies': return nothing; // quick replies sit above the composer (composerTpl)
    case 'notice': return html`<notice tone="info" title=${b.data.bold || nothing} text=${b.data.text || ''}/>`;
  }
  return nothing;
}

// ---- the run's own rows ---------------------------------------------------------------------------

// the context the backend answers back on the person's message (API.md §Workspace mode, "In fields")
function contextOf(b) {
  const v = b.msgId != null ? st.session.views.get(st.S.sel) : null;
  const m = v && v.messages.find((x) => x.id === b.msgId);
  return (m && m.context) || null;
}

function plainTpl(b) {
  switch (b.k) {
    case 'user': {
      const text = String(b.text || '');
      if (text.startsWith(UPDATES)) {
        // what xbin and the tile agents reported: folded, never drawn as the person's words
        const nl = text.indexOf('\n');
        return html`<toolcard title=${'↵ ' + W.updates} icon="info" family="notice" open=${isOpen(b.id, false)} @toggle=${setOpen(b.id)}>
          <text selectable>${nl >= 0 ? text.slice(nl + 1) : ''}</text></toolcard>`;
      }
      // its "About …" chip, from what the backend resolved, rides above the words
      const about = aboutOf(contextOf(b));
      return html`<message role="user" text=${text} sender=${about ? aboutLabel(about) : nothing}>
        <actions>${text ? html`<button icon="copy" copy=${text}>Copy</button>` : nothing}</actions></message>`;
    }
    case 'think': return thinkTpl(b);
    case 'assistant': return assistantTpl(b);
    case 'draft': return draftTpl(b);
    case 'notice': return noticeTpl(b);
    case 'tool': return toolTpl(b, 0);
    case 'agent': return agentTpl(b, 0); // a research subagent: its steps inside its card
    case 'step': return stepTpl(b);
  }
  return nothing;
}

// a journal line, in this page's words (the web page's plainOther)
function stepTpl(b) {
  const d = b.detail || {};
  switch (b.kind) {
    case 'error': return html`<notice tone="danger" text=${d.error || d.text || 'It failed.'}/>`;
    case 'compaction': return html`<step glyph="≡" tone="muted" text="Earlier turns were summarised"/>`;
    case 'finish': return d.result ? html`<step glyph="✓" tone="ok" text="finished"/><markdown source=${d.result}/>` : html`<step glyph="✓" tone="ok" text="finished"/>`; // theme-ok: the step primitive's text glyph
    case 'cancel': return html`<step glyph="■" tone="warn" text=${`stopped${d.reason ? ': ' + d.reason : ''}`}/>`;
    case 'ask': return nothing;
    // the agent's switch met this message or turn: answered without the model — which switch, who changes it, where
    case 'off': return html`<step glyph="‖" tone="warn" text=${d.text || ''}/>`;
  }
  const text = d.text || d.summary || '';
  return text ? html`<step glyph="•" tone="muted" text=${text}/>` : nothing;
}

// rowTpl: a row of the fold, with what the conversation placed at it — in its place, or after it
function rowTpl(b, layout) {
  const e = layout.at.get(b.id);
  if (!e) return plainTpl(b);
  const own = e.hide ? nothing : e.replace ? e.replace.map(placedTpl) : plainTpl(b);
  return html`${own}${e.after.map(placedTpl)}`;
}

// ---- the transcript -----------------------------------------------------------------------------

// emptyOf: the empty state (GET /ws/empty) as the transcript's `empty` slot: what the agent can and can't do in
// this session's own numbers — three lines each on a phone — and starters from the person's own tiles (three on a
// phone, four beside it). With `off` (the agent's switch: xbind says it can't act) only its sentence — which switch,
// who changes it, where — as the web page's empty state says it (ws/panels.js emptyView).
function emptyOf(e) {
  const n = compact() ? 3 : 4;
  if (e.off) return { title: e.heading || W.title, text: e.line || W.offWhy[e.off] || '' };
  return {
    title: e.heading || 'Ask for an outcome', text: e.line || '',
    lists: [{ title: W.itCan, items: (e.can || []).slice(0, compact() ? 3 : 8) }, { title: W.itCant, items: (e.cant || []).slice(0, compact() ? 3 : 8) }].filter((l) => l.items.length),
    ...((e.starters || []).length ? { suggestionsTitle: e.startersTitle || 'Try one', suggestions: e.starters.slice(0, n).map((s, i) => ({ id: String(i), kind: s.kind || '', text: s.text || '' })) } : {}),
  };
}
/** starter(i): a starter fills the composer — it never sends (CHOICES.starterSends). */
export function starter(i) {
  const e = st.S.empty;
  const s = (e && !e.off && e.starters) ? e.starters[Number(i)] : null;
  if (s) st.fill(s.text);
}

/**
 * transcriptTpl(): the conversation — {tpl, pill}: the transcript, and the
 * "jump to latest" words while the reader is away from its end.
 */
export function transcriptTpl() {
  late = [];
  const S = st.S;
  if (!st.session.current()) return { tpl: html`<progress label="Loading…"/>`, pill: '' };
  const { shown: s, layout } = st.place();
  const w = winOf(s, S.sel);
  const rows = w.start || w.end < s.blocks.length ? s.blocks.slice(w.start, w.end) : s.blocks;
  const atEnd = w.end >= s.blocks.length && !s.hasNewer;
  const empty = !s.blocks.length && !s.hasOlder;
  const agents = st.working();
  const line = [s.activity, agents ? `${agents} tile agent${agents === 1 ? '' : 's'} working` : ''].filter(Boolean).join(' · ');
  const slot = empty && S.empty && T3();
  // drawn now, not when the tree is built: a card's buttons may be the composer's chips (cardTpl, `late`)
  const drawn = rows.map((b) => ({ id: b.id, tpl: rowTpl(b, layout) }));
  const tpl = html`<transcript follow ?older=${w.start > 0 || s.hasOlder} @more=${more}
      anchor=${anchors() && !w.atBottom && w.anchor != null ? String(w.anchor) : nothing}
      scrollTo=${anchors() ? `end#${w.jumps}` : nothing}
      @scrolled=${anchors() ? nothing : scrolled} @edge=${anchors() ? edge : nothing}
      empty=${slot ? emptyOf(S.empty) : nothing} @suggestion=${slot ? (e) => starter(e.id) : nothing}>
    ${empty && S.empty && !T3() ? emptyFallback(S.empty) : nothing}
    ${empty && !S.empty ? html`<progress label="Loading…"/>` : nothing}
    ${repeat(drawn, (x) => x.id, (x) => x.tpl)}
    ${atEnd ? layout.tail.map(placedTpl) : nothing}
    ${atEnd && line ? html`<activity ?live=${st.busy() || agents > 0} text=${line}/>` : nothing}
  </transcript>`;
  return { tpl, pill: pillOf(w, s) };
}

// an app whose transcript has no `empty` slot: the same words as text, the starters as the composer's chips
function emptyFallback(e) {
  if (e.off) return html`<text style="title3">${e.heading || W.title}</text><text>${e.line || W.offWhy[e.off] || ''}</text>`;
  const list = (title, items) => (items && items.length ? `**${title}**\n${items.slice(0, 3).map((x) => `- ${x}`).join('\n')}` : '');
  (e.starters || []).slice(0, 3).forEach((s, i) => late.push({ label: s.text, tap: () => starter(i) }));
  return html`<text style="title3">${e.heading || 'Ask for an outcome'}</text>
    ${e.line ? html`<text>${e.line}</text>` : nothing}
    <markdown source=${[list(W.itCan, e.can), list(W.itCant, e.cant)].filter(Boolean).join('\n\n')}/>`;
}

// ---- the composer ------------------------------------------------------------------------------

/**
 * composerTpl(pill): the message box — the context chip, the quick replies
 * and the hint (composer rev 2; rev 1: chips above the box). Paused, it is
 * read-only and keeps the draft in view (nothing can be sent until the
 * person resumes, and an editable box with nowhere to send would invite
 * words that go nowhere); the app's own bar under it says it is paused and
 * holds Resume — resuming is a grant, and this view draws none. Its
 * `value` is the draft, controlled
 * (docs/native.md §Controlled props): a send takes the words as the draft,
 * and only a message the backend took clears it — one it refused (the
 * agent's switch, 409 agent-off; no connection) stays in the box, unsent.
 */
export function composerTpl(pill = '') {
  const S = st.S;
  if (st.paused()) {
    // the words the person typed stay where they were, read-only, until they resume
    return html`<composer value=${S.drafts.get(S.sel) || ''} placeholder=${W.paused} disabled hint=${has('composer', 2) ? W.keptPaused : nothing}></composer>`;
  }
  const cs = st.composerNow();
  const c2 = has('composer', 2);
  const c = st.contextNow();
  const chips = c ? [{ id: 'about', label: aboutLabel(c), icon: c.tile ? 'box' : 'list', ...(c.not ? { tone: 'warn' } : {}) }] : [];
  const replies = cs.disabled ? [] : st.replies();
  const hint = st.hint();
  const off = cs.disabled || S.sel == null;
  return html`<composer value=${S.drafts.get(S.sel) || ''} placeholder=${cs.placeholder} ?busy=${st.busy()} ?disabled=${off}
      context=${c2 && chips.length ? chips : nothing} replies=${c2 && replies.length ? replies.map((r) => ({ id: r.id, label: r.label })) : nothing}
      hint=${c2 && hint ? hint : nothing}
      @input=${(e) => { S.drafts.set(S.sel, e.value); }} @send=${(e) => { S.drafts.set(S.sel, e.value); st.send(e.value); }} @stop=${() => st.stop()}
      @context=${c2 && c && c.not ? () => st.openScope({}) : nothing} @remove-context=${c2 ? () => st.dropContext() : nothing}
      @reply=${c2 ? (e) => st.reply(e.id) : nothing}>
    ${pill ? html`<button role="primary" @tap=${jump}>${pill}</button>` : nothing}
    ${!c2 && c ? html`<button icon="xmark" @tap=${() => st.dropContext()}>${aboutLabel(c)}</button>` : nothing}
    ${!c2 ? replies.map((r) => html`<button role=${r.primary ? 'primary' : nothing} @tap=${() => st.reply(r.id)}>${r.label}</button>`) : nothing}
    ${late.slice(-4).map((b) => html`<button role=${b.role || nothing} @tap=${b.tap}>${b.label}</button>`)}
    ${repeat(st.session.queued(), (q) => q.id, (q) => html`<button icon="xmark" @tap=${() => st.takeBack(q.id)}>${'queued: ' + String(q.text || '').slice(0, 40)}</button>`)}
  </composer>`;
}
