# Tile frontend isolation (sandboxed opaque origins) — 2026-08-04

## What changed

Tile frontends no longer share the browser's origin powers. Before, every
tile ran as a plain same-origin iframe: its JS could read the shell's DOM,
use origin storage, and — critically — call any API with the signed-in
human's ambient session cookie (frame tokens made that *attributable*, not
*prevented*).

Now, every non-chrome tile:

- is framed by `<bx-frame>` with
  `sandbox="allow-scripts allow-forms allow-modals"` (never
  `allow-same-origin`) **and** served with the equivalent
  `Content-Security-Policy: sandbox …` header, so even opening `/c/<tile>/`
  directly in a tab runs it in an **opaque origin** — no parent/sibling DOM
  access, no `localStorage`/IndexedDB/`document.cookie`, no service workers;
- carries **no ambient credentials**: Chromium strips cookies from sandboxed
  frames, `bx-frame` adds `credentialless` where supported, and everywhere
  else xbind's new **Fetch-Metadata gate** drops the session cookie from any
  request showing the opaque-origin fingerprint (`Sec-Fetch-Site: cross-site`
  on non-navigations, or non-GET navigations to `/api/*`/`/ws/*`);
- authenticates with its **frame token alone** — `xbin.fetch`/`xbin.ws`
  attach it as before, and token renewal (`GET /api/xbin/frame-token`) now
  works cookie-less for the tile's own component;
- loads its static files (`/c/<tile>/…` JS/CSS/images/fonts/media)
  credential-less via the **opaque-origin fingerprint**: sandboxed frames
  strip cookies *and* the Referer (verified in Chromium), so a GET with
  `Sec-Fetch-Site: cross-site` (or `same-site`) and a subresource
  `Sec-Fetch-Dest` (script/style/image/font/media/worker — never documents,
  frames, fetch, or `.html`) is authorized without credentials. Tile JS
  can't forge that from a sandbox — but headers are client-settable, so a
  determined non-browser client can spoof it to read tile source. Another
  reason secrets belong in the vault, never in source;
- reaches APIs via `fetch()` at all because xbind answers
  `Access-Control-Allow-Origin: null` (+ preflight) for `Origin: null`
  requests — opaque-origin fetches are otherwise CORS-blocked wholesale.
  Safe because tile requests carry no ambient credentials.

Trusted **chrome** — `root`, `shell`, and components with `"chrome": true`
in xbin.json — is exempt: it runs unsandboxed and keeps acting as the
signed-in human. The flag is host-set only (edit the manifest on disk; the
create APIs never write it) and can never be acquired via grants.

## Who's affected

- **Tiles using `localStorage`/`sessionStorage`/IndexedDB/`document.cookie`
  in their frontend** — these APIs are dead inside sandboxed frames
  (`SecurityError`). No shipped tile used them.
- **Tiles that raw-`fetch` `/api/xbin/*` or another tile's API expecting to
  act as the signed-in human** (no frame token) — those calls now 401. The
  shipped `tiles/organisations` did this by design and became chrome
  (`"chrome": true`).
- **Tiles embedding `<bx-terminal>` or otherwise opening terminals from
  their frontend** — terminals are human-plane and now enforceably so; the
  cookie-less frame gets 401.
- **Tiles framing other components with their own `<iframe src="/c/…">` or
  `<bx-frame>`** — the nested frame can only authenticate for the framing
  tile's own component. Use `xbin.window({src})` instead; the shell creates
  that frame with proper authority.
- Backends, terminals, `bx`, and the SDKs are unaffected. The postMessage
  bridge (`xbin.dialog`, `xbin.window`, height reporting) works unchanged —
  `targetOrigin` is now `*` on both sides because an opaque origin matches
  no origin string; identity stays `event.source`-verified.

## How to migrate

- **State in the frontend** → move it to your backend (prefs/kv via the
  SDK) and reach it with `xbin.fetch`, or keep per-session state in JS
  memory only.
- **Acting as the human** → don't. Element frontends act as the element;
  ask for the grants you need (`uses`) so `xbin.fetch` covers your calls.
  If the component genuinely *is* workspace chrome (a management UI that
  must see the world as the signed-in user), have the workspace owner add
  `"chrome": true` to its xbin.json — and treat that component as
  trusted as the shell itself.
- **Terminals/code browsing from a tile UI** → use the shell's surfaces
  (the frame's edit button / `bx-shell` chrome), which run unsandboxed.
- **Cross-origin fetches from your tile** (to a genuinely different origin)
  now originate from an opaque origin (`Origin: null`, no cookies) — if you
  call external APIs from the frontend, expect CORS behavior to change;
  route those calls through your backend instead (also keeps keys out of
  the browser).

## Why

"Attribution, not isolation" was the documented weak spot of the browser
plane: any tile's JS could spend the human's cookie on any API, and read
the DOM of the shell that framed it. The new stack (sandbox + CSP +
Fetch-Metadata gate + Referer asset rule) makes the grant system enforced
rather than advisory for tile frontends, on a single origin, with no
subdomains or extra ports — the constraints `http://127.0.0.1` development
and single-domain deployment impose. The remaining gap is a shared renderer
*process* (a browser exploit, not tile JS); closing that still requires
separate origins and stays on the roadmap. See plans/auth.md §6 and
plans/DECISIONS.md ND8.
