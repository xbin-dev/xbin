// native/chat.js — an open conversation, full screen: the transcript drawn
// from the model's blocks (model/fold.js → the chat family: message,
// thinking, toolcard with a subagent's own transcript inside, step, notice,
// activity, approval, question), the composer (tool mode, attachments the app
// uploads itself, Stop giving queued text back, queued messages as chips you
// take back, a placeholder by state) and the toolbar's menu (retry, rename,
// compact, learn, memory, files, the tree, share, delete). Who may do what is
// model/rules.js — the same words and controls as the web's top bar.
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { ui, ctx, fail, guard, push, secs, clip, base, cardState, FAMILY_ICON, thumb, raw, IMAGE } from './ui.js';
import { argsShown } from '../model/tool-heads.js';
import { fold } from '../model/fold.js';
import { MAX_ATTACH, fmtBytes } from '../model/actions.js';

const CUT = 1200; // a long result is cut here; the card's ↗ opens all of it

// --- blocks → the chat family -----------------------------------------------------

// isOpen/setOpen keep a card's open state where the web keeps it (the
// session's ui map), so a streamed token never folds what you opened.
const isOpen = (id, dflt) => ctx.app.session.ui.isOpen(id, dflt);
const setOpen = (id) => (e) => { ctx.app.session.open.set(id, !!e.open); ctx.paint(); };

// The engine's and the automations' notices fold like the web's (↵ head).
const NOTICE_ICON = { Scheduled: 'clock', 'Watcher check': 'eye', 'Learn a skill': 'sparkles', Triggered: 'bolt' };

export function blockTpl(b, depth = 0) {
  switch (b.k) {
    case 'user': return userTpl(b);
    case 'notice': return noticeTpl(b);
    case 'think': return html`<thinking text=${b.text} ?live=${b.live} seconds=${b.live ? nothing : b.ms ? secs(b.ms) : nothing}
      open=${isOpen(b.id, b.live)} @toggle=${setOpen(b.id)}/>`;
    case 'assistant': return html`<message role="assistant" markdown text=${b.text}>
      <actions><button icon="copy" copy=${b.text}>Copy</button></actions></message>`;
    case 'draft': return html`<message role="assistant" markdown streaming text=${b.text}/>`;
    case 'tool': return toolTpl(b);
    case 'agent': return agentTpl(b, depth);
    case 'step': return stepTpl(b);
  }
  return nothing;
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

// argRows: short arguments as one "key: value" block, long ones each their own.
function argRows(raw) {
  const a = argsShown(raw);
  const short = [], long = [];
  for (const [k, v] of Object.entries(a)) {
    const s = typeof v === 'string' ? v : JSON.stringify(v, null, 2);
    (s.length > 80 || s.includes('\n') ? long : short).push([k, s]);
  }
  return html`${short.length ? html`<text mono selectable>${short.map(([k, s]) => `${k}: ${s}`).join('\n')}</text>` : nothing}
    ${repeat(long, ([k]) => k, ([k, s]) => html`<text style="caption" tone="muted">${k}</text><code text=${clip(s, CUT * 4)}/>`)}`;
}

function toolTpl(b) {
  const { state, chip } = cardState(b.state);
  const long = b.result && b.result.length > CUT;
  const done = b.result && b.state !== 'running';
  return html`<toolcard title=${b.headline} icon=${FAMILY_ICON[b.fam] || 'wrench'} family=${b.fam} state=${state}
      chips=${chip ? [chip] : nothing} open=${isOpen(b.id, false)} @toggle=${setOpen(b.id)}
      @open=${long ? () => push({ kind: 'call', run: ctx.app.sel, id: b.id }) : nothing}>
    <text style="caption" tone="muted" mono>${b.name}</text>
    ${argRows(b.args)}
    ${done ? html`<code text=${long ? b.result.slice(0, CUT) + '…' : b.result}/>` : nothing}
    ${done && long ? html`<text style="footnote" tone="muted">${`cut at ${CUT} of ${b.result.length} characters — ↗ shows all`}</text>` : nothing}
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
  if (open && b.childId && !b.blocks) ctx.app.session.ui.act.loadChild(b.childId);
  const chips = [
    ...(b.childId ? [{ text: '#' + b.childId }] : []),
    ...(b.state === 'done' ? [{ text: steps || 'done' }] : phase ? [{ text: phase, tone: b.state === 'approval' ? 'warn' : 'accent' }] : []),
    ...(b.state === 'error' ? [{ text: 'failed', tone: 'danger' }] : b.state === 'stopped' ? [{ text: 'stopped' }] : []),
  ];
  const state = { running: 'running', approval: 'running', error: 'error', stopped: 'canceled', done: 'ok' }[b.state] || 'running';
  const answer = !running && b.result && !b.result.startsWith('(') ? stripHead(b.result) : '';
  return html`<toolcard title=${title} icon="branch" family="agent" state=${state} chips=${chips}
      open=${open} @toggle=${setOpen(b.id)} @open=${b.childId ? () => openChild(b.childId) : nothing}>
    ${b.task ? html`<text style="footnote" tone="muted" lines=${isOpen(b.id + ':task', false) ? nothing : 3}>${'task: ' + b.task}</text>` : nothing}
    <transcript>
      ${b.blocks ? repeat(b.blocks, (x) => x.id, (x) => blockTpl(x, depth + 1)) : html`<progress label="loading…"/>`}
      ${b.pendingApproval ? approvalTpl(b.pendingApproval, b.childId, 'The subagent wants to run') : nothing}
    </transcript>
    ${answer ? html`<text style="caption" tone="muted">answer</text><markdown source=${answer}/>` : nothing}
  </toolcard>`;
}

// openChild pushes a subagent's session full screen (the web's "open ↗").
export function openChild(id) {
  ui.opening = id;
  ctx.app.select(id);
}

const STEP = {
  error: ['⚠', 'danger', (d) => d.error || d.text || ''],
  compaction: ['🗜', 'muted', (d) => `compacted ${d.messages || 0} message(s) into the summary`],
  yield: ['⏸', 'muted', (d) => `slept ${d.seconds ?? ''}s`],
  finish: ['✓', 'ok', (d) => (d.result ? `finished: ${d.result}` : 'finished')],
  state_changed: ['✳', 'accent', (d) => `state changed${d.summary ? ': ' + d.summary : ''}`],
  cancel: ['⏹', 'warn', (d) => `cancelled${d.reason ? ': ' + d.reason : ''}`],
  ask: ['?', 'accent', (d) => `asked: ${d.question || ''}`],
  render: ['🖼', 'accent', (d) => `rendered ${d.path || ''} v${d.version || ''}`],
};
function stepTpl(b) {
  const [glyph, tone, text] = STEP[b.kind] || ['•', 'muted', (d) => d.text || ''];
  return html`<step glyph=${glyph} tone=${tone} text=${text(b.detail || {})}/>`;
}

// approvalTpl: the calls a run wants to run, approve or deny (a subagent's
// from its parent's card too).
export function approvalTpl(calls, runId, lead = 'The agent wants to run') {
  const names = (calls || []).map((c) => (c.function ? c.function.name : String(c)));
  return html`<approval title=${lead} text=${names.join('\n')}
    options=${[{ id: 'approve', label: 'Approve', kind: 'allow_once' }, { id: 'deny', label: 'Deny', kind: 'reject_once' }]}
    @choose=${guard((e) => ctx.app.session.approve(runId, e.id === 'approve'))}/>`;
}

// --- the conversation screen ------------------------------------------------------------

// chatScreens: the open conversation — a subagent's parents first (the
// breadcrumbs: back goes up the chain), then the run itself.
export function chatScreens() {
  const app = ctx.app;
  const v = app.session.current();
  if (!v) {
    // loading: a subagent opened from its card keeps the screens under it
    const under = ui.opening === app.sel && chatScreens.last ? chatScreens.last.filter((s) => s.key !== 'chat:' + app.sel) : [];
    return [...under, { key: 'chat:' + app.sel, tpl: () => loadingScreen() }];
  }
  if (ui.opening === app.sel) ui.opening = null;
  const chain = (v.chain || []).map((c) => ({ key: 'chat:' + c.id, tpl: () => parentScreen(c), back: () => app.select(c.id) }));
  const out = [...chain, { key: 'chat:' + v.run.id, tpl: () => chatScreen(v), back: () => { if (app.sel !== v.run.id) app.select(v.run.id); } }];
  chatScreens.last = out;
  return out;
}

const loadingScreen = () => html`<screen title="loading…" style="scroll"><progress label="loading…"/></screen>`;

// A parent under a subagent: its transcript as last seen (back re-reads it).
function parentScreen(c) {
  const s = ctx.app.session;
  const pv = s.merged(c.id);
  const blocks = pv ? fold(pv, (id) => s.merged(id)) : null;
  return html`<screen title=${c.title || '#' + c.id} style="scroll">
    <transcript>${blocks ? repeat(blocks, (b) => b.id, (b) => blockTpl(b)) : html`<progress/>`}</transcript>
  </screen>`;
}

export function chatScreen(v) {
  const app = ctx.app;
  const { rules } = app;
  const s = app.session.shown();
  const t = rules.topBar(v);
  const r = v.run;
  const ps = r.pendingState || {};
  const chain = (v.chain || []).map((c) => c.title || '#' + c.id);
  const subtitle = [chain.length ? 'in ' + chain.join(' › ') : '', r.status, t.laneLabel, t.viewOnly ? 'view only' : ''].filter(Boolean).join(' · ');
  return html`<screen title=${t.title} subtitle=${subtitle} style="scroll">
    <toolbar>
      <button icon="list" @tap=${() => { ui.drawer = true; ctx.paint(); }}>Conversations</button>
      <menu icon="ellipsis" label="More">${runMenu(v, t)}</menu>
    </toolbar>
    <transcript follow ?older=${s.hasOlder} @more=${() => app.session.loadOlder().catch(fail)}>
      ${ui.err ? html`<notice tone="danger" text=${ui.err}/>` : nothing}
      ${app.halted ? html`<notice tone="warn" title="Halted" text="Every run of this agent is stopped until a manager resumes it."/>` : nothing}
      ${s.olderHidden ? html`<notice tone="muted" text="earlier turns were compacted into the summary"/>` : nothing}
      ${repeat(s.blocks, (b) => b.id, (b) => blockTpl(b))}
      ${r.status === 'waiting_input' && ps.kind === 'approval' ? approvalTpl(ps.toolCalls, r.id) : nothing}
      ${r.status === 'waiting_input' && ps.kind !== 'approval' && r.result ? questionTpl(r) : nothing}
      ${s.activity ? html`<activity live text=${s.activity}/>` : nothing}
      ${s.conn === 'reconnecting' ? html`<notice tone="warn" text="live updates lost — reconnecting…"/>` : nothing}
    </transcript>
    ${composerTpl(v, t)}
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
    @submit=${(e) => ctx.app.send(String((e.content || {}).answer || ''), () => { ui.draft = ''; })}/>`;
}

// runMenu: the top bar's controls (model/rules.js topBar) in the toolbar's menu.
function runMenu(v, t) {
  const app = ctx.app;
  const id = v.run.id;
  const control = (what) => guard(() => app.actions.control(id, what));
  return html`
    ${t.retry ? html`<button icon="refresh" @tap=${control('resume')}>Retry</button>` : nothing}
    ${t.own ? html`<button icon="pencil" @tap=${() => { ui.rename = { id: t.shareRun.id, title: t.shareRun.title || '' }; ctx.paint(); }}>Rename…</button>` : nothing}
    ${t.compact ? html`<button icon="archive" @tap=${control('compact')}>Compact</button>
      <button icon="sparkles" @tap=${control('learn')}>Learn skill</button>` : nothing}
    <button icon="database" @tap=${() => push({ kind: 'memory', run: id })}>${`Memory (${t.memory})`}</button>
    <button icon="folder" @tap=${() => push({ kind: 'files', run: id })}>${`Files (${t.files})`}</button>
    ${t.tree ? html`<button icon="branch" @tap=${() => push({ kind: 'tree', root: v.run.rootId || id })}>Workflow tree</button>` : nothing}
    <button icon="people" @tap=${() => { ui.share = { run: t.shareRun }; ctx.paint(); }}>${t.share}</button>
    ${t.crumb ? html`<button icon="clock" @tap=${() => app.openAutomations(t.crumb.kind, t.crumb.id)}>Its automation</button>` : nothing}
    ${t.del ? html`<divider/><button icon="trash" role="destructive"
      confirm=${{ title: 'Delete this run and its history?', label: 'Delete', destructive: true }}
      @tap=${guard(async () => { await app.actions.deleteRun(id); app.session.runs.delete(id); app.home(); })}>Delete</button>` : nothing}`;
}

// --- the composer ------------------------------------------------------------------------

export function composerTpl(v, t) {
  const app = ctx.app;
  const c = app.rules.composer(v, app.HOME);
  const talk = !v || app.rules.access(v).talk;
  const att = app.attach.items.map((a) => ({
    id: String(a.key), name: a.err ? `${a.name} — ${a.err}` : a.name, mime: a.type || '',
    ...(a.state === 'up' ? { progress: 0 } : {}),
  }));
  const queued = v ? app.session.queued() : [];
  const web = app.toolset === 'web';
  return html`<composer value=${ui.draft} placeholder=${c.placeholder} ?busy=${c.busy} ?disabled=${c.disabled}
      attachments=${att}
      upload=${v && talk ? { method: 'PUT', path: `${app.base}/runs/${v.run.id}/upload?name={name}` } : nothing}
      @input=${(e) => { ui.draft = e.value; }}
      @send=${(e) => app.send(e.value, () => { ui.draft = ''; })}
      @stop=${stop}
      @uploaded=${uploaded}
      @remove=${(e) => app.attach.remove(+e.id)}>
    <button icon=${web ? 'globe' : 'lock'} @tap=${() => app.toggleToolset()}>${web ? 'web' : 'internal'}</button>
    ${t && t.retry ? html`<button icon="refresh" role="primary" @tap=${guard(() => app.actions.control(v.run.id, 'resume'))}>Retry</button>` : nothing}
    ${repeat(queued, (q) => q.id, (q) => html`<button icon="xmark"
      @tap=${guard(() => app.session.removeQueued(q.id))}>${'queued: ' + clip(q.text || '(files)', 40)}</button>`)}
  </composer>`;
}

// Stop interrupts the run; what was still queued comes back into the composer.
const stop = guard(async () => {
  const text = await ctx.app.stop();
  if (text) ui.draft = [text, ui.draft].filter(Boolean).join('\n\n');
});

// uploaded: the app put a picked file into the run (PUT /runs/{id}/upload)
// and hands over the backend's answer; the next Send names it.
function uploaded(e) {
  const r = e.response || {};
  if (r.path) {
    ctx.app.attach.uploaded({ name: e.name, size: r.bytes || 0, type: r.mime || '', path: r.path });
    return;
  }
  // Refused over the 16 MiB cap (413): a chip marked like the web's, which
  // holds Send until it is removed. Any other failure is just said.
  if (!/413|too large|over 16/i.test(JSON.stringify(r))) { fail(`${e.name}: ${r.error || 'upload failed'}`); return; }
  const a = ctx.app.attach;
  a.items.push({ key: ++a.seq, name: e.name, size: MAX_ATTACH + 1, type: '', state: 'bad', err: `too large (max ${fmtBytes(MAX_ATTACH)})` });
  a.changed();
}
