// agents.js — an org's constraints on agents in the organisations tile
// (D229, docs/auth.md §Agent constraints): the card an org's admin edits
// their org's rules in — the workspace agent, the coding agents an agent
// session on its tiles may run, the models that may see their data. The
// editor is xbind's own (<bx-org-agents>, /vendor/org-agents.js): it shows
// what a workspace admin pinned on the org, which these rules can narrow
// and never loosen, and the workspace's bounds beside each rule. The tile
// is chrome, so a save is the signed-in admin's own act — the only way an
// org's admin can make it (docs/protocol/orgs.md).
import { html } from 'lit';
import '/vendor/org-agents.js';

// agentsView(org, saved): the card for one org the viewer manages; `saved`
// runs after a save (the tile reloads its orgs).
export function agentsView(o, saved) {
  return html`<div class="card" data-agents=${o.id}>
    <b>agents</b>
    <span class="muted hint">(what agents may do on this org's tiles — and the tiles shared into it)</span>
    <bx-org-agents style="margin-top:8px" org=${o.id} .rules=${o.agents ?? null} .pin=${o.agentsPin ?? null}
      @org-agents-saved=${() => saved?.()}></bx-org-agents>
  </div>`;
}
