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

`BUXON_VAULT_PASSPHRASE` is **required in production** — buxond refuses to
start with a plaintext vault (secrets encrypted at rest; the key is never in
the data dir). For a real deployment use **[Docker Compose](#deployment)**
rather than a raw `docker run`, and keep the passphrase in a gitignored
`.env`, not on the command line.

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
- **Vault**: per-element private secrets (`bx vault`, `buxon.Secret()`).
- **bx** CLI + **Go SDK** (`github.com/magik6k/buxon/sdk`, zero deps) +
  buildless frontend (Lit via import maps; vendored, offline-capable, no
  bundler anywhere).

## Deployment

buxon is a **single-container appliance**: one image with the toolchains baked
in, all state in one bind-mounted `/workspace`. The recommended model is
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

**Secrets.** `BUXON_VAULT_PASSPHRASE` gates the encryption barrier and is
**required** — production refuses to start with a plaintext vault (override
only with `--insecure-vault` if you knowingly accept plaintext). Keep it in
`.env` (Compose reads it automatically) or a Docker/Swarm secret, never inline
in `compose.yml`. Lose it and the encrypted secrets are unrecoverable.

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

## Hacking on buxon itself

```sh
make dev          # buxond from source against ./devws — auth ON (login admin/admin),
                  #   web/docs served from disk for live editing
make dev-noauth   # frictionless: every request is admin
make test         # unit tests
make integration
make image        # docker build
```

Design documents: [ARCHITECTURE.md](ARCHITECTURE.md) and [plans/](plans/)
(implementation plan, auth/multi-user design, decision log with rationale).

## Security posture, honestly

buxon is remote code execution as a feature. The container is the boundary:
bind it to loopback, reach it over Tailscale or a TLS proxy, give it the
resources you'd give a dev box. The grant system is real RBAC at the API
layer; per-scope uid isolation (`--scope-uids`) hardens the floor; full
syscall-level sandboxing is roadmap (see `plans/auth.md` §9). Details:
[docs/auth.md](docs/auth.md).

## License

Dual-licensed MIT ([LICENSE-MIT](LICENSE-MIT)) or Apache-2.0
([LICENSE-APACHE](LICENSE-APACHE)), at your option.
