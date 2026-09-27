# Sandbox managers, agent classes, and agents in coding sandboxes

> Status: **live** (D115, D116, D120) — phase 1 is built (merged in
> 9b1bb95); phase 2's implementation plan is
> [tile-sandbox-runtime.md](tile-sandbox-runtime.md) (D120); phase 3 is
> designed below.

## Why

Agents in the builtin agent tile should work in **coding sandboxes**: run
commands and edit files at a working directory, in a box that has the tools
of the job and none of the workspace's secrets. The owner's direction
(2026-09-27): the sandboxes must **not** be built into the agent tile.
Instead:

1. **A service contract for sandbox managers** — `sandbox-manager`,
   documented in `docs/sandbox-manager.md` (protocol 1).
2. **Managers** implement it: the builtin **`coding-sandbox`** template
   (VM-based sandboxes by default, on xbind's tile-sandbox runtime —
   plans/tile-sandboxes.md, D113 as revised here), and anyone's own tile —
   one that delegates to a cloud's API and ssh, for example.
3. **Consumers** multi-bind managers: the **agent tile** (a bound
   conversation gets a coding toolset) and a separate **`sandbox-terminal`**
   tile (people get terminals onto sandboxes, in the browser and over SSH).
   The legacy builtin `devbox` tile, which never worked, is retired.

```
 agent tile ──(slot sandboxes, multi)──┐
 sandbox-terminal ──(slot, multi)──────┼──► sandbox-manager ──► coding-sandbox (builtin template)
 any consumer tile ────────────────────┘   contract            ├─ xbin backend → /api/xbin/sandboxes (cap:sandboxes)
                                                               └─ a copy's backend: cloud API + ssh, …
                                     any tile implementing the contract is a manager too
```

The split: **a manager knows the substrate** (VMs, containers, a cloud's
API, ssh, disks, quotas); **a consumer knows who and why** (people,
conversations, the agent's classes). The interface system carries it with no
daemon change: a multi `http` slot, `XBIN_IFACE_<SLOT>`, the xbind-set
`X-XBin-From`, WebSocket upgrades through the proxy.

## Owner decisions (2026-09-27)

- **One sandbox per agent conversation at a time**; tools run at a cwd.
  Subagents and workflows share the root's; a subagent may be spawned onto
  another sandbox the conversation has attached (a workflow can span
  sandboxes). Many conversations may bind one sandbox. Rebinding is allowed
  any time and applies from the next turn; a sandbox can be picked when a
  conversation starts or later.
- **Creating**: people create (the agent UI, the manager's own UI); the
  agent's `sandbox_create` parks for the conversation owner's approval
  (D111's grant card, once or for an hour).
- **Order**: the contract and the agent first (tested against a scripted
  manager), then xbind's runtime, then the tiles.
- **Human access** is its own tile, `sandbox-terminal`, multi-binding the
  managers: browser terminals and SSH ingress. **Drop devbox.**
- **Agent classes replace the lane.** Private (internal-reach) conversations
  generally don't get a sandbox. The agent tile's managers define classes,
  each a set of toolsets; tiles with very sensitive data aren't bound to the
  internet in the first place, but sets are good UX.
- **Per-consumer partitions**: a manager shows each consumer tile only the
  sandboxes it created plus those explicitly shared with it. Team-shared
  conversations should have shared sandboxes.

## The contract (D115)

`docs/sandbox-manager.md` is normative; in short:

- **Wiring.** A manager provides `{kind:"http", service:"sandbox-manager",
  role:"consumer"}` (a custom role, the D86 `channel` pattern — its own admin
  routes stay out of reach); a consumer requests `{kind:"http",
  service:"sandbox-manager", multi:true}`. Routes live under `<url>/sbx/`;
  `GET /sbx/hello?protocol=1` negotiates the version and names the
  capabilities (`exec` and `files` required; `tar`, `tty`, `snapshots`,
  `clone`, `archive` optional), images, sizes, egress kinds and limits.
- **Who is asking.** `X-XBin-From` (set by xbind) is the consumer tile.
  `X-XBin-User` is a person verified by xbind — only on calls a tile's page
  makes. A consumer's backend names the person it acts for in `Sbx-User`:
  asserted, recorded as such. Everything else is in the body or the query
  (a sandboxed page can't set custom headers).
- **Partitions.** A sandbox's home is the consumer that created it; it may
  be shared with other consumer tiles (`shares`, by its home consumer or the
  manager's operators). A consumer lists its own and those shared with it.
  Per person: an owner, `private|team` visibility and members; the manager
  enforces them on verified calls, and a consumer enforces them for the
  people it acts for on its own calls.
- **Operations.** Sandboxes CRUD and `start|stop|archive|thaw` (all with
  `?wait`); blocking `run` (head+tail output, TERM then KILL at the
  timeout); background `execs` whose combined output is a ring read by byte
  offset with a long-poll (`output?since=&waitMs=`), with stdin, signals to
  the process group, resize; an interactive `tty` WebSocket speaking exactly
  the `/ws/term` framing (so `<bx-terminal>` and the xbin app's terminal
  work against it); files (`stat`, ranged `content` with etags and
  `ifMatch`, `list`, `mkdir`, `remove`, `move`) and `tar` in/out;
  optional snapshots/clone and archive/thaw. `clientId` makes creates and
  execs idempotent; errors carry a `refusal` from a fixed enum.
- **Inside a sandbox**: a POSIX shell and the usual tools, `IN_SANDBOX`,
  `SANDBOX_ID` — and **never** an xbin identity: no token, no gateway, no
  route to xbind.
- **Cloud managers**: every operation maps onto a provider's instance API
  plus ssh (setsid + a spool file + `tail -c +N` for execs, `ssh -t` for a
  TTY, sftp for files, disk snapshots, a deny-all security group for
  `egress:none`).

**Not chosen:** sandboxes inside the agent (the owner's point: one
mechanism for every tile, and managers anyone can write); a manager-wide set
visible to every bound consumer (owner: partitions, with explicit shares);
manager-side glob/grep (the contract stays small enough for a cloud; the
agent runs them through `run`); SSE or WebSocket for output (byte offsets
with a long-poll resume across a consumer's restart and map onto
`tail -c +N`); a path-based protocol version (`hello` negotiates).

## Agent classes (D116)

The agent's per-conversation lane (🔒 private: internal reach, no egress /
🌐 web: egress, no internal reach) becomes a set of **classes** the tile's
managers define. A class is `{id, name, description, icon, toolsets, mcp,
managers, sandboxEgress, model?, system?, who}`:

- **toolsets**: `files` (session files), `repl`, `web` (web_search,
  web_fetch), `internal` (xbin_call and MCP servers — `mcp` narrows them),
  `sandbox` (the coding tools; `managers` and `sandboxEgress` narrow what
  may be bound), `subagents`, `schedule`, `threads`, `skills`. The core tools
  (memory, note, finish, yield, ask_user) are always there.
- **Built in**: `internal` 🔒 (today's private lane), `web` 🌐 (today's web
  lane) and `coding` ▣ (new: sandbox + web + files + subagents + skills).
  They can be edited; a deleted built-in comes back as its default, since
  old conversations and APIs name it.
- **The firewall** becomes a property of the class: one that holds `internal`
  together with any egress (`web`, or a sandbox with internet) "can move
  internal data out" — saving one takes an explicit confirmation, and its
  conversations say so. The built-in classes never mix them.
- **Fixed per conversation**, as the lane is: a conversation's context was
  gathered under its class's reach.
- **Compatibility**: `toolset: "private"|"web"` (asks, runs, schedules,
  triggers, channels, old configs) maps to `internal`/`web`; `toolset()`
  keeps answering `"private"|"web"` from the class (web = any egress and no
  internal reach), so everything keyed by the lane (skills, the channel
  policy) keeps working.

**Not chosen:** keeping two lanes plus a third "coding" lane (the owner:
classes are the UX, and admins know which tiles hold what); letting a
conversation switch class (the context would cross the firewall);
per-person class grants beyond `who: everyone|managers` (later, if asked).

## Phase 1 — the agent in a sandbox (this branch)

1. The contract doc; `hack/fakesandbox`, a reference manager (stdlib only;
   each sandbox a directory; host exec — test and harness only) whose test
   is the conformance suite; its copy in the agent's tests kept identical.
2. Retire devbox (`retiredTiles` in `internal/builtins`: importing it says
   what replaces it; imported copies keep working).
3. The agent's grants become a registry (threads, and now sandboxes).
4. Agent classes (above), with the composer's class picker, the top-bar
   badge and a Classes settings tab, on the web and native views.
5. The `sandboxes` slot: managers, a merged catalog (`GET /sandboxes`),
   refs `<provider>[#inst]|<id>`, typed contract calls.
6. Binding: `Config.Sandbox` (the active one: ref, cwd, …) and
   `Config.Attached` (up to 8), in `runs.config` like D111's model pick —
   read every turn, inherited by subagents, a rebind applies from the next
   turn. Who may use a sandbox follows D83: its owner, its members, the team
   when it is `team`; a sandbox created for a team-shared conversation is a
   team one; anyone who may steer a conversation may use what it has bound.
   The class gates binding (`sandbox` toolset, managers, egress).
7. Tools: `bash` (an exec plus a long-poll; at its timeout the command goes
   on as a job; interrupt or cancel kills the process group; a restarted
   backend's result names the job), `bash_output`, `bash_kill`, `read`,
   `write`, `edit` (etag-guarded), `ls`, `glob`, `grep` (via `run`),
   `sandbox_upload`/`sandbox_download` (session files ↔ sandbox),
   `sandbox_copy` (between attached sandboxes), `sandbox_info`,
   `sandbox_create` (the owner's grant). `bash` is side-effecting (Approve
   mode) when the sandbox has egress.
8. The UI: the sandbox picker beside the model picker, the ▣ badge (cwd,
   detach, manage), the Sandboxes dialog/screen, tool cards.
9. The UI harness: a fakesandbox tile seeded and bound, fakeopenai scripts,
   a pass.

## Phase 2 — xbind's tile-sandbox runtime (D113, revised)

> **Implementation plan: [tile-sandbox-runtime.md](tile-sandbox-runtime.md)
> (D120)** — the owner's kickoff decisions, the API, the SDK, the mapping of
> the contract onto the runtime, and work packages WP-1…WP-22. Where it and
> this summary differ, the plan wins.

Only a manager tile — holding the admin-approved `cap:sandboxes` — calls
`/api/xbin/sandboxes`; per-consumer and per-person isolation and quotas are
the manager's, xbind books everything to the manager (registry entries carry
the manager's claims `For`/`ForUser`, shown as claims).

**Tile deployments** (the dev-lifecycle design, `plans/dev-lifecycle/` on
its own branch; agreed with that work 2026-09-27): tile sandboxes belong to
the manager tile's *deployment*. A non-primary deployment's backend gets its
own sandbox set, keyed per deployment (state under
`.xbin/deploy/<TK>/d/<deployment>/sbx/` for non-`main` — a deployment segment
under `.xbin/sbx/<key>/` would collide with sandbox names); `{source:true}` mounts that deployment's code and resource
mounts resolve in its data namespace; `cap:sandboxes`, the sandboxes policy
and quotas stay the tile's; VM reservations are booked to the tile
(`Reserve(owner = tile)`, their WP-S3); registry rows carry the deployment
(their WP-S0 `Deployment` field, set only off `main`). That work's WP-S5
("sandboxes follow the deployment") is built here, not there. Whoever lands
first on `internal/sbx`, `internal/cgroup` or `internal/vm` rebases the
other. Known v1 limitation of theirs: a non-primary deployment of the agent
tile can't reach its sandbox managers (the `consumer` role can't be clamped
read-only under their edge policy P23).

In order:

1. **VM backends first**: a Go backend in a VM never listens on this box
   (the guest configures and never reports `listening`). Test the paths no
   test covers — a binary served over the VM file server as a directory and
   as a single-file export, Go and C, 1 and 2 vCPUs, KVM and emulated — add
   a debug dump (goroutines, `/proc/<pid>/wchan`) on a health timeout, then
   fix it (the lead: `os.StartProcess` vforks while the FUSE relay runs in
   the same process; move the relay to its own process).
2. **One exec core** (`internal/sandbox/agentcore`, factored out of the
   guest agent): strict cwd, signals to the process group, file streams;
   `Spec.AgentFD` (a connection factory the init leaves open) and
   `bx __sbx-agent` for namespace sandboxes; a resident shim for VMs.
3. **`internal/tilesbx`**: definitions, state in `.xbin/sbx/<key>/<name>/`,
   lifecycle and idle stop (timers, no tickers), exec rings, the terminal
   wire shared with `/ws/term` (`internal/termwire`), registry entries.
   Egress `none | inherit | class:<slot>` with a new request-side interface
   kind `sandbox-net`, so a manager's own backend needn't hold the internet.
   Mounts only from the manager's own reach, sub-paths bound inside the
   sandbox (never resolved on the host — D78).
4. **Gates**: `cap:sandboxes`; `.xbin/sandboxes/policy.json`; the VM
   policy's `tiles` switch and a tile sub-budget.
5. **API + SDK**: the manager-facing routes (D113 §3), a TTY WebSocket on
   the `/ws/term` wire that the manager relays one for one (no tickets in the
   first version), snapshots, archive/thaw; `sdk/sandbox.go`.
6. **Snapshots and archives**: a sparse-aware clone for VM disks (FICLONE,
   else `copy_file_range`), confined tar for namespace uppers, a backup
   format that keeps symlinks, whiteouts and xattrs and restores safely
   (today's term-layer backup reads and restores sandbox-written trees as
   xbind — fixed on the way), streaming archive I/O (and multipart uploads
   in s3-archiver).
7. **Bases and cleanup**: base-image pinning and GC across `.xbin/sbx`,
   `EnsureDiskAt(dir, size)`, disk listing for both trees, offload archiving
   sandboxes, the admin tab nesting tile sandboxes under their manager.

## Phase 3 — the tiles

1. `<bx-terminal src>`: aimed at a manager's `tty` with the page's frame
   token; the terminal wire documented as reusable. *Landed*: `src` on the
   element (`web/term-src.js`: a same-host path through `xbin.ws`, a drop
   reattaching to the same exec on a manager's route, an `exit` frame or a
   clean close ending it, as the app's terminal does), docs/protocol.md
   §The terminal wire, and the agent template's **Open terminal** (the ▣
   popover and a Sandboxes row; ✕ DELETEs the exec at the manager). The
   page dials the manager itself, so the manager checks the verified
   person. The native view has none: the app's `terminal` dials only the
   tile's own routes, and relaying through the agent's backend would make
   the person asserted (a D96 difference; an app release that takes a
   bound interface's URL would close it).
2. A conformance suite any manager can run (`sdk/sandboxcontract`; the
   fakesandbox suite moves there). *Landed*, with its groundwork: `sdk/ws`
   (a standard-library WebSocket in the SDK, checked against gorilla both
   ways) and terminals in the reference manager (host PTYs on the
   `/ws/term` framing), so the suite checks `tty` too.
3. **`builtin-templates/coding-sandbox`**: a `Backend` Go interface (the
   agent-messaging-bridge `Platform` pattern) with an `xbin` backend (VMs by
   default) and a fake; images as setup scripts snapshotted on first use;
   per-consumer and per-person access and quotas; a UI (sandboxes, create,
   lifecycle, snapshots, archive/thaw, files, a terminal, shares); a native
   view; `AGENTS.md` on adding a cloud/ssh backend; live isolated tests.
4. **`builtin-tiles/sandbox-terminal`**: browser terminals straight to the
   manager (the page's verified user), SSH ingress (a `stream` expose, keys
   registered per person, the user name is the sandbox) bridged to the
   `tty` route; sandboxes reach it by being shared with it. *Landed, the
   backend and SSH* (D121): keys, `GET /sandboxes` with each sandbox's
   login, the SSH server (a pty → the `tty` route; no pty → an exec with
   stdin), tested end to end against the reference manager. The page (web
   and native), the harness pass and the agent's "share with a terminal
   tile" follow.

## Open questions (asked at the phase 2 and 3 kickoffs)

- ~~sandbox-terminal's SSH path acts for a person the manager can't verify:
  a `gateway` role for asserted users?~~ No: its backend asserts the person
  (`Sbx-User`) as any consumer's may, and enforces the person rules itself
  (D121).
- Relay only, no xbind tickets — and admins never attach?
- D88's `noTerminal` for sandbox terminals.
- Namespace uppers in component backups by default?
- Emulated VMs for tile sandboxes; a namespace fallback where VMs can't run?
- Thawing onto a changed base image.
- Offloading a manager: archive its sandboxes, or refuse?
- ~~A conformance suite in the SDK.~~ Yes: `sdk/sandboxcontract` (phase 3
  item 2).
- Internal-class sandboxes with no network, if ever wanted.
