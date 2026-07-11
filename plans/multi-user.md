# Multi-user auth & RBAC — design

Today xbin is single-user: one root token = admin-of-everything. This adds
**human users** with tile-level permissions, keeps the token as a root/admin
service credential, gates terminals (root access) behind an explicit
permission, exposes user-management as capability-scoped APIs, and locks down
every public surface so a xbin port exposed to a network reveals nothing
without a credential.

Extended by `plans/orgs.md` (organizations & teams: grouping, per-tile team
access, policy ceilings, delegated org admins — D19–D21).

Non-goal for this pass (explicit follow-up): `X-Source-Tile` / `X-Source-User`
attribution headers on cross-tile and UI→tile calls (its own task).

## Principals

| Principal | Auth | Privilege |
|---|---|---|
| **root token** (`XBIN_TOKEN`) | `Authorization: Bearer` / bootstrap cookie | admin. The machine/bootstrap credential — bx, xbind-spawned terminals, automation. Always valid. |
| **admin user** | username + password → session cookie | admin (all tiles, terminals, user mgmt) |
| **regular user** | username + password → session cookie | only permitted tiles; no terminal/admin unless granted |
| **element backend** | per-generation instance token (gateway) | unchanged |
| **element frontend** | owner/user cookie **+ frame token** bound to (user × tile) | acts as the tile, on behalf of a user |

`Admin` (the privilege) = root token **or** a user whose role is `admin`.
Every place that was gated "owner only" becomes "admin" and now accepts admin
users, not just the root token.

The root token is deliberately kept: it's how `bx`, terminals, and automation
authenticate, and it's the break-glass admin when no user exists yet. Human
users are layered on top; the first admin user is created by the root
principal (admin tile → Users, or `bx user add`).

## The user model

Stored in `data/users.json` (xbind-owned, mode 0600; passwords Argon2id —
same primitive as the vault barrier):

```jsonc
{ "users": [
  { "id": "alice", "name": "Alice", "role": "user",
    "tiles": ["apps/chat", "apps/calendar", "lib/*"],  // allowed paths/prefixes
    "terminal": false,                                  // may open tile shells
    "passHash": "argon2id$…", "created": 1782… }
]}
```

- `role`: `admin` | `user`. Admins ignore `tiles` (all) and default
  `terminal: true`.
- `tiles`: allow-list of component paths or `prefix/*` (a scope or subtree).
  `"*"` = all. Empty = none. Ignored for admins.
- `terminal`: may open the per-tile terminal (= **root shell** in that tile's
  dir). Because that's root, it defaults to admins only and is a loud,
  explicit toggle for anyone else.

Backward compatible: **no users configured ⇒ exactly today's behavior** —
the root token is admin and there's nothing to log into beyond the token URL.
Multi-user turns on the moment the first user exists.

## RBAC: tile-level access

A user may **open and drive** only tiles on their allow-list; admins, all.
Enforcement points (defense in depth — deny at every reachable door):

1. `GET /c/<tile>/…` — serving the view. Not allowed ⇒ 403, and no frame
   token is minted, so the user can't obtain a credential for it.
2. `GET /api/xbin/frame-token?component=X` — refuse X the user can't use.
3. `ANY /api/<tile>/…` — the frontend's API calls; re-checked on the
   principal even though (1)+(2) already gate token issuance.
4. `GET /api/xbin/components` — filtered to the caller's visible tiles
   (admins see all); the shell sidebar shows only what a user may use.

Frame tokens now bind **(user, component)**. A tile's own cross-tile calls
(`apps/chat` → `apps/llm-gw`) remain governed by element **grants**, not the
user's allow-list: allowing a user to use a tile lets them use that tile's
own granted capabilities, exactly like app permissions on a phone. Users
never get a frame token for a tile they can't open, so they can't forge
cross-tile origins.

Terminals are separate: `GET /ws/term` requires the **terminal** permission
(admins, or a user explicitly flagged). It is root access to the tile's
directory; the UI labels it as such.

## User management — capability-scoped APIs

User CRUD is exposed as xbind APIs so a dedicated user-management tile can be
built, gated by a **distinct capability** `xbin:users` (separate from
`xbin:admin`; admin implies both):

```
GET    /api/xbin/users                list (no hashes)
POST   /api/xbin/users                create {id,name,role,tiles,terminal,password}
PATCH  /api/xbin/users/<id>           update fields (incl. password reset)
DELETE /api/xbin/users/<id>           remove
GET    /api/xbin/whoami               the caller's own identity/permissions
```

Gate: root token, an admin user, or an element granted `xbin:users`. This
lets the owner grant a purpose-built user-admin tile just `xbin:users`
without full system admin — the "separate scopes" the design calls for.

The **admin tile** gains a **Users** tab that is a complete manager: list,
create (with a generated or set password), edit role/tiles/terminal, reset
password, delete. It uses these same APIs, so it's not privileged over what a
third-party user-admin tile could do.

## Public-surface lockdown

Threat: a xbin instance reachable on a network must expose nothing sensitive
without a credential. Audit of every route:

| Route | Auth |
|---|---|
| `GET /healthz` | none — returns `ok`, no data. (liveness) |
| `GET /login`, `POST /login`, `POST /logout` | none — the auth entrypoint itself |
| everything else (`/`, `/c/`, `/api/`, `/vendor/`, `/docs/`, `/ws/*`) | valid principal required (already behind `authed`) |

Hardening added here:

- **Login throttle**: per-IP failed-attempt backoff to blunt password
  brute-force; generic "invalid credentials" (never reveal whether a user
  exists).
- **Cookies**: `HttpOnly`, `SameSite=Lax`, `Secure` behind an https proxy.
  Sessions are server-side (random id → user), so logout/delete revokes
  immediately; a deleted/edited user's sessions are invalidated.
- **No plaintext token in URLs after bootstrap**: the `?token=` login sets the
  cookie and redirects; the token isn't needed in the browser again.
- **Dev bypass is unmistakable**: `--dev`/`--no-auth` logs a warning and must
  never be the default; it grants admin. Deployments never set it.
- Confirm no endpoint returns secrets to a non-admin (vault values, tokens,
  grant table, backend logs/status are all admin-gated).

## Session mechanism

In-memory session store: `sessionID (random 32B) → userID`. Cookie
`xbin_session` holds either the **root token** (bootstrap admin, as today) or
a **session id**. `FromRequest` resolves: bearer root/instance token →
admin/element; cookie == root token → admin; cookie == known session →
that user (+ frame token ⇒ user-attributed tile frontend). Sessions drop on
restart (users re-login) — same precedent as terminal sessions; persistence
can come later.

## Build order

1. `internal/users` store (CRUD, Argon2id) + sessions in `internal/auth`.
2. Login page + `POST /login`/`/logout` + throttle.
3. `Principal` gains `UserID`/user + `IsAdmin()`; unify all `Owner`-admin
   checks to `IsAdmin`; frame tokens bind user+component.
4. Tile-access enforcement (`/c/`, `/api/<tile>`, `/frame-token`,
   `/components`) + terminal gating.
5. `/api/xbin/users*` + `whoami`, gated by `xbin:users` capability;
   `bx user` CLI.
6. Admin tile **Users** tab.
7. Lockdown audit + docs (docs/auth.md, protocol.md, AGENTS.md, deployment).

## Deferred (next task): source attribution headers

On cross-tile and UI→tile requests, inject verified `X-Source-Tile` and
`X-Source-User` (the human driving the call, distinct from `X-XBin-From`
which is the calling *tile*). Enables per-user auditing, user-scoped data in
shared tiles, and "who asked" features. Designed to ride the same verified
frame/instance-token machinery.
