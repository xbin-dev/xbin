/**
 * agent-slash.js — slash-command completion for the Agent tab's composer
 * (D77), at the URL it always had. The rule is the chat kit's now
 * (/vendor/chat/ask.js, D224 — names that start with what is typed first,
 * then names or descriptions containing it, at most eight); this module
 * re-exports it under its names (hack/agent-slash.test.mjs and the Swift
 * port's parity fixtures hold them).
 */
export { slashQuery, matchCommands, commandHint } from './chat/ask.js';
