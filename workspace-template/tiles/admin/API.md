# tiles/admin

The workspace admin console. It exposes no API of its own; it *consumes*
buxond's admin-capable endpoints using the `buxon:admin` capability
(declared in `uses`, pre-granted in the workspace manifest).

## What it needs

`buxon:admin` — a grant on the reserved target `buxon`. This is the
heaviest capability in the system: it can read every element's vault
secrets and add/revoke any grant. Granting it to an element is equivalent
to trusting that element as the owner for administration. It is shipped
pre-granted only to this tile.

Endpoints used (all gated by owner-or-`buxon:admin`, see /docs/protocol.md):
`GET /api/buxon/auth-overview`, `GET /api/buxon/vaults`,
`GET|PUT|DELETE /api/buxon/vault/<c>/<k>`, `GET|POST|DELETE /api/buxon/grants`,
`GET /api/buxon/cron/jobs`, `DELETE /api/buxon/cron/jobs/<name>`,
`GET /api/buxon/resources`, `GET /api/buxon/backends`.

## Panels

- **Overview** — counts, per-component principals (roles, uses, backend, vault).
- **Vault** — password-manager view of every vault; reveal/copy/edit/delete.
- **Roles & grants** — exposed roles, full grant table, approve/revoke/add.
- **Cron** — all scheduled jobs; delete.

Revoke the grant to disarm it. Nothing here works for an unprivileged tile.
