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

// REASON: why a conversation is in "Needs you" (GET /needs items[].reason).
export const REASON = { question: 'has a question for you', approval: 'wants your approval', failed: 'failed' };
