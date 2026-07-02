# buxon — repo guide

Self-modifying browser workspace. Design: `ARCHITECTURE.md` + `plans/`
(implementation plan, auth design, `plans/DECISIONS.md` for every decision
with rationale — check there before re-litigating a choice). Builder-facing
docs: `docs/` (embedded into buxond, served at `/docs/`; keep them true —
they are the contract `bx`, SDKs, and workspace builders rely on).

## Commands

- `make dev` — buxond from source against `./devws`, no auth, web/docs from disk
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
- New buxond HTTP/WS surface ⇒ document it in `docs/protocol.md` in the same
  change; builder-visible behavior ⇒ the relevant `docs/*.md`.
- Grants/identity changes must keep `plans/auth.md` semantics: buxond strips
  inbound `X-Buxon-*`, default-deny for element principals, owner is admin.

## Layout

`cmd/buxond` (daemon), `cmd/bx` (CLI), `internal/{server,registry,watch,
term,runner,proxy,broker,auth,events,deps,jsonc,util}`, `sdk/` (Go SDK,
own module), `web/` (bx-frame, bx-terminal, bx-grants, buxon-client),
`workspace-template/` (scaffold for `buxond init`), `examples/`
(counter-go, calendar, email — these double as integration fixtures and
docs; keep them working).
