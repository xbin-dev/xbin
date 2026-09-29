// harness-web.js — the web's feature modules for coding harnesses
// (D-harness §8 U2–U8). agent.js imports this file once; each
// module registers its hooks on the web's seams (web-ext.js) when imported,
// so a feature lands as a new file plus one import line here — agent.js,
// chat-cards.js and the other hot files stay as they are.
//
// One import per line, each in its own slot below (parallel branches merge
// without touching each other's lines).

// U2 start a conversation

// U3 the transcript
import './harness-cards.js';

// U4 asking and controls

// U5 terminals and sign-in

// U6 child cards

// U7 the Coding agents board

// U8 managers
