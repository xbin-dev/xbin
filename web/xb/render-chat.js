/**
 * xb/render-chat.js — the chat family of the reference renderer: transcript,
 * message, thinking, toolcard, approval, question, plan, diff, activity,
 * step, composer. The app draws these with the same components as its own
 * agent screen; here they follow a phone chat layout — assistant text full
 * width, user turns as bubbles, tool calls as foldable cards, the composer
 * docked at the bottom of its screen.
 *
 * Renderer-owned state (a question's draft answers, an approval's feedback,
 * whether a transcript is scrolled to the bottom) lives in cx.ui(k); the
 * tile only hears the events it listens to.
 */
import { css, live } from '/vendor/lit-all.min.js';
import { html, nothing, own, P, cls, tone, icon, spinner, str } from '/vendor/xb/render-base.js';
import { mdBlocks, tokensOf } from '/vendor/xb/render-markdown.js';

const fmtTime = (t) => {
  if (typeof t === 'number' && Number.isFinite(t)) return new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  return str(t);
};
const fmtSecs = (s) => { s = Math.max(0, Math.round(Number(s) || 0)); return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`; };
const link = (n, cx) => (href) => { if (cx.on(n, 'link')) cx.emit(n, 'link', { href }); };

function transcript(n, cx) {
  const p = P(n);
  const u = cx.ui(n.k);
  const nested = cx.place === 'card';
  const onScroll = (e) => {
    const el = e.target;
    const at = el.scrollHeight - el.scrollTop - el.clientHeight < 32;
    if (at !== (u.atBottom ?? true)) { u.atBottom = at; if (cx.on(n, 'scrolled')) cx.emit(n, 'scrolled', { atBottom: at }); }
  };
  return html`<xb-transcript data-k=${n.k} class=${cls('transcript', p.follow && 'follow', nested && 'nested', cx.place === 'group' && 'cell')} @scroll=${onScroll}>
    ${p.older && cx.on(n, 'more') ? html`<xb-more class="older" @more=${() => cx.emit(n, 'more', {})}>${spinner()}</xb-more>` : nothing}
    ${cx.kids(n, 'chat')}
  </xb-transcript>`;
}

function message(n, cx) {
  const p = P(n);
  const role = ['user', 'assistant', 'system'].includes(p.role) ? p.role : 'assistant';
  const acts = (n.c || []).filter((c) => c.t === 'actions');
  const tap = cx.on(n, 'tap') ? () => cx.emit(n, 'tap', {}) : null;
  const body = p.markdown
    ? html`<div class=${cls('md', p.streaming && 'streaming')}>${mdBlocks(tokensOf(p, 'text'), link(n, cx))}</div>`
    : html`<div class=${cls('m-text', p.streaming && 'streaming')}>${str(p.text)}</div>`;
  const files = Array.isArray(p.files) && p.files.length ? html`<div class="m-files">${p.files.map((f) => html`<span class="m-file">${
    icon(/^image\//.test(str(f?.mime)) ? 'photo' : 'paperclip')}<span>${str(f?.name)}</span></span>`)}</div>` : nothing;
  const meta = p.sender || p.time != null ? html`<div class="m-meta">${p.sender ? html`<span class="m-sender">${p.sender}</span>` : nothing}${
    p.time != null ? html`<span>${fmtTime(p.time)}</span>` : nothing}</div>` : nothing;
  return html`<xb-message data-k=${n.k} class=${cls('msg', `m-${role}`, p.queued && 'queued', tap && 'tap')}>
    ${meta}
    <div class="m-bubble" @click=${tap}>${str(p.text) || !files ? body : nothing}${files}</div>
    ${p.queued ? html`<div class="m-q">${icon('clock')}queued</div>` : nothing}
    ${acts.map((a) => cx.in('actions').node(a))}
  </xb-message>`;
}

function thinking(n, cx) {
  const p = P(n);
  const open = !!cx.val(n, 'open', false);
  const label = p.live ? 'Thinking…' : p.seconds != null ? `Thought for ${fmtSecs(p.seconds)}` : 'Thought';
  return html`<xb-thinking data-k=${n.k} class=${cls('think', p.live && 'live', open && 'open')}>
    <button class="th-head" aria-expanded=${open ? 'true' : 'false'} @click=${() => cx.emit(n, 'toggle', { open: !open })}>
      <span class="th-label">${label}</span>${icon('forward', 'th-chev')}</button>
    ${open && p.text ? html`<div class="th-body">${p.text}</div>` : nothing}
  </xb-thinking>`;
}

const STATE = {
  writing: () => spinner('st'), running: () => spinner('st'),
  ok: () => html`<span class="st st-ok">${icon('check')}</span>`,
  error: () => html`<span class="st st-err">${icon('xmark')}</span>`,
  canceled: () => html`<span class="st st-can">${icon('ui-ban')}</span>`,
};

function toolcard(n, cx) {
  const p = P(n);
  const has = (n.c || []).length > 0;
  const open = has && !!cx.val(n, 'open', false);
  const st = STATE[p.state];
  const chips = Array.isArray(p.chips) ? p.chips.filter((c) => c && c.text != null) : [];
  const toggle = () => { if (has) cx.emit(n, 'toggle', { open: !open }); };
  return html`<xb-toolcard data-k=${n.k} class=${cls('card', 'tool', `s-${p.state || 'none'}`, open && 'open')}>
    <div class="tc-head">
      <button class="tc-main" aria-expanded=${has ? (open ? 'true' : 'false') : nothing} @click=${toggle}>
        <span class="tc-ic">${icon(p.icon || 'wrench')}</span>
        <span class="tc-text"><span class="tc-title">${str(p.title)}</span>
          ${chips.length ? html`<span class="tc-chips">${chips.map((c) => html`<span class=${cls('chip', c.tone && `pill-${c.tone}`)}>${str(c.text)}</span>`)}</span>` : nothing}</span>
        ${st ? st() : nothing}
        ${has ? icon('forward', 'tc-chev') : nothing}
      </button>
      ${cx.on(n, 'open') ? html`<button class="tc-open" aria-label="Open" @click=${() => cx.emit(n, 'open', {})}>${icon('ui-expand-full')}</button>` : nothing}
    </div>
    ${open ? html`<div class="tc-body">${cx.kids(n, 'card')}</div>` : nothing}
  </xb-toolcard>`;
}

const optRole = (o, i, opts) => {
  const k = str(o.kind).toLowerCase();
  if (/reject|deny|cancel/.test(k)) return 'r-destructive';
  const firstAllow = opts.findIndex((x) => !/reject|deny|cancel|always/.test(str(x.kind).toLowerCase()));
  return i === (firstAllow < 0 ? 0 : firstAllow) ? 'r-primary' : 'r-default';
};

function approval(n, cx) {
  const p = P(n);
  const opts = (Array.isArray(p.options) ? p.options : []).filter((o) => o && typeof o === 'object');
  const s = p.settled && typeof p.settled === 'object' ? p.settled : null;
  const u = cx.ui(n.k);
  const choose = (o) => cx.emit(n, 'choose', { id: str(o.id), feedback: p.feedback ? str(u.fb) : '' });
  const picked = s ? opts.find((o) => str(o.id) === str(s.id)) : null;
  return html`<xb-approval data-k=${n.k} class=${cls('card', 'appr', s && 'settled')}>
    <div class="ap-head">${icon(s ? 'ui-circle-check' : 'shield', s ? 'ap-ic ok' : 'ap-ic')}<span class="ap-title">${str(p.title) || 'Permission needed'}</span></div>
    ${p.text ? html`<div class="ap-text">${p.text}</div>` : nothing}
    ${p.note ? html`<div class="ap-note">${p.note}</div>` : nothing}
    ${s ? html`<div class="ap-settled">${picked ? str(picked.label) : str(s.id)}${s.by ? html` · <span>answered by ${s.by}</span>` : nothing}</div>`
      : html`${p.feedback ? html`<textarea class="ap-fb" rows="2" placeholder="Feedback (optional)" .value=${live(str(u.fb))}
          @input=${(e) => { u.fb = e.target.value; }}></textarea>` : nothing}
        <div class="ap-opts">${opts.map((o, i) => html`<button class=${cls('btn', 'b-opt', optRole(o, i, opts))} @click=${() => choose(o)}>${str(o.label ?? o.id)}</button>`)}</div>`}
  </xb-approval>`;
}

// question: a flat JSON Schema form (string/number/integer/boolean, enums).
function qFields(schema) {
  const props = schema && typeof schema.properties === 'object' ? schema.properties : {};
  const req = new Set(Array.isArray(schema?.required) ? schema.required : []);
  return Object.entries(props).map(([name, s]) => {
    s = s && typeof s === 'object' ? s : {};
    const oneOf = Array.isArray(s.oneOf) ? s.oneOf.filter((o) => o && 'const' in o) : null;
    const choices = Array.isArray(s.enum) ? s.enum.map((v, i) => ({ v, l: str(s.enumNames?.[i] ?? v) }))
      : oneOf?.length ? oneOf.map((o) => ({ v: o.const, l: str(o.title ?? o.const) })) : null;
    return { name, s, req: req.has(name), choices, type: s.type === 'integer' ? 'number' : s.type || 'string' };
  });
}

function question(n, cx) {
  const p = P(n);
  const fields = qFields(p.schema);
  const u = cx.ui(n.k);
  if (!u.v) { u.v = {}; for (const f of fields) if (f.s.default !== undefined) u.v[f.name] = f.s.default; }
  const set = (k, v) => { u.v[k] = v; u.err = ''; cx.update(); };
  const settled = p.settled != null && p.settled !== false;
  const submit = () => {
    const miss = fields.filter((f) => f.req && (u.v[f.name] === undefined || u.v[f.name] === ''));
    if (miss.length) { u.err = `Required: ${miss.map((f) => f.s.title || f.name).join(', ')}`; cx.update(); return; }
    const content = {};
    for (const f of fields) {
      let v = u.v[f.name];
      if (v === undefined || v === '') continue;
      if (f.type === 'number') v = Number(v);
      content[f.name] = v;
    }
    cx.emit(n, 'submit', { content });
  };
  const field = (f) => {
    const label = html`<span class="q-label">${str(f.s.title || f.name)}${f.req ? html`<span class="q-req">*</span>` : nothing}</span>`;
    const v = u.v[f.name];
    let input;
    if (f.type === 'boolean') {
      input = html`<button role="switch" aria-checked=${v ? 'true' : 'false'} class=${cls('switch', v && 'on')} ?disabled=${settled}
        @click=${() => set(f.name, !v)}><span class="knob"></span></button>`;
      return html`<div class="q-row q-bool">${label}${input}</div>`;
    }
    if (f.choices) {
      const i = f.choices.findIndex((c) => c.v === v);
      input = html`<select class="q-input" ?disabled=${settled} @change=${(e) => set(f.name, f.choices[Number(e.target.value)]?.v)}>
        ${i < 0 ? html`<option value="-1" selected hidden>Choose…</option>` : nothing}
        ${f.choices.map((c, j) => html`<option value=${j} ?selected=${j === i}>${c.l}</option>`)}</select>`;
    } else {
      const type = f.type === 'number' ? 'number' : { email: 'email', uri: 'url', date: 'date', 'date-time': 'datetime-local' }[f.s.format] || 'text';
      input = html`<input class="q-input" type=${type} ?disabled=${settled} .value=${str(v)} @input=${(e) => set(f.name, e.target.value)}>`;
    }
    return html`<label class="q-row">${label}${input}${f.s.description ? html`<span class="q-desc">${f.s.description}</span>` : nothing}</label>`;
  };
  return html`<xb-question data-k=${n.k} class=${cls('card', 'quest', settled && 'settled')}>
    <div class="ap-head">${icon(settled ? 'ui-circle-check' : 'question', settled ? 'ap-ic ok' : 'ap-ic')}<span class="ap-title">${str(p.title) || 'Question'}</span></div>
    ${p.schema?.description ? html`<div class="ap-text">${p.schema.description}</div>` : nothing}
    <div class="q-fields">${fields.map(field)}</div>
    ${u.err ? html`<div class="q-err">${u.err}</div>` : nothing}
    ${settled ? html`<div class="ap-settled">Answered</div>` : html`<div class="ap-opts row">
      ${cx.on(n, 'skip') ? html`<button class="btn b-opt r-default" @click=${() => cx.emit(n, 'skip', {})}>Skip</button>` : nothing}
      <button class="btn b-opt r-primary" @click=${submit}>Submit</button></div>`}
  </xb-question>`;
}

const PLAN_IC = { completed: ['ui-circle-check', 'ok'], in_progress: ['ui-circle-dot', 'accent'], pending: ['ui-circle', 'muted'] };
function plan(n) {
  const es = (Array.isArray(P(n).entries) ? P(n).entries : []).filter((e) => e && typeof e === 'object');
  const done = es.filter((e) => e.status === 'completed').length;
  return html`<xb-plan data-k=${n.k} class="card plan">
    <div class="pl-head"><span>Plan</span><span class="pl-count">${done} of ${es.length} done</span></div>
    ${es.map((e) => { const [ic, t] = PLAN_IC[e.status] || PLAN_IC.pending; return html`<div class=${cls('pl-row', `pl-${e.status || 'pending'}`)}>
      <span class=${cls('pl-ic', tone(t))}>${icon(ic)}</span><span class="pl-text">${str(e.text)}</span></div>`; })}
  </xb-plan>`;
}

const DIFF_ST = { added: 'A', add: 'A', a: 'A', new: 'A', deleted: 'D', delete: 'D', removed: 'D', d: 'D', renamed: 'R', r: 'R', modified: 'M', m: 'M' };
function diff(n, cx) {
  const p = P(n);
  const files = (Array.isArray(p.files) ? p.files : []).filter((f) => f && typeof f === 'object');
  const tap = cx.on(n, 'open-file');
  const lines = p.patch ? str(p.patch).replace(/\n$/, '').split('\n') : [];
  const lc = (l) => (l.startsWith('+++') || l.startsWith('---') || l.startsWith('diff ') || l.startsWith('index ') ? 'dl-file'
    : l.startsWith('@@') ? 'dl-hunk' : l.startsWith('+') ? 'dl-add' : l.startsWith('-') ? 'dl-del' : '');
  return html`<xb-diff data-k=${n.k} class="card diff">
    ${files.length ? html`<div class="df-files">${files.map((f) => { const s = DIFF_ST[str(f.status).toLowerCase()] || (str(f.status).slice(0, 1).toUpperCase() || 'M'); return html`
      <button class=${cls('df-row', tap && 'tap')} ?disabled=${!tap} @click=${() => cx.emit(n, 'open-file', { path: str(f.path) })}>
        <span class=${`df-st st-${s}`}>${s}</span><span class="df-path"><span dir="ltr">${str(f.path)}</span></span>
        ${Number(f.add) ? html`<span class="df-add">+${f.add}</span>` : nothing}${Number(f.del) ? html`<span class="df-del">−${f.del}</span>` : nothing}
      </button>`; })}</div>` : nothing}
    ${lines.length ? html`<pre class="df-patch">${lines.map((l) => html`<span class=${cls('dl', lc(l))}>${l || ' '}</span>`)}</pre>` : nothing}
  </xb-diff>`;
}

const activity = (n) => html`<xb-activity data-k=${n.k} class=${cls('activity', P(n).live && 'live')}>
  ${P(n).live ? spinner() : nothing}<span class="ac-text">${str(P(n).text)}</span></xb-activity>`;

const step = (n) => html`<xb-step data-k=${n.k} class=${cls('step', P(n).tone && `s-${P(n).tone}`)}>
  <span class=${cls('sp-glyph', tone(P(n).tone))}>${str(P(n).glyph) || '•'}</span><span class="sp-text">${str(P(n).text)}</span></xb-step>`;

function composer(n, cx) {
  const p = P(n);
  const u = cx.ui(n.k);
  const v = str(cx.val(n, 'value', ''));
  const dis = !!p.disabled;
  const bound = own(n.p, 'value');
  const send = () => {
    const now = str(cx.val(n, 'value', ''));
    if (dis || !now.trim()) return;
    cx.emit(n, 'send', { value: now });
    if (!bound) { u.value = ''; cx.update(); }
  };
  const key = (e) => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); send(); } };
  const slash = Array.isArray(p.slash) && /^\/\S*$/.test(v)
    ? p.slash.filter((s) => s && str(s.name).startsWith(v.slice(1))).slice(0, 6) : [];
  const atts = (Array.isArray(p.attachments) ? p.attachments : []).filter((a) => a && typeof a === 'object');
  const chips = n.c || [];
  const pick = (e) => {
    const f = e.target.files?.[0]; e.target.value = '';
    if (f && typeof cx.v.onupload === 'function') cx.v.onupload(n, f);
  };
  return html`<xb-composer data-k=${n.k} class=${cls('composer', dis && 'disabled')}>
    ${slash.length ? html`<div class="cm-slash">${slash.map((s) => html`<button class="cm-cmd" @click=${() => cx.emit(n, 'input', { value: `/${s.name} ` })}>
      <span class="cm-name">/${s.name}${s.hint ? html` <span class="cm-hint">${s.hint}</span>` : nothing}</span>
      ${s.description ? html`<span class="cm-desc">${s.description}</span>` : nothing}</button>`)}</div>` : nothing}
    ${chips.length ? html`<div class="cm-chips">${cx.kids(n, 'chips')}</div>` : nothing}
    ${atts.length ? html`<div class="cm-atts">${atts.map((a) => html`<span class="cm-att">${icon(/^image\//.test(str(a.mime)) ? 'photo' : 'file')}
      <span class="cm-an">${str(a.name)}</span>${typeof a.progress === 'number' && a.progress < 1 ? html`<span class="cm-ap">${Math.round(a.progress * 100)}%</span>` : nothing}
      ${cx.on(n, 'remove') ? html`<button class="cm-ax" aria-label="Remove" @click=${() => cx.emit(n, 'remove', { id: str(a.id) })}>${icon('xmark')}</button>` : nothing}</span>`)}</div>` : nothing}
    <div class="cm-row">
      ${p.upload ? html`<label class="cm-attach" aria-label="Attach">${icon('plus')}<input type="file" accept=${str(p.accept) || nothing} ?disabled=${dis} @change=${pick}></label>` : nothing}
      <div class="cm-box"><textarea rows="1" placeholder=${str(p.placeholder) || 'Message'} ?disabled=${dis} .value=${live(v)}
        @input=${(e) => cx.emit(n, 'input', { value: e.target.value })} @keydown=${key}></textarea></div>
      ${p.busy ? html`<button class="cm-send stop" aria-label="Stop" @click=${() => cx.emit(n, 'stop', {})}>${icon('ui-square')}</button>`
        : html`<button class="cm-send" aria-label="Send" ?disabled=${dis || !v.trim()} @click=${send}>${icon('ui-arrow-up')}</button>`}
    </div>
  </xb-composer>`;
}

export const CHAT = { transcript, message, thinking, toolcard, approval, question, plan, diff, activity, step, composer };

export const CHAT_CSS = css`
  xb-transcript > * { flex-shrink: 0; }
  xb-transcript { display: flex; flex-direction: column; gap: 14px; overflow-y: auto; padding: 12px var(--xb-margin) 20px; overscroll-behavior: contain; }
  xb-transcript.nested, xb-transcript.cell { overflow: visible; padding: 4px 0; gap: 10px; }
  xb-transcript > .older { display: flex; justify-content: center; padding: 4px; }

  xb-message { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
  .m-meta { display: flex; gap: 8px; font: var(--xb-font-caption); color: var(--xb-muted); }
  .m-sender { font-weight: 600; }
  .m-text { white-space: pre-wrap; overflow-wrap: anywhere; }
  .m-user { align-items: flex-end; }
  .m-user .m-bubble { max-width: 82%; padding: 9px 14px; border-radius: 20px 20px 6px 20px;
    background: var(--xb-bubble); }
  .m-user .m-meta { padding: 0 6px; }
  .m-assistant .m-bubble { max-width: 100%; }
  .m-system { align-items: center; text-align: center; }
  .m-system .m-bubble { font: var(--xb-font-footnote); color: var(--xb-muted); max-width: 90%; }
  .msg.queued .m-bubble { opacity: 0.55; }
  .m-q { display: flex; align-items: center; gap: 4px; font: var(--xb-font-caption); color: var(--xb-muted); }
  .m-q .ic { width: 12px; height: 12px; }
  .msg.tap .m-bubble { cursor: pointer; }
  .m-files { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 6px; }
  .m-file { display: inline-flex; align-items: center; gap: 6px; padding: 5px 10px; border-radius: 10px; background: var(--xb-surface2); font: var(--xb-font-footnote); max-width: 100%; }
  .m-file .ic { width: 16px; height: 16px; color: var(--xb-muted); }
  .m-text.streaming::after { content: ''; display: inline-block; width: 8px; height: 1em; margin-left: 2px; vertical-align: -2px; border-radius: 2px; background: var(--xb-accent); animation: xb-blink 1s steps(2) infinite; }
  xb-message > xb-actions { padding: 2px 0 0; }
  .m-user > xb-actions { justify-content: flex-end; }

  xb-thinking { display: block; }
  .th-head { display: inline-flex; align-items: center; gap: 4px; font: var(--xb-font-subheadline); color: var(--xb-muted); padding: 2px 0; }
  .th-chev { width: 14px; height: 14px; stroke-width: 2.6; transition: transform 0.15s; }
  .think.open .th-chev { transform: rotate(90deg); }
  .think.live .th-label { background: linear-gradient(90deg, var(--xb-muted) 30%, var(--xb-text) 50%, var(--xb-muted) 70%) 0 0 / 200% 100%;
    -webkit-background-clip: text; background-clip: text; color: transparent; animation: xb-shimmer 1.6s linear infinite; }
  @keyframes xb-shimmer { from { background-position: 100% 0; } to { background-position: -100% 0; } }
  .th-body { margin-top: 6px; padding-left: 12px; border-left: 2px solid var(--xb-border); font: var(--xb-font-subheadline); color: var(--xb-muted); white-space: pre-wrap; overflow-wrap: anywhere; }

  .card { display: block; background: var(--xb-surface); border-radius: 14px; box-shadow: 0 0 0 0.5px var(--xb-separator); min-width: 0; }
  .tc-head { display: flex; align-items: stretch; }
  .tc-main { flex: 1; min-width: 0; display: flex; align-items: center; gap: 10px; padding: 10px 12px; text-align: left; }
  .tc-ic { display: flex; color: var(--xb-muted); }
  .tc-ic .ic { width: 18px; height: 18px; }
  .tc-text { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
  .tc-title { font: var(--xb-font-subheadline); font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tc-chips { display: flex; flex-wrap: wrap; gap: 4px; }
  .chip { font: var(--xb-font-caption); font-weight: 600; padding: 1px 7px; border-radius: 999px; }
  .chip:not([class*="pill-"]) { background: var(--xb-fill); color: var(--xb-muted); }
  .st { display: flex; }
  .st .ic { width: 16px; height: 16px; stroke-width: 2.6; }
  .st-ok { color: var(--xb-ok); } .st-err { color: var(--xb-danger); } .st-can { color: var(--xb-muted); }
  .spin.st { width: 16px; height: 16px; }
  .tc-chev { width: 14px; height: 14px; color: var(--xb-muted); stroke-width: 2.6; transition: transform 0.15s; }
  .tool.open .tc-chev { transform: rotate(90deg); }
  .tc-open { padding: 0 12px; color: var(--xb-accent-text); display: flex; align-items: center; }
  .tc-open .ic { width: 16px; height: 16px; }
  .tc-body { display: flex; flex-direction: column; gap: 8px; padding: 0 12px 12px; }
  .tool.s-error { box-shadow: 0 0 0 1px color-mix(in srgb, var(--xb-danger) 45%, transparent); }

  .appr, .quest { padding: 14px; display: flex; flex-direction: column; gap: 8px; box-shadow: 0 0 0 1px color-mix(in srgb, var(--xb-accent) 45%, transparent); }
  .appr.settled, .quest.settled { box-shadow: 0 0 0 0.5px var(--xb-separator); }
  .ap-head { display: flex; align-items: center; gap: 8px; }
  .ap-ic { color: var(--xb-accent-text); }
  .ap-ic.ok { color: var(--xb-ok); }
  .ap-title { font: var(--xb-font-headline); min-width: 0; overflow-wrap: anywhere; }
  .ap-text { font: var(--xb-font-subheadline); white-space: pre-wrap; overflow-wrap: anywhere; }
  .ap-note { font: var(--xb-font-footnote); color: var(--xb-muted); }
  .ap-settled { font: var(--xb-font-subheadline); color: var(--xb-muted); }
  .ap-fb { width: 100%; resize: none; border: 0; outline: 0; border-radius: 10px; padding: 9px 11px; background: var(--xb-surface2); font: var(--xb-font-subheadline); color: var(--xb-text); }
  .ap-opts { display: flex; flex-direction: column; gap: 8px; margin-top: 4px; }
  .ap-opts.row { flex-direction: row; justify-content: flex-end; }
  .b-opt { min-height: 44px; padding: 0 16px; border-radius: 12px; font: var(--xb-font-headline); background: var(--xb-fill); color: var(--xb-text); }
  .ap-opts.row .b-opt { min-height: 40px; }
  .b-opt.r-primary { background: var(--xb-accent); color: var(--xb-on-accent); }
  .b-opt.r-destructive { background: color-mix(in srgb, var(--xb-danger) 14%, transparent); color: var(--xb-danger); }
  .q-fields { display: flex; flex-direction: column; gap: 10px; }
  .q-row { display: flex; flex-direction: column; gap: 4px; }
  .q-row.q-bool { flex-direction: row; align-items: center; justify-content: space-between; }
  .q-label { font: var(--xb-font-footnote); color: var(--xb-muted); }
  .q-bool .q-label { font: var(--xb-font-body); color: var(--xb-text); }
  .q-req { color: var(--xb-danger); margin-left: 2px; }
  .q-input { border: 0; outline: 0; border-radius: 10px; padding: 10px 12px; background: var(--xb-surface2); font: var(--xb-font-body); color: var(--xb-text); width: 100%; }
  .q-desc { font: var(--xb-font-caption); color: var(--xb-muted); }
  .q-err { font: var(--xb-font-footnote); color: var(--xb-danger); }

  .plan { padding: 12px 14px; }
  .pl-head { display: flex; justify-content: space-between; font: var(--xb-font-subheadline); font-weight: 600; margin-bottom: 6px; }
  .pl-count { font-weight: 400; color: var(--xb-muted); }
  .pl-row { display: flex; align-items: flex-start; gap: 10px; padding: 4px 0; font: var(--xb-font-subheadline); }
  .pl-ic { display: flex; padding-top: 1px; }
  .pl-ic .ic { width: 18px; height: 18px; }
  .pl-completed .pl-text { color: var(--xb-muted); }
  .pl-in_progress .pl-text { font-weight: 600; }
  .pl-text { overflow-wrap: anywhere; min-width: 0; }

  .diff { overflow: hidden; }
  .df-files { display: flex; flex-direction: column; }
  .df-row { display: flex; align-items: center; gap: 10px; padding: 9px 12px; text-align: left; font: var(--xb-font-footnote); width: 100%; }
  .df-row + .df-row { border-top: 0.5px solid var(--xb-separator); }
  .df-row:disabled { opacity: 1; cursor: default; }
  .df-row.tap:active { background: var(--xb-fill); }
  .df-st { flex: none; width: 18px; height: 18px; border-radius: 5px; display: flex; align-items: center; justify-content: center; font: var(--xb-font-caption2); font-weight: 700; background: var(--xb-fill); color: var(--xb-muted); }
  .st-A { color: var(--xb-ok); background: color-mix(in srgb, var(--xb-ok) 16%, transparent); }
  .st-D { color: var(--xb-danger); background: color-mix(in srgb, var(--xb-danger) 16%, transparent); }
  .st-M { color: var(--xb-accent-text); background: color-mix(in srgb, var(--xb-accent) 18%, transparent); }
  .df-path { flex: 1; min-width: 0; font-family: var(--xb-mono); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; direction: rtl; text-align: left; }
  .df-add { color: var(--xb-ok); font-variant-numeric: tabular-nums; }
  .df-del { color: var(--xb-danger); font-variant-numeric: tabular-nums; }
  .df-patch { margin: 0; padding: 6px 0; overflow-x: auto; font-family: var(--xb-mono); font-size: calc(var(--xb-size-footnote) * 0.95); line-height: 1.5; border-top: 0.5px solid var(--xb-separator); }
  .df-files:empty + .df-patch { border-top: 0; }
  .dl { display: block; padding: 0 12px; white-space: pre; min-width: max-content; }
  .dl-add { background: color-mix(in srgb, var(--xb-ok) 14%, transparent); }
  .dl-del { background: color-mix(in srgb, var(--xb-danger) 14%, transparent); }
  .dl-hunk { color: var(--xb-muted); background: var(--xb-fill); }
  .dl-file { color: var(--xb-muted); font-weight: 600; }

  xb-activity { display: flex; align-items: center; gap: 8px; font: var(--xb-font-footnote); color: var(--xb-muted); }
  xb-activity .spin { width: 14px; height: 14px; }
  xb-step { display: flex; align-items: flex-start; gap: 8px; font: var(--xb-font-footnote); color: var(--xb-muted); }
  .sp-glyph { flex: none; min-width: 20px; height: 20px; padding: 0 5px; border-radius: 10px; display: flex; align-items: center; justify-content: center;
    font: var(--xb-font-caption); font-weight: 700; background: var(--xb-fill); }
  .sp-glyph.tone-warn { background: color-mix(in srgb, var(--xb-warn) 18%, transparent); }
  .sp-glyph.tone-danger { background: color-mix(in srgb, var(--xb-danger) 16%, transparent); }
  .sp-glyph.tone-ok { background: color-mix(in srgb, var(--xb-ok) 16%, transparent); }
  .sp-glyph.tone-accent { background: color-mix(in srgb, var(--xb-accent) 18%, transparent); }
  .sp-text { padding-top: 1px; overflow-wrap: anywhere; min-width: 0; }

  xb-composer { display: flex; flex-direction: column; gap: 8px; flex: none; padding: 8px 12px 24px; background: color-mix(in srgb, var(--xb-bg) 92%, transparent);
    border-top: 0.5px solid var(--xb-separator); backdrop-filter: blur(18px); }
  .cm-row { display: flex; align-items: flex-end; gap: 8px; }
  .cm-box { flex: 1; min-width: 0; display: flex; background: var(--xb-surface); border-radius: 20px; box-shadow: inset 0 0 0 0.5px var(--xb-border); padding: 7px 14px; }
  .cm-box textarea { flex: 1; min-width: 0; border: 0; outline: 0; background: none; resize: none; font: var(--xb-font-body); color: var(--xb-text);
    field-sizing: content; min-height: 22px; max-height: 132px; padding: 0; }
  .cm-box textarea::placeholder { color: color-mix(in srgb, var(--xb-muted) 75%, transparent); }
  .cm-send, .cm-attach { flex: none; width: 36px; height: 36px; border-radius: 18px; display: flex; align-items: center; justify-content: center; }
  .cm-send { background: var(--xb-accent); color: var(--xb-on-accent); }
  .cm-send .ic { stroke-width: 2.6; }
  .cm-send:disabled { background: var(--xb-fill); color: var(--xb-muted); opacity: 1; }
  .cm-send.stop { background: var(--xb-text); color: var(--xb-bg); }
  .cm-send.stop .ic { width: 14px; height: 14px; fill: currentColor; }
  .cm-attach { background: var(--xb-fill); color: var(--xb-text); cursor: pointer; position: relative; overflow: hidden; }
  .cm-attach input { position: absolute; inset: 0; opacity: 0; cursor: pointer; }
  .cm-chips, .cm-atts { display: flex; gap: 6px; overflow-x: auto; }
  .cm-chips > xb-button { flex: none; }
  .cm-att { display: inline-flex; align-items: center; gap: 6px; padding: 4px 6px 4px 10px; border-radius: 10px; background: var(--xb-surface); box-shadow: inset 0 0 0 0.5px var(--xb-border); font: var(--xb-font-footnote); flex: none; }
  .cm-att .ic { width: 16px; height: 16px; color: var(--xb-muted); }
  .cm-an { max-width: 140px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .cm-ap { color: var(--xb-muted); font-variant-numeric: tabular-nums; }
  .cm-ax { display: flex; color: var(--xb-muted); padding: 2px; }
  .cm-ax .ic { width: 14px; height: 14px; }
  .cm-slash { display: flex; flex-direction: column; background: var(--xb-surface); border-radius: 12px; box-shadow: var(--xb-shadow); overflow: hidden; }
  .cm-cmd { display: flex; flex-direction: column; align-items: flex-start; gap: 1px; padding: 8px 12px; text-align: left; }
  .cm-cmd + .cm-cmd { border-top: 0.5px solid var(--xb-separator); }
  .cm-name { font: var(--xb-font-subheadline); font-weight: 600; font-family: var(--xb-mono); }
  .cm-hint { font-weight: 400; color: var(--xb-muted); }
  .cm-desc { font: var(--xb-font-caption); color: var(--xb-muted); }
`;
