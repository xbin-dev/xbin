// harness-web.js — the web's feature modules for coding harnesses
// (D-harness §8 U2–U8). agent.js imports this file once; each
// module registers its hooks on the web's seams (web-ext.js) when imported,
// so a feature lands as a new file plus one import line here — agent.js,
// chat-cards.js and the other hot files stay as they are.
//
// One import per line, each in its own slot below (parallel branches merge
// without touching each other's lines).

// U2 start a conversation
import './harness-start.js';

// U3 the transcript
import './harness-cards.js';

// U4 asking and controls
import './harness-ask.js';
import './harness-controls.js';

// U5 terminals and sign-in
import './signin.js'; // the sign-in card (end, a login park only); the dock is terminals.js, which sandboxes.js imports

// U6 child cards
import './harness-child.js'; // a coding agent the agent started, as its card in the parent's chat (ext.block)

// U7 the Coding agents board

// U8 managers (the Coding agents tab: agent.js imports harness-catalog.js)
