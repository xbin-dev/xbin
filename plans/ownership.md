# Ownership, orgs v2, delegated approvals — design

Rewrite of the multi-user grouping model (supersedes `plans/orgs.md`, D19–D21).
No production workspace used orgs/teams or `o/` paths yet, so this is a clean
replacement — **no data migration**, only doc/decision supersession.

**The moves** (decided 2026-08-02):

1. **Ownership, not position.** Tile↔org binding by path marker (`o/<org>/…`)
   is abolished. The workspace keeps one global tile namespace (NPM-style);
   every component has an **owner** — a *user* or an *org* — recorded outside
   the workspace and **transferable**. Permissions hang off ownership, not the
   path.
2. **Orgs without teams.** The team indirection is removed. An org is a flat
   member list; each member carries **org-wide** permissions (level + create +
   admin). Sharing beyond the org is per-tile ACL entries (user *or org* →
   level), same as before minus teams.
3. **Delegated self-approval, ws-admin-decided all the way.** Workspace
   admins can grant an org an **allowance**: grant/binding target classes its
   org admins may approve *themselves* on org-owned tiles. **Any** system
   target is allowable — `net:internet`, `net:host`, `gpu:*`, `res:*`,
   `cap:containers`, `cap:net-admin`, ingress publication — a high-trust dev
   workspace can hand an org the full kit. The single irreducible exception
   is the `xbin`/`xbin:*` capability family (see D26: it isn't a trust knob,
   it's model coherence). Ceilings (D20) still evaluate on top; deny wins.
4. **Permission sets.** Org permissions (allowances + ceiling rows + member
   term flags) are packaged as named, reusable **permission sets** attached
   to orgs by reference — multi-org management is "attach `dev-hightrust`",
   not per-org checkbox surgery.

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
  security-cap idea.
- **D26 — org allowances (delegated approval).** Per-org **allowance** =
  target patterns org **admins** may approve on **org-owned** tiles, set only
  by ws-admins (directly or via permission sets, D28). *Everything is
  allowable* — including `net:host`, `cap:*` and ingress publication — the
  trust dial is entirely the ws-admin's. Two structural rules, not trust
  knobs:
  - **`xbin` / `xbin:*` are never allowable.** An element granted `xbin@admin`
    *is* a workspace admin (`broker.IsAdmin`, broker.go:379) — an org admin
    who could self-approve it would transitively be a workspace admin, and
    the delegation boundary dissolves. Workspace-governance capability grants
    stay ws-admin approved, always.
  - **Intra-org wiring needs no allowance.** A grant whose caller *and*
    target are owned by the same org (tile→tile API calls; `res:` targets
    whose scope root is an org-owned component dir) is org-admin approvable
    inherently — it's the org wiring its own property, the ownership-model
    successor of the same-scope auto-grant's spirit. Allowances govern
    *system* resources and anything crossing the org boundary.
  Approval always re-runs the ceiling check (deny beats allow) and is audited
  as the actual approver.
- **D27 — default tile visibility + the organisations tile.** The store gains
  a workspace-level `defaultTiles` pattern→level map applied to *every* user
  (ws-admin managed). The scaffold seeds `apps/welcome`, `tiles/apidocs`,
  `tiles/organisations` → `read`, which is how non-admin users can see the
  delegated surface at all (and fixes "new users see nothing / admin tiles").
- **D28 — permission sets.** A named, workspace-level bundle
  `{allow[], policy[], termApi?, termNet?}` attached to orgs **by reference**
  (an org lists `sets: [...]`; multiple allowed). Editing a set updates every
  attached org — that's the point. Composition: effective allowance =
  ∪(all attached sets' `allow`) ∪ the org's own extras; effective ceiling =
  restrictive union of workspace rows + every attached set's rows + org rows
  (any deny wins — so a set can also *impose* restrictions fleet-wide);
  member term flags = user flag ∨ any attached set's flag. Sets are
  ws-admin-only to create/edit/attach; deleting a set attached to any org is
  refused (detach first — explicit beats silent). Org admins see their
  resolved capabilities read-only.

### Allowance target grammar (D26/D28 `allow` entries)

Entries are matched against the target being approved, using the same
patterns as grants/policy (`*` glob):

```
res:<scope>/<name> | res:*        resource grants (cross-org / parent-scope)
gpu:<idx|uuid> | gpu:*            GPU grants
cap:containers | cap:net-admin | cap:*   reserved sandbox capabilities
net:internet | net:lan:<cidr> | net:host | net:provider:<tile-pattern>
                                  net-interface bindings by bound target
iface:<service>                   http/stream interface bindings to providers
                                  OUTSIDE the org (intra-org needs none)
ingress:host:<pattern>            expose publication for matching hostnames
ingress:zone:<pattern>            …delegated wildcard zones
ingress:listen:<port|lo-hi>       …stream/host-port publication in range
tile:<pattern>                    cross-org/workspace tile-API call grants
```

`xbin`, `xbin:*` never match — rejected at set/allowance write time with the
escalation explanation, and ignored defensively at evaluation.

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

  "permissionSets": {                                          // D28
    "dev-hightrust": {
      "allow":  [ "net:internet", "res:*", "gpu:*", "cap:containers",
                  "ingress:zone:*.dev.example.com", "ingress:listen:20000-20999" ],
      "policy": [ { "tiles": "*", "deny": [] } ],
      "termNet": true
    },
    "locked-down": { "allow": [], "policy": [ { "tiles": "*", "deny": ["net","gpu"] } ] }
  },

  "orgs": [ {
    "id": "sales", "name": "Sales", "created": 1786...,
    "members": [                                               // D25 — flat
      { "id": "alice", "level": "terminal", "create": true, "admin": true },
      { "id": "bob",   "level": "write" }
    ],
    "tiles": { "apps/shared-dash": "read" },  // tiles shared TO this org
                                              // (exact paths; mirror of user.Tiles)
    "sets":  [ "dev-hightrust" ],             // D28 — referenced permission sets
    "allow": [ ],                             // per-org extras on top of sets
    "policy": [ /* org ceiling rows — scoped to org-OWNED tiles */ ]
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
Term flags: user flag ∨ any attached permission set's flag for orgs the user
belongs to (D28) — the group mechanism teams used to provide.

### Lifecycle edges

- **Create** — *every* creation path sets ownership: `POST /create`
  (create.go:27), `POST /clone` (clone.go:28), `POST /git/import`
  (gitimport.go:115), `POST /builtins/import` (tiles.go:42), and
  `POST /templates/new` (templates.go:66) all gain
  `owner: "user:<self>" | "org:<id>"` (default `user:<self>` for non-admin
  callers, workspace-owned for admins unless specified). Org create requires
  membership + `create`. `ValidateNewTilePath` (policy.go:87) becomes
  `ValidateNewTile(path, owner)` — drops the `o`/`u` positional checks, keeps
  reserved names, validates the owner ref.
- **Transfer**: `POST /owner {tile, to}` — allowed for ws-admin always; for
  the current user-owner (target org must be one they're a member of); for
  org admins of the owning org (to another org they admin, or to a member).
  ACL entries are untouched by transfer; derived levels follow the new owner.
- **Delete user**: their owned tiles fall to **workspace-owned** (logged;
  `bx doctor` lists them). Delete org: **refused while it owns tiles** —
  transfer first (explicit beats silent orphaning); detaching its sets and
  dropping its shares is part of the delete.
- **Rename/move** of a component dir orphans its `owners` entry → the tile
  becomes workspace-owned; doctor warns. (Moves are an admin-plane operation
  already; acceptable, documented.)
- **Backup/restore**: ownership lives in `data/users.json`, not the
  workspace, so restoring a deleted tile does *not* resurrect its ownership —
  it comes back workspace-owned; doctor flags it next to the orphan check.

## Delegated approval — exact semantics (D26 + D28)

Chokepoint changes in the broker, `apiGrantsAdd` (broker.go:474) +
`apiBindingSet` (netfn.go:448):

```
may_approve(p, tile, target):
  ws-admin(p)                                   → yes (as today)
  p is human org admin of O, owners[tile]==org:O:
    target is intra-org (both endpoints org-owned)          → yes (audited)
    target ∉ {xbin, xbin:*}
      and target matches resolvedAllow(O)       # sets ∪ extras
      and ceiling(tile).permits(target)         # D20 still evaluated
                                                → yes (audited as p)
  else                                          → 403 / stays pending
```

- Pending routing: a pending grant/binding whose (tile, target) an org could
  approve is **visible to that org's admins** (new filtered views of
  `GET /grants` / `GET /bindings` — today both are ws-admin-only; the
  organisations tile renders them with one-click approve/deny).
- Evaluation-time ceilings are unchanged — an allowance can't out-permit a
  deny row (allow ∧ ceiling, deny wins; a permission set's own `policy` rows
  participate in that restrictive union).
- Exposes: `apiBindingSet`'s Host/Zone/Listen forms match the
  `ingress:host:/zone:/listen:` allowance entries; anything not matched stays
  ws-admin.
- Allowance/set edits are ws-admin-only, audited.

## Runtime changes by package

- **`internal/users`** (the bulk): drop `Team`/team CRUD/`OrgOf`/positional
  validation; new `owners` map + CRUD (`Owner(path)`, `SetOwner`, orphan
  handling), `Org.Members` with the D25 triple, `Org.Sets`+`Org.Allow`,
  `PermissionSet` CRUD with attach/detach + delete-refusal, `defaultTiles`;
  rewrite `Access` (resolution above, incl. set-conferred term flags) and
  `Ceiling(path)` (org rows + attached-set rows keyed by ownership);
  `ResolvedAllow(org)` + `AllowanceCovers(org, target)` with the grammar
  above and the `xbin` rejection. All table-testable, no HTTP/broker imports
  (unchanged layering).
- **`internal/auth`**: no shape change — `Principal.Access` keeps working;
  the resolver's internals changed only.
- **`internal/broker`**: ownership at all five creation entry points;
  `/owner` transfer endpoint; `/access` GET/PUT rewritten to exact
  `user:`/`org:` entries (org entries land in `org.Tiles`, user entries in
  `user.Tiles`, writable by ws-admin, the tile's owner, and org admins of the
  owning org); `apiGrantsAdd`/`apiBindingSet` gain the D26 path (incl.
  intra-org + expose matching); pending lists gain the org-admin filtered
  view; access-matrix provenance kinds become
  `owner | org-member | org-share | user-exact | pattern | default`;
  `whoami` reshaped (`orgs: [{id, name, level, create, admin}]`,
  `owned: […]`, `resolvedAllow` for orgs the user admins); new minimal
  `GET /users-directory` (id+name only) so org admins can add members
  without the admin-only `GET /users`; permission-set CRUD endpoints
  (`GET/PUT /permission-sets`, attach via org PATCH).
- **`cmd/bx`**: `bx team` removed; `bx org` reworked
  (`member add|rm|set <org> <user> [--level] [--create] [--admin]`,
  `sets +name|-name`, `allow +target|-target` — ws-admin); new
  `bx permset ls|add|set|rm <name> [--allow …] [--policy …] [--term-net]`;
  new `bx owner <tile> [--transfer to]`; `bx access <tile> set user:bob=write
  org:sales=read`; doctor: orphaned owners, `xbin` targets in allowances
  (defense-in-depth), org-delete blockers, sets attached to missing orgs.
- **`cmd/xbind` / scaffold**: seed `defaultTiles`; ship `tiles/organisations`
  in `workspace-template`.

## UX changes

**Admin tile (ws-admin surface)** — user management group becomes
*users · organisations · permission sets · access map* (teams tab deleted):
- *permission sets*: the set editor — allow-target chips (with the
  `xbin` rejection inline), ceiling-row editor, term-flag toggles, and an
  "attached to N orgs" list; attach/detach from either side.
- *organisations*: org cards — member table with the three-knob role editor
  (presets Admin/Developer/Viewer), attached-sets picker + resolved-allowance
  preview, owned-tiles list (with transfer), shared-to-org entries, per-org
  extra allow/policy rows. Org delete disabled while owning tiles, with the
  reason shown.
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
  intra-org/allowance rules (one-click approve/deny), and a read-only
  "what this org may self-approve" card (resolved sets + ceilings).
- **Not here** (ws-admin only, by server gate): set/allowance/ceiling
  editing, org create/delete, term flags outside sets.

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
  (ownership, roles, allowances + grammar + the `xbin` floor rationale,
  permission sets), `docs/protocol.md` (every endpoint above; `/orgs` shape
  change is **breaking** → changelog +
  `docs/changes/2026-08-XX-ownership.md` noting teams/`o/`-paths removal and
  that no released workspace used them), `plans/DECISIONS.md` D24–D28 with
  D19/D21-semantics superseded notes, banner on `plans/orgs.md`.
- Tests: `users` resolver table tests (ownership/org-level/share/pattern/
  default precedence, transfer effects, delete-user orphaning, org-delete
  refusal, set-conferred term flags), ceiling-by-ownership incl. set rows,
  allowance gate (covered target on org-owned tile by org admin ✓; dev ✗;
  non-org tile ✗; `xbin` listed → write-rejected AND eval-ignored ✓;
  deny row beats allowance ✓; intra-org grant needs no allowance ✓;
  cross-org same-target ✗ without `tile:` entry), set attach/detach/delete
  refusal, ingress host/zone/listen matching, create-as-org across all five
  entry points, directory endpoint privacy; integration: full multi-user
  scenario rewrite (member sees org tiles by role, dev creates org tile +
  self ACL, org admin approves an allowed `net:internet` binding end-to-end,
  `cap:containers` approvable only after the ws-admin attaches a set carrying
  it, `xbin` never).

## Build order

1. `internal/users` — model + resolver + ceilings + allowances + sets
   (+tests). The whole semantic core, zero deps.
2. Broker enforcement — ownership at the five creation points, transfer,
   access rewrite, D26 approval path incl. intra-org + exposes (+tests,
   protocol.md).
3. API surface + `bx` (+doctor), whoami/directory/permission-sets.
4. UI — admin tile rework (incl. sets editor), `tiles/organisations`,
   manager owner picker, shell retirement of bx-org-admin + seed filtering.
5. Docs wrap-up + DECISIONS + changelog/migration note.

Step 1–2 land together behind nothing (no compat constraint — the old model
has no users); UI can trail by a commit without breaking anyone.

## Code audit — findings & gaps (2026-08-02)

Audit of the shipping code against this design; each finding is folded into
the sections above, listed here with refs so the implementation doesn't lose
them:

1. **`xbin` allowance = admin escalation (confirmed, hence the one floor).**
   `broker.IsAdmin` (broker.go:379) treats an element granted `xbin@admin` as
   a full workspace admin. Any allowance covering `xbin`/`xbin:*` would let an
   org admin mint workspace-admin elements — delegation would be circular.
   Enforced at write *and* evaluation.
2. **Same-scope auto-grants stop aligning with orgs.** `grantedRole`'s
   same-scope path (broker.go:355–371) auto-approves `uses` within a
   directory scope. Under positional naming org tiles clustered in one scope;
   with ownership they scatter, so org tiles silently lose that convenience.
   Resolution: the D26 **intra-org rule** (same-owner ⇒ org-admin approvable,
   one click in the organisations tile). Directory same-scope auto-grants
   stay as-is for genuinely co-located tiles — the two rules coexist.
3. **Five creation entry points, not three.** The original plan listed
   create/clone/git-import; `apiBuiltinsImport` (tiles.go:42) and
   `apiTemplatesNew` (templates.go:66) also create components and must set
   ownership, and `ValidateNewTilePath` (policy.go:87) is called on the
   create path only — the owner-validation replacement must cover all five.
4. **Expose bindings need their own allowance grammar.** `apiBindingSet`
   (netfn.go:448) multiplexes iface bindings *and* exposes (Host/Zone/Listen,
   netfn.go:458–475): a bare "net:*"-style class can't express "may publish
   under *.dev.example.com on ports 20000-20999" — hence the
   `ingress:host:/zone:/listen:` entries.
5. **Term flags lose their group mechanism with teams.** `Access.TermAPI/
   TermNet` (auth.go:140–159) unioned user ∨ team flags; teams' removal
   would leave only per-user flags — exactly the multi-org manual work this
   revision is against. Resolution: permission sets may carry
   `termApi`/`termNet` (D28), conferred to members of attached orgs.
6. **Pending-request routing is admin-only end to end.** `GET /grants`/
   `GET /bindings` and the shell's `bx-grants`/`bx-bindings` panels are
   ws-admin surfaces; without the filtered org-admin views (+ organisations
   tile rendering), allowances would be approvable but *invisible*. The
   filtered views are part of step 2, not a UI afterthought.
7. **Element principals must stay out of org management.** `canManageOrg`
   (orgsapi.go:48) already refuses element principals — keep that exact
   property in v2: an org-owned tile (even one granted broad capabilities)
   never manages its own org, approves grants, or transfers ownership.
   Humans approve; tiles request.
8. **Ceiling re-key keeps patterns.** `Store.Ceiling` (orgs.go:652) selects
   org rows by `OrgOf`; v2 selects by owner org (plus attached sets) but org
   rows *keep* their `tiles` patterns — matching within the owned set — so
   one org can still have differentiated ceilings across its tiles.
9. **Ownership is not in workspace backups.** Backups cover component dirs;
   `owners` lives in `data/users.json`. Restore-after-delete comes back
   workspace-owned by design — documented, doctor-flagged (lifecycle edges).
10. **Org resource sharing has no positional home anymore.** Scoped
    resources (`scope.json`, registry.go:157–163) are directory-bound; org
    tiles scattered across `apps/*` can't share a scope resource without
    grants. Covered by intra-org approval for org-owned scope roots +
    `res:*`-class allowances for the rest; genuinely *shared* org data is
    encouraged to live behind an org-owned API tile rather than raw
    resource grants (matches the existing cross-scope guidance).

## Open questions (flagged, defaults chosen)

- **Allowance exercisers**: v1 = org **admins** only. (Could later add a
  per-allowance `selfServe: devs` bit if approval traffic warrants.)
- **Org-to-org sharing granularity**: shared-to-org entries confer the entry
  level to *all* members flat (no per-role clamp) — simple and predictable;
  revisit only with evidence.
- **`defaultTiles` scope**: applies to users only (element principals are
  untouched — grants govern them).
- **Set inheritance**: sets are flat (no set-includes-set) — composition is
  the org's `sets` list; nesting can come later if fleets demand it.
