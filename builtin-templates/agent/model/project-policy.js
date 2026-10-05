// model/project-policy.js — a project's policy keys as the settings show
// them, and the helpers that read and write one key (API.md §Projects and
// tasks, ProjectPolicy). Re-exported by model/projects.js.

// The policy's keys (ProjectPolicy, API.md §Projects and tasks), grouped as
// the settings show them. type: text | area | lines (a list, one per line) |
// number | bool | select (options) | class (a class you may use) | harness
// (a coding agent of the catalog). A key the build doesn't know is kept as
// it is stored and never shown.
export const POLICY = [
  { key: 'tasks', title: 'Tasks', fields: [
    { path: 'taskClass', label: 'Class of new tasks', type: 'class', hint: 'never one with internal reach' },
    { path: 'engine', label: 'Who answers', type: 'select', options: [['auto', 'auto — your pick'], ['builtin', 'the built-in agent'], ['harness', 'a coding agent']] },
    { path: 'harness', label: 'Coding agent', type: 'harness', hint: 'empty: the one you used last' },
    { path: 'maxTasks', label: 'Tasks at work at once', type: 'number', min: 1, max: 16 },
    { path: 'maxOpenTasks', label: 'Open tasks a coordinator may make', type: 'number', min: 0 },
    { path: 'maxTaskCreatesPerDay', label: 'Tasks a coordinator may make a day', type: 'number', min: 0 },
    { path: 'instructions', label: 'Instructions for every task', type: 'area', hint: 'after the repos\' own AGENTS.md' },
    { path: 'checks', label: 'Checks before a push', type: 'lines', hint: 'one command per line' },
  ] },
  { key: 'prs', title: 'Branches and pull requests', fields: [
    { path: 'branchPrefix', label: 'Branch prefix', type: 'text', hint: 'empty: xbin/<the project\'s id>' },
    { path: 'autoPR', label: 'Open a pull request when a task rests', type: 'select', options: [['off', 'off — Open PR by hand'], ['draft', 'as a draft'], ['ready', 'ready for review']] },
    { path: 'prConventions', label: 'Pull request conventions', type: 'area' },
    { path: 'as', label: 'Work as', type: 'select', options: [['', 'the default (you in your own space, else the bot)'], ['person', 'you'], ['bot', 'the provider\'s bot']] },
    { path: 'membersAsBot', label: 'Team members may work as the bot', type: 'bool' },
    { path: 'protection', label: 'A base branch without protection', type: 'select', options: [['warn', 'warn'], ['refuse', 'refuse']] },
    { path: 'workflows', label: 'Tasks may change .github/workflows', type: 'bool' },
  ] },
  { key: 'ci', title: 'CI and reviews', fields: [
    { path: 'ci.autoFix', label: 'Tell a task when its CI fails', type: 'bool' },
    { path: 'ci.maxPerDay', label: 'CI fixes a task gets a day', type: 'number', min: 0 },
    { path: 'ci.delaySec', label: 'Wait for the other checks (s)', type: 'number', min: 0 },
    { path: 'ci.logBytes', label: 'Log a fix sees (bytes)', type: 'number', min: 0 },
    { path: 'reviews.forward', label: 'Review comments that reach the task', type: 'select', options: [['trusted', 'from people with access'], ['all', 'all'], ['off', 'none']] },
    { path: 'reviews.allow', label: 'Also forward from', type: 'lines', hint: 'logins, one per line' },
    { path: 'reviews.batchSec', label: 'Gather review comments for (s)', type: 'number', min: 0 },
    { path: 'autoLabel', label: 'An issue with this label wakes the coordinator', type: 'text' },
  ] },
  { key: 'setup', title: 'Workspace, ports and setup', fields: [
    { path: 'checkout', label: 'A task\'s checkout', type: 'select', options: [['worktree', 'a git worktree'], ['clone', 'a clone sharing the objects']] },
    { path: 'setupTimeoutSec', label: 'Setup timeout (s)', type: 'number', min: 1 },
    { path: 'setupBlocking', label: 'A task waits for its setup', type: 'bool' },
    { path: 'ports.base', label: 'First port', type: 'number', min: 1024, max: 65535 },
    { path: 'ports.span', label: 'Ports per task', type: 'number', min: 1 },
    { path: 'ports.slots', label: 'Port ranges', type: 'number', min: 1 },
    { path: 'fetchEveryMin', label: 'Fetch every (min)', type: 'number', min: 1 },
  ] },
  { key: 'big', title: 'Big tasks', fields: [
    { path: 'bigTasks.mode', label: 'A big task\'s sandbox', type: 'select', options: [['fork', 'forked from the project\'s'], ['fresh', 'a fresh one']] },
    { path: 'bigTasks.keepFork', label: 'Keep its sandbox after cleanup', type: 'bool' },
  ] },
  { key: 'cleanup', title: 'Cleanup', fields: [
    { path: 'cleanup.onMerge', label: 'Clean up when its pull request merges', type: 'bool' },
    { path: 'cleanup.onClose', label: 'Clean up when it closes', type: 'bool' },
  ] },
  { key: 'coord', title: 'The coordinator', fields: [
    { path: 'coordinator.web', label: 'May search and read the web', type: 'bool' },
    { path: 'coordinator.model', label: 'Its model', type: 'text', hint: 'empty: the agent\'s default' },
  ] },
];

/** policyGet(policy, 'ci.autoFix'): a key's value. */
export function policyGet(p, path) {
  let v = p;
  for (const k of String(path).split('.')) v = v == null ? undefined : v[k];
  return v;
}

/** policySet(policy, path, value): a copy with the key set (every other key kept, unknown ones too). */
export function policySet(p, path, value) {
  const keys = String(path).split('.');
  const out = { ...(p || {}) };
  let o = out;
  for (const k of keys.slice(0, -1)) { o[k] = { ...(o[k] && typeof o[k] === 'object' ? o[k] : {}) }; o = o[k]; }
  o[keys[keys.length - 1]] = value;
  return out;
}

/** fieldValue(field, raw): what a field's input means (a number, a bool, a list). */
export function fieldValue(f, raw) {
  if (f.type === 'bool') return !!raw;
  if (f.type === 'number') { const n = Number(raw); return Number.isFinite(n) ? Math.trunc(n) : 0; }
  if (f.type === 'lines') return String(raw ?? '').split('\n').map((s) => s.trim()).filter(Boolean);
  return String(raw ?? '');
}

/** fieldText(field, value): a field's value as its input shows it. */
export const fieldText = (f, v) => (f.type === 'lines' ? (Array.isArray(v) ? v.join('\n') : '') : v == null ? '' : String(v));
