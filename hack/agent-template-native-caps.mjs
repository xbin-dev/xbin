// hack/agent-template-native-caps.mjs — the app capabilities the agent
// template's native tests play (hack/xbn/node.mjs runNative's `caps`).
//
// PHONE: an app with everything but the collapsing split of vocabulary rev 2
// (D189) — the native view's one nav of the list and its stack (native.js
// layout), which the tests walk screen by screen (a nav's `pop` depth counts
// from the list). FULL: everything — the list and the stack in a `split`
// (an iPad's two columns, a phone's collapsed stack). OLD: an app of
// vocabulary rev 1 throughout (no anchors, no submenus, no split detail).
// CHAT1: PHONE with a transcript of rev 1 — no anchors or edges: the window
// never lets the live end go and there is no "jump to latest" (D130's rev-1
// path, which hears `scrolled`).
import { fullCaps } from '../web/xb/vocab.js';

export const FULL = fullCaps();
export const PHONE = (() => { const c = fullCaps(); c.prims.split = 1; return c; })();
export const OLD = (() => { const c = fullCaps(); for (const k of Object.keys(c.prims)) c.prims[k] = 1; return c; })();
export const CHAT1 = (() => { const c = fullCaps(); c.prims.split = 1; c.prims.transcript = 1; return c; })();
