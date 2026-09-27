# sandbox-manager — coding sandboxes as a service

A **sandbox manager** is a tile that creates and runs sandboxes — boxes with
a shell, a filesystem and the tools of a job — for other tiles. A
**consumer** is a tile that uses them: the agent template (an agent
conversation works in a sandbox), a terminal tile (people open shells in
them), anything else. The split (D115):

- **The manager knows the substrate:** VMs, containers, a cloud's API and
  ssh, disks, images, quotas, what a sandbox may reach.
- **The consumer knows who and why:** the people it acts for, its own
  conversations, which of its users may use which sandbox.

The builtin manager will be the `coding-sandbox` template (VM sandboxes
on xbind's own runtime; it is on its way — until then the reference
manager below is the one to test against). Any tile that implements the
routes below is a manager — one that runs sandboxes on a cloud over its API and ssh, for
example. This page is the contract, **protocol 1**.

## Wiring

A manager provides the service, with a role of its own that reaches only the
`/sbx/` routes (its operator pages keep theirs):

```jsonc
"provides": { "sandboxes": { "kind": "http", "service": "sandbox-manager", "role": "consumer" } },
"expose": { "roles": { "consumer": "Use this tile's sandboxes (the sandbox-manager contract)" } }
```

A consumer requests it; several managers can be bound at once:

```jsonc
"interfaces": { "sandboxes": { "kind": "http", "service": "sandbox-manager", "multi": true } }
```

The owner binds them — `bx bind apps/agent sandboxes+=apps/<manager>`,
or the binding panel — and the binding grants the consumer the `consumer`
role. The consumer's backend finds its managers in
`XBIN_IFACE_SANDBOXES` (`[{provider, instance?, url, service}]`) and calls
them with its instance client (`xbin.Client()` in Go); its page finds them in
`xbin.iface("sandboxes")`. Rebinding restarts the consumer. Every route
below is under `<url>/sbx/`.

## Who is asking

- **The consumer** is `X-XBin-From`: the calling tile's path, set by xbind
  (inbound `X-XBin-*` headers are stripped). A manager trusts it.
- **A person, verified**, is `X-XBin-User`: xbind sets it only on calls a
  tile's page makes with its frame token (docs/auth.md). A manager
  enforces its per-person rules (below) on such a call.
- **A person, asserted**, is `Sbx-User: <user id>`: a consumer's backend
  (whose calls carry no person) names the person it acts for. The manager
  records it as asserted (`owner.asserted`), shows it, and does not verify
  it; a verified `X-XBin-User` always wins over it.

Nothing else travels in headers: parameters are in the query or the JSON
body (a sandboxed page can't set custom request headers).

## Partitions, sharing and people

- **A sandbox's home** is the consumer that created it (`owner.via`). A
  consumer sees — lists, reads, uses — only the sandboxes whose home it is
  and those **shared** with it: `shares: [{consumer, users}]`, where `users`
  is `"*"` or a list of user ids. Only the home consumer (or the manager's
  own operators) changes `shares`. For every other consumer a sandbox it
  can't see doesn't exist (`not-found`).
- **People.** A sandbox has an `owner.user`, a `visibility` (`private`: the
  owner and `members`; `team`: anyone the consumer serves) and `members`.
  On a **verified** call the manager enforces them: the person must be the
  owner, a member, or the sandbox must be `team` — and on a shared
  consumer, the share's `users` must include them. On a **backend** call
  the consumer is trusted to enforce its own rules for the person it acts
  for; the manager only records the assertion.
- Changing `visibility`, `members` or `shares` takes the home consumer's
  backend, the owner (verified, through the home consumer), or the
  manager's operators. Only the home consumer deletes a sandbox.

## Conventions

- Bodies are JSON, times are unix milliseconds, sizes are bytes unless
  named (`memMiB`, `diskGiB`).
- **Paths** are absolute POSIX paths **inside** the sandbox; symlinks
  resolve inside it. A manager never resolves a sandbox-supplied path
  against host files a sandbox can write.
- **`?wait=<seconds>`** (up to `limits.waitMaxSec`) on any lifecycle call
  returns when the transition is done or the wait runs out; the resource
  returned always says where it stands.
- **Limits.** A request parameter above its `hello.limits` value
  (`maxOutput`, `timeoutMs`, an output read's `max` and `waitMs`, `?wait`)
  is clamped to it, never refused; a body over one (a file over `fileMax`,
  a tar over `tarMax`) is `too-large`.
- **Idempotency.** `clientId` on creates (unique per consumer), execs and
  snapshots (unique per consumer and sandbox): repeating it returns the
  existing object (200); reusing it for a different request is `exists`.
- A **stopped** sandbox starts on an exec or a file operation; an
  **archived** one doesn't (`state` — thawing is explicit, it may be slow
  or cost money).
- **Errors** are `{error, refusal, state?, etag?, retryAfterMs?,
  protocols?}` with the status below. `error` is for people; `refusal` is
  for programs:

| refusal | status | meaning |
|---|---|---|
| `protocol` | 400 | an unknown protocol version (`protocols` lists the known ones) |
| `invalid` | 400 | a bad field, or a `cwd` that doesn't exist |
| `not-found` | 404 | no such sandbox, exec, snapshot or path — or not visible to the caller |
| `not-allowed` | 403 | the verified person may not do this |
| `state` | 409 | not in a state that allows it (`state` names the current one) |
| `exists` | 409 | a `clientId` reused for a different request |
| `precondition` | 412 | `ifMatch` / `ifNoneMatch` failed (`etag` is the current one) |
| `too-large` | 413 | over a limit in `hello.limits` |
| `limit` | 429 | too many sandboxes, running execs, a quota |
| `unsupported` | 501 | a capability this manager doesn't offer |
| `lost` | 410 | the exec is gone (its sandbox restarted) |
| `unavailable` | 503 | the substrate is down or still starting (`retryAfterMs`) |

## `GET /sbx/hello?protocol=1`

```json
{"protocol": 1, "protocols": [1],
 "manager": {"name": "coding-sandbox", "title": "Coding sandboxes", "version": "1.0.0"},
 "caps": ["exec", "files", "tar", "tty", "snapshots", "clone", "archive"],
 "egress": ["none", "internet"],
 "images": [{"id": "base", "title": "Debian with git, Go and Node", "default": true, "tools": ["git", "go", "node", "rg"]}],
 "sizes": [{"id": "small", "memMiB": 2048, "vcpus": 2, "diskGiB": 20, "default": true}],
 "limits": {"sandboxes": 0, "runTimeoutMaxMs": 600000, "runOutputMax": 1048576,
            "execsRunning": 16, "outputRing": 1048576, "stdinMax": 1048576,
            "fileMax": 67108864, "tarMax": 1073741824, "waitMaxSec": 120}}
```

`exec` and `files` are required in protocol 1; `tar`, `tty`, `snapshots`,
`clone` and `archive` are optional, and a route whose capability is missing
answers `unsupported`. `limits.sandboxes` 0 means no fixed limit.

## The sandbox

```json
{"id": "sb-7f3a", "name": "api-dev",
 "state": "running", "stateDetail": "",
 "image": {"id": "base", "title": "Debian with git, Go and Node"},
 "size": {"id": "small", "memMiB": 2048, "vcpus": 2, "diskGiB": 20},
 "isolation": "vm",
 "egress": "none", "egressDetail": "",
 "owner": {"user": "alice", "via": "apps/agent", "asserted": true},
 "visibility": "private", "members": ["bob"],
 "shares": [{"consumer": "apps/sandbox-terminal", "users": "*"}],
 "shared": false,
 "labels": {"xbin.agent/conversation": "42"},
 "workdir": "/work", "home": "/home/dev", "user": "dev", "shell": "/bin/bash",
 "caps": ["exec", "files", "tar", "tty"],
 "created": 1790000000000, "lastActive": 1790000100000, "autoStopMin": 30, "version": 7}
```

- `id` matches `[A-Za-z0-9][A-Za-z0-9._-]{0,63}`; `name` is free text (≤64).
- `state`: `creating`, `stopped`, `starting`, `running`, `stopping`,
  `archiving`, `archived`, `thawing`, `deleting` or `error`
  (`stateDetail` says why).
- `isolation`: `vm`, `container`, `namespace`, `cloud-vm` or `other` — what
  keeps the sandbox from its host.
- `egress` — what the sandbox may reach **now**, always present:
  - `none` — nothing: no network at all.
  - `internet` — the public internet only: no private or local networks,
    nothing of the workspace.
  - `open` — whatever the manager's substrate gives (a LAN, say).
- `egressNext` — the egress a `PATCH` set that applies at the sandbox's next
  start; present only while it differs from `egress`. A `PATCH` of a
  running sandbox's egress sets it (`restartNeeded`); of a stopped or
  archived one, it applies at once. In a state between (`starting`,
  `stopping`, `thawing`) the manager picks either, as long as `egress`
  never claims less than the sandbox can reach. A consumer that enforces a
  firewall on egress checks the **less restrictive** of `egress` and
  `egressNext` — a stopped sandbox
  starts on an exec, and takes `egressNext` — in the order `none` <
  `internet` < `open`, counting a missing or unknown value as `open`.
- `shared` is true when the caller sees the sandbox through a share.
- `labels` are the consumer's (≤1 KiB in all); the manager stores them.
- `caps` may be fewer than `hello.caps` for this sandbox.
- `version` increments on every change; `PATCH` may pass it back to refuse
  a lost update (`precondition`).

### Routes

| Method & path | Body | Result |
|---|---|---|
| `GET /sbx/sandboxes` | | `{"sandboxes": [sandbox…]}` — the caller's own and those shared with it (a verified person: only those they may use) |
| `POST /sbx/sandboxes` | `{name, image?, size?, egress?, visibility?, members?, labels?, clientId?, start?, from?}` | **201** + the sandbox. `start` defaults to true; `from: {sandbox, snapshot?}` clones (`clone`) |
| `GET /sbx/sandboxes/{id}` | | the sandbox |
| `PATCH /sbx/sandboxes/{id}` | `{name?, visibility?, members?, shares?, labels?, egress?, size?, autoStopMin?, version?}` | the sandbox, with `restartNeeded: true` when a change applies at the next start (an egress: `egressNext`) |
| `DELETE /sbx/sandboxes/{id}` | | **204** |
| `POST /sbx/sandboxes/{id}/start` · `/stop` · `/archive` · `/thaw` | `{start?}` on thaw | the sandbox |

The owner of a new sandbox is the verified person, else the asserted one,
else none (a sandbox of the consumer itself). Stopping, archiving,
restoring or deleting a sandbox kills its execs.

## Running commands (`exec`)

A command is either `cmd` — a string run by the sandbox user's login shell
(`$SHELL -lc`, else `sh -c`) — or `argv`. `cwd` must exist (`invalid`
otherwise; never a silent fallback to `/`); it defaults to `workdir`. A
command's process group is what signals, timeouts and kills reach.

### `POST /sbx/sandboxes/{id}/run` — run and wait

```json
{"cmd": "go test ./...", "cwd": "/work/api", "env": {"CI": "1"},
 "stdin": "", "timeoutMs": 60000, "maxOutput": 65536, "merge": false}
```

→ `{"exitCode": 1, "signal": "", "timedOut": false, "ms": 4210,
"stdout": {"head": "…", "tail": "…", "elided": 1024, "bytes": 66560},
"stderr": {…}}` (with `merge`, one `"output"` in their place). Output past
`maxOutput` (per stream; at most `limits.runOutputMax`) keeps its first
quarter in `head` and its last three quarters in `tail`, `elided` bytes
between; output that fits is all in `head`. At `timeoutMs` (at most
`limits.runTimeoutMaxMs`) the group gets TERM, then KILL 5 s later, and
`timedOut` is true. `timeoutMs` defaults to 60000. `exitCode` is null
when a signal ended the command (`signal` names it). The run belongs to
its request: if the caller hangs up, the group is killed. Output is UTF-8
(invalid bytes replaced). An `argv` whose program can't start is
`invalid` (a `cmd`'s shell reports exit 127 instead).

### Background execs

| Method & path | Body / query | Result |
|---|---|---|
| `POST /sbx/sandboxes/{id}/execs` | `{cmd\|argv, cwd?, env?, tty?, rows?, cols?, stdin?, timeoutMs?, label?, clientId?}` | **201** + the exec |
| `GET /sbx/sandboxes/{id}/execs` | | `{"execs": [exec…]}` |
| `GET /sbx/sandboxes/{id}/execs/{eid}` | | the exec |
| `GET /sbx/sandboxes/{id}/execs/{eid}/output` | `since, max, waitMs, encoding` | a chunk (below) |
| `POST /sbx/sandboxes/{id}/execs/{eid}/stdin` | raw bytes; `?eof=1` closes stdin | **204** (only with `stdin: true`) |
| `POST /sbx/sandboxes/{id}/execs/{eid}/signal` | `{signal: "INT"\|"TERM"\|"KILL"\|"HUP", group?}` | **204** (`group` defaults to true) |
| `POST /sbx/sandboxes/{id}/execs/{eid}/resize` | `{rows, cols}` | **204** (a `tty` exec) |
| `DELETE /sbx/sandboxes/{id}/execs/{eid}` | | **204** — kills the group, forgets the exec |

An exec is `{id, label, cmd, argv, cwd, tty, state, exitCode, signal,
started, ended, total, clientId}`; `state` is `running`, `exited`, `killed`
(a signal, its timeout, a stop or a `DELETE` ended it — `exitCode` is null
when a signal did) or `lost`; `timeoutMs` 0 means none. `stdin` to an exec
started without `stdin: true`, or after `eof`, is `invalid`; after it
ended, `state`. Its stdout and stderr are **one
combined byte stream** (a `tty` exec's is the terminal's), kept in a ring
of at least `limits.outputRing` bytes. A finished exec is kept at least an
hour or the last 50 per sandbox.

**Reading output** — `GET …/output?since=<offset>&max=<bytes>&waitMs=<ms>&encoding=text|base64`
(`max` ≤ 1 MiB, default 64 KiB; `waitMs` ≤ 30000):

```json
{"start": 2048, "end": 4096, "total": 9000, "ringStart": 0,
 "data": "…", "encoding": "text", "state": "running", "exitCode": null, "signal": ""}
```

Offsets count bytes of the stream from its beginning. The answer carries
the bytes from `since` (or the ring's oldest, `start > since` meaning the
gap was dropped) up to `end`. With nothing past `since` and the exec still
running it waits up to `waitMs` for more — and answers as soon as the exec
ends. `text` is UTF-8 with invalid bytes
replaced; `base64` is exact. A reader resumes from the last `end` it saw —
across its own restarts, too.

### Terminals (`tty`)

`GET /sbx/sandboxes/{id}/execs/{eid}/tty` attaches to a `tty` exec, and
`GET /sbx/sandboxes/{id}/tty?cwd=&cmd=&rows=&cols=` starts one (the login
shell unless `cmd`) and attaches — both are WebSocket upgrades speaking
exactly the terminal framing of `/ws/term` (docs/protocol.md §`/ws/term`),
so `<bx-terminal>` and the xbin app's `terminal` work against it:

- **Binary frames** both ways: raw terminal bytes. The ring's tail replays
  first.
- **Server → client** JSON: `{"op":"session","id":"<exec id>","sandbox":"<id>","echoAck":false}`
  first; `{"op":"exit","code":0}` when the command ends; with `echoAck`,
  `{"op":"ack","n":N}` and `{"op":"pong","t":…}` as `/ws/term` sends them.
- **Client → server** JSON: `{"op":"resize","cols":120,"rows":32}`,
  `{"op":"ping","t":…}`. Unknown ops are ignored on both ends.

A page connects with its frame token (`xbin.ws(url)`), so the manager sees
the verified person.

## Files (`files`) and trees (`tar`)

| Method & path | Body / query | Result |
|---|---|---|
| `GET /sbx/sandboxes/{id}/files/stat` | `path` | `{path, type: "file"\|"dir"\|"symlink"\|"other", size, mode, mtimeMs, etag, target?}` |
| `GET /sbx/sandboxes/{id}/files/content` | `path, offset?, length?` | the bytes, with an `ETag` |
| `PUT /sbx/sandboxes/{id}/files/content` | raw bytes; `path, mode?, mkdirs=1, ifMatch=<etag>, ifNoneMatch=*` | the file's stat |
| `GET /sbx/sandboxes/{id}/files/list` | `path, limit? (1000)` | `{path, entries: [{name, type, size, mtimeMs, mode, target?}], truncated}` |
| `POST /sbx/sandboxes/{id}/files/mkdir` | `{path, parents?}` | **204** |
| `POST /sbx/sandboxes/{id}/files/remove` | `{path, recursive?}` | **204** |
| `POST /sbx/sandboxes/{id}/files/move` | `{from, to, overwrite?}` | **204** |
| `GET /sbx/sandboxes/{id}/tar` | `path, exclude=` (repeatable) | `application/x-tar`, names relative to `path` (`tar`) |
| `PUT /sbx/sandboxes/{id}/tar` | a tar stream; `path, mkdirs=1` | **204** — extracted under `path` (`tar`) |

`mode` is the permission bits as an octal string (`"0644"`). `move` onto
an existing path without `overwrite` is `precondition`; `mkdir` without
`parents` of an existing path, `remove` of a non-empty directory without
`recursive`, and `tar` on a path that isn't a directory are `invalid`.

A file's `etag` changes whenever its content does (a content hash, or its
modification time with nanoseconds, size and inode). `PUT content` replaces
the file atomically (a temporary file renamed into place); `ifMatch` makes
an edit safe against a concurrent one (`precondition` with the current
`etag`), `ifNoneMatch=*` creates only. A file over `limits.fileMax` is
`too-large` — use a ranged read, or `tar`.

## Snapshots, clones and archives

| Method & path | Body | Result |
|---|---|---|
| `GET /sbx/sandboxes/{id}/snapshots` | | `{"snapshots": [{id, name, created, bytes?}]}` (`snapshots`) |
| `POST /sbx/sandboxes/{id}/snapshots` | `{name, clientId?}` | **201** + the snapshot (may stop the sandbox briefly) |
| `POST /sbx/sandboxes/{id}/snapshots/{sid}/restore` | | the sandbox — its execs are killed |
| `DELETE /sbx/sandboxes/{id}/snapshots/{sid}` | | **204** |

A **clone** is `POST /sbx/sandboxes` with `from` (`clone`). An **archive**
(`archive`) frees the sandbox's compute and fast storage and keeps its
contents somewhere cheaper; the sandbox keeps its id, so whatever refers to
it still does, and `thaw` brings it back (stopped, or running with
`start: true`).

## Inside a sandbox

- A POSIX `sh` and the usual tools: coreutils, `find`, `grep`, `sed`,
  `tar`. An image's `tools` names what else it has (`git`, `go`, `rg`, …).
- Commands run as the sandbox's `user`, with `$HOME` = `home`; `workdir`
  exists and is writable. The environment has `IN_SANDBOX=1`,
  `SANDBOX_ID` and `SANDBOX_NAME`.
- **No xbin identity, ever**: no token, no gateway, no route to xbind or the
  workspace's tiles. What a sandbox reaches is its `egress`, nothing more.

## Building a manager

**On a cloud.** Every operation maps onto an instance API and ssh:

| Contract | On a cloud |
|---|---|
| create / start / stop / delete, `?wait` | the provider's instance API, polled |
| `run` | `ssh host -- 'cd -- <cwd> && exec setsid $SHELL -lc <cmd>'`; a timeout sends `kill -TERM -<pgid>` over a second connection |
| execs | `setsid nohup <cmd> > /var/lib/sbx/<eid>/out 2>&1; echo $? > /var/lib/sbx/<eid>/exit`; output is `tail -c +<offset+1>`; signals are `kill -<SIG> -<pgid>` |
| `tty` | `ssh -t`, window-change on resize |
| files | sftp: stat, ranged reads, write to a temporary name and rename, readdir; `etag` a sha256 |
| `tar` | `tar -C <dir> -cf -` / `-xf -` over ssh |
| snapshots, clones, archives | disk snapshots and images; archive = snapshot + terminate |
| `egress: none` | a security group denying all egress (ssh from the manager only) |
| partitions, people | the manager's own table |

**The reference manager** is `hack/fakesandbox` in the xbin repository: the
whole contract in one standard-library Go file (each sandbox a directory on
the host — for tests only), whose tests are the conformance suite a manager
can be checked against.

**Versions.** Protocol 1 grows only by addition: new optional fields,
routes and capabilities. Consumers ignore fields they don't know; managers
ignore unknown request fields. A change that isn't additive is protocol 2,
and `hello` negotiates it.
