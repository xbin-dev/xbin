# 2026-09-25 — tile creation follows ownership, not path patterns (D82)

## What changed

A non-admin no longer needs a per-user `canCreate` path pattern to create a
tile. They may create one they will **own** at any free path. The owner is
themselves, or an org where they hold Create. This covers every way a tile
appears: create, clone, git import, builtin tile import and template
instantiate. It holds whether they create directly or through a tile such as
the manager.

A path is refused for non-admins when it is:

- **reserved**: under `tiles/`, the chrome names `root` / `shell`, or any
  segment containing `:`;
- **inside someone else's scope**: the nearest `scope.json` root at or above
  the path must belong to the new tile's owner. That means the root's owner
  entry, or, if it has none, every tile already in the scope;
- **carrying leftovers** of a removed tile:
  - workspace grant rows naming the path;
  - interface bindings, instances or ingress hosts;
  - its vault;
  - another owner's entry;
  - other users' exact access entries, org shares, or an exact
    `defaultTiles` entry.

  The error lists them. Re-creating a path you already own is fine.

Creating **as an org** used to skip path checks entirely. It now gets the
same rule.

The `canCreate` field (per user and in the new-account defaults) is
**deprecated and ignored**. It is still accepted by `POST`/`PATCH /users`,
`PUT /defaults` and `bx user|defaults --create`, and still stored in
`data/users.json`, but it neither grants nor restricts anything. The admin
console no longer shows it.

`xbin.dialog(spec)` accepts a new optional `error` string, shown as an alert
box. The shell's *New tile* dialog uses it for refusals.

## Who's affected

- **Workspaces that relied on "no `canCreate` = can't create personal
  tiles".** Those users can now create personal tiles.
- **Scripts or tiles that expect a 403 for personal creation without a
  pattern.** That call now succeeds.
- **Org members who create org-owned tiles** at reserved paths (e.g.
  `tiles/x`), inside a scope their org doesn't own, or over a removed tile's
  leftovers. That is now refused.
- **Tools reading `canCreate`** from `GET /users` / `GET /whoami` to decide
  where someone may create. The field is still returned but means nothing.

Admins and unattributed automation (instance tokens, the bootstrap owner)
are unaffected.

## How to migrate

- To keep non-admins from owning tiles personally, set the tile-creation
  policy: `bx defaults set --tile-creation org-only` (or admin console →
  organisations → *tile creation*). They can then create only org-owned
  tiles where they hold Create.
- To let people add tiles to a shared scope, make the scope belong to them.
  For example, a workspace admin runs
  `curl -X POST -H "Authorization: Bearer $XBIN_TOKEN" $XBIN_URL/api/xbin/owner -d '{"tile":"apps/suite","to":"org:eng"}'`
  on the scope root, and members with Create add tiles as `org:eng`.
- If a create is refused over leftovers you know are stale, revoke the listed
  grants (`bx grant --revoke …`), drop the listed access entries, or create
  at another path. A workspace admin may also create there directly, which
  inherits them as before.
- Remove `--create` from provisioning scripts at your leisure. It keeps
  working, but does nothing.

## Why

`canCreate` came from D16, when permissions were keyed by path. Since D24
every tile has an owner, and creating as an org already ignored path
patterns. Keeping patterns for personal tiles meant an admin grant for
something that ownership already answers.

Path patterns did guard two real things, and the new rule guards them
explicitly:

- **A path is a durable key.** Grants, vault files and shares survive a
  removed tile's directory, so a new tile at that path would inherit them.
- **A scope is a trust unit.** Same-scope grants are auto-approved.
