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

Endpoints used (all gated by owner-or-`xbin:admin`, see /docs/protocol.md):
`GET /api/xbin/auth-overview`, `GET /api/xbin/vaults`,
`GET|PUT|DELETE /api/xbin/vault/<c>/<k>`, `GET|POST|DELETE /api/xbin/grants`,
`GET /api/xbin/cron/jobs`, `DELETE /api/xbin/cron/jobs/<name>`,
`GET /api/xbin/resources`, `GET /api/xbin/backends`.

## Panels

- **Overview** — counts, per-component principals (roles, uses, backend, vault).
- **Vault** — password-manager view of every vault; reveal/copy/edit/delete.
- **Roles & grants** — exposed roles, full grant table, approve/revoke/add.
- **Cron** — all scheduled jobs; delete.

Revoke the grant to disarm it. Nothing here works for an unprivileged tile.
