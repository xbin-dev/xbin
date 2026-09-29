# 01 — Manifest, registry and partition mode

## Current behaviour

- `Manifest` (`internal/registry/registry.go:56`) is parsed with jsonc
  (`registry.go:603`); unknown keys are ignored (docs/compat.md rule 7).
  Errors are recorded in `Component.ManifestErr` (`registry.go:461`,
  `:604-616`). Only `ValidateRuntime` (`:234`, runtime `cgi`, D117) stops the
  backend; `ValidateExposes`/`ValidateInterfaces` errors leave it serving.
- `HasBackend` (`registry.go:524`) accepts `go|node|python` only.
- `scope.json` resources are `Resource{Type}` (`registry.go:253`), names
  checked by `dropInvalidResources` (D118 charset). A scope's data is shared
  by every tile under it (docs/tile-deployments.md:564-570).
- A pinned primary's manifest comes from its checkpoint (`composePinned`,
  D119e). `composeManifest` (`internal/registry/deployview.go:148-168`)
  assembles a view from three field kinds — tile (authority requests),
  inbound surface, code (deployment-level) — and `TestManifestFieldSplit`
  (`internal/registry/deployfields_test.go:69`) holds that every field is in
  exactly one kind.
- Non-primary deployments read their own code's manifest through
  `Registry.View` (`internal/runner/deployments.go:166` `viewOf`).
- Trust never comes from a file a sandbox can write (D118): chrome approval
  is xbind state, the manifest only asks.
- User ids: `^[a-z0-9][a-z0-9._-]{0,31}$`, `owner` reserved
  (`internal/users/users.go:741`), creation-only check; deployment names
  `^[a-z][a-z0-9-]{0,23}$` (`internal/util/util.go:154`).
- **Templates.** Instantiation (`apiTemplatesNew`,
  `internal/broker/templates.go:68-180`, body `{source, path, owner}` at
  `:69-73`) copies the tree (`Instantiate`, `internal/builtins/builtins.go:173-187`)
  and strips the `template` block (`stripTemplateBlock`, `:376-390`).
  Instances are forks that xbind never rewrites (D50,
  `internal/broker/templaterepo.go:19-25`); builders pull upstream with
  `git merge template/main` (`cmd/bx/template.go:62`). Builtin tiles and
  scaffold update by whole-file replace or a textual 3-way merge of each
  file, `xbin.json` included (`ApplyReplace`/`ApplyMerge`,
  `internal/builtins/updates.go:504-532`, `:538-604`).
- xbind already serves its own page in place of a tile document: the D36
  request-access page (`serveRequestAccessPage`,
  `internal/server/requestpage.go:13`).
- **Tile managers** in xbind are `mayManageTile`
  (`internal/broker/orgsapi.go:433-450`): a workspace admin or user manager,
  the tile's user owner, or an admin of the owning org. The same set sets
  lifecycle (`internal/broker/lifecycle.go:36`), including offload, which
  removes a tile's data from the host (`internal/broker/backup.go:397-429`).

## The change

### 1. The manifest keys

```go
// registry.Manifest — all CODE kind in composeManifest (TestManifestFieldSplit):
// the running code is what must know how to be partitioned.

// Partition asks xbind to run one backend per partition (docs/partitions.md):
// "user" — one per person; "global" — plus one background instance that
// non-partitioned callers reach. It sets the mode only while the tile holds
// no data; afterwards a change is a mode-switch request (§2).
Partition []string `json:"partition,omitempty"`

// PartitionMail is the path xbind POSTs a mail doorbell to (04 §3).
PartitionMail string `json:"partitionMail,omitempty"`

// PartitionNote is shown under xbind's own text when a mode switch is
// requested (§2.4): what gets deleted, in the tile's own terms. Display only,
// at most 280 characters, rendered as text.
PartitionNote string `json:"partitionNote,omitempty"`
```

The `template` block (stripped from instances) gains `"partition": [...]`:
the mode new instances start in (§2.7, PD-35).

Accepted `partition` values (PD-02, PD-03):

| Value | Meaning |
|---|---|
| absent | not requested (today) |
| `["user"]` | user partitions only; non-partitioned callers are refused |
| `["user","global"]` (any order) | user partitions + the global instance |
| anything else (`[]`, `["global"]` alone, duplicates, unknown words, non-array) | **invalid: no backend runs** (409); nothing is recorded or deleted |

Unknown words fail closed **by design** — a departure from rule 7's "unknown
keys are ignored", recorded in docs/compat.md under rule 7: a future kind
(`"org"`) makes this version run no backend rather than a wrong one
(C22). `partitionMail` must be an absolute path without query; it is a
warning (ignored) when `global` isn't declared.

`registry.ValidatePartition(m, c, scopes)` returns the error; the registry
stores it in a new `Component.PartitionErr` **and** `ManifestErr`.

### 2. The mode (PD-44, decided)

The manifest can be written by the tile's terminals, by a D127 rollback or
promote to an old checkpoint, or by a typo. The owner's rule: **the
manifest sets the mode only while the tile holds no data; on a tile that
holds data, a change is a mode-switch request**, which a tile manager either
confirms — deleting all the tile's data — or declines.

#### 2.1 The record and the states

```
data/partitions/<TileKey>/mode.json   (xbind-owned, masked from every sandbox)
{ "schema": 1, "tile": "apps/agent",
  "mode":     {"user": true, "global": true},             // recorded mode R; absent = unpartitioned
  "request":  {"spec": null, "since": "…", "gen": "…"},   // an open switch request R → Q; absent when none
  "declined": {"spec": null, "by": "alice", "at": "…"},   // "keep the current mode" for exactly Q; absent when none
  "history":  [{"op": "auto|request|switch|keep|withdrawn", "from": …, "to": …,
                "by": "…", "at": "…", "wiped": {…}}] }
```

- **Recorded mode R** — the record's `mode`; routing obeys only this.
- **Requested mode Q** — the primary's running code's `partition`, via
  `composeManifest`'s code kind (a pinned primary reads its checkpoint's).
- **Holds data** — §2.2.

| Q vs R | Holds data | State | Backend |
|---|---|---|---|
| Q = R | — | **unpartitioned** or **partitioned** (by R) | as R |
| Q ≠ R | no | **auto**: R := Q at once (history `auto`) | as Q |
| Q ≠ R | yes | **pending** (request R → Q) | none (§2.3) |
| Q ≠ R, `declined` names exactly Q | yes | **declined**: R kept | as R |
| Q invalid | — | **invalid** | none: 409 `partition: <why>` |

- No `mode.json` is written while R and Q are both absent (zero state, 10 §A.1).
- Code that returns to R clears `request` and `declined` (history
  `withdrawn`); a different request Q′ opens a new request.
- A declined tile runs R while its code asks Q. R = partitioned: code that
  doesn't know partitions runs once per person (people's data stays apart).
  R = unpartitioned: the code sees no `XBIN_PARTITION`, as under an older
  xbind (PD-06); a tile calling `xbin.RequirePartition()` exits, which is
  fail-closed.

```go
type PartitionSpec struct{ User, Global bool }               // registry
type PartitionState uint8 // Unpartitioned | Partitioned | Pending | Invalid
type PartitionRequest struct{ Spec *PartitionSpec; Declined bool; Since time.Time }
func (c *Component) PartitionState() (PartitionState, PartitionSpec /*R*/, *PartitionRequest /*Q, nil when Q = R*/)
func (c *Component) Partitioned() (PartitionSpec, bool)      // R has user partitions and the state isn't Pending/Invalid
```

#### 2.2 "Holds data" (PD-51)

xbind decides this from its own stores and never from content a sandbox
wrote. A tile holds data when its scope root has any of the following:

- a data namespace with content — main, `.deployments/<escS>/*` or
  `.partitions/<escS>/*` (03 §B.1). "Content" means one of:
  - a volume whose cipher directory holds more than gocryptfs's
    `gocryptfs.conf`/`gocryptfs.diriv` (in plaintext-vault mode, a non-empty
    directory);
  - a kv bucket with a key;
  - a blob;
- a vault file with a key (main, deployment or partition);
- a registration row: cron, bus subscriptions, iface instances or ingress
  hosts, in the main stores or the deployment and partition files;
- a partition record, mail, or a personal bind of the tile.

Empty volumes created by provisioning don't count, so an instance fresh from
a template holds no data until its backend first writes. An empty sqlite
file does count, because it is a file; the rule leans towards asking. A tile
without resources, vault or registrations never holds data, so its mode
always follows the manifest (there is nothing to delete).

#### 2.3 Pending: nothing runs (PD-50)

While a switch is pending, **no instance of the primary runs**: not the
global instance and not the user partitions. The alternative, running in R
read-only, was rejected for three reasons:

1. The running code asked for Q. Running it in R serves people from code
   that expects a different mode.
2. xbind can't make a backend read-only without breaking it. sqlite and
   filesystem resources are mounted directly, and sqlite writes its journal
   even for reads; the agent migrates at open (`builtin-templates/agent/_backend/db.go:151-175`). The
   only read-only that can be enforced is not running.
3. One act resolves it: "Keep the current mode" restores service at once.
   The grey-out already tells everyone why the tile is paused.

While pending:

- the tile's API answers 409 `{"error": "<t> is paused: a partition mode
  switch is requested (<R> → <Q>); a manager of <t> must switch (deleting
  all its data) or keep the current mode", "partition": {"state": "pending",
  "from": R, "to": Q}}`;
- ingress answers 503 with today's text;
- cron ticks are counted as missed, and bus and mail deliveries wait (mail
  is durable);
- non-primary deployments keep D127's behaviour (writers only, own
  namespace; PD-17).

#### 2.4 The grey-out: who sees what

1. **The tile's frame** (guaranteed in every shell and in the iOS app). HTML
   navigations to the tile's documents are answered with xbind's own page,
   `servePartitionSwitchPage` (new `internal/server/partitionpage.go`; the D36
   precedent). No tile HTML is touched. It returns 409 and `Cache-Control:
   no-store`, and draws a greyed card with this text:
   > A partition mode switch is requested for `<t>` (`<R>` → `<Q>`). **All
   > data in this tile will be deleted for the switch to happen.**

   The card continues with:
   - `<t> says: <partitionNote>`, when the tile has one;
   - "Until a manager decides, `<t>` doesn't run";
   - who can decide (the D36 owner line: the tile's owner, its org's admins
     or a workspace admin) and where: `<origin>/xbin/partitions`, or
     `bx partition switch|keep <t>`.

   Tile frames are sandboxed without same-origin (`web/bx-frame.js:806`,
   `web/frame-info.js:35-45`), so this page only informs.
2. **The shell banner** (guaranteed). xbind raises
   `Alert{level: "warn", kind: "partition-switch", tile, message}`
   (`internal/broker/diskmon.go:55-65`). `GET /alerts` delivers it to admins
   and the tile's readers (`diskmon.go:365-378`). Old shells render it as
   the top banner (`workspace-template/shell/bx-shell.js:1735-1738`) and the
   admin tile shows it in its alert bar (`workspace-template/tiles/admin/admin.js:232-235`).
3. **Deciders.** When a request opens on a tile that holds data, the tile's
   managers get a push notification and a `partitions` op `mode` event. They
   decide on one of these surfaces:
   - `/xbin/partitions` (guaranteed, 06 §12), which asks for a typed
     confirmation and shows counts (namespaces, people's partitions, vault
     keys, registrations, bytes) plus what is kept;
   - the new shell's card overlay with **Keep the current mode** /
     **Switch and delete all data…** (scaffold, 06 §12);
   - the admin tile's Partitions section;
   - `bx`.

Everyone who can open the tile sees surface 1, and surface 2 in the shell.
Only managers see the buttons.

#### 2.5 Deciding

**Who: a tile manager** (`mayManageTile`, `orgsapi.go:433-450`), acting
with their own session, app or device credential. A tile principal (its
instance, frames, terminals or agent sessions) can never decide (02 §8).
Write level alone isn't enough: writers can raise a request by changing the
code, but deleting other people's data is governance. The manager set
already sets lifecycle, including offload, which removes the tile's data
from the host (`lifecycle.go:36`). A switch therefore adds no destructive
power that set lacks today.

**Keep the current mode**, `POST /api/xbin/partitions/mode {tile, act:
"keep", from: R, to: Q}`:
- records `declined` for Q;
- the tile runs R again at once, and nothing is deleted;
- history gets a `keep` entry, and the alert and grey-out clear.

Afterwards the components row carries `request: {spec: Q, declined: true}`.
`bx doctor`, the admin Partitions section and `/xbin/partitions` keep
showing "code asks for Q; R kept (declined by …)" with a **Switch…** action.

**Switch**, `{tile, act: "switch", from: R, to: Q, confirm: "<tile path>"}`:
- It answers 409 unless R and Q still match the request (race) and Q is
  valid.
- A switch to user partitions is refused without `--isolate` (PD-19).
- For a tile with sandbox-manager slots, it lists bound managers that lack
  `caps.partitions` (C12) and needs `--yes`.

It then runs these steps in order:
1. Stop every instance of the primary. Revoke their tokens synchronously
   (02 §2, S13). Hold the namespace gates (`holdNS`,
   `internal/broker/deployns.go:139`).
2. **Wipe** (§2.6).
3. Erase the backup subkeys of every wiped namespace and partition
   (11 §3). Every existing sealed backup of that data becomes unreadable.
4. Set R := Q. Record a history `switch` entry with a wiped summary:
   namespaces, people's partitions, vault keys, registrations, bytes and
   subkeys erased. Write a deploy-log line; the slog `audit` line comes from
   the POST (`internal/server/server.go:584-601`).
5. Send notices. Each person whose partition was deleted gets a push and a
   `/xbin/partitions` notice: "`<t>` changed how it keeps data; your data in
   it was deleted by `<who>` at `<when>`".
6. Instances start lazily in Q.

#### 2.6 What a switch deletes and keeps

A switch **deletes** the following, using the existing confined, nofollow
removers (D78) — the offload paths `removeScopeData`
(`internal/broker/backup.go:433-453`) and `dropNamespaceKV`
(`internal/broker/backup_cron.go:296-314`):
- every data namespace of the tile's scope: main, every deployment's and
  every partition's (all resource types);
- every vault file of the tile (main, deployments, partitions);
- every registration: cron, bus subscriptions, iface instances and ingress
  hosts;
- partition-bound state:
  - partition records, mail, ledgers and log shares;
  - consents that name the tile, and personal binds whose requester is the
    tile (05 §3);
  - person terminal layers, partition logs and partition agent-session
    history (06 §2);
- in backups, that data, by subkey erasure (11 §3).

Every other plane registers a wipe hook with the same executor (03, 04,
05, 06, 11), so the list stays complete as packs land.

It **keeps**:
- the code: the tile directory, checkpoints and deployment records;
- grants and global bindings (workspace wiring);
- the tile's own terminal layer;
- people's homes and their own agent-session history
  (`data/agent-history/<user>/`);
- provider-held records, such as sandboxes at a sandbox manager. The
  confirmation names the bound managers so those records can be cleaned
  there.
- archives made before sealing (11 §6). They are plaintext, so no key
  erases them; the confirmation says how many exist.

#### 2.7 Templates and updates never switch a mode (PD-35, PD-52)

- **Instantiation.** A template declares its instances' starting mode in
  the stripped `template` block. Instantiation writes it as the instance's
  top-level `partition`, both through `Instantiate` and through
  `instantiateWorkspace` (`internal/broker/templates.go:190-199`). There are
  two exceptions:
  - the request body says `"partition": false`. This is a new field. The
    Tile Manager's template card (`workspace-template/tiles/manager/index.html:415-450`)
    shows a checked "Keep each person's data apart" box, and `bx template
    new … --no-partition` does the same;
  - xbind runs without `--isolate`. The box is then off and disabled with
    "needs --isolate" (PD-19).

  The new instance holds no data, so its first rescan records the mode
  (`auto`, `by` = the instantiating person).
- **Instance merges.** Because the default lives only in the `template`
  block, a template's top-level manifest never carries `partition`. A
  builder's `git merge template/main` (D50) can therefore never add or
  remove the line: a change inside the `template` block touches lines the
  instance dropped and surfaces as a conflict.
- **Builtin updates.** `bx builtin update` (`ApplyReplace`/`ApplyMerge`)
  carries the **installed** `partition` value (present, absent or its list)
  into the `xbin.json` it writes, as a single jsonc-aware key splice. When
  upstream differs, the result reports `partition kept as installed
  (upstream asks <x>): edit it deliberately to request a switch`.
- **Promote and rollback.** A D127 promote or rollback to code that asks
  for a different mode is the one non-edit path that can open a request. Its
  preflight warns: "`<t>` will pause for a partition-mode decision".

#### 2.8 Non-primary deployments (PD-17)

Non-primary deployments (D119/D127) never run user partitions in v1. A
non-primary deployment of a partitioned tile runs **one** instance:
- on its own deployment namespace;
- reachable as deployments are today (writers only);
- with `XBIN_PARTITION=global` when its own code asks for partitions.

Its code's flag never gates the tile. `POST /deployments/primary` is
refused for a partitioned tile (409 `switching the primary would leave every
person's data with <old>: promote instead`).

### 3. Structural rules (PD-03, PD-28)

A `partition` request is **invalid** when:

1. the tile uses same-scope resources but **doesn't root its scope**
   (`c.Scope != c.Path`): the resources would be another tile's scope's;
2. another tile inside the partitioned scope root requests a **different**
   list (the scope's resources are one namespace set);
3. `chrome: true` (a chrome frame acts as the person with the session
   cookie — no partition semantics);
4. `vm: true` (C14): each person would be a 2 GiB guest drawn from the
   workspace-wide VM budget (`internal/vm/manager.go:225`,
   `internal/vm/policy.go:24-27,40`); revisit with a per-tile VM sub-budget;
5. the tile holds (or requests) `xbin`/`xbin:*` (governance), `cap:sandboxes`
   (tilesbx keys by `Key{Tile,Deployment}`, `internal/tilesbx/keys.go:19`),
   `cap:net-admin` or `cap:containers` (S5: these widen every partition's
   sandbox — `internal/runner/sandboxcmd.go:139-144`); approving such a grant
   for a partitioned tile answers 409, as D127k does for non-primary
   deployments;
6. a `scope.json` resource of the partitioned scope is `"shared": "read"`
   with type `sqlite` (04 §1);
7. it's a template (`template` block): ignored until instantiated — not an
   error, the instance carries it.

Allowed with notes: `alwaysOn` (global only, 03 §A.6), `exposes` (served only
by global, 05 §5), a `net → host` binding or a provider splice (global
instance only; user partitions get relay egress, 03 §A.4), static tiles
(warning: nothing to partition).

### 4. `scope.json`: shared resources (details in 04)

```go
type Resource struct {
    Type   string `json:"type"`
    // Shared, in a partitioned scope: one namespace (today's). true — every
    // partition and the global instance read-write; "read" — the global
    // instance read-write, user partitions read-only. Ignored elsewhere.
    Shared SharedMode `json:"shared,omitempty"` // accepts true | "read"
}
```

`shared` in a scope no tile partitions is ignored silently (templates ship it
ready). `cron` resources can't be shared: `shared` on a `cron` is a warning,
ignored. An unknown `shared` value is invalid for the scope's partitioned
tiles (fail closed, rule 6 above).

### 5. Registry answers used elsewhere

- `Registry.PartitionedScope(scope) (PartitionSpec, bool)` — for the broker's
  namespace choice (a scope is partitioned when its root tile is
  `Partitioned`, i.e. by the recorded mode).
- `Registry.Components()` unchanged; partition state rides on `Component`.
- `ResolveRef` (`internal/registry/qualifier.go:90`) **unchanged**: a
  partition is never named in a URL path (the `+`, `:`, `~` separators are
  all taken — docs/changes/2026-09-28-plus-in-tile-names.md; path tickets
  `/api/~<ticket>/`). Only the F5 query parameter names `global` (05 §6).

### 6. Transitions (`Runner.PartitionsChanged(c, old, new)`)

`PartitionsChanged` runs when the **effective** state changes: a rescan, an
auto record, a keep, a switch, or a promote/rollback. Before the new state
is published, every instance token registered for a partition that the new
state doesn't cover is revoked synchronously (02 §2, S13).

- **auto** (no data): the tile starts in Q. Under a mode with user
  partitions, today's instance becomes the global instance when `global` is
  declared.
- **→ pending / invalid**: every primary instance stops, and nothing is
  deleted.
- **keep**: the tile runs R again.
- **switch**: the steps of §2.5. Nothing starts until the wipe completes.

A `partitions` event (op `mode`, `{state, spec, request}`) goes to the
tile's readers, with the decision fields for managers. `bx doctor` and the
`/alerts` banner report pending and invalid tiles.

## Wire (for docs/protocol.md, docs/elements.md, docs/compat.md)

- `xbin.json` keys `partition`, `partitionMail`, `partitionNote`, and
  `template.partition` (docs/elements.md manifest table, docs/partitions.md);
  the docs/compat.md rule-7 note (unknown partition words fail closed).
- `scope.json` resource `shared: true | "read"` (docs/resources.md).
- `GET /api/xbin/components` rows gain, only for tiles that request or
  record a mode, `"partition": {"state": "partitioned|unpartitioned|pending|invalid",
  "user": true, "global": true, "request": {"user": …, "global": …,
  "declined": false}}` and, on error, `"partitionError": "<text>"`. Both are
  absent otherwise (additive, rule 2).
- `POST /api/xbin/partitions/mode {tile, act: "keep"|"switch", from, to,
  confirm?}` (06 §6).
- The tile's API answers 409 with the pending body of §2.3.
  HTML navigations get the switch page (409). `/alerts` gains kind
  `partition-switch`.
- `POST /api/xbin/templates/new` gains `partition: false`, and `bx template
  new` gains `--no-partition`. `POST /deployments/primary` answers 409 for
  partitioned tiles.

## Tests

- `registry`:
  - `TestValidatePartition` table: the values above, order, duplicates,
    non-array JSON types, `partitionMail` shapes, and `partitionNote`
    length;
  - `TestPartitionScopeRoot`, `TestPartitionScopeSiblings` and
    `TestPartitionStructuralRefusals` (chrome, vm, cap:net-admin,
    cap:containers, sqlite `"shared": "read"`); a template is ignored;
  - `TestManifestFieldSplit` covers the three keys in the code kind.
- `registry`/`broker` `TestPartitionStateTable` covers every row of §2.1:
  - auto on an empty tile (a fresh instance with provisioned empty
    volumes);
  - pending on a tile with one kv key, with one vault key, and with one
    cron row;
  - declined runs R, and a new Q′ reopens a request;
  - code returning to R records `withdrawn`;
  - `TestPinnedPrimaryRollback`: rolling back to a pre-partition checkpoint
    gives pending, never a silent switch.
- `proxy`:
  - pending and invalid answer 409 with the body and never call the runner;
  - an HTML navigation gets the switch page, which contains
    `partitionNote` escaped;
  - `/alerts` carries the alert for the tile's readers only.
- `broker` mode acts:
  - only `mayManageTile` principals decide; a writer, the tile's own
    terminal and its instance get 403;
  - a stale `from`/`to` gives 409;
  - keep deletes nothing;
  - switch deletes every item of §2.6 and keeps every item of the keep
    list (a fixture with main, `+dev`, two partitions, vault, cron, mail and
    a personal bind);
  - switch erases the subkeys, notifies the people, and writes history and
    the deploy log;
  - switch without `--isolate` is refused.
- `builtins`:
  - `TestInstantiatePartitionDefault`: the default is written; with
    `partition:false` it is absent; without isolation it is absent;
  - `TestUpdateKeepsPartition`: replace and merge keep the installed value
    (absent → absent, `["user"]` → `["user"]`) when upstream adds, removes
    or changes it;
  - a `git merge template/main` fixture never introduces a top-level
    `partition`.
- `broker`: approving `xbin:*`/`cap:sandboxes`/`cap:net-admin`/`cap:containers`
  for a partitioned tile gives 409.
- `deployments`:
  - `POST /deployments/primary` gives 409 on a partitioned tile;
  - a non-primary deployment runs one instance with `XBIN_PARTITION=global`;
  - the rollback preflight warns.
- compat: a manifest without the keys produces byte-identical
  `/api/xbin/components` rows (golden) and writes no `mode.json`.
