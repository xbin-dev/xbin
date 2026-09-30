// model/harness-manage.js — the Coding agents catalog for the tile's
// managers (D147 §4.3.10): each coding agent GET /harnesses
// lists — whether it could be started and why not, the sandbox managers and
// images that have it, the sandboxes it was found or signed in on, the
// classes that allow it, its modes and sign-in command — and the running
// sandboxes that can be checked now (?probe=). Both views draw it: the web's
// Coding agents tab (harness-catalog.js), the app's screen
// (native/harness-catalog.js). Pure: no DOM, no lit.
import { monogram, nameOf, whyNot } from './harness.js';
import { splitRef, ago, EGRESS } from './sandboxes.js';
import * as classes from './classes.js';

const modeName = (h, id) => (id ? (h.modes || []).find((m) => m.id === id)?.name || id : '');
const yesNo = (v, yes, no, unknown) => (v === true ? yes : v === false ? no : unknown);

/**
 * catalogRows: one row per coding agent — {id, name, mono, available, why,
 * images: [{label, advertised}], sandboxes: [{ref, name, label, tone}],
 * classes: [label], modes: {default, auto, approve, plan, explicit: [name]},
 * login, options: [name]}. cat: model/harness.js catalogOf; list: GET
 * /sandboxes (model/sandboxes.js listOf, names the sandboxes); state: GET
 * /classes (model/classes.js listOf, names the classes).
 */
export function catalogRows(cat, list, state, now = Date.now()) {
  const boxes = (list && list.sandboxes) || [];
  return ((cat && cat.harnesses) || []).map((h) => ({
    id: h.id, name: nameOf(h), mono: monogram(h.id), available: !!h.available, why: h.available ? '' : whyNot(h),
    images: (h.images || []).map((i) => ({
      advertised: i.advertised !== false,
      label: `${i.manager || i.provider} · ${i.image}${i.advertised === false ? ' (its manager doesn\'t say — a check decides)' : ''} — ${
        (i.egress || []).length ? i.egress.map((e) => EGRESS[e] || e).join(', ') : 'no network (egress none only)'}`,
    })),
    sandboxes: Object.entries(h.sandboxes || {}).map(([ref, seen]) => {
      const s = boxes.find((x) => x.ref === ref);
      const name = (s && s.name) || splitRef(ref).id;
      const words = [yesNo(seen.installed, 'installed', 'missing', 'not checked'), yesNo(seen.signedIn, 'signed in', 'not signed in', '')].filter(Boolean);
      return { ref, name, label: `${words.join(' · ')}${seen.at ? ` · ${ago(seen.at, now)}` : ''}`,
        tone: seen.installed === false ? 'bad' : seen.signedIn === true ? 'ok' : seen.signedIn === false ? 'warn' : '' };
    }),
    classes: (h.classes || []).map((id) => { const c = classes.find(state, id); return c ? classes.label(c) : id; }),
    modes: {
      default: modeName(h, h.defaultMode), approve: modeName(h, h.approveMode), plan: modeName(h, h.planMode),
      auto: h.autoMode ? modeName(h, h.autoMode) : '',
      explicit: (h.modes || []).filter((m) => m.explicit).map((m) => m.name || m.id),
    },
    login: (h.login && h.login.command) || '',
    options: (h.options || []).map((o) => o.name || o.id),
  }));
}

// probeTargets: the sandboxes a manager can check now — running ones they
// may use ({ref, name}); a probe never starts a stopped one.
export const probeTargets = (list) => ((list && list.sandboxes) || [])
  .filter((s) => s.state === 'running' && s.canUse !== false).map((s) => ({ ref: s.ref, name: s.name || splitRef(s.ref).id }));

// modesWords: a row's modes in a line — "Ask before acting · Auto: Accept edits · Plan: Plan · explicit only: Bypass permissions".
export function modesWords(r) {
  const m = r.modes;
  return [m.default ? `default ${m.default}` : '', m.auto ? `Auto: ${m.auto}` : 'no auto mode', m.plan && m.plan !== m.default ? `plan: ${m.plan}` : '',
    m.explicit.length ? `the owner only: ${m.explicit.join(', ')}` : ''].filter(Boolean).join(' · ');
}
