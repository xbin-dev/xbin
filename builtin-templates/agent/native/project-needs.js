// native/project-needs.js — "Waiting for you" atop a project's native screen
// (#56), the web's project-needs.js with the app's primitives: a section,
// first on the screen, while something in the project waits on you — a row
// per task question, approval, sign-in (danger) or failure (warn) and your
// coordinator's own question: who asks, what, how long ago; Open (a tap)
// opens the conversation it waits in. The words are
// model/project-needs.js's.
//
//   needsSectionTpl(pv)   the section, or nothing
import { html, nothing, repeat } from '/vendor/xb-native.js';
import { ctx } from './ui.js';
import { projectNeeds, needRows } from '../model/project-needs.js';

const TONE = { bad: 'danger', warn: 'warn' };
const ICON = { question: 'question', approval: 'shield', action: 'shield', login: 'key', project: 'warning', failed: 'warning', unfinished: 'pause' };

/** needsSectionTpl(pv): the section (read the first time it is drawn). */
export function needsSectionTpl(pv) {
  const app = ctx.app;
  const rows = needRows(projectNeeds(app).ensure(pv.id).items);
  if (!rows.length) return nothing;
  return html`<section title="Waiting for you" badge=${String(rows.length)}>
    ${repeat(rows, (r) => r.key, (r) => html`<row title=${[r.who, r.title].filter(Boolean).join(' ')}
      subtitle=${[r.text ? `${r.words}: ${r.text}` : r.words, r.age].filter(Boolean).join(' · ')}
      icon=${ICON[r.reason] || 'question'} badge="Open" tone=${TONE[r.tone]} nav @tap=${() => app.select(r.run)}/>`)}
  </section>`;
}
