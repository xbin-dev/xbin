# 2026-07-11 — orgs & teams: `o` and `u` are reserved path segments

xbin now has organizations and teams (docs/auth.md → "Organizations &
teams"). An org binds to tiles **positionally**: the path segment `o`
followed by the org id — `o/<org>/…` or `<dir>/o/<org>/…`, e.g.
`apps/o/sales/crm` — and `u` is reserved the same way for future per-user
tiles.

## What breaks

Creating a **new** tile whose path contains a segment that is exactly `o` or
`u` is now rejected (via `POST /api/xbin/create`, clone, git import, builtin
tile import, template instantiate, and `bx new`, which routes such paths
through the API):

- `apps/o/<name>/…` is only valid when `<name>` is an **existing org**
  (create it first: `bx org add <name>`, or admin tile → orgs & teams).
- Any other position (`apps/foo/o/bar`, a tile literally named `o` or `u`)
  is invalid.

Segments merely *containing* the letters are unaffected (`apps/opera`,
`apps/u2-tribute` are fine) — only the exact segments `o` / `u`.

## What keeps working

- **Existing tiles are grandfathered**: nothing on disk is rejected or
  relocated; a pre-existing path that would no longer validate keeps
  running. `bx doctor` warns about such paths (and about org markers naming
  a non-existent org — those tiles are workspace-plane until the org is
  created).
- Workspaces with no orgs configured behave exactly as before; the identity
  store (`data/users.json`) loads old files unchanged.

## Existing workspaces: update the chrome to get the UI

The org/team **enforcement and API** ship in the xbind binary, but the UI
lives in workspace files copied at init: the shell (`shell/` — the per-tile
⚙ access panel, the "orgs & teams" popover, org sidebar groups), the admin
tile (`tiles/admin` — orgs tab, policy editor), and the manager
(`tiles/manager` — create-in-team picker). On a workspace created before
this release, update them after deploying:

```
bx builtin updates          # shows what drifted
bx builtin update <id>      # per component; --replace or --merge if edited
```

Until then the feature is fully usable via `bx org|team|access` and the API
— only the buttons are missing.

## Also tightened in this release (behavior changes)

- Tile-creation authority is uniform across create / clone / git import /
  builtin import / template instantiate: users' `canCreate` patterns now
  work on all of them (previously admin/capability-only for copy/import
  routes); copy-shaped routes require read on the source; and an element's
  workspace-management grant no longer extends the DRIVING user's own
  create rights — a non-admin user driving the manager tile can only create
  where their own patterns (or teams) allow. Unattributed automation is
  unchanged.
- `whoami`'s driving-user info on element principals is scoped by tile
  trust (docs/auth.md) — plain tiles now see `{id, name}` only.

## Why

Path-as-identity: a tile's org must be readable off its path with no
ownership table to drift, which requires the marker segment to be
unambiguous — hence reserved. Reserving `u` now (before per-user tiles
exist) avoids a second migration later.
