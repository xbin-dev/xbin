// model/project-wizard.js — the new-project wizard's steps for both views
// (API.md §Projects in the UI, "A new project"): which steps a form has —
// a team project's definition has no starter, and one made from your own
// space no sandbox of yours — what keeps a step from going on (Next says
// it), and what the starter work may be. The form itself is
// app.projects.form (model/projects.js); these read it. No lit, no DOM.
import { partitionState } from './partition.js';
import { find, reach } from './classes.js';

/** STEPS: every step, in order (the header numbers those that apply). */
export const STEPS = [
  { key: 'code', label: 'Code' },
  { key: 'name', label: 'Name' },
  { key: 'sandbox', label: 'Sandbox' },
  { key: 'how', label: 'How it works' },
  { key: 'start', label: 'Get started' },
  { key: 'review', label: 'Review' },
];

/** teamDefOf(form, state): the form makes a team project's definition —
 * at the shared space always, in your own space when you said so. */
export const teamDefOf = (f, state = partitionState()) => state === 'global' || (state === 'user' && !!(f && f.teamDef));

/** wizardSteps(form, state): the steps that apply, numbered from 1. A
 * definition has no tasks and no coordinator (no Get started); one made
 * from your own space has no sandbox of yours either (its seed is set on
 * its page) — the shared space's keeps its optional seed. */
export function wizardSteps(f, state = partitionState()) {
  const def = teamDefOf(f, state);
  return STEPS.filter((s) => !(s.key === 'start' && def) && !(s.key === 'sandbox' && state === 'user' && f && f.teamDef))
    .map((s, i) => ({ ...s, n: i + 1 }));
}

/** stepAt(form, key, state): the step key the form can show — key when it
 * applies, else the one before it that does (a choice that took a step away). */
export function stepAt(f, key, state = partitionState()) {
  const steps = wizardSteps(f, state);
  if (steps.some((s) => s.key === key)) return key;
  const at = STEPS.findIndex((s) => s.key === key);
  return [...steps].reverse().find((s) => STEPS.findIndex((x) => x.key === s.key) < at)?.key || steps[0].key;
}

/** nextStep / prevStep(form, key, state): the neighbour that applies ('' at an end). */
export function nextStep(f, key, state = partitionState()) {
  const steps = wizardSteps(f, state);
  const i = steps.findIndex((s) => s.key === key);
  return i >= 0 && i + 1 < steps.length ? steps[i + 1].key : '';
}
export function prevStep(f, key, state = partitionState()) {
  const steps = wizardSteps(f, state);
  const i = steps.findIndex((s) => s.key === key);
  return i > 0 ? steps[i - 1].key : '';
}

/**
 * stepProblem(form, key, {sb, state, providers}): why the step can't go on
 * ('' when it can) — the words Next says. sb is the sandbox choice as the
 * view resolves it (model/sandboxes.js projectSandbox: the manager's
 * defaults); Review is the first problem of the steps before it.
 */
export function stepProblem(f, key, ctx = {}) {
  const state = ctx.state || partitionState();
  if (!f) return 'closed';
  switch (key) {
    case 'code':
      if (!f.scm) return ctx.providers === 0 ? 'No scm provider is bound to this agent.' : 'Pick a provider.';
      if (!f.repos.length) return 'Choose a repo.';
      return '';
    case 'name': {
      const n = String(f.name || '').trim();
      if (!n) return 'Name the project.';
      if ([...n].length > 80) return 'A name is at most 80 characters.';
      return '';
    }
    case 'sandbox': {
      const sb = ctx.sb || f.sandbox || {};
      const mode = sb.mode || 'new';
      if (mode === 'none' && state === 'global') return '';
      if (mode === 'pick') return sb.ref ? '' : 'Pick a sandbox, or make a new one.';
      return sb.provider ? '' : 'No sandbox manager to make a sandbox with.';
    }
    case 'how': {
      const n = Number(f.policy.maxTasks);
      return Number.isInteger(n) && n >= 1 && n <= 16 ? '' : 'Tasks at work at once: 1 to 16.';
    }
    case 'review':
      for (const s of wizardSteps(f, state)) {
        if (s.key === 'review') break;
        const why = stepProblem(f, s.key, ctx);
        if (why) return why;
      }
      return '';
  }
  return '';
}

/**
 * starterOffer(form, {state, wiki, classes}): what the starter work may be
 * here — {applies, build: {ok, why}, wiki: {ok, why}}. It applies to a
 * project of yours with a sandbox; the wiki needs your coordinator (its
 * class, the built-in web class, one you may use without internal reach)
 * and a backend with the project wiki — wiki is GET /projects/0/wiki's
 * probe (true, false, null while unknown); a backend without the wiki has
 * no starter either (they came together, D222/D223).
 */
export function starterOffer(f, ctx = {}) {
  const state = ctx.state || partitionState();
  const no = (why) => ({ ok: false, why });
  const yes = { ok: true, why: '' };
  if (teamDefOf(f, state)) {
    const why = 'A team project\'s definition has no tasks and no coordinator: each member\'s own project of it can start with its own.';
    return { applies: false, build: no(why), wiki: no(why) };
  }
  if (f && f.sandbox && f.sandbox.mode === 'none') {
    const why = 'The starter works in the project\'s sandbox.';
    return { applies: false, build: no(why), wiki: no(why) };
  }
  if (ctx.wiki == null) {
    const why = 'Checking what this agent\'s backend offers…';
    return { applies: true, build: no(why), wiki: no(why) };
  }
  if (ctx.wiki === false) {
    const why = 'This agent\'s backend is older: it has no project wiki and no starter work.';
    return { applies: false, build: no(why), wiki: no(why) };
  }
  return { applies: true, build: yes, wiki: coordProblem(ctx.classes) ? no(coordProblem(ctx.classes)) : yes };
}

// coordProblem: why you can't have a coordinator ('' when you can) — the
// classes you may use (GET /classes) lack its class, or it has internal reach.
function coordProblem(classes) {
  if (!classes || classes.fallback) return '';
  const web = find(classes, 'web');
  if (!web) return 'The coordinator\'s class (Web) is kept for the agent\'s managers: the initial wiki needs your coordinator.';
  if (reach(web).internal) return 'The coordinator\'s class (Web) has internal reach, so no coordinator is made in it.';
  return '';
}

/** starterBody(form, offer): POST /projects' starter — what is chosen and
 * may be — or undefined (nothing: the project starts as it always has). */
export function starterBody(f, offer) {
  const s = (f && f.starter) || {};
  const build = !!s.build && offer.build.ok;
  const wiki = !!s.wiki && offer.wiki.ok;
  return offer.applies && (build || wiki) ? { build, wiki } : undefined;
}

/** STARTER: the starter's choices, each said in a line. */
export const STARTER = [
  { key: 'build', label: 'Clone, build and run the tests',
    hint: 'Task #1: its agent finds how each repo is built and tested, runs it in the sandbox and says what worked and what failed — no code changed.' },
  { key: 'wiki', label: 'Create the initial knowledge base',
    hint: 'Your coordinator maps the code with research subagents and writes the project wiki — after task #1 when both are chosen, building on what it verified.' },
];
