# XBin — Decision Log

> Status: **live** — the decision log; every non-obvious choice gets an entry here.

Status meanings:
- **NEEDS CALL** — blocks or shapes early work; want an explicit decision.
- **DEFAULT SET** — a default is picked and the plans assume it; veto if wrong.
- **ACCEPTED TRADE-OFF** — known wart, recorded so it's deliberate.
- **RESOLVED** — decided (date, decider).

## Where the entries are

The log is split to stay under the 1 MiB guard on committed files
(`make large-files`). This file is the entry: the status meanings above, this
index and the recent entries below. Older entries were moved, byte for byte,
into archives:

| File | Holds |
|---|---|
| [decisions/D001-D099.md](decisions/D001-D099.md) | the opening sections (resolved calls, needs-a-call, the RT, ISO, CM, BU, ND, ING series, accepted trade-offs, review findings, implementation notes, residual risks, LC series) and D1–D99 |
| [decisions/D100-D149.md](decisions/D100-D149.md) | D100–D149 |
| [decisions/D150-D179.md](decisions/D150-D179.md) | D150–D179 |

An archived entry stands until a later entry supersedes it; nothing is added
to an archive. A new entry goes at the end of this file, in number order.
When this file passes about 450 KB, move its oldest whole entries into a new
archive `decisions/D<first>-D<last>.md` (three-digit numbers) and add its row
here: cut from the old end, never reword.

---

## Recent decisions (from D180)

- **D180 — One set of workspace settings: base auto-update (D175) and the
  partitioned tiles' switches (PD-55) are the workspace settings, one
  model, one store, one event, one console tab (2026-10-01).**
  internal/wssettings (the store), internal/server/wssettings.go (the
  routes, the aliases included), internal/broker/policies.go (the reader
  and the alert), cmd/bx/settings.go, the admin tile's tabs/settings.js and
  settings-view.js. The owner asked for one place: D177 had left `bx
  settings` / `/workspace-settings` / data/workspace-settings.json /
  workspace → terminals (D175, released in v0.3.65) and `bx policies` /
  `/workspace-policies` / data/workspace-policies.json / workspace →
  policies (PD-55, shipping in v0.3.66) side by side — "folding them is a
  compat question … left to the owner".
  - **Chosen: the released name and file are the model.** D175's route
    and file shipped first and can't move, so the switches join them:
    `GET/PUT /workspace-settings` carry `baseAutoUpdate`,
    `partitionConsent`, `credentialResetConfirm`, grouped by topic
    (terminals; partitioned tiles) in `bx settings` and the console's
    workspace → settings tab. One store (wssettings.Store, shared by the
    terminal manager, the broker and the server: one cache, one mutex),
    one event (`workspace-settings`, `{baseAutoUpdate, changed: [keys]}`).
  - **Each switch keeps its semantics, per key.** Defaults: base
    auto-update on, the switches off. Reads: base auto-update any
    credential; the switches a person (session, device, or a terminal or
    agent session they drive) or an admin — other tile code's GET has no
    such keys (one rule, server.ReadsPartitionSettings, which the
    partitions listing uses too). Writes: admin, audited with every
    setting's old→new. Fail-safe values when a value can't be read:
    base auto-update off (a guess must not discard a layer); a switch —
    a protection — its last value read, or on (never quietly off); a
    switch's key in another case is unreadable (encoding/json would match
    it). A file with any setting it can't read is never overwritten;
    admins get one alert, kind `workspace-settings` — crit while a
    switch can't be read, warn for base auto-update alone (new: D175 had
    none). An empty settings file is every default, as D175 had it.
    The consent and confirmation timing is the switches' consumers', not
    the store's: untouched.
  - **The event carries no switch.** Every socket hears a non-bus event —
    tile frames too — and tile code may not read the switches, so the
    `workspace-settings` event carries base auto-update (as before) and
    the keys a write set; a console reads GET again.
  - **Never break users (compat.md rules 2, 4, 6): the v0.3.66 surface
    stays as aliases.** `GET/PUT /workspace-policies` answer as before —
    same shape (`schema: 1`), same 403 for tile code, same 400s
    (`baseAutoUpdate` there is still unknown), 500 while a switch can't be
    read — from the one store. A write of a switch on either route
    publishes `policies` beside `workspace-settings`: v0.3.66's admin
    console, shell (bx-part-consent) and partitions page follow it. `bx
    policies` is the same command with one line on stderr naming `bx
    settings`. The console's `tabs/terminals.js` and `tabs/policies.js`
    stay (rule 4) as the settings element limited to one group, and
    `#terminals`/`#policies` open the settings tab. A new console against
    an older xbind tells it by its answers, not a version: no
    `partitionConsent` in `GET /workspace-settings` → the switches are at
    `/workspace-policies` (read, saved, and followed by `policies` there);
    that route missing too (Go's mux 404, no error body) → no
    partitioned tiles; `/workspace-settings` missing → no settings. `bx
    settings` does the same. The alert's kind changed from `policies`;
    nothing reads it by kind.
  - **The pre-D180 file: imported, kept current, re-imported.** The first
    read after the upgrade copies data/workspace-policies.json's switches
    into the settings file and records the SHA-256 of the bytes it took
    (`policiesFileSha256`), so the import is once and idempotent. The file
    is not removed: every write of a switch also writes both into it
    (keeping its keys and a newer schema number) and records its new sum,
    so a downgrade to v0.3.66 reads the switches as they are — and when
    an older xbind (or a hand) changes it, its sum no longer matches and
    the next read imports it again: what was set while downgraded is what
    the upgrade back applies. Written first, the settings file second: a
    write that fails between them leaves the policies file to be
    imported. A policies file to import that can't be read (empty
    included, as it always was) makes both switches unreadable — on —
    until it is fixed or removed; a settings file that can't be read
    imports nothing. A fresh workspace gets the policies file at its first
    switch write (a downgrade to v0.3.66 must not find them off).
  - **The partition class of the writes is unchanged.** `PUT
    /workspace-settings` stays GlobalOnlyRefused and the alias
    PartitionNeutral, as each was: the only credential the difference
    reaches is a partitioned tile's own holding xbin:admin.
  - **Rejected:** keeping the policies' file as the switches' store under
    one route (two files, two caches, a downgrade of D175's file
    semantics); moving everything to a new route or file (D175's are
    released); a boot-time migration (the read imports it, and the
    downgrade case needs the re-import anyway); deleting or renaming the
    policies file (a downgrade would find the switches off); an event
    carrying the switches' values (tile frames hear every event); a
    `policies`-only event for the switches (an older-only signal for a new
    model).
  - Tests: internal/wssettings (defaults, persistence, unknown keys, each
    fail-safe, the import, the downgrade copy and its re-import, a broken
    policies file), internal/server (the routes, who reads and writes what
    on both routes, the alias's shape, body rules, events and audit, an
    unreadable file), internal/broker (the readers with the real IsAdmin,
    the upgrade from v0.3.66's file, the alert), cmd/bx (set's forms, an
    older xbind, the alias's note), hack/admin-settings.test.mjs (the
    console's detection), the `adminSettings` harness pass (the tab, an
    older xbind's answers, the console from before the tab).

- **D181 — Partitioned tiles in the iOS app: the marker, a paused tile's
  page, the `xbin/partitions` link and "Your partitions" (2026-10-01).**
  native/ios: XbinCore's Client/XbindPages.swift (the page, its feature
  gate, its ticket, the settings entry), Catalog.swift (`TilePartition`),
  DeepLink.swift, Push.swift, Events.swift, TileLoading.swift; the app's
  Shell/XbindPageScreen.swift, Shell/Screens/PartitionMark.swift,
  WorkspaceModel, WebTileController's page mode, PushManager and the
  Notification Service Extension; native/spec/push.md. Server-side the app
  needed nothing for a tile to reach the viewer's partition (its frames and
  native.js run with the viewer's credential, plans/partitions/10 A.4), but
  what the plans and push.md give the app was never built: xbind's pushes
  linking `xbin/partitions` opened the workspace, `account.credential` never
  reached the app (it registered `agent`, `tile`, `test`), and rows had no
  marker (06 §12.2 "the iOS app shows the same marker on tile rows").
  - **`xbin://<ws>/xbin/<page>` names one of xbind's pages, and only the
    pages the app knows parse** (`XbindPage`: `partitions`). An unknown
    page is refused, so its push falls back to the workspace — what an
    older app does with any of these links (push.md §2). The link opens
    the page only after `GET /api/xbin/partitions` lists
    `partitions-page/1`; not listed, a 404 (no partitions) or no answer
    opens the workspace. Strict: no guessing from the version, no opening
    "to see".
  - **The page opens as a panel of the window, in a web view of its own**
    (`Surface.page`, XbindPageScreen): top level on the workspace's origin,
    no tile bridge, the chrome cookie store (D125: the person's own cookie
    session, never next to a tile page), signed in by a one-shot `POST
    /api/xbin/web-ticket {next: "/xbin/partitions"}` redeemed in that view
    ("Continue as …" once). Unlike a chrome tile there is no fallback to the
    plain page: a refused or failed ticket is an error with Try again (a
    password form inside the app is the convenience fallback the owner's
    direction rules out), and a reload mints a new ticket rather than
    reloading a page whose cookie session may have ended. Never a tile
    frame (xbind refuses frames anyway), never Safari: its one
    `target=_blank` link (the docs) loads in the same view, and its
    "← workspace" link (`/`) goes to the app's Home rather than loading the
    web shell. A panel rather than a sheet: Settings, a notification tap
    and a link all land the same way, back returns to where the window
    was, a window restores onto it (asking the gate again), Handoff gives a
    browser `/xbin/partitions`, and the banner of a notification linking
    the page is suppressed while it is shown (it updates itself). *Amended
    (the Mac mini's first run):* the page keeps its own theme whatever the
    phone's appearance — xbind's dark one in a light-mode app drew the
    bar's black title on the dark page. As under a tile page its colour
    runs up under the bar, and the bar now follows it: that colour as a
    visible bar background, the scheme a title reads on
    (`toolbarColorScheme`; `PageBarScheme`: WCAG contrast, white text below
    relative luminance ≈ 0.179) and the title drawn in that scheme's colour
    as the bar's principal item, legible even where the scheme isn't
    applied. Tile pages and chrome tiles show no title (only glass items,
    which adapt), so they are left as they were.
  - **Push kinds.** The app registers `account` too (`PushAPI.appKinds`;
    a registration without it is renewed by the kinds check; an older
    xbind accepts any well-formed kind — checked against v0.3.61). The
    NSE's category and thread read the kind *and the link*: only xbind
    links its own pages (a tile's notify link is always `c/<tile>/…`), so
    `tile.partition-…` from a tile stays a tile's notification with "Mute
    this tile", while xbind's notices linking the page get "Open your
    partitions" without Mute (a tile's mute doesn't hold them back) and
    `account.*` gets "Review…" (foreground, after the device is unlocked:
    allowing a held sign-in is what lets someone else in). Grouping: per
    workspace as before, and the page's notices a thread of their own
    (`<ws>/xbin/partitions`) so a held credential isn't buried under agent
    turns. Collapse stays the relay's `collapseId`; in addition the app
    removes the workspace's delivered notifications linking the page once
    it has loaded — the page lists everything they said. Unknown kinds
    still show like `tile`.
  - **The marker is the web shell's design A** (shell-kit.js
    `partitionMark`): the ring of radius 3.35 stroked 1.1 with the left half
    of the disc filled, teal `#3fb5a3` in dark mode and `#1f8778` in light
    (shell-css.js's light-dark pair), 9 pt on rows and cards (the row's
    status dot's size), last in a row's trailing badges (the sidebar has it
    before its ⋯) and in a card's corner before its sessions badge, so
    widget cards carry it too; its words are the web tooltip's, in the
    row's accessibility value. Rule: the recorded mode has user partitions
    (a pending switch doesn't change it). A **paused** tile (`state`
    `pending` with a request) gets a quiet "paused" badge and opens its
    page — xbind's switch page — never its native view: xbind answers the
    runtime document with that page (409), so the native view would only
    fail into it with a misleading banner; the native view returns with the
    catalog that says the tile runs. The catalog is re-read on a
    `partitions` event with op `mode` (and after an events gap, only where
    a row carries `partition`), so both follow a switch live.
  - **"Your partitions" in Settings follows I13 and the web's
    `pageEntry`:** the page served, a signed-in person (not view-as, not a
    token), and a partitioned tile in sight. The feature is probed only
    where the entry could show, so an xbind whose rows never carry
    `partition` is never asked.
  - **Left out:** the partition chip (I3, `yours`/`shared`/`global` on a
    window's head) — the plans assign it to the shell, and a phone shows
    only the viewer's own partition; consent prompts in the app (the push
    links the page, where consents are given).
  - **Compat.** An older xbind: no `partition` in rows, no probe, the link
    opens the workspace, `account` registered and never sent. An older
    app on this xbind: as before (its links open the workspace).
  - **Verified:** XbinCore's tests (deep links, kinds, categories and
    threads, catalog old/new JSON, the gate, the ticket, the entry, the
    event), app-check (navigation), app-stubcheck and widget-stubcheck,
    uitest-stubcheck, and app-live against real xbinds (this one: the
    page through its ticket and "Continue as", served top level only;
    v0.3.61: not served, `account` accepted). The app target, the UI tests
    (XbinPartitionsTests, on the e2e workspace's new `apps/parted`) and the
    look of the marker need the Apple CI / Mac mini.

- **D182 — Coding sandboxes that can develop xbin: the rootfs's special
  modes come back in VM guests, an image's `sudo`, devices opened per boot
  (2026-10-03).** docker/rootfs.Dockerfile (the recording step, last),
  internal/sandbox/vm/guest (modes_linux.go, devices_linux.go,
  root_linux.go's assembleRoot), the coding-sandbox template's backend
  (config.go `Image.Sudo`, `sudoWorks`; images.go `setupHash`;
  lifecycle.go `prepareScript`; create.go, manager.go's notes, operator.go,
  store.go `record.Sudo`) and its page (model/ops.js, web-ops.js,
  native/images.js, native/ops.js, feature `images.sudo`); the record is
  plans/sbx-dev-root.md. `sudo` failed in a coding sandbox that is a VM
  guest (no `NO_NEW_PRIVS`, no seccomp, no `nosuid` on `/`) although the
  base installs it: every setuid, setgid and sticky bit was gone, because
  hack/build-rootfs.sh unpacks `docker export` with an unprivileged `tar`
  (no `-p`), and deploy/install.sh's unpack and `chown -R` strip them
  again. The VM image path (`mkfs.erofs --all-root`, the guest's mounts)
  keeps whatever modes the tree has, so it was purely a build artifact.
  - **The owner's rulings at land (2026-10-05).** One switch: an image's
    `sudo` brings `/dev/fuse` and `/dev/net/tun` with it, no separate
    `devices` setting. Only for KVM-backed sandboxes: `sudoWorks` is the
    runtime's `vm` mode with `accel: "kvm"` (Firecracker) — an emulated
    VM (QEMU, D90) or another substrate's mode gets none, and hello's
    notes and the image editor say why. `hack/build-rootfs.sh` stays as
    it is (no forced `--no-same-owner`). Shipped before the owed VM
    end-to-end run (plans/sbx-dev-root.md "Owed").
  - **Fixed in the guest, never on the host.** The image records its
    special modes at build time — `find / -xdev \( -type f -o -type d \)
    -perm /7000 -printf '%m %U %G %y %p\n'` into `/etc/xbin-rootfs-modes`,
    names with a newline left out — and the VM guest re-applies the list
    at every boot, before anything runs. The host-side `tar` and `chown`
    stay as they are, on purpose: a setuid-root program in xbind's install
    directory is a host privilege surface (and a stale one: no package
    manager updates it), the installer runs unprivileged in user mode, and
    namespace sandboxes run with `NO_NEW_PRIVS`
    (internal/sandbox/init_linux.go), where the bits mean nothing. A
    missing list is no change (an older base); a list from a newer base
    under an older xbind is ignored. An image `FROM` the base that adds
    setuid programs runs the step again, last (the Dockerfile says so).
  - **Rejected: `mkfs.erofs --tar` from the export stream.** The stream
    exists only where `docker export` runs. Everywhere else the rootfs
    reaches xbind as an unpacked directory — the release bundle is a
    tar.zst of one, made after the strip; install.sh unpacks it; the
    install upgrade keeps old bases as `<rootfs>-<version>` directories
    that `layers.ResolveBase` finds and the VM image is built from — and
    namespace sandboxes overlay that directory anyway. Keeping a stream
    would mean a second multi-GB artifact per base version, a new bundle
    format and installer, and VM images buildable only where the export
    happened. The list is a few kilobytes inside the tree, travels through
    every copy, and needs nothing from the host. Also rejected: building
    the image from a confined overlay with the bits re-set (a privileged
    mkfs step for a guest-side concern), and unpacking as root (above).
  - **How the guest re-applies it: files in a layer, directories in
    place.** Each listed regular file whose image copy lacks its mode or
    owner is copied, moded and owned as listed, into a tmpfs stacked over
    the image (`lowerdir=/fixup:/lower`, parents mirrored from the image:
    mode, owner, times). Not a `chmod` in the assembled root: that copies
    each file up into the sandbox's upper — the VM disk, written behind
    the person's back — and a rebase (tile sandboxes keep their upper on a
    newer base) would then keep the old base's `sudo` over the new base's
    plugins. With the layer, the upper is never written for a file, a
    program the sandbox replaced itself (`apt`) stays its own, and a rebase
    finds the newer one. Directories (`/var/tmp` sticky, setgid ones) are
    set in the assembled root — a copy-up of metadata alone, which merges
    and holds no stale data — and only while they still show the image's
    unpacked mode and owner: one the sandbox changed is its own; mount
    points (`/tmp`, `/run`) are skipped. Bounds: the list is read from the
    image (never the upper), at most 1 MiB and 4096 entries, each with a
    special bit, a clean absolute path and the type the image has there;
    paths open with `openat2` RESOLVE_BENEATH | NO_SYMLINKS | NO_XDEV, so
    a symbolic link anywhere is skipped, never followed; at most 32 MiB of
    file data (today's base: about 1.5 MiB, an estimate from its setuid
    programs' sizes); a copy that fails is removed (a partial file never
    covers the image's); an overlay that won't mount over the layer boots
    without it, logged. Copying costs that much memory and time per boot.
  - **`sudo` is an image's, not the layout's.** One manager offers a
    confined image and a dev one side by side and consumers pick; an
    image is where root-time setup lives, and the grant rides in its
    build's snapshot, so every clone has it. `setupHash` includes sudo
    when it is set and is the script's alone otherwise: a changed `sudo`
    rebuilds an image with a setup script, and no existing build goes
    stale on upgrade. Plain images get it from the first start's prepare.
    A sandbox's `sudo` is fixed when it is made (`record.Sudo`, like the
    layout); a clone takes its source's — its root is the source's. The
    grant is `/etc/sudoers.d/<user>`, `<user> ALL=(ALL:ALL) NOPASSWD: ALL`,
    0440, written whole (a dotted temporary name, which sudo skips, then a
    rename): it names the user because the guest starts commands without
    supplementary groups (agentcore/procattr_linux.go), so `%sudo` would
    never reach them, and NOPASSWD because the account's password is
    locked. Never for root (a `users: root` substrate) or a name the image
    gives another uid.
  - **Namespace mode: refused when explicit, a note when automatic.** A
    namespace sandbox can't give sudo (`NO_NEW_PRIVS`). `validate` refuses
    a sudo image while the mode is `namespace`; in `auto` mode while the
    substrate offers no VMs, hello's `notes` say the image's sandboxes get
    none, the page says so on the image, and those sandboxes are made
    without the grant. `sudoWorks` is "not `namespace`": another backend's
    `cloud-vm` or `container` gets the grant, and a backend whose sandboxes
    can't run setuid programs names its mode `namespace` (AGENTS.md).
  - **Devices per boot, by the guest, from the sandbox's root.** `/dev` is
    a fresh devtmpfs at every boot with no udev, so `/dev/fuse` and
    `/dev/net/tun` are 0600 — rootless podman needs both. The guest reads
    `/etc/xbin-vm-devices` (`<mode> /dev/<node>` a line, at most 4 KiB and
    64 lines, permission bits only, character devices in that devtmpfs
    only, no symbolic link or other mount on the way) and sets the modes
    once the kernel filesystems are mounted; the template's prepare writes
    `0666` for both when the image has sudo — what a distribution's udev
    rules give, and nothing a sudo user couldn't do. Not a root run at the
    manager's start: the runtime restarts a VM on its own (a snapshot stops
    and starts it, a restore, an xbind restart's next start), so the
    manager doesn't see every boot. Not a runtime API field: new xbind
    surface (the spec, protocol.md, openapi, the definition's storage) for
    what the sandbox's root can state itself — and a file in the root
    rides along with clones, snapshots and image builds. Any VM guest's
    root may write it; it grants nothing that root couldn't.
  - **No "run as root" in the sandbox-manager contract.** The owner's
    rule: add it only if ACP has it, and ACP's `terminal/create` carries
    only sessionId, command, args, env, cwd and outputByteLimit
    (sdk/acp/types.go). Coding agents run their own commands as the
    layout's user, so `sudo` is what reaches them. If ACP gains a user or
    privilege field, mirror it as an exec/tty option
    (docs/sandbox-manager.md §Running commands) — that is the trigger to
    revisit.
  - **`xbin-dev` is documented, not a default** (the template's API.md
    §Images): root for every coding agent is an operator's choice. Its
    script installs podman, fuse3, uidmap and xbin's check tools, gives
    the sandbox's user subordinate ids and FUSE's `user_allow_other`,
    swaps in Node 24 (xbin's CI) and Chromium's libraries; in a sandbox of
    it, `hack/dev-setup.sh` does the rest. No init runs in the guest, so
    what a boot resets (an inotify sysctl) is the user's to redo; tests
    that need systemd's delegated cgroup skip; VMs started inside run
    emulated.
  - **Verified here** (a VM guest without docker or VM helpers): the
    guest's unit tests — the list's parsing and bounds, planning and
    staging against temporary trees, symbolic links refused, the
    Dockerfile's recording step run on a tree, and a real overlay over the
    layer in a user and mount namespace booted three times (the modes
    shown and nothing of the files in the upper; a newer base's program
    found after a rebase; the sandbox's own program and directory kept);
    the device file's parsing and refusals and a mode set on a real
    character device (a pseudo-terminal's); the template's
    TestPrepareScriptSudo and TestSudo (the grant, a clone's, the hash, the
    namespace refusal and note) and its UI tests (model, native, web).
    **Owed:** a VM boot on a rebuilt rootfs — `sudo -n id -u` as the
    layout's user, `/dev/fuse` 0666 after a restart; test/isolated's
    `testCSSudo` checks exactly that and needs docker or podman, the VM
    helpers and KVM.

- **D183 — Base Two brand: the system, the mark, and xbin.dev rebuilt in
  it (2026-10-03).**
  The owner's story for xbin is an era: "upgrade to exponential era
  software", "the first exponential era workspace". The brand was found
  without priming: agents started over in isolated sessions, three concept
  rounds of independent directions, each rendered and scored by a judge
  that saw every image and read by cold readers who knew nothing of the
  product, with the owner's notes between rounds (round three fixed the
  headline, "Software has entered its exponential era.", and asked for more
  presence). The owner picked V2, **Base Two** ("bold colors, some 70s
  brutalism-adjacent vibe and overall it's a big statement"), then, from four
  marks drawn for it, **M3, the bˣ tile**: a white b with a yellow x raised
  as its exponent, reversed out of cobalt (it also spells `bx`). Approving
  it the owner added: "I really like the less rounded corners, we should
  apply that consistently everywhere; Theme should follow system settings,
  and we need to audit light theme isn't broken by hardcoded styles."
  The system is `plans/brand.md`; the mark's files and rules are
  `plans/brand/marks/`.
  - **The idea.** Read the name as a formula: x is the variable, bin is
    base two. Four colour fields double in size, yellow 1, green 2, magenta
    4, cobalt 8, in that order in both themes; the biggest statement gets
    the biggest field. Loud headline, sober proof: the announcement is
    posters and signs, everything after it is quiet, exact and checkable.
  - **Three volumes** decide what each surface may do: Announce (the home
    hero, OG cards, film titles: the stair, the fields, one photograph),
    Inform (product, security, docs, install, legal: ink type, plates,
    the era stripe as the only colour, at most one field band and none on
    security, docs or legal), Work (the product: concrete, ink, one cobalt
    accent, fields only for part tabs, the elevated banner and first run).
    The home page alone moves through all three, once and in order.
  - **The owner's thesis, which the copy now leads with.** A company is a
    set of systems that cause things to be made, so agents should build
    those systems rather than be them: people and agents build the systems
    and no longer operate them. The definition, used verbatim: "xbin is a
    workspace where people and AI agents build the systems a company runs
    on, and where those systems run." The home page tells the story in a
    few words (the curve of software made, from sourced figures only: the
    measured series it can stand behind is new repositories on GitHub per
    year, 2013 to 2025 and the August 2026 rate, below; what breaks; the
    idea; xbin working, measured) and keeps the product plug quiet.
  - **What it replaces.** D67's frame, "an office for your agents", with
    its office metaphor and "IT holds the keys", is retired for the site
    (the repository README still carries it). Still in force from D67: no
    comparisons with other products, no "not X, but Y", no em dashes,
    exactly two calls to action (the trial command first, the install
    command second, each verbatim on one line).
  - **The copy rules now in force** (`plans/brand.md` §3): announce, then
    explain in a plain sentence; measured numbers only from the claims
    register (`hack/demo/measurements.md`), each sentence naming its
    machine and each page carrying the hardware note once; agents are
    software that builds and changes apps, never colleagues, hires or a
    person's replacement; short declaratives, plain part names, sentence
    case, "xbin" always lowercase; no hype words; trust claims show the
    mechanism (who checks what, where it is recorded).
  - **No deployment mode.** No copy says where xbin runs or who runs it,
    to stay flexible (the owner: "just don't mention deployment mode,
    besides no one really cares"). The install guide states requirements
    as facts; the privacy page keeps its existing wording.
  - **No sandbox technology on story surfaces** (home, product, iOS, film,
    social): no microVM, namespace, daemon, kernel, container or language
    names. A technology name overstates or misstates what most apps get,
    and it varies by app and will change; the copy says "its own sandbox".
    The security page may be concrete for IT but never names it either,
    and the microVM figures left the claims the site makes.
  - **Square corners and the system's theme.** 2 px wherever a corner
    shows, on the site and in the product: buttons, inputs, code, cards,
    plates, windows, mats, tabs; no pills; full-bleed bands and fields show
    none. The theme follows `prefers-color-scheme` with no toggle and
    nothing stored, and every colour comes from tokens, so neither theme
    can break on a hard-coded value. Focus is a cyan ring, 3 px with a 2 px
    gap, a hue no field, accent or status uses.
  - **The site.** `website/` is rebuilt in it with no build step: plain
    HTML and CSS, one small ES module (the copy buttons; without it a
    command is plain text a click selects whole), self-hosted fonts, no
    cookies, storage, analytics or third-party loads, every page working
    without JavaScript. The site's guard, `hack/check-website.sh`
    (`make website-guard`, in `make guards` and CI), holds it there:
    `install.sh` byte-identical, the privacy page's words, `app/ios.json`,
    no third-party loads or storage (no script names another host, no
    cookies, web storage, IndexedDB, Cache API or service worker), per-page
    budgets (HTML and CSS 60 KB, JS 80 KB, fonts 200 KB, first-screen
    images 250 KB), one header and footer, every image's alt and size.
    `make website-check` is the separate browser pass (Playwright, not in
    the guards: every page at six widths, 360 to 1920 px, in light, dark
    and reduced motion, and at 320 px for overflow).
    `make website` runs the guard with `--dist` and refuses a site not
    ready to deploy: a `{{DATA}}` slot or a shot waiting for its capture
    left on a page, or a prebuilt helper set the site serves missing or not
    matching its sha256. Wordmark A ships with the exact Bricolage
    Grotesque outlines in place of the mark round's hand-drawn proxies.
  - **Rejected** (in the judges' and cold readers' terms). The first rounds'
    five directions turned out to be one idea, warm paper, one vermilion
    accent and a square in the exponent: *Superscript* (A), an editorial
    thesis set like a magazine's new chapter, whose spec box told readers
    what xbin is but whose framing left them unsure they were on a product
    page; *Instrument: Log Scale* (B), an engineering datasheet, the most
    trustworthy to IT, whose log axis put unrelated quantities on one
    scale; *Bounded Series* (C), every speed paired with its bound, the
    clearest security story, under a headline claiming a speed nothing
    measures; *Superscript* (D), a motion grammar taken from the product's
    own mechanics, behind a near-generic two-square mark; *Superscript*
    (E), the x of "exponential" rising into the exponent, whose red x
    smudged at 16 px and read as close or error. Round three: *Chambered*
    (A1), the thesis with a nautilus photograph, more atmosphere and the
    round's lowest clarity; *Night Edition* (A2), the product standing in
    the one lit opening of a dark concrete hall, the most "product page
    with vibe" and close to dark-mode-only; *First Light* (V1), a cinematic
    title sequence with the best information design and a sunrise that
    came closest to the generic AI-startup look; *Generations* (V3), a
    5ⁿ lattice growing from the product, rigorous but calm in type and busy
    in picture; *Raw machine* (V4), brutalist and system-native, the
    clearest install band under an 8-bit wordmark in a well-worn
    yellow-on-black look. Base Two's own weak points, as judged (four
    saturated fields read as a creative tool, a halving-square mark read
    as decay, dark mode flipping the stair), are what the three volumes,
    the mark round and the fixed doubling order answer. Marks: M1, a
    doubling stair of columns, the best motion story but a generic bar
    chart at 16 px; M2, 2ˣ, the most legible but outside the field
    language and close to UI notation ("2×"); M4, the stair on an ink
    plate, closest to the hero but with no single shape across themes.
    M3 has the strongest presence where the mark is a square (favicon, app
    icon); its refinement opened the b's counter and unboxed the x after
    cold readers saw a notification badge.
  - **The curve is drawn from measured data only.** The research pass's
    register found no measured "software made per year" series before
    public version control, so the home page's chart draws what it can
    stand behind: new repositories created on GitHub per year, 2013 to
    2025, and the August 2026 rate marked as annualised, every point with
    its source (`website/data/software-per-year.json`, drawn by
    `make website-chart`); the marker sits at the first language model for
    code (June 2021). The register also warns that the bend came with
    coding agents in 2025 and 2026, not with the first language models.
    The August 2026 point is one month's count times twelve, so it is
    drawn as what it is: a dotted segment with no fill to a hollow square,
    and the caption says so. Every sentence about the chart (its caption,
    the figures under it, a projection's line and words) is generated from
    the data, so a projection the data does not carry cannot be described;
    the two figures the register does not give (the growth since the
    inflection, the end of the decade) left the page rather than ship as
    slots.
  - **The product shots.** The four brand-terminal shots (S-4 `bx --help`,
    S-5 the grants file's diff, S-11 the installer on a fresh Ubuntu 24.04
    VM, S-12 the apps as folders) are real sessions recorded byte for byte
    and replayed into xterm.js set up as the brand terminal, kept with
    their recordings in `website/art/shots/`. The product UI shots wait for
    the product theme and their sections ship without them, as the film's
    do; only S-9 has a matching interim still.
  - **Open.** The owner's sign-off on wordmark A or B (A ships; B is wider,
    so a swap changes the wordmark's sizes on every page and the share card
    with the file, `website/README.md` → "Assets"); the product theme,
    Concrete Day and Concrete Night (D184), and the product shots and film
    that wait for it; the iOS app icon (W8's iOS half: the web and server
    half landed, below); the curve's projection and the two figures the
    register does not give; whether the section's copy ("Seventy years of
    software. Then the curve bent.", the marker "First large language
    models"), written for a seventy-year chart, should change to fit the
    measured one; the brand terminal's window (captured at 800 × 500 CSS
    px, not the spec's 1600 × 1000, so its type reads on the page). The
    repository README's pitch, open here first, landed with W8's web half.
  - **W8 in the web and server product, and the guide for tile builders
    (branch `brand/web`, 2026-10-04).** Asked by the owner on D184's review
    ("make sure tile agent Agent.md is updated with new guidelines and
    points to docs"; "for logo we have the new one").
    - *The mark ships as the masters' copies.* `/vendor/favicon.svg` and
      `/vendor/logo.svg` keep their URLs (compat rule 3) and are
      `favicon.svg` and `lockup-a.svg` byte for byte
      (`TestProductMarksAreTheMasters`); the PNGs beside them are rendered
      from the masters with `rsvg-convert` (the 32 px one from the hinted
      drawing), `apple-touch-icon.png` is the site's.
    - *Inline where the word must follow the person.* xbind's own pages
      (sign-in, invite, "Continue as", request access, the partition
      switch, the hop, the refusal) and the shell's header draw lockup A
      inline: the tile in its own colours, the word in `currentColor`. An
      `<img>` of `logo.svg` was rejected: two of those pages load no image
      (CSP), and the file's word follows the system's colour scheme, not
      the person's theme cookie or setting. The copies' paths and the
      word's placement are tested against `logo.svg`
      (`TestBrandMarkIsTheMasters`, `hack/shell-brand.test.mjs`); the
      theme canary exempts the tile's three shapes by class. Sizes: the
      tile 24 px in the 40 px top bar (wordmark A's least size, 16 px from
      ascender to baseline, sets it), 32 px on the sign-in plate, 24 px on
      a notice. Workspace branding (D76) still wins; an icon set without a
      title now stands beside the word alone, never beside xbin's tile.
    - *docs/design.md*, the product half of this brand for tile builders
      (people and agents): the calm/labelled/honest tests, ten rules, the
      components on the tokens, the voice, both themes, do and don't. One
      rule is new: no brand colour fields in a tile (a tile shares the
      screen; the product allows one field on it). The workspace
      `AGENTS.md` carries nine of the rules as *Design guidelines* and
      points at design.md and frontend-kit.md §Theme; the welcome note and
      `bx new`'s page point there too.
    - *Copy:* the README opens with the kicker, the definition and the
      pillars as facts (the pitch above, closed); `docs/index.md` with the
      definition; the shell's empty states in plain sentences. The
      installer's banner carried no tagline and is unchanged (its S-11
      shot is recorded byte for byte).
    - W8's iOS half landed as D185 (brand/ios, merged with this into
      brand/release): `XbinPalette` on Concrete Day and Night, the
      reference renderer with it, the mark on the Welcome and Log in, and
      the app icon. The app's copies of the mark are held to the masters
      by BrandMastersTests, as the web's are by the tests above.

- **D184 — Base Two in the workspace: Concrete Day and Concrete Night; a
  document opts in, the person's choice rides the D4 injection, frames
  follow by message (2026-10-03; W9).** plans/theme-base-two.md (the
  contract, the mechanism, the guard and six packages), web/theme.css,
  web/bx-theme.js, web/bx-icons.js, web/theme-boot.js. (The two numbers
  before this one are taken on other branches: claim-account and the
  website.) The owner approved the Base Two product theme at gate B2 with
  its defaults (Q4–Q13) and asked for 2 px corners everywhere, the system's
  light or dark with a per-person override, and a light theme audited until
  no hard-coded style is left. xbin was dark-only: one token set and
  `color-scheme: dark`, 10 of 396 radius rules on a token and 40 pills, 521
  colour literals outside token fallbacks, 213 lines of emoji used as icons,
  sign-in pages and the native renderer on palettes of their own.
  - **A document opts in; every other document stays dark.**
    `<html data-bx-theme="auto">` plus the `theme.css` link follows the
    person; a document that links the sheet without it gets Concrete Night,
    new values under the same names (`--bx-green/-amber/-red` alias
    ok/warn/danger), so a third-party tile that hard-codes light text stays
    legible. `theme.css` is still never injected; bare documents render from
    fallbacks that equal Night.
  - **The person's choice is two keys in the shell's prefs bucket**
    (`theme`: light|dark, absent or "system" = the system's; `density`:
    comfortable, absent = compact), written with the existing
    `PUT/DELETE /prefs/<key>`; only chrome writes that bucket. Its `prefs`
    event carries the change to the person's other tabs and devices. No new
    route.
  - **First paint comes from the D4 block**: `<meta name="xbin-theme">` and
    `<meta name="xbin-density">`, added only when set (a person who never
    chose gets today's bytes), read by `:root:where([data-bx-theme="auto"]
    :has(> head > meta…))` selectors; "system" is `prefers-color-scheme`, so
    no script is involved. `:where()` keeps `:root`'s specificity (a
    document's own `:root` override still wins) and forgives a browser
    without `:has()`, which stays Night. The person is the principal's user
    (the viewed person under view-as), `root`'s bucket for the owner token
    and `--no-auth`; a tile's backend and a credential-less load get none.
  - **Live changes travel by message.** `setAppearance()` rewrites the
    document's metas and fires `xbin-appearance`; `<bx-frame>` posts
    `{type: 'xbin:appearance', theme, density}` to its iframe on every load
    and change, only when its own document follows; `xbin-client.js`
    applies it from its parent alone (from the workspace origin alone on a
    tile origin), and nested frames relay. An old shell that never opted in
    sends nothing.
  - **Pages without an injection read a hint cookie**, `xbin_theme`
    (light|dark, a UI hint and never a credential), kept by `bx-theme.js`
    and by `xbin-client.js` in a top-level chrome document: the sign-in
    pages render the meta from it server-side, the static partitions page
    copies it with a synchronous `/vendor/theme-boot.js`; the docs viewer
    reads the person's prefs like the injection.
  - **Terminals follow unless the person picked a palette**: the terminal
    menu's stored `default` builds xterm's theme from `--bx-term-*` and
    rebuilds it on every change; picks (now with Concrete Night and Day)
    stay per browser.
  - **Tokens**: tokens.json's product blocks with Q4's Night lift (panel and
    panel-2 one ramp step up, the dependent steps with them, windows edged
    in border_strong, status tints recomputed on the lifted panel, subtle
    nudged to keep 4.5:1 on hover), Q5's window chrome, new syntax and diff
    palettes (Q7; 4.5:1 on the code well, the selection and the diff
    tints), the focus ring as tokens (3 px cyan, 2 px gap in the page
    colour), shadows for windows at rest, active and pop-overs, 2 px radius,
    Instrument Sans / JetBrains Mono / Bricolage Grotesque self-hosted
    (OFL), density and motion. `--bx-part` stays the partition marker;
    brand parts are `--bx-part-tab-*` (Q12). The scroll tint moves from the
    accent to the focus colour: it marks where scrolling input goes.
  - **Icons**: `/vendor/bx-icons.js` (`<bx-icon name>`), drawn to brand §9
    and named after the native vocabulary where meanings match; an emoji
    leaves a string with its space and the words stay; view models that
    carried one gain an `icon` field; a person's own emoji (folders, agent
    classes) is content and is left alone.
  - **The guard**: `make theme-check` = `theme-fallbacks.mjs` (resolving
    aliases, checking the two Day blocks are identical) + a lint (colour,
    radius, font, small type, emoji, contrast) with an allowlist and inline
    `theme-ok:` reasons; harness passes for no-flash and a canary audit of
    the rendered UI in both themes.
  - **Rejected:** injecting `theme.css` (it would flip third-party tiles,
    compat rule 5); `<meta name="color-scheme">` as the opt-in (boilerplate
    could turn a tile light by accident); a cookie as the person's store
    (per browser, and credentialless sandboxed frames send none: their first
    paint would be wrong); `light-dark()` for every token (one definition,
    but every document's Night would then depend on 2024 browsers, and a
    browser without it would render nothing rather than Night); an inline
    script in the injection (new executable injection, and tile CSPs); a
    class applied by script after load (a flash); relying on an iframe's
    `prefers-color-scheme` following its embedder (Chromium doesn't);
    generating `theme.css` (a build step for one file; checking the two Day
    blocks are equal is cheaper); a per-tile theme pin; a static import of
    `bx-theme.js` in `xbin-client.js` (a page that couldn't load it would
    lose `window.xbin`: it loads beside the client, its listener there from
    the start).
  - **Plan**: six packages with disjoint files (P1 core, P2 shell, P3 core
    elements, P4 xbind's pages, P5 shipped tiles and templates, P6 the film
    set); merged P1, P2, P3, P4, P5, P6 (the plan's P1, P3, P2 order
    wasn't needed: no file is two packages', and no merge conflicted),
    then one integration step.
  - **Shipped with P1 (core):** the fonts, vendored and pinned like the
    rest of `web/vendor/` (`hack/vendor.sh` fetches Instrument Sans 400,
    600 and 400 italic and Bricolage Grotesque 600, 800 from @fontsource
    5.3.0 as latin and latin-ext halves with their `unicode-range`, and
    JetBrains Mono 400, 500, 700 v2.304 whole — all 128 box-drawing and 32
    block characters; `hack/vendor.sha256` lists `fonts/`, which
    `check-pins.sh` now walks); the injection (`internal/server`
    appearance.go: the two keys read through `internal/prefsfile`, the
    bucket path's one home now that obs and server both need it, cached per
    file by identity, size and modification time); the docs viewer on
    tokens with the person's metas; `xbin-client.js`'s relay and cookie;
    tests for the contract modules; the guard (`theme-fallbacks.mjs`
    extended — comments stripped, aliases and composites resolved, font
    tokens exempt, Day blocks compared, paths — and the new
    `theme-lint.mjs` with `hack/theme-allow.txt`); the harness's dark
    default and `HARNESS_THEME`, the `appearance` and `themeCanary`
    passes, the theme in `tileAssets` and `oldScaffold`; the builder docs
    (frontend-kit §Theme, protocol, elements, compat rule 5, getting
    started, the workspace AGENTS.md, maintenance, the changelog).
  - **Verified (P1):** `go test ./internal/server/... ./internal/obs/...
    ./internal/prefsfile/...` — a person who never chose, or whose bucket
    holds junk (nine kinds, a half-written file), gets today's bytes (frame
    tokens masked); light, dark and comfortable land in the block before
    the document's own head; the owner token, the owner's frame and shell
    read root's bucket, a session, frame and terminal the person's;
    view-as the viewed person's; an instance none; a deployment URL, its
    native document, a partitioned tile, the tokens and origins modes and
    the native runtime document all carry it; the cache sees a
    replacement, a same-size rewrite in place and a removal; the docs
    viewer is opted in with no literal of its own; the fonts are served as
    font/woff2 and answer `Origin: null`. A mutation that drops the metas
    fails 22 checks. `node --test hack/theme-*.test.mjs
    hack/xbin-client-*.test.mjs` (the DOM shim's bx-theme, theme-boot,
    icons, client relay, fallbacks and lint suites). In Chromium (the UI
    harness, test seed): the `appearance` pass, 57 checks in each of the
    legacy, tokens and origins modes — for system, light and dark on a
    light and a dark system, a tab, a sandboxed frame and a chrome frame of
    an opted-in tile are right at their first animation frame, rows
    included, and never repaint another background; a tile that didn't opt
    in stays Night; the shell leaves the hint cookie equal to the choice;
    the sandboxed frame loads all three families cross-origin (on its own
    tile origin in origins mode) and draws in them; xbin:appearance from
    the parent restyles a frame at once and from the frame itself does
    nothing. `tileAssets` (fonts and the meta under every gating mode),
    `oldScaffold` (v0.3.65's root, shell and admin console stay Night under
    a person's light on a light system; a new opted-in tile inside follows
    the injected light; nothing breaks), `appHelp`, `adminTabs`. The shell's
    own change reaching frames, a second tab following through the prefs
    event and terminals following SKIP until P2 and P3 land.
  - **Integrated (2026-10-03):** the fallbacks and the lint were already
    at zero once the six had merged (each package had rewritten its own
    files; `--fix` found nothing). The step drew the glyphs the hand-offs
    asked for — `grid` (a screen), `power` (the agent template's brake, as
    its native view), `call` (the film set's call activities) — and added
    one token, `--bx-mono-size` (mono beside UI text: 12px, 13px
    comfortable); `.bx` code draws no ligatures. It split the five files the
    packages had grown past their size budget (`internal/sizebudget`) into
    siblings — `web/agent-css.js`, `web/frame-css.js`, `web/term-chrome.js`
    (also xterm's one loader, which `bx-logs` now shares),
    `workspace-template/shell/shell-appearance.js`, the admin console's
    `navCss` — and made the cross-package fixes the hand-offs named: the
    termTheme pass registered, personalBinds' drawn remove control, `shot()`
    putting the scroll back, the harness seed editing the coding sandbox's
    JSONC manifest without `json.load`, the deploy chip's icon on the test
    surface, two native fixtures, the docs (glyph names for ⇈ 📌 ⇡ and the
    rest, `bx-window-front`/`bx-build`, the pages that read the hint cookie,
    status as a glyph and word). The harness on the merged branch found the
    rest: the grant requests are taller plates now, so `menus`, `menuOpen`,
    `gridScale` and `partitionConsent` scroll what they click into view;
    `sandboxTerminal`'s ssh runs with `-F /dev/null`; the canary found the
    shell mark's X computing to fill black and the seeded zsh prompt's
    256-colour grey (now `%F{8}`, which follows the theme); the real-theme
    audit found the settings slider's UA field colour. Also fixed: the Agent
    tab's 404 handler called `this._end()`, shadowed by the window's `_end`
    index (now `_gone()`), and the app's help screenshots were reshot.
  - **Verified at integration:** `make check` (fmt-check with CI's gofmt
    1.26.3, vet, js-check, js-test 724 pass, native-check, theme-check,
    shellcheck 0.10.0 over 63 scripts, pins-offline, large-files, `go test
    ./...`), `make tile-check` (9 backends). `make integration` can't be
    judged in the session it ran in: 889 tests passed, among them the
    examples' end to end (TestGrantFlowAndResources, TestComponentsAPI),
    and the 100 that failed all need what that session lacks — a user
    namespace with sub-uids (`newuidmap: write to uid_map failed`, "parent
    did not complete uid mapping") or FUSE outside a namespace of its own
    (encrypted resources held) — none touches D184's files; it wants a
    `hack/dev-setup.sh` machine. The UI harness on
    fresh test seeds, in a user and mount namespace for gocryptfs: every
    pass in dark (73 of 74, then `menuOpen` fixed and green in both
    themes); appearance (61 checks, no SKIP now that P2 and P3 are in),
    themeCanary (14 of 14 screens), scrollbars, windows, tileAssets with
    `HARNESS_THEME=light`; oldScaffold without the overlay in both themes;
    appearance and tileAssets under `tokens` and `origins` gating; under
    `HARNESS_ISOLATE` (and `HARNESS_AGENT_PARTITION`) livereload (no SKIP),
    deployments, deployBranches, sandboxes, agentHomes 20, agentHosted 24,
    agentMoves 17, channelsPartitioned 15. The film set: seeded with no
    failure, demoStills' 20 stills in both themes, and the T1 screenshots
    (`.film-media/theme-t1/{light,dark}`) with a real-theme audit of every
    screen (preview.js's walk, compared with each document's own computed
    tokens; no injection): no unexplained colour, font, corner or emoji —
    xterm's invisible input proxy and the CRM's columns (a mix of two
    tokens) are the allowlist. A third-party-style tile that links the
    sheet without opting in, light text hard-coded, stays Night in a light
    workspace. The audit's scanners: colour literals and font stacks only
    in the allowlisted files and `theme-ok` lines, emoji only in the class
    icon table, every corner on the token, 0 or the native renderer's, black
    shadows only as Night fallbacks.
  - **The review round (2026-10-03, before T1)** fixed what a review of the
    integrated branch found, with these decisions:
    - *A palette of a tile's own carries.* Rethemes through the old names
      are a documented path, and the `.bx` rules and core elements had
      moved roles onto new tokens with fixed Night values (a hover at
      1.13:1 under a light retheme's text, dark patches in a light
      dialog). Night now writes every role that sits on the old names
      from them — the hover (the panel 8.3% toward muted), the code well
      (the canvas 5% toward the text), the accent's hover (25% toward
      white) and ink (the page colour), links (the accent), the four status
      tints (12% over the panel), window chrome (the panel steps, the
      text, the border, the danger colour) — each chosen to compute to
      Base Two's Night value exactly (`theme-fallbacks.mjs` resolves
      `color-mix()` and checks; the fallbacks didn't change). Tokens with no
      exact derivation keep literals and are listed for rethemes to set
      (selection, focus, subtle, border-strong, syntax, diff, terminal);
      deriving one half of a pair (the selection without its text, focus
      without its gap) would only have made the pair illegible. Day keeps
      its literals, except what derives identically there (links, the
      title text, the close control, the old names).
    - *Old status names keep their old values where a document didn't opt
      in.* White text on `var(--bx-green)` was the old examples' own
      pattern; Base Two's brighter status colours took it from 2.78:1 to
      1.80:1. So a `:root:where(:not([data-bx-theme="auto"]))` block
      (`bx-compat`, checked to set nothing else) gives such a document
      `#4CAF50`/`#F2A71B`/`#EF5350` and makes ok/warn/danger follow them
      (info follows muted): a retheme of `--bx-red` reaches the core
      elements' errors as before. In an opted-in document the old names
      stay aliases of the status colours. Rejected: old literals in every
      Night with the status colours derived from them (Base Two's Night
      danger would have become `#EF5350`, 4.06:1 on its tint); status
      colours not following the old names (a light retheme's dialog error
      at 2.3:1). The compat pairs are linted to the floor they always had
      (4.5 on the panel; 3 on panel-2 and tints, where the old red was
      3.80 and 3.77 before Base Two).
    - *The `.bx` variants and sizes wait for the opt-in.* `button.primary`,
      `.quiet`, `.danger`, `:disabled` and the 28 px minimum applied to
      every `.bx` document and beat a tile's own classes (0,2,1 over
      0,1,1). They now apply only under `[data-bx-theme="auto"]`, at the
      base rule's specificity (`:where()`/`:is()`), so a later rule of the
      document's own wins. The rest of the `.bx` restyle (weight 600, the
      strong edge, the new face, 2 px, the focus ring) stays for every
      `.bx` document — it is the theme's look, legible and layout-neutral
      — and the changelog and `docs/changes/2026-10-03-base-two.md` say
      what such a document sees and how to keep its own.
    - *The shell never opts its host page in.* With the new shell on a
      root page from before D184 (only `scaffold:shell` updated, or a root
      of the workspace's own), the Theme and Density rows are disabled,
      showing the choice and saying `bx builtin update scaffold:root`; a
      pick can't happen. Rejected: setting `data-bx-theme` on the host at
      boot — a customized root's own token overrides would mix with
      Concrete Day. The changelog names the scaffold units (there is no
      update-all).
    - *Fonts are cached.* `/vendor/` answered `no-cache` without a
      validator, so every load refetched ~420 KB of fonts and painted the
      fallback face first. Every answer now carries a strong ETag (its
      SHA-256, cached per path by size and modification time) and `Vary:
      Origin` (a sandboxed frame's CORS answer differs from the page's —
      without it a cached immutable font of the page's was handed to the
      frame, which the harness caught); a font asked for with its version
      (`?v=` = the first 8 hex digits of its SHA-256, which `theme.css`
      writes and `hack/vendor.sh` rewrites) is immutable for a year. `.woff2`
      and `.txt` are registered MIME types, so the fonts' type no longer
      depends on the host's mime.types.
    - *The native reference renderer previews the app.* The iOS app still
      draws amber (`XbinPalette`, Tokens.swift), so `render-theme.js` went
      back to the app's palette and faces (the roles it gained for what
      other modules hard-coded stay, with the app's values); it moves with
      the app.
    - *The agent template's ▣* (sandbox badge, picker, terminal tabs, a
      coding agent's card, titles) left its strings; the web draws the
      `box` glyph beside them, the app's strings say "sandbox"/"in"; the
      export stays for an instance's own code.
    - *The lint* caught too little: it now flags the CSS named colours where
      a colour stands, `filter: drop-shadow()`, `-webkit-text-fill-color`,
      colours in SVG `data:` URIs (`%23` hex), any colour on a line that
      merely mentions a mask only outside the mask's own value, SVG
      `rx`/`ry`, relative and keyword sizes and a small size hidden in an
      undefined token's fallback, and the text glyphs §1.6 replaces; a
      `theme-ok` on the line before excepts the next line only when it
      stands alone. What it found was fixed (the welcome notes' unused
      `color` data, a docs glyph) or marked with its reason (the native
      step primitive's text glyphs, the feature catalogue).
    - **Verified (review round):** `make test`, `make fmt-check vet`,
      `make js-check`, `make js-test`, `make theme-check`; every harness
      pass on a fresh test seed, once on a dark and once on a light system
      (74 each, all green; agentSandbox now checks the drawn glyph), among
      them themeCompat (new: an own-palette tile's hover, dialog and code
      well, a third-party tile's own buttons and old-name fills, an
      opted-in tile's variants — light and dark systems), appearance,
      themeCanary, termTheme, tileAssets, scrollbars and windows; the
      passes needing `--isolate` (agentHomes, agentMoves, agentHosted,
      channelsPartitioned, livereload's node steps) skip there, and
      oldScaffold ran on its own without the overlay (now also this shell
      on the old root page); the T1 screenshots reshot on the film set
      (only the third-party tile's changed).
  - **Open:** the iOS app's colours and type (Tokens.swift `XbinPalette`,
    the reference renderer with it) and its deployments words
    (DeployView.swift) keep the pre-Base-Two look and emoji (the app is an
    open item of plans/theme-base-two.md §6);
    the film's camera overlay draws the old amber ripple
    (`hack/demo/cam/overlay.js`, the film's call); on a phone the alert bar
    (z 3500) covers the pop-up sheet, and at a font size other than 13
    anchored menus open offset (both P2's findings, older than D184); the
    agent template's browser tests `terminal.mjs`, `harness-term.mjs` and
    `native.mjs` time out waiting for a terminal on this machine, at the
    integrated commit as well (not looked into).

- **D185 — Base Two in the iOS app: the native renderer and the app's
  chrome on Concrete Day and Night, the M3 icon and mark (2026-10-04;
  brand/ios).** native/ios (XbinRendererModel/Tokens.swift, XbinRenderer/
  Theme.swift, App/Shell/BrandMark.swift, App/Terminal/TerminalLook.swift,
  App/Resources/AppIcon.icon and Fonts/), web/xb/render-theme.js. (The
  tile agent's guidelines, brand/web's half of the same request, are
  recorded under D183, so no number moved at the merge.) The owner, on
  D184's review: "we should
  restyle iOS app to be consistent with the web, keeping similar design
  language. For logo we have the new one". The app still drew its amber
  XbinPalette on iOS's grouped greys, and the reference renderer had gone
  back to that look so a preview matched the phone (D184 §6 Open).
  - **One palette, in the renderer's model.** XbinPalette holds theme.css's
    Day/Night values (surfaces, ink, accent and its ink, focus, status and
    tints, the partition marker, part tabs, the brand fields, the mark's
    colours, the chart series in the ANSI order, the terminal's palette) as
    pairs; XbinColor turns them into colours that follow the appearance, and
    the app's screens use the same roles. render-theme.js is Base Two again
    and ThemeParityTests holds it equal to XbinPalette, so a builder's
    preview keeps showing what the phone draws.
  - **The appearance follows the person.** A window takes the theme the
    person chose in the workspace it shows (the shell bucket's `theme`, the
    key xbind already injects into the tiles' pages; re-read on its `prefs`
    event), else the phone's. Before, a person who chose dark saw dark tiles
    inside light native chrome. The app never writes the key: the web's
    Settings → Theme stays the one place to choose.
  - **Faces: system for text, the brand's for two roles.** Body, controls
    and inline titles are the system font (Dynamic Type intact); navigation
    large titles, the renderer's `largeTitle` and the first-run stair are
    Bricolage Grotesque 800, terminals and `mono` JetBrains Mono (regular
    and bold, without its ligatures). The TTFs are made from
    web/vendor/fonts by native/ios/scripts/app-fonts.sh (the web's own
    glyphs; Bricolage's latin and latin-ext halves merged; JetBrains Mono's
    `calt` left out), bundled with their OFL texts and listed in
    Info.plist's UIAppFonts.
  - **Shapes: 2 pt on what the app draws itself** — cards (screens'
    cards, the launcher's boxes, the renderer's card groups, chat cards,
    notices, code, images, terminals, a widget's card), badges, the switcher
    plate and buttons (`.buttonBorderShape(.roundedRectangle(radius: 2))`).
    Navigation and tab bars, sheets, lists' inset sections, switches,
    segmented controls and search fields stay iOS's.
  - **Concrete under lists.** Every panel, the onboarding's pages and the
    app's sheets put the canvas behind their lists and forms
    (`concreteBackground()`: the list's own background hidden), so lists
    sit on the same concrete as the screens' cards; rows stay on the panel
    (iOS's own row colour, the panel's in light and a step from it in
    dark). An agent's transcript sits on the panel (product-ui 8).
  - **Part tabs** are a 3 pt rule on a card's top edge: green for a
    terminal, magenta for an agent session (the sessions screen's tabs and
    the launcher's boxes), yellow on the admin console's card (as the web
    canvas marks `tiles/admin`). User tiles get none.
  - **Glyphs: SF Symbols, mapped from the web's.** View models carry the
    web's glyph names (DeployView now ports deploy-state.js's emoji-free
    words with an `icon`: pin, live, refresh, shield); XbinGlyphs maps a
    name to the SF Symbol of the same meaning, and status is that glyph, a
    word and the status colour (a tile's status was a coloured dot). SF
    Symbols rather than the web's drawn glyphs as template assets: they
    scale and weigh with Dynamic Type and sit with the bars, menus and lists
    the system draws in SF Symbols; a second icon idiom inside the native
    chrome would be the inconsistency. Agents' sparkles go (brand §9): the
    agent glyph is `square.on.square`, SF Symbols' nearest to two linked
    squares.
  - **The terminal stays dark in both appearances**, now in Concrete
    Night's terminal palette (the web terminal's "Concrete Night"), with
    its dark keyboard and a key row of square keys; sticky modifiers take
    the terminal's accent. The renderer's `terminal` primitive does the same.
  - **The mark and the icon.** XbinMark draws M3 from the masters' path
    data (plans/brand/marks), XbinLockup sets it with wordmark A in the text
    colour, on the Welcome and on Log in. AppIcon.icon is the cobalt fill with the white b and the yellow
    x as two flat layers (no specular, no translucency); its Dark look is
    the Default's (the fill given for dark too), and the clear and tinted
    looks stay the system's derivations (native/AGENTS.md), checked with
    ictool.
  - **First run** is the one place the stair appears (product-ui 10):
    "Your apps / on your / phone." on yellow, green and magenta over the
    definition, then the four ways in, which stay (installing xbin is a
    product function, labelled "Install xbin" as on the site). The About
    pages follow the voice rules.
  - **The review round (2026-10-04)** found eleven things in the app; all
    were fixed on brand/ios before the merge:
    - *The terminal's bright colours.* `useBrightColors = false` doesn't
      mean "bold is weight" in SwiftTerm 1.20: AppleTerminalView.mapColor
      then maps every indexed colour above 7 to index − 8 in the bold face,
      so SGR 90–97 lost their bright colours, bright black (the zsh and
      bash prompts' path) was drawn as black on the background, and every
      256-colour code from 8 up took the wrong entry. The line is gone and
      SwiftTerm's default stays. **The one deviation from product-ui 7:**
      bold text in colours 0–6 also takes the bright colour, as most
      terminals draw it; SwiftTerm has no option for bold-as-weight alone
      (an upstream change would be the way to drop it). The theme gallery
      now checks the bright black on screen (pixels of `#5C5F70`).
    - *Ligatures.* SwiftTerm and SwiftUI apply a font's default features,
      so JetBrains Mono drew `->`, `!=` and `=>` as joined symbols in the
      terminal and every `mono`/code text, against D184 and the web's
      `"liga" 0, "calt" 0`. app-fonts.sh now leaves `calt` (and `liga`) out
      of the bundled faces, which BrandMastersTests checks; the reference
      renderer's mono rules say `font-variant-ligatures: none`.
    - *The icon's Dark look.* With a fill for the default appearance only,
      iOS derived a Dark of its own: a near-black tile with a cobalt b and
      the yellow x loose on it, which the mark's rules forbid ("the tile
      never changes between themes", "never draw the b and x loose",
      "yellow touches only cobalt"). The fill is now given for dark too
      (`fill-specializations`, cobalt in both); ictool renders the Dark
      look byte for byte the Default, and Clear and Tinted as before.
    - *Shapes and colours the first pass missed.* The proposal sheet's
      Reject draws in the danger colour (it drew the accent, the one
      primary action's colour); the bordered buttons left as capsules
      (the selection bar's nudges and All, the tabletop keys and Keyboard,
      the chat question's Skip and Submit) are square, and the tabletop
      keys take the key row's face. The reference renderer's attachment
      chips and thumbnails are square as the app's are; a person's chat
      turn has a 1 pt edge in both renderers (panel-2 alone is 1.06:1 on
      Day's panel), as the web's agent chat draws it. The New tile, Add a
      tile, proposal, terminal sessions and tile dialog sheets sit on the
      concrete like the other sheets.
    - *Copy.* "Run your own xbin" named who runs xbin (D183, brand §17):
      the button and its page are "Install xbin", as on the site, and the
      page no longer says what kind of machine it runs on.
    - *Drift guards and records.* XbinBrandPaths (the mark's and wordmark
      A's path data) moved into XbinRendererModel, where BrandMastersTests
      reads the masters and the icon's layers and fills, as
      TestBrandMarkIsTheMasters does for the web's copies; the marks README
      and the swap recipe list the app's copies. The app stubs lost
      `Color.xbinAmber`, docs/native.md's `badge` is square, and
      plans/native.md §10.1 holds Base Two's roles.
    - *Beyond the review: the sparkles left.* Brand §9 says "never …
      sparkles", and D185 said the agents' sparkles had gone, but the
      renderers still chose three: the thinking header's glyph (iOS,
      animated while live), the think tool's icon (the agent screen) and
      an `accent` notice's glyph (both renderers). Thinking is its label
      and chevron in both, without motion (the reference's shimmer went
      too), the think tool draws the chat bubble (the web's is a thought
      glyph) and an accent notice the info glyph. A tile that names
      `sparkles` in its own tree still gets one: that is its content.
    - *Beyond the review: the chat's pills.* The composer, in both
      renderers, was a 20 pt pill with circular send and attach buttons
      and capsule attachment chips, beside the web agent tab's square
      field and buttons; the "jump to latest" pill and the notes the app
      floats over a tile (a native view's fallback, an upload, a
      terminal's) were capsules on glass. All are 2 pt plates now: the
      field on the panel with a 1 pt edge, 36 pt square buttons as the
      reference draws them (send in the accent while there is something to
      send, else the neutral fill with the arrow muted; stop filled in the
      text colour; attach on the neutral fill), the notes on the panel with
      an edge. The sheet grabber stays iOS's. (A first cut used the
      system's prominent button for send: disabled, it drew a near-white
      square on the light bar and a black one on the dark, so the empty
      composer looked as if it had no send button; the merged tree's
      gallery showed it.)

- **D186 — Projects in the agent template and the scm contract
  (2026-10-05).** builtin-templates/scm-github; docs/scm.md; the agent's
  `_backend/project_*.go`, `scm*.go`, `projects_coord_*.go`,
  `project_team*.go`, `ci_*.go`, `projects_types.go`, `projects_seams.go`,
  `scm_types.go`; its model/project*.js, projects.js, ci-dock.js,
  native/projects.js, native/ci.js and siblings; API.md §Projects;
  plans/projects-scm.md and its records.
  - **The owner's rulings (2026-10-02).** A separate provider with a
    repo-hosting contract (credentials required, the rest optional); tile
    `scm-github`, contract and service `scm`; a builtin template,
    partitioned (`user`, `global`), bot-only unpartitioned; personal
    projects in a person's partition with their own sign-in, only in a
    private sandbox of theirs; team projects: a definition and board at
    global, each task in its creator's partition (coding agents stay
    barred at global, D172); worktrees in one sandbox, big tasks fork it;
    screenshots in PRs deferred (GitHub has no public upload API); CI
    visible and inspectable in the coding UI (D147's chip and dock), down
    to steps and logs; "run as root" left to the sandbox track's decision
    (ACP has no such field); the whole scope. At the freeze (2026-10-03)
    none of the vetoable defaults V1–V18 was vetoed; scm-github's `net`
    is plain `internet` (the narrowed list documented, which must include
    the Actions log hosts); the channel attach (V10) may slip — it did.
    After the live checks (2026-10-04) the owner kept the **epoch**: a
    person's scoped tokens end at their parent's expiry less an hour,
    although S2 showed a scoped token outlives its parent's refresh — the
    cap bounds how long a token lives after a sign-in moves on.
  - **The owner's rulings at land (2026-10-05).** Every default built for
    the open owner questions 1–33 in records/LAND.md is accepted as built.
    34 (S1 live: the manifest form from a sandboxed frame's new tab) and
    35 (webhooks against real GitHub) stay open: not run.
  - **Why a contract and a provider:** the provider knows the host and the
    credentials, the consumer knows who, why and which sandbox; a person's
    sign-in lives in their partition, the App's keys at global; the agent
    never sees an App key or a refresh token. Global holds the client
    secret, so global scopes and revokes, recomputing permissions,
    accounts and repos itself from a relay body the person could have
    written; a partition refreshes itself. The identity directory comes
    from GitHub's answer for a token only this App issued, never `GET
    /user`. A partition's token cache follows global through conf
    `public` (the policy's public half and a `tokenGen`). Not chosen:
    people's tokens at global (one place to steal every sign-in), an
    OAuth web flow (the client secret in every partition), no caching in
    the partition (a relay and a mint per git operation).
  - **Credentials:** short-lived, scoped to the project's repos, written
    0600 outside every repo, kept in memory and the sandbox only
    (`project_creds` holds who, until when and why, never the value),
    rotated by one loop only while a task works, scrubbed on share, stop,
    archive, delete, fork, snapshot and Forget, redacted by shape and by
    value everywhere output is kept (same-length masks); a person's only in
    their own private sandbox homed in their partition and never one a
    hosted conversation used (that use is refused while a credential is
    live). The gate reads the sandbox as its manager reports it at every
    write. Scrub on share blocks; revocation is best effort. A GitHub App
    whose user tokens don't expire is refused at sign-in: nothing could
    rotate or end them. Not chosen: a vault entry per project.
  - **Authority over the bot:** where a home's identity is the bot
    (global, unpartitioned), naming a repo takes a manager or the scm bot
    rule; a project reads only its own repos thereafter; a partitioned
    global holds team definitions only; reruns are a person's own, never
    the bot (V12). A team's seed holds no credential and a sandbox cloned
    from it takes only the bot; a membership runs the definition's setup
    and policy only as its member accepted them, by hash of exactly what
    was shown; a project task's class never has internal reach, checked in
    `classOf` itself. The bot's own writes use internal tokens never
    handed out — including, for draft ↔ ready, one with `contents: write`
    because GitHub's GraphQL requires it.
  - **A project's sandbox and its people:** tasks bind with the project
    owner's authority and the owner takes part in every task; so a shared
    project has its sandbox to itself (409 `sandbox-shared`) — the
    credential gate judges a sandbox by its own users and can't see
    people who reach it through another project. A person removed from a
    project keeps reading their tasks and nothing more. A project's runs
    refuse publish, copy, hosting and moves (V9).
  - **The pipeline:** one worker per engine owner with jobs in the
    database, epoch-fenced, execs found again by `clientId`; the gate
    parks and never consumes, ending its park itself and poking (never
    queuing a wake that could be left over); slots count runs, from
    delivery until taken up; archiving holds rather than fails. Big tasks'
    forks are made inside `prepare` from a fork base taken while the
    sandbox is quiet (agent and manager both asked), with no live token in
    a snapshot or fork, tracked in a registry until deleted. The pr job
    pushes one explicit refspec and never a default branch; auto-PR is off
    by default (V7). An upgrade fails closed (internal data, shared, busy).
  - **Events:** webhooks at global through an outbox (GitHub never
    retries), foreign accounts dropped first, a person's subscriptions
    only through their partition and their private-repo access re-read
    before each event, fork branches never reported as the repo's,
    passed-through text redacted. In the agent one path for events and
    polls: an event schedules the read polling makes, acted on with
    semantic keys; polling in an owner loop, not jobs; the intake takes
    only the provider bound in `scm`, at its global instance.
  - **The coordinator** is made only when its person asks, acts with the
    tile level "read", holds the park on both ends so it never answers
    for a person, and keeps a stable prompt; project events go to one
    coordinator (the task's creator's).
  - **CI:** one dock with sections (the D147 board hosts `ext.dock`);
    summaries kept in memory per (database, root) to avoid a second
    handle inside P1's transactions; pushed branches from git's reflog in
    the pushing run's own sandbox; reads shared and conditional, the 15 s
    re-read only while someone looks (V11); logs only for completed jobs
    on GitHub (S4, checked live).
  - **Not chosen:** a run column for tasks (origin `project` instead);
    project tools as a class toolset (the stored-classes rollback; gated
    on `Config.Project`); a team coordinator at global; a merge route;
    polling as the primary event path; a second right dock for CI; a
    `projects.coord_run` column (the session key); a route moving a
    seed-cloned membership to a fresh sandbox.
  - **Slipped:** the channel attach (`/project <name>` in a DM, V10) — it
    needs the channel rule, the partition's `handoff/dm` consumer and the
    coordinator's replies to the chat; a follow-up.
  - **Defaults the owner may veto:** §2.2–§2.3 of the spec as landed, and
    the open owner questions in records/LAND.md, each with the default
    built.
  - **Owed:** S1 and webhooks against live GitHub; a SAML/SSO
    organization, GHES, a protected branch, a spent rate limit; the UI
    harness passes for Projects; the native view on Apple CI.
  - **Verified:** each WP's named tests (projects-scm §15.2), gate 1's
    `TestSeededTokenNeverStoredTask` (fails without either redaction
    layer), the event v1 literal pinned on both sides, scm-github against
    real GitHub with a test App (`TestLive*`, run by hand; its answers in
    `testdata/github-live.json`, `TestFakeMatchesLiveGitHub`), the agent
    and scm-github `-race` suites, the node tests and `make check` (known
    failures in records/LAND.md).

- **D187 — The shell's Document display mode, and the screen tabs in the
  top bar (2026-10-07).** workspace-template/shell/doc-layout.js,
  shell-doc.js, shell-tabs.js, bx-canvas.js, bx-shell.js, menus.js,
  shell-css.js; web/bx-frame.js; internal/broker/screens.go;
  hack/doc-layout.test.mjs, hack/ui-harness/passes/docmode.js;
  docs/frontend-kit.md §Document mode, elements.md, protocol.md,
  design.md. (The same plan's grant-strip dismiss and per-device
  light/dark override are another change.)
  - **The owner's answers (2026-10-07).** The mode is **per screen**, and a
    shared org screen carries its mode for everyone. In Document mode the
    top bar is **revealed on demand** — at the top edge, or when the page
    scrolls up — and hidden otherwise.
  - **Data, never breaking a saved layout.** A screen gains `mode: "doc"`
    (absent = canvas): in the `layout` pref for a personal screen, and as
    an optional `mode` on the org screen for a shared one. A tile gains an
    optional `doc: {row, col, cols, h?}` beside its canvas geometry; the
    canvas fields (x, y, w, h, float) are never written by Document mode,
    so switching back shows the canvas exactly. A tile without `doc`
    (opened on the canvas, by an older shell or the app) is a row of its
    own after the placed rows, in reading order (y, then x); `doc` is
    written only when someone arranges the rows, or opens a tile on a
    Document screen (it becomes the last row). Older shells and the app
    ignore both fields and re-save them untouched (they spread the tile
    and screen objects).
  - **Org screens.** `mode` is admin-plane like the name: org admins and
    ws-admins change it, it never bumps the revision (no conflict with a
    member's draft), and a PUT that leaves it out keeps it — an older
    shell publishing tiles never resets it. `canvas` is stored as absent,
    so a screen nobody switched reads exactly as before. The switch is
    meta, outside the draft: arranging the rows is a tile edit and goes
    through the draft as on the canvas.
  - **Rows.** 1, 2 or 4 wide — nothing else, so every row sits on one
    4-column CSS grid (a cell spans 4/cols columns; a 1-wide row's card
    is centred at most 1100 px, wider rows use the full width). Cells keep
    a stable DOM order (by path) and the grid places them, because moving
    an iframe in the DOM reloads it: rearranging rows reloads no tile.
    Below 820px the cells go one column in reading order (`--ord`).
    Making a row wider pulls the following tiles up into it; narrower
    pushes the overflow into new rows of the new width below; dropping a
    card beside a full row's card widens it 1 → 2 → 4, and a full 4-wide
    row pushes its last tile down. Floats stay floating windows, outside
    the rows. The keyboard path is the tile menu's Row submenu (1 · 2 · 4
    columns, Move up / Move down: a tile alone swaps rows, one sharing a
    row leaves it as a row of its own) — the drag is not the only way.
  - **Growing to content.** The doc card renders `<bx-frame>` without
    `height` and its body unclipped; the frame takes the height the
    document reports (xbin-client's `xbin:resize`, the html element's
    height, 24–20000 px). bx-frame's auto-height became reactive (the
    `height` attribute or a style height switches it in place, and a frame
    turning auto takes the last reported height) instead of decided once
    on connect. xbin-client is unchanged: reporting `scrollHeight` would
    make a `min-height: 100vh` document grow without bound. A document
    pinned to its viewport (terminal, chat) reports the frame's own
    height, so it settles at the initial 480 px (a CSS default on the doc
    card's frame); every doc card has a bottom handle that sets a fixed
    `doc.h` (two clicks clear it, as does Row ▸ Fit height to content).
    Every card gets the handle, not only the ones that can't grow:
    telling them apart from the outside is guesswork.
  - **The top bar in Document mode** (wider than 820px; a phone keeps its
    bar, which holds the menu button): fixed over the page, it starts out
    of the way and slides back on a scroll up, at the top edge (an 8px
    strip catches the pointer — over a tile the pointer's moves go to the
    tile's document, never the page), while focus is in it and while a
    menu is open; a scroll down sends it away. The motion is
    `--bx-dur-panel` (0 under reduced motion). Hidden it is
    `transform`ed; shown it has no transform, so the settings menu's
    viewport-fixed backdrop still covers the page.
  - **The tabs in the top bar** (all modes, wider than 820px): between the
    mark and the settings chip, scrolling sideways (the wheel too, with no
    scrollbar drawn) and keeping the active tab in view only when the
    active tab changes, so a strip scrolled by hand stays put. Below 820px
    the strip stays its own row. The rendering and the screen actions
    (add, rename, close, hide, reorder, the Layout lines) moved to
    shell-tabs.js — bx-shell was at its size budget (1888/1892), now
    ~1810. A tab's right-click menu is new: Rename…, Layout, Close/Hide.
  - **Not chosen:** a "new screen" dialog with the layout choice (there is
    no dialog; a new screen is a canvas and the tab menu switches it);
    Document mode's own scroll container (main stays the scroller, so the
    grant strip stays the page's first row); detecting tiles that can't
    grow; moving rows by drag on a phone (it reads; the menu arranges).
  - **Verified:** hack/doc-layout.test.mjs (rows, placement, 1/2/4
    changes, moves within and across rows, reading order, canvas geometry
    untouched by every edit), menus.test.mjs (Layout and Row lines),
    `TestScreensOrgMode`, and the harness `docMode` pass in Night and Day
    (one tile per row; a 2-wide row by dragging, a 4-wide one from the
    menu; a tall tile with `scrollHeight == clientHeight` inside; a
    viewport-pinned tile at 480 px, its handle and its reset; the bar on
    scroll down/up and at the top edge; the phone stack; the canvas
    identical after switching back).

- **D188 — The grant strip: dismiss, and no "N grants active"; a
  light/dark override per device (2026-10-07).** web/bx-dismiss.js,
  web/bx-grants.js, web/bx-bindings.js, the shell's `_loadPendingCount`;
  web/bx-theme.js, web/theme-boot.js, web/bx-frame.js,
  workspace-template/shell/shell-appearance.js, the root page template,
  internal/server/appearance.go. Sections 2 and 4 of the 2026-10-07 shell
  plan (Document mode and tabs in the top bar are D187).
  - **The owner's answers (2026-10-07).** Dismiss is for that person on
    all their devices, stored in the shell's prefs; nothing changes for
    other admins or for the tile. "N grants active" goes; the admin
    console's grants and binding tabs remain the full list. The light/dark
    override is per device: this browser shows Light or Dark whatever the
    person's Theme says (a dark laptop beside a light office screen).
  - **Dismiss:** the shell pref `dismissed`, `{grants:{"from|target|role":
    at}, bindings:{"component|slot|kind": at}}`, read and written by the
    two elements through `/api/xbin/prefs` (the bucket `xbin.fetch`
    reaches: the shell's in the root page), followed live through the
    `prefs` event. Keys, not the time asked: grant requests and unbound
    slots are recomputed from manifests and carry no time, so a request
    that changes role or target is a new key and shows again. Dismissals
    of what the server no longer lists are pruned when an element has
    read both, as keepDismissed does (D163) — a request that disappears
    and comes back (a manifest edited twice) shows again, the price of not
    keeping keys forever. Each write is a read, change and write, so the
    two elements' kinds don't undo each other (a last-writer race between
    two devices dismissing at the same instant can lose one dismissal; the
    item just shows again). "dismissed (N) · show" restores that
    element's hidden items at once, not one by one. The organisations
    badge counts through the same module (`pendingCount`), less the
    dismissed; people's access requests (D36) have no dismiss. The
    elements dismiss wherever they are used: a builder's page that embeds
    `<bx-grants>` keeps its own dismissals in its own bucket.
  - **"N grants active" removed** with its list and revoke; the scoped
    ("mine") view already hid it. `<bx-bindings>`' "N interface bindings"
    line stays (not part of the answer; the ingressMulti pass drives it).
  - **Device override:** localStorage `xbin-theme-device` (`light` |
    `dark`; absent = follow the person) — never synced, so not a pref.
    Precedence device → person → system (`effectiveTheme`). In a document
    that follows the person the theme meta holds what is painted, so
    `<bx-frame>`'s relay and everything reading `appearance()` see the
    device's theme without change; the person's own choice is set aside
    on `<html data-bx-theme-person>` for the Theme row and for following
    another device's change (`setPersonAppearance`). The hint cookie
    equals the device's theme while it is set (`rememberTheme`), so the
    sign-in pages follow it on that browser. Before the first paint:
    `/vendor/theme-boot.js` in the root page template (its first use in a
    root); a root from before gets it when shell-appearance.js loads (a
    flash, never wrong after). Tiles: credentialless frames get no cookie
    or storage, so `<bx-frame>` adds `?xbin-appearance=light|dark` to the
    tile's URL and the existing appearance injection (the one sanctioned
    transform, extended, not a new one) uses it in place of the person's
    theme; the query survives the tile-origin redirects. Not chosen: a
    cookie the server reads for tiles (none reaches a credentialless or
    tile-origin frame), a synced per-device pref (needs device identity
    the shell doesn't have), the shell rewriting tile documents.
  - **Verified:** `TestAppearanceDeviceOverride`; hack/bx-dismiss.test.mjs
    and the device tests in hack/theme-appearance.test.mjs; `make js-test`
    (830 tests, 829 pass, 1 skipped), fmt-check, vet, js-check,
    theme-check; `go test ./internal/server/... ./internal/broker/...`;
    the UI harness's new grantDismiss (15 checks) and deviceTheme (17)
    passes under a dark and a light system, and appearance (61),
    themeCanary (12, 2 SKIP: no gocryptfs), layoutSync (17),
    adminSettings (25), windows (20), menuOpen (6), tabStrip (6), viewAs
    (11). gridScale's zoom-tip check fails here on master's code too (a
    headless Control+= never reaches the shell); not this change.
  - **Follow-up (owner, 2026-10-08).** The strip vanishes entirely unless
    something not dismissed waits: no "N interface bindings" count and no
    per-element "dismissed (N) · show" line — the settings menu's "Show
    dismissed (N)" restores every kind at once. Warning banners join the
    store as a third kind, `alerts`, keyed `<kind>|<message>` (changed words
    show again); every banner has Dismiss (the server's route when it has
    one, else the person's), and the banners are in the page's flow, one
    line each, never over the top bar and tabs (they were a sticky strip at
    z-index 3500 that covered them, wrapping long trust warnings).

- **D189 — The native vocabulary's revision 2: navigation (2026-10-07).**
  web/xb/vocab.js (→ native/spec/vocab.json), web/xb/rt-build.js,
  web/xb/rt-runtime.js + web/xb-native.js (`xbn.navigate`), the reference
  renderer (web/xb/render-*.js), XbinRendererModel (Navigation2.swift,
  ScreenLayout.swift, Vocabulary.swift) and the SwiftUI views, XbinCore
  (`RuntimeCall.navigate`, `TileScheme.runtimeURL(…fragment:)`), the app
  (NativeTile.swift, RuntimePool.swift, TileScreens.swift). Part A of the
  "AgTT on iOS" plan; the agent template's list-first view (part B, D190)
  builds on it.
  - **Why.** The owner (2026-10-07): the agent template's iOS view "is
    quite lacking, especially navigation". The gap was the app runtime:
    deep links never reached a native view, a compact `split` stacked its
    panes, no toolbar placement, trailing swipes only, flat menus, a
    refresh that ended after a fixed 600 ms, search without submit or
    scopes, no stacked or full-screen sheets, no scroll anchor.
  - **One revision, additive.** Every addition is `since: 2` on its
    primitive (`screen`, `toolbar`, `list`, `actions`, `sheet`, `split`,
    `menu`, `transcript` go to rev 2). The schema gains two markers next to
    `since`: `enumSince` (a new enum value, `detents: full`) and a child
    rule's `since` (a `menu` inside a `menu`); listening to a rev-2 event
    counts too. The runtime reports each as `unsupported` to a rev-1 app,
    which shows the web page — so tiles branch on `supports(name, 2)`.
    Not chosen: feature flags per addition (eight flags for one coherent
    step, and `supports('split', 2)` reads better); silently ignoring
    rev-2 events on old apps (a tile would wait for a `close` that never
    comes). A rev-1 tree draws exactly as before on a rev-2 app: a `split`
    without `detail` still stacks on a phone; a toolbar without `place` is
    the trailing one; a single `actions` keeps its reversed swipe order.
  - **The additions.** `split detail` (compact: a stack, the detail pushed
    while true, Back → `close` reporting `detail: false`) and `columns`
    auto·all·detail (`columns {value}`); `toolbar place`
    trailing·leading·bottom (one toolbar per place); `actions edge`
    trailing·leading and `full` (a full swipe runs the first button, then
    outermost); `menu` children may be menus (submenus); `screen
    refreshing` (the spinner waits up to 1 s for it to turn true, then
    until false, at most 60 s; unbound: 600 ms as before), `submit
    {value}`, `scopes`/`scope` (`scope {value}`), `suggestions` (choosing
    one sets the query and submits); `sheet detents="full"` (a full-screen
    cover) and a `sheet` inside a `sheet` presented over it (structural,
    no marker: rev-1 apps already presented a nested sheet, if crudely);
    `list`/`transcript` `anchor` (a child by its `key=` or wire key: the
    view opens at it and keeps it in place; a following transcript stops
    following meanwhile), `scrollTo` (a key or start/end, acted on when
    the value changes — its first value is the start, not a jump; text
    after `#` only makes a new value) and `edge {edge, at}`.
  - **Not as planned.** The plan named `select`/`close` events for the
    split; `select` was left out — opening the detail is the row's own
    `tap` setting `detail`, and a second event for it would only duplicate
    it. Toolbar placement is a prop of `toolbar`, not of each item: items
    of one place group together and a button never carries a place it
    can't use outside a bar.
  - **Deep links.** The app loads the runtime document with the link's
    fragment (`?native=1#c=42`), so a tile reads `location.hash` as it
    starts; a link to a running view calls `xbn.navigate(hash)`, which
    rewrites the hash (`history.replaceState` where it can, so the browser
    fires nothing itself) and dispatches one `hashchange` — also for the
    same hash. A link before the first tree waits for it. No caps flag: an
    older app simply opens the view at its root.
  - **Nested stacks.** A tile's own stack inside the app's panel stack owns
    Back while it can pop: the panel's edge pan yields to a
    UINavigationController under the finger that has more than one view
    controller (PanelStack, `innerStackCanPop`). A rev-1 `nav` keeps its own
    NavigationStack inside the panel's, and push/Back/push/Back work on the
    device (XbinNavigationTests test02). A collapsed `split` in the app
    pushes its detail onto the panel's own stack
    (`navigationDestination(isPresented:)`, `XbinRenderOptions.hostNavigation`)
    instead: one bar, the system's Back. Two app bugs this found, both
    fixed: the tile's screen disappearing under a page its own tree pushed
    was taken for the tile closing (its runtime unpinned, hatches stopped),
    and reappearing re-opened the screen's deep link — so Back from the
    deep-linked detail came straight back to it. Now a disappear while the
    window's surface is still this tile keeps everything live, and a
    screen hands its fragment to the runtime once (`NativeHostedStack`).
  - **Presenting over the app's stack (2026-10-08, QA of v0.3.69).** A
    destination of the app's stack covers the tree's root, so what the
    tree presents from there must be presented from the screen in front.
    Confirmations: each carried destination installs its own
    `ConfirmHost` (the tree's one, at the root, showed nothing on a pushed
    screen). Sheets: the runtime's top level is the one node while it is
    alone and a `fragment` once a sheet joins it (rt-build.js), and the
    renderer drew the two differently — the split was drawn anew as Share
    opened, and the new one dropped the destination the old one pushed
    (`close`, the conversation gone under the sheet). Now the top level is
    drawn as a fragment's content either way (`TopLevelView`), the sheets
    come and go inside a background instead of re-wrapping the content
    (`SheetsModifier`, `DrawersModifier`), and a split that is all the
    tree draws presents the top-level sheets itself: over itself, or —
    collapsed onto the app's stack — from the list or the top pushed
    screen (`SplitSheets`). Not chosen: ignoring the split's binding
    writes around a sheet (the destination is really gone by then; the
    binding only reports it). XbinAgentNativeTests test08/test09.
  - **Verified:** see the commit messages of the native-rev2 branch.

- **D190 — The agent's native view: list-first, a navigation stack
  (2026-10-07).** builtin-templates/agent/native.js, native/nav.js,
  native/convs.js, native/home.js, native/chat.js, native/tools.js,
  native/projects.js, native/project-task.js, native/harness-board.js;
  API.md "The frontend"; plans/agent-template-native.md §3. Part B (items
  1, 2, 5, 6) of the 2026-10-07 "AgTT on iOS" plan. **Supersedes the
  one-surface rule** (plans/agent-template-native.md §3 and §9.4; D96's
  "one surface at a time" and its conversations drawer).
  - **The owner's answers (2026-10-07).** List-first on the phone: chats,
    tasks and runs are pushed and Back returns; split on iPad and the Duo
    (a follow-up, B3); the root is the conversations list. Parity covers
    the four declared gaps (B4, a follow-up); composer keys, paste and
    drop stay the app's composer's.
  - **The stack is the native layer's, the selection the model's.**
    `ui.stack` holds route entries `{kind, …}`: *places* — a conversation
    `{kind: 'chat', run}`, the new chat screen `new`, the Automations page
    `auto`, Projects `projects` / `project` {pid} — and tool screens over
    them (memory, files, settings, the Coding agents board, the seams'
    screens…). The list is the root, never an entry. `model/app.js` keeps
    its one `sel`/`page` (the web view is unchanged); `nav.follow()` runs
    before every draw and makes the model match the topmost place (home
    when there is none, or the new chat screen). History in the model was
    not chosen: the web has no use for it, and two views would have to
    agree on what Back means.
  - **How it moves.** `app.select()` from anywhere (a list row, Needs you,
    a board row, a task row, an automation's run, a subagent's card, a
    started chat) pushes — the `select` event; a chat started from the
    new chat screen takes that screen's place. `open()` goes back to a
    place already on the stack instead of pushing it twice (a task's
    Project: ‹name› returns to its board when it came from there).
    `app.home()` on the model's own (deleted, revoked, unreadable, moved)
    takes the conversation off; a conversation that never opened is
    replaced by the one it moved to. The nav's `pop` cuts the stack.
    Events the native layer causes while it drives the model are ignored
    (a `driving` count), so only the model's own moves reach the stack.
  - **Subagent chains.** A subagent opened from its parent's card is
    pushed over the parent (Back returns to it). One opened from the list
    or a link gets its parents inserted under it once its view names
    them (`v.chain`) — unless one of them is already on the stack. A
    conversation under the open one is drawn as last seen (the session
    holds one open view); Back makes it the open one and reads it again.
  - **Deep links build the stack** (`nav.link`): `#c=42` is [list, chat
    42], `#auto=kind:id` [list, Automations, the automation], `#proj=7`
    [list, Projects, the project], `#join=` joins and pushes. A hashchange
    (the app's navigate, A1) builds it afresh. **A restarted runtime**
    comes back to the same stack: `xbin.native.saveState({hash, nav})`,
    each entry reduced to the ids its kind needs; a kind that isn't
    saveable ends the saved stack (a form comes back as the screen it was
    pushed from). State from before (`{hash}` alone) still opens.
  - **The root.** Search (a pasted invite link joins), Needs you, the
    partition and halt notices, the Mine / Shared / Archived scope,
    pinned, by date; row swipes and menus; New chat and ⋯ (Automations,
    Projects, Coding agents, Settings, the brake) in its bar — Projects
    moved from a drawer row to `ext.main`; `ext.drawer` rows still draw,
    at the top of the list. New chat is a pushed screen: the greeting,
    Try, Who answers, the class/model/sandbox pickers, the composer, ⋯ →
    New chat with options….
  - **Older apps.** Everything here is vocabulary rev 1 (`nav`, `screen`,
    `row`, `sheet`), so every app gets it; no `supports()` branch is
    needed yet. `nav.rev2(name)` is the gate the rev-2 parts will use
    (D189): native.js `layout()` is where the iPad/Duo split goes, drawing
    the same stack as list | detail. The parity catalog gains `link.back`
    (native: the stack; web: a declared difference — its panes sit side
    by side and its address is replaced, not pushed).
  - **Verified:** `node --test hack/agent-template-*.test.mjs` (315 pass;
    new: the list root, a deep link building [list, chat] and a
    subagent's chain, push/pop for a subagent, a board row (from the list
    and from a conversation), a project's task, an automation's run, a
    restored stack); `make js-check js-test theme-check native-check`;
    `test/native.mjs` in Chromium (the list, New chat, push and back);
    `test/native-shots.mjs` and iPhone-size shots of the list, a pushed
    chat, a project's task, New chat and a pushed subagent, light and dark.
  - **B3/B4, the same day: the iPad/Duo split and the four parity gaps.**
    Every rev-2 use is gated (`nav.rev2(name)` = `supports(name, 2)`); an
    app of rev 1 keeps the phone stack and today's behaviour.
    - *The split* (`native.js layout()`, `supports('split', 2)`): the list is
      the split's primary column, the stack a `nav` in its detail one;
      `detail` is "the stack is not empty", `close` cuts it, the detail
      nav's `pop {depth}` is the stack's depth + 1, `columns` is kept in
      `ui.cols`. With nothing open the detail column is the new chat screen
      (the web's home pane) — on a phone it is simply not pushed. The tile
      never learns the width: the same tree is two columns on an iPad and
      a collapsed stack on a phone.
    - *Terminals as tabs.* The app's `terminal` closes its socket when it
      leaves the tree (and `tabs` materializes only the shown tab), and it
      reports no exec id — so the **relay** keeps a person's terminal per
      tab name (`tab=<random name>` on both relays): a dial of the tab
      attaches to it while it runs, it outlives its client (30 min with
      none), and the tab's Close ends it (`DELETE /terminals/{tab}`). The
      tabs are the web dock's model (`model/terminals.js termsOf`), one
      Terminals screen (`tabs style=bar`, New shell, Close tab); Back
      leaves them running and Terminals (N) in the ⋯ menus returns. Not
      chosen: a runtime change to keep a hidden terminal's socket open
      (the runtime is another track's), or a session id event on
      `terminal` (same). The sign-in terminal stays its own pushed
      screen (its flow retries and leaves).
    - *Ports*: a screen from the ▣ Sandbox screen — the previews probed
      (`GET /runs/{id}/ports`) with Open, and a probe of any port.
    - *Partition forms* (`native/homes.js`): Share a copy (its row's and the
      chat's ⋯), and in a shared conversation's share sheet Copy to my own
      space, Use my private resources… (the warning's sheet, then
      `POST /hosting`; its host's Take them back), Add a copy of my files…;
      Who can see it in new chat with options. The web's wording;
      `shareOf` moves to `model/homes.js` for both views.
    - *Jump to latest* (`supports('transcript', 2)`): away from the end
      (the end's `edge`), the row the window grew up from (each `more`) is
      the transcript's `anchor`; a window longer than 120 rows below it is
      cut to 60 and `Session.keep(…, canDetach)` lets the live tail go (new
      messages counted: `fresh`). The pill is a composer button ("↓ N new —
      jump to latest", the web's words): `Session.latest()` and
      `scrollTo="end#n"`. Reading down to a cut window's end grows it a
      page (`loadNewer` when the tail was let go). No anchor at the end, so
      `follow` sticks there as before. An app of rev 1: no anchor, no pill,
      nothing below the window let go (D130's rule).
    - *Child cards* get steering: an `approval` with `feedback` in the open
      card's transcript (Send, Send now, Stop, Cancel task… confirmed by a
      second approval), on the child's run; the outcome is said in its
      text. A `toolcard` may hold no button, so this reuses the card the
      vocabulary has — a runtime follow-up could give `toolcard` an
      `actions` child; its shield icon and "Feedback" placeholder are the
      renderer's.
    - *Composer drafts per place* (`ui.drafts`, keyed by run id, `'new'` for
      the new chat screen): a send clears the draft of the place it was
      written in. Native only — the web keeps one box (no catalog key).
    - Rev-2 niceties on the list: Pin as a leading full swipe, Return
      searches at once (`submit`), the refresh spinner held until the list
      is read (`refreshing`).
    - `DIFFERENCES.native` keeps only `composer.keys`, `.attach.paste`,
      `.attach.drop`. The node tests play an app without the rev-2 split
      by default (`hack/agent-template-native-caps.mjs` PHONE; the D130
      window tests CHAT1, a rev-1 transcript); the new
      `agent-template-native-parity.test.mjs` plays FULL and OLD.
    - **Verified:** `node --test hack/agent-template-*.test.mjs` (326
      pass), `make js-check js-test theme-check native-check` (js-test 866
      pass), the backend's `TestTerminalTabs` (+ the relay tests) against
      its go.mod.tile, `test/native.mjs` in Chromium (with an iPad-width
      split step), and `test/native-shots.mjs` at 390×844 and 1024×1366,
      light and dark (list, split with a chat, terminals, ports, the
      partition forms, the jump pill, a child card's steering). Not
      verified on a device or the Swift renderer.

- **D191 — The focused tile holds its place (2026-10-08).**
  workspace-template/shell/shell-anchor.js (new), bx-canvas.js,
  shell-doc.js, bx-shell.js (testApi `anchor`); hack/shell-anchor.test.mjs,
  hack/ui-harness/passes/focusanchor.js; docs/frontend-kit.md §Document
  mode, maintenance.md, changelog.
  - **The owner's words.** A tile focused in the middle of a long stack
    must not jump when tiles above or below expand or contract; the last
    tile at the bottom, focused, expanding or contracting itself keeps its
    contents where they are. "The title bar of the focused tile always
    stays at the same height relative to the browser window."
  - **Focused** is the shell's active window (zorder.js, D184) when it is a
    grid or Document-mode card: its title bar pressed, its frame clicked
    into, or (new) a control in its head keyboard-focused. A click into a
    Document-mode card's frame now makes it active too (raiseFocusedFloat
    only knew grid cards). Floats are viewport windows and never held; a
    terminal pop-up or a spawned window in front means nothing is held.
  - **Mechanism.** The baseline is the head's viewport y. A ResizeObserver
    on `<bx-canvas>`, `<main>` (the scroller) and main's other children
    (the decision strip), plus every canvas render, re-measures the head
    and moves `main.scrollTop` by the drift — in the observer's turn, after
    layout and before paint, so nothing visibly jumps (the harness samples
    the head in a later observer of the same turn: 0 px). A scroll event
    only moves the baseline with the page (`scrolled`), never re-measures,
    so a resize already pending when it lands is still undone; a scroll
    position change the layout made itself — the browser clamping to a
    shorter page — is told from the person's (`clamped`) and undone. The
    anchor's own scrolls are ignored by the Document-mode top bar, which
    would otherwise read them as reading (TopReveal).
  - **The spacer.** When the page would end before the head's place (the
    last tile shrank), padding under `<bx-canvas>` makes the room; it only
    ever shrinks — to what lies above the viewport's bottom as the person
    scrolls up — and goes when the page grows back (`trim`). Not chosen: a
    min-height on the content (the canvas sets its own min-height from the
    grid extent; padding stays out of its ResizeObserver content box).
    **Exception:** when keeping the head would leave none of the card in
    the window (a tall tile read near its foot shrank far), the page ends
    where it ends and the hold restarts where the head lands — a blank
    window held to the letter of the rule helps nobody.
  - **Letting go.** Scrolled fully out of view by the person, the tile is
    not held; it is again once it is focused or scrolled back into view.
    Nothing focused, nothing held. Native scroll anchoring
    (`overflow-anchor`) is off on main only while a hold is on: it would
    move the page under the anchor, which would read that as the person's
    scroll; with no hold the browser's own anchoring keeps working.
  - **Drags are the person's placement.** While a card, a height handle or
    a grid resize is dragged (`<bx-canvas>.dragging`) nothing is held, and
    the baseline is taken again a frame after the drop: a dropped card
    lands where the drop bar showed, and a grid push (D66) — only ever
    from a drag — moves its neighbours as before. Holding a tile pushed by
    a resize would scroll the canvas under the pointer.
  - **The desktop canvas** places a card at its tile's x/y: a change of the
    head's offset in the canvas is the tile moved (a layout edit, another
    device's layout via layout-sync, a harness `setGeom`), taken as the new
    place, not held; what is held there is the page moving around it (the
    decision strip, a clamp, the window resized). In Document mode and on
    a phone the cards flow, so every move is the page's and is held —
    including the tile's own Row ▸ Move up/down, which keeps the tile under
    the person's eyes while its neighbours swap around it. bx-scroll.js's
    focused-scroll tint (D123) is a different notion (where the wheel
    goes) and is not reused.
  - **Found on the way:** a frame out of view doesn't render, so it never
    reports a height change until it is scrolled to — exactly when the
    focused tile below it would have jumped.
  - **Verified:** `make js-check js-test theme-check` (js-test 881 pass,
    0 fail), `go test ./internal/sizebudget`; the harness's `focusAnchor`
    pass (33 checks; 15 of them fail with the anchor taken out) and
    `windows`, `docMode`, `gridScale`, `layoutSync`, `menuOpen`, `tabStrip`
    green.

- **D192 — Headings and text on a screen, without a tile (2026-10-08).**
  workspace-template/shell/blocks.js (pure), shell-blocks.js (view,
  gestures, the shell's ends), bx-canvas.js, shell-doc.js, bx-shell.js,
  menus.js, shell-tabs.js, layout-sync.js; internal/broker/screens.go;
  hack/blocks.test.mjs, menus.test.mjs, `TestScreensOrgBlocks`,
  hack/ui-harness/passes/blocks.js; docs/frontend-kit.md §Headings and
  text on a screen, elements.md, protocol.md. The owner's request: "Canvas
  mode — allow putting Headings and Text on screens without creating tiles
  for them."
  - **Data: a `blocks` array beside `tiles`, never inside it.** A block is
    `{id, kind: 'heading'|'text', text, level?, x, y, w, h, doc?}`: in the
    `layout` pref (`screens[i].blocks`) for a personal screen, and an
    optional opaque `blocks` on the org screen (and in its draft, which
    persists in the pref like the tiles). Inside `tiles` was not chosen:
    every shell before this one, and the iOS app (XbinCore Catalog's
    `ScreenInfo.tilePaths(s["tiles"])`, MobileScreens/LayoutPref, which
    edit the stored JSON and keep unknown keys), read `tiles` and expect
    a `path` — a pathless entry would be a broken card or a dropped
    tile. A separate key is invisible to them, and they re-save it
    untouched (they spread the screen object). The ws default screen
    (`PUT /screens/default`, opaque) carries `blocks` too, and seeds
    them with its tiles.
  - **Org screens.** `blocks` is content, like `tiles`: the same edit
    knob, `rev` and 409; a save of tiles and/or blocks bumps the
    revision (a blocks-only PUT is allowed); a PUT without `blocks` keeps
    the stored ones — a pre-D192 shell publishing tiles never wipes them;
    `blocks: []` clears; not an array → 400. Name/edit/mode stay
    meta-only. Adding a block on an org screen in view mode opens the
    draft for an editor (as opening a tile there does) and is disabled
    for anyone else; nothing reaches others until Save and update for
    everyone.
  - **One grid, one push.** On the canvas a block is an item keyed
    `block:<id>` (no tile path holds a colon) among the grid tiles:
    `pushLayout` and `spotNear` run on tiles + blocks (blocks.js
    `itemsOf`/`gridItems`), so a dragged tile pushes or swaps with a
    block and a block pushes a tile; a new tile never lands on a block.
    The drag state is the card's own (`_drag`, the ghosts, the commit);
    the result is split back (`splitItems`): tiles as `bx-tiles`, the
    blocks as a `bx-blocks` event whose detail is a **function over the
    stored list** — an entry this shell doesn't draw (a kind from a later
    shell) is kept exactly. Sizes: a heading one cell tall (an H1's 36 px
    line fits the 40 px a cell draws), text three, both at least 2×1
    cells (not the tiles' 4×3).
  - **Document mode.** The rows are read from tiles + blocks as one list
    (doc-layout.js unchanged; `mutateCanvas`/`mutateItems` split the
    result), so a block is a row like a tile — its own row in reading
    order by default (a 1-wide row: as wide as a single tile, which is
    the page's reading width), joinable beside a tile, movable by its
    grip and by the Row submenu in its menu. Every Document-mode edit
    (drag, height, Row lines, a tile opened on a Document screen) now
    renumbers tiles and blocks together; a tiles-only renumbering would
    give a tile and a block the same row. Known edge: a v0.3.69 shell
    arranging rows on a screen that has placed blocks renumbers only the
    tiles, so a tile and a block can share a row afterwards — nothing is
    lost, the next arrangement here fixes it.
  - **Look (Base Two).** No card, no title bar, no frame: the page's own
    type — H1 the hero face/size, H2 the heading token, H3 600 16/22 in
    the display face, text in the body face with the shared Markdown
    styles (bx-md.js `mdCssText`). While the screen can change, a dashed
    `--bx-border-strong` outline and a tool strip (grip, ⋯) show under the
    pointer, and the resize corner. Light and dark from the tokens
    (`make theme-check` 0).
  - **Editing.** Double-click or ⋯ → Edit; an input for a heading, a
    textarea for text. Enter (heading), Ctrl/Cmd+Enter, Escape or blur
    saves — Escape saves rather than cancels, per the plan: there is no
    "unsaved" state to fall back to. A block saved empty is removed (a
    new one abandoned leaves nothing behind). A layout `prefs` event from
    another client waits while a block is being edited (layout-sync.js).
  - **Markdown, safely.** `/vendor/bx-md.js`, the hardened renderer the
    Agent tab already uses: raw HTML escaped, images as their alt text,
    links to safe schemes only, `target=_blank rel="noopener
    noreferrer"`. An org screen's text is written by one member and read
    by all in the shell's own origin, so no other renderer was considered.
  - **A phone** (the stacked canvas, < 820px): blocks stack between the
    cards — a block before the first card (in the tiles' array order, as
    the phone always stacked them) that comes after it in reading order
    (CSS `order`, `mobileOrder`); no drag, the menu by long-press.
  - **Not chosen:** blocks on the iOS app's screens (it reads tiles; a
    later app can read `blocks`); per-block colours or alignment; images
    in text (the renderer never fetches); keyboard moves on the canvas
    (as for tiles, the drag; Document mode has the Row lines).
  - **Verified:** hack/blocks.test.mjs (10: drawing/keeping, new spot,
    push both ways and the split, Document rows across tiles and blocks,
    phone order, menus, tiles-only readers), menus.test.mjs (Add lines),
    `TestScreensOrgBlocks`, the harness `blocks` pass in Night and Day
    (add from the canvas menu, type, Markdown with hostile HTML, edit by
    double-click, H1 from the menu, move pushing a tile, resize, reload,
    the phone stack, Document rows and Row ▸ Move down, delete, empty
    discarded, an org draft published at rev+1, kept through a tiles-only
    PUT, read-only in view mode).
- **D193 — Screen modes: full-width Document rows, Fixed width, Tiled; a
  screen's mode and arrangement travel with it (2026-10-22).**
  workspace-template/shell/modes.js, doc-layout.js, shell-doc.js,
  fixed-layout.js, grid-layout.js, tiling.js, shell-tiled.js, bx-canvas.js,
  shell-blocks.js, blocks.js, shell-tabs.js, menus.js, bx-shell.js;
  web/bx-icons.js (`columns`, `tiling`); internal/broker/screens.go;
  hack/{modes,doc-layout,fixed-layout,grid-layout,tiling,blocks,menus}.test.mjs,
  `TestScreensOrgArrange`; harness passes docMode (extended), fixedGrid,
  tiled; docs/frontend-kit.md §Screen modes, elements.md, protocol.md. The
  owner: "in document mode a single tile-row the tile should be full width
  not half, add 1. fixed width mode, like original grid, but fixed 48
  columns instead of infinite X; 2. Single tile tabs (fullscreen); Tab mode
  should persist as part of layout (so shared tile can have a pre-set
  display mode)". Their answers: rows full width with no cap ("maybe side
  draggable bar to constrain"); stretch the columns only; Fixed width keeps
  its own positions; single tile, "then tiled/bspwm like".
  - **Modes.** A screen's `mode` is `canvas` (absent) | `fixed` | `doc` |
    `tiled`; anything else reads as the canvas, so a shell from before
    this shows a Fixed-width or Tiled screen as its canvas. The server
    accepts the four (else 400); the mode stays admin-plane (D187): never
    a revision. The Layout menu lists all four (modes.js MODE_INFO, one
    place instead of the `'doc'` literals); a tab shows the mode's glyph.
  - **No mode rewrites another's layout.** Each reads its own fields: the
    canvas `x/y/w/h`, Document `doc`, Fixed width `fixed` (per tile and
    block), Tiled `arrange.tiling`. Switching back always shows a mode as
    it was. The cost is that a tile placed in one mode has no place in
    another until it is drawn there; every mode places such a tile
    deterministically (reading order, the canvas place when it fits, the
    focused split).
  - **`arrange`, one screen-level object beside tiles and blocks:**
    `{docWidth?, tiling?}`. On org screens it is content, not meta (an
    editor drags a divider or the width handle in a draft): the edit
    knob, `rev`, 409, kept by a PUT that leaves it out (a pre-D193 shell
    or the iOS app re-saving tiles), not an object → 400 — Blocks'
    (D192) rule. Personal screens keep it in the layout pref. Not in
    `tiles` (the app and older shells read every entry as a tile) and
    not two top-level keys (one opaque field keeps the server out of the
    shell's shapes).
  - **Carrying it (the owner's "pre-set display mode").** Share to org
    sends mode + arrange (on a replace, the mode only from an org admin —
    the server's meta rule; a member's replace keeps the target's mode);
    the ws default screen (`PUT /screens/default`) carries both and seeds
    them; Copy to my screens and a vanished org screen's draft copy keep
    both (the draft holds the mode for that).
  - **Document rows.** A row holding one tile reads as 1 wide whatever
    width it was given (doc-layout.js `row`): the tile a 2-wide row is
    left with spans the page instead of half; a tile joining it makes
    it 2. The 1100 px cap goes (D187's "reading width"; D192's "as wide
    as a single tile" now means the page). The width limit is a handle on
    the page's right edge, centred, ≥ 480 px, lifted by dragging it to the
    edge or two clicks; a phone ignores it.
  - **Fixed width.** The canvas grid with exactly 48 columns (2304
    logical px) stretched to the pane: x scales (`_kx`), y doesn't
    (`_ky` = 1), so rows stay 48 px and nothing scrolls sideways; the
    grid-scale setting (D68) doesn't apply. `pushLayout`/`spotNear` take
    a right bound: a push right past it goes down (after a swap was
    tried). An item without `fixed` keeps its canvas place when it fits
    and is free, else packs below; the first Fixed-width edit writes
    `fixed` on every item so later canvas edits don't move them. The dot
    field is an SVG pattern (the canvas's CSS mask would stretch).
  - **Tiled.** A binary tree (tiling.js): leaf `{leaf: path}`, node
    `{split: 'row'|'col', ratio 0.1–0.9, a, b}`. A new tile splits the
    focused (else the last) tile along its longer side, 50/50, new second;
    closing gives the space to its sibling; dividers drag (two clicks:
    half); a title bar dropped on another tile swaps them; the tile menu
    has Rotate split / Swap with sibling. What is drawn is always
    `normalise(clean(stored), placed tiles)` — the stored tree comes from
    the network, so it is bounded (64 leaves/depth) and fitted to the
    tiles actually there; it is written only on an edit. Floats stay
    floating; blocks are kept but not drawn (Add heading/text disabled);
    a phone shows one tile with a switcher strip. The top bar slides away
    as in Document mode (and, nothing scrolling, also when the pointer
    leaves it downwards); D191's anchor is inert. A decision strip taller
    than 40 % of the pane keeps the tiles a full pane below it.
  - **Rejected:** a true fullscreen (Fullscreen API) per tile — the
    owner's "single tile" is a screen mode, and the browser's fullscreen
    hides the tabs a person switches with; a fixed-width mode with
    48-px square cells and a centred column — the owner chose stretched
    columns; packing Fixed width from the canvas every time — positions
    would jump whenever the canvas changed.
  - **Verification:** node tests (modes, doc rows, fixed placement and
    the right bound, the tiling tree incl. garbage and exact cover, menus,
    blocks); `TestScreensOrgArrange`; harness docMode (one-tile rows span
    the page, the handle limits/saves/lifts, all four modes on an org
    screen at the same rev), fixedGrid and tiled, dark and light, with
    windows, gridScale, focusAnchor, blocks, layoutSync, tabStrip,
    menuOpen unchanged.
- **D194 — A tile's own title and icon: from its manifest, and live from
  its page (2026-10-26; issue #6).** internal/registry (manifest `title`,
  `icon`), /components rows, web/xbin-client.js `xbin.meta`, web/bx-frame.js
  `xbin:meta` → `tileMeta`/`bx-meta`, /vendor/tile-meta.js (the shared
  cleaning), the shell's title bar, sidebar, Open-tile menu, Tiled switcher
  and admin popover; the iOS app's TileBridge and Catalog. The owner: "Allow
  tiles to set icons and title — as well as dynamic title".
  - **Two sources.** `xbin.json` `title` (≤ 80 characters) and `icon` (≤ 40,
    a `bx-icons` name) are what everyone sees, composed from the running code
    like `native`. `xbin.meta({title, icon, badge})` (badge ≤ 8) is what this
    viewer's window shows while the page is open: per viewer, never stored,
    cleared when the frame shows another tile — the same shape as the native
    runtime's `xbin.native.meta`, which the app already showed.
  - **Spoofing.** The path is always drawn beside a title: a tile can't pass
    itself off as another tile or the shell. Control and bidi characters are
    stripped (server and client); an icon must be a set name — never markup
    or an SVG; `xbin:meta` is taken only from the frame's own iframe.
  - **Rejected:** reading the tile's `document.title` automatically (every
    existing tile's window would change its name unasked); arbitrary icons.
- **D195 — Decisions open on the tile that asked; the sidebar flags it
  (2026-10-26; issue #12).** workspace-template/shell (shell-prompts.js,
  bx-canvas.js, bx-side.js, bx-shell.js), web/bx-requests.js (one fetch per
  page), web/bx-grants.js, web/bx-bindings.js, bx-part-consent (a `tile`
  attribute). The owner: "Binding dialogs should popup on tiles, not at the
  top"; their answer: on the tile only, "in tile/folder list flag perm
  requests that weren't dismissed".
  - **Where.** Grant requests land on their `from`, interfaces to bind on
    their `component`, consent asks on their `from`. A tile with any gets a
    key badge with a count in its title bar and a popover over the top of
    its window (Canvas, Fixed width, Tiled, floats); in Document mode and on
    a phone it sits in the card's flow above the tile. ⌃ folds it to the
    badge — kept in this browser (localStorage, by path and the count it was
    folded at), so a fold outlasts a reload; a new request opens it again. The top strip is gone (no fallback:
    the owner's call) — a tile not on the screen shows its sidebar flag (a
    key on its row; a closed folder counts its flagged tiles) and the
    organisations badge, as before.
  - **Compat.** `<bx-grants>`/`<bx-bindings>` stay public standalone vendor
    elements (a page that embeds them without `tile` gets every row, as
    before); the dismissal keys are D188's.
- **D196 — A window says its backend is building or starting (2026-10-26;
  issue #22).** internal/runner (the phases), internal/server (/components
  `backend`), web/bx-frame.js, frame-deploy.js, the shell's live square and
  sidebar, iOS WorkspaceEvents. The owner: "Shell should indicate when
  backends are compiling/starting".
  - **Phases.** `build-start` stays byte-identical (with text it is the
    environment setup step); a new `backend-starting` event says the build
    finished and the process waits for health; `build-ok` as before.
    Deployments get phase `starting`, a person's partition the event
    `backend-starting`. Rejected: a phase field on `build-start` — existing
    event types keep their shape (12-compat).
  - **Shown.** The title bar says "Compiling…" (Go), "Preparing…"
    (node/python), "Setting up…" (an env script) or "Starting…", the live
    square pulses, the sidebar row shows a wait glyph; `/components` rows
    carry `backend: {state: building|starting|failed}` so a window opened
    mid-build knows at once. The app's cards say "Building…"/"Starting…".
  - **Also (#11, no decision of its own): dismissals that stay dismissed.**
    Every Dismiss was a read-change-write of the whole `dismissed` pref, so
    two at once lost one (shown by hack/bx-dismiss.test.mjs), and every open
    page pruned dismissals against its own possibly stale list on any prefs
    event. `updateDismissed` is now one chain per page; pruning needs a
    fresh successful list and two loads missing the item; a failed save
    retries, then puts the row back with a toast.
- **D197 — The Agent tab takes messages while a turn runs: steered in, or
  queued for its end (2026-10-26; issue #36).** internal/term
  (agentqueue.go), internal/agent (optional `Steerer`), internal/agent/acp,
  internal/server (agentapi.go, openapi_agentqueue.go, the route classes),
  cmd/bx (agentqueue.go), web/bx-agent.js + agent-queue.js, agent-fold.js.
  The owner: "no way to send/queue messages when agent is busy … at least
  should be able to queue messages that the agent will pick up on next llm
  calling round (not at end of turn)".
  - **Route.** `POST /term/sessions/{id}/queue {text, attachments?,
    interrupt?}` → `{outcome: prompted|steered|queued, turn?, id?}`;
    `DELETE …/queue[/{qid}]` takes held messages back. `/prompt` is
    unchanged (409 while busy) — it reserves its slot before reading a
    32 MiB body, so a mode field there couldn't be seen in time.
  - **Semantics.** Idle → a turn starts. Busy, nothing held, an adapter that
    steers (claude-agent-acp, codex-acp: `_session/steering`) → taken into
    the running turn at its next step, shown "taken in mid-turn". Otherwise
    held, and later messages queue behind; at the turn's end the held ones
    go in as one prompt. `interrupt` holds then cancels. Cancel keeps the
    queue; the session's end drops it with a `queue {dropped}` event and a
    notice. 16 messages, 40 MiB of held files. `promptRequired` → sent as a
    prompt (held if a turn started meanwhile); `startedNewTurn` → steered.
  - **UI.** Send stays beside Stop; held messages are chips every browser
    sees; × or Stop returns them to the composer; Ctrl/⌘+Enter interrupts.
    `bx agent send --queue|--interrupt`, `bx agent unqueue`. Also #31: the
    composer's height counts its border and shrinks after a send or a pick,
    capped at 40 % of the tab's height, not the window's.
- **D198 — Actionability: a coding agent's park reaches the phone and the
  parent row at once; a judge marks a turn that stopped part-way
  (2026-10-26; issue #27).** builtin-templates/agent/_backend
  (harness_park.go, harness_login.go → needsMoment; the root-row cache;
  judge.go; migration `run_verdicts`), model/rules.js, conv-list, sidebar,
  home, native/convs.js, API.md. The owner's list: a Claude subagent asking
  for input wasn't flagged; a Claude agent that "yielded cleanly" mid-project
  should be marked "no clean close / not delivered, with UI dismiss";
  "propagate in the ios app"; their answer: judge coding agents and
  projects.
  - **Fixes.** A coding agent's question, permission or sign-in now pushes
    (`needsMoment` from `parkTx`/`loginTx`, as `ask_user` did); a child's park
    or unpark republishes its root's row (`waiting`/`kids`, only on a
    change), so the ?, the order, Needs you and the iOS list move at once.
  - **The judge.** On a top-level harness run or a project run whose turn
    ended answered and idle: the memory-tier model through the background
    slot (cost on the run; skipped without a memory model or with the
    `judge` feature off), given the last user message and the final answer
    (2000 characters each), 60 s, one per run. `{"delivered", "reason" ≤ 15
    words}`; not delivered = only planned, lists undone steps, will
    continue, asks something, reports an unresolved blocker. Stored in
    `run_verdicts` unless the run moved on; a new turn deletes it.
  - **Shown.** A "not finished" glyph with the reason, a Needs-you item
    ("stopped before finishing: …"), a push; "Mark as finished" (row menu,
    Needs you, an iOS swipe) clears it — for everyone, like answering a
    question: a per-person dismissal can't ride the broadcast row, and
    anyone who may talk to the conversation may act on it.
  - **Rejected:** judging subagents (their parent reads them); every turn of
    every conversation (the owner's choice).
- **D199 — Copy with formatting from a tile (2026-10-26; issue #34).**
  web/xbin-client.js `xbin.copy`, web/bx-frame.js `xbin:copy`,
  web/copy-rich.js (the allowlist), the shell's Copy (`xbin:contextmenu`
  `html`), the agent template's transcript copy handler and Copy action; on
  iOS `services.copyRich` and selectable markdown (D200's worker). The
  owner: "should be possible to select/copy text as rich text … numbered
  lists … table support".
  - **Why through the page.** A sandboxed opaque-origin tile has no async
    clipboard; Ctrl/⌘+C inside it works (a `copy` handler sets text/html and
    markdown-ish text/plain), but a button or the shell's menu can't, so the
    tile asks the embedding page (`xbin:copy {text, html}`, 256 KiB each).
  - **The tile's HTML is untrusted.** copy-rich.js parses it with DOMParser
    (nothing loads or runs) and rebuilds it from an allowlist: p, br,
    strong/em (b/i become them), code, pre, ul/ol (`start`), li, tables
    (`colspan`/`rowspan`), h1–h6, blockquote, hr, links to http(s)/mailto;
    other elements unwrapped to their text; scripts, styles, controls,
    images, embeds dropped; no other attribute.
  - **Older xbinds and the app.** `xbin.copy` resolves false after 3 s and
    the tile copies in its own frame (plain text).
- **D200 — The iPhone Duo's vertical bar (2026-10-26; issue #33).**
  native/ios (the panel bar, the tile's toolbar and ⋯, screen actions;
  Navigation.swift's gated Duo code), .github/workflows/ios.yml (`duo` job),
  native/ios/scripts (ci-pick-xcode.sh, pick-sim.sh, `XBIN_XCODE` honoured
  everywhere), native/AGENTS.md. The owner: "iPhone Duo: put top screen
  controls to the right side bar — matches apple design language, frees up
  vertical space"; their answer: Duo only, through the iOS 27.1 SDK.
  - **API (checked in Xcode 27.1, 27A9269's SDK).** The workspace switcher
    with its needs count, Home/Back, the tile's ⋯ menu and toolbar items and
    a screen's actions are marked `axisBehavior(.verticalPreferred)`: on an
    open Duo the system draws them as a bar down the side (it picks the
    edge); every other device and a folded Duo are unchanged. The gated
    `XBIN_SDK_27_1` code had never compiled: `ArrangementView` takes
    `primary:secondary:`, `reservedRegions` as written; `ArrangementView` is
    used only where the system draws a vertical bar, so an iPad keeps its
    columns on a 27.1 build.
  - **CI.** A `duo` job builds the app and runs the package tests with the
    Xcode carrying the 27.1 SDK (`XBIN_SWIFT_CONDITIONS=XBIN_SDK_27_1`);
    the 27.0 build is unchanged. No Duo simulator exists yet (27.1's newest
    device types are iPhone 18 Pro/Pro Max), so the layout itself is
    untested; the Mac mini's beta also needs its first-launch packages
    (an admin step) before any 27.1 simulator runs. pick-sim.sh skips
    runtimes newer than the selected Xcode (the installed 27.1 runtime made
    27.0 fail creating a simulator: "Incompatible device").
  - **Also on iOS (no decisions of their own).** #30: every renderer text
    field, the composer and the app's own forms get a Hide keyboard button
    above the keyboard, and lists, forms and transcripts dismiss it on a
    drag. #34: the `button` vocabulary goes to rev 2 with `copyFormat:
    "text"|"markdown"` — the app renders the markdown it draws to HTML
    itself (escaped, http(s)/mailto links only), so no HTML crosses the
    bridge; the template sends it only to rev-2 apps; markdown messages are
    selectable, and the Agent tab gets Copy. #28: verified fixed on a
    partitioned agent (a node test of the native list's two homes; the Mac's
    list-to-chat and deep-link UI tests against an `--isolate` partitioned
    agent, `XBIN_E2E_AGENT_PARTITION=1`).
- **D201 — Office documents and PDFs for the agent template, without a
  coding sandbox (2026-10-26; issue #35).** builtin-templates/agent/_backend
  docs*.go (readers, writers, previews, PDF), tools.go/llm.go/attach.go/
  files.go hooks, go.mod.tile; doc-cards.js, native/doc-card.js,
  model/docs.js, the parity catalogs; API.md. The owner: "sheets/docs/slides
  support. Research sota on how agents do this and implement a version, must
  work in the internal agents with no coding sandbox", plus PDF reading,
  "ideally any docs decoding/eval is type safe code like rust/go or running
  in wasm"; their answer: read and write real Office files.
  - **State of the art (Oct 2026).** Anthropic's docx/xlsx/pptx/pdf skills
    run Python (python-docx, openpyxl, python-pptx, pdfplumber) in a code
    sandbox; OfficeCLI exposes layered operations — read, DOM-level edits,
    raw XML; EditPPT edits through structured tools and validates by
    rendering. Ours is the structured DOM-level pattern in type-safe Go
    instead of a sandbox, with previews both the model and the person check
    (page pictures for PDFs, HTML previews for what it writes).
  - **Tools** (the `files` toolset, behind a `docs` feature, on by default —
    a new toolset would never reach custom classes that list theirs):
    `doc_read(path, pages?|slides?|sheet?, range?, offset?, limit?)` →
    Markdown ≤ 48 KiB (PDF text per page; pptx slides with tables and notes;
    xlsx tables with column letters, formulas and computed values; docx
    headings, lists, tables; CSV/TSV paged); `doc_view(path, page)` — a PDF
    page as a picture (vision); `sheet_write(path, ops)` — in place via
    excelize (set, formula, style, width/height, freeze, merge, clear,
    filter, sheets, chart), formulas computed; `doc_write(path, markdown)` →
    .docx; `slides_write(path, slides, theme?)` → 16:9 .pptx (title,
    content with bullets/table/image, two columns, image, section; notes).
    Each write keeps the earlier version and an HTML preview, shown in chat
    (web and the app) with a download/Share. `file_read` on a document
    answers with its Markdown — no stored sidecar.
  - **Libraries.** excelize v2 (BSD-3; pinned to the 2026-09-30 commit after v2.11.0, the first that fixes GO-2026-6684/6685/6688/6689 — panics, an endless row loop, a fatal recursion and memory exhaustion on malformed workbooks, which the release's vulncheck caught; move to the next tag when one ships); go-pdfium v1.21 in WebAssembly mode
    (MIT) with PDFium (BSD-3/Apache-2.0) on wazero v1.12 (Apache-2.0); our
    own docx/pptx readers and writers on archive/zip + encoding/xml. No cgo,
    no subprocess. Rejected: Python skills (need a sandbox); unioffice (AGPL
    or commercial); pdf.js (a JS engine, no rasteriser in Go); PDFium via
    cgo (neither type-safe nor sandboxed); a separate tile over `xbin_call`
    (internal lane only, needs approval); editing docx/pptx in place (v1
    rewrites them; xlsx is edited in place).
  - **Containment.** In-process in the tile backend's sandbox (one backend
    per person when partitioned). OOXML: ≤ 4000 entries, 32 MiB per part,
    96 MiB decompressed in total (counted while reading, never trusted from
    headers), XML depth ≤ 256, ≤ 2 M elements, 90 s a call; excelize panics
    become errors. PDFium: go-pdfium's default mounts the host's `/` — we
    pass an empty mount config: no filesystem, no network, 384 MiB memory,
    a 60 s deadline that kills the instance, a fresh instance per document,
    one PDF at a time. Fuzz targets for every reader.
  - **Cost.** The backend binary grows 37.4 → 59.0 MB (PDFium's wasm is
    5.8 MB of it); a cold build 14.1 → 16.4 s on 4 CPUs; PDFium compiles on
    first use (~2 s).
  - **Not yet:** .doc/.xls/.ppt; formatting another program put in a
    docx/pptx is lost on a rewrite; no picture view of docx/pptx for the
    model; slide links are text.

- **D202 — The tile window as an IDE (2026-10-27).** The owner found the
  terminal window (sessions, Logs, Files, PRs, Deployments, the D129 split)
  "all really janky" and approved a mockup of an IDE model with its
  defaults ("Design looks great, and the defaults make most sense").
  - **The window.** A slim title bar (the live square, the name, the path)
    whose last grid column is the ✕'s own; a tool strip down the left
    (Files, Changes, PRs with a count, Deploy; Logs at the bottom — a glyph
    and one word, the glyph alone under 640 px); panes filling the rest, a
    binary split tree, each pane its own tab strip with `+`, split right,
    split down and close; the logs dock along the bottom; a status bar
    under it all. Tabs are sessions (`s:`), files (`f:`), the uncommitted
    change of a file (`w:`), a commit (`c:`), a change proposal (`pr:`) and
    Deployments (`deploy`). A tab from a list opens in the focused pane
    (the one last clicked or typed in); one already open shows where it
    is. Tabs drag: onto a pane's middle to move, onto its right or bottom
    edge to split. Dividers drag, take the arrow keys (2 %, 10 % with
    Shift) and even on a double-click. An empty pane shows the launcher.
    The phone sheet shows the focused pane alone, every pane's tabs in its
    strip, and no splits (the layout itself is kept for a wider screen).
  - **Code (W1, ide/core).** `web/frame-ide.js` is the pure layout model
    and pref mapping (node tests: `hack/frame-ide.test.mjs`);
    `web/frame-ide-view.js` draws it; `web/frame-status.js` is the status
    bar; `frame-titlebar.js` is slimmed and `frame-panels.js` a shim, their
    exports kept (compat rule 3). Sessions carry a stable `ref` that
    survives an agent restart's new key. The test surface keeps every old
    name's meaning (`layout`, `layouts`, `beside`, `paneW`, `setTools`, …)
    and adds `ide`.
  - **Panels (W2, ide/panels).** `<bx-code>` and `<bx-prs>` split into
    elements of their own: `code-tree.js` (`<bx-code-tree>`,
    `<bx-code-changes>`: the files with their M/A/D/R status; the
    uncommitted files, the history, the activity folded),
    `code-view.js` (`<bx-code-view>`, `<bx-code-diff>`, followed live),
    `pr-panels.js` (`<bx-pr-list>` with `bx-pr-count`, `<bx-pr-view>`:
    Comment, Merged and Reject ask for their note through the window's
    dialog — `.ask` — or a line of their own, never `prompt()`), and
    `code-shared.js` (the rules they share, `hack/code-panels.test.mjs`).
    A list asks the window for a tab with `bx-open-tab {ref}`. `<bx-code>`
    and `<bx-prs>` stay served (the admin console and embedders use them).
  - **The ✕ bug's root cause.** The degraded title bar (old
    `frame-titlebar.js` 72–77) drew no `.spacer`, so `.winx` followed the
    tab strip; a bar near its limit tipped into that mode whenever its
    `barKey` changed — an ACP tab ending, the base-update button
    appearing, the deploy chip. Fixed by construction: the ✕ has its own
    grid column (the `tileIde` pass measures it at 520, 700 and 1100 px,
    with no session and after an agent ended).
  - **Why the session stage.** `<bx-terminal>` and `<bx-logs>` died when
    moved in the DOM: `disconnectedCallback` closed them and a reconnect
    never restarted them. So a session's element is never re-parented: all
    of them live in one layer over the panes, keyed by session in an order
    that never changes, each absolutely placed over the body of the pane
    whose active tab it is (hidden otherwise); a move, a split or a divider
    drag only changes its box — a shell keeps its scrollback, an agent its
    scroll. The logs dock mounts `<bx-logs>` once and hides it. As defence
    in depth, both elements now defer their teardown to the end of the
    task, so a real DOM move (a disconnect and a connect in one task)
    keeps the socket (the `ideMove` pass).
  - **The status bar.** The session in view's pickers moved there from the
    title bar (network, tile API or deployment target, VM, GPU, the base
    update, the live reload chip and Reload now), folded into one chip in a
    narrow window. Nothing draws without a session: before D202 the bar
    drew pickers for none.
  - **Pref compatibility.** The window pref `term:<tile>` keeps writing
    the fields older frames read — `open`, `active`, `activeId`, `pop`, and
    `layout`/`beside`/`paneW` derived from the layout (Files beside as
    `code`, PRs as `prs`, the dock as `logs`) — and adds `ide: {v: 1, tree,
    panes, focus, side, dock}` with sessions by server id. Neither the
    Deployments tab nor history or pending session tabs are saved. Without
    `ide`, the legacy fields map: `split` or `code` opens Files, `prs` the
    PRs list, `logs` the dock. Sessions gone on restore drop out; new ones
    join the focused pane. `open(layout)` keeps every value the shell's
    tile menu passes: `term` the session in view, `code`/`split` Files,
    `logs` the dock, `prs` the PRs list and the newest open proposal,
    `deployments` the Deployments tab. Closing the last tab closes the
    window; file tabs alone keep it open.
  - **A file that isn't there** (deleted, renamed, a tab restored after a
    rename) says so in a sentence instead of "error 404".
  - **Rejected** (the mockup's other knobs, kept there for reference):
    sessions in a bottom dock (the sessions are the work; they get the
    panes); logs as a tab (they are read beside a session, not instead of
    one); PRs as one tab (a list beside the panes opens each proposal next
    to its code); Deployments as a side panel (its table needs the width a
    tab has); a header per session (the status bar speaks for the session
    in view once, instead of a strip per pane).

- **D203 — Tile resource limits: manifest asks, raises at the cap,
  org ceilings (2026-10-27).** The owner: "tiles should be able to request
  additional resources, in manifest speccing minimums IFF those are bigger
  than defaults, that (org)admins would approve, also when tiles are eg out
  of disk, a prompt should allow admins to raise disk cap by some % as long
  as theres space in the workspace. Management should be possible in Admin
  UI at all times." Until now every per-tile limit could only be lowered
  (`XBIN_LIMIT_MEM`, the scope disk quota, the tile-sandbox book, the VM
  policy), and an OOM kill mostly went unseen (the leaf was removed before
  a scan read `memory.events`). Design and plan: `plans/tile-limits.md`.
  - **Kinds:** backend memory (`memMiB`), the scope's data quota
    (`diskGiB`), a VM backend's size (`vmMemMiB`, `vmVCPUs`) and a sandbox
    manager's tile-sandbox totals (`sbxMemMiB`, `sbxVCPUs`, `sbxDiskGiB`).
    Not pids (the owner's scope). Every value is an integer in the kind's
    unit.
  - **The manifest asks; xbind decides.** A top-level `"limits"` key
    (`memory`, `disk`, `sandboxes{memory,vcpus,disk}`) and the existing
    `vm` sizes, which above the VM policy become an ask instead of being
    clamped. Not `"resources"`: that already names data declarations. An
    ask at or below the default is ignored; a malformed one is a manifest
    warning, never a failed manifest. `disk` counts only on the tile that
    roots its scope (the quota is the scope's).
  - **Two slots per kind** in `data/tile-limits.json` (xbind-owned, like
    `partition-limits.json`): `ask`, the approval of a manifest ask, which
    applies only while the manifest still asks and up to what it asks (it
    goes dormant when the ask is dropped, like D118 chrome, and an ask
    back up to it needs no new prompt); and `set`, a raise or an outright
    value that holds whatever the manifest says. `effective = max(default,
    min(max(set, min(ask, manifestAsk)), max))`. Lowering below the
    default stays per deployment (D127n) — this is only ever a raise.
  - **Approvers:** workspace admins always. Org admins, for tiles their
    org owns, up to a **per-tile** ceiling per kind that workspace admins
    set on the org (`PUT /api/xbin/orgs/{org}/limits`) — not an org-wide
    budget (the owner's call). A tile's own credentials never decide; the
    admin console decides only under an admin driver (the D163
    `partitionActor` rule).
  - **Raise step:** +50 % pre-selected, +25 % and +100 % offered (the
    owner's), rounded up to 64 MiB, 1 GiB or one vCPU. A choice that
    doesn't fit is greyed with the reason.
  - **Fit counts only the granted extra** (the owner's call): a raise of
    `e → v` fits when `v − e ≤ free − 10 % reserve − promised`, where
    `promised` sums every other raised tile's `max(0, effective −
    max(usage, default))`. The default is a fair share, not a reservation;
    a raise is a promise. Read literally ("every tile's unused quota"),
    the 50 GiB default would grey out every choice on a typical
    workspace. vCPUs are shared by weight, so they only meet the maximum.
    Fit is re-checked inside the store's lock.
  - **At the cap:** an OOM kill (read before the leaf goes), a full quota,
    or a sandbox refused or stopped at its tile's total is a sticky hit.
    It shows as an alert with an `action` whose choices are decorated per
    viewer (the shell's frame resolves to its driver), and a third prompt
    kind beside grants and bindings. The alert stays until a decision on
    the kind or an approver's dismissal. Its text names `/xbin/limits` and
    `bx limits`, so a shell or admin console from an older scaffold still
    leads somewhere.
  - **"At all times":** the admin console's Limits tab and the ⚙'s Limits
    section ship with the scaffold, so `/xbin/limits` is an xbind-served
    page (the D163 `/xbin/partitions` pattern) that no scaffold update
    gates.
  - **Applying:** a memory raise resizes running leaves live (`memory.max`,
    then `memory.high`). A lower value waits for the next start, so an
    admin's edit never OOM-kills. Deployments and people's partitions get
    the raised cap per leaf. A disk raise rescans at once, so the 507
    lifts. Sandbox grants apply at the next admission, and a single
    sandbox may use the whole grant. A VM's size applies at its next
    start, with a "restart now" choice.

- **D204 — App tunnels: the xbin app over iroh, with an xbin layer inside
  (2026-10-27).** The owner asked for iroh tunnels for the iOS app: an
  admin switch with an optional custom relay, plus self-hosting docs with
  relay auth; "inside iroh tunnels we still must have xbin layer of
  encryption … authenticated between xbind and user device, with PFS"; a
  bigger add-device QR with a confirmation code; a connected-apps limit
  (default 1, workspace-wide, per-user overrides); and refusing non-iroh app
  connections. Design: `plans/app-tunnel.md`. Wire contract:
  `native/spec/tunnel.md`.
  - **iroh runs in a sidecar.** `xbin-iroh` is Rust on iroh 1.4.x, after
    n0's dumbpipe. It is embedded in xbind, extracted to a memfd and run in
    a bare namespace sandbox with the host network. xbind stays pure Go with
    `CGO_ENABLED=0`. A crash or exploit in the network-facing QUIC/TLS stack
    can't take down xbind or read its memory. The owner weighed cgo-linking
    the library and chose the embedded process.
  - **The xbin layer is Noise, and it terminates in xbind.** Pairing uses
    `Noise_IKpsk2_25519_ChaChaPoly_SHA256`, with the psk from the QR; the
    tunnel uses `Noise_IK`. Each iroh stream gets its own handshake, and the
    prologue binds both iroh EndpointIds. Transport messages are
    forward-secret, with an in-band rekey. The sidecar and every relay
    (n0's or the workspace's own) see only ciphertext; the sidecar can't
    read or forge a session. Go uses flynn/noise; the app uses CryptoKit;
    both are checked against vectors xbind generates.
  - **The confirmation code is compare-and-click** (the owner's choice).
    Both sides derive six digits from the handshake hash. The computer that
    showed the QR shows them with "Codes match" / "They don't", and the
    phone shows its own. A leaked or phished QR pairs nothing without the
    owner of the minting session. Tunnel codes never redeem over plain
    enrollment, so the step can't be skipped.
  - **The tunnel authenticates the device; the bearer still authenticates
    the person.** The bearer comes from device login (enclave key, Face ID).
    Sessions opened over the tunnel, and everything derived from them, work
    only on that device's tunnel. A device's tunnel serves only that
    device's credentials and its own sign-in steps, because other apps can
    reach the phone's loopback forwarder.
  - **The phone carries everything through a loopback forwarder.** It
    listens on 127.0.0.1 on a stable port, only while the app is in front.
    URLSession, the tile scheme handler, the sockets and WebKit (chrome
    pages, tile WebSockets) all use it, while the real https origin stays
    the workspace's identity. Tunnel requests are plain http to xbind: iOS
    26 WebKit drops Secure cookies on http loopback.
  - **Relays.** The default is n0's public relays. A self-hosted
    `iroh-relay` uses `access.shared_token`; the token rides in the QR,
    since a phone must reach the relay before xbind knows it. A staged
    "next" token rotates without re-pairing. The phone always uses iroh's
    Minimal preset (no n0 DNS, no mDNS), so it publishes nothing.
  - **iroh-only refuses device credentials and app sign-ins off the
    tunnel.** The answer is 403 with `tunnel: "required"`, never 401 or
    404, so old apps don't loop. The SSO browser leg stays allowed.
    Browsers are not affected. Apps that predate D204 are locked out of
    such a workspace by the admin's choice, and the admin sees how many
    devices first. Enrolled devices upgrade to the tunnel without
    re-pairing.
  - **The limit counts paired devices.** The workspace default is 1, with
    per-user overrides from 0 to 32. It applies when a device is added;
    existing devices are grandfathered. At the limit, the confirmation names
    a device to replace.
  - **App Store** (audited): not a VPN (5.4), no background modes, no
    downloaded code. IPv6-only networks work over the relay. The privacy
    manifest, policy and purpose strings are updated, and there is a
    licences screen. Export compliance: the owner answered App Store
    Connect's questions (standard algorithms, none proprietary); it found
    no documents needed and advised declaring that in Info.plist, so
    `ITSAppUsesNonExemptEncryption` stays `false` (2026-10-28). `true`
    without a compliance code is refused at upload.
- **D205 — Tiled: a tile dropped on half of another, or of the screen; new
  screens start Tiled (2026-10-30; issue #48).**
  workspace-template/shell/tiling.js (`zoneOf`, `place`, `placeRoot`,
  `drop`, `SWAP_ZONE`), shell-tiled.js (the drag, its preview, the edge
  bands), shell-tabs.js (`addScreen`), bx-shell.js (the harness's
  `addScreen(mode)`); hack/tiling.test.mjs; harness pass tiled (extended),
  with docMode, fixedGrid, blocks, focusAnchor, richCopy and tilePrompts
  asking for a Canvas screen; docs/frontend-kit.md §Screen modes,
  elements.md. The owner: "hard binary split doesn't feel amazing … When a
  tile is dragged over another tile, Left/Right trapezoids put the tile into
  there as 50/50 split left/right; Similar for Top/Bottom; Center swaps tiles
  with whatever is already there; If there is new empty space its taken by
  collapsing binary partition. Before committing to that, check with smaller
  artifact", and "make tiled = default mode for new tabs". A prototype
  (artifact "Tiled Drop Zones", vanilla JS on the same tree) settled the
  open choices with the owner.
  - **Zones.** Over another tile, a centre box 45 % of its width and
    height swaps the two (the owner picked the middle of 30/45/60 %). The
    tile's diagonals cut the rest into four trapezoids: dropping on one
    moves the tile into that half of the other's space, half each.
  - **The tile is taken out first.** The space it leaves goes to what
    shared it, as when it is closed (`remove`), and only then is the
    target split. Dropped on its own sibling, the two share their space
    again on the chosen sides, half each. No empty space is ever left.
  - **The screen's edges.** A 26 px band along each edge of the screen
    (and just past it) moves the tile into that half of the whole screen,
    with the rest of the tree, as it was without the tile, in the other
    half (the owner picked "Split the screen" over no edge zones). The
    edge wins over the tile beneath it.
  - **The preview is the outcome, not the zones.** The owner's pick: no
    trapezoids are drawn. While dragging, an accent fill shows where the
    tile lands ("Mail lands here", or "… left half of the screen") and a
    dashed outline where the tile under it goes: "squeezes here" when it
    gives up half its space, "grows to here" when the space the dragged
    tile leaves makes it larger, "moves here" for a swap or the same
    size. Both come from laying out the tree the drop would make, so a
    drop onto a sibling previews truthfully. The dragged tile's name
    rides at the pointer. Escape cancels.
  - **New screens start Tiled.** "New screen" (the tab strip's +, the
    canvas menu) makes a `mode: "tiled"` screen. Screens that already
    exist keep their mode, and the first Home screen of a new person is
    still built from the workspace default (its mode, else the canvas).
    The iOS app's "New screen" still makes a canvas screen; changing it
    needs an app release.
  - **Compatibility.** The tree format is unchanged; a drop only produces
    shapes `insert` and `swap` already could, so older shells and the iOS
    app read the result. The harness's `addScreen()` follows the new
    default; passes written for the canvas ask for it.
- **D206 — A person reads the source of a tile they may read (2026-10-30;
  issue #50).** internal/broker/code.go `requireCodeRead`;
  code_access_test.go `TestCodeReadPerson`; web/code-tree.js, bx-code.js
  (the 403 text); docs/auth.md, protocol.md, openapi.go. The owner:
  "Non-admin users can't see tile code in Tile Agent window". The code and
  git routes were written for the admin console and let in only admins,
  the tile itself and elements with a `code[:<tile>]` grant; D202 reused
  them for everyone's tile window, so a person with terminal on a tile
  (the level the Agent needs) got 403 for the code their agent edits,
  while the same window's PRs (`canReadPRs`), the `/c/` plane and the
  terminal all served them. Now a person (no element on the principal)
  with read on the tile passes too — docs/auth.md's read level is "read
  its source". An element is never let in through the person it acts
  for: its own grant decides, as before.
- **D209 — Projects atop your conversations; the coordinator as a small
  chat (2026-10-30; issues #43, #44).** builtin-templates/agent:
  model/projects.js (`pinned`, `pinWords`, `PINNED`, `setFolded`,
  `opens`), model/project-feed.js (`preview`, `readTail`, `coordTail`,
  `coordState`), projects.js (`listTop`), sidebar.js and web-ext.js (the
  `listTop` seam), project-feed.js (the card), native/projects.js (the
  `drawer` section, `coordTpl`), native/convs.js, native/ext.js,
  _backend/projects_coord_resolve.go (`GET /projects/{pid}/coordinator`);
  features `proj.entry` (reworded) and `proj.coordinator.preview`; tests in
  hack/agent-template-{native-projects,projects,native}.test.mjs,
  test/projects.mjs, test/projects-more.mjs, TestCoordinatorRoute. The
  owner: "AgTT iOS projects hidden in a menu — When enabled projects
  should be as first class as conversations, ideally pinned at the top;
  same for web" and "AgTT coordinator convo on main project screen —
  Currently the simple input is too distant to it, having even small convo
  preview would really help". This partly reverses D190, which moved
  Projects from a drawer row into the list's ⋯ menu.
  - **Where.** A Projects section at the top of Mine (not while searching,
    not in Shared or Archived), below the scope switch and above Pinned, on
    both views. Below the scope, not above it: the section depends on the
    scope, and above the switch it would move the switch under the pointer
    whenever the scope changes. On the web the old entry under Automations
    is gone. It would only repeat the section's heading, and showing it
    only outside Mine would move the views switch the same way. The app
    keeps Projects in the list's ⋯ menu: a menu costs no room, and it
    reaches the page from Shared and Archived.
  - **What.** Active projects only. Those with tasks that need you come
    first, then by activity, at most five; the rest are behind All
    projects (n), the page. Each row shows how many of its tasks need you.
    A team project's definition is left out while your membership of it
    is listed: the membership links to the definition. There is no
    per-project pin, because there is no pin state for projects (a
    conversation's pins are run rows); needs-you first, then activity,
    keeps what waits for you inside the cap. With no project yet, the section is
    one quiet row to the page (where New project is). It takes the old
    entry's place without adding a heading for people who don't use
    Projects.
  - **Folding.** The heading folds the section. Folded, the heading shows
    how many tasks need you in total. The fold is kept in xbind's
    per-person prefs (`prefs/projects-folded`), where the template already
    keeps the class, the model and CI's dismissed cards, and it is read
    once with the list. Natively it is the rev-1 `section collapsible`, so
    every app version has it.
  - **The coordinator's preview.** D186 makes the coordinator only when
    its person asks, so the preview must not POST. A new read-only `GET
    /projects/{pid}/coordinator` answers `{run}` or `{run: null}` (viewer
    level, the caller's own; a component or someone viewing as another
    person gets null). The card shows the coordinator's state, its last
    three messages (only the person's and the agent's; tool calls and
    thinking stay in its chat) and the message line right under them.
    Before a coordinator exists, it says the first message starts it. The
    messages come from `GET /runs/{id}/view?limit=20` (20 so that a turn's
    tool calls don't push the words out of the page), kept in the feed's
    own store and folded there. They are not the session's views: a view
    read into the session is one the stream doesn't follow, and replacing
    the open chat's paged view would disturb it. The preview reads them
    again when the project's page opens, after Send, and on the
    coordinator's `run` events. Those reach its owner whatever the stream
    follows, at a turn's start and end. The reads are coalesced to at most
    one a second while the page shows it; otherwise the next opening reads
    them. So the preview updates per turn, without token streaming. While
    the coordinator's own chat is open, the preview uses the session's
    live view and reads nothing. A backend without the `GET` (405) shows
    the card as before.
  - **Compatibility.** Additive. The new route is a GET beside the POST.
    The app needs nothing new: section, row, message and text are rev-1
    vocabulary. The `ext.drawer` seam (native), which nothing in the
    template used, now answers sections drawn in Mine only. The web gains
    `ext.listTop`; `ext.side` stays.
  - **Not built.** On a project's page the bottom composer still starts a
    new chat, and it does not say so. Proposed: its placeholder on the
    Projects page becomes "New chat — not this project's coordinator…", or
    the composer hides there. Left for the owner to pick.
- **D207 — The agent template: a report's file links, the preview's zoom,
  bulk file downloads (2026-10-30; issues #46, #47, #49).**
  builtin-templates/agent/_backend files_links.go (`reportLinks`,
  `linkRef`, `htmlHrefs`), files_http.go (`/dl/`, `/zip`), files.go
  `renderHTML`, docs.go `showDoc`, docs_preview.go; agent.js `frameDoc`,
  preview-frame.js, settings-files.js, model/report-links.js,
  model/preview-zoom.js, native/tools.js `filesTpl`; test/frame-policy.mjs;
  the template's API.md. The owner: "AgTT html reports should be able to
  link to sandbox files … the report writing tool should explain to the
  writing agent how to link to those files such that downloads will work"
  ("this needs to work within the report preview iframe somehow"); "AgTT
  file list - add download button + multi select -> bulk download, bulk
  delete"; "AgTT html report/doc view: add ability to zoom in/out the
  preview".
  - **Links are the step's, not the page's.** The backend reads the
    report's `<a>`/`<area>` hrefs (x/net/html's tokenizer — what the
    browser's DOM gives, entities decoded), resolves each where the report
    is, and journals `links: [{href, path}]`: the href as written, the
    session file it is. The frame keeps exactly those (matched on the
    trimmed href) and strips every other href — the model can't widen the
    set by writing links, only by naming files that exist in the session.
    The alternative — the frontend matching hrefs against the file list —
    can't see a sandbox report's files, which are named differently once
    copied (copyName keeps two `…/index.html` apart), hence the map.
  - **Sandbox reports carry their files.** A report shown from the sandbox
    (or a session copy of one from the bound sandbox) links the files
    beside it there: they are copied in with it (fetchSandboxFile — the
    etag/hash dedupe, so a re-render copies only what changed), ≤ 32
    files and 60 s per render, the store's caps as ever; what isn't is
    said in the result and the step's `skipped`. A session report's bare
    link it lacks is looked for in the sandbox's working directory, as a
    session-file miss is (D136).
  - **The frame stays sandboxed: `allow-downloads`, nothing else.** A kept
    link is rewritten to `GET /runs/{id}/dl/<path>` below a path ticket
    (D135) for `runs/{id}/dl` — the route path-shaped so one ticket
    covers the run's files and nothing else of the tile — with
    `target=_self` (a `_blank` would need allow-popups) and `download`;
    the answer is `Content-Disposition: attachment`, so a click downloads
    in place and loads nothing (verified in Chromium,
    test/frame-policy.mjs). Rejected: the frame token in the URL
    (`xbin.url` — the whole tile's credential in a document the model
    wrote); blob URLs made by the page (every linked file fetched up
    front, up to 64 × 16 MiB); allow-popups.
    Stripping also covers `<area href>`, SVG `xlink:href` and a kept
    link's `ping`. A link answering with a page (an expired ticket)
    navigates the frame; the page sees the frame's unexpected `load` and
    paints the report again with a new ticket. Not reached: a shared
    conversation at the global instance from a person's partition (a
    ticket is its own partition's, plans/partitions/05 §6) — its links
    stay inert and the header says to use Files.
  - **Zoom from outside the frame.** The frame is laid out at 1/z of the
    pane and scaled by z (CSS transform): the report reflows as under a
    browser's zoom, the srcdoc is never reassigned (no reload, no lost
    scroll), the sandbox untouched. Kept per person and tile in the prefs
    API — a tile frame has no localStorage (touching it throws).
  - **Downloads.** `/zip` streams `zip.NewWriter(w)`, one file's bytes in
    memory at a time (blobs read past the cache), every path checked
    before the first byte, a later read failure aborting the response
    (`http.ErrAbortHandler`) rather than ending a short archive that looks
    whole; no `path` zips every file (select-all stays a short URL). Bulk
    delete is the existing `DELETE …/file` per path from the client — no
    new route, any backend of the blue/green overlap answers it.
  - **The app.** Files gets Share per row (`/dl/`, the file's own name)
    and Select (rows `selected`, rev 1) with Share as zip and Delete in a
    `toolbar place="bottom"` on rev 2, a section above the list on rev 1.
    The render screen's zoom and links (`tools.render.zoom`,
    `tools.render.links`) are DIFFERENCES.native until the iOS render
    screen change that follows. Seen while there: the app's
    `NativeCallAction.tileRelativeFile` refuses an absolute
    `/api/<self>/…` file, which is what every `native.share({file})` of
    the template passes (Share / export too) — an app-side fix.
- **D210 — A turn's changes reviewed a file at a time (2026-10-30; issue
  #45).** web/agent-diffs.js (`FileDiffs`, `diffBody`, `inlinePart`),
  agent-cards.js (`filesBlock`, `fileRow`, `capped`), bx-agent.js
  (`_fileDiff`, `_openSlot`, `_touch`), bx-code.js (`diffHTML`'s
  `startLang`); hack/agent-diffs.test.mjs; harness pass agentReview (and
  agentTab's diff check); XbinAgent Review.swift (`ChangeSet`,
  `FileReview`, `PatchParts`, `ReviewLoader`, `AgentClient.diff`) +
  ReviewTests; App/Agent/AgentReview.swift, AgentScreen.swift; UITests
  XbinAgentReviewTests; docs/overview/09-terminals.md. The owner: "iOS Tile
  Agent turn code change preview is laggy — Often turns change 1000s of
  loc, by default open preview lags when that happens. Should be default
  hidden, and for mobile it should be a more suitable code review UX, not
  just massive diff (actually for the web a bit better ux would be good as
  well)". The app drew a turn's `files.changed` always expanded — every
  line of up to 192 KiB of patch as a Text in one non-lazy transcript row,
  re-split on every render; the web's card was folded but opening it ran
  the whole patch through the highlighter into one `<pre>`.
  - **File list first, a diff per file.** Folded, a change set is its
    counts ("This turn changed N files +a −d"); open, its files (status,
    path, +/−); a file opens its own diff and nothing else. The iOS card
    respects its open state like a tool card (it ignored it) and lists
    the first 12 files, then "All N files".
  - **Where a file's diff comes from, in this order:** its part of the
    event's own patch when the patch holds all of it (no request); else
    the existing `GET …/diff?turn=|toolCallId=&path=` (until now unused by
    any client) while the session runs; else what the patch holds of it,
    marked cut, or a note that the session no longer has it (a past
    session, history, a viewer the route refuses). Loaded once per file
    and kept (the 64 most recent on the web, 32 parsed on iOS).
  - **The web expands rows in place** rather than opening a D202 tab: the
    diff is of the agent's snapshot trees, which `<bx-code-diff>` (the
    tile's git work tree against HEAD) can't show; the Agent tab also
    stands where there is no IDE window to open a tab in (a history view,
    a phone sheet); and the reader keeps their place in the transcript.
    The diff flows in the transcript (no scroller inside the scroller),
    long lines wrapped, git's header lines dropped (the row names the
    file; the language still highlights through `diffHTML`'s new optional
    `startLang`), the open row's header sticky. An edit tool's own diff
    gets the same 400-line cap.
  - **iOS reviews in a sheet.** A file opens a NavigationStack at its diff
    with the change set's file list behind Back: numbered lines (the new
    side's; the old side's for a removed line) in a lazy list, wrapped or
    scrolling sideways (the choice kept), the first 400 lines then "Show
    all N lines", previous and next file and "k of N" at the bottom, Copy
    path. Parsing is XbinAgent's (`FileReview.parse`, counters, not the
    quadratic `GitPatch` hunk check), off the main actor in an actor.
    Tool cards and the full-screen tool sheet list their files the same
    way. `DiffView` (the renderer's `diff` primitive) is untouched.
  - **No server change.** The `files.changed` event keeps its shape and
    its inline patch, so shipped apps and older shells render as before;
    the route was already there.
- **D211 — App tunnels come back after the app was away, in place, and say
  what they did (2026-10-30; issue #42).** native/ios/App/Model/
  TunnelManager.swift, TunnelSession.swift (split out of it),
  LoopbackForwarder.swift, TunnelTransport.swift,
  AppTransport.swift (`stream(after:)`), WorkspaceModel/WorkspaceEvents
  (`tunnelCameUp`, `nudge`), Tiles/WebTileController.swift,
  Shell/ConnectionView.swift; Shared/NoiseTunnel.swift (`probe`); XbinCore
  Client/TunnelLog.swift, Tunnel.swift (`TunnelProbe`), Events.swift
  (`EventSocketPolicy.nudge`); web/xbin-client.js (`online`);
  UITests/XbinTunnelTests `test04ResumeInPlace`. The owner, on a phone
  with the TestFlight build: work in a tile's agent, lock the screen for
  minutes (or seconds), unlock — disconnected until the app is restarted.
  - **The cause.** The background tore every tunnel down, as designed —
    and the open screen's follow loops (the agent session's stream, a
    native runtime's fetch retries) asked for the tunnel again within the
    background task's grace period. `ready()` didn't look at the app's
    state, so a fresh forwarder and iroh connection came up in the
    background (the simulator's log: "tunnel/1 connected" one second after
    Home). iOS then suspended the app; on a device it reclaims a suspended
    app's listening socket (TN2277) and breaks iroh's UDP socket (iroh
    #4289). Back in front, the session still read as up — the forwarder's
    listener failure was never noticed (`isListening` stayed true), the
    connection had no reason to report a close — so nothing came up again:
    every request failed ("Could not connect to the server") until a
    relaunch. A late `suspend()` (sessions closed one after another, each
    awaiting its endpoint's close) could also stop a session resumed
    meanwhile, and set its status after a fresh connect.
  - **Nothing comes up in the background.** A request in the background
    waits in `ready()` for the front (so open screens pick up where they
    were the moment the app is back, no backoff to wait out), unless it is
    a notification's work (`TunnelManager.inBackground`, the Mute action),
    whose tunnel goes again when that is done.
  - **The front always makes a fresh, proven tunnel.** Back from the
    background, whatever came up there goes. A connection counts as up
    only once a probe through it — a `tunnel/1` stream, its IK handshake,
    `GET /api/xbin/login/methods` (open to the paired device over its
    tunnel, spec §7) — got xbind's answer within 8 s; else it is dropped
    and retried with the backoff. A stream or handshake that fails (10 s)
    on a connection marks it dead and the request gets one more try on a
    new one. A forwarder whose listener fails reports it and is replaced.
    Suspends are synchronous (the closes run in parallel, touching no
    session), so a late one can't stop a resumed session. `stream` and
    `upload` call `ready()` themselves. A dial that outlives its deadline
    closes what it made. A network change restarts a connect that hangs
    (not the path watch's first report, which only says how things are);
    an attempt takes its epoch when it is made, not when its task first
    runs — a restart in between had two attempts run at once, two
    connections and a moved port (seen in the first fixed run's log).
  - **Open screens recover in place.** A verified tunnel nudges the
    workspace's events socket (connect now, unparked: a re-sign the dead
    tunnel failed had parked it), and posts `xbinTunnelUp`: a web tile
    whose load failed (the forwarder's 502 page, marked `X-Xbin-Tunnel:
    down`, or any gateway error) reloads; other pages get an `online`
    event, on which xbin-client reconnects its events socket at once (a
    browser's `online` does the same). Pages aren't reloaded wholesale:
    their state (a half-typed form) would go, and their sockets reconnect
    themselves. The agent tab's follow loop needed nothing more: its
    waiting `ready()` returns as the tunnel is back.
  - **Diagnostics in every build.** `TunnelLog`, a ring of 500 entries in
    memory: the app's background/front (and how long it was away),
    bring-ups, connects with their duration and failures, probes, closes,
    retries, the forwarder's port and its 502s, failed streams. Scrubbed
    on the way in (any run of more than 20 key-like characters is cut to
    6 and "…"; ids are shown as 8 characters), never written to disk.
    Settings → Connection → Diagnostics shows the newest and all of them,
    and **Copy diagnostics** copies them with the app's version and the
    tunnel's state, for a bug report. NSLog stays Debug-only;
    per-connection lines stay out of the ring.
  - **Tests.** The simulator never reclaims sockets, so the baseline
    passed there; Debug's `-XbinTunnelEmulateSuspend` kills what lived
    through the background on return, as a device does, and with it
    `test04ResumeInPlace` (a minute in the background, then the counter's
    +1 and an agent session's answer on the same screen, and the
    diagnostics) failed before the fix as the owner saw it. XbinCore and
    app-check test the log, the probe, the policy's nudge and the events
    driver. On a device: unverified until the next TestFlight build.
- **D208 — A document to read: the agent template's report on iOS full
  screen, zoomable, beside its conversation, its linked files shared
  (2026-10-30; issues #41, #49, #46 — the app's half).** web/xb/vocab.js
  (`canvas` rev 2, `split` rev 3), web/xb/render-content.js and
  render-structure.js (the reference), native/ios (XbinCore
  `CanvasDocument.base/href/zoom/Navigation`, `TileResource.shareName`;
  XbinRendererModel `CanvasProps`, `ScreenLayout.fillCanvas`,
  `SplitLayout.halves`; the renderer's canvas, screen and split views; the
  app's `StaticCanvas`), builtin-templates/agent (native/tools.js
  renderTpl, native/render-doc.js, native.js layout()/halves(), the parity
  catalogs), native/fixtures/doc-reader, e2e tile apps/report and
  XbinNavigationTests test03, docs/native.md, plans/native.md §15. The
  owner: "AgTT html doc preview is janky on iOS: No full screen, no side by
  side/halves on Duo, no chat render in second half"; "add ability to zoom
  in/out the preview"; "AgTT html reports should be able to link to sandbox
  files". Another change does the web and the backend (the render step's
  `links: [{href, path}]`, `GET /runs/{id}/dl/<path…>`).
  - **Why it was janky.** The preview was a `canvas html` inside a `scroll`
    screen at the largest height token (xl, 360 pt): a small box in a page
    that scrolled around a page that scrolled itself, and no height could
    say "the rest of the screen". On an iPad it was pushed in the detail
    column over the conversation.
  - **`canvas` rev 2.** `fill` (the island takes the rest of its screen; a
    `scroll` screen holding one lays its other children above it at their
    own height and doesn't scroll — the page scrolls and pinch-zooms
    itself), `zoom` (0.5–3: a CSS zoom on the island's page, which reflows
    it — `WKWebView.pageZoom` was tried first and, on an iPhone, scaled the
    page without reflowing it, a 125 % table running off the screen's edge;
    the zoom reloads the island, and the app puts the reader back at the
    same fraction of the page; pinch magnifies on top) and
    `link {href}` on an `html` island. JavaScript stays off and the CSP is
    unchanged; a tapped link still never navigates — the navigation
    delegate refuses it and, when the tile listens, reports it. A height
    token `fill` was rejected: tokens are shared by image and chart and
    can't carry a revision. **Links' hrefs:** with no address
    (`loadHTMLString(baseURL: nil)`) WebKit can't resolve a relative href
    against `about:blank`, so the tap did nothing; the island now loads at
    `xbin-canvas://island/` (a scheme nothing serves — the CSP refuses every
    load before one would try) and reports an href under it relative to it,
    resolved as a browser does (`./a/../b.pdf` → `b.pdf`, spaces
    percent-encoded). An app-injected script in its own content world
    could have read the attribute as written, but "JS off" stays literal.
    The reference renderer resolves the same way (`canvasHref`,
    canvas-parity.test.mjs holds the base and the zoom range equal).
  - **`split` rev 3: `halves`.** On a regular width the two columns share
    it equally: the `NavigationSplitView` sidebar column measured to half
    the split (a GeometryReader around it) instead of 280–400 pt — but
    never leaving the detail under 480 pt: on an 834-pt iPad in portrait a
    half-width sidebar was laid OVER a dimmed detail (the split tiles only
    while the detail keeps its minimum; the list's 360 | 474 tiles), so
    there it is 354 | 480 (seen on the simulator); on an
    open Duo the same `ArrangementView` D200 uses for every split, which
    keeps one pane each side of the hinge (compile-checked only: still no
    Duo simulator). Compact, it collapses like any rev-2 split.
    **plans/native.md §15's "transcripts never share the screen" gets one
    exception** — the owner asked for the chat in the second half: a
    document beside the conversation it belongs to. Everything else keeps
    the rule; docs/native.md says `split` is never for transcripts except
    `halves`.
  - **The template.** The render screen is a reader where the app has
    canvas rev 2: the island fills the screen, − / 100 % / + in a bottom
    toolbar set `zoom` (steps 50–300 %), a Linked files menu in the bar. On
    a regular width with split rev 3, a render on top of a conversation
    switches the layout: the conversation (live: transcript, composer) in
    the leading half where the list was, the report in the trailing half
    with a Close (leading toolbar); the list and whatever was pushed
    between the two come back on Close (or a compact Back), which, like
    Back, keeps a followed render closed. The sidebar button hides the
    conversation for a wider report. Chat leading and report trailing, as
    the web's render pane sits; the conversation in the sidebar column
    keeps the semantics right when the width collapses (the report pushed
    over the conversation). Phones stay list-first with the report pushed.
  - **Linked files (#46).** native/render-doc.js keeps the hrefs the render
    step's `links` name (as the report wrote them) and strips every other
    off-page one, as the web's frameDoc does (the textual fallback too
    now); the frame-policy test holds both in Chromium. A tap → `link` →
    the matching entry (compared as resolved) → `native.share({file})` of
    `GET /runs/{id}/dl/<path…>`, whose last segment is the file's name.
    Links come from the step that opened the preview, else from the newest
    render of that file in the conversation (a Render from Files).
  - **Older apps** never see what they lack (the runtime would show the web
    page): canvas rev 1 gets the island at xl with − / + re-rendering it
    with `html{zoom}` (a reload) and a "Files linked from this report" list
    of share rows under it; split rev 2 keeps the report pushed in the
    detail column; toolbar rev 1 gets − / + beside Source.
  - **`xbin.native.share({file})` never shared a backend's file (found
    by D207's review, verified here).** `NativeCallAction` took only
    relative paths and refused anything starting with `/`, and every
    `file` the agent template passes is its API's (`/api/<self>/runs/…`:
    Share / export, D207's per-row Share and Share as zip, this report's
    links) — so the app answered "share: nothing to share". It now passes
    absolute paths on (no scheme, no host, no `..` in the path);
    `TileResource.assetPath` still decides, when the file is fetched, that
    it is the tile's own (`/c/<self>/…`, `/api/<self>/…`), as for an image.
    Apps already shipped keep refusing them: nothing a template can do
    about that (a relative path can't reach the API). **The file's name:**
    the app named it after the reference's last component
    (`raw?path=out%2Freport.pdf`, `zip?path=…`); it now takes the
    response's `Content-Disposition` filename (`filename*` first — the dl
    and zip routes set it), else the last segment or a `?path=`'s, decoded.
  - **Built on D207** (rebased onto it): the − / + walk
    model/preview-zoom.js's steps and keep the zoom per person and tile in
    prefs/preview-zoom, as the web does; links come through
    model/report-links.js `linksOf` (a step without `links` has none) and
    are shared through D207's `GET /runs/{id}/dl/…`; render-doc.js mirrors
    frameDoc's stricter stripping (an image map's areas, SVG links, pings;
    hrefs trimmed), frame-policy.mjs holding both in Chromium.
  - **An unbound halves split shows both columns.** On an iPad in portrait
    an unbound `NavigationSplitView` opened on the detail alone — the
    conversation behind the sidebar button (the e2e tile's first iPad run);
    an unbound halves split now starts with `.all`.
  - **Parity.** `tools.render.zoom` and `tools.render.links` (D207's keys)
    come off DIFFERENCES.native; `tools.render.beside` is new (the web's
    render pane already sits beside the conversation).
- **D212 — On a project's page the bottom bar writes to its coordinator,
  and All projects is always in reach (2026-10-31).**
  builtin-templates/agent/page-composer.js (a page's own bar: web-ext
  `page(p).composer`), project-feed.js `coordComposer`, projects.js,
  model/project-pins.js (`more`), native/projects.js (the docked
  composer). The owner, after D209: the bottom composer on a project's
  page started a general new chat, which is "really where I imagine the
  coordinator chat input would be"; and with at most 5 projects pinned,
  "more should be very easy to access".
  - **A page can give the bar a job, or none.** A project's board and
    activity give it to the coordinator (POST /projects/{pid}/coordinator).
    The Projects list, the forms and a project's settings hide it. Each job
    keeps its own draft, so text for a new chat never goes to a
    coordinator. The pickers and attachments belong to a new chat and are
    hidden while the bar has a page's job.
  - **All projects** stays visible when the section is folded, and says how
    many active projects didn't fit ("2 more"), on web and native.
  - On iOS the coordinator's input is a composer docked at the bottom of the
    project's screen. The screen's own search moved into the board's
    section as a field, because iOS 26 puts a screen's search in the same
    bottom bar.
- **D213 — The tunnel is invisible above the transport; the halves split
  drags (2026-10-31; the owner's polish calls on D208 and D211).** XbinCore
  Client/NetworkFailure.swift (`NetworkFailure`, `ReachableGate`);
  native/ios/App/Model/TunnelTransport.swift, LoopbackForwarder.swift,
  TunnelPairing.swift (`overTunnel`), WorkspaceEvents.swift
  (`onReachable`), WorkspaceModel.swift (`reachableAgain`,
  `.xbinWorkspaceReachable`); Tiles/WebTileController.swift,
  TileSchemeHandler.swift; XbinRenderer Navigation.swift
  (`SplitLayout.halvesWidths`).
  - **The owner:** "for the app there should be no difference between
    'tunnel is down' and 'internet disconnected and some http reqs failed
    transiently', tunnel is not something layers above it should care".
  - **Failures look like the network's.** The forwarder no longer answers
    its own 502 page when the tunnel can't carry a connection: the
    connection ends unanswered (URLSession and WebKit: "the network
    connection was lost"). `TunnelTransport` turns a tunnel that can't come
    up into a URL error — offline when the phone has no path, timed out
    for a step that ran out of time, else "could not connect to the
    server" — with the tunnel's reason under it and in TunnelLog. The tile
    scheme handler fails such a request as a lost connection (no 502 or
    401 page of the app's own), as it already did for a stream that broke.
    Pairing keeps its own words: it is about the tunnel, so a network
    failure during the first sign-in is still "can't reach the tunnel".
  - **Recovery is the workspace's, not the tunnel's.**
    `.xbinTunnelUp` became `.xbinWorkspaceReachable`: posted when the
    tunnel comes up and when the events socket opens after a drop, no
    connection or a gateway's 5xx (any transport; not after a background,
    which closes it on purpose), at most once per 2 s with one trailing
    (`ReachableGate`) — a tunnel and its socket coming back are one
    recovery. Open pages that failed load again, the others get `online`:
    a direct workspace's pages now recover the same way.
  - **Halves drag to any ratio.** The leading column of a halves split is
    `navigationSplitViewColumnWidth(min: 280, ideal: half, max: width −
    280)` instead of a fixed width: iPadOS 26 lets the person drag a
    split's column separator (UIKit's iOS 26 split view gained
    `minimumSecondaryColumnWidth` and friends for it), and each side keeps
    280 pt. The system keeps the dragged width while the split's width
    doesn't change. The tile isn't told (no vocabulary change). The UI
    tests drag it (`E2E.dragHalves`, in XbinNavigationTests test03 and
    XbinAgentNativeTests test11); if the system turns out not to resize,
    the fallback is a drag handle of our own at the boundary.
  - **Verified 2026-11-01, and the document side fixed** (the first iPad
    run: the Mac mini's iPad simulators failed until its reinstall). The
    system resizes, but the document stopped at 448 pt on an 11-inch iPad
    in portrait (480 at the start: the bar moved 32 pt toward it) — UIKit's
    own automatic minimum for the secondary column, which no setting of
    ours reached. The detail column now takes a width range too
    (`SplitLayout.halvesDetailWidths`: 280, the rest, the width less the
    sidebar's minimum); iOS 26's SwiftUI passes it to UIKit's
    `minimumSecondaryColumnWidth`/`preferredSecondaryColumnWidth`. (Setting
    `minimumSecondaryColumnWidth` on the split controller directly was
    reset by SwiftUI's next update.) The owner asked for this first, a drag
    handle of our own only if it failed. An open iPhone Duo arranges its
    halves with the system's `ArrangementView` (D200), not a split view:
    its panes take `splitArrangementLayoutSize(minWidth: 280)`, the same
    rule where the system resizes them — built (the `duo` job), not run:
    Xcode 27.1 (27A9269) has no Duo simulator.

- **D216 — Repo stats are a read of the scm protocol, cap `stats`
  (2026-10-31; plans/project-overview.md §Repo stats).** docs/scm.md
  §Repo stats; builtin-templates/scm-github/_backend/stats.go. The project
  overview (D214) shows each repo's open pull requests and issues, merges
  in the last week, CI on the default branch and the last push.
  - **A route, not the consumer counting through the list routes.** A
    count from `GET /scm/pulls`/`/scm/issues` pages a whole list (30 to
    100 a page, a host call each) on every refresh, and "merged in the last
    seven days" isn't a filter those routes have. Every host keeps these
    counts itself (GitHub's `totalCount`/search `issueCount`, GitLab's
    `count`/`X-Total`, Gitea's counters); only the provider can ask for
    them cheaply.
  - **Provider-neutral.** "Pulls" are pull or merge requests; the fields
    are what any host can say, and one it can't is **absent**, never `0`
    or a guess — a GitLab or Gitea provider maps it (docs/scm.md §Building
    a provider). Without the cap, 501 `unsupported`, as any cap; a
    consumer falls back to `GET /scm/repo` and `GET /scm/checks`. Protocol
    stays 1 (additive).
  - **Cached by the provider, at most 5 minutes per (identity, repo)**,
    `at` saying when it was read: an overview re-reads every minute and
    counts that lag minutes are fine. The identity (`as`) and policy are
    checked before the cache, so it never outlives a caller's right to it.
    scm-github keeps an answer missing a part that failed transiently (the
    rate limit, GitHub's trouble) a minute only.
  - **scm-github counts with one GraphQL query, not REST search.** The
    repository's `pullRequests`/`issues(states: OPEN)` `totalCount` (exact,
    not the search index) and `search(type: ISSUE)` `issueCount` for the
    merged ones: one call and one point of the GraphQL limit (5000/h per
    identity), where REST search would be three calls against search's
    30 a minute per identity — which `GET /scm/issues?q=` also spends, and
    which one cold overview of ten repos would exhaust. The client already
    tracked GraphQL's limit apart (`rateResource`); `graphqlRaw` reads a
    partial answer (a field GitHub answers null stays absent).
    `defaultBranchChecks` is `GET /scm/checks`'s own computation on the
    default branch (`checksAt`, shared), not a second one.
- **D215 — Live CPU and memory for tile sandboxes, passed through the
  sandbox-manager contract (2026-10-31; plans/project-overview.md §"Tile
  sandbox usage").** internal/tilesbx/sample.go (+ `usageState.sample`,
  `run.stats`, `Info.Usage`), internal/cgroup `ReadUsage`, sdk
  `SandboxInfo.Usage`, sdk/sandboxcontract (`Sandbox.Usage`, an optional
  check), builtin-templates/coding-sandbox (`sandboxView.Usage`,
  `answerAs`), docs/protocol.md, docs/sandbox-manager.md. The project
  overview's sandbox cell (D214) shows CPU and memory over the last hour.
  - **Once a minute, one pass.** The overview re-reads every 60 s, and an
    hour of minutes is a readable chart. One `time.AfterFunc` chain, as
    usage.go's disk measuring is (no ticker): a sandbox coming up arms it,
    it re-arms only while one with a leaf runs, and xbind's shutdown
    (`closeUsage`) ends it. A pass reads four small files per running
    sandbox's leaf (`cgroup.Usage`), with no lock held: no walk, no
    process listing, nothing confined (the leaves are xbind's own
    cgroup tree, not sandbox-written data). Lock order stays usage.go's
    (m.mu before m.usage.mu; never both while reading).
  - **In memory, 60 points per run.** The samples live on the run, so a
    stop (or a restart of xbind) starts them over by construction; nothing
    is persisted: an hour's chart isn't worth a write a minute per
    sandbox, and a restart's gap is honest. `cpuPct` is the percent of one
    core over the interval (`usage_usec` delta / wall time); the first
    interval starts at the run's start, its leaf being made fresh for it.
    A reading within a second of the last is dropped (noise, not data).
  - **The list without `series`.** The list is polled by every manager
    and the overview; 60 points × every sandbox would be most of its
    bytes. The latest reading rides the list; `GET` of one sandbox (and
    the manager's `GET /sbx/sandboxes/{id}`) carries the series. Other
    answers that carry a SandboxInfo (start, PATCH, …) leave it out too.
  - **Absent, never zero.** Without cgroup delegation there is no leaf:
    no `usage` at all (and the sampler is never armed), rather than a
    reading of 0 that would read as "idle". The same before the first
    reading and once stopped. Older managers and xbinds don't send it;
    the contract makes it optional for consumers.
  - **memMaxBytes is the leaf's memory.max** — `memMiB` plus the agent's
    and fuse-overlayfs's 128 MiB (a VM's: its VMM's overhead), what the
    OOM killer acts on — not `memMiB`; absent when the leaf has no cap.
- **D214 — A project's overview: one read for the machinery that moves it
  (2026-10-31).** builtin-templates/agent/_backend project_overview.go
  (`GET /projects/{pid}/overview`, the board's classes of runs, tokens,
  wakesOn), project_overview_ext.go (the scm and sandbox cells),
  usage_hourly.go, project_goal.go (`project_goal`, `coord_goals`, the
  `coordinator` change), scm.go `RepoStats`; the template's API.md (§The
  overview, §The coordinator); tests project_overview{,_ext}_test.go,
  usage_hourly_test.go, project_goal_test.go. Contract:
  plans/project-overview.md. The owner wants a project's page to show, on
  one screen, everything that moves the project.
  - **One route.** The strip has six cells (coordinator, workers,
    subagents, tokens, source control, sandbox) and every card names its
    agent. Read separately, that would be six to ten reads, each with its
    own access checks, re-read on every `project` event and every minute.
    One viewer-level read under `projectNeed` gives the same visibility as
    `GET …/tasks` and `…/status` and a single coalesced re-read. The scm
    and sandbox reads, which go to other tiles, run beside the database
    work under one 15 s bound.
  - **Classes of runs.** The page's split is who spends: the person's own
    coordinator, the worker agents (each task's conversation and the coding
    agents it drives), and their helpers (every other descendant, from
    `subagent_spawn`). They are told apart by root and engine, from one
    query over the tasks' and the coordinator's trees. Only the caller's
    coordinator counts. Other people's coordinators in a shared project are
    theirs and private, so a viewer sees the board and no coordinator. A
    worker's state follows its board column, so the strip and the board
    agree. "Idle" is the PR column: CI or a review is next.
  - **usage_hourly.** The runs table has lifetime counters only, so a chart
    over time needs time-bucketed spend. The new table has one row per run
    per hour and is written in `addRunCost`, beside the counters. Coding
    agents report a cumulative cost, not tokens, so each usage update adds
    its increase in USD, or the whole value when it drops (a new adapter
    counts from zero), to the same table. The chart shows cost on its own
    line. Rows are kept 8 days, enough for the week view plus a margin. They
    are pruned at the first write of each new hour and when the table is
    opened. There is no ticker (D81). The table is additive: an older build
    ignores it, and spend from before it is simply absent. We chose not to
    backfill from the lifetime counters, which have no time.
  - **project_goal.** The goal line needs words the coordinator chooses, so
    it gets a tool. Its prompt says when to use it: a new instruction, or
    an aim reached. `now` is the current turn's and clears at the turn's
    end. Until a goal is set, the person's latest message to the
    coordinator stands in for it (`by: "you"`). The goal lives in its own
    table, keyed by the coordinator run, and goes when the run is deleted.
    Setting it, a coordinator's turn starting or ending, and its status
    moving all send the `project` event's new `coordinator` change, so an
    open page re-reads the overview. The event names no person.
  - **wakesOn** is derived, never stored. It lists the caller's own tasks
    with CI pending or a PR awaiting review, because a task's events reach
    its creator's coordinator. It lists `issues` for the owner when
    `policy.autoLabel` is set, because project-level events reach the
    owner's coordinator. It always lists `you`.
  - **Fallbacks.** The scm part is provider-neutral: the provider's hello
    names it. Repo counts come from the provider's new `stats` cap (D216),
    kept 5 minutes per repo and identity. Without the cap, or on a 501, the
    rows carry what the project knows plus the default branch's checks via
    the existing checks read, and `stats` is false. Refusals sit in the row
    (kept a minute) and never fail the read. The sandbox cell is the
    manager's GET, read as the project's owner (as the status reads it).
    The manager's `diskBytes` and `usage` (D215) pass through as sent, and
    are absent when it sends none.
  - **The page (web: project-status.js, project-coord.js, tooltips.js,
    projects.js; native: native/project-status.js, native/projects.js; the
    shared model model/project-overview.js).** Agreed with the owner over
    four rounds of a clickable prototype. Top to bottom: the status strip,
    the board, a drag bar, then the coordinator (header, a one-line goal,
    its newest messages, the input). The owner's calls:
    - **Dense and explained.** It is for professionals who want the high
      level at a glance. Every part has a tooltip, shown after a 600 ms
      hover so it isn't always on; keyboard focus shows it at once.
    - **One scroll, and the input stays in view.** On a tile with room
      (≥ 600 px tall and enough for a 120 px board and a 100 px chat), the
      page fits the tile, the board scrolls inside its share, and the
      coordinator's input is the page's bottom bar (D212). Otherwise the
      whole page scrolls and the input ends the coordinator's section.
    - **The chat never jumps.** The conversation is a window of fixed
      height on the newest messages: the oldest are clipped at the top
      under a fade, it has no scrollbar of its own, and its whole history
      stays behind Open chat.
    - **Folding and sizing are kept per person.** The strip folds to one
      line, and the drag bar shares the height between the board and the
      chat. Both are in xbind's prefs.
    - **Native.** The app's vocabulary has no drag bar or tooltip. The iOS
      screen follows the same order, and each status row opens a sheet
      with its explanation. An older backend (no overview route) gets the
      page exactly as before, on both views.

- **D217 — A drag never falls off its window onto a tile (2026-10-31;
  issue #54).** The owner: stretching the tile window by its bottom-right
  corner "too fast" over another tile lost the drag.
  - **Cause.** The kit's drags (`dragPointer`) already raised a shield —
    a transparent full-viewport layer, so a tile's `<iframe>` can't take
    the pointer — but the corners of the tile window, floating tiles and
    pop-outs were CSS `resize: both`, the browser's own drag, which has no
    shield: once the cursor outran the corner onto another tile's page,
    that page (its own document, often its own process) took the moves and
    the release. The owner's second report has the same root: a CSS
    resize changes only the DOM, so a tile's sub-window (`xbin.window`,
    `.spawn`) kept its spawn-time `w`/`h`, and a mere press on its title
    bar (bring to front: a new `z`, the whole `style` re-rendered) put the
    old size back. The grip writes the window's own geometry as it moves
    (the sub-window's `w`/`h`, the terminal window's `_pop`, a float's
    saved layout on release), so a press, a drag, a re-render keep it, and
    a float's survives a reload (sub-windows close with the page).
  - **Two mechanisms, one helper (web/bx-kit.js `dragPointer`).** (1) The
    shield, now over every drag: the corners became a grip
    (`web/win-resize.js`, `resizeCorner` over the kit's new `dragResize`)
    with the grid tile's corner mark, and `resize: both` is gone from
    `.pop`, `.float` and `.spawn`. The admin popover keeps it: its
    backdrop already covers the tiles. Edges are not grips (they would
    sit over the panes' scrollbars). (2) Pointer capture: the shield holds
    the pointer from the pointerdown (`ev`, new) or else the first move,
    so leaving the browser window doesn't lose it either. On the shield,
    not on the element that started the drag: a re-render may replace
    that element mid-drag (a lost capture ends the drag), and the shield's
    cursor is the one that shows.
  - **The always-remove rule.** A shield left up would leave every tile
    unclickable, far worse than the bug. It comes down first, before any
    callback, on every end: pointerup; a mouse move with no button held (a
    release nobody saw: `onUp`); and, through `onCancel` (else `onUp` at
    the last move), Escape, `pointercancel`, a lost capture and the window
    losing focus. Escape is ended after the event, so an older shell's own
    Escape on the Tiled drag still decides. Escape undoes: windows go back,
    grid and block drags drop their ghosts, dividers keep their ratio. Unit
    tests: `hack/drag-pointer.test.mjs`; the `dragShield` harness pass
    drags fast across tile pages, in a Chromium of its own with the pages
    out of process (only there did the CSS corner stall: 2b9f9d40 fails
    it), then presses, holds and drags a resized float's and sub-window's
    title bar (2b9f9d40 snaps the sub-window back).
  - **Compatibility.** `dragPointer`'s options are additive; a shell from
    before this keeps its CSS corners, and its drags get the capture and
    the ends.
  - **The owner's calls (2026-10-31).** Only the corner is a grip (no edge
    grips); Escape reverts, as built. A tile page that had the keyboard
    gets it back when the drag ends (released or cancelled), unless a
    callback focused something else or the page was re-rendered away —
    before, the shield kept it and the tile needed a click. The IDE
    window's status bar pads its right side to 14 px so its last button
    clears the 14 px grip (2 px again at phone width, where there is no
    grip).
- **D218 — A person who can change a partitioned tile's code reads its
  global instance's log from their partition (2026-11-02; issue #55).**
  The owner: a tile agent "can only view its own partition. When someone
  can change tile code they should also be able to view global instance
  logs". D156 had refused `GET /logs?xbin-partition=global` to every
  credential acting in a person's partition (its "not chosen": listing the
  global instance's log to a person's partition), so an agent session in
  a writer's partition couldn't read the log of the code it was changing.
  - **Chosen.** The tile's own terminals, agent sessions and frames in a
    person's partition read the global instance's log when that person
    holds write on the tile at their level now (`codeWriterOf`,
    internal/broker/partitionlogshare.go) — the level that changes the
    code (docs/partitions.md §What partitions protect: whoever can change
    the code already reaches every partition's data through it). After
    that, apiLogs' gate is today's (the tile's own credential).
  - **Unchanged.** The partition's backend (its instance token names no
    person), view-as, another tile's credentials and anyone without write
    on the tile stay 403; the default answer stays the caller's own
    partition's log; a person in the shell still needs terminal level or
    admin, as before (the issue names the tile's agent).

- **D219 — The size caps rise to 1000 lines; a core file may have a
  ceiling instead of a ratchet (2026-11-03).** The owner: the budget had
  become arbitrary for a codebase this far along, and the agent template's
  `agent.js` is core. Reviewing the agent-template series, #13 (MCP by
  URL) took `agent.js` from 946 to 957 lines against its 949 budget.
  - **The caps.** An unlisted non-test Go file and an unlisted shipped
    `.js`/`.mjs`/`.html` may reach 1000 lines, up from 800 and 900. The
    listed budgets stay as they were, ratchets: a listed file still may
    not grow past its number, and still is asked to lower it once it
    shrinks below 90 %.
  - **Ceilings.** A line in `hack/size-budget.txt` may end in `ceiling`:
    room a core file may grow into, never asked to come down (otherwise
    the 90 % rule would undo the room at once). `agent.js` is the first,
    at 1500.
  - **Not chosen.** A two-tier rule (500 lines for basic files, 1000 for
    core) would have failed 131 files that passed under the old caps, and
    needed each classified as basic or core; the owner chose to raise the
    caps alone.
- **D220 — A place in the workspace is a link: the shell's query, and a
  tile's fragment inside it (2026-11-08; #7).** Nothing in the shell's URL
  said where the person was, so nobody could be sent there.
  - **The grammar.** `/c/root/?screen=<id>&tile=<path>&at=<fragment>`,
    each optional, values escaped only where a query needs it (a tile path
    keeps its slashes). The query, not the shell's own fragment: it is the
    convention the limits page set (`?tile=`), and a query can be carried
    through a sign-in's `?next=` later, a fragment never reaches the
    server. Pure parse/build in `shell/route.js`, tested round-trip
    (`hack/shell-route.test.mjs`).
  - **A tile's place is its fragment.** Tiles already deep-link that way
    (the admin console's `#tab/sub`, the welcome notes' `#topic/child`).
    `xbin-client.js` reports it to the frame (`xbin:location`: start,
    hashchange, popstate, and the page's own push/replaceState) and applies
    `xbin:navigate` by replacing the fragment — hashchange fires, no reload
    and no history entry of the tile's own. The path and query stay out:
    under strict asset gating they carry credentials.
  - **History.** A different screen or front tile is pushState; the tile's
    fragment changing is replaceState on the shell — the tile's frame
    already made its own entry when it navigated, so pushing too would make
    Back take two steps. The first write after a load or a popstate
    replaces: opening the workspace adds no entry.
  - **Landing.** The screen is activated (un-parked, an org tab un-hidden);
    the tile is the one on it, else on another visible tab, else opened —
    on the active personal screen, or the person's own when the active (or
    linked) one is a shared org screen: a link never opens an org draft.
  - **What can't be shown says so** in a strip above the canvas: a screen
    not among the viewer's tabs ("doesn't exist any more, or isn't shared
    with you" — a personal screen's id never is), a tile not in their
    `/components` (gone or unreadable: the list is filtered server-side, so
    the shell can't tell which and doesn't guess), an offloaded tile. The
    rest of the link still applies.
  - **Not done.** Signing in drops the query (the root page isn't a
    `loginNextPaths` page; adding it is a server change of its own). The
    frame window's IDE state (open file, terminal) isn't in the link.

- **D221 — AgTT Projects say what really happened to a task: its PR
  wherever the agent opened it, a PR's end however late, a person's
  question as the question, a sandbox being made as being made
  (2026-11-08).** The owner tried Projects and passed on the coordinator's
  report of five defects: "Check in detail what can be causing what, don't
  trust it blindly". Each claim was checked against 003700b3 before
  anything was built; two were half wrong, and the fixes follow the code,
  not the report.
  - **A PR binds to its task wherever the agent opened it (confirmed).**
    The coding agent opened #58 itself with `gh` (the project's token is
    in its `GH_CONFIG_DIR`) from `xbin/bchzwo/review-agent-template-series-17`;
    every match knew only `k.Branch`, and `prScript`'s `OFF` line was a
    quiet note. Now `TaskPR.Head` is a branch the task pushes to, and
    `taskBranchIn` answers every match (the push script's `BR_i`, the
    create, `jobRefs`, `pull.opened` and push routing, `project_refs`, the
    CI watch) — the head of its one OPEN pull request there that it may
    push to, else `k.Branch`. A PR is adopted on its own only when it is the
    one OPEN PR from the checkout's branch, from the same repo, by the
    project's own login, its head the checkout's HEAD or an ancestor of it
    but not on `origin/<default>` already, while no other live task holds
    it or its branch and the task has no other open PR in that repo
    (checked again in the transaction that records it) — the login alone
    proves little, since a person's project acts as that person, so the
    checkout is the evidence. Adopting never ends a task: a merged or
    closed PR (an old one from a reused branch name) is only named.
    Anything less says so once, visibly, naming `task_link_pr`. Linking
    refuses another live task's PR, a head that is the default branch or
    another live task's branch, and a second open PR in a repo (one open
    PR per task and repo: the poll rows are per repo); a link the task may
    not push to (another author's, a fork's) is `tracked` — followed and
    shown, its CI failures and review comments never sent to the task. A
    merged PR may still be linked (task #9 / #58 merged before this), so
    cleanup no longer takes "a PR merged in the repo" as "pushed": a merged
    PR holds a checkout's commits only when HEAD is its head or in its
    history (its head fetched as `pull/<n>/head` if missing); else the
    commits count as unpushed and the task is `blocked` for a person, its
    fork kept. The refs job writes what it read onto the task as stored in
    its transaction, so a concurrent end or link isn't reverted. The brief
    says to stay on the branch and never `gh pr create`.
  - **A PR's state stays true (the report's "never re-derived" was
    wrong).** States were always derived (`taskDerived`); the facts went
    stale. #57's last push was 2026-11-02 11:31, its merge 2026-11-05
    00:38: the pull row had stopped 24 h after the push and the webhook
    never came. Now an open PR's `pull` and `comments` rows back off to
    every 6 h instead of stopping (a `checks` row still gives up, with its
    note); the scm loop re-tracks open PRs with no row at start and daily,
    so a task stranded before this heals; `task_refresh` reads them now.
  - **A person's question reads as the question (partly confirmed).** A
    question can't outlive its turn (every turn end clears it); task #8
    most likely asked a new one and waited 57 h, with the coordinator
    told only "waiting for a person" — the words it also used for an idle
    task whose answer was ready. Now it reads the question, quoted, and
    its age (`pendingState.since`), and an idle task as "its answer is
    ready"; `task_message` says who must answer. Nothing expires a
    question and the coordinator can't decline one (its messages stay
    held, `HoldPark`). A question the agent withdraws (`$/cancel_request`)
    is settled in the sdk like a withdrawn permission.
  - **What waits shows on the project's page (#56; partly confirmed: it
    was pushed and in the global Needs you, not on the project).** `/needs`
    items carry `text` and `since`; "Waiting for you" heads the overview
    and the board (red: questions, approvals, sign-ins; yellow: failures),
    the coordinator's own `ask_user` included, each with Open; the list's
    badge splits real needs (`counts.asks/failed/coord`) from ready ones.
  - **Infrastructure isn't a person (confirmed, two causes).** A big
    task's fork that was still being created was started (409, "a creating
    sandbox can't start") until five tries ran out against a 15-minute
    create: it now waits (`forkCreateWait` 2 minutes per call, then looks
    again without spending attempts), and a fork its manager says failed
    is deleted and made again on Retry (it holds no work: only a first
    prepare makes one). A snapshot fork carried the project sandbox's
    coding-agent login — tested, all five providers' files survived — and
    copies of one refresh token sign each other out: forks now remove each
    provider's `acp.Provider.LoginFiles` and use a saved sign-in (D179),
    saying before the first turn when none can go in. A coding agent
    waiting for its sign-in is the state `agent-signin`, not a question;
    a fork's scm sign-in is the `signin` workspace state small tasks have.
  - **The coordinator's new tools** — `task_link_pr`, `task_refresh`,
    `task_retry` — record what the provider says or re-queue failed
    workspace jobs. It still never merges, approves, pushes, comments or
    answers a person (the report's constraints); `task_retry` was not on
    the report's list and is recorded here as the owner's to veto.
    Issue create/comment/close (allowed by the report) is left out: no
    defect needed it, and it widens the scm contract.
  - **Owner's call: mint, don't copy.** Asked whether forks should drop
    the copied login, the owner answered: "Don't copy whatever token would
    be 'bound' to one claude path, see if we can mint new tokens from the
    in-partition signup? Whatever is claude/acp standard way of doing
    this". So the removal stays, and the standard way for one CLI login
    used in many places — a long-lived credential that doesn't rotate:
    Claude Code's `claude setup-token` (`CLAUDE_CODE_OAUTH_TOKEN`), an API
    key for Codex and Gemini CLI — is the path, through D179's saved
    sign-ins (the person's partition vault; `credPick`/`credWhy` hand it
    to every fork, gates unchanged). A task parked on `agent-signin` gets
    a card whose first offer is "Sign in once for every big task": the
    guided sign-in with Remember, and `default: true` with its code so the
    minted sign-in becomes the coding agent's default even over a refused
    one (for a coding agent with no mint, a key pasted and saved as the
    default); "Sign in for this task only" comes second. The park's
    `login.save {ok, why}` (the gate as judged at the adapter's start)
    decides the offer; a team project's fork shares its members, so no
    saved sign-in goes in and the card says so. The fork's note before the
    first turn points at the same one-click path, and the project's status
    warns its person while a coding agent there runs on the sandbox's own
    login with nothing saved. Where minting can't happen — an unpartitioned
    agent, the global instance (A-M1: no person's credentials there) — each
    fork still asks, and the words say why (saved sign-ins need a person's
    partition; with partitions on, one serves every big task); no shared
    vault was invented.

- **D222 — A project keeps a wiki its agents read and write, and the
  coordinator reads code through research subagents (2026-11-08).** The
  owner, after D221's report: the coordinator is "a somewhat weaker
  agentic model and I don't think it has great access to understand the
  projects by itself - which itself is a problem"; give it "sub-agents
  with read-only access to code and code search tools" and a "Shared
  project KB based on ideas from
  https://www.anthropic.com/research/components-of-agent-memory, so -
  coordinator can R(/W?) the wiki, research subagents can R/W wiki, coding
  agents can read the wiki and at the end they have a 'dreaming' round
  where they write the memories". Owner calls: the wiki lives in the
  project's store, versioned; "Everyone writes directly, on conflict:
  write retuns: 'other agent changed contents of this article since you
  last read it, read the article again before editing'".
  - **The store.** `wiki_pages` (slug, title, body, version, archived,
    who and which run last wrote it), every version in `wiki_revisions`,
    `wiki_reads` (the version each run last read), `wiki_fts`. 16 KB a
    page, about 400 a project; written text redacted (`projRedact`).
  - **Optimistic concurrency, kept by the server.** A run's read records
    the version it saw; a write or edit to a page is a compare-and-swap
    against that version, so a weaker model never carries version numbers.
    A stale one gets the owner's words: "Another agent changed the contents
    of this article since you last read it — read the article again before
    editing."; a page it never read, "read the article before editing it".
    People write through `PUT …/wiki/{slug}` with `baseVersion` and get the
    same words with a 409.
  - **The article's lessons, applied.** A small injected layer: the
    `index` page (about 60 lines) rides the coordinator's prompt and each
    task's brief, framed as notes that may be wrong, its version
    snapshotted at the task's start so the prompt cache holds (pruning
    keeps every snapshotted version). Injected text is defused like
    untrusted text — invisibles out, the frames', the coordinator's and its
    own tags' markers defused in any case or spacing, 3 KB at most — and
    the coordinator counts the wiki among what is data. Navigation
    plus search (`[[links]]`, `wiki_search` over FTS5). The dream prompt
    steers the goal — the next agent on this project — not steps, asks for
    verified facts that quote paths and identifiers verbatim, and forbids
    status and what goes stale. Size, reads per page and dream tokens show
    under the Wiki tab.
  - **Research subagents.** The coordinator stays in the web class (no
    sandbox); `research` starts a subagent through the subagent machinery
    with a `codeRead` binding built by the agent, never from the call: its
    own detached worktree of a repo's default branch under
    `.xbin/research/r-<id>/` (removed when it answers; stale ones swept),
    or a small task's worktree. Its tools are `read`, `ls`, `glob`, `grep`
    and the wiki, and a read-only `git` — `runTool` refuses everything else
    whatever the model calls, and an API-made conversation can't carry the
    binding. Paths resolve inside the project after symlinks; glob
    patterns may not be absolute, `~` or climb with `..`.
  - **Research's git, after the review** (git grep ran `sh` through
    `--open-files-in-pager` behind a pattern of `--`; `git diff <outside>
    /dev/null` became `--no-index`). argv only, and every argument parsed
    by the agent against an allowlist per subcommand (`log`, `show`,
    `diff`, `blame`, `grep`, `ls-files`; `status` dropped): options by exact
    name, one flag an argument, a short option's separate value never
    starting with `-`, a long option's value always attached — so git and
    the parser can't disagree about where an option or a path starts.
    Revisions and paths before and after `--` refused when absolute, `~`,
    pathspec magic or out of the roots. `diff` only between commits or
    against the index, `blame` only at a named revision: the forms that
    read the working tree through git's filters are gone. Run with
    `GIT_CONFIG_GLOBAL=/dev/null`, `core.attributesFile=/dev/null`,
    `log.showSignature=false`, fsmonitor and hooks off, no textconv or
    external diff. **Residual risk:** the repository's own `.git/config`
    is still read, so a filter or diff driver planted there could run on a
    form that still reads a blob through a driver — only someone who can
    already write the sandbox (and so run anything in it) can plant one.
  - Four research subagents at once per project, ten minutes each.
    Project code isn't internal data, so the coordinator's lane is
    unchanged.
  - **Coding agents** have no tools of ours: they read a copy of the wiki
    written to `.xbin/wiki/<slug>.md` (a header names its version) before a
    prompt reaches them when it changed, and write in the dream round to
    their task's own `.xbin/wiki-out/<n>/<slug>.md` (small tasks share a
    sandbox) keeping the header of what they read; the ingest applies the
    files written since the round began with the same compare-and-swap
    and, on a conflict, copies the wiki again and sends the conflict's
    words once more (two rounds at most). A version-0 page over an
    archived slug brings it back rather than conflicting forever. Built-in task runs get the wiki tools, writing only in
    their dream turn.
  - **The dream round.** Once when a task's PR opens after a turn it
    worked (`pr.opened`: tasks from before this build don't all dream at
    their next push), and once more when the task merges after three or
    more turns since — never when a person closed or stopped it. Project
    policy `dream` (default on).
  - **A turn of its own, after the review** (a coordinator's message and
    the dream were delivered together; a message mid-dream stranded the
    ingest; the dream erased the work turn's D198 verdict; the end dream
    raced cleanup). The queue delivers the round only to a resting run
    with nothing else pending and its last work turn's verdict in (the
    judge's end releases it; 90 s at most); what is queued after it, and
    anything queued while it runs, waits for its end (a person's own
    message goes straight in as ever). Its delivery records that the next
    turn is the dream's (`wiki_dreams.active`) — the write right, the
    ingest and the judge key off that, never `turn_by`. The dream turn
    isn't judged and doesn't take the work's verdict down. It is held
    behind a person's question as the coordinator's messages are; its
    answer doesn't replace the task's latest. The end dream holds the
    task's cleanup back until its pages are in (30 minutes at most) —
    chosen over skipping it, since cleanup is on by default.
  - **Tried with agents (2026-11-08).** A lab ran five real agents
    (Sonnet and Haiku) on the real wiki code — coordinator, research
    subagents, a built-in task and a coding-agent task. It showed: a task
    started before the wiki had an index never saw one (a "no index"
    snapshot was kept for life — now it isn't: the index is taken once it
    exists, then kept); agents had to guess the map's slug (prompts and
    tool descriptions now say `index`); they asked for a way to tell
    verified from reported from not runnable (no schema field: every
    writer is asked to say "(read in path:line)", "(ran `cmd`)" or
    "(reported by ‹who›, unverified)", and to keep facts about its own
    session in the note); they repeated the title as a heading (a leading
    `# …` that repeats the title is dropped on write and ingest); "‹slug›
    is at version 2" read as an error (now "edited/wrote/archived ‹slug› —
    now version N"); an edit without a note left a blank reason (agents'
    changes now need one; people's `PUT` doesn't); the folder README and
    the research prompt still named `wiki-out/<slug>.md` and git `status`.
  - **Tried with agents, round 2 (2026-11-09).** Ten agents: a real
    build-and-test starter task, a Haiku coordinator bootstrapping the
    wiki from empty, four research subagents in parallel, an A/B against a
    no-wiki control, two dream rounds at once. What held: 10 of 10 sampled
    claims matched the code; four writers on `index` lost no update; the
    second dreamer was refused, read again and rightly added nothing. A/B:
    the control (no wiki) took 44k tokens, 4 tool calls, 34 s and didn't
    know how to run the tests; with the wiki 53–54k tokens, 6–9 tool
    calls, 44 s, and both knew the build recipe and the test patterns —
    better plans for ~20 % more tokens. Fixed after it: a leading heading
    other than the title becomes a plain line; the conflict keeps the
    owner's words and adds "(now version N, by …)", in the dream's
    follow-up too; the index is framed as the index page with "that page's
    own version" (agents read "version 5" as the wiki's); an all-words
    search that finds nothing falls back to any word, said so; research
    subagents get the framed index in their prompt; a research author is
    `research #8 "‹label›" (…'s coordinator)`; each revision records the
    commits it was read at (`at`: research's checkouts, a task's pushed
    heads), shown in `wiki_read`, `wiki_list` and the tab; the writing
    tools say "(note required: one line, why)"; a missing `index` says how
    to start it; the dream prompt says to re-read just before writing and
    not to sign what it adds; one provenance line suffices for a table or
    a list read from one place.
  - **Round 3 (2026-11-09):** four fresh agents on round two's wiki rated
    it about 4/4; fixed after it: "alias" now finds "aliases" (porter
    stems them apart — words of 3+ letters match as prefixes too; the
    tokenizer also folds diacritics, an older `wiki_fts` rebuilt once),
    the any-word fallback leaves out common English words, headers carry
    absolute times, writers cite paths and commits never run or task
    numbers, a dream adds to `index` only a line for a new page, and a
    dream page that lost is kept as `<slug>.md.rejected` to merge from.
  - **Not done.** A task reads "working" during its dream turn; on a
    downgrade a queued wiki sync or ingest job fails its task's workspace
    ("not in this build"); real Claude Code/Codex sessions writing
    `wiki-out` were not exercised (the fake agent's files are written by
    the test).

- **D223 — A new project is made in a wizard, and can start with built-in
  work: build and test it, then write its wiki (2026-11-09).** The owner:
  "project creation/onboarding - should split into few screens wizard
  style, adding projects is a bit janky (find repo should be a more
  readable dropdown, shouldn't have another add step) and the wizard should
  allow starting the projects with some built-in 'clone, compile and run
  tests' -> 'create initial KB' tasks".
  - **The wizard** (web and app, one state, `app.projects.form`): Code →
    Name → Sandbox → How it works → Get started → Review. Steps that don't
    apply drop out (a team definition: no sandbox, no starter), Next says
    why it is disabled, Review has an Edit per part. The repos are a
    readable combobox (ARIA): your repos on focus, filtered as you type
    (debounced, the latest answer wins), private, permission and archived
    shown (archived ones disabled); choosing one adds it — no Add step;
    each chosen repo's setup script is a collapsed disclosure.
  - **The starter is the backend's, not the page's** (`POST /projects
    {starter: {build, wiki}}`, stored as a small pending record; no new
    state machine). `build`: once the project's sandbox and repos are ready,
    task #1 "Clone, build and run the tests" is created as its creator,
    with a built-in brief (per repo: how it is built and tested, worked out
    from its manifests when it has no build docs; the exact commands, the
    toolchain, timings, what passed and failed, what had to be worked
    around; no code changed, no PR). `wiki`: the creator's coordinator gets
    one message from the platform (source `starter`) to build the initial
    wiki from task #1's answer with research subagents.
  - **The build task dreams before the wiki starts** — the lab (D222) had a
    real agent do the build brief: it found the facts most worth keeping
    (the sdk from github.com/xbin-dev/xbin, a go.work, `-mod=mod` breaking
    workspace mode, `go build` needing `-o`), and a starter task (one turn,
    no PR) would never have a dream round to keep them. So task #1's first
    turn ends with its dream round (with the wiki step or without it), and
    the coordinator's message waits for that round's pages — at once when
    dreaming is off, the turn failed, or the task ended; at most 30 minutes
    otherwise. A turn a person stopped isn't the builds' end (the next turn's
    is); one that hit its limit counts, and the dream and the message say it
    stopped part-way.
  - **It never waits unsaid** (the review): a workspace job it waits for,
    or its own job, that failed is a `starter:` note, and Warm (or Retry on
    a task) goes on from it; an archived project's starter is queued and
    held like its other jobs; a kick while its job runs reruns it. Warm
    after a failed clone used to get the old exec's answer (the clone's
    client id repeated across jobs): it now carries the job's id.
  - **Once only**, across restarts; a failed task #1 still starts the wiki
    step, which says what failed. The `[project starter` marker is defused
    in text from outside, like the coordinator's other markers.

- **D224 — One chat kit for the Agent tab, the agent template and the
  workspace agent; a newer template needs its xbind (2026-11-14; issue
  #51).** The Agent tab (`web/agent-*.js`), the agent template
  (`builtin-templates/agent/`) and, next, the workspace agent draw the same
  things — markdown, tool cards, asks, a composer — from separate
  implementations (`plans/agent-dedupe.md` §1). They move onto one kit
  under `web/chat/` (`/vendor/chat/…`), public and additive like the rest
  of the frontend kit, a step at a time. This entry grows with the steps;
  the first is markdown (F1).
  - **One HTML renderer: `/vendor/chat/md.js`.** `web/bx-md.js` and the
    template's `chat-md.js` were two renderers with one policy (raw HTML
    escaped, images as text, links on safe schemes) and small differences.
    The kit's has both sides' parts: the template's line-based closed-fence
    check and optional highlighter, the web's block memo, `mdCssText`.
    Exports: `md`, `mdInto(el, s, memo?)`, `mdCssText`, `closedFence`,
    `mdEpoch`, `onHighlighter`, `useHighlighter`.
  - **Its own `Marked` instance.** Both old modules called `marked.use()`
    on the one shared `marked` of `/vendor/marked.esm.js`, so two renderers
    in a document would have stacked their options. The kit's instance is
    its own, and so is `web/xb/rt-markdown.js`'s (it built a `Lexer` from a
    full options object per call to stay clear of exactly that): an old
    template instance still configures the shared `marked` in its own
    document, and nothing of xbind's listens. rt-markdown's tokens are
    byte-identical (`native/tools/markdown-parity.mjs` rewrites
    `expected.json` unchanged), so Swift's `MarkdownLexer` is not touched.
  - **`/vendor/bx-md.js` stays the renderer with the highlighter up
    front; the lazy one is the new URL.** The plan had `bx-md.js` itself
    go lazy ("plain text until the highlighter loads"). It can't: scaffold
    copies of `shell/shell-blocks.js` in workspaces import `md` from it and
    draw `unsafeHTML(md(text))` once — they know nothing of `mdEpoch`, so a
    lazy `md` would leave a text block's code plain until some later
    render (rule 3 freezes what a served URL does, not only that it
    answers). A static import is the one way to have the highlighter
    before the first call (top-level await was ruled out: WebKit's
    multi-importer bug, and `bx-md.js` has two importers in the Agent
    tab). So `bx-md.js` is four lines: import highlight.js, hand it to the
    kit (`useHighlighter`), re-export. Both URLs are the same module state
    in a document. The Agent tab keeps `bx-md.js` and its behaviour; the
    template takes the lazy URL and keeps its own (no 127 KB before its
    first paint). A host picks by which URL it imports.
  - **The unifications, each the safer or the already-served rule.**
    (1) *Closed fence*: line-based — the last line is a fence of the
    opening character, at least as long (the web's regex took any trailing
    fence line, so a ```` block whose last line was ``` was coloured while
    still open). (2) *Size cap*: 32 KiB, the web's — what `bx-md.js`
    callers in the wild already get; the template's 20,000 characters goes
    (blocks between the two are now coloured there). (3) *Language class*:
    the web's allowlist `^[\w+#.-]+$`, else no class attribute at all (the
    template escaped any string into the attribute); highlighting needs
    the same name (every highlight.js name and alias passes). (4) *Image
    class*: `md-img`; the template's page gains a `.md-img` rule equal to
    its `.muted`. (5) *CSS*: `mdCssText` for shadow roots as before; the
    template keeps its own global `.md` rules, which differ on purpose
    (coloured headings and list markers, #26) — moving it onto
    `mdCssText` would have changed its look. (6) *mdInto*: one function
    with the optional memo and the epoch; a memo is dropped when the epoch
    moves.
  - **Not unified: link safety between HTML and tokens.** The HTML
    renderer refuses `javascript:`, `data:` and `vbscript:` and keeps
    everything else (relative links, anchors, `tel:`); rt-markdown allows
    only `http`, `https` and `mailto`, because a native app opens what it
    is given. One rule would change one side's output (relative links in
    the wiki and in screen text, or the Swift parity corpus), so both
    stay, and rt-markdown's header says so.
  - **A newer template needs its xbind: `/vendor/chat/rev.js` and
    `boot.js`.** Template instances are forks in workspaces, never updated
    by an upgrade, and they import xbind's modules by `/vendor/` URL. Old
    instance on new xbind keeps working (nothing served is removed). New
    template on old xbind is the new case: a static import of a module or
    export the old xbind lacks fails the whole graph — a blank page. No
    second code path is kept for it (that would be the duplicate this
    programme removes); instead `rev.js` exports `KIT`, a number that only
    goes up as the kit grows (1 = the markdown renderer), and the
    template's `index.html` starts `boot.js`, which imports `rev.js`
    guarded, compares `KIT` with `NEEDS.kit`, and either imports
    `agent.js` or replaces the page with "This agent needs xbind ≥ v0.3.80
    — update xbind" and a link to `/docs/install.md` (on the workspace's
    origin when the tile has its own). A failure of `agent.js` itself is
    re-raised with `reportError` — an uncaught error as a failing `<script
    type="module">` was — never that message. `index.html` preloads
    `agent.js` (`modulepreload`) so the probe costs no round trip. The
    native view imports only models and `/vendor/xb-native.js`, nothing of
    the kit: it needs no guard, and where the app falls back to the web
    page (`docs/native.md` §Fallback) that page carries the guard.
    `docs/compat.md` states it under "What this does not promise", next to
    the Go case it mirrors (new SDK calls don't build on an older xbind).
  - **Guards.** `make js-check` refuses a second definition of `mdInto`,
    `mdCssText`, `closedFence`, `onHighlighter` or `useHighlighter` in the
    shipped trees (`md` is too common a name to ban);
    `hack/agent-template-boot.test.mjs` fails when the template asks for a
    kit revision this checkout doesn't serve; the template's
    `test/needs-xbind.mjs` drives the page with the kit, without it (404),
    with an older one, and with a broken `agent.js`.
  - **Open.** `NEEDS.xbind` is `v0.3.80`, the release after v0.3.79 — the
    first that can carry this; whoever cuts the release this lands in
    checks the number. Later steps (tool and ask models, views, composer)
    add to this entry and raise `KIT`.
  - **F4 — the agent template on the views (KIT 4).** The template's
    transcript is drawn by the kit: `model/fold.js` `blockOf` (the
    fixtures' AgTT adapter, now its code — a converted block is kept with
    the fold's block it was made of, so `v` moves exactly when the block
    does; the call in flight hands its `$memo` on), `model/harness-ask.js`
    `parkBlock`, and `chat-kit.js` (the page's `ChatCtx`, `kitRow`,
    `kitPark`). Decided along the way:
    - **Where the two looks differed, the richer one went into the kit**,
      so the Agent tab changed too: a call's line says what it came to
      (outcome chip, subline, a still mark while it runs, its state only
      while that needs a look) instead of a boxed status on every card;
      the request card leads with who asks what and takes a word with a
      rejection; a person's own words sit on their side and unlabelled;
      markdown has the reading colours. The kit's bodies were kept where
      they were equal or better (capped highlighted diffs in place of a
      row per file to unfold; a command box with Copy; output behind
      "show all" as a `<details>`).
    - **A host's own tools draw on the same card** through the block's own
      words (`icon`, `sub`, `outcome`, `args`, `result`) — not a second
      card, not an AgTT branch in the kit. A journal line is the kit's
      `notice` with `icon` / `tone` / `md` / `action`; an engine-written
      message is a `notice` with `fold`.
    - **Instance patches and tests hold on by hooks, not by markup**:
      `ChatCtx.rowAttrs` puts the page's old classes and data attributes
      on the kit's row (`.tcard[data-tool]`, `.msg.user`, `.ask.hask`…);
      inside a row the markup is the kit's. The template's old rules for
      those classes are scoped to elements the kit did not write
      (`:not([data-kind])`), for the cards feature modules still draw
      (`ext.block`: a coding agent's child card, a document's line).
    - **A fold follows the host** (`?open=${ctx.isOpen(…)}`), and
      `setOpen` hears a close: the template keeps "open" in its Session,
      and a row's `.on` hook must be true.
    - **No fallback for an xbind without the scroll window**: the page
      needs kit 4 before anything loads, so `chat-window.js` is only the
      Session's side of the kit's `ChatWindow`.
    - **Not moved: the composer.** `#msg`, Send / Stop, the queued chips,
      the slash menu, the pickers and `#hctl`, and the attachments are
      still the template's. Three things stand between it and
      `<bx-chat-composer>`: seven modules read and write the textarea
      directly (drafts per page job, the hosted lock, example prompts),
      the template uploads a file when it is attached (`/files`, chips
      with a state) where the kit's composer stages bytes until Send, and
      `#hctl` is a popover (modes as radios with a confirmed bypass, the
      person's setting, the account) where the kit has `<select>`
      pickers. `plans/wsagent/chat-kit-spec.md` §12 has the mapping.
    - **Tests' browser sees no host network** (side item): Chromium fails
      queued requests with `ERR_NETWORK_CHANGED` on any interface change
      and has no switch for it; the UI harness now starts it in its own
      network namespace with xbind's port relayed
      (`hack/ui-harness/quietnet.js`). The template's browser scripts
      answer every request by a route and are immune.
- **D225 — The agent session rules live in sdk/acp: tool record, mode
  escalation, steer queue, auth state, status words (2026-11-14; issue
  #51).** Two hosts run coding agents through `sdk/acp`'s client — xbind's
  tile agent sessions (`internal/term`, `internal/agent`) and the agent
  template's harness engine (`builtin-templates/agent/_backend`) — and each
  had its own reading of what to do with the event stream
  (`plans/agent-dedupe.md` §1, §4 B1). The hosts stay two (different
  stores, different lifetimes); the rules move to five pure files of
  `sdk/acp` — no I/O, no goroutine, no clock of their own, table-tested —
  that both call, and that the workspace agent will call too. `sdk/` is
  the only place a tile backend can import from, so that is where shared
  Go lives; every name below is public API that only grows.
  - **`toolrec.go` — the tool record.** `ToolRecord` is a call's
    `tool.call`/`tool.update` events merged (the chat kit's record,
    `plans/wsagent/chat-kit-spec.md` §2, K6–K12); `ToolRecords` folds a
    session, with the id-reuse rule (`StartsToolRecord`: a `tool.call` for
    an id whose record is over, or of an earlier turn, starts a new one —
    the union of the two halves each side had) and an embedder's
    `files.changed`. `ToolPolicy` is the one parameter set: xbind keeps
    everything and coalesces bare output chunks in its log
    (`OutputChunk`); the template keeps the newest 64 KiB of output, 64 KiB
    of text, an edit as a patch (`Diff`), and an 8 KiB result tail
    (`OutputTail`). The Go fold reproduces the shared fixtures'
    `<name>.records.json` exactly. The template stores what it stored
    (`harnessMeta`, with `tool` and patch-form diffs: rows written by
    earlier versions read back), so its `acp` row meta is byte-identical on
    every fixture; four intended differences are tested in
    `harness_toolrec_test.go` (an id of an earlier turn's unfinished call,
    cuts between characters, an unknown kind stored as `other`, a call
    first named by its permission request takes the request's content).
    `internal/agent/page.go` asks `ToolRecords.Observe` which card an
    event lands on; because a client's fold may be older than the daemon,
    it also keeps two same-turn calls of one id in one page (the old fold
    merged them) — that clause goes when no fold from before the rule is
    in use.
  - **`escalation.go` — does an option raise the mode.** The template's
    `optionRaises` is `Provider.OptionRaises` (with `ModeOpen`, `ModeIDs`,
    `ModeOption`, `SessionState.Mode`). The client marks the options of
    every request it files — `PermissionOption.Explicit`, `explicit: true`
    in `permission.request`, the pending list and the tail page's state —
    judged with `Config.Mode` as the start mode: a session opened in a
    bypass mode by its person is not "raised" by returning to it. xbind
    refuses nothing; the flag is for the Agent tab's second confirmation
    (K13, gap W3 closed). The template keeps refusing such options to
    non-owners, judged with its own start mode (the adapter's first).
    Rejected: marking in xbind's pump — the requests a client re-reads
    (`GET …/sessions/{id}`, the page state) come from `Permissions`, which
    only the client fills.
  - **`queue.go` — a message while a turn runs.** `QueueState.Message`
    (prompt, steer, hold), `SteerOutcome`, `QueueState.Flush`, and
    `DetachedTurn` for the turn codex-acp starts when a steer lands just
    after its turn ended. Each host keeps its store (xbind: memory and
    `queue` events; the template: inbox rows and its steer mark) and its
    timer. xbind gains the detached turn it lacked
    (`internal/term/agentdetached.go`): the session says running, holds
    messages, and ends the turn itself with a `turn.end {stopReason,
    detached: true}` — after 5 s of quiet with no request waiting, or at a
    cancel. Two choices there: statuses the client restates as idle are
    rewritten in the pump rather than teaching the client about detached
    turns (the template would have had to unlearn its own handling in the
    same step); and a turn waiting on a person is never "quiet" (the
    template ends its detached state at the quiet time and goes idle at the
    answer — left as it is). One client fix came with it: a request
    answered outside a prompt returns the status to idle.
  - **`authstate.go` — signed out, and what to say.** `IsAuthRequired`,
    `SaysSignedOut`, `AuthState.SignedOut` (a host that injected a
    credential trusts only the agent's own refusal) and one sentence,
    `Provider.SignInHint(where)`. xbind's longer sentence goes; it names
    the place ("in a terminal on ‹tile›") and uses `LoginCmd`, the line
    that works in a sandbox terminal.
  - **`summary.go` — status words.** `Busy`, `TurnOver`, `StateOf` (the
    template's `harness.state` words, now also the SDK's), `Summary` (what
    xbind's `term` status event carries), `Need` (heading and sentence for
    an approval, a question, a sign-in). Strings a client or test matches
    are kept on both sides; two fallback push bodies of xbind's take the
    template's wording. Not unified: stop-reason words (xbind's push says
    "Stop reason: ‹reason›" and a test holds it) and the three vocabularies
    for "what it is doing" (ACP status, the template's `activity.kind`,
    the Live Activity's phase) — they are wire formats of three clients.
  - **Compatibility.** Everything is additive on the wire (`explicit`,
    `detached`) and in the SDK; the v0.3.79 template backend builds and
    passes its tests against this `sdk/`. A newer template needs the SDK
    of the xbind it ships with, as ever (`docs/compat.md`, "What this does
    not promise") — no fallback code is kept for an older SDK.
  - **Not here.** The guided sign-in driver (`guided.go`) and anything
    about credentials land with the sign-in store (B4); the provider table
    for the LLM connector with the connector.
  - **F2 — the models: `/vendor/chat/tools.js` and `/vendor/chat/ask.js`;
    `KIT` 2 (2026-11-14).** Both pure (their only imports are each other
    and `blocks.js`, relative, so node tests and the parity script load
    them without a resolver). `tools.js` is one tool call read from its
    `ToolRecord`: the merge of an agent's events (`mergeTool`,
    `ToolRecords` — the executable rules of
    `plans/wsagent/chat-kit-spec.md` §2, which lived in
    `hack/chat-fixtures/reference.mjs` and are now shipped code the
    fixtures hold), headline, subline, outcome and state chips,
    `stripAnsi`, the output's tail, diff helpers, plan approval. `ask.js`
    is a permission request's card, a question's form, the slash menu, the
    composer's words, `cleanCode`.
    - *The old modules stay, thin.* `web/agent-tools.js`,
      `web/agent-slash.js` and `signin-scan.js`'s `cleanCode` re-export
      (URLs and names are frozen, rule 3; the Swift port is held to them).
      `agent-tools.js` is also the adapter from the Agent tab's old record
      (`tk`, the last content list) to the kit; `newTool` now carries the
      kit's record too (`rec`, kept by `mergeTool`), so the fold holds
      what the views of F3 will read while today's cards keep reading the
      old fields. The two outputs are one string: the rules differ only
      when one event carries a whole output and a delta (the old record:
      the whole one wins; the kit, as the template's backend and
      `sdk/acp`'s record: delta appended after it), and equal lengths mean
      equal texts. The template's `model/harness-heads.js`,
      `model/harness-ask.js` and `model/harness-signins.js` keep every
      export and become adapters from its rows (`acp:<kind>`, arguments
      with `summary`, the `acp` lift, the park) to the kit's record and
      blocks (`acpRecord`, `permBlock`, `askBlock`).
    - *Headline: one chain.* Label → a `description` argument → a shell
      command read by `describeCommand` (the Agent tab's) → the title's
      first line → a reading by kind (the template's `acpReading`) → name
      → id. The template gets it through `acpHeadline` (the fold passes
      the tool row's `acp`); a summary that is neither the lift's title
      nor the kind is taken as the label, so hand-made rows and rows from
      before the lift read as they did. **`/vendor/agent-tools.js`
      `headline` keeps its old chain** (no reading by kind:
      `{reading: false}`): the Swift port is held to that export and its
      parity corpus has no call the reading would change; the Agent tab's
      cards get the full chain when they move to the kit (F3), with the
      port.
    - *One `stripAnsi`.* There were three rules (the Agent tab's, the
      template's for a call's output, the template's for a CI log). The
      kit's is the richest escape rule of the three: CSI with any
      parameter byte, an OSC up to its own terminator, character-set
      designations, the 8-bit CSI. The Agent tab's old rule left
      `ESC[1;2:3m` and `ESC ( B` in the text and ate a hyperlink's text
      (and everything up to the last terminator). Three outputs of the
      parity corpus changed; `XbinAgent`'s `stripANSI` regex changed with
      them (`swift test` on Linux, 113 tests). The CI log's function is
      the kit's plus what a log adds (CR ends a line, control characters
      go).
    - *One form reader: the template's superset* (`anyOf` single choice,
      `enumNames`, defaults, "Other" answering a required choice) — with
      its bug fixed: a plain `enum` without `enumNames` was labelled
      "false" per choice (`(Array.isArray(names) && names[i]) ?? o`). The
      parity corpus found it; no parity output changed.
    - *One slash rule: the Agent tab's.* Prefix matches first, then names
      **or descriptions** containing the query, at most **8**; a draft is
      a command in progress while it is `/[\w:.-]*`. The template's was
      names only, at most 12, `/[^\s/]*`. Eight because the same number
      has to fit a phone's menu and a small tab (a host with room passes
      another `max`); descriptions because a person who types "/pull"
      means the command that shows pull request comments.
    - *`patchLines` is not deleted* (the plan had it go for `bx-code.js`
      `diffHTML`). `bx-code.js` is a lit element module with highlight.js
      behind a bare `lit` import: a tile page loads it lazily and, on a
      workspace whose import map has no `lit`, never — the template draws
      a patch plain until then. That plain drawing's line classes are the
      kit's `patchLines` now (one home, re-exported by the template).
    - *`commandOf`*: the Agent tab's reading (`command` string or argv,
      else `cmd`, else `script`; a string command's `args`) for both; the
      template read `command`/`cmd` only.
    - *The native view on an older xbind.* `native/*` imports the models,
      which now import the kit, and a static import cannot ask first. On
      an xbind without revision 2 the import is a 404, `native.js` fails
      to load, the runtime reports `{op: "error", kind: "module"}` — one
      of the app's fatal kinds — and the app shows the tile's web page,
      whose `boot.js` says "needs xbind ≥ v0.3.80". No guard was added to
      `native.js`: a guarded dynamic entry would be a second load path,
      and the failure is already whole (no tree, no request to the
      backend — `hack/agent-template-boot.test.mjs` plays both older
      xbinds with `hack/xbn/older-xbind.mjs`).
    - *Guards.* js-check's duplicate ban gains `mergeTool`, `stripAnsi`,
      `outputTail`, `describeCommand`, `unifiedDiff`, `joinPatches`,
      `patchLines`, `diffFiles`, `diffTotals`, `contentPreview`,
      `diffsPreview`, `filesStat`, `rawText` (tools.js) and `formFields`,
      `formContent`, `missingRequired`, `nativeSchema`, `nativeContent`,
      `cleanCode`, `slashQuery`, `matchCommands`, `commandHint`,
      `optionConfirm`, `modeConfirm`, `composerWords`, `steerTracker`
      (ask.js). `hack/chat-tools.test.mjs` and `hack/chat-ask.test.mjs`
      hold the kit and that the old modules' functions are the kit's own;
      `hack/chat-fixtures-agtt.test.mjs` holds that the kit says every
      call of the shared transcripts the same from either reducer's
      record. Fixture gaps closed: W1 (locations, files), W4 (a
      question's mode and url).
    - *Open after F2.* The Agent tab's cards still read the old record:
      the reading by kind, the `approval` state on a call's card (W2) and
      the confirmation of an `explicit` option arrive with its views
      (F3); `explicit` itself needs xbind's `permission.request` to carry
      it (W3, `sdk/acp`'s escalation rule). The fold's id-reuse rule is
      still per turn only (K6's other half is `ToolRecords`'). A whole
      output and a delta in one event: the kit follows the spec's
      reference (delta after the whole output); the Agent tab's old order
      (the whole output wins) may be the better reading — to settle with
      `sdk/acp`'s record, no fixture has one.
  - **F3 — the views, and the Agent tab on them; `KIT` 3 (2026-11-15).**
    `web/chat/views.js`, `cards.js`, `asks.js`, `css.js`, `composer.js`,
    `window.js`; `<bx-agent>` draws with them and nothing else.
    - *Template functions of `(block, ctx)`, as F0 measured.* Rows are
      `<div class="bxc-row" data-k>` holding a `guard`ed template; widgets
      with state of their own are elements, both light DOM
      (`<bx-chat-composer>`, `<bx-chat-form>`) so they wear the stylesheet
      of the root they sit in — a shadow root for the Agent tab, a
      document for a tile. The view modules import lit by URL
      (`/vendor/lit-all.min.js`, what every workspace's import map maps
      `lit` to): one copy with the shell, and they load on a page with no
      import map at all.
    - *The kit's look is the Agent tab's.* F3 changes nothing the tab
      drew except the table of gains, so where the two chats' cards
      differ (the status badge's words, a command under the headline, the
      template's outcome chips) the kit has the tab's; F4 decides each for
      the template (`plans/wsagent/chat-kit-spec.md` §11).
    - *The fold is not rewritten.* `agent-fold.js` keeps its blocks (the
      test API and the iOS port's parity corpus read them) and says each
      in the kit's model on demand: `blockOf(b)`, the same object while
      `b.v` holds, its `$memo` shared with `b` — so a streaming message's
      parsed parts outlive the mapping, and a row whose block did not
      change is not drawn. The fixtures' test adapter (`web.mjs`) became
      that code. The fold now ties a permission request to its call
      (`perm` on the tool block, the call's label on the request): the
      card says "needs approval" — gap W2 closed, the Agent tab has none
      left.
    - *`ChatCtx` grew by what the tab needed to stop reaching into its
      element*: `rev(block)` (the host's revision for a row — its folds, a
      diff that loaded — beside the block's `v` in the row's guard),
      `state(block)` (a bag for a widget's values: a form or a rejection's
      feedback survives the row leaving the window, as the tab's
      `_askVals` did), `readonly`, `role`, `copy`, `openDiff`, `signin`,
      `host`, `sessionRules` (xbind's "always" is its own session rule,
      offered only when the request can be scoped to one — the agent
      template's is the agent's own and always offered).
    - *The `host` block kind.* A card the kit frames and the host draws
      (`ctx.host`), for what only a host can say or may do: the workspace
      agent's cards for tile agent sessions it started, and a request its
      TRUSTED chrome must grant. The kit draws no answer for one, by
      construction: a page that merely renders a transcript cannot grant
      it. Rejected: a kind per case (`session`, `grant`) — each would be a
      special case in a module that must not know its hosts.
    - *Diffs: `bx-code.js` stays optional.* `cards.js` imports it on the
      first diff drawn and draws plain (`patchLines`, same line classes,
      styled under `.bxc-diff`) until it arrives; a host that has it hands
      it over (`useDiffHighlighter`; the Agent tab does, so nothing
      changes there). Markdown: the tab imports `bx-md.js` as before
      (highlighter up front).
    - *`ChatWindow`* is the bookkeeping the tab had around `ScrollWindow`
      (which rows, when to render or load more, what to let go, the pill),
      moved as it was. Where the template's `chat-window.js` differs —
      it returns after growing the window before letting pages go, keeps
      a floor of two pages under the unload margin, lets nothing go while
      a page is being read, and renders again for a trim when not
      following too — each is an option that defaults to the tab's rule
      (`growFirst`, `minMargin`, `holdWhileBusy`, `retrim`); F4 sets them
      and deletes the template's class.
    - *The gains, each with a check in the UI harness*: an `explicit`
      option confirmed in the window's dialog (`bx-frame`'s, passed as
      `ask`; Cancel sends no request); files in the message box; the
      usage badge; URL-mode questions, `anyOf`/`enumNames`/defaults and a
      viewer's and participant's cards (the kit drawn in the shell's
      document with a plain `ChatCtx`); the diff drawn plain on a page
      with no import map; harnesses not offered listed with their reason
      (`agentPolicy`).
    - *Two root causes while in that code.* (1) A session that is
      `starting` was not polled: its `idle` is one live event, the events
      hub drops a slow subscriber silently, and nothing later reveals the
      gap — the tab stayed on "starting". It is polled like a running
      turn now. (2) The `agentTab` pass kept the resumed tab's INDEX
      across several looks at the page; a listing in between re-sorts the
      tabs (ended ones last) and the index named the ended tab, whose
      status is never idle. It asks by id every time.
    - *A whole output and a delta in one event*: settled as the whole
      output, then the delta — `sdk/acp`'s record and the kit already
      agreed; `agent-tools.js` `foldTool` and the Swift port follow, and
      the shared transcript `output` (acptest's `recap`) pins it for Go,
      the template's backend and both JS reducers.
    - *Performance* (agentLongPerf, `AGENT_PERF=950`, CPU 4×, 8 runs on a
      machine at load ~25; F0's three runs in brackets): streamed 549–650
      ms, median 586 [550–616]; reload → bottom 1444–1882, median 1503
      [1432–1648]; a fresh tab 61–89, median 77 [65–76]; 30 more units
      127–211, median 136 [128–167]; held 134 blocks, 30 rendered, 200
      events [same]; heap 11.5 MiB [11.1–11.3 — the pre-F3 code measures
      11.5 on today's base too]. The pre-F3 code measured in the same
      session (the machine then at load 30–70): reload 1498–2768, a fresh
      tab 62–156, 30 more units 142–355.
  - **Decided by the owner (2026-11-14)**, of what the core left open:
    `people` stays default `admins`; binding `xbin:llm` stays a workspace
    admin's act; calls nobody acts in stay under the workspace total alone
    (usage lists them as their own row); a quota that warns raises an admin
    alert as one that stops does.
  - **The admin UI (package B1b).** One element, two homes:
    `<bx-llm-admin>` (`web/llm-admin.js` and its parts — `llm-upstreams`,
    `llm-quotas`, `llm-usage`) is xbind's own page **`/xbin/llm`**
    (`web/llm.html`, served as `/xbin/limits` is: no transform, top-level
    only, a CSP of `'self'`) and the admin console's **workspace → LLM**
    tab (`tabs/llm.js`, a sibling the entry imports relatively, passing
    `xbin.fetch`). The page exists whatever scaffold a workspace runs,
    which is what lets every alert name it. Words and arithmetic are
    `web/llm-kit.js` (pure, `hack/llm-kit.test.mjs`); **every call is in
    `web/llm-client.js`**, so a route the core moves is a one-file change.
    - **Locks are drawn, never inferred**: a control is disabled and
      marked "Set by your host" exactly when `locks` names its path (or
      `*`); the form never sends a locked row (`modelsChanges` skips them),
      and xbind's 409 is the authority either way. `lockAll` gets one note
      at the top instead of thirty badges being the only signal.
    - **A key is write-only in the UI too**: a masked field, sent once,
      gone from the DOM after the save; edit opens with an empty field
      ("unchanged"). The harness asserts the key is in no text, attribute
      or field value afterwards.
    - **Who sees what is xbind's answer, not the page's**: the page tries
      `GET /llm/config`; 403 switches it to `<bx-llm-me>` — the person's
      own quota and usage, from `GET /llm` and `GET /llm/usage`, which
      already answer only their own. `GET /llm`'s `admin` is not used for
      this: it is only set for a caller who is admitted.
    - **Server additions, kept to `internal/llm/alerts.go`**: (1)
      `Service.Alerts()`, chained into the broker's `AdminAlerts`
      (`boot/llm.go`): a reached quota per person — the verdict's limit,
      its source (their own, org `<id>`, the default), stop or warn, the
      reset — `crit` for a workspace quota that stops, one summary row
      past five people, computed from the meter's windows on each read and
      stored nowhere, so it ends with the window or a raise. (2) The setup
      alert `llm-setup` with `POST /llm/setup/dismiss`: shown only while
      nothing is configured, no admin has saved anything (`store.Set ==
      0`), the host doesn't lock all, and `data/llm/setup.json` is absent
      — the file's presence is the whole state, so the alert can't
      outlive a dismissal through a cache. (3) `GET /llm` answers a
      person's own `user` and `quota` when their direct calls aren't
      admitted: under the default people switch a member's calls all go
      through tiles, and their limits were otherwise unreadable to them.
    - **The setup card is the alert.** The console draws kind `llm-setup`
      as a card (Set up…, dismiss) instead of a row; an old console or
      shell shows the row, whose text names `/xbin/llm`. No second source
      of truth, and one dismissal covers every surface. It is `warn`
      because alerts have two levels and old shells style only those.
    - **An admin's shell gets the connector's alerts; found while
      building.** `AdminAlerts` never reached a shell: a shell reads
      `/alerts` with its frame token, which is no admin's (only an admin's
      own session, the owner token and the admin console are). So "an
      alert usable by old shells" needed a rule: the broker gains
      `AdminShellAlerts`, answered to a trusted chrome frame minted under a
      workspace admin's own login (`Broker.AdminShell` =
      `chromeFrameDriver` + `IsAdmin` of the driver — the rule D203's
      raises already use; such a document runs with the admin's cookie, so
      the token learns nothing the page couldn't fetch). `AdminShell` is
      not `IsAdmin`: that frame manages nothing, and of the connector's
      routes it may only POST the setup alert's dismissal (a shell posts
      an alert's `dismiss` with its own token). Only the connector's
      alerts go this way; the other admin-only alerts (Go build versions,
      backup keys, consent files, settings) stay where they were — moving
      them is a separate call.
    - **After the review's route changes** (merged while this was built):
      the core's own alert (kind `llm`, unpriced calls under cost quotas)
      rides the same chain — `internal/llm/adminalerts.go` holds this
      package's `AdminAlerts()`, which appends `Alerts()`. The page gained
      *Calls at once* (`concurrency`, lockable), a write's `notices` at
      the top, `~`/"estimated" and an "unknown models" row in usage (and a
      per-model table for admins). A moved address needs the key again
      **in the form**, before any write: the kit refuses the keyless move
      xbind would answer by dropping the key, so the notice is a backstop,
      not the way an admin learns it. A host's default is drawn as a
      ceiling over the per-person and per-org forms — said, shown in each
      field, a higher limit refused client-side (xbind accepts it and it
      gives nothing, which is the silent failure the form exists to
      prevent).
    - **Not done here**: a banner for the affected person in the shell
      (the alert chain has no per-person source; they read it on the page
      and in their call's refusal); live updates (the connector emits no
      event; the page reloads after its own writes and has a Refresh on
      usage); per-component quotas.

- **D226 — An LLM connector in the daemon: two config sources with a host
  lock, per-user metering and quotas (2026-11-14; issue #51).** The owner:
  "Connector built into xbind; Make sure it's configurable both from xbin
  admin UI and from xbin deployment config (so eg hosted instances would
  pre-populate the llm config and lock admin settings for the builtin
  connector in hosted instances)", and "for admin purposes might make sense
  to have some user-based metering/quota system in mind". Design:
  `plans/workspace-agent.md` §5.4; builder page `docs/llm.md`; routes
  `docs/protocol/llm.md`. This entry is the core, API and CLI; the admin
  console section, the hook-up of harnesses and terminals and an
  Anthropic-wire route are later packages.
  - **In the daemon, behind one interface.** `internal/llm` serves
    `/api/xbin/llm/v1/{chat/completions,responses,models}`: a streaming
    reverse proxy that holds nothing back, ends the upstream call when its
    client goes (after a grace, below), bounds a call by silence (10 minutes without headers
    or without a byte) and never by total time, caps a request at 32 MiB,
    and follows no redirect. Everything that touches an upstream is
    `llm.Connector` (three methods, a target resolved from configuration
    handed in per call), so the data plane can move to a sealed component
    without the rest moving; a call is a path, a body and a usage sink, so
    another wire is a route and a sink. It interprets no model output and
    logs no body and no key. Upstream URLs come from configuration only.
    The connector doesn't retry: clients do, and two retry layers multiply.
  - **Shared wire code**: `sdk/llm` (standard library only) holds the two
    wires' types, usage from a body or a stream, the model list, prices and
    the error shape. The connector is its first user; llm-gw, the agent
    template and `hack/fakeopenai` moved onto it next (2026-11-15), which
    added `ReadData` — a stream read one data line at a time, as the
    template always read one — and names for a chunk's choice and a usage
    block's details, so the types write the wire as well as read it.
  - **Two sources, the host's on top.** An admin's settings live in
    `data/llm/config.json` (the D204 pattern); the host's in the file
    `XBIN_LLM_CONFIG` names (JSON with comments; keys by `keyFile`). The
    host's values win setting by setting, and **what the file sets is
    locked**: reads carry `locks: {<path>: {locked: true, reason: "set by
    your host"}}` and writes naming a locked setting are 409 with the paths
    (a locked value sent back unchanged passes, so a client may PUT what it
    read). Granularity: `enabled`, `people`, each upstream, the default
    model, each role and alias, and each part of `quotas` the file
    mentions — an empty `"users": {}` locks per-user overrides with none.
    `lockAll` makes every setting the host's and takes the admin's own out
    of force. The admin's shadowed settings stay on disk: remove the file
    and they are back.
  - **The file is read at boot and on change.** An unreadable or invalid
    file stops the boot (a host must not run without the limits it wrote);
    afterwards its size and modification time, and its key files', are
    looked at when the connector is used (at most every 2 s — the ingress
    certificate's pattern, no watcher, no SIGHUP), and a change that is
    invalid is refused with the last good file in force. Its error goes to
    xbind's log; an admin's view says only that the host's configuration
    could not be read (the host's paths are not a workspace admin's to see).
  - **Keys in the vault; what happens while it is sealed.** Admin-set
    upstream keys are one map in `data/llm/keys.json`, sealed under a
    barrier subkey of their own (`llm/upstream-keys`) — not under
    `data/vault/`, whose files the vault API lists and migrates as tiles'.
    Sealed: an upstream whose key is in the vault answers 503
    `xbin_vault_sealed` naming the unseal, setting a key is 409, nothing
    decrypted outlives the seal (the cache drops on the first read after
    it); the host's upstreams keep working, their keys being files. Locked
    (production before a vault exists): the same, and a key is never
    written in the clear. Plaintext mode: in the clear, 0600, sealed by the
    first read once a barrier exists. No read ever answers a key: `{set,
    source, hint}`, the hint an admin key's last four characters.
  - **Callers.** (i) A person — session, app or device bearer, owner
    token — by a workspace switch, `people: admins | everyone | nobody`,
    default `admins`: adding a key must not, by itself, let every member
    spend on it. (ii) A component, by its binding: the connector is a
    builtin provider of the `openai` service, ref **`xbin:llm`**, so a tile
    binds its `llm` slot to it as to llm-gw (`XBIN_IFACE_LLM[_URL]` →
    `/api/xbin/llm`; `GET …/preferred` answers llm-gw's shape so the agent
    template resolves its default model unchanged). Element principals
    stay default-deny: no binding, no call, whoever drives the tile; the
    binding is asked per call; binding it is a workspace admin's act (an
    org admin's or an owner's would spend the workspace's quota), and
    pickers offer it only once an upstream exists. (iii) An LLM key
    (below). Attribution is from the verified credential only: the person
    of a partition's backend, the person driving a frame, terminal or
    agent session, `owner` for the root token, nobody for a backend that
    isn't a person's partition (metered to the tile; the workspace quota
    alone holds it).
  - **LLM keys** (after spike S4, `plans/wsagent/s4-connector.md` §3): a
    coding CLI must not hold the tile's API token — Codex writes its
    environment to `$HOME`, and a tool that ignores the base URL sends its
    key to the vendor. So a narrow credential, `xbl_…`, as `Authorization:
    Bearer` (what OpenCode and Codex send): good on `/api/xbin/llm/v1/`
    only, 401 anywhere else whatever rides beside it, resolved to a marker
    principal (`Via: "llm"`) no other route accepts. The store
    (`llm.Keys`: person, component, label, sandbox, expiry, revocation;
    hashes only, in memory) and the server's gate are here; minting — per
    terminal and agent session, and the SDK call for manager sandboxes —
    is the hook-up package's.
  - **Model names.** `<upstream>/<model>`, always prefixed in listings so
    adding a second upstream renames nothing. A request's model resolves:
    none or `xbin/default` → the default; `xbin/<role>` → the role's model
    (`agent`, `harness`, any an admin adds), else the default; an alias; a
    prefixed name; a bare name one upstream lists, or any name on a single
    passthrough upstream (the review, below, put aliases after what
    configuration names). **An unknown name is refused, not mapped to the
    default** — a client billed for a model it didn't ask for is a
    surprise only an admin may arrange, with the alias `*` (S4: Codex sends
    its own default model name). An upstream's `models` list is an
    allowlist; empty passes its `/models` and any name through.
  - **Metering.** Counters per UTC day, person, component and model —
    requests, failed, input/output/cached/reasoning tokens, cost, unpriced
    — in `data/llm/usage/<YYYY-MM>.json`, counted in memory (one lock, two
    map writes), written 10 s after the first unsaved count and at
    shutdown (the partition ledger's pattern; no new dependency),
    24 months kept, a file this xbind can't read never written over. Cost
    is the upstream's price table (USD per million tokens, cached input
    priced apart), else what the provider itself reported. A streamed chat
    completion is forced to `stream_options.include_usage`, or a client
    could opt out of being metered. A call abandoned mid-stream is not free (the
    review, below).
  - **Quotas.** Tokens and cost per UTC day and month; `stop` (429 in
    OpenAI's shape, `insufficient_quota`, naming the quota and its reset,
    with `Retry-After`) or `warn` (a header and the status). A person's
    quota, **each limit on its own**: their user override → else the most
    generous of their orgs' overrides (unlimited beats numbers) → else the
    default. Orgs add up as access does (a member of two orgs gets the
    better grant); the user override wins in either direction. `workspace`
    is a total over everyone and the one quota an admin can't lift for
    themselves once a host locks it. Checked before a call against what is
    metered, so calls in flight at a limit finish.
  - **Errors** on the OpenAI routes are OpenAI-shaped, the 401 of xbind's
    own auth gate on those routes included, so SDKs and CLIs show them. An
    upstream's refusal of the workspace's key is a 502 of the connector's,
    never the caller's 401 (a CLI would ask its person to sign in again);
    an upstream's other errors pass with their status, the key cut out of
    the body should it be echoed.
  - **Nothing changes unconfigured** (compat): the routes answer 503 "not
    configured", no picker lists `xbin:llm`, no file is written. Route
    classes: the calls are neutral (the handler reads the tile's edge
    policy for a non-primary deployment, below) and partition-scoped (a
    partition's credential is stamped with its person, which is the
    attribution); the settings are primary-only and refused to partitions.
    Model calls are left out of the audit log (metered instead; the audit
    writer would also have hidden the flush); settings changes are audited.
  - **Deviations from the plan, and why.** llm-gw is not an upstream yet:
    the connector would need a daemon principal a tile's policy admits — an
    identity rule that belongs with delegation's (B3), not here; the
    upstream `kind` and the plane's `Target` leave room for it. The seam
    for session credentials became a real key store and gate instead of an
    interface with a test double, because S4 landed its design while this
    was built and the gate (which route accepts which bearer) is the part
    that must not be improvised later.
  - **After an independent review (2026-11-14)** — what a caller, an admin
    or a deployment could do that it shouldn't:
    - *A caller-chosen model name made a usage row and a Warn line each.*
      Names are bounded (200 characters, no spaces); on a pass-through
      upstream a call is metered and logged under its model only when the
      upstream's own list (cached; fetched once when missing) has it, and
      as `<upstream>/(unknown)` otherwise, accepted or refused.
    - *A cancelled stream was free.* The upstream call now outlives its
      client by a grace (5 s, `XBIN_LLM_CANCEL_GRACE`) in which the answer
      is read for its usage; then it is cut. Without usage — also from a
      provider that reports none, or a stream that broke off — the call is
      charged an estimate, stated in the docs and marked `estimated`: a
      token per four request bytes in, a token per stream event (or four
      answer bytes) out. The grace is short on purpose: generation nobody
      reads is money too.
    - *A non-primary deployment called in full.* The binding to `xbin:llm`
      is an edge like a slot bound to a provider tile (role `writer`):
      read-clamped by default — models and status, no calls, as llm-gw's
      reader — or blocked by a tile manager; `docs/auth.md` and this entry
      now say the same. The builtin is an edge and never a grant role
      (`xbin:` is the governance namespace).
    - *An admin's alias could redirect a host-locked model.* Resolution
      puts everything configuration names — `<upstream>/<model>`, a listed
      bare name — before aliases, and such alias names are refused.
    - *A stored key followed a repointed base URL.* Changing scheme, host
      or port drops the key unless the request brings one. Private and
      loopback upstreams stay allowed (local model servers); the docs say
      adding upstreams is an admin's reach into the host's network, and
      that `lockAll` removes it.
    - *Lenient parsing.* `stream` is a boolean or 400; the upstream always
      gets a re-marshalled body (a duplicated member can't be read two
      ways); Responses `background: true` is refused until it can be
      metered.
    - *The upstream test* needs the connector on, stops at the quotas,
      sends one word for one token, and is metered to the admin running it.
    - *Resources.* A per-caller cap on calls in flight (`concurrency`, 16,
      lockable; 429 `xbin_llm_busy`) — which also bounds how far concurrent
      calls overshoot a quota; who is calling, the cap and the quotas are
      checked before the body is read; each write to the client has a
      deadline, so one that stops reading is treated as gone.
    - *LLM keys* are refused once their person is disabled, deleted or
      lost the tile they were minted for, and (minted under a binding) once
      the tile is unbound; sign-out-everywhere and unbinding revoke them.
      `bx llm` takes no key on the command line.
    - *Quotas (the owner's calls).* A host-set default is a ceiling: the
      admin's per-user and per-org entries narrow it and never widen it,
      with no empty `users` block needed; the host's own entries are the
      host's to give. A model without a price counts for nothing toward a
      cost limit: an upstream's `"*"` price is the default price, and
      without one the calls are `unpriced`, token limits still hold, and
      admins get one alert while cost limits are in force.
    - *Backups.* `data/llm` is daemon state: in no tile backup, untouched
      by a tile restore; restoring the workspace directory brings it back,
      the keys readable only under the barrier that sealed them.
  - **Part 2 — harnesses and terminals on the connector by default
    (2026-11-15; spike `plans/wsagent/s4-connector.md`).** The owner:
    "consider how we could by-default hook up tile agent (maybe also agtt
    acp) OpenCode to the builtin llm connector (right now OpenCode ships
    with default config which is really painful to connect to custom llm
    endpoint + tile terminals can't easily connect to llm connector bind";
    their choice, "Connector by default + sign-ins held by xbind", with a
    login the person already has still working. Builder page `docs/llm.md`
    §Coding agents and terminals.
    - *One table.* `sdk/acp`'s catalog says, per CLI, how it is pointed at
      an OpenAI-compatible endpoint from its environment alone
      (`Provider.Connector`, `ConnectorEnv`): the wire, **additive**
      (opencode: an inline config declares the provider beside the
      person's own, and its `model` member — what makes it the default —
      is rendered only when the connector serves) or **replacing** (codex:
      codex-acp's gateway auth request and `CODEX_CONFIG`'s model; claude:
      the gateway variables, waiting for an Anthropic-wire route; gemini:
      none), and the variables that must never hold the connector's key.
      xbind's sessions and the agent template render from it; goldens pin
      it. Rejected: the key in `OPENAI_API_KEY` for any harness (opencode
      turns it into its built-in OpenAI provider, pointed at OpenAI).
    - *Who serves a session.* The session's choice (`llm` on `POST
      /term/sessions`), then the person's saved choice, then the workspace's
      mode — one answer from the agents policy (D229's `AgentSessionLLM`,
      asked through `term.Manager.Credentials`): `own-first` (default),
      `connector-first`, and `connector-only`, which nothing overrides and
      which an org's model rule forces on its tiles. "The person has a
      sign-in" is a fact of the session's `$HOME`, which the daemon never
      reads (D78): it renders both outcomes into `_xbin/spawn`, and the
      agent host — in the sandbox, with the agent's own environment —
      settles `own-first` by the provider's login files and key variables
      and answers who serves in `_xbin/hello`. So opencode with no sign-in
      gets the connector's model and never, silently, its vendor's free
      third-party models.
    - *The credential is a per-session LLM key*, never the terminal token:
      minted in the open, revoked through the same revoke as the terminal
      token (close, crash, failed spawn, restart — whose session has a new
      one), ended at once when the person's own sign-in serves a replacing
      harness, and all of them at xbind's stop. An agent session's travels
      in the spawn frame: in the agent's environment only, not the
      sandbox's nor the terminals the agent opens. **Codex writes its
      environment to `~/.codex/shell_snapshots`** (seen live: the key was
      there, answered 200 while the session lived and 401 after). Decided:
      no scrub — xbind doesn't clean a person's home, the host is killed
      with the session so there is no reliable moment, and revocation is
      the control; the docs say so.
    - *The model rule holds on the key.* A key's grant carries its tile,
      and the connector's `CallPolicy` is the policy's `AgentCallAllows` by
      tile: a session on a tile whose orgs allow private models only can't
      call another, whatever its CLI asks; the models it is offered and
      the one it starts on are filtered the same way. xbind answers the
      policy's two questions of the connector (`AgentConnector`: can this
      harness run on it; `AgentModels`: is this model private).
    - *Terminals* get `XBIN_LLM_URL` and `XBIN_LLM_KEY`; `OPENAI_BASE_URL`
      and `OPENAI_API_KEY` only by a switch of their own, default off:
      everything started in a terminal inherits its environment, and a
      tool that reads the key but not the base URL sends it to OpenAI. The
      three switches (`sessions: {agents, terminals, openaiEnv}`) are the
      connector's settings, host-lockable. A person without the terminal
      API grant gets no key in a terminal.
    - *No key where it couldn't be held to the rules* (as first built;
      both reversed by the owner in part 3): a session with no network
      had no route to xbind, and a session that targets a non-primary
      deployment got none either — a key carried no deployment.
    - *What the connector refuses is said in the session.* Found live:
      with a daily quota reached opencode sat in "running" without a word,
      waiting out a `Retry-After` that ends at midnight. The minter of a
      key may watch it (`llm.WatchKey`); an agent session logs a notice
      with the status, code and message, once per kind while the CLI
      retries.
    - *Manager sandboxes: one door.* A sandbox a manager tile makes reaches
      nothing of xbind. Created with `net.llm` it gets one port of its own
      gateway forwarded to **the LLM-only listener** — a unix socket where
      xbind serves the three model routes to an LLM key and nothing else,
      outside its own handler. `relay.Config.Doors` is a new, separate
      field: `HostFwd` under a `Deny` stays refused (the guard that says so
      stands), a door is what an embedder opens on purpose. It needs no
      egress and works in both modes (proved in a namespace sandbox and a
      Firecracker VM). The contract gains the optional capability `llm`
      (`llm: true` on create, `llm.url` on the sandbox); a consumer reads
      hello first, so managers from before it are never sent the member.
      Rejected: a general route to xbind for sandboxes; relaying through
      the consumer over stdio (right for off-host managers, later).
    - *Two variables of xbind's may enter a tile sandbox.* The runtime
      refuses `XBIN_*` in a sandbox's environment ("never an xbin
      identity"), and the first end-to-end run of the template on the
      connector stopped there: the table names the address and the key
      `XBIN_LLM_URL`/`XBIN_LLM_KEY` (opencode's config reads the key by
      that name). Decided: exactly those two pass, in a *command's* `env`
      only — an LLM key opens the model routes and nothing else, so it is
      no identity; `defaults.env`, which is stored, still refuses them.
      The contract says a manager offering `llm` lets them through, and
      its suite checks it; the fake manager now refuses `XBIN_*` like the
      real one, which is what would have caught this before the live run.
    - *Not in xbind's logs.* A hook or a tool can print the environment:
      the agent's stderr is the session's text log (`GET …/log`), and the
      last lines of a session that dies at start go to xbind's own log.
      Both mask `xbl_…` by shape (found by the integration test's last
      check, with the scripted agent's `printenv`). The transcript is the
      person's own and is not rewritten; the key dies with the session.
    - *Keys for tiles.* `POST /api/xbin/llm/keys` (`xbin.LLMKeyFor`): a
      tile bound to `xbin:llm` gets a key for the person its credential
      acts for — exactly who its own calls are metered to — and nobody
      else; the key stands on the binding. Ended by id or by label
      (`DELETE /llm/keys?label=`), so a consumer keeps no ids: the agent
      template labels a key by its run and ends it before a restart, at
      the adapter's exit, and from a successor process.
    - *The agent template* starts a coding agent with no sign-in (none
      saved, none in the sandbox) on the connector when its `llm` slot is
      bound to `xbin:llm` and the sandbox has `llm.url`; the key is the
      generation's secret in its redactor, which also masks `xbl_…` by
      shape. (Its gate "a coding agent's sandbox needs an egress" went in
      part 3.)
    - **Part 3 — the review of part 2, and the owner's answers
      (2026-11-15).** An independent review found the rule and the keys
      softer than the text; the owner answered the open questions. What
      changed, and why it is so:
      - *`connector-only` fails closed.* A rule that the connector alone
        serves was a default with a 403 in front: a `Mint` error opened the
        session without it, a host that answered "own" only lost its key,
        and opencode kept every other provider. Now a session that can't
        be given the connector is refused; the host must answer
        "connector" (once: later frames change nothing) or the session is
        ended with the reason; opencode is rendered with
        `enabled_providers: ["xbin"]` and its small model
        (`ConnectorParams.Only` — the inline config is merged last, checked
        against opencode 1.18.32 with a sign-in in `auth.json`, a custom
        provider in the global config and a project config enabling
        others: the model list held `xbin/…` only and every other model
        was "not found"); and a model option a client sets must be one of
        the connector's (`Connector.AllowsOption`), at the start and later,
        for every harness with a connector entry. **Codex had the hole
        the reviewer suspected**, found with the real CLI: codex-acp
        applies the gateway auth request only where Codex asks for a
        sign-in, so with a `model_provider` of the person's own in
        `~/.codex/config.toml` it was never applied and the turn went to
        that provider (seen: no call reached the connector). The session's
        config now declares the connector as a model provider itself
        (`CODEX_CONFIG`: `model_provider`, `model_providers.custom-gateway`
        with `env_key`; `MODEL_PROVIDER`), whenever the connector serves —
        checked with no config, a personal `config.toml` and a project's,
        each naming another provider.
        *Inherent, and said in the docs:* the rule holds the session xbind
        starts and the calls through the connector. A person's plain
        terminal on the tile can run a CLI on their own sign-in (an org
        that must prevent it also sets the tile's `net`), and someone with
        a shell in the session's sandbox can change what its CLI reads —
        the connector-side call policy is the control that does not depend
        on the sandbox. Without the rule, a project's own
        `enabled_providers` that leaves `xbin` out keeps opencode off the
        connector even when the session chose it: xbind overrides that
        list only under the rule.
      - *The host's frames are the host's.* The agent host relayed
        everything the agent printed, `_xbin/hello` included — an agent
        could say who serves it. It now drops the agent's `_xbin/*`.
      - *Keys are a backend's to ask for, for a day.* A frame, terminal or
        agent-session credential could mint a 30-day key for its person:
        a token a browser holds became one that outlived it. `POST
        /llm/keys` now takes an instance (or partition) token only; a
        component's key lives 24 h and its backend renews it (`POST
        /llm/keys/{id}/renew`) — a key whose backend went away is dead in a
        day. xbind has no notion of "the driver of the request a backend
        is serving" yet, so a non-partitioned backend's key is still
        metered to the tile alone; that waits for delegation.
      - *The store is bounded.* 64 live keys per (component, person), 512
        per component, counted (not scanned), `429 xbin_llm_keys` at the
        cap — never an eviction of a key something runs on; expired keys
        go on a timer, not on the path of `Mint`; labels are bounded in
        the store, not only at the route.
      - *Keys carry their deployment.* A backend's key is held to the
        tile's edge policy for its deployment on every call (a policy
        change or a removed deployment takes it along), and by-label
        revocation is per deployment — two deployments both have a
        "harness run 7".
      - *Only plain model names reach a CLI's configuration*
        (`ValidConnectorModel`: no braces, quotes, spaces): an upstream's
        list could otherwise put `{env:…}`/`{file:…}` into opencode's
        config. Such a model stays callable; it is not offered to a CLI.
      - *The owner's answers.* (1) A `net: none` session gets the door:
        "its model only, no internet" is the default network for sessions
        the workspace agent starts. Its sandbox gets a relay whose policy
        is empty, with no forward to xbind's own listener, DNS refused,
        and one gateway port to the LLM-only listener — the same door a
        manager sandbox has. A person without `termApi` still gets no key
        in a terminal. (2) A session on a non-primary deployment gets a
        key bound to person + tile + deployment, metered to the person;
        the edge clamp stays on the deployment's own backend credentials.
        (3) The template starts a coding agent in a sandbox with no egress
        when the connector is reachable there — and there the connector
        serves whatever sign-in exists, or the agent doesn't start. (4)
        Codex's metadata warning stays: its config's one place for model
        metadata is `model_catalog_json`, a file in codex's own versioned
        schema that replaces its whole catalog — not something to write
        into a person's sandbox for a warning. (5) The launcher has "runs
        on": the person's saved `credentials` for the harness, shown only
        where both can serve and nothing forces one.
      - *The template.* No model for coding agents → the connector is not
        used and the conversation says why (opencode would otherwise run
        its own free models under a note saying "connector"). Ending a
        run's key is retried until xbind acknowledged it; a new start
        waits for the earlier key's end; a running generation's key is
        renewed every 6 h.
      - **Still open:** real opencode with a *project* `enabled_providers`
        list when the rule is not forced (above); a person-level
        attribution for non-partitioned backends' keys; the Anthropic wire
        for Claude Code.
- **D227 — Sealed components: code that is always the running release's
  (2026-11-14; issue #51).** The owner, on where the workspace agent runs:
  a sealed system component, as a general facility, with "a very clean
  auto-update story (eg code always = builtin template code)". **The
  full record** — each rule's reasoning, what was weighed and rejected,
  the owner's answers, four review rounds — is
  `plans/wsagent/s1-sealed.md` §Decision record (moved there verbatim on
  2026-11-15: this log is at its size guard). The rules:
  - **A compiled-in table declares them** (`internal/sealed`; empty in a
    release until `xbin/wsagent` is registered). Nothing in a workspace
    declares one or is read about one. The path is `xbin/<name>`: `xbin`
    is reserved, names are at most 18 characters, `xbin~*` is reserved,
    and a name may not shadow an xbind route or page.
  - **The code is the binary's**: rendered, hashed and materialized under
    `.xbin/sealed`, verified before every start, pages served from
    memory. Backends are prebuilt per release (`bin/sealed`, listed in
    `sealed.json`); only `--dev`/`--no-auth` compile.
  - **Holds, not fallbacks** (`no-build`, `foreign-data`, `newer-data`,
    `stamp`, `needs-sandboxes`), shown by `/xbin/system` and `bx system`.
  - **Its data key is its own**, claimed by a stamp with the data format;
    unclaimed data at the key holds it and is never read, moved or
    deleted; a newer format holds it (a downgrade).
  - **Never a target**: no grant, binding or route reaches it or a
    person's partition of it; only its instance credential acts as it;
    its page's frame token is a viewer's (its own backend and files,
    plus the exact GET paths its row names and, for a holder, the
    delegation API).
  - **What an admin may change**: who has read access (no level above
    read exists on it), enabled, limits, the bindings of its declared
    slots to providers the release names. Everything else is the table's.
  - **The boundary is where the work is done**, on the canonical path
    (`util.SealedPath`); the route table (`server/sealedclass.go`, pinned
    by a golden) is the early answer. One spelling: an odd one is refused.
  - **Restore is a write path**: only a sealed archive that names it,
    under a backup key this workspace created; never while held.
  - **Delegation** (D228, D232): holders, their notice path and the one
    row that is the agent xbin's chrome frames are the table's
    (`Capabilities.Delegate`, `Notify`, `Capabilities.Agent`). A person's
    agent does nothing in a sealed component; no preview shows one.
  - **Compat and words.** Listings leave sealed rows out unless asked
    (`?sealed=1`). "Sealed component" in code and builder docs; people
    read "system component", never beside the vault's sealed state.
  - **Not done here.** No entry point in the shell, the admin console or
    the app (the workspace agent's UI work adds them). The real-binary
    downgrade suite has nothing sealed to downgrade until a row exists.
- **D228 — Delegation: a sealed component acts as its person, under a cap,
  with xbind-verified approvals and admin change sets (2026-11-14; issue
  #51).** The workspace agent needs "the access of its user clicking
  through the UI". Spike S2 (`plans/wsagent/s2-delegation.md`) found the
  shape; the owner decided its nine questions the same day. Contract:
  `docs/auth.md` §Delegation, `docs/protocol/delegation.md`.
  - **Who**: only a sealed system component marked for it (one lookup, set
    at boot from the sealed-component table). Never a workspace's
    `xbin.json`, a grant or an approval.
  - **The credential** is the instance token of the person's partition
    under another scheme (`Authorization: XBin-Delegate`), honoured on the
    gateway socket and on `/api/` only; with isolation off (outside
    `--dev`/`--no-auth`) delegation refuses to run. It resolves to the
    person's principal (`Via: "delegate"`, `Agent`), **never an admin**.
  - **The cap** is a table naming every route xbind mounts (read, act,
    admin-read, never, refused), kept exhaustive by a test over the route
    inventory. Never delegated: answering a tile agent, a session's
    settings, terminals, deletes, code writes, the account and devices,
    other identities, the person's consents, tile stores, the admin API.
  - **Approvals are xbind's, not the agent's.** A side effect (a non-GET
    tile call, a session start, a tile create) needs a one-shot ticket the
    person approved — bound to method, path with query, and body — or a
    standing approval of a tile's API the person can list and revoke. A
    stolen token is read-only. Rejected: approval only in the engine (a
    token taken from the sandbox would then write).
  - **Tiles** see `From: user:<id>`, the person's level as the role
    (`reader`/`writer`), and a new `X-XBin-Agent`. `From` is not the
    component's path: the agent template in the wild treats another
    component as a tile manager. Consequence, stated in the changelog:
    `user:<id>` no longer implies an admin. A tile opts down with
    `"agent": "off" | "read" | "full"`; absent is full.
  - **Tile agents it starts** take mode and pickers from the ticket the
    person approved or from their preferences, on the default deployment;
    the agent drives only sessions it opened and never answers one. A
    reduced `$HOME` for such sessions waits for the sign-in store.
  - **Admin changes** are change sets: a closed list of kinds (grants,
    bindings, org shares, limits), each previewed by the handler's own
    checks without writing, approved only by the person whose agent
    proposed it after a step-up, applied in order as that person through
    the ordinary handlers; stale items are not applied; an undo set.
    An opted-in admin's agent also reads grants, bindings, orgs and limits.
  - **Org rules** live in one place, `Org.Agents` (another package);
    delegation asks through `delegation.Policy` and refuses without one.
    No new policy deny kind.
  - **Also**: per-person audit log, 13 months, every act and tile call and
    no listing read; the person's pause cuts running calls; 30 acts a
    minute; no grant or binding may target a sealed holder; each delegate
    tile call is counted in the partition's egress ledger and its edges
    count as approved for the consent prompt.
  - **After the security review (2026-11-15).** Nine findings, fixed on the
    branch, each with the case that passed before as a test. What changed
    in the design: (1) a session-start card shows the harness, mode and
    options the session *will* run with (history included), the ticket
    binds them, and an agent resumes only sessions it started; (2) change
    sets are closed by what a kind can *express*, not only by its name —
    nothing on a holder, no `xbin`/`cap:`/`gpu:`/`code` targets, no `@`
    slots, workspace default or host network, no share of a tile that
    holds such grants; a binding's line states its route and xbind writes
    PUBLIC / OPEN PORT / EGRESS warnings; an item whose warnings changed
    is stale; (3) the plane is primary-only; (4) `?xbin-partition=global`
    takes the same consent and ledger as the person's partition; (5) a
    standing approval is of one call (tile, method, path) by default, the
    whole tile a separate explicit scope — and the shipped infrastructure
    says `"agent"` (`off`; the agent template `read`, with a closed rule in
    its backend; apps say nothing — see below). The review's "the agent can answer a
    harness's permission through the template's API" did not reproduce:
    every route there needs the `admin` role and a delegate's call never
    carries it — an accident, so the explicit rule went in anyway; (6)
    the audit log rate-limits what isn't an act that ran, stores are
    per-person locked and bounded; (7) running calls are re-judged (3 s,
    and at once on a sweep), method-override headers are stripped, a
    ticket binds the content type and the tile; (8) the cap's classes are
    pinned by a golden file; (9) `Subject.Model` has one source.
    Rejected: letting an agent message or start conversations in the
    agent template under `"agent": "full"` — a message answers a parked
    question and a start picks a harness and mode; each needs its own
    design.
  - **Wired to the agent rules (2026-11-15, with D229 merged).** The
    delegation policy xbind runs with is `Broker.DelegationPolicy`: the
    orgs' rules (`AgentTileAllows`, `AgentSessionAllows`) and people's
    agent preferences (`agentprefs.Store.StartPrefs`), installed by boot;
    a server, broker or terminal manager given none still refuses
    everything, and `delegation.OpenPolicy` is the tests'. `GET /alerts`
    and `GET /screens` are delegated reads, filtered by the same
    `DelegateHides` as every listing. The routes D229 and D226 added are
    classed and none is in the cap: the agent rules and preferences
    (`PUT /agent/prefs` is what an agent may do unasked — the person's),
    the LLM connector (a component calls it as itself), and **the
    connection view** (`GET /agents/connection`) — its handler expects the
    workspace agent as a caller, but it names the tiles the rules block,
    which an agent's listings leave out; opening it is a decision of its
    own. Owner's calls: the agent template's closed rule stands for this
    release (messaging a template instance's conversation under tickets
    is a possible later addition); `"agent": "off"` for infrastructure
    (the gateways, sandboxes, ingress, bridges, and a new workspace's
    admin, organisations and tile-manager consoles), `read` for
    prometheus-viewer and the agent template, and **nothing** in the chat
    tile, the starter template and the examples — apps are what a
    workspace agent acts on, under tickets; the starter's `AGENTS.md`
    tells a builder when to narrow it and how to keep a route human-only.
    Kept for the sealed-components merge: under `--dev --no-auth` the
    holder's single global instance is the implicit owner's delegate
    (`TestDelegateNoAuthGlobalHolder`, skipped until then).
  - **After the second review (2026-11-15).** What changed in the design:
    a tile's agent level is xbind's record, raised only by a person who
    manages the tile (`data/agent-levels.json`; the manifest lowers, never
    raises; the consoles' level is the release's); a delegate's tile call
    forwards an allowlist of request headers, a ticket binds the whole
    `Content-Type`, a tile's redirect headers are dropped and `AsPerson()`
    follows none; a missing tile and an unreadable one are one refusal; a
    binary body is approved only as `unseen`; applying a change set takes
    an explicit non-empty list, its names are validated before they reach
    a line, and sets are capped (50 decided, 30 days, 256 KiB); every
    audit field is bounded and a bad line is skipped and counted; a
    delegate's session start is strict (`acp.Config.Strict`: the mode in
    force or no session, only a model or thinking level set) and a
    preference is its harness's; the cap's golden pins the audit flag and
    tile function and is not regenerated in CI; the agent template's rule
    lists reads too and covers the adapters, bus and mailbox. **A holder
    is never trusted chrome**: "the person's own session" is a request
    with the person's session and no component, which is exactly what
    chrome's documents send — `trustedChrome` is false for a holder,
    `PUT /chrome` refuses one, and boot stops on a conflict.
  - **Part C0 — what the agent's backend builds on (2026-11-15).**
    *Agent sessions*: a conversation is a handle xbind issues
    (`POST /delegate/sessions`: scope, model; per person and holder, 32,
    24 h idle). Every delegate call names it; the gate takes the model
    from it (the self-declared `X-XBin-Agent-Model` is gone), confines
    the call to its scope ("hidden by you"; a policy refusal keeps its
    reason) and taints it with the orgs of every tile a call touched,
    reads and listing rows included. The model changes only to one every
    tainted org allows (409 `model_taint`); the LLM connector holds the
    holder's own calls to the session's model (its `CallPolicy`).
    *Views*: the agent's connection view carries no blocked, hidden or
    no-access tile's name (it enters a model's context); the holder's
    page reads the full one, for its driver only. *Notices*: xbind tells
    the holder's backend that something changed — kinds and ids only,
    coalesced, at least once — over partition mail's delivery principal
    (no new kind of principal), on a path from the holder's table entry
    (`auth.SetDelegateNotify`). *Reads*: the docs searchable on `/api/`,
    a workspace map, the session directory's `origin=agent` filter.
    Rejected: a process-wide handle in the SDK (one backend serves many
    conversations: the handle rides the context); naming blocked tiles
    to the agent "so it can explain" (the reason and a count explain).
  - **After the final combined review (2026-11-15).** *Asks*: "asks
    before acting" is a property the catalog states per provider
    (`Provider.Asks`: its ask or plan mode) — "not bypass" was not it
    (accept-edits, auto). *Taint is about where text goes*: a brief to a
    tile agent goes to that session's model, so the handle's taint is
    asked against the SESSION's model source (`agentpolicy.Brief`): the
    tile's own layers hold it to the rule (the connector asks per call),
    or it is held to the connector on a model the rule allows; a session
    on a person's own sign-in is never vouched for, and a start (no
    session yet) passes only on a tile under the rule. Rejected: judging
    a start by the model its body names (the option isn't enforced by a
    strict start) and re-judging when the person later changes the
    session's model (their own act). *Harness commands*: every shipped
    adapter reads a leading `/` in prompt text as a command of its own
    (claude: `/model` and project commands with `allowed-tools`; codex:
    `/logout`, `/compact`; gemini: `/restore`, `/memory`, `/extensions`)
    — refused for the agent outright rather than ticketed: a person who
    wants one types it. *Mode drift*: the agent's messages go to a
    session whose current mode asks, or is the mode approved at its
    start (kept with the session, across a restart). *Off means off*:
    the connector's holder rule asks the gate; a stop voids unused
    tickets and takes back queued messages; standing approvals survive
    only a pause. *Having an agent is being given one*: the gate asks
    `users.Access.Given` — access without the admin role; the role keeps
    the system page. *Deleted accounts*: the per-user directory moves to
    `deleted/` on delete and carries the account's creation stamp
    (`Claim`), so a re-created id inherits nothing; kept aside for the
    log's retention, not destroyed. *Framing*: in the asset modes where
    tile documents share the workspace origin, `frame-ancestors` can't
    tell the shell from a tile (it matches by URL), so a holder's framed
    load must be `Sec-Fetch-Site: same-origin`; a browser without Fetch
    Metadata gets the CSP alone (other sites kept out, sibling tiles
    not) — accepted, as the legacy plane already rests on Fetch Metadata.
- **D229 — Orgs constrain agents by harness and model; scope and
  connection state; agent preferences on the server (2026-11-14; issue
  #51).** The owner: "Orgs define constraints; Workspace agents then get
  their calls filtered based on those constraints", a multi-select of orgs
  (unfolding to tiles) says what a session sees, and the UI "should very
  clearly indicate what orgs/tiles are hooked up and which are not (incl
  top level UI element showing eg aggregate numbers)"; from the issue:
  "internal-data org only allows private LLMs". This is the data and the
  API of `plans/workspace-agent.md` §5.3 and §5.6; the scope picker, the
  chip and the panel are the web and iOS packages'.
  - **Three rules, one shape, two levels.** `Org.Agents` (additive, in
    `data/users.json` like D203's `Org.Limits`): `workspaceAgent` on|off,
    `harnesses` any | a list of `sdk/acp` provider ids, `models` any |
    `private` | a list of LLM connector ids (an upstream, or
    `upstream/model`). The workspace has the same three as outer bounds
    (a top-level `agents` key beside the workspace policy rows, not
    `workspace-settings.json`: that store is booleans with their own
    protections, and the bounds are validated together with the orgs' rules
    under one lock), plus the credential default of S4 §1.5 (`own-first`,
    or `connector` forced). Stored only when it narrows something: with
    nothing set `users.json` is byte for byte what it was.
  - **Filtering follows the tile's org, on every call**
    (`internal/agentpolicy`, pure: `Check(layers, ask)`). A tile's layers
    are the workspace's bounds, the org that owns it, and every org it is
    shared into (an exact share or a pattern — `users.Store.TileOrgs`).
    Every layer must allow, so several orgs give the strictest and a tile
    in no org follows the bounds. A path inside a tile (a session opened
    on a sub-directory, a nested component) falls under the orgs of every
    path above it: stepping into a tile never steps out of its org's
    rules. A share is the tile's managers' act
    (D24), so the org a tile is shared into gaining a say over it is
    something its owner chose. The answer carries the reason a person
    reads ("Finance allows private models only"), the org and the rule.
  - **An org only narrows — at write time and at call time.** A list entry
    the bounds don't cover is refused when written, for a workspace admin
    too; "any" is always accepted (it adds no rule). Bounds tightened later
    rewrite nothing: each call is asked against both.
  - **Who sets what.** Workspace admins (and `xbin:users`): the bounds and
    any org. An org's admin: their own org, in their own session only —
    refused from a tile they drive (the frame-token rule), from a
    terminal's or an agent session's token, and in view-as. D203 kept
    ceilings to workspace admins because a ceiling is a budget; these
    rules are the org's own data policy, so its admins hold them, and the
    bounds are where a workspace pins what no org may loosen. The stricter
    "own session" rule is new here: a coding agent running on an org
    admin's token must not widen the rules it runs under.
  - **`private` is the connector's word.** `Broker.AgentModels` (wired by
    the LLM connector) says whether a model's upstream is private; without
    it, and for a model it doesn't know, nothing is private. No route takes
    the caller's word for it.
  - **The connection view** (`GET /agents/connection?model=&orgs=&tiles=
    &whatIf=`): every org the person has to do with and every tile they
    can read as connected | hidden (by their scope) | blocked (reason, org,
    rule) | no-access (the scope names what they can't see — or what
    doesn't exist: not told apart), with the totals and the sentence for
    the chip, and what a model change would connect and disconnect.
    Blocked wins over hidden: selecting it would not connect it. The scope
    is the caller's parameter — the session that holds it is the agent
    component's — and takes `workspace` for the tiles in no org (a
    reserved org id). The handler answers whoever its credential names as
    a person, so a delegate gets its person's view with no code of its own.
  - **The harness list also governs sessions people start themselves**
    (`term.Manager.AgentGate`, the broker's `AgentHarnessGate`):
    `GET /agent/providers?cwd=` offers only what the tile allows, and
    `POST /term/sessions` refuses the rest with the reason. Nothing running
    is ever ended: a session keeps going, a restart of it is refused before
    it would end it, and a resume of a past session of a disallowed harness
    is refused with its transcript still readable. Without `cwd` the
    catalog is whole, so the shipped app and older frames lose nothing and
    learn the reason from the refusal. Not a sandbox boundary: a terminal
    on the tile can still run any CLI.
  - **Agent preferences on the server** (`internal/agentprefs`,
    `data/agent-prefs/<user>.json`, `GET`/`PUT /agent/prefs`, a merge
    patch): the default harness and the launcher's last choice; per
    harness the mode, model, options and the credential choice; and what
    the person's workspace agent may start a session with — `start` ask
    (default) or allow, a mode per harness, the network and tile-API
    pickers. A bypass mode is never stored (Explicit modes are per
    session, D74), and `agentSessions.modes` takes only modes the catalog
    knows keep asking — checked again on every read (`AgentStart`), so the
    file is never trusted. The routes are `PersonOnly` and refuse terminal
    and agent tokens too: what an agent may do without asking is not the
    agent's to write. The file records whose it is (the account's creation
    stamp): a user id given to a new person keeps the conveniences, like
    the home directory, and resets `agentSessions`. The launcher reads and
    writes them, with its `localStorage` value as the migration source and
    as the fallback against an xbind without the route or a credential the
    route refuses (the root token, `--no-auth`).
  - **`origin`** on a session row and its history entry: `"agent"` for
    one a workspace agent opened, absent otherwise; the session keeps the
    component that opened it. The field, its JSON and the lines that carry
    it through `internal/term` are the delegation package's, word for
    word, so the two merge; setting it at open is that package's alone,
    and until it lands every row reads as its person's own.
  - **A session opens from outside its tile's window.** `<bx-frame>`
    `open({session})` shows a live session's tab, or a past one's
    transcript; `xbin:open-session {tile, session}` asks the shell for it
    from a tile's page; the D220 link gains `&session=<id>`. A shell that
    doesn't answer the request (an older scaffold) gets the link, which it
    reads as a plain link to the tile.
  - **Round 2 (2026-11-14), the owner's answers.**
    - *A workspace admin pins an org's rules* (`Org.AgentsPin`, `PUT
      /orgs/{org}/agents/pin`; the pattern of D203's ceiling, turned
      around: a floor of strictness). The first round let an org's admin
      put back anything a workspace admin had set on their org; only the
      bounds held. Pin and own rules are two layers asked on every call,
      so narrowing below the pin needs no rule and loosening it is
      impossible; a write past it is also refused, for the message. The
      answer names it: "(pinned by a workspace admin)".
    - *A share brings a tile under the org's rules, patterns included*
      (kept), and the reason names the share — "this tile is under
      Finance by its share apps/*" — so a surprising `*` can be traced
      (`TileOrgVia`, `Layer.Via`).
    - *Three credential modes*: `own-first`, `connector-first` (the
      connector unless the person chose their own sign-in for the harness
      or the session), `connector-only`. The earlier `connector` reads as
      `connector-only`.
    - *The model rule binds tile agent sessions* (the issue: "internal-data
      org only allows private LLMs … at harness/model level"). A harness
      on a person's own subscription sends the tile's code to a model
      xbind can't verify, so wherever a layer's `models` rule is not `any`
      the session is connector-only whatever the mode and the person's
      choice, and the connector asks the rule for every call
      (`Broker.AgentCallAllows`, its `CallPolicy`). One function answers
      the hook-up: `Broker.AgentSessionLLM(user, tile, harness)
      agentpolicy.SessionLLM` — `Mode`, `Use` (the saved choice applied;
      `Choose(session)` for a session's), `Forced`, `Reason`,
      `ModelAllowed(ModelRef)`. A harness that can't run on the connector
      (`Broker.AgentConnector`, the connector's to answer; unset: there is
      none) isn't offered there: `AgentHarnessGate`, so providers, create,
      resume and restart, and `unavailable` in the connection view and in
      `GET /agent/providers?cwd=&all=1`. A plain terminal is not governed
      by either rule, and the docs say so.
    - *The editor is xbind's* (`/vendor/org-agents.js`, as
      `<bx-org-limits>` is), used by the admin console and — through a new
      sibling module — the organisations tile, so an org's admin has a UI.
  - **Not done.** The scope picker, chip and panel (web, iOS); the
    delegation package's `Policy` answered by `Broker.AgentTileAllows`,
    `AgentSessionAllows` and `agentprefs.Store.StartPrefs` (written for
    it; wired where the two meet); the connector's side of
    `AgentSessionLLM`, `AgentCallAllows` and `AgentConnector` (the hook-up
    package wires them); a session's `llm` choice and the
    connector's variables (the connector's); per-model rules inside one
    upstream beyond the allowlist; the admin console's editor reaches an
    old scaffold only after `bx builtin update` (`bx org agents` and the
    API reach it now).

- **D231 — The workspace agent: one long-lived coordinating agent per
  person, built from the agent template's engine (2026-11-15; issue
  #51).** The owner: one persistent agent per person that works at a high
  level across the workspace — plans, delegates to tile agents, calls
  tiles' APIs as the person, keeps a notebook, reports — never reads or
  writes code itself, never answers a tile agent's permission or question,
  and asks before every side effect, with xbind, not the agent, keeping
  the approval; for "a fairly broad audience", so it must be clear about
  what it can't do and steer toward outcome-level requests. This entry is
  its backend (`plans/workspace-agent.md` §4, §5.1, §5.2, §5.6, §5.10;
  package C2); the chat UI is D and E.
  - **A mode of the template's backend, not a fork of it.** xbind starts
    the sealed component `xbin/wsagent` with `XBIN_AGENT_MODE=workspace`
    (`ws_mode.go`; an unknown value refuses to serve, like an unknown
    partition). In that mode the process has one class (the fixed
    `workspace` class: the subagent toolset only), one kind of
    conversation (`startRunTx` refuses every origin but the profile's),
    and a closed route list (`wsMay`; `wsGate` wraps the mux, so a route
    added to the template later is 404 here by default — the same shape as
    `agentMay` for calls *into* the template). Outside the mode nothing is
    registered or reachable. Rejected: a build tag or a second `main` (two
    binaries to prebuild and to keep honest); a class the admin could edit
    (the lane is the security boundary of a component that holds a
    person's access).
  - **A profile of the coordinator kit** (D221–D223's lineage). The
    `workspace` slot is filled: `Is` (a session row, in workspace mode),
    `Resolve` (the person's own session), `Pending`/`Deliver` (requests
    whose outcome is owed, tile agents that changed), `Prompt`, and two
    hooks the kit gained: `System` (the base prompt as the running build
    words it — Main outlives releases, and a stored prompt would freeze
    it) and `Specs` (the profile's wording of a core tool: `ask_user` with
    choices). The two tests that asserted an empty slot now assert a dead
    one outside its mode.
  - **A request is pending, never a wait** (owner, 2026-11-15, after the
    first build). The first build parked the tool call on its request —
    the result or the refusal "comes back as the tool result" — which
    put the conversation in `waiting_input`, where the kit wakes no
    coordinator: a person away for hours with an approval open heard
    nothing of three tile agents finishing. So the call isn't parked
    (`coord_park.go` and its hooks are gone): the tool answers "pending"
    at once, the turn goes on, and the outcome is a workspace update, as a
    Projects coordinator continues past a task that waits for a person. A
    decision is its person's act, so it wakes the conversation at once
    (the coalescing window is reset for it; the window is for tile
    agents' chatter). A session with requests open says so in its
    `waiting` line; its status is the conversation's own.
  - **xbind behind one interface** (`wsXbin`: sessions, looking, one call
    asked or made, tile agents, change sets, previews). The delegation
    package is still moving; `ws_xbin_http.go` is the only file that knows
    a route or a shape, the tests answer from `fakeXbin`, and
    `TestWsHTTPShapes` pins what the HTTP side assumes against a stand-in
    on a gateway socket, in the shapes C0 merged (`wsagent/main`
    00a64588): the handle rides the context (`xbin.InAgentSession`),
    `no-session` reopens it, `hidden` is the person's scope, the
    connection view the backend gets is counts and one row an org (it
    never holds a blocked tile's name), a what-if is counts and
    `allowed`. The start (owner, 2026-11-15): the brief rides the
    ticketed start as `prompt` — bound into the ticket, shown by xbind as
    what the session is told first, sent by xbind; the backend sends no
    first message. The harness, mode and network are xbind's to resolve
    from the person's preferences and the tile's org's rules: the backend
    picks nothing, reads `GET /delegate/start-defaults?tile=` for the
    brief's card, and passes a harness only when the person picked one of
    the allowed ones there (`/ws/prefs.harness` decides nothing).
    `…/allows` and `…/taint` are acts at xbind (30 a minute): one
    `allows` covers every org on any stamp, asked when an answer is
    missing or five minutes old; a taint is sent only for orgs the
    session doesn't hold yet. An end by restart is read from the compact
    session read with `ids` — only for sessions this agent started, since
    xbind answers "restart" for an id it never knew.
  - **Notices are hints, and none is relied on.** A notice
    (`xbin.ReadHolderNotice`, on `POST /delegate/notify`) asks for a
    reconcile — one worker, dirty-flagged, so a storm is two passes — that
    reads every open request, change set and live tile agent through the
    API; what the notice lists is not used. A request is claimed
    (`pending → running` in one statement) before its ticket is used, so
    two passes or a pass and a click never use it twice; a claim that
    outlived a crash asks xbind whether the ticket was spent. xbind's
    expiry notices don't survive its restart and a session a restart
    ended looks like any other end, so everything open is read again at
    start-up and, while anything is open, by a one-shot timer (45 s). A
    tile agent xbind marks as ended by its restart is reported as cut
    off, not finished, with the offer to continue it (`tile_agent_start
    {resume}`, which asks the person). No ticker.
  - **Sessions own handles; taint is checked twice.** Main is made on
    first use and can't be archived or deleted. One model serves a
    session's turns, summaries and titles (the connector takes only the
    handle's, and checks it against the handle's taint). A model change
    is xbind's to refuse; before asking, the orgs this backend last saw
    the conversation hold are compared with the handle's — a handle xbind
    made again (they go after a day unused) has lost its record, the map
    is read once to rebuild it, and what still can't be vouched for
    refuses the move. A refusal carries the new-session offer.
  - **Tile agents: scope, then hand off.** `tile_agent_brief` writes the
    brief (goal, done-when, leave-alone, check-by, what is known) the
    person can edit; `tile_agent_start` asks, and what is approved is
    what is sent (an edit withdraws and refiles). The start names no
    harness, mode or network. Where the person's preference lets starts
    through without xbind asking, the click on the brief is the
    confirmation and nothing starts before it — the brief is then the only
    thing the person sees (owner: yes). The approval covers the brief;
    what the agent messages the tile agent later is not approved message
    by message — the session's asking mode is the guard — and the prompt,
    the tools and the docs say so. A message to a session that waits for its
    person is held, in order (`ws_held`), and delivered when they
    answered; the agent has no tool, and no route, that answers.
    `tile_agent_result` is the final answer, a stat of files and the
    commands run — the log's patches are never copied.
  - **Research reads through xbind.** A `codeReader` kind `tile` over
    `/code/tree` and `/code/file`: no sandbox, no git, bounded (60 files,
    3 MiB, 400 lines a read, 40 files a grep). The subagent gets the file
    tools only. Its answer can be kept (`save_as`) as a notebook page
    stamped with the tile and the revision read — xbind's when it names
    one, else a fingerprint of the file list.
  - **The notebook is the knowledge store with org stamps, and no page
    is exempt** (owner, 2026-11-15). Owner kind `person-notebook`;
    `ws_pages` holds each page's stamp. A page the agent writes is stamped
    with every org its session is tainted with at that moment (xbind's
    taint, read fresh; broad by design — a row of the map counts), added
    to what it had. A session reads, searches and is shown a page only
    when its *model* is allowed by every stamped org (`…/allows`, kept on
    the session, asked again when stale or when a stamp names an org it
    has no answer for); reading one, or being shown it in the prompt,
    adds its stamps to the session's taint first (`…/taint`; if that
    fails the page isn't shown). The person's route keeps a page's stamps
    on an edit and is the only place that clears them. The prompt carries
    a *generated* list (slug, title, one line) of the readable pages and
    the page `preferences` when it is one of them — not a model-written
    index. The wiki's own tools are not offered: they know no rule.
    What it costs, measured in the fake workspace (two orgs, one
    private-only): stamps by themselves lock nothing — pages written on
    the hosted model after a map read (stamped sales, workspace) are
    readable in every session; every page written in a session on the
    private model after its first map read carries the private-only org
    and is closed to every hosted-model session, a preferences note
    told there included; and a new private session is tainted with that
    org by its first prompt (the list shows it such pages). So the
    notebook splits by model class, in one direction.
  - **The prompt** is goals and reasons, not procedure (`ws_prompt.go`,
    about 8 KiB, 13 KiB with the map): what needs approval and what doesn't, risk by
    reversibility and who is affected, the three nudges (vague →
    choices, low-level → redirect, impossible → the nearest thing), briefs,
    "a report to check, not proof", data is not instructions, notebook
    hygiene, reports for a reader who saw none of the work. The stable
    part comes first; the session's connection, plan and notebook list
    follow it. The map of xbin is generated from the docs by
    `hack/wsagent-model.sh` (passages listed in
    `hack/wsagent-model/main.go`, 6 KiB budget) into `ws_model.md`, and
    `TestModelIsCurrent` fails when a quoted passage changed and the file
    wasn't regenerated. It compares the output, not a hash of the pages:
    an edit elsewhere in a page costs nobody a regeneration.
  - **Evaluation.** Fourteen scripted scenarios (`ws_eval_test.go`; the
    fields' own tests are `ws_fields_test.go`) run
    in the template's suite — two came with the follow-up: pending
    requests interleaving with a finishing tile agent, and an end by
    restart offered for resume. They check what a scripted model can't
    fake — what was offered, told, filed, held, framed. Writing them found
    three defects: a notebook page could close the prompt's notebook
    frame, two messages to a tile agent could swap (the engine's parallel
    batch; a workspace session now runs a step's calls in order), and a
    wake row the first build's park left behind when a pass raced its
    commit. What a real model does is twenty-one scenarios for a manual
    run (`testdata/ws-real-model-scenarios.md`); no provider was called.
  - **Fields beside the words.** The page draws from fields, the model
    reads sentences, and neither is parsed out of the other
    (`ws_context.go`): a message's `context` (the tile or screen the
    person attached; the tile's name and whether the session is connected
    are xbind's answer, and the model reads one labelled line before their
    words, the stored message staying theirs); a request's outcome
    (`grant` once | call | tile and until when, `by`, `via`,
    `resultStatus`, `result`, `expiresMs`); a refusal as a card of its own
    with a `kind` and xbind's sentence (`ws_refusals`); `ask_user`'s
    `kind` (choices | redirect, with its tile | cant); and on
    `/ws/state` the stopping `quota` (from the connector's 429), `pausedAt`
    and each model's `private` and `where`. All additive; no sentence the
    model is told changed. *Rejected*: deriving the cards from the tool
    results' wording (the page did, and every reword would break it);
    asking the model to restate a refusal (it is xbind's fact, not the
    model's). *Open*: the connector's model list carries no `private` per
    model, so only a model one of the person's sessions is on is marked
    (from xbind's connection view); a tile outside the map is `hidden` or
    `policy` only where xbind's dry run says so, else `not-connected`.
  - **Not here.** The page (D, E) and anything in the template's
    frontend; attachments (the class has no file tools: a file a person
    attaches would be unreadable — open); a preview's picture (the tool gives the model
    the page's text and says a picture exists: a session runs without
    vision); resuming
    tile agents by itself after an xbind update (it is told they stopped
    and may ask to continue one, which asks the person); the reduced
    `$HOME` for started sessions (5.7).
  - **Registered: `xbin/wsagent` (2026-11-15; on D227 — the detailed
    record is `plans/wsagent/registration.md`).**
    - *The row.* The embedded `builtin-templates/agent` tree in workspace
      mode (`XBIN_AGENT_MODE=workspace`), notices at `/delegate/notify`,
      `Capabilities{Delegate, Agent}`, the `llm` slot on `xbin:llm` and
      nothing else, the connection view as its page's one extra read,
      `cap:open-links`. Pinned by `TestTheReleasesTable`.
    - *One backend per person, no shared instance, for good.* A person's
      agent is their partition; a shared instance would hold delegation
      for nobody. The mode is fixed for the life of the entry: the data
      stamp records it and a table that says otherwise holds the
      component (`partition-mode`) instead of switching, which would
      delete its data. (The template read its brake from the shared
      instance's `conf`; a workspace agent reads none.)
    - *Nobody has a sealed component until it is given to them.* An
      access entry reaches one only by naming it; `*` and other patterns
      were written about tiles. Admins read it. This replaces D227's
      "a pattern that reaches it is clamped to read": with it, people who
      hold `*` would all have had an agent the moment the switch went on.
    - *Three gates, each xbind's:* the connector (the slot's default
      applies only while it is configured), the delegation switch (off by
      default), access. A workspace that does nothing sees nothing new.
      Who gets one is that access: `PUT /delegate/workspace {who:
      everyone | me | chosen}` writes it where access is kept, and the
      chrome's third setup step is those two buttons.
    - *Limits below a tile's.* A row may ask for less than a tile's
      defaults (`sealed.Limits`). The agent's: 512 MiB a backend (a tile:
      2 GiB; measured 57–63 MiB idle, 74 MiB after 216 tool calls and a
      compaction) and an idle stop of three minutes (ten; a stopped
      partition answers again in 0.1–0.4 s). The running cap stays the
      default. An admin raises from there as for a tile.
    - *The build.* Releases carry `bin/sealed/wsagent-<digest>` (about 45
      MB, 16 MB in the bundle); `make build` needs the template's Go
      modules, `make check` builds no sealed backend. A development xbind
      compiles it on first use.
- **D232 — The workspace agent's chrome on the web: grants are drawn and
  sent by xbin, never by the agent's page (2026-11-15; issue #51;
  `plans/wsagent/ux.md`).** The agent's conversation renders what a model
  wrote; a grant drawn there could be drawn by the model.
  - **The rule.** A request, a standing approval, a change set, resume and
    the admin opt-in are decided in a document that holds the person's
    session — the shell, `/xbin/agent` — by `web/bx-agent-dock.js` and its
    siblings. The agent's page sits below the dock's header in a sandboxed
    frame on its tile credential (a holder is never chrome, D228) and may
    post `xbin:agent-open-request {id}`, `xbin:agent-open-changeset {id}`,
    `xbin:open-session {session, tile}`; the dock takes messages only from
    its own frame's window, reads ids and no other field
    (`agent-chrome-words.js` `pageMessage`), and draws what xbind answers
    for the id. The agent's sentence is quoted and labelled.
  - **Against a click the person didn't mean.** Allow is armed 600 ms
    after the sheet shows, only while nothing covers it (hit-tested
    through the shadow roots), only for a trusted event in a focused
    document; Enter never allows; Esc is "later". The wider grant (every
    change to a tile) has a second step whose primary button is Go back
    and a default duration of an hour; a danger-mode session start makes
    Deny primary. A warned change-set item starts unticked; a blocked one
    can't be ticked.
  - **One obvious place per choice**: `CHOICES` in
    `web/agent-chrome-words.js` (dock bounds 360–720 px, whole page under
    640 px, the arming delay, default durations, the shortcut Ctrl/⌘+J,
    no typing while paused) and the words beside it — the copy deck of
    ux.md §14, node-tested (`hack/agent-chrome.test.mjs`).
  - **`/xbin/agent`** follows the pages of D203/D226 (static, top-level
    only, the person's session) with one difference: its CSP has a
    `frame-src`, because it frames the agent's tile document.
  - **Old shells** get one alert a person (kind `agent-page`, dismissed
    with `POST /delegate/page/dismiss`, kept in the person's delegation
    state through `Store.SeePage`, which never replaces an unreadable
    file). It is a person's, not an admin's: the alert chain got its first
    per-person source (`Broker.alertPerson`: own session, or the trusted
    chrome frame under their login). A shell with the chip filters the
    kind out.
  - **Seams, one assignment each.** `Server.AgentHolder` (boot, from the
    sealed-component table): until it is set `GET /delegate/state` has no
    `agent`, the dock says the conversation arrives with an update, and
    the alert is sent to nobody. `state.held` (`locked` | `older`) is read
    by the chrome and not sent by any xbind yet.
  - **Tested by faking**: the `agentChrome` harness pass fakes
    `/api/xbin/delegate/…` by route interception (no holder can post a
    request on this tree) and refuses the frame's credential as xbind
    does. To re-run against a real holder: an approval reaching xbind and
    its ticket being used, live `delegate` events, the sign-in step, the
    holder's own frame and its messages.
  - **Not done here**: an "ask my agent about this tile" entry in the
    tile menu (the chip opens the dock about the tile in front; the dock
    takes `context`); a mode picker in Preferences (modes are shown, the
    start preference is set); naming the admins in "An admin can fix
    this" (no route lists admins to a non-admin); the setup's "Choose who
    gets one" step links the docs rather than an editor.
- **D233 — The workspace agent in the iOS app (2026-11-16; issue #51;
  `plans/wsagent/ux.md` §1, §6, §9, §13.4–13.5; package E1).** The first
  stage on the phone: the entry, the conversation, the native grants, the
  inbox. The Live Activity and its pushes, notification actions, dictation,
  Handoff and widgets are later stages; the seams are where the app already
  has them (the push kinds `agent.approval` / `agent.changeset` open the
  agent by its link today).
  - **Two layers, as on the web — and the app is the trusted one.** The
    conversation is the sealed component's own native view
    (`builtin-templates/agent/native/ws.js`, drawn by XbinRenderer like any
    native tile, on the tile's frame token in the hidden runtime document).
    Everything that grants is `App/Shell/Agent*.swift`, drawn from what
    xbind answers for an id with the device's session — which
    `docs/protocol/delegation.md` already names as the person's own ("a
    browser session, the app's session or a device's"); no xbind route was
    added for it. Rejected: the web page in a web view with the dock around
    it (the grant would be web chrome inside the app, and the phone's most
    important screen a page); grants as rev-1 `approval` nodes (a tile's
    tree would describe a grant).
  - **`request` and `session` are placed by id and drawn by the app.** The
    design's V1/V2 gave the tile a sentence on the request and the status
    and last line on the session; both were dropped: the node carries an id
    (and a hint — the kind, the tile), the app reads the rest from xbind
    (`GET /delegate/requests/<id>`, `/delegate/changesets/<id>`, the compact
    session directory). The renderer asks `XbinServices.trustedNode`, which
    only the agent's container supplies; with none it draws an inert line,
    so the nodes are harmless in any other tile and in previews.
  - **Only the holder may ask the chrome, and only to show.**
    `xbin.native.agent(msg)` is one new `call` of the bridge behind the
    wire feature `agent`, which the app lists in the caps of exactly one
    runtime: the component `agent` of `GET /delegate/state` names
    (`NativeTileRuntime.isAgent`). The messages are the web page's own
    (`xbin:agent-open-request {id}`, `-changeset {id}`, `xbin:open-session`,
    `xbin:agent-open {view}`), read as strictly as the dock reads them
    (`AgentBridge.decide` mirrors `pageMessage()`), and the other way is
    `xbn.agent(msg)` with `xbin:agent-context`. Rejected: a deep link the
    tile opens (it would need `cap:open-links` and leave the app's own
    routing); a `present` prop on the request node (a tree could then open
    a sheet by re-rendering).
  - **The vocabulary grew by what the conversation needs, additively**
    (transcript rev 3, question rev 2, plan rev 2, composer rev 2; `card`,
    `connection`; five icons). The scope panel, sessions and the notebook
    are the agent's own screens from existing primitives (Q9: scope is
    focus, not permission); a generic tri-state tree was not added — an
    org is a collapsible section whose first row is its tick. Dictation
    (`composer.dictation`) waits for its stage.
  - **The words are the web chrome's**, ported (`DelegationWords.swift`)
    and held to `web/agent-chrome-words.js` by generated vectors, so the
    dock and the app can't drift apart sentence by sentence. Rejected:
    running the JS in the app (JavaScriptCore for strings), and writing the
    app's own copy deck.
  - **ux.md §15, as built** (`AgentChoices`, one place): a sheet with
    medium and large detents, opened large, the button in the panel bar
    (Q1); no iPad sidebar — the same item opens a trailing column (Q2);
    nothing is allowed from the lock screen (Q3: no notification actions
    in this stage at all); Face ID for the wider grant and for applying
    admin changes, as a local gate that never stands in for the server's
    recent sign-in (Q4); both durations offered, "until I take it back"
    for the exact action and "for one hour" for the whole tile (Q5);
    answering a tile agent happens in its session (Q6); warned items start
    unticked (Q7); no composer while paused — the app draws Resume there
    (Q10).
  - **The container keeps the panels as they are.** A sheet never joins
    the panel stack; on a regular width `XbinTwoPanes` puts the agent
    beside the `PanelStack` without rebuilding it (the primary keeps its
    identity whether the column shows or not). On an open Duo the same
    view gives halves where the system draws its vertical bar. Rejected
    there: the system's `ArrangementView`, which would rebuild the panels
    each time the agent opens or closes — and which no simulator can show.
  - **Shipped apps and old workspaces see nothing.** The holder's row is
    read with `GET /components?sealed=1` by the agent's model alone and
    never enters the catalog; an xbind without `/delegate/state` yields no
    state, so no button; an app from before this ignores the new nodes
    because the only tile that renders them is one it never lists. The
    template's view falls back by `supports()` for an app that knows the
    agent but not a later revision.
  - **Not tested on hardware that doesn't exist**: the Duo layout compiles
    against the iOS 27.1 SDK and has never been drawn.
  - **After the security review's fixes on the web side (second round,
    2026-11-16).** The app takes the same rules, each held to the web's:
    only xbind's `asks: true` is "asks before acting" (the words module and
    its Swift port changed together — `asks !== false` had given a preview
    without the word the green tag); the agent's view may ask the chrome
    for views that change nothing, never Preferences (`AgentBridge.View` is
    `PAGE_VIEWS`, pinned by the vectors); every control that widens what
    the agent may do is armed like Allow (`ArmingClock` / `AgentWidening`,
    pure and tested in XbinCore — the web's WidenGuard as a rule, not a
    port of its DOM handling); a draft from the view is labelled as the
    page's suggestion until the person edits or sends it; an admin without
    an agent has none in the app either, and a `403 no-agent` on any read
    ends the reading. Home's agent row lost its count plate: the count is
    on Needs you and on the bar item (the owner's review of the shots, over
    ux.md §1.1).
  - **One xbind change came out of it**: the workspace's switch now sends
    people's own sockets the `delegate` state event (it told only the
    agents' backends), so the dock and the app follow it as it is turned.
    Additive; found by the UI test that turns the switch with the agent
    open. Rejected: a refresh on a timer in the app (the chrome polls
    nothing), and leaving it to the view's banner (the view heard nothing
    either until a message was refused).
  - **States a test xbind can't be in are Debug demos** (`-XbinAgentDemo`,
    after `-XbinPairingDemo`): the chrome's own reads answered from the
    vectors' corpus, generated into the app and pinned by `make js-test`,
    so a demo shows exactly what the words' tests pin. Live where the e2e
    xbind can make the state. Rejected: a second, slower xbind per state
    (a held vault, an older data stamp, a non-`--dev` xbind for the
    password step) for screens whose every word is already pinned.

- **D234 — Previews: xbind renders a tile or a screen as a person,
  read-only, in a confined browser (2026-11-15; issue #51).** The workspace
  agent has to see what its person sees, and a person (or `bx`) wants a
  picture of a tile without opening a browser. Spike S3
  (`plans/wsagent/s3-preview.md`) showed it costs about 1.5 s and 0.4 GB a
  render; this is the production form. Owner's calls are marked (O1–O8).
  - **The route.** `POST /api/xbin/preview {tile | screen, shell?, at?,
    viewport?, wait?, fullPage?}` → a PNG, a text rendering (an
    accessibility tree per frame), console errors, failed requests, the
    final URL, the browser's build and a fixed `notice`; `GET
    /api/xbin/preview` → availability and limits. Classes: primary-only for
    the POST (a preview shows what people see, so a non-primary
    deployment's credential gets 403), partition-neutral (it renders as the
    person behind the call).
  - **Who (O5, O8).** `previewPerson` (internal/server/preview.go) is the
    one function that decides: a person for themselves (browser session,
    the app's bearers), a person's terminal for its own tile only; never
    the root token, a tile, a view-as session. The delegate's case is added
    there when delegation merges. Admins who want someone else's view use
    view-as: a preview is never of another person.
  - **The session (O1).** A view-as session whose viewer is the reserved
    `~preview` (`auth.PreviewViewer`): `ReadOnly()` holds, so every write
    gate and every derived credential is read-only with no new claim in any
    wire form. `Principal.ViewAs()` separates "someone else is looking"
    from "read-only": privacy gates (a partitioned tile's document, its
    routing, its events, session history and names, personal binds) ask
    `ViewAs`, write gates keep `ReadOnly`, and every remaining direct read
    of `Impersonator` is classified act / carry / name in a test that fails
    on a new one. No account can take the id; the reserved viewer mints and
    redeems no view-as; a preview session is not listed, not persisted, has
    no "stop" and expires on its own.
  - **The transport.** One unix socket per render; xbind sets the session
    on its requests, by the cookie jar's rules (not to a tile origin, not
    on a request a sandboxed frame starts: `Sec-Fetch-Site`, or `Origin`
    on a WebSocket handshake). The socket refuses non-GET, terminals and
    sign-in routes whatever credential arrives, and any Host that is not
    the workspace's. Read-only therefore holds by the credential alone and
    by the transport alone (tested separately).
  - **Scope (O4).** A tile render's socket admits that tile's files and its
    frame's calls; a screen render's the shell, the person's own reads
    minus tile source, git, checkpoints, backups and the vault, and the
    tiles on that screen (resolved from the person's layout and the org
    screens shared with them, read as the shell reads them). A tile that
    loads another tile's files renders without them.
  - **Sockets (O2).** An upgrade by a preview principal reaches the backend
    at `reader` (the read clamp; a role it can't narrow is refused). Plain
    GETs keep their role, so the picture is true. View-as is unchanged.
  - **Partitions (O3).** A preview opens the person's own partition
    read-only and never makes one: a tile never opened answers 409.
  - **Tiles are told.** `X-XBin-Preview: 1` on every call
    (`xbin.Caller(r).Preview`), documented as the contract: a GET or a
    socket under it changes nothing. xbind can't enforce it inside a
    handler; the builtin tiles and templates were audited for it.
  - **Ready (O6).** `xbin.ready()` and `<meta name="xbin-ready"
    content="manual">`, additive. The render ends on the signal when every
    tile gave it, else when the network is quiet and frames are loaded; a
    declared tile is waited for about ten seconds.
  - **The sandbox.** `internal/preview` runs the rootfs's node and
    Chromium through `internal/confine`, unchanged but for a cgroup hook:
    an empty network namespace, one read-only bind (the driver and the
    socket), the answer over stdout only. Only the workspace's names
    resolve, to the socket; an https workspace gets TLS on the socket under
    a per-render key pinned by SPKI. No `--isolate` or no Chromium → 503,
    never a host run (D78).
  - **Limits (O7).** 1 per person, 2 per workspace, 45 s, 1 GiB:
    `XBIN_PREVIEW_PER_USER`, `_TOTAL`, `_TIMEOUT`, `_MEM`; `XBIN_PREVIEWS=off`.
    The memory cap is a cgroup leaf (killed at the cap, no `memory.high`)
    and needs delegation, as tile limits do.
  - **Chromium's own sandbox stays off.** Tried: it refuses uid 0, which a
    confined tool is in its user namespace, and an unprivileged process
    there can't write a nested namespace's uid map. Turning it on needs the
    sandbox init to exec a tool as a non-root uid of the range — not done.
    Mitigations: the scope, the empty netns, and the rootfs's Playwright
    now pinned (`PLAYWRIGHT_VERSION`, guarded by `make pins`). The residual
    risk is written down in docs/isolation.md §Previews: a page that owns
    the browser can read what the render shows and pass it to a backend it
    controls in a GET.
  - **Prompt injection.** The answer's `notice` says its content is data,
    not instructions; the agent's tool shows it with every result.
  - **Not done.** The shell still tries its saves under `readOnly` (they
    are refused; a failure toast can be in a picture); VM-backed sandboxes
    for the browser; a tile's non-primary deployment as a target; the
    delegate's route-class row (with delegation).
  - **After an independent review (same day).** What it found and what
    changed: (1) clamping only the role left backends that authorize on
    the person's level writable over a socket (the coding-sandbox's
    terminals) — in a preview every call, GET or upgrade, now carries role
    `reader` and user level `read`, `UserCanWrite()` is false, and the
    builtin tiles' sockets refuse a preview from any consumer; a custom
    role with no read form keeps its role on a GET and gets no socket.
    The cost is fidelity: a tile is drawn as its reader sees it. (2)
    xbind's own GETs with effects: a work-tree diff captured a checkpoint
    for any read-only session (fixed in a commit of its own, for master);
    the cross-tile branch could start a first partition and send a consent
    prompt; the model list dialled upstreams — all refused. (3) The answer
    is rebuilt in Go from a strict shape with byte caps; nothing the
    sandbox returns is passed through, the browser's name included. (4)
    The picture has a pixel budget checked from its header before
    decoding; the runner clips a taller page (`clipped`). (5) No
    enforceable memory cap → unavailable unless `XBIN_PREVIEWS=uncapped`;
    the sandbox's tmpfs mounts are sized in every case
    (`sandbox.Spec.TmpfsBytes`). (6) The person's plane on a screen render
    is an allow-list of the shell's reads, pinned by an end-to-end test
    through `leftOut`; an admin's render is a member's view (the
    workspace-wide lists answered empty, events refused, a `note`). (7)
    The socket honours only its own render's session, on every host. (8)
    `/whoami` no longer says `preview`; a tile must not draw differently
    for the header. The review also turned up, by its admin-screen test,
    that the first scope gate refused the shell's own frame token and so
    broke every admin's screen render.

- **D235 — The workspace agent's own page on the web: it shows, xbin's
  chrome grants (2026-11-15; issue #51; `plans/wsagent/ux.md`; package
  D2).** The chrome around it — the dock, the approval sheet, the change
  set, Needs you, pause — is D232. The conversation with a person's
  workspace agent is the agent template's page in workspace mode,
  `builtin-templates/agent/ws/`, framed by the dock.
  - **A page of its own, not a mode of agent.js.** `boot.js` chooses: on a
    sealed component's path (`xbin/<name>`, which no workspace can hold)
    it asks `GET /ws/state` after the kit check and loads `ws/page.js` when
    the mode is `workspace`. Rejected: a field on `GET /me` (the backend is
    another package's, and `/me` is read after agent.js has loaded half the
    app); asking every instance (one more call before every normal page —
    and `test/ci.mjs`, which takes the first call as "the app started",
    showed what that shifts); a branch inside agent.js (its static imports
    run the whole app first, and instances patch that file). The ws tree
    reuses the models (`model/session.js`, `model/fold.js`) and the
    template's chat window; it never imports agent.js's views, and a test
    pins that both ways.
  - **It grants nothing, by construction.** Every way out is a message
    to the chrome with ids only (`ws/dock.js`: `xbin:agent-open-request
    {id}`, `xbin:agent-open-changeset {id}`, `xbin:open-session {session,
    tile}`; in, `xbin:agent-context {tile | screen}` from the framing window
    only). The one exception is the backend's: where the person's
    preferences let a start through without a ticket (`start-defaults` →
    `ticket: false`), the brief's button reads Start and the click is the
    confirmation. Browser scripts assert that no Allow, Approve, Deny or
    Resume is ever drawn, whatever waits.
  - **Cards where the conversation made them, the window untouched.**
    `ws/place.js` maps the session's cards onto the fold's rows: a request
    in place of the call that filed it (by `callId`, else the call's own
    "Pending — request N"), a brief in place of `tile_agent_brief`, a tile
    agent after the request that started it, the plan once in place of its
    first call, an `ask_user` with `choices` as the kit's form; else after
    the line before it in time. A row may draw several cards or none, so
    the session's paging and the kit's window keep their indices.
    Rejected: a second list of blocks for the window (its `keep()` cuts by
    the fold's indices). Every JSON field it reads is pinned by a node
    test against the backend's Go source.
  - **Fields, never the model's sentences** (follow-up round, after the
    backend's a1c7c312). The context chip goes as the message route's
    `context: {tile} | {screen}`; requests settle from `by`, `grant`,
    `grantUntilMs`, `via`, `resultStatus`, `result`, `expiresMs`; refusals
    are `cards.refusals[]` by kind, each with the copy deck's words and
    ways forward; a question's `kind` decides its card — a redirect offers
    "Hand this to <tile>'s own agent" (`xbin:open-session {session: "new",
    tile, draft}`, the draft the person's own words) and "Tell me the
    outcome instead"; the quota banner and pause time are `state.quota`
    and `delegation.pausedAt`. The first version drew from the backend's
    prose (a first-line prefix for the context, outcome sentences, refusal
    phrases) because the fields didn't exist; that is gone. Two readings
    remain, pinned by the test: a request and a brief find their tool call
    by the call's answer ("Pending — request N", "Brief N for"), since
    `requests[].callId` is no longer set and briefs carry none.
  - **Kit, generic** (KIT 5): a plan's title, meta, note, numbering and a
    step's tag; a question's button words; the composer's accessible name.
    The page needs `NEEDS.ws` = 5; the normal page still needs 4.
  - **The recommendations of ux.md §15 that the page follows**, each in
    `CHOICES` (`ws/words.js`): a starter fills, never sends (Q8); no typing
    while paused (Q10); no session cap, archived fold under Older (Q17);
    "can't reach" after 3 s of a dropped stream.
  - **"xbin is updating"** is the stream's `bye` (the backend is being
    replaced) followed by a lost connection; back, the page says what
    continued and what an update cut off (`status: stopped`). A dropped
    stream without `bye` is "can't reach" — the draft stays and is not
    sent; Review… is off.
  - **Tested by faking.** `test/ws-stub.mjs` stubs the backend in
    workspace mode and xbind's connection view; `ws-page`, `ws-scope`,
    `ws-states` drive the page in Chromium, `hack/agent-template-ws-page`
    the pure parts. No UI harness pass: no holder component is registered
    on this tree, so no real workspace-mode backend can be framed by a
    real xbind; to re-run against one: the dock's frame, live `delegate`
    events, a real `bye` on an xbind update, the full connection view's
    frame check.

- **D236 — Give an agent a tool: the `mcp-server` template, `GET /tools`,
  required slots, a default coding agent that runs (2026-11-16; issue #51,
  plans/wsagent/journey.md G4 instance half, G8, G9, G10).** Building an
  MCP tile needed knowledge no page held (three comment lines in the agent
  template's manifest), the hook-up couldn't be checked without an
  approval, the default coding agent for an agent-started session was one
  the workspace's connector can't serve, and a fresh assistant opened
  under a popover of five rows.
  - **The template is the contract.** `builtin-templates/mcp-server`: a Go
    backend on the zero-dependency sdk (no third-party module: it builds
    with no network), MCP over streamable HTTP at `POST /mcp` —
    `initialize` with version negotiation (2025-11-25, 2025-06-18,
    2025-03-26, 2024-11-05; the client's when spoken, else the newest),
    `ping`, `tools/list`, `tools/call`, notifications 202, batches, JSON-RPC
    errors, a tool's failure as `isError` (the model reads and corrects it,
    rather than a protocol error). Stateless: no session id, because the
    agent's client initializes before every call and a backend restarts
    between any two. A tool is one function and one line (`tools.go`), the
    example `roll_dice` (readOnlyHint). `"agent": "full"`: reads free, each
    `tools/call` by a person's agent approved by them; narrow it for tools
    that act. The manifest provides `mcp` from the first second, so a
    binding is valid before anyone wrote a tool — what lets a plan create,
    bind and brief in one batch.
  - **`docs/mcp.md`** is the page docs_search must find (a test holds it
    first for eight queries); two lines of the agent's map are generated
    from it; `tile_agent_brief` takes `template: "mcp-tool"` with the
    tool's name, arguments and result and fills done-when, check-by and
    leave-alone — the always-true parts live in code, not in a model's
    memory.
  - **`GET /tools` on an agent instance** answers what a conversation
    would be offered now: per class every tool with its source, per MCP
    server where it comes from and whether it answers (a live
    `initialize` + `tools/list`, reused 10 s; a URL-connected server is
    never probed, since its sign-in may refresh a token). Nothing stored;
    no header, token or address credential, errors' URLs cut to scheme,
    host and path. It is the one route of the template opened to a
    person's workspace agent at its call's role (`agentReadOpen`): every
    other needs `admin`, which an agent's call never has. Rejected: opening
    all of `agentMay`'s reads the same way — each needs its own look.
  - **`"required": true` on a request slot** (the agent template's `llm`).
    Pending rows say `required`, and `optional` once every required slot
    is bound; a tile that marks none has no optional rows, so nothing
    changes for any tile today. The shell then shows optional slots and
    grant requests as one line in the card's flow above the tile, not a
    popover over it; `<bx-bindings>` (xbind-served, so old shells get it)
    folds optional rows behind one line. Rejected: hiding optional rows
    from `/bindings` (old shells would lose them, and dismissals would be
    pruned); folding grant requests of tiles that mark nothing required.
  - **start-defaults' default** with no saved choice is the first allowed
    harness that can run as things are (a sign-in a session saw, or the
    connector), else the first allowed; the saved choice wins; each
    harness says `noSignIn`. Rejected: changing the catalog's order — a
    person signed in to Claude Code keeps it first.

- **D237 — The first-run sheet: one sheet from the login URL to the
  agent's dock, and the host's preset for workspace agents (2026-11-16;
  issue #51; `plans/wsagent/journey.md` G3, G5, G7).** The owner opens the
  login URL and lands in the shell as the bootstrap token, which has no
  agent; today's path to a useful agent is the vault banner, an account in
  the admin console, a sign-out and in, and three setup steps (17
  interactions of the measured 49).
  - **One sheet, xbin's own chrome, in the bootstrap state only.** The
    owner token's browser session (its cookie: not bx's bearer, a tile,
    view-as, `--no-auth`) in a workspace with no accounts at all —
    `GET /first-run {bootstrap}` answers it, so the shell never decides it.
    A workspace with any account (an invited one included) and `--dev`
    (its seeded admin) never see it. "Not now — set up by hand" is
    remembered in that browser only (localStorage), and everything is
    then as before. The sheet is `/vendor/bx-first-run.js` (light DOM, in
    `document.body`, so a password manager sees an ordinary form); the
    scaffold's shell only asks and mounts it (`shell-first-run.js`), and
    keeps its own first-run hints (the "Secure this workspace" card, the
    alerts) off while it is up.
  - **The ordinary routes, in order, and one new one.** `POST /users
    {role: admin}` makes the account — its password rules, defaults and
    audit line; then `POST /first-run/session {id, password}` swaps this
    browser's token cookie for the account's session in one step; then,
    as the new account, `POST /vault-unseal` (when the vault is
    unconfigured and the host doesn't unseal it) and `PUT
    /delegate/workspace {enabled: true, who: "me"}` (when a model is
    connected and the switch was never set). The swap is a sign-in no
    wider than `POST /login`: the account's own password, the owner
    token's browser session only, the bootstrap account only (the
    workspace's ONLY account, an enabled admin, never signed in, made in
    the last 15 minutes), same-origin by Fetch Metadata (else Origin),
    throttled with `/login`, audited. So it can't open a session for an
    account that existed before or was ever used. The token stays the
    bootstrap credential — its browser sign-in until an admin turns token
    sign-in off (the sheet says so, and where), the token itself until it
    is rotated. Rejected: one route that creates and signs in
    (a second account-creation path to keep in step with `POST /users`);
    `POST /login` from the sheet (it would do, but nothing narrows it to
    the bootstrap account); the vault first (a failure would then leave
    nothing made; the sheet makes the account first, and a vault or agent
    step that fails is said as what is left, never a state that locks the
    person out — they are signed in by then).
  - **What the button does.** With a model (the host's or an admin's) and
    the switch never set (`workspaceAt` absent) — or agents on already (an
    admin's switch, or the host's file leaving who to admins) with nobody
    given one, where it sends only `{who: "me"}` — the primary button reads
    "Create my account and turn on my workspace agent" and a quiet one
    makes the account without it; where the host's file gives admins or
    everyone one it only makes the account, and where an admin set agents
    up already or the host turns them off it leaves them; without a model the sheet says the
    agent needs one (`/xbin/llm`) and the button is "Create my account".
    "Everyone" is never chosen here. After it the shell reloads as the
    account and, when the agent was turned on, opens the dock and gives
    the agent's composer the keys (the dock's `focusComposer()`: the frame
    focused and a cleared `xbin:agent-context`, which the page answers by
    focusing its composer — once its store has booted, when the context
    came before the composer was drawn, as it does on a first open). The
    reload lands on `/`, not the token's screen, which isn't the account's.
    `/xbin/system` shows a host-locked who read-only.
  - **The host's preset: `"workspaceAgent"` in `XBIN_LLM_CONFIG`.** Beside
    the connector's settings, read and validated with them
    (`internal/llm/hostagent.go`: a misspelling stops the boot, a bad
    change is refused and the last good file stays), and locked as they
    are: `false` locks agents off; `true` locks them on and leaves who to
    admins; `{"enabled": true, "who": "admins" | "everyone"}` locks who
    too. "admins" is a new value: every admin account with no agent and no
    entry of its own gets one — at boot (the key first appearing), on
    every `users` event (an admin made, or made an admin) and when the
    file changes. xbind keeps it in force by writing it (the switch by
    "host", the entries), so every reader of the switch reads the host's,
    and records what it wrote (`data/hostagent.json`) to take back exactly
    that: an entry it gave when its person stops being an admin or the key
    stops saying "admins", the defaults' entry it added when it stops
    saying "everyone", and, when the key goes, the switch back to the
    workspace's own setting from before (off if never set; as it is if the
    host never changed it). An entry a person or an admin made is never
    removed, lowered or overwritten (review finding 3);
    `GET /delegate/state` and `GET/PUT /delegate/workspace` carry `locks:
    {enabled?, who?: {locked, reason}}` (the connector's shape) and a PUT
    of a locked key is 409. The chrome draws "Set by your host" on the
    setup steps and says the host turned agents off. Rejected: an env
    variable of its own (a second file and lock model for a host to
    learn); overlaying the host's value at every read of the switch (the
    delegation package's readers are many; writing it keeps them one).
  - **Not built.** Shells from before keep today's path (no hook; xbind
    rewrites no HTML); the iOS app's first run (the app joins a workspace
    that has an account — `plans/wsagent/journey.md` §6e says what a
    native sheet would need).

- **D238 — The plan: multi-step work is one change set, reviewed once
  (2026-11-16; issue #51; `plans/wsagent/journey.md` G1, G2, G11, G12 and
  G6's mode on the line; §9 there).** Through the agent the journey took
  three prompts, two sheets and a review, with three dead ends: an admin's
  `tile_create` was refused after approval, the admin opt-in sat two menus
  away, and every step was a sheet of its own.
  - **Two more kinds, on a route of their own.** `tile.create {template,
    path, owner?}` and `agent.start {tile, brief, harness?, mode?}` join
    the admin kinds in a change set proposed through `POST
    /delegate/plans` (`plan: true`). `POST /delegate/changesets` and its
    sets are untouched — its 403 without the opt-in, its step-up for every
    approval, its closed list of six — so nothing changes for a workspace
    until an agent proposes a plan. Rejected: a flag on the old route (one
    body, two sets of rules); letting the old route take the new kinds.
  - **Later lines name earlier lines' tiles.** xbind describes a line that
    names a tile an earlier `tile.create` line makes from the template's
    manifest (`deferred`, `needs`), and judges it by the handler's own
    check when it is applied, after the tile exists: stale unless the
    sentence is the same and no warning appears that was not shown. Only
    what a new tile needs wired is planned ahead (http slots to tiles and
    `xbin:llm`, a start); the rest waits for a plan after it exists.
    Rejected: an overlay over the registry (every check that reads a
    component would have to learn of tiles that are not there).
  - **Personal or an admin's.** A line is personal when the person could
    do it by hand with no admin: a creation their own rule allows (the
    delegate-clamped check — and an org only where they hold Create), a
    binding between tiles they own (D88; never `xbin:llm` or a named set),
    a start. Grants, org shares and limits stay an admin's whoever asks.
    Personal lines apply with no step-up, and only while they are still
    personal (`TestPersonalLinesNeedNoAdmin`,
    `TestPersonalLinesOnPlannedTilesNeedNoAdmin`,
    `TestPersonalLineThatBecameAnAdmins`). An admin's line needs a recent
    sign-in and the admin's own tick (it starts unticked, "applied as
    you"): that is the consent for that line of that plan. The standing
    opt-in (`adminChanges`) is not asked for on a plan's review — first it
    was offered there, a click the owner's bar counts — and stays what
    lets an agent READ admin state: of a line that takes an admin, or that
    xbind can't apply, the agent, its conversation and its page read
    xbind's sentence and the result, nothing of the state behind it,
    unless the person opted in (`TestPlanShowsTheAgentNoAdminState`).
  - **No Done.** After an Apply in which every chosen line was applied
    the review closes itself and the conversation's card keeps each
    line's outcome; a failure keeps the results open until the person
    closes them.
  - **A creation says what the template gives.** The line's effects are
    read from the template's manifest — a use of something in the scope
    the tile lands in is its own grant, so it is a warning (`ACCESS`), as
    are endpoints for the outside (`EXPOSES`) — and the line is pinned to
    that manifest (`pin`), as a start is to its network and API access: a
    template edited, or a start resolving differently, by Apply is stale.
  - **Needs an admin.** For a person who is no admin such a line carries
    xbind's sentence and nothing of the state behind it (an agent could
    otherwise read bindings it may not see by proposing them), can't be
    chosen, and `…/ask` — the person's own act — copies it to every
    workspace admin as a set `from` them, described as that admin would
    apply it; the first to decide ends the other copies and the result
    returns to the asker's line. Only the admin's own session reads such a
    set: not their agent, not its page.
  - **Bound to its conversation.** A set keeps the agent session that
    proposed it; a start runs as that conversation (the tile agent is its
    to follow), is refused when its taint forbids the target's model or
    when it is over, and starts on the coding agent and mode that were
    shown (pinned when the line named none).
  - **What an agent may touch.** A tile's agent level governs an agent's
    calls to the tile's API and its reads of the tile's data; managing a
    tile (its bindings, a grant, creating it, starting its own coding
    agent) is governed by the person's rights and their approval on xbin's
    review. So a line about a "read" tile is proposed, and says "this
    tile's author marked it read-only for agents: this changes how it is
    wired, not its data" (the agent template's instances are "read"), and
    "off" refuses a line on either side of it — as consumer or provider,
    source or target. Every tile a line names is also held to the
    proposing conversation's scope, to what its person can read (one
    answer for a hidden tile and a missing one) and to its orgs' agent
    rules, when described and when applied.
  - **G1.** Under delegation an empty owner is the person's, for an admin
    too. `POST /templates/new`'s checks are one function the handler, the
    dry run and the request's preview share; every `DelegateAct` row has
    the handler's narrowing as a dry-run judge or is listed with why it
    needs none (`TestDelegateDryRunJudgesEveryAct`).
  - **G11, G12.** xbind writes a change's before and after in words
    (`beforeText`, `afterText`); the agent's backend notes the turn it
    last told its conversation of (`told_turn`, forward only).
  - **The agent.** `plan_propose` beside `admin_propose`; the prompt says
    when (work that creates or binds is ONE plan), and that an applied
    plan is followed through and checked before "done". A single request
    for a step of an open plan files nothing.
  - **Measured.** `journeyAgent`: 13 interactions to the agent's "done"
    (36 before, 49 at first), one prompt, one review of five clicks.
  - **Not built.** Answering a tile agent's permission requests from the
    dock (G6's other half); changing the coding agent on a plan's start
    line; asking org admins (only workspace admins are asked); telling the
    asker's conversation by itself when an admin decides (`admin_changes`
    reads it).

- **D239 — The workspace agent on the web and with MCP servers:
  workspace policy, org rules, the person's settings; xbind fetches and
  speaks MCP (2026-11-16; issue #51; `plans/wsagent/web-mcp.md`).** The
  owner: "Web access/MCP — let's make this configurable in agent settings
  + have workspace policy."
  - **Three layers, each narrowing the one above.** The workspace policy
    (`PUT /delegate/workspace` `web`, `mcp`; off by default), preset and
    capped by the host's one deployment file (`XBIN_LLM_CONFIG`'s
    `workspaceAgent` key, beside D237's `enabled` and `who`); the org rules (`web: ask |
    allow | never`, `mcp: any | org` in `Org.Agents`); the person's
    toggles in Preferences (`PUT /delegate/state` `web`, `mcp`,
    `mcpReadOnly`), widening and so armed like Allow. The host's mode is a
    ceiling and a preset, not a lock against narrowing: turning a
    capability off is never what a host must prevent. One host file, not two: `web` and `mcp` are members of
    the `workspaceAgent` key (parsed with it in `internal/llm`), and
    `locks` names the held settings by their `PUT` names in the one locks
    map; a write past a ceiling is `409 {settings}`. Rejected: a file of
    its own (`XBIN_WSAGENT_CONFIG`, this change's first version), and
    `lockAll` for these keys — the switch and who are what locks outright.
  - **xbind fetches, the sandbox stays "model only".** `POST
    /delegate/web/fetch` (an act): GET only, no credentials, addresses
    checked when the socket connects (no rebinding), redirects judged hop
    by hop, 2 MiB / 20 s, a type list, text out, a per-person limit, an
    audit row per fetch. Rejected: giving the backend's sandbox a network
    and filtering there — the policy, the org rules and the taint are
    xbind's, and a sandbox network can't see a conversation's taint.
  - **A URL is a way out.** A conversation that holds any org's data —
    the tiles in no org included — fetches only a URL its person approved
    once (`kind: "web"`), unless every org it holds allows the web
    outright; one that holds an org's data whose rule is `never` loses the
    tool. No standing approval of a URL: a URL is the data.
  - **xbind is the MCP client for a tool's call.** To judge a
    `tools/call` — which tool, its `readOnlyHint`, the tile's agent level
    — xbind must read it, and the tool's annotation must come from the
    server, not from the agent. So `POST /delegate/mcp/call {server,
    tool, arguments}`, xbind calling the MCP tile as the person. The
    template's own MCP client is reused for the reads (`initialize`,
    `tools/list` on the tile's endpoint, through the gate). A tool asks
    the first time; "always for this tool" is a standing approval of
    scope `tool`.
  - **Search only where the provider has it** — a native search tool of
    an upstream an admin marked, refused by the connector unless the
    session's reach allows it; never a third-party search service. Not
    built in this change: `web.search: true` is refused "not available
    yet", and the reach's `search` is off.
  - **The reads of an MCP server are reads** (the journey plan's G4): on
    a tile's declared endpoint, exactly `initialize`, `ping` and
    `tools/list` (one JSON-RPC request) pass unasked; every other POST
    there is refused, so a tile-call ticket or standing approval of the
    endpoint never covers a tool.
  - **Not yet**: `web.mode: "full"`, `web.search` and `mcp.mode: "listed"`
    (external MCP servers with credentials held by xbind) are refused with
    "not available yet" rather than half-built.
- **D240 — Outside MCP servers for the workspace agent: listed by an
  admin, spoken to by xbind, a way out like a URL (2026-11-16; issue #51;
  `plans/wsagent/web-mcp.md` §6.1).** The part D239 left out
  (`mcp.mode: "listed"`), as the owner asked for it next.
  - **An admin lists them; xbind holds the secret and is the client.**
    `mcp.servers` of `PUT /delegate/workspace`: `{id, name, url, auth:
    none | bearer | header, secret (write-only), note, orgs,
    privateNetwork, plainHttp, trustReadOnly}`. The secrets live in the
    LLM connector's sealed envelope, now `internal/vault`'s `SealedMap`
    (one implementation for xbind-held secrets), under a label of their
    own; reads say `hasSecret`, and whatever a server answers is scrubbed
    of its secret before the agent, its page or the model reads it. The
    host's file may preset the list — and then locks it, unlike a mode:
    a list is the addresses that leave the workspace — naming secrets by
    `secretFile` (as an upstream's `keyFile`) or `secretEnv`. Rejected:
    giving the agent's sandbox the server's address (its network stays
    "model only", and the org rules and taint are xbind's), and the
    agent naming a URL (only the admin's list is ever dialled).
  - **The web's dialer, one host per server.** Requests go through
    `internal/webfetch`'s guarded dial to the listed host alone (checked
    when the socket connects), TLS verified, redirects only to the same
    origin, 1 MiB and 20/60 s, an event stream read within those until
    the request's answer. A server's own requests (sampling, elicitation,
    roots) end the call: xbind is a client for tools only. A private
    address needs the admin's explicit `privateNetwork` (link-local, the
    metadata address, stays refused). The session id is xbind's, per
    person, server and conversation.
  - **Sending arguments out is the web's way out.** From a conversation
    that holds an org's data a call is approved each time (no hour or
    always), unless the org trusts the list (its rule `mcp: "listed"`)
    or the server is listed with that org (`orgs`: it counts as inside).
    The org rule grows to `any | listed | org | never`, strictest
    `never > org > any > listed` — `listed` is the one loosening value,
    as `web: "allow"` is. What a server answers taints no org but marks
    the conversation as having read the web.
  - **A tool's first call always asks**, the standing approval pinned to
    its definition as for a tile — and the key of what is allowed is the
    server's id and a hash of its URL, so an address that changes (or an
    id reused for another server) is allowed by nobody. "Read-only tools
    don't ask" doesn't reach an outside server unless the admin trusts
    its marks (`trustReadOnly`, off by default): a `readOnlyHint` there
    is an outsider's claim. Listing a server's tools is free once the
    person's MCP setting is on and the server is visible to them.
  - **Not yet**: OAuth sign-ins to an outside server, its resources and
    prompts, and the server's `GET` stream.

- **D230 — Coding-agent sign-ins are held by xbind: one sign-in per
  person serves their sessions on every tile, sealed under the vault, put
  into a session only where nobody else can change what it runs
  (2026-11-16; issue #51; `plans/workspace-agent.md` §5.7,
  `plans/agent-dedupe.md` B4/F5; spec `plans/wsagent/signin-store.md`).**
  internal/signins (store, flows, the confined runner), sdk/acp guided.go,
  internal/term signin.go, internal/server agentsignins*.go,
  web/agent-signin.js; docs/agent-signins.md, docs/protocol/signins.md.
  The owner: "That's for agents like Claude Code? Would say do now, very
  important to UX." Lifts D179's saved sign-ins out of the agent template
  into xbind and extends D178's guided strip.
  - **Before.** A subscription login lived in one place at a time: the
    `$HOME` of a person's terminals (`claude auth login`, D178) or one
    agent-template instance's partition vault (D179). A session the
    workspace agent started on a fresh setup stopped at "sign in first".
  - **A per-person store sealed like the connector's keys.**
    `data/signins/<hex(user)>.json`: metadata in the clear (so the list,
    the admin's counts and "the vault is sealed" work while sealed), the
    secrets sealed whole under a barrier subkey of the person's own. No
    read returns a secret or a part of one — not "last four" either; the
    only way out is the store's `Use`, called by xbind's own session
    start. Rejected: the workspace settings file (a secret is not a
    setting), a tile's vault (a tile's code reads its own vault), one
    workspace-wide file (one person's write would re-seal everyone's).
  - **The guided flow runs in a throwaway confined sandbox.** xbind runs
    `claude setup-token` itself through `internal/confine`: the base
    image, no tile, no home, no layer, an empty `HOME`, internet egress, a
    1000-column terminal. Nobody can enter it and nothing a tile wrote is
    in it — stricter than D179, which had to mint in the person's own
    sandbox. Never a copy of a CLI's credential file (D179: refresh tokens
    are single-use). So only Claude Code has a guided flow: Codex, Gemini
    and OpenCode have no mint, and take a pasted key. The plan's "and its
    equivalents for the other harnesses" names something that doesn't
    exist.
  - **Into the adapter's environment, through the spawn frame.** The path
    the LLM connector's key takes (D226): to the agent host, set on the
    adapter alone — not the sandbox spec, not PID 1, not a terminal. A
    setup-token is already the least-exposed form Claude Code has (model
    requests only); nothing shorter-lived exists. Codex needs ACP
    `authenticate` and then writes `auth.json`: the host gives that
    session a private `CODEX_HOME` in the sandbox's own `/tmp` (links to
    the person's `~/.codex` but for `auth.json`), so the file is never in
    the shared home — rather than D179's "write it, then remove it".
  - **The shared-tile rule (the hard part).** Each session is a sandbox
    of its own, so a co-user can't read another's environment — but they
    can change what it runs: the tile's shared environment layer holds
    the binaries, the tile's directory the project's hooks. So a saved
    sign-in goes in only when the session is the person's own (their
    sign-in — never view-as, a component, the root token), sandboxes are
    on, and nobody else can change the tile's code: no other non-admin
    holds write level or more, by any source, on the registered tile the
    session's directory lies in (`Broker.SigninOthers`). Partitioned tiles
    are not exempt — a partition isolates data, and the tile's directory
    is bound read-write into every partition's sessions (the first cut
    exempted them and judged the directory as written; the review of
    2026-11-16 found both). A session the workspace agent starts gets one
    only when the person's approval named it in xbind's words and the
    ticket bound its id: the agent steers that session, so its model can
    get the credential — redaction (the joined stream, separators,
    base64/hex, credential shapes) lowers accidents and is not a
    boundary; the approval is. Otherwise it is left out, the session says
    why and offers the connector or the tile's own login.
    `term.signinPlanFor` decides; the UI repeats it. A session that
    carries one is its person's alone — admins and view-as get no pass to
    drive or read it, live or in history — a restart never widens what it
    was started with, and it
    is ended when someone else becomes able to change its tile, or the
    switch goes off. Rejected:
    injecting everywhere (the brief's worry is real: one hook in a shared
    tile takes a year-long token); counting admins as "other people" (in
    any workspace with two admins nothing would ever be injected, people
    would fall back to the `$HOME` login — a full login with a refresh
    token, readable by the same code — and an admin can change any tile
    anyway: they are in the trust base, D179 N20); a private layer for
    such sessions (the person would lose the tile's installed tools, and
    the directory's hooks would still run).
  - **An admin can't take the sign-ins by resetting the account.** A
    credential someone else makes for a person (a password, a sign-in
    link, an SSO email) removes that person's saved sign-ins. Without it
    the store would be one password reset away from any admin. A deleted
    account's file goes at once and the account stamp keeps a later
    account under the same id from reading it; a disabled account's are
    unusable.
  - **A removed sign-in ends the sessions running on it.** They hold a
    copy in their environment; ending them is the only way "removed"
    means anything inside xbind. The credential stays valid at its vendor
    until the person revokes it there, and Remove says so.
  - **What xbind keeps is redacted.** The session's exact secret in any
    frame from the agent host becomes `[saved sign-in]` before an event
    is logged, published or written to history: an admin, the workspace
    agent's model and a backup never see it. The harness's own model does
    see what its tools print — stated, not solved.
  - **Policy.** A workspace switch (default on), the host's
    `"agentSignIns"` key beside `workspaceAgent` (set = locked), the
    orgs' harness lists as before; admins see counts and metadata per
    person without names, and revoke.
  - **One element.** `<bx-agent-signin>` keeps its URL and its D178
    properties and gains the server-driven card, the list and the chip;
    `signin-scan.js` stays served and is no longer used by new code.
  - **The consumer interface is designed, not built.** "A component never
    receives the secret as data" can't hold for a component that controls
    the sandbox its coding agent runs in: it can read that process. So
    the interface is the person's consent per component, a single-use
    grant, and redemption by the tile-sandbox runtime at a launch it
    performs — the agent template's own store stays the fallback and
    existing instances are untouched. Left for a change of its own.
  - **Not chosen:** a proxy that holds subscription credentials (D179's
    policy stands); validating a pasted key against its vendor (xbind
    calls no provider); showing a hint of the secret; a per-tile opt-in
    for shared tiles (possible later: the person's informed choice).
  - **Changed 2026-11-17 — used by default; "I trust this tile's source"
    where the source isn't only the person's.** The owner, a day after
    the strict rule shipped (v0.3.80): "maybe let's default sign-in, AND
    on tiles which were created by different users have a 'I trust this
    tile source' with a small risks explanation." The shared-tile rule
    above ("only where nobody else can change the tile's code") is
    REPLACED: it left a team's shared tiles — where people work — with no
    sign-in at all, and people fell back to the `$HOME` login, the
    broader credential. Now the default sign-in goes into a person's own
    sessions; on a tile whose source is not only theirs it waits for
    their answer, once per tile. Two conditions make a source not only
    theirs, kept apart in the code: (1) the tile is owned by someone
    else — a person, an org, the workspace — which is the owner's
    sentence; (2) someone else who isn't an admin can change its code
    (write level, any source, partitioned tiles too) — the coordinator's
    addition, because a file a co-editor plants runs with the credential
    just the same; one line turns it off (`signins.CoEditorsNeedTrust`).
    Trust is per (person, tile identity — the directory's own, as waiting
    plan lines pin it), kept with the person's sign-ins, listed and
    revocable, and re-asked when its basis changes (a new editor, another
    owner, a re-made tile); only the person gives it, in xbin's chrome —
    the card, or the ticked trust line of a start they approve — never an
    admin, an owner, the agent or a page message; a workspace switch
    (host-lockable) can forbid trusting. A workspace-agent start under
    "start without asking" now gets the default on the person's own and
    trusted tiles and never into an untrusted one; a start on a tile the
    same plan creates as the person's own names their default. Kept from
    the review of 2026-11-16: restarts never widen, sign-in sessions and
    their transcripts are their person's alone, masking on the joined
    stream, the sweep, the structural guard against real CLIs in tests.
    **The owner accepted the risk model on 2026-11-17**: on the person's
    own tile nobody else can edit, only code they put there runs with the
    sign-in; after trusting, the people the card listed can read it
    (spec §9, residual 8). Rejected: asking on every tile (the default is
    the point); implying trust by approving a start (an approval is of a
    start, not of a tile's source — the line is separate and unticked);
    trusting a path (a tile made again at the path is another tile);
    letting a tile's owner or an admin vouch for it (the credential is
    the person's).
  - **Changed again 2026-11-17, after the independent review of the
    trust rule** (the coordinator's decisions A and B, and findings 1–7).
    **A. A session the workspace agent starts never carries the person's
    sign-in unless the person ticks it, each time** — "start without
    asking carries the default" above is REVERTED. The reviewer confirmed
    in code that the agent drives the sessions it opens, nothing refuses
    its prompts to one that carries a sign-in, redaction is best-effort
    and a file written in the tile skips it. So: never under the
    preference, on any tile, with no sub-preference; on an approval or a
    plan's start line, "Use my ‹harness› sign-in for this session" is a
    line of its own, unticked, warned, saying the agent directs the
    session; the trust line goes under it and only with it; unticked,
    the session runs on the workspace's model — the owner's original
    decision for harness auth, "connector by default + sign-ins held by
    xbind". The person's OWN starts keep the default-on rule. **B. A
    workspace-owned tile needs no trust for a workspace admin** while no
    non-admin can change it — keyed on the stored role, so a demotion
    sweeps; not extended to org tiles. **Trust is void when its basis is
    seen to change, and stays void** (finding 1: a stateless "editors ⊆
    then" check let mallory gain write, plant a hook and leave); a move
    is a change (5); **an answer is bound to a basis token** and a card
    re-arms when its content changes (2); the trust switch gets the
    host-file latch (3) and its own file, which a downgraded xbind never
    rewrites (7); people are shown by account id with the display name
    beside it, all of them before the button can be used (4); the
    agent's credential learns that a sign-in exists and nothing else
    (6). Rejected: reviving a void trust when the tile is back as it was
    (the gap is exactly when something was planted); hashing the basis
    alone for staleness (a gain and loss between two looks hashes the
    same — hence the event-time void); a "use my sign-in for agent
    starts" preference (it would be the default again). **Open, with
    the owner** (the reviewer's HIGH): one `$HOME` is mounted read-write
    into every session, so code in an untrusted tile can plant
    `~/.claude/settings.json` hooks, `~/.gitconfig` or `~/.bashrc` that
    run later in a session that carries the sign-in — in the threat
    table as an open residual (§9, 10), not changed here.
- **D242 — A failed start retries by itself (2026-11-16; the postfix
  report: a tile sat `failed` for eight days after one slow start).** The
  owner: "a failed start retries by itself." Until now every start failure
  stayed failed until the next save; only a crash after a healthy run
  restarted by itself.
  - **Only what can clear by itself.** Retried: the process was alive at
    the health deadline — it never listened (also while waiting on xbind:
    the grace of 52d21da2 was used up) or listened and never accepted — a
    launch that met a transient errno (`EAGAIN`, `ENOMEM`, `EMFILE`,
    `ENFILE`, `ETXTBSY`), and an exit before it listened once (a second
    one in the series is a crash loop). Never: a build or manifest error,
    a refusal by policy, a hold or a limit, xbind shutting down, anything
    else (`retryable`, internal/runner/retry.go).
  - **Three retries at 10 s, 1 min, 5 min**, then plain `failed` with the
    last reason, as before. The alwaysOn restart's mechanism — a one-shot
    timer per deployment, nothing polls — on the engine's seam (`after`),
    so the tests fire it. Each retry goes through `runCurrent` like a
    request after a save: the record's code, its build, its launch spec.
  - **What resets.** A save or grant (`Changed`), a deploy or restart (the
    build turn), a healthy start: the count is zero again and a pending
    retry never starts (an epoch on the state). A stop, a removal and
    shutdown cancel it; a retry that comes due while the tile builds,
    may not run (held, disabled, sealed) or xbind stops starts nothing.
    A request meanwhile gets the failure, as before: demand never
    shortens the wait, so a polling client can't turn it into a loop.
  - **Additive wire.** The state stays `failed`; rows gain `retry
    {attempt, of, at, inSec}` (`/backends`, `/runtime`, `/tile-status`);
    a `backend-retry` event follows the primary's `build-error`; the
    frame's `buildRetry`. An older shell draws plain `failed`. Not timed:
    people's partitions (their next request already retries), a failed
    deploy or restart (its answer is the deploy's; the previous generation
    serves).

- **D243 — The MCP tile is built, not stamped: no `mcp-server` template,
  the docs carry the interface, and a plan's binding waits for what a
  tile agent writes (2026-11-16; issue #51; `plans/wsagent/journey.md`
  §10).** The owner: "why mcp-server template? Shouldn't be needed, really
  it's just clutter-ish for users, maybe useful as a docs artifact for the
  interface."
  - **The template is removed; the example and the page stay.** It landed
    the same day and is in no release, so nothing in the wild has an
    instance and there is no retired-template entry: `POST /templates/new
    {source: "mcp-server"}` is the 404 of any unknown template. Its content
    is `examples/mcp-server/` — one file, so that `docs/mcp.md` can carry
    it whole, held to the same bytes by `TestDocsMCPServerIsTheExample` —
    and the page is the interface (manifest, wire, errors, version
    negotiation, `readOnlyHint`). Rejected: keeping a richer multi-file
    example (the page could not carry it, and a page that summarises code
    drifts from it); a "Go backend" starter template (one more template is
    what was objected to; `starter` plus three files a coding agent writes
    is the path).
  - **A line that waits (`planwait.go`).** The tile a plan binds to
    provides nothing until its coding agent has written the manifest; a
    second review for one binding would be the chain of sheets a plan
    replaces. So a `binding.set` to a tile that declares nothing of what
    the slot wants, *after* an `agent.start` line in that tile, is approved
    with the plan and applied by xbind itself, as the person, when the
    tile's manifest declares exactly what was approved — pinned to the
    consumer's slot, the provider, and the provide's kind, service and
    role (`reader`: the least a tool's caller needs; a coding agent that
    declares a wider role, another kind or the service twice is not bound,
    and the line says why). It lapses after 24 hours; the person or their
    agent taking the plan back, undo, the agent's end, either tile deleted
    or changing owner, and the slot being bound meanwhile cancel it; every
    Apply-time check runs again at the moment it would apply and a refusal
    cancels it; a pause holds it. At most once (`waitMu` and the item's
    result); audited as a line of the plan (`under: "waits"`, `"waited"`).
    **Only a personal binding waits** — between the person's own tiles.
    A line that takes an admin's rights is approved with a recent sign-in,
    and that sign-in must not reach 24 hours ahead: it never waits, nor
    does any line of a request to the admins. Such a binding is blocked
    with xbind's words for what to do (`later`: propose it again, in a
    plan of its own, once the tile provides the interface), which the
    agent reads. (First built with admin lines waiting; reversed in
    review.) Rejected: letting any line wait (only what a tile agent must write is
    a reason to; `TestOnlyABindingWaits`); binding at once to a tile that
    provides nothing (the handler refuses it, rightly: a binding is a
    grant of a role the tile does not yet define); a standing "bind
    whatever it provides" (a coding agent would choose the role).
  - **What the review promised holds after the line fired** (review of
    7d17c1ff). The approved role is recorded with the binding
    (`bindingCaps`) and is the most it grants whatever the tile declares
    later; the bind re-reads the tile under the lock it writes under. The
    line is held to its two tiles' own identities, not their paths (a
    tile made again at the same path is another tile). It is watched only
    once its set is written, and every stop of its person's agent but a
    pause or a hold ends it, judged at the line — not left to each stop
    path to remember. Of a held line the agent reads one fixed sentence;
    the mismatch is the person's. Taking a plan back ends what it asked
    of the admins.
  - **The start must come before the line that waits on it.** The line
    needs the start (it is applied only with it), and without one the
    binding is blocked as ever: nothing waits on a tile nobody is
    building.
  - **The permissions are answered in the dock.** A tile agent the
    workspace agent started asks its person, not the agent; the dock's
    Needs you draws each request with the chat kit's permission card and
    the answer is the person's own call (`bx-agent-permits.js`). "Allow
    always" is offered only where xbind scopes it to a session rule, as in
    the tile's window.
  - **Counts.** `journeyAgent` 13 to "done" (11 with the template, which
    built nothing); `journeyManual` 28 (21).
  - **Not built:** the start line's alternative mode (edit without asking,
    limited to the tile) as a warned choice on the review; the app's tray
    for permission requests and waiting lines; telling the asker's
    conversation when an admin's copy of a waiting line applies.

- **D241 — The workspace agent reachable when the app is not open:
  pushes, lock-screen actions, one Live Activity per workspace, one router
  (2026-11-16; issue #51; `plans/wsagent/ios-stage2.md`; package E2).** The
  owner: "do STAGE 2 — make the agent reachable when the app is not open".
  Stage 1 (D233) had no notification actions and no card of its own.
  - **One push per thing that waits, on the one push path.** Requests,
    change sets (plans, an admin's ask), the agent's own question and a
    finished request are `agent.*` kinds sealed to the device as every
    xbin push is; the new data rides in the sealed plaintext (`agent`), so
    the relay learns nothing new. One per thing (collapse
    `agent-approval:<id>`, …), a 3 s grace, a per-person budget with one
    summary past it — replacing "one notification a person, a count, one a
    minute". Rejected: a payload with ids only and a fetch on wake (the NSE
    holds no session to fetch with, and giving it one is a credential on
    the lock screen's side); the agent's words in a title (never).
  - **Allow once from the lock screen only where the whole call is shown
    and two independent checks agree.** xbind's clip rule
    (`LockScreenOffer`: a tile call, text body ≤ 200 bytes and 3 lines,
    target ≤ 120 bytes, nothing hidden, no warning) sets `offer`; the app
    re-checks the same fields itself; the person turns it on per workspace
    after an explanation (off by default); the action needs device
    authentication; the app re-reads the request and sends nothing that
    differs from what was shown; xbind refuses a `via: notification` Allow
    that fails its rule and audits the decision as one. Everything wider is
    Review only; Deny (and Reject) wherever it is on. This answers ux.md's
    Q3 "revisit for once approvals whose whole body fits". Rejected:
    deciding in the NSE (no session there), and an approval from the Live
    Activity (no room to show the call).
  - **Clearing by a sealed background push** (`agent.clear`, the relay's new
    `background` type) plus a sweep on every foreground and background
    refresh: iOS rations background pushes and drops them for a force-quit
    app, so the sweep is what makes it hold. Rejected: an alert with the
    same collapse id (it shows again), the notification-filtering
    entitlement (Apple's approval, and still a delivered push).
  - **One Live Activity per workspace on the existing activity plane**: a
    type of its own (`WorkspaceAgentActivityAttributes`: its push-to-start
    token is its own), registered through the same routes (`agent: true`,
    `agentStartHandle`), state counts and a phase only (`agents`, `paused`
    added to the relay's checked fields). An older relay gets the subset it
    checks. Rejected: reusing the tile agent's attributes type with a
    marker (an older app would draw it as a tile agent's turn).
  - **The holder's notifications are worded by xbind.** A delegation
    holder's `POST /notify` (`question`, `done`) becomes `agent.ask` /
    `agent.done` with xbind's title and body; the agent's question is drawn
    by the app quoted as its words, below xbind's title — a deliberate
    change of ux.md §9.3's "no body text": a Reply needs the question in
    view.
  - **One router; links are xbind's page link plus a fragment**
    (`xbin/agent#r=<id>`, `cs=`, `ts=`, `c=`, `needs`, `first`): stage 1's
    app opens the agent, an older one the workspace. Fixed on the way: the
    approval pushes linked the holder's path, which no app parses.
  - **Quick replies fill, never send** (the template's store, web and
    native alike) and are labelled the agent's suggestions; **dictation is
    on-device system speech**, app chrome given only to the person's
    agent's view — no vocabulary change, no microphone for any tile.
  - **Handoff to `/xbin/agent`** (the web page opens the person's last
    session, which the view keeps); universal links only for a domain the
    project controls — a self-hosted workspace's domain can't be claimed by
    the App Store app, and its `xbin://` link stays the way in.
  - **Owner steps** (ios-stage2.md §12): deploy the relay, set the app's
    relay, the Time Sensitive Notifications capability and its entitlement,
    the review notes. Not verified without a device: real APNs, Face ID on
    an action, the Dynamic Island, dictation audio, Handoff between devices.
  - **Review round (2026-11-17).** An independent review's findings, each
    fixed with a test that failed before: the lock-screen rule in rendered
    terms (four collapsed lines of 12 em at the largest default text size,
    only countable characters — no line or paragraph separators, tabs, odd
    or doubled spaces, right-to-left, wide, combining or emoji), xbind and
    the app held together by shared vectors; the app's own lock leaves
    Review/Open only and Allow once needs a passcode and protected data;
    every pushed count clamped to 0…9999; a lost answer re-read before the
    follow-up speaks; a tile's or a person's name never in a title, quoted
    and bounded in the body; where the person decided is theirs, not the
    agent's; tests that check what they claim, test01 on xbind's real
    sealed push (a spooling relay, simctl push, the extension's own drawing
    code — simctl push doesn't wake the extension). The shapes that landed
    beside it on master (outside MCP servers, a start naming a sign-in,
    waiting plan lines, dock permission prompts) are Review only, and so is
    a tile call whose preview says more than a tile call's.

- **D244 — The connector translates Responses to chat completions for an
  upstream that has no Responses API (2026-11-17; issue #51; the first
  real-model run, `plans/wsagent/real-model-run-2026-11-17.md`).** The
  first endpoint the workspace agent was pointed at that was not a
  scripted one serves `POST /chat/completions` and nothing else — as
  OpenRouter-style gateways and most local model servers do. The agent
  template's own engine was fine (it picks the chat wire for every model
  that is not an OpenAI reasoning family, and falls back by itself). The
  connector was not: it relayed `/v1/responses` to the upstream's `404`,
  while telling every host that it serves both wires — so Codex, which
  speaks Responses only and is the first coding agent the connector can
  serve, was offered with "no sign-in needed", picked as the default for
  a tile agent's start, and could not make one call.
  - **Translate in the connector, not gate in the catalog.** The
    alternative — report per upstream which wires it has and stop
    offering Codex — keeps the lie out of the catalog but leaves a
    workspace on such an endpoint with one coding agent and every tile
    that calls `/v1/responses` broken. The wires share a stateless core
    (messages, function tools, streamed text and tool calls, usage);
    translating it is one file (`internal/llm/translate.go`) and makes
    "any OpenAI-compatible endpoint" true for both.
  - **Detect, with a setting to pin it.** `responsesVia: "auto"` (the
    default) sends a Responses call to the upstream; a `404`, `405` or
    `501` whose own body doesn't speak of a model — judged before it is
    rewritten for the caller, in whatever shape the upstream words it —
    makes the call go again as chat (the probe generated nothing, so the
    call is metered once), and only a translated call that works is
    remembered, per model, for ten minutes. A refusal alone never is: a
    caller can provoke one by naming a model the upstream doesn't have
    (review of 2026-11-17: as first built, that switched everyone's
    Responses calls for the upstream to translation). `"chat"` and
    `"upstream"` pin either way and always win. No probe at configuration time: an upstream may be down
    or unreachable when it is added, and one settings write should not
    make a model call.
  - **What is not translated is refused, not dropped** —
    `previous_response_id`, item references, file parts: `400
    xbin_llm_translate` before anything is sent. Two things are left out
    and said in the docs: OpenAI's hosted tools (the upstream has none)
    and a provider's opaque reasoning items in `input`. A `custom`
    (free-form) tool becomes a one-argument function and its call comes
    back as a `custom_tool_call`, since Codex sends such tools.
  - **Not done:** the reverse (chat completions for an upstream that has
    only Responses); Anthropic's wire (unchanged: not relayed); a control
    on `/xbin/llm`'s provider form (the row says "translating"; the
    setting is the API's and `bx`'s).
- **D245 — A person's home is copy-on-write on a tile they have not
  trusted (2026-11-18, the owner's "Copy-on-write home";
  `plans/wsagent/home-on-untrusted-tiles.md`).** A person's one home was
  mounted read-write into every session they ran, so code in a tile
  someone else controls could plant `~/.claude/settings.json`,
  `~/.gitconfig` or `~/.bashrc` that ran later, on the person's own tile,
  beside their saved sign-in (residual 10 of the sign-in store's threat
  table; in every release so far).
  - **The rule is the sign-in's.** Where a tile's source needs the
    person's trust (`signins.Source.NeedsTrust`) and has not got it, the
    session's home is a copy: reads come from the real home, writes go to
    a layer per (person, tile identity) under `data/home-copies/`. One
    function decides for every session, on the resolved tile; fixed at
    the start and shown; a restart or resume never widens; a trust that
    stops holding ends the sessions that have the real home there.
    Trusting merges nothing back.
  - **One overlay mount per (person, tile), held in a namespace of its
    own** and handed to each session as a detached clone
    (`open_tree`/`move_mount`), because two overlay mounts over one upper
    are unsafe and nothing may be mounted on the host: the kernel's
    overlayfs, and fuse-overlayfs only where the kernel can't (a FUSE
    mount held in another user namespace serves one kuid: non-root
    processes in a session were refused their home — the review's L5).
    A copy that cannot be mounted fails the session (D78).
  - **The review of 2026-11-18** (all in the plan, each with a test shown
    red): **a session on the copy is confined like a member's, whoever
    opened it** — an admin's sandbox keeps CAP_SYS_ADMIN and could bind
    the workspace again without its masks and write the real home; trust
    the tile for the admin's sandbox. The condition is **sticky**: xbind
    records per tile identity who could change its code
    (`data/signins/tiles.json`), and a tile someone else could change
    before asks until the person answers; history before the record is
    unknown; a one-person install records nothing. **What a trust means
    is versioned**: answers from before D245 are void and asked again. A
    terminal token gets the real home only when the session it was minted
    for has it; the root token's shells' tokens are the root token's. A
    session is judged again when it registers (a start in flight is
    invisible to a sweep). A copy is deleted without its person's click
    only when its tile's directory is positively gone — a restored
    workspace leaves orphans, listed and forgettable. Forget ends the
    sessions on a copy and waits for a start in flight.
  - **The re-review of the same day:** xbind NEVER removes a copy on its
    own (the owner: "homes - must not remove old ones") — a copy follows
    its tile by identity, a gone tile's and a deleted account's are kept
    and listed (to the person; to admins), only "forget" removes one, and
    the sweep only reads, off the watch loop. The root token keeps trust
    answers of its own (one seam, to delete when the token becomes
    onboarding-only: `signins/roottoken.go`, `term.homeUser`,
    `server/signinsroot.go`). The record of past editors covers tiles
    nested in a tile someone can write, former workspace admins
    (workspace-wide), a changed identity (carried over) and a full
    record ("and others"); one account records nothing. A trust answer
    says what its page showed (`shown`), so a page from before cannot
    answer for more than it said. An agent session is judged before its
    driver starts. A fuse-stamped copy is not converted (forget + new).
  - **Open follow-up:** a nested user + mount namespace after the root is
    built, so that an admin's session could keep `mount` without being
    able to rearrange the sandbox's own mounts (the plan's §11).
  - **Rejected:** a host-level FUSE mount like the resources' gocryptfs
    (fuse-overlayfs would run as xbind on sandbox-written data, and leaks
    mounts); a mount per session (unsafe on a shared upper) or an
    ephemeral one for the second session (a login made in one tab missing
    in the next); merging the layer on trust (the attack itself); hiding
    credential directories from reads (not built: the list is never
    complete — what it would take is in the plan).
  - **Kept open:** untrusted code still READS the home. An admin no
    longer attaches to another person's session that has their real home.
    An admin's REAL-home session on a tile runs that tile's code with the
    admin's sandbox powers — by design, now behind the trust answer.

- **D246 — A person's access as a change, one read for a tile, and a
  context budget of a million tokens (2026-11-18; issue #51; the owner's
  three picks from `plans/wsagent/real-model-feedback-2026-11-17.md` §5;
  spec `plans/wsagent/access-and-status.md`).**
  - **`access.set {tile, user, level}` applies as `PUT /access` — the
    tile's access dialog's own call — through `handleAPI`, as the
    approver with their rights read again at Apply.** No parallel writer:
    the handler's `accessCheck` and the users store decide. Personal
    exactly when the person is the tile's user-owner — the branch of the
    by-hand rule that needs no admin right (D88's rule for bindings);
    every other by-hand path is an admin's right, so an admin's line, and
    a person with none gets Ask an admin. A raise to terminal, a system
    component and one's own level are warned (start unticked); one's own
    level is blocked for anyone but a workspace admin, and never personal.
    It never waits. The workspace agent itself and tiles that hold
    `xbin`/`cap:*`/`gpu:*`/`code` are refused as `org.share` refuses them.
  - **The agent learns applied or failed, never the access list** — the
    admin-line redaction, here unconditional (an admin's agent reads
    grants and bindings when opted in, never `GET /access`), and the line
    it reads is written from its own arguments: xbind's line holds the
    before.
  - **Who may resolve a name is who already sees accounts.** Workspace and
    org admins read every account's id and name (`GET /users`,
    `/users-directory`); everyone else sees none, so their lookups answer
    alike for every name and their lines carry the name as typed — an
    admin's review resolves it, or (their own tile) the by-hand route's
    own answer at Apply. Exact id, else exact display name; two by name is
    "ask which", never the list.
  - **A client never applies a kind it doesn't know** (`knows` on approve):
    an older app ticks any unblocked line of a non-plan set.
  - **`GET /delegate/tile` is the union of reads the agent makes today,
    each under its own gate** — map, component, status, logs (terminal),
    the person's own bindings and grants views (the tiles they write, own
    or whose org they admin), cron where a terminal on the tile would read
    it, their own sessions. New surface in one place, no new reach: a
    test compares it with those reads per agent level. Not the template
    (git history, never read for an agent: D78).
  - **The budget: `min(setting, max(60 % of the window, window − 64 000))`,
    the setting 1 000 000 by default**, a workspace setting the host may
    preset as a ceiling. The fixed margin covers one step's growth between
    the size the provider reported and the next call; below a 160 000-token
    window today's 60 % stays. Unknown window: 32 000, as before. It is not
    a spend limit: metering and quotas are.
  - **After the review (same day): a level is judged on what it reaches,
    and the read on what the agent itself reads.** A tile's writers change
    its code and every nested tile's (writersCover; a terminal on it binds
    its whole directory read-write, nested tiles included; its writers
    decide its PRs), and that code runs with whatever those tiles reach —
    so write is warned like terminal, personal lines too, and power is
    looked for over the tile's subtree and what its tiles reach through
    `code:`/call grants and bindings (64 tiles, each once; past that is
    power), the host's network and a call to a system component included;
    `org.share` takes the same rule. Raise and warnings follow the
    EFFECTIVE level before → after (`Access.TileLevel`/`Explain`, the
    Access page's own), so removing an exclusion under an org's terminal
    is a raise; an unresolved account's removal counts as one. Power is
    said only after the person is known to manage the tile's access; the
    line names the id first and apart; Ask an admin answers an
    unresolvable name as a line left out. The tile read's wiring is the
    agent's own admin reads (an opted-in admin), otherwise one bit —
    "unmet needs, an admin can look"; a bound tile the conversation can't
    see is "another tile", one it can taints it. Cron stays at the terminal
    gate: the one part no delegate read gives — said, not hidden. What a
    tile wrote reaches the model framed as data, one field a line; a new
    job name is a short label.
  - **After the second review: power is what code holds, not what it can
    call.** A raise to write or terminal is refused (a blocked line,
    `power`) only where the raised people would change code that holds
    power: the tile, its nested tiles, a tile it lies in, and a tile that
    builds or loads its code (the runner's build go.work, deps, import
    maps). A callee's power is the callee's API's to mediate: warned and
    listed. Power is an explicit list (the admin API, devices, all source,
    the host's network, cap:net-admin, cap:containers, cap:sandboxes, any
    unnamed cap); cap:open-links is listed only. The refusal is a line, so
    the conversation's scope is held first and the agent reads one
    sentence naming nothing; an admin's copy of an ask reads it as "xbin
    refuses this one", and the asker's outcome stays one. Unticked is a
    client default; the server applies what an approve names.

- **D247 — The top bar always visible; the tile menu carries the workspace
  section, admin folded (2026-11-18; the owner's UX request of that day).**
  workspace-template/shell/menus.js, shell-doc.js, shell-css.js,
  shell-anchor.js, bx-shell.js, shell-tiled.js, shell-blocks.js,
  doc-layout.js, shell-agent.js, bx-canvas.js; web/bx-menu.js;
  hack/menus.test.mjs, hack/doc-layout.test.mjs,
  hack/ui-harness/passes/topbarmenu.js (+ docmode, tiled, focusanchor,
  blocks).
  - **The owner's words:** the top bar "shouldn't slide up … should be
    always visible"; "especially in tiled mode, right-click even in a tile
    should have a section … 'create new tile/open tile/new screen' … above
    the 'admin' section, or think about some alternate layout … it's
    starting to get pretty busy, we need to keep it ergonomic".
  - **The bar.** D187 made it float over a Document screen (wider than
    820 px) and slide away while reading — back at an 8 px top-edge strip,
    on a scroll up, with focus or a menu in it — and D193 did the same on
    a Tiled screen (also away when the pointer left it downwards); Canvas,
    Fixed width and every phone kept it in the page's flow. That was 40 px
    of height for the tiles. TopReveal, its CSS and the anchor's
    scroll bookkeeping for it (`heldScroll`) are gone: the bar is in the
    flow in every mode and width. D187's other space saving — the screen
    tabs inside the bar on a wide screen — stays. Nothing else assumed the
    bar could be gone: popovers anchor to `--bx-topbar-h`, the org bar is
    below it in the flow, Tiled measures its pane, the iOS app draws its
    own chrome and embeds no shell.
  - **One section, both menus.** `workspaceItems` — Open tile ▸, Create a
    new tile (… or ▸ per owner), Add heading or text ▸, New screen — leads
    the canvas menu and sits in every tile menu (title bar, ⋯, sidebar
    row, right-click inside the page) under a `workspace` header, after the
    tile's own lines; the same words, the same order, the same
    disabled/absent rules. D192's two block lines fold into one row in
    both (the canvas menu goes from 7 rows to 6); the tab menu keeps its
    own two lines (it adds to that screen).
  - **Admin ▸.** The tile's nine admin lines become one submenu, last:
    lifecycle (Disable/Enable/Unhide, Hide — still confirmed) then the
    admin window's sections. Its row's hint names a state other than
    enabled. A tile menu's top level is now the four panel squares, the
    tile's lines (Close/Open, Pin/Unpin, Row ▸ or Rotate/Swap, Open full
    page, Deployments…, Ask my agent), four workspace rows and Admin ▸:
    8 rows on a canvas, 10 on a Tiled screen (was 12 and 14 for an
    admin), ≤ 12 with every optional line. "Ask my agent about this tile" stays
    the last of the tile's own lines.
  - **Where it lands** (the menu's tile travels as `near` through
    `_menuActions`, the new-tile dialog and `addBlock`): Tiled — splits
    *that* tile's space (D205's "a new tile splits the focused tile",
    with the menu's tile as the focus: the menu's tile need not be the
    focused one — another tile's ⋯ or title bar was right-clicked); Document —
    a row of its own right after that tile's row (`placeAfter`), not the
    last row; Canvas and Fixed width — near the click, as the canvas menu
    already does (D80). A sidebar row's tile that isn't on the screen has
    no place to be beside: the old rules apply.
  - **`<bx-menu>`, fixed, not grown:** a desktop panel is
    `visibility: hidden` until measured, and the focus call ran before
    that, so a menu opened by the mouse never had the keyboard (the same
    for a flyout opened with ArrowRight); it now focuses after placing.
    ArrowLeft returned to the first row (a selector list matches in
    document order), now to the row the flyout hung off. Rows with a
    submenu carry `aria-haspopup="menu"` and `aria-expanded`. No item
    field was added; an older shell's menus work unchanged.
  - **Rejected:** the workspace actions as a second row of squares
    (open tile · new tile · add text · new screen) at the top of the tile
    menu — squares can't hold the find-a-tile and per-owner flyouts, so
    two of the four would need a dialog, and a second row of look-alike
    squares under the tile's own panel squares invites the wrong click
    (mocks in the change's screenshots). Also not chosen: a `Tile ▸`
    submenu for the tile's own lines — they are the frequent ones and
    must stay one click away.

- **D248 — The workspace agent sees updates, proposes a builtin's simple
  update, and hands every other update to the tile's agent (2026-11-19;
  issue #51; the owner's decision of that day; spec
  `plans/wsagent/updates.md`).**
  - **The owner's words:** "give it tools to see updates and apply simple
    builtin updates (incl the PR flow when there are conflicts), any
    application should just be done by having the workspace agent prompt a
    tile agent for it - let's just make sure it's clear to workspace
    agents". Before: both update lists were refused to the agent ("not
    delegated"), `POST /builtins/update` was `never` ("an agent doesn't
    write code"), and no change kind covered an update.
  - **Seeing: `GET /delegate/updates[?tile=]`** — delegate `read`,
    `tile:byTileQuery`, not audited; partition neutral, deployments
    primary only, a sealed component's own credential refused. Its rows
    are the person's own by-hand rows (`GET /builtins/updates`,
    `GET /templates/updates`: the same functions), each held to the
    agent's gates — the tile readable, in the conversation's scope, its
    orgs' rules — and tainting like a listing row. It adds, cheap and
    exact: files edited here only, an open builtin-update proposal (the
    person's `GET /code/prs?target=`), an instance's commits behind its
    template and of its own (git history, confined), and `next` with
    xbind's brief. It runs no merge. Tool `updates`.
  - **`builtin.update {id}`, an admin's line applied as `POST
    /builtins/update {id, mode: "safe"}`** through `handleAPI` as the
    approver, rights read again at Apply. Mode `safe` is new on the
    by-hand route (and `bx builtin update --safe`): `merge`'s own plan,
    written only when clean; any conflict — markers, or an upstream
    removal of an edited file — writes nothing and files the update as
    `pr` does; an adopted unit only where every file it has is upstream's;
    a missing essential unit installed where nothing is; pinned, up to
    date, unknown and sealed refused. "Simple" is that: `replace` (loses
    local edits) and `merge` (markers in live files) are never the line's.
  - **Why the agent may choose this one code change.** It chooses which
    builtin, never the content: the files are the binary's embedded copy,
    and the kind takes `{id}` and nothing else, so a prompt-injected
    "update it with this" has nothing to aim. `POST /builtins/update`
    itself stays `never` for the agent's credential; the change set
    applies it as the approver.
  - **Always an admin's line, never anything that runs unasked.** Never
    personal; for anyone but a workspace admin (an org admin too)
    "needs an admin" with Ask an admin; never waits, never deferred; no
    standing approval, ticket or notification reaches it; `knows`
    (`builtin.update`) keeps an older client from applying it. Scaffold
    units are warned (`SCAFFOLD: …`) and so start unticked. The line is
    pinned to the installed files and the embedded copy: an edit before
    Apply makes it stale, and `safe` decides again as it runs. Held to the
    conversation like a plan's lines; the consoles (`off`, D238) are
    refused — their updates stay an admin's, by hand.
  - **The agent reads applied / proposed / failed**, with `out {update,
    files, tile, proposal?}` and the builtin's own refusals (pinned, no
    update); never xbind's line, its effects (file names) or warnings —
    the line it reads is written from its arguments, as for `access.set`.
  - **Everything else is a tile agent's.** A template instance's merge, a
    builtin update that became a proposal: `next: "tile-agent"` with a
    brief xbind writes from its own names only (the tile's path, the
    template's catalog name, the builtin's id, counts, a proposal's
    number), taken verbatim by the brief template `update`
    (`tile_agent_brief {tile, template: "update"}`, a plan's
    `agent.start`). The commands it names are proven in a tile session
    of an isolated xbind (`TestTemplateUpdateInATileSession`,
    `TestBuiltinProposalInATileSession`). The prompt is unchanged (at its
    cap): the tool's description and every result row carry the rule.
  - **What a tile session lacked, added under the existing rules** (found
    by the two isolated tests): a git identity — a sandboxed session's env
    carries `EMAIL=<person>@xbin.invalid`, git's last resort, below any
    identity the person set (every commit, a merge included, failed
    before); the proposal's copy is cloned into the tile's own `.git`, not
    `/tmp` (the Landlock read guard grants a sandbox's `/tmp` only as it is
    at session start when the workspace lives under `/tmp`); and the read
    sends an update `by-hand` where the person's sessions can't reach xbind
    (no terminal API grant, or offline — D17), saying which. No grant is
    widened.
  - **Not done:** sealed components (they update with xbind), the
    consoles through the agent (their level is `off`), listing pinned
    builtins (the by-hand listing doesn't either).
  - **Review of 2026-11-20** (plans/wsagent/updates.md §The review): a
    tile's files are read and written as the files at their paths — no
    link followed, no FIFO opened, a parent swapped for a link refused,
    each write a rename of a temp file, only while the file is what was
    read — in every mode and the proposal's series (master's code too:
    security commits 5abfa67c, c80e622e, on a branch of master); a
    builtin-update proposal closes merged only by a person (never the
    tile's code) and records only the files that hold the update. Safe
    mode: the approved pin travels with the call and is checked under the
    lock; a file deleted here is a local edit; an adopted unit is always a
    proposal. The template row needs xbind's exact remote URL and the brief
    fetches that URL with git's hooks off; xbin's brief is named by the
    start and written by xbind, the review labelling it xbin's and any
    other text the agent's. A proposal's words promise no review: the
    tile's coding agent applies it, its changes showing in the tile's
    Agent tab and history. A merged manifest raises nothing in force (the
    agent level and cross-scope grants stay pending).
  - **Re-review of 2026-11-20:** the security branch gains per-file
    recording in every mode (repeated merges stacked markers), a root-token
    terminal's merged close, skipped-and-named links in workspace templates
    (migration note links-in-tiles), the fd-walking openat2 fallback, kept
    file modes, notes in the Tile Manager and `bx`, a streamed one-at-a-time
    listing. D248: one update of a unit at a time in every mode, the pin
    holds the recorded base, and the template brief verifies and merges
    xbind's exact head commit (the tile's url.*.insteadOf, attributes,
    filters and merge drivers remain its own — bounded by its sandbox).
- **D249 — Reasoning effort is one word of OpenAI's ladder, mapped by the
  connector onto each model's own levels; a model's reasoning goes back to
  it by default (2026-11-20; `plans/llm/reasoning-effort.md`, stages 0 and
  1).** The owner wanted a control for how hard the model thinks
  (DeepSeek-V4.1-Flash behind his gateway) and accepted the design's
  recommendations, with one change of his own: "Ship the levels, thinking
  replay - at this stage it's industry standard to keep thinking blocks in
  context pretty much always".
  - **A named level, not a number.** `none · minimal · low · medium · high
    · xhigh · max`, or "default" (nothing sent). Every caller of the
    connector already speaks it; only one model family has a numeric knob,
    and only self-hosted. A number (DeepSeek's 1–100 through
    `chat_template_kwargs`) is a later, model-local refinement; the
    connector passes `chat_template_kwargs` untouched.
  - **The connector maps, the agent sends the intent.** A capability record
    per upstream + model — an admin's or the host's entry (keyed like
    `prices`, the whole truth), else the upstream's own `/models` (untrusted:
    ladder words, booleans, a bounded alias map; a list's silence says
    nothing — a real router left `reasoning_effort` out of a model that
    takes it), else a vendored table written from the providers' pages
    (a family whose page doesn't state its levels — OpenAI's — is left
    out). Rules: exact → the provider's alias → the highest level at or
    below → the lowest; `none` off only where it can be; a non-ladder word
    untouched; a model with no reasoning gets none (named). An undescribed
    model's request is byte for byte today's. `X-XBin-LLM-Effort` says what
    was sent. Capability is a property of upstream + model: the owner's
    router serves `glm-5.2` with none · medium · high · xhigh where GLM's API
    takes all; that is an admin's entry, never the table's.
  - **Replay by default, to its source only.** A model's reasoning — plain
    (`reasoning_content`, `reasoning`, a `<think>` block) or opaque — goes
    back on every later turn, with or without tools, for every model that
    returned any: no per-model opt-in (the design had gated it on a
    `replay: {when: tools}` record; the owner's call). Only ever to the
    same provider, model (an alias's target) and wire: never across a model
    switch, never to the same model id on another upstream. The
    translation tags the reasoning items it makes with the upstream and
    model (`rs_<tag>_…`) for the same reason, and names every other item it
    drops (D244's rule). It is input: counted in the provider's prompt
    tokens, which compaction reads. A compacted message takes its reasoning
    with it.
  - **Recovering from a strict endpoint.** An upstream that 400s on the
    field (a 400 not about the context's size) gets the call once more
    without it. The agent remembers that for the provider's model for the
    process, only when the retry worked, and says so in the conversation;
    the connector's translation never remembers — a refusal is something a
    caller can provoke (D244). `"replay": "never"` on a record is the
    admin's lasting switch.
  - **The control:** a chip beside the model, a notched track over the
    model's own levels (Off where it can be off; Off | On), "Default ·
    High" sending nothing, hidden for a model nobody describes; a picker in
    the app (its vocabulary has no slider). Not remembered for new chats (a
    forgotten Max is the cost surprise others' users complain of). The
    level is kept on a model switch and the transcript says what it became.
  - **The admin's edit** ticks the levels a model takes (presets pre-tick)
    — no JSON, no request patches: those (`send`), budgets and scales are
    read and kept but not acted on; a record that needs them is unknown.
  - **Not done (later stages):** a per-upstream ceiling, the workspace
    agent's control, harness configs (Codex's catalog, OpenCode's variants),
    request patches, numeric values.
  - **After review (same day).** *The vendored table applies only on the
    maker's own API*: each family names its maker's API hosts, compared
    exactly with the upstream's base-URL host (no suffix match); elsewhere
    — a router, a local server, a gateway: the same id may take other
    levels — a table match is a suggestion (`xbin_effort_suggestion`, *Use
    these* on `/xbin/llm`) and the model stays undescribed, so an upgrade
    rewrites nothing on a non-maker upstream (proved against golden bytes
    recorded on master). A list's explicit levels still apply anywhere; a
    list never says "no reasoning" (only an admin, the host or the maker's
    table); `none` passes where `can_disable` is unknown. Qwen left the
    table: Model Studio's hosts are per workspace. *Remembering a refusal*
    needs the refusal to name the field and the call without it to work;
    the agent keeps it per conversation (never process-wide, never for
    opaque blocks; a later refusal brings it back once), the connector per
    upstream + model in memory, shown with a Reset. *A replay names its
    model*: the connector names the model it called (`X-XBin-LLM-Model`) and
    refuses a request that expects another (`X-XBin-LLM-Expect-Model`, 409
    `xbin_llm_model_moved`) — the agent's model list is cached a minute and
    an alias can move inside it. The call path never fetches a list for
    effort (an upstream with a configured list is mapped by entries and the
    maker's table until models are listed). One mapping rule, one set of
    test vectors read by Go and JS. Compaction's estimate counts the
    reasoning that goes back. The Responses wire asks for no summary it
    didn't before when no level is set. *Second review:* the connector's
    memory is per caller (tile + person, or person) + upstream at its
    address + model, LRU-bounded, dropped on any change of the upstream —
    one caller (which could forge a reasoning item, the tag being no
    secret) can switch replay off only for itself; "named" is read from the
    parsed error's param, code or unquoted message, never the raw body (an
    echo of request text is no evidence). The agent's memory is per
    conversation *and* model (its Via). `bx … --reasoning <model>=maker`
    stores the record *Use these* stores, aliases included.

- **D250 — One approval, one place: a start is the person's pick on
  xbin's sheet, a follow-up continues the session that is there, and a
  refusal says what differed (2026-11-20; the owner's test session of
  2026-11-21 and the agent's retro; spec `plans/wsagent/approvals-ux.md`).**
  "turn eui into a tic tac toe game" took five approval rounds and
  started nothing. The causes, in the code: the conversation drew a
  brief's start twice (`ws/place.js` placed a request card beside the
  brief's, each with a Review…); a coding agent with no default mode
  (OpenCode) was refused on its own ticket AFTER the ticket was spent
  (`ticketStart` wanted a mode; the plan path had the exception), in the
  words "the approval no longer covered the call (it expired after 15
  minutes, or the tile changed)"; the coding agent was picked on the
  agent's page, which re-filed the start and marked the brief "edited by
  your person"; a mode that doesn't ask turned Deny into the primary
  button and Approve red.
  - **The ticket binds what the agent chose, the approval what the person
    chose.** Tile, brief text (the body's hash) and kind stay bound; the
    coding agent, model and mode are the approve call's (`harness`,
    `model`, `mode`), validated as a by-hand start's, and are what opens.
    The agent suggests (`suggest` in a start's body; `agent.start` takes
    `model` too); the person's tile choice and preference outrank the
    suggestion; a suggested mode that doesn't ask is never preselected; a
    sign-in is never suggested (D230 unchanged). Rejected: binding the
    harness and asking again on a change (the five rounds); letting the
    agent's page carry the pick (it renders a model's text); re-binding
    the ticket to the person's edited brief inside xbind (the edit is made
    on the agent's page, which can't act as the person at xbind — the
    backend files the superseding request and the sheet shows it).
  - **Preferences reuse the store that exists:** `agentSessions.harness`
    and `agentSessions.tiles` are new keys of the agent preferences; the
    model is `harnesses.<id>.model`, the Agent tab's. No admin default:
    the workspace agent's host/admin layering doesn't cover start choices.
  - **One item a pending act; one batch** ("Waiting for you" with a
    count, Review all). **Continue by default** a live idle session this
    conversation started on the tile; a message to a session that doesn't
    ask is a `message` request instead of a refusal.
  - **Refusals name the field** (`reason` beside `refusal: "ticket"`) and
    spend nothing; a start that fails for a reason not the agent's is
    `failed`, with Try again for the person (`…/retry`).
  - **Buttons:** Approve primary on the right, Deny quiet on the left,
    always; danger colours only the Approve. A permission card places the
    coding agent's own options so: its rejections left, its first allow
    right, the rest between in its order (superseding "in its order"). No
    "·" in the elements this adds or rebuilds (the owner's rule, kept to
    new elements until broader UX guidelines): facts are elements there;
    existing lines elsewhere, and the shared words tables, are unchanged.
  - **The app** approves xbind's preselected pick (an older client to the
    approve call) and says where to pick others: the web's sheet. A
    `message` request is a kind it doesn't know, so it shows "update to
    review" and never decides it.
  - **Not built:** pickers on a plan's start line (the line resolves by
    the same chain and shows where each came from); page-driving
    verification (a proposal in the spec, §7c); an admin default for start
    choices.
  - **The review's follow-ups (2026-11-20).** A plan's `agent.start`
    opens exactly the line reviewed (it ran the agent's suggestion: the
    open was unpicked). "Remember" keeps only the fields the person
    changed, and never a mode that acts without asking — one predicate in
    the store, refused on write and ignored on read. OpenCode: read from
    the pinned rootfs binary's own agent table (v1.18.32, not run), every
    agent starts from `{"*": "allow"}` (asking only for paths outside the
    project and for `.env` files); `build` adds nothing that asks; `plan`
    denies edits but its plan files and still runs every command and
    fetches every page unasked; and the tile's own `opencode.json`,
    `.opencode/` config, agents and plugins load into the session, so a
    writer to the tile can loosen any agent. Pinning its permissions from
    xbin would not hold against the tile's own plugins, and was never run
    against a live session — so the fallback was built: no mode xbin
    vouches asks; never preselected from the agent's suggestion, never
    unasked, every agent message to it an approval (the re-review, below,
    lets the person choose it). A
    continue is judged on the live session's model and taint, and one
    carrying the person's saved sign-in needs their unticked, warned tick
    each time (the rule chosen: a follow-up uses the sign-in as a start
    with it would; without the tick, a new session). The approve carries
    what the sheet showed (`seen`); a different resolution is 409 with
    `reload`, nothing spent. Failures are classified by cause, `failed`
    only for a backend that says `knows: ["retry"]`; Try again is the
    sheet again with the sign-in unticked. An agent's held message is
    judged when it goes in (`returned`).
  - **The re-review (2026-11-20).** Stored data a released xbind wrote
    never blocks a write: a saved mode that no longer asks (v0.3.83 took
    OpenCode's `plan` as asking) is ignored at read, not an error — it
    had made every later write fail, the Agent tab's launcher's included.
    The owner wants OpenCode for a tile: the coding agent and model a
    person picks are kept for any coding agent (only a mode that doesn't
    ask never is), and a remembered OpenCode is preselected WITH the
    warning, every time, and never starts unasked (the start's answer
    says why); the default prefers a coding agent that asks. A plan line
    that resolves to it carries the warning and so starts unticked — how
    warned lines already work (D238) — rather than being refused at
    propose. The app already draws `asks: false` as the danger wording
    with the danger-coloured, armed Start, so it may approve such a start
    as it is: no app change. The sheet's mode note says "asks" only of a
    mode that asks. Picks without `seen` are 400 (picks are new here, no
    released client sends them). A filed follow-up is judged as the
    decide judges it. A held agent message: judged on a switch in flight
    too, and the agent is told once when one is returned. Tests that swap
    the package's globals drain their engine within a deadline that fails
    the test before restoring them (the flake "no plan waits").
