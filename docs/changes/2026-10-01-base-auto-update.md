# 2026-10-01 — terminals move to a new base image by themselves (base auto-update, on by default)

## What changed

A tile's terminal keeps the system changes made in it (apt installs,
`/etc` edits) in a persistent layer on top of the base image it was built
on ([09-terminals.md](/docs/overview/09-terminals.md) §Base images). When
xbind ships a newer base, the layer used to stay on the old one until
someone clicked **⬆ base update** in the terminal window.

A new workspace setting, **base auto-update**, is **on by default**: the
next session that opens a layer built on an older base moves it to the
current base first. The layer is reset exactly as **⬆ base update**
resets it, and the shell's first line says so, in grey:

```
xbin: this tile's terminal layer moved to the new base image — system changes (apt installs, /etc) were reset; your files and $HOME are kept
```

A running terminal is never moved: it keeps its base until it ends or
restarts. An xbind upgrade restarts every session, so on an upgrade that
ships a new base, each tile's first terminal opened afterwards moves. A
layer whose old base isn't installed any more moves the same way, where
xbind used to refuse to start. Agent sessions move too. Tile sandboxes
(sandbox managers') are not touched.

The setting: admin console → workspace → **terminals**, `bx settings`, or
`GET`/`PUT /api/xbin/workspace-settings` ([protocol.md](/docs/protocol.md)).
It is kept in `data/workspace-settings.json`.

## Who's affected

Workspaces whose terminals hold installed packages or `/etc` changes they
want to keep, on an upgrade that ships a new base image. This release does
(Go 1.26.3, [changes/2026-09-30-builtins-go-1-26.md](/docs/changes/2026-09-30-builtins-go-1-26.md)).
Workspace files and every `$HOME` are kept: they are not part of the
layer.

## How to migrate

- **To keep the packages,** turn base auto-update off before anyone opens a
  terminal on this release. Before upgrading, write
  `{"baseAutoUpdate": false}` to `data/workspace-settings.json` in the
  workspace (an older xbind ignores the file). After upgrading, run `bx
  settings set --base-auto-update=false` on the host, or use the admin
  console's workspace → terminals tab (an admin tile from before this
  release gets it with `bx builtin update`). With it off, each terminal
  window offers **⬆ base update**, as before.
- **Otherwise** nothing: reinstall what you need after the move. (What a
  tile's backend needs belongs in its `setup`, whose layer is rebuilt for
  a new base by itself: [isolation.md](/docs/isolation.md) §The dev
  layer.)

## Why

A terminal on an old base runs that base's tools. This release's base
ships Go 1.26.3, and the builtins' modules now say `go 1.26.0`: a terminal
left on the old base (Go 1.24.0) downloads a Go toolchain for every `go`
command, or fails where its network can't reach `proxy.golang.org`. So
terminals now keep up with the base by default, and one workspace switch
keeps the old behaviour (D173).
