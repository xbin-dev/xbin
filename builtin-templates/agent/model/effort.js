// model/effort.js — the composer's reasoning-effort chip (D249), pure.
//
// A conversation's level is one word of OpenAI's ladder (or "default": the
// model's own). The model's record — on its row of GET /models, effort: the
// LLM connector's, or the provider's own list's — says which levels it
// takes; the chip offers only those, with Off first when it can be turned
// off, as a notched track; a model that is on or off only is Off | On; a
// model with no record, or no reasoning, gets no chip. "Default" sends
// nothing and is named after the model's own default when the record knows
// it ("Default · High"). On a model switch the level (the intent) is kept,
// and the chip shows what it becomes there — the connector's rule (mapLevel)
// — and why.
//
// The backend's half is _backend/llm_effort.go; the connector's mapping is
// internal/llm/effort.go (the same rule, mirrored here for the label).

export const LADDER = Object.freeze(['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']);
export const DEFAULT = 'default';
const WORDS = { none: 'Off', minimal: 'Minimal', low: 'Low', medium: 'Medium', high: 'High', xhigh: 'Extra high', max: 'Max' };
const rank = (w) => LADDER.indexOf(w);
const WORKSPACE_CLASS = 'workspace'; // classWorkspace: the workspace agent has no chip yet

/** word('xhigh') → "Extra high": a level as the chip says it. */
export const word = (w) => WORDS[w] ?? String(w ?? '');

/**
 * levelsOf(rec) → the levels a record says the model takes, cheapest first,
 * ladder words only ("none" left out); null when it says nothing usable.
 */
export function levelsOf(rec) {
  if (!rec || typeof rec !== 'object' || !Array.isArray(rec.supported_levels)) return null;
  const ls = [...new Set(rec.supported_levels.filter((w) => rank(w) > 0))].sort((a, b) => rank(a) - rank(b));
  if (!ls.length && (rec.partial || rec.supported_levels.length)) return null; // a budget it can't use yet, or words not ours
  return ls;
}

/** kindOf(rec) → 'levels' | 'toggle' | '' (no chip: no record, no reasoning). */
export function kindOf(rec) {
  const ls = levelsOf(rec);
  if (!ls || !ls.length) return '';
  return ls.length === 1 && rec.can_disable === true ? 'toggle' : 'levels';
}

/**
 * mapLevel(rec, level) → what the connector sends a model of rec for level:
 * a level it takes; else its own alias; else the highest it has at or below;
 * else its lowest. none: off where it can be, else its lowest. A word that is
 * no ladder word, or a model it can't describe, keeps the word.
 */
export function mapLevel(rec, level) {
  const ls = levelsOf(rec);
  if (!ls || !ls.length || rank(level) < 0) return level;
  if (level === 'none') return rec.can_disable === true ? 'none' : ls[0];
  if (ls.includes(level)) return level;
  const alias = rec.aliases && rec.aliases[level];
  if (alias && ls.includes(alias)) return alias;
  let out = ls[0];
  for (const l of ls) if (rank(l) <= rank(level)) out = l;
  return out;
}

/**
 * notches(rec) → the track's stops, cheapest first: [{value, label}] — Off
 * first when the model can be turned off; a toggle-only model's Off | On.
 */
export function notches(rec) {
  const k = kindOf(rec);
  if (!k) return [];
  const ls = levelsOf(rec);
  if (k === 'toggle') return [{ value: 'none', label: 'Off' }, { value: ls[0], label: 'On' }];
  return [...(rec.can_disable === true ? [{ value: 'none', label: 'Off' }] : []), ...ls.map((l) => ({ value: l, label: word(l) }))];
}

/** defaultLabel(rec) → "Default · High", "Default · Off", or "Default". */
export function defaultLabel(rec) {
  const d = rec && rec.default_level;
  if (d === 'none') return 'Default · Off';
  if (d && rank(d) > 0) return kindOf(rec) === 'toggle' ? 'Default · On' : `Default · ${word(d)}`;
  if (kindOf(rec) === 'toggle' && rec.thinks_by_default === true) return 'Default · On';
  return 'Default';
}

/**
 * recordOf(catalog, ref) → a model reference's record from GET /models (an
 * alias without one: its target's); null when nothing says.
 */
export function recordOf(catalog, ref) {
  const data = (catalog && catalog.data) || [];
  const i = (ref || '').indexOf('|');
  const prov = i >= 0 ? ref.slice(0, i) : '', id = i >= 0 ? ref.slice(i + 1) : ref || '';
  const find = (x) => data.find((m) => (m.ref === x || m.id === x) && (!prov || m.provider === prov));
  const m = find(id) || find(ref);
  if (!m) return null;
  if (!m.effort && m.alias_of) {
    const t = find(m.alias_of);
    return (t && t.effort) || null;
  }
  return m.effort || null;
}

/**
 * modelOf(config, cls, catalog) → the model a conversation's next turn uses
 * (llm.go modelFor's general tier): its pick, its class's, the tiers, the
 * tile's default (GET /models defaultModel).
 */
export function modelOf(config, cls, catalog) {
  const c = config || {};
  return c.pick || (cls && cls.model) || (c.models && c.models.general) || c.model || (catalog && catalog.defaultModel) || '';
}

/**
 * intentOf(config, cls, tileDefault) → the level a turn asks for
 * (llm_effort.go effortFor): {value, from} — value '' for the model's own;
 * from 'pick' | 'class' | 'tile' | ''.
 */
export function intentOf(config, cls, tileDefault = '') {
  const c = config || {};
  if (cls && cls.id === WORKSPACE_CLASS) return { value: '', from: '' };
  const pick = c.effort || (cls && cls.effort) || '';
  const from = c.effort ? 'pick' : (cls && cls.effort) ? 'class' : '';
  if (pick === DEFAULT) return { value: '', from };
  if (pick) return { value: pick, from };
  const tile = c.reasoningEffort ?? tileDefault ?? '';
  return { value: tile || '', from: tile ? 'tile' : '' };
}

/**
 * chip(v, ctx) → the composer's chip: {shown, value, label, title, model,
 * notches, def, always, note, rec}. v is the open view (null at home); ctx
 * {catalog, model (home's pick), effort (home's pick, not remembered),
 * classes (the class for a new chat), tileDefault, talk}. value is the notch
 * the track marks: the level as the model takes it, or DEFAULT.
 */
export function chip(v, ctx = {}) {
  const cls = v ? v.class : ctx.cls;
  const config = v ? v.config : { pick: ctx.model || '', effort: ctx.effort || '' };
  const model = modelOf(config, cls, ctx.catalog);
  const rec = recordOf(ctx.catalog, model);
  const harness = !!(v && v.run && v.run.engine === 'harness');
  const k = kindOf(rec);
  const intent = intentOf(config, cls, ctx.tileDefault ?? (ctx.catalog && ctx.catalog.defaultEffort));
  const out = { shown: !!k && !harness && !(cls && cls.id === WORKSPACE_CLASS), model, rec, notches: notches(rec), def: defaultLabel(rec),
    always: !!k && rec.can_disable !== true, value: DEFAULT, label: '', title: '', note: '', disabled: !!v && ctx.talk === false };
  if (!out.shown) return out;
  const bare = model.includes('|') ? model.slice(model.indexOf('|') + 1) : model;
  if (!intent.value) {
    out.label = out.def;
    out.title = `Reasoning: the model's own default${rec.default_level ? ` (${word(rec.default_level)})` : ''} — ${bare}`;
    return out;
  }
  const got = mapLevel(rec, intent.value);
  out.value = got;
  const name = k === 'toggle' ? (got === 'none' ? 'Off' : 'On') : word(got);
  out.label = name;
  if (got !== intent.value && rank(intent.value) >= 0) {
    out.note = intent.value === 'none' ? `${bare} always thinks: it gets ${word(got)}` : `${word(intent.value)} isn't offered by ${bare}: it gets ${word(got)}`;
    out.label = `${name} (${word(intent.value)} isn't offered)`;
    if (intent.value === 'none') out.label = `${name} (always thinks)`;
  }
  out.title = `Reasoning: ${name}${intent.from === 'tile' ? ' (the agent\'s default)' : intent.from === 'class' ? ' (the class\'s)' : ''} — ${bare}`;
  return out;
}

/**
 * step(c, key) → the value a key moves the track to (null: not a track key):
 * ←/→ notch to notch, Home/End the cheapest and the most thorough, 0
 * Default. From Default, → starts at the model's default notch (or the
 * first) and ← at the last.
 */
export function step(c, key) {
  const vals = c.notches.map((n) => n.value);
  if (!vals.length) return null;
  const i = vals.indexOf(c.value);
  switch (key) {
    case '0': return DEFAULT;
    case 'Home': return vals[0];
    case 'End': return vals[vals.length - 1];
    case 'ArrowRight': case 'ArrowUp':
      if (i < 0) return vals.includes(c.rec.default_level) ? c.rec.default_level : vals[0];
      return vals[Math.min(i + 1, vals.length - 1)];
    case 'ArrowLeft': case 'ArrowDown':
      if (i < 0) return vals.includes(c.rec.default_level) ? c.rec.default_level : vals[vals.length - 1];
      return vals[Math.max(i - 1, 0)];
  }
  return null;
}

/**
 * thoughtTokens(usage) → "4.3k tokens" for the thinking fold: the reasoning
 * tokens an answer's usage reports ('' when it reports none).
 */
export function thoughtTokens(usage) {
  const n = Number(usage && usage.reasoning_tokens) || 0;
  if (n <= 0) return '';
  if (n < 1000) return `${n} tokens`;
  return `${(n / 1000).toFixed(n < 10000 ? 1 : 0).replace(/\.0$/, '')}k tokens`;
}
