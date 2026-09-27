# Tile-sandbox runtime — the implementation plan

> Status: **live** (D120) — the implementation plan for phase 2 of
> [sandbox-managers.md](sandbox-managers.md): xbind's tile-sandbox runtime,
> building [tile-sandboxes.md](tile-sandboxes.md) (D113) as revised by D115
> and D120. Being built on branch `sandbox-runtime`; §12 is the work list.
> The builtin `coding-sandbox` manager (phase 3) serves
> [docs/sandbox-manager.md](../docs/sandbox-manager.md) by driving this
> runtime through the SDK (§10, §11).

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
| State under `.xbin/sbx/<key>/<deployment>/…` off `main` | Off `main`, state goes under **`.xbin/deploy/<TK>/d/<d>/sbx/<name>/`**. A deployment segment under `.xbin/sbx/<key>/` would collide with sandbox names (§1.4) |

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
| `internal/cgroup` | Per-call limits with `cpu.max`, `Kill`, `Populated`, and a boot `Sweep` | changed |
| `internal/confine` | A file-caps profile (`FSCaps`) for confined copy and remove of uppers that may hold sub-uid-owned files | changed |
| `internal/termwire` | The `/ws/term` wire (hub, echo acks, upgrade), shared by terminals and tile TTY execs | new (out of `term/attach.go`) |
| `internal/sandbox/relay` | `Deny` (no route to the host itself), `DNSRefuse`, a nil-`Allow` fix | changed |
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

### 1.3 Data layout

```
<ws>/data/sandboxes.json                    definitions, main, keyed by tile path (xbind-only, 0600)
<ws>/data/deployments/<TK>/<d>/sandboxes.json   seam: a non-main deployment's definitions (dev-lifecycle)
<ws>/.xbin/sandboxes/policy.json            the sandboxes policy (admin)
<ws>/.xbin/vm/policy.json                   + tiles, tilesBudgetMiB, tilesEmulated
<ws>/.xbin/sbx/<CK>/<name>/                 one sandbox's state, 0700, xbind-created
    lock                                    flock'd by the running sandbox (its init, agent, fuse-overlayfs)
    base                                    pinned base version (internal/layers)
    overlay                                 namespace: "fuse" | "kernel" — the flavour that wrote upper/
    upper/  work/                           namespace mode (sandbox-written: never walked as xbind)
    vm/disk.img                             VM mode: sparse ext4 the guest formats (never parsed by the host)
    snapshots/<sid>/{meta.json, upper/ | disk.img}
    tmp/                                    fresh, empty staging dirs for clone/restore (xbind-created)
<ws>/.xbin/deploy/<TK>/d/<d>/sbx/<name>/    seam: a non-main deployment's state
```

- **The definitions file** has this shape: `{"version":1,"tiles":{"<path>":{"sandboxes":{"<name>":Def}}}}`.
  It is written atomically (tmp + rename) under the manager's mutex and loaded
  at boot. A `Def` is exactly what `POST /sandboxes` stored, after
  validation, plus `created`, `version`, `base` (mirrors the stamp; archived
  sandboxes keep it) and `clientId`. `data/` is masked from terminals and
  never bound into a backend, so no tile code can forge a mount, an egress
  class or a mode. Everything read back is re-validated against the tile's
  **current** reach on every start (§8).
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
| registry id | `tile:<CK>:<name>` | `tile+<d>:<CK>:<name>` |
| cgroup leaf | `sbx-<CK>-<name>` | `sbx-<CK>+<d>-<name>` |
| archive key (WP-22) | `<CK>.sbx.<name>` | `<CK>.deployments.<TK>.<d>.sbx.<name>` |

- **On this branch every key is main.** The key comes from the principal:
  `Key{Tile: p.Component}`, and it becomes `Deployment: p.Deployment` once
  dev-lifecycle's `Principal.Deployment` lands. For a non-main key the
  non-main paths answer `unsupported` until their `TileKey` helper exists.
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

## 3. The HTTP API (`/api/xbin`)

### 3.1 Conventions

- **Who.**
  - **Manager calls** need `p.Component != "" && p.Via == "instance" &&
    SandboxesFor(p.Component)`, checked on every call. The key is the
    principal's (tile, deployment), so a manager only ever names its own
    sandboxes.
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
            "outputRing": 1048576, "stdinMax": 1048576, "fileMax": 67108864, "tarMax": 1073741824, "waitMaxSec": 120},
 "used": {"sandboxes": 3, "running": 1, "memMiB": 2048, "vcpus": 2, "diskBytes": 5368709120}}
```

```jsonc
// Policy (.xbin/sandboxes/policy.json; zero = default; PUT merges absent fields)
{"enabled": true,
 "perTile":    {"max": 8, "running": 4, "memMiB": 8192, "vcpus": 8, "diskGiB": 100},
 "perSandbox": {"memMiB": 2048, "vcpus": 2, "diskGiB": 20, "maxMemMiB": 8192, "maxVCPUs": 8, "maxDiskGiB": 200, "pids": 4096},
 "idleStopMin": 30, "outputRingMiB": 1, "outputBudgetMiB": 64,
 "overrides": {"apps/coding-sandbox": {"perTile": {"max": 32}}}}   // per tile
```

`idleStopMin` may go up to 1440. `outputRingMiB` may go up to 8.
`outputBudgetMiB` is the ring memory a tile may hold across its execs; §6.4
explains it.

### 3.3 Sandboxes

```
GET    /sandboxes                   manager → {sandboxes:[SandboxInfo]}   admin → today's registry view + tileSandboxes
POST   /sandboxes                   manager → 201 SandboxInfo (200 when clientId repeats the same request)
GET    /sandboxes/{name}            manager → SandboxInfo
PATCH  /sandboxes/{name}            manager → SandboxInfo (restartNeeded when a change waits for the next start)
DELETE /sandboxes/{name}            manager, admin (?tile=) → 204: stop, confined removal of the state, forget
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

- **`state`** is `stopped`, `starting`, `running`, `stopping` or `error`.
  - `error` means the state can't be used: its base is missing, or its
    overlay flavour changed. `stateDetail` says why, and says that `reset`
    (or `rebase`) repairs it.
  - A failed start returns to `stopped`, with the failure in `stateDetail`.
- **`reach`** is the running relay's reach, or the class's reach now when the
  sandbox is stopped (§4). `egressNext` is set while a `PATCH`ed class waits
  for the next start.
- **`diskBytes`** counts allocated bytes. For a VM it is the disk image's
  blocks. For a namespace upper it is a confined `du` measured at each stop
  (§6.3). Snapshots are included.
- **Mounts.** `res` must be a `filesystem` resource the tile holds: its own
  scope's, or one granted to it. The writer role may mount read-write; the
  reader role forces read-only. `sqlite` and every other kind is `invalid`.
  - `path` is a relative, clean sub-path, resolved at start beneath the
    resource root without following symlinks (§5).
  - `at` must be absolute and clean. It must not be `/`, and must not lie
    under `/proc`, `/sys`, `/dev`, `/run/xbin` or `/opt/xbin`.
  - `{source:true}` is always read-only.

### 3.4 Lifecycle

```
POST /sandboxes/{name}/start?wait=   manager        → SandboxInfo
POST /sandboxes/{name}/stop?wait=    manager, admin → SandboxInfo (sync, then kill; state kept; execs → killed)
POST /sandboxes/{name}/reset         manager        → SandboxInfo (stops; wipes state — confined; re-pins the current base)
POST /sandboxes/{name}/rebase        manager        → SandboxInfo (stops; keeps state; re-pins the current base — may break apt state)
```

An exec or file operation on a `stopped` sandbox with `autoStart` starts it
and waits for it. The run's `timeoutMs` doesn't include the start.

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
POST   /sandboxes/{name}/execs/{id}/signal             {signal: INT|TERM|KILL|HUP, group?: true} → 204
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
- **Ids** are `<boot>-<n>`. `<boot>` is 6 hex characters, random per xbind
  start. An id from another boot answers 410 `lost`, and so does any exec
  that was running when its sandbox stopped under it.
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
- **Limits.** `SetReadLimit(1 MiB)` (a paste arrives as one frame). An
  attached client counts as activity for the idle timer.
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

### 3.9 Snapshots and clones (WP-20)

```
GET    /sandboxes/{name}/snapshots                  → {snapshots:[{id, name, created, bytes}]}
POST   /sandboxes/{name}/snapshots {name, clientId} → 201 (stops the sandbox briefly; restarts it if it ran)
POST   /sandboxes/{name}/snapshots/{sid}/restore    → SandboxInfo (execs killed)
DELETE /sandboxes/{name}/snapshots/{sid}            → 204
```

A clone is `POST /sandboxes` with `from`. A snapshot records its base version,
overlay flavour and mode. Restoring or cloning onto a different base or mode is
`invalid`.

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
  - `SandboxNetClasses(tile)` lists the classes. `GET /sandboxes/runtime`
    returns them, and a manager builds `hello.egress` from them (§11).
- **The relay is always there.** `Spec.Net = "relay"` in both modes, which
  keeps the modes identical. The config:

  ```go
  relay.Config{TunFD: fd, Allow: pol.Allow /* never nil */,
      Resolver: HostResolver() /* "" when pol.Empty() */, DNSRefuse: pol.Empty(),
      Gateway: 10.0.2.2 /* no HostFwd: a dead end */, Deny: relay.HostDeny(xbindListen),
      Processors: 1 /* §14 */}
  ```

  plus `AllowHost` only when the policy has host rules. There is no
  `Published`, `HairpinDial`, `HostFwd`, `HostDial` or gateway socket, and
  `vm.Options.Gateway` is `""`.
- **What `Deny` blocks**, before every other check (TCP, UDP, ICMP, and flows
  to pinned hosts):
  - loopback, link-local, unspecified and multicast addresses;
  - `10.0.2.0/24`;
  - every address on a host interface, read at relay start;
  - xbind's listen addresses.

  So `internet` can't reach the host's own public address, and `lan:` can't
  reach loopback. **A sandbox has no route to xbind.**
- **`none`** is an empty policy with `DNSRefuse`. TCP gets a reset at once,
  DNS answers REFUSED, and nothing waits on a timeout.
- **Changes.** `OnSandboxNetChange(tile)` fires on a bind, an unbind, a
  network-set edit or a transfer. It re-resolves each running sandbox's
  class.
  - When the new rules are **not a superset** of the running ones, the
    sandbox is synced and stopped (state kept; `stateDetail` says why). The
    relay can't cut flows it has admitted.
  - When they are a superset, `egressNext` is set, and the new policy applies
    at the next start.
  - The manager's backend is never restarted for a `sandbox-net` change.
- **One `net` slot.** A tile's own `net` slot is now picked deterministically
  (sorted slot names). Two `net` slots used to resolve in map order.

## 5. Mounts

- **Resolution.** `broker.ResourceMount(key, res)` returns `{src, role, kind,
  encrypted}`, using the same path resolution as `EnvFor`.
  - A resource the tile doesn't hold, or holds as something other than a
    `filesystem`, is refused.
  - An encrypted resource needs the vault unsealed and a mounted gocryptfs
    view. While the vault is sealed, a start answers 503 `unavailable`, as
    backends are held.
- **The bind.** A mount becomes `sandbox.Bind{Src: <trusted resource root>,
  Sub: <path>, Dst: <at>, RO: ro || role == reader}`.
  - The init resolves `Sub` beneath `Src` with `openat2(RESOLVE_BENEATH |
    RESOLVE_NO_SYMLINKS)` and binds through `/proc/self/fd/N`. A symlink
    planted anywhere in the sub-path is refused.
  - `Dst` is created inside the new root with no-follow, component-by-component
    mount points, so a symlink in the sandbox's upper can't redirect it. The
    helpers are dl/confine-dirfrom's `nestedPoint`/`mountNested` if that has
    landed; otherwise WP-2 adds them.
- **In VM mode** the same binds become FUSE exports, served by the shim's
  confined file server. Guest-side mount points live in the guest.
- **`{source:true}`** binds `CodeRoot(key)` (the tile dir) read-only.
- **Never mounted:** homes, the workspace view, `.xbin`, `data/` outside a
  resource, the gateway socket, tokens, vault contents, devices. The static
  `bx` is bound read-only at `/opt/xbin/bin/bx` in namespace mode. VM mode
  doesn't need it: the guest runs its own agent.

## 6. Resource accounting

### 6.1 Admission

**At create:**

- the tile's definitions ≤ `perTile.max`;
- the requested sizes are clamped to the per-sandbox caps;
- the mode is available now (`invalid` names why);
- in VM mode, the sum of declared disks ≤ `perTile.diskGiB`.

**At start** (every start re-checks):

- the policy is `enabled`, else 503;
- running sandboxes ≤ `perTile.running`, else 429 `limit`;
- the running `memMiB` and `vcpus` sums stay within the per-tile caps;
- `diskBytes` summed over the tile's sandboxes and snapshots ≤
  `perTile.diskGiB`, else 429;
- the tile's scope isn't over its disk quota (`diskmon`), else 429.

**VM mode, in addition:** `vm.Reserve(tile, memMiB, vm.TileSandbox())`
checks the global VM count and budget and books against the **tile
sub-budget**, `tilesBudgetMiB`, which defaults to half of `budgetMiB`. Its
refusals are marked `sbx.Refuse`. `usedTiles` is shown next to `used` in the
admin view.

### 6.2 cgroups

- **One leaf per running sandbox** (`sbx-<CK>-<name>`, flat under xbind's
  delegated cgroup today). It is created before `SetupUserns`, as the runner
  does, through `cgroup.AddWith(name, pid, Limits)`:

  | | namespace | VM |
  |---|---|---|
  | `memory.max` | `memMiB` + 128 MiB (agent, fuse-overlayfs) | `memMiB` + `OverheadMiB` (192, or 512 emulated) |
  | `memory.high` | 7/8 of max | unset |
  | `pids.max` | `perSandbox.pids` (4096) | 512 |
  | `cpu.max` | `vcpus` × 100 ms per 100 ms | (`vcpus` + 1) × 100 ms |
  | `cpu.weight` | 100 | 100 |

- **The relay runs in xbind** and is counted there: about 200–300 KiB idle on
  a small host.
- **Stop:** `cgroup.Kill` (`cgroup.kill`, else a SIGKILL per pid), then
  `Remove`.
- **Boot:** `Sweep("sbx-")` kills and removes every stale leaf.
- **Without cgroup delegation** everything still works, without limits. The
  registry's `Leaf` is `""`, as for terminals.
- **With dev-lifecycle.** When its per-tile parent (`tile-<key>/d-<name>`)
  lands, tile-sandbox leaves go in a **sibling** subtree sized from the
  sandboxes policy, not under the backend's limited parent. Otherwise they
  would share its `memory.max`, and the admin view's per-tile stats would
  count them twice.

### 6.3 Disk

- **A VM disk** is sparse, sized by `diskGiB` (`EnsureDiskAt`, grow only). A
  PATCH to a smaller size is `invalid`. Growing takes effect at the next
  start (the guest's `resize2fs`). It is counted in allocated blocks.
- **A namespace upper has no hard cap.** It is counted by a **confined** `du`
  at each stop, with the file-caps profile so sub-uid-owned dirs count too. The
  per-tile quota and the scope's disk footprint include it (§9). A running
  sandbox's growth shows at its next stop.

### 6.4 Memory in xbind

- **Rings grow lazily** up to `outputRingMiB` per exec.
- **A tile's rings share `outputBudgetMiB`** (64 MiB). When the budget is
  full, the oldest finished exec's data is dropped first; its `ringStart`
  moves, which the contract allows.
- **A TTY hub** keeps a 256 KiB replay.
- **`run` keeps nothing** past its response.

## 7. Lifecycle and restart semantics

**Start** runs under a single flight per sandbox:

1. Admission (§6.1).
2. Take the lock: `flock(<state>/lock, LOCK_EX|LOCK_NB)`.
   - If it's busy, an orphan from a crashed xbind may still be dying. Wait up
     to 5 s, and also for the old leaf's `cgroup.events populated 0`.
   - If it's still busy, the start fails with a `state` refusal.
3. Pin the base and flavour.
   - A missing base, or a changed overlay flavour, sets `error`. Unlike
     terminals, this never gates xbind's boot.
4. Resolve the mounts and the egress class.
5. Build the `Spec`: `Lower: [base]`, `Upper`/`Work` (namespace mode),
   `Restricted`, `MountGuard`, `Hostname: name`, `Net: "relay"`, `Agent`,
   `Lock`, binds, and `Entry: /opt/xbin/bin/bx`.
   - In VM mode, `vm.Apply(…, Resident)` also runs, along with `Reserve` and
     `EnsureDiskAt`.
6. `Launch`, `cmd.Start`, `cgroup.AddWith`, `SetupUserns`, `RecvTUN`,
   `relay.Start`, `Handle.Started`.
7. Dial `ctl` and wait for `ready`: 30 s in namespace mode, 60 s for a VM, and
   3× that under emulation.
8. Register with `sbx.Add`, arm the idle timer, and set the state to
   `running`.

Any failure along the way unwinds, records `sbx.Fail(…, StageOf(err, Start))`
and returns to `stopped` with `stateDetail`. VM mode never falls back to
namespace mode.

**Stop:**

1. Refuse new execs and file operations (409 `state`).
2. Send `{op:"sync"}` and wait for `synced`, at most 5 s. Namespace mode
   answers with `syncfs`; VM mode flushes the guest.
3. Kill the process.
   - Namespace mode: SIGKILL the init.
   - VM mode: SIGHUP the shim, which syncs and exits 129. After 10 s, SIGKILL.
4. `cgroup.Kill` and `Remove`, close the relay, release the reservation, and
   remove the registry entry.
5. Execs that were running become `killed`. Measure the upper's usage and set
   the state to `stopped`.

**Other transitions:**

- **Idle stop.** Every activity records a time. Activity is:
  - an API call on the sandbox;
  - exec output or input;
  - a TTY attach, a detach or a frame;
  - a file operation.

  A `time.AfterFunc(idleStopMin)` timer re-arms itself for whatever remains
  of the interval (no tickers). When it fires, the sandbox stops unless a
  **non-tty** exec is running.

  A detached TTY with no traffic counts as idle. Background work belongs in
  non-tty execs, bounded by their `timeoutMs` and the caps.
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
  - At boot, `Sweep("sbx-")` clears stale leaves. The per-sandbox lock stops
    a new start from mounting an upper that a dying orphan still has
    mounted.
- **Policy flips.**
  - Sandboxes `enabled → false` stops every tile sandbox (state kept) and
    refuses starts.
  - VM `tiles → false`, or `tilesEmulated → false` while VMs are emulated,
    stops VM-mode sandboxes. Their next start is refused with the reason.
  - Lowered sizes apply at the next start. A running sandbox shows
    `restartNeeded`.
- **Revoking `cap:sandboxes`** (`OnCapChange`) stops the tile's sandboxes
  (state kept). Every call then answers 403. A ceiling change that strips the
  grant without a `grants` event is reconciled on the hub's grants and
  policy events.
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
| `runtime` | manager | the key comes from the principal |
| list | manager (own key) · admin | — |
| create / patch | manager | name grammar + reserved names; `mode` required and available; sizes clamped; `net` names a `sandbox-net` slot of this tile; mounts (§5: held, role, kind, sub-path, `at`); `defaults.env` and `env` keys matching `XBIN_*` are refused (`invalid`); labels ≤ 1 KiB; `for`/`forUser` ≤ 128 chars, stored as claims; per-tile count; `clientId`; `version` |
| delete / stop | manager · admin (`?tile=`) | the admin path never creates, execs, reads files or attaches |
| start, reset, rebase | manager | admission; the definition re-validated against the tile's current reach (a mount it no longer holds fails the start with the reason) |
| run / execs | manager | argv or cmd non-empty; argv + env ≤ 256 KiB; `cwd` absolute; `uid`/`gid` allowed (`users`); the tty `forUser` check (§3.7); `execsRunning` |
| exec sub-routes | manager | the exec belongs to this sandbox and this boot; stdin ≤ `stdinMax`; a signal from the enum |
| tty | manager | as for execs; `noTerminal` re-checked at every attach; read limit; override ids' grammar |
| files / tar | manager | `path` absolute and clean (resolved **inside** the sandbox); sizes; `mode` octal |
| copy | manager | both sandboxes in the caller's key |
| snapshots | manager | the base, flavour and mode match on restore and clone |
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
  - Reset, delete and snapshot removal are a confined `rm -rf`.
  - Snapshots and clones of uppers are a confined `cp -a --reflink=auto`.
  - Confined runs use the file-caps profile, so trees owned by other sub-uids
    are readable and writable inside the throwaway sandbox. Nothing about
    that sandbox touches the host.
  - xbind creates the state and staging dirs empty, 0700.
  - Swapping a restored or cloned upper in is a rename of an xbind-created
    dir, done only while no process holds the lock.
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
  - the relay (policy plus `Deny`);
  - the shim's FUSE server (every step `openat2` beneath the export);
  - fuse-overlayfs (inside the sandbox);
  - the kernel.
- **A compromised manager** reaches only its own sandboxes, its own resources
  (as mounts), its bound `sandbox-net` classes and its quotas. A frame
  principal of the manager tile (a user's devtools) gets 403 on every route.
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
  - Restore re-validates each definition, merges it by name, and brings it
    back `stopped`. State that is still on disk is kept when its base
    resolves.
  - Restore never touches `.xbin/sbx`.
- **State moves only through snapshots and clones** (WP-20), and later
  archive/thaw (WP-22).
- **Offload** stops the tile's sandboxes. While any of them has state (an
  upper, a disk or a snapshot), offload is refused 409: "archive or delete its
  N sandboxes (X GiB) first". Once WP-22 lands, offload carries them instead:
  each is archived under `<CK>.sbx.<name>`, and local state is removed only
  after every PUT succeeds. Offload never drops state.
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
  - `scopeDiskUsage` adds the tile's sandbox bytes (allocated, measured as in
    §6.3) through a `Usage` hook.
  - `vm.ListDisks` globs `.xbin/sbx/*/*/vm/disk.img`.
  - The admin view maps each disk to its tile and sandbox.
- **Base pinning** (decision 9).
  - `internal/layers.Pinned` is the union of three sources: the
    `.xbin/term/*` stamps, the `.xbin/sbx/*/*` stamps, and the `base` of every
    definition, archived ones included.
  - Both `GCBaseImages` and `vm.GC` keep what that union pins.
  - `restart` and `thaw` keep the pin. Only `reset` and `rebase` change it.
- **The existing term-layer backup reads and restores sandbox-written trees
  as xbind.** The storage map found that restore writes through planted
  symlinks. WP-9 fixes it first. It's not a tile-sandbox feature, but tile
  sandboxes must not copy the pattern.

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

// Forward passes a manager's own request through to the runtime route sub
// ("execs/<id>/output", "files/content", "tar", "execs/<id>/tty", …) with
// the query q the manager chose — never the consumer's raw query. It
// streams both bodies, copies status/Content-Type/ETag/Content-Length, drops
// the inbound Cookie, Authorization, Sbx-User, X-XBin-* and
// Sec-WebSocket-Extensions, and tunnels an Upgrade byte for byte
// (httputil.ReverseProxy over Client().Transport, Upgrade headers restored
// as internal/proxy does). It returns once the response (or tunnel) is done.
func (b *Sandbox) Forward(w http.ResponseWriter, r *http.Request, sub string, q url.Values)
func (b *Sandbox) RelayTTY(w http.ResponseWriter, r *http.Request, execID string, o TTYOptions) // Forward to execs/<id>/tty
func (b *Sandbox) RelayNewTTY(w http.ResponseWriter, r *http.Request, o TTYStart)              // Forward to tty?cwd=&cmd=…
func (b *Sandbox) DialTTY(ctx context.Context, execID string, o TTYOptions) (*ws.Conn, error)   // sdk/ws, via Client()

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
  SSH bridge. It uses the stdlib `sdk/ws` package (being built on `p3/prep`).

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
| `POST sandboxes` | `POST /sandboxes` · `Create` | `clientId` dedupe per consumer (its table: consumer + clientId → id + request hash, for `exists`); id; `image` → `from: {sandbox: img-<image>, snapshot}` (built on first use by a setup `run`, then snapshotted); `size` → `memMiB`/`vcpus`/`diskGiB`; `egress` → `net.egress`; `mode` → `vm` by default, else `namespace` (`isolation` follows); `for` = the consumer, `forUser` = the person; `defaults` (workdir, user, home, shell, `SANDBOX_ID`/`SANDBOX_NAME`); `state: creating` while the image builds |
| `GET/PATCH/DELETE sandboxes/{id}` | `/sandboxes/{name}` · `Get`/`Patch`/`Delete` | name, visibility, members, shares and labels stay in its own table (`version` is its own); `size`, `egress`, `autoStopMin` go to the runtime (`idleStopMin`, `egressNext`); only the home consumer deletes |
| `start` · `stop` (`?wait`) | same | — |
| `archive` · `thaw` | WP-22 (until then `unsupported`) | — |
| `run` | `POST …/run` · `Run` | the body passes through; it adds `uid`/`gid` from its user and `forUser` |
| `execs` (POST/GET/one/DELETE) | same · `Exec`/`Execs`/`GetExec`/`Kill` | `clientId` → the runtime's, prefixed `<consumer hash>:` (contract clientIds are per consumer); `forUser` |
| `…/output`, `stdin`, `signal`, `resize` | same routes · `Forward` | nothing: identical query and shapes |
| `…/execs/{eid}/tty`, `…/tty` | same routes · `RelayTTY`/`RelayNewTTY` | its person check (verified `X-XBin-User`); `forUser` = that person; `sessionId`/`sandboxId` = its ids |
| `files/*`, `tar` | same routes · `Forward` | nothing |
| `snapshots` | same · `Snapshots`… | snapshot names in its table if it wants them |
| clone (`from`) | `POST /sandboxes` with `from` | — |
| errors | the same enum | passed through (Forward) or rebuilt from `*SandboxError` |

**What the manager keeps**, in a sqlite resource of its own:

- **sandboxes:** id, display name, image, size id, contract egress word,
  owner `{user, via, asserted}`, visibility, members, shares, labels, created,
  version, and a state overlay (`creating`, `deleting`, `error`);
- **client ids:** consumer + clientId → id + request hash;
- **images:** id → image sandbox, snapshot and base version. When
  `base.outdated` shows, the image is rebuilt.

**What it doesn't keep:** execs and their output (the runtime's, lost with
xbind, as the contract allows); egress policy (bindings); quotas across
tiles (the runtime's books).

**Inside a sandbox** it sets `IN_SANDBOX=1` (the runtime already does),
`SANDBOX_ID`, `SANDBOX_NAME`, `HOME` and the user through `defaults`.
`workdir` exists because the image's setup script made it.

## 12. Work packages

**Tracks.** Wave 1 runs as four parallel tracks with disjoint files. Wave 2 is
the runtime proper and waits on its inputs. Wave 3 closes phase 2 and opens
phase 3.

```
WP-0 (external: p2/vmfix) ─┐
Track A  exec core:     WP-1 ──┬─ WP-3 ─┐           (WP-2 ∥ WP-1)      then WP-4
                        WP-2 ──┘        │
Track B  books:         WP-5 · WP-6 · WP-7 · WP-8 · WP-9   (independent of each other)
Track C  net + wire:    WP-10 → WP-11 · WP-12
Track D  API + SDK:     WP-13 → WP-14
Wave 2:  WP-15a (runtime core) → { WP-15b lifecycle ∥ WP-16 VM ∥ WP-17 execs/tty ∥ WP-18 files }; WP-15b → WP-19 workspace
Wave 3:  WP-20 snapshots/clone → WP-21 fixture + e2e = "phase 3 ready"
Later:   WP-22 archive/thaw (+ offload carries, streaming archive I/O, s3 multipart)
```

Each WP ends green on `make check` (fmt, vet, the unit tests, `apicheck`,
`sizebudget`, `docscheck`, `TestNoDirectExec`). It also passes the
integration packages it touches, run with the Bash sandbox disabled on this
box. Docs and the changelog travel with the change that makes them true.

### WP-0 — VM backends listen *(external dependency, in flight on `p2/vmfix`)*

Another agent is fixing a Go backend in a VM that never reports
`listening`. The VM map's lead: the guest's `/dev/fuse` pumps run in PID 1
next to its vforking `os.StartProcess`.

- **Why it blocks us:** WP-1 and WP-4 edit the same guest and shim files,
  and the resident design mustn't ship before the pumps leave PID 1.
- **What we need from it:** the relay process registered with the reaper,
  which WP-1's `Spawner.Register` keeps.

### WP-1 — `proto` additions + `agentcore` (Track A · M · after WP-0)

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

### WP-3 — `bx __sbx-agent` (Track A · S · after WP-1, WP-2)

- **Goal:** §2.4.
- **Files:**
  - new `cmd/bx/sbxagent.go`;
  - `cmd/bx/agent.go`: +2 lines in `cmdExtra`, which is at 786/800;
  - new `internal/sandbox/agentcore/ns_linux.go` (`RunNamespace`).
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

### WP-4 — The resident shim (Track A · M · after WP-0, WP-1, WP-2)

- **Goal:** §2.5.
- **Files:**
  - `proto/hostspec.go` (`Resident`, `AgentFD`);
  - new `internal/sandbox/vm/host/resident_linux.go`;
  - `host/shim_linux.go` (the branch after `ready`; its size stays ~470);
  - `internal/sandbox/init_vm_linux.go` (`writeVMSpec` copies `AgentFD`;
    the fd is kept across the VM lockdown);
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
    - factory close → the VMM is gone within 3 s (18 s emulated).
- **Size:** `resident_linux.go` ≤ 450.
- **Parallel:** with WP-3.

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

### WP-15a — The runtime core: launch, agent client, start and stop (wave 2 · M, the critical path)

- **Goal:** a namespace sandbox that starts, answers the agent and stops
  cleanly, with egress, mounts, a cgroup leaf and a registry row. It
  includes a minimal exec path through the agent client, enough for tests;
  WP-17 adds the API.
- **Files:**
  - `internal/tilesbx/launch.go`: the Spec builder for both modes; mounts →
    binds; env;
  - `agent.go`: the agent client (factory, ctl reader, session allocation,
    event demux, stream dial, bounded frames);
  - `lifecycle.go`: the state machine's start and stop, single flight, ready
    timeouts, the log ring;
  - `registry.go`;
  - `internal/sbx/sbx.go` (`Entry.Name`, `For`, `ForUser`);
  - `internal/boot/sandboxes.go` (`case sbx.Tile`);
  - `internal/boot/serve.go` (`StopAll` at shutdown);
  - `internal/boot/tilesandboxes.go` (the Deps wiring).
- **Tests:**
  - Unit, with a fake launcher: start/stop transitions; a failed start
    unwinds everything, records `sbx.Fail` and returns to `stopped`.
  - Integration (`linux && integration`; the rootfs or a minimal lower; a
    static `bx`):
    - create → start → exec `true` → stop → start: the upper persists;
    - egress `none`: TCP to 1.1.1.1 is reset, DNS REFUSED, both under
      100 ms;
    - a read-only mount refuses writes; a `Sub` symlink is refused;
    - leaf limits written; the registry row added and removed;
    - `unshare -U` fails inside.
  - Add `./internal/tilesbx/` to `make integration`, twice (KVM, then
    emulate), like `internal/vm`.
- **Size:** each file ≤ 600 lines.
- **Depends on:** WP-2, WP-3, WP-7, WP-8, WP-10, WP-11 (it can start with
  `none` only), WP-13.

### WP-15b — Lifecycle policy: admission, idle, auto-start, reset, restart semantics (wave 2 · M · after WP-15a)

- **Goal:** the rest of §6.1 and §7.
- **Files:**
  - `internal/tilesbx/admission.go`;
  - `idle.go` (the timer and the activity record);
  - `lifecycle.go`: auto-start, reset and rebase (confined `rm` of the
    state, then a re-pin), the lock wait, and the `error` state for a
    missing base or a changed flavour;
  - `sweep.go` (at boot: `Sweep("sbx-")`, every sandbox `stopped`);
  - `api_lifecycle.go` (filled).
- **Tests:**
  - Unit, with an injected clock: admission per cap and per mode; the idle
    timer re-arms, fires, and is held off by a running non-tty exec; exec
    ids from a new boot answer `lost`; a definition is re-validated at
    start, so a mount the tile no longer holds fails the start with the
    reason.
  - Integration:
    - reset wipes the upper;
    - closing the factory kills the sandbox, and a restarted Manager finds
      it `stopped`;
    - a second start while an orphan holds the lock waits, then fails;
    - an exec on a stopped sandbox starts it.
- **Parallel:** with WP-16, WP-17 and WP-18, which need only WP-15a.

### WP-16 — VM mode (wave 2 · M · after WP-4, WP-6, WP-15a)

- **Goal:** `mode: "vm"`, with resident VMs, the tile sub-budget,
  `EnsureDiskAt`, the emulation gate and report, leaf sizing, and stop by
  SIGHUP → `synced`.
- **Files:**
  - new `internal/tilesbx/vm.go`;
  - hook points in `launch.go` and `lifecycle.go`;
  - boot wiring for VM policy flips.
- **Tests** (integration, KVM and `XBIN_VM_ACCEL=emulate`):
  - create, start, exec, stop keeps the disk;
  - a `diskGiB` grow applies at the next start;
  - a sub-budget refusal is an `sbx.Refuse` failure;
  - `tiles:false` → `unavailable` with the reason;
  - emulation is refused unless `tilesEmulated`;
  - FUSE mounts; egress `none` in a VM.
- **Parallel:** with WP-17, WP-18 and WP-19.

### WP-17 — Execs, run, output, TTY (wave 2 · M · after WP-12, WP-15a)

- **Goal:** §3.5–3.7.
- **Files:**
  - `internal/tilesbx/{exec.go, ring.go, run.go, tty.go}`;
  - `api_exec.go` (filled);
  - `internal/broker/usersapi_personal.go` (the `OnNoTerminal` hook).
- **Tests:**
  - Unit:
    - ring: offsets, a gap reported, a wait answered at exit, the text mode's
      UTF-8 boundary, the tile budget;
    - run: head/tail/elided arithmetic identical to the contract's examples,
      and UTF-8 replacement.
  - Integration:
    - 20 concurrent execs;
    - a run's timeout kills the group, grandchild included;
    - a hang-up kills the group;
    - `lost` after a restart;
    - a gorilla client on the TTY: the session frame first, with overrides;
      replay; an echo ack; `exit {code}`; a `noTerminal` `forUser` gets 403.
- **Parallel:** with WP-16, WP-18 and WP-19.

### WP-18 — Files, tar, copy (wave 2 · S/M · after WP-15a)

- **Goal:** §3.8.
- **Files:** `internal/tilesbx/files.go`; `api_files.go` (filled).
- **Tests** (integration):
  - stat, list and ranged content;
  - an atomic PUT; `ifMatch` → 412; `ifNoneMatch`; `mkdirs`;
  - `fileMax` → 413;
  - a tar round trip keeps symlinks;
  - a tar with `../x` and absolute entries lands inside;
  - `/work/l → /etc`, then `PUT /work/l/x`, writes the **sandbox's** `/etc/x`
    and never the host's;
  - a read-only mount refuses;
  - a copy between two sandboxes; another tile's sandbox is `not-found`.
- **Parallel:** with WP-16, WP-17 and WP-19.

### WP-19 — Workspace integration (wave 2 · M · after WP-5, WP-9, WP-15b)

- **Goal:** §7's reconciles and §9.
- **Files:**
  - new `internal/broker/tilesbx_hooks.go` (`TileSandboxHooks`: `Defs`,
    `RestoreDefs`, `StopAll`, `HasState`, `Leftovers`, `Usage`);
  - `broker/lifecycle.go`;
  - `broker/backup.go` (the manifest field, the offload refusal,
    `doRestore`);
  - `internal/backup/backup.go` (`Manifest.Sandboxes`);
  - `broker/policy.go` (`pathLeftovers`);
  - `broker/broker.go` / `diskmon.go` (usage), fed by a new
    `internal/tilesbx/usage.go`: a confined `du` at each stop, VM disks by
    allocated blocks, and the per-tile disk check in admission;
  - boot wiring: `OnCapChange` → `StopTile`, structure changes → reconcile,
    policy flips;
  - `workspace-template/tiles/admin/tabs/sandboxes.js` (tile rows, stopped
    and orphaned definitions, stop/delete, the sandboxes policy editor);
  - `hack/ui-harness/passes/sandboxes.js`;
  - docs: `docs/overview/02-workspace.md`, `docs/overview/14-lifecycle.md`,
    `docs/sandbox-manager.md` (a short "On xbin" section), changelog.
- **Tests:**
  - A tile without sandboxes backs up byte-identical; definitions ride along;
    a restore brings them back stopped, state untouched.
  - Offload is refused with state and allowed without it.
  - Disabling a tile stops its sandboxes; so does revoking the cap.
  - `pathLeftovers` lists them.
  - The harness pass: the admin tab shows tile rows and stops or deletes
    one.
- **Parallel:** with WP-16, WP-17 and WP-18.

### WP-20 — Snapshots and clones (wave 3 · M · after WP-8, WP-15b, WP-16)

- **Goal:** §3.9. The manager's images depend on clones.
- **Files:**
  - new `internal/fsutil/clone_linux.go` (`CloneSparse`) with an `_other`
    stub;
  - `internal/tilesbx/snapshot.go`; `api_snapshots.go` (filled);
  - `from` in create;
  - confined `cp -a --reflink=auto` with `FSCaps` for uppers.
- **Tests:**
  - `CloneSparse`: sparseness kept on tmpfs; `FICLONE` used where the
    filesystem supports it.
  - Integration in both modes: snapshot, modify, restore; a whiteout (a
    removed package) survives snapshot, restore and clone; clone from a
    snapshot; a mode or base mismatch is refused; snapshots count toward
    the tile's disk.
- **Parallel:** with WP-21's fixture work.

### WP-21 — Fixture, end to end, phase 3 gate (wave 3 · M)

- **Goal:** prove the whole path through a real manager tile, and hand off
  to phase 3.
- **Files:**
  - new `examples/sandbox-go/`: a minimal manager with `cap:sandboxes`, an
    `internet` `sandbox-net` slot, and a backend on the SDK that exposes
    create, run, exec, output, files and a relayed TTY. It is an integration
    fixture and doubles as docs;
  - new `test/isolated/` (`linux && integration`; skips without the rootfs,
    userns or VM assets). It boots xbind with `--isolate`, imports the
    example, approves the cap, and drives everything through the proxy:
    - a TTY through the manager's `RelayTTY` with a gorilla client;
    - a restart: the exec is `lost`, the sandbox `stopped`, the state kept;
    - the same in VM mode where KVM is present;
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
  offload carries; GC pins archived bases (already in WP-7's union).

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
  - The userns is single-uid: `users` answers `root`, and range-mode
    behaviour (sub-uid owners, `FSCaps` reading other uids' files) is
    verified on the QA box.
  - Kill only the PIDs you started.
- **Harness.** The passes `sandboxes`, the bindings rows, and the
  `predict`/`termrun` passes (termwire). They are the only JS regression
  tests.

## 14. Risks and open items

- **Merge order with dev-lifecycle.** Both branches edit `internal/sbx`,
  `internal/vm/policy.go`/`reserve.go`, `internal/cgroup`, `confine.go`,
  `init_linux.go`, `broker/backup.go`, `term.go`'s budget line,
  `openapi.go`, `protocol.md` and the admin `sandboxes.js`. Whichever lands
  second rebases. Each of those WPs names its collision.
- **Emulated VMs** are 5–20× slower. Stream attach and ready timeouts are
  stretched (×3 or ×6), and xbind opens streams before it sends `exec`.
- **fuse-overlayfs** is slower than a kernel overlay for build-heavy work.
  The flavour stamp keeps it consistent; measuring the difference is a
  WP-21 note, not a blocker.
- **The relay per sandbox** costs about 2.5 MiB on a 192-CPU host (its
  goroutine count follows `GOMAXPROCS`). Setting `ProcessorsPerChannel: 1`
  halves it (WP-10).
- **Running work dies with xbind** (decision 4). Auto-updating installs will
  kill long jobs. A relay out of process is the later fix, if it is ever
  wanted.
- **Native.** A consumer's native view can't point the `terminal` primitive
  at a manager: it allows only its own `/api/<self>/…`. The agent tile
  relays a second hop with `Forward`. Widening the vocabulary is an app
  release, left to phase 3.
