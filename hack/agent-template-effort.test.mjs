// hack/agent-template-effort.test.mjs — the agent template's reasoning-effort
// chip (builtin-templates/agent/model/effort.js, D249): a model's record →
// the track's notches, the Default label, the keyboard, what a level becomes
// on a model that lacks it (the LLM connector's rule, internal/llm/effort.go,
// mirrored), and the fold's token count. Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { installHooks } from './xbn/hooks.mjs';

installHooks();
const E = await import('../builtin-templates/agent/model/effort.js');
// the chat kit's own helper (web/chat/views.js imports lit: read as text, run alone)
const kitSrc = readFileSync(new URL('../web/chat/views.js', import.meta.url), 'utf8');
const kitTokens = new Function(`${/export const thoughtTokens = ([^;]*);/.exec(kitSrc)[0].replace('export ', '')}; return thoughtTokens;`)();

const deepseek = { supported_levels: ['low', 'high', 'max'], default_level: 'high', can_disable: true, thinks_by_default: true,
  aliases: { minimal: 'low', medium: 'high', xhigh: 'high' }, source: 'xbin' };
const kimi = { supported_levels: ['low', 'high', 'max'], default_level: 'max', can_disable: false, source: 'xbin' };
const toggle = { supported_levels: ['high'], can_disable: true, source: 'admin' };
const none = { supported_levels: [], source: 'admin' };
const glm52 = { supported_levels: ['medium', 'high', 'xhigh'], can_disable: true, source: 'admin' }; // a router's, as an admin described it

test('the ladder is the connector\'s', () => {
  const go = readFileSync(new URL('../internal/llm/effort.go', import.meta.url), 'utf8');
  const ladder = /var effortLadder = \[\]string\{([^}]*)\}/.exec(go)[1].split(',').map((s) => s.trim().replace(/"/g, ''));
  assert.deepEqual([...E.LADDER], ladder);
  const tpl = readFileSync(new URL('../builtin-templates/agent/_backend/llm_effort.go', import.meta.url), 'utf8');
  assert.deepEqual(/var effortLadder = \[\]string\{([^}]*)\}/.exec(tpl)[1].split(',').map((s) => s.trim().replace(/"/g, '')), ladder);
});

test('a record → its notches: Off first where it can be off, on/off as two, none without a usable record', () => {
  assert.deepEqual(E.notches(deepseek).map((n) => n.label), ['Off', 'Low', 'High', 'Max']);
  assert.deepEqual(E.notches(kimi).map((n) => n.label), ['Low', 'High', 'Max']);
  assert.deepEqual(E.notches(toggle), [{ value: 'none', label: 'Off' }, { value: 'high', label: 'On' }]);
  assert.deepEqual(E.notches(none), []);
  assert.deepEqual(E.notches(null), []);
  assert.deepEqual(E.notches({ supported_levels: [], partial: true }), [], 'a budget it can\'t use yet');
  assert.deepEqual(E.notches({ supported_levels: ['max', 'turbo', 'low', 'low'] }).map((n) => n.value), ['low', 'max'], 'ordered, ladder words only');
  assert.deepEqual(E.notches({ reasons: true }), [], 'reasons, levels unknown: no control');
  assert.equal(E.kindOf(kimi), 'levels');
  assert.equal(E.kindOf(toggle), 'toggle');
});

test('Default is named after the model\'s own', () => {
  assert.equal(E.defaultLabel(deepseek), 'Default · High');
  assert.equal(E.defaultLabel({ ...deepseek, default_level: 'none' }), 'Default · Off');
  assert.equal(E.defaultLabel({ supported_levels: ['low'] }), 'Default');
  assert.equal(E.defaultLabel({ ...toggle, thinks_by_default: true }), 'Default · On');
});

test('what a level becomes: exact, the alias, the highest at or below, the lowest; none only where it can be off', () => {
  const cases = [
    [deepseek, 'medium', 'high'], [deepseek, 'minimal', 'low'], [deepseek, 'none', 'none'], [deepseek, 'max', 'max'],
    [kimi, 'none', 'low'], [kimi, 'medium', 'low'], [kimi, 'xhigh', 'high'],
    [glm52, 'low', 'medium'], [glm52, 'minimal', 'medium'], [glm52, 'max', 'xhigh'], [glm52, 'none', 'none'],
    [toggle, 'low', 'high'], [null, 'medium', 'medium'], [deepseek, 'ultra', 'ultra'],
  ];
  for (const [rec, asked, want] of cases) assert.equal(E.mapLevel(rec, asked), want, `${JSON.stringify(rec?.supported_levels)} ${asked}`);
});

const catalog = {
  data: [
    { id: 'house/deepseek-v4.1-flash', ref: 'house/deepseek-v4.1-flash', provider: 'xbin:llm', effort: deepseek },
    { id: 'house/kimi-k3', ref: 'house/kimi-k3', provider: 'xbin:llm', effort: kimi },
    { id: 'house/plain', ref: 'house/plain', provider: 'xbin:llm', effort: none },
    { id: 'house/llama', ref: 'house/llama', provider: 'xbin:llm' },
    { id: 'xbin/default', ref: 'xbin/default', provider: 'xbin:llm', alias_of: 'house/kimi-k3' },
  ],
  defaultModel: 'house/deepseek-v4.1-flash', defaultEffort: '',
};
const view = (config, cls = { id: 'internal' }, run = { id: 1 }) => ({ run, config, class: cls, messages: [] });

test('the chip: shown for a model that reasons, hidden otherwise; its label and the value the track marks', () => {
  let c = E.chip(view({ pick: 'house/deepseek-v4.1-flash' }), { catalog });
  assert.equal(c.shown, true);
  assert.equal(c.label, 'Default · High');
  assert.equal(c.value, 'default');
  c = E.chip(view({ pick: 'house/deepseek-v4.1-flash', effort: 'max' }), { catalog });
  assert.deepEqual([c.label, c.value, c.always], ['Max', 'max', false]);
  for (const m of ['house/plain', 'house/llama', 'gone/model']) assert.equal(E.chip(view({ pick: m }), { catalog }).shown, false, m);
  assert.equal(E.chip(view({ pick: 'house/kimi-k3' }, { id: 'workspace' }), { catalog }).shown, false, 'the workspace agent');
  assert.equal(E.chip(view({ pick: 'house/kimi-k3' }, { id: 'x' }, { id: 1, engine: 'harness' }), { catalog }).shown, false, 'a coding agent');
  // the model a turn uses: the pick, the class's, the tiers, the agent's default; an alias: its target's record
  assert.equal(E.chip(view({}), { catalog }).model, 'house/deepseek-v4.1-flash');
  assert.equal(E.chip(view({}, { id: 'c', model: 'house/kimi-k3' }), { catalog }).def, 'Default · Max');
  assert.equal(E.chip(view({ pick: 'xbin/default' }), { catalog }).def, 'Default · Max');
  // a model switch keeps the level: the chip says what it became
  c = E.chip(view({ pick: 'house/kimi-k3', effort: 'none' }), { catalog });
  assert.deepEqual([c.label, c.value, c.always], ['Low (always thinks)', 'low', true]);
  c = E.chip(view({ pick: 'house/kimi-k3', effort: 'medium' }), { catalog });
  assert.equal(c.label, 'Low (Medium isn\'t offered)');
  assert.match(c.note, /Medium isn't offered by house\/kimi-k3: it gets Low/);
  // the class's, then the agent's default (the run's snapshot of it), then nothing
  assert.equal(E.chip(view({ pick: 'house/kimi-k3' }, { id: 'c', effort: 'high' }), { catalog }).label, 'High');
  assert.equal(E.chip(view({ pick: 'house/kimi-k3', reasoningEffort: 'low' }), { catalog }).label, 'Low');
  assert.equal(E.chip(view({ pick: 'house/kimi-k3', reasoningEffort: 'low', effort: 'default' }), { catalog }).label, 'Default · Max');
  // at home: the next new chat's pick, over the agent's default
  assert.equal(E.chip(null, { catalog, model: 'house/kimi-k3', effort: 'high' }).label, 'High');
  assert.equal(E.chip(null, { catalog: { ...catalog, defaultEffort: 'low' }, model: 'house/kimi-k3' }).label, 'Low');
  // someone who may only read sees it, disabled
  assert.equal(E.chip(view({ pick: 'house/kimi-k3' }), { catalog, talk: false }).disabled, true);
});

test('the keyboard: ←/→ notch to notch, Home/End, 0 Default; from Default, the model\'s default notch', () => {
  const c = (value) => ({ ...E.chip(view({ pick: 'house/deepseek-v4.1-flash' }), { catalog }), value });
  assert.equal(E.step(c('low'), 'ArrowRight'), 'high');
  assert.equal(E.step(c('high'), 'ArrowLeft'), 'low');
  assert.equal(E.step(c('none'), 'ArrowLeft'), 'none', 'stays at the end');
  assert.equal(E.step(c('max'), 'ArrowRight'), 'max');
  assert.equal(E.step(c('low'), 'Home'), 'none');
  assert.equal(E.step(c('low'), 'End'), 'max');
  assert.equal(E.step(c('max'), '0'), 'default');
  assert.equal(E.step(c('default'), 'ArrowRight'), 'high');
  assert.equal(E.step(c('default'), 'ArrowLeft'), 'high');
  assert.equal(E.step(c('high'), 'x'), null);
});

test('the fold\'s reasoning tokens', () => {
  assert.equal(E.thoughtTokens({ reasoning_tokens: 4310 }), '4.3k tokens');
  assert.equal(E.thoughtTokens({ reasoning_tokens: 900 }), '900 tokens');
  assert.equal(E.thoughtTokens({ reasoning_tokens: 21000 }), '21k tokens');
  assert.equal(E.thoughtTokens({}), '');
  assert.equal(kitTokens(4310), ' · 4.3k tokens');
  assert.equal(kitTokens(0), '');
  assert.equal(kitTokens(undefined), '');
});

test('the fold carries the tokens of an answer\'s usage to its thought', async () => {
  const { fold, blocksOf } = await import('../builtin-templates/agent/model/fold.js');
  const v = { run: { id: 1, status: 'idle' }, steps: [], links: [], messages: [
    { id: 1, runId: 1, seq: 1, role: 'user', content: 'q', created: 1 },
    { id: 2, runId: 1, seq: 2, role: 'assistant', content: 'a', reasoning: 'hm', reasoningMs: 21000, usage: { reasoning_tokens: 4310 }, created: 2 },
    { id: 3, runId: 1, seq: 3, role: 'assistant', content: 'b', reasoning: 'hm', reasoningMs: 1000, created: 3 },
  ] };
  const thoughts = blocksOf(fold(v)).filter((b) => b.kind === 'thought');
  assert.equal(thoughts[0].tokens, 4310);
  assert.equal('tokens' in thoughts[1], false);
});
