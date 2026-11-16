# 2026-11-16 — a session inside a tile is that tile's

## What changed

A terminal or agent session can open in a directory inside a tile
(`/ws/term?cwd=apps/crm/src`, `POST /api/xbin/term/sessions {cwd}`, a
resume, a restart, a workspace agent's start). Every decision about such a
session is now made on the **registered tile the directory lies in**
(`apps/crm`), never on the directory's own path:

- **who may open it**: the person's level on the tile — its exact entries,
  its owner, its org (D31) — must be `terminal`. Before, the level of the
  path `apps/crm/src` was asked, which only *patterns* match: an exact
  `apps/crm: none` or `read` beside `apps/*: terminal` let the person open a
  read-write shell or a coding agent anywhere inside apps/crm, and a
  personal pattern reached inside an org's tile;
- **who may come back to it**: reattach, drive, restart and the session
  directory follow the person's level on the tile, so an exact entry that
  closes the tile closes the sessions they opened inside it too;
- **what it is given**: the org's network sets and net ceiling, the harness
  rules, the LLM connector's policy, the deployment target (a protected
  primary), the partition, and the terminal token are the tile's — the
  token is the one a terminal on the tile gets, and **`XBIN_COMPONENT` is
  the tile** (`apps/crm`), no longer the directory (`apps/crm/src`);
- **what it is announced as**: its `term`/`session` events, its sandbox
  row and the deployment plane's protect/reassign name the tile; past
  sessions are listed and read on the tile they ran on;
- **its directory must be real**: one reached through a symbolic link
  (`apps/a/sub -> ../b`) is refused (`400`, "is not a directory reached
  without a link");
- the source reads `GET /api/xbin/code/tree` and `/code/file` with
  `component=<a directory inside a tile>`, `GET /ws/term/env?cwd=`,
  `GET /api/xbin/term-net?tile=` and `GET /api/xbin/delegate/start-defaults?tile=`
  decide on the tile the same way (start-defaults answers the tile in
  `tile`).

Unchanged: the session still starts in the directory it was opened in,
that directory alone is bound read-write (the rest of the tile read-only),
its persistent dev layer and agent history are where they were, and its
row's `cwd` is the directory.

## Who's affected

- **People whose access came from a pattern** (`apps/*: terminal`, or
  `apps/*: read` for the source reads) on a tile where an exact entry
  gives them less, or on an org's tile they are no member of: they no
  longer open sessions, or read source, inside it — that was the hole.
- **People whose access to a tile is an exact entry, ownership or org
  membership** can now open a session in a directory inside it (before,
  only on the tile's own path), and read its directories' source.
- **Scripts in a session opened inside a tile** that read
  `$XBIN_COMPONENT` expecting the directory: it is the tile now.
- **Anyone opening a session at a symlinked directory.** Under isolation
  such a session never started (the sandbox refuses a mount point through
  a tile's link); without isolation it ran in the link's target.

## How to migrate

No configuration change is needed. In a script that needs the session's
directory, use `$PWD` (the session starts there), not `$XBIN_COMPONENT`.
Open a session on the directory a link points to, not on the link.

To check whether the hole was used on your workspace: list the people who
hold a terminal or read **pattern** together with an exact `none` or lower
entry on a tile under it, or whose pattern covers an org's tile they are
not a member of, and look in xbind's log for `terminal session created` /
`agent session created` lines whose `cwd` is deeper than a tile's path.

## Why

Levels are looked up by the tile's own path: an exact entry, an owner and
an org are recorded for `apps/crm`, not for every directory under it. A
gate that asked the directory's level skipped all three and saw only the
patterns — the weakest, widest entries — so it opened what the exact entry
and D31 had closed. A session is a tile's whatever directory it starts in,
so every decision about it is now that tile's.
