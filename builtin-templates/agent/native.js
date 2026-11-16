// native.js — the agent tile's native entry: what the xbin app loads to draw
// the tile with platform UI instead of its web page (API.md "The frontend").
// It only decides which view this instance has, as boot.js does for the web
// page, and loads that one — nothing of the other:
//
//   native/main.js  the template's own view (the conversation list, the
//                   chat, Automations, Projects, …) — every instance
//   native/ws.js    a person's workspace agent (API.md §Workspace mode;
//                   D233): the sealed component xbin/wsagent, whose backend
//                   says so (GET /ws/state → mode "workspace", model/mode.js)
//
// A normal instance is asked nothing: its path is not a sealed component's.
// A sealed component draws a line while its backend answers (it may be
// starting), so the app has a tree at once.
import { html, render } from '/vendor/xb-native.js';
import { pageMode } from './model/mode.js';

const sealed = /^xbin\/[^/]+$/.test(globalThis.xbin?.self ?? '');
if (sealed) render(html`<screen title="Workspace agent" style="scroll"><progress label="Starting your agent…"/></screen>`);
const mode = sealed ? await pageMode() : 'normal';
const view = mode === 'workspace' ? await import('./native/ws.js') : await import('./native/main.js');

/** The view's model (the template's app; a workspace agent's store). */
export const app = view.app;
