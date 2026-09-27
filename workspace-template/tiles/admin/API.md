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
  the sandbox layer refused or failed at; D112), *backup*, *cron*.
- **user management** — users, sign-in (SSO, tokens), browser sessions,
  organisations (and the workspace policy ceiling and defaults),
  permission sets, network sets, the access map.
- **vault** — every vault; seal/unseal/rekey; reveal/copy/edit/delete.
- **binding** — roles, grants (approve/revoke/add), interface providers,
  binding wiring.
- **ingress** — endpoints, services / expose.
- **workspace** — branding; the xbin app's native-runtime switch.

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
`/grants`, `/bindings`, `/ingress`, `/branding`, `/native-runtime`.

Revoke the grant to disarm it. Nothing here works for an unprivileged tile.
