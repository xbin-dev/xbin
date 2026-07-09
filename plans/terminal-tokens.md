# Terminal-scoped tokens — design

Status: **implemented** (2026-07-09), with one change from the original
sketch: the **root terminal is disabled entirely** (owner's call — it was
reachable from no UI; whole-workspace editing and owner-token automation live
on the host). So every terminal token is a tile element principal, and the
live-user root-resolution branch below was dropped. `bx` in a tile terminal
prints a scope hint on 403s. Kill/reset also gained creator-or-admin /
tile-access gates.

**Filesystem bypass closed (2026-07-10, Gap 0).** The token scoping is only a
boundary if the terminal can't *read the owner token off disk*. The isolated
terminal's read-only workspace mount now masks `.xbin/` (owner token +
frame-token secret), `data/` (vault, resource state, password hashes), and
other users' `homes/`, so `cat .xbin/token` no longer re-grants owner. A
per-session **api=0** toggle withholds the token entirely (code-only shell).
Residual: a single-uid sandbox shell is root in its userns and could `umount`
a mask; full robustness comes with per-tenant uids (multi-tenant work). Tier-1
(non-isolated host shell) terminals are unchanged — the masks are an
`--isolate` property. See docs/isolation.md.

## Problem

Every terminal env carries `XBIN_TOKEN = a.OwnerToken` (main.go's term Env
closure) → `Principal{Owner: true}` → `IsAdmin()` everywhere. So `bx`/`curl`
(and any agent) inside *any* tile terminal can today:

- read any tile's admin surface — `proxy.Policy` gives an admin caller role
  `admin` on **every** component (`broker.go:Policy`), so e.g. llm-gw's
  `/config` (base URLs, aliases), any tile's admin endpoints;
- call the whole `/api/xbin` admin surface: `/auth-overview`, `/runtime`,
  lifecycle, backups/restore, users (**create an admin user**), and
  `POST /grants` — i.e. an agent can **self-approve its own cross-scope
  grants**, the exact thing workspace AGENTS.md §Auth forbids but nothing
  technically prevents;
- the git `insteadOf` env-config (term.sandboxEnv) bakes this owner token into
  every terminal's git config.

The vault lockdown (2026-07-08) already removed secret-value reads from this
surface; everything else remains. Meanwhile **backends already solved this**:
the runner mints per-generation instance tokens (`runner.go:369,431,504`), and
tile frontends get frame tokens whose principal explicitly does **not**
inherit the driving user's privilege (`auth.go framePrincipal`). Terminals are
the last owner-token leak.

## Design

**New token class: terminal tokens**, symmetric with instance/frame tokens.
An in-memory registry on `auth.Auth` (mirroring `instances`):

```
terminals: map[string]termID     // token → {component, userID}
Mint/Revoke/lookup; resolved in FromRequest's bearer chain
(owner → instance → terminal → reject), and in the noAuth branch too, so dev
mode exercises the same RBAC (as instance tokens already do).
```

Resolution semantics — this is where min(user, tile) lands:

- **Tile terminal** (`component != ""`) → element principal
  `{Component: tile, UserID: user, Via: "terminal"}`. Exactly the frame-token
  model: the tile acting as itself — **admin of itself** (self-policy), its
  vault, its resources (same-scope auto-grants), its approved `uses`, its
  http-binding grants. `IsAdmin()` = false **even for admin users** — that's
  the min(): the terminal is scoped to the tile; admin work happens in the
  root terminal or the browser. UserID rides for attribution (X-XBin-From
  could show `user@tile`), prefs bucketing, and audit.
- **Root terminal** (`component == ""`) → the **live user principal**: resolve
  `userID` against the user store per request (snapshot like cookie auth), so
  demoting a user instantly downgrades their open root shells. Admin user →
  admin (owner-plane, as today). Bootstrap-token session (no user) → owner.
- **Lifecycle**: minted in `term.create()` (Manager gets a small
  `Tokens interface { Mint(comp, user string) string; Revoke(string) }` wired
  from main), injected as `XBIN_TOKEN` via the per-session env (same seam as
  the per-user `HOME`), revoked in the pump-cleanup where `envKey` is
  released. Sessions don't survive a daemon restart, so in-memory is correct
  (same as instance tokens). Reattach reuses the session env unchanged.

**Session-open gates** (the user half of min(user, tile)) — two gaps found:

1. `term.create()` never checks `CanUseTile(cwd)`: any Terminal-flagged user
   can open a terminal on **any** tile. Gate it.
2. The root terminal isn't admin-gated: `authedTerminal` only checks
   `CanTerminal()` (= `IsAdmin || u.Terminal`), while its own 403 message
   already claims "terminal access is admin-only (root shell)". A non-admin
   Terminal-flagged user gets the whole workspace **rw** + (today) the owner
   token. Gate `cwd == ""` on `IsAdmin()`; the Terminal flag then means
   "tile terminals on my allow-list", which is what it reads like.

## Blast radius (what a tile terminal can no longer do)

| Was (owner token) | Becomes (tile terminal token) |
|---|---|
| `bx grant` approve, `/users`, lifecycle, backups, `/runtime`, `/auth-overview` | 403 — do it in a **root terminal** or the browser as an admin |
| read any tile's `/api/<tile>` at admin | own tile: admin (self); others: only per the tile's approved grants/bindings |
| `bx vault set <any-tile>` | own tile only (self-vault); admin management stays in root/browser |
| `/code/*`, `/git/*` of any component | own component (self) or `code:` grants — note the FS already shows siblings read-only via the ro workspace bind; the API staying grant-gated is a (documented) inconsistency |
| `git fetch template` (admin-gated serve) | **needs a fix**: relax `templaterepo.go:111` to any authenticated principal — builtin template sources are embedded, identical in every install, not secret |
| create components / `bx new` | root terminal (the tile terminal's ro workspace bind already prevented the mkdir anyway — the token change aligns API scope with the existing FS scope) |

Unchanged: `bx` against one's own tile, calling bound providers (binding-as-
grant works for element principals), `/components`, `/whoami`, docs, prefs
(terminal token carries the same (user×tile) identity as frame tokens).
Backends, frontends, cron: already scoped. The official agent flow (declare
`uses`, owner approves) is untouched — only the unofficial escalation dies.

## Implementation sketch (≈ a focused day)

1. `internal/auth`: terminals map + `MintTerminal/RevokeTerminal/lookup`;
   `FromRequest` bearer chain + noAuth branch. (~40 LOC + tests)
2. `internal/term`: `Tokens` field; mint in `create()`, `XBIN_TOKEN` into the
   per-session env (sandbox + host-shell paths — the same HOME-filter seam),
   revoke in pump cleanup; `CanUseTile`/root-`IsAdmin` gates in `ServeWS`.
   (~60 LOC + tests)
3. `cmd/xbind`: wire `tm.Tokens = a`; drop `XBIN_TOKEN` from the shared Env
   closure. (~10 LOC)
4. `templaterepo.go`: serve gate → any authenticated. (~5 LOC)
5. `bx`: friendlier 403 ("this terminal is scoped to apps/foo — admin ops need
   a root terminal"). (optional)
6. Docs: auth.md (token table + new class), protocol.md auth table,
   isolation.md, getting-started env table, workspace AGENTS.md (the
   "`XBIN_TOKEN` = owner, full access" claims at :45/:319/:518 and the "you
   are the owner principal" framing), DECISIONS entry, changelog + a
   **breaking** migration note.

## Open decisions

- **Escape hatch?** Recommend **none** (scoped-by-default). A
  `XBIN_LEGACY_TERMINAL_TOKENS=1` env could ease transition, but it re-opens
  the self-approval hole and would linger.
- Root terminal = admin-only (recommended; matches the existing 403 text) vs
  keeping Terminal-flag access to it.
- Live-user root tokens (recommended; demotion applies immediately) vs
  handing the raw owner token to admin root terminals.

## Testing

- auth: bearer resolution per class; terminal token → element principal not
  admin; root terminal token tracks live user role; revoke kills it.
- term: mint-on-create/revoke-on-exit; gates (non-admin × root terminal 403,
  disallowed tile 403); env carries the terminal token, not the owner's.
- integration: multiuser test extends naturally (alice already 403s on
  /ws/term; add: bob's tile terminal token can't POST /grants).
