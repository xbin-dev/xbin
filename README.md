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
  -p 127.0.0.1:8642:8642 \
  --restart unless-stopped \
  ghcr.io/magik6k/buxon:latest
docker logs buxon        # → one-time login URL
```

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

## Hacking on buxon itself

```sh
make dev        # buxond from source against ./devws, auth off, assets from disk
make test       # unit tests
make integration
make image      # docker build
```

Design documents: [ARCHITECTURE.md](ARCHITECTURE.md) and [plans/](plans/)
(implementation plan, auth design, decision log with rationale).

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
