# buxon

A self-modifying, in-browser workspace. Every piece of UI is a directory;
every directory can have a live backend; the tiny square in the corner of any
component opens a real shell in its source. Save a file — the frontend reloads
and the backend recompiles under you. Notion-shaped, but every block is code
you own, and apps talk to each other through granted, role-scoped APIs and
shared resources.

```
Workspace (one host, one git repo)
└── Scope (an app: calendar, email…)     scope.json — owns resources
    └── Component (an element)           a directory: index.html + buxon.json + backend/
```

buxond is a single Go binary; the frontend is buildless (Lit via import maps,
vendored — no bundler anywhere). Full docs are served by the workspace itself at
`/docs/` (also in [docs/](docs/)): getting started, the component contract,
auth/grants, resources, SDKs, wire protocol, CLI.

## What's inside

- **buxond** (one binary): static component serving with a single sanctioned
  HTML transform (import map + client injection), PTY terminals over WebSocket
  (xterm.js), a file watcher driving live reload, and a backend runner —
  `go build` on save, blue/green socket swap, error overlays, crash-loop
  breaking, idle reaping. CGI for shell scripts; node/python restart-on-change.
- **RBAC between elements**: callees declare roles, callers request them, the
  owner approves once (UI panel or `bx grant`); buxond verifies identity on
  every call and injects `X-Buxon-From`/`X-Buxon-Role`. Element frontends are
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
- **Vault**: per-element private secrets (`bx vault`, `buxon.Secret()`),
  encrypted at rest.
- **bx** CLI + **Go SDK** (`github.com/magik6k/buxon/sdk`, zero deps).

## Running it

buxond runs the workspace on a Linux host and — with `--isolate` — **becomes
the sandbox runtime** for its components, building each one's namespaces,
overlay rootfs, and egress relay itself. Two ways to run it, by how strong you
need the boundary between components:

- **Isolated, on a VM or host buxond controls (recommended).** Full
  per-component OS isolation. It's rootless (unprivileged user namespaces), but
  building sandboxes from *inside* someone else's unprivileged container is the
  fragile part (nested userns, missing devices) — so the honest substrate is a
  **Linux VM or bare-metal host**: the host is the outer boundary, the
  per-component namespaces the inner one.
- **Docker, single container (quick / Tier 1).** One container is the boundary;
  components share it. RBAC/grants still apply, but there's no OS-level
  per-component isolation. Simplest to stand up; fine for single-user or
  low-stakes use.

A prebuilt VM/appliance image (qcow2/OVA/ISO/cloud) is on the roadmap
(`plans/runtime.md`); until it ships you run the binary yourself. Design detail:
`plans/runtime.md` (where it runs) + `plans/isolation.md` (mechanics).

### Isolated (VM / host)

Put buxond on a Linux host (a release binary, or `go build ./cmd/buxond`), build
the base rootfs once (`make rootfs` — needs Docker; the appliance will ship it),
and run it as a service (systemd unit, or PID 1 under a tiny init):

```sh
buxond --isolate --rootfs /var/lib/buxon/rootfs \
       --workspace /workspace --listen 127.0.0.1:8642
```

Rootless — no root needed — but the host must provide:

| Requirement | Why |
|---|---|
| **Unprivileged user namespaces** (`kernel.unprivileged_userns_clone=1`; Debian/Arch default-on) | build the sandbox userns; buxond logs whether isolation came up |
| **`/etc/subuid` + `/etc/subgid`** delegating a range to buxond's user (e.g. `buxon:100000:65536`) | map a *full uid range* so `apt`, `sudo`, and non-root in-container users work — else a single-uid fallback (backends still run; apt/user-switching won't) |
| **`newuidmap` / `newgidmap`** (the `uidmap` package) | apply that range rootlessly (setuid, or file caps `cap_setuid,cap_setgid`) |
| **`/dev/fuse`** | mount each sandbox root with fuse-overlayfs so unprivileged directory renames work (`apt install`). buxond ships its own static one (`make` builds it from source); absent it, falls back to kernel overlay |
| **`/dev/net/tun`** | the per-netns egress relay TUN — needed for any `net:*` grant or the terminal internet scope |
| **cgroup v2** (optional) | per-component CPU/mem/pids accounting; under systemd, `Delegate=yes` |

The base rootfs (Go/Node/Python + agent CLIs + `bx`) is bind-mounted read-only
under every sandbox and terminal; refresh it by rebuilding the OCI image
(`docker/rootfs.Dockerfile` → `make rootfs`).

### Docker (single container)

```sh
mkdir -p ~/buxon-ws && cd ~/buxon-ws
curl -O https://raw.githubusercontent.com/magik6k/buxon/master/docker/compose.yml
printf 'BUXON_VAULT_PASSPHRASE=%s\n' "$(openssl rand -base64 24)" > .env   # gitignore this
docker compose up -d
docker compose logs        # → one-time login URL
```

**Always mount `/workspace` explicitly** — a bind mount (`./workspace:/workspace`,
inspectable and git-able) or a named volume. A bare `docker run` *without* it
lands your data in an anonymous volume that's orphaned on `docker rm` — the
classic footgun. With an explicit mount, `docker compose down` throws away only
the container. The image runs as uid 1000 and drops to the bind mount's owner,
so files stay yours.

## Operating (either mode)

**Data.** Everything is the workspace directory: source, manifests, and the
grant table are plain files you can commit; `.buxon/` (build cache, sockets,
logs) and `data/` (resource state, vault, kv) are the runtime bits. Back up = the
workspace dir (`tar` it, minus `.buxon/`), or `bx backup` for a safe sqlite
checkpoint. Upgrades migrate the workspace schema forward untouched; roll back by
pinning the previous version.

**Vault.** Production never stores secrets in the clear — two ways to run the
encryption barrier:

- **Env auto-unseal** — set `BUXON_VAULT_PASSPHRASE` (Compose reads it from
  `.env`; a systemd `EnvironmentFile` or secret store on a host). Hands-off; the
  passphrase lives in the process env.
- **Manual unseal (stronger)** — leave it unset. buxond boots with the vault
  **locked**: it runs and you can log in, but secret storage is refused until an
  admin runs `bx vault unseal` (or the admin console) once. The passphrase never
  touches env or disk; you re-unseal after each restart, like HashiCorp Vault.

`--insecure-vault` (implied by `--dev`) is the only way to store plaintext, for
throwaway setups. Lose the passphrase and encrypted secrets are unrecoverable.

**Exposure.** buxon runs arbitrary code by design and does no TLS itself — bind
it to `127.0.0.1` and reach it over **Tailscale** (map the port on the tailnet)
or a **TLS reverse proxy** (Caddy/Traefik; the session cookie flips to `Secure`
behind `X-Forwarded-Proto: https`). Never raw-expose the port to the internet:
the login token is the only lock.

**Sysctl** (host, once, for large workspaces): raise
`fs.inotify.max_user_watches` — see `docs/getting-started.md`; `bx doctor`
checks the effective limit.

## Hacking on buxon itself

```sh
make dev          # buxond from source against ./devws, ISOLATED — auth ON (login admin/admin),
                  #   web/docs served from disk for live editing
make dev-noauth   # frictionless: every request is admin
make test         # unit tests
make integration
make image        # build the Docker (Tier-1) image
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

buxon is remote code execution as a feature — treat the box like a dev machine.
The **outer boundary** is the VM/host (isolated) or the container (Tier 1); keep
it bound to loopback behind Tailscale or a TLS proxy. Inside, defense is layered:
real RBAC/grants at the API, per-scope uids (`--scope-uids`), and — with
`--isolate` — genuine per-component OS sandboxing (user/mount/pid/net namespaces
+ overlay rootfs, **default-deny `net:*` egress**, cgroup accounting). Terminals
are the deliberate **owner plane** (full workspace, a host-network escape hatch)
— not sandboxed from you. Browser-side, elements are same-origin: frame tokens
give attribution, not isolation — per-scope origin isolation is roadmap. Details:
[docs/auth.md](docs/auth.md), `plans/isolation.md`.

## License

Dual-licensed MIT ([LICENSE-MIT](LICENSE-MIT)) or Apache-2.0
([LICENSE-APACHE](LICENSE-APACHE)), at your option.
