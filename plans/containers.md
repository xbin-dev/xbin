# cap:containers — running containers inside a tile

A **container-host tile** runs rootless Podman (or Docker) inside its own
sandbox to spawn sub-containers — the substrate for "dev sandbox" tiles. It is
the third reserved sandbox capability, built on the exact pattern of
`cap:net-admin` (DECISIONS D18a): an admin-only grant that relaxes one tile's
sandbox profile, contained to that tile's own namespaces.

## The problem

An ordinary tile backend is `Unprivileged`: **all Linux capabilities dropped +
a seccomp block-list** (`backendDeny`) that denies the whole mount family,
`pivot_root`, `setns`, `mknod`. Every container runtime needs exactly those to
build a container's rootfs / `/proc` / overlay, so a container runtime dies
immediately in an ordinary tile. `unshare`/`clone` are *not* blocked (nested
namespaces per se are allowed) — but a namespace you can't `mount` into is
useless for containers.

## The capability

`cap:containers` (a reserved grant target, admin-only, `internal/broker` →
`ContainersFor`) flips the backend into a **container-host profile**
(`Spec.Containers`):

- **Keep the user-namespace capabilities** instead of dropping them — rootless
  Podman as userns-root needs `CAP_SYS_ADMIN` (nested user/mount/net
  namespaces, mounting overlay/tmpfs/proc per container), plus the file/uid
  caps. They are **userns-scoped** (rootless): `CAP_SYS_ADMIN` in the tile's
  own userns cannot touch the host.
- **A minimal seccomp floor** (`containerDeny`) instead of the block-list: only
  the host-damaging syscalls — module (un)loading, `kexec`, `reboot`, swap,
  time/accounting. The mount family, `pivot_root`, `setns`, `mknod`, `ptrace`,
  `bpf` all stay allowed. This is deliberate: **the tile's filter is inherited
  by every container it spawns** and can only be tightened, never relaxed, by
  Podman's own per-container profile — so a wider floor here would silently
  break `strace`/`gdb`/`mount`/`unshare` inside the dev containers. Cross-
  container reach is prevented by per-container pid/mount/net namespaces
  (Podman's job), not by the floor.

Everything else is unchanged: the tile is still rootless, still in its own
user+mount+pid+ipc+uts+net namespaces, still reaches nothing on the host and
no other tile. A backend carries **no workspace-secret masks** (unlike a
terminal — it only binds its own dir + granted resources), so no mount/read
guard is needed.

## Governance

- **Admin-only to approve** — a reserved target, never same-scope
  auto-granted; it lands pending in the grants panel on import.
- The policy ceiling's **`xbin-caps` deny** class strips it (an org that
  forbids system capabilities for its tiles forbids container hosts too).
- Approving it restarts the tile's backend (spawn-materialized, like
  `cap:net-admin`).

## What a container-host tile still needs (environmental, in the tile)

The capability lifts the *kernel* blocks; the tile's own `setup` supplies the
userland:

- **Rootless Podman** (daemonless; nests more cleanly than dockerd) + `uidmap`
  + `fuse-overlayfs` + `pasta`/`slirp4netns`.
- **Nested subuid/subgid**: the sandbox already maps the tile to a delegated
  sub-uid RANGE (via `newuidmap`, when the host has `/etc/subuid` delegated to
  the xbind user — the same prerequisite `apt` needs). Podman inside needs its
  own `/etc/subuid`/`/etc/subgid` entry so it can subdivide that range for its
  containers' users; `setup` seeds it. Without a delegated range the tile falls
  back to single-uid and multi-user container images break — same limitation
  xbind documents for its own sandbox.
- **Persistent storage**: a `filesystem` resource, with Podman's `--root`
  pointed at it, so images/containers survive restarts and don't sit in the
  throwaway tmpfs upper (which is RAM). The `vfs` storage driver is the robust
  default (no FUSE-on-FUSE-on-gocryptfs stacking); `fuse-overlayfs` is faster
  where it works.
- **Container networking**: a bound `net` interface. Podman's rootless network
  (pasta/slirp4netns) NATs the containers' egress out through the tile's own
  egress relay. `net=host` is the simplest, most robust option for a dev box
  (containers use the host network directly); `net=internet` keeps them
  contained at some perf cost (a userspace stack over the gVisor relay).

## Worked example

`builtin-tiles/devbox` — creates/removes Podman containers and exposes their
shells over SSH: an `exposes` `ssh` stream port (→ a host TCP port via the
runtime L4 relay) fronted by an in-tile Go SSH server that authenticates the
owner's public key and routes `ssh -p<port> <container>@host` to
`podman exec -it <container>` (plans/ingress.md for the stream plane).

## Decisions

- **CT-1** — container support is an **opt-in reserved capability**
  (`cap:containers`), admin-only, ceiling-strippable — never on by default.
  The blast radius is one tile's sandbox, contained to its own namespaces.
- **CT-2** — the sandbox provides only the **kernel relaxation** (caps +
  minimal seccomp floor); the runtime, subuid seeding, storage, and networking
  are the **tile's** `setup`/manifest, so xbind stays a minimal middlebox and
  the container runtime is a swappable tile concern (podman today, docker/
  runsc possible).
- **CT-3** — the seccomp floor is **minimal on purpose** (host-damage only),
  because it is inherited by every nested container; per-container hardening is
  Podman's profile, layered on top.

## Touchpoints

`internal/sandbox` (`Spec.Containers`, the init branch, `containerDeny`/
`installContainerSeccomp`) · `internal/runner` (`ContainerCaps` hook) ·
`internal/broker` (`ContainersCap`/`ContainersFor`, ceiling classification,
grant restart) · `cmd/xbind` (wire `run.ContainerCaps`) ·
`builtin-tiles/devbox` (the worked example) · `workspace-template/AGENTS.md`
(how to build a container-host tile).
