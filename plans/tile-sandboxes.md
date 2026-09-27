# Tile-managed sandboxes

> Status: **live** (D113) — designed, not built: the design the next project
> builds and edits as it goes.

The substrate landed with D112: the sandbox registry (`internal/sbx`, kind `tile` with a `parent` reserved for
these) and VM reservations charged to a tile (`vm.Manager.Reserve(owner, …)`).
This implements plans/vm-sandbox.md §Sub-sandboxes. The first consumer is the
builtin agent template (§7); nothing here is specific to it.

## 0. Why, and what exists

The agent template will run agents against sandboxes: N sandboxes bound to M
agents, a sandbox set separate from the agent set. Many agents can work in one
sandbox (in one or more directories), one agent may work in several, files move
between sandboxes, and sandboxes may talk to each other. The runtime's job is
the general mechanism: **a tile creates and drives sandboxes of its own.** The
agent template is one tile that uses it.

What the code gives us today:

- **No tile can start a sandbox.** Terminal and agent sessions (D74) are
  human-plane: `Principal.CanTerminalTile` refuses every component principal
  (`internal/auth/auth.go`), `CanTerminalTileVia` admits only a terminal
  token on its own tile, and an agent's own token is refused everywhere.
- **The launch path exists.** `sandbox.Launch(*Spec)` builds any sandbox
  from a `Spec`; `vm.Apply` turns the same `Spec` into a VM (refusing host
  networking, splice/links, more than one lower, devices; clearing
  `NetAdmin`/`Containers`).
- **Nothing can exec into a namespace sandbox after start**: the init applies
  its guards and `unix.Exec`s the entry (`internal/sandbox/init_linux.go`) —
  D74's reason for "no sub-sandbox".
- **A VM guest can already run many processes**: the guest protocol numbers
  its sessions (`proto.Exec{Session,…}`) and the guest agent keeps a session
  map; only the shim limits it to session 1 and kills the VM when that exits.
- **D82: tiles never nest.** A VM backend can't nest a VM (no nested KVM), and
  a backend spawning its own sandboxes would bypass the budget, the registry
  and the policy.

## 1. Model

- **A tile owns a set of named sandboxes**, independent of its backend:
  blue/green and idle reaping of the backend don't touch them, and every
  instance token of the tile addresses the same set. They are **siblings
  xbind launches** (never nested), listed in the registry as kind `tile` under
  their owner.
- **Identity:** a name `[a-z0-9][a-z0-9-]{0,31}`, unique per tile, stable
  across restarts — the API key. The registry gives each live one an id.
- **Lifecycle:** `stopped → starting → running → stopping`. Create (stored; not
  booted unless `start:true`), start, stop (sync, then kill), reset (wipe its
  root layer or disk), delete (stop, then remove everything). An exec or a
  file operation on a stopped sandbox starts it (auto-start, default on).
- **Across xbind restarts:** definitions persist, processes do not (the relay
  that carries egress lives in xbind; the shim and the agent are its
  children). After a boot every sandbox is `stopped`; exec ids from before
  answer `lost`. Definitions live in the tile's `data/sandboxes.json` (a
  component backup carries them, like `cron-jobs.json`).
- **What a sandbox sees:** the pinned workspace rootfs (the terminal layer
  machinery, `term/base.go`), plus persistent state at
  `<ws>/.xbin/sbx/<CompKey>/<name>/` — `upper/` + `work/` in namespace mode,
  `vm/disk.img` in VM mode (`EnsureDisk`, with a per-sandbox size). Working
  directories (`/work/<x>`) are just paths on that root: guest-local and fast
  in both modes.
- **Mounts — never beyond the tile's own reach:**
  - a `filesystem` resource the tile holds: `{"res":"res:<scope>/<name>",
    "path"?, "ro"?}` — writer role may mount read-write, reader forces
    read-only; `sqlite` is refused (two guests' caches aren't coherent over
    the VM file server, as the runner's `stopFirst` already notes);
  - `{"source":true}`: the tile's own directory, read-only;
  - later: `{"code":"apps/x"}` read-only when the tile holds `code:apps/x`,
    and tile-private scratch volumes;
  - never: homes, the workspace view, `.xbin`, `data/` outside the resource,
    the gateway socket, an instance token, vault contents, devices.
  Encrypted resources need the vault unsealed; while sealed, start answers
  503, as backends are held.
- **Mode, per sandbox:** `namespace` (the `Restricted` + `MountGuard` lockdown,
  D18: `apt` works, nested namespaces don't) or `vm` (root in its own guest:
  docker works; needs the VM policy's `tiles` switch). A VM that can't start is
  an error, never a namespace fallback (D78/D89); `emulated` is reported.
- **Limits** are carved from the tile's allowance (§6): a cgroup leaf per
  sandbox (memory, pids, and a `cpu.max` from its vCPUs); the per-tile total
  is enforced at admission.
- **Removal:** deleting the tile deletes its sandboxes (the D82/D85 leftover
  hook forgets definitions and state for the path); disabling or offloading it
  stops them.

## 2. One exec protocol for both modes

- **`proto`, unchanged in shape.** xbind allocates session numbers from 2 per
  sandbox (1 is the VM's reserved first session).
- **Transport:** at launch xbind creates a listening unix socket in an
  xbind-only directory (`.xbin/run/sbx/`, never bound into any sandbox) and
  passes the fd through a new `Spec.AgentFD` (an inherited fd the init leaves
  open across its exec). Never a socket path inside the sandbox: sandbox code
  could swap it for a symlink to another host socket.
- **Namespace mode:** the entry is `bx __sbx-agent` — the daemon's static
  `bx`, bound read-only as for `__agent-host` — running the guest agent's
  session core (`guest/session_linux.go`, factored out of its transport),
  `accept()`ing on `AgentFD`. `config` is a no-op answered `ready` (the init
  built mounts and network). Same trust argument as `__agent-host`: xbind's
  code, under every guard. This retires D74's "nothing can run a second
  process after init" for tile sandboxes.
- **VM mode:** a new `HostSpec.Resident`: the shim configures the guest, runs
  no session 1, stays the only owner of the control connection (the guest
  agent sends events to the newest control connection only), routes xbind's
  exec/resize/signal to the guest and fans events back, and pipes a vsock
  stream per exec stream. An exec ending doesn't end the VM; SIGHUP still
  means sync, then exit.
- **Protocol fixes needed:** a strict cwd (today a missing cwd falls back to
  `/` — an agent's `rm -rf *` would run there): `Exec.CwdStrict`; signals to
  the process group (`Msg.Group`; execs already `Setsid`); file streams (new
  Hello kinds `get`, `put`, `stat` carrying `{Path, Tar, Mode, Mkdirs,
  Overwrite}`, done by the agent inside the sandbox — §4).
- **Execs in xbind:** an output ring per exec (combined frames `{s: out|err|pty,
  seq, data}`, 1 MiB default, up to 8 MiB by policy, loss reported), many
  readers (the backend, a human attach) and stdin writers; a timeout sends TERM
  to the group, then KILL after a grace; the last N exited execs are kept per
  sandbox (lost on restart).
- **Many agents in one sandbox:** each exec carries its own cwd and env.
  Directories are a convention, not a boundary (`Restricted` forbids nested
  mount namespaces, so a per-exec mount view would make the modes differ);
  hard separation means separate sandboxes.
- **Environment:** `IN_SANDBOX=1`, `XBIN_SANDBOX=<name>`, `PATH` as in
  terminals, plus what the request adds. Never `XBIN_TOKEN` or `XBIN_URL`.

## 3. The API: `/api/xbin/sandboxes`

For a tile's backend (its instance token, through the gateway socket), scoped
to the caller's tile:

| Route | Body / query | Result |
|---|---|---|
| `GET /sandboxes` | | the tile's sandboxes (today's admin route, scoped by `sandboxScopeOf`) |
| `POST /sandboxes` | `{name, mode, memMiB?, vcpus?, diskGiB?, net:{egress:"none"\|"inherit"\|[rules]}, mounts:[…], env?, labels? (≤1 KiB, opaque), idleStopMin?, user?, start?}` | `SandboxInfo` |
| `GET` · `PATCH /sandboxes/{name}` | | `SandboxInfo`; a PATCH says `restartNeeded` when it applies at the next start |
| `POST /sandboxes/{name}/start` · `stop` · `reset`; `DELETE /sandboxes/{name}` | | |
| `POST /sandboxes/{name}/run` | `{argv\|cmd, cwd, env, stdin?, timeoutSec, maxOutput}` | waits: `{code, stdout, stderr, truncated, timedOut, ms}` — most tool calls |
| `POST /sandboxes/{name}/execs` | `{argv\|cmd, cwd, env, tty, rows, cols, stdin:bool, timeoutSec, label, user?}` | `{id}` |
| `GET /sandboxes/{name}/execs` · `/execs/{id}` | | |
| `GET /sandboxes/{name}/execs/{id}/output?since=<seq>&follow=1` | | chunked NDJSON, resumable by seq (long-poll without `follow`) |
| `POST …/execs/{id}/stdin` (raw, `?eof=1`) · `signal {signal, group}` · `resize`; `DELETE …/execs/{id}` | | |
| `GET` · `PUT /sandboxes/{name}/files?path=&tar=1`; `GET …/stat?path=` | | |
| `POST /sandboxes/copy` | `{from:{sandbox,path}, to:{sandbox,path}, overwrite}` | |
| later: `GET …/ports/{port}` (`Upgrade: xbin-tcp`), `POST …/execs/{id}/ticket {user, ttlSec, readOnly}` | | |

- **Streams:** backends get chunked HTTP with cursors — the zero-dependency
  SDK has no WebSocket client, and a cursor survives blue/green (the new
  generation resumes from the seq it saved). Humans get the existing
  `/ws/term` wire (§ attach). The gateway serves the same handler as the main
  listener, so every form works over it.
- **Authorization:** `Component != "" && Via == "instance"` and a grant on the
  new **`cap:sandboxes`** (admin-approved, never same-scope auto-granted; a
  ceiling class of its own), checked on every call, so a revoke takes effect
  at once — and stops the tile's sandboxes (state kept). Frame principals get
  403 (default-deny: a user's devtools hold their frame token); terminal
  tokens too in the MVP. Admins may list, stop and delete any tile's
  sandboxes (`?tile=`), not create them. **No token inside a sandbox**: its
  code can't drive its own or a sibling sandbox.
- **Confused deputy:** an instance token carries no user, and the tile's reach
  is the ceiling whatever user it acts for. `user` is stored and shown only;
  xbind widens nothing from it. The one user-facing power, attach, is
  verified against the real user.
- **Human attach (phase 2):** the backend (which knows its own per-user ACL,
  e.g. the agent template's D83 conversations) mints a one-shot ticket bound to
  (tile, sandbox, exec, user, readOnly, 2–5 min); the tile's frame embeds
  `<bx-terminal ticket=…>`, which opens `/ws/term?ticket=…`. xbind checks the
  ticket, that the session's user is the ticket's, that the user can read the
  tile and that the login is live. Same wire, so bx-terminal, predictive echo
  and the app's terminal work unchanged.
- **SDK:** `sdk/sandbox.go`, zero dependencies: `Sandboxes().Create/Get/
  List/Start/Stop/Delete`, `s.Run`, `s.Exec` (an output iterator with cursor,
  `Stdin`, `Signal`, `Resize`, `Wait`), `ReadFile`/`WriteFile`/`Stat`/
  `GetTar`/`PutTar`, `Copy`, `AttachTicket`; later `s.Dial(ctx, port)`.

## 4. Moving files

- **xbind never resolves a sandbox-supplied path on the host** (D78; D89
  rejected an xbind file server for the same reason). For VM disks, stricter:
  the host never parses what the guest wrote, so a guest ext4 is never
  host-mounted.
- **Between two sandboxes of a tile:** a `get` stream on the source's agent,
  a `put` stream on the destination's, xbind piping bytes it never parses.
  Extraction happens inside the destination, where the kernel enforces its
  read-only mounts. The same in both modes; caps apply.
- **Between a sandbox and the tile's resources:** the backend does the I/O in
  its own sandbox and streams through `files`; or mount the resource.
- **Live sharing:** one `filesystem` resource (or, later, a scratch volume)
  mounted into several sandboxes. Across VMs it is FUSE: fine for hand-off
  directories, wrong for databases (no cross-guest coherence).
- **Clone/snapshot (phase 3):** a VM `disk.img` is an xbind-owned file (a copy
  or reflink is fine); a namespace `upper/` is a tree the sandbox wrote, so
  walking it is confined (`internal/confine`).

## 5. Networking

- **Egress per sandbox, fixed at start:** `none` (default); `inherit` (the
  tile's own `EgressFor`, resolved at start); or a rule list that the tile's
  policy must cover (the relay enforces `tile ∧ list`). A tile whose `net` is
  host-share or a provider splice can't `inherit` (VMs refuse both; tile
  sandboxes never get host networking).
- **A relay is always there** (`Net:"relay"`, deny-all and no DNS for `none`),
  as the runner does for ingress-only plumbing, so `DialIn` and peers work in
  both modes.
- **Backend → sandbox port (phase 2):** `relay.DialIn`, exposed as an upgrade
  stream and `sdk Dial`; a tile reverse-proxies a dev server into its own
  frame — no new origin surface.
- **A tile-private network (phase 2):** L4 in the relay: `<name>.sbx`
  resolves to a virtual IP, TCP to it becomes a dial into the peer's relay
  (the `Published`/`HairpinDial` pattern), one tile only, to the ports the
  target lists. Never an L3 splice (VMs refuse splices/links).
- **Sandbox → its tile (phase 3):** a host forward on the gateway IP to the
  tile's backend with a declared custom role `sandbox` (and `X-XBin-Sandbox`),
  which `admin` doesn't satisfy — the D86 `channel` precedent.

## 6. Budget, policy, lifecycle

- **A sandboxes policy** (`.xbin/sandboxes/policy.json`, `GET`/`PUT
  /api/xbin/sandboxes/policy`, admin): `{enabled (kill switch, default true),
  perTile {max 8, running 4, memMiB 8192, vcpus 8, diskGiB 100}, perSandbox
  {memMiB 2048 (≤ 8192), vcpus 2 (≤ 8), diskGiB 20 (≤ 200)}, idleStopMin 30
  (≤ 1440), tiles {"<path>": overrides}}`. Requests are clamped as manifest
  `vm` sizes are.
- **VM policy additions:** `tiles` (default false; the installer's D110 rule
  never touches an existing file) and `tilesBudgetMiB`, a sub-budget of
  `budgetMiB` (default half) so tile VMs can't starve people's VM terminals.
- **Admission:** VM mode goes through `Reserve(owner=tile, …)` (global count
  and budget, the tile sub-budget, the per-tile running cap: the per-owner
  books D112 added); namespace mode sums against the per-tile caps plus its
  leaf's `memory.max`/`pids.max`/`cpu.max` (a nested per-tile cgroup later).
- **Disk:** a VM disk is sparse and capped at its size; a namespace `upper/`
  has no hard cap — it is counted in the scope's footprint (as diskmon walks),
  and a tile over quota is refused start and raises an alert.
- **Idle stop:** a per-sandbox timer reset on activity (a running exec, an
  attached client, a file operation, a port stream) — no ticker. On firing:
  sync, stop; the next exec starts it again. A long-running exec keeps it up,
  bounded by the caps.
- **Policy flips:** `enabled → false` stops every tile sandbox and refuses
  starts (state kept); `vm.tiles → false` stops VM-mode ones, which then
  refuse with the reason; lowered sizes apply at the next start; a revoked
  cap or a disabled tile stops that tile's.
- **Shutdown and crashes:** shutdown stops them all (sync) beside
  `run.StopAll()`; after a crash, orphans are swept at boot (stale `sbx-*`
  cgroup leaves), since nothing sets `Pdeathsig`.
- **Visibility:** the admin console's runtime → sandboxes tab (D112) lists
  them nested under their tile (`parent`), with their failures.

## 7. How the agent template would use it

This is the shape check: every need maps to a route, and xbind needs to know
nothing about lanes, conversations or users.

- **Tables:** `sandboxes` (alias, xbind name, owner, visibility/team role per
  D83, lane, mode, spec, `created_by_run`, scope `conversation | user |
  team`) — separate from agents; `sandbox_bindings (root_id, sandbox, alias,
  dirs[{path, rw}], default_cwd)`, many-to-many (a subagent inherits its
  root's, optionally narrowed at spawn); `sandbox_execs (run_id, sandbox,
  exec_id, cursor, status)` so a new backend generation resumes streams.
- **Access:** 404/403 as for runs; managers may stop/delete and see sizes but
  not read a private sandbox's output; conversation-scoped sandboxes go with
  the conversation.
- **Lanes (the tile's, from fields xbind exposes):** a private-lane sandbox
  has egress `none` and may mount internal resources; a web-lane one may
  inherit egress and gets no internal mounts; a run binds only sandboxes of
  its lane; `sandbox_copy` private → web is refused; schedules inherit the
  lane.
- **Tools** (routed by alias or name, cwd defaulting to the binding's
  directory): `sandbox_list`, `sandbox_create` (feature-gated; managers may
  restrict it to people), `sandbox_exec {sandbox, cmd, cwd?, timeout_s?,
  background?}` (→ `run`, or an exec + cursor), `sandbox_output`,
  `sandbox_kill`, `sandbox_read`/`write`/`edit`, `sandbox_copy`,
  `sandbox_upload`/`download` (session files ↔ sandbox). Exec approval per
  conversation, like D77's "allow for session".
- **UI:** a Sandboxes panel, binding chips on conversations, "open terminal"
  (a ticket for `<bx-terminal>`), D96 parity keys and the native view.

## 8. Phasing and risks

- **P0 (done, D112):** the registry, owner-attributed reservations, the admin
  view, this design.
- **P1 — xbind MVP:** `cap:sandboxes`, the sandboxes policy and `vm.tiles`; the
  manager (persistence, layers, disks); `bx __sbx-agent` (the session core
  factored out); the resident shim and `AgentFD`; the protocol fixes; `run`,
  `execs`/output/stdin/signal/resize; files and same-tile copy; egress `none |
  inherit | subset`; idle stop; restart semantics; SDK, docs, OpenAPI,
  changelog; an integration fixture tile (e.g. `examples/sandbox-go`).
  **P1 — agent template:** tables, bindings, lanes, the exec/read/write/edit/
  copy tools.
- **P2:** human attach (ticket, `/ws/term`, bx-terminal, the native app);
  port streams and proxied previews; relay peers; scratch volumes; `code:`
  mounts; a nested per-tile cgroup; operator access (terminal token, `bx
  sbx`); exec-exit pushes to the backend (an `xbin/sandbox` principal, the D85
  pattern).
- **P3:** snapshots, templates, clones; GPUs in namespace mode; the
  sandbox → tile role; env layers (`setup`); per-exec directory confinement
  inside VMs.
- **Risks:** every xbind restart kills running work (and boxes auto-update);
  orphans after a crash; guest-held memory (the balloon helps; the sub-budget
  protects terminals); unbounded namespace uppers; an untrusted agent-protocol
  peer (bound every frame); FUSE incoherence between guests; emulated VMs 5–20×
  slower; the resident-shim router's complexity; prompt injection leading to
  code execution with egress (default `none`, plus the lane firewall).

## 9. Open questions (for the next project's kickoff)

1. Human attach: a tile-minted ticket only, or may the tile's operators
   (terminal level) and admins attach without one? (D83 keeps admins out of
   private runs.)
2. Does `noTerminal` (D88) forbid TTY attach, or TTY execs attributed to that
   user, on tile sandboxes?
3. Backups: should namespace uppers go into component backups (VM disks stay
   out, like `vm/` today)? Is `.xbin/sbx` right, given `.xbin` is "derived
   state" (terminal layers already live there)?
4. Default caps: 8 per tile / 4 running / 8 GiB, half the VM budget for tiles,
   idle stop 30 min, auto-start on exec?
5. Who approves `cap:sandboxes`: workspace admins only (like `cap:containers`),
   or also org admins / personal owners within their allowance (like
   `cap:open-links`)?
6. Is losing running work on an xbind restart acceptable, or is a
   restart-surviving design (the relay out of process) worth it later?
7. Namespace mode as the `Restricted` terminal lockdown (apt works), docker
   only in VM mode?
8. Should sandboxes ever get a scoped API token, or does the tile always
   proxy?
9. MVP mounts: `filesystem` resources + the tile's own source read-only, with
   `code:` mounts and scratch volumes in P2?
10. Is TCP-only L4 peering by name enough for the private network?
11. The agent template: may the model create sandboxes, or only people and
    managers? Does each conversation get one automatically?
