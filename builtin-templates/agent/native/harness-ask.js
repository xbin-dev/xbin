// native/harness-ask.js — a coding harness asking and driven, in the native
// view (D-harness §4.2.4, §4.2.5, §4.2.9, §4.2.10, §4.3.4, §4.3.12), the
// web's harness-ask.js and harness-controls.js in the chat family:
//   end       its permission request as an `approval` with the harness's own
//             options (reject first when it defaults to no; an option that
//             raises it to a bypass mode only for the owner, marked ⚠, and
//             confirmed by a second approval), the call and a `diff` preview,
//             the rule as its note, feedback with a rejection; a plan
//             approval with the plan as `markdown` above it; a question as a
//             `question` from the schema (url mode: the page as a link, then
//             Submit when done)
//   toolbar   in a conversation: Mode (a menu: the adapter's modes, a bypass
//             one confirmed and the owner's only, and your Auto / Always
//             approve) and a picker per config option (Model, Effort…); at
//             home: Coding agents → your setting per harness (a screen)
//   composer  the placeholder while a turn runs, the harness's slash
//             commands, Send now (interrupts), a steered message said
// The `end` seam answers only for a harness run parked on a permission or a
// question (the login park is the sign-in module's). Words:
// model/harness-ask.js.
import { html, nothing } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ui, ctx, guard, push, clip, fail } from './ui.js';
import { isHarness, harnessOf, nameOf } from '../model/harness.js';
import {
  permission, question, nativeSchema, nativeContent, patchOf, controls, settingOf, slashCommands,
  steerWords, steerTrack, ownerOf, modeConfirm, optionConfirm,
} from '../model/harness-ask.js';

const confirming = new Map(); // park → the explicit option picked, until confirmed or not
const track = steerTrack();
let noteT = null;

const who = (v) => ({ owner: ownerOf(v, ctx.app.me), talk: ctx.app.rules.access(v).talk, name: nameOf(v.run.harness) });

// the call's tool row (its acp carries the backend's patches), searched in the held blocks
function acpOf(blocks, callId) {
  for (const b of blocks || []) {
    if (b.k === 'tool' && b.id === 'c' + callId) return b.acp || null;
    if (b.kids) { const a = acpOf(b.kids, callId); if (a) return a; }
  }
  return null;
}

// permit/answer never throw: a refusal (the ask is gone: 409) is said as the screen's notice
async function permit(runId, body) {
  const ok = await ctx.app.harness.permit(runId, body);
  if (!ok) fail(ctx.app.session.ui.approveNote(runId) || 'the verdict was not sent');
}
async function answer(runId, action, content, park) {
  const ok = await ctx.app.harness.answer(runId, action, content, park);
  if (!ok) fail(ctx.app.session.ui.approveNote(runId) || 'the answer was not sent');
}

ext.register({
  end(v, s) {
    const r = v.run;
    const ps = r.pendingState || {};
    if (!isHarness(r) || r.status !== 'waiting_input' || !ps.harness) return null;
    const w = who(v);
    if (ps.kind === 'approval') return approvalTpl(r, permission(ps, { ...w, acp: acpOf(s.blocks, ps.harness.callId) }), w);
    if (ps.kind === 'question') return questionTpl(r, question(ps), w);
    return null;
  },

  toolbar(v) {
    const app = ctx.app;
    if (!v) {
      app.harness.ensure();
      return app.harness.catalog.harnesses.some((h) => h.available)
        ? html`<button icon="agent" @tap=${() => push({ kind: 'harness-settings' })}>Coding agents</button>` : null;
    }
    const h = harnessOf(v);
    if (!h) return null;
    app.harness.ensure();
    return controlsTpl(v, h);
  },

  composer(v) {
    const h = v ? harnessOf(v) : null;
    if (!h) return null;
    const app = ctx.app;
    const w = steerWords(v, { native: true });
    const notes = h.steering ? track(v, app.session.shown().blocks, Date.now()) : track(null);
    clearTimeout(noteT);
    if (notes.length) noteT = setTimeout(() => ctx.paint(), Math.max(50, Math.min(...notes.map((n) => n.until)) - Date.now() + 20));
    return {
      ...(w ? { placeholder: w.placeholder } : {}),
      slash: app.rules.access(v).talk ? slashCommands(h) : [],
      tpl: () => html`${w && w.busy ? html`<button icon="bolt" @tap=${sendNow}>Send now (interrupts)</button>` : nothing}
        ${notes.map((n) => html`<button icon="check" @tap=${() => {}}>${'steered: ' + clip(n.text, 40)}</button>`)}`,
    };
  },

  screen(s) {
    return s.kind === 'harness-settings' ? settingsScreen() : null;
  },
});

// --- a permission request, a plan approval ----------------------------------------------

function approvalTpl(r, p, w) {
  if (!p) return null;
  const pick = confirming.get(p.park);
  if (pick) {
    return html`<approval title=${`Allow “${pick.name}”?`} text=${optionConfirm(w.name, pick)}
      options=${[{ id: 'yes', label: pick.name, kind: 'allow_once' }, { id: 'no', label: 'Back', kind: 'reject_once' }]}
      @choose=${guard(async (e) => { confirming.delete(p.park); if (e.id === 'yes') await permit(r.id, { option: pick.id, park: p.park }); })}/>`;
  }
  const text = [p.plan ? '' : p.label, p.command ? '$ ' + p.command : '', p.raw, p.description,
    p.hidden ? `${p.hidden === 1 ? 'One option' : `${p.hidden} options`} that would stop ${w.name} asking — only the owner may pick them.` : '',
    w.talk ? '' : 'view only — someone who may write here answers it'].filter(Boolean).join('\n');
  const choose = guard(async (e) => {
    const o = p.options.find((x) => x.id === e.id);
    if (!o) return;
    if (o.explicit) { confirming.set(p.park, o); return; }
    const fb = o.reject && e.feedback ? String(e.feedback).trim() : '';
    await permit(r.id, { option: o.id, park: p.park, ...(fb ? { feedback: fb } : {}) });
  });
  return html`${p.plan && p.planText ? html`<markdown source=${p.planText}/>` : nothing}
    ${(p.preview || []).map((d) => (d.text != null ? html`<text mono text=${d.text}/>`
      : html`<diff files=${[{ path: d.path, status: 'modified', add: d.add, del: d.del }]} patch=${patchOf(d)}/>`))}
    <approval title=${p.title ? `${p.lead}: ${p.title}` : p.lead} text=${text}
      options=${w.talk ? p.options.map((o) => ({ id: o.id, label: (o.explicit ? '⚠ ' : '') + o.name, kind: o.kind })) : []}
      note=${p.rule || nothing} ?feedback=${w.talk && (p.plan || !!p.reject)} @choose=${choose}/>`;
}

// --- a question ------------------------------------------------------------------------------

function questionTpl(r, q, w) {
  if (!q) return null;
  if (!w.talk) return html`<notice tone="info" title=${`${w.name} asks`} text=${q.message || 'a question'}/>`;
  const decline = guard(() => answer(r.id, 'decline', undefined, q.park));
  if (q.mode === 'url') {
    const open = (e) => Promise.resolve(globalThis.xbin?.native?.open?.(e.href)).catch(() => fail(`Open ${e.href} in your browser.`));
    return html`${q.url ? html`<markdown source=${`[${q.url}](${q.url})`} @link=${open}/>` : nothing}
      <question title=${q.message || `${w.name} asks you to open a page`}
        schema=${{ type: 'object', description: `Open ${q.text || 'the page'}, then Submit when you are done.`, properties: {} }}
        @submit=${guard(() => answer(r.id, 'accept', {}, q.park))} @skip=${decline}/>`;
  }
  return html`<question title=${q.message || `${w.name} asks`} schema=${nativeSchema(q.fields)}
    @submit=${guard((e) => answer(r.id, 'accept', nativeContent(q.fields, e.content), q.park))} @skip=${decline}/>`;
}

// --- mode, options, your setting ----------------------------------------------------------

function controlsTpl(v, h) {
  const app = ctx.app;
  const entry = app.harness.find(h.provider);
  const c = controls(h, entry, who(v));
  const id = v.run.id;
  const s = entry ? settingOf(entry, app.harness.setting(entry.id)) : null;
  return html`<menu icon="gear" label=${'Mode: ' + (c.mode.name || '—')}>
      ${c.modes.filter((m) => m.allowed || m.current).map((m) => html`<button icon=${m.current ? 'check' : nothing}
        confirm=${m.explicit && !m.current ? { title: modeConfirm(c.name, m), label: 'Switch', destructive: true } : nothing}
        @tap=${guard(() => (m.current ? null : app.harness.setMode(id, m.id)))}>${(m.explicit ? '⚠ ' : '') + m.name}</button>`)}
      ${s ? html`<divider/>${s.choices.filter((x) => !x.disabled).map((x) => html`<button icon=${s.value === x.value ? 'check' : nothing}
        @tap=${guard(() => (s.value === x.value ? null : app.harness.setSetting(s.provider, x.value)))}>${`${x.label} — your setting for new ones`}</button>`)}` : nothing}
    </menu>
    ${c.talk ? c.options.map((o) => html`<picker label=${o.name} style="menu" value=${String(o.value)}
      options=${o.choices.map((ch) => ({ value: String(ch.value), label: ch.name }))}
      @change=${guard((e) => {
        const ch = o.choices.find((x) => String(x.value) === String(e.value));
        return ch && ch.value !== o.value ? app.harness.setOptionOf(id, o.id, ch.value) : null;
      })}/>`) : nothing}`;
}

function settingsScreen() {
  const app = ctx.app;
  app.harness.ensure();
  const list = app.harness.catalog.harnesses.filter((h) => h.available || app.harness.modes[h.id]);
  return html`<screen title="Coding agents" style="form">
    <section title="Auto or Always approve"
      footer="How a coding agent starts for you: your new conversations with it and the ones the agent starts for you. An open conversation's own mode is switched from its toolbar.">
      ${list.length ? list.map((h) => {
        const s = settingOf(h, app.harness.setting(h.id));
        return s.canAuto
          ? html`<picker label=${s.name} style="segmented" value=${s.value} options=${s.choices.map((x) => ({ value: x.value, label: x.label }))}
              @change=${guard((e) => app.harness.setSetting(h.id, e.value))}/>`
          : html`<row title=${s.name} detail="Always approve" subtitle=${s.choices[1].title}/>`;
      }) : html`<row title="No coding agent is available" subtitle=${app.harness.error || 'no bound sandbox manager offers one'}/>`}
    </section>
    ${ui.err ? html`<section><notice tone="danger" text=${ui.err}/></section>` : nothing}
  </screen>`;
}

// Send now: the composer's text, interrupting the running turn (the web's ⌘/Ctrl+Enter).
const sendNow = guard(async () => {
  const text = String(ui.draft || '');
  if (!text.trim()) { fail('Write the message first — Send now sends it and interrupts the turn.'); return; }
  await ctx.app.send(text, () => { ui.draft = ''; }, { interrupt: true });
});
