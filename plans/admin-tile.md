# Admin tile — design

A privileged tile (`tiles/admin`) that is a full owner-console into the
running workspace: vaults (password-manager style), roles & grants, cron,
and an auth/system overview. It works by holding an **admin capability**,
not by being special-cased in buxond — so the same mechanism is available
to any tile the owner chooses to trust, and revoking one grant disarms it.

## 1. The capability

Extend the reserved management target `buxon` (already used for
`buxon:writer` = "create components", held by the Tile Manager) with a
second role:

| grant | capability |
|-------|-----------|
| `buxon:writer` | create components (`POST /create`) |
| `buxon:admin`  | full workspace administration; `admin ⊃ writer` so it also creates |

`tiles/admin` requests `{"target":"buxon","role":"admin"}` in its `uses`;
the template pre-approves it in the workspace grant table (like the Tile
Manager's writer grant). Remove that row → the tile's calls 403 and its
request reappears in the grants panel. This keeps the honest story: an
admin-capable element is **owner-equivalent for management** (it can read
every secret and rewrite the grant table), and that power is exactly one
visible, revocable grant — nothing hidden.

## 2. Server-side changes (least surprising, capability-gated)

Today several management endpoints are hard-gated `owner only`. Generalize
them to **owner OR `buxon:admin`** via one predicate.

- Broker gains `IsAdmin(p) = p.Owner || grantedRole(p.Component,"buxon") ⊇ admin`.
- The `server.Server` gets an injected `IsAdmin func(auth.Principal) bool`
  hook (nil ⇒ owner-only), set by the broker — same pattern as `Policy` /
  `BusFilter`, so `server` keeps no dependency on the grant table.

Endpoints moved from owner-only → admin-capable:

| endpoint | change |
|----------|--------|
| `GET /grants`, `POST /grants`, `DELETE /grants` | admin may view/approve/revoke |
| `GET /status`, `GET /backends` | admin may read system state |
| `GET /cron/jobs`, `DELETE /cron/jobs/{name}?component=` | admin sees & manages **all** jobs |
| vault `GET/PUT/DELETE /vault/<comp>/<key>` | owner-or-self **or admin** |

New read/aggregate endpoints (admin-capable):

| endpoint | returns |
|----------|---------|
| `GET /vaults` | `[{component, keys:[…]}]` for every vault (names only; values via the per-key GET) |
| `GET /resources` | declared resources across scopes + workspace: `[{id:"res:…", type, scope}]` |
| `GET /auth-overview` | one call powering the overview tab: components (with roles), grants, pending, terminals, backend states, counts |

`create` already accepts `buxon:writer`; admin implies writer, so no change.

Vault values: admin can read them (owner already can). That is the point —
password-manager management — and it's why `buxon:admin` is a heavy grant.

## 3. The tile (`tiles/admin`)

Single-page Lit view, dense, themed, tabbed. All calls via `buxon.fetch`
(admin identity attributed by frame token). Read-often, so it refreshes on
the `grants`/`reload` event stream rather than polling.

Tabs:

1. **Overview** — the system at a glance from `GET /auth-overview`:
   counts (components, exposed APIs, grants, pending, terminals, healthy/
   failed backends); a principals table (each component: runtime, roles it
   exposes, what it's granted, backend state); pending-grant callouts.
2. **Vault** — password-manager UI. Left: components with vaults (from
   `GET /vaults`); right: their keys, each value hidden behind a
   reveal/copy control (`GET /vault/<c>/<k>` on demand), inline edit, add,
   delete. Never renders a value until explicitly revealed.
3. **Roles & Grants** — every `expose.roles` (with descriptions) and the
   full grant table; approve pending, revoke existing, add arbitrary grants
   (component/role pickers built from the components list). This supersets
   `<bx-grants>` (which stays as the lightweight owner panel on root).
4. **Cron** — all jobs (`GET /cron/jobs` as admin): component, schedule,
   path, role, resource; delete; "run now" is out of scope v1 (would need a
   trigger endpoint) — noted as a follow-up.

Layout uses the theme tokens; fits the shell card like other tiles but is
most useful opened full-page (⤢).

## 4. Safety notes (documented in the tile + auth.md)

- The tile is same-origin but iframe-isolated; only its own frontend calls
  carry its admin frame token (attribution, per auth.md). A different tile
  cannot borrow it.
- `buxon:admin` = every secret + grant-table control. The tile's `API.md`
  and the auth doc state plainly that granting it equals trusting the tile
  as the owner for management. It is pre-granted only to the shipped
  `tiles/admin`; treat new grants of it as you would a root shell.
- Nothing new is exposed to *unprivileged* elements: every generalized
  endpoint still denies principals without the grant, exactly as before.

## 5. Build order

1. Broker `IsAdmin` + server `IsAdmin` hook; flip the four owner-only gates.
2. New endpoints: `/vaults`, `/resources`, `/auth-overview`.
3. `tiles/admin` UI (overview → vault → roles → cron) + `buxon.json`
   (`uses` buxon:admin) + `API.md`.
4. Pre-grant in the template workspace manifest; pin on root.
5. Docs: protocol.md endpoints, auth.md capability + warning, AGENTS.md.
6. Verify: admin tile reaches everything; an ungranted tile 403s on all of
   it; revoking the grant makes the tile's calls fail.
