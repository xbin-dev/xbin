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

Terminals are the editing plane — a real shell, scoped to its tile, in a
component's directory. (The **root terminal** — a shell on the workspace root —
is **disabled**; workspace-wide work happens in the browser UI or a host shell.)

A **component terminal** mounts the workspace **read-only except `$HOME` and
that component's own directory** — so you can read every other tile's *source*
(needed to integrate against its API) but edit only your own tile and `$HOME`.
On top of that, the platform's secrets and other users' data are **masked out
entirely** (an empty overlay hides them, even though the root is bound
read-only):

- **`.xbin/`** — the owner token and the frame-token secret. Without this mask
  a shell could `cat .xbin/token` and become owner, defeating the tile-scoped
  terminal token (below).
- **`data/`** — the vault, the encrypted resource state, and `users.json`
  (password hashes). Terminals reach resources through the API, never the raw
  at-rest files.
- **`homes/`** — every *other* user's `$HOME` (their agent credentials, shell
  history). Your own `$HOME` remains read-write.

This holds for **every** terminal, including one on a tile you own. So a rogue
agent in a component terminal can touch **only its own component and `$HOME`**,
and can read code but not secrets.

**Live-API toggle.** A terminal's titlebar has a **tile-API / no-API** switch
(alongside the network scope). With it off, the session is minted with **no
token** — the shell can read and edit source but every call to the tile's (or
xbin's) API is unauthorized. Use it to run untrusted code or an agent that
should see code but not act on the live workspace.

**Honest bound.** The masks make the secrets *unreadable in normal operation* —
the realistic hygiene threat (an agent reading `~/.claude` of others, `grep`-ing
the tree, `cat`-ing the token). They are an isolation property of `--isolate`
(tier 3); a non-isolated (tier-1) host-shell terminal still sees the host
workspace. And because a single-uid sandbox shell is root in its own user
namespace, a *deliberately adversarial* shell could `umount` a mask to reveal
what's beneath — closing that fully needs per-tenant uids (the multi-tenant
work). For today's single-owner workspace, where terminal users are the owner
or delegated admins, the masks make the tile-scoped terminal token a real
boundary against agent misbehavior and casual escalation.

Commits still work from a component terminal because **each component is its own
git repo**: `cd` into the component and `git commit` writes to the component's
`.git`, which lives inside the writable component dir. (Cross-component or
workspace-wide git is a host-shell job — the root terminal is disabled.) This is also what makes components
**installable** — importing one from a git URL is just a clone.

## `$HOME` — per user, shared across that user's terminals

`$HOME` is `<workspace>/homes/<user>` — one home per signed-in user (the root
token gets `homes/owner`) — and it is **read-write in every terminal**,
including component terminals, where it's the one writable thing outside the
component itself. Within a user it is shared: your agent-CLI config, auth/login
state, and dotfiles live there once and follow you into every terminal you
open, and they **survive xbind upgrades** (workspace data, not part of any
rootfs). Other users get their own homes — configs don't mix. (Hygiene, not a
security boundary — the filesystem user is the same; the API credential,
though, is per-session and tile-scoped, see plans/terminal-tokens.md.)

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
