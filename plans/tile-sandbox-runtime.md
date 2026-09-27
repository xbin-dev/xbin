# Tile-sandbox runtime — the implementation plan

> Status: **live** (D120) — the implementation plan for phase 2 of
> [sandbox-managers.md](sandbox-managers.md): xbind's tile-sandbox runtime,
> building [tile-sandboxes.md](tile-sandboxes.md) (D113) as revised by D115
> and D120. Being built on branch `sandbox-runtime`; §12 is the work list.
> The builtin `coding-sandbox` manager (phase 3) serves
> [docs/sandbox-manager.md](../docs/sandbox-manager.md) by driving this
> runtime through the SDK (§10, §11).
>
> **Amended after wave 1** (D120 addendum, 2026-09-27): an adversarial
> review of waves 2–3 (15 findings) and wave 1's as-built handoffs are folded
> into §1–§11 and into the specs of WP-15a…WP-22. The things wave 1 already
> built that the review changes are small follow-up WPs (WP-2b, 3b… 14b);
> §12 says which can start now and which wait for WP-15a.

## 0. What this plan settles

The inputs are D113's design, the phase 2 and 3 sections of
sandbox-managers.md, the contract (D115), the owner's kickoff decisions (D120)
and six subsystem maps: VM, namespace + confine, network, terminals, storage,
auth. Where this plan departs from D113, it says so here:

| D113 said | This plan (D120) |
|---|---|
| Any tile with `cap:sandboxes` | Only **manager** tiles hold it; workspace admins approve it and no allowance delegates it, not even `cap:*` (§8.1) |
| A listening socket in `.xbin/run/sbx/`, passed as `AgentFD` | A **connection factory**: a `SOCK_SEQPACKET` socketpair whose other end xbind keeps. No path, no stale socket, no 108-byte limit. Its EOF is also how a sandbox learns that xbind died (§2.1) |
| Exec output as chunked NDJSON `{s, seq, data}` | Output is **read by byte offset with a long-poll**, in the contract's shape. Most runtime routes mirror the contract, so a manager forwards them unchanged (§3, §11) |
| Human attach with xbind tickets | **Relay only.** The TTY WebSocket answers only the manager's instance token, and the manager relays it byte for byte. No tickets (§3.7) |
| Egress `none \| inherit \| [rules]` | Egress is **`none \| class:<slot>`**. A class is a new request-side interface kind, `sandbox-net`, that the manager declares and an approver binds. There is no `inherit` and no rule-list subsets (§4) |
| Definitions in the tile's `data/sandboxes.json` | Definitions live in **`<ws>/data/sandboxes.json`, keyed by tile**. xbind owns this file and no sandbox or backend can write it. This is the layout the dev-lifecycle `08-data` table uses (§1.3) |
| Deleting a tile deletes its sandboxes | Removing a tile **stops** its sandboxes and **keeps** their state. The state is listed as a leftover and an admin cleans it up. State is never dropped silently (§9) |
| Namespace uppers in backups? (open) | **No.** Backups carry definitions. State moves only through snapshots and, later, archive/thaw (§9) |
| State under `.xbin/sbx/<key>/<deployment>/…` off `main` | Off `main`, state goes under **`.xbin/deploy/<TK>/d/<d>/sbx/<name>.<uid>/`**. A deployment segment under `.xbin/sbx/<key>/` would collide with sandbox names (§1.4) |

**Out of scope, with a seam left for each:** Deployment plumbing
(dev-lifecycle); ports and previews; a private sandbox-to-sandbox network;
`code:` mounts and scratch volumes; GPUs; exec-exit pushes to the backend;
the sandbox → tile role. **Later work package:** archive/thaw (WP-22). Until
it lands, offload refuses while any sandbox of the tile has state.

## 1. Architecture

### 1.1 Components

| Package / file | Role | New or changed |
|---|---|---|
| `internal/sandbox/vm/proto` | The wire between xbind, the agent and the shim. Additive changes: file ops, strict cwd, group signals, uid/gid, `Merge`/`NoStdin`/`NoSync`, bounded reads, `HostSpec.Resident` | changed |
| `internal/sandbox/agentcore` | **One exec core** for both modes. It holds the sessions, streams, PTYs and file operations, and runs inside the sandbox | new (factored out of `vm/guest`) |
| `internal/sandbox` | `Spec.Agent`/`AgentFD`, `Spec.Lock`/`LockFD`, `Spec.Hostname`, `Bind.Sub`, mount points that are never followed, the connection factory, fd hygiene | changed |
| `cmd/bx` `__sbx-agent` | Namespace mode's PID 1: agentcore, reached through the factory | new |
| `internal/sandbox/vm/host` | The **resident shim**: configures the guest, runs no session 1, and routes xbind's control and stream connections to the guest | changed |
| `internal/vm` | `Options.Resident`; policy fields `tiles`, `tilesBudgetMiB`, `tilesEmulated`; the `TileSandbox()` reserve option; `EnsureDiskAt`; `ListDisks` over both trees; image GC that keeps pinned bases | changed |
| `internal/layers` | Base pinning and overlay-flavour stamps for `.xbin/term` and `.xbin/sbx` alike | new (out of `term/base.go`) |
| `internal/cgroup` | Per-call limits with `cpu.max`, `Kill`, `Populated`, a boot `Sweep`, and a per-workspace `comp-tilesbx` parent that holds every tile-sandbox leaf (§6.2) | changed |
| `internal/confine` | A file-caps profile (`FSCaps`) for confined copy and remove of uppers that may hold sub-uid-owned files | changed |
| `internal/termwire` | The `/ws/term` wire (hub, echo acks, upgrade), shared by terminals and tile TTY execs | new (out of `term/attach.go`) |
| `internal/sandbox/relay` | `Deny` (no route to the host itself, decided per flow), `DNSRefuse`, a nil-`Allow` fix, flow caps per relay and across relays, the strict public predicate (§4) | changed |
| `internal/broker` | `cap:sandboxes`, the `sandbox-net` kind, resource-mount resolution, and the hooks for lifecycle, backup, leftovers and usage | changed |
| `internal/tilesbx` | **The runtime:** definitions, policy, admission, lifecycle, agent client, execs, rings, run, TTY, files, snapshots, registry entries, idle timers | new |
| `internal/boot/tilesandboxes.go` | Route literals, the boot step, wiring | new |
| `sdk/sandbox*.go` | The zero-dependency Go surface managers use | new |

### 1.2 Processes

```
namespace mode                                   VM mode
──────────────                                   ───────
xbind                                            xbind
 ├─ tilesbx ── factory (SEQPACKET) ──┐            ├─ tilesbx ── factory ──────────────┐
 └─ relay (gVisor) ◄──── TUN ────────┤            └─ relay ◄── TUN (jail netns, TAP) ─┤
                                     ▼                                                ▼
 [userns pidns mntns netns uts ipc;               [jail: userns … netns; VM lockdown]
  Restricted + MountGuard + NO_NEW_PRIVS]          PID 1  bx __vm-host (resident router)
  PID 1  bx __sbx-agent (agentcore)                  ├─ firecracker | qemu+vhost-vsock
    ├─ fuse-overlayfs (the upper)                    └─ FUSE file server (mounts)
    ├─ session 2  sh -lc 'go test ./...'                 │ vsock: ctl + one conn per stream/file op
    └─ session 3  bash -l   (pty)                        ▼
                                                   guest PID 1 xbin-vmagent (agentcore)
                                                     ├─ FUSE relay process (WP-0)
                                                     └─ sessions 2…
```

- **Every connection** from xbind to a sandbox is a new stream socket that
  xbind obtains through the factory: `sandbox.Factory.Dial()` makes a
  `socketpair(AF_UNIX, SOCK_STREAM)` and sends one end over the factory with
  `SCM_RIGHTS`. Each connection opens with a `proto.Hello`: one `ctl`, one
  per exec stream, one per file operation.
- **There is one control connection per running sandbox**, owned by xbind. In
  VM mode the shim keeps the guest's only `ctl` and multiplexes over it. The
  guest agent sends its events only to the newest `ctl`.
- **xbind's death kills tile sandboxes.** The factory reads EOF. The
  namespace agent then runs `syncfs(/)` and exits. The shim tells the guest to
  sync (at most 2 s) and exits. Either way PID 1 is gone, and the kernel
  tears down the pid namespace.
- **A sandbox can also end on its own**: its agent crashes, a cgroup OOM
  kill takes it, the VMM exits (the shim's 125), or fuse-overlayfs dies under
  it (the agent then exits, §2.4). xbind notices through a watcher on
  `cmd.Wait()` and on the `ctl` connection's EOF, and runs the same
  `teardown(reason)` a stop runs (§7).

### 1.3 Data layout

```
<ws>/data/sandboxes.json                    definitions, main, keyed by tile path (xbind-only, 0600)
<ws>/data/deployments/<TK>/<d>/sandboxes.json   seam: a non-main deployment's definitions (dev-lifecycle)
<ws>/.xbin/sandboxes/policy.json            the sandboxes policy (admin)
<ws>/.xbin/vm/policy.json                   + tiles, tilesBudgetMiB, tilesEmulated
<ws>/.xbin/sbx/<CK>/<name>.<uid>/           one sandbox's state, 0700, xbind-created; <uid> is Def.uid
    lock                                    flock'd by the running sandbox (its init, agent, fuse-overlayfs)
    cur/                                    THE state: the one unit a restore swaps (RENAME_EXCHANGE)
        base                                pinned base version (internal/layers)
        overlay                             namespace: "fuse" | "kernel" — the flavour that wrote upper/
        upper/  work/                       namespace mode (sandbox-written: never walked as xbind)
        vm/disk.img                         VM mode: sparse ext4 the guest formats (never parsed by the host)
    snapshots/<sid>/{meta.json, base, overlay, upper/ | vm/disk.img}   complete or absent (staged in tmp/)
    tmp/<rand>/                             staging for snapshot/restore/clone copies (xbind-created, empty)
<ws>/.xbin/sbx/<CK>/.trash/<uid>[.<rand>]   what DELETE, reset and restore put aside; a confined rm empties it
<ws>/.xbin/deploy/<TK>/d/<d>/sbx/<name>.<uid>/    seam: a non-main deployment's state
```

- **The definitions file** has this shape: `{"version":1,"tiles":{"<path>":{"sandboxes":{"<name>":Def}}}}`.
  It is written atomically (tmp + rename) under the manager's mutex and loaded
  at boot. A `Def` is exactly what `POST /sandboxes` stored, after
  validation, plus `uid`, `created`, `version`, `base` (mirrors the stamp;
  archived sandboxes keep it), `clientId`, `snapSeq` (the last snapshot
  number handed out) and `pending` (`"clone"` while a clone's copy runs).
  `data/` is masked from terminals and never bound into a backend, so no tile
  code can forge a mount, an egress class or a mode. Everything read back is
  re-validated against the tile's **current** reach on every start (§8).
- **`uid` is the sandbox's identity; its name is only its address.** `uid`
  is 12 random hex characters, set at create and never changed, and it keys
  everything that outlives a request: the state dir, `.trash` entries, a
  backup's match, the archive key. Deleting `img-base` and creating a new
  `img-base` gives a new uid, so the new sandbox never meets the old one's
  state, a slow removal of it, or its archive. A definition loaded without a
  `uid` (only this branch ever wrote one) is given one on load.
- **`cur/` is swapped whole.** A restore stages the new state in `tmp/`
  and swaps it with `cur` in one `renameat2(RENAME_EXCHANGE)`, so a crash
  leaves either the old state or the new one, stamps included, and never a
  missing `upper/` or an old `work/` beside a new `upper/`. A start that
  finds no `cur/` for a definition whose `base` is set puts the sandbox in
  `error` ("its state is missing — reset it"); it never starts a blank root
  silently.
- **Nothing is deleted inline.** DELETE renames the state dir to
  `.trash/<uid>`, reset and restore rename the old `cur` to
  `.trash/<uid>.<rand>`, and one background worker empties `.trash` with
  `confine.RemoveAll`, one entry at a time. The boot re-queues whatever
  `.trash` holds and empties every `tmp/` (nothing staged survives a
  restart).
- **`.xbin/sbx`** joins `token`, `secret` and `term` as *not derived, not safe
  to delete* (docs/overview/02-workspace.md; ARCHITECTURE.md's "derived
  state" line is corrected too).

### 1.4 The key: (tile, deployment)

Every map, path, id and book in `tilesbx` is keyed by `Key{Tile, Deployment
string}` (`""` = main). One file, `internal/tilesbx/keys.go`, derives
everything from a key:

| | main | a non-main deployment (seam) |
|---|---|---|
| definitions | `data/sandboxes.json` → `tiles[<path>]` | `data/deployments/<TK>/<d>/sandboxes.json` |
| state root | `.xbin/sbx/<CK>/` | `.xbin/deploy/<TK>/d/<d>/sbx/` |
| state dir | `<state root>/<name>.<uid>/` | same, under its root |
| registry id | `tile:<CK>:<name>` | `tile+<d>:<CK>:<name>` |
| cgroup leaf (under `comp-tilesbx-<ws8>/`, §6.2) | `sbx-<CK>-<name>` | `sbx-<CK>+<d>-<name>` |
| archive key (WP-22) | `<CK>.sbx.<name>.<uid>` | `<CK>.deployments.<TK>.<d>.sbx.<name>.<uid>` |

- **One place builds a key: `keyOf(p auth.Principal) (Key, error)`** in
  `keys.go`. Every gate calls it; nothing else reads `p.Component` to name
  sandboxes. On this branch every key is main: `Key{Tile: p.Component}`.
- **A principal of a non-main deployment gets 501 `unsupported` on every
  manager route** until non-main keys are built. Without this, the moment
  dev-lifecycle adds `Principal.Deployment` (with `Component` staying the
  tile's path, its P11), a branch deployed as a dev deployment of the
  manager would compile unchanged and silently address **main's**
  sandboxes: list them, exec into them, read every consumer's files, and
  spend main's quota. So:
  - `keyOf` reads a `Deployment` string field of the principal by
    reflection (`deploymentOf(p)`: `reflect.ValueOf(p).FieldByName`, the
    index cached), which is `""` on this branch. A non-empty value answers
    `errDeployment()`. The guard works the moment the field appears, before
    anyone edits `keyOf`.
  - A test (`keys_test.go`) lists `auth.Principal`'s fields by reflection
    against a reviewed list and fails on any field not on it ("a new
    principal field must be reviewed for the sandbox key"), and checks that
    a principal with a non-empty `Deployment` (set by reflection when the
    field exists) gets 501. Whoever merges dev-lifecycle replaces the
    reflection with `p.Deployment` and extends the list (§14).
- For a non-main key the non-main paths answer `unsupported` until their
  `TileKey` helper exists.
- **Two more hooks** default to today's behaviour and belong to the
  deployment work: `CodeRoot(key)`, which is `{source:true}`'s source (the
  tile dir today), and `ResourceMount(key, res)`, which resolves a resource
  in the deployment's data namespace.
- **The policy, `cap:sandboxes`, quotas and VM reservations are the tile's**,
  summed across its deployments. `Reserve(owner = tile)`.

## 2. One exec protocol for both modes

### 2.1 Transport: the connection factory

```go
// internal/sandbox/factory_linux.go
type Factory struct{ /* xbind's end of a SOCK_SEQPACKET socketpair */ }
func NewFactory() (xbind *Factory, child *os.File, err error) // child → Spec.Agent
func (f *Factory) Dial() (net.Conn, error)  // fresh SOCK_STREAM pair; one end sent (MSG_DONTWAIT: a wedged agent is an error, not a hang)
func (f *Factory) Close() error
func AcceptFrom(f *os.File) (net.Conn, error) // agent/shim side: recvmsg; io.EOF = xbind is gone
```

- `Launch` appends `Spec.Agent` to `ExtraFiles`, after ctrl and sync, and
  records `AgentFD`. It then appends `Spec.Lock`, an `O_RDONLY` fd of
  `<state>/lock` already `flock`ed by xbind, and records `LockFD`.
  - Once the sandbox has started, xbind **closes** its copy. It never calls
    `LOCK_UN`, which would release the lock for the sandbox too.
  - A flock belongs to the open file description, so the lock then lives as
    long as any process of the sandbox holds that description: its init, the
    agent or the shim, and fuse-overlayfs.
- A new `Handle.Started()` closes the parent's copies of the child-side files,
  which today are left to the GC. `fail()` never closes the caller's files.
- **The init's fd hygiene.**
  - Right after it reads the spec, the init marks `AgentFD` (and `CtrlFD`,
    which leaks into fuse-overlayfs today) `FD_CLOEXEC`.
  - `LockFD` stays inheritable, so fuse-overlayfs holds the lock too.
  - Immediately before `unix.Exec(entry)` the init clears `FD_CLOEXEC` on
    `AgentFD`. Its number travels in argv (`bx __sbx-agent --fd N --lock M`),
    never in the environment.
- **The agent**, in its first few lines, does four things. It dups both fds
  `O_CLOEXEC` and closes the originals, so execs never inherit them. It calls
  `prctl(PR_SET_DUMPABLE, 0)`: `Restricted` has dropped `CAP_SYS_PTRACE`, so
  execs can't `ptrace` it, `pidfd_getfd` its fds or read `/proc/1/fd`. It
  ignores SIGTERM, SIGINT, SIGHUP, SIGQUIT, SIGUSR1 and SIGUSR2, because
  `CAP_KILL` is kept. And it becomes the PID 1 reaper.

### 2.2 `proto` additions (all `omitempty`, additive)

```go
type Hello struct { Kind string; Session int; Stream string
    File *FileOp `json:"file,omitempty"` }            // Kind "file" (new)
type Exec struct { /* Session, Path, Argv, Env, Cwd, TTY, Rows, Cols, Listen, Gateway */
    CwdStrict bool    `json:"cwdStrict,omitempty"`    // a missing cwd is an error, never "/"
    UID       *uint32 `json:"uid,omitempty"`          // run as (refused when not mapped)
    GID       *uint32 `json:"gid,omitempty"`
    Merge     bool    `json:"merge,omitempty"`        // stderr → the stdout stream (one combined stream)
    NoStdin   bool    `json:"noStdin,omitempty"`      // stdin = /dev/null; no "stdin" stream expected
    NoSync    bool    `json:"noSync,omitempty"`       // skip the per-exit unix.Sync (resident sandboxes)
}
type Msg struct { /* Op, Config, Exec, Session, Rows, Cols, Signal, Code, Error */
    Group bool `json:"group,omitempty"` // "signal": kill(-pgid) (execs Setsid, so pgid = pid)
    Pid   int  `json:"pid,omitempty"`   // "started": the session's pid (in the sandbox's pidns)
    // "exited": Signal > 0 when a signal ended it (Code stays 128+sig for old readers)
}
func (c *Conn) RecvMax(v any, max int) error // bounded line; over max → error, connection dropped
func ReadHelloMax(c io.Reader, max int) (Hello, *bufio.Reader, error)
type HostSpec struct { /* … */ Resident bool `json:"resident,omitempty"`; AgentFD int `json:"agentFd,omitempty"` }
```

**File operations** (`proto/file.go`): one connection each, `Hello{Kind:"file", File:&FileOp{…}}`.

```go
type FileOp struct {
    Op        string   // stat | read | write | list | mkdir | remove | move | tar-get | tar-put
    Path, To  string   // absolute, inside the sandbox; To for move
    Offset, Length int64 // read (Length 0 = to EOF)
    Mode      uint32   // write, mkdir (0 = 0644 / 0755)
    Mkdirs, Parents, Recursive, Overwrite bool
    IfMatch   string   // write: precondition on the current etag
    Create    bool     // write: ifNoneMatch=* (create only)
    Owner     *[2]uint32 // chown what is created (the definition's default uid/gid)
    Limit     int      // list (default 1000)
    Exclude   []string // tar-get: globs relative to Path
    Max       int64    // write / tar-put: refuse past this many bytes (too-large)
}
type FileResult struct {
    OK bool; Refusal, Error string // refusal: invalid | not-found | precondition | too-large
    Stat *FileStat; Entries []FileStat; Truncated bool
}
type FileStat struct { Path, Name, Type, Target string; Size int64; Mode uint32; MTimeMs int64; ETag string }
```

- **Framing.** Data travels in frames of `u32 big-endian length` plus bytes,
  at most 1 MiB each, and a zero-length frame ends the data.
  - `read` and `tar-get`: the agent sends a `FileResult` line (the stat, or
    the refusal), then the data frames, the terminator, and a final
    `FileResult` line (`ok`, or an error that happened mid-stream).
  - `write` and `tar-put`: xbind sends the frames and the terminator, and the
    agent answers with one line.
  - Everything else is one line each way.
- **Why frames.** A dropped connection can never pass for EOF. A `write`
  commits (temp file, `fsync`, `rename`) only after the terminator arrives.
  None of this depends on vsock half-close. xbind only reads frame lengths; it
  never parses content.
- **ETag** is `"<ino>-<size>-<mtime ns>"` in hex. An atomic replace gets a
  new inode, so its etag changes.

### 2.3 `agentcore`

```go
package agentcore
type Options struct {
    Spawn      Spawner                  // PID1Spawner (guest, __sbx-agent); ProcSpawner (tests)
    Configure  func(proto.Config) error // VM: assemble root/net/mounts; nil: "ready" at once (namespace)
    Sync       func()                   // "sync": VM unix.Sync(); namespace syncfs("/") — never sync(2)
    Root       string                   // file ops resolve IN this root (openat2 RESOLVE_IN_ROOT); "/" in production
    MaxSessions int                     // live sessions (default 256); more is an "error" event
    StreamWait time.Duration            // streams must attach within (default 15 s)
    Logf       func(string, ...any)
}
type Spawner interface {
    Start(argv0 string, argv []string, attr *os.ProcAttr) (*os.Process, <-chan unix.WaitStatus, error)
    Register(pid int) <-chan unix.WaitStatus // a process started elsewhere (the guest's FUSE relay, WP-0)
}
func New(o Options) *Core
func (c *Core) Handle(conn io.ReadWriteCloser)                   // one accepted connection
func (c *Core) Serve(accept func() (io.ReadWriteCloser, error)) error // until accept fails (EOF)
func PID1Spawner() Spawner  // wait4(-1) loop; statuses of unregistered pids are dropped
func ProcSpawner() Spawner  // per-process Wait (not PID 1)
```

Moved out of `guest/agent_linux.go`: `handle`, `control`, `send`, `session`, `reap` and `spawn`. Moved out of `guest/session_linux.go`: all of it. The fixes fold in as they move:

- **Session lifecycle.**
  - A session is deleted after `exited`, or after a stream timeout.
  - A stream Hello for an unknown session creates at most `MaxSessions`
    phantoms, which are pruned after `StreamWait`.
  - The `Session == 0` sentinel becomes an explicit `execReceived` flag.
  - The streams map is read under its lock.
- **Exec semantics.**
  - `CwdStrict` refuses a missing or non-directory cwd.
  - `Group` sends `kill(-pid)`.
  - `NoSync` skips the per-exit `unix.Sync()`.
  - `Merge` gives fd 2 = fd 1 on one pipe.
  - `NoStdin` gives stdin `/dev/null`.
  - `UID`/`GID` set `SysProcAttr.Credential`, with an empty supplementary
    group list.
  - `started` carries `Pid`. `exited` carries `Signal`.
- **Unchanged for old peers.** Backends and terminals send none of the new
  fields, so session 1, the per-exit sync and the fallback to `/` all behave
  as today.
- **File operations run inside the sandbox, as its root.**
  - Every path resolves with `openat2(RESOLVE_IN_ROOT)` under `Options.Root`,
    which is `/` in production. Symlinks resolve inside the sandbox, and the
    kernel enforces its read-only mounts.
  - Device nodes and sockets are refused.
  - `tar-put` extracts through `os.Root` (Go 1.25+) opened on the target
    directory, so a `..` or symlink entry can't write outside it.
  - `tar-get` stores symlinks as links and never follows them.
  - Everything is Go's `archive/tar`. The agent is a static binary with no
    tool dependency.

### 2.4 `bx __sbx-agent` (namespace mode)

`cmd/bx/sbxagent.go` parses `--fd N --lock M` and calls
`agentcore.RunNamespace(agentFD, lockFD)` (`agentcore/ns_linux.go`), which does
this in order:

1. Dup both fds with `O_CLOEXEC` and close the originals.
2. Set dumpable 0 and ignore the catchable signals.
3. Build a `Core` with `Configure: nil`, `Sync: syncfs(/)` and `PID1Spawner`.
4. `Serve(AcceptFrom(factory))`.
5. On EOF, `syncfs` and exit 0. PID 1 exiting tears down the pid namespace.

**It dies with its root filesystem, and its sessions die before it** (WP-3b):

- **fuse-overlayfs is watched.** Under a new `Spec.FuseWatch` (tile
  sandboxes only; terminals and backends keep today's daemonizing mount),
  the init starts fuse-overlayfs with `-f`, so it never forks away, and
  waits until the new root is a FUSE mount (`statfs` magic
  `0x65735546`, polled for at most 10 s; fuse-overlayfs exiting first fails
  the start with its output). The init appends `--fuse-pid P` to the
  entry's argv. The agent registers P with its `PID1Spawner` and, when P
  exits, exits with code **3**. Without this a dead fuse-overlayfs would
  leave PID 1 alive and a "running" sandbox whose every file access fails
  with `ENOTCONN`. xbind maps exit 3 to `stateDetail` "the sandbox's root
  filesystem (fuse-overlayfs) died".
- **User work is what the OOM killer picks.** agentcore gains
  `Options.SessionOOMScoreAdj`. The namespace agent passes 500, and so does
  the VM guest for sessions ≥ 2 (session 1, a backend's or a terminal's,
  keeps today's 0). The agent writes it to `/proc/<pid>/oom_score_adj` right
  after the session starts; raising it needs no privilege, and a failed
  write is logged, never fatal. The instant before the write only changes
  which process a simultaneous OOM would pick. The leaf keeps
  `memory.oom.group` 0, so an OOM kills a process, not the sandbox.

Its stdout and stderr go to xbind's log pipe: a 64 KiB ring per sandbox that
also holds the init's `[sbx]` trace.

The agent runs xbind's own code under every guard, as `__agent-host` does.
The agent does no FUSE service itself, so the vfork hazard that WP-0 fixes in
the guest can't arise here.

### 2.5 The resident shim (VM mode)

`vm.Apply(…, Options{Resident: true})` does four things differently from a
backend or terminal VM:

- It sets `hs.Resident` and ignores `hs.Guest`.
- It keeps `spec.Agent` and `spec.Lock`. `writeVMSpec` copies `AgentFD` into
  `hs.AgentFD`.
- It still refuses host networking, splices, links, devices and more than one
  lower.
- `Gateway` and `Listen` stay empty: there is no gateway in a tile sandbox.

After `ready`, `shim.run()` calls `s.resident()` (a new file,
`host/resident_linux.go`):

- **Connections from the factory.** The shim reads a bounded Hello (4 KiB).
  - A `ctl` becomes the upstream control connection; the newest wins and the
    old one is closed. The shim sends it `{"op":"ready"}` at once, because the
    guest is already configured.
  - A `stream` or `file` is dialled to the guest (`CONNECT 1024` + the same
    Hello), then spliced both ways with half-close propagated. The shim never
    parses what flows through.
  - A `listen` is refused, as are any other kinds.
- **One goroutine reads the guest's `ctl`** and forwards every event upstream
  (lines ≤ 64 KiB). This replaces `recvUntil`'s single pending reader.
- **Upstream `exec`, `resize`, `signal` and `sync`** go to the guest
  (lines ≤ 1 MiB). An `exec` for a session below 2 is refused with an error
  event.
- **SIGHUP or factory EOF:** the shim sends `{op:"sync"}` for session 0
  (flush only) and waits up to 2 s (6× emulated) for `synced`, then exits
  129.
- **VMM exit:** the shim exits 125 with the console tail on stderr.

Session 1 (backends, terminals) is unchanged. `serveListen`'s hard-coded
session 1 stays for backends.

### 2.6 Trust and bounds

- **xbind trusts nothing across these lines:**
  - the guest, from the shim's side;
  - the shim, which shares its jail with the VMM, from xbind's side;
  - the namespace agent, which sandbox code running as the same uid could
    replace by killing it.
- **Every line read is bounded:**
  - `RecvMax`: 64 KiB for events to xbind, 1 MiB for exec messages to the
    guest;
  - 4 KiB for Hellos;
  - 1 MiB per data frame.
- **What the agent reports is data**, handed through as bytes: exit codes,
  pids, stats, file bytes. xbind never uses it as a host path, never parses
  file content, and never lets it pick a session it didn't open.
- **Sessions are allocated by xbind**, from 2 per sandbox run, and are never
  reused within a run.
- **The fd-in-flight limit is per host uid.** A sandbox's root maps to
  xbind's uid, so a sandbox that stuffs sockets with fds can push every
  factory's `Dial` into `ETOOMANYREFS`. Any process with a socketpair can
  already do this, so the factory adds nothing; a `Dial` that fails this way
  is an `unavailable` answer for that call (and a failed start), never a
  hang.

## 3. The HTTP API (`/api/xbin`)

### 3.1 Conventions

- **Who.**
  - **Manager calls** need `p.Component != "" && p.Via == "instance" &&
    SandboxesFor(p.Component)`, checked on every call. The key is the
    principal's (tile, deployment), built only by `keyOf` (§1.4), so a
    manager only ever names its own sandboxes; a non-main deployment's
    principal gets 501 until its keys exist.
  - **Admins** (`brk.IsAdmin`) may list, stop and delete, with `?tile=` (and
    `&deployment=` later), and may read and write the policy.
  - **Everyone else gets 403.** That includes frame principals (even of the
    manager tile), terminal and cron tokens, and signed-in users.
  - **Without `--isolate`**, every manager route answers 501 `unsupported`
    ("tile sandboxes need isolation"), and `GET /sandboxes/runtime` says
    `isolation: false`.
- **Errors** use the contract's shape and enum: `{error, refusal, state?, etag?,
  retryAfterMs?}` with the contract's statuses (`invalid` 400, `not-allowed` 403, `not-found`
  404, `state`/`exists` 409, `lost` 410, `precondition` 412, `too-large` 413,
  `limit` 429, `unsupported` 501, `unavailable` 503).
- **Bodies.**
  - JSON is decoded **leniently**: unknown fields are ignored, as the
    contract asks, so a newer SDK works against an older xbind.
  - Bodies are capped with `MaxBytesReader`: 64 KiB for definitions,
    `stdinMax` + 64 KiB for `run`.
  - Times are unix ms. Sizes are bytes unless named.
- **Names** match `[a-z0-9][a-z0-9-]{0,31}`. `runtime`, `policy` and `copy`
  are reserved, because they are fixed route segments.
- **Ids** have grammars too, checked on every route before any lookup: exec
  ids `^[0-9a-f]{6}-[0-9]{1,12}$`, snapshot ids `^s-[0-9]{1,12}$`.
- **Path hygiene.** A request under `/sandboxes/` whose raw path
  (`r.URL.RawPath`, or `EscapedPath()`) has a `.` or `..` segment, or an
  encoded `/` (`%2F`), `.` (`%2E`) or `\` in any segment, is 400 `invalid`
  before routing reaches a handler. So is a `{name}`, `{id}` or `{sid}`
  path value that fails its grammar. Go's mux redirects a plain `..`, but
  an encoded one would otherwise arrive as one segment's value.
- **`?wait=<sec>`** is accepted on lifecycle calls (≤ `waitMaxSec`), as in the
  contract.
- **Audit.** The data plane is not audit-logged: `run`, `execs` POST,
  `stdin`/`signal`/`resize`, file writes, `tar` PUT and `copy` are left out,
  as the agent's drive routes are (`server.auditable`). Definitions,
  lifecycle, snapshots and the policy are logged.

### 3.2 Runtime and policy

```
GET  /sandboxes/runtime          manager → Runtime (what this tile may use now)
GET  /sandboxes/policy           admin   → {policy, stored}
PUT  /sandboxes/policy           admin   body: a partial Policy, merged onto the stored one → {policy, stored}
```

```jsonc
// Runtime
{"enabled": true, "isolation": true,
 "modes": [{"mode": "namespace"}, {"mode": "vm", "accel": "kvm"}],
 "unavailable": [{"mode": "vm", "reason": "an admin hasn't enabled VM tile sandboxes (vm policy: tiles)"}],
 "users": "any",                                   // "root": single-uid namespace host; VM mode is always "any"
 "egress": [{"class": "none", "reach": "none"},
            {"class": "class:internet", "slot": "internet", "ref": "internet", "reach": "internet",
             "rules": ["net:internet"], "note": ""}],
 "caps": ["exec", "files", "tar", "tty", "snapshots", "clone"],
 "limits": {"sandboxes": 8, "running": 4, "memMiB": 8192, "vcpus": 8, "diskGiB": 100,
            "perSandbox": {"memMiB": 2048, "vcpus": 2, "diskGiB": 20, "maxMemMiB": 8192, "maxVCPUs": 8, "maxDiskGiB": 200},
            "idleStopMin": 30, "runTimeoutMaxMs": 600000, "runOutputMax": 1048576, "execsRunning": 16,
            "outputRing": 1048576, "stdinMax": 1048576, "fileMax": 67108864, "tarMax": 1073741824, "waitMaxSec": 120,
            "flows": {"tcp": 1024, "udp": 256}},
 "used": {"sandboxes": 3, "running": 1, "memMiB": 2048, "vcpus": 2, "diskBytes": 5368709120}}
```

- `limits` are this tile's **effective** caps: its override applied, and
  `diskGiB` the cap its sandbox bytes are checked against (§6.1, §6.3).
- `limits.flows` is each sandbox's cap on concurrent relay flows (§4).
  The workspace-wide flow cap and `total` are the admin's (the policy and
  `GET /sandboxes` health), not the tile's.

```jsonc
// Policy (.xbin/sandboxes/policy.json; zero = default; PUT merges absent fields)
{"enabled": true,
 "perTile":    {"max": 8, "running": 4, "memMiB": 8192, "vcpus": 8, "diskGiB": 100},
 "perSandbox": {"memMiB": 2048, "vcpus": 2, "diskGiB": 20, "maxMemMiB": 8192, "maxVCPUs": 8, "maxDiskGiB": 200, "pids": 4096},
 "total":      {"memMiB": 0, "pids": 32768},   // every tile sandbox together (the comp-tilesbx parent, §6.2); memMiB 0 = ¾ of the host's RAM
 "idleStopMin": 30, "outputRingMiB": 1, "outputBudgetMiB": 64,
 "overrides": {"apps/coding-sandbox": {"perTile": {"max": 32}}}}   // per tile
```

`idleStopMin` may go up to 1440. `outputRingMiB` may go up to 8.
`outputBudgetMiB` is the ring memory a tile may hold across its execs; §6.4
explains it. `total` has no per-tile override.

- **An unreadable policy file fails closed.** Today (WP-13) an unreadable
  `.xbin/sandboxes/policy.json` silently falls back to the defaults, whose
  `enabled` is true, so a corrupt file would undo an admin's kill switch.
  WP-15b changes this: the error is logged once at load, shown in
  `GET /sandboxes/policy` (`error`) and in the admin view's health, and
  while it stands `enabled` is **false** (every start 503, "the sandboxes
  policy file is unreadable: …"). An admin's `PUT` replaces the file and
  clears the error.

### 3.3 Sandboxes

```
GET    /sandboxes                   manager → {sandboxes:[SandboxInfo]}   admin → today's registry view + tileSandboxes
POST   /sandboxes?wait=             manager → 201 SandboxInfo (200 when clientId repeats the same request; a clone may answer `creating`)
GET    /sandboxes/{name}            manager → SandboxInfo
PATCH  /sandboxes/{name}            manager → SandboxInfo (restartNeeded when a change waits for the next start)
DELETE /sandboxes/{name}            manager, admin (?tile=) → 204: stop, state moved to .trash (removed later, confined), forget
```

```jsonc
// POST body (PATCH takes the same fields but name/mode/from, plus "version")
{"name": "sb-7f3a",
 "mode": "vm",                                   // required: "namespace" | "vm" — never chosen for the manager
 "memMiB": 2048, "vcpus": 2, "diskGiB": 20,      // 0 = the policy default; clamped to the caps (the answer says what applied)
 "net": {"egress": "class:internet"},            // "none" (default) | "class:<sandbox-net slot>"
 "mounts": [{"res": "res:apps/coding-sandbox/work", "path": "shared", "at": "/mnt/shared", "ro": false},
            {"source": true, "at": "/opt/manager"}],
 "defaults": {"cwd": "/work", "uid": 1000, "gid": 1000, "shell": "/bin/bash",
              "env": {"HOME": "/home/dev", "SANDBOX_ID": "sb-7f3a"}},
 "labels": {"manager.v": "1"},                   // ≤ 1 KiB in all, opaque
 "for": "apps/agent", "forUser": "alice",        // claims: stored, shown as claims, widen nothing
 "idleStopMin": 30, "autoStart": true,
 "clientId": "c-5e1…", "start": false,
 "from": {"sandbox": "img-base", "snapshot": "s-2"}}   // clone (WP-20); same mode required
```

```jsonc
// SandboxInfo
{"name": "sb-7f3a", "state": "running", "stateDetail": "",
 "mode": "vm", "accel": "kvm",
 "memMiB": 2048, "vcpus": 2, "diskGiB": 20,
 "net": {"egress": "class:internet", "reach": "internet", "egressNext": "", "note": ""},
 "mounts": [/* as stored */], "defaults": {/* as stored */}, "labels": {}, "for": "apps/agent", "forUser": "alice",
 "idleStopMin": 30, "autoStart": true,
 "base": {"version": "v7", "outdated": false},
 "users": "any", "diskBytes": 734003200, "snapshots": 1, "execsRunning": 0,
 "created": 1790000000000, "started": 1790000100000, "lastActive": 1790000200000,
 "version": 7, "clientId": "c-5e1…", "restartNeeded": false}
```

- **`state`** is `creating`, `stopped`, `starting`, `running`, `stopping` or
  `error`.
  - `creating` is a clone whose copy is still running (§3.9). Every call
    but `GET`, list and `DELETE` answers 409 `state` until it ends in
    `stopped` (or `running`, with `start`), or in `error`.
  - `error` means the state can't be used: its base is missing, its overlay
    flavour changed, its state is missing (§1.3), or a clone was cut short
    by an xbind restart. `stateDetail` says why, and says what repairs it
    (`reset` or `rebase`; `DELETE` for a cut-short clone).
  - A failed start returns to `stopped`, with the failure in `stateDetail`.
  - A **busy** sandbox (a snapshot or restore copying) keeps its state and
    says so in `stateDetail` ("busy: taking snapshot s-3"); §3.9 says what
    waits and what answers 409.
- **`reach`** is the running relay's reach, or the class's reach now when the
  sandbox is stopped (§4). `egressNext` is set while a `PATCH`ed class waits
  for the next start.
- **`diskBytes`** counts allocated bytes. For a VM it is the disk image's
  blocks. For a namespace upper it is a confined `du` measured at each stop
  and, while it runs, every few minutes (§6.3). Snapshots are included.
- **Mounts.** `res` must be a `filesystem` resource **of the tile's own
  scope** that the tile holds: declared in its `uses` **and** granted (the
  policy ceiling applied), as `EnvFor` requires. A cross-scope resource is
  refused even when granted, since handing out another scope's path is
  what EnvFor never does (docs/resources.md); granted foreign resources are
  later work (the owner's call, D120 addendum). The writer role may mount
  read-write; the reader role forces read-only. `sqlite` and every other
  kind is `invalid`.
  - `path` is a relative, clean sub-path, resolved at start beneath the
    resource root without following symlinks (§5).
  - `at` must be absolute and clean. It must not be `/`, and must not lie
    under `/proc`, `/sys`, `/dev`, `/run/xbin` or `/opt/xbin`.
  - `{source:true}` is always read-only.

### 3.4 Lifecycle

```
POST /sandboxes/{name}/start?wait=   manager        → SandboxInfo
POST /sandboxes/{name}/stop?wait=    manager, admin → SandboxInfo (sync, then kill; state kept; execs → killed)
POST /sandboxes/{name}/reset?wait=   manager        → SandboxInfo (stops; puts the state aside for a confined rm; re-pins the current base)
POST /sandboxes/{name}/rebase?wait=  manager        → SandboxInfo (stops; keeps state; re-pins the current base — may break apt state)
```

- **Reset and rebase restart a sandbox that was running**: it is stopped
  (its execs `killed`), reset or re-pinned, and started again, so it is
  running afterwards; a stopped one stays stopped. Both keep the sandbox's
  snapshots (each pins its own base, §3.9). Reset renames `cur` to
  `.trash/<uid>.<rand>` and clears `Def.base`; the next start pins the
  current base into a fresh `cur`. Reset is idempotent and repairs `error`.
- An exec or file operation on a `stopped` sandbox with `autoStart` starts
  it and waits for it. The run's `timeoutMs` doesn't include the start.
- One that arrives while the sandbox is **`stopping`** waits for the stop to
  finish (at most `waitMaxSec`), then auto-starts it the same way (or
  answers 409 `state` without `autoStart`). One that arrives while it is
  `starting` waits for `running`.

### 3.5 `run`

`POST /sandboxes/{name}/run` takes the **contract's body and answers the
contract's result**, byte for byte:

- **The body:** `{cmd|argv, cwd, env, stdin, timeoutMs, maxOutput, merge}`,
  plus `uid`, `gid` and `forUser`.
- **The result:** `{exitCode, signal, timedOut, ms, stdout:{head, tail,
  elided, bytes}, stderr}`, or one `output` with `merge`.
- **Defaults.** `cmd` runs as `[defaults.shell || "/bin/sh", "-lc", cmd]`.
  `cwd` defaults to `defaults.cwd`, and a missing cwd is `invalid`.
- **Output.** Past `maxOutput` a stream keeps its first quarter in `head` and
  its last three quarters in `tail`. The text is UTF-8 with invalid bytes
  replaced.
- **Timeouts.** At `timeoutMs` the process group gets TERM, then KILL 5 s
  later. If the caller hangs up, the group is killed.
- **Bookkeeping.** A run isn't listed as an exec, but it counts against
  `execsRunning`.

### 3.6 Execs

```
GET    /sandboxes/{name}/execs                         → {execs:[Exec]}
POST   /sandboxes/{name}/execs                         → 201 Exec (200 on a clientId repeat)
GET    /sandboxes/{name}/execs/{id}                    → Exec
DELETE /sandboxes/{name}/execs/{id}                    → 204 (kills the group, forgets it)
GET    /sandboxes/{name}/execs/{id}/output?since=&max=&waitMs=&encoding=text|base64 → chunk
POST   /sandboxes/{name}/execs/{id}/stdin[?eof=1]      raw body → 204 (stdin:true execs and tty execs)
POST   /sandboxes/{name}/execs/{id}/signal             {signal: INT|TERM|KILL|HUP, group?} → 204 (group defaults to true)
POST   /sandboxes/{name}/execs/{id}/resize             {rows, cols} → 204 (tty)
```

- **The request** is the contract's: `{cmd|argv, cwd?, env?, tty?, rows?,
  cols?, stdin?, timeoutMs?, label?, clientId?}`. It also takes `uid`, `gid`
  and `forUser`.
- **The Exec** is the contract's: `{id, label, cmd, argv, cwd, tty, state
  (running|exited|killed|lost), exitCode, signal, started, ended, total,
  clientId}`. It also carries `forUser` and `uid`.
- **The output chunk** is the contract's: `{start, end, total, ringStart,
  data, encoding, state, exitCode, signal}`.
- **Output.** A non-tty exec runs with `Merge`, so it has one combined
  stream. A tty exec's stream is its PTY.
- **Timeout.** `timeoutMs` (0 = none) sends TERM to the group, then KILL
  after 5 s.
- **Signals.** `group` defaults to **true**, as in the contract. The body is
  decoded into `Group *bool`; nil means true, so a forwarded
  `{"signal":"INT"}` reaches the whole process group, not only its leader.
- **Ids** are `<boot>-<n>`. `<boot>` is 6 hex characters, random per xbind
  start. An id from another boot answers 410 `lost`; that is the only
  `lost`.
- **A stop ends execs as `killed`.** Any exec running when its sandbox
  stops, for whatever reason (§7's `teardown`), becomes `killed` with
  `signal: "KILL"` and `exitCode` null, as the contract says for a stop. Its
  record and its ring stay, under the retention below, so a reader can
  still fetch the output up to the end.
- **Retention.** A finished exec is kept at least an hour, or the last 50 per
  sandbox.
- **`clientId`** is per sandbox and deduplicates in memory. It is lost with
  the exec.

### 3.7 The TTY WebSocket

```
GET /sandboxes/{name}/execs/{id}/tty?sessionId=&sandboxId=&forUser=             attach
GET /sandboxes/{name}/tty?cwd=&cmd=&rows=&cols=&uid=&gid=&forUser=&sessionId=&sandboxId=   start (login shell unless cmd) + attach
```

- **The wire** is exactly `/ws/term`'s (`internal/termwire`):
  - Binary frames carry terminal bytes both ways. The ring's tail replays
    first, at most 256 KiB.
  - The server sends `{"op":"session","id":…,"sandbox":…,"echoAck":true}`
    first, then acks and pongs per socket.
  - When the exec ends, `{"op":"exit","code":N}`, with `code` null and a
    `signal` field when a signal ended it.
  - The client sends `resize` and `ping`. Unknown ops are ignored.
- **`sessionId` and `sandboxId`** (`[A-Za-z0-9._-]{1,64}`) replace the `id`
  and `sandbox` in the session frame. A manager relaying bytes can then show
  the consumer its own contract ids.
- **Reaching it.** It is a GET under `/api/xbin`, so it goes through no audit
  writer and can be hijacked. It is reachable over the gateway socket, like
  every other route.
- **Limits.** `SetReadLimit(1 MiB)` (a paste arrives as one frame). While
  any client is attached (the hub's `OnClients` > 0), the sandbox is never
  idle (§7); a detached TTY exec with no traffic is.
- **D88.** A tty exec is refused (403 `not-allowed`) when its `forUser`, or
  the attach's `forUser`, names a user with `noTerminal`. The check is
  repeated at every attach. Turning `noTerminal` on kills the tty execs
  claimed for that user (`OnNoTerminal`). Non-tty execs aren't restricted;
  the docs say so.
- **What it can't reach.** No human principal reaches this route; the
  manager relays it (D120). An admin can list, stop and delete a tile's
  sandboxes but can never exec into them or attach.

### 3.8 Files, tar, copy

The contract's routes and semantics:

```
GET  /sandboxes/{name}/files/stat?path=
GET  /sandboxes/{name}/files/content?path=&offset=&length=              → bytes + ETag
PUT  /sandboxes/{name}/files/content?path=&mode=&mkdirs=1&ifMatch=&ifNoneMatch=*   raw body → stat
GET  /sandboxes/{name}/files/list?path=&limit=
POST /sandboxes/{name}/files/mkdir   {path, parents}      POST …/remove {path, recursive}   POST …/move {from, to, overwrite}
GET  /sandboxes/{name}/tar?path=&exclude=…                → application/x-tar
PUT  /sandboxes/{name}/tar?path=&mkdirs=1                 tar body → 204
POST /sandboxes/copy   {from:{sandbox, path}, to:{sandbox, path}, overwrite}   → 204 (a tar-get spliced into a tar-put)
```

- Each call is one `file` connection to the agent. Data moves in frames,
  which xbind turns into HTTP bodies and back.
- Created files are owned by `defaults.uid`/`gid`.
- `fileMax` and `tarMax` give 413.
- `copy` works only between sandboxes of the caller's own key.
- The `ETag` header of a content read and the `etag` of a stat are the same
  string (quoted in the header, bare in JSON), so `ifMatch` works with
  either; the SDK trims the quotes (WP-14).
- A file, tar or copy operation in flight holds the idle timer off (§7).

### 3.9 Snapshots and clones (WP-20)

```
GET    /sandboxes/{name}/snapshots                        → {snapshots:[{id, name, created, bytes, pending?}]}
POST   /sandboxes/{name}/snapshots?wait= {name, clientId} → 201 (stops the sandbox; restarts it if it ran) | 202 {…, pending: true}
POST   /sandboxes/{name}/snapshots/{sid}/restore?wait=    → SandboxInfo (execs killed)
DELETE /sandboxes/{name}/snapshots/{sid}                  → 204
```

- **Ids** are `s-<n>`, from the definition's `snapSeq`, never reused within
  a uid.
- **A snapshot pins its base.** Its dir carries `base` and `overlay` stamps
  (`layers.Stamp`) and a `meta.json` (`{id, name, created, mode, base,
  overlay, bytes, clientId}`). `layers.Pinned` reads those stamps (WP-7), so
  GC keeps the base of every snapshot, including one whose sandbox was
  reset or rebased since, and one taken before an xbind upgrade shipped a
  new base.
- **Restore brings the state back as it was, base included.** It re-pins the
  sandbox to the snapshot's base: the staged `cur` carries the snapshot's
  stamps, and `Def.base` follows. A clone takes its snapshot's base the same
  way. Only a **mode** mismatch is refused (`invalid`), and in namespace
  mode an **overlay-flavour** mismatch (an upper written by fuse-overlayfs
  can't be read by a kernel overlay, or the other way round). A snapshot
  whose base is no longer installed can't be restored (`invalid`, naming
  the base); since GC keeps pinned bases, that happens only when someone
  removed it by hand.
- **Reset and rebase keep snapshots.**
- **Copies are staged, never torn, never synchronous past `wait`.**
  - Every copy lands in a fresh `tmp/<rand>/` and is renamed into place
    whole: a snapshot to `snapshots/<sid>/` (with `meta.json` written before
    the rename, so an incomplete one never lists), a restore swapped with
    `cur` (`RENAME_EXCHANGE`, §1.3), a clone to the new sandbox's `cur/`.
  - Uppers are copied by `confine.CopyTree` (`cp -a --preserve=xattr
    --reflink=auto`, `FSCaps`): naming `xattr` makes a failure to copy one an
    error. `cp -a` alone drops them silently, and both overlay flavours keep
    opaque-directory markers and ownership overrides in `user.*` xattrs
    (fuse-overlayfs's, or a `userxattr` kernel overlay's). VM disks go
    through `fsutil.CloneSparse`.
  - The copy runs off the request. `?wait=<sec>` (≤ `waitMaxSec`, default
    `waitMaxSec`) bounds how long the call waits for it. Done in time: the
    normal answer. Not done: a snapshot answers **202** with `pending:
    true`, a restore answers the SandboxInfo with its busy `stateDetail`,
    and a clone answers 201 with state `creating`. The caller polls `GET`.
  - While a snapshot or restore copies, the sandbox is **busy**: start,
    stop, reset, rebase, restore, another snapshot, `DELETE` and every exec
    or file call answer 409 `state` with the busy `stateDetail` and a
    `retryAfterMs`. An auto-starting exec or file call waits for it like a
    `stopping` sandbox (§3.4).
  - At boot, every `tmp/` is emptied, a snapshot dir without `meta.json`
    is removed, and a definition still `pending: "clone"` comes back
    `error` ("the clone was cut short by an xbind restart — delete it and
    clone again").
- **Clones.** A clone is `POST /sandboxes` with `from: {sandbox,
  snapshot?}`, same key and mode.
  - With `snapshot`, it copies that snapshot, whatever the source's state.
  - Without `snapshot`, it copies the source's current `cur`, and only
    while the source is `stopped`: a running (or `starting`, `stopping`,
    busy) source answers 409 `state` ("stop it, or clone a snapshot"). A
    live upper or disk would copy torn; an implicit snapshot would stop the
    source's work from inside someone else's create.
  - The new definition counts against `perTile.max` from the moment it is
    stored (`creating`; the count is checked under the definitions mutex,
    as for any create). The clone is admitted only when the tile's sandbox
    bytes plus the source's (the snapshot's `bytes`, or the source's last
    `diskBytes`) stay within `perTile.diskGiB`, else 429 `limit`; the same
    check guards a snapshot.

### 3.10 The admin

- **`GET /sandboxes`** for an admin keeps today's registry view.
  - Running tile sandboxes appear as kind `tile` rows: `name`, `for`,
    `forUser`, their own leaf and stats.
  - It gains `tileSandboxes: [{tile, name, state, mode, accel, memMiB, vcpus,
    diskGiB, diskBytes, for, forUser, lastActive, tileExists}]`, so stopped
    sandboxes, and those of removed tiles, can be cleaned up.
- **`disks`** includes VM disks under `.xbin/sbx`.
- **Admin actions** are `POST …/stop?tile=` and `DELETE …?tile=`, and nothing
  else.
- **Health** (`GET /sandboxes` `health.tileSandboxes`): the policy file's
  error, if any (§3.2); the `total` book (`memMiB`, `pids` used of the
  parent's caps); the workspace flow budget (used of its cap, §4); whether
  starts are held for low disk (§6.3); and the `.trash` backlog (entries,
  bytes last measured).

## 4. Egress: the `sandbox-net` interface kind

A manager's backend shouldn't hold the internet only because its sandboxes
need it (D120). So a class of sandbox network is **a request-side interface
slot of a new kind** that the manager declares and an approver binds:

```jsonc
"interfaces": {
  "internet": {"kind": "sandbox-net"},    // bound to "internet" or "internet:<hosts>"
  "lab":      {"kind": "sandbox-net"}     // bound by an operator to lan:<cidr> | org | personal | set:<name>
}
```

- **Binding** uses today's route, `POST /bindings {component, slot, provider}`.
  - The allowed refs are `none`, `internet`, `internet:<spec,…>`,
    `lan:<cidr>`, `org`, `personal` and `set:<name>`.
  - Refused: `host`, provider tiles, a set whose rules say host, `#instance`,
    and more than one ref.
  - `validateBinding`, `bindOptions`, the D20 deny row, D54 org-set coverage,
    D65 (sets are a workspace-admin act), D26 org-admin approval within the
    allowance, D88 personal self-approval, allowances and inert notes all
    apply. They reach the new kind by treating it like `net` in
    `bindingTargetsPaired`.
- **An unbound slot is `none`.** There is no D54/D88 auto-default: a class
  never silently gets the org network.
- **Resolution.** `broker.SandboxEgress(tile, "class:<slot>")` resolves the
  class at every start into `{ref, reach, rules, note, policy}`.
  - The `reach` vocabulary is the contract's (`EgressPolicy.Reach()`): `none`
    when empty; `internet` when every rule is internet, a host rule or a
    wholly public prefix; `open` otherwise.
  - **"Internet" is strict for tile sandboxes** (the owner's call, D120
    addendum). The contract promises `internet` = "no private or local
    networks", but today's `isPublic` counts `100.64.0.0/10` (CGNAT, which
    Tailscale uses), `198.18.0.0/15` (benchmarking), `240.0.0.0/4`
    (reserved) and `64:ff9b::/96` (NAT64) as public, so an `internet` class
    would reach tailnet peers or a local NAT64 gateway. A tile sandbox's
    policy is built with `EgressPolicy.Strict()`, whose internet test also
    refuses those four ranges (and `64:ff9b:1::/48`, RFC 8215's local-use
    NAT64, the same kind of range). `Allow`, `Reach()` ("wholly public") and
    the relay's DNS pins (`relay.Config.StrictPublic`) all use it.
    **Tile backends' `net:internet` is unchanged**: narrowing it would
    change existing tiles' egress (never break users).
  - `SandboxNetClasses(tile)` lists the classes, with their strict reach.
    `GET /sandboxes/runtime` returns them, and a manager builds
    `hello.egress` from them (§11).
- **The relay is always there.** `Spec.Net = "relay"` in both modes, which
  keeps the modes identical. The config:

  ```go
  relay.Config{TunFD: fd, Allow: pol.Allow /* never nil; pol = class.Policy.Strict() */,
      Resolver: HostResolver() /* "" when pol.Empty() */, DNSRefuse: pol.Empty(),
      Gateway: 10.0.2.2 /* no HostFwd: a dead end */, Deny: relay.HostDeny(xbindListen...),
      StrictPublic: true, MaxTCP: 1024, MaxUDP: 256, Budget: rt.flowBudget /* shared, §4 flows */,
      Processors: 1 /* §14 */}
  ```

  plus `AllowHost` only when the policy has host rules. There is no
  `Published`, `HairpinDial`, `HostFwd`, `HostDial` or gateway socket, and
  `vm.Options.Gateway` is `""`. `xbindListen` is every address xbind
  listens on (its HTTP listener's, plus any extra listener's, a wildcard
  bind included, so `::` or `0.0.0.0` becomes "every host address" and is
  covered by the locality check anyway). The caller owns the TUN fd and
  closes it **after** `Relay.Close` returns (Close stops the readers first).
- **What `Deny` blocks**, before every other check (TCP, UDP, ICMP, and flows
  to pinned hosts):
  - loopback, link-local, unspecified and multicast addresses;
  - `10.0.2.0/24`;
  - **every address the host would deliver locally, decided per flow**: an
    `RTM_GETROUTE` for the destination in xbind's network namespace, the
    same lookup the dial would make, refusing `RTN_LOCAL`, `RTN_BROADCAST`,
    `RTN_MULTICAST` and `RTN_ANYCAST`. An unreachable answer is not local
    (the flow goes on to `Allow`); any other error denies. An address that
    appears after the relay started (a VPN, docker0, a rotated IPv6
    temporary address) is refused on its first flow, with no window.
    (WP-10 built a set re-read at most every 5 s; WP-10b replaces it on
    Linux and keeps it elsewhere.)
  - xbind's listen addresses.

  So `internet` can't reach the host's own public address, and `lan:` can't
  reach loopback. **A sandbox has no route to xbind.** One gap is accepted:
  a public address that reaches the host only through 1:1 NAT (a cloud
  elastic IP) is on no interface, so a sandbox with internet can hairpin to
  xbind's public face — as any internet client can.
- **Flow caps.** The relay runs inside xbind, so its flows are xbind's fds
  and memory. Each tile-sandbox relay admits at most `MaxTCP` (1024)
  concurrent TCP and `MaxUDP` (256) UDP flows; past that a TCP SYN gets a
  RST and a UDP datagram an ICMP port-unreachable, at once. One
  `relay.Budget` shared by every tile-sandbox relay caps them together at
  `min(16384, RLIMIT_NOFILE/4)`. The per-sandbox caps are
  `Runtime.limits.flows`; the shared one is in the admin health. Backends'
  and terminals' relays pass no caps (today's behaviour). A closed relay
  releases its UDP flows at once rather than after their 30 s idle timeout,
  so a stopped sandbox gives its budget back.
- **`none`** is an empty policy with `DNSRefuse`. TCP gets a reset at once,
  DNS answers REFUSED, and nothing waits on a timeout.
- **DNS under a non-empty class** is forwarded to the host resolver, even
  for a `lan:`-only class. Its reach is `open`, and DNS tunnelling is within
  what `open` promises. Only `none` refuses DNS.
- **Changes.** `OnSandboxNetChange(tile)` fires on a bind, an unbind, a
  network-set edit, an org network-set attachment, a personal-holder change
  or a transfer (WP-11). WP-15a wires it in boot; it re-resolves each
  running sandbox's class with `SandboxEgress`. A D20 policy-row edit fires
  nothing, so the runtime also re-resolves on the hub's `users` events
  (WP-15b).
  - When the new rules are **not a superset** of the running ones
    (`!new.Policy.Covers(running)`), the sandbox is synced and stopped
    (state kept; `stateDetail` says why). The relay can't cut flows it has
    admitted.
  - When they are a superset, `egressNext` is set, and the new policy applies
    at the next start.
  - The manager's backend is never restarted for a `sandbox-net` change.
- **One `net` slot.** A tile's own `net` slot is now picked deterministically
  (sorted slot names). Two `net` slots used to resolve in map order.

## 5. Mounts

- **Resolution.** `broker.ResourceMount(key, res)` returns `{src, role, kind,
  encrypted, ready}`, using the same path resolution as `EnvFor`.
  - **Same scope only in v1** (the owner's call, D120 addendum): a resource
    of another scope is refused even when granted. Granted foreign
    resources come later, with their own design.
  - The tile must **hold** it the way `EnvFor` requires: a `uses` entry
    **and** the grant. WP-13's `ResourceMount` accepts a workspace grant
    without a `uses` entry; WP-13b adds the `uses` check, so a grant left
    over after a manifest dropped the entry mounts nothing.
  - A resource the tile doesn't hold, or holds as something other than a
    `filesystem`, is refused.
  - An encrypted resource needs the vault unsealed and a mounted gocryptfs
    view. While the vault is sealed, a start answers 503 `unavailable`, as
    backends are held.
- **Running sandboxes follow the grant, not only the next start.** A
  sandbox kept up by a non-tty exec may never restart, so re-checking at
  start isn't enough:
  - **Vault seal.** `SealResources` calls the runtime's
    `StopWhere(func(Key, *Def) bool)` for every running sandbox with a
    `res` mount (every filesystem resource is encrypted), and waits for
    those stops, **before** `resenc.UnmountAll`. A sandbox's own bind of the
    gocryptfs view (or the VM shim's export of it) would otherwise keep the
    superblock and its daemon alive, and serve plaintext after the seal. The
    stops are synced; `stateDetail` says "the vault was sealed". Their next
    start answers 503 until the vault is unsealed.
  - **A single-tenant flip.** The `cap:containers` remount path
    (`grantRestart` → `MountEncrypted`) first stops, the same way, the
    running sandboxes whose mounts are in that tile's scope, since their
    binds hold the old view.
  - **Grant changes and deleted resources.** A `res:` grant approve or revoke
    (`grantRestart`), a registry rescan (a manifest's `uses` or `resources`
    changed, a resource deleted) and a `users` event (a ceiling change) fire
    the runtime's `OnResourceChange(tile)` (every tile with a running
    sandbox that has `res` mounts, for a rescan or `users` event). It
    re-resolves each running sandbox's mounts through `ResourceMount`. A
    mount that is no longer held, or a read-write mount whose role dropped
    to reader, syncs and stops the sandbox, state kept, with `stateDetail`
    naming the mount: the same rule §4 applies when egress narrows. A role
    that widened waits for the next start.
- **The bind.** A mount becomes `sandbox.Bind{Src: <trusted resource root>,
  Sub: <path>, Dst: <at>, RO: ro || role == reader}`.
  - The init resolves `Sub` beneath `Src` with `openat2(RESOLVE_BENEATH |
    RESOLVE_NO_SYMLINKS)` and binds through `/proc/self/fd/N`. A symlink
    planted anywhere in the sub-path is refused.
  - `Dst` is created inside the new root with no-follow, component-by-component
    mount points, so a symlink in the sandbox's upper can't redirect it. This
    is `Spec.NoFollow` (WP-2, opt-in; tilesbx always sets it), with
    dl/confine-dirfrom's helpers copied into `nofollow_linux.go` until that
    branch lands.
- **In VM mode** the same binds become FUSE exports, served by the shim's
  confined file server. Guest-side mount points live in the guest.
- **`{source:true}`** binds `CodeRoot(key)` (the tile dir) read-only.
- **Never mounted:** homes, the workspace view, `.xbin`, `data/` outside a
  resource, the gateway socket, tokens, vault contents, devices. The static
  `bx` is bound read-only at `/opt/xbin/bin/bx` in namespace mode. VM mode
  doesn't need it: the guest runs its own agent.

## 6. Resource accounting

### 6.1 Admission

**At create** (and clone), under the definitions mutex:

- the tile's definitions, this one included, ≤ `perTile.max` (summed across
  the tile's deployments once there are several);
- the requested sizes are clamped to the per-sandbox caps;
- the mode is available now (`invalid` names why);
- in VM mode, the sum of declared disks ≤ `perTile.diskGiB`.

**At start** (every start re-checks), as one atomic booking:

- the policy is `enabled` (and its file readable, §3.2), else 503;
- the workspace disk isn't low (§6.3), else 503 `unavailable`;
- `diskBytes` summed over the tile's sandboxes and snapshots ≤
  `perTile.diskGiB`, else 429. Sandbox bytes count against this cap
  **only**, never against the scope's resource-write quota (diskmon's
  per-scope default is 50 GiB, below `perTile.diskGiB`'s 100, so counting
  them there would block the manager's own kv writes). They still count
  for disk pressure (§6.3);
- then the **book**: each tile has one (summed across its deployments)
  holding `running`, `memMiB` and `vcpus`, under the book's own mutex.
  `reserve(tile, def)` checks, with this sandbox included, running ≤
  `perTile.running`, the `memMiB` sum ≤ `perTile.memMiB` and the `vcpus` sum
  ≤ `perTile.vcpus`, plus the workspace-wide `total` book (§6.2), and
  increments them in the same critical section, as `vm.Reserve` does.
  Anything over is 429 `limit` naming the cap. It returns an idempotent
  `release`, which the start's unwind and `teardown` (§7) call. Ten
  concurrent starts under `running: 4` give exactly four sandboxes.

**VM mode, in addition:** `vm.Reserve(tile, memMiB, vm.TileSandbox())`
checks the global VM count and budget and books against the **tile
sub-budget**, `tilesBudgetMiB`, which defaults to half of `budgetMiB`. Its
refusals are marked `sbx.Refuse`. `usedTiles` is shown next to `used` in the
admin view.

### 6.2 cgroups

- **A parent for every tile sandbox: `comp-tilesbx-<ws8>/`** under xbind's
  delegated cgroup, where `<ws8>` is the first 8 hex of the sha256 of the
  workspace's absolute path (two xbinds sharing a delegated cgroup never
  touch each other's). Its `memory.max` is `policy.total.memMiB` (default ¾
  of the host's RAM) and its `pids.max` is `total.pids` (32768); it enables
  `+cpu +memory +pids` for its children. This is the workspace-wide ceiling
  namespace mode lacked (several managers with overrides could otherwise
  oversubscribe host RAM); admission books the same totals so a start is
  refused (429) before the kernel would have to OOM. Its leaves never share
  a namespace with backends' `comp-<CompKey>` leaves, so a tile at path
  `sbx-foo` is safe from the sweep.
- **One leaf per running sandbox** (`sbx-<CK>-<name>`) inside that parent.
  It is prepared with its limits **before** the init is started, and the
  init is started straight into it: `cgroup.Prepare(name, Limits)` returns an
  `O_DIRECTORY` fd, the caller sets `SysProcAttr.UseCgroupFD`/`CgroupFD`
  (clone3 `CLONE_INTO_CGROUP`) and closes the fd after `cmd.Start`, so
  nothing the init forks ever runs outside the leaf. Where clone3 can't do
  that (a kernel before 5.7: `ENOSYS`/`EINVAL`), the start retries once
  without it and joins the pid with `AddWith` right after `cmd.Start`,
  before `SetupUserns`, as the runner does. The limits:

  | | namespace | VM |
  |---|---|---|
  | `memory.max` | `memMiB` + 128 MiB (agent, fuse-overlayfs) | `memMiB` + `OverheadMiB` (192, or 512 emulated) |
  | `memory.high` | 7/8 of max | unset |
  | `pids.max` | `perSandbox.pids` (4096) | 512 |
  | `cpu.max` | `vcpus` × 100 ms per 100 ms | (`vcpus` + 1) × 100 ms |
  | `cpu.weight` | 100 | 100 |

- **The relay runs in xbind** and is counted there: about 200–300 KiB idle on
  a small host.
- `memory.oom.group` stays 0: an OOM kills a process (a session first,
  §2.4), not the sandbox. Before a leaf is removed, `teardown` reads its
  `memory.events` `oom_kill` count; a non-zero count goes into
  `stateDetail` ("out of memory: N processes were killed") when the
  sandbox ended on its own.
- **Stop:** `cgroup.Kill` (`cgroup.kill`, else a SIGKILL per pid), then
  `Remove`.
- **Boot:** the parent's `Sweep` kills and removes every leaf inside
  `comp-tilesbx-<ws8>/` and nothing outside it.
- **Without cgroup delegation** everything still works, without limits. The
  registry's `Leaf` is `""`, as for terminals.
- **With dev-lifecycle.** When its per-tile parent (`tile-<key>/d-<name>`)
  lands, tile-sandbox leaves stay in `comp-tilesbx-<ws8>/`, a **sibling**
  subtree sized from the sandboxes policy, not under the backend's limited
  parent. Otherwise they would share its `memory.max`, and the admin view's
  per-tile stats would count them twice.

### 6.3 Disk

- **A VM disk** is sparse, sized by `diskGiB` (`EnsureDiskAt`, grow only). A
  PATCH to a smaller size is `invalid`. Growing takes effect at the next
  start (the guest's `resize2fs`). It is counted in allocated blocks.
- **A namespace upper has no hard cap**, so it is watched while it runs:
  - It is counted by a **confined** `du` (`confine.DiskUsage`, the file-caps
    profile, so sub-uid-owned dirs count too) at each stop, **and while it
    runs**: one measuring worker takes the running namespace sandbox whose
    measurement is oldest, one `du` at a time, never the same sandbox twice
    within 2 minutes (a `time.AfterFunc` chain, no ticker).
  - A tile whose measured bytes pass `perTile.diskGiB` while running has
    its largest running namespace sandbox synced and stopped, state kept
    (`stateDetail`: "the tile's sandboxes use X GiB, over its N GiB
    (sandboxes policy: perTile.diskGiB)").
  - **Low disk.** While any namespace sandbox runs, the runtime also
    `statfs`es the workspace partition every 5 s (cheap: no walk) against
    diskmon's reserve, and listens to diskmon's own low-disk verdict (its
    45 s scan). When the disk is low, starts answer 503 `unavailable`
    ("the workspace disk is low"), and running namespace sandboxes are
    synced and stopped, largest tile first (by measured bytes), every tile
    above diskmon's fair share. Stopping frees nothing, but it stops the
    writer. VM sandboxes keep running: their disks are bounded by
    `diskGiB`. Starts resume when free space is back above the reserve.
  - The per-tile cap and the partition's pressure include sandbox bytes
    (§9); the scope's resource-write quota doesn't (§6.1).

### 6.4 Memory in xbind

- **Rings grow lazily** up to `outputRingMiB` per exec.
- **A tile's rings share `outputBudgetMiB`** (64 MiB). When the budget is
  full, the oldest finished exec's data is dropped first; its `ringStart`
  moves, which the contract allows.
- **A TTY hub** keeps a 256 KiB replay.
- **`run` keeps nothing** past its response.

## 7. Lifecycle and restart semantics

**Start** runs under a single flight per sandbox. The single flight never
holds the definitions mutex `m.mu` across slow work: WP-13's `ServeCreate`
and `ServeDelete` call `start`/`stop` under `m.mu`, which WP-15a moves out
(take what's needed under `m.mu`, release it, then run the transition in the
sandbox's flight).

1. Admission and the book (§6.1): `release := reserve(tile, def)`.
2. Take the lock: `flock(<state>/lock, LOCK_EX|LOCK_NB)`.
   - If it's busy, an orphan from a crashed xbind may still be dying. Wait up
     to 5 s, and also for the old leaf's `cgroup.events populated 0`.
   - If it's still busy, the start fails with a `state` refusal.
3. Pin the base and flavour of `cur/` (`layers.Pin`).
   - A missing base, a changed overlay flavour, or a missing `cur/` while
     `Def.base` is set (§1.3) sets `error`. Unlike terminals, this never
     gates xbind's boot.
4. Resolve the mounts and the egress class (`SandboxEgress`, made
   `Strict()`).
5. Build the `Spec`: `Lower: [base]`, `Upper`/`Work` (namespace mode,
   `cur/upper`, `cur/work`), `Restricted`, `MountGuard`, **`NoFollow`** (§5:
   mount points in the upper are never followed; it is opt-in, WP-2),
   **`FuseWatch`** (§2.4, WP-3b), `Hostname: name`, `Net: "relay"`, `Agent`,
   `Lock`, binds, and `Entry: /opt/xbin/bin/bx`.
   - In VM mode, `vm.Apply(…, Resident)` also runs (it copies `AgentFD` into
     `HostSpec.AgentFD`, WP-4), along with `vm.Reserve(tile, memMiB,
     vm.TileSandbox())` and `EnsureDiskAt(cur)`.
6. `cgroup.Prepare` the leaf (§6.2), `Launch`, set `UseCgroupFD`,
   `cmd.Start`, close the leaf fd, `SetupUserns`, `RecvTUN`, `relay.Start`
   (§4's config), `Handle.Started`. From here the **watcher** runs: one
   goroutine on `cmd.Wait()`.
7. Dial `ctl` and wait for `ready`: 30 s in namespace mode, 60 s for a VM, and
   3× that under emulation. The `ctl` reader's EOF also triggers the
   watcher's path.
8. Register with `sbx.Add` (with `Name`, `For`, `ForUser`), arm the idle
   timer, and set the state to `running`.

Any failure along the way unwinds (the steps done so far, in reverse,
`release()` included), records `sbx.Fail(…, StageOf(err, Start))` and returns
to `stopped` with `stateDetail`. If `cmd.Start` fails, the caller closes
`Agent` and `Lock` itself (WP-2's ownership rule). VM mode never falls back
to namespace mode.

**Every way a sandbox ends goes through one `teardown(reason)`**, run once
per run (a `sync.Once` on the run's record), whether a stop, an idle stop, a
policy flip, a revoke, low disk, the watcher (the init exited: the agent
crashed, exited 3 because fuse-overlayfs died, was OOM-killed, or the VMM
exited 125) or the `ctl` EOF started it:

1. Close the relay (`Relay.Close` returns once its readers stopped), then
   close xbind's TUN fd.
2. `cgroup.Kill` the leaf, read its `memory.events` `oom_kill`, and remove
   it (`Remove`).
3. Release the VM reservation and the book (`release()`).
4. Remove the registry row (`sbx.Remove`), close the factory and the `ctl`
   connection, and stop the idle timer.
5. Mark every running exec `killed` (§3.6) and end its TTY hub
   (`ExitSignal("KILL")`).
6. Measure the upper (namespace) or the disk's blocks (VM), set `stopped`,
   and set `stateDetail` from the reason: "" for a stop the manager asked
   for; "the sandbox's agent exited (code N)"; "the root filesystem
   (fuse-overlayfs) died" (exit 3); "out of memory: N processes were
   killed"; "the VM exited: <console tail>"; or the runtime's own reason
   (idle, revoke, seal, egress narrowed, low disk, over the disk cap, a
   policy flip).

**Stop** is how the runtime asks a sandbox to end:

1. Refuse new execs and file operations: they wait (§3.4), or answer 409
   `state` without `autoStart`.
2. Send `{op:"sync"}` and wait for `synced`, at most 5 s. Namespace mode
   answers with `syncfs`; VM mode flushes the guest.
3. Kill the process.
   - Namespace mode: SIGKILL the init.
   - VM mode: SIGHUP the shim, which syncs and exits 129. After 10 s, SIGKILL.
4. The watcher sees the exit and runs `teardown(reason)`; the stop returns
   when it has finished (or after `?wait`).

`StopWhere(pred)`, `StopTile(tile, why)` and `StopAll(why)` are stops over a
set, run in parallel, each idempotent; `StopTile` is safe to call from any
hook and returns at once when nothing of the tile runs.

**Other transitions:**

- **Idle stop.** Every activity records a time. Activity is:
  - an API call on the sandbox;
  - exec output or input;
  - a TTY attach, a detach or a frame;
  - a file operation.

  A `time.AfterFunc(idleStopMin)` timer re-arms itself for whatever remains
  of the interval (no tickers). When it fires, the sandbox stops **unless**
  any of these is in flight: a non-tty exec, a `run`, a file, tar or copy
  operation (a quiet `run` longer than `idleStopMin`, or a long tar stream,
  is work, not idleness), or an attached TTY client (termwire's
  `OnClients` > 0). The timer then re-arms for a full interval.

  A detached TTY exec with no traffic counts as idle. Background work
  belongs in non-tty execs, bounded by their `timeoutMs` and the caps.
- **Blue/green.** Both generations of the manager address one set. Creates
  and execs are idempotent by `clientId`. Lifecycle transitions take a
  per-sandbox lock. A new generation resumes output reads from the offset it
  saved.
- **An xbind restart or crash.**
  - Factory EOF kills every sandbox (§1.2). A running VM gets a best-effort
    guest sync first.
  - Definitions persist. After boot every sandbox is `stopped`, and exec ids
    from the old boot answer `lost`.
  - Graceful shutdown runs `StopAll` (with sync, 15 s in all) next to
    `run.StopAll()`.
  - At boot, the `comp-tilesbx-<ws8>/` sweep clears stale leaves, every
    `tmp/` is emptied, `.trash` is re-queued, and a `pending` clone becomes
    `error` (§3.9). The per-sandbox lock stops a new start from mounting an
    upper that a dying orphan still has mounted.
- **Policy flips.**
  - Sandboxes `enabled → false` (or an unreadable policy file, §3.2) stops
    every tile sandbox (state kept) and refuses starts.
  - VM `tiles → false`, or `tilesEmulated → false` while VMs are emulated,
    stops VM-mode sandboxes. Their next start is refused with the reason.
    The hook is `boot.(*State).putVMPolicy`, the only place the VM policy
    changes, which holds the old stored policy (WP-16).
  - Lowered sizes apply at the next start. A running sandbox shows
    `restartNeeded`.
- **Revoking `cap:sandboxes`** (`Broker.OnCapChange(tile, cap, held)`) stops
  the tile's sandboxes (state kept). The hook fires on the request goroutine,
  on approves too (`held` is the state after the change), and repeatedly
  from `capSweep` on every `users` event, so the wiring filters on `cap ==
  broker.SandboxesCap && !held` and calls `go rt.StopTile(tile, …)`:
  non-blocking and idempotent. Every call then answers 403 (`SandboxesFor`
  is checked per call). A hand edit of `xbin.json` that drops the grant row
  fires no hook; the rescan reconcile below catches it.
- **Reconcile on rescans and `users` events.** On every registry rescan
  (`OnStructureChange`) and `users` event the runtime walks the tiles with
  running sandboxes: a tile that vanished, or whose `SandboxesFor` is now
  false, is stopped; the others get `OnResourceChange` (§5) and a
  `SandboxEgress` re-resolve (§4).
- **Vault seal, resource and egress changes** stop the affected sandboxes
  (§4, §5).
- **Low disk and the disk cap** stop namespace sandboxes (§6.3).
- **A tile disabled, hidden or offloaded** has its sandboxes stopped. **A
  tile that vanishes** (rescan, `OnStructureChange`) has them stopped and
  their state kept (§9).

## 8. Security analysis

### 8.1 `cap:sandboxes`

- **Declaring and granting.** A manager declares `uses: [{target:
  "cap:sandboxes", role: "writer"}]`.
  - The cap is never granted by same-scope approval.
  - `ceilingBlockWith` classifies it with `deny(PolicyDenyXbinCaps)`. This
    line is mandatory: an unlisted cap falls through to `callBlock` and would
    be silently stripped by a `mayCall` row.
  - `allowCovers` gains a floor like `xbin`'s: **no allowance delegates it**,
    not even `cap:*`. So only a workspace admin approves it (D120).
- **Revoke and approve.** A revoke fires `OnCapChange`, which stops the
  tile's sandboxes. Approving restarts nothing.

### 8.2 Per route

| Route | Who | What xbind checks |
|---|---|---|
| every route | manager · admin | path hygiene (§3.1: no `.`/`..`/encoded `/` segment; `{name}`, `{id}`, `{sid}` match their grammars); the key comes from `keyOf` (a non-main deployment → 501) |
| `runtime` | manager | the key comes from the principal |
| list | manager (own key) · admin | — |
| create / patch | manager | name grammar + reserved names; `mode` required and available; sizes clamped; `net` names a `sandbox-net` slot of this tile; mounts (§5: same scope, `uses` + grant, role, kind, sub-path, `at`); `defaults.env` and `env` keys matching `XBIN_*` are refused (`invalid`); labels ≤ 1 KiB; `for`/`forUser` ≤ 128 chars, stored as claims; per-tile count; `clientId`; `version` |
| delete / stop | manager · admin (`?tile=`) | the admin path never creates, execs, reads files or attaches |
| start, reset, rebase | manager | admission; the definition re-validated against the tile's current reach (a mount it no longer holds fails the start with the reason) |
| run / execs | manager | argv or cmd non-empty; argv + env ≤ 256 KiB; `cwd` absolute; `uid`/`gid` allowed (`users`); the tty `forUser` check (§3.7); `execsRunning` |
| exec sub-routes | manager | the id's grammar; the exec belongs to this sandbox and this boot (another boot's → 410); stdin ≤ `stdinMax`; a signal from the enum, `group` nil = true |
| tty | manager | as for execs; `noTerminal` re-checked at every attach; read limit; override ids' grammar |
| files / tar | manager | `path` absolute and clean (resolved **inside** the sandbox); sizes; `mode` octal |
| copy | manager | both sandboxes in the caller's key |
| snapshots | manager | the id's grammar; the mode (and, in namespace mode, the flavour) matches on restore and clone; the snapshot's base installed; not busy; `from` without `snapshot` only from a stopped source; the tile's disk cap |
| policy | admin | merge, then `Validate` |

### 8.3 D78 and the host

- **Neither xbind nor the SDK ever resolves a sandbox-supplied path on the
  host.**
  - File operations run inside the sandbox (agentcore, §2.3).
  - Mount sub-paths are resolved beneath a trusted root with no symlinks.
  - Mount points are created without following.
  - Paths in requests are only ever sent to the agent.
- **Nothing reads, walks, copies or deletes a sandbox-written tree as
  xbind.**
  - Upper usage is a confined `du`.
  - Reset, delete, restore leftovers and snapshot removal are a rename into
    `.trash`, then a confined remove (`confine.RemoveAll`) by one background
    worker.
  - Snapshots and clones of uppers are a confined `cp -a --preserve=xattr
    --reflink=auto` (`confine.CopyTree`), so a lost xattr is an error, not
    a silently resurrected directory.
  - Confined runs use the file-caps profile, so trees owned by other sub-uids
    are readable and writable inside the throwaway sandbox. Nothing about
    that sandbox touches the host.
  - xbind creates the state and staging dirs empty, 0700.
  - Swapping a restored `cur` in is one `renameat2(RENAME_EXCHANGE)` of two
    xbind-created dirs, and a clone's is one rename; both happen only while
    no process holds the lock.
  - The same rule reaches the terminal layers: the removals WP-9 left as
    xbind `os.RemoveAll` (a restore swap's old layer, the `.xbin/restore`
    sweep, `term.ResetEnv`, offload-full's term dir) become confined
    (WP-9b).
- **VM disks.** xbind only creates, sizes, `fstat`s and clones them
  (`CloneSparse`: `FICLONE`, else `SEEK_DATA`/`SEEK_HOLE` +
  `copy_file_range`). It never mounts or parses one.
- **Tools.** `TestNoDirectExec` holds.
  - `agentcore` lives under `internal/sandbox/` (exempt, and it runs inside
    the sandbox).
  - `tilesbx` launches only through `sandbox.Launch` and `vm.Apply`, and runs
    tools only through `confine`.
  - Under isolation, every failure is an error, never a host fallback.
- **No xbin identity inside a sandbox.** There is no gateway bind, and
  `vm.Options.Gateway` is `""`. The environment is built by xbind
  (§3.3/§3.5), and `XBIN_*` keys are refused. The relay has no `HostFwd` and
  has `Deny`. There is no token of any kind. A manager can still hand its own
  token to a sandbox under another name; the docs say never to do that.
- **Inside a sandbox.**
  - Namespace mode has the D18 `Restricted` lockdown (no nested user or mount
    namespaces, `aptSafeCaps`, the ns-restrict seccomp), plus `MountGuard`
    and `NO_NEW_PRIVS`. `apt` works; docker doesn't.
  - VM mode is root in its own kernel, and docker works. The jail keeps the
    VM lockdown, `rp_filter` and permanent neighbour entries.
- **The attack surface a sandbox reaches:**
  - the agent (bounded frames; xbind trusts nothing it says);
  - the relay (policy plus `Deny`, decided per flow; flow caps per sandbox
    and across sandboxes, so no sandbox can spend xbind's fds or memory);
  - the shim's FUSE server (every step `openat2` beneath the export);
  - fuse-overlayfs (inside the sandbox);
  - the kernel.
- **A compromised manager** reaches only its own sandboxes, its own resources
  (as mounts), its bound `sandbox-net` classes and its quotas. A frame
  principal of the manager tile (a user's devtools) gets 403 on every route.
- **Consumers of one manager share its key**, so the runtime can't tell
  them apart; the manager's partitions are the only wall. The SDK keeps a
  consumer-supplied id from retargeting a route: `Forward` takes typed
  routes whose ids are checked against the runtime's grammars and escaped
  per segment (§10, WP-14b), and the runtime refuses dot and encoded-slash
  segments itself (§3.1).
- **Consumers are invisible to xbind** except through the manager's claims
  (`for`, `forUser`), which widen nothing. The one xbind-side check keyed on
  a claim is D88's `noTerminal`, and it is defense in depth: the real gate
  is the manager checking verified users.

## 9. Storage, backups, offload, removal

- **Backups carry definitions, not state** (D120).
  - `backup.Manifest` gains `sandboxes []json.RawMessage` (`omitempty`), and
    `includes` gains `"sandboxes"` only when the list isn't empty. A tile
    with no sandboxes therefore produces today's archive byte for byte, which
    dev-lifecycle's zero-state test requires.
  - Restore re-validates each definition and merges it **by `uid`**,
    bringing it back `stopped`:
    - a live definition with the same uid is replaced (it is the same
      sandbox), and its state on disk is kept when its base resolves;
    - a backed-up definition whose name is now taken by a **different** uid
      is skipped, and the restore's answer lists it: the live sandbox and
      its state are never displaced, and the backup never adopts another
      sandbox's state;
    - a restored definition with no state dir of its uid comes back `error`
      ("restored without state — reset it to start from a fresh base"),
      never as a blank `stopped` sandbox that would start from nothing
      silently.
  - Restore never touches `.xbin/sbx`.
- **State moves only through snapshots and clones** (WP-20), and later
  archive/thaw (WP-22).
- **Offload** stops the tile's sandboxes. While any of them has state (an
  upper, a disk or a snapshot), offload is refused 409: "archive or delete its
  N sandboxes (X GiB) first". Once WP-22 lands, offload carries them instead:
  each is archived under `<CK>.sbx.<name>.<uid>`, and local state is
  removed only after every PUT succeeds. Offload never drops state.
- **Restore** (`doRestore`) stops the tile's sandboxes first.
- **Removal.** Removing a tile isn't an event, so a rescan reconciles:
  - The tile's sandboxes are stopped, and their definitions and state are
    kept.
  - `pathLeftovers` lists "N tile sandboxes (X GiB) of a removed tile". A
    non-admin can't create a tile at that path and inherit them, and D85's
    forgetting doesn't apply to them.
  - An admin deletes them with `DELETE /sandboxes/{name}?tile=`, from the
    admin console.
- **Disk accounting.**
  - diskmon gets the tile's sandbox bytes (allocated, measured as in §6.3)
    through a `Usage` hook, and counts them **for disk pressure only**: its
    low-disk verdict and fair share include them, its per-scope quota and
    write blocking don't (§6.1). The per-tile cap is `perTile.diskGiB`.
  - `vm.ListDisks` globs `.xbin/sbx/*/*/cur/vm/disk.img` (WP-7b).
  - The admin view maps each disk to its tile and sandbox (the dir's
    `<name>.<uid>`).
- **Base pinning** (decision 9).
  - `internal/layers.Pinned` is the union of four sources: the
    `.xbin/term/*` stamps, the `.xbin/sbx/*/*/cur` stamps, the
    `snapshots/<sid>` stamps (§3.9), and the `base` of every definition,
    archived ones included. `.trash` pins nothing.
  - Both `layers.GC` and `vm.GC` keep what that union pins; an unreadable
    source (a stamp, or the definitions file) makes the set unknown, and
    nothing is released that boot.
  - `restart` and `thaw` keep the pin. `reset` and `rebase` change it, and
    a snapshot restore sets it to the snapshot's.
- **The existing term-layer backup reads and restores sandbox-written trees
  as xbind.** The storage map found that restore writes through planted
  symlinks. WP-9 fixed it first. It's not a tile-sandbox feature, but tile
  sandboxes must not copy the pattern. Its removals are still xbind's
  `os.RemoveAll` (which never follows links, but can't delete sub-uid-owned
  files in range mode, so disk leaks); WP-9b makes them confined, and has
  offload-full hold the terminal layer (`HoldTermEnv`) before removing it.

## 10. The SDK (`sdk/`, zero-dependency)

The SDK is split into `sandbox.go`, `sandbox_exec.go`, `sandbox_files.go` and
`sandbox_tty.go`, each under 800 lines. Every call goes through `Client()`,
so gateway, Bearer and all.

```go
func SandboxAPI() *Sandboxes
func (s *Sandboxes) Runtime(ctx context.Context) (*SandboxRuntime, error)
func (s *Sandboxes) List(ctx context.Context) ([]SandboxInfo, error)
func (s *Sandboxes) Create(ctx context.Context, spec SandboxSpec) (*SandboxInfo, error)
func (s *Sandboxes) Get(ctx context.Context, name string) (*SandboxInfo, error)
func (s *Sandboxes) Patch(ctx context.Context, name string, p SandboxPatch) (*SandboxInfo, error)
func (s *Sandboxes) Delete(ctx context.Context, name string) error
func (s *Sandboxes) Start(ctx context.Context, name string, wait time.Duration) (*SandboxInfo, error) // Stop, Reset, Rebase alike
func (s *Sandboxes) Copy(ctx context.Context, from, to SandboxPath, overwrite bool) error
func (s *Sandboxes) Sandbox(name string) *Sandbox

func (b *Sandbox) Run(ctx context.Context, r RunRequest) (*RunResult, error)
func (b *Sandbox) Exec(ctx context.Context, r ExecRequest) (*ExecInfo, error)
func (b *Sandbox) Execs(ctx context.Context) ([]ExecInfo, error)
func (b *Sandbox) GetExec(ctx context.Context, id string) (*ExecInfo, error)
func (b *Sandbox) Output(ctx context.Context, id string, q OutputQuery) (*OutputChunk, error)
func (b *Sandbox) Follow(ctx context.Context, id string, since int64) iter.Seq2[OutputChunk, error]
func (b *Sandbox) Stdin(ctx context.Context, id string, r io.Reader, eof bool) error
func (b *Sandbox) Signal(ctx context.Context, id, sig string, group bool) error
func (b *Sandbox) Resize(ctx context.Context, id string, rows, cols int) error
func (b *Sandbox) Kill(ctx context.Context, id string) error               // DELETE the exec
func (b *Sandbox) Stat(ctx context.Context, path string) (*FileStat, error)
func (b *Sandbox) ReadFile(ctx context.Context, path string, off, n int64) (io.ReadCloser, *FileStat, error)
func (b *Sandbox) WriteFile(ctx context.Context, path string, r io.Reader, o WriteOptions) (*FileStat, error)
func (b *Sandbox) List(ctx context.Context, path string, limit int) (*FileList, error) // Mkdir, Remove, Move alike
func (b *Sandbox) GetTar(ctx context.Context, path string, exclude []string) (io.ReadCloser, error)
func (b *Sandbox) PutTar(ctx context.Context, path string, r io.Reader, mkdirs bool) error
func (b *Sandbox) Snapshots(ctx context.Context) ([]Snapshot, error)       // Snapshot, RestoreSnapshot, DeleteSnapshot

// A SandboxRoute is one runtime sub-route of a sandbox, built only by the
// typed builders below (WP-14b). Each checks its id against the runtime's
// grammar (exec ids ^[0-9a-f]{6}-[0-9]{1,12}$, snapshot ids ^s-[0-9]{1,12}$)
// and escapes every segment itself; a bad id makes a route that Forward
// answers 400 invalid without sending anything. There is no free-form sub.
type SandboxRoute struct{ /* unexported: escaped path, or the error */ }
func ExecRoute(id string) SandboxRoute   // execs/{id}          (GET, DELETE)
func ExecOutput(id string) SandboxRoute  // execs/{id}/output
func ExecStdin(id string) SandboxRoute   // execs/{id}/stdin
func ExecSignal(id string) SandboxRoute  // execs/{id}/signal
func ExecResize(id string) SandboxRoute  // execs/{id}/resize
func ExecTTY(id string) SandboxRoute     // execs/{id}/tty
func FilesRoute(op FilesOp) SandboxRoute // files/{stat|content|list|mkdir|remove|move}
func TarRoute() SandboxRoute             // tar

// Forward passes a manager's own request through to the runtime route rt
// with the query q the manager chose — never the consumer's raw query. It
// streams both bodies, copies status/Content-Type/ETag/Content-Length, drops
// the inbound Cookie, Authorization, Sbx-User, X-XBin-* and
// Sec-WebSocket-Extensions, and tunnels an Upgrade byte for byte
// (httputil.ReverseProxy over Client().Transport, Upgrade headers restored
// as internal/proxy does). It returns once the response (or tunnel) is done.
func (b *Sandbox) Forward(w http.ResponseWriter, r *http.Request, rt SandboxRoute, q url.Values)
func (b *Sandbox) RelayTTY(w http.ResponseWriter, r *http.Request, execID string, o TTYOptions) // Forward to execs/<id>/tty
func (b *Sandbox) RelayNewTTY(w http.ResponseWriter, r *http.Request, o TTYStart)              // Forward to tty?cwd=&cmd=…
func (b *Sandbox) DialTTY(ctx context.Context, execID string, o TTYOptions) (*ws.Conn, error)   // sdk/ws, via Client()
func WriteSandboxError(w http.ResponseWriter, err error)  // a *SandboxError in the contract's shape
func IsExecID(id string) bool; func IsSnapshotID(id string) bool // the grammars, for a manager's own not-found (WP-14b as built)

type SandboxError struct { Status int; Refusal, Message, State, ETag string; RetryAfter time.Duration }
```

- **Request structs** mirror the JSON in §3 with `omitempty` everywhere, so a
  newer SDK talks to an older xbind.
- **Errors.** Every error from the runtime is a `*SandboxError`, with
  `errors.Is` helpers: `ErrSandboxNotFound`, `ErrSandboxLost`,
  `ErrSandboxState`.
- **The byte relay** needs no WebSocket code. It keeps frame counts and echo
  acks exact, because the browser's masked frames reach xbind unchanged.
  `DialTTY` is for consumers that drive a TTY themselves, such as phase 3's
  SSH bridge. It uses the stdlib `sdk/ws` package (on the branch since
  `p3/prep` merged; wired in b9f70575).
- **Why typed routes.** A manager puts a consumer's `{eid}` into the route.
  With a free-form `sub` (WP-14 as built), an id carrying `/` reaches a
  different sub-route, and in any router that cleans paths, a `..` or an
  encoded `/`, `?` or `#` reaches another sandbox of the same manager: the
  runtime can't tell consumers apart within one key, so that bypasses the
  contract's partitions. WP-14 already escapes and refuses dot segments;
  WP-14b removes the free-form string, so the only thing a consumer's id can
  do is fail its grammar. `url.JoinPath` is never used (it cleans `..`).

## 11. Mapping the contract onto the runtime

The builtin `coding-sandbox` manager's `xbin` Backend serves
docs/sandbox-manager.md almost one for one. It uses **its contract sandbox id
as the runtime name**: `sb-<8 hex>`, which fits both grammars, and `img-<id>`
for image sandboxes. It uses **the runtime's exec ids as contract exec
ids**.

| Contract (`/sbx/…`) | Runtime (`/api/xbin/…`) · SDK | What the manager adds |
|---|---|---|
| `GET hello` | `GET /sandboxes/runtime` · `Runtime` | `protocol`; `manager`; `caps` = the runtime's caps (`archive` once WP-22 lands); `egress` = `none`, then `internet` if the class it maps to internet is bound with reach `internet`, then `open` if an operator-chosen class has reach `open` (unbound ⇒ only `none`); `images`, `sizes` (its own catalog, clamped to `limits`); `limits` from the runtime's, with `sandboxes` = its own quota |
| `GET sandboxes` | `GET /sandboxes` · `List` | partitions (home consumer = `X-XBin-From`, `shares`); people (owner, visibility, members, on verified calls); merges its record with the runtime's `state`, `mode`, `reach`, `lastActive` |
| `POST sandboxes` | `POST /sandboxes` · `Create` | `clientId` dedupe per consumer (its table: consumer + clientId → id + request hash, for `exists`); id; `image` → `from: {sandbox: img-<image>, snapshot}` (built on first use by a setup `run`, then snapshotted); `size` → `memMiB`/`vcpus`/`diskGiB`; `egress` → `net.egress`; `mode` → `vm` by default, else `namespace` (`isolation` follows); `for` = the consumer, `forUser` = the person; `defaults` (workdir, user, home, shell, `SANDBOX_ID`/`SANDBOX_NAME`); `state: creating` while the image builds, and the runtime's own `creating` while the clone copies (`?wait` passes through) |
| `GET/PATCH/DELETE sandboxes/{id}` | `/sandboxes/{name}` · `Get`/`Patch`/`Delete` | name, visibility, members, shares and labels stay in its own table (`version` is its own); `size`, `egress`, `autoStopMin` go to the runtime (`idleStopMin`, `egressNext`); only the home consumer deletes |
| `start` · `stop` (`?wait`) | same | — |
| `archive` · `thaw` | WP-22 (until then `unsupported`) | — |
| `run` | `POST …/run` · `Run` | the body passes through; it adds `uid`/`gid` from its user and `forUser` |
| `execs` (POST/GET/one/DELETE) | same · `Exec`/`Execs`/`GetExec`/`Kill` | `clientId` → the runtime's, prefixed `<consumer hash>:` (contract clientIds are per consumer); `forUser` |
| `…/output`, `stdin`, `signal`, `resize` | same routes · `Forward(…, ExecOutput(eid) \| ExecStdin(eid) \| ExecSignal(eid) \| ExecResize(eid), q)` | nothing: identical query and shapes (a bad `eid` never goes out: `Forward` answers 400, so a manager answers the contract's 404 first, with `IsExecID`) |
| `…/execs/{eid}/tty`, `…/tty` | same routes · `RelayTTY`/`RelayNewTTY` | its person check (verified `X-XBin-User`); `forUser` = that person; `sessionId`/`sandboxId` = its ids |
| `files/*`, `tar` | same routes · `Forward(…, FilesRoute(op) \| TarRoute(), q)` | nothing |
| `snapshots` | same · `Snapshots`… | snapshot names in its table if it wants them |
| clone (`from`) | `POST /sandboxes` with `from` | — (a `from` without `snapshot` of a running source is the runtime's 409 `state`, passed through) |
| errors | the same enum | passed through (Forward) or rebuilt from `*SandboxError` |

**What the manager keeps**, in a sqlite resource of its own:

- **sandboxes:** id, display name, image, size id, contract egress word,
  owner `{user, via, asserted}`, visibility, members, shares, labels, created,
  version, and a state overlay (`creating`, `deleting`, `error`);
- **client ids:** consumer + clientId → id + request hash;
- **images:** id → image sandbox, snapshot and base version. When
  `base.outdated` shows, the image is rebuilt. Deleting and re-creating
  `img-<id>` is safe: the new sandbox gets a new `uid`, so it never meets the
  old one's state while that is still being removed (§1.3). Sandboxes
  already cloned from the old image are independent copies, each pinning
  its own base.

**What it doesn't keep:** execs and their output (the runtime's, lost with
xbind, as the contract allows); egress policy (bindings); quotas across
tiles (the runtime's books).

**Inside a sandbox** it sets `IN_SANDBOX=1` (the runtime already does),
`SANDBOX_ID`, `SANDBOX_NAME`, `HOME` and the user through `defaults`.
`workdir` exists because the image's setup script made it.

## 12. Work packages

**Tracks.** Wave 1 ran as four parallel tracks with disjoint files. Wave 2 is
the runtime proper and waits on its inputs. Wave 3 closes phase 2 and opens
phase 3.

**Where it stands (2026-09-27).** On `sandbox-runtime`: WP-0 (`p2/vmfix`),
WP-2, WP-5 … WP-14 and phase 3's groundwork (`p3/prep`: `sdk/ws`, so
`DialTTY` is wired). WP-1 is built on `p2/agentcore`, waiting to merge.
WP-3 and WP-4 haven't started. The review of waves 2–3 and wave 1's
handoffs added the **follow-ups** (WP-2b … WP-14b, after WP-14 below): each
changes code wave 1 already built, so most can start now.

```
Track A  exec core:  WP-1 (built, p2/agentcore) → WP-3 → WP-3b ;  WP-1 → WP-4
Follow-ups, now:     WP-7b · WP-8b · WP-10b · WP-13b   ── all four before WP-15a
  (wave-1 code only) WP-14b                            ── before WP-21 and phase 3's manager
                     WP-9b · WP-11b                    ── independent of the runtime
                     WP-2b                             ── independent; after WP-3b and WP-10b (shared files)
Wave 2:  WP-15a (after WP-3b, 7b, 8b, 10b, 13b) → { WP-15b ∥ WP-16 (+WP-4) ∥ WP-17 ∥ WP-18 }; WP-15b → WP-19
Wave 3:  WP-20 (after WP-15b, WP-16) → WP-21 fixture + e2e = "phase 3 ready"
Later:   WP-22 archive/thaw (+ offload carries, streaming archive I/O, s3 multipart)
```

**Parallel tracks for the rest of phase 2:**

| Track | Now | After WP-15a |
|---|---|---|
| A exec core | WP-1 merge → WP-3 → WP-3b; WP-4 | — |
| B books | WP-7b; WP-8b; WP-9b | WP-15b → WP-19 |
| C net + wire | WP-10b; WP-11b | WP-17 |
| D API + SDK | WP-13b; WP-14b | WP-18 |
| E VM | (WP-4, track A) | WP-16 → WP-20 (with WP-15b) |
| F hygiene | WP-2b (after WP-3b, WP-10b) | — |

Within "now", the files are disjoint except where noted in each WP (and
`docs/changelog.md`, where each track amends its own bullet under
`## 2026-09-28`, or adds one when it has none).

Each WP ends green on `make check` (fmt, vet, the unit tests, `apicheck`,
`sizebudget`, `docscheck`, `TestNoDirectExec`). It also passes the
integration packages it touches, run with the Bash sandbox disabled on this
box. Docs and the changelog travel with the change that makes them true.

### WP-0 — VM backends listen *(external dependency; merged from `p2/vmfix`, ccf591e5)*

Another agent is fixing a Go backend in a VM that never reports
`listening`. The VM map's lead: the guest's `/dev/fuse` pumps run in PID 1
next to its vforking `os.StartProcess`.

- **Why it blocks us:** WP-1 and WP-4 edit the same guest and shim files,
  and the resident design mustn't ship before the pumps leave PID 1.
- **What we need from it:** the relay process registered with the reaper,
  which WP-1's `Spawner.Register` keeps.

### WP-1 — `proto` additions + `agentcore` (Track A · M · after WP-0)

*Built on `p2/agentcore`; to merge. WP-3b adds one `Options` field.*

- **Goal:** one exec core, used by the guest now and by `__sbx-agent` in
  WP-3, with the fixes in §2.3 and the file operations in §2.2.
- **Files:**
  - `internal/sandbox/vm/proto/proto.go` (Exec/Msg/Hello fields,
    `RecvMax`, `ReadHelloMax`) and new `proto/file.go` (FileOp, FileResult,
    FileStat, `WriteFrame`/`ReadFrame`, `MaxFrame`);
  - new `internal/sandbox/agentcore/{core.go, session.go, spawn_linux.go,
    files_linux.go, tar_linux.go, core_other.go}`;
  - `internal/sandbox/vm/guest/agent_linux.go` becomes `agentcore.New` with
    the guest's `Configure`, `Sync: unix.Sync` and `PID1Spawner`;
    `guest/session_linux.go` is removed (moved).
- **Exposes:** `agentcore.{New, Options, Core.Handle, Core.Serve, Spawner,
  PID1Spawner, ProcSpawner}`; the proto types.
- **Tests:**
  - Unit (`agentcore/*_test.go`, linux, `ProcSpawner`, over socketpairs):
    - strict cwd refusal;
    - a group signal reaches a grandchild;
    - sessions pruned, the cap enforced, an id not reusable;
    - `Merge`, `NoStdin`, `NoSync`;
    - tty resize;
    - `exited` carries the signal;
    - every file op: ranged read, atomic write with `IfMatch`/`Create`,
      mkdirs, list truncation, remove, move;
    - tar round trip with symlinks and hard links; `../`, absolute and
      symlink-escape entries stay inside the target;
    - a `write` without a terminator never commits;
    - an oversized Hello is dropped.
  - Integration: the existing `internal/vm` suite (KVM and
    `XBIN_VM_ACCEL=emulate`) stays green; backends and terminals are
    unchanged.
- **Size:** each agentcore file ≤ 500 lines. `proto.go` 220 → ~300. The guest
  package shrinks by ~300 lines.
- **Parallel:** with WP-2 (no shared files) and with all of Tracks B–D.
- **As built** (branch `p2/agentcore`):
  - Past the list above, agentcore also has `Options.Gateway` (serves
    `Exec.Gateway`; nil refuses it) and `Options.Dump` (answers `dump`; nil
    ignores it), `Core.Sessions()` (the guest's dump) and `Splice`. With
    `Configure: nil`, every `ctl` gets `ready` at once and a `config` is an
    error.
  - `PID1Spawner` also remembers the last 64 statuses nobody registered, so
    a `Register` after the exit still answers. It owns SIGCHLD: call it once
    per process.
  - The guest's `StreamWait` is 60 s, because the shim dials each stream in
    turn, which is slow when emulated. The default is 15 s.
  - proto also has `Conn.Reader`/`Writer` (the frames after a line),
    `FrameReader`/`FrameWriter`, the bounds `MaxHello`, `MaxEvent`,
    `MaxCommand` and `MaxResult` (2 MiB, for a `FileResult` line; a
    listing stops at 1 MiB of entries and says `truncated`), and the
    `Refuse*` constants.
  - `HostSpec.Resident`/`AgentFD` stay with WP-4.
  - The files are split further to keep each under 500 lines:
    `filesmut_linux.go` (write, mkdir, remove, move), `procattr_linux.go`
    (cwd, uid/gid, PATH) and `streams.go` (listen bridging, `Splice`).

### WP-2 — Launch plumbing: factory, `AgentFD`, lock, `Bind.Sub` (Track A · M)

- **Goal:** §2.1 and §5's init side.
- **Files:**
  - `internal/sandbox/sandbox.go`: `Spec.Agent`/`AgentFD`,
    `Spec.Lock`/`LockFD`, `Spec.Hostname`, `Bind.Sub`;
  - `launch_linux.go`: ExtraFiles order ctrl, sync, agent, lock;
    `Handle.Started`; `fail` never closes the caller's files;
  - new `factory_linux.go`;
  - new `agentfd_linux.go`: the init's CLOEXEC dance, `sethostname`, the
    `Sub` resolution through `/proc/self/fd`, and no-follow mount points
    (dl/confine-dirfrom's helpers if they have landed);
  - `init_linux.go`: call sites only;
  - `launch_other.go`: stubs.
- **Exposes:** `sandbox.{NewFactory, Factory, AcceptFrom}` and the new Spec
  and Bind fields.
- **Tests:**
  - Unit (`factory_linux_test.go`): round trip; 100 concurrent dials; peer
    close → `AcceptFrom` returns EOF; `Dial` after the peer closes → error;
    a full queue → error, not a hang.
  - Integration (`linux && integration`, a minimal lower + static helpers
    built in the test, like `TestSandboxIsolation`, so CI runs it):
    - the entry sees `AgentFD` and `LockFD` and answers a factory connection;
    - fuse-overlayfs's `/proc/<pid>/fd` doesn't hold the factory;
    - a `Sub` with a symlinked component is refused;
    - a symlink planted at a mount point is refused;
    - the hostname is set.
  - Add `./internal/sandbox/` to `make integration`. The dev-lifecycle
    branch does the same with its package-listing guard; this box's range-map
    hang is environmental (the memory note).
- **Size:** `init_linux.go` is 543 lines here but 752 on dl/confine-dirfrom,
  so the logic goes in `agentfd_linux.go` (≤ 300).
- **Parallel:** with WP-1. Rebase with dl/confine-dirfrom, whichever lands
  second.
- **As built (notes and deviations):**
  - *No-follow is opt-in:* `Spec.NoFollow`, which tilesbx sets. Terminals
    and backends keep their path-based mounts, because a bind of theirs may
    legitimately pass a symlink the rootfs ships (never break users). Under
    `NoFollow` the no-follow walk covers every mount point the init makes in
    the new root, not just binds and masks: `/proc`, `/tmp`, `/dev` and the
    pivot's `.oldroot` too, since each is a D78 hole otherwise. A bind nested
    in a read-only bind gets its read-only remount after the nesting, as on
    dl/confine-dirfrom.
  - *dl/confine-dirfrom's helpers* (`openNoFollow`, `nestedPoint`, `fdPath`,
    `beneath`) are copied verbatim into `nofollow_linux.go`. Whichever
    branch lands second deletes that file. The existing integration tests
    gained dl's `SetupUserns` hunk byte for byte, so they no longer hang in
    range mode.
  - *The fd numbers travel in argv, appended by the init.* Launch picks the
    numbers after the caller built the spec, so the init appends
    `--fd N [--lock M]` to a namespace-mode entry's argv. It leaves a VM
    shim's argv alone: WP-4 carries the fd in `HostSpec`.
  - *The CLOEXEC dance runs in the init's final stage*, not right after the
    first read of the spec. A range-mode stage 1 re-execs, and that re-exec
    has to keep the fds.
  - *Ownership:* `Handle.Started` closes the caller's `Agent` and `Lock`
    along with Launch's own child-side files. Launch and `Cleanup` never
    close the caller's files. So if `cmd.Start` fails, the caller closes
    them.
  - *A pre-existing D78 hole, closed for every sandbox:* the egress setup's
    `/etc/resolv.conf` write, and a host-network terminal's
    `resolv.conf`/`hosts` copies, followed symlinks in the new root before
    pivot_root. A terminal user could plant `/etc/resolv.conf → <host
    path>` in their persistent layer, and the next start overwrote that host
    file as xbind (`TestResolvConfNeverFollowed` reproduces it on the old
    code). `writeInRoot` now replaces a symlink there with a regular file,
    whatever `NoFollow` says.
  - *Still open, outside tile sandboxes:* a terminal's or backend's
    path-based mount points still follow a symlink in a persistent upper.
    That can make empty directories and files on the host (the old code
    made `xbin/` where a planted `/opt` pointed), though it can't overwrite
    anything. A planted `/proc` symlink also wedged that old path, with the
    init and fuse-overlayfs both stuck in a FUSE wait. Turning `NoFollow` on
    for them needs a survey of rootfs symlinks first.

### WP-3 — `bx __sbx-agent` (Track A · S · after WP-1, WP-2)

- **Goal:** §2.4.
- **Files:**
  - new `cmd/bx/sbxagent.go`;
  - `cmd/bx/agent.go`: +2 lines in `cmdExtra`, which is at 786/800;
  - new `internal/sandbox/agentcore/ns_linux.go` (`RunNamespace`).
- **The fd contract WP-2 built:** the init appends `--fd <AgentFD>` and,
  when a lock is set, `--lock <LockFD>` to a namespace entry's argv; the
  agent parses exactly these. The factory end arrives close-on-exec cleared
  (the init's last step), the lock inheritable.
- **Tests:** integration in `internal/sandbox/agentcore` (`linux &&
  integration`; `TestMain` dispatches `sandbox.InitArg`; the test builds a
  static `bx`). It uses a minimal lower, and the rootfs when present, with
  `Restricted` + `MountGuard`:
  - exec, tty and file ops;
  - closing the factory empties the pid namespace within 1 s;
  - SIGTERM from an exec doesn't kill the agent;
  - an exec can't read `/proc/1/fd`;
  - `unshare -U` fails.
  - Add the package to `make integration`.
- **Parallel:** with WP-4 once both deps are in.
- **As built** (branch `p2/wp3`):
  - `RunNamespace(agentFD, lockFD) int` is the exit code: 0 on the
    factory's EOF, 1 for a setup or transport failure, 2 when it isn't PID 1.
    It refuses a factory fd that isn't a `SOCK_SEQPACKET` socket, and past
    the two dups it marks every other fd above stdio close-on-exec
    (`close_range`).
  - *Signals are caught, not ignored.* `signal.Ignore` would set `SIG_IGN`,
    which survives exec: every session would start with TERM, INT and HUP
    ignored. `signal.Notify` plus a drain keeps a handler, which the child
    resets to the default. The list adds ABRT, SEGV, BUS, FPE, ILL, TRAP,
    SYS and STKFLT: Go dies of any of them sent by `kill`. SIGKILL and
    SIGSTOP need nothing, since the kernel drops them for a pid namespace's
    init. A fault signal forged with `rt_sigqueueinfo` still crashes the
    runtime. That only stops the sandbox itself, and xbind trusts nothing
    from the agent anyway (§2.6).
  - *Sessions never inherit the agent's environment.* A nil `Exec.Env` used
    to inherit it through `os.StartProcess`. A session now gets exactly its
    exec's `Env`, plus the terminals' `PATH` when that names none. This
    applies to the VM guest too, where every existing caller already sends a
    `PATH`. So the contract's `IN_SANDBOX`, `SANDBOX_ID`, `SANDBOX_NAME` and
    `HOME` are WP-17's to send.
  - *The final sync is bounded.* fuse-overlayfs is a process of the sandbox,
    so a session can SIGSTOP it. The agent's `syncfs` then hung and kept the
    sandbox and its lock alive after the factory closed. On EOF the agent now
    sends SIGCONT to the whole namespace, then syncs. If the sync is still
    waiting after 5 s, it SIGKILLs the namespace, which aborts the FUSE
    connection, and exits.
    - **For WP-15a:** while the sandbox runs, a stopped fuse-overlayfs
      still wedges a session's per-exit sync and the `sync` op. xbind's
      stop must bound its wait for `synced`, then close the factory.
  - *The integration test* is `agentcore/ns_linux_test.go`, with the probe
    in `testdata/nsprobe`. It runs over a minimal lower with the kernel
    overlay, and again over the rootfs with fuse-overlayfs when they are
    present. Its attack probe also covers fuse-overlayfs, which holds the
    host's lower and upper directories. That process is out of reach too,
    because its capabilities aren't a subset of an exec's
    (`cap_ptrace_access_check`).
  - *Non-root sessions.* When the sandbox maps uid 1000 (range mode), the
    attack probe also runs as uid 1000, then as root again. A spawn with
    `UID`/`GID` calls setuid in Go's vfork child, which shares the agent's
    memory. So the kernel resets the agent's dumpable to `fs.suid_dumpable`.
    With 0 or 2 (the kernel's and systemd's defaults) the agent stays
    closed. A host set to 1 would open it to root sessions: this is not
    mitigated.

### WP-4 — The resident shim (Track A · M · after WP-0, WP-1, WP-2)

- **Goal:** §2.5.
- **Files:**
  - `proto/hostspec.go` (`Resident`, `AgentFD`);
  - new `internal/sandbox/vm/host/resident_linux.go`;
  - `host/shim_linux.go` (the branch after `ready`; its size stays ~470);
  - `internal/sandbox/init_vm_linux.go`: **`writeVMSpec` copies
    `Spec.AgentFD` into `HostSpec.AgentFD`** (WP-2 left this to WP-4: the
    init already clears close-on-exec on `AgentFD` before exec'ing the
    shim, keeps `LockFD` inheritable, and leaves the shim's argv alone); the
    fd is kept across the VM lockdown;
  - `internal/vm/manager.go` (`Options.Resident`; `Apply` keeps
    `Agent`/`Lock` and skips `Guest`).
- **Tests:**
  - Unit (`resident_test.go`): routing with a fake guest over `net.Pipe`:
    newest `ctl` wins, session < 2 refused, events fanned upstream, bounded
    lines.
  - Integration (`internal/vm/resident_linux_test.go`, run twice by `make
    integration`, KVM then `XBIN_VM_ACCEL=emulate`):
    - 20 concurrent execs;
    - tty + resize;
    - a group signal;
    - strict cwd;
    - file ops on the disk and through a FUSE mount;
    - an exec of a binary served as a single-file FUSE export at 1 vCPU
      (WP-0's path, resident);
    - SIGHUP → `synced` + 129;
    - factory close → the VMM is gone within 3 s (18 s emulated);
    - the shim's HostSpec carries the factory: an exec through a factory
      connection works with nothing in the shim's argv.
- **Size:** `resident_linux.go` ≤ 450.
- **Parallel:** with WP-3.
- **As built** (branch `p2/wp4`):
  - *The factory's fd.* `writeVMSpec` copies `Spec.AgentFD` into
    `HostSpec.AgentFD`; `handAgentFD` keeps it open across the exec and the
    VM lockdown closes nothing. The shim moves it to an `F_DUPFD_CLOEXEC`
    copy and checks that it is a `SOCK_SEQPACKET` socket before it starts
    the VMM, so Firecracker and QEMU never hold it (the integration test
    reads the VMM's `/proc/<pid>/fd` to check). The lock fd stays
    inheritable, so the shim and the VMM both hold the lock.
  - *`vm.Options.Resident`* needs `spec.Agent` and refuses `Listen`,
    `Gateway` and `TTY`. It leaves `hs.Guest` empty and keeps `spec.Agent`
    and `spec.Lock`. The guest's hostname falls back to `spec.Hostname`,
    which no other caller sets. A VM that isn't resident refuses
    `spec.Agent`: no shim would take the factory, and the VMM would inherit
    it.
  - *What goes to the guest is rebuilt.* The shim rebuilds each command
    from the fields its op uses and never forwards the raw line. It also
    re-marshals each Hello, so unknown fields don't pass. What it refuses
    gets an `error` event: an exec, resize or signal for a session below 2
    (named in the event), an exec with `Listen` or `Gateway`, a sync for
    session 1, and any other op (`config`, `dump`, …) with session 0. A
    refused exec never reaches the guest. A stream for a session below 2,
    a `listen` and an unknown kind are closed. A Hello must arrive within
    30 s.
  - *An upstream that stops reading loses its ctl.* Each event line to
    xbind has a 10 s write deadline; past it the shim closes that ctl, so a
    wedged reader can't hold the shim's loop, or its SIGHUP. A guest event
    is re-encoded on its way up, which can grow it (a raw `<` becomes
    `\u003c`); one that would pass `MaxEvent` is dropped, so xbind's
    `RecvMax(MaxEvent)` never cuts its ctl over what the guest sent.
  - *Hangup.* SIGHUP, SIGTERM and SIGINT exit 128+signal; the factory's EOF
    exits 129. Each sends the flush-only sync. The shim counts the syncs in
    flight, xbind's own included, and exits on the answer to its own (the
    guest answers them in order) or after 2 s (6× emulated). A guest line
    over `MaxEvent`, or the guest closing its ctl, exits 125: nothing can
    be routed after that.
  - *SIGQUIT's dump* on a resident VM still reports the shim, the exports
    and the console, but never asks the guest, whose answer can outgrow
    `MaxEvent`.
  - *Tests.* `host/resident_linux_test.go` drives the router over a real
    factory against a fake guest. `internal/vm/resident_linux_test.go` does
    two boots per accelerator. The first runs at 1 vCPU and covers exec,
    stdin EOF through vsock, `Merge`/`NoStdin`, the single-file FUSE
    export, strict cwd, 20 concurrent execs, tty + resize, a group signal
    reaching a grandchild, file ops on the disk and through a mount, and a
    tar round trip. Its factory close then ends it in ~40 ms (129) and
    frees the lock. The second boots on the same disk: a file the first
    wrote with `NoSync` survived the close's flush, and SIGHUP exits 129.
    The flush and fd checks were mutation-tested.
  - *For WP-15a/16.* xbind sets `Exec.NoSync` on resident execs; the shim
    doesn't force it. xbind can dial its ctl right after `Start`: the
    connection waits in the factory, and `ready` answers it once the guest
    is configured, which is the boot time. The factory's queue is
    `net.unix.max_dgram_qlen` deep: 512 under systemd, 10 on a bare
    kernel. A burst of dials past that is `EAGAIN` (WP-2's `Dial`), so the
    runtime should retry briefly, as the test client does.

### WP-5 — `cap:sandboxes` (Track B · S)

- **Goal:** §8.1.
- **Files:**
  - new `internal/broker/caps.go` (`SandboxesCap`, `SandboxesFor`,
    `OnCapChange`);
  - `broker/policy.go` (+1 ceiling case);
  - `broker/broker.go` (`grantRestart` also fires `OnCapChange` for `cap:`
    targets);
  - `internal/users/personal.go` (the `allowCovers` floor);
  - `web/bx-allow.js` (`CAP_INFO['sandboxes']`);
  - docs: `docs/auth.md`, `docs/elements.md`, `workspace-template/AGENTS.md`,
    `docs/changelog.md`.
- **Tests:** `broker/caps_test.go`:
  - pending until approved; never approved by same scope;
  - a ws admin approves;
  - an org admin with `cap:*` in the allowance can't, and neither can a
    personal owner with `cap:*`;
  - a policy row denying xbin-caps strips it at evaluation;
  - a revoke fires `OnCapChange(tile, cap, false)`;
  - an approve restarts no backend.
- **Parallel:** fully.
- **As built** (branch `p2/cap-vmpol`):
  - `OnCapChange func(tile, capTarget string, held bool)` is a `Broker`
    field (broker.go); `caps.go` has `SandboxesCap`, `SandboxesFor(tile)`
    (false for a tile that no longer exists), `capChanged` and `capSweep`.
    `held` is the effective state **after** the change, so it fires on an
    approve too (`true`), and revoking one of two rows reports `true`.
  - **Added:** a ceiling change that strips the cap also fires
    `OnCapChange(tile, cap, false)`. A policy row, a permission set, org or
    personal sets and an owner transfer all end in `usersEvent`, which now
    runs `capSweep` over the `cap:` grant rows. It is stateless and may
    repeat; the hook must be idempotent and return promptly (WP-19's
    `StopTile` wiring).
  - **Added:** the floor is `users.NeverDelegable` (xbin family +
    `users.SandboxesCap`). A literal `cap:sandboxes` allowance entry is
    refused at write (`parseAllowEntry`), mirrored in `bx doctor` and
    `web/bx-allow.js` (`CAP_INFO.sandboxes.noDelegate`; `KNOWN_CAPS`
    leaves it out of the allowance picker). Globs that match it stay valid
    and never cover it.
  - Docs also: the reserved-targets table in
    `docs/overview/06-authorization.md`.

### WP-6 — VM policy for tiles (Track B · S/M)

- **Goal:** decision 11's VM half, and `tilesEmulated`.
- **Files:**
  - `internal/vm/policy.go` (`Tiles`, `TilesBudgetMiB` (0 = `budgetMiB`/2,
    ≤ `budgetMiB`), `TilesEmulated`);
  - new `internal/vm/reserve.go` (`ReserveOption`, `TileSandbox()`,
    `usedTiles`, `UsedTiles()`), with the same signature as dev-lifecycle's
    `reserve.go`, so rebasing is trivial;
  - `internal/boot/vm.go`: `PUT /vm/policy` decodes onto the stored policy,
    so absent fields keep their values and an older admin console can't zero
    `tiles`;
  - `deploy/install.sh`: `vm_policy_json` writes `"tiles": <kvm usable>` in a
    fresh file only;
  - `workspace-template/tiles/admin/tabs/sandboxes.js` (the three fields);
  - `hack/ui-harness/passes/sandboxes.js`;
  - docs: `docs/protocol.md` (`/vm`, `/vm/policy`), `docs/isolation.md`,
    changelog.
- **Tests:**
  - `vm/reserve_test.go`: the sub-budget refuses; the global budget still
    applies; release is idempotent; non-tile reservations don't count
    against the sub-budget.
  - `boot` PUT merge: a body without `tiles` keeps it.
  - `make shellcheck`, and the installer's policy test if one covers
    `vm_policy_json`.
- **Parallel:** fully.
- **As built** (branch `p2/cap-vmpol`):
  - `Reserve(owner, memMiB, opts ...ReserveOption)` stays in `policy.go`
    with dev-lifecycle's exact signature, and `reserve.go` is its file plus
    one field (`reserveOptions{tile}`), `TileSandbox()` and `UsedTiles()`.
    Rebasing is the union of the option fields and checks. `usedTiles` is a
    `Manager` field in `manager.go` (Go declares fields with the struct).
    The checks run in order: count, tile sub-budget, global budget.
  - `TilesBudgetMiB`: `Validate` refuses one above the effective
    `budgetMiB` (so lowering the budget below a set sub-budget is a 400
    naming it); `withDefaults` clamps a hand-edited file.
  - **Added:** `(*vm.Manager).TileVMs() (st Status, reason string)`: the one
    place `tiles` and `tilesEmulated` are read. `reason` is `""` when VM mode
    is available to tile sandboxes, else §3.2's text ("an admin hasn't
    enabled VM tile sandboxes (vm policy: tiles)", the host's reason, or the
    `tilesEmulated` one). WP-13's `runtime` and WP-16's gate should call it;
    `st.Emulated` is the report.
  - `PUT /vm/policy` is `State.putVMPolicy`: under a mutex it decodes onto
    `StoredPolicy()` (strict decode unchanged: unknown fields are 400) and
    answers `{status, policy, stored}`. It is the one place the policy
    changes, so WP-16's flip wiring (`tiles → false`, `tilesEmulated →
    false` while emulated) hooks there, with the old stored policy in hand.
  - `GET /vm` (admins) and `GET /sandboxes` `health.vm` gain `usedTiles`
    (`boot/sandboxes.go` `vmView`, +1 field: a small WP-7 collision).
  - The installer's `vm_policy_json BACKENDS TILES`; both are "KVM usable".
    The admin tab sends every switch and size (`POLICY_SWITCHES`,
    `POLICY_FIELDS`), shows the sub-budget line (`data-vm-tiles-used`) and
    warns about emulation and flips.

### WP-7 — Layers, disks and GC across both trees (Track B · M)

- **Goal:** §9's base pinning, and the disk helpers. This must merge before
  any tile sandbox stamps a layer.
- **Files:**
  - new `internal/layers/{layers.go, pins.go}`: `BaseVersion`,
    `ResolveBase`, `Stamp` (base + flavour), `Read`, `Outdated`,
    `Pinned(ws, extra func() []string)`, `GC`, `Check` (a report per layer);
  - `internal/term/base.go` becomes thin wrappers. The terminal boot gate
    stays, for `.xbin/term` only;
  - `internal/vm/disk.go`: `EnsureDiskAt(dir, bytes)`, grow only;
    `EnsureDisk` wraps it; `ListDisks` also globs `.xbin/sbx/*/*` and gains
    `Kind`/`Sandbox`;
  - `internal/vm/image.go` (`GC(keep)`);
  - `internal/boot/boot.go` (GC passes use the union; a pin-source func,
    nil until WP-15a);
  - `internal/boot/sandboxes.go` (the disk rows).
- **Tests:**
  - `layers` unit: the union; GC keeps a base only a `.xbin/sbx` stamp or a
    definition pins; `Check` reports per layer.
  - `term` `base_test.go` and `views_boot_test.go` unchanged and green.
  - `vm` `TestListDisks` extended.
- **Parallel:** fully.
- **As built (p2/layers).** Additions, no departures:
  - `layers.Pin(dir, rootfs, overlay)` is §7 step 3 in one call: an
    unpinned dir takes the current base (and flavour); a base that isn't
    installed is `ErrBaseMissing`, another flavour `ErrOverlay`, and neither
    writes. `reset`/`rebase` re-stamp with `Stamp`. VM mode passes no
    flavour.
  - **Snapshots pin too.** `Pinned` reads a `base` stamp in each
    `.xbin/sbx/<CK>/<name>/snapshots/<sid>/` (`<name>.<uid>/` with its
    stamps under `cur/` after WP-7b), so a snapshot keeps its base
    after its sandbox is reset or rebased (a clone needs it). WP-20 stamps
    each snapshot dir with `layers.Stamp` next to `meta.json`.
  - An unstamped `.xbin/sbx` dir pins nothing (never started); an unstamped
    terminal layer still pins `v0`. `view-*` dirs are not layers.
  - **Unknown pins release nothing.** `Pinned` errors when a stamp or a
    tree can't be read; the boot then passes nil, and `layers.GC(nil)` /
    `vm.GC(nil)` keep every base and image (only build leftovers go).
  - `vm.GC(keep)` takes the pinned versions and keeps the image of each
    installed pinned base, keyed as it would be built.
  - `EnsureDiskAt(dir, bytes)` takes the layer or state dir and makes
    `dir/vm/disk.img`; a symlink or non-regular file there is refused.
  - `Disk.Kind` is `terminal` | `tile` (the registry's kind names); the
    admin tab shows a tile disk's sandbox name (a one-line change to
    `sandboxes.js`, plus a harness fixture row).
  - The boot's pin-source func is `State.sandboxBasePins` (nil until
    WP-15a, which must set it and load the definitions before
    `stepIsolation`). `term.GCBaseImages` is gone: the boot runs
    `layers.GC(rootfs, st.pinnedBases())` and `vm.GC(st.pinnedBases())`.

### WP-8 — cgroup leaves + confine's file-caps profile (Track B · S+S)

- **Goal:** §6.2, and the confined `du`/`rm`/`cp -a` of §8.3.
- **Files:**
  - `internal/cgroup/cgroup_linux.go`, or a new `leaf_linux.go`:
    `AddWith(name, pid, Limits{…, CPUMax})`, `Kill`, `Populated`, `Sweep`;
  - `cgroup_other.go`;
  - `internal/confine/confine.go`: `Cmd.FSCaps`;
  - `internal/sandbox`: `Spec.FileCaps`, a lockdown branch that keeps
    CHOWN, DAC_OVERRIDE, DAC_READ_SEARCH, FOWNER, FSETID and SETFCAP with
    the backend seccomp minus `mknodat` (a new small `filecaps_linux.go`);
  - `internal/boot/sandboxes.go` (`sessionWhat` gets a tile case).
- **Tests:**
  - cgroup unit against a temp dir used as the base: limits written, the
    `Kill` fallback, `Sweep`.
  - confine integration: an `FSCaps` run can `cp -a` a tree holding a 0:0
    char whiteout and a 0600 file, and `rm -rf` it. This box is single-uid;
    range mode is verified on the QA box.
- **Parallel:** fully. Rebase with dl/confine-dirfrom.
- **Landed (branch `p2/cg-backup`) — notes and deviations:**
  - **`cgroup`** (new `leaf_linux.go`): `AddWith(name, pid, Limits) (leaf
    string, err error)` returns the registry's `Leaf` (`""` without
    delegation, no error). `Limits` gains `CPUMax` (µs per 100 ms period,
    so `vcpus × 100000`) and `MemHigh` (0 = ⅞ of `MemMax`, <0 = none, the
    VM row). A leaf of the same name is `rmdir`'d first — EBUSY while an
    orphan populates it fails the start, and a fresh leaf means no stale
    limits carry over. `Kill` writes `cgroup.kill`, else SIGKILLs every
    listed pid each round, and **waits** (≤ 5 s) until `populated 0`, so a
    stop's `Remove` follows directly. `Populated(name) bool`.
    `Sweep(prefix) ([]string, error)` refuses an empty prefix. Names are
    one path segment. `AddMem` is now `MemHigh: -1` (same writes).
  - **Real cgroupfs** (`leaf_integration_test.go`, `linux && integration`):
    a populated leaf's rmdir is EBUSY, `cgroup.kill` empties a tree, and
    an unreaped zombie doesn't hold `populated`. It needs a delegated
    cgroup holding only the test binary, so it isn't in `make
    integration` (the `go` tool shares its cgroup and it would skip): build
    with `go test -c` and run under `systemd-run --user --scope -p
    Delegate=yes` (recipe in the file).
  - **`sandbox.Spec.FileCaps`** (with `Unprivileged`; wins over `NetAdmin`
    and `Containers`) in `filecaps_linux.go`: the six caps, the VM jail's
    block-list (`vmDeny`: backend minus `mknodat`), and nested user
    namespaces pinned to zero like the VM jail (`setUserNSLimits`).
  - **`confine`**: `Cmd.FSCaps`, plus the helpers the §8.3 bullets need, in
    `tree.go`: `DiskUsage(ctx, dir)` (`du -skx`, allocated bytes),
    `RemoveAll(ctx, dir)` (a confined `find dir -xdev -mindepth 1 -delete`,
    then xbind `rmdir`s the empty dir — a bind's root can't be removed from
    inside) and `CopyTree(ctx, src, dst)` (`cp -a --reflink=auto -- src/.
    dst/`, src bound read-only; plain `-a` in a direct run). Paths are
    absolute, clean, not `/`, and xbind-created in every component (the
    bind follows a link there; dl/confine-dirfrom's fd binds can tighten
    this).
  - **The integration test** (`confine/tree_linux_test.go`) copies,
    measures and removes a whiteout, a 0600 and a 0000 file and a sealed
    dir, plus — where the host delegates a sub-uid range, as this box now
    does — a 0700 dir and 0600 file `chown`ed to sub-uid 1000, which xbind
    itself can't read; the copy keeps that owner. The capability-less
    profile fails the same `cp -a` (the whiteout's `mknodat`, the locked
    modes) and `find -delete`; `mount`, `mknod c 1 3` and `unshare -U`
    still fail under the file caps.
  - **Found, not fixed:** this box has a sub-uid range now, and
    `internal/sandbox`'s own integration tests `TestSandboxIsolation`
    (hangs) and `TestSandboxServesUnixSocket` (fails) never call
    `SetupUserns`, so a range-mode init waits for its maps. Same on the
    parent commit; the package isn't in `make integration`.
  - **`sessionWhat`** names `the tile sandbox "<name>" of <tile>` from the
    registry id (`Entry.Name` is WP-15a's); also fixes "a agent session".
  - **Not done here:** the term layer's existing `os.RemoveAll` calls
    (WP-9's restore swap, `ResetEnv`, offload-full) still run as xbind;
    `confine.RemoveAll` can take them over. No docs or changelog: nothing
    builder-visible uses the profile until WP-15b/WP-20.

### WP-9 — Backup and restore never follow planted symlinks (Track B · M) — *an existing D78 gap*

- **Goal:** close the storage map's finding before tile sandboxes add more
  state. Today `writeFileFrom` (`MkdirAll`, `Remove`, `Create`) follows
  symlinks already on disk when it restores `source/`, `term/` and resource
  files, as xbind. A tile's owner can trigger a restore (offloaded →
  enabled). `backup.Tree` also opens with `os.Open` after `WalkDir` (a
  TOCTOU).
- **Files:**
  - `internal/backup/backup.go`: `Tree` walks with no-follow dir fds and
    opens through `fsutil.OpenIn`;
  - `internal/broker/backup.go`: restores through `os.Root` (`MkdirAll`,
    `Remove`, `OpenFile`). A `term/` restore extracts into a fresh staging
    dir and swaps it in, only after the sessions holding the layer are
    killed.
- **Tests:**
  - `broker/backup_safety_test.go`: a symlinked dir planted in `source/`, in
    `term/upper` and in a resource mount → the restore writes nothing outside
    the tree; a symlink at a file's path is replaced, and its target isn't
    written.
  - `TestRoundTrip` holds, and so does dev-lifecycle's zero-state archive:
    the bytes are unchanged.
- **Docs:** a changelog security entry. The finding came from reading the
  code; the first test proves or disproves it.
- **Parallel:** fully. Conflicts with dev-lifecycle's `backup.go` edits;
  whichever lands second rebases.
- **Landed (branch `p2/cg-backup`) — notes and deviations:**
  - **Proved.** Written first, against the old code: `restore` wrote
    through a symlinked dir planted in `source/`, `term/upper` and a
    resource mount (all three wrote outside the tree), and `Tree` read a
    file, and a directory's file, swapped for a symlink between the listing
    and the open (`TestTreeNeverFollowsASwappedSymlink`, racing inside the
    `skip` callback). A symlink *at a file's path* was already replaced
    (`Remove` then `Create`), barring a race between the two.
  - **`Tree`** lists each directory from its fd and opens every entry with
    `openat(O_NOFOLLOW)` relative to it (a swapped entry is skipped); only
    the walk's top goes through `fsutil.OpenIn`. **`TreeIn(prefix, root,
    sub)`** is new: the tile's source dir is reached without symlinks (a
    nested tile lives in its parent's writable tree), and a link there fails
    the backup. `TestTreeBytesUnchanged` pins the bytes against the old
    `WalkDir` walk.
  - **Restore** writes through `os.Root`, but **replaces** a symlink met on
    the way (a dir's or a file's path) instead of following it in-tree as a
    bare `os.Root` would; `os.Root` still bounds a link swapped in
    mid-restore. The tile's dir is made and opened without symlinks — new
    `fsutil.MkdirAllIn` / `fsutil.OpenRootIn`.
  - **The term staging dir is `.xbin/restore/<CK>-*`**, not under
    `.xbin/term/` (every dir there is read as a layer by `CheckBaseImages`,
    `pinnedBases`, `vm.ListDisks`). The swap runs under the new
    `Broker.HoldTermEnv` → `term.Manager.HoldEnv` (kill the sessions, wait,
    hold the layer; a hold that times out fails the restore and leaves the
    layer alone), keeps the old layer's `vm/`, ignores `term/vm/…` entries,
    and removes the old layer with `os.RemoveAll` (never follows; WP-8's
    confined remove can take it over for sub-uid-owned uppers). Leftovers
    older than a day are swept on the next term restore. The layer is now
    **replaced**, not merged.
  - **Extra, cheap hardening:** the manifest's `Component` must equal the
    component being restored (it named the host path to write); resource
    data only for a scope-root archive (`Scope == Component`); resource
    names are one path segment; offload-full's `removeSourceBulk` clears
    through `OpenRootIn` (a symlinked tile dir used to aim `RemoveAll` at the
    link's target).
  - **Files, for the size budget:** the restore half of
    `internal/broker/backup.go` moved to `internal/broker/restore.go`, and
    `HoldEnv` lives in `internal/term/holdenv.go` (dev-lifecycle's
    `backup.go` edits rebase onto the split).

### WP-10 — Relay hardening (Track C · S)

- **Goal:** §4's `Deny`, `DNSRefuse` and `Reach`.
- **Files:**
  - `internal/sandbox/relay/types.go` (`Deny`, `DNSRefuse`);
  - `relay.go` (checks first in `handleTCP`, in `permitted`, and in UDP);
  - `dns_linux.go` (REFUSED);
  - `icmp_linux.go` (the nil `allow` check);
  - new `relay/deny.go` (`HostDeny(listen ...netip.AddrPort)`);
  - `internal/sandbox/policy.go` (`EgressPolicy.Reach()`).
- **Tests:**
  - `Deny` beats an `Allow` that says yes, for TCP, UDP, ICMP and a
    DNS-pinned host;
  - `DNSRefuse` answers REFUSED within 50 ms;
  - a nil `Allow` doesn't panic;
  - a table test for `Reach`;
  - the existing relay and pin tests stay green.
- **Parallel:** fully.
- **As built** (branch `p2/relay-net`):
  - `relay.Deny` is `func(netip.Addr) bool`, checked first in `handleTCP`
    (before the gateway forwards and the hairpin, so a relay with `Deny` has
    neither), in `permitted` (so UDP and pinned hosts) and in ICMP. The
    relay's own DNS service isn't a flow to the queried address: `:53` is
    answered (`DNSRefuse`) or forwarded to `Resolver` before `Deny`.
  - `HostDeny(listen ...netip.AddrPort)` denies whole addresses (listen
    ports are ignored). It reads the host's addresses from the **local
    routing table** (Linux: every interface address plus AnyIP ranges;
    elsewhere `net.InterfaceAddrs`) and **re-reads them at most every 5 s**
    instead of once at start, so a VPN or bridge that comes up later is
    denied too. Until a read succeeds, it denies everything. IPv4-mapped
    destinations are judged unmapped.
  - `DNSRefuse` answers every UDP `:53` query REFUSED locally (question
    echoed), records the flow as denied, and caps the answerers at 64.
  - `relay.Config.Processors` sets gVisor's packet processors (0 = one per
    CPU, today's default for every existing relay). §14's saving is opt-in:
    tile sandboxes pass `Processors: 1` (added to §4's config).
  - `EgressPolicy.Reach()` returns `sandbox.ReachNone|ReachInternet|
    ReachOpen`. A prefix is "wholly public" when it overlaps none of the
    ranges `isPublic` refuses (tested /16 by /16 against it).
  - Found on the way and fixed:
    - `isPublic`/`publicAddr` didn't unmap before `IsUnspecified`, so the
      `net:internet` policy admitted `::ffff:0.0.0.0` (a dial to it reaches
      the host). gVisor drops IPv4-mapped destinations, so it wasn't
      reachable: defense in depth, no changelog entry. `Reach` needs the
      policy to be exact anyway.
    - `Relay.Close` never stopped the TUN's reader and its per-CPU
      processors. They leaked until the TUN died and could read whatever
      file reused the fd number. `Close` now stops them before it returns
      and is idempotent, and a ping reply that lands after it no longer
      writes to the closed fd's number.
    - The forwarders are installed before the NIC starts reading: a packet
      already queued on the TUN raced them.
  - Tests: `relay/hardening_linux_test.go` drives a real gVisor stack over a
    SEQPACKET socketpair with vetted dials (no privileges);
    `sandbox/relay_linux_test.go` (integration) runs a namespace sandbox
    behind an allow-all relay: without `Deny` it reaches the host's address
    and the gateway forward, with `HostDeny` + `DNSRefuse` both are reset
    and a lookup fails at once.

### WP-11 — The `sandbox-net` interface kind (Track C · M · after WP-10)

- **Goal:** §4's binding side.
- **Files:**
  - `internal/registry/registry.go` (`KindSandboxNet`, validation at load);
  - new `internal/broker/sandboxnet.go` (`SandboxNet`, `SandboxEgress`,
    `SandboxNetClasses`, validate and options helpers,
    `OnSandboxNetChange`);
  - `broker/netfn.go`: at most 35 lines, since it is at 1131/1170. It adds
    dispatch into `sandboxnet.go`, `sandboxNetOptions` on `GET /bindings`
    (additive), and the deterministic `net` slot;
  - `broker/delegated.go` (`bindingTargetsPaired`);
  - `broker/transfer.go`, `netsetsapi.go`, `netsetref.go` (the kind is
    included);
  - `cmd/bx/doctor.go`;
  - `workspace-template/tiles/admin/tabs/binding.js` and
    `workspace-template/shell/bx-tile-admin.js` (rows through
    `_netBindRow`);
  - docs: `docs/elements.md` (interfaces), `docs/isolation.md` (egress),
    `docs/protocol.md` (the bindings answer), changelog.
- **Tests:**
  - broker unit: refusals (host, a provider, a host-bearing set, multiple
    refs); the D54 ceiling; a D26 org admin within the allowance; D88
    self-approval; unbound = none; the inert note; `OnSandboxNetChange`
    fired on bind, unbind and set edits; the backend not restarted; the
    deterministic `net` pick.
  - Harness: the bindings pass renders a sandbox-net row.
- **Parallel:** with WP-12.
- **As built** (branch `p2/relay-net`):
  - `registry.KindSandboxNet`, `ValidSandboxNetSlot` and `ValidateInterfaces`
    (at load, after `ValidateExposes`): a sandbox-net slot is request-side,
    takes no `multi`/`service`/`role`/`instances`, and is named
    `[a-z0-9][a-z0-9_-]{0,31}` (it is spelled `class:<slot>`). A bad one is a
    `ManifestErr`, is no class, and refuses binding.
  - `broker/sandboxnet.go`: `SandboxNet{Class, Slot, Ref, Reach, Rules, Note,
    Policy}`, `SandboxNetClasses(tile)` (none first, then slots by name),
    `SandboxEgress(tile, "" | "none" | "class:<slot>")` (an error when the
    selector names no class of the tile), `OnSandboxNetChange` (a `Broker`
    field; boot wiring waits for the runtime, WP-15). The net case of
    `validateBinding` moved into `validateNetRef`, shared by both kinds with
    net's messages unchanged, so `netfn.go` shrank (1131 → 1099; its budget
    line is left alone to spare merges).
  - **Host inside a network.** Binding a set that says host is refused (as
    planned). An `org` or `personal` network whose rules include host is
    allowed: the class gets the other rules and a `note` says so (a host-only
    one is inert). The picker labels them.
  - Inert classes show in the bindings answer's `inert` by slot; `bx doctor`
    words them as sandbox classes.
  - `OnSandboxNetChange` fires on bind/unbind (which then skips every
    `OnGrantChange`), on a set edit (tiles with a class bound to it), on an
    org attachment (the org's tiles with classes), on a personal holder change
    (personal defaults included) and on a transfer. A D20 policy-row edit fires
    nothing, as for net slots: the runtime resolves at every start (WP-15b may
    also reconcile on the hub's `users` events).
  - **Added for WP-15b:** `sandbox.EgressPolicy.Covers(q)` is §4's superset
    test: each rule of the old policy inside one rule of the new (an internet
    rule covers host rules and wholly public prefixes; ports must match). It
    is conservative, and a randomized test checks it against `Allow`.
  - UI: `netOptions({…, sandbox: true})` in `web/bx-netrules.js` (unbound =
    "no network", no host, no providers, a set that says host greyed) feeds
    `_netBindRow` and the tile popover (`tr[data-kind=sandbox-net]`);
    `hack/netrules.test.mjs` pins it. The harness pass `sandboxNet`
    (`passes/sandboxnet.js`) creates its own manager tile, so the seed is
    unchanged, and leaves both classes bound to `none`.
  - Docs also cover `docs/overview/11-interfaces.md` and `12-egress.md`. The
    runtime-side sentences there and in `isolation.md` (a narrowed class stops
    the sandbox, a widened one waits for the next start, the relay config)
    describe WP-15a/15b.

### WP-12 — `internal/termwire` (Track C · M)

- **Goal:** the `/ws/term` wire as a package (the terminal map's §A), plus
  two fixes:
  - a slow client that is dropped gets **no** `exit` frame (it reconnects
    and replays), which fixes a false "shell ended";
  - `SetReadLimit` for tile-facing sockets.
- **Files:**
  - new `internal/termwire/{hub.go, echo.go, upgrade.go}`: `Hub`
    (`Output`, `End(exit)`, `Attach(conn, hello, Terminal)`, `Clients`,
    `LastActive`, `Tail`, `OnClients`), `Terminal{Write, Resize}`, `Upgrade`;
  - `internal/term/attach.go` becomes the adapter;
  - `term.go`, `sessions.go` and `agent.go` switch to the hub;
  - `attach_test.go` moves to `termwire/echo_test.go`;
  - `hack/size-budget.txt` (term.go's budget lowered);
  - docs: `docs/protocol.md` §`/ws/term` (the optional `code`/`signal` on
    exit and `sandbox` on session; no exit on a drop), changelog.
- **Tests:**
  - new `termwire/hub_test.go` (httptest + gorilla): hello first; replay
    order; an ack after output; the pong echoes `t`; exit only when dead; a
    slow client is dropped without exit; the read limit.
  - Harness: predict.js, termrun.js, termsessions.js and vmtoggle.js pass.
- **Parallel:** fully. `hack/size-budget.txt` conflicts with dev-lifecycle's
  term.go line; whichever lands second rebases.
- **As built** (branch `p2/termwire`):
  - The API: `NewHub(maxTail)`; `Terminal{Write func([]byte) error,
    Resize func(cols, rows uint16)}` (resizes outside 1..65535 never reach
    it); `Exit{Code *int, Signal string}` with `ExitCode(n)`/`ExitSignal(s)`
    (the zero `Exit` is a bare `{"op":"exit"}`); `Attach(conn, hello
    map[string]any, t)` sets `op` and `echoAck` itself; `Upgrade(w, r, hdr,
    readLimit)` with `ReadLimit` (1 MiB) for tile sockets. **Added:**
    `Touch()`, since an agent session's events move its activity clock and it
    has no output.
  - `/ws/term` keeps no read limit (0): a paste over 1 MiB was always one
    frame there, and bounding it now would break it. Its shells still send a
    bare exit at PTY EOF, before the reap, so a VM's 3 s hang-up grace doesn't
    hold the pane open. A tile TTY passes the code or signal.
  - A dropped client's socket is closed at once (off the lock), not drained.
  - The session frame now goes first on an already-ended session too (before
    the replay and the exit), as the contract states.
  - `OnClients` calls are serialized and read the count as they run, so the
    last one is always current.
  - Extra test: `internal/term/attach_test.go` drives `/ws/term` over a real
    PTY (a host shell, isolation off): the session frame, the echo ack, a
    reattach that replays, and the exit.

### WP-13 — `tilesbx` skeleton + the whole route surface (Track D · M)

- **Goal:** the definitions store, the policy and the gates, with every §3
  route registered and documented from day one. Routes not built yet answer
  501 `unsupported` from their own files, so wave 2 fills method bodies and
  never touches boot, openapi or the route list.
- **Files:**
  - new `internal/tilesbx/{manager.go (Manager, Deps: small interfaces for
    caps, net, mounts, vault, users, disk, launcher), keys.go, defs.go,
    policy.go, info.go, errors.go, api.go (gates; lenient decode;
    runtime/policy/list/create/get/patch/delete), api_lifecycle.go,
    api_exec.go, api_files.go, api_snapshots.go}`; the last four are 501
    stubs;
  - new `internal/boot/tilesandboxes.go`: the `RegisterAPI` literals,
    `sandboxScopeOf`'s manager case, and a step `tile-sandboxes` after
    `vm`;
  - `internal/boot/boot.go` (+1 step) and the step-order test;
  - new `internal/server/openapi_sandboxes.go`; `openapi.go` appends it and
    updates the `GET /sandboxes` capability;
  - `internal/server/server.go` (`auditable` exclusions);
  - new `internal/broker/sandboxmounts.go` (`ResourceMount`, §5: create and
    start validate mounts through it);
  - docs: `docs/protocol.md`, a new §"Tile sandboxes (manager tiles)" with
    every route, body and error and the TTY note; `docs/isolation.md`
    (short); changelog.
- **Tests:**
  - gates: the instance principal with the cap passes; the same tile's frame
    principal, a terminal, cron and a user get 403; an admin may list, stop
    and delete but gets 403 on create, run, files and tty;
  - definitions CRUD and persistence; the name grammar and reserved names;
    a `clientId` repeat answers 200, a conflict 409;
  - clamping; mount validation (fake Deps);
  - the policy merge; lenient decode; body caps;
  - `apicheck`, `openapi_test` and the boot order test green.
- **Parallel:** fully. The real `SandboxesFor` arrives with WP-5; until then
  a Deps fake stands in.
- **As built (notes):**
  - `Deps` has `Caps, Admin, Net, Mounts, Vault, Users, Disk, Modes,
    Tiles` — **no `Launcher`**: its shape follows WP-15a's Spec builder, so
    WP-15a adds it. The lifecycle seams are stubs in `manager.go` that
    WP-15a/b fill: `start` (answers unsupported), `stop` (nothing runs),
    `removeState` (refuses while a state dir exists — no confined remove
    yet — and the definition is kept), and `box`, the live state.
  - Validation lives in a new `validate.go`. A PATCH checks only the fields
    it changes, so an unrelated edit never fails on reach the tile lost
    since (the start re-checks everything).
  - **Mounts are same-scope only.** §3.3's "or one granted to it" would hand
    a cross-scope `filesystem` path out, which EnvFor never does
    (docs/resources.md) and D120's decision 7 ("the manager's own
    `filesystem` resources") doesn't ask for; `broker.ResourceMount`
    refuses another scope's resource even when granted. **The owner
    confirmed it** (D120 addendum: same scope only in v1). Its verifier
    found one divergence, fixed by WP-13b: a same-scope grant without a
    `uses` entry still resolves.
  - Sizes are stored resolved (defaults filled, clamped at create) and are
    clamped again to the caps of the moment on read and at start.
  - The boot wiring picks up the broker's `SandboxesFor(tile string) bool`
    by interface assertion once WP-5 adds it; until then no tile holds the
    cap (a boot warning says so). `Modes.VM` answers unavailable ("this
    xbind can't run VM tile sandboxes yet", or the host's reason) until
    WP-6/WP-16; `Net` is unwired (no classes) until WP-11.
  - `runtime.caps` is `builtCaps` (`info.go`), empty until wave 2 serves a
    capability. `stop` is a 501 stub like the other lifecycle routes (an
    admin passes its gate, then 501). `start: true` on create answers 201
    with the start's failure in `stateDetail`. An admin's `&deployment=`
    answers unsupported. An unreadable definitions file, or one from a newer
    xbind, makes the store read-only (writes 503) — it is never clobbered.

### WP-14 — The SDK (Track D · M · after WP-13's docs)

- **Goal:** §10.
- **Files:**
  - new `sdk/{sandbox.go, sandbox_exec.go, sandbox_files.go,
    sandbox_tty.go}` and `sdk/sandbox_test.go`;
  - `docs/sdk.md` (§ Sandboxes, for manager tiles); changelog.
- **Tests** (a fake gateway: an httptest server on a unix socket, as in
  `notify_test.go`, with `XBIN_GATEWAY`/`XBIN_TOKEN` set):
  - every call's method, path and body; the Bearer added;
  - errors mapped to `*SandboxError`;
  - `Follow` across a gap and to the end;
  - streaming `Stdin`, `WriteFile` and `PutTar`;
  - `Forward` of a WebSocket upgrade end to end (a fake runtime with
    `sdk/ws.Upgrade`, a `sdk/ws.Dial` client): bytes pass both ways, and the
    inbound `Cookie`, `Authorization` and `X-XBin-*` aren't forwarded.
- **Depends on:** WP-13's protocol text, not its code. `sdk/ws` from
  `p3/prep` for `DialTTY` and the WS test; `Forward` doesn't need it.
- **Parallel:** fully.
- **As built (notes):**
  - **Wired since:** `p3/prep` merged, and b9f70575 turned `DialTTY` and
    its tests on and documented it; the rest of this bullet is history.
  - **`DialTTY` waits for `sdk/ws`.** `sdk/ws` is committed on `p3/prep`
    (769d9654), not on this branch. `DialTTY` and the two `sdk/ws` tests
    (`DialTTY`, and `Forward` of an upgrade with `ws.Upgrade`/`ws.Dial` on
    both ends) are written against its documented API in
    `sdk/sandbox_dialtty.go` and `sdk/sandbox_dialtty_test.go`, both behind
    `//go:build ignore` with a "WIRE ON MERGE" note. They pass in a scratch
    copy of the module with `p3/prep`'s `sdk/ws`. Once both branches are
    merged, the integrator deletes the first two lines of each file (the
    build line and the blank line after it: deleting only the build line
    leaves a file gofmt rejects) and adds `DialTTY` to docs/sdk.md. The
    compiled `RelayTTY` test drives the upgrade with a raw
    handshake (a browser's masked frame through, raw bytes back), which is
    what "byte for byte" means anyway.
  - **Small additions to §10:** `WriteSandboxError(w, err)` answers a
    `*SandboxError` in the contract's shape, so a manager passes a refusal
    on unchanged. `OutputChunk.Bytes()` decodes a base64 chunk.
    `Sandbox.Name()`.
  - **Route safety in the SDK.** A name, exec id or snapshot id that would
    change the route (empty, `.`, `..`, one with a `/`) and the reserved
    names are refused (400 `invalid`) before anything is sent. `Forward`
    escapes each segment of `sub` and refuses the same segments.
  - **`Refusal` is the runtime's own**, never derived from the status. A
    plain 404 from an xbind without these routes doesn't read as
    `ErrSandboxNotFound`.
  - **`Follow`** asks for `base64`, so the bytes are exact and a character
    split between two reads stays intact; `Bytes()` decodes. It yields
    chunks that carry bytes or show a gap, then the last one. It skips
    empty long-polls, and waits 250 ms after an empty answer that came back
    at once, so it never spins.
  - `ReadFile`'s `*FileStat` carries only `Path` and `ETag`; `Stat` has the
    rest. `Forward` also strips `Set-Cookie` and `X-XBin-*` from the
    answer, and answers 503 `unavailable` when xbind can't be reached.
    `uid`/`gid` are `*int` in the SDK. `Reset` and `Rebase` take `wait`
    like `Start` (§3.1 accepts `?wait` on every lifecycle call). The
    snapshot calls live in `sandbox.go`.

### Follow-ups to wave 1

Each changes code wave 1 already built, because the review of waves 2–3 or a
wave-1 handoff changed what it must do. They are small, and all but WP-3b
and WP-2b can start now. Each ends green on `make check` like any WP;
"now" means its inputs are all on `sandbox-runtime`.

#### WP-2b — `NoFollow` for terminals and backends (F · S/M · after WP-3b and WP-10b, which share its files)

- **Goal:** close the D78 gap WP-2 found outside tile sandboxes. A
  terminal's or backend's mount points (MkdirAll plus a mount by path)
  still follow a symlink planted in its persistent upper: the old code made
  `xbin/` on the host where a planted `/opt` pointed (it creates, never
  overwrites), and a planted `/proc` symlink wedged the init and
  fuse-overlayfs in a FUSE wait.
- **Work:**
  - A survey first: list every mount point `internal/term` and
    `internal/runner` pass (`/opt/xbin`, `/run/xbin`, `/work`, the home,
    resource paths, `/proc`, `/tmp`, `/dev`) and check each against the
    shipped rootfs for a symlink on the way (`docker/rootfs.Dockerfile`'s
    image and a current `.rootfs`). The result goes in this WP's as-built
    note.
  - Then either turn `Spec.NoFollow` on for both, or, where the rootfs
    ships a symlink a mount point legitimately passes, a variant that
    follows a symlink only when it comes from the lower (the entry doesn't
    exist in the upper, checked by the init with `openat2(RESOLVE_BENEATH |
    RESOLVE_NO_SYMLINKS)` on the upper dir) and refuses one planted in the
    upper.
- **Files:** `internal/sandbox/init_linux.go` / `nofollow_linux.go`,
  `internal/term` and `internal/runner` Spec call sites; a changelog
  security bullet.
- **Tests:** a planted `/opt` → host path and a planted `/proc` in a
  terminal's upper: the start refuses (or ignores the link), writes nothing
  on the host and doesn't wedge; the existing terminal and backend suites
  stay green on the rootfs.
- **Never break users:** a terminal whose upper holds a planted link at a
  mount point fails to start with a message naming the path and `bx term
  reset`, instead of silently making host dirs.

#### WP-3b — The agent dies with its root; user work dies first (A · S · after WP-1, WP-3)

- **Goal:** §2.4's two rules: fuse-overlayfs is watched, and sessions carry
  `oom_score_adj` 500.
- **Files:**
  - `internal/sandbox/sandbox.go` (`Spec.FuseWatch bool`, json `fuseWatch`),
    `init_linux.go` (under `FuseWatch`: start fuse-overlayfs with `-f`,
    wait for the FUSE mount by `statfs` magic, fail on an early exit with
    its output, append `--fuse-pid P`; without it, today's daemonizing
    `CombinedOutput` path is untouched);
  - `internal/sandbox/agentcore` (`Options.SessionOOMScoreAdj`, applied to
    sessions ≥ 2 right after start; `Spawner.Register` for the fuse pid);
  - `agentcore/ns_linux.go` + `cmd/bx/sbxagent.go` (`--fuse-pid`; exit 3
    when it exits; `SessionOOMScoreAdj: 500`);
  - `internal/sandbox/vm/guest/agent_linux.go` (`SessionOOMScoreAdj: 500`;
    session 1 is unaffected by the ≥ 2 rule).
- **Tests** (integration, both overlay flavours where fuse applies):
  - SIGKILL fuse-overlayfs from outside → the init's process exits 3 within
    1 s and the pid namespace is empty;
  - a session's `/proc/<pid>/oom_score_adj` reads 500, the agent's 0;
  - a start whose fuse-overlayfs fails to mount answers with its output;
  - a terminal (no `FuseWatch`) still daemonizes it (unchanged).
- **As built** (branch `p2/wp3b`):
  - *`Spec.FuseWatch` needs `Spec.Agent`*: Launch refuses it otherwise,
    since no other entry would hear. It does nothing over a kernel overlay
    or in a VM. The init's side is `internal/sandbox/fusewatch_linux.go`
    (`mountFuseWatched`); `mountRoot` returns the pid, and `entryArgv(s,
    fusePID)` appends `--fuse-pid P` after `--fd`/`--lock`, to a
    namespace-mode agent only.
  - *fuse-overlayfs is started as a daemon places itself*: `-f`, in `/`
    (pivot_root then moves its cwd into the new root, as it did the
    daemon's) and in a session of its own (`Setsid`: a ^C to the process
    group xbind runs in doesn't reach it). Its stdout and stderr go to a
    memfd, not xbind's log pipe: the init quotes its last 4 KiB when it
    fails, as `sandbox-init: fuse-overlayfs mount: it exited before
    mounting the root (exit status 1): <output>` or `… no FUSE mount after
    10s: <output>` (exit 127), and traces it under `Debug` otherwise, as
    the old path hid its harmless `lazytime` warning. Whatever it prints
    later stays in the memfd; it is quiet without `-d`.
  - *The 10 s bound is a watchdog*, not only the poll: statfs on a FUSE
    mount asks its server, so one that mounted and never answers would
    hang the init. At 10 s the watchdog kills it, which aborts the
    connection and ends the wait.
  - *The init never reaps it.* It looks with `waitid(WNOWAIT)`, so an exit
    between its last look and the exec is still a zombie for the agent's
    `PID1Spawner`, which remembers the status for `Register` (called before
    the first session).
  - *The agent:* `RunNamespace(agentFD, lockFD, fusePID)`; `--fuse-pid`
    takes a pid ≥ 2. When fuse-overlayfs exits, the agent logs `sbx-agent:
    the root filesystem is gone: fuse-overlayfs (pid N) was killed by
    SIGKILL` (or `exited with code N`) and returns
    `agentcore.ExitRootGone` (3) at once, with no sync (nothing can be
    flushed). The constant is in an untagged `agentcore/exit.go` for
    WP-15a's teardown to map. The watch ends with the factory: a stop's
    exit stays 0 even when `exitSync` then kills a stopped fuse-overlayfs.
    Measured here: the agent exits and the pid namespace is empty 2–3 ms
    after the SIGKILL.
  - *`Options.SessionOOMScoreAdj`* is clamped to ±1000 and written right
    after the spawn, before `started` is sent. A write that fails because
    the session already ended (gone, or a zombie, whose `oom_score_adj` is
    root's: `EACCES`) is silent; any other failure is logged. That is
    chiefly a target below `oom_score_adj_min`, the floor a privileged
    writer sets (systemd's `OOMScoreAdjust=` for xbind's unit): an
    unprivileged write may lower a score down to that floor, not past it.
    So a session can lower its own score back to the agent's, and a root
    session can raise the agent's (its `/proc/1` belongs to the sandbox's
    root once the agent is non-dumpable). Both only change which of the
    sandbox's own processes an OOM takes: code in a sandbox can always end
    its own sandbox.
  - *Tests.* `agentcore/procattr_test.go` (unit): session 1 and the agent
    keep the test's score, sessions 2 and 3 read 500, a reaped or zombie
    process logs nothing, and without the option nothing changes.
    `agentcore/ns_fuse_linux_test.go` (integration, `TestNamespaceAgentRoot`,
    minimal lower, so CI's `bin/fuse-overlayfs` runs it):
    - a stand-in that fails (the probe binary) and one that never mounts (a
      10 s script): 127, the output quoted;
    - the real binary refusing a missing lower: its own lines quoted;
    - watched: the agent's child, `-f`, its own session, `--fuse-pid` equal
      to its in-namespace pid, sessions at 500 (and as uid 1000 in range
      mode), then SIGKILL from the host: exit 3 and an empty pid namespace
      within 1 s;
    - unwatched: a daemon (its own session, no `-f`), and the agent gets no
      `--fuse-pid`.

    `TestNamespaceAgent` now sets `FuseWatch` as the runtime will, and
    checks the scores over both overlay flavours. `internal/vm`'s
    `TestResidentVM` reads `500` for an exec and `0` for the guest's agent
    (KVM and emulated). Mutation-checked: without the `Register`, without
    `-f`, and without the write, the tests fail. The namespace score checks
    first lower the test's own score to 0 (`lowerOwnOOMScore`): a CI runner
    may start its jobs at 500, where they would otherwise skip.
    `TestFuseWatchNeedsAgent` pins Launch's refusal.

#### WP-7b — State layout: `<name>.<uid>/cur/`, snapshot stamps, pins with errors (B · S · now; before WP-15a)

- **Goal:** §1.3's layout in the readers WP-7 built, before anything stamps
  a tile layer.
- **Files:**
  - `internal/layers`: `List`/`Check`/`Pinned` read a tile sandbox's stamps
    at `.xbin/sbx/<CK>/<name>.<uid>/cur/` and each
    `snapshots/<sid>/`; `Layer` gains `UID` and splits the dir name at the
    last `.` into `Sandbox` and `UID`; `.trash` and anything not matching
    `<name>.<uid>` pins nothing and isn't a layer; `Pin`/`Stamp` take the
    `cur` dir;
  - `Pinned(ws, extra func() ([]string, error))`: an error from `extra`
    joins the returned error, so an unreadable definitions file makes the
    set unknown (nothing released);
  - `internal/boot/boot.go`: `State.sandboxBasePins func() ([]string,
    error)`; `pins_test.go`;
  - `internal/vm/disk.go`: `ListDisks` globs `.xbin/sbx/*/*/cur/vm/disk.img`,
    `Disk.Sandbox` is the name and a new `Disk.SandboxUID` the uid;
    `EnsureDiskAt(cur)` unchanged in shape;
  - `internal/term/base.go`: `ensureLayerBase` reads `if s, _ :=
    layers.Read(layer); s.Base != ""`, so a bad `overlay` stamp never
    discards a readable base (WP-7's verifier note);
  - `internal/boot/sandboxes.go` + `workspace-template/tiles/admin/tabs/sandboxes.js`
    + the harness fixture row: the disk row shows the name (and the uid on
    hover).
- **Tests:** `layers` unit: a `cur` stamp and a snapshot stamp pin; `.trash`
  doesn't; a malformed dir name is skipped; `extra` erroring makes
  `Pinned` error. `vm` `TestListDisks` for `cur/vm/disk.img`. `boot`
  `TestPinnedBases` with an erroring source.
- **As built (p2/wp7b).** Additions, no departures:
  - `layers.CurDir` (`"cur"`) and `layers.SplitStateDir(dir) (name, uid,
    ok)`: the split at the last `.`; the uid must be 12 lowercase hex, the
    name non-empty and not starting with `.` (so `.trash`, hidden and
    uid-less dirs are skipped). The name's grammar is `tilesbx`'s and isn't
    re-checked here: a widened name grammar must never un-pin a sandbox's
    base. `vm.ListDisks` uses the same split; WP-13b's `StateDir`/`CurDir`
    can build on it.
  - A sandbox's `Layer.Dir` is its `cur/` (a snapshot's, `snapshots/<sid>/`);
    both carry `Sandbox` and `UID`. A state dir with no `cur/` (never
    started) is listed and pins nothing; a `cur` that is a symlink or a
    file is listed with `Err` (so the pins are unknown), never followed.
  - `Pinned` keeps what `extra` returned alongside its error (the caller
    releases nothing on an error anyway).
  - `Disk.SandboxUID` is `sandboxUid` in JSON (`GET /sandboxes`' disk
    rows; protocol.md, openapi and the WP-7 changelog bullet amended, no
    new bullet). `ListDisks` also skips a disk whose `cur` isn't a real
    dir; a disk in `.trash` isn't listed (it is being removed).
  - `boot/sandboxes.go` needed only its comment (the embedded `vm.Disk`
    carries the uid). The admin tab puts the uid in the sandbox name's
    hover title; the harness `sandboxes` pass asserts it.
  - `term`: `TestEnsureLayerBaseKeepsBaseOverBadOverlay` pins the
    `ensureLayerBase` fix; `TestCheckBaseImagesIgnoresTileSandboxes` uses
    the `cur/` layout (and checks both are listed as layers).

#### WP-8b — The `comp-tilesbx` parent, cgroup-fd starts, xattr-exact copies (B · S · now; before WP-15a)

- **Goal:** §6.2's parent and clone3 placement; §3.9's xattr rule in the
  confined copy.
- **Files:**
  - `internal/cgroup/leaf_linux.go` (+ `cgroup_other.go` stubs):
    - `(*Manager).Parent(name string, l Limits) (*Manager, error)`: makes
      `<base>/comp-<name>` with `memory.max`/`pids.max` from `l`, enables
      `+cpu +memory +pids` in its `cgroup.subtree_control`, and returns a
      Manager whose `AddWith`, `Kill`, `Populated`, `Remove`, `Sweep`,
      `Usage` work inside it only. `SetLimits` re-writes its limits (a
      policy change). Disabled Manager → disabled child.
    - `(*Manager).Prepare(name string, l Limits) (dir *os.File, leaf
      string, err error)`: creates the leaf with its limits, joins nothing,
      and returns an `O_DIRECTORY|O_CLOEXEC` fd for
      `SysProcAttr.CgroupFD` (nil, "" when disabled). Same stale-leaf rule
      as `AddWith`.
    - `(*Manager).OOMKills(name string) int64`: `memory.events` `oom_kill`.
  - The name the runtime uses is `tilesbx-<ws8>` (so `comp-tilesbx-<ws8>/`
    on disk); `Sweep(prefix)` stays for callers that need it, and the
    runtime sweeps the parent's children with `Sweep("sbx-")` on the child
    Manager.
  - `internal/confine/tree.go`: `CopyTree` runs `cp -a --preserve=xattr
    --reflink=auto -- src/. dst/` in both the confined and the direct run.
- **Tests:**
  - cgroup unit (temp dir as base): the parent's limits and
    `subtree_control` written; a child's leaf lives under it; the child's
    `Sweep` never touches a sibling `comp-sbx-foo` of the base (a backend
    leaf of a tile at path `sbx-foo`); `Prepare` + `OOMKills` on a fake
    `memory.events`.
  - The real-cgroupfs integration test (`leaf_integration_test.go`, run by
    its recipe) gains: a child started with `UseCgroupFD` into a prepared
    leaf is in it before its first instruction (a fork in `TestMain`'s
    helper lands in the leaf); the parent's `memory.max` binds the children.
  - confine integration: a tree with `user.test` xattrs, a fuse-overlayfs
    opaque marker and a `userxattr` overlay's `user.overlay.opaque` keeps
    them through `CopyTree`; copying onto a filesystem that refuses
    `user.*` xattrs (tmpfs without `user_xattr`, where available) is an
    error, not a silent drop.
- **As built (branch `p2/wp8b`) — notes and deviations:**
  - **On disk, a parent's leaves keep the `comp-` prefix:** a Parent's
    Manager names leaves exactly as the base one does, so the runtime's
    leaf `sbx-<CK>-<name>` is `comp-tilesbx-<ws8>/comp-sbx-<CK>-<name>/`.
    The registry's `Leaf` stays the name (`sbx-…`), as for `AddWith`.
  - **`Parent`** keeps a parent a previous xbind left (leaves and all: the
    runtime then runs the child's `Sweep("sbx-")`); a zero cap is written as
    `max`, so a policy change can lift one; only `MemMax`/`PidsMax` apply
    (a parent gets no `memory.high` or `cpu.*`). It enables `+cpu +memory
    +pids`, one at a time if the base doesn't pass one on (cpu, say), and
    **errors when memory can't be enabled** (a process left directly in the
    dir, a broken delegation), and when `memory.max` can't be written;
    `pids.max` is best-effort. WP-15a decides whether to run on without the
    parent (a disabled child) and says so in the health view.
  - **`SetLimits` now returns an `error`** (every existing call is a
    statement, so nothing else changed): on a Parent's Manager it re-writes
    `memory.max`/`pids.max` and reports a failed write; on the base it
    stores the shared caps as before and returns nil.
  - **`Prepare`** opens the leaf `O_RDONLY|O_DIRECTORY|O_CLOEXEC` and
    shares `AddWith`'s stale-leaf rule (`makeLeaf`). WP-15a's `ENOSYS`/
    `EINVAL` fallback can call `AddWith` on the same name right after: it
    `rmdir`s the empty prepared leaf and makes it again.
  - **`OOMKills`** counts the leaf's subtree (`memory.events` is
    hierarchical), whichever limit was hit; the integration test shows a
    parent-limit kill lands in the leaf's `oom_kill` with the leaf's own
    `oom` at 0 and the parent's at 1.
  - **For WP-15a/WP-19: the base Manager can't see a parent's leaves.**
    `runner/stats.go` and `boot.sessionLimitAlerts` query registry leaves
    through the base Manager (`Procs`, `Usage`, `AtLimit`); for tile rows
    they must go through the tilesbx parent's Manager (e.g. `Deps.Cgroup`
    held by the runtime and passed to the observers), or tile sandboxes get
    no cgroup stats and no OOM/pids alerts.
  - **A name collision, left as is:** the parent shares the base's
    namespace with backend leaves (`comp-<CompKey>`). Only a tile at path
    `tilesbx` can meet it, and only when the 8 hex of its `CompKey` hash
    equal the workspace path's (2⁻³² per workspace); then the two share one
    cgroup dir (`Parent` fails while the backend runs in it).
  - **`CopyTree`** runs `cp -a --preserve=xattr --reflink=auto` in both
    runs (the direct run used plain `-a` before). GNU cp: a busybox host
    running without isolation would fail the direct copy, and nothing
    copies trees without isolation (tile sandboxes need it).
  - **Tests.** cgroup unit: `TestParent` (limits, controllers, a leftover
    kept, the leaf inside, the parent's `Sweep` leaving `comp-sbx-foo` of
    the base unkilled and in place, `SetLimits` on each, disabled, bad
    names), `TestPrepare` (the fd is the leaf's dir and close-on-exec,
    limits written, nothing joined, stale leaf, `OOMKills`). The
    real-cgroupfs test gains a `TestMain` whose re-exec is a helper
    (`fork`: forks `cat /proc/self/cgroup` at once and prints both;
    `hog`: 1 GiB touched) and `TestParentOnCgroupfs`: both lines name the
    prepared leaf under the parent, and a hog in a 1 GiB leaf under a
    96 MiB parent (swap.max 0) is SIGKILLed; checked failing with
    `UseCgroupFD: false` and with a 4 GiB parent. confine:
    `TestCopyTreeXattrsDirect` (unit, linux), and in the integration file
    `TestCopyTreeXattrs` (confined: `user.test`, a binary value,
    `user.fuseoverlayfs.opaque`, `user.overlay.opaque`, two
    `override_stat`s, exactly) and `TestCopyTreeRefusedXattrs`. **The
    refusing filesystem is a FUSE loopback with `DisableXAttrs`** (go-fuse,
    already a dependency; skips without FUSE), not tmpfs: tmpfs takes user
    xattrs since Linux 6.6 (this box runs 7.1) and has no option to refuse
    them. It checks direct and confined: a tree without xattrs copies onto
    it; the xattr tree fails with cp's "setting attributes …: Operation not
    supported"; with plain `-a` both runs pass silently (checked). The
    direct subtest needs no rootfs, so CI runs it wherever the runner has
    FUSE.
  - No docs or changelog: nothing builder-visible uses any of it until
    WP-15a/WP-20.

#### WP-9b — Terminal-layer removals confined; offload-full holds the layer (B · S · now)

- **Goal:** the four removals WP-8 and WP-9 left as xbind `os.RemoveAll`
  on sandbox-written trees go through `confine.RemoveAll` (with `FSCaps`),
  which in range mode can delete sub-uid-owned files that xbind can't (so
  disk no longer leaks); and offload-full stops touching a live layer.
- **Files:**
  - `internal/broker/restore.go`: the old layer after a restore swap, and
    `sweepRestoreLeftovers`' `.xbin/restore` entries;
  - `internal/term/term.go`: `ResetEnv`'s removal (after its sessions are
    killed and the layer is free, as today);
  - `internal/broker/backup.go` `offload`: `full` first takes
    `HoldTermEnv(comp)` (kill the sessions, wait, hold), then removes
    `.xbin/term/<CK>` confined, then releases; a hold that times out fails
    the offload after the archive, with nothing removed;
  - boot's `view-*` sweep (`term/base.go`) and `term.go`'s staged-view
    removal only if a staged view can hold sandbox-written files (check;
    say which in the as-built note);
  - also in `restore.go`: a restored file gets its archived permission bits
    (`mode & 0o777`; never setuid, setgid or sticky), so executables in a
    terminal upper keep `+x` (a pre-existing loss WP-9 noted);
  - changelog: one bullet (restores keep executables executable; range-mode
    removals no longer leak).
- **Tests:** broker and term unit tests with the remover injected (a fake
  that records paths) for each call site; an integration test under range
  mode (this box has a sub-uid range now) where a layer holding a file
  `chown`ed to sub-uid 1000 is fully removed by `ResetEnv`; offload-full
  with a live session kills it first; a restored `0755` file is `0755`.
- **Landed (branch `p2/wp9b`) — notes and deviations:**
  - **One remover per side:** `Broker.removeTree` and `term.Manager.removeLayer`
    call `confine.RemoveAll` (a direct `find -delete` without isolation, as
    before in effect); an unexported `rmTree` field stands in for it in
    tests. The call sites: the restore swap's old layer, each day-old
    `.xbin/restore` entry (`sweepRestoreLeftovers` is now a method),
    `ResetEnv` and offload-full's `.xbin/term/<CK>`.
  - **The swap removes after releasing.** `finish` = `swapIn` (hold, swap,
    release) then the confined removal of `<staging>.old`, so no session
    waits on it; a failed removal is logged and left to the day-old sweep.
    `close()`'s removal of an unfinished staging dir stays `os.RemoveAll`:
    it holds only what the restore wrote as xbind (and at most the VM disk
    image xbind made, if a failed swap couldn't hand it back).
  - **`ResetEnv` holds the layer** (it moved to `holdenv.go`, which keeps
    `term.go` under its size budget): `HoldEnv` (kill, wait, hold), the
    confined removal, release. Before, it waited 5 s and removed the layer
    even if a session still had it mounted, and nothing stopped a new
    session mounting it mid-removal (a confined removal takes longer than
    `os.RemoveAll` did). Now a session that won't let go fails the reset
    (500) with the layer untouched — documented in `docs/protocol.md`.
  - **Offload-full** takes the hold right after the archive, before
    `removeScopeData`, so a hold that times out removes nothing (502,
    "archived, but its terminal layer is still in use (nothing removed)").
    A confined removal that fails is logged and the offload goes on (as
    `os.RemoveAll`'s error was ignored before): failing there would leave
    the tile `enabled` with its data gone, and the restore replaces a
    leftover layer whole anyway.
  - **Staged views stay xbind's.** A `view-*` dir can't hold sandbox-written
    files: it is bound read-only, only into restricted sessions (no
    `CAP_SYS_ADMIN` to remount it; `MountGuard`), and a VM's FUSE export of
    a read-only bind refuses writes. So `dropView` and the boot's `view-*`
    sweep keep `os.RemoveAll`; `stageView`'s comment says why.
  - **Permission bits:** `backup.Reader.Perm()` is the last entry's
    `mode & 0o777`; `destTree.write` creates `0600` and `fchmod`s to it
    after the copy (exact whatever the umask). Every xbind archive already
    recorded `fi.Mode().Perm()`, so old archives restore with their real
    bits. A restored `0444` git object is now `0444` (was `0644`).
  - **Tests:** unit — `TestResetEnvHoldsThenRemovesConfined` (term),
    `TestRestoreRemovesOldLayersConfined`,
    `TestOffloadFullHoldsThenRemovesConfined`,
    `TestRestoreKeepsPermissionBits` (broker; `TestOffloadFullThenRestore`
    now counts two holds). Integration (`linux && integration`, real
    isolated sessions over `/ws/term`, range mode here):
    `term.TestConfinedResetEnv` and `broker.TestConfinedOffloadFull` — a
    live terminal writes `/opt/owned` `chown`ed to uid 1000 into its upper
    (xbind's own unlink fails: checked), then the reset / offload-full
    kills the session and the whole layer goes; both fail with
    `rmTree = os.RemoveAll`. `make integration` runs them with
    `-run '^TestConfined' ./internal/term/ ./internal/broker/` (the
    sandbox init is dispatched from an `init()` in the tagged file; term's
    `TestMain` builds binaries first). They pass with fuse-overlayfs and
    with the kernel overlay.

#### WP-10b — Relay: flow caps, per-flow locality, the strict public predicate (C · S/M · now; before WP-15a)

- **Goal:** §4's review fixes in the relay WP-10 built, plus the owner's
  decision on "internet" for tile sandboxes.
- **Files:**
  - `internal/sandbox/relay/types.go`: `Config.MaxTCP`, `MaxUDP int` (0 =
    no cap: backends and terminals), `Config.Budget *Budget`,
    `Config.StrictPublic bool`; new `budget.go`: `NewBudget(n int)
    *Budget`, `(*Budget) Used() int`, `Cap() int`;
  - `relay.go`: a flow takes a per-relay slot and a budget slot before it
    dials, and gives both back when it closes; past either cap a TCP SYN is
    answered with a RST and a UDP datagram with ICMP port-unreachable at
    once, and the flow is recorded denied (`Stats`); `Close` also closes the
    host side of every UDP flow at once (they used to linger up to their 30 s
    idle timeout); the DNS pins judge addresses with the strict predicate
    under `StrictPublic`;
  - `deny_linux.go`: `HostDeny` decides locality per flow with an
    `RTM_GETROUTE` (netlink, one socket per `HostDeny`, a mutex; the route
    type of the answer: `RTN_LOCAL`, `RTN_BROADCAST`, `RTN_MULTICAST`,
    `RTN_ANYCAST` deny; `ENETUNREACH`/`EHOSTUNREACH` answers aren't local;
    any other error denies), after the static checks and the listen set.
    The 5 s re-read stays as `deny_other.go`'s fallback;
  - `internal/sandbox/policy.go`: `EgressPolicy.Strict() EgressPolicy`
    (a flag its `Allow` and `Reach` honour) and `isPublicStrict` =
    `isPublic` minus `100.64.0.0/10`, `198.18.0.0/15`, `240.0.0.0/4`,
    `64:ff9b::/96` and `64:ff9b:1::/48`. `isPublic` itself is unchanged;
  - `internal/broker/sandboxnet.go`: `SandboxEgress` and
    `SandboxNetClasses` return `Policy.Strict()` and its reach;
  - `internal/runner/runner.go` (and `internal/term` if it has the same
    pattern): close the egress TUN fd after `Relay.Close` (one fd leaked
    per sandboxed-backend restart);
  - docs: `docs/isolation.md` §Network egress and
    `docs/overview/12-egress.md` (a sandbox class's "internet" excludes
    CGNAT/Tailscale, benchmarking, reserved and NAT64 ranges; tile backends'
    `net:internet` doesn't), the WP-11 changelog bullet amended.
- **Tests:**
  - `hardening_linux_test.go`: 5000 concurrent connects through one relay
    with `MaxTCP` 1024: at most 1024 dials reach the vetted dialer, the
    rest are reset at once, and the process's open fd count stays bounded;
    a shared `Budget` of 100 across two relays admits 100 in all; closing a
    relay returns its budget, UDP included, at once.
  - Locality (`linux && integration`, a re-exec'd helper in a new user +
    net namespace with a dummy interface): an address added to the dummy
    interface **after** the relay started is refused on its first flow; an
    AnyIP route (`ip route add local …`) is refused; a routable non-local
    address passes on to `Allow`.
  - `policy_test.go`: `Strict()` refuses each of the five ranges and
    admits a public address; `Reach()` of `net:100.64.0.0/10` under
    `Strict()` is `open`, of `net:internet` still `internet`; a non-strict
    policy is byte-for-byte today's (a table over the old cases).
- **As built** (branch `p2/wp10b`):
  - **Flow caps.** `relay.Budget` (`budget.go`, all platforms; a nil
    `*Budget` admits everything, `NewBudget(n ≤ 0)` admits nothing). A
    dialed flow — policy, gateway forward, hairpin, the DNS forward — takes
    a *claim* (`admit`) before its dial: the relay's `MaxTCP`/`MaxUDP` slot
    and a `Budget` slot, held until the splice ends. Past either cap a SYN
    gets a RST and a UDP datagram is left unhandled, so gVisor answers it
    with a port-unreachable (gVisor's ICMP rate limit, 1000/s burst 50,
    applies); both are recorded denied. A ping holds a `Budget` slot for
    its ≤ 3 s (its per-relay cap stays the old 32 in flight) and ends with
    the relay. `Close` cancels a context every host dial now takes (the test
    seam changed from a `net.Dialer` to a dial func), refuses new flows,
    releases every claim **synchronously** (`Budget.Used()` is back when
    `Close` returns) and closes both sides of every open flow, **TCP as well
    as UDP** (a TCP flow's host side could also linger on an idle remote).
  - **Locality per flow** (`deny_linux.go`): a hand-rolled `RTM_GETROUTE`
    (`x/sys/unix`, no dump; ~70 lines) on one netlink socket per `HostDeny`
    under a mutex, with a 1 s send/receive timeout (a timeout closes the
    socket and denies; the next lookup reopens it). The socket is opened
    when `HostDeny` is called, so in the caller's netns, and is closed by a
    `runtime.AddCleanup` when the `Deny` is dropped: one per relay would
    otherwise leak with every sandbox start. `local`, `broadcast`,
    `multicast`, `anycast` deny; `ENETUNREACH`/`EHOSTUNREACH` pass on; any
    other error (a blackhole route's `EINVAL`, `EACCES` of `prohibit`)
    denies. The static checks and the listen set come first. Off Linux the
    old re-read is `addrSet` (`deny.go`), wired in `deny_other.go`; the
    local-routing-table read is gone.
  - **Strict.** `EgressPolicy` gains an unexported `strict` flag
    (`Strict()`, `IsStrict()`); `Allow`, `Reach` and **`Covers`** honour
    it — a strict policy never covers a non-strict `internet` or host rule
    (whose pins may land in the refused ranges), the other way round it
    does. `SandboxEgress`/`SandboxNetClasses` return strict policies for
    every class, `none` included, so WP-15a can set `StrictPublic` from
    `IsStrict()`. The relay keeps its own copy of the five ranges
    (`strictPublicAddr`; the parent package imports it).
  - **Deviation — `Config.CloseTUN`.** Instead of each caller closing the
    fd after `Close`, the four existing callers (runner, term — at its
    922-line budget —, the env-setup run and confine) pass `CloseTUN: true`
    and the relay closes the fd after `Close` has stopped the readers, and
    on a failed `Start` (which leaked it too). §4's "the caller owns the fd"
    stays the default; tilesbx may use either.
  - **Found on the way and fixed** (no changelog entry: no builder-visible
    behaviour):
    - **gVisor leaks an eventfd per relay.** fdbased makes a stop eventfd per
      inbound dispatcher and never closes it (its endpoint's `Close` is
      empty), so every relay — backend, terminal, confined tool run — leaked
      one fd besides the TUN. `stopfd_linux.go` reads them out of the
      endpoint by reflection (read-only) and `Close` closes them once the
      readers stop; `TestCloseReleasesFDs` fails if a gVisor bump moves them.
    - The runner leaked the egress TUN fd when the net provider's link wasn't
      ready, and a lan-ingress leg's fd likewise; both are closed now.
    - A failed `CreateNIC` in `start` left the stack and the context behind.
  - **Tests.** `flowcap_linux_test.go`: 5000 SYNs at `MaxTCP` 1024 with the
    dials held (their sockets open): exactly 1024 dials, 3976 RSTs in well
    under a second, peak fds base + ~1024 (the flood would reach 2048, the
    TCP forwarder's in-flight bound); SYNs are resent for unanswered ports,
    since the non-blocking TUN drops what the peer doesn't read in time; a
    shared `Budget(100)` across two relays; `MaxUDP` → port-unreachable;
    `Close` returns a budget of 6 TCP + 4 UDP at once and closes the UDP
    flows' host sides; strict pins; `CloseTUN`; route lookups on this host;
    netlink sockets and stop eventfds released. `locality_linux_test.go`
    (`linux && integration`, added to `make integration`): the test binary
    re-execs itself into a new user + net namespace with a dummy interface
    (falls back to `lo`); an address (v4 and v6) added after the relay
    started, an AnyIP route and the link's broadcast address are refused on
    the first flow, a routed non-local address and one with no route are
    dialed, a removed address passes again. It fails against the 5 s
    re-read. `strict_test.go` and `TestSandboxNetStrict` (broker) cover
    Strict; `TestCoversSound` now mixes strict and plain policies.

#### WP-11b — The bind prompt never preselects a network for a sandbox class (C · XS · now)

- **Goal:** WP-11's UX risk: `web/bx-bindings.js` preselects the first
  unblocked option for a pending class, which is `internet` on a workspace
  tile, so an admin who clicks through grants the sandboxes the internet.
- **Files:** `web/bx-bindings.js`: a pending `sandbox-net` row preselects
  "no network" (`none`); a set blocked because it says host reads "a
  sandbox class can't reach the host" instead of "refused by the owning
  org's network sets". `hack/netrules.test.mjs` or the `sandboxNet` harness
  pass checks the preselection. The WP-11 changelog bullet amended.
- **As built** (branch `p2/wp11b`):
  - The two rules are pure helpers in `web/bx-netrules.js`, so `make
    js-test` covers them: `bindPreselect(pending)` (a `sandbox-net` row →
    `none`; `''` if the server offered no unblocked `none`, which today's
    never does; any other row → its first unblocked option, as before) and
    `blockedTitle(option)`, read off the server label. Beyond the host case
    it also names "workspace admins only", "outside your network allowance"
    and "provider-only" instead of blaming the org's sets for them; anything
    else keeps "refused by the owning org's network sets".
  - `web/bx-bindings.js`: the picker renders the preselection (`?selected`)
    and the bind submits the same value; a row with nothing to start on shows
    a disabled "pick one…" and its bind button is off. The rows are keyed
    (`repeat`), so a `<select>` is never reused for another slot's row after
    a bind reloads the list (what it shows is what it submits).
  - Tests: `hack/netrules.test.mjs` (three cases); the `sandboxNet` pass
    opens the shell's prompt, checks both classes start on `none` and the
    seed's `infra-net` is greyed with the new title, and clicks `lab`'s bind
    (→ `none`) before the wiring checks. Docs: the changelog bullet,
    `docs/overview/11-interfaces.md` and `docs/isolation.md` (one sentence
    each).

#### WP-13b — One key builder, a deployment guard, `uid`, path hygiene, `uses` for mounts (D · S · now; before WP-15a)

- **Goal:** §1.3/§1.4/§3.1/§5's changes to what WP-13 built, so wave 2
  builds on the final shapes.
- **Files:**
  - `internal/tilesbx/keys.go`: `keyOf(p) (Key, error)` (replaces
    `KeyOf`; every gate and `ServeRuntime` call it), `deploymentOf(p)` by
    reflection (§1.4), `StateDir(k, d *Def)` = `<root>/<name>.<uid>`,
    `CurDir`, `TrashDir(k)`, `ArchiveKey(k, d)` with the uid; new
    `keys_test.go` (the reviewed-fields reflect test; a non-empty
    `Deployment` → 501 on a manager route, exercised through a stand-in
    principal type when the field doesn't exist);
  - `defs.go`: `Def.UID` (json `uid`; 12 random hex at create; assigned on
    load when missing, persisted on the next write), `Def.SnapSeq`
    (`snapSeq`), `Def.Pending` (`pending`, omitempty); `clone()` copies
    them; `SandboxInfo` gains `uid` (a manager can tell a re-created
    sandbox from the old one);
  - `api.go`: the path-hygiene check and the id grammars in the gates
    (`manager`, `managerOrAdmin`, `managed`), before any lookup; `StateCreating`;
  - `policy.go`: `Policy.Total {MemMiB, Pids}` (validated; no override),
    `RuntimeLimits.Flows {TCP, UDP}` (constants 1024/256 until the relay's
    are configurable); `GET /sandboxes/policy` gains `error` (filled by
    WP-15b);
  - `internal/broker/sandboxmounts.go`: `ResourceMount` also requires a
    `uses` entry for the resource (as `EnvFor` does), so a leftover grant
    mounts nothing; the refusal says "declare it in uses";
  - docs: `docs/protocol.md` §Tile sandboxes (`uid` in the sandbox,
    `creating`, the id grammars and the 400 for dot/encoded segments,
    `policy.total`, `limits.flows`, same-scope mounts needing `uses`),
    `internal/server/openapi_sandboxes.go`; the WP-13 changelog bullet
    amended.
- **Tests:** the reflect test; `/sandboxes/a%2Fb`, `/sandboxes/x/execs/..%2Fy`,
  `/sandboxes/x/execs/%2E%2E/output` and a bad exec id → 400 with nothing
  looked up; a create stores a uid, a delete + re-create of the same name
  gets another; an old-shape file loads and gains uids; `ResourceMount`
  refuses a granted same-scope resource with no `uses` entry.
- **As built (notes):**
  - **`keyOf` and the guard.** `deploymentOf(p any)` reads a `Deployment`
    field of any struct (index cached per type in a `sync.Map`), so the
    test proves the reflection on a stand-in type (`auth.Principal`
    embedded + `Deployment`); a field that isn't a string reads as
    non-main whenever it is set (fails closed). `keyOf` reads it through
    a package variable, `principalDeployment`, which `keys_test.go`
    points at the stand-in to drive every manager route to 501 (and sets
    the real field by reflection once it exists). Gate order: hygiene
    (400) → who (403) → isolation (501) → `keyOf` (501); `ServeRuntime`
    and the policy routes run hygiene too.
  - **Hygiene sees the path as sent.** Behind `server.handleAPI` the
    inner mux routes a *decoded* path: handleAPI resets `URL.Path` and
    leaves `RawPath` stale, so `/api/xbin/sandboxes/x%2Fstop` reaches the
    stop route for `x`, and `%2E%2E` is cleaned into a redirect before any
    handler. The gate checks `RawPath` (stale: the original), `EscapedPath`
    and `Path`, so the first is 400 too; the test drives both the plain mux
    and a handleAPI-shaped request. handleAPI itself is unchanged (it
    serves every `/api/xbin` route). On the plain mux the grammars catch
    most encoded values a second time.
  - **uids.** A definition loaded without one gets one, and the load
    writes them back at once (best effort; never over an unreadable or
    newer file) rather than at the next write, so a uid can't change
    across a restart before WP-15a's first start creates `<name>.<uid>/`.
    A malformed `uid` drops the entry (no create made it, like a bad
    name); a uid another sandbox of the tile already has is replaced
    (name order). `StateDir`/`CurDir` refuse a definition whose name or
    uid fails its grammar. `removeState(k, *Def)`.
  - **`StateCreating`** is the constant and the documented rule; nothing
    sets it and the 409 isn't enforced yet — WP-20 does both where it
    sets the state.
  - **`GET /sandboxes/policy`'s `error`** is filled already from the
    store's existing unreadable-file error (the defaults still apply
    meanwhile); WP-15b makes it fail closed. `total.memMiB` 0 stays 0 in
    the effective policy (¾ of the host's RAM is WP-15a's cgroup parent's
    to resolve); `total` is range-checked only, and an override's `total`
    is ignored (lenient decode).
  - **`ResourceMount`'s refusals:** no `uses` entry → "…: declare it in
    uses"; a `uses` entry without the grant (a ceiling) → "declared in
    uses but not granted (or the workspace policy refuses it)".
  - **Beyond the file list:** `sdk/sandbox.go`'s `SandboxInfo` gains `UID`
    (one line; WP-14b edits the same file elsewhere), so a manager can
    read it; `docs/isolation.md`'s mounts line says `uses`;
    `openapi_sandboxes.go` adds one more package-level helper, `sbxSnap`
    (§14's clash note). Existing tests now use well-formed exec ids, and a
    malformed name is 400 where it was 404.

#### WP-14b — Typed routes for `Forward` (D · S · now; before WP-21 and phase 3's manager)

- **Goal:** §10's `SandboxRoute` builders replace `Forward`'s free-form
  `sub`.
- **Files:** `sdk/sandbox_tty.go` (`SandboxRoute`, the builders, `FilesOp`
  constants, `Forward(w, r, rt SandboxRoute, q)`; `RelayTTY`/`RelayNewTTY`
  build theirs); `sdk/sandbox.go` (the exec and snapshot id grammars, shared
  with the calls that already take ids); `sdk/sandbox_test.go`;
  `docs/sdk.md` §Tile sandboxes (the examples use `ExecOutput(eid)`);
  `workspace-template/AGENTS.md`: one line beside WP-5's sandbox-manager
  paragraph pointing at `xbin.SandboxAPI()` and docs/sdk.md (WP-14's open
  note); the WP-14 changelog bullet amended.
- **Compatibility:** the SDK surface is unreleased (only on
  `sandbox-runtime`), so the signature changes in place. Phase 3's
  `p3/coding-sandbox` rebases onto it; the integrator tells that track.
- **Tests:** every builder's path; an id with `/`, `..`, `%2F`, `?` or `#`,
  or failing its grammar, makes `Forward` answer 400 `invalid` with nothing
  sent (the fake gateway sees no request); a valid route forwards byte for
  byte as before (the existing Forward and upgrade tests, ported).
- **As built** (branch `p2/wp14b`):
  - As specified: `SandboxRoute` (unexported escaped sub + the builder's
    error), `ExecRoute`/`ExecOutput`/`ExecStdin`/`ExecSignal`/`ExecResize`/
    `ExecTTY`, `FilesOp` with `FilesStat … FilesMove`, `FilesRoute` (an op
    outside the constants is a refused route), `TarRoute`, and
    `Forward(w, r, rt SandboxRoute, q)`. The zero `SandboxRoute` is refused
    too (400, "no route"). `RelayTTY` forwards to `ExecTTY(id)`;
    `RelayNewTTY` builds its `tty` route internally (no exported builder:
    it is the only caller). `execRoute` (every typed call taking an exec
    id, `Follow` and `DialTTY` included) and `RestoreSnapshot`/
    `DeleteSnapshot` check the same grammars. A refusal quotes the id cut
    to 64 bytes (it may be a consumer's).
  - **Added: `xbin.IsExecID`, `xbin.IsSnapshotID`** (the grammars as
    predicates). The contract says an id that names nothing is 404
    `not-found` (its conformance suite asks `GET …/execs/nope` → 404),
    while the SDK and the runtime (§3.1) refuse one that fails the grammar
    as 400 `invalid`. A manager whose contract ids are the runtime's (§11)
    must answer it `not-found` itself, and needs the grammar to do so
    without copying it. docs/sdk.md and `Forward`'s doc example show the
    check.
  - **The caller, `coding-sandbox`'s `xbin` backend**, never used
    `Forward` (it maps output, stdin, signals and resizes onto the typed
    calls, and terminals onto `RelayTTY`/`RelayNewTTY`), so nothing there
    changed signature. The grammar did change what it answered: "nope" as
    an exec id became the SDK's 400 (and would have been the runtime's
    400 once WP-13b lands), failing the contract. `xbinBackend.Sandbox`
    now returns `xbinBox{*xbin.Sandbox}`, which answers an exec or snapshot
    id failing the grammar with `not-found` before calling the SDK
    (`GetExec`, `Output`, `Stdin`, `Signal`, `Resize`, `Kill`, `RelayTTY`,
    `RestoreSnapshot`, `DeleteSnapshot`); `backend_iface_test.go` still
    asserts `*xbin.Sandbox` is a `Box`. New
    `TestXbinBackendUnknownIDs`: bad ids (including `..%2F<runtime name>`)
    on every exec and snapshot route and a terminal attach answer 404 and
    reach nothing at the runtime double. The template's API.md and AGENTS.md
    say so (a backend's id that names nothing is `not-found`, never
    `invalid`).
  - Tests ported: the ids in `sdk/sandbox_test.go` and
    `sandbox_dialtty_test.go` are grammar ids now (`ab12cd-1`); the
    "escaped id" case (`x y` → `x%20y`) is gone, since no grammar id needs
    escaping. `TestSandboxRouteSegments` covers every id-taking call with
    22 bad exec ids and 15 bad snapshot ids; new `TestSandboxRoutes`
    covers every builder's path and method, and bad routes answering 400
    with nothing sent (a `RelayTTY` to `..%2Fsb-2` included).
  - WP-14's open note (docs for AGENTS.md) closed: one line in
    `workspace-template/AGENTS.md`'s sandbox-manager paragraph.

### WP-15a — The runtime core: launch, agent client, start, stop, teardown (wave 2 · M, the critical path)

- **Goal:** a namespace sandbox that starts, answers the agent and stops
  cleanly, and that is torn down the same way whatever ends it, with
  egress, mounts, a cgroup leaf and a registry row. It includes a minimal
  exec path through the agent client, enough for tests; WP-17 adds the API.
- **Files:**
  - `internal/tilesbx/launch.go`: the Spec builder for both modes (§7 step
    5): `NoFollow: true` always, `FuseWatch: true` in namespace mode,
    `Hostname`, `Net: "relay"`, mounts → `Bind{Src, Sub, Dst, RO}`
    (`{source:true}` from `CodeRoot`), the env (`IN_SANDBOX=1`,
    `defaults.env`; never `XBIN_*`), `Entry`; VM mode relies on WP-4's
    `HostSpec.AgentFD` (`writeVMSpec`), filled in by WP-16;
  - `netcfg.go`: the §4 relay config, exactly: the class's
    `Policy.Strict()`; `Resolver: HostResolver()`, or `""` with
    `DNSRefuse: true` when the policy is empty; `Deny:
    relay.HostDeny(listen...)` with **xbind's listen addresses** (a new
    `Deps.Listen []netip.AddrPort` from boot); `StrictPublic: true`;
    `MaxTCP: 1024`, `MaxUDP: 256`; `Budget`: one `relay.NewBudget` per
    Manager, sized `min(16384, RLIMIT_NOFILE/4)`; `Processors: 1`; `Gateway`
    10.0.2.2; **no** `HostFwd`, `HostDial`, `Published`, `HairpinDial`.
    xbind closes its TUN fd **after** `Relay.Close` returns;
  - `agent.go`: the agent client (factory, ctl reader, session allocation,
    event demux, stream dial, bounded frames; a `Dial` error such as
    `ETOOMANYREFS` is `unavailable`);
  - `lifecycle.go`: start (§7 steps 1–8) under a per-sandbox single flight
    that never holds `m.mu` across slow work (WP-13's `ServeCreate` and
    `ServeDelete` call `start`/`stop` under `m.mu`; move them out); the
    watcher on `cmd.Wait()` and `ctl` EOF; **`teardown(reason)`** (§7) with
    its reasons (exit 3 → fuse, `OOMKills`, the VMM's 125 console tail);
    `Stop`, `StopWhere`, `StopTile`, `StopAll`; ready timeouts; the log
    ring; a `reserve(tile, def) (release func(), error)` seam that WP-15b
    fills (a no-op book until then), called at step 1 and released by the
    unwind and by `teardown`;
  - `cgroup.go`: the `comp-tilesbx-<ws8>` parent (`cgroup.Parent`, limits
    from `policy.total`), `Prepare` + `UseCgroupFD`, the fallback to
    `AddWith` on `ENOSYS`/`EINVAL`;
  - `trash.go`: DELETE stops, renames the state dir to `.trash/<uid>`,
    forgets the definition and answers 204; one worker empties `.trash` with
    `confine.RemoveAll`, one entry at a time; the boot re-queues `.trash`
    and empties every `tmp/`. `removeState`'s 501 goes away;
  - `registry.go`; `internal/sbx/sbx.go` (`Entry.Name`, `For`, `ForUser`);
    `internal/boot/sandboxes.go` (`case sbx.Tile`; `sessionWhat` from
    `Entry.Name` instead of parsing the id);
  - `internal/boot/serve.go` (`StopAll` at shutdown);
  - `internal/boot/tilesandboxes.go`: the Deps wiring (the launcher,
    `Listen`, the cgroup parent) and **`brk.OnSandboxNetChange =
    rt.OnSandboxNetChange`**: re-resolve each running sandbox of the tile
    with `SandboxEgress`; `!new.Policy.Covers(running)` → sync and stop
    (state kept, `stateDetail` says the class narrowed); otherwise set
    `egressNext`;
  - `internal/boot/boot.go`: **`st.sandboxBasePins` is set before
    `stepIsolation`** to `tilesbx.DefBases(ws)`, which reads
    `data/sandboxes.json` directly (every definition's `base`, archived ones
    included) and returns an error for an unreadable file (WP-7b's
    signature), so the boot GC never releases a base only a definition
    pins. The step-order test gains that edge.
- **Tests:**
  - Unit, with a fake launcher: start/stop transitions; a failed start
    unwinds everything, `release` included, records `sbx.Fail` and returns
    to `stopped`; `teardown` runs once when a stop and the watcher race;
    each reason gives its `stateDetail`; DELETE answers 204 while the
    remover is blocked, and a re-create of the same name gets a new uid and
    dir; `OnSandboxNetChange` narrowing stops, widening sets `egressNext`;
    `DefBases` of an unreadable file errors.
  - Integration (`linux && integration`; the rootfs or a minimal lower; a
    static `bx`):
    - create → start → exec `true` → stop → start: the upper persists;
    - egress `none`: TCP to 1.1.1.1 is reset, DNS REFUSED, both under
      100 ms;
    - a read-only mount refuses writes; a `Sub` symlink is refused; a
      symlink planted in the upper at a mount point is refused (the Spec
      sets `NoFollow`);
    - leaf limits written under `comp-tilesbx-<ws8>/`, the init in its
      leaf from the start (`UseCgroupFD`); the registry row added and
      removed;
    - `unshare -U` fails inside;
    - **it ends on its own:** SIGKILL the agent from outside → `stopped`,
      "the sandbox's agent exited", and the leaf, relay, reservation and
      registry row are gone; SIGKILL fuse-overlayfs → `stopped` with the
      root-filesystem reason within 2 s; a session allocating past
      `memory.max` is the one killed (its exec ends `killed`, the sandbox
      stays `running`);
    - 20 start/stop cycles leave xbind's fd count where it started (the TUN
      fd and the relay closed);
    - a boot with a definition pinning an old base keeps that base through
      `stepIsolation`'s GC.
  - Add `./internal/tilesbx/` to `make integration`, twice (KVM, then
    emulate), like `internal/vm`.
- **Docs wave 1 wrote ahead of the runtime** become true here, so check
  them against the code: `docs/isolation.md` §Network egress and
  `docs/overview/12-egress.md` (a narrowed class stops the sandbox with its
  state kept, a widened one waits for the next start, the relay config, no
  route to the host), and `docs/protocol.md` §Tile sandboxes (start, stop,
  delete now work; the 501s they replace go).
- **Size:** each file ≤ 600 lines.
- **Depends on:** WP-2, WP-3, WP-3b, WP-7, WP-7b, WP-8, WP-8b, WP-10,
  WP-10b, WP-11 (it can start with `none` only), WP-13, WP-13b.
- **As built** (branch `p2/wp15a`):
  - *Files.* `launch.go` (the `Launcher`/`Proc` seam, `nsLauncher`,
    `nsSpec`, `binds`, `sessionEnv`, `modeOps` + `nsOps`), `netcfg.go`,
    `agent.go`, `lifecycle.go` (runs, start, `teardown`), `stop.go` (stop,
    `StopWhere`/`StopTile`/`StopAll`, the egress reconcile), `state.go`
    (the lock, the pin), `cgroup.go` (+ `cgroupfd_linux.go`/`_other.go`),
    `trash.go`, `registry.go`, `logring.go`. `lifecycle.go` came out at 720
    lines and was split to stay under 600.
  - *The seams later WPs fill.* `Manager.reserve` (a field; a no-op book
    now, WP-15b's admission replaces it; its release is made idempotent and
    runs at the unwind or the teardown). `m.modes[ModeVM]` is WP-16's
    `modeOps{spec, leaf, readyWait, stop, exitReason}`: `spec` returns an
    `undo` (the `vm.Reserve` release, run at the unwind or the teardown),
    `stop` replaces the sync-then-SIGKILL (the shim's SIGHUP), `exitReason`
    maps the shim's 125; `r.accel` is set by it. The agent client
    (`Exec`, `Signal`, `Resize`, `Sync`, `File`) is WP-17's and WP-18's
    transport: session ids from 2, never reused in a run, streams dialled
    before the exec; a teardown ends every open session `Killed` (WP-17's
    `killed`, signal KILL). `sessionEnv(d)` is every exec's base
    environment (`IN_SANDBOX=1`, a `PATH`, `defaults.env`, no `XBIN_*`).
    Callers that hold a sandbox's flight (reset, rebase, snapshots) use
    `startLocked`/`stopLocked`; `trash.put` takes what they put aside.
  - *`Handle.Started` right after `cmd.Start`* (not after `relay.Start`):
    xbind never holds the child-side files past a start, whatever fails
    after it. A failure after the start ends the run (`end(r, why)`) and
    waits for its teardown; before it, the steps done are undone in
    reverse (the book, the lock, the factory, the leaf).
  - *Races with the watcher.* A process can die while its start is still
    attaching its TUN, relay, agent client or registry row: each is
    attached under the run's mutex unless the teardown began (`attach`),
    else the start closes it itself. `-race` clean (the unit tests, ×3).
  - *A stop's sync is bounded as a whole, its send included* (verifier):
    the agent runs `syncfs` and a session's spawn inline in its control
    loop, so over a wedged root it stops reading `ctl`; one large line
    (an exec's body can pass the socket buffer) then blocks `send` holding
    `sendMu`, and an unbounded `Sync` never reached the kill. **WP-17:**
    the agent client's other sends (`Exec`, `Signal`, `Resize`) are still
    unbounded — bound them the same way (or end the run), so a request
    never hangs on a sandbox that stopped reading.
  - *One host `Deny` per runtime*, not per relay: `HostDeny`'s netlink
    socket (one per call, closed only by a GC cleanup) would otherwise be
    one more fd per start until a GC. The TUN is closed by the runtime
    after `Relay.Close` (not `CloseTUN`).
  - *The leaf's memory (a deviation from §6.2's table).* A namespace leaf
    has `memory.max` = `memMiB` + 128 MiB, **`memory.swap.max` 0** (new
    `cgroup.Limits.NoSwap`) and **no `memory.high`**. With the host's swap,
    a 4 GiB allocation in a 2 GiB sandbox simply swapped (no cap at all);
    with no swap and `memory.high` at ⅞, anonymous memory over it can't be
    reclaimed and a 1 GiB allocation in a 384 MiB leaf was still throttled
    after 60 s instead of being killed. Now it is OOM-killed in ~25 ms, the
    session (oom_score_adj 500) and not the agent, and the sandbox runs on.
  - *What WP-15b's list had that the start needed anyway:* the lock wait
    (5 s, then 409 `state` naming an earlier run; the orphan leaf's
    `populated` wait is still WP-15b's); the pin's `error` state (a missing
    `cur/` with `Def.base` set, a base no longer installed, another overlay
    flavour: 409 `state`, `state: "error"`, kept until a reset); the kill
    switch (`enabled: false` → 503; the fail-closed file is WP-15b's); the
    parent's `Sweep("sbx-")` when it is made, and the boot's re-queue of
    `.trash` and of every `tmp/` entry (moved into `.trash/<uid>.<rand>`).
  - *Answers.* A start answers the SandboxInfo: `running`, or `stopped`
    with the failure (quoting the init's last log line) in `stateDetail`;
    refusals answer as refusals. A stop answers the SandboxInfo; an admin's
    leaves "stopped by a workspace admin". `DELETE` stops, renames the
    state dir to `.trash/<uid>` (renamed back if forgetting the definition
    fails), forgets, answers 204 and queues the confined removal.
  - *Registry and observers.* `sbx.Entry` gains `Name`, `For`,
    `ForUser`; `sessionWhat` reads `Name`. The runner's sampler and the
    boot's limit alerts read a tile row's leaf through the tilesbx parent
    (`Runner.TileCgroup`, `Runner.LeafCgroup(e)`) — WP-8b's note. Start
    failures are `sbx.Fail`ed (`start`; limit/unavailable as `refused`), and
    so are ends of a sandbox's own and a lost control connection (`exit`).
    `GET /sandboxes` `health.tileSandboxes` is `{cgroup, flows:{used,
    cap}}` (WP-19 adds the rest of §3.10's health): `cgroup` says why the
    sandboxes run without limits when the parent couldn't be made — they
    run on, without limits, as without delegation.
  - *OnSandboxNetChange* returns at once and re-resolves off the broker's
    goroutine (`reconcileEgress`): not covered → a synced stop, state kept,
    "its network (class:x) narrowed…" (or "…is gone…" when the class no
    longer resolves); wider → `egressNext` (+ `restartNeeded`).
  - *Boot.* `stepWorkspace` sets `sandboxBasePins` to `tilesbx.DefBases`
    (edge `workspace` → `isolation` in the order test);
    `TestDefinitionPinsSurviveTheBootGC` runs the GC stepIsolation runs.
    `serve.go` runs `StopAll("xbind shut down")` after `run.StopAll()`.
    `Deps.Listen` is the console's, the ingress listener's and an injected
    listener's address.
  - *`make integration` runs `./internal/tilesbx/` once*, not twice: VM
    mode is WP-16's, which adds the `XBIN_VM_ACCEL=emulate` run with its VM
    tests.
  - *Tests.* Unit (`lifecycle_linux_test.go`, a fake launcher whose
    "sandbox" is an in-process agentcore over the real factory, with a
    socketpair TUN the real relay runs on): start/stop and the spec, a
    failed launch, a never-ready agent and an agent dying while starting
    all unwind (the book back, the lock free, no row), each end reason,
    teardown once under a racing stop (×20), narrowing/widening, delete
    stops, unsolicited events dropped, `EAGAIN` retried then
    `unavailable`, the pin's errors; `defs_test.go`: delete answers 204
    with the remover blocked and a re-create gets a new uid and dir, the
    boot re-queue, `DefBases`. Integration (`live_linux_test.go`, through
    the routes as `apps/mgr` holding the cap; a minimal lower with the
    kernel overlay, again with fuse-overlayfs, and over `.rootfs`): the
    upper persists across a stop; `none` resets TCP to 1.1.1.1 and
    REFUSES DNS in < 100 ms, and the gateway is a dead end; `unshare -U`
    fails; a reader's mount refuses writes; a symlinked sub-path and a
    symlink planted at a mount point fail the start and make nothing on
    the host; SIGKILL of the agent and of fuse-overlayfs end it with their
    reasons (fuse: ~40 ms) and nothing left (process, book, row, leaf,
    flows, factory); 20 cycles leave the fd count where it was; delete
    removes the state. Under `systemd-run --user --scope -p Delegate=yes`
    (the file's header) also: started into its leaf by `UseCgroupFD`, the
    leaf's limits, and the OOM kill of a session with the sandbox running.
  - *Found, not fixed (pre-existing; reproduced with the daemonizing
    mount too, `FuseWatch` off, so terminals and backends on
    fuse-overlayfs likely share it):* a regular file created **directly in
    `/`** wedges fuse-overlayfs for good. After `pivot_root` its root is its own FUSE
    mount, and for a create in the root dir it `lgetxattr()`s
    `/proc/self/fd/<upper fd>/<name>` — a path walked through that mount,
    whose one thread is the one waiting (seen in `/proc/<pid>/syscall`;
    `-o threaded=1` and longer entry timeouts don't help). Files in a
    subdir are fine. The tests write under `/work`. A fix wants
    fuse-overlayfs's root off its own mount without handing it the host's
    tree (see the open issue). **What WP-15a does about it:** a file
    operation (WP-18's path) creating in `/` leaves a thread of the agent
    itself waiting on a request fuse-overlayfs already took, so SIGKILL
    can't end PID 1 and the pid namespace is never torn down — the stop
    answered 503 after 15 s and the sandbox stayed `stopping`, its
    processes alive (mutation-checked). So `Proc.Kill` also SIGKILLs every
    descendant of the init while it is unreaped (`killtree.go`; the cgroup
    leaf's `cgroup.kill` does the same where there is one): killing the
    FUSE server wakes every waiter, and the stop ends in ~5 s (its sync
    wait) — `TestLive/*/a wedged root still stops`. An orphan left when
    xbind dies with a wedged root is swept at the next boot only where
    there are cgroups.

### WP-15b — Lifecycle policy: admission, idle, auto-start, reset, restart semantics (wave 2 · M · after WP-15a)

- **Goal:** the rest of §6.1 and §7, and §3.2's fail-closed policy file.
- **Files:**
  - `internal/tilesbx/admission.go`: the per-tile book and the `total`
    book, each under its own mutex; `reserve` checks "the count or sum with
    this one included ≤ cap" and increments in one critical section, as
    `vm.Reserve` does; `release` is idempotent. `perTile.max` stays inside
    the definitions mutex for creates and (WP-20) clones. `Deps.Disk`
    becomes `interface{ Low() bool }` (the scope-quota check leaves
    admission) with a boot adapter to a new `(*Broker) DiskLow() bool` (the
    last diskmon verdict);
  - `idle.go`: the activity record; in-flight counters for runs and file,
    tar and copy operations (WP-17/18 bump them); the TTY hubs' `OnClients`
    count; the `time.AfterFunc` timer that re-arms, and on firing stops
    only when nothing is in flight and no client is attached;
  - `lifecycle.go`: auto-start (an exec or file call on `stopped` starts
    and waits; on `starting` waits for `running`; on `stopping` or busy
    waits, then auto-starts, all within `waitMaxSec`); reset (stop,
    `cur` → `.trash/<uid>.<rand>`, clear `Def.base`, start again if it ran)
    and rebase (stop, re-stamp, start again if it ran); the lock wait; the
    `error` state for a missing base, a changed flavour or a missing `cur`;
  - `sweep.go`: at boot the parent's `Sweep("sbx-")`, every `tmp/` emptied,
    `.trash` re-queued (with WP-15a's worker), every sandbox `stopped`;
  - `policy.go`: an unreadable policy file is logged once, surfaced in
    `GET /sandboxes/policy` (`error`) and the admin health, and makes
    `enabled` false until a `PUT` replaces it;
  - a `users`-event reconcile of egress: re-resolve every running
    sandbox's class (a D20 policy-row edit fires no `OnSandboxNetChange`);
  - `api_lifecycle.go` (filled; `?wait` on every lifecycle call).
- **Tests:**
  - Unit, with an injected clock:
    - admission per cap and per mode; **ten concurrent starts with
      `running: 4` → exactly four**, and the same for `memMiB` and the
      `total` book; the book returns to zero after unwinds and teardowns;
      twenty concurrent creates with `perTile.max` 8 → exactly eight;
    - the idle timer re-arms and fires; it is held off by a running non-tty
      exec, by an in-flight `run` longer than `idleStopMin`, by an in-flight
      tar, and by an attached TTY client; a detached TTY exec with no
      traffic is idle;
    - an exec that arrives while `stopping` waits and then auto-starts;
    - reset and rebase of a running sandbox leave it running, with a fresh
      (or re-pinned) `cur` and its snapshots untouched;
    - exec ids from a new boot answer `lost`;
    - a corrupt `policy.json` → `enabled` false, starts 503, the error in
      `GET /sandboxes/policy`; a `PUT` clears it;
    - a definition is re-validated at start, so a mount the tile no longer
      holds fails the start with the reason.
  - Integration:
    - reset wipes the upper (the old `cur` lands in `.trash` and is removed
      confined);
    - closing the factory kills the sandbox, and a restarted Manager finds
      it `stopped`;
    - a second start while an orphan holds the lock waits, then fails;
    - an exec on a stopped sandbox starts it.
- **Parallel:** with WP-16, WP-17 and WP-18, which need only WP-15a.
- **As built** (branch `p2/wp15b`):
  - *Files.* `admission.go` (the books, `admit` = `Manager.reserve`),
    `idle.go`, `autostart.go` (`acquire`, `within`, `waitOf`), `reset.go`
    (reset, rebase, `recordBase`), `sweep.go` (the boot's sweep, the boot
    id, `execLost`; `requeueTrash` moved here from `trash.go`, the leaf
    `Sweep` out of `initCgroup`); `policy.go`, `api.go`,
    `api_lifecycle.go`, `lifecycle.go`, `state.go`, `stop.go`, `info.go`
    changed. The auto-start helper and reset/rebase are their own files,
    not `lifecycle.go` (it would pass 600 lines).
  - *Admission* (`admit`, called at §7 step 1 as WP-15a's seam; tests
    wrap it to count): the disk-low verdict (503), the tile's measured
    sandbox bytes (`b.diskBytes`, summed; WP-19's worker fills them) over
    `perTile.diskGiB` (429), then the tile's book (`running`, `memMiB`,
    `vcpus`, each "with this one ≤ cap") and the **total book**, each
    under its own mutex, the tile's taken first and given back if the
    total refuses. The total book counts what each leaf may take — the
    mode's `leaf(d, lim).MemMax` (namespace: `memMiB` + 128; WP-16's VM
    leaf its own) — against `total.memMiB` (0 = ¾ of the host's RAM, the
    parent's own cap); pids aren't booked (a kernel ceiling, not a
    reservation). `enabled` stays `startLocked`'s check, now through
    `policyStore.on()`. `TotalBook()` feeds `health.tileSandboxes.total`.
    `Deps.Disk` is `Low() bool`; boot's `tileDiskLow` → the new
    `(*Broker) DiskLow()` (diskmon's last scan's `low`).
  - *Idle* (`run.idle`: `last` atomic, `holds`/`timer`/`off` under `m.mu`):
    armed at running for the definition's `idleStopMin`, **or the
    policy's when it has none** — the policy's is the default, not a cap
    (WP-13 stored and answered a definition's 60 under a policy of 30; a
    cap would change that answer). On firing it re-arms for what remains
    since the last activity, a full interval while a hold is on; a quiet
    sandbox is stopped in its flight after a second look, the state set
    `stopping` under `m.mu` so no hold is taken once the stop is decided
    (a call arriving then waits and auto-starts). `stateDetail` "idle for
    N minutes: stopped, state kept (idleStopMin)". Activity: `managed()`
    (every per-sandbox manager route) touches — **but `GET
    /sandboxes/{name}` doesn't** (it reads xbind's record; a manager
    polling state would otherwise keep a sandbox up forever); a hold's take
    and release touch. A PATCH of `idleStopMin` and a policy PUT re-arm
    running timers at once. The teardown disarms. Injected clock:
    `Manager.afterFunc` (+ `Options.Now`).
  - *For WP-17/18:* `m.acquire(k, name, wait) (*run, release, error)` —
    the auto-start path — returns the run **with a hold**; release it when
    the work ends (a non-tty exec at its exit, a `run`, a file/tar/copy
    op when done, a TTY exec once started). `m.hold(r)`, `m.touch(r)`
    (exec output/input), `m.ttyClients(r)` (a hub's `OnClients`). Exec
    ids must be `m.bootID + "-" + n`; `managed()` already answers another
    boot's id 410 `lost` (`execLost`). A busy sandbox (WP-20) should hold
    the flight while it copies, or `acquire` must learn to wait on it.
  - *`?wait`* (`waitOf`, `within`): absent = `waitMaxSec` (WP-15a's
    blocking behaviour kept), clamped, `0` = don't wait — with a 50 ms
    floor so a transition whose flight is free says `starting` (or its
    refusal) rather than the state before it. Also on `POST /sandboxes`
    with `start: true`. The transition runs on after the answer.
  - *Reset/rebase* (`restage`, in the flight): stop if up; then under
    `lockRun` (the lock and the orphan leaf) reset clears `Def.base`
    first (a failed rename then leaves `cur/` pinned as it was) and
    renames `cur/` to `.trash/<uid>.<rand>`; rebase `layers.Stamp`s the
    current base and `Def.base` follows. Both clear `error` (an error the
    restage itself finds — rebase of a missing `cur/` — sets it); both
    restart what ran. A sandbox that never ran creates no state dir.
    `base.outdated` is now answered (`Def.base` vs the rootfs's version,
    read once at `New`).
  - *The lock wait* (`lockRun` → `lockStateAnd`): the flock and the leaf's
    `populated 0`, within one `Manager.lockWait` (5 s; `endWait` is a
    field too, for tests).
  - *The policy file fails closed:* loaded in `New` (logged once),
    `policyStore.on()`/`effective()` answer off while `err` stands; the
    runtime says `enabled: false`; a PUT merges onto the zero policy (the
    file's content is unknown) and clears it. `PolicyError()` feeds
    `health.tileSandboxes.policyError`. A PUT leaving sandboxes off
    stops every running one (`switchedOff`, async) — the WP-15a
    verifier's gap.
  - *Users events:* boot subscribes to the hub's `users` events (under
    `--isolate`; resubscribes if dropped) → `OnUsersChange`, coalesced (one
    reconcile at a time, one more if events came meanwhile), re-resolving
    every running tile's egress with `reconcileEgress`. WP-19 extends the
    same hook (caps, resources).
  - *WP-15a verifier's findings fixed:* the leaf killer is registered on
    a `WaitGroup` only while the teardown hasn't begun, and the teardown
    waits for it before `cg.Remove` — a reset's fast restart re-creating
    the leaf name can't be hit by the old run's pre-5.14 kill rounds;
    `end()` kills on every ask, so a second stop retries a kill that
    didn't take (`TestSecondStopKillsAgain`).
  - *Tests.* Unit (`admission_linux_test.go`, `idle_linux_test.go`, a fake
    clock in `fake_linux_test.go`): ten concurrent starts per cap
    (running, memMiB, vcpus, total) → exactly the cap, the book back to
    zero and admitting again; the total book per mode; releases after a
    failed launch, an own end, a stop; twenty concurrent creates under 8;
    low disk 503 and the disk cap 429; the kill switch stops; a revoked
    mount fails the start (400) with nothing launched; idle re-arm/fire,
    a GET not being activity, a PATCH re-arming; holds (exec, run, tar,
    TTY client) over 3 h; a detached TTY exec idles; `acquire` from
    stopped, waiting out a stop then starting, starting, the wait running
    out, no autoStart, error; `?wait` (invalid 400, `0` answering as it
    stands, clamping); an orphan's lock (409 after the wait; let go within
    it → runs); reset/rebase of a running sandbox (fresh vs re-pinned
    `cur/`, snapshots untouched, the old `cur/` removed via `.trash`),
    rebase repairing a missing base, reset repairing a missing state,
    idempotence; another boot's exec ids 410; a users event narrowing.
    Integration (`live_policy_linux_test.go`, inside `TestLive`, all three
    variants, and under `systemd-run … Delegate=yes`): reset wipes the
    upper (confined removal in the rootfs variant); an exec on a stopped
    sandbox starts it; an orphan's lock → 409 after the wait; the factory
    closed ends the sandbox and a new runtime finds it `stopped`.
  - *Verifier's fixes:* `acquire` on a sandbox a timed-out stop left
    `stopping` (its flight free) spun for the whole wait — it now waits
    for the run's `done`, then the flight (`TestAutoStartAfterStuckStop`,
    CPU-bounded); a start already past the policy check when `enabled`
    went off came up running — step 8 checks the switch again after
    `b.run` is set, so either it sees the switch or the switch sees it
    (`TestPolicyOffDuringStart`, 503); `admit` read the tile's book for
    its refusal message after unlocking it, a data race with a release
    (`TestAdmissionRefusalUnderLock`, `-race`).

### WP-16 — VM mode (wave 2 · M · after WP-4, WP-6, WP-15a)

- **Goal:** `mode: "vm"`, with resident VMs, the tile sub-budget,
  `EnsureDiskAt`, the emulation gate and report, leaf sizing, and stop by
  SIGHUP → `synced`.
- **Files:**
  - new `internal/tilesbx/vm.go`: `vm.Apply(…, Options{Resident: true})`
    (WP-4 carries `AgentFD` in `HostSpec`), `vm.Reserve(tile, memMiB,
    vm.TileSandbox())` (a sub-budget refusal is an `sbx.Refuse` failure),
    `EnsureDiskAt(cur)`, the VM leaf row of §6.2, the VMM's exit 125 → a
    `teardown` reason carrying the console tail;
  - hook points in `launch.go` and `lifecycle.go`;
  - `internal/boot/tilesandboxes.go`: the `Modes` adapter calls
    `vm.(*Manager).TileVMs()` (replacing WP-13's `sandboxModes` stand-in;
    `st.Emulated` is the reported accel, `reason` the unavailable text);
  - `internal/boot/vm.go` **`(*State).putVMPolicy`**: with the old stored
    policy in hand, `tiles` true → false, or `tilesEmulated` true → false
    while VMs are emulated, calls `go rt.StopWhere(mode == vm)` (state
    kept; `stateDetail` names the policy switch). It is the only place the
    VM policy changes.
- **Tests** (integration, KVM and `XBIN_VM_ACCEL=emulate`):
  - create, start, exec, stop keeps the disk;
  - a `diskGiB` grow applies at the next start;
  - a sub-budget refusal is an `sbx.Refuse` failure;
  - `tiles:false` → `unavailable` with the reason; flipping `tiles` off
    stops a running VM sandbox; flipping `tilesEmulated` off under
    emulation does too;
  - emulation is refused unless `tilesEmulated`;
  - FUSE mounts; egress `none` in a VM;
  - killing the VMM → `stopped` with the console tail in `stateDetail`;
  - rerun `TestVMSandboxRelay` and `TestVMSandboxEgressAllowed`, which WP-10
    couldn't run here (no VM assets), against the hardened relay.
- **Docs:** WP-6 documented the flip stop ahead of it (`docs/protocol.md`
  `/vm/policy`, `docs/isolation.md` §VM sandboxes, the admin editor's
  warning); this WP makes those sentences true.
- **Parallel:** with WP-17, WP-18 and WP-19.

### WP-17 — Execs, run, output, TTY (wave 2 · M · after WP-12, WP-15a)

- **Goal:** §3.5–3.7.
- **Files:**
  - `internal/tilesbx/{exec.go, ring.go, run.go, tty.go}`;
  - `api_exec.go` (filled): the signal body decodes `Group *bool` (nil =
    true); exec ids checked against their grammar (WP-13b's gate);
  - `internal/broker/usersapi_personal.go` (the `OnNoTerminal` hook).
- **Semantics to build exactly:** a stop (any `teardown`) marks running
  execs `killed` (signal `KILL`, `exitCode` null) and keeps their records
  and rings; only another boot's ids answer 410 `lost`. A `run` and a
  non-tty exec bump WP-15b's in-flight counters; a TTY hub's `OnClients`
  feeds the idle hold. An exec that arrives while the sandbox is
  `stopping` waits, then auto-starts (WP-15b's helper).
- **Tests:**
  - Unit:
    - ring: offsets, a gap reported, a wait answered at exit, the text mode's
      UTF-8 boundary, the tile budget;
    - run: head/tail/elided arithmetic identical to the contract's examples,
      and UTF-8 replacement;
    - a signal body without `group` is a group signal.
  - Integration:
    - 20 concurrent execs;
    - a run's timeout kills the group, grandchild included;
    - a hang-up kills the group;
    - `{"signal":"INT"}` (no `group`) reaches a grandchild;
    - an exec running when its sandbox stops is `killed` and its output
      still reads to the end; after an xbind restart its id is `lost`;
    - a gorilla client on the TTY: the session frame first, with overrides;
      replay; an echo ack; `exit {code}`; a `noTerminal` `forUser` gets 403.
- **Docs:** WP-12 documented `sandbox` on the session frame and
  `code`/`signal` on `exit` as tile-TTY behaviour (`docs/protocol.md`
  §`/ws/term`); this WP implements them.
- **Parallel:** with WP-16, WP-18 and WP-19.

### WP-18 — Files, tar, copy (wave 2 · S/M · after WP-15a)

- **Goal:** §3.8.
- **Files:** `internal/tilesbx/files.go`; `api_files.go` (filled). Every
  file, tar and copy operation bumps WP-15b's in-flight counter for its
  duration. The `ETag` header of a content read is the stat's `etag`,
  quoted.
- **Tests** (integration):
  - stat, list and ranged content;
  - an atomic PUT; `ifMatch` → 412; `ifMatch` with the header's ETag and
    with the stat's both work; `ifNoneMatch`; `mkdirs`;
  - `fileMax` → 413;
  - a tar round trip keeps symlinks;
  - a tar with `../x` and absolute entries lands inside;
  - `/work/l → /etc`, then `PUT /work/l/x`, writes the **sandbox's** `/etc/x`
    and never the host's;
  - a read-only mount refuses;
  - a copy between two sandboxes; another tile's sandbox is `not-found`;
  - (unit, injected clock) a tar stream longer than `idleStopMin` isn't
    cut by the idle timer.
- **Parallel:** with WP-16, WP-17 and WP-19.

### WP-19 — Workspace integration (wave 2 · M · after WP-5, WP-9, WP-15b)

- **Goal:** §5's running-sandbox rules, §6.3, §7's reconciles and §9.
- **Files:**
  - new `internal/broker/tilesbx_hooks.go` (`TileSandboxHooks`: `Defs`,
    `RestoreDefs`, `StopAll`, `StopTile`, `StopWhere`, `HasState`,
    `Leftovers`, `Usage`, `OnResourceChange`);
  - `internal/broker/resenc_wire.go`: **`SealResources` calls
    `StopWhere(has a res mount)` and waits for those stops before
    `resenc.UnmountAll`**; the `cap:containers` branch of `grantRestart`
    stops the scope's mounted sandboxes before `MountEncrypted`;
  - `broker/broker.go`: `grantRestart` fires `OnResourceChange(g.From)` for
    `res:` targets;
  - `broker/lifecycle.go`; `broker/backup.go` (the manifest field, the
    offload refusal, `doRestore`: definitions merged **by uid**, a name
    taken by another uid skipped and reported, a definition without state
    `error` "restored without state");
  - `internal/backup/backup.go` (`Manifest.Sandboxes`);
  - `broker/policy.go` (`pathLeftovers`);
  - `broker/diskmon.go`: the sandbox `Usage` counts for the low-disk verdict
    and the fair share only, never for a scope's quota or write blocking;
    a low-disk verdict calls the runtime's `OnLowDisk` (and `DiskLow()`,
    added by WP-15b, reads the same verdict);
  - new `internal/tilesbx/usage.go`: a confined `du` at each stop, the
    running-upper measuring worker (one `du` at a time, ≥ 2 min apart per
    sandbox), VM disks by allocated blocks, the 5 s `statfs` watch while a
    namespace sandbox runs, the low-disk stop (largest tile first, every
    tile above the fair share, starts 503 until free space is back), the
    over-`perTile.diskGiB` stop, and the per-tile disk check in admission;
  - boot wiring:
    - **`brk.OnCapChange = func(tile, cap string, held bool) { if cap ==
      broker.SandboxesCap && !held { go rt.StopTile(tile, "cap:sandboxes
      was revoked") } }`**: non-blocking, idempotent, filtered (it fires on
      approves too and repeats from `capSweep` on every `users` event);
    - `OnStructureChange` (rescans) and the hub's `users` events →
      reconcile: tiles that vanished or whose `SandboxesFor` is false are
      stopped (this also catches a hand edit of `xbin.json` that dropped
      the grant row, which fires no hook), the rest get `OnResourceChange`;
    - tile disable, hide and offload → `StopTile`;
  - `workspace-template/tiles/admin/tabs/sandboxes.js` (tile rows, stopped
    and orphaned definitions, stop/delete, the sandboxes policy editor with
    `total`, the health line: policy error, flow budget, low-disk hold,
    `.trash` backlog);
  - `hack/ui-harness/passes/sandboxes.js`, and **run the `sandboxes` pass**
    (`hack/ui-harness/run.sh`, stopped with `run.sh --stop`): WP-7's disk
    row and WP-13's `tileSandboxes` checks have never run;
  - docs: `docs/overview/02-workspace.md`, `docs/overview/14-lifecycle.md`,
    `docs/sandbox-manager.md` (a short "On xbin" section), changelog. WP-5
    documented "revoking stops the tile's sandboxes" ahead of it
    (`docs/auth.md`, `workspace-template/AGENTS.md`, the changelog); this WP
    makes it true.
- **Tests:**
  - A tile without sandboxes backs up byte-identical; definitions ride
    along; a restore brings them back stopped, state untouched; a restore
    after a delete + re-create of the same name skips the old definition
    and keeps the new sandbox; a definition whose state is gone comes back
    `error`.
  - Offload is refused with state and allowed without it.
  - Disabling a tile stops its sandboxes; so does revoking the cap, once
    (repeated `capSweep` calls stop nothing twice); approving stops
    nothing; a hand-edited grant row removed + a rescan stops them.
  - **Seal:** seal the vault with a sandbox mounting a resource → it is
    `stopped` ("the vault was sealed") before the views unmount, and its
    start answers 503.
  - **Grant changes:** revoking the manager's writer grant on a mounted
    resource, or dropping the resource from the manifest, stops the
    sandbox with the mount named; downgrading writer → reader stops a
    read-write mount's sandbox; a read-only mount's sandbox keeps running.
  - **Disk:** a fake `statfs` below the reserve stops running namespace
    sandboxes largest tile first and makes starts 503; a tile measured over
    `perTile.diskGiB` while running has its largest sandbox stopped; a
    scope holding 60 GiB of sandbox bytes still writes its kv (the quota
    doesn't count them).
  - `pathLeftovers` lists them.
  - The harness pass: the admin tab shows tile rows and stops or deletes
    one.
- **Parallel:** with WP-16, WP-17 and WP-18.
- **As built** (branch `p2/wp15b`, on WP-15b):
  - *Files.* `tilesbx`: new `usage.go` (the measuring worker, the statfs
    watch, the low-disk and over-cap stops, `Usage`, `DiskLow`),
    `reconcile.go` (`Reconcile`, coalesced — `OnUsersChange` is it now —
    `OnResourceChange`, the users-event coalescing moved out of `stop.go`)
    and `workspace.go` (`Defs`, `RestoreDefs`, `HasState`, `Leftovers`);
    `broker`: new `tilesbx_hooks.go` (`TileSandboxHooks`,
    `SetTileSandboxes`, the seal/grant/offload/leftover helpers); boot's
    adapter `tileSbxHooks` and `tileSandboxCapHook` in
    `boot/tilesandboxes.go`. The admin tab's tile part is its own module,
    `workspace-template/tiles/admin/tabs/tilesbx.js`
    (`<bx-admin-tile-sandboxes>`).
  - *The hooks.* `TileSandboxHooks` has no `StopAll` (serve.go calls the
    runtime's at shutdown) and gains `OnLowDisk`. `StopWhere`'s predicate
    sees a `broker.TileSandbox{Tile, Name, Res}` (the broker never imports
    tilesbx; boot converts). They are held in an `atomic.Pointer`: diskmon's
    goroutine reads them from `broker.New` on.
  - *Reconcile* (rescans: the watcher's `watchLoop` and the broker's
    `OnStructureChange`; users events) also stops a tile that exists but
    isn't enabled — a hand edit of `lifecycle` — through a new
    `Tiles.Enabled`. The cap check fails closed without `Deps.Caps`.
    Stops are `go`: a reconcile never waits for one.
  - *Mounts.* A run records its res mounts as bound (`run.mounts`: read-write
    or not); `OnResourceChange` resolves each again: an error or a
    non-filesystem stops it, a read-write mount now held as reader stops
    it, a widened role or a view that is merely down doesn't (the seal stops
    those itself). The seal's `stateDetail`: "the vault was sealed:
    stopped, state kept — start it again once the vault is unsealed"; the
    `cap:containers` flip's names the remount.
  - *Disk.* A measurement is the whole state dir (`<name>.<uid>/`: `cur/`,
    snapshots, staging) by `confine.DiskUsage` (VM: `cur/vm/disk.img` and
    each snapshot's by allocated blocks, lstat'ed). Measured at each stop
    (queued before the sandbox reads `stopped`), after a reset and a
    restore, once at boot for every sandbox with a state dir, and while
    running at most every 2 minutes; `StopAll` ends the measuring (no
    confined `du` outlives xbind). The over-`perTile.diskGiB` stop fires on
    a *running* sandbox's measurement only — a stop's own measurement stops
    nothing — so a tile over its cap loses a writer per rotation, not all at
    once. `Deps.Disk` gains `LowAt(free, total)` (diskmon's reserve rule,
    `broker.DiskLowAt`) and `FairShare()`; starts are held while diskmon's
    last verdict **or** a statfs taken now says low. diskmon's statfs is
    injectable (`diskMon.free`) and it stores its fair share.
  - *Restore.* Definitions are merged by uid as specified, re-validated
    for shape (the start re-checks reach), their sizes resolved under the
    caps now, `perTile.max` enforced (skipped and reported), `version`
    bumped past the live one's and `snapSeq` never lowered; a replaced
    sandbox keeps its live `base` when its `cur/` is there. **"Restored
    without state" is `error` only for a definition that pinned a base**
    (`Def.base` set): one that never ran, or was reset, has nothing to lose
    and comes back `stopped`. A definition restored with `pending` is
    `error` ("delete it"). `doRestore` answers a `restored{Manifest,
    SandboxesSkipped}`; `POST /restore` adds `sandboxesSkipped`.
  - *Offload* checks `HasState` first — it reads `.xbin/sbx/<CK>/` on disk,
    so an unreadable definitions file hides nothing — and answers 409
    (`offloadStatus`) with nothing stopped or archived.
  - *Health and the admin rows.* `health.tileSandboxes` gains `lowDisk`,
    `trash {entries, bytes}` (bytes are known for a delete; a reset's
    `cur/` and a boot re-queue count 0) and `total.pids {used, cap}` (new
    `cgroup.Manager.Pids()`, `-1` without a parent). `AdminRow` gains `uid`
    and `stateDetail`. A running tile row nests under its manager's current
    backend generation in the tab (no `Parent` set server-side).
  - *Tests.* Unit (`workspace_linux_test.go`, fake launcher): the
    reconcile (removed, disabled, cap lost), repeated `StopTile`, mounts
    narrowed and widened, the seal's stop and its 503, the over-cap stop,
    the low-disk order and 503 on a fake statfs and clock, `HasState`,
    `Leftovers`, `RestoreDefs`, the trash backlog; broker
    (`tilesbx_hooks_test.go`, fake hooks): byte-identical backup,
    definitions round-trip and `sandboxesSkipped`, offload 409, disable and
    hide stop, the seal waits for its stops, `res:` and `cap:containers`
    grants, leftovers, diskmon's quota vs fair share and its low-disk call;
    boot: the cap hook's filter, the predicate's view. Mutation-checked (ten
    mutations, each killed). Integration: `TestLive/*/a stop measures the
    upper, confined` (8 MiB in a 0700 sub-uid-owned dir; the probe gained
    `fill`). The harness `sandboxes` pass (41 PASS) was run against an
    xbind started and stopped by PID, not `run.sh`, whose `--stop` kills by
    pattern.

### WP-20 — Snapshots and clones (wave 3 · M · after WP-8b, WP-15b, WP-16)

- **Goal:** §3.9 exactly. The manager's images depend on clones.
- **Files:**
  - new `internal/fsutil/clone_linux.go` (`CloneSparse`) with an `_other`
    stub, and `renameat2(RENAME_EXCHANGE)` (`fsutil.Exchange`);
  - `internal/tilesbx/snapshot.go`: staging in `tmp/<rand>/`, **each
    snapshot dir stamped with `layers.Stamp`** (base and overlay) plus
    `meta.json` before its rename; restore re-pins the sandbox to the
    snapshot's base (`Def.base` follows) and swaps `cur` with one
    `Exchange`, the old `cur` to `.trash`; the busy `stateDetail`; `?wait`
    (202 `pending` / busy / `creating`); the boot reconcile (no `meta.json`
    → removed; `pending: "clone"` → `error`);
  - `api_snapshots.go` (filled); `from` in create: `creating` while the
    copy runs, the snapshot's base, `from` without `snapshot` only from a
    stopped source (409 `state` otherwise), `perTile.max` under the
    definitions mutex, the disk check;
  - uppers copied with `confine.CopyTree` (WP-8b's `--preserve=xattr
    --reflink=auto`, `FSCaps`).
- **Tests:**
  - `CloneSparse`: sparseness kept on tmpfs; `FICLONE` used where the
    filesystem supports it. `Exchange` swaps two dirs atomically.
  - Integration in both modes: snapshot, modify, restore; a whiteout (a
    removed package) survives snapshot, restore and clone; **an opaque
    directory** (`rm -rf /usr/share/doc && mkdir /usr/share/doc`) stays
    empty after restore and clone; **ownership overrides** (a file
    `chown`ed to 1000 in a single-uid sandbox, kept in fuse-overlayfs's
    xattr) survive; clone from a snapshot; a mode mismatch (and, in
    namespace mode, a flavour mismatch) is refused; snapshots count toward
    the tile's disk.
  - **Bases:** a snapshot taken, then `rebase` → restore brings back the
    old base (the sandbox's `base` reads it again); GC after a reset keeps
    the snapshot's base; a clone of an old snapshot runs on that snapshot's
    base.
  - **Staging and crashes:** `?wait=0` → 202 / `creating`, then done; a
    busy sandbox answers 409 with `retryAfterMs`; `from` without `snapshot`
    on a running source → 409 `state`; a crash injected between staging and
    rename (a fake `Exchange` that fails, or xbind killed mid-copy in the
    integration test) leaves the old state intact, no half snapshot listed,
    `tmp/` emptied at boot, and a cut-short clone `error`.
- **Parallel:** with WP-21's fixture work.

### WP-21 — Fixture, end to end, phase 3 gate (wave 3 · M · after WP-14b, WP-20)

- **Goal:** prove the whole path through a real manager tile, and hand off
  to phase 3.
- **Files:**
  - new `examples/sandbox-go/`: a minimal manager with `cap:sandboxes`, an
    `internet` `sandbox-net` slot, and a backend on the SDK that exposes
    create, run, exec, output (`Forward(…, ExecOutput(eid), q)`), files
    and a relayed TTY. It is an integration fixture and doubles as docs;
  - new `test/isolated/` (`linux && integration`; skips without the rootfs,
    userns or VM assets). It boots xbind with `--isolate`, imports the
    example, approves the cap, and drives everything through the proxy:
    - a TTY through the manager's `RelayTTY` with a gorilla client;
    - a restart: the exec is `lost`, the sandbox `stopped`, the state kept;
    - the boot's base GC keeps a base only a definition or a snapshot pins
      (WP-7's wiring had only unit coverage);
    - a consumer id carrying `..%2F` never reaches another sandbox;
    - the same in VM mode where KVM is present;
    - range mode (this box has a sub-uid range now): `users: any`, a
      sub-uid-owned upper measured, snapshotted and removed;
  - the harness `sandboxes` pass against a live tile sandbox;
  - a docs pass;
  - `make integration` lists the new packages.
- **Gate — phase 3 is ready when:**
  - `make check` and `make integration` are green here, in both
    accelerations;
  - the QA box runs the example in range-uid mode;
  - `docs/protocol.md`, `docs/sdk.md` and `docs/sandbox-manager.md` "On xbin"
    are reviewed against the code.

### WP-22 — Archive and thaw *(later; split when scheduled)*

- **22a:** streaming archiver I/O. `archiveStream` uses a pipe-backed
  `ResponseWriter` with a deadline, instead of `httptest.NewRecorder`, and
  `doRestore` streams.
- **22b:** s3-archiver multipart (stdlib SigV4; parts sized for ≤ 10,000; an
  abort on error; no tmpfs spool).
- **22c:** a sparse-aware disk archive format (an extent map + data) and a
  confined, streaming tar of uppers with `FSCaps`. This needs streaming
  confine, dev-lifecycle's WP-S6 or an equivalent.
- **22d:** `archive`/`thaw` (states `archiving`/`archived`/`thawing`);
  offload carries; GC pins archived bases (already in WP-7's union). The
  archive key is `<CK>.sbx.<name>.<uid>`, so a delete and re-create of a
  name never meets the old archive, and thaw restores into the uid's own
  state dir.

## 13. Testing notes

- **Tags and environment.**
  - Integration files are `//go:build linux && integration`, with a
    `TestMain` that dispatches `sandbox.InitArg` to `RunInit`.
  - The rootfs comes from `XBIN_ROOTFS` (the `internal/vm` convention) or
    `../../.rootfs`. VM assets come from `bin/`, or from the `XBIN_*`
    variables.
  - `XBIN_VM_ACCEL=emulate` forces QEMU.
  - Tests that need neither the rootfs nor VM assets use a minimal lower with
    static helpers, so CI (userns on, no `.rootfs`) runs them.
- **On this box.**
  - Run with the Bash sandbox disabled: the `/proc/self/exe` re-exec gets
    EPERM otherwise.
  - **This box now delegates a sub-uid range** (`/etc/subuid
    magik6k:100000:65536`), so namespace sandboxes run in range mode here:
    `users` answers `any`, and range-mode behaviour (sub-uid owners,
    `FSCaps` reading other uids' files, confined removal of sub-uid-owned
    trees) is testable here as well as on the QA box. WP-8's confine test
    already covers a sub-uid-owned tree when the range exists. Tests that
    need the single-uid path skip or force it explicitly.
  - **A range-mode init waits for its uid maps**, so every test that
    launches a sandbox must call `SetupUserns` after `cmd.Start`. WP-2
    applied dl/confine-dirfrom's `SetupUserns` hunk to `internal/sandbox`'s
    old integration tests (`TestSandboxIsolation`,
    `TestSandboxServesUnixSocket`), which hung or failed here before it; the
    WP-8 and WP-10 reports of those failures predate WP-2's merge. Rerun
    them once on the merged branch to confirm, and give every new launching
    test the same call.
  - `make integration` runs `./internal/sandbox/` in CI for the first time
    (WP-2): the first CI run on the GitHub runner is worth watching.
  - The real-cgroupfs test (`internal/cgroup/leaf_integration_test.go`)
    needs a delegated cgroup holding only the test binary, so it isn't in
    `make integration`; run it with the `systemd-run --user --scope -p
    Delegate=yes` recipe in its header (`-test.run OnCgroupfs` takes both
    WP-8's leaves test and WP-8b's parent + `UseCgroupFD` test).
  - No live isolated boot has exercised the base-GC wiring yet: a boot GC
    would release unpinned `.rootfs-*` siblings of the shared main
    checkout's rootfs, so use a copied rootfs (WP-21's `test/isolated/`).
  - Kill only the PIDs you started; stop the harness with `run.sh --stop`.
- **Harness.** The passes `sandboxes`, `sandboxNet`, the bindings rows, and
  the `predict`/`termrun` passes (termwire). They are the only JS regression
  tests. Two failures predate phase 2 (reproduced on 783c50de): `predict`'s
  final `.gear` click ("outside of the viewport") and `termSessions`' "the
  tab and the window's ✕ are uncovered" (narrow). They stop `shots.js`
  early, so run the passes one at a time.

## 14. Risks and open items

- **Merge order with dev-lifecycle.** Both branches edit `internal/sbx`,
  `internal/vm/policy.go`/`reserve.go`, `internal/cgroup`, `confine.go`,
  `init_linux.go`, `broker/backup.go`, `term.go`'s budget line,
  `openapi.go`, `protocol.md` and the admin `sandboxes.js`. Whichever lands
  second rebases. Each of those WPs names its collision. Specifically:
  - **The sandbox key (must not be skipped).** When dev-lifecycle's
    `auth.Principal.Deployment` arrives, `keyOf` compiles unchanged and its
    reflection guard answers 501 to every non-main deployment; the
    reviewed-fields test fails until someone replaces `deploymentOf`'s
    reflection with `p.Deployment` and adds the field to the list. Only
    then may non-main keys be built (their `TileKey` paths).
  - `vm.Reserve`'s body (`policy.go`) and `reserve.go`: take the union of
    the option fields and their checks; the signature is already the same.
  - dl/confine-dirfrom: delete `internal/sandbox/nofollow_linux.go`
    (identical helpers), resolve `init_linux.go`'s bind loop as `if
    s.NoFollow { mountBindsNoFollow } else { mountBinds }`, and the trivial
    Makefile comment. Its fd binds can then tighten confine's tree helpers
    (today they bind a path string whose every component xbind created).
  - `broker/backup.go` was split into `backup.go` + `restore.go` (WP-9):
    dev-lifecycle's edits rebase onto the split.
- **Other integration notes.** `internal/server/openapi_sandboxes.go`
  defines package-level helpers (`integer`, `object`, `sbxName`, `sbxExec`,
  `sbxPath`, `sbxWait`) that could clash with another branch's. WP-14b
  changes `Forward`'s signature: `p3/coding-sandbox` rebases onto it.
  `hack/size-budget.txt` still allows `netfn.go` 1170 lines (it is 1099).
- **Emulated VMs** are 5–20× slower. Stream attach and ready timeouts are
  stretched (×3 or ×6), and xbind opens streams before it sends `exec`.
- **fuse-overlayfs** is slower than a kernel overlay for build-heavy work.
  The flavour stamp keeps it consistent; measuring the difference is a
  WP-21 note, not a blocker.
- **The relay per sandbox** costs about 2.5 MiB on a 192-CPU host (its
  goroutine count follows `GOMAXPROCS`). Setting `ProcessorsPerChannel: 1`
  halves it (WP-10). Its flows are capped per sandbox and across sandboxes
  (WP-10b), so a sandbox can't spend xbind's fds.
- **Hooks that don't fire.** A hand edit or a backup restore of `xbin.json`
  that drops a `cap:sandboxes` or `res:` grant row fires no hook; the
  per-call `SandboxesFor` check blocks the manager at once, and the rescan
  reconcile (WP-19) stops what's running. A D20 policy-row edit fires no
  `OnSandboxNetChange`; the `users`-event reconcile (WP-15b) covers it.
- **Running work dies with xbind** (decision 4). Auto-updating installs will
  kill long jobs. A relay out of process is the later fix, if it is ever
  wanted.
- **Native.** A consumer's native view can't point the `terminal` primitive
  at a manager: it allows only its own `/api/<self>/…`. The agent tile
  relays a second hop with `Forward`. Widening the vocabulary is an app
  release, left to phase 3.
