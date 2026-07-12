# The backend sandbox

Every component backend runs as a least-privileged tenant: its own Linux
namespace set over an overlay rootfs, its source mounted **read-only**, its
only door out a single unix socket, and — by default — no network at all.
This is the *runtime plane*. This page explains what that sandbox is made of,
what each layer enforces, and precisely what a compromised backend can and
cannot do.

**Related:** [09-terminals.md](09-terminals.md) (the editing plane, same
machinery, opposite defaults) · [10-resources.md](10-resources.md) (the only
things that persist) · [11-interfaces.md](11-interfaces.md) · [12-egress.md](12-egress.md)
· [13-ingress.md](13-ingress.md) · reference: [/docs/isolation.md](/docs/isolation.md),
[/docs/auth.md](/docs/auth.md) · design: plans/isolation.md, plans/isolation-impl.md,
plans/runtime.md.

## Three honesty tiers

The authorization *model* — grants, roles, the gateway, `X-XBin-From`
injection — is always enforced by xbind. How hard it is for a hostile element
to *cheat* the model depends on which isolation tier the daemon runs in. This
is deliberate: the model is right from day one, and the OS floor hardens as
you turn tiers on.

| Tier | Flag | Backends run as | A hostile element could still… |
|------|------|-----------------|-------------------------------|
| 1 | (default; dev/local) | one shared uid | read a sibling's `/proc` env (steal its instance token), open sibling sockets, write any workspace file |
| 2 | `--scope-uids` (xbind as root) | a per-scope uid | abuse only what it was granted; can't even write its own source (editing is terminal-only); vault/data enforced by file perms |
| 3 | `--isolate --rootfs <dir>` | its own full namespace set | almost nothing at the OS layer — no sibling `/proc`, no sibling sockets, only granted files mounted, no network beyond its bound `net` |

**Production is tier 3** (`plans/runtime.md`). Everything below describes it.
Tiers compose with the same daemon: `--scope-uids` and `--isolate` are
independent flags; the standard deployment uses `--isolate` rootless
(unprivileged user namespaces, no root needed) on a VM/host xbind controls.
Do not market tier 1 as element isolation — it is attribution + seatbelts,
not a jail (D9; `plans/DECISIONS.md`).

## Anatomy of one backend (tier 3)

When the proxy needs a backend and none is warm, the runner builds it (for Go,
compiles a **static** binary — `CGO_ENABLED=0` — so it runs on any rootfs),
then `sandbox.Launch` re-execs xbind itself as the hidden `__sandbox-init`
subcommand inside a fresh set of namespaces. That init process is PID 1 of the
sandbox: it assembles the mount tree, `pivot_root`s into it, installs the
guards, and `exec`s the backend. It never returns (`internal/sandbox/init_linux.go`).

```
   xbind (runner)                        the sandbox (a re-exec'd init → backend)
        │  clone(CLONE_NEWUSER|NEWNS|NEWPID|NEWIPC|NEWUTS[|NEWNET])
        │  exec /proc/self/exe __sandbox-init <spec.json>
        ▼
   ┌─────────────────────────────────────────────────────────────┐
   │ new user + mount + pid + ipc + uts namespaces (+ net, below) │
   │                                                              │
   │   /                overlay: lower = base rootfs [+ env layer]│  ← read-only
   │                            upper = THROWAWAY tmpfs           │  ← writes to / vanish on exit
   │   /proc            fresh (new pid ns)                        │
   │   /tmp /dev/shm    private tmpfs                             │
   │   /dev/{null,zero,random,urandom,tty,pts}  minimal nodes     │
   │   <component dir>  bind, READ-ONLY   ← your source           │
   │   <run dir>        bind, rw (tmpfs)  ← the listen socket     │
   │   gateway.sock     bind             ← the ONE door out       │
   │   <resource dirs>  bind, rw          ← granted fs/sqlite     │
   │   [GPU nodes+libs] bind              ← when gpu:* granted    │
   └─────────────────────────────────────────────────────────────┘
```

### The overlay: nothing you write to `/` survives

The root filesystem is an overlay. Its **lowers** (read-only) are the base
rootfs — Go/node/python toolchains and core tools — plus, if the component
declares a `setup` script, its prebuilt **env layer** (extra apt/runtime deps,
content-hashed and cached; see [10-resources.md](10-resources.md) and
`plans/component-env.md`). The **upper** is a throwaway tmpfs. So a backend can
write anywhere on `/` and it will *work* for the life of that process — and
then vanish on the next restart. **Persist only through a resource bind or a
brokered API.** This is not a quota; it is a design boundary: state lives in
resources, code lives in the (read-only) source, everything else is scratch.

### Your source is read-only

The component's own directory is bind-mounted **read-only**. A backend
therefore **cannot modify its own code** — editing is the terminal's job (the
editing plane, [09-terminals.md](09-terminals.md)), and at tier 2+ this is
enforced by the mount, not by convention. A self-rewriting workspace still
self-rewrites; it just does so through the terminal/agent plane, never by a
running backend scribbling on itself.

### The bind set

| Bind | Access | Why |
|------|--------|-----|
| base rootfs (+ env layer) | ro (overlay lowers) | the userland; your `setup` deps |
| component dir | **ro** | your source — read but never write at runtime |
| run dir (`.xbin/run/…` → tmpfs) | rw | where the backend's listen socket lands; must be tmpfs, never host disk |
| `gateway.sock` | rw (socket) | the single egress for calling xbind + other tiles |
| granted resource dirs | rw | `filesystem`/`sqlite` resources you hold ([10-resources.md](10-resources.md)) |
| GPU device nodes + driver libs | ro/dev | only when a `gpu:*` grant is approved |

Nothing else is mounted. Other components' source, other tiles' vaults, the
workspace `homes/`/`data/`/`.xbin/`, and the host filesystem are simply absent
— not permission-denied, *not there*. The gateway socket is the one door: it
is not IP egress, is never network-blocked, and carries the backend's
instance-token identity to xbind ([05-identity.md](05-identity.md)).

## UID mapping: single-uid vs a delegated range

Rootless sandboxing maps container uid 0 to the xbind unix user. How the
*rest* of the uid space maps decides whether heavier package installs work
(`internal/sandbox/launch_linux.go`):

- **Delegated sub-uid range** (preferred): if `/etc/subuid` + `/etc/subgid`
  delegate a range to the xbind user and `newuidmap`/`newgidmap` are present,
  init maps a full uid range via those setuid helpers. `apt`/`dpkg`
  post-install scripts that create system users (systemd, dbus, messagebus)
  can then `chown` to them.
- **Single-uid fallback**: with no delegated range, only container-root maps.
  Simple packages still install; anything that chowns to a fresh system user
  fails with `EINVAL` ("Invalid argument"), breaking installs midway. xbind
  logs a loud warning at startup when it falls back; `deploy/install.sh` sets
  up the range and the `uidmap` package.

xbind started as root on a workspace owned by another user drops to that
owner's uid first (D13), so bind-mounted workspaces keep sane ownership; tier-2
`--scope-uids` keeps xbind root to allocate per-scope uids instead.

## Capabilities and syscalls

A tile backend needs no privilege — it serves on a socket as its own uid — so
tier 3 strips everything (`Spec.Unprivileged`, `internal/sandbox/seccomp_linux.go`):

- **All capabilities dropped.** `dropAllCaps` clears the bounding set (so none
  can be regained across `execve`) and the effective/permitted/inheritable
  sets. The backend runs truly capability-less.
- **A seccomp block-list** (`backendDeny`) EPERMs system-damaging syscalls no
  element server needs: the whole mount family (`mount`, `umount2`,
  `move_mount`, `open_tree`, `fsopen`/`fsmount`/`fsconfig`/`fspick`,
  `pivot_root`, `chroot`), `setns`, module load/unload, `kexec_load`, `reboot`,
  `swapon`/`swapoff`, `mknodat`, `ptrace`, `bpf`, `perf_event_open`, the
  keyring calls, `acct`/`quotactl`, and the clock-setting calls. A call on a
  foreign ABI is killed outright, so the block can't be dodged via the compat
  syscall table.

### The one exception: net-provider tiles (`cap:net-admin`, D18a)

A router/firewall/VPN tile builds a real Linux dataplane *inside its own
netns* — routing tables, the `ip_forward` sysctl, `AF_PACKET` sockets. That
needs capabilities the blanket drop removes. The admin-only reserved grant
**`cap:net-admin`** switches the drop to `dropCapsExcept(netProviderCaps())`,
keeping exactly **`CAP_NET_ADMIN` + `CAP_NET_RAW` + `CAP_NET_BIND_SERVICE`** —
and nothing else; every other cap is still dropped and the same seccomp
block-list still applies (it never blocked the net syscalls; the D18a
regression was purely the caps). The kept caps are confined to the tile's own
network namespace — nothing reaches the host network or the host's
capabilities. It is admin-only to approve (like `gpu:*`), lands pending on
import, and a workspace/org policy `net` deny strips it.
See [12-egress.md](12-egress.md).

### Low ports inside your own netns

After bringing up loopback, init writes `net.ipv4.ip_unprivileged_port_start=0`
in the sandbox's network namespace, so a backend can `bind()` **any** port —
including 80/443 — inside its own netns without any capability. This is what
lets an ingress terminator tile listen on `:80`/`:443` (published to a real
host port by the L4 relay; [13-ingress.md](13-ingress.md)) while staying fully
unprivileged. It is scoped to the sandbox's netns; the host's port privilege
is untouched.

## Network namespace modes

The netns is chosen per backend by its `net` interface binding (resolved in
`internal/runner/runner.go`; details in [12-egress.md](12-egress.md) /
[13-ingress.md](13-ingress.md)):

| Mode | Meaning |
|------|---------|
| `""` / none | empty netns, loopback only — **default-deny egress**. The gateway socket still works (it's a bind mount, not IP). |
| `relay` | a TUN in the netns whose fd xbind holds; a userspace gVisor stack enforces the bound `net` policy (internet/lan) and, for ingress, dials *in* |
| `splice` | the TUN fd is spliced to a provider tile instead of running the relay — the client's egress routes through that tile |
| `hostNet` | no netns at all: shares the host network (owner escape hatch; not for ordinary backends) |

An **ingress-only** backend (a bound stream expose but no egress) still gets
`relay` plumbing — with a deny-all egress policy and no DNS — purely so xbind
can reach *into* its ports.

### The fd-passing control socket

For `relay`/`splice`, `Launch` creates a `SOCK_DGRAM` socketpair; init creates
the TUN(s) inside the fresh netns (where it has the caps to) and hands the fds
back to xbind over that socket via `SCM_RIGHTS`, in a fixed order: the **egress
TUN first**, then one TUN per **net-provider client link** (a provider tile
terminating others' egress), then this tile's own **lan-ingress legs**
(inbound links into a provider's subnet). xbind runs the userspace stack, or
splices the fd to another tile's link. The backend never sees a raw host
interface — only its TUN, carrying exactly the flows policy allows.

## Resource limits (cgroup v2)

Where xbind's cgroup is delegated (systemd `Delegate=yes`, or a container),
each backend joins a per-component cgroup leaf (`internal/cgroup`):

- **Memory** — `memory.max` 2 GiB, with a soft `memory.high` at ⅞ of that to
  throttle a gradual leak before the hard OOM. Over budget → the tile's own
  process is OOM-killed, and crash-loop backoff kicks in; nothing else is hit.
- **Processes** — `pids.max` = `max(512, ncpu×8)`. A fork bomb hits its own
  ceiling. Because every namespace needs a process, this also bounds
  namespace/mount exhaustion.
- **CPU** — `cpu.weight` (fair share): equal slices under contention, but an
  idle box lets any tile burst to all cores (no hard `cpu.max`).

Limits are tunable (`XBIN_LIMIT_MEM`). At-limit events (OOM, pids ceiling)
surface as workspace alerts (`GET /api/xbin/alerts`) so an operator sees a
degrading tile before it breaks. Disk is capped per *scope*
([10-resources.md](10-resources.md)), not per backend.

## Threat model: what a compromised backend can and cannot do

Assume a backend is fully hostile — arbitrary code execution as its own
process. On tier 3:

**It cannot:**
- read or write another component's source, vault, or resources — they aren't
  mounted;
- modify its own source (read-only) — so it can't persist a backdoor into code;
- reach `.xbin/` (the owner token, frame-token secret), `data/` (vault,
  password hashes), or any `homes/` — not mounted;
- see another backend's `/proc`, environment, or sockets — separate pid/ipc
  namespaces;
- make any IP connection unless its `net` interface is bound — empty netns;
- mount, load a kernel module, `ptrace`, `bpf`, set the clock, or reboot —
  caps dropped + seccomp;
- escalate via a nested user namespace to regain caps — it has none to inherit,
  and the seccomp block-list denies the mount family it would need anyway;
- exhaust the host — cgroup memory/pids/CPU caps degrade it alone.

**It can:**
- do anything within its own process and throwaway overlay (compute, crash,
  leak memory up to its cap);
- write its own granted resource dirs and call APIs it holds grants for — i.e.
  exactly the authority the owner approved, no more;
- reach the gateway socket to call xbind and any tile it's been granted (this
  is *the* lateral surface — it is why grants are the security boundary, and
  why `xbin:admin` is the heaviest grant in the system, [06-authorization.md](06-authorization.md));
- make egress *only* through its bound `net` provider, metered and policy-filtered.

The residual boundary is the same as any single-tenant model: xbind and the
sandboxes share a host, and same-origin element *frontends* are one browser
trust domain (frame tokens give attribution, not isolation). The outer wall is
the VM/host xbind runs on. Within that wall, tier 3 reduces a hostile backend
to "its own grants and nothing else" — which is the whole point of building the
authorization model on capability edges rather than ambient trust.
