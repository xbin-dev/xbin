/**
 * agent-slash.js — slash-command completion for the Agent tab's composer
 * (D77), pure (node-tested: hack/agent-slash.test.mjs). The agent advertises
 * its commands (ACP available_commands_update → `commands` on status
 * events); typing "/" at the start of the composer offers them. A command
 * goes to the agent as plain prompt text — ACP's own model, the agent parses
 * "/name args" — so there is no protocol of ours here.
 */

// slashQuery: the command name being typed ("/rev" → "rev", "/" → ""), or
// null when the draft is not a bare slash command in progress.
export function slashQuery(draft) {
  const m = /^\/([\w:.-]*)$/.exec(String(draft ?? ''));
  return m ? m[1].toLowerCase() : null;
}

// matchCommands: prefix matches first (in the agent's order), then names or
// descriptions containing the query; at most max.
export function matchCommands(cmds, q, max = 8) {
  const list = Array.isArray(cmds) ? cmds.filter((c) => c && c.name) : [];
  const query = String(q ?? '').toLowerCase();
  const pre = list.filter((c) => c.name.toLowerCase().startsWith(query));
  const sub = query ? list.filter((c) => !pre.includes(c) && (c.name.toLowerCase().includes(query) || String(c.description || '').toLowerCase().includes(query))) : [];
  return pre.concat(sub).slice(0, max);
}

// commandHint: what to type after a completed "/name " whose command takes
// input (its hint), while nothing is typed yet.
export function commandHint(cmds, draft) {
  const m = /^\/([\w:.-]+)\s+$/.exec(String(draft ?? ''));
  if (!m) return null;
  const c = (Array.isArray(cmds) ? cmds : []).find((x) => x && x.name === m[1]);
  return c && c.hint ? { name: c.name, hint: c.hint } : null;
}
