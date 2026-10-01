# sandbox-manager — coding sandboxes as a service

A **sandbox manager** is a tile that creates and runs sandboxes — boxes with
a shell, a filesystem and the tools of a job — for other tiles. A
**consumer** is a tile that uses them: the agent template (an agent
conversation works in a sandbox), the `sandbox-terminal` tile (people open
shells in them, in the browser and over SSH; see below), anything else.
The split (D115):

- **The manager knows the substrate:** VMs, containers, a cloud's API and
  ssh, disks, images, quotas, what a sandbox may reach.
- **The consumer knows who and why:** the people it acts for, its own
  conversations, which of its users may use which sandbox.

The builtin manager is the **`coding-sandbox` template** (§The builtin
manager). The conformance suite, `sdk/sandboxcontract`, checks a manager
against this page (below). Any tile that implements the routes below is a
manager — one that runs sandboxes on a cloud over its API and ssh, for
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
  it; a verified `X-XBin-User` always wins over it. (A partitioned
  consumer's user partition is the exception: its person is the
  partition's, verified — §Partitioned consumers.)
- **A partition**, on a call from a **partitioned** consumer — a tile xbind
  runs as one instance per person (a *user partition*), plus maybe one
  shared *global* instance: `X-XBin-Partition` is `user:<user id>` or
  `global`, and a user partition's calls also carry `X-XBin-Partition-Id`,
  that partition's stable, opaque id (a person deleted and made again gets
  a new one). xbind sets both; every other call has neither.
  §Partitioned consumers says what a manager does with them.

Nothing else travels in headers: parameters are in the query or the JSON
body (a sandboxed page can't set custom request headers).

## Consumers, sharing and people

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
  for; the manager only records the assertion (a user partition's backend
  call is its verified person's: §Partitioned consumers).
- Changing `visibility`, `members` or `shares` takes the home consumer's
  backend, the owner (verified, through the home consumer), or the
  manager's operators. Only the home consumer deletes a sandbox.

### Partitioned consumers

A manager whose `hello.caps` carry **`partitions`** keeps a partitioned
consumer's people apart. Added to protocol 1 as an optional capability of
the manager's (never in a sandbox's `caps`):

- **The consumer is (`X-XBin-From`, partition id).** The partition id is
  `X-XBin-Partition-Id` on a user partition's call and **empty for the
  consumer's non-personal identity** — an unpartitioned consumer and a
  partitioned one's `global` instance alike (`""` ≡ `global`). So every
  sandbox a consumer made before it was partitioned stays its global
  instance's. A partition id alone names no consumer: one person's
  partitions of two consumers may carry the same id, so a manager keys on
  both.
- **Home.** A sandbox made in a user partition is homed there: `owner:
  {user, via, partitionId, partition, asserted}` (`partition` is the
  `user:<id>`, for display; both fields are absent otherwise). The
  consumer's global instance, its other partitions and every other
  consumer don't see it (`not-found`) unless it is shared with them.
  `clientId`s are per consumer and partition.
- **The person** of a user partition's call is the partition's, and
  verified — its backend's calls and sockets (`tty`, `stdio`) as much as
  its page's: the person rules apply as on a page's call, and an `Sbx-User`
  or `X-XBin-User` naming anyone else is `403 not-allowed`. So is a call
  whose partition headers don't agree (a user partition without its id, a
  kind the manager doesn't know): it is never taken for the consumer's
  global identity.
- **What the consumer's global identity has.** A user partition also sees
  a sandbox homed at its consumer's non-personal identity, or shared with
  that identity, **where its person may use it there** (`team`, the owner,
  a member; a share's `users` naming them) — a person's page in their
  partition keeps the team's sandboxes. Seen so it is `shared: true`: the
  partition changes neither who may use it nor deletes it. Never the
  converse.
- **Shares** may name one partition: `shares: [{consumer, partitionId?,
  users}]`. Without `partitionId` a share is with the consumer's
  non-personal identity — today's meaning, and by the rule above the
  people it names in the consumer's partitions. A share makes a sandbox
  visible; the person rules still apply to the partition's person. So a
  `private` sandbox shared with a partition whose person is neither its
  owner nor a member is `403 not-allowed` there and listed nowhere: make
  them a member (or the sandbox `team`).
- **A recreated person** (deleted and made again) has a new partition id,
  so nothing homed in the old one's partition is theirs. What the
  consumer's non-personal identity holds follows its person rules, by user
  id, as a page's call there always has: a sandbox the old person owned or
  was a member of there is the new one's too.
- **Isolation stops at the sandbox.** Every partition that sees a sandbox
  shares its files, execs and terminals: any of them can list the execs,
  read their output, attach to a terminal, type into it or end it — and
  attach to an exec's stdio socket (`stdio`), which takes the exec's stdin
  from the socket attached before: a program driven that way, a coding
  agent, then takes its input from that partition. The sandbox's home
  directory (`home`, `$HOME`) is shared the same way, with what lands
  there: a coding agent's sign-in (hello's `harnesses[].login`) leaves its
  credentials there, so whoever runs that agent in the sandbox afterwards
  — from any partition that sees it, or in a clone — runs it as the person
  who signed in, on their account. A consumer should keep a person's
  private work, and their sign-ins, in sandboxes homed in that person's
  partition, never in one its non-personal identity holds or shares with
  several.
- **Quotas** a manager keeps per consumer count a tile's sandboxes across
  all its partitions; per person, as ever (now verified on a partition's
  calls).
- A partitioned consumer uses a manager from a user partition only when
  `hello.caps` carry `partitions`: one without it would see every person's
  sandboxes as one consumer's.
- **Not keyed on `X-XBin-Deployment`.** User partitions run only on a
  consumer's primary deployment, so the partition id already names one;
  the consumer's other deployments call as its non-personal identity, as
  they did before partitions, and keep the sandboxes they made.

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
| `not-listening` | 502 | nothing accepts connections on the port (`ports`) |
| `lost` | 410 | the exec is gone (its sandbox restarted) |
| `unavailable` | 503 | the substrate is down or still starting (`retryAfterMs`) |

## `GET /sbx/hello?protocol=1`

```json
{"protocol": 1, "protocols": [1],
 "manager": {"name": "coding-sandbox", "title": "Coding sandboxes", "version": "1.0.0"},
 "caps": ["exec", "files", "tar", "tty", "stdio", "snapshots", "clone", "archive", "ports", "partitions"],
 "egress": ["none", "internet"],
 "images": [{"id": "base", "title": "Debian with git, Go and Node", "default": true, "tools": ["git", "go", "node", "rg"],
             "harnesses": [{"id": "claude", "title": "Claude Code", "argv": ["claude-agent-acp"], "login": "claude auth login"},
                           {"id": "codex"}]}],
 "sizes": [{"id": "small", "memMiB": 2048, "vcpus": 2, "diskGiB": 20, "default": true}],
 "limits": {"sandboxes": 0, "runTimeoutMaxMs": 600000, "runOutputMax": 1048576,
            "execsRunning": 16, "outputRing": 1048576, "stdinMax": 1048576,
            "fileMax": 67108864, "tarMax": 1073741824, "waitMaxSec": 120}}
```

`exec` and `files` are required in protocol 1; `tar`, `tty`, `stdio`,
`snapshots`, `clone`, `archive` and `ports` are optional, and a route whose
capability is missing answers `unsupported` (a manager from before `ports`,
D135, or `stdio` answers its route `not-found` — a consumer checks `caps`
first). `partitions` is the manager's own, no sandbox's: it keeps a
partitioned consumer's people apart (§Partitioned consumers).
`limits.sandboxes` 0 means no fixed limit.

An **image** is `{id, title, default, tools, harnesses?}`; exactly one is
the default, and `tools` names what it has beyond a POSIX shell (§Inside a
sandbox). **`harnesses`** (optional) are the coding agents installed in the
image that speak the Agent Client Protocol (ACP) — JSON-RPC over stdio, so a
consumer runs one as a non-`tty` exec with `stdin: true`. Each is
`{id, title?, argv?, login?}`:

- `id` matches `[A-Za-z0-9][A-Za-z0-9._-]{0,31}`, unique in the image. The
  well-known ids are `claude` (Claude Code's adapter, `claude-agent-acp`),
  `codex` (`codex-acp`), `gemini` (`gemini --acp`) and `opencode`
  (`opencode acp`): a consumer knows their commands, modes and sign-in, and
  an entry's own fields override what it knows.
- `title` is the name people see (default: the consumer's, else the id).
- `argv` is the command that speaks ACP on its stdin and stdout (default: the
  consumer's, for a well-known id — a consumer ignores an entry it neither
  knows nor has an `argv` for).
- `login` is a shell command that signs the agent in, for a person at a
  terminal (the `tty` route's `cmd`). Credentials land in the sandbox's
  `home`, so everyone who may use the sandbox — every consumer and
  partition that sees it (§Partitioned consumers), and its clones — shares
  them, and runs the agent as the person who signed in. A partitioned
  consumer should offer a person the sign-in only in a sandbox homed in
  their own partition, where the credentials stay theirs unless they share
  the sandbox on. That is the consumer's to keep: to the manager a sign-in
  is a terminal like any other. The agent template keeps it: in a
  partitioned agent a coding agent starts, and signs in, only in a person's
  own conversations, in a sandbox homed in their partition — never at its
  global instance nor in a shared or hosted conversation (its API.md,
  "Partitioned instances").

The list is the manager's word about the image, not a probe (an image's
installs can fail): a consumer may check with `command -v <argv[0]>` through
`run` before offering one. A missing or empty list says nothing about the
image — a consumer may probe for the agents it knows. `login` runs at a
person's terminal when they sign in and `argv` at every run of the agent,
beside their credentials, so whoever may set them — the manager's
operators, whoever may change its code — is in the trust base of every
person who uses the image's coding agents.

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
  archived one, it applies at once. A stop may keep a pending one pending
  or apply it (`egress` becomes it) — the next start takes it either way.
  In a state between (`starting`,
  `stopping`, `thawing`) the manager picks either, as long as `egress`
  never claims less than the sandbox can reach. A consumer that enforces a
  firewall on egress checks the **less restrictive** of `egress` and
  `egressNext` — a stopped sandbox
  starts on an exec, and takes `egressNext` — in the order `none` <
  `internet` < `open`, counting a missing or unknown value as `open`.
- `owner.partitionId` and `owner.partition` are there when the sandbox is
  homed in a partitioned consumer's user partition, and a share may carry
  a `partitionId` (§Partitioned consumers).
- `shared` is true when the caller sees the sandbox through a share (or,
  from a user partition, through its consumer's non-personal identity).
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
| `POST /sbx/sandboxes/{id}/execs` | `{cmd\|argv, cwd?, env?, tty?, rows?, cols?, stdin?, split?, timeoutMs?, label?, clientId?}` | **201** + the exec (`split`: `stdio`) |
| `GET /sbx/sandboxes/{id}/execs` | | `{"execs": [exec…]}` |
| `GET /sbx/sandboxes/{id}/execs/{eid}` | | the exec |
| `GET /sbx/sandboxes/{id}/execs/{eid}/output` | `since, max, waitMs, encoding, stream?` | a chunk (below) |
| `POST /sbx/sandboxes/{id}/execs/{eid}/stdin` | raw bytes; `?eof=1` closes stdin | **204** (only with `stdin: true`) |
| `POST /sbx/sandboxes/{id}/execs/{eid}/signal` | `{signal: "INT"\|"TERM"\|"KILL"\|"HUP", group?}` | **204** (`group` defaults to true) |
| `POST /sbx/sandboxes/{id}/execs/{eid}/resize` | `{rows, cols}` | **204** (a `tty` exec) |
| `GET /sbx/sandboxes/{id}/execs/{eid}/stdio` | `since, errSince` | a WebSocket (`stdio`, below) |
| `DELETE /sbx/sandboxes/{id}/execs/{eid}` | | **204** — kills the group, forgets the exec |

An exec is `{id, label, cmd, argv, cwd, tty, state, exitCode, signal,
started, ended, total, clientId}` (a `split` one adds `split` and
`errTotal`); `state` is `running`, `exited`, `killed`
(a signal, its timeout, a stop or a `DELETE` ended it — `exitCode` is null
when a signal did) or `lost`; `timeoutMs` 0 means none. `stdin` to an exec
started without `stdin: true`, or after `eof`, is `invalid`; after it
ended, `state`. Its stdout and stderr are **one
combined byte stream** (a `tty` exec's is the terminal's; a `split` one's
are two, `stdio` below), kept in a ring of at least `limits.outputRing`
bytes. A finished exec is kept at least an hour or the last 50 per sandbox.

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

### A program's streams on one socket (`stdio`)

For a program a consumer drives over its stdin and stdout — a coding agent
speaking ACP — without polling, and with its stderr apart. Where `caps`
lacks `stdio`, the routes above do the same (`split` is then a field the
manager doesn't know: it ignores it, and the exec answers `split` false).

- **`split: true`** on `POST …/execs` (not with `tty`: `invalid`) keeps
  stderr out of the exec's stream: `…/output` and `total` are its stdout,
  `…/output?stream=stderr` reads its stderr — offsets of its own, a ring of
  its own — and the exec gains `split: true` and `errTotal`.
  `stream=stderr` on an exec that isn't split is `invalid`; `stream=stdout`
  is the default.
- **`GET …/execs/{eid}/stdio?since=<n>&errSince=<n>`** is a WebSocket for
  a non-`tty` exec (a `tty` exec is `invalid`: its route is `…/tty`), from
  stdout offset `since` and stderr offset `errSince` (both default 0; past
  their stream's end is `invalid`). Offsets are `…/output`'s: a reader may
  switch between the two. Refusals come before the upgrade, as JSON
  (`not-found`, `invalid`, `lost`, `unsupported`).
- **Server → client**: `{"op":"hello","id":"<eid>","total":N,"errTotal":M,"state":"running","stdin":true,"split":true}`
  first; **binary frames** are stdout from `since`, the ring's bytes and
  then live ones, preceded by `{"op":"gap","stream":"stdout","from":<n>,"to":<ring start>}`
  where the ring dropped some; a split exec's stderr comes as
  `{"op":"stderr","off":<offset of its first byte>,"data":"<base64>"}` (its
  own `gap` with `"stream":"stderr"`); `{"op":"exit","code":0,"signal":"","total":N,"errTotal":M}`
  once the exec has ended and all its output is out (`code` null when a
  signal ended it), then a normal close (1000); `{"op":"pong","t":…}` for
  each ping (`t` echoed); `{"op":"error","refusal":"invalid","error":"…"}`
  for a client frame that couldn't be done — stdin to an exec without
  `stdin: true` or after `eof` (`invalid`), after its end (`state`) —
  and the socket goes on.
- **Client → server**: **binary frames** are stdin (each at most
  `limits.stdinMax`); `{"op":"eof"}` closes stdin; `{"op":"ping","t":…}`.
  A stdin frame is taken when the command reads it: the socket's own flow
  control holds the client meanwhile (no 30 s / `unavailable` as on
  `POST …/stdin`). The client's frames are handled in order, so a ping's
  pong says the command has taken every stdin frame sent before it — a
  client that must not lose input across a drop resends, on its next
  socket, what no pong acknowledged. Unknown ops are ignored both ways.
- **The socket attached last holds stdin**: attaching closes the one
  before it with code **4001** (`replaced`) — a consumer handing a program
  to another process (a restart, a new replica) just attaches. Frames of a
  socket that was replaced are dropped.
- A client that leaves doesn't end the command; attaching to an exec that
  has ended replays from the offsets and says `exit`.
- **Who dials it**: a consumer's backend, through xbind with its instance
  credential and its person in `Sbx-User`, as any backend call (in Go,
  `xbin.DialManagerStdio`, docs/sdk.md), or a page with its frame token.
  Who may is as on every route: a sandbox the consumer doesn't see is
  `not-found`; the person rules are the manager's on a verified call and
  the consumer's for an asserted person — except from a partitioned
  consumer's user partition, whose person is the partition's and verified:
  the manager applies the person rules itself, and an `Sbx-User` naming
  anyone else is `403 not-allowed` (§Partitioned consumers).
  Attaching is a **change**, not a read — the socket writes the exec's
  stdin and takes it from whoever held it — so a manager that lets some
  people only look (the reference manager's own page, for people with
  read access to it) refuses them the socket before the upgrade, as it
  does a terminal.

### Terminals (`tty`)

`GET /sbx/sandboxes/{id}/execs/{eid}/tty` attaches to a `tty` exec, and
`GET /sbx/sandboxes/{id}/tty?cwd=&cmd=&rows=&cols=` starts one (the login
shell unless `cmd`) and attaches — both are WebSocket upgrades speaking
exactly the terminal wire of `/ws/term` (docs/protocol.md §The terminal
wire), so `<bx-terminal src>` (docs/elements.md) works against it:

- **Binary frames** both ways: raw terminal bytes. The ring's tail replays
  first.
- **Server → client** JSON: `{"op":"session","id":"<exec id>","sandbox":"<id>","echoAck":false}`
  first; `{"op":"pong","t":…}` answering each ping (`t` echoed verbatim);
  `{"op":"exit","code":0}` once the command has ended and its output is
  out (`code` null when a signal ended it, with `"signal":"KILL"`), then a
  close; with `echoAck`, `{"op":"ack","n":N}` as `/ws/term` sends it.
- **Client → server** JSON: `{"op":"resize","cols":120,"rows":32}`,
  `{"op":"ping","t":…}`. Unknown ops are ignored on both ends. A resize
  reaches the terminal before the keystrokes sent after it.

The session's `id` is a `tty` exec's: it is listed under `execs`, its
output (`…/output`) is the terminal's stream, `…/resize` resizes it and
`…/execs/{id}/tty` attaches to it again. Label that exec `terminal`: a
consumer that offers running terminals to attach (the `sandbox-terminal`
tile) looks for tty execs labelled `terminal`, or not labelled. A client
that leaves doesn't end the command; attaching to one that has ended
replays its ring, then says `exit`. A request that isn't a WebSocket upgrade is `invalid`, and refusals
come before the upgrade, as JSON like any other route's.

**Who opens one.** These routes, and tty execs (`POST …/execs {tty:
true}`), serve a consumer's pages and its backend alike:

- **A page** connects with its frame token (`xbin.ws(url)`, or
  `<bx-terminal src="<url>/sbx/sandboxes/{id}/tty?cwd=…">`, which does it
  for you and reattaches to the same exec after a drop), so the manager
  sees the **verified** person and applies its person rules.
- **A consumer's backend** dials them through xbind with its instance
  credential — the binding's `consumer` role — to drive a terminal itself
  (an SSH bridge, a sign-in it runs) or to relay one to its own page or app.
  It names the person it acts for in `Sbx-User`, as on any backend call:
  **asserted**, recorded, not verified (§Who is asking). The manager
  answers it as any backend call — consumers stay apart (the consumer's own
  sandboxes and those shared with it), the person rules are the
  consumer's — and a manager that asks its substrate about the person
  (xbind's `noTerminal`, through `forUser`) asks about that one. **From a
  partitioned consumer's user partition** the person is the partition's,
  and verified (§Partitioned consumers): the manager applies the person
  rules itself, as on a page's call, and an `Sbx-User` naming anyone else
  is `403 not-allowed`.
- **A consumer that relays a terminal to a person checks that person first**
  — may they use this sandbox, by the rules of §Consumers, sharing and
  people as it applies them, and may they have a terminal at all: the
  manager can't (from a user partition it applies the first itself; the
  second stays the consumer's), and the relay carries whatever they type.
  It relays every message both ways unchanged (the session and exit
  frames, resizes and pings included), dials anew for each client — no
  header, cookie or query of the person's request passes — and closes each
  end the way the other ended. In Go, `xbin.RelayManagerTTY` does exactly
  this, and `xbin.DialManagerTTY` dials for a backend that drives the
  terminal itself (docs/sdk.md).

The xbin app's `terminal` primitive dials only a tile's own routes, so an
app view reaches a manager's terminal through its tile's relay.

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

## Ports (`ports`)

`ANY /sbx/sandboxes/{id}/ports/{port}/{path…}` is an HTTP reverse proxy to
a server listening on TCP `{port}` (1–65535) on the sandbox's **own
loopback** — `127.0.0.1`, else `::1` — so a consumer can show its people a
page a program in the sandbox serves (`python3 -m http.server 8000`), live.
Added to protocol 1 by D135, as an optional capability (the contract grows
by addition: no new protocol number).

- **Any method**, the body streamed, and a **WebSocket upgrade** tunnelled
  (a dev server's live reload). `{path…}` and the query reach the server as
  the consumer sent them, still escaped — the server's path is `/{path…}` —
  and nothing is rewritten: it is a path-prefix proxy, so a page's
  **relative** URLs work below whatever prefix the consumer serves it at,
  and root-absolute ones (`/app.js`) don't. `Host` is `localhost:{port}`.
  A path segment that decodes to `.` or `..` is `invalid`. A manager
  behind xbind gets the port's **root** as `…/ports/{port}`, with no
  trailing slash — xbind hands a tile backend its path without one — and
  serves it as `/`, never as a redirect (the `Location` would name the
  manager's own path, which the consumer can't reach).
- **Who**: the same scoping and person rules as `exec` — a consumer reaches
  only the sandboxes it sees (`not-found` otherwise), and on a verified call
  the person must be allowed to use the sandbox (`not-allowed`).
- **Nothing of xbin crosses.** The consumer's `Authorization`, `Cookie`,
  `Sbx-User`, `X-XBin-*` and `Forwarded`/`X-Forwarded-*` headers never
  reach the server, and its `Set-Cookie` and `X-XBin-*` never come back.
  It is the one path **into** a sandbox's network, from the consumer: it
  gives the sandbox no route out — to xbind, the workspace or anything else
  (§Inside a sandbox).
- The sandbox must be **running**: a port route never starts one (its
  server would be gone anyway) — `state` otherwise. Nothing accepting on
  the port within a few seconds is **502 `not-listening`**; the server's
  own answers (a 404 of its own included) come back as they are.
- A sandbox a manager offers ports for can still answer **501
  `unsupported`** when what runs it predates them (on xbind: a sandbox
  started under an older xbind, or from a VM image from before D135 — "the
  sandbox's agent doesn't serve ports (it predates them): restart the
  sandbox"). A restart fixes it; a manager may say so as `restartNeeded`
  on the sandbox until it runs again (the reference manager does).
- A consumer serving the page to people's browsers answers it under its
  own policy: the agent template serves it in an opaque-origin sandboxed
  frame with a CSP of its own (API.md §Live previews).

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
  `SANDBOX_ID` and `SANDBOX_NAME`. A `tty` command also gets a terminal's
  `TERM=xterm-256color`, `COLORTERM=truecolor` and `LANG=C.UTF-8`, each
  unless its `env` (or the manager's own defaults) names it — full-screen
  programs and the coding agents' sign-in screens need them.
- An image's `harnesses` (hello, above) run as the sandbox's `user`, like any
  command; what they reach is the sandbox's `egress`, so an agent that
  calls its provider's API needs `internet` or `open`.
- **No xbin identity, ever**: no token, no gateway, no route to xbind or the
  workspace's tiles. What a sandbox reaches is its `egress`, nothing more.
  The `ports` proxy is inbound only — a consumer's request to a server in
  the sandbox — and carries no credential in.

## People's terminals: the `sandbox-terminal` tile

The builtin **`sandbox-terminal`** tile (`bx tile import sandbox-terminal`,
D121) is a consumer that gives people terminals onto sandboxes and creates
none. Bind it to managers (`bx bind apps/sandbox-terminal
sandboxes=apps/<manager>`, `--add` for more); a sandbox shows up there once
it is **shared** with it — `{"shares": [{"consumer": "apps/sandbox-terminal",
"users": "*"}]}` by its home consumer (the agent template's **Share with a
terminal tile…** on a sandbox's row: for its owner, or `"*"` for a team
sandbox), or the manager's operators. The person rules above decide who
opens which:

- **In the browser** its page dials your `tty` route with its frame token,
  so you see the **verified** person. It lists a sandbox's execs (`GET
  …/execs`, as that person) to offer the running terminals — tty execs
  labelled `terminal` or not labelled (§Terminals) — for attaching again
  (`…/execs/{id}/tty`) and ending (`DELETE …/execs/{id}`).
- **Over SSH** (`ssh <sandbox>@host -p 2222`, after an admin runs `bx expose
  apps/sandbox-terminal ssh=runtime --listen :2222` — or `--listen
  127.0.0.1:2222` to keep it on the host's loopback, for people who come
  through an SSH tunnel or a VPN) a key registered on its
  page names the person — only while xbind says they may still use the
  tile, asked at every login — and its backend calls you as an
  **asserted** one (`Sbx-User`): `GET /sbx/sandboxes` to find the sandbox,
  then your `tty` route for a session with a terminal, or a background exec
  with `stdin` for one without (`ssh host cmd`: stdout and stderr arrive
  together). A client that leaves gets its command a `HUP`, then a
  `DELETE` if it still runs — one that leaves while you start the command
  too: the tile still waits for your answer (the exec; for a terminal, the
  `tty` route's upgrade and its `session` frame — up to two minutes) to
  learn which command to end.
  The SSH user name is the sandbox's name in lower case (runs of other
  characters `-`), `<name>~<n>` when several share it, or its id.
  No port or agent forwarding, no X11, no sftp in v1.

What it offers people and its page, route by route, is its `API.md`
(`apps/sandbox-terminal/API.md` once imported).
## The builtin manager

`bx template new coding-sandbox as apps/coding-sandbox`, a workspace admin
approves its `cap:sandboxes`, and consumers bind it
(`bx bind apps/agent sandboxes+=apps/coding-sandbox`). Each copy is a
manager of its own (its `API.md` has everything):

- **Sandboxes on xbind's own runtime** (docs/protocol.md §Tile sandboxes):
  a VM where the workspace runs VMs for tiles, else a namespace — or only
  the one mode its operators choose, never falling back (`isolation` always
  says which). `caps` are what the runtime serves (`exec`, `files`, `tar`,
  `tty`, `snapshots`, `clone`, and `ports` and `stdio` on an xbind that has
  them; not `archive`), `hello.notes` say what it
  lacks.
- **Images** are the runtime's base plus a setup script, built once as root
  and cloned (a rebuild that fails keeps the previous good build);
  **sizes**, per-consumer and per-person **quotas** (`hello.limits` carry
  the effective ones), the layout (a `dev` user in `/work`), the idle stop
  and mounts of the tile's own filesystem resources are its operators'.
- **Networks**: `none`, then `internet` and `open` while the copy's
  `sandbox-net` classes of those names are bound.
- **Its page**: for its operators (write access to the tile) every
  consumer's sandboxes — metadata, never contents — with their lifecycle and
  snapshots, usage against the quotas, the images and the settings, and
  their own sandboxes with a file browser and a terminal. Operators never
  change who may use another consumer's sandbox; only its home consumer
  does. A sandbox homed in a partitioned consumer's user partition shows
  to them as `<consumer>/<partition id, first 8> #<n>`, without its labels
  and with its snapshots named `snapshot #<n>` — a model may have named
  them — unless it is shared with them. People with read access get a
  read-only view of the sandboxes they may use: a change from the page
  needs write access to the tile. The app draws the same natively.
- **Partitioned consumers** (`partitions`, §Partitioned consumers): the
  runtime is told the consumer tile and the person as ever, plus the
  partition id as the runtime label `coding-sandbox/partition` (kept with
  the sandbox's definition; the admin's sandbox registry doesn't show
  labels); its quotas per consumer count every partition of the tile.
  Whoever may change the manager's code (its writers, admins) could reach
  every sandbox it holds, and its operators (the same writers) set the
  images' `harnesses` commands a person's sign-in and coding agents run
  (§hello): they are in the trust base of every person whose partition
  uses it.
- **Other substrates**: a copy adds a backend (a cloud's API and ssh) in one
  Go file; its `AGENTS.md` says how, and how to run the conformance suite
  against it.

## On xbin

A manager that runs its sandboxes on xbind's own runtime
(docs/protocol.md §Tile sandboxes; the Go SDK's `xbin.SandboxAPI()`,
docs/sdk.md; the xbin repository's `examples/sandbox-go` is the smallest
one) hands them to the workspace too, which may stop one under it —
synced, state kept, the reason in its `stateDetail`. Show that reason to
the consumer, and start the sandbox again (or let `autoStart` do it) once
the cause is gone:

- **xbind itself.** Running work dies with xbind: after a restart every
  sandbox is `stopped`, state kept, and an exec id from before it answers
  `lost` (410) — the contract's `lost`, which a manager whose exec ids are
  the runtime's passes on as it is. Its execs' output goes with them.
- **Idle.** A sandbox with no activity for its `idleStopMin` (the
  runtime's policy, 30 minutes by default) is stopped; a command or file
  call starts it again with `autoStart`. Reading its `SandboxInfo` isn't
  activity; a running non-terminal command, a file or tar call and an
  attached terminal hold it off however long they take.
- **Its tile.** Disabling, hiding, offloading or removing the manager tile
  stops its sandboxes, and so does losing `cap:sandboxes`. A removed
  manager's sandboxes stay, definitions and state, until a workspace admin
  deletes them.
- **Its grants.** A mount the tile no longer holds, or a read-write one it
  now holds only as a reader, stops the sandbox; so does sealing the vault,
  for every sandbox with a resource mounted (its start answers 503 until
  the vault is unsealed). A narrowed `sandbox-net` class does the same.
- **Disk.** Each sandbox's bytes are measured (`diskBytes`, after each
  stop and every few minutes while it runs). Past the tile's
  `perTile.diskGiB`, its largest running namespace sandbox is stopped and
  starts answer 429; while the workspace disk is low, starts answer 503 and
  the namespace sandboxes of the tiles holding most are stopped first (a
  VM's disk is bounded: it runs on).
- **Backups.** A backup of the manager tile carries its sandbox
  definitions, never their state; a restore brings them back by `uid`,
  stopped. State moves only through snapshots and clones — so offloading a
  manager whose sandboxes hold state is refused.
- **Snapshots and clones.** xbind copies a sandbox's state off the request
  — an upper exactly, a VM disk sparse — and a snapshot keeps the base
  image it was built on installed. A copy still running after
  `waitMaxSec` answers as it stands (a snapshot `pending`, a restore
  `busy: …`, a clone `creating`), and meanwhile the sandbox answers
  `state`: wait it out before you answer your consumer, as the builtin
  manager does. A clone of a running sandbox needs a snapshot (the
  builtin manager takes one for it, and deletes it once the clone is made).

## Building a manager

**On a cloud.** Every operation maps onto an instance API and ssh:

| Contract | On a cloud |
|---|---|
| create / start / stop / delete, `?wait` | the provider's instance API, polled |
| `run` | `ssh host -- 'cd -- <cwd> && exec setsid $SHELL -lc <cmd>'`; a timeout sends `kill -TERM -<pgid>` over a second connection |
| execs | `setsid nohup <cmd> > /var/lib/sbx/<eid>/out 2>&1; echo $? > /var/lib/sbx/<eid>/exit`; output is `tail -c +<offset+1>`; signals are `kill -<SIG> -<pgid>` |
| `split`, `stdio` | `2>` a second log beside the exec's; the socket is one ssh session per attach: `tail -c +<off+1> -f` of each log as frames, stdin into a fifo the exec reads (held open by the manager, so `eof` closes it); or leave `stdio` out — consumers read and write through `…/output` and `…/stdin` |
| `tty` | `ssh -t` with the terminal's environment (`TERM`, `COLORTERM`, `LANG`; §Inside a sandbox) into a holder on the instance (`dtach`, `tmux`) that keeps the command and its pty past the connection, its output logged as the exec's (`…/output`, replayed on an attach, which is another `ssh -t`); window-change on resize. The same for a page's terminal and a consumer backend's (`Sbx-User`) |
| files | sftp: stat, ranged reads, write to a temporary name and rename, readdir; `etag` a sha256 |
| `tar` | `tar -C <dir> -cf -` / `-xf -` over ssh |
| snapshots, clones, archives | disk snapshots and images; archive = snapshot + terminate |
| `egress: none` | a security group denying all egress (ssh from the manager only) |
| partitions, people | the manager's own table |

**Check it against the contract.** The conformance suite is a Go package,
`github.com/xbin-dev/xbin/sdk/sandboxcontract` (the standard library and
`sdk/ws`). Serve your manager's handler in a test — or aim at one running —
and run it:

```go
func TestContract(t *testing.T) {
	srv := httptest.NewServer(newManager(t.TempDir()))
	defer srv.Close()
	sandboxcontract.Run(t, sandboxcontract.Target{
		URL:   srv.URL,                                  // its routes are URL + "/sbx/…"
		Caps:  []string{"exec", "files", "tar", "tty"}, // what hello must offer
		Grace: 5 * time.Second,                          // its TERM → KILL grace
	})
}
```

Every section of this page is a group of parallel subtests (`go test -run
'TestContract/tty'` picks one). The checks act as consumers of their own
(`apps/ct-<section>-<check>-a`, …), setting `X-XBin-From`, `X-XBin-User`,
`X-XBin-Partition`, `X-XBin-Partition-Id` and `Sbx-User` as xbind and a
consumer would, and delete the sandboxes they make — `stdio` drives
programs over the stdio socket (replay from an offset, gaps, split stderr,
exit, `eof`, the newest attach winning); `tty/backend` opens terminals as
a consumer's backend does, for an asserted person who is neither the
sandbox's owner nor a member, and on a sandbox shared with it (a manager
that refuses them is warned — the check skips, saying why — in the
release that added it, 2026-09-30, and fails it from the next:
docs/changes/2026-09-30-manager-terminals-for-backends.md). A section
whose optional capability hello leaves out is skipped; its routes must
answer `unsupported` (`caps/missing`) — `stdio`'s may also answer `404`
(`not-found`, or its router's plain 404), as a manager from before it
does for a route it doesn't know. The `partitions` section is about
consumers — one consumer's sandboxes apart from another's (§Consumers,
sharing and people); the `user-partitions` section is about a partitioned
consumer's people (§Partitioned consumers) and runs when hello's caps
carry `partitions` (a capability with no routes of its own) — its
`sockets` check dials the `tty` and `stdio` routes, where hello offers
them, as another partition, the same person's partition of another
consumer (the same partition id), the consumer's global instance, a
person they assert, and the partition naming someone else — and names a
partition's exec under a sandbox the caller does see. The rest of
`Target`:

- `Client` — the HTTP client for every call and terminal (TLS, a proxy).
- `Consumer`, `Verified`, `Asserted`, `Partition` — how to call as a
  consumer, a verified person, an asserted one and a partitioned
  consumer's partition, when the headers aren't how your manager hears it.
- `Create` — fields for every sandbox the suite creates (a small image or
  size); `Setup` — run first in every check.
- `Fresh` — a manager of its own for a check that wants `Knobs`: a small
  output ring, a small `fileMax`, fewer capabilities (so the refusals of a
  missing one are checked). Without it those checks use your manager with
  the limits its hello states.
- `Skip` — checks you know it fails (`"execs/stdin"`, or a whole section),
  each with why: they show as skipped, never silently.
- `Strict` — fail what the suite still only warns about (a check a manager
  built to the earlier suite may not pass yet skips with a warning for one
  release); the reference managers set it.

`Target.As(t, consumer)` is the suite's client (calls, refusals, runs,
execs, files, terminals) for your own tests of what the contract leaves to
you; `.Verified(user)`, `.Asserting(user)`, `.InPartition(user, id)` and
`.Global()` say who calls.

**The reference manager** is `hack/fakesandbox` in the xbin repository: the
whole contract in one Go file — the standard library and `sdk/ws`; each
sandbox a directory on the host, each terminal a host pseudo-terminal, for
tests only. Its tests run the suite (`hack/fakesandbox/fsb_test.go`).

**Versions.** Protocol 1 grows only by addition: new optional fields,
routes and capabilities. Consumers ignore fields they don't know; managers
ignore unknown request fields. A change that isn't additive is protocol 2,
and `hello` negotiates it.
