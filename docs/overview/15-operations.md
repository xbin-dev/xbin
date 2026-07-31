# Deployment & operations

How xbin is put on a host and kept healthy: the reference install and its
systemd unit, what the kernel must provide, what happens at boot, and the
observability surface an operator actually uses — logs, status APIs, alerts,
the audit stream, the admin tile, and `bx doctor`. The recurring theme:
**xbind is itself a sandbox runtime**, so it is deployed *unprivileged but
unconfined*, and the security boundary is the host it runs on.

**Related:** [08-sandbox.md](08-sandbox.md) (what the sandboxes do),
[09-terminals.md](09-terminals.md) (base images & dev layers),
[13-ingress.md](13-ingress.md) (the public listeners),
[14-lifecycle.md](14-lifecycle.md) (backup/restore/offload),
[/docs/getting-started.md](/docs/getting-started.md) · plans/deployment.md,
plans/isolation.md.

## The reference deployment

One Linux host (VM or bare metal) that xbin controls, one systemd service,
one workspace. `deploy/install.sh` (curl-pipe from the repo, idempotent —
re-running upgrades in place) produces this layout under `/opt/xbin`:

| Path | What | Why it lives here |
|---|---|---|
| `bin/` | `xbind`, `bx`, static `fuse-overlayfs`, static `gocryptfs` | single-artifact install; xbind auto-finds the helpers next to itself |
| `rootfs/` | unpacked base OCI image (toolchains: go, node, python, git, vim…) | the read-only lower layer of every backend/terminal sandbox |
| `rootfs-<ver>/` | preserved previous base images | terminals pin the base their overlay was built on; GC'd once unused ([09-terminals.md](09-terminals.md)) |
| `sdk/` | the Go SDK source | the generated `go.work` resolves `github.com/xbin-dev/xbin/sdk` here; terminals get it as a read-only bind so `go build` works offline |
| `workspace/` | the workspace tree | auto-initialized on first boot |
| `/etc/xbin/xbin.env` | optional env file (mode 0600) | `XBIN_VAULT_PASSPHRASE` for hands-off unseal; ingress listener opt-ins |

The installer also: preflight-checks the kernel (below), installs `uidmap` +
`fuse3` + build deps, creates the **`xbin` system user** (home `/opt/xbin`)
with a **delegated `/etc/subuid`+`/etc/subgid` range** (default
`100000:65536`), persists `fuse`/`tun` module autoload and the sysctls,
functionally tests `unshare -Ur` as that user, writes the unit, and prints
the one-time login URL. Everything is overridable via `XBIN_*` env
(`XBIN_PREFIX`, `XBIN_LISTEN`, `XBIN_PREBUILT_BIN`/`XBIN_ROOTFS_DIR` to skip
building, …); `--check-only` runs just the preflight.

For hacking on xbin itself, `make dev` is the same shape from source:
isolated against `./devws`, web/docs served from disk, debug logs, a seeded
`admin`/`admin` login (dev only). `make dev-noauth` / `make dev-plaintext`
trade auth/encryption for friction-free inspection.

## The systemd unit — and the rule

The unit runs xbind as the unprivileged `xbin` user with
`--isolate --rootfs /opt/xbin/rootfs`. Its header carries the one rule that
operators must not "improve" away, quoted from `deploy/xbin.service`:

> IMPORTANT — do NOT add systemd's namespace/filesystem hardening here.
> xbind is a *rootless sandbox runtime*: it builds user/mount/pid/net
> namespaces for every component itself and calls the setuid
> newuidmap/newgidmap helpers to map a uid range. Directives like
> `PrivateUsers=`, `RestrictNamespaces=`, `ProtectSystem=strict`,
> `ReadOnlyPaths=`, `ProtectHome=`, or `NoNewPrivileges=yes` will BREAK the
> sandbox (backends fail to start, apt/user-switching in terminals stops
> working). The real boundary is the VM/host this runs on.

The directives that ARE there are load-bearing:

| Directive | Why |
|---|---|
| `RuntimeDirectory=xbin` (mode 0700) | a **tmpfs** at `/run/xbin` for xbind's IPC sockets (the gateway socket + each backend's listen socket). The run dir is bind-mounted **read-write into every sandbox**, and the isolation model forbids RW host-disk mounts in sandboxes — tmpfs only (plans/isolation.md). xbind picks it up via `$RUNTIME_DIRECTORY`; without one it falls back to `$XDG_RUNTIME_DIR`/`$TMPDIR` and warns if no tmpfs is found. |
| `Delegate=yes` | xbind manages its own cgroup-v2 subtree: one leaf per backend with memory (`XBIN_LIMIT_MEM`, default 2 GiB), pids (≥512, scaled by CPUs), and CPU-weight caps — a runaway tile OOMs *alone*. |
| `LimitNOFILE=1048576`, `TasksMax=infinity`, `OOMPolicy=continue` | a busy workspace holds many watches, sockets and child processes; one component's OOM must not take the daemon down. |
| `Restart=on-failure`, `RestartSec=15` | restart on a crash, but wait 15 s first: a workspace's delegated cgroup subtree (sandboxes, rootless podman) has to drain before the new instance can attach, or the immediate restart fails `219/CGROUP` ("cgroup busy") and flaps. |
| `Environment=XBIN_ROOTFS/XBIN_FUSE_OVERLAYFS/XBIN_GOCRYPTFS/XBIN_SDK_PATH` | pin the sandbox base image, the bundled overlay/crypto helpers, and the SDK location. |
| `EnvironmentFile=-/etc/xbin/xbin.env` | optional secrets/opt-ins (vault passphrase, ingress listener). |

**OS updates don't restart xbind.** On Debian/Ubuntu, `needrestart` (run by
`unattended-upgrades`) restarts every service whose libraries a security update
replaced — and restarting xbind mid-run **seals the vault** (and can leave a
busy sandbox cgroup that flaps the restart). The installer writes
`/etc/needrestart/conf.d/xbin.conf`
(`$nrconf{override_rc}{qr(^xbin\.service$)} = 0`) so security updates still
*install* but never bounce the daemon. Restart it on your own schedule after
patch days with `sudo systemctl restart xbin` (then unseal, if you run sealed).

**Sub-uid delegation matters.** With `newuidmap`/`newgidmap` (the `uidmap`
package) and a subid range, every sandbox maps a full uid space: `apt`,
`sudo`, `useradd`, and packages whose post-install scripts create system
users (systemd, dbus) all work. Without it xbind falls back to
**single-uid mode** — only container-root is mapped — and those `chown`s
fail with `Invalid argument`, breaking heavier package installs midway.
xbind warns loudly at startup; `bx doctor` detects it from inside any
terminal.

## Host prerequisites

| Requirement | Used for | If missing |
|---|---|---|
| unprivileged user namespaces | every sandbox | **fatal** — nothing isolates. Ubuntu 23.10+: the installer lifts `kernel.apparmor_restrict_unprivileged_userns` |
| `/etc/subuid`+`/etc/subgid` range + `uidmap` | full-range uid mapping | single-uid fallback (apt/dpkg system-user installs break) |
| `/dev/fuse` | fuse-overlayfs sandbox roots | kernel-overlay fallback; `apt install` in terminals fails on cross-dir renames |
| `/dev/net/tun` | the egress relay / terminal internet scope | no component egress, no terminal internet |
| cgroup v2 | per-component limits/accounting | non-fatal; limits unavailable |
| `fs.inotify.max_user_watches=524288` | watching every workspace dir | rescans silently miss changes; the #1 support issue — `bx doctor` checks it |

## Network posture

`--listen` defaults to `127.0.0.1:8642` — **loopback, deliberately**. xbin
is remote code execution by design; the console is reached through
Tailscale/WireGuard or a TLS reverse proxy, never raw. Behind a proxy the
session cookie flips to `Secure` when `X-Forwarded-Proto: https` arrives.

Public traffic is a **separate, opt-in door** ([13-ingress.md](13-ingress.md)):
set `XBIN_INGRESS_LISTEN` (+ `XBIN_INGRESS_CERT`/`KEY` for bring-your-own
TLS, reloaded on renewal) and it serves *only* published tile routes — never
the authenticated console. Host ports below 1024 (the Traefik tile's
`:80/:443`, low stream ports) need one uncomment in the unit:
`AmbientCapabilities=CAP_NET_BIND_SERVICE` — it grants low-port binding and
nothing else.

## What boots, in order

`serve()` in `cmd/xbind/main.go`, top to bottom:

1. **Auto-init**: no `xbin.json` in the workspace → scaffold it from the
   embedded template (shell, root page, admin/manager tiles, git init,
   scaffold provenance for later builtin updates). An empty bind mount
   becomes a working workspace on first boot.
2. **Backfill** `AGENTS.md` (agents depend on it) + the `CLAUDE.md` symlink;
   nothing else is ever overwritten.
3. **Drop privileges** (D13(b)): started as root on a workspace owned by
   someone else (and not `--scope-uids`), xbind setuids to the workspace
   owner so bind-mounted workspaces keep sane file ownership.
4. **Auth + users**: load/create the owner token and frame-token secret
   under `.xbin/`; open `data/users.json` (zero users = single-user mode).
   `--dev` with auth on and no users seeds `admin`/`admin` — dev only.
5. **Home migration**: a legacy shared `home/` becomes per-user
   `homes/<user>` ([09-terminals.md](09-terminals.md)); refuses to guess if
   both forms hold real data.
6. **Registry scan**, events hub, runner; **deps reconcile + `go.work`**
   generation (the SDK path from `XBIN_SDK_PATH`).
7. **Terminal manager**: session env, per-user home seeding, tile-scoped
   terminal tokens, mount-masking of tiles below the user's read level.
8. **Broker**: kv store, vault barrier, per-resource encryption mounts,
   cron, disk monitor; builtin tile/template catalogs; per-component git
   repos ensured.
9. **Vault comes up in one of five modes** (docs/auth.md §vault):

   | Condition | Mode |
   |---|---|
   | `XBIN_VAULT_PASSPHRASE` set | auto-init/unseal at boot |
   | `--insecure-vault` or `--no-auth` | plaintext at rest (opt-out) |
   | `--dev` | auto-unseal with a fixed in-source dev key (INSECURE) |
   | barrier exists, no env | starts **sealed** — an admin unseals after login |
   | no barrier, no env | starts **locked** — refused until an admin sets it up |

10. **Isolation wiring** (`--isolate` + `--rootfs`): egress/GPU/net-provider/
    ingress hooks into the runner; terminal sandboxes get the same base. Two
    hard gates here: unprivileged userns must work, and **`CheckBaseImages`
    aborts startup** if a terminal's persistent layer pins a base-image
    version that no longer exists — a base upgrade must preserve old bases
    (the installer does), or dev layers would corrupt.
11. **HTTP wiring**: the server + broker APIs; the **gateway unix socket**
    (components' API door) serving the same handler; ingress stream
    listeners/forward sockets reconciled; the optional ingress HTTP listener;
    finally the console TCP listener and the login URL print.
12. **The watcher** rescans on debounced (300 ms) file changes: re-provision
    resources, reconcile deps/go.work/ingress, live-reload frames, rebuild
    changed backends.

Shutdown (SIGTERM): close the HTTP server, stop every backend, exit.

## Flags & environment

| Flag (env) | Default | Meaning |
|---|---|---|
| `--workspace` (`XBIN_WORKSPACE`) | `/workspace` | workspace directory |
| `--listen` (`XBIN_LISTEN`) | `127.0.0.1:8642` | console listener |
| `--dev` | off | web/docs from source tree, debug logs, dev vault key, dev admin seed |
| `--no-auth` | off | every request is admin (dev only; element identity still enforced) |
| `--isolate` + `--rootfs` (`XBIN_ROOTFS`) | off | per-component sandboxes (tier 3) — production always sets this |
| `--scope-uids` | off | tier-2 per-scope uids (needs root) |
| `--insecure-vault` | off | plaintext secrets/data at rest |
| `--ingress-listen/cert/key` (`XBIN_INGRESS_*`) | off | the public HTTP door |

Other env: `XBIN_VAULT_PASSPHRASE`, `XBIN_SDK_PATH`, `XBIN_BIN` (where `bx`
lives), `XBIN_FUSE_OVERLAYFS`, `XBIN_GOCRYPTFS`, `XBIN_LIMIT_MEM` (per-tile
memory cap), `XBIN_SESSION_IDLE_TTL`/`XBIN_SESSION_MAX_TTL` (login session
windows; default 12h idle / 30d absolute), `XBIN_SANDBOX_DEBUG=1` (init step
trace when a sandbox dies before its entrypoint).

## Observability

**Backend logs.** Every generation's stdout/stderr appends to
`.xbin/log/<compkey>.log` with `--- gen N start …` markers. Three readers:
`bx logs [-f]`, `GET /api/xbin/logs?component=…[&tail=…][&follow=1]` (plain
text; follow streams), and the terminal window's read-only **logs tab**.
All gate identically: admins, the tile itself, or a human with
**terminal-level** access on that tile — read/write users don't get logs,
because backend output can carry secrets (the gate matches "could root-shell
in anyway").

**Status APIs** (admin unless noted):

| Endpoint | Answers |
|---|---|
| `GET /api/xbin/status` | component count, live terminals, xbind version, host CPU/mem/workspace-disk, request+traffic counters (the shell's footer) |
| `GET /api/xbin/backends` | per-component backend state: building/healthy/failed, generation, last error |
| `GET /api/xbin/runtime` | the deep view: daemon process stats, kernel, **which terminal protections the kernel supports** (seccomp/Landlock+ABI), and per-backend process/namespace/cgroup/egress-flow detail |
| `GET /api/xbin/tile-status` | one tile's runtime metrics — readable by the tile itself/its terminal (`bx status`) |
| `GET /api/xbin/ingress` | published endpoints, live routes, stream-listener health ([13-ingress.md](13-ingress.md)) |
| `WS /ws/events` | live stream: reload / build-start / build-error / build-ok / grants / bus |

**Alerts** (`GET /api/xbin/alerts`, bannered in the shell and admin tile):
a scope over its disk quota, workspace low-disk write-blocking, and
cgroup-detected repeat OOM-kills / pids-limit hits per tile — the "your
workspace is degrading" signals, surfaced before things break.

**Audit.** Every state-changing call to the governance API (grants, users,
bindings, lifecycle, vault, create — the high-frequency data plane like
kv/prefs is excluded as noise) logs one structured line: who (principal),
method, path, resulting status. It rides the daemon journal
(`journalctl -u xbin`).

## The admin tile: two-level nav, one line per sub-tab

The console groups its tabs (it runs to thousands of tiles, so every list view
also carries a text filter and, where it helps, scope/org category chips):

- **runtime** — *components* (the tile roster: manifest + live backend state,
  expandable to each tile's access relations and runtime detail; filterable) ·
  *resources* (host health + brokered-resource footprint) · *backup*
  (archiver bindings, schedules, restore) · *cron* (scheduled jobs).
- **user management** — *users* (accounts, roles, per-tile levels) ·
  *organisations* (D19–21) · *teams* (cross-org roster) · *access map*
  (who-can-what matrix with provenance).
- **vault** — seal/unseal, per-tile secrets.
- **binding** — *roles* (the exposed-role catalog) · *grants* (pending
  approvals + grant table) · *interface providers* · *binding* (wire each
  requested slot to a provider).
- **ingress** — *endpoints* (the live public routing table + listeners) ·
  *services / expose* (publish/unpublish exposed endpoints).

Old hash deep-links (`#overview`, `#runtime`, `#interfaces`) redirect to their
new homes.

## `bx doctor`

The first command when something "doesn't reload": xbind reachability,
manifest parse errors, dangling `deps`, roles without `API.md`/descriptions,
org-path sanity (unknown org markers, inert team patterns), go.work
ownership, **single-uid sandbox detection**, the inotify budget, and missing
toolchains for runtimes in use.

## Upgrading

Re-run the installer: it detects an existing install and switches to
**upgrade mode** — rebuild, stop, swap binaries/rootfs/sdk, re-render the
unit (a locally-edited unit is preserved as `.bak`), restart. User, subids,
vault choice, and the workspace are untouched. When the base-image version
changes, the old `rootfs` is preserved as `rootfs-<ver>` so pinned terminal
layers keep working until they upgrade (then GC'd by xbind).

Two operator contracts around an upgrade:

- **A binary swap restarts every backend** — backends are xbind's children
  and there is no socket handoff (a known current limitation). Plan upgrades
  like brief maintenance.
- **Read the changelog after every upgrade** (`/docs/changelog.md`): every
  builder-visible change lands an entry; **BREAKING** entries link a
  migration note under `/docs/changes/` with exactly what to change. For
  copied builtin content (the scaffold, imported tiles), `bx builtin
  updates` lists what the new xbind ships newer, and `bx builtin update
  <id> --replace|--merge` applies it without trampling local edits.

Backups are workspace-level insurance, not upgrade insurance — the
archiver-tile model, schedules, and restore live in
[14-lifecycle.md](14-lifecycle.md).
