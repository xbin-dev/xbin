# Protocol: status, runtime and logs

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page: what is running and how it is doing — status, backends, the
runtime view, GPUs, Go build versions, VM sandboxes, the sandbox inventory,
a tile's metrics and logs, and workspace alerts.

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it.

## Status, runtime and logs


```
GET    /status                     admin. terminals ({id,cwd,net,kind,vm,user,
                                   …}), component count, host
                                   {cpuBusy,cpuTotal,memTotal,memAvail,
                                   diskTotal,diskFree}, traffic
                                   {reqs,bytesOut,uptimeSec} (cumulative —
                                   delta two polls for rates), and version (the
                                   running xbind build commit). A terminal
                                   whose session targets a named deployment
                                   carries deployment; a person's session
                                   on a partitioned tile carries partition
                                   ("user:<id>", or "global" when it
                                   targets a non-primary deployment) and
                                   an empty name
GET    /backends                   admin. per-component backend state
                                   {<path>: {state, gen, error?}}. The row
                                   stays the primary's; a tile with
                                   deployments adds deployment (the primary's
                                   name) and, for admins in a person's own
                                   session, deployments: {<name>: {state, gen,
                                   error?}} of its other deployments that
                                   run. A tile running only another
                                   deployment shows the primary's idle row
                                   {state: idle, gen: 0}. Other tiles'
                                   credentials an xbin grant makes admin get
                                   the primary rows only. A partitioned
                                   tile's row adds partitions: [{partition,
                                   state, gen, uptimeSec, rssKb, restarts,
                                   crashLoop?, lastExit?, lastStarted?,
                                   errorClass?}] — its people's running
                                   instances, metadata only (errorClass:
                                   crash-loop | build | start | exit |
                                   other, never the text); a tile whose
                                   global instance isn't running gets the
                                   idle row to hold them
GET    /runtime                    admin. full runtime visibility →
                                   {host:{version,kernel,pid,uid,numCPU,goroutines,
                                   heapMB,uptimeSec,isolate,rootfs,scopeUids,
                                   protections:{seccomp,landlock,landlockAbi}},
                                   backends:[{path,runtime,state,isolated,
                                   sandbox,vm?:{memMiB,vcpus,emulated?},
                                   checkpoint?,pid,gen,
                                   uptimeSec,restarts,activeConns,rssKb,threads,fds,
                                   cpuSec,namespaces:{<ns>:{id,isolated}},egress:[…],
                                   netRef?,net?,netSource?,netNote?,
                                   cgroup:{memCurrent,memMax,cpuUsec,pidsCurrent},
                                   activity:{allowed,denied,active,txBytes,rxBytes,
                                   recent:[{proto,dst,port,allowed,txBytes,rxBytes,
                                   start,end}]}}], resources:[{id,type,size,detail}],
                                   stats:{cgroup,intervalSec,tiles:[{path,owner,
                                   cur:{t,cpu,mem,rbps,wbps,riops,wiops,pids},
                                   series:[point…]}]}} — stats is the live
                                   per-tile sampler (~2s cadence, ~3 min of
                                   points; demand-driven — sampling runs only
                                   while /runtime is being polled). cpu is % of
                                   one core; rbps/wbps + riops/wiops are
                                   syscall-level I/O rates (includes FUSE-backed
                                   resource I/O); cgroup=true means exact
                                   whole-tree accounting via cgroup v2
                                   delegation, false = /proc-tree sampling
                                   {state: idle|building|healthy|failed, gen, error?}.
                                   netRef = the bound/defaulted net ref (`org`,
                                   `internet`, `lan:…`, a provider), net = the
                                   effective mode host|relay|splice|none,
                                   netSource = "org:<id> (<sets>)" for org
                                   reach, netNote = why a stored binding is
                                   inert (D54). sandbox = how the backend is
                                   isolated, vm | namespace | host (a running
                                   generation's, else how it would start;
                                   D112); vm = a VM generation's guest size
                                   and whether it is emulated. For a VM
                                   backend pid, namespaces, rssKb, threads and
                                   fds describe its host-side jail (the shim,
                                   whose child VMM holds guest memory) — read
                                   cgroup for what the VM uses. checkpoint:
                                   the full tree id of the checkpoint the
                                   running generation runs (live reload
                                   paused); absent while it runs the work
                                   tree. backends[] keeps one row per tile,
                                   the primary's, which gains deployment on a
                                   tile with deployments; the others' rows (the
                                   same shape plus deployment) are listed in a
                                   top-level deploymentBackends[], to people
                                   only, absent while none runs. resources[]
                                   rows gain deployment on a scope with a
                                   deployment record or deployments beyond
                                   main ("main" for main's), and a scope's
                                   data beyond main has rows of its own (kv,
                                   blob, sqlite, filesystem), one per
                                   deployment's data. A partitioned scope's
                                   people's partitions have rows of their own
                                   too, one per partition and resource that
                                   isn't shared, with deployment and
                                   partition ("user:<id>"; the partition id
                                   when its record can't be read)
GET    /gpus                       admin. host NVIDIA GPUs for gpu:* grants and
                                   the terminal picker → {gpus:[{index,uuid,
                                   name,node}]}
GET    /go-build-versions          admin. the D166 upgrade check's latest
                                   result → {since?, done, fresh?, running,
                                   checkedAt?, workspaceError?, tiles:[{tile,
                                   require:["<module> <version>"], minimal,
                                   changes:[{module, had?, now?}], dismissed,
                                   checkedAt}], errors:[{tile, error}]}.
                                   tiles: each Go tile whose own build (its
                                   go.mod's versions) links a module lower
                                   than the workspace's shared go.work did,
                                   with the fewest require lines that keep
                                   what it had (minimal:false = the raw
                                   differing lines: the search gave up);
                                   a change without now is no longer
                                   linked, one without had newly linked.
                                   errors: tiles it couldn't compare (their
                                   build fails one way or the other).
                                   workspaceError: the shared go.work itself
                                   doesn't load (`go list -m` refuses it),
                                   said once for every tile it left
                                   uncompared. since: the xbind version the
                                   change came with; done: the first pass
                                   completed; fresh: no Go build of an
                                   earlier xbind was found, nothing
                                   compared. It runs once on its own, in the
                                   background after the first boot of an
                                   xbind with it on a workspace an earlier
                                   xbind built Go tiles in (a work-tree
                                   binary, or a checkpoint artifact whose
                                   build.json records the shared go.work;
                                   marker: data/go-build-versions.json),
                                   over the Go tiles the workspace has then,
                                   keeping what each linked under the shared
                                   go.work (its baseline). A tile it names is
                                   listed again after a build of it (work
                                   tree or checkpoint) once its go.mod, or
                                   another go.mod or the go.work its build
                                   uses, changed — and leaves once its
                                   go.mod caught up. Only tiles the
                                   workspace still has as Go tiles are
                                   listed. `bx doctor` renders it
POST   /go-build-versions/check    admin. compare the check's tiles again, in
                                   the background, a few at a time → 202
                                   {running:true, started} (started false: a
                                   pass is already running; GET shows
                                   running until it is done). Each tile is
                                   compared with its baseline — only the
                                   modules it links both ways count — and
                                   the shared go.work is listed only for a
                                   tile that has none yet; a tile added
                                   since the upgrade is not compared (it
                                   never built with the shared go.work).
                                   200 {running:false, started:false,
                                   reason} when no tile has anything to
                                   compare (a fresh workspace); 503 while
                                   xbind stops
POST   /go-build-versions/dismiss  admin. body {tile?} (none: every tile the
                                   alert names) → the GET report. Hides the
                                   alert's line for the tile until its lines
                                   change; 404 a tile the check doesn't list
GET    /vm                         authenticated. VM sandboxes (D89)
                                   → {status:{available,reason,emulated?,
                                   note?}, policy:
                                   {terminals,backends,memMiB,vcpus,maxVMs,
                                   budgetMiB,diskGiB,tiles,tilesBudgetMiB,
                                   tilesEmulated}, used?:{vms,memMiB},
                                   usedTiles?:{vms,memMiB}} (used,
                                   usedTiles: admins; usedTiles is the part
                                   of used that tile sandboxes hold).
                                   available=false names why: no /dev/kvm, the
                                   xbind user not in the kvm group, a missing
                                   asset (firecracker, vmlinux, xbin-vmagent,
                                   mkfs.erofs, a static bx), no --isolate.
                                   emulated=true: no usable KVM, so VMs run
                                   under QEMU's emulation, much slower (D90);
                                   note says why
PUT    /vm/policy                  admin. body: any of {terminals,backends,
                                   memMiB,vcpus,maxVMs,budgetMiB,diskGiB,
                                   tiles,tilesBudgetMiB,tilesEmulated},
                                   merged onto the stored policy (a field the
                                   body leaves out keeps its value) →
                                   {status, policy, stored}.
                                   Off by default (the installer writes
                                   terminals on, backends and tiles on with
                                   KVM, for a workspace with no policy: D110,
                                   D120); zero
                                   sizes = defaults (2048 MiB, 2 vCPUs, 8
                                   VMs, budget maxVMs×memMiB,
                                   a 20 GiB VM terminal disk — grown, never
                                   shrunk). Turning backends off stops new VM
                                   generations; running ones keep going.
                                   tiles: the sandboxes manager tiles run
                                   (cap:sandboxes) may use VM mode; their VMs
                                   also count against tilesBudgetMiB (0 =
                                   half the budget; at most budgetMiB);
                                   tilesEmulated also allows it where VMs run
                                   emulated. Turning tiles off stops the
                                   running tile VM sandboxes, and
                                   tilesEmulated off those running emulated
                                   (state kept; a start answers 503 with the
                                   reason until it is back on). 400 on
                                   unknown fields or out-of-range sizes, 409
                                   without --isolate
GET    /sandboxes?tile=            admin. every sandbox xbind runs (D112) →
                                   {sandboxes:[{id,kind (backend|terminal|
                                   agent|tile),tile,parent?,user?,label?,
                                   for?,forUser?,partition? (a person's
                                   partition's backend: user:<id>),mode
                                   (vm|namespace|host),accel? (kvm|emulate),
                                   memMiB?,vcpus?,pid,gen?,started,leaf?,
                                   disk?,net?,restricted?,owner?,name?,
                                   status?,personal? (a person's terminal
                                   or agent session on a partitioned tile,
                                   listed without its name),uptimeSec,
                                   stats?:{cpu,mem,pids,
                                   scope}}], disks:[{kind (terminal|tile|
                                   person-terminal),
                                   key,sandbox?,sandboxUid?,path,tile?,
                                   apparentBytes,allocatedBytes,inUse}],
                                   failures:[{time,kind,tile,partition?,user?,mode,stage
                                   (refused|start|health|exit),error,count}],
                                   failureCounts:{<stage>:n}, cgroup,
                                   intervalSec, health:{isolation:{tier,
                                   isolate,rootfs,scopeUids,protections,
                                   cgroup,uidRange?,uidRangeNote?},vm:{
                                   available,reason?,emulated?,note?,accel?,
                                   forced?,assets:{piece:path},missing?:[…],
                                   kvm?,emulation?,policy,stored,used,
                                   usedTiles,usedBy:{<tile>:{vms,memMiB}}},
                                   tileSandboxes:{cgroup,flows:{used,cap},
                                   total:{memMiB:{used,cap},pids:{used,cap}},
                                   policyError,lowDisk,trash:{entries,
                                   bytes}}}}.
                                   A backend
                                   is listed per generation (blue/green shows
                                   two; stats scope "tile" is the tile's
                                   shared leaf — count it once), a session
                                   by its id; stats are the live sampler's
                                   (demand-driven, like /runtime). stored is
                                   the VM policy as set (0 = default) — what
                                   an editor PUTs back. disks: the VM disks
                                   on the host — a tile's terminal layer's
                                   (kind terminal), its tile sandboxes'
                                   (kind tile, with the sandbox's name and
                                   uid) and, on a partitioned tile, each
                                   person's terminal layer's (kind
                                   person-terminal: key is the tile's
                                   storage key, the person unnamed). A
                                   person's terminal or agent session on a
                                   partitioned tile is listed without its
                                   name. A running tile sandbox (D120) is a
                                   kind tile row: its name, its manager's
                                   claims for/forUser, stats from its own
                                   leaf. health.tileSandboxes: why tile
                                   sandboxes run without cgroup limits
                                   although xbind's cgroup is delegated
                                   (cgroup; "" = they have them, or nothing
                                   does), their relays' shared cap on
                                   concurrent connections (flows), the
                                   memory the running ones may take of the
                                   policy's total (total.memMiB, MiB; cap 0 =
                                   none) and the processes they hold of it
                                   (total.pids; used -1 = unknown, no
                                   cgroup), why the sandboxes policy file
                                   can't be read (policyError; "" = it can —
                                   tile sandboxes are off while it can't),
                                   whether starts are held for a low disk
                                   (lowDisk), and the state deleted or reset
                                   that waits for its confined removal
                                   (trash: entries, bytes as last measured).
                                   failures: the newest 64, identical ones
                                   within 10 min coalesced (count). ?tile=
                                   narrows.
                                   Deployments follow the registry's name
                                   rule: main's backend rows are unchanged
                                   (id backend:<key>:g<gen>, no deployment,
                                   stats scope "tile"); another deployment's
                                   have the id backend+<name>:<key>:g<gen>,
                                   deployment, tile (the tile's path) and
                                   stats scope "deployment" (its cgroup leaf,
                                   shared by its generations). While a tile
                                   runs a deployment beyond main, main's
                                   "tile" stats are its own leaf's, so a
                                   tile's rows sum per leaf. leaf is the
                                   generation's own: the flat <key> while main
                                   runs alone, tile-<key>/d-<name>/backend
                                   otherwise. A person's partition's backend
                                   (docs/partitions.md) has the id
                                   backend@<pkey>:<key>:g<gen>, partition
                                   (user:<id>), its own leaf
                                   tile-<key>/p-<hash>/backend and stats
                                   scope "partition"; metadata only.
                                   failures[] of another deployment
                                   carry deployment? (a start past the caps
                                   or the VM room of protocol/deployments.md shows
                                   here, stage refused).
                                   &deployment=<name> narrows rows and
                                   failures (main: the rows without a
                                   deployment) and is echoed as a top-level
                                   deployment; a malformed name → 400.
                                   health.vm.usedBy stays keyed by tile.
                                   tileSandboxes:[{tile,name,uid,state,
                                   stateDetail?,mode,accel?,memMiB,vcpus,
                                   diskGiB,diskBytes,for?,forUser?,
                                   lastActive?,tileExists,agentPorts?,
                                   ports?}] — every tile
                                   sandbox definition (D120), stopped ones
                                   and those of removed tiles too (an admin
                                   stops or deletes one with ?tile=).
                                   agentPorts: whether the running one's
                                   in-box agent serves ports (serves |
                                   predates — restart it), as a port
                                   request found; ports: its latest 8 port
                                   requests (D135), newest last, [{at,port,
                                   status?,refusal?,from}] (from: the calling
                                   manager tile); both absent until one. A manager
                                   tile's backend gets its own tile sandboxes
                                   instead (protocol/sandboxes.md)
GET    /tile-status?component=<p>  self or admin. one tile's runtime metrics —
                                   backend {state,gen,sandbox,vm?,cpuSec,cgroup:{mem,pids},
                                   rssKb,fds,activeConns,egress}, disk {usage,
                                   quota,blocked}, alerts[], net {netRef, net,
                                   netRules, netSource, netNote} (the effective
                                   network, D54). Readable from that tile's
                                   terminal (tile-scoped token). `bx status`
                                   renders it.
                                   &deployment=<name>: the deployment reported
                                   (default: the caller's bound deployment —
                                   a tile credential's — else the primary).
                                   400 a malformed name, and a component=
                                   that names a deployment as tile+name
                                   (D127j: "a deployment is named with
                                   deployment=, …"); 403 "a tile's own
                                   credentials act only on their own
                                   deployment (<bound>)" (a tile's frames and
                                   backends read their own only); 403
                                   "deployments of <tile> need write access"
                                   for a non-primary deployment and a caller
                                   outside its audience, whether or not it
                                   exists (a frame bound beyond the primary
                                   whose user no longer writes the tile
                                   included); 403 for a session that follows
                                   a protected primary; 404 unknown. The
                                   answer gains deployment on a tile with
                                   deployments or when one was named (the
                                   echo), and for admins and the tile's
                                   terminal/agent tokens whose user writes it
                                   deployments: {primary, liveReload,
                                   items:[{name, state, gen, checkpoint?}]}
                                   (checkpoint: the pinned id). A person's
                                   partition's own credential (its backend,
                                   the person's frames, terminals, agent
                                   sessions) gets its partition's: backend
                                   (its instance, or null), disk (the
                                   partition's bytes and ceiling) and
                                   partition: "user:<id>" — never the
                                   global instance's
GET    /term-net?tile=<p>          terminal access on the tile. the network
                                   scopes a terminal there may take for the
                                   caller (D54): {tile, scopes:[{id,label,
                                   desc}], default, label, org, personal} —
                                   `org` where the owning org has network
                                   sets, `personal` where a personal tile's
                                   owner has network sets (D88 — added to
                                   internet, not replacing it), `set:<name>`
                                   for each set the caller may pick (the
                                   owner's sets; every set for a workspace
                                   admin, D65), internet/host where
                                   allowed, offline always. The
                                   picker offers exactly this list; default
                                   is never a set
GET    /logs?component=<p>         admin, the tile itself, or a user with
                                   TERMINAL-level access on it (read/write
                                   users don't — output can carry secrets).
                                   text/plain tail of the backend's captured
                                   stdout/stderr (.xbin/log/<key>.log, all
                                   generations). ?tail=<bytes> (default 64K,
                                   max 1M) sizes it; ?follow=1 keeps streaming
                                   appended bytes (chunked) until the client
                                   goes away (sent with X-Content-Type-Options:
                                   nosniff, so a client doesn't hold back the
                                   first bytes to sniff them). The HTTP twin of `bx logs [-f]`
                                   (which streams it in an isolated terminal,
                                   where .xbin is masked); the terminal
                                   window's read-only logs dock. A failed
                                   deploy of a checkpoint appends
                                   "--- deploy of c:<id> failed <time> ---"
                                   and the compiler output.
                                   &deployment=<name>: one deployment's log
                                   (default: the caller's bound deployment —
                                   a frame's or backend's, a session's named
                                   target — else the primary). A tile's
                                   frames and backend read only their own
                                   (403 "a tile's own credentials act only on
                                   their own deployment (<bound>)"); the
                                   tile's terminal/agent tokens, people with
                                   terminal level and admins read any. 404
                                   unknown, 400 malformed, and 400 for a
                                   component= that names a deployment as
                                   tile+name (D127j). X-XBin-Deployment:
                                   <name> on a non-primary answer and on every
                                   answer to a request that named a
                                   deployment (the echo of a text/plain
                                   answer). A deployment beyond main keeps
                                   its log inside xbind's state
                                   (.xbin/deploy/<tile-key>/d/<name>/
                                   backend.log), with its setup output.
                                   A partitioned tile (docs/partitions.md
                                   §Logs and status): the caller's own
                                   partition's log at any level — a
                                   person's session, their frames,
                                   terminals and agent sessions, the
                                   partition's backend
                                   (.xbin/partition/<tile-key>/<dep>/<pkey>/
                                   backend.log); &user=<id>: that person's,
                                   for them, or for an admin or a tile
                                   manager in their own session while the
                                   person shares it (POST
                                   /partitions/share-log; else 403);
                                   &xbin-partition=global: the global
                                   instance's under the rule above (a
                                   partition's 403 but a writer's, D218); a credential
                                   acting in no person's partition (the
                                   root token, another tile) reads the
                                   global instance's as before;
                                   &partition= 400. X-XBin-Partition names
                                   the partition served. A follow of a
                                   partition's log asks again every few
                                   seconds and ends (with a closing line)
                                   once the reader may no longer read it —
                                   a share that ended or was revoked
```

## Alerts


```
GET    /alerts                    any. workspace health {alerts:[{level,kind,
                                   tile?,message,system,dismiss?}]} — disk
                                   quota / low disk / cgroup at-limit; system
                                   alerts to all, tile alerts to admins +
                                   that tile's users. A workspace setting
                                   xbind can't read is kind
                                   `workspace-settings`, admins only: crit
                                   while a partitioned tiles' switch can't
                                   be read, warn for base auto-update alone
                                   (`policies` before D180). Admins also get
                                   kind go-build-versions (warn): Go tiles
                                   that build with older dependency versions
                                   since each builds with its own go.mod
                                   (D166), and the require lines that keep
                                   what each had (GET /go-build-versions).
                                   dismiss: the route a POST to which
                                   dismisses the alert
                                   (/go-build-versions/dismiss).
                                   An alert about a deployment's data beyond
                                   the primary's main carries deployment (the
                                   data's deployment; tile is then the data's
                                   scope, or absent for main while main isn't
                                   the primary): admins only. A limit alert
                                   names the deployment whose cgroup leaf hit
                                   its cap; a tile's checkpoint and build
                                   storage warns at 90% of its quota. Kind
                                   backup-keys (admins only, warn): N backup
                                   keys aren't in any exported key bundle
                                   yet, or the vault passphrase changed
                                   after the last export (GET /backup-keys;
                                   cleared by POST /backup-keys/export).
                                   Kind partition-switch (warn, tile): a
                                   partition mode switch is requested for
                                   the tile, which doesn't run until a tile
                                   manager switches or keeps the current
                                   mode (POST /partitions/mode; the message
                                   names bx partition switch|keep and
                                   /xbin/partitions) — admins and
                                   the tile's readers; it goes when the
                                   request is decided or withdrawn. Kind
                                   partition-invalid (warn, tile): the
                                   tile's partition request can't run (or
                                   its mode record can't be read), so its
                                   primary doesn't — same audience. Kind
                                   partition-consents (warn, admins only):
                                   person consent records in
                                   data/partitions/consents this xbind
                                   can't read (each named; POST /partitions/
                                   consents). An alert about a cap carries
                                   action (D203): {action: raise-limit,
                                   tile, scope?, kind, unit, current,
                                   approvers, choices: [{pct, value, fits,
                                   why?, approvable, default?}]} — choices'
                                   approvable decorated for the viewer
                                   (the shell's frame: the person driving
                                   it), dismiss (POST /limits/dismiss…)
                                   for approvers only. Kinds oom (warn,
                                   sticky: OOM-killed at its memory
                                   limit), sbx-cap (tile sandboxes at
                                   their total), quota (a scope at its
                                   disk quota), quota-near (warn, from 90 %
                                   of it, with the action), tile-limits
                                   (crit, admins: data/tile-limits.json
                                   can't be read). Kind memory (warn,
                                   passing): a backend under memory
                                   pressure at its cap that wasn't killed
                                   — before D203 it was oom; pids (warn,
                                   passing): at its pids cap
```
