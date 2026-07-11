# 2026-07-11 — per-tile access tiers replace the tile allow-list

## What changed

A user's tile access is now a **level per path** — `read < write < terminal`
(monotone) — instead of a flat allow-list plus a global `terminal` flag, and
two things were added alongside (docs/auth.md):

- `canCreate`: path patterns the user may scaffold tiles under; creating one
  auto-grants them `terminal` on it.
- `termApi` / `termNet`: without these grants a non-admin's terminals get no
  live tile-API token and no internet egress (clamped, not rejected);
  `net=host` is admin-only always (docs/isolation.md).

`users.json` and the users API now carry `tiles` as a `{path: level}` map:

```json
{"id": "eve", "role": "user",
 "tiles": {"apps/chat": "terminal", "lib/*": "read"},
 "canCreate": ["sales/*"], "termApi": true}
```

## Migration

- **`users.json` migrates itself.** The legacy shape (`"tiles": ["apps/a"]` +
  `"terminal": true`) loads as the power it had — each entry becomes `write`,
  or `terminal` when the flag was set — and is rewritten to the new shape on
  the next save. Nothing to do.
- **The API still accepts the legacy body.** `POST/PATCH /api/xbin/users`
  with a tiles *array* (+ `terminal` bool) keeps working, same mapping; a
  PATCH with only `{"terminal": bool}` re-levels the user's existing entries
  (the old ±term toggle). **Responses**, however, return the map shape — a
  pre-tiers admin tile renders the new `tiles` field poorly until updated:
  refresh your workspace's `tiles/admin` from the builtins updater (or copy
  `workspace-template/tiles/admin/admin.js`).
- **`bx user`** flags changed: `--tiles apps/a=terminal,lib/*=read` (a bare
  path means `write`), `--create sales/*`, `--term-api`/`--term-net`; the old
  `--terminal` flag is gone (grant a `terminal` level instead).
- **Semantics tightened for non-admins** (admin/owner behavior is unchanged):
  opening a tile terminal (and resetting its dev layer) now needs `terminal`
  level on that tile, not just membership plus the global flag — the migration
  preserves exactly what each user could already do; viewing tiles needs
  `read` (any level qualifies); non-admin terminals are code-only + airgapped
  unless `termApi`/`termNet` are granted, and their filesystem hides tiles
  below `read`.
