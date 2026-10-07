// native/chat.js — an open conversation, full screen: the transcript drawn
// from the model's blocks (model/fold.js → the chat family: message,
// thinking, toolcard with a subagent's own transcript inside, step, notice,
// activity, approval, question), the composer (attachments the app
// uploads itself, Stop giving queued text back, queued messages as chips you
// take back, a placeholder by state) and the toolbar's menu (retry, rename,
// compact, learn, memory, files, the sandbox, the tree, share, delete). Who
// may do what is model/rules.js — the same words and controls as the web's
// top bar; the coding sandbox's picker and ▣ are native/sandboxes.js.
// Feature modules draw into it through the seams (ctx.ext, native/ext.js):
// a block their way, the transcript's end, the toolbar, the menu, the composer.
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { ui, ctx, fail, guard, push, secs, clip, base, cardState, FAMILY_ICON, thumb, raw, IMAGE, draftKey, draftOf, setDraft, clearer } from './ui.js';
import { argsShown } from '../model/tool-heads.js';
import { MAX_ATTACH, fmtBytes } from '../model/actions.js';
import { sandboxPickerTpl, badgeWords, brokenTpl, sandboxMenuTpl } from './sandboxes.js';
import { openRender, openLive } from './tools.js';
import { openPublish } from './homes.js'; // one of a person's own conversations: shared by a copy
import { newChat, openAutos, rev2 } from './nav.js';
import { hostedNoticeTpl, hostedComposer, hostedButtonsTpl } from './hosted.js'; // a non-secure conversation's warning and lock
import { steerWords } from '../model/harness-ask.js'; // a coding harness's queued chips
import { activityStill } from '../model/harness.js'; // …and its activity line

const CUT = 1200; // a long result is cut here; the card's ↗ opens all of it

// --- blocks → the chat family -----------------------------------------------------

// isOpen/setOpen keep a card's open state where the web keeps it (the
// session's ui map), so a streamed token never folds what you opened.
const isOpen = (id, dflt) => ctx.app.session.ui.isOpen(id, dflt);
const setOpen = (id) => (e) => { ctx.app.session.open.set(id, !!e.open); ctx.paint(); };

// The engine's and the automations' notices fold like the web's (↵ head).
const NOTICE_ICON = { Scheduled: 'clock', 'Watcher check': 'eye', 'Learn a skill': 'sparkles', Triggered: 'bolt' };

export function blockTpl(b, depth = 0) {
  return ctx.ext.block(b, depth) || builtInTpl(b, depth);
}

function builtInTpl(b, depth) {
  switch (b.k) {
    case 'user': return userTpl(b);
    case 'notice': return noticeTpl(b);
    case 'think': return html`<thinking text=${b.text} ?live=${b.live} seconds=${b.live ? nothing : b.ms ? secs(b.ms) : nothing}
      open=${isOpen(b.id, b.live)} @toggle=${setOpen(b.id)}/>`;
    case 'assistant': return html`<message role="assistant" markdown text=${b.text}>
      <actions><button icon="copy" copy=${b.text}>Copy</button></actions></message>`;
    case 'draft': return html`<message role="assistant" markdown streaming text=${b.text}/>`;
    case 'tool': return toolTpl(b, depth);
    case 'agent': return agentTpl(b, depth);
    case 'step': return stepTpl(b);
  }
  return nothing;
}

// rowTpl: a transcript row, built again only when its block (the fold hands
// back the same object until it changes) or its open state changed. A
// subagent's card and a call with a harness subagent's blocks inside (their
// own rows open and close inside them), a user's message (its files come and
// go) and a row a seam draws (ext.block keeps its own memo) are built every time.
const rowMemo = new WeakMap();
function rowTpl(b) {
  const x = ctx.ext.block(b, 0);
  if (x) return x;
  const stamp = b.k === 'think' ? isOpen(b.id, b.live) : (b.k === 'tool' && !b.kids) || b.k === 'notice' ? isOpen(b.id, false)
    : b.k === 'assistant' || b.k === 'step' ? true : null;
  if (stamp == null) return builtInTpl(b, 0);
  const m = rowMemo.get(b);
  if (m && m.stamp === stamp) return m.tpl;
  const tpl = builtInTpl(b, 0);
  rowMemo.set(b, { stamp, tpl });
  return tpl;
}

function userTpl(b) {
  const s = ctx.app.session;
  const me = s.ui.me ? s.ui.me() : '';
  const other = b.sender && b.sender !== me; // someone else in a shared conversation: say who
  const v = s.current();
  const files = (b.files || []).map((f) => ({ f, linked: linked(v, b.msgId, f) }));
  return html`<message role="user" text=${b.text} sender=${other ? b.sender : nothing}
      files=${files.length ? files.map(({ f, linked: ok }) => ({
        name: ok ? `${base(f.path)} · ${f.size}` : `${base(f.path)} — deleted`, mime: f.mime,
        src: ok && IMAGE.test(f.mime) ? (/webp$/.test(f.mime) ? raw(v.run.id, f.path) : thumb(v.run.id, f.path)) : undefined,
      })) : nothing}>
    <actions>
      ${b.text ? html`<button icon="copy" copy=${b.text}>Copy</button>` : nothing}
      ${repeat(files.filter((x) => x.linked), (x) => x.f.path, (x) => html`<button icon="folder"
        @tap=${() => openFile(v.run.id, x.f.path)}>${`Open ${base(x.f.path)}`}</button>`)}
    </actions>
  </message>`;
}

// linked: the attachment still exists (a message_files link; the web's fileState).
const linked = (v, msgId, f) => !!(v && ((v.messageFiles || {})[msgId] || []).includes(f.path));

export function openFile(runId, path) {
  push({ kind: 'files', run: runId });
  push({ kind: 'file', run: runId, path });
}

function noticeTpl(b) {
  const lines = b.text.split('\n');
  const head = lines[0].replace(/^\[|\]$/g, '').replace(/ — .*$/, '');
  const label = head.split(' · ')[0];
  return html`<toolcard title=${'↵ ' + head} icon=${NOTICE_ICON[label] || 'mail'} family="notice"
      open=${isOpen(b.id, false)} @toggle=${setOpen(b.id)}>
    <text selectable>${lines.slice(1).join('\n')}</text>
  </toolcard>`;
}

// argRows: short arguments as one "key: value" block, long ones each their
// own — parsed once per block object (the fold hands back the same block
// until its call changes; a call being written is a new one each time).
const argMemo = new WeakMap();
function argRows(b) {
  let t = argMemo.get(b);
  if (t) return t;
  const short = [], long = [];
  for (const [k, v] of Object.entries(argsShown(b.args))) {
    const s = typeof v === 'string' ? v : JSON.stringify(v, null, 2);
    (s.length > 80 || s.includes('\n') ? long : short).push([k, s]);
  }
  t = html`${short.length ? html`<text mono selectable>${short.map(([k, s]) => `${k}: ${s}`).join('\n')}</text>` : nothing}
    ${repeat(long, ([k]) => k, ([k, s]) => html`<text style="caption" tone="muted">${k}</text><code text=${clip(s, CUT * 4)}/>`)}`;
  argMemo.set(b, t);
  return t;
}

// A sandbox call's outcome (model/tool-heads.js outcome) as a chip's tone.
const OUTCOME_TONE = { ok: 'ok', bad: 'danger', run: 'accent' };

function toolTpl(b, depth = 0) {
  const { state, chip } = cardState(b.state);
  const long = b.result && b.result.length > CUT;
  const done = b.result && b.state !== 'running';
  // a sandbox call (▣) says what it came to on the card, and its command inside
  const oc = b.outcome ? [{ text: b.outcome.text, ...(OUTCOME_TONE[b.outcome.tone] ? { tone: OUTCOME_TONE[b.outcome.tone] } : {}) }] : [];
  const chips = [...oc, ...(chip ? [chip] : [])];
  return html`<toolcard title=${b.headline} icon=${FAMILY_ICON[b.fam] || 'wrench'} family=${b.fam} state=${state}
      chips=${chips.length ? chips : nothing} open=${isOpen(b.id, false)} @toggle=${setOpen(b.id)}
      @open=${long ? () => push({ kind: 'call', run: ctx.app.sel, id: b.id }) : nothing}>
    <text style="caption" tone="muted" mono>${b.name}</text>
    ${b.sub ? html`<text mono selectable>${b.sub}</text>` : nothing}
    ${argRows(b)}
    ${done ? html`<code text=${long ? b.result.slice(0, CUT) + '…' : b.result}/>` : nothing}
    ${done && long ? html`<text style="footnote" tone="muted">${`cut at ${CUT} of ${b.result.length} characters — ↗ shows all`}</text>` : nothing}
    ${b.kids ? html`<transcript>${repeat(b.kids, (x) => x.id, (x) => blockTpl(x, depth + 1))}</transcript>` : nothing}
  </toolcard>`;
}

// A delivered child result starts with "--- #id title (outcome) ---".
const stripHead = (s) => String(s || '').replace(/^--- #\d+ .*? ---\n/, '');

function agentTpl(b, depth) {
  const running = b.state === 'running' || b.state === 'approval';
  const open = isOpen(b.id, running);
  const child = b.child || {};
  const title = b.link && b.link.label ? b.link.label : b.headline;
  const phase = b.link && b.link.phase ? b.link.phase : child.status || '';
  const steps = child.llmCalls ? `${child.llmCalls} step${child.llmCalls === 1 ? '' : 's'}` : '';
  if (open && b.childId && !b.blocks) ctx.app.session.ui.act.loadChild(b.childId); // once: a failed read waits (model/session.js Failures)
  const readErr = open && b.childId && !b.blocks ? ctx.app.session.ui.readError(b.childId) : '';
  const chips = [
    ...(b.childId ? [{ text: '#' + b.childId }] : []),
    ...(b.state === 'done' ? [{ text: steps || 'done' }] : phase ? [{ text: phase, tone: b.state === 'approval' ? 'warn' : 'accent' }] : []),
    ...(b.state === 'error' ? [{ text: 'failed', tone: 'danger' }] : b.state === 'stopped' ? [{ text: 'stopped' }] : []),
  ];
  const state = { running: 'running', approval: 'running', error: 'error', stopped: 'canceled', done: 'ok' }[b.state] || 'running';
  const answer = !running && b.result && !b.result.startsWith('(') ? stripHead(b.result) : '';
  return html`<toolcard title=${title} icon="branch" family="agent" state=${state} chips=${chips}
      open=${open} @toggle=${(e) => { if (e.open && b.childId) ctx.app.session.failed.clear(b.childId); setOpen(b.id)(e); }}
      @open=${b.childId ? () => openChild(b.childId) : nothing}>
    ${b.task ? html`<text style="footnote" tone="muted" lines=${isOpen(b.id + ':task', false) ? nothing : 3}>${'task: ' + b.task}</text>` : nothing}
    <transcript>
      ${b.blocks ? repeat(b.blocks, (x) => x.id, (x) => blockTpl(x, depth + 1))
        : readErr ? html`<notice tone="danger" text=${`Couldn't read its steps: ${readErr} — fold the card and open it again to retry.`}/>`
        : html`<progress label="loading…"/>`}
      ${b.pendingApproval ? approvalTpl(b.pendingApproval, b.childId, 'The subagent wants to run') : nothing}
    </transcript>
    ${answer ? html`<text style="caption" tone="muted">answer</text><markdown source=${answer}/>` : nothing}
  </toolcard>`;
}

// openChild pushes a subagent's session full screen (the web's "open ↗"):
// over its parent's, so back returns to it (native/nav.js).
export function openChild(id) {
  ctx.app.select(id);
}

// a journal line's glyph is text (the step primitive draws a string): no emoji (D184)
const STEP = {
  error: ['!', 'danger', (d) => d.error || d.text || ''],
  compaction: ['≡', 'muted', (d) => ctx.app.rules.compactionWords(d)],
  yield: ['⏸', 'muted', (d) => `slept ${d.seconds ?? ''}s`], // theme-ok: the native step primitive takes its glyph as text the app draws (vocab step.glyph)
  finish: ['✓', 'ok', (d) => (d.result ? `finished: ${d.result}` : 'finished')], // theme-ok: the step primitive's text glyph
  state_changed: ['✳', 'accent', (d) => `state changed${d.summary ? ': ' + d.summary : ''}`],
  cancel: ['⏹', 'warn', (d) => `cancelled${d.reason ? ': ' + d.reason : ''}`], // theme-ok: the step primitive's text glyph
  ask: ['?', 'accent', (d) => `asked: ${d.question || ''}`],
  render: ['▢', 'accent', (d) => `rendered ${d.path || ''} v${d.version || ''}`],
  live: ['◎', 'accent', (d) => `showing ${d.name || d.sandbox || 'the sandbox'}:${d.port || ''}${d.path || '/'} live`],
};
// stepTpl: a journal line. A render or a live page is a card that opens it
// again (a step has no tap); finish's result is markdown under its line.
function stepTpl(b) {
  const d = b.detail || {};
  const [glyph, tone, text] = STEP[b.kind] || ['•', 'muted', (x) => x.text || ''];
  const run = b.run || ctx.app.sel; // a subagent's step, shown in its parent: the file (the page) is its run's
  if (b.kind === 'render' && d.path) {
    return html`<toolcard title=${text(d)} icon="photo" family="render" state="ok" @open=${() => openRender(run, d.path, d.version, false)}/>`;
  }
  if (b.kind === 'live' && d.sandbox) {
    return html`<toolcard title=${text(d)} icon="globe" family="live" state="ok" @open=${() => openLive(run, d, false)}/>`;
  }
  if (b.kind === 'finish' && d.result) return html`<step glyph="✓" tone="ok" text="finished"/><markdown source=${d.result}/>`; // theme-ok: the step primitive's text glyph
  return html`<step glyph=${glyph} tone=${tone} text=${text(d)}/>`;
}

// approvalTpl: the calls a run wants to run, approve or deny (a subagent's
// from its parent's card too).
// grant (rules grantAsk) is a capability only the conversation's owner may
// allow (D111): once, or here for an hour; others may only deny.
export function approvalTpl(calls, runId, lead = 'The agent wants to run', grant = null, park = undefined) {
  const names = (calls || []).map((c) => (c.function ? c.function.name : String(c)));
  const options = !grant ? [{ id: 'approve', label: 'Approve', kind: 'allow_once' }, { id: 'deny', label: 'Deny', kind: 'reject_once' }]
    : [...(grant.canAllow ? [{ id: 'once', label: 'Allow once', kind: 'allow_once' }, { id: 'hour', label: 'Allow here for 1 hour', kind: 'allow_always' }] : []),
      { id: 'deny', label: 'Deny', kind: 'reject_once' }];
  const text = grant ? [grant.note, ...names].join('\n') : names.join('\n');
  return html`<approval title=${grant ? grant.lead : lead} text=${text} options=${options}
    @choose=${guard((e) => ctx.app.session.approve(runId, e.id !== 'deny', grant && e.id !== 'deny' ? e.id : undefined, park))}/>`;
}

// --- the conversation screen ------------------------------------------------------------

// chatRouteScreen: a conversation's entry of the stack (native/nav.js
// {kind: 'chat', run}) — the open one in full; one under it (a subagent's
// parent, a conversation another was opened from) as last seen, read again
// when back makes it the open one.
export function chatRouteScreen(s) {
  const app = ctx.app;
  if (app.sel === s.run) {
    const v = app.session.current();
    return v ? chatScreen(v) : loadingScreen(s);
  }
  const v = app.session.merged(s.run);
  return v ? parentScreen({ id: s.run, title: v.run.title || s.title }) : loadingScreen(s);
}

const loadingScreen = (s) => html`<screen title=${s.title || (ctx.app.convs.find(s.run) || {}).title || 'loading…'} style="scroll"><progress label="loading…"/></screen>`;

// A conversation under the open one: the end of its transcript as last seen
// (back re-reads it) — drawn again only when its blocks changed.
const parentMemo = new Map(); // run id → {blocks, title, tpl}
function parentScreen(c) {
  const blocks = ctx.app.session.blocks(c.id);
  const title = c.title || '#' + c.id;
  const m = parentMemo.get(c.id);
  if (m && m.blocks === blocks && m.title === title && blocks) return m.tpl;
  const rows = blocks && blocks.length > KEEP ? blocks.slice(-KEEP) : blocks;
  const tpl = html`<screen title=${title} style="scroll">
    <transcript>${rows ? repeat(rows, (b) => b.id, rowTpl) : html`<progress/>`}</transcript>
  </screen>`;
  if (parentMemo.size > 8) parentMemo.clear(); // only the chain under the open conversation is drawn
  parentMemo.set(c.id, { blocks, title, tpl });
  return tpl;
}

// --- a window of the transcript (D130) ------------------------------------------------
//
// The chat renders a window of its blocks: the tail when it opens, growing a
// page of rows at a time as the loader at its top fires `more` (a page is read
// from the backend when no held rows are left above it). The app's transcript
// keeps its bottom still while it follows it, so rows above go — the window
// trimmed from the top, and the messages far above let go (Session.keep) —
// only while the reader is at the bottom, as `scrolled` says; scrolled up,
// the window only grows.
//
// Where the app's transcript has the anchors of vocabulary rev 2 (D189), the
// reader far up lets the live end go too, as the web's window does
// (chat.jumpLatest): while they are away from the end (its `edge`), the row
// the window grew up from is the transcript's `anchor` — kept in place while
// rows come and go around it — so the window is cut below it too, and the
// messages there are let go (the live tail with them: what arrives is
// counted). The composer's "↓ N new — jump to latest" reads the newest page
// again and `scrollTo`s the end; reading down to the window's end grows it
// back a page at a time. From an app of rev 1 nothing below the window goes
// (that would move what the reader looks at) and there is no pill.
const PAGE = 40;  // rows the window opens on, and grows by
const TRIM = 120; // a window longer than this is cut back to KEEP rows — at the bottom from the top; away, below the anchor
const KEEP = 60;

const anchors = () => rev2('transcript');

// winOf: the window of s.blocks for the open run, placed for this render.
function winOf(s, run) {
  let w = ui.win;
  if (!w || w.run !== run) w = ui.win = { run, fromKey: null, toKey: null, atBottom: true, anchor: null, jumps: 0, start: 0, end: 0, n: 0 };
  const blocks = s.blocks, n = blocks.length;
  w.start = startOf(w, blocks);
  w.end = endOf(w, blocks);
  if (w.end <= w.start) { w.toKey = null; w.end = n; }
  w.n = n;
  w.fromKey = n ? blocks[w.start].id : null;
  return w;
}

// startOf: where window w starts in blocks — at its first row, else on the
// tail; at the bottom, a window grown past TRIM rows is cut back to KEEP.
function startOf(w, blocks) {
  const n = blocks.length;
  let start = w.fromKey == null ? -1 : blocks.findIndex((b) => b.id === w.fromKey);
  if (start < 0) start = Math.max(0, n - PAGE);
  if (w.atBottom && n - start > TRIM) start = n - KEEP;
  return start;
}

// endOf: where it ends — its last row (cut below the anchor), else the end.
function endOf(w, blocks) {
  if (w.toKey == null) return blocks.length;
  const i = blocks.findIndex((b) => b.id === w.toKey);
  return i < 0 ? blocks.length : i + 1;
}

// more: the loader at the transcript's top is on screen — a page of held
// rows joins the window, else the next older page is read and joins it. The
// row it grew up from keeps its place (rev 2: the anchor, while away).
const more = guard(async () => {
  const w = ui.win, s = ctx.app.session;
  if (!w || w.run !== ctx.app.sel) return;
  const blocks = s.shown().blocks;
  if (anchors() && !w.atBottom && w.fromKey != null) w.anchor = w.fromKey;
  if (w.start > 0) { w.fromKey = blocks[Math.max(0, w.start - PAGE)].id; return; }
  if (!s.shown().hasOlder) return;
  await s.loadOlder();
  const now = s.shown().blocks, i = now.findIndex((b) => b.id === w.fromKey);
  if (i > 0) w.fromKey = now[Math.max(0, i - PAGE)].id;
});

// atEnd: the reader is at the live end (true) or left it — the model counts
// what arrives meanwhile (Session.follow); at the end no row is anchored.
function atEnd(w, at) {
  if (at === w.atBottom) return;
  w.atBottom = at;
  if (at) w.anchor = null;
  ctx.app.session.follow(at);
  ctx.paint();
}

// scrolled (rev 1): the reader left the bottom, or came back to it (the
// window is trimmed at the next render).
function scrolled(e) {
  const w = ui.win;
  if (w) atEnd(w, !!e.atBottom);
}

// edge (rev 2): the transcript's end came into view or left it. The end of a
// window cut below (or of the held pages, the live tail let go) is not the
// live end: the window grows a page down, read back when need be.
function edge(e) {
  const w = ui.win, s = ctx.app.session;
  if (!w || e.edge !== 'end' || w.run !== ctx.app.sel) return;
  const v = s.shown();
  if (e.at && (w.end < v.blocks.length || v.hasNewer || v.detached)) return below(w, v);
  atEnd(w, !!e.at);
}

const below = guard(async (w, v) => {
  const n = v.blocks.length;
  if (w.end < n) { const e = w.end + PAGE; w.toKey = e < n ? v.blocks[e - 1].id : null; return; }
  if (v.hasNewer) await ctx.app.session.loadNewer();
  else await ctx.app.session.latest();
  w.toKey = null;
});

// jump: "↓ N new — jump to latest" — the newest page again when the tail was
// let go, the window back on it, scrolled to the end and followed.
const jump = guard(async () => {
  const w = ui.win, s = ctx.app.session;
  if (!w) return;
  const v = s.shown();
  if (v.hasNewer || v.detached) await s.latest();
  w.fromKey = w.toKey = w.anchor = null;
  w.jumps++;
  atEnd(w, true);
});

// pillOf: the pill's words while the reader is away and something is below ('' else).
function pillOf(w, v) {
  if (!anchors() || w.atBottom || !(v.fresh || v.hasNewer || v.detached || w.end < v.blocks.length)) return '';
  return `↓ ${v.fresh ? `${v.fresh} new — ` : ''}jump to latest`;
}

// chatDrawn (native.js, after a render): at the bottom, what lies far above
// the window is let go (a page or more at a time); away from it with a row
// anchored (rev 2), a window grown long is cut below the anchor and what lies
// below it let go — the live tail too: what arrives is counted.
export function chatDrawn() {
  const w = ui.win, app = ctx.app;
  if (!w || app.sel !== w.run) return;
  const blocks = app.session.shown().blocks;
  if (!w.atBottom) {
    if (!anchors() || w.anchor == null) return;
    const a = blocks.findIndex((b) => b.id === w.anchor);
    if (a < 0 || endOf(w, blocks) - a <= TRIM) return;
    const e = Math.min(blocks.length, a + KEEP);
    w.toKey = blocks[e - 1].id;
    if (!app.session.keep(0, e, true)) ctx.paint();
    return;
  }
  const i = startOf(w, blocks); // where the render this paint asked for places it
  if (i <= KEEP) return;
  w.fromKey = blocks[i].id;
  app.session.keep(i - KEEP, blocks.length, false);
}

export function chatScreen(v) {
  const app = ctx.app;
  const { rules } = app;
  const s = app.session.shown();
  const w = winOf(s, v.run.id);
  const r = v.run;
  const t = rules.topBar(v, app.convs.find(r.rootId || r.id), app.me);
  const ps = r.pendingState || {};
  const tail = ctx.ext.end(v, s);
  const parked = r.status === 'waiting_input' && !(tail && ps.harness); // a harness park is the seams' while they answer
  const chain = (v.chain || []).map((c) => c.title || '#' + c.id);
  // a shared conversation says so in its header, as the web's top bar does
  const subtitle = [chain.length ? 'in ' + chain.join(' › ') : '', ...(ctx.ext.subtitle(v) || []), r.status, t.cls.label, t.cls.warn, badgeWords(v), t.viewOnly ? 'view only' : '',
    t.share.tone ? t.share.label : '', t.model || '', ...t.grants.map((g) => g.label)].filter(Boolean).join(' · ');
  return html`<screen title=${t.title} subtitle=${subtitle} style="scroll">
    <toolbar>
      <button icon="pencil" @tap=${newChat}>New chat</button>
      ${modelPickerTpl(v)}
      ${sandboxPickerTpl()}
      ${ctx.ext.toolbar(v) || nothing}
      <menu icon="ellipsis" label="More">${runMenu(v, t)}</menu>
    </toolbar>
    <transcript follow ?older=${w.start > 0 || s.hasOlder} @more=${more}
        anchor=${anchors() && !w.atBottom && w.anchor != null ? String(w.anchor) : nothing}
        scrollTo=${anchors() ? `end#${w.jumps}` : nothing}
        @scrolled=${anchors() ? nothing : scrolled} @edge=${anchors() ? edge : nothing}>
      ${hostedNoticeTpl(v)}
      ${s.olderHidden && !w.start && !s.hasOlder ? html`<notice tone="muted" text="earlier turns were compacted into the summary"/>` : nothing}
      ${repeat(w.start || w.end < s.blocks.length ? s.blocks.slice(w.start, w.end) : s.blocks, (b) => b.id, rowTpl)}
      ${tail || nothing}
      ${parked && ps.kind === 'approval' ? approvalTpl(ps.toolCalls, r.id, undefined, rules.grantAsk(r, app.me), ps.park) : nothing}
      ${parked && ps.kind !== 'approval' && r.result ? questionTpl(r) : nothing}
      ${s.activity ? html`<activity ?live=${!activityStill(r)} text=${s.activity}/>` : nothing}
      ${s.conn === 'reconnecting' ? html`<notice tone="warn" text="live updates lost — reconnecting…"/>` : nothing}
      ${app.halted ? html`<notice tone="warn" title="Halted" text="Every run of this agent is stopped until a manager resumes it."/>` : nothing}
      ${brokenTpl(v)}
      ${ui.err ? html`<notice tone="danger" text=${ui.err}/>` : nothing}
      ${ui.note ? html`<notice tone="ok" text=${ui.note}/>` : nothing}
    </transcript>
    ${composerTpl(v, t, pillOf(w, s))}
  </screen>`;
}

// plain: a markdown question as text (a schema's description is never markup).
const plain = (md) => String(md ?? '').replace(/\*\*(.+?)\*\*|__(.+?)__|`([^`]+)`/g, (m, a, b, c) => a ?? b ?? c).replace(/^#+\s*/gm, '');

// The agent's question (its run's result), answered by your next message —
// here or in the composer.
function questionTpl(r) {
  const schema = { type: 'object', description: plain(r.result), required: ['answer'],
    properties: { answer: { type: 'string', title: 'Your answer' } } };
  return html`<question title="The agent is asking" schema=${schema}
    @submit=${(e) => ctx.app.send(String((e.content || {}).answer || ''), clearer())}/>`;
}

// runMenu: the top bar's controls (model/rules.js topBar) in the toolbar's menu.
function runMenu(v, t) {
  const app = ctx.app;
  const id = v.run.id;
  const control = (what) => guard(() => app.actions.control(id, what));
  return html`
    ${t.retry ? html`<button icon="refresh" @tap=${control('resume')}>Retry</button>` : nothing}
    ${t.own ? html`<button icon="pencil" @tap=${() => { ui.rename = { id: t.shareRun.id, title: t.shareRun.title || '' }; ctx.paint(); }}>Rename…</button>` : nothing}
    ${t.compact ? html`<button icon="archive" @tap=${control('compact')}>Compact</button>` : nothing}
    ${t.learn ? html`<button icon="sparkles" @tap=${control('learn')}>Learn skill</button>` : nothing}
    ${t.task ? html`<button icon="pin" @tap=${() => push({ kind: 'task', run: id })}>${t.task.more ? `Task (+${t.task.more})` : 'Task'}</button>` : nothing}
    ${t.memory != null ? html`<button icon="database" @tap=${() => push({ kind: 'memory', run: id })}>${`Memory (${t.memory})`}</button>` : nothing}
    <button icon="folder" @tap=${() => push({ kind: 'files', run: id })}>${`Files (${t.files})`}</button>
    ${sandboxMenuTpl(v)}
    ${t.tree ? html`<button icon="branch" @tap=${() => push({ kind: 'tree', root: v.run.rootId || id })}>Workflow tree</button>` : nothing}
    ${t.sharing ? html`<button icon="people" @tap=${() => { ui.share = { run: t.shareRun }; ctx.paint(); }}>${t.own ? 'Share' : 'Shared'}</button>` : nothing}
    ${t.publish && t.own ? html`<button icon="people" @tap=${() => openPublish(t.shareRun)}>Share a copy…</button>` : nothing}
    ${t.grants.filter((g) => g.revoke).map((g) => html`<button icon="lock" @tap=${guard(() => app.session.revokeGrant(g.run, g.cap))}>${`Revoke: ${g.label}`}</button>`)}
    ${t.crumb ? html`<button icon="clock" @tap=${() => openAutos(t.crumb.kind, t.crumb.id)}>Its automation</button>` : nothing}
    ${ctx.ext.menu(v, t) || nothing}
    ${t.del ? html`<divider/><button icon="trash" role="destructive"
      confirm=${{ title: 'Delete this run and its history?', label: 'Delete', destructive: true }}
      @tap=${guard(async () => { await app.actions.deleteRun(id); app.session.runs.delete(id); app.home(); })}>Delete</button>` : nothing}`;
}

// modelPickerTpl: the model (model/rules.js modelPicker) — the open
// conversation's from its next turn, or at home the next new chat's. In the
// toolbar: the app's composer holds buttons only.
export function modelPickerTpl(v) {
  const app = ctx.app;
  const p = app.rules.modelPicker(v, app.model, app.catalog);
  if (!p.shown || p.disabled) return nothing;
  return html`<picker label="Model" style="menu" value=${p.value} options=${p.options.map(({ value, label }) => ({ value, label }))}
    @change=${(e) => guard(() => app.pickModel(e.value))()}/>`;
}

// --- the composer ------------------------------------------------------------------------

// composerTpl: the composer — and, while the reader is away from the live
// end of the open conversation, its "↓ N new — jump to latest" (pill).
export function composerTpl(v, t, pill = '') {
  const app = ctx.app;
  const c = v ? hostedComposer(app.rules.composer(v, app.HOME), v) : app.rules.composer(v, app.HOME);
  // the seams' part (ext.composer): the last placeholder given, every slash command, their buttons
  const xs = ctx.ext.composer(v, t) || [];
  const placeholder = xs.reduce((p, x) => x.placeholder || p, c.placeholder);
  const slash = xs.flatMap((x) => x.slash || []);
  const talk = !v || app.rules.access(v).talk;
  // what was picked here (at home: into the new ask's draft, which Send sends)
  const place = v ? v.run.id : 'home';
  const att = app.attach.here(place).map((a) => ({
    id: String(a.key), name: a.err ? `${a.name} — ${a.err}` : a.name, mime: a.type || '',
    ...(a.state === 'up' ? { progress: 0 } : {}),
  }));
  const queued = v ? app.session.queued() : [];
  const qw = steerWords(v, { native: true });
  const dk = draftKey(); // its own draft: each conversation's, the new chat screen's (native/ui.js)
  return html`<composer value=${draftOf(dk)} placeholder=${placeholder} ?busy=${c.busy} ?disabled=${c.disabled}
      attachments=${att} slash=${slash.length ? slash : nothing}
      upload=${talk ? app.uploadTarget() : nothing}
      @input=${(e) => setDraft(e.value, dk)}
      @send=${(e) => app.send(e.value, clearer(dk))}
      @stop=${stop}
      @uploaded=${uploaded(place)}
      @remove=${(e) => app.attach.remove(+e.id)}>
    ${pill ? html`<button role="primary" @tap=${jump}>${pill}</button>` : nothing}
    ${t && t.retry ? html`<button icon="refresh" role="primary" @tap=${guard(() => app.actions.control(v.run.id, 'resume'))}>Retry</button>` : nothing}
    ${v ? hostedButtonsTpl(v) : nothing}
    ${repeat(queued, (q) => q.id, (q) => html`<button icon="xmark"
      @tap=${guard(() => app.session.removeQueued(q.id))}>${(qw ? qw.label : 'queued') + ': ' + clip(q.text || '(files)', 40)}</button>`)}
    ${xs.map((x) => (x.tpl ? x.tpl() : nothing))}
  </composer>`;
}

// Stop interrupts the run; what was still queued comes back into the composer.
const stop = guard(async () => {
  const k = draftKey();
  const text = await ctx.app.stop();
  if (text) setDraft([text, draftOf(k)].filter(Boolean).join('\n\n'), k);
});

// uploaded: the app put a picked file into the run (PUT /runs/{id}/upload),
// or at home into the new ask's draft (PUT /ask/upload?draft=), and hands over
// the backend's answer; the next Send there names it. The chip stays where it
// was picked (`at`), even if you went elsewhere meanwhile.
const uploaded = (place) => (e) => {
  const r = e.response || {};
  if (r.path) {
    ctx.app.attach.uploaded({ name: e.name, size: r.bytes || 0, type: r.mime || '', path: r.path, at: place });
    return;
  }
  // Refused over the 16 MiB cap (413): a chip marked like the web's, which
  // holds Send until it is removed. Any other failure is just said.
  if (!/413|too large|over 16/i.test(JSON.stringify(r))) { fail(`${e.name}: ${r.error || 'upload failed'}`); return; }
  const a = ctx.app.attach;
  a.items.push({ key: ++a.seq, name: e.name, size: MAX_ATTACH + 1, type: '', state: 'bad', err: `too large (max ${fmtBytes(MAX_ATTACH)})`, at: place });
  a.changed();
};
