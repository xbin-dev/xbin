# Protocol: the terminal WebSocket

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page:
`/ws/term` and the terminal wire. The session directory
(`/term/sessions`) is in [agents.md](/docs/protocol/agents.md).

## `/ws/term` — terminals (admins + users with a terminal-level tile)

```
GET    /ws/term?cwd=<p>|session=<id>   WebSocket upgrade → a terminal session (below)
                                   &deployment=<name>: a new session's
                                   target (ignored on reattach); the session
                                   control frame echoes deployment (below)
DELETE /ws/term?session=<id>       end a session now (creator or admin) → 204
DELETE /ws/term/env?cwd=<p>        terminal level on the tile: wipe its persistent
                                   terminal layer back to the base rootfs → 204
                                   (on a partitioned tile: the caller's own)
GET    /ws/term/env?cwd=<p>        that layer's state → {exists, baseOutdated,
                                   baseAutoUpdate,
                                   vm:{available,reason,memMiB,vcpus}}
```

Connect with `?cwd=<component-path>` (new session) or `?session=<id>`
(reattach; scrollback replays first). A session may only be opened on a tile
where the caller's access level is **terminal** (docs/auth.md), mounts its
creator's `$HOME`, and carries a per-session `XBIN_TOKEN` scoped to that tile
(docs/overview/09-terminals.md). Reattaching re-checks that level: a creator
whose terminal level on the tile was withdrawn since gets 403 ("revoked")
until the session ends. Under `--isolate` the workspace mounts read-only
(all tiles' source — for a non-admin, minus tiles below their read level,
which are masked out) with `.xbin/`, `data/`, and other users' `homes/`
**masked out** (docs/isolation.md), so the terminal can't read the owner
token or resource state. **The root terminal (no cwd) is disabled** — 403 for
everyone. Reattach/kill of another user's session: admins only. Which
sessions are yours on a tile: `GET /api/xbin/term/sessions?cwd=` (the
session directory, D73) — a browser keeps no session ids of its own. An
**agent session** (`kind:"agent"`, created on `POST /api/xbin/term/sessions`)
has no terminal socket: `?session=<agent id>` answers **409** — drive it
through the agent routes (protocol/agents.md).

For a **non-admin**, the query params below are clamped rather than honored
(docs/isolation.md): `api` is forced to `0` without the `termApi` grant; on a
personal/workspace tile `net` is forced to `none` without `termNet` and
`net=host` is admin-only; on an **org-owned tile with network sets** (D54)
the org network is the members' grant — `internet` and a refused `host`
clamp to `org`, and `host` is allowed when a set says so; a `set:<name>` the
caller may not pick there clamps to the tile's default. The session still
opens; the `session` control frame reports the effective scope, the scopes
the caller may pick, and why a request was clamped (`netNote`). New-session
query params (all optional):

- `?api=0` — mint **no** terminal token: the shell sees code but every tile/xbin
  API call is unauthorized (default `1`).
- `?purpose=signin` — a **guided sign-in's shell** (D178: the Agent tab runs
  a CLI's login in it): the session directory lists it with
  `purpose:"signin"` and the name `xbin:sign-in`, clients make no tab of it,
  no rename touches it, and xbind ends it 15 minutes after it opened. Any
  other `purpose` (on a new session or a reattach) is 400; only xbind sets a
  row's purpose.
- `?net=<scope>` (absent = the tile's default: `org` where it exists, else
  `internet` where allowed, else `none` — `GET /term-net?tile=` lists them):

- `org` — own network namespace with an egress relay enforcing the owning
  org's **network sets** (the union of their rules — LAN ranges, named
  internet destinations with DNS pinning, all internet); when a set says
  `host` this scope is host networking. Org-owned tiles only.
- `set:<name>` — one named network set (D65): the relay under exactly its
  rules, host networking when it says `host`. Each set attached to the
  tile's org, for whoever may open a terminal there; any workspace set for
  a workspace admin, on any tile. An unknown or unpickable name clamps to
  the tile's default (`netNote` says so). Never the default.
- `internet` — own network namespace with an **internet-only egress relay**
  (`net:internet`: public addresses only, no host interfaces visible). TCP, UDP,
  and ICMP echo (`ping`) are forwarded under the policy; `traceroute` needs the
  `host` scope. xbind stays reachable at `$XBIN_URL` via the relay's gateway
  host-forward, so `bx`/`curl` work.
- `host` — share the host network (LAN + host services reachable, host
  interfaces visible). The escape hatch.
- `none` — an isolated namespace with no egress (xbind unreachable).

A new session also takes `?deployment=<name>`, its **target**: the tile
deployment ([tile-deployments.md](/docs/tile-deployments.md)) its
`XBIN_TOKEN`'s self-calls and self-scoped calls reach. It is fixed for the
session's life (changing it restarts the session) and ignored on reattach.
The primary's name means "follow the primary"; a protected primary answers 403
`the primary of <tile> is protected: terminal and agent sessions can't target
it`, an unknown name 404, a malformed one 400. Without it the session follows
the primary — unless the primary is protected: then it targets the live
reload target, and when there is none (live reload paused) it opens with no
tile API. A session following the primary follows a reassignment without a
restart; protecting the primary restarts it onto that default (a shell is
ended with a notice), and a reassignment restarts the sessions named after
either deployment. A removed target makes the session's calls answer 404;
the session isn't killed. `XBIN_DEPLOYMENT` is set in a session whose target
is a named deployment other than the primary at start, and absent otherwise.
`api=0` (and a user without the `termApi` grant) means no target at all.

A new session also takes `?gpu=<none|all|index|uuid>` (default `none`) to bind
host NVIDIA GPU(s) into the terminal's dev sandbox (owner plane; no grant
needed). Enumerate host GPUs at `GET /api/xbin/gpus` (admin).

`?vm=1` opens the session in a **VM sandbox** (D89,
[isolation.md](/docs/isolation.md) §VM sandboxes): a Firecracker microVM with its
own kernel, where the shell is root, running inside the same namespace
sandbox as the jail. The same mounts appear at the
same paths (served from outside the VM), the same network scope applies (the relay
enforces it outside the VM), and `$XBIN_URL`/`XBIN_TOKEN` work unchanged.
It needs `--isolate`, KVM (or the shipped emulation — much slower; `GET
/ws/term/env` then reports `vm.emulated` and `vm.note`, D90), and an admin
who turned VM terminals on (`PUT /vm/policy`); otherwise the upgrade fails
with 400 and the reason (`GET /ws/term/env` reports `vm.available` and
`vm.reason` beforehand). `net=host`
and `gpu` can't combine with `vm=1` (400). A VM terminal's root filesystem
changes are kept on the tile's VM disk (in its terminal layer — the same
lock and Reset as the namespace layer, a separate filesystem).

The scope is fixed at spawn; switching net, GPU, VM or the target restarts the
session (the UI ends the old one and opens a new WS).

The frames are [the terminal wire](#the-terminal-wire) (below), with
`/ws/term`'s own fields on its `session` frame:

- **Binary frames** both directions: raw PTY bytes.
- **Text frames**: JSON control.
  - server → client: `{"op":"session","id":"…","net":"org","label":"org network
    (devs-net)","scopes":[{"id":"org","label":"…","desc":"…"},…],"netNote":"…",
    "baseOutdated":false,"vm":false}` (first message — on every socket,
    including one attached to a session that has already ended, where the
    scrollback and the exit follow it; `vm` = a VM sandbox; `scopes` = what this caller may
    pick on this tile, `label` names the effective scope, `netNote` explains a
    clamp; `baseOutdated:true` ⇒ this terminal's persistent layer was built on
    an older base image — reset it via `/ws/term/env` to rebuild on the
    current base (with the workspace's base auto-update on, the session's
    next start does that by itself, D175); `echoAck:true` ⇒ this xbind sends the `ack` and `pong`
    frames below; `deployment` = the named target, or the current primary's
    name for a session that asked for the primary by name — the echo a client
    checks, since an older xbind ignores `?deployment=`; `targetNote` = a line
    to print like `netNote`: `this terminal calls <tile>+<name>`, or `tile API
    off: <primary> is protected and live reload is paused`; `api:false` ⇒ the
    session asked for or was given a target but has no tile API (the
    protected-primary fallback, or a user without the `termApi` grant). All
    three are absent for a session that sent no `deployment` and follows the
    primary. On a partitioned tile only, the frame adds `partition`
    (`user:<id>` | `global`: what the session acts in), `partitionNote` (a
    line to print like `netNote`: `partition: yours (user:<id>) · the tile
    directory is shared code — keep your own files in $HOME`; `partition:
    user:<id> · no isolation: this shell runs on the host and can read every
    partition's data` on an xbind without `--isolate`; or `tile API off:
    <why>`) and `api:false` when the session has no person and the tile
    no global instance; a tile sandbox's TTY, which speaks this wire too, adds
    `sandbox` — the sandbox's id — so ignore fields you don't know),
    `{"op":"exit"}` — the process ended (here, the shell), and the socket
    closes after it. It may carry `"code":N`, or `"code":null` with
    `"signal":"KILL"` when a signal ended the process (a tile TTY does; a
    shell's exit here is bare). It is sent **only** when the process ended: a
    socket that falls too far behind the output is closed **without** one —
    the session lives on, so reattach (`?session=`) and the scrollback
    replays. `{"op":"ack","n":N}` —
    the client's Nth **binary** frame on this socket reached the PTY at least
    50 ms ago, so whatever the application printed in answer precedes this
    frame (one ack covers every earlier frame; mosh's echo ack, the basis of
    the terminal's predictive echo, D70), `{"op":"pong","t":…}` — answers a
    ping, `t` echoed verbatim.
  - client → server: `{"op":"resize","cols":120,"rows":32}`,
    `{"op":"ping","t":<any JSON>}` (the browser measures its round trip; a
    WebSocket-level ping is answered below JavaScript). Unknown ops are
    ignored on both ends.

`DELETE /ws/term?session=<id>` ends a session immediately (creator or admin;
used by the UI to restart under a new scope); `204` on success, `404` unknown.

**On a partitioned tile** ([partitions.md](/docs/partitions.md) §Terminals and
agent sessions) a new session belongs to its opener's partition: its
`XBIN_TOKEN` acts there (protocol.md §Authentication), `XBIN_PARTITION=user:<id>` is set
right after `XBIN_COMPONENT` (after `XBIN_DEPLOYMENT` when that is set;
`global` for a session targeting a non-primary deployment), it starts in its
`$HOME` instead of the tile directory, and its persistent layer is the
person's own (`.xbin/term-part/<tile-key>/<partition-id>/`, never backed up;
`/ws/term/env` resets and reports that one). A session without a person (the
owner token) acts in the global instance, or opens with no tile API when the
tile has none (the session frame's `api:false`). An admin reattaching to
another person's session there gets 403 `session belongs to another user:
<tile> keeps each person's data apart, so an admin can't open other
people's sessions there (ending one is allowed)`; ending it stays allowed. A
session asked for while the tile's partition mode switches answers 409 (as
does a person's while their partition of the tile is being reset or
removed); a switch that deletes the tile's data ends its sessions first —
those opened on a sub-path of the tile too — and deletes the person layers,
and a tile whose recorded mode gains or loses user partitions ends the
sessions opened under the other mode. Every other tile's sessions are
unchanged.

`DELETE /ws/term/env?cwd=<component-path>` (terminal level on that tile; the
root layer — `cwd` empty — admin-only) wipes that component's
**persistent terminal layer** (installed packages / system changes) back to the
base rootfs, killing any live session on it first; `204` on success, `500`
with the layer untouched when a session doesn't end within 5 s. Each
component's terminal has its own persistent overlay layer (`.xbin/term/<key>/`)
so system-level changes survive across sessions — a resettable dev sandbox
(`docs/isolation.md` §The dev layer). Workspace files and `$HOME` persist independently.
`GET /ws/term/env?cwd=<component-path>` (same gate) reports that layer
without opening a terminal: `{"exists":bool,"baseOutdated":bool,"baseAutoUpdate":bool}` —
`baseOutdated` as on the session frame, so the terminal window offers the
base update on its session chooser too; `baseAutoUpdate` is the
workspace's setting (`GET /api/xbin/workspace-settings`, D175): while it is
on, the next session that opens the layer — no other session holding it —
moves it to the current base first (its upper and VM disk put aside and
removed as the reset removes them: everything outside the workspace files
and `$HOME`), and the shell's first output is one grey line saying so
(`xbin: this tile's terminal moved to the new base image — everything
outside the workspace files and $HOME was reset (…)`); an agent session
logs a `notice` event instead, and the tile's next shell prints `xbin: an
agent session's start moved this tile's terminal to the new base image —
…` once; a person's own layer on a partitioned tile is named `your
terminal on <tile>` in both lines and the notice. A running session is
never moved. A layer whose base isn't
installed any more moves the same way; with the setting off, such a layer
fails to open (reset it). Neither keeps xbind from booting any more (it
used to refuse to start over such a layer, D175). A layer stamp or base
version that can't be read fails the open, the layer untouched.

Sessions survive disconnects; idle unattached sessions are reaped after 24 h;
xbind restart kills them (run `tmux` inside if you care).

## The terminal wire

`/ws/term`'s framing is the one terminal protocol in xbin, and other
endpoints speak it too: a sandbox manager's `tty` routes
([sandbox-manager.md](/docs/sandbox-manager.md) §Terminals), a tile's own pty
route for the xbin app's `terminal` ([native.md](/docs/native.md) §Escape
hatches), and whatever `<bx-terminal src>` is pointed at
([elements.md](/docs/elements.md) §`<bx-terminal>`). Speak exactly this, and those
clients work against your endpoint:

- **A WebSocket.** Refusals come before the upgrade, as plain HTTP (a
  browser page never sees them: the socket just closes, so a client checks
  what it can beforehand).
- **Binary frames**, both ways: the terminal's bytes. Keystrokes arrive as
  typed (Enter is `\r`); output is sent as it comes — on (re)attach the
  endpoint may first replay what it kept (the scrollback, a ring).
- **Text frames** are JSON control, `{"op": …}`. Unknown ops, and unknown
  fields, are ignored on both ends.
  - server → client, first: `{"op":"session","id":"<session id>",
    "echoAck":false}` — `id` names what a client reattaches to (`/ws/term`
    `?session=`; a manager `…/execs/{id}/tty`); an endpoint adds its own
    fields (`/ws/term`: the scope; a manager: `sandbox`).
  - `{"op":"pong","t":…}` answers every ping, `t` echoed verbatim.
  - `{"op":"exit","code":0}` once the command has ended and its output is
    out (`code` null and `"signal":"KILL"` when a signal ended it; bare
    `{"op":"exit"}` is fine), then a close with 1000. A clean close (1000,
    or none) with no exit frame also means it ended.
  - `{"op":"ack","n":N}` only with `echoAck:true` in the session frame: the
    client's Nth binary frame on this socket reached the terminal at least
    50 ms ago, so the output it caused precedes this frame (the client's
    predictive echo, D70). Without `echoAck` the client doesn't predict.
  - client → server: `{"op":"resize","cols":120,"rows":32}` — first on every
    connect, and whenever the client's grid changes; it takes effect before
    the keystrokes sent after it. `{"op":"ping","t":<any JSON>}` — the
    client measures its round trip (a WebSocket-level ping is answered below
    JavaScript).
- **Leaving.** A client that disconnects doesn't end the command; ending it
  is the endpoint's own route (`DELETE /ws/term?session=`, a manager's
  `DELETE …/execs/{id}`). Any close other than a clean one (a drop, 1001,
  an error code) is a client's cue to reconnect with backoff.
