# Protocol: deployments and checkpoints

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page: pausing live reload, a tile's named deployments, deploying,
promoting and rolling back, and the checkpoint remote. The builder page is
[tile-deployments.md](/docs/tile-deployments.md).

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it.

## Routes


```
Tile deployments: pausing live reload, a tile's deployments, promotion
(docs/tile-deployments.md; conventions and texts: §Tile deployments below
this block). features lists what this xbind speaks: live-reload/1 (pausing
live reload, the log, the diff, deploy, roll back, the checkpoint remote)
and deployments/1 (every other row). An older xbind answers a plain 404 or
405, which is how a client detects the feature; an act this build of xbind
can't do answers 501 ({"error","docs"}), and its Can reads "not built in
this xbind yet". Bodies are JSON, decoded strictly; each names `tile`, a
tile ref (apps/crm, or apps/crm+dev for one deployment; a body's deployment
and the ref's qualifier must agree). The GET rows take `tile` as a query
parameter: a tile's path, never a ref — a deployment is named with
`deployment=` (D127j; §Tile deployments, *Tile refs in a query string*). Each
body takes dryRun:true (judged as for real, refusals included, nothing
changes → {state, impact}) and seq (the record's sequence the caller acted
on: 409 when it moved; optional everywhere). A dry run of a deploy, roll
back or promote onto the primary whose code asks for another partition
mode than the tile records carries impact.partition, a warning: the tile
will pause for a partition-mode decision (it holds data), the mode follows
at once (it holds none), or a manager declined that mode (docs/partitions.md
§The mode). confirm tokens guard data: remove "erase", reset and a restore into data "erase-data", seed
and add with data:"seed" "copy-data", primary "data-stays" — a missing one
is 400 naming it. An operation answers {state, deploy?, …} (each row names
its answer) once the record change is committed; deploys are asynchronous
(follow the deploy entry with the log's ?id= and `deployments` events). On a
tile without deployments nothing here writes a file, except the three
opt-ins (live-reload/pause, add, protect) and purge, which creates nothing;
any other POST on it answers 409. Onto a protected primary every code move
names the checkpoint its actor reviewed (checkpoint on deploy and rollback;
expect on live-reload/now, promote and primary; plus seq), else 400 before
anything is captured, dry runs included; restart:true needs neither.
"tile manager" below is a person's own session — the tile's owner, its
org's admins, a workspace admin; the root token (bx on the host) is one —
never a terminal, agent or tile credential, whatever grants its tile holds.
One exception (D127m): on primary, protect, deliveries
and always-on, a frame of a tile holding the `xbin` admin capability (the
admin console, `tiles/admin`) whose token was minted under a person's own
login — a session or the root token, not a view-as session, not a token a
terminal or agent session minted — stands in for that person, and the person
is judged as a tile manager. For a tile its person manages that frame is
also the write audience of the reads: GET /deployments in the full view,
the log, and a diff without a work-tree side (not `deployments` events,
which it gets as any frame does). Every other manager route, and
every code move, refuses it like any tile credential (docs/auth.md §Tile
deployments).

GET    /deployments?tile=<tile>&deployment=<name>
                                   read on the tile, or the tile itself
                                   (readers get the primary only). → State
                                   in the caller's view: full (the tile's
                                   writers), deployment (a non-primary
                                   deployment's own credentials: the
                                   primary and that deployment) or reader
                                   (facts about the primary only — no other
                                   deployment's name, no count).
                                   record:false for a tile without
                                   deployments, and nothing is written.
                                   features lists what this xbind speaks
                                   (live-reload/1, deployments/1; none
                                   while --tile-deployments=off).
                                   deployment= selects one, echoed as
                                   selected; one naming a non-primary
                                   deployment is 403 for a caller outside
                                   its audience, whether or not it exists,
                                   and 404 (no such deployment) for the
                                   rest when it doesn't exist; a malformed
                                   name is 400. tile= is a
                                   tile's path: a '+' in it that names no
                                   tile, escaped or read as a space, is
                                   400 (D127j)
GET    /deployments/log?tile=<tile>&deployment=<name>&limit=<n>&before=<id>
                                   write on the tile, or its terminal/agent
                                   sessions. → {tile, entries:[DeployEntry],
                                   more}: every
                                   attempt, newest first — queued and
                                   running ones included, and failed ones
                                   (limit default 50, max 200); without
                                   deployment every
                                   deployment's entries, by id;
                                   ?id=<id>&wait=<s> one attempt,
                                   held until it finishes (wait ≤ 25) →
                                   {entry}. A non-primary deployment's own
                                   credentials see its entries only;
                                   readers and the primary's frame and
                                   backend tokens are refused
GET    /deployments/diff?tile=<tile>&from=<spec>&to=<spec>&path=<file>&stat=1
                                   write on the tile, or its terminal/agent
                                   sessions; a work-tree side captures a
                                   checkpoint and needs terminal
                                   level. spec: c:<id> | deployment:<name> |
                                   work-tree (default: the primary → the
                                   work tree). → text/x-diff with
                                   X-XBin-Checkpoint-From / -To (X-Truncated
                                   past 16 MiB), or stat=1 → {from, to,
                                   files:[{path, status, added, removed,
                                   binary?}], truncated}. Confined. 409 on a
                                   tile without deployments (nothing is
                                   captured), 429 while one diff runs and
                                   one waits for the tile, 504 past 30 s
POST   /deployments/live-reload/pause
                                   terminal-level on the tile (its own
                                   terminal/agent tokens count). {tile} →
                                   {state, deploy?}: saves stop
                                   reaching the live reload target, which
                                   keeps a fresh checkpoint of the work
                                   tree. The opt-in that creates a tile's
                                   record. Idempotent. 409 for a backend
                                   tile without --isolate
POST   /deployments/live-reload/resume
                                   terminal-level on the tile. {tile,
                                   deployment?, confirm?} → {state, deploy}:
                                   deployment (default: where live
                                   reload last was) follows the work tree
                                   again. Onto main, while it is the only
                                   deployment and every setting is at its
                                   default, the record is removed
                                   (record:false). 409 while live reload is
                                   on another deployment (attach instead),
                                   and onto a protected primary
POST   /deployments/live-reload/now
                                   terminal-level on the tile. {tile,
                                   expect?, confirm?} → {state, deploy?}: checkpoint
                                   the work tree and deploy it
                                   once where live reload last was, which
                                   stays pinned (unchanged:true when nothing
                                   moved). Onto a protected primary: a tile
                                   manager, with expect and seq
POST   /deployments/live-reload/attach
                                   terminal-level on the tile. {tile,
                                   deployment, confirm?} → {state, deploy}: the work
                                   tree is checkpointed, the former target
                                   is pinned to that checkpoint (its deploy
                                   entry is how attach too), and deployment
                                   follows the work tree. Idempotent. 409
                                   "live reload is paused: resume it onto
                                   <name> instead", and onto a protected
                                   primary
POST   /deployments/add            terminal-level on the tile.
                                   {tile, deployment, from?: work-tree|
                                   primary|c:<id>, data?: empty|seed,
                                   attach?, confirm?, branch? | newBranch?}
                                   → {state, deploy, joins?}: a new
                                   deployment with its code
                                   (default a fresh checkpoint of the work
                                   tree), its own data (empty; seed —
                                   confirm:"copy-data" — and joining data
                                   already seeded, restored or partly so are
                                   a tile manager's acts), a vault holding
                                   the primary's key names without values,
                                   deliveries on, alwaysOn off, limits at
                                   the tile's. Its code is built in the
                                   background and its process starts on its
                                   first request. attach:true (from
                                   work-tree only; 400 otherwise) moves live
                                   reload onto it. data:"seed" seeds it once
                                   it is added (409 on a workspace-scope
                                   tile, whose data has one namespace); a
                                   seed refused then leaves it added, empty.
                                   joins: the (scope, name) data a sibling
                                   tile already claims, with its state and
                                   who seeded or restored it. A dry run on a
                                   tile without deployments captures nothing
                                   (impact.code null). An opt-in. A name is
                                   lowercase letters, digits and -, a letter
                                   first, at most 24; not main. 409 "<tile>
                                   already has a deployment "<name>"", "a
                                   tile exists at <tile>+<name>; pick another
                                   name", "<tile>'s name holds '+', which
                                   names a tile deployment in URLs: it can't
                                   get deployments — clone it to a path
                                   without '+' (POST /api/xbin/clone) to give
                                   it some" (D127j), "<tile> has <n> non-primary
                                   deployments, the most allowed here", "the
                                   workspace has <n> non-primary deployments,
                                   the most allowed here", "<tile> can't
                                   have non-primary deployments: <reason>"
                                   (workspace chrome, code that asks for
                                   chrome, an xbin or xbin:* grant), and for
                                   a backend tile without --isolate.
                                   branches/1: branch assigns it a branch
                                   (*Assigned branches* below); newBranch
                                   creates that branch in the tile first
                                   and checks it out (409 "<tile> already
                                   has a branch <b>: newBranch only creates
                                   one …"; a dry run creates nothing). Code
                                   from a work tree on another branch than
                                   the one assigned needs confirm
                                   "other-branch" ("copy-data,other-branch"
                                   with data:"seed")
POST   /deployments/remove         terminal-level on the tile.
                                   {tile, deployment, confirm:"erase"} →
                                   {state}: stops it and ends its queued and
                                   running deploys; deletes its vault, logs,
                                   registrations, build products, and its
                                   data when this tile is the last of its
                                   scope with that deployment (a sibling
                                   keeps it otherwise); checkpoints stay
                                   until GC. Live reload attached to it
                                   detaches, and lastLiveReload falls to the
                                   primary. 409 "main can't be removed",
                                   "<name> is the primary of <tile>"
POST   /deployments/deploy         terminal-level on the tile. {tile,
                                   deployment, checkpoint? | expect? |
                                   restart?, confirm?} → {state, deploy?}: puts the
                                   checkpoint (default: a fresh one of the
                                   work tree, equal to expect when given) on
                                   deployment; live reload attached to it
                                   pauses. restart:true starts a new
                                   generation of its current code and clears
                                   the crash breaker (terminal level on
                                   every deployment, a protected primary
                                   included: it moves no code). Onto a
                                   protected primary: a tile manager, with
                                   checkpoint and seq
POST   /deployments/promote        as deploy, for the target.
                                   {tile, from, to, expect?} → {state,
                                   deploy?}: to receives from's current
                                   code (its checkpoint, or a fresh
                                   checkpoint of the work tree while from
                                   follows it) — code only, data stays; the
                                   entry carries from. Live reload attached
                                   to to pauses. Onto a protected primary
                                   expect and seq
POST   /deployments/rollback       as deploy. {tile, deployment,
                                   checkpoint?} → {state, deploy?}: default
                                   the newest ok entry of its deploy log
                                   with other code; data stays. Onto a
                                   protected primary checkpoint and seq
POST   /deployments/primary        tile manager. {tile, deployment,
                                   confirm:"data-stays", expect?} →
                                   {state, deploy?, inactiveHosts?}: the
                                   bare URL moves to deployment; data and
                                   xbin.json don't, limits stay with each
                                   deployment. The old primary's interface
                                   instances and ingress hosts go dormant
                                   at once and the new one's activate (cron
                                   jobs and bus subscriptions stay with the
                                   deployment that registered them); the
                                   answer returns
                                   once routing moved, and both
                                   generations restart after it, the new
                                   primary first; sessions named after
                                   either restart. Onto a protected primary
                                   (expect and seq), a target that follows
                                   the work tree is first pinned to exactly
                                   expect (deploy entry how "reassign").
                                   inactiveHosts: its ingress hosts that
                                   conflict with another tile's and stay
                                   off. Idempotent. 409 "<name> isn't
                                   healthy (<state>); only a healthy
                                   deployment can become the primary", and
                                   "reassigning the primary of <tile> would
                                   split <scope>'s data: …" unless the tile
                                   is in the workspace scope or alone in the
                                   scope it roots; "<tile> is partitioned:
                                   switching the primary would leave every
                                   person's data with <primary>: promote
                                   instead" when its recorded partition
                                   mode has user partitions
POST   /deployments/protect        tile manager. {tile, on, expect?} →
                                   {state, deploy?, nested?, warnings?}: on
                                   pins the primary in place (while live
                                   reload is on it, to a fresh checkpoint,
                                   equal to expect when given; how
                                   "protect"); from then on only tile
                                   managers change its code, naming the
                                   checkpoint they reviewed, and sessions
                                   following it restart onto the default
                                   target (protocol/terminals.md). off: nothing moves;
                                   the primary stays pinned. nested (dry
                                   runs too): the components nested in the
                                   tile, [{tile, protected, manager}], whose
                                   code its writers change; warnings: one
                                   per unprotected nested component, and
                                   "not enforced: authentication is off"
                                   under --no-auth. An opt-in. Idempotent.
                                   off answers 409 (kind policy) while the
                                   tile, or a partitioned tile it is bound
                                   into, runs reviewed code only (POST
                                   /partitions/reviewed)
POST   /deployments/edge           tile manager. {tile, edge:
                                   slot:<slot>|grant:<target>, policy: read|
                                   block|inherit|default} → {state}: that
                                   edge's policy for every non-primary
                                   deployment of the tile, at their next
                                   call; default removes the override (also
                                   one whose edge is gone). The values an
                                   edge takes are its Edge.values (§Tile
                                   deployments, *The edge policy*). 400
                                   "<edge> takes <values>: <reason>", 404
                                   "<tile> has no edge <id>"; a net slot
                                   sharing the host's network takes inherit
                                   but stays block (effective + why).
                                   Idempotent
POST   /deployments/deliveries     tile manager. {tile, deployment, on} →
                                   {state}: the off switch of its cron jobs
                                   and bus push subscriptions, on by
                                   default. on:false keeps them registered
                                   but dormant (no tick, no delivery);
                                   on:true makes them fire for it again.
                                   Interface instances and ingress hosts
                                   never activate off the primary either
                                   way. 409 for the primary, whose
                                   registrations are always active
POST   /deployments/always-on      tile manager. {tile, deployment, on} →
                                   {state}: it is kept up — restarted after
                                   an exit on its own backoff, never reaped
                                   — while its own code declares alwaysOn
                                   and this switch is on. 409 for the
                                   primary, and when its code doesn't
                                   declare alwaysOn
POST   /deployments/limits         tile manager. {tile, deployment,
                                   limits:{memMiB?, pids?, diskGiB?}} →
                                   {state}: each override lowers the tile's
                                   ceiling for this deployment; null
                                   removes one, an absent key stays.
                                   memMiB and pids apply to its cgroup leaf
                                   from its next generation; diskGiB is the
                                   quota of its data (the lowest among the
                                   scope's tiles that have it), set on the
                                   scope's root tile only. 400 "<limit>
                                   can't exceed the tile's ceiling (<n>)" or
                                   not a positive integer; 409 "the quota
                                   of <scope>'s "<name>" data is set on
                                   <root tile>" elsewhere, the workspace
                                   scope included
POST   /deployments/seed           tile manager of every tile that has the
                                   deployment. {tile, deployment,
                                   confirm:"copy-data", stop?} → {state}:
                                   its data, replaced by a copy of the
                                   scope primary's (§Tile deployments, *Data
                                   per deployment*). Answers once the data
                                   is held (data.busy "seeding");
                                   completion is a deployments event, op
                                   data. 409 for the primary, while another
                                   data act runs, when the primary's data is
                                   over the target's disk limit or the disk
                                   is low, and when stop:true is required;
                                   503 while the vault is sealed
POST   /deployments/reset          terminal-level on every tile of the
                                   scope that has the deployment (main,
                                   while it isn't the primary: a tile
                                   manager's). {tile, deployment,
                                   confirm:"erase-data", vault?} → {state}:
                                   empties its data (data.state empty,
                                   reset:true); vault:true also empties its
                                   vault back to placeholders. Never data
                                   some tile serves as its primary, never
                                   res:workspace/*. 403 "<operation> of
                                   <scope>'s "<name>" data needs <level> on
                                   <tile> too"; 409 for the primary and while
                                   another data act runs
POST   /deployments/vault-copy     tile manager. {tile, deployment, keys? |
                                   all?} (exactly one) → {state, copied,
                                   missing}: the primary's values overwrite
                                   its keys, never the other way; the audit
                                   names keys, never values. 409 for the
                                   primary; the vault routes' 503 while
                                   sealed
POST   /deployments/backup         admin in a person's own session. {tile,
                                   deployment} → {ok, deployment, version}:
                                   archives its data now, through the tile's
                                   bound @archive provider, under its own
                                   archive key (POST /backup is unchanged).
                                   502 with no archiver bound
GET    /deployments/backups?tile=<p>&deployment=<name>
                                   admin (as above) → {deployment,
                                   versions:[{version, time, size}],
                                   archiver}: empty versions without an
                                   archiver, as GET /backups. deployment
                                   defaults to the primary; a removed
                                   deployment's archives are still listed
POST   /deployments/restore        admin (as above), and the reset level on
                                   every tile claiming the target's data.
                                   {tile, deployment, version?, into?,
                                   replace?, confirm?} → {ok, deployment,
                                   into, restored, skipped}: data only,
                                   never the work tree (POST /restore is
                                   unchanged). deployment: whose archive
                                   (main: the data part of the tile's — of
                                   a sealed workspace's, the data archive
                                   its main archive names; its key erased:
                                   409 "this backup's data was erased on
                                   <date> (<reason>)");
                                   version: default latest; into: the
                                   target, default the archive's own.
                                   Beyond main each archived resource is
                                   emptied first; main merges unless
                                   replace:true. confirm:"erase-data" only
                                   when the target holds data; skipped
                                   names archived resources the target
                                   doesn't declare. Stops every deployment
                                   using the target's data; data.state
                                   becomes restored. 409 when the target
                                   doesn't exist (the answer lists the
                                   choices) or its data is busy, and for an
                                   archive older than the tile's last
                                   partition mode switch that deleted its
                                   data (main's; a deployment's after one
                                   that deleted everything,
                                   docs/partitions.md §Backups): this route
                                   can't confirm it — only POST /restore,
                                   the whole tile's (its source and every
                                   deployment's data that backup lists),
                                   takes the switch's date
POST   /deployments/backup-schedule
                                   admin (as above). {tile, deployment,
                                   schedule, retention?} → {state}: a
                                   non-primary deployment's data on a
                                   5-field cron schedule, keeping retention
                                   versions (default 3); schedule ""
                                   removes it
POST   /deployments/run-now        terminal-level on the tile. {tile,
                                   deployment, job} → {state,
                                   delivery:{status, ms}}: delivers the job
                                   once to that deployment as xbin/cron
                                   with the job's role, whatever its
                                   deliveries switch says, and waits for the
                                   handler (≤ 2 min); status is what it
                                   answered. 409 for the primary (its jobs
                                   fire on schedule) and "<job> is already
                                   running on <name>"; 502 when its backend
                                   can't start
POST   /deployments/purge          tile manager. {tile, checkpoint:"c:<id>"}
                                   → {state, purged, entries}: every deploy
                                   log entry of the tile naming the
                                   checkpoint is rewritten to name none
                                   (entries: how many), and its content,
                                   git view and materialized tree are
                                   pruned at once; content other
                                   checkpoints share stays, archives made
                                   earlier keep it. No confirm token: the
                                   id names what goes. Accepted on a tile
                                   without deployments, whose store
                                   outlives the opt-out. 409 "<tile>:
                                   checkpoint <id> is in use — <why>; only a
                                   checkpoint no deployment runs can be
                                   purged" (a deployment runs it, a deploy of
                                   it hasn't finished, a running generation
                                   binds it); 404 a checkpoint the tile
                                   doesn't have
POST   /deployments/branch         terminal-level on the tile. branches/1.
                                   {tile, deployment, branch: "<b>"|null}
                                   → {state}: the work tree's branch the
                                   deployment requires (*Assigned
                                   branches* below); null clears it, and
                                   either drops its override. branch is
                                   required (400 "bad request body: branch
                                   is required …"); a name xbind doesn't
                                   take is 400. The dry run's impact.branch
                                   names the work tree's branch, and
                                   pausesLiveReload says live reload on it
                                   pauses at the next save. 409 "main takes
                                   no assigned branch …", "<name> is the
                                   primary of <tile> — the primary takes no
                                   assigned branch". Idempotent
GET    /checkpoints/<tile>.git/<path>
                                   the tile's terminal/agent sessions, or
                                   write on the tile. Read-only dumb HTTP
                                   git (git fetch xbin-deploy): the tile's
                                   view repository —
                                   refs/heads/deploy/<name> per pinned
                                   deployment and HEAD naming the primary's,
                                   nothing else. path: HEAD, info/refs,
                                   objects/info/packs, a pack or a loose
                                   object; anything else 404
```

## Tile deployments (`/deployments`, `/checkpoints`)


The builder's view is [tile-deployments.md](/docs/tile-deployments.md);
this is the wire's. Every tile has one deployment, `main`, which starts as
its **primary**: what the bare URLs, grants, bindings, interface instances,
ingress and every other tile reach (a cron job or bus subscription reaches
the deployment that registered it). A tile's developers can pause live
reload, add deployments (`dev`, …) with their own URL, data, vault and deploy
log, promote code between them, and a tile manager can make another
deployment the primary or protect the primary. A tile that never opts in has
no deployment record, and every surface below answers for it exactly as
before.

**The state** (`GET /deployments`) depends only on who asks, never on how
many deployments exist. `view` is:
- `full` for the tile's write audience: admins, people with write (at their
  current level), and the tile's own terminal and agent sessions while their
  user has write;
- `deployment` for a non-primary deployment's own frame and backend
  credentials: the full view cut to the primary and that deployment. Other
  deployments are left out, `liveReload` and `lastLiveReload` read `""` when
  they name one of them, and `edges`, `caps`, `allowed` and `workTree` are
  omitted;
- `reader` for everyone else who may read the tile, the primary's frame and
  backend credentials included: the primary's facts only. `liveReload` reads
  the primary's name while it follows the work tree and `""` otherwise.
  `selected` (but for the primary's alias), `seq`, `lastLiveReload`,
  `liveReloadSince`, `workTree`, `allowed`, `caps` and `edges` are left out,
  and so is every other deployment. On the primary, `status.error`,
  `status.queued`, `data`, `vault`, `limits`, `deliveries`, `alwaysOn`,
  `alwaysOnDeclared`, `registrations`, `wouldNotify`, `created`, `by`,
  `can`, and the deploy entries' `id`, `how`, `from` and `session` are left
  out too.

A zero-state answer (`record: false`) is the same for every view but for
`workTree.branch`, which only the write audience gets, and reading it writes
nothing. `features` lists what this xbind speaks (`live-reload/1`,
`deployments/1`, `branches/1`; none while `--tile-deployments=off`): a
client sends a body field a feature added — `branches/1`'s `branch` and
`newBranch` on add, `confirm: "other-branch"`, the branch route — only to
an xbind that lists it, since bodies are decoded strictly. Every `can` and
`allowed` entry is a `{ok, why?, kind?}` judged by the same policy that
judges the request (kind `authority`, `policy` or `state`). While paused,
`workTree` `{changed, since}` counts the files that differ from the
checkpoint live reload was paused at. For the write audience `workTree`
also carries `branch`, on every read: the branch the tile's own repository
has checked out, `""` for a detached HEAD or none (*Assigned branches*).
A deployment with an assigned branch carries `branch`, and
`branchOverride` while it takes another this time; `can.branch` judges the
branch route. `caps` counts the tile's and the
workspace's non-primary deployments against their caps (*Runtime* below);
`edges` lists the tile's outbound edges and their policies (*The edge
policy*). Each deployment carries `url` and `api` (the qualified forms below;
the primary's are the bare ones), `origin` (origins mode), `data`, `vault`
(`{keys, placeholders}`), `limits` (`{memMiB, pids, diskGiB, overrides}`),
`deliveries` (true for the primary, and for any other deployment unless a
tile manager switched it off), `alwaysOn`, `alwaysOnDeclared`, `backup` (a
non-primary deployment's schedule), `registrations` (its cron jobs, bus
subscriptions, interface instances and ingress hosts, each with `dormant`)
and `wouldNotify` (its last 20 held notifications).

**Tile refs in a query string.** A query string never carries the
qualifier: a `+` there decodes to a space (D127j). A query parameter that
names a tile (`tile=`, `component=`) takes the tile's path, and the
deployment rides its own parameter, `deployment=` —
`/deployments?tile=apps/crm&deployment=dev`, never `?tile=apps/crm+dev`.
- The GET rows above, `/frame-token`, `/logs` and `/tile-status` answer 400
  `a deployment is named with deployment=, not tile+name (a '+' in a query
  string reads as a space)` for a tile parameter that holds a `+` and names
  no tile, and for one whose `+` a client sent unescaped: a space splitting
  it into a tile and a deployment name.
- A tile whose own name holds `+` (one created before the name rule) is an
  exact match and resolves: send its `+` as `%2B`, as `URLSearchParams` and
  Go's `url.Values` do.
- JSON bodies (`tile` of the POST rows) and URL paths (`/c/`, `/api/`) keep
  the `<tile>+<name>` ref: a path keeps its `+`.

`limit` above 200 and `wait` above 25 are clamped, not refused.

**Tile names never hold `+`.** Creating a tile whose path holds `+` in any
segment is refused (403) for every creator, admins included, on every
creation route (`/create`, `/clone`, `/templates/new`, `/builtins/import`,
`/git/import`) and by `bx new`'s local write: `<tile>+<name>` is a
deployment's URL. A directory named so before the rule keeps resolving, as
an exact match, but can't get deployments (`/deployments/add` answers 409
`<tile>'s name holds '+', which names a tile deployment in URLs: it can't get
deployments — …`); `bx doctor` flags it.

**Deployment URLs.** `/c/<tile>+<name>/…` and `/api/<tile>+<name>/…` reach
deployment `<name>`; the qualifier sits in the tile path's last segment.
- It is read only for a tile that has deployments, and only when nothing
  answers the path today: a tile, or anything on disk, at `<tile>+<name>`
  wins, so a directory whose name holds `+` keeps working, and for a tile
  without deployments `+<name>` means nothing. A `+` in a query string is
  never the qualifier (above).
- `<tile>+<primary>` is the bare URL, for people and the tile's own
  credentials. An unknown name answers 404 `<tile> has no deployment
  "<name>"` to those who may open deployment URLs, and 403 to everyone else.
- A qualified URL never enters a nested tile: 404 `<path> is a tile of its
  own; its deployments are /c/<path>+<name>/`. A nested tile's deployments
  carry the qualifier on its own last segment (`/c/apps/crm/widgets+dev/`).
- **Who.** People with write on the tile (terminal level and admins
  included), checked on every request: a reader gets 403 `deployment URLs
  need write access on <tile>`, whether or not the name exists. Also the
  tile's own frame, terminal and backend credentials bound to that
  deployment. Other tiles never, the alias included: 403 `<caller> opens <tile> by its bare
  URL, /c/<tile>/, which serves its primary: deployment URLs are for the tile
  itself and the people who work on it` (on `/api/`: `<caller> calls <tile>
  by its bare URL, /api/<tile>/, which reaches its primary: …`). The bare
  `/c/<tile>/` serves the primary whoever asks; only `/api/` has the
  self-call rule below.
- **Asset modes.** Legacy mode serves a deployment's non-document files by
  the credential-less subresource rule, as it serves the tile's. Tokens mode
  serves them under an asset token whose user writes the tile. Origins mode
  serves each deployment on its own origin (protocol/core.md, *tile origin*).
- **Documents.** A document at a deployment URL carries `<meta
  name="xbin-deployment" content="<name>">` and an import-map entry mapping
  `/c/<tile>/` to `/c/<tile>+<name>/` (under the asset token in tokens mode),
  so absolute self-imports stay in the deployment. An absolute `/c/<tile>/`
  in `src` or `href` loads the primary's files; relative URLs are the
  portable form. `xbin-component` stays the tile path. Such a document is
  never chrome, whatever its code declares. `?native=1` there runs that
  deployment's native entry.
- **Frame tokens.** A document's frame token names its deployment (six
  fields when it isn't `main`, protocol.md §Authentication). A person gets one only for a
  deployment they may open (read for the primary, write for any other), a
  tile's own credential only for the deployment it is bound to, and the
  one-tree navigation rule holds only between primaries: a frame of `main`
  that fetches `dev`'s URL gets no `dev` token, and a frame of `dev` gets no
  primary token from the bare URL.
- **`/api/`.** Admins and the tile's own credentials bound to that
  deployment reach it; a person with write passes the URL gate and then
  holds no role there, as on the bare URL. The qualifier is consumed: the
  backend sees `/<endpoint>`. Lifecycle is the tile's: 409 with
  `X-XBin-Lifecycle`, as on the bare URL.
- **The inbound surface follows the primary's code.** `template`, `exposes`,
  `expose.roles`, `provides` and `chrome` come from the primary's code (its
  checkpoint while pinned): the bare `/api/<tile>/` template answer, whether
  bare documents are chrome, ingress routes, interface bindings and the roles
  callers are checked against. A save while live reload is paused, or
  attached to another deployment, changes none of them.

**Which deployment a call acts on.**
- **Bound credentials.** A tile's own credential acts in one deployment: an
  instance token in the one its backend generation runs; a frame token or
  tile-origin cookie in its document's (no field means `main`); a terminal or
  agent token in its session's target, or the current primary for a session
  that follows it. While the primary is protected a session that follows it
  is bound to nothing: every self-call and self-scoped route answers 403
  `the primary of <tile> is protected: terminal and agent sessions can't
  target it` until the session restarts. People, the owner, `xbin/cron` and
  `xbin/bus` are bound to nothing.
- **Self-calls.** `/api/<self>/…` reaches the bound deployment, at `admin`.
  The self-scoped routes act on it too: vault, kv, blob, bus publish, cron,
  bus subscriptions, interface instances, ingress hosts, tile-report,
  notify, prefs, and `/logs` and `/tile-status` without `deployment`.
  Naming another deployment of its own tile is refused: on `/api/` 403
  `<tile>'s deployment "<d>" can only call itself: this credential belongs to
  "<d>", not "<other>". A tile's code never reaches another deployment of its
  own tile; a person switches deployments by URL, a terminal by changing its
  target.`; on a self-scoped route 403 `a tile's own credentials act only on
  their own deployment (<bound>)`. A frame of a non-primary deployment whose
  user no longer writes the tile gets 403 `deployments of <tile> need write
  access`. A credential whose deployment was removed gets 404 `<tile> has no
  deployment "<name>"`; nothing ever falls back to the primary.
- **Named calls.** A call that names a tile acts on its primary unless it
  names a deployment, by a qualified URL or a `deployment` parameter. Existing
  routes keep taking the bare path in `component=`, `tile=` and `cwd=` (a
  qualified string there is looked up literally) and gain a separate
  `deployment` parameter. Naming a non-primary deployment needs write on the
  tile, or a credential bound to it; the tile's terminal and agent tokens
  may name any deployment in xbind's own reads (`/logs`, `/tile-status`,
  `/deployments`). Other tiles never reach a non-primary deployment.
- **The echo rule.** Every `deployment` parameter sent to an existing route is
  echoed in the answer: a `deployment` field, or `X-XBin-Deployment` on a
  non-JSON answer. An older xbind ignores the parameter and answers for the
  primary, so a client that finds no echo treats the answer as the
  primary's and says so.
- **Route classes.** Non-primary credentials are default-deny on
  `/api/xbin/*`. Every route is classed deployment-scoped (it acts on the
  caller's own deployment: the self-scoped routes, this family, the reads
  above), primary-only, or neutral (it has no deployment in it and behaves as
  before). On a primary-only route a non-primary credential gets 403 `this
  route is the primary's alone: a non-primary deployment's credentials can't
  use it (<deployment>)`, on a route without a class 403 `this route isn't
  available to a non-primary deployment's credentials yet (<deployment>)`,
  reads included; a credential bound to a removed deployment gets the 404
  above, and a session following a protected primary that primary's 403.
  Primary-only: tile creation (create, clone, git import, templates, builtin
  import and update), lifecycle, grant and binding writes, owner transfer and
  its impact report, access, users, orgs, sets, policy, defaults, screen
  writes, and every admin API (backups, the vault barrier, vaults,
  resources, auth-overview, backends, runtime, ingress, gpus, the VM policy,
  token rotation, view-as, the native-runtime, chrome, branding and
  workspace-policies and workspace-settings writes, push config and
  devices), and partition mode decisions (`POST /partitions/mode`). Deciding
  a PR (`POST /code/pr/state`) is
  primary-only for backends: 403 `deciding a PR is the primary's act: a
  non-primary deployment's backend can't do it (<deployment>)`.
- **Audit.** A tile credential acting in a deployment other than `main`
  adds `deployment=<name>` to its `audit` line.

**`X-XBin-Deployment` and `XBIN_DEPLOYMENT`** follow one rule: absent means
the primary.
- `X-XBin-Deployment: <name>` is injected on every proxied request whose
  caller is one of a tile's own credentials bound to a non-primary
  deployment: its self-calls and its calls to other tiles' primaries. It is
  absent on the primary's calls and on those of people, the owner,
  `xbin/cron`, `xbin/bus` and ingress, and stripped inbound like every
  `X-XBin-*`, so receiving it means xbind set it. `X-XBin-From` stays the
  tile path. A proxied response from a non-primary deployment carries it
  too, set by xbind over the backend's own, so `curl -i` tells which
  deployment answered.
- `XBIN_DEPLOYMENT=<name>` is set for a backend whose deployment isn't its
  tile's primary when it spawns, and for a terminal or agent session whose
  target is a named deployment other than the primary at session start;
  absent otherwise. A reassignment of the primary restarts both backends
  (the new primary first) and the sessions named after either, so it holds
  for a process's life. It is informational: routing reads the credential,
  never env. `XBIN_COMPONENT` (the SDK's `Self()`, `xbin.self`) stays the
  tile path in every deployment, so vault URLs and `res:${self}` ids keep
  working, and `XBIN_RES_*` values are the same in every deployment.
- Credentials and stored state say `main` by leaving it out instead: the
  frame token's sixth field, tile-origin labels, tickets and cookies,
  `/sandboxes` ids, `deployment` on `bus` events and every storage key name a
  deployment only when it isn't `main`, so they keep their meaning when the
  primary moves.

**Registrations of a deployment that isn't the primary** (`main` beside
another primary included) are stored for that deployment.
- Its cron jobs and bus push subscriptions are active: a tick or delivery
  reaches the deployment that registered it (its backend, its data), never
  the primary. A tile manager's deliveries switch (on by default) turns them
  off: they stay registered and dormant — a dormant job doesn't tick, a
  dormant subscription receives nothing (its events count as
  `dormantEvents`) — and registering answers 200 with `"dormant": true`.
  `run-now` delivers one job once, whatever the switch says.
- Its interface instances and ingress hosts are dormant, deliveries on or
  not: registering answers 200 with `"dormant": true`, so a backend that
  registers at start keeps working, and they never route: no grants event,
  no re-wiring, no reconcile, and ingress hosts are zone-validated but meet
  no conflict check. Only the primary's route, and a reassignment changes
  which set does at once.
- A registration on another scope's resource is refused while its edge is
  `block`, and stored under `read`: a job only ever calls its own
  deployment's handler, and a subscription reads the scope's primary's bus,
  like a read binding, its edge re-checked at every delivery. Publishing is
  unchanged: into the deployment's own data only.
- `main`'s registrations stay in the stores they always used, whether or not
  `main` is the primary; another deployment's live in files of its own
  (protocol.md §Filesystem contract), which an older xbind never loads.
- A non-primary deployment's tile status and notices travel only in
  `deployments` events (ops `status` and `notify`): nothing is pushed or
  counted, no `status` event is sent and `GET /tile-report` never lists them.

**Data per deployment.** Each deployment beyond `main` has its own data in
its tile's scope: kv, blob, bus and filesystem and sqlite volumes, declared
by that deployment's own code (its checkpoint's `scope.json` while pinned,
the work tree's while live reload follows it); beyond `main` a deployment's
data holds at most 64 resources. Its data is shared by the scope's tiles
that have a deployment of the same name.
- Resource ids and `XBIN_RES_*` values are the same in every deployment, and
  file resources are bound at the same paths, so code that stores absolute
  paths keeps working. kv, blob and bus publish calls resolve in the caller's
  deployment's data, with no wire change; volumes beyond `main` mount on
  first use. A start whose code declares a file resource its deployment has
  no data for fails with `no data namespace for <deployment>'s resource
  XBIN_RES_<NAME>`.
- Other scopes' data, and the workspace's, are read-only to non-primary
  deployments: 403 `<tile>+<name> may not write <res>: non-primary
  deployments reach other scopes read-only (edge policy "read")`.
- `Deployment.data.state` is `original` (`main`, untouched), `empty`,
  `seeded`, `restored` or `partial`, with `busy` (`seeding`, `resetting`,
  `restoring`) while an act runs. Meanwhile that data's kv, blob and bus
  requests answer 503 with `Retry-After`, the deployments using it don't
  start, and another data act answers 409 `<name>'s data is being
  <seeded|reset|restored>`. An act cut short (a failure, a crash) leaves the
  data `partial`, and its deployments refuse to start (`<act> of <scope> for
  <name> failed at <step>: reset or seed again`) until a reset or a new seed.
  Completion is a `deployments` event, op `data`.
- **Seed** copies the scope primary's data over the deployment's. Online by
  default: kv consistent at one moment, each SQLite database at one point in
  time, files and blobs file by file; `stop: true` stops the primary for a
  point-in-time copy of everything, and is required for a single-tenant
  container store and for a VM primary with file resources. Resources both
  deployments' code declare with the same type are copied; those only the
  target declares start empty; the rest are skipped and listed. Special
  files (FIFOs, sockets) are not copied, nor are the vault (vault copy is its
  own act), cron jobs, bus subscriptions or registrations. The seed is
  refused (409) while another data act runs, when the primary's data is over
  the target's disk limit or the disk is low, and (503) while the vault is
  sealed or encryption isn't ready.
- **Reset** empties the deployment's data. It never touches data some tile
  serves as its primary, nor `res:workspace/*`.
- **Deletion.** Removing a deployment deletes its data only with the scope's
  last tile that has that deployment. Data no tile claims any more (a
  `scope.json` that went away, a record removed) is kept 14 days, listed to
  admins, then deleted; `main`'s data is never collected.
- **Disk.** Each deployment's data beyond `main` is a quota bucket of its own,
  at the scope quota or a lower `diskGiB` limit. Non-primary data is
  write-blocked first when the disk is low (507). A tile's checkpoints, view
  repository, materialized trees and build artifacts count against a 10 GiB
  per-tile quota: an alert from 90%; when full, new captures and
  materializations answer 507, and running deployments keep serving.
- **Backups** of one deployment's data are the four routes above, beside
  `POST /backup`, whose body is unchanged. A deployment archive is refused
  by older xbinds.

**The edge policy.** Every outbound edge of the tile — an interface binding
(`slot:<interface>`, the net slot included) or a grant (`grant:<target>`:
a call, resource, code, `gpu:` or `cap:` grant) — has a policy for the
tile's non-primary deployments; the primary uses every edge as before.
- `read`, the default where the role can be read-clamped: the call reaches
  the provider's primary with `X-XBin-Role` clamped to `reader` (a bus
  `publisher` to `subscriber`). Providers treat `reader` as read-only.
- `inherit`, for the net slot and capability grants: the tile's own
  authority, never host networking or a provider splice. The net slot
  inherits the tile's relay policy; a tile whose network shares the host's
  gives its non-primary deployments no egress, whatever is stored (`effective`
  `block`, with `why`). `gpu:` grants default to `block`, other capability
  grants to `inherit`.
- `block`: the call is refused, naming the deployment, the edge, its target
  and the policy: `<tile>'s non-primary deployment "<d>" may not use edge
  <id>: the tile's edge policy for it is "block". A tile manager can change
  it in the Deployments panel.`
- Some edges take `block` alone, with no override: a custom role whose
  `implies` don't reach `reader`, stream and lan-ingress bindings, a net slot
  bound to a provider tile, and a workspace-level filesystem or sqlite
  resource on a workspace-scope tile. Their refusal says so (`… can't use
  edge <id>: <why>, so non-primary deployments are blocked from it …`). A
  provider makes a custom role clampable by declaring `implies: {"<role>":
  ["reader"]}`. Two consequences: llm-gw guards completions with `writer`,
  so an LLM-using tile's non-primary deployments can list models but not run
  a turn; and the agent template reaches its sandbox managers through the
  custom role `consumer`, so its non-primary deployments are blocked from
  them.
- A stored value this xbind doesn't know, or one no longer valid for the
  edge, reads as `block`, and when several edges authorize one call any
  `block` among them refuses it. `Edge.refused` and `Edge.clamped` count
  since xbind started.
- Governance grants are no edge: approving `xbin` or an `xbin:*` target for
  a tile that has non-primary deployments answers 409, and a tile holding one
  can't have them.

**Runtime.**
- A backend of a non-primary deployment needs `--isolate`, as a pinned one
  does: without it, adding one answers 409, and a start fails with `<tile>:
  deployment <name> runs only in a sandbox (--isolate), and this xbind runs
  backends without one`.
- A workspace runs at most 3 non-primary deployments per tile, 24 per
  workspace and 12 non-primary backends at once. A start past them is
  refused, and the refusal shows in the deployment's build status and in
  `/sandboxes` failures. At most max(1, NumCPU/4) builds for non-primary
  deployments run at once across the workspace; a primary's build never
  waits.
- Host networking and net-provider splices serve a tile's primary only: a
  non-primary deployment of such a tile starts with no egress, and its
  backend log says why. It gets no stream interface dial (each fails: with
  relay egress its relay refuses the dial and writes a line to its backend
  log; without egress its sandbox has no route to the interface's
  address), no ingress, no lan-ingress legs and no provider roster.
- Its VM guest is charged to the tile, admitted only while the primary's next
  guest still fits the workspace's VM room, and stopped to admit the
  primary's.
- A tile keeps one cgroup leaf, `comp-<key>`, while it runs only `main`;
  once it runs another deployment its generations move under a per-tile
  parent, `tile-<key>/d-<name>/backend`, whose tile node weighs one flat
  leaf and whose deployments' leaves carry their own memory and pids caps.
  Limit alerts name the deployment whose leaf hit its cap.
- It is kept up only while its own code declares `alwaysOn` and its alwaysOn
  switch is on; otherwise it starts on its first request and is reaped when
  idle, like any lazy backend.
- Its backend log, setup output included, is
  `.xbin/deploy/<tile-key>/d/<name>/backend.log`, read with `GET
  /logs?deployment=`; `main`'s stays `.xbin/log/<key>.log`.

**The operations** beyond what the rows say:
- **`live-reload/pause`** pins the live reload target to a fresh checkpoint of
  the work tree at request time; a backend's swap onto it follows as a
  queued deploy (`how: "pause"`), normally of identical code; for a static
  tile the commit is the whole deploy. A failed build or start leaves live
  reload paused and the deployment pinned to the attempted checkpoint (its
  state `failed`): every restart runs it, and Reload now retries it. Pinning a
  `go`, `node` or `python` tile without `--isolate` answers 409 (kind
  `policy`); static tiles pause everywhere; resuming never needs isolation.
- **`live-reload/now`** answers `unchanged: true` when the work tree equals
  the deployment's checkpoint and nothing is queued; `expect` must be a
  prefix of the fresh capture, else 409 `the code changed since you reviewed
  <expect> (now <actual>)`.
- **`live-reload/resume`** onto `main` with nothing else set removes the
  record, the deploy journal and the view repository (`record: false`); the
  checkpoint store and its deploy log stay, unread, until the next opt-in,
  whose deploy ids keep counting.
- **`deploy`** without `deployment` names the primary; `checkpoint` and
  `expect` together are 400; `restart: true` starts a new generation of the
  current code, clears the crash breaker and moves nothing (with
  `checkpoint` or `expect`: 400 `restart runs <name>'s current code: send no
  checkpoint or expect with it`). A checkpoint the deployment already runs
  answers `unchanged: true` while its backend is healthy or being built; when
  the backend is crash-looping, failed or not running, or its last move
  failed, the deploy starts a new generation from the kept build (`how:
  "restart"`) and clears the crash breaker. The same holds for `rollback`
  naming the running checkpoint; `live-reload/now` of an identical work tree
  always answers `unchanged: true`.
- **`rollback`** without `checkpoint` takes the newest `ok` entry whose
  checkpoint differs from the current one, else 409 `<name> has no earlier
  checkpoint in its deploy log`.
- **Queueing.** One deploy in flight per deployment and up to eight waiting;
  a request whose checkpoint equals the tail of the queue is merged into it
  (the answer carries that entry); a ninth answers 409 `<name> already has 8
  deploys waiting; try again when one finishes`. Removing a deployment or
  disabling the tile ends its queued and running deploys `cancelled`.
- **The journal.** Every accepted attempt is journaled until its deploy-log
  entry is written; after a crash, the next boot logs it: queued as
  `cancelled`, swapped as `ok`, running as `failed` with `interrupted: xbind
  restarted mid-deploy; <name> runs <code>`.
- **Checkpoints** are refused past their caps (409, `checkpoint of <tile>
  refused: …`, naming the largest paths) and rate-limited per tile (429,
  `<tile> was checkpointed too often; retry in <n>s`). `by` is `user:<id>` or
  `owner`; `via` is the acting credential's kind. After each successful
  deploy xbind collects the tile's checkpoint store, keeping every
  deployment's current checkpoint, those of each deployment's last 20
  successful deploys and anything younger than 24 hours; `purge` removes one
  at once. Checkpoints capture gitignored files such as `.env`: purge one
  that caught a secret, and keep secrets in the vault.
- **Assigned branches** (`branches/1`, D131). A deployment other than `main`
  and the primary may require a branch of the tile's own repository: a
  requirement and a label, not a feed. The work tree feeds it only while
  it has that branch checked out, read from `.git/HEAD` beneath the tile
  (no git runs); a detached HEAD, no repository or one xbind can't read is
  no branch.
  - **The ops that feed it from the work tree** — `live-reload/attach` and
    `live-reload/resume` onto it, `live-reload/now` while it is where live
    reload last was, a `deploy` of the work tree onto it, and `add` from the
    work tree (with `attach` or not) — answer 409 `<name> is assigned branch
    <b>, and the work tree is on <w>: check out <b>, or send
    confirm:"other-branch" to use <w> this time`, dry runs included, whose
    `impact.branch` is `{deployment, assigned, workTree, other?}`.
    `confirm: "other-branch"` takes the work tree's branch this time; attach
    and resume keep it as the deployment's `branchOverride` (which the other
    ops on it take too), lapsing when live reload moves or the work tree's
    branch changes again. A work
    tree on no branch can't be followed even so (409 `… isn't on a branch
    …`). Their capture reads the branch again: one taken while a checkout
    moved HEAD answers 409 `the work tree's branch moved while xbind
    captured it for <name> (<b>, now <w>): a checkout raced this request,
    and nothing shipped; …`. A deploy of a checkpoint, promote and roll back
    aren't fed by the work tree and aren't asked.
  - **Saves.** While live reload follows it, a save reaches it only once
    xbind found the work tree on its branch (or its override) and captured
    it, with the branch read again at the capture. A save on another branch
    — or one a checkout raced — deploys nothing: live reload pauses, the
    deployment pinned to the code it runs (the last save deployed on its
    branch; its deploy log entry is a `pause` by `xbind`), and op `branch`
    tells the write audience (protocol/events.md). A pause on another branch pins
    it the same way, never to the other branch's work tree. Tiles without
    an assigned branch save as before.
  - Checkpoints taken from the work tree carry an `Xbin-Work-Tree-Branch`
    trailer (in the git view too). A deploy entry's `branch` is the branch
    its own capture was taken on (kept in the deploy log as an
    `Xbin-Branch` trailer, which an older xbind leaves unread), else the
    branch its checkpoint was first captured on.
  - **newBranch** runs a confined `git switch --create=<b> --end-of-options`
    in the tile: it creates and checks out a new branch at the work tree's
    HEAD and changes no file, so nothing reloads. It never switches to an
    existing branch; an add refused after it keeps the branch.
  - Reassigning the primary to a deployment clears its branch. An older
    xbind keeps `branch` and `branchOverride` in the record as it found
    them, and ignores them.
- **A protected primary.** Only tile managers change its code, each naming
  the checkpoint they reviewed; the server commits only while `seq` is
  unchanged and re-checks the actor's authority at commit. Terminal and
  agent sessions never target it, and its vault is written only by its own
  backend and by tile managers in their own session.

**A deploy of a checkpoint** reports on the `deployments` event, never
`build-*`: a failed one paints no overlay, the previous generation keeps
serving, and the compiler output goes to the deployment's log under `---
deploy of c:<id> failed <time> ---`. After a swap that changed the code a
deployment serves (reload now, deploy, promote, roll back, resume, attach),
the primary gets one bare `reload` and a non-primary deployment op `reload`;
pausing and restarts reload nothing. A crash-looping pinned backend says
`deploy a fixed checkpoint or restart it`: a save doesn't reach it. A pinned
backend needs isolation: on an xbind restarted without
`--isolate` it is held (its `/api/` answers `pinning a backend to a
checkpoint needs isolation (--isolate)`, the pages keep serving the
checkpoint; a restart that goes through a build fails it with a longer text
that also names `--isolate`), and it never runs the work tree instead.

**Texts** beyond the refusals the routes above name:

| Status | `error` |
|---|---|
| 400 | `need ?tile= (a tile's path: apps/crm; name a deployment with deployment=)` |
| 400 | `a deployment is named with deployment=, not tile+name (a '+' in a query string reads as a space)` |
| 400 | `the tile ref names deployment "<a>" and the body "<b>": send one` |
| 400 | `deployment names are lowercase letters, digits and "-", start with a letter, at most 24 characters` |
| 400 | `<consequence>: send confirm:"<token>" to proceed` |
| 400 | `the primary of <tile> is protected: name the checkpoint you reviewed (send <checkpoint\|expect> and seq)` |
| 400 | `id and before are deploy ids; wait, seconds (at most 25); limit, 1 to 200` |
| 400 | `bad spec "<spec>": c:<id> \| deployment:<name> \| work-tree` |
| 400 | `path is one clean tile-relative file; stat is 1 or absent` |
| 403 | `deployments of <tile> need read access` (the state, outside the reader audience) |
| 403 | `deployments of <tile> need write access` (the log, the diff and the checkpoint remote, outside the write audience) |
| 403 | `deployment URLs need write access on <tile>` (the state naming a non-primary deployment with `deployment=`, outside its audience, whether or not it exists) |
| 403 | `a tile's own credentials act only on their own deployment (<bound>)` (the log, from a non-primary deployment's credentials naming another) |
| 403 | `<act> needs terminal-level access on <tile>` (`<act>`: "pausing live reload", "deploying", …); for any tile credential but the tile's own terminal and agent sessions (a frame, a backend, another tile's) it adds ` — only people and the tile's own terminal and agent sessions operate its deployments` |
| 403 | `<act> is a tile manager's act: the tile's owner, its org's admins, or a workspace admin` |
| 403 | `<act> is a tile manager's act, done in a person's own session: terminal, agent and tile credentials can't do it` |
| 403 | `<act> is a workspace admin's act, done in a person's own session` |
| 403 | `the primary of <tile> (<name>) is protected: only tile managers change its code, and not from a terminal or agent session` |
| 403 | `<scope>'s "<name>" data was <seeded\|restored> by <by> <when>: joining it is a tile manager's act` |
| 404 | `no such tile: <tile>` · `<tile> has no deploy <id>` · `<tile> has no deployment "<name>"` · `<tile> has no checkpoint <id>` |
| 409 | `<tile> has no deployments yet: pause live reload or add a deployment first` — a tile without a record, for any operation but an opt-in and purge, and for the log; the checkpoint remote answers it as a 404 |
| 409 | `<tile> has no deployments: its work tree is what runs, so there is nothing to diff` |
| 409 | `the deployments of <tile> changed (seq <n>); reload and retry` |
| 409 | `live reload is attached to <name>; <act> works while it is paused` |
| 409 | `<name> is assigned branch <b>, and the work tree is on <w>: check out <b>, or send confirm:"other-branch" to use <w> this time` (branches/1) |
| 409 | `the work tree's branch moved while xbind captured it for <name> (<b>, now <w>): a checkout raced this request, and nothing shipped; check the branch and retry` |
| 409 | `checkpoint id <id> is ambiguous in <tile>; use more digits` |
| 409 | `pinning a backend to a checkpoint needs isolation (--isolate)` |
| 409 | `<tile>'s deployment record was written by a newer xbind (schema <n>)` — the tile is held: its backends don't start, `/c/` and `/api/` answer 503 with this text, and every write here answers 409 |
| 409 | `<act> is turned off on this xbind (--tile-deployments=off): it would create or extend deployment state; resuming live reload onto main, removing a deployment, resetting its data and unprotecting still work` |
| 429 | `<tile> already has a diff running and one waiting; retry shortly` |
| 504 | `the diff of <tile> took longer than 30 s (narrow it with path or stat)` |

A view-as session's POSTs are refused 403 read-only by the usual
middleware.

**The deploy log** holds every finished attempt: one confined commit per
attempt on the deployment's chain in the checkpoint store. An attempt that
made no checkpoint names the empty tree, and so does an entry whose
checkpoint was purged; `error` keeps the failure's first line, at most 500
bytes (the whole text is in the deployment's log). Reading it needs write on
the tile; a non-primary deployment's own credentials read its entries only.

**The diff** runs confined (one per tile at a time with one waiting, two
across xbind). `path` is literal, not a pattern. A `work-tree` side is a
capture and spends the capture rate; a deployment that follows the work tree
means `work-tree`. The `stat=1` answer carries the `X-XBin-Checkpoint-From`
and `-To` headers too. `workTree.changed` and the diff count added, changed
and removed files, never a nested component's.

**The checkpoint remote** (`GET /checkpoints/<tile>.git/<path>`) serves the
tile's view repository by dumb HTTP: `HEAD`, `info/refs`,
`objects/info/packs`, `objects/pack/pack-<hex>.pack|.idx` and loose objects,
with git's content types; GET and HEAD only; everything else 404, and
`?service=` is ignored, so git uses the dumb protocol. `deploy/<name>`
exists only while `<name>` is pinned, and `HEAD` names `deploy/<primary>` —
dangling while the primary follows the work tree. View commits are
parentless and carry `Xbin-Checkpoint` and `Xbin-Work-Tree-Head` trailers
(the latter the base for `git rebase --onto`). A fetch racing a refresh can
fail once and succeeds on retry. Terminal and agent sessions opened while
the tile has a record carry it as the `xbin-deploy` remote, in two more
`GIT_CONFIG_*` pairs (`GIT_CONFIG_COUNT` 4); sessions without a record keep
the two they always had.
