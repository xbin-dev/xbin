# Tile deployments: pausing live reload

Most tiles never use any of this. Every save reaches everyone: the frame
reloads, the backend rebuilds and swaps, and there is **no deploy step**.
That stays true for every tile that never opts in, byte for byte — nothing on
this page changes a tile until someone on it pauses live reload.

A tile's developers can change that, per tile. **Pausing live reload** keeps
the tile running the code it runs now while the files go on changing:
viewers, callers and every restart keep that code until someone presses
**Reload now** or **resumes live reload**. Along the way xbin keeps a
**deploy log** of what the tile ran, so you can **roll back**, diff what runs
against the work tree, and branch from exactly what runs with
`git fetch xbin-deploy`.

In this release a tile has one deployment, `main`, which is its **primary**:
what everyone and everything else reaches. Named deployments (`dev`, …) with
their own URL and data, promotion between them, protecting the primary and
the rest of the tile-deployments routes are reserved: they answer 501, and
`GET /api/xbin/deployments` lists `features: ["live-reload/1"]`
([protocol.md](/docs/protocol.md) §Tile deployments).

## The words

| Word | Meaning |
|---|---|
| **work tree** | the tile's directory as terminals, agents and your editor change it |
| **live reload** | a save in the work tree reloads frames and rebuilds the backend — today's behaviour. It follows one deployment, the **live reload target** (`main`), or none |
| **paused** (live reload) | live reload follows no deployment: saves change the files and nothing else |
| **checkpoint** | an immutable capture of the tile's files, named by its content: `c:3f2a1c9` |
| **pinned** | a deployment that isn't the live reload target runs a checkpoint and is pinned to it: "main is pinned to c:3f2a1c9" |
| **deploy** | put a checkpoint on a deployment, through the usual build, health check and swap |
| **reload now** | while paused: checkpoint the work tree and deploy it once; the deployment stays pinned |
| **resume** (live reload) | the deployment follows the work tree again |
| **roll back** | deploy an earlier checkpoint from the deploy log |
| **deploy log** | a deployment's history of attempts: who, when, how, which checkpoint, failed ones included |
| **primary** | the deployment that receives everything from outside: the bare URLs `/c/<tile>/` and `/api/<tile>/`, grants, bindings, cron, bus deliveries, ingress. Here: always `main` |

Pausing live reload is not **disabling** the tile (`bx disable`, the tile
menu's Disable): a disabled tile stops serving; a tile with live reload
paused keeps serving, from its checkpoint.

## Where do my saves go?

```sh
bx live-reload            # this terminal's tile; or: bx live-reload apps/crm
```

| It says | Meaning |
|---|---|
| `main — every save reaches everyone (no deployments)` | the zero state: nothing on this page applies |
| `paused by ana 12m ago — 3 files changed since c:3f2a1c9 (main)` | saves reach nobody; `main` runs `c:3f2a1c9` |
| `… — the last deploy to main failed; main keeps running c:3f2a1c9` | a deploy failed, and nothing anyone uses changed |

In the browser the terminal window says the same: in the zero state its bar
has one quiet entry, `⇈`, whose menu offers **Pause live reload**. While live
reload is paused the entry becomes a chip, `📌 Live reload paused · 3`,
counting the files changed since the checkpoint, next to **⇡ Reload now**;
the chip's menu has Reload now and **Resume live reload on ▸**. The terminal
window's launcher shows a banner while live reload is paused, people with
terminal access see a `📌 pinned` chip over the tile itself (a click opens
the window), and every open terminal of the tile prints a grey line when live
reload pauses or resumes, or code moves or fails to.

Programs can ask `GET /api/xbin/deployments?tile=<tile>` (`record: false` is
the zero state) or read `deployments: {primary, pinned, protected}` on the
tile's `/api/xbin/components` entry, present only for a tile that has left
the zero state.

## Pausing live reload

```sh
bx live-reload pause      # or: ⇈ → Pause live reload, in the terminal window
```

- **What it does.** `main` is pinned to a fresh checkpoint of the work tree,
  taken when the request commits. Normally that is exactly the code already
  running, so nothing visible happens. If the work tree moved since the last
  build (a save still building, a failed build), pausing ships it once, now;
  the dry run says so before you confirm.
- **Saves while paused** change the files and nothing else: no frame reloads,
  no backend rebuilds. The terminal window counts the files that differ from
  the checkpoint.
- **Who may.** Anyone with terminal access to the tile, and the tile's own
  terminal and agent sessions — the same people whose saves go live today.
  A tile's frontend and backend can't.
- **The first pause is the opt-in.** It creates the tile's deployment record
  (`data/deployments/`) and its checkpoint store (`data/checkpoints/`), both
  xbind-owned and invisible to terminals. Before that, nothing is written for
  the tile — reading its state, dry runs and diffs included.
- **Backends need isolation.** Pinning a `go`, `node` or `python` backend to a
  checkpoint needs an xbind started with `--isolate`: without it the pause
  is refused with `pinning a backend to a checkpoint needs isolation
  (--isolate)`, and the tile keeps live reload. Static tiles pause
  everywhere.
- It is idempotent: pausing live reload again changes nothing.

## What runs while live reload is paused

Everything the tile serves and runs comes from the pinned checkpoint:

- **Its pages.** `/c/<tile>/…` serves the checkpoint's files, in every asset
  mode, still `Cache-Control: no-store`; saves don't show. A symlink in the
  checkpoint that points outside it answers 404; `deps/<name>/…` is served
  as the dependency tile's own `/c/` path (its primary, under its own access
  rules). `?native=1` is generated from the checkpoint too.
- **Its backend.** The backend runs the checkpoint, which its sandbox sees
  read-only at the tile's own path; `setup` runs against it. The build is
  kept per checkpoint, so every restart — a crash, the idle reaper, a grant
  or binding change, alwaysOn, an unseal, an xbind restart — starts the same
  code without compiling. A pinned backend never falls back to the work tree:
  an xbind restarted without `--isolate` holds it, and its `/api/` answers
  `pinning a backend to a checkpoint needs isolation (--isolate)` while its
  pages keep serving the checkpoint.
- **Its manifest, in two halves.** How the tile runs and what it offers come
  from the checkpoint: `runtime`, `entry`, `setup`, `alwaysOn`, `vm`,
  `inject`, `native`, `exposes`, `expose` (its roles), `provides`,
  `template`, `chrome`, and `scope.json`'s resources and import map. What
  the tile asks for stays the work tree's: `uses`, `interfaces` and `deps` —
  a new `uses` entry files a pending grant as it does today, and a pinned
  backend's `XBIN_RES_*` covers the work tree's and the checkpoint's `uses`,
  still filtered by grants. `/api/xbin/components` follows the same split:
  `runtime`, `hasIndex`, `native`, `chrome` and `template` describe the
  checkpoint; `roles`, `uses`, `deps` and `manifestError` the work tree.
- **A broken work tree doesn't take the tile down.** While live reload is
  paused, the tile stays registered as long as its directory exists, even
  with no valid `xbin.json` or `index.html`; `bx ls`, `bx doctor` and
  `manifestError` then say `the work tree has no valid xbin.json; serving
  the pinned primary`. A checkpoint whose `scope.json` doesn't parse, or
  names a resource the naming rule refuses, can't be deployed.

## Reload now

```sh
bx live-reload now        # or: ⇡ Reload now, in the terminal window
```

Checkpoint the work tree and deploy it once to `main`, which stays pinned:
later saves wait for the next Reload now. When the work tree equals the
checkpoint `main` runs, nothing moves (`unchanged: true`). The confirmation
names the checkpoint it will ship, and the request carries it (`expect`): if
the work tree changed between the two, the answer is 409 `the code changed
since you reviewed …` and nothing ships.

## Resuming live reload

```sh
bx live-reload resume     # or: Resume live reload on ▸ main
```

`main` follows the work tree again: the work tree is built, health-checked
and swapped in, and every later save reaches everyone. Resuming onto `main`
while every setting is at its default removes the deployment record, so the
tile is back in the zero state (`bx` says `apps/crm is back to plain live
reload, with no deployments.`). The checkpoint store and its deploy log stay
on disk, unread, until the tile opts in again; deploy ids keep counting from
where they were.

Resuming ships the work tree to everyone. Pause and resume are cheap, so
leave a pause someone else made alone unless you were asked; `bx live-reload`
says who paused and when.

## Deploying, restarting and rolling back `main`

```sh
bx deploy --to main                        # a fresh checkpoint of the work tree
bx deploy --to main --checkpoint c:1e9d0aa # a checkpoint from the deploy log
bx rollback --to main                      # the newest successful deploy with other code
bx rollback --to main --checkpoint c:1e9d0aa
```

- A code move never guesses its target: `--to` names it (or a tile ref with
  the qualifier, `apps/crm+main`).
- A deploy onto the live reload target pauses live reload, since the next
  save would silently overwrite it. `bx` says where saves go afterwards.
- `rollback` without `--checkpoint` takes the newest `ok` entry of the
  deploy log whose checkpoint differs from the current one; with none, 409
  `main has no earlier checkpoint in its deploy log`. Data stays as it is:
  a roll back moves code, not state.
- **Restart**, without moving code: `POST /api/xbin/deployments/deploy
  {"tile": "<tile>", "deployment": "main", "restart": true}` starts a new
  generation of the current code, even while it is healthy, and clears the
  crash breaker. Deploying the checkpoint `main` already runs changes nothing
  (`unchanged: true`) while its backend is healthy. When the backend is
  crash-looping, failed or not running, or its last deploy failed, the same
  deploy starts it again from the kept build (logged as `restart`) and
  clears the crash breaker.
- **Deploys are asynchronous.** One runs per deployment and up to eight wait
  behind it; a request for the checkpoint already last in line joins that
  entry; a ninth answers 409 `main already has 8 deploys waiting; try again
  when one finishes`. `bx` waits for the result (up to 20 minutes;
  `--no-wait` returns once accepted) and prints each phase: checkpoint,
  materialize, build, start, swap.
- **A failed deploy changes nothing anyone uses.** The previous generation
  keeps serving, no build-error overlay is painted, and the compiler output
  goes to the backend log (`bx logs`) under `--- deploy of c:<id> failed
  <time> ---` and into the deploy log's `error`. If the failed attempt was
  moving `main` off the work tree (a pause or a deploy while live reload was
  on it), live reload stays paused and `main` stays pinned to the attempted
  checkpoint: every restart runs it, and Reload now retries it.
- A pinned backend that crash-loops says `deploy a fixed checkpoint or
  restart it`: a save doesn't reach it, while a deploy of the checkpoint it
  runs, or a restart, brings it back.
- Frames reload once after a swap that changed the code a deployment serves
  (reload now, deploy, roll back, resume). Pausing and restarts don't reload
  anything.
- If xbind stops mid-deploy, the next boot writes the attempt into the deploy
  log: a queued one as cancelled, one that had swapped as ok, one still
  running as failed with `interrupted: xbind restarted mid-deploy; main runs
  <code>`. `main` keeps the code its record names.

### Asking before shipping

Every changing `bx` command first sends a dry run and prints what will
happen — Code, Data, Pauses, Affects — then acts. A command that moves code
onto the primary (`deploy` and `rollback` to `main`, and `live-reload now` or
`resume` when they change what it runs) asks `Deploy the work tree to main?
[y/N]` on a terminal; without one it stops with exit code 4 unless you pass
`--yes`. `--dry-run` prints the report and changes nothing. Exit codes:
0 done, 1 failed, 2 usage, 3 refused (not yours to do — the message says who
can), 4 not confirmed, 5 still running when bx stopped waiting, 6 this xbind
has no tile deployments ([bx.md](/docs/bx.md) §Live reload, deploy, roll
back).

## The deploy log and the diff

```sh
T=apps/crm
curl -s -H "Authorization: Bearer $XBIN_TOKEN" "$XBIN_URL/api/xbin/deployments/log?tile=$T"
curl -s -H "Authorization: Bearer $XBIN_TOKEN" "$XBIN_URL/api/xbin/deployments/diff?tile=$T"          # main → the work tree, a git patch
curl -s -H "Authorization: Bearer $XBIN_TOKEN" "$XBIN_URL/api/xbin/deployments/diff?tile=$T&stat=1"   # per-file counts
```

- The log lists every finished attempt newest first, failed ones included,
  with who, when, how (`pause`, `reload-now`, `resume`, `deploy`,
  `rollback`, `restart`), from which session, and the checkpoint before and
  after. `?id=<id>&wait=<s>` waits (up to 25 s) for one attempt to finish.
- The diff compares two of `c:<id>`, `deployment:main` and `work-tree`
  (default: what `main` runs → the work tree); `path=<file>` narrows it to one
  file. It runs confined and bounded: one diff per tile at a time with one
  waiting (else 429), 30 s at most (else 504), patches cut at 16 MiB. A
  work-tree side takes a checkpoint and needs terminal access. The answer
  names both checkpoints in `X-XBin-Checkpoint-From` and `-To`.
- Both need write access to the tile. A tile in the zero state has nothing
  to diff: 409, and nothing is captured.

## Branching from what `main` runs: `git fetch xbin-deploy`

To fix what `main` runs while the work tree holds unfinished work:

```sh
git fetch xbin-deploy
git checkout --no-track -b hotfix deploy/main
# fix, commit, then — once the user says ship:
bx deploy --to main
```

- Terminal and agent sessions opened while the tile has a deployment record
  get a fetch-only git remote, `xbin-deploy`, set only in the session's
  `GIT_CONFIG_*` variables — nothing is written to the tile's `.git/config`.
  `git fetch xbin-deploy` brings `deploy/<name>` for every pinned deployment:
  the checkpoint it runs, minus the files your own ignore rules exclude, so a
  branch made from it never tracks `node_modules` or `.env`.
- `deploy/main` exists only while `main` is pinned.
- Its commits are parentless — they share no history with your own — and
  carry `Xbin-Checkpoint` and `Xbin-Work-Tree-Head` trailers, the latter
  naming the commit the checkpoint was taken on, a base for `git rebase
  --onto`.
- `--no-track` keeps git from recording `xbin-deploy` as the branch's
  upstream, a remote that exists only in some sessions.
- A session opened before the record existed (the usual case: you paused
  from a window with a shell already open) has no `xbin-deploy`; open a new
  terminal, or fetch by URL, which any session's git can:
  `git fetch http://xbin/api/xbin/checkpoints/<tile>.git '+refs/heads/deploy/*:refs/deploy/*'`.
  A fetch that races a refresh can fail once; fetch again.
- Who can fetch: the tile's terminal and agent sessions while their user has
  write access, and people with write access.

## Committing never deploys

Commit as often as ever. A commit changes nothing anyone runs: only a save
(while live reload is on), Reload now, resume, deploy or roll back does.

## For coding agents

The rules for an agent working on a tile, beyond the workspace `AGENTS.md`:

- **Check before you test or debug:** `bx live-reload`. While it says
  `paused`, your saves reach nobody: a fix that "didn't work" may simply not
  be running.
- **Shipping to the primary is a deliberate act.** Run `bx live-reload now`,
  `bx live-reload resume`, `bx deploy --to main` or `bx rollback --to main`
  only when the user asked you to ship — never as part of committing,
  testing or tidying up. Without a terminal bx prints what will change and
  stops (exit 4); `--yes` says the user asked.
- **Don't `bx deploy --to` the live reload target**: saves already reach it,
  and a deploy onto it pauses live reload.
- **A large change:** pause live reload, build and test in the terminal, and
  resume when done — resuming ships the work tree, so only when the user
  asked. Leave a pause someone else made (`bx live-reload` says who) unless
  the user asked.
- **A refusal is not yours to work around** (exit 3): stop and tell the user
  who can.
- "3 fast crashes ⇒ failed" still holds; a pinned backend recovers through a
  deploy or a restart, not a save.

## Limits

- **What a checkpoint holds:** every file under the tile's directory except
  `.git` directories and nested components; ignore rules don't apply.
  Symlinks stay symlinks and the exec bit is kept; empty directories, other
  permission bits, xattrs and special files are not captured. A directory
  holding its own git repository is captured as plain files under
  `--isolate`; without isolation the checkpoint is refused, naming it.
- **Caps per checkpoint:** 200 000 entries, 2 GiB of content, 256 MiB for one
  file, 1024-byte paths, 64 levels deep, 5 minutes. A refusal names the
  largest paths: large data belongs in a resource or outside the tile.
  Checkpoints of one tile are rate-limited: ten at once, then one every
  3 s.
- **Retention:** after each successful deploy xbind collects the tile's
  checkpoint store. It keeps every deployment's current checkpoint, the
  checkpoints of each deployment's last 20 successful deploys, and anything
  younger than 24 hours, and trims each deploy log to its last 50 entries.
  An extracted tree stays under `.xbin/deploy/` for each deployment's
  current and previous checkpoint. Kept builds under `.xbin/build/` stay for
  each deployment's current checkpoint and its previous three successful
  deploys, so restarting or rolling back to one of them reuses its build.
  Extracted trees and builds are rebuilt from the store on demand, so
  deleting `.xbin/deploy/` or `.xbin/build/` while xbind is stopped is safe.

## A tile's life, backups and downgrades

- **Transfer** (`bx owner --transfer`) moves the tile's deployments with it:
  live reload stays paused.
- **Removing and re-creating.** A tile created at a path never inherits a
  removed tile's deployments: creation drops the path's deployment record, so
  the new tile starts in the zero state. The old checkpoint store stays, a
  leftover like a removed tile's grants: a non-admin can't create over it
  ([auth.md](/docs/auth.md), *Creating tiles*).
- **Backups** of a tile that has left the zero state carry its deployment
  record and checkpoint store, under `deployments/` in the archive; a tile in
  the zero state gets exactly today's archive. A restore refuses, before it
  writes anything, an archive whose deployment state belongs to another tile.
  This release doesn't put deployment state back yet: a restore leaves the
  archived record and store out (and logs it), so a tile restored where none
  stood starts in the zero state
  ([overview/14-lifecycle.md](/docs/overview/14-lifecycle.md)).
- **An older xbind** ignores every record and store, and serves every tile's
  work tree with live reload. Before downgrading, check out each pinned
  tile's checkpoint in its work tree (`git fetch xbin-deploy`, commit or
  stash unfinished work, `git checkout --no-track -b pre-downgrade
  deploy/main`), and tell agents that saves go live everywhere. After
  upgrading again, a pinned tile returns to its checkpoint; if you skipped
  the checkout, deploy the work tree at once (`bx deploy <tile> --to main`).
- **Restarting without `--isolate`** holds every pinned backend (see above).
  Before that, resume live reload on those tiles.

## The operator's switch

`--tile-deployments=off` (`XBIN_TILE_DEPLOYMENTS=off`) closes opting in: no
tile can pause live reload, and `GET /api/xbin/deployments` lists no
`features` and reports why in each refusal. It never unpins anything:
existing records keep governing what runs. What leads back to the zero state
or creates no state stays open — in this release, resuming live reload onto
`main` and restarting — so every tile can return to plain live reload
without a downgrade ([config.md](/docs/config.md)).
