# Elements (components)

A **component** is a directory. It becomes visible to xbin when it contains
an `index.html` (a view), a `xbin.json` (a manifest), or both. Its
workspace-relative path *is* its identity — `mv` renames it, `cp -r` forks
it, `rm -r` deletes it. There is no registry beyond the filesystem.

```
apps/thing/
  xbin.json      # manifest (optional for pure-static components)
  index.html      # view, rendered in <bx-frame>
  backend/        # backend entry (runtime-specific, see below)
  API.md          # required when exposing roles (see §API contract)
  deps/           # managed symlinks to declared dependencies (don't edit)
  ...anything     # it's just a directory
```

Reserved names you cannot use: component id `xbin`; top-level dirs
`vendor`, `data`, `home`, `.xbin`. Dirs named `deps`, `node_modules`,
`.git`, or starting with `.` are never scanned or watched.

## Manifest — `xbin.json`

JSONC (comments and trailing commas allowed). Everything is optional.

```jsonc
{
  // Backend runtime: "static" (default, no backend), go, node, python, cgi.
  "runtime": "go",

  // Backend entry. Defaults: go "./backend" (a package), node
  // "backend/server.js", python "backend/server.py", cgi "backend/handler".
  "entry": "./backend",

  // Source-level dependencies: materialized as deps/<basename> symlinks so
  // a shell here can read/edit them. Editing-plane only — this grants no
  // runtime call rights (that's "uses").
  "deps": ["lib/ui-kit"],

  // Extra system/runtime deps this component's BACKEND needs beyond the base
  // rootfs (go/node/python + tools) — a freeform shell script run once at build
  // time to populate a cached environment layer (under isolation, with
  // fuse-overlayfs). Rebuilt only when this changes; the running backend stacks
  // the result read-only. Runs with net:internet in a sandbox. Update the system
  // and pin/verify what you install (see AGENTS.md). E.g. give the backend Ruby:
  "setup": "apt-get update && apt-get install -y --no-install-recommends ruby && gem install --no-document sinatra",

  // Keep the backend running (optional, default false): started at boot and
  // whenever it becomes runnable, never idle-reaped, restarted after an exit
  // (1 s backoff doubling to 5 min; the crash-loop breaker still stops it).
  // For a tile that holds a connection open — a chat adapter's socket — and
  // so never gets the inbound request that would start it. Disable the tile
  // to stop it.
  "alwaysOn": false,

  // Run the backend in a VM sandbox — a Firecracker microVM, root in its own
  // kernel (optional, default false; docs/isolation.md §VM sandboxes): true,
  // or {"memory": "1G", "vcpus": 2}, capped by the workspace's VM policy.
  // Needs --isolate, an admin who enabled VM backends, and KVM (without it,
  // the VM is emulated: the same, but several times slower); otherwise the
  // backend fails with the reason (never a silent fallback). Not with "setup".
  "vm": false,

  // Runtime call rights this component wants (docs/auth.md). Targets are
  // component paths, resources ("res:<scope>/<name>"), reserved capabilities
  // ("cap:open-links" — links in new tabs from the frontend; "cap:net-admin",
  // "cap:containers" — admin-only), or — under isolation (xbind --isolate) —
  // GPUs ("gpu:all", "gpu:<index>", or "gpu:<uuid>"). All are owner-approved
  // grants. (Network egress is NOT a use — it is a "net" interface the owner
  // binds, below.)
  "uses": [
    { "target": "apps/calendar",         "role": "reader" },
    { "target": "res:apps/thing/db",     "role": "writer" },
    { "target": "cap:open-links",        "role": "writer" },
    { "target": "gpu:0",                 "role": "egress" }
  ],

  // Typed capability wiring (docs/overview/11-interfaces.md). "interfaces" are slots this
  // component REQUESTS; the owner binds each to a provider (a builtin or a tile).
  // This is how a sandboxed backend gets network egress — with nothing bound it
  // has zero IP egress ("internet" never covers LAN/RFC1918). "provides" are
  // slots it offers others (e.g. a firewall/VPN tile provides a "net" interface
  // that other components route their egress through). Kinds: net (L3 egress;
  // bind to "internet"/"host"/"lan:<cidr>"/a filtered
  // "internet:<host|ip|cidr>[:port][,…]" (egress restricted to the named
  // destinations, hostnames DNS-pinned — D35)/a provider tile), http (a service
  // endpoint, "service": "<contract>"), stream (a raw TCP dependency — bind
  // to a sibling's exposed stream slot, "provider#slot"; injected as
  // XBIN_IFACE_<slot>_ADDR), lan-ingress (an inbound link into a router/VPN
  // tile's subnet; injected as XBIN_IFACE_<slot>_IP), and — provide-side —
  // ingress (an HTTP ingress terminator tile, docs/ingress.md).
  //
  // Multiplicity (http only): a REQUEST slot with "multi": true explicitly
  // accepts a SET of bindings — the backend gets XBIN_IFACE_<slot> as a JSON
  // array [{provider, instance?, url, service}] and xbin.iface(slot) returns
  // {service, multi, endpoints} instead of one url. A PROVIDE with
  // "instances": true is a template: the provider registers its concrete
  // instances at runtime — PUT /api/xbin/iface-instances {"instances":
  // {"<id>": "/m/1"}} with PROVIDER-RELATIVE path prefixes (xbind composes
  // /api/<provider>+path; absolute "/api/<self>/…" is rejected 400). Each
  // instance binds as provider#id — to multi AND plain slots alike (an
  // instance presents itself like any provider, so instance-unaware tiles
  // connect to one unchanged; the injected URL routes into the provider's
  // own API at that sub-path, e.g. per-account routes "GET /m/{acct}/…").
  //
  // Injected shapes, exactly:
  //   backend, single slot:  XBIN_IFACE_<SLOT>_URL   = "http://xbin/api/<prov><path>"
  //                          XBIN_IFACE_<SLOT>_INSTANCE = "<id>" (when instanced)
  //   backend, multi slot:   XBIN_IFACE_<SLOT> = JSON [{provider, instance?,
  //                          url: "http://xbin/api/…", service}]
  //   frontend (both):       xbin.iface(slot) urls are same-origin PATHS
  //                          ("/api/…") — fetch them directly.
  // "http://xbin" is the gateway pseudo-host (backends dial the unix socket).
  // Rebinding a slot (or an instance re-registration) RESTARTS the requester
  // backend — the env is captured at spawn.
  "interfaces": { "net": { "kind": "net" },
                  "channels": { "kind": "http", "service": "comm", "multi": true } },
  "provides":   { "egress": { "kind": "net" },
                  "email":  { "kind": "http", "service": "comm", "role": "writer",
                              "instances": true } },

  // Endpoints offered to the OUTSIDE world (docs/ingress.md). Declaring is
  // inert — the endpoint becomes publicly reachable only when the owner
  // binds the slot to an ingress source (`bx expose`, or admin → interfaces
  // → ingress). http: "paths" is the public allowlist (default-deny; "/*" =
  // everything; anonymous callers arrive as X-XBin-From: ingress). stream:
  // the backend just net.Listen()s on "port" inside its sandbox; binding
  // relays a host port (tcp or udp) into it.
  "exposes": {
    "web":  { "kind": "http",   "paths": ["/", "/api/public/*"] },
    "game": { "kind": "stream", "proto": "udp", "port": 2456 }
  },

  // The callable surface this component offers to others. (Not "exposes"
  // above — that publishes ports; this declares the ROLES another tile may
  // be granted. A backend guard on a role missing here is a 403 nobody
  // can grant past; the builtin tiles are tested for exactly that.)
  "expose": {
    "roles": {
      // name → human description. Descriptions are REQUIRED — they render
      // in the grant-approval UI and `bx api`.
      "reader": "Read thing data",
      "writer": "Modify thing data"
    },
    // Optional, for custom role names: which roles a role includes.
    // reader/writer/admin ordering is built in (admin ⊃ writer ⊃ reader).
    "implies": { "auditor": ["reader"] }
  },

  // Set false to serve this component's HTML byte-exact, skipping the
  // standard <head> injection. You lose the import map, xbin-client.js, and
  // the frame token — so the frontend has NO identity at all (element APIs
  // 401/403 to it; the document is still sandbox-confined unless chrome).
  // Escape hatch for machine-targeted HTML; leave it alone normally. Under
  // strict tile asset gating's tokens mode (docs/auth.md) such a document
  // gets no asset <base> either, so none of its files load — see
  // §Asset URLs below.
  "inject": true,

  // The tile's native app UI (optional): the module the xbin app runs to
  // draw this tile natively instead of showing its web page, as a path
  // relative to the tile. Default: native.js when that file exists, so most
  // tiles never set this; false opts out of that convention. Old xbinds
  // ignore the key, and a value that is neither a path nor a boolean is
  // ignored too (never a manifest error). See §Native app UI below.
  "native": "./native.js",

  // TRUSTED CHROME (host-set only): run this component's frames UNSANDBOXED,
  // so its frontend keeps the ambient session cookie and acts as the
  // signed-in human (like the shell itself — tiles/organisations works this
  // way). Without it, frames are sandboxed opaque origins and the frame
  // token is the tile's only credential (docs/auth.md §Who is calling). Setting this is
  // trusting the component with your session; it can only be set by editing
  // the manifest on the host, never via the create APIs or grants.
  "chrome": false,

  // Marks this component a TEMPLATE — a blueprint, not a live tile. It runs
  // no backend and isn't openable; you instantiate it into an independent
  // named copy (Tile Manager → "New from template", or `bx template new`),
  // which copies the files and strips this block. See docs/overview/03-components.md §Templates.
  "template": {
    "title": "AI Agent",
    "description": "A blank-slate agentic loop you clone and build up.",
    "defaultName": "agent"   // suggested instance basename (under apps/)
  }
}
```

Manifest errors don't take the workspace down: the component keeps serving
statically, the parse error shows in `bx ls` / `bx doctor` /
`/api/xbin/components`.

## Views

`GET /c/<component>/` serves the component directory (`index.html` for the
dir itself, correct MIME for everything, `Cache-Control: no-store` — it's a
live system). HTML gets exactly one transform on the way out: xbind injects
into `<head>`:

- the merged **import map** (workspace `xbin.json` `importMap` + scope
  overrides) — so `import { LitElement } from 'lit'` works with no build step
- `<meta name="xbin-component">` and a short-lived frame token (minted only
  when a human or the tile itself loads the document — another tile that
  fetches your page gets it with an empty token)
- `<script type="module" src="/vendor/xbin-client.js">` — the in-frame API
  (`xbin.self`, `xbin.fetch`, `xbin.bus`; see [sdk.md](/docs/sdk.md))
- only when the xbin app requested the document (`X-XBin-Client:
  app/<version>`): `<meta name="xbin-ws-origin" content="wss://<host>">` —
  the app loads pages from its own URL scheme, which has no WebSocket origin
  to derive, so `xbin.ws` and the event stream connect there instead.
  Browsers never get it; their documents are unchanged.

Write your view as a plain HTML document. **Reference your own files with
relative URLs** (`src="app.js"`, `href="style.css"`, CSS `url(img/x.png)`) —
see §Asset URLs below. Vendored libraries: `lit` via the import map,
anything else you drop into your own component dir.

**Isolation (ND8).** Unless your component is trusted chrome, its document
runs in a **sandboxed opaque origin** (iframe `sandbox` + CSP header, also
on direct-tab opens): no parent/sibling DOM access, no `localStorage`/
IndexedDB/cookies, and no ambient session cookie on requests — the frame
token is your only credential, and `xbin.fetch`/`xbin.ws` carry it (that's
why raw `fetch` to other elements 403s *and* can't impersonate the user).
Your own static assets load with a credential the workspace attaches for
you when you use relative URLs (§Asset URLs). Keep per-session state in JS
memory; put durable state in your backend (prefs/kv); route any genuinely
cross-origin API calls through your backend (tile fetches go out as
`Origin: null`, cookie-free). The postMessage bridge (dialogs, windows,
auto-height) works as before. **File downloads work** (the sandbox carries
`allow-downloads`, ND10): hand data to the browser with
`xbin.download(filename, data)` — or stream straight from your backend with
`<a href="${xbin.url('/api/'+xbin.self+'/export')}" download>` and a
`Content-Disposition: attachment` response; build the URL at click time
(the embedded token is short-lived). Trigger downloads from a user gesture —
browsers throttle unprompted ones. **Links in new tabs** (`target="_blank"`,
`window.open`) need the `cap:open-links` grant (ND11): declare it in `uses`,
an admin approves it, and your frame's sandbox gains
`allow-popups allow-popups-to-escape-sandbox`; without it such links are
silently dropped (the console says which grant). Use `rel="noopener"`.
Top navigation is never allowed.

### Asset URLs

Every load of a tile's files is authorized for the **user** the browser acts
for: they may load a tile's HTML, JS, CSS, images, fonts and data only if
they can read that tile ([auth.md §Tile asset gating](/docs/auth.md)). How
the credential rides along depends on the workspace's `--tile-assets` mode,
and one habit works in all of them — **relative URLs**:

- **Relative URLs always work** — `<script type="module" src="app.js">`,
  `<link href="style.css">`, `<img src="img/logo.png">`, `srcset`, CSS
  `url()`/`@import`, `import './lib.js'`, `import('./lazy.js')`,
  `new URL('data.json', import.meta.url)`, `fetch('data.json')`. Under the
  `tokens` mode the injection adds `<base href="/c/~<asset-token>/<tile>/<dir>/">`
  so they carry a path-scoped asset token, and whatever they load resolves
  under the same prefix. Another tile's files load relatively too
  (`../../lib/ui/button.js`) when the user can read that tile.
- **Absolute `/c/<tile>/…` URLs in markup and CSS don't** under `tokens`
  (`<img src="/c/apps/me/x.png">` carries no credential and fails; xbin-client
  logs why in the console). Absolute **module imports of your own tile** keep
  working (the import map remaps `/c/<tile>/`), as do workspace import-map
  entries. Under `origins` absolute URLs work too, but relative is the
  portable form.
- **`bx fix assets <tile>`** rewrites the absolute `/c/` URLs in your HTML,
  CSS, import maps and module imports to relative ones (dry run first,
  `--write` applies); `bx doctor` and `GET /api/xbin/tile-assets` list what
  remains. Relative URLs resolve to the same file in every mode, so the
  rewrite changes nothing today.
- **With the asset `<base>`** (tokens mode) xbin-client keeps the page
  behaving as if there were none: `href="#section"` scrolls, a relative link
  to another page of yours (`href="page2.html"`) navigates to it, and
  `history.pushState`/`replaceState` resolve relative URLs against the
  document URL. Setting `location.href = 'page2.html'` yourself would ask
  the asset plane for a document — navigate with an absolute path via
  `location.assign(xbin.url('/c/' + xbin.self + '/page2.html'))` instead. A
  document that sets its own `<base>` keeps it (ours then points at the
  same place, token-bearing).
- **`inject: false`** documents get no injection and so no asset `<base>`:
  under `tokens` none of their files load (use `origins`, or drop the flag).
- **`origins` mode** gives your tile its own origin: `fetch('/api/<you>/x')`
  works without `xbin.fetch` there (the tile cookie is your credential), and
  your tile gets its own `localStorage`/IndexedDB. `location.origin` is then
  the tile's origin, not the workspace's: a link built from it to a
  workspace page (`/login?invite=…`) or another tile is redirected to the
  workspace, but a link you hand to someone else is best built from a
  workspace-relative path. Your pages can be framed only by the workspace
  and by your own tile (`frame-ancestors`); your backend never sees the tile
  cookie and can't set cookies there (`Set-Cookie` is dropped). A
  sub-directory holding its own `index.html` is a component of its own and
  so gets its own origin: navigating your frame to it (a relative link)
  still works.
- **In every mode**, a file that isn't `.html`/`.htm` (any case) is served
  with `Content-Security-Policy: sandbox`: as a subresource nothing changes,
  but opened directly or framed (`<iframe>`, `<object>`) an SVG's or an
  `.xhtml` file's scripts don't run. Ship an interactive page as `.html`.

`legacy` (today's default) still loads absolute self-references
credential-less; the next release removes that path
([compat.md](/docs/compat.md)).

**Sizing.** A view is framed inside a fixed-size card on the shell's snappable
grid (the user drags to size it, down to ~192px; content scrolls inside — it
can't stretch the card) and can also be opened full page.
Design to be usable when **narrow** and to **reflow, never scroll
horizontally**: relative units, flexbox/grid, `max-width:100%` on media, and
wrap inherently wide content (tables, code, diagrams) in its own
`overflow-x:auto` container so the view body never overflows sideways.
Horizontal scroll on a tile is a bug — avoid it at all cost.

### `<bx-frame>`

```html
<bx-frame src="apps/thing"></bx-frame>            <!-- auto-height -->
<bx-frame src="apps/thing" height="420px"></bx-frame>
<bx-frame src="apps/thing" no-edit></bx-frame>
```

- **Auto-height**: the framed document reports its size via xbin-client
  (with hysteresis, so no resize loops). Set `height` for a fixed frame.
- **Edit button**: 7×7 px, top-right, 35 % opacity until hover. Opens a
  **floating terminal window** with a shell cwd'd to `src`: it appears at
  the frame's corner and keeps its position *relative to the frame* — it
  follows the frame through scrolls and drags (D66); drags by its title bar,
  resizes by the bottom-right handle, and stacks above everything (click
  brings to front). A host may set the element's `popBounds` (a function
  returning a viewport rect) to fence the window in — the shell hands the
  canvas's tile extent, so a terminal never leaves the scroll area; the
  frame fires `bx-pop` when the window opens, moves, resizes or closes and
  exposes `popBox()` (its viewport box, `null` while closed).
  Ctrl+scroll inside adjusts the font size (remembered across terminals).
  Multiple terminal tabs per window; sessions persist server-side when you
  close it — reopening reattaches with scrollback, from any browser you are
  signed into (the session directory, D73: the frame asks
  `GET /api/xbin/term/sessions?cwd=` for its tabs, remembers nothing itself,
  and follows `term` events so a tab opened elsewhere appears here).
- **Agent tab**: the `+ Agent` button opens an **agent session** (D74) instead
  of a shell — a coding agent (Claude Code, Codex, Gemini, OpenCode) running
  in this tile's sandbox, driven over the Agent Client Protocol. The tab
  shows the transcript, tool cards, the plan, and a permission card any
  attached client can answer (first answer wins); the same session is
  reachable from another browser or from `bx agent`
  (docs/overview/09-terminals.md §Agent sessions). A tab whose session
  ended keeps its transcript, greyed, until you dismiss it.
- **The bar degrades, never clips**: when the window is narrow (below
  ~640 px, or the phone sheet) or the full bar measures wider than the
  window (it varies by host and tab: a GPU picker, the VM toggle, long tab
  names), the path and the network picker's label shorten first, then the
  layout switcher and the pickers move into a tools row behind `⋯`. Tabs
  keep a legible width; a tab strip that still overflows scrolls (the
  wheel scrolls it sideways) and keeps the active tab in view, so every
  tab and the window's `✕` stay reachable. The pickers ask before
  restarting a live session, since the network scope, GPU and API token
  are fixed at spawn. A window restored open (a reload, another browser)
  loads the same pickers, VM toggle and base-update state as one opened by
  hand.
- **Live reload**: the most specific mounted frame for a changed path
  reloads — editing `apps/cal/widgets/month` reloads that frame, not the
  whole `apps/cal` frame, when both are mounted.
- **Build errors** render as an overlay with compiler output; cleared by the
  next successful build.

Frames nest. The root page is itself a component full of frames; you can
frame the root inside the root if you enjoy that sort of thing.

### Native app UI (`native.js`)

Every tile works in the xbin app as its web page. A tile may also ship a
**native UI entry** — `native.js` next to its `xbin.json`, or the path the
manifest's `"native"` names — a module the app runs to draw the tile with
native controls; anything the app can't show that way falls back to the web
page. How to write one — the template API, every primitive, the rules — is
[native.md](/docs/native.md). What xbind does for it:

- **Discovery.** `GET /api/xbin/components` lists `native: {entry}` (the
  tile-relative module path) for each tile with one, and `GET
  /api/xbin/whoami` says `native: {runtime: 1}` — this xbind serves runtime
  documents. Trusted chrome (`chrome: true`, root, shell) never has a native
  UI: it acts as the signed-in human, so the app opens it in the browser.
- **The runtime document.** `GET /c/<tile>/?native=1` is generated by xbind,
  not a file: the same `<head>` injection as the tile's page (import map,
  identity, frame token, sandbox, interfaces, `xbin-client.js` — even under
  `inject: false`, since there are no tile bytes to keep), plus `<meta
  name="xbin-native" content="1">` and one module script that imports
  `/vendor/xb-native.js` and then your entry, relative to the tile directory
  — so the entry imports the same `./model.js` your `index.html` does, and
  `window.xbin` is all there. It is authorized, sandboxed and headed exactly
  like the tile's `index.html`, and carries a frame token under the same
  rule (never for another tile fetching it); a tile with no usable entry
  gets a 404 that says why. `&preview=1` adds `<meta
  name="xbin-native-preview" content="1">` and loads the preview host
  (`/vendor/xb/preview-host.js`) before the entry, for drawing in a
  browser.
- **The workspace switch.** An admin can turn native UIs off for the whole
  workspace — the admin console's *workspace → xbin app* tab, or `PUT
  /api/xbin/native-runtime {"enabled": false}`. `whoami` then says `native:
  {runtime: 0, disabled: true}`, the app opens every tile as its web page,
  and `?native=1` answers `410` with the reason; `&preview=1` (and so `bx
  native tree` / `bx preview --native` / `bx lint --native`) keeps working,
  so you can fix what the switch is covering for. Tiles already open in the
  app stay as they are until reopened; open apps are told by the `native`
  event.
- **Entry paths** are `.js`/`.mjs` modules inside the tile; each segment uses
  letters, digits and `. _ ~ + @ -`, with no `..` or hidden (`.name`)
  segments. A declared entry that is invalid or missing means no native UI.
- **Live reload.** Editing the entry reloads the tile (the usual `reload`
  event — web frames and native views alike) **without restarting its
  backend**, since no backend reads it. The exception is the undeclared
  `native.js` convention under a `node` backend, or a `go` backend built
  from the tile root (`"entry": "."`), which could import or embed the file:
  those keep restarting on every edit, as before. Declare `"native":
  "./native.js"` to opt out of the restart. Any other file, including
  modules the entry shares with `index.html`, restarts the backend as
  always.
- **Checking it.** `bx native tree <tile>` prints what the entry renders,
  `bx preview --native <tile> --out shot.png` draws it with the reference
  renderer, and `bx lint --native` checks every native tile and reports the
  workspace's native coverage ([bx.md §Native UIs](/docs/bx.md)).

## Dialogs & windows

A tile is an iframe, so any modal or window it renders itself is **clipped to
its card**. To float over the whole workspace, a tile asks the **shell** to
spawn it. Two APIs on the in-frame `xbin` global (see [sdk.md](/docs/sdk.md)):
(The shell's own context menus are `<bx-menu>`, served at
`/vendor/bx-menu.js` — shell chrome with action closures, not a tile API;
tiles keep using the two calls below. Which `/vendor/` modules *are* for
tiles — `bx-kit`, `bx-dialog`, `bx-multiselect`, `bx-netrules`, `bx-allow`,
`bx-code`, `events-socket`, `theme.css` — is listed in
[frontend-kit.md](/docs/frontend-kit.md). Right-clicking selected text in a
tile carries the text to the shell, whose tile menu leads with **Copy**;
inputs and editable text keep the native menu.)

- **`xbin.dialog(spec) → Promise<{button, values}>`** — a modal the shell
  renders from a plain-data `spec` (`title`, `message`, `error`, `fields`,
  `buttons`; `error` shows as a red alert box — re-open the dialog with it
  set to say why a submit failed).
  It is **data-only**: strings are shown as text, never HTML, so a tile can't
  inject markup or script into workspace chrome. Resolves with the clicked
  button's `value` (`null` when dismissed via Escape / backdrop / Cancel) and
  the field `values`. Good for confirm / prompt / a few inputs. Falls back to
  an in-frame `<bx-dialog>` when the tile isn't inside the shell.
- **`xbin.window(spec) → { id, close(), closed }`** — a floating, draggable
  window whose body is a real tile frame. By default it frames a **sub-path of
  the calling component** (`spec.path`, e.g. `"compose"` → `/c/<self>/compose/`),
  so the window runs *your own* UI and, being an ordinary tile document, has its
  own `xbin` client and talks to your backend with the usual `xbin.fetch`
  ([auth.md](/docs/auth.md) — it's the same component identity). `spec.src`
  frames a full component path instead, subject to the same tile-access RBAC as
  any `<bx-frame>`. The window and the card are separate documents (no shared JS
  memory); coordinate through the shared backend / bus / kv. `closed` resolves
  when the window closes.

**Permissions & trust.** Spawning UI is a tile affordance, not a privileged
capability — no grant is required — but four things bound it:

- **Verified origin**: the sender iframe *is* the identity, so a request is
  always attributed to the calling component; a tile can't spawn on another's
  behalf. Every **dialog shows its originating component**, so a tile can't
  pass its modal off as system/owner chrome (anti-phishing).
- **Data-only dialogs**: the shell renders from a plain spec (text, never
  HTML) — no markup/script from a tile runs in workspace chrome.
- **Windows are sandboxed sub-frames**, not tile markup in the top page. A
  sub-path is traversal-stripped; `spec.src` to another component runs the
  normal **frame-token / `CanUseTile`** check (§auth), so it grants nothing
  beyond embedding a `<bx-frame>` the tile could already place itself.
- **Rate-bounded**: one dialog and a handful of windows per tile at a time, so
  a misbehaving tile can't carpet-bomb modals or windows.

What is deliberately *impossible*: `xbin.window({html})` (tile HTML in the top
page = privilege escalation), spoofing another component's identity, and
framing a component the user may not use.

**In the xbin app** (iOS) a tile page is a top-level WebView rather than a
frame, and the app stands in for the shell: `xbin.dialog` opens a native
sheet from the same data spec (attributed to the tile, one at a time) and
`xbin.window` pushes a screen framing the same sub-path or component, with the
same traversal stripping and caps. Nothing changes in tile code; a tile page
without the app keeps the in-frame fallbacks above.

## Status & notifications

A tile tells the workspace how it's doing over a small self-scoped channel; the
shell renders it as a colour on the tile's sidebar entry (breathing for
`warn`/`error`), a tint on the screen tab, and the browser-tab title.

- **`xbin.status(level, message)`** — set a **persistent, self-clearing**
  condition. `level` ∈ `ok | info | warn | error`. `ok` with an empty message
  **clears** it (or use `xbin.clearStatus()`); `ok` with a message shows a
  healthy dot. Sticky until you change it — clear it when the condition passes.
- **`xbin.notify(level, message)`** — a **one-shot** notification (toast) that
  fades; does not change the persistent status.
- Backend equivalents: `xbin.Status` / `xbin.ClearStatus` / `xbin.Notify`
  (SDK). Both planes POST `/api/xbin/tile-report` ([protocol.md](/docs/protocol.md)).

Status is per-component and self-reported (the owner sees all; other users only
tiles they can read), and it **resets when your backend restarts**. Keep
messages to a short headline. Full guidelines — when to use which level, and the
“always clear it” rule — are in the workspace `AGENTS.md`.

## Runtimes & backend lifecycle

Backends serve plain HTTP on a unix socket xbind hands them
(`XBIN_SOCKET`). xbind routes `ANY /api/<component>/<path>` to them,
stripping the prefix (your handler sees `/<path>`).

| runtime | entry default | change behavior |
|---------|---------------|-----------------|
| `go` | `./backend` package | `go build` (workspace go.work, shared cache) → new process → health check → atomic swap → old gets SIGTERM, 30 s drain |
| `node` | `backend/server.js` | restart-on-change (same swap dance, no compile) |
| `python` | `backend/server.py` | restart-on-change |
| `cgi` | `backend/handler` (executable) | executed per request, CGI/1.1; nothing to restart |

Lifecycle facts that matter when writing backends:

- **Lazy start**: nothing runs until the first request (or first save).
- **Blue/green**: in-flight requests finish on the old generation. Requests
  during a rebuild wait for the new one (never connection-refused). A failed
  build keeps the old generation serving.
- **Statelessness pays**: a swap is a new process — keep state in resources
  (kv, sqlite), not memory. Long-lived connections (WS/SSE) to an old
  generation are killed at the 30 s drain deadline; reconnect.
- **Idle reaping**: ~30 min without requests → the process is stopped; the
  next request restarts it (~100–300 ms). Need periodic work? Use a `cron`
  resource, not a sleeping goroutine. A backend that must hold an outbound
  connection (a chat adapter) sets `"alwaysOn": true` instead: it starts at
  boot, is never reaped, and is restarted after an exit.
- **Crash loops**: 3 quick exits → marked failed (overlay + `bx status`)
  until you save a change. Logs: `bx logs -f <component>`,
  or `tail -f $XBIN_WORKSPACE/.xbin/log/<key>.log`.
- **Graceful stop**: handle SIGTERM ([sdk.md](/docs/sdk.md) `xbin.Serve`
  does).

Env every backend instance gets:

| Env | Meaning |
|-----|---------|
| `XBIN_SOCKET` | unix socket to listen on |
| `XBIN_COMPONENT` | own path (identity) |
| `XBIN_GATEWAY`, `XBIN_TOKEN` | how to call other elements / xbin APIs (this generation's credential — dies at swap) |
| `XBIN_RES_<NAME>` | each granted resource ([resources.md](/docs/resources.md)) |

Nothing else of xbind's own environment reaches a sandboxed backend: it gets
the rootfs `PATH` and only the locale (`LANG`, `LC_*`), `TZ` and proxy
(`HTTP(S)_PROXY`, `NO_PROXY`, `ALL_PROXY`) variables of the daemon. Put
configuration a backend needs in its manifest, a resource or the vault, not in
xbind's environment.

## Scopes

A `scope.json` marks a directory as a **scope** — an app boundary:

```jsonc
// apps/thing/scope.json
{
  "resources": { "db": { "type": "sqlite" }, "bus": { "type": "bus" } },
  "importMap": { "thing-ui": "/c/apps/thing/ui/lib.js" }   // merged over workspace map
}
```

Components inside a scope get **auto-approved** grants to that scope's
resources and to each other (declaring in `uses` is still required — the
manifest documents the graph). Everything cross-scope needs one-time owner
approval. Rule of thumb: one scope = one app = one trust unit.

## API contract (the docs standard)

If your component sets `expose`, ship an **`API.md`** in its root. This is a
convention with teeth: `bx doctor` warns when it's missing, `bx api <path>`
and the grants UI render it, and it's how the next builder (human or agent)
integrates with you in minutes. Standard shape (scaffolded by
`bx new --expose`):

1. One-paragraph overview.
2. **Roles** table mirroring the manifest descriptions.
3. **Endpoints**: method, path, minimum role, request/response example.
4. **Bus topics** you publish, if any.
5. **Use it**: a copy-paste `uses` snippet.

Keep it truthful over pretty — it's a contract, not marketing.

## Cross-component code access (editing plane)

- Children are just subdirectories; a shell in the parent sees them.
- `deps` in the manifest materializes `deps/<name>` symlinks (relative, so
  the workspace stays relocatable). Follow them, edit through them, build
  against them.
- Go: xbind maintains a generated `go.work` at the workspace root listing
  every Go component and the xbin SDK — any shell can `go build`/`gopls`
  across the whole workspace. If you hand-edit `go.work`, remove the
  generated-marker line and xbind will leave it alone.
