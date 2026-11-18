# Your home is a copy on a tile you haven't trusted

**BREAKING for anyone who relied on what they wrote under `~` on one tile
showing up on their other tiles** — on tiles that aren't only theirs.
A security fix: it closes a hole present in every release so far.

## What changed

A person has one home directory (`homes/<user>`, `~` in every terminal and
coding-agent session). Until now every session mounted it read-write, on
every tile.

Now, **on a tile whose source isn't only yours and that you haven't
trusted**, your sessions mount *this tile's copy* of your home:

- they **read** your real home exactly as before (git identity, SSH keys,
  dotfiles, tool settings, logins);
- what they **write** under `~` stays on that tile — kept for you there,
  from session to session — and never reaches your real home.

A tile "isn't only yours" when it is owned by another person, an org, or
the workspace (unless you are a workspace admin), or when someone else who
isn't a workspace admin can change its code — **or could before**: from
this release xbind remembers, per tile, who it has seen able to change
the code, and a tile someone else could change keeps asking for your
trust after they lose that access (what they left is still there). It
can't know who could change a tile before this release. That is the rule a saved
coding-agent sign-in already follows, and the same answer — **"I trust
this tile's source"** — switches both: trusting a tile now means *your
sign-in is used there* and *that tile's code can change your home*.

On your own tiles, and on tiles you trusted, nothing changes: your real
home, read-write.

Also:

- A session says which home it has: `home: yours` or `home: this tile's
  copy (changes stay on this tile)` — on its row (`home` in `GET
  /term/sessions`), in the window's status bar, and once in the first
  session on a copy.
- `GET /agent/signins` gains `home` (with `?tile=`) and `homes`; `DELETE
  /agent/signins/homes/<id>` forgets a copy
  ([protocol](../protocol/signins.md#your-home-on-a-tile-that-isnt-only-yours)).
- A session that has a person's **real** home is that person's alone: a
  workspace admin can no longer attach to, or drive, someone else's such
  session (they can still end it). Sessions on a copy keep the admin pass.
- **A workspace admin's session on a copy is confined like a member's**
  until they trust the tile (the root token's too): the restricted
  sandbox — no mounts, no new namespaces, the workspace seen read-only
  through the tiles they may read, a member's resource limits, no host
  network. An admin's sandbox can mount, and could mount its way around
  the copy. The session's first lines and the home dialog say so.
- **Every "I trust this tile's source" given before this release is
  void** and asked again: it was given for the sign-in only, and now also
  lets the tile's code change your home. A card or approval that was open
  across the upgrade is refused (409) and drawn again with the new words.
- A session opened with a terminal's own token (`bx agent` inside a shell)
  has the real home only when **that terminal** has it. In the root
  token's terminal it is the root token's.
- Stopping to trust a tile (or the trust going void) now also ends your
  sessions there that have your real home.
- Under `--isolate`, a copy that can't be mounted **fails the session**
  with the reason; the real home is never mounted in its place.
- **xbind never deletes a copy on its own.** A tile that is moved or
  renamed keeps its people's copies. A tile that is deleted or made
  again — a workspace **restored from a backup** looks like that for
  every tile: trust answers are asked again, sessions start new copies —
  leaves its copies listed ("its tile is gone — kept until you forget
  it"). A deleted account's copies are kept and listed to workspace
  admins (*Coding-agent sign-ins → Kept from deleted accounts*), who
  forget them.
- **The root token is asked too.** On a tile that isn't the workspace's
  its sessions are on the copy, confined; the home line's dialog has the
  same "I trust this tile's source", and the token's own answer gives its
  new sessions there the real home. (It still never has a saved sign-in.)
- **A trust answer says what was shown** (`shown: "signin+home"` with
  `POST /agent/signins/trust` and with a ticked trust line). A page that
  was open across the upgrade is told to reload.
- Who can change a tile now includes who can write **a tile it lies in**,
  and a **workspace admin who stops being one** is remembered: every tile
  asks each person once after it.

## Who's affected

- **A one-person workspace: nobody.** A sole admin's sessions on the
  workspace's own tiles need no trust and keep the real home; so does the
  root token there; so does `bx agent` in a shell. **If you live on the
  root token** and work on tiles owned by an account or an org: the
  first terminal there is on a copy, confined — click the home line in
  its status bar and trust the tile, once, for the real home.
- **People on shared workspaces**, on tiles owned by someone else, an org
  or the workspace, or that a colleague can edit: a `claude auth login`,
  a `git config --global`, an `npm login`, shell history or a tool
  installed under `~` *while working on such a tile* now stays on that
  tile. Everything already in your home is still there to read: nothing
  is moved, removed or migrated, and the first session after the upgrade
  starts on an empty copy over your real home.
- **Admins** who attach to other people's terminals to help: possible on
  tiles where that person works on a copy, refused where the session has
  their real home. **Admins working on a person's or an org's tile**: the
  terminal there is a member's until you trust the tile — `mount`,
  `unshare`, host networking and the whole-workspace view are gone in
  that session. Tiles the workspace owns are unaffected (unless a
  non-admin could change one since this release).
- **People who had trusted tiles**: asked once more per tile; until then
  those tiles' sessions start without the saved sign-in and on a copy.
- **Workspaces without sandboxes** (no `--isolate`): no change, and no
  protection — every session runs as xbind's own user with the real home.
- **Hosts**: the copy is an overlay mounted inside sandboxes — the
  kernel's overlayfs (Linux 5.11+), so every user in a session can use
  its home; on an older kernel fuse-overlayfs (it ships beside `xbind`),
  where only root in the session can (a non-root process gets
  "permission denied" on `~`). It needs `open_tree`/`move_mount` (Linux
  5.2+). On the kernel's overlay a directory that exists in the real home
  can't be renamed in place. Copies are kept in `data/home-copies/`; who
  could change each tile, in `data/signins/tiles.json`.

## How to migrate

Nothing to do to keep working. To get the old behaviour **on a tile you
trust**: click the home line in that tile's window (its status bar) and
choose **I trust this tile's source** — once per tile. New sessions there
then use your real home; sessions already open stay on the copy until you
start them again.

Something you set up on an untrusted tile and want everywhere (a login, a
global git setting): do it again on one of your own tiles. xbind does not
copy a tile's copy into your home — that would let the tile's code write
there after all.

To drop what a tile wrote: **Forget what this tile wrote to my home**, in
the same dialog or under *Coding-agent sign-ins → What tiles wrote to your
home*.

Prefer a [saved sign-in](../agent-signins.md) to a login kept in `~`: it
is never in your home, and never enters a session on a tile you haven't
trusted.

## Why

Code in a tile runs in the sessions people open there — a project hook, a
build script. With the person's real home writable, a tile someone else
controls could plant `~/.claude/settings.json` hooks, `~/.gitconfig`
(`core.fsmonitor`, `core.hooksPath`), `~/.bashrc` or `~/.local/bin/…`,
and that file ran later in a session on the person's *own* tile, where
their saved coding-agent sign-in is in the environment. The sign-in rule
kept the sign-in out of untrusted tiles; this keeps untrusted tiles out of
the home.

What it does not do: code on an untrusted tile can still **read** your
home. Design and the threat table: D245.
