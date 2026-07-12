# Identity: principals & tokens

Every request that touches xbind is resolved to a **principal** — a verified
answer to "who is calling" — before any decision is made about what it may
do. This page is the taxonomy: the seven principal kinds, the token
machinery behind each, and the one rule that holds the whole system together
(strip inbound identity, inject verified identity). Authorization — what a
principal may *do* — is the next page.

**Related:** [06-authorization.md](06-authorization.md) ·
[07-users-orgs.md](07-users-orgs.md) · [09-terminals.md](09-terminals.md) ·
[13-ingress.md](13-ingress.md) · [/docs/auth.md](/docs/auth.md) ·
[/docs/protocol.md](/docs/protocol.md) · plans/auth.md ·
plans/terminal-tokens.md · plans/multi-user.md

## Why identity is xbind's job

xbin's security model has one load-bearing simplification: **callees never
authenticate anything**. All traffic — browser, CLI, element→element, cron,
public — flows through xbind (the TCP listener or the gateway unix socket,
same handler), which resolves the caller, strips any identity the caller
*claimed*, and injects the identity it *verified*:

```
X-XBin-From: owner | user:<id> | <component-path> | xbin/cron | ingress
X-XBin-Role: <role granted on the callee>
X-XBin-Ingress-Host: <public hostname>     (ingress traffic only)
```

If a backend sees these headers, they are true (plans/auth.md §3). The SDK's
`xbin.Caller(r)` is a header read, nothing more. This is what makes the rest
of the system composable: every enforcement point downstream — role guards,
grants, policy ceilings — consumes the same two verified facts.

## The taxonomy

| Principal | Authenticates by | `X-XBin-From` | Lifetime |
|---|---|---|---|
| **Owner** | root token: `Authorization: Bearer`, or as a login cookie | `owner` | until rotated |
| **Human user** | username/password → server-side session cookie | `user:<id>` | 12 h idle / 30 d absolute |
| **Element backend** | per-generation instance token over the gateway socket | `apps/email` | dies with the process generation |
| **Element frontend** | owner/user cookie **+ HMAC frame token** | `apps/email` | 15 min, auto-refreshed |
| **Terminal shell** | per-session tile-scoped token (`$XBIN_TOKEN`) | `apps/email` — the tile, never the human | dies with the session |
| **Scheduler** | internal | `xbin/cron` | per tick |
| **Public visitor** | none — structural (published endpoints only) | `ingress` | per request |

Three of these are *element* principals (backend, frontend, terminal): three
surfaces of the same identity — the component path — with the same rights.
A grant covers a component's backend, its frontend, and its terminal
equally; *where* a call originates never matters, *which element* is calling
does.

## Owner: the root token

First boot generates a random token at `.xbin/token` (0600); xbind prints a
one-time `…/login?token=…` URL. The same token authenticates two ways:

- **Bearer** — `bx`, curl, host-side automation. This is the intended home
  of owner-privileged automation: the file lives on the host, outside every
  sandbox.
- **Cookie** — opening the login URL sets the token as an HttpOnly,
  SameSite=Lax session cookie (Secure behind an https proxy, detected via
  `X-Forwarded-Proto`).

The owner passes every authorization check as role `admin` — the human is
root on the runtime plane. Two controls keep the token governable:

- **Rotation** (`POST /api/xbin/auth-rotate-token`, or the admin console):
  writes a fresh `.xbin/token` atomically and swaps it live; the old token
  stops authenticating immediately, bearer and cookie alike. Rotate once
  after upgrading past 2026-07-09 — before terminal-scoped tokens, every
  shell carried the owner token, so old agent transcripts may hold it.
- **Token-login disable** (`PATCH /api/xbin/auth-settings`
  `{tokenLoginDisabled: true}`): once real admin *users* exist, the
  bootstrap token's **browser** login can be turned off — the `?token=` URL
  is refused and an owner-token cookie no longer authenticates (a leaked
  token can't be pasted into a cookie). The **Bearer** form deliberately
  keeps working so `bx` and host automation don't break; revoking that too
  is what rotation is for. Lockout-guarded: flipping it requires an existing
  admin user and a signed-in admin-*user* caller.

## Human users: server-side sessions

Users (plans/multi-user.md, [07-users-orgs.md](07-users-orgs.md)) log in
with username/password (Argon2id-hashed in `data/users.json`). A successful
login mints a **server-side session**; the cookie carries only a random id,
so the server stays authoritative:

- **Idle TTL** 12 h, sliding — each authenticated request renews it.
- **Absolute TTL** 30 d since login, regardless of activity — a stolen
  cookie can't authenticate forever. Both are env-tunable
  (`XBIN_SESSION_IDLE_TTL` / `XBIN_SESSION_MAX_TTL`); the cookie's 30-day
  `MaxAge` is only a browser-side hint.
- Sessions live in memory: logout, **user deletion**, and a daemon restart
  all end them immediately. Deletion goes further — frame and terminal
  tokens *naming* a deleted user are rejected at resolution, so their live
  shells and open tiles lose API access in the same instant.

Each request that resolves a session also snapshots the user and their
org/team-aware `Access` resolution, so per-tile permission checks
throughout the request see one consistent view.

## Element backends: instance tokens

xbind spawns every backend, so it mints identity at spawn: a random
**per-generation instance token**, handed to the process as `XBIN_TOKEN`
alongside `XBIN_GATEWAY` (the unix socket where the whole API is served
under the same auth). The SDK's `xbin.Client()` attaches it automatically.

The token is registered when the generation starts and revoked when its
process exits — after a blue/green swap the old binary drains (30 s, D8)
and then **its credential dies with it**. A stale process can't act after
replacement. Identity *is* the component path: there is no separate id
space to keep in sync with the filesystem.

## Element frontends: frame tokens

Everything is same-origin in the browser, so without extra machinery any
element's JS could ride the human's cookie into any other element's API.
The fix (ND2): the cookie proves the **human**, and a **frame token**
attributes the request to the **element** whose document made it.

- Minted at the single sanctioned HTML-injection point (D4) into every
  served component page as a `<meta>` tag: HMAC-SHA256 over
  `(component, user, expiry)`, 15-minute TTL, refreshed every 10 minutes by
  `xbin-client.js` via `GET /api/xbin/frame-token`. `xbin.fetch()` attaches
  it; a raw `fetch` to a sibling element 403s.
- **Present-but-invalid is rejected, never downgraded.** A request carrying
  a bad or expired frame token does not fall back to the cookie principal —
  otherwise an element could shed its attribution by corrupting its own
  token and act as the human.
- **Cross-user replay is rejected**: the token's embedded user must match
  the session that presents it, so one user's captured frame token is
  useless with another's cookie.
- A frame token naming a **deleted user** is rejected, and WebSockets pass
  the token as a `?frame=` query parameter (headers are impossible there) —
  consumed by xbind and never forwarded to the callee, where it could be
  replayed.

This is **attribution, not isolation**: element frontends share one origin,
so a hostile element's JS still executes in the same browser context. The
grant system is the seatbelt and audit trail; the outer boundary is the
host (see the honesty section of [/docs/auth.md](/docs/auth.md)).

## Terminals: tile-scoped tokens

A terminal opened on a tile gets a per-session token whose principal is
**the tile's element identity — never the human driving it**
(plans/terminal-tokens.md). The shell (and any agent in it) holds
min(user, tile): self-admin on its own tile, its approved grants and
bindings, and nothing else. `IsAdmin()` is false inside a tile terminal
*even for admin users* — admin work happens in the browser or with the
host-side owner token. The user id rides along only for attribution,
prefs bucketing, and the session-binding check; the root
(whole-workspace) terminal is disabled entirely.

Why this exists: terminals used to carry the owner token, which let any
agent in any shell self-approve its own grants — the exact escalation the
model forbids. Terminal tokens closed it (2026-07-09), and the sandbox
mount masks + Landlock read guard ([09-terminals.md](09-terminals.md))
closed the "just `cat .xbin/token`" bypass behind it. Tokens die with the
session, and deleting a user revokes theirs instantly. A per-session
**no-API** toggle withholds the token entirely — a code-only shell.

## Scheduler: cron ticks

Cron jobs are registered by an element **against its own endpoints only** —
cron can never be aimed at a third element. A tick arrives as
`From: xbin/cron` carrying the **role chosen at registration** (default
`writer`), bounded by being self-inflicted: the element decides how much
of its own authority its schedule wields.

## The public: the ingress principal

Anonymous traffic through a **published endpoint**
([13-ingress.md](13-ingress.md)) arrives as `From: ingress` — a principal
that is **structural, not a credential** (ING-5). It never passes the
authenticated routes at all: it enters on the separate ingress listeners,
is routed to exactly the one tile whose owner-approved binding published
the hostname, and is confined to the paths that tile's manifest declared
public. All inbound `X-XBin-*` headers are stripped wholesale, the
workspace session cookie is removed before the request reaches the
backend, and the public hostname rides in `X-XBin-Ingress-Host`. The SDK's
`Caller(r).Ingress()` is how a backend branches "public visitor" vs
"workspace caller".

## The identity spine

The mechanics that make the header contract trustworthy:

- **Strip, then inject.** The element proxy deletes the identity headers
  (`X-XBin-From`, `X-XBin-Role`, the frame-token header) from every inbound
  request before injecting verified values; the ingress path strips the
  entire `X-XBin-*` namespace. A caller — or a compromised terminator tile —
  cannot smuggle an identity through.
- **One handler, two doors.** The gateway unix socket serves the *same*
  API under the *same* authentication as the TCP listener; backends simply
  reach it from inside their sandbox with their instance token. There is no
  side channel with weaker rules.
- **Reserved names.** From-identities that aren't component paths can't be
  spoofed by *creating* a component with that name: `xbin` is a reserved
  top-level (so `xbin/cron` is unclaimable), and `ingress` and `runtime`
  were reserved with the ingress work. `owner` is additionally reserved as
  a **user id** (it is the bootstrap principal's home under `homes/`).
- **Bearer resolution order** is fixed: owner token → instance token →
  terminal token → reject. Cookies resolve to the owner (bootstrap) or a
  session; a frame token on top narrows the principal to that element.

## The capability-leak principle

The single most consequential rule in the identity layer: **an element
principal never inherits the privilege of the human driving it.** A tile an
admin merely *opens* does not become admin — the frame token's principal is
the tile, and the tile's authority comes from the tile's own grants. Same
for terminals: the shell acts as the tile, whoever opened it.

The inverse edge is the one to internalize: granting a user access to a
*privileged tile* grants them that tile's capabilities (like installing an
app with broad permissions). The admin tile is only powerful because it
holds `xbin:admin` — so don't put privileged tiles on non-admin users'
access lists. Consequences of the principle elsewhere in the system:

- **Org admins operate from workspace chrome, not the admin tile** (D21):
  giving a non-workspace-admin read on `tiles/admin` would mint them its
  frame token and thereby the *tile's* `xbin` capabilities. The shell's ⚙
  panels use raw `fetch` — the human cookie principal — precisely so the
  human's own (org-clamped) authority is what acts.
- **Attributed humans clamp capable tiles** the other way too: a
  manager-style tile holding `xbin:writer` cannot be driven by a user to
  create tiles the *user* couldn't create themselves — the confused-deputy
  checks in [06-authorization.md](06-authorization.md).
- `whoami` on element principals reports the driving human **scoped by the
  tile's trust** ([07-users-orgs.md](07-users-orgs.md)) — a low-trust tile
  can't harvest org membership through its visitor.

## Login mechanics & dev mode

- `/healthz` and `/login`/`/logout` are the only unauthenticated routes.
  Everything else requires a principal; unauthenticated browser navigations
  redirect to the login page, API calls get 401.
- Password logins are throttled per client IP (5 failures → 30 s cooldown;
  success clears it) and fail with a generic "invalid credentials" that
  never reveals whether the username exists.
- **`--no-auth` (dev) disables owner auth but keeps element identity
  live**: instance, terminal, and frame tokens still resolve to element
  principals, so dev mode exercises exactly the RBAC production enforces —
  a grant bug shows up on `make dev`, not after deploy. Cookie-less
  requests simply become the owner.

The full endpoint/table reference is [/docs/auth.md](/docs/auth.md) and
[/docs/protocol.md](/docs/protocol.md); the design rationale is
plans/auth.md (§1–2, §6) and plans/terminal-tokens.md.
