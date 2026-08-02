# Ownership, orgs v2, delegated approvals — design

Rewrite of the multi-user grouping model (supersedes `plans/orgs.md`, D19–D21).
No production workspace used orgs/teams or `o/` paths yet, so this is a clean
replacement — **no data migration**, only doc/decision supersession.

**The three moves** (decided 2026-08-02):

1. **Ownership, not position.** Tile↔org binding by path marker (`o/<org>/…`)
   is abolished. The workspace keeps one global tile namespace (NPM-style);
   every component has an **owner** — a *user* or an *org* — recorded outside
   the workspace and **transferable**. Permissions hang off ownership, not the
   path.
2. **Orgs without teams.** The team indirection is removed. An org is a flat
   member list; each member carries **org-wide** permissions (level + create +
   admin). Sharing beyond the org is per-tile ACL entries (user *or org* →
   level), same as before minus teams.
3. **Delegated self-approval.** Workspace admins can grant an org an
   **allowance**: a list of grant/binding targets its org admins may approve
   *themselves* on org-owned tiles (e.g. `net:internet`) — always under the
   policy ceilings. This is the positive half D20's deny-only ceilings were
   missing.

Plus a new **pre-installed `tiles/organisations` tile** — the delegated
(non-workspace-admin) management surface.

## Decisions (to land in DECISIONS.md with the implementation)

- **D24 — component ownership.** Every component has an owner ref
  `user:<id> | org:<id>`, stored in the xbind-owned users store (never in the
  workspace — a tile terminal must not be able to edit its own ownership).
  Absent ⇒ *workspace-owned* (admin-managed; all scaffold/builtin tiles).
  Ownership is assigned at create (creator, or an org they may create in) and
  transferable. A user-owner implicitly holds `terminal` on the tile and
  manages its ACL (subsumes D16's creator auto-grant and the D23
  creator-sharing question — sharing is an ownership right now). Supersedes
  D19's positional `o/` binding; the `o`/`u` path-segment reservations are
  dropped.
- **D25 — flat org roles.** Org membership is `{id, level, create, admin}`:
  `level` (`read|write|terminal`) applies org-wide to **org-owned** tiles;
  `create` = may create new org-owned tiles; `admin` = org management
  (members, org tiles' ACLs, transfers, exercising allowances). The UI offers
  presets — **Admin** (terminal+create+admin), **Developer**
  (terminal+create), **Viewer** (read) — over these three knobs. Teams,
  `basePermission`, and per-team pattern maps are removed. Org policy-ceiling
  rows now match **org-owned tiles** (ownership lookup, not path parse).
  Supersedes the D19 team semantics; keeps D20 ceilings and D21's
  security-cap idea (below).
- **D26 — org allowances (delegated approval).** Per-org, **ws-admin-set**
  list of grant-target patterns (`net:internet`, `res:*`, `gpu:*`, `iface:…`)
  that org **admins** may approve on **org-owned** tiles. Approval still runs
  the full ceiling check (deny beats allow) and is audited with the actual
  approver. Never covers: `net:host`, `cap:*` (containers/net-admin),
  `xbin`/`xbin:*` capability roles, ingress exposes — those stay
  workspace-admin regardless of allowance content (a hard floor, not a
  default).
- **D27 — default tile visibility + the organisations tile.** The store gains
  a workspace-level `defaultTiles` pattern→level map applied to *every* user
  (ws-admin managed). The scaffold seeds `apps/welcome`, `tiles/apidocs`,
  `tiles/organisations` → `read`, which is how non-admin users can see the
  delegated surface at all (and fixes "new users see nothing / admin tiles").

## Storage (`data/users.json`, additive rewrite of the org section)

```jsonc
{
  "users": [ /* unchanged: tiles pattern→level, canCreate, termApi/termNet, admin */ ],
  "tokenLoginDisabled": false,
  "policy": [ /* workspace ceiling rows — unchanged (D20) */ ],

  "defaultTiles": { "apps/welcome": "read", "tiles/apidocs": "read",
                    "tiles/organisations": "read" },          // D27

  "owners": { "apps/crm": "org:sales", "apps/foo": "user:alice" },  // D24
                    // absent path ⇒ workspace-owned

  "orgs": [ {
    "id": "sales", "name": "Sales", "created": 1786...,
    "members": [                                               // D25 — flat
      { "id": "alice", "level": "terminal", "create": true, "admin": true },
      { "id": "bob",   "level": "write" }
    ],
    "tiles": { "apps/shared-dash": "read" },  // tiles shared TO this org
                                              // (exact paths; mirror of user.Tiles)
    "allow": [ "net:internet", "res:*" ],     // D26 — ws-admin-set allowance
    "policy": [ /* org ceiling rows — now scoped to org-OWNED tiles */ ]
  } ]
}
```

Removed: `Team`, `basePermission`, `admins[]` (folded into `member.admin`),
`OrgOf`, the `o`/`u` reserved-segment validation. Kept: everything on `User`,
workspace+org policy rows, `validID`, atomic persist.

### Resolution (`Access.TileLevel`, rewritten)

```
level(path) = max(
  ws-admin                          → terminal (as today),
  owners[path] == user:me           → terminal,
  owners[path] == org:O, me ∈ O     → my member.level,
  org O' ∋ me and O'.tiles[path]    → that entry's level (shared-to-org),
  user.Tiles pattern/exact entries  → as today (D16),
  defaultTiles pattern entries      → as today's user.Tiles semantics,
)
```

`CanCreateTile(path)`: personal creation via `user.canCreate` patterns
(unchanged); creating **as an org** is gated by `member.create` instead (the
path no longer encodes the org, so the create *request* names the owner).
Term flags: user flags only (team union is gone; org membership confers no
terminal-API/net flags — those stay per-user, ws-admin set).

### Lifecycle edges

- **Create**: `POST /create` gains `owner: "user:<self>" | "org:<id>"`
  (default `user:<self>`; admins may set any). Org create requires
  membership + `create`. The D16 creator-terminal auto-grant is **replaced**
  by ownership (derived, so it survives transfers and doesn't litter
  `user.Tiles`).
- **Transfer**: `POST /owner {tile, to}` — allowed for ws-admin always; for
  the current user-owner (target org must be one they're a member of); for
  org admins of the owning org (to another org they admin, or to a member).
  ACL entries are untouched by transfer; derived levels follow the new owner.
- **Delete user**: their owned tiles fall to **workspace-owned** (logged;
  `bx doctor` lists them). Delete org: **refused while it owns tiles** —
  transfer first (explicit beats silent orphaning).
- **Rename/move** of a component dir orphans its `owners` entry → the tile
  becomes workspace-owned; doctor warns. (Moves are an admin-plane operation
  already; acceptable, documented.)

## Delegated approval — exact semantics (D26)

Chokepoint changes in the broker, `apiGrantsAdd` + `apiBindingSet`:

```
may_approve(p, tile, target):
  ws-admin(p)                                  → yes (as today)
  p is human, org O = owners[tile] is an org,
    p ∈ O.members with admin,
    target ∉ HARD_FLOOR,                        # net:host, cap:*, xbin[:*], exposes
    target matches some O.allow pattern,
    ceiling(tile).permits(target)               # D20 still evaluated
                                               → yes (audited as p)
  else                                          → 403 / stays pending
```

- Pending routing: a pending grant/binding whose (tile, target) an org could
  approve is **visible to that org's admins** (new filtered view of
  `GET /grants` / `GET /bindings` — today both are ws-admin-only).
- Evaluation-time ceilings are unchanged — an allowance can't out-permit a
  deny row (allow ∧ ceiling, deny wins).
- Allowance edits are ws-admin-only (`PUT /orgs/{org}/allow`), audited.

## Runtime changes by package

- **`internal/users`** (the bulk): drop `Team`/team CRUD/`OrgOf`/positional
  validation; new `owners` map + CRUD (`Owner(path)`, `SetOwner`, orphan
  handling), `Org.Members` with the D25 triple, `Org.Allow`,
  `defaultTiles`; rewrite `Access` (resolution above) and `Ceiling(path)`
  (org rows keyed by ownership); `AllowanceCovers(org, target)`. All
  table-testable, no HTTP/broker imports (unchanged layering).
- **`internal/auth`**: no shape change — `Principal.Access` keeps working;
  the resolver's internals changed only.
- **`internal/broker`**: create/clone/import set ownership; `/owner` transfer
  endpoint; `/access` GET/PUT rewritten to exact `user:`/`org:` entries
  (org entries land in `org.Tiles`, user entries in `user.Tiles`, writable by
  ws-admin, the tile's owner, and org admins of the owning org);
  `apiGrantsAdd`/`apiBindingSet` gain the D26 path; pending lists gain the
  org-admin filtered view; access-matrix provenance kinds become
  `owner | org-member | org-share | user-exact | pattern | default`;
  `whoami` reshaped (`orgs: [{id, name, level, create, admin}]`, `owned: […]`);
  new minimal `GET /users-directory` (id+name only) so org admins can add
  members without the admin-only `GET /users`.
- **`cmd/bx`**: `bx team` removed; `bx org` reworked
  (`member add|rm|set <org> <user> [--level] [--create] [--admin]`,
  `allow +target|-target` ws-admin); new `bx owner <tile> [--transfer to]`;
  `bx access <tile> set user:bob=write org:sales=read`; doctor: orphaned
  owners, allowance targets hitting the hard floor, org-delete blockers.
- **`cmd/xbind` / scaffold**: seed `defaultTiles`; ship `tiles/organisations`
  in `workspace-template`.

## UX changes

**Admin tile (ws-admin surface)** — user management group becomes
*users · organisations · access map* (teams tab deleted):
- *organisations*: org cards — member table with the three-knob role editor
  (presets Admin/Developer/Viewer), owned-tiles list (with transfer),
  shared-to-org entries, **allowance editor** (target chips + the hard-floor
  note), org ceiling rows (unchanged editor). Org delete disabled while
  owning tiles, with the reason shown.
- *users*: gains an "owned tiles" column + transfer shortcut; `defaultTiles`
  editor lands here (small "defaults for every user" card).
- *access map*: provenance chips updated to the six kinds above.

**`tiles/organisations` (new, pre-installed, D27)** — the delegated surface,
built like the admin tile (raw fetch = cookie user; server 403s are the
backstop; sections render by capability):
- **Everyone**: "my orgs" (role shown), org-owned tiles they can reach, "my
  tiles" (owned by me) with per-tile ACL editing (owner right), "new tile"
  (owner picker: *me* / orgs where `create`).
- **Org admins additionally**: member management (add via users-directory,
  role knobs), org tile ACLs + transfers, **pending approvals** covered by
  the allowance (one-click approve/deny), read-only view of the org's
  allowance + ceilings ("what this org may self-approve").
- **Not here** (ws-admin only, by server gate): allowance/ceiling editing,
  org create/delete, term flags.

**Shell**: retire `bx-org-admin.js` (the ⚑ popover) — the button now opens
`tiles/organisations` as a tile; one delegated surface instead of two
half-duplicates (closes an audit gap). Seeded first screens filter by the
viewer's access (fixes non-admins landing on `tiles/admin`).

**Manager tile**: "create in team" select → **owner** select (*me* /
eligible orgs), sending `owner` in the create body.

**bx-tile-admin (⚙ access section)**: shows the owner (+ transfer for those
allowed), exact user/org entries editable by ws-admin *or* the owner-side
principals per above.

## Docs & tests

- Docs (same change, per repo rules): `docs/auth.md` §orgs rewritten
  (ownership, roles, allowances, hard floor), `docs/protocol.md` (every
  endpoint above; `/orgs` shape change is **breaking** → changelog +
  `docs/changes/2026-08-XX-ownership.md` noting teams/`o/`-paths removal and
  that no released workspace used them), `plans/DECISIONS.md` D24–D27 with
  D19/D21-semantics superseded notes, banner on `plans/orgs.md`.
- Tests: `users` resolver table tests (ownership/org-level/share/pattern/
  default precedence, transfer effects, delete-user orphaning,
  org-delete refusal), ceiling-by-ownership, allowance gate (covered target
  on org-owned tile by org admin ✓; non-admin ✗; non-org tile ✗; hard-floor
  target ✗ even when listed; deny row beats allowance ✓), create-as-org,
  directory endpoint privacy; integration: full multi-user scenario rewrite
  (member sees org tiles by role, dev creates org tile + self ACL, org admin
  approves an allowed `net:internet` binding end-to-end, ws-admin still
  required for `net:host`).

## Build order

1. `internal/users` — model + resolver + ceilings + allowances (+tests).
   The whole semantic core, zero deps.
2. Broker enforcement — ownership at create/clone/import, transfer, access
   rewrite, D26 approval path (+tests, protocol.md).
3. API surface + `bx` (+doctor), whoami/directory.
4. UI — admin tile rework, `tiles/organisations`, manager owner picker,
   shell retirement of bx-org-admin + seed filtering.
5. Docs wrap-up + DECISIONS + changelog/migration note.

Step 1–2 land together behind nothing (no compat constraint — the old model
has no users); UI can trail by a commit without breaking anyone.

## Open questions (flagged, defaults chosen)

- **Allowance exercisers**: v1 = org **admins** only. (Could later add a
  per-allowance `selfServe: devs` bit if approval traffic warrants.)
- **Org-to-org sharing granularity**: shared-to-org entries confer the entry
  level to *all* members flat (no per-role clamp) — simple and predictable;
  revisit only with evidence.
- **`defaultTiles` scope**: applies to users only (element principals are
  untouched — grants govern them).
