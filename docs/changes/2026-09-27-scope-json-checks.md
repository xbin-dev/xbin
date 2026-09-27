# 2026-09-27 — scope.json: one scope per resource data key (D118)

## What changed

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

Only workspaces with a pair of paths that differ by `/` versus `~`, or a
scope directory named `workspace` at the top level. Their data was already
shared between the two scopes. After the upgrade, the scope that was there
first keeps it. If both predate this release, neither does until one is
renamed.

## How to migrate

1. Run `bx doctor` and look for lines starting with `scope.json (`.
2. Rename the directory of the scope that should not hold the key, for
   example `apps~thing` to `apps-thing`. Anything it stored under the
   shared key belongs to the holder; copy what it needs through the holder's
   API or a backup before renaming.
3. When both claimants were refused, renaming either one gives the other
   the key.

## Why

Two scopes with one key shared the same gocryptfs volumes, since the
volume's directory, mount point and password all derive from the key. So a
tile that could create a directory named like another scope's key could
read and write that scope's files. A scope at `workspace` also shared the
workspace-level kv buckets in backups, offload and restore. Offloading or
backing up one scope also removed or archived the other's data. Keying data by the full path would
move every workspace's data, so the key stays and gets a single holder.
This closes a security hole, so it takes effect in this release
(docs/compat.md rule 11).
