// native/harness-all.js — the native view's feature modules for coding
// harnesses (D147 §8 U2–U8). native.js imports this file
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
import './harness-ask.js';

// U5 terminals and sign-in
import './terminal.js'; // the Terminal and Sign in screens, the sign-in notice, composer button and menu items

// U6 child cards
import './harness-child.js'; // a coding agent the agent started, as its card in the parent's chat (block); Cancel in its own chat's menu

// U7 the Coding agents board
import './harness-board.js'; // the Coding agents screen (sections), its toolbar button and ⋯ item, Message, the Task screen's Delegated

// U8 managers
import './harness-catalog.js';

// D179 saved sign-ins
import './harness-signins.js'; // Coding-agent sign-ins (a person's own): list, rename, default, paste a key or token, Forget

// CI in the conversation
import './ci.js'; // the Coding agents screen's CI sections, ci-job, ci-annotations, Watch CI for…, the child's CI words, the board's, outcome cards
