// web/deploy-branch.js — branch-assigned deployments in the terminal window
// (D131, feature `branches/1`; docs/tile-deployments.md §Assigned branches):
// pure functions from the deployments state to what the window shows of a
// deployment's branch and the work tree's — the words when they differ, the
// offers to follow a switch, the add form's Branch control, the overview's
// Branch row and its dialog, the confirmations' Branch line, a mismatch's
// "use it this time" question, and the terminals' grey line for op branch.
//
// A leaf: it imports nothing, so web/deploy-state.js (the chip, the menu,
// the launcher, the confirmations) and web/deploy-panel.js (the panel) both
// build on it, and hack/deploy-branch.test.mjs runs it under node. Who may
// do what stays the server's: an offer carries the permission its caller
// (`ctl(op, deployment)`, deploy-state.js's control) reads from the state.

export const FEATURE = 'branches/1';

// speaks(state): this xbind knows assigned branches — only then does a client
// send branch, newBranch or confirm:"other-branch" (bodies are strict).
export const speaks = (s) => Array.isArray(s?.features) && s.features.includes(FEATURE);

const dep = (s, name) => s?.deployments?.find((d) => d.name === name) || null;
const shown = (b) => b || 'no branch';

// facts(state, base) → the branch facts every surface reads, for the
// deployment live reload follows (or last followed): need (its branch, ''
// for none), other (the branch it takes this time), wtb (the work tree's
// branch; null when the state doesn't say), off (the work tree feeds it
// not), related (the deployment the work tree's branch is assigned to),
// switched (live reload was paused by a branch switch: xbind's own pause).
// base: deploy-state.js's {zero, reader, attached, last}.
export function facts(s, base) {
  const none = { need: '', other: '', wtb: null, off: false, related: '', switched: false };
  if (!s || base.zero || base.reader) return none;
  const cur = base.attached || base.last, d = dep(s, cur), last = dep(s, base.last)?.lastDeploy;
  const need = d?.branch || '', other = d?.branchOverride || '';
  const wtb = typeof s.workTree?.branch === 'string' ? s.workTree.branch : null;
  const off = !!need && wtb !== null && wtb !== need && !(other && wtb === other);
  const related = wtb ? (s.deployments || []).find((x) => x.name !== cur && x.branch === wtb)?.name || '' : '';
  return { need, other, wtb, off, related, switched: !base.attached && last?.how === 'pause' && last?.by === 'xbind' };
}

// pausedSentence(f, L, pin) → the chip's and the header's sentence while a
// branch switch holds live reload paused.
export function pausedSentence(f, L, pin) {
  if (f.off) return `Live reload paused — the work tree ${f.wtb ? `is on ${f.wtb}` : "isn't on a branch"}, and ${L} requires ${f.need}: ${L} keeps running ${pin}.`;
  return `Live reload paused when the work tree left ${f.need || `${L}'s branch`}; it is on ${f.wtb || f.need} again — resume live reload on ${L} to follow saves.`;
}

// offSentence(f, A) → while live reload still follows A with the work tree off
// its branch (a checkout that changed no file): what the next save does.
export const offSentence = (f, A) => (f.off ? ` The work tree is on ${shown(f.wtb)}, and ${A} requires ${f.need}: the next save pauses live reload.` : '');

// launcherText(f, L) → the launcher's banner while a branch switch paused it.
export const launcherText = (f, L) => (f.off ? `Live reload is paused: the work tree is on ${shown(f.wtb)}, and ${L} requires ${f.need}.`
  : `Live reload is paused: the work tree is on ${f.need} again — resume live reload on ${L}.`);

// offers(state, base, f, ctl) → the offers to follow a branch switch, as chip
// menu items ({label, enabled, hint, op, deployment, other?, branch?, title,
// offer: true}): the deployment the work tree's branch is assigned to (attach
// to it, or resume on it while paused); the one that left its branch, back on
// it (resume); with no deployment for the branch, keep the target on it this
// time (resume with confirm:"other-branch") or add a deployment for it.
export function offers(s, base, f, ctl) {
  // nothing to offer: no branch in play, or the target takes this one this time (the user chose it)
  if (!speaks(s) || f.wtb === null || (!f.need && !f.related) || (f.other && f.wtb === f.other)) return [];
  const out = [], C = base.attached || base.last, w = f.wtb;
  // attach is judged on its deployment; resume on the tile; an add for the branch as add
  const item = (label, op, name, title, x = {}) => {
    const c = op === 'attach' ? ctl('attach', name) : ctl(op === 'addFor' ? 'add' : op, null);
    out.push({ label, enabled: c.enabled, hint: c.why, op, deployment: name, title, offer: true, ...x });
  };
  if (f.related && f.related !== base.attached) {
    if (base.attached) item(`Attach live reload to ${f.related} (${w})`, 'attach', f.related, `${f.related} requires ${w}, the work tree's branch: saves reach it, and ${base.attached} is pinned to its current code.`);
    else item(`Resume live reload on ${f.related} (${w})`, 'resume', f.related, `${f.related} requires ${w}, the work tree's branch: it follows every save.`);
  }
  if (!base.attached && f.switched && f.need && !f.off) item(`Resume live reload on ${C}`, 'resume', C, `The work tree is on ${f.need} again: ${C} follows every save.`);
  if (f.off && w && !f.related) {
    if (!base.attached) item(`Keep ${C} on ${w} this time`, 'resume', C, `${C} follows the work tree on ${w} until live reload moves or the branch changes again; it still requires ${f.need}.`, { other: true });
    item(`Add a deployment for ${w}…`, 'addFor', '', `A new deployment that requires ${w}, live reload attached to it.`, { branch: w });
  }
  return out;
}

// offerId(offer) → the panel's action id of an offer: follow/<name> (attach
// to it, or resume on it), keep/<name> (this time), addFor/<branch>.
export const offerId = (it) => (it.op === 'addFor' ? `addFor/${it.branch}` : `${it.other ? 'keep' : 'follow'}/${it.deployment}`);

// branchLine(impact.branch, op) → a confirmation's Branch line: the
// deployment's branch and the work tree's, and "this time" when the request
// takes the work tree's.
export function branchLine(b, op) {
  if (!b?.assigned) return '';
  if (!b.other || b.workTree === b.assigned) return `Branch: ${b.deployment} requires ${b.assigned} — the work tree is on it.`;
  const lasting = op === 'resume' || op === 'attach' || op === 'add' ? ', until live reload moves or the work tree\'s branch changes again' : '';
  return `Branch: ${b.deployment} requires ${b.assigned}; it takes the work tree's ${shown(b.workTree)} this time${lasting}.`;
}

// row(state, impact, deployment, x) → the branch route's confirmation (the
// overview's Set branch… and Clear branch); x.branch null clears.
export function row(s, im, Y, x) {
  const b = x.branch, w = im?.branch?.workTree;
  if (!b) return { title: `Clear ${Y}'s branch?`, ok: 'Clear branch', send: () => ({}), lines: [`The work tree feeds ${Y} on any branch again.`] };
  return { title: `Assign ${Y} branch ${b}?`, ok: 'Assign branch', send: () => ({}), lines: [
    `${Y} requires ${b}: the work tree feeds it — saves while live reload follows it, attach, resume, Reload now, a deploy of the work tree — only while ${b} is checked out.`,
    typeof w === 'string' && w !== b ? `The work tree is on ${shown(w)} now${im?.pausesLiveReload ? `: live reload is on ${Y}, so the next save pauses it` : ''}.` : '',
    'Nothing is deployed now.'] };
}

// result(state, deployment) → the branch route's result line.
export const result = (s, X) => (dep(s, X)?.branch ? `${X} requires ${dep(s, X).branch}.` : `${X} takes the work tree on any branch.`);

// overviewLine(state, deployment) → the overview's Branch row, or null (the
// primary, main, an xbind without branches/1).
export function overviewLine(s, name) {
  const d = dep(s, name);
  if (!d || d.primary || name === 'main' || !speaks(s)) return null;
  if (!d.branch) return 'none — the work tree feeds it on any branch';
  return `${d.branch}${d.branchOverride ? ` · takes ${d.branchOverride} this time` : ''}`;
}

// actions(state, deployment, ctl) → the overview's Set branch… and Clear branch.
export function actions(s, name, ctl) {
  const d = dep(s, name);
  if (overviewLine(s, name) === null) return [];
  const c = ctl('branch', name), out = [{ id: 'branch', label: d.branch ? `Branch: ${d.branch}…` : 'Set branch…', enabled: c.enabled, why: c.why,
    title: `The work tree's branch ${name} requires: it feeds ${name} only on that branch.` }];
  if (d.branch) out.push({ id: 'clearBranch', label: 'Clear branch', enabled: c.enabled, why: c.why, title: `${name} takes the work tree on any branch again.` });
  return out;
}

// dialog(state, deployment, error) → the Set branch… form.
export function dialog(s, name, error) {
  const d = dep(s, name), w = typeof s?.workTree?.branch === 'string' ? s.workTree.branch : '';
  return { title: `The branch ${name} requires`, ...(error ? { error } : {}),
    message: `The work tree feeds ${name} only while it has this branch checked out.${w ? ` The work tree is on ${w}.` : ''}`,
    fields: [{ name: 'branch', label: 'Branch', value: d?.branch || w, placeholder: 'feature/x' }],
    buttons: [{ label: 'Cancel', value: null }, { label: 'Assign branch', value: 'ok', primary: true }] };
}

// nameOK(branch): the names xbind takes (checkpoint.BranchNameOK's rule).
export const nameOK = (b) => typeof b === 'string' && b.length > 0 && b.length <= 200 && b !== 'HEAD' && /^[A-Za-z0-9._+/-]+$/.test(b)
  && !/^[-.]|\.$|\.\.|\/\/|\/\.|\/$|\.lock$/.test(b);
export const BAD_NAME = 'A branch name is letters, digits and . _ + / -, not starting with - or ., with no "..".';

// addFields(state, preset) → the add form's Branch control and the new
// branch's name, when this xbind speaks branches/1; [] otherwise. preset:
// {pick: none|current|new, branch (the work tree's: current), newBranch}.
export function addFields(s, preset = {}) {
  if (!speaks(s)) return [];
  const w = typeof s.workTree?.branch === 'string' ? s.workTree.branch : '', opt = (value, label) => ({ value, label });
  const pickd = preset.pick || (preset.branch && preset.branch === w ? 'current' : preset.newBranch ? 'new' : 'none');
  return [{ name: 'branch', label: 'Branch', type: 'select', value: pickd,
    options: [opt('none', 'none — the work tree feeds it on any branch'), ...(w ? [opt('current', `current (${w}) — it requires ${w}`)] : []),
      opt('new', 'a new branch, created at HEAD and checked out')] },
  { name: 'newBranch', label: 'New branch name (with "a new branch")', value: preset.newBranch || '', placeholder: 'feature/x' }];
}

// readAdd(values, state) → {branch?, newBranch?} from the add form, or {error}.
export function readAdd(v, s) {
  const pickd = v?.branch || 'none', w = typeof s?.workTree?.branch === 'string' ? s.workTree.branch : '';
  if (pickd === 'current') return w ? { branch: w } : { error: "The work tree isn't on a branch." };
  if (pickd !== 'new') return {};
  const nb = String(v.newBranch || '').trim();
  if (!nb) return { error: 'Name the new branch.' };
  return nameOK(nb) ? { newBranch: nb } : { error: BAD_NAME };
}

// suggestName(branch) → a deployment name made from a branch ('' when none fits).
export function suggestName(b) {
  const n = String(b || '').toLowerCase().split('/').pop().replace(/[^a-z0-9-]+/g, '-').replace(/^[^a-z]+/, '').replace(/-+$/, '').slice(0, 24).replace(/-+$/, '');
  return n === 'main' ? '' : n;
}

// mismatch(error) → {deployment, assigned, workTree} for the 409 of an op on a
// work tree off the deployment's branch that confirm:"other-branch" takes; null otherwise.
export function mismatch(error) {
  const m = /^(\S+) is assigned branch (\S+), and the work tree is on (\S+): check out .* send confirm:"other-branch"/.exec(String(error || ''));
  return m ? { deployment: m[1], assigned: m[2], workTree: m[3] } : null;
}

// mismatchDialog(op, m, error) → the question a mismatch asks instead of a bare refusal.
export const mismatchDialog = (m, error) => ({ title: `${m.deployment} requires branch ${m.assigned}`,
  message: `${error}\n\nUse ${m.workTree} for ${m.deployment} this time? It lasts until live reload moves or the work tree's branch changes again; check out ${m.assigned} instead to keep ${m.deployment} on its branch.`,
  buttons: [{ label: 'Cancel', value: null }, { label: `Use ${m.workTree} this time`, value: 'ok', primary: true }] });

// notice(event, state, pinOf) → the grey line op branch prints in the tile's
// terminals, or null. pinOf(name): the checkpoint a deployment runs.
export function notice(ev, s, pinOf) {
  if (ev?.op !== 'branch') return null;
  const D = ev.deployment, w = ev.workTree, R = ev.related;
  if (ev.paused) {
    const next = R ? ` — ${R} requires ${w}: follow it from the live reload chip` : w ? ` — keep ${D} on ${w} this time, or add a deployment for it, from the live reload chip` : '';
    return `live reload paused — the work tree is on ${shown(w)}, and ${D} requires ${ev.assigned}; ${D} keeps running ${pinOf(D) || 'its code'}${next}`;
  }
  if (ev.assigned && w === ev.assigned && s && !s.liveReload) return `the work tree is on ${w} again, ${D}'s branch — resume live reload on ${D} from the live reload chip`;
  if (R) return `the work tree is on ${w}, which ${R} requires — ${s?.liveReload ? 'attach' : 'resume'} live reload on ${R} from the live reload chip`;
  return null;
}
