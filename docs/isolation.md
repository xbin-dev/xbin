# Isolation: sandboxes, terminals, and the dev layer

xbin runs on two planes, and **both** are sandboxed with a default-deny
posture — you get your own code, your granted state, and nothing else until
something is explicitly wired:

- **The runtime plane** — a component's **backend**, a least-privileged tenant.
- **The owner/editing plane** — **terminals**, where you (or an agent) edit code.

The mechanism is Linux namespaces over an overlay rootfs (design records
`isolation` / `runtime` in the xbin repo); this page is the builder-facing
summary of what that means for you.

## The backend sandbox (runtime plane)

A running backend sees a minimal, purpose-built filesystem:

| Mount | Access | What it is |
|-------|--------|------------|
| base rootfs | read-only | Go / node / python toolchains + core tools |
| env layer | read-only | your `setup` deps, prebuilt into an overlay lower |
| your component dir | **read-only** | your source (editing is the terminal's job) |
| granted resource dirs | **read-write** | `filesystem`/`sqlite` resources you were granted |

It does **not** see other components' source, other elements' vaults, the
workspace `home/`, `data/`, `.xbin/`, or the host — they simply aren't
mounted. Its own source is read-only at runtime, so **persist state only in a
resource dir**; anything you write elsewhere lands in a throwaway overlay and is
gone on the next restart ([resources.md](/docs/resources.md)).

Two things always work regardless of the network:

- **The gateway.** Reaching xbind and calling other components (subject to your
  grants) goes over the gateway unix socket in env as `XBIN_GATEWAY` — that is
  not IP egress and is never blocked.
- **Nothing else, by default.** A backend has **no IP egress** until the owner
  binds its `net` interface (see **Network egress** below).

## Confined tool runs (xbind's own work on your tile)

Some work on your tile is done by xbind itself rather than by your code:
the Code panel's history and diffs, giving a new tile its git repo, forking,
importing from a git URL, template instances, builtin updates, the Agent
tab's "changed files", and **compiling a Go backend**. Every one of those
tools reads your tile's content as configuration — a repo's `.git/config`
can name commands for git to run (`core.fsmonitor`, filters,
`diff.external`), `go build` runs git, `go.mod` steers downloads — and your
tile is written from inside sandboxes (terminals, coding agents). So none of
it runs with xbind's privileges: each tool runs in a **throwaway sandbox**
that sees only the paths it needs, has no capabilities, and no network
unless the job is a fetch. Whatever your repo makes git do stays inside that
box, with your own tile.

What that means when you build a **Go backend** (`--isolate` workspaces):

| | |
|---|---|
| toolchain | the host's Go, the same version as before, read-only |
| sees | the workspace read-only (so `go.work` and every module it `use`s resolve as always) with `.xbin/`, `data/` and `homes/` masked; the xbin SDK |
| writes | only your tile's own: its build output and its **own** build and module caches under `.xbin/cache/tile/` (a shared cache would let one tile's build plant code in another's). The first build after this change compiles the standard library once per tile |
| modules | whatever the host's module cache already holds is served from it read-only, offline; new modules are downloaded — **public addresses only** (the operator sets `XBIN_BUILD_NET=host` for a GOPROXY or private modules on the LAN) |
| not honoured | a `replace` to a path outside the workspace and the SDK (it isn't there); VCS stamping (`-buildvcs=false` — nothing your repo's config says runs, even inside the box) |

An **import from a git URL** runs its `git ls-remote`/`git clone` on the
host's network (anything the host reaches, as before), with the daemon's
`~/.ssh` visible read-only for `git@…` URLs — but in a sandbox that sees only
the new tile's directory. The daemon's own global git config (credential
helpers included) no longer applies to it.

Workspaces without isolation have no sandbox to use: their backends and
terminals already run as the xbind user, and these tools run directly, with
system/global git config and repo hooks switched off.

## Terminal isolation (owner/editing plane)

Terminals are the editing plane — a real shell, scoped to its tile, in a
component's directory. (The **root terminal** — a shell on the workspace root —
is **disabled**; workspace-wide work happens in the browser UI or a host shell.)
A terminal (or agent session) whose sandbox cannot be set up does not open —
the error says why; it never falls back to a shell on the host (D78).

How a component terminal sees the workspace depends on who opened it (D40):

An **admin's component terminal** mounts the workspace **read-only except
`$HOME` and that component's own directory** — read every tile's *source*
(needed to integrate against its API), edit only your own tile and `$HOME`.
The platform's secrets and other users' data are **masked out entirely** (an
empty overlay hides them, even though the root is bound read-only):

- **`.xbin/`** — the owner token and the frame-token secret. Without this mask
  a shell could `cat .xbin/token` and become owner, defeating the tile-scoped
  terminal token (below).
- **`data/`** — the vault, the encrypted resource state, and `users.json`
  (password hashes). Terminals reach resources through the API, never the raw
  at-rest files.
- **`homes/`** — every *other* user's `$HOME` (their agent credentials, shell
  history). Your own `$HOME` remains read-write.

A **non-admin's component terminal** gets an **allow-list view** instead of
masks (D40): the workspace root is a small staged directory containing only
redacted root files, and the ONLY things bound into it are the components the
user can `read` (read-only), their own component (read-write), and their own
`$HOME`. Consequences, each closing a leak the deny-list masking had:

- **Unreadable tiles don't exist** — not their contents, not their *names*
  (`ls apps/` lists only what you may read; the mount table carries no
  per-tile masks to enumerate).
- **`xbin.json` is redacted**: schema + import map, plus only the grants and
  binding rows whose every referenced component you can read. The real file
  is the workspace's entire topology — grant edges, wiring, public
  hostnames — and never enters the sandbox.
- **`go.work` is filtered** to readable modules (so `go build` never chases
  directories that aren't there), and `AGENTS.md`/`.gitignore` are copies.
- `.xbin/`, `data/`, other homes: simply **not mounted** (an empty `.xbin`
  marker exists so `bx` can locate the workspace root), and the resenc
  mount-table names the recursive bind used to carry are gone with it.

Either way it holds for **every** terminal, including one on a tile you own:
a rogue agent in a component terminal can touch **only its own component and
`$HOME`**, and can read code but not secrets.

For ADMIN terminals the masks hide the secrets' *contents*; the mount table
may still hint at their *existence*. The workspace's per-resource encrypted
stores are each a gocryptfs mount under `.xbin/resenc/…` named after the
owning tile. The admin view's read-only workspace bind is **recursive**, so
those submounts are cloned into the terminal; the `.xbin` mask then shadows
them (their contents are unreadable), and on current kernels they drop out of
the terminal's `/proc/self/mountinfo` as well — but that shadow-hiding isn't
guaranteed across kernels, so treat the resource *names* as potentially
visible in an ADMIN terminal's mount table. This is a benign disclosure
there: an admin terminal can already `ls` every tile's source, so a resource
path in `mount` names nothing
`ls` doesn't. Truly removing the names would require moving resource storage
outside the workspace tree — the mount can't be pruned in place, because a
rootless user namespace **locks every inherited mount**: you can neither
unmount a resenc submount from inside (`umount2` → `EINVAL`) nor bind the
workspace *non*-recursively to exclude them (a non-recursive bind of a subtree
with locked children is also `EINVAL`). Both directions are the same lock.

**Live-API toggle.** A terminal's titlebar has a **tile-API / no-API** switch
(alongside the network scope). With it off, the session is minted with **no
token** — the shell can read and edit source but every call to the tile's (or
xbin's) API is unauthorized. Use it to run untrusted code or an agent that
should see code but not act on the live workspace.

A sandbox shell is uid 0 in its own user namespace and keeps `CAP_SYS_ADMIN`
(so `apt`, nested namespaces, and profiling still work), which would let it
`umount` a mask and read what's beneath. Two guards, installed just before the
shell starts and inherited across `execve` and every later `unshare`, close
that:

- **Mount guard (seccomp).** A filter denies exactly the four ways to remove or
  relocate a mount — `umount2`, `move_mount`, `open_tree`, and `mount(MS_MOVE)`
  — so the masks can't be peeled off without dropping the cap. Everything else
  (plain `mount`, bind mounts, `unshare`, `pivot_root`, `clone`) stays allowed.
  *Collateral:* tools that unmount — `fusermount`, container/browser sandboxes
  that detach their old root — get `EPERM`; run those on the host or a backend.
- **Read guard (Landlock).** A second layer at the VFS level: even if a mask
  *were* peeled (a kernel bug, or a reveal path the mount guard misses), the
  secret files still can't be opened. It denies reading file *contents* under
  `.xbin/`, `data/`, and other users' `homes/` — so the owner token, vault,
  password hashes, and other agents' credentials are unreadable regardless of
  the mount. (seccomp can't do this — it can't see an `open`'s path argument;
  Landlock enforces on the resolved path.) Directory listing, execution, and
  writes are untouched, so collateral is nil. The guard also explicitly grants
  *reparenting* (`LANDLOCK_ACCESS_FS_REFER`) on every path it allows reading:
  on an ABI-2+ kernel, enforcing any Landlock ruleset otherwise denies
  cross-directory `rename`/`link` with `EXDEV` — which would break `apt`
  (its `partial/ → parent` rename) and any tool that moves a file between
  directories. Best-effort: a no-op on kernels without Landlock (the masks +
  mount guard still apply).

The admin console's **runtime** tab shows each guard's kernel support
(*terminal guard: mount ✓ · read ✓ (ABI n)*).

**Restricted terminals (non-admin users).** The guards above keep `CAP_SYS_ADMIN`
so an admin's dev shell can still mount and nest containers. A terminal opened by
a **non-admin** user instead gives that power up entirely — it's an untrusted
tier — while still running `apt`:

- **Caps dropped to a file-only set.** `CAP_SYS_ADMIN` (mount/namespaces) and
  `CAP_SYS_RESOURCE` are gone, along with every other privileged cap; only the
  file/ownership caps `dpkg` needs to unpack packages remain, so `apt install`
  works but the shell can't reach past its own files.
- **No new namespaces.** Dropping `CAP_SYS_ADMIN` isn't enough on its own —
  creating an *unprivileged* user namespace needs no capability, so a capless
  shell could `unshare -Ur` into a nested userns and get the full cap set back.
  So init pins the terminal's userns with `/proc/sys/user/max_user_namespaces=0`
  and `max_mnt_namespaces=0` (which block creation inside the kernel, regardless
  of `clone`/`clone3`/`unshare`) **and then drops `CAP_SYS_RESOURCE`**, so the
  shell can't raise those limits back — it can neither nest a userns nor reach
  `mount`. A seccomp filter denying the namespace-creating syscalls backs this up
  on kernels where the knob doesn't take (see decision D18).
- **Source visibility cut to the allow-list.** An admin's terminal sees every
  tile's source read-only; a non-admin's mounts ONLY the tiles at/above their
  `read` level (the D40 allow-list view — unreadable tiles are absent, names
  included, and the root files are redacted copies) — the same rule the tile
  list applies, enforced at the mount level.
- **Code-only and airgapped by default.** Without the user's `termApi` grant
  the session is minted with **no** tile-API token (the `?api=1` toggle is
  clamped); on a workspace tile, or a personal one whose owner has no
  personal network, without `termNet` there is no internet egress (`?net=`
  is clamped to `none`) — the exfiltration path that makes source masking
  matter. On an **org-owned tile** the org's network sets are the grant
  instead (the `org` scope, D54); on a **personal tile** whose owner has
  network sets, their personal network is added (the `personal` scope,
  D88). `host` networking needs the admin plane, or a set that says
  `host`. The session still opens either way — an ungranted user gets a
  working, airgapped, code-only shell, and the banner says why. An account
  with `noTerminal` gets no session at all (D88).
- **Resource limits.** Where xbind's cgroup is delegated (see *Resource
  limits* below), each restricted session lives in its own cgroup leaf with
  the same memory/pids/CPU caps as a tile backend — a runaway build or fork
  bomb degrades that one shell, not the workspace. (Admin terminals stay
  unlimited; dev builds are hungry.)

The trade-off: a restricted terminal can't run rootless podman, nested `bwrap`,
or Chromium's own userns sandbox (use `--no-sandbox` there). The mount + read
guards still apply underneath, so this is defense in depth, not a replacement.

**Honest bound.** With the masks + both guards, the secrets are unreadable from
a tile terminal even against a deliberately adversarial shell: peeling a mask
is blocked (seccomp) *and* reading the files is blocked independently
(Landlock), so a single-layer failure doesn't expose them. This is an isolation
property of `--isolate` (tier 3); a non-isolated (tier-1) host-shell terminal
still sees the host workspace. The *complete* boundary against a hostile
co-tenant is still per-tenant uids (the multi-tenant work); the guards are
defense in depth on top of that. For today's single-owner workspace they make
the tile-scoped terminal token a real boundary against agent misbehavior and
escalation.

Commits still work from a component terminal because **each component is its own
git repo**: `cd` into the component and `git commit` writes to the component's
`.git`, which lives inside the writable component dir. (Cross-component or
workspace-wide git is a host-shell job — the root terminal is disabled.) This is also what makes components
**installable** — importing one from a git URL is just a clone.

## `$HOME` — per user, shared across that user's terminals

`$HOME` is `<workspace>/homes/<user>` — one home per signed-in user (the root
token gets `homes/owner`) — and it is **read-write in every terminal**,
including component terminals, where it's the one writable thing outside the
component itself. Within a user it is shared: your agent-CLI config, auth/login
state, and dotfiles live there once and follow you into every terminal you
open, and they **survive xbind upgrades** (workspace data, not part of any
rootfs). Other users get their own homes — configs don't mix. (Hygiene, not a
security boundary — the filesystem user is the same; the API credential,
though, is per-session and tile-scoped, see docs/overview/09-terminals.md.)

## The dev layer — persistent, per-component, resettable

A terminal is a full dev box, not just a shell. Filesystem changes you make to
the *system* — an `apt install`, a tweak under `/etc`, an extra toolchain —
don't touch the base rootfs (read-only) and aren't lost at exit. They're
captured in a **persistent per-component layer** at `.xbin/term/<component>/`
(the overlay's upper dir), which **survives across sessions and restarts**. Each
component effectively gets its own long-lived dev sandbox; the ⟲ button in the
terminal UI resets it back to a clean base.

Keep two "layers" straight — they are deliberately separate:

- **The terminal dev layer** (`.xbin/term/…`, above) is for *interactive* work:
  whatever you install while poking around in the shell.
- **The component env layer** is built from the manifest's `setup` and is what
  the **running backend** gets (read-only). This is where **backend
  dependencies** belong, so they're declared, reproducible, and present at run
  time — not accidentally living only in some terminal's upper dir.

Rule of thumb: `apt install` in a terminal to try something; move anything the
backend needs into `setup`.

> Only one live session may hold a given component's persistent layer at a time
> (concurrent overlay mounts of one upper dir would corrupt it). A second
> concurrent terminal on the same component falls back to an ephemeral layer —
> functional, but its system changes don't persist.

## Network scopes for terminals

A terminal picks a network scope when it opens (the net selector in the UI —
it lists only the scopes the server will honour for you on that tile):

- **org** *(default on org-owned tiles with network sets, D54)* — its own
  network namespace with an egress relay enforcing the owning org's
  **network sets** (LAN ranges, named internet destinations, all internet —
  whatever a workspace admin attached); when a set says `host` this scope
  *is* host networking. Members need no `termNet` for it.
- **personal** *(default on personal tiles whose owner has network sets,
  D88)* — the same, under the tile owner's **personal network** (their
  network sets ∪ the workspace personal defaults). It is *added* to the
  other scopes, not a replacement: `internet` stays available with
  `termNet`, or when the personal network holds full internet.
- **set:\<name\>** — one named network set, the relay under exactly its
  rules (host networking when it says `host`): each set of the tile
  owner's network (the org's attached sets, or a personal tile owner's
  sets), or — for a workspace admin — any set on any tile (D65). Never
  the default.
- **internet** *(default elsewhere)* — its own network namespace with an egress
  relay that permits the **public internet only**; host interfaces and the LAN
  stay hidden. `XBIN_URL` is transparently routed so `bx`/`curl` still reach
  xbind.
- **host** — shares the host network (LAN + host-local services visible). An
  admin escape hatch — or an org's, when its network sets say `host`.
- **none** — an isolated namespace with **no egress at all** (airgapped —
  xbind itself is unreachable).

Note the contrast: a **terminal** on a personal tile defaults to public-
internet egress (you usually want to `git clone`, `go get`, `npm i`) — or
to the owner's personal network where they have one — on an org tile to
the org network, whereas a **backend** defaults to *no* egress until its
`net` interface is bound — or, on an org-owned tile with network sets, to
that org network (`org`), and on a personal tile whose owner has network
sets, to that personal network (`personal`, D88).

## Network egress

Egress is an **interface**, not an ambient capability. A backend requests a
`net` interface; the **owner binds** it to a provider (binding *is* the
authorization — a component can never self-bind). Providers include:

- the **`internet`** builtin — public internet only, through a userspace gVisor
  relay that terminates and meters every flow (`internet:<host|cidr>[:port]`
  narrows it to named destinations, D35);
- **`host`** / **`lan:<cidr>`** — host or a specific LAN range;
- **`org`** — the owning org's **network sets** (a workspace-admin-managed
  union of the forms above), live; the default for org-owned tiles that
  declare `net`, and the ceiling for whatever else they bind (docs/auth.md
  §Network sets, D54); **`none`** — explicitly no egress;
- a **provider tile** — a VPN, firewall, or router (e.g. the egress-approver or
  s3-archiver's upstream). Provider tiles are themselves clients of *their* own
  egress, so binding one to another **chains** them
  (client → firewall → VPN → internet) purely from the binding graph, no code.

The full interface model (request / provide / bind, plus `http` service
contracts and the `@archive` slot used by backups) lives in
[protocol.md](/docs/protocol.md); the design rationale is in
`docs/overview/11-interfaces.md`.

---

**See also:** [resources.md](/docs/resources.md) (what persists and where),
[auth.md](/docs/auth.md) (grants, principals, the vault),
[protocol.md](/docs/protocol.md) (interfaces, bindings, the full API).

## VM sandboxes — a kernel of your own (D89)

A terminal, an agent session or a backend can run in a **Firecracker
microVM** instead of sharing the host kernel. Inside, the workload is `root`
in its own Linux: docker/podman, kernel knobs and `apt` all work as on any
VM. Everything around it stays the same:

- **The same files.** Every mount a namespace sandbox would get appears at
  the same path in the guest, served from outside the VM. Read-only mounts are
  read-only; masked paths (`.xbin/`, `data/`, other homes) are empty; tiles
  you may not read aren't there. The file server runs outside the VM, under
  the host's enforcement, and a guest can't walk out of what it was given.
- **The same network.** The guest sits behind the same egress relay, under
  the same scope and grants. Root in the guest can reconfigure its own
  interfaces but can't widen what leaves. `$XBIN_URL` works as usual.
- **The same identity and API.** The terminal token or the backend's
  instance token, the gateway, grants, logs.

The namespace sandbox is still there, as the VM's jail (Firecracker runs
inside it with a bare root and five file capabilities). A VM escape lands
in a rootless sandbox that holds only the binds. VMs are **off by default**:
an admin turns them on for terminals and/or backends with `PUT
/api/xbin/vm/policy`, which also sets the size per VM (default 2 GiB, 2
vCPUs), the number of VMs and a memory budget. `GET /api/xbin/vm` says
whether this host can run them and why not.

**Terminals.** The **⧉ VM** toggle in the terminal title bar restarts the
session in a VM (`?vm=1` on `/ws/term`). The prompt is up in about 0.2 s;
the very first VM on a workspace also builds the guest's image from the
base rootfs, which takes seconds. Root filesystem changes (`apt install`,
`/etc`) are kept on the tile's **VM disk**, a sparse image in the tile's
dev layer. It is shared by that tile's VM terminals, is separate from the
namespace terminals' layer (packages installed in one mode aren't in the
other), is wiped by Reset, and is not in backups. Host networking and GPUs
aren't available in a VM.

**Backends.** `"vm": true` (or `{"memory": "1G", "vcpus": 2}`, capped by
the policy) in `xbin.json` runs the backend in a VM
([elements.md](/docs/elements.md)). Its sockets live inside the guest and
are bridged, so the proxy, `XBIN_GATEWAY` and the SDK behave as usual.
Differences to design around:
- A first start takes about 0.4 s more (boot).
- A tile with `sqlite`/`filesystem` resources stops its old generation
  before the new one starts, so there is a short gap on reload.
- `setup` can't be combined with `vm` yet: install at start, you're root.
- Host networking, provider links and GPUs are refused.
- Without KVM, or without the admin's switch, the backend fails with the
  reason — it never falls back to the namespace sandbox.

**Files** reach the guest as FUSE filesystems over vsock, and the guest
caches them hard: repeated work (`git status`, `find`, a rebuild, re-reading
files) runs at nearly local speed. The *first* touch of each file or
directory costs a round trip to the host (~0.1 ms), so a cold `grep -r` over
a huge tree, `npm install` into the tile or unpacking thousands of files is
several times slower than in a namespace terminal. Keep bulk data on the VM's
own disk (anything outside the tile dir and `$HOME`: `/root`, `/tmp`,
`/var`), where it's local. Edits made outside the VM (the browser editor,
another terminal) show up at once, but a file watcher *inside* the guest
doesn't hear about them; use polling there.

**Host requirements:** KVM (`/dev/kvm` usable by the xbind user — the
installer adds it to the `kvm` group; a cloud VM needs nested
virtualization) and the release bundle's `firecracker`, `vmlinux`,
`xbin-vmagent` and `mkfs.erofs`. Decision: D89.

## Resource limits (blast-radius containment)

The workspace is shared, so one clumsy or runaway tile must not be able to take
it down. Under `--isolate` (and wherever xbind's cgroup is delegated) each tile
backend is capped so it degrades *itself*, not the box:

- **Memory** — `memory.max` 2 GiB (a soft `memory.high` 1/8 under it throttles a
  gradual leak before the hard OOM). Over-budget → the tile's own process is
  OOM-killed and crash-loop backoff kicks in; nothing else is touched.
- **Processes** — `pids.max` `max(512, ncpu×8)`: a fork bomb hits its own
  ceiling. This is also the practical cap on **namespace/mount exhaustion** —
  every user/mount/pid namespace needs a process, so `pids.max` bounds how many
  a tile can create. (Linux's own `user.max_*_namespaces` / `fs.mount-max`
  sysctls are the coarse, hierarchical backstop if you want a hard ceiling; we
  don't touch them by default.)
- **CPU** — `cpu.weight` (fair share): under contention every tile gets an equal
  slice, but an idle box lets any tile burst to all cores (no hard `cpu.max`).
- **Disk** — each scope's resource storage is capped at **50 GiB**; over it, its
  API resource writes (kv/blob) get `507`. When the data partition drops below
  **10 % free**, the biggest users are write-blocked too, to hold the reserve.
  Directly-mounted resources (sqlite/filesystem) can't be write-blocked at the
  API — they count toward the quota and raise an alert, but stopping them is the
  admin's call.
- **Terminals** — 32 per user (64 global), so one person can't exhaust the pool.

Limits are tunable via `XBIN_LIMIT_MEM` / `XBIN_LIMIT_DISK`.

**Backend hardening.** A tile backend needs no privilege, so it runs with **all
capabilities dropped** and a **seccomp block-list** of system-damaging syscalls
(mount family, module load, kexec, reboot, ptrace, bpf, keyrings, device nodes,
clock/quota). A wedged or buggy tile can't reach past its own process. Terminals
are different — they keep capabilities (for `apt`, nested namespaces) and rely on
the narrower mount/read guards above.

**Alerts.** At-limit and blocking events (a tile over disk quota, low workspace
disk, a tile hitting its memory/pids cap) surface as `GET /api/xbin/alerts` and
show as a banner in the **workspace shell** (system-wide notices to everyone)
and the **admin console** (all of them). So an operator sees a degrading
workspace immediately, not after it breaks.
