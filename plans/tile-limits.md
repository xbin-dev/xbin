# Tile resource limits (D203): design and implementation plan

> Status: live — being built (D203); the owner's decisions below are settled.

Built on master 5f78b902 (v0.3.73). The design pass checked every claim
against the code; §0 has the corrections. **§P (Phase 0) below is what is
already on `limits/base`; the workers build on it.**

## Owner decisions (2026-10-27)

- **The ask:** "tiles should be able to request additional resources, in
  manifest speccing minimums IFF those are bigger than defaults, that
  (org)admins would approve, also when tiles are eg out of disk, a prompt
  should allow admins to raise disk cap by some % as long as theres space
  in the workspace. Management should be possible in Admin UI at all
  times."
- **Resources:** backend memory, the data disk quota, tile sandboxes and
  VM size. Not pids.
- **Approvers:** workspace admins always; org admins for their org's
  tiles, up to a **per-tile** ceiling workspace admins set per org.
- **Raise step:** +50 % pre-selected, 25/50/100 offered; a choice that
  doesn't fit is greyed with the reason.
- **Fit counts only the granted extra:** `max(0, quota − max(usage,
  default))` per other tile; the same rule for memory.
- **Release:** the next one after v0.3.73.

## §P. Phase 0 (on `limits/base`): the contract

- **Moves for the size budgets** (pure, no behaviour change):
  - `boot.stepCgroup` and `stepLimitAlerts` → `internal/boot/limits.go`
    (`stepCgroup`'s log line now logs the configured `memMax`);
  - diskmon's deployment-namespace section → `internal/broker/diskdeploy.go`;
  - registry's interface/expose validation → `internal/registry/iface.go`.
- **`internal/limits`** (pure, tested):
  - `Kind` with `MemMiB, DiskGiB, VMMemMiB, VMVCPUs, SbxMemMiB, SbxVCPUs,
    SbxDiskGiB`, plus `Kinds`, `Meta` (label, unit, step, bx alias,
    manifest path, min), `Parse` (name or alias), `CPU()`;
  - `ParseSize`, `FromBytes`, `ToBytes`, `ParseValue`, `Format`;
  - `Decision`, `Entry{Ask, Set}`, `Raised`, `Effective`, `Pending`;
  - `Hit`, `Book{Limits, Hits}`, `File{Schema, Tiles, Scopes}` — the
    `data/tile-limits.json` format;
  - `Pcts`, `DefaultPct`, `Step`, `Choice`, `Choices`, `Capacity`
    (`WithReserve`, `Room`, `Fit`), `Promise`, `ReserveFraction`;
  - `Action` (`ActionRaise`), `ApproverAdmin`, `ApproverOrg`.
- **Broker seams** (`internal/broker/tilelimits.go`; W1 implements the
  store behind them, W2 calls and wires them):
  - `LimitSources{Default, Max, Usage, Capacity, Apply}` +
    `Broker.SetLimitSources` — the runtime's side, every kind but diskGiB
    (the broker's own disk monitor answers that);
  - `Broker.LimitGrant(key, kind) int64` — what decisions raise a kind
    to, 0 for nothing; the runtime applies `max(default, grant)`;
  - `Broker.RecordLimitHit(key, kind, limits.Hit)` — sticky, coalesced,
    never calls out;
  - `broker.Alert.Action *limits.Action` (additive JSON `action`);
  - `diskMon.kick()` — one rescan now (a raised quota lifts its 507).
- **Phase 1 additions (the lead, W2):**
  - `LimitSources.Applied(tile, k) int64` — what runs now when it differs
    from the grant (0: the same, or unknown): the row's `applied` /
    `appliesAt`. `Default("", k)` answers the workspace's default. Boot
    calls `RefreshLimitAsks()` right after `SetLimitSources`.
  - Lock rule (W2): `Usage`, `Capacity`, `Applied` and `Apply` reach into
    the runtime, which asks `LimitGrant` (through `LimitsFor`, `VMGrant`,
    tilesbx `Granted`): call them with no store lock held. `Default` and
    `Max` are lock-free (tilesbx reads an atomic snapshot of its policy),
    so the store may ask them under its lock, and tilesbx calls
    `LimitGrant` under its `m.mu`.
  - A hit's `Count` is 1 per report (one exit, one poll, one refusal);
    `Deployment` is empty for the primary's (the tile's), set for any
    other deployment's; a person's partition's sets `Partition` alone.

## §W. The wire (binding for W1 and W3)

Every value is an integer in its kind's unit; times are RFC 3339. Routes
are under `/api/xbin`. Errors are `server.WriteError`'s `{error, doc?}`.

**`GET /limits`** — rows for every tile the caller may see (workspace
admin: all with a backend, a scope root or sandboxes/VM; org admin: the
org's; a person: tiles they own or manage; a tile's own credentials: its
own row):

```jsonc
{
  "tiles": [Row, …],
  "defaults": {"memMiB": 2048, "diskGiB": 50, …},   // the workspace defaults (a tile's own may differ: sbx*)
  "enforced": {"memMiB": true, …},                   // false: no cgroup delegation, no VM runtime, …
  "orgCeilings": {"acme": {"memMiB": 4096, …}},      // orgs the caller admins (workspace admin: every org with one)
  "canSetCeilings": true                             // workspace admin
}
```

**Row:**

```jsonc
{
  "tile": "apps/foo",
  "scope": "apps", "scopeKey": "apps", "rootsScope": false,
  "org": "acme",                       // the owning org, when org-owned
  "limits": {                          // only kinds that apply to the tile (enforced, placed)
    "memMiB": {
      "effective": 3072, "default": 2048, "max": 65536, "enforced": true,
      "ask": 4096,                     // the manifest's standing ask above the default (absent: none)
      "pending": true,                 // limits.Pending
      "set": Decision,                 // the raise/admin-set slot, when held
      "approved": Decision,            // the manifest-ask slot, when held
      "dormant": true,                 // the approved ask no longer applies (the manifest dropped it)
      "usage": 1800,                   // when known
      "applied": 2048,                 // what runs now, when it differs (a lower value waits)
      "appliesAt": "next-start",       // when applied ≠ effective, or a VM kind
      "hit": Hit,                      // at the cap, sticky
      "note": "ignored: …",            // a placement note (limits.disk off the scope root, …)
      "approvers": ["workspace-admin", "org:acme"],
      "canDecide": true,               // the viewer may approve/raise this kind at some value
      "canRevert": true,               // the viewer may DELETE (managers, approvers)
      "choices": [Choice, …]           // approvers only: +25/+50/+100 from effective
    }
  }
}
```

**`GET /limits?tile=apps/foo`** → `{"tile": Row, "capacity": {"memMiB":
limits.Capacity, …}}` (capacity: approvers only). Readers and the tile's
own credentials get the row read-only (no choices).

**`GET /limits?pending=1`** — what the shell's prompts read:

```jsonc
{"pending": [{
  "tile": "apps/foo", "kind": "memMiB", "unit": "MiB",
  "why": "ask" | "cap",
  "ask": 4096,          // why=ask
  "hit": Hit,           // why=cap
  "effective": 2048,
  "approvable": true,   // the viewer may decide it (the ask's value / some choice)
  "mine": false,        // the viewer owns or manages the tile (shown even when not approvable)
  "approvers": ["workspace-admin"]
}]}
```

**`POST /limits`** `{tile? | scope?, kind, value, source, restart?,
force?}` → `200 {"tile": Row}`. `scope` (a scope path) for `diskGiB`
only; `tile` for diskGiB means its scope (must root it). 400: bad kind or
value, `value ≤ default` ("use DELETE to go back to the default"), past
max, `source: manifest-ask` above the manifest's ask; 403: not an
approver, above the org's ceiling, the tile's own credentials, `force` by
a non-workspace-admin; 409: doesn't fit (the `why`), unless `force` by a
workspace admin; 500: `data/tile-limits.json` unreadable (never
overwritten).

**`DELETE /limits?tile=&kind=`** (or `scope=`) → `200 {"tile": Row}`:
both slots back to the default; a standing ask is pending again.

**`POST /limits/dismiss?tile=&kind=`** (or `scope=`) → `204`: an
approver clears the hit for everyone. An alert's `dismiss` names exactly
this route (`/limits/dismiss?tile=apps%2Ffoo&kind=memMiB`).

**`PUT /orgs/{org}/limits`** `{memMiB?: n|null, …}` → `200 {"limits":
{…}}` (workspace admins; null or 0 removes a kind). `GET /orgs` carries
each org's `limits`.

**Events:** `{type: "limits", component: "apps/foo"}` on `/ws/events`
after any decision, hit or new manifest ask (no payload; clients refetch).

**Alerts** (`GET /alerts`): an alert about a cap carries `action`
(`limits.Action`): `{action: "raise-limit", tile, scope?, kind, unit,
current, approvers, choices}` — `choices`' `approvable` and the alert's
`dismiss` decorated for the viewer (`dismiss` only for approvers). Kinds:
`oom` (sticky, from a hit), `quota` (crit, at the quota) and the new
`quota-near` (warn, from 90 %), `sbx-cap` (a sandbox refused/stopped at
the tile's total), `tile-limits` (admins: the store unreadable), and
`memory` (warn, passing: a backend's leaf at its memory limit without an
OOM kill — the old `oom` warning's case that no longer claims a kill).
The disk monitor's `quota`/`quota-near` actions name the scope's path and
its root tile ("" when none roots it) and carry `current` in GiB; the
workspace scope's alert has no action (no API path names it).

**`GET /xbin/limits[?tile=]`** — the xbind-served page (top level only,
like `/xbin/partitions`): `web/limits.html` + `/vendor/limits-page.js`.

## Worker ownership (Phase 1)

To keep the three branches mergeable, each file has one owner; a worker
that needs a change in another's file says so in its report instead.

- **W1 (store, API, CLI):** `internal/broker/tilelimits*.go` (except the
  seams' signatures), `apiAlerts` in diskmon.go (decoration only),
  `internal/registry/limits.go` + the manifest field,
  `internal/users/orglimits.go` + `Org.Limits`, `orgsapi.go`'s route,
  `internal/server/openapi_limits.go`, `limitspage.go`, the `/xbin/limits`
  route, the one `RefreshLimitAsks` line in boot's `serve.go` watchLoop,
  `cmd/bx/limits.go`, and `docs/protocol.md` entries for its routes
  (apicheck needs them).
- **W2 (runtime, detection):** `internal/cgroup`, `internal/runner`,
  `internal/deployments` (ceilings), `internal/tilesbx`, `internal/vm`,
  diskmon's `scan`/`Status`/alerts (not `apiAlerts`), `internal/boot`
  (`stepTileLimits`, `stepLimitAlerts`), `test/limits/`.
- **W3 (UI):** `web/limits-kit.js`, `web/bx-limits.js`,
  `web/limits-table.js`, `web/limits.html`, `web/limits-page.js`,
  `web/bx-requests.js`, `web/bx-dismiss.js`, `workspace-template/shell/*`,
  `workspace-template/tiles/admin/*`, `hack/limits.test.mjs`.
- **Phase 2:** the docs, the harness pass, merges.

## 0. Corrections to the brief, and constraints that shape the design

**Most of the brief checks out.** These references are correct: `stepCgroup` (boot.go:706-720), the ⅞ `memory.high` in `writeLimitsTo` (cgroup_linux.go:354-359), `joinLeaf`/`leafLimits` (runner/limits.go:68-92), `LimitsFor` (m2types.go:552), `limitOK` (authz.go:745-768), `guestSize` (runner/vm.go:126), the diskmon constants, tilesbx `Policy`/`limitsFor` (policy.go:62-72 and :382), `stepLimitAlerts` (boot.go:528) and `leaveLeaf` (runner.go:624 → limits.go:97).

**Corrections and additions:**

- **`cgroup.OOMKills` (leaf_linux.go:150) only reads flat leaves.**
  - It calls `checkName`, which refuses any name containing `/`, and it joins `m.leaf(name)` = `comp-<name>`.
  - So it cannot read a deployment leaf (`tile-<k>/d-<dep>/backend`) or a person's partition leaf. It has to switch to `m.path(name)`. For flat names that gives the same path, so tilesbx is unaffected.
- **`AtLimit` doesn't count OOM kills.** It reports `memory.events` `max`+`oom`, not `oom_kill`.
- **There are two crash watches, not one.** One is at runner.go:424 and the other at states.go:214 (deployments). Both build crash-loop errors with no OOM wording.
- **The exit path records nothing for an OOM kill.** `sbxExited` (sbx.go:161) returns early on any signal, so a SIGKILL'd backend leaves no trace.
- **diskmon has three gaps:**
  - A quota alert's `Tile` is the scope's data key (`util.ScopeKey`), e.g. `apps~foo` for a nested scope, not a path. Readers of that scope's root tile never see it.
  - `Status()` (diskmon.go:309) returns the global quota.
  - `scan()` reads `d.quota` at :215.
- **The shell reads `/alerts` with `window.xbin.fetch`**, i.e. the shell's frame principal. Any per-viewer content in an alert must resolve the human driving that frame.
- **There is an existing pattern for "at all times":** `/xbin/partitions` (server/personpage.go). It is an xbind-served page, "a guaranteed surface… never waits for a scaffold update".
  - This matters because of compat rule 4: an upgraded workspace keeps its old shell and admin tile until someone runs `bx builtin update`.
- **The iOS app ignores unknown event types** (`WorkspaceEvents.swift:320`, `default: break`), so a new `limits` event type is safe.
- **Several files are at or near their size budgets.** Edits to them must stay net-neutral, or code moves out first:

| File | Lines now | Budget |
|---|---|---|
| boot/boot.go | 800 | 800 |
| server/openapi.go | 800 | 800 |
| registry/registry.go | 796 | 800 |
| deployments/authz.go | 794 | 800 |
| broker/diskmon.go | 790 | 800 |
| broker/usersapi.go | 788 | 800 |
| deployments/plane.go | 775 | 800 |
| users/orgs.go | 1548 | 1600 |
| runner/runner.go | 632 | 639 |
| tiles/admin/admin.js | 307 | 318 |
| ui-harness/shots.js | 868 | 877 |

- **D203 is free.**

## 1. Manifest key

**Key name: `"limits"`, not `"resources"`.** `resources` already means data declarations (kv, sqlite, blob, fs, `res:` grants) in `scope.json` and the workspace `xbin.json`, which are sibling files. `limits` matches the existing vocabulary: `/deployments/limits`, `/partitions/limits`, `LimitsView`, `XBIN_LIMIT_*`. The VM ask folds into the existing `vm` key.

```jsonc
"limits": {
  "memory": "4G",          // backend memory.max (kind memMiB); default XBIN_LIMIT_MEM
  "disk": "100G",          // the SCOPE's data quota (diskGiB); read only on the tile that roots its scope
  "sandboxes": { "memory": "16G", "vcpus": 8, "disk": "200G" }  // tilesbx perTile (sbxMemMiB/sbxVCPUs/sbxDiskGiB)
},
"vm": { "memory": "4G", "vcpus": 4 }   // existing key: above the VM policy it is now an ask (vmMemMiB/vmVCPUs)
```

**Kinds are named after their unit:** `memMiB`, `diskGiB`, `vmMemMiB`, `vmVCPUs`, `sbxMemMiB`, `sbxVCPUs`, `sbxDiskGiB`. This reuses `deployments.LimitMemMiB`/`LimitDiskGiB`. Every API value is an integer in the kind's unit, so there are no floats and no byte/MiB ambiguity.

**Parsing:**

- New `internal/registry/limits.go` defines `type LimitsAsk struct{ Values map[string]int64; Problems []string }`.
- It has an `UnmarshalJSON` that **never returns an error**. A failing value would otherwise fail the whole manifest (`jsonc.Unmarshal` at registry.go:631) and break the tile, which is the cautionary tale from the `partition` key.
- Sizes are strings, binary units, case-insensitive: `K/M/G/T`, with `Gi`/`GiB`/`GB` read as binary, like `parseMiB`.
- Memory rounds up to MiB and disk to GiB. vCPUs are integers 1–256. Minimums are 128M memory and 1G disk.
- A bare number in `limits` is a warning ("use a size like \"4G\"") and is ignored. `vm.memory` keeps today's parse, where a number means MiB.
- Unknown sub-keys (`pids`, `cpu`, …) are warnings and are ignored.
- `ValidateLimits(m)` turns `Problems` into text appended to `ManifestErr` in the existing else-if chain. That is informational only: the tile keeps serving.

**Placement checks** happen in the broker, which knows the registry, and are shown as row notes:

- `limits.disk` on a tile that doesn't root its scope is listed as "ignored: the quota is its scope's (apps); ask from the tile that roots it".
- `limits.sandboxes` without a `cap:sandboxes` grant is dormant.
- `limits.memory` on a static tile, or on a VM tile, is ignored. For VM tiles the `vm` sizes apply instead.

**An ask at or below the default** is ignored silently: no prompt, and the row shows `covered: true`.

**VM semantics change** (permissive, gated by approval):

- `guestSize(p, o, g)`: the ceiling is `max(policy, approved)`. The manifest's size *lowers* below that ceiling exactly as today.
- So a manifest without an approval behaves exactly as today. A size above the policy becomes a pending ask instead of being silently clamped.

## 2. Store: `data/tile-limits.json` (xbind-owned, under data/ like partition-limits.json)

```json
{"schema":1,
 "tiles":  {"apps/foo": {"memMiB": {"ask": {"value":4096,"by":"user:alice","at":"…"},
                                    "set": {"value":3072,"by":"user:bob","at":"…","source":"raise"}}}},
 "scopes": {"apps~foo": {"diskGiB": {"set": {"value":75,"by":"…","at":"…","source":"admin-set"}}}},
 "hits":   {"apps/foo": {"memMiB": {"at":"…","count":3,"value":2048,"detail":"OOM-killed (2 processes)","deployment":"","partition":false}}}}
```

**Two independent slots per kind** are what make both requirements work: an ask approval that follows the manifest, and a raise that survives the ask being dropped.

- **`ask`** (`source: manifest-ask`) is the approval of a manifest ask. It applies only while the manifest still asks above the default: its contribution is `min(ask.value, manifestAsk)`. If the ask drops, the slot goes dormant but is kept, like D118 chrome. Re-asking up to the approved value applies again without a new prompt.
- **`set`** (`source: raise | admin-set`) is independent of the manifest and stays until it is reverted.

**Effective limit:**

```
raise     = max(set.value, min(ask.value, manifestAsk if manifestAsk > default else 0))
effective = max(default, min(raise, globalMax))
```

**Pending:** an ask is pending when `manifestAsk > max(default, set, ask) ∧ kind enforced ∧ placement ok`.

**Defaults** are per tile, supplied by the hook:

| Kind | Default |
|---|---|
| memMiB | `TileLimits.MemMax>>20` (0 = not enforced: no cgroup delegation) |
| diskGiB | `XBIN_LIMIT_DISK` |
| vm* | `vm.Policy` |
| sbx* | `tilesbx` `policy.For(tile).PerTile`, which already includes the admin's Overrides |

**Global maximums:**

| Kind | Maximum |
|---|---|
| memMiB | host memory (`hostMemory`: MemTotal ∧ xbind's cgroup ancestors) |
| diskGiB | partition total − 10% |
| vmMemMiB | `vm.Policy.BudgetMiB` |
| vmVCPUs | min(32, NumCPU) |
| sbxMemMiB | the sandboxes total (`Total.MemMiB`, or ¾ of host) |
| sbxVCPUs, sbxDiskGiB | tilesbx's validate bounds (and disk total − reserve) |

**Disk is keyed by the scope's data key** (`util.ScopeKey`; `"workspace"` for the workspace scope). That is exactly what diskmon measures, and D118 guarantees one holder per key. The API speaks in scope path and root tile.

**Unreadable file:**

- All values fall back to the defaults, which is the conservative direction.
- POST answers 500 and never overwrites the file.
- Admins get an alert of kind `tile-limits`, patterned on `settingsAlert`.

**Write rules:**

- Writes use `fsutil.WriteFileAtomicIn(…, 0o600)`, and no file exists until the first decision. There is no boot migration.
- `hits` writes are coalesced to at most one per tile and kind every 10 s.
- **Lock order:** callers may take tilesbx `m.mu` and then the store's mutex. The store never calls out while holding its mutex: effects run after it unlocks.

**Files:**

- `internal/limits/` is a new pure package with no I/O: `Kind`, `Values`, `Decision`, `Entry`, `Effective`, `Pending`, `Choices`, `Fit*`, and `ParseSize` (shared with the registry).
- `internal/broker/tilelimits.go` holds the store: load/save, effective, pending, hits.
- `tilelimits_api.go` holds the routes.
- `tilelimits_fit.go` holds capacity.
- `tilelimits_alerts.go` holds hits → alerts and per-viewer decoration.

## 3. Per-org ceiling

- **Data:**
  - `users.Org` gains `Limits *OrgLimits \`json:"limits,omitempty"\``, about 3 lines in orgs.go. The struct carries `memMiB, diskGiB, vmMemMiB, vmVCPUs, sbxMemMiB, sbxVCPUs, sbxDiskGiB`.
  - A new `users/orglimits.go` holds `SetOrgLimits` and validation, following `SetOrgNetSets`.
  - GET /orgs carries it automatically, so org admins see their ceiling.
- **Route:** `PUT /api/xbin/orgs/{org}/limits` (workspace admins only, `canManageUsers`).
  - Body: `{memMiB?: n|null, …}`. Null or 0 removes a kind.
  - Registered in `orgsapi.go`'s table: +1 line (756/800).
- **Semantics:** a per-tile maximum. An org admin may approve value `v` of kind `k` for a tile their org owns iff `v ≤ ceiling[k]` and the choice fits.
  - With no ceiling set for a kind, org admins can't raise above the default.
  - Org admins can never use `force`.
  - Per tile, not an org-wide budget (the owner's call, 2026-10-27).
- **Server gate:** `limitsActor(p)` copies the person-or-console rule from `partitionActor` (partitionops.go:289).
  - Allowed: a person's own session, app or device; the root token; or the admin tile's frame under an admin driver (`AdminFrameDriver`).
  - Then: `wsAdmin = IsAdmin(actor)`, and `org = approverOrg(actor, rootTile)` (delegated.go:24).
  - The tile's own credentials (frame, instance, terminal, agent) always get 403, as in partitionlimits.go.
- **Approvers hint:** `["workspace-admin"]`, plus `"org:<id>"` when the tile is org-owned and `v ≤ ceiling`.
- **Approvable:** computed per choice, for the viewer.
- **For disk:** the owner org is the scope root tile's. Scopes with no root tile are workspace-admin only.

## 4. Pending asks API and prompts

New routes (openapi in a new `server/openapi_limits.go`, appended to `joinEndpoints(…)` at openapi.go:630 on the same line):

- **`GET /limits[?tile=|?pending=1]`**
  - **No query:** rows for every tile the caller may manage or approve (workspace admin: all; org admin: the org's; user-owner: theirs). Each row is `{tile, scope, rootsScope, owner, limits:{<kind>:{effective, default, ask?, covered?, pending?, set?, askApproved?, usage?, applied?, max, enforced, atCap?, note?}}}`.
    - Also returns `defaults`, `enforced`, and `orgCeilings` for the caller's admin orgs.
    - Usage comes from `run.StatsSnapshot()`, not per-row cgroup reads.
  - **`?tile=`:** one row. Readers and the tile's own credentials get it read-only. Approvers also get `capacity` and, for each kind that is at cap or pending, `choices:[{pct, value, fits, why?, approvable, default?}]` plus `approvers`.
  - **`?pending=1`:** `{pending:[{tile, kind, why:"ask"|"cap", ask?|at?, effective, unit, approvable, mine, approvers}]}`, only items the viewer may approve or manages. This is the shell's one read.
- **`POST /limits {tile?|scope?, kind, value, source:"manifest-ask"|"raise"|"admin-set", restart?, force?}`**
  - **Validation:**
    - `value ≤ default` → 400 ("use DELETE; lowering below the default is per deployment, POST /deployments/limits").
    - `manifest-ask` requires `manifestAsk ≥ value`.
    - `> max` → 400.
    - Doesn't fit → 409 with the reason, unless `force` by a workspace admin.
    - Approver checks as in §3.
  - Fit is re-checked **inside** the store lock, so two concurrent approvals can't both pass.
  - **Effects:** write the store, run the apply hooks (§5), clear the hit for (tile, kind), publish `{type:"limits", component}`, and log an `audit` line.
  - Returns the tile's row.
- **`DELETE /limits?tile=&kind=`** (or `scope=`) reverts both slots to the default.
  - Workspace admins, the owning org's admins, and `mayManageTile` managers may do it, since narrowing is safe.
  - A still-standing manifest ask becomes pending again.
- **`POST /limits/dismiss?tile=&kind=`:** approvers acknowledge an at-cap hit for the whole workspace. This is the route an alert's `dismiss` field names.
- **`GET /xbin/limits`:** the guaranteed page; see §7.

**Notification:**

- `broker.RefreshLimitAsks()` is called in `watchLoop` after `RefreshPending` (serve.go:202, +1 line). It publishes `limits` when new asks appear.
- `limits` events carry no data, so no filter rule is needed in `eventFilter`.

**Prompts:**

- A third kind joins `grants`/`bindings`: `<bx-limits tile view="prompt">`.
- **Dismissal kind `limits`:**
  - Key: `limitKey = tile|kind|<ask>` for asks, and `tile|kind|cap:<at>` for hits.
  - A changed ask, or a new hit, shows again.
  - Personal dismissal; an approver's popover Dismiss on a hit calls the workspace route.

## 5. Applying limits at runtime

New step `{"tile-limits", stepTileLimits}` goes after `tile-sandboxes` in boot.go's `Steps`.

**Make room first:** move `stepCgroup` and `stepLimitAlerts` out of boot.go into a new `internal/boot/limits.go`. This frees about 50 lines. Fix the `slog` in `stepCgroup` that logs the constant `2<<30` instead of `memMax`.

- **Memory (live)**
  - New `cgroup.Manager.Resize(name string, l Limits) error`, with a stub in cgroup_other.go.
    - It writes only `memory.max` and `memory.high` (⅞ via `writeLimitsTo`) to an existing leaf through `m.path`.
    - Raising writes max then high. Lowering writes high then max.
    - In cgroup v2, `memory.max` is writable at any time on a populated cgroup. A raise takes effect immediately. Writing below `memory.current` triggers reclaim, then OOM.
  - Add `Resize` and `OOMKills` to `cgroupOps` (limits.go) and the test fakes.
  - New `Runner.ApplyLimits(tile string, restart bool)` in runner/limits.go:
    - For each state in `allStates(tile)` + `partStates(tile)`, under `s.mu`, if `s.cur != nil` and it is not a VM (`vmLeafBytes`), call `Resize(s.cur.leaf, r.leafLimits(tile, s.dep))`.
    - If a state has a crash-loop `lastErr`, or `restart` is set, call `ChangedTile(c)`. That clears `crashes` and restarts; the new generation joins with the new `LimitsFor`.
    - **Policy:** raises apply live. A lower value applies at the next start, and the UI says so, so we never OOM-kill on an admin edit.
- **Ceiling plumbing**
  - `deployments.Plane` gains `TileCeiling func(tile string) cgroup.Limits` (plane.go, +3 lines) and a helper `p.tileLimits(tile)` in a new `deployments/limits.go`.
  - `LimitsFor` (m2types.go:553) and `limitOK` (authz.go:753) use `p.tileLimits(o.tile)`. Net zero lines.
  - `GovHooks` gains `DiskCeilingFor func(tile string) int64`. `limitOK` at authz.go:762 uses it, and deploywire.go:38 wires the broker's per-scope quota.
  - Deployments and people's partitions inherit the tile's raised ceiling, as D127n's "tile's ceiling" implies. That is a per-leaf cap, not a shared one.
- **Disk (immediate)**
  - Make room: move `namespaceQuota`, `DeploymentDiskStatus`, `DiskQuota` and `DiskLimitCheck` from diskmon.go into a new `broker/diskquota.go`.
  - diskmon gets `quotaOf func(key string) int64`. `scan()` at :215 becomes `quota := d.quotaFor(scope, bk)`.
  - `nsBucket` gains `ceiling`: the scope's effective quota, set in `bucketOf` from `id.scope`.
  - `Status()` at :309 returns the effective quota, so `/tile-status`, `bx status` and partition limits follow it.
  - POST calls `d.kick()`, a single-flight async rescan, so the 507 lifts now rather than at the next 45 s tick.
- **Sandboxes**
  - `tilesbx.Deps` gains `Granted func(tile string) (memMiB, vcpus, diskGiB int)`.
  - `limitsFor` (policy.go:382) applies `l.raised(g)`:
    - `PerTile.X = max(PerTile.X, g)`.
    - **and** `PerSandbox.MaxX = max(MaxX, g)`, so one sandbox may use the whole grant (decided: a grant that no single sandbox could use would be a trap).
  - This takes effect at the next admission. The policy file and the admin's Overrides are untouched: the grant is a separate layer.
  - The answer notes that a running sandbox's own `memMiB` belongs to its manager (PATCH).
- **VM**
  - `Runner.VMGrant func(tile) (memMiB, vcpus int)` feeds `guestSize` at vm.go:100 and :165.
  - It takes effect at the next start. The UI says "applies at the backend's next start", with a Restart now option (POST `restart:true` → `ChangedTile`). Note that `stopFirst` means downtime.
- **Restricted terminals** (`cg.SetLimits`, D17d) are unchanged.

## 6. Detection

**OOM in a backend:**

- runner.go:624 `r.leaveLeaf(leaf)` becomes `r.noteExit(c, dep, leaf, logf)`, the same single line, implemented in limits.go:
  1. Read `cg.OOMKills(leaf)` **before** `Remove`.
  2. Compute the delta against `r.oomSeen[leaf]`. Blue/green share a leaf, so counters are cumulative; delete the entry when Remove succeeds.
  3. If the delta is positive: write `--- gen N: OOM-killed at memory.max X ---` to the backend log, mark `r.oomAt[(tile,dep,part)]`, and call `r.OnOOM(OOMEvent{Tile, Deployment, Partition bool, Kills, LimitMiB})`.
- `crashLoop` (partitions.go:605) and states.go:214 wrap their error with `withOOM(...)`, adding: "it was OOM-killed at its 2 GiB memory limit — raise it (the tile's ⚙ → Limits, /xbin/limits, `bx limits raise apps/foo memory`)".
- `noteExit` runs before `close(waitCh)`, so the mark exists when the crash watch runs.

**OOM in a live leaf:**

- `LimitHit` gains `OOMKills`, read via the fixed nested `OOMKills`.
- In `stepLimitAlerts` (moved), an `oom_kill` delta records a store hit instead of emitting its own alert. Pids and memory `max` pressure keep today's non-sticky warn.

**Sticky alert:**

- The broker's `apiAlerts` appends `b.limitHitAlerts(viewer)` live, not through the 45 s cached list.
- It reads: level `warn`, kind `oom`, tile, message "apps/foo was OOM-killed at its 2 GiB memory limit (3× since 14:02) — raise it: the tile's ⚙ → Limits, /xbin/limits, or `bx limits raise apps/foo memory`".
- It stays until a raise or set for that kind, or a workspace dismiss.
- Wording: a person's partition is "a person's partition of X" (03 §A.9). A non-primary deployment's hit goes to admins only (D127h).
- `TileAlerts` (diskstatus.go) adds the hits too, for `bx status`.

**Disk:**

- diskmon's existing `quota` crit alert (`u ≥ quota`) gains an action.
- A new kind `quota-near` (warn) fires at `u ≥ 0.9·quota`, as one more `case` in the switch.
- Only main namespaces of a scope get actions. Deployment and partition buckets don't.
- diskmon attaches `Action{Kind: diskGiB, Scope, Tile: rootTile}` using the registry's scope paths.

**Sandboxes:**

- `tilesbx.Deps.AtCap func(tile, kind, detail string)` is called from three places:
  - `teardown` when `oom>0 && !asked` (lifecycle.go:187) → `sbxMemMiB`;
  - `overDiskStop` (usage.go:300) → `sbxDiskGiB`;
  - admission `RefLimit` at a perTile cap (admission.go) → the matching kind.
- All of these are recorded as store hits.

**Alert action** (new optional `Action *LimitAction` field on `broker.Alert`; additive):

```
{action:"raise-limit", tile, scope?, kind, unit, current, approvers,
 choices:[{pct:25|50|100, value, fits, why?, approvable, default:(pct==50)}]}
```

- **Decoration is per viewer.** `apiAlerts` clones each alert before decorating it.
- **Viewer resolution:**
  - a session human is used as is;
  - a trusted-chrome login frame (the shell) is resolved to its driver user;
  - the admin console is resolved to its admin driver;
  - other tile frames get choices with `approvable:false`.
- `dismiss` is set only for approvers. Old shells POST it; everyone else falls back to personal dismissal.

**Choices:** `value = roundUp(current·(1+pct/100), step)`. The step is 64 MiB for memory, 1 GiB for disk, and at least +1 for vCPUs.

**Fit computation** (`internal/limits`, pure; inputs from hooks, capacity cached for 5 s). For tile T, candidate `v`, current effective `e`:

**memMiB** (also vmMemMiB, sbxMemMiB):

```
host      = runner.HostMemory()        (export hostMemoryOnce)
avail     = MemAvailable (∧ xbind's ancestor memory.max − memory.current)
reserve   = 10% · host                  (limits.ReserveFraction)
promised  = Σ over other tiles t with a raise: max(0, eff(t) − max(usage(t), default))
fits      ⇔ v ≤ max(kind)  ∧  (v − e) ≤ avail − reserve − promised
```

- The "why" text reads like: "needs +1.0 GiB; 0.6 GiB free after the 10% reserve and 4.0 GiB granted to other tiles but unused".
- Extra constraints: vmMemMiB also requires `v ≤ BudgetMiB`; sbxMemMiB requires `v ≤` the sandboxes total.

**diskGiB** (and sbxDiskGiB), from diskmon's last scan:

```
reserve   = 10% · total
promised  = Σ over other scopes s: unused promise
fits      ⇔ v ≤ total − reserve  ∧  (v − e) ≤ free − reserve − promised
```

**"The other tiles' unused quota" (decided by the owner, 2026-10-27: granted-only):**

- Read literally, it is Σ `max(0, quota(s) − usage(s))` over every scope. With the 50 GiB default that is usually more than the free space, so every choice would be greyed out on a typical workspace.
- Count only what has been *granted*: `max(0, quota(s) − max(usage(s), default))`. That is the same "a raise is a promise, the default is a share" rule as memory.
- Switching between the two is one constant.

**vCPUs:** `fits ⇔ v ≤ NumCPU` (and ≤ 32 for VMs).

**Approvable:** `wsAdmin ∨ (orgAdmin ∧ v ≤ ceiling)`.

## 7. UI

All shared code lives in `web/` (served by xbind, so it updates with the binary). Shipped `/vendor` URLs stay; only new files are added. No build step.

**New `web/` modules:**

- **`limits-kit.js`**, pure and tested in node: kind metadata (label, unit, step), `fmt`, `limitKey`, choice labels and grey reasons, `promptItems`.
- **`bx-limits.js` `<bx-limits tile view="prompt|section">`:**
  - Prompt view: pending asks (Approve / Dismiss), and at-cap hits with +25 / +50 (primary) / +100 buttons, greyed with `why` in the title and in a visible line, plus "ask a workspace admin / org:acme" for non-approvers.
  - Section view: every kind's effective, default, approved (who, when, source), ask, usage, applies-when, with edit (approvers), revert (managers) and restart.
  - Prop `.fetcher`, default plain `fetch` (the person's session). The admin tile passes `xbin.fetch`.
  - Exposes `testApi()`.
- **`limits-table.js`:**
  - `<bx-limits-table>`: the list with filters (raised, pending, at cap, org) and inline edit.
  - `<bx-org-limits org>`: the ceilings editor.
- **`limits.html` + `limits-page.js`:** the page at `GET /xbin/limits[?tile=]`. It is the "at all times" surface for workspaces whose scaffold predates the feature, and alert messages name it.
  - Server: `server/limitspage.go`, a copy of personpage.go: top-level only, CSP `'self'`, no transform. Extend `topLevelPage` and `loginNextPaths`.
  - `server.go` gets one `handle` line (767/800).

**Changes to existing `web/` modules:**

- `bx-requests.js`: `limitsStore()` (`/api/xbin/limits?pending=1`).
  - `listStore` takes the event types it follows (default `['grants','reload']`, plus `'limits'`), which is backward compatible.
- `bx-dismiss.js`: `KINDS` gains `'limits'`, a `limitKey` export, and `pendingCount(g,b,q,d,l)` with an optional 5th argument.
  - Old shells import this file from `/vendor` too. `normDismissed` already keeps unknown top-level keys, and the 4-argument call still works.

**Shell** (`workspace-template/shell`):

- `prompts.js`:
  - `promptsByTile({…, limits})` adds `p.tile` for items that are `approvable || mine` and not dismissed.
  - `e.limits` is counted.
  - `promptWords` gains "limit request(s)" / "cap hit(s)".
- `shell-prompts.js`: subscribe to `limitsStore`; `promptPop` adds `${e.limits ? html\`<bx-limits tile=${o.path} view="prompt">\` : nothing}`.
- `shell-alerts.js`: an alert with an `action` gets the three choice buttons (approvable and fitting only; the others are greyed with `why`).
- `bx-shell.js` (about 2 lines): reload `/alerts` and the prompts on a `limits` event.
- `bx-tile-admin.js`: a "limits" section rendering `<bx-limits view="section">`.
  - Shown to admins, owners and org admins.
  - Owners can view and revert. Org admins can edit within the ceiling.

**Admin console** (`tiles/admin`):

- `tabs/limits.js` is a thin plain tab wrapping `<bx-limits-table .fetcher=${xbin.fetch}>`. Register it in `plain-tabs.js` and add `{id:'limits'}` to the runtime group on the existing sandboxes line of admin.js, so admin.js stays within its budget.
- `tabs/orgs.js` embeds `<bx-org-limits>`.
- `shared.js` `alertBar`: a "raise…" link to `#limits/<tile>` for alerts with an action.

**Who sees what:**

| Viewer | Sees | Can do |
|---|---|---|
| Tile's own credentials | its own row | read only (403 on writes) |
| Reader | row, approvers hint | personal dismiss |
| User-owner | the above + pending (mine) | revert |
| Org admin (owning org) | rows + capacity + choices | approve/raise/set ≤ ceiling and fits; revert; workspace dismiss |
| Workspace admin | everything | anything ≤ global max (beyond fit with `force`); org ceilings |

## 8. CLI

New `cmd/bx/limits.go` registered in `moreCmds` (native.go:28). Add `limitsUsage` to the `usage()` concatenation on main.go's existing line. `bx chrome` is the template.

```
bx limits [ls] [--pending] [--at-cap]          tiles you may see: raised, pending, at cap
bx limits show <tile>                          kinds: effective, default, approved (by/at/source), ask, usage, choices
bx limits approve <tile> [<kind>…]             approve the manifest's pending asks
bx limits raise <tile> <kind> [+25%|+50%|+100%|<size>] [--restart] [--force]
bx limits set <tile> <kind> <size> [--force] | reset <tile> <kind> | dismiss <tile> <kind>
bx org limits <org> [--set memory=8G,disk=500G,…] [--rm <kind>]     (workspace admin)
```

- Kind aliases: `memory`, `disk`, `vm-memory`, `vm-vcpus`, `sandbox-memory`, `sandbox-vcpus`, `sandbox-disk`.
- A 404 from an older xbind exits 6, like the deployments commands.
- Every command is new, so the CLI stays a superset.

## 9. Docs

- **`docs/protocol.md`:**
  - the routes in §4 and `PUT /orgs/{org}/limits`;
  - `GET /xbin/limits` under Core;
  - `/alerts` `action` and the kinds `oom` (sticky), `quota-near`, `tile-limits`;
  - `/tile-status` disk quota is now effective;
  - the `limits` event in §/ws/events.
  - Plus `openapi_limits.go`; `apicheck` catches drift.
- **`docs/elements.md`:** the manifest `limits` key, and the `vm` comment ("above the policy, a request an admin approves").
- **`docs/isolation.md` §Resource limits:** per-tile raises, live memory, the OOM alert, raised disk quota, sandboxes.
- **Other docs:**

| Doc | Change |
|---|---|
| `docs/auth.md` §Ownership | resource ceilings |
| `docs/bx.md` | the new commands |
| `docs/config.md` | `XBIN_LIMIT_*` are now the *defaults* |
| `docs/sandbox-manager.md` | perTile grants |
| `workspace-template/AGENTS.md` | how a tile asks |
| welcome notes | if they teach manifests |
| `docs/maintenance.md` | the admin tab and the harness pass |

- **`docs/changelog.md`:** not breaking. Call out that an existing `vm.memory` above the policy now shows as a pending ask, and that a third-party top-level `"limits"` key is now read as an ask; malformed values are warnings.
- **`docs/compat.md`:** a short "Tile limits" note.
  - Additive wire. No migration.
  - Old shells and admin consoles show the banners' text; `/xbin/limits` and `bx limits` cover them.
  - **Downgrade:** an older xbind ignores `data/tile-limits.json`, so tiles drop back to the defaults, and its next write of users.json drops `org.limits`.
- **`plans/DECISIONS.md`:** D203, plus `plans/tile-limits.md`. Embedded trees cite D203 and the docs, never plans/.

## 10. Tests

**Go unit:**

- `internal/limits`:
  - `ParseSize` tables;
  - `Effective`/`Pending`: an ask going dormant and coming back, `min(ask, approved)`, a set surviving a dropped ask, the max clamp;
  - choice rounding and the default pct;
  - both disk "promised" variants.
- `registry`: a lenient `limits` (bad values become `ManifestErr`, the manifest still parses, other keys are intact). `vm` parse is unchanged.
- `users`: the `Org.Limits` round-trip; a users.json without it.
- broker `tilelimits`:
  - store load/save, unreadable → defaults + 500 + no overwrite;
  - the authorization matrix: workspace admin, org admin within and above the ceiling, user-owner, the tile's own token, a tile terminal, a console driven by a non-admin;
  - DELETE by a manager;
  - pending (offloaded skipped, placement notes);
  - the `limits` event;
  - hits coalescing, sticky, dismiss;
  - per-viewer decoration (`dismiss` only for approvers; `approvable` flags);
  - fit re-checked under the lock.
- diskmon: `quotaOf`; a raise followed by `kick` lifts the block; `quota-near` at 90%; actions on main buckets only.
- deployments: `LimitsFor` with `TileCeiling`; `limitOK` per-tile ceilings for memMiB and diskGiB.
- runner (fake `cgOps`):
  - `OOMKills` is read before `Remove` (order assertion);
  - the shared-leaf delta;
  - `OnOOM` carries tile, dep and partition;
  - the crash-loop text includes the OOM;
  - `ApplyLimits` resizes non-VM live leaves and restarts a crash-looped state;
  - `guestSize` with a grant.
- cgroup: `Resize` write order; nested `OOMKills`.
- tilesbx: `limitsFor` with `Granted`; `AtCap` from teardown, `overDiskStop` and admission.
- server: the `/xbin/limits` headers and byte-for-byte serving (copy `personpage_test.go`).
- `sizebudget`.

**Delegated integration:**

- Extend `internal/cgroup/leaf_integration_test.go`: `Resize` raises `memory.max` on a populated leaf; a hog in a 32M leaf gets OOM-killed, and `OOMKills ≥ 1` before `Remove`.
- New `test/limits/` package as a delegated suite in hack/integration.jsonc:
  - an xbind with `XBIN_LIMIT_MEM=96M`;
  - a Go fixture whose backend allocates N MiB on `/hog`;
  - **OOM path:** OOM → `/alerts` has a sticky `oom` alert with an action → `POST /limits` +100% → the backend restarts → `/hog?mb=150` succeeds → the leaf's `memory.max` is 192M;
  - **live raise:** a raise on a running backend keeps the same generation;
  - **disk:** `XBIN_LIMIT_DISK=8M`, a 10M blob write → 507, wait for the scan → `quota` alert → raise → writes succeed at once.

**Node:** `hack/limits.test.mjs` covers limits-kit, `prompts.js` with limits, and bx-dismiss's `limitKey`, `KINDS`, the 5-argument `pendingCount`, and the 4-argument compatibility path.

**UI harness:** `passes/limitscaps.js`, registered with one line in passes-more.js.

1. A tile with `limits.memory: "3G"` → key badge and popover → Approve → `GET /limits?tile` shows effective 3072 with source manifest-ask (shot).
2. An org tile, with dev1 as org admin and a 2560 ceiling: the choice is greyed "above your org's ceiling". The admin sets 4096 in orgs → dev1 approves.
3. A served `/alerts` OOM with an action (the harness has no cgroups; follow the route-interception pattern in grantdismiss.js:182). The banner shows +25/+50/+100 with +100 greyed → click +50 → the real server's `set` source is `raise`. When run with `HARNESS_CGROUP=1` under a delegated scope, use a real hog instead; otherwise the pass says it skipped.
4. Admin console runtime → limits: edit, revert (shot).
5. ⚙ limits for owner (view and revert) and for org admin.
6. `/xbin/limits` top-level works; framed, it renders nothing.
7. Extend `oldScaffold`: the old shell shows the OOM banner's text and no page errors.

## 11. Order of work and the split between workers

- **Phase 0, serial, small; freezes the contract:**
  - pure moves for the size budgets: boot `stepCgroup`/`stepLimitAlerts` → boot/limits.go; diskmon quota functions → broker/diskquota.go; optionally registry `ValidateInterfaces` → its own file;
  - `internal/limits` types plus the hook structs (`broker.LimitSources{Defaults(tile), Usage, Host, Apply}`, `runner.OOMEvent`, `limits.Action`);
  - draft the protocol.md section with the JSON shapes;
  - D203 and the plans doc.
- **Phase 1, three workers in parallel:**
  - **W1, backend (store + API):** registry key, users org ceiling and route, broker store/API/authz/pending/hits/alert decoration, `openapi_limits.go`, the `/xbin/limits` server route, `bx limits` and `bx org limits`.
  - **W2, runtime and detection:**
    - cgroup `Resize`/`OOMKills`;
    - runner `noteExit`/`OnOOM`/`ApplyLimits`/`VMGrant`/crash text;
    - deployments `TileCeiling`/`DiskCeilingFor`;
    - diskmon `quotaOf`/`quota-near`/`kick`/actions;
    - tilesbx `Granted`/`AtCap`;
    - `stepTileLimits` wiring;
    - the delegated integration tests.
  - **W3, UI:** the web/ modules and page, shell, admin tab and org ceilings, node tests. It works against the Phase 0 contract, using route interception until W1 lands.
- **Phase 2, one worker:** integration on a real build, the harness pass and the oldScaffold extension, all the docs (elements, isolation, auth, config, bx, sandbox-manager, workspace AGENTS.md, changelog, compat), `make check`, `make integration`, and a harness run.
- **Commits:** small, with area prefixes (`limits:`, `cgroup:`, `runner:`, `diskmon:`, `shell:`, `admin:`, `bx:`, `docs:`).

## 12. Risks and compat

1. **Disk fit reading.** The literal "others' unused quota" would grey out every choice on typical workspaces; the owner chose the granted-only rule (2026-10-27), for memory too.
2. **Live lowering could OOM-kill a backend.** It is restricted to the next start.
3. **Raised caps are per leaf.** Each deployment and each person's partition gets the raised cap. Document this.
4. **Old scaffolds (compat rule 4).** No popover, ⚙ section or tab until `bx builtin update`. Mitigations: alert text names `/xbin/limits` and `bx limits`, the `dismiss` field is set only for approvers, and the oldScaffold pass checks it.
5. **Nested-scope quota alerts are keyed by data key.** The new action carries the real tile. Fixing `Alert.Tile` itself would widen who sees it; that is a separate, flagged change.
6. **Guest OOM inside a VM is invisible to the host.** VM kinds have no at-cap detection in v1, and a raise needs a restart (with downtime).
7. **Trust.** The manifest only asks. Approvals live in data/ and users.json, both xbind-owned. The tile's own credentials and terminals are refused, and the admin console needs an admin driver.
8. **TOCTOU:** fit is re-checked inside the store lock.
9. **Lock order:** tilesbx `m.mu` → store mutex; effects run after the store unlocks.
10. **Size budgets** on boot.go, diskmon.go, openapi.go, registry.go, authz.go, runner.go and shots.js: the Phase 0 moves are required.
11. **Alerts read through the shell's frame token.** Per-viewer decoration must resolve the chrome frame's driver. Get this wrong and admins see no buttons, or everyone does.
12. **iOS app:** it shows no alerts and ignores the unknown `limits` event (verified). Push notifications on OOM are a follow-up.
13. **Behaviour change in `stepLimitAlerts`.** OOM becomes sticky and store-backed. The memory-pressure (`max`) warning stays as it is.

### Critical Files for Implementation
- /home/magik6k/buxon/internal/broker/diskmon.go
- /home/magik6k/buxon/internal/runner/limits.go
- /home/magik6k/buxon/internal/broker/partitionlimits.go
- /home/magik6k/buxon/workspace-template/shell/shell-prompts.js
- /home/magik6k/buxon/internal/boot/boot.go
