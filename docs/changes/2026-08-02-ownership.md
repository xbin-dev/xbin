# 2026-08-02 — ownership replaces teams & positional org paths (BREAKING)

The multi-user grouping model was rewritten (plans/ownership.md, D24–D28).
**No released workspace used orgs/teams or `o/` paths**, so there is no data
migration — but API clients and scripts touching the org surface must update.

## What changed

- **Teams are gone.** Orgs are flat member lists: `{id, level, create,
  admin}` per member, org-wide on **org-owned** tiles. The
  `/orgs/<org>/teams*` endpoints, `bx team`, and team ACL entries no longer
  exist.
- **`o/<org>/…` paths mean nothing.** A component's org is its **owner**
  (`user:<id>` / `org:<id>`, `GET/POST /owner`), assigned at create
  (`owner` field on /create; creators default to user-owned) and
  transferable. The `o`/`u` path-segment reservations are dropped.
- **Sharing is an ownership right.** `PUT /access` (exact `user:`/`org:`
  entries) is writable by the ws-admin, the tile's user-owner, and the
  owning org's admins.
- **Delegated approval (new).** Ws-admins may grant an org an **allowance**
  (`PATCH /orgs/<org> {allow}` or attached **permission sets**,
  `/permission-sets`): its admins then approve covered grants/bindings on
  org-owned tiles themselves. `xbin`/`xbin:*` is never delegable.
- **`defaultTiles` (new).** Workspace-wide pattern→level visibility for all
  users (`GET/PUT /defaults`).
- **whoami orgs shape**: `[{id,name,level,create,admin}]` (was
  `[{id,name,admin,teams}]`).

## Updating clients

- `POST /create {team: "<org>/<team>"}` → `{owner: "org:<id>"}`.
- `bx access <tile> set team:… ` → `org:<id>=<level>`.
- Anything parsing `/orgs` must read the new member objects.
