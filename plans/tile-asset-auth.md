# Tile asset auth — strict per-user, per-tile gating of tile frontends

> Status: **live** — a proposal under review (Phase 0 of the native client,
> plans/native.md). Nothing here is built. It replaces a draft that kept an IP
> heuristic and a fallback window; the owner asked for strict gating, so no
> heuristic survives.

## The rule

A user may load a tile's frontend resources — its HTML, JS, CSS, images,
fonts, workers, data files, anything under `/c/<tile>/` — **only if that user
can read that tile**. Every such request:

1. carries a credential that proves (user, tile) — never an ambient signal;
2. is authorized against **live** RBAC (`CanReadTile`, the user not disabled,
   the credential's session still valid);
3. is refused otherwise (401/403), with no fallback.

No IP heuristics, no Fetch-Metadata heuristics. When the replacement ships, the
credential-less path is **deleted**, not kept behind a flag.

## What exists today (the hole)

`/c/` static loads are authorized in two places that share one rule:
`authedStatic` (`internal/server/server.go`) admits a credential-less request
when `tileSubresourceAuthed` holds, and `handleComponentStatic`
(`internal/server/static.go`) authorizes it with the same check:

- `tileSubresource(r)` (static.go): GET/HEAD, path not `.html`/`.htm`,
  `Sec-Fetch-Site` `cross-site` or `same-site`, `Sec-Fetch-Dest` in
  {script, style, image, font, audio, video, track, worker, manifest};
- **and** `RecentlyAuthed(clientIP)` (`internal/auth/auth.go`): *anyone*
  authenticated from that IP within the last hour (the warm set records no
  user).

Consequences:

- It checks the destination header, **not the file**: any non-HTML file in any
  tile — backend source, `xbin.json`, `API.md`, local data — is readable.
- Headers are client-settable: any client sharing an egress IP with any signed-in
  session (an office NAT, carrier-grade NAT on mobile, a VPN exit) can read any
  tile's files, including tiles its user has no access to. A lower-privilege
  signed-in user can read every tile's frontend and more.
- Docs promise it: docs/elements.md ("relative URLs work"; "your own static
  assets load credential-less … Fetch-Metadata fingerprint"), plans/auth.md §6
  (the "Honest scope" paragraph). Those promises change with this plan.

Every other path into `/c/` is credentialed: the session cookie (humans, chrome
and direct navigation), `?frame=`/`X-XBin-Frame-Token` (frame principals, whose
embedded user is clamped per tile), element principals with a `code[:owner]`
grant, and bearer tokens. `/vendor/` is public by design and holds no tile
files.

Who relies on the credential-less path today: every sandboxed tile's
subresources (module scripts, stylesheets, images, fonts) in every browser —
Chromium's `credentialless` iframes and opaque-origin documents send no cookie,
and WebKit/Firefox documents are opaque too (CSP `sandbox`). That includes the
app's WKWebViews unless they avoid it (mechanism C). `inject:false` tiles and
direct-tab opens (`window.open('/c/<tile>/')`) rely on it as well.

## Threat model

- a signed-in user reading tiles they have no access to;
- anyone sharing an egress IP with a signed-in user (NAT, CGNAT, VPN);
- non-browser clients forging Fetch-Metadata;
- one tile's frontend tag-loading another tile's assets — allowed only when the
  *user* may read that other tile (the user is who the browser is acting for).

## Mechanisms

### A — Per-tile origins (the target)

Each tile's frontend is served from **its own origin**:
`https://t-<id>.<tiles-domain>/c/<tile>/…`, where `<tiles-domain>` is a
subdomain of the workspace's own registrable domain (e.g. workspace at
`xbin.example.com`, tiles at `*.tiles.xbin.example.com`) and `<id>` is a
stable, non-reversible id of the tile path (keyed hash; tile names don't leak
into DNS or SNI).

- **Credential:** the shell points the tile iframe at
  `https://t-<id>…/c/<tile>/?frame=<token>`; xbind verifies the frame token,
  sets an `HttpOnly; Secure; SameSite=Strict; Path=/` cookie for that origin —
  bound to (user, tile, session generation) — and redirects to the clean URL.
  Every later load from the tile — relative or absolute `/c/<self>/…`,
  `<script src>`, CSS `url()`, workers, `fetch` — carries the cookie
  automatically. **No URL rewriting**, so relative *and* absolute
  self-references keep working.
- **Same site, not third party:** the tile origin is a subdomain of the
  workspace's site, so the cookie is first-party for the embedding shell
  (Safari ITP / Firefox TCP don't block it). bx-frame stops using
  `credentialless` for these frames — the separate origin now provides the
  isolation `credentialless` + opaque origins provided.
- **Authorization** on the tile origin: every request is checked live —
  `CanReadTile(user, owningComponent(path))` for `/c/…`, including other tiles'
  paths (a tile tag-loading `/c/<other>/lib.js` works exactly when its user can
  read `<other>`); `/api/…` and `/ws/…` on the tile origin act as that tile's
  frame principal (the same principal the frame token gives today).
- **Stronger isolation for free:** tiles stop sharing an origin — each gets its
  own `localStorage`/IndexedDB (an additive capability; opaque origins had
  none), and browsers can put each tile origin in its own process (the "phase
  5, subdomain per scope" item of plans/auth.md §6).
- **Needs:** wildcard DNS and a wildcard TLS certificate
  (`*.tiles.<domain>` — ACME DNS-01 via the traefik tile, or the operator's
  proxy). Dev: `*.localhost` resolves to loopback in browsers and is
  same-site with `localhost`.
- **Chrome** (root, shell, `chrome: true` tiles) stays on the main origin and
  cookie auth, unchanged.

### B — Path-scoped asset tokens (deployments without wildcard DNS)

For a workspace served at an IP or a single hostname:

- The sanctioned injection (D4) adds `<base href="/c/~<tok>/<tile>/<doc-dir>/">`
  and import-map entries remapping `/c/<tile>/` → `/c/~<tok>/<tile>/`. Relative
  URLs resolve under the token path, and resolution is transitive (a module's
  relative imports, a stylesheet's `url()`s resolve against their own
  token-bearing URLs). The document URL itself stays clean.
- **The token** is an asset-only HMAC (purpose-tagged, distinct from frame
  tokens) bound to (user, tile, session generation). It authorizes **only
  non-document static files** of tiles its user can read (live RBAC); it never
  authenticates `/api`, never serves HTML, never mints a frame token.
- **Side effects of `<base>`**, handled in `xbin-client.js` (already injected):
  fragment-only links (`href="#x"`) would resolve against the base and
  navigate — xbin-client scrolls instead; relative URLs passed to
  `history.pushState/replaceState` resolve against the document URL, not the
  base, so the token never enters `location`.
- **What cannot be credentialed:** absolute self-references in HTML attributes
  (`<script src="/c/<self>/x.js">`, `<link href="/c/<self>/…">`,
  `<img src="/c/<self>/…">`) and `inject:false` tiles. Under B these must
  migrate (to relative URLs, or to A). This is the one **breaking** element of
  the whole plan, and it comes with:
  - detection: a server-side scan of each tile's HTML/JS/CSS listing absolute
    `/c/` references in the tile report, and in `bx doctor`;
  - `bx fix assets <tile>` — an owner-run codemod that rewrites them relative;
  - an in-frame diagnostic (xbin-client reports failed subresource loads with
    the reason and the fix);
  - a **BREAKING** changelog entry and `docs/changes/` migration note.
- **Residual risk (stated, not hidden):** the token lives in subresource URLs
  inside the document (the `<base>`, performance entries). Tile JS can read its
  own token — it already holds its frame token, which is strictly more
  powerful. Copying it elsewhere yields at most static-asset reads of one tile
  as that user for as long as that user's session lives and their access
  stands.

Considered and rejected for B: per-tile *ports* (real origins without DNS, but
hundreds of open ports and proxy routes per workspace); a service worker
(opaque origins can't register one); a token in the Referer (opaque origins
send none).

### C — The native app (strict on its own)

The app never uses the credential-less path: its tile WebViews load through a
per-workspace `WKURLSchemeHandler` that attaches the tile's frame token to every
request, so absolute and relative loads are all credentialed
(plans/native.md §6.1). It ships independently of A and B.

## Rollout

1. **Release N** — mechanism A (config `--tiles-domain`, wildcard cert
   guidance) and B (default where A isn't configured); the absolute-reference
   scan in the tile report and `bx doctor`; `bx fix assets`; xbin-client's
   diagnostics. Today's behaviour is otherwise unchanged in N — nothing new is
   built on the heuristic.
2. **Release N+1** — enforcement: `tileSubresource` and the warm-IP gate are
   removed from `authedStatic`/`handleComponentStatic`; `RecentlyAuthed` loses
   its only consumer and goes too. The BREAKING note lands with N and names
   N+1.

(The owner may prefer enforcing in N directly — decision 3 below.)

## Tests

- Unit: every `/c/` request without a credential → 401 (documents,
  subresources, every file type), including forged Fetch-Metadata from an
  IP with a live session.
- A user without read on tile X: X's assets refused under A (cookie of another
  tile, another user's cookie), under B (another tile's asset token, a token
  for a revoked session), and through the app's scheme handler.
- Cross-tile tag loads allowed exactly when the user can read the other tile.
- RBAC changes take effect on the next request (no cache of the decision).
- A: redirect strips `?frame=`; the cookie is origin-scoped; `/api` on the tile
  origin acts as the tile; storage isolation between two tile origins.
- B: relative module graphs load; fragment links scroll; `replaceState('#x')`
  keeps `location` clean; absolute self-references fail with the diagnostic;
  the codemod fixes them.
- UI harness: every pass runs under A (`*.localhost`) and under B.

## Docs that change

docs/elements.md (asset URLs, `inject:false`), docs/auth.md and plans/auth.md
§6 (the browser model), docs/protocol.md (tile origins, asset tokens),
docs/compat.md (the absolute-reference rule), docs/changelog.md +
docs/changes/ (BREAKING under B), workspace-template/AGENTS.md (use relative
asset URLs).

## Decisions for review

1. **Target** — per-tile origins (A) wherever wildcard DNS is available
   (recommended), path-scoped asset tokens (B) otherwise.
2. **Tile host ids** — a keyed hash of the tile path (recommended: stable,
   doesn't leak tile names into DNS/SNI/certificate logs) vs readable slugs.
3. **Enforcement** — release N ships mechanisms + detection + codemod,
   release N+1 enforces and deletes the heuristic (recommended) vs enforcing
   in N.
4. **Per-tile storage** — accept that tiles gain their own
   `localStorage`/IndexedDB under A (recommended; additive) vs forcing
   `sandbox` without `allow-same-origin` on tile origins too.
