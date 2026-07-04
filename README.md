# buxon

A self-modifying, in-browser workspace. Every piece of UI is a directory;
every directory can have a live backend; the tiny square in the corner of
any component opens a real shell in its source. Save a file — the frontend
reloads and the backend recompiles under you. Notion-shaped, but every block
is code you own, and apps can talk to each other through granted,
role-scoped APIs and shared resources.

```
Workspace (one container, one git repo)
└── Scope (an app: calendar, email…)     scope.json — owns resources
    └── Component (an element)           a directory: index.html + buxon.json + backend/
```

## Quick start

```sh
mkdir -p ~/buxon-ws
docker run -d --name buxon \
  -v ~/buxon-ws:/workspace \
  -e BUXON_VAULT_PASSPHRASE='change-me-to-a-strong-passphrase' \
  -p 127.0.0.1:8642:8642 \
  --restart unless-stopped \
  ghcr.io/magik6k/buxon:latest
docker logs buxon        # → one-time login URL
```

`BUXON_VAULT_PASSPHRASE` encrypts the vault at rest (the key never touches the
data dir). Set it for hands-off auto-unseal, or **omit it and unseal manually
after login** (`bx vault unseal`) for the stronger mode where the passphrase
is never stored — either way production never writes secrets in the clear. For
a real deployment use **[Docker Compose](#deployment)** rather than a raw
`docker run`, and keep any passphrase in a gitignored `.env`.

Full docs are served by the workspace itself at `/docs/` (also in
[docs/](docs/) here): getting started, the component contract, auth/grants,
resources, SDKs, wire protocol, CLI.

## What's inside

- **buxond** (Go, one binary): static component serving with a single
  sanctioned HTML transform (import map + client injection), PTY terminal
  sessions over WebSocket (xterm.js in front), a file watcher driving live
  reload, and a backend runner — `go build` on save, blue/green socket swap,
  error overlays, crash-loop breaking, idle reaping. CGI for shell scripts,
  node/python restart-on-change.
- **RBAC between elements**: callees declare roles, callers request them in
  their manifest, the owner approves once (UI panel or `bx grant`), buxond
  verifies identity on every call and injects `X-Buxon-From`/`X-Buxon-Role`.
  Element frontends are attributed via frame tokens; element backends via
  per-generation instance credentials on a gateway socket.
- **Resources**: kv, blob, bus (live cross-app events into the browser),
  cron (scheduled calls that wake idle backends), sqlite provisioning —
  all in the same grant grammar (`res:apps/calendar/bus`).
- **Per-component OS isolation** (opt-in `--isolate`, rootless): each backend
  runs in its own user + mount + pid + ipc + uts + net namespaces over an
  overlay of a shared base rootfs; egress is **default-deny** with a transparent
  userspace relay that enforces `net:*` grants for TCP/UDP/ICMP; cgroup v2
  accounting. Terminals share the base rootfs (Go/Node/Python + agent CLIs, zero
  setup) and pick a per-session **network scope** — internet-only (own netns, no
  host interfaces), host, or offline. See `plans/isolation.md`, `plans/runtime.md`.
- **Vault**: per-element private secrets (`bx vault`, `buxon.Secret()`).
- **bx** CLI + **Go SDK** (`github.com/magik6k/buxon/sdk`, zero deps) +
  buildless frontend (Lit via import maps; vendored, offline-capable, no
  bundler anywhere).

## Deployment

There are two ways to run buxon, differing in **how strong the boundary between
components is**:

- **Docker (Tier 1)** — one container is the security boundary; components share
  it. RBAC/grants still apply, but there's no OS-level per-component isolation.
  Simplest; fine for single-user or low-stakes use. This is the Quick start above.
- **Isolated on a VM/host (Tier 3)** — buxond itself becomes the sandbox runtime,
  building per-component namespaces + an overlay rootfs + an egress relay. This is
  the recommended production model and needs a Linux host buxond controls plus a
  few host features (see [Isolated mode](#isolated-mode-on-a-vm--host) below).

Full design: `plans/runtime.md` (where it runs) + `plans/isolation.md` (mechanics).

### Docker (Tier 1)

buxon ships as a **single-container appliance**: one image with the toolchains
baked in, all state in one bind-mounted `/workspace`. The recommended model is
**Docker Compose with a bind mount** — it's transparent (your workspace is a
real directory you can inspect, `git` and back up), declarative, and keeps the
vault passphrase out of your shell history.

```sh
mkdir -p ~/buxon-ws && cd ~/buxon-ws
curl -O https://raw.githubusercontent.com/magik6k/buxon/master/docker/compose.yml
printf 'BUXON_VAULT_PASSPHRASE=%s\n' "$(openssl rand -base64 24)" > .env   # gitignore this
docker compose up -d
docker compose logs        # → one-time login URL
```

**Data persistence — the one thing to get right.** All state lives under
`/workspace`; the image declares it a volume. **Always mount it explicitly**
(a bind mount like `./workspace:/workspace`, or a named volume). If you run a
bare `docker run` *without* mounting `/workspace`, Docker puts your data in an
**anonymous volume** that is orphaned on `docker rm` and easy to lose — the
classic footgun. With an explicit mount, `docker rm -f buxon` /
`docker compose down` throws away only the container; your workspace stays.

| Mount style | Good for |
|---|---|
| **bind mount** (`./workspace:/workspace`) | recommended — inspectable, git-able, back up by copying the dir |
| **named volume** (`buxon-data:/workspace`) | portable/host-agnostic; back up with a helper container |

`.buxon/` (build cache, sockets, logs) and `data/` (resource state, vault,
kv) are the runtime bits; source, manifests, and the grant table are plain
files you can commit. Ownership: started as root, buxond drops to the bind
mount's owner uid, so files stay yours.

**Secrets / the vault.** Production never stores secrets in the clear. Two
ways to run the encryption barrier:

- **Env auto-unseal** — set `BUXON_VAULT_PASSPHRASE` (Compose reads it from
  `.env`; use a Docker/Swarm secret in a cluster). Hands-off; the passphrase
  lives in the container env.
- **Manual unseal (stronger)** — leave it unset. buxond boots with the vault
  **locked**: it runs and you can log in, but secret storage is refused until
  an admin runs `bx vault unseal` (or the admin console) once, which creates
  the barrier from the passphrase they type and encrypts anything existing.
  The passphrase never touches the container env or disk; you re-unseal after
  each restart, like HashiCorp Vault.

`--insecure-vault` (or `--dev`) is the only way to store plaintext, for
throwaway setups. Whatever you choose, keep the passphrase out of
`compose.yml` and git — lose it and the encrypted secrets are unrecoverable.

**Exposure.** The example binds to `127.0.0.1` on purpose. buxon runs
arbitrary code by design and does no TLS itself — reach it over **Tailscale**
(map the port on the tailnet) or a **TLS reverse proxy** (Caddy/Traefik;
buxon's cookie flips to `Secure` behind `X-Forwarded-Proto: https`). Never
raw-expose the port to the internet: the login/token is the only lock.

**Backups & upgrades.** Backup = the workspace directory (`tar` it, minus
`.buxon/`), or `bx backup` which checkpoints sqlite safely. Upgrade =
`docker compose pull && docker compose up -d`; the mounted workspace and its
schema migrate forward untouched. Roll back by pinning the previous image tag.

Sysctl (host, once, for large workspaces): `fs.inotify.max_user_watches` — see
`docs/getting-started.md`; `bx doctor` checks the effective limit.

### Isolated mode (on a VM / host)

For per-component OS isolation, buxond stops living *inside* someone else's
container and instead **becomes the sandbox runtime** on a host it controls: it
builds each component's namespaces, overlay rootfs, cgroups, and egress relay.
Doing that from inside an unprivileged Docker container is the fragile part
(nested userns, missing devices), so the honest deployment is a **Linux VM or
bare-metal host** — the hypervisor/host is the outer boundary, the per-component
namespaces are the inner one. (A prebuilt VM/appliance image is on the roadmap;
`plans/runtime.md`.)

Run buxond directly (systemd unit, or PID 1 under a tiny init) with
`--isolate --rootfs <dir>`; `make rootfs` builds the base rootfs, or the
appliance ships it. It's **rootless** — no root needed — but the host must
provide:

| Requirement | Why | Notes |
|---|---|---|
| **Unprivileged user namespaces** | build the sandbox userns rootless | `kernel.unprivileged_userns_clone=1` (Debian/Arch default-on); buxond logs whether tier-3 isolation came up at startup |
| **`/etc/subuid` + `/etc/subgid` delegation** for the buxond user | map a *full uid range* so `apt`, `sudo`, and non-root in-container users work | e.g. `buxon:100000:65536`; without it, falls back to a single-uid map (backends still run; apt/user-switching won't) |
| **`newuidmap` / `newgidmap`** (the `uidmap` package) | apply that range rootlessly | setuid or file-cap `cap_setuid,cap_setgid` |
| **`/dev/fuse`** | mount each sandbox root with fuse-overlayfs (so `apt install` etc. work) | buxond ships its own static fuse-overlayfs (`make` / `hack/build-fuse-overlayfs.sh`); absent `/dev/fuse` it falls back to kernel overlay |
| **`/dev/net/tun`** | the egress relay's TUN, per netns | needed whenever a component has `net:*` grants or a terminal uses the internet scope |
| **cgroup v2** (optional) | per-component CPU/mem/pids accounting | delegated leaf; under systemd set `Delegate=yes` |

The base rootfs (Go/Node/Python + agent CLIs + `bx`) is bind-mounted read-only
under every sandbox and terminal; refresh it by rebuilding the OCI image
(`docker/rootfs.Dockerfile` → `make rootfs`). The workspace, vault, exposure,
and backup guidance above all still apply — only the boundary changes.

## Hacking on buxon itself

```sh
make dev          # buxond from source against ./devws — auth ON (login admin/admin),
                  #   web/docs served from disk for live editing
make dev-noauth   # frictionless: every request is admin
make test         # unit tests
make integration
make image        # docker build
```

`make dev` runs **isolated** on purpose (it should match the Tier-3 sandbox
model): first run builds the base rootfs and a static fuse-overlayfs — both need
Docker, both cached (`make rootfs` / `make fuse-overlayfs` build them alone) —
and it needs unprivileged user namespaces. Delegate a sub-id range to your user
(`/etc/subuid` + `/etc/subgid`) for full apt / non-root-in-container support;
without it, dev falls back to a single-uid sandbox.

Design documents: [ARCHITECTURE.md](ARCHITECTURE.md) and [plans/](plans/)
(implementation plan, auth/multi-user design, decision log with rationale).

## Security posture, honestly

buxon is remote code execution as a feature — treat the box like a dev machine:
bind it to loopback, reach it over Tailscale or a TLS proxy. The **outer
boundary** is the container (Tier 1) or the VM/host (Tier 3); keep it that way.
Inside, defense is layered: real RBAC/grants at the API, per-scope uids
(`--scope-uids`), and — with `--isolate` — genuine per-component OS sandboxing
(user/mount/pid/net namespaces + overlay rootfs, **default-deny egress** through
the `net:*` relay, cgroup accounting). Terminals are the deliberate **owner
plane** (full workspace, a host-network escape hatch) — not sandboxed from you.
Details: [docs/auth.md](docs/auth.md), `plans/isolation.md`.

## License

Dual-licensed MIT ([LICENSE-MIT](LICENSE-MIT)) or Apache-2.0
([LICENSE-APACHE](LICENSE-APACHE)), at your option.
