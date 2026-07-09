# 2026-07-09 — terminal tokens are tile-scoped; the root terminal is disabled

**BREAKING** for any workflow that ran admin operations from inside a
terminal.

## What changed

`$XBIN_TOKEN` inside a terminal used to be the **owner token** — every shell
(and every agent in it) passed every permission check. It is now a
**per-session token scoped to the tile the terminal is opened on**: the
tile's element principal, i.e. admin of *that* component (its API, vault,
resources) plus whatever `uses`/bindings the owner approved for it — exactly
the identity the tile's own backend and frontend already had. It is revoked
when the session ends, and deleting a user immediately kills their live
shells' API access.

Consequences inside a tile terminal:

- `bx grant` approval, `/users`, lifecycle, backups, `/runtime`,
  `/auth-overview`, other tiles' admin endpoints → **403**. Agents can no
  longer self-approve cross-scope grants — the human-in-the-loop is enforced.
- Your own tile keeps working: its API (as admin of itself), its vault
  (`bx vault set <self>`), its resources, its bound providers, `git fetch
  template`, `/components`, `/whoami`, docs.
- `bx` prints a scope hint on 403s.

Also:

- **The root terminal (no `cwd`) is disabled** — it was reachable from no UI.
  Workspace-wide work (creating components, workspace `xbin.json`, the
  workspace-root repo) happens in the browser (shell right-click → *Create a
  new tile*, Tile Manager, admin tile) or from the **host shell**, where the
  owner token lives (`.xbin/token`).
- A terminal can only be opened on a tile the signed-in user may use;
  killing/reattaching another user's session is admin-only.

## Who's affected / what to do

- **Agents in tile terminals** (Claude Code etc.): declare `uses` in your
  manifest and tell the owner it's pending — approval happens in the grants
  panel. This was always the documented flow; now it's the only one.
- **Humans who used a terminal for admin ops**: use the admin tile / grants
  panel in the browser, or run `bx` on the host with
  `XBIN_URL=… XBIN_TOKEN=$(cat <workspace>/.xbin/token)`.
- **Scripts inside terminals** calling other tiles' APIs: have the tile
  declare `uses` for what it calls (the terminal exercises the tile's
  grants), or move the script to the host.

## Why

Backends already had per-generation instance tokens and frontends frame
tokens; terminals were the last surface holding the owner credential. One
prompt-injected or misbehaving agent in any tile terminal could previously
read every tile's admin config and grant itself anything.
