// home.js — the home view (no conversation open): the greeting and example
// asks an instance customises (HOME, model/home.js), and what is waiting for
// you (GET /needs): a question, an approval, an automation that failed.
import { html, nothing } from '/vendor/lit-all.min.js';
import { REASON } from './model/home.js';

/**
 * @param HOME   the words (model/home.js)
 * @param needs  GET /needs items
 * @param ui     {pick(example), select(id), mcpBound}
 */
export function homeTpl(HOME, needs, ui) {
  return html`<div class="home">
    <div class="hi">${HOME.hi}</div>
    <div class="sub">${HOME.sub}${ui.mcpBound ? '' : ' No MCP servers are bound yet — see ⚙ → MCP.'}</div>
    <div class="exs">${HOME.examples.map((e) => html`<span class="ex" @click=${() => ui.pick(e)}>${e}</span>`)}</div>
    ${needs && needs.length ? html`<h5>Needs you</h5>${needs.map((n) => html`
      <div class="qa need" data-r=${n.run.id} @click=${() => ui.select(n.subRun || n.run.id)}>
        <div class="q">${n.reason === 'failed' ? '⚠' : '❓'} ${n.run.title || 'run ' + n.run.id}
          <span class="when">${REASON[n.reason] || n.reason}</span></div>
      </div>`)}` : nothing}
  </div>`;
}
