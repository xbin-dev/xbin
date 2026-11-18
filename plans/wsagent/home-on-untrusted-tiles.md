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
as the sign-in's: one trust per (person, tile identity).

Who the session is for:

| Opener | Home |
|---|---|
| the person (browser, app, device) | the rule, for them |
| their workspace agent (a delegate start, approved or by preference) | the rule, for the person. Trust is only ever the person's own answer (the approval's trust line records it before the open) |
| the root token (`homes/owner`) | the rule, as a workspace admin with no account: a workspace-owned tile nobody else can change is its own. It cannot keep a trust answer (answers are an account's), so on every other tile it gets the copy |
| a terminal's token (`bx agent` inside a shell: the only tile credential that can open a session, and only on its own tile) | never more than the sessions it came from: **own** only when the rule says so now AND a live session of that person on that tile already has the real home; else copy |
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
copy uses the same two, chosen the same way (`sandbox.FuseOverlayfs()`),
but it cannot be mounted per session: several sessions of one person on
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
  (`confine.RemoveAll`: its files may be other sub-uids').
- **Deleted with the tile**: at every rescan and at boot, a layer whose
  tile directory is gone or has another identity is removed. A tile that
  is merely unregistered keeps its layers (its directory is still that
  directory).
- **Deleted with the account** (`OnUserSignedOut(deleted)`), and the
  holds ended.
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

## 11. Not done, known

- Reads are open (§9).
- The hold's process and its fuse-overlayfs are xbind's children, outside
  the session's cgroup leaf: their memory is not counted against a tile's
  limit.
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
