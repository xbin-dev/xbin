# Changelog

Builder-visible changes to xbind, `bx`, the SDK, core elements, and builtin
tiles — newest first. Entries marked **BREAKING** link a migration note under
`/docs/changes/` with exactly what to change; **read those after every xbind
upgrade** (`curl -s -H "Authorization: Bearer $XBIN_TOKEN"
"$XBIN_URL/docs/changelog.md?raw=1"` from any terminal).

Maintainers: every builder-visible change lands an entry here in the same
commit; breaking ones add `changes/YYYY-MM-DD-<slug>.md` (rules: repo
`AGENTS.md`).

## 2026-08-03

- **Container image stores: `plain: true` filesystem resources (D43).**
  Encrypted (gocryptfs) resources structurally cannot host a podman layer
  store — the unprivileged FUSE daemon is the I/O actor, so 0555 layer
  dirs block its own `.pivot_root` mkdirs (`ApplyLayer … permission
  denied`) and sub-uid chowns are refused. A `filesystem` resource can now
  declare `"plain": true`: a plaintext kernel dir under `data/resources/…`
  — never resenc-mounted, never held on vault state, not stopped by seal
  (deliberate: the data opts out of vault protection; don't put secrets
  in it). Filesystem-type only; elsewhere the flag warns and is ignored.
  `bx doctor` lists every plain resource and flags leftover ciphertext of
  now-plain resources; the admin tile resources view gets a `plain` pill.
  `cap:containers` sandboxes also gain a private cgroup2 view at
  `/sys/fs/cgroup` (libpod stats it even with cgroups disabled) — tiles
  can drop their self-mount workaround. Migration:
  [changes/2026-08-03-container-store-resources.md](changes/2026-08-03-container-store-resources.md).

- **The installer knows when xbin is already there.** A no-flag non-root
  run on a box with a system-wide install now leads with that fact
  (version, running state, listen address) and makes upgrading the
  default — plain Enter at the chooser sudo-upgrades; user mode is
  offered as a separate second instance. Side-by-side installs stop
  colliding on the port: a fresh user instance auto-moves to the next
  free port when 8642 is taken (stated in the plan and the summary),
  upgrades preserve the existing unit's port, and an explicitly
  requested busy `XBIN_LISTEN` fails the plan up front.

- **Ubuntu 26.04 LTS everywhere**: the base rootfs image (terminal +
  backend sandboxes; existing terminal env layers stay pinned to their
  old base, which upgrades preserve as `rootfs-<version>`) and the macOS
  Lima VM image both move from 24.04 to the current LTS. Bonus for
  container-host tiles: 26.04's podman ships netavark, so bridged
  container networking works out of the box.

## 2026-08-02

- **SECURITY — restricted terminals now mount an allow-list view (D40).**
  A production check showed a non-admin tile terminal could enumerate
  unreadable siblings' NAMES (each deny-mask is a visible tmpfs) and read
  the workspace root `xbin.json` — the entire grants/bindings topology,
  public hostnames included. Restricted terminals now get a staged view:
  only readable tiles are mounted (unreadable ones are absent, names and
  all), `xbin.json` is redacted to rows referencing only readable
  components, `go.work` is filtered to readable modules (so builds don't
  chase absent dirs), and `.xbin`/`data`/other homes simply don't exist
  inside — no masks, no resenc names in the mount table. Admin terminals
  keep the full read-only view. If your agent tooling relied on reading
  another (unreadable) tile's source from a non-admin terminal, that was
  the leak: ask for `read` on it.

- **Upgrades backfill essential tiles, and terminator owners control their
  domains (D41).** Workspaces created before `tiles/organisations` existed
  now get it installed at boot (the shell's ⚑ button targets it) — once:
  the backfill is ledgered in `data/backfills.json`, so deleting the tile
  afterwards sticks. `bx builtin updates` lists missing essential tiles
  and `bx builtin update tiles/organisations` installs one (bare names
  resolve). Workspaces already using defaultTiles get a read entry for the
  new tile; others are left for the admin to decide. And ingress consent
  now follows terminator ownership: an org admin can publish org tiles
  through the org's own terminator — and approve outside tiles publishing
  through it — without any allowance; host ports and the builtin listener
  still need one.

- **The installer grew a user-only mode and a plan-before-approve flow.**
  `curl … | bash -s -- --user` installs xbin entirely under your own
  account: `~/.local/opt/xbin`, a systemd *user* unit with lingering, no
  root anywhere — anything that would need root (missing distro packages,
  a missing subuid range, an AppArmor userns restriction) is caught in
  preflight and reported with the exact one-line root command, before
  anything is touched. Both modes now print a numbered plan of exactly
  what this run will do (steps already in place listed as skipped) and
  ask once; `--check-only` stops after preflight + plan. The website
  shows both commands.

- **Hidden tiles (D42).** Tiles can now be hidden: a lifecycle state that
  is exactly `disabled` (backend stopped, refuses to spawn) plus removal
  from sidebars and listings. The workspace sidebar, the admin console's
  components table and access map, and the organisations tile's org-tiles
  list all filter hidden tiles behind a "show hidden (N)" toggle, render
  them dimmed with a badge when shown, and offer hide/unhide wherever
  lifecycle controls live (the ⚙ panel too). Same D24 gate as the rest of
  lifecycle: the tile's owner, its org's admins, or a ws-admin. `bx hide` /
  `bx unhide`; /components rows now carry the lifecycle `state`. Hiding an
  offloaded tile is refused (restore first); placed tiles stay on screens,
  rendering their disabled state.

- **macOS installs, for real.** `curl -fsSL https://xbin.dev/install.sh |
  sh` on a Mac now sets up a lightweight Linux VM via Lima (Apple's vz
  runtime; qemu fallback pre-macOS 13) sized by you at install time
  (defaults 32 GiB thin disk / 4 GiB RAM / 4 CPUs), runs the regular Linux
  installer in system mode inside it pinned to the same release, forwards
  the UI to the Mac's loopback :8642, and prints the one-time login URL —
  with the same numbered plan-before-approve contract as the Linux
  installer. Requires Homebrew for Lima (never installs Homebrew itself);
  re-running upgrades xbin inside the existing VM; `limactl delete xbin`
  uninstalls. The Linux installer's macOS refusal now points at the
  one-liner instead of a bare "get a VM".

- **The installer chooses with you, not for you.** Run without sudo and
  without a mode flag and it explains system vs user mode (system creates
  a dedicated `xbin` user for better separation), prints BOTH numbered
  plans — the system plan from read-only probes, re-verified after
  escalation — and asks: [s]udo into the system install from right there,
  [u]ser-only, or quit. `--system` without root now shows the full
  read-only plan and offers to sudo instead of dying; `--yes` never
  guesses a mode.

- **Publishing from the bind dialogs actually works now, and org admins
  got a wiring surface.** The root page's bind panel (and the
  organisations tile's pending-binds card) rendered exposed endpoints
  with just a provider picker — no way to enter the hostname/zone/port,
  so "publish" always died with a silent 400. Expose rows now carry the
  route editor (host or zone for http, listen for stream), the button
  stays disabled until the route is filled, and every server refusal
  renders inline. The organisations tile gained a **wiring & ingress**
  card: pending slots for org tiles with the full editor, active
  bindings with their routes and one-click unbind — publishing through
  your org's own terminator needs no allowance (D41); host ports and
  the builtin listener still do.

- **BREAKING — transfers grew a preview, side effects, and a tighter
  receive rule (D39).** ([migration note](changes/2026-08-02-transfer-create-bound.md))
  Every transfer surface (organisations tile, new admin-console owner
  editor, `bx owner --transfer`) now shows an impact report before the
  confirm: your own post-transfer access level, bindings/grants that die
  under the new owner's ceilings, and approval-plane changes. The
  transfer then unbinds fully-dead binding slots and restarts affected
  backends, so a tile moved into a net-denying org loses egress NOW, not
  at some future restart. Receiving a tile INTO an org now requires that
  org's **Create** knob (previously any member could) — transferring in
  is creating, capability-wise. The shell's right-click "Create a new
  tile" dialog gained the owner picker (me / your create-orgs /
  workspace for admins).

- **Org screens, a first-screen editor, sidebar that knows whose tile is
  whose, and self-service passwords (D37/D38).** The sidebar now groups
  tiles into *mine / each org / workspace* with an org filter (tree view
  preserved inside groups). Ws-admins can save any screen as the
  **workspace default** every new user starts from — no more hand-editing
  root/index.html — and org admins can share screens to their org: **org
  screens** appear as tabs for every member, with an edit knob choosing
  who may rearrange (admins / write-level / all members; everyone else
  views read-only). Users change their own password from the new
  my-account section (POST /account/password), and org admins can reset a
  non-admin member's password by re-minting their invite link — the
  forgotten-kid-password case no longer needs the workspace admin. Admin
  console polish: successes render green (not in the error slot), the
  rotated owner token and invite links land in copy-fields instead of
  prompt() dialogs, and secrets/resets use inline forms. The organisations
  tile gained org-admin deep visibility: ceiling rows (read-only),
  member presets, per-member override badges ("clamps org level" /
  "excluded"), and org-screen management.

- **Re-review fix batch (incl. one security fix).** A second five-story
  review of the shipped model confirmed every prior finding fixed and
  surfaced a short list, all addressed:
  - *SECURITY:* per-tile read RBAC now binds **element principals** on the
    `/c/` static plane — previously any tile's frame token could read every
    other tile's source/manifest regardless of the driving user's access
    (pre-existing, not from the recent waves). Elements read their own tile
    always; beyond it the attributed user's access decides; unattributed
    backend tokens are self-only.
  - Provider-org admins can now actually **withdraw** a consumer's binding
    to their tile (DELETE carried no refs, so the documented D33 withdraw
    lever never matched; the stored binding decides now).
  - Allowance hostname globs cover the apex: `net:internet:*.stripe.com`
    also matches `stripe.com` (the TLS-wildcard footgun pushed admins back
    to unfiltered internet).
  - Request dismissals stick: a manager's dismissal starts a 24h re-file
    cooldown, and users excluded by an exact `none` entry get told so
    instead of re-filing forever. Withdrawing your own request stays free.
  - Pending-request hints now include the self-serve detour
    (`transfer:org:<id>`) when moving a personal tile into your org would
    put it under the org's allowance.
  - `bx doctor`: recognizes D35 `net:internet:<spec>` allowances (was a
    false positive), treats suspended org admins as absent, and flags exact
    entries that clamp below a member's org level (stale approvals). The
    access map renders exact `none` exclusions instead of flattening them
    into "never granted"; disabled users no longer show "invited".

- **Disable, suspend, ask-for-access, and hostname-granular egress
  (D34-D36).** The second wave of review follow-ups, all additive:
  - *Account disable (D34):* `bx user set <id> --disable` (admin tile
    toggle) pauses an account — login, sessions, live terminals and invite
    links all refuse — while keeping every grant, membership and owned tile
    for `--enable`. Lockout-guarded. Org admins get the org-scoped little
    sibling: a member's **suspended** knob pauses one membership (confers
    nothing, stays listed).
  - *Ask for access (D36):* navigating to a tile you can't read now shows a
    request-access page (owner named, one click) instead of a bare 403;
    `bx access <tile> request [level]` does the same from a shell. The
    tile's owner / org admins see the queue in the organisations tile (and
    `bx access <tile>`), approve into an exact entry or dismiss; requesters
    can withdraw. The ⚑ badge counts these too.
  - *Filtered internet egress (D35):* `bx bind apps/x
    net=internet:api.stripe.com:443` restricts a tile's egress to named
    hosts/CIDRs/ports — hostnames enforced by DNS pinning in the egress
    relay (rebinding into the LAN pins nothing). Allowances grant it with
    globs and CIDR containment: `net:internet:*.stripe.com` or a
    `net:lan:10.0.0.0/8` entry that lets org admins carve out any narrower
    subnet. Complex filtering stays in net-provider tiles.
  - Invite links now also return an absolute `inviteLink`, the invite page
    warns when you're already signed in (so you don't burn someone else's
    single-use link), and the login/invite cards no longer clip on small
    phones. A fresh workspace's shell shows a one-time "secure this
    workspace" checklist until the first admin account exists.

- **BREAKING — the ownership model grew teeth: org-governed access, exact
  overrides, provider-side approvals, backend-only secrets, user
  attribution.** ([migration note](changes/2026-08-02-ownership-fixes.md))
  The fixes from the five-story UX review, in one wave:
  - *Access resolution (D31):* an org-owned tile is governed by the org —
    personal pattern grants and workspace defaults no longer leak into org
    tiles; an **exact per-user entry is authoritative** (override down, or
    `none` to exclude one member from one tile).
  - *Create-as-org actually works:* a member with the org's Create knob can
    create org-owned tiles with no personal canCreate pattern (the D25
    promise, previously dead code) — manager tile, organisations tile, `bx
    new --owner org:<id>`, raw POST /create.
  - *Provider-side consent + nobody is blind (D33):* admins of the org that
    owns a tile now SEE its consumers and may approve/withdraw grants and
    bindings targeting their property; requesters see their own pending
    requests with who-can-approve hints; the shell ⚑ badge counts real
    pending approvals and updates live (new-pending events); grant rows
    record who approved (`approvedBy`).
  - *Allowance granularity (D32):* `tile:<pat>@<role>` / `res:<glob>@<role>`
    cap the delegable role; `iface:<svc>@<tile>#<instance>` pins a provider
    and instance (share the dev instance, not prod); entries validate per
    class at write time instead of storing dead strings.
  - *Vault (D30, BREAKING):* secret VALUES are readable by the tile's
    backend only — admins and tile terminals list/set/rotate but never
    read; tile frontends can't reach the vault API at all.
  - *User attribution (D29):* backends receive `X-XBin-User` +
    `X-XBin-User-Level` for the driving human; SDK `Caller(r)` carries
    `User`/`UserLevel` + `UserCanWrite()` so tiles can gate in-app.
  - Also: lifecycle is now the owner's (org admins can stop their runaway
    tile), org admins can read their org's policy rows, deleting a user
    reports the tiles that fell to workspace-owned, `bx doctor` learned
    ownership checks, and `bx new --team` (dead teams-era flag) became
    `--owner`.

- **Invite links: onboard users without sharing passwords (D22).** Create a
  user with no password (admin tile, `bx user add --invite`, or plain
  `POST /users`) and you get a **single-use, 72h invite link** to send them —
  they set their own password on a themed page and are signed in.
  `bx user invite <id>` / the users-tab **invite** button re-mint (and
  invalidate) links for existing users — credential delivery and
  reset-by-link in one. Tokens are hashed at rest and redemption is
  login-throttled. **No self-signup**: accounts still only come from admins.
  The login page's stale "one-time token URL" note was fixed too.

- **The ownership UX: organisations tile, admin-console rework, owner
  pickers.** A new pre-installed **`tiles/organisations`** tile is the
  delegated surface: members see their orgs, owned tiles and sharing; org
  admins manage members, org-tile ACLs/transfers, and one-click-approve the
  pending grants/bindings their allowance covers (with the resolved allowance
  shown). The admin console's user-management group is now *users ·
  organisations · permission sets · access map* — member role editors with
  Admin/Developer/Viewer presets, a permission-set editor, an allowance/owned-
  tiles view per org, a workspace **defaults** editor, and owner-based
  provenance in the access map. The Tile Manager's create form gained an
  **Owner** picker (me / orgs where you may create); `/components` now carries
  each tile's owner; the shell's ⚑ button opens the organisations tile (the
  old org popover is gone) and first-screen seeding is filtered to tiles the
  user can read. New `bx owner`, `bx permset`, and reworked `bx org
  member/sets/allow` commands; a fresh workspace seeds `defaultTiles`
  (welcome/apidocs/organisations → read).

- **BREAKING: ownership replaces teams & positional org paths.** Components
  now have an **owner** (a user or an org, transferable) recorded outside the
  workspace; org membership is a flat `{level, create, admin}` role applied
  org-wide to org-owned tiles; sharing a tile is an ownership right (exact
  `user:`/`org:` ACL entries); workspace admins can delegate grant/binding
  approval to org admins via **allowances** and reusable **permission sets**
  (`cap:containers`, `net:*`, `gpu:*`, ingress publication — everything except
  the `xbin` capability family); `defaultTiles` gives every user baseline
  visibility. Teams, `basePermission` and the `o/<org>/` path convention are
  removed — no released workspace used them, so there is no data migration.
  See [changes/2026-08-02-ownership.md](changes/2026-08-02-ownership.md),
  docs/protocol.md, and plans/ownership.md (D24–D28).

## 2026-08-01

- **`bx` works from the host without fiddling with tokens.** Run on the host
  (not inside a terminal), `bx` now auto-reads the workspace **owner token** from
  `.xbin/token` — locating the workspace via `XBIN_WORKSPACE`, a walk-up from the
  cwd, or the default `/opt/xbin/workspace` — so `sudo -u xbin bx ls` just works.
  A non-privileged user still can't read the 0600 token (unchanged). When no
  token is found, the 401 now explains how to fix it instead of only "sign in at
  /login". (`bx term`/`bx login` were never commands — bx prints usage for
  unknown ones.)

- **Sidebar & tabs: filter, nested folders, reorderable + parkable tabs.** The
  component tree gains a **filter box** (matches tile and tab names, auto-expands
  folders). **Folders nest** — drop a folder onto another to nest it, onto empty
  space to un-nest. **Screen tabs drag to reorder**, and can be **dropped into a
  folder** to park them in the tree: the tree entry is the *live* screen (not a
  snapshot), so closing its tab keeps the layout and clicking it restores the
  screen exactly. Parked screens leave the tab bar until reopened.

- **Mobile workspace mode.** The shell now adapts below 820px: the sidebar
  becomes an off-canvas drawer (tap ☰), tiles stack full-width instead of the
  mouse-driven snap-grid (drag/resize off on touch), and terminals and floating
  windows open as full-screen sheets. Desktop is unchanged. Tile authors: your
  tile becomes a full-width card on phones (its own height, content scrolls
  inside) — make sure it's usable narrow (see workspace `AGENTS.md`).

## 2026-07-30

- **Tiles can report status & notifications to the workspace.** A new
  self-scoped channel lets a component surface its condition: `xbin.status(level,
  message)` / `xbin.clearStatus()` (frontend) and `xbin.Status` / `xbin.ClearStatus`
  / `xbin.Notify` (Go SDK), levels `ok|info|warn|error`. The shell renders it as a
  breathing dot on the tile's sidebar entry (and its folder), a tint on the
  screen tab holding an affected tile, and a mark in the browser-tab title;
  `xbin.notify` raises a one-shot toast. Status is **persistent and
  self-clearing** (set `ok` to clear) and **resets when the backend restarts**.
  New `GET/POST /api/xbin/tile-report` + a `status` event on `/ws/events`
  ([protocol.md](protocol.md), [elements.md](elements.md)). Guidelines — when to
  use each level and the always-clear-it rule — are in the workspace `AGENTS.md`.

- **Code browser: line numbers, change counts, an Analysis tab, and live
  refresh.** The terminal's code panel (`bx-code`) now shows **line numbers** in
  the file view, a **change-count summary** (`+add −del · N files`) on the
  working-tree/commit diff and per-commit in the Changes list, and a new
  **Analysis** tab charting commit activity over time (commits/week for the last
  year, top authors, totals) — including the **upstream** tracking branch when a
  component was git-imported. The file/changes views also **refresh
  automatically** when a tile's files change on disk (agent or terminal edits),
  instead of showing stale content. `GET /git/log` now returns `add`/`del`/
  `files` per commit and there's a new `GET /git/activity` endpoint
  ([protocol.md](protocol.md)).

## 2026-07-14

- **New capability: `cap:containers` — run containers inside a tile**
  ([plans/containers.md](../plans/containers.md)). A **container-host tile**
  (rootless Podman/Docker spawning sub-containers — the substrate for "dev
  sandbox" tiles) declares `uses: [{target:"cap:containers", role:"writer"}]`.
  It's an **admin-only** reserved grant (lands pending on import) that keeps the
  tile's user-namespace capabilities and swaps the backend seccomp block-list
  for a **minimal floor** (only host-damaging syscalls — module/kexec/reboot/
  swap/clock), so the mount family / `pivot_root` / `setns` a container runtime
  needs are available. Still fully rootless and namespaced — no host reach, no
  other-tile reach; the policy `xbin-caps` deny strips it. The tile supplies the
  runtime itself (Podman + subuid seeding + storage + networking, in its
  `setup`/manifest). Worked example: the new **devbox** builtin tile. See
  [changes/2026-07-14-container-tiles.md](changes/2026-07-14-container-tiles.md).

## 2026-07-12

- **Fix: disk quotas now count encrypted resources.** On an encrypted
  workspace (the production default) a `filesystem`/`sqlite`/`blob` resource's
  bytes are ciphertext under `data/resources-enc/…`, but the per-scope quota
  measurement (and the admin "resources" size view) only scanned the plaintext
  `data/resources/…` tree — which is an empty mountpoint once encrypted. So
  those resources counted **~0** toward a scope's quota and the low-disk
  block/alert never fired for the file plane. Both now measure the ciphertext
  footprint (summed with the plaintext tree for unencrypted / mid-migration
  scopes). `kv` was always counted correctly.
- **Security: `owner` is now a reserved component path.** A top-level
  component literally named `owner` would have made the proxy inject
  `X-XBin-From: owner`, so a callee's SDK (`Caller().Owner`) would treat it as
  the human owner — an impersonation across the identity spine. It joins
  `ingress`/`runtime` as a reserved top-level name (creating one is refused;
  an existing such dir stops being scanned as a component). The element proxy
  also now strips **every** inbound `X-XBin-*` header before injecting the
  verified identity (previously it enumerated three), so a caller can't feed a
  backend a spoofed `X-XBin-Ingress-Host` on the authenticated `/api` path.
  Low real-world likelihood (naming a top-level `owner` needs admin/top-level
  create rights), but a latent contract break — now closed.
- **Admin console: two-level navigation + filtering, built for large
  deployments.** The flat tab row is now grouped — **runtime**
  (components · resources · backup · cron), **user management** (users ·
  organisations · teams · access map), **vault**, **binding** (roles ·
  grants · interface providers · binding), **ingress** (endpoints ·
  services / expose). Every list view gained a live text filter (and
  scope/org category chips on the component + team lists) so it scales to
  thousands of tiles. The component list now merges manifest data with live
  backend state and expands to show **who can reach each tile** (its access
  relations) alongside the backend's runtime detail. Ingress **publishing is
  now a first-class surface** (ingress → services / expose) with the
  live routing table under ingress → endpoints. Old hash deep-links
  (`#overview`, `#runtime`, `#interfaces`) redirect to their new homes.
- **New: a top-down system overview** at [/docs/overview/](/docs/overview/00-index.md)
  (start at `00-index.md`). 17 short chapters walking the whole architecture —
  the core model, workspace anatomy, components & the backend lifecycle, the
  frontend & shell, identity & authorization, users/orgs/teams, the backend
  sandbox and the terminal plane, resources & the vault, interfaces & bindings,
  egress & net-provider tiles, ingress, tile lifecycle, deployment/operations,
  and extending xbin — explaining how the subsystems compose and why. The
  existing reference docs stay the field-level truth; the overview is the map
  that puts them in context. Served at `/docs/overview/` and on disk in every
  terminal (`$XBIN_DOCS/overview/`).
- **Fix: terminal read guard blocked the SDK, and workspace-root files like
  `AGENTS.md`.** Two bugs in the Landlock read guard, both surfacing as
  `Permission denied` on world-readable files in tile terminals (while `ls`
  worked, since only file *reads* are restricted):
  1. On the standard nested layout (`/opt/xbin/workspace`) the guard skipped
     the workspace's whole top-level path component, so nothing else under
     `/opt` was readable — `cat /opt/xbin/sdk/xbin.go` failed and backend
     `go build` couldn't read the SDK. The guard now grants siblings level by
     level down to the workspace root, and terminals' explicit read-only
     mounts (the SDK bind) are always allowed.
  2. Files granted directly at the workspace root (`AGENTS.md`, `go.work`,
     the `CLAUDE.md` symlink) were read-blocked because the grant carried
     `LANDLOCK_ACCESS_FS_REFER`, which the kernel rejects on a non-directory —
     the rule was silently dropped. A file is now granted read-only without
     `REFER` (directories keep it, so `apt`'s cross-directory renames still
     work). Workspaces at a top-level path (`/workspace`) were unaffected by
     (1); (2) affected every layout. The deny set (`.xbin/`, `data/`, other
     users' `homes/`) is unchanged. Restart xbind and reopen the terminal —
     the guard is per-process and inherited, so live sessions keep the old one.
- **Ingress — publishing tiles** ([docs/ingress.md](/docs/ingress.md)): tiles
  can now be reached from OUTSIDE the workspace, deliberately and
  owner-gated. A new manifest section `exposes` declares endpoints —
  `{kind:"http", paths:[…]}` (a hostname-routed site/API with a **public
  path allowlist**, default-deny) or `{kind:"stream", proto, port}` (the
  backend just `net.Listen`s; xbind relays a host port in, TCP or UDP).
  Declaring is inert: the **owner binds** each slot to an ingress source
  (`bx expose <tile> <slot>=<source> --host/--zone/--listen`, or admin →
  interfaces → ingress), exactly like interface bindings — the binding
  carries the route. Public traffic reaches the one bound tile as the new
  anonymous **`ingress` principal** (`X-XBin-From: ingress`, SDK
  `Caller(r).Ingress()`, public host in `X-XBin-Ingress-Host`), confined to
  the declared paths, with no reach into `/api/xbin/*` or sibling tiles.
  Sources: **`runtime`** — xbind's own second listener (`xbind
  --ingress-listen`, BYO TLS via `--ingress-cert/-key`) + host-port stream
  relays — or an **ingress terminator tile**: the new **Public HTTPS
  (Traefik)** builtin does automatic Let's Encrypt TLS in a sandboxed tile
  (certs in its own resource, never in the daemon). Also: delegated
  wildcard **zones** with tile self-registration bounded to the zone (`PUT
  /api/xbin/ingress-hosts`), direct tile→tile TCP via `{kind:"stream"}`
  interfaces (`bx bind app db=apps/postgres#pg` → `XBIN_IFACE_DB_ADDR`),
  `{kind:"lan-ingress"}` links for VPN/router-tile inbound, split-horizon
  hairpin (a tile using its own public URL routes straight back), a new
  policy-ceiling deny kind **`ingress`**, `bx ingress` + an admin ingress
  panel, and `GET /api/xbin/ingress[-routes]`. Host ports <1024 need
  `AmbientCapabilities=CAP_NET_BIND_SERVICE` on the xbind unit. Additive —
  nothing is published until you bind it.
- **BREAKING (net-provider tiles): `cap:net-admin` grant required.** A
  regression on 2026-07-10 (making every tile backend fully unprivileged)
  broke **net-provider tiles** — routers/firewalls that splice other tiles'
  egress (the `egress-approver` builtin, `examples/netrouter`): building their
  dataplane needs network-admin capabilities the sandbox now drops, so they
  failed at startup with `operation not permitted` (ip_forward / ip route /
  AF_PACKET). A provider now declares `uses {target:"cap:net-admin",
  role:"writer"}` — an **admin-only** reserved grant that makes the sandbox
  keep CAP_NET_ADMIN / CAP_NET_RAW / CAP_NET_BIND_SERVICE **inside the tile's
  own network namespace** (nothing reaches the host; every other cap still
  dropped, seccomp block-list unchanged). The shipped provider tiles declare
  it; approve it once in the grants panel (it lands pending). A workspace/org
  policy `net` deny strips it. See
  [changes/2026-07-12-net-provider-cap.md](changes/2026-07-12-net-provider-cap.md).
- Terminal window: a **read-only "logs" tab** (the ▤ button next to `>_`
  `{ }` `⇋`) streams the tile backend's captured stdout/stderr live —
  rendered in an xterm view for ANSI colors + scrollback, no input. It's
  gated exactly like the tile's terminal (admin, the tile itself, or a
  **terminal-level** user — read/write users don't get it, since backend
  output can carry secrets), so it only appears where a shell would. Backed
  by `GET /api/xbin/logs?component=<p>[&tail=<bytes>][&follow=1]` (text/plain
  tail + chunked follow; the HTTP twin of `bx logs -f`) — see protocol.md.

## 2026-07-11

- **Organizations & teams** (docs/auth.md → "Organizations & teams"): GitHub-
  style grouping on top of users. Orgs own the `o/<org>` path namespace
  (tiles at `apps/o/<org>/…`); teams grant tile access to members by union
  (effective level = max of own entries, team entries inside the org, org
  base permission); tiles can be created *in a team* (`POST /create
  {team:"<org>/<team>"}`, `bx new --team`, the manager tile's picker — the
  team is auto-granted its `newTiles` level); per-tile access is viewable/
  editable at `GET/PUT /api/xbin/access` (`bx access`, or the shell's
  per-tile ⚙ → access, which now also opens for org admins). **Org policy
  ceilings** (`/api/xbin/policy`, `/orgs/<org>/policy`, `bx org policy`, or
  the admin tile's row editor): pattern-keyed rows `{tiles,
  deny[net|gpu|xbin-caps], mayCall[]}` capping what the covered tiles may
  be granted — enforced at approval *and* at every evaluation, so
  hand-edited grants/bindings under a ceiling are inert (`mayCall` governs
  external reach only: a tile's own scope is always exempt, and capability
  targets are classified — bare `code` sits under the `xbin-caps` deny
  class, `code:<comp>` is governed like calling that component — so a path
  allow-list never silently strips `code:reader` source access; unapprovable
  pending requests are annotated `blocked`). Delegated **org admins**
  manage their org's teams/members/access from the shell's "orgs & teams"
  popover and per-tile ⚙ (workspace-security knobs — policy, term flags,
  org create/delete — stay workspace-admin); the sidebar groups org tiles
  under `o/<org>`. New API: `/api/xbin/orgs*`, `/access`, `/policy`;
  `whoami` gains `orgs` and, on element principals, the attributed driving
  `user` — scoped by tile trust (identity only; +own-org slice for org
  tiles; full list for `xbin`-capable tiles) — all in protocol.md. New CLI:
  `bx org|team|access`. **Tile-creation authority** is now uniform across
  create/clone/git-import/tile-import/template-instantiate: a user's
  `canCreate` patterns work everywhere (previously admin/capability-only
  for the copy/import routes), copy-shaped routes need read on the source,
  creating inside an org needs membership, and an element's
  workspace-management grant no longer extends the *driving* user's own
  create rights (the confused-deputy clamp). **BREAKING (edge case):** the
  path segments `o` and `u` are now reserved in NEW tile paths — see
  [changes/2026-07-11-orgs-and-teams.md](changes/2026-07-11-orgs-and-teams.md),
  which also covers updating the workspace chrome (`bx builtin update`) to
  get the new shell/admin/manager UI on existing workspaces.
- Admin UI: **permissions are fully click-through** — people are picked from
  chip dropdowns (small workspaces: everything enumerable), tile grants from
  row editors with a datalist of real paths/patterns (inert org patterns get
  a live ⚠), replacing every free-text spec/prompt in the users, orgs, shell
  org popover and ⚙ access surfaces. New **access map** tab in the admin
  tile: a visual org/teams/people structure (policy ceilings marked ⛔) and
  the resolved users × tiles **effective-access matrix** — click any cell
  for the full derivation (which entry/team/base/adminship contributes and
  which wins). Backed by `GET /api/xbin/access-matrix` (resolved server-side
  with provenance) and `GET /api/xbin/users-directory` (identity-only people
  list, reachable by org admins for their pickers) — both in protocol.md.
- terminal: **fixed multi-tab terminals rendering stacked in the first tab after
  a reload**, and **added per-tab close buttons**. On restore, every saved
  terminal reattached at once and each forced itself visible (`bx-terminal`'s
  `connectedCallback` set an inline `display:block` that overrode the host's
  `display:none` for inactive tabs), so they piled up in a vertical split until
  each tab was clicked. The element no longer sets that inline display (the
  `:host` rule is the standalone default). Tabs now carry an ✕ to close one
  (the whole window still has its own close), and typing `exit` closes just
  that tab — closing the last one closes the window. (web/bx-terminal.js,
  web/bx-frame.js.)
- terminal: **`apt install` of packages that create system users fixed** (was
  failing with `chown … Invalid argument` mid-configure on systemd/dbus/etc.,
  while simple packages installed fine). Two parts:
  - **Bug:** `/etc/subgid` is keyed by the *user*, but xbind looked it up by
    *gid* — so for any account whose uid ≠ gid (a `useradd --system` user like
    `xbin` at uid 999 / gid 988), a correctly-delegated sub-gid range was never
    found and the sandbox silently fell back to **single-uid mode**, where only
    container-root is mapped and dpkg's chown-to-system-user fails with EINVAL.
    Fixed to match both sub-id files by the user (name/uid).
  - **Diagnosis:** xbind now logs the uid-mapping mode at startup and warns
    loudly on the single-uid fallback (naming what's missing); `bx doctor`
    flags it from inside a terminal (`/proc/self/uid_map`). If the warning
    persists after upgrading, the host genuinely lacks the delegation — add the
    user to `/etc/subuid` + `/etc/subgid` and install `uidmap`
    (`deploy/install.sh` does both), then restart.
- terminal: **mouse/selection no longer drifts when the workspace font size is
  changed.** The workspace scales via CSS `zoom`, but xterm measures its cell
  size on a canvas (which ignores an ancestor's zoom) while reading pointer
  coords that honor it — so clicks, right-click, and text selection landed off
  by the zoom factor, worse the further from the terminal's top-left. The
  terminal now detects the ambient zoom (walking its ancestors' computed
  `zoom`, across shadow-DOM boundaries), counters it so xterm renders at
  net-zoom-1 (exact coordinate math), and re-applies the scale through xterm's
  own font size — same visual size, correct mouse. Ships with xbind; works even
  on a workspace whose shell predates this change. (web/bx-terminal.js.)
- terminal: **fixed terminals failing to start** (`exit 127` at spawn) on any
  workspace with encrypted resources. The read-only workspace bind had been
  made *non-recursive* to hide other tiles' resource (resenc) mounts from
  `mount`; but in a rootless user namespace those inherited mounts are locked,
  and the kernel rejects a non-recursive bind of a subtree with locked
  children (`EINVAL`) — the exact mirror of not being able to unmount them.
  Reverted to a recursive bind. Resource *contents* stay masked; their mount
  names may reappear in `mount` (benign — a terminal can already `ls` every
  tile; docs/isolation.md). Ships with xbind.
- terminal/sandbox: a terminal that dies during sandbox init now surfaces the
  reason instead of a blank pane — the failing pane replays the init's error,
  the daemon logs a sanitized tail of it (`journalctl -u xbin`), and
  `XBIN_SANDBOX_DEBUG=1` adds a per-step init trace. (Diagnostics; no
  behavior change.)
- users: **BREAKING — per-tile access tiers.** A user's `tiles` is now a
  `{path: level}` map with levels `read < write < terminal`, plus `canCreate`
  (path patterns the user may scaffold tiles under; creating auto-grants them
  terminal on it) and `termApi`/`termNet` (whether a non-admin's terminals
  get the live tile-API token / internet egress — both default off, `net=host`
  stays admin-only). `users.json` and legacy API bodies migrate automatically
  (array entries → `write`, `terminal: true` → `terminal`); API *responses*
  return the new shape, so update your workspace's admin tile. `bx user` flags
  changed accordingly (`--tiles a=terminal,b`, `--create`, `--term-api`,
  `--term-net`). Admin/owner behavior is unchanged. Migration note:
  [changes/2026-07-11-tile-access-tiers.md](/docs/changes/2026-07-11-tile-access-tiers.md);
  model: [auth.md](/docs/auth.md).
- terminal: **non-admin terminals are locked down by default**
  (isolation.md): source of tiles below the user's `read` level is masked out
  of the mount; no tile-API token without the `termApi` grant; no egress
  without `termNet` (query params are clamped, the session still opens); and,
  under cgroup delegation, each restricted session gets the same
  memory/pids/CPU caps as a tile backend. Admin terminals unchanged.
- xbind: terminal session logs now include spawn failures (ERROR + cwd) and
  exit status + uptime on session end, so a shell that dies at start is
  diagnosable from the server log.
- terminal: **`apt` fully fixed** (`apt update` + `apt install`) — the real
  cause of the `rename … Invalid cross-device link` was **not** fuse-overlayfs
  but the terminal's Landlock read guard: on an ABI-2+ kernel, enforcing any
  Landlock ruleset denies *reparenting* (cross-directory rename/link) with
  EXDEV unless the ruleset handles `LANDLOCK_ACCESS_FS_REFER`. The read guard
  handled only file-reads, so apt's `partial/ → parent` rename failed on every
  filesystem. The guard now handles + grants REFER on the same paths it already
  allows reading, so cross-directory renames work while secret reads stay
  denied. Ships with xbind — **no `make rootfs` needed** (the earlier apt
  working-dir relocation was treating the wrong cause and is reverted in the
  next base image, which also adds `strace`). (docs/isolation.md.)
- terminal: **restricted tier for non-admin users** (isolation.md). A shell
  opened by a non-admin user drops `CAP_SYS_ADMIN`/`CAP_SYS_RESOURCE` (+ other
  privileged caps) but keeps the file caps `apt` needs, and its user namespace
  is pinned so no nested user/mount namespace can be created — so it can't
  regain privilege via `unshare -Ur` or mount over its masks. Admin/owner
  terminals are unchanged (full caps for dev work). Ships with xbind; dormant
  until non-admin users exist. (plans/DECISIONS.md D18.)

## 2026-07-10

- terminal: **resource mounts no longer leak in `mount`.** A tile terminal
  binds the workspace read-only+recursive, which cloned in every tile's
  gocryptfs resource (resenc) mount; their contents were already masked, but
  `mount`/mountinfo still listed other tiles by resource name. The terminal
  now detaches those submounts before masking `.xbin`/`data`. Safe by
  construction (both dirs are fully masked; the sandbox root is
  MS_REC|MS_PRIVATE, so the host's live mounts are untouched). No rebuild —
  ships with xbind.
- terminal: **`apt install` fixed** (`rename … Invalid cross-device link`)
  via apt config only — `Dir::Cache::Archives` moves the download cache to
  `/var/cache/xbin-apt`, a path absent from the base image, so at runtime it
  lives entirely in the writable overlay upper and apt's partial/ → archives/
  rename never crosses layers. No overlay-mount option (an earlier
  `redirect_dir=on` attempt on the shared overlay broke component backends'
  state and was reverted). Needs a `make rootfs` rebuild.
- terminal: fixed **doubled keystroke echo after a base-image upgrade / sandbox
  reset**. Resetting kills the session server-side then reconnects; the killed
  socket's onclose could still schedule a reconnect that reattached to the new
  session, leaving two sockets writing to one terminal (every byte, incl. the
  echo of what you typed, twice). A connection epoch now ensures only the
  latest socket drives the terminal or reconnects — also closing the same
  latent race on network/GPU/API-scope switches.
- base rootfs: added the tools agents reach for — full `vim`, OpenAI `codex`
  CLI, `chromium`+Playwright (system browser path), `gh`, `fd`/`bat`/`shellcheck`,
  Go `gopls`/`dlv`/`golangci-lint`, and `pnpm`/`yarn` — so a fresh
  terminal doesn't re-install them. (docker/rootfs.Dockerfile; `make rootfs`.)
- terminal: **`bx status`** now shows the current tile's runtime metrics
  (backend state/cpu/mem/pids/fds/conns/egress, disk usage vs quota, alerts)
  via the new read-only `GET /api/xbin/tile-status?component=` — readable with
  the tile-scoped terminal token (self), or any tile for admins. `--all` keeps
  the admin global view.
- terminal pop-up: a **code browser / git-review panel** (`bx-code`) beside
  the terminal. A collapsible file tree + syntax-highlighted viewer (vendored
  highlight.js, no build step) and a **Changes** tab showing the working-tree
  diff and per-commit diffs for review. Title-bar layout switch: terminal /
  code / resizable split. Read-only (editing stays the terminal's job);
  backed by the existing grant-gated /api/xbin/code/* + /git/* endpoints.
- **resource limits (blast-radius containment for shared/mixed-team
  workspaces).** Per-tile cgroup caps are now *enforced*, not just measured:
  memory.max 2 GiB (+ memory.high), pids.max max(512, ncpu×8), cpu.weight
  fair-share (burst when idle). Per-scope **disk quota** 50 GiB with `507` on
  kv/blob writes over it, plus low-disk (<10% free) write-blocking of the
  biggest users. Backends now run **capability-dropped + seccomp block-list**
  (mount/module/kexec/reboot/ptrace/bpf/keyrings/…). Terminals capped at 32
  per user. **Alerts** (`GET /api/xbin/alerts`) surface at-limit / low-disk /
  blocking events as a banner in the workspace shell and the admin console.
  Tunable via `XBIN_LIMIT_MEM` / `XBIN_LIMIT_DISK`. Details: docs/isolation.md
  §resource limits.
- terminals: **workspace secrets are masked from tile terminals** (Gap 0).
  The isolated terminal's read-only workspace mount now covers `.xbin/`
  (owner token + frame secret), `data/` (vault, encrypted resource state,
  password hashes), and other users' `homes/` with an empty overlay — so a
  shell (or an agent in it) can read every tile's *source* for API work but
  can no longer `cat .xbin/token` to re-grant owner, which had undercut the
  tile-scoped terminal token. Applies to every terminal, including your own
  tiles. Own tile dir + `$HOME` stay read-write. New per-terminal **tile-API
  toggle** (titlebar, default on): off mints no token — the shell sees code
  but every API call is unauthorized. Isolation property of `--isolate`;
  see docs/isolation.md for the honest bound.
- terminals: **seccomp mount guard** makes those masks umount-proof. A tile
  terminal shell keeps `CAP_SYS_ADMIN` (apt / nested namespaces / profiling
  still work) but a seccomp filter — installed before the shell, inherited
  across execve/unshare — denies `umount2`, `move_mount`, `open_tree`, and
  `mount(MS_MOVE)`, the four ways to remove or relocate a mask. So a shell
  that is root in its user namespace still can't peel a mask off to read the
  owner token. Collateral: `fusermount` and nested container/browser
  sandboxes that detach their old root get `EPERM` (run them on the host).
- terminals: **Landlock read guard** — a second layer that denies reading
  the secret *files* (`.xbin`/`data`/other `homes/`) at the VFS level, so even
  if a mask were peeled the owner token / vault / password hashes / other
  agents' credentials still can't be opened. seccomp can't filter by path
  (it can't deref the `open` arg); Landlock can. Restricts only READ_FILE, so
  exec/readdir/writes are untouched (collateral nil); best-effort where
  Landlock is unavailable. The admin **runtime** tab now shows each guard's
  kernel support (*terminal guard: mount ✓ · read ✓ (ABI n)*).

## 2026-07-09

- auth: **server-side session expiry** — browser logins now die after 12 h
  idle (sliding) or 30 days absolute, whichever first, so a stolen session
  cookie can't authenticate indefinitely (previously: valid until logout or
  restart). Tunable via `XBIN_SESSION_IDLE_TTL` / `XBIN_SESSION_MAX_TTL`.
- auth: **audit log** — every mutating core-API call (`POST/PUT/PATCH/DELETE`
  on `/api/xbin/…`, minus the `prefs`/`kv`/`blob`/`bus` data plane) logs an `audit` line
  with actor + path + status; a who-changed-what trail for governance actions
  (users, grants, lifecycle, vault, token rotation).
- users: **password floor** — accounts created/updated through the API need
  an 8-character minimum (enforced at the API so the dev admin/tests, which
  write the store directly, are unaffected); the admin UI surfaces create/
  reset errors instead of silently clearing the form.
- users: **user ids are now validated at creation and immutable** (D15) —
  `[a-z0-9][a-z0-9._-]{0,31}`, `owner` reserved. An id is the permanent key
  for `homes/<user>`, the prefs bucket, and `user:<id>` attribution, so it
  can't be renamed or contain path/attribution separators; the constraint is
  set before GA because it can't be tightened once ids exist on disk.
  Existing users (loaded from disk) are unaffected — only new creates are
  gated.
- auth: **owner-token rotation** — `POST /api/xbin/auth-rotate-token` (admin;
  a button under the admin console's Users → sign-in security) rewrites
  `.xbin/token` and swaps it live: the old token stops authenticating
  immediately, bearer and cookie alike. **Rotate once after upgrading to
  terminal-scoped tokens** — before 2026-07-09 every terminal carried the
  owner token, so old agent transcripts/shell histories under `homes/` may
  hold it. (Terminal tokens themselves already revoke automatically: each
  dies with its session; deleting a user kills theirs instantly.)
- shell: tiles' **title bars gain a `>_` terminal button** (replacing the
  tiny 7×7 corner square on shell cards; standalone frames keep the corner
  button), and a **workspace settings** menu (🔧 in the top bar) with a
  per-user **global font size** (persisted via prefs, applied as a clean
  zoom). The terminal settings icon is now a wrench (🔧) instead of a gear.
- **connection-leak fix, both sides of the proxy.** xbind built a fresh HTTP
  transport per proxied request, stranding one keep-alive connection per RPC
  — on the backend that parked a goroutine + ~15 KB forever (the SDK server
  had no idle timeout), and xbind leaked the matching client conn. The proxy
  now pools one transport per backend socket (reused across requests, idle
  conns reaped at 90s, evicted when a generation's socket goes away), and
  `xbin.Serve` sets `IdleTimeout: 120s` + `ReadHeaderTimeout: 30s` (hijacked
  WebSocket conns exempt; no blanket read/write timeouts — SSE/streams still
  run indefinitely). xbind's own TCP + gateway listeners got the same
  timeouts. Backends pick up the SDK fix on their next rebuild — every spawn
  rebuilds, so an xbind upgrade + restart covers the fleet.
- terminals: **tile-scoped tokens — the owner token no longer enters any
  terminal.** A terminal's `XBIN_TOKEN` is now a per-session token resolving to
  the TILE the terminal is opened on (its element principal: admin of itself +
  its approved grants/bindings — the frame-token model, for shells), never the
  human's privilege. Agents in tile terminals can no longer read other tiles'
  admin config, call `/api/xbin` admin endpoints, or **self-approve grants**
  (now enforced, not just documented). Session-open gates: terminals only on
  tiles the user may use; **the root terminal is disabled** (workspace-wide
  work: the browser UI, or the host shell — the owner token lives only in
  `.xbin/token`). Kill/reattach of another user's session: admins only.
  Deleting a user kills their live shells' API access. **Breaking** for
  workflows that ran admin `bx` from inside tile terminals — migration:
  `docs/changes/2026-07-09-terminal-scoped-tokens.md`.
- terminals: **$HOME is now per user** — `homes/<user>` instead of one shared
  `home/`, so each human's agent-CLI config (`~/.claude`, credentials), shell
  history, and dotfiles stay their own (the root token uses `homes/owner`;
  seeded lazily on first terminal; a non-admin can't attach to another user's
  session). A legacy shared `home/` **migrates automatically at startup** to
  the workspace's sole user/admin (else `homes/owner`) — xbind refuses to
  start only when both forms hold real data, so nothing is merged by guesswork.
  Migration note: `docs/changes/2026-07-09-per-user-homes.md`.
- terminals: **base-image versioning + safe upgrades.** A terminal's
  persistent sandbox layer (apt installs / system changes) is now stamped with
  the base image it was built on and **pinned** to it — xbind never stacks that
  overlay on a different base (which corrupts apt/dpkg state). When a newer
  base is installed the terminal tile shows a **base-update** banner; clicking
  it (or the reset ⟲) rebuilds on the current base — installed packages are
  wiped, your workspace files and `$HOME` are kept. The `/ws/term` `session`
  message carries `baseOutdated`. Base images are stamped by `build-rootfs.sh`;
  `install.sh` preserves the old base as `rootfs-<version>` on upgrade (legacy
  unstamped bases become `rootfs-v0`); xbind aborts startup if a pinned base is
  missing and GCs preserved bases once no terminal pins them. Design:
  `plans/component-env.md`.

## 2026-07-08

- terminal: a **settings menu** (the ⚙ that appears top-right on hover),
  starting with a **theme picker** — Default (dark-steel), Dracula, Nord,
  Solarized Dark/Light, Monokai, Gruvbox Dark, One Dark, Tango Dark, GitHub
  Light — plus a font-size stepper. Applied live, shared across open terminals
  (same document + other tabs), and persisted in the browser.

- lifecycle: **offloading a component now fully quiesces it.** Previously an
  offloaded tile kept firing cron, still surfaced pending access/binding
  requests, and stayed visible in the shell + the admin principals list.
  Now: the cron scheduler skips jobs for non-enabled components; `Pending()`
  and pending-bindings omit offloaded components; the shell hides them (out of
  the sidebar/folders, and any open card is closed on the reload); the admin
  console lists offloaded tiles in a separate section, not the main table.
  `GET /api/xbin/components` now carries each component’s lifecycle `state`.

- interfaces: **fixed a binding-role flap**. A provider with several http
  provides (e.g. llm-gw: `openai`=writer + `metrics`=reader) resolved a
  binding’s granted role from an *arbitrary* provide — provides live in a map
  with no stable order — so a caller bound to `openai` (writer) intermittently
  got `reader`, failing ~15–30% of calls with "role writer required". The
  role/endpoint/validation now select the provide **by the requester slot’s
  service**, deterministically. (Introduced when llm-gw gained its second
  provide; single-provide tiles were never affected.)
- llm-gw: the stats table groups digits with **commas** (`123,142`) instead of
  a thin space, which was hard to read against the column gaps.

- vault: **secret values are now readable only by the element they belong to** —
  not even the owner or an `xbin:admin` tile. Admins can still list keys and
  set/rotate/delete any vault (the admin console + per-tile mini-admin drop the
  "reveal" button and show masked values), but `GET …/vault/<comp>/<key>` is
  self-only, so a compromised admin tile can't exfiltrate secrets. **Breaking**
  for any admin/owner flow that read another element's secret value via the API
  (`bx vault get <other-component>` now 403s); the owning element's own
  `xbin.Secret()` is unaffected. Migration:
  `docs/changes/2026-07-08-vault-read-lockdown.md`; see `docs/auth.md` §Vault.
- shell: the sidebar bottom shows the **running xbind build commit** (`⬡ xbind
  <commit>`). The commit is baked in at build time (`make build` ldflags →
  `git describe`, falling back to the VCS revision Go stamps into the binary)
  and surfaced on `GET /status` as `version`.

## 2026-07-07

- Templates: a **template-instance update path**. Each builtin template is
  served read-only as a git repo (`GET /api/xbin/templates/<name>.git/…`, admin)
  and added to every instance as a `template` remote, so a builder pulls
  upstream fixes with `git fetch template && git merge template/main` (or
  cherry-pick) — in control of what they adopt, no auto-merge clobber. The
  builtin-tile updater still never touches template instances. Phase 7 of the
  agent-v2 program. Terminals now resolve the SDK's `http://xbin/…` gateway host
  for raw `git`/`curl` too (rewritten to `XBIN_URL` + owner bearer, scoped to
  xbind), so the `template` remote fetches. Existing instances (pre-Phase-7) add
  the remote once by hand — see `plans/templates.md`.
- New builtin tile **prometheus-viewer**: binds one or more components that
  expose Prometheus metrics (service `prometheus`, multi:true — e.g. llm-gw)
  and renders their `/metrics` as a live dashboard (per-source counters/gauges,
  current value + per-second rate + inline sparklines). Phase 6 of the agent-v2
  program.
- llm-gw: **preferred models** — one workspace default per use-type (`agent`,
  `chat`, `pipeline`, `vlm`, `coding`, `summarizing`), set on the tile and read
  by callers via `GET /preferred`, so a model choice lives in one place. Plus
  per-model **cost** tracking, a Prometheus `GET /metrics` endpoint (bindable via
  the new `metrics`/`prometheus` provide interface), and transient-error
  **retry/backoff** on the proxy (429/5xx, pre-first-byte, honors `Retry-After`).
  First slice of the agent-v2 program (`plans/agent-v2.md`).
- agent template (v2, backend): model **tiers** (`general`/`code`/`memory`/
  `vlm`) — each empty tier resolved from llm-gw's preferred model for the mapped
  use-type, so a workspace sets models once and agents inherit; compaction and
  memory work run on the `memory` tier. Plus LLM-call **retry/backoff**
  (transport + 429/5xx, so a gateway reload no longer fails a run), the
  **provider-reported prompt tokens** as the compaction trigger (not a char/4
  estimate), a per-tool `toolTimeout` config field, and a **date-only**
  system-prompt prefix so prompt caching keeps hitting. Tools in one turn now
  run **in parallel** (bounded, each under `toolTimeout`); several
  `spawn_subagent` calls in a turn run as **parallel subagents**. Control tools
  (finish/ask_user/yield) stay sequential and stop the turn. MCP servers are now
  **bound** via an `mcp` interface (multi:true, service `mcp`, like the chat
  tile) rather than a static config list — the owner binds MCP providers in the
  Interfaces tab and their tools reach the model through the binding. New
  **`recall`** tool: FTS5 full-text search over the run's whole history
  (including turns compacted out of context), so detail folded into a summary
  is still retrievable. The wake heartbeat is now **on-demand** — registered
  only while runs are sleeping or mid-drive and removed when idle, so an agent
  with nothing pending stops polling (cron is no longer always-on).
- agent template (v2, scheduling): **cron-agents** — a session or the agent
  itself schedules recurring runs (`schedule`/`unschedule` tools, `/schedules`
  CRUD), each registering its own cron job. **Watcher mode**: a watcher schedule
  re-drives one persistent run with a "check now" nudge and, via a
  `state_changed` tool, keeps only the rounds that reported a change — no-change
  rounds are rolled back, so a long-running watch stays compact. A **Features**
  menu (`GET /features`, toggled in the config's `features` map) gates optional
  capabilities: recall, skills, streaming, vision, parallelTools, watcher.
- agent template (v2): **streaming** — with the streaming feature on, LLM calls
  stream tokens into a live per-run draft (`GET /runs/{id}` → `draft`) so the
  tile shows progress as it generates; retries only before the first token.
  **Multimodal tiers** — a message carrying image content routes to the `vlm`
  tier when the task model isn't vision-capable (name heuristic; override by
  setting the vlm model). `wireMsg` content is now text or an OpenAI
  content-parts array.
- agent template (v2): **skills** — a self-improving skill library (Hermes-
  inspired). The agent authors reusable procedures with `skill_manage`, lists
  them (`skills_list`), and loads one on demand (`skill_view`); the compact
  name+description list is injected into context. **`POST /runs/{id}/learn`**
  distills a run into a skill. `/skills` CRUD + storage in the agent's sqlite.
  Gated by the `skills` feature. (Background auto-curator deferred.)
- agent template (v2, tile): the control tile is rebuilt ~2× taller with a
  settings overlay — **Config** (model tiers + budget/iters/timeout), a
  **Features** menu (toggle recall/skills/streaming/vision/parallelTools/
  watcher), **Memory** CRUD, **Schedules** (create/enable/run-now cron-agents),
  **Skills** (view/edit), and **MCP** bind status — plus a **live streaming**
  draft in the timeline and a "learn skill from this run" action.

- auth: the `code` capability now also has a **blanket form** — `uses {target:
  "code", role:"reader"}` grants read-only source access to **every** component
  (for scanners/linters/stats), alongside `code:<component>` for one. Honored
  on `/api/xbin/code/*` and `/git/{log,diff}`.
- docs: corrected the workspace `AGENTS.md` §Sandbox — `deps/` symlinks are
  editing-plane only; their targets are NOT mounted in the backend sandbox
  (matches docs/isolation.md). Read a sibling's source via a `code:` grant, or
  call it over HTTP.

## 2026-07-06

- frontend: tiles can spawn workspace-level UI via the shell — `xbin.dialog(spec)`
  (a data-driven modal that escapes the iframe; resolves `{button, values}`) and
  `xbin.window(spec)` (a floating window framing one of the tile's own sub-paths,
  a real tile frame that uses `xbin.fetch` normally). New core element
  `<bx-dialog>`. Docs: elements.md §Dialogs & windows, sdk.md, protocol.md.

- interfaces/auth: new `code:<component>` capability — grant a component
  read-only access to *another* component's source (files + git log/diff via
  `/api/xbin/code/*`, `/git/{log,diff}`), owner-approved like any cross-scope
  grant. A component always reads its own source; admin reads any.

- **BREAKING** — interfaces: instance path prefixes registered via
  `PUT /api/xbin/iface-instances` are **provider-relative** and
  workspace-absolute (`/api/<self>/…`) registrations are now rejected with
  a 400. Providers that registered absolute paths must re-register.
  → [migration note](/docs/changes/2026-07-06-instance-paths.md)
- interfaces: multi-input slots (`{kind:"http", multi:true}` — a slot binds a
  SET of providers) and provider instances (`provides {instances:true}` +
  runtime registration, bound as `provider#id`). Injection shapes documented
  in [elements.md](/docs/elements.md); multiselect binding UI everywhere.
- interfaces (admin UI): http bind options are filtered by the slot's
  `service` contract (an `s3basic` slot no longer offers an `openai`
  provider).
- chat tile: binds multiple `llm` providers (model picker aggregates all) and
  any number of `mcp` servers — Model Context Protocol tools are offered to
  the model and executed in an agent loop. MCP providers serve
  Streamable-HTTP JSON-RPC at `/mcp` under themselves (`service: "mcp"`).
- llm-gw tile: multiplexes multiple named upstream backends; model ids become
  `<backend>/<model>` once more than one is configured; per-backend usage
  (reqs, tokens in/out, in-flight) shown on the tile.
- shell: per-tile ⚙ mini-admin on card title bars (admins), sidebar folders /
  collapse / resize, and a system-status footer (cpu, mem, disk, services,
  vault state, req/s). `GET /api/xbin/status` grew `host` and `traffic`
  gauges.
- workspace: `POST /api/xbin/clone {from,to}` forks a component (git history
  kept, old-path references rewritten); Tile Manager grew a clone tab.
  Prefer it over `cp -r`.
- terminals: `IN_SANDBOX=1` / `IS_SANDBOX=1` set in sandboxed terminals;
  terminal panes use a distinct `--bx-term-bg` theme token.
- vault: admin-tile barrier UI (seal state, first-time passphrase, unseal,
  rekey), `POST /api/xbin/vault-rekey`, `bx vault rekey`, and a red
  vault-sealed banner on the admin overview.
- auth: `PATCH /api/xbin/auth-settings` can disable owner-token *browser*
  login once an admin user exists (`bx` Bearer token unaffected).
- terminals: Ctrl+W now reaches the shell as word-erase instead of closing the
  browser tab (best-effort — some browsers reserve it); overlapping floating
  windows get a higher-contrast edge.
- terminals: exiting the shell now closes that terminal (its tab, or the
  window if it was the last one) instead of leaving a dead "[session ended]"
  pane; terminal tabs can be renamed (double-click).

## 2026-07-05

- **BREAKING** — the project renamed **buxon → xbin**: module
  `github.com/xbin-dev/xbin`, daemon `xbind`, env `XBIN_*`, API `/api/xbin/*`,
  headers `X-XBin-*`, manifest `xbin.json`, runtime dir `.xbin/`, JS global
  `xbin`. Workspaces are migrated on upgrade; external tiles need the same
  rename (see the bx-term-tile migration for the pattern).
- resources: encryption at rest is **always on** — filesystem/sqlite/blob are
  per-resource gocryptfs mounts, kv is envelope-encrypted; sealed vault ⇒
  resource unavailable + component held, never silent plaintext
  ([resources.md](/docs/resources.md)).
- terminals: sessions are persistent server-side (reattach with exact
  scrollback); component terminals are scoped read-only outside the
  component's own dir + `$HOME`.
- shell: tiles can be unpinned into floating windows (persisted per user).
- imports: a component whose `uses` reference nonexistent resources is
  rejected at import time instead of silently landing broken.
