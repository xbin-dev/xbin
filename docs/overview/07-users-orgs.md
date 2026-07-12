# Humans: users, orgs & teams

xbin governs two populations with two separate machineries: **humans** (who
may see, edit, or shell into which tiles) and **tiles** (what a running
element may call or be granted). This chapter is the human half — accounts,
per-tile access levels, GitHub-shaped organizations and teams — plus the one
construct that bridges the planes: the **policy ceiling**, which lets
workspace and org admins bound what a set of tiles may ever be granted.

**Related:** [05-identity.md](05-identity.md) (how a human becomes a
principal) · [06-authorization.md](06-authorization.md) (the tile-side grant
model) · [09-terminals.md](09-terminals.md) (what the terminal flags actually
do) · [13-ingress.md](13-ingress.md) (the `ingress` deny kind) ·
[/docs/auth.md](/docs/auth.md) (reference: multi-user + "Organizations &
teams") · plans/orgs.md, plans/multi-user.md, plans/DECISIONS.md (D16–D21).

## Two planes, one bridge

| Plane | Governs | Mechanism | Lives in |
|---|---|---|---|
| Human | what a signed-in person sees and does | users, levels, orgs/teams (this chapter) | `data/users.json` |
| Runtime | what a running tile may call/hold | roles, grants, bindings ([06-authorization.md](06-authorization.md)) | workspace `xbin.json` |
| Bridge | what tiles **may be granted** at all | policy-ceiling rows, keyed by tile patterns | `data/users.json` |

The split is deliberate. Grants live in the git-tracked workspace manifest —
agents can read them, `bx doctor` can lint them, and a hand edit is an
ordinary file change. Identity and policy live in **`data/users.json`**:
xbind-owned, written `0600` under a `0700` directory, replaced atomically
(temp file + rename), masked out of terminals (`data/` is a secret dir) and
never mounted into tile sandboxes at all — so no shell, agent, or
compromised backend can edit an ACL or a ceiling row.
Humans are *never* subject to the ceiling; it constrains elements only (D20).

With zero users configured the workspace is in **single-user mode**: the
bootstrap owner token is the only principal and behaves as an admin
everywhere. Everything below activates the moment the first account exists.

## Users (D16, D17)

```jsonc
// one entry of data/users.json → "users" (PassHash never leaves the server)
{
  "id": "alice",                     // permanent key: login, homes/alice, "user:alice" attribution
  "name": "Alice",
  "role": "user",                    // admin | user
  "tiles": {                         // path or pattern → level (read|write|terminal)
    "apps/crm": "terminal",
    "apps/reports/*": "read"
  },
  "canCreate": ["apps/sandbox/*"],   // where they may create tiles
  "termApi": false, "termNet": false // D17 terminal-plane grants (below)
}
```

A user id is a **durable key** — it names the terminal home (`homes/<id>`),
the per-user prefs bucket, and the `user:<id>` attribution in `X-XBin-From`
and the audit log — so ids are validated once, at creation
(`^[a-z0-9][a-z0-9._-]{0,31}$`; `owner` is reserved for the bootstrap-token
principal's home) and are immutable thereafter. Passwords are hashed with
Argon2id; login verification does constant-ish work even for unknown
usernames, and the login endpoint is throttled per client IP
([05-identity.md](05-identity.md) covers sessions, and the owner-token-login
kill switch with its double lockout guard).

`role: "admin"` short-circuits everything in this chapter: admins hold
`terminal` on every tile, may create anywhere, carry both terminal flags
implicitly, and manage users, orgs, and policy.

### Access levels: read < write < terminal

Levels are **monotone** (each includes the ones below) and **union-only**:
the highest matching entry wins, so patterns can widen access but never
narrow it. Patterns are the workspace-wide idiom — exact path, `prefix/*`
(the prefix and everything under it), or `*`.

| Level | What it gates |
|---|---|
| `read` | The tile appears in the shell's sidebar and component list; its page opens (`/c/…` static serving is read-gated); its alerts are visible; its **source is visible in terminals** — in a non-admin's terminal, every tile *below* read is mount-masked out of the workspace (sealed empty mounts, D17a), so the file tree itself matches the ACL. |
| `write` | Edit and drive it: source edits through the editing plane, write-shaped tile actions. |
| `terminal` | A shell **on that tile** (the terminal acts as the tile's element principal, not as the human — [09-terminals.md](09-terminals.md)), the dev-layer reset, and the **logs tab / `GET /logs`**. Logs are deliberately terminal-gated, not read-gated: backend stdout/stderr routinely carries secrets. |

Creating a tile auto-grants the creator `terminal` on it — "create ≈ own a
namespace" (D16). The grant is attributed: it fires for the human even when
they create through a manager-style tile (the frame principal carries the
user id along).

### Terminal-plane flags (D17)

Two per-user booleans govern what a non-admin's terminals *carry*, because a
shell is the workspace's most powerful surface:

| Flag | Off (default) | On |
|---|---|---|
| `termApi` | `api=0` forced — the shell gets no live tile-API token | terminal carries the tile-scoped `$XBIN_TOKEN` |
| `termNet` | `net=none` forced — no internet egress | `net=internet` allowed (through the egress relay) |

Denial **clamps rather than 403s**: an ungranted user still gets a working,
airgapped, code-only shell. `net=host` stays admin-only unconditionally.
Teams confer both flags by union (below), but only a workspace admin may set
them — on users or on teams (D21). Non-admin terminals additionally run in
the restricted sandbox tier (D18 kernel lockdown, cgroup limits —
[09-terminals.md](09-terminals.md)).

## Organizations (D19)

Orgs group people and tiles GitHub-style — with one xbin twist: **an org owns
its namespace positionally**. The reserved path segment `o` binds a tile to
its org:

```
apps/o/sales/crm      → org "sales"     (<dir>/o/<org>/…)
o/sales/dashboard     → org "sales"     (o/<org>/… at the top level)
apps/crm              → workspace-plane (no org)
```

"Whose tile is this" is readable off the path — no ownership table to drift,
no collisions, and a pre-orgs workspace keeps working untouched (non-marker
paths stay workspace-plane). The segment `u/` is reserved the same way for
future per-user tiles.

The marker is protected at **creation time**: every tile-creating entry point
(create, clone, git import, tile import, template instantiation) runs the
same validation — `o` is legal only in the two marker positions, must name an
org that already exists (no squatting `apps/o/x` before org `x` does), and
the org container itself (`apps/o/sales`) is not a valid tile path (tiles
live strictly below it). Existing directories are never rejected
retroactively; `bx doctor` warns about paths that would no longer validate.

```jsonc
// one entry of data/users.json → "orgs"
{
  "id": "sales", "name": "Sales",
  "admins":  ["alice"],          // delegated org admins (D21); implicitly members
  "members": ["alice", "bob"],
  "basePermission": "read",      // ""|read|write — the floor every member holds
                                 // on every org tile; terminal is never org-wide
  "policy": [ /* ceiling rows — workspace-admin-managed */ ],
  "teams": [ {
    "id": "backend", "name": "Backend",
    "members": ["bob"],                       // must be org members
    "tiles": { "apps/o/sales/crm": "write" }, // pattern→level, org-clamped
    "canCreate": ["apps/o/sales/*"],          // org-clamped
    "newTiles": "write",                      // level the team gets on create-in-team
    "termApi": false, "termNet": false        // workspace-admin-set only (D21)
  } ]
}
```

Membership hygiene follows GitHub: org admins are implicitly members; a
member removed from the org is stripped from its teams; a deleted user is
stripped from every org and team.

## Teams: union-only widening

Teams **only ever widen** access (D19 — capping models were considered and
rejected; GitHub doesn't cap, and min-over-caps × max-over-grants is
unreasonable to reason about). A team's `tiles` and `canCreate` entries work
exactly like a user's own — same patterns, same levels — with one clamp:
**they take effect only on paths inside the team's org**
(`OrgOf(path) == team's org`).

The clamp is enforced at **evaluation**, not (only) at write time: a
hand-edited team pattern that reaches outside its org is simply inert, and
`bx doctor` flags it. That choice makes the guarantee independent of every
write path.

### The effective level, precisely

For a signed-in human on tile path `p`, the effective level is computed by
one resolver (`users.Access`) as the **maximum** of:

1. workspace admin → `terminal`, everywhere (stop);
2. org admin of `OrgOf(p)` → `terminal` on that org's tiles (stop);
3. the user's own matching `tiles` entries;
4. the org `basePermission`, if the user is a member of `OrgOf(p)`;
5. each of the user's teams' matching entries, where the team's org is
   `OrgOf(p)`.

Every gate in the system — sidebar, static serving, editing, terminals,
mount masking — asks this one resolver, so there is exactly one answer to
"what can Alice do here". The same resolver **explains itself**: each
contribution carries a provenance tag (`admin`, `org-admin:<org>`,
`direct:<pattern>`, `team:<org>/<team>:<pattern>`, `base:<org>`), which is
what the access-map UI renders.

### Creating tiles in an org, and in a team

Creation inside an org **requires org membership** (a D19 amendment): a broad
personal pattern like `apps/*` must not let a non-member inject tiles into
`apps/o/sales/…`. (Read/write personal patterns deliberately stay global —
the workspace-admin-appointed auditor case.) Within the org, create authority
is the union of the user's own patterns, their teams' patterns, and implicit
org-admin create.

**Create-in-team** (`POST /create` with `team: "sales/backend"`, the manager
tile's team picker, or `bx new --team`):

1. the path must be inside the team's org;
2. the attributed human must be a team member or an org admin (workspace
   admins pass outright) — an element's own workspace-management grant is
   *not* enough to act "in a team";
3. on success the **team** is auto-granted its configured `newTiles` level
   (default `write`) as an exact entry — visible and revocable in the tile's
   ACL — and the **creator** keeps their personal D16 `terminal` auto-grant.

The per-team `newTiles` level was chosen over a per-creation choice (a
low-trust member could hand the whole team `terminal`) and over
namespace-only grants (no per-tile row to show or revoke).

One more amendment closes the confused deputy: when a human drives a
tile-creating element (frame or terminal principal), the *human's own* create
rights must cover the path too — a manager tile's `xbin:writer` grant never
extends what its driver may create. Unattributed automation (instance tokens,
the bootstrap owner) keeps pure capability semantics.

## Org admins: security-capped delegation (D21)

| Org admins manage (org-clamped) | Workspace-admin only |
|---|---|
| org name, members, co-admins | creating / deleting orgs |
| base permission | policy-ceiling rows (workspace and org) |
| teams: create/delete, members, tiles, canCreate, newTiles | team `termApi` / `termNet` (terminal-plane security) |
| per-tile access entries on org tiles | anything outside the org's tree |

Org admins hold implicit `terminal` and create on their org's tiles —
equivalent power to their ACL-editing rights, with less friction (the
implicit grant still shows in `/access` provenance as `org-admin:<org>`).
They may also read the **users directory** (`{id, name}` only — no roles,
grants, or hashes) so their pickers can add existing accounts.

Two structural rules keep the delegation capped:

- **Org admins act as signed-in humans.** The management APIs accept a
  session (cookie) principal that is an org admin of *that* org; element
  principals never pass this gate, so a tile can never inherit its driver's
  org-adminship (the frame-token rule, [05-identity.md](05-identity.md)).
- **Their UI is workspace chrome, not the admin tile.** The shell's per-tile
  ⚙ access panel and the "orgs & teams" popover (`bx-tile-admin`,
  `bx-org-admin`) run in the shell document and use raw `fetch` — the caller
  *is* the human. Granting a non-workspace-admin `read` on `tiles/admin`
  instead would mint them the admin tile's frame token, and with it the
  tile's own `xbin` capabilities — a capability leak, not a UI choice.

## The policy ceiling (D20)

Ceiling rows bound what the covered **tiles** may ever be granted —
regardless of who approves, and regardless of what's written in the
workspace manifest:

```jsonc
// workspace-level rows: data/users.json → "policy"; per-org: orgs[].policy
{ "tiles": "apps/o/sales/*",                     // which tiles the row covers
  "deny":    ["net", "gpu", "xbin-caps", "ingress"], // strip capability classes outright
  "mayCall": ["apps/o/sales/*", "res:apps/o/sales/*"] } // allow-list call targets
```

**Composition** is restrictive: the rows covering a tile (workspace rows +
its org's rows) combine so that *any* deny wins, and *every* row that
carries a `mayCall` list must cover a target for the call to survive
(intersection).

**Enforcement is double.** At **approval**, the grant and binding APIs
refuse with a 400 naming the blocking row, and pending requests a ceiling
makes unapprovable are annotated `blocked` so UIs don't offer a dead approve
button. At **evaluation**, the ceiling is applied inside `grantedRole` —
ahead of all three grant sources (explicit grant rows, http-interface
bindings, same-scope auto-grants) — and at the `net`, GPU, and ingress
resolution points. Grants live in the git-tracked `xbin.json`, but the
ceiling lives in xbind-owned `users.json`: hand-editing the manifest cannot
bypass it, and a pre-existing binding goes inert the moment a deny row
covers its tile.

Deny kinds and their reach:

| Kind | Strips |
|---|---|
| `net` | net-interface bindings (internet/host/lan/provider), the `cap:net-admin` provider capability, and lan-ingress legs |
| `gpu` | `gpu:*` grants |
| `xbin-caps` | the reserved `xbin` / `xbin:*` capability targets — including a covered element's broker-adminship — and the blanket `code` grant; also downgrades the tile's whoami trust tier (below) |
| `ingress` | exposed-endpoint bindings: covered tiles are unpublishable, existing routes go inert ([13-ingress.md](13-ingress.md)) |

Two subtleties earned their own amendments:

- **Same-scope exemption.** `mayCall` governs *external* reach only:
  same-scope targets — an app's own `res:<scope>/…` resources and intra-app
  calls — are always exempt. Without it, the obvious org row
  `{tiles: "*", mayCall: ["apps/o/x/*"]}` silently severed every covered
  tile from its own database. A scope is one trust unit.
- **Capability targets are classified explicitly, never left to the
  path-matcher.** A `mayCall` allow-list is a list of *paths*; it can never
  match the string `code`, so routing capability targets through it silently
  strips them — the 2026-07-12 `code:reader` regression. Bare `code`
  (whole-workspace source read — owner-level) is classified under
  `xbin-caps`; scoped `code:<component>` is governed exactly like *calling*
  that component (same-scope exempt, otherwise `mayCall` must cover the
  path). The regression also taught a testing lesson: the broker suite ran
  without a user store while production always has one, so every ceiling
  path was dormant in tests — the harness now always attaches a store.

## Trust-scoped introspection

`GET /whoami` answers "who am I and what may I do" for every principal — but
what it reveals about the *driving human* on an element principal is scoped
to the tile's trust, so a low-value or compromised tile can't harvest the
org chart:

| Caller | `whoami` shows about the human |
|---|---|
| the human directly (session) | full self view: tiles, canCreate, term flags, org memberships with teams (+ per-team `canCreate`, so create-in-team pickers can pin paths); workspace admins see every org and team |
| any tile they drive | `user: {id, name}` — attribution only |
| a tile *inside* an org (`OrgOf` set) | + that one org's membership slice (its teams, the org-admin flag) |
| a workspace-management tile (`xbin` / `xbin:users` grant) | + the admin flag and the full org list — a compromise there already means workspace control |

The tier check runs through `grantedRole`, so an `xbin-caps` policy deny
downgrades a tile's view together with its capability.

For admins, the same provenance machinery powers the **access map**: a
users × tiles matrix of effective levels where every cell explains itself
(`direct:apps/crm`, `team:sales/backend:apps/o/sales/*`, `base:sales`,
`org-admin:sales`, `admin`), and a per-tile ACL panel (`GET/PUT /access`)
that lists exact and pattern entries with their sources and edits exact
entries — the one write path that may also *lower* or clear a level (the
auto-grant paths are monotone and only ever raise).

## Surfaces

| Surface | What it drives |
|---|---|
| admin tile → **users** | accounts: role, tiles editor, canCreate, term flags, password, token-login kill switch |
| admin tile → **orgs & teams** | org CRUD, members/admins, base permission, team editors, policy rows |
| admin tile → **access map** | the users × tiles matrix with per-cell provenance |
| shell ⚙ → **access** (`bx-tile-admin`) | per-tile ACL: entries, sources, add/edit/remove — org-admin reachable |
| shell → **orgs & teams** popover (`bx-org-admin`) | org-admin self-service: members, teams, base — no bx needed |
| `bx user` / `bx org` / `bx team` / `bx access` | the same APIs from any terminal (`bx org policy [--set]` for ceiling rows) |
| `bx doctor` | org-path warnings: stray `o` markers, unknown orgs, reserved `u`, org-escaping (inert) team patterns |

Org, team, access-entry, and policy mutations publish a `users` event on the
workspace hub, so open panels refresh live. The underlying HTTP surface is
documented in [/docs/protocol.md](/docs/protocol.md); the security model in
depth in [/docs/auth.md](/docs/auth.md).
