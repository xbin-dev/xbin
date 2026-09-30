// model/home.js — the home view's words (no conversation open) and what the
// "Needs you" list says about each item (GET /needs). An instance that
// specializes the agent (a persona, a domain) changes HOME and nothing else;
// both views (agent.js on the web, a native view) read it from here.
export const HOME = {
  title: 'Agent',
  tagline: 'conversations · cron-agents',
  hi: 'What do you need?',
  sub: 'Ask below — every question starts a conversation of its own (yours, until you share it); recurring work becomes a cron-agent.',
  examples: [
    'What can you do in this workspace?',
    'Every morning at 8, check…',
    'Call apps/… and summarize what it returns',
  ],
  placeholder: 'ask anything…',
};

// REASON: why a conversation is in "Needs you" (GET /needs items[].reason);
// login: a coding agent in it waits for you to sign in (D-harness §4.3.9).
export const REASON = { question: 'has a question for you', approval: 'wants your approval', failed: 'failed', login: 'needs you to sign in' };

// needWords: an item's reason in words — a sign-in names the coding agent
// when the item says which (its own `harness`, or its conversation's when
// that is the one waiting): "needs you to sign in to Codex".
export function needWords(n) {
  if (n.reason !== 'login') return REASON[n.reason] || n.reason;
  const own = !n.subRun || n.subRun === (n.run && n.run.id);
  const h = n.harness || (own && n.run && n.run.harness) || null;
  const name = h && (h.name || h.provider);
  return name ? `${REASON.login} to ${name}` : `a coding agent ${REASON.login}`;
}
