# The terminal plane

A terminal is a real, persistent shell — the *editing plane*, where you (or an
agent) read and change code. It runs in the same namespace-over-overlay
sandbox as a backend, but with the defaults inverted: your own tile's directory
is **read-write**, the rest of the workspace is read-only, secrets are masked
out, and the shell acts as the *tile*, never as you. This page is the exact
mount picture and the guards that hold it up.

**Related:** [08-sandbox.md](08-sandbox.md) (the backend sandbox this reuses) ·
[05-identity.md](05-identity.md) (terminal tokens) · [07-users-orgs.md](07-users-orgs.md)
(who can see which tile) · [12-egress.md](12-egress.md) · reference:
[/docs/isolation.md](/docs/isolation.md), [/docs/protocol.md](/docs/protocol.md)
(`/ws/term` wire protocol) · design: plans/terminal-tokens.md, plans/runtime.md,
plans/component-env.md, `plans/DECISIONS.md` D6/D16/D17/D17a/D18.

## Two planes, opposite defaults

The backend is the *runtime* plane (least-privileged tenant, source read-only,
no egress). The terminal is the *editing* plane. A terminal is always opened
**on a tile** — there is no free-floating workspace shell:

- **Root terminal disabled.** A shell on the workspace root (`cwd == ""`) was
  the whole-workspace owner plane; it is refused outright. It is reachable from
  no UI, and workspace-wide work (editing `xbin.json`, cross-component git)
  belongs in the browser admin tile or a host-side `bx`.
- A **component terminal** needs `terminal` level on that tile (D16;
  [06-authorization.md](06-authorization.md)). It `cd`s into the component's
  source directory and runs as the xbind unix user.

Backend defaults: source read-only, no egress. Terminal defaults: own source
read-write, **public-internet egress on** (you usually want `git clone` /
`go get` / `npm i`). Same sandbox, mirrored posture.

## The mount picture

A component terminal (`internal/term/term.go` → `scopedBinds` /
`sandboxShell`) assembles this:

```
   /                     base rootfs overlay
   │                       lower = base rootfs (pinned to this layer's base)
   │                       upper = .xbin/term/<tile-key>/   ← PERSISTENT dev layer
   │                                (apt installs, /etc tweaks survive sessions)
   │
   <workspace root>      bind, READ-ONLY          ← read every tile's source…
   │
   ├─ .xbin/             MASK (empty tmpfs, sealed)   ← owner token, frame secret
   ├─ data/              MASK (empty tmpfs, sealed)   ← vault, resource state, users.json
   ├─ homes/             MASK (empty tmpfs, writable) ← every OTHER user's $HOME
   │   └─ <you>/         bind, READ-WRITE (nested)    ← YOUR $HOME, back in
   ├─ apps/<your-tile>/  bind, READ-WRITE (nested)    ← …but write only YOUR tile
   ├─ apps/<hidden>/     MASK (empty tmpfs, sealed)   ← tiles below your read level (D17a)
   │
   <SDK path>            bind, READ-ONLY (ExtraBind)  ← so `go build` resolves
```

The ordering matters: shallower mounts land first, so the read-write tile dir
and `$HOME` **nest on top of** the read-only workspace ground and shadow it at
their paths, and a mask over `homes/` still lets the own `$HOME` re-mount
inside it (`internal/sandbox` sorts binds ancestors-first).

### In-tile code is read-write; every other tile's code is read-only

The workspace is bound read-only so a terminal can **read every tile's
source** — you routinely need to see another tile's API to integrate against
it — but the only writable code is **your own component's directory**. A rogue
agent in a component terminal can touch only its own component and its `$HOME`:
never workspace state (`xbin.json`, `AGENTS.md`, `go.work`), runtime `data/`,
`.xbin/`, or another tile's files. Commits still work because **each component
is its own git repo** — `git commit` writes `.git` inside the writable
component dir even while the root is read-only. That is also what makes a
component *installable*: importing from a git URL is just a clone.

### `$HOME`: per user, shared across that user's terminals

`$HOME` is `<workspace>/homes/<user>` — **one home per signed-in human** (the
root token gets `homes/owner`; `internal/term/homes.go`), read-write in every
terminal including component ones. Within a user it is *shared*: agent-CLI
config (`~/.claude`, credentials), shell history, and dotfiles live there once
and follow you into every terminal you open, on every tile — a home per human,
not per tile. It is seeded with skeleton dotfiles on first use (lazy — user
accounts are dynamic) and **survives xbind upgrades** (it's workspace data, not
part of any rootfs). Other users get their own homes, masked from yours, so
configs never mix. A legacy shared `home/` is migrated once to `homes/<user>`
at startup (picking the sole user/admin, else `owner`; refusing to guess if
both forms hold real data), and `homes/` is force-added to `.gitignore` because
it holds credentials.

This is **hygiene, not a security boundary**: all terminals share one unix
user, so a determined shell could reach another home at the filesystem layer
were it not masked. The real per-session boundary is the API credential (below).

### The masks

Three things are covered with an empty tmpfs even though the root is bound
read-only — without them the read-only bind would re-grant owner (a shell could
`cat .xbin/token` and become root):

| Masked | Contains | Cover |
|--------|----------|-------|
| `.xbin/` | owner token, frame-token secret | sealed (ro) |
| `data/` | vault, encrypted resource state, `users.json` password hashes | sealed (ro) |
| `homes/` | every *other* user's `$HOME` | writable, so the own `$HOME` nests back |

These apply to **every** terminal, including one on a tile you own.

### Per-user source visibility (D17a)

For a **non-admin** user, the mount goes further: every tile **below that
user's `read` level** is masked out with a sealed cover — its source
disappears from the terminal's filesystem, not just from the sidebar. This is
the filesystem half of tile-list filtering; what's readable is decided by the
org/team access resolution ([07-users-orgs.md](07-users-orgs.md)). Admins see
all source (the mask list is empty for them). The mask is skipped for any dir
overlapping the shell's own cwd — a defensive belt, since `terminal` level
implies `read` so it can't legitimately happen.

> **Known limitation.** The read-only workspace bind is *recursive* (the only
> option in a rootless userns — the kernel locks every inherited mount), so the
> per-resource gocryptfs (`resenc`) submounts are carried in. The `.xbin` mask
> shadows their *contents*; their *names* may still appear in the terminal's
> `/proc/self/mountinfo`. Benign — a terminal can already `ls` every tile — and
> unfixable in place; truly hiding them needs resource storage outside the
> workspace tree (`/docs/isolation.md`).

## The guards

The shell is uid 0 in its own user namespace and (for an admin) keeps
`CAP_SYS_ADMIN` — so `apt`, nested namespaces, and profiling work — which would
let it `umount` a mask and read underneath. Two independent guards, installed
just before `exec` and inherited across `execve` and every later `unshare`,
close that:

- **Mount guard (seccomp).** `mountGuardProgram` EPERMs exactly the four ways
  to remove or relocate a mount — `umount2`, `move_mount`, `open_tree`, and
  `mount(MS_MOVE)` — so the masks can't be peeled without dropping the cap.
  Plain `mount`, `unshare`, `pivot_root`, `clone` stay allowed, so most nested
  work still functions. *Collateral:* tools that unmount (`fusermount`, some
  container/browser sandboxes) get `EPERM` — run those on the host.
- **Read guard (Landlock).** A VFS-level allow-list on `LANDLOCK_ACCESS_FS_READ_FILE`:
  even if a mask *were* peeled, the secret file *contents* still can't be
  opened. It grants read on everything legitimate and withholds it on the
  workspace's `.xbin/`, `data/`, and other users' `homes/`.

  The allow-list is built by **walking `/` down to the workspace root, granting
  every sibling at each level** — so only the workspace subtree itself is left
  to the selective per-child grants, and everything outside the workspace stays
  readable at any nesting depth. This is load-bearing and was gotten *wrong*
  once: an earlier version excluded the workspace's whole top-level path
  component, which — on the standard `/opt/xbin/workspace` layout — read-blocked
  everything else under `/opt`, including the SDK bind at `/opt/xbin/sdk`
  (`cat`/`go build` failed on world-readable files while `ls` worked, because
  only *reads* are restricted; fixed 2026-07-12). The terminal's explicit
  read-only extra mounts (the SDK) are additionally listed in `AllowUnder`, so a
  bind the sandbox itself makes can never be read-blocked wherever it lands. The
  guard also grants `LANDLOCK_ACCESS_FS_REFER` on every allowed path, or an
  ABI-2+ kernel would `EXDEV` every cross-directory `rename` — which breaks
  `apt`'s `partial/ → parent` move. Best-effort: a no-op on kernels without
  Landlock (the masks + mount guard remain).

The masks give hygiene; the two guards make the secrets **unreadable even
against a deliberately adversarial shell** — peeling is blocked *and* reading is
blocked, independently, so a single-layer failure doesn't expose anything. The
admin console's runtime tab shows each guard's kernel support.

## Restricted terminals (non-admin users, D18)

Admin/owner terminals keep full caps (dev shells mount and nest containers). A
terminal opened by a **non-admin** gives that power up entirely — an untrusted
tier — while still running `apt`:

- **Caps dropped to a file-only set.** `dropCapsExcept(aptSafeCaps())` keeps
  only what `dpkg` needs to unpack as root (`CHOWN`, `DAC_OVERRIDE`, `FOWNER`,
  `FSETID`, `MKNOD`, `SETFCAP`, `SETUID`, `SETGID`, `SYS_CHROOT`, `KILL`,
  `NET_BIND_SERVICE`). Notably gone: `CAP_SYS_ADMIN` (mount/namespaces) and
  `CAP_SYS_RESOURCE`.
- **No new namespaces.** Dropping `CAP_SYS_ADMIN` isn't enough — creating an
  *unprivileged* userns needs no capability, so a capless shell could
  `unshare -Ur` back into a full cap set. So init writes
  `/proc/sys/user/max_user_namespaces=0` + `max_mnt_namespaces=0` **inside** the
  terminal's userns (the check lives in `create_user_ns`/`copy_mnt_ns`, below
  the syscall layer, so it's immune to `clone3`'s in-memory flags) and *then*
  drops `CAP_SYS_RESOURCE`, so the shell can't raise the limit back — a closed
  loop. A seccomp filter (`restrictNSProgram`) EPERMs `clone`/`unshare`
  with `NEWUSER|NEWNS` and ENOSYS's `clone3` (ENOSYS not EPERM — glibc must
  fall back to `clone`, or it aborts `pthread_create` and breaks apt) as a
  belt-and-suspenders backstop on kernels where the knob doesn't take.

Why apt still works but `unshare -Ur` doesn't: apt only ever needed file caps
(kept) and never a namespace (blocked). The trade-off is rootless podman /
nested `bwrap` / Chrome's own userns sandbox (use `--no-sandbox`). The mount +
read guards still apply underneath — defense in depth, not a replacement.

## Terminal identity: the shell acts as the tile

The shell's API credential is a **per-session tile-scoped terminal token**
(`plans/terminal-tokens.md`). Its `XBIN_TOKEN` resolves to *the tile's element
principal* — self-admin plus the tile's approved grants — **never** the driving
user's privilege, and **never** the owner token (which is filtered out of the
env entirely; `getenv` is first-match, so it's stripped, not just overridden).
An admin opening a terminal on a low-trust tile does not lend it their power;
the tile acts as itself. The token is minted per session and revoked the moment
the session dies. `bx`/`curl`/`git` inside the terminal use it: xbind rewrites
`http://xbin/…` to the reachable `XBIN_URL` and attaches the bearer pinned to
that URL, so a template instance's `template` git remote can fetch.

Two user flags gate what the token can do (D17 b/c; clamped, never rejected, so
an ungranted user still gets a working shell):

- **`termApi`** — without it, or with the titlebar **tile-API/no-API** switch
  off (`?api=0`), the session is minted with **no token at all**: the shell
  reads and edits source but every call to the tile's (or xbin's) API is
  unauthorized. Use it for untrusted code that should see code but not act.
- **`termNet`** — without it, internet egress is clamped to `none`.

## Network scopes per session

A terminal picks a scope when it opens (`?net=`); the netns/relay is fixed at
spawn, so switching scope restarts the session:

| Scope | Meaning |
|-------|---------|
| `internet` *(default)* | its own netns + an egress relay permitting the **public internet only**; host interfaces and LAN stay hidden. xbind stays reachable via a host-forward on the relay gateway `10.0.2.2` (`XBIN_URL` is transparently rewritten so `bx`/`curl` reach it without any host interface exposed). |
| `host` | shares the host network (LAN + host services). Owner escape hatch — **admin-only**, clamped to `none` for non-admins. |
| `none` | isolated netns, **no egress at all** (airgapped; even xbind is unreachable). |

## The dev layer: persistent, per-component, resettable

System changes you make in a terminal — an `apt install`, an `/etc` tweak, an
extra toolchain — land in a **persistent per-component overlay upper** at
`.xbin/term/<tile-key>/` and survive across sessions and restarts (the base
rootfs is read-only; nothing is lost at exit). Each tile gets its own long-lived
dev box. Keep it distinct from the **component env layer** (built from the
manifest's `setup`, read-only, what the *backend* gets): `apt install` in a
terminal to try things; move anything the backend needs into `setup`.

> Only one live session may hold a given tile's persistent layer (concurrent
> overlay mounts of one upperdir would corrupt it). A second concurrent terminal
> on the same tile falls back to an **ephemeral** upper — functional, but its
> system changes don't persist that session.

**Reset** (`DELETE /ws/term/env?cwd=<tile>`, the terminal's ⟲ button) kills any
live session on the layer and wipes the upper back to a clean base — safe,
because your code and `$HOME` are bind mounts, not part of the overlay. Reset
needs `terminal` level on the tile; resetting the (disabled) root layer is
admin-only.

## How tiles and terminals share the filesystem

Three planes touch a tile's files, and they compose along **three different
axes** — this is the subtle part, so here it is in one place.

**The tile source is one real directory, shared live.** A terminal's own-tile
bind (`{Src: root/<tile>, RO: false}`) and the backend's source bind
(`{Src: <tile>, RO: true}`) point at the **same real path** under the
workspace — not copies. So the loop closes on itself: you edit a file in the
terminal, the same bytes are what the backend reads, the workspace watcher
fires (300 ms debounce), the registry rescans, and the backend rebuilds and
blue/green-swaps ([03-components.md](03-components.md)). That is the whole
"editing plane feeds the runtime plane" mechanism — no sync step, no copy.
Every principal with `terminal` on the tile writes that same directory (last
write wins on disk, then a rebuild); the backend only ever reads it.

**`apt install` in a terminal does *not* reach the backend.** They live in
different overlays. A terminal's system changes land in the tile's persistent
dev layer (above); the backend gets the base rootfs plus the read-only **env
layer** built from `setup` ([08-sandbox.md](08-sandbox.md)) and a throwaway
upper. The only paths that cross from terminal to backend are the tile
directory (shared, above) and brokered resources — never installed packages or
`/etc` edits. Move anything the backend needs into `setup`.

**The dev layer is per *tile*, not per *user*.** Its key is the component path
alone — so two users with `terminal` access to the same tile contend for one
`.xbin/term/<tile>` layer: whoever opens first holds it, the other gets an
ephemeral upper that session. When the holder closes, the next session — *even
a different user* — inherits that layer: their `apt` installs, their `/etc`
tweaks, and any file they wrote **outside** the tile dir and `$HOME` (into
`/opt`, `/var`, `/tmp`, …). The layer belongs to the tile's dev box, not to a
person. `$HOME` is the opposite: keyed per user, so credentials and dotfiles
never cross between users on the same tile. (In a single-owner workspace this
is all one human; it matters once a tile has terminal access from more than
one user — e.g. a shared org tile.)

| What | Scoped by | Shared across | Backend sees it? |
|------|-----------|---------------|------------------|
| Tile **source** (`<tile>/…`) | the tile | everyone with `terminal` on the tile — one real dir | **yes**, read-only → triggers rebuild |
| Dev **layer** (`.xbin/term/<tile>`: apt, `/etc`, stray writes) | the tile | **all users** of that tile (serially; one live holder, rest ephemeral) | no — separate overlay |
| **`$HOME`** (`homes/<user>`) | the **user** | that user's terminals on **every** tile | no — a private bind |
| Other tiles' source | — | read-only if within your read level; masked otherwise (D17a) | n/a |
| Secrets (`.xbin`, `data`, other homes) | — | nobody (masked + read-guarded) | no |

So: **source is per-tile-and-live**, **the dev layer is per-tile-across-users**,
**`$HOME` is per-user-across-tiles**. Three axes, three answers — and the
backend shares exactly one of them (source, read-only).

> **Cross-user dev-layer sharing is a design choice, not an accident** — the
> layer models "this tile's dev box," which is a property of the tile. It does
> mean a lower-trust user with terminal access to a shared tile inherits and can
> read a higher-trust user's *system-level* artifacts on that tile (never their
> `$HOME`). If per-user isolation of the dev layer is ever wanted, the layer key
> would move from the tile to `(tile, user)` — at the cost of every user
> re-installing per-tile apt deps. Recorded as an open design point.

## Base images and their lifecycle

The sandbox lower is a base rootfs directory. Because a persistent upper records
apt/dpkg state *relative to the base it was built on*, stacking it on a
**different** base merges new-base packages under an old dpkg status and breaks
apt. So each layer is **stamped and pinned** to its base
(`internal/term/base.go`):

- **`ensureLayerBase`** stamps a layer with its base version on first use (a
  brand-new layer → the current base; a pre-existing unstamped upper → the
  legacy `v0`).
- **`resolveBase`** pins the layer's upper to the exact base it was built on —
  the current rootfs if it matches, else a preserved sibling `<rootfs>-<version>`.
- **`CheckBaseImages`** is a startup safety gate: xbind **refuses to start** if
  any existing layer is pinned to a base that isn't installed, rather than
  corrupt its apt state. A base upgrade must therefore *preserve* old bases as
  `<rootfs>-<version>` siblings (`deploy/install.sh` does this on upgrade); the
  fix if you hit the gate is to restore the base or reset the affected
  terminal(s).
- **`GCBaseImages`** releases preserved bases that no layer pins anymore — the
  cleanup side, so old bases don't accumulate once every terminal has upgraded.

A terminal whose layer's base is older than the current rootfs reports
`baseOutdated` on attach, so the UI can offer a reset-to-upgrade.

## Resource limits and GPUs

Where xbind's cgroup is delegated, each **restricted** session joins its own
cgroup leaf with the same memory/pids/CPU caps as a tile backend (D17d) — a
runaway build or fork bomb degrades that one shell, not the workspace. Admin
terminals stay unlimited (dev builds are hungry). Session caps: **32 per user,
64 global**, so no one person exhausts the pool. GPUs are opt-in per session
(`?gpu=all|<index>`, owner plane) — the device nodes and driver libs are bound
in like a `gpu:*` backend grant.

## The logs tab

The terminal window also carries a read-only **logs** tab (the `▤` button):
it streams the tile backend's captured stdout/stderr live in an xterm view (no
input), backed by `GET /api/xbin/logs`. It is gated exactly like the tile's
terminal — admin, the tile itself, or a `terminal`-level user — so it appears
only where a shell would, since backend output can carry secrets.
