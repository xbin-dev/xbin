# 2026-09-08 — links in new tabs need the `cap:open-links` grant (ND11)

## Why

Tile frontends run in an opaque-origin sandbox (`sandbox allow-scripts
allow-forms allow-modals allow-downloads`, ND8/ND10). That list has no
`allow-popups`, so every `<a target="_blank">` and `window.open` inside a
tile was dropped by the browser without a word — including the shipped
tiles' own "docs ↗" links and the agent template's markdown links.

The token that makes such links useful is the pair `allow-popups
allow-popups-to-escape-sandbox` (with `allow-popups` alone the new window
inherits the sandbox and external sites break). With the pair a tile can
open a fully privileged, cookie-bearing top-level page at a URL it chose —
the look-alike-page primitive. That is not a default; it is a reserved
capability an admin approves per tile, like `cap:containers`.

## What breaks

Nothing for tiles that never open new tabs. Tiles that already carry
`target="_blank"` links keep behaving as before (blocked) until granted —
the browser console now names the grant on the first blocked click.

## What to do

**A tile you build:** declare the capability and have it approved.

```jsonc
// xbin.json
{ "uses": [ { "target": "cap:open-links", "role": "writer" } ] }
```

The request lands pending (never auto-granted). A workspace admin approves
it in the grants panel, the admin console (roles & grants), or with
`bx grant <tile> cap:open-links:writer`; an org admin can approve it from
the organisations tile when the org's allowance (or a permission set) covers
`cap:open-links`. The frame reloads at once — no backend restart. Put
`rel="noopener"` on your links.

**An existing workspace (created before this release):** the shipped tiles
gained the declaration, but a workspace copies them at init, so update them
and approve the three pending rows:

```
bx builtin updates                      # shows scaffold:tiles/admin, tiles/apidocs, apps/welcome
bx builtin update scaffold:tiles/admin  # --merge if you edited the tile
bx builtin update scaffold:tiles/apidocs
bx builtin update scaffold:apps/welcome
bx grant tiles/admin  cap:open-links:writer
bx grant tiles/apidocs cap:open-links:writer
bx grant apps/welcome cap:open-links:writer
```

(or approve the three rows in the grants panel). Nothing is granted for
you: a silent grant is exactly what the decision avoids. A fresh workspace
pre-approves them in its template manifest.

## Scope of the grant

- Exactly two sandbox tokens, in both layers (the iframe attribute and the
  CSP header of the tile's documents, so a full-page `/c/<tile>/` open
  behaves the same). Top navigation is never allowed.
- The workspace's own pages (chrome, `/docs/`) send
  `Cross-Origin-Opener-Policy: same-origin`, so a tile that opened them
  keeps no handle; for external targets use `rel="noopener"`. In a
  credentialless frame (Chromium) `window.open()` returns `null` — the tab
  still opens.
- A workspace or org policy row with `deny xbin-caps` strips it, like every
  reserved capability. Revoking the grant takes the tokens back on the next
  frame load (immediate — the frame reloads on the change).
- `/components` reports the extra tokens as `sandbox` so the shell's frame
  and the server's header always carry the same list.
