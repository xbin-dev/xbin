# 2026-09-28 — `+` is refused in new tile names (D127)

## What changed

A new tile's path may no longer hold `+` in any segment. `<tile>+<name>` is
how a tile deployment's URL is spelled (`/c/apps/crm+dev/`,
[tile-deployments.md](/docs/tile-deployments.md)), so a tile named that way
would read as one. The refusal holds:

- for every creator — admins, the root token and tiles holding the
  workspace-management grant included;
- on every way a tile is created: `bx new` (its local write too),
  `POST /api/xbin/create`, clone, template instantiate, builtin tile import
  and git import, and so the manager tile and the shell's *New tile*.

The answer is 403 `can't create <path>: '+' isn't allowed in tile names (it
names a tile deployment in URLs, /c/<tile>+<name>/) — pick another path`.
Before, such a name was created like any other.

Existing tiles are untouched. A directory whose name already holds `+`
keeps resolving — an exact match wins over the deployment reading — and
keeps serving, building and answering as before. It can't get deployments
(`/deployments/add` answers 409), and `bx doctor` flags it.

With it, a query string never carries a `tile+name` ref: `+` there decodes
to a space. A query names the tile and the deployment apart —
`GET /api/xbin/deployments?tile=apps/crm&deployment=dev` — and `tile=` (or
`component=` on `/frame-token`, `/logs` and `/tile-status`) that reads as
`<tile>+<name>` is answered 400 `a deployment is named with deployment=, not
tile+name (a '+' in a query string reads as a space)`: an escaped `+` (`%2B`)
that names no tile, or an unescaped one, which arrives as a space after a
tile's path. Paths and JSON bodies keep `<tile>+<name>`
([protocol.md](/docs/protocol.md) §Tile deployments).

## Who's affected

- **Scripts, templates and tiles that create tiles named with `+`** (for
  example a manager tile that derives paths from user input, or
  `bx new apps/c++`). That call now fails with 403.
- **Workspaces with tiles already named with `+`**: nothing changes for
  them, except that they can't get tile deployments and `bx doctor` lists
  them.
- **Clients that sent `?tile=<tile>+<name>`** to the deployments reads.
  Tile deployments shipped in the same release, so only pre-release clients
  did.

## How to migrate

- Pick another name: `-` is the usual replacement (`apps/c-plus-plus`,
  `notes-ideas`).
- A tile named with `+` that should get deployments: clone it to a path
  without `+` (`POST /api/xbin/clone {"from":"notes+ideas","to":"notes-ideas"}`,
  or the manager tile), move what points at the old path (grants, bindings,
  frames), then remove the old directory. A clone copies the code, not the
  resource data or vault secrets: carry those over before removing the old
  tile.
- In a query, send the tile's path as `tile=` and the deployment as
  `deployment=`; `bx` does this for a ref you give it. A tile whose own name
  holds `+` is still named in a query by its path, with the `+` escaped as
  `%2B` (`URLSearchParams` and Go's `url.Values` do it).

## Why

A deployment URL puts the deployment's name after a `+` in the tile's last
segment. While tile names could hold `+` too, `apps/x+dev` could be either,
and every reader — xbind, the shell, `bx`, a person reading a link — had to
ask which. Refusing `+` in new names removes the question going forward;
existing directories keep the exact-match rule so no workspace breaks. This
follows D82, which refused `:` in new tile names at once (it separates grant
targets and identities), though for everyone but admins.

`:` was considered as the qualifier instead, and rejected: in a relative URL,
`notes:dev/` parses as a scheme (JavaScript's `new URL`, Go's `url.Parse`),
and `:` is the grant grammar's separator. `+` is kept because paths carry it
unchanged; the one place it doesn't survive, a query string, now never
carries the qualifier.
