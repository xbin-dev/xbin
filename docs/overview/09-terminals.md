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
(`/ws/term` wire protocol — including the per-frame echo acks and the
ping/pong the browser's predictive echo runs on, `internal/termwire`,
D70) · design records in the xbin repo: `terminal-tokens`, `runtime`,
`component-env`; decisions D6/D16/D17/D17a/D18/D70.

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

### The session directory: whose sessions are where (D73)

A session is owned by its creator (the same key as `$HOME`) — and xbind is
the only place that knows which live sessions a user has on a tile:
`GET /api/xbin/term/sessions?cwd=` lists them (id, effective scope, the
pickers it was opened with, the tab's name — set with `PATCH
/term/sessions/<id>`), and a `term` event on `/ws/events` tells the owner's
browsers (and admins) when one opens, ends or is renamed — and, for an
agent session, when its status or the number of requests waiting for an
answer changes (op `status`, the summary inline: an inbox needs nothing
else). `<bx-frame>` builds
its tab bar from that answer and keeps no session ids of its own, so a
second browser signed in as the same user shows the same tabs and attaches
to the same PTYs (several sockets may attach to one session; output fans
out), while another user on the same browser sees only their own. The
window's state (open, active tab, geometry) is a per-user pref. Reattaching
re-checks the tile's terminal level: a creator whose level was withdrawn is
refused (403) — the session lives on until it ends or is reaped. Sessions
are in-memory: an xbind restart ends them, directory included.

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
([/docs/isolation.md](/docs/isolation.md) §Terminal isolation). Its `XBIN_TOKEN` resolves to *the tile's element
principal* — self-admin plus the tile's approved grants — **never** the driving
user's privilege, and **never** the owner token (which is filtered out of the
env entirely; `getenv` is first-match, so it's stripped, not just overridden).
An admin opening a terminal on a low-trust tile does not lend it their power;
the tile acts as itself. The token is minted per session and revoked the moment
the session dies. `bx`/`curl`/`git` inside the terminal use it: xbind rewrites
`http://xbin/…` to the reachable `XBIN_URL` and attaches the bearer pinned to
that URL, so a template instance's `template` git remote can fetch. Those are
two `GIT_CONFIG_*` pairs in the session's env, never the tile's `.git/config`.
While a tile has a deployment record (its live reload is paused, or it has
tile deployments), a session opened on it gets two more, `GIT_CONFIG_COUNT`
becoming 4: a fetch-only `xbin-deploy` remote. `git fetch xbin-deploy`
brings `deploy/<name>` for every pinned deployment — the git view of the
checkpoint it runs, ignored files left out — so `git checkout --no-track -b
hotfix deploy/main` branches from exactly what `main` runs
([/docs/tile-deployments.md](/docs/tile-deployments.md)).
A session opened before the record existed doesn't have it (a session's env
is fixed at spawn): open a new one, or fetch by URL
(`git fetch http://xbin/api/xbin/checkpoints/<tile>.git '+refs/heads/deploy/*:refs/deploy/*'`).

**A session's target.** On a tile with tile deployments, a terminal or agent
session's API calls and `bx` reads reach one deployment, fixed for the
session's life: its target. The title bar's tile-API select picks it — `🔌
target: main (primary)`, `🔌 target: dev`, …, `⛔ no API` — once the tile has
more than an unprotected `main`; a tile without deployments keeps the two
entries below. The default is the primary; a protected primary is never
offered, and the default then falls to the live reload target or, when live
reload is paused too, to no API. Switching restarts the session (an agent
resumes its conversation), and the select shows what the server echoed in
the session frame, never a guess. A session whose target isn't the primary
gets `XBIN_DEPLOYMENT=<name>` (absent otherwise, so a tile without
deployments sees today's env) and prints a grey line — `this terminal calls
apps/crm+dev`, or why the tile API is off. Protecting the primary restarts
the sessions that targeted it onto the default. The target rides
`?deployment=` on `/ws/term` and on `POST /api/xbin/term/sessions` (`bx
agent run --deployment <name>`), is echoed in session listings and `term`
events, and is recorded on agent history entries; agent history itself stays
per tile. A session's own calls can't reach another deployment of its tile
— switching is a restart, a terminal-level act
([/docs/tile-deployments.md](/docs/tile-deployments.md)).

**On a partitioned tile** ([/docs/partitions.md](/docs/partitions.md), in
development) a terminal or agent session belongs to the person who opened
it. Its token needs nothing new: xbind puts a person's terminal in their
own partition (`user:<id>`), so its API calls and `bx` reach that
partition's instance and data only — an admin's too, never someone else's.
The session gets `XBIN_PARTITION=user:<id>` (after `XBIN_COMPONENT`, and
after `XBIN_DEPLOYMENT` when that is set; a session targeting a non-primary
deployment says `global`), starts in `$HOME` rather than the tile directory
— which is code every partition shares, so keep your own files in `$HOME` —
and prints a grey line saying so. A session without a person (the owner
token) acts in the tile's global instance, or, on a tile without one, opens
with the tile API off. Admins can end other people's sessions there but not
reattach to, drive, rename or read them — nor read them viewing as the
person — and listings leave their names out. (Without `--isolate` a terminal
is a host shell that can read every partition, and its grey line says so.)

Two user flags gate what the token can do (D17 b/c; clamped, never rejected, so
an ungranted user still gets a working shell):

- **`termApi`** — without it, or with the titlebar **tile-API/no-API** switch
  off (`?api=0`; on a tile with deployments, the same select's `⛔ no API`),
  the session is minted with **no token at all**: the shell
  reads and edits source but every call to the tile's (or xbin's) API is
  unauthorized. Use it for untrusted code that should see code but not act.
- **`termNet`** — without it, internet egress on a personal/workspace tile is
  clamped to `none`. On an **org-owned** tile the org's network sets decide
  instead (D54): the terminal gets the `org` scope, `termNet` or not. On a
  **personal** tile whose owner has network sets the `personal` scope is
  added (D88) — and plain `internet` also opens when those sets hold it.
- **`noTerminal`** (account switch, D88) — no session at all, on any tile.

## Network scopes per session

A terminal picks a scope when it opens (`?net=`; absent = the tile's default);
the netns/relay is fixed at spawn, so switching scope restarts the session. The
session frame lists the scopes *this* user may pick on *this* tile (the menu
renders exactly that) and explains any clamp:

| Scope | Meaning |
|-------|---------|
| `org` *(default on org-owned tiles with network sets)* | its own netns + the relay under the **owning org's network sets** — the same reach the org's tiles get (`lan:` ranges, pinned hosts, the internet if a set says so); host networking when a set grants `host`. Members need no `termNet`. |
| `personal` *(default on personal tiles whose owner has network sets)* | the same, under the tile **owner's personal network** — their network sets ∪ the workspace personal defaults (D88). Added to the other scopes, not replacing `internet`. |
| `set:<name>` | one **named network set** — the relay under exactly that set's rules (host networking when it says `host`). Listed for each set of the tile owner's network (an org's attached sets, a personal tile owner's sets), for whoever may open a terminal there (a narrowing of `org`/`personal`); a workspace admin may pick any workspace set on any tile (D65). Never the default. |
| `internet` *(default elsewhere)* | its own netns + an egress relay permitting the **public internet only**; host interfaces and LAN stay hidden. xbind stays reachable via a host-forward on the relay gateway `10.0.2.2` (`XBIN_URL` is transparently rewritten so `bx`/`curl` reach it without any host interface exposed). |
| `host` | shares the host network (LAN + host services). Owner escape hatch — **admin-only** unless the tile's org has a `host` network-set rule; refused requests fall back to `org` (or `none`). |
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

On a **partitioned tile** each person's layer is their own
(`.xbin/term-part/<tile-key>/<partition-id>/`, their VM disk inside it): one
person's `apt install` never shows in another's terminal. Reset and the base
status act on the caller's own layer there; the tile's own layer is what a
session without a person uses. People's layers aren't backed up, and a
partition mode switch deletes them.

**Reset** (`DELETE /ws/term/env?cwd=<tile>`, the terminal's ⟲ button) kills any
live session on the layer and wipes the upper back to a clean base — safe,
because your code and `$HOME` are bind mounts, not part of the overlay. Reset
needs `terminal` level on the tile; resetting the (disabled) root layer is
admin-only. It is also what clears a symlink the layer holds where a mount
point goes (`/opt`, `/proc`, …): the terminal refuses to start over one,
naming the path, instead of following it (D78; the base rootfs's own links,
and the workspace root's — an operator's `homes/` → `.homes` — are
followed, inside the sandbox, and one at a file mount point, like an
apt-installed `nvidia-smi` under a GPU terminal, is covered by the mount —
[isolation.md](/docs/isolation.md) §The dev layer).

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
apt. So each layer is **stamped and pinned** to its base (`internal/layers`,
shared by terminal layers and tile sandboxes; `internal/term/base.go` wraps it
for terminals):

- **Stamps.** A layer dir records its base version in `base` (and, for a tile
  sandbox's namespace upper, the overlay flavour that wrote it in `overlay`).
  xbind writes them, atomically; nothing reads what the sandbox wrote. A
  terminal layer is stamped on first use (a brand-new layer → the current
  base; a pre-existing unstamped upper → the legacy `v0`).
- **`ResolveBase`** pins the layer's upper to the exact base it was built on —
  the current rootfs if it matches, else a preserved sibling `<rootfs>-<version>`.
- **`CheckBaseImages`** is a startup safety gate: xbind **refuses to start** if
  any existing terminal layer is pinned to a base that isn't installed, rather
  than corrupt its apt state. A base upgrade must therefore *preserve* old bases
  as `<rootfs>-<version>` siblings (`deploy/install.sh` does this on upgrade);
  the fix if you hit the gate is to restore the base or reset the affected
  terminal(s). (A tile sandbox whose base is gone fails its own start instead.)
- **GC at boot** releases preserved bases that nothing pins anymore — the
  cleanup side, so old bases don't accumulate once every layer has upgraded.
  The pins are the union of the terminal layers' stamps (`.xbin/term/*`), the
  tile sandboxes' stamps (`.xbin/sbx/<CK>/<name>.<uid>/cur/`) and their
  snapshots' (`…/snapshots/<sid>/`; what `.xbin/sbx/<CK>/.trash` holds pins
  nothing), and the base of every tile-sandbox definition. The VM images
  built from bases (`.xbin/vm/images`) follow the same pins. If any pin
  can't be read (a stamp, or the definitions file), nothing is released that
  boot.

A terminal whose layer's base is older than the current rootfs reports
`baseOutdated` on attach, so the UI can offer a reset-to-upgrade.

## VM terminals (D89)

The title bar's **⧉ VM** toggle (`?vm=1`) restarts a session inside a
Firecracker microVM: the shell is root in its own kernel, so docker, kernel
knobs and ordinary `apt` work. The mount picture above holds unchanged —
the same paths, served from outside the VM (FUSE over vsock), with the same masks,
read-only binds and per-user visibility — and so does the network scope
(the guest sits behind the same relay; `host` isn't offered). The tile's
**VM disk** (`.xbin/term/<key>/vm/disk.img`, sparse) keeps root filesystem
changes across VM sessions the way the dev layer does for namespace
sessions: the same lock (one holder at a time), the same base pin, wiped by
the same Reset, but a separate filesystem, and not in backups. Closing a VM
terminal syncs its disk before the VM is stopped. The toggle is disabled,
with the reason in its tooltip, when the host can't run VMs or the admin
hasn't enabled VM terminals (`GET /ws/term/env` → `vm`); on a host without
KVM the VM is emulated (D90) and the tooltip says it runs several times
slower. Agent
sessions take the same `vm` flag. [isolation.md](/docs/isolation.md) §VM
sandboxes has the rest.

The **start-a-session launcher** carries the same choice *before* anything
starts: where VMs can run, its **⧉ VM sandbox** switch decides whether the
tile's new sessions — shells and agents alike, from the launcher cards, the
`+` menu, ↺ last, or a Resume — open in a VM. The choice is per user and per
tile (the `termvm:<tile>` pref, so every browser sees it), and a ⧉ VM toggle
in the title bar that went through updates it too; a tile nobody chose for
starts outside a VM.

## Resource limits and GPUs

Where xbind's cgroup is delegated, each **restricted** session joins its own
cgroup leaf with the same memory/pids/CPU caps as a tile backend (D17d) — a
runaway build or fork bomb degrades that one shell, not the workspace. Admin
terminals stay unlimited (dev builds are hungry). Session caps: **32 per user,
64 global**, so no one person exhausts the pool. GPUs are opt-in per session
(`?gpu=all|<index>`, owner plane) — the device nodes and driver libs are bound
in like a `gpu:*` backend grant.

## Agent sessions (D74)

An **agent session** is a terminal session whose sandbox runs a coding
agent instead of a shell. Same mounts, same per-user `$HOME`, same tile
token and network scope — but the entry is xbind's own `bx __agent-host`,
which spawns the provider's CLI (Claude Code through `claude-agent-acp`,
Codex through `codex-acp`, `gemini --acp`, `opencode acp`) and speaks the
Agent Client Protocol (ACP, JSON-RPC over the process's stdio) between it
and xbind. There is no PTY: xbind sends prompts, receives typed updates
(message and thought deltas, plans, tool calls, permission requests) and
keeps them in an **append-only event log** per session that any client
replays by cursor and follows live (`GET /api/xbin/term/sessions/<id>/events`,
`session` events on `/ws/events`). Two clients on one session — the Agent
tab in two browsers, or `bx agent attach` in a shell — see one stream; a
**permission request** is answered by whichever answers first, and *allow
for the session* records a rule on the session (later requests of the same
kind and title auto-resolve; nothing lands in `xbin.json`) — except a **plan
approval** (Claude's "Ready to code?", Codex's "Implement this plan?"): its
choices are modes ("clear context and use auto mode", "bypass permissions"),
not "remember this", so it is never turned into a rule and every plan is
asked for. The session
outlives every client; its **transcript outlives the session**: once it took a
prompt and ended — or the daemon stopped — the log is kept on disk (per user,
per tile, the newest 20; `GET /api/xbin/agent/history`), so a finished
conversation can be read back, and **resumed** where the agent can reopen its
own session (it replays the earlier turns, then continues — all four bundled
agents advertise it, and Claude Code and OpenCode are verified end to end; an
agent that cannot offers a fresh start instead). On a partitioned tile the
transcript is kept with the person's partition instead (the same listing
shows it), and goes when a partition mode switch deletes the tile's data.

A long conversation stays quick (D124, D130): the Agent tab reads only the
tail of the log when it opens, folds what arrives, renders at most once a
frame (not at all while the tab is hidden), and keeps only the part of the
transcript near what you are reading — scrolling up loads earlier entries a
page at a time (or all of them, from the "earlier entries" row at the top,
e.g. before a browser find), and what is far above or below is let go and
fetched again when you come back to it, without moving what you are
reading. A folded tool card, file diff or thought renders its body when you
open it; a message being written re-renders only its last paragraph, so a
selection in it survives. Following the bottom, the view keeps up with the
agent; scrolled up, new turns land below without pulling you down, and a
**↓ N new — jump to latest** pill takes you back to the bottom.

What the agent gets:

- **its home.** `HOME` is the same `homes/<you>` a shell gets, so
  `~/.claude/settings.json`, `~/.codex/config.toml`, `~/.gemini/`,
  `~/.config/opencode/` and the logins kept there carry over — a `claude
  auth login` done in a shell terminal serves the agent on every tile. The
  mode you pass at creation is applied after the CLI has loaded its
  settings, so a `permissions.defaultMode` in your settings is the default
  when you pass none.
- **credentials from the home — the same place a shell terminal gets
  them.** There are no provider keys in the tile vault. The agent
  authenticates exactly as the CLI would in a shell: from its own `$HOME`
  (`~/.claude/.credentials.json`, `~/.codex/auth.json`, `~/.gemini/`,
  `~/.local/share/opencode/`), which is the per-user home shared with every
  terminal. So `claude /login` (or `codex login`, `opencode auth login`, …)
  run once in a shell terminal signs the agent in on every tile. When the
  agent reports it is signed out (or a turn hits auth-required), the Agent
  tab shows a **"Sign in to <Provider>"** button that opens a shell terminal
  in the same window running that command for you — the sign-in URL it prints
  is clickable, so there is no wrapped URL to copy out of the transcript. The
  first turn on a home with no login also ends with a `status error` naming
  the command — never a vault command.
- **its own settings, live.** The agent advertises what it can change —
  the model, the reasoning effort, the permission mode — and the Agent tab
  shows each as a picker once the session is up — picking a provider from the
  window's `+` menu starts the session straight away, so the pickers are there
  before you type the first prompt; `bx agent run --model …` /
  `bx agent set` do the same from a shell. A change applies to the next
  turn. Nothing is hardcoded per provider: the list is the agent's.
- **the same window bar as a shell.** An Agent tab has the Bash tab's title
  bar: the layout switcher (the code browser, code beside the agent, backend
  logs, change proposals), the network scope, tile-API and GPU pickers, and
  the tile layer's base update / reset. Those three settings are fixed when a
  sandbox starts, so changing one **restarts the agent** — and resumes its
  conversation where the agent can reopen its own session (Claude Code,
  OpenCode): it replays the turns in the new sandbox, then continues. An
  agent that cannot starts fresh, and the old conversation stays under
  Recent sessions (`POST /api/xbin/term/sessions/<id>/restart`).
- **conservative modes by default.** Claude Code starts in `default` (ask
  before acting), Codex in `read-only`, Gemini in `default`; the bypass
  modes (`bypassPermissions`, `agent-full-access`, `yolo`) exist but must
  be asked for by name — never a default, never chosen for you.
- **your files, in its sandbox.** A prompt can carry attachments — a
  screenshot, a photo, a log, a PDF (`POST
  /api/xbin/term/sessions/<id>/prompt {text, attachments}`, up to 10 files,
  10 MiB each, 20 MiB together). Each is written inside the agent's sandbox
  (a private directory under its own `/tmp`, never the tile; with isolation
  off, a directory xbind makes for the session and removes when it ends or
  xbind stops)
  and handed to the agent by path, so it can read, grep or copy it with its
  own tools; an image (PNG, JPEG, GIF, WebP) up to 3.75 MiB also goes to the
  model inline (4 MiB of images per prompt — a bigger one is a file the agent
  opens itself; downscale photos), and a small text file inline with the
  prompt. The transcript shows the file names.
- **files and terminals inside the sandbox.** The agent's file reads and
  writes and the terminals it opens are served by the host *inside* the
  sandbox, so the kernel's mount view — the allow-list, the masks, the
  read-only tiles — is the authority; writes are confined to the tile.
  `bx` inside such a terminal carries the session's tile token, so a
  cross-tile call is refused until a grant says otherwise, like a shell's.
- **the shell's restrictions.** A non-admin's agent session is a
  restricted session (D18/D17); one started from a shell's own token
  (`bx agent run` in a tile terminal) is restricted even for an admin —
  the terminal token is the tile's element principal, not the human.

**How the Agent tab shows what the agent does.** Every tool call is a card
titled by what the harness says it does — Claude's own description of a
shell command ("Run the unit tests"), else a reading of the command itself
(`python3 - <<'EOF' … open('main.go')` reads *Python script (12 lines) →
main.go*; `sed -i`, `cat >`, `tee`, `>` name the file they write). The
command sits collapsed beneath it (first lines, *show all*, copy) with its
output streamed in as it runs (Codex live, Claude when it finishes) and a red
`exit N` chip on a failure. Text results render as markdown, file edits as
diffs; a tool's raw JSON input is one click away, never the headline. **What
changed on disk** is shown from snapshots, not from what the agent says: when
a shell call finishes, its card lists the files it changed with the real
patch (a `sed -i` or a python script editing `main.go` shows as that diff),
and each turn ends with *This turn changed N files*. The patch in an event
is capped (64 KiB per call, 192 KiB per turn); a client wanting all of it —
a full-screen diff viewer — asks `GET /api/xbin/term/sessions/<id>/diff`
with the call or turn (and optionally one file) while the session lives.
The snapshots live in a
private git directory next to the tile (on tiles that are git repos) — your
repo, index and history are never touched. A **subagent** (Claude's Task)
is one card with everything it did nested beneath — its thinking, its tool
calls, its text — open while it works and folded to its answer when done.
When the agent **asks you something** (Claude Code's AskUserQuestion — which
it only uses because the tab can answer it), the question is a form: a choice
per question with each option's explanation, several ticks where it allows
several, an *Other* box for your own answer; *Submit* sends it, *Skip* tells
the agent you passed. Typing `/` at the start of the message box offers the agent's own **slash
commands** (Claude Code's `/review`, `/compact`, …): arrows pick, Tab or
Enter completes, and the command's input hint shows until you type it. The
agent's reasoning streams into an open *Thinking…* block (Claude Code's is
requested summarized — recent models send none otherwise) that folds to
*Thought for Ns* once the agent moves on, and a line under the transcript
says what a running turn is doing. A plan approval is a **plan card**: the plan as markdown, the agent's choices in its
own words (the first one highlighted), and beside *keep planning* a box whose
text goes in as your next message once the agent stops.

When the session ends — the agent crashed, or it could not sign in — the
tab stays, greyed, with the transcript and the reason, until you dismiss
it; the agent's own title for the session names the tab if you have not.
Every session that took a prompt is then under **Recent sessions** (the
empty window and the `+` menu): open one to read the transcript, or
**Resume** to continue it — the agent reopens its own session and replays the
turns first. An agent that cannot reopen sessions shows the transcript
read-only and offers a fresh start on the tile instead.

Start one from the terminal window's **`+`** menu, or from a shell:
`bx agent run --provider opencode "list the files here"` (docs/bx.md).

## The logs tab

The terminal window also carries a read-only **logs** tab (the `▤` button):
it streams the tile backend's captured stdout/stderr live in an xterm view (no
input), backed by `GET /api/xbin/logs`. It is gated exactly like the tile's
terminal — admin, the tile itself, or a `terminal`-level user — so it appears
only where a shell would, since backend output can carry secrets. It shows
the primary's log; a non-primary tile deployment's own log is the
Deployments panel's logs tab (the `⇈` layout), `bx logs <tile>+<name>`, or
`GET /api/xbin/logs?deployment=<name>`, whose answer names the deployment.
