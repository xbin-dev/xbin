# Auth: identities, roles, grants, vault

xbin has two planes with different rules:

- **Editing plane** — terminals, `bx`, git. Owner-privileged, unrestricted.
  It's your workspace; xbin does not protect you from yourself.
- **Runtime plane** — running elements (backends and their frontends).
  Default-deny: an element can call exactly its own API plus whatever it was
  granted. This page is about that plane.

## Who is calling? (principals)

| Principal | How it authenticates | Typical `X-XBin-From` |
|---|---|---|
| Owner (you) | login cookie, or `Authorization: Bearer $XBIN_TOKEN` | `owner` |
| Element backend | per-generation instance token over the gateway socket (`XBIN_GATEWAY` + `XBIN_TOKEN` env — the SDK's `xbin.Client()` handles it) | `apps/email` |
| Element frontend | owner cookie **+ frame token** (`xbin.fetch` attaches it) | `apps/email` |
| Scheduler | internal | `xbin/cron` |

Callees never verify any of this themselves: xbind strips inbound
`X-XBin-*` headers and injects the verified `X-XBin-From` and
`X-XBin-Role`. If those headers are present, they're true.

**Frame tokens** are why *which element's page* made a browser request
matters and can't be forged by another element's JS: the cookie proves the
human, the injected short-lived token attributes the request to the
component whose document it is. Consequence for your frontend code: **use
`xbin.fetch()` for anything beyond your own API.** A raw `fetch` to another
element 403s.

## Roles and grants

The callee declares roles (manifest `expose.roles`, descriptions mandatory);
the caller requests them (`uses`); the owner approves cross-scope requests
once; xbind enforces at every call.

```jsonc
// callee: apps/calendar/xbin.json
{ "expose": { "roles": {
    "reader": "Read events and calendars",
    "writer": "Create and modify events" } } }

// caller: apps/email/xbin.json
{ "uses": [ { "target": "apps/calendar", "role": "reader" } ] }
```

- **Same scope → auto-approved.** A scope is one app; its parts trust each
  other. The `uses` entry itself is the grant.
- **Cross-scope → pending** until approved in the root page's grants panel
  or `bx grant apps/email apps/calendar:reader`. Grants live in the
  workspace `xbin.json` — a visible, git-diffable capability table. Revoke:
  panel, or `bx grant --revoke …`. Approving a grant reloads the affected
  caller's frame automatically, so a frontend that was 403'ing retries
  against the new permission without a manual refresh.
- **Automated agents should not self-approve cross-scope grants.** Approval
  is the owner's human-in-the-loop decision; an agent's terminal runs as
  owner, so running `bx grant` on the agent's own behalf bypasses exactly
  the review the model is for. Agents declare `uses` and leave cross-scope
  (and `xbin:*`) grants pending for the owner — only approving when the
  owner explicitly asks. (AGENTS.md restates this for in-workspace agents.)
- **Role names**: `reader` / `writer` / `admin` are the convention and imply
  each other downward (`admin ⊃ writer ⊃ reader`). Custom names are
  exact-match unless the callee's manifest declares `implies`. On a `bus`
  resource, `subscriber`/`publisher` are aliases for reader/writer.
- The **owner passes every check as `admin`** — your curl and the root UI
  are never blocked.
- **Sandbox capability targets** (under `--isolate`): besides components and
  `res:*`, `uses` can request `net:*` egress (`plans/isolation.md`) and `gpu:*`
  GPUs — `gpu:all` / `gpu:<index>` / `gpu:<uuid>` (`plans/gpu.md`). Same
  owner-approval flow; ungranted means the sandbox gets no egress / no GPU.

Enforcing in the callee is one middleware:

```go
mux.HandleFunc("GET /events", list)                       // any granted role
mux.Handle("POST /events", xbin.RoleFunc("writer", add)) // writer or better
who := xbin.Caller(r)  // {From: "apps/email", Role: "reader", Owner: false}
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
pass, err := xbin.Secret("imap-pass")     // own vault only
```

Rules, all deliberate:

- An element reads **only its own vault**. There are no cross-element vault
  grants. If two apps need the same secret, either store it twice or — the
  right pattern — the owning app exposes a role-guarded API that *uses* the
  secret without revealing it.
- Secrets are fetched at runtime, never injected into env (env leaks via
  `/proc`, logs, and child processes).
- Storage: `data/vault/`, xbind-owned, mode 0600, gitignored always,
  excluded from `bx backup` unless `--with-vault`.
- The owner (and `xbin:admin` tiles) can read/write any vault via
  `bx vault` / the admin console — the human is root.

### Encryption at rest (the barrier)

Secrets are encrypted on disk with an AES-256-GCM barrier whose key never
lives in the at-rest data — the property that makes HashiCorp Vault's
at-rest story meaningful. The key hierarchy:

```
passphrase --Argon2id(salt)--> KEK  (in memory only)
KEK --AES-256-GCM--> wraps -->  DEK  (random data key)
DEK --AES-256-GCM--> encrypts each vault file (key names included)
```

Only the **wrapped** DEK and the KDF salt sit on disk (`data/vault/.barrier.json`);
without the passphrase they yield nothing. So a stolen workspace dir, backup,
or disk snapshot is just ciphertext.

**Seal / unseal**, like Vault:

- Sealed → the DEK isn't in memory; vault reads/writes return `503 sealed`.
- Unseal supplies the passphrase, which is never persisted; the DEK is held
  in memory (mlock'd best-effort) until seal or restart.
- The barrier also encrypts **resource data** (kv/filesystem/sqlite/blob) at
  rest, not just secrets ([resources.md](/docs/resources.md),
  `plans/vault-data.md`). So while sealed, components that use those resources
  are **held** (won't spawn) and come alive on unseal — sealing a configured
  vault takes the stateful workspace offline until an admin unseals.

```sh
bx vault status                 # insecure | sealed | unsealed
bx vault unseal                 # prompts (no echo); creates the barrier on
                                # first use and encrypts existing plaintext
bx vault seal                   # drop the key from memory
```

Boot modes (production never persists secrets in the clear — the broker
refuses plaintext writes unless explicitly allowed):

- `XBIN_VAULT_PASSPHRASE=…` → **auto-unseal** at startup (convenient; the
  passphrase lives in the process env, readable by root — the weaker mode,
  same tradeoff as Vault's env unseal).
- Unset, barrier already set up → boots **sealed**; an admin unseals after
  login via `bx vault unseal` / `POST /api/xbin/vault-unseal` (**strongest**:
  the passphrase never touches the process env or the data dir). Re-unseal
  after every restart, like Vault.
- Unset, no barrier yet → boots **locked** (unconfigured): the daemon runs
  and you can reach the UI, but secret storage is refused (503) until an admin
  runs `bx vault unseal` once — which **creates** the barrier with the
  passphrase they type and encrypts any existing plaintext. Also the
  passphrase-never-in-env mode.
- `--dev` / `--no-auth` / `--insecure-vault` → **plaintext at rest** permitted
  (for local/dev; a loud warning). Only these modes ever write plaintext.

So: set the env for hands-off convenience, or leave it unset and unseal
manually after login for the stronger posture where the passphrase is never
stored anywhere. `bx vault status` reports which mode you're in
(unsealed / sealed / plaintext / unconfigured).

**Honest limits.** Go's GC gives no guaranteed memory zeroing, so while
unsealed the DEK/plaintext may be copied on the heap or appear in a core
dump — the same fundamental constraint Vault (also Go) has. The barrier
defends **data at rest**; it does not defend against a root-compromised
host while unsealed. There is no Shamir key splitting, transit engine,
dynamic secrets, or leasing — this is at-rest encryption with seal/unseal,
not full Vault parity. Losing the passphrase means losing the secrets: the
DEK is unrecoverable without it. `bx vault status` on a running workspace
tells you which mode you're in.

## Workspace-management capabilities (the `xbin` target)

Some actions manage the workspace itself rather than another element. They're
granted as roles on the reserved target `xbin`:

| grant | lets an element… |
|-------|------------------|
| `xbin:writer` | create components (`POST /api/xbin/create`) — held by `tiles/manager` |
| `xbin:admin`  | administer everything: read/write **any** vault, view/approve/revoke **any** grant, manage all cron jobs, read system state — held by `tiles/admin` |

`admin ⊃ writer`. The owner always has both implicitly. These make
otherwise owner-only endpoints reachable by a trusted tile.

**`xbin:admin` is the heaviest grant in the system.** It can read every
secret and rewrite the grant table — granting it to an element is trusting
that element as yourself for administration. It ships pre-granted only to
`tiles/admin`; treat any new grant of it like handing out a root shell.
Revoke it (`bx grant --revoke tiles/admin xbin:admin`) and the admin tile
goes dark. Unprivileged elements gain nothing: every management endpoint
still denies principals without the grant.

## Honesty: enforcement tiers

The *model* above is always enforced by xbind. How hard it is to cheat
depends on the tier:

| Tier | When | What a hostile element could still do |
|---|---|---|
| 1 (no `--isolate`; dev/local) | all backends run as one uid | read a sibling's env via `/proc` (steal its instance token), open sibling sockets directly, write any workspace file |
| 2 (`--scope-uids`, xbind runs as root) | each scope's backends get their own uid | abuse only what it was granted. Also: **elements can't write source, even their own** — editing is terminal-only; vault/data enforced by file perms |
| 3 (`--isolate`, rootless — production) | each backend in its own user+mount+pid+ipc+uts+net namespaces over an overlay rootfs; **default-deny egress** via the `net:*` relay; cgroup accounting | almost nothing at the OS layer: no sibling `/proc` or env, no sibling sockets, only granted files are mounted, and no network beyond its `net:*` grants |

Tier 3 is the OS-level sandbox (`plans/isolation.md`, `plans/runtime.md`) and the
production model: rootless (unprivileged user namespaces), run on a VM/host
xbind controls (README → Running it). Still roadmap: a default **seccomp**
profile, enforced **cgroup resource limits** (today it's accounting only),
`wasm`/wazero backends, and per-scope **origin** isolation.

Browser side, all elements are same-origin: frame tokens give **attribution**
(RBAC works), not **isolation** (a malicious element's JS runs in the same
origin). So the outer boundary is the VM/host; treat same-origin element
*frontends* as one trust domain, and the grant system as seatbelts and audit
trail, not a jail.

## Multi-user (users, roles, tile access)

xbin can have **human users** on top of the root token (plans/multi-user.md).

- **Root token** (`XBIN_TOKEN`) — the admin/bootstrap service credential.
  Full admin; used by `bx`, terminals, automation. Always valid.
- **Admin user** — logs in with username+password; full access (all tiles,
  terminals, user management).
- **Regular user** — logs in; may open/drive only the tiles on their
  allow-list; no terminal or admin unless explicitly granted.

No users configured ⇒ single-user mode (the root token is the only
principal), exactly as before. The first user is created by an admin.

**Tile-level RBAC.** A user's `tiles` is an allow-list of component paths or
`prefix/*` (a scope/subtree; `*` = all). They can load `/c/<tile>/` and get a
frame token only for allowed tiles; the shell sidebar shows only those; every
door (view, frame-token mint) enforces it. A tile a user is allowed to use
runs with its *own* grants — allowing a user a tile lets them use that tile's
capabilities, like app permissions on a phone. A tile never inherits the
driving user's admin: opening the admin tile only grants admin because the
tile itself holds `xbin:admin` — so **don't add admin/privileged tiles to a
non-admin user's allow-list**.

**Terminals are admin-only.** `/ws/term` is a **root shell** in a tile's
directory, gated behind a per-user `terminal` permission (admins have it;
grant it to others only if you mean root).

**User management API** — gated by `xbin:users` (distinct from
`xbin:admin`, so a dedicated user-admin tile can hold just this; admin
implies it):

```
GET    /api/xbin/whoami            caller identity + permissions (any principal)
GET    /api/xbin/users             list (no hashes)
POST   /api/xbin/users             create {id,name,role,tiles,terminal,password}
PATCH  /api/xbin/users/<id>        update (role/tiles/terminal/password reset)
DELETE /api/xbin/users/<id>        remove (revokes their sessions)
```

The admin console's **Users** tab and `bx user ls|add|set|rm` drive these.
Passwords are Argon2id-hashed in `data/users.json` (0600); sessions are
server-side (delete/edit revokes immediately) and drop on restart.

**Public-surface lockdown.** Only `/healthz` and `/login`/`/logout` are
unauthenticated; everything else needs a valid principal. Login uses a
per-IP throttle and a generic "invalid credentials" (never reveals whether a
user exists). Nothing sensitive (vault values, tokens, grants, backend
status/logs) is reachable by a non-admin. Expose a xbin port only behind
Tailscale or a TLS proxy regardless — the outer boundary is the VM/host.

## Owner login mechanics

- First boot generates a token (`.xbin/token`); xbind prints
  `…/login?token=…` — opening it sets an HttpOnly SameSite=Lax cookie.
- CLI/scripts use `Authorization: Bearer` (terminals have `$XBIN_TOKEN`).
- **Disabling token login.** Once you've created an admin *user*, an admin can
  turn off the bootstrap token's *browser* login from the admin console's
  **Users → sign-in security** toggle (`PATCH /api/xbin/auth-settings`
  `{tokenLoginDisabled:true}`). Then the `…/login?token=` URL is refused **and**
  an owner-token cookie no longer authenticates (a leaked token can't be pasted
  into a cookie) — everyone signs in with an account. Guarded against lockout:
  it needs an existing admin user and a signed-in admin-*user* caller (not the
  bootstrap token itself). The **Bearer** owner token is deliberately
  unaffected, so `bx`/terminals keep working; to revoke that too, rotate
  `.xbin/token`.
- Behind an https proxy the cookie turns `Secure` automatically
  (`X-Forwarded-Proto`). xbind itself never does TLS; put Tailscale or
  Caddy in front.
- `--dev` / `--no-auth` disables owner auth (never expose such an instance),
  but element identity and grants still apply — dev and prod run the same
  RBAC.
