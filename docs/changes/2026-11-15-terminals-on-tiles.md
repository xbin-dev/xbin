# 2026-11-15 — a terminal opens on a tile; names starting `xbin~` are reserved

## What changed

**1. A terminal or agent session opens only on a registered tile, or a
directory inside one.** `/ws/term?cwd=<path>` and `POST
/api/xbin/term/sessions {cwd}` now answer `403` — "a terminal opens on a
tile: `<path>` is not a registered tile, nor inside one" — for any other
path:

- xbind's own directories: `.xbin`, `data`, `homes`, `vendor`, and any
  hidden or `..` path;
- a plain folder that is no tile: `apps` itself, a directory with no
  `xbin.json` and no `index.html`, a path that doesn't exist.

Before, the only check was the caller's terminal level at that path, and a
workspace admin (and the owner token) has terminal level on every path.

**2. Top-level names starting with `xbin~` can't be created.** `POST
/api/xbin/create`, `/clone`, `/git/import`, `/templates/new` and
`/builtins/import` answer `409` for a path whose first segment starts with
`xbin~` ("reserved: they would share a system component's data key").

## Who's affected

- **Admins who opened a terminal at a folder that is no tile** — by typing
  a URL, or with a script against `/ws/term?cwd=` or `POST
  /api/xbin/term/sessions`. The shell, the admin console and `bx` open
  terminals on tiles only, so nothing they offer changes.
- **Nobody has to rename anything.** A directory named `xbin~something`
  that already exists keeps running; only creating a new one is refused.
  (If a later release ships a system component `xbin/something`, such a
  tile loses its declared resources and says why on its row:
  [compat.md](/docs/compat.md) → Sealed components. Renaming it then is the
  fix.)
- Tiles, their backends and their terminals are untouched.

## How to migrate

If you used a terminal at a non-tile folder, open it on a tile instead:

- **To work across tiles** (grep, compare, copy between them): open the
  terminal on any tile you have terminal level on. Every tile you can read
  is visible there, read-only, at its workspace path; your own tile and
  your `$HOME` are writable.
- **To create a tile**: use the shell's **+** (new tile, from a template,
  from a builtin) or `bx new <path>`. A tile is registered as soon as it
  has an `index.html` or an `xbin.json`; then a terminal opens on it.
- **To look at xbind's own state** (`data/`, `.xbin/`): use the admin
  console, `bx` and the API (`bx status`, `bx logs`, `bx vault`, `bx
  backups`), or a shell on the host. A sandboxed terminal never could
  read those directories; it can no longer be pointed at them either.
- **To change files outside any tile** (the workspace `xbin.json`, a
  shared folder): use the admin console or `bx`, or the host.

For a new tile whose name you planned to start with `xbin~`, pick another
name; `/` and `~` map to the same data key, so `xbin~x` would collide with
a system component `xbin/x`.

## Why

A session's directory is bound read-write into its sandbox, on top of the
masks that hide `.xbin`, `data` and other people's homes from every
terminal. With a non-tile path that bind landed on xbind's own state: an
admin's shell opened with `cwd=.xbin` could not read the owner token or
the token-signing secret, but could replace them; with `cwd=data` it could
rewrite `data/users.json` (accounts, roles, password hashes) and move the
vault away; with `cwd=homes` it could write into anyone's home. An admin is
trusted with the workspace, not with becoming the owner token or another
person — and a leaked admin session should not reach that far either.

`xbin~` names are reserved because resource data is keyed by a path with
`/` written as `~`: a tile named `xbin~x` and a system component `xbin/x`
would be given the same encrypted volumes.
