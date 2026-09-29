# Tile deployments: live reload, deploy, promote

Most tiles never use any of this. Every save reaches everyone: the frame
reloads, the backend rebuilds and swaps, and there is **no deploy step**.
That stays true for every tile that never opts in, byte for byte — nothing on
this page changes a tile until someone on it pauses live reload or adds a
deployment.

A tile's developers can change that, per tile, in two steps:

- **Pausing live reload** keeps the tile running the code it runs now while
  the files go on changing: viewers, callers and every restart keep that code
  until someone presses **Reload now** or **resumes live reload**.
- **Adding a deployment** — `dev`, say — gives the tile a second runtime with
  its own URL (`/c/<tile>+dev/`), its own data and secrets, its own backend
  and logs. Live reload can follow `dev` while `main` stays put; when `dev`
  looks right, **promote** its code to `main`.

Along the way xbin keeps a **deploy log** of what each deployment ran, so you
can **roll back**, diff what runs against the work tree, and branch from
exactly what runs with `git fetch xbin-deploy`.

## The words

| Word | Meaning |
|---|---|
| **work tree** | the tile's directory as terminals, agents and your editor change it |
| **tile deployment** (here: **deployment**) | a named runtime of one tile — `main`, `dev`, … — with its own code, data, secrets, backend, logs and URL. It shares the tile's path, grants and bindings; it is not a new tile |
| **`main`** | every tile's first deployment. It keeps the storage the tile has always used, and can't be removed |
| **primary** | the deployment that receives everything from outside: the bare URLs `/c/<tile>/` and `/api/<tile>/`, other tiles' calls, grants, bindings, interface instances, ingress, notifications. `main`, unless a tile manager reassigns it. (Cron jobs and bus subscriptions are each deployment's own: they deliver to the deployment that registered them.) |
| **non-primary deployment** | any other deployment: reachable only at its **deployment URL** `/c/<tile>+<name>/`, by people who write the tile and by the tile's own terminals and agents |
| **live reload** | a save in the work tree reloads frames and rebuilds the backend — today's behaviour. It follows one deployment, the **live reload target**, or none |
| **paused** (live reload) | live reload follows no deployment: saves change the files and nothing else |
| **checkpoint** | an immutable capture of the tile's files, named by its content: `c:3f2a1c9` |
| **pinned** | a deployment that isn't the live reload target runs a checkpoint and is pinned to it: "main is pinned to c:3f2a1c9" |
| **deploy** | put a checkpoint on a deployment, through the usual build, health check and swap |
| **promote** | give deployment B exactly deployment A's code. Code only: B keeps its data, secrets and routing |
| **reload now** | while paused: checkpoint the work tree and deploy it once to the deployment live reload last followed, which stays pinned |
| **resume** (live reload) | a deployment follows the work tree again |
| **roll back** | deploy an earlier checkpoint from the deploy log |
| **deploy log** | a deployment's history of attempts: who, when, how, which checkpoint, failed ones included |
| **target** (of a terminal) | the deployment a terminal or agent session's API calls and `bx` reads reach: the primary, unless someone picked another in the terminal's tile API select |
| **deployment data** | a deployment's own resources (kv, sqlite, blob, filesystem, bus) and vault. `main` has the tile's; every other deployment starts empty |
| **dormant** | an interface instance or ingress host that a non-primary deployment registered (stored and answered with success, never routed), or a cron job or bus subscription of a deployment whose deliveries are off (stored, never fired) |
| **deliveries** | a per-deployment off switch, on by default: while it is on, a non-primary deployment's cron jobs and bus subscriptions fire for it; a tile manager can turn it off |
| **assigned branch** | a branch of the tile's repository that a non-primary deployment requires: the work tree feeds it only while it has that branch checked out (§Assigned branches) |
| **edge policy** | per edge of the tile (a call grant, an interface binding, the net, a capability), what its non-primary deployments may use: `read`, `inherit` or `block` |
| **protected** (primary) | only tile managers change the primary's code, in their own session, naming the checkpoint they reviewed |
| **tile managers** | the tile's owner, its org's admins and workspace admins — as for every other tile setting. Their acts here are done in a person's **own session**: the browser, or `bx` with the root token on the host — never a terminal or agent session (§Managing protection) |

Pausing live reload is not **disabling** the tile (`bx disable`, the tile
menu's Disable): a disabled tile stops serving; a tile whose live reload is
paused keeps serving, from its checkpoint.

## Where do my saves and my calls go?

```sh
bx live-reload            # this terminal's tile; or: bx live-reload apps/crm
bx deployment ls          # every deployment: role, code, status, data, and "← this terminal"
```

| `bx live-reload` says | Meaning |
|---|---|
| `main — every save reaches everyone (no deployments)` | the zero state: nothing on this page applies |
| `main (primary) — every save reaches everyone` | the tile has deployments, and live reload follows the primary |
| `dev — saves reach apps/crm+dev; main pinned to c:3f2a1c9` | saves reach only `dev`; the primary runs `c:3f2a1c9` |
| `paused by ana 12m ago — 3 files changed since c:3f2a1c9 (main)` | saves reach nobody; `main` runs `c:3f2a1c9` |
| `… — the last deploy to main failed; main keeps running c:3f2a1c9` | a deploy failed, and nothing anyone uses changed |

```
$ bx deployment ls apps/crm
apps/crm · primary main · live reload → dev
  NAME  ROLE     CODE               STATUS       DATA
  main  primary  pinned c:3f2a1c9   healthy g12  original
  dev   -        follows work tree  healthy g3   started empty  ← this terminal
```

Saves reach the **live reload target**; your API calls and `bx status`/`bx
logs` reach **your target**. They can differ: check both before deciding a
fix "didn't work". `bx status` prints a `deployments` line for a tile that
has any, ending `this terminal → dev`.

In the browser the terminal window says the same: in the zero state its bar
has one quiet entry, `⇈`, whose menu offers **Pause live reload**. While live
reload is paused the entry becomes a chip, `📌 Live reload paused · 3`,
counting the files changed since the checkpoint, next to **⇡ Reload now**;
the chip's menu has Reload now and **Resume live reload on ▸**. The terminal
window's launcher shows a banner while live reload is paused, the tile's
window head in the shell carries `⇈` (below; nothing is drawn over the page
itself), and every open terminal of the tile prints a grey line when live
reload pauses, resumes or moves, or code moves or fails to. The window's `⇈`
layout is the **Deployments panel** (below).

Programs can ask `GET /api/xbin/deployments?tile=<tile>` (`record: false` is
the zero state; `&deployment=<name>` selects one) or read `deployments:
{primary, pinned, protected}` on the tile's `/api/xbin/components` entry,
present only for a tile that has left the zero state. A reader of the tile gets the primary's facts only: no other
deployment's name, and no count that reveals one
([protocol.md](/docs/protocol.md) §Tile deployments).

## Pausing live reload

```sh
bx live-reload pause      # or: ⇈ → Pause live reload, in the terminal window
```

- **What it does.** The live reload target is pinned to a fresh checkpoint of
  the work tree, taken when the request commits. Normally that is exactly the
  code already running, so nothing visible happens. If the work tree moved
  since the last build (a save still building, a failed build), pausing
  ships it once, now; the dry run says so before you confirm.
- **Saves while paused** change the files and nothing else: no frame reloads,
  no backend rebuilds. The terminal window counts the files that differ from
  the checkpoint.
- **Who may.** Anyone with terminal access to the tile, and the tile's own
  terminal and agent sessions — the same people whose saves go live today.
  A tile's frontend and backend can't.
- **The first pause is the opt-in** (so is adding a deployment, or protecting
  the primary). It creates the tile's deployment record (`data/deployments/`)
  and its checkpoint store (`data/checkpoints/`), both xbind-owned and
  invisible to terminals. Before that, nothing is written for the tile —
  reading its state, dry runs and diffs included.
- **Backends need isolation.** Pinning a `go`, `node` or `python` backend to a
  checkpoint needs an xbind started with `--isolate`: without it the pause
  is refused with `pinning a backend to a checkpoint needs isolation
  (--isolate)`, and the tile keeps live reload. Static tiles pause
  everywhere.
- It is idempotent: pausing live reload again changes nothing.

## What a pinned deployment runs

Everything a pinned deployment serves and runs comes from its checkpoint:

- **Its pages.** `/c/<tile>/…` (or its deployment URL) serves the
  checkpoint's files, in every asset mode, still `Cache-Control: no-store`;
  saves don't show. A symlink in the checkpoint that points outside it
  answers 404; `deps/<name>/…` is served as the dependency tile's own `/c/`
  path (its primary, under its own access rules). `?native=1` is generated
  from the checkpoint too.
- **Its backend.** The backend runs the checkpoint, which its sandbox sees
  read-only at the tile's own path; `setup` runs against it. The build is
  kept per checkpoint, so every restart — a crash, the idle reaper, a grant
  or binding change, alwaysOn, an unseal, an xbind restart — starts the same
  code without compiling. A pinned backend never falls back to the work tree:
  an xbind restarted without `--isolate` holds it, and its `/api/` answers
  `pinning a backend to a checkpoint needs isolation (--isolate)` while its
  pages keep serving the checkpoint.
- **Its manifest, in three parts.** How a deployment runs comes from its own
  code — its checkpoint, or the work tree for the live reload target:
  `runtime`, `entry`, `setup`, `alwaysOn`, `vm`, `inject`, `native`, and
  `scope.json`'s resources and import map. What the tile offers others comes
  from the **primary's** code, since only the primary receives anything from
  outside: `exposes`, `expose` (its roles), `provides`, `template` and
  `chrome`. What the tile asks for stays the work tree's: `uses`,
  `interfaces` and `deps` — a new `uses` entry files a pending grant as it
  does today, and a pinned backend's `XBIN_RES_*` covers the work tree's and
  its checkpoint's `uses`, still filtered by grants. `/api/xbin/components`
  follows the same split: `runtime`, `hasIndex`, `native`, `chrome` and
  `template` describe the primary's code; `roles`, `uses`, `deps` and
  `manifestError` the work tree.
- **A broken work tree doesn't take the tile down.** While the primary is
  pinned, the tile stays registered as long as its directory exists, even
  with no valid `xbin.json` or `index.html`; `bx ls`, `bx doctor` and
  `manifestError` then say `the work tree has no valid xbin.json; serving
  the pinned primary`. A checkpoint whose `scope.json` doesn't parse, or
  names a resource the naming rule refuses, can't be deployed.

## Reload now

```sh
bx live-reload now        # or: ⇡ Reload now, in the terminal window
```

Checkpoint the work tree and deploy it once to the deployment live reload
last followed, which stays pinned: later saves wait for the next Reload now.
When the work tree equals the checkpoint that deployment runs, nothing moves
(`unchanged: true`). The confirmation names the checkpoint it will ship, and
the request carries it (`expect`): if the work tree changed between the two,
the answer is 409 `the code changed since you reviewed …` and nothing ships.

## Resuming, and moving live reload

```sh
bx live-reload resume                # onto the deployment it last followed
bx live-reload resume --to dev       # or: Resume live reload on ▸ dev
bx live-reload attach --to dev       # while live reload is on: move it to dev
```

- **Resume** makes a deployment follow the work tree again: the work tree is
  built, health-checked and swapped in, and every later save reaches it.
- **Attach** moves live reload from one deployment to another while it is
  on: the one it leaves is pinned to a fresh checkpoint of the work tree
  (usually exactly what it ran), and the other follows every save. While live
  reload is paused, `attach` points you at `resume`.
- **Back to the zero state.** Resuming onto `main` while `main` is the only
  deployment and every setting is at its default removes the deployment
  record, so the tile is back to plain live reload (`bx` says `apps/crm is
  back to plain live reload, with no deployments.`). The checkpoint store and
  its deploy log stay on disk, unread, until the tile opts in again; deploy
  ids keep counting from where they were.
- A protected primary is never a live reload target (below).

Resuming ships the work tree to everyone that deployment serves. Pause and
resume are cheap, so leave a pause someone else made alone unless you were
asked; `bx live-reload` says who paused and when.

## Named deployments

### Adding one

```sh
bx deployment add dev --attach            # dev: the work tree's code, empty data; live reload follows it
bx deployment add dev --from primary      # dev: what the primary runs now
bx deployment add dev --from c:1e9d0aa    # dev: a checkpoint from the deploy log
bx deployment add apps/crm+dev            # the same as the first, naming the tile
bx deployment add dev --attach --new-branch feature   # dev requires a new branch, feature (below)
```

- **What a new deployment gets.** Its code (default: a fresh checkpoint of
  the work tree), **empty data**, a vault holding only the primary's secret
  **names** (placeholders, no values), deliveries on (its cron jobs and bus
  subscriptions fire for it), alwaysOn off, and the tile's resource limits.
  Its backend is built in the background and starts on its first request. With `--attach` live reload follows the new
  deployment, and the deployment it leaves is pinned. `--seed` also copies
  the primary's data into it (a tile manager's act, below).
- **Who may.** Anyone with terminal access to the tile, and the tile's own
  terminal and agent sessions.
- **Names** are lowercase letters, digits and `-`, start with a letter, and
  have at most 24 characters. They are permanent: a name keys the
  deployment's storage.
- **Refusals.** A tile has at most 3 non-primary deployments and a workspace
  24 (`apps/crm has 3 non-primary deployments, the most allowed here`), and
  at most 12 non-primary backends run at once workspace-wide (a start past
  that is refused, and shows in the deployment's status). A backend tile
  needs `--isolate`. Workspace chrome (root, shell, a tile whose code asks for
  `chrome`) and tiles holding an `xbin` or `xbin:*` grant can pause live
  reload but can't have other deployments (`apps/admin can't have
  non-primary deployments: …`): they act on the whole workspace. A name
  already taken, a tile existing at `<tile>+<name>`, and a tile whose own name
  holds `+` (one created before `+` was refused in tile names) are refused
  too.

### Its URL

`/c/<tile>+<name>/` is the deployment's frontend and `/api/<tile>+<name>/…`
its backend; the bare `/c/<tile>/` and `/api/<tile>/` always mean the
primary.

- **Who may open it:** people with write access to the tile, checked on
  every request, and the tile's own frames, backend, terminals and agents
  bound to that deployment. Readers get 403 (`deployment URLs need write
  access on apps/crm`); other tiles never reach a non-primary deployment —
  they call the bare URL, the primary.
- **How it resolves.** The `+` is read only for a tile that has deployments,
  and only when nothing answers the path today: a tile or anything on disk at
  `<tile>+<name>` wins, so a directory whose name holds `+` keeps working. An
  unknown name answers 404 `apps/crm has no deployment "<name>"`. A qualified
  URL never reaches into a nested tile (`<path> is a tile of its own; its
  deployments are /c/<path>+<name>/`).
- **Only in paths and JSON bodies.** A query string never carries
  `<tile>+<name>`: a `+` there decodes to a space. A query names the tile
  and the deployment apart — `?tile=apps/crm&deployment=dev` — and one that
  sends `?tile=apps/crm+dev` (or `component=` so) is answered 400 `a
  deployment is named with deployment=, not tile+name (a '+' in a query
  string reads as a space)`. `bx` splits a ref you give it for you.
- **Its documents** carry `<meta name="xbin-deployment" content="dev">`, and
  `xbin.deployment` is `"dev"` in them (absent at the primary); `xbin.self`
  stays the tile path. Their import map sends absolute `/c/<tile>/…` module
  imports to `/c/<tile>+dev/…`, so self-imports stay in the deployment.
  **Relative URLs are the portable form:** an absolute `/c/<tile>/…` in an
  HTML attribute or CSS `url()` loads the primary's file in the legacy asset
  mode. A deployment's document is always sandboxed, never chrome, whatever
  its `xbin.json` says. `?native=1` at a deployment URL runs that
  deployment's native entry.
- **Its calls.** A document's `xbin.fetch('/api/' + xbin.self + '/…')` and a
  backend's self-calls reach their own deployment: a tile's frame token and
  backend token each name the deployment they were minted for, and a tile's
  credential can't call another deployment of its own tile. In the origins
  asset mode every deployment has its own origin, so its `localStorage` and
  cookie-authenticated `fetch` are its own.
- **In another page,** `<bx-frame src="apps/crm+dev">` shows the deployment
  (for people who may open it).

### Your terminal's target

A terminal or agent session calls one deployment, fixed for its life: its
**target**. It is chosen in the terminal window's tile API select, which
lists `🔌 target: main (primary)`, `🔌 target: dev`, …, `⛔ no API` once the
tile has more than an unprotected `main` (a tile without deployments keeps
today's two entries).

- **The default** is the primary. A protected primary is never offered: the
  default then falls to the live reload target; when live reload is paused
  too, the tile API is off.
- **Switching restarts the session**: a shell asks `Restart this terminal
  calling dev?`; an agent resumes its conversation. The select shows what the
  server confirmed, never a guess; a terminal whose chosen target wasn't
  confirmed ends with a red line.
- **Inside the session**, `XBIN_DEPLOYMENT=dev` is set when the target isn't
  the primary (unset means the primary), the terminal prints a grey line
  (`this terminal calls apps/crm+dev`, or why the tile API is off), and
  `curl $XBIN_URL/api/$XBIN_COMPONENT/…`, `bx status` and `bx logs` reach
  the target. An agent can't switch its own target; `bx agent run
  --deployment dev` (or `--tile apps/crm+dev`) opens one on `dev`.
- **Reads work without switching:** `bx status apps/crm+dev`, `bx logs
  apps/crm+dev`, `bx deployment log apps/crm dev`. A command that changes
  something never takes its deployment from `XBIN_DEPLOYMENT`.

### Removing one

```sh
bx deployment rm dev
```

Stops `dev` and deletes its vault, logs, registrations and builds,
and its data — unless another tile of the same scope still has a `dev`
(below). `main` and the primary can't be removed. If live reload followed
`dev`, it is paused and its default target becomes the primary. The
checkpoints stay until retention collects them.

## Deploying, restarting and rolling back

```sh
bx deploy --to dev                          # a fresh checkpoint of the work tree
bx deploy --to main --checkpoint c:1e9d0aa  # a checkpoint from the deploy log
bx rollback --to main                       # the newest successful deploy with other code
bx rollback apps/crm+main --checkpoint c:1e9d0aa
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
  {"tile": "<tile>", "deployment": "<name>", "restart": true}` starts a new
  generation of the current code, even while it is healthy, and clears the
  crash breaker; terminal level on every deployment, a protected primary
  included. Deploying the checkpoint a deployment already runs changes
  nothing (`unchanged: true`) while its backend is healthy. When the backend
  is crash-looping, failed or not running, or its last deploy failed, the
  same deploy starts it again from the kept build (logged as `restart`) and
  clears the crash breaker.
- **Deploys are asynchronous.** One runs per deployment and up to eight wait
  behind it; a request for the checkpoint already last in line joins that
  entry; a ninth answers 409 `main already has 8 deploys waiting; try again
  when one finishes`. `bx` waits for the result (up to 20 minutes;
  `--no-wait` returns once accepted) and prints each phase: checkpoint,
  materialize, build, start, swap.
- **A failed deploy changes nothing anyone uses.** The previous generation
  keeps serving, no build-error overlay is painted, and the compiler output
  goes to that deployment's backend log (`bx logs`) under `--- deploy of
  c:<id> failed <time> ---` and into the deploy log's `error`. If the failed
  attempt was moving a deployment off the work tree (a pause, an attach, or
  a deploy onto the live reload target), it stays pinned to the attempted
  checkpoint: every restart runs it, and Reload now retries it.
- A pinned backend that crash-loops says `deploy a fixed checkpoint or
  restart it`: a save doesn't reach it, while a deploy of the checkpoint it
  runs, or a restart, brings it back.
- Frames reload once after a swap that changed the code a deployment serves
  (reload now, deploy, promote, roll back, resume, attach). Pausing and
  restarts don't reload anything.
- If xbind stops mid-deploy, the next boot writes the attempt into the deploy
  log: a queued one as cancelled, one that had swapped as ok, one still
  running as failed with `interrupted: xbind restarted mid-deploy; main runs
  <code>`. Every deployment keeps the code its record names.

## Promoting

```sh
bx deployment diff apps/crm main dev --stat   # what promoting dev would change on main
bx promote dev main                           # main gets exactly dev's code
```

- **Code only.** `main` receives `dev`'s checkpoint — or, while `dev` follows
  the work tree, a fresh checkpoint of it — and keeps its own data, vault,
  registrations and routing. Everything bound late (resource paths, interface
  URLs, secrets) is resolved for `main` when it starts. Promotion is not a
  merge: combining work is a git activity in the work tree, and its result is
  what you promote.
- **The diff first.** The panel's Promote shows the diff between what the
  target runs and what it will get; `bx promote` prints the same report.
  When `dev` follows the work tree, the report names the checkpoint it
  captured, and the promote ships exactly that one (a 409 if the work tree
  moved since).
- A promote onto the live reload target pauses live reload, like any deploy.
- **Who may:** as a deploy to the target — terminal level, and onto a
  protected primary only tile managers.

### Asking before shipping

Every changing `bx` command first sends a dry run and prints what will
happen — `Code`, `Data`, `Pauses`, `Affects` — then acts. A command that
moves code onto the primary (`deploy`, `promote` and `rollback` to it, and
`live-reload now`, `resume` or `attach` when they change what it runs) and a
command that touches data or routing (`deployment rm`, `primary`, `protect`,
`seed`, `reset`, `vault-copy`, `add --seed`) asks its question (`Promote dev
→ main? [y/N]`) on a terminal; without one it stops with exit code 4 unless
you pass `--yes`. `--dry-run` prints the report and changes nothing. Exit
codes: 0 done, 1 failed, 2 usage, 3 refused (not yours to do — the message
says who can), 4 not confirmed, 5 still running when bx stopped waiting, 6
this xbind has no tile deployments ([bx.md](/docs/bx.md) §Tile
deployments).

## The deploy log and the diff

```sh
bx deployment log                      # this terminal's target, or every deployment you may see
bx deployment log apps/crm main
bx deployment diff                     # the primary → the work tree
bx deployment diff apps/crm main dev --stat  # what main runs → what dev runs
T=apps/crm
curl -s -H "Authorization: Bearer $XBIN_TOKEN" "$XBIN_URL/api/xbin/deployments/log?tile=$T"
curl -s -H "Authorization: Bearer $XBIN_TOKEN" "$XBIN_URL/api/xbin/deployments/diff?tile=$T&stat=1"
```

- The log lists every attempt newest first — queued and running ones, and
  failed ones, included — with who, when, how (`pause`, `reload-now`, `resume`, `attach`, `add`,
  `deploy`, `promote` with its `from`, `rollback`, `restart`, `reassign`,
  `protect`), from which session, and the checkpoint before and after.
  `?id=<id>&wait=<s>` waits (up to 25 s) for one attempt to finish.
- The diff compares two of `c:<id>`, a deployment and `work-tree`
  (default: what the primary runs → the work tree); `path=<file>` narrows it
  to one file. It runs confined and bounded: one diff per tile at a time
  with one waiting (else 429), 30 s at most (else 504), patches cut at
  16 MiB. A work-tree side takes a checkpoint and needs terminal access. The
  answer names both checkpoints in `X-XBin-Checkpoint-From` and `-To`.
- Both need write access to the tile. A tile in the zero state has nothing
  to diff: 409, and nothing is captured.

## Assigned branches

A non-primary deployment can require a branch of the tile's repository: `dev`
runs `feature`, `qa` runs `release`. It is a requirement and a label, not a
feed — `dev` still follows the work tree or runs a checkpoint, but the work
tree feeds it only while it has `feature` checked out.

```sh
bx deployment add dev --attach --branch feature      # dev requires feature, which exists
bx deployment add dev --attach --new-branch feature  # xbind creates feature at HEAD and checks it out
bx deployment branch dev feature                     # later; --clear takes it off
bx live-reload                                       # each deployment's branch, and the work tree's
```

- **What it guards.** Attach and resume onto `dev`, Reload now while live
  reload last followed it, a deploy of the work tree to it, and adding it
  from the work tree answer `dev is assigned branch feature, and the work
  tree is on main: check out feature, or send confirm:"other-branch" to use
  main this time` (409). `--other-branch` (the confirmation's "this time")
  takes the work tree's branch once; after attach or resume it holds until
  live reload moves or the work tree's branch changes again. A deploy of a
  checkpoint, a promote and a roll back aren't fed by the work tree, and
  aren't asked. A detached HEAD, or a tile without a repository, is on no
  branch.
- **Saves too.** While live reload follows `dev`, xbind reads the work
  tree's branch on every batch of saves and checkpoints the work tree before
  the save reaches `dev`, reading the branch again as it does. A save on
  another branch — a `git checkout`, or a checkout racing the save — reaches
  nothing: live reload pauses, and `dev` stays on the code it ran (its deploy
  log says `pause` by `xbind`), and clients get a `deployments` event (op
  `branch`) naming the deployment assigned the branch you switched to, if
  one is. Pausing, or attaching live reload elsewhere, while the work tree
  is on another branch keeps `dev` on the code it ran too. Deployments
  without a branch — `main`, the primary — follow the work tree on any
  branch, as before, and a tile none of whose deployments has a branch
  saves exactly as it always did.
- **Following a switch.** After `git checkout release` with `qa` assigned
  `release`, resume live reload on `qa`; after `git checkout feature` again,
  resume it on `dev`. With no deployment for the branch, resume `dev` with
  `--other-branch` (this time), or add one with `--branch release`.
- **Creating a branch.** `--new-branch` runs a confined `git switch
  --create=<name>` in the tile: a new branch at HEAD, checked out, no file
  changed, so nothing reloads. It never switches to an existing branch (409
  `apps/crm already has a branch feature …`); xbind checks out nothing else.
- `main` and the primary never have a branch: making a deployment the
  primary clears its branch. Checkpoints taken from the work tree name the
  branch in an `Xbin-Work-Tree-Branch` trailer, and each deploy log entry
  names the branch its capture was taken on.
- An older xbind keeps the branch in the record untouched and ignores it.
- **In the terminal window.** The Add deployment form has a **Branch**
  control — none, the work tree's (`current (feature)`), or a new branch
  with its name. A deployment's overview has a **Branch** row (with
  `takes main this time` while an override holds) and **Set branch…** /
  **Clear branch**; the side list and the deploy log show `⎇ feature`. After
  a switch the live reload chip reads `📌 Live reload paused · ⎇ main`, and
  its menu and the panel's header lead with the offers: **Resume live
  reload on qa (release)** (**Attach live reload to …** while live reload
  is still attached), **Resume live reload on dev** once you are back on its
  branch, and with no deployment for the branch **Keep dev on main this
  time** and **Add a deployment for main…** (the form preset: that branch,
  live reload attached). The tile's open terminals print a grey line saying
  the same. An operation refused because the work tree is on another branch
  asks whether to use it this time instead of showing a bare refusal.

## Branching from what a deployment runs: `git fetch xbin-deploy`

To fix what the primary runs while the work tree holds unfinished work — here
`dev` requires `feature`, which holds it:

```sh
git commit -am wip                           # the unfinished work stays on feature
git fetch xbin-deploy
git checkout --no-track -b hotfix deploy/main
bx deployment add hotfix --attach --branch hotfix   # optional: try the fix at /c/<tile>+hotfix/
# fix, commit, then — once the user says ship:
bx deploy --to main                          # or: bx promote hotfix main
git checkout feature
git rebase --onto hotfix <the Xbin-Work-Tree-Head commit> feature
bx live-reload resume --to dev
bx deployment rm hotfix
```

The checkout of `hotfix` leaves `feature`, so live reload pauses and `dev`
keeps running the unfinished work; the fix never reaches it. Checking out
`feature` again and resuming brings `dev` the rebased work. Nothing in xbind
merges anything: git combines work in the work tree, and deployments move
checkpoints.

- Terminal and agent sessions opened while the tile has a deployment record
  get a fetch-only git remote, `xbin-deploy`, set only in the session's
  `GIT_CONFIG_*` variables — nothing is written to the tile's `.git/config`.
  `git fetch xbin-deploy` brings `deploy/<name>` for every pinned deployment:
  the checkpoint it runs, minus the files your own ignore rules exclude, so a
  branch made from it never tracks `node_modules` or `.env`.
- `deploy/<name>` exists only while `<name>` is pinned: a deployment that
  follows the work tree has none.
- Its commits are parentless — they share no history with your own — and
  carry `Xbin-Checkpoint` and `Xbin-Work-Tree-Head` trailers, the latter
  naming the commit the checkpoint was taken on: once the fix is in,
  `git rebase --onto hotfix <that commit> <your branch>` moves your
  unfinished work onto it.
- `--no-track` keeps git from recording `xbin-deploy` as the branch's
  upstream, a remote that exists only in some sessions.
- A session opened before the record existed (the usual case: you paused
  from a window with a shell already open) has no `xbin-deploy`; open a new
  terminal, or fetch by URL, which any session's git can:
  `git fetch http://xbin/api/xbin/checkpoints/<tile>.git '+refs/heads/deploy/*:refs/deploy/*'`.
  A fetch that races a refresh can fail once; fetch again.
- Who can fetch: the tile's terminal and agent sessions while their user has
  write access, and people with write access.

Without an assigned branch, a `dev` that follows the work tree runs the
primary's code plus the fix as soon as you check out `hotfix` — the fix is
tried there first; deploy it to `main` once it works, and check out your
branch again.

## Data and secrets per deployment

- **`main` keeps the tile's data, forever**: its keys, files and paths never
  move, whichever deployment is the primary. Adding deployments migrates
  nothing.
- **Every other deployment starts empty.** Its kv, blob, bus, sqlite and
  filesystem resources live in a namespace of its own, and its code sees them
  exactly where `main`'s code would: resource ids and `XBIN_RES_*` paths are
  the same in every deployment. Most non-primary deployments run on data
  their own code creates.
- **Each deployment provisions what its own code declares.** A resource that
  `dev`'s `scope.json` adds exists only in `dev` until code declaring it is
  promoted; a namespace beyond `main` holds at most 64 resources. A resource
  the code stops declaring is kept.
- **Data belongs to scopes.** In a scope shared by several tiles, same-named
  deployments share one namespace: `apps/shop+dev` and `apps/shop-admin+dev`
  see the scope's `dev` data, as their `main`s share the scope's data today.
  A sibling without a `dev` uses the primary's. Adding a deployment to a
  namespace a sibling already seeded or restored is a tile manager's act.
  Other scopes' data and `res:workspace/*` are read-only to non-primary
  deployments.
- **Seed** (`bx deployment seed dev`, or Seed from main… in the panel)
  replaces `dev`'s data with a copy of the primary's: every resource both
  deployments' code declares with the same type (resources only `dev`
  declares start empty; the rest are skipped and listed). It may carry
  personal data, so it is a tile manager's act, in their own session — a
  manager of every tile of the scope that has that deployment.
  It runs online by default
  (kv consistent at one moment, each SQLite database at one point in time,
  files one by one); `--stop` stops the primary for a point-in-time copy of
  everything, and is required for a single-tenant volume or a VM primary with
  file resources. The vault, cron jobs, subscriptions and registrations are
  never copied.
- **Reset** (`bx deployment reset dev [--vault]`) empties `dev`'s data;
  `--vault` also empties its vault. Terminal level, on every tile of the
  scope that has a `dev`; resetting `main`'s data while it isn't the primary
  is a tile manager's act, and data some tile serves as its primary is never
  reset.
- **While a seed, reset or restore runs**, the data's kv, blob and bus
  requests answer 503 with `Retry-After`, the deployments using it don't
  start, and another data act answers 409 `dev's data is being seeded`. One
  cut short (a crash, a failure) leaves the data **partial**: the deployment
  refuses to start (`seed of <scope> for dev failed at <step>: reset or seed
  again`) until a reset or a new seed.
- The panel and `bx deployment ls` show each deployment's data: `original`
  (main), `started empty`, `seeded from main 2026-09-27`, `restored`, or
  `seeding…`/`resetting…` while it runs.
- **Removing** a deployment deletes its data only with the scope's last tile
  that has that deployment. Data no tile claims any more (a `scope.json`
  that vanished, a removed record) is kept 14 days, listed to admins, then
  deleted. `main`'s data is never collected.
- **The vault is per deployment.** A new deployment's vault holds only the
  primary's key names, as placeholders: reading one answers 404 `vault key
  "<k>" has no value in deployment dev; a tile manager can copy it from the
  primary`. **Copy vault values** (`bx deployment vault-copy dev --keys
  STRIPE_KEY`, or `--all`) copies values from the primary, a tile manager's
  act; the audit names keys, never values. A backend reads its own
  deployment's values only; a terminal or agent session's vault calls reach
  its target's vault. While the primary is protected, its vault is written
  only by its own backend and by tile managers in their own session.
- **Disk.** Each deployment's data beyond `main` is its own quota bucket, at
  the scope's quota or a lower per-deployment limit, and is write-blocked
  first when the disk runs low.

## What a non-primary deployment can reach

A deployment has no authority of its own: it acts with its tile's grants,
bindings and capabilities. The primary uses them exactly as today. For the
tile's **non-primary** deployments each edge — `grant:<target>` or
`slot:<name>` — has a policy, set by tile managers (`bx deployment edge`
lists them; `bx deployment edge grant:apps/calendar block` sets one; the
panel's tile-wide **Non-primary access** table does both):

| Policy | Default on | What a non-primary deployment gets |
|---|---|---|
| `read` | call grants, http interface bindings, other scopes' resources and buses | the provider's **primary**, with the role clamped to `reader`: it reads other tiles and never writes to them |
| `inherit` | the net slot, capability grants other than `gpu:*` | the tile's own authority — for the net slot its relay policy, never host networking or a provider splice |
| `block` | `gpu:*`; takes any edge | nothing: the call fails, naming the deployment, the edge, its target and the policy |

- **Edges that can't be limited to reading are blocked, with no override:**
  a custom role whose `implies` doesn't reach `reader`, stream and
  lan-ingress interface slots, a net bound to a provider tile, and a
  workspace-level filesystem or sqlite resource on a workspace-scope tile. A
  provider makes a custom role clampable by declaring
  `implies: {"<role>": ["reader"]}`.
- **What that costs, plainly.** llm-gw runs completions for `writer`, so an
  LLM-using tile's non-primary deployments can list models but can't run a
  turn. The agent tile reaches its sandbox managers through a custom role,
  so its non-primary deployments can't reach them: test sandbox work from
  the primary. A sandbox manager tile's own sandboxes are `main`'s: its other
  deployments get 501 from xbind's tile-sandbox routes
  ([protocol.md](/docs/protocol.md) §Tile sandboxes).
- A tile whose `net` shares the host's network gives its non-primary
  deployments **no egress**, and the panel says why. A non-primary
  deployment never gets host networking, a provider splice, a stream
  interface slot's dial or ingress through a terminator; its backend log
  says why it has no egress, and, when it has relay egress, logs each
  refused stream dial (without egress the dial finds no route).
- An unknown or invalid value reads as `block`, and when several edges
  authorize one call, any `block` among them refuses it.
- Callees see `X-XBin-Deployment: <name>` on calls from a non-primary
  deployment (`X-XBin-From` stays the tile path), so a provider that cares
  can tell; the primary's calls carry nothing new. Providers must treat
  `reader` as read-only.
- **xbind's own API** refuses a non-primary deployment's credentials on the
  routes that belong to the primary (creating tiles, grants and bindings,
  lifecycle, ownership, users, admin routes, …): 403 `this route is the
  primary's alone: a non-primary deployment's credentials can't use it
  (dev)`. Approving an `xbin` or `xbin:*` grant for a tile with non-primary
  deployments answers 409.
- The panel counts, per edge, the calls refused and clamped.

A non-primary deployment is an accident boundary: it keeps untested code away
from the primary's data and traffic when everyone involved already writes the
tile's code. It is not a trust boundary.

## Registrations, deliveries and background work

- **Cron jobs and bus subscriptions are the deployment's own.** A cron job
  or bus push subscription that a non-primary deployment registers is stored
  under that deployment and fires for it — its backend, its data — never for
  the primary. Registrations never collide with `main`'s, which stay where
  they were. A push subscription on the tile's own bus receives only events
  published in its deployment's data; one on another scope's bus receives
  that scope's primary's, like a read binding: the edge policy's `read`
  allows it and `block` refuses it, checked at registration and again at
  every delivery. Publishing is unchanged: a deployment publishes into its
  own data only. What a job or delivery does is real: it runs with the
  deployment's data and the tile's network, so anything it sends (email,
  webhooks) is sent.
- **Interface instances and ingress hosts are dormant.** A non-primary
  deployment's are stored under it and answered as usual (with `dormant:
  true`), so start-up code keeps working — but nothing routes to them: they
  belong to the primary.
- **Deliveries** is a tile manager's off switch per deployment, on by
  default: `bx deployment set dev --deliveries off` silences a noisy `dev`'s
  cron jobs and bus subscriptions (they stay registered and are listed and
  answered `dormant: true`); `--deliveries on` restores them. The primary's
  always deliver.
- **Run now** (`bx deployment run-now dev nightly`, or the panel's
  registrations tab) delivers one of `dev`'s cron jobs once, as `xbin/cron`
  with the job's role, whatever its deliveries switch says: terminal level,
  one run of a job at a time, and the answer carries the handler's status.
- **alwaysOn** for a non-primary deployment needs both its own code saying
  `"alwaysOn": true` and a tile manager's switch (`--always-on on`);
  otherwise it starts on demand and is reaped when idle.
- **Notifications are never pushed** from a non-primary deployment: the call
  succeeds, and the panel lists the last 20 as "would notify …".
- **Status is per deployment.** A non-primary deployment's `xbin.status`,
  notices and builds show in the panel and `bx deployment ls` only — never in
  the shell's sidebar, toasts or title, which speak of the primary.
- **Logs** are per deployment: `bx logs apps/crm+dev`, the panel's logs tab,
  `GET /api/xbin/logs?component=<tile>&deployment=<name>`.
- **Prefs** are per deployment beyond `main`, and so is the `prefs` event a
  write sends: only that deployment's own frames, sessions and backend hear
  it, never the user's browser ([protocol.md](/docs/protocol.md)).

## The primary: reassigning and protecting it

**Reassigning** (`bx deployment primary --to dev`, or Reassign the primary…)
sends everything from outside to `dev`: the bare URLs, other tiles' calls,
bindings, interface instances, ingress. **Data doesn't move** — `dev`
serves its own data, and `main`'s stays behind; the confirmation says so.

- A tile manager's act, in their own session or from the admin console
  (§Managing protection), onto a healthy deployment. In this release only a
  tile alone in its scope, or in the workspace scope, can change its
  primary (a multi-tile scope's primary data would split).
- `dev` starts as the primary first; then the old primary restarts, its
  long-lived connections cut. Its interface instances and ingress hosts go
  dormant and `dev`'s activate; an ingress host of `dev`'s that conflicts
  stays inactive and is listed. Each deployment's cron jobs and bus
  subscriptions keep firing for it (unless its deliveries are off). Sessions targeting either deployment restart.
- `main` remains, pinned, and can become the primary again.

**Protecting** (`bx deployment protect on`, or Protect the primary) makes
every change to the primary's code a tile manager's act:

- Deploy, promote, roll back and Reload now onto the primary need a tile
  manager in their own session — the Deployments panel, or `bx` with the
  root token on the host — naming the checkpoint they reviewed (the panel
  and `bx` send it from the dry run). Terminal and agent sessions
  get 403 `the primary of apps/crm (main) is protected: only tile managers
  change its code, and not from a terminal or agent session`. A restart stays
  terminal level.
- The primary is never the live reload target (protecting pins it in place)
  and never a session's target: sessions that targeted it restart onto the
  default (the live reload target, or with the tile API off).
- What it offers others (`exposes`, `provides`, `template`, `chrome`) comes
  from its checkpoint, and its builds come only from managers' operations.
- The answer lists the components nested in the tile and warns about each
  unprotected one: their code is the tile's writers' to change.
- Unprotecting moves nothing: the primary stays pinned until someone deploys
  or resumes onto it.

Without protection, terminal-level users and their agents deploy to the
primary as they save to it today.

### Managing protection

Tile managers — the tile's owner, its org's admins, a workspace admin —
manage protection in three places, all of them a person's credential:

- **The Deployments panel**, in their own browser session.
- **`bx` on the host with the root token** (`bx deployment protect apps/crm
  on`, `bx deployment primary apps/crm --to dev`, `bx deployment set
  apps/crm dev --deliveries on`): the owner token is the owner's, so it
  passes for every tile. Never from a tile terminal: there bx is refused.
- **The admin console's runtime → deployments tab** (`tiles/admin`; in an
  existing workspace after `bx builtin update scaffold:tiles/admin`): every
  tile with a deployment record — its primary, 🛡 when protected, where
  live reload is, its deployments, the last deploy — with Protect /
  Unprotect the primary, Reassign the primary… (the same loud confirmation
  as the panel's), and
  deliveries and alwaysOn per non-primary deployment. ⇈ Deployments panel
  opens the tile's terminal window for everything else. The tab acts as the
  person who opened the admin tile: a tile they don't manage shows the
  primary only, its buttons disabled with the reason
  ([auth.md](/docs/auth.md) §Tile deployments).

## Limits

- **Per deployment** (a tile manager: `bx deployment set dev --mem 512
  --pids 256 --disk 5`, or Set limits… in the panel): memory, processes and
  the data's disk quota default to the tile's and can only be lowered;
  `default` removes an override. Memory and processes apply from the next
  generation; the disk quota is set on the scope's root tile.
- **The primary comes first.** Inside the tile the primary's backend has the
  higher CPU weight, and deployments never enlarge the tile's share of the
  machine. A non-primary deployment's VM is admitted only while the
  primary's next VM still fits the workspace's budget, and is stopped to
  admit the primary's. At most max(1, CPUs/4) builds for non-primary
  deployments run at once; a primary's build never waits.
- **Isolation.** Pinned and non-primary backends need `--isolate`
  ([isolation.md](/docs/isolation.md)).
- **Blocked edges.** In this release a non-primary deployment can't use the
  edges that can't be limited to reading — a custom role with no path to
  `reader`, stream and lan-ingress interface slots, a net bound to a
  provider tile, a workspace-level filesystem or sqlite resource on a
  workspace-scope tile — and gets no egress where the tile's net shares the
  host's. So llm-gw's completions and the agent tile's sandbox managers are
  unavailable to it, whatever a tile manager sets (§What a non-primary
  deployment can reach).
- **Counts.** At most 3 non-primary deployments per tile, 24 per workspace,
  and 12 non-primary backends running at once.

## The Deployments panel

The terminal window's `⇈` layout (the button counts the deployments you may
see, `⇈ 2`). It opens full width; the window's `⇋` puts the terminal
beside it, split by a divider you drag, and a narrow panel shows its side
list and the selected row's page one at a time. A header with the live reload sentence and its buttons (and
**Undo** after a code move that paused live reload); a side list —
**tile-wide**, one row per deployment (the primary first, `🛡` when
protected; **`Dev API`** on the deployment the active tab's API calls and
`bx` commands reach — its target, `$XBIN_DEPLOYMENT` when non-primary —
and **`● live reload`** on the one saves reach), **+ Add deployment…** —
and a pane for the selected row:

- **overview**: code, status, the branch it requires (§Assigned branches),
  data, resources, limits, vault, deliveries, alwaysOn, the URL, the `git
  fetch xbin-deploy` line of a pinned deployment, and the actions: Deploy
  to, Promote, Set branch… / Clear branch, Remove, Seed, Reset, Copy vault
  values, Set limits;
- **deploy log**, with Roll back on its entries and the branch each was
  captured on;
- **logs**; **registrations** (cron jobs and bus subscriptions active, or
  dormant while deliveries are off; interface instances and ingress hosts
  dormant off the primary; Run now; "would notify");
- **view**: a non-primary deployment's frontend, embedded.

The tile-wide page holds the primary (Reassign the primary…, Protect) and the
Non-primary access table, with each edge's refused count. A control the
viewer may not use is disabled with the reason, and every change confirms
from a dry run of the exact request. Readers see the primary only.

**In the shell** (in an existing workspace after `bx builtin update
scaffold:shell`, and `scaffold:tiles/admin` for the admin console): a tile
with deployments gets a `⇈ Deployments…` line in its tile menu (after Open
full page; not for its readers). Its window's head (a grid card or a floating window)
carries `⇈` while the tile has a deployment you may show besides the
primary, the primary is pinned, or its last deploy onto it failed (`⇈!`);
hovering says what the primary is pinned to. `⇈` opens a menu that picks
**what this window shows**: the primary (it follows the role: after a
reassignment the window shows the new primary) or a non-primary deployment
— the frame then loads `/c/<tile>+<name>/` and reloads from that
deployment's events, and the head carries a `+<name>` tag, while the
window's terminal, code, logs and proposals stay the tile's. The menu also
opens the shown deployment's full page and the Deployments panel. On a
personal screen the pick is kept in your layout (the tile's entry gains
`"deployment": "<name>"`; an older shell and the app ignore it and show the
primary); on an org screen, whose layout reaches everyone, it lasts for this
page only. A deployment that is removed, that you may no longer see, or
that becomes the primary shows the primary again. The tile admin shows
`pinned to c:…` and a
deployments section (its Non-primary access selects take narrowing changes;
widening ones open the Deployments panel), and the admin console's runtime →
components tab lists non-primary deployments under their tile.

**In the xbin app** (D132) a tile's sessions screen has the same view
under its tools menu, **Live reload & deployments**: the header with its
actions and the branch offers, the deployments with `Dev API` (the last
tab's session) and `● live reload`, a deployment's overview with its
Branch row and a link to open its URL, and its deploy log with Roll back,
each change confirmed from a dry run in the same words. Adding, promoting,
reassigning, protecting and the edges stay in the web's panel.

## Committing never deploys

Commit as often as ever. A commit changes nothing anyone runs: only a save
(to the live reload target), Reload now, resume, attach, deploy, promote or
roll back does. Builtin updates and code PRs land in the work tree, so they
reach only the live reload target — try them on `dev`, then promote.

## For coding agents

The rules for an agent working on a tile, beyond the workspace `AGENTS.md`:

- **Check before you test or debug:** `bx live-reload` and `bx deployment
  ls`. Saves reach the live reload target (nobody while `paused`); your
  calls reach your target (`← this terminal`, `$XBIN_DEPLOYMENT`). A fix
  that "didn't work" may simply not be running where you look.
- **You can't switch your own target.** To call `dev`, ask the user to pick
  `🔌 target: dev` in the terminal's tile API select (the session restarts;
  an agent resumes its conversation). Reads work anyway: `bx status
  <tile>+dev`, `bx logs <tile>+dev`. If bx answers "unauthorized", this
  terminal's tile API is off.
- **Shipping to the primary is a deliberate act.** Run `bx deploy --to main`,
  `bx promote <tile> dev main`, `bx rollback --to main`, or `bx live-reload
  now`/`resume` while live reload last followed the primary, only when the
  user asked you to ship — never as part of committing, testing or tidying
  up. Try the change on a non-primary deployment first. Without a terminal bx
  prints what will change and stops (exit 4); `--yes` says the user asked.
- **Don't `bx deploy --to` the live reload target**: saves already reach it,
  and a deploy onto it pauses live reload.
- **Branches.** A deployment with an assigned branch (`bx live-reload`
  shows them) takes saves only while the work tree is on it: a `git
  checkout` of another branch pauses live reload there. Say so when you
  switch branches, and don't pass `--other-branch` unless the user asked.
- **A large change on a tile with no other deployment:** pause live reload,
  build and test in the terminal, and resume when done — resuming ships the
  work tree, so only when the user asked. Leave a pause someone else made
  (`bx live-reload` says who) unless the user asked.
- **A new deployment starts with empty data** and secret names only. Fill it
  with synthetic data through its own API, from a session that targets it.
  Copying the primary's data or secret values is a tile manager's act.
- **Non-primary deployments read other tiles' primaries and never write to
  them**; LLM turns through llm-gw and the agent tile's sandbox managers are
  unavailable there.
- **A refusal is not yours to work around** (exit 3): stop and tell the user
  who can.
- "3 fast crashes ⇒ failed" and "status resets on restart" hold per
  deployment; a pinned backend recovers through a deploy or a restart, not a
  save.

## What a checkpoint holds, and retention

- **What a checkpoint holds:** every file under the tile's directory except
  `.git` directories and nested components; ignore rules don't apply.
  Symlinks stay symlinks and the exec bit is kept; empty directories, other
  permission bits, xattrs and special files are not captured. A directory
  holding its own git repository is captured as plain files under
  `--isolate`; without isolation the checkpoint is refused, naming it.
  Gitignored files such as `.env` are captured too: keep secrets in the
  vault.
- **Caps per checkpoint:** 200 000 entries, 2 GiB of content, 256 MiB for one
  file, 1024-byte paths, 64 levels deep, 5 minutes. A refusal names the
  largest paths: large data belongs in a resource or outside the tile.
  Checkpoints of one tile are rate-limited: ten at once, then one every
  3 s.
- **A per-tile quota:** a tile's checkpoints, extracted trees and kept builds
  count against 10 GiB. At 90 % an alert shows; when it is full, new
  checkpoints and extractions are refused (507) and running deployments keep
  serving.
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
- **Purging a checkpoint** removes it at once — one that caught a secret, for
  example: `POST /api/xbin/deployments/purge {"tile": "<tile>",
  "checkpoint": "c:<id>"}`, a tile manager's act in their own session. The
  deploy log's entries naming it stay, naming no checkpoint; its content,
  git view and extracted tree go (content other checkpoints
  share stays). A checkpoint a deployment runs, or a deploy still uses, can't
  be purged (409). It works on a tile without deployments too, whose store
  outlives resuming onto `main`. Archives made earlier keep it.

## A tile's life, backups and downgrades

- **Lifecycle is the tile's.** Disabling, hiding or offloading a tile stops
  every deployment; enabling starts the primary (an alwaysOn primary at
  once), and each other deployment by its own alwaysOn switch or on its next
  request. A deployment URL has no lifecycle of its own.
- **Transfer** (`bx owner --transfer`) moves the tile's deployments with it,
  their settings and edge policies included, and restarts each running one
  under the new owner's ceilings.
- **Clone, templates and imports** copy the work tree only: the new tile
  starts in the zero state.
- **Removing and re-creating.** A tile created at a path never inherits a
  removed tile's deployments: creation drops the path's deployment record, so
  the new tile starts in the zero state. The old checkpoint store, and a
  removed tile's non-`main` vaults, registrations and data, stay as
  leftovers like a removed tile's grants: a non-admin can't create over them
  ([auth.md](/docs/auth.md), *Creating tiles*).
- **`+` in tile names.** Nobody, admins included, can create a tile whose
  path holds `+` in any segment, by any route (`bx new`, create, clone,
  template instantiate, builtin or git import): 403 `can't create
  apps/crm+dev: '+' isn't allowed in tile names (it names a tile deployment
  in URLs, /c/<tile>+<name>/) — pick another path`. A directory named so
  before 2026-09-28 keeps resolving, as an exact match, but can't get
  deployments; `bx doctor` flags it
  ([changes/2026-09-28-plus-in-tile-names.md](/docs/changes/2026-09-28-plus-in-tile-names.md)).
- **Backups** of a tile that has left the zero state carry its deployment
  record and checkpoint store, under `deployments/` in the archive; a tile in
  the zero state gets exactly today's archive. When the primary isn't
  `main`, every backup also archives the primary's data, in an archive of its
  own that the tile's archive lists. Other deployments' data is archived only
  when an admin asks — `POST /api/xbin/deployments/backup`, or a schedule per
  deployment (`/deployments/backup-schedule`, keeping the last 3 archives
  unless told) — under its own key, never using the tile's retention.
  `POST /api/xbin/deployments/restore` puts a deployment's data back,
  replacing what it holds.
- **Restoring a tile.** A restore refuses, before it writes anything, an
  archive whose deployment state belongs to another tile. This release
  doesn't put the deployment record and checkpoint store back yet: a restore
  leaves them out (and logs it), so a tile restored where none stood starts
  in the zero state ([overview/14-lifecycle.md](/docs/overview/14-lifecycle.md)).
- **An older xbind** ignores every record, store and non-`main` data and
  registration, and serves every tile's work tree with live reload. Before
  downgrading: reassign every primary back to `main` (data moves in neither
  direction); check out each pinned tile's primary checkpoint in its work
  tree (`git fetch xbin-deploy`, commit or stash unfinished work, `git
  checkout --no-track -b pre-downgrade deploy/main`); and tell agents that
  saves go live everywhere. After upgrading again, a pinned tile returns to
  its checkpoint; if you skipped the checkout, deploy the work tree at once
  (`bx deploy <tile> --to main`).
- **Restarting without `--isolate`** holds every pinned backend (see above),
  and non-primary backends stay down until isolation returns. Before that,
  resume live reload onto the primary on those tiles (unprotecting it
  first), and remove the non-primary deployments of backend tiles.

## The operator's switch

`--tile-deployments=off` (`XBIN_TILE_DEPLOYMENTS=off`) closes opting in and
growing: no tile can pause live reload, add a deployment or move code, and
`GET /api/xbin/deployments` lists no `features` and reports why in each
refusal. It never unpins anything: existing records keep governing what runs.
What leads back to the zero state or creates no state stays open — resuming
live reload onto `main`, restarting, removing a deployment, resetting its
data, unprotecting, clearing a deployment's branch, run now and purging — so
every tile can return to plain
live reload without a downgrade ([config.md](/docs/config.md)).
