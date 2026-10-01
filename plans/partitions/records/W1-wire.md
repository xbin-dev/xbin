# W1-wire — wave 1's seams wired

Wave 1's integration of the partitioned-tiles plan: the five packs merged on
`partitions` (F2 identity and routing, F3 runner, F4 data namespaces, F13a
mode switch and wipe, F13b templates and updates) were built against each
other through seams with fail-closed defaults. Branch `pt/w1-wire`, on
`partitions` at c91f543e. This change edits neither `docs/changelog.md` nor
`plans/DECISIONS.md` (see §Notes for the changelog).

## What is wired

Boot installs the runner's side per xbind (`internal/boot/partitionrunner.go`,
`partitionroute.go`); the broker's package seams are filled in one file,
`internal/broker/partitionwire.go`.

| Seam | Filled with |
|---|---|
| `boot.partitionRunnerOf` (F2) | `partitionStarts[runner.StartClass]` over `Runner.EnsurePartition` / `TrackPartition`, the three start classes by name, and `ErrPartitionBusy`, `ErrPartitionDeferred`, `ErrPartitionRefused`, `ErrNoPartition` marked `sbx.ErrRefused` → 503 with their text (F2's adapter as recorded; `ErrPartitionBusy`'s text is the documented 503 exactly) |
| `Runner.PartitionIdent` (F3) | `Broker.PartitionIdent`, now `(pkey, uid, err)`: liveness, then `mintPartitionUID` (the one minting path), `util.PartitionKey` |
| `Runner.ShouldRunPartition` (F3) | `Broker.ShouldRunPartition(tile, dep, part, uid)` — now takes the state's uid and answers false unless the stored uid is that one (PD-20) — ∧ `Broker.PartitionEncryptionHoldReason(tile, dep, part) == ""` (F4) |
| `Runner.PartitionEnv` (F3) | `Broker.PartitionEnv` (F4); `XBIN_PARTITION` stays the runner's own |
| `Runner.RegisterPartitionInstance` (F3) | `State.registerPartitionInstance(token, tile, dep, part, uid)`: auth's `RegisterInstancePartition` with the **state's** uid (F2's version looked the uid up in the users store, which could bind an old incarnation's token to a recreated person's uid); no uid registers nothing |
| `Runner.PartitionEvent` (F3) | `Broker.PublishPartitionState` (F2; the `partitions` event's shape stays provisional, F7b's) |
| `Registry.OnPartitionChange` | the runner's `PartitionsChanged` (F3, unchanged), then a second hook: `Auth.RevokePartitionInstances(tile)` whenever the new state has no running user partitions (F2's "token revocation of its own") |
| `addressedPartitionSeam` (F4) | `(*Broker).addressedPartitionKey` (F2's `addressedPartition`) |
| `partitionUIDSeam` / `partitionMintUIDSeam` (F4) | `(*Broker).storedPartitionUID` / `(*Broker).mintPartitionUID` |
| `partitionAdoptUID` (F2) | F4's `adoptablePartitionUID` through a two-line adapter (F4's is `(userID, created int64) string`, F2's `(userID, created time.Time) (string, bool)`); `TestPartitionUIDAdoptionWired` passes |
| `stampBusPartition` / `busEventPartition` (F4) | `ev.Partition = part` / `e.Partition`; `TestBusPartitionStampWired` passes. F4's test fixture no longer stands a Data-borne stamp in: the tests run on the real field |
| `partitionRunningSeam` (F4) | the runner's new `PartitionRunning(scope, pkey)` through `Broker.SetPartitionRunner`: a person's state on a tile at or under the scope (from admission, before `PartitionEnv` is asked, through a reaped generation's stop), **or a stop of one still draining** — `stopPart` now counts its drains (`partitionsState.draining`), so the contract "until that instance's sandbox has exited" holds. Unset (no runner): true, as F4's default |
| `partitionDiskCeilingSeam` (F4) | `Broker.PartitionBytes` (F3's limits store holds `partitionBytes`) |
| F3's `dataBinds` | a person's partition binds through F4's `partitionBindsFor` with `sharedResources` (F4's record, verbatim); `mainData` already let `.partitions` through (F4) |
| F13a's wipe executor | `wipeHook{name: "partition-namespaces"}` — the name of F4's `registerPartitionStore` — whose `wipe` calls F4's `wipePartitionNamespaces(tile, dryRun)` on `wipeEverything` only (H1) and adds namespaces, partitions, bytes and people to the summary (F4's record's adapter; F13a's record showed one for F4's pre-fix signature). Its `stop` stops every person's instance of the tile — and of every tile of the scope it roots — (`Runner.StopPartitions`, waited for) and revokes their tokens (`Auth.RevokePartitionInstances`) before anything is wiped and before the new mode is recorded or published. `TestWipeHooksCoverHoldsData` passes |
| F2's class table | `POST /partitions/mode`: GlobalOnlyRefused (F13a's row); `POST /partitions/limits`: Neutral (unchanged); `partitionPlanned` emptied (both routes are mounted). No other wave-1 route lacked a row (`TestPartitionRouteClasses`) |
| F2's `partitionUnconverted` | the kv (`GET/PUT/DELETE /kv/{rest...}`), blob and `POST /bus/publish` rows removed: their handlers resolve through `reachRes` → `partitionReach` and act on the caller's partition (checked: `kvAccess`, `blobAccess`, `apiBusPublish`). `TestPartitionGate` gains the case (a partition's frame and instance token reach them, stamped; an unconverted route and a mode decision are refused); `TestPartitionWiring` asserts the refusal on `GET /bus/subscriptions` now and that kv reaches its handler |
| the proxy's deployment path | `ensureTarget`'s `EnsureDeployment` is the deployment path (unpartitioned tiles and a partitioned tile's global instance), so it gets its `// deployment:` comment; user partitions already went through `EnsurePartition` |
| `auth.Principal.Partition` (F2) × tile sandboxes | `TestPrincipalFieldsReviewed` (internal/tilesbx) failed on `partitions`: F2's new principal field was never reviewed for the sandbox key. Reviewed: `keyOf` refuses a person's partition (403 `not-allowed`) — sandboxes are the global instance's alone (PD-28), and the API's partition gate already refuses every `/sandboxes` route to user partitions (GlobalOnlyRefused), so this is defence in depth; a global credential keeps the tile's key |
| F13b's open end | `pathLeftovers` lists a removed partitioned tile's mode record (`partition mode record of <tile>`; an unreadable one at the path too) and its people's partition namespaces (`<scope>'s people's partition data (<n> namespace(s))`), for tiles no longer registered — a non-owner's creation there is refused, as for a deployment's data |

## Deduplicated

- **The pkey.** F4's local `partitionKeyFor` is deleted; `partitionKeyOf`
  and F4's tests use `util.PartitionKey`. The two encodings were
  byte-identical (`"u-"` + hex of the first 16 bytes of SHA-256(
  `xbin-partition-v1` ‖ 0 ‖ id ‖ 0 ‖ uid)); `TestNsKeysPartition` still pins
  02 §1's bytes, now against `util.PartitionKey`. Nothing pinned different
  bytes.
- **Consent.** F4's fix had already removed its own consent check: the data
  plane asks `addressedPartition`, which asks F2's `partitionConsentHolds`.
  So `partitionConsentHolds` is the one consent seam, for calls and data
  alike; its default (no consent anywhere) keeps every cross-tile
  partition edge refused while the `partitionConsent` policy is on. Only
  comments changed.
- **Ledger.** F2's `partitionEdgeCounted` is deleted; `partitionEdgeSeam`
  (partitionreach.go) is the one ledger seam, shape
  `func(b *Broker, userID, from, to string)` (F2's record's), counted by
  Route for cross-tile partition calls and by the data plane for
  cross-scope reaches into the same person's namespace. F10 fills it.
- **The partition bus.** F2's `publishPartitionBus` and `busPartitionAllows`
  are deleted. F4's `publishPartitioned` stamps every publish on a
  partitioned scope's own bus — `global` for the global instance's too —
  and F4's `busPartitionReaches` compares the subscriber's reach
  (`partitionOf`, what `reachRes` sets as `ra.part`) with the stamp. F2's
  check at the top of `busFilter` required a *user* stamp and would have
  dropped every `global`-stamped event, so the global instance's own bus
  would have reached no one once the stamp was wired. What F2's check added
  is kept in F4's: a person's stamped event on a scope that no longer
  partitions reaches no one. `TestBusPartitionScope` now publishes through
  `apiBusPublish` and asks `busPartitionReaches`, on the real identity
  plane.

## Deviations

- **ErrPartitionRefused answers 503**, not F3's suggested 403: F2's adapter
  marks every runner refusal `sbx.ErrRefused` (F2's recorded deviation;
  `TestPartitionWiring` pins the 503 for a start without `--isolate`).
  `ErrNoPartition` never reaches the adapter (the proxy sends global to
  `EnsureDeployment`).
- **`Runner.PartitionDataBinds`** is a new exported, side-effect-free
  method (a partition's binds through `genEnv` and `dataBinds`), so the
  broker's integration test can run the runner's bind rules on the data
  plane's real remap. It starts nothing.
- **The switch's stop hook stops people's instances on every switch kind**
  (the removal of `global` included): the runner's `Stop` in `runSwitch`
  already did; the hook adds the explicit, waited stop and the wholesale
  token revocation. People's partitions restart lazily.
- **Leftovers, not a reset.** A removed partitioned tile's mode record and
  namespaces are listed by `pathLeftovers` (so a new owner can't inherit
  them) rather than reset by the creation paths; the path's owner and
  admins may still create there (as for every leftover), and then inherit
  the old record — the "tile comes back" case F4's retention keeps.

## Tests (added or changed)

- broker: `TestPartitionWireSeams` (every filled seam answers the planes'
  own functions; the running seam asks the runner once installed),
  `TestPartitionWireSwitch` (removing `global` keeps people's namespaces;
  a switch to unpartitioned stops and revokes people's instances first,
  under the old recorded mode, wipes both people's namespaces, counts them
  in the dry run and the history), `TestPartitionLeftovers`,
  `TestPartitionBindsThroughHooks` — the F3/F4 test F4's record asks for:
  real gocryptfs mounts (skipped without FUSE or `bin/gocryptfs` /
  `XBIN_GOCRYPTFS`), the real identity plane, `PartitionIdent` →
  `ShouldRunPartition` ∧ `PartitionEncryptionHoldReason` → `PartitionEnv` →
  the runner's `dataBinds`: own volume for the partitioned filesystem and
  sqlite, the shared sqlite read-write at its own path, the `"read"`
  filesystem read-only; `TestPartitionRunHooks` (the new signatures, a
  stale uid refused), `TestBusPartitionScope` (above).
- runner: `TestPartitionRunning` (scope matching; true while a stop
  drains, false after).
- server: `TestPartitionGate` (above). boot: `TestPartitionWiring`,
  `TestPartitionStartsAdapter` (the uid parameter), and
  `TestPartitionRunnerWired` pass.

## Seams still open (fail closed), by owner

- **F5** — bus subscriptions, cron jobs, vault and `POST /notify` for user
  partitions (`partitionUnconverted` rows); the GlobalOnlyDormant arm of
  `partitionGate`; `publishPartitionPushSeam` (a person's bus events reach
  frames and sockets only); cron/bus delivery principals carrying a
  partition; rebuilding `partition.json` from `ns.json`; partition vault
  files, registrations and identity records' wipe hooks.
- **F6** — `xbin/mail` doorbells (`StartMail`), the mail store's wipe hook.
- **F7a** — every `POST /term/sessions*` row; person terminal layers and
  partition agent history (wipe hooks, stops).
- **F7b** — the `partitions` event proper (`PublishPartitionState`'s op
  `state` shape is provisional; op `mode` unpublished), `partitionNotice`,
  `GET /partitions` (`InspectPartitions`, `DefaultPartitionCaps`), `bx
  partition limits`, `bx doctor`, `GET /logs` and `GET /tile-status` rows,
  the users-store hooks (`StopPartitionsOf`,
  `RevokeUserPartitionInstances`, `PartitionUserDeleted`,
  `dropPartitionNS`), a partition's log directory
  (`.xbin/partition/<TileKey>/…`, which no switch or delete removes yet),
  and removing a removed tile's mode record after the sweep (today it stays,
  a leftover of its path, with no surface to clear it but an admin's hand).
- **F9** — `?xbin-partition=global`, `Decision.Attribute`.
- **F10** — `partitionConsentHolds` (refuses while the policy is on),
  `partitionEdgeSeam` (counts nothing), consents' and ledgers' wipe hooks.
- **F15** — `personalBindGrant`, `partitionIfaceEnvSeam`.
- **F17b** — `partitionBackupSubject` for archives, `resenc` `Hold`/`Touch`
  during an archive.
- **prefs per partition** — `partitionPersonKeyed` (a partition's instance
  token is refused `/prefs`) until a pack keys prefs by partition.
- **F11/F12/F14, B2a** — UI surfaces and the agent template's default, as
  their records say.

## Looks wrong once the packs meet (flagged, not redesigned)

- **Fixed here — global's bus events** (above): F2's filter would have made
  a partitioned scope's own bus deliver nothing of the global instance's.
- **Fixed here — the token's uid**: F2's `registerPartitionInstance` read
  the person's *current* uid; with F3's incarnation rule the token must
  carry the uid its state was made for, which is now the hook's argument.
- **A person's start mounts the global instance's volumes.** The runner's
  gate for a user partition asks `shouldRun(tile, primary)` →
  `ShouldRunDeployment` → `DeploymentEncryptionHold(tile, primary)`, which
  mounts today's (global's) volume of every file resource the tile uses —
  on a `["user"]` tile too, where nothing ever uses them. Harmless but a
  gocryptfs process per resource; F3 (or F4) may skip the primary's
  encryption hold for a user partition's start, whose own hold is
  `PartitionEncryptionHoldReason`.
- **F13a's record's F4 adapter** called `wipePartitionNamespaces(tile)
  (int, error)`, F4's pre-fix signature; the hook uses F4's current
  `(tile, dryRun) (partitionWipe, error)`, which counts and names people
  itself.
- **F4's record's flip of `TestPartitionKVRoundTrip`** was stale: the test
  asserts no bus 503 any more; nothing to flip.

## Notes for the changelog (the owner folds these in)

- F2's and F3's changelog texts say people's partitions don't run yet ("a
  call reaching one answers 503"; "start once the routing and data planes
  land"). With this wiring they run under `xbind --isolate` (without it, a
  person's call still answers 503 `… only in a sandbox (--isolate) …`);
  fold the texts accordingly.
- New builder-visible behavior here: a `bus` event the global instance
  publishes on a partitioned scope's own bus carries `"partition":"global"`
  and reaches only subscribers acting in the global instance
  ([protocol.md](/docs/protocol.md) §/ws/events); people's partition
  volumes unmount after an hour unused while none of that person's
  instances runs ([resources.md](/docs/resources.md)); a removed partitioned
  tile's mode record and people's data are leftovers of its path
  ([auth.md](/docs/auth.md) §Creating tiles, [protocol.md](/docs/protocol.md)
  `POST /create`). Nothing changes for a workspace without a partitioned
  tile.
