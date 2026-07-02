# Auth: identities, roles, grants, vault

buxon has two planes with different rules:

- **Editing plane** — terminals, `bx`, git. Owner-privileged, unrestricted.
  It's your workspace; buxon does not protect you from yourself.
- **Runtime plane** — running elements (backends and their frontends).
  Default-deny: an element can call exactly its own API plus whatever it was
  granted. This page is about that plane.

## Who is calling? (principals)

| Principal | How it authenticates | Typical `X-Buxon-From` |
|---|---|---|
| Owner (you) | login cookie, or `Authorization: Bearer $BUXON_TOKEN` | `owner` |
| Element backend | per-generation instance token over the gateway socket (`BUXON_GATEWAY` + `BUXON_TOKEN` env — the SDK's `buxon.Client()` handles it) | `apps/email` |
| Element frontend | owner cookie **+ frame token** (`buxon.fetch` attaches it) | `apps/email` |
| Scheduler | internal | `buxon/cron` |

Callees never verify any of this themselves: buxond strips inbound
`X-Buxon-*` headers and injects the verified `X-Buxon-From` and
`X-Buxon-Role`. If those headers are present, they're true.

**Frame tokens** are why *which element's page* made a browser request
matters and can't be forged by another element's JS: the cookie proves the
human, the injected short-lived token attributes the request to the
component whose document it is. Consequence for your frontend code: **use
`buxon.fetch()` for anything beyond your own API.** A raw `fetch` to another
element 403s.

## Roles and grants

The callee declares roles (manifest `expose.roles`, descriptions mandatory);
the caller requests them (`uses`); the owner approves cross-scope requests
once; buxond enforces at every call.

```jsonc
// callee: apps/calendar/buxon.json
{ "expose": { "roles": {
    "reader": "Read events and calendars",
    "writer": "Create and modify events" } } }

// caller: apps/email/buxon.json
{ "uses": [ { "target": "apps/calendar", "role": "reader" } ] }
```

- **Same scope → auto-approved.** A scope is one app; its parts trust each
  other. The `uses` entry itself is the grant.
- **Cross-scope → pending** until approved in the root page's grants panel
  or `bx grant apps/email apps/calendar:reader`. Grants live in the
  workspace `buxon.json` — a visible, git-diffable capability table. Revoke:
  panel, or `bx grant --revoke …`. Approving a grant reloads the affected
  caller's frame automatically, so a frontend that was 403'ing retries
  against the new permission without a manual refresh.
- **Automated agents should not self-approve cross-scope grants.** Approval
  is the owner's human-in-the-loop decision; an agent's terminal runs as
  owner, so running `bx grant` on the agent's own behalf bypasses exactly
  the review the model is for. Agents declare `uses` and leave cross-scope
  (and `buxon:*`) grants pending for the owner — only approving when the
  owner explicitly asks. (AGENTS.md restates this for in-workspace agents.)
- **Role names**: `reader` / `writer` / `admin` are the convention and imply
  each other downward (`admin ⊃ writer ⊃ reader`). Custom names are
  exact-match unless the callee's manifest declares `implies`. On a `bus`
  resource, `subscriber`/`publisher` are aliases for reader/writer.
- The **owner passes every check as `admin`** — your curl and the root UI
  are never blocked.

Enforcing in the callee is one middleware:

```go
mux.HandleFunc("GET /events", list)                       // any granted role
mux.Handle("POST /events", buxon.RoleFunc("writer", add)) // writer or better
who := buxon.Caller(r)  // {From: "apps/email", Role: "reader", Owner: false}
```

Design guidance: prefer **service APIs over shared state**. "Email reads the
calendar" should be a `reader` grant on `apps/calendar`'s API — not a shared
database file. The owning app keeps its schema private and can change it.

## Vault: per-element secrets

Each element has a private key-value vault for third-party credentials
(IMAP passwords, API keys). Secrets stop living in source or env files.

```sh
bx vault set apps/email imap-pass          # reads value from stdin
bx vault ls  apps/email
```

```go
pass, err := buxon.Secret("imap-pass")     // own vault only
```

Rules, all deliberate:

- An element reads **only its own vault**. There are no cross-element vault
  grants. If two apps need the same secret, either store it twice or — the
  right pattern — the owning app exposes a role-guarded API that *uses* the
  secret without revealing it.
- Secrets are fetched at runtime, never injected into env (env leaks via
  `/proc`, logs, and child processes).
- Storage: `data/vault/`, buxond-owned, mode 0600, gitignored always,
  excluded from `bx backup` unless `--with-vault`.
- The owner can read/write any vault via `bx vault` — the human is root.

## Workspace-management capabilities (the `buxon` target)

Some actions manage the workspace itself rather than another element. They're
granted as roles on the reserved target `buxon`:

| grant | lets an element… |
|-------|------------------|
| `buxon:writer` | create components (`POST /api/buxon/create`) — held by `tiles/manager` |
| `buxon:admin`  | administer everything: read/write **any** vault, view/approve/revoke **any** grant, manage all cron jobs, read system state — held by `tiles/admin` |

`admin ⊃ writer`. The owner always has both implicitly. These make
otherwise owner-only endpoints reachable by a trusted tile.

**`buxon:admin` is the heaviest grant in the system.** It can read every
secret and rewrite the grant table — granting it to an element is trusting
that element as yourself for administration. It ships pre-granted only to
`tiles/admin`; treat any new grant of it like handing out a root shell.
Revoke it (`bx grant --revoke tiles/admin buxon:admin`) and the admin tile
goes dark. Unprivileged elements gain nothing: every management endpoint
still denies principals without the grant.

## Honesty: enforcement tiers

The *model* above is always enforced by buxond. How hard it is to cheat
depends on the tier:

| Tier | When | What a hostile element could still do |
|---|---|---|
| 1 (default) | all backends run as one uid | read a sibling's env via `/proc` (steal its instance token), open sibling sockets directly, write any workspace file |
| 2 (`--scope-uids`, container runs privileged as root) | each scope's backends get their own uid | abuse only what it was granted. Also: **elements can't write source, even their own** — editing is terminal-only; vault/data enforced by file perms |
| 3 (roadmap) | wasm runtime, netns egress, subdomain isolation | approximately nothing ungranted |

Browser side, all elements are same-origin: frame tokens give
**attribution** (RBAC works), not **isolation** (a malicious element's JS
runs in the same origin). Origin isolation per scope is roadmap. The real
boundary today is the container — treat the workspace as one trust domain
against determined-malicious code, and the grant system as seatbelts and
audit trail, not a jail.

## Owner login mechanics

- First boot generates a token (`.buxon/token`); buxond prints
  `…/login?token=…` — opening it sets an HttpOnly SameSite=Lax cookie.
- CLI/scripts use `Authorization: Bearer` (terminals have `$BUXON_TOKEN`).
- Behind an https proxy the cookie turns `Secure` automatically
  (`X-Forwarded-Proto`). buxond itself never does TLS; put Tailscale or
  Caddy in front.
- `--dev` / `--no-auth` disables owner auth (never expose such an instance),
  but element identity and grants still apply — dev and prod run the same
  RBAC.
