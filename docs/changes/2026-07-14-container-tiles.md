# 2026-07-14 — container-host tiles need the `cap:containers` grant

A **container-host tile** — one that runs rootless Podman/Docker inside its
sandbox to spawn sub-containers (the new `devbox` builtin; any "dev sandbox"
tile) — must hold the reserved capability grant **`cap:containers`**.

## Why

An ordinary tile backend runs fully unprivileged: all Linux capabilities
dropped, plus a seccomp block-list that denies the mount family, `pivot_root`,
`setns`, and `mknod`. A container runtime needs all of those to build each
container's rootfs, `/proc`, and overlay layers, so it fails immediately
without this capability.

## What to do

Declare it in the tile's `xbin.json`:

```jsonc
"uses": [
  { "target": "cap:containers", "role": "writer" }
  // …its other uses (a filesystem resource for image storage, a net interface)…
]
```

…then approve the pending grant (admin-only; grants panel, or
`bx grant <tile> cap:containers:writer`). The backend restarts with its
user-namespace capabilities kept and a minimal seccomp floor.

The capability only lifts the *kernel* blocks. The tile's own `setup` still
installs the runtime and seeds nested rootless config — Podman, `uidmap`,
`fuse-overlayfs`, `pasta`/`slirp4netns`, a `/etc/subuid`+`/etc/subgid` entry —
and its manifest declares a `filesystem` resource for image storage and a `net`
interface for container egress. See `plans/containers.md` and the `devbox`
tile for the full recipe.

## Scope of the grant

`cap:containers` keeps the tile's capabilities **inside its own user
namespace** (rootless — nothing reaches the host or the host's capabilities)
and replaces the block-list with a floor that still denies module loading,
`kexec`, `reboot`, swap, and clock/accounting syscalls. Everything else about
the sandbox is unchanged: own user+mount+pid+ipc+uts+net namespaces, no host
reach, no other-tile reach. It's **admin-only** to approve, and a workspace/org
policy **`xbin-caps`** deny strips it.

## Prerequisite (host)

Nested rootless containers need a **delegated sub-uid/sub-gid range** for the
xbind user (`/etc/subuid`/`/etc/subgid` + the `uidmap` package) — the same
prerequisite `apt` in tile terminals already needs. Without it the tile falls
back to single-uid mode and multi-user container images break.
