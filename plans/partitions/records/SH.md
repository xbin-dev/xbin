# SH — records for the integrator

Work pack SH (shell follow-ups of wave 4; owner rulings 90 §I13 and §I3,
W4-wire's "Looks wrong" 1 and 2), branch `pt/sh` on `partitions` at
3092f0f2. Scaffold (the shell, the admin tile), one core module
(`web/logs-partition.js`, the logs panel's switcher), node tests, a
harness pass and docs. No new HTTP surface, no Go change. This pack edits
neither `docs/changelog.md` nor `plans/DECISIONS.md`: the two texts below
are the integrator's to fold in (the D number is theirs to assign).

## Changelog entry (docs/changelog.md, under the merge date)

- **Partitioned tiles: the shell's settings menu links your partitions
  page; times read the same everywhere; `yours` says where shared things
  come from** ([partitions.md](/docs/partitions.md) §Partitioned tiles,
  §Your partitions page, §Operating people's partitions).
  - Once you see a partitioned tile, the shell's **settings** menu has
    **your partitions** under *my account*: the partitioned marker's shape
    and a link to `/xbin/partitions` in a new tab (the page opens only
    top-level). It shows for a signed-in person only — not for the
    workspace token, nor for an admin viewing the workspace as someone —
    and comes and goes with the tiles you see.
  - On a tile that also runs a global instance, the window chip `yours`
    keeps its word — your window's calls reach your partition — and its
    tooltip now adds that anything the tile shares with everyone who uses
    it (a partitioned agent's shared conversations, for example) comes
    from its global instance, not from your partition.
  - The admin console's partitions view shows times in your browser's
    zone with the zone named ("2026-09-30 17:46 GMT+2"), as the partitions
    page does; it showed UTC without saying so. The logs panel's "shared
    with you" entry names its end the same way (it showed a UTC date).
  - The shell and the admin tile are scaffold: existing workspaces get the
    menu entry, the tooltip and the console's times with `bx builtin
    update scaffold:shell` and `bx builtin update scaffold:tiles/admin`;
    the logs panel ships with xbind. Workspaces without a partitioned tile
    look exactly as before. Nothing to change.

## Decision entry (plans/DECISIONS.md, the next free D-number)

- **D<next> — Partitioned tiles, SH: the settings menu's "your partitions",
  one time format, and `yours` on a tile with a global instance
  (2026-09-30).** Implements the owner's ruling I13 (a user-menu entry to
  `/xbin/partitions`, shown when the workspace has a partitioned tile) and
  settles two things W4-wire flagged: the admin console's times without a
  zone beside the partitions page's named zone, and the chip `yours` on a
  partitioned agent's window, which since B2b also shows the global
  instance's shared conversations (I3). Design:
  plans/partitions/90-decisions.md §I3, §I13; records/W4-wire.md "Looks
  wrong" 1–2; docs/partitions.md.
  - **Chosen.**
    - **The entry lives in the settings menu's *my account* block**, after
      **devices…** and styled like it: the shell's per-person menu (its
      "user menu"), where a person's own things already are. Label
      **your partitions** (the menu's lowercase words), the marker's
      half-split disc in its teal before it and ↗ after it (it leaves the
      shell), tooltip "Your partitions page: your own data in each
      partitioned tile, the consents and personal binds you gave, and the
      switches you decide — xbind's page, in a new tab". A plain link
      (`target=_blank rel=noopener`): the page refuses frames, so it opens
      top-level; choosing it closes the menu.
    - **When it shows** (`partition-mode.js pageEntry`): a signed-in
      person (`consentPerson`: not view-as — the page shows view-as none
      of that person's partitions —, not the workspace token, which has no
      partitions of its own) whose `/components` listing holds a
      partitioned tile — the marker's rule, `anyPartitioned` (a recorded
      mode with user partitions; a switch into partitions still pending
      isn't one yet, and its card links the page with **details…**; one
      out of partitions still is, while people's partitions exist). The
      same rule as the consent prompts' `consentWatch`, which now calls
      it. "The workspace has a partitioned tile" is read from the tiles
      the viewer sees, so it needs no new request or field, and follows
      the listing live (the shell reloads it on `reload`/`users`/`grants`
      events).
    - **The "my account" block moved to a module of its own**
      (`workspace-template/shell/shell-account.js`: `accountMenu(shell)`
      — the identity line, the password form, devices…, the entry): bx-shell
      sat at its size budget (1892/1892) and now renders
      `${accountMenu(this)}` — 1853 lines. The password change is the same
      calls and messages. The marker's drawing is `shell-kit.js
      markShape`, shared by the marker and the entry.
    - **One time format for partitions:** the admin console's partitions
      view (`partitions-view.js when`) and the logs panel's shared-log
      entry (`web/logs-partition.js`) use the partitions page's format —
      local time, zone named ("2026-09-30 17:46 GMT+2", `Intl`
      `timeZoneName: 'short'`) — where they showed UTC unmarked
      (`toISOString`, minutes or the date). The logs module imports the
      page's `timeText` (core to core, one binary); the admin tile is
      scaffold, so it keeps a copy (a scaffold importing a core module's
      non-contract export would break when that export moves), and
      `hack/admin-partitions.test.mjs` asserts the two agree.
    - **`yours` stays `yours` on a tile with a global instance; its
      tooltip names the global instance** (`CHIP_YOURS_GLOBAL`): "Your
      partition: this window shows your own data in this tile — everyone
      who uses it has their own, and nobody else's partition shows here.
      Anything here that the tile shares with everyone who uses it (a
      shared chat, for example) comes from its global instance, not from
      your partition". The chip names the partition the window's calls
      reach and its credential — which a page's shared view doesn't change
      — so it is accurate; the tooltip removes the misreading ("nobody
      else's data shows here") for every tile with a global instance, not
      only the agent: any such tile's window may show global's data (F5).
      A tile without one keeps F14b's tooltip, with "nobody else's" made
      "nobody else's partition" (a shared resource written by someone else
      may show on any partitioned tile).
  - **Not chosen:** the entry at the settings menu's top beside **add a
    device** (that slot is the menu's one call to action, and the harness
    pins it as the first item); a top-bar chip (the bar is the workspace's,
    not the person's; the menu is per person); showing it for the
    workspace token (the page offers it only the switch decisions, which
    each paused card links) or for view-as; an xbind field or request for
    "the workspace has a partitioned tile" (the viewer's own listing
    answers it, and a tile they can't see isn't theirs to open a partition
    in); a chip that follows the open conversation (`global` while a
    shared conversation shows — it needs a frame → shell message the agent
    would have to send, applies to one template, and would make the chip
    change within one window whose calls still reach the person's
    partition); a fifth chip word (`yours + global`, `mixed`: I3 names
    three, and W4 built a fourth only where no partition is reached);
    UTC with its name in the console (the page shows local time: one
    format was asked for, the page's).

## Seams for the integrator

- **`workspace-template/shell/bx-shell.js`** — now 1853 lines against its
  1892 budget (the account block moved out: net −39). Hunks: one import
  line after `openDevices`' (`accountMenu`), the `_accountMenu` /
  `_changePassword` methods removed (before `_sideResizeStart`), and
  `${this._accountMenu()}` → `${accountMenu(this)}` in the settings menu.
  A later pack may lower the budget to lock the gain in
  (`hack/size-budget.txt`); I left it at 1892 so parallel packs' merges
  don't trip over a number moved under them.
- **`workspace-template/shell/partition-mode.js`** (F14/F14b's file) — the
  chip block (`CHIP_YOURS` words, `CHIP_YOURS_GLOBAL`, one line of
  `partitionChip`), `consentWatch`'s body and `anyPartitioned` after it,
  and a section at the end (`pageEntry`, `PAGE_ENTRY`,
  `PAGE_ENTRY_TITLE`); the header comment names shell-account.js.
- **`shell-kit.js`** — `partitionMark` renders `markShape` (now exported).
- **`shell-css.js`** — `.wsmenu a.parts` rules after the add-device ones.
- **`hack/ui-harness/shots.js`** — `PASSES.partitionsEntry = …` on its own
  line after `channelsPartitioned`'s: 848/877 lines.
- **`web/logs-partition.js`** imports `./partitions-kit.js` (F11's) for
  `timeText`: F11's module becomes one bx-logs loads too; renaming
  `timeText` must update both (node tests cover both).
- **The admin tile's other tabs** (sessions, users, backup, tilesbx) show
  times with `toLocaleString`/`toLocaleTimeString` (local, unnamed):
  outside the partitions view, not touched. A console-wide format is a
  later clean-up if wanted.
- **The xbin app** has no settings menu entry (native programme): the
  pushes already link `xbin/partitions`.
- Nothing filled of another pack's; no Go hook declared.

## Deviations

- **The logs panel's shared-log tooltip** changed too (F12's core module,
  beyond the admin console the flag named): the same fact — a person's log
  share's end — reads as on the page and in the console. It showed a UTC
  date.
- **`CHIP_YOURS` gained one word** ("nobody else's partition shows here"),
  see the decision. F14b's harness regex (`/^Your partition: this window
  shows your own data/`) is unchanged and passes.
- **The whole "my account" block moved** (not just the new entry) to make
  room in bx-shell.js; the password change's code was tightened (one
  `say` helper for the message and its 4 s timer), same calls, same texts.

## Tests

On the branch's final tree (.dev.mk's environment exported; the Bash
sandbox off for the isolated runs and the harness):

| Run | Result |
|---|---|
| node: `hack/partition-mode.test.mjs` (23: + the entry's rule — a person who sees a partitioned tile; not the token, view-as, an unread `/whoami`; a pending switch into partitions no, out of partitions yes; global-only no — and `yours` with a global instance) | PASS |
| node: `hack/admin-partitions.test.mjs` (13: + times: the viewer's zone, named, equal to the page's `timeText` for three inputs; the request's "since"), also under `TZ=UTC` and `TZ=Pacific/Kiritimati` | PASS |
| node: `hack/logs-partition.test.mjs` (7: the shared log's end in the page's format) | PASS |
| `make fmt-check vet js-check js-test` (507 JS tests: 506 pass, 1 skipped as before) | PASS |
| repo guards: `go test . ./internal/assetscan ./internal/sizebudget ./internal/docscheck ./internal/builtins ./internal/apicheck`; `go test ./internal/registry -run Shell` | PASS |
| isolated: `TestPartitionsPage` (the page and its reads on a real `--isolate` xbind) | PASS (15.9 s) |
| isolated: `TestPartitionsSmoke` (19 subtests) | PASS (43.5 s) |
| `hack/tile-check.sh` | n/a: no builtin tile or template touched (the shell and the admin tile are scaffold) |
| Go `-race` | n/a: no Go changed |

**UI harness** (PORT 8981, HARNESS_DIR …/scratchpad/h5-SH, a fresh seed,
unisolated, one run in this order, stopped after):

| Pass | Checks |
|---|---|
| devices (the moved account block: password form, devices…) | 48 PASS |
| appHelp, gridScale, windows, menus | 4, 11, 16 PASS; menus ran clean |
| **partitionsEntry** (new) | 15 PASS |
| partitionSwitch (the old shell) | 14 PASS, unchanged |
| adminTabs | 25 PASS |
| partitionMark (the chip's `yours` regex, F14b's) | 46 PASS |
| partitionConsent (`consentWatch` now calls `anyPartitioned`) | 36 PASS |
| personPage | 47 PASS |
| adminPartitions (the new times) | 36 PASS |
| partitionLogs | 13 PASS |

No FAIL, no SKIP. An earlier `--shots` rerun on a reused workspace failed
`devices` at "add a device": the adminPartitions pass leaves two tiles
pending, whose `/alerts` banners, for the admin, cover the settings
menu's first item — pass order on a reused workspace, not this change
(the fresh run above puts `devices` first and passes).

Screenshots looked at: `partitions-entry-absent` (the account block
without the entry), `partitions-entry` (the entry after devices…: the
teal half disc, "your partitions ↗", the width of devices…),
`partitions-entry-chip` (apps/pentry's head: marker, path, `yours`),
`partitions-entry-page` (the page opened from the entry: "Your
partitions", signed in as P Entry, apps/pentry `user + global` listed),
`admin-partitions-tile` (times "2026-09-30 19:33 GMT+2" in the people's
log share, personal binds, mode history, the waiting requests and the
orphans), `devices-menu` (the admin's account block, no entry on a
workspace without partitioned tiles).

## Owner questions

1. **Should the chip follow the open conversation?** Built: `yours`
   always names the partition the window's calls reach; on a tile with a
   global instance its tooltip says that what the tile shares (a shared
   chat) comes from the global instance. The alternative is `global`
   while the agent shows a shared conversation — a frame → shell message
   the agent sends on each switch, agent-only. Recommendation: keep it as
   built (the agent's own Shared view marks what is shared; a chip that
   changes within one window blurs what it promises).
