# Buxon — Deployment Flow

Target: one container, one bind-mounted workspace, one exposed port. A person with
Docker and five minutes gets a running workspace; upgrades never touch their data.

## Image

`docker/Dockerfile`, multi-stage:

1. **build**: `golang:1.x` → `CGO_ENABLED=0 go build ./cmd/buxond` (web assets
   embedded).
2. **runtime**: base `debian:stable-slim` +
   - toolchains: Go (full toolchain — components need it), Node LTS, Python 3,
     `build-essential`, git `[D1: fat image, ~2.5–3 GB — accepted, slim variant later]`
   - editors/tools for the terminal experience: vim, nano, less, curl, jq, ripgrep,
     tmux (for people who want their own mux inside sessions), openssh-client
   - `buxond`, `bx` in `/opt/buxon/bin` (on PATH)
   - sysctl-friendly defaults documented (inotify limits — see Run)
3. Image runs as **uid 1000 `buxon`**, not root `[D13]`. `ENTRYPOINT ["buxond"]`
   (buxond does the PID-1 duties: signal forwarding to component processes, zombie
   reaping — no tini needed, one less moving part).

Variants: `buxon:<ver>` (fat, default). `buxon:<ver>-slim` (no toolchains, static +
cgi-shell only) is backlog, not v1.

## Release flow

- Tag `vX.Y.Z` → GitHub Actions: tests → multi-arch image (amd64, arm64) pushed to
  `ghcr.io/<owner>/buxon` `[D10]` → GitHub release with standalone `buxond` binaries
  (linux/amd64, linux/arm64, darwin/arm64 — the binary alone is the "host mode" for
  dev/poweruser use; docs treat container as the supported path).
- Versioning: semver; **workspace schema version** stored in workspace `buxon.json`
  (`"schema": 1`). buxond refuses to open a *newer* schema; migrates older schemas
  forward automatically with a pre-migration safety commit if the workspace is a git
  repo (else a `.buxon/backup-pre-migrate.tar.zst`).

## Install / first run

Recommended: **Compose + bind mount + `.env` passphrase** (see README
§Deployment). The bare `docker run` still works for a quick try:

```sh
mkdir -p ~/buxon-ws
docker run -d --name buxon \
  -v ~/buxon-ws:/workspace \
  -e BUXON_VAULT_PASSPHRASE='a-strong-passphrase' \
  -p 127.0.0.1:8642:8642 \
  --restart unless-stopped \
  ghcr.io/<owner>/buxon:latest
docker logs buxon   # prints one-time login URL with token
```

- **Secure-by-default vault:** production (no `--dev`) refuses to start with a
  plaintext vault. Provide `BUXON_VAULT_PASSPHRASE` (auto-creates + unseals the
  AES-256-GCM barrier) or, to knowingly accept plaintext, pass
  `--insecure-vault`. Keep the passphrase in `.env`/a secret, never inline.
- **Data persistence:** always mount `/workspace` explicitly (bind mount or
  named volume). A bare run without the mount lands data in an anonymous
  volume — orphaned on `docker rm`, easy to lose. Bind mount is preferred
  (transparent, git-able, back up by copying the dir).
- Empty mount → buxond runs `init` automatically (template + git init per `[D2]`).
- Binds `0.0.0.0:8642` **inside** the container; the example maps to loopback on
  purpose — remote exposure is explicitly the operator's move (below).
- Compose file shipped in repo (`docker/compose.yml`) with the same content + named
  volume alternative + `ulimits`/`sysctls` for inotify
  (`fs.inotify.max_user_watches=524288` — must be set on the **host**; documented,
  and `bx doctor` checks the effective limit).
- Recommended (not enforced) hardening in compose: memory/pids limits
  (`mem_limit: 4g`, `pids_limit: 2048`), no extra capabilities. NOT `read_only` —
  the workspace premise is writes.

## Remote access & TLS

buxond speaks plain HTTP + its own auth; it will not grow TLS/ACME `[D14]`.
Documented patterns, in order of recommendation:
1. **Tailscale/WireGuard** to the host, map port on tailnet IP. Zero certs, fits the
   single-user model.
2. **Caddy/Traefik in front** for a public hostname (compose example provided).
   Buxon's cookie is `Secure` when it sees `X-Forwarded-Proto: https`.
3. Never raw-expose 8642 to the internet: token auth is the only lock, and the
   payload behind it is arbitrary code execution.

WebSockets must be proxied (both patterns above handle this; documented for nginx
users: `Upgrade`/`Connection` headers).

## Upgrade

```sh
docker pull ghcr.io/<owner>/buxon:latest
docker rm -f buxon && docker run … (same flags)   # or: docker compose up -d
```

- Workspace mount is untouched; schema migrations run on boot (see Release flow).
- In-container state that must survive upgrades lives in `/workspace` **only** —
  enforced by convention and by an integration test that diffs container fs writes
  outside `/workspace` (catches accidental `/root/.cache` style leaks; HOME is inside
  the workspace `[D6]` largely for this reason).
- Rollback = run the previous tag. Schema migrations must be
  backward-tolerant within one minor version (older buxond opens a workspace touched
  by ≤ one minor newer, read-only warnings allowed) — cheap insurance, tested.

## Backup & restore

- **The workspace dir is the backup unit.** `tar` of `/workspace` minus `.buxon/`
  is a complete cold backup.
- Hot backups: sqlite in WAL mode is the only consistency hazard →
  `bx backup <dest.tar.zst>`: broker checkpoints each sqlite resource
  (`VACUUM INTO` per db) into a staging dir, tars workspace (excluding `.buxon/`
  and `data/vault/` unless `--with-vault`, substituting checkpointed dbs), streams
  to dest. Cron it from the host or a
  scheduled component (dogfooding).
- Restore: untar into an empty dir, mount it, start. Nothing else.
- Git remains the fine-grained history mechanism for *source*; `data/` is
  gitignored by default `[D2]` — backups, not git, own runtime state.

## Ops surface (small on purpose)

- Logs: buxond → container stdout (structured slog); component logs →
  `/workspace/.buxon/log/` (rotated, also `bx logs`).
- Health: `GET /healthz` unauthenticated 200 (for restart policies / uptime checks),
  `GET /api/buxon/status` authenticated (component states, build queue, session
  count, watch count).
- Metrics: not in v1; `/api/buxon/status` is scrapeable JSON if someone insists.
- Crash behavior: buxond exits → container exits → `--restart unless-stopped`
  brings it back; terminal sessions are lost (documented), backends restart lazily,
  no data loss (all state on disk).
