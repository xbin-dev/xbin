// model/mode.js — is this instance a person's workspace agent? Asked by both
// entries before they load a view: boot.js (the page: ws/page.js or
// agent.js) and native.js (the app's view: native/ws.js or native/main.js).
// Pure but for the one read; no lit, no DOM (API.md §Workspace mode; D231,
// D233, D235).

/**
 * pageMode(fetch, self): 'workspace' when this page is a sealed component's
 * (self xbin/<name>) and its backend says it is a workspace agent, else
 * 'normal' — any other path (nothing is asked), a 404, an answer without
 * the mode. Anything else (the backend starting, a dropped connection) is
 * asked again, a few times, before the normal page is loaded as before.
 */
export async function pageMode(fetchFn = (u) => globalThis.xbin.fetch(u), self = globalThis.xbin?.self ?? '', wait = (ms) => new Promise((r) => setTimeout(r, ms))) {
  if (!/^xbin\/[^/]+$/.test(self)) return 'normal';
  for (let i = 0; i < 4; i++) {
    try {
      const r = await fetchFn(`/api/${self}/ws/state`);
      if (r.status === 404) return 'normal';
      if (r.ok) {
        let d = null;
        try { d = JSON.parse(await r.text()); } catch { d = null; }
        return d && d.mode === 'workspace' ? 'workspace' : 'normal';
      }
    } catch { /* asked again below */ }
    await wait(250 * 3 ** i);
  }
  return 'normal';
}
