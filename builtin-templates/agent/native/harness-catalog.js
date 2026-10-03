// native/harness-catalog.js — Settings → Coding agents in the native view
// (D147 §4.3.10), for the tile's managers: each coding agent GET
// /harnesses lists — whether it could be started and why not, the sandbox
// managers and images that have it, the sandboxes it was found or signed in
// on, the classes that allow it, its modes and sign-in command — and
// checking a running sandbox now (?probe=). A pushed screen of its own kind
// ({kind: 'harnesses'}, native/settings.js pushes it) drawn through the
// `screen` seam. What it says is model/harness-manage.js; the web's
// harness-catalog.js draws the same tab. It links your own saved sign-ins
// (native/harness-signins.js).
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ctx, guard } from './ui.js';
import { catalogRows, probeTargets, modesWords } from '../model/harness-manage.js';
import { openSignins } from './harness-signins.js';

const TONE = { ok: 'ok', warn: 'warn', bad: 'danger' };

function catalogScreen(s) {
  const app = ctx.app;
  if (!s.loaded) {
    s.loaded = true;
    Promise.all([app.harness.load(), app.sbx.load(true)]).finally(() => ctx.paint());
  }
  const rows = catalogRows(app.harness.catalog, app.sbx.list, app.classes);
  const targets = probeTargets(app.sbx.list);
  const check = (t) => guard(async () => {
    await app.harness.load(t.ref);
    s.msg = app.harness.error || `checked ${t.name}`;
  });
  return html`<screen title="Coding agents" style="list" refreshable @refresh=${guard(() => Promise.all([app.harness.load(), app.sbx.load(true)]))}>
    ${app.harness.error ? html`<section><notice tone="danger" text=${app.harness.error}/></section>` : nothing}
    <section footer="One can be started when a bound sandbox manager's image has it, a class people may use allows it (Classes: the Coding agents toolset), and that class allows a sandbox with an egress other than none — it must reach its provider.">
      ${rows.length ? nothing : html`<progress label="loading…"/>`}
    </section>
    <section footer="Your own saved sign-ins — every person keeps theirs in their own space.">
      <row title="Your coding-agent sign-ins" icon="key" nav @tap=${openSignins}/>
    </section>
    ${repeat(rows, (r) => r.id, (r) => html`<section title=${`${r.mono} · ${r.name}`} footer=${modesWords(r)}>
      <row title=${r.available ? 'Available' : 'Not available'} subtitle=${r.why || nothing} icon=${r.available ? 'check' : 'warning'} tone=${r.available ? 'ok' : 'warn'}/>
      ${r.images.length ? repeat(r.images, (i) => i.label, (i) => html`<row title=${i.label} icon="box"/>`) : html`<row title="No bound manager's image has it" icon="box" tone="muted"/>`}
      ${repeat(r.sandboxes, (x) => x.ref, (x) => html`<row title=${x.name} subtitle=${x.label} icon="terminal" tone=${TONE[x.tone] || nothing}/>`)}
      <row title="Classes" detail=${r.classes.length ? r.classes.join(', ') : 'none you may use'} icon="shield"/>
      ${r.login ? html`<row title="Sign-in" subtitle=${r.login} mono="subtitle" icon="key"/>` : nothing}
    </section>`)}
    ${targets.length ? html`<section title="Check a running sandbox now" footer=${s.msg || 'Asks it which coding agents it has.'}>
      ${repeat(targets, (t) => t.ref, (t) => html`<row title=${t.name} icon="search" @tap=${check(t)}/>`)}
    </section>` : nothing}
  </screen>`;
}

ext.register({ screen: (s) => (s.kind === 'harnesses' ? catalogScreen(s) : null) });
