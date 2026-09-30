# F7a — records for the integrator

Work pack F7a (terminals and agent sessions on partitioned tiles;
plans/partitions/06-terminals-ops.md §1-§3; PD-09, PD-22, and the terminal
half of PD-10), branch `pt/f7a` on `partitions` at 982718a3, with the
review's fixes on `pt/f7a-fix` (section "Review fixes" below). It fills the
seams W1-wire.md left to F7a ("every `POST /term/sessions*` row; person
terminal layers and partition agent history (wipe hooks, stops)"). This
pack edits neither `docs/changelog.md` nor `plans/DECISIONS.md`; the two
texts below go there at merge.

## Changelog entry (docs/changelog.md, under the merge date)

- **Partitioned tiles: terminals and agent sessions belong to their
  person** ([partitions.md](/docs/partitions.md) §Terminals and agent
  sessions, [protocol.md](/docs/protocol.md) §/ws/term, `/term/sessions`,
  `/agent/history`, `GET /sandboxes`,
  [09-terminals.md](/docs/overview/09-terminals.md)). On a tile whose
  recorded mode has user partitions, a terminal or agent session reaches
  only its opener's partition: its terminal token already acts there, and
  the session now gets `XBIN_PARTITION=user:<id>` (after `XBIN_COMPONENT`,
  and after `XBIN_DEPLOYMENT` when set; `global` for a session targeting a
  non-primary deployment), starts in `$HOME` instead of the tile directory
  (shared code), and keeps its system changes in the person's own dev
  layer (`.xbin/term-part/<tile-key>/<partition-id>/`, their VM disk inside
  it — listed in `GET /sandboxes` disks as kind `person-terminal`, the
  person unnamed; `/ws/term/env` resets and reports that one; the boot's
  base-image GC keeps the base it was built on). A finished agent
  session's transcript is kept with the person's partition and merged into
  `GET /agent/history` — never read by a person recreated under the same
  id, nor by an admin viewing as the person — and a past session continues
  only where it ran (a partition's entry in the partition, an own one
  outside it; 409 otherwise). A session without a person (the owner token)
  acts in the global instance, or opens with the tile API off
  (`api:false`) when the tile has none. The session frame gains
  `partition` and `partitionNote` (a grey line the terminal prints; on an
  xbind without `--isolate` it says the host shell can read every
  partition), session rows `partition`. Admins can end another person's
  session there but get 403 on reattaching to it, on every agent-session
  route (events, log, diff, prompt, cancel, restart, permission, question
  and option answers) and on renaming it; an admin viewing as a person
  gets 403 on reading any session there; `?user=` listings, a view-as
  listing, `GET /status` and the sandbox list leave a person's sessions'
  names out. A person's terminal on the tile may now open and drive its
  own agent sessions (`POST /term/sessions*` answered 403 to a partition's
  credentials until now). A partition mode switch that deletes the tile's
  data ends its sessions first (those opened on a sub-path of the tile
  too) and deletes the person layers and partition agent history (the
  tile's own layer and people's own history stay); a new session asked for
  while the switch runs answers 409, and a tile whose recorded mode gains
  or loses user partitions ends the sessions opened under the other mode.
  None of it is backed up. Nothing changes for a workspace without a
  partitioned tile — no env line, frame field, row field, path, disk row or
  answer differs. Nothing to change.

## Decision entry (plans/DECISIONS.md, the next free D-number)

- **D&lt;next&gt; — Partitioned tiles, F7a: terminals and agent sessions
  (2026-09-30).** Implements PD-09, PD-22 and the terminal half of PD-10
  of plans/partitions/90-decisions.md; the design is
  plans/partitions/06-terminals-ops.md §1-§3. docs/partitions.md
  §Terminals and agent sessions, docs/protocol.md (§/ws/term,
  `/term/sessions*`, `/agent/history`, `GET /status`, `GET /sandboxes`),
  docs/overview/09-terminals.md.
  - **Chosen.**
    - **No token change; the broker answers the session's partition at
      open.** `term.Manager` gains three hooks boot installs
      (internal/boot/partitionterm.go): `SessionPartition` = the broker's
      `TermPartition(p, path, dep)` (the owning tile via `Reg.Resolve` —
      answered for an unpartitioned tile too, as `Tile` alone;
      `addressedPartition` for the opener; the person's partition id from
      `PartitionIdent`, which mints the uid at their first partition; a
      non-primary target → `global`, PD-17; no person and no global →
      `NoAPI`; a switch of the tile running → `term.ErrPartitionSwitching`,
      409), `TilePartitioned` = `TermTilePartitioned` (recorded mode has
      user, or unreadable), `PersonPartitionKey` (stored uid only, never
      minted). `pickPartition` runs after `pickTarget` on both kinds (and in
      the restart's probe, so a refusal leaves the session running). Nil
      hooks: every session is today's.
    - **What the session keeps** (`sessionPart`, fixed at start): its
      owning tile (every session, partitioned or not — a switch's stop and
      a mode change match sessions by it, so one opened on a sub-path of
      the tile is the tile's); `XBIN_PARTITION` after
      `XBIN_COMPONENT`/`XBIN_DEPLOYMENT` in both env paths; the start
      directory `$HOME` for every session on a partitioned tile (the
      agent's ACP cwd too; its `files.changed` snapshots still watch the
      tile's work tree); the layer key `term-part/<TileKey>/<pkey>` (mapped
      by `layerDir`; a tile's own key never holds `/`), so the one-holder
      lock, VM disk, base stamp and reset are per person; the session
      frame's `partition`, `partitionNote`, `api:false` (PD-10), the row's
      `partition`. A session without a person keeps the tile's own layer.
      On an xbind without `--isolate` the note says the host shell can read
      every partition's data instead of promising separation (the session
      still opens: the same person can read those files from any other
      tile's host shell, so refusing here would hide nothing).
    - **History by partition id**: `data/agent-history/.partitions/<pkey>/
      <TileKey>/` (no user id starts with a dot), merged into the history
      calls through `PersonPartitionKey`, so list, read, delete and resume
      need no route change; a recreated person (new uid) reads none of it.
      The calls take a `term.HistoryScope{Home, ViewAs}` (`HistoryOf(p)`):
      an admin viewing as the person gets their own history only. `?cwd=`
      narrows partition entries to that exact cwd, as own history is. A
      resume stays in its store (`resumeHere`, 409): a partition's entry
      continues only in its person's session on the partitioned tile, an
      own entry only outside one, so a continuation never moves a
      partition's transcript into history that outlives the partition.
    - **Admins (PD-09)**: `adminPass` = admin and (own session, or the
      session's tile not partitioned — at open *or now*, fail closed).
      `mayReattach`, `MayDrive` (every drive route through `drive`/
      `driveOther`, `GET /term/sessions/<id>` included) and the new
      `MayRename` use it; the new `MayKill` keeps the admin pass (`DELETE
      /term/sessions/<id>`; `DELETE /ws/term` keeps `CanTouch`). **View-as**
      (a read-only principal: `UserID` the person, `Impersonator` the
      admin, which `authed` lets through on every GET) is refused on every
      session of a partitioned tile (`viewAsBarred` in `MayDrive` and
      `mayReattach`, PD-08: view-as opens no partition). Names: a person's
      sessions on a partitioned tile (`SessionInfo.Personal()` — in their
      partition or in `global` on a non-primary target, both keyed by
      their partition id) have `name` blanked in `?user=` rows, a view-as
      listing, `GET /status` rows and the admin sandbox list. Events were
      already F2's (`termEventVisible`).
    - **The routes are converted**: the seven `POST /term/sessions*` rows
      left `partitionUnconverted` — the handlers act on the caller's
      person, whose session now opens in that person's partition with its
      own layer.
    - **The switch** (01 §2.5-§2.6): wipe hook `person-terminals`
      (broker/partitionterm.go) on `wipeEverything` only: `stop` ends every
      session of the tile and waits for their teardown (an agent's history
      is saved then), `wipe` asks again (a session that won't end fails the
      switch before anything of the terminals' goes) and deletes
      `.xbin/term-part/<TileKey>/` (confined `removeLayer`, D78) and
      `.partitions/*/<TileKey>/`; the dry run counts; the partition ids map
      to people (`peopleOfPartitionKeys`), who are told. The terminal
      manager reaches the broker through `SetPartitionTerminals`
      (a `PartitionTerminals` interface, held per broker in a `sync.Map`, so
      broker.go's struct is untouched). New sessions are refused while the
      broker's switch hold is on.
    - **No open slips between a stop and a wipe** (internal/term/
      partitionhold.go): every open that passes `pickPartition` counts as
      in flight until its session registers or the open fails
      (`partOpened`), and a stop (`StopTileSessions`,
      `StopPartitionSessions`) waits for the in-flight opens it matches as
      it waits for teardowns. One partition's end holds the partition
      (`HoldPartition(tile, pkey)`: its person's new sessions answer 409
      `ErrPartitionEnding`, checked atomically with the in-flight count)
      from before its stop until its wipe is done — `wipePersonTerminalsOf`
      does, for F7b.
    - **Mode changes without a switch** (an empty tile's manifest decides
      at once): boot's `OnPartitionChange` hook, added after the runner's,
      ends the tile's sessions whose partitioned-at-open flag differs from
      the new recorded mode (`PartitionModeChanged`).
    - **Base images**: `layers.List` walks `.xbin/term-part/<TK>/<pkey>`
      (tree `term-part`, key `<TK>/<pkey>`, an unstamped one pinning the
      legacy base like a tile's), so `Pinned` — and with it the boot's
      base GC and the VM image GC — keeps every base a person layer was
      built on. `vm.ListDisks` lists people's VM disks (kind
      `person-terminal`, key the tile's `TileKey`; boot maps it to the
      tile).
  - **Not chosen:** a per-session partition claim in the terminal token
    (the credential already decides, 02 §3); a term-side hold on the tile
    during a switch (the broker's switch hold covers the whole act and
    can't leak); counting layer bytes in the wipe summary (walking layers
    of GBs on every dry run); `$HOME` only for people (the owner's global
    session in the tile directory would invite shared-code writes too);
    admin rename kept (it is not kill, the one act PD-09 keeps); refusing
    people's terminals on a partitioned tile without `--isolate` (hides
    nothing, see above); holding each person-layer key (`holdLayer`) for a
    partition's end instead of a partition hold (it keeps the layer from
    being mounted but not a new agent session's history from being
    written into the wiped partition); person layers in the boot's
    base-image gate (`CheckBaseImages` stays on `.xbin/term`: one person's
    layer on a missing base refuses that person's session with the reset
    hint rather than the whole boot, and the GC no longer releases their
    bases).

## Seams for the integrator

### Filled here (W1-wire's "Seams still open", F7a)

| Seam | Filled with |
|---|---|
| `partitionUnconverted` rows `POST /term/sessions`, `…/restart`, `…/prompt`, `…/cancel`, `…/permissions/{pid}`, `…/elicitations/{eid}`, `…/options` | removed (server/partitionclass.go); `TestPartitionTermRoutesConverted` |
| person terminal layers and partition agent history (wipe hooks, stops) | wipe hook `person-terminals` (broker/partitionterm.go: `stopPersonTerminals`, `wipePersonTerminals`) over `term.Manager.StopTileSessions`/`WipePartitionTile`, installed by boot (`SetPartitionTerminals`) |

### Declared for other packs

| Seam | Signature | For |
|---|---|---|
| `(*Broker).wipePersonTerminalsOf` | `(tile, pkey string, dry bool) (term.PartitionTileWipe, error)`: holds partition id `pkey` on `tile` (`""` every tile), ends its sessions, then deletes its person layers and partition agent history there, then releases; a broker without terminals answers the zero wipe | **F7b**: `POST /partitions/reset` (tile, pkey), `purge` of orphans (tile, pkey), and the users-store delete/orphan sweep (`""`, pkey). Unused until then. A caller that stops and wipes in separate phases must hold the partition across both (`HoldPartition`) |
| `term.Manager.HoldPartition` / `StopPartitionSessions` / `WipePartitionKey` | `(tile, pkey string) (release func())` / `(tile, pkey string) error` / `(tile, pkey string, dry bool) (PartitionTileWipe, error)` | the calls behind `wipePersonTerminalsOf` (the `PartitionTerminals` interface); while held, the person's new sessions answer 409 (`term.ErrPartitionEnding`) — F7b's reset should say so in its docs row |
| `termPartitionRecord` (broker/partitionterm.go, a `var`) | `func(b *Broker, tile, user, uid string)`, called by `TermPartition` for every person's session opening on a partitioned tile; no-op until filled | **F5** (`partitionrecords.go`, PD-43): write or touch the person's partition record (user, uid, created) there, so a users store that lost the uid can re-adopt it and a record-driven sweep (F7b) finds the terminal layer and agent history, which are keyed by H(id, uid) alone |
| session frame `partition`, `partitionNote`; `bx-session` detail `partition`; `SessionInfo.partition` (rows, `term` directory) | additive, partitioned tiles only | **F14**: the terminal's partition chip ("partition: yours · the tile directory is shared code" — web/bx-terminal.js already prints the note as a grey line) |
| `XBIN_PARTITION` in terminals | env | **F7b**: `bx status` printing `partition:` and `bx logs` defaulting to it |
| `SessionInfo.partition`, `Personal()` in the admin sandbox list (names blanked, boot/sandboxes.go); disks kind `person-terminal` | row data | **F12**: the admin runtime → sandboxes view may label partition sessions and person disks (today it renders them by `tile`/`key` like any disk) |

### Merge contention

- `internal/server/partitionclass.go` `partitionUnconverted`: removing F7a's
  seven rows made gofmt realign the whole map (the longest key went), so
  F5's and F7b's row removals conflict textually with it — resolve by
  keeping only the rows neither pack removed, then `gofmt`.
- `web/bx-terminal.js` is at 889 of the 900-line cap for an unlisted JS
  file (888 before F7a): **F14** adds the partition chip there — it has 11
  lines, and must split the chip into its own module (or list the file in
  hack/size-budget.txt) if it needs more; its hunk meets F7a's around the
  `bx-session` dispatch (~line 820).
- `internal/boot/boot.go`: one line after `st.wirePartitionRunner()`.
- `internal/boot/sandboxes.go`: the session row's `Name` (three lines) and
  the disks' tile map (six lines).
- `internal/server/server.go`: two call sites (`ResetEnvFor`,
  `EnvStatusFor`), same length.
- `internal/term/term.go`: one field of `Manager` (`parts`), the
  `partOpened` line in `ServeWS`, `List`'s name blanking.
- `internal/layers/pins.go` (`List`, the tree constants), `internal/vm/
  disk.go` (`ListDisks`, the kind constants), `Makefile` (the `integration`
  target's `./internal/term/` regex).
- The history calls' signatures changed (`homeKey string` →
  `term.HistoryScope`): a pack calling `ListHistory`, `HistoryMeta`,
  `ReadHistory` or `DeleteHistory` passes `term.OwnHistory(home)` or
  `term.HistoryOf(p)`.
- `docs/protocol.md`: rows in §/api/xbin (`GET /status`, `GET /sandboxes`,
  `GET/PATCH /term/sessions`, the agent routes' closing parenthesis, `GET
  /agent/history`) and §/ws/term (the route table, the session frame, one
  paragraph after `DELETE /ws/term`), and the agent-history sentence.
- `docs/partitions.md`: a new section before §In your code, a sentence in
  §The mode, and the TODO bullet now reads "logs and status on partitioned
  tiles" (F7b's).

## Deviations and open ends

- **Admins lose rename too**, not only reattach and drive (kill is the one
  act PD-09 keeps). And **`GET /status` and the admin sandbox list** blank
  the names of person-partition sessions like `?user=` does (06 §3 names
  only `?user=`) — the same agent-written title would leak there.
- **View-as is refused on every session of a partitioned tile**, the
  person's own included, not only on reads of partition data: a
  terminal's transcript and scrollback are the partition's (PD-08). On an
  unpartitioned tile view-as keeps today's read pass.
- **The admin gate fails closed on "partitioned at open or now"**, so a
  tile an empty manifest partitioned after a session opened gets no admin
  pass either; such sessions are also ended by the partition-change hook.
- **`/ws/term/env` (reset, base status) acts on the caller's own layer** on
  a partitioned tile (`ResetEnvFor`/`EnvStatusFor`); the tile's own layer
  is reset only from a session without a person. Not in 06, but the
  terminal window's ⟲ would otherwise reset (and kill the sessions of) a
  layer the person doesn't use.
- **An unreadable mode record**: a person's session is refused (403, no
  layer can be keyed); a session without a person opens with the API off on
  the tile's own layer.
- **Sessions during a switch answer 409** (`ErrPartitionSwitching`) — the
  broker's switch hold, released before the settle's rescan; a session
  opened in that last window is ended by the partition-change hook, but a
  person's session opened there on a tile leaving partitions can leave a
  new, empty person layer behind (a leftover, never mounted until the tile
  is partitioned again, and then only by that person). The partition-change
  hook doesn't wait for opens in flight (a stop does): an open racing the
  rescan registers under the mode it asked in.
- **A stop waits for opens in flight** (bounded by the stop's 15 s, then
  the switch or partition end fails before deleting): an open that blocks
  long — a VM terminal booting — delays the stop by that much.
- **Resuming across modes answers 409** with "start a new one" (the
  status the Agent tab already treats as "offer a fresh start"); a restart
  whose own transcript is in the other store starts fresh instead.
- **An auto flip** (a tile with no data changing mode at once, 01 §2.2 —
  terminal layers and agent history don't count as data) deletes nothing:
  the person layers and partition history stay, readable only by their own
  person, until F7b's orphan sweep/purge or a later switch removes them.
- **Person layers aren't in the boot base-image gate** (`CheckBaseImages`
  scans `.xbin/term`); since the review fix they pin their base (the GC
  keeps it), and one on a base that isn't installed refuses its session
  with the reset hint.
- **Without `--isolate`** a person's terminal on a partitioned tile opens
  as a host shell (as every terminal there), with `XBIN_PARTITION` and a
  note saying it can read every partition; the person's partition API
  calls answer 503 there anyway (partitions run under isolation only).
- **`pathLeftovers`** doesn't list a removed partitioned tile's person
  layers or partition history: each belongs to one person and is mounted
  only by them.
- **Not built here (F7b):** the users-store hooks (delete → move/sweep,
  recreate), `bx doctor`'s untracked files in partitioned tiles'
  directories (06 §1, S16) and its "partitioned without isolation" flag,
  `GET /logs` and `GET /tile-status` (still unconverted), `bx status`'s
  `partition:` line. **Not built here (F5):** the partition record a
  terminal open touches (`termPartitionRecord`).

## Review fixes (pt/f7a-fix)

The review of `pt/f7a` found, and this branch fixes:

1. **View-as read people's partitioned sessions and partition history**
   (high): `MayDrive`/`mayReattach` refuse a read-only principal on a
   partitioned tile, `GET /term/sessions` blanks personal names for
   view-as, and the history calls take `HistoryScope` (view-as: own history
   only). `TestPartitionAdminGates`, `TestPartitionAgentHistory`,
   `TestPartitionTermAdmin` (server: view-as on the session GETs, the
   listing, both history routes; checked failing without the fix).
2. **Person layers didn't pin their base** (high): `layers.List` walks
   `term-part`; `TestPersonLayerPinsItsBase`.
3. **`TestPartitionLayerIsolated` never ran in CI** (medium): the
   `integration` target's term regex adds `TestPartitionLayer`.
4. **Sub-path sessions escaped the switch's stop and the mode-change hook**
   (medium): every session records its owning tile at open (the broker
   answers `Tile` for unpartitioned tiles too); sub-path cases in
   `TestPartitionWipeAndStop`, `TestPartitionModeChangedEndsStaleSessions`,
   `TestTermPartition`.
5. **Nothing held a partition between its stop and its wipe** (medium):
   `HoldPartition` + opens in flight; `TestPartitionHold`,
   `TestPartitionTermSwitch` (hold → stop → wipe → release).
6. **Name blanking keyed on `user:`** (low): `SessionInfo.Personal()`
   (and `sessionPart.personal()` in `GET /status`) covers a person's
   non-primary session.
7. **Person VM disks missing from `/sandboxes`** (low): kind
   `person-terminal`; `TestListDisks`; the protocol row names it and the
   blanked session names.
8. **History across modes** (low): `resumeHere` (and the restart's
   candidate filter); `?cwd=` filters partition entries by exact cwd.
9. **No record of pkey → (user, uid, created)** (low): declared
   `termPartitionRecord` for F5 (above); not filled here.
10. **bx-terminal.js headroom** (low): F7a's hunk trimmed to +1 line (889);
    named in Merge contention for F14.
11. **"partition: yours" without isolation** (low): the note says the host
    shell can read every partition instead.

## Tests (added)

- term: `TestPartitionSessionEnvGolden` (sandboxed and host env,
  partitioned vs not, with a target), `TestPartitionSessionOpen` ($HOME
  start, frame and row fields, the no-isolation note and the isolated one,
  token minted as today, the owner without global → API off and no token,
  with global → `global`; a refused person 403, a switch 409, an agent
  session too), `TestPartitionLayerPerPerson` (keys, dirs, the reset key),
  `TestPartitionAdminGates` (reattach, drive, rename refused, kill allowed,
  own session and unpartitioned tiles as today, a tile partitioned since,
  `GET /status` names incl. a non-primary personal session, view-as),
  `TestPartitionAgentHistory` (real agent host: $HOME cwd, history under
  `.partitions/<pkey>/<TileKey>`, merged listing, sub-path narrowing,
  view-as reads none, resume, resume across stores refused, a new
  incarnation reads none), `TestPartitionWipeAndStop` (a sub-path session
  opened before partitioning stops too), `TestPartitionKeyWipe`,
  `TestPartitionHold`, `TestPartitionModeChangedEndsStaleSessions` (a
  sub-path session); integration (linux, rootfs): `TestPartitionLayerIsolated`
  — a real sandbox: ana's `apt`-style change lands in her own layer, bob's
  terminal doesn't see it, `XBIN_PARTITION` and the `$HOME` cwd inside the
  sandbox, the confined wipe (now in `make integration`).
- broker: `TestTermPartition` (people, an admin, the root token with and
  without global, a non-primary target, no read, unpartitioned → its tile
  alone, a sub-path, the record seam, the switch hold),
  `TestTermPartitionReach` (alice's terminal writes; carol's and an
  admin's terminals on the tile read nothing of it and act in their own
  partitions), `TestPartitionTermSwitch` (removing global touches nothing;
  dry run counts; the switch stops, then wipes, under the old mode; people
  told; `wipePersonTerminalsOf` holds, stops, wipes, releases).
- server: `TestPartitionTermRoutesConverted` (a person's terminal passes
  the gate on every `POST /term/sessions*`, stamped),
  `TestPartitionTermAdmin` (HTTP: 403 on every drive route, rename and
  reattach for an admin on a partitioned tile, 200 on an unpartitioned
  one, `?user=` without names, bob's own listing with them, view-as 403 on
  the session GETs and no partition history, kill 204).
- layers: `TestPersonLayerPinsItsBase`. vm: `TestListDisks` (a person's
  disk).
- Unchanged and green: `TestSessionEnvZeroState`, `TestTermSessionsRoutes`,
  `TestAgentRoutesGates`, `TestPartitionEvents`, `TestPartitionGate`,
  `TestPartitionRouteClasses`, `TestWipeHooksCoverHoldsData`,
  `TestNoPartitionGolden`, `TestZeroStateRoute`, the layers GC tests.
