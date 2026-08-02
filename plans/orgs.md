# Orgs & teams — design

> **SUPERSEDED (2026-08-02)** by [`plans/ownership.md`](ownership.md): teams
> and positional `o/<org>/` paths are being replaced by NPM-style per-tile
> **ownership** (user- or org-owned, transferable), flat org roles, and
> ws-admin-delegated **allowances** for self-approving system-resource grants
> (D24–D27). No released workspace used this model, so it is removed rather
> than migrated. Kept for the D19–D21 rationale it documents.

GitHub-shaped grouping layered on the flat multi-user store
(plans/multi-user.md): users grouped into **teams** inside **orgs**, per-tile
access assigned to teams/users, tiles created *in a team* picking up team
perms, org-level **policy ceilings** on what member tiles may be granted, and
**security-capped delegated administration**. Decisions D19–D21 in
`DECISIONS.md`; builder docs in `docs/auth.md` ("Organizations & teams").

## The shape (one screen)

```
org "sales"                              ← owns the o/sales path namespace
  admins:  [alice]                       ← delegated management (capped, D21)
  members: [alice, bob]                  ← basePermission floor on org tiles
  basePermission: "read"                 ← ""|read|write (never terminal)
  policy: [{tiles:"*", deny:["net"]}]    ← runtime ceiling (ws-admin-set, D20)
  teams:
    backend:
      members: [bob]                     ← must be org members
      tiles: {"apps/o/sales/*":"write"}  ← union grants, org-clamped
      canCreate: ["apps/o/sales/*"]
      newTiles: "write"                  ← create-in-team auto-grant level
      termApi/termNet: false             ← D17 flags, ws-admin-set only
```

A tile at `apps/o/sales/crm` belongs to org `sales` — **positionally**: the
reserved `o` segment at depth 0 or 1 (`o/<org>/…`, `<dir>/o/<org>/…`) is the
whole org↔tile binding. `u/` is reserved identically for future per-user
tiles. Anything else is workspace-plane and works exactly as pre-orgs.

## Module boundaries (the design rule)

Strictly one-way; each layer answers one question and knows nothing above it:

```
web UI (lit)        bx CLI            ← speak only the documented HTTP API
      └─────┬───────────┘
      HTTP API (broker/orgsapi.go)    ← decode → gate → store call → encode
            │
  enforcement chokepoints             ← one-line questions, no org knowledge
  (server, term, static, broker)        p.CanReadTile(path)? ceiling.Denies(net)?
            │
      auth.Principal                  ← "who is calling" + an Access snapshot
            │
      internal/users (orgs.go)        ← THE semantics: data, OrgOf, patterns,
                                        Access resolution, Ceiling evaluation
```

Consequences worth keeping true:

- `internal/users/orgs.go` has no HTTP/broker/auth imports and is entirely
  table-testable. Any rule you can't point at there doesn't exist.
- All 15 pre-existing enforcement call sites (`Principal.Can*Tile`) became
  team-aware **without changing shape** — shell filtering, `/c/` gating,
  terminal mount-masking (HiddenTiles), create gates.
- The broker asks `Store.Ceiling(path)` at exactly the chokepoints every
  grant source already flows through (see below) and adds nothing else.

## Resolution (union-only, D19)

Effective level of user U on path P = **max** of:

1. U's own `tiles` entries matching P (any path — unchanged);
2. for each team T containing U: T's entries matching P, **iff
   `OrgOf(P) == T's org`** — the *evaluation clamp*: a team pattern can
   never confer access outside its org, however it's written (hand-edited
   escaping entries are inert; `bx doctor` flags them);
3. the org `basePermission` where U is a member (admins are implicitly
   members);
4. implicit `terminal` where U is an **org admin** of `OrgOf(P)` (equivalent
   power to their ACL-editing rights, less friction).

`canCreate` and `termApi`/`termNet` union the same way (create is
org-clamped; term flags are not path-scoped). No caps anywhere — membership
only widens (GitHub semantics; rejected: per-team maxLevel). Two deliberate
asymmetries (pre-ship review): read/write personal patterns stay global
(the auditor case), but creating INSIDE an org requires membership — a
broad personal `canCreate` can't inject tiles into another org's tree; and
the org container path itself is not a valid tile path.

**Create-in-team**: `POST /create {team:"<org>/<team>"}` — path must satisfy
`OrgOf(path)==org`, the *attributed human* must be a team member or an
org/workspace admin (an element's own xbin-writer grant is not enough to act
"in a team"), the team gets an exact entry at its `newTiles` level, and the
creator keeps the personal D16 `terminal` auto-grant.

**Creation authority is one gate** (`broker.canCreateAt`, shared by create /
clone / git import / builtin import / template instantiate): admins; humans
whose union create patterns cover the path; capability-granted elements —
clamped by the attributed driving human's own rights (the confused-deputy
rule: the manager tile never extends what its driver may create).
Copy-shaped routes additionally require read on the source. Nesting guards
(`guardNewComponentTree`) refuse paths inside or above existing components.

## Policy ceiling (D20)

Pattern-keyed rows at workspace + org level, stored in the identity store —
NOT the workspace manifest, deliberately: grants live in git-tracked
`xbin.json` that terminals can edit; the *ceiling* over them lives in
xbind-owned `data/users.json`. Rows compose restrictively: any `deny` wins;
every `mayCall`-bearing matching row must cover the target (intersection).

Enforced twice:

- **Approval**: `POST /grants` and net-slot bindings 400 with the blocking
  row named; revoke/unbind always allowed (cleanup).
- **Evaluation** (the guarantee): `grantedRole` applies the ceiling before
  all three grant sources — explicit rows, interface bindings (bindings are
  grants, plans/interfaces.md), same-scope auto-grants. `xbin`/`xbin:*`
  targets fall under `xbin-caps` (which thereby also neuters a covered
  element's broker-adminship); `gpu:*` under `gpu` (GPUFor resolves through
  grantedRole). `net` is a binding, not a grant, so `netBinding()` — the one
  point every consumer resolves through (spawn env, relay, provider roster)
  — returns none under a deny, making even a pre-existing binding inert.

`mayCall` governs EXTERNAL reach only: same-scope targets (an app's own
`res:` resources, intra-app calls) are always exempt — a scope is one trust
unit (ND5), and the obvious org row `{tiles:"*", mayCall:[…]}` must not
sever an app from its own database. Pending requests a ceiling makes
unapprovable carry a `blocked` annotation so UIs grey them out.

Policy constrains the **runtime plane only** (elements). Humans are never
subject to it; terminal egress/API stay governed by the D17 user/team flags.

## Delegation (D21) and the frame-token boundary

Org admins manage their org — name, members, co-admins, base permission,
teams, per-tile access entries — all org-clamped. Workspace-admin-only: org
create/delete, policy rows, team `termApi`/`termNet`.

They act **as signed-in humans** (cookie principal). Two consequences:

- A frame principal (a tile they're driving) never inherits org-adminship —
  the standing "tiles act as themselves" rule.
- Their UI surface is **workspace chrome**, *not* the admin tile: granting
  a non-workspace-admin read on `tiles/admin` would mint them its frame
  token and hand them the tile's own `xbin` capabilities. Chrome surfaces:
  the per-tile ⚙ access panel AND the shell's "orgs & teams" popover
  (`bx-org-admin`: members, co-admins, base permission, team CRUD — term
  flags render only for workspace admins). The admin tile's "orgs & teams"
  tab (with the structured policy-row editor) is the workspace-admin
  console.

whoami's driving-user view on element principals is scoped by tile trust so
a low-trust tile can't harvest memberships: `{id,name}` for every tile; the
tile's own org's slice for org tiles; the full list (+admin flag) only for
xbin/xbin:users-capable elements — a policy `xbin-caps` deny downgrades the
view along with the capability.

## Reserved segments & migration

`o`/`u` are rejected in NEW tile paths everywhere tiles come from (create,
clone, git import, builtin import, template instantiate; `bx new` routes
marker paths through the API so the same validation applies). Existing dirs
are grandfathered — never rejected at load; `bx doctor` warns (unknown org,
stray markers, inert team patterns). Migration note:
`docs/changes/2026-07-11-orgs-and-teams.md`.

## Surfaces

- **API** (protocol.md): `/orgs` CRUD, `/orgs/{org}/teams` CRUD, `/access`
  (per-tile ACL view with provenance: exact | pattern:<pat> | base; PUT for
  exact entries), `/policy` + `/orgs/{org}/policy`, `whoami.orgs` +
  `whoami.user` (attributed human on element principals, so chrome tiles
  like the manager can offer the create-in-team picker without raw fetch).
- **CLI**: `bx org|team|access`, `bx org policy`, `bx new --team`,
  memberships in `bx user ls`, doctor warnings.
- **UI**: admin tile "orgs & teams" tab with the policy-row editor
  (ws-admin console); shell ⚙ → "access" section (also for org admins;
  other sections degrade to "workspace-admin only"); the shell "orgs &
  teams" popover for delegated org admins; org-grouped sidebar; manager
  create-in-team picker (prefix pinned from the team's create patterns).

## Non-goals (this pass)

- Per-user tiles (`u/<user>/…`) — the segment is reserved, nothing else.
- Nested teams, team maintainers (GitHub has them; add if wanted later).
- Org-scoped vaults/resources — resources stay scope/workspace-shaped.
- Write-time validation of team patterns (the evaluation clamp is the
  guarantee; doctor covers the UX).
- One-step "move tile into an org" (adopt = clone under the marker or
  host-side mv; documented in docs/auth.md).
