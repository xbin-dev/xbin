# Views & the shell

Every piece of UI on screen is a component's own document in an iframe —
including the workspace shell that arranges them. This chapter covers the
no-build frontend doctrine, the single sanctioned HTML transform that wires
a view into the workspace, the `window.xbin` in-frame API, `<bx-frame>`, and
the shell chrome (sidebar, screens, grants panel, per-tile admin).

**Related:** [03-components.md](03-components.md) (what a view belongs to),
[05-identity.md](05-identity.md) (frame tokens), [09-terminals.md](09-terminals.md)
(the frame's terminal window), [07-users-orgs.md](07-users-orgs.md) (who sees
which tiles) · [/docs/elements.md](/docs/elements.md) §Views,
[/docs/sdk.md](/docs/sdk.md) §In-frame JS API · plans/DECISIONS.md (D4).

## The no-build doctrine

There is **no JS build step, ever**. Views are plain ES modules resolved
through browser import maps; the few third-party deps (lit, xterm, marked)
are vendored as single files and served — together with xbin's own core
elements (`bx-frame`, `bx-terminal`, …) — under one stable `/vendor/` root.
TypeScript syntax is banned from core web code.

Why this is a load-bearing decision and not an aesthetic one: the workspace
is edited *live*, by humans in terminals and by agents, and every
transformation between source and screen is a thing that can drift, break,
or need a toolchain inside the sandbox. With zero build, "edit the shell's
CSS" is `vim shell/bx-shell.js` and a reload — for a human or an agent, in
any component, with nothing installed. Cross-component code reuse works the
same way: import another component's modules by URL (`/c/lib/ui-kit/…`) or
put it in `deps` for a stable relative path
([03-components.md](03-components.md)).

## Serving views: `/c/` and the one transform (D4)

`GET /c/<component>/<file>` serves component files byte-exact — except HTML,
which gets the **single sanctioned transform**: an injection into `<head>`
(nothing else is ever rewritten). Exactly four things are added:

| injected | purpose |
|----------|---------|
| `<script type="importmap">` | the workspace import map overlaid with the component's scope map (scope wins) — how `import 'lit'` resolves |
| `<meta name="xbin-component">` | the document's verified identity (its component path) |
| `<meta name="xbin-frame-token">` | a short-lived (15 min) HMAC token binding *this component + this signed-in human*; minted only if the caller may read the tile |
| `<meta name="xbin-interfaces">` (when bound) + `<script src="/vendor/xbin-client.js">` | resolved http-interface endpoints, and the client module that turns all of the above into `window.xbin` |

Serving is gated by tile-level RBAC: a user loads only tiles they can
*read* (D16); the chrome components (`root`, `shell`) are always loadable —
the shell itself then only shows tiles the user may see. Everything is
`Cache-Control: no-store`: this is a live system.

`"inject": false` in the manifest opts a component out — its HTML is served
byte-exact. The cost is the whole contract: no import map, no `window.xbin`,
and no frame-token attribution, so requests from that document carry only
the login cookie and act as the *human* — admins pass (as admin, so the
component's own grants are never exercised), non-admin users get 403 on any
element API. An escape hatch for fully self-contained pages, not a default.

## The in-frame API: `window.xbin`

`xbin-client.js` exposes one frozen global (the full surface — verified
against the module):

| member | what it does |
|--------|--------------|
| `xbin.self` | this component's path (from the injected meta) |
| `xbin.fetch(url, opts)` | `fetch` with the frame token attached — **required for calling any other element's API**; streams (SSE/chunked) work |
| `xbin.ws(path)` | attributed WebSocket — browsers can't set WS headers, so the token rides a `?frame=` query param that xbind *consumes* (never forwarded to the callee) |
| `xbin.iface(slot)` | a bound http interface: `{url, service}` (or `{service, multi, endpoints}` for a `multi:true` slot) — call a typed, swappable dependency instead of a hard-coded path ([11-interfaces.md](11-interfaces.md)) |
| `xbin.bus.on(prefix, cb)` / `xbin.bus.publish(res, topic, data)` | pub/sub on granted bus resources ([10-resources.md](10-resources.md)) |
| `xbin.events.on(cb)` | the raw event stream (reload / build / bus), over a frame-token-authenticated WS |
| `xbin.dialog(spec)` | a shell-rendered trusted modal → `Promise<{button, values}>` (in-frame fallback when standalone) |
| `xbin.window(spec)` | a floating top-level workspace window framing one of your own sub-paths (or, via `spec.src`, another component — subject to normal tile RBAC) |

The client also auto-refreshes the frame token every 10 minutes and reports
the document's height to the embedding frame (with hysteresis) so
auto-sized tiles work.

**Why raw `fetch` is wrong for cross-element calls:** attribution *is* the
security model. The cookie proves the human; the injected token attributes
the request to *the component whose document made it* — that pair is what
xbind turns into the element principal with the element's own grants
([05-identity.md](05-identity.md)). A raw `fetch` to a sibling API carries
only the cookie: for a non-admin user it simply 403s; for an admin it
"works" — but as the admin, meaning the component's declared grants are
never exercised and the missing `uses` entry surfaces only when someone
else opens the tile. Use `xbin.fetch` for anything beyond your own API.

## `<bx-frame>`: a tile is a document

`<bx-frame src="apps/calendar">` renders the component's `index.html` in a
same-origin iframe and is the mechanism behind every card, floating window,
and embedded panel:

- **Identity by construction.** For messages from the framed document, the
  *sender window is the identity*: bx-frame only trusts messages from its
  own iframe's `contentWindow` and, when relaying dialog/window requests up
  to the shell, stamps them with its own `src` — a tile cannot spoof another
  component's requests no matter what it posts.
- **Auto-height** from the client's resize reports (clamped, hysteresis to
  prevent feedback loops); or a fixed `height`.
- **Live reload, precisely targeted.** On a `reload` event the *most
  specific* mounted frame wins — a change in `apps/cal/widgets/x` reloads
  the widget's frame, not the whole calendar (longest-`src`-prefix over the
  registry of mounted frames).
- **Build errors as an overlay** — compiler output rendered over the tile on
  `build-error`, cleared on `build-ok` ([03-components.md](03-components.md)).
- **Grant changes reload the frame**, so a frontend that was 403ing retries
  against its new permissions without a manual refresh.
- **The edit button** (the 7×7 corner dot; the shell renders its own header
  button instead) opens the floating per-tile work window: tabbed terminal
  sessions plus layout modes — terminal (`>_`), code browser/review (`{ }`),
  split (`⇋`), and a read-only backend-logs view (`▤`). Session state
  (including window geometry) persists in the browser and reattaches to the
  still-running server-side PTYs across reloads. Per-session pickers for
  network scope, live tile-API access, and GPU each restart the session —
  those properties are fixed at sandbox spawn. The whole terminal plane —
  what those sessions can see and do — is [09-terminals.md](09-terminals.md).

## The shell: chrome is just components

The root page (`root/index.html`) is a thin document composing
`<bx-shell>`; the shell implementation lives in the workspace's own
`shell/` component. There is **no privileged chrome** — open a terminal on
`shell/` and restyle the workspace live. The `<bx-frame>` children of
`<bx-shell>` only *seed* a user's first screen; after that the per-user
layout, saved server-side through the prefs API (so it follows you across
browsers), is the source of truth.

What the shell provides:

- **Screens & cards** — named tabs, each an independent set of tiles in
  draggable columns; unpin a card into a floating window; all persisted
  per user.
- **The sidebar** — the openable-tile list from `GET /api/xbin/components`,
  which the server already filters to the caller's *readable* tiles (chrome
  excepted) — visibility is enforced server-side, the shell just renders
  it. Templates (blueprints) and offloaded tiles are hidden; offloaded
  tiles are also auto-closed from open screens. View-only folders organize
  the list without moving anything on disk.
- **The grants panel** (`<bx-grants>`) — pending `uses` requests with the
  callee's role descriptions and one-click approve; rows a policy ceiling
  blocks are greyed with the blocking row named; renders nothing when
  there's nothing to decide.
- **Per-tile ⚙ mini-admin** (`<bx-tile-admin>`) — lifecycle, access,
  backups for one tile. Shown to workspace admins and — for tiles inside
  their org — org admins ([07-users-orgs.md](07-users-orgs.md)). It uses
  *raw* fetch deliberately: the popover is workspace chrome acting as the
  signed-in human, never as a tile — handing a non-admin a tile's frame
  token would hand them that tile's capabilities (D21).
- **Orgs & teams popover** (`<bx-org-admin>`) — the same chrome-plane
  surface for org admins to manage members, teams, and per-tile access.
- **Dialog/window service** — shell-rendered modals and pop-out tile
  windows spawned on behalf of tiles via the verified relay above.
- The **Welcome tile** (`apps/welcome`) seeds the first screen alongside
  the Tile Manager, API docs, and admin console.

## The admin tile is just a tile

`tiles/admin` has no special code path in xbind. Its power is a grant — the
reserved target `xbin` at role `admin` — pre-approved in the workspace
template's manifest and revocable like any other
(`bx grant --revoke tiles/admin xbin:admin` disarms every panel; the
request reappears in the grants panel). The Tile Manager similarly holds
`xbin:writer` for component creation. This is the self-hosting rule applied
to administration: management UI is workspace code you can read, edit, and
strip of capability ([06-authorization.md](06-authorization.md)).

## The live loop

One event stream (`/ws/events`, shared per page; per-frame streams
authenticate with the frame token) drives the whole editing UX:

```
save ─▶ watcher ─▶ hub ──▶ reload ──────▶ most specific frame reloads
                     ├──▶ build-error ─▶ red overlay on the tile
                     ├──▶ build-ok ────▶ overlay clears
                     ├──▶ grants ──────▶ affected frame reloads;
                     │                   grants panel & admin views refetch
                     └──▶ bus ─────────▶ app-level pub/sub (xbin.bus)
```

Bus events are authorization-filtered per subscriber; everything else is
workspace-plane housekeeping. The effect: edit any component — view, backend,
or the shell itself — and the running workspace converges on the change
within a second, with failures rendered where you're looking.
