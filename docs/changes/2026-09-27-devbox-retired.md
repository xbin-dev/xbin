# 2026-09-27 — the `devbox` builtin tile is retired (D115)

## What changed

xbind no longer ships the `devbox` builtin tile (rootless Podman containers
you could SSH into). It is gone from the Tile Manager's Import tab and from
`bx tile ls`. Importing it — `bx tile import devbox`, or
`POST /api/xbin/builtins/import` with `{"name":"devbox"}` — now fails with
**410 Gone** and a message saying what replaces it.

Coding sandboxes come from **sandbox managers** instead: tiles that provide
the `sandbox-manager` service ([sandbox-manager.md](../sandbox-manager.md),
protocol 1). The builtin manager, the **`coding-sandbox`** template
(sandboxes on xbind's own VM runtime), is on its way; the
**`sandbox-terminal`** tile (browser terminals and SSH into sandboxes, for
people) ships since 2026-09-28. The agent template binds the same managers
for its coding tools.

`cap:containers` is unchanged: any tile may still run containers the way
[2026-07-14-container-tiles.md](2026-07-14-container-tiles.md) describes.

## Who's affected

- **Workspaces that imported devbox.** Nothing is removed. An import is a
  copy, and the copy is yours: it stays where it is (`apps/devbox` by
  default), keeps its grants and bindings, and runs as it did. What stops:
  xbind no longer offers it updates — `bx builtin updates` and the Tile
  Manager don't list it, and `bx builtin update devbox` says it is retired.
  Its entry in `.xbin/builtins.json` stays and is ignored.
- **Scripts, docs and agents that run `bx tile import devbox`** — they now
  get the error above.

## How to migrate

- **Keep your copy** if it does what you need. It is ordinary workspace code
  now; fix and extend it like any tile you own. Its source is in every
  xbind released before this change (`builtin-tiles/devbox` in xbin's
  history).
- **Drop it** if you never got it working: delete the tile like any other,
  then revoke its `cap:containers` grant and remove its ingress mapping (the
  SSH port you published with `bx expose`), if you made them.
- **For coding sandboxes**, use a sandbox manager: the `coding-sandbox`
  template once it ships, or your own tile implementing
  [sandbox-manager.md](../sandbox-manager.md) — for example one that drives
  a cloud's API and ssh. For SSH into them, import the `sandbox-terminal`
  tile (`bx tile import sandbox-terminal`), bind it to the manager and
  publish its `ssh` port — what devbox's SSH port was for.

## Why

The devbox tile never really worked, and fixing it would have kept coding
sandboxes a one-tile special case. They are now a service contract with
managers and consumers (D115): the manager knows the substrate (VMs,
containers, a cloud's API), the consumer knows who and why (the agent's
conversations, people's terminals), and the builtin manager runs on xbind's
own sandbox runtime instead of nesting containers inside one tile.
