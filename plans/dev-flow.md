# Buxon — Development Flow

Two distinct loops: **working on Buxon core** (this repo) and **working inside a
Buxon workspace** (the product experience). Both must stay fast; the second is the
product, the first is our daily life.

## 1. Working on Buxon core

### Prerequisites
Go ≥ 1.24 and git, plus **Docker** (to build the dev rootfs once) and
unprivileged **user namespaces** — because `make dev` now runs **isolated**.
The frontend is still buildless (vendored deps checked in).

### The loop

```
make rootfs       # once: build the base rootfs (docker → .rootfs/, ~2 GB, cached)
make dev          # go run ./cmd/buxond --dev --isolate … (auth ON, assets from disk)
make dev-noauth   # same, but --no-auth: every request is admin
```

**Dev runs isolated** (per-component namespaces + overlay rootfs + egress relay):
the sandbox network/fs model is different enough from unsandboxed that dev must
match it. It's rootless (no root needed) but requires unprivileged user
namespaces and a base rootfs — `make dev` builds it via `make rootfs` on first
run (docker; cached in `.rootfs/`; override with `ROOTFS=/path make dev`).
Component backends run in their sandboxes; the netns is default-deny egress
until a component is granted `net:*` (docs/auth.md).

`--dev` (orthogonal to `--isolate`) means: `web/`+`docs/` served from disk (edit
bx-frame.js → hard-refresh browser, no rebuild), verbose logs, and `devws/`
auto-initialized from `workspace-template/` on first run (gitignored). It **does
not imply `--no-auth`** — `make dev` runs with auth on and seeds a dev admin
(login `admin`/`admin`, plus the root token URL in the logs) so multi-user RBAC
can be exercised while live-editing core elements. `make dev-noauth` is the
frictionless admin-everything loop (`--dev --no-auth --isolate`).

- Restarting buxond on Go changes: `hack/dev.sh` wraps `go run` with a file watcher
  (use `gow` or a 10-line inotify loop — do **not** take a dependency on air; keep
  contributor setup at "go + git") `[D11: buxond restart kills terminal sessions in
  dev — accepted]`.
- `devws/` accumulates realistic mess on purpose; `make dev-reset` nukes it.
  `examples/` components are symlinked into `devws/` so example, test fixture, and
  manual test are the same files.

### Testing

| Layer | What | How |
|---|---|---|
| Unit | watch debounce/coalescing, path confinement, manifest parse, deps reconcile, import-map merge, runner state machine (fake exec) | `go test ./...`, no network, < 10 s |
| Integration | real buxond on `httptest` listener + temp workspace + real `go build` of example components; terminal WS round-trip incl. reattach/scrollback; blue/green swap under concurrent requests; crash-loop breaker | `go test -tags=integration ./test/…`, needs Go toolchain only, ~1–2 min |
| E2E (thin) | Playwright: load root, frame renders, edit button opens drawer, type in terminal, live reload fires `[D12]` | `make e2e`, runs in CI on the built image, kept to ≤ ~10 flows |

Policy: every bug fix lands with the test that would have caught it, at the lowest
layer that can express it. The runner state machine and the watcher are the two
components that will otherwise rot into heisenbugs — they get table-driven unit
suites from day one.

### CI (GitHub Actions)
1. `lint`: `golangci-lint` + `go vet` + `gofmt` check.
2. `test`: unit + integration (Linux; macOS integration job weekly, not per-PR).
3. `image`: build `docker/Dockerfile` (no push) — catches image rot per-PR.
4. `e2e`: Playwright against the built image (after phase 1 lands).
5. On tag `v*`: push multi-arch image (amd64/arm64) to registry `[D10]`, attach
   `buxond` binaries to the GitHub release.

### Conventions
- Commits: conventional-ish (`term:`, `runner:`, `web:` prefixes), PRs optional while
  solo but CI must be green on `master`.
- Frontend code style: plain ES2022, Lit 3 idioms, no TS syntax; JSDoc types where it
  helps; `// @ts-check` headers welcome (checked by IDE only, never by CI —
  no build/typecheck step is a hard rule).
- Every exported buxond HTTP/WS endpoint gets a paragraph in `docs/protocol.md` in
  the same PR that adds it (frames, `bx`, and future SDKs all consume this).

### Debugging notes
- Component backend processes: `dlv attach $(pidof <bin>)` from any workspace
  terminal works because everything is same-user in one container/host.
- buxond itself in dev: it's just `go run`; use dlv normally.
- `bx doctor` (phase 3) is also the support tool for "my thing doesn't reload".

## 2. Working inside a workspace (the product loop)

Documented here as the target UX; phases must not regress it.

1. **Create**: open any terminal → `bx new apps/thing --runtime go` (or plain
   `mkdir` + `index.html` for static). Frame it: add `<bx-frame src="apps/thing">`
   to whatever component should show it — often `root`, itself edited via its own
   7×7 button.
2. **Edit**: 7×7 → drawer shell → `vim`/`claude`/whatever. Save → frontend reloads /
   backend rebuilds automatically. Compiler errors appear as the frame overlay AND
   in red in the terminal that has the component's log tail open.
3. **Inspect**: `tail -f .buxon/log/<id>.log`, `bx logs -f apps/thing`, `bx ls`,
   `curl $BUXON_URL/api/apps/thing/...` (terminals have the token in env).
4. **Compose**: declare `deps`, import through `deps/…` symlinks or Go packages via
   go.work; request resources in the manifest; approve grants in the root panel or
   `bx grant`.
5. **Version**: the workspace is a git repo; `git add -A && git commit` in any
   terminal is the checkpoint story v1 `[D2]`.

Latency budgets (regression-tested in integration suite):
- static save → frame reload: **< 500 ms**
- Go save → new backend serving: **< 2 s** warm cache
- terminal keystroke echo: **< 30 ms** LAN
