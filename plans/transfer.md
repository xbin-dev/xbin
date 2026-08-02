# Tile ownership transfer — semantics, impacts, surfaces (D39)

> 2026-08-02. Transfers existed since D24 as a bare storage move with
> membership-gated authz. This spec makes them a first-class operation:
> create-bound authorization, an impact PREVIEW before every confirm, active
> re-evaluation afterwards (a transfer changes which ceilings/allowances
> govern the tile — enforcement must bite NOW, not at the next restart),
> and complete UI surfaces. plans/DECISIONS.md D39 records the rulings.

## 1. Authorization (tightened)

"May I transfer tile T to owner X?" decomposes into a GIVE right on the
current owner side and a RECEIVE right on the target side. Both must hold.

GIVE (unchanged from D24):
- ws-admin: always;
- the user-owner of T;
- an org admin of T's owning org.

RECEIVE (new — **the bound for "may I transfer INTO X" is "may I create
in X"**):
- `org:X`: the caller holds X's **Create** knob (or org-adminship of X, or
  ws-admin). Plain membership no longer suffices — receiving a tile is
  creating one, capability-wise. *(BREAKING vs the old membership-only
  rule; migration note.)*
- `user:U`: U == caller needs the GIVE right only (taking your own tile
  back out of an org you administer); U != caller is ws-admin-only
  (gifting tiles to others stays an admin act).
- `""` (workspace): ws-admin only.

## 2. Impact preview — GET /owner/preview?tile=T&to=X

Never mutates. Returns the report both confirm dialogs render:

```jsonc
{
  "tile": "apps/bot", "from": "user:dee", "to": "org:crew",
  "allowed": true,            // the §1 check for the CALLER (+error if not)
  "callerLevel":  {"before": "terminal", "after": "write"},   // D31 resolution
  "deadBindings": [           // slots whose every ref/target the NEW owner's
    {"slot": "net", "reason": "…policy row … denies net…"}    // ceiling denies
  ],
  "deadGrants": [             // grant rows FROM T that stop resolving under
    {"target": "res:…", "role": "writer", "reason": "…"}      // the new ceiling
  ],
  "planeChanges": [           // informational: approval-plane shifts, e.g.
    "org:crew admins gain full control (terminal, lifecycle, sharing)",
    "grants from this tile become intra-org for org:crew"
  ]
}
```

Computation: evaluate T's stored bindings/grants against `Ceiling(T)` **as
if owned by X** — the store gains `CeilingFor(path, ownerRef)` (pure), and
the broker reuses `bindingTargets`/`ceilingBlockMsg` against it. callerLevel
via an Access resolved over an owners snapshot with the override.

## 3. Transfer side effects — POST /owner

After the storage move (same request, ordered):

1. **Unbind hard-dead binding slots** — a slot whose EVERY ref is
   ceiling-denied under the new owner is removed from the workspace
   manifest (state stays honest; a policy row added later shouldn't
   silently resurrect egress someone believed gone). Partial slots stay.
2. **Grants stay stored** — a ceiling-dead grant is inert at evaluation and
   renders as blocked in every grants view; revoking is the human's call
   (rows are the auditable capability table).
3. **Restart to re-materialize** — spawn-captured access (egress policy,
   res env, GPU) must reflect the new owner NOW: fire `OnGrantChange(T)`
   (and for each unbound net slot, the old provider) exactly like the
   bindings API does.
4. Publish `users` + `grants` events; respond with the executed impact
   report (same shape as preview + `"unbound": [slots]`).

## 4. Surfaces

- **Shell right-click → Create a new tile**: owner picker — *me* /
  each org where the caller holds Create/admin / *workspace* (ws-admin
  only, their default). Same semantics as the manager tile's picker.
- **Admin console**: per-tile owner reassignment (access-map/tile row →
  owner cell becomes an editor: picker + preview-confirm). Ws-admin only.
- **Organisations tile**: existing transfer flows call preview first and
  render the report in the confirm (replacing the static consequence
  copy); refuse-with-reason when §1 denies.
- **bx**: `bx owner <tile> --transfer X` prints the preview and asks
  (`--yes` skips); preview exposed as `bx owner <tile> --preview X`.

## 5. Testing strategy (exhaustive by construction)

- **users unit**: RECEIVE matrix — {ws-admin, org-admin, member+create,
  member, non-member} × {org, self, other-user, workspace} (20 cells, each
  pinned); CeilingFor override vs Ceiling parity.
- **broker unit**: preview correctness per impact class (callerLevel drop,
  dead net binding under org deny, dead grant under mayCall, plane notes);
  transfer executes unbind + fires OnGrantChange for tile AND old net
  provider; partial-dead slot NOT unbound; grants untouched; events
  published; authz matrix at the API layer (403 bodies name the missing
  right).
- **integration**: live flow — user tile with net=internet binding →
  transfer into an org whose set denies net → response lists the unbind,
  binding row gone from xbin.json, backend restarted (generation bump),
  egress policy empty; transfer-into-org by a no-Create member → 403;
  reverse transfer by org admin back to the user restores nothing
  silently (bindings stay gone until re-approved).
