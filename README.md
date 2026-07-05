# xbin

A self-modifying, in-browser workspace. Every piece of UI is a directory;
every directory can have a live backend; the tiny square in the corner of any
component opens a real shell in its source. Save a file — the frontend reloads
and the backend recompiles under you. Notion-shaped, but every block is code
you own, and apps talk to each other through granted, role-scoped APIs and
shared resources.

```
Workspace (one host, one git repo)
└── Scope (an app: calendar, email…)     scope.json — owns resources
    └── Component (an element)           a directory: index.html + xbin.json + backend/
```

xbind is a single Go binary; the frontend is buildless (Lit via import maps,
vendored — no bundler anywhere). Full docs are served by the workspace itself at
`/docs/` (also in [docs/](docs/)): getting started, the component contract,
auth/grants, resources, SDKs, wire protocol, CLI.

## What's inside

- **xbind** (one binary): static component serving with a single sanctioned
  HTML transform (import map + client injection), PTY terminals over WebSocket
  (xterm.js), a file watcher driving live reload, and a backend runner —
  `go build` on save, blue/green socket swap, error overlays, crash-loop
  breaking, idle reaping. CGI for shell scripts; node/python restart-on-change.
- **RBAC between elements**: callees declare roles, callers request them, the
  owner approves once (UI panel or `bx grant`); xbind verifies identity on
  every call and injects `X-XBin-From`/`X-XBin-Role`. Element frontends are
  attributed via frame tokens; backends via per-generation instance credentials
  on a gateway socket.
- **Resources**: kv, blob, bus (live cross-app events into the browser), cron
  (scheduled calls that wake idle backends), sqlite — one grant grammar
  (`res:apps/calendar/bus`).
- **Per-component OS isolation** (`--isolate`, rootless): each backend runs in
  its own user + mount + pid + ipc + uts + net namespaces over an overlay of a
  shared base rootfs; egress is **default-deny** through a transparent relay
  that enforces `net:*` grants (TCP/UDP/ICMP); cgroup v2 accounting. Terminals
  share the base rootfs (Go/Node/Python + agent CLIs, zero setup) and pick a
  per-session **network scope** — internet-only (own netns, no host interfaces),
  host, or offline.
- **Vault**: per-element private secrets (`bx vault`, `xbin.Secret()`),
  encrypted at rest.
- **bx** CLI + **Go SDK** (`github.com/magik6k/xbin/sdk`, zero deps).

## Running it

xbind runs the workspace on a Linux host and **becomes the sandbox runtime**
for its components, building each one's namespaces, overlay rootfs, and egress
relay itself. It's rootless (unprivileged user namespaces), but building
sandboxes from *inside* someone else's unprivileged container is the fragile
part (nested userns, missing devices) — so xbin runs on a **Linux VM or
bare-metal host it controls**: the host/hypervisor is the outer boundary, the
per-component namespaces the inner one. (An earlier single-Docker-container mode
existed but was container-as-boundary with no per-component isolation; it's been
dropped — see `plans/runtime.md`.)

A prebuilt VM/appliance image (qcow2/OVA/ISO/cloud) is on the roadmap
(`plans/runtime.md`); until it ships you run the binary yourself. Design detail:
`plans/runtime.md` (where it runs) + `plans/isolation.md` (mechanics).

Put xbind on a Linux host (a release binary, or `go build ./cmd/xbind`), build
the base rootfs once (`make rootfs` — needs Docker as a build tool; the appliance
will ship it), and run it as a service (systemd unit, or PID 1 under a tiny init):

```sh
xbind --isolate --rootfs /var/lib/xbin/rootfs \
       --workspace /workspace --listen 127.0.0.1:8642
```

Rootless — no root needed — but the host must provide:

| Requirement | Why |
|---|---|
| **Unprivileged user namespaces** (`kernel.unprivileged_userns_clone=1`; Debian/Arch default-on) | build the sandbox userns; xbind logs whether isolation came up |
| **`/etc/subuid` + `/etc/subgid`** delegating a range to xbind's user (e.g. `xbin:100000:65536`) | map a *full uid range* so `apt`, `sudo`, and non-root in-container users work — else a single-uid fallback (backends still run; apt/user-switching won't) |
| **`newuidmap` / `newgidmap`** (the `uidmap` package) | apply that range rootlessly (setuid, or file caps `cap_setuid,cap_setgid`) |
| **`/dev/fuse`** | mount each sandbox root with fuse-overlayfs so unprivileged directory renames work (`apt install`). xbind ships its own static one (`make` builds it from source); absent it, falls back to kernel overlay |
| **`/dev/net/tun`** | the per-netns egress relay TUN — needed for any `net:*` grant or the terminal internet scope |
| **cgroup v2** (optional) | per-component CPU/mem/pids accounting; under systemd, `Delegate=yes` |
| **NVIDIA driver** (optional) | enables `gpu:*` grants — components/terminals get GPUs by binding the world-readable `/dev/nvidia*` + host driver libs (rootless, no container toolkit). `plans/gpu.md` |

The base rootfs (Go/Node/Python + agent CLIs + `bx`) is bind-mounted read-only
under every sandbox and terminal; refresh it by rebuilding the OCI image
(`docker/rootfs.Dockerfile` → `make rootfs`).

## Deployment on a VM

To stand xbin up on a Linux VM (or bare-metal host) you control, one command:

```sh
curl -fsSL https://raw.githubusercontent.com/magik6k/xbin/master/deploy/install.sh | sudo bash
```

It's interactive, idempotent (re-run to upgrade), and does the whole job:

- **Preflights** the kernel features rootless sandboxing needs — unprivileged
  user namespaces, cgroup v2, `/dev/fuse`, `/dev/net/tun`, `newuidmap` (the
  table above). Add `-s -- --check-only` to print just that report and stop.
- **Installs deps** for your distro (apt/dnf/pacman/zypper): `uidmap`, `fuse3`,
  `git` — and, to build, Go + podman.
- **Builds** `xbind`, `bx`, a static `fuse-overlayfs`, and the base rootfs from
  source. (No release binaries yet; set `XBIN_PREBUILT_BIN` + `XBIN_ROOTFS_DIR`
  to your own to skip the build and the build-only deps.)
- **Creates** the `xbin` system user with home `/opt/xbin` and delegates it a
  `/etc/subuid`+`/etc/subgid` range (so `apt`/`sudo` work inside sandboxes),
  then verifies a user namespace actually comes up for that user.
- **Installs and starts** the service, waits for `/healthz`, and prints your
  one-time login URL.

What it leaves on disk:

```
/opt/xbin/bin/{xbind,bx,fuse-overlayfs}   # owned by the xbin user
/opt/xbin/rootfs/                          # unpacked base rootfs
/opt/xbin/workspace/                       # your data (auto-init on first boot)
/etc/systemd/system/xbin.service           # rendered from deploy/xbin.service
/etc/xbin/xbin.env                        # optional vault passphrase, mode 0600
```

From there it's plain systemd — `systemctl status xbin`, `journalctl -u xbin -f`
(the login URL is in the log: `journalctl -u xbin | grep login`). The service
binds **loopback only** by design; see **Operating → Exposure** to reach it over
Tailscale or a TLS proxy, and **Vault** for the auto- vs manual-unseal choice the
installer offers. Upgrade by re-running the script; uninstall with
`systemctl disable --now xbin && rm /etc/systemd/system/xbin.service && userdel -r xbin`.

Wiring it up by hand instead? The unit is [`deploy/xbin.service`](deploy/xbin.service)
and the installer is [`deploy/install.sh`](deploy/install.sh) — both short and
commented. The one rule: **don't add systemd namespace/filesystem hardening**
(`PrivateUsers`, `RestrictNamespaces`, `ProtectSystem=strict`,
`NoNewPrivileges=yes`, …). xbind builds the sandboxes itself and those directives
break it; the boundary is the VM, kept on loopback behind Tailscale or a proxy.

## Operating

**Data.** Everything is the workspace directory: source, manifests, and the
grant table are plain files you can commit; `.xbin/` (build cache, sockets,
logs) and `data/` (resource state, vault, kv) are the runtime bits. Back up = the
workspace dir (`tar` it, minus `.xbin/`), or `bx backup` for a safe sqlite
checkpoint. Upgrades migrate the workspace schema forward untouched; roll back by
pinning the previous version.

**Vault.** Production never stores secrets in the clear — two ways to run the
encryption barrier:

- **Env auto-unseal** — set `XBIN_VAULT_PASSPHRASE` (a systemd
  `EnvironmentFile`, or a secret store). Hands-off; the passphrase lives in the
  process env.
- **Manual unseal (stronger)** — leave it unset. xbind boots with the vault
  **locked**: it runs and you can log in, but secret storage is refused until an
  admin runs `bx vault unseal` (or the admin console) once. The passphrase never
  touches env or disk; you re-unseal after each restart, like HashiCorp Vault.

The barrier also encrypts **resource data** (kv/filesystem/sqlite/blob), not just
secrets — see [docs/resources.md](docs/resources.md) and `plans/vault-data.md`.
`--insecure-vault` (or `--no-auth`) stores everything plaintext for throwaway
setups; a bare `--dev` instead auto-encrypts with a built-in, insecure dev key so
`make dev` dogfoods encryption. Lose the passphrase and encrypted data is
unrecoverable.

**Exposure.** xbin runs arbitrary code by design and does no TLS itself — bind
it to `127.0.0.1` and reach it over **Tailscale** (map the port on the tailnet)
or a **TLS reverse proxy** (Caddy/Traefik; the session cookie flips to `Secure`
behind `X-Forwarded-Proto: https`). Never raw-expose the port to the internet:
the login token is the only lock.

**Sysctl** (host, once, for large workspaces): raise
`fs.inotify.max_user_watches` — see `docs/getting-started.md`; `bx doctor`
checks the effective limit.

## Hacking on xbin itself

```sh
make dev          # xbind from source against ./devws, ISOLATED — auth ON (login admin/admin),
                  #   web/docs served from disk for live editing
make dev-noauth   # frictionless: every request is admin
make test         # unit tests
make integration
```

`make dev` runs **isolated** on purpose — it should match the sandbox model.
First run builds the base rootfs and a static fuse-overlayfs (both need Docker,
both cached; `make rootfs` / `make fuse-overlayfs` build them on their own), and
it needs unprivileged user namespaces. Delegate a sub-id range to your user
(`/etc/subuid` + `/etc/subgid`) for full apt / non-root-in-container support;
without it, dev falls back to a single-uid sandbox.

Design documents: [ARCHITECTURE.md](ARCHITECTURE.md) and [plans/](plans/)
(implementation plan, auth/multi-user design, runtime/isolation design, and the
decision log with rationale).

## Security posture, honestly

xbin is remote code execution as a feature — treat the box like a dev machine.
The **outer boundary** is the VM/host xbind runs on; keep it bound to loopback
behind Tailscale or a TLS proxy. Inside, `--isolate` gives genuine per-component
OS sandboxing (user/mount/pid/net namespaces + overlay rootfs, **default-deny
`net:*` egress**), on top of real RBAC/grants at the API. Terminals are the
deliberate **owner plane** (full workspace, a host-network escape hatch) — not
sandboxed from you. Browser-side, elements are same-origin: frame tokens give
attribution, not isolation — per-scope origin isolation is roadmap. Details:
[docs/auth.md](docs/auth.md), `plans/isolation.md`.

## License

Dual-licensed MIT ([LICENSE-MIT](LICENSE-MIT)) or Apache-2.0
([LICENSE-APACHE](LICENSE-APACHE)), at your option.
