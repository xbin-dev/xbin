# xbin — repo guide

Self-modifying browser workspace. Design: `ARCHITECTURE.md` + `plans/`
(implementation plan, auth design, `plans/DECISIONS.md` for every decision
with rationale — check there before re-litigating a choice). Builder-facing
docs: `docs/` (embedded into xbind, served at `/docs/`; keep them true —
they are the contract `bx`, SDKs, and workspace builders rely on). Dev
practices in full: `AGENTS.md`.

## Commands

- `make dev` — xbind from source against `./devws`, no auth, web/docs from disk
- `make test` / `make integration` — unit / end-to-end (integration compiles
  real Go backends; needs network for module downloads on first run)
- `make fmt-check vet` — CI mirrors these
- `./hack/vendor.sh` — refresh pinned frontend deps (lit, xterm, marked)

## Hard rules

- **No JS build step, ever.** Frontend is plain ES modules + import maps;
  vendored single-file deps in `web/vendor/`. TS syntax is banned in `web/`.
- **One sanctioned HTML transform** (import-map/client injection in
  `internal/server/static.go`). Do not add rewriting.
- The `sdk/` module must stay **zero-dependency** — components inherit it.
- New xbind HTTP/WS surface ⇒ document it in `docs/protocol.md` in the same
  change; builder-visible behavior ⇒ the relevant `docs/*.md` **and a
  `docs/changelog.md` entry**; breaking ⇒ also a migration note at
  `docs/changes/YYYY-MM-DD-<slug>.md` linked from the changelog (AGENTS.md
  has the template).
- Grants/identity changes must keep `plans/auth.md` semantics: xbind strips
  inbound `X-XBin-*`, default-deny for element principals, owner is admin.

## Layout

`cmd/xbind` (daemon), `cmd/bx` (CLI), `internal/{server,registry,watch,
term,runner,proxy,broker,auth,events,deps,jsonc,util}`, `sdk/` (Go SDK,
own module), `web/` (bx-frame, bx-terminal, bx-grants, xbin-client),
`workspace-template/` (scaffold for `xbind init`), `examples/`
(counter-go, calendar, email — these double as integration fixtures and
docs; keep them working).
