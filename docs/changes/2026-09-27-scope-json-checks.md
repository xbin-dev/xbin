# 2026-09-27 — scope.json: checked resource names, one scope per data key (D118)

## What changed

**Resource names are checked.** A resource name, in a scope.json or in the
workspace xbin.json's `resources`, must be letters, digits, `.`, `_` and
`-`, start with a letter or digit, and be at most 64 characters. Any other
name (`a/b`, `..`, `../x`, spaces, non-ASCII) is refused. The resource is
not provisioned (no directory, no encrypted mount, no kv bucket), and grants
can't address it. Every tile in the scope carries a `manifestError` that
starts with `scope.json (<scope>):` and names it. A workspace-level one is
left out and logged by xbind, and the file itself is left as written.
Restoring a backup that names an invalid resource fails.

**One scope per data key.** A scope's resource data lives under its *data
key*: the scope path with `/` written as `~`, so `apps/thing` becomes
`apps~thing`. That covers `data/resources-enc/<key>/`,
`data/resources/<key>/` and the encryption key derived for each resource.
Two paths can have the same key: `apps/thing` and a directory literally
named `apps~thing`, or a scope at a top-level `workspace/` and the
workspace-level resources (key `workspace`). Before this change such scopes
shared encrypted volumes. Now each key has one holder:

- the workspace-level resources always hold `workspace`;
- otherwise the scope that had the key first. xbind records the holder in
  `data/scope-keys.json` the first time a key is contested, so a restart
  doesn't change it.
- if two claimants appear with no history (both existed before this
  release, or both appeared in the same scan), neither holds the key.

A scope that doesn't hold its key is still a scope: its tiles keep their
scope and same-scope grants. But none of its resources are provisioned,
mounted, backed up, removed or restored, and its tiles carry a
`manifestError` starting with `scope.json (<scope>):` that names the
holder. `bx doctor`, `bx ls`, the shell and the admin console show it.
Creating a tile at a path whose key another scope has (e.g. `apps~thing`
next to `apps/thing`, or `workspace`) is refused by every creation path:
create, clone, git import, builtin import and template instantiate.

Nothing is rekeyed. Every existing scope keeps its data where it is.

## Who's affected

- **Scopes with a resource name outside the rule.** None of the shipped
  tiles, templates or examples have one. A name with `/` or `..` never
  worked as intended. Names with spaces or other characters did work, and
  now need renaming.
- **Workspaces with a pair of scope paths that differ by `/` versus `~`, or
  a scope directory named `workspace` at the top level.** Their data was
  already shared between the two scopes. After the upgrade, the scope that
  was there first keeps it. If both predate this release, neither does
  until one is renamed.

## How to migrate

1. Run `bx doctor` and look for lines starting with `scope.json (`.
2. **An invalid resource name:** rename it in scope.json, and in the `uses`
   entries and `XBIN_RES_<NAME>` reads that refer to it. The data lives
   under the old name, so a renamed resource starts empty. If you can, copy
   the data out through the tile before upgrading and back in after. The
   old encrypted directory stays on disk, unmounted and untouched. A backup
   made before the upgrade holds its files in plain form under the old
   name: a workspace admin can extract them from the archive, but not
   restore it as a whole (a restore refuses invalid names).
3. **A data key held by another scope:** rename the directory of the scope
   that should not hold it, for example `apps~thing` to `apps-thing`.
   Anything it stored under the shared key belongs to the holder, so copy
   what it needs through the holder's API or a backup before renaming. When
   both claimants were refused, renaming either one gives the other the
   key.

## Why

A resource name became a directory under `data/resources-enc/<key>/` and
`.xbin/resenc/<key>/`. xbind created that directory, ran `gocryptfs -init`
in it and mounted it for every declared resource, granted or not. scope.json
sits in a tile's directory, which the tile's terminals and coding agents
write. So a name like `../../../x` let anyone with a terminal on a tile make
xbind create, initialize and mount directories anywhere its user can write,
including over other tiles or the workspace's own state. A name with `/`
could also share another scope's kv bucket in backups and offload.

Two scopes with one data key shared the same gocryptfs volumes, since the
volume's directory, mount point and password all derive from the key. So a
tile that could create a directory named like another scope's key could
read and write that scope's files. A scope at `workspace` also shared the
workspace-level kv buckets in backups, offload and restore. Offloading or
backing up one scope removed or archived the other's data. Keying data by
the full path would move every workspace's data, so the key stays and gets
a single holder.

Both close security holes, so they take effect in this release
(docs/compat.md rule 11).
