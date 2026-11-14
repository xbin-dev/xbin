# Protocol: tile sandboxes

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page:
the sandboxes a manager tile runs for the tiles it serves —
definitions, lifecycle, exec, files, snapshots and the TTY and stdio
routes. The contract between a manager and its consumers is
[sandbox-manager.md](/docs/sandbox-manager.md); the admin's inventory of
every sandbox (`GET /sandboxes?tile=`) is in
[runtime.md](/docs/protocol/runtime.md).

## Tile sandboxes (manager tiles)

A **manager tile** runs coding sandboxes for the tiles it serves (the
sandbox-manager contract, [sandbox-manager.md](/docs/sandbox-manager.md)). On
xbin its backend defines and drives them through the routes below (D120),
which mirror the contract, so the manager forwards most calls unchanged.
The Go SDK wraps them: `xbin.SandboxAPI()` ([sdk.md](/docs/sdk.md) §Tile
sandboxes).

- **Who.** Only a *manager call* reaches them: the tile's **backend** — its
  instance token, over the gateway — holding **`cap:sandboxes`**, a grant
  only a workspace admin approves. The key is the caller's own tile: a
  manager names only its own sandboxes. Everyone else gets 403
  `not-allowed`: a frame of the same tile, terminals, cron and signed-in
  people. An **admin** may read and set the policy and list, stop and
  delete a tile's sandboxes (`?tile=`) — and never defines one, runs a
  command in one, reads its files or attaches to it.
- **Deployments.** A manager tile's sandboxes are its `main`
  deployment's. The backend of any other deployment of it
  (protocol/deployments.md) gets 501 `unsupported` on every manager route — never
  `main`'s sandboxes, files or quota — and so does an admin's `?deployment=`
  naming one; a deployment's own sandbox set isn't built yet.
- **Isolation.** Without `--isolate` every manager route but `runtime`
  answers 501 `unsupported` ("tile sandboxes need isolation"), and `runtime`
  says `isolation: false`.
- **In this xbind** the definition routes work — runtime, policy, list,
  create, get, patch, delete — and so do start, stop, reset and rebase, in
  both modes, the commands (`run`, execs and their output, stdin, signals,
  resizes, the TTY WebSocket and the stdio WebSocket), the file, tar and
  copy routes, and snapshots, restores and clones, and the ports proxy;
  `runtime.caps` lists the contract capabilities served (`exec`, `tty`,
  `files`, `tar`, `snapshots`, `clone`, `ports` since D135 and `stdio` —
  an older xbind leaves them out, and their routes are a 404; it ignores
  `split` and `stream`).
- **Ports** (D135). `ANY /sandboxes/<name>/ports/<port>/<path>` proxies
  the request — any method, a WebSocket upgrade included — to a server
  listening on TCP `<port>` (1–65535) on the sandbox's **own loopback**:
  xbind asks the sandbox's agent for a connection, and the agent dials
  `127.0.0.1:<port>` (else `[::1]`, for a server that bound `localhost`)
  inside the sandbox and splices it; each request is a connection of its
  own. It is inbound only: nothing in the sandbox reaches xbind or the
  workspace through it. `<path>` and the query go on as they came (escaped;
  a segment that decodes to `.` or `..` is 400), `Host` is
  `localhost:<port>`. `Authorization`, `Cookie`, `Sbx-User`, `X-XBin-*`,
  `Forwarded` and `X-Forwarded-*` never reach the server, and its
  `Set-Cookie` and `X-XBin-*` never come back. The sandbox must be running:
  the route never starts one (409 `state`: its server would be gone
  anyway); nothing accepting on the port is **502 `not-listening`**; an
  agent from before ports (restart the sandbox) is 501 `unsupported`. A
  request in flight — a WebSocket for as long as it is open — holds off the
  idle stop.

```
GET    /sandboxes/runtime          manager. what this tile may use now → {enabled,
                                   isolation, modes:[{mode,accel?}],
                                   unavailable:[{mode,reason}], users, egress:
                                   [{class,slot?,ref?,reach,rules?,note?}], caps,
                                   limits:{sandboxes,running,memMiB,vcpus,diskGiB,
                                   perSandbox:{memMiB,vcpus,diskGiB,maxMemMiB,
                                   maxVCPUs,maxDiskGiB,pids},idleStopMin,
                                   runTimeoutMaxMs,runOutputMax,execsRunning,
                                   outputRing,stdinMax,fileMax,tarMax,waitMaxSec,
                                   flows:{tcp,udp}},
                                   used:{sandboxes,running,memMiB,vcpus,diskBytes}}
GET    /sandboxes/policy           admin. → {policy (effective), stored (0 = default),
                                   error? (why the policy file can't be read)}
PUT    /sandboxes/policy           admin. a partial policy, merged onto the stored
                                   one → {policy, stored}; 400 invalid when out of range
GET    /sandboxes                  manager. → {sandboxes:[SandboxInfo]} (an admin's
                                   GET /sandboxes is the registry view, protocol/runtime.md)
POST   /sandboxes                  manager. define one → 201 SandboxInfo (200 when
                                   clientId repeats the same request)
GET    /sandboxes/<name>           manager. → SandboxInfo
PATCH  /sandboxes/<name>           manager. change it → SandboxInfo (restartNeeded
                                   when a change waits for the next start)
DELETE /sandboxes/<name>[?tile=]   manager, admin (?tile=). stop it, put its
                                   state aside for a confined removal, forget
                                   it → 204 (the removal goes on after it)

POST   /sandboxes/<name>/start?wait=         manager. → SandboxInfo (running, or
                                             stopped with why in stateDetail)
POST   /sandboxes/<name>/stop?wait=[&tile=]  manager, admin. sync, then kill; state
                                             kept, running execs end killed
                                             → SandboxInfo
POST   /sandboxes/<name>/reset?wait=         manager. stop, wipe the state, pin the
                                             current base image, start again if it
                                             ran → SandboxInfo
POST   /sandboxes/<name>/rebase?wait=        manager. stop, keep the state, pin the
                                             current base image, start again if it
                                             ran → SandboxInfo

POST   /sandboxes/<name>/run                 manager. the contract's run → its result
GET    /sandboxes/<name>/execs               manager. → {execs:[Exec]}
POST   /sandboxes/<name>/execs               manager. → 201 Exec (200 on a clientId repeat)
GET    /sandboxes/<name>/execs/<id>          manager. → Exec
DELETE /sandboxes/<name>/execs/<id>          manager. kill the group, forget it → 204
GET    /sandboxes/<name>/execs/<id>/output?since=&max=&waitMs=&encoding=text|base64&stream=stdout|stderr
                                             manager. → the contract's chunk
POST   /sandboxes/<name>/execs/<id>/stdin?eof=   manager. raw body → 204 (eof=1 closes stdin)
POST   /sandboxes/<name>/execs/<id>/signal   manager. {signal: INT|TERM|KILL|HUP,
                                             group?} → 204
POST   /sandboxes/<name>/execs/<id>/resize   manager. {rows, cols} → 204 (tty)
GET    /sandboxes/<name>/execs/<id>/tty?sessionId=&sandboxId=&forUser=
                                             manager. WebSocket: attach (below)
GET    /sandboxes/<name>/execs/<id>/stdio?since=&errSince=
                                             manager. WebSocket: a non-tty exec's
                                             streams (below)
GET    /sandboxes/<name>/tty?cwd=&cmd=&rows=&cols=&uid=&gid=&forUser=&sessionId=&sandboxId=
                                             manager. WebSocket: start a tty exec
                                             (the login shell unless cmd) and attach

GET    /sandboxes/<name>/files/stat?path=
GET    /sandboxes/<name>/files/content?path=&offset=&length=      → bytes + ETag
PUT    /sandboxes/<name>/files/content?path=&mode=&mkdirs=1&ifMatch=&ifNoneMatch=*
                                             raw body → the stat
GET    /sandboxes/<name>/files/list?path=&limit=
POST   /sandboxes/<name>/files/mkdir         {path, parents} → 204
POST   /sandboxes/<name>/files/remove        {path, recursive} → 204
POST   /sandboxes/<name>/files/move          {from, to, overwrite} → 204
GET    /sandboxes/<name>/tar?path=&exclude=… → application/x-tar
PUT    /sandboxes/<name>/tar?path=&mkdirs=1  tar body → 204
POST   /sandboxes/copy                       {from:{sandbox, path}, to:{sandbox,
                                             path}, overwrite} → 204 (both the caller's)

ANY    /sandboxes/<name>/ports/<port>/<path>?<query>
                                             manager. an HTTP reverse proxy to a
                                             server on the sandbox's loopback,
                                             WebSocket upgrades too (below)

GET    /sandboxes/<name>/snapshots           manager. → {snapshots:[{id, name, created,
                                             bytes, pending?}]}
POST   /sandboxes/<name>/snapshots?wait=     manager. {name, clientId} → 201 the snapshot
                                             (200 on a clientId repeat; 202 {…, pending:
                                             true} when ?wait ran out first)
POST   /sandboxes/<name>/snapshots/<sid>/restore?wait=
                                             manager. → SandboxInfo (execs killed; busy
                                             when ?wait ran out first)
DELETE /sandboxes/<name>/snapshots/<sid>     manager. → 204
```

**Conventions.** Errors are the contract's: `{error, refusal, state?,
etag?, retryAfterMs?}` with its statuses (`invalid` 400, `not-allowed` 403,
`not-found` 404, `state`/`exists` 409, `lost` 410, `precondition` 412,
`too-large` 413, `limit` 429, `unsupported` 501, `unavailable` 503). Bodies
are JSON decoded leniently — unknown fields are ignored, so a newer SDK
works against an older xbind — and capped: 64 KiB for a definition, a PATCH
and the policy (413 `too-large` past it). Times are unix ms, sizes bytes
unless named. Names match `[a-z0-9][a-z0-9-]{0,31}`; `runtime`, `policy`
and `copy` are reserved. Exec ids match `[0-9a-f]{6}-[0-9]{1,12}` and
snapshot ids `s-[0-9]{1,12}`. A path with a `.` or `..` segment, or an
encoded `/`, `.` or `\` (`%2F`, `%2E`, `%5C`) in any segment, and a
`<name>`, `<id>` or `<sid>` that fails its grammar, are 400 `invalid`
before anything is looked up, so an id a manager forwards can never
address another route or sandbox. (xbind's router may answer first: a
plain dot segment with a redirect, an encoded one with 400 `invalid`, and
an encoded `/` that leaves no route for the method with 404 or 405.
None reaches a sandbox.) The
data plane — `run`, starting an exec, stdin, signals, resizes, file
writes, tar uploads and copies — isn't audit-logged;
definitions, lifecycle, snapshots and the policy are.

**A definition** (`POST /sandboxes`; `PATCH` takes the same fields but
`name`, `mode` and `from`, plus `version`):

```
{"name": "sb-7f3a", "mode": "namespace" | "vm",
 "memMiB": 2048, "vcpus": 2, "diskGiB": 20,
 "net": {"egress": "none" | "class:<slot>"},
 "mounts": [{"res": "res:apps/coding-sandbox/work", "path": "shared", "at": "/mnt/shared", "ro": false},
            {"source": true, "at": "/opt/manager"}],
 "defaults": {"cwd": "/work", "uid": 1000, "gid": 1000, "shell": "/bin/bash", "env": {"HOME": "/home/dev"}},
 "labels": {"manager.v": "1"}, "for": "apps/agent", "forUser": "alice",
 "idleStopMin": 30, "autoStart": true, "clientId": "c-5e1", "start": false,
 "from": {"sandbox": "img-base", "snapshot": "s-2"}}
```

- `mode` is required and must be available now (`runtime.modes`; `invalid`
  names why) — xbind never picks it, and a VM never falls back to a
  namespace.
- Sizes: 0 is the policy default; a size over a cap is clamped to it, and
  the answer says what applied. A VM's disk only grows (`PATCH` to less is
  `invalid`). More sandboxes than `perTile.max`, or VM disks summing over
  `perTile.diskGiB`, is 429 `limit`.
- `net.egress` is `none` (the default) or a **sandbox-net slot** the
  manager's manifest declares (`interfaces: {"internet": {"kind":
  "sandbox-net"}}`); `runtime.egress` lists them with what each reaches.
- A `res` mount is a `filesystem` resource of the manager's own scope that
  it holds the way its backend's `XBIN_RES_*` variables need: declared in
  its `uses` **and** granted (a same-scope `uses` entry is its own grant).
  A grant left over after the manifest dropped its `uses` entry mounts
  nothing ("declare it in uses"). A reader's mount is read-only. `path` is
  a clean relative sub-path of the resource. `source: true` mounts the
  tile's own code, read-only. `at` is absolute and clean, not `/`, and not
  under `/proc`, `/sys`, `/dev`, `/run/xbin`, `/opt/xbin` or `/.xbin-vm`
  (a VM's plumbing). `path` may name a file as well as a directory, in
  both modes. `sqlite` and
  other kinds are `invalid`.
- `defaults.env` keys `XBIN_*` are `invalid`: a sandbox never gets an xbin
  identity. `uid`/`gid` must be runnable (`users: root` — a namespace host
  mapping a single uid — allows only 0). `labels` are opaque, ≤ 1 KiB in
  all; `for` and `forUser` (≤ 128) are claims, stored and shown, widening
  nothing.
- `clientId` makes a create repeat-safe: the same request again answers 200
  with the sandbox, another request with that id is 409 `exists` (so is an
  existing name). `version` in a `PATCH` refuses a lost update (412
  `precondition`); a `PATCH` changes the fields it names, `defaults` and
  `labels` as a whole. `start: true` starts it after the create; a start
  that fails leaves it `stopped`, the failure in `stateDetail`. `from`
  clones (below).

**Running one.** A start runs the sandbox's first process — in
namespace mode, xbind's agent as PID 1 under the terminals' restricted
lockdown (no nested user or mount namespaces, mount points never
followed, `NO_NEW_PRIVS`), over its own upper pinned to the base image it
first started on, with its own hostname, its mounts and its egress class
resolved again — and answers once the sandbox is `running`. Its network
goes through a relay in xbind with no route to the host or to xbind (every
address the host delivers locally, and xbind's listen addresses, are
refused whatever the class says; a public address a NAT outside the host
maps to it isn't one — [isolation.md](/docs/isolation.md)); under `none` a connection is reset and a
DNS query answered REFUSED at once. Each sandbox may hold at most
`runtime.limits.flows` connections, and all tile sandboxes together a
share of xbind's descriptors. Where xbind's cgroup is delegated each
running sandbox has its own cgroup — `memory.max` its `memMiB` + 128 MiB
(its agent), no swap, `pids.max` the policy's `perSandbox.pids`, its
`vcpus` as a hard CPU cap — inside one `comp-tilesbx-*` cgroup capped by
the policy's `total`; its commands are what the OOM killer takes first, so
a command over the memory cap is killed and the sandbox runs on.

In **VM mode** the sandbox is a microVM with its own kernel, where it is
root (docker works), booted from the same base image. `runtime.modes`
offers it while the VM policy's `tiles` switch is on (`GET /vm`) and,
where VMs run emulated (`accel: "emulate"`, several times slower), its
`tilesEmulated` too; `runtime.unavailable` says why not. Its state is its
own disk (sparse, `diskGiB`; a `PATCH` that grows it applies at the next
start), its mounts are served into the guest at the same places, and its
network goes through the same relay. Its VM counts against the
workspace's VM count and memory budget and against `tilesBudgetMiB`: a
start past any of them is 429 `limit`. Where cgroups are delegated its
cgroup holds its `memMiB` plus what the VMM needs (192 MiB, 512 emulated),
512 processes and `vcpus` + 1 CPUs. A stop flushes the guest's disks
before the VM goes.

A start answers the sandbox as it stands: `running`, or `stopped` with
the failure in `stateDetail` (the sandbox's own start-up error, quoted). It
is refused 409 `state` when the sandbox is in `error`, or its start finds
it so (the sandbox reads `stopped` until a start looks) — its state went
missing, its base image is no longer installed, or its upper was written
by the other overlay flavour (`stateDetail` says which; a reset repairs
it, and a rebase repairs a missing base) — or while an earlier run's
processes still hold its state (a start waits up to 5 s for them first);
503 `unavailable` while the sandboxes policy is off or its file can't be
read, the workspace disk is low, the vault is sealed (a resource mount),
VM mode is unavailable (a VM sandbox: the reason said) or the tile is
disabled; 403 `not-allowed` when the tile no longer holds
`cap:sandboxes`; 400 `invalid` when a mount or the egress class is no
longer the tile's to use. A start of a running sandbox changes nothing.
One of the workspace's own reasons below that arrives while a sandbox is
still starting — a revoke, a disable, a seal, low disk, the policy
switched off — ends that start before it comes up: 503, `stopped`, the
reason in `stateDetail`; so does a mount the tile no longer holds as the
start resolved it (a `res:` grant revoked, a writer now a reader): 400
`invalid`.

**Admission.** Every start is booked against the tile's quotas — with it,
at most `perTile.running` sandboxes running, their `memMiB` summed within
`perTile.memMiB` and their `vcpus` within `perTile.vcpus` — and against the
workspace's `total.memMiB` (each running sandbox counts what its cgroup
may take: its `memMiB` + 128 MiB, a VM's `memMiB` + its VMM's 192 MiB, 512
emulated); over any of them is 429 `limit`,
naming the cap. So is a tile whose sandboxes' bytes on disk, snapshots
included, pass `perTile.diskGiB`; those bytes never count against the
tile's resource-write quota. The booking is atomic: ten starts at once
under `running: 4` run exactly four. A lowered cap applies at the next
start (a running sandbox shows `restartNeeded` for its sizes). A manager
whose manifest asks for more (`"limits": {"sandboxes": {…}}`, D203) and was
granted it has its `perTile.memMiB`, `vcpus` and `diskGiB` raised to the
grant, and `perSandbox.maxMemMiB`, `maxVCPUs` and `maxDiskGiB` with them
(one sandbox may use the whole grant) — a layer of its own above the
policy and its overrides, which it never rewrites; it applies from the next
start. A start refused at one of those totals, a sandbox stopped past
`perTile.diskGiB`, or one OOM-killed, is a hit an approver may raise
(isolation.md §Resource limits).

**Waiting.** Lifecycle calls take `?wait=<seconds>` (up to
`limits.waitMaxSec`; more is clamped; absent is `waitMaxSec`): the call
answers once its transition is done or the wait runs out, with the
sandbox as it stands (`starting`, say) — the transition goes on, and `GET`
says where it got. `?wait=0` answers without waiting. A refusal is
answered as itself. `POST /sandboxes` with `start: true` takes it too.

**Reset and rebase.** Both restart a sandbox that was running: it is
stopped (its execs end `killed`), reset or re-pinned, and started again,
so it is `running` afterwards; a stopped one stays stopped. Both keep its
snapshots and both answer the sandbox. **Reset** puts the state aside for
a confined removal and clears `base`, so the next start runs on the
current base image from an empty upper; it repairs `error` and repeating
it is harmless. **Rebase** keeps the state and pins it to the current
base image — what a package manager installed on the old one may break;
it repairs a missing base, but an upper of the other overlay flavour only
a reset repairs. `base.outdated` says a sandbox is pinned to an older base
than the current one.

**Idle stop.** A running sandbox that sees no activity for its
`idleStopMin` (its definition's, else the policy's) is stopped, state
kept, `stateDetail` "idle for N minutes: stopped, state kept
(idleStopMin)". Activity is a call on the sandbox (reading its
`SandboxInfo` or the list isn't one), exec input and output, a terminal
attach, detach or keystroke, and a file operation; `lastActive` is the
last, and stays so after the sandbox stopped. Work in flight holds it off
however long it runs: a non-tty exec, a `run`, a file, tar or copy
operation, an attached terminal client. A terminal exec nobody is
attached to and that prints nothing is idle.
A `PATCH` of `idleStopMin`, or a policy change, applies to running
sandboxes at once.

**Auto-start.** An exec or file call on a `stopped` sandbox with
`autoStart` starts it and waits for it (the command's `timeoutMs` starts
after); on a `starting` one it waits for `running`; on a `stopping` one it
waits for the stop, then starts it again — within `waitMaxSec`, else 409
`state`. Without `autoStart` a stopped or stopping sandbox answers 409
`state`.

However a run ends, the sandbox is `stopped` and `stateDetail` says why:
`""` after the manager's stop; "stopped by a workspace admin"; "the
sandbox's agent exited (code N)" or "(killed by SIGKILL)"; "the sandbox's
root filesystem (fuse-overlayfs) died"; "out of memory: N processes were
killed"; "the sandbox's agent stopped answering (its control connection
closed)"; "the VM exited: <the last lines of its console>" (a VM whose
VMM died); "an admin switched VM tile sandboxes off (vm policy:
tiles)…" (or `tilesEmulated`, for one running emulated); "its network
(class:<slot>) narrowed…" when the class's new
rules don't cover the running ones (a class that widens shows in
`egressNext` and applies at the next start, `restartNeeded`). A stop's
`stateDetail` is kept until the next start. xbind stops its tile sandboxes,
synced, when it exits, and they die with it when it dies: after a restart
every sandbox is `stopped`, its state kept, and an exec id of the old boot
answers 410 `lost`. A users-plane change (a policy row, a network set) is
checked against the running sandboxes' classes too: one whose class
narrowed is stopped as above. So are the workspace's own reasons, below.

**The workspace around them.** A running sandbox follows its tile's reach,
not only its next start — each of these stops it, synced, state kept, with
the reason in `stateDetail`:

- its tile is removed, disabled, hidden or offloaded, or loses
  `cap:sandboxes` — a revoke at once, a hand edit of `xbin.json` at the
  next rescan (a sandbox still starting then never comes up; its start
  answers 503);
- a mount the tile no longer holds (the grant revoked, the resource
  dropped from its manifest or deleted), or a read-write mount the tile
  now holds only as a reader — `stateDetail` names the mount; a role that
  widened waits for the next start;
- the vault sealed ("the vault was sealed…"), for every sandbox with a
  resource mounted, before the decrypted views go; its next start answers
  503 until the vault is unsealed. A `cap:containers` change of the scope
  (its resources remount) stops the scope's mounted sandboxes too;
- the disk (below).

`diskBytes` is measured — a namespace sandbox's upper and snapshots at
each stop and every 2 minutes while it runs, a VM's disks by their
allocated blocks. A tile whose sandboxes pass `perTile.diskGiB` while one
runs has its largest running namespace sandbox stopped ("the tile's
sandboxes use X GiB, over its N GiB (sandboxes policy: perTile.diskGiB)");
further starts are 429 until something is deleted. While the workspace
disk is low (the partition below diskmon's reserve), starts are 503 and the
running namespace sandboxes of every tile whose sandboxes hold more than
the fair share are stopped, largest tile first ("the workspace disk is
low…"); VM sandboxes, whose disks are bounded, run on. Sandbox bytes count
for the workspace's disk pressure, never against a scope's resource-write
quota.

A tile's backup carries its sandbox definitions, never their state. A
restore brings them back **by uid**, `stopped`: the same sandbox is
replaced and keeps its state; a name another sandbox holds now is left out
(listed in the restore's `sandboxesSkipped`); one that had state which is
gone comes back `error` ("restored without state — reset it…"). A tile's
offload is refused while its sandboxes hold state. A removed tile's
sandboxes stay — definitions and state — as leftovers of its path: a
non-admin can't create a tile there, and an admin deletes them with
`DELETE /sandboxes/<name>?tile=<path>`.

**Files and trees** go to the sandbox's own agent, which resolves every
path inside the sandbox: a symlink leads only to the sandbox's files, and
its read-only mounts refuse a write (400 `invalid`). xbind never resolves
one on the host.

- **Paths** are absolute and clean — no empty, `.` or `..` segment, no
  trailing `/` — and UTF-8 without NUL. A call's paths and `exclude`
  patterns together must fit the agent's 4 KiB request line: a path of a
  few thousand plain characters fits, and fewer where JSON escapes
  characters. Anything else is 400 `invalid`.
- **Running.** As for a command (Auto-start, above): a stopped sandbox
  with `autoStart` starts for the call, a `starting` one is waited for and
  a `stopping` one is waited out and started again, within `waitMaxSec`;
  without `autoStart` a stopped or stopping sandbox is 409 `state`. While a
  file, tar or copy call runs, the sandbox isn't idle, however long its
  stream takes.
- **`stat`** answers `{path, type: file | dir | symlink | other, size,
  mode, mtimeMs, etag, target?}`. A symlink is stated itself, not its
  target. `mode` is the permission bits in octal (`"0644"`). `etag` is
  `<inode>-<size>-<mtime ns>` in hex, so an atomic replace changes it.
- **Reading `content`** answers the bytes with `Content-Length` and
  `ETag: "<etag>"`, the stat's etag quoted. `offset` and `length` (0 = to
  the end) pick a range. A range over `limits.fileMax` is 413
  `too-large`. Only regular files are read: a device, FIFO or socket is
  `invalid`.
- **Writing `content`** streams the raw body into a temporary file and
  renames it into place (fsynced) only once all of it arrived, so a body
  cut short changes nothing. It answers the new stat.
  - `mode` sets the permission bits. Without it a replaced file keeps its
    mode, and a new one is `0644`. A replaced file keeps its owner too.
  - `mkdirs=1` makes missing parent directories.
  - `ifMatch=<etag>`, bare or quoted as the header gives it, replaces the
    file only while its etag is that one. `ifNoneMatch=*` only creates it.
    Either failing is 412 `precondition` with the current `etag`.
  - A body over `limits.fileMax` is 413, at once when its
    `Content-Length` says so.
- **`list`** answers `{path, entries: [{name, type, size, mtimeMs, mode,
  target?}], truncated}`, sorted by name. `limit` defaults to 1000; more
  than 100000 is clamped.
- **`mkdir`, `remove` and `move`** answer 204. `mkdir` without `parents`
  of a path that exists, and `remove` of a non-empty directory without
  `recursive`, are `invalid`. `move` onto an existing path without
  `overwrite` is 412 `precondition`, with the destination's `etag`.
- **Owners.** What these calls create (files, directories, tar entries)
  belongs to the sandbox's `defaults.uid`/`gid`.
- **`GET tar`** is the directory as a tar stream, with names relative to
  `path`.
  - Symlinks are stored as links and never followed. Hard links are
    stored as links. Devices, FIFOs and sockets are left out.
  - An `exclude` glob drops an entry whose name relative to `path`, or
    whose base name, matches it.
  - A tar of `/`, or of a symlink leading there, leaves out `/proc`,
    `/sys` and `/dev`.
  - It is bounded by `limits.tarMax`.
- **`PUT tar`** extracts the stream under `path`, which `mkdirs=1` makes.
  - No entry lands outside `path`: `../x` is skipped, and `/x` lands at
    `path/x`. A symlink an entry plants is never written through.
  - setuid and setgid bits are dropped, and devices aren't extracted.
  - A body over `limits.tarMax` is 413.
- **A stream that fails after its status went out** is cut: the
  connection closes before the body ends. This covers a tar passing
  `tarMax` and a read error part-way. Such a body never ends as if it
  were whole.
- **`POST /sandboxes/copy`** copies between two of the caller's own
  sandboxes, or within one. Naming another tile's sandbox is 404. The copy
  streams from agent to agent, bounded by `limits.tarMax`, and answers
  204.
  - A directory, or a symlink leading to one, is copied as a tree: its
    contents land at `to.path`. With `overwrite` they merge into what is
    there; without it, an existing `to.path` is 412.
  - Anything else is copied as a file, atomically. Without `overwrite`, an
    existing `to.path` is 412.
  - Missing parents of `to.path` are made.
  - A source that fails part-way leaves a file copy uncommitted. A tree
    keeps what was extracted by then.
  - Copying a tree into itself is 400.

**Snapshots and clones.** A snapshot is a copy of a sandbox's state — a
namespace sandbox's upper, exactly (whiteouts, opaque directories, owners,
modes and `user.*` xattrs; a copy that can't keep one fails), copied in a
confined run; a VM's disk, sparse, sharing its extents with the original
where the filesystem can. xbind never reads either.

- **Taking one** stops the sandbox (its execs end `killed`), copies, and
  starts it again if it ran. A sandbox that never ran has nothing to copy
  (409 `state`). Ids are `s-<n>`, never handed out twice for a sandbox.
  `clientId` makes it repeat-safe (per sandbox): the same `name` answers the
  snapshot (200), another is 409 `exists`.
- **A snapshot pins its base image**: the base it was built on stays
  installed while the snapshot exists, whatever the sandbox does next
  (reset, rebase, an upgrade).
- **A restore** puts the snapshot's state back, base included — `base`
  follows it, and a rebase moves it on again — and starts the sandbox again
  if it ran. It repairs `error`. The old state goes to a confined removal.
- **A clone** is `POST /sandboxes` with `from: {sandbox, snapshot?}`, a
  sandbox of the same manager, in its `mode`: of a snapshot, whatever the
  source does meanwhile, or — without `snapshot` — of the source's state,
  only while it is `stopped` (409 `state` otherwise: stop it, or clone a
  snapshot of it). It takes the source's base image; a VM clone's disk is
  never smaller than the source's. It counts against `perTile.max` from
  the moment it is defined.
- **Refused:** another `mode`, and in namespace mode an upper of the other
  overlay flavour (fuse-overlayfs's and the kernel's can't read each
  other's), are 400 `invalid`; so is a snapshot whose base image is no
  longer installed. A snapshot or clone whose bytes (the snapshot's, or the
  source's last measured) would take the tile's sandboxes past
  `perTile.diskGiB` — copies still running counted — is 429 `limit`.
  Snapshots count toward `diskBytes`.
- **Copies run off the request**, and are made whole or not at all: a
  crash or a restart leaves the old state, no half snapshot, and a clone
  it cut short in `error`. `?wait=<seconds>` (absent: `waitMaxSec`) bounds
  how long the call waits: done in time, the normal answer; not done, a
  snapshot answers 202 with `pending: true` (it lists so until it is
  done), a restore the sandbox with `stateDetail` `busy: restoring
  snapshot s-2`, a clone 201 `creating` — poll `GET`.
- **While a snapshot or a restore copies** (and while a clone copies a
  stopped source's state, the source) the sandbox is **busy**: its
  `stateDetail` starts `busy: `, and start, stop, reset, rebase, another
  snapshot, a restore, `DELETE` and every command or file call answer 409
  `state` with that `stateDetail` and `retryAfterMs` — but a command or
  file call on a sandbox with `autoStart` waits the copy out, as it waits
  out a stop. A snapshot a clone is copying, and its sandbox, aren't
  deleted meanwhile (409 `state`). The workspace's own reasons (a revoke,
  a seal, the policy switched off) find the sandbox stopped for its copy,
  and its start after the copy is refused as any start would be; xbind's
  shutdown ends the copy, leaves the sandbox stopped, and what it staged
  is removed.
- **A clone that fails** — its copy failed, or an xbind restart cut it
  short — is `error`, and answers 409 `state` to everything but `GET`, the
  list and `DELETE`.

**SandboxInfo:** `{name, uid, state (creating | stopped | starting |
running | stopping | error), stateDetail, mode, accel?, memMiB, vcpus,
diskGiB, net:{egress, reach, egressNext, note}, mounts, defaults, labels,
for?, forUser?, idleStopMin, autoStart, base:{version, outdated}, users,
diskBytes, snapshots, execsRunning, created, started?, lastActive?,
version, clientId?, restartNeeded, usage?}` — `uid` is the sandbox's identity (12 hex,
fixed at create; its name is only its address): a sandbox deleted and
created again under the same name gets another `uid`, and never the old
one's state. `creating` is a clone whose copy still runs: until it ends
`stopped` (or `running`, or `error`), every call but `GET`, the list and
`DELETE` answers 409 `state`. `reach` is what the egress reaches (`none`,
`internet` or `open`, the contract's words), `egressNext` an egress a
`PATCH` set that waits for the next start.

**`usage`** (D215) is a running sandbox's CPU and memory: its cgroup leaf
read once a minute, the last 60 readings kept in memory — the contract's
`usage` ([sandbox-manager.md](/docs/sandbox-manager.md) §The sandbox, §On xbin).
Only `GET /sandboxes/<name>` carries its `series`. Absent before the first
reading, once stopped, and without cgroup delegation.

**The policy** (`.xbin/sandboxes/policy.json`; 0 = the default, a `PUT`
keeps the fields it doesn't name): `{enabled (true), perTile:{max 8,
running 4, memMiB 8192, vcpus 8, diskGiB 100}, perSandbox:{memMiB 2048,
vcpus 2, diskGiB 20, maxMemMiB 8192, maxVCPUs 8, maxDiskGiB 200, pids
4096}, total:{memMiB 0, pids 32768}, idleStopMin 30 (≤ 1440), outputRingMiB
1 (≤ 8), outputBudgetMiB 64, overrides:{"<tile>": {perTile, perSandbox,
…}}}` — `total` caps every tile sandbox together (`memMiB` 0 = ¾ of the
host's RAM) and has no per-tile override; an override replaces that tile's
previous one, `null` removes it. `runtime.limits.flows` is each sandbox's
cap on concurrent network connections (TCP and UDP flows through its relay);
past it a new one is refused at once. Turning `enabled` off stops every
running tile sandbox (state kept, `stateDetail` says so) and refuses
starts. **A policy file that can't be read fails closed:** `GET` shows why
in `error`, `policy.enabled` is false and so is `runtime.enabled`, and no
tile sandbox starts (503) — whatever the file said — until an admin's
`PUT` writes a new one (merged onto the defaults), which clears the
error.

**Commands** (`run`, `execs`) are the contract's, byte for byte
([sandbox-manager.md](/docs/sandbox-manager.md) §Running commands), plus `uid`,
`gid` and `forUser` (a claim, like the definition's):

- **What runs.** `argv`, or `cmd` run as `<defaults.shell or /bin/sh> -lc
  <cmd>` — one of them, never both. `cwd` defaults to `defaults.cwd`, else
  `/`, and must exist: a missing one is 400 `invalid`, never a fallback to
  `/`; so is an `argv` whose program can't start — but a start that
  fails because the sandbox itself ran out of processes, memory or file
  descriptors (its `pids.max`, say) is 429 `limit`, to retry once some of
  its processes ended. `uid`/`gid` default to
  the definition's (`users: root` allows only 0). `argv` and `env` together
  are at most 256 KiB (413). The environment is xbind's: `IN_SANDBOX=1`
  (always), `SANDBOX_ID` and `SANDBOX_NAME` (the sandbox's name), `HOME`
  (`/root` for root, `/` for any other user) and a `PATH` with the base
  rootfs's toolchains first (`/usr/local/{go,node,bun}/bin`, as terminals
  and backends have it), plus `PLAYWRIGHT_BROWSERS_PATH=/usr/local/ms-playwright`
  where the rootfs ships Playwright's browsers — and, for a command on a
  terminal (a `tty` exec, the `tty` route), `TERM=xterm-256color`,
  `COLORTERM=truecolor` and `LANG=C.UTF-8`, as xbind's terminals have
  them — with `defaults.env` and then the command's `env` over them;
  `XBIN_*` keys are 400. Nothing of xbind's or of the agent's own
  environment gets in, nor xbind's user's supplementary groups: a command has none (except on a
  namespace host mapping a single uid, `users: root`, which can't drop
  them). Each command leads its own process group, and it is what the OOM
  killer takes first.
- **A stopped sandbox** with `autoStart` (the default) is started for a
  command, a `starting` one waited for and a `stopping` one waited out and
  started again (Auto-start, above); without `autoStart` a stopped or
  stopping one is 409 `state`. A run's `timeoutMs` doesn't count the
  start.
- **Limits.** A sandbox runs at most `runtime.limits.execsRunning` (16)
  commands at once — execs and runs together; past it 429 `limit`.
- **`run`** answers when the command ends: `{exitCode, signal, timedOut,
  ms, stdout, stderr}` (`output` with `merge`), each `{head, tail, elided,
  bytes}` — past `maxOutput` (default and cap `runOutputMax`, 1 MiB per
  stream) its first quarter in `head` and its last three quarters in
  `tail`; text is UTF-8 with invalid bytes replaced. `timeoutMs` defaults
  to 60000 (cap `runTimeoutMaxMs`); at it the process group gets TERM,
  then KILL 5 s later, and `timedOut` is true. A caller that hangs up has
  the group killed. `stdin` is a string (at most `stdinMax`). A run isn't
  listed.
- **Execs.** `POST …/execs` answers 201 once the command runs (200 when a
  `clientId`, per sandbox, repeats the same request; 409 `exists` for
  another). Its id is `<6 hex>-<n>`, the hex random per xbind start: an id
  from before xbind restarted is 410 `lost` — the only `lost` — and one
  this sandbox doesn't have is 404. An exec is `{id, label, cmd, argv,
  cwd, tty, state (running | exited | killed), exitCode, signal, started,
  ended, total, split?, errTotal?, clientId, forUser, uid}`: `killed` when a signal ended it
  (`exitCode` null, `signal` names it), and when its timeout or a `DELETE`
  did. **A stop — the manager's, an admin's, or however the sandbox ended
  — ends its running execs `killed` with `signal: "KILL"`**; their records
  and output stay. A finished exec is kept an hour, and past that while it
  is one of the sandbox's last 50 (at most 1000 within the hour); deleting
  the sandbox forgets them all. `timeoutMs` (0 = none) sends TERM to the
  group, then KILL 5 s later.
- **Output.** A non-tty exec's stdout and stderr are one stream, a tty
  exec's is its terminal, kept in a ring of `limits.outputRing` bytes —
  unless it was started with `split: true` (not with `tty`: 400), which
  keeps its stderr in a second ring: `…/output` and `total` are then its
  stdout, `…/output?stream=stderr` its stderr (offsets of its own), and the
  exec answers `split: true` and `errTotal`; `stream=stderr` on an exec
  that isn't split is 400. The tile's rings share the policy's
  `outputBudgetMiB`, and past it the
  oldest finished exec's bytes go first (its `ringStart` moves). `GET
  …/output?since=&max=&waitMs=&encoding=` answers `{start, end, total,
  ringStart, data, encoding, state, exitCode, signal}`: the bytes from
  `since` (or the ring's oldest: `start > since` is a gap), at most `max`
  (default 64 KiB, cap 1 MiB); with nothing past `since` while the exec
  runs it waits up to `waitMs` (cap 30000) and answers as soon as the exec
  ends. `text` never splits a character across two reads (`end` stops
  before it) and replaces invalid bytes; `base64` is exact. A `since` past
  `total` is 400.
- **stdin** (`POST …/stdin`, the raw body, at most `stdinMax`) goes to an
  exec started with `stdin: true`, or to a tty exec's terminal; `?eof=1`
  closes stdin after it (a tty's is 400: send `^D`). Without `stdin: true`
  it is 400, after the exec ended 409 `state`, and a command that doesn't
  read it for 30 s answers 503. **signal** takes `{signal: INT | TERM |
  KILL | HUP, group?}`: `group` defaults to true, so a forwarded
  `{"signal":"INT"}` reaches the whole process group. **resize** is a tty
  exec's (`{rows, cols}`, 1…65535). Both are 409 `state` once the exec
  ended. `DELETE …/execs/<id>` kills the group and forgets the exec.

**The TTY routes** are WebSockets on exactly the `/ws/term` wire (protocol/terminals.md):
binary frames both ways (the ring's tail replays first, at most 256 KiB),
`{"op":"session","id":…,"sandbox":…,"echoAck":true}` first, acks and
pongs, and `{"op":"exit","code":N}` at the end (`code` null and a `signal`
when a signal ended it); the client sends `resize` and `ping`. `sessionId`
and `sandboxId` (`[A-Za-z0-9._-]{1,64}`) replace the session frame's `id`
and `sandbox`, so a manager relaying the bytes shows its consumer its own
ids. `GET …/tty` starts a tty exec — the login shell (`<shell> -l`), or
`cmd` — labelled `terminal` and listed under `execs`, and attaches; an
attach to a tty exec that ended replays its ring and says `exit`. A client
that leaves doesn't end the command. Refusals come before the upgrade, as
JSON: a request that isn't a WebSocket upgrade is 400, and so is an attach
to an exec without a terminal. Only the manager's instance token reaches
them — no person does; the manager relays the socket to its consumer's
page. A tty exec whose `forUser`, or an attach whose `forUser`, names a
user with `noTerminal` (D88) is 403, checked again at every attach, and a
user's `noTerminal` taking effect — switched on, or an admin who had it
set demoted (by the users API, an org role or SSO) — kills the tty execs
claimed for them and those they attached to; non-tty execs aren't
restricted by it. One tty exec is attached for at most 64 distinct
`forUser` values; a new one past them is 429 `limit`.

**The stdio route** (`GET …/execs/<id>/stdio?since=&errSince=`, the
contract's `stdio`, [sandbox-manager.md](/docs/sandbox-manager.md) §stdio) is
a WebSocket for a non-tty exec: `{"op":"hello","id","total","errTotal","state","stdin","split"}`
first; stdout as binary frames from `since` — the ring's bytes, then live
— after `{"op":"gap","stream":"stdout","from","to"}` where the ring
dropped some; a split exec's stderr from `errSince` as
`{"op":"stderr","off","data"}` (base64; its own gap); `{"op":"exit","code","signal","total","errTotal"}`
once the exec ended and all its output is out, then a close (1000);
`{"op":"pong","t"}`; and `{"op":"error","refusal","error"}` for a client
frame that couldn't be done (stdin without `stdin: true` or after `eof`:
`invalid`; after the end: `state`). The client sends stdin as binary
frames (each at most `stdinMax`: past it the socket closes 1009),
`{"op":"eof"}` and `{"op":"ping","t"}`; a stdin frame waits for the
command to read it (the socket holds the client meanwhile — no 503).
The socket attached last holds stdin: attaching closes the one before it
with 4001. Refusals come before the upgrade, as JSON: not a WebSocket, a
tty exec, or an offset past its stream's end is 400. A client that leaves
doesn't end the command. Only the manager's instance token reaches it;
the manager relays it to its consumer (the SDK's `RelayStdio`).
