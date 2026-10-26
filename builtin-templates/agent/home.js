// home.js — the home view (no conversation open): the greeting and example
// asks an instance customises (HOME, model/home.js), and what is waiting for
// you (GET /needs): a question, an approval, a coding agent's sign-in, an
// automation that failed.
import { html, nothing } from '/vendor/lit-all.min.js';
import { needWords } from './model/home.js';

const NEED_ICON = { failed: 'warning', login: 'key', unfinished: 'pause' }; // the judge's mark (D198)

/**
 * @param HOME   the words (model/home.js)
 * @param needs  GET /needs items
 * @param ui     {pick(example), select(id), mcpBound, dismissUnfinished?(id)}
 */
export function homeTpl(HOME, needs, ui) {
  return html`<div class="home">
    <div class="hi">${HOME.hi}</div>
    <div class="sub">${HOME.sub}${ui.mcpBound ? '' : ' No MCP servers are bound yet — see Settings → MCP.'}</div>
    <div class="exs">${HOME.examples.map((e) => html`<span class="ex" @click=${() => ui.pick(e)}>${e}</span>`)}</div>
    ${needs && needs.length ? html`<h5>Needs you</h5>${needs.map((n) => html`
      <div class="qa need" data-r=${n.run.id} data-reason=${n.reason} @click=${() => ui.select(n.subRun || n.run.id)}>
        <div class="q"><bx-icon name=${NEED_ICON[n.reason] || 'question'}></bx-icon>${n.run.title || 'run ' + n.run.id}
          <span class="when">${needWords(n)}</span>
          ${n.reason === 'unfinished' && ui.dismissUnfinished ? html`<button class="btn ghost btnsm" title="take the mark down, for everyone"
            @click=${(e) => { e.stopPropagation(); ui.dismissUnfinished(n.run.id); }}>Mark as finished</button>` : nothing}</div>
      </div>`)}` : nothing}
  </div>`;
}
