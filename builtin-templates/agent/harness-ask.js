// harness-ask.js — a coding harness asking, on the web (D147 §4.2.5,
// §4.2.9, §4.3.4): the card at the end of the chat for its permission
// request (the harness's own options as buttons, reject first when it
// defaults to no; an option that raises it to a bypass mode only for the
// owner, marked ⚠ and confirmed; the call, its diff preview, what "always"
// remembers; an optional word with a rejection), its plan approval (the plan,
// its options, a "keep planning" box sent as feedback) and its question (a
// form from the schema, Submit / Skip; url mode: the page, then Done).
//
// The `end` seam (web-ext.js): it answers only for a harness run parked on a
// permission or a question — the login park is the sign-in module's, and
// anything else is left to the built-in cards. Verdicts go through
// app.harness (model/harness-store.js); a refused one (the ask is gone: 409)
// is said under the card by the built-in note. The words are
// model/harness-ask.js's.
import { html, nothing, unsafeHTML } from '/vendor/lit-all.min.js';
import { ext, ctx } from './web-ext.js';
import { md } from './chat-md.js';
import { isHarness, nameOf } from './model/harness.js';
import { permission, question, formContent, missingRequired, ownerOf, optionConfirm } from './model/harness-ask.js';
import { access } from './model/rules.js';

// per park: what is typed into its card — {vals, fb, err, busy}
const forms = new Map();
const form = (park) => {
  let f = forms.get(park);
  if (!f) {
    if (forms.size > 20) forms.delete(forms.keys().next().value);
    forms.set(park, (f = { vals: {}, fb: '', err: '', busy: false }));
  }
  return f;
};
const repaint = () => ctx.paint();

// the call's tool row (its acp carries the backend's patches), searched in the held blocks
function acpOf(blocks, callId) {
  for (const b of blocks || []) {
    if (b.k === 'tool' && b.id === 'c' + callId) return b.acp || null;
    if (b.kids) { const a = acpOf(b.kids, callId); if (a) return a; }
  }
  return null;
}

ext.register({
  end(s) {
    const r = s.run || {};
    const ps = r.pendingState || {};
    if (!isHarness(r) || r.status !== 'waiting_input' || !ps.harness) return null;
    const v = ctx.app && ctx.app.session.current();
    const who = { owner: ownerOf(v, ctx.app && ctx.app.me), talk: access(v).talk, name: nameOf(r.harness) };
    if (ps.kind === 'approval') return permissionTpl(r, permission(ps, { ...who, acp: acpOf(s.blocks, ps.harness.callId) }), who);
    if (ps.kind === 'question') return questionTpl(r, question(ps), who);
    return null;
  },
});

// --- a permission request, a plan approval -------------------------------------------------

async function choose(r, p, o, who) {
  const f = form(p.park);
  if (o.explicit && !confirm(optionConfirm(who.name, o))) return;
  f.busy = true; repaint();
  const fb = o.reject ? f.fb.trim() : '';
  const ok = await ctx.app.harness.permit(r.id, { option: o.id, park: p.park, ...(fb ? { feedback: fb } : {}) });
  f.busy = false;
  if (ok) forms.delete(p.park);
  repaint();
}

function previewTpl(p) {
  if (!p.preview) return nothing;
  return html`<div class="hprev">${p.preview.map((d) => (d.text != null ? html`<pre class="hout">${d.text}</pre>` : html`
    <div class="hdiff"><div class="hdh mono">${d.path} <span class="hadd">+${d.add}</span> <span class="hdel">−${d.del}</span></div>
      <pre>${d.lines.map((l) => html`<span class=${l.t === '+' ? 'hadd' : l.t === '-' ? 'hdel' : l.t === '@' ? 'hhunk' : ''}>${l.t === '@' ? l.text : l.t + l.text}\n</span>`)}${d.more ? html`<span class="muted">… ${d.more} more lines</span>` : nothing}</pre></div>`))}</div>`;
}

function permissionTpl(r, p, who) {
  if (!p) return null;
  const f = form(p.park);
  const fbTpl = p.plan
    ? html`<textarea class="hfb" rows="2" placeholder=${`Keep planning — tell ${who.name} what should change`} .value=${f.fb}
        @input=${(e) => { f.fb = e.target.value; }}></textarea>`
    : p.reject ? html`<input class="hfb" placeholder=${`optional — tell ${who.name} why (sent with a rejection)`} .value=${f.fb}
        @input=${(e) => { f.fb = e.target.value; }}>` : nothing;
  return html`<div class="ask hask ${p.plan ? 'hplan' : ''}" data-park=${p.park}>
    <div class="hlead"><b>${p.lead}</b>${p.title ? html` · <span class="htitle">${p.title}</span>` : nothing}</div>
    ${p.label ? html`<div class="hlabel">${p.label}</div>` : nothing}
    ${p.command ? html`<pre class="hcmd">$ ${p.command}</pre>` : nothing}
    ${p.raw ? html`<pre class="hcmd">${p.raw}</pre>` : nothing}
    ${p.plan && p.planText ? html`<div class="md hplanmd">${unsafeHTML(md(p.planText))}</div>` : nothing}
    ${previewTpl(p)}
    ${p.description ? html`<div class="muted small">${p.description}</div>` : nothing}
    ${p.talk ? html`${fbTpl}<div class="hopts">${p.options.map((o) => html`<button class="btn btnsm ${o.reject ? 'ghost' : ''} ${o.explicit ? 'hwarn' : ''}"
        data-opt=${o.id} data-kind=${o.kind} title=${o.title || nothing} ?disabled=${f.busy} @click=${() => choose(r, p, o, who)}>${o.explicit ? '⚠ ' : ''}${o.name}</button>`)}</div>
      ${p.rule ? html`<div class="muted small hrule">${p.rule}</div>` : nothing}
      ${p.hidden ? html`<div class="muted small">${p.hidden === 1 ? 'One option' : `${p.hidden} options`} that would stop ${who.name} asking — only the owner may pick them.</div>` : nothing}`
    : html`<div class="muted small">view only — someone who may write here answers it</div>`}
  </div>`;
}

// --- a question ----------------------------------------------------------------------------

async function answer(r, q, action, content) {
  const f = form(q.park);
  f.busy = true; f.err = ''; repaint();
  const ok = await ctx.app.harness.answer(r.id, action, content, q.park);
  f.busy = false;
  if (ok) forms.delete(q.park);
  repaint();
}

function submit(r, q) {
  const f = form(q.park);
  const content = formContent(q.fields, f.vals);
  const miss = missingRequired(q.fields, content);
  if (miss.length) { f.err = `Answer ${miss.join(', ')} first.`; repaint(); return; }
  answer(r, q, 'accept', content);
}

function fieldTpl(f, x) {
  const set = (k, val) => { f.vals = { ...f.vals, [k]: val }; };
  const cur = f.vals[x.key] ?? x.dflt;
  const head = html`<div class="hfl">${x.title || x.key}${x.required ? html`<span class="hreq">*</span>` : nothing}
    ${x.description ? html`<span class="muted small"> — ${x.description}</span>` : nothing}</div>`;
  const other = x.other ? html`<input class="hother" placeholder=${x.otherHint || 'Other…'} .value=${f.vals[x.other] || ''}
    @input=${(e) => set(x.other, e.target.value)}>` : nothing;
  switch (x.kind) {
    case 'radio': return html`<div class="hfield" data-field=${x.key}>${head}${x.options.map((o, i) => html`<label class="hopt">
      <input type="radio" name=${'hq-' + x.key} .checked=${cur !== undefined && String(cur) === String(o.value)} @change=${() => set(x.key, o.value)} data-i=${i}>
      ${o.title}${o.description ? html`<span class="muted small"> — ${o.description}</span>` : nothing}</label>`)}${other}</div>`;
    case 'check': return html`<div class="hfield" data-field=${x.key}>${head}${x.options.map((o) => html`<label class="hopt">
      <input type="checkbox" .checked=${(cur || []).includes(o.value)}
        @change=${(e) => set(x.key, e.target.checked ? [...(f.vals[x.key] || []), o.value] : (f.vals[x.key] || []).filter((y) => y !== o.value))}>
      ${o.title}${o.description ? html`<span class="muted small"> — ${o.description}</span>` : nothing}</label>`)}${other}</div>`;
    case 'bool': return html`<div class="hfield" data-field=${x.key}><label class="hopt"><input type="checkbox" .checked=${!!cur}
      @change=${(e) => set(x.key, e.target.checked)}> ${x.title || x.key}</label>${x.description ? html`<div class="muted small">${x.description}</div>` : nothing}</div>`;
    case 'number': return html`<div class="hfield" data-field=${x.key}>${head}<input type="number" .value=${cur ?? ''} @input=${(e) => set(x.key, e.target.value)}></div>`;
  }
  return html`<div class="hfield" data-field=${x.key}>${head}<input .value=${cur ?? ''} @input=${(e) => set(x.key, e.target.value)}>${other}</div>`;
}

function questionTpl(r, q, who) {
  if (!q) return null;
  const f = form(q.park);
  const off = !who.talk || f.busy;
  if (q.mode === 'url') {
    return html`<div class="ask hask hq" data-park=${q.park}>
      <div class="hlead"><b>${who.name} asks you to open a page</b></div>
      ${q.message ? html`<div class="hmsg">${q.message}</div>` : nothing}
      ${q.url ? html`<div><a class="hurl mono" href=${q.url} target="_blank" rel="noopener noreferrer">${q.url}</a></div>`
        : html`<div class="mono hurl">${q.text}</div>`}
      <div class="hopts"><button class="btn btnsm" ?disabled=${off} @click=${() => answer(r, q, 'accept', {})}>Done</button>
        <button class="btn ghost btnsm" ?disabled=${off} @click=${() => answer(r, q, 'decline')}>Cancel</button></div>
    </div>`;
  }
  return html`<div class="ask hask hq" data-park=${q.park}>
    <div class="hlead"><b>${who.name} asks</b></div>
    ${q.message ? html`<div class="hmsg">${q.message}</div>` : nothing}
    ${q.fields.map((x) => fieldTpl(f, x))}
    ${f.err ? html`<div class="err small">${f.err}</div>` : nothing}
    ${who.talk ? html`<div class="hopts"><button class="btn btnsm" ?disabled=${off} @click=${() => submit(r, q)}>Submit</button>
      <button class="btn ghost btnsm" ?disabled=${off} @click=${() => answer(r, q, 'decline')}>Skip</button></div>`
    : html`<div class="muted small">view only — someone who may write here answers it</div>`}
  </div>`;
}

// the cards' look (the tile's own sheet stays as it is)
const style = document.createElement('style');
style.textContent = `
  .hask .hlead { margin-bottom: 4px; }
  .hask .htitle { font-family: var(--bx-mono); font-size: 12px; }
  .hask .hlabel { margin: 2px 0 4px; }
  .hask pre.hcmd, .hask pre.hout { margin: 4px 0; padding: 4px 6px; background: var(--bx-panel-2); border-radius: 4px; white-space: pre-wrap; word-break: break-word; font-size: 12px; max-height: 12em; overflow: auto; }
  .hask .hdiff { margin: 4px 0; border: 1px solid var(--bx-border); border-radius: 4px; overflow: hidden; }
  .hask .hdiff .hdh { font-size: 11.5px; padding: 2px 6px; background: var(--bx-panel-2); }
  .hask .hdiff pre { margin: 0; padding: 4px 6px; font-size: 12px; max-height: 16em; overflow: auto; }
  .hask .hadd { color: var(--bx-green, #4caf50); } .hask .hdel { color: var(--bx-red, #ef5350); } .hask .hhunk { color: var(--bx-muted); }
  .hask .hopts { display: flex; flex-wrap: wrap; gap: 6px; margin: 6px 0 2px; }
  .hask .hopts .btn { margin: 0; }
  .hask .hwarn { border-color: var(--bx-red, #ef5350); color: var(--bx-red, #ef5350); background: none; }
  .hask .hfb { width: 100%; box-sizing: border-box; margin: 4px 0 0; font: inherit; font-size: 12.5px; }
  .hask .hrule { margin-top: 2px; }
  .hask .hplanmd { max-height: 24em; overflow: auto; margin: 4px 0; }
  .hask .hmsg { margin: 2px 0 6px; white-space: pre-wrap; }
  .hask .hfield { margin: 6px 0; }
  .hask .hfl { font-weight: 600; font-size: 12.5px; }
  .hask .hreq { color: var(--bx-red, #ef5350); margin-left: 2px; }
  .hask .hopt { display: block; margin: 2px 0; cursor: pointer; }
  .hask .hfield > input:not([type]), .hask .hfield > input[type=number], .hask .hother { width: 100%; box-sizing: border-box; margin-top: 2px; }
  .hask .hurl { word-break: break-all; }
`;
document.head.append(style);

// A coding agent's card in its parent's chat draws its child's park with
// these (harness-child.js): r is the child's run, so the answer is the child's.
export { permissionTpl, questionTpl };
