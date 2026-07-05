# Isolation: sandboxes, terminals, and the dev layer

xbin runs on two planes, and **both** are sandboxed with a default-deny
posture — you get your own code, your granted state, and nothing else until
something is explicitly wired:

- **The runtime plane** — a component's **backend**, a least-privileged tenant.
- **The owner/editing plane** — **terminals**, where you (or an agent) edit code.

The mechanism is Linux namespaces over an overlay rootfs (design in
`plans/isolation.md` / `plans/runtime.md`); this page is the builder-facing
summary of what that means for you.

## The backend sandbox (runtime plane)

A running backend sees a minimal, purpose-built filesystem:

| Mount | Access | What it is |
|-------|--------|------------|
| base rootfs | read-only | Go / node / python toolchains + core tools |
| env layer | read-only | your `setup` deps, prebuilt into an overlay lower |
| your component dir | **read-only** | your source (editing is the terminal's job) |
| granted resource dirs | **read-write** | `filesystem`/`sqlite` resources you were granted |

It does **not** see other components' source, other elements' vaults, the
workspace `home/`, `data/`, `.xbin/`, or the host — they simply aren't
mounted. Its own source is read-only at runtime, so **persist state only in a
resource dir**; anything you write elsewhere lands in a throwaway overlay and is
gone on the next restart ([resources.md](/docs/resources.md)).

Two things always work regardless of the network:

- **The gateway.** Reaching xbind and calling other components (subject to your
  grants) goes over the gateway unix socket in env as `XBIN_GATEWAY` — that is
  not IP egress and is never blocked.
- **Nothing else, by default.** A backend has **no IP egress** until the owner
  binds its `net` interface (see **Network egress** below).

## Terminal isolation (owner/editing plane)

Terminals are the editing plane — a real shell, owner-privileged, in a
component's directory. They come in two shapes:

- **Root terminal** (opened on the workspace root) — the whole workspace is
  **read-write**. This is where you create components, run workspace-level git,
  and edit shared state. Full power; use it deliberately.
- **Component terminal** (opened on a component) — the workspace is mounted
  **read-only except `$HOME` and that component's own directory**. You can read
  siblings for patterns, but you cannot edit them, workspace state
  (`xbin.json`, `AGENTS.md`, `go.work`), `data/`, or `.xbin/`.

So a rogue agent in a component terminal can only touch **its own component and
`$HOME`** — it can't break the workspace or reach into another component.

Commits still work from a component terminal because **each component is its own
git repo**: `cd` into the component and `git commit` writes to the component's
`.git`, which lives inside the writable component dir. (Cross-component or
workspace-wide git is a root-terminal job.) This is also what makes components
**installable** — importing one from a git URL is just a clone.

## `$HOME` — shared across terminals

`$HOME` is `<workspace>/home`, and it is **read-write in every terminal** —
including component terminals, where it's the one writable thing outside the
component itself. It is **shared**: your agent-CLI config, auth/login state, and
dotfiles live there once and follow you into every terminal, and they **survive
xbind upgrades** (they're workspace data, not part of any rootfs).

## The dev layer — persistent, per-component, resettable

A terminal is a full dev box, not just a shell. Filesystem changes you make to
the *system* — an `apt install`, a tweak under `/etc`, an extra toolchain —
don't touch the base rootfs (read-only) and aren't lost at exit. They're
captured in a **persistent per-component layer** at `.xbin/term/<component>/`
(the overlay's upper dir), which **survives across sessions and restarts**. Each
component effectively gets its own long-lived dev sandbox; the ⟲ button in the
terminal UI resets it back to a clean base.

Keep two "layers" straight — they are deliberately separate:

- **The terminal dev layer** (`.xbin/term/…`, above) is for *interactive* work:
  whatever you install while poking around in the shell.
- **The component env layer** is built from the manifest's `setup` and is what
  the **running backend** gets (read-only). This is where **backend
  dependencies** belong, so they're declared, reproducible, and present at run
  time — not accidentally living only in some terminal's upper dir.

Rule of thumb: `apt install` in a terminal to try something; move anything the
backend needs into `setup`.

> Only one live session may hold a given component's persistent layer at a time
> (concurrent overlay mounts of one upper dir would corrupt it). A second
> concurrent terminal on the same component falls back to an ephemeral layer —
> functional, but its system changes don't persist.

## Network scopes for terminals

A terminal picks a network scope when it opens (the net selector in the UI):

- **internet** *(default)* — its own network namespace with an egress relay that
  permits the **public internet only**; host interfaces and the LAN stay hidden.
  `XBIN_URL` is transparently routed so `bx`/`curl` still reach xbind.
- **host** — shares the host network (LAN + host-local services visible). An
  owner escape hatch; use it when you specifically need host reachability.
- **none** — an isolated namespace with **no egress at all** (airgapped —
  xbind itself is unreachable).

Note the contrast: a **terminal** defaults to public-internet egress (you
usually want to `git clone`, `go get`, `npm i`), whereas a **backend** defaults
to *no* egress until its `net` interface is bound.

## Network egress

Egress is an **interface**, not an ambient capability. A backend requests a
`net` interface; the **owner binds** it to a provider (binding *is* the
authorization — a component can never self-bind). Providers include:

- the **`internet`** builtin — public internet only, through a userspace gVisor
  relay that terminates and meters every flow;
- **`host`** / **`lan:<cidr>`** — host or a specific LAN range;
- a **provider tile** — a VPN, firewall, or router (e.g. the egress-approver or
  s3-archiver's upstream). Provider tiles are themselves clients of *their* own
  egress, so binding one to another **chains** them
  (client → firewall → VPN → internet) purely from the binding graph, no code.

The full interface model (request / provide / bind, plus `http` service
contracts and the `@archive` slot used by backups) lives in
[protocol.md](/docs/protocol.md); the design rationale is in
`plans/interfaces.md`.

---

**See also:** [resources.md](/docs/resources.md) (what persists and where),
[auth.md](/docs/auth.md) (grants, principals, the vault),
[protocol.md](/docs/protocol.md) (interfaces, bindings, the full API).
