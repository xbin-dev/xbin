# A person's home on a tile they have not trusted: copy-on-write

> Status: live

Decision D245 (owner, 2026-11-18: "Copy-on-write home"). Closes residual 10
of `plans/wsagent/signin-store.md` §9 for writes; reads stay open (§9 here).

## 1. The problem

A person has ONE home, `homes/<user>` (`internal/term/homes.go`), and until
this change every session they ran — a terminal, a coding-agent session, on
any tile — mounted it read-write (`internal/term/binds.go`). Code in a tile
someone ELSE controls runs in such a session (a hook, a Makefile, a build
script) and could write `~/.claude/settings.json` hooks, `~/.gitconfig`
(`core.fsmonitor`, `core.hooksPath`, an alias), `~/.bashrc`, `~/.profile`,
`~/.npmrc`, `~/.local/bin`, `~/go/bin`, `~/.ssh/config`. The planted file
ran LATER, in a session on the person's own tile, where their saved
coding-agent sign-in (D230) is in the environment. The sign-in rule and the
trust prompt bounded the code that runs in the session they are decided
for, never code that waits in the home. Present in every release so far.

## 2. The rule

One function decides, for every session that mounts a home, on the
RESOLVED tile (`(*term.Manager).TileOf`): `homePlanFor`
(`internal/term/home.go`). A session's home is one of

- **own** — the person's real home, read-write, as before ("home: yours");
- **copy** — a copy-on-write view of it: the session sees the real home's
  contents, and every write goes to a layer kept for that person on that
  tile, never to the real home ("home: this tile's copy (changes stay on
  this tile)").

It is **copy** exactly when the tile's source needs the person's trust
(`signins.Source.NeedsTrust`: owned by another person, an org or the
workspace — except workspace-owned for a workspace admin — or someone else
who is not an admin can change its code) and the person has not said "I
trust this tile's source" for the tile as it stands
(`signins.Store.Trusts`). The same answer, the same basis, the same voiding
as the sign-in's: one trust per (person, tile identity). A trust that is
not usable — an admin or the host switched trusting off — gives the copy;
the rule looks at the host's switch at every judgement (`signinTrustOff`),
and the first time it is seen off the real-home sessions on a trust end.

**The condition is sticky** (`signins/past.go`). "Only yours now" forgot
the past: someone holds write, leaves a hook, loses write, and the tile
asked nothing. xbind keeps, per tile identity, in `data/signins/tiles.json`
(never in the tile), every party it has seen able to change the tile's
code — an owner that is a person or an org, the non-admin people with
write — and when each was last seen able to. A tile with a party that no
longer can (`Source.Past`) needs each person's answer; the card names them
with the day. Written when observed: at the events the sweep runs on
(`broker.ObserveTileParties`, before the trusts are revalidated) and at
every judgement of a tile (`SigninSource` — a session's start is the
backstop). A tile only ever one party's gets no record (it is remembered
in memory until a second party appears or the one leaves), so a
one-person install writes no file (tested on a booted daemon). A
workspace-owned tile keeps an admin's exemption only while the record
shows no non-admin who could change it. **Unknown to xbind, and said so in
the docs:** who could change a tile before this record began, and changes
made to the workspace's files while xbind was not running.

**The record's gaps, closed in the re-review** (`signins/past.go`,
`broker/signinothers.go`): (a) a writer of a tile ABOVE can change a
nested tile (it is inside the read-write bind, unmasked) — editors, now
and in the record, include the ancestors' writers, and the card says
through which tile; (b) a workspace admin who is demoted, disabled or
deleted could change every tile — recorded once, workspace-wide
(`ObserveAdmins`), named on every tile until each person answers; an
answer covers who it named (`VoidPast` otherwise); (c) a tile whose
identity changed takes over the record of the tile at its path — nothing
is pruned; (d) a full record (200 parties) says "and others", which
still asks; (e) an admin's ownership is no party (admins are in the
trust base while they are admins), so a workspace with one account
writes nothing, also when a tile goes from that account to the
workspace. Still unknown to xbind: history before the record, changes
made while it was not running, and an admin demoted while it was down.

**The answer says what was shown** (`signins.TrustShown`, the re-review's
item 4): the basis says what the tile is; a page loaded before D245
re-fetches today's basis and still shows the old words. So the card, the
dialog and a ticked trust line send `shown: "signin+home"`; an answer
without it gets 409 `{changed, reload}` whose text tells the person to
reload (an old page cannot recover by itself: its words are in its code).

**What an answer means is versioned** (`signins.TrustMeans` = 2, the
basis prefix `tb2_`, `Trust.Means`): an answer given before D245 was for
the sign-in only. It is void (`VoidMeans`) and asked again with the words
as they are; a card or a stored approval's preview drawn before gets the
existing 409 and is drawn again.

**A session on the copy is confined like a member's, whoever opened it**
(`homePlan.confines`, `setHome` — review H1). An admin's sandbox keeps
CAP_SYS_ADMIN in its user namespace; the mount guard refuses only
`umount2`, `move_mount`, `open_tree` and `MS_MOVE`. So the tile's code in
an admin's copy session could bind the workspace root again without its
masks, remount it read-write and write `homes/<admin>/.bashrc` — or
remount the read-only workspace read-write and edit the admin's own tile.
The copy protected nothing for the people with the most to lose. Now a
copy session is always the restricted tier (D18: capabilities dropped, no
nested user/mount namespace), with the restricted view of the workspace
(D40), a member's cgroup leaf, and no host network; `home.confined` says
so to an admin, as do the session's first lines and the home dialog.
Trusting the tile gives new sessions there the admin's sandbox back. What
else an unrestricted session has, checked: the whole-workspace view (now
the allow-list view), the masks (the view binds no `homes`, `data`,
`.xbin`), devices (none differ by tier; GPU binds are the same request a
member can make), the admin pass (it is about attaching: a copy session
may be attached to by an admin, which is why a terminal token of a copy
session never opens a real-home one — below), host networking (refused
for an admin's confined session: it runs on the owner's network or
offline, and the session's first lines, its row — `home.netInstead` —
and the home dialog say which, and that trusting the tile restores it).
What the tile's code STILL has in an admin's confined session, stated in
the docs: every tile's source read-only (an admin's allow-list is every
tile), the unredacted `xbin.json`, a GPU if asked for, the connector's
model key, the session's API token with the admin's access riding on it. A VM session's guest has only what is
shared into it — the clone — whatever its tier. (Where the kernel has
Landlock the read guard also happens to refuse a session's mounts; the
rule does not rest on it, and the test runs with and without.)

Who the session is for:

| Opener | Home |
|---|---|
| the person (browser, app, device) | the rule, for them |
| their workspace agent (a delegate start, approved or by preference) | the rule, for the person. Trust is only ever the person's own answer (the approval's trust line records it before the open) |
| the root token (`homes/owner`) | the rule, as a workspace admin with no account: a workspace-owned tile nobody else can change is its own. Elsewhere the copy, confined — and it is ASKED: the root token keeps trust answers of its own under the key of its home (`signins.RootTokenUser`), given through the same dialog and route, basis-bound, voided and revocable like a person's. It never has a saved sign-in. **One seam, to delete when the token becomes a credential that only onboards**: `internal/signins/roottoken.go` (the key, the source wrapper), `term.homeUser` / `term.RootTokenAsks`, `internal/server/signinsroot.go` (the four routes, the partition gate's exception) |
| a terminal's token (`bx agent` inside a shell: the only tile credential that can open a session, and only on its own tile) | never more than the session it came from: **own** only when the rule says so now AND the session the token was minted for (`auth.Principal.TermRef`, a hash of the token) is that person's, on that tile, with the real home; else copy. Not "some session of theirs there": an admin may be attached to a copy session, and its token would open a real-home session they drive |
| a terminal token of the root token's shell (no account) | the root token's: the row above, with `homes/owner` — a one-person install's `bx agent` in a shell keeps the real home |
| any other component credential, view-as | they open no session (the gates refuse); the rule answers copy for them all the same |
| an admin attaching to / driving another person's session | a session with its person's REAL home is its person's alone: no admin pass (`adminPass`, `mayReattach`) — an admin typing there would be writing that home. A session on the tile's copy keeps today's admin pass. Ending a session is always allowed |

Where decision 5 of the brief ("a tile's credentials never get the real
home read-write") could not hold as written: a shell's `bx agent` on the
person's own tile. Giving it the copy would change a one-person install
(decision 7 forbids that) and protects nothing — the caller is code inside
a session that already has that home read-write. So the rule is "never
more than the session it came from", stated above and tested.

Fixed at the session's START and shown on its row; a running session never
switches. A restart, and a resume of a past session, never widen: one that
ran on the copy stays on the copy (its coding agent's own session files are
in the copy — `session/load` reads them there); one that ran on the real
home is judged anew. So after trusting a tile, NEW sessions get the real
home; the ones already open say they started on the copy.

A trust that stops holding (revoked, void, trusting switched off, the tile
no longer the person's own) ends the sessions that have the real home on
that tile, as the sweep already ends the ones that carry a sign-in — they
could otherwise keep writing. Sessions on the copy are never ended by a
trust change.

Without `--isolate` there are no mounts: every session is a host process
with the real home, as documented for the development mode. Nothing is
protected there and the row says so.

A session that is on no tile does not exist: both gates (`ServeWS`,
`OpenAgentWith`) refuse a directory outside a registered tile before any
home is mounted (`sessionDir`).

## 3. Every call site that mounts a home

All of them reach `(*Manager).prepare` → `shellCmd` → `sandboxShell`, the
one place a home is bound (`scopedBinds`, `scopedBindsView`). The plan is
made where `openOpts` is made — `openOptsFor`, the one constructor — and
the bind is made from the plan and nothing else.

| Call site | Path | Gets |
|---|---|---|
| a terminal (`GET /ws/term`) | `ServeWS` → `openOptsFor` → `create` | the rule |
| a guided sign-in's shell (`?purpose=signin`) | the same | the rule (a login made there on an untrusted tile lands in the copy) |
| an agent session (`POST /term/sessions`) | `OpenAgentWith` → `openOptsFor` → `createAgent` | the rule |
| a delegate / workspace-agent start | `OpenAgentWith` (`p.Agent`) | the rule, for the person |
| resume (`resume:` — `session/load`) | `OpenAgentWith` + `HistoryMeta.Home` | copy if the past session ran on the copy, else the rule |
| restart (`…/restart`, a protected primary's retarget) | `RestartAgentOnto` → `OpenAgentWith` (`AgentOpen.home`) | copy if the session ran on the copy, else the rule |
| `bx agent` in a shell (terminal token) | `OpenAgentWith` | never more than its sessions (above) |
| a VM sandbox (`vm=1`) | the same spec through `vm.Apply`: the jail mounts the home, the shim exports the jail's view | the same mount, so the same mode |
| a restricted (non-admin) session's allow-list view (D40) | `scopedBindsView` | the same bind |
| a partitioned tile | the same; its code directory is shared, so the rule is the tile's | the rule |
| the terminal relay (`/ws/term` attach by id) | no new mount: it joins a session | that session's mode; an admin has no pass into a real-home session of someone else |
| tile sandboxes (sandbox-manager tiles, `internal/tilesbx`) | mount no home | — |
| the guided sign-in's throwaway sandbox (`signins.Confined`) | an empty `HOME` of its own | — |
| confined tool runs (`internal/confine`), backends, builds | `homes/` masked | — |

## 4. The mechanism

xbind already overlays a base rootfs for every sandbox, inside the
sandbox's own namespaces (`internal/sandbox/init_linux.go` `mountRoot`:
fuse-overlayfs when xbind has one, else the kernel's overlayfs). The home
copy uses the same two, **the kernel's first** (`homeCopies.startHold`;
review L5): a FUSE mount is served to one kuid without `allow_other`, and
with it only to processes in the mounting user namespace or below
(`fuse_allow_current_process`) — sessions are siblings of the hold, so
with fuse-overlayfs a non-root process in a session got EACCES on the
whole home, in both settings (checked on this kernel). The kernel's
overlay is a filesystem like any other: every uid of the session's
mapping uses it. fuse-overlayfs is the fallback where the kernel can't
mount an overlay in a user namespace (before 5.11), with that limit
documented; a layer is only ever mounted by the flavour that wrote it
(its stamp, written after the first successful mount). The price of the
kernel flavour: no `redirect_dir` in a user namespace, so a directory that
exists in the real home can't be renamed in place (EXDEV; `mv` copies).
It cannot be mounted per session: several sessions of one person on
one tile share one upper, and two overlay mounts over one upper is unsafe
(two fuse-overlayfs each with its own cache; the kernel calls it undefined).

So there is **one overlay mount per (person, tile), held in a namespace of
its own** — a *hold* (`sandbox.StartHold`, `internal/sandbox/hold*.go`):

- a process started like any sandbox (`sandbox.Launch`: its own user, mount,
  pid, net, ipc and uts namespaces, the SAME uid/gid mapping sessions get —
  single-uid, or sub-uid ranges through `newuidmap`), whose init, instead of
  building a root, mounts the overlay on a private tmpfs, pivots into that
  tmpfs (it holds `/proc` and the mount, nothing of the host), and then
  serves one request: "a clone of the mount";
- a clone is `open_tree(OPEN_TREE_CLONE)` — a detached mount of the same
  superblock — sent to xbind over a socketpair (`SCM_RIGHTS`); xbind hands
  it to the session's init as an inherited fd (`sandbox.Bind.Mount`), and the
  init attaches it at `homes/<user>` with `move_mount` on the mount point it
  walked without following a symlink (`mountBindsNoFollow`);
- xbind counts the sessions on a hold (`internal/term/homecopy.go`); the
  last one to end ends the hold (SIGKILL of its pid 1, which takes
  fuse-overlayfs with it; the process is reaped before the count is
  released). A hold whose process dies while sessions use it ends them
  with the reason — never a home that answers ENOTCONN.

Nothing is ever mounted in the host's mount namespace: the overlay lives
in the hold's namespace and in the sessions', and dies with them. There is
no host mount to leak, and xbind never reads the merged view.

Flavours:

- **fuse-overlayfs**: the init opens `/dev/fuse`, mounts
  `fuse.fuse-overlayfs` itself with `default_permissions` and WITHOUT
  `allow_other`, and runs fuse-overlayfs on `/dev/fd/N`. Without
  `allow_other` the kernel serves the mounter's kuid/kgid only — which is
  root of every sandbox of this xbind (they all map 0 → xbind's uid) —
  whereas `allow_other`, which fuse-overlayfs asks for when it mounts by
  itself, limits access to the mounting user namespace and its children,
  and a session's namespace is a sibling. Directory renames work
  (`redirect_dir`).
- **kernel overlayfs** (`userxattr`; Linux 5.11+): no server process. A
  rename of a directory that exists in the real home returns `EXDEV` (`mv`
  copies; a tool that calls rename(2) itself on an old directory fails).

The real home is bound read-only inside the hold before it becomes the
lower layer; neither overlay writes to a lower layer by design.

The layer is stamped with the flavour that wrote it
(`layers.Stamps.Overlay`). One written by the other flavour is not mounted:
the session fails with the reason ("forget it").

**Fail closed (D78).** Under isolation a home copy that cannot be mounted —
no user namespaces for the hold, fuse-overlayfs missing while the layer is
stamped for it, the mount refused, `open_tree`/`move_mount` refused, the
flavour changed, the tile without an identity — fails the session with the
reason. There is no code path from a failed copy to a read-write bind of
the real home: the bind list is built from the plan, and a copy plan has no
source path to bind.

## 5. Where the layer lives, and its life

`data/home-copies/<home key>/<id>/` — `upper/`, `work/`, `meta.json` (the
tile's path, identity and name, when it was made, whether its person was
told), the flavour stamp. `<id>` is a hash of the tile's identity
(`broker.TileIdentity`: its directory's own — inode, birth time,
generation), so a tile made again at the same path gets a new, empty layer.
`data/` is mounted in no sandbox and masked in every terminal; the layer is
never inside the tile nor inside the real home; another person's sessions,
another tile's, and the tile's backend never see it.

- **Persistent** across the person's sessions on that tile (a `claude auth
  login`, shell history, a tool cache made there is there next time, on
  that tile).
- **Unused while the tile is trusted** — and kept, and said so in the
  person's list. Trusting merges NOTHING into the real home (that would be
  the attack); there is no "copy it over". When the trust is void again,
  sessions are back on the layer as it was.
- **Forget** ("Forget what this tile wrote to my home", `DELETE
  /agent/signins/homes/{id}`): the person's sessions on that copy are
  ended, the hold stopped, the layer removed by a confined run
  (`confine.RemoveAll`: its files may be other sub-uids'). **Forget ends
  the sessions first, and waits** (review L6b): it takes the layer's lock
  (a start that holds it finishes first), marks the layer as being
  forgotten (new starts are refused), ends the listed sessions, and waits
  up to 20 s for the hold to have no session — a session that was
  starting ends itself as it registers (`registered`). If one does not
  come up in time, forget fails and NOTHING is removed. Ending rather than
  refusing: "forget" is the person's way out of a copy a tile filled, and
  a refusal while a session of theirs runs there would make them hunt for
  it first.
- **Never deleted by xbind** (the re-review's item 1; the owner's rule
  for homes: "must not remove old ones / needs migration"). The first
  fix (L4) still deleted a copy whose recorded PATH was positively
  absent — but the layer is keyed by identity and the path was never
  updated, so a tile moved or renamed, a renamed parent, or a path
  missing at one rescan wiped its people's copies (agent transcripts
  included); a tile's owner could do it to everyone by renaming twice.
  Now nothing but "forget" removes a layer:
  - a copy **follows its tile by identity** (`copyTile`): when the
    identity is no longer at the recorded path and a registered tile has
    it (`Manager.TilePathOf`), `meta.json`'s path is updated — at the
    list, at a session's start (`prepareCopy`), at `FollowHomeCopies`;
  - a copy whose tile is **gone** (deleted; another identity at its path
    and none anywhere) is kept, listed to its person as "its tile is
    gone", with Forget. An identity that can't be read says nothing;
  - a **deleted account's** copies are kept (`EndHomeCopySessions` ends
    its sessions on them) and listed to admins (`kept` in `GET
    /agent/signins`), who forget them (`DELETE
    /agent/signins/homes/{id}?of=<user>`: the same confined removal;
    never a living account's). A re-created account of the same id has
    the same home key — and so its copies, as it has the home.
  - `FollowHomeCopies` (boot, and a rescan that changed the tiles) only
    reads each layer's `meta.json` and runs in a goroutine of its own:
    the watch loop and boot no longer wait on it (the sweep used to
    forget — 20 s + a confined run per copy — synchronously there).
  A workspace **restored from a backup** gives every tile another
  identity: every copy is listed as gone, every trust is void (asked
  again), nothing is removed.
- **Quota**: homes have no quota or limit in xbind today (checked: none in
  `internal/limits`, the broker's usage, the docs), so there is nothing to
  count against; the list shows each layer's size on disk (`du`, confined).
- A backup of `data/` includes the layers. They hold what untrusted code
  wrote: nothing reads them but the overlay.

## 6. What a person sees

- The session's row and the window's status bar: `home: yours`, or `home:
  this tile's copy (changes stay on this tile)` — a link to the docs
  section, and for the copy the trust question.
- The first session on a tile's copy says it once, in the session (a
  terminal's first lines; an agent session's notice): what changed, why,
  and how to get the old behaviour there — trust the tile, one click on
  the status bar's home chip.
- The trust card says what trusting means now — two things: "xbin uses
  your saved sign-in here" and "this tile's code can change your home" —
  and offers nothing that copies the layer over.
- The list of trusted tiles (the sign-in settings) shows each tile's copy,
  its size, "kept, not used while you trust this tile", and "Forget what
  this tile wrote to my home".
- iOS: the session row's words for the home mode; trust is decided on the
  web (the existing line).

## 7. Never break users

- The real home is never touched by this change: no migration, nothing
  moved. The first session after the upgrade on a tile that needs trust
  starts on an empty layer over the real home: every file is still there
  to read.
- What changes, on such tiles only: what is written under `~` there no
  longer follows the person to other tiles. Announced in the session,
  once; migration note `docs/changes/2026-11-18-home-on-untrusted-tiles.md`;
  changelog entry marked BREAKING.
- A one-person install sees no change: a sole admin on workspace-owned
  tiles needs no trust (`signinNotOwn`: the workspace's tiles are its
  admins'), the root token likewise, and `bx agent` in a shell keeps the
  home its shell has. Tested.
- An admin can no longer attach to another person's shell that has that
  person's real home (said in the note).
- A downgrade: an older xbind ignores `data/home-copies/` and mounts the
  real home read-write again (the old exposure returns, nothing is lost);
  the layers wait for the next upgrade.

## 8. Things thought about

- **uid mapping in the upper**: root in every sandbox is xbind's uid, so
  what a session writes as root is xbind's on disk; other in-sandbox uids
  are sub-uids (range mode) — in the real home too, today. A layer is
  removed by a confined run with the file capabilities.
- **whiteouts and xattrs**: char 0:0 devices (creatable in a user namespace
  since Linux 5.8) and `user.*` xattrs — ext4, xfs and btrfs have both;
  tmpfs has user xattrs from Linux 6.6 (the tests' scratch space).
- **hard links**: a link to a file of the real home copies it up first;
  the two names then share the copy, not the original.
- **sockets, FIFOs**: made in the upper; a socket that exists in the real
  home is connected to as before.
- **a file open O_RDWR / written**: copied up whole on the first write
  (a reflink where lower and upper share a filesystem that has them:
  btrfs, xfs). A large sqlite database in `~` costs its size once per
  tile. A reader that opened it before the copy-up keeps reading the old
  file (both overlays).
- **flock / fcntl locks**: per overlay — shared by all the person's
  sessions on that tile (one superblock), not with sessions on the real
  home.
- **the lower layer changes under a mounted overlay** — it is the
  person's real home, which their sessions on their own tiles write. The
  kernel calls that undefined, and in practice it is stale names: the
  overlay keeps every name it has looked up (a file since removed stays
  readable, one since made stays "not there", one replaced by a rename
  keeps its old content) for as long as the hold lives — and a hold
  lives as long as any session on that copy, a moment longer than the
  last one's DELETE. `make integration` found it (`test/llm`
  `TestSessionsOnConnector`: a login removed from the real home was
  still seen by the next session). So the hold drops the unused names
  before it hands out each clone (`sandbox.holdRefresh`: a reconfigure
  of the overlay's superblock that changes nothing —
  `reconfigure_super` → `shrink_dcache_sb`): a session that starts sees
  the real home as it is now, and the open ones do from then on, for
  what they don't hold open. Best effort (a kernel that refuses serves
  the clone as before); fuse-overlayfs keeps its own table, so on the
  fallback a change is seen once every session on the copy has closed.
  `TestHomeCopySeesTheRealHomeAsItIsNow`, red without the refresh.
- **teardown**: no host mounts. A session's clone goes with its mount
  namespace; the hold with its last session.
- **two sessions at once**: one hold, two clones of one mount. A start
  racing the last session's end waits for the dying hold, then starts a
  new one (per-key lock).
- **performance**: the overlay mount costs nothing per file. The first
  session on a (person, tile) starts the hold (one more sandbox launch);
  later ones ask it for a clone. Measured numbers in the change's report.

## 9. What remains — reads

Code in an untrusted tile can still READ the person's home: reads are
unchanged. A `claude auth login` kept in `$HOME`, an SSH key, a
`~/.netrc`, the harnesses' transcripts (residual 9 of signin-store.md) are
readable there as they always were. Recommended: use a saved sign-in
(D230), which never enters a session on a tile the person has not trusted.

Hiding credential directories on untrusted tiles (not built) would take:
a list of paths per harness and tool (`~/.claude/.credentials.json`,
`~/.codex/auth.json`, `~/.ssh`, `~/.netrc`, `~/.config/gh`, `~/.gnupg`,
`~/.aws`, …) masked over the copy — whiteouts made by xbind in a fresh
layer, or tmpfs covers — and a decision on what breaks (git over SSH and
the tile's own logins on shared tiles); the list is never complete, so the
honest end of that road is a home per tile with nothing of the real home
under it, and an explicit, per-file "bring this along" by the person.

## 10. Tests (each shown red without its rule)

Without sandboxes (`make test`):

- `internal/term/home_test.go` — `TestHomeRule` (the table: own tile,
  another person's, an org's, the workspace's for an admin and a
  non-admin, a co-editor, trusted, a void trust, trusting off, the root
  token, a delegate, a terminal's token, other credentials, view-as, a
  kept copy, no identity, no tile), `TestHomeRuleWithoutSandboxesOrRule`
  (no sandboxes; no rule wired; an undecided home is never bound; a copy
  that can't be mounted is an error), `TestHomeLayerStore` (one layer per
  person and tile identity, one hold shared and ended with the last
  session, the flavour stamp, the note once, the list, forget, a tile
  made again, the sweep that keeps an unregistered tile's layer, the
  account's deletion), `TestHomeLayerDeadHold`, `TestHomeSweep`,
  `TestHomeAdminPass`, `TestHomeTerminalToken`, `TestHomeWords`;
- `internal/boot/homecopy_test.go` — `TestHomeCopyRoutes` (the wiring on a
  booted daemon, `home`/`homes` in `GET /agent/signins`, forget and who
  may, the sweep, a trust given with saved sign-ins off, the account's
  deletion);
- the words: `hack/agent-signins.test.mjs`, XbinTerm's
  `TermDirectoryTests.rowsCarryTheSessionsHome`; the docs:
  `internal/docscheck`.

Under isolation, real sandboxes, both overlay flavours (the `term` and
`sandbox` integration suites):

- `internal/sandbox/hold_linux_test.go` — `TestHoldClones` (one overlay,
  two sandboxes; the lower never written), `TestHoldFailsClosed`,
  `TestHoldCycles` (50 holds: no process, no mount);
- `internal/term/home_linux_test.go` — `TestHomeCopyAttack` (bob's tile;
  alice's session reads her home, the tile's code plants
  `~/.claude/settings.json`, `~/.gitconfig`, `~/.bashrc`, a `~/.local/bin`
  tool and an ssh config; her real home is byte-identical — a hash of the
  tree; her own tile has none of it; the layer persists there, is
  invisible from another tile and to another person; a restricted session
  on the allow-list view), `TestHomeCopyTrustAndForget` (trust → the real
  home, the layer untouched; a new editor voids it → the real-home
  session ends, a copy session elsewhere stays, the next is on the layer
  as it was; forget; the tile made again → a new layer; the sweep),
  `TestHomeCopySoleAdmin` (an admin account and the root token on the
  workspace's tile: no hold, no layer, the real home written),
  `TestHomeCopyFailsClosed` (a broken fuse-overlayfs, a foreign flavour,
  an upper that is a file, a tile without identity: refused with the
  reason, the real home unchanged), `TestHomeCopyCycles` (two sessions at
  once share one mount; 50 terminals: no hold, no process, no mount, no
  lost write), `TestHomeCopyAgentSessions` (an agent session, a delegate's
  start, resume and restart stay on the copy after the tile is trusted, a
  new session has the real home), `TestHomeCopyPerf` (opt-in numbers).

End to end on an `xbind --isolate` with accounts (`test/isolated`):
`TestHomeOnATileThatIsntOnlyYours` (the attack through the broker's own
source, the row, an admin's attach to a copy session and its refusal on a
real-home one, trust, untrust ending the session, forget) and `…VM` (a
Firecracker guest on the copy). In a browser: the UI harness pass
`termHome` (`HARNESS_ISOLATE=1`).

Red runs: fifteen mutations, each removing one rule, each failing the
tests named for it (the change's report lists them).

The review's findings (2026-11-18), each with its test and a red run:

| Finding | Rule | Tests |
|---|---|---|
| H1 an admin's copy session could mount its way to the real home | a copy session is confined (`setHome`) | `TestHomeCopyConfinesAdmins`; under isolation `TestHomeCopyAdminIsConfined` — the two mount attacks, an admin and the root token, with and without the Landlock guard: the real home and the own tile byte-identical; red: the real home IS written |
| M1 `bx agent` in the root token's shell got a copy | `rootHome` | `TestHomeTerminalToken`, `TestHomeCopyBxAgentInAShell` |
| M2 a session being opened is invisible to a sweep | `Manager.judged`, `registered` | `TestHomeJudgedAgainAtRegistration`, `TestHomeCopyStartInFlight` (a start held before its registration) |
| L1 any real-home session vouched for a terminal token | `minterHasRealHome` | `TestHomeTerminalToken`, `TestHomeCopyBxAgentInAShell` |
| L2 a pre-D245 trust also opened the home | `TrustMeans`, `VoidMeans` | `TestTrustCoversThePastAndWhatItMeans` |
| L3 the home dialog's trust button was not armed | bx-dialog `armed` | the `termHome` harness pass: an early click, a script's click and Enter trust nothing |
| L4 a copy deleted on a transient error | `tileAbsent` | `TestHomeLayerStore`, `TestHomeCopyTrustAndForget` |
| L5 non-root processes refused their home | kernel overlay first | `TestHomeCopyNonRootUser` (sub-uid mapping), `TestHomeLayerStore` |
| L6a the host's switch | `signinTrustOff` in `homePlanFor` | `TestHomeTrustSwitchedOffByTheHost` |
| L6b forget under a start | the lock, the wait | `TestHomeForgetDuringAStart`, `TestHomeCopyCycles` |
| re-review 1: copies deleted by a move | nothing removes a copy; it follows its tile | `TestHomeLayerStore`, `TestHomeCopyTrustAndForget` (a rename keeps the copy), `TestHomeCopyRoutes` (a deleted account's copies kept, an admin's forget) |
| re-review 2: the root token locked out | its own trust answers (the seam) | `TestRootTokenTrust`, `TestHomeRootTokenTrust`, `TestHomeCopyAdminIsConfined`, `TestHomeCopyRoutes`, e2e `TestHomeRootTokenTrusts` |
| re-review 3: nested tiles, former admins, identity, the cap, one account | `codeWritersAbove`, `ObserveAdmins`, carry-over, `More`, `pastOwner` | `TestPastAdminsIdentityAndTheCap`, `TestTrustCoversWhoItNamed`, `TestHomeCopyRoutes` |
| re-review 4: an old page's answer | `TrustShown` | `TestHomeCopyRoutes`, `TestDelegateStartDefaultsSavedSignIn`, the plan test, e2e `TestHomeRootTokenTrusts` |
| re-review 5: the sign-in frame sent before the judgement | `registered` before `drv.Start` | `TestSigninNotSentToASessionJudgedGone` |
| re-review 6: host network moved silently | `home.netInstead`, the words | `TestHomeCopyConfinesAdmins`, the JS word tests |
| sticky past | `signins.Past` | `TestPastRemembersWhoCouldChangeATile`, `TestHomeRuleRemembersPastEditors`, `TestHomeCopyRoutes` (a one-person install records nothing) |

## 11. Not done, known

- Reads are open (§9).
- **OPEN follow-up (not built): a nested user + mount namespace after the
  root is built**, for every terminal and agent session. Today a session's
  processes are root in the user namespace that OWNS its mount namespace,
  so whatever keeps CAP_SYS_ADMIN there can rearrange the mounts the
  sandbox made (the guard is a seccomp block-list of four calls). Entering
  a child user namespace (and a mount namespace owned by it) once the root
  is built would make every mount the init made *locked* for the workload:
  it could keep mounting inside its own tree (tmpfs, overlays, bind mounts
  of what it sees) but could not remount, unmask or move anything it
  inherited, by the kernel's own rule rather than a filter. That would let
  an admin's copy session keep `mount` — and would harden the real-home
  session too. It needs: the uid/gid mapping composed through two levels
  (sub-uid ranges), apt and the file capabilities checked inside the
  nested namespace, the D18 ucount pin moved one level down, containers
  (`s.Containers`) and VM sandboxes re-checked.
- **An admin's REAL-home session on a tile runs that tile's code with the
  admin's sandbox powers** — CAP_SYS_ADMIN in the session's user
  namespace: the workspace can be remounted read-write, masks peeled where
  Landlock is not there. Pre-existing and by design (an admin's terminal
  is a dev sandbox, not a boundary against the tile they chose to work
  on); what is new is that it is now gated by the trust answer on tiles
  that aren't the admin's own.
- The hold's process — and on the fallback its fuse-overlayfs — are
  xbind's children, outside the session's cgroup leaf (review L6c): their
  memory and CPU are not counted against a tile's limit. Kept: the kernel
  flavour's hold is one idle process (it serves a clone per session start
  and nothing else; the file operations run in the sessions' own tasks, in
  their cgroups); the fallback's fuse-overlayfs does only what the
  sessions' file operations ask and keeps a bounded inode cache. Putting
  it in a leaf of its own needs a cgroup that outlives each session and is
  shared by several (the leaf is per session today) — not simple, so it
  is a documented residual (docs/agent-signins.md, "Admins").
- **Two walks over "who can change a tile's code"** since D246 was
  merged: this rule's (`broker.codeWritersAbove`: a tile's writers and
  those of every tile it lies IN) and `access.set`'s
  (`broker/accessreach.go`: a raise on T reaches T, every tile nested in
  T, a tile T lies inside — whose build and pages may take in files
  beneath it — and the tiles that build or load T's code). They agree on
  the direction this rule has (an ancestor's writers change a nested
  tile); accessreach is WIDER: for it, a writer of a tile nested in T,
  or of a tile T builds with, also reaches T's code. This rule does not
  count those as T's editors yet. Not refactored here (the other
  package's); one shared walk is the follow-up. A person given `write`
  through an approved `access.set` line is seen like any other new
  editor (`TestAccessSetIsSeenByThePastRecord`): the apply fires
  OnPeopleChanged, where trusts are revalidated and the record written.
- A copy stamped fuse-overlayfs keeps the non-root lockout after the
  kernel is upgraded (the re-review's item 7). Decided: **not migrated**
  — the two overlays keep whiteouts and opaque directories differently
  (fuse-overlayfs: `.wh.` files and its own xattrs; the kernel:
  character devices and `user.overlay.*`), so re-stamping a written
  upper would resurrect deleted files or hide directories. The docs say
  how to get a kernel copy: forget it, start a session.
- On the fuse-overlayfs fallback (a kernel before 5.11) only root in a
  session can use the home on a copy (§4). Not fixable from outside the
  kernel short of nesting the sessions' user namespaces under the hold's.
- An admin's confined session is refused host networking also where the
  owner's network sets grant `host` to members (`TermNet.HostOK` folds the
  two together).
- `HomeLogin` — the launcher's in-memory hint "this coding agent is signed
  in on its own in your home" — is kept per home, not per (home, tile): a
  login made on a tile's copy can read as "signed in" elsewhere until a
  session there looks. A hint, never a decision.
- A home that has mounts of its own beneath it shows their mount points,
  not their contents, on a copy (an overlay does not cross mounts).
- fuse-overlayfs in a hold keeps CAP_SYS_ADMIN of its own user namespace
  (as the one serving every sandbox's root does); the real home is bound
  read-only beneath it, which a compromised fuse-overlayfs could undo. The
  kernel flavour has no such process.
