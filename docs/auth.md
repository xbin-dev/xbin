# Auth: identities, roles, grants, vault

xbin has two planes with different rules:

- **Editing plane** — terminals, `bx`, git. Full *filesystem* access to the
  component being edited; the shell's API token, though, is **scoped to the
  tile the terminal is opened on** (plans/terminal-tokens.md). Owner-
  privileged automation lives on the host (`.xbin/token`).
- **Runtime plane** — running elements (backends and their frontends).
  Default-deny: an element can call exactly its own API plus whatever it was
  granted. Terminals follow the same identity model, so this page covers all
  of it.

## Who is calling? (principals)

| Principal | How it authenticates | Typical `X-XBin-From` |
|---|---|---|
| Owner (you) | login cookie, or `Authorization: Bearer` with the owner token | `owner` |
| Element backend | per-generation instance token over the gateway socket (`XBIN_GATEWAY` + `XBIN_TOKEN` env — the SDK's `xbin.Client()` handles it) | `apps/email` |
| Element frontend | owner cookie **+ frame token** (`xbin.fetch` attaches it) | `apps/email` |
| Terminal shell | per-session terminal token (`$XBIN_TOKEN` in the shell) | `apps/email` — the tile the terminal is opened on, **not** the human driving it |
| Scheduler | internal | `xbin/cron` |
| Public visitor | none — anonymous traffic through a **published endpoint** (docs/ingress.md) | `ingress` |

Callees never verify any of this themselves: xbind strips inbound
`X-XBin-*` headers and injects the verified `X-XBin-From` and
`X-XBin-Role`. If those headers are present, they're true.

**The `ingress` principal** is structural, not a credential: it enters only
on the separate ingress listeners (never the authenticated routes), reaches
exactly ONE tile — the one whose owner-approved binding published the
hostname — and only the paths that tile's manifest declared public
(`exposes.…paths`, default-deny; traversal resolved before matching). It
carries no role, can't call `/api/xbin/*` or any sibling tile, and the
workspace session cookie is stripped before it reaches the backend. The tile
owns any app-level auth on its public routes; a workspace/org policy row can
deny `ingress` for a set of tiles outright (see policy ceiling below).
`ingress` is a reserved name — no component can produce that `From`.

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
- **Automated agents cannot self-approve cross-scope grants — enforced.**
  Approval is the owner's human-in-the-loop decision. An agent's terminal
  token is scoped to its tile, so `bx grant` (like every admin endpoint)
  403s from inside a tile terminal. Agents declare `uses` and leave
  cross-scope (and `xbin:*`) grants pending for the owner to approve in the
  grants panel. (AGENTS.md restates this for in-workspace agents.)
- **Role names**: `reader` / `writer` / `admin` are the convention and imply
  each other downward (`admin ⊃ writer ⊃ reader`). Custom names are
  exact-match unless the callee's manifest declares `implies`. On a `bus`
  resource, `subscriber`/`publisher` are aliases for reader/writer.
- The **owner passes every check as `admin`** — the root UI, and curl with
  the host-side owner token, are never blocked. (A *terminal's* `$XBIN_TOKEN`
  is not the owner — it's scoped to the terminal's tile.)
- **Sandbox capability targets** (under `--isolate`): besides components and
  `res:*`, `uses` can request `net:*` egress (`plans/isolation.md`) and `gpu:*`
  GPUs — `gpu:all` / `gpu:<index>` / `gpu:<uuid>` (`plans/gpu.md`). Same
  owner-approval flow; ungranted means the sandbox gets no egress / no GPU.
- **`code:<component>`** (one component) / **`code`** (all components) —
  read-only access to another component's **source** (its files + git log/diff
  via `/api/xbin/code/*`, `/api/xbin/git/{log,diff}`), the runtime equivalent
  of what a workspace terminal sees read-only. Request
  `uses {target:"code:apps/x", role:"reader"}` for one, or
  `{target:"code", role:"reader"}` for **every** component (tooling — linters,
  stats, search — that must scan the whole workspace without re-granting per
  component). Owner-approved like any grant; a component always reads its own
  source, and admin reads any. Powerful — it exposes everything in the
  target's tree except `.git` internals / `node_modules` / `data`; `code`
  (all) even more so. Grant deliberately.
- **`cap:net-admin`** — a **net-provider** tile (a router/firewall/VPN that
  splices other tiles' egress; `plans/interfaces.md`) builds its own dataplane
  — routing tables, `ip_forward`, `AF_PACKET` sockets — which needs the
  network-admin capabilities (CAP_NET_ADMIN, CAP_NET_RAW, CAP_NET_BIND_SERVICE)
  the sandbox otherwise drops from every backend. Request
  `uses {target:"cap:net-admin", role:"writer"}`; **admin-only to approve**
  and it lands pending on import. Without it the provider's gate setup fails
  with "operation not permitted". A workspace/org policy `net` deny
  (`plans/orgs.md`) strips it, and it stays confined to the tile's own network
  namespace (the caps don't reach the host). Grant deliberately.

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
- **A secret's value is readable only by the element it belongs to** — not even
  the owner or an `xbin:admin` tile. Admins can **list keys** and **set / rotate
  / delete** any vault via `bx vault` / the admin console (the password-manager
  function), but the read (`GET …/vault/<comp>/<key>`) is self-only, so a
  compromised admin tile can rotate secrets but can't exfiltrate them. (The
  human with host access can still read `data/vault/` on disk with the
  passphrase — this locks down the *API*, the exfiltration surface.)

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
bx vault status                 # unsealed | sealed | unconfigured | plaintext
bx vault unseal                 # prompts (no echo); creates the barrier on
                                # first use and encrypts existing plaintext
bx vault seal                   # drop the key from memory
bx vault rekey                  # change the passphrase (re-wraps the data
                                # key; nothing is re-encrypted)
```

The admin console's **vault tab** is the UI for the same lifecycle: it shows
the seal state and has forms to set the first passphrase (unconfigured →
creates the barrier), unseal after a restart, change the passphrase, and
seal on demand.

**First setup (manual mode).** A fresh install without
`XBIN_VAULT_PASSPHRASE` boots **unconfigured**: the UI works but secret and
resource storage is refused until a passphrase exists. Log in, open the admin
tile → vault, and set the passphrase once (or run `bx vault unseal` in a
terminal) — that creates the barrier and encrypts anything already written.
Pick the passphrase carefully: **it cannot be recovered**, and after every
daemon restart an admin must enter it again to unseal (that's the point of
manual mode — it's never stored). For hands-off restarts instead, put
`XBIN_VAULT_PASSPHRASE=…` in `/etc/xbin/xbin.env` (mode 600).

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
| 3 (`--isolate`, rootless — production) | each backend in its own user+mount+pid+ipc+uts+net namespaces over an overlay rootfs; **all Linux capabilities dropped** + a **seccomp block-list** (a net-provider tile keeps net-admin caps in its own netns, D18a); **default-deny egress** via the `net:*` relay; **enforced cgroup limits** (memory/pids/CPU) | almost nothing at the OS layer: no sibling `/proc` or env, no sibling sockets, only granted files are mounted, no network beyond its `net:*` grants, and it can't exceed its memory/pids budget |

Tier 3 is the OS-level sandbox (`plans/isolation.md`, `plans/runtime.md`) and the
production model: rootless (unprivileged user namespaces), run on a VM/host
xbind controls (README → Running it). Backends already drop all capabilities,
carry a seccomp block-list, and run under enforced cgroup v2 limits
(memory.max/high, pids.max, cpu.weight). Still roadmap: `wasm`/wazero backends
and per-scope **origin** isolation.

Browser side, all elements are same-origin: frame tokens give **attribution**
(RBAC works), not **isolation** (a malicious element's JS runs in the same
origin). So the outer boundary is the VM/host; treat same-origin element
*frontends* as one trust domain, and the grant system as seatbelts and audit
trail, not a jail.

## Audit log

Every state-changing call to the core API (`POST`/`PUT`/`PATCH`/`DELETE` on
`/api/xbin/…`) is logged at `INFO` as an `audit` line — actor (`X-XBin-From`:
`owner`, `user:<id>`, or a component), method, path, and resulting status — so
there's a who-changed-what trail for user/grant/lifecycle/vault/token changes.
High-frequency data-plane writes (`prefs`, `kv`, `blob`, `bus`) are excluded as noise. This is
a log stream, not a queryable store; ship xbind's stderr somewhere durable if
you need retention.

## Multi-user (users, roles, tile access)

xbin can have **human users** on top of the root token (plans/multi-user.md).

- **Root token** (`XBIN_TOKEN`) — the admin/bootstrap service credential.
  Full admin; used by `bx`, terminals, automation. Always valid.
- **Admin user** — logs in with username+password; full access (all tiles,
  terminals, user management).
- **Regular user** — logs in; access is scoped per tile by an access level
  (below); no admin, and no terminal/create beyond what's granted.

No users configured ⇒ single-user mode (the root token is the only
principal), exactly as before. The first user is created by an admin.

**User ids are permanent keys.** An id is the durable key for the terminal
home (`homes/<user>`), the per-user prefs bucket, and `user:<id>` attribution
in `X-XBin-From`/logs — so it is validated at creation and **never renamable**
(to "rename", create a new user and delete the old; their `homes/<old>` stays
on disk for you to move). Rules: 1–32 chars of `a–z 0–9 . _ -`, starting
alphanumeric; `owner` is reserved (it is the root-token principal's home).
Passwords set through the API must be **at least 8 characters** (Argon2id
hashed; the dev-seeded admin and tests bypass this by writing the store
directly).

**Tile-level RBAC — access levels.** A user's `tiles` maps component paths —
or `prefix/*` (a scope/subtree; `*` = all) — to an **access level**, monotone
`read < write < terminal`:

| Level | Grants |
|-------|--------|
| `read` | see the tile: load `/c/<tile>/`, get a frame token, see it in the shell, read its source in a terminal |
| `write` | everything read grants, plus edit/drive the tile (the old allow-list power) |
| `terminal` | everything write grants, plus a **root shell** in the tile's directory (`/ws/term`, and resetting its dev layer) |

Levels union: the highest matching entry wins, so patterns widen access and
can never narrow it (`{"apps/*": "read", "apps/chat": "terminal"}` gives a
shell on chat and read on the rest). A tile a user may use runs with its
*own* grants — allowing a user a tile lets them use that tile's capabilities,
like app permissions on a phone. A tile never inherits the driving user's
admin: opening the admin tile only grants admin because the tile itself holds
`xbin:admin` — so **don't add admin/privileged tiles to a non-admin user's
allow-list**.

**Create permission.** `canCreate` lists path patterns (`sales/*`) under
which the user may scaffold new tiles. It governs **every way a tile can
appear**: `POST /api/xbin/create`, clone, git import, builtin tile import,
and template instantiate (`bx new`, the manager tile). Creating one
auto-grants the creator `terminal` on it — create ≈ own a namespace.
Copy-shaped creation (clone, workspace-template instantiate) additionally
requires **read on the source** — copying is reading.

**The confused-deputy clamp.** An element holding the workspace-management
grant (`xbin:writer` — the manager tile ships with it) may create tiles, but
when a signed-in human is attributed on the call (frame/terminal
principals), the human's **own** create permission must cover the path too.
Granting a user the manager tile never extends what they may create;
unattributed automation (instance tokens, the bootstrap owner) keeps plain
capability semantics.

**Terminal-plane grants.** A non-admin's terminals run restricted
(docs/isolation.md): beyond the kernel lockdown, they get **no live tile-API
token** unless the user has `termApi`, and **no internet egress** unless
`termNet` (host networking is admin-only, always). Grant `terminal` levels
only if you mean root in that directory.

**User management API** — gated by `xbin:users` (distinct from
`xbin:admin`, so a dedicated user-admin tile can hold just this; admin
implies it):

```
GET    /api/xbin/whoami            caller identity + permissions (any principal)
GET    /api/xbin/users             list (no hashes)
POST   /api/xbin/users             create {id,name,role,tiles:{path:level},
                                   canCreate?,termApi?,termNet?,password}
PATCH  /api/xbin/users/<id>        update (fields overlay; +password reset)
DELETE /api/xbin/users/<id>        remove (revokes their sessions)
```

(The pre-tiers body — `tiles` as an array + a global `terminal` bool — is
still accepted: array entries load as `write`, or `terminal` when the flag is
set; `users.json` migrates the same way on load. See
changes/2026-07-11-tile-access-tiers.md.)

The admin console's **Users** tab and `bx user ls|add|set|rm` drive these.
Passwords are Argon2id-hashed in `data/users.json` (0600); sessions are
server-side (delete/edit revokes immediately) and drop on restart.

## Accounts, invites — and no self-signup (D22)

Accounts are **only ever created by an admin** (`POST /users`, `bx user add`,
the admin tile) — there is no self-registration surface at all. Credential
delivery has two shapes:

- **Admin-set password** — as before; tell the person out of band.
- **Invite link** — create the user *without* a password and the API returns a
  **single-use, 72h** link (`/login?invite=…`; token hashed at rest). The
  invitee opens it, sets their own password, and is signed in. Re-minting
  (`POST /users/<id>/invite`, `bx user invite`) invalidates the previous link
  and doubles as reset-by-link; redemption is login-throttled.

The account row is deliberately **credential-agnostic**: a user is an ID plus
however their credential arrives — an admin-set password, an invite-redeemed
one, and in the future an **SSO/OIDC identity** (enterprise/Google-Workspace
sign-in for company-wide workspaces) would bind to the same row via the same
no-self-signup rule (the IdP asserts identity; a workspace admin — or a
domain allow-rule they configure — still decides who gets an account).

## Ownership, organizations & delegated approval

The multi-user grouping model (plans/ownership.md, DECISIONS D24–D28).
Everything lives in the identity store (`data/users.json`) — outside the
workspace, so no terminal or tile can edit it.

**Ownership (D24).** Every component may have an owner: `user:<id>` or
`org:<id>` (absent = workspace-owned, admin-managed). Ownership is assigned
at creation (a human creator becomes the user-owner; `owner: "org:<id>"`
creates an org-owned tile) and is transferable (`POST /owner`). A user-owner
holds implicit `terminal` on their tile and manages its ACL — **sharing is an
ownership right**: the ws-admin, the user-owner, and the owning org's admins
may edit a tile's exact `user:`/`org:` ACL entries (`PUT /access`).

**Orgs (D25).** An org is a flat member list — no teams. Each member carries
`{level: read|write|terminal, create, admin}`: `level` applies org-wide to
every tile the org **owns** (org admins get terminal implicitly); `create`
gates creating new org-owned tiles; `admin` is org management (members,
org-tile ACLs, transfers, exercising allowances). UI presets: **Admin**
(terminal+create+admin), **Developer** (terminal+create), **Viewer** (read).
Tiles can also be **shared to an org** (an `org.Tiles` entry — all members
get that level, wherever the tile lives).

**Effective access** = max of: workspace-admin (terminal everywhere) ·
owning-user (terminal) · org-member level / org-admin terminal on org-owned
tiles · org shares · the user's own pattern entries (D16) · workspace
`defaultTiles` (D27 — baseline visibility every user gets; the scaffold
seeds welcome/apidocs read).

**Policy ceilings (D20, re-keyed).** Deny/mayCall rows still cap what any
tile may be *granted* — workspace rows plus, for org-owned tiles, the owning
org's rows and every attached permission set's rows (restrictive union; any
deny wins; humans are never subject to ceilings).

**Delegated approval (D26).** A ws-admin can give an org an **allowance** —
target patterns its **admins** may approve on org-owned tiles themselves:
`res:… gpu:… cap:… net:internet|host|lan:…|provider:… iface:<service>
ingress:host:/zone:/listen:<range> tile:<pattern>`. Anything is delegable —
a high-trust org can get `cap:containers` or publication rights — **except
`xbin`/`xbin:*`**: an element granted `xbin@admin` *is* a workspace admin,
so delegating it would make org admins ws-admins transitively (rejected at
write, ignored at evaluation). Grants wholly inside one org's owned tiles
(intra-org wiring) need no allowance. Every approval still runs the ceiling
check and is audited as the actual approver; revokes/unbinds are always
allowed for the owning org's admins. Element principals never approve —
tiles request, humans decide.

**Permission sets (D28).** Named, reusable `{allow, policy, termApi,
termNet}` bundles attached to orgs *by reference* (`sets: […]`, multiple per
org): effective allowance = union(sets)∪extras; set ceiling rows join the
restrictive union; term flags confer to members of attached orgs. Ws-admin
only; deleting an attached set is refused. Managing ten orgs = editing one
set.

## Owner login mechanics

- First boot generates a token (`.xbin/token`); xbind prints
  `…/login?token=…` — opening it sets an HttpOnly SameSite=Lax cookie.
- CLI/scripts use `Authorization: Bearer`. **Inside a terminal**, `$XBIN_TOKEN`
  is a per-session token scoped to that tile (the tile's element principal —
  self-admin + its approved grants, never the owner; plans/terminal-tokens.md);
  it dies with the session, and deleting the user kills it immediately. The
  root terminal is disabled. The *owner* token lives only on the host
  (`.xbin/token`) for host-side `bx`/automation — and under `--isolate` a
  terminal can't read it, because `.xbin/`, `data/`, and other users' `homes/`
  are masked out of the workspace mount (docs/isolation.md §terminal). A
  terminal's titlebar can also set **no-API** mode: the session gets no token at
  all, so the shell sees code but every API call is unauthorized.
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
- **Rotating the owner token.** `POST /api/xbin/auth-rotate-token` (admin; a
  button in the admin console's Users tab) writes a fresh `.xbin/token` and
  swaps it live — the old token stops authenticating *immediately*, for bearer
  calls and owner-token cookies alike. Host-side `bx`/automation must re-read
  the file. **Rotate once after upgrading to terminal-scoped tokens
  (2026-07-09):** before that change every terminal carried the owner token,
  so agent session transcripts under `homes/*/.claude` (and shell histories)
  may hold the old one. (Terminal tokens themselves need no rotation — each
  dies with its session, and deleting a user revokes theirs instantly.)
- **Session lifetime.** A browser login is a server-side session (the cookie
  holds only a random id). It expires after **12 h of inactivity** (sliding —
  each request renews it) or **30 days** since login (a hard cap regardless of
  activity), whichever comes first, so a stolen cookie can't authenticate
  forever. The server is authoritative; override the windows with
  `XBIN_SESSION_IDLE_TTL` / `XBIN_SESSION_MAX_TTL` (Go durations, e.g. `8h`).
  Deleting a user, logout, and an xbind restart all end their sessions
  immediately. (Bearer tokens have no session — the owner token is valid until
  rotated; element/terminal tokens die with their generation/shell.)
- Behind an https proxy the cookie turns `Secure` automatically
  (`X-Forwarded-Proto`). xbind itself never does TLS; put Tailscale or
  Caddy in front.
- `--dev` / `--no-auth` disables owner auth (never expose such an instance),
  but element identity and grants still apply — dev and prod run the same
  RBAC.
