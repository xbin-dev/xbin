// native/settings.js — the managers' settings, as pushed screens: the config
// (a model per tier, the base system prompt, limits, behaviour), the feature
// switches, the skill library (tools.js) and the MCP servers bound. The web's
// agent.js draws the same as the ⚙ panel's tabs, over the same routes.
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi as api, jbody } from '/vendor/bx-kit.js';
import { ui, ctx, push } from './ui.js';

function load(s, fn) {
  if (s.loaded) return;
  s.loaded = true;
  s.err = '';
  Promise.resolve().then(() => fn(s)).catch((e) => { s.err = e.message; }).finally(() => ctx.paint());
}
const errTpl = (s) => (s.err ? html`<section><notice tone="danger" text=${s.err}/></section>` : nothing);

function settingsTpl() {
  const app = ctx.app;
  const mcp = globalThis.xbin?.iface?.('mcp');
  const n = (mcp && (mcp.endpoints || []).length) || 0;
  return html`<screen title="Settings" style="list">
    <section>
      <row title="Config" subtitle="models, system prompt, limits, behaviour" icon="gear" nav @tap=${() => push({ kind: 'config' })}/>
      <row title="Features" icon="bolt" nav @tap=${() => push({ kind: 'features' })}/>
      <row title="Skills" icon="star" nav @tap=${() => push({ kind: 'skills' })}/>
      <row title="MCP servers" icon="server" detail=${String(n)} nav @tap=${() => push({ kind: 'mcp' })}/>
    </section>
    ${app.me.manager ? html`<section title="The brake" footer="Halting stops every run of this agent at once; nothing starts until it resumes.">
      ${app.halted
        ? html`<button role="primary" @tap=${() => app.setHalt(false).catch((e) => { ui.err = e.message; }).finally(ctx.paint)}>Resume the agent</button>`
        : html`<button role="destructive" confirm=${{ title: 'Stop every running agent now?', label: 'Halt', destructive: true }}
            @tap=${() => app.setHalt(true).catch((e) => { ui.err = e.message; }).finally(ctx.paint)}>Halt every run</button>`}
    </section>` : nothing}
  </screen>`;
}

const TIERS = [['general', 'General'], ['code', 'Code'], ['memory', 'Memory'], ['vlm', 'Vision (VLM)']];

// config: GET /config + GET /models; Save sends the WHOLE merged config back
// (features, mcp and the rest ride along untouched), as the web does.
function configTpl(s) {
  load(s, async () => {
    const [c, m] = await Promise.all([api('/config'), api('/models').catch(() => ({ data: [] }))]);
    s.cfg = c;
    s.models = (m.data || []).map((x) => x.id).filter(Boolean);
    s.f = { models: { ...(c.models || {}) }, system: c.system || '', tokenBudget: String(Number(c.tokenBudget) || 0),
      maxIters: String(Number(c.maxIters) || 0), toolTimeout: String(Number(c.toolTimeout) || 0), subagents: !!c.subagents, approve: !!c.approve };
  });
  const f = s.f;
  if (!f) return html`<screen title="Config" style="form">${errTpl(s)}<section><progress label="loading…"/></section></screen>`;
  const opts = [{ value: '', label: '— llm-gw default —' }, ...s.models.map((id) => ({ value: id, label: id }))];
  const save = async () => {
    s.err = ''; s.msg = '';
    const next = { ...s.cfg, models: { ...f.models }, system: f.system, tokenBudget: Number(f.tokenBudget) || 0,
      maxIters: Number(f.maxIters) || 0, toolTimeout: Number(f.toolTimeout) || 0, subagents: f.subagents, approve: f.approve };
    try { await api('/config', jbody(next, 'PUT')); s.cfg = next; s.msg = 'saved ✓'; } catch (e) { s.err = e.message; }
    ctx.paint();
  };
  return html`<screen title="Config" subtitle=${s.msg || nothing} style="form">
    <toolbar><button role="primary" @tap=${save}>Save</button></toolbar>
    ${errTpl(s)}
    <section title="Model tiers" footer=${s.models.length ? 'Empty = the workspace\'s llm-gw default for that job.' : 'No models listed — set an llm-gw backend token.'}>
      ${repeat(TIERS, ([k]) => k, ([k, label]) => html`<picker label=${label} style="menu" value=${f.models[k] || ''}
        options=${opts.some((o) => o.value === (f.models[k] || '')) ? opts : [...opts, { value: f.models[k], label: f.models[k] }]}
        @change=${(e) => { f.models[k] = e.value; ctx.paint(); }}/>`)}
    </section>
    <section title="Base system prompt"><field kind="multiline" value=${f.system} @input=${(e) => { f.system = e.value; }}/></section>
    <section title="Limits">
      <field label="Token budget" kind="number" value=${f.tokenBudget} @input=${(e) => { f.tokenBudget = e.value; }}/>
      <field label="Max iterations per drive" kind="number" value=${f.maxIters} @input=${(e) => { f.maxIters = e.value; }}/>
      <field label="Tool timeout (s)" kind="number" value=${f.toolTimeout} @input=${(e) => { f.toolTimeout = e.value; }}/>
    </section>
    <section title="Behaviour">
      <toggle label="Subagents (expose spawn_subagent)" value=${f.subagents} @change=${(e) => { f.subagents = e.value; ctx.paint(); }}/>
      <toggle label="Require approval before side-effecting tools" value=${f.approve} @change=${(e) => { f.approve = e.value; ctx.paint(); }}/>
    </section>
  </screen>`;
}

const FEATURE_DESC = {
  recall: 'FTS recall over turns compacted out of the window',
  skills: 'skill-library tools + the injected skills list',
  streaming: 'stream partial assistant text (the live draft)',
  vision: 'send images to the VLM tier',
  parallelTools: "run a turn's tool calls in parallel",
  watcher: 'watcher cron-agents (one persistent run, discard no-change rounds)',
};

// features: each switch merges {features: {k: on}} into the current config.
function featuresTpl(s) {
  load(s, async () => { const f = await api('/features'); s.keys = f.keys || []; s.on = f.features || {}; });
  const flip = (k) => async (e) => {
    s.err = '';
    try {
      const c = await api('/config');
      c.features = { ...(c.features || {}), [k]: e.value };
      await api('/config', jbody(c, 'PUT'));
      s.on = { ...s.on, [k]: e.value };
    } catch (err) { s.err = err.message; }
    ctx.paint();
  };
  return html`<screen title="Features" style="form">
    ${errTpl(s)}
    <section footer="Each switch merges into the agent's default config.">${s.keys ? repeat(s.keys, (k) => k, (k) => html`
      <toggle label=${FEATURE_DESC[k] ? `${k} — ${FEATURE_DESC[k]}` : k} value=${!!s.on[k]} @change=${flip(k)}/>`) : html`<progress label="loading…"/>`}
    </section>
  </screen>`;
}

// mcp: the bound MCP providers (the multi:true `mcp` http slot), read-only.
function mcpTpl() {
  const mcp = globalThis.xbin?.iface?.('mcp');
  const eps = (mcp && mcp.endpoints) || [];
  return html`<screen title="MCP servers" style="list">
    <section footer=${eps.length ? 'Their tools are offered to the model as mcp:<server>:<tool>.'
      : 'No MCP servers bound. Bind MCP-providing components to this component\'s mcp slot (its Interfaces tab); their tools then become available to the agent.'}>
      ${eps.length ? repeat(eps, (e, i) => i, (e) => html`<row title=${e.provider || e.instance || e.service || ''} subtitle=${e.url || ''} mono="subtitle" icon="server"/>`)
        : html`<empty icon="server" title="none bound"/>`}
    </section>
  </screen>`;
}

export const settingsScreens = { settings: settingsTpl, config: configTpl, features: featuresTpl, mcp: mcpTpl };
