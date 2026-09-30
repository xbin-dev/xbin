# F7a — records for the integrator

Work pack F7a (terminals and agent sessions on partitioned tiles;
plans/partitions/06-terminals-ops.md §1-§3; PD-09, PD-22, and the terminal
half of PD-10), branch `pt/f7a` on `partitions` at 982718a3. It fills the
seams W1-wire.md left to F7a ("every `POST /term/sessions*` row; person
terminal layers and partition agent history (wipe hooks, stops)"). This
pack edits neither `docs/changelog.md` nor `plans/DECISIONS.md`; the two
texts below go there at merge.

## Changelog entry (docs/changelog.md, under the merge date)

- **Partitioned tiles: terminals and agent sessions belong to their
  person** ([partitions.md](/docs/partitions.md) §Terminals and agent
  sessions, [protocol.md](/docs/protocol.md) §/ws/term, `/term/sessions`,
  `/agent/history`, [09-terminals.md](/docs/overview/09-terminals.md)). On
  a tile whose recorded mode has user partitions, a terminal or agent
  session reaches only its opener's partition: its terminal token already
  acts there, and the session now gets `XBIN_PARTITION=user:<id>` (after
  `XBIN_COMPONENT`, and after `XBIN_DEPLOYMENT` when set; `global` for a
  session targeting a non-primary deployment), starts in `$HOME` instead of
  the tile directory (shared code), and keeps its system changes in the
  person's own dev layer (`.xbin/term-part/<tile-key>/<partition-id>/`,
  their VM disk inside it; `/ws/term/env` resets and reports that one). A
  finished agent session's transcript is kept with the person's partition
  and merged into `GET /agent/history` — never read by a person recreated
  under the same id. A session without a person (the owner token) acts in
  the global instance, or opens with the tile API off (`api:false`) when
  the tile has none. The session frame gains `partition` and
  `partitionNote` (a grey line the terminal prints), session rows
  `partition`. Admins can end another person's session there but get 403
  on reattaching to it, on every agent-session route (events, log, diff,
  prompt, cancel, restart, permission, question and option answers) and on
  renaming it; `?user=` listings, `GET /status` and the sandbox list leave
  such sessions' names out. A person's terminal on the tile may now open
  and drive its own agent sessions (`POST /term/sessions*` answered 403 to
  a partition's credentials until now). A partition mode switch that
  deletes the tile's data ends its sessions first and deletes the person
  layers and partition agent history (the tile's own layer and people's
  own history stay); a new session asked for while the switch runs answers
  409, and a tile whose recorded mode gains or loses user partitions ends
  the sessions opened under the other mode. None of it is backed up.
  Nothing changes for a workspace without a partitioned tile — no env
  line, frame field, row field, path or answer differs. Nothing to change.

## Decision entry (plans/DECISIONS.md, the next free D-number)

- **D&lt;next&gt; — Partitioned tiles, F7a: terminals and agent sessions
  (2026-09-30).** Implements PD-09, PD-22 and the terminal half of PD-10
  of plans/partitions/90-decisions.md; the design is
  plans/partitions/06-terminals-ops.md §1-§3. docs/partitions.md
  §Terminals and agent sessions, docs/protocol.md (§/ws/term,
  `/term/sessions*`, `/agent/history`, `GET /status`),
  docs/overview/09-terminals.md.
  - **Chosen.**
    - **No token change; the broker answers the session's partition at
      open.** `term.Manager` gains three hooks boot installs
      (internal/boot/partitionterm.go): `SessionPartition` = the broker's
      `TermPartition(p, path, dep)` (the owning tile via `Reg.Resolve`;
      `addressedPartition` for the opener; the person's partition id from
      `PartitionIdent`, which mints the uid at their first partition; a
      non-primary target → `global`, PD-17; no person and no global →
      `NoAPI`; a switch of the tile running → `term.ErrPartitionSwitching`,
      409), `TilePartitioned` = `TermTilePartitioned` (recorded mode has
      user, or unreadable), `PersonPartitionKey` (stored uid only, never
      minted). `pickPartition` runs after `pickTarget` on both kinds (and in
      the restart's probe, so a refusal leaves the session running). Nil
      hooks: every session is today's.
    - **What the session keeps** (`sessionPart`, fixed at start):
      `XBIN_PARTITION` after `XBIN_COMPONENT`/`XBIN_DEPLOYMENT` in both env
      paths; the start directory `$HOME` for every session on a partitioned
      tile (the agent's ACP cwd too; its `files.changed` snapshots still
      watch the tile's work tree); the layer key
      `term-part/<TileKey>/<pkey>` (mapped by `layerDir`; a tile's own key
      never holds `/`), so the one-holder lock, VM disk, base stamp and
      reset are per person; the session frame's `partition`,
      `partitionNote`, `api:false` (PD-10), the row's `partition`. A session
      without a person keeps the tile's own layer.
    - **History by partition id**: `data/agent-history/.partitions/<pkey>/
      <TileKey>/` (no user id starts with a dot), merged into
      `ListHistory`/`historyPath` through `PersonPartitionKey`, so list,
      read, delete and resume need no route change; a recreated person
      (new uid) reads none of it.
    - **Admins (PD-09)**: `adminPass` = admin and (own session, or the
      session's tile not partitioned — at open *or now*, fail closed).
      `mayReattach`, `MayDrive` (every drive route through `drive`/
      `driveOther`, `GET /term/sessions/<id>` included) and the new
      `MayRename` use it; the new `MayKill` keeps the admin pass (`DELETE
      /term/sessions/<id>`; `DELETE /ws/term` keeps `CanTouch`). Names:
      `?user=` rows of another person's partition sessions, `GET /status`
      rows and the admin sandbox list blank `name`. Events were already
      F2's (`termEventVisible`).
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
    - **Mode changes without a switch** (an empty tile's manifest decides
      at once): boot's `OnPartitionChange` hook, added after the runner's,
      ends the tile's sessions whose partitioned-at-open flag differs from
      the new recorded mode (`PartitionModeChanged`).
  - **Not chosen:** a per-session partition claim in the terminal token
    (the credential already decides, 02 §3); a term-side hold on the tile
    during a switch (the broker's switch hold covers the whole act and
    can't leak); counting layer bytes in the wipe summary (walking layers
    of GBs on every dry run); `$HOME` only for people (the owner's global
    session in the tile directory would invite shared-code writes too);
    admin rename kept (it is not kill, the one act PD-09 keeps).

## Seams for the integrator

### Filled here (W1-wire's "Seams still open", F7a)

| Seam | Filled with |
|---|---|
| `partitionUnconverted` rows `POST /term/sessions`, `…/restart`, `…/prompt`, `…/cancel`, `…/permissions/{pid}`, `…/elicitations/{eid}`, `…/options` | removed (server/partitionclass.go); `TestPartitionTermRoutesConverted` |
| person terminal layers and partition agent history (wipe hooks, stops) | wipe hook `person-terminals` (broker/partitionterm.go: `stopPersonTerminals`, `wipePersonTerminals`) over `term.Manager.StopTileSessions`/`WipePartitionTile`, installed by boot (`SetPartitionTerminals`) |

### Declared for other packs

| Seam | Signature | For |
|---|---|---|
| `(*Broker).wipePersonTerminalsOf` | `(tile, pkey string, dry bool) (term.PartitionTileWipe, error)`: ends partition id `pkey`'s sessions on `tile` (`""` every tile), then deletes its person layers and partition agent history there; a broker without terminals answers the zero wipe | **F7b**: `POST /partitions/reset` (tile, pkey), `purge` of orphans (tile, pkey), and the users-store delete/orphan sweep (`""`, pkey). Unused until then |
| `term.Manager.StopPartitionSessions` / `WipePartitionKey` | `(tile, pkey string) error` / `(tile, pkey string, dry bool) (PartitionTileWipe, error)` | the calls behind `wipePersonTerminalsOf` (part of the `PartitionTerminals` interface) |
| session frame `partition`, `partitionNote`; `bx-session` detail `partition`; `SessionInfo.partition` (rows, `term` directory) | additive, partitioned tiles only | **F14**: the terminal's partition chip ("partition: yours · the tile directory is shared code" — web/bx-terminal.js already prints the note as a grey line) |
| `XBIN_PARTITION` in terminals | env | **F7b**: `bx status` printing `partition:` and `bx logs` defaulting to it |
| `SessionInfo.partition` in the admin sandbox list (names blanked, boot/sandboxes.go) | row data | **F12**: the admin runtime → sandboxes view may label partition sessions |

### Merge contention

- `internal/server/partitionclass.go` `partitionUnconverted`: removing F7a's
  seven rows made gofmt realign the whole map (the longest key went), so
  F5's and F7b's row removals conflict textually with it — resolve by
  keeping only the rows neither pack removed, then `gofmt`.
- `internal/boot/boot.go`: one line after `st.wirePartitionRunner()`.
- `internal/boot/sandboxes.go`: three lines in the session row (`Name`).
- `internal/server/server.go`: two call sites (`ResetEnvFor`,
  `EnvStatusFor`), same length.
- `docs/protocol.md`: rows in §/api/xbin (`GET /status`, `GET/PATCH
  /term/sessions`, the agent routes' closing parenthesis, `GET
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
  is partitioned again, and then only by that person).
- **An auto flip** (a tile with no data changing mode at once, 01 §2.2 —
  terminal layers and agent history don't count as data) deletes nothing:
  the person layers and partition history stay, readable only by their own
  person, until F7b's orphan sweep/purge or a later switch removes them.
- **Person layers aren't in the boot base-image gate** (`CheckBaseImages`
  scans `.xbin/term`); a person layer on a base that isn't installed
  refuses at session start with the reset hint, as any layer does.
- **`pathLeftovers`** doesn't list a removed partitioned tile's person
  layers or partition history: each belongs to one person and is mounted
  only by them.
- **Not built here (F7b):** the users-store hooks (delete → move/sweep,
  recreate), `bx doctor`'s untracked files in partitioned tiles'
  directories (06 §1, S16), `GET /logs` and `GET /tile-status` (still
  unconverted), `bx status`'s `partition:` line.

## Tests (added)

- term: `TestPartitionSessionEnvGolden` (sandboxed and host env,
  partitioned vs not, with a target), `TestPartitionSessionOpen` ($HOME
  start, frame and row fields, token minted as today, the owner without
  global → API off and no token, with global → `global`; a refused person
  403, a switch 409, an agent session too), `TestPartitionLayerPerPerson`
  (keys, dirs, the reset key), `TestPartitionAdminGates` (reattach, drive,
  rename refused, kill allowed, own session and unpartitioned tiles as
  today, a tile partitioned since, `GET /status` names),
  `TestPartitionAgentHistory` (real agent host: $HOME cwd, history under
  `.partitions/<pkey>/<TileKey>`, merged listing, resume, a new incarnation
  reads none), `TestPartitionWipeAndStop`, `TestPartitionKeyWipe`,
  `TestPartitionModeChangedEndsStaleSessions`; integration (linux, rootfs):
  `TestPartitionLayerIsolated` — a real sandbox: ana's `apt`-style change
  lands in her own layer, bob's terminal doesn't see it, `XBIN_PARTITION`
  and the `$HOME` cwd inside the sandbox, the confined wipe.
- broker: `TestTermPartition` (people, an admin, the root token with and
  without global, a non-primary target, no read, unpartitioned, the switch
  hold), `TestTermPartitionReach` (alice's terminal writes; carol's and an
  admin's terminals on the tile read nothing of it and act in their own
  partitions), `TestPartitionTermSwitch` (removing global touches nothing;
  dry run counts; the switch stops, then wipes, under the old mode; people
  told; `wipePersonTerminalsOf`).
- server: `TestPartitionTermRoutesConverted` (a person's terminal passes
  the gate on every `POST /term/sessions*`, stamped),
  `TestPartitionTermAdmin` (HTTP: 403 on every drive route, rename and
  reattach for an admin on a partitioned tile, 200 on an unpartitioned
  one, `?user=` without names, bob's own listing with them, kill 204).
- Unchanged and green: `TestSessionEnvZeroState`, `TestTermSessionsRoutes`,
  `TestAgentRoutesGates`, `TestPartitionEvents`, `TestPartitionGate`,
  `TestPartitionRouteClasses`, `TestWipeHooksCoverHoldsData`,
  `TestNoPartitionGolden`, `TestZeroStateRoute`.
