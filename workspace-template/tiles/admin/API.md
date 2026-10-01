# tiles/admin

The workspace admin console. It exposes no API of its own; it *consumes*
xbind's admin-capable endpoints using the `xbin:admin` capability
(declared in `uses`, pre-granted in the workspace manifest).

## What it needs

`xbin:admin` — a grant on the reserved target `xbin`. This is the
heaviest capability in the system: it can read every element's vault
secrets and add/revoke any grant. Granting it to an element is equivalent
to trusting that element as the owner for administration. It is shipped
pre-granted only to this tile.

## Tabs

The router (`admin.js`, `GROUPS`) and one element per tab under `tabs/`
(docs/maintenance.md → "The admin console's tabs"):

- **runtime** — *components* (the tile roster: runtime, state, how each
  backend is sandboxed — ⧉ VM, 🔒 namespace or host — exposes, uses,
  vault, lifecycle; a row opens to who can reach it and the backend's
  sandbox, process, namespaces and egress; code & history drill-in),
  *resources* (host health, workspace totals, live per-tile stats, the
  brokered resources), *sandboxes* (every sandbox xbind runs — backend
  generations, terminals, agent sessions — with the host's isolation and VM
  health, the VM budget per tile, the VM policy editor, VM disks and what
  the sandbox layer refused or failed at; D112), *deployments* (every tile
  with a deployment record: its primary, 🛡 protection, live reload, its
  deployments and the last deploy; protect / unprotect the primary,
  reassign it behind the terminal window's loud confirmation, deliveries
  and alwaysOn per non-primary deployment, and ⇈ to the tile's Deployments
  panel for everything else — see *Tile managers' acts* below),
  *partitions* (tiles that keep each person's data apart: their mode and
  any request, people's metadata rows — never what a partition holds —,
  Keep / Switch…, reviewed code only, limits, stop / reset / restore of a
  person's partition, personal-bind records, orphans and their purge, the
  mode history; docs/partitions.md §Operating people's partitions), *backup*,
  *cron*. The sandboxes tab labels a person's partition instance, their
  sessions (without names) and their terminal disks.
- **user management** — users, sign-in (SSO, tokens), browser sessions,
  organisations (and the workspace policy ceiling and defaults),
  permission sets, network sets, the access map.
- **vault** — every vault; seal/unseal/rekey; reveal/copy/edit/delete.
- **binding** — roles, grants (approve/revoke/add), interface providers,
  binding wiring.
- **ingress** — endpoints, services / expose.
- **workspace** — branding; the xbin app's native-runtime switch;
  *settings* (D180) — every workspace setting, by topic: terminals (base
  auto-update, D175) and partitioned tiles (asking each person before
  another partitioned tile uses their data; credential resets waiting for
  the person). Against an older xbind it reads what that one has (v0.3.66:
  the partitioned tiles' switches through `/workspace-policies`) and says
  what it lacks; the old `#terminals` and `#policies` links open it.

## Endpoints used

All under `/api/xbin`, gated by owner-or-`xbin:admin` unless
/docs/protocol.md says otherwise: `/auth-overview`, `/runtime`,
`/sandboxes`, `/vm`, `/vm/policy`, `/alerts`, `/access`, `/access-matrix`,
`/access-requests`, `/lifecycle`, `/components`, `/code/tree`,
`/code/file`, `/git/log`, `/git/diff`, `/backup`, `/backups`,
`/backup-schedule`, `/restore`, `/cron/jobs`, `/users`, `/owner`,
`/impersonate`, `/sessions`, `/devices`, `/auth-settings`,
`/auth-rotate-token`, `/orgs`, `/policy`, `/defaults`,
`/permission-sets`, `/net-sets`, `/vaults`, `/vault/<c>/<k>`,
`/vault-status`, `/vault-seal`, `/vault-unseal`, `/vault-rekey`,
`/grants`, `/bindings`, `/ingress`, `/branding`, `/native-runtime`,
`/workspace-settings`, `/workspace-policies`, `/partitions/edges`,
`/deployments`, `/deployments/protect`, `/deployments/primary`,
`/deployments/deliveries`, `/deployments/always-on`, `/partitions`,
`/partitions/mode`, `/partitions/reviewed`, `/partitions/limits`,
`/partitions/stop`, `/partitions/reset`, `/partitions/purge`,
`/partitions/binds`, `/partitions/backups`, `/partitions/restore`.

The partitions tab reads and acts as the person who opened the console,
like the deployments tab below — not with the tile's `xbin:admin` grant:
xbind lists people's rows, totals, orphans and the mode history only when
that person is an admin, and judges each act as theirs (a mode decision
needs a manager of the tile; the limits, a reset of someone else's
partition, a restore, the purge, a personal bind's removal and reviewed
code only an admin).

## Tile managers' acts (the deployments tab)

Protect, unprotect, reassign the primary, deliveries and alwaysOn are tile
managers' acts. The admin tile's frame does them **as the person who
opened it**, not as the tile (D127m;
docs/auth.md §Tile deployments): xbind judges that person — the tile's
owner, its org's admins, or a workspace admin — so an admin manages every
tile and anyone else only what they manage in their own session. The
`xbin:admin` grant alone manages nothing, a view-as session is read-only,
and the admin tile's terminal and agent sessions are refused like any
tile's. Every other deployment act (code moves, edges, data, vault copy,
limits) is in the tile's Deployments panel, which the tab's ⇈ opens
(the `xbin:open-deployments` message, docs/protocol.md). New in an existing
workspace after `bx builtin update scaffold:tiles/admin`.

Revoke the grant to disarm it. Nothing here works for an unprivileged tile.
