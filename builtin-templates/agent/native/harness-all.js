// native/harness-all.js — the native view's feature modules for coding
// harnesses (D-harness §8 U2–U8). native.js imports this file
// once; each module registers its hooks on the native seams (native/ext.js)
// when imported, so a feature lands as a new file plus one import line
// here — native.js and native/chat.js stay as they are.
//
// One import per line, each in its own slot below (parallel branches merge
// without touching each other's lines).

// U2 start a conversation
import './harness-start.js';

// U3 the transcript
import './harness-cards.js';

// U4 asking and controls

// U5 terminals and sign-in

// U6 child cards

// U7 the Coding agents board

// U8 managers
import './harness-catalog.js';
