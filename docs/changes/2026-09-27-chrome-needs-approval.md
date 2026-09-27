# 2026-09-27 — `chrome: true` needs a workspace admin's approval (D118)

## What changed

A tile's `"chrome": true` in its own xbin.json is now only a **request**.
xbind runs a tile unsandboxed (on the workspace origin, with the session
cookie, acting as whoever opens it) only when it is:

- `root` or `shell`, which are chrome implicitly;
- the shipped `tiles/organisations`;
- or a tile a **workspace admin approved**: `bx chrome approve <tile>`, or
  `PUT /api/xbin/chrome {"path": "<tile>", "approved": true}`. The approval
  applies while the tile's xbin.json says `chrome: true`.

Every other tile that says `chrome: true` is served like any other tile:
CSP sandbox header, a sandboxed `bx-frame`, an injected frame token as its
only credential, and a native UI if it has one. `GET /api/xbin/components`
reports it with `chrome: false` and `chromeRequested: true`, and `bx doctor`
lists it.

Approvals are kept in `data/users.json` (`chromeTiles`). `bx chrome` lists
every tile that asks and every approval; `bx chrome revoke <tile>`
withdraws one. An approval is path-keyed, so it counts as a leftover: a
non-admin can't create a new tile at an approved path.

## Who's affected

- **Workspaces with their own `chrome: true` tiles** (anything other than
  `tiles/organisations`, `root` and `shell`). After the upgrade those tiles
  run sandboxed. Code that raw-`fetch`es `/api/xbin/*` or another tile's API
  as the signed-in human gets 401. Code that reads `document.cookie`,
  `localStorage` or the shell's DOM throws `SecurityError`.
- **Tools that read `chrome` from `/components`.** It now says what xbind
  does, not what the manifest asks.

Unaffected: the shell, `tiles/organisations`, every tile without the flag,
backends, terminals, `bx`, and the SDKs.

## How to migrate

1. Run `bx doctor` or `bx chrome` as a workspace admin to see which tiles
   ask for chrome.
2. For each one, decide whether it really must act as the person looking at
   it. Chrome is trusted like the shell. Everyone who can write the tile
   (its terminal users and their coding agents) can make it act as any
   viewer, admins included.
3. If it must: `bx chrome approve <tile>`. Open frames switch at once.
4. If it need not, drop the flag and use the tile's own identity: ask for
   the grants it needs in `uses` and call through `xbin.fetch` or the kit's
   `api()`
   ([2026-08-04-tile-frontend-isolation.md](2026-08-04-tile-frontend-isolation.md)
   has the patterns).

## Why

`chrome: true` switches off the frame sandbox, so the tile's frontend runs
with the ambient session cookie as whoever opens it. The flag lived only in
the tile's own xbin.json, which sits in the tile's directory. Every terminal
on the tile, and every coding agent run there, can write that directory
(D40). So any user with terminal access to a tile, or an agent following a
prompt injection, could make it chrome and act as the next admin who opened
it. The old docs called the flag host-set; it was not. Trust now comes from
state only xbind writes, set by a workspace admin. This closes a security
hole, so it takes effect in this release without a warning period
(docs/compat.md).
