# 2026-09-26 — strict tile asset gating: relative asset URLs (enforced next release)

## What changed

A tile's frontend files — everything under `/c/<tile>/` — are moving to
strict, per-user gating: a browser loads them only with a credential proving
the user may read that tile ([auth.md §Tile asset gating](/docs/auth.md)).
Until now a sandboxed tile's subresource loads (scripts, styles, images,
fonts) passed without any credential on a Fetch-Metadata fingerprint plus
"someone signed in from this IP within the last hour" — which let anyone
sharing an office NAT, a carrier NAT or a VPN exit with a signed-in user
read any tile's files.

This release ships the replacement behind the daemon flag `--tile-assets`
and keeps `legacy` (the old rule) as the **default**:

- `tokens` — the injected `<base href="/c/~<asset-token>/<tile>/<dir>/">`
  makes **relative** URLs carry a path-scoped asset token;
- `origins` — each tile runs on its own origin `t-<id>.<tiles-domain>` with
  its own cookie (`--tiles-domain`, wildcard DNS + TLS);
- detection: `bx doctor` and `GET /api/xbin/tile-assets` list what the
  strict modes refuse; `bx fix assets <tile>` rewrites it; xbin-client logs
  failed loads with the reason and the fix.

**The next release enforces**: the credential-less rule is deleted and a
strict mode becomes the default.

Also in this release, in **every** mode (security fixes, no opt-out):

- a symlink in a tile that resolves outside the workspace or into `.xbin/`,
  `data/` or `homes/` answers 404 (a symlink to another tile still works in
  `legacy`); a FIFO or device answers 404;
- a sandboxed tile's non-document files (anything but `.html`/`.htm`, any
  case) carry `Content-Security-Policy: sandbox` — no change as
  subresources; opened directly or framed (`<iframe>`, `<object>`), an SVG
  or `.xhtml` page no longer runs script as the workspace origin; `x.HTML` is now treated as a document
  (injected and sandboxed);
- a tile document fetched, opened or framed by another tile gets no frame
  token (navigating a tile's own nested pages still mints one;
  [frame-tokens.md](/docs/changes/2026-09-26-frame-tokens.md)).

## Who's affected

Under `--tile-assets=tokens` (and, next release, by default):

- tiles referencing their own or another tile's files by **absolute** `/c/…`
  URL in HTML attributes (`src`, `href`, `srcset`, `poster`, …), in CSS
  (`url()`, `@import`) or in the document's own `<script type="importmap">`;
- `inject: false` tiles (no injection, so no asset `<base>`);
- code that builds `/c/…` URLs for the DOM (`img.src = '/c/me/x.png'`) —
  reported by the scan as "needs a look".

Absolute **module imports** of a tile's own files, workspace import-map
entries and relative URLs keep working in every mode. Under `origins`
absolute URLs keep working too.

Operators switching to `origins`: every browser is signed out once (the
session cookie becomes `__Host-xbin_session`), and the workspace plus
`*.<tiles-domain>` must be served over HTTPS (dev: `*.localhost`).

In every mode: a tile that served files through a symlink into `data/`,
`homes/` or outside the workspace; a tile relying on an SVG (or other
non-HTML file) running script when opened directly or framed; a tile
frontend that fetched ANOTHER tile's document to use the frame token in it.

Credentials end with the login that opened the tile: `tokens`-mode asset
tokens are bound to the same credential generation as the document's frame
token (sign-out, *sign out everywhere*, device revocation end them), and
tile-origin cookies end with the browser session.

## How to migrate

1. `bx doctor` — lists every tile with references the strict modes refuse.
2. `bx fix assets <tile>` — prints the rewrite (`file:line:col old → new`);
   `bx fix assets <tile> --write` applies it. Relative URLs resolve to the
   same file in every mode, so the rewrite changes nothing today.
3. Fix by hand what the codemod lists under "needs a look": URLs built in
   JavaScript (resolve them against `document.baseURI` or
   `import.meta.url`; navigate with `xbin.url()`), references to workspace
   chrome (vendor what you need), documents with their own `<base>`.
4. Drop `"inject": false` unless the document truly needs no identity (it
   then loads nothing under `tokens`; use `origins`, or no flag).
5. Try the workspace with `--tile-assets=tokens` (or `origins`) before the
   next release makes it the default.
6. Serve data a tile reads from `data/` through its backend API, not a
   symlink; ship interactive pages as `.html`.

## Why

The credential-less rule checked the request's shape, not the file or the
user: any non-HTML file in any tile — backend source, `API.md`, local data —
was readable by anyone sharing an egress IP with any signed-in session, and
Fetch-Metadata headers are client-settable. The strict modes check every
request against the user's live access. Details: docs/auth.md §Tile asset
gating; the decision is recorded in the decision log.
